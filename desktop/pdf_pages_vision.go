package main

// pdf_pages_vision.go — render a PDF's pages to images (via pdf_to_page_images.py)
// and ask the VLM to describe each page's CONTENT + LAYOUT + FORMAT + DESIGN, so
// ppt-auto can redraw a similar deck page-by-page.
//
// This is the "PDF (esp. scanned / image-style) → ppt-auto" bridge — the missing
// piece for "give a PDF, redraw each page as a slide". It implements exactly what
// the user described: split the PDF into one image per page, recognize each page
// with the VLM (not just text — also format, layout, design), and hand the
// per-page descriptions to ppt-auto to redraw.
//
// Pipeline:
//   pdf_to_page_images.py (fitz) renders each page → page-N.png
//   → each PNG sent to builtin.CallVLM with a 4-section prompt
//   → ~/.hiq/pdf-pages/page-N.json (one per page)
//   → ppt-auto reads these and redraws each page
//
// Design notes:
//   - VLM (not OCR) is the recognizer, so layout/format/design survive, not just
//     text. OCR already exists (ocr_pdf.py) but only yields flat text.
//   - PNG bytes are sent as-is (data:image/png, no JPEG re-encode, no downscale):
//     pdf_to_page_images.py already renders at scale=2, and PNG preserves the
//     sharp text/solid-color regions that JPEG smears — same lesson as the
//     image_understand format-preservation fix.
//   - Mirrors ppt_template_vision.go's CallVLM + write-JSON pattern; the only new
//     piece is per-page rendering via the python script.
//   - Per-page VLM failures are recorded in that page's JSON (error field) rather
//     than aborting the whole PDF — one bad page shouldn't sink the rest.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	skillassets "github.com/zzycxz/hiq/internal/assets"
	"github.com/zzycxz/hiq/internal/docconv"
	"github.com/zzycxz/hiq/internal/proc"
	runtimepkg "github.com/zzycxz/hiq/internal/runtime"
	"log/slog"
)

// pdfPageResult is one page's VLM description, written to page-{N}.json.
type pdfPageResult struct {
	Page       int    `json:"page"`                  // 1-based page number
	Image      string `json:"image"`                 // rendered PNG filename (page-N.png)
	TotalPages int    `json:"total_pages,omitempty"` // total in the source PDF
	Verdict    string `json:"verdict,omitempty"`     // per-page PLAIN/VISUAL — mixed PDFs (visual cover + text body) get the right treatment per page
	Desc       string `json:"description,omitempty"` // transcription (PLAIN) or 4-section markdown (VISUAL)
	Error      string `json:"error,omitempty"`       // per-page VLM/read error (other pages still process)
}

// AnalyzePDFPages is the desktop entry point: render each page of pdfPath to a
// PNG (via pdf_to_page_images.py) and ask the VLM to describe it, writing one
// JSON per page to ~/.hiq/pdf-pages/page-{N}.json. Returns the number of
// pages processed. Intended to run async (call in a goroutine, like
// analyzeTemplateStyleAsync); per-page failures land in the JSON, not as errors.
func (a *App) AnalyzePDFPages(pdfPath string) (int, error) {
	ctx := context.Background()
	if a.ctx != nil {
		ctx = a.ctx
	}
	n, _, err := analyzePDFPages(ctx, pdfPath, "")
	return n, err
}

// analyzePDFPages returns (analyzed, total, err): analyzed = pages actually
// described by the VLM, total = the source PDF's full page count. analyzed <
// total means the render cap truncated the run and the caller should surface
// that (ppt:reference-warning "pdf_truncated") instead of silently shipping a
// half-reference-driven deck.
//
// firstBody, when non-empty, is the gate call's page-1 analysis (verdict line
// already stripped) — page 1 skips its VLM call and reuses it. Pages run through
// a bounded concurrency pool (3): the calls are independent, and the old serial
// loop made a 6-page PDF block the submit path for 6 full round trips.
func analyzePDFPages(ctx context.Context, pdfPath string, firstBody string) (int, int, error) {
	// 1. Locate the renderer script — same search path as ocr_pdf.py (both sit at
	//    the project root and ship together).
	script := docconv.FindScript("pdf_to_page_images.py")
	if script == "" {
		return 0, 0, fmt.Errorf("pdf_to_page_images.py not found")
	}

	home, _ := os.UserHomeDir()
	outDir := filepath.Join(home, ".hiq", "pdf-pages")
	// Wipe before re-analyzing: a previous LONGER PDF's leftover page-K.json
	// (K > this PDF's total) would linger and later be consumed as if it
	// belonged to the current reference — cross-task contamination.
	if err := os.RemoveAll(outDir); err != nil {
		return 0, 0, fmt.Errorf("clear pdf-pages dir: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return 0, 0, fmt.Errorf("create pdf-pages dir: %w", err)
	}

	// 2. Render pages via the python script (fitz). ResolvePython prefers a bundled
	//    interpreter if present (uv); fall back to python3 / python. 10-min cap
	//    mirrors rag's PDF OCR path.
	pyCmd, pyPrefix, _ := runtimepkg.ResolvePython()
	if pyCmd == "" {
		pyCmd = "python3"
		if runtime.GOOS == "windows" {
			pyCmd = "python"
		}
	}
	// Cap pages at 6 — each page is one VLM call, so an unbounded PDF would block
	// SubmitToTab for minutes (the call is synchronous on the submit path). 6 covers
	// most decks; bigger PDFs degrade gracefully (only the first 6 pages analyzed,
	// the rest fall back to plain topic-driven generation).
	args := append(append([]string{}, pyPrefix...), script, pdfPath, outDir, "--scale", "2", "--max-pages", "6")
	rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(rctx, pyCmd, args...)
	proc.HideWindow(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, 0, fmt.Errorf("render pdf pages: %w: %s", err, stderr.String())
	}

	// 3. Parse the LAST stdout line as JSON {"pages": [...], "total_pages": N}.
	pages, total, err := parseRenderOutput(stdout.Bytes())
	if err != nil {
		return 0, 0, fmt.Errorf("parse render output: %w (stdout=%q)", err, strings.TrimSpace(stdout.String()))
	}

	// 4. Describe each page and write one JSON per page. Per-page isolation: a
	//    VLM error on page 3 still yields page-4.json. Pages run concurrently
	//    through a bounded pool — providers rate-limit, and an unbounded fan-out
	//    on a 6-page PDF would trip 429s.
	const pool = 3
	sem := make(chan struct{}, pool)
	var wg sync.WaitGroup
	for i, pagePath := range pages {
		pageNum := i + 1
		if pageNum == 1 && firstBody != "" {
			// Gate call already analyzed page 1 — reuse its body (the gate only
			// proceeds here when it judged the page VISUAL).
			result := pdfPageResult{Page: pageNum, Image: filepath.Base(pagePath), TotalPages: total, Verdict: "VISUAL", Desc: firstBody}
			data, _ := jsonMarshal(result)
			_ = os.WriteFile(filepath.Join(outDir, fmt.Sprintf("page-%d.json", pageNum)), data, 0o644)
			continue
		}
		wg.Add(1)
		go func(pageNum int, pagePath string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			result := pdfPageResult{Page: pageNum, Image: filepath.Base(pagePath), TotalPages: total}
			if imgBytes, rerr := os.ReadFile(pagePath); rerr != nil {
				result.Error = fmt.Sprintf("read png: %v", rerr)
			} else {
				// PNG bytes as-is — no JPEG re-encode, no downscale (scale=2 already
				// applied at render time; PNG keeps text/edges sharp for the VLM).
				dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imgBytes)
				if resp, verr := callVLMWithTimeout(ctx, dataURL, pptRefAnalyzerPrompt); verr == nil {
					a := parseRefAnalysis(resp)
					result.Verdict = a.Verdict
					result.Desc = a.Body
				} else {
					result.Error = verr.Error()
				}
			}
			data, _ := jsonMarshal(result)
			_ = os.WriteFile(filepath.Join(outDir, fmt.Sprintf("page-%d.json", pageNum)), data, 0o644)
		}(pageNum, pagePath)
	}
	wg.Wait()
	return len(pages), total, nil
}

// parseRenderOutput extracts the JSON object from the last non-empty stdout line.
// pdf_to_page_images.py prints progress to stderr and the result JSON as the final
// stdout line, so we scan from the end for a line starting with '{'.
func parseRenderOutput(stdout []byte) (pages []string, total int, err error) {
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var res struct {
			Pages []string `json:"pages"`
			Total int      `json:"total_pages"`
			Err   string   `json:"error"`
		}
		if jerr := json.Unmarshal([]byte(line), &res); jerr != nil {
			continue
		}
		if res.Err != "" {
			return nil, 0, fmt.Errorf("%s", res.Err)
		}
		return res.Pages, res.Total, nil
	}
	return nil, 0, fmt.Errorf("no JSON result line in render output")
}

// completePDFPagesInBackground finishes the per-page VLM analysis for pages the
// synchronous submit-path pass skipped (analyzePDFPages caps at 6 pages so
// SubmitToTab stays responsive). It runs the ppt-auto skill's own idempotent
// analyzer (analyze_pdf_pages.py) with --max 999 — there is no bash 2-minute
// budget on this path — so by the time the model dispatches to ppt-auto,
// ~/.hiq/pdf-pages/ usually holds EVERY page and the skill's in-task
// batching finds nothing left to do. Best-effort: errors only log.
func completePDFPagesInBackground(pdfPath string) {
	dir, err := skillassets.PPTAutoSkillDir()
	if err != nil || dir == "" {
		return
	}
	script := filepath.Join(dir, "scripts", "analyze_pdf_pages.py")
	if _, err := os.Stat(script); err != nil {
		return
	}
	pyCmd, pyPrefix, _ := runtimepkg.ResolvePython()
	if pyCmd == "" {
		pyCmd = "python3"
		if runtime.GOOS == "windows" {
			pyCmd = "python"
		}
	}
	args := append(append([]string{}, pyPrefix...), script, pdfPath, "--max", "999")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, pyCmd, args...)
	proc.HideWindow(cmd)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		slog.Warn("ppt reference: background completion of pdf page analysis failed",
			"pdf", filepath.Base(pdfPath), "err", err, "out", strings.TrimSpace(out.String()))
		return
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	tail := ""
	if len(lines) > 0 {
		tail = lines[len(lines)-1]
	}
	slog.Info("ppt reference: background pdf page analysis finished",
		"pdf", filepath.Base(pdfPath), "summary", tail)
}
