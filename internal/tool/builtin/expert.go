package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/experts"
	"github.com/zzycxz/hiq/internal/tool"
)

// Expert-team tools (coWork). These expose the multi-model collaboration engine
// to the agent so a user can say "use an expert team to review my proposal" in
// chat and the agent can drive a run — same engine the ExpertPanel uses, just
// reachable from the conversation.
//
// The orchestrator + store are process-global, injected from boot.go (under the
// cowork profile) via SetExpertOrchestrator / SetExpertStore. When nil the tool
// returns a clear error; it isn't registered in the dev profile anyway.
//
// Runs are SYNCHRONOUS (this is an agent tool call — it blocks until the team
// finishes, then returns the synthesis). The orchestrator still emits
// CollabEvents to the live "experts:collab" channel, so a user who opens the
// Expert panel during a chat-driven run sees the streamed rounds. Under RPM=5 a
// 3-expert debate (2 rounds + synthesis ≈ 7 requests) takes ~1.5 min — the agent
// tool budget accommodates this, and the user can watch progress in the panel.

var (
	globalExpertOrchestrator *experts.Orchestrator
	globalExpertStore        *experts.Store
)

// SetExpertOrchestrator injects the app-level orchestrator the tool drives.
// Called once at cowork boot; nil disables the tool ("experts offline").
func SetExpertOrchestrator(o *experts.Orchestrator) { globalExpertOrchestrator = o }

// SetExpertStore injects the team store so the tool can list teams for
// auto-selection (when team_id is omitted). nil disables that lookup.
func SetExpertStore(s *experts.Store) { globalExpertStore = s }

func requireExpertOrchestrator() (*experts.Orchestrator, error) {
	if globalExpertOrchestrator == nil {
		return nil, errors.New("expert team engine is offline (only available under the cowork profile)")
	}
	return globalExpertOrchestrator, nil
}

// ExpertTools returns the expert-team tools for cowork registration.
func ExpertTools() []tool.Tool {
	return []tool.Tool{expertTeamRun{}, expertTeamList{}}
}

// --- expert_team_run -------------------------------------------------------

type expertTeamRun struct{}

func (expertTeamRun) Name() string { return "expert_team_run" }

func (expertTeamRun) Description() string {
	return "Run an expert team (multi-model collaboration) on a task and return the moderator's synthesis. Modes: \"parallel\" (each expert answers independently, fastest), \"debate\" (experts see each other's answers and refine/rebut over N rounds, best for review/decisions), \"pipeline\" (experts work in sequence, each building on the previous, best for division-of-labor like research→draft→proofread). Leave team_id empty to auto-pick by team name (fuzzy) or fall back to the first available team. The run is synchronous and may take 1-2 minutes under a tight RPM budget; progress streams to the Expert panel. Use when a task benefits from multiple perspectives or a multi-step production chain — not for single-answer questions."
}

func (expertTeamRun) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "task":{"type":"string","description":"The task for the team to collaborate on. Be specific — experts work from this alone."},
  "team_id":{"type":"string","description":"Team id from expert_team_list. Omit to auto-pick by team_name, or fall back to the first team."},
  "team_name":{"type":"string","description":"Team name to resolve to team_id (fuzzy/contains match) when team_id is omitted. e.g. \"文档撰写团\" or just \"文档\"."},
  "mode":{"type":"string","description":"Override the team's default mode: \"parallel\" | \"debate\" | \"pipeline\". Omit to use the team default."},
  "rounds":{"type":"integer","description":"Debate rounds (only for mode=\"debate\"). Omit to use the team default."}
},
"required":["task"]
}`)
}

func (expertTeamRun) ReadOnly() bool { return false }

func (expertTeamRun) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Task     string `json:"task"`
		TeamID   string `json:"team_id"`
		TeamName string `json:"team_name"`
		Mode     string `json:"mode"`
		Rounds   int    `json:"rounds"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Task) == "" {
		return "", errors.New("task is required")
	}
	o, err := requireExpertOrchestrator()
	if err != nil {
		return "", err
	}
	teamID, err := resolveTeamID(p.TeamID, p.TeamName)
	if err != nil {
		return "", err
	}
	// Synchronous: blocks until the team finishes. The orchestrator emits
	// CollabEvents to the live channel as it runs, so a user watching the Expert
	// panel sees streamed rounds. We return only the final synthesis — the agent
	// reasons from the conclusion, and the full per-expert transcript is visible
	// in the Expert panel and (for panel-initiated runs) persisted as a folded
	// block in the main session.
	// Bound the run so a hung LLM call or a slow pipeline can't block the
	// agent's tool call indefinitely (the parent turn ctx may itself lack a
	// deadline). 10 minutes covers a 3-expert debate + synthesis at low RPM;
	// the Expert panel already streams progress so a timeout lands gracefully.
	// See audit finding E7.
	const expertRunTimeout = 10 * time.Minute
	runCtx, cancel := context.WithTimeout(ctx, expertRunTimeout)
	defer cancel()
	res, err := o.Run(runCtx, teamID, p.Task, p.Mode, p.Rounds)
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return "", errors.New("专家团运行超时（超过 10 分钟），请在 Expert 面板查看部分进度后缩小任务范围重试")
		}
		return "", err
	}
	// Surface the synthesis plus a compact transcript so the agent has the full
	// picture (which expert said what, per round) without the user needing to
	// open the panel.
	var b strings.Builder
	if strings.TrimSpace(res.Synthesis) == "" {
		b.WriteString("(专家团未产出综合结论；以下是各专家发言：)\n\n")
	} else {
		b.WriteString("【主持人综合】\n")
		b.WriteString(res.Synthesis)
		b.WriteString("\n\n【各专家发言】\n")
	}
	for ri, round := range res.Rounds {
		if len(res.Rounds) > 1 {
			fmt.Fprintf(&b, "— 第 %d 轮 —\n", ri+1)
		}
		for _, a := range round {
			fmt.Fprintf(&b, "[%s]\n%s\n\n", a.ExpertName, truncateForTool(a.Text, 600))
		}
	}
	return strings.TrimRight(b.String(), "\n "), nil
}

// resolveTeamID resolves the team to run. Explicit team_id wins; otherwise look
// up by team_name (fuzzy contains); otherwise fall back to the first team so a
// bare "use an expert team for X" still works.
func resolveTeamID(teamID, teamName string) (string, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID != "" {
		return teamID, nil
	}
	if globalExpertStore == nil {
		return "", errors.New("no team specified and the team store is offline; pass team_id")
	}
	teams := globalExpertStore.List()
	if len(teams) == 0 {
		return "", errors.New("no expert teams available; create one in the Expert panel first")
	}
	if name := strings.TrimSpace(teamName); name != "" {
		for _, t := range teams {
			if strings.Contains(t.Name, name) {
				return t.ID, nil
			}
		}
	}
	// Fallback: first team (stable order = builtin order after seeding).
	return teams[0].ID, nil
}

// --- expert_team_list ------------------------------------------------------

type expertTeamList struct{}

func (expertTeamList) Name() string { return "expert_team_list" }

func (expertTeamList) Description() string {
	return "List available expert teams (builtin + user-created). Each entry shows id, name, default mode, default debate rounds, and the expert roster (name + perspective). Use to pick a team_id for expert_team_run, or to let the user choose. Builtin teams cover office loops: review, brainstorm, doc-writing, data analysis, translation, meeting minutes, project planning, email drafting."
}

func (expertTeamList) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{},
"required":[]
}`)
}

func (expertTeamList) ReadOnly() bool { return true }

func (expertTeamList) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if globalExpertStore == nil {
		return "", errors.New("team store is offline (only available under the cowork profile)")
	}
	teams := globalExpertStore.List()
	if len(teams) == 0 {
		return "no expert teams available", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d team(s):\n", len(teams))
	for _, t := range teams {
		fmt.Fprintf(&b, "- [%s] %s (mode: %s", t.ID, t.Name, t.DefaultMode)
		if t.DefaultMode == "debate" {
			fmt.Fprintf(&b, ", rounds: %d", t.DefaultRounds)
		}
		b.WriteString(")\n")
		for _, ex := range t.Experts {
			model := ex.Model
			if model == "" {
				model = "(default)"
			}
			fmt.Fprintf(&b, "    · %s [%s]: %s\n", ex.Name, model, truncateForTool(ex.Perspective, 100))
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// truncateForTool clamps a string for tool output, appending an ellipsis when
// cut. Keeps tool results readable without dumping full multi-KB answers.
func truncateForTool(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
