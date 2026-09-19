package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zzycxz/hiq/internal/tool"
)

func init() { tool.RegisterBuiltin(taskLedger{}) }

// taskLedger is the task-ledger tool: the model's write handle on the session's
// durable task anchor. The kernel keeps the ledger message verbatim across
// compactions (it is pinned alongside the first user turn), so decisions and
// constraints recorded here survive every fold byte-for-byte — the drift guard
// for long tasks.
//
// The description below is prompt surface and must stay deterministic — it is
// part of the cache-stable prompt.
type taskLedger struct{}

func (taskLedger) Name() string { return "task_ledger" }

func (taskLedger) Description() string {
	return `Maintain the session's task ledger — the durable anchor the kernel keeps verbatim across context compactions. The ledger has five sections: Goal (the user's request and intent), Constraints (hard rules, exact paths, versions, preferences, "never do X" rules), Decisions & rationale (key choices and why, so they are not re-litigated), Progress & artifacts (completed steps and what they produced), Acceptance criteria (how the user will judge the task done).

Use action "update" at milestones only: record a decision when you commit to an approach, move finished work into Progress with its artifact (file, command, result). Preserve exact identifiers and paths. Keep entries terse — bullet fragments, not prose. Each update rewrites part of the prompt prefix and costs one cache miss, so batch small changes and never use the ledger as a scratchpad (state that only matters within the current step belongs in your working notes, not here).

Use action "read" to review the current ledger before resuming work after a long stretch of tool calls, or whenever you are unsure what the goal or constraints were.`
}

func (taskLedger) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["read", "update"],
      "description": "read returns the current ledger; update writes one section."
    },
    "section": {
      "type": "string",
      "enum": ["goal", "constraints", "decisions", "progress", "acceptance"],
      "description": "Ledger section to update (required for update)."
    },
    "content": {
      "type": "string",
      "description": "New section content (required for update)."
    },
    "mode": {
      "type": "string",
      "enum": ["replace", "append"],
      "description": "replace rewrites the section; append adds a bullet to it (default replace)."
    }
  },
  "required": ["action"]
}`)
}

func (taskLedger) ReadOnly() bool { return false }

func (taskLedger) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Action  string `json:"action"`
		Section string `json:"section"`
		Content string `json:"content"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	editor, ok := tool.LedgerEditorFrom(ctx)
	if !ok {
		return "", fmt.Errorf("task_ledger is unavailable: no session ledger editor in this context")
	}
	switch in.Action {
	case "read":
		body := editor.LedgerSnapshot()
		if strings.TrimSpace(body) == "" {
			return "(the ledger is empty)", nil
		}
		return body, nil
	case "update":
		if in.Mode == "" {
			in.Mode = "replace"
		}
		return editor.UpdateLedger(in.Section, in.Content, in.Mode)
	default:
		return "", fmt.Errorf("unknown action %q (want read or update)", in.Action)
	}
}
