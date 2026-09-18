package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// writeTestUserConfig writes a minimal hiq.toml declaring a synthetic
// "test-provider" into the isolated user config dir. Default() ships no built-in
// presets (setup wizard owns first-run config), so tests that exercise the
// /model picker must declare a provider explicitly.
func writeTestUserConfig(t *testing.T) {
	t.Helper()
	dir := filepath.Dir(config.UserConfigPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir user config dir: %v", err)
	}
	body := `
default_model = "test-provider/test-provider/test-model-a"

[[providers]]
name = "test-provider"
kind = "openai"
base_url = "http://localhost:0"
model = "test-provider/test-model-a"
default = "test-provider/test-model-a"
api_key_env = "HIQ_API_KEY"
`
	if err := os.WriteFile(config.UserConfigPath(), []byte(body), 0o644); err != nil {
		t.Fatalf("write user config: %v", err)
	}
}

// TestModelRefsFromConfig verifies the /model picker enumerates configured
// provider/model refs, and only those whose provider API key is set.
//
// Uses isolateUserConfig (not just t.Chdir) because modelRefs() reads the USER
// config dir (~/.config/hiq or %AppData%\hiq), not the CWD. Without
// isolating it, a real user config on the machine would override the test
// config and make this test flaky (machine-dependent refs).
func TestModelRefsFromConfig(t *testing.T) {
	isolateUserConfig(t)
	writeTestUserConfig(t)
	t.Setenv("HIQ_API_KEY", "test-key")
	refs := modelRefs()
	if len(refs) == 0 {
		t.Fatal("expected configured provider/model refs, got none")
	}
	for _, r := range refs {
		if !strings.Contains(r, "/") {
			t.Errorf("ref %q should be provider/model", r)
		}
	}
}

// TestModelRefsSkipsUnconfigured verifies that with no provider keys set, the
// picker offers nothing rather than listing models the user can't select.
func TestModelRefsSkipsUnconfigured(t *testing.T) {
	isolateUserConfig(t)
	writeTestUserConfig(t)
	t.Setenv("HIQ_API_KEY", "")
	if refs := modelRefs(); len(refs) != 0 {
		t.Errorf("no keys set → no refs, got %v", refs)
	}
}

// TestModelArgCompletion verifies "/model " completes to the configured refs
// through the shared completion path.
func TestModelArgCompletion(t *testing.T) {
	isolateUserConfig(t)
	writeTestUserConfig(t)
	t.Setenv("HIQ_API_KEY", "test-key")
	m := newTestChatTUI()
	items, _, ok := m.slashArgItems("/model ")
	if !ok || len(items) == 0 {
		t.Fatalf("/model arg completion should offer refs, ok=%v n=%d", ok, len(items))
	}
}

// TestPersistModelWritesDefaultModel verifies that calling persistModel with a
// "provider/model" ref writes default_model = "<ref>" to the user config file
// in TOML form. This is the fix for the "default model resets on every launch"
// regression: previously /model only mutated the in-memory controller and the
// next startup read the global default.
func TestPersistModelWritesDefaultModel(t *testing.T) {
	isolateUserConfig(t)
	writeTestUserConfig(t)
	t.Setenv("HIQ_API_KEY", "test-key")

	m := newTestChatTUI()
	m.persistModel("test-provider/test-provider/test-model-a")

	body, err := os.ReadFile(config.UserConfigPath())
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if !strings.Contains(string(body), `default_model = "test-provider/test-provider/test-model-a"`) {
		t.Fatalf("saved config missing default_model ref:\n%s", body)
	}
}

// TestPersistModelRejectsUnknownRef verifies that an unresolvable ref is
// silently dropped (logged to slog, not pushed to the TUI notice channel)
// and never lands in the config file. Reason: surface a "persist failed"
// notice on the input box would make /model feel broken to users whose
// stored config doesn't list the exact model ref they picked; the in-
// memory switch still goes through.
func TestPersistModelRejectsUnknownRef(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv("HIQ_API_KEY", "test-key")

	m := newTestChatTUI()
	m.persistModel("ghost/never-existed")

	if _, err := os.Stat(config.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("unknown ref must not create config file, stat err=%v", err)
	}
}
