package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/fairpeer/internal/provider"
)

// 1x1 red PNG — minimal valid image. Used by cua_path_test.go too; duplicated
// here to keep this test self-contained and free of the windows build tag.
const imgUnderstandRed1x1B64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="

// mustWriteRedPNG writes the 1x1 red PNG to a temp file and returns its path.
func mustWriteRedPNG(t *testing.T, dir, name string) string {
	t.Helper()
	pngBytes, err := base64.StdEncoding.DecodeString(imgUnderstandRed1x1B64)
	if err != nil {
		t.Fatalf("decode red PNG: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, pngBytes, 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return p
}

// vlmStub installs a fake VLM model + runner for the duration of the test,
// returning wantText when called. It asserts the runner received an image_url
// part (so we catch "image dropped before VLM"). Restore happens via t.Cleanup.
func vlmStub(t *testing.T, wantText string) {
	t.Helper()
	origModel := vlmModel
	origRunner := runProviderChat
	t.Cleanup(func() {
		vlmModel = origModel
		runProviderChat = origRunner
	})
	SetVLMModel("test-provider/test-vlm")
	SetProviderChatRunner(func(ctx context.Context, modelRef string, msgs []provider.Message) ([]provider.Message, error) {
		for _, m := range msgs {
			if m.HasImages() {
				return []provider.Message{{Role: provider.RoleAssistant, Content: wantText}}, nil
			}
		}
		t.Errorf("image_understand: image part dropped before reaching VLM runner")
		return nil, nil
	})
}

func TestImageUnderstandRequiresPathAndPrompt(t *testing.T) {
	vlmStub(t, "unused")
	tool := imageUnderstand{}

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected error when path is missing")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"x.png"}`)); err == nil {
		t.Fatal("expected error when prompt is missing")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"x.png","prompt":"  "}`)); err == nil {
		t.Fatal("expected error when prompt is whitespace")
	}
}

func TestImageUnderstandRejectsNonImage(t *testing.T) {
	vlmStub(t, "unused")
	dir := t.TempDir()
	// A text file with a .txt extension: sniff fails, extension not in image set.
	txtPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(txtPath, []byte("just text, not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path": txtPath, "prompt": "describe"})
	_, err := imageUnderstand{}.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "not a recognized image") {
		t.Fatalf("expected non-image rejection, got: %v", err)
	}
}

func TestImageUnderstandRejectsSymlink(t *testing.T) {
	vlmStub(t, "unused")
	dir := t.TempDir()
	pngPath := mustWriteRedPNG(t, dir, "real.png")
	linkPath := filepath.Join(dir, "link.png")
	if err := os.Symlink(pngPath, linkPath); err != nil {
		// Symlink creation may need elevated perms on some Windows setups; skip
		// rather than fail if the OS won't let us set up the test.
		t.Skipf("cannot create symlink for test: %v", err)
	}
	args, _ := json.Marshal(map[string]string{"path": linkPath, "prompt": "describe"})
	_, err := imageUnderstand{}.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got: %v", err)
	}
}

func TestImageUnderstandReturnsVLMDescription(t *testing.T) {
	const wantDesc = "a 1x1 red pixel"
	vlmStub(t, wantDesc)

	dir := t.TempDir()
	pngPath := mustWriteRedPNG(t, dir, "shot.png")

	args, _ := json.Marshal(map[string]string{"path": pngPath, "prompt": "what color is this pixel?"})
	got, err := imageUnderstand{}.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	// Result is now the VLM description prefixed with an objective header
	// ([image: name | WxH | mime | vision: model]). Verify the VLM answer is
	// intact AND the header carries the image facts (incl. PNG staying PNG —
	// the format-preservation fix, not smashed to jpeg).
	if !strings.Contains(got, wantDesc) {
		t.Errorf("result missing VLM answer %q; got %q", wantDesc, got)
	}
	if !strings.Contains(got, "[image: shot.png | 1x1 | image/png |") {
		t.Errorf("result missing objective header (name/dims/mime); got %q", got)
	}
}

func TestImageUnderstandErrorsWhenNoVLMConfigured(t *testing.T) {
	// No vlmStub here: simulate an unconfigured VLM by clearing the model.
	origModel := vlmModel
	origRunner := runProviderChat
	t.Cleanup(func() {
		vlmModel = origModel
		runProviderChat = origRunner
	})
	SetVLMModel("")

	dir := t.TempDir()
	pngPath := mustWriteRedPNG(t, dir, "shot.png")
	args, _ := json.Marshal(map[string]string{"path": pngPath, "prompt": "describe"})
	_, err := imageUnderstand{}.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "no VLM model configured") {
		t.Fatalf("expected unconfigured-VLM error, got: %v", err)
	}
}

func TestImageUnderstandSchemaRequiredFields(t *testing.T) {
	schema := imageUnderstand{}.Schema()
	var s struct {
		Type       string            `json:"type"`
		Properties map[string]any    `json:"properties"`
		Required   []string          `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if len(s.Required) != 2 {
		t.Errorf("required = %v, want exactly [path prompt]", s.Required)
	}
	hasPath, hasPrompt := false, false
	for _, r := range s.Required {
		if r == "path" {
			hasPath = true
		}
		if r == "prompt" {
			hasPrompt = true
		}
	}
	if !hasPath || !hasPrompt {
		t.Errorf("schema must require both path and prompt, required=%v", s.Required)
	}
}

func TestImageUnderstandReadOnly(t *testing.T) {
	tool := imageUnderstand{}
	if !tool.ReadOnly() {
		t.Error("image_understand should be ReadOnly (it only reads a file + calls the VLM)")
	}
}
