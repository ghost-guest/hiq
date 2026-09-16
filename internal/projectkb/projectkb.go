// Package projectkb implements 项目知识中枢 (the project knowledge hub): a
// self-maintaining, queryable map of a long-running project that keeps
// "项目详情与进度" answerable across months of development.
//
// It unifies four knowledge sources that already exist elsewhere in the kernel
// but were never brought together:
//
//   - code   — the module/package inventory (go.mod + package doc lines)
//   - docs   — the repo's own Markdown (docs/, README, AGENTS.md …)
//   - memory — the L1/L2 facts saved by internal/memory
//   - team   — the 团队 blackboard, task progress and artifacts
//
// Design — borrowed from WeKnora's "Wiki Mode" (raw documents distilled into a
// self-maintaining, interlinked knowledge base with revision history and
// rollback) but implemented natively so the hub stays portable and free of
// external services:
//
//   - 整理映射: Sync collects the enabled sources into a deterministic,
//     interlinked project map (map.md) plus a flat node index (kb.json).
//   - 增量同步: change detection is by content fingerprint, so a sync only
//     reports/rewrites what actually moved.
//   - 修订历史 + 回滚: every map-changing sync writes an immutable snapshot;
//     Rollback restores one.
//
// The hub is deliberately file-backed and self-contained: New takes a data root
// plus a working directory and touches only <root>/projectkb/<slug>/. It reads
// the memory and team stores directly off disk (through their own packages), so
// it needs no handle to a live store. Everything else — which sources are
// enabled, feeding the index into the system prompt, the tool surface, the UI —
// is wired by the caller.
package projectkb

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Kind identifies a knowledge source, and doubles as a node family.
type Kind string

const (
	KindCode   Kind = "code"
	KindDoc    Kind = "doc"
	KindMemory Kind = "memory"
	KindTeam   Kind = "team"
)

// Kinds is the canonical source order; it is also the map's section order
// (structure before prose before narrative).
var Kinds = []Kind{KindCode, KindDoc, KindMemory, KindTeam}

// KindLabels are the display titles used in the map and the panel.
var KindLabels = map[Kind]string{
	KindCode:   "模块与包",
	KindDoc:    "项目文档",
	KindMemory: "记忆（约定与决策）",
	KindTeam:   "团队与进度",
}

// Label returns a kind's display title.
func (k Kind) Label() string {
	if s, ok := KindLabels[k]; ok {
		return s
	}
	return string(k)
}

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	_, ok := KindLabels[k]
	return ok
}

// Source is one switchable knowledge source and its last-observed size.
type Source struct {
	Kind    Kind   `json:"kind"`
	Enabled bool   `json:"enabled"`
	Count   int    `json:"count"`
	Note    string `json:"note,omitempty"`
}

// Node is one indexed item of the map. ID is deterministic
// ("<kind>:<ref>") so a sync can diff by identity, and FP carries the
// fingerprint that decides added/changed/removed.
type Node struct {
	ID      string    `json:"id"`
	Kind    Kind      `json:"kind"`
	Title   string    `json:"title"`
	Ref     string    `json:"ref,omitempty"`
	Summary string    `json:"summary,omitempty"`
	Tags    []string  `json:"tags,omitempty"`
	Status  string    `json:"status,omitempty"`
	FP      string    `json:"fp,omitempty"`
	Updated time.Time `json:"updated,omitempty"`
}

// Revision is one immutable snapshot of the map.
type Revision struct {
	ID      string         `json:"id"`
	At      time.Time      `json:"at"`
	Trigger string         `json:"trigger,omitempty"`
	Note    string         `json:"note,omitempty"`
	Nodes   int            `json:"nodes"`
	Digest  string         `json:"digest"`
	Counts  map[string]int `json:"counts,omitempty"`
}

// Report summarises one Sync.
type Report struct {
	At        time.Time `json:"at"`
	Added     int       `json:"added"`
	Changed   int       `json:"changed"`
	Removed   int       `json:"removed"`
	Unchanged int       `json:"unchanged"`
	Total     int       `json:"total"`
	Digest    string    `json:"digest"`
	// Revision is the snapshot ID written by this sync, or "" when nothing
	// changed (so no snapshot was needed).
	Revision string `json:"revision,omitempty"`
}

// State is the persisted hub state (kb.json).
type State struct {
	Version int       `json:"version"`
	CWD     string    `json:"cwd"`
	Profile string    `json:"profile,omitempty"`
	Nodes   []Node    `json:"nodes"`
	Sources []Source  `json:"sources"`
	Digest  string    `json:"digest,omitempty"`
	Updated time.Time `json:"updated,omitempty"`
}

// Counts tallies nodes per kind.
func Counts(nodes []Node) map[string]int {
	out := make(map[string]int, len(Kinds))
	for _, n := range nodes {
		out[string(n.Kind)]++
	}
	return out
}

// digestOf fingerprints a node set so a sync can tell whether the map moved.
// Only identity + fingerprint participate, so re-rendering the same content
// never produces a spurious revision.
func digestOf(nodes []Node) string {
	h := sha256.New()
	for _, n := range nodes {
		fmt.Fprintf(h, "%s\x00%s\x00", n.ID, n.FP)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// shortDigest is the first 8 hex chars of a digest (for display).
func shortDigest(d string) string {
	if len(d) > 8 {
		return d[:8]
	}
	return d
}

// nodeID builds a node's deterministic identity.
func nodeID(kind Kind, ref string) string {
	return string(kind) + ":" + strings.TrimSpace(ref)
}

// fingerprint hashes the parts of a node that matter for change detection.
func fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// slugify turns an absolute path into a single directory name. Kept local (and
// deliberately simple) so the hub's own directory naming never depends on
// another package's private helper.
func slugify(abs string) string {
	s := filepath.ToSlash(abs)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == '/' || r == '\\' || r == ':' || r == ' ':
			b.WriteByte('-')
		default:
			// Drop anything exotic so the name is portable across filesystems.
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "default"
	}
	if len(out) > 80 {
		out = out[len(out)-80:]
	}
	return out
}

// firstLine returns the first non-blank line of s, trimmed and rune-capped.
func firstLine(s string, max int) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		if line == "" {
			continue
		}
		return clip(line, max)
	}
	return ""
}

// bodyLine returns the first non-blank body line of s that is not a Markdown
// heading, trimmed and rune-capped. Used for a node's one-line summary.
func bodyLine(s string, max int) string {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		t = strings.TrimSpace(strings.TrimLeft(t, "-*>|` "))
		if t == "" {
			continue
		}
		return clip(collapseSpace(t), max)
	}
	return ""
}

// collapseSpace squeezes runs of whitespace into single spaces.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clip truncates s to max runes, appending an ellipsis when it cut something.
func clip(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}
