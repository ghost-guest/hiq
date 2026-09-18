package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResponseHeaderTimeoutDefaultAndEnv(t *testing.T) {
	if got := ResponseHeaderTimeout(); got != DefaultResponseHeaderTimeout {
		t.Fatalf("default = %v, want %v", got, DefaultResponseHeaderTimeout)
	}

	t.Setenv("HIQ_RESPONSE_HEADER_TIMEOUT", "45s")
	if got := ResponseHeaderTimeout(); got != 45*time.Second {
		t.Fatalf("duration env = %v, want 45s", got)
	}

	t.Setenv("HIQ_RESPONSE_HEADER_TIMEOUT", "120")
	if got := ResponseHeaderTimeout(); got != 120*time.Second {
		t.Fatalf("seconds env = %v, want 120s", got)
	}

	t.Setenv("HIQ_RESPONSE_HEADER_TIMEOUT", "bogus")
	if got := ResponseHeaderTimeout(); got != DefaultResponseHeaderTimeout {
		t.Fatalf("invalid env should fall back to default, got %v", got)
	}
}

func TestIsHeaderTimeout(t *testing.T) {
	wrapped := fmt.Errorf("post: %w", fmt.Errorf(`Post "https://x/v1": net/http: timeout awaiting response headers`))
	if !IsHeaderTimeout(wrapped) {
		t.Fatal("wrapped header timeout should be detected")
	}
	if IsHeaderTimeout(fmt.Errorf("connection reset by peer")) {
		t.Fatal("non-timeout error should not match")
	}
	if IsHeaderTimeout(nil) {
		t.Fatal("nil should not match")
	}
}

// TestSendWithRetryHeaderTimeoutCap verifies that a blackholed endpoint (TLS
// fine, headers never arrive) fails fast after maxHeaderTimeoutRetries+1
// attempts with an actionable message, instead of burning all MaxRetries.
func TestSendWithRetryHeaderTimeoutCap(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // never answer headers until the test finishes
	}))
	defer srv.Close()
	defer close(release)

	tr := &http.Transport{ResponseHeaderTimeout: 50 * time.Millisecond}
	client := &http.Client{Transport: tr}

	attempts := 0
	start := time.Now()
	_, err := SendWithRetry(context.Background(), client, SendOptions{ProvName: "dead-relay"},
		func(ctx context.Context) (*http.Request, error) {
			attempts++
			return http.NewRequestWithContext(ctx, "POST", srv.URL, strings.NewReader("{}"))
		})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a blackholed endpoint")
	}
	if !strings.Contains(err.Error(), "never responded") || !strings.Contains(err.Error(), "switch channels") {
		t.Fatalf("error should be actionable, got: %v", err)
	}
	if attempts != maxHeaderTimeoutRetries+1 {
		t.Fatalf("attempts = %d, want %d (header-timeout cap)", attempts, maxHeaderTimeoutRetries+1)
	}
	// 3 attempts x 50ms + capped backoff must stay far below MaxRetries pace.
	if elapsed > 10*time.Second {
		t.Fatalf("took %v — header-timeout retries are not capped", elapsed)
	}
}
