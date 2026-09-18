package remotehost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zzycxz/hiq/internal/acp"
	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/fileref"
)

// projectSessionDir resolves a workspace root to its host-side session dir.
// A variable so tests can redirect it; default delegates to config.
var projectSessionDir = func(root string) string {
	return config.ProjectSessionDir(root)
}

// maxTextBytes caps a text preview; larger files stream as truncated text.
const maxTextBytes = 256 << 10 // 256 KiB

// maxMediaBytes caps a media file carried inline as a data URL.
const maxMediaBytes = 8 << 20 // 8 MiB

// resolveInRoot joins a caller-relative path onto the session root and refuses
// anything that would escape it (.. or absolute paths).
func resolveInRoot(root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return root, nil
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
		return "", errEscape
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errEscape
	}
	return filepath.Join(root, clean), nil
}

var errEscape = &acp.RPCError{Code: acp.ErrInvalidParams, Message: "path escapes the workspace root"}

func (h *host) fsList(_ context.Context, raw json.RawMessage) (any, error) {
	var p FsListParams
	if err := decodeParams("fs/list", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	dir, err := resolveInRoot(s.cwd, p.Path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, internalErr("fs/list", err)
	}
	out := FsListResult{Entries: []FsEntry{}}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && name != ".hiq" {
			// Keep hidden dirs out of the picker except hiq's own.
			continue
		}
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
				isDir = info.IsDir()
			}
		}
		out.Entries = append(out.Entries, FsEntry{Name: name, Dir: isDir})
	}
	sort.SliceStable(out.Entries, func(i, j int) bool {
		if out.Entries[i].Dir != out.Entries[j].Dir {
			return out.Entries[i].Dir
		}
		return out.Entries[i].Name < out.Entries[j].Name
	})
	return out, nil
}

// mediaKind classifies a previewable file by extension. Office documents come
// back as kind "office" — the desktop's extraction sidecars run locally, so P1
// marks them unpreviewable remotely.
func mediaKind(name string) (kind, mime string, ok bool) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image", "image/png", true
	case ".jpg", ".jpeg":
		return "image", "image/jpeg", true
	case ".gif":
		return "image", "image/gif", true
	case ".webp":
		return "image", "image/webp", true
	case ".svg":
		return "image", "image/svg+xml", true
	case ".bmp":
		return "image", "image/bmp", true
	case ".pdf":
		return "pdf", "application/pdf", true
	case ".mp4":
		return "video", "video/mp4", true
	case ".webm":
		return "video", "video/webm", true
	case ".mp3":
		return "audio", "audio/mpeg", true
	case ".wav":
		return "audio", "audio/wav", true
	case ".doc", ".docx", ".ppt", ".pptx", ".xls", ".xlsx":
		return "office", "", true
	}
	return "", "", false
}

func (h *host) fsRead(_ context.Context, raw json.RawMessage) (any, error) {
	var p FsReadParams
	if err := decodeParams("fs/read", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	path, err := resolveInRoot(s.cwd, p.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return FsReadResult{Kind: "missing"}, nil
	}
	if info.IsDir() {
		return nil, invalidParams("fs/read", "path is a directory")
	}
	if kind, mime, ok := mediaKind(path); ok {
		if info.Size() > maxMediaBytes {
			return FsReadResult{Kind: kind, Mime: mime, Size: info.Size(), Truncated: true}, nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, internalErr("fs/read", err)
		}
		dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
		if kind == "office" {
			return FsReadResult{Kind: kind, Size: info.Size()}, nil
		}
		return FsReadResult{Kind: kind, Mime: mime, DataURL: dataURL, Size: info.Size()}, nil
	}
	// Text: read a bounded prefix; stop early on a NUL byte (binary sniff).
	f, err := os.Open(path)
	if err != nil {
		return nil, internalErr("fs/read", err)
	}
	defer f.Close()
	buf := make([]byte, min64(info.Size(), maxTextBytes))
	n, _ := f.Read(buf)
	buf = buf[:n]
	truncated := int64(n) < info.Size()
	for _, b := range buf {
		if b == 0 {
			return FsReadResult{Kind: "binary", Size: info.Size()}, nil
		}
	}
	text := string(buf)
	if !utf8.ValidString(text) {
		return FsReadResult{Kind: "binary", Size: info.Size()}, nil
	}
	return FsReadResult{Kind: "text", Mime: "text/plain; charset=utf-8", Text: text, Size: info.Size(), Truncated: truncated}, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (h *host) fsSearch(_ context.Context, raw json.RawMessage) (any, error) {
	var p FsSearchParams
	if err := decodeParams("fs/search", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	return FsSearchResult{Results: rawJSON(fileref.Search(s.cwd, p.Query, 50))}, nil
}

// gitStatus mirrors the desktop's WorkspaceChanges git calls, executed here on
// the real workspace.
func (h *host) gitStatus(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("git/status", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	root := s.cwd
	out := GitStatusResult{Root: root, Entries: []GitEntry{}}
	if !isGitRepo(root) {
		return out, nil
	}
	out.IsRepo = true
	branch, detached := gitBranch(root)
	out.Branch, out.Detached = branch, detached
	porc := gitOutput(root, "status", "--porcelain")
	for _, line := range strings.Split(strings.TrimRight(porc, "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		code := strings.TrimSpace(line[:2])
		path := strings.TrimSpace(line[3:])
		out.Entries = append(out.Entries, GitEntry{Path: path, Change: code})
	}
	out.Added, out.Removed = parseNumstat(gitOutput(root, "diff", "--numstat", "HEAD", "--"))
	return out, nil
}

// parseNumstat totals a `git diff --numstat` report, skipping binary files
// (their columns are "-"). Mirrors the desktop/CLI parsers.
func parseNumstat(out string) (added, removed int) {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] != "-" {
			if n, err := strconv.Atoi(fields[0]); err == nil {
				added += n
			}
		}
		if fields[1] != "-" {
			if n, err := strconv.Atoi(fields[1]); err == nil {
				removed += n
			}
		}
	}
	return added, removed
}

func isGitRepo(root string) bool {
	return gitOutput(root, "rev-parse", "--is-inside-work-tree") == "true"
}

func gitBranch(root string) (branch string, detached bool) {
	b := strings.TrimSpace(gitOutput(root, "rev-parse", "--abbrev-ref", "HEAD"))
	if b == "HEAD" {
		return strings.TrimSpace(gitOutput(root, "rev-parse", "--short", "HEAD")), true
	}
	return b, false
}

func gitOutput(root string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}
