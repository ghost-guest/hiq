package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/zzycxz/hiq/internal/secret"
)

// secretEnvOnce bounds the encrypted-store env injection to once per process:
// loadDotEnvForRoot runs on every config load, but the store's contents only
// change through explicit Set/Delete, which mirror into the process env
// themselves (see desktop's upsertCredential and the CLI's storeSecretLines).
var secretEnvOnce sync.Once

// loadDotEnv loads KEY=value files into the process environment without
// overriding variables that are already set (first file to set a key wins).
// Order: a project ./.env (read-only back-compat, so a manual project override
// takes precedence), then the hiq-owned global credentials file in the user
// config dir (legacy plaintext; boot migrates it into the encrypted secret
// store, after which it no longer exists), then ~/.env as a legacy fallback.
// Existing environment variables always win over all three. Finally the
// encrypted secret store (DPAPI on Windows, AES-GCM elsewhere) is decrypted
// into the env the same way, so every config consumer — CLI one-shots like
// `hiq models`/`doctor` included, not just the chat boot path — resolves
// provider keys exactly as before the credentials file was encrypted.
func loadDotEnv() {
	loadDotEnvForRoot(".")
}

// loadDotEnvForRoot loads a root's .env file (if present) before the home .env
// fallback. When root is "." it behaves like loadDotEnv().
func loadDotEnvForRoot(root string) {
	dotEnvPath := ".env"
	if root != "" && root != "." {
		dotEnvPath = filepath.Join(root, ".env")
	}
	loadDotEnvFile(dotEnvPath)
	if p := UserCredentialsPath(); p != "" {
		loadDotEnvFile(p)
	}
	if home, err := os.UserHomeDir(); err == nil {
		loadDotEnvFile(filepath.Join(home, ".env"))
	}
	secretEnvOnce.Do(func() {
		// Best-effort, mirroring the lenient file reads above: a corrupt store
		// must not break config loading — the affected tools report a clear
		// "not configured" error and prompt for re-entry. boot logs a warning
		// when its own LoadIntoEnv call hits the same failure.
		//
		// UNTRACKED on purpose: these secrets must live for the whole process —
		// the desktop scheduler, RAG extraction, and settings key-set indicators
		// read os.Getenv outside any controller's lifetime. Tracked injection
		// would be cleared by boot's controller-teardown UnloadFromEnv (audit
		// A9), and rebuild() builds the new controller BEFORE closing the old
		// one, so that unload would strip keys the fresh session still needs.
		_, _ = secret.Default().LoadIntoEnvUntracked()
	})
}

// loadDotEnvFile reads one .env file (if present) and sets any keys not already
// present in the environment. Lenient, zero-dependency parsing.
func loadDotEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
	_ = sc.Err()
}
