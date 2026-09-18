package builtin

// docxsafety.go provides the read-side safety guards shared by every tool that
// opens an untrusted Office package: doc_read (.xlsx/.docx/.pptx), doc_write's
// template-fill path (.docx source), and writeDOCXAppend (existing .docx).
// atomic.go protects WRITES (a failed write never tears the user's file); this
// file protects READS of untrusted Office packages: a malicious or corrupted
// package can't OOM the process (decompression bomb), and a package that's
// still open in Word/Excel fails loudly instead of producing a half-read result.
//
// We deliberately stop at three bomb heuristics (total bytes / entry count /
// ratio). OfficeCLI carries six guards (adds recursion depth, regex timeout,
// DOM element cap, SSRF), but those defend a multi-tenant server scenario;
// hiq is a local single-user assistant, Go's RE2 can't backtrack, and
// encoding/xml doesn't fetch external entities, so the extra guards add cost
// without covering a real threat.

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// --- structured error codes ------------------------------------------------

// DocError is the LLM-facing error type for document operations. The Code is
// a stable machine-readable identifier the model can branch on; Suggestion
// tells it how to recover (close the file, fix the index, convert the image).
// Mirrors OfficeCLI's CliException { Code, Suggestion, ValidValues }.
type DocError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

func (e DocError) Error() string {
	if e.Suggestion != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Suggestion)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Stable error codes. LLMs match on these to decide retry vs. give up.
const (
	ErrFileNotFound  = "file_not_found"
	ErrFileLocked    = "file_locked"
	ErrCorruptFile   = "corrupt_file"
	ErrDecompBomb    = "decompression_bomb"
	ErrCellOverflow  = "cell_overflow"
	ErrTableIndexOOB = "table_index_out_of_bounds"
	ErrRowIndexOOB   = "row_index_out_of_bounds"
	ErrColIndexOOB   = "col_index_out_of_bounds"
	ErrMergedCell    = "merged_cell_continuation"
	ErrPlaceholderNF = "placeholder_not_found"
	ErrInvalidArg    = "invalid_argument"
)

// MaxExcelCellChars is Excel's hard per-cell limit. Writing more produces a
// file Excel refuses to open or silently truncates.
const MaxExcelCellChars = 32767

// checkCellValue rejects a string that would overflow an Excel cell. Call this
// before SetCellValue/SetCellStr in the xlsx paths.
func checkCellValue(value, ref string) error {
	if utf8.RuneCountInString(value) > MaxExcelCellChars {
		return DocError{
			Code:       ErrCellOverflow,
			Message:    fmt.Sprintf("cell %s value length %d exceeds Excel's %d-char limit", ref, utf8.RuneCountInString(value), MaxExcelCellChars),
			Suggestion: "split the content across multiple cells, or shorten it",
		}
	}
	return nil
}

// --- decompression-bomb guard ----------------------------------------------

// Limits for the bomb guard. Generous enough that no legitimate Office doc
// comes close — real .docx/.xlsx are a few MB uncompressed with tens of parts.
// A hostile file can hit megabytes-of-zip → gigabytes-uncompressed; these
// catch that before we allocate.
const (
	maxUncompressedBytes int64 = 2 * 1024 * 1024 * 1024 // 2 GiB total
	maxZipEntries        int   = 100_000                // entry count
	maxCompressionRatio  int64 = 1000                   // uncompressed/compressed
)

// guardDecompressionBomb scans a zip's central directory (without decompressing
// the bodies) and rejects packages that would OOM the process. Reads only the
// per-entry UncompressedSize64 metadata, so it's cheap even on large zips.
//
// Three checks, mirroring OfficeCLI DocumentLimits.cs:
//   - total uncompressed size > 2 GiB
//   - entry count > 100k (million-entry zip exhaustion)
//   - worst-entry compression ratio > 1000× (a few KB → many MB)
//
// Real Office documents rarely exceed 100× compression, so the ratio cap has
// headroom for dense spreadsheets without false-positiving on legit files.
func guardDecompressionBomb(zipPath string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		// Surface as corrupt_file rather than a raw os error — the caller
		// already stat'd the path (file exists), so an open failure means the
		// zip structure is damaged.
		return DocError{Code: ErrCorruptFile, Message: fmt.Sprintf("cannot read package: %v", err),
			Suggestion: "the file may be corrupted or not a valid .docx/.xlsx; try re-saving it from Word/Excel"}
	}
	defer zr.Close()

	var totalUncompressed int64
	for _, f := range zr.File {
		totalUncompressed += int64(f.UncompressedSize64)
		if totalUncompressed > maxUncompressedBytes {
			return DocError{Code: ErrDecompBomb,
				Message: fmt.Sprintf("package uncompressed size exceeds %d-byte limit", maxUncompressedBytes),
				Suggestion: "the file is unusually large; if it's legitimate, split it into smaller documents"}
		}
		if f.UncompressedSize64 > 0 && f.CompressedSize64 > 0 {
			if ratio := int64(f.UncompressedSize64) / int64(f.CompressedSize64); ratio > maxCompressionRatio {
				return DocError{Code: ErrDecompBomb,
					Message: fmt.Sprintf("entry %s has compression ratio %d× (limit %d×)", f.Name, ratio, maxCompressionRatio),
					Suggestion: "the file may be a zip bomb; obtain it from a trusted source"}
			}
		}
	}
	if len(zr.File) > maxZipEntries {
		return DocError{Code: ErrDecompBomb,
			Message:    fmt.Sprintf("package has %d entries (limit %d)", len(zr.File), maxZipEntries),
			Suggestion: "the file has an unusual structure; obtain it from a trusted source"}
	}
	return nil
}

// --- file-lock detection ---------------------------------------------------

// checkFileLocked reports whether path is open in another process (typically
// Word/Excel). It does this by attempting an exclusive open — if another
// process holds the file, the OS denies us. On Windows this catches Office's
// write lock reliably; the ~$ owner-file check OfficeCLI mentions is less
// reliable (OneDrive sync and crashes leave stale/missing owner files) so we
// rely on the open-probe alone.
//
// Call this BEFORE reading a template: a half-flushed file produces a
// half-read result that silently corrupts the output. Returns nil if the
// file is freely accessible.
func checkFileLocked(path string) error {
	// Re-open for read+write briefly. We don't hold the handle — just probe.
	// O_RDWR because a read-only open succeeds even when Word has a write lock;
	// matching the access we'd need to copy/rewrite the file.
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		// Distinguish "locked" (exists, busy) from "missing" (caller bug).
		if errors.Is(err, os.ErrNotExist) {
			return DocError{Code: ErrFileNotFound, Message: fmt.Sprintf("%s does not exist", path)}
		}
		return DocError{
			Code:       ErrFileLocked,
			Message:    fmt.Sprintf("%s is open in another program: %v", path, err),
			Suggestion: "close the file in Word/Excel/WPS and try again",
		}
	}
	f.Close()
	return nil
}

// --- BOM stripping ---------------------------------------------------------

// stripBOM removes a leading UTF-8 or UTF-16 BOM from an XML part's bytes.
// Office packages occasionally carry a BOM (some non-Microsoft generators add
// one), and while encoding/xml tolerates a UTF-8 BOM, a UTF-16 BOM on a part
// declared as encoding="UTF-8" confuses downstream readers. We strip whatever
// BOM is present and let the caller treat the result as UTF-8.
func stripBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:] // UTF-8 BOM
	}
	if len(data) >= 2 {
		if data[0] == 0xFF && data[1] == 0xFE {
			return data[2:] // UTF-16 LE BOM (caller assumes UTF-8; rare in practice)
		}
		if data[0] == 0xFE && data[1] == 0xFF {
			return data[2:] // UTF-16 BE BOM
		}
	}
	return data
}

// --- XML escape helper -----------------------------------------------------

// xmlEscapeText escapes XML special characters for safe embedding in OOXML.
// Wraps encoding/xml's Escape so all text-injection sites (find_replace values,
// table_fill values, header/footer text) route through one place.
func xmlEscapeText(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if i > 0 {
			b.WriteString(`</w:t><w:br/><w:t xml:space="preserve">`)
		}
		// Also trim \r in case of CRLF
		line = strings.TrimSuffix(line, "\r")
		segs := strings.Split(line, "\t")
		for j, seg := range segs {
			if j > 0 {
				b.WriteString(`</w:t><w:tab/><w:t xml:space="preserve">`)
			}
			xml.EscapeText(&b, []byte(seg))
		}
	}
	return b.String()
}

// --- existence guard (used by template-fill before bomb/lock checks) --------

// checkFileExists is a cheap pre-flight that returns a friendly ErrFileNotFound
// before the caller attempts a more expensive zip open. The order matters:
// existence → bomb → lock, so a missing file doesn't surface as "corrupt".
func checkFileExists(path string) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DocError{Code: ErrFileNotFound, Message: fmt.Sprintf("%s does not exist", path)}
		}
		return err
	}
	return nil
}
