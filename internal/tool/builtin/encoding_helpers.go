package builtin

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	fileenc "github.com/zzycxz/hiq/internal/fileutil/encoding"
)

// maxWriteBytes caps the content length a writer tool will accept. Without it a
// model can paste an oversized payload (e.g. a directory dump or a repetition
// loop) into write_file/edit_file/apply_patch and produce a huge single file
// that bloats checkpoint snapshots, slows grep, or fills the disk. 5 MiB is
// well above any realistic source file while catching pathological output; a
// genuinely large artifact (data, logs, minified bundles) should go through
// bash redirection rather than a model-generated tool argument.
//
// Enforced by writeFileEncoded (write_file/edit_file/multi_edit/apply_patch/
// delete_range) AND by doc_write's own check (document.go:698, which bypasses
// writeFileEncoded because its atomic-write path is separate). Both checks are
// deliberate — removing the doc_write one would re-open the cap gap.
const maxWriteBytes = 5 << 20 // 5 MiB

// maxReadBytes caps how much readFileEncoded will load into memory. Without it,
// editing/appending a multi-GB file (e.g. a log) would os.ReadFile the whole
// thing and OOM the process. 10 MiB covers any realistic source/config/text
// file the model should edit in place; larger artifacts should be handled via
// bash (head/tail/sed) or the document tools' streaming path. This bounds only
// the read; the write is separately capped by maxWriteBytes.
const maxReadBytes int64 = 10 << 20 // 10 MiB

// readFileEncoded reads a file and decodes its encoding to UTF-8.
// Returns the decoded content and the detected encoding kind so callers
// can re-encode on write to preserve the original charset. It refuses files
// larger than maxReadBytes (os.Stat first, cheap) to avoid OOM on huge files.
// os.IsNotExist is preserved so first-write callers can distinguish "missing"
// from "too large".
func readFileEncoded(path string) (content string, enc fileenc.Kind, err error) {
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > maxReadBytes {
		return "", 0, fmt.Errorf("file is %d bytes (read limit %d); for a large file use bash head/tail/sed or a streaming tool", info.Size(), maxReadBytes)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	enc, _ = fileenc.Detect(b)
	decoded := fileenc.Decode(b, enc)
	// After decoding (which collapses UTF-16 NUL pairs into UTF-8), a remaining
	// NUL byte means the file is genuinely binary. The edit tools are text-only
	// — refuse so a blind guess-edit can't corrupt a binary file. read_file shows
	// the same binary marker; write_file tolerates this error (it only reads the
	// existing content for the no-change short-circuit and falls back to UTF-8).
	if bytes.IndexByte(decoded, 0) >= 0 {
		return "", 0, fmt.Errorf("binary file (NUL byte after decode); the edit tools are text-only — use bash for binary files")
	}
	return string(decoded), enc, nil
}

// writeFileEncoded encodes content back to the given encoding and writes it
// atomically (temp + fsync + rename via atomicWriteBytes), so a crash mid-write
// leaves either the old or new file intact — never a torn one. It refuses
// content larger than maxWriteBytes so a runaway model payload can't produce a
// giant file; callers surface the error to the model with guidance to use bash
// for oversized content.
func writeFileEncoded(path string, content string, enc fileenc.Kind) error {
	if n := len(content); n > maxWriteBytes {
		return fmt.Errorf("content is %d bytes (limit %d); content this large should be written via a shell redirect, not a model-generated tool argument", n, maxWriteBytes)
	}
	return atomicWriteBytes(path, fileenc.Encode(content, enc))
}

// matchLineEndings adapts an edit's old/new text to a CRLF file when the literal
// old_string isn't present but its CRLF form is. read_file strips '\r' (bufio
// ScanLines), so a model's multi-line old_string arrives LF-only while a
// Windows/CJK source stores '\r\n'; rewriting search and replacement to the
// file's ending fixes the match without rewriting the file's other line endings.
func matchLineEndings(content, old, new string) (string, string) {
	if strings.Contains(content, old) || !strings.Contains(content, "\r\n") {
		return old, new
	}
	toCRLF := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
	}
	if strings.Contains(content, toCRLF(old)) {
		return toCRLF(old), toCRLF(new)
	}
	return old, new
}
