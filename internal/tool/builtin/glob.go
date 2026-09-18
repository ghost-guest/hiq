package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/tool"
)

func init() { tool.RegisterBuiltin(globTool{}) }

// globTool matches files by pattern. workDir, when non-empty, is the directory
// a relative pattern resolves against (see resolveIn).
type globTool struct{ workDir string }

func (globTool) Name() string { return "glob" }

func (globTool) Description() string {
	return "Find files matching a glob pattern (e.g. \"*.go\", \"internal/*/*.go\", \"**/*.test.ts\"). Supports shell metacharacters * ? [] and the recursive ** pattern. Note: results are capped at 200 entries; use more specific patterns to narrow down."
}

func (globTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Glob pattern (supports ** for recursive matching)"}},"required":["pattern"]}`)
}

func (globTool) ReadOnly() bool { return true }

// globMaxResults caps how many paths glob returns. Beyond it the output is
// truncated and a "(N more)" note reports the real overflow (not 0 — the
// original bug sliced before computing the remainder). 200 keeps a single glob
// response inside a manageable token budget while surfacing enough matches.
const globMaxResults = 200

func (g globTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	// Save the original pattern before resolveIn prepends workDir, so the
	// simple-filename recursive-fallback check below works on the raw input
	// — not the already-joined absolute path that always contains separators.
	rawPattern := p.Pattern
	p.Pattern = resolveIn(g.workDir, p.Pattern)
	p.Pattern = filepath.FromSlash(p.Pattern) // models emit "/" (see Description); WalkDir/Match compare OS-native paths

	// If the pattern contains **, use recursive matching via filepath.WalkDir.
	if strings.Contains(p.Pattern, "**") {
		return globRecursive(ctx, p.Pattern)
	}

	// For patterns without **, try filepath.Glob first. If no matches are
	// found and the pattern is a simple filename (no path separator), retry
	// with a recursive walk (equivalent to "**/<pattern>") so the tool finds
	// files anywhere in the tree — the common case where the model only knows
	// a filename but not its exact location. Uses the raw pattern (before
	// resolveIn) so a workspace root doesn't mask a simple "*.go".
	matches, err := filepath.Glob(p.Pattern)
	if err != nil {
		return "", fmt.Errorf("glob %q: %w", p.Pattern, err)
	}
	if len(matches) == 0 && !strings.ContainsAny(rawPattern, "/\\") {
		return globRecursive(ctx, filepath.Join(g.workDir, "**", rawPattern))
	}
	if len(matches) == 0 {
		return "(no matches)", nil
	}
	sortByMtimeDesc(matches)
	return formatGlobResults(matches), nil
}

// globRecursive handles patterns containing ** by walking the filesystem.
// It splits the pattern at ** to get a root prefix and a suffix to match
// against each file path found during the walk. Accepts a context so the
// walk can be interrupted on cancellation.
func globRecursive(ctx context.Context, pattern string) (string, error) {
	// Split on ** to find the root directory and the remaining pattern.
	parts := strings.SplitN(pattern, "**", 2)
	root := parts[0]
	// If root doesn't end with a separator, walk from its parent or "."
	// so we don't miss files at that level.
	if root == "" {
		root = "."
	}
	// Ensure root is a clean directory path.
	root = filepath.Clean(root)

	// Check root exists.
	if info, err := os.Stat(root); err != nil {
		return "", fmt.Errorf("glob %q: %w", pattern, err)
	} else if !info.IsDir() {
		return "(no matches)", nil
	}

	suffix := ""
	if len(parts) > 1 {
		suffix = strings.TrimPrefix(parts[1], string(os.PathSeparator))
	}

	var matches []string
	truncated := false

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err() // abort promptly on cancel — a huge tree is interruptible
		}
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			if skipWalkDir(root, path, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		// If there's no suffix, every file matches.
		if suffix == "" {
			matches = append(matches, path)
		} else {
			// Match the path against root + any-subdir + suffix.
			// Try matching the path relative to root against the suffix pattern.
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return nil
			}
			if matchGlobSuffix(rel, suffix) {
				matches = append(matches, path)
			}
		}
		if len(matches) >= globMaxResults {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("glob %q: %w", pattern, err)
	}

	if len(matches) == 0 {
		return "(no matches)", nil
	}
	sortByMtimeDesc(matches)
	result := formatGlobResults(matches)
	if truncated {
		// truncated only signals the cap was hit; the "(N more)" note is only
		// meaningful when we know the true total, which the recursive walker
		// stops counting at globMaxResults. Keep the cap-exceeded marker without
		// a fabricated count.
		result += fmt.Sprintf("\n... (truncated at %d results)", globMaxResults)
	}
	return result, nil
}

// doubleStarMatch handles patterns containing ** (double-star) which match
// zero or more directory levels. Supports ANY number of ** segments — the
// fixed segments between them are honored (the C5 regression: "a/**/b/**/c.go"
// used to wrongly match "a/x/c.go" because only the first ** was split and the
// middle "b" segment was dropped). Examples:
//   - **/foo/** matches any path containing "foo" as a directory component
//   - **/test/*.go matches any .go file under a "test" directory
//   - a/**/b/**/c.go matches "a/x/b/y/c.go" and "a/b/c.go" but NOT "a/x/y/c.go"
func doubleStarMatch(pattern, name string) bool {
	pattern = filepath.ToSlash(pattern)
	name = filepath.ToSlash(name)
	return doubleStarMatchParts(strings.Split(pattern, "**"), strings.Split(name, "/"))
}

// doubleStarMatchParts is the recursive core. segs is the pattern split on **,
// comps is the path split on /. segs[0] is a literal anchored at the start of
// comps; between two literal segs a ** absorbs zero or more components, so the
// next literal seg is searched for at every component offset.
func doubleStarMatchParts(segs, comps []string) bool {
	// No more pattern segments → match only if all components consumed.
	if len(segs) == 0 {
		return len(comps) == 0
	}
	// segs[0] is a literal (may be empty when the pattern starts with **).
	literal := strings.Trim(segs[0], "/")
	literalComps := splitNonEmpty(literal, "/")

	// Consume the first literal segment from the front of comps.
	if len(literalComps) > 0 {
		if len(comps) < len(literalComps) {
			return false
		}
		for i, lc := range literalComps {
			matched, _ := filepath.Match(lc, comps[i])
			if !matched {
				return false
			}
		}
		comps = comps[len(literalComps):]
	}

	// If that was the last pattern segment, the whole name must be consumed.
	// (A trailing literal in the pattern anchors the path's end.)
	if len(segs) == 1 {
		return len(comps) == 0
	}

	// Otherwise a ** sits between segs[0] and segs[1]: it absorbs zero or more
	// leading components, so try every possible split and recurse.
	for skip := 0; skip <= len(comps); skip++ {
		if doubleStarMatchParts(segs[1:], comps[skip:]) {
			return true
		}
	}
	return false
}

// splitNonEmpty splits s on sep and drops empty pieces (for "/a//b" → ["a","b"]).
func splitNonEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// matchGlobSuffix checks if path matches the suffix pattern after **.
// It tries matching at each directory level: if the pattern is "*.go",
// it matches "foo.go" and "dir/foo.go". If the pattern is "test/*.go",
// it matches "test/foo.go" and "dir/test/foo.go".
func matchGlobSuffix(path, pattern string) bool {
	// Direct match of the full relative path.
	if matched, _ := filepath.Match(pattern, path); matched {
		return true
	}
	// Try matching at each directory level.
	parts := strings.Split(path, string(os.PathSeparator))
	for i := range parts {
		sub := strings.Join(parts[i:], string(os.PathSeparator))
		if matched, _ := filepath.Match(pattern, sub); matched {
			return true
		}
	}
	// Also try matching just the filename against the pattern (for patterns
	// like "*.go" that should match any .go file at any depth).
	if !strings.Contains(pattern, string(os.PathSeparator)) {
		if matched, _ := filepath.Match(pattern, filepath.Base(path)); matched {
			return true
		}
	}
	return false
}

// sortByMtimeDesc reorders paths newest-first by file mtime. Paths whose mtime
// can't be read are pushed to the end (stable among themselves) rather than
// dropped — a stat failure shouldn't hide a match. This realizes the tool's
// documented "most-recent first" ordering; the previous code sorted
// lexicographically, which buried recently-edited files behind alphabetical
// noise.
func sortByMtimeDesc(paths []string) {
	// Pair each path with its mtime BEFORE sorting: sort callbacks receive
	// indices into the slice being reordered, so a parallel mtimes[] array
	// would desync as elements swap. Carrying mtime on each entry keeps them
	// glued together through the swap.
	type entry struct {
		path  string
		mtime time.Time
	}
	entries := make([]entry, len(paths))
	for i, p := range paths {
		entries[i] = entry{path: p}
		if info, err := os.Stat(p); err == nil {
			entries[i].mtime = info.ModTime()
		}
		// zero time on failure → sorts to the end under descending order.
	}
	sort.SliceStable(entries, func(i, j int) bool {
		// Newer (larger mtime) first. Equal mtimes keep their original order
		// (SliceStable), so a tie doesn't reshuffle the walk's natural order.
		return entries[i].mtime.After(entries[j].mtime)
	})
	for i, e := range entries {
		paths[i] = e.path
	}
}

// formatGlobResults joins paths and, when the caller has more matches than the
// cap, appends a "(N more)" note carrying the true overflow count. The earlier
// code sliced first and then computed len-len, always yielding 0.
func formatGlobResults(paths []string) string {
	if len(paths) <= globMaxResults {
		return strings.Join(paths, "\n")
	}
	more := len(paths) - globMaxResults
	return strings.Join(paths[:globMaxResults], "\n") +
		fmt.Sprintf("\n... (%d more)", more)
}
