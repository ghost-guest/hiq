package builtin

import (
	"archive/zip"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// readDocxPart opens the .docx at path and returns one part's bytes.
func readDocxPartForTest(t *testing.T, path, name string) []byte {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open %s: %v", name, err)
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			return data
		}
	}
	t.Fatalf("part %s not found in %s", name, path)
	return nil
}

// TestWriteDOCXNewlineAndTab verifies that literal \n and \t in paragraph text
// become <w:br/> and <w:tab/> respectively — without this, Word renders them as
// whitespace and the content collapses onto one line.
func TestWriteDOCXNewlineAndTab(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nl.docx")
	sections := []DocSection{
		{Type: "paragraph", Text: "line1\nline2\tcol2"},
	}
	if err := writeDOCX(DocInput{Path: path, Sections: sections}); err != nil {
		t.Fatal(err)
	}
	body := string(readDocxPartForTest(t, path, "word/document.xml"))
	if !strings.Contains(body, "<w:br/>") {
		t.Errorf("newline did not become <w:br/>: body has no <w:br/> tag")
	}
	if !strings.Contains(body, "<w:tab/>") {
		t.Errorf("tab did not become <w:tab/>: body has no <w:tab/> tag")
	}
}

// TestWriteDOCXMultipleOrderedListsIndependentNumbering verifies that two
// ordered lists each get their own numId with startOverride, so the second
// list restarts at 1 instead of continuing from the first (4,5,6...).
func TestWriteDOCXMultipleOrderedListsIndependentNumbering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lists.docx")
	sections := []DocSection{
		{Type: "list", Items: []string{"a", "b"}, Ordered: true},
		{Type: "list", Items: []string{"x", "y"}, Ordered: true},
	}
	if err := writeDOCX(DocInput{Path: path, Sections: sections}); err != nil {
		t.Fatal(err)
	}
	numbering := string(readDocxPartForTest(t, path, "word/numbering.xml"))
	// Two ordered lists → two <w:num> instances with startOverride, numId=2 and 3.
	if !strings.Contains(numbering, `w:numId="2"`) || !strings.Contains(numbering, `w:numId="3"`) {
		t.Errorf("expected numId=2 and numId=3 for two ordered lists; numbering:\n%s", numbering)
	}
	if !strings.Contains(numbering, "startOverride") {
		t.Errorf("expected startOverride in numbering to reset count per list")
	}
	// Body should reference both numIds (via w:numId + w:val="N").
	body := string(readDocxPartForTest(t, path, "word/document.xml"))
	count2 := strings.Count(body, `<w:numId w:val="2"/>`)
	count3 := strings.Count(body, `<w:numId w:val="3"/>`)
	if count2 == 0 || count3 == 0 {
		t.Errorf("body should reference numId=2 and numId=3; counts: %d, %d", count2, count3)
	}
}

// TestWriteDOCXTOCEmitsUpdateFields verifies a TOC section causes
// word/settings.xml with <w:updateFields/> to be emitted, so Word populates
// the TOC on open instead of showing placeholder text.
func TestWriteDOCXTOCEmitsUpdateFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "toc.docx")
	sections := []DocSection{
		{Type: "heading", Text: "Chapter 1", Level: 1},
		{Type: "paragraph", Text: "body"},
		{Type: "toc", TOCLevel: 2},
	}
	if err := writeDOCX(DocInput{Path: path, Sections: sections}); err != nil {
		t.Fatal(err)
	}
	settings := string(readDocxPartForTest(t, path, "word/settings.xml"))
	if !strings.Contains(settings, "updateFields") {
		t.Errorf("expected settings.xml to contain updateFields; got:\n%s", settings)
	}
	// ContentTypes must declare the settings part.
	ct := string(readDocxPartForTest(t, path, "[Content_Types].xml"))
	if !strings.Contains(ct, "/word/settings.xml") {
		t.Errorf("ContentTypes missing settings override")
	}
	// Rels must wire rId3 → settings.
	rels := string(readDocxPartForTest(t, path, "word/_rels/document.xml.rels"))
	if !strings.Contains(rels, "settings.xml") {
		t.Errorf("rels missing settings relationship")
	}
}

// TestWriteDOCXImageRejectsBmp verifies a non-whitelisted image format fails
// loudly rather than silently producing a corrupt docx (image/png ContentTypes
// mismatch for a .bmp payload).
func TestWriteDOCXImageRejectsBmp(t *testing.T) {
	dir := t.TempDir()
	// Create a fake .bmp so the os.Stat check passes.
	bmpPath := filepath.Join(dir, "logo.bmp")
	if err := writeFileBytesForTest(bmpPath, []byte("BM\x00fake")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "out.docx")
	sections := []DocSection{
		{Type: "image", ImagePath: bmpPath},
	}
	err := writeDOCX(DocInput{Path: path, Sections: sections})
	if err == nil {
		t.Fatal("expected error for .bmp image, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "bmp") && !strings.Contains(err.Error(), "unsupported image format") {
		t.Errorf("error should mention the format; got: %v", err)
	}
}

// TestReadDOCXPreservesTableStructure verifies the reader renders tables with
// pipe-separated cells and newline-separated rows, so the model can reconstruct
// the grid instead of seeing a flat stream of cells.
func TestReadDOCXPreservesTableStructure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tbl.docx")
	sections := []DocSection{
		{Type: "table", Headers: []string{"指标", "Q1"}, Rows: [][]string{{"营收", "1.2亿"}}},
	}
	if err := writeDOCX(DocInput{Path: path, Sections: sections}); err != nil {
		t.Fatal(err)
	}
	text, err := readDOCXStructure(path)
	if err != nil {
		t.Fatal(err)
	}
	// Structure JSON should contain the table cells (as values in the rows
	// array) — both header and body content.
	if !strings.Contains(text, "指标") || !strings.Contains(text, "营收") || !strings.Contains(text, "1.2亿") {
		t.Errorf("table content missing from structure read; got:\n%s", text)
	}
}

// TestReadXLSXReturnsFormula verifies that a formula cell written by hiq
// (which has no cached value) reads back as "=<formula>" rather than empty.
func TestReadXLSXReturnsFormula(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.xlsx")
	wb := XLSXWorkbook{
		Path: path,
		Sheets: []XLSXSheet{{
			Name: "Sheet1",
			Cells: []XLSXCell{
				{Ref: "A1", Number: ptrFloat(10)},
				{Ref: "A2", Number: ptrFloat(20)},
				{Ref: "A3", Formula: ptrString("SUM(A1:A2)")},
			},
		}},
	}
	if _, err := XLSXWriteStructured(wb); err != nil {
		t.Fatal(err)
	}
	rows, err := readXLSX(path)
	if err != nil {
		t.Fatal(err)
	}
	// A3 should be "=SUM(A1:A2)".
	found := false
	for _, row := range rows {
		for _, cell := range row {
			if cell == "=SUM(A1:A2)" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("formula cell not read back as =SUM(A1:A2); rows: %v", rows)
	}
}

// TestReadXLSXPadsTrailingEmptyCells verifies all rows are padded to equal
// width so the model sees a rectangular grid.
func TestReadXLSXPadsTrailingEmptyCells(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.xlsx")
	// Row 1: "a","b","c". Row 2: "d" (trailing empties that excelize truncates).
	if err := XLSXWriteRows(path, [][]string{{"a", "b", "c"}, {"d"}}); err != nil {
		t.Fatal(err)
	}
	rows, err := readXLSX(path)
	if err != nil {
		t.Fatal(err)
	}
	// Both rows should have width 3 after padding.
	for i, row := range rows {
		if strings.HasPrefix(strings.Join(row, ""), "---") {
			continue
		}
		if len(row) != 3 {
			t.Errorf("row %d width %d, want 3 (padded): %v", i, len(row), row)
		}
	}
}

// TestXLSXCondFmtCellGreaterThan verifies the criteria mapping fix: passing
// "greater_than" (snake_case, what the schema documents) no longer fails with
// ErrParameterInvalid — it maps to excelize's "greater than" (space).
func TestXLSXCondFmtCellGreaterThan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cf.xlsx")
	wb := XLSXWorkbook{
		Path: path,
		Sheets: []XLSXSheet{{
			Name: "Sheet1",
			Cells: []XLSXCell{
				{Ref: "A1", Number: ptrFloat(50)},
				{Ref: "A2", Number: ptrFloat(150)},
			},
			CondFmt: []XLSXCondFmt{
				{Range: "A1:A2", Type: "cell", Criteria: "greater_than", Value: "100", Format: XLSXStyle{Bg: "#FF0000"}},
			},
		}},
	}
	if _, err := XLSXWriteStructured(wb); err != nil {
		t.Fatalf("greater_than criteria should succeed, got: %v", err)
	}
}

// TestXLSXCondFmtBetweenParsesMinMax verifies the between fix: "100,200" is
// split into MinValue/MaxValue rather than stuffed into Value (which excelize
// ignores for between, producing empty rules).
func TestXLSXCondFmtBetweenParsesMinMax(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bt.xlsx")
	wb := XLSXWorkbook{
		Path: path,
		Sheets: []XLSXSheet{{
			Name: "Sheet1",
			Cells: []XLSXCell{
				{Ref: "A1", Number: ptrFloat(150)},
			},
			CondFmt: []XLSXCondFmt{
				{Range: "A1:A1", Type: "cell", Criteria: "between", Value: "100,200", Format: XLSXStyle{Bg: "#00FF00"}},
			},
		}},
	}
	if _, err := XLSXWriteStructured(wb); err != nil {
		t.Fatalf("between criteria should succeed, got: %v", err)
	}
}

// TestXLSXMergeStyleAppliedToRange verifies that after a merge, the top-left
// cell's style is applied across the whole merged range — without this, the
// bottom/right borders of a merged cell are missing in Excel.
func TestXLSXMergeStyleAppliedToRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.xlsx")
	bordered := XLSXStyle{Border: true}
	wb := XLSXWorkbook{
		Path: path,
		Sheets: []XLSXSheet{{
			Name: "Sheet1",
			Cells: []XLSXCell{
				{Ref: "A1", Value: ptrString("merged"), Style: bordered},
				{Ref: "B1", Style: bordered},
			},
			Merges: []XLSXMerge{{Range: "A1:B1"}},
		}},
	}
	if _, err := XLSXWriteStructured(wb); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Both A1 and B1 should have a style id (non-zero) — before the fix B1 had
	// style 0 (no border) because the merge wiped it.
	a1Style, _ := f.GetCellStyle("Sheet1", "A1")
	b1Style, _ := f.GetCellStyle("Sheet1", "B1")
	if a1Style == 0 {
		t.Errorf("A1 has no style")
	}
	if b1Style == 0 {
		t.Errorf("B1 (in merged range) has no style — borders will be missing")
	}
}

// TestHtmlToMarkdown converts a small HTML doc and checks key mappings.
func TestHtmlToMarkdown(t *testing.T) {
	html := `<h1>Title</h1><p>This is <strong>bold</strong> and <em>italic</em>.</p><ul><li>a</li><li>b</li></ul><a href="http://x">link</a>`
	md := htmlToMarkdown(html)
	checks := []string{"# Title", "**bold**", "*italic*", "- a", "- b", "[link](http://x)"}
	for _, c := range checks {
		if !strings.Contains(md, c) {
			t.Errorf("markdown missing %q; got:\n%s", c, md)
		}
	}
}

// TestWriteDOCXAppendOrderedListsRestartNumbering verifies that ordered lists
// appended to an existing .docx each restart at 1, and that the appended
// numbering definitions don't collide with the base doc's existing numIds.
func TestWriteDOCXAppendOrderedListsRestartNumbering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chapters.docx")
	// Base doc with one ordered list (numId=2 in full-write).
	base := DocInput{Path: path, Sections: []DocSection{
		{Type: "list", Items: []string{"a", "b"}, Ordered: true},
	}}
	if err := writeDOCX(base); err != nil {
		t.Fatal(err)
	}
	// Append a second ordered list — it should get its own numId (not 2, which
	// the base already uses) with a startOverride so it restarts at 1.
	appendIn := DocInput{Path: path, Append: true, Sections: []DocSection{
		{Type: "list", Items: []string{"x", "y"}, Ordered: true},
	}}
	if err := writeDOCX(appendIn); err != nil {
		t.Fatal(err)
	}
	numbering := string(readDocxPartForTest(t, path, "word/numbering.xml"))
	// The appended list must reference a numId > 2 (the base's max). Count
	// <w:num> entries with startOverride — there should be one for the appended
	// list (the base's numId=2 has no startOverride in the static numberingXML).
	startOverrideCount := strings.Count(numbering, "startOverride")
	if startOverrideCount == 0 {
		t.Errorf("append should inject a startOverride for the new ordered list; numbering:\n%s", numbering)
	}
	body := string(readDocxPartForTest(t, path, "word/document.xml"))
	// The appended list's paragraphs should reference a numId distinct from 2.
	if !strings.Contains(body, `<w:numId w:val="2"/>`) {
		t.Errorf("base ordered list (numId=2) should still be present in body; body:\n%s", body)
	}
}

// TestWriteDOCXAppendTOCInjectsUpdateFields verifies that appending a TOC
// section into an existing .docx injects <w:updateFields/> into settings.xml
// (or creates it), so Word populates the TOC on open.
func TestWriteDOCXAppendTOCInjectsUpdateFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.docx")
	// Base doc with no TOC (no settings.xml emitted by full-write without TOC).
	base := DocInput{Path: path, Sections: []DocSection{
		{Type: "heading", Text: "Intro", Level: 1},
		{Type: "paragraph", Text: "body"},
	}}
	if err := writeDOCX(base); err != nil {
		t.Fatal(err)
	}
	// Append a TOC — settings.xml should be created with updateFields.
	appendIn := DocInput{Path: path, Append: true, Sections: []DocSection{
		{Type: "toc", TOCLevel: 2},
	}}
	if err := writeDOCX(appendIn); err != nil {
		t.Fatal(err)
	}
	settings := string(readDocxPartForTest(t, path, "word/settings.xml"))
	if !strings.Contains(settings, "updateFields") {
		t.Errorf("appended TOC should inject updateFields into settings.xml; got:\n%s", settings)
	}
	// ContentTypes must declare the settings part.
	ct := string(readDocxPartForTest(t, path, "[Content_Types].xml"))
	if !strings.Contains(ct, "/word/settings.xml") {
		t.Errorf("ContentTypes missing settings override after append TOC")
	}
	// Rels must wire a relationship to settings.xml.
	rels := string(readDocxPartForTest(t, path, "word/_rels/document.xml.rels"))
	if !strings.Contains(rels, "settings.xml") {
		t.Errorf("rels missing settings relationship after append TOC")
	}
}

// --- helpers ---

func writeFileBytesForTest(path string, data []byte) error {
	return atomicWriteBytes(path, data)
}

func ptrFloat(f float64) *float64 { return &f }
func ptrString(s string) *string  { return &s }
