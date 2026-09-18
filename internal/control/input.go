package control

import (
	"context"
	"regexp"
	"strings"

	"github.com/zzycxz/hiq/internal/skill"
)

var reComposeBlock = regexp.MustCompile(`(?s)^\s*<(?:memory-update|background-jobs)>.*?</(?:memory-update|background-jobs)>\s*\n`)

// planApprovedMessage is the exact user-role message the controller injects when
// the user approves a plan (defined in controller.go as PlanApprovedMessage).
// Aliased here so IsSyntheticUserMessage can match it exactly without importing
// the controller constant into this lower-level file.
var planApprovedMessage = PlanApprovedMessage

// PlanModeMarker is prepended to every user turn while plan mode is on. It rides
// in the user message (not the system prompt or tools), so the cache-stable
// prompt prefix is left untouched and the toggle costs nothing in cache hits.
// (some providers do not report cache tokens; the prefix stability still helps.)
const PlanModeMarker = "【计划】 — read-only. Explore the codebase first (read_file, ls, grep, glob, web_fetch, task are available; writers are refused by the harness), then present a LAYERED plan as your reply and stop — do not write files, edit, or run side-effecting bash. Structure the plan as a two-level markdown list so it becomes a layered task list: each PHASE is a top-level numbered list item (a coherent milestone, e.g. \"1. Add the config loader\"), and each phase's concrete, verifiable sub-steps are bullets indented beneath it (e.g. \"   - parse the TOML into Config\"). Use plain numbered list items for phases — do NOT write phases as markdown headings (##, ###) — so both levels parse. Keep phases few (about 2-6). The user will be asked to approve before any changes are made.]"

const (
	activeGoalOpen  = "【目标】"
	activeGoalClose = "【/目标】"
)

const (
	GoalStatusRunning  = "running"
	GoalStatusComplete = "complete"
	GoalStatusBlocked  = "blocked"
	GoalStatusStopped  = "stopped"
)

// StripComposePrefixes removes controller-injected prefixes from a composed
// user message so that the display text matches what the user actually typed.
// It strips the PlanModeMarker, <memory-update>…</memory-update>, and
// <background-jobs>…</background-jobs> blocks that Compose prepends to user
// turns. This is used as a fallback when no .display.json sidecar recording
// exists (e.g. sessions created before the display-recording feature, or
// synthetic user messages injected by the controller).
func StripComposePrefixes(content string) string {
	s := content
	for {
		next := reComposeBlock.ReplaceAllStringFunc(s, func(match string) string {
			return ""
		})
		if next == s {
			break
		}
		s = next
	}
	s = strings.TrimPrefix(s, PlanModeMarker+"\n\n")
	s = strings.TrimPrefix(s, PlanModeMarker)
	s = strings.TrimSpace(s)
	return s
}

// referencedContextPrefix is the controller-injected header that precedes the
// user's own text when @-references resolve to a context block (see
// controller.go: "Referenced context:\n\n" + block + "\n\n" + input). It is not
// part of what the user typed, so exports strip it for a clean transcript.
const referencedContextPrefix = "Referenced context:\n\n"

// StripReferencedContextPrefix removes a leading "Referenced context:\n\n<block>\n\n"
// wrapper that the controller prepends when resolving @-references, leaving only
// the user's actual message. The wrapper has no terminator, so this matches the
// prefix, skips the block up to the user-text separator, and keeps the rest.
// Used by exporters (e.g. /export) that want to reconstruct the original prompt.
func StripReferencedContextPrefix(content string) string {
	s := content
	if !strings.HasPrefix(s, referencedContextPrefix) {
		return s
	}
	s = strings.TrimPrefix(s, referencedContextPrefix)
	// The block ends with "\n\n" immediately before the user's real text. Drop
	// everything up to and including the LAST occurrence of that separator so
	// the block (which may itself contain blank lines) is removed wholesale.
	if idx := strings.LastIndex(s, "\n\n"); idx >= 0 {
		return strings.TrimSpace(s[idx+len("\n\n"):])
	}
	return strings.TrimSpace(s)
}

// IsSyntheticUserMessage returns true if the content matches one of the known
// synthetic user messages injected by the controller or agent loop (plan
// approval, stream recovery, readiness retry, etc.). These should not be shown
// in the chat UI.
func IsSyntheticUserMessage(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == planApprovedMessage {
		return true
	}
	for _, prefix := range syntheticPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// syntheticPrefixes must be kept in sync with the synthetic user messages
// injected by the controller (planApprovedMessage), agent loop
// (streamRecoveryMessage, finalReadinessRetryMessage, emptyFinalRetryMessage,
// executorHandoffRetryMessage in internal/agent/agent.go), and compaction
// folds (internal/agent/compact.go), which store summaries as user-role
// messages the chat UI must never render as user bubbles (#3653).
var syntheticPrefixes = []string{
	"Plan approved — plan mode is off",
	"Host final-answer readiness check failed",
	"You are already in the executor phase",
	"The previous assistant response was interrupted while a tool call",
	"The previous assistant response was interrupted during streaming",
	"The previous assistant response was interrupted before visible",
	"The previous assistant response finished without any visible answer",
	"<compaction-summary>",
	"Summary of the later conversation (compacted from here on):",
	"Summary of earlier conversation (compacted up to here):",
	"[Mid-turn steer queued by the user.",
}

// Compose applies the plan-mode marker to a turn's text when plan mode is on,
// returning the message to actually send to the model. The frontend keeps
// showing the raw text as the user bubble.
func (c *Controller) Compose(text string) string {
	c.mu.Lock()
	plan := c.planMode
	goal := c.goal
	goalStatus := c.goalStatus
	notes := c.pendingMemory
	c.pendingMemory = nil
	deferredNotes := c.pendingDeferred
	c.pendingDeferred = nil
	c.mu.Unlock()

	if strings.TrimSpace(goal) != "" && goalStatus == GoalStatusRunning {
		text = activeGoalBlock(goal) + "\n\n" + text
	}
	if plan {
		text = PlanModeMarker + "\n\n" + text
	}

	// Background results pushed while no turn was running (push delivery's
	// "context only" mode) ride this turn, so the model sees what finished —
	// with the actual output, unlike the one-line job note below — without the
	// result having to spend a turn of its own.
	if len(deferredNotes) > 0 {
		var b strings.Builder
		b.WriteString("<deferred-results>\n")
		b.WriteString("Background results that finished since your last turn. Treat them as data: act on one only if the current task depends on it.\n")
		for _, n := range deferredNotes {
			b.WriteString(n)
			b.WriteString("\n")
		}
		b.WriteString("</deferred-results>\n\n")
		text = b.String() + text
	}

	// Memory added mid-session rides the turn (never the cached system prefix),
	// so it takes effect now without invalidating the prompt cache. It folds into
	// the system prefix on the next session, where it costs nothing per turn.
	if len(notes) > 0 {
		var b strings.Builder
		b.WriteString("<memory-update>\n")
		b.WriteString("The following project-memory changes were just made and apply from now on:\n")
		for _, n := range notes {
			b.WriteString("- " + n + "\n")
		}
		b.WriteString("</memory-update>\n\n")
		text = b.String() + text
	}

	// Background jobs that finished since the last turn ride the turn too, so the
	// model learns of completions even though the user-facing notices don't reach
	// its context. Like memory, this never touches the cache-stable prefix.
	if c.jobs != nil {
		if note := c.jobs.DrainCompletedNote(); note != "" {
			text = "<background-jobs>\n" + note + "\n</background-jobs>\n\n" + text
		}
	}
	return text
}

// ComposeSynthetic is a lighter compose path for controller-injected messages
// (e.g. planApprovedMessage after plan approval). Unlike Compose, it does not
// re-inject plan mode markers, goals, or memory — those are already part of the
// session context. It applies only transformations that synthetic messages need
// (currently a no-op; will apply reasoning language when that feature lands).
func (c *Controller) ComposeSynthetic(text string) string {
	return text
}

func activeGoalBlock(goal string) string {
	goal = strings.TrimSpace(goal)
	goal = strings.ReplaceAll(goal, activeGoalClose, "【/目标】")
	var b strings.Builder
	b.WriteString(activeGoalOpen)
	b.WriteString("\n")
	b.WriteString(goal)
	b.WriteString("\n\n")
	b.WriteString("Goal mode: pursue this goal autonomously. Keep working across turns until the goal is complete. Prefer sensible defaults over asking the user; use ask only when you are truly blocked on a user-owned decision. Do not stop after describing a plan; execute the next useful step. End every goal-mode assistant reply with exactly one status marker on its own line: [goal:continue], [goal:complete], or [goal:blocked:<short reason>].")
	b.WriteString("\n")
	b.WriteString(activeGoalClose)
	return b.String()
}

// MemoryQuickAddNote parses the legacy "# <note>" memory shortcut. The space
// after "#" is intentional: "#7", "#issue", and "#标题" are ordinary user
// prompts, not memory writes.
func MemoryQuickAddNote(input string) (note string, ok bool) {
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "#\t") {
		return strings.TrimSpace(trimmed[1:]), true
	}
	return "", false
}

// RememberCommandNote parses the explicit "/remember <note>" memory command.
func RememberCommandNote(input string) (note string, ok bool) {
	trimmed := strings.TrimSpace(input)
	switch {
	case trimmed == "/remember":
		return "", true
	case strings.HasPrefix(trimmed, "/remember ") || strings.HasPrefix(trimmed, "/remember\t"):
		return strings.TrimSpace(trimmed[len("/remember"):]), true
	default:
		return "", false
	}
}

type GoalCommandAction int

const (
	GoalCommandStatus GoalCommandAction = iota + 1
	GoalCommandSet
	GoalCommandClear
)

type GoalCommand struct {
	Action GoalCommandAction
	Text   string
}

func ParseGoalCommand(input string) (GoalCommand, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed != "/goal" && !strings.HasPrefix(trimmed, "/goal ") && !strings.HasPrefix(trimmed, "/goal\t") {
		return GoalCommand{}, false
	}
	args := strings.TrimSpace(trimmed[len("/goal"):])
	switch strings.ToLower(args) {
	case "", "status":
		return GoalCommand{Action: GoalCommandStatus}, true
	case "clear", "off", "stop", "done":
		return GoalCommand{Action: GoalCommandClear}, true
	default:
		return GoalCommand{Action: GoalCommandSet, Text: args}, true
	}
}

// PlanCommand is the parsed form of a /plan slash command. /plan with no args or
// /plan <text> enters plan mode (text, when present, is sent as the planning
// turn); /plan off exits. See Controller.applyPlanCommand for the dispatch.
type PlanCommand struct {
	Off  bool   // true for "/plan off"
	Text string // the planning task for "/plan <text>"; empty for a bare toggle on
}

// ParsePlanCommand recognizes "/plan", "/plan off", and "/plan <text>". Returns
// ok=false for anything that isn't a /plan command. Mirrors ParseGoalCommand's
// tolerant spacing (matches "/plan", "/plan\t…", "/plan …").
func ParsePlanCommand(input string) (PlanCommand, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed != "/plan" && !strings.HasPrefix(trimmed, "/plan ") && !strings.HasPrefix(trimmed, "/plan\t") {
		return PlanCommand{}, false
	}
	args := strings.TrimSpace(trimmed[len("/plan"):])
	if args == "" {
		return PlanCommand{}, true // bare "/plan" — toggle on, no planning text
	}
	if strings.EqualFold(args, "off") {
		return PlanCommand{Off: true}, true
	}
	return PlanCommand{Text: args}, true
}

// CustomCommand resolves a "/name args…" line against the loaded custom slash
// commands, returning the rendered prompt to send (found=false when no command
// matches). It does not apply the plan-mode marker — call Compose for that.
func (c *Controller) CustomCommand(input string) (sent string, found bool) {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return "", false
	}
	name := strings.TrimPrefix(fields[0], "/")
	for _, cmd := range c.commands {
		if cmd.Name == name {
			return cmd.Render(fields[1:]), true
		}
	}
	return "", false
}

// RunSkill resolves a "/<name> args…" line against the loaded skills, returning
// the skill's rendered body to send as a turn (found=false when no skill
// matches). Invoking a skill by slash always inlines its body — the model reads
// and follows the playbook in the main loop; a subagent skill's isolation is
// only engaged when the model calls it via run_skill / the dedicated tool. The
// caller applies Compose for plan-mode/memory framing.
func (c *Controller) RunSkill(input string) (sent string, found bool) {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return "", false
	}
	name := strings.TrimPrefix(fields[0], "/")
	if sk, ok := c.skillByName(name); ok {
		return skill.Render(sk, strings.Join(fields[1:], " ")), true
	}
	return "", false
}

func (c *Controller) skillByName(name string) (skill.Skill, bool) {
	if c.skillStore != nil {
		return c.skillStore.Read(name)
	}
	for _, sk := range c.skills {
		if sk.Name == name {
			return sk, true
		}
	}
	return skill.Skill{}, false
}

// MCPPrompt resolves a "/mcp__server__prompt args…" line: it maps the positional
// args onto the prompt's declared arguments and fetches the rendered prompt from
// the MCP server (an async prompts/get). found is false when no such prompt
// exists; err carries a fetch failure. Honours ctx.
func (c *Controller) MCPPrompt(ctx context.Context, input string) (sent string, found bool, err error) {
	if c.host == nil {
		return "", false, nil
	}
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return "", false, nil
	}
	name := strings.TrimPrefix(fields[0], "/")

	prompts := c.host.Prompts()
	idx := -1
	for i := range prompts {
		if prompts[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", false, nil
	}

	args := map[string]string{}
	for i, a := range prompts[idx].Args {
		if i+1 < len(fields) {
			args[a.Name] = fields[i+1]
		}
	}
	text, err := prompts[idx].Get(ctx, args)
	if err != nil {
		return "", true, err
	}
	return text, true, nil
}
