package main

// classify_reference.go — VLM gatekeeper + unified analyzer for the PPT vision
// pipeline.
//
// Per ppt-vision-enhancement-spec §六.五: decide whether a reference is PLAIN
// TEXT or VISUALLY DESIGNED by having the VLM look at it (never by file
// extension / size / page-count rules). Plain text → harvest the words as
// material (normal topic-driven ppt-auto); visually designed → the vision flow.
//
// Originally a separate lightweight A/B call preceded the expensive 4-section
// extraction; they are now MERGED into one prompt (pptRefAnalyzerPrompt): the
// verdict rides the first output line and the extraction follows in the same
// response. The submit path is synchronous, so serial round trips were the
// user-felt latency; with the speculative color call running concurrently
// (analyzeRefParallel) a visual reference now costs ≈ ONE VLM round trip before
// dispatch. The verdict stays VLM-judged, parseRefAnalysis just defaults
// ambiguous answers to the cheap PLAIN path.

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/docconv"
	"github.com/zzycxz/hiq/internal/proc"
	runtimepkg "github.com/zzycxz/hiq/internal/runtime"
	"github.com/zzycxz/hiq/internal/tool/builtin"
)

// pptVisionDebugLog appends a timestamped line to ~/.hiq/ppt-vision-debug.log
// so the image→PPT pipeline can be diagnosed end-to-end without relying on slog
// routing (which may go to stderr and be lost when the app is double-clicked).
// Best-effort: errors silently dropped. Remove/toggle off once the pipeline is stable.
func pptVisionDebugLog(format string, args ...any) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(home, ".hiq", "ppt-vision-debug.log")
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("15:04:05.000"), msg)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// pptVLMTimeout bounds each VLM call on the submit-blocking pre-analysis path.
// mayPreparePPTReference runs synchronously before SubmitDisplay, so a hung
// provider would freeze the send button indefinitely — and no deadline existed
// anywhere on the CallVLM chain (the existing WithTimeout calls cover only the
// python render subprocesses). 90s is generous for a vision call while still
// failing faster than a user gives up and retypes.
const pptVLMTimeout = 90 * time.Second

// callVLMWithTimeout is builtin.CallVLM with a deadline, shared by every VLM
// call on the PPT pre-analysis path (classify / describe / colors / OCR /
// per-page).
func callVLMWithTimeout(ctx context.Context, dataURL, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, pptVLMTimeout)
	defer cancel()
	return builtin.CallVLM(ctx, dataURL, prompt)
}

// classifyVisualPrompt asks the VLM for a single A/B verdict on whether the
// reference is plain text or visually designed. Kept short on purpose — this is
// the cheap gate before the expensive 4-section extraction.
const classifyVisualPrompt = `Look at this image. Decide which category it falls into:

(A) PLAIN TEXT — only words and numbers, with NO visual styling, layout, or design. Examples: a plain text note, a body-text-only document page, a code listing, a raw data dump.
(B) VISUALLY DESIGNED — has visual design elements such as: styled/large titles, a color scheme, multi-column layout, charts/diagrams, cards/boxes, icons, decorative shapes, or a slide-like layout.

Answer with ONLY "A" or "B" on the FIRST line, then ONE short sentence explaining why (e.g. "B - has a large blue title and a 2-column card layout"). Do not output anything else.`

// pptRefAnalyzerPrompt merges the plain/visual gate INTO the extraction call —
// one call per image instead of a separate cheap classify followed by the
// expensive 4-section extraction. The submit path is synchronous and the calls
// were serial, so 3 round trips (classify → describe → colors) became the
// user-felt latency; merging the gate costs nothing (the verdict is one extra
// output line) and the colors call runs concurrently (analyzeRefParallel).
//
// Cognitive-mode discipline (anti-hallucination): one call = ONE response mode.
// The verdict + transcription/4-sections share the "read this image" mode; the
// strict-JSON colors call stays separate because precision formats degrade when
// mixed with long prose. The (illegible) escape valve matters most: a VLM forced
// to produce text it cannot actually read will confabulate — letting it say
// "cannot read" is the cheapest hallucination reduction there is.
const pptRefAnalyzerPrompt = `You are looking at ONE page (a reference for recreating it as an editable PowerPoint slide).

FIRST LINE — the verdict, exactly one word:
PLAIN — only words and numbers, NO visual styling, layout, or design (a plain text note, a body-text document page, a code listing, a raw data dump).
VISUAL — has visual design: styled/large titles, a color scheme, multi-column layout, charts/diagrams, cards/boxes, icons, decorative shapes, or a slide-like layout.

If PLAIN: after the first line, transcribe ALL visible text verbatim (preserve line breaks; top-to-bottom, left-to-right reading order). Output ONLY the transcription — no headings, no commentary, no markdown formatting. If the image contains no text at all, output exactly: (no text)

If VISUAL: after the first line, describe the page in exactly 4 markdown sections:
## 1 CONTENT
Transcribe ALL text verbatim — title, headings, body paragraphs, every bullet point, captions, data labels, table cells. Do NOT summarize. Mark text you cannot read clearly as (illegible) — never guess.
## 2 LAYOUT
The spatial arrangement: where the title sits (top-center / top-left / etc.), the body region, single vs multi-column, paragraphs vs bullet list, any table (give its rows and columns), image regions (position and rough size share of the page).
End this section with one fenced json block of rough region boxes — pixel estimates on a 1280x720 canvas, 4-8 regions, rough is fine:
` + "```json" + `
[{"type":"text|card|image|table|chart","bbox":[x,y,w,h],"content":"key text or short label"}]
` + "```" + `
## 3 FORMAT
Relative font sizing (which text is largest, what looks like body size relative to it), weight cues (bold title?), bullet style, text alignment per block (left / center / right).
## 4 DESIGN
Background (light / dark, hue name), accent colors (hue names), overall style (formal / casual / minimal / illustrated / corporate), any logo or decorative elements.

Text (CONTENT) must be exact — use (illegible) for unreadable text instead of guessing. Positions and colors (LAYOUT/FORMAT/DESIGN) can be approximate — you cannot see exact pixels. Output ONLY the verdict line followed by the required content, no preamble, no closing remarks.`

// refAnalysis is the parsed result of one pptRefAnalyzerPrompt call.
type refAnalysis struct {
	IsVisual bool   // true = VISUAL (4-section description in Body)
	Verdict  string // "VISUAL" / "PLAIN" / "PLAIN?" (ambiguous → conservative cheap path)
	Reason   string // short explanation when the model gave one ("VISUAL - has card layout" → "has card layout")
	Body     string // transcription (PLAIN) or 4-section markdown (VISUAL), verdict line stripped
}

// parseRefAnalysis pulls the verdict off the first non-empty line and returns
// the remainder as Body. Tolerant of a code fence, a leading '#', "VERDICT:"
// prefixes, and A/B habit answers; ambiguous output defaults to PLAIN — the
// cheap path — mirroring parseClassification's conservatism (never burn the
// vision pipeline on an unclear answer).
func parseRefAnalysis(resp string) refAnalysis {
	var a refAnalysis
	lines := strings.Split(strings.TrimSpace(resp), "\n")
	verdictIdx := -1
	var firstClean string
	for i, raw := range lines {
		clean := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "`"))
		clean = strings.TrimSpace(strings.TrimPrefix(clean, "#"))
		if clean == "" {
			continue
		}
		verdictIdx = i
		firstClean = clean
		break
	}
	if verdictIdx < 0 {
		a.Verdict = "PLAIN?"
		a.Body = strings.TrimSpace(resp)
		return a
	}
	words := strings.Fields(strings.ToUpper(firstClean))
	w := ""
	if len(words) > 0 {
		w = strings.TrimSuffix(strings.TrimSuffix(words[0], ":"), ".")
		w = strings.Trim(w, "`\"'") // tolerate a half-stripped code fence around the verdict word
	}
	if (w == "VERDICT" || w == "ANSWER") && len(words) > 1 {
		w = strings.TrimSuffix(strings.TrimSuffix(words[1], ":"), ".")
		w = strings.Trim(w, "`\"'")
	}
	switch w {
	case "VISUAL", "B":
		a.IsVisual, a.Verdict = true, "VISUAL"
	case "PLAIN", "A":
		a.IsVisual, a.Verdict = false, "PLAIN"
	default:
		// Ambiguous: no verdict line to strip — keep the whole response as Body
		// (the model may have ignored the format but still produced content).
		a.IsVisual, a.Verdict = false, "PLAIN?"
		a.Body = strings.TrimSpace(resp)
		return a
	}
	// Optional " - reason" tail on the verdict line.
	if idx := strings.Index(firstClean, "-"); idx >= 0 {
		a.Reason = firstClean[idx+1:]
		if len(a.Reason) > 120 {
			a.Reason = a.Reason[:120]
		}
	}
	a.Body = strings.TrimSpace(strings.Join(lines[verdictIdx+1:], "\n"))
	return a
}

// refParallelResult is the outcome of analyzeRefParallel.
type refParallelResult struct {
	analysis  refAnalysis
	colorResp string // raw color-JSON response; "" when the (best-effort) call failed
}

// analyzeRefParallel runs the merged verdict+extraction call and the SPECULATIVE
// structured-color call concurrently on the same image — wall clock ≈ one VLM
// round trip instead of two serial ones. The color call is speculative because
// its result is only consumed when the verdict is VISUAL; on a PLAIN verdict it
// is discarded (one cheap wasted call on plain images — accepted: PPT
// references are overwhelmingly visual). The merged call is load-bearing: its
// failure fails the whole thing, a color failure just degrades colors to the
// template fallback.
func analyzeRefParallel(ctx context.Context, dataURL string) (*refParallelResult, error) {
	var (
		wg           sync.WaitGroup
		analysisResp string
		colorResp    string
		analysisErr  error
		colorErr     error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		analysisResp, analysisErr = callVLMWithTimeout(ctx, dataURL, pptRefAnalyzerPrompt)
	}()
	go func() {
		defer wg.Done()
		colorResp, colorErr = callVLMWithTimeout(ctx, dataURL, referenceColorPrompt)
	}()
	wg.Wait()
	if analysisErr != nil {
		return nil, analysisErr
	}
	res := &refParallelResult{analysis: parseRefAnalysis(analysisResp)}
	if colorErr == nil {
		res.colorResp = colorResp
	}
	pptVisionDebugLog("analyzeRefParallel: verdict=%s reason=%q bodyLen=%d colorErr=%v",
		res.analysis.Verdict, res.analysis.Reason, len(res.analysis.Body), colorErr)
	return res, nil
}

// ReferenceClassification is the result of the VLM gatekeeper check.
type ReferenceClassification struct {
	IsVisual bool   `json:"is_visual"` // true = B (run vision flow), false = A (plain text → extract words as material)
	Verdict  string `json:"verdict"`   // "A" or "B"
	Reason   string `json:"reason"`    // the VLM's one-line explanation
}

// ClassifyReferenceVisual asks the VLM whether filePath (image or PDF) is plain
// text or visually designed. For a PDF, page 1 is rendered to an image first via
// pdf_to_page_images.py (--max-pages 1); for an image, it's read as-is. The image
// goes to the VLM PNG-lossless. This is the gate that decides whether to run the
// full vision pipeline or just harvest text as material.
func (a *App) ClassifyReferenceVisual(filePath string) (*ReferenceClassification, error) {
	ctx := context.Background()
	if a.ctx != nil {
		ctx = a.ctx
	}
	pptVisionDebugLog("ClassifyReferenceVisual start: filePath=%s", filePath)
	imgPath, cleanup, err := singleImageForVLM(ctx, filePath)
	if err != nil {
		pptVisionDebugLog("ClassifyReferenceVisual singleImageForVLM err: %v", err)
		return nil, err
	}
	defer cleanup()

	imgBytes, err := os.ReadFile(imgPath)
	if err != nil {
		pptVisionDebugLog("ClassifyReferenceVisual read img err: %v (imgPath=%s)", err, imgPath)
		return nil, fmt.Errorf("read image for classify: %w", err)
	}
	pptVisionDebugLog("ClassifyReferenceVisual img ready: imgPath=%s size=%d", imgPath, len(imgBytes))
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imgBytes)

	resp, err := callVLMWithTimeout(ctx, dataURL, classifyVisualPrompt)
	if err != nil {
		pptVisionDebugLog("ClassifyReferenceVisual CallVLM FAILED: %v", err)
		return nil, fmt.Errorf("classify VLM: %w", err)
	}
	cls := parseClassification(resp)
	pptVisionDebugLog("ClassifyReferenceVisual OK: verdict=%s isVisual=%v reason=%q", cls.Verdict, cls.IsVisual, cls.Reason)
	return cls, nil
}

// singleImageForVLM returns a path to a single image the VLM can look at, plus a
// cleanup func (nil-op for plain images). For PDFs it renders page 1 via
// pdf_to_page_images.py; for images it returns the file itself.
func singleImageForVLM(ctx context.Context, filePath string) (imgPath string, cleanup func(), err error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext != ".pdf" {
		return filePath, func() {}, nil
	}

	// PDF: render page 1 only (cheap — we just need a glance to classify).
	script := docconv.FindScript("pdf_to_page_images.py")
	if script == "" {
		return "", nil, fmt.Errorf("pdf_to_page_images.py not found")
	}
	tmp, err := os.MkdirTemp("", "classify-pdf-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(tmp) }

	pyCmd, pyPrefix, _ := runtimepkg.ResolvePython()
	if pyCmd == "" {
		pyCmd = "python3"
		if runtime.GOOS == "windows" {
			pyCmd = "python"
		}
	}
	args := append(append([]string{}, pyPrefix...), script, filePath, tmp, "--max-pages", "1", "--scale", "2")
	rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(rctx, pyCmd, args...)
	proc.HideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("render pdf page 1: %w: %s", err, stderr.String())
	}
	page1 := filepath.Join(tmp, "page-1.png")
	if _, statErr := os.Stat(page1); statErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("page-1.png not produced after render")
	}
	return page1, cleanup, nil
}

// PrepareResult is what PreparePPTReference returns to its caller (the frontend /
// cowork PPT request handler): whether the reference is visual, and if the vision
// flow ran, whether it hit any per-step error (best-effort — a VLM hiccup on one
// step shouldn't sink the whole request).
type PrepareResult struct {
	IsVisual       bool   `json:"is_visual"`                  // true = vision flow ran, false = plain text
	Verdict        string `json:"verdict"`                    // "A" (plain) or "B" (visual)
	Reason         string `json:"reason"`                     // VLM's one-line classify reason
	PDFPages       int    `json:"pdf_pages,omitempty"`        // pages processed when input was a PDF
	PDFTotalPages  int    `json:"pdf_total_pages,omitempty"`  // total pages of the source PDF (analyzed < total ⇒ truncated)
	VisionError    string `json:"vision_error,omitempty"`     // non-fatal error from the vision step, if any
	NeedsVLMConfig bool   `json:"needs_vlm_config,omitempty"` // VLM not configured — caller should warn the user to set one in Settings
}

// PreparePPTReference is the unified entry point the PPT request handler calls
// when the user gives a reference file (image or PDF) with "make a PPT" intent.
// ONE merged VLM call (verdict + extraction, pptRefAnalyzerPrompt) plus a
// speculative structured-color call run concurrently on the first page, then:
//
//   - PLAIN → the merged call already transcribed the words (its PLAIN branch IS
//     the OCR). For an image they land in reference-style.json as source
//     material; a plain PDF falls through (refs.go extracts its text on the
//     normal path). Returns IsVisual=false → caller runs the topic-driven flow.
//
//   - VISUAL → the vision flow, reusing what the gate call already produced:
//     image → writeReferenceStyle (4-section description + speculative colors →
//     reference-style.json); PDF → analyzePDFPages reuses page 1's body and
//     describes pages 2..N through a bounded concurrency pool, plus deck-level
//     colors from the speculative call (the PDF path previously had NO color
//     extraction at all).
//
// Vision-step errors are recorded in VisionError, not returned as a fatal error —
// the caller can still proceed (degrade to topic-driven) if the vision step hiccups.
func (a *App) PreparePPTReference(filePath string) (*PrepareResult, error) {
	ctx := context.Background()
	if a.ctx != nil {
		ctx = a.ctx
	}
	// One image for the gate call: the file itself, or PDF page 1 rendered.
	imgPath, cleanup, err := singleImageForVLM(ctx, filePath)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	imgBytes, err := readImageForVLM(imgPath)
	if err != nil {
		return nil, fmt.Errorf("read reference image: %w", err)
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imgBytes)

	start := time.Now()
	pr, err := analyzeRefParallel(ctx, dataURL)
	if err != nil {
		// VLM 未配置是可识别的配置错误：返回带 NeedsVLMConfig 的 result，让调用方
		// (SubmitToTab) 告警用户去设置 VLM，而不是当普通错误静默吞掉。其他错误正常返回。
		if strings.Contains(err.Error(), "no VLM model configured") {
			return &PrepareResult{NeedsVLMConfig: true, VisionError: err.Error()}, nil
		}
		return nil, err
	}
	analysis := pr.analysis
	pptVisionDebugLog("PreparePPTReference: verdict=%s (%.1fs) file=%s", analysis.Verdict, time.Since(start).Seconds(), filePath)
	result := &PrepareResult{
		IsVisual: analysis.IsVisual,
		Verdict:  analysis.Verdict,
		Reason:   analysis.Reason,
	}

	// Route by verdict + file type.
	ext := strings.ToLower(filepath.Ext(filePath))
	switch {
	case analysis.IsVisual && ext == ".pdf":
		// Visually designed PDF: deck-level colors (speculative page-1 call) go
		// to reference-style.json for merge_vlm_style; per-page guidance is
		// page-N.json, with page 1 reusing the gate call's body.
		if werr := writeReferenceColorsOnly(filePath, pr.colorResp); werr != nil {
			result.VisionError = werr.Error()
		}
		n, total, verr := analyzePDFPages(ctx, filePath, analysis.Body)
		result.PDFPages = n
		result.PDFTotalPages = total
		if verr != nil {
			result.VisionError = verr.Error()
		}
		// The synchronous pass caps at 6 pages (submit-path latency); finish the
		// REST in the background via the skill's idempotent analyzer so ppt-auto
		// usually finds every page-N.json already there. Never blocks submit.
		if n < total {
			go completePDFPagesInBackground(filePath)
		}
	case analysis.IsVisual:
		// Visually designed image → 4-section description + structured colors,
		// both already in hand from the parallel calls.
		if werr := writeReferenceStyle(filePath, analysis.Body, pr.colorResp); werr != nil {
			result.VisionError = werr.Error()
		}
	case ext != ".pdf":
		// PLAIN-TEXT IMAGE → the merged call's PLAIN branch already transcribed
		// the words; store them as source material (ppt-auto can't read image
		// bytes; refs.go only leaves a text placeholder for images). No visual
		// flow — nothing to replicate design-wise.
		if werr := writePlainReference(filePath, analysis.Body); werr != nil {
			result.VisionError = werr.Error()
		}
		// Plain-text PDF falls through with no action: refs.go already extracts
		// its text when the @PDF reference is resolved, so ppt-auto receives it.
	}
	return result, nil
}

// parseClassification pulls the A/B verdict out of the VLM response. Tolerant of
// leading whitespace / a leading code fence; defaults to "plain text" (A, the
// cheaper path) if it can't find a clear A/B so we never accidentally run the
// expensive vision flow on an ambiguous answer.
func parseClassification(resp string) *ReferenceClassification {
	for _, raw := range strings.Split(strings.TrimSpace(resp), "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "`"))
		if line == "" {
			continue
		}
		first := strings.ToUpper(line[:1])
		if first == "A" || first == "B" {
			// Rest of this line (or the next) is the reason.
			reason := strings.TrimSpace(strings.TrimPrefix(line, first))
			reason = strings.TrimLeft(reason, " -—:.)")
			if reason == "" {
				reason = strings.TrimSpace(resp)
			}
			return &ReferenceClassification{
				IsVisual: first == "B",
				Verdict:  first,
				Reason:   reason,
			}
		}
	}
	// Ambiguous — default to plain text (A) so we don't burn the vision pipeline
	// on an uncertain answer.
	return &ReferenceClassification{IsVisual: false, Verdict: "A?", Reason: strings.TrimSpace(resp)}
}

// pptIntentKeywords marks user input as PPT-making intent. Kept broad on purpose:
// the VLM classify step (PreparePPTReference → ClassifyReferenceVisual) is the
// real gatekeeper that decides vision-flow vs plain-text, so a loose keyword net
// here just avoids burning a VLM call on every screenshot a user pastes for
// non-PPT reasons.
var pptIntentKeywords = []string{"ppt", "幻灯", "演示文稿", "slides", "slide"}

func hasPPTIntent(lowered string) bool {
	for _, kw := range pptIntentKeywords {
		if strings.Contains(lowered, kw) {
			return true
		}
	}
	return false
}

// referenceAttachmentExts are attachment extensions that can be a visual reference
// for PPT generation (images + PDF).
var referenceAttachmentExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true,
	".pdf": true,
}

// pptReferenceAttachment scans input for an @.hiq/attachments/<file> token
// whose extension is an image/PDF, AND whose text shows PPT intent. Returns the
// attachment path (relative, without the leading @) and true when both match —
// the signal that PreparePPTReference should run before the message reaches the
// model, so reference-style.json / page-N.json is ready when ppt-auto reads it.
func pptReferenceAttachment(input string) (string, bool) {
	if !hasPPTIntent(strings.ToLower(input)) {
		return "", false
	}
	const prefix = "@.hiq/attachments/"
	search := input
	for {
		idx := strings.Index(search, prefix)
		if idx < 0 {
			return "", false
		}
		rest := search[idx+len(prefix):]
		end := len(rest)
		for i, r := range rest {
			if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '@' || r == ',' || r == '，' {
				end = i
				break
			}
		}
		name := rest[:end]
		search = rest[end:] // advance for the next iteration
		if name == "" {
			continue
		}
		if referenceAttachmentExts[strings.ToLower(filepath.Ext(name))] {
			return ".hiq/attachments/" + name, true
		}
	}
}

// localPathReference scans input for an ABSOLUTE local file path with an
// image/PDF extension — the "user pasted a path instead of uploading the file"
// form (e.g. 把 C:\Users\me\Desktop\shot.png 转成PPT). pptReferenceAttachment only
// recognizes @.hiq/attachments tokens, so without this the message would
// bypass the pre-analysis gate entirely and ppt-auto would run with no
// reference-style.json at all (the model improvising via image_understand).
// Returns the first path that EXISTS on disk (symlinks rejected, mirroring
// image_understand's guards). A path-shaped token with a whitelisted extension
// that does NOT exist is returned as `missing` so the caller can warn the user
// instead of silently dropping the reference. URLs are excluded ("://" tokens).
func localPathReference(input string) (found string, missing string, ok bool) {
	for _, raw := range strings.FieldsFunc(input, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', ',', '，', '。', '、', ';', '；',
			'"', '\'', '“', '”', '‘', '’', '「', '」', '《', '》':
			return true
		}
		return false
	}) {
		token := strings.Trim(raw, "\"'“”‘’「」《》()（）")
		if token == "" || strings.Contains(token, "://") {
			continue // URL, not a local path
		}
		if !isAbsoluteishPath(token) {
			continue
		}
		if !referenceAttachmentExts[strings.ToLower(filepath.Ext(token))] {
			continue
		}
		info, err := os.Lstat(token)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
			if missing == "" {
				missing = token
			}
			continue
		}
		return token, "", true
	}
	return "", missing, false
}

// isAbsoluteishPath reports whether token looks like an absolute local path: a
// Windows drive-rooted path (C:\ or C:/) or a POSIX root path. Relative paths
// (./x.png, x.png) are deliberately NOT matched — they resolve against an
// ambiguous base and would false-positive on ordinary words ending in .png.
func isAbsoluteishPath(token string) bool {
	if filepath.IsAbs(token) {
		return true
	}
	// Drive path with either separator. Checked manually because filepath.IsAbs
	// only recognizes the host's native form (backslash on Windows), and the
	// slashed form (C:/x) shows up in pasted paths.
	if len(token) >= 3 && token[1] == ':' {
		c := token[0]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			return token[2] == '\\' || token[2] == '/'
		}
	}
	return false
}

// clearStaleReferenceFiles enforces the invariant "reference-style.json /
// pdf-pages exist ⟺ the CURRENT PPT task provided a reference". When a message
// shows PPT intent but carries no reference, leftovers from a previous task
// would silently hijack this run — worst case pdf-pages/page-N.json overrides
// the deck's page count (SKILL.md Step 3 takes page-N count as authoritative).
// Colors already merged into template_config.json survive the clear, so an
// iteration request ("把第3页改一下") only loses the 4-section description — the
// accepted trade: cross-task contamination is a real bug, iteration without
// re-attaching is rarer and recoverable (re-attach the reference).
func clearStaleReferenceFiles() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	clearStaleReferenceFilesIn(home)
}

// clearStaleReferenceFilesIn is the testable core of clearStaleReferenceFiles.
func clearStaleReferenceFilesIn(home string) {
	if home == "" {
		return
	}
	fp := filepath.Join(home, ".hiq", "reference-style.json")
	if err := os.Remove(fp); err == nil {
		pptVisionDebugLog("cleared stale reference file: %s", fp)
	}
	pd := filepath.Join(home, ".hiq", "pdf-pages")
	if err := os.RemoveAll(pd); err == nil {
		pptVisionDebugLog("cleared stale pdf-pages dir: %s", pd)
	}
}
