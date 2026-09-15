package builtin

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/zzycxz/fairpeer/internal/netclient"
	"github.com/zzycxz/fairpeer/internal/sandbox"
	"github.com/zzycxz/fairpeer/internal/tool"
)

// ConfineBash returns the bash built-in bound to an OS-sandbox spec, overriding
// the unconfined instance registered at init. When the spec enforces, bash runs
// each command through the sandbox (see package sandbox).
func ConfineBash(spec sandbox.Spec, timeout ...time.Duration) tool.Tool {
	b := bash{sb: spec, shell: sandbox.ResolveShell("", "", nil)}
	if len(timeout) > 0 {
		b.timeout = timeout[0]
	}
	return b
}

// ConfineWebFetch returns the web_fetch built-in bound to fairpeer proxy
// settings while preserving its SSRF-guarded dialer.
func ConfineWebFetch(proxySpec netclient.ProxySpec) tool.Tool {
	return webFetch{proxySpec: proxySpec}
}

// ConfineWriters returns the file-writing built-ins bound to roots — the only
// directories they may modify: write_file, edit_file, multi_edit, and the
// document writers (doc_write, csv_write, xlsx_write, doc_convert). The
// composition root adds these to the per-run registry to override the
// unconfined instances registered at init time, so writes stay inside the
// workspace by default. roots may be relative; they are resolved to absolute,
// symlink-free paths once here. An empty roots slice yields unconfined writers.
//
// ConfineReaders is the read-side counterpart: when [sandbox] read_roots is
// configured, it returns read_file/grep AND the document readers (doc_read,
// csv_read, xlsx_read) bound to those roots so the agent can't read host files
// outside them. By default read tools are unconfined (see ConfineReaders note).
// This keeps the workspace boundary a WRITE boundary by default — read
// isolation is opt-in for high-security deployments. Audit A7.
func ConfineWriters(roots []string) []tool.Tool {
	rs := realRoots(roots)
	return []tool.Tool{
		writeFile{roots: rs},
		editFile{roots: rs},
		multiEdit{roots: rs},
		// delete_range/delete_symbol/notebook_edit are writers too (they mutate or
		// remove file content); their init()-registered zero values have roots==nil
		// and would otherwise bypass the workspace boundary. The desktop/workspace
		// path (Workspace.Tools) already binds roots; this covers the CLI/library path.
		deleteRange{roots: rs},
		deleteSymbol{roots: rs},
		notebookEdit{roots: rs},
		// Document tools that write files (doc_write/csv_write/xlsx_write/doc_convert)
		// are confined here too — without this they'd only do filepath.Abs and could
		// write anywhere (e.g. ~/.ssh/authorized_keys), bypassing [sandbox] workspace_root.
		docWrite{roots: rs},
		imageGenerate{roots: rs},
		csvWrite{roots: rs},
		xlsxWrite{roots: rs},
		docConvert{roots: rs},
		// mindmap_create writes .md/.html — without this it is the one writer
		// that escapes the workspace boundary entirely (it also lacks a Workspace
		// override), so it could write ~/.bashrc or .git/config.
		mindmapCreate{roots: rs},
	}
}

// ConfineReaders returns read_file/grep bound to roots — the only directories
// they may read from. Unlike ConfineWriters (always on), this is OPT-IN: by
// default read tools are unconfined because an agent legitimately reads /etc,
// system headers, ~/.gitconfig, package caches, etc. A deployment that wants a
// read/data-isolation boundary (not just a write boundary) configures
// [sandbox] read_roots and boot wires these in to override the unconfined
// instances. An empty roots slice yields unconfined readers (no-op).
//
// NOTE: even with this on, bash is NOT read-confined (it can cat any readable
// file), so this is a defense-in-depth measure, not a complete read isolation.
// glob is also left unconfined because a glob pattern isn't a single path to
// check. See security audit finding A7.
func ConfineReaders(roots []string) []tool.Tool {
	rs := realRoots(roots)
	if len(rs) == 0 {
		return nil // unconfined — caller should skip overriding the defaults
	}
	return []tool.Tool{
		readFile{roots: rs},
		grepTool{roots: rs},
		// Document read tools (doc_read/csv_read/xlsx_read/xlsx_query) also read
		// files by path, so they need the same read-boundary as read_file when
		// read isolation is enabled — otherwise they'd bypass [sandbox] read_roots.
		docRead{roots: rs},
		csvRead{roots: rs},
		xlsxRead{roots: rs},
		xlsxQuery{roots: rs},
	}
}

// confineRead is the read-side analogue of confine: it rejects a read target
// outside roots, but with a read-oriented error message (pointing at
// [sandbox] read_roots rather than workspace_root). Empty roots = unconfined.
func confineRead(roots []string, target string) error {
	if len(roots) == 0 {
		return nil
	}
	abs, err := realPath(target)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", target, err)
	}
	for _, r := range roots {
		if within(r, abs) {
			return nil
		}
	}
	return fmt.Errorf("path %q is outside the read boundary (reads are confined to %s); "+
		"read inside it, or widen [sandbox] read_roots in fairpeer.toml",
		target, strings.Join(roots, ", "))
}

// realRoots resolves each root to an absolute, symlink-free path, dropping any
// that cannot be made absolute. Resolving here (once) means the per-call check
// only has to resolve the target.
func realRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if real, err := realPath(r); err == nil {
			out = append(out, real)
		}
	}
	return out
}

// confine reports an error when target resolves outside every root. An empty
// roots slice is unconfined (returns nil) — the safe default for the built-in
// templates before a run configures the workspace. The error text is written
// for the model: it names the boundary and how the user can widen it.
func confine(roots []string, target string) error {
	if len(roots) == 0 {
		return nil
	}
	abs, err := realPath(target)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", target, err)
	}
	for _, r := range roots {
		if within(r, abs) {
			return nil
		}
	}
	return fmt.Errorf("path %q is outside the workspace (writes are confined to %s); "+
		"write inside it, or widen [sandbox] workspace_root / allow_write in fairpeer.toml",
		target, strings.Join(roots, ", "))
}

// realPath resolves path to an absolute, symlink-free form. Because a write
// target need not exist yet (write_file creates it), it resolves the deepest
// existing ancestor with EvalSymlinks and re-appends the not-yet-existing tail.
// This stops a symlinked directory from smuggling a write outside a root.
func realPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	tail := ""
	cur := abs
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, tail), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil // nothing along the path exists; use the cleaned abs
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}

// within reports whether path is at or below root. Both must be absolute,
// cleaned, symlink-free. It uses filepath.Rel so it is correct across volumes
// and is not fooled by a prefix that only matches a partial path component
// (e.g. /work-other is not within /work).
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
