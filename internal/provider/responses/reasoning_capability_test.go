package responses

import (
	"testing"

	"github.com/zzycxz/hiq/internal/provider"
)

// A DeepSeek SKU served through a relay host unknown to DetectVendor must
// still expose the DeepSeek effort vocabulary: the model family, not the
// gateway host, decides which efforts the server accepts.
func TestReasoningForConfigRelayedDeepSeekModel(t *testing.T) {
	cfg := provider.Config{
		Name:    "aiaaa-responses",
		BaseURL: "https://aiaaa.cc/v1",
		Model:   "deepseek-v4.1-flash",
	}
	cap := ReasoningForConfig(cfg)
	if err := cap.Validate(cfg.Model, "high"); err != nil {
		t.Fatalf("high must be accepted for relayed DeepSeek SKU: %v", err)
	}
	if err := cap.Validate(cfg.Model, "low"); err != nil {
		t.Fatalf("low must be accepted for relayed DeepSeek SKU: %v", err)
	}
	if err := cap.Validate(cfg.Model, "medium"); err != nil {
		t.Fatalf("medium must be accepted for relayed DeepSeek SKU (verified live): %v", err)
	}
	if contains(cap.IDs(), "none") {
		t.Fatalf("relayed vocabulary must not claim none, got %v", cap.IDs())
	}
	if !contains(cap.IDs(), "high") {
		t.Fatalf("supported efforts must include high, got %v", cap.IDs())
	}
	if cap.Default != "high" {
		t.Fatalf("default = %q, want high (DeepSeek default depth)", cap.Default)
	}
}

// A model the adapter cannot attribute keeps the empty capability: any
// explicit effort stays rejected (fail-closed) instead of guessing a wire
// vocabulary the endpoint may not implement.
func TestReasoningForConfigUnknownModelOnRelayStaysEmpty(t *testing.T) {
	cfg := provider.Config{
		Name:    "relay",
		BaseURL: "https://aiaaa.cc/v1",
		Model:   "totally-custom-model",
	}
	cap := ReasoningForConfig(cfg)
	if len(cap.IDs()) != 0 {
		t.Fatalf("unknown model must keep empty capability, got %v", cap.IDs())
	}
	if err := cap.Validate(cfg.Model, "high"); err == nil {
		t.Fatal("explicit effort on unknown model must stay rejected")
	}
}

// An explicit supported_efforts declaration always wins over the
// model-derived default, including narrowing a known vendor.
func TestReasoningForConfigDeclaredEffortsWin(t *testing.T) {
	cfg := provider.Config{
		Name:    "aiaaa-responses",
		BaseURL: "https://aiaaa.cc/v1",
		Model:   "deepseek-v4.1-flash",
		Extra: map[string]any{
			"supported_efforts": []string{"low", "high"},
			"default_effort":    "low",
		},
	}
	cap := ReasoningForConfig(cfg)
	if got := cap.IDs(); len(got) != 2 || !contains(got, "low") || !contains(got, "high") {
		t.Fatalf("declared efforts must win verbatim, got %v", got)
	}
	if cap.Default != "low" {
		t.Fatalf("declared default = %q, want low", cap.Default)
	}
}

// reasoning_protocol = "none" disables effort control entirely.
func TestReasoningForConfigProtocolNoneDisablesEffort(t *testing.T) {
	cfg := provider.Config{
		Name:    "aiaaa-responses",
		BaseURL: "https://aiaaa.cc/v1",
		Model:   "deepseek-v4.1-flash",
		Extra:   map[string]any{"reasoning_protocol": "none"},
	}
	cap := ReasoningForConfig(cfg)
	if len(cap.IDs()) != 0 {
		t.Fatalf("protocol none must yield empty capability, got %v", cap.IDs())
	}
}

// The official DeepSeek endpoint keeps its host-derived capability.
func TestReasoningForConfigOfficialDeepSeekHost(t *testing.T) {
	cfg := provider.Config{
		Name:    "deepseek",
		BaseURL: "https://api.deepseek.com/v1",
		Model:   "deepseek-v4.1-flash",
	}
	cap := ReasoningForConfig(cfg)
	if err := cap.Validate(cfg.Model, "max"); err != nil {
		t.Fatalf("official DeepSeek host must keep max: %v", err)
	}
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// A relay fronting DeepSeek inherits the upstream endpoint's wire semantics:
// stateless continuation (the upstream Responses API rejects
// previous_response_id) and historical reasoning on tool-call turns. An
// unknown gateway serving a non-DeepSeek model keeps the standard
// OpenAI-compatible behavior, and an explicit mode override always wins.
func TestRelayedDeepSeekIsStateless(t *testing.T) {
	cases := []struct {
		name     string
		baseURL  string
		model    string
		mode     string
		stateful *bool
		want     string
	}{
		{name: "relay-deepseek", baseURL: "https://aiaaa.cc/v1", model: "deepseek-v4.1-flash", want: "stateless"},
		{name: "relay-other-model", baseURL: "https://aiaaa.cc/v1", model: "glm-5.3-flash", want: "stateful"},
		{name: "official-deepseek", baseURL: "https://api.deepseek.com/v1", model: "deepseek-v4.1-flash", want: "stateless"},
		{name: "relay-deepseek-explicit-stateful", baseURL: "https://aiaaa.cc/v1", model: "deepseek-v4.1-flash", mode: "stateful", want: "stateful"},
		{name: "relay-deepseek-stateful-bool", baseURL: "https://aiaaa.cc/v1", model: "deepseek-v4.1-flash", stateful: boolPtr(true), want: "stateful"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{BaseURL: tc.baseURL, Model: tc.model, Mode: tc.mode, Stateful: tc.stateful}
			if got := cfg.mode(); got != tc.want {
				t.Fatalf("mode() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The wire capability set is resolved once, so continuation mode and the
// reasoning-retention contract can never disagree.
func TestResolveCapabilitiesInheritsFamilyTraits(t *testing.T) {
	relay := resolveCapabilities("https://aiaaa.cc/v1", "deepseek-v4.1-flash")
	if !relay.stateless {
		t.Error("relayed DeepSeek must be stateless")
	}
	if !relay.toolCallReasoning {
		t.Error("relayed DeepSeek must retain historical reasoning on tool-call turns")
	}
	// Output limits and headers stay at the unknown-gateway zero value.
	if relay.defaultMaxOutputTokens != 0 || relay.compactionOutputTokens != 0 {
		t.Errorf("a relay must not inherit DeepSeek's output budgets, got %+v", relay)
	}
	if relay.sessionCacheHeader || relay.summaryRequired || relay.ignoresTemperature {
		t.Errorf("a relay must not inherit vendor headers/fields, got %+v", relay)
	}
	// Same host, other model family: unchanged OpenAI-compatible behavior.
	other := resolveCapabilities("https://aiaaa.cc/v1", "glm-5.3-flash")
	if other.stateless || other.toolCallReasoning {
		t.Errorf("unknown gateway with an unknown family must keep the zero value, got %+v", other)
	}
	// Official hosts are unaffected.
	official := resolveCapabilities("https://api.deepseek.com/v1", "deepseek-v4.1-flash")
	if !official.stateless || official.compactionOutputTokens == 0 {
		t.Errorf("official DeepSeek must keep its full vendor row, got %+v", official)
	}
}

func boolPtr(v bool) *bool { return &v }
