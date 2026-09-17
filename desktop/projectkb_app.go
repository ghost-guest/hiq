package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/zzycxz/fairpeer/internal/config"
	projectkbpkg "github.com/zzycxz/fairpeer/internal/projectkb"
	"github.com/zzycxz/fairpeer/internal/tool/builtin"
)

// Project knowledge hub (项目知识中枢) desktop wiring.
//
// The hub itself is a pure, file-backed package (internal/projectkb): given a
// data root and a working directory it collects the code/doc/memory/team
// sources into a self-maintaining project map with revision history. This file
// supplies what the domain package deliberately does not own — the data root
// (config.MemoryUserDir), the working directory (the active tab's workspace) and
// the UI bridge.
//
// One hub exists per workspace root, cached on the App, and the agent's kb_*
// tools reach the right one through a resolver, so a tab switch needs no tool
// re-registration.

// KBView is the panel's status snapshot.
type KBView struct {
	Dir       string         `json:"dir"`
	CWD       string         `json:"cwd"`
	Total     int            `json:"total"`
	Counts    map[string]int `json:"counts"`
	Sources   []KBSourceView `json:"sources"`
	Updated   string         `json:"updated"`
	Digest    string         `json:"digest"`
	Revisions int            `json:"revisions"`
	// Watching / WatchSyncs report the 变更即同步 watcher's live state.
	Watching   bool `json:"watching"`
	WatchSyncs int  `json:"watchSyncs"`
	// LastChange is the one-line gist of the most recent real change and
	// LastChangeAt when it happened. Both survive a no-op sync, so the panel can
	// always answer 自上次以来有什么变化.
	LastChange   string `json:"lastChange,omitempty"`
	LastChangeAt string `json:"lastChangeAt,omitempty"`
	// Note carries a one-line outcome of the last action (sync report /
	// rollback confirmation) so the panel can show it without a second call.
	Note string `json:"note,omitempty"`
}

// KBSourceView is one source switch in the panel.
type KBSourceView struct {
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
	Count   int    `json:"count"`
	Note    string `json:"note"`
}

// KBSearchHitView is one search result row.
type KBSearchHitView struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Title   string `json:"title"`
	Ref     string `json:"ref"`
	Summary string `json:"summary"`
	Status  string `json:"status"`
	Score   int    `json:"score"`
}

// KBRevisionView is one row of the revision history.
type KBRevisionView struct {
	ID      string `json:"id"`
	At      string `json:"at"`
	Trigger string `json:"trigger"`
	Note    string `json:"note"`
	Nodes   int    `json:"nodes"`
}

// initProjectKB installs the hub resolver once at startup. Resolving through
// the active workspace (rather than binding one hub) is what lets the kb_* tools
// follow a tab switch for free.
func (a *App) initProjectKB() {
	builtin.SetKnowledgeHubResolver(func() *projectkbpkg.Hub {
		h, err := a.knowledgeHub()
		if err != nil {
			return nil
		}
		return h
	})
	// First run for this workspace: build the map once, in the background, so
	// the very first kb_map/kb_search has something to show. Cheap on repeat
	// runs because a persisted map is simply re-read, but we still avoid
	// blocking startup.
	// 变更即同步 is on by default: the whole point is that the map keeps up
	// while you work, not that you remember to press 同步.
	a.kbMu.Lock()
	a.kbWatchEnabled = true
	a.kbMu.Unlock()
	a.ensureKBWatcher()

	go func() {
		h, err := a.knowledgeHub()
		if err != nil {
			return
		}
		if len(h.Nodes()) > 0 {
			return // already built
		}
		if _, err := h.Sync("startup"); err != nil {
			return
		}
		a.emitKBChanged()
	}()
}

// ensureKBWatcher (re)targets the 变更即同步 watcher at the ACTIVE workspace: it
// starts polling when enabled and stops when not. It runs at startup and from
// every status read, so a tab switch re-binds the watcher lazily instead of
// leaking one watcher per workspace.
func (a *App) ensureKBWatcher() {
	h, err := a.knowledgeHub()
	if err != nil {
		return
	}
	key := h.Dir()

	a.kbMu.Lock()
	if !a.kbWatchEnabled {
		old := a.kbWatch
		a.kbWatch, a.kbWatchKey = nil, ""
		a.kbMu.Unlock()
		if old != nil {
			old.Stop()
		}
		return
	}
	if a.kbWatchKey == key && a.kbWatch != nil && a.kbWatch.Running() {
		a.kbMu.Unlock()
		return
	}
	old := a.kbWatch
	w := h.Watch(projectkbpkg.WatchOptions{Roots: a.kbWatchRoots(h)}, func(projectkbpkg.Report) {
		a.emitKBChanged()
	})
	a.kbWatch, a.kbWatchKey = w, key
	a.kbMu.Unlock()

	if old != nil {
		old.Stop()
	}
	w.Start()
}

// kbWatchRoots is what the watcher polls: the project tree itself (code + docs)
// plus the team store, so finishing a card also refreshes the map. The memory
// tree is deliberately out of it — memory writes already go through the app.
func (a *App) kbWatchRoots(h *projectkbpkg.Hub) []string {
	roots := []string{h.CWD()}
	if root := config.MemoryUserDir(); strings.TrimSpace(root) != "" {
		roots = append(roots, filepath.Join(root, "teams"))
	}
	return roots
}

// knowledgeHub resolves (and caches) the hub for the active workspace. The hub
// lives under the memory data root, so project knowledge follows the user's
// configured data location instead of the system config dir — the same rule the
// team store follows.
func (a *App) knowledgeHub() (*projectkbpkg.Hub, error) {
	root := config.MemoryUserDir()
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("记忆数据根不可用")
	}
	cwd := a.kbWorkspaceRoot()
	if cwd == "" {
		return nil, fmt.Errorf("没有可用的工作目录")
	}
	profile := a.activeProfileKey()

	a.kbMu.Lock()
	defer a.kbMu.Unlock()
	if a.kbHubs == nil {
		a.kbHubs = map[string]*projectkbpkg.Hub{}
	}
	key := cwd + "\x00" + profile
	if h, ok := a.kbHubs[key]; ok {
		return h, nil
	}
	h, err := projectkbpkg.New(root, cwd, profile)
	if err != nil {
		return nil, err
	}
	a.kbHubs[key] = h
	return h, nil
}

// kbWorkspaceRoot is the directory the map describes: the active tab's
// workspace, falling back to the process working directory when no tab is open.
func (a *App) kbWorkspaceRoot() string {
	if _, root := a.activeCtrlAndRoot(); strings.TrimSpace(root) != "" {
		return root
	}
	if tab := a.activeTab(); tab != nil && strings.TrimSpace(tab.WorkspaceRoot) != "" {
		return tab.WorkspaceRoot
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

// emitKBChanged pushes the refreshed status to the panel.
func (a *App) emitKBChanged() {
	if a.ctx == nil {
		return
	}
	view, err := a.KnowledgeStatus()
	if err != nil {
		return
	}
	runtime.EventsEmit(a.ctx, "kb:changed", view)
}

// --- bindings ---------------------------------------------------------------

// KnowledgeStatus reports the active workspace's map state.
func (a *App) KnowledgeStatus() (KBView, error) {
	h, err := a.knowledgeHub()
	if err != nil {
		// A missing hub is a normal state (no workspace / no memory root), so
		// report an empty view instead of an error the panel would have to
		// special-case.
		return KBView{Sources: emptyKBSources()}, nil
	}
	// Reading the status is also the cheapest place to notice a workspace
	// switch, so the auto-sync watcher follows the active tab from here.
	a.ensureKBWatcher()
	return a.kbView(h), nil
}

// KnowledgeWatch turns 变更即同步 on or off for the active workspace. With it on,
// the project map re-syncs itself (debounced) as the tree changes, so a long dev
// session can never leave the map stale.
func (a *App) KnowledgeWatch(enable bool) (KBView, error) {
	a.kbMu.Lock()
	a.kbWatchEnabled = enable
	a.kbMu.Unlock()
	a.ensureKBWatcher()
	return a.KnowledgeStatus()
}

// KnowledgeSummary returns the incremental summary (增量摘要) of the most recent
// change, or of one revision when an ID is given — the "what moved?" counterpart
// to KnowledgeMap's "what is there?".
func (a *App) KnowledgeSummary(revision string) (string, error) {
	h, err := a.knowledgeHub()
	if err != nil {
		return "", err
	}
	if id := strings.TrimSpace(revision); id != "" {
		return h.RevisionSummary(id)
	}
	st := h.State()
	if st.LastDiff == nil || st.LastDiff.IsEmpty() {
		return "", nil
	}
	var b strings.Builder
	if !st.LastChangeAt.IsZero() {
		b.WriteString("最近一次变更：" + st.LastChangeAt.Local().Format("2006-01-02 15:04") + "\n\n")
	}
	b.WriteString(st.LastDiff.Markdown(0))
	return b.String(), nil
}

// KnowledgeSync refreshes the map from every enabled source and pushes the
// result to the panel.
func (a *App) KnowledgeSync(note string) (KBView, error) {
	h, err := a.knowledgeHub()
	if err != nil {
		return KBView{}, err
	}
	rep, err := h.Sync(strings.TrimSpace(note))
	if err != nil {
		return KBView{}, err
	}
	view := a.kbView(h)
	view.Note = kbReportNote(rep)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "kb:changed", view)
	}
	return view, nil
}

// KnowledgeMap returns the rendered map, optionally limited to one section.
func (a *App) KnowledgeMap(kind string) (string, error) {
	h, err := a.knowledgeHub()
	if err != nil {
		return "", err
	}
	return h.Map(), nil
}

// KnowledgeSearch ranks indexed items against a query.
func (a *App) KnowledgeSearch(query, kind string, limit int) ([]KBSearchHitView, error) {
	h, err := a.knowledgeHub()
	if err != nil {
		return nil, err
	}
	want := projectkbpkg.Kind(strings.TrimSpace(kind))
	if want.Valid() {
		// Widen the candidate set before filtering, so a narrow section can
		// still fill the requested limit.
		n := limit
		if n <= 0 || n > projectkbpkg.MaxSearchLimit {
			n = projectkbpkg.MaxSearchLimit
		}
		hits := h.Search(query, n)
		out := make([]KBSearchHitView, 0, len(hits))
		for _, hit := range hits {
			if hit.Node.Kind != want {
				continue
			}
			out = append(out, toKBSearchHit(hit))
			if limit > 0 && len(out) >= limit {
				break
			}
		}
		return out, nil
	}
	hits := h.Search(query, limit)
	out := make([]KBSearchHitView, 0, len(hits))
	for _, hit := range hits {
		out = append(out, toKBSearchHit(hit))
	}
	return out, nil
}

// KnowledgeHistory lists the map's revision history.
func (a *App) KnowledgeHistory(limit int) ([]KBRevisionView, error) {
	h, err := a.knowledgeHub()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	revs := h.History()
	out := make([]KBRevisionView, 0, len(revs))
	for i, r := range revs {
		if i >= limit {
			break
		}
		out = append(out, KBRevisionView{
			ID:      r.ID,
			At:      r.At.Local().Format("2006-01-02 15:04"),
			Trigger: r.Trigger,
			Note:    r.Note,
			Nodes:   r.Nodes,
		})
	}
	return out, nil
}

// KnowledgeRollback restores the map to a previous revision.
func (a *App) KnowledgeRollback(revision string) (KBView, error) {
	h, err := a.knowledgeHub()
	if err != nil {
		return KBView{}, err
	}
	if err := h.Rollback(strings.TrimSpace(revision)); err != nil {
		return KBView{}, err
	}
	view := a.kbView(h)
	view.Note = "已回滚到 " + strings.TrimSpace(revision)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "kb:changed", view)
	}
	return view, nil
}

// KnowledgeSetSource toggles one source and re-syncs so the map reflects it.
func (a *App) KnowledgeSetSource(kind string, enabled bool) (KBView, error) {
	h, err := a.knowledgeHub()
	if err != nil {
		return KBView{}, err
	}
	k := projectkbpkg.Kind(strings.TrimSpace(kind))
	if err := h.SetSource(k, enabled); err != nil {
		return KBView{}, err
	}
	if _, err := h.Sync("source toggle"); err != nil {
		return KBView{}, err
	}
	view := a.kbView(h)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "kb:changed", view)
	}
	return view, nil
}

// KnowledgeOpenDir reveals the hub's directory (map.md, kb.json, revisions/).
func (a *App) KnowledgeOpenDir() error {
	h, err := a.knowledgeHub()
	if err != nil {
		return err
	}
	dir := h.Dir()
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("目录还不存在：先做一次同步")
	}
	return openInFileExplorer(dir)
}

// --- conversions ------------------------------------------------------------

// kbView snapshots a hub for the panel.
func (a *App) kbView(h *projectkbpkg.Hub) KBView {
	st := h.State()
	counts := projectkbpkg.Counts(st.Nodes)
	srcs := make([]KBSourceView, 0, len(st.Sources))
	for _, s := range st.Sources {
		srcs = append(srcs, KBSourceView{
			Kind:    string(s.Kind),
			Label:   s.Kind.Label(),
			Enabled: s.Enabled,
			Count:   s.Count,
			Note:    s.Note,
		})
	}
	updated := ""
	if !st.Updated.IsZero() {
		updated = st.Updated.Local().Format("2006-01-02 15:04")
	}
	lastAt := ""
	if !st.LastChangeAt.IsZero() {
		lastAt = st.LastChangeAt.Local().Format("2006-01-02 15:04")
	}
	watching, syncs := a.kbWatchState()
	return KBView{
		Dir:          h.Dir(),
		CWD:          st.CWD,
		Total:        len(st.Nodes),
		Counts:       counts,
		Sources:      srcs,
		Updated:      updated,
		Digest:       st.Digest,
		Revisions:    len(h.History()),
		Watching:     watching,
		WatchSyncs:   syncs,
		LastChange:   st.LastChangeLine(),
		LastChangeAt: lastAt,
	}
}

// kbWatchState reports the watcher's liveness without holding kbMu while the
// watcher might call back into the app.
func (a *App) kbWatchState() (bool, int) {
	a.kbMu.Lock()
	w := a.kbWatch
	a.kbMu.Unlock()
	if w == nil {
		return false, 0
	}
	running, syncs, _ := w.Stats()
	return running, syncs
}

// emptyKBSources lists the known sources as disabled, so the panel can render
// switches even when no hub is available yet.
func emptyKBSources() []KBSourceView {
	out := make([]KBSourceView, 0, len(projectkbpkg.Kinds))
	for _, k := range projectkbpkg.Kinds {
		out = append(out, KBSourceView{Kind: string(k), Label: k.Label(), Enabled: true})
	}
	return out
}

func toKBSearchHit(hit projectkbpkg.Hit) KBSearchHitView {
	return KBSearchHitView{
		ID:      hit.Node.ID,
		Kind:    string(hit.Node.Kind),
		Label:   hit.Node.Kind.Label(),
		Title:   hit.Node.Title,
		Ref:     hit.Node.Ref,
		Summary: hit.Node.Summary,
		Status:  hit.Node.Status,
		Score:   hit.Score,
	}
}

// kbReportNote renders a sync report as one line for the panel.
func kbReportNote(rep projectkbpkg.Report) string {
	parts := []string{fmt.Sprintf("新增 %d", rep.Added), fmt.Sprintf("变更 %d", rep.Changed)}
	if rep.Removed > 0 {
		parts = append(parts, fmt.Sprintf("移除 %d", rep.Removed))
	}
	parts = append(parts, fmt.Sprintf("共 %d", rep.Total))
	if rep.Revision != "" {
		parts = append(parts, "已存快照 "+rep.Revision)
	}
	return "同步完成：" + strings.Join(parts, " · ")
}
