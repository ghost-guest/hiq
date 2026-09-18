package builtin

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/zzycxz/hiq/internal/diff"
	fileenc "github.com/zzycxz/hiq/internal/fileutil/encoding"
)

// preview.go gives the file-writing built-ins the optional tool.Previewer
// capability: compute the change a call would make, reading the current file
// but never writing. A front-end (e.g. a desktop approval card) calls Preview
// before the permission gate runs Execute.
//
// Each Preview mirrors its Execute's transformation exactly — same arg parsing,
// same uniqueness / not-found rules — so the previewed NewText equals what
// Execute would persist. That equality is asserted by TestPreviewMatchesExecute
// in preview_test.go, which runs Execute against a temp file and compares; if
// an Execute body ever drifts, that test fails rather than the preview lying.

// Preview computes the change write_file would make. A path that does not yet
// exist is a Create; an existing one is a Modify.
func (w writeFile) Preview(args json.RawMessage) (diff.Change, error) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return diff.Change{}, fmt.Errorf("invalid args: %w", err)
	}
	if p.Path == "" {
		return diff.Change{}, fmt.Errorf("path is required")
	}
	p.Path = resolveIn(w.workDir, p.Path)
	// Confine reads to the workspace too — a preview runs BEFORE the permission
	// gate, so without this the diff card could surface file contents from
	// outside the workspace. Execute enforces the same boundary after the gate.
	if err := confine(w.roots, p.Path); err != nil {
		return diff.Change{}, err
	}

	old, kind := "", diff.Create
	if data, err := os.ReadFile(p.Path); err == nil {
		enc, _ := fileenc.Detect(data)
		old, kind = string(fileenc.Decode(data, enc)), diff.Modify
	} else if !os.IsNotExist(err) {
		return diff.Change{}, fmt.Errorf("read %s: %w", p.Path, err)
	}
	return diff.Build(p.Path, old, p.Content, kind), nil
}

// Preview computes the change edit_file would make. It enforces the same
// "old_string must occur exactly once" rule as Execute, returning that error
// when it doesn't — so a preview never shows a change the call couldn't make.
func (e editFile) Preview(args json.RawMessage) (diff.Change, error) {
	var p struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return diff.Change{}, fmt.Errorf("invalid args: %w", err)
	}
	if p.Path == "" {
		return diff.Change{}, fmt.Errorf("path is required")
	}
	if p.OldString == "" {
		return diff.Change{}, fmt.Errorf("old_string is required")
	}
	p.Path = resolveIn(e.workDir, p.Path)
	// Confine reads to the workspace — preview runs before the permission gate.
	if err := confine(e.roots, p.Path); err != nil {
		return diff.Change{}, err
	}

	content, _, err := readFileEncoded(p.Path)
	if err != nil {
		return diff.Change{}, fmt.Errorf("read %s: %w", p.Path, err)
	}

	// Preview must use the same match rule as Execute (exact first, then the
	// fuzzy fallbacks in fuzzyMatch) so a preview never reports "not found"
	// for an edit that would in fact succeed, nor shows a change the call
	// couldn't make. matchLineEndings normalizes CRLF the same way Execute does.
	old, newStr := matchLineEndings(content, p.OldString, p.NewString)
	region, found, unique := fuzzyMatch(content, old)
	if !found {
		return diff.Change{}, fmt.Errorf("old_string not found in %s", p.Path)
	}
	if !unique {
		return diff.Change{}, fmt.Errorf("old_string is not unique in %s; add more surrounding context", p.Path)
	}
	// Guard against fuzzy-match fallback returning a region that is not an
	// exact substring of the file content (mirrors editFile.Execute): a
	// fully-stripped indent-normalized region would otherwise make
	// strings.Replace replace zero occurrences, showing a preview that lies.
	if !strings.Contains(content, region) {
		return diff.Change{}, fmt.Errorf(
			"fuzzy match found %q but it does not appear verbatim in %s; "+
				"please supply the exact text from the file (including indentation)", old, p.Path)
	}

	updated := strings.Replace(content, region, newStr, 1)
	return diff.Build(p.Path, content, updated, diff.Modify), nil
}

// Preview computes the change multi_edit would make by replaying every edit
// against an in-memory buffer — exactly as Execute does — and diffing the
// result against the original. Any edit error surfaces here too, so a preview
// of an invalid batch fails the same way the call would.
func (m multiEdit) Preview(args json.RawMessage) (diff.Change, error) {
	var p struct {
		Path  string     `json:"path"`
		Edits []editStep `json:"edits"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return diff.Change{}, fmt.Errorf("invalid args: %w", err)
	}
	if p.Path == "" {
		return diff.Change{}, fmt.Errorf("path is required")
	}
	if len(p.Edits) == 0 {
		return diff.Change{}, fmt.Errorf("edits must not be empty")
	}
	p.Path = resolveIn(m.workDir, p.Path)
	// Confine reads to the workspace — preview runs before the permission gate.
	if err := confine(m.roots, p.Path); err != nil {
		return diff.Change{}, err
	}

	content, _, err := readFileEncoded(p.Path)
	if err != nil {
		return diff.Change{}, fmt.Errorf("read %s: %w", p.Path, err)
	}
	original := content

	for i, step := range p.Edits {
		if step.OldString == "" {
			return diff.Change{}, fmt.Errorf("edit %d: old_string is required", i+1)
		}
		old, newStr := matchLineEndings(content, step.OldString, step.NewString)
		if step.ReplaceAll {
			if strings.Count(content, old) == 0 {
				return diff.Change{}, fmt.Errorf("edit %d: old_string not found", i+1)
			}
			content = strings.ReplaceAll(content, old, newStr)
			continue
		}
		// Same fuzzy match rule as multiEdit.Execute so preview and execute
		// agree on whether each step is found/unique.
		region, found, unique := fuzzyMatch(content, old)
		if !found {
			return diff.Change{}, fmt.Errorf("edit %d: old_string not found", i+1)
		}
		if !unique {
			return diff.Change{}, fmt.Errorf("edit %d: old_string is not unique; add more surrounding context or set replace_all", i+1)
		}
		if !strings.Contains(content, region) {
			return diff.Change{}, fmt.Errorf(
				"edit %d: fuzzy match found %q but it does not appear verbatim in %s; "+
					"please supply the exact text from the file (including indentation)", i+1, old, p.Path)
		}
		content = strings.Replace(content, region, newStr, 1)
	}
	return diff.Build(p.Path, original, content, diff.Modify), nil
}
