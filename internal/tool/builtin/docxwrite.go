package builtin

// docxwrite.go generates .docx files from a structured JSON description
// (sections → headings/paragraphs/tables/lists with styles), compiling each to
// OOXML and packaging into the standard .docx zip. Pure stdlib (archive/zip +
// encoding/xml via text templates) — no external docx library, mirroring how
// readDOCX parses with the same stdlib tools.
//
// The document model is intentionally small but covers the office-report 80%:
// headings (H1-H3), paragraphs, bulleted/ordered lists, and tables (with an
// optional styled header row). Run-level styles (bold/italic/color/size/font)
// are honored via <w:rPr>. The agent describes WHAT the doc contains; this
// builder compiles the HOW (OOXML), the same split as ppt_create/xlsx_write.

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// DocSection is one block of the document. Type selects the renderer; the
// shared Style applies to text runs within the section where relevant.
type DocSection struct {
	Type    string     `json:"type"`    // "heading"|"paragraph"|"list"|"table"|"image"|"toc"
	Level   int        `json:"level"`   // heading level (1-6, default 1)
	Text    string     `json:"text"`    // heading/paragraph text; list single item (when Items empty)
	Items   []string   `json:"items"`   // list items (type=list)
	Ordered bool       `json:"ordered"` // list ordered? (type=list)
	Headers []string   `json:"headers"` // table header cells (type=table)
	Rows    [][]string `json:"rows"`    // table body rows (type=table)
	Style   DocStyle   `json:"style"`   // run styling (bold/italic/color/size/font/align)

	// Image fields (type=image). Supported: PNG/JPG/GIF. SVG is NOT supported
	// (Word needs a PNG raster fallback + asvg extension; convert SVG to PNG
	// first — writeDOCX returns an explicit error for .svg paths).
	ImagePath   string `json:"image_path,omitempty"`   // path to image file (PNG/JPG/GIF)
	ImageAlt    string `json:"image_alt,omitempty"`    // alt text for accessibility (→ wp:docPr descr)
	ImageWidth  int    `json:"image_width,omitempty"`  // image width in pixels (0 = default 400)
	ImageHeight int    `json:"image_height,omitempty"` // image height in pixels (0 = default 300)

	// TOC fields (type=toc)
	TOCLevel int `json:"toc_level,omitempty"` // depth of TOC (heading levels 1-N; default 3)
}

// DocStyle is the shared run/paragraph style vocabulary. Color is "#RRGGBB".
type DocStyle struct {
	Bold        bool    `json:"bold"`
	Italic      bool    `json:"italic"`
	Color       string  `json:"color"`       // "#RRGGBB"
	Size        int     `json:"size"`        // half-points (24 = 12pt); 0 = default
	Font        string  `json:"font"`        // font family; "" = default
	Align       string  `json:"align"`       // "left"|"center"|"right" (paragraph-level)
	Bg          string  `json:"bg"`          // table cell shading "#RRGGBB"
	LineSpacing float64 `json:"lineSpacing"` // line spacing multiplier (1.5 = 1.5×); 0 = default
	Indent      int     `json:"indent"`      // first-line indent in characters; 0 = none
	HeaderBg    string  `json:"header_bg"`   // table header row shading "#RRGGBB"
}

// DocInput is the top-level payload for writeDOCX.
type DocInput struct {
	Path     string       `json:"path"`
	Title    string       `json:"title"` // optional document title (rendered as H1 if non-empty)
	Sections []DocSection `json:"sections"`
	Append   bool         `json:"append,omitempty"` // when true, insert sections into existing docx
}

// writeDOCX compiles a DocInput into a valid .docx at the given path. The zip
// contains the minimum OOXML parts Word/WPS/LibreOffice require:
// [Content_Types].xml, _rels/.rels, word/document.xml, word/_rels/document.xml.rels,
// word/styles.xml. Produces a file openable in any conformant reader.
//
// When in.Append is true and the file already exists, new sections are inserted
// before </w:body> in the existing document.xml. Append preserves the existing
// package: all non-overwritten parts (media, headers/footers, numbering, rels,
// content types) are copied verbatim from the original, and new images get rIds
// continuing past the original's maximum so existing image references stay valid.
func writeDOCX(in DocInput) error {
	if err := os.MkdirAll(filepath.Dir(in.Path), 0o755); err != nil {
		return err
	}

	// Collect & validate images up-front (before any file is created). Done
	// first because both append and full-write paths need the list, and a
	// missing/unsupported image must error out with no partial .docx on disk.
	// usedNames also de-duplicates media filenames (two logo.png in different
	// dirs would collide in word/media/).
	var images []struct{ name, path string }
	usedNames := make(map[string]int)
	for _, s := range in.Sections {
		if s.Type != "image" || strings.TrimSpace(s.ImagePath) == "" {
			continue
		}
		// Whitelist the formats addImageContentTypes knows a MIME type for;
		// anything else (bmp/tiff/webp/svg/…) would be stored with a wrong
		// content-type and corrupt the package, so reject it up front.
		ext := strings.ToLower(filepath.Ext(s.ImagePath))
		switch ext {
		case ".png", ".jpg", ".jpeg", ".gif":
			// supported
		case ".svg":
			return fmt.Errorf("SVG image not supported (Word needs a PNG raster fallback); convert to PNG first: %s", s.ImagePath)
		default:
			return fmt.Errorf("unsupported image format %q (png/jpg/jpeg/gif only): %s", ext, s.ImagePath)
		}
		// Size cap: addImageToZip reads the whole image into memory, so a
		// multi-GB referenced image would OOM. Stat is cheap and gates it.
		info, err := os.Stat(s.ImagePath)
		if err != nil {
			return fmt.Errorf("image not readable: %w", err)
		}
		if info.Size() > maxImageBytes {
			return fmt.Errorf("image %s is %d bytes (limit %d); downscale it before embedding", s.ImagePath, info.Size(), maxImageBytes)
		}
		base := filepath.Base(s.ImagePath)
		name := base
		if n, dup := usedNames[base]; dup {
			stem := strings.TrimSuffix(base, ext)
			name = fmt.Sprintf("%s_%d%s", stem, n+1, ext)
			usedNames[base] = n + 1
		} else {
			usedNames[base] = 1
		}
		images = append(images, struct{ name, path string }{name: name, path: s.ImagePath})
	}

	// Append mode: try to extend an existing package. If the file doesn't exist
	// (the common "first chapter" case), fall through to a full write.
	if in.Append {
		if _, statErr := os.Stat(in.Path); statErr == nil {
			return writeDOCXAppend(in, images)
		}
		// File missing → degrade to a full write below.
	}
	return writeDOCXFull(in, images)
}

// writeDOCXFull builds a complete .docx from scratch. Image rIds start at 100
// (continuing past the static rels' rId1/rId2), so this is only correct when
// the package contains no other relationships.
func writeDOCXFull(in DocInput, images []struct{ name, path string }) error {
	xmlBody := buildDocumentXML(in)
	styles := defaultStylesXML()
	numbering := buildNumberingXML(countOrderedLists(in)) // C-3: per-list numId

	// C-4: a TOC section needs <w:updateFields/> in word/settings.xml so Word
	// populates the TOC on open instead of leaving placeholder text.
	hasTOC := false
	for _, s := range in.Sections {
		if strings.EqualFold(strings.TrimSpace(s.Type), "toc") {
			hasTOC = true
			break
		}
	}

	// Build content types (add image types if needed)
	ctXML := contentTypesXML
	if len(images) > 0 {
		ctXML = addImageContentTypes(ctXML, images)
	}
	// Register settings.xml override when we emit it (TOC case).
	if hasTOC {
		ctXML = strings.Replace(ctXML, "</Types>",
			`<Override PartName="/word/settings.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.settings+xml"/></Types>`, 1)
	}

	// Build document rels (add image relationships if needed)
	relsXML := documentRelsXML
	if len(images) > 0 {
		relsXML = addImageRels(relsXML, images, 100)
	}
	// Wire rId3 → settings.xml (rId1/rId2 are styles/numbering; images start at 100).
	if hasTOC {
		relsXML = strings.Replace(relsXML, "</Relationships>",
			`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/settings" Target="settings.xml"/></Relationships>`, 1)
	}

	parts := []struct{ name, body string }{
		{"[Content_Types].xml", ctXML},
		{"_rels/.rels", rootRelsXML},
		{"word/_rels/document.xml.rels", relsXML},
		{"word/styles.xml", styles},
		{"word/numbering.xml", numbering},
		{"word/document.xml", xmlBody},
	}
	if hasTOC {
		parts = append(parts, struct{ name, body string }{
			"word/settings.xml",
			`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
				`<w:settings xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
				`<w:updateFields w:val="true"/></w:settings>`,
		})
	}

	// Write to a temp file, then atomically rename over the target so a crash
	// mid-write leaves the prior file (or no file on first write) intact rather
	// than a truncated, unopenable .docx.
	return atomicWrite(in.Path, func(f *os.File) error {
		zw := zip.NewWriter(f)
		for _, p := range parts {
			w, err := zw.Create(p.name)
			if err != nil {
				return err
			}
			if _, err := w.Write([]byte(p.body)); err != nil {
				return err
			}
		}
		// Add image files to the zip
		for _, img := range images {
			if err := addImageToZip(zw, img); err != nil {
				return err
			}
		}
		// Close flushes the zip's central directory to f; do it inside the
		// callback so an error aborts the rename (atomicWrite cleans up temp).
		return zw.Close()
	})
}

// writeDOCXAppend inserts new sections into an existing .docx while preserving
// the rest of the package. It copies every original part verbatim except:
//   - word/document.xml (new sections spliced before </w:body>)
//   - word/_rels/document.xml.rels (original + new image relationships)
//   - [Content_Types].xml (original + new image MIME types)
//
// New image rIds continue past the original's maximum rId so existing image
// references (and any header/footer/hyperlink relationships) stay intact.
func writeDOCXAppend(in DocInput, images []struct{ name, path string }) error {
	// Guard the existing package against a decompression bomb before opening
	// it — the append reads every part (copyZipPart io.Copy's each entry, and
	// readDocxPartFromReader io.ReadAll's the modified ones). Without this a
	// hostile/corrupted .docx passed as the append target OOMs the process.
	// Mirrors the doc_write template-fill read guard and doc_read's binary-branch guard.
	if err := guardDecompressionBomb(in.Path); err != nil {
		return err
	}
	// buildTemp reads the existing package, splices in new sections, copies all
	// unchanged parts, and writes the result to a temp file. It returns the
	// temp path. The original zip reader is closed (via defer) BEFORE this
	// returns, so the caller's rename onto the original path isn't blocked by
	// an open file handle (a Windows-specific failure).
	buildTemp := func() (string, error) {
		rz, err := zip.OpenReader(in.Path)
		if err != nil {
			return "", fmt.Errorf("append: open existing docx: %w", err)
		}
		defer rz.Close()

		// Read the parts we must modify.
		existingDoc, err := readDocxPartFromReader(rz, "word/document.xml")
		if err != nil {
			return "", fmt.Errorf("append: read existing document.xml: %w", err)
		}
		existingStyles, sErr := readDocxPartFromReader(rz, "word/styles.xml")
		var styles string
		if sErr != nil {
			// styles.xml missing is non-fatal (use defaults), but a corrupt-but-
			// present file must surface rather than silently swapping styling.
			if !errors.Is(sErr, fs.ErrNotExist) {
				return "", fmt.Errorf("append: read existing styles.xml: %w", sErr)
			}
			styles = defaultStylesXML()
		} else {
			styles = existingStyles
		}
		existingRels, relErr := readDocxPartFromReader(rz, "word/_rels/document.xml.rels")
		existingCT, ctErr := readDocxPartFromReader(rz, "[Content_Types].xml")
		existingNumbering, _ := readDocxPartFromReader(rz, "word/numbering.xml")
		existingSettings, setErr := readDocxPartFromReader(rz, "word/settings.xml")

		// New image rIds continue past the original's max rId so existing image
		// / header / footer relationships stay valid. Computed BEFORE splicing
		// so the new fragments can be remapped in isolation (remapping the merged
		// body would also rewrite the original image's rIds).
		relsBase := nextRIdBase(existingRels, relErr)

		// Appended ordered lists need their own numIds that don't collide with
		// the base doc's existing numbering definitions. numBase is the highest
		// existing numId; buildSectionsXMLWithBase then allocates numBase+1,
		// numBase+2, … per appended ordered list (each restarting at 1).
		numBase := maxNumIdInNumbering(existingNumbering)
		orderedCount := countOrderedLists(in)

		// Build the new section fragments and remap their rIds to the append
		// base BEFORE splicing. renderImage always emits rId100+i (the full-write
		// base); here we shift each new image's embed to relsBase+i so it points
		// at the relationship added below. Done on the fragment alone so the
		// original body's rIds are untouched.
		newFragments := buildSectionsXMLWithBase(in, numBase)
		if relsBase != 100 && len(images) > 0 {
			for i := len(images) - 1; i >= 0; i-- {
				old := fmt.Sprintf(`r:embed="rId%d"`, 100+i)
				newAttr := fmt.Sprintf(`r:embed="rId%d"`, relsBase+i)
				newFragments = strings.ReplaceAll(newFragments, old, newAttr)
			}
		}
		xmlBody := strings.Replace(existingDoc, "</w:body>", newFragments+"</w:body>", 1)

		// Merge new numbering definitions (one per appended ordered list, each
		// with a startOverride) into the existing numbering.xml so the appended
		// lists render with independent counters.
		numberingXML := mergeNumberingForAppend(existingNumbering, numBase, orderedCount)

		// TOC handling: if the appended sections include a TOC, ensure
		// word/settings.xml carries <w:updateFields/> so Word populates it on
		// open. Inject into the existing settings if present, else create a
		// minimal settings part. Also wire its content-type override + rel.
		hasAppendTOC := false
		for _, s := range in.Sections {
			if strings.EqualFold(strings.TrimSpace(s.Type), "toc") {
				hasAppendTOC = true
				break
			}
		}
		settingsXML := ""
		needsSettingsPart := false
		if hasAppendTOC {
			settingsXML = injectUpdateFields(existingSettings, setErr)
			needsSettingsPart = true
		}

		// Merged rels / content-types; fall back to static defaults when a part
		// is absent (hand-authored minimal docx) so the package stays valid.
		relsXML := existingRels
		if relErr != nil {
			relsXML = documentRelsXML
		}
		if len(images) > 0 {
			relsXML = addImageRels(relsXML, images, relsBase)
		}
		ctXML := existingCT
		if ctErr != nil {
			ctXML = contentTypesXML
		}
		if len(images) > 0 {
			ctXML = addImageContentTypes(ctXML, images)
		}
		// If we're emitting/overwriting settings.xml for a TOC, register its
		// content-type override (idempotent — a base doc with settings already
		// has it) and its relationship (use the next free rId past images).
		if needsSettingsPart {
			if !strings.Contains(ctXML, "/word/settings.xml") {
				ctXML = strings.Replace(ctXML, "</Types>",
					`<Override PartName="/word/settings.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.settings+xml"/></Types>`, 1)
			}
			if !strings.Contains(relsXML, `Target="settings.xml"`) {
				settingsRId := relsBase // images occupy relsBase..relsBase+len-1
				if len(images) > 0 {
					settingsRId = relsBase + len(images)
				}
				relsXML = strings.Replace(relsXML, "</Relationships>",
					fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/settings" Target="settings.xml"/></Relationships>`, settingsRId), 1)
			}
		}

		// De-dup new image basenames against existing media entries.
		origMedia := make(map[string]bool)
		for _, f := range rz.File {
			if strings.HasPrefix(f.Name, "word/media/") {
				origMedia[filepath.Base(f.Name)] = true
			}
		}
		for i := range images {
			if origMedia[images[i].name] {
				ext := filepath.Ext(images[i].name)
				stem := strings.TrimSuffix(images[i].name, ext)
				for n := 2; ; n++ {
					cand := fmt.Sprintf("%s_%d%s", stem, n, ext)
					if !origMedia[cand] {
						images[i].name = cand
						origMedia[cand] = true
						break
					}
				}
			}
		}

		// Write to a temp file: copy every original part verbatim except the
		// modified ones, then write those + new media. We always rewrite
		// document/styles/rels/content-types; numbering.xml only when we added
		// ordered-list definitions, and settings.xml only for a TOC.
		out, err := os.CreateTemp(filepath.Dir(in.Path), ".docx-append-*")
		if err != nil {
			return "", fmt.Errorf("append: create temp file: %w", err)
		}
		tmpName := out.Name()
		zw := zip.NewWriter(out)

		skip := map[string]bool{
			"word/document.xml":            true,
			"word/styles.xml":              true,
			"word/_rels/document.xml.rels": true,
			"[Content_Types].xml":          true,
		}
		// When we're rewriting numbering/settings, skip the original copies so
		// our merged versions replace them (a zip can't contain two entries
		// with the same name).
		rewriteNumbering := orderedCount > 0
		if rewriteNumbering {
			skip["word/numbering.xml"] = true
		}
		if needsSettingsPart {
			skip["word/settings.xml"] = true
		}
		for _, f := range rz.File {
			if skip[f.Name] {
				continue
			}
			if err := copyZipPart(zw, f); err != nil {
				zw.Close()
				out.Close()
				_ = os.Remove(tmpName)
				return "", fmt.Errorf("append: copy %s: %w", f.Name, err)
			}
		}
		modified := []struct{ name, body string }{
			{"[Content_Types].xml", ctXML},
			{"word/_rels/document.xml.rels", relsXML},
			{"word/styles.xml", styles},
			{"word/document.xml", xmlBody},
		}
		if rewriteNumbering {
			modified = append(modified, struct{ name, body string }{"word/numbering.xml", numberingXML})
		}
		if needsSettingsPart {
			modified = append(modified, struct{ name, body string }{"word/settings.xml", settingsXML})
		}
		for _, p := range modified {
			w, err := zw.Create(p.name)
			if err != nil {
				zw.Close()
				out.Close()
				_ = os.Remove(tmpName)
				return "", err
			}
			if _, err := w.Write([]byte(p.body)); err != nil {
				zw.Close()
				out.Close()
				_ = os.Remove(tmpName)
				return "", err
			}
		}
		for _, img := range images {
			if err := addImageToZip(zw, img); err != nil {
				zw.Close()
				out.Close()
				_ = os.Remove(tmpName)
				return "", err
			}
		}
		if err := zw.Close(); err != nil {
			out.Close()
			_ = os.Remove(tmpName)
			return "", fmt.Errorf("append: close zip: %w", err)
		}
		if err := out.Close(); err != nil {
			_ = os.Remove(tmpName)
			return "", fmt.Errorf("append: close file: %w", err)
		}
		return tmpName, nil
	}

	tmpName, err := buildTemp()
	if err != nil {
		return err
	}
	// fsync the temp before the rename so a power loss after a successful
	// rename still leaves the bytes committed to disk (crash-safety comes from
	// the rename alone; this adds durability, mirroring atomicWrite's tmp.Sync).
	if fsErr := fsyncPath(tmpName); fsErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("append: fsync temp: %w", fsErr)
	}
	// The original reader is now closed; rename the temp over the original in
	// one step. os.Rename atomically replaces the destination on all platforms
	// (POSIX rename-over-existing; Windows MoveFileEx with REPLACE_EXISTING),
	// so we must NOT os.Remove the original first — a crash between a Remove
	// and a Rename would lose the user's only copy. On any rename error the
	// temp is cleaned up and the original is left untouched.
	if err := os.Rename(tmpName, in.Path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("append: replace file: %w", err)
	}
	return nil
}

// fsyncPath opens path read-write, fsyncs it, and closes it. This forces the
// kernel page cache to disk so a power loss after a subsequent rename still
// leaves the file's bytes committed. Errors are surfaced (best-effort: a
// fsync failure on some filesystems is non-fatal, but we err on the side of
// reporting).
func fsyncPath(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// nextRIdBase scans a document.xml.rels body for rId<N> relationships and
// returns N+1 of the maximum, so new relationships never collide with existing
// ones. Falls back to 100 when the part is absent/unreadable.
func nextRIdBase(relsXML string, relErr error) int {
	if relErr != nil || relsXML == "" {
		return 100
	}
	maxID := 1
	for _, m := range reIdRegexp.FindAllStringSubmatch(relsXML, -1) {
		if len(m) >= 2 {
			if n, err := strconv.Atoi(m[1]); err == nil && n > maxID {
				maxID = n
			}
		}
	}
	return maxID + 1
}

// maxNumIdInNumbering scans a word/numbering.xml body for <w:num w:numId="N">
// entries and returns the maximum N, so appended ordered lists can allocate
// numIds that don't collide with the base document's existing definitions.
// Returns 1 (the legacy decimal numId hiq always defines) when the part
// is absent/unreadable — append then behaves like full-write (lists use 2,3,…).
func maxNumIdInNumbering(numberingXML string) int {
	if numberingXML == "" {
		return 1
	}
	maxID := 1
	for _, m := range numIdRegexp.FindAllStringSubmatch(numberingXML, -1) {
		if len(m) >= 2 {
			if n, err := strconv.Atoi(m[1]); err == nil && n > maxID {
				maxID = n
			}
		}
	}
	return maxID
}

// injectUpdateFields returns a word/settings.xml body that forces Word to
// update fields (TOC, page numbers) on open. If existingSettings is non-empty
// and already contains updateFields, it's returned unchanged; otherwise
// updateFields is injected before </w:settings>. When the base doc has no
// settings part (setErr != nil / existingSettings empty), a minimal settings
// part is created. This is used by append mode so a TOC added via append
// auto-populates just like a full-write TOC.
func injectUpdateFields(existingSettings string, setErr error) string {
	const updateFieldsTag = `<w:updateFields w:val="true"/>`
	if setErr != nil || existingSettings == "" {
		// No settings part in the base doc — create a minimal one.
		return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
			`<w:settings xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
			updateFieldsTag + `</w:settings>`
	}
	if strings.Contains(existingSettings, "updateFields") {
		return existingSettings // already asks Word to update fields
	}
	// Inject just before the closing tag. If the body lacks </w:settings>
	// (malformed), fall back to creating a fresh minimal part.
	merged := strings.Replace(existingSettings, "</w:settings>", updateFieldsTag+"</w:settings>", 1)
	if merged == existingSettings {
		return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
			`<w:settings xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
			updateFieldsTag + `</w:settings>`
	}
	return merged
}

// mergeNumberingForAppend inserts one <w:num> entry per appended ordered list
// into an existing numbering.xml body, each referencing abstractNumId=1 (the
// shared decimal definition) with a <w:startOverride> so its counter restarts
// at 1. numBase is the first numId to allocate; orderedCount is how many
// ordered lists the appended sections contain. If the base numbering part is
// empty/unreadable, fall back to the static numberingXML and append onto that.
func mergeNumberingForAppend(existingNumbering string, numBase, orderedCount int) string {
	if orderedCount <= 0 {
		if existingNumbering == "" {
			return numberingXML
		}
		return existingNumbering
	}
	base := existingNumbering
	if base == "" {
		base = numberingXML
	}
	var extra strings.Builder
	for i := 1; i <= orderedCount; i++ {
		numID := numBase + i
		fmt.Fprintf(&extra, `<w:num w:numId="%d"><w:abstractNumId w:val="1"/><w:lvlOverride w:ilvl="0"><w:startOverride w:val="1"/></w:lvlOverride></w:num>`, numID)
	}
	// Insert before the closing tag. If the body somehow lacks </w:numbering>
	// (malformed), append at the end as a last resort.
	merged := strings.Replace(base, "</w:numbering>", extra.String()+"</w:numbering>", 1)
	if merged == base {
		merged = base + extra.String()
	}
	return merged
}

// copyZipPart copies one file from a source zip reader into a zip writer,
// preserving its name and (compressed) content.
func copyZipPart(zw *zip.Writer, f *zip.File) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	w, err := zw.Create(f.Name)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, rc)
	return err
}

// addImageToZip reads an image file and writes it into word/media/<name>.
func addImageToZip(zw *zip.Writer, img struct{ name, path string }) error {
	imgData, err := os.ReadFile(img.path)
	if err != nil {
		return fmt.Errorf("read image %s: %w", img.path, err)
	}
	w, err := zw.Create("word/media/" + img.name)
	if err != nil {
		return err
	}
	_, err = w.Write(imgData)
	return err
}

// readDocxPartFromReader extracts a single named part from an already-open zip
// reader (used by append mode, which already has the reader open for copying).
func readDocxPartFromReader(r *zip.ReadCloser, partName string) (string, error) {
	for _, f := range r.File {
		if f.Name != partName {
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
		return string(data), nil
	}
	// Wrap fs.ErrNotExist so callers can distinguish a missing part (use
	// defaults) from a corrupt-but-present part (must surface). The append path
	// checks errors.Is(err, fs.ErrNotExist) to decide its styles.xml fallback.
	return "", fmt.Errorf("part %q not found: %w", partName, fs.ErrNotExist)
}

// addImageContentTypes adds image MIME types to [Content_Types].xml. Each
// extension is declared at most once — OOXML requires Default extensions to be
// unique, so two .png images would otherwise produce a duplicate entry that
// Word rejects as a corrupt package.
func addImageContentTypes(ctXML string, images []struct{ name, path string }) string {
	seen := make(map[string]string) // ext → mime (first wins)
	for _, img := range images {
		ext := strings.ToLower(filepath.Ext(img.name))
		if ext == "" {
			continue
		}
		if _, dup := seen[ext]; dup {
			continue
		}
		mime := "image/png"
		switch ext {
		case ".jpg", ".jpeg":
			mime = "image/jpeg"
		case ".gif":
			mime = "image/gif"
		}
		seen[ext] = mime
	}
	if len(seen) == 0 {
		return ctXML
	}
	var additions strings.Builder
	// Stable order: sort extensions for deterministic output.
	var exts []string
	for ext := range seen {
		exts = append(exts, ext)
	}
	// Simple insertion sort (extension set is tiny).
	for i := 1; i < len(exts); i++ {
		for j := i; j > 0 && exts[j] < exts[j-1]; j-- {
			exts[j], exts[j-1] = exts[j-1], exts[j]
		}
	}
	for _, ext := range exts {
		additions.WriteString(fmt.Sprintf(`  <Default Extension="%s" ContentType="%s"/>`+"\n", strings.TrimPrefix(ext, "."), seen[ext]))
	}
	return strings.Replace(ctXML, "</Types>", additions.String()+"</Types>", 1)
}

// addImageRels adds image relationships to word/_rels/document.xml.rels. base
// is the starting rId number (100 for a full write continuing past the static
// rId1/rId2; one-past-the-max for an append so existing relationships survive).
func addImageRels(relsXML string, images []struct{ name, path string }, base int) string {
	additions := ""
	for i, img := range images {
		additions += fmt.Sprintf(`  <Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/%s"/>`+"\n", base+i, img.name)
	}
	return strings.Replace(relsXML, "</Relationships>", additions+"</Relationships>", 1)
}

// reIdRegexp matches rId<digits> relationship ids inside a .rels part, used to
// find the maximum existing rId so appended relationships never collide.
var reIdRegexp = regexp.MustCompile(`Id="rId(\d+)"`)

// numIdRegexp matches <w:num w:numId="N"> entries inside numbering.xml, used to
// find the maximum existing numId so appended ordered lists allocate ids that
// don't collide with the base document's numbering definitions.
var numIdRegexp = regexp.MustCompile(`<w:num w:numId="(\d+)"`)

// buildSectionsXML renders only the section fragments (no XML header or
// <w:body> wrapper) for use in append mode. It mirrors renderSection's dispatch
// so the same input renders identically in append and full-write modes.
// buildSectionsXML renders the new-section fragments (WITHOUT the
// <w:body> wrapper) for use in append mode. numBase is the first numId the
// appended ordered lists may use (so they don't collide with the base doc's
// existing numbering definitions); pass 1 for full-write mode (lists then use
// 2,3,…). It mirrors renderSection's dispatch so the same input renders
// identically in append and full-write modes.
func buildSectionsXMLWithBase(in DocInput, numBase int) string {
	var b strings.Builder
	imgCounter := 0     // shared across the loop → unique r:embed / docPr id per image
	orderedCounter := 0 // each ordered list gets its own numId (restart at 1)
	for _, sec := range in.Sections {
		switch strings.ToLower(strings.TrimSpace(sec.Type)) {
		case "heading":
			lvl := sec.Level
			if lvl < 1 {
				lvl = 1
			}
			if lvl > 6 {
				lvl = 6
			}
			b.WriteString(renderHeading(sec.Text, lvl, sec.Style))
		case "paragraph", "para", "text", "":
			b.WriteString(renderParagraph(sec.Text, sec.Style))
		case "table":
			b.WriteString(renderTable(sec.Headers, sec.Rows, sec.Style))
		case "list", "ul", "ol":
			items := sec.Items
			if len(items) == 0 && sec.Text != "" {
				items = []string{sec.Text}
			}
			numID := 0
			if sec.Ordered {
				orderedCounter++
				numID = numBase + orderedCounter
			}
			b.WriteString(renderList(items, sec.Ordered, numID))
		case "image":
			b.WriteString(renderImage(sec, imgCounter))
			imgCounter++
		case "toc":
			b.WriteString(renderTOC(sec.TOCLevel))
		default:
			// Unknown → paragraph, same safety net as renderSection.
			b.WriteString(renderParagraph(sec.Text, sec.Style))
		}
	}
	return b.String()
}

// buildSectionsXML is the full-write-mode wrapper: ordered lists use numId 2,3,…
// (numBase=1, so the first ordered list is numBase+1=2). Kept for back-compat
// with call sites that don't append into an existing package.
func buildSectionsXML(in DocInput) string { return buildSectionsXMLWithBase(in, 1) }

// buildDocumentXML renders the <w:document><w:body>…</w:body></w:document>
// from sections. Each section maps to one or more <w:p>/<w:tbl> blocks. An
// image counter is threaded through so each drawing gets a unique r:embed and
// docPr id (matching the image collection order in writeDOCX).
func buildDocumentXML(in DocInput) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	b.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`)
	b.WriteString(`<w:body>`)
	if strings.TrimSpace(in.Title) != "" {
		b.WriteString(renderHeading(in.Title, 1, DocStyle{Bold: true}))
	}
	imgCounter := 0
	orderedCounter := 0
	for _, s := range in.Sections {
		b.WriteString(renderSection(s, &imgCounter, &orderedCounter))
	}
	// Section properties: A4 page size + default margins so the doc renders
	// predictably across readers (Word defaults to US Letter otherwise).
	b.WriteString(`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/>`)
	b.WriteString(`<w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="720" w:footer="720" w:gutter="0"/>`)
	b.WriteString(`</w:sectPr>`)
	b.WriteString(`</w:body></w:document>`)
	return b.String()
}

// renderSection dispatches by Type. Unknown types render as a plain paragraph
// so the agent never produces a broken doc over a typo. imgIdx is incremented
// once per image section so every drawing references a distinct relationship id
// (rId100+i) and carries a unique docPr id (1000+i).
func renderSection(s DocSection, imgIdx *int, orderedCounter *int) string {
	switch strings.ToLower(s.Type) {
	case "heading":
		lvl := s.Level
		if lvl < 1 {
			lvl = 1
		}
		if lvl > 6 {
			lvl = 6 // Word defines Heading1-6; 公文 needs up to H4 ("（1）每周例会")
		}
		// Headings are bold by default (the Heading1-6 styles carry <w:b/>), but
		// we do NOT force Bold=true on the run: 公文 headings use SimHei/KaiTi
		// fonts (already visually heavy) and explicitly pass Bold:false, which
		// must be honored at the run level. The style-level <w:b/> still gives
		// plain users bold headings.
		return renderHeading(s.Text, lvl, s.Style)
	case "paragraph", "para", "text", "":
		return renderParagraph(s.Text, s.Style)
	case "list", "ul", "ol":
		items := s.Items
		if len(items) == 0 && s.Text != "" {
			items = []string{s.Text}
		}
		// Allocate a fresh numId (>=2) for each ordered list so it restarts at 1.
		// numId=1 stays defined as the legacy default but is never referenced by
		// renderList anymore; ordered lists use 2, 3, 4… built by buildNumberingXML.
		numID := 0
		if s.Ordered && orderedCounter != nil {
			*orderedCounter++
			numID = *orderedCounter + 1 // +1 so the first ordered list is numId=2
		}
		return renderList(items, s.Ordered, numID)
	case "table":
		return renderTable(s.Headers, s.Rows, s.Style)
	case "image":
		idx := 0
		if imgIdx != nil {
			idx = *imgIdx
			*imgIdx++
		}
		return renderImage(s, idx)
	case "toc":
		return renderTOC(s.TOCLevel)
	default:
		return renderParagraph(s.Text, s.Style)
	}
}

// renderHeading maps level → a built-in heading style (Heading1-6) defined in
// styles.xml. The style carries the size/bold; per-run style overrides color/font.
func renderHeading(text string, level int, st DocStyle) string {
	pStyle := fmt.Sprintf("Heading%d", level)
	return fmt.Sprintf(`<w:p>%s%s</w:p>`, pPrXML(pStyle, st), runXML(text, st))
}

// renderParagraph emits a body paragraph with run styling + alignment.
func renderParagraph(text string, st DocStyle) string {
	return fmt.Sprintf(`<w:p>%s%s</w:p>`, pPrXML("", st), runXML(text, st))
}

// pPrXML builds the full <w:pPr>…</w:pPr> for a paragraph: an optional heading
// pStyle, then line spacing / first-line indent, then alignment. All properties
// sit INSIDE one pPr block — a bare <w:jc> outside pPr is silently ignored by
// Word (the bug behind "title not centered"), so we never emit one.
func pPrXML(pStyle string, st DocStyle) string {
	var parts []string
	if pStyle != "" {
		parts = append(parts, fmt.Sprintf(`<w:pStyle w:val="%s"/>`, pStyle))
	}
	if st.LineSpacing > 0 {
		// OOXML line spacing: 240 = single, 360 = 1.5×, 480 = double.
		val := int(st.LineSpacing * 240)
		parts = append(parts, fmt.Sprintf(`<w:spacing w:line="%d" w:lineRule="auto"/>`, val))
	}
	if st.Indent > 0 {
		// First-line indent in CHARACTER units (公文 standard): each char =
		// 100 hundredths-of-a-char (firstLineChars). Indent:2 → "200" = 2 chars,
		// which Word renders at the font's actual char width regardless of font
		// size — the property Chinese official documents require. (The earlier
		// firstLine twips value drifted with font size, breaking 公文 layout.)
		val := st.Indent * 100
		parts = append(parts, fmt.Sprintf(`<w:ind w:firstLineChars="%d"/>`, val))
	}
	if jc := pAlignXML(st.Align); jc != "" {
		parts = append(parts, jc)
	}
	if len(parts) == 0 {
		return ""
	}
	return `<w:pPr>` + strings.Join(parts, "") + `</w:pPr>`
}

// renderList emits a sequence of paragraphs each carrying a numbering property.
// Unordered lists use numId=0 (bullet). Each ordered list gets its own numId
// (>=2) so it restarts counting at 1 instead of continuing from the previous
// ordered list — the matching <w:num> with <w:startOverride> is emitted by
// buildNumberingXML. Pass numID=0 for unordered, or the allocated id for
// ordered (the caller increments its ordered-list counter before calling).
func renderList(items []string, ordered bool, numID int) string {
	if !ordered {
		numID = 0 // bullet abstractNum
	}
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="%d"/></w:numPr></w:pPr>%s</w:p>`,
			numID, runXML(it, DocStyle{}))
	}
	return b.String()
}

// renderTable emits a <w:tbl> with an optional header row. Header cells get
// bold + shading (HeaderBg or a default); body cells honor per-section Bg.
// Column widths are auto (Word distributes evenly); borders are on by default.
func renderTable(headers []string, rows [][]string, st DocStyle) string {
	var b strings.Builder
	// Table properties: 100% width, single-line borders.
	b.WriteString(`<w:tbl>`)
	b.WriteString(`<w:tblPr><w:tblW w:w="5000" w:type="pct"/>`)
	b.WriteString(`<w:tblBorders>`)
	for _, edge := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
		fmt.Fprintf(&b, `<w:%s w:val="single" w:sz="4" w:space="0" w:color="auto"/>`, edge)
	}
	b.WriteString(`</w:tblBorders></w:tblPr>`)
	// Header row.
	if len(headers) > 0 {
		b.WriteString(`<w:tr>`)
		hSt := DocStyle{Bold: true}
		bg := st.HeaderBg
		if bg == "" {
			bg = "#D9D9D9" // light gray default header
		}
		for _, h := range headers {
			b.WriteString(renderTableCell(h, hSt, bg))
		}
		b.WriteString(`</w:tr>`)
	}
	// Body rows.
	for _, row := range rows {
		b.WriteString(`<w:tr>`)
		for _, cell := range row {
			b.WriteString(renderTableCell(cell, DocStyle{}, st.Bg))
		}
		b.WriteString(`</w:tr>`)
	}
	b.WriteString(`</w:tbl>`)
	// Empty paragraph after table (OOXML requires a paragraph after a table).
	b.WriteString(`<w:p/>`)
	return b.String()
}

// renderTableCell emits one <w:tc> with optional shading.
func renderTableCell(text string, st DocStyle, bg string) string {
	shd := ""
	if bg != "" {
		shd = fmt.Sprintf(`<w:shd w:val="clear" w:color="auto" w:fill="%s"/>`, hexNoHash(bg))
	}
	return fmt.Sprintf(`<w:tc><w:tcPr><w:tcW w:w="0" w:type="auto"/>%s</w:tcPr>%s</w:tc>`,
		shd, runXML(text, st))
}

// runXML emits a <w:r> with optional <w:rPr> styling + a <w:t> text run. XML
// escaping is via xml.Escape (handles & < > and quotes).
func runXML(text string, st DocStyle) string {
	rPr := runPropsXML(st)
	escaped := xmlEscapeText(text)
	// preserveSpaces keeps leading/trailing spaces Word would otherwise trim.
	return fmt.Sprintf(`<w:r>%s<w:t xml:space="preserve">%s</w:t></w:r>`, rPr, escaped)
}

// renderImage renders an image section as OOXML. imgIdx is the image's position
// among all image sections (0-based); it drives both the relationship id
// (rId100+imgIdx — matching addImageRels) and the drawing/docPr ids (1000+imgIdx)
// so a multi-image document never reuses an id. imageAlt, if set, becomes the
// wp:docPr descr (accessibility text).
func renderImage(s DocSection, imgIdx int) string {
	filename := filepath.Base(s.ImagePath)
	width := s.ImageWidth
	height := s.ImageHeight
	if width == 0 {
		width = 400
	}
	if height == 0 {
		height = 300
	}
	// Convert pixels to EMUs (1 pixel = 9525 EMUs)
	widthEMU := width * 9525
	heightEMU := height * 9525

	// docPr descr (alt text) — emitted only when provided, XML-escaped.
	var descrAttr string
	if alt := strings.TrimSpace(s.ImageAlt); alt != "" {
		var esc strings.Builder
		xml.Escape(&esc, []byte(alt))
		descrAttr = fmt.Sprintf(` descr="%s"`, esc.String())
	}

	// Unique ids: relationships start at rId100 (see addImageRels), drawing
	// object ids at 1000 to avoid colliding with other package parts.
	rID := fmt.Sprintf("rId%d", 100+imgIdx)
	drawID := 1000 + imgIdx

	return fmt.Sprintf(`<w:p>
  <w:r>
    <w:drawing>
      <wp:inline xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing">
        <wp:extent cx="%d" cy="%d"/>
        <wp:docPr id="%d" name="%s"%s/>
        <a:graphic xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
          <a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture">
            <pic:pic xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture">
              <pic:nvPicPr>
                <pic:cNvPr id="%d" name="%s"/>
                <pic:cNvPicPr/>
              </pic:nvPicPr>
              <pic:blipFill>
                <a:blip r:embed="%s" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"/>
                <a:stretch><a:fillRect/></a:stretch>
              </pic:blipFill>
              <pic:spPr>
                <a:xfrm>
                  <a:off x="0" y="0"/>
                  <a:ext cx="%d" cy="%d"/>
                </a:xfrm>
                <a:prstGeom prst="rect"><a:avLst/></a:prstGeom>
              </pic:spPr>
            </pic:pic>
          </a:graphicData>
        </a:graphic>
      </wp:inline>
    </w:drawing>
  </w:r>
</w:p>`, widthEMU, heightEMU, drawID, filename, descrAttr, drawID, filename, rID, widthEMU, heightEMU)
}

// renderTOC renders a Table of Contents field.
func renderTOC(level int) string {
	if level < 1 {
		level = 3
	}
	return fmt.Sprintf(`<w:p>
  <w:r>
    <w:fldChar w:fldCharType="begin"/>
  </w:r>
  <w:r>
    <w:instrText xml:space="preserve"> TOC \o "1-%d" \h \z \u </w:instrText>
  </w:r>
  <w:r>
    <w:fldChar w:fldCharType="separate"/>
  </w:r>
  <w:r>
    <w:t>[Table of Contents - Update field to populate]</w:t>
  </w:r>
  <w:r>
    <w:fldChar w:fldCharType="end"/>
  </w:r>
</w:p>`, level)
}

// runPropsXML builds the <w:rPr> for a run from a DocStyle. Empty when no
// styling is set (keeps the XML lean).
func runPropsXML(st DocStyle) string {
	var parts []string
	if st.Bold {
		parts = append(parts, `<w:b/>`)
	}
	if st.Italic {
		parts = append(parts, `<w:i/>`)
	}
	if st.Size > 0 {
		parts = append(parts, fmt.Sprintf(`<w:sz w:val="%d"/><w:szCs w:val="%d"/>`, st.Size, st.Size))
	}
	if c := hexNoHash(st.Color); c != "" {
		parts = append(parts, fmt.Sprintf(`<w:color w:val="%s"/>`, c))
	}
	if st.Font != "" {
		parts = append(parts, fmt.Sprintf(`<w:rFonts w:ascii="%s" w:hAnsi="%s" w:eastAsia="%s"/>`, st.Font, st.Font, st.Font))
	}
	if len(parts) == 0 {
		return ""
	}
	return "<w:rPr>" + strings.Join(parts, "") + "</w:rPr>"
}

// pAlignXML maps an align string to a <w:jc> paragraph property.
func pAlignXML(align string) string {
	switch strings.ToLower(align) {
	case "center":
		return `<w:jc w:val="center"/>`
	case "right":
		return `<w:jc w:val="right"/>`
	case "left":
		return `<w:jc w:val="left"/>`
	}
	return ""
}

// hexNoHash strips a leading # from a hex color, uppercased. Returns "" for
// invalid/empty input so callers can skip the attribute.
func hexNoHash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return ""
	}
	return strings.ToUpper(s)
}

// --- static OOXML parts -----------------------------------------------------

// maxImageBytes caps a single embedded image's size. addImageToZip reads the
// whole image into memory, so without this a multi-GB referenced image would
// OOM. 25 MiB is well above any reasonable embedded doc image (a full-page
// 300 DPI photo is ~5-10 MiB) while bounding peak memory.
const maxImageBytes int64 = 25 << 20 // 25 MiB

const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
<Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/>
</Types>`

const rootRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

const documentRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/>
</Relationships>`

// numberingXML defines two numbering definitions referenced by renderList:
// numId=0 → bullet (•), numId=1 → decimal (1. 2. 3.). Lives in its own
// word/numbering.xml part (Word rejects numbering declared inside styles.xml).
const numberingXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="bullet"/><w:lvlText w:val="•"/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="720" w:hanging="360"/></w:pPr></w:lvl></w:abstractNum>
<w:abstractNum w:abstractNumId="1"><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="%1."/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="720" w:hanging="360"/></w:pPr></w:lvl></w:abstractNum>
<w:num w:numId="1"><w:abstractNumId w:val="1"/></w:num>
<w:num w:numId="0"><w:abstractNumId w:val="0"/></w:num>
</w:numbering>`

// buildNumberingXML returns the numbering.xml part with one decimal <w:num>
// entry per ordered list in the document, each carrying a <w:startOverride
// w:val="1"/> so every ordered list restarts counting at 1 instead of
// continuing from the previous one. numId=0 (bullet) and numId=1 (legacy
// decimal, kept for back-compat) are always present; ordered lists use
// numId 2..orderedCount+1. abstractNumId=1 is the shared decimal definition.
func buildNumberingXML(orderedCount int) string {
	if orderedCount <= 0 {
		return numberingXML
	}
	var extra strings.Builder
	for i := 1; i <= orderedCount; i++ {
		numID := i + 1
		fmt.Fprintf(&extra, `<w:num w:numId="%d"><w:abstractNumId w:val="1"/><w:lvlOverride w:ilvl="0"><w:startOverride w:val="1"/></w:lvlOverride></w:num>`, numID)
	}
	// Insert before </w:numbering>.
	return strings.Replace(numberingXML, "</w:numbering>", extra.String()+"</w:numbering>", 1)
}

// countOrderedLists returns how many ordered list sections appear in in.Sections.
// Used to size the per-list numbering definitions emitted by buildNumberingXML.
// Matches renderSection's accepted type aliases: "list" (with Ordered), and
// "ol" (always ordered). "ul" is unordered and excluded.
func countOrderedLists(in DocInput) int {
	n := 0
	for _, s := range in.Sections {
		t := strings.ToLower(strings.TrimSpace(s.Type))
		if t == "ol" || (t == "list" && s.Ordered) {
			n++
		}
	}
	return n
}

// defaultStylesXML defines Normal + Heading1-6 styles. Heading sizes:
// H1=32 half-pts (16pt), H2=28 (14pt), H3=24 (12pt), H4=24 (12pt), H5=22 (11pt),
// H6=22 (11pt). H4+ exist for 公文 ("（1）每周例会") and deep document outlines.
// Numbering lives in numbering.xml (not here).
func defaultStylesXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="SimSun"/><w:sz w:val="22"/><w:szCs w:val="22"/></w:rPr></w:rPrDefault></w:docDefaults>
<w:style w:type="paragraph" w:styleId="Normal"><w:name w:val="Normal"/><w:pPr><w:spacing w:after="120" w:line="276" w:lineRule="auto"/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="240" w:after="120"/><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:b/><w:sz w:val="32"/><w:szCs w:val="32"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="200" w:after="100"/><w:outlineLvl w:val="1"/></w:pPr><w:rPr><w:b/><w:sz w:val="28"/><w:szCs w:val="28"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="160" w:after="80"/><w:outlineLvl w:val="2"/></w:pPr><w:rPr><w:b/><w:sz w:val="24"/><w:szCs w:val="24"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading4"><w:name w:val="heading 4"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="160" w:after="80"/><w:outlineLvl w:val="3"/></w:pPr><w:rPr><w:b/><w:sz w:val="24"/><w:szCs w:val="24"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading5"><w:name w:val="heading 5"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="140" w:after="80"/><w:outlineLvl w:val="4"/></w:pPr><w:rPr><w:b/><w:sz w:val="22"/><w:szCs w:val="22"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading6"><w:name w:val="heading 6"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="140" w:after="80"/><w:outlineLvl w:val="5"/></w:pPr><w:rPr><w:b/><w:sz w:val="22"/><w:szCs w:val="22"/></w:rPr></w:style>
</w:styles>`
}
