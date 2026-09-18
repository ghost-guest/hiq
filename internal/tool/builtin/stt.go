package builtin

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/zzycxz/hiq/internal/provider"
)

// STT (speech-to-text) is the single voice entry: it sends audio (as a base64
// data URL) to the user-configured voice model via the standard OpenAI
// multimodal chat format (input_audio content parts) and returns the model's
// transcribed text. boot.go resolves [cowork] voice_model and injects it via
// SetVoiceModel; the provider chat runner is shared with VLM
// (SetProviderChatRunner, injected from boot.go). When no voice model is
// configured, CallSTT returns a clear configuration error.
//
// This deliberately uses the multimodal-chat path (chat/completions +
// input_audio), NOT a dedicated /audio/transcriptions endpoint. One unified
// interface serves every audio-capable model — the model just needs to accept
// input_audio content (GPT-4o-audio / MiMo-V2.5 / Qwen-Omni / Doubao-Omni
// style). The same CallSTT entry backs both voice input (mic → text → compose
// box) and audio-attachment understanding (agent tool).

var (
	sttMu    sync.RWMutex
	sttModel string // configured voice model ref (from [cowork] voice_model); "" = unconfigured
)

// SetVoiceModel sets the STT model used by CallSTT. boot.go calls this after
// resolving config. An empty model is accepted; CallSTT then returns a "no
// voice model configured" error, guiding the user to set an audio-capable
// model in Settings.
func SetVoiceModel(model string) {
	sttMu.Lock()
	defer sttMu.Unlock()
	sttModel = strings.TrimSpace(model)
}

// ConfiguredVoiceModel returns the current voice model under the read lock.
// Exported so the desktop layer can report whether voice input is available
// (mic button enabled vs disabled-with-hint), and so boot.go can re-inject on
// profile switch without a rebuild race.
func ConfiguredVoiceModel() string {
	sttMu.RLock()
	defer sttMu.RUnlock()
	return sttModel
}

// CallSTT sends audio (base64 data URL, e.g. "data:audio/wav;base64,...") to
// the configured voice model and returns the transcribed text. language hints
// the locale ("auto" lets the model decide). When no voice model is configured
// it returns a clear configuration error.
//
// The audio is sent as an input_audio content part alongside a prompt asking
// the model to transcribe; the model's text response is the transcript. This
// works for any model that accepts input_audio — no per-vendor adapter.
func CallSTT(ctx context.Context, audioDataURL, language string) (string, error) {
	model := ConfiguredVoiceModel()
	if model == "" {
		return "", fmt.Errorf("no voice model configured: set an audio-capable speech-to-text model in Settings")
	}
	prompt := "请将这段语音准确转写为文字，只输出转写结果，不要添加任何解释、标注或标点外的符号。"
	if lang := strings.TrimSpace(language); lang != "" && !strings.EqualFold(lang, "auto") {
		prompt = fmt.Sprintf("请将这段语音准确转写为文字（语言：%s），只输出转写结果，不要添加任何解释。", lang)
	}
	msgs := []provider.Message{
		provider.AudioMessage(provider.RoleUser, prompt, audioDataURL),
	}
	resp, err := runProviderChat(ctx, model, msgs)
	if err != nil {
		return "", fmt.Errorf("provider STT (%s): %w", model, err)
	}
	if len(resp) > 0 {
		return provider.ContentString(resp[0].Content), nil
	}
	return "", nil
}
