package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/provider"
)

// TestResponseFormatAndCacheKeyOnWire (upgrade spec 4-7/4-6) captures the
// request body and asserts: response_format json_schema is present exactly
// when ResponseSchema is set (with the caller's schema name), prompt_cache_key
// passes through clamped, and neither leaks into a plain request.
func TestResponseFormatAndCacheKeyOnWire(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{Name: "t", BaseURL: srv.URL, Model: "t/m", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}

	drain := func(req provider.Request) {
		ch, err := p.Stream(context.Background(), req)
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		for range ch {
		}
	}

	drain(provider.Request{
		Messages:       []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
		ResponseSchema: json.RawMessage(`{"type":"object"}`),
		SchemaName:     "verdict",
		CacheKey:       strings.Repeat("k", 80),
	})
	drain(provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})

	if len(bodies) != 2 {
		t.Fatalf("captured %d bodies, want 2", len(bodies))
	}
	var with struct {
		ResponseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		} `json:"response_format"`
		PromptCacheKey string `json:"prompt_cache_key"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &with); err != nil {
		t.Fatalf("body[0]: %v", err)
	}
	if with.ResponseFormat.Type != "json_schema" || with.ResponseFormat.JSONSchema.Name != "verdict" {
		t.Fatalf("response_format = %+v", with.ResponseFormat)
	}
	if len(with.PromptCacheKey) != 64 {
		t.Fatalf("prompt_cache_key len = %d, want clamped 64", len(with.PromptCacheKey))
	}
	if strings.Contains(bodies[1], "response_format") || strings.Contains(bodies[1], "prompt_cache_key") {
		t.Fatalf("plain request leaked optional fields: %s", bodies[1])
	}
}
