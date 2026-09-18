package control

import (
	"errors"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/i18n"
	"github.com/zzycxz/hiq/internal/provider"
)

func TestExplainError(t *testing.T) {
	if explainError(nil) != nil {
		t.Error("nil should stay nil")
	}

	auth := explainError(&provider.AuthError{Provider: "test-provider", KeyEnv: "HIQ_API_KEY", Status: 401})
	if !strings.Contains(auth.Error(), "HIQ_API_KEY") {
		t.Errorf("401 should name the key env: %q", auth.Error())
	}

	for _, status := range []int{400, 422, 429, 500, 503} {
		got := explainError(&provider.APIError{Provider: "p", Status: status})
		if got.Error() == "" || got.Error() == (&provider.APIError{Provider: "p", Status: status}).Error() {
			t.Errorf("status %d should map to a localized message, got %q", status, got.Error())
		}
	}

	jsonBody := explainError(&provider.APIError{Provider: "test-provider", Status: 400, Body: `{"error":{"message":"This model's maximum context length is 65536 tokens.","type":"invalid_request_error"}}`})
	if !strings.Contains(jsonBody.Error(), i18n.M.ProviderErrBadRequest) || !strings.Contains(jsonBody.Error(), "maximum context length") {
		t.Errorf("400 should append the provider reason from a JSON body, got %q", jsonBody.Error())
	}

	rawBody := explainError(&provider.APIError{Provider: "test-provider", Status: 422, Body: "some unparseable detail"})
	if !strings.Contains(rawBody.Error(), "some unparseable detail") {
		t.Errorf("422 should fall back to the raw body, got %q", rawBody.Error())
	}

	noLeak := explainError(&provider.APIError{Provider: "test-provider", Status: 429, Body: `{"error":{"message":"slow down"}}`})
	if noLeak.Error() != i18n.M.ProviderErrRateLimited {
		t.Errorf("429 body must not leak into the message, got %q", noLeak.Error())
	}

	plain := errors.New("some other failure")
	if explainError(plain) != plain {
		t.Error("unknown errors should pass through unchanged")
	}
}
