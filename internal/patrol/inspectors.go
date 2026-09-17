package patrol

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zzycxz/fairpeer/internal/gitcmd"
)

// gitProbeTimeout bounds each git invocation. Patrol must never block the loop
// on a slow or hung repository.
const gitProbeTimeout = 10 * time.Second

// GitInspector reports the state of a git working tree: uncommitted changes,
// unresolved merge conflicts, divergence from upstream, and a detached HEAD.
//
// It is the core "is my desk tidy?" check for a coding session — exactly the
// thing a proactive patrol should notice while the user is away. Every probe is
// read-only (gitcmd hardens the invocation: no index lock, no maintenance
// daemon, no credential prompt).
type GitInspector struct {
	// Timeout overrides the per-probe timeout. 0 → 10s.
	Timeout time.Duration
}

// Name implements Inspector.
func (GitInspector) Name() string { return "git" }

// Inspect implements Inspector. A directory that is not a git work tree yields
// no findings (the marker inspector covers plain directories).
func (g GitInspector) Inspect(ctx context.Context, t Target, _ string) Result {
	if !isGitWorkTree(ctx, t.Root, g.timeout()) {
		return Result{}
	}
	branch := gitOut(ctx, t.Root, g.timeout(), "rev-parse", "--abbrev-ref", "HEAD")
	head := gitOut(ctx, t.Root, g.timeout(), "rev-parse", "HEAD")

	porcelain := gitOut(ctx, t.Root, g.timeout(), "status", "--porcelain=v1")
	var files, conflicts []string
	for _, line := range strings.Split(porcelain, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		code := line[:min(2, len(line))]
		path := strings.TrimSpace(line[len(code):])
		files = append(files, path)
		if isConflictCode(code) {
			conflicts = append(conflicts, path)
		}
	}

	var out []Finding
	if len(conflicts) > 0 {
		out = append(out, Finding{
			ID:          "git.conflict",
			Kind:        "git",
			Severity:    SevHigh,
			Summary:     fmt.Sprintf("%d file(s) have unresolved merge conflicts", len(conflicts)),
			Detail:      "Resolve the conflicts before continuing; the tree will not build or diff reliably until they are gone.",
			Paths:       capPaths(conflicts, 20),
			Fingerprint: hashLines(conflicts),
		})
	}
	if len(files) > 0 {
		out = append(out, Finding{
			ID:          "git.dirty",
			Kind:        "git",
			Severity:    SevWarn,
			Summary:     fmt.Sprintf("%d uncommitted change(s) in %s", len(files), branchLabel(branch)),
			Detail:      "The working tree differs from HEAD.",
			Paths:       capPaths(files, 20),
			Fingerprint: hashLines(files),
		})
	}
	if behind, ahead, ok := divergence(ctx, t.Root, g.timeout()); ok && (ahead > 0 || behind > 0) {
		out = append(out, Finding{
			ID:          "git.diverged",
			Kind:        "git",
			Severity:    SevInfo,
			Summary:     fmt.Sprintf("branch is %d ahead / %d behind its upstream", ahead, behind),
			Fingerprint: fmt.Sprintf("ahead=%d,behind=%d", ahead, behind),
		})
	}
	if branch == "HEAD" {
		out = append(out, Finding{
			ID:          "git.detached",
			Kind:        "git",
			Severity:    SevInfo,
			Summary:     "HEAD is detached",
			Detail:      "New commits here are easy to lose; check out a branch before committing.",
			Fingerprint: head,
		})
	}
	return Result{Findings: out, State: head}
}

func (g GitInspector) timeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return gitProbeTimeout
}

func isGitWorkTree(ctx context.Context, root string, timeout time.Duration) bool {
	return gitOut(ctx, root, timeout, "rev-parse", "--is-inside-work-tree") == "true"
}

// divergence returns (behind, ahead) relative to the upstream. ok is false when
// there is no upstream (a brand-new branch), which is not a finding.
func divergence(ctx context.Context, root string, timeout time.Duration) (behind, ahead int, ok bool) {
	out := gitOut(ctx, root, timeout, "rev-list", "--left-right", "--count", "@{upstream}...HEAD")
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, false
	}
	b, err1 := strconv.Atoi(fields[0])
	a, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return b, a, true
}

// gitOut runs a git probe and returns trimmed stdout, or "" on any error. A
// probe failure is not a patrol finding: a missing git, a bare repo or a
// permission problem must stay quiet rather than cry wolf.
func gitOut(ctx context.Context, root string, timeout time.Duration, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, err := gitcmd.Command(ctx, root, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func isConflictCode(code string) bool {
	switch code {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	}
	return false
}

func branchLabel(branch string) string {
	if branch == "" || branch == "HEAD" {
		return "the working tree"
	}
	return branch
}

func hashLines(lines []string) string {
	sorted := make([]string, len(lines))
	copy(sorted, lines)
	sort.Strings(sorted)
	h := sha256.New()
	for _, l := range sorted {
		io.WriteString(h, l)
		io.WriteString(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func capPaths(paths []string, n int) []string {
	if len(paths) <= n {
		return paths
	}
	out := make([]string, n, n+1)
	copy(out, paths[:n])
	return append(out, fmt.Sprintf("… and %d more", len(paths)-n))
}

// --- marker inspector -------------------------------------------------------

// MarkerInspector scans source files for unresolved TODO/FIXME/XXX/HACK markers
// and reports the set. It is the hygiene half of a patrol: a proactive agent
// that notices "you just left four TODOs in the new package" is worth more than
// one that only reads git status.
//
// The finding is keyed by the whole marker set, so it reports when markers are
// added or removed and stays quiet while they are untouched.
type MarkerInspector struct {
	// MaxFiles bounds one scan (default 4000).
	MaxFiles int
	// MaxFileBytes skips files too large to be hand-written source
	// (default 1 MiB).
	MaxFileBytes int64
	// MaxMarkers bounds how many markers are collected (default 300).
	MaxMarkers int
	// MaxShown bounds how many markers appear in the detail (default 20).
	MaxShown int
}

// Name implements Inspector.
func (MarkerInspector) Name() string { return "markers" }

// marker is one unresolved marker line.
type marker struct {
	path string // relative to the target root, slash-separated
	line int
	text string
}

// Inspect implements Inspector.
func (m MarkerInspector) Inspect(_ context.Context, t Target, _ string) Result {
	maxFiles := m.MaxFiles
	if maxFiles <= 0 {
		maxFiles = 4000
	}
	maxBytes := m.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	maxMarkers := m.MaxMarkers
	if maxMarkers <= 0 {
		maxMarkers = 300
	}
	maxShown := m.MaxShown
	if maxShown <= 0 {
		maxShown = 20
	}

	root := absPath(t.Root)
	var found []marker
	scanned := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner must not abort the scan
		}
		if d.IsDir() {
			if p != root && markerSkipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !markerRelevantFile(d.Name()) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > maxBytes {
			return nil
		}
		scanned++
		if scanned > maxFiles {
			return fs.SkipAll
		}
		found = append(found, scanMarkers(relPath(root, p), p, maxMarkers-len(found))...)
		if len(found) >= maxMarkers {
			return fs.SkipAll
		}
		return nil
	})

	if len(found) == 0 {
		return Result{State: "none"}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].path != found[j].path {
			return found[i].path < found[j].path
		}
		return found[i].line < found[j].line
	})

	lines := make([]string, len(found))
	paths := make([]string, 0, 20)
	seenPath := map[string]bool{}
	for i, mk := range found {
		lines[i] = fmt.Sprintf("%s:%d %s", mk.path, mk.line, mk.text)
		if !seenPath[mk.path] && len(paths) < 20 {
			seenPath[mk.path] = true
			paths = append(paths, mk.path)
		}
	}

	shown := lines
	if len(shown) > maxShown {
		shown = shown[:maxShown]
	}
	detail := strings.Join(shown, "\n")
	if len(lines) > len(shown) {
		detail += fmt.Sprintf("\n… and %d more", len(lines)-len(shown))
	}

	return Result{
		Findings: []Finding{{
			ID:          "workspace.markers",
			Kind:        "workspace",
			Severity:    SevInfo,
			Summary:     fmt.Sprintf("%d unresolved marker(s) (TODO/FIXME/HACK)", len(found)),
			Detail:      detail,
			Paths:       paths,
			Fingerprint: hashLines(lines),
		}},
		State: hashLines(lines),
	}
}

// scanMarkers reads one file and returns its marker lines, up to budget entries.
func scanMarkers(rel, abs string, budget int) []marker {
	if budget <= 0 {
		return nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []marker
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if !hasMarker(text) {
			continue
		}
		out = append(out, marker{path: rel, line: line, text: strings.TrimSpace(text)})
		if len(out) >= budget {
			break
		}
	}
	return out
}

func hasMarker(line string) bool {
	for _, tok := range markerTokens {
		if strings.Contains(line, tok) {
			return true
		}
	}
	return false
}

var markerTokens = []string{"TODO", "FIXME", "XXX", "HACK"}

func markerSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", "build", "target", "obj", ".cache":
		return true
	}
	return strings.HasPrefix(name, ".")
}

func markerRelevantFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java", ".kt",
		".c", ".h", ".cc", ".cpp", ".hpp", ".cs", ".rb", ".php", ".swift",
		".sh", ".sql", ".md":
		return true
	}
	return false
}

func relPath(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return filepath.ToSlash(r)
	}
	return filepath.ToSlash(p)
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
