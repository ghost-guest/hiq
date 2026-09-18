// document_preview.go — upgrade spec 3-8: PreviewFiles for the office
// writers (doc_write / csv_write / xlsx_write / mindmap_create).
//
// These tools' new content comes from format-specific serialization that is
// too heavy to mirror twice, so their preview is a RESTORE POINT: the path
// being touched plus its current content. That is everything the checkpoint
// hook needs to snapshot and rewind (which is the point — office products were
// previously invisible to rewind), everything the parallel-writer partition
// needs for disjointness, and enough for an approval card to list the file.
// Diff stays empty: the card shows the file, not a diff it can't compute.
// doc_write additionally renders a full text diff for its plain-text outputs
// (.md/.txt with string content), the one case where the new text is cheap.
package builtin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zzycxz/hiq/internal/diff"
	fileenc "github.com/zzycxz/hiq/internal/fileutil/encoding"
)

// previewRestorePoint describes the file a writer is about to touch — old
// content for the checkpoint's restore, kind create/modify by existence —
// without predicting the new content.
func previewRestorePoint(roots []string, rawPath string) (diff.Change, error) {
	abs, err := filepath.Abs(strings.TrimSpace(rawPath))
	if err != nil {
		return diff.Change{}, err
	}
	if err := confine(roots, abs); err != nil {
		return diff.Change{}, err
	}
	old, kind := "", diff.Create
	if data, err := os.ReadFile(abs); err == nil {
		enc, _ := fileenc.Detect(data)
		old, kind = string(fileenc.Decode(data, enc)), diff.Modify
	} else if !os.IsNotExist(err) {
		return diff.Change{}, fmt.Errorf("read %s: %w", abs, err)
	}
	return diff.Change{Path: abs, Kind: kind, OldText: old}, nil
}

// docWritePath mirrors docWrite.Execute's target resolution: explicit path,
// or source's filled default (.docx) when only a template source is given.
func docWriteTarget(args json.RawMessage) (string, error) {
	var p struct {
		Source string `json:"source"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Path) != "" {
		return strings.TrimSpace(p.Path), nil
	}
	if strings.TrimSpace(p.Source) != "" {
		return defaultFilledPath(strings.TrimSpace(p.Source)), nil
	}
	return "", fmt.Errorf("path is required")
}

// PreviewFiles implements tool.MultiPreviewer for doc_write: a full text diff
// for .md/.txt with plain string content, a restore point otherwise.
func (w docWrite) PreviewFiles(args json.RawMessage) ([]diff.Change, error) {
	target, err := docWriteTarget(args)
	if err != nil {
		return nil, err
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(target), "."))
	if ext == "md" || ext == "txt" {
		var p struct {
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(args, &p) == nil && len(p.Content) > 0 {
			var text string
			if json.Unmarshal(p.Content, &text) == nil {
				return []diff.Change{w.textChange(target, text)}, nil
			}
		}
	}
	ch, err := previewRestorePoint(w.roots, target)
	if err != nil {
		return nil, err
	}
	return []diff.Change{ch}, nil
}

// textChange builds the full before/after diff for a plain-text write.
func (w docWrite) textChange(rawPath, newText string) diff.Change {
	abs, err := filepath.Abs(strings.TrimSpace(rawPath))
	if err != nil || confine(w.roots, abs) != nil {
		return diff.Change{Path: rawPath, Kind: diff.Create, NewText: newText}
	}
	old, kind := "", diff.Create
	if data, err := os.ReadFile(abs); err == nil {
		enc, _ := fileenc.Detect(data)
		old, kind = string(fileenc.Decode(data, enc)), diff.Modify
	}
	return diff.Build(abs, old, newText, kind)
}

// PreviewFiles implements tool.MultiPreviewer for csv_write (restore point —
// the CSV serialization stays single-sourced in Execute).
func (w csvWrite) PreviewFiles(args json.RawMessage) ([]diff.Change, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	ch, err := previewRestorePoint(w.roots, p.Path)
	if err != nil {
		return nil, err
	}
	return []diff.Change{ch}, nil
}

// PreviewFiles implements tool.MultiPreviewer for xlsx_write (restore point).
func (w xlsxWrite) PreviewFiles(args json.RawMessage) ([]diff.Change, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	ch, err := previewRestorePoint(w.roots, p.Path)
	if err != nil {
		return nil, err
	}
	return []diff.Change{ch}, nil
}

// PreviewFiles implements tool.MultiPreviewer for mindmap_create (restore
// point — the .md/.html renderers stay single-sourced in Execute).
func (m mindmapCreate) PreviewFiles(args json.RawMessage) ([]diff.Change, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	abs := resolveIn(m.workDir, strings.TrimSpace(p.Path))
	ch, err := previewRestorePoint(m.roots, abs)
	if err != nil {
		return nil, err
	}
	return []diff.Change{ch}, nil
}
