package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadForEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hiq.toml")
	custom := `default_model = "custom"
[[providers]]
name = "custom"
kind = "openai"
base_url = "https://x"
model = "m"
api_key_env = "X_KEY"
`
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	// Existing file: its providers/default override the built-in defaults, so a
	// reconfigure preserves the user's setup.
	cfg := LoadForEdit(path)
	if cfg.DefaultModel != "custom" {
		t.Errorf("default_model = %q, want custom", cfg.DefaultModel)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "custom" {
		t.Errorf("providers = %v, want a single custom provider", cfg.Providers)
	}

	// Missing file: falls back to the built-in defaults.
	if cfg := LoadForEdit(filepath.Join(dir, "absent.toml")); cfg.DefaultModel != Default().DefaultModel {
		t.Errorf("missing-file default = %q, want %q", cfg.DefaultModel, Default().DefaultModel)
	}
}

func TestLoadForEditMigratesLegacyMCPTiers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hiq.toml")
	body := `
[codegraph]
enabled = true
tier = "eager"

[[plugins]]
name = "playwright"
command = "npx"
tier = "lazy"

[[providers]]
name = "local"
kind = "openai"
base_url = "https://x"
model = "m"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadForEdit(path)
	if cfg.Codegraph.Tier != "" {
		t.Fatalf("codegraph tier = %q, want migrated empty", cfg.Codegraph.Tier)
	}
	if len(cfg.Plugins) != 1 || cfg.Plugins[0].Tier != "" {
		t.Fatalf("plugins after migration = %+v, want empty tier", cfg.Plugins)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(updated), "\ntier") {
		t.Fatalf("legacy tier lines should be removed from file:\n%s", updated)
	}
	if !strings.Contains(string(updated), `command = "npx"`) || !strings.Contains(string(updated), `[codegraph]`) {
		t.Fatalf("migration should preserve ordinary config:\n%s", updated)
	}
}

// TestLoadForEditStrictFailsClosed pins the write-path contract: a config file
// that exists but cannot be parsed must make LoadForEditStrict return an error,
// while read-path LoadForEdit keeps its resilient defaults fallback. This is
// what stops a settings save from persisting a defaults-shaped config over a
// file we failed to read (which had wiped the user's providers once).
func TestLoadForEditStrictFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hiq.toml")
	broken := `default_model = "custom"
[[providers]
name = "custom"
`
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadForEditStrict(path); err == nil {
		t.Fatal("LoadForEditStrict on a broken file must return an error")
	} else if !strings.Contains(err.Error(), "hiq.toml") {
		t.Errorf("error should name the file, got: %v", err)
	}

	// The read path keeps its defaults fallback (rendering must not break).
	cfg := LoadForEdit(path)
	if cfg == nil {
		t.Fatal("LoadForEdit should still return a usable defaults-shaped config")
	}

	// A valid file passes strict load with its values preserved.
	good := `default_model = "custom"
[[providers]]
name = "custom"
kind = "openai"
base_url = "https://x"
model = "m"
api_key_env = "X_KEY"
`
	if err := os.WriteFile(path, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadForEditStrict(path)
	if err != nil {
		t.Fatalf("strict load of a valid file: %v", err)
	}
	if cfg.DefaultModel != "custom" || len(cfg.Providers) != 1 {
		t.Errorf("strict load lost values: model=%q providers=%d", cfg.DefaultModel, len(cfg.Providers))
	}
}
