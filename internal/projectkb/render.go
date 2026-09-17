package projectkb

import (
	"strconv"
	"strings"
	"time"
)

// DefaultIndexMaxChars caps the compact index injected into the system prompt.
// Mirrors memory.DefaultPromptIndexMaxChars: a hard backstop, not a target, so
// a large project still contributes a bounded, cache-stable prefix.
const DefaultIndexMaxChars = 1200

// RenderMap renders the project map (map.md) — the human- and model-readable
// artifact of 整理映射. It is deterministic for a given node set, so a sync
// produces a clean textual diff and a revision is only written when the map
// genuinely moved.
func RenderMap(st State) string {
	var b strings.Builder
	b.WriteString("# 项目知识地图\n\n")
	when := st.Updated
	if when.IsZero() {
		when = time.Now().UTC()
	}
	b.WriteString("> 工作目录 `" + firstNonBlank(st.CWD, ".") + "` · 共 " +
		strconv.Itoa(len(st.Nodes)) + " 条 · 指纹 " + shortDigest(st.Digest) +
		" · 更新于 " + when.Local().Format("2006-01-02 15:04") + "\n")

	counts := Counts(st.Nodes)
	for _, kind := range Kinds {
		nodes := nodesOf(st.Nodes, kind)
		if len(nodes) == 0 {
			continue
		}
		b.WriteString("\n## " + kind.Label() + "（" + strconv.Itoa(counts[string(kind)]) + "）\n\n")
		for _, n := range nodes {
			b.WriteString("- " + mapLine(n) + "\n")
		}
	}
	return b.String()
}

// mapLine renders one map entry. A document becomes a relative Markdown link so
// the map is navigable inside an editor; everything else uses a bold name plus
// its ref, because its "ref" is an identifier rather than a path.
func mapLine(n Node) string {
	var b strings.Builder
	if n.Kind == KindDoc && n.Ref != "" {
		b.WriteString("[" + n.Title + "](" + n.Ref + ")")
	} else {
		b.WriteString("**" + n.Title + "**")
		if n.Ref != "" && n.Ref != n.Title &&
			!strings.HasPrefix(n.Ref, "team:") && !strings.HasPrefix(n.Ref, "task:") {
			b.WriteString(" `" + n.Ref + "`")
		}
	}
	if n.Status != "" {
		b.WriteString(" (" + n.Status + ")")
	}
	if s := strings.TrimSpace(n.Summary); s != "" {
		b.WriteString(" — " + s)
	}
	return b.String()
}

// RenderIndex renders the compact index folded into the system prompt. It is a
// pointer, not the content: the model sees the shape of the project and pulls
// detail with kb_map / kb_search.
func RenderIndex(st State, maxChars int) string {
	if len(st.Nodes) == 0 {
		return ""
	}
	if maxChars <= 0 {
		maxChars = DefaultIndexMaxChars
	}
	counts := Counts(st.Nodes)
	var lines []string
	lines = append(lines, "规模："+strconv.Itoa(len(st.Nodes))+" 条 · 模块 "+
		strconv.Itoa(counts[string(KindCode)])+" · 文档 "+
		strconv.Itoa(counts[string(KindDoc)])+" · 记忆 "+
		strconv.Itoa(counts[string(KindMemory)])+" · 团队 "+
		strconv.Itoa(counts[string(KindTeam)]))

	if mods := pkgRefs(st.Nodes, 12); len(mods) > 0 {
		lines = append(lines, "关键模块："+strings.Join(mods, "、"))
	}
	if running := teamTitles(st.Nodes, 6, func(n Node) bool {
		return n.Status == "running"
	}); len(running) > 0 {
		lines = append(lines, "进行中："+strings.Join(running, "、"))
	}
	if failed := teamTitles(st.Nodes, 6, func(n Node) bool {
		return n.Status == "failed" || n.Status == "stale" || n.Status == "waiting"
	}); len(failed) > 0 {
		lines = append(lines, "待关注："+strings.Join(failed, "、"))
	}
	// The incremental summary's one-line gist: the single most useful thing to
	// tell a session that just resumed — what moved last time. Reading it needs
	// no scan (it is persisted in kb.json), so it costs nothing at boot.
	if gist := st.LastChangeLine(); gist != "" {
		when := ""
		if !st.LastChangeAt.IsZero() {
			when = "（" + st.LastChangeAt.Local().Format("01-02 15:04") + "）"
		}
		lines = append(lines, "上次变更："+gist+when)
	}
	if !st.Updated.IsZero() {
		lines = append(lines, "同步于 "+st.Updated.Local().Format("2006-01-02 15:04"))
	}

	head := "## 项目知识地图（`kb_map` 读全文 · `kb_search` 检索 · `kb_sync` 刷新）\n\n"
	var b strings.Builder
	b.WriteString(head)
	for i, l := range lines {
		b.WriteString("- ")
		b.WriteString(l)
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	out := b.String()
	if r := []rune(out); len(r) > maxChars {
		// Keep the result genuinely within the budget: the marker itself is
		// reserved out of maxChars rather than appended on top of it, so the
		// injected prefix can never exceed the configured cap.
		keep := maxChars - len([]rune(indexTruncMark))
		if keep < 0 {
			keep = 0
		}
		out = string(r[:keep]) + indexTruncMark
	}
	return out
}

// indexTruncMark tells the model the index was clipped, so it searches instead
// of assuming the list is complete.
const indexTruncMark = "\n… (index truncated; call kb_map for the full map)"

// nodesOf returns the nodes of one kind, preserving the stored order.
func nodesOf(nodes []Node, kind Kind) []Node {
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Kind == kind {
			out = append(out, n)
		}
	}
	return out
}

// pkgRefs lists up to max package refs, deduplicated, for the index's
// "关键模块" line.
func pkgRefs(nodes []Node, max int) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range nodes {
		if n.Kind != KindCode || !strings.HasPrefix(n.ID, string(KindCode)+":pkg/") {
			continue
		}
		ref := firstNonBlank(n.Ref, n.Title)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
		if len(out) >= max {
			break
		}
	}
	return out
}

// teamTitles lists up to max team-node titles matching pred.
func teamTitles(nodes []Node, max int, pred func(Node) bool) []string {
	var out []string
	for _, n := range nodes {
		if n.Kind != KindTeam || !strings.HasPrefix(n.ID, string(KindTeam)+":task/") {
			continue
		}
		if !pred(n) {
			continue
		}
		out = append(out, clip(n.Title, 24))
		if len(out) >= max {
			break
		}
	}
	return out
}
