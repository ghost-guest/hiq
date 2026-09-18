package main

// experts_app.go exposes the expert-team collaboration engine to the frontend.
// It wires desktop/experts (Team store + Orchestrator) to Wails bindings and
// provides a desktop-specific ExpertRunner that calls the LLM directly via a
// per-expert provider (auto rate-limited by the global budget decorator).
//
// Events:
//   - "experts:collab" (CollabEvent) — streamed during a run (expert chunks,
//     synthesis, completion).
//   - "experts:changed" (no payload) — team list mutated (create/update/delete).

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/boot"
	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/experts"
	"github.com/zzycxz/hiq/internal/netclient"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/tool"
	"github.com/zzycxz/hiq/internal/tool/builtin"
)

// TeamView is the JSON-friendly projection of experts.Team.
type TeamView struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Experts       []ExpertView `json:"experts"`
	DefaultMode   string       `json:"defaultMode"`
	DefaultRounds int          `json:"defaultRounds"`
	AllowSearch   bool         `json:"allowSearch"`
}
type ExpertView struct {
	Name        string `json:"name"`
	Model       string `json:"model"`
	Perspective string `json:"perspective"`
}

// ListExpertTeams returns all saved teams.
func (a *App) ListExpertTeams() []TeamView {
	if a.expertStore == nil {
		return seedBuiltinTeams()
	}
	teams := a.expertStore.List()
	if len(teams) == 0 {
		return seedBuiltinTeams()
	}
	out := make([]TeamView, 0, len(teams))
	for _, t := range teams {
		out = append(out, toTeamView(t))
	}
	return out
}

// seedBuiltinTeams returns the builtin rosters as views (shown when the store
// is empty so the panel isn't blank on first run).
func seedBuiltinTeams() []TeamView {
	out := make([]TeamView, 0, len(experts.BuiltinTeams))
	for _, t := range experts.BuiltinTeams {
		out = append(out, toTeamView(t))
	}
	return out
}

// CreateExpertTeam saves a new team and emits experts:changed.
func (a *App) CreateExpertTeam(tv TeamView) (TeamView, error) {
	if a.expertStore == nil {
		return TeamView{}, fmt.Errorf("expert store offline")
	}
	created, err := a.expertStore.Create(teamViewToModel(tv))
	if err != nil {
		return TeamView{}, err
	}
	a.emitExpertsChanged()
	return toTeamView(created), nil
}

// UpdateExpertTeam updates an existing team.
func (a *App) UpdateExpertTeam(tv TeamView) (TeamView, error) {
	if a.expertStore == nil {
		return TeamView{}, fmt.Errorf("expert store offline")
	}
	updated, err := a.expertStore.Update(tv.ID, func(t *experts.Team) {
		t.Name = tv.Name
		t.Experts = expertViewsToModel(tv.Experts)
		t.DefaultMode = tv.DefaultMode
		t.DefaultRounds = tv.DefaultRounds
		t.AllowSearch = tv.AllowSearch
	})
	if err != nil {
		return TeamView{}, err
	}
	a.emitExpertsChanged()
	return toTeamView(updated), nil
}

// DeleteExpertTeam removes a team and closes any open expert-session tab for
// it (otherwise the TabBar would show a dangling tab pointing at a deleted team).
func (a *App) DeleteExpertTeam(id string) error {
	if a.expertStore == nil {
		return fmt.Errorf("expert store offline")
	}
	a.expertStore.Delete(id)
	a.clearExpertRun(id) // drop run bookkeeping so it isn't left as stale state
	// Close any open expert-session tab for this team.
	a.mu.Lock()
	var toClose []string
	for _, tab := range a.tabs {
		if tab.IsExpertSession && tab.ExpertTeamID == id {
			toClose = append(toClose, tab.ID)
		}
	}
	a.mu.Unlock()
	for _, tid := range toClose {
		_ = a.CloseTab(tid)
	}
	a.emitExpertsChanged()
	return nil
}

// RunExpertTeam starts a collaboration. Returns the runID immediately. The run
// streams live via "experts:collab" AND is persisted into the team's own
// independent expert-session tab (created/activated here). mode/rounds override
// the team defaults when non-empty/>0.
//
// The run is tracked in expertRuns so a panel remounted after the CoWorkLayout
// was torn down (tab/profile switch) can recover the in-flight run via
// GetActiveExpertRun and re-subscribe to its stream.
func (a *App) RunExpertTeam(teamID, task, mode string, rounds int) (string, error) {
	if a.expertOrchestrator == nil {
		return "", fmt.Errorf("expert orchestrator offline")
	}
	if strings.TrimSpace(task) == "" {
		return "", fmt.Errorf("task is required")
	}
	teamName := a.teamDisplayName(teamID)
	// Open (or activate) the team's independent expert-session tab. The run's
	// full transcript persists there; the main chat is left untouched.
	meta, err := a.OpenExpertSessionTab(teamID, teamName)
	if err != nil {
		return "", err
	}
	expertTabID := meta.ID
	runID := fmt.Sprintf("run_%d", nowNano())
	// Derive from a.ctx (the wails runtime context) so a shutdown cancels a
	// long-running team instead of leaving it orphaned after close. A per-run
	// cancel is stored in expertRuns so CloseTab can cancel it when the expert
	// tab is closed mid-run.
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	a.markExpertRunStarted(runID, teamID, expertTabID, teamName, task, mode, cancel)
	go func() {
		defer cancel()
		a.runExpertTeamIntoSession(ctx, runID, expertTabID, teamID, teamName, task, mode, rounds)
	}()
	return runID, nil
}

// teamDisplayName resolves a team's display name for the persisted record. Falls
// back to the id when the team isn't found (a deleted custom team mid-run).
func (a *App) teamDisplayName(teamID string) string {
	if a.expertStore != nil {
		if t, ok := a.expertStore.Get(teamID); ok {
			return t.Name
		}
	}
	for _, t := range experts.BuiltinTeams {
		if t.ID == teamID {
			return t.Name
		}
	}
	return teamID
}

// DeleteExpertCollab removes the Nth expert-collab message (0-based among
// expert_team_collab messages) from the active tab's session — the "不采纳"
// affordance. It re-persists so the discard survives a restart. Returns the
// refreshed message history so the frontend can re-render without a separate
// HistoryForTab round-trip. An empty tabID targets the active tab.
func (a *App) DeleteExpertCollab(tabID string, ordinal int) ([]HistoryMessage, error) {
	ctrl := a.ctrlByTabID(tabID)
	if ctrl == nil {
		return nil, fmt.Errorf("no active conversation to delete from")
	}
	if err := ctrl.DeleteExpertCollab(ordinal); err != nil {
		return nil, err
	}
	// Return the refreshed history so the frontend re-renders in one step.
	return historyMessages(ctrl.History(), sessionDisplayResolver(controllerSessionDir(ctrl), ctrl.SessionPath())), nil
}

// emitExpertsCollab pushes a collaboration event to the frontend AND caches it
// into the run's streamCache so a tab that was hidden during the event can
// recover the accumulated progress when the user switches back.
func (a *App) emitExpertsCollab(ev experts.CollabEvent) {
	a.cacheCollabEvent(ev)
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "experts:collab", ev)
}

// emitExpertsChanged notifies the frontend the team list mutated.
func (a *App) emitExpertsChanged() {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "experts:changed")
}

// --- desktop ExpertRunner: calls the LLM per expert (one-shot OR mini-agent) ---

// desktopExpertRunner implements experts.ExpertRunner by building a per-expert
// provider (auto rate-limited, background priority so it respects [llm]
// reserve_main) and running one turn. It has two modes selected by allowSearch:
//
//   - allowSearch=false (default): one-shot completion. Fast and cheap — the
//     expert answers from its own knowledge with no tool calls. Used by teams
//     whose task needs no real-time data (translation, proofreading, drafting).
//   - allowSearch=true: a mini-agent loop with a read-only tool registry
//     (web_search + web_fetch) so the expert can look things up first. Slower
//     and costlier, but accurate for tasks needing current data (college majors,
//     event predictions, industry trends). MaxSteps is capped at 4 to bound
//     cost; the registry excludes every write/bash/file tool so an expert can
//     only READ the web, never touch the user's filesystem.
//
// Both modes stream text deltas to streamFn for live UI display. The search
// mode additionally surfaces each web_search call as a "🔍 搜索: <query>" line
// so the user can watch the expert research in real time.
type desktopExpertRunner struct {
	app *App
}

func (r *desktopExpertRunner) Run(ctx context.Context, model, systemPrompt, task string, allowSearch bool, streamFn func(delta string)) (string, error) {
	// Resolve the model ref to a provider entry. Empty model = use the active
	// tab's default (the user left the expert's model blank = "use default").
	entry, err := r.resolveEntry(model)
	if err != nil {
		return "", err
	}
	// Build a provider for this model. boot.NewProvider auto-wraps it with the
	// rate-limit decorator (background priority, since this runs in a team).
	// Experts run as background work, not the main agent — pass mainProvider=false
	// so they respect reserve_main and never starve the foreground conversation.
	prov, err := boot.NewProviderWithProxy(entry, netclient.ProxySpec{Mode: netclient.ModeAuto}, false)
	if err != nil {
		return "", fmt.Errorf("build provider for %s: %w", entry.Model, err)
	}
	if allowSearch {
		return r.runSearchMiniAgent(ctx, prov, entry, systemPrompt, task, streamFn)
	}
	return r.runOneShot(ctx, prov, systemPrompt, task, streamFn)
}

// runOneShot streams a single completion (system + task) — the original expert
// path. No tools, no loop. Used when the team's AllowSearch is false.
func (r *desktopExpertRunner) runOneShot(ctx context.Context, prov provider.Provider, systemPrompt, task string, streamFn func(delta string)) (string, error) {
	req := provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: systemPrompt},
			{Role: provider.RoleUser, Content: task},
		},
	}
	ch, err := prov.Stream(ctx, req)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for chunk := range ch {
		if chunk.Type == provider.ChunkText {
			b.WriteString(chunk.Text)
			if streamFn != nil {
				streamFn(chunk.Text)
			}
		}
		if chunk.Err != nil {
			return b.String(), chunk.Err
		}
	}
	return b.String(), nil
}

// runSearchMiniAgent runs the expert as a tool-calling mini-agent restricted to
// web_search + web_fetch, returning the final answer text. Text deltas and
// search calls are forwarded to streamFn so the panel shows live progress.
//
// MaxSteps caps the loop to roughly "search → read → (maybe re-search) →
// answer" so a single expert can't burn the RPM budget. We use a NEW helper
// instead of agent.RunSubAgentWithSession because the step-cap returns a hard
// "paused after N tool-call rounds" error — for an expert that's not fatal: the
// session already holds whatever the expert produced, so we recover the last
// assistant text as the answer (the orchestrator/moderator can still use a
// partial-but-researched answer better than a missing one). ContextWindow is
// taken from the provider entry so compaction engages (0 would disable it, and
// 4 steps of raw search output can blow past a model's window).
func (r *desktopExpertRunner) runSearchMiniAgent(ctx context.Context, prov provider.Provider, entry *config.ProviderEntry, systemPrompt, task string, streamFn func(delta string)) (string, error) {
	reg := r.webSearchRegistry()
	if reg == nil {
		// No search tools resolved (LookupBuiltin returned nothing) — degrade to
		// one-shot so the expert still answers instead of erroring out.
		if streamFn != nil {
			streamFn("\n（web_search 工具不可用，改为直接回答）\n")
		}
		return r.runOneShot(ctx, prov, systemPrompt, task, streamFn)
	}
	sess := agent.NewSession(systemPrompt)
	opts := agent.Options{
		MaxSteps:      8,
		ContextWindow: entry.ContextWindow, // 0 disables compaction — avoid for search loops
	}
	sink := event.FuncSink(func(e event.Event) {
		if streamFn == nil {
			return
		}
		switch e.Kind {
		case event.Text:
			// Assistant answer delta — stream verbatim.
			if e.Text != "" {
				streamFn(e.Text)
			}
		case event.ToolDispatch:
			// Surface a web_search call as a one-line marker so the user sees
			// the expert researching. web_fetch (reading a result page) is too
			// noisy to announce; only announce searches.
			if e.Tool.Name == "web_search" {
				streamFn("\n🔍 搜索: " + webSearchQueryLabel(e.Tool.Args) + "\n")
			}
		}
	})
	// An expert step's output is rendered into the collaboration transcript, so
	// it must be a real answer rather than a reasoning-only stop.
	opts.RequireVisibleFinal = true
	sub := agent.New(prov, reg, sess, opts, sink)
	runErr := sub.Run(ctx, task)
	answer := lastAssistantText(sess)
	// If the step cap fired (runErr is a "paused after N tool-call rounds"
	// error), treat it as success when we recovered a usable partial answer —
	// a researched-but-truncated expert beats a missing one in a collaboration.
	// Only propagate genuinely fatal errors (and only when we have NO text).
	if runErr != nil {
		if isMaxStepsPaused(runErr) {
			// The step cap fired. If we have a partial answer, use it; otherwise
			// return a graceful note instead of leaking the raw English error to
			// the user. A missing-but-tried expert is better than a crash.
			if answer == "" {
				answer = "（已达搜索步数上限，未能完成回答）"
			}
			if streamFn != nil {
				streamFn("\n（已达搜索步数上限，基于已查到的信息作答）\n")
			}
			return answer, nil
		}
		return "", fmt.Errorf("search mini-agent: %w", runErr)
	}
	if answer == "" {
		return "", fmt.Errorf("search mini-agent finished without producing an answer")
	}
	return answer, nil
}

// lastAssistantText returns the last assistant message with non-empty text in
// the session (the mini-agent's latest partial/final answer). Mirrors the
// extraction in agent.RunSubAgentWithSession but is reusable when Run errored.
func lastAssistantText(sess *agent.Session) string {
	if sess == nil {
		return ""
	}
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		m := sess.Messages[i]
		if m.Role == provider.RoleAssistant && strings.TrimSpace(provider.ContentString(m.Content)) != "" {
			return provider.ContentString(m.Content)
		}
	}
	return ""
}

// isMaxStepsPaused reports whether err is the agent's step-cap "paused" error
// (vs. a genuine failure: provider error, context cancel, etc.). We key off
// the message text because the error is constructed inline in agent.Run without
// a typed sentinel — string-matching is the established pattern here.
func isMaxStepsPaused(err error) bool {
	return err != nil && strings.Contains(err.Error(), "paused after")
}

// webSearchRegistry builds (once, cached) a read-only tool registry containing
// only web_search + web_fetch. Returns nil if neither builtin resolves — the
// caller then degrades to one-shot. The registry is process-shared and never
// mutated after build, so caching it behind sync.Once is safe.
var (
	webSearchRegOnce sync.Once
	webSearchReg     *tool.Registry
)

func (r *desktopExpertRunner) webSearchRegistry() *tool.Registry {
	webSearchRegOnce.Do(func() {
		reg := tool.NewRegistry()
		added := 0
		if t, ok := tool.LookupBuiltin("web_search"); ok {
			reg.Add(t)
			added++
		}
		if t, ok := tool.LookupBuiltin("web_fetch"); ok {
			reg.Add(t)
			added++
		}
		if added == 0 {
			webSearchReg = nil // signal "unavailable" to the caller
			return
		}
		webSearchReg = reg
	})
	return webSearchReg
}

// webSearchQueryLabel extracts a short human-readable query from a web_search
// tool's raw JSON args, for the "🔍 搜索: ..." UI marker. Best-effort: on any
// parse failure it falls back to the raw args so the user still sees something.
func webSearchQueryLabel(rawArgs string) string {
	var p struct {
		Query string `json:"query"`
		Q     string `json:"q"`
	}
	if json.Unmarshal([]byte(rawArgs), &p) == nil {
		if p.Query != "" {
			return p.Query
		}
		if p.Q != "" {
			return p.Q
		}
	}
	if len(rawArgs) > 60 {
		return rawArgs[:60] + "…"
	}
	return rawArgs
}

// resolveEntry finds the provider entry for a model ref (e.g. "deepseek/r1").
// Empty ref → the configured default model. We prefer cfg.DefaultModel (the
// "默认模型" the user picked on the settings page) since that's the intended
// fallback; if it's unset we fall back to the first provider that is BOTH
// configured (has an API key) AND has a non-empty default model. Configured()
// alone is not enough — a provider can have a key but no model filled in, which
// would build a provider then fail at request time with "model is required".
func (r *desktopExpertRunner) resolveEntry(modelRef string) (*config.ProviderEntry, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	modelRef = strings.TrimSpace(modelRef)
	if modelRef == "" {
		// Preferred fallback: the user's configured default model.
		if dm := strings.TrimSpace(cfg.DefaultModel); dm != "" {
			if entry, ok := cfg.ResolveModel(dm); ok && entry.Configured() {
				return entry, nil
			}
		}
		// Otherwise: first configured provider that actually has a usable model.
		for i := range cfg.Providers {
			if cfg.Providers[i].Configured() && cfg.Providers[i].DefaultModel() != "" {
				entry := cfg.Providers[i]
				if entry.Model == "" {
					entry.Model = entry.DefaultModel()
				}
				return &entry, nil
			}
		}
		return nil, fmt.Errorf("no usable model available — set a default model on the 设置 page, or fill the model field on a provider")
	}
	entry, ok := cfg.ResolveModel(modelRef)
	if !ok {
		return nil, fmt.Errorf("unknown model %q — add it under [[providers]] in config", modelRef)
	}
	return entry, nil
}

// --- helpers ---

func toTeamView(t experts.Team) TeamView {
	evs := make([]ExpertView, 0, len(t.Experts))
	for _, e := range t.Experts {
		evs = append(evs, ExpertView{Name: e.Name, Model: e.Model, Perspective: e.Perspective})
	}
	return TeamView{ID: t.ID, Name: t.Name, Experts: evs, DefaultMode: t.DefaultMode, DefaultRounds: t.DefaultRounds, AllowSearch: t.AllowSearch}
}

func teamViewToModel(tv TeamView) experts.Team {
	return experts.Team{
		ID:            tv.ID,
		Name:          tv.Name,
		Experts:       expertViewsToModel(tv.Experts),
		DefaultMode:   tv.DefaultMode,
		DefaultRounds: tv.DefaultRounds,
		AllowSearch:   tv.AllowSearch,
	}
}

func expertViewsToModel(evs []ExpertView) []experts.Expert {
	out := make([]experts.Expert, 0, len(evs))
	for _, ev := range evs {
		out = append(out, experts.Expert{Name: ev.Name, Model: ev.Model, Perspective: ev.Perspective})
	}
	return out
}

// initExperts creates the team store + orchestrator at startup.
func (a *App) initExperts() {
	path := filepath.Join(desktopConfigDir(), "expert_teams.json")
	store, err := experts.NewStore(path)
	if err != nil {
		return
	}
	a.expertStore = store
	// Backfill any missing builtin teams. This is idempotent: for each builtin,
	// we only insert when its stable ID is absent from the store. This lets new
	// builtins ship in a release and automatically appear for existing users
	// (whose expert_teams.json already pins the earlier builtins), without
	// clobbering teams they edited or deleted. Runs every startup; the Get+Create
	// guard makes repeat runs a no-op.
	seedBuiltinTeamsInto(store)
	a.expertOrchestrator = experts.NewOrchestrator(store, &desktopExpertRunner{app: a}, a.emitExpertsCollab)
	// Inject RAG searcher if available.
	if a.ragStore != nil {
		a.expertOrchestrator.SetRAGSearcher(&ragSearcherAdapter{app: a})
	}
	// Apply the RAG master switch ([cowork] rag_enabled). When disabled, the
	// orchestrator skips knowledge-base injection even though a searcher is set,
	// so expert teams honour the user's global "knowledge base off" choice.
	if cfg, err := config.Load(); err == nil {
		a.expertOrchestrator.SetRAGEnabled(cfg.Cowork.RAGEnabledOrDefault())
	}
	// Bind the engine to the expert_team_* tools (registered under cowork in
	// boot.go). Mirrors how initRAG/initScheduler bind SetRAGStore/SetScheduler.
	builtin.SetExpertStore(store)
	builtin.SetExpertOrchestrator(a.expertOrchestrator)
}

// seedBuiltinTeamsInto inserts any builtin team whose stable ID is not yet in
// the store. It is the back-half of the 2→8 builtin expansion: old installs
// already have builtin_review + builtin_brainstorm persisted, so the previous
// "only seed when store is empty" guard would never surface the 6 newer teams.
// By keying on ID and skipping existing rows we (a) never duplicate a team,
// (b) never overwrite a user's edits to an existing builtin, and (c) leave a
// deleted builtin gone (respecting user intent) — it only comes back if the
// user wipes the whole store. Safe to call repeatedly.
func seedBuiltinTeamsInto(store *experts.Store) {
	for _, t := range experts.BuiltinTeams {
		if _, exists := store.Get(t.ID); exists {
			continue
		}
		_, _ = store.Create(t)
	}
}

func nowNano() int64 {
	return time.Now().UnixNano()
}

// ragSearcherAdapter adapts the RAG store to the experts.RAGSearcher interface.
type ragSearcherAdapter struct {
	app *App
}

func (a *ragSearcherAdapter) Search(collection, query string, topK int) (string, error) {
	if a.app.ragStore == nil {
		return "", nil
	}
	if topK <= 0 {
		topK = 3
	}
	// Search entities + relations + FTS5 snippets and format as context.
	hits, err := a.app.RagSearch(collection, query, topK)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range hits.Entities {
		b.WriteString(fmt.Sprintf("- %s (%s): %s\n", e.Name, e.Type, e.Description))
	}
	for _, r := range hits.Relations {
		b.WriteString(fmt.Sprintf("- %s → [%s] → %s", r.Source, r.Type, r.Target))
		if r.Description != "" {
			b.WriteString(fmt.Sprintf(": %s", r.Description))
		}
		b.WriteString("\n")
	}
	for _, s := range hits.Snippets {
		b.WriteString(fmt.Sprintf("- [%s] %s\n", s.Path, s.Snippet))
	}
	content := b.String()
	if content == "" {
		return "", nil
	}
	// Wrap in <untrusted_content> so prompt injection hidden in imported
	// documents cannot hijack expert behavior. The expert system prompt
	// (buildExpertPrompt) instructs the model to treat this tag as DATA only.
	// Use builtin.WrapUntrusted (not a hand-rolled tag) so the content is
	// sanitized — a literal </untrusted_content> embedded in a document
	// cannot close the fence early and inject expert instructions.
	return builtin.WrapUntrusted("rag", content), nil
}
