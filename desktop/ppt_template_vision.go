package main

// ppt_template_vision.go — async vision-based color/style extraction for PPT
// templates.
//
// When the user picks a template (PickPPTTemplate), we kick off a goroutine
// that:
//  1. Opens the .pptx zip, finds the first full-screen blipFill background
//     image (the same geometry check as extract_template_colors.py).
//  2. Decodes + downscales it to a JPEG data URL.
//  3. Calls the configured vision model (builtin.CallVLM) with a prompt asking
//     for a JSON description of the template's colors/style.
//  4. Writes the result to ~/.hiq/ppt-template-style.json.
//
// The ppt-auto skill reads this file (Step 0) and uses it as the highest-
// priority color source — it's more accurate than the XML/PIL heuristics for
// image-background templates. If anything fails (no full-screen image, no VLM
// configured, API error), we degrade silently — extract_template_colors.py
// remains the fallback.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zzycxz/hiq/internal/tool/builtin"

	"golang.org/x/image/draw"
)

// vlmMaxDim caps the long edge of the image sent to the VLM (most models are
// trained at ~1024-1920px; larger just adds latency).
const pptVlmMaxDim = 1280

// ooxml namespaces used when parsing the slide layout XML.
var pptNS = map[string]string{
	"a": "http://schemas.openxmlformats.org/drawingml/2006/main",
	"p": "http://schemas.openxmlformats.org/presentationml/2006/main",
	"r": "http://schemas.openxmlformats.org/officeDocument/2006/relationships",
}

// full-screen background thresholds (EMU). 16:9 = 12192000×6858000; 4:3 width
// = 9144000. Tolerance ~0.4in.
const (
	fullScreenW16x9 = 12192000
	fullScreenW4x3  = 9144000
	fullScreenH     = 6858000
	fullScreenTol   = 400000
)

// templateStyleResult is the JSON written to ppt-template-style.json and read
// by the skill's Step 0.
type templateStyleResult struct {
	Background     string   `json:"background"`      // #RRGGBB
	IsDark         bool     `json:"is_dark"`         // background brightness < 128
	AccentColors   []string `json:"accent_colors"`   // top accent hexes, [#RRGGBB, ...]
	TextColor      string   `json:"text_color"`      // recommended text color #RRGGBB
	StyleKeywords  []string `json:"style_keywords"`  // e.g. ["商务","简约","科技"]
	BackgroundType string   `json:"background_type"` // "image" | "solid"
	Source         string   `json:"source"`          // "vision-vlm" | "vision-pil-fallback"
}

// analyzeTemplateStyleAsync is the goroutine entry point. It runs the full
// pipeline (zip read → image find → encode → VLM → write JSON) and swallows
// all errors — this is a best-effort enhancement, never a blocking failure.
func (a *App) analyzeTemplateStyleAsync(templatePath string) {
	ctx := context.Background()
	if a.ctx != nil {
		ctx = a.ctx
	}

	result, err := extractTemplateStyle(ctx, templatePath)
	if err != nil || result == nil {
		// Silent degradation — extract_template_colors.py is the fallback.
		return
	}

	// Write to ~/.hiq/ppt-template-style.json
	home, _ := os.UserHomeDir()
	outPath := filepath.Join(home, ".hiq", "ppt-template-style.json")
	data, _ := jsonMarshal(result)
	_ = os.WriteFile(outPath, data, 0o644)
}

// extractTemplateStyle finds the full-screen background image in the pptx,
// sends it to the VLM, and parses the JSON response.
func extractTemplateStyle(ctx context.Context, pptxPath string) (*templateStyleResult, error) {
	imgBytes, imgFmt, err := findFullScreenBackgroundImage(pptxPath)
	if err != nil {
		return nil, err
	}

	dataURL, err := encodeImageForVLM(imgBytes, imgFmt)
	if err != nil {
		return nil, err
	}

	resp, err := builtin.CallVLM(ctx, dataURL, vlmColorPrompt)
	if err != nil {
		return nil, err
	}

	return parseVLMStyleResponse(resp), nil
}

// vlmColorPrompt asks the vision model for structured color/style info.
const vlmColorPrompt = `Look at this PPT template's background image. Analyze its color scheme and style. Respond with ONLY a JSON object (no markdown, no explanation), in this exact format:
{"background":"#RRGGBB","is_dark":false,"accent_colors":["#RRGGBB","#RRGGBB","#RRGGBB"],"text_color":"#RRGGBB","style_keywords":["keyword1","keyword2"],"background_type":"image"}
Rules:
- background: the dominant background color (sample the largest area)
- is_dark: true if the background is dark (brightness < 50%), false if light
- accent_colors: 2-4 non-background colors that stand out (logos, accents, decorations)
- text_color: the color text should be for readability on this background (#FFFFFF if dark, #1A1A1A if light)
- style_keywords: 2-3 style descriptors in Chinese (e.g. "商务简约","科技感","活泼","中国风")
- background_type: always "image" since this is a photo/graphic background`

// findFullScreenBackgroundImage scans the pptx zip for a full-screen blipFill
// image in any slideLayout or slideMaster, returns its bytes + format.
func findFullScreenBackgroundImage(pptxPath string) ([]byte, string, error) {
	zr, err := zip.OpenReader(pptxPath)
	if err != nil {
		return nil, "", fmt.Errorf("open pptx zip: %w", err)
	}
	defer zr.Close()

	// Build a map of rels: part path -> {rid -> target}
	// We scan all slideLayouts and slideMasters for full-screen pics.
	for _, partFile := range zr.File {
		partName := partFile.Name
		if !strings.HasPrefix(partName, "ppt/slideLayouts/slideLayout") &&
			!strings.HasPrefix(partName, "ppt/slideMasters/slideMaster") {
			continue
		}
		if !strings.HasSuffix(partName, ".xml") {
			continue
		}

		xmlData, err := readZipFile(partFile)
		if err != nil {
			continue
		}

		rid := findFullScreenBlipRID(xmlData)
		if rid == "" {
			continue
		}

		// Find the rels file for this part
		relsPath := buildRelsPath(partName)
		imgPath, err := resolveImageFromRels(zr, relsPath, rid)
		if err != nil || imgPath == "" {
			continue
		}

		// Read the image bytes
		for _, f := range zr.File {
			if f.Name == imgPath {
				imgBytes, err := readZipFile(f)
				if err != nil {
					return nil, "", err
				}
				fmt := "png"
				if strings.HasSuffix(strings.ToLower(imgPath), ".jpg") || strings.HasSuffix(strings.ToLower(imgPath), ".jpeg") {
					fmt = "jpeg"
				}
				return imgBytes, fmt, nil
			}
		}
	}

	return nil, "", fmt.Errorf("no full-screen background image found")
}

// findFullScreenBlipRID parses the layout/master XML and returns the rId of
// the first full-screen blipFill pic element, or "" if none found.
func findFullScreenBlipRID(xmlData []byte) string {
	// We use a custom XML walk because encoding/xml doesn't handle the deeply
	// nested + namespaced OOXML well with a single struct. A targeted search
	// for <p:pic> blocks with xfrm + blip is more robust here.
	//
	// Strategy: find all <p:pic>...</p:pic> substrings, within each look for
	// <a:off x= y=>, <a:ext cx= cy=>, and <a:blip r:embed=>. This is a pragmatic
	// text scan — OOXML is too irregular for strict unmarshalling.
	text := string(xmlData)
	rid := scanForFullScreenBlip(text)
	return rid
}

// scanForFullScreenBlip does a targeted text scan for full-screen pics.
func scanForFullScreenBlip(text string) string {
	// Split on <p:pic to get individual pic blocks. (Also handles <p:pic/>.)
	for _, rest := range splitXMLBlock(text, "<p:pic") {
		if rest == "" {
			continue
		}
		// Truncate to the end of this pic element (heuristic: next </p:pic> or <p:pic)
		picBlock := truncateToClosing(rest, "p:pic")
		if picBlock == "" {
			picBlock = rest
		}

		// Scope geometry search to <a:xfrm>...</a:xfrm> to avoid matching
		// <a:ext> inside <a:extLst> (which is a different element that also
		// starts with "<a:ext" but has uri= instead of cx=/cy= attributes).
		xfrmBlock := extractTagContent(picBlock, "a:xfrm")
		if xfrmBlock == "" {
			continue // no xfrm = can't determine geometry
		}

		off := extractAttrInt(xfrmBlock, "<a:off", "x")
		offY := extractAttrInt(xfrmBlock, "<a:off", "y")
		cx := extractAttrInt(xfrmBlock, "<a:ext", "cx")
		cy := extractAttrInt(xfrmBlock, "<a:ext", "cy")

		if !isFullScreenEMU(off, offY, cx, cy) {
			continue
		}

		// Found a full-screen pic — extract its blip rId.
		rid := extractBlipRID(picBlock)
		if rid != "" {
			return rid
		}
	}
	return ""
}

// extractTagContent returns the text between the first <tag> and </tag>,
// or "" if not found. Handles both <tag>...</tag> and <tag/> (returns "" for
// self-closing since there's no content).
func extractTagContent(text, tag string) string {
	openTag := "<" + tag
	closeTag := "</" + tag + ">"
	idx := strings.Index(text, openTag)
	if idx < 0 {
		return ""
	}
	// Skip past the opening tag (find its closing >)
	rest := text[idx:]
	gt := strings.Index(rest, ">")
	if gt < 0 {
		return ""
	}
	// Check for self-closing <tag .../>
	if gt > 0 && rest[gt-1] == '/' {
		return ""
	}
	contentStart := rest[gt+1:]
	end := strings.Index(contentStart, closeTag)
	if end < 0 {
		return ""
	}
	return contentStart[:end]
}

// splitXMLBlock splits text on a tag-start, returning the remainder after each
// occurrence (including the tag itself stripped).
func splitXMLBlock(text, tagStart string) []string {
	parts := []string{}
	for {
		idx := strings.Index(text, tagStart)
		if idx < 0 {
			break
		}
		rest := text[idx+len(tagStart):]
		// find the closing > of this opening tag
		gt := strings.Index(rest, ">")
		if gt < 0 {
			break
		}
		parts = append(parts, rest[gt+1:])
		text = rest[gt+1:]
	}
	return parts
}

// truncateToClosing returns the substring up to the matching close tag.
func truncateToClosing(s, tag string) string {
	closeTag := "</" + tag + ">"
	idx := strings.Index(s, closeTag)
	if idx < 0 {
		return ""
	}
	return s[:idx]
}

// extractAttrInt extracts an integer attribute value from a tag like
// <a:off x="0" y="0"/>. tagPrefix="<a:off", attrName="x".
func extractAttrInt(block, tagPrefix, attrName string) int64 {
	idx := strings.Index(block, tagPrefix)
	if idx < 0 {
		return -1
	}
	rest := block[idx:]
	gt := strings.Index(rest, ">")
	if gt < 0 {
		return -1
	}
	tagContent := rest[:gt]
	// Find attrName="
	search := attrName + "=\""
	aIdx := strings.Index(tagContent, search)
	if aIdx < 0 {
		return -1
	}
	valStart := aIdx + len(search)
	valEnd := strings.Index(tagContent[valStart:], "\"")
	if valEnd < 0 {
		return -1
	}
	var n int64
	fmt.Sscanf(tagContent[valStart:valStart+valEnd], "%d", &n)
	return n
}

// extractBlipRID extracts r:embed="rId2" from a blip element.
func extractBlipRID(block string) string {
	// Scope to <a:blip ...> to avoid matching embed= in unrelated elements.
	blipIdx := strings.Index(block, "<a:blip")
	if blipIdx < 0 {
		// Some producers use a different prefix; fall back to whole-block search
		blipIdx = 0
	}
	searchBlock := block[blipIdx:]
	embedKey := "embed=\""
	idx := strings.Index(searchBlock, embedKey)
	if idx < 0 {
		return ""
	}
	rest := searchBlock[idx+len(embedKey):]
	end := strings.Index(rest, "\"")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// isFullScreenEMU checks if the EMU coordinates represent a full-screen image.
func isFullScreenEMU(x, y, cx, cy int64) bool {
	if x < 0 || y < 0 || cx <= 0 || cy <= 0 {
		return false
	}
	atOrigin := x < 200000 && y < 200000
	fullW := abs64(cx-fullScreenW16x9) < fullScreenTol || abs64(cx-fullScreenW4x3) < fullScreenTol
	fullH := abs64(cy-fullScreenH) < fullScreenTol
	return atOrigin && fullW && fullH
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// buildRelsPath converts a part path to its rels path.
// e.g. ppt/slideLayouts/slideLayout1.xml -> ppt/slideLayouts/_rels/slideLayout1.xml.rels
func buildRelsPath(partName string) string {
	dir := filepath.Dir(partName)
	base := filepath.Base(partName)
	return filepath.ToSlash(filepath.Join(dir, "_rels", base+".rels"))
}

// resolveImageFromRels reads the rels XML and finds the image target for rid.
// Returns the normalized in-zip path (e.g. ppt/media/image1.png).
func resolveImageFromRels(zr *zip.ReadCloser, relsPath, rid string) (string, error) {
	for _, f := range zr.File {
		if f.Name != relsPath {
			continue
		}
		data, err := readZipFile(f)
		if err != nil {
			return "", err
		}
		// Parse relationships XML to find the matching Id
		var rels relationships
		if err := xml.Unmarshal(data, &rels); err != nil {
			return "", err
		}
		for _, rel := range rels.Relationships {
			if rel.ID == rid && strings.Contains(strings.ToLower(rel.Target), "image") {
				return normalizeZipPath(rel.Target, filepath.Dir(relsPath)), nil
			}
		}
	}
	return "", fmt.Errorf("rels %s not found or rid %s not an image", relsPath, rid)
}

type relationships struct {
	XMLName       xml.Name       `xml:"Relationships"`
	Relationships []relationship `xml:"Relationship"`
}

type relationship struct {
	ID     string `xml:"Id,attr"`
	Type   string `xml:"Type,attr"`
	Target string `xml:"Target,attr"`
}

// normalizeZipPath resolves a relative Target (like ../media/image1.png) against
// the rels dir (like ppt/slideLayouts/_rels) to an absolute in-zip path
// (ppt/media/image1.png).
func normalizeZipPath(target, relsDir string) string {
	target = strings.ReplaceAll(target, "\\", "/")
	// relsDir is like ppt/slideLayouts/_rels; the part dir is its parent
	partDir := filepath.Dir(relsDir)
	// Clean and join
	combined := filepath.ToSlash(filepath.Join(partDir, target))
	// Resolve any .. by cleaning
	combined = filepath.Clean(combined)
	// filepath.Clean uses OS separators; normalize
	combined = strings.ReplaceAll(combined, "\\", "/")
	// Remove leading slash if any (zip paths are relative)
	combined = strings.TrimPrefix(combined, "/")
	// If target was absolute (/ppt/media/...), handle that
	if strings.HasPrefix(target, "/") {
		combined = strings.TrimPrefix(target, "/")
	}
	return combined
}

// encodeImageForVLM decodes the image bytes and re-encodes as a downscaled
// JPEG data URL suitable for builtin.CallVLM.
func encodeImageForVLM(imgBytes []byte, fmtStr string) (string, error) {
	img, _, err := image.Decode(bytes.NewReader(imgBytes))
	if err != nil {
		return "", fmt.Errorf("decode image: %w", err)
	}
	return downscaleAndEncodeJPEG(img), nil
}

// downscaleAndEncodeJPEG downscales to at most pptVlmMaxDim on the long edge
// and returns a base64 JPEG data URL.
func downscaleAndEncodeJPEG(img image.Image) string {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	scaled := img
	if w > pptVlmMaxDim || h > pptVlmMaxDim {
		nw, nh := w, h
		if w >= h {
			nw = pptVlmMaxDim
			nh = h * pptVlmMaxDim / w
		} else {
			nh = pptVlmMaxDim
			nw = w * pptVlmMaxDim / h
		}
		dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
		scaled = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: 85}); err != nil {
		// Fallback: PNG
		buf.Reset()
		_ = png.Encode(&buf, scaled)
		return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// readZipFile reads a zip.File into memory.
func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// parseVLMStyleResponse extracts the JSON from the VLM's text response. The
// model may wrap it in markdown or add prose, so we find the first {...} block.
func parseVLMStyleResponse(resp string) *templateStyleResult {
	// Find the first { and last } to extract the JSON object from prose/markdown
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end < 0 || end <= start {
		pptVisionDebugLog("parseVLMStyleResponse: no JSON object in response (len=%d, head=%q)", len(resp), strHead(resp, 120))
		return nil
	}
	jsonStr := resp[start : end+1]

	result := &templateStyleResult{Source: "vision-vlm", BackgroundType: "image"}
	if err := json.Unmarshal([]byte(jsonStr), result); err != nil {
		// Reasoning-model VLMs can return HTTP 200 with content=None (reasoning
		// ate the whole token budget) — resp is then empty and lands in the
		// no-JSON branch above; other malformations land here. Log enough to
		// diagnose without re-running the call.
		pptVisionDebugLog("parseVLMStyleResponse: unmarshal failed: %v (json head=%q)", err, strHead(jsonStr, 160))
		return nil // malformed JSON — fall back to Python extractor
	}
	// Override the defaults (Unmarshal would leave these at zero values)
	result.Source = "vision-vlm"
	// Preserve the VLM's background_type when it gave one ("solid" matters for
	// reference pages — unconditionally forcing "image" used to mislabel
	// flat-color references); "image" remains only the fallback (template bg).
	if result.BackgroundType == "" {
		result.BackgroundType = "image"
	}

	// Sanity: must have at least a background
	if result.Background == "" {
		pptVisionDebugLog("parseVLMStyleResponse: parsed JSON has no background field (%q)", strHead(jsonStr, 160))
		return nil
	}
	return result
}

// strHead returns the first n bytes of s, for logging arbitrary responses.
func strHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// jsonMarshal marshals the result to indented JSON.
func jsonMarshal(v interface{}) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
