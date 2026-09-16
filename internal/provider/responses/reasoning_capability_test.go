package responses

import (
	"testing"

	"github.com/zzycxz/fairpeer/internal/provider"
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
