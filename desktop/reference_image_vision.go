package main

// reference_image_vision.go — Phase 2 of ppt-vision-enhancement-spec.
//
// Analyze a single reference image (typically a one-page PPT screenshot the user
// gave) with the VLM and write ~/.hiq/reference-style.json. ppt-auto reads
// this in Step 0 to draw a similar slide — text content verbatim, layout+density,
// font-size ratios, and color/style cues all come from the VLM's description.
//
// This is the "image → VLM → PPT" core link. It does NOT use image_understand
// (that tool is main-session-only by design); it calls the VLM directly from the
// desktop layer. The merged analyzer prompt (pptRefAnalyzerPrompt in
// classify_reference.go) carries the plain/visual verdict + the 4-section
// description (CONTENT/LAYOUT/FORMAT/DESIGN) in ONE call; the structured color
// JSON (referenceColorPrompt below) runs as a speculative parallel call.
//
// This file now holds the WRITE side: the three reference-style.json writers
// (visual / plain-material / colors-only) plus the standalone public entry
// point. PreparePPTReference orchestrates the calls and hands results here.
//
// Output: ~/.hiq/reference-style.json = {image, description, + structured
// color fields}. ppt-auto reads `description` for layout/content/font-ratio
// guidance; merge_vlm_style.py reads the structured color fields (background/
// accent_colors/is_dark/text_color) to recolor the deck to match the reference.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// referenceImageMaxBytes caps the image size sent to the VLM. VLM providers reject
// oversized request bodies, and a giant image is almost always a mistake (an
// uncropped full-page export, a raw photo). 10 MiB matches image_understand's cap.
const referenceImageMaxBytes = 10 * 1024 * 1024

// readImageForVLM reads an image file and enforces the VLM size cap. Shared by
// the gate call in PreparePPTReference (which also handles PDF page-1 renders)
// and the standalone AnalyzeReferenceImage, so both get the same stability
// guard — a too-large image returns an error instead of being base64-encoded
// into a request the VLM rejects.
func readImageForVLM(imgPath string) ([]byte, error) {
	b, err := os.ReadFile(imgPath)
	if err != nil {
		return nil, err
	}
	if len(b) > referenceImageMaxBytes {
		return nil, fmt.Errorf("image too large: %d bytes (max %d) — downscale or crop before using as reference", len(b), referenceImageMaxBytes)
	}
	return b, nil
}

// referenceColorPrompt asks the VLM for the reference image's color scheme as a
// structured JSON object. The shape matches templateStyleResult so
// parseVLMStyleResponse (shared with ppt_template_vision) parses it, and
// merge_vlm_style.py's _apply_vlm_style picks up background/accent_colors/
// is_dark/text_color directly — that's how the reference's palette actually
// reaches the generated PPT.
//
// Distinct from vlmColorPrompt (ppt_template_vision.go): that one assumes a
// template background photo and forces background_type="image"; a reference page
// may have a flat solid background, so here background_type is the VLM's call.
const referenceColorPrompt = `Analyze this image's color scheme. Respond with ONLY a JSON object (no markdown, no explanation), in this exact format:
{"background":"#RRGGBB","is_dark":false,"accent_colors":["#RRGGBB","#RRGGBB"],"text_color":"#RRGGBB","style_keywords":["keyword1","keyword2"],"background_type":"solid"}
Rules:
- background: the dominant background color (sample the largest area)
- is_dark: true if the background is dark (brightness < 50%), false if light
- accent_colors: 2-4 non-background colors that stand out (titles, accents, charts)
- text_color: the color text should be for readability on this background (#FFFFFF if dark, #1A1A1A if light)
- style_keywords: 2-3 style descriptors in Chinese (e.g. "商务简约","科技感","活泼")
- background_type: "solid" if a flat color background, "image" if a photo/textured background`

// referenceStyleResult is written to ~/.hiq/reference-style.json.
//
// Two kinds of info ride in here:
//   - Description: the VLM's 4-section markdown (content/layout/format/design) —
//     ppt-auto Step 3 reads this for layout/content/font-ratio guidance.
//   - Color fields (background/accent_colors/is_dark/text_color/...): structured
//     hex from the color call — merge_vlm_style.py reads THESE (not Description)
//     and merges them into template_config.json.colors, so the deck's palette
//     matches the reference. Without these hex fields the merge skips the
//     reference and the deck falls back to template/default colors.
type referenceStyleResult struct {
	Image          string          `json:"image"`                     // source image filename
	SourcePath     string          `json:"source_path,omitempty"`     // absolute path of the analyzed image — qa_compare.py reads this to find the reference for post-generation comparison
	Description    string          `json:"description,omitempty"`     // VLM 4-section markdown
	Regions        json.RawMessage `json:"regions,omitempty"`         // D-05: [{type,bbox,content},...] extracted from Description's fenced json block — rough 1280x720 boxes for mechanical placement
	Background     string          `json:"background,omitempty"`      // #RRGGBB — merge_vlm_style reads this
	IsDark         bool            `json:"is_dark,omitempty"`         // background brightness < 50%
	AccentColors   []string        `json:"accent_colors,omitempty"`   // top accent hexes
	TextColor      string          `json:"text_color,omitempty"`      // recommended text color
	StyleKeywords  []string        `json:"style_keywords,omitempty"`  // style descriptors
	BackgroundType string          `json:"background_type,omitempty"` // "solid" | "image"
}

// regionsFenceRe matches a fenced ```json [ {...}, ... ] ``` block. The array
// form (not an object) keeps the regex anchored; the "bbox" probe below makes
// sure we grabbed the regions block and not some other fenced array.
var regionsFenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(\\[\\s*\\{.*?\\}\\s*\\])\\s*```")

// extractRegionsJSON pulls the optional regions block out of a VISUAL
// description (D-05). The analyzer prompt asks the VLM to append it to the
// LAYOUT section as rough 1280x720 pixel boxes; Description keeps the prose
// for the LLM while this structured copy lets tooling consume positions
// mechanically. Returns nil when absent/invalid — callers treat that as
// "text-only description" (fully backward compatible).
func extractRegionsJSON(desc string) json.RawMessage {
	for _, m := range regionsFenceRe.FindAllStringSubmatch(desc, -1) {
		raw := []byte(m[1])
		if json.Valid(raw) && strings.Contains(m[1], `"bbox"`) {
			return json.RawMessage(raw)
		}
	}
	return nil
}

// AnalyzeReferenceImage is the standalone desktop entry point (kept for the
// frontend bridge contract): read one image, run the merged analyzer + color
// calls in parallel, write ~/.hiq/reference-style.json. The main PPT flow
// goes through PreparePPTReference, which shares the same writers.
func (a *App) AnalyzeReferenceImage(imgPath string) error {
	ctx := context.Background()
	if a.ctx != nil {
		ctx = a.ctx
	}
	pptVisionDebugLog("analyzeReferenceImage start: imgPath=%s", imgPath)
	imgBytes, err := readImageForVLM(imgPath)
	if err != nil {
		pptVisionDebugLog("analyzeReferenceImage readImageForVLM err: %v", err)
		return fmt.Errorf("read reference image: %w", err)
	}
	// PNG-lossless data URL — no JPEG re-encode, no downscale. Keeps text/edges/
	// solid-color blocks sharp for the VLM (same lesson as image_understand's
	// format-preservation fix: JPEG q85 smears exactly what OCR/layout/color
	// recognition need).
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imgBytes)

	pr, err := analyzeRefParallel(ctx, dataURL)
	if err != nil {
		pptVisionDebugLog("analyzeReferenceImage analyzeRefParallel FAILED: %v", err)
		return fmt.Errorf("reference image VLM: %w", err)
	}
	analysis := pr.analysis
	if analysis.IsVisual {
		return writeReferenceStyle(imgPath, analysis.Body, pr.colorResp)
	}
	// PLAIN from the standalone entry: same material treatment as the main flow.
	return writePlainReference(imgPath, analysis.Body)
}

// writeReferenceStyle writes the VISUAL branch: 4-section description +
// structured colors parsed from colorResp (best-effort — an empty/unparseable
// color response skips the color fields, and merge_vlm_style falls back to the
// template palette). srcPath is the analyzed file's path; its absolute form is
// stored so qa_compare.py can find the reference image later regardless of the
// skill's working directory.
func writeReferenceStyle(srcPath, desc, colorResp string) error {
	result := referenceStyleResult{Image: filepath.Base(srcPath), SourcePath: absPath(srcPath), Description: desc}
	result.Regions = extractRegionsJSON(desc)
	applyColorFields(&result, colorResp)
	return writeRefJSON(result)
}

// writePlainReference writes the PLAIN branch: the merged call's transcription
// as source material. There is no visual design worth replicating, but the words
// are still useful — and ppt-auto can't read image bytes directly (refs.go
// leaves only a text <image path> placeholder).
func writePlainReference(srcPath, transcription string) error {
	result := referenceStyleResult{
		Image:      filepath.Base(srcPath),
		SourcePath: absPath(srcPath),
		Description: "Plain-text reference (no visual design worth replicating). Transcribed content below — use as source material:\n\n" +
			transcription,
	}
	return writeRefJSON(result)
}

// writeReferenceColorsOnly writes the PDF deck branch: colors without a
// Description. The per-page layout/content guidance lives in
// ~/.hiq/pdf-pages/page-N.json; this file exists so merge_vlm_style.py
// picks up the deck-level palette (previously the PDF path had NO color
// extraction — decks referenced against a PDF ran on template/baseline colors).
func writeReferenceColorsOnly(srcPath, colorResp string) error {
	result := referenceStyleResult{Image: filepath.Base(srcPath), SourcePath: absPath(srcPath)}
	applyColorFields(&result, colorResp)
	return writeRefJSON(result)
}

// absPath best-efforts p to an absolute path (resolved against the current
// working directory, which on the pre-analysis path is the active workspace).
func absPath(p string) string {
	if ap, err := filepath.Abs(p); err == nil {
		return ap
	}
	return p
}

// applyColorFields parses a raw color-JSON response into result's structured
// fields. Best-effort: a nil parse (empty/failed call) leaves the fields zero.
func applyColorFields(result *referenceStyleResult, colorResp string) {
	cs := parseVLMStyleResponse(colorResp)
	if cs == nil {
		return
	}
	result.Background = cs.Background
	result.IsDark = cs.IsDark
	result.AccentColors = cs.AccentColors
	result.TextColor = cs.TextColor
	result.StyleKeywords = cs.StyleKeywords
	result.BackgroundType = cs.BackgroundType
}

// writeRefJSON marshals and writes ~/.hiq/reference-style.json.
func writeRefJSON(result referenceStyleResult) error {
	home, _ := os.UserHomeDir()
	outDir := filepath.Join(home, ".hiq")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("ensure ~/.hiq: %w", err)
	}
	data, _ := jsonMarshal(result)
	refPath := filepath.Join(outDir, "reference-style.json")
	writeErr := os.WriteFile(refPath, data, 0o644)
	pptVisionDebugLog("write reference-style.json: path=%s err=%v", refPath, writeErr)
	return writeErr
}
