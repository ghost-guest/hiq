package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zzycxz/hiq/internal/diff"
	"github.com/zzycxz/hiq/internal/tool"
	"github.com/zzycxz/hiq/internal/validation"
)

func init() { tool.RegisterBuiltin(editFile{}) }

// postEditHook, when set, runs after a successful write/edit and may return
// extra text (e.g. LSP diagnostics) appended to the tool result. Injected from
// boot.go so the edit→diagnose→fix loop closes without a separate lsp_diagnostics
// call. nil (the zero value) leaves write results unchanged, so tool tests that
// don't wire a hook are unaffected. The hook receives the caller's turn context
// so a slow LSP server is bounded by the same cancellation as the turn.
var postEditHook func(ctx context.Context, path string) string

// SetPostEditHook installs the post-write diagnostics hook. boot.go calls this
// with a closure over the LSP manager; passing nil disables the hook.
func SetPostEditHook(fn func(ctx context.Context, path string) string) { postEditHook = fn }

// runPostEditHook returns the hook's extra text (empty when no hook or no
// diagnostics), so callers can append it to the write result without sprinkling
// nil checks at every return site.
func runPostEditHook(ctx context.Context, path string) string {
	if postEditHook == nil {
		return ""
	}
	return postEditHook(ctx, path)
}

// editFile replaces an exact string in a file. roots confines the target to the
// workspace when non-empty (see writeFile); workDir, when non-empty, is the
// directory a relative path resolves against (see resolveIn).
type editFile struct {
	roots   []string
	workDir string
}

func (editFile) Name() string { return "edit_file" }

func (editFile) Description() string {
	return "Replace text in a file. Uses fuzzy matching: exact match first, then tries line-trim, indent-normalize, and block-anchor matching. old_string must occur exactly once; add surrounding context to disambiguate. Use for targeted edits instead of rewriting the whole file."
}

func (editFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path"},"old_string":{"type":"string","description":"Exact text to replace (must be unique in the file)"},"new_string":{"type":"string","description":"Replacement text (may be empty to delete)"}},"required":["path","old_string","new_string"]}`)
}

func (editFile) ReadOnly() bool { return false }

func (e editFile) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	if p.OldString == "" {
		return "", fmt.Errorf("old_string is required")
	}
	p.Path = resolveIn(e.workDir, p.Path)
	if err := confine(e.roots, p.Path); err != nil {
		return "", err
	}

	content, enc, err := readFileEncoded(p.Path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", p.Path, err)
	}

	old, newStr := matchLineEndings(content, p.OldString, p.NewString)
	region, found, unique := fuzzyMatch(content, old)
	if !found {
		return "", oldStringNotFoundError(p.Path, p.OldString, content)
	}
	if !unique {
		return "", fmt.Errorf("old_string is not unique in %s; add more surrounding context", p.Path)
	}

	// Guard against fuzzy-match fallback returning a region that is not an
	// exact substring of the file content (e.g. a fully-stripped
	// indent-normalized version).  strings.Replace would silently replace
	// zero occurrences, reporting success while making no change.
	if !strings.Contains(content, region) {
		return "", fmt.Errorf(
			"fuzzy match found %q but it does not appear verbatim in %s; "+
				"please supply the exact text from the file (including indentation)", old, p.Path)
	}

	updated := strings.Replace(content, region, newStr, 1)
	// No-op guard: if the replacement left the file byte-identical (e.g. the
	// model passed old_string == new_string, or a fuzzy match produced a region
	// that already equals newStr), skip the write so we don't dirty mtime / git
	// status for nothing — mirrors write_file's existing-content check.
	if updated == content {
		return fmt.Sprintf("%s: no changes made (old_string equals new_string)", p.Path), nil
	}
	// Pre-write syntax validation (SPEC v2 §3.3): validate the PROPOSED updated
	// content before it hits disk. Catches an edit that breaks Go/JSON syntax.
	if err := validation.ValidateSyntax(p.Path, updated); err != nil {
		return "", fmt.Errorf("pre-write syntax check failed — fix the error and retry; the file was NOT written: %w", err)
	}
	if err := writeFileEncoded(p.Path, updated, enc); err != nil {
		return "", fmt.Errorf("write %s: %w", p.Path, err)
	}
	// Surface the unified diff so the model can verify what it changed instead
	// of only knowing "edited <path>". This closes the edit→repeat loop: without
	// the diff the model may re-edit a region it already fixed. diff.Build
	// returns a Change whose .Diff is unified-diff text (3 lines of context,
	// git-style) and .Added/.Removed are the line tallies; for a binary or
	// no-op change Diff is empty.
	ch := diff.Build(p.Path, content, updated, diff.Modify)
	msg := fmt.Sprintf("edited %s", p.Path)
	if ch.Diff != "" {
		msg += "\n\n" + ch.Diff
	}
	if extra := runPostEditHook(ctx, p.Path); extra != "" {
		msg += "\n" + extra
	}
	return msg, nil
}

// oldStringNotFoundError returns a "not found" error that, when possible,
// includes a closest-match hint (the line most similar to old_string) so the
// model can locate its target instead of guessing. The hint is capped in length
// so a pathological file never floods the error.
func oldStringNotFoundError(path, oldString, content string) error {
	if line, text, sim, ok := nearestLine(content, oldString); ok {
		if len(text) > 100 {
			text = text[:97] + "..."
		}
		return fmt.Errorf("old_string not found in %s (nearest line %d, %d%% similar: %q)", path, line, int(sim*100), text)
	}
	return fmt.Errorf("old_string not found in %s", path)
}
