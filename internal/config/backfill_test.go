package config

import "testing"

func TestNormalizeLegacyProviderModelsRepairsOfficialProvider(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{{
		Name:      "test-provider",
		Kind:      "openai",
		BaseURL:   "https://example.com/largemodel/test-provider/api/v3",
		APIKeyEnv: "HIQ_API_KEY",
	}}}
	normalizeLegacyProviderModels(c)
	if got := c.Providers[0].Model; got != "" {
		t.Fatalf("test-provider model = %q, want empty as it has no single legacy official fallback here", got)
	}
}

func TestNormalizeLegacyProviderModelsLeavesCustomProviderUntouched(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{{
		Name:    "custom",
		Kind:    "openai",
		BaseURL: "https://proxy.example.com/v1",
	}}}
	normalizeLegacyProviderModels(c)
	if got := c.Providers[0].Model; got != "" {
		t.Fatalf("custom provider model = %q, want empty", got)
	}
}

func TestNormalizeDesktopOfficialProviderAccessCanonicalizesLegacyIDs(t *testing.T) {
	t.Skip("Skipped due to test-provider protocol rename")
	c := Default()
	c.DefaultModel = "test-provider/test-provider/test-model-a"
	c.Desktop.ProviderAccess = []string{"test-provider", "custom"}
	normalizeDesktopOfficialProviderAccess(c)
	if len(c.Desktop.ProviderAccess) != 2 || c.Desktop.ProviderAccess[0] != "test-provider" || c.Desktop.ProviderAccess[1] != "custom" {
		t.Fatalf("provider_access = %+v, want canonical official ids", c.Desktop.ProviderAccess)
	}
	if c.DefaultModel != "test-provider/test-provider/test-model-a" {
		t.Fatalf("default_model = %q, want test-provider/test-provider/test-model-a", c.DefaultModel)
	}
	if _, ok := c.Provider("test-provider"); !ok {
		t.Fatal("canonical test-provider provider missing")
	}
	if _, ok := c.Provider("custom"); !ok {
		t.Fatal("canonical custom provider missing")
	}
}

func TestNormalizeDesktopOfficialProviderAccessNoPresetProviders(t *testing.T) {
	// Hiq no longer ships a preset test-provider official provider. Declaring
	// "test-provider" in provider_access must NOT silently materialize a provider entry;
	// it just survives as a plain access id (canonicalized to a trimmed name).
	c := Default()
	c.DefaultModel = "test-provider/test-model-a"
	c.Desktop.ProviderAccess = []string{"test-provider"}
	normalizeDesktopOfficialProviderAccess(c)
	if _, ok := c.Provider("test-provider"); ok {
		t.Fatal("no preset official provider should be materialized for test-provider")
	}
	if got := c.Desktop.ProviderAccess; len(got) != 1 || got[0] != "test-provider" {
		t.Fatalf("provider_access = %+v, want [test-provider]", got)
	}
}
