package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// TestUpsertCredential proves a secret lands in the encrypted store, is pinned
// into the live env, and that a lingering plaintext copy in the legacy
// credentials file is scrubbed so the encrypted entry is the only value on disk.
func TestUpsertCredential(t *testing.T) {
	isolateDesktopUserDirs(t)
	legacy := config.UserCredentialsPath()
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("# comment\nFOO=old\nBAR=keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := upsertCredential("FOO", "new"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := upsertCredential("BAZ", "added"); err != nil {
		t.Fatalf("upsert new key: %v", err)
	}

	store := credentialStore()
	for key, want := range map[string]string{"FOO": "new", "BAZ": "added"} {
		got, ok, err := store.Get(key)
		if err != nil || !ok {
			t.Errorf("store Get(%s): ok=%v err=%v", key, ok, err)
			continue
		}
		if got != want {
			t.Errorf("store %s = %q, want %q", key, got, want)
		}
	}
	if os.Getenv("FOO") != "new" || os.Getenv("BAZ") != "added" {
		t.Errorf("process env not updated: FOO=%q BAZ=%q", os.Getenv("FOO"), os.Getenv("BAZ"))
	}
	// The scrubbed key must be gone from the legacy file while unrelated lines
	// survive.
	b, _ := os.ReadFile(legacy)
	got := string(b)
	if strings.Contains(got, "FOO=") {
		t.Errorf("upserted key must be scrubbed from the legacy file:\n%s", got)
	}
	for _, want := range []string{"# comment", "BAR=keep"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in legacy file:\n%s", want, got)
		}
	}
}

func TestRemoveEnvFileDeletesKeyAndUnsetsProcessEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(path, []byte("# comment\nFOO=old\nexport BAR=remove\nBAZ=keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BAR", "remove")

	if err := removeEnvFile(path, "BAR"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	b, _ := os.ReadFile(path)
	got := string(b)
	for _, want := range []string{"# comment", "FOO=old", "BAZ=keep"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "BAR=") {
		t.Errorf("removed key should be absent:\n%s", got)
	}
	if _, ok := os.LookupEnv("BAR"); ok {
		t.Errorf("process env BAR should be unset")
	}
}

// TestRemoveCredential proves a secret is gone from the encrypted store, the
// live env, and the legacy plaintext file.
func TestRemoveCredential(t *testing.T) {
	isolateDesktopUserDirs(t)
	legacy := config.UserCredentialsPath()
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("FOO=old\nBAR=keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := upsertCredential("FOO", "secret-value"); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := removeCredential("FOO"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok, err := credentialStore().Get("FOO"); ok || err != nil {
		t.Errorf("store still holds FOO: ok=%v err=%v", ok, err)
	}
	if _, ok := os.LookupEnv("FOO"); ok {
		t.Error("process env FOO should be unset")
	}
	b, _ := os.ReadFile(legacy)
	if strings.Contains(string(b), "FOO=") {
		t.Errorf("FOO must be scrubbed from the legacy file:\n%s", b)
	}
}

// providerKeyCfg builds a config with one provider keyed on HIQ_API_KEY —
// Default() ships no built-in presets, so promotion tests declare their own.
func providerKeyCfg() *config.Config {
	cfg := config.Default()
	cfg.Providers = []config.ProviderEntry{{
		Name: "test-model", Kind: "openai", BaseURL: "https://example.invalid",
		Model: "x", APIKeyEnv: "HIQ_API_KEY",
	}}
	return cfg
}

// TestPromoteProviderKeysLiftsProjectKeyAndStripsHomeEnv proves a provider key
// that resolves only from ~/.env is copied into the encrypted store, removed
// from ~/.env, and that unrelated env vars are ignored.
func TestPromoteProviderKeysLiftsProjectKeyAndStripsHomeEnv(t *testing.T) {
	home := isolateDesktopUserDirs(t)
	homeEnv := filepath.Join(home, ".env")
	if err := os.WriteFile(homeEnv, []byte("HIQ_API_KEY=sk-test\nNPM_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIQ_API_KEY", "sk-test")
	t.Setenv("NPM_TOKEN", "secret")

	promoteProviderKeysToCredentials(providerKeyCfg())

	if v, ok, err := credentialStore().Get("HIQ_API_KEY"); err != nil || !ok || v != "sk-test" {
		t.Errorf("provider key not promoted into encrypted store: ok=%v err=%v val=%q", ok, err, v)
	}
	if _, ok, _ := credentialStore().Get("NPM_TOKEN"); ok {
		t.Error("non-provider env var must not be promoted")
	}

	rest, _ := os.ReadFile(homeEnv)
	if strings.Contains(string(rest), "HIQ_API_KEY") {
		t.Errorf("promoted key must be stripped from ~/.env:\n%s", rest)
	}
	if !strings.Contains(string(rest), "NPM_TOKEN=secret") {
		t.Errorf("unrelated ~/.env line must survive:\n%s", rest)
	}
}

// TestPromoteProviderKeysLeavesExistingCredentialsKey proves promotion never
// overwrites a key already in the encrypted store and leaves ~/.env untouched.
func TestPromoteProviderKeysLeavesExistingCredentialsKey(t *testing.T) {
	home := isolateDesktopUserDirs(t)
	if err := credentialStore().Set("HIQ_API_KEY", "sk-global"); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	homeEnv := filepath.Join(home, ".env")
	if err := os.WriteFile(homeEnv, []byte("HIQ_API_KEY=sk-stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIQ_API_KEY", "sk-global")

	promoteProviderKeysToCredentials(providerKeyCfg())

	if v, _, _ := credentialStore().Get("HIQ_API_KEY"); v != "sk-global" {
		t.Errorf("existing store key was changed: %q", v)
	}
	if data, err := os.Stat(homeEnv); err != nil || data.Size() == 0 {
		t.Errorf("~/.env should be untouched when key already stored, err=%v", err)
	}
}

// TestMigrateCredentialsFileStartup proves the startup sweep lifts the legacy
// plaintext credentials file into the encrypted store and removes it.
func TestMigrateCredentialsFileStartup(t *testing.T) {
	isolateDesktopUserDirs(t)
	if err := os.MkdirAll(filepath.Dir(config.UserCredentialsPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.UserCredentialsPath(), []byte("# legacy\nHIQ_API_KEY=sk-mig\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	migrateCredentialsFile()

	if v, ok, err := credentialStore().Get("HIQ_API_KEY"); err != nil || !ok || v != "sk-mig" {
		t.Errorf("key not migrated into encrypted store: ok=%v err=%v val=%q", ok, err, v)
	}
	if _, err := os.Stat(config.UserCredentialsPath()); !os.IsNotExist(err) {
		t.Errorf("legacy credentials file kept after successful migration, stat err=%v", err)
	}
}
