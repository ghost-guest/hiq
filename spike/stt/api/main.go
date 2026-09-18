// Command sttspike verifies STT (speech-to-text) API connectivity against
// every hiq-supported vendor that exposes an OpenAI-compatible (or
// near-compatible) STT endpoint.
//
// This is an ISOLATED SPIKE. It does not import or depend on hiq in any
// way. Run it from spike/stt/api/. API keys are read from environment
// variables and never written to disk.
//
// Vendor coverage (see README for the full matrix):
//
//   multipart (POST {base}/audio/transcriptions, OpenAI Whisper-style):
//     stepfun, zhipu, openai, siliconflow, openrouter
//   chat (POST {base}/chat/completions with input_audio, GPT-4o-audio-style):
//     mimo
//   private-protocol vendors (NOT covered here, need separate adapters):
//     aliyun (DashScope), volcengine, xfyun, baidu, tencent
//   no STT API at all: deepseek, moonshot, minimax, anthropic
//
// Subcommands:
//
//	gen       [-out ../samples/beep.wav] [-dur 3s] [-freq 440]
//	          Generate a test WAV (sine beep) to use as a sample.
//
//	test      -provider <name> -file <audio> [-model X]
//	          Call one provider's STT endpoint with the given file.
//
//	matrix    -files f1,f2,...
//	          For each provider whose key is set, test each file and print a grid.
//
//	probe     [no flags]
//	          Hit every provider's STT endpoint with a fake key to learn whether
//	          the endpoint exists (401/403) vs is missing (404). NO API KEY NEEDED.
//
//	providers List configured providers + key status.
//
// Examples:
//
//	go run . gen
//	go run . probe
//	go run . test -provider stepfun -file ../samples/beep.wav
//	go run . matrix -files ../samples/beep.wav,../samples/voice.webm
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// mode controls the request shape.
const (
	modeMultipart = "multipart" // POST {base}/audio/transcriptions (OpenAI Whisper)
	modeChat      = "chat"      // POST {base}/chat/completions with input_audio (GPT-4o-audio / MiMo-ASR)
)

// provider describes one STT endpoint. baseURL is everything up to (but
// excluding) the trailing endpoint path.
type provider struct {
	name       string // short id
	display    string // human label
	baseURL    string
	model      string
	keyEnv     string
	mode       string // modeMultipart | modeChat
	authHeader string // "" => Authorization: Bearer; otherwise the literal header name (e.g. "api-key")
	asrLang    string // for chat mode, optional asr_options.language
}

// providerOrder governs iteration order in matrix/probe/providers.
var providerOrder = []string{"stepfun", "zhipu", "siliconflow", "openrouter", "mimo", "openai"}

var providers = map[string]provider{
	"stepfun":     {"stepfun", "阶跃星辰", "https://api.stepfun.com/v1", "stepaudio-2.5-asr", "STEPFUN_API_KEY", modeMultipart, "", ""},
	"zhipu":       {"zhipu", "智谱 GLM", "https://open.bigmodel.cn/api/paas/v4", "glm-asr-2512", "ZHIPU_API_KEY", modeMultipart, "", ""},
	"siliconflow": {"siliconflow", "硅基流动", "https://api.siliconflow.cn/v1", "FunAudioLLM/SenseVoiceSmall", "SILICONFLOW_API_KEY", modeMultipart, "", ""},
	"openrouter":  {"openrouter", "OpenRouter", "https://openrouter.ai/api/v1", "openai/whisper-1", "OPENROUTER_API_KEY", modeMultipart, "", ""},
	"mimo":        {"mimo", "小米 MiMo", "https://api.xiaomimimo.com/v1", "mimo-v2.5-asr", "MIMO_API_KEY", modeChat, "api-key", "auto"},
	"openai":      {"openai", "OpenAI", "https://api.openai.com/v1", "whisper-1", "OPENAI_API_KEY", modeMultipart, "", ""},
}

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "gen":
		cmdGen(os.Args[2:])
	case "test":
		cmdTest(os.Args[2:])
	case "matrix":
		cmdMatrix(os.Args[2:])
	case "probe":
		cmdProbe(os.Args[2:])
	case "providers":
		for _, name := range providerOrder {
			p := providers[name]
			set := "unset"
			if os.Getenv(p.keyEnv) != "" {
				set = "SET"
			}
			fmt.Printf("%-12s %-10s %-42s model=%-30s %s=%s\n", p.name, p.mode, p.baseURL, p.model, p.keyEnv, set)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `sttspike — isolated STT API verification (no hiq dependency)

Vendors covered (OpenAI-compatible or near): stepfun, zhipu, siliconflow,
openrouter, mimo, openai. (aliyun/volcengine/xfyun/baidu/tencent use private
protocols — not covered; deepseek/moonshot/minimax/anthropic have no STT.)

Subcommands:
  gen       [-out ../samples/beep.wav] [-dur 3s] [-freq 440]
  test      -provider <name> -file <audio> [-model X]
  matrix    -files f1,f2,...
  probe     (no flags — needs no API key, just checks endpoints exist)
  providers

Examples:
  go run . gen
  go run . probe
  go run . test -provider stepfun -file ../samples/beep.wav
  go run . matrix -files ../samples/beep.wav,../samples/voice.webm
`)
	os.Exit(2)
}

// ---------------- gen ----------------

func cmdGen(args []string) {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	out := fs.String("out", "../samples/beep.wav", "output wav path")
	dur := fs.Duration("dur", 3*time.Second, "beep duration")
	freq := fs.Float64("freq", 440, "beep frequency Hz")
	fs.Parse(args)

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := writeBeepWav(*out, *dur, *freq); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s (%s, %.0fHz sine beep)\n", *out, *dur, *freq)
	fmt.Println("note: a pure beep tests the wire (HTTP/auth/format) but ASR may")
	fmt.Println("      return empty text — drop a real voice clip in samples/ for")
	fmt.Println("      a meaningful recognition test.")
}

// writeBeepWav writes a mono 16kHz 16-bit PCM WAV containing a sine beep with
// short fade in/out to avoid clicks.
func writeBeepWav(path string, dur time.Duration, freq float64) error {
	const sampleRate = 16000
	n := int(dur.Seconds() * sampleRate)
	pcm := make([]int16, n)
	amp := 0.3 * float64(0x7FFF)
	fade := int(0.05 * sampleRate)
	if fade > n/2 {
		fade = n / 2
	}
	for i := 0; i < n; i++ {
		t := float64(i) / sampleRate
		env := 1.0
		switch {
		case i < fade:
			env = float64(i) / float64(fade)
		case i > n-fade:
			env = float64(n-i) / float64(fade)
		}
		pcm[i] = int16(amp * env * math.Sin(2*math.Pi*freq*t))
	}

	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+2*len(pcm)))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1)) // mono
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate*2)) // byte rate
	_ = binary.Write(&buf, binary.LittleEndian, uint16(2))            // block align
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16))           // bits per sample
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(2*len(pcm)))
	for _, s := range pcm {
		_ = binary.Write(&buf, binary.LittleEndian, s)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// ---------------- test ----------------

func cmdTest(args []string) {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	provName := fs.String("provider", "", "provider name (see providers)")
	file := fs.String("file", "", "audio file path")
	model := fs.String("model", "", "override model name (optional)")
	fs.Parse(args)

	if *provName == "" || *file == "" {
		fs.Usage()
		os.Exit(2)
	}
	p, ok := providers[*provName]
	if !ok {
		log.Fatalf("unknown provider %q (want one of %s)", *provName, strings.Join(providerOrder, "|"))
	}
	if *model != "" {
		p.model = *model
	}

	endpoint := endpointOf(p)
	start := time.Now()
	text, status, raw, err := transcribe(p, *file)
	elapsed := time.Since(start)

	fmt.Printf("provider = %s (%s)\n", p.name, p.display)
	fmt.Printf("model    = %s\n", p.model)
	fmt.Printf("endpoint = %s\n", endpoint)
	fmt.Printf("mode     = %s\n", p.mode)
	fmt.Printf("file     = %s\n", *file)
	fmt.Printf("HTTP     = %d   (%.2fs)\n", status, elapsed.Seconds())
	if err != nil {
		fmt.Printf("ERROR    = %v\n", err)
		if len(raw) > 0 {
			fmt.Printf("body     = %s\n", truncate(string(raw), 800))
		}
		os.Exit(1)
	}
	fmt.Printf("text     = %q\n", text)
}

// endpointOf returns the full URL a provider posts to.
func endpointOf(p provider) string {
	suffix := "/audio/transcriptions"
	if p.mode == modeChat {
		suffix = "/chat/completions"
	}
	return strings.TrimRight(p.baseURL, "/") + suffix
}

// transcribe dispatches by mode and returns (text, httpStatus, rawBody, err).
func transcribe(p provider, file string) (text string, status int, raw []byte, err error) {
	if p.mode == modeChat {
		return transcribeChat(p, file)
	}
	return transcribeMultipart(p, file)
}

// transcribeMultipart posts file to {base}/audio/transcriptions.
func transcribeMultipart(p provider, file string) (string, int, []byte, error) {
	key := os.Getenv(p.keyEnv)
	if key == "" {
		return "", 0, nil, fmt.Errorf("env %s not set", p.keyEnv)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", 0, nil, err
	}
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	if err := w.WriteField("model", p.model); err != nil {
		return "", 0, nil, err
	}
	fw, err := w.CreateFormFile("file", filepath.Base(file))
	if err != nil {
		return "", 0, nil, err
	}
	if _, err := fw.Write(data); err != nil {
		return "", 0, nil, err
	}
	if err := w.Close(); err != nil {
		return "", 0, nil, err
	}
	url := endpointOf(p)
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return "", 0, nil, err
	}
	setAuth(req, p, key)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return doSTT(req, "text")
}

// transcribeChat posts file to {base}/chat/completions with an input_audio
// block (GPT-4o-audio / MiMo-ASR style).
func transcribeChat(p provider, file string) (string, int, []byte, error) {
	key := os.Getenv(p.keyEnv)
	if key == "" {
		return "", 0, nil, fmt.Errorf("env %s not set", p.keyEnv)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", 0, nil, err
	}
	mime := extToMime(file)
	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)

	payload := map[string]any{
		"model": p.model,
		"messages": []map[string]any{
			{"role": "user", "content": []map[string]any{
				{"type": "input_audio", "input_audio": map[string]string{"data": dataURL}},
			}},
		},
	}
	if p.asrLang != "" {
		payload["asr_options"] = map[string]string{"language": p.asrLang}
	}
	bodyJSON, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, endpointOf(p), bytes.NewReader(bodyJSON))
	if err != nil {
		return "", 0, nil, err
	}
	setAuth(req, p, key)
	req.Header.Set("Content-Type", "application/json")
	// chat mode returns text under choices[0].message.content
	return doSTT(req, "choices.0.message.content")
}

// setAuth applies the provider's auth header.
func setAuth(req *http.Request, p provider, key string) {
	if p.authHeader != "" {
		req.Header.Set(p.authHeader, key)
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
}

// doSTT sends req and extracts text by JSON path. path "text" => top-level
// {text}; path "choices.0.message.content" => nested.
func doSTT(req *http.Request, path string) (string, int, []byte, error) {
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	status := resp.StatusCode
	if status != http.StatusOK {
		return "", status, raw, fmt.Errorf("non-200 status %d", status)
	}
	text, err := extractByPath(raw, path)
	if err != nil {
		return "", status, raw, fmt.Errorf("decode (%s): %w (body: %s)", path, err, truncate(string(raw), 400))
	}
	return text, status, raw, nil
}

// extractByPath pulls a string out of JSON by a dotted path.
func extractByPath(data []byte, path string) (string, error) {
	var top any
	if err := json.Unmarshal(data, &top); err != nil {
		return "", err
	}
	cur := top
	for _, seg := range strings.Split(path, ".") {
		switch v := cur.(type) {
		case map[string]any:
			cur = v[seg]
		case []any:
			if seg == "0" && len(v) > 0 {
				cur = v[0]
			} else {
				return "", fmt.Errorf("bad array path %q", path)
			}
		default:
			return "", fmt.Errorf("bad path %q", path)
		}
		if cur == nil {
			return "", fmt.Errorf("nil at %q", path)
		}
	}
	switch v := cur.(type) {
	case string:
		return v, nil
	default:
		b, _ := json.Marshal(v)
		return string(b), nil
	}
}

// ---------------- matrix ----------------

func cmdMatrix(args []string) {
	fs := flag.NewFlagSet("matrix", flag.ExitOnError)
	filesStr := fs.String("files", "", "comma-separated audio files")
	fs.Parse(args)
	if *filesStr == "" {
		log.Fatal("need -files f1,f2,...")
	}
	files := strings.Split(*filesStr, ",")

	var provs []provider
	for _, name := range providerOrder {
		p := providers[name]
		if os.Getenv(p.keyEnv) != "" {
			provs = append(provs, p)
		} else {
			fmt.Printf("# skipped %s (%s unset)\n", p.name, p.keyEnv)
		}
	}
	if len(provs) == 0 {
		log.Fatal("no provider keys are set")
	}

	nameW := 24
	for _, f := range files {
		if len(filepath.Base(f)) > nameW {
			nameW = len(filepath.Base(f))
		}
	}
	cellW := 16

	hdr := fmt.Sprintf("%-*s", nameW, "file \\ provider")
	for _, p := range provs {
		hdr += fmt.Sprintf(" | %-*s", cellW, p.name)
	}
	fmt.Println()
	fmt.Println(hdr)
	fmt.Println(strings.Repeat("-", len(hdr)))

	for _, f := range files {
		row := fmt.Sprintf("%-*s", nameW, filepath.Base(f))
		for _, p := range provs {
			row += fmt.Sprintf(" | %-*s", cellW, cellFor(p, f))
		}
		fmt.Println(row)
	}
	fmt.Println()
	fmt.Println("legend: OK[text…] = HTTP200 + recognized text | FAIL<code> = non-200 | ERR = transport/key error")
}

func cellFor(p provider, file string) string {
	text, status, _, err := transcribe(p, file)
	switch {
	case err != nil && status == 0:
		return "ERR"
	case err != nil:
		return fmt.Sprintf("FAIL<%d>", status)
	default:
		return "OK[" + truncate(strings.ReplaceAll(text, "\n", " "), 12) + "]"
	}
}

// ---------------- probe (no API key needed) ----------------

func cmdProbe(args []string) {
	flag.NewFlagSet("probe", flag.ExitOnError).Parse(args)
	fmt.Println("probing STT endpoints with a FAKE key — verifies endpoint exists, NOT recognition.")
	fmt.Println("interpreting: 401/403 = endpoint EXISTS (needs auth) · 404 = NOT FOUND · 400 = exists (bad request)")
	fmt.Println()
	for _, name := range providerOrder {
		p := providers[name]
		status, snippet, err := probeProvider(p)
		verdict := interpretProbe(status, err)
		fmt.Printf("%-12s %-10s %-44s HTTP %-3d  %s\n", p.name, p.mode, endpointOf(p), status, verdict)
		if snippet != "" {
			fmt.Printf("             body: %s\n", snippet)
		}
	}
}

func probeProvider(p provider) (status int, snippet string, err error) {
	const fakeKey = "sttspike-probe-fake-key"
	url := endpointOf(p)
	var req *http.Request
	if p.mode == modeChat {
		bodyJSON, _ := json.Marshal(map[string]any{
			"model":    p.model,
			"messages": []map[string]any{},
		})
		req, err = http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyJSON))
		if err != nil {
			return 0, "", err
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		buf := &bytes.Buffer{}
		w := multipart.NewWriter(buf)
		_ = w.WriteField("model", p.model)
		_ = w.Close()
		req, err = http.NewRequest(http.MethodPost, url, buf)
		if err != nil {
			return 0, "", err
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
	}
	setAuth(req, p, fakeKey)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, herr := client.Do(req)
	if herr != nil {
		return 0, "", herr
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	status = resp.StatusCode
	snippet = truncate(strings.ReplaceAll(string(body), "\n", " "), 160)
	return status, snippet, nil
}

func interpretProbe(status int, err error) string {
	if err != nil {
		return "CONNECT ERROR — " + truncate(err.Error(), 80)
	}
	switch status {
	case 401, 403:
		return "endpoint EXISTS (needs auth) ✅"
	case 404:
		return "endpoint NOT FOUND ❌"
	case 400:
		return "endpoint EXISTS (bad/empty request)"
	default:
		return fmt.Sprintf("responds (status %d)", status)
	}
}

// ---------------- helpers ----------------

var errNotSet = fmt.Errorf("api key not set")

// extToMime maps a file extension to an audio MIME for the chat-mode data URL.
func extToMime(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav":
		return "audio/wav"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a", ".mp4":
		return "audio/mp4"
	case ".ogg":
		return "audio/ogg"
	case ".webm":
		return "audio/webm"
	case ".flac":
		return "audio/flac"
	default:
		return "application/octet-stream"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
