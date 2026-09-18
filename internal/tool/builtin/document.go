package builtin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	fileenc "github.com/zzycxz/hiq/internal/fileutil/encoding"
	"github.com/zzycxz/hiq/internal/rag"
	"github.com/zzycxz/hiq/internal/tool"
	"github.com/zzycxz/hiq/internal/validation"
	"golang.org/x/text/transform"
)

// Document tools for the office/cowork agent. doc_read/doc_write cover the
// document formats an office agent produces and consumes:
//   - text: CSV (encoding/csv, column-width table), JSON (pretty-printed),
//     Markdown/text/HTML/code (encoding-aware, streamed + paginated).
//   - binary Office: .xlsx (excelize), .docx/.pptx (stdlib archive/zip +
//     encoding/xml — OOXML, no external docx lib). .docx is both read and
//     written (structured sections + template fill); .pptx is read-only.
//   - PDF: text extraction via rag.ReadPDF (ocr_pdf.py → markitdown → pure-Go).
//
// Reads detect encoding (GBK/UTF-16/BOM), reject binaries, and stream large
// text files. Writes are crash-atomic (atomicWrite) and preserve the existing
// charset on overwrite. Binary Office reads are guarded against decompression
// bombs. csv_read/xlsx_read are discoverability aliases of doc_read;
// csv_write/xlsx_write alias doc_write.
//
// All tools are read-only/write-aware flagged correctly so the agent's batch
// optimizer can parallelize reads.

// DocumentTools returns the document tools for cowork registration.
func DocumentTools() []tool.Tool {
	return []tool.Tool{docRead{}, docWrite{}, csvRead{}, csvWrite{}, xlsxRead{}, xlsxWrite{}, xlsxQuery{}, docConvert{}, mindmapCreate{}}
}

// maxDocReadBytes caps the stat-size of a text file that doc_read will load
// (via readFileEncoded) for the csv/json/md/txt/html branches. Unlike read_file
// (which streams), doc_read needs the full content to hand to the csv/json
// parsers, so we bound by size instead. 50 MiB covers realistic text documents;
// larger sources should be paged via bash.
const maxDocReadBytes int64 = 50 << 20 // 50 MiB

// --- doc_read (csv/json/md/txt) --------------------------------------------

type docRead struct{ roots []string }

func (docRead) Name() string { return "doc_read" }

func (docRead) Description() string {
	return "Read a document and return its content. Supports: .csv (formatted table), .json (pretty-printed), .md/.txt/.html/.code (text), AND binary Office formats .xlsx (spreadsheet cells as a table), .docx (document text), .pptx (slide text), .pdf (extracted text via OCR/markitdown). Binary Office formats are parsed via the OOXML zip+XML structure. Plain-text formats (md/txt/html/code) stream and paginate by line (default 2000 lines; pass offset/limit to page further). Structured formats (csv/json) and binary Office/PDF truncate output at 200k chars (binary formats cannot be paged); csv/json inputs over 50 MiB are refused. Encoding (GBK/UTF-16/BOM) is detected and decoded automatically."
}

func (docRead) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Absolute path to the document"},
  "offset":{"type":"integer","description":"0-based line to start at (text formats only; default 0). Use to page past the 200k-char cap."},
  "limit":{"type":"integer","description":"Max lines to return (text formats only; default 2000)."}
},
"required":["path"]
}`)
}

func (docRead) ReadOnly() bool { return true }

func (r docRead) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := confineRead(r.roots, p.Path); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(strings.TrimSpace(p.Path))
	if err != nil {
		return "", err
	}
	// A directory can be os.Open'd but not read as a document — catch it up
	// front with an actionable message (mirrors read_file), so the model
	// switches to ls instead of getting a confusing parse error downstream.
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a file — use the ls tool to list it, or read a specific file inside it", abs)
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(abs), "."))
	const max = 200_000

	// Binary Office formats: parse via OOXML, don't treat as text. Guard each
	// against a decompression bomb before opening, mirroring the doc_write
	// template-fill read path — a hostile or corrupted package can otherwise
	// OOM the process
	// (excelize decompresses every part into memory; the stdlib zip readers
	// io.ReadAll each part).
	switch ext {
	case "xlsx", "docx", "pptx":
		if err := guardDecompressionBomb(abs); err != nil {
			return "", err
		}
	}
	switch ext {
	case "xlsx":
		rows, err := readXLSX(abs)
		if err != nil {
			return "", err
		}
		content := formatRows(rows)
		if len(content) > max {
			content = content[:max] + fmt.Sprintf("\n\n[...truncated, %d more chars]", len(content)-max)
		}
		return content, nil
	case "docx":
		content, err := readDOCXStructure(abs)
		if err != nil {
			return "", err
		}
		if len(content) > max {
			content = content[:max] + fmt.Sprintf("\n\n[...truncated, %d more chars]", len(content)-max)
		}
		return content, nil
	case "pptx":
		content, err := readPPTX(abs)
		if err != nil {
			return "", err
		}
		if len(content) > max {
			content = content[:max] + fmt.Sprintf("\n\n[...truncated, %d more chars]", len(content)-max)
		}
		return content, nil
	case "pdf":
		// PDF is not a zip package, so it bypasses guardDecompressionBomb and
		// the NUL-byte text rejection below. Reuse the rag package's extractor
		// (ocr_pdf.py → markitdown → pure-Go fallback) so doc_read and the RAG
		// importer share one extraction path.
		content, err := rag.ReadPDF(abs)
		if err != nil {
			return "", err
		}
		if len(content) > max {
			content = content[:max] + fmt.Sprintf("\n\n[...truncated, %d more chars]", len(content)-max)
		}
		return content, nil
	case "html", "htm":
		// Mind-map HTML is the one .html special-case: writeMindMapHTML embeds
		// the tree as a JSON string in `const MD = "..."`, so streaming the raw
		// HTML gives the model a wall of CDN <script> tags instead of the tree.
		// Detect the markmap signature and extract the embedded Markdown so the
		// model sees the heading-level tree directly. Plain HTML still streams.
		if info, statErr := os.Stat(abs); statErr == nil && info.Size() > maxDocReadBytes {
			return "", fmt.Errorf("file is %d bytes (limit %d); page it with offset/limit or split it first", info.Size(), maxDocReadBytes)
		}
		htmlContent, _, err := readFileEncoded(abs)
		if err != nil {
			return "", err
		}
		if md, ok := extractMindMapMarkdown(htmlContent); ok {
			out := "[extracted from mindmap HTML]\n" + md
			if len(out) > max {
				out = out[:max] + fmt.Sprintf("\n\n[...truncated, %d more chars]", len(out)-max)
			}
			return out, nil
		}
		return streamTextFile(abs, p.Offset, p.Limit)
	case "xmind", "mmap", "pos", "xmm", "mm", "opml":
		// Professional mind-map formats are out of document-auto's scope — each
		// needs a format-specific parser (.xmind is a zip package, .mm/.opml are
		// XML, .mmap is proprietary binary). Give an actionable hint instead of
		// a raw binary rejection or a wall of XML tags. For the XML-based ones
		// (.mm/.opml) the raw source is still LLM-readable, so append it so the
		// model is not empty-handed; zip/proprietary ones get only the hint.
		hint := fmt.Sprintf("hiq deeply supports .md/.html mind maps. .%s is a professional mind-map format that doc_read does not parse natively (.xmind is a zip package, .mm/.opml are XML, .mmap is proprietary). For best results, export your map as .md / .html / .opml from the original app (XMind/FreeMind/MindManager all support OPML export), then read that.", ext)
		if ext == "mm" || ext == "opml" {
			if info, statErr := os.Stat(abs); statErr == nil && info.Size() <= maxDocReadBytes {
				if raw, _, rerr := readFileEncoded(abs); rerr == nil {
					if len(raw) > max {
						raw = raw[:max]
					}
					hint += "\n\n---- raw ." + ext + " source ----\n" + raw
				}
			}
		}
		return hint, nil
	}

	// Text formats branch into two paths:
	//   - csv/json need the FULL decoded content (parsers consume whole bytes),
	//     so they go through readFileEncoded with a stat-gate to bound memory.
	//   - plain text (md/txt/html/code) streams line-by-line via streamTextFile
	//     (peek → detect → bufio.Scanner), so a large .md/.txt is paged without
	//     an OOM risk and without a hard size refusal — matching read_file.
	if ext == "csv" || ext == "json" {
		// Stat-gate the full read so a multi-GB structured file is refused
		// cheaply rather than slurped by readFileEncoded + ReadAll.
		if info, statErr := os.Stat(abs); statErr == nil && info.Size() > maxDocReadBytes {
			return "", fmt.Errorf("file is %d bytes (limit %d); for a large %s, page it with bash head/tail or split it first", info.Size(), maxDocReadBytes, ext)
		}
		content, _, err := readFileEncoded(abs)
		if err != nil {
			return "", err
		}
		if ext == "csv" {
			content, err = formatCSV([]byte(content))
			if err != nil {
				return "", err
			}
		} else { // json
			content, err = formatJSON([]byte(content))
			if err != nil {
				// Not valid JSON — return raw.
			}
		}
		if len(content) > max {
			content = content[:max] + fmt.Sprintf("\n\n[...truncated, %d more chars]", len(content)-max)
		}
		return content, nil
	}
	// Plain text: stream + paginate (defaults to first 2000 lines, like read_file).
	return streamTextFile(abs, p.Offset, p.Limit)
}

// streamTextFile reads a text file with bounded memory (peek → detect → stream
// line-by-line via bufio.Scanner, mirroring read_file's streaming path) and
// returns lines [offset, offset+limit) with 1-based line numbers and pagination
// hints. It rejects binary files (NUL byte) up front. This is the plain-text
// path for doc_read (md/txt/html/code): unlike the csv/json branch (which needs
// the full content for parsing), this never slurps the whole file, so a large
// .md/.txt is paged without an OOM risk and without a hard size refusal.
func streamTextFile(path string, offset, limit int) (string, error) {
	if limit <= 0 {
		limit = readFileDefaultLimit
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// Peek the first 8 KiB for binary rejection + BOM/UTF-16 detection — same
	// discipline as read_file (readfile.go:101-145).
	peek := make([]byte, readFileBinaryPeek)
	pn, perr := io.ReadFull(f, peek)
	peek = peek[:pn]
	peekEOF := perr != nil // whole file fit in the peek

	// UTF-16 / BOM: buffer fully (rare, usually small) and decode.
	switch fileenc.DetectQuick(peek) {
	case fileenc.UTF16LE, fileenc.UTF16BE:
		rest, rerr := io.ReadAll(f)
		if rerr != nil {
			return "", fmt.Errorf("read %s: %w", path, rerr)
		}
		all := append(peek, rest...)
		return paginateText(string(fileenc.Decode(all, fileenc.DetectQuick(all))), offset, limit), nil
	case fileenc.UTF8BOM:
		body := peek
		if len(body) >= 3 {
			body = body[3:] // strip the 3-byte BOM
		}
		return scanLines(io.MultiReader(bytes.NewReader(body), f), offset, limit)
	}
	// BOM-less UTF-16: detect by NUL pattern and decode fully.
	if k, ok := fileenc.DetectUTF16NoBOM(peek); ok {
		rest, rerr := io.ReadAll(f)
		if rerr != nil {
			return "", fmt.Errorf("read %s: %w", path, rerr)
		}
		all := append(peek, rest...)
		return paginateText(string(fileenc.Decode(all, k)), offset, limit), nil
	}
	// Binary rejection (a NUL byte means .exe/.png/etc.).
	if bytes.IndexByte(peek, 0) >= 0 {
		return "", fmt.Errorf("binary file %s (NUL byte detected); use `bash hexdump` or another tool", path)
	}
	// Read a bounded sample for encoding detection, then stream the rest.
	head := peek
	if !peekEOF {
		more := make([]byte, readFileDetectSample-len(peek))
		mn, merr := io.ReadFull(f, more)
		head = append(peek, more[:mn]...)
		peekEOF = merr != nil
	}
	// Char-safe sample boundary (trim to last newline so detection doesn't end
	// mid multi-byte sequence).
	sample := head
	if !peekEOF {
		if i := bytes.LastIndexByte(head, '\n'); i >= 0 {
			sample = head[:i+1]
		}
	}
	enc, _ := fileenc.Detect(sample)
	src := io.MultiReader(bytes.NewReader(head), f)
	if dec := fileenc.Decoder(enc); dec != nil {
		return scanLines(transform.NewReader(src, dec), offset, limit)
	}
	return scanLines(src, offset, limit)
}

// scanLines reads lines from src, returns lines [offset, offset+limit) with
// 1-based numbering and a "more lines below" trailer — the streaming analogue
// of paginateText (which operates on an in-memory string). Used by streamTextFile.
func scanLines(src io.Reader, offset, limit int) (string, error) {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var collected []string
	lineNo := 0
	hasMore := false
	for scanner.Scan() {
		lineNo++
		if lineNo <= offset {
			continue
		}
		if len(collected) < limit {
			// bufio.Scanner (ScanLines) already strips trailing \r, so CRLF is
			// handled correctly without an extra normalize pass.
			collected = append(collected, scanner.Text())
			continue
		}
		hasMore = true
		break
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan: %w", err)
	}
	if lineNo == 0 {
		return "(empty file)", nil
	}
	if len(collected) == 0 {
		return fmt.Sprintf("(offset %d is past EOF — file has %d lines)", offset, lineNo), nil
	}
	maxShown := offset + len(collected)
	w := len(fmt.Sprint(maxShown))
	var b strings.Builder
	for i, line := range collected {
		fmt.Fprintf(&b, "%*d→%s\n", w, offset+i+1, line)
	}
	if hasMore {
		fmt.Fprintf(&b, "\n[more lines below; pass offset=%d to continue]\n", maxShown)
	}
	return b.String(), nil
}

// paginateText returns lines [offset, offset+limit) of content, each prefixed
// with its 1-based line number. If limit is 0 it defaults to 2000. This lets
// doc_read page past the 200k-char cap for large text documents.
func paginateText(content string, offset, limit int) string {
	if limit <= 0 {
		limit = 2000
	}
	// Normalize CRLF → LF so Windows text files don't render a stray \r at the
	// end of each numbered line (mirrors read_file's bufio.ScanLines behavior).
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	lines := strings.Split(content, "\n")
	if offset >= len(lines) {
		return fmt.Sprintf("(offset %d is past EOF — file has %d lines)", offset, len(lines))
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}
	window := lines[offset:end]
	w := len(fmt.Sprint(end))
	var b strings.Builder
	for i, line := range window {
		fmt.Fprintf(&b, "%*d→%s\n", w, offset+i+1, line)
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "\n[more lines below; pass offset=%d to continue]\n", end)
	}
	return b.String()
}

func formatCSV(data []byte) (string, error) {
	r := csv.NewReader(strings.NewReader(string(data)))
	// Read all rows without limit.
	var rows [][]string
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parse csv: %w", err)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return "(empty csv)", nil
	}
	// Column-width table for readability.
	widths := make([]int, 0)
	for _, row := range rows {
		for i, cell := range row {
			if i >= len(widths) {
				widths = append(widths, len(cell))
			} else if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	var b strings.Builder
	for _, row := range rows {
		for i, cell := range row {
			pad := widths[i] - len(cell)
			if pad < 0 {
				pad = 0
			}
			b.WriteString(cell)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", pad+2))
			}
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func formatJSON(data []byte) (string, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return "", err
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// --- doc_write --------------------------------------------------------------

type docWrite struct{ roots []string }

func (docWrite) Name() string { return "doc_write" }

func (docWrite) Description() string {
	return "Write a document to a path, creating parent dirs. Format by extension: .md/.txt/.html/code, .json, .csv, .xlsx, .docx (structured sections). Overwrites by default; set append=true to extend (.docx: insert sections; .md/.txt/.html: text append; .csv/.json: append is ignored — overwrite). If 'source' is provided for a .docx file, doc_write acts as a template filler: it reads the source template, applies find_replace, paragraph_replace, table_fill, header/footer modifications, and writes the NEW filled document to 'path' (the template is never modified). Content is capped at 5 MiB; an overwrite identical to the existing content is a no-op. Writes are crash-atomic and preserve the existing file's encoding (GBK/UTF-16/BOM) on .md/.txt/.html."
}

func (docWrite) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "source": {"type": "string", "description": "Read-only template path (.docx). If provided, acts as a template filler and writes the filled result to path."},
  "path":{"type":"string","description":"Absolute path to write (extension determines format)"},
  "content":{"description":"For .md/.txt/.html/code: a string. For .json: any JSON value. For .csv: an array of arrays of strings. For .xlsx SIMPLE form: an array of arrays of strings (rows → Sheet1). For .xlsx STRUCTURED form: an object {sheets:[{name, cells:[{ref, value, number, formula, format, style}], merges:[{range}], col_widths:[{col,width}], cond_fmt:[{range,type:'cell|data_bar|color_scale',criteria,value,format:{...}}]}], charts:[{sheet,type:'bar|line|pie|scatter',title,data_range,category_range,position}]}. Use 'number' (numeric) for cells you intend to sum with formulas."},
  "sections":{"type":"array","description":"For .docx ONLY: document blocks. Each {type:'heading'|'paragraph'|'list'|'table'|'image'|'toc' (also accepts 'para'/'text'/'ul'/'ol' aliases), text, level(1-6, default 1), items(list), ordered(list bool), headers/rows(table), image_path(PNG/JPG/GIF), image_alt, image_width(image px, default 400), image_height(image px, default 300), toc_level(1-9, default 3), style:{bold,italic,color:'#RRGGBB',size(half-pts),font,align:'left|center|right',bg,header_bg,lineSpacing,indent}}."},
  "title":{"type":"string","description":"For .docx: optional document title (rendered as H1)."},
  "append":{"type":"boolean","description":"Append mode. .docx: insert new sections into the existing document (preserves prior chapters/styles); appends to a non-existent path create a fresh doc. .md/.txt/.html: append text to the file. Other formats ignore append. Default false."},
  "find_replace": {"type": "array", "description": "SHORT field substitutions for .docx template. Each {find: \"{{name}}\", replace: \"Alice\"}.", "items": {"type": "object", "properties": {"find": {"type": "string"}, "replace": {"type": "string"}}, "required": ["find", "replace"]}},
  "paragraph_replace": {"type": "array", "description": "Replace body paragraph text by INDEX in .docx template. Each {index: N, text: \"new content\"}.", "items": {"type": "object", "properties": {"index": {"type": "integer"}, "text": {"type": "string"}}, "required": ["index", "text"]}},
  "table_fill": {"type": "array", "description": "Cell fills by index for .docx template. Each {table: 0, row: 2, col: 1, value: \"...\"}.", "items": {"type": "object", "properties": {"table": {"type": "integer"}, "row": {"type": "integer"}, "col": {"type": "integer"}, "value": {"type": "string"}, "style": {"description": "optional DocStyle"}}}, "required": ["table", "row", "col", "value"]},
  "header": {"type": "object", "description": "Replace the default header text for .docx template. {text: \"...\", align: \"left|center|right\"}.", "properties": {"text": {"type": "string"}, "align": {"type": "string"}}},
  "footer": {"type": "object", "description": "Replace the default footer text for .docx template. {text: \"...\", align: \"left|center|right\"}.", "properties": {"text": {"type": "string"}, "align": {"type": "string"}}}
},
"required":["path"]
}`)
}

func (docWrite) ReadOnly() bool { return false }

func (w docWrite) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Source           string               `json:"source"`
		Path             string               `json:"path"`
		Content          json.RawMessage      `json:"content"`
		Sections         json.RawMessage      `json:"sections"`
		Title            string               `json:"title"`
		Append           bool                 `json:"append"`
		FindReplace      []findReplacePair    `json:"find_replace"`
		TableFill        []tableFillOp        `json:"table_fill"`
		ParagraphReplaceRaw json.RawMessage      `json:"paragraph_replace"`
		Header           *headerFooterSpec    `json:"header"`
		Footer           *headerFooterSpec    `json:"footer"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	
	var paragraphReplace []paragraphReplaceOp
	var paragraphReplaceWarn string
	if len(p.ParagraphReplaceRaw) > 0 && string(p.ParagraphReplaceRaw) != "null" {
		raw := p.ParagraphReplaceRaw
		// Unwrap string-wrapped JSON — AI sometimes serialises the array as a JSON
		// string (or even double-escaped). Retry until the outer wrapper is gone.
		for len(raw) > 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
			var s string
			if err2 := json.Unmarshal(raw, &s); err2 != nil {
				break
			}
			raw = []byte(s)
		}
		if err2 := json.Unmarshal(raw, &paragraphReplace); err2 != nil {
			// Non-fatal: record warning and skip paragraph_replace so that
			// find_replace / table_fill in the same call still succeed.
			paragraphReplace = nil
			paragraphReplaceWarn = "paragraph_replace parse error (skipped): " + err2.Error()
		}
	}
	var abs string
	var err error
	var ext string
	if strings.TrimSpace(p.Path) == "" {
		if strings.TrimSpace(p.Source) != "" {
			srcAbs, err := filepath.Abs(strings.TrimSpace(p.Source))
			if err != nil {
				return "", err
			}
			abs = defaultFilledPath(srcAbs)
			ext = "docx"
		} else {
			return "", errors.New("path is required")
		}
	} else {
		abs, err = filepath.Abs(strings.TrimSpace(p.Path))
		if err != nil {
			return "", err
		}
		ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(abs), "."))
	}
	if err := confine(w.roots, abs); err != nil {
		return "", err
	}
	if ext == "docx" {
		if len(p.FindReplace) > 0 || len(p.TableFill) > 0 || len(paragraphReplace) > 0 || p.Header != nil || p.Footer != nil {
			if strings.TrimSpace(p.Source) == "" {
				return "", DocError{Code: ErrInvalidArg, Message: "source is REQUIRED when using find_replace, paragraph_replace, or table_fill on a .docx file. In-place modification is not supported; provide the original template in 'source' and the output path in 'path'."}
			}
		}
	}
	
	if ext == "docx" && strings.TrimSpace(p.Source) != "" {
		src := strings.TrimSpace(p.Source)
		srcAbs, err := filepath.Abs(src)
		if err != nil {
			return "", err
		}
		if err := confineRead(w.roots, srcAbs); err != nil {
			return "", fmt.Errorf("source: %w", err)
		}
		if filepath.Clean(srcAbs) == filepath.Clean(abs) {
			return "", DocError{Code: ErrInvalidArg,
				Message:    "source and path must differ (the template is read-only; doc_write never modifies the source)",
				Suggestion: "set path to a different filename"}
		}
		if err := checkFileExists(srcAbs); err != nil {
			return "", err
		}
		if err := guardDecompressionBomb(srcAbs); err != nil {
			return "", err
		}
		if _, statErr := os.Stat(abs); statErr == nil {
			if err := checkFileLocked(abs); err != nil {
				return "", err
			}
		}

		result, err := fillDocxTemplate(srcAbs, abs, fillJob{
			findReplace:      p.FindReplace,
			tableFill:        p.TableFill,
			paragraphReplace: paragraphReplace,
			header:           p.Header,
			footer:           p.Footer,
		})
		if err != nil {
			return "", err
		}
		out := fmt.Sprintf("wrote %s from template %s", abs, srcAbs)
		if paragraphReplaceWarn != "" {
			out += "\nwarning: " + paragraphReplaceWarn
		}
		if len(result.warnings) > 0 {
			out += "\nwarnings:"
			for _, w := range result.warnings {
				out += "\n  - " + w.Error()
			}
		}
		return out, nil
	}

	// Binary docx: structured sections → real Word document.
	if ext == "docx" {
		var sections []DocSection
		if len(p.Sections) > 0 {
			if err := json.Unmarshal(p.Sections, &sections); err != nil {
				return "", fmt.Errorf("docx sections must be an array: %w", err)
			}
		}
		if err := writeDOCX(DocInput{Path: abs, Title: p.Title, Sections: sections, Append: p.Append}); err != nil {
			return "", err
		}
		return fmt.Sprintf("wrote %s (%d sections)", abs, len(sections)), nil
	}
	// Binary xlsx: structured {sheets:[...]} object OR a simple rows array.
	if ext == "xlsx" {
		trimmed := strings.TrimSpace(string(p.Content))
		// Structured form: content is an object with "sheets" (and a "path"
		// that may be omitted since the tool's path wins).
		if len(trimmed) > 0 && trimmed[0] == '{' {
			var wb XLSXWorkbook
			if err := json.Unmarshal(p.Content, &wb); err != nil {
				return "", fmt.Errorf("xlsx structured content invalid: %w", err)
			}
			wb.Path = abs
			n, err := XLSXWriteStructured(wb)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %s (%d sheets)", abs, n), nil
		}
		// Simple form: array of arrays (rows → Sheet1).
		var rows [][]string
		if err := json.Unmarshal(p.Content, &rows); err != nil {
			return "", fmt.Errorf("xlsx content must be an array of arrays (rows) or an object with sheets: %w", err)
		}
		if err := XLSXWriteRows(abs, rows); err != nil {
			return "", err
		}
		return fmt.Sprintf("wrote %s (%d rows)", abs, len(rows)), nil
	}
	var data []byte
	switch ext {
	case "json":
		// Pretty-print JSON content.
		var v any
		if err := json.Unmarshal(p.Content, &v); err != nil {
			return "", fmt.Errorf("json content invalid: %w", err)
		}
		data, err = json.MarshalIndent(v, "", "  ")
		if err != nil {
			return "", err
		}
	case "csv":
		var rows [][]string
		if err := json.Unmarshal(p.Content, &rows); err != nil {
			// Not an array of arrays — try plain string (raw CSV text).
			var s string
			if err2 := json.Unmarshal(p.Content, &s); err2 != nil {
				return "", fmt.Errorf("csv content must be an array of arrays or a CSV string: %w", err)
			}
			r := csv.NewReader(strings.NewReader(s))
			rows, err = r.ReadAll()
			if err != nil {
				return "", fmt.Errorf("csv string parse error: %w", err)
			}
		}
		var b strings.Builder
		w := csv.NewWriter(&b)
		w.UseCRLF = true // RFC 4180 §2: records end with CRLF (Excel/WPS interop)
		_ = w.WriteAll(rows)
		if err := w.Error(); err != nil {
			return "", err
		}
		data = []byte(b.String())
	default:
		// Text: content is a string.
		var s string
		if err := json.Unmarshal(p.Content, &s); err != nil {
			return "", errors.New("content must be a string for this format")
		}
		data = []byte(s)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	// Encoding preservation applies only to free-form text formats (.md/.txt/
	// .html). .csv and .json are standards-bound: JSON MUST be UTF-8 (RFC 8259
	// §8.1) and CSV interop assumes UTF-8 or an explicit BOM, so re-encoding
	// either to GBK would corrupt non-GBK characters and emit invalid JSON.
	// For text formats we sniff the existing file's charset and re-encode on
	// write (a GBK .txt stays GBK); for csv/json we always write UTF-8.
	preserveEnc := ext == "md" || ext == "txt" || ext == "html" || ext == ""
	var enc fileenc.Kind // UTF8 (zero value) unless sniffed below
	existing := ""
	mode := "wrote"
	if p.Append && preserveEnc {
		var encErr error
		existing, enc, encErr = readFileEncoded(abs)
		if encErr != nil && !errors.Is(encErr, fs.ErrNotExist) {
			return "", fmt.Errorf("read existing for encoding: %w", encErr)
		}
		mode = "appended"
	} else if preserveEnc {
		// Overwrite of a text file: sniff existing encoding to preserve it.
		// Capture the decoded content too so the no-op check below can compare.
		var encErr error
		existing, enc, encErr = readFileEncoded(abs)
		if encErr != nil && !errors.Is(encErr, fs.ErrNotExist) {
			return "", fmt.Errorf("read existing for encoding: %w", encErr)
		}
	}
	// Compose the final UTF-8 bytes (append prepends existing content), then
	// re-encode to the sniffed charset for text formats, or leave UTF-8 for
	// csv/json.
	var utf8 []byte
	if mode == "appended" {
		utf8 = append([]byte(existing), data...)
	} else {
		utf8 = data
	}
	// C-16: no-op detection — if the (non-append) content already matches the
	// existing decoded content, skip the write so mtime/inode and upstream
	// watchers aren't disturbed. Only applies to the overwrite text path where
	// we have `existing` in hand (csv/json overwrite has no existing read).
	if mode == "wrote" && preserveEnc && existing == string(data) {
		return fmt.Sprintf("no change %s (content already matches)", abs), nil
	}
	// Pre-write syntax validation (mirrors write_file): refuse to write a .go/
	// .json file whose content has syntax errors, so the file isn't corrupted on
	// disk. .json is also checked by the json.Unmarshal branch above; this is
	// belt-and-suspenders and the single source of truth for future formats.
	if mode == "wrote" {
		if verr := validation.ValidateSyntax(abs, string(utf8)); verr != nil {
			return "", fmt.Errorf("pre-write syntax check failed — fix the error and retry; the file was NOT written: %w", verr)
		}
	}
	// C-8: cap the payload so a runaway model paste can't produce a giant file
	// (mirrors write_file's maxWriteBytes via writeFileEncoded).
	if len(utf8) > maxWriteBytes {
		return "", fmt.Errorf("content is %d bytes (limit %d); content this large should be written via a shell redirect, not a model-generated tool argument", len(utf8), maxWriteBytes)
	}
	// Write atomically (temp + fsync + rename) so a mid-write crash leaves
	// either the old or new file intact — never a torn one.
	out := utf8
	if preserveEnc {
		out = fileenc.Encode(string(utf8), enc)
	}
	if err := atomicWriteBytes(abs, out); err != nil {
		return "", err
	}
	// C-18: warn when append=true was requested for a format that doesn't
	// support append (csv/json overwrite silently).
	note := ""
	if p.Append && !preserveEnc {
		note = " (append not supported for ." + ext + "; overwrote)"
	}
	msg := fmt.Sprintf("%s %s (%d bytes)%s", mode, abs, len(data), note)
	// Post-edit hook (mirrors write_file): triggers LSP diagnostics / code
	// indexing for the path. A no-op for pure document formats (no LSP server
	// diagnoses .docx/.csv/.md), but keeps parity with write_file so a .json
	// written via doc_write gets the same downstream handling.
	if extra := runPostEditHook(ctx, abs); extra != "" {
		msg += "\n" + extra
	}
	return msg, nil
}

// --- csv_read / csv_write aliases for discoverability -----------------------
// (The agent may reach for csv_read specifically; route to doc_read/write logic.)

type csvRead struct{ roots []string }

func (csvRead) Name() string { return "csv_read" }
func (csvRead) Description() string {
	return "Read a .csv file and return it as a formatted table. Alias for doc_read on .csv, surfaced separately so it's discoverable for spreadsheet tasks. Returns up to 200k chars."
}
func (csvRead) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
}
func (csvRead) ReadOnly() bool { return true }
func (r csvRead) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return docRead{roots: r.roots}.Execute(ctx, args)
}

type csvWrite struct{ roots []string }

func (csvWrite) Name() string { return "csv_write" }
func (csvWrite) Description() string {
	return "Write rows to a .csv file. content is an array of arrays of strings (each inner array = one row). Overwrites by default; set append=true to append rows. Creates parent dirs."
}
func (csvWrite) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"description":"array of arrays of strings (rows)"},"append":{"type":"boolean"}},"required":["path","content"]}`)
}
func (csvWrite) ReadOnly() bool { return false }
func (w csvWrite) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return docWrite(w).Execute(ctx, args)
}

// --- xlsx_read / xlsx_write aliases for discoverability ---------------------
// (Binary spreadsheet access via the OOXML parser; surfaced separately so the
// agent reaches for the right tool on spreadsheet tasks.)

type xlsxRead struct{ roots []string }

func (xlsxRead) Name() string { return "xlsx_read" }
func (xlsxRead) Description() string {
	return "Read a .xlsx spreadsheet. THREE modes (pass 'mode'): (1) overview [RECOMMENDED FIRST CALL for large sheets] — returns JSON with each sheet's dimensions, column types, column names, and a 50-row sample; fast even on 300k-row files. (2) page — reads a row range [offset, offset+limit) via a streaming iterator (deep offsets are slower); good for inspecting specific records. (3) full [default if mode omitted] — reads the whole sheet as a formatted table (truncated at 200k chars; avoid for large files). For whole-table questions (sum/count/average) prefer xlsx_query over paging."
}
func (xlsxRead) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Absolute path to the .xlsx file"},
  "mode":{"type":"string","enum":["overview","page","full"],"description":"overview=shape+sample (fast, recommended first); page=offset/limit row range; full=whole sheet (default; avoid for large files)."},
  "offset":{"type":"integer","description":"0-based start row (page mode only; default 0). Deep offsets cost linear scan time.","minimum":0},
  "limit":{"type":"integer","description":"Max rows to return (page mode only; default 10000).","minimum":1},
  "sheet":{"type":"string","description":"Sheet name (default: first sheet)."}
},
"required":["path"]
}`)
}
func (xlsxRead) ReadOnly() bool { return true }
func (r xlsxRead) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path   string `json:"path"`
		Mode   string `json:"mode"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
		Sheet  string `json:"sheet"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := confineRead(r.roots, p.Path); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(strings.TrimSpace(p.Path))
	if err != nil {
		return "", err
	}
	// guardDecompressionBomb before opening any xlsx.
	if err := guardDecompressionBomb(abs); err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(p.Mode)) {
	case "overview":
		return readXLSXOverview(abs, p.Sheet)
	case "page":
		return readXLSXPage(abs, p.Sheet, p.Offset, p.Limit)
	case "", "full":
		// Backward-compatible: fall through to doc_read's xlsx behavior.
		return docRead{roots: r.roots}.Execute(ctx, args)
	default:
		return "", fmt.Errorf("unknown mode %q (use overview, page, or full)", p.Mode)
	}
}

type xlsxWrite struct{ roots []string }

func (xlsxWrite) Name() string { return "xlsx_write" }
func (xlsxWrite) Description() string {
	return "Write rows to a real .xlsx spreadsheet file. content is an array of arrays of strings (each inner array = one row). Produces a valid .xlsx (one sheet, Sheet1) openable in Excel/WPS/LibreOffice. Creates parent dirs."
}
func (xlsxWrite) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"description":"array of arrays of strings (rows)"}},"required":["path","content"]}`)
}
func (xlsxWrite) ReadOnly() bool { return false }
func (w xlsxWrite) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return docWrite(w).Execute(ctx, args)
}

// --- xlsx_query (streaming aggregation over large sheets) -------------------

type xlsxQuery struct{ roots []string }

func (xlsxQuery) Name() string { return "xlsx_query" }
func (xlsxQuery) Description() string {
	return "Aggregate a .xlsx column with a single streaming pass (constant memory; fast on 300k-row files). Use for whole-table questions (sum/avg/min/max/count/distinct_count) instead of paging through rows. Optional where[] filters rows (AND of conditions). column and where.column accept a letter (A/B) or a header name from row 1."
}
func (xlsxQuery) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Absolute path to the .xlsx file"},
  "op":{"type":"string","enum":["sum","avg","min","max","count","distinct_count"],"description":"sum/avg require a numeric column."},
  "column":{"type":"string","description":"Column letter (A/B) or header name from row 1."},
  "where":{"type":"array","description":"Filter rows (AND). Each {column, op, value}.","items":{"type":"object","properties":{"column":{"type":"string"},"op":{"type":"string","enum":["=",">","<",">=","<=","!=","contains"]},"value":{"type":"string"}},"required":["column","op","value"]}},
  "sheet":{"type":"string","description":"Sheet name (default: first sheet)."}
},
"required":["path","op","column"]
}`)
}
func (xlsxQuery) ReadOnly() bool { return true }
func (q xlsxQuery) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path   string         `json:"path"`
		Op     string         `json:"op"`
		Column string         `json:"column"`
		Where  []xlsxWhereCond `json:"where"`
		Sheet  string         `json:"sheet"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := confineRead(q.roots, p.Path); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(strings.TrimSpace(p.Path))
	if err != nil {
		return "", err
	}
	if err := guardDecompressionBomb(abs); err != nil {
		return "", err
	}
	return queryXLSX(abs, p.Sheet, strings.ToLower(strings.TrimSpace(p.Op)), p.Column, p.Where)
}

// --- doc_convert (md↔html, json pretty) -------------------------------------

type docConvert struct{ roots []string }

func (docConvert) Name() string { return "doc_convert" }

func (docConvert) Description() string {
	return "Convert a text document between formats: markdown→html, html→markdown (text), or pretty-print json. Reads from path, writes to out_path. Supported: md→html, html→md, json→json (pretty). Binary Office format conversion (docx↔pdf) is not supported — use the ppt tools for slides or export from the source app."
}

func (docConvert) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Source file path"},
  "out_path":{"type":"string","description":"Output file path (extension determines target format)"},
  "format":{"type":"string","description":"Explicit target: \"html\", \"markdown\", \"text\", \"json\". Inferred from out_path extension when omitted."}
},
"required":["path","out_path"]
}`)
}

func (docConvert) ReadOnly() bool { return false }

func (w docConvert) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path    string `json:"path"`
		OutPath string `json:"out_path"`
		Format  string `json:"format"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := confine(w.roots, p.Path); err != nil {
		return "", err
	}
	if err := confine(w.roots, p.OutPath); err != nil {
		return "", err
	}
	src, err := filepath.Abs(strings.TrimSpace(p.Path))
	if err != nil {
		return "", err
	}
	dst, err := filepath.Abs(strings.TrimSpace(p.OutPath))
	if err != nil {
		return "", err
	}
	// Read with a size cap (defense against a multi-GB source OOMing the
	// process) and encoding detection (so a GBK .md converts correctly).
	// doc_convert handles only text formats (md/html/json), so guardDecompressionBomb
	// is unnecessary — a binary Office source hits the default error below.
	info, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if info.Size() > maxDocReadBytes {
		return "", fmt.Errorf("source is %d bytes (limit %d); convert a smaller file or use bash to split it first", info.Size(), maxDocReadBytes)
	}
	content, _, err := readFileEncoded(src)
	if err != nil {
		return "", err
	}
	data := []byte(content)
	srcExt := strings.ToLower(strings.TrimPrefix(filepath.Ext(src), "."))
	target := strings.ToLower(strings.TrimSpace(p.Format))
	if target == "" {
		target = strings.ToLower(strings.TrimPrefix(filepath.Ext(dst), "."))
	}

	var out []byte
	switch {
	case (srcExt == "md" || srcExt == "markdown") && (target == "html" || target == "htm"):
		out = []byte(markdownToHTML(string(data)))
	case (srcExt == "html" || srcExt == "htm") && (target == "md" || target == "markdown"):
		out = []byte(htmlToMarkdown(string(data)))
	case (srcExt == "html" || srcExt == "htm") && target == "text":
		out = []byte(stripHTMLText(string(data)))
	case srcExt == "json" && target == "json":
		s, err := formatJSON(data)
		if err != nil {
			return "", err
		}
		out = []byte(s)
	default:
		return "", fmt.Errorf("unsupported conversion %s→%s (try md→html, html→md/text, json→json)", srcExt, target)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	// Atomic write (temp + rename) so a crash mid-write can't leave a torn
	// .html/.md/.json half-written at the destination.
	if err := atomicWriteBytes(dst, out); err != nil {
		return "", err
	}
	// Surface post-edit diagnostics (e.g. LSP on a converted .json) so the
	// edit→diagnose→fix loop stays consistent with doc_write/write_file.
	msg := fmt.Sprintf("converted %s → %s (%d bytes)", src, dst, len(out))
	if extra := runPostEditHook(ctx, dst); extra != "" {
		msg += "\n" + extra
	}
	return msg, nil
}

// markdownToHTML is a minimal converter (headings, bold, italic, code, links,
// lists, paragraphs). Not a full CommonMark parser — sufficient for rendering
// agent-produced reports into a viewable HTML file. For richer rendering use the
// frontend's Markdown component.
func markdownToHTML(md string) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html><head><meta charset=\"utf-8\"><style>body{font-family:sans-serif;max-width:760px;margin:2em auto;padding:0 1em;line-height:1.6}code{background:#f4f4f4;padding:2px 4px;border-radius:3px}pre{background:#f4f4f4;padding:1em;border-radius:6px;overflow:auto}</style></head><body>\n")
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "###### "):
			b.WriteString("<h6>" + inline(strings.TrimPrefix(t, "###### ")) + "</h6>\n")
		case strings.HasPrefix(t, "##### "):
			b.WriteString("<h5>" + inline(strings.TrimPrefix(t, "##### ")) + "</h5>\n")
		case strings.HasPrefix(t, "#### "):
			b.WriteString("<h4>" + inline(strings.TrimPrefix(t, "#### ")) + "</h4>\n")
		case strings.HasPrefix(t, "### "):
			b.WriteString("<h3>" + inline(strings.TrimPrefix(t, "### ")) + "</h3>\n")
		case strings.HasPrefix(t, "## "):
			b.WriteString("<h2>" + inline(strings.TrimPrefix(t, "## ")) + "</h2>\n")
		case strings.HasPrefix(t, "# "):
			b.WriteString("<h1>" + inline(strings.TrimPrefix(t, "# ")) + "</h1>\n")
		case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* "):
			b.WriteString("<li>" + inline(strings.TrimPrefix(strings.TrimPrefix(t, "- "), "* ")) + "</li>\n")
		case t == "":
			b.WriteString("<br>\n")
		default:
			b.WriteString("<p>" + inline(t) + "</p>\n")
		}
	}
	b.WriteString("</body></html>")
	return b.String()
}

// inline handles **bold**, *italic*, `code`.
func inline(s string) string {
	out := s
	// Bold: **x** → <strong>x</strong>
	for {
		i := strings.Index(out, "**")
		if i < 0 {
			break
		}
		j := strings.Index(out[i+2:], "**")
		if j < 0 {
			break
		}
		inner := out[i+2 : i+2+j]
		out = out[:i] + "<strong>" + inner + "</strong>" + out[i+2+j+2:]
	}
	out = wrapPairs(out, "`", "<code>", "</code>")
	out = wrapPairs(out, "*", "<em>", "</em>")
	return out
}

// wrapPairs replaces pairs of delim with open/close tags.
func wrapPairs(s, delim, openTag, closeTag string) string {
	var b strings.Builder
	isOpen := true
	for {
		i := strings.Index(s, delim)
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		if isOpen {
			b.WriteString(openTag)
		} else {
			b.WriteString(closeTag)
		}
		s = s[i+len(delim):]
		isOpen = !isOpen
	}
	return b.String()
}

// stripHTMLText is a local minimal tag stripper (the rag package has its own).
func stripHTMLText(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// htmlToMarkdown converts a (well-enough-formed) HTML fragment to Markdown,
// handling the tag set markdownToHTML emits (headings, bold/italic, code, links,
// lists, line breaks). It is not a full CommonMark parser — sufficient for
// round-tripping agent-produced HTML reports into editable Markdown. Block tags
// (headings, paragraphs, list items, br) insert newlines so the output reads as
// separate lines rather than one run.
func htmlToMarkdown(s string) string {
	// Drop the head/style/script blocks entirely (their text content isn't prose).
	s = stripHTMLBlock(s, "head")
	s = stripHTMLBlock(s, "style")
	s = stripHTMLBlock(s, "script")
	// Block-level conversions first (so they emit newlines), then inline.
	// Headings: <h1>..</h1> → "# text", up to h6.
	for lvl := 6; lvl >= 1; lvl-- {
		s = replaceTag(s, "h"+strconv.Itoa(lvl), strings.Repeat("#", lvl)+" ")
	}
	// Links: <a href="URL">text</a> → "[text](URL)".
	s = linkRE.ReplaceAllString(s, "[$2]($1)")
	// List items: <li>text</li> → "- text\n" (unordered) — ordered lists get
	// "1." prefixes; we don't track numbering across items, so "- " is used for
	// both (Markdown renderers renumber). ul/ol wrappers just unwrap.
	s = liRE.ReplaceAllString(s, "\n- $1")
	s = unwrapTag(s, "ul")
	s = unwrapTag(s, "ol")
	// Inline emphasis.
	s = replaceTag(s, "strong", "**")
	s = replaceTag(s, "b", "**")
	s = replaceTag(s, "em", "*")
	s = replaceTag(s, "i", "*")
	s = replaceTag(s, "code", "`")
	// Block breaks.
	s = brRE.ReplaceAllString(s, "\n")
	s = replaceTagBlock(s, "p", "\n")
	s = replaceTagBlock(s, "div", "\n")
	// Strip any remaining tags (keep their inner text).
	s = anyTagRE.ReplaceAllString(s, "")
	// Decode the common HTML entities markdownToHTML emits.
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	// Collapse 3+ newlines to 2 (paragraph spacing) and trim each line.
	lines := strings.Split(s, "\n")
	var out []string
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}

// stripHTMLBlock removes <tag>...</tag> (case-insensitive, non-greedy) entirely.
func stripHTMLBlock(s, tag string) string {
	re := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>.*?</` + tag + `>`)
	return re.ReplaceAllString(s, "")
}

// replaceTag converts <tag>...</tag> to prefix+inner+prefix (symmetric wrap),
// preserving the inner content. Used for inline tags like <strong>→**.
func replaceTag(s, tag, marker string) string {
	open := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>`)
	close := regexp.MustCompile(`(?is)</` + tag + `>`)
	s = open.ReplaceAllString(s, marker)
	s = close.ReplaceAllString(s, marker)
	return s
}

// replaceTagBlock converts <tag>...</tag> to newline+inner+newline (block).
func replaceTagBlock(s, tag, marker string) string {
	open := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>`)
	close := regexp.MustCompile(`(?is)</` + tag + `>`)
	s = open.ReplaceAllString(s, marker)
	s = close.ReplaceAllString(s, marker)
	return s
}

// unwrapTag drops the open/close tags but keeps inner content (no marker).
func unwrapTag(s, tag string) string {
	open := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>`)
	close := regexp.MustCompile(`(?is)</` + tag + `>`)
	s = open.ReplaceAllString(s, "")
	s = close.ReplaceAllString(s, "")
	return s
}

// Pre-compiled regexes for htmlToMarkdown.
var (
	linkRE   = regexp.MustCompile(`(?is)<a\b[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	liRE     = regexp.MustCompile(`(?is)<li\b[^>]*>(.*?)</li>`)
	brRE     = regexp.MustCompile(`(?is)<br\s*/?>`)
	anyTagRE = regexp.MustCompile(`(?is)<[^>]+>`)
)
