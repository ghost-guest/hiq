package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/zzycxz/hiq/internal/tool"
)

// audio_understand is the audio counterpart to image_understand: it reads an
// audio file (typically a user-uploaded attachment referenced as
// @.hiq/attachments/x.mp3) and transcribes it via the configured voice
// model (CallSTT), returning the transcript as text.
//
// Single-track audio path: user audio is NEVER sent to the main model as an
// input_audio content part. ResolveRefs leaves a text <audio path="...">
// reference instead; the main model sees it and calls this tool to obtain the
// transcript as text. This keeps audio content parts out of the main-model
// request (most models can't handle them), and lets the agent reason about,
// translate, or summarize the transcript with its normal text abilities.
//
// Only transcription is offered here (CallSTT is a fixed transcribe prompt).
// Anything beyond — translation, summarization, speaker labelling — the agent
// does itself once it has the transcript, just like it reasons over image text
// returned by image_understand.

func init() { tool.RegisterBuiltin(audioUnderstand{}) }

// audioUnderstand reads an audio file and returns its transcript via CallSTT.
// workDir, when non-empty, is the directory a relative path is resolved
// against (see resolveIn); the zero value registered at init resolves against
// the process working directory.
type audioUnderstand struct {
	workDir string
}

// audioUnderstandMaxBytes mirrors control's maxFileAttachmentBytes
// (attachments.go) for non-image attachments. STT providers also cap request
// body size; 25 MB comfortably covers several minutes of compressed audio.
const audioUnderstandMaxBytes = 25 * 1024 * 1024

func (audioUnderstand) Name() string { return "audio_understand" }

func (audioUnderstand) Description() string {
	return "Transcribe a user-uploaded audio file: read an audio file (the path appears in the conversation as an <audio path=\"...\"> reference, e.g. .hiq/attachments/xxx.mp3) and return its speech-to-text transcript via the configured voice model. Use this whenever the user attached an audio clip and you need to know what is said in it — the main model cannot hear the audio bytes directly. Once you have the transcript you can answer, translate, or summarize it with your normal text abilities."
}

func (audioUnderstand) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "path":{
      "type":"string",
      "description":"Audio file path — use the path from the <audio path=\"...\"> reference in the conversation (e.g. .hiq/attachments/xxx.mp3)."
    }
  },
  "required":["path"]
}`)
}

func (audioUnderstand) ReadOnly() bool { return true }

func (a audioUnderstand) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	path := resolveIn(a.workDir, p.Path)

	// Reject symlinks and directories, cap size before reading — mirrors
	// image_understand's guards so a crafted attachment can't redirect to an
	// arbitrary file or exhaust memory.
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", p.Path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("audio path must not be a symlink")
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not an audio file", p.Path)
	}
	if info.Size() <= 0 || info.Size() > audioUnderstandMaxBytes {
		return "", fmt.Errorf("audio must be between 1 byte and 25 MB (got %d bytes)", info.Size())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", p.Path, err)
	}

	mime := sniffAudioMIME(data, p.Path)
	if mime == "" {
		return "", fmt.Errorf("%s is not a recognized audio file", p.Path)
	}

	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)

	transcript, err := CallSTT(ctx, dataURL, "auto")
	if err != nil {
		return "", fmt.Errorf("audio_understand (%s): %w", filepath.Base(p.Path), err)
	}
	if strings.TrimSpace(transcript) == "" {
		return "(no speech detected in audio)", nil
	}
	return transcript, nil
}

// sniffAudioMIME reports the audio MIME type of data, using content sniffing
// first (http.DetectContentType recognizes mp3/wav/ogg) and falling back to the
// file extension for formats the sniffer misses. Returns "" for non-audio.
func sniffAudioMIME(data []byte, path string) string {
	peek := data
	if len(peek) > 512 {
		peek = peek[:512]
	}
	mime := http.DetectContentType(peek)
	if strings.HasPrefix(mime, "audio/") {
		return mime
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".m4a":
		return "audio/mp4"
	case ".flac":
		return "audio/flac"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".aac":
		return "audio/aac"
	case ".webm":
		return "audio/webm"
	}
	return ""
}
