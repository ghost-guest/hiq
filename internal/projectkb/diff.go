package projectkb

import (
	"strconv"
	"strings"
)

// Incremental diff + summary.
//
// A sync that only reports counters ("added 3, changed 2") tells a reader that
// something moved but not WHAT moved — which is useless for the one question a
// long-running project actually asks: "what happened since I last looked?".
//
// Diff answers that structurally: every changed node is captured with its kind,
// its identity and its before/after summary, and Diff.Markdown renders the
// bounded prose that the panel, the revision history and the kb_summary tool all
// read. The rendering is deterministic (no model call), so it is free and
// reproducible; a model-narrated version is layered on top by the desktop.

// ChangeOp labels what happened to one node between two syncs.
type ChangeOp string

const (
	OpAdded   ChangeOp = "added"
	OpChanged ChangeOp = "changed"
	OpRemoved ChangeOp = "removed"
)

// Change is one node-level change.
type Change struct {
	Op     ChangeOp `json:"op"`
	Kind   Kind     `json:"kind"`
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Ref    string   `json:"ref,omitempty"`
	Before string   `json:"before,omitempty"`
	After  string   `json:"after,omitempty"`
}

// Diff is a structured description of what moved between two syncs.
type Diff struct {
	Added   []Change `json:"added,omitempty"`
	Changed []Change `json:"changed,omitempty"`
	Removed []Change `json:"removed,omitempty"`
}

// IsEmpty reports whether nothing moved.
func (d Diff) IsEmpty() bool {
	return len(d.Added) == 0 && len(d.Changed) == 0 && len(d.Removed) == 0
}

// Count is the number of changed nodes.
func (d Diff) Count() int { return len(d.Added) + len(d.Changed) + len(d.Removed) }

// All returns every change in render order: added → changed → removed.
func (d Diff) All() []Change {
	out := make([]Change, 0, d.Count())
	out = append(out, d.Added...)
	out = append(out, d.Changed...)
	out = append(out, d.Removed...)
	return out
}

// Kinds tallies changed nodes per source kind.
func (d Diff) Kinds() map[string]int {
	out := map[string]int{}
	for _, c := range d.All() {
		out[string(c.Kind)]++
	}
	return out
}

// Line renders a one-line gist (panel header + prompt index).
func (d Diff) Line() string {
	if d.IsEmpty() {
		return "无变化"
	}
	var parts []string
	if n := len(d.Added); n > 0 {
		parts = append(parts, "新增 "+strconv.Itoa(n))
	}
	if n := len(d.Changed); n > 0 {
		parts = append(parts, "变更 "+strconv.Itoa(n))
	}
	if n := len(d.Removed); n > 0 {
		parts = append(parts, "移除 "+strconv.Itoa(n))
	}
	return strings.Join(parts, " · ")
}

// DefaultSummaryMaxItems bounds one rendered summary. A first sync on a large
// repo can touch hundreds of nodes; the summary stays a summary.
const DefaultSummaryMaxItems = 40

// Markdown renders the incremental summary: a per-op, per-kind bullet list with
// each item's before → after gist. maxItems <= 0 uses DefaultSummaryMaxItems.
func (d Diff) Markdown(maxItems int) string {
	if d.IsEmpty() {
		return ""
	}
	if maxItems <= 0 {
		maxItems = DefaultSummaryMaxItems
	}
	var b strings.Builder
	b.WriteString("### 本次变更（" + d.Line() + "）\n")
	writeGroup := func(label string, cs []Change) {
		if len(cs) == 0 {
			return
		}
		b.WriteString("\n**" + label + "（" + strconv.Itoa(len(cs)) + "）**\n")
		for i, c := range cs {
			if i >= maxItems {
				b.WriteString("- … 其余 " + strconv.Itoa(len(cs)-maxItems) + " 项\n")
				break
			}
			b.WriteString("- [" + c.Kind.Label() + "] " + changeLine(c) + "\n")
		}
	}
	writeGroup("新增", d.Added)
	writeGroup("变更", d.Changed)
	writeGroup("移除", d.Removed)
	return b.String()
}

// changeLine renders one change: title, ref, and the before→after gist.
func changeLine(c Change) string {
	var b strings.Builder
	b.WriteString(firstNonBlank(c.Title, c.ID))
	if c.Ref != "" && c.Ref != c.Title {
		b.WriteString(" `" + c.Ref + "`")
	}
	switch {
	case c.Op == OpChanged && c.Before != "" && c.Before != c.After:
		b.WriteString("：" + clip(c.Before, 40) + " → " + clip(c.After, 60))
	case c.After != "":
		b.WriteString("：" + clip(c.After, 60))
	}
	return b.String()
}

// buildDiff compares the previous node set (by ID) against the new one and
// captures every add/change/remove with enough context to render a summary.
// Order follows the new node set (already sorted) so the output is stable.
func buildDiff(prev map[string]Node, next []Node) Diff {
	var d Diff
	seen := make(map[string]bool, len(next))
	for _, n := range next {
		seen[n.ID] = true
		old, ok := prev[n.ID]
		switch {
		case !ok:
			d.Added = append(d.Added, changeOf(OpAdded, n, Node{}, n))
		case old.FP != n.FP:
			d.Changed = append(d.Changed, changeOf(OpChanged, n, old, n))
		}
	}
	// Removed nodes are not in `next`; walk the previous set in sorted order so
	// the list is deterministic too.
	if len(prev) > 0 {
		var ids []string
		for id := range prev {
			if !seen[id] {
				ids = append(ids, id)
			}
		}
		sortStrings(ids)
		for _, id := range ids {
			d.Removed = append(d.Removed, changeOf(OpRemoved, prev[id], prev[id], Node{}))
		}
	}
	return d
}

// changeOf projects one node transition onto a Change.
func changeOf(op ChangeOp, n Node, before, after Node) Change {
	return Change{
		Op:     op,
		Kind:   n.Kind,
		ID:     n.ID,
		Title:  firstNonBlank(n.Title, n.ID),
		Ref:    n.Ref,
		Before: nodeGist(before),
		After:  nodeGist(after),
	}
}

// nodeGist renders the part of a node that a summary should show for a change:
// status first (a task moving lanes is the most interesting thing on the map),
// then the one-line summary.
func nodeGist(n Node) string {
	var parts []string
	if s := strings.TrimSpace(n.Status); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(n.Summary); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

// sortStrings sorts in place (a local helper so diff.go needs no import churn).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
