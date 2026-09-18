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
	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/boot"
	"github.com/zzycxz/hiq/internal/control"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/netclient"
	"github.com/zzycxz/hiq/internal/taskmonitor"
	teampkg "github.com/zzycxz/hiq/internal/team"
	"github.com/zzycxz/hiq/internal/tool"
	"github.com/zzycxz/hiq/internal/tool/builtin"
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

// resolveTeamRunTarget loads a card plus everything needed to run it, applying
// the same gates the board applies before enabling "run": the assignee must
// still exist and the card's dependencies must have reached terminal success.
//
// Shared by both launch paths — a user-initiated run (which then asks the pool
// for a slot) and a queue promotion (which already holds one) — so the two can
// never drift apart.
func (a *App) resolveTeamRunTarget(teamID, taskID string) (teampkg.Team, teampkg.Task, teampkg.Member, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return teampkg.Team{}, teampkg.Task{}, teampkg.Member{}, err
	}
	tm, ok := store.Get(teamID)
	if !ok {
		return teampkg.Team{}, teampkg.Task{}, teampkg.Member{}, fmt.Errorf("团队不存在")
	}
	tk, ok := tm.Task(taskID)
	if !ok {
		return teampkg.Team{}, teampkg.Task{}, teampkg.Member{}, fmt.Errorf("任务不存在")
	}
	if strings.TrimSpace(tk.AssigneeID) == "" {
		return teampkg.Team{}, teampkg.Task{}, teampkg.Member{},
			fmt.Errorf("这张卡片还没有负责人：先拖到「待开始」按能力自动指派，或手动指派")
	}
	member, ok := tm.Member(tk.AssigneeID)
	if !ok {
		return teampkg.Team{}, teampkg.Task{}, teampkg.Member{}, fmt.Errorf("负责人已不在团队中，请重新指派")
	}
	if missing := unmetTeamDeps(tm, tk); len(missing) > 0 {
		return teampkg.Team{}, teampkg.Task{}, teampkg.Member{},
			fmt.Errorf("前置任务尚未完成：%s", strings.Join(missing, "、"))
	}
	return tm, tk, member, nil
}

// RunTeamTask executes a card as its assignee's isolated sub-session and returns
// the board immediately (the run itself streams in the background, updating
// Progress and finally the card's state). It is the "让团员真正干活" entry point.
//
// The card must first win a concurrency slot: Policy.MaxParallel is a bound, not
// a suggestion, so a card arriving at a full pool is parked in the team's FIFO
// queue instead of opening a session (P1-1).
func (a *App) RunTeamTask(teamID, taskID string) (TeamProjectView, error) {
	tm, tk, member, err := a.resolveTeamRunTarget(teamID, taskID)
	if err != nil {
		return TeamProjectView{}, err
	}
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	key := teamRunKey(teamID, taskID)
	pool := a.teamPoolEnsure(tm.ID, tm.Policy.MaxParallel)

	switch pool.Admit(key) {
	case teampkg.AdmitDuplicate:
		return TeamProjectView{}, fmt.Errorf("该任务正在执行或排队中")
	case teampkg.AdmitQueued:
		// No slot yet: park the card. The pool hands the key back through
		// releaseTeamSlot when a slot frees up.
		queued, err := store.Update(teamID, func(t *teampkg.Team) {
			for i := range t.Tasks {
				if t.Tasks[i].ID != taskID {
					continue
				}
				t.Tasks[i].Status = taskmonitor.TaskStateQueued
				t.Tasks[i].Error = ""
				t.Tasks[i].Progress = a.teamQueueNote(pool, key)
				t.Tasks[i].UpdatedAt = time.Now().UTC()
			}
		})
		if err != nil {
			// Never leave the card holding a queue slot it no longer has a
			// record for.
			pool.Dequeue(key)
			return TeamProjectView{}, err
		}
		a.emitTeamChanged(queued)
		return toTeamProjectView(queued), nil
	}
	return a.launchTeamTask(teamID, taskID, tk, member)
}

// launchTeamTask starts the goroutine for a card whose slot is ALREADY booked in
// the pool — either Admit returned AdmitRunning, or Release promoted the card out
// of the queue. Callers must NOT admit again for this key.
func (a *App) launchTeamTask(teamID, taskID string, tk teampkg.Task, member teampkg.Member) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
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
	a.teamRuns[key] = cancel
	a.teamRunsMu.Unlock()

	// Flip the card to running BEFORE dispatching so the board shows a spinner
	// the moment the call returns. The member prompt is built from the snapshot
	// taken by the caller, so the status flip cannot affect what the member is
	// told.
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
		// Hand the slot back so a queued card is not stranded behind a card
		// that never started.
		a.releaseTeamSlot(teamID, key)
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

// releaseTeamSlot frees a finished run's slot and starts the next queued card, if
// any. Because it runs from runTeamMember's defer, every exit path — success,
// failure, cancel — gives the slot back, so the queue always keeps moving.
func (a *App) releaseTeamSlot(teamID, key string) {
	pool := a.teamPoolGet(teamID)
	if pool == nil {
		return
	}
	next, ok := pool.Release(key)
	if !ok {
		return
	}
	a.startPromotedTeamRun(next)
}

// startPromotedTeamRun starts a card the pool promoted out of the queue. Its slot
// is already booked, so this path re-reads the card (its assignee or dependency
// state may have changed while it waited) and launches directly, never admitting
// again — and hands the slot back if the card is no longer runnable, so one dead
// card cannot stall the queue behind it.
func (a *App) startPromotedTeamRun(key string) {
	teamID, taskID, ok := splitTeamRunKey(key)
	if !ok {
		return
	}
	_, tk, member, err := a.resolveTeamRunTarget(teamID, taskID)
	if err != nil {
		a.releaseTeamSlot(teamID, key)
		a.setTeamProgressNote(teamID, taskID, "排队期间条件已变化，未能启动："+err.Error())
		return
	}
	if _, err := a.launchTeamTask(teamID, taskID, tk, member); err != nil {
		a.setTeamProgressNote(teamID, taskID, "启动失败："+err.Error())
	}
}

// splitTeamRunKey inverts teamRunKey. Team IDs are generated without a slash, so
// the first separator is the boundary.
func splitTeamRunKey(key string) (teamID, taskID string, ok bool) {
	i := strings.Index(key, "/")
	if i <= 0 || i == len(key)-1 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

// teamPoolEnsure returns a team's pool, creating it with the team's policy bound.
func (a *App) teamPoolEnsure(teamID string, max int) *teampkg.Pool {
	return a.teamPoolsInit().Ensure(teamID, max)
}

// teamPoolGet returns an existing pool without creating one.
func (a *App) teamPoolGet(teamID string) *teampkg.Pool {
	pools := a.teamPools
	if pools == nil {
		return nil
	}
	return pools.Get(teamID)
}

// teamPoolsInit lazily creates the per-team pool registry. It is safe to call
// from concurrent RPC handlers, which matters because requireTeamStore can
// re-create the store outside startup.
func (a *App) teamPoolsInit() *teampkg.Pools {
	a.teamPoolsMu.Lock()
	defer a.teamPoolsMu.Unlock()
	if a.teamPools == nil {
		a.teamPools = teampkg.NewPools()
	}
	return a.teamPools
}

// teamQueueNote renders the "waiting" line for a card parked in the pool queue,
// so the board says where it stands instead of a bare "queued".
func (a *App) teamQueueNote(pool *teampkg.Pool, key string) string {
	pos, depth := 0, 0
	if pool != nil {
		snap := pool.Snapshot()
		depth = len(snap.Queued)
		for i, k := range snap.Queued {
			if k == key {
				pos = i + 1
				break
			}
		}
	}
	if pos == 0 {
		return "排队中（等前面的成员跑完自动开始）"
	}
	return fmt.Sprintf("排队中（第 %d/%d 位，等前面的成员跑完自动开始）", pos, depth)
}

// setTeamProgressNote writes one progress line and refreshes the board. Used for
// notes that are not tied to a live run (queue or startup problems).
func (a *App) setTeamProgressNote(teamID, taskID, line string) {
	store, err := a.requireTeamStore()
	if err != nil {
		return
	}
	tm, err := store.Update(teamID, func(t *teampkg.Team) {
		for i := range t.Tasks {
			if t.Tasks[i].ID != taskID {
				continue
			}
			t.Tasks[i].Progress = line
			t.Tasks[i].UpdatedAt = time.Now().UTC()
		}
	})
	if err != nil {
		return
	}
	a.emitTeamChanged(tm)
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

// gateReason explains why a before-gate card is parked, so the board's 待确认
// column says something more useful than a bare "pending".
func gateReason(tk teampkg.Task) string {
	if s := strings.TrimSpace(tk.ApprovalNote); s != "" {
		return s
	}
	return "执行前需要人工确认（卡片已设为「执行前确认」）"
}

func teamTaskFailed(s taskmonitor.TaskState) bool {
	return s == taskmonitor.TaskStateFailed || s == taskmonitor.TaskStateStale
}

// CancelTeamTask stops a card's run — or, when the card is still waiting, simply
// drops it from the pool queue (there is no run to cancel, only a wait to end).
// Either way the card returns to 待开始 (queued + assigned) so it can be re-run.
func (a *App) CancelTeamTask(teamID, taskID string) error {
	key := teamRunKey(teamID, taskID)
	if pool := a.teamPoolGet(teamID); pool != nil && pool.Dequeue(key) {
		a.setTeamProgressNote(teamID, taskID, "")
		return nil
	}
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

// RunningTeamTasks returns the IDs of a team's cards that hold a concurrency slot
// or are waiting in its queue, so a remounted board restores both its spinners
// and its 排队中 notes (the goroutine outlives the panel).
func (a *App) RunningTeamTasks(teamID string) []string {
	pool := a.teamPoolGet(teamID)
	if pool == nil {
		return []string{}
	}
	snap := pool.Snapshot()
	keys := make([]string, 0, len(snap.Running)+len(snap.Queued))
	keys = append(keys, snap.Running...)
	keys = append(keys, snap.Queued...)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if tid, taskID, ok := splitTeamRunKey(key); ok && tid == teamID {
			out = append(out, taskID)
		}
	}
	sort.Strings(out)
	return out
}

// recoverTeamRuns reconciles cards left "running" by a previous process (P1-2).
//
// A member run exists only in memory — a goroutine plus a pool slot — so a
// persisted card still marked running at startup cannot have a live run behind
// it: the process that owned it is gone. Such a card is moved to stale with an
// honest note rather than being silently re-run, because an app that starts
// burning quota the moment it opens is worse than one that says it was
// interrupted and offers a rerun.
func (a *App) recoverTeamRuns() {
	store, err := a.requireTeamStore()
	if err != nil {
		return
	}
	for _, tm := range store.List() {
		// At startup every pool is empty, but the check is written against the
		// live set anyway so the function stays correct if it is ever called
		// while runs are in flight.
		live := map[string]struct{}{}
		if pool := a.teamPoolGet(tm.ID); pool != nil {
			for _, key := range pool.Snapshot().Running {
				live[key] = struct{}{}
			}
		}
		changed := false
		updated, err := store.Update(tm.ID, func(t *teampkg.Team) {
			for i := range t.Tasks {
				if t.Tasks[i].Status != taskmonitor.TaskStateRunning {
					continue
				}
				if _, ok := live[teamRunKey(t.ID, t.Tasks[i].ID)]; ok {
					continue
				}
				t.Tasks[i].Status = taskmonitor.TaskStateStale
				t.Tasks[i].Progress = ""
				t.Tasks[i].Error = "上次运行被中断（程序已退出），可直接重新运行"
				t.Tasks[i].UpdatedAt = time.Now().UTC()
				changed = true
			}
		})
		if err != nil || !changed {
			continue
		}
		a.emitTeamChanged(updated)
	}
}

// CancelAllTeamRuns stops every in-flight member run and empties every queue
// (P1-3) — the app-wide counterpart of open-vetta's work.stop-all.
//
// Closing a workspace or quitting must not leave member sessions burning quota
// behind a hidden panel, so this runs on the shutdown path. Cards that were only
// waiting have nothing to cancel; their notes are cleared so the board stops
// claiming they are queued.
func (a *App) CancelAllTeamRuns(reason string) {
	if pools := a.teamPools; pools != nil {
		pools.CancelAll()
	}

	a.teamRunsMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(a.teamRuns))
	for key, cancel := range a.teamRuns {
		cancels = append(cancels, cancel)
		delete(a.teamRuns, key)
	}
	a.teamRunsMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}

	store, err := a.requireTeamStore()
	if err != nil {
		return
	}
	for _, tm := range store.List() {
		changed := false
		updated, err := store.Update(tm.ID, func(t *teampkg.Team) {
			for i := range t.Tasks {
				if t.Tasks[i].Status != taskmonitor.TaskStateQueued || t.Tasks[i].Progress == "" {
					continue
				}
				t.Tasks[i].Progress = ""
				t.Tasks[i].UpdatedAt = time.Now().UTC()
				changed = true
			}
		})
		if err != nil || !changed {
			continue
		}
		a.emitTeamChanged(updated)
	}
	_ = reason
}

// runTeamMember is the background half of RunTeamTask: resolve the member's
// model + tools, run the isolated session, then record the outcome. It owns
// releasing the run slot (the defer), so every exit path is covered.
func (a *App) runTeamMember(ctx context.Context, cancel context.CancelFunc, key, teamID string, tm teampkg.Team, member teampkg.Member, tk teampkg.Task) {
	defer func() {
		cancel()
		a.forgetTeamRun(key)
		// Released last, so a card promoted out of the queue starts only after
		// this run's bookkeeping is complete.
		a.releaseTeamSlot(teamID, key)
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

	reg := a.teamRegistry(store, tm, member)
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
	//
	// The archived-note count lets the digest tell the member that older notes
	// exist and how to page them (P1-4); a read failure just omits the hint
	// rather than blocking the run.
	archived, err := store.CountNotes(teamID)
	if err != nil {
		archived = 0
	}
	sess := agent.NewSession(teampkg.MemberRunPromptWithArchive(tm, member, tk, archived))
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
//
// The live registry is CLONED before anything is added: run-scoped tools (the
// shared-history reader) must never leak into the main session's tool list,
// which is part of its cached prompt prefix.
func (a *App) teamRegistry(store *teampkg.Store, tm teampkg.Team, member teampkg.Member) *tool.Registry {
	reg := a.teamBaseRegistry(member)
	reg.Add(&teamSharedHistoryTool{store: store, team: tm})
	return reg
}

// teamBaseRegistry resolves the member's base tool set without any run-scoped
// additions. It always returns a registry the caller may mutate.
func (a *App) teamBaseRegistry(member teampkg.Member) *tool.Registry {
	if ctrl, ok := a.activeCtrl().(*control.Controller); ok && ctrl != nil {
		if live := ctrl.ToolRegistry(); live != nil && len(live.Names()) > 0 {
			if len(member.Tools) == 0 {
				return live.Clone()
			}
			narrowed := agent.FilterRegistry(live, member.Tools, agent.SubagentMetaTools()...)
			if len(narrowed.Names()) > 0 {
				return narrowed
			}
			return live.Clone()
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
	// P4 共享上下文: a member publishes notes by ending its answer with a
	// 【共享笔记】 section. They are peeled off here and posted to the team's
	// blackboard, so the NEXT member — a different context window entirely —
	// reads them in its run prompt. The stored deliverable keeps the clean body.
	notes, body := teampkg.ExtractSharedNotes(answer)
	// addedNotes collects the notes the hot window actually accepted, so the
	// durable archive receives exactly those (a deduped duplicate belongs in
	// neither place). It is filled inside the Update transaction below.
	var addedNotes []teampkg.Note
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
			if body != "" {
				card.Deliverable = body
				card.Evidence = append(card.Evidence,
					"团员「"+t.MemberName(card.AssigneeID)+"」以独立上下文执行完成")
			}
			if len(notes) > 0 {
				batch := make([]teampkg.Note, 0, len(notes))
				for _, txt := range notes {
					batch = append(batch, teampkg.NewNote(card.AssigneeID, card.ID, txt))
				}
				var added []teampkg.Note
				t.Context.Notes, added = teampkg.AddNotes(t.Context.Notes, batch...)
				addedNotes = added
				if len(added) > 0 {
					t.Context.Version++
				}
			}
			// P4 HITL: an after-gate card does not complete on the member's word
			// alone — the deliverable is held until a human accepts it.
			if card.GateAfter() {
				teampkg.GateDeliverable(&card, "产出待人工确认")
			}
			t.Tasks[i] = card
			if !teamHasArtifact(t.Context.Artifacts, card.ID) {
				t.Context.Artifacts = append(t.Context.Artifacts, teampkg.ResultArtifact(card, body))
				t.Context.Version++
			}
		}
	})
	if err != nil {
		return
	}
	// The archive is what keeps a note retrievable after it leaves the hot
	// window, so it is written once the store has accepted the notes — and only
	// then. A failed append costs the paging history, never the board update.
	if len(addedNotes) > 0 {
		_ = store.AppendNotes(teamID, addedNotes)
	}
	a.emitTeamChanged(tm)
	// A finished card is the natural checkpoint trigger: the window just grew.
	// Summarisation runs off this path so the member's card never waits on it.
	if len(addedNotes) > 0 {
		a.checkpointTeamAsync(teamID)
	}
}

// checkpointTeamAsync folds the oldest shared notes into a checkpoint when the
// hot window has outgrown the digest (P1-4 step B).
//
// One pass per team at a time: a burst of members finishing at once would
// otherwise launch several summarisation calls over the same notes, and the
// later ones would fold an already-folded range.
func (a *App) checkpointTeamAsync(teamID string) {
	if !a.beginTeamCheckpoint(teamID) {
		return
	}
	go func() {
		defer a.endTeamCheckpoint(teamID)
		a.runTeamCheckpoint(teamID)
	}()
}

// beginTeamCheckpoint claims the single checkpoint slot for a team. It reports
// false when a pass is already running, so the caller can simply drop the
// request — the running pass will observe the same (or a larger) fold range.
func (a *App) beginTeamCheckpoint(teamID string) bool {
	a.teamCheckpointMu.Lock()
	defer a.teamCheckpointMu.Unlock()
	if a.teamCheckpointing[teamID] {
		return false
	}
	if a.teamCheckpointing == nil {
		a.teamCheckpointing = map[string]bool{}
	}
	a.teamCheckpointing[teamID] = true
	return true
}

// endTeamCheckpoint releases the slot. It must run on every exit path, or a
// team would never checkpoint again.
func (a *App) endTeamCheckpoint(teamID string) {
	a.teamCheckpointMu.Lock()
	delete(a.teamCheckpointing, teamID)
	a.teamCheckpointMu.Unlock()
}

// runTeamCheckpoint performs one checkpoint pass. It is the goroutine half of
// checkpointTeamAsync and never returns an error: a checkpoint is an
// optimisation over the archive, so failing to produce one must leave the team
// exactly as it was.
func (a *App) runTeamCheckpoint(teamID string) {
	store, err := a.requireTeamStore()
	if err != nil {
		return
	}
	tm, ok := store.Get(teamID)
	if !ok {
		return
	}
	fold := teampkg.PendingCheckpointNotes(tm)
	if len(fold) == 0 {
		return
	}
	summary, degraded := a.summarizeTeamNotes(tm, fold)
	updated, err := store.Update(teamID, func(t *teampkg.Team) {
		// ApplyCheckpoint removes exactly the folded notes by ID, so a note some
		// other member published while the summary was being written survives.
		teampkg.ApplyCheckpoint(t, fold, summary, degraded)
	})
	if err != nil {
		return
	}
	a.emitTeamChanged(updated)
}

// summarizeTeamNotes compresses folded notes with the leader's model. A failure
// at any layer — no usable model, timeout, empty reply — degrades to the
// index-style summary rather than skipping the checkpoint, because the notes it
// covers are about to leave the window either way.
func (a *App) summarizeTeamNotes(tm teampkg.Team, notes []teampkg.Note) (string, bool) {
	model := ""
	if leader, ok := tm.Leader(); ok {
		model = strings.TrimSpace(leader.Model)
	}
	system, user := teampkg.CheckpointPrompt(tm, notes)
	out, err := a.runTeamDraftLLM(model, system, user)
	if err != nil || strings.TrimSpace(out) == "" {
		return "", true
	}
	return strings.TrimSpace(out), false
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
