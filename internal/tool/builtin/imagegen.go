package builtin

import (
	"bytes"
	"encoding/base64"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/tool"
)

func init() { tool.RegisterBuiltin(imageGenerate{}) }

// imageGenerate (upgrade spec 5-6) generates an image via an OpenAI-compatible
// /images/generations endpoint and stores it under .hiq/attachments so
// the existing attachment pipeline (agent.go's attachmentImageRe → ToolCard
// thumbnails + lightbox) renders it with zero extra wiring. The endpoint/base
// URL/API key reuse the image model's provider entry, so users configure one
// [[models]] with image capabilities and the tool becomes useful; with no
// image model configured the tool self-describes as unavailable (the registry
// hides it via HiddenOnMissingConfig below — simpler: it returns an error).
type imageGenerate struct{ roots []string }

func (imageGenerate) Name() string { return "image_generate" }

func (imageGenerate) Description() string {
	return "Generate an image from a text prompt. Returns the saved file path as a markdown image so it renders in the chat. Requires an image-capable model configured (image model)."
}

func (imageGenerate) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","description":"What to draw"},"size":{"type":"string","description":"Optional: 1024x1024 | 1792x1024 | 1024x1792"}},"required":["prompt"]}`)
}

func (imageGenerate) ReadOnly() bool { return false }

type imageGenConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

// imageGenCfg reads the image model config from the environment — the same
// three variables the boot provider layer honours for image entries.
func imageGenCfg() (imageGenConfig, bool) {
	cfg := imageGenConfig{
		BaseURL: strings.TrimRight(os.Getenv("HIQ_IMAGE_BASE_URL"), "/"),
		APIKey:  os.Getenv("HIQ_IMAGE_API_KEY"),
		Model:   os.Getenv("HIQ_IMAGE_MODEL"),
	}
	return cfg, cfg.BaseURL != "" && cfg.APIKey != "" && cfg.Model != ""
}

var imageExtRe = regexp.MustCompile(`\.(png|jpg|jpeg|webp|gif)$`)

func (g imageGenerate) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Prompt string `json:"prompt"`
		Size   string `json:"size"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Prompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}
	cfg, ok := imageGenCfg()
	if !ok {
		return "", fmt.Errorf("image generation is not configured: set HIQ_IMAGE_BASE_URL / HIQ_IMAGE_API_KEY / HIQ_IMAGE_MODEL")
	}

	body := map[string]any{"model": cfg.Model, "prompt": p.Prompt, "n": 1, "response_format": "b64_json"}
	if p.Size != "" {
		body["size"] = p.Size
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL+"/images/generations", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("image request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("image API %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode image response: %w", err)
	}
	if len(out.Data) == 0 {
		return "", fmt.Errorf("image API returned no data")
	}

	// Save under .hiq/attachments (confined to the workspace roots so the
	// markdown path the model echoes resolves through the attachment viewer).
	var data []byte
	if out.Data[0].B64JSON != "" {
		data, err = base64Decode(out.Data[0].B64JSON)
		if err != nil {
			return "", fmt.Errorf("decode b64 image: %w", err)
		}
	} else if out.Data[0].URL != "" {
		data, err = downloadImage(ctx, out.Data[0].URL)
		if err != nil {
			return "", err
		}
	} else {
		return "", fmt.Errorf("image API returned neither b64_json nor url")
	}

	name := fmt.Sprintf("gen-%s.png", time.Now().Format("20060102-150405"))
	for _, root := range g.roots {
		dir := filepath.Join(root, ".hiq", "attachments")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", path, err)
		}
		rel := ".hiq/attachments/" + name
		return fmt.Sprintf("generated image saved:\n![image](%s)", rel), nil
	}
	if len(g.roots) == 0 {
		return "", fmt.Errorf("no workspace root to store the image in")
	}
	return "", fmt.Errorf("could not create .hiq/attachments under any root")
}

func downloadImage(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download image: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

// base64Decode decodes standard base64 (images API payload).
func base64Decode(s string) ([]byte, error) {
	dst := make([]byte, base64.StdEncoding.DecodedLen(len(s)))
	n, err := base64.StdEncoding.Decode(dst, []byte(s))
	if err != nil {
		return nil, err
	}
	return dst[:n], nil
}
