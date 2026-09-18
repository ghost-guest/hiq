package main

import (
	"context"

	"github.com/zzycxz/hiq/internal/tool/builtin"
)

// VoiceModelConfigured reports whether a voice model is set ([cowork]
// voice_model). The frontend mic button uses this to render enabled vs
// disabled-with-hint — it costs nothing (reads an in-memory field) so it's
// safe to call on every Composer render.
func (a *App) VoiceModelConfigured() bool {
	return builtin.ConfiguredVoiceModel() != ""
}

// TranscribeAudio sends audio (a "data:audio/wav;base64,..." data URL produced
// by the frontend Web Audio recorder) to the configured voice model and returns
// the transcribed text. language hints the locale ("auto" lets the model
// decide). Returns a clear error when no voice model is configured — the
// frontend surfaces this as a toast.
//
// This is the single entry point for the mic button. It routes through
// builtin.CallSTT, which sends the audio as an input_audio content part to the
// model's chat/completions endpoint — one unified interface for every audio-
// capable model (MiMo-V2.5 / GLM-4-Voice / GPT-4o-audio / ...).
func (a *App) TranscribeAudio(audioDataURL, language string) (string, error) {
	return builtin.CallSTT(context.Background(), audioDataURL, language)
}
