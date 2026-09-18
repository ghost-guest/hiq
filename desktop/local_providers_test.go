package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// Keyless local providers (built-in ollama/llamacpp presets) must be fully
// selectable once the user adds them — no API-key gate — while keyed providers
// without their env keep being rejected.

func TestSelectableDesktopModelRefAllowsKeylessProvider(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.BuiltinLocalProviders()...)
	cfg.Desktop.ProviderAccess = []string{"ollama"}

	ref, err := selectableDesktopModelRef(cfg, "ollama/qwen3-coder:30b")
	if err != nil {
		t.Fatalf("keyless provider selection failed: %v", err)
	}
	if ref != "ollama/qwen3-coder:30b" {
		t.Fatalf("ref = %q, want ollama/qwen3-coder:30b", ref)
	}

	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name: "keyed", Kind: "openai", BaseURL: "https://x.example.com/v1",
		Models: []string{"m"}, APIKeyEnv: "HIQ_TEST_MISSING_KEY",
	})
	cfg.Desktop.ProviderAccess = append(cfg.Desktop.ProviderAccess, "keyed")
	_, err = selectableDesktopModelRef(cfg, "keyed/m")
	if err == nil || !strings.Contains(err.Error(), "no key") {
		t.Fatalf("keyed provider without env must be rejected, got %v", err)
	}
}

func TestProviderViewFlagsKeylessProviderReady(t *testing.T) {
	keyless := config.ProviderEntry{Name: "llamacpp", APIKeyEnv: "", Models: []string{"local-model"}}
	if !providerViewFromEntry(keyless, false, true).KeySet {
		t.Error("keyless provider must report KeySet (no key needed), or pickers filter it out")
	}

	keyed := config.ProviderEntry{Name: "x", APIKeyEnv: "HIQ_TEST_MISSING_KEY", Models: []string{"m"}}
	if providerViewFromEntry(keyed, false, true).KeySet {
		t.Error("provider with unresolved key must not report KeySet")
	}
}

// Tabs must never boot on an ambient local preset: a persisted tab model that
// only resolves against the injected ollama preset (the bug that silently
// turned every tab into "qwen3-coder:30b" though the user never chose ollama)
// counts as dead — resolution fails and buildTabController drops it to the
// Welcome state instead of chatting on an unselected local endpoint.
func TestAmbientPresetTabModelIsDead(t *testing.T) {
	isolateDesktopUserDirs(t)
	cfg := config.LoadForEdit(filepath.Join(t.TempDir(), "absent.toml"))
	if !cfg.AmbientLocalPreset("ollama") {
		t.Fatal("the injected ollama preset must be ambient until the user adds it")
	}
	if _, _, ok := cfg.ResolveModelWithFallback("ollama/qwen3-coder:30b"); ok {
		t.Fatal("an ambient preset's model must not resolve for tabs")
	}
	if tabHasSelectableProvider(cfg) {
		t.Fatal("ambient presets alone must not make a tab bootable")
	}

	cfg.Desktop.ProviderAccess = []string{"ollama"}
	if _, _, ok := cfg.ResolveModelWithFallback("ollama/qwen3-coder:30b"); !ok {
		t.Fatal("an added preset's model must resolve for tabs")
	}
}

// NeedsOnboarding must stay true for a fresh install even though the keyless
// local presets exist, and turn off once the user adds one of them.
func TestNeedsOnboardingIgnoresBuiltinLocalPresets(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("HIQ_API_KEY", "")

	cfg := config.Default()
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if !NewApp().NeedsOnboarding() {
		t.Fatal("fresh install with only built-in local presets must still onboard")
	}

	cfg.Desktop.ProviderAccess = []string{"ollama"}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if NewApp().NeedsOnboarding() {
		t.Fatal("user-added keyless local provider must finish onboarding")
	}
}
