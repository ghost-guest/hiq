package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zzycxz/hiq/internal/tool"
	"github.com/zzycxz/hiq/internal/validation"
)

func init() { tool.RegisterBuiltin(writeFile{}) }

// writeFile writes a file. roots, when non-empty, confines the target to the
// workspace (see confine); the zero value registered at init is unconfined and
// is overridden per run by ConfineWriters. workDir, when non-empty, is the
// directory a relative path resolves against (see resolveIn).
type writeFile struct {
	roots   []string
	workDir string
}

func (writeFile) Name() string { return "write_file" }

func (writeFile) Description() string {
	return "Write content to a file at the given path (overwriting existing content). Creates parent directories as needed."
}

func (writeFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path"},"content":{"type":"string","description":"Full content to write"}},"required":["path","content"]}`)
}

func (writeFile) ReadOnly() bool { return false }

func (w writeFile) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	p.Path = resolveIn(w.workDir, p.Path)
	if err := confine(w.roots, p.Path); err != nil {
		return "", err
	}
	// Preserve the existing file's encoding (GBK/UTF-16/BOM) on overwrite instead
	// of always writing UTF-8, which would silently corrupt a non-UTF-8 file.
	// readFileEncoded returns enc=UTF8 for a missing file — the right default for
	// a newly created one.
	existing, enc, rerr := readFileEncoded(p.Path)
	if rerr == nil && existing == p.Content {
		return fmt.Sprintf("%s already contains the exact content; no changes made", p.Path), nil
	}
	if dir := filepath.Dir(p.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	// Pre-write syntax validation (SPEC v2 §3.3): refuse to write a .go/.json
	// file whose content has syntax errors, so the file isn't corrupted on disk.
	// The agent gets a clear error and can fix it; unrecognized extensions pass.
	if err := validation.ValidateSyntax(p.Path, p.Content); err != nil {
		return "", fmt.Errorf("pre-write syntax check failed — fix the error and retry; the file was NOT written: %w", err)
	}
	if err := writeFileEncoded(p.Path, p.Content, enc); err != nil {
		return "", fmt.Errorf("write %s: %w", p.Path, err)
	}
	msg := fmt.Sprintf("wrote %d bytes to %s", len(p.Content), p.Path)
	if extra := runPostEditHook(ctx, p.Path); extra != "" {
		msg += "\n" + extra
	}
	return msg, nil
}
