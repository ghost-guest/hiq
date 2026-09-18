package main

// remote_session.go — remoteSession implements tabSession for tabs whose
// workspace lives on a remote host: every turn, tool call, approval, and
// checkpoint executes in the host process; this side is the desktop's proxy.
// Cached bits (running/paused/mode/goal/label/context usage) are updated from
// the event stream and re-synced via session/state on (re)connect, because the
// desktop reads them on hot UI paths where a round-trip per read would lag.

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/checkpoint"
	"github.com/zzycxz/hiq/internal/command"
	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/control"
	"github.com/zzycxz/hiq/internal/diff"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/jobs"
	"github.com/zzycxz/hiq/internal/memory"
	"github.com/zzycxz/hiq/internal/plugin"
	"github.com/zzycxz/hiq/internal/present"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/remotehost"
	"github.com/zzycxz/hiq/internal/skill"
)

// remoteSession is one host-side session (one desktop tab).
type remoteSession struct {
	id   string // host-side session id (== tab ID)
	link *remoteHostLink
	ref  RemoteRef
	root string

	sink *tabEventSink // the tab's event sink; events decoded off the wire land here

	mu struct {
		sync.Mutex
		running       bool
		paused        bool
		turn          int
		label         string
		sessionPath   string
		sessionDir    string
		planMode      bool
		approvalMode  string
		goal          string
		goalStatus    string
		contextUsed   int
		contextWindow int
		compactRatio  float64
		lastUsage     *provider.Usage
		mode          string // tab mode composite, replayed on rebuild/reattach
		effort        string
		// Host-initiated round-trips parked until the user answers in the UI.
		permissionWaiters map[string]chan remoteDecision
		askWaiters        map[string]chan []remotehost.AskAnswer
	}
}

func (s *remoteSession) waitersLocked() {
	if s.mu.permissionWaiters == nil {
		s.mu.permissionWaiters = make(map[string]chan remoteDecision)
	}
	if s.mu.askWaiters == nil {
		s.mu.askWaiters = make(map[string]chan []remotehost.AskAnswer)
	}
}

// newRemoteSession opens a session on the host and returns the proxy.
func newRemoteSession(ctx context.Context, link *remoteHostLink, ref RemoteRef, p remotehost.SessionNewParams) (*remoteSession, error) {
	s := &remoteSession{id: p.SessionID, link: link, ref: ref, root: p.Cwd}
	s.mu.Lock()
	s.waitersLocked()
	s.mu.Unlock()
	var res remotehost.SessionNewResult
	if err := link.call(ctx, "session/new", p, &res); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.mu.label = res.Label
	s.mu.sessionPath = res.SessionPath
	s.mu.Unlock()
	link.register(s)
	s.syncState()
	return s, nil
}

// reattach re-opens this session on a fresh link (reconnect): the host pins
// the transcript by path, so the conversation continues where it left off.
func (s *remoteSession) reattach(ctx context.Context, link *remoteHostLink) error {
	path := s.SessionPath()
	s.mu.Lock()
	p := remotehost.SessionNewParams{
		SessionID:   s.id,
		Cwd:         s.root,
		SessionPath: path,
		Mode:        s.mu.mode,
		Goal:        s.mu.goal,
	}
	if s.mu.approvalMode != "" {
		p.ToolApprovalMode = s.mu.approvalMode
	}
	s.mu.Unlock()
	var res remotehost.SessionNewResult
	if err := link.call(ctx, "session/new", p, &res); err != nil {
		return err
	}
	s.link = link
	s.mu.Lock()
	s.mu.label = res.Label
	if res.SessionPath != "" {
		s.mu.sessionPath = res.SessionPath
	}
	s.mu.Unlock()
	link.register(s)
	s.syncState()
	return nil
}

// bindSink points decoded host events at the tab's sink.
func (s *remoteSession) bindSink(sink *tabEventSink) { s.sink = sink }

// consumeEvent feeds one decoded host event into the tab sink and refreshes
// the cached runtime facts.
func (s *remoteSession) consumeEvent(e event.Event) {
	s.mu.Lock()
	switch e.Kind {
	case event.TurnStarted:
		s.mu.running = true
		s.mu.turn++
	case event.TurnDone:
		s.mu.running = false
	case event.Usage:
		if e.Usage != nil {
			u := *e.Usage
			s.mu.lastUsage = &u
			s.mu.contextUsed = u.PromptTokens
		}
	}
	s.mu.Unlock()
	if s.sink != nil {
		s.sink.Emit(e)
	}
}

// syncState pulls the host's session/state into the local caches.
func (s *remoteSession) syncState() {
	var st remotehost.SessionStateResult
	if err := s.call("session/state", remotehost.SessionRef{SessionID: s.id}, &st); err != nil {
		return
	}
	s.mu.Lock()
	s.mu.running = st.Running
	s.mu.paused = st.Paused
	s.mu.label = st.Label
	s.mu.sessionPath = st.SessionPath
	s.mu.sessionDir = st.SessionDir
	s.mu.planMode = st.PlanMode
	s.mu.approvalMode = st.ToolApprovalMode
	s.mu.goal = st.Goal
	s.mu.goalStatus = st.GoalStatus
	s.mu.contextUsed = st.ContextUsed
	s.mu.contextWindow = st.ContextWindow
	s.mu.compactRatio = st.CompactRatio
	s.mu.Unlock()
}

// --- permission / ask round-trips (host → desktop) --------------------------

type remoteDecision struct {
	allow, session, persist bool
}

// awaitPermission blocks the host's outbound request until the user decides in
// the UI (remoteSession.Approve), the link dies, or the generous deadline hits
// (deny — the host denies too, so the turn can't hang forever).
func (s *remoteSession) awaitPermission(ctx context.Context, e event.Event) (any, error) {
	ch := make(chan remoteDecision, 1)
	s.mu.Lock()
	s.waitersLocked()
	s.mu.permissionWaiters[e.Approval.ID] = ch
	s.mu.Unlock()
	res := remotehost.PermissionRequestResult{}
	select {
	case d := <-ch:
		res.Allow, res.Session, res.Persist = d.allow, d.session, d.persist
	case <-ctx.Done():
	case <-time.After(25 * time.Minute):
	}
	return res, nil
}

func (s *remoteSession) awaitAsk(ctx context.Context, e event.Event) (any, error) {
	ch := make(chan []remotehost.AskAnswer, 1)
	s.mu.Lock()
	s.waitersLocked()
	s.mu.askWaiters[e.Ask.ID] = ch
	s.mu.Unlock()
	res := remotehost.AskRequestResult{}
	select {
	case answers := <-ch:
		res.Answers = answers
	case <-ctx.Done():
	}
	return res, nil
}

// --- tabSession: turn driving -----------------------------------------------

func (s *remoteSession) Submit(input string) { s.SubmitDisplay(input, input) }

func (s *remoteSession) SubmitDisplay(display, input string) {
	s.mu.Lock()
	s.mu.running = true
	s.mu.Unlock()
	if err := s.call("session/submit", remotehost.SubmitParams{SessionID: s.id, Input: input, Display: display}, nil); err != nil {
		s.mu.Lock()
		s.mu.running = false
		s.mu.Unlock()
		s.notifyErr("submit", err)
	}
}

func (s *remoteSession) Steer(text string) {
	if err := s.call("session/steer", remotehost.SteerParams{SessionID: s.id, Text: text}, nil); err != nil {
		s.notifyErr("steer", err)
	}
}

func (s *remoteSession) FollowUp(input string) {
	if err := s.call("session/followUp", remotehost.FollowUpParams{SessionID: s.id, Input: input}, nil); err != nil {
		s.notifyErr("follow-up", err)
	}
}

func (s *remoteSession) QueuedMessages() (steer []string, followUp []string) {
	var res remotehost.QueuedResult
	if err := s.call("session/queued", remotehost.SessionRef{SessionID: s.id}, &res); err == nil {
		return res.Steer, res.FollowUp
	}
	return nil, nil
}

func (s *remoteSession) Cancel() {
	s.mu.Lock()
	s.mu.running = false
	s.mu.Unlock()
	if err := s.call("session/cancel", remotehost.SessionRef{SessionID: s.id}, nil); err != nil {
		s.notifyErr("cancel", err)
	}
}

func (s *remoteSession) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mu.running
}

func (s *remoteSession) Turn() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mu.turn
}

func (s *remoteSession) WaitTurn(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !s.Running() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *remoteSession) Pause() {
	if err := s.call("session/pause", remotehost.SessionRef{SessionID: s.id}, nil); err == nil {
		s.mu.Lock()
		s.mu.paused = true
		s.mu.Unlock()
	}
}

func (s *remoteSession) ResumeTurn() {
	if err := s.call("session/resumeTurn", remotehost.SessionRef{SessionID: s.id}, nil); err == nil {
		s.mu.Lock()
		s.mu.paused = false
		s.mu.Unlock()
	}
}

func (s *remoteSession) Paused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mu.paused
}

func (s *remoteSession) RunShell(command string) {
	if err := s.call("session/runShell", remotehost.RunShellParams{SessionID: s.id, Command: command}, nil); err != nil {
		s.notifyErr("shell", err)
	}
}

// Run drives one turn synchronously (scheduler path): submit, then wait for
// the turn to finish.
func (s *remoteSession) Run(ctx context.Context, input string) error {
	s.Submit(input)
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !s.Running() {
			return nil
		}
		select {
		case <-ctx.Done():
			s.Cancel()
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// --- tabSession: approvals / asks -------------------------------------------

func (s *remoteSession) Approve(id string, allow, session, persist bool) {
	s.mu.Lock()
	ch := s.mu.permissionWaiters[id]
	delete(s.mu.permissionWaiters, id)
	s.mu.Unlock()
	if ch != nil {
		ch <- remoteDecision{allow: allow, session: session, persist: persist}
		return
	}
	// No parked round-trip (replayed prompt / reconnect edge): tell the host
	// directly; its controller resolves or drops the unknown id.
	_ = s.call("session/approve", remotehost.ApproveParams{SessionID: s.id, ID: id, Allow: allow, Session: session, Persist: persist}, nil)
}

func (s *remoteSession) AnswerQuestion(id string, answers []event.AskAnswer) {
	wire := make([]remotehost.AskAnswer, len(answers))
	for i, a := range answers {
		wire[i] = remotehost.AskAnswer{QuestionID: a.QuestionID, Selected: a.Selected}
	}
	s.mu.Lock()
	ch := s.mu.askWaiters[id]
	delete(s.mu.askWaiters, id)
	s.mu.Unlock()
	if ch != nil {
		ch <- wire
		return
	}
	_ = s.call("session/answer", remotehost.AnswerParams{SessionID: s.id, ID: id, Answers: wire}, nil)
}

func (s *remoteSession) ReplayPendingPrompts() {
	_ = s.call("session/replayPendingPrompts", remotehost.SessionRef{SessionID: s.id}, nil)
}

// --- tabSession: mode / goal / scope -----------------------------------------

func (s *remoteSession) SetMode(plan, autoApproveTools bool) {
	mode := "normal"
	switch {
	case plan && autoApproveTools:
		mode = "plan-yolo"
	case plan:
		mode = "plan"
	case autoApproveTools:
		mode = "yolo"
	}
	s.mu.Lock()
	s.mu.mode = mode
	s.mu.planMode = plan
	if autoApproveTools {
		s.mu.approvalMode = control.ToolApprovalYolo
	}
	s.mu.Unlock()
	_ = s.call("session/setMode", remotehost.SetModeParams{SessionID: s.id, Mode: mode}, nil)
}

func (s *remoteSession) SetPlanMode(v bool) {
	s.mu.Lock()
	s.mu.planMode = v
	switch s.mu.mode {
	case "normal":
		if v {
			s.mu.mode = "plan"
		}
	case "plan":
		if !v {
			s.mu.mode = "normal"
		}
	}
	mode := s.mu.mode
	s.mu.Unlock()
	_ = s.call("session/setMode", remotehost.SetModeParams{SessionID: s.id, Mode: mode}, nil)
}

func (s *remoteSession) PlanMode() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mu.planMode
}

func (s *remoteSession) SetToolApprovalMode(mode string) {
	s.mu.Lock()
	s.mu.approvalMode = mode
	s.mu.Unlock()
	_ = s.call("session/setToolApprovalMode", remotehost.SetToolApprovalModeParams{SessionID: s.id, Mode: mode}, nil)
}

func (s *remoteSession) ToolApprovalMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mu.approvalMode
}

func (s *remoteSession) SetAutoApproveTools(on bool) {
	if on {
		s.SetToolApprovalMode(control.ToolApprovalYolo)
	}
}

func (s *remoteSession) AutoApproveTools() bool {
	return s.ToolApprovalMode() == control.ToolApprovalYolo
}

func (s *remoteSession) SetGoal(goal string) {
	s.mu.Lock()
	s.mu.goal = goal
	s.mu.Unlock()
	_ = s.call("session/setGoal", remotehost.SetGoalParams{SessionID: s.id, Goal: goal}, nil)
}

func (s *remoteSession) Goal() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mu.goal
}

func (s *remoteSession) GoalStatus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mu.goalStatus
}

func (s *remoteSession) SetRAGScope(scope string) {
	_ = s.call("session/setRagScope", remotehost.SetRagScopeParams{SessionID: s.id, Scope: scope}, nil)
}

// --- tabSession: checkpoints / branches --------------------------------------

func (s *remoteSession) Checkpoints() []checkpoint.Meta {
	var res remotehost.CheckpointsResult
	if err := s.call("session/checkpoints", remotehost.SessionRef{SessionID: s.id}, &res); err != nil {
		return nil
	}
	var metas []checkpoint.Meta
	_ = json.Unmarshal(res.Checkpoints, &metas)
	return metas
}

func (s *remoteSession) CheckpointDiff(turn int) []diff.Change {
	var res remotehost.CheckpointDiffResult
	if err := s.call("session/checkpointDiff", remotehost.CheckpointDiffParams{SessionID: s.id, Turn: turn}, &res); err != nil {
		return nil
	}
	var changes []diff.Change
	_ = json.Unmarshal(res.Changes, &changes)
	return changes
}

func (s *remoteSession) CheckpointHasBoundary(turn int) bool {
	var res remotehost.CheckpointHasBoundaryResult
	if err := s.call("session/checkpointHasBoundary", remotehost.CheckpointDiffParams{SessionID: s.id, Turn: turn}, &res); err != nil {
		return false
	}
	return res.Has
}

// RewindPreview classifies the suffix of a code rewind over the remote host's
// checkpoint store (same buckets as the local controller).
func (s *remoteSession) RewindPreview(turn int) []control.RewindFileClass {
	var res remotehost.RewindPreviewResult
	if err := s.call("session/rewindPreview", remotehost.CheckpointDiffParams{SessionID: s.id, Turn: turn}, &res); err != nil {
		return nil
	}
	var classes []control.RewindFileClass
	_ = json.Unmarshal(res.Classes, &classes)
	return classes
}

func (s *remoteSession) Rewind(turn int, scope control.RewindScope) error {
	var scopeStr string
	switch scope {
	case control.RewindCode:
		scopeStr = "code"
	case control.RewindConversation:
		scopeStr = "conversation"
	default:
		scopeStr = "both"
	}
	return s.call("session/rewind", remotehost.RewindParams{SessionID: s.id, Turn: turn, Scope: scopeStr}, nil)
}

func (s *remoteSession) ForkSession(turn int, name string) (string, error) {
	var res remotehost.ForkResult
	if err := s.call("session/fork", remotehost.ForkParams{SessionID: s.id, Turn: turn, Name: name}, &res); err != nil {
		return "", err
	}
	if res.SessionPath != "" {
		s.mu.Lock()
		s.mu.sessionPath = res.SessionPath
		s.mu.Unlock()
	}
	return res.SessionPath, nil
}

func (s *remoteSession) Branches() ([]agent.BranchInfo, error) {
	var res remotehost.BranchesResult
	if err := s.call("session/branches", remotehost.SessionRef{SessionID: s.id}, &res); err != nil {
		return nil, err
	}
	var branches []agent.BranchInfo
	if err := json.Unmarshal(res.Branches, &branches); err != nil {
		return nil, err
	}
	return branches, nil
}

func (s *remoteSession) SwitchBranch(ref string) (agent.BranchInfo, error) {
	var res remotehost.ForkResult
	if err := s.call("session/switchBranch", remotehost.SwitchBranchParams{SessionID: s.id, Ref: ref}, &res); err != nil {
		return agent.BranchInfo{}, err
	}
	if res.SessionPath != "" {
		s.mu.Lock()
		s.mu.sessionPath = res.SessionPath
		s.mu.Unlock()
	}
	s.syncState()
	return agent.BranchInfo{Path: res.SessionPath}, nil
}

func (s *remoteSession) SummarizeFrom(ctx context.Context, turn int) error {
	return s.link.call(ctx, "session/summarize", remotehost.SummarizeParams{SessionID: s.id, Turn: turn}, nil)
}

func (s *remoteSession) SummarizeUpTo(ctx context.Context, turn int) error {
	return s.link.call(ctx, "session/summarize", remotehost.SummarizeParams{SessionID: s.id, Turn: turn, UpTo: true}, nil)
}

// --- tabSession: session state / persistence ---------------------------------

func (s *remoteSession) Resume(_ *agent.Session, path string) {
	_ = s.call("session/setSessionPath", remotehost.SetSessionPathParams{SessionID: s.id, SessionPath: path}, nil)
	s.syncState()
}

func (s *remoteSession) SetSessionPath(p string) {
	s.mu.Lock()
	s.mu.sessionPath = p
	s.mu.Unlock()
	_ = s.call("session/setSessionPath", remotehost.SetSessionPathParams{SessionID: s.id, SessionPath: p}, nil)
}

func (s *remoteSession) SessionPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mu.sessionPath
}

func (s *remoteSession) SessionDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mu.sessionDir
}

func (s *remoteSession) History() []provider.Message {
	var res remotehost.HistoryResult
	if err := s.call("session/history", remotehost.SessionRef{SessionID: s.id}, &res); err != nil {
		return nil
	}
	var msgs []provider.Message
	_ = json.Unmarshal(res.Messages, &msgs)
	return msgs
}

func (s *remoteSession) PresentRecords() (records []present.Record, rewriteVersion int, ok bool) {
	var res remotehost.PresentResult
	if err := s.call("session/present", remotehost.SessionRef{SessionID: s.id}, &res); err != nil || !res.OK {
		return nil, 0, false
	}
	_ = json.Unmarshal(res.Records, &records)
	return records, res.RewriteVersion, true
}

func (s *remoteSession) SearchSessionText(query string) []agent.SearchHit {
	var res remotehost.FsSearchResult
	if err := s.call("session/searchText", remotehost.FsSearchParams{SessionID: s.id, Query: query}, &res); err != nil {
		return nil
	}
	var hits []agent.SearchHit
	_ = json.Unmarshal(res.Results, &hits)
	return hits
}

func (s *remoteSession) Snapshot() error {
	return s.call("session/snapshot", remotehost.SessionRef{SessionID: s.id}, nil)
}

func (s *remoteSession) NewSession() error {
	var res remotehost.NewSessionResult
	if err := s.call("session/newSession", remotehost.NewSessionParams{SessionID: s.id}, &res); err != nil {
		return err
	}
	if res.SessionPath != "" {
		s.mu.Lock()
		s.mu.sessionPath = res.SessionPath
		s.mu.Unlock()
	}
	return nil
}

func (s *remoteSession) ClearSession() error {
	return s.call("session/clearSession", remotehost.SessionRef{SessionID: s.id}, nil)
}

func (s *remoteSession) Compact(ctx context.Context, instructions string) error {
	return s.link.call(ctx, "session/compact", remotehost.CompactParams{SessionID: s.id, Instructions: instructions}, nil)
}

// --- tabSession: read-only surface -------------------------------------------

func (s *remoteSession) Label() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mu.label
}

func (s *remoteSession) ContextSnapshot() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mu.contextUsed, s.mu.contextWindow
}

func (s *remoteSession) CompactRatio() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mu.compactRatio
}

func (s *remoteSession) LastUsage() *provider.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mu.lastUsage == nil {
		return nil
	}
	u := *s.mu.lastUsage
	return &u
}

func (s *remoteSession) Jobs() []jobs.View { return nil }

func (s *remoteSession) Commands() []command.Command { return nil }

func (s *remoteSession) Skills() []skill.Skill         { return nil }
func (s *remoteSession) AllSkills() []skill.Skill      { return nil }
func (s *remoteSession) DisabledSkills() []skill.Skill { return nil }

func (s *remoteSession) ConfiguredMCPNames() []string   { return nil }
func (s *remoteSession) DisconnectedMCPNames() []string { return nil }
func (s *remoteSession) Host() *plugin.Host             { return nil }

func (s *remoteSession) SkillEnabled(name string) bool                   { return true }
func (s *remoteSession) SetSkillEnabled(name string, enabled bool) error { return nil }

// --- tabSession: desktop-local features without a host side in P1 -------------

func (s *remoteSession) Memory() *memory.Set { return nil }
func (s *remoteSession) QuickAdd(scope memory.Scope, note string) (string, error) {
	return "", errRemoteUnsupported
}
func (s *remoteSession) SaveDoc(path, body string) (string, error) {
	return "", errRemoteUnsupported
}
func (s *remoteSession) ForgetMemory(name string) error { return errRemoteUnsupported }
func (s *remoteSession) QueueMemory(note string)        {}
func (s *remoteSession) Profile() control.ProfileView   { return control.ProfileView{} }
func (s *remoteSession) ProfilePresets() control.ProfilePresetsView {
	return control.ProfilePresetsView{}
}
func (s *remoteSession) SaveProfilePresets(f memory.PresetFile) (string, error) {
	return "", errRemoteUnsupported
}

func (s *remoteSession) AppendExpertCollab(content string) error { return errRemoteUnsupported }
func (s *remoteSession) EmitExpertCollab(collab event.Collab)    {}
func (s *remoteSession) DeleteExpertCollab(ordinal int) error    { return errRemoteUnsupported }

func (s *remoteSession) TriggerDream(ctx context.Context) (agent.DreamRun, bool) {
	return agent.DreamRun{}, false
}
func (s *remoteSession) TriggerDistill(ctx context.Context) (agent.DreamRun, bool) {
	return agent.DreamRun{}, false
}
func (s *remoteSession) LastDreamRun(kind agent.DreamKind) (agent.DreamRun, bool) {
	return agent.DreamRun{}, false
}

func (s *remoteSession) AddMCPServer(e config.PluginEntry) (int, error) {
	return 0, errRemoteUnsupported
}
func (s *remoteSession) ConnectMCPServer(e config.PluginEntry) (int, error) {
	return 0, errRemoteUnsupported
}
func (s *remoteSession) DisconnectMCPServer(name string) bool { return false }
func (s *remoteSession) RemoveMCPServer(name string) (bool, error) {
	return false, errRemoteUnsupported
}
func (s *remoteSession) ConnectCodegraphMCPServer(cfg *config.Config) (int, error) {
	return 0, errRemoteUnsupported
}

// --- tabSession: lifecycle ----------------------------------------------------

func (s *remoteSession) Close() {
	_ = s.call("session/close", remotehost.SessionRef{SessionID: s.id}, nil)
	s.link.unregister(s.id)
}
