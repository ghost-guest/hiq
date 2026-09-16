package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/zzycxz/fairpeer/internal/agent"
	"github.com/zzycxz/fairpeer/internal/boot"
	"github.com/zzycxz/fairpeer/internal/control"
	"github.com/zzycxz/fairpeer/internal/event"
	"github.com/zzycxz/fairpeer/internal/netclient"
	"github.com/zzycxz/fairpeer/internal/taskmonitor"
	teampkg "github.com/zzycxz/fairpeer/internal/team"
	"github.com/zzycxz/fairpeer/internal/tool"
	"github.com/zzycxz/fairpeer/internal/tool/builtin"
)

// Team member execution (P2 of 团队功能设计.md).
//
// One kanban card is executed as its assignee's OWN agent session:
//
//	agent.NewSession(MemberRunPrompt(...)) → agent.New(prov, reg, sess, opts, sink) → Run(brief)
//
// This is what makes "每个团员有自己的上下文，又能读团队上下文" real. The member
// never touches the main conversation's Session, so the main session's
// system-prompt byte-stability (the cache-prefix invariant) is untouched: the
// blackboard digest is injected ONLY into the member's fresh sub-session.
//
// The runner is deliberately a goroutine rather than a jobs.Manager job: the
// board only needs liveness + cancel, and a goroutine keeps the whole feature
// free of coupling to the active tab's job manager.

// teamRunKey identifies one in-flight member run.
func teamRunKey(teamID, taskID string) string { return teamID + "/" + taskID }

// RunTeamTask executes a card as its assignee's isolated sub-session and returns
// the board immediately (the run itself streams in the background, updating
// Progress and finally the card's state). It is the "让团员真正干活" entry point.
func (a *App) RunTeamTask(teamID, taskID string) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	tm, ok := store.Get(teamID)
	if !ok {
		return TeamProjectView{}, fmt.Errorf("团队不存在")
	}
	tk, ok := tm.Task(taskID)
	if !ok {
		return TeamProjectView{}, fmt.Errorf("任务不存在")
	}
	if strings.TrimSpace(tk.AssigneeID) == "" {
		return TeamProjectView{}, fmt.Errorf("这张卡片还没有负责人：先拖到「待开始」按能力自动指派，或手动指派")
	}
	member, ok := tm.Member(tk.AssigneeID)
	if !ok {
		return TeamProjectView{}, fmt.Errorf("负责人已不在团队中，请重新指派")
	}
	if missing := unmetTeamDeps(tm, tk); len(missing) > 0 {
		return TeamProjectView{}, fmt.Errorf("前置任务尚未完成：%s", strings.Join(missing, "、"))
	}

	key := teamRunKey(teamID, taskID)
	base := a.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)

	a.teamRunsMu.Lock()
	if a.teamRuns == nil {
		a.teamRuns = map[string]context.CancelFunc{}
	}
	if _, running := a.teamRuns[key]; running {
		a.teamRunsMu.Unlock()
		cancel()
		return TeamProjectView{}, fmt.Errorf("该任务正在执行中")
	}
	a.teamRuns[key] = cancel
	a.teamRunsMu.Unlock()

	// Flip the card to running BEFORE dispatching so the board shows a spinner
	// the moment the call returns. The member prompt is built from the snapshot
	// taken above, so the status flip cannot affect what the member is told.
	started, err := store.Update(teamID, func(t *teampkg.Team) {
		for i := range t.Tasks {
			if t.Tasks[i].ID != taskID {
				continue
			}
			t.Tasks[i].Status = taskmonitor.TaskStateRunning
			t.Tasks[i].Attempts++
			t.Tasks[i].Error = ""
			t.Tasks[i].Progress = "已启动…"
			t.Tasks[i].UpdatedAt = time.Now().UTC()
		}
	})
	if err != nil {
		a.forgetTeamRun(key)
		cancel()
		return TeamProjectView{}, err
	}

	go func() {
		a.runTeamMember(ctx, cancel, key, teamID, started, member, tk)
		// The run slot is released by now (runTeamMember's defer), so if the
		// card failed, a re-plan is free to start its retry immediately.
		a.replanFailedTeamTask(teamID, taskID)
	}()
	return toTeamProjectView(started), nil
}

// TeamTaskProgress is the payload of the lightweight `team:task` event. Progress
// streams several times a second, so the board patches one card from this
// instead of re-rendering the whole project on every tick.
type TeamTaskProgress struct {
	TeamID     string `json:"teamId"`
	TaskID     string `json:"taskId"`
	Status     string `json:"status"`
	Column     string `json:"column"`
	Progress   string `json:"progress"`
	Error      string `json:"error"`
	Attempts   int    `json:"attempts"`
	AssigneeID string `json:"assigneeId"`
	Assignee   string `json:"assignee"`
}

// replanFailedTeamTask is the "跟踪 + 再规划" step of P3. It runs after a member
// finishes: a failed card is re-planned by the leader — reassigned to another
// capable member and retried, or escalated into 阻塞 with the reason recorded on
// the blackboard. Policy.MaxRounds (via the card's attempt count) caps the
// loop, so a card that keeps failing escalates instead of churning forever.
//
// It must run only after the run slot is free, otherwise the retry it starts
// would be rejected as "already running".
func (a *App) replanFailedTeamTask(teamID, taskID string) {
	store, err := a.requireTeamStore()
	if err != nil {
		return
	}
	tm, ok := store.Get(teamID)
	if !ok {
		return
	}
	tk, ok := tm.Task(taskID)
	if !ok || !teamTaskFailed(tk.Status) {
		return
	}
	updated, res, err := store.ApplyReplan(teamID, taskID)
	if err != nil || !res.Attempted() {
		return
	}
	a.emitTeamChanged(updated)
	if res.Outcome != teampkg.ReplanReassign {
		return
	}
	// Retry on the new assignee. A failure to start (e.g. a dependency
	// disappeared) leaves the card queued and assigned, which is still a better
	// resting state than failed, so the reason is surfaced as the progress line.
	if _, err := a.RunTeamTask(teamID, taskID); err != nil {
		a.setTeamTaskProgress(store, teamID, taskID, "已改派，待启动："+res.Reason)
	}
}

func teamTaskFailed(s taskmonitor.TaskState) bool {
	return s == taskmonitor.TaskStateFailed || s == taskmonitor.TaskStateStale
}

// CancelTeamTask stops an in-flight member run. The card returns to 待开始
// (queued + assigned) so it can be re-run.
func (a *App) CancelTeamTask(teamID, taskID string) error {
	key := teamRunKey(teamID, taskID)
	a.teamRunsMu.Lock()
	cancel, ok := a.teamRuns[key]
	if ok {
		delete(a.teamRuns, key)
	}
	a.teamRunsMu.Unlock()
	if !ok {
		return fmt.Errorf("该任务没有正在执行的运行")
	}
	cancel()
	return nil
}

// RunningTeamTasks returns the IDs of a team's cards with an in-flight run, so a
// remounted board can restore its spinners (the goroutine outlives the panel).
func (a *App) RunningTeamTasks(teamID string) []string {
	a.teamRunsMu.Lock()
	defer a.teamRunsMu.Unlock()
	prefix := teamID + "/"
	out := make([]string, 0, len(a.teamRuns))
	for key := range a.teamRuns {
		if strings.HasPrefix(key, prefix) {
			out = append(out, strings.TrimPrefix(key, prefix))
		}
	}
	sort.Strings(out)
	return out
}

// runTeamMember is the background half of RunTeamTask: resolve the member's
// model + tools, run the isolated session, then record the outcome. It owns
// releasing the run slot (the defer), so every exit path is covered.
func (a *App) runTeamMember(ctx context.Context, cancel context.CancelFunc, key, teamID string, tm teampkg.Team, member teampkg.Member, tk teampkg.Task) {
	defer func() {
		cancel()
		a.forgetTeamRun(key)
	}()

	store, err := a.requireTeamStore()
	if err != nil {
		return
	}

	entry, err := (&desktopExpertRunner{app: a}).resolveEntry(member.Model)
	if err != nil {
		a.finishTeamTask(teamID, tk.ID, "", err)
		return
	}
	// A member may pin its own reasoning effort; otherwise inherit the entry's.
	if e := strings.TrimSpace(member.Effort); e != "" && e != entry.Effort {
		cp := *entry
		cp.Effort = e
		entry = &cp
	}
	// mainProvider=false: a member run is background work and must respect
	// reserve_main so it can never starve the foreground conversation.
	prov, err := boot.NewProviderWithProxy(entry, netclient.ProxySpec{Mode: netclient.ModeAuto}, false)
	if err != nil {
		a.finishTeamTask(teamID, tk.ID, "", fmt.Errorf("构建模型失败: %w", err))
		return
	}

	reg := a.teamRegistry(member)
	opts := agent.Options{
		MaxSteps:            teampkg.DefaultMemberMaxSteps,
		ContextWindow:       entry.ContextWindow,
		RequireVisibleFinal: true,
		ModelRef:            entry.Model,
		Gate:                a.teamGate(),
	}

	// Progress is written back at most twice a second: the board shows liveness
	// without rewriting the team file on every streamed token.
	var mu sync.Mutex
	last := time.Time{}
	flush := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(last) < 500*time.Millisecond {
			return
		}
		last = time.Now()
		a.setTeamTaskProgress(store, teamID, tk.ID, line)
	}
	sink := event.FuncSink(func(e event.Event) {
		switch e.Kind {
		case event.ToolDispatch:
			if name := strings.TrimSpace(e.Tool.Name); name != "" {
				flush("🔧 " + name)
			}
		case event.Text:
			if s := clipTeamLine(e.Text, 96); s != "" {
				flush(s)
			}
		}
	})

	// The member's system prompt carries its persona + the L0 blackboard digest;
	// the user message carries the card. Neither ever reaches the main session.
	sess := agent.NewSession(teampkg.MemberRunPrompt(tm, member, tk))
	sub := agent.New(prov, reg, sess, opts, sink)
	runErr := sub.Run(ctx, teampkg.TaskBrief(tm, tk))
	answer := strings.TrimSpace(lastAssistantText(sess))

	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		a.finishTeamTaskCanceled(teamID, tk.ID)
	case runErr != nil && answer == "":
		a.finishTeamTask(teamID, tk.ID, "", runErr)
	default:
		// A step-cap stop that still produced text is treated as a delivery, the
		// same way the expert runner recovers a partial answer: a researched-but-
		// truncated card beats an empty one on the board.
		a.finishTeamTask(teamID, tk.ID, answer, nil)
	}
}

// teamRegistry resolves the tool set a member runs with.
//
// Preferred: the active tab's live registry — already workspace-bound (writers
// confined to the project), sandboxed, and carrying the session's plugin/MCP
// tools. This is why the runner does not try to rebuild the sandbox config.
// A member's declared Tools narrow it; subagent/skill meta-tools stay excluded so
// delegation stays one layer deep.
func (a *App) teamRegistry(member teampkg.Member) *tool.Registry {
	if ctrl, ok := a.activeCtrl().(*control.Controller); ok && ctrl != nil {
		if reg := ctrl.ToolRegistry(); reg != nil && len(reg.Names()) > 0 {
			if len(member.Tools) == 0 {
				return reg
			}
			narrowed := agent.FilterRegistry(reg, member.Tools, agent.SubagentMetaTools()...)
			if len(narrowed.Names()) > 0 {
				return narrowed
			}
			return reg
		}
	}
	// Fallback: workspace-bound built-ins, so a member can still read/write/run
	// instead of starting tool-less (e.g. no active tab, or a remote session).
	if tab := a.activeTab(); tab != nil {
		ws := builtin.Workspace{Dir: tab.WorkspaceRoot, ProxySpec: netclient.ProxySpec{Mode: netclient.ModeAuto}}
		reg := tool.NewRegistry()
		for _, t := range ws.Tools(member.Tools...) {
			addTeamTool(reg, t)
		}
		if len(reg.Names()) > 0 {
			return reg
		}
	}
	return tool.NewRegistry()
}

// teamGate returns the headless permission gate for a member run, so hard-deny
// rules still bite while an unattended sub-session never waits on a prompt.
func (a *App) teamGate() agent.Gate {
	if ctrl, ok := a.activeCtrl().(*control.Controller); ok && ctrl != nil {
		return ctrl.HeadlessGate()
	}
	return nil
}

func addTeamTool(reg *tool.Registry, t tool.Tool) {
	defer func() { _ = recover() }() // duplicate built-in: keep the first
	reg.Add(t)
}

// setTeamTaskProgress writes the live progress line onto a card and pushes the
// card to the board. It deliberately uses the light `team:task` event rather
// than `team:changed`: progress fires several times a second, and the board only
// needs to patch one card.
func (a *App) setTeamTaskProgress(store *teampkg.Store, teamID, taskID, line string) {
	tm, err := store.Update(teamID, func(t *teampkg.Team) {
		for i := range t.Tasks {
			if t.Tasks[i].ID == taskID {
				t.Tasks[i].Progress = line
			}
		}
	})
	if err != nil {
		return
	}
	a.emitTeamTask(tm, taskID)
}

// emitTeamTask pushes one card's current state.
func (a *App) emitTeamTask(tm teampkg.Team, taskID string) {
	if a.ctx == nil {
		return
	}
	tk, ok := tm.Task(taskID)
	if !ok {
		return
	}
	runtime.EventsEmit(a.ctx, "team:task", TeamTaskProgress{
		TeamID:     tm.ID,
		TaskID:     tk.ID,
		Status:     string(tk.Status),
		Column:     teampkg.ColumnFor(tm, tk),
		Progress:   tk.Progress,
		Error:      tk.Error,
		Attempts:   tk.Attempts,
		AssigneeID: tk.AssigneeID,
		Assignee:   tm.MemberName(tk.AssigneeID),
	})
}

// finishTeamTask records a terminal outcome. Success stores the delivery and
// publishes it on the blackboard (so later members see what already exists — the
// shared-context payoff); failure records the reason for the leader's re-plan.
func (a *App) finishTeamTask(teamID, taskID, answer string, runErr error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return
	}
	failed := runErr != nil
	tm, err := store.Update(teamID, func(t *teampkg.Team) {
		for i := range t.Tasks {
			if t.Tasks[i].ID != taskID {
				continue
			}
			card := t.Tasks[i]
			card.Progress = ""
			card.UpdatedAt = time.Now().UTC()
			if failed {
				card.Status = taskmonitor.TaskStateFailed
				card.Error = firstNonBlankTeam(strings.TrimSpace(runErr.Error()), "执行失败")
				t.Tasks[i] = card
				continue
			}
			card.Status = taskmonitor.TaskStateSucceeded
			card.Error = ""
			if answer != "" {
				card.Deliverable = answer
				card.Evidence = append(card.Evidence,
					"团员「"+t.MemberName(card.AssigneeID)+"」以独立上下文执行完成")
			}
			t.Tasks[i] = card
			if !teamHasArtifact(t.Context.Artifacts, card.ID) {
				t.Context.Artifacts = append(t.Context.Artifacts, teampkg.ResultArtifact(card, answer))
				t.Context.Version++
			}
		}
	})
	if err != nil {
		return
	}
	a.emitTeamChanged(tm)
}

// finishTeamTaskCanceled returns a cancelled card to 待开始 so it can be re-run.
func (a *App) finishTeamTaskCanceled(teamID, taskID string) {
	store, err := a.requireTeamStore()
	if err != nil {
		return
	}
	tm, err := store.Update(teamID, func(t *teampkg.Team) {
		for i := range t.Tasks {
			if t.Tasks[i].ID != taskID {
				continue
			}
			t.Tasks[i].Status = taskmonitor.TaskStateQueued
			t.Tasks[i].Progress = ""
			t.Tasks[i].Error = ""
			t.Tasks[i].UpdatedAt = time.Now().UTC()
		}
	})
	if err != nil {
		return
	}
	a.emitTeamChanged(tm)
}

func (a *App) forgetTeamRun(key string) {
	a.teamRunsMu.Lock()
	delete(a.teamRuns, key)
	a.teamRunsMu.Unlock()
}

// unmetTeamDeps lists a card's prerequisites that have not reached a
// terminal-success state yet (their titles, for the error message).
func unmetTeamDeps(tm teampkg.Team, tk teampkg.Task) []string {
	var missing []string
	for _, dep := range tk.Deps {
		d, ok := tm.Task(dep)
		if !ok {
			continue
		}
		if d.Status != taskmonitor.TaskStateSucceeded {
			missing = append(missing, firstNonBlankTeam(d.Title, dep))
		}
	}
	return missing
}

func teamHasArtifact(arts []teampkg.Artifact, taskID string) bool {
	for _, a := range arts {
		if a.TaskID == taskID {
			return true
		}
	}
	return false
}

// clipTeamLine collapses a streamed delta to one short board line.
func clipTeamLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
