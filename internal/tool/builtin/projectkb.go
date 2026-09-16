package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/zzycxz/fairpeer/internal/projectkb"
	"github.com/zzycxz/fairpeer/internal/tool"
)

// Project knowledge hub tools (项目知识中枢). They expose the self-maintaining
// project map — modules, docs, saved memory and team progress in one place — so
// a long-running project stays answerable ("这个模块是干嘛的？进度到哪了？").
//
// The hub is injected via SetKnowledgeHub (desktop app.go) and, like the rag
// store, when it is nil the tools report a clear "offline" error instead of
// failing obscurely. Unlike the rag tools these stay VISIBLE in the main-loop
// schema: they are the intended path for the model to survey a project, not an
// auto-injected side channel.

var (
	globalKnowledgeHub         *projectkb.Hub
	globalKnowledgeHubResolver func() *projectkb.Hub
)

// SetKnowledgeHub injects a fixed project knowledge hub. Called once at startup.
func SetKnowledgeHub(h *projectkb.Hub) { globalKnowledgeHub = h }

// SetKnowledgeHubResolver injects a resolver that returns the hub for the
// CURRENT workspace (the desktop app switches tabs, and each workspace has its
// own map). A resolver takes precedence over a fixed hub, so injecting one lets
// the tool surface follow tab switches without re-registering anything.
func SetKnowledgeHubResolver(fn func() *projectkb.Hub) { globalKnowledgeHubResolver = fn }

// requireKnowledgeHub resolves the hub or explains why it is unavailable.
func requireKnowledgeHub() (*projectkb.Hub, error) {
	if globalKnowledgeHubResolver != nil {
		if h := globalKnowledgeHubResolver(); h != nil {
			return h, nil
		}
	}
	if globalKnowledgeHub != nil {
		return globalKnowledgeHub, nil
	}
	return nil, errors.New("project knowledge hub is not available in this session")
}

// KnowledgeTools returns the kb_* tool surface, in registration order.
func KnowledgeTools() []tool.Tool {
	return []tool.Tool{kbSync{}, kbMap{}, kbSearch{}, kbHistory{}, kbRollback{}}
}

// maxKbMapChars caps a kb_map reply so a large project can't flood the context
// in one call; the caller narrows with `kind` or reads the map file directly.
const maxKbMapChars = 12000

// --- kb_sync ---------------------------------------------------------------

type kbSync struct{}

func (kbSync) Name() string { return "kb_sync" }

func (kbSync) Description() string {
	return "Refresh the project knowledge map (项目知识地图) by re-scanning its sources: the code module/package inventory, the repo's Markdown docs, saved memory facts, and team task progress. Change detection is per-item, so this is cheap to re-run and only reports what actually moved. Call it after significant work (new files, finished tasks, new decisions) so the map — and the compact index injected into your context — reflects reality. Returns counts of added/changed/removed items."
}

func (kbSync) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "note":{"type":"string","description":"Optional short note recorded with this sync's revision (e.g. \"接入 P3 后刷新\")"}
}
}`)
}

func (kbSync) ReadOnly() bool { return false }

func (kbSync) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Note string `json:"note"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
	}
	h, err := requireKnowledgeHub()
	if err != nil {
		return "", err
	}
	rep, err := h.Sync("kb_sync")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "项目知识地图已刷新：共 %d 条 · 新增 %d · 变更 %d · 移除 %d · 未变 %d",
		rep.Total, rep.Added, rep.Changed, rep.Removed, rep.Unchanged)
	if rep.Revision != "" {
		b.WriteString(" · 已存快照 " + rep.Revision)
	} else {
		b.WriteString(" · 无变化，未产生新快照")
	}
	if n := strings.TrimSpace(p.Note); n != "" {
		b.WriteString("\n备注：" + n)
	}
	counts := map[string]int{}
	for _, s := range h.Sources() {
		if s.Enabled {
			counts[string(s.Kind)] = s.Count
		}
	}
	b.WriteString("\n各来源：")
	for _, k := range projectkb.Kinds {
		if _, ok := counts[string(k)]; !ok {
			continue
		}
		fmt.Fprintf(&b, "%s %d · ", k.Label(), counts[string(k)])
	}
	return strings.TrimSuffix(b.String(), "· "), nil
}

// --- kb_map ----------------------------------------------------------------

type kbMap struct{}

func (kbMap) Name() string { return "kb_map" }

func (kbMap) Description() string {
	return "Read the project knowledge map (项目知识地图): the interlinked inventory of modules/packages, documents, saved memory and team progress. Use this to get oriented in an unfamiliar or long-running project before making changes — it answers \"what does this codebase consist of and where is each thing\" in one call. Narrow with `kind` to read a single section. For a specific lookup prefer kb_search."
}

func (kbMap) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "kind":{"type":"string","enum":["code","doc","memory","team"],"description":"Limit to one section (default: the whole map)"}
}
}`)
}

func (kbMap) ReadOnly() bool { return true }

func (kbMap) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Kind string `json:"kind"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
	}
	h, err := requireKnowledgeHub()
	if err != nil {
		return "", err
	}
	text := h.Map()
	if k := strings.TrimSpace(p.Kind); k != "" {
		text = sectionOf(text, projectkb.Kind(k))
		if text == "" {
			return "", fmt.Errorf("no section %q in the project map yet — run kb_sync first", k)
		}
	}
	if strings.TrimSpace(text) == "" {
		return "项目知识地图还是空的——先运行 kb_sync 采集来源。", nil
	}
	if r := []rune(text); len(r) > maxKbMapChars {
		text = string(r[:maxKbMapChars]) + "\n… (map truncated; narrow with kind= or read map.md directly)"
	}
	return WrapUntrusted("project-kb", text), nil
}

// sectionOf returns the map subsection whose heading carries the kind's label.
func sectionOf(text string, kind projectkb.Kind) string {
	if !kind.Valid() {
		return ""
	}
	label := "## " + kind.Label()
	lines := strings.Split(text, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), label) {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	// The section ends at the next H2 (or EOF).
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// --- kb_search -------------------------------------------------------------

type kbSearch struct{}

func (kbSearch) Name() string { return "kb_search" }

func (kbSearch) Description() string {
	return "Search the project knowledge map for a keyword — a module name, a file, a saved decision, or a task. Returns ranked pointers (title + where it lives + a one-line summary); follow up by reading the file or task it names. This is the fast path for \"where is X written down / which package does Y\". An empty query lists the map in order."
}

func (kbSearch) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "query":{"type":"string","description":"Keywords (module, path fragment, decision, task title). Empty = browse."},
  "kind":{"type":"string","enum":["code","doc","memory","team"],"description":"Limit to one section"},
  "limit":{"type":"integer","description":"Max results (default 10, max 50)"}
}
}`)
}

func (kbSearch) ReadOnly() bool { return true }

func (kbSearch) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
		Kind  string `json:"kind"`
		Limit int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
	}
	h, err := requireKnowledgeHub()
	if err != nil {
		return "", err
	}
	// A kind filter is applied by widening the candidate list, then filtering,
	// so a narrow section still fills the requested limit.
	want := projectkb.Kind(strings.TrimSpace(p.Kind))
	limit := p.Limit
	if want.Valid() {
		if limit <= 0 || limit > projectkb.MaxSearchLimit {
			limit = projectkb.MaxSearchLimit
		}
	}
	hits := h.Search(p.Query, limit)
	var b strings.Builder
	n := 0
	for _, hit := range hits {
		if want.Valid() && hit.Node.Kind != want {
			continue
		}
		n++
		b.WriteString("- ")
		b.WriteString(hit.Node.Title)
		if ref := hit.Node.Ref; ref != "" && ref != hit.Node.Title {
			b.WriteString(" `" + ref + "`")
		}
		fmt.Fprintf(&b, " [%s]", hit.Node.Kind.Label())
		if hit.Node.Status != "" {
			b.WriteString(" (" + hit.Node.Status + ")")
		}
		if s := strings.TrimSpace(hit.Node.Summary); s != "" {
			b.WriteString(" — " + s)
		}
		b.WriteString("\n")
		if p.Limit > 0 && n >= p.Limit {
			break
		}
	}
	if n == 0 {
		return "没有匹配项。可以先 kb_sync 刷新，或换个关键词。", nil
	}
	return WrapUntrusted("project-kb", b.String()), nil
}

// --- kb_history ------------------------------------------------------------

type kbHistory struct{}

func (kbHistory) Name() string { return "kb_history" }

func (kbHistory) Description() string {
	return "List the project map's revision history (newest first). Each entry is an immutable snapshot of the map written when the map actually changed, and can be restored with kb_rollback. Use it to see how the project's structure and progress evolved, or to undo a bad sync."
}

func (kbHistory) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "limit":{"type":"integer","description":"Max entries (default 15)"}
}
}`)
}

func (kbHistory) ReadOnly() bool { return true }

func (kbHistory) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Limit int `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
	}
	if p.Limit <= 0 {
		p.Limit = 15
	}
	h, err := requireKnowledgeHub()
	if err != nil {
		return "", err
	}
	revs := h.History()
	if len(revs) == 0 {
		return "还没有修订记录——运行 kb_sync 后会开始留痕。", nil
	}
	var b strings.Builder
	for i, r := range revs {
		if i >= p.Limit {
			break
		}
		fmt.Fprintf(&b, "- %s · %s · %d 条 · %s",
			r.ID, r.At.Local().Format("2006-01-02 15:04"), r.Nodes, r.Trigger)
		if r.Note != "" {
			b.WriteString(" — " + r.Note)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n用 kb_rollback(revision=\"<ID>\") 回滚到某一版。")
	return b.String(), nil
}

// --- kb_rollback -----------------------------------------------------------

type kbRollback struct{}

func (kbRollback) Name() string { return "kb_rollback" }

func (kbRollback) Description() string {
	return "Restore the project knowledge map to a previous revision from kb_history. The current state is snapshotted first, so a rollback is itself undoable. Use this to discard a sync that produced a wrong or noisy map. This only rewrites the map (kb.json/map.md) — it never touches the source files, memory or team data it indexes."
}

func (kbRollback) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "revision":{"type":"string","description":"Revision ID from kb_history (e.g. 20260916-211530-ab12cd34)"}
},
"required":["revision"]
}`)
}

func (kbRollback) ReadOnly() bool { return false }

func (kbRollback) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Revision) == "" {
		return "", errors.New("revision is required (see kb_history)")
	}
	h, err := requireKnowledgeHub()
	if err != nil {
		return "", err
	}
	if err := h.Rollback(strings.TrimSpace(p.Revision)); err != nil {
		return "", err
	}
	return "已回滚到 " + strings.TrimSpace(p.Revision) +
		"（当前状态也已存为快照，可再回滚回来）。共 " + strconv.Itoa(len(h.Nodes())) + " 条。", nil
}
