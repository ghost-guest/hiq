package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/zzycxz/fairpeer/internal/agent"
	"github.com/zzycxz/fairpeer/internal/boot"
	"github.com/zzycxz/fairpeer/internal/config"
	"github.com/zzycxz/fairpeer/internal/control"
	"github.com/zzycxz/fairpeer/internal/event"
	"github.com/zzycxz/fairpeer/internal/provider"
)

// --- WorkspaceTab -----------------------------------------------------------

// WorkspaceTab is one open conversation tab in the desktop. Each tab owns an
// independent controller (its own agent, session, tool registry, plugin host,
// memory, permissions) scoped to a workspace root, so multiple projects and
// topics can be active concurrently without interfering.
type WorkspaceTab struct {
	ID            string     // stable random id
	Scope         string     // "project" | "global"
	WorkspaceRoot string     // project root dir (empty for global); remote tabs carry the remote-side path
	TopicID       string     // topic within the project
	TopicTitle    string     // display title
	SessionPath   string     // exact .jsonl file this tab continues (remote-side path for remote tabs)
	Ctrl          tabSession // nil while booting / on error; *control.Controller locally, remoteSession for remote tabs
	// Remote, when non-nil, marks this tab's workspace as living on a remote
	// host (P1: WSL). The controller runs in the host process over RPC;
	// WorkspaceRoot and SessionPath above are remote-side strings.
	Remote     *RemoteRef
	Label      string        // model label (for the tab badge)
	Ready      bool          // true once boot.Build (or the remote session attach) completes
	StartupErr string        // build error, surfaced to the frontend
	sink       *tabEventSink // routes events with this tab's ID
	// readyCh is closed when the current build attempt finishes (success or
	// failure). It replaces the 100ms poll loop that waitForExpertTab used to
	// spin while waiting for boot.Build: callers now block on <-readyCh and wake
	// the instant the build returns. reset at the start of each build so a
	// rebuild (model switch) produces a fresh signal. Guarded by readyOnce +
	// the App mutex (reset happens under a.mu in buildTabController).
	readyCh   chan struct{}
	readyOnce sync.Once

	ActivityStatus string // transient project-tree status for the in-flight turn

	// Per-turn autosave per tab.
	saveMu    sync.Mutex
	saving    bool
	saveAgain bool

	// readTelemetry tracks files read during this tab's session.
	readTelemetry  []readFileRecord
	usageTelemetry sessionUsageStats
	telemMu        sync.Mutex

	// Per-turn lifecycle facts (upgrade spec 4-4).
	turnFacts  []turnFact
	turnActive activeTurn

	model            string // active model ref (for meta)
	effort           *string
	mode             string // "normal" | "plan" | "yolo" | "plan-yolo"; yolo/full access is runtime-only
	goal             string
	toolApprovalMode string
	// ragScope is the knowledge-base collection the user picked in the Composer
	// "知识库" dropdown for auto-injection; "" = "不使用" (default, no injection).
	// Persisted per tab so a reopened session keeps its RAG selection. Applied
	// to the controller via applyTabRagScopeToController on (re)build.
	ragScope string
	// profile is the active product mode: "dev" (default, coding) or "cowork"
	// (office). Empty is treated as "dev" everywhere it is read. Persisted so a
	// restarted tab lands back in the same mode. A profile switch rebuilds the
	// controller via the SetModelForTab flow (see app.SwitchProfileForTab).
	profile     string
	disabledMCP map[string]ServerView
	mcpOrder    []string
	// Expert session tracking.
	IsExpertSession bool
	ExpertTeamID    string
	ExpertTeamName  string
}

const (
	topicStatusThinking            = "thinking"
	topicStatusStreaming           = "streaming"
	topicStatusWaitingConfirmation = "waiting_confirmation"
	topicStatusPaused              = "paused"
	topicStatusError               = "error"
)

type readFileRecord struct {
	Path      string `json:"path"`
	Turn      int    `json:"turn"`
	Time      int64  `json:"time"`
	Offset    int    `json:"offset,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type sessionUsageStats struct {
	Cost             float64 `json:"cost"`
	Currency         string  `json:"currency,omitempty"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	TotalTokens      int     `json:"totalTokens"`
	ReasoningTokens  int     `json:"reasoningTokens"`
	CacheHitTokens   int     `json:"cacheHitTokens"`
	CacheMissTokens  int     `json:"cacheMissTokens"`
	CacheWriteTokens int     `json:"cacheWriteTokens"`
	RequestCount     int     `json:"requestCount"`
	ElapsedMs        int64   `json:"elapsedMs"`

	activeTurnStartedAt int64
}

type tabTelemetrySnapshot struct {
	Version   int               `json:"version"`
	ReadFiles []readFileRecord  `json:"readFiles"`
	Usage     sessionUsageStats `json:"usage"`
}

// turnFact is one completed turn's lifecycle record (upgrade spec 4-4):
// duration, retry/recovery counts, tool volume, token split, and failure.
// Kept as a small ring (last turnFactsCap) per tab for the diagnostics view.
type turnFact struct {
	Seq              int    `json:"seq"`
	DurationMs       int64  `json:"durationMs"`
	Retries          int    `json:"retries"`
	ToolCalls        int    `json:"toolCalls"`
	ToolErrors       int    `json:"toolErrors"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	CacheHitTokens   int    `json:"cacheHitTokens"`
	CacheMissTokens  int    `json:"cacheMissTokens"`
	Err              string `json:"err,omitempty"`
}

// activeTurn accumulates the in-flight turn's facts from the event stream.
type activeTurn struct {
	seq        int
	startedAt  int64
	retries    int
	toolCalls  int
	toolErrors int
	prompt     int
	completion int
	hit        int
	miss       int
}

const turnFactsCap = 50

func cloneStringPtr(v *string) *string {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

func cloneServerViewMap(in map[string]ServerView) map[string]ServerView {
	out := make(map[string]ServerView, len(in))
	for name, view := range in {
		out[name] = view
	}
	return out
}

// normalizeProfileName returns the canonical profile identifier for wire output.
// Empty (unprofiled) becomes "dev" so the frontend always sees a concrete mode
// name. Non-empty is lowercased to match config.ProfileNameKey resolution.
func normalizeProfileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return config.ProfileDev
	}
	return strings.ToLower(name)
}

func (t *WorkspaceTab) currentSessionPath() string {
	if t == nil {
		return ""
	}
	if t.Ctrl != nil {
		if path := strings.TrimSpace(t.Ctrl.SessionPath()); path != "" {
			return path
		}
	}
	return strings.TrimSpace(t.SessionPath)
}

func (t *WorkspaceTab) recordReadFile(rec readFileRecord) {
	t.telemMu.Lock()
	t.readTelemetry = append(t.readTelemetry, rec)
	t.telemMu.Unlock()
}

func (t *WorkspaceTab) recordTurnStarted(now int64) {
	t.telemMu.Lock()
	if t.usageTelemetry.activeTurnStartedAt == 0 {
		t.usageTelemetry.activeTurnStartedAt = now
	}
	t.turnActive = activeTurn{seq: len(t.turnFacts) + 1, startedAt: now}
	t.telemMu.Unlock()
}

func (t *WorkspaceTab) recordTurnDone(now int64, err string) {
	t.telemMu.Lock()
	if started := t.usageTelemetry.activeTurnStartedAt; started > 0 && now >= started {
		t.usageTelemetry.ElapsedMs += now - started
		t.usageTelemetry.activeTurnStartedAt = 0
	}
	a := t.turnActive
	if a.startedAt > 0 {
		fact := turnFact{
			Seq: a.seq, DurationMs: now - a.startedAt, Retries: a.retries,
			ToolCalls: a.toolCalls, ToolErrors: a.toolErrors,
			PromptTokens: a.prompt, CompletionTokens: a.completion,
			CacheHitTokens: a.hit, CacheMissTokens: a.miss, Err: err,
		}
		t.turnFacts = append(t.turnFacts, fact)
		if len(t.turnFacts) > turnFactsCap {
			t.turnFacts = t.turnFacts[len(t.turnFacts)-turnFactsCap:]
		}
	}
	t.turnActive = activeTurn{}
	t.telemMu.Unlock()
}

// recordTurnEvent folds one lifecycle event into the active turn's facts.
func (t *WorkspaceTab) recordTurnEvent(e event.Event) {
	t.telemMu.Lock()
	defer t.telemMu.Unlock()
	switch e.Kind {
	case event.Retrying:
		t.turnActive.retries++
	case event.ToolDispatch:
		t.turnActive.toolCalls++
	case event.ToolResult:
		if e.Tool.Err != "" {
			t.turnActive.toolErrors++
		}
	case event.Usage:
		if u := e.Usage; u != nil {
			t.turnActive.prompt += u.PromptTokens
			t.turnActive.completion += u.CompletionTokens
			t.turnActive.hit += u.CacheHitTokens
			t.turnActive.miss += u.CacheMissTokens
		}
	}
}

// TurnFacts copies the recorded per-turn facts, newest last.
func (t *WorkspaceTab) TurnFacts() []turnFact {
	t.telemMu.Lock()
	defer t.telemMu.Unlock()
	out := make([]turnFact, len(t.turnFacts))
	copy(out, t.turnFacts)
	return out
}

func (t *WorkspaceTab) recordUsage(e event.Event) {
	if e.Usage == nil {
		return
	}
	u := e.Usage
	if e.Pricing != nil {
		t.usageTelemetry.Cost += e.Pricing.Cost(u)
		t.usageTelemetry.Currency = e.Pricing.Symbol()
	}
	t.telemMu.Lock()
	t.usageTelemetry.PromptTokens += u.PromptTokens
	t.usageTelemetry.CompletionTokens += u.CompletionTokens
	t.usageTelemetry.TotalTokens += u.TotalTokens
	t.usageTelemetry.ReasoningTokens += u.ReasoningTokens
	if e.SessionHit+e.SessionMiss > 0 {
		t.usageTelemetry.CacheHitTokens = e.SessionHit
		t.usageTelemetry.CacheMissTokens = e.SessionMiss
	} else {
		t.usageTelemetry.CacheHitTokens += u.CacheHitTokens
		t.usageTelemetry.CacheMissTokens += u.CacheMissTokens
		t.usageTelemetry.CacheWriteTokens += u.CacheWriteTokens
	}
	t.usageTelemetry.RequestCount++
	t.telemMu.Unlock()
}

func (t *WorkspaceTab) telemetrySnapshot() tabTelemetrySnapshot {
	t.telemMu.Lock()
	defer t.telemMu.Unlock()
	records := make([]readFileRecord, len(t.readTelemetry))
	copy(records, t.readTelemetry)
	usage := t.usageTelemetry
	if started := usage.activeTurnStartedAt; started > 0 {
		now := time.Now().UnixMilli()
		if now >= started {
			usage.ElapsedMs += now - started
		}
	}
	usage.activeTurnStartedAt = 0
	return tabTelemetrySnapshot{Version: 2, ReadFiles: records, Usage: usage}
}

// tabEventSink wraps a parent event.Sink and prepends a tabId to every wire
// event so the frontend can route it to the correct tab's reducer.
type tabEventSink struct {
	tabID string
	app   *App
	ctx   context.Context
}

func (s *tabEventSink) Emit(e event.Event) {
	if s.app != nil {
		switch e.Kind {
		case event.TurnStarted:
			s.recordTurnStarted()
		case event.Usage:
			s.recordUsageTelemetry(e)
			s.recordTurnFactsEvent(e)
		case event.Retrying, event.ToolDispatch, event.ToolResult:
			s.recordTurnFactsEvent(e)
		case event.TurnDone:
			s.recordTurnDoneEvent(e)
		}
		if m := s.app.metrics.Load(); m != nil {
			m.observe(e)
			if e.Kind == event.TurnDone {
				m.persist()
			}
		}
	}
	if s.ctx != nil {
		runtime.EventsEmit(s.ctx, eventChannel, toWireTab(e, s.tabID))
	}
	if s.app != nil {
		if status, update := topicActivityStatusFromEvent(e); update && s.app.setTabActivityStatus(s.tabID, status) {
			s.app.emitProjectTreeChanged()
		}
	}
	// Record read_file successes in the tab's telemetry.
	if e.Kind == event.ToolResult && e.Tool.Name == "read_file" && e.Tool.Err == "" {
		s.recordReadTelemetry(e)
	}
	// Persist after each turn so a force-kill loses at most the in-flight prompt.
	if e.Kind == event.TurnDone && s.app != nil {
		s.app.scheduleTabSnapshot(s.tabID)
	}
	// linkpeer mobile bridge: forward this event (as wireEvent JSON) to any
	// mobile client subscribed to this tab. Single injection point — the
	// mobilebridge package owns all P2P/encryption downstream of here.
	if s.app != nil {
		if mb := s.app.mobilebridge.Load(); mb != nil {
			if wireJSON, err := json.Marshal(toWireTab(e, s.tabID)); err == nil {
				mb.ForwardEvent(s.tabID, wireJSON)
			}
		}
	}
	// bot desktop bridge: forward approval/ask/turn-done events to subscribed IM
	// chats. Observe only does in-memory bookkeeping + enqueue, so it never
	// blocks this controller event goroutine.
	if s.app != nil {
		if bb := s.app.botBridge.Load(); bb != nil {
			switch e.Kind {
			case event.ApprovalRequest, event.AskRequest, event.TurnDone:
				bb.Observe(s.tabID, e)
			}
		}
	}
}

func topicActivityStatusFromEvent(e event.Event) (string, bool) {
	switch e.Kind {
	case event.TurnStarted, event.Reasoning, event.ToolDispatch, event.ToolProgress, event.ToolResult, event.CompactionStarted, event.CompactionDone, event.Retrying:
		return topicStatusThinking, true
	case event.Text, event.Message:
		return topicStatusStreaming, true
	case event.ApprovalRequest, event.AskRequest:
		return topicStatusWaitingConfirmation, true
	case event.TurnDone:
		if e.Err != nil {
			return topicStatusError, true
		}
		return "", true
	default:
		return "", false
	}
}

// emitReady signals the frontend that a tab's controller finished building.
// The payload carries the tab's ID so the frontend reloads THAT tab — without
// it, a background tab finishing its build triggers a reload of whatever tab
// happens to be active (misrouted reload of a possibly streaming tab, and
// N redundant reloads of the active tab when startup restores N tabs).
func (a *App) emitReady(ctx context.Context, tabID string) {
	a.mu.RLock()
	hook := a.readyHook
	a.mu.RUnlock()
	if hook != nil {
		hook()
		return
	}
	if ctx != nil {
		runtime.EventsEmit(ctx, "agent:ready", map[string]any{"tabId": tabID})
	}
}

func (s *tabEventSink) recordReadTelemetry(e event.Event) {
	if s.app == nil {
		return
	}
	s.app.mu.RLock()
	tab, ok := s.app.tabs[s.tabID]
	var ctrl tabSession
	if ok && tab != nil {
		ctrl = tab.Ctrl
	}
	s.app.mu.RUnlock()
	if !ok || tab == nil {
		return
	}
	turn := 0
	if ctrl != nil {
		turn = ctrl.Turn()
	}

	// Parse read_file args: {"path": "...", "offset": N, "limit": N}
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	path := e.Tool.Args
	offset := 0
	limit := 0
	if err := json.Unmarshal([]byte(e.Tool.Args), &args); err == nil && args.Path != "" {
		path = args.Path
		offset = args.Offset
		limit = args.Limit
	}

	truncated := e.Tool.Truncated || strings.Contains(e.Tool.Output, "truncated") ||
		strings.Contains(e.Tool.Output, "File truncated")

	tab.recordReadFile(readFileRecord{
		Path:      path,
		Turn:      turn,
		Time:      time.Now().UnixMilli(),
		Offset:    offset,
		Limit:     limit,
		Truncated: truncated,
	})
	if ctrl == nil {
		return
	}
	if sp := ctrl.SessionPath(); sp != "" {
		_ = saveTelemetry(sp+".telemetry.json", tab.telemetrySnapshot())
	}
}

func (s *tabEventSink) recordTurnStarted() {
	tab, sp := s.telemetryTab()
	if tab == nil {
		return
	}
	tab.recordTurnStarted(time.Now().UnixMilli())
	if sp != "" {
		_ = saveTelemetry(sp+".telemetry.json", tab.telemetrySnapshot())
	}
}

// recordTurnDoneEvent finalizes the turn on both accumulators: session
// elapsed-time telemetry and the per-turn facts ring (upgrade spec 4-4).
func (s *tabEventSink) recordTurnDoneEvent(e event.Event) {
	tab, sp := s.telemetryTab()
	if tab == nil {
		return
	}
	errText := ""
	if e.Err != nil {
		errText = e.Err.Error()
	}
	tab.recordTurnDone(time.Now().UnixMilli(), errText)
	if sp != "" {
		_ = saveTelemetry(sp+".telemetry.json", tab.telemetrySnapshot())
	}
}

func (s *tabEventSink) recordTurnFactsEvent(e event.Event) {
	if tab, _ := s.telemetryTab(); tab != nil {
		tab.recordTurnEvent(e)
	}
}

func (s *tabEventSink) recordUsageTelemetry(e event.Event) {
	tab, sp := s.telemetryTab()
	if tab == nil {
		return
	}
	tab.recordUsage(e)
	if sp != "" {
		_ = saveTelemetry(sp+".telemetry.json", tab.telemetrySnapshot())
	}
}

func (s *tabEventSink) telemetryTab() (*WorkspaceTab, string) {
	if s.app == nil {
		return nil, ""
	}
	s.app.mu.RLock()
	tab, ok := s.app.tabs[s.tabID]
	var ctrl tabSession
	if ok && tab != nil {
		ctrl = tab.Ctrl
	}
	s.app.mu.RUnlock()
	if !ok || tab == nil {
		return nil, ""
	}
	if ctrl == nil {
		return tab, ""
	}
	sp := ctrl.SessionPath()
	if sp == "" {
		return tab, ""
	}
	return tab, sp
}

// --- wire event with tab ----------------------------------------------------

func toWireTab(e event.Event, tabID string) wireEventTab {
	w := toWire(e)
	return wireEventTab{
		wireEvent:         w,
		TabID:             tabID,
		SessionHitTokens:  e.SessionHit,
		SessionMissTokens: e.SessionMiss,
	}
}

// wireEventTab extends wireEvent with tab routing info. The frontend reducer
// uses tabId to dispatch to the correct per-tab state.
type wireEventTab struct {
	wireEvent
	TabID string `json:"tabId"`
	// Session-cumulative tokens per tab.
	SessionHitTokens  int `json:"sessionHitTokens,omitempty"`
	SessionMissTokens int `json:"sessionMissTokens,omitempty"`
}

// --- Tab management on App --------------------------------------------------

// TabMeta is the frontend-facing shape of one tab.
type TabMeta struct {
	ID                string `json:"id"`
	Scope             string `json:"scope"`
	WorkspaceRoot     string `json:"workspaceRoot"`
	WorkspaceName     string `json:"workspaceName"`
	TopicID           string `json:"topicId"`
	TopicTitle        string `json:"topicTitle"`
	ProjectColor      string `json:"projectColor,omitempty"`
	Label             string `json:"label"`
	Ready             bool   `json:"ready"`
	Running           bool   `json:"running"`
	Mode              string `json:"mode"`
	CollaborationMode string `json:"collaborationMode"`
	ToolApprovalMode  string `json:"toolApprovalMode"`
	RagScope          string `json:"ragScope"`
	Goal              string `json:"goal,omitempty"`
	GoalStatus        string `json:"goalStatus,omitempty"`
	StartupErr        string `json:"startupErr,omitempty"`
	Active            bool   `json:"active"`
	Cwd               string `json:"cwd"`
	// Profile is the tab's product mode ("dev" | "cowork"); empty = dev. Drives
	// the frontend layout selection. Read alongside the per-tab "profile:changed"
	// event so a switch updates the active tab immediately.
	Profile string `json:"profile,omitempty"`
	// Remote describes the remote connection a tab's workspace lives on
	// (nil for local tabs): kind + target (WSL distro / container) + label.
	// RemoteState is the connection health the frontend badges:
	// "connecting" | "connected" | "offline".
	Remote      *RemoteRef `json:"remote,omitempty"`
	RemoteState string     `json:"remoteState,omitempty"`
	// IsExpertSession marks a tab hosting an expert-team collaboration run
	// (Scope=="expert"). The frontend uses this to render the tab differently
	// (🤝 icon, team name as title) and to route expert_collab events.
	IsExpertSession bool `json:"isExpertSession,omitempty"`
	// ExpertTeamID is the team identifier for an expert-session tab; empty for
	// normal tabs. The frontend uses it to dedup expert tabs per team and to
	// drive the project-tree's expert_topic node (clicking it opens this tab).
	ExpertTeamID string `json:"expertTeamId,omitempty"`
	// ExpertTeamName is the display name of the team for an expert-session tab.
	// The frontend shows it as the tab's title instead of the topic title.
	ExpertTeamName string `json:"expertTeamName,omitempty"`
}

func (a *App) tabMeta(tab *WorkspaceTab, active bool) TabMeta {
	m := TabMeta{
		ID:                tab.ID,
		Scope:             tab.Scope,
		WorkspaceRoot:     tab.WorkspaceRoot,
		WorkspaceName:     workspaceName(tab.WorkspaceRoot),
		TopicID:           tab.TopicID,
		TopicTitle:        tab.TopicTitle,
		Label:             tab.Label,
		Ready:             tab.Ready,
		Mode:              currentTabMode(tab),
		CollaborationMode: currentTabCollaborationMode(tab),
		ToolApprovalMode:  currentTabToolApprovalMode(tab),
		RagScope:          tab.ragScope,
		Goal:              currentTabGoal(tab),
		GoalStatus:        currentTabGoalStatus(tab),
		StartupErr:        tab.StartupErr,
		Active:            active,
		Remote:            tab.Remote,
		RemoteState:       remoteStateForTab(a, tab),
		Cwd:               tab.WorkspaceRoot,
		Profile:           normalizeProfileName(tab.profile),
	}
	switch tab.Scope {
	case "global":
		m.ProjectColor = globalProjectColor(a.activeProfileKeyLocked())
		m.WorkspaceName = globalProjectTitle(a.activeProfileKeyLocked())
	case "project":
		m.ProjectColor = projectColor(tab.WorkspaceRoot, a.activeProfileKeyLocked())
	}
	if tab.Ctrl != nil {
		m.Running = tab.Ctrl.Running()
	}
	// Surface expert-session fields so the frontend can render the tab as an
	// expert-team collaboration view (🤝 icon, team name title, project-tree
	// expert_topic node). Empty for normal tabs (omitempty drops them).
	m.IsExpertSession = tab.IsExpertSession
	m.ExpertTeamID = strings.TrimSpace(tab.ExpertTeamID)
	m.ExpertTeamName = strings.TrimSpace(tab.ExpertTeamName)
	return m
}

// ListTabs returns every open tab's metadata for the frontend TabBar.
func (a *App) ListTabs() []TabMeta {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]TabMeta, 0, len(a.tabs))
	for _, id := range a.orderedTabIDsLocked() {
		if tab := a.tabs[id]; tab != nil {
			out = append(out, a.tabMeta(tab, tab.ID == a.activeTabID))
		}
	}
	return out
}

// OpenProjectTab builds a controller scoped to workspaceRoot and opens a tab
// for the given topic. If a tab with the same (workspaceRoot, topicID) is
// already open, it just activates the existing tab.
// OpenProjectTab opens (or activates) a project-scope tab for the given
// workspace and topic. The profile-aware form takes (workspaceRoot, topicID,
// profile); the legacy form takes (workspaceRoot, topicID). The profile is
// stamped onto the new tab so its controller builds in the right partition and
// its sessions land in the per-profile session dir.
func (a *App) OpenProjectTab(args ...string) (TabMeta, error) {
	var workspaceRoot, topicID, profile string
	switch len(args) {
	case 2: // legacy: workspaceRoot, topicID
		workspaceRoot, topicID = args[0], args[1]
	case 3: // profile-aware: workspaceRoot, topicID, profile
		workspaceRoot, topicID, profile = args[0], args[1], args[2]
	default:
		return TabMeta{}, fmt.Errorf("OpenProjectTab: expected 2 or 3 args, got %d", len(args))
	}
	if workspaceRoot == "" {
		return TabMeta{}, fmt.Errorf("workspaceRoot is required")
	}
	if abs, err := filepath.Abs(workspaceRoot); err == nil {
		workspaceRoot = abs
	}
	// Strict profile isolation: the root belongs to another profile's index.
	if rootOwnedByOtherProfile(workspaceRoot, profile) {
		return TabMeta{}, fmt.Errorf("项目 %s 属于其他模式，三模式项目互相隔离", workspaceRoot)
	}
	saveWorkspace(workspaceRoot)
	addProjectTitled(workspaceRoot, profile)

	a.mu.Lock()
	// If already open, just activate.
	for _, tab := range a.tabs {
		if tab.Scope == "project" && tab.WorkspaceRoot == workspaceRoot && tab.TopicID == topicID {
			a.activeTabID = tab.ID
			meta := a.tabMeta(tab, true)
			a.saveTabsLocked()
			a.mu.Unlock()
			return meta, nil
		}
	}

	tabID := a.newUniqueTabIDLocked()
	topicTitle := topicTitleForTab("project", workspaceRoot, topicID)
	tab := &WorkspaceTab{
		ID:               tabID,
		Scope:            "project",
		WorkspaceRoot:    workspaceRoot,
		TopicID:          topicID,
		TopicTitle:       topicTitle,
		mode:             "normal",
		toolApprovalMode: control.ToolApprovalAsk,
		disabledMCP:      map[string]ServerView{},
		profile:          profile,
	}
	tab.sink = &tabEventSink{tabID: tabID, app: a}

	a.tabs[tabID] = tab
	a.tabOrder = append(a.tabOrder, tabID)
	a.activeTabID = tabID
	a.saveTabsLocked()
	// Snapshot tabMeta under the lock so currentTabMode reads tab.Ctrl
	// consistently (it's nil here — controller builds async below — but the
	// read must still be lock-protected, not torn by a concurrent rebuild).
	meta := a.tabMeta(tab, true)
	a.mu.Unlock()

	a.startTabControllerBuild(tab)
	a.emitProjectTreeChanged()
	return meta, nil
}

// OpenProjectTab3 is a non-variadic wrapper for OpenProjectTab.
// Wails v2 bindings don't handle variadic Go functions correctly (it expects
// a single array argument, not separate arguments). This wrapper accepts
// explicit parameters so the frontend can call it directly.
func (a *App) OpenProjectTab3(workspaceRoot, topicID, profile string) (TabMeta, error) {
	return a.OpenProjectTab(workspaceRoot, topicID, profile)
}

// OpenGlobalTab is the RETIRED global scope's entry point, kept as a shim for
// persisted frontend state and history entries: it now opens the topic on the
// profile's home project (工作台), where all former global topics live after
// migration.
func (a *App) OpenGlobalTab(topicID, profile string) (TabMeta, error) {
	profile = strings.TrimSpace(profile)
	return a.OpenProjectTab(ensureProfileHomeRoot(profile), topicID, profile)
}

// OpenExpertSessionTab opens (or activates) a dedicated tab for an expert
// team's collaboration run. The tab lives in the cowork profile so its
// transcript is partitioned from dev conversations, and the run's full history
// persists there. The main chat is left untouched. Dedup: reuses an existing
// expert tab for the same team if one is already open.
func (a *App) OpenExpertSessionTab(teamID, teamName string) (TabMeta, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return TabMeta{}, fmt.Errorf("teamID is required")
	}

	a.mu.Lock()
	// Dedup: reuse an existing expert tab for this team if one is open.
	for _, tab := range a.tabs {
		if tab.IsExpertSession && tab.ExpertTeamID == teamID {
			a.activeTabID = tab.ID
			// Refresh the name in case the team was renamed.
			tab.ExpertTeamName = teamName
			meta := a.tabMeta(tab, true)
			a.saveTabsLocked()
			a.mu.Unlock()
			return meta, nil
		}
	}

	tabID := a.newUniqueTabIDLocked()
	tab := &WorkspaceTab{
		ID:               tabID,
		Scope:            "expert",
		WorkspaceRoot:    "",
		TopicID:          "",
		TopicTitle:       teamName,
		profile:          config.ProfileCowork,
		mode:             "normal",
		toolApprovalMode: control.ToolApprovalAsk,
		disabledMCP:      map[string]ServerView{},
		IsExpertSession:  true,
		ExpertTeamID:     teamID,
		ExpertTeamName:   teamName,
	}
	tab.sink = &tabEventSink{tabID: tabID, app: a}

	a.tabs[tabID] = tab
	a.tabOrder = append(a.tabOrder, tabID)
	a.activeTabID = tabID
	a.saveTabsLocked()
	meta := a.tabMeta(tab, true)
	a.mu.Unlock()

	a.startTabControllerBuild(tab)
	return meta, nil
}

// EnsureBlankTab activates the existing blank tab for the target scope, or
// creates one if none exists. Reusing a blank tab keeps repeated "new session"
// clicks from piling up empty conversations.
// EnsureBlankTab activates the existing blank tab for the target scope, or
// creates one if none exists. Reusing a blank tab keeps repeated "new session"
// clicks from piling up empty conversations.
//
// The profile-aware form takes (scope, workspaceRoot, profile); the legacy form
// takes (scope, workspaceRoot) and defaults profile to "". The profile is
// stamped on the new tab so the controller builds in the right partition.
func (a *App) EnsureBlankTab(scope, workspaceRoot, profile string) (TabMeta, error) {
	profile = strings.TrimSpace(profile)
	scope = strings.TrimSpace(scope)
	// The global scope is retired (2026-08-21): every session belongs to a
	// project. Legacy global callers (scope ""/"global" or an empty root)
	// land on the profile's home project 工作台 — a real project root whose
	// sessions resolve to the former global partition.
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if scope != "project" || workspaceRoot == "" {
		scope = "project"
		workspaceRoot = ensureProfileHomeRoot(profile)
	}
	if abs, err := filepath.Abs(workspaceRoot); err == nil {
		workspaceRoot = abs
	}
	// Strict profile isolation: a root owned by another profile (e.g. the
	// coding workspace carried across a mode switch) never opens here — the
	// session lands on this profile's home project instead.
	if rootOwnedByOtherProfile(workspaceRoot, profile) {
		workspaceRoot = ensureProfileHomeRoot(profile)
	}
	saveWorkspace(workspaceRoot)
	addProjectTitled(workspaceRoot, profile)

	var created *WorkspaceTab
	a.mu.Lock()
	for _, id := range a.orderedTabIDsLocked() {
		tab := a.tabs[id]
		if a.blankTabMatchesTargetLocked(tab, scope, workspaceRoot, profile) {
			a.activeTabID = tab.ID
			meta := a.tabMeta(tab, true)
			a.saveTabsLocked()
			a.mu.Unlock()
			return meta, nil
		}
	}

	if topicID := a.indexedBlankTopicIDLocked(scope, workspaceRoot, profile); topicID != "" {
		a.mu.Unlock()
		return a.OpenProjectTab(workspaceRoot, topicID, profile)
	}

	topicID := newTopicID()
	topicTitle := defaultTopicTitle
	if err := setTopicTitleWithSource(workspaceRoot, topicID, topicTitle, topicTitleSourceAuto); err != nil {
		a.mu.Unlock()
		return TabMeta{}, err
	}
	f := loadProjectsFile(profile)
	for i, p := range f.Projects {
		if p.Root == workspaceRoot {
			f.Projects[i].Topics = prependUniqueString(p.Topics, topicID)
			_ = saveProjectsFile(f, profile)
			break
		}
	}

	tabID := a.newUniqueTabIDLocked()
	created = &WorkspaceTab{
		ID:               tabID,
		Scope:            scope,
		WorkspaceRoot:    workspaceRoot,
		TopicID:          topicID,
		TopicTitle:       topicTitleForTab(scope, workspaceRoot, topicID),
		mode:             "normal",
		toolApprovalMode: control.ToolApprovalAsk,
		profile:          profile,
		disabledMCP:      map[string]ServerView{},
	}
	created.sink = &tabEventSink{tabID: tabID, app: a}
	a.tabs[tabID] = created
	a.tabOrder = append(a.tabOrder, tabID)
	a.activeTabID = tabID
	a.saveTabsLocked()
	meta := a.tabMeta(created, true)
	a.mu.Unlock()

	a.startTabControllerBuild(created)
	a.emitProjectTreeChanged()
	a.notifyMobileSessionList()
	return meta, nil
}

// blankTabMatchesTargetLocked returns true if tab is a reusable blank tab
// matching the given scope/project root — no running controller, no real history.
func (a *App) blankTabMatchesTargetLocked(tab *WorkspaceTab, scope, workspaceRoot, profile string) bool {
	if tab == nil || tab.Scope != scope {
		return false
	}
	if scope == "project" && tab.WorkspaceRoot != workspaceRoot {
		return false
	}
	// Profile must match — never reuse a blank tab from a different profile.
	if normalizeProfileName(tab.profile) != normalizeProfileName(profile) {
		return false
	}
	if tab.Ctrl == nil {
		return strings.TrimSpace(tab.SessionPath) == ""
	}
	if tab.Ctrl.Running() {
		return false
	}
	return !messagesHaveConversationContent(tab.Ctrl.History())
}

// indexedBlankTopicIDLocked finds a blank topic ID that is indexed on disk
// but not open in any tab — for reusing without creating a new topic.
func (a *App) indexedBlankTopicIDLocked(scope, workspaceRoot, profile string) string {
	titleRoot := topicTitleRoot(scope, workspaceRoot)
	titles := loadTopicTitles(titleRoot, normalizeProfileName(profile))
	f := loadProjectsFile(normalizeProfileName(profile))

	var topicIDs []string
	for _, project := range f.Projects {
		if project.Root == workspaceRoot {
			topicIDs = orderedTopicIDs(project.Topics, titles)
			break
		}
	}
	if len(topicIDs) == 0 {
		return ""
	}

	openTopics := map[string]bool{}
	for _, tab := range a.tabs {
		if tab == nil || tab.Scope != scope || strings.TrimSpace(tab.TopicID) == "" {
			continue
		}
		if scope == "project" && tab.WorkspaceRoot != workspaceRoot {
			continue
		}
		openTopics[tab.TopicID] = true
	}
	for _, topicID := range topicIDs {
		if openTopics[topicID] {
			continue
		}
		if topicTitleForTab(scope, workspaceRoot, topicID) != defaultTopicTitle {
			continue
		}
		if findTopicSession(desktopSessionDirFor(workspaceRoot, normalizeProfileName(profile)), topicID) != "" {
			continue
		}
		return topicID
	}
	return ""
}

// SetActiveTab switches the frontend's active tab. A no-op when tabID is
// already active or unknown.
func (a *App) SetActiveTab(tabID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.tabs[tabID]; !ok {
		return fmt.Errorf("tab %q not found", tabID)
	}
	if a.activeTabID == tabID {
		return nil
	}
	a.activeTabID = tabID
	a.saveTabsLocked()
	return nil
}

// ReorderTabs persists the frontend's manual tab order. The submitted order must
// contain every currently open tab exactly once.
func (a *App) ReorderTabs(tabIDs []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(tabIDs) != len(a.tabs) {
		return fmt.Errorf("tab order length mismatch")
	}
	seen := make(map[string]bool, len(tabIDs))
	next := make([]string, 0, len(tabIDs))
	for _, id := range tabIDs {
		if _, ok := a.tabs[id]; !ok {
			return fmt.Errorf("tab %q not found", id)
		}
		if seen[id] {
			return fmt.Errorf("duplicate tab %q", id)
		}
		seen[id] = true
		next = append(next, id)
	}
	a.tabOrder = next
	a.saveTabsLocked()
	return nil
}

// CloseTab shuts down a tab's controller (snapshot + cancel + close) and
// removes it. The active tab cannot be closed when it is the last one; the
// frontend should prompt first.
func (a *App) CloseTab(tabID string) error {
	a.mu.Lock()
	tab, ok := a.tabs[tabID]
	if !ok {
		a.mu.Unlock()
		return fmt.Errorf("tab %q not found", tabID)
	}
	if len(a.tabs) <= 1 {
		a.mu.Unlock()
		return fmt.Errorf("cannot close the last tab")
	}
	ordered := a.orderedTabIDsLocked()
	closedIndex := -1
	for i, id := range ordered {
		if id == tabID {
			closedIndex = i
			break
		}
	}
	delete(a.tabs, tabID)
	a.removeTabOrderLocked(tabID)
	wasActive := a.activeTabID == tabID
	if wasActive {
		a.activeTabID = ""
		if len(a.tabOrder) > 0 {
			nextIndex := closedIndex
			if nextIndex < 0 {
				nextIndex = 0
			}
			if nextIndex >= len(a.tabOrder) {
				nextIndex = len(a.tabOrder) - 1
			}
			a.activeTabID = a.tabOrder[nextIndex]
		}
	}
	a.saveTabsLocked()
	a.mu.Unlock()

	// Tear down outside the lock.
	if tab.Ctrl != nil {
		tab.Ctrl.Cancel()
		// Drain the cancelled turn BEFORE snapshotting: a step that completes
		// between Cancel and Snapshot lands only in memory, and with the tab
		// already deleted no later snapshot can flush it — the tail of a run
		// would vanish from the reopened session.
		tab.Ctrl.WaitTurn(3 * time.Second)
		_ = tab.Ctrl.Snapshot()
		tab.Ctrl.Close()
		a.releaseSharedHost(tab.WorkspaceRoot) // drop this tab's shared-host reference
	}
	if tab.sink != nil {
		tab.sink.ctx = nil // stop further emissions (nil ctx → Emit becomes no-op)
	}
	a.notifyMobileSessionList()
	return nil
}

// buildTabController assembles a controller for a tab in the background, the
// same way buildController works for the single-controller App. On success it
// wires the controller and flips Ready; on failure it stores StartupErr.
func (a *App) startTabControllerBuild(tab *WorkspaceTab) {
	if a.ctx == nil {
		a.buildTabController(tab)
		return
	}
	go a.buildTabController(tab)
}

// markBuilt closes the tab's ready signal, waking any goroutine blocked on
// waitForTabReady. Idempotent via readyOnce: safe to call on every exit path of
// buildTabController, and harmless when a rebuilt tab closes it again.
func (tab *WorkspaceTab) markBuilt() {
	tab.readyOnce.Do(func() { close(tab.readyCh) })
}

// tabHasSelectableProvider reports whether cfg offers any model a tab could
// boot with: a provider whose api_key_env resolves, or a keyless provider the
// user explicitly added (ProviderAccess). Mirrors NeedsOnboarding — fresh
// installs carrying only the built-in local presets report false.
func tabHasSelectableProvider(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	access := map[string]bool{}
	for _, n := range cfg.Desktop.ProviderAccess {
		if n = strings.TrimSpace(n); n != "" {
			access[n] = true
		}
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.APIKeyEnv == "" {
			if access[p.Name] {
				return true
			}
			continue
		}
		if strings.TrimSpace(os.Getenv(p.APIKeyEnv)) != "" {
			return true
		}
	}
	return false
}

// cfgModelResolvable reports whether a persisted "provider/model" reference
// still exists in the current config. Tabs outlive config edits: a model picked
// months ago (e.g. a provider later removed) must fall back to default_model,
// not brick startup with "unknown model".
func cfgModelResolvable(cfg *config.Config, ref string) bool {
	if cfg == nil {
		return false
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return true
	}
	prov, model, _ := strings.Cut(ref, "/")
	p, ok := cfg.Provider(prov)
	if !ok {
		return false
	}
	if model == "" || model == p.Default {
		return true
	}
	for _, m := range p.Models {
		if m == model {
			return true
		}
	}
	return false
}

func (a *App) buildTabController(tab *WorkspaceTab) {
	// Reset the ready signal so a rebuild (model switch) produces a fresh edge.
	// Callers blocked on the previous build already returned; a new wait starts
	// against this fresh channel. Done under a.mu so reset+Do are race-free.
	a.mu.Lock()
	tab.readyCh = make(chan struct{})
	tab.readyOnce = sync.Once{}
	a.mu.Unlock()
	defer tab.markBuilt()

	wailsCtx := a.ctx
	buildCtx := a.bootContext()

	root := tab.WorkspaceRoot
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		}
	}

	if tab.Remote != nil {
		a.buildRemoteTabController(tab, root, wailsCtx)
		return
	}

	// Load config for this tab's workspace root.
	cfg, err := config.LoadForRoot(root)
	if err != nil {
		a.mu.Lock()
		tab.StartupErr = err.Error()
		tab.Ready = true
		a.mu.Unlock()
		a.emitReady(wailsCtx, tab.ID)
		return
	}

	// A key resolved from this project's .env is project-scoped; lift it into the
	// global credentials store so it follows the user to every other workspace.
	promoteProviderKeysToCredentials(cfg)

	model := strings.TrimSpace(tab.model)
	if model == "" {
		model = cfg.DefaultModel
	}
	requestedModel := model
	if resolved, fallback, ok := cfg.ResolveModelWithFallback(model); ok {
		if fallback && strings.TrimSpace(tab.model) != "" {
			a.noticeForTab(tab.ID, fmt.Sprintf("model %q is no longer available; switched to %s", requestedModel, resolved))
		}
		model = resolved
	} else if strings.TrimSpace(model) != "" {
		// Dead ref with nothing legal to fall back to — typically a tab left on
		// an injected local preset the user never added. Drop it so the Welcome
		// guard below takes over, instead of booting an "unknown model" error.
		a.noticeForTab(tab.ID, fmt.Sprintf("model %q is no longer available; add a provider to continue", requestedModel))
		model = ""
	}

	a.mu.Lock()
	tab.model = model
	tab.Label = model
	a.saveTabsLocked()
	a.mu.Unlock()

	// Fresh install with no selectable model (empty tab model + empty
	// default_model + only keyless local presets): nothing to boot yet — the
	// onboarding overlay / Welcome state guides setup, and boot.Build would
	// just surface "unknown model" noise as a startup error.
	if strings.TrimSpace(model) == "" && !tabHasSelectableProvider(cfg) {
		a.mu.Lock()
		tab.Ready = true
		a.mu.Unlock()
		a.emitReady(wailsCtx, tab.ID)
		return
	}

	// Resolve the tab's product profile (dev/cowork). Empty or "dev" yields a nil
	// profile → boot.Build keeps the unprofiled (coding) behaviour. Any other name
	// resolves against config + builtins; an unknown profile is logged and falls
	// back to dev so a stale persisted profile never blocks tab boot.
	var profile *config.Profile
	if name := strings.TrimSpace(tab.profile); name != "" && !strings.EqualFold(name, config.ProfileDev) {
		if p, perr := cfg.ResolveProfile(name); perr == nil {
			profile = p
		} else {
			slog.Warn("tabs: unknown profile, falling back to dev", "profile", name, "err", perr)
		}
	}

	if tab.sink != nil {
		tab.sink.ctx = wailsCtx
	}

	sessionDir := tabSessionDir(tab)
	topicID := strings.TrimSpace(tab.TopicID)
	if tab.Scope == "global" {
		migratedTopics := migrateLegacySessionsIntoGlobalTopics(desktopSessionDirFor("", a.activeProfileKey()), a.activeProfileKey())
		if len(migratedTopics) > 0 {
			a.emitProjectTreeChanged()
		}
		if topicID == "" && len(migratedTopics) > 0 {
			topicID = migratedTopics[0]
			topicTitle := topicTitleForTab("global", "", topicID)
			a.mu.Lock()
			if strings.TrimSpace(tab.TopicID) == "" {
				tab.TopicID = topicID
				tab.TopicTitle = topicTitle
				a.saveTabsLocked()
			} else {
				topicID = strings.TrimSpace(tab.TopicID)
			}
			a.mu.Unlock()
		}
	}
	if topicID != "" {
		if _, dir := a.findKnownTopicSession(topicID); dir != "" {
			sessionDir = dir
		}
	}

	ctrl, err := boot.Build(buildCtx, boot.Options{
		StatsSource:    "desktop",
		Model:          model,
		RequireKey:     false,
		Sink:           tab.sink,
		WorkspaceRoot:  root,
		SessionDir:     sessionDir,
		EffortOverride: cloneStringPtr(tab.effort),
		Host:           a.acquireSharedHost(root),
		Profile:        profile,
		Present:        true,
	})
	if err != nil {
		a.releaseSharedHost(root) // Build failed: drop the acquire
		a.mu.Lock()
		tab.StartupErr = err.Error()
		tab.Ready = true
		a.mu.Unlock()
		a.emitReady(wailsCtx, tab.ID)
		return
	}

	// boot.Build just (re)initialized the global RPM budget. Rebind it into the
	// RAG extractor and the platform direct-call path so extraction / multimodal
	// tools / embedding share the same per-minute quota as the main conversation.
	// On first launch this is when globalBudget first becomes available (initRAG
	// runs before Build); on later tabs it's an idempotent refresh. cfg is this
	// tab's loaded config, used to resolve the RAG bucket key.
	boot.RebindRAGBudget(a.ragExtractor, cfg)

	a.bindControllerDisplayRecorder(ctrl)
	ctrl.EnableInteractiveApproval()
	applyTabModeToController(ctrl, tab.mode)
	applyTabToolApprovalModeToController(ctrl, tab.toolApprovalMode)
	applyTabRagScopeToController(ctrl, tab.ragScope)
	ctrl.SetGoal(tab.goal)

	if dir := ctrl.SessionDir(); dir != "" {
		migratedTopics := migrateLegacySessionsIntoGlobalTopics(dir, a.activeProfileKey())
		if len(migratedTopics) > 0 {
			a.emitProjectTreeChanged()
		}
		if tab.Scope == "global" && strings.TrimSpace(tab.TopicID) == "" && len(migratedTopics) > 0 {
			topicID := migratedTopics[0]
			topicTitle := topicTitleForTab("global", "", topicID)
			a.mu.Lock()
			tab.TopicID = topicID
			tab.TopicTitle = topicTitle
			a.saveTabsLocked()
			a.mu.Unlock()
		}
		var path string
		// Prefer the exact session file persisted for this tab. Topic lookup is a
		// compatibility fallback for older desktop-tabs.json files that only stored
		// topicId and could pick the wrong session when one topic had multiple files.
		if loaded, pinnedPath, ok := loadPinnedTabSession(dir, tab.SessionPath); ok {
			if loaded != nil {
				ctrl.Resume(loaded, pinnedPath)
			} else {
				ctrl.SetSessionPath(pinnedPath)
			}
			path = pinnedPath
		}
		if path == "" && tab.TopicID != "" {
			existingPath := findTopicSession(dir, tab.TopicID)
			if existingPath != "" {
				if loaded, err := agent.LoadSession(existingPath); err == nil {
					ctrl.Resume(loaded, existingPath)
					path = existingPath
				}
			}
		}
		if path == "" {
			path = agent.NewSessionPath(dir, ctrl.Label())
			ctrl.SetSessionPath(path)
		}
		// Write/update scope/session meta.
		if path != "" {
			a.persistTabSessionPath(tab, path)
			if strings.TrimSpace(tab.TopicID) != "" {
				if err := ensureTopicIndexed(tab.Scope, tab.WorkspaceRoot, normalizeProfileName(tab.profile), tab.TopicID, tab.TopicTitle, loadTopicTitleSource(topicTitleRoot(tab.Scope, tab.WorkspaceRoot), normalizeProfileName(tab.profile), tab.TopicID)); err == nil {
					a.emitProjectTreeChanged()
				}
			}
			// Restore existing telemetry if resuming a session.
			telemetryPath := path + ".telemetry.json"
			if snapshot := loadTelemetry(telemetryPath); len(snapshot.ReadFiles) > 0 || snapshot.Usage.RequestCount > 0 {
				tab.telemMu.Lock()
				tab.readTelemetry = snapshot.ReadFiles
				tab.usageTelemetry = snapshot.Usage
				tab.telemMu.Unlock()
			}
		}
	}

	a.mu.Lock()
	if tab.Ctrl != nil && tab.Ctrl != ctrl {
		// A concurrent build won the race (async startTabControllerBuild vs
		// the sync path in SubmitToTab) — abandon THIS controller instead of
		// overwriting, which would leak it along with its shared-host ref.
		// Legit rebuild flows nil tab.Ctrl before calling us.
		a.mu.Unlock()
		ctrl.Close()
		a.releaseSharedHost(root)
		return
	}
	tab.Ctrl = ctrl
	tab.Label = ctrl.Label()
	tab.Ready = true
	tab.StartupErr = ""
	a.mu.Unlock()
	a.emitReady(wailsCtx, tab.ID)
}

// --- active tab helpers -----------------------------------------------------

// activeTab returns the currently active tab (nil when there are no tabs).
// Self-locking; safe to call from any goroutine without external lock.
func (a *App) activeTab() *WorkspaceTab {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.activeTabID == "" {
		return nil
	}
	return a.tabs[a.activeTabID]
}

// activeTabLocked is like activeTab but assumes the caller already holds a.mu
// (either RLock or Lock). Use this inside critical sections that already own
// the lock to avoid double-locking a write-lock holder.
func (a *App) activeTabLocked() *WorkspaceTab {
	if a.activeTabID == "" {
		return nil
	}
	return a.tabs[a.activeTabID]
}

// activeCtrl returns the controller of the active tab, or nil.
// Self-locking; safe to call from any goroutine without external lock.
func (a *App) activeCtrl() tabSession {
	ctrl, _ := a.activeCtrlAndRoot()
	return ctrl
}

// activeCtrlAndRoot returns the controller and workspace root of the active tab
// in a single RLock snapshot, eliminating TOCTOU between ctrl and root reads.
func (a *App) activeCtrlAndRoot() (tabSession, string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	t := a.activeTabLocked()
	if t == nil {
		return nil, ""
	}
	return t.Ctrl, t.WorkspaceRoot
}

// activeCtrlLocked is like activeCtrl but assumes the caller already holds a.mu.
func (a *App) activeCtrlLocked() tabSession {
	t := a.activeTabLocked()
	if t == nil {
		return nil
	}
	return t.Ctrl
}

func (a *App) tabByID(tabID string) *WorkspaceTab {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.tabByIDLocked(tabID)
}

func (a *App) tabByIDLocked(tabID string) *WorkspaceTab {
	if strings.TrimSpace(tabID) == "" {
		return a.activeTabLocked()
	}
	return a.tabs[tabID]
}

func (a *App) ctrlByTabID(tabID string) tabSession {
	a.mu.RLock()
	defer a.mu.RUnlock()
	tab := a.tabByIDLocked(tabID)
	if tab == nil {
		return nil
	}
	return tab.Ctrl
}

// ActiveTabID returns the currently focused tab's ID. mobilebridge maps the
// linkpeer "default"/"" tab alias onto this so the phone joins the desktop's
// active session without knowing fairpeer's UUID-style tab IDs.
func (a *App) ActiveTabID() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.activeTabID
}

// activeSink returns the active tab's event sink, or nil.

// activeModel returns the active tab's model ref.

// activeDisabledMCP returns the active tab's disabled MCP map.

// activeMCPOrder returns the active tab's MCP order.

// --- autosave per tab -------------------------------------------------------

func (a *App) scheduleTabSnapshot(tabID string) {
	a.mu.RLock()
	tab, ok := a.tabs[tabID]
	a.mu.RUnlock()
	if !ok {
		return
	}
	tab.saveMu.Lock()
	if tab.saving {
		tab.saveAgain = true
		tab.saveMu.Unlock()
		return
	}
	tab.saving = true
	tab.saveMu.Unlock()
	go a.tabSnapshotLoop(tab)
}

func (a *App) tabSnapshotLoop(tab *WorkspaceTab) {
	for {
		a.mu.RLock()
		ctrl := tab.Ctrl
		a.mu.RUnlock()
		if ctrl != nil {
			if err := ctrl.Snapshot(); err == nil {
				if !a.maybeAutoTitleTopic(tab) {
					a.emitProjectTreeChanged()
				}
			}
		}
		tab.saveMu.Lock()
		if tab.saveAgain {
			tab.saveAgain = false
			tab.saveMu.Unlock()
			continue
		}
		tab.saving = false
		tab.saveMu.Unlock()
		return
	}
}

func (a *App) maybeAutoTitleTopic(tab *WorkspaceTab) bool {
	if tab == nil || strings.TrimSpace(tab.TopicID) == "" || tab.Ctrl == nil {
		return false
	}
	titleRoot := tab.WorkspaceRoot
	if tab.Scope == "global" {
		titleRoot = ""
	}
	if source := loadTopicTitleSource(titleRoot, tab.TopicID); source != topicTitleSourceAuto {
		return false
	}
	sessionPath := tab.Ctrl.SessionPath()
	if sessionPath == "" {
		return false
	}
	nextTitle, updated := autoTitleTopicFromSession(titleRoot, tab.TopicID, sessionPath)
	if !updated {
		return false
	}
	a.updateOpenTopicTitle(tab.TopicID, nextTitle)
	a.updateTopicSessionTitles(tab.TopicID, nextTitle)
	a.emitProjectTreeChanged()
	return true
}

func autoTitleTopicFromSession(workspaceRoot, topicID, sessionPath string) (string, bool) {
	if source := loadTopicTitleSource(workspaceRoot, topicID); source != topicTitleSourceAuto {
		return "", false
	}
	nextTitle := topicTitleFromSession(sessionPath)
	if nextTitle == "" || nextTitle == loadTopicTitle(workspaceRoot, topicID) {
		return "", false
	}
	if err := setTopicTitleWithSource(workspaceRoot, topicID, nextTitle, topicTitleSourceAuto); err != nil {
		return "", false
	}
	return nextTitle, true
}

func topicTitleFromSession(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for {
		var msg struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := dec.Decode(&msg); err != nil {
			return ""
		}
		if msg.Role == "user" {
			return topicTitleFromText(agent.HandoffTask(msg.Content))
		}
	}
}

func topicTitleFromText(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "【计划】 —") || strings.HasPrefix(text, "[Plan mode") || strings.HasPrefix(text, "Plan mode") {
		return "【计划】"
	}
	text = strings.Replace(text, "<active-goal>", "【目标】", 1)

	if text == "" {
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	text = strings.Trim(text, " \t\r\n，。！？；：、,.!?;:\"'`“”‘’()（）")
	if text == "" {
		return ""
	}
	// 24 runes: enough for a meaningful Chinese/English snippet while still
	// fitting a single sidebar line at 12.5px.
	const maxRunes = 24
	runes := []rune(text)
	if len(runes) > maxRunes {
		text = strings.TrimRightFunc(string(runes[:maxRunes]), unicode.IsPunct) + "…"
	}
	if text == defaultTopicTitle {
		return ""
	}
	return text
}

// --- persistence: desktop-projects.json -------------------------------------

const desktopProjectsFile = "desktop-projects.json"
const tabsFileName = "desktop-tabs.json"
const desktopGlobalOrderToken = "__global__"

type desktopProject struct {
	Root   string   `json:"root"`
	Title  string   `json:"title,omitempty"`
	Color  string   `json:"color,omitempty"`
	Topics []string `json:"topics"` // ordered topic IDs
}

type desktopProjectFile struct {
	GlobalTitle  string           `json:"globalTitle,omitempty"`
	GlobalColor  string           `json:"globalColor,omitempty"`
	GlobalTopics []string         `json:"globalTopics,omitempty"`
	SidebarOrder []string         `json:"sidebarOrder,omitempty"`
	Projects     []desktopProject `json:"projects"`
}

type desktopTabEntry struct {
	ID               string     `json:"id"`
	Scope            string     `json:"scope"`
	WorkspaceRoot    string     `json:"workspaceRoot"`
	TopicID          string     `json:"topicId"`
	SessionPath      string     `json:"sessionPath,omitempty"`
	Model            string     `json:"model,omitempty"`
	Effort           *string    `json:"effort,omitempty"`
	Mode             string     `json:"mode,omitempty"`
	Goal             string     `json:"goal,omitempty"`
	ToolApprovalMode string     `json:"toolApprovalMode,omitempty"`
	RagScope         string     `json:"ragScope,omitempty"`
	Profile          string     `json:"profile,omitempty"`
	Remote           *RemoteRef `json:"remote,omitempty"`
	IsExpertSession  bool       `json:"isExpertSession,omitempty"`
	ExpertTeamID     string     `json:"expertTeamId,omitempty"`
	ExpertTeamName   string     `json:"expertTeamName,omitempty"`
}

type desktopTabsFile struct {
	Tabs      []desktopTabEntry `json:"tabs"`
	ActiveTab string            `json:"activeTab"`
}

func desktopConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".fairpeer")
	}
	return filepath.Join(dir, "fairpeer")
}

func (a *App) saveTabsLocked() {
	dir := desktopConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Error("save tabs: mkdir config dir", "dir", dir, "err", err)
		return
	}
	var entries []desktopTabEntry
	for _, id := range a.orderedTabIDsLocked() {
		if tab := a.tabs[id]; tab != nil {
			entries = append(entries, desktopTabEntry{
				ID:               tab.ID,
				Scope:            tab.Scope,
				WorkspaceRoot:    tab.WorkspaceRoot,
				TopicID:          tab.TopicID,
				SessionPath:      tab.currentSessionPath(),
				Model:            tab.model,
				Effort:           cloneStringPtr(tab.effort),
				Mode:             persistedTabMode(currentTabMode(tab)),
				Goal:             strings.TrimSpace(currentTabGoal(tab)),
				ToolApprovalMode: persistedToolApprovalMode(currentTabToolApprovalMode(tab)),
				RagScope:         tab.ragScope,
				Profile:          tab.profile,
				Remote:           tab.Remote,
			})
		}
	}
	f := desktopTabsFile{Tabs: entries, ActiveTab: a.activeTabID}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		slog.Error("save tabs: marshal", "err", err)
		return
	}
	path := filepath.Join(dir, tabsFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		slog.Error("save tabs: write tmp", "path", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		slog.Error("save tabs: rename", "from", tmp, "to", path, "err", err)
	}
}

func (a *App) orderedTabIDsLocked() []string {
	seen := make(map[string]bool, len(a.tabs))
	ordered := make([]string, 0, len(a.tabs))
	for _, id := range a.tabOrder {
		if _, ok := a.tabs[id]; ok && !seen[id] {
			ordered = append(ordered, id)
			seen[id] = true
		}
	}
	var missing []string
	for id := range a.tabs {
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	ordered = append(ordered, missing...)
	if len(ordered) != len(a.tabOrder) || len(missing) > 0 {
		a.tabOrder = append([]string(nil), ordered...)
	}
	return ordered
}

func (a *App) removeTabOrderLocked(tabID string) {
	next := a.tabOrder[:0]
	for _, id := range a.tabOrder {
		if id != tabID {
			next = append(next, id)
		}
	}
	a.tabOrder = next
}

func loadTabsFile() desktopTabsFile {
	path := filepath.Join(desktopConfigDir(), tabsFileName)
	b, err := os.ReadFile(path)
	if err != nil {
		return desktopTabsFile{}
	}
	var f desktopTabsFile
	json.Unmarshal(b, &f)
	return f
}

// loadProjectsFile reads the projects index. When profileKey is supplied it
// reads the per-profile file (<config>/<profile>/projects.json); without an
// argument it reads the legacy un-profiled desktop-projects.json. A missing or
// corrupt file yields an empty (normalized) desktopProjectFile.
func loadProjectsFile(profileKey ...string) desktopProjectFile {
	var path string
	if len(profileKey) > 0 && strings.TrimSpace(profileKey[0]) != "" {
		path = projectsFilePath(profileKey[0])
	} else {
		path = filepath.Join(desktopConfigDir(), desktopProjectsFile)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return desktopProjectFile{}
	}
	var f desktopProjectFile
	json.Unmarshal(b, &f)
	return normalizeProjectsFile(f)
}

var projectsMu sync.Mutex

// saveProjectsFile writes the projects index. When profileKey is supplied it
// writes the per-profile file; otherwise it writes the legacy un-profiled
// desktop-projects.json.
func saveProjectsFile(f desktopProjectFile, profileKey ...string) error {
	var path string
	if len(profileKey) > 0 && strings.TrimSpace(profileKey[0]) != "" {
		path = projectsFilePath(profileKey[0])
	} else {
		path = filepath.Join(desktopConfigDir(), desktopProjectsFile)
	}
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0o755)
	f = normalizeProjectsFile(f)
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func normalizeProjectRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}
	return root
}

func normalizeProjectsFile(f desktopProjectFile) desktopProjectFile {
	out := desktopProjectFile{
		GlobalTitle:  strings.TrimSpace(f.GlobalTitle),
		GlobalColor:  normalizeProjectColor(f.GlobalColor),
		GlobalTopics: uniqueStrings(f.GlobalTopics),
	}
	index := map[string]int{}
	for _, p := range f.Projects {
		root := normalizeProjectRoot(p.Root)
		if root == "" {
			continue
		}
		p.Root = root
		p.Title = strings.TrimSpace(p.Title)
		p.Color = normalizeProjectColor(p.Color)
		p.Topics = uniqueStrings(p.Topics)
		if i, ok := index[root]; ok {
			if out.Projects[i].Title == "" && p.Title != "" {
				out.Projects[i].Title = p.Title
			}
			if out.Projects[i].Color == "" && p.Color != "" {
				out.Projects[i].Color = p.Color
			}
			out.Projects[i].Topics = uniqueStrings(append(out.Projects[i].Topics, p.Topics...))
			continue
		}
		index[root] = len(out.Projects)
		out.Projects = append(out.Projects, p)
	}
	out.SidebarOrder = normalizeSidebarOrder(f.SidebarOrder, out.Projects)
	return out
}

func normalizeSidebarOrder(order []string, projects []desktopProject) []string {
	projectRoots := make(map[string]bool, len(projects))
	for _, project := range projects {
		if project.Root != "" {
			projectRoots[project.Root] = true
		}
	}
	seen := make(map[string]bool, len(order))
	out := make([]string, 0, len(order))
	for _, value := range order {
		value = strings.TrimSpace(value)
		if value == desktopGlobalOrderToken {
			if !seen[value] {
				seen[value] = true
				out = append(out, value)
			}
			continue
		}
		root := normalizeProjectRoot(value)
		if root == "" || !projectRoots[root] || seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func prependUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return uniqueStrings(values)
	}
	return uniqueStrings(append([]string{value}, values...))
}

func removeString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return uniqueStrings(values)
	}
	out := make([]string, 0, len(values))
	for _, item := range uniqueStrings(values) {
		if item != value {
			out = append(out, item)
		}
	}
	return out
}

func orderedTopicIDs(explicit []string, titleMap map[string]string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(explicit))
	for _, tid := range explicit {
		tid = strings.TrimSpace(tid)
		if tid == "" || seen[tid] {
			continue
		}
		seen[tid] = true
		out = append(out, tid)
	}
	remaining := make([]string, 0, len(titleMap))
	for tid := range titleMap {
		if tid != "" && !seen[tid] {
			remaining = append(remaining, tid)
		}
	}
	sort.Strings(remaining)
	return append(out, remaining...)
}

func projectTreeOrderKey(node ProjectNode) string {
	switch node.Kind {
	case "global_folder":
		return desktopGlobalOrderToken
	case "project":
		return normalizeProjectRoot(node.Root)
	default:
		return ""
	}
}

func applyProjectTreeOrder(nodes []ProjectNode, order []string) []ProjectNode {
	if len(order) == 0 {
		return nodes
	}
	byKey := make(map[string]ProjectNode, len(nodes))
	for _, node := range nodes {
		key := projectTreeOrderKey(node)
		if key != "" {
			byKey[key] = node
		}
	}
	seen := make(map[string]bool, len(nodes))
	out := make([]ProjectNode, 0, len(nodes))
	for _, value := range order {
		key := strings.TrimSpace(value)
		if key != desktopGlobalOrderToken {
			key = normalizeProjectRoot(key)
		}
		if key == "" || seen[key] {
			continue
		}
		node, ok := byKey[key]
		if !ok {
			continue
		}
		seen[key] = true
		out = append(out, node)
	}
	for _, node := range nodes {
		key := projectTreeOrderKey(node)
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		out = append(out, node)
	}
	return out
}

func projectDisplayName(p desktopProject) string {
	if title := strings.TrimSpace(p.Title); title != "" {
		return title
	}
	return workspaceName(p.Root)
}

func normalizeProjectColor(color string) string {
	switch strings.TrimSpace(strings.ToLower(color)) {
	case "red", "orange", "amber", "green", "teal", "blue", "purple", "pink":
		return strings.TrimSpace(strings.ToLower(color))
	default:
		return ""
	}
}

func projectColor(root string, profileKey ...string) string {
	root = normalizeProjectRoot(root)
	if root == "" {
		return globalProjectColor(profileKey...)
	}
	for _, p := range loadProjectsFile(profileKey...).Projects {
		if p.Root == root {
			return normalizeProjectColor(p.Color)
		}
	}
	return ""
}

func globalProjectColor(profileKey ...string) string {
	return normalizeProjectColor(loadProjectsFile(profileKey...).GlobalColor)
}

func globalProjectTitle(profileKey ...string) string {
	if title := strings.TrimSpace(loadProjectsFile(profileKey...).GlobalTitle); title != "" {
		return title
	}
	return "Global"
}

func addProject(root, title string, profileKey ...string) error {
	root = normalizeProjectRoot(root)
	if root == "" {
		return fmt.Errorf("project root is required")
	}
	// Registration gate (generation-level isolation): a root already claimed
	// by another profile is never written into this profile's index. With
	// per-profile generation (no shared sources, no root carry-over) this is
	// unreachable in normal flows — it exists so no future caller can produce
	// cross-profile content even by mistake.
	profile := ""
	if len(profileKey) > 0 {
		profile = strings.TrimSpace(profileKey[0])
	}
	if rootOwnedByOtherProfile(root, profile) {
		return nil
	}
	title = strings.TrimSpace(title)
	f := loadProjectsFile(profileKey...)
	for i, p := range f.Projects {
		if p.Root == root {
			if title != "" {
				f.Projects[i].Title = title
			}
			return saveProjectsFile(f, profileKey...)
		}
	}
	f.Projects = append(f.Projects, desktopProject{Root: root, Title: title})
	return saveProjectsFile(f, profileKey...)
}

func renameProject(root, title string, profileKey ...string) error {
	title = strings.TrimSpace(title)
	f := loadProjectsFile(profileKey...)
	if strings.TrimSpace(root) == "" {
		f.GlobalTitle = title
		return saveProjectsFile(f, profileKey...)
	}
	root = normalizeProjectRoot(root)
	for i, p := range f.Projects {
		if p.Root == root {
			f.Projects[i].Title = title
			return saveProjectsFile(f, profileKey...)
		}
	}
	f.Projects = append(f.Projects, desktopProject{Root: root, Title: title})
	return saveProjectsFile(f, profileKey...)
}

func setProjectColor(root, color string, profileKey ...string) error {
	root = normalizeProjectRoot(root)
	color = normalizeProjectColor(color)
	if root == "" {
		f := loadProjectsFile(profileKey...)
		f.GlobalColor = color
		return saveProjectsFile(f, profileKey...)
	}
	f := loadProjectsFile(profileKey...)
	for i, p := range f.Projects {
		if p.Root == root {
			f.Projects[i].Color = color
			return saveProjectsFile(f, profileKey...)
		}
	}
	f.Projects = append(f.Projects, desktopProject{Root: root, Color: color})
	return saveProjectsFile(f, profileKey...)
}

func removeProject(root string, profileKey ...string) error {
	root = normalizeProjectRoot(root)
	f := loadProjectsFile(profileKey...)
	projects := make([]desktopProject, 0, len(f.Projects))
	for _, p := range f.Projects {
		if p.Root != root {
			projects = append(projects, p)
		}
	}
	f.Projects = projects
	return saveProjectsFile(f, profileKey...)
}

// --- topic helpers ----------------------------------------------------------

const (
	topicTitlesFile        = "desktop-topic-titles.json"
	topicTitleSourcesFile  = "desktop-topic-title-sources.json"
	topicCreatedAtsFile    = "desktop-topic-created-at.json"
	defaultTopicTitle      = "新的会话"
	topicTitleSourceAuto   = "auto"
	topicTitleSourceManual = "manual"
)

// topicSidecarFile suffixes a topic sidecar filename with the profile key for
// NAMED profiles (cowork/netdev) — topic titles must not cross modes. The
// default profile keeps the legacy unsuffixed filename so existing disks and
// the one-time legacy merge keep working unchanged.
func topicSidecarFile(base string, profileKey ...string) string {
	if len(profileKey) == 0 {
		return base
	}
	k := config.ProfileNameKey(profileKey[0])
	if k == "" || k == config.ProfileDev || k == "default" {
		return base
	}
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext) + "-" + k + ext
}

func topicTitlesPath(workspaceRoot string, profileKey ...string) string {
	name := topicSidecarFile(topicTitlesFile, profileKey...)
	if workspaceRoot == "" {
		return filepath.Join(desktopConfigDir(), "global", name)
	}
	return filepath.Join(workspaceRoot, ".fairpeer", name)
}

func topicTitleSourcesPath(workspaceRoot string, profileKey ...string) string {
	name := topicSidecarFile(topicTitleSourcesFile, profileKey...)
	if workspaceRoot == "" {
		return filepath.Join(desktopConfigDir(), "global", name)
	}
	return filepath.Join(workspaceRoot, ".fairpeer", name)
}

func topicCreatedAtsPath(workspaceRoot string, profileKey ...string) string {
	name := topicSidecarFile(topicCreatedAtsFile, profileKey...)
	if workspaceRoot == "" {
		return filepath.Join(desktopConfigDir(), "global", name)
	}
	return filepath.Join(workspaceRoot, ".fairpeer", name)
}

func loadTopicTitles(workspaceRoot string, profileKey ...string) map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(topicTitlesPath(workspaceRoot, profileKey...))
	if err != nil {
		return m
	}
	json.Unmarshal(b, &m)
	return m
}

func loadTopicTitleSources(workspaceRoot string, profileKey ...string) map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(topicTitleSourcesPath(workspaceRoot, profileKey...))
	if err != nil {
		return m
	}
	json.Unmarshal(b, &m)
	return m
}

func loadTopicCreatedAts(workspaceRoot string, profileKey ...string) map[string]int64 {
	m := map[string]int64{}
	b, err := os.ReadFile(topicCreatedAtsPath(workspaceRoot, profileKey...))
	if err != nil {
		return m
	}
	json.Unmarshal(b, &m)
	return m
}

func saveTopicTitles(workspaceRoot string, m map[string]string, profileKey ...string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := topicTitlesPath(workspaceRoot, profileKey...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func saveTopicTitleSources(workspaceRoot string, m map[string]string, profileKey ...string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := topicTitleSourcesPath(workspaceRoot, profileKey...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func saveTopicCreatedAts(workspaceRoot string, m map[string]int64, profileKey ...string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := topicCreatedAtsPath(workspaceRoot, profileKey...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadTopicTitle(workspaceRoot, topicID string, profileKey ...string) string {
	return loadTopicTitles(workspaceRoot, profileKey...)[topicID]
}

// loadTopicTitleSource returns the title source ("auto"/"manual") for a topic.
// The profile-aware form takes (workspaceRoot, profile, topicID) and reads the
// profile's sidecar bucket; the legacy form takes (workspaceRoot, topicID) and
// reads the default (unsuffixed) bucket.
func loadTopicTitleSource(workspaceRoot string, rest ...string) string {
	topicID := ""
	var profileKey []string
	if len(rest) == 1 {
		topicID = rest[0]
	} else if len(rest) >= 2 {
		profileKey = []string{rest[0]}
		topicID = rest[1]
	}
	return loadTopicTitleSources(workspaceRoot, profileKey...)[topicID]
}

func loadTopicCreatedAt(workspaceRoot, topicID string, profileKey ...string) int64 {
	return loadTopicCreatedAts(workspaceRoot, profileKey...)[topicID]
}

func topicTitleForTab(scope, workspaceRoot, topicID string, profileKey ...string) string {
	titleRoot := topicTitleRoot(scope, workspaceRoot)
	if title := strings.TrimSpace(loadTopicTitle(titleRoot, topicID, profileKey...)); title != "" {
		return title
	}
	if scope == "global" {
		return "Global"
	}
	return defaultTopicTitle
}

func topicTitleRoot(scope, workspaceRoot string) string {
	if scope == "global" {
		return ""
	}
	return workspaceRoot
}

func forkTopicTitle(title string) string {
	base := strings.TrimSpace(title)
	if base == "" || base == defaultTopicTitle || base == "Global" {
		return "分叉会话"
	}
	if strings.HasSuffix(base, " · 分叉") {
		return base
	}
	return base + " · 分叉"
}

func setTopicTitle(workspaceRoot, topicID, title string, profileKey ...string) error {
	return setTopicTitleWithSource(workspaceRoot, topicID, title, topicTitleSourceManual, profileKey...)
}

func setTopicTitleWithSource(workspaceRoot, topicID, title, source string, profileKey ...string) error {
	m := loadTopicTitles(workspaceRoot, profileKey...)
	if strings.TrimSpace(title) == "" {
		delete(m, topicID)
	} else {
		m[topicID] = strings.TrimSpace(title)
	}
	if err := saveTopicTitles(workspaceRoot, m, profileKey...); err != nil {
		return err
	}

	sources := loadTopicTitleSources(workspaceRoot, profileKey...)
	if strings.TrimSpace(title) == "" || strings.TrimSpace(source) == "" {
		delete(sources, topicID)
	} else {
		sources[topicID] = strings.TrimSpace(source)
	}
	return saveTopicTitleSources(workspaceRoot, sources, profileKey...)
}

func setTopicCreatedAt(workspaceRoot, topicID string, createdAt int64, profileKey ...string) error {
	created := loadTopicCreatedAts(workspaceRoot, profileKey...)
	topicID = strings.TrimSpace(topicID)
	if topicID == "" || createdAt <= 0 {
		delete(created, topicID)
	} else {
		created[topicID] = createdAt
	}
	return saveTopicCreatedAts(workspaceRoot, created, profileKey...)
}

func deleteTopicCreatedAt(workspaceRoot, topicID string, profileKey ...string) {
	created := loadTopicCreatedAts(workspaceRoot, profileKey...)
	delete(created, topicID)
	_ = saveTopicCreatedAts(workspaceRoot, created, profileKey...)
}

// topicIndexMu serializes recovery writes to desktop-projects.json and topic
// title indexes. Startup builds restored tabs concurrently, and each tab may
// repair its missing index.
var topicIndexMu sync.Mutex

// ensureTopicIndexed records a topic in the projects index and writes its title
// sidecar. The profile-aware form takes scope, root, profile, topicID, title,
// source (6 args); the legacy form is scope, root, topicID, title, source (5
// args) and operates on the un-profiled index. The profile routes the projects
// index to the per-profile file so dev/cowork topics stay separated.
func ensureTopicIndexed(args ...string) error {
	var scope, workspaceRoot, topicID, title, source, profile string
	switch len(args) {
	case 5: // legacy: scope, root, topicID, title, source
		scope, workspaceRoot, topicID, title, source = args[0], args[1], args[2], args[3], args[4]
	case 6: // profile-aware: scope, root, profile, topicID, title, source
		scope, workspaceRoot, profile, topicID, title, source = args[0], args[1], args[2], args[3], args[4], args[5]
	default:
		return fmt.Errorf("ensureTopicIndexed: expected 5 or 6 args, got %d", len(args))
	}
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return fmt.Errorf("topicID is required")
	}
	projectsMu.Lock()
	defer projectsMu.Unlock()
	// Global scope retired (2026-08-21): global/empty-root topics index into
	// the profile home project 工作台.
	if strings.TrimSpace(scope) == "global" || strings.TrimSpace(workspaceRoot) == "" {
		scope = "project"
		workspaceRoot = ensureProfileHomeRoot(profile)
	} else {
		workspaceRoot = normalizeProjectRoot(workspaceRoot)
	}
	// Strict profile isolation: skip (without error) when the tab's root is
	// owned by another profile — the topic stays indexed under its owner.
	if rootOwnedByOtherProfile(workspaceRoot, profile) {
		return nil
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = defaultTopicTitle
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = topicTitleSourceManual
	}
	if err := setTopicTitleWithSource(workspaceRoot, topicID, title, source, profile); err != nil {
		return err
	}
	f := loadProjectsFile(profile)
	for i, p := range f.Projects {
		if p.Root == workspaceRoot {
			f.Projects[i].Topics = prependUniqueString(p.Topics, topicID)
			return saveProjectsFile(f, profile)
		}
	}
	f.Projects = append(f.Projects, desktopProject{Root: workspaceRoot, Topics: []string{topicID}})
	return saveProjectsFile(f, profile)
}

var telemSaveMu sync.Mutex

func saveTelemetry(path string, snapshot tabTelemetrySnapshot) error {
	telemSaveMu.Lock()
	defer telemSaveMu.Unlock()
	if snapshot.Version == 0 {
		snapshot.Version = 2
	}
	if snapshot.ReadFiles == nil {
		snapshot.ReadFiles = []readFileRecord{}
	}
	b, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadTelemetry(path string) tabTelemetrySnapshot {
	b, err := os.ReadFile(path)
	if err != nil {
		return tabTelemetrySnapshot{Version: 2, ReadFiles: []readFileRecord{}}
	}
	var snapshot tabTelemetrySnapshot
	if err := json.Unmarshal(b, &snapshot); err == nil && (snapshot.Version > 0 || snapshot.ReadFiles != nil) {
		if snapshot.ReadFiles == nil {
			snapshot.ReadFiles = []readFileRecord{}
		}
		return snapshot
	}
	var records []readFileRecord
	if err := json.Unmarshal(b, &records); err != nil || records == nil {
		records = []readFileRecord{}
	}
	return tabTelemetrySnapshot{Version: 1, ReadFiles: records}
}

// --- project tree -----------------------------------------------------------

// ProjectNode is one node in the sidebar project tree (a project folder or a
// topic leaf).
type ProjectNode struct {
	Key            string        `json:"key"`  // stable key for React
	Kind           string        `json:"kind"` // "project" | "topic" | "global_folder" | "global_topic" | "expert_topic"
	Label          string        `json:"label"`
	Root           string        `json:"root,omitempty"` // project workspace root
	TopicID        string        `json:"topicId,omitempty"`
	ProjectColor   string        `json:"projectColor,omitempty"`
	Turns          int           `json:"turns,omitempty"`
	CreatedAt      int64         `json:"createdAt,omitempty"`
	LastActivityAt int64         `json:"lastActivityAt,omitempty"`
	Open           bool          `json:"open,omitempty"`
	Running        bool          `json:"running,omitempty"`
	Status         string        `json:"status,omitempty"`
	Children       []ProjectNode `json:"children,omitempty"`
	// Remote marks a project/topic node whose workspace lives on a remote
	// host; Root is the remote-side path. Clicks route to OpenRemoteTopicTab.
	Remote *RemoteRef `json:"remote,omitempty"`
	// SessionPath on a remote topic node pins the host-side transcript to
	// continue when the topic is clicked.
	SessionPath string `json:"sessionPath,omitempty"`
	// ExpertTeamID is set when Kind=="expert_topic" — the team this expert
	// session belongs to. The frontend uses it to open/activate the expert tab
	// on click and to render the 🤝 icon. Empty for non-expert nodes.
	ExpertTeamID string `json:"expertTeamId,omitempty"`
	// ExpertTeamName is the display name of the team for an expert_topic node.
	// Shown as the node's label when the user hasn't typed a custom topic title.
	ExpertTeamName string `json:"expertTeamName,omitempty"`
}

func normalizeTopicStatus(status string) string {
	switch status {
	case topicStatusThinking, topicStatusStreaming, topicStatusWaitingConfirmation, topicStatusPaused, topicStatusError:
		return status
	default:
		return ""
	}
}

func topicStatusPriority(status string) int {
	switch normalizeTopicStatus(status) {
	case topicStatusWaitingConfirmation:
		return 60
	case topicStatusStreaming:
		return 40
	case topicStatusThinking:
		return 30
	case topicStatusPaused:
		return 20
	case topicStatusError:
		return 10
	default:
		return 0
	}
}

func mergeTopicStatus(current, candidate string) string {
	if topicStatusPriority(candidate) > topicStatusPriority(current) {
		return normalizeTopicStatus(candidate)
	}
	return normalizeTopicStatus(current)
}

func activityStatusForTab(tab *WorkspaceTab) string {
	if tab == nil {
		return ""
	}
	status := normalizeTopicStatus(tab.ActivityStatus)
	running := tab.Ctrl != nil && tab.Ctrl.Running()
	if running {
		if status == "" || status == topicStatusError {
			return topicStatusThinking
		}
		return status
	}
	if status == topicStatusError || status == topicStatusPaused {
		return status
	}
	return ""
}

// migrateLegacySessionsIntoGlobalTopics makes pre-topic desktop history visible
// in the v2 sidebar. Imported v0.x sessions and older desktop sessions are plain
// .jsonl files, sometimes with branch metadata but no topic metadata; the
// history panel can list them, but the project tree cannot. Give each such
// session a deterministic Global topic so every old conversation has a direct
// sidebar entry without guessing a project workspace.
// legacyMigrationMu serializes the lockless load-modify-save of the projects /
// topic-title files: this migration runs from every concurrent buildTabController
// and from ListProjectTree, so without it parallel runs lose each other's appends.
var legacyMigrationMu sync.Mutex

// topicMigratedMarker is a sentinel file written after a successful migration
// pass so subsequent calls skip the expensive directory scan. The marker is
// invalidated if the directory is deleted (e.g. user clears data).
const topicMigratedMarker = ".topic-migrated"

func migrateLegacySessionsIntoGlobalTopics(dir string, profileKey ...string) []string {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	projectsMu.Lock()
	defer projectsMu.Unlock()

	// Fast path: if the marker exists, the directory was already fully migrated
	// on a previous call. The per-session TopicID check is idempotent, but
	// scanning and loading every .meta sidecar is unnecessary I/O.
	markerPath := filepath.Join(dir, topicMigratedMarker)
	if _, err := os.Stat(markerPath); err == nil {
		return nil
	}

	infos, err := agent.ListSessions(dir)
	if err != nil || len(infos) == 0 {
		return nil
	}
	profile := ""
	if len(profileKey) > 0 {
		profile = strings.TrimSpace(profileKey[0])
	}
	// Global scope retired (2026-08-21): legacy unscoped sessions are adopted
	// by the profile's home project 工作台 instead of a global topic section.
	home := ensureProfileHomeRoot(profile)
	titles := loadSessionTitles(dir)
	topicTitles := loadTopicTitles(home, profile)
	topicSources := loadTopicTitleSources(home, profile)
	f := loadProjectsFile(profileKey...)

	var migratedTopicIDs []string
	for _, info := range infos {
		if strings.TrimSpace(info.TopicID) != "" {
			continue
		}
		topicID := legacySessionTopicID(info.Path)
		if topicID == "" {
			continue
		}
		title := strings.TrimSpace(titles[filepath.Base(info.Path)])
		if title == "" {
			title = topicTitleFromText(info.Preview)
		} else if normalized := topicTitleFromText(title); normalized != "" {
			title = normalized
		}
		if title == "" {
			when := info.LastActivityAt
			if when.IsZero() {
				when = info.ModTime
			}
			if when.IsZero() {
				title = "历史会话"
			} else {
				title = "历史会话 " + when.Local().Format("2006-01-02")
			}
		}

		meta, err := agent.EnsureBranchMeta(info.Path)
		if err != nil {
			continue
		}
		// Only adopt genuinely-unscoped legacy sessions. A session that already
		// carries a project scope or workspace root is not legacy — never strip
		// its binding into its project.
		if meta.Scope == "project" || strings.TrimSpace(meta.WorkspaceRoot) != "" {
			continue
		}
		meta.Scope = "project"
		meta.WorkspaceRoot = home
		meta.TopicID = topicID
		meta.TopicTitle = title
		if err := agent.SaveBranchMetaPreserveUpdated(info.Path, meta); err != nil {
			continue
		}
		if strings.TrimSpace(topicTitles[topicID]) == "" {
			topicTitles[topicID] = title
			topicSources[topicID] = topicTitleSourceManual
		}
		migratedTopicIDs = append(migratedTopicIDs, topicID)
	}
	if len(migratedTopicIDs) == 0 {
		_ = os.WriteFile(markerPath, []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
		return nil
	}
	homeIdx := -1
	for i := range f.Projects {
		if normalizeProjectRoot(f.Projects[i].Root) == normalizeProjectRoot(home) {
			homeIdx = i
			break
		}
	}
	if homeIdx < 0 {
		f.Projects = append([]desktopProject{{Root: home, Title: homeProjectTitle}}, f.Projects...)
		homeIdx = 0
	}
	f.Projects[homeIdx].Topics = uniqueStrings(append(migratedTopicIDs, f.Projects[homeIdx].Topics...))
	_ = saveProjectsFile(f, profileKey...)
	_ = saveTopicTitles(home, topicTitles, profile)
	_ = saveTopicTitleSources(home, topicSources, profile)
	// Mark as done after a successful migration pass.
	_ = os.WriteFile(markerPath, []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
	return migratedTopicIDs
}

func restoreSessionTopicIndex(dir, sessionPath string, profileKey ...string) error {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return nil
	}
	meta, ok, err := agent.LoadBranchMeta(sessionPath)
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(meta.TopicID) == "" {
		migrateLegacySessionsIntoGlobalTopics(dir, profileKey...)
		return nil
	}

	topicID := strings.TrimSpace(meta.TopicID)
	scope := strings.TrimSpace(meta.Scope)
	workspaceRoot := strings.TrimSpace(meta.WorkspaceRoot)
	profile := ""
	if len(profileKey) > 0 {
		profile = strings.TrimSpace(profileKey[0])
	}
	// Global scope retired (2026-08-21): legacy global sessions (scope
	// "global" or unscoped with no root) are adopted by the profile's home
	// project 工作台. Session files stay put — the home project resolves to
	// the former global partition.
	if scope != "project" {
		scope = "project"
	}
	workspaceRoot = normalizeProjectRoot(workspaceRoot)
	if workspaceRoot == "" {
		workspaceRoot = ensureProfileHomeRoot(profile)
	}

	title := restoredSessionTopicTitle(dir, sessionPath, meta)
	if title == "" {
		title = defaultTopicTitle
	}
	if err := setTopicTitleWithSource(workspaceRoot, topicID, title, topicTitleSourceManual); err != nil {
		return err
	}

	f := loadProjectsFile(profileKey...)
	found := false
	for i, p := range f.Projects {
		if p.Root != workspaceRoot {
			continue
		}
		f.Projects[i].Topics = prependUniqueString(p.Topics, topicID)
		found = true
		break
	}
	if !found {
		title := ""
		if _, ok := profileKeyForHomeRoot(workspaceRoot); ok {
			title = homeProjectTitle
		}
		f.Projects = append(f.Projects, desktopProject{
			Root:   workspaceRoot,
			Title:  title,
			Topics: []string{topicID},
		})
	}
	meta.Scope = "project"
	meta.WorkspaceRoot = workspaceRoot
	meta.TopicID = topicID
	meta.TopicTitle = title
	if err := saveProjectsFile(f, profileKey...); err != nil {
		return err
	}
	return agent.SaveBranchMetaPreserveUpdated(sessionPath, meta)
}

func restoredSessionTopicTitle(dir, sessionPath string, meta agent.BranchMeta) string {
	if title := topicTitleFromText(meta.TopicTitle); title != "" {
		return title
	}
	if title := topicTitleFromText(loadSessionTitles(dir)[filepath.Base(sessionPath)]); title != "" {
		return title
	}
	if s, err := agent.LoadSession(sessionPath); err == nil {
		for _, msg := range s.Messages {
			if msg.Role == provider.RoleUser {
				if title := topicTitleFromText(provider.ContentString(msg.Content)); title != "" {
					return title
				}
			}
		}
	}
	return ""
}

func legacySessionTopicID(path string) string {
	id := agent.BranchID(path)
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	var b strings.Builder
	b.WriteString("legacy_")
	for _, r := range id {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	prefix := strings.TrimRight(b.String(), "_")
	if prefix == "legacy" {
		prefix = "legacy_session"
	}
	return prefix + "_" + hex.EncodeToString(sum[:])[:12]
}

// TopicMeta describes a topic for the project tree.
type TopicMeta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"createdAt"`
}

// CreateTopic creates a new topic under a project workspace and returns its metadata.
// CreateTopic registers a new topic in the projects index and writes its title
// sidecar. The profile-aware form takes (scope, workspaceRoot, profile, title);
// the legacy form takes (scope, workspaceRoot, title). The profile routes the
// topic into the matching per-profile projects index so dev/cowork topics stay
// separated. An empty profile defaults to the un-profiled index (backward
// compatible).
func (a *App) CreateTopic(args ...string) (TopicMeta, error) {
	var scope, workspaceRoot, title, profile string
	switch len(args) {
	case 3: // legacy: scope, workspaceRoot, title
		scope, workspaceRoot, title = args[0], args[1], args[2]
	case 4: // profile-aware: scope, workspaceRoot, profile, title
		scope, workspaceRoot, profile, title = args[0], args[1], args[2], args[3]
	default:
		return TopicMeta{}, fmt.Errorf("CreateTopic: expected 3 or 4 args, got %d", len(args))
	}
	trimmedTitle := strings.TrimSpace(title)
	titleSource := topicTitleSourceManual
	if trimmedTitle == "" {
		trimmedTitle = defaultTopicTitle
		titleSource = topicTitleSourceAuto
	}
	topicID := newTopicID()
	createdAt := time.Now().UnixMilli()
	// Global scope retired (2026-08-21): global/empty-root topics land on the
	// profile home project 工作台.
	if scope != "project" || strings.TrimSpace(workspaceRoot) == "" {
		scope = "project"
		workspaceRoot = ensureProfileHomeRoot(profile)
	}
	if abs, err := filepath.Abs(workspaceRoot); err == nil {
		workspaceRoot = abs
	}
	// Strict profile isolation: refuse creating a topic on another profile's
	// project root.
	if rootOwnedByOtherProfile(workspaceRoot, profile) {
		return TopicMeta{}, fmt.Errorf("项目 %s 属于其他模式，三模式项目互相隔离", workspaceRoot)
	}
	addProjectTitled(workspaceRoot, profile)
	if err := setTopicTitleWithSource(workspaceRoot, topicID, trimmedTitle, titleSource); err != nil {
		return TopicMeta{}, err
	}
	if err := setTopicCreatedAt(workspaceRoot, topicID, createdAt); err != nil {
		return TopicMeta{}, err
	}
	// New topics should appear first in their project/global group so the item
	// just created is immediately visible and selected in the sidebar.
	f := loadProjectsFile(profile)
	if workspaceRoot == "" {
		f.GlobalTopics = prependUniqueString(f.GlobalTopics, topicID)
		_ = saveProjectsFile(f, profile)
	} else {
		for i, p := range f.Projects {
			if p.Root == workspaceRoot {
				f.Projects[i].Topics = prependUniqueString(p.Topics, topicID)
				_ = saveProjectsFile(f, profile)
				break
			}
		}
	}
	a.emitProjectTreeChanged()
	return TopicMeta{ID: topicID, Title: trimmedTitle, CreatedAt: createdAt}, nil
}

// RenameProject updates the sidebar-only display title for a project folder.
// Empty title clears the override and falls back to the folder name.
func (a *App) RenameProject(workspaceRoot, title string) error {
	if err := renameProject(workspaceRoot, title, a.activeProfileKey()); err != nil {
		return err
	}
	a.emitProjectTreeChanged()
	return nil
}

// SetProjectColor updates the project-level accent color used by project topics
// in the sidebar and tabs. Empty color restores the default accent.
func (a *App) SetProjectColor(workspaceRoot, color string) error {
	if err := setProjectColor(workspaceRoot, color, a.activeProfileKey()); err != nil {
		return err
	}
	a.emitProjectTreeChanged()
	return nil
}

// ReorderProjects persists the user-defined order of project folders and,
// when present, the virtual Global sidebar section. The profile-aware form
// takes (workspaceRoots, profile) so the reorder lands in the per-profile
// projects index; the legacy form takes (workspaceRoots) and uses the
// un-profiled index. The profile argument is the active profile key
// ("dev"/"cowork") so reordering dev's sidebar doesn't reorder cowork's.
func (a *App) ReorderProjects(workspaceRoots []string, profile string) error {
	f := loadProjectsFile(profile)
	byRoot := make(map[string]desktopProject, len(f.Projects))
	for _, project := range f.Projects {
		byRoot[project.Root] = project
	}
	seen := make(map[string]bool, len(workspaceRoots))
	next := make([]desktopProject, 0, len(workspaceRoots))
	sidebarOrder := make([]string, 0, len(workspaceRoots))
	hasGlobalOrder := false
	for _, root := range workspaceRoots {
		root = strings.TrimSpace(root)
		if root == desktopGlobalOrderToken {
			if seen[root] {
				return fmt.Errorf("duplicate global section")
			}
			seen[root] = true
			hasGlobalOrder = true
			sidebarOrder = append(sidebarOrder, root)
			continue
		}
		root = normalizeProjectRoot(root)
		project, ok := byRoot[root]
		if !ok {
			return fmt.Errorf("project %q not found", root)
		}
		if seen[root] {
			return fmt.Errorf("duplicate project %q", root)
		}
		seen[root] = true
		next = append(next, project)
		sidebarOrder = append(sidebarOrder, root)
	}
	if len(next) != len(f.Projects) {
		return fmt.Errorf("project order length mismatch")
	}
	f.Projects = next
	if hasGlobalOrder {
		f.SidebarOrder = sidebarOrder
	} else {
		f.SidebarOrder = nil
	}
	if err := saveProjectsFile(f, profile); err != nil {
		return err
	}
	a.emitProjectTreeChanged()
	return nil
}

// RenameTopic updates a topic's display title.
func (a *App) RenameTopic(topicID, title string) error {
	trimmed := strings.TrimSpace(title)
	profileKey := a.activeProfileKey()
	// Find which workspace this topic belongs to by scanning all project topic titles.
	f := loadProjectsFile(profileKey)
	for _, p := range f.Projects {
		m := loadTopicTitles(p.Root, profileKey)
		if _, ok := m[topicID]; ok {
			if err := setTopicTitle(p.Root, topicID, trimmed, profileKey); err != nil {
				return err
			}
			a.updateOpenTopicTitle(topicID, trimmed)
			a.updateTopicSessionTitles(topicID, trimmed)
			a.emitProjectTreeChanged()
			return nil
		}
	}
	// Check global.
	m := loadTopicTitles("", profileKey)
	if _, ok := m[topicID]; ok {
		if err := setTopicTitle("", topicID, trimmed, profileKey); err != nil {
			return err
		}
		a.updateOpenTopicTitle(topicID, trimmed)
		a.updateTopicSessionTitles(topicID, trimmed)
		a.emitProjectTreeChanged()
		return nil
	}
	if scope, workspaceRoot, ok := a.findTopicLocation(topicID); ok {
		if err := ensureTopicIndexed(scope, workspaceRoot, profileKey, topicID, trimmed, topicTitleSourceManual); err != nil {
			return err
		}
		a.updateOpenTopicTitle(topicID, trimmed)
		a.updateTopicSessionTitles(topicID, trimmed)
		a.emitProjectTreeChanged()
		return nil
	}
	return fmt.Errorf("topic %q not found", topicID)
}

func (a *App) findTopicLocation(topicID string) (string, string, bool) {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return "", "", false
	}
	a.mu.RLock()
	for _, tab := range a.tabs {
		if tab == nil || tab.TopicID != topicID {
			continue
		}
		scope := tab.Scope
		workspaceRoot := tab.WorkspaceRoot
		a.mu.RUnlock()
		if scope == "global" {
			return "global", "", true
		}
		return "project", normalizeProjectRoot(workspaceRoot), true
	}
	a.mu.RUnlock()

	infos, err := agent.ListSessions(config.SessionDir())
	if err != nil {
		return "", "", false
	}
	for _, info := range infos {
		if strings.TrimSpace(info.TopicID) != topicID {
			continue
		}
		scope := strings.TrimSpace(info.Scope)
		if scope == "" {
			scope = "global"
		}
		if scope == "global" {
			return "global", "", true
		}
		return "project", normalizeProjectRoot(info.WorkspaceRoot), true
	}
	return "", "", false
}

func (a *App) updateOpenTopicTitle(topicID, title string) {
	if strings.TrimSpace(topicID) == "" || strings.TrimSpace(title) == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, tab := range a.tabs {
		if tab != nil && tab.TopicID == topicID {
			tab.TopicTitle = title
		}
	}
}

func (a *App) updateTopicSessionTitles(topicID, title string) {
	if strings.TrimSpace(topicID) == "" || strings.TrimSpace(title) == "" {
		return
	}
	for _, dir := range a.knownSessionDirs() {
		infos, err := agent.ListSessions(dir)
		if err != nil {
			continue
		}
		for _, info := range infos {
			if info.TopicID != topicID {
				continue
			}
			meta, ok, err := agent.LoadBranchMeta(info.Path)
			if err != nil || !ok {
				continue
			}
			meta.TopicTitle = title
			_ = agent.SaveBranchMetaPreserveUpdated(info.Path, meta)
		}
	}
}

func (a *App) setTabActivityStatus(tabID, status string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	tab := a.tabs[tabID]
	if tab == nil {
		return false
	}
	status = normalizeTopicStatus(status)
	if tab.ActivityStatus == status {
		return false
	}
	tab.ActivityStatus = status
	return true
}

func (a *App) emitProjectTreeChanged() {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "project-tree:changed")
	}
}

// DeleteTopic removes a topic and its title metadata.
func (a *App) DeleteTopic(topicID string) error {
	profileKey := a.activeProfileKey()
	f := loadProjectsFile(profileKey)
	found := false
	for _, p := range f.Projects {
		m := loadTopicTitles(p.Root)
		if _, ok := m[topicID]; ok {
			delete(m, topicID)
			_ = saveTopicTitles(p.Root, m)
			sources := loadTopicTitleSources(p.Root)
			delete(sources, topicID)
			_ = saveTopicTitleSources(p.Root, sources)
			deleteTopicCreatedAt(p.Root, topicID)
			found = true
			break
		}
	}
	if !found {
		m := loadTopicTitles("")
		if _, ok := m[topicID]; ok {
			delete(m, topicID)
			_ = saveTopicTitles("", m)
			sources := loadTopicTitleSources("")
			delete(sources, topicID)
			_ = saveTopicTitleSources("", sources)
			deleteTopicCreatedAt("", topicID)
			f.GlobalTopics = removeString(f.GlobalTopics, topicID)
			found = true
		}
	}
	if !found {
		return fmt.Errorf("topic %q not found", topicID)
	}
	// Remove from project topic list.
	for i, p := range f.Projects {
		for j, tid := range p.Topics {
			if tid == topicID {
				f.Projects[i].Topics = append(f.Projects[i].Topics[:j], f.Projects[i].Topics[j+1:]...)
				break
			}
		}
	}
	_ = saveProjectsFile(f, profileKey)
	a.emitProjectTreeChanged()
	return nil
}

// TrashTopic removes a topic from the project tree and moves its saved session
// records into the session trash. Open non-running tabs for the topic are first
// snapshotted and closed so their autosave files can be moved instead of being
// recreated immediately after deletion.
func (a *App) TrashTopic(topicID string) error {
	if strings.TrimSpace(topicID) == "" {
		return fmt.Errorf("topicID is required")
	}

	type topicTab struct {
		id            string
		tab           *WorkspaceTab
		ctrl          tabSession
		sink          *tabEventSink
		scope         string
		workspaceRoot string
	}
	var openTabs []topicTab
	a.mu.RLock()
	for _, tab := range a.tabs {
		if tab == nil || tab.TopicID != topicID {
			continue
		}
		if tab.Ctrl != nil && tab.Ctrl.Running() {
			a.mu.RUnlock()
			return fmt.Errorf("can't move a running conversation to trash; stop it first")
		}
		openTabs = append(openTabs, topicTab{
			id:            tab.ID,
			tab:           tab,
			ctrl:          tab.Ctrl,
			sink:          tab.sink,
			scope:         tab.Scope,
			workspaceRoot: tab.WorkspaceRoot,
		})
	}
	a.mu.RUnlock()

	for _, item := range openTabs {
		if item.ctrl != nil {
			if err := item.ctrl.Snapshot(); err != nil {
				return err
			}
			item.ctrl.Close()
		}
		if item.sink != nil {
			item.sink.ctx = nil
		}
	}

	var fallbackScope, fallbackRoot string
	needsFallback := false
	if len(openTabs) > 0 {
		fallbackScope = openTabs[0].scope
		fallbackRoot = openTabs[0].workspaceRoot
		a.mu.Lock()
		removedActive := false
		for _, item := range openTabs {
			if a.tabs[item.id] != item.tab {
				continue
			}
			if a.activeTabID == item.id {
				removedActive = true
			}
			delete(a.tabs, item.id)
			a.removeTabOrderLocked(item.id)
		}
		if removedActive {
			a.activeTabID = ""
			if len(a.tabOrder) > 0 {
				a.activeTabID = a.tabOrder[0]
			}
		}
		needsFallback = len(a.tabs) == 0
		a.saveTabsLocked()
		a.mu.Unlock()
	}

	for _, dir := range a.knownSessionDirs() {
		infos, err := agent.ListSessions(dir)
		if err != nil {
			return err
		}
		for _, info := range infos {
			if info.TopicID != topicID {
				continue
			}
			sessionPath, _, err := validateSessionPath(dir, info.Path)
			if err != nil {
				return err
			}
			if err := deleteSessionFile(dir, sessionPath); err != nil {
				return err
			}
		}
	}
	if err := a.DeleteTopic(topicID); err != nil {
		return err
	}
	if needsFallback {
		if fallbackScope == "global" {
			fallbackRoot = ""
		}
		topic, err := a.CreateTopic(fallbackScope, fallbackRoot, "")
		if err != nil {
			return err
		}
		if fallbackScope == "global" {
			_, err = a.OpenGlobalTab(topic.ID, "")
		} else {
			_, err = a.OpenProjectTab(fallbackRoot, topic.ID)
		}
		if err != nil {
			return err
		}
	}
	a.emitProjectTreeChanged()
	return nil
}

// ListProjectTree builds the sidebar tree: project folders each containing
// their topics, plus a Global section.
// ListProjectTree returns the project-tree sidebar model. The profile argument
// routes the projects index to the per-profile file so dev/cowork/netdev show
// only their own conversations. An empty profile ("") reads the legacy
// un-profiled index (backward compatible). Wails binds this as a 1-arg method;
// the frontend always passes a profile ("dev"/"cowork"/"netdev").
func (a *App) ListProjectTree(profile string) []ProjectNode {
	// Legacy adoption reads ONLY this profile's partition: the dev partition
	// (config.SessionDir) must never feed a cowork/netdev tree (generation-
	// level isolation — cross-profile content is not produced in the first
	// place).
	migrateLegacySessionsIntoGlobalTopics(desktopSessionDirFor("", profile), profile)
	// Global scope retired (2026-08-21): fold any remaining global topics into
	// the profile home project and make sure the home entry exists.
	migrateGlobalIntoHome(profile)
	f := loadProjectsFile(profile)
	out := []ProjectNode{}
	type topicSummary struct {
		turns          int
		lastActivityAt int64
		profile        string
	}
	topicSummaries := map[string]topicSummary{}
	for _, dir := range a.knownSessionDirs() {
		infos, err := agent.ListSessions(dir)
		if err != nil {
			continue
		}
		for _, info := range infos {
			if strings.TrimSpace(info.TopicID) == "" {
				continue
			}
			key := topicSummaryKey(info.Scope, info.WorkspaceRoot, info.TopicID)
			summary := topicSummaries[key]
			summary.turns += info.Turns
			lastActivityAt := info.LastActivityAt.UnixMilli()
			if lastActivityAt > summary.lastActivityAt {
				summary.lastActivityAt = lastActivityAt
			}
			if summary.profile == "" && info.Profile != "" {
				summary.profile = info.Profile
			}
			topicSummaries[key] = summary
		}
	}
	openTopics := map[string]struct {
		open    bool
		running bool
		status  string
	}{}
	openRoots := map[string]bool{}
	profileKey := config.ProfileNameKey(profile)
	a.mu.RLock()
	for _, tab := range a.tabs {
		if tab == nil {
			continue
		}
		if tab.WorkspaceRoot != "" {
			if profileKey == "" || tab.profile == "" || config.ProfileNameKey(tab.profile) == profileKey {
				openRoots[normalizeProjectRoot(tab.WorkspaceRoot)] = true
			}
		}
		if strings.TrimSpace(tab.TopicID) == "" {
			continue
		}
		key := topicSummaryKey(tab.Scope, tab.WorkspaceRoot, tab.TopicID)
		status := openTopics[key]
		status.open = true
		if tab.Ctrl != nil && tab.Ctrl.Running() {
			status.running = true
		}
		status.status = mergeTopicStatus(status.status, activityStatusForTab(tab))
		openTopics[key] = status

		summary := topicSummaries[key]
		if summary.profile == "" && tab.profile != "" {
			summary.profile = tab.profile
			topicSummaries[key] = summary
		}
	}
	a.mu.RUnlock()

	// Project sections. (The global section is retired — former global topics
	// live in the profile home project 工作台 after migrateGlobalIntoHome.)

	// Project sections.
	for _, p := range f.Projects {
		title := p.Title
		if title == "" {
			title = workspaceName(p.Root)
		}
		node := ProjectNode{
			Key:  "project_" + p.Root,
			Kind: "project",
			Root: p.Root,
		}

		// Gather topics: explicit topic list + all known topic titles.
		titleMap := loadTopicTitles(p.Root, profile)
		createdMap := loadTopicCreatedAts(p.Root, profile)
		topicIDs := orderedTopicIDs(p.Topics, titleMap)

		children := make([]ProjectNode, 0, len(topicIDs))
		for _, tid := range topicIDs {
			topicTitle := strings.TrimSpace(titleMap[tid])
			if topicTitle == "" {
				topicTitle = topicTitleForTab("project", p.Root, tid)
			}
			summary := topicSummaries[topicSummaryKey("project", p.Root, tid)]
			if legacy, ok := topicSummaries[topicSummaryKey("global", "", tid)]; ok {
				// Legacy global sessions of a migrated topic keep their old
				// "global::" summary key until the topic is reopened and its
				// meta re-stamped; merge so turns/activity stay visible.
				summary.turns += legacy.turns
				if legacy.lastActivityAt > summary.lastActivityAt {
					summary.lastActivityAt = legacy.lastActivityAt
				}
				if summary.profile == "" {
					summary.profile = legacy.profile
				}
			}
			effSummary := summary.profile
			if effSummary == "" {
				effSummary = config.ProfileDev
			}
			effProfile := profile
			if effProfile == "" {
				effProfile = config.ProfileDev
			}
			if effSummary != effProfile {
				continue
			}
			status := openTopics[topicSummaryKey("project", p.Root, tid)]
			// Skip empty topics: a session record only appears in the project
			// tree after at least one completed conversation turn.  Brand-new
			// tabs that haven't exchanged any messages yet stay invisible,
			// even if the tab is currently open.
			if summary.turns == 0 {
				continue
			}
			children = append(children, ProjectNode{
				Key:            "topic_" + tid,
				Kind:           "topic",
				Label:          topicTitle,
				Root:           p.Root,
				TopicID:        tid,
				ProjectColor:   p.Color,
				Turns:          summary.turns,
				CreatedAt:      createdMap[tid],
				LastActivityAt: summary.lastActivityAt,
				Open:           status.open,
				Running:        status.running,
				Status:         status.status,
			})
		}
		if len(children) == 0 && !openRoots[normalizeProjectRoot(p.Root)] {
			// The home project always renders — it is the profile's landing
			// workspace (the retired Global folder's replacement).
			if _, isHome := profileKeyForHomeRoot(p.Root); !isHome {
				continue
			}
		}
		node.Label = title
		node.ProjectColor = p.Color
		node.Children = children
		out = append(out, node)
	}

	// Remote-workspace nodes: one per registered remote project (registry in
	// remote-projects.json), with host-side topics merged in when a link is
	// live. Offline remote projects still render (bare node) so the workspace
	// stays reachable from the tree.
	for _, entry := range loadRemoteProjects() {
		if strings.TrimSpace(config.ProfileNameKey(profile)) != "" && entry.Ref.Kind == "" {
			continue
		}
		ref := entry.Ref
		node := ProjectNode{
			Key:    "remote_" + entry.Slug,
			Kind:   "project",
			Label:  entry.Title,
			Root:   entry.Root,
			Remote: &ref,
		}
		for _, t := range remoteTopicsForRef(a.remoteManager, ref, entry.Root) {
			label := t.Title
			if label == "" {
				label = topicTitleForTab("project", entry.Root, t.TopicID)
			}
			node.Children = append(node.Children, ProjectNode{
				Key:            "remote_topic_" + entry.Slug + "_" + t.TopicID,
				Kind:           "topic",
				Label:          label,
				Root:           entry.Root,
				TopicID:        t.TopicID,
				Remote:         &ref,
				SessionPath:    t.NewestSession,
				Turns:          t.Turns,
				LastActivityAt: t.LastActivityMs,
			})
		}
		out = append(out, node)
	}

	// Expert-session nodes: one per open expert tab. These render as 🤝 rows in
	// the project tree (kind=="expert_topic") so the user can click back into a
	// team's collaboration run. Only tabs matching the requested profile are
	// shown — expert sessions always live in the cowork profile, so they only
	// surface when the cowork sidebar is rendered.
	a.mu.RLock()
	profileKey = config.ProfileNameKey(profile)
	for _, tab := range a.tabs {
		if !tab.IsExpertSession || tab.ExpertTeamID == "" {
			continue
		}
		// Expert sessions live in cowork; skip when listing dev's tree.
		if profileKey != "" && profileKey != config.ProfileCowork {
			continue
		}
		teamName := strings.TrimSpace(tab.ExpertTeamName)
		if teamName == "" {
			teamName = tab.ExpertTeamID
		}
		label := teamName
		if t := strings.TrimSpace(tab.TopicTitle); t != "" && t != teamName {
			label = t
		}
		out = append(out, ProjectNode{
			Key:            "expert_topic_" + tab.ExpertTeamID,
			Kind:           "expert_topic",
			Label:          label,
			TopicID:        tab.TopicID,
			Open:           tab.ID == a.activeTabID,
			Running:        tab.Ctrl != nil && tab.Ctrl.Running(),
			ExpertTeamID:   tab.ExpertTeamID,
			ExpertTeamName: teamName,
		})
	}
	a.mu.RUnlock()

	return applyProjectTreeOrder(out, f.SidebarOrder)
}

func topicSummaryKey(scope, workspaceRoot, topicID string) string {
	if scope == "global" {
		return "global::" + topicID
	}
	return "project:" + workspaceRoot + ":" + topicID
}

// --- utility ----------------------------------------------------------------

func (a *App) newUniqueTabIDLocked() string {
	for {
		id := newTabID()
		if _, exists := a.tabs[id]; !exists {
			return id
		}
	}
}

func (a *App) restoredTabIDLocked(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return a.newUniqueTabIDLocked()
	}
	if _, exists := a.tabs[id]; exists {
		return a.newUniqueTabIDLocked()
	}
	return id
}

func normalizeTabMode(mode string) string {
	switch mode {
	case "plan", "yolo", "plan-yolo", "yolo-plan":
		if mode == "yolo-plan" {
			return "plan-yolo"
		}
		return mode
	default:
		return "normal"
	}
}

func tabModeFromAxes(plan, autoApproveTools bool) string {
	switch {
	case plan && autoApproveTools:
		return "plan-yolo"
	case plan:
		return "plan"
	case autoApproveTools:
		return "yolo"
	default:
		return "normal"
	}
}

func tabModeHasPlan(mode string) bool {
	switch normalizeTabMode(mode) {
	case "plan", "plan-yolo":
		return true
	default:
		return false
	}
}

func tabModeHasAutoApproveTools(mode string) bool {
	switch normalizeTabMode(mode) {
	case "yolo", "plan-yolo":
		return true
	default:
		return false
	}
}

func currentTabMode(tab *WorkspaceTab) string {
	if tab == nil {
		return "normal"
	}
	if tab.Ctrl != nil {
		return tabModeFromAxes(tab.Ctrl.PlanMode(), tab.Ctrl.AutoApproveTools())
	}
	return normalizeTabMode(tab.mode)
}

func currentTabGoal(tab *WorkspaceTab) string {
	if tab == nil {
		return ""
	}
	if tab.Ctrl != nil {
		return tab.Ctrl.Goal()
	}
	return strings.TrimSpace(tab.goal)
}

func currentTabGoalStatus(tab *WorkspaceTab) string {
	if tab == nil {
		return control.GoalStatusStopped
	}
	if tab.Ctrl != nil {
		return tab.Ctrl.GoalStatus()
	}
	if strings.TrimSpace(tab.goal) != "" {
		return control.GoalStatusRunning
	}
	return control.GoalStatusStopped
}

func currentTabCollaborationMode(tab *WorkspaceTab) string {
	if tab == nil {
		return "normal"
	}
	if tab.Ctrl != nil && tab.Ctrl.PlanMode() {
		return "plan"
	}
	if strings.TrimSpace(currentTabGoal(tab)) != "" && currentTabGoalStatus(tab) == control.GoalStatusRunning {
		return "goal"
	}
	return "normal"
}

func currentTabToolApprovalMode(tab *WorkspaceTab) string {
	if tab == nil {
		return control.ToolApprovalAsk
	}
	if tab.Ctrl != nil {
		return tab.Ctrl.ToolApprovalMode()
	}
	return normalizeToolApprovalMode(tab.toolApprovalMode)
}

func normalizeToolApprovalMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case control.ToolApprovalAuto:
		return control.ToolApprovalAuto
	case control.ToolApprovalYolo, "full", "full-access", "bypass":
		return control.ToolApprovalYolo
	default:
		return control.ToolApprovalAsk
	}
}

func persistedToolApprovalMode(mode string) string {
	switch normalizeToolApprovalMode(mode) {
	case control.ToolApprovalAuto, control.ToolApprovalYolo:
		return normalizeToolApprovalMode(mode)
	default:
		return ""
	}
}

// persistedTabMode is the composer mode saved with a tab so it survives reload
// and app relaunch. plan, yolo, and plan-yolo are remembered (a restored yolo
// tab keeps its status-bar indicator); "normal" is the default and isn't
// persisted. (#3517)
func persistedTabMode(mode string) string {
	switch normalizeTabMode(mode) {
	case "plan", "yolo", "plan-yolo":
		return normalizeTabMode(mode)
	}
	return ""
}

func newTabID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		now := time.Now().UTC()
		return "tab_" + now.Format("20060102150405") + "_" + fmt.Sprintf("%09d", now.Nanosecond())
	}
	return "tab_" + hex.EncodeToString(b[:])
}

func newTopicID() string {
	var b [8]byte
	rand.Read(b[:])
	return "topic_" + time.Now().UTC().Format("20060102-150405") + "_" + hex.EncodeToString(b[:])
}

func globalWorkspaceRoot() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".fairpeer", "global-workspace")
	}
	return filepath.Join(dir, "fairpeer", "global-workspace")
}

func ensureGlobalWorkspaceRoot() (string, error) {
	root := globalWorkspaceRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

func globalTabWorkspaceRoot() string {
	root, err := ensureGlobalWorkspaceRoot()
	if err != nil {
		return globalWorkspaceRoot()
	}
	return root
}

func loadPinnedTabSession(dir, sessionPath string) (*agent.Session, string, bool) {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" || dir == "" {
		return nil, "", false
	}
	path, _, err := validateSessionPath(dir, sessionPath)
	if err != nil {
		return nil, "", false
	}
	loaded, err := agent.LoadSession(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, path, true
		}
		return nil, "", false
	}
	return loaded, path, true
}

func saveTabSessionMeta(tab *WorkspaceTab, path string) error {
	if tab == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	m, err := agent.EnsureBranchMeta(path)
	if err != nil {
		return err
	}
	m.Scope = tab.Scope
	m.WorkspaceRoot = tab.WorkspaceRoot
	m.TopicID = tab.TopicID
	m.TopicTitle = tab.TopicTitle
	// Stamp the profile so sidecar readers (ListSessions, findTopic*) can
	// attribute the session without consulting the directory partition —
	// previously only Fork and the one-time migration wrote this field, leaving
	// most live cowork/netdev sessions unattributed.
	if p := normalizeProfileName(tab.profile); p != "" {
		m.Profile = p
	}
	if tab.IsExpertSession {
		m.ExpertTeamID = strings.TrimSpace(tab.ExpertTeamID)
	} else if m.Scope != "expert" {
		// Clear a stale team id if a session file is reused by a non-expert tab.
		m.ExpertTeamID = ""
	}
	return agent.SaveBranchMetaPreserveUpdated(path, m)
}

func canonicalTabSessionPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if validPath, _, err := validateSessionPath(config.SessionDir(), path); err == nil {
		return validPath
	}
	return path
}

func (a *App) rememberTabSessionPath(tab *WorkspaceTab, path string) {
	path = canonicalTabSessionPath(path)
	if tab == nil || path == "" {
		return
	}
	a.mu.Lock()
	if current := a.tabs[tab.ID]; current == tab {
		tab.SessionPath = path
		a.saveTabsLocked()
	} else {
		tab.SessionPath = path
	}
	a.mu.Unlock()
}

func (a *App) persistTabSessionPath(tab *WorkspaceTab, path string) {
	path = canonicalTabSessionPath(path)
	if tab == nil || path == "" {
		return
	}
	_ = saveTabSessionMeta(tab, path)
	a.rememberTabSessionPath(tab, path)
}

func (a *App) knownSessionDirs() []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		if seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, dir)
	}
	add(config.SessionDir())
	add(desktopSessionDir(globalWorkspaceRoot()))
	add(desktopSessionDirFor(globalWorkspaceRoot(), a.activeProfileKey()))
	for _, project := range loadProjectsFile(a.activeProfileKey()).Projects {
		add(desktopSessionDir(project.Root))
		add(desktopSessionDirFor(project.Root, a.activeProfileKey()))
	}
	a.mu.RLock()
	for _, tab := range a.tabs {
		add(tabSessionDir(tab))
	}
	a.mu.RUnlock()
	return out
}

// findTopicSession scans the session directory for a .jsonl file whose .meta
// carries the given topicID. Returns the most recently updated match, or ""
// if no session exists for this topic.
func findTopicSession(dir, topicID string) string {
	if topicID == "" || dir == "" {
		return ""
	}
	paths := sessionTranscriptCandidates(dir)
	var bestPath string
	var bestTime time.Time
	for _, path := range paths {
		meta, ok, err := agent.LoadBranchMeta(path)
		if err != nil || !ok {
			continue
		}
		if meta.TopicID != topicID {
			continue
		}
		if meta.UpdatedAt.After(bestTime) {
			bestTime = meta.UpdatedAt
			bestPath = path
		}
	}
	return bestPath
}

// sessionTranscriptCandidates lists candidate transcript paths in a session
// dir: flat legacy *.jsonl plus per-session-folder subdirs' *.jsonl (one
// level, newest-name order within a folder is irrelevant — findTopicSession
// picks by meta.UpdatedAt).
func sessionTranscriptCandidates(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			sub := filepath.Join(dir, e.Name())
			files, err := os.ReadDir(sub)
			if err != nil {
				continue
			}
			for _, f := range files {
				if !f.IsDir() && filepath.Ext(f.Name()) == ".jsonl" {
					paths = append(paths, filepath.Join(sub, f.Name()))
				}
			}
			continue
		}
		if filepath.Ext(e.Name()) == ".jsonl" {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	return paths
}

func (a *App) findKnownTopicSession(topicID string) (string, string) {
	for _, dir := range a.knownSessionDirs() {
		if path := findTopicSession(dir, topicID); path != "" {
			return path, dir
		}
	}
	return "", ""
}

// reviveParkedTabs rebuilds controllers for tabs that were parked in the
// Welcome state after a model change. Called by SetDefaultModel so every tab
// picks up the new default. Parked means "the build finished but left no
// controller and no error" — Ready is true in that state too, and the active
// tab carries the new model ref by the time this runs, so neither Ready nor
// model can be part of the filter.
func (a *App) reviveParkedTabs() {
	a.mu.Lock()
	var parked []*WorkspaceTab
	for _, tab := range a.tabs {
		if tab.Ctrl == nil && tab.StartupErr == "" && !tab.IsExpertSession {
			parked = append(parked, tab)
		}
	}
	a.mu.Unlock()
	for _, tab := range parked {
		a.buildTabController(tab)
	}
}
