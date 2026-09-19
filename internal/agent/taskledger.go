package agent

import (
	"fmt"
	"strings"

	"github.com/zzycxz/hiq/internal/provider"
)

// The task ledger is the drift guard for long tasks: a single tagged user
// message the model maintains through the task_ledger tool, holding the
// goal, constraints, decisions, progress, and acceptance criteria. The
// compaction machinery pins it verbatim (see pinnedPrefixLen/partitionFold in
// compact.go), so facts recorded here survive every fold byte-for-byte — in
// contrast to the compaction summary, which is an LLM digest and drifts.
//
// Cache economics: the ledger sits in the pinned prefix, so each update costs
// one prefix miss on the next request. That is the designed trade-off — the
// tool description steers the model to milestone-sized updates, and
// UpdateLedger skips the rewrite entirely when the section content is
// unchanged.

const (
	ledgerTagOpen  = "<task-ledger>"
	ledgerTagClose = "</task-ledger>"

	// ledgerReanchorEvery is how many tool-call rounds pass between re-anchor
	// nudges: the goal + acceptance digest is appended (advisory) to the
	// latest tool result so a model many rounds into a long task is pulled
	// back to the stated objective. Text appended to a result that has not
	// been sent yet costs no prefix stability.
	ledgerReanchorEvery = 12

	ledgerPreamble = "Task ledger (durable anchor, kept verbatim across compactions; update via the task_ledger tool):"
)

// ledgerSections is the canonical section order (also the layout of a fresh
// ledger). Section aliases map onto these canonically in normalizeLedgerSection.
var ledgerSections = []string{
	"Goal",
	"Constraints",
	"Decisions & rationale",
	"Progress & artifacts",
	"Acceptance criteria",
}

var ledgerSectionAliases = map[string]string{
	"goal":        "Goal",
	"constraint":  "Constraints",
	"constraints": "Constraints",
	"decisions":   "Decisions & rationale",
	"decision":    "Decisions & rationale",
	"progress":    "Progress & artifacts",
	"artifacts":   "Progress & artifacts",
	"acceptance":  "Acceptance criteria",
	"criteria":    "Acceptance criteria",
}

// isTaskLedger reports whether m is the session's task-ledger message.
func isTaskLedger(m provider.Message) bool {
	return m.Role == provider.RoleUser &&
		strings.HasPrefix(strings.TrimLeft(m.Content, "\n "), ledgerTagOpen)
}

// IsTaskLedger reports whether m is the task-ledger message. Exported for
// session owners outside this package (the guardian) whose rollback logic
// must not treat the ledger as a disposable user message.
func IsTaskLedger(m provider.Message) bool { return isTaskLedger(m) }

// newTaskLedgerMessage builds a fresh ledger seeded with the user's request as
// the Goal. The other sections start empty and fill in as the model records
// milestones.
func newTaskLedgerMessage(goal string) provider.Message {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		goal = "(not yet stated — record the user's request here via task_ledger)"
	}
	if len(goal) > 4000 { // defensive cap: a pasted mega-prompt is not a goal line
		goal = goal[:4000] + " …(truncated; see the user's first message for the full request)"
	}
	var b strings.Builder
	b.WriteString(ledgerTagOpen + "\n")
	b.WriteString(ledgerPreamble + "\n")
	for _, s := range ledgerSections {
		if s == "Goal" {
			fmt.Fprintf(&b, "\n## %s\n%s\n", s, goal)
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n(empty)\n", s)
	}
	b.WriteString(ledgerTagClose)
	return provider.Message{Role: provider.RoleUser, Content: b.String()}
}

// ledgerIndex returns the position of the session's task-ledger message, or -1.
func (a *Agent) ledgerIndex() int {
	for i, m := range a.session.Messages {
		if isTaskLedger(m) {
			return i
		}
	}
	return -1
}

// ensureTaskLedger inserts the task ledger after the first user turn when the
// session has none. Called at Run start, before any provider request of the
// turn: for a fresh session the insertion rides the turn's first (cache-cold)
// request, and for a resumed session it costs exactly one prefix miss — after
// which the ledger is part of the stable prefix. A ledger seeded this way
// survives Save/Load untouched (it is an ordinary message), so reloaded
// sessions keep theirs.
func (a *Agent) ensureTaskLedger(goal string) {
	if a.ledgerIndex() >= 0 {
		return
	}
	pos := 0
	if pos < len(a.session.Messages) && a.session.Messages[pos].Role == provider.RoleSystem {
		pos++
	}
	if pos < len(a.session.Messages) && a.session.Messages[pos].Role == provider.RoleUser {
		pos++ // after the first user turn — the pinned prefix keeps it verbatim there
	}
	msgs := a.session.Messages
	next := make([]provider.Message, 0, len(msgs)+1)
	next = append(next, msgs[:pos]...)
	next = append(next, newTaskLedgerMessage(goal))
	next = append(next, msgs[pos:]...)
	a.session.Rewrite(next, "task-ledger created")
}

// normalizeLedgerSection maps a tool-supplied section name onto the canonical
// heading.
func normalizeLedgerSection(s string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(s))
	key = strings.TrimSuffix(key, " & rationale")
	key = strings.TrimSuffix(key, " & artifacts")
	key = strings.TrimSuffix(key, " criteria")
	if canon, ok := ledgerSectionAliases[key]; ok {
		return canon, nil
	}
	return "", fmt.Errorf("unknown ledger section %q (want goal, constraints, decisions, progress, or acceptance)", s)
}

// UpdateLedger implements tool.LedgerEditor: set or append to one section of
// the ledger, rewriting the message in place. A no-op rewrite (identical
// content) returns "unchanged" without touching the session, so redundant
// tool calls cost no cache.
func (a *Agent) UpdateLedger(section, content, mode string) (string, error) {
	canon, err := normalizeLedgerSection(section)
	if err != nil {
		return "", err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("content is required")
	}
	if mode != "replace" && mode != "append" {
		return "", fmt.Errorf("unknown mode %q (want replace or append)", mode)
	}
	idx := a.ledgerIndex()
	if idx < 0 {
		return "", fmt.Errorf("no task ledger in this session")
	}
	secs := parseLedgerSections(a.session.Messages[idx].Content)
	if mode == "append" {
		if cur := strings.TrimSpace(secs[canon]); cur != "" && cur != "(empty)" {
			content = cur + "\n- " + content
		} else {
			content = "- " + content
		}
	}
	if prev := strings.TrimSpace(secs[canon]); prev == content {
		return fmt.Sprintf("ledger %s: unchanged", canon), nil
	}
	secs[canon] = content
	a.session.Rewrite(replaceLedgerMessage(a.session.Messages, idx, secs), "task-ledger update")
	return fmt.Sprintf("ledger %s updated", canon), nil
}

// LedgerSnapshot implements tool.LedgerEditor: the ledger body without the tag
// wrapper.
func (a *Agent) LedgerSnapshot() string {
	idx := a.ledgerIndex()
	if idx < 0 {
		return ""
	}
	return ledgerBody(a.session.Messages[idx].Content)
}

// ledgerBody strips the tag wrapper and preamble, returning the raw sections.
func ledgerBody(content string) string {
	body := strings.TrimLeft(content, "\n ")
	body = strings.TrimPrefix(body, ledgerTagOpen)
	body = strings.TrimLeft(body, "\n ")
	if i := strings.LastIndex(body, ledgerTagClose); i >= 0 {
		body = body[:i]
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == ledgerPreamble {
		lines = lines[1:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// parseLedgerSections splits a ledger message's content into its canonical
// sections. Unparsable shapes degrade to empty strings — a section the model
// mangled via other means is simply not carried into the rewrite.
func parseLedgerSections(content string) map[string]string {
	out := make(map[string]string, len(ledgerSections))
	for _, s := range ledgerSections {
		out[s] = "(empty)"
	}
	cur := ""
	var b strings.Builder
	flush := func() {
		if cur != "" {
			out[cur] = strings.TrimSpace(b.String())
		}
	}
	for _, line := range strings.Split(ledgerBody(content), "\n") {
		if strings.HasPrefix(line, "## ") {
			flush()
			b.Reset()
			cur = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}
		if cur != "" {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	flush()
	return out
}

// replaceLedgerMessage rebuilds the ledger message at idx with the given
// section map, preserving message order.
func replaceLedgerMessage(msgs []provider.Message, idx int, secs map[string]string) []provider.Message {
	var b strings.Builder
	b.WriteString(ledgerTagOpen + "\n")
	b.WriteString(ledgerPreamble + "\n")
	for _, s := range ledgerSections {
		content := strings.TrimSpace(secs[s])
		if content == "" {
			content = "(empty)"
		}
		fmt.Fprintf(&b, "\n## %s\n%s\n", s, content)
	}
	b.WriteString(ledgerTagClose)

	next := make([]provider.Message, len(msgs))
	copy(next, msgs)
	next[idx] = provider.Message{Role: provider.RoleUser, Content: b.String()}
	return next
}

// applyLedgerReanchor appends an advisory goal digest to the latest tool
// result every ledgerReanchorEvery tool-call rounds, so a model deep in a
// long task keeps seeing the objective and acceptance criteria without the
// kernel rewriting any already-sent prefix bytes. Called with the step number
// (1-based) and the batch's results before they are persisted.
func (a *Agent) applyLedgerReanchor(step int, results []string) {
	if len(results) == 0 || step < ledgerReanchorEvery {
		return
	}
	if a.lastReanchorStep > 0 && step-a.lastReanchorStep < ledgerReanchorEvery {
		return
	}
	idx := a.ledgerIndex()
	if idx < 0 {
		return
	}
	secs := parseLedgerSections(a.session.Messages[idx].Content)
	var b strings.Builder
	b.WriteString("[task-ledger re-anchor] Re-grounding after ")
	fmt.Fprintf(&b, "%d", step-a.lastReanchorStep)
	b.WriteString(" tool rounds. ")
	if g := secs["Goal"]; g != "" && g != "(empty)" {
		b.WriteString("Goal: " + firstLedgerLines(g, 3) + " ")
	}
	if acc := secs["Acceptance criteria"]; acc != "" && acc != "(empty)" {
		b.WriteString("Acceptance: " + firstLedgerLines(acc, 3) + " ")
	}
	b.WriteString("Work that still serves this goal; call task_ledger (read) if the full ledger is unclear, and (update) to record what you just finished.")
	a.lastReanchorStep = step
	results[len(results)-1] += "\n\n" + b.String()
}

// firstLedgerLines returns the first n non-empty lines of a section, joined,
// for the compact re-anchor digest.
func firstLedgerLines(s string, n int) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kept = append(kept, line)
		if len(kept) == n {
			break
		}
	}
	out := strings.Join(kept, " / ")
	if len(out) > 300 {
		out = out[:300] + "…"
	}
	return out
}
