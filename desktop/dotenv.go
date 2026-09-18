package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/fileutil"
	"github.com/zzycxz/hiq/internal/secret"
)

// dotenvMu serializes read-modify-write of the legacy credentials file and
// ~/.env. Multiple desktop tabs boot concurrently and each calls
// promoteProviderKeysToCredentials; without serialization their interleaved
// read/modify/write drops whichever write lands in the middle — a configured
// key silently vanishes. The mutex covers the whole promotion so upsert +
// remove act as one transaction. (The encrypted store has its own internal
// mutex; this one guards only the plaintext-file cleanup around it.)
var dotenvMu sync.Mutex

// credentialsPath is the LEGACY plaintext global credentials file — what the
// settings panel and `hiq setup` wrote API keys to before the encrypted
// secret store existed. It is read-only now: startup migrates it into the
// encrypted store (see migrateCredentialsFile) and every writer scrubs keys
// from it. Never a project .env: keys stay out of the user's project tree.
func credentialsPath() string {
	if p := config.UserCredentialsPath(); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".env")
	}
	return ".env"
}

// credentialStore returns the encrypted store credential writes go to. A fresh
// Store per call rather than secret.Default()'s singleton: DefaultPath()
// re-resolves the user config dir each call, so tests that point AppData/HOME at
// a temp tree (isolateDesktopUserDirs) write there instead of the developer's
// real store — the singleton would stay bound to the first path it saw for the
// whole process. All writes are file-based read-modify-write cycles guarded by
// the Store's own mutex, so separate instances over the same path stay safe.
func credentialStore() *secret.Store {
	return secret.New(secret.DefaultPath())
}

// upsertCredential stores a secret in the encrypted secret store (DPAPI on
// Windows, AES-GCM elsewhere) under the given env-var name — the one a
// provider's api_key_env points at — and applies it to the running process so a
// rebuild picks it up without a restart. A lingering plaintext copy in the
// legacy credentials file (pre-encryption installs) is scrubbed so the
// encrypted entry is the only value left on disk.
func upsertCredential(key, value string) error {
	dotenvMu.Lock()
	defer dotenvMu.Unlock()
	if err := credentialStore().Set(key, value); err != nil {
		return err
	}
	_ = removeEnvFile(credentialsPath(), key) // best-effort legacy scrub
	return os.Setenv(key, value)
}

// removeCredential deletes a secret from the encrypted store and unsets the
// live process env so the provider immediately becomes unauthenticated. The
// legacy plaintext copy, if any, is scrubbed too.
func removeCredential(key string) error {
	dotenvMu.Lock()
	defer dotenvMu.Unlock()
	if err := credentialStore().Delete(key); err != nil {
		return err
	}
	_ = removeEnvFile(credentialsPath(), key)
	return os.Unsetenv(key)
}

// migrateCredentialsFile lifts the legacy plaintext credentials file into the
// encrypted secret store at desktop startup (the CLI/chat boot path runs the
// same sweep in boot.go; whichever gets there first wins). Best-effort: on
// failure the plaintext file is kept and keeps loading via config.loadDotEnv,
// so nothing breaks — the next startup finishes the migration.
//
// Only the hiq-owned credentials file qualifies. When the user config dir
// can't be resolved, credentialsPath() falls back to ~/.env — that file is the
// USER's own shell env (also loaded by config as a legacy fallback), so
// consuming it into the store and deleting it would destroy user data.
func migrateCredentialsFile() {
	p := config.UserCredentialsPath()
	if p == "" {
		return
	}
	if n, err := credentialStore().MigrateEnvFile(p); err != nil {
		slog.Warn("credentials: migration to encrypted store failed; keeping plaintext file", "path", p, "err", err)
	} else if n > 0 {
		slog.Info("credentials: migrated into encrypted secret store", "count", n, "from", p)
	}
}

func removeEnvFile(path, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return os.Unsetenv(key)
		}
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	outLines := make([]string, 0, len(lines))
	for _, ln := range lines {
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "export "))
		if t == "" || strings.HasPrefix(t, "#") {
			outLines = append(outLines, ln)
			continue
		}
		if k, _, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == key {
			continue
		}
		outLines = append(outLines, ln)
	}
	out := ""
	if len(outLines) > 0 {
		out = strings.Join(outLines, "\n") + "\n"
	}

	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, "credentials.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(out); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := fileutil.ReplaceFile(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Unsetenv(key)
}

// envFileKeys returns the set of KEY names assigned in a KEY=value file, empty
// when the file is absent.
func envFileKeys(path string) map[string]bool {
	keys := map[string]bool{}
	data, err := os.ReadFile(path)
	if err != nil {
		return keys
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimPrefix(strings.TrimSpace(raw), "export ")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, _, ok := strings.Cut(line, "="); ok {
			keys[strings.TrimSpace(k)] = true
		}
	}
	return keys
}

// promoteProviderKeysToCredentials copies any configured provider api_key_env that
// currently resolves (from a project .env, ~/.env, or the OS env) into the
// encrypted secret store when it isn't there yet, so a key set for one workspace
// follows the user across every project. Promoted keys are then stripped from
// ~/.env so the encrypted store is the single source of truth; a project's own
// .env is user-owned and left untouched.
func promoteProviderKeysToCredentials(cfg *config.Config) {
	dotenvMu.Lock()
	defer dotenvMu.Unlock()
	store := credentialStore()
	have := map[string]bool{}
	if keys, err := store.Keys(); err == nil {
		for _, k := range keys {
			have[k] = true
		}
	}
	// Pre-migration installs may still hold a key only in the legacy plaintext
	// file; count those as present so promotion doesn't duplicate them.
	for k := range envFileKeys(credentialsPath()) {
		have[k] = true
	}
	for _, p := range cfg.Providers {
		env := strings.TrimSpace(p.APIKeyEnv)
		if env == "" || have[env] {
			continue
		}
		val := os.Getenv(env)
		if val == "" {
			continue
		}
		if err := store.Set(env, val); err != nil {
			continue
		}
		have[env] = true
		removeHomeEnvKey(env)
		_ = removeEnvFile(credentialsPath(), env)
	}
}

// removeHomeEnvKey deletes a single KEY=value assignment from ~/.env (the legacy
// fallback the old migration wrote to), leaving every other line intact. No-op when
// ~/.env is absent or the credentials store resolves to ~/.env itself.
func removeHomeEnvKey(key string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(home, ".env")
	if sameConfigPath(path, credentialsPath()) {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var kept []string
	removed := false
	for _, raw := range strings.Split(string(data), "\n") {
		check := strings.TrimPrefix(strings.TrimSpace(raw), "export ")
		if k, _, ok := strings.Cut(check, "="); ok && strings.TrimSpace(k) == key {
			removed = true
			continue
		}
		kept = append(kept, raw)
	}
	if !removed {
		return
	}
	_ = os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o600)
}
