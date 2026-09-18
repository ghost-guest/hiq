package builtin

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/zzycxz/hiq/internal/provider"
)

// VLM is the single vision-language-model entry: it sends an image (as a base64
// data URL) plus a text prompt to the user-configured vision model via the
// standard OpenAI multimodal chat format (image_url content parts), and returns
// the model's text response.
//
// boot.go resolves [cowork] vlm_model (falling back to screenshot_vlm_model)
// and injects it via SetVLMModel, plus the provider chat runner via
// SetProviderChatRunner. When no vision model is configured, CallVLM returns a
// clear configuration error instead of attempting any fallback.

var (
	vlmMu    sync.RWMutex
	vlmModel string // the configured vision model ref (from [cowork] vlm_model); "" = unconfigured
)

// SetVLMModel sets the vision model used by CallVLM. boot.go calls this after
// resolving config. An empty model is accepted; CallVLM will then return a
// "no VLM model configured" error, guiding the user to set a vision-capable
// model in Settings.
func SetVLMModel(model string) {
	vlmMu.Lock()
	defer vlmMu.Unlock()
	vlmModel = strings.TrimSpace(model)
}

// configuredVLMModel returns the current vision model under the read lock.
func configuredVLMModel() string {
	vlmMu.RLock()
	defer vlmMu.RUnlock()
	return vlmModel
}

// CallVLM sends an image (base64 data URL) + prompt to the configured vision
// model and returns its text response. This is the unified entry for
// screen_perceive and any other vision use. When no vision model is configured
// it returns a clear configuration error.
func CallVLM(ctx context.Context, imgDataURL string, prompt string) (string, error) {
	model := configuredVLMModel()
	if model == "" {
		return "", fmt.Errorf("no VLM model configured: set a vision-capable model in Settings")
	}
	return callProviderVLM(ctx, model, imgDataURL, prompt)
}

// callProviderVLM uses the provider layer's multimodal chat (qwen/kimi/etc). The
// provider supports image_url content parts (provider.ImageMessage); we
// construct a multimodal user message and run it through the injected runner.
// The model must be vision-capable (provider.Vision=true), enforced by boot.
func callProviderVLM(ctx context.Context, model, imgDataURL, prompt string) (string, error) {
	msgs := []provider.Message{
		provider.ImageMessage(provider.RoleUser, prompt, imgDataURL),
	}
	resp, err := runProviderChat(ctx, model, msgs)
	if err != nil {
		return "", fmt.Errorf("provider VLM (%s): %w", model, err)
	}
	if len(resp) > 0 {
		return provider.ContentString(resp[0].Content), nil
	}
	return "", nil
}

// runProviderChat is a thin bridge to the provider layer. boot.go injects the
// real runner via SetProviderChatRunner; the zero value returns an error so a
// misconfigured boot surfaces clearly instead of a nil panic.
var runProviderChat = func(ctx context.Context, model string, msgs []provider.Message) ([]provider.Message, error) {
	return nil, fmt.Errorf("provider VLM bridge not initialized")
}

// SetProviderChatRunner injects the provider chat runner from boot.go (which has
// access to the resolved provider config). Avoids a circular import between
// tool/builtin and provider/boot.
func SetProviderChatRunner(fn func(ctx context.Context, model string, msgs []provider.Message) ([]provider.Message, error)) {
	runProviderChat = fn
}
