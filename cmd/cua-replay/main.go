// Command cua-replay is a debugging tool for the CUA (Computer-Use Agent) vision
// loop. It does NOT run the agent — it replays/inspects what the vision pipeline
// actually sees, so you can watch the process instead of guessing.
//
// Two modes:
//
//  1. REPLAY (default, any platform): read PNG screenshots (from a dir, default
//     .hiq/attachments) and run each through the configured multimodal
//     model, printing what the VLM saw + any coordinates it returned. Copies each
//     image + the VLM's raw response into cua-replay-trace/ numbered in order, so
//     you can open the folder and walk through every frame the agent looked at.
//
//  2. LIVE (--live, Windows only): capture the current screen, run it through the
//     VLM, print the result, save the frame. Use this to test perception on the
//     live desktop without the full agent loop (no step budget, no timeouts).
//
// Why this exists: when the agent runs, it captures screenshots and calls the
// VLM, but that all happens behind the agent loop — you see tool calls in the
// terminal but NOT the images or the VLM's actual reasoning. cua-replay makes
// the vision pipeline observable and runnable in isolation, which is how you
// diagnose "did the agent see the right thing / did the VLM hallucinate".
//
// Usage:
//
//	# Replay all screenshots the last agent run captured:
//	go run ./cmd/cua-replay
//
//	# Replay a specific image with a custom task:
//	go run ./cmd/cua-replay -image .hiq/attachments/perceive-noUIA-1782574502.png -task "where is the save button"
//
//	# Live capture (Windows) — see what the VLM sees right now:
//	go run ./cmd/cua-replay -live -task "find the text input area"
//
//	# Override the model:
//	go run ./cmd/cua-replay -model provider/model
//
// Requires OPENAI_API_KEY (or any OpenAI-compatible provider key) in the environment.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.openai.com/v1" // default; override via -base flag

func main() {
	var (
		task     = flag.String("task", "找到屏幕上可以输入文字的区域，给我它的中心坐标", "what to look for in each frame")
		model    = flag.String("model", "", "multimodal model (provider/model format); set via [cowork] vlm_model in hiq.toml")
		srcDir   = flag.String("dir", ".hiq/attachments", "dir to replay screenshots from (replay mode)")
		image    = flag.String("image", "", "analyze a single image file (overrides -dir)")
		live     = flag.Bool("live", false, "capture the current screen once and analyze it (Windows)")
		traceDir = flag.String("trace", "cua-replay-trace", "where to copy frames + VLM responses")
		limit    = flag.Int("limit", 0, "max frames to process (0 = all)")
	)
	flag.Parse()

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		die("OPENAI_API_KEY not set")
	}
	if *model == "" {
		die("-model is required (provider/model format); set via [cowork] vlm_model in hiq.toml or pass -model flag")
	}

	if err := os.MkdirAll(*traceDir, 0o755); err != nil {
		die("mkdir trace: %v", err)
	}

	frames := collectFrames(*image, *srcDir, *live, *limit)
	if len(frames) == 0 {
		die("no frames to analyze — point -dir at a screenshots folder, or use -image / -live")
	}
	fmt.Printf("analyzing %d frame(s) with %s\n", len(frames), *model)
	fmt.Printf("trace → %s/\n\n", *traceDir)

	for i, fr := range frames {
		tag := fmt.Sprintf("%03d", i+1)
		w, h := pngDims(fr.bytes)
		// Copy the frame into the trace dir with a stable name so you can open the
		// folder and flip through frames in order, matching each to the printed line.
		framePath := filepath.Join(*traceDir, tag+"-"+fr.label+".png")
		_ = os.WriteFile(framePath, fr.bytes, 0o644)

		fmt.Printf("[%s] %s (%dx%d)\n", tag, fr.label, w, h)
		text, conf, cx, cy, hasCoord, noTarget, err := analyzeFrame(apiKey, *model, fr.bytes, *task)
		if err != nil {
			fmt.Printf("      ❌ VLM error: %v\n", err)
			_ = os.WriteFile(filepath.Join(*traceDir, tag+"-error.txt"), []byte(err.Error()), 0o644)
			fmt.Println()
			continue
		}
		// Save the raw response next to the frame for offline reading.
		_ = os.WriteFile(filepath.Join(*traceDir, tag+"-vlm.txt"), []byte(text), 0o644)

		switch {
		case noTarget:
			fmt.Printf("      🟡 VLM: [NO_TARGET] (conf %d)\n", conf)
		case !hasCoord:
			fmt.Printf("      🔴 VLM responded, no coordinate parsed (conf %d): %.80s\n", conf, oneLine(text))
		default:
			px := int(cx / 1000.0 * float64(w))
			py := int(cy / 1000.0 * float64(h))
			mark := "🟢"
			if conf < 50 {
				mark = "🟠"
			}
			fmt.Printf("      %s VLM: point (%.0f,%.0f) norm → (%d,%d) pixels, conf %d\n", mark, cx, cy, px, py, conf)
		}
		fmt.Println()
	}

	fmt.Printf("done. open %s/ to see every frame the agent looked at.\n", *traceDir)
}

type frame struct {
	label string // source description: filename or "live"
	bytes []byte
}

// collectFrames gathers the images to analyze, in order. Priority: a single
// -image; else -live capture (Windows); else all PNGs in -dir sorted by modtime.
func collectFrames(single, dir string, live bool, limit int) []frame {
	if single != "" {
		b, err := os.ReadFile(single)
		if err != nil {
			die("read -image: %v", err)
		}
		return []frame{{label: filepath.Base(single), bytes: b}}
	}
	if live {
		b, err := captureLive()
		if err != nil {
			die("live capture only works on Windows (this binary wasn't built for it, or capture failed): %v", err)
		}
		return []frame{{label: "live", bytes: b}}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var fs []frame
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		fs = append(fs, frame{label: e.Name(), bytes: b})
	}
	// Sort by modification time so the replay follows the order the agent saw them.
	sort.Slice(fs, func(i, j int) bool {
		return fs[i].label < fs[j].label
	})
	if limit > 0 && len(fs) > limit {
		fs = fs[len(fs)-limit:] // keep the most recent N
	}
	return fs
}

// analyzeFrame runs one image through the multimodal endpoint and parses the
// normalized coordinate / confidence / NO_TARGET. Mirrors what callProviderVLM +
// the parse helpers do in production, kept local so this tool is self-contained.
func analyzeFrame(apiKey, model string, imgBytes []byte, task string) (text string, conf int, cx, cy float64, hasCoord bool, noTarget bool, err error) {
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imgBytes)
	prompt := fmt.Sprintf(`你是一个精准的 GUI 视觉分析助手。

[任务]: %s

[诚实性约束]:
- 只输出你【真实看到】的目标坐标。
- 找不到/看不清 → [NO_TARGET]。禁止编造坐标。

[输出格式]:
1. 明确看到: [TARGET_ACTION] x=X, y=Y （归一化 0-1000，左上0,0 右下1000,1000）
2. 找不到: [NO_TARGET]

[置信度]: [CONFIDENCE: 0-100]

直接给出结论。`, task)

	body := chatRequest{Model: model, Stream: false, Messages: []chatMsg{{
		Role: "user",
		Content: []chatPart{
			{Type: "text", Text: prompt},
			{Type: "image_url", ImageURL: &chatImg{URL: dataURL}},
		},
	}}}
	raw, _ := json.Marshal(body)

	client := &http.Client{Timeout: 90 * time.Second}
	req, _ := http.NewRequestWithContext(context.Background(), "POST", defaultBaseURL+"/chat/completions", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, doErr := client.Do(req)
	if doErr != nil {
		return "", 0, 0, 0, false, false, doErr
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", 0, 0, 0, false, false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, trunc(string(rb), 200))
	}
	var cr chatResponse
	if err := json.Unmarshal(rb, &cr); err != nil {
		return "", 0, 0, 0, false, false, fmt.Errorf("parse: %w", err)
	}
	if len(cr.Choices) == 0 {
		return "", 0, 0, 0, false, false, fmt.Errorf("empty choices")
	}
	text = cr.Choices[0].Message.Content
	conf = parseConf(text)
	cx, cy, hasCoord = parseCoord(text)
	noTarget = strings.Contains(text, "[NO_TARGET]")
	return text, conf, cx, cy, hasCoord, noTarget, nil
}

// --- chat request/response types ---

type chatRequest struct {
	Model    string    `json:"model"`
	Stream   bool      `json:"stream"`
	Messages []chatMsg `json:"messages"`
}
type chatMsg struct {
	Role    string     `json:"role"`
	Content []chatPart `json:"content"`
}
type chatPart struct {
	Type     string   `json:"type"`
	Text     string   `json:"text,omitempty"`
	ImageURL *chatImg `json:"image_url,omitempty"`
}
type chatImg struct {
	URL string `json:"url"`
}
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// --- parse helpers ---

var coordPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)\[TARGET_ACTION\].*?['"]?x['"]?\s*[:=]\s*(\d+\.?\d*)\s*[,;]\s*['"]?y['"]?\s*[:=]\s*(\d+\.?\d*)`),
	regexp.MustCompile(`["']x["']\s*:\s*(\d+\.?\d*)\s*,\s*["']y["']\s*:\s*(\d+\.?\d*)`),
	regexp.MustCompile(`x[：:]\s*(\d+\.?\d*)\s*[,，;；]\s*y[：:]\s*(\d+\.?\d*)`),
	regexp.MustCompile(`\bx\s*[=:]\s*(\d+\.?\d*)\s*[,，]\s*y\s*[=:]\s*(\d+\.?\d*)`),
}
var confRe = regexp.MustCompile(`(?i)\[CONFIDENCE[:\s]+(\d{1,3})\]`)

func parseCoord(text string) (float64, float64, bool) {
	for _, p := range coordPatterns {
		m := p.FindStringSubmatch(text)
		if m != nil {
			x, e1 := strconv.ParseFloat(m[1], 64)
			y, e2 := strconv.ParseFloat(m[2], 64)
			if e1 == nil && e2 == nil && x >= 0 && x <= 1000 && y >= 0 && y <= 1000 {
				return x, y, true
			}
		}
	}
	return 0, 0, false
}
func parseConf(text string) int {
	m := confRe.FindStringSubmatch(text)
	if m == nil {
		return 50
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		return 50
	}
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func pngDims(b []byte) (int, int) {
	cfg, err := pngDecodeConfig(b)
	if err != nil {
		return 0, 0
	}
	return cfg.w, cfg.h
}

type dims struct{ w, h int }

func pngDecodeConfig(b []byte) (dims, error) {
	// Minimal PNG IHDR width/height parse (bytes 16-24), avoiding an image/png
	// dependency so this file stays stdlib-only.
	if len(b) < 24 || string(b[0:8]) != "\x89PNG\r\n\x1a\n" {
		return dims{}, fmt.Errorf("not a PNG")
	}
	w := int(b[16])<<24 | int(b[17])<<16 | int(b[18])<<8 | int(b[19])
	h := int(b[20])<<24 | int(b[21])<<16 | int(b[22])<<8 | int(b[23])
	return dims{w, h}, nil
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
func oneLine(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
}
func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "cua-replay: "+format+"\n", args...)
	os.Exit(1)
}
