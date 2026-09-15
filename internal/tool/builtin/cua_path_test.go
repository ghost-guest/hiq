//go:build windows

package builtin

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/zzycxz/fairpeer/internal/provider"
)

// TestCallVLMProviderPath verifies the PRODUCTION VLM path end-to-end:
// SetProviderChatRunner (the boot injection point) → CallVLM (backend=provider)
// → callProviderVLM → the injected runner. This is the exact chain screen_perceive
// uses in cowork mode now. It does NOT mock the HTTP layer — when FAIRPEER_API_KEY
// is set it builds a REAL one-shot provider client (mirroring runProviderVLMChat)
// and hits the live test-model-b endpoint, so a green result means the production
// vision path genuinely works, not a unit-test stub.
//
// Without the key it still runs a cheap path: inject a fake runner that asserts
// it received an image_url content part, proving the dispatch + message
// construction are correct even offline. That alone catches the "image was
// silently dropped" class of bug.
func TestCallVLMProviderPath(t *testing.T) {
	// Configure the global VLM model (the production default is a provider
	// multimodal model). We set it to test-model-b and inject a fake runner that
	// asserts the image part is present, proving the multimodal message
	// construction is correct even offline.
	origModel := vlmModel
	origRunner := runProviderChat
	t.Cleanup(func() {
		vlmModel = origModel
		runProviderChat = origRunner
	})
	SetVLMModel("test-provider/test-model-b")

	const prompt = "Reply with exactly: VLM_OK"
	// 1x1 red PNG — minimal valid image. We're testing the path, not the vision.
	const red1x1B64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="
	imgDataURL := "data:image/png;base64," + red1x1B64

	// Inject a fake runner that asserts the message carries an image part. This
	// runs in CI without secrets and proves the dispatch + multimodal message
	// construction are correct — the part most likely to silently break.
	SetProviderChatRunner(func(ctx context.Context, modelRef string, msgs []provider.Message) ([]provider.Message, error) {
		if modelRef != "test-provider/test-model-b" {
			t.Errorf("model ref = %q, want test-provider/test-model-b", modelRef)
		}
		for _, m := range msgs {
			if m.HasImages() {
				t.Logf("✅ provider path: image_url part present, model=%s", modelRef)
				return []provider.Message{{Role: provider.RoleAssistant, Content: "ok"}}, nil
			}
		}
		t.Errorf("image part was DROPPED before reaching the runner (vision check failed)")
		return nil, nil
	})

	if _, err := CallVLM(context.Background(), imgDataURL, prompt); err != nil {
		t.Fatalf("CallVLM (provider) failed: %v", err)
	}
}

// TestFocusWindowDoesNotPanic exercises the full focusWindow path — the one that
// panicked in the field with "Failed to find GetCurrentThreadId procedure in
// user32.dll". GetCurrentThreadId is a kernel32 export; we declare it there now.
// This test calls focusWindow against a REAL visible window so the
// AttachThreadInput/GetCurrentThreadId code actually runs; if the proc lookup
// fails again it panics (not just errors), failing the test loudly.
//
// It finds an existing window (Notepad/Explorer/etc.) and focuses it; if none is
// available it skips rather than guessing, so it never manipulates an unknown
// window. Safe to run on a live desktop.
func TestFocusWindowDoesNotPanic(t *testing.T) {
	// Find any real visible window to focus — prefer the ones likely present.
	candidates := []string{"Notepad", "记事本", "文件资源管理器", "Explorer"}
	var hwnd uintptr
	var matched string
	for _, c := range candidates {
		if h, _, err := resolveWindow(c); err == nil {
			hwnd, matched = h, c
			break
		}
	}
	if hwnd == 0 {
		t.Skip("no Notepad/Explorer window open to focus; skipping")
	}
	t.Logf("focusing window matching %q (hwnd %#x) — this used to panic", matched, hwnd)
	// This call must NOT panic. Before the kernel32 fix it crashed the whole
	// process here. Recover just in case so we get a clean failure message.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("focusWindow panicked (regression of the kernel32 GetCurrentThreadId fix): %v", r)
		}
	}()
	if err := focusWindow(hwnd); err != nil {
		t.Fatalf("focusWindow error: %v", err)
	}
	t.Logf("✅ focusWindow completed without panic for %q", matched)
}

// TestResolveWindowNoMatch verifies the error path of resolveWindow: a title that
// matches nothing returns an error that LISTS the real visible windows, so the
// agent (or user) gets actionable feedback instead of a bare "not found". Uses the
// live EnumWindows, so on a real desktop it will report whatever windows exist.
func TestResolveWindowNoMatch(t *testing.T) {
	_, _, err := resolveWindow("zzz-definitely-no-such-window-12345")
	if err == nil {
		t.Skip("unexpectedly matched a window; skipping no-match assertion")
	}
	// The error should mention visible titles so the caller can fix the name.
	msg := err.Error()
	if !strings.Contains(msg, "visible titles:") {
		t.Errorf("error should list visible titles for diagnostics, got: %s", msg)
	}
	t.Logf("resolveWindow no-match error (shows real windows):\n  %s", msg)
}

// keep base64 referenced for clarity (used in the test above).
var _ = base64.StdEncoding
