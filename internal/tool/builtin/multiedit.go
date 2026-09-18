package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zzycxz/hiq/internal/tool"
	"github.com/zzycxz/hiq/internal/validation"
)

func init() { tool.RegisterBuiltin(multiEdit{}) }

// multiEdit applies a batch of edits to one file. roots confines the target to
// the workspace when non-empty (see writeFile); workDir, when non-empty, is the
// directory a relative path resolves against (see resolveIn).
type multiEdit struct {
	roots   []string
	workDir string
}

// editStep is one edit in a multi_edit operation. Mirrors edit_file's args
// plus a per-step replace_all toggle so a single call can mix targeted and
// sweep replacements (e.g. rename a function with replace_all, then patch
// one specific call site with a unique-match edit).
type editStep struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

func (multiEdit) Name() string { return "multi_edit" }

func (multiEdit) Description() string {
	return "Apply a list of edits to a single file atomically: each edit runs against the result of the previous one, all in memory; the file is rewritten only if every edit succeeds. Cheaper and safer than chaining edit_file calls — a failure in step 3 leaves the file untouched instead of half-edited."
}

func (multiEdit) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"File path"},
  "edits":{
    "type":"array",
    "minItems":1,
    "description":"Ordered edits. Each step sees the file as left by the previous step.",
    "items":{
      "type":"object",
      "properties":{
        "old_string":{"type":"string","description":"Exact text to find. Without replace_all, must match exactly once."},
        "new_string":{"type":"string","description":"Replacement text (empty deletes)."},
        "replace_all":{"type":"boolean","description":"Replace every occurrence instead of requiring uniqueness."}
      },
      "required":["old_string","new_string"]
    }
  }
},
"required":["path","edits"]
}`)
}

func (multiEdit) ReadOnly() bool { return false }

func (m multiEdit) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path  string     `json:"path"`
		Edits []editStep `json:"edits"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	if len(p.Edits) == 0 {
		return "", fmt.Errorf("edits must not be empty")
	}
	p.Path = resolveIn(m.workDir, p.Path)
	if err := confine(m.roots, p.Path); err != nil {
		return "", err
	}

	content, enc, err := readFileEncoded(p.Path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", p.Path, err)
	}

	// Apply edits in order against the running in-memory buffer. Any failure
	// returns before the write, leaving the file untouched — that's the
	// safety guarantee that makes multi_edit preferable to chained
	// edit_file calls.
	applied := 0
	for i, step := range p.Edits {
		if step.OldString == "" {
			return "", fmt.Errorf("edit %d: old_string is required", i+1)
		}
		old, newStr := matchLineEndings(content, step.OldString, step.NewString)
		if step.ReplaceAll {
			count := strings.Count(content, old)
			if count == 0 {
				return "", multiOldStringNotFoundError(i+1, step.OldString, content)
			}
			content = strings.ReplaceAll(content, old, newStr)
			applied += count
			continue
		}
		region, found, unique := fuzzyMatch(content, old)
		if !found {
			return "", multiOldStringNotFoundError(i+1, step.OldString, content)
		}
		if !unique {
			return "", fmt.Errorf("edit %d: old_string is not unique; add more surrounding context or set replace_all", i+1)
		}
		// Guard against fuzzy-match fallback returning a region that is not an
		// exact substring of the file content. strings.Replace would otherwise
		// silently replace zero occurrences, reporting success while making no
		// change. (Same guard edit_file has; without it multi_edit could claim
		// "1 edit applied" yet leave the file untouched.)
		if !strings.Contains(content, region) {
			return "", fmt.Errorf(
				"edit %d: fuzzy match found %q but it does not appear verbatim in %s; "+
					"please supply the exact text from the file (including indentation)", i+1, old, p.Path)
		}
		content = strings.Replace(content, region, newStr, 1)
		applied++
	}

	// Pre-write syntax validation (SPEC v2 §3.3): validate the final accumulated
	// content after all edits before it hits disk. One bad edit breaks the file.
	if err := validation.ValidateSyntax(p.Path, content); err != nil {
		return "", fmt.Errorf("pre-write syntax check failed — fix the error and retry; the file was NOT written: %w", err)
	}
	if err := writeFileEncoded(p.Path, content, enc); err != nil {
		return "", fmt.Errorf("write %s: %w", p.Path, err)
	}
	// Surface post-edit diagnostics (e.g. LSP) to the model — without this the
	// edit→diagnose→fix loop breaks for batch edits. Mirrors edit_file/write_file.
	msg := fmt.Sprintf("multi_edit %s: %d edits applied (%d total replacements)", p.Path, len(p.Edits), applied)
	if extra := runPostEditHook(ctx, p.Path); extra != "" {
		msg += "\n" + extra
	}
	return msg, nil
}

// multiOldStringNotFoundError is the multi_edit analogue of edit_file's
// oldStringNotFoundError: it includes a closest-match hint (the line most
// similar to old_string) so the model can locate its target, prefixed with the
// 1-based edit index that failed.
func multiOldStringNotFoundError(index int, oldString, content string) error {
	if line, text, sim, ok := nearestLine(content, oldString); ok {
		if len(text) > 100 {
			text = text[:97] + "..."
		}
		return fmt.Errorf("edit %d: old_string not found (nearest line %d, %d%% similar: %q)", index, line, int(sim*100), text)
	}
	return fmt.Errorf("edit %d: old_string not found", index)
}
