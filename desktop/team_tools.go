package main

import (
	"context"
	"encoding/json"
	"fmt"

	teampkg "github.com/zzycxz/hiq/internal/team"
)

// teamSharedHistoryToolName is the member-visible name of the archive pager.
const teamSharedHistoryToolName = "team_read_shared_history"

// Team member tools (open-vetta 移植 P1-4 步骤 A).
//
// A team's shared notes are a bounded hot window, so the oldest ones leave the
// blackboard digest. The full text still exists — every note is appended to the
// team's archive — and this tool is how a member gets back to it.
//
// Two deliberate constraints:
//
//   - The team is CAPTURED at construction, never named by the model. A member
//     can therefore only read its own team's history; there is no argument that
//     could point the tool at another team or at an arbitrary path.
//   - It is read-only, and it is registered only into the member's own registry
//     (see App.teamRegistry), so it never appears in the main session's tool
//     list — the list is part of the cached prompt prefix.
type teamSharedHistoryTool struct {
	store *teampkg.Store
	// team is a snapshot taken when the run started; it is used only to resolve
	// note authors to display names.
	team teampkg.Team
}

// teamSharedHistoryArgs is the tool's (optional) parameter object. Both fields
// are optional: the zero value returns the newest page, which is what a member
// asking "what did I miss?" wants.
type teamSharedHistoryArgs struct {
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

func (t *teamSharedHistoryTool) Name() string { return teamSharedHistoryToolName }

func (t *teamSharedHistoryTool) Description() string {
	return "翻阅所在团队「共享笔记」的完整归档（最新在前，可翻页）。" +
		"团队上下文里只显示最近十几条，更早的笔记在这里。当你需要知道别人早先踩过的坑、" +
		"定过的约定、或者某个决定的原因，但上下文里没有时，用这个工具往前翻。" +
		"只读，不会修改任何东西。"
}

func (t *teamSharedHistoryTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "offset": {"type": "integer", "description": "从最新一条往前跳过多少条，0 表示从最新开始"},
    "limit": {"type": "integer", "description": "本页返回多少条，默认 20，最多 50"}
  }
}`)
}

// ReadOnly is true: paging the archive has no observable effect on the host, so
// a batch of these calls may run in parallel.
func (t *teamSharedHistoryTool) ReadOnly() bool { return true }

func (t *teamSharedHistoryTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in teamSharedHistoryArgs
	if len(args) > 0 {
		// A malformed argument object must not fail the read — offset/limit both
		// default to the newest page, which is always a sane answer.
		_ = json.Unmarshal(args, &in)
	}
	if t == nil || t.store == nil {
		return "", fmt.Errorf("%s: 团队存储不可用", teamSharedHistoryToolName)
	}
	page, err := t.store.ReadNotes(t.team.ID, in.Offset, in.Limit)
	if err != nil {
		return "", fmt.Errorf("%s: %w", teamSharedHistoryToolName, err)
	}
	return teampkg.FormatNotesPage(page, t.team.ResolveNoteAuthor), nil
}
