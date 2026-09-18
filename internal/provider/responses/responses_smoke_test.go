package responses

import (
	"testing"

	"github.com/zzycxz/hiq/internal/provider"
)

// TestKindsRegistered pins the custom-provider contract: the responses kinds
// must be registered so config validation, the desktop kind picker
// (provider.Kinds()) and boot resolution accept them.
func TestKindsRegistered(t *testing.T) {
	kinds := provider.Kinds()
	want := map[string]bool{"responses": false, "dashscope-responses": false}
	for _, k := range kinds {
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("kind %q not registered (kinds=%v)", k, kinds)
		}
	}
}

// TestNewFromConfigHappyPath builds a client the way boot would for a
// [[providers]] entry with kind = "responses".
func TestNewFromConfigHappyPath(t *testing.T) {
	p, err := newFromConfig(provider.Config{
		Name:    "myrelay",
		BaseURL: "https://relay.example.com/v1",
		Model:   "gpt-5.4",
		APIKey:  "sk-test",
		Extra:   map[string]any{"api_key_env": "MYRELAY_API_KEY"},
	})
	if err != nil {
		t.Fatalf("newFromConfig: %v", err)
	}
	if p == nil {
		t.Fatal("nil provider")
	}
	if p.Name() != "myrelay" {
		t.Errorf("Name() = %q, want myrelay", p.Name())
	}
}

// TestNewFromConfigRequiresBaseURL keeps the openai-kind error contract.
func TestNewFromConfigRequiresBaseURL(t *testing.T) {
	if _, err := newFromConfig(provider.Config{Name: "x"}); err == nil {
		t.Fatal("newFromConfig without base_url must fail")
	}
	if _, err := newFromConfig(provider.Config{Name: "x", BaseURL: "https://x"}); err == nil {
		t.Fatal("newFromConfig without model must fail")
	}
}
