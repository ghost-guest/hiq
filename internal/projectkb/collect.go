package projectkb

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/memory"
	"github.com/zzycxz/hiq/internal/taskmonitor"
	"github.com/zzycxz/hiq/internal/team"
)

// Scan bounds. Every collector is capped so a sync stays cheap on a large repo
// (the hub runs on every explicit refresh and after team changes).
const (
	maxDocFiles  = 400
	maxCodeNodes = 300
	maxTeamNodes = 300
	maxScanDepth = 5
	maxDocBytes  = 256 * 1024
)

// skipDirs are directory names never worth indexing: VCS metadata, dependency
// trees, build output and generated bundles.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "out": true, "target": true, "bin": true,
	".cache": true, ".codegraph": true, ".workbuddy": true, ".idea": true,
	".vscode": true, "__pycache__": true, ".venv": true, "venv": true,
	"wailsjs": true, "site-packages": true,
}

// collectDocs indexes the repo's Markdown. Title comes from the first H1 when
// present (falling back to the filename) and the summary from the first body
// line, so a map reader can tell what each document is for.
func collectDocs(cwd string) []Node {
	root := absOfPath(cwd)
	var out []Node
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return fs.SkipDir
			}
			if depthOf(root, path) > maxScanDepth {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(name), ".md") {
			return nil
		}
		if len(out) >= maxDocFiles {
			return fs.SkipAll
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = name
		}
		rel = filepath.ToSlash(rel)

		title := strings.TrimSuffix(name, filepath.Ext(name))
		summary := ""
		var mod time.Time
		var size int64
		if info, ierr := d.Info(); ierr == nil {
			size = info.Size()
			mod = info.ModTime()
		}
		if b, rerr := os.ReadFile(path); rerr == nil && len(b) <= maxDocBytes {
			body := string(b)
			if h := firstHeading(body); h != "" {
				title = h
			}
			summary = bodyLine(body, 160)
		}
		out = append(out, Node{
			ID:      nodeID(KindDoc, rel),
			Kind:    KindDoc,
			Title:   title,
			Ref:     rel,
			Summary: summary,
			FP: fingerprint(rel, strconv.FormatInt(size, 10),
				strconv.FormatInt(mod.Unix(), 10)),
			Updated: mod,
		})
		return nil
	})
	sortNodes(out)
	return out
}

// collectMemories indexes the durable L1/L2 facts. L3 (session working memory)
// is deliberately excluded: it is scratch space, not project knowledge.
func collectMemories(root, cwd, profile string) []Node {
	store := memory.StoreFor(root, cwd, profile)
	var out []Node
	for _, m := range store.List() {
		lvl := memory.LevelOf(m)
		if lvl == memory.LevelSession {
			continue
		}
		title := strings.TrimSpace(m.Name)
		if title == "" {
			continue
		}
		out = append(out, Node{
			ID:      nodeID(KindMemory, title),
			Kind:    KindMemory,
			Title:   title,
			Ref:     string(lvl),
			Summary: bodyLine(m.Body, 160),
			Tags:    append([]string(nil), m.Tags...),
			FP:      fingerprint(title, m.Body, string(m.Type), string(lvl)),
			Updated: m.CreatedAt,
		})
	}
	sortNodes(out)
	return out
}

// collectTeams indexes each team project (goal + progress) and each card, so
// "项目进度" is answerable and searchable rather than only visible in the board.
func collectTeams(root string) []Node {
	b, err := os.ReadFile(filepath.Join(root, "teams", "team_projects.json"))
	if err != nil {
		return nil
	}
	var teams []team.Team
	if err := json.Unmarshal(b, &teams); err != nil {
		return nil
	}
	var out []Node
	for _, tm := range teams {
		done, running, failed := 0, 0, 0
		for _, tk := range tm.Tasks {
			switch tk.Status {
			case taskmonitor.TaskStateSucceeded:
				done++
			case taskmonitor.TaskStateRunning:
				running++
			case taskmonitor.TaskStateFailed, taskmonitor.TaskStateStale:
				failed++
			}
		}
		total := len(tm.Tasks)
		summary := strconv.Itoa(done) + "/" + strconv.Itoa(total) + " 完成"
		if running > 0 {
			summary += " · " + strconv.Itoa(running) + " 进行中"
		}
		if failed > 0 {
			summary += " · " + strconv.Itoa(failed) + " 失败"
		}
		if goal := firstNonBlank(tm.Context.Goal, tm.Goal); goal != "" {
			summary = clip(collapseSpace(goal), 80) + " — " + summary
		}
		out = append(out, Node{
			ID:      nodeID(KindTeam, "team/"+tm.ID),
			Kind:    KindTeam,
			Title:   firstNonBlank(strings.TrimSpace(tm.Name), "未命名团队"),
			Ref:     "team:" + tm.ID,
			Summary: summary,
			FP: fingerprint("team", tm.ID, summary, strconv.Itoa(total),
				strconv.Itoa(tm.Context.Version)),
			Updated: tm.UpdatedAt,
		})
		for _, tk := range tm.Tasks {
			if len(out) >= maxTeamNodes {
				break
			}
			who := firstNonBlank(tm.MemberName(tk.AssigneeID), "未分配")
			sum := string(tk.Status) + " · " + who
			if d := strings.TrimSpace(tk.Desc); d != "" {
				sum += " — " + clip(collapseSpace(d), 100)
			}
			if e := strings.TrimSpace(tk.Error); e != "" {
				sum += " ！" + clip(collapseSpace(e), 60)
			}
			out = append(out, Node{
				ID:      nodeID(KindTeam, "task/"+tk.ID),
				Kind:    KindTeam,
				Title:   firstNonBlank(strings.TrimSpace(tk.Title), "未命名任务"),
				Ref:     "task:" + tk.ID,
				Summary: sum,
				Status:  string(tk.Status),
				FP: fingerprint("task", tk.ID, string(tk.Status), tk.Deliverable,
					tk.Progress, tk.Error, tk.AssigneeID, strconv.Itoa(tk.Attempts)),
				Updated: tk.UpdatedAt,
			})
		}
	}
	sortNodes(out)
	return out
}

// collectCode builds the module + package inventory. This is the "整理映射"
// backbone for a large project: which modules exist, and what each package is
// for (taken from its own package doc line, so the map never invents intent).
func collectCode(cwd string) []Node {
	root := absOfPath(cwd)
	var out []Node
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return fs.SkipDir
			}
			if depthOf(root, path) > 3 {
				return fs.SkipDir
			}
			return nil
		}
		if name != "go.mod" {
			return nil
		}
		dir := filepath.Dir(path)
		rel, rerr := filepath.Rel(root, dir)
		if rerr != nil {
			rel = ""
		}
		rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
		if rel == "." {
			rel = ""
		}
		mod := readModuleLine(path)
		if mod == "" {
			return nil
		}
		out = append(out, Node{
			ID:      nodeID(KindCode, "mod/"+rel),
			Kind:    KindCode,
			Title:   mod,
			Ref:     firstNonBlank(rel, "."),
			Summary: "Go module",
			FP:      fingerprint("mod", rel, mod),
		})
		return nil
	})
	for _, base := range []string{"internal", "cmd", "pkg"} {
		entries, err := os.ReadDir(filepath.Join(root, base))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || len(out) >= maxCodeNodes {
				continue
			}
			rel := base + "/" + e.Name()
			doc := packageDoc(filepath.Join(root, base, e.Name()))
			out = append(out, Node{
				ID:      nodeID(KindCode, "pkg/"+rel),
				Kind:    KindCode,
				Title:   rel,
				Ref:     rel,
				Summary: doc,
				FP:      fingerprint("pkg", rel, doc),
			})
		}
	}
	sortNodes(out)
	return out
}

// sortNodes orders deterministically: source order, then title, then ID. A
// stable order is what lets the rendered map diff cleanly between syncs.
func sortNodes(nodes []Node) {
	rank := make(map[Kind]int, len(Kinds))
	for i, k := range Kinds {
		rank[k] = i
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if rank[a.Kind] != rank[b.Kind] {
			return rank[a.Kind] < rank[b.Kind]
		}
		if a.Title != b.Title {
			return a.Title < b.Title
		}
		return a.ID < b.ID
	})
}

// firstHeading returns the text of the first ATX H1 in body, or "".
func firstHeading(body string) string {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "# ") {
			return clip(strings.TrimSpace(strings.TrimPrefix(t, "# ")), 80)
		}
	}
	return ""
}

// readModuleLine returns the module path declared by a go.mod, or "".
func readModuleLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "module ") {
			return clip(strings.TrimSpace(strings.TrimPrefix(t, "module ")), 80)
		}
	}
	return ""
}

// packageDoc extracts a package's own doc line ("// Package x ...") from the
// first non-test Go file that carries one. Reading at most a few files keeps
// the code collector cheap even on a wide package.
func packageDoc(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") ||
			strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		if checked >= 4 {
			break
		}
		checked++
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			continue
		}
		if doc := packageComment(string(b)); doc != "" {
			return doc
		}
	}
	return ""
}

// packageComment returns the most descriptive line of a file's leading doc
// comment block: a "Package …" line when present, otherwise the first line.
func packageComment(src string) string {
	var block []string
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "//") {
			block = append(block, strings.TrimSpace(strings.TrimPrefix(t, "//")))
			continue
		}
		if t == "" {
			if len(block) > 0 {
				break
			}
			continue
		}
		break
	}
	for _, l := range block {
		if strings.HasPrefix(l, "Package ") {
			return clip(collapseSpace(l), 140)
		}
	}
	for _, l := range block {
		if l != "" {
			return clip(collapseSpace(l), 140)
		}
	}
	return ""
}

// absOfPath returns the cleaned absolute form of p (falling back to the
// original when Abs fails).
func absOfPath(p string) string {
	if p == "" {
		return "."
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

// depthOf reports how many directory levels path sits below root.
func depthOf(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(rel), "/"))
}

// firstNonBlank returns the first argument that is non-blank after trimming.
func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
