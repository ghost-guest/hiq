package boot

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// TestBootWarningOnce verifies that the sandbox warnings are printed at most once,
// even if boot.Build is called multiple times.
func TestBootWarningOnce(t *testing.T) {
	// We'll capture stderr to see if it prints multiple times
	var buf bytes.Buffer
	
	// Create a minimal config to trigger the warning
	opts := Options{
		Stderr: &buf,
		Profile: &config.Profile{
			Name: "test-profile",
		},
	}

	ctx := context.Background()

	// 1. Call boot.Build the first time
	_, _ = Build(ctx, opts)
	firstOutput := buf.String()
	
	// Count how many times the warning appears in the first run
	warnCountFirst := strings.Count(firstOutput, "bash sandbox requested but unavailable")
	
	// Reset buffer
	buf.Reset()

	// 2. Call boot.Build a second time
	_, _ = Build(ctx, opts)
	secondOutput := buf.String()

	// Count how many times the warning appears in the second run
	warnCountSecond := strings.Count(secondOutput, "bash sandbox requested but unavailable")

	// 3. Verify it doesn't print again
	if warnCountSecond > 0 {
		t.Errorf("Expected 0 warnings on second boot, got %d. Output: %s", warnCountSecond, secondOutput)
	} else {
		t.Logf("Success! First boot had %d warnings, second boot had 0.", warnCountFirst)
	}
}
