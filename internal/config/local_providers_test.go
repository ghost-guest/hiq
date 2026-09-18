package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The keyless local presets (Ollama, llama.cpp) ship for every install, but
// are injected at load time — never baked into Default(), whose non-nil
// Providers slice would field-merge with user [[providers]] entries during
// TOML decode. These tests pin the invariants:
//
//  1. The presets are keyless (no api_key_env) and bypass the proxy.
//  2. They apply only when no config file defines [[providers]].
//  3. Selecting them explicitly is valid without any key.
//  4. They never get auto-selected by fallback resolution.
//  5. Until the user adds one (provider_access), it is ambient: never a
//     fallback target, never resolved for tabs, never persisted on save.

func TestBuiltinLocalProvidersShape(t *testing.T) {
	for _, e := range BuiltinLocalProviders() {
		if e.APIKeyEnv != "" {
			t.Errorf("%s must be keyless, got api_key_env %q", e.Name, e.APIKeyEnv)
		}
		if !e.NoProxy {
			t.Errorf("%s must bypass the proxy (loopback endpoint)", e.Name)
		}
		if len(e.ModelList()) == 0 || e.DefaultModel() == "" {
			t.Errorf("%s must ship a model list and default, got %+v", e.Name, e)
		}
		if e.Kind != "openai" || e.BaseURL == "" {
			t.Errorf("%s is incomplete: %+v", e.Name, e)
		}
	}
}

func TestAppendBuiltinLocalProvidersKeepsUserOverrides(t *testing.T) {
	c := Default()
	c.Providers = append(c.Providers, ProviderEntry{
		Name: "ollama", Kind: "openai", BaseURL: "http://192.168.1.10:11434/v1", Models: []string{"mine:8b"},
	})
	appendBuiltinLocalProviders(c)

	if len(c.Providers) != 3 {
		t.Fatalf("providers = %d, want 3 (user ollama kept + lmstudio/llamacpp presets appended)", len(c.Providers))
	}
	e, _ := c.Provider("ollama")
	if e.BaseURL != "http://192.168.1.10:11434/v1" || len(e.ModelList()) != 1 {
		t.Errorf("user-defined ollama must win over the preset, got %+v", e)
	}
}

func TestLoadForEditInjectsLocalPresetsOnlyWithoutUserProviders(t *testing.T) {
	dir := t.TempDir()

	// Absent file → the presets apply.
	cfg := LoadForEdit(filepath.Join(dir, "absent.toml"))
	for _, name := range []string{"ollama", "llamacpp"} {
		if _, ok := cfg.Provider(name); !ok {
			t.Errorf("absent config should carry the %s preset", name)
		}
	}

	// File WITHOUT [[providers]] → the presets still apply.
	plain := filepath.Join(dir, "plain.toml")
	if err := os.WriteFile(plain, []byte("default_model = \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadForEdit(plain).Provider("ollama"); !ok {
		t.Error("config without [[providers]] should carry the ollama preset")
	}

	// File WITH [[providers]] → replaced wholesale, no preset leakage.
	withProviders := filepath.Join(dir, "with-providers.toml")
	body := `[[providers]]
name = "mine"
kind = "openai"
base_url = "https://x.example.com"
model = "m"
api_key_env = "X_KEY"
`
	if err := os.WriteFile(withProviders, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg = LoadForEdit(withProviders)
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "mine" {
		t.Fatalf("providers = %+v, want exactly the user's entry (no field-merge with presets)", cfg.Providers)
	}
	if cfg.Providers[0].BaseURL != "https://x.example.com" || cfg.Providers[0].ContextWindow != 0 {
		t.Errorf("user entry was contaminated by preset fields: %+v", cfg.Providers[0])
	}
}

func TestValidateAllowsKeylessLocalProvider(t *testing.T) {
	cfg := Default()
	cfg.Providers = append(cfg.Providers, BuiltinLocalProviders()...)
	if err := cfg.Validate("ollama/qwen3-coder:30b"); err != nil {
		t.Fatalf("selecting the keyless ollama preset must validate, got %v", err)
	}

	// Keyed providers still require their env to resolve.
	cfg.Providers = append(cfg.Providers, ProviderEntry{
		Name: "keyed", Kind: "openai", BaseURL: "https://x.example.com",
		Model: "m", APIKeyEnv: "HIQ_TEST_MISSING_KEY",
	})
	t.Setenv("HIQ_TEST_MISSING_KEY", "")
	if err := cfg.Validate("keyed/m"); err == nil {
		t.Fatal("keyed provider with unresolved env must fail validation")
	}
}

func TestResolveModelWithFallbackNeverAutoSelectsLocalPresets(t *testing.T) {
	cfg := Default()
	cfg.Providers = append(cfg.Providers, BuiltinLocalProviders()...) // fresh install: only the keyless local presets exist
	if got, _, ok := cfg.ResolveModelWithFallback(""); ok {
		t.Fatalf("fallback on a fresh config selected %q — local presets must never be auto-selected", got)
	}
}

// Ambient presets (injected, never added via provider_access) must not capture
// tabs: neither as a stale-ref fallback nor by resolving a persisted
// "ollama/..." tab model directly. This is the regression that silently
// retargeted every tab onto an unselected local ollama endpoint.
func TestResolveModelWithFallbackSkipsAmbientLocalPresets(t *testing.T) {
	c := Default()
	c.DefaultModel = ""
	appendBuiltinLocalProviders(c)
	if _, _, ok := c.ResolveModelWithFallback("mimo/mimo-v2.5-pro"); ok {
		t.Fatal("stale ref must not fall back to an ambient local preset")
	}
	if _, _, ok := c.ResolveModelWithFallback("ollama/qwen3-coder:30b"); ok {
		t.Fatal("a model resolving only to an ambient preset is dead, not selectable")
	}
	// Adding the preset (wizard/settings writes provider_access) turns it into
	// a real choice for both paths — and leaves the sibling llamacpp ambient.
	c.Desktop.ProviderAccess = []string{"ollama"}
	resolved, fallback, ok := c.ResolveModelWithFallback("mimo/mimo-v2.5-pro")
	if !ok || !fallback || !strings.HasPrefix(resolved, "ollama/") {
		t.Fatalf("resolved=%q ok=%v fallback=%v — an added local preset must be a valid fallback", resolved, ok, fallback)
	}
	if _, _, ok := c.ResolveModelWithFallback("ollama/qwen3-coder:30b"); !ok {
		t.Fatal("an added preset's model must resolve")
	}
}

// A keyless provider the user defined themselves (hand-written [[providers]],
// never injected) stays a legitimate fallback for stale refs — that is the
// bricks-vs-fallback fix the ambient gate must not regress.
func TestResolveModelWithFallbackUserDefinedKeyless(t *testing.T) {
	c := Default()
	c.Providers = []ProviderEntry{{
		Name: "mylocal", Kind: "openai", BaseURL: "http://127.0.0.1:9000/v1",
		Models: []string{"m"}, Default: "m",
	}}
	resolved, fallback, ok := c.ResolveModelWithFallback("gone/model-x")
	if !ok || !fallback || resolved != "mylocal/m" {
		t.Fatalf("resolved=%q ok=%v fallback=%v — user-defined keyless must stay a fallback", resolved, ok, fallback)
	}
}

// Saving a config whose only local providers are ambient presets must not
// persist them: the file defines no [[providers]], so the presets stay
// runtime-only until the user adds one (which is exactly what renders).
func TestRenderSkipsAmbientLocalPresets(t *testing.T) {
	c := Default()
	appendBuiltinLocalProviders(c)
	if out := RenderTOMLForScope(c, RenderScopeUser); strings.Contains(out, "[[providers]]") {
		t.Fatal("ambient presets must not be persisted on save")
	}

	c.Desktop.ProviderAccess = []string{"ollama"}
	out := RenderTOMLForScope(c, RenderScopeUser)
	if !strings.Contains(out, `name        = "ollama"`) {
		t.Fatal("an added preset must persist as a [[providers]] entry")
	}
	if strings.Contains(out, "llamacpp") {
		t.Fatal("an unadded sibling preset must stay ambient (not rendered)")
	}
}

func TestNormalizeLocalProviderNoProxy(t *testing.T) {
	cfg := Default()
	cfg.Providers = append(cfg.Providers,
		ProviderEntry{Name: "lmstudio", Kind: "openai", BaseURL: "http://localhost:1234/v1", Model: "m"},
		ProviderEntry{Name: "remote", Kind: "openai", BaseURL: "https://api.example.com/v1", Model: "m", APIKeyEnv: "K"},
	)
	normalizeLocalProviderNoProxy(cfg)

	if e, _ := cfg.Provider("lmstudio"); !e.NoProxy {
		t.Error("localhost endpoint must be forced to no_proxy")
	}
	if e, _ := cfg.Provider("remote"); e.NoProxy {
		t.Error("remote endpoint must not be touched")
	}
}
