package main

// expert_runs.go owns the in-flight-run tracking and main-session persistence
// for expert-team collaborations.
//
// Two concerns live here:
//
//  1. Active-run state (expertRunState): when a panel-initiated run starts we
//     record it keyed by teamID. If the CoWorkLayout is torn down mid-run (a
//     tab/profile switch unmounts it), the remounted panel queries
//     GetActiveExpertRun to discover the run is still going and re-subscribes
//     to the "experts:collab" stream. The backend goroutine is decoupled from
//     the frontend and keeps running regardless.
//
//  2. Persistence into the main session: on success the full CollabResult is
//     appended to the active tab's session as a folded-block message (the
//     context layer later projects it down to a synthesis-only view for the
//     model). This is the "archive layer" that survives restarts.

import (
	"context"
	"time"

	"github.com/zzycxz/fairpeer/internal/event"
	"github.com/zzycxz/fairpeer/internal/experts"
	"github.com/zzycxz/fairpeer/internal/provider"
)

// expertRunStatus labels an in-flight run's phase for the frontend.
type expertRunStatus string

const (
	expertRunRunning expertRunStatus = "running"
	expertRunDone    expertRunStatus = "done"
	expertRunError   expertRunStatus = "error"
)

// expertRunState is the queryable status of one expert-team run. It is set when
// a panel-initiated run starts and updated when it finishes, so a remounted
// panel can recover "is a run still going for this team?" without keeping
// React state alive across unmounts.
//
// streamCache holds the accumulated StreamMessages for this run. The backend
// goroutine keeps running even when the user switches to another tab (which
// unmounts ExpertSessionView and its onExpertsCollab subscription). When the
// user switches back, GetActiveExpertRun returns the cached messages so the
// frontend can render the experts' progress that happened while the tab was
// hidden — without this, those events are lost (they were broadcast to nobody).
type expertRunState struct {
	runID       string
	teamID      string
	tabID       string // the expert-session tab this run writes to
	teamName    string
	task        string
	mode        string
	status      expertRunStatus
	startedAt   int64 // unix ms
	err         string
	cancel      context.CancelFunc  // cancels the orchestrator goroutine (e.g. on tab close)
	streamCache []StreamMessageWire // accumulated live-stream messages (protected by expertRunsMu)
}

// StreamMessageWire mirrors the frontend's StreamMessage type. It's the JSON
// form of one expert's (or the synthesis's) accumulated text in the live
// collaboration stream. Returned via GetActiveExpertRun so a remounted
// ExpertSessionView can restore the progress that happened while it was
// unmounted.
type StreamMessageWire struct {
	Kind       string `json:"kind"` // "expert" | "synthesis"
	ExpertName string `json:"expertName"`
	Round      int    `json:"round"`
	Text       string `json:"text"`
	Streaming  bool   `json:"streaming"`
}

// ExpertRunView is the JSON shape returned to the frontend by GetActiveExpertRun.
type ExpertRunView struct {
	RunID     string              `json:"runId,omitempty"`
	TeamID    string              `json:"teamId,omitempty"`
	TeamName  string              `json:"teamName,omitempty"`
	Task      string              `json:"task,omitempty"`
	Mode      string              `json:"mode,omitempty"`
	Status    string              `json:"status,omitempty"` // "" | "running" | "done" | "error"
	StartedAt int64               `json:"startedAt,omitempty"`
	Err       string              `json:"err,omitempty"`
	Messages  []StreamMessageWire `json:"messages,omitempty"` // cached live-stream state for recovery
}

// markExpertRunStarted records a run as in-flight. If a prior run for the same
// team is still running, it is CANCELLED first — this prevents two goroutines
// from racing to write into the same expert-session tab (double AppendExpertCollab,
// interleaved stream events, and the old run's terminal status clobbering the
// new run's). The old goroutine's context is cancelled so it exits at the next
// check point.
func (a *App) markExpertRunStarted(runID, teamID, tabID, teamName, task, mode string, cancel context.CancelFunc) {
	a.expertRunsMu.Lock()
	defer a.expertRunsMu.Unlock()
	if a.expertRuns == nil {
		a.expertRuns = map[string]*expertRunState{}
	}
	// Cancel any prior in-flight run for this team before replacing it.
	if prev, ok := a.expertRuns[teamID]; ok && prev.status == expertRunRunning && prev.cancel != nil {
		prev.cancel()
	}
	a.expertRuns[teamID] = &expertRunState{
		runID: runID, teamID: teamID, tabID: tabID, teamName: teamName, task: task, mode: mode,
		status: expertRunRunning, startedAt: time.Now().UnixMilli(), cancel: cancel,
	}
}

// markExpertRunFinished updates a run's terminal status, then removes the entry
// after a short delay. The delay lets a remounted frontend panel observe the
// terminal status (done/error) via GetActiveExpertRun before the entry is
// cleaned up. Without removal, completed runs leak forever and GetActiveExpertRun
// keeps returning stale "done" for teams whose run finished long ago.
func (a *App) markExpertRunFinished(teamID string, status expertRunStatus, errMsg string) {
	a.expertRunsMu.Lock()
	if a.expertRuns == nil {
		a.expertRunsMu.Unlock()
		return
	}
	st, ok := a.expertRuns[teamID]
	if !ok {
		a.expertRunsMu.Unlock()
		return
	}
	// Only update if this entry still belongs to the run that called us —
	// a newer run may have already replaced it (markExpertRunStarted cancels
	// the old run, but the old goroutine may finish after the new one starts).
	st.status = status
	st.err = errMsg
	// Nil the cancel so the delayed cleanup doesn't hold a dangling closure.
	st.cancel = nil
	a.expertRunsMu.Unlock()

	// Remove the entry after a grace period so a remounted panel can read the
	// terminal status. If a new run starts for this team in the meantime, it
	// replaces the entry and this goroutine's delete becomes a harmless no-op
	// (the entry no longer matches the finished run).
	go func() {
		time.Sleep(10 * time.Second)
		a.expertRunsMu.Lock()
		defer a.expertRunsMu.Unlock()
		if cur, ok := a.expertRuns[teamID]; ok && cur.status != expertRunRunning {
			delete(a.expertRuns, teamID)
		}
	}()
}

// cancelExpertRun cancels an in-flight run for a team and removes its entry.
// Called when a team is deleted (DeleteExpertTeam) so the orchestrator goroutine
// doesn't keep calling LLMs for a team that no longer exists.
func (a *App) cancelExpertRun(teamID string) {
	a.expertRunsMu.Lock()
	defer a.expertRunsMu.Unlock()
	if a.expertRuns == nil {
		return
	}
	if st, ok := a.expertRuns[teamID]; ok {
		if st.status == expertRunRunning && st.cancel != nil {
			st.cancel()
		}
		delete(a.expertRuns, teamID)
	}
}

// clearExpertRun cancels any in-flight run for a team and drops its run state.
// Called when a team is deleted (DeleteExpertTeam). Unlike the old version which
// only deleted the bookkeeping (leaving the goroutine running), this now cancels
// the orchestrator goroutine first so it stops calling LLMs for a deleted team.
func (a *App) clearExpertRun(teamID string) {
	a.cancelExpertRun(teamID)
}

// cancelExpertRunByTab cancels any in-flight run whose expert-session tab
// matches tabID. Called from CloseTab so closing an expert tab mid-run stops
// the orchestrator goroutine instead of letting it write to a closed tab.
func (a *App) cancelExpertRunByTab(tabID string) {
	a.expertRunsMu.Lock()
	defer a.expertRunsMu.Unlock()
	for _, st := range a.expertRuns {
		if st.tabID == tabID && st.status == expertRunRunning && st.cancel != nil {
			st.cancel()
			st.status = expertRunError
			st.err = "专家会话已关闭"
			return
		}
	}
}

// cacheCollabEvent accumulates a streamed CollabEvent into the run's
// streamCache, mirroring the frontend's StreamMessage reducer. Called from
// emitExpertsCollab so every event is both broadcast (for live tabs) AND
// cached (for recovery when the user switches back to the tab later). No-op
// if the team has no active run.
func (a *App) cacheCollabEvent(ev experts.CollabEvent) {
	if ev.TeamID == "" {
		return
	}
	a.expertRunsMu.Lock()
	defer a.expertRunsMu.Unlock()
	st, ok := a.expertRuns[ev.TeamID]
	if !ok || st.status != expertRunRunning {
		return
	}
	switch ev.Phase {
	case experts.PhaseExpertStart:
		if ev.ExpertName == "" {
			return // aggregate "协作开始" marker, not a real expert
		}
		st.streamCache = append(st.streamCache, StreamMessageWire{
			Kind: "expert", ExpertName: ev.ExpertName, Round: ev.Round, Text: "", Streaming: true,
		})
	case experts.PhaseExpertChunk:
		// Append delta to the last matching expert message, or lazy-create.
		for i := len(st.streamCache) - 1; i >= 0; i-- {
			m := &st.streamCache[i]
			if m.Kind == "expert" && m.ExpertName == ev.ExpertName && m.Round == ev.Round {
				m.Text += ev.Text
				return
			}
		}
		st.streamCache = append(st.streamCache, StreamMessageWire{
			Kind: "expert", ExpertName: ev.ExpertName, Round: ev.Round, Text: ev.Text, Streaming: true,
		})
	case experts.PhaseExpertDone:
		for i := len(st.streamCache) - 1; i >= 0; i-- {
			m := &st.streamCache[i]
			if m.Kind == "expert" && m.ExpertName == ev.ExpertName && m.Round == ev.Round {
				m.Streaming = false
				return
			}
		}
	case experts.PhaseSynthesis:
		if ev.Text == "" {
			return
		}
		if n := len(st.streamCache); n > 0 && st.streamCache[n-1].Kind == "synthesis" {
			st.streamCache[n-1].Text += ev.Text
			return
		}
		st.streamCache = append(st.streamCache, StreamMessageWire{
			Kind: "synthesis", ExpertName: "", Round: 0, Text: ev.Text, Streaming: true,
		})
	}
}

// GetActiveExpertRun reports the current run state for a team (running/done/
// error), or an empty view if no run has been recorded. This lets a remounted
// ExpertPanel recover after the CoWorkLayout was torn down and re-mounted.
// For a running run, Messages carries the accumulated live-stream state so the
// frontend can render the experts' progress that happened while the tab was
// hidden.
func (a *App) GetActiveExpertRun(teamID string) ExpertRunView {
	a.expertRunsMu.Lock()
	defer a.expertRunsMu.Unlock()
	if a.expertRuns == nil {
		return ExpertRunView{}
	}
	st, ok := a.expertRuns[teamID]
	if !ok {
		return ExpertRunView{}
	}
	view := ExpertRunView{
		RunID: st.runID, TeamID: st.teamID, TeamName: st.teamName, Task: st.task,
		Mode: st.mode, Status: string(st.status), StartedAt: st.startedAt, Err: st.err,
	}
	// Return a copy of the cached messages (nil slice → omitted in JSON).
	if len(st.streamCache) > 0 {
		view.Messages = append([]StreamMessageWire(nil), st.streamCache...)
	}
	return view
}

// toEventCollab converts a persisted CollabRecord into the event.Collab payload
// the frontend renders (same fields, expert answers mapped to the event type).
func toEventCollab(r experts.CollabRecord) event.Collab {
	rounds := make([][]event.CollabAnswer, len(r.Rounds))
	for ri, round := range r.Rounds {
		rounds[ri] = make([]event.CollabAnswer, len(round))
		for ai, ans := range round {
			rounds[ri][ai] = event.CollabAnswer{ExpertName: ans.ExpertName, Text: ans.Text}
		}
	}
	return event.Collab{
		RunID: r.RunID, TeamID: r.TeamID, TeamName: r.TeamName, Task: r.Task,
		Mode: r.Mode, Rounds: rounds, Synthesis: r.Synthesis, CreatedAt: r.CreatedAt,
	}
}

// waitForExpertTab blocks until the expert-session tab's controller is built
// (boot.Build runs async in a goroutine), up to a timeout. The build signals
// completion by closing tab.readyCh, so this is a pure blocking wait — no poll
// loop. Returns the controller or nil (timeout / build failure).
func (a *App) waitForExpertTab(tabID string, timeout time.Duration) tabSession {
	a.mu.RLock()
	tab := a.tabs[tabID]
	ch := tab.readyCh
	a.mu.RUnlock()
	if ch == nil {
		return nil // tab gone or build never started
	}

	// Block on the build's done signal. On wake, re-read Ctrl under the lock;
	// it's nil if the build failed, in which case we return nil.
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
		return nil
	}
	a.mu.RLock()
	ctrl := a.tabs[tabID].Ctrl
	a.mu.RUnlock()
	return ctrl
}

// priorExpertRuns reads the expert session's history and extracts prior
// collaboration records (for multi-turn context). Returns only the syntheses
// (compact) so the token cost stays bounded — per the "only synthesis in
// context" decision. Capped at the most recent maxPriorRuns records so a
// long-running session with many past collaborations doesn't inject unbounded
// tokens into the task prompt.
const maxPriorRuns = 5

func (a *App) priorExpertRuns(ctrl tabSession) []experts.PriorRun {
	if ctrl == nil {
		return nil
	}
	var out []experts.PriorRun
	for _, m := range ctrl.History() {
		if m.Role != provider.RoleTool || m.Name != experts.ExpertCollabToolName {
			continue
		}
		s := m.Content
		if s == "" {
			continue
		}
		if rec, ok := experts.ParseCollabRecord(s); ok {
			out = append(out, experts.PriorRun{Task: rec.Task, Synthesis: rec.Synthesis})
		}
	}
	// Keep only the most recent N — older collaborations are less relevant and
	// including all of them would grow the prompt unboundedly over time.
	if len(out) > maxPriorRuns {
		out = out[len(out)-maxPriorRuns:]
	}
	return out
}

// runExpertTeamIntoSession runs the orchestrator and persists the full result
// into the team's own expert-session tab (NOT the main chat). The run streams
// live via a.emitExpertsCollab as before; the session append happens at the end.
// priorRuns (the session's prior collaboration syntheses) seed multi-turn
// context so the team sees what it concluded before.
func (a *App) runExpertTeamIntoSession(ctx context.Context, runID, expertTabID, teamID, teamName, task, mode string, rounds int) {
	// The expert tab's controller builds asynchronously after OpenExpertSessionTab.
	// Wait for it before reading history / writing the result.
	ctrl := a.waitForExpertTab(expertTabID, 30*time.Second)
	if ctrl == nil {
		a.markExpertRunFinished(teamID, expertRunError, "专家会话未就绪")
		a.emitExpertsCollab(experts.CollabEvent{TeamID: teamID, Phase: experts.PhaseError, Message: "专家会话未就绪，请重试"})
		return
	}
	// NOTE: we deliberately do NOT persist partial results here. An earlier
	// version wrote a CollabRecord tool-message on every expert's partial
	// callback, which stacked many near-duplicate records into the session
	// history (one per expert × round) plus the final one — producing a
	// cluttered/garbled transcript on re-open. The live stream already shows
	// progress in real time; only the completed collaboration is persisted.
	// Multi-turn context: prior runs' syntheses (compact, bounded).
	prior := a.priorExpertRuns(ctrl)
	res, err := a.expertOrchestrator.RunWithHistory(ctx, teamID, task, mode, rounds, prior)
	if err != nil {
		a.markExpertRunFinished(teamID, expertRunError, err.Error())
		a.emitExpertsCollab(experts.CollabEvent{TeamID: teamID, Phase: experts.PhaseError, Message: err.Error()})
		return
	}
	a.markExpertRunFinished(teamID, expertRunDone, "")
	rec := experts.NewCollabRecord(runID, teamID, teamName, task, mode, res, time.Now().UnixMilli())
	content, err := rec.MarshalContent()
	if err != nil {
		a.emitExpertsCollab(experts.CollabEvent{TeamID: teamID, Phase: experts.PhaseError, Message: "序列化失败: " + err.Error()})
		return
	}
	if perr := ctrl.AppendExpertCollab(content); perr != nil {
		// Persistence failure is non-fatal: the run streamed successfully.
		a.emitExpertsCollab(experts.CollabEvent{TeamID: teamID, Phase: experts.PhaseError, Message: "保存到专家会话失败: " + perr.Error()})
		return
	}
	// Surface the finished collaboration as an event so the ExpertSessionView
	// renders it (the session's own controller sink routes it to the right tab).
	ctrl.EmitExpertCollab(toEventCollab(rec))
}
