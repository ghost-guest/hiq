package rag

// officedoc.go implements text extraction from binary Office formats (.docx,
// .xlsx, .pptx, .pdf) using stdlib for Office and ledongthuc/pdf for PDF.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	goruntime "runtime"

	pdflib "github.com/ledongthuc/pdf"

	rt "github.com/zzycxz/hiq/internal/runtime"

	"github.com/zzycxz/hiq/internal/docconv"
	"github.com/zzycxz/hiq/internal/proc"
)

// cjkSpaceRe matches a space between two CJK characters.
var cjkSpaceRe = regexp.MustCompile(`(\p{Han})\s+(\p{Han})`)

// fixCJKSpaces removes spaces between CJK characters. PDF renderers often
// insert spurious spaces because the internal character positioning uses
// discrete glyph boxes — a tiny gap becomes a space in extracted text.
func fixCJKSpaces(s string) string {
	// Run multiple passes because the regex only catches one gap at a time
	// and adjacent matches don't overlap ("A B C" → "AB C" → "ABC").
	for {
		fixed := cjkSpaceRe.ReplaceAllString(s, "$1$2")
		if fixed == s {
			return fixed
		}
		s = fixed
	}
}

// readPDF extracts text from a .pdf file. Prefers the Python pipeline
// (pdfplumber for tables + PaddleOCR for scanned pages). Falls back to
// ledongthuc/pdf (pure Go) when the Python script is unavailable.
func readPDF(path string) (string, error) {
	// Try Python pipeline first (pdfplumber + PaddleOCR).
	if findOCRScript() != "" {
		text, err := readPDFWithOCR(path)
		if err == nil && utf8.RuneCountInString(text) > 0 {
			return fixCJKSpaces(text), nil
		}
	}

	// Try markitdown (Python doc converter) for PDF — handles modern PDF
	// stream encodings that the Go library can't parse.
	if findDocConverterScript() != "" {
		text, err := convertWithMarkitdown(path)
		if err == nil && utf8.RuneCountInString(text) > 0 {
			return fixCJKSpaces(text), nil
		}
	}

	// Fallback: ledongthuc/pdf (pure Go). Limited — can't parse many
	// modern PDFs (xref streams, object streams). Returns error on failure
	// so the caller can report which files were skipped.
	f, r, err := pdflib.Open(path)
	if err != nil {
		return "", fmt.Errorf("open pdf (Go fallback): %w", err)
	}
	defer f.Close()
	var b strings.Builder
	pageErrors := 0
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, err := p.GetPlainText(nil)
		if err != nil {
			pageErrors++
			continue
		}
		text = strings.TrimSpace(text)
		if text != "" {
			b.WriteString(text)
			b.WriteString("\n\n")
		}
	}
	result := strings.TrimSpace(b.String())
	if result == "" {
		if pageErrors > 0 {
			return "", fmt.Errorf("pdf: %d/%d pages failed to parse (unsupported stream encoding)", pageErrors, r.NumPage())
		}
		return "", fmt.Errorf("pdf: no extractable text found")
	}
	return fixCJKSpaces(result), nil
}

// ReadPDF is the exported wrapper around readPDF so other packages (e.g. the
// document tools in internal/tool/builtin) can reuse the same PDF extraction
// pipeline (ocr_pdf.py → markitdown → pure-Go fallback) without duplicating it.
func ReadPDF(path string) (string, error) { return readPDF(path) }

// ocrScriptCandidates lists possible locations of ocr_pdf.py.
func ocrScriptCandidates() []string { //nolint:unused
	return docconv.ScriptCandidates("ocr_pdf.py")
}

// findOCRScript locates the ocr_pdf.py script.
func findOCRScript() string {
	return docconv.FindScript("ocr_pdf.py")
}

// docConverterScriptCandidates lists possible locations of doc_converter.py.
func docConverterScriptCandidates() []string { //nolint:unused
	return docconv.ScriptCandidates("doc_converter.py")
}

// findDocConverterScript locates the doc_converter.py script.
func findDocConverterScript() string {
	return docconv.FindScript("doc_converter.py")
}

// convertWithMarkitdown calls doc_converter.py to convert a file to Markdown.
func convertWithMarkitdown(path string) (string, error) {
	return docconv.ConvertText(path)
}

// readPDFWithOCR calls the ocr_pdf.py Python script to extract text from a
// scanned PDF using PaddleOCR. Returns the OCR'd text or an error.
func readPDFWithOCR(path string) (string, error) {
	script := findOCRScript()
	if script == "" {
		return "", fmt.Errorf("ocr_pdf.py not found")
	}
	pyCmd, pyPrefix, _ := rt.ResolvePython()
	if pyCmd == "" {
		pyCmd = "python3"
	}
	args := append(append([]string{}, pyPrefix...), script, path)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, pyCmd, args...)
	proc.HideWindow(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ocr script: %w: %s", err, stderr.String())
	}

	var result struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return "", fmt.Errorf("ocr parse: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("ocr: %s", result.Error)
	}
	return result.Text, nil
}

// readDOCX extracts text from word/document.xml, joining paragraphs with newlines.
func readDOCX(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("open docx (is it a valid .docx?): %w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return "", err
		}
		return parseDOCXText(data), nil
	}
	return "", fmt.Errorf("docx has no word/document.xml")
}

// parseDOCXText walks the document XML collecting <w:t> runs, breaking at
// paragraph boundaries (<w:p>). Tabs and breaks are approximated.
func parseDOCXText(data []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var b strings.Builder
	var inPara bool
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				if inPara {
					b.WriteByte('\n')
				}
				inPara = true
			case "t":
				var txt string
				if err := dec.DecodeElement(&txt, &t); err == nil {
					b.WriteString(txt)
				}
			case "tab":
				b.WriteByte('\t')
			case "br":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			if t.Name.Local == "p" {
				b.WriteByte('\n')
				inPara = false
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// readXLSXAsText extracts cell values from a .xlsx via stdlib zip+xml,
// returning all sheets as tab-separated text. No excelize dependency.
func readXLSXAsText(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("open xlsx (is it a valid .xlsx?): %w", err)
	}
	defer zr.Close()

	// Read shared strings first.
	var sharedStrings []string
	for _, f := range zr.File {
		if f.Name == "xl/sharedStrings.xml" {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			data, _ := io.ReadAll(rc)
			rc.Close()
			sharedStrings = parseSharedStrings(data)
			break
		}
	}

	// Read each sheet.
	var sheetFiles []string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
			sheetFiles = append(sheetFiles, f.Name)
		}
	}
	sort.Slice(sheetFiles, func(i, j int) bool {
		ni, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(sheetFiles[i]), "sheet"), ".xml"))
		nj, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(sheetFiles[j]), "sheet"), ".xml"))
		return ni < nj
	})

	var b strings.Builder
	for si, name := range sheetFiles {
		for _, ff := range zr.File {
			if ff.Name != name {
				continue
			}
			rc, err := ff.Open()
			if err != nil {
				continue
			}
			data, _ := io.ReadAll(rc)
			rc.Close()
			if si > 0 {
				fmt.Fprintf(&b, "\n--- sheet %d ---\n", si+1)
			}
			rows := parseSheetRows(data, sharedStrings)
			for _, row := range rows {
				b.WriteString(strings.Join(row, "\t"))
				b.WriteString("\n")
			}
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// parseSharedStrings extracts <t> text from xl/sharedStrings.xml.
func parseSharedStrings(data []byte) []string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out []string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "t" {
			var txt string
			if err := dec.DecodeElement(&txt, &se); err == nil {
				out = append(out, txt)
			}
		}
	}
	return out
}

// parseSheetRows extracts rows/cells from a worksheet XML.
func parseSheetRows(data []byte, sharedStrings []string) [][]string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var rows [][]string
	var currentRow []string
	inRow := false
	inValue := false
	cellType := ""
	var valueBuf strings.Builder

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				inRow = true
				currentRow = nil
			case "c":
				cellType = ""
				for _, attr := range t.Attr {
					if attr.Name.Local == "t" {
						cellType = attr.Value
					}
				}
			case "v":
				inValue = true
				valueBuf.Reset()
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inValue = false
				val := valueBuf.String()
				if cellType == "s" {
					// Shared string reference.
					idx, err := strconv.Atoi(val)
					if err == nil && idx < len(sharedStrings) {
						val = sharedStrings[idx]
					}
				}
				currentRow = append(currentRow, val)
			case "row":
				inRow = false
				if len(currentRow) > 0 {
					rows = append(rows, currentRow)
				}
			}
		case xml.CharData:
			if inValue && inRow {
				valueBuf.Write(t)
			}
		}
	}
	return rows
}

// readPPTX extracts text from each slide (slideN.xml <a:t> runs), one block
// per slide labeled [slide N].
func readPPTX(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("open pptx (is it a valid .pptx?): %w", err)
	}
	defer zr.Close()
	var slideFiles []string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			slideFiles = append(slideFiles, f.Name)
		}
	}
	sort.Slice(slideFiles, func(i, j int) bool {
		ni, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(slideFiles[i]), "slide"), ".xml"))
		nj, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(slideFiles[j]), "slide"), ".xml"))
		return ni < nj
	})
	var b strings.Builder
	for _, name := range slideFiles {
		for _, ff := range zr.File {
			if ff.Name != name {
				continue
			}
			rc, err := ff.Open()
			if err != nil {
				continue
			}
			data, _ := io.ReadAll(rc)
			rc.Close()
			fmt.Fprintf(&b, "[slide %s]\n", filepath.Base(name))
			b.WriteString(parseSlideText(data))
			b.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// parseSlideText collects all <a:t> run text from a slide XML, space-joined.
func parseSlideText(data []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "t" {
			var txt string
			if err := dec.DecodeElement(&txt, &se); err == nil {
				if b.Len() > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(txt)
			}
		}
	}
	return b.String()
}

// officePreviewExts are the rich-document extensions the preview pipeline
// extracts. PDF is absent — callers serve those natively (iframe) rather than
// as text. Legacy binary .doc/.ppt/.rtf have no pure-Go parser and no
// markitdown support; they go straight to Office COM automation (readDOC /
// readPPT), as does legacy .xls when markitdown can't parse it.
var officePreviewExts = map[string]bool{
	"docx": true, "doc": true, "rtf": true,
	"xlsx": true, "xls": true,
	"pptx": true, "ppt": true,
	"epub": true, "msg": true,
}

// IsOfficeDoc reports whether the path's extension is a rich document the
// preview pipeline can attempt to extract.
func IsOfficeDoc(path string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	return officePreviewExts[ext]
}

// ReadDocumentForPreview extracts preview text for a rich document using the
// same pipeline as RAG import: markitdown first (bounded by timeout; 0 uses
// the docconv default), then the pure-Go parsers (docx/xlsx/pptx/epub), then
// Office/WPS COM automation for the legacy binary formats (.doc/.rtf via
// Word, .xls via Excel, .ppt via PowerPoint). Returns an error when nothing
// could parse the file — callers fall back to their binary/external handling.
func ReadDocumentForPreview(path string, timeout time.Duration) (string, error) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if !officePreviewExts[ext] {
		return "", fmt.Errorf(".%s is not a previewable document", ext)
	}
	// Formats markitdown provably can't handle — skip the subprocess (and its
	// ~1-2s Python startup) and go straight to the fallback parser.
	if findDocConverterScript() != "" && ext != "doc" && ext != "ppt" && ext != "rtf" {
		if text, err := convertWithMarkitdownTimeout(path, timeout); err == nil && len(text) > 0 {
			return text, nil
		}
	}
	switch ext {
	case "docx":
		return readDOCX(path)
	case "xlsx":
		return readXLSXAsText(path)
	case "pptx":
		return readPPTX(path)
	case "epub":
		return readEPUB(path)
	case "doc", "rtf":
		return readDOC(path, timeout)
	case "xls":
		return readXLS(path, timeout)
	case "ppt":
		return readPPT(path, timeout)
	default:
		return "", fmt.Errorf(".%s requires markitdown to parse properly", ext)
	}
}

// convertWithMarkitdownTimeout is convertWithMarkitdown with a caller-bound
// timeout (0 → docconv default). Preview callers pass a tight bound so a
// hanging conversion can't pin the UI wait.
func convertWithMarkitdownTimeout(path string, timeout time.Duration) (string, error) {
	script := findDocConverterScript()
	if script == "" {
		return "", errors.New("doc_converter.py not found")
	}
	res, err := docconv.ConvertFile(script, path, timeout)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

// comOfficePath resolves an absolute path (Office COM servers resolve relative
// paths against their own working directory, typically system32) and escapes
// it for a single-quoted PowerShell string literal (double the quotes).
func comOfficePath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return strings.ReplaceAll(path, "'", "''")
}

// runOfficeCOM executes a PowerShell COM-extraction script and decodes its
// base64 stdout into normalized document text. Shared by the Word/Excel/
// PowerPoint extractors: base64 transport keeps CJK independent of console
// code pages, and the timeout bounds a stuck Office process. Non-Windows
// platforms — or a machine without the matching Office app — get an error and
// the caller falls back to its "open externally" handling.
func runOfficeCOM(script string, timeout time.Duration) (string, error) {
	if goruntime.GOOS != "windows" {
		return "", fmt.Errorf("office COM extraction requires Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, proc.ResolvePowerShell(), "-NoProfile", "-NonInteractive", "-Command", script)
	proc.HideWindow(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("office com: timed out after %s", timeout)
		}
		if stderr.Len() > 0 {
			return "", fmt.Errorf("office com: %s", strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("office com: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout.String()))
	if err != nil {
		return "", fmt.Errorf("office com output: %w", err)
	}
	return normalizeDocText(string(raw)), nil
}

// readDOC extracts text from a legacy binary .doc (or .rtf) via Word/WPS COM
// automation. The document opens read-only and is never written back.
func readDOC(path string, timeout time.Duration) (string, error) {
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$path = '%s'
$app = $null
foreach ($progid in @('Word.Application', 'KWPS.Application', 'wps.Application')) {
  try { $app = New-Object -ComObject $progid; break } catch {}
}
if ($null -eq $app) { throw 'no Word-compatible application (Word/WPS) installed' }
try {
  $app.Visible = $false
  $doc = $app.Documents.Open($path, $false, $true)
  try {
    $text = $doc.Content.Text
  } finally {
    $doc.Close($false)
  }
  [Console]::Out.Write([Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($text)))
} finally {
  $app.Quit()
}
`, comOfficePath(path))
	return runOfficeCOM(script, timeout)
}

// readXLS extracts a legacy binary .xls as CSV text via Excel/WPS COM
// automation. UsedRange.Value2 is marshaled in one call (fast even for large
// sheets); sheets are separated by a "[sheet] name" header line.
func readXLS(path string, timeout time.Duration) (string, error) {
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$path = '%s'
$app = $null
foreach ($progid in @('Excel.Application', 'Ket.Application', 'et.Application')) {
  try { $app = New-Object -ComObject $progid; break } catch {}
}
if ($null -eq $app) { throw 'no Excel-compatible application (Excel/WPS) installed' }
try {
  $app.Visible = $false
  $app.DisplayAlerts = $false
  $wb = $app.Workbooks.Open($path, 0, $true)
  try {
    $lines = New-Object System.Collections.Generic.List[string]
    foreach ($ws in $wb.Worksheets) {
      $lines.Add(('[sheet] ' + $ws.Name))
      $used = $ws.UsedRange
      $rows = $used.Rows.Count
      $cols = $used.Columns.Count
      if ($rows -lt 1 -or $cols -lt 1) { continue }
      $vals = $used.Value2
      if ($null -eq $vals) { continue }
      if ($rows -eq 1 -and $cols -eq 1) {
        $lines.Add(('' + $vals))
        continue
      }
      for ($r = 1; $r -le $rows; $r++) {
        $cells = New-Object System.Collections.Generic.List[string]
        for ($c = 1; $c -le $cols; $c++) {
          $v = $vals[$r, $c]
          if ($null -eq $v) {
            $cells.Add('')
          } elseif ($v -is [double]) {
            if ($v -eq [math]::Floor($v)) { $cells.Add([string][int64]$v) } else { $cells.Add([string]$v) }
          } else {
            $cells.Add(('"' + ('' + $v).Replace('"', '""') + '"'))
          }
        }
        $lines.Add(($cells -join ','))
      }
    }
    $nl = [string][char]10
    $text = ($lines -join $nl)
    [Console]::Out.Write([Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($text)))
  } finally {
    $wb.Close($false)
  }
} finally {
  $app.Quit()
}
`, comOfficePath(path))
	return runOfficeCOM(script, timeout)
}

// readPPT extracts text from a legacy binary .ppt via PowerPoint/WPS COM
// automation: every slide's text-frame shapes plus its speaker notes,
// prefixed by a "[slide n]" header line. The presentation opens read-only
// with no window.
func readPPT(path string, timeout time.Duration) (string, error) {
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$path = '%s'
$app = $null
foreach ($progid in @('PowerPoint.Application', 'KWPP.Application', 'wpp.Application')) {
  try { $app = New-Object -ComObject $progid; break } catch {}
}
if ($null -eq $app) { throw 'no PowerPoint-compatible application (PowerPoint/WPS) installed' }
try {
  $pres = $app.Presentations.Open($path, $true, $false, $false)
  try {
    $lines = New-Object System.Collections.Generic.List[string]
    $i = 0
    foreach ($slide in $pres.Slides) {
      $i++
      $lines.Add('')
      $lines.Add(('[slide ' + $i + ']'))
      foreach ($shape in $slide.Shapes) {
        try {
          if ($shape.HasTextFrame -and $shape.TextFrame.HasText) {
            $lines.Add($shape.TextFrame.TextRange.Text)
          }
        } catch {}
      }
      try {
        $nt = $slide.NotesPage.Shapes.Placeholders.Item(2).TextFrame.TextRange.Text
        if ($nt) { $lines.Add('[notes] ' + $nt) }
      } catch {}
    }
    $nl = [string][char]10
    $text = ($lines -join $nl)
    [Console]::Out.Write([Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($text)))
  } finally {
    $pres.Close()
  }
} finally {
  $app.Quit()
}
`, comOfficePath(path))
	return runOfficeCOM(script, timeout)
}

// readEPUB is the pure-Go fallback for .epub (markitdown parses it better
// when present): the epub is a zip of XHTML chapters; each entry is
// tag-stripped and space-joined. Entries are processed in name order — the
// OPF spine order isn't parsed, which only matters for oddly-named chapters.
func readEPUB(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("open epub (is it a valid .epub?): %w", err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		lower := strings.ToLower(f.Name)
		if strings.HasSuffix(lower, ".xhtml") || strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".htm") {
			names = append(names, f.Name)
		}
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		for _, f := range zr.File {
			if f.Name != name {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(rc, 2<<20))
			rc.Close()
			if err != nil {
				continue
			}
			if text := strings.TrimSpace(stripHTML(string(data))); text != "" {
				b.WriteString(text)
				b.WriteString("\n\n")
			}
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("epub has no readable html content")
	}
	return out, nil
}

// normalizeDocText converts Word's Content.Text control characters into
// plain text: \r paragraph marks → \n, \x07 cell/row ends → tab/newline,
// \x0b manual line breaks and \x0c page breaks → \n.
func normalizeDocText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\x07\r", "\n")
	s = strings.ReplaceAll(s, "\x07", "\t")
	s = strings.ReplaceAll(s, "\x0b", "\n")
	s = strings.ReplaceAll(s, "\x0c", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}
