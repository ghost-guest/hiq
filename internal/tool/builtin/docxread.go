package builtin

// docxread.go implements doc_read's non-default modes for .docx:
//   - structure: JSON of paragraphs/headings/tables with indices that align
//     with doc_write template-fill's find_replace (text runs) and table_fill
//     (table/row/col)
//   - tables: JSON of tables only — the rows/cols a template-fill table_fill op
//     would target
//   - metadata: author/title/created/modified from docProps/core.xml
//
// All three use string-level parsing (regexp/scan) for the same reason
// find_replace and table_fill do: encoding/xml normalizes namespace prefixes
// on re-encode and breaks the OOXML round-trip. We read only — no mutation —
// so we could safely use xml.Decoder here, but keeping the parse style uniform
// with the rest of the template-fill subsystem avoids a second mental model.

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	fileenc "github.com/zzycxz/hiq/internal/fileutil/encoding"
)

// docxStructure is the JSON shape returned by mode:"structure". It mirrors the
// document's block flow so the LLM can see headings, paragraphs, and tables in
// order, with table/row/col indices that match template-fill's table_fill.
type docxStructure struct {
	Title    string          `json:"title,omitempty"`
	Blocks   []docxBlock     `json:"blocks"`
	Metadata docxMetadataOut `json:"metadata,omitempty"`
}

type docxBlock struct {
	Type  string         `json:"type"` // "heading" | "paragraph" | "table"
	Level int            `json:"level,omitempty"`
	Index *int           `json:"index,omitempty"` // Matches paragraph_replace index
	Text  string         `json:"text,omitempty"`
	Style *docxRunStyle  `json:"style,omitempty"` // run-level formatting extracted from <w:rPr>
	Table *docxTableInfo `json:"table,omitempty"`
}

// docxRunStyle is the formatting snapshot extracted from a paragraph's first
// run's <w:rPr> (and the paragraph's <w:pPr> for alignment). It lets the LLM
// understand the template's formatting without opening Word — bold headings,
// font choices (公文 SimHei/SimSun), sizes, colors, alignment. Empty fields
// mean "not explicitly set" (inherits from style/defaults).
type docxRunStyle struct {
	Bold      bool   `json:"bold,omitempty"`
	Italic    bool   `json:"italic,omitempty"`
	Underline bool   `json:"underline,omitempty"`
	Color     string `json:"color,omitempty"`     // hex RRGGBB
	Size      string `json:"size,omitempty"`      // half-points as string ("24" = 12pt)
	Font      string `json:"font,omitempty"`      // font family
	Align     string `json:"align,omitempty"`     // left/center/right/justify
	StyleID   string `json:"style_id,omitempty"`  // the pStyle id (e.g. "Heading1")
}

type docxTableInfo struct {
	Index int        `json:"index"`
	Rows  [][]string `json:"rows"`
}

type docxMetadataOut struct {
	Author   string `json:"author,omitempty"`
	Title    string `json:"title,omitempty"`
	Subject  string `json:"subject,omitempty"`
	Created  string `json:"created,omitempty"`
	Modified string `json:"modified,omitempty"`
}

// readDOCXStructure returns the document as structured JSON.
func readDOCXStructure(path string) (string, error) {
	parts, err := readAllDocxParts(path)
	if err != nil {
		return "", err
	}
	body := parts["word/document.xml"]
	if body == nil {
		return "", fmt.Errorf("docx has no word/document.xml")
	}
	meta := readCoreProps(parts["docProps/core.xml"])
	structure := docxStructure{Title: meta.Title, Metadata: meta}
	structure.Blocks = parseBlocks(body)
	// Append secondary text parts (header/footer/footnote/endnote/comment) as
	// labeled blocks so the agent sees content that lives outside the body —
	// contract numbers in headers, citations in footnotes, etc. Each becomes a
	// paragraph block whose Type carries the source label (e.g. "header").
	structure.Blocks = appendSecondaryBlocks(structure.Blocks, parts)
	out, _ := json.MarshalIndent(structure, "", "  ")
	return string(out), nil
}

// appendSecondaryBlocks scans the package's non-body text parts and appends
// their content as labeled blocks. Labels: header / footer / footnote /
// endnote / comment. The body's blocks come first; secondary parts follow.
func appendSecondaryBlocks(blocks []docxBlock, parts map[string][]byte) []docxBlock {
	// Stable order: header*, footer*, footnotes, endnotes, comments.
	order := []struct{ prefix, label string }{
		{"word/header", "header"},
		{"word/footer", "footer"},
		{"word/footnotes", "footnote"},
		{"word/endnotes", "endnote"},
		{"word/comments", "comment"},
	}
	// Collect part names per category, sorted for deterministic output.
	for _, cat := range order {
		var names []string
		for name := range parts {
			if strings.HasPrefix(name, cat.prefix) && strings.HasSuffix(name, ".xml") {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			text := flattenDocxText(parts[name])
			if strings.TrimSpace(text) == "" {
				continue
			}
			blocks = append(blocks, docxBlock{Type: cat.label, Text: text})
		}
	}
	return blocks
}

// flattenDocxText extracts visible text from an OOXML part by pulling every
// <w:t>...</w:t> run's content. Simpler than parseBlocks (no structure needed
// for header/footer prose) and reuses the same string-scan approach.
func flattenDocxText(data []byte) string {
	s := string(data)
	var b strings.Builder
	for {
		idx := strings.Index(s, "<w:t")
		if idx < 0 {
			break
		}
		gt := strings.IndexByte(s[idx:], '>')
		if gt < 0 {
			break
		}
		start := idx + gt + 1
		end := strings.Index(s[start:], "</w:t>")
		if end < 0 {
			break
		}
		b.WriteString(s[start : start+end])
		b.WriteByte(' ')
		s = s[start+end:]
	}
	return strings.TrimSpace(b.String())
}

// readAllDocxParts opens the .docx zip and returns a name→bytes map of parts.
// XML parts are normalized to UTF-8: real-world docx files (especially from
// Chinese WPS/Word) sometimes carry GBK/GB18030 bytes inside an XML part that
// DECLARES encoding="UTF-8". Without normalization, find_replace can't match
// any CJK text (the bytes don't line up with UTF-8 codepoints). We detect and
// transcode each XML part so downstream string matching works correctly.
func readAllDocxParts(path string) (map[string][]byte, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open docx (is it a valid .docx?): %w", err)
	}
	defer zr.Close()
	parts := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		// Normalize XML parts to UTF-8 (mirrors OfficeCLI's FixXmlEncoding).
		// Only touch .xml/.rels parts; media/fonts/etc. are binary and must
		// not be "decoded".
		if isXMLPart(f.Name) {
			data = normalizeToUTF8(data)
		}
		parts[f.Name] = data
	}
	return parts, nil
}

// isXMLPart reports whether a zip entry is an XML/rels part (vs binary media).
func isXMLPart(name string) bool {
	return strings.HasSuffix(name, ".xml") || strings.HasSuffix(name, ".rels")
}

// normalizeToUTF8 detects the byte encoding and transcodes to UTF-8 if needed.
// Strips BOM first. If the data is already valid UTF-8 AND contains recognizable
// CJK text (not GBK-as-UTF-8 mojibake), returns it unchanged.
//
// The tricky case: GBK-encoded Chinese text is often ALSO valid UTF-8 by accident
// (GBK byte pairs 0xB0-0xF7 / 0x40-0xFE happen to form legal UTF-8 2-byte
// sequences). utf8.Valid returns true, but decoding produces mojibake. We detect
// this by checking for common Chinese characters: real UTF-8 Chinese text
// contains '的'/'一'/'是' (which have distinct 3-byte UTF-8 patterns); GBK-as-UTF8
// produces garbage that won't contain them.
func normalizeToUTF8(data []byte) []byte {
	data = stripBOM(data)
	if !utf8.Valid(data) {
		// Not valid UTF-8 — detect and transcode.
		kind, _ := fileenc.Detect(data)
		return []byte(fileenc.Decode(data, kind))
	}
	// Valid UTF-8. But could be GBK-as-UTF-8 mojibake. Check for common CJK.
	// If the XML declares UTF-8 AND contains <w:t> with CJK runs, the text
	// inside should have recognizable Chinese. If '的'/'一'/'是' are all absent
	// but there ARE high bytes (non-ASCII), it's likely GBK misread as UTF-8.
	if looksLikeMojibake(data) {
		// Try GB18030 decode (superset of GBK).
		kind, _ := fileenc.Detect(encodeAsGBK(data))
		if kind != fileenc.UTF8 {
			return []byte(fileenc.Decode(encodeAsGBK(data), kind))
		}
		// Fallback: decode the ORIGINAL bytes as GB18030.
		return []byte(fileenc.Decode(data, fileenc.GB18030))
	}
	return data
}

// looksLikeMojibake reports whether data is valid UTF-8 but likely GBK text
// misread as UTF-8. Heuristic: has many high bytes (CJK range) but none of the
// common characters that real CJK documents contain — across Chinese, Japanese,
// AND Korean. Only if none of the high-frequency characters from ANY of these
// languages appear do we flag it as likely mojibake. This avoids false positives
// on Japanese (の/は/が) and Korean (가/이/은) documents.
func looksLikeMojake(data []byte) bool {
	return looksLikeMojibake(data)
}
func looksLikeMojibake(data []byte) bool {
	highBytes := 0
	for _, b := range data {
		if b >= 0x80 {
			highBytes++
		}
	}
	// Only check documents with substantial CJK content — small docs (test
	// fixtures with a few words) don't have enough signal.
	if highBytes < 100 {
		return false
	}
	s := string(data)
	// High-frequency characters from each major CJK language. A real document
	// in ANY of these languages will contain at least one. Only if NONE appear
	// do we suspect GBK-as-UTF-8 mojibake.
	//
	// Chinese: 的是了一在有 (top frequency chars)
	// Japanese: のはがを (grammatical particles in every JP text)
	for _, ch := range "的是了一在有のはがを" {
		if strings.ContainsRune(s, ch) {
			return false // found a real CJK char — it IS valid UTF-8
		}
	}
	// Korean check: Hangul syllables (U+AC00–U+D7AF) are 3-byte UTF-8 sequences
	// starting with 0xEA/0xEB/0xEC. A real Korean doc has many of these; GBK
	// mojibake won't produce valid Hangul. Scan for a few Hangul ranges.
	hangulCount := 0
	for i := 0; i+2 < len(data); i++ {
		if data[i] == 0xEA || data[i] == 0xEB || data[i] == 0xEC {
			// Check that bytes i+1/i+2 are in the valid Hangul continuation range.
			if data[i+1] >= 0x80 && data[i+1] <= 0xBF && data[i+2] >= 0x80 && data[i+2] <= 0xBF {
				hangulCount++
			}
		}
	}
	if hangulCount >= 5 {
		return false // found real Korean text — it IS valid UTF-8
	}
	return true // many high bytes but no common CJK chars → likely mojibake
}

// encodeAsGBK is a no-op placeholder — we can't easily "re-encode" UTF-8 back
// to GBK to then re-detect. Instead we just pass the original bytes to Detect
// which tries GB18030 decoding directly. The function name is kept for clarity.
func encodeAsGBK(data []byte) []byte { return data }

// readCoreProps extracts the Dublin Core / core properties from docProps/core.xml.
// We pull the common fields by simple tag scan (the file is small and flat).
func readCoreProps(data []byte) docxMetadataOut {
	var m docxMetadataOut
	if data == nil {
		return m
	}
	m.Author = extractTagText(data, "creator")
	m.Title = extractTagText(data, "title")
	m.Subject = extractTagText(data, "subject")
	m.Created = extractTagText(data, "created")
	m.Modified = extractTagText(data, "modified")
	return m
}

// extractTagText finds <{tag}...>text</{tag}> and returns text. Local-name
// match (ignores namespace prefix). Returns "" if not found.
func extractTagText(data []byte, tag string) string {
	s := string(data)
	// Simple approach: find "<tag" then ">" then text then "</...tag>".
	idx := strings.Index(s, "<"+tag)
	if idx < 0 {
		// try with any namespace prefix: "<cp:tag" etc.
		idx = strings.Index(s, ":"+tag)
		if idx >= 0 {
			// back up to '<'
			for idx > 0 && s[idx] != '<' {
				idx--
			}
		}
	}
	if idx < 0 {
		return ""
	}
	gt := strings.IndexByte(s[idx:], '>')
	if gt < 0 {
		return ""
	}
	textStart := idx + gt + 1
	closeIdx := strings.Index(s[textStart:], "</")
	if closeIdx < 0 {
		return ""
	}
	return strings.TrimSpace(s[textStart : textStart+closeIdx])
}

// parseBlocks walks the body XML and emits a block list. Headings and paragraphs
// are detected by their paragraph style (HeadingN) or fall back to paragraph.
// Tables emit a docxTableInfo with index/headers/rows.
func parseBlocks(body []byte) []docxBlock {
	var blocks []docxBlock
	s := string(body)
	tableIdx := -1
	paraIdx := -1

	// We scan tag-by-tag, tracking open/close of p/tbl/tr/tc to group text.
	type tcBuf struct{ text strings.Builder }
	var curRow []string
	var curTable *docxTableInfo
	var paraText strings.Builder
	var paraStyle string
	var paraAlign string                 // <w:jc w:val="..."/>
	var paraOutlineLvl int               // <w:outlineLvl w:val="N"/>; -1 = not set
	var paraRunStyle *docxRunStyle       // first run's rPr formatting
	var inRPr bool                       // inside <w:rPr>
	var inPPr bool                       // inside <w:pPr>
	var rPrBuf strings.Builder           // accumulate rPr inner XML
	var inPara, inTable, inRow, inCell bool
	_ = tcBuf{}

	flushPara := func() {
		text := strings.TrimSpace(paraText.String())
		paraText.Reset()
		if text == "" && paraStyle == "" {
			// reset formatting state even on empty
			paraStyle = ""
			paraAlign = ""
			paraOutlineLvl = -1
			paraRunStyle = nil
			paraIdx++ // keep index aligned with applyParagraphReplace
			return
		}
		var style *docxRunStyle
		if paraRunStyle != nil || paraAlign != "" || paraStyle != "" {
			style = paraRunStyle
			if style == nil {
				style = &docxRunStyle{}
			}
			if paraAlign != "" {
				style.Align = paraAlign
			}
			if paraStyle != "" {
				style.StyleID = paraStyle
			}
		}
		paraIdx++
		idxCopy := paraIdx
		// Heading detection: first try the pStyle name (HeadingN, 标题N, etc.),
		// then fall back to <w:outlineLvl> which some templates set without a
		// standard HeadingN style (custom style names, localized Word).
		lvl, isHeading := headingLevel(paraStyle)
		if !isHeading && paraOutlineLvl >= 0 && paraOutlineLvl <= 8 {
			lvl = paraOutlineLvl + 1 // outlineLvl is 0-based, heading level is 1-based
			isHeading = true
		}
		if isHeading {
			blocks = append(blocks, docxBlock{Type: "heading", Level: lvl, Index: &idxCopy, Text: text, Style: style})
		} else {
			blocks = append(blocks, docxBlock{Type: "paragraph", Index: &idxCopy, Text: text, Style: style})
		}
		paraStyle = ""
		paraAlign = ""
		paraOutlineLvl = -1
		paraRunStyle = nil
	}
	flushCell := func() {
		curRow = append(curRow, strings.TrimSpace(paraText.String()))
		paraText.Reset()
	}

	i := 0
	for i < len(s) {
		adv, kind, isClose, isSelfClose, attr := scanTag(s, i)
		if adv == 0 {
			i++
			continue
		}

		if kind == "t" && !isClose && !isSelfClose {
			// gather text until </w:t>
			textEnd := strings.Index(s[i+adv:], "</w:t>")
			if textEnd < 0 {
				textEnd = strings.Index(s[i+adv:], "<")
				if textEnd < 0 {
					textEnd = len(s) - i - adv
				}
			}
			paraText.WriteString(s[i+adv : i+adv+textEnd])
			i += adv + textEnd
			continue
		}

		processTag := func(closing bool) {
			if !closing {
				switch kind {
				case "p":
					if !inCell {
						inPara = true
						paraText.Reset()
						paraStyle = ""
						paraAlign = ""
						paraOutlineLvl = -1
						paraRunStyle = nil
						inPPr = false
						inRPr = false
					}
				case "pStyle":
					paraStyle = attrVal(attr, "val")
				case "outlineLvl":
					// <w:outlineLvl w:val="N"/> in pPr — used as a heading-level
					// fallback when the pStyle isn't a standard HeadingN name.
					if inPPr {
						if n := atoiSafe(attrVal(attr, "val")); n >= 0 && n <= 8 {
							paraOutlineLvl = n
						}
					}
				case "pPr":
					inPPr = true
				case "jc":
					if inPPr {
						paraAlign = attrVal(attr, "val")
					}
				case "rPr":
					if paraRunStyle == nil {
						paraRunStyle = &docxRunStyle{}
						rPrBuf.Reset()
					}
					inRPr = true
				case "b":
					if inRPr && paraRunStyle != nil {
						paraRunStyle.Bold = true
					}
				case "i":
					if inRPr && paraRunStyle != nil {
						paraRunStyle.Italic = true
					}
				case "u":
					if inRPr && paraRunStyle != nil {
						paraRunStyle.Underline = true
					}
				case "sz":
					if inRPr && paraRunStyle != nil {
						paraRunStyle.Size = attrVal(attr, "val")
					}
				case "color":
					if inRPr && paraRunStyle != nil {
						paraRunStyle.Color = attrVal(attr, "val")
					}
				case "rFonts":
					if inRPr && paraRunStyle != nil {
						if f := attrVal(attr, "eastAsia"); f != "" {
							paraRunStyle.Font = f
						} else if f := attrVal(attr, "ascii"); f != "" {
							paraRunStyle.Font = f
						}
					}
				case "tbl":
					inTable = true
					tableIdx++
					curTable = &docxTableInfo{Index: tableIdx}
				case "tr":
					if inTable {
						inRow = true
						curRow = curRow[:0]
					}
				case "tc":
					if inTable && inRow {
						inCell = true
						paraText.Reset()
					}
				}
			} else {
				switch kind {
				case "p":
					if inCell {
						if paraText.Len() > 0 {
							paraText.WriteByte(' ')
						}
					} else if inPara {
						flushPara()
						inPara = false
					}
				case "rPr":
					inRPr = false
				case "pPr":
					inPPr = false
				case "tc":
					if inTable && inRow {
						flushCell()
						inCell = false
					}
				case "tr":
					if inTable && inRow {
						row := append([]string(nil), curRow...)
						curTable.Rows = append(curTable.Rows, row)
						inRow = false
					}
				case "tbl":
					if inTable && curTable != nil {
						blocks = append(blocks, docxBlock{Type: "table", Table: curTable})
						curTable = nil
						inTable = false
					}
				}
			}
		}

		if !isClose {
			processTag(false)
		}
		if isClose || isSelfClose {
			processTag(true)
		}
		i += adv
	}
	if inPara {
		flushPara()
	}
	return blocks
}

// headingLevel maps a heading pStyle to its level (1-9). Returns ok=false for
// non-heading styles. Recognizes:
//   - "Heading1".."Heading9" (English, the OOXML default)
//   - "标题1".."标题9" (Simplified Chinese WPS/Word)
//   - "見出し1".."見出し9" (Japanese Word)
//   - "제목1".."제목9" (Korean Word)
//   - Custom styles that contain "heading" + a digit (e.g. "MyHeading2")
func headingLevel(style string) (int, bool) {
	style = strings.TrimSpace(style)
	if style == "" {
		return 0, false
	}
	lower := strings.ToLower(style)
	// English: "Heading1".."Heading9", "Heading 1", "heading-1"
	if strings.HasPrefix(lower, "heading") {
		num := strings.TrimPrefix(style, style[:len("heading")]) // strip "Heading"/"heading"/etc.
		num = strings.TrimSpace(strings.Trim(num, " -_"))
		if n := atoiSafe(num); n >= 1 && n <= 9 {
			return n, true
		}
	}
	// Localized heading style prefixes (WPS and localized Word use these).
	localizedPrefixes := []string{"标题", "見出し", "제목"}
	for _, prefix := range localizedPrefixes {
		if strings.HasPrefix(style, prefix) {
			num := strings.TrimPrefix(style, prefix)
			num = strings.TrimSpace(num)
			if n := atoiSafe(num); n >= 1 && n <= 9 {
				return n, true
			}
		}
	}
	return 0, false
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// scanTag inspects s at pos (must be '<'). Returns (adv, localName, isClose, attrString).
// adv is the byte length of the opening tag (through '>'); 0 if not a tag.
// attrString is the raw attribute text (for extracting w:val etc.).
func scanTag(s string, pos int) (adv int, local string, isClose bool, isSelfClose bool, attr string) {
	if pos >= len(s) || s[pos] != '<' {
		return 0, "", false, false, ""
	}
	end := strings.IndexByte(s[pos:], '>')
	if end < 0 {
		return 0, "", false, false, ""
	}
	adv = end + 1
	inner := s[pos+1 : pos+end]
	selfClose := strings.HasSuffix(inner, "/")
	if selfClose {
		inner = strings.TrimSuffix(inner, "/")
	}
	inner = strings.TrimSpace(inner)
	if strings.HasPrefix(inner, "/") {
		isClose = true
		inner = inner[1:]
	}
	// Split name and attrs on first space.
	sp := strings.IndexByte(inner, ' ')
	if sp >= 0 {
		local = inner[:sp]
		attr = inner[sp+1:]
	} else {
		local = inner
	}
	if colon := strings.Index(local, ":"); colon >= 0 {
		local = local[colon+1:]
	}
	return adv, local, isClose, selfClose, attr
}

// attrVal extracts the value of attr `name` from an attribute string like
// `w:val="Heading1" xml:space="preserve"`. Returns "" if absent.
func attrVal(attr, name string) string {
	// find `name="..."` or `...:name="..."` (local match).
	parts := strings.Fields(attr)
	for _, p := range parts {
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			continue
		}
		key := p[:eq]
		if strings.HasSuffix(key, ":"+name) || key == name {
			val := p[eq+1:]
			val = strings.Trim(val, `"'`)
			return val
		}
	}
	return ""
}
