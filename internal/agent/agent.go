package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/zzycxz/hiq/internal/diff"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/evidence"
	"github.com/zzycxz/hiq/internal/instruction"
	"github.com/zzycxz/hiq/internal/jobs"
	"github.com/zzycxz/hiq/internal/memory"
	"github.com/zzycxz/hiq/internal/nilutil"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/tool"
)

// maxToolOutputBytes caps a single tool result before it goes into the model's
// context. ~32KB is roughly 8K tokens — enough for a full file read or a busy
// grep, while preventing one accidental "read this 5 MB log" from blowing the
// window before the next compaction runs.
const maxToolOutputBytes = 32 * 1024

const maxFinalReadinessBlocks = 3
const maxEmptyFinalBlocks = 3
const maxStreamRecoveries = 1

// Runner executes one task turn. *Agent satisfies it; the controller and compose
// hold a Runner so they're agnostic to the concrete executor. (The two-model
// Coordinator that previously also satisfied Runner has been removed — hiq
// uses a single-model planner-executor path exclusively. The interface stays so
// callers don't depend on the concrete *Agent type.)
type Runner interface {
	Run(ctx context.Context, input any) error
}

// Renderer redraws the assistant's final-answer text as styled output. It is
// applied only after a turn's text stream completes, so the user sees raw
// markdown stream live, then a single redraw replaces it with formatted
// output. The renderer is intentionally interface-shaped so the agent stays
// independent of the cli's markdown library choice. Consumed by TextSink.
type Renderer interface {
	Render(text string) string
}

// Asker puts structured multiple-choice questions to the user and blocks for the
// answers. The agent consults it for the `ask` tool. It is interface-shaped so
// the agent stays independent of the frontend; a nil asker means no interactive
// user (headless runs), where `ask` returns a "decide for yourself" result. The
// interactive frontends wire the controller in as the Asker.
type Asker interface {
	Ask(ctx context.Context, questions []event.AskQuestion) ([]event.AskAnswer, error)
}

// callContextKey carries the executing tool call's identity into Execute.
type callContextKey struct{}
type parentSessionContextKey struct{}

// callContext is the per-call context a tool can read. parentID is the call being
// executed and sink is the agent's event sink (the `task` tool uses both to nest
// a sub-agent's events under this call); asker lets the `ask` tool reach the user.
type callContext struct {
	parentID string
	sink     event.Sink
	asker    Asker
}

// withCallContext stamps ctx with the executing call's ID, the agent's sink, and
// the asker. executeOne sets this before every Execute; `task` reads it (via
// CallContext) to nest sub-agent events, and `ask` reads the asker to prompt.
func withCallContext(ctx context.Context, parentID string, sink event.Sink, asker Asker) context.Context {
	return context.WithValue(ctx, callContextKey{}, callContext{parentID: parentID, sink: sink, asker: asker})
}

// CallContext returns the executing call's ID, the agent's sink, and the asker,
// if the context was set by an agent's executeOne. ok is false for a plain
// context (headless tool tests, calls made outside the run loop).
func CallContext(ctx context.Context) (parentID string, sink event.Sink, asker Asker, ok bool) {
	cc, ok := ctx.Value(callContextKey{}).(callContext)
	if !ok {
		return "", nil, nil, false
	}
	return cc.parentID, cc.sink, cc.asker, true
}

// WithParentSession stamps the active parent session ID onto a turn context so
// persisted sub-agents can record and enforce their owning conversation.
func WithParentSession(ctx context.Context, parentSession string) context.Context {
	return context.WithValue(ctx, parentSessionContextKey{}, strings.TrimSpace(parentSession))
}

// ParentSession returns the active parent session ID carried by a turn context.
func ParentSession(ctx context.Context) string {
	parentSession, _ := ctx.Value(parentSessionContextKey{}).(string)
	return strings.TrimSpace(parentSession)
}

// Gate decides, per tool call, whether it may run. The agent consults it at
// execute time (after the plan-mode gate). It is interface-shaped so the agent
// stays independent of the permission package and of how "ask" is resolved
// (silently in headless runs, interactively in the chat TUI). A nil gate means
// no gating — every call runs, preserving behaviour for callers that don't wire
// one in. reason is fed back to the model when allow is false; a non-nil err
// (e.g. ctx cancelled awaiting approval) is treated as a block for that call.
type Gate interface {
	Check(ctx context.Context, toolName string, args json.RawMessage, readOnly bool) (allow bool, reason string, err error)
}

// ToolHooks fires user-configured shell hooks around each tool call. PreToolUse
// runs before the call and may block it (block=true; message is the reason fed
// back to the model); PostToolUse runs after and only surfaces output to the
// user (it can't block). It is interface-shaped so the agent stays independent
// of the hook package — a nil hooks field disables hook firing entirely.
type ToolHooks interface {
	PreToolUse(ctx context.Context, name string, args json.RawMessage) (block bool, message string)
	PostToolUse(ctx context.Context, name string, args json.RawMessage, result string)
	// PostLLMCall fires after each model turn completes (streaming finishes)
	// but before reasoning_content is stored. It returns the (possibly
	// translated) reasoning string — the original when no hook is configured.
	// HasPostLLMCall reports whether such a hook exists, so the agent keeps
	// streaming reasoning live when none is wired up.
	PostLLMCall(ctx context.Context, reasoning string, turn int) string
	HasPostLLMCall() bool
	// SubagentStop fires when a `task` sub-agent finishes (foreground). PreCompact
	// fires just before a compaction pass and returns extra summary guidance (its
	// hooks' stdout) to fold into the summary prompt; "" when no hook contributes.
	SubagentStop(ctx context.Context, last string)
	PreCompact(ctx context.Context, trigger string) string
}

// Agent drives a single task: a Provider, a tool Registry, and a Session wired
// into the main loop.
type Agent struct {
	prov        provider.Provider
	tools       *tool.Registry
	session     *Session
	sessMu      sync.Mutex // guards the session pointer for external Session()/SetSession
	maxSteps    int
	maxStepsKey string
	temperature float64
	pricing     *provider.Pricing

	// modelRef is the canonical "provider/model" label for usage events and the
	// prompt-cache key. Empty when the caller did not supply one.
	modelRef string
	// requireVisibleFinal, when set, retries a no-tool turn that produced no
	// visible text instead of accepting it as the final answer.
	requireVisibleFinal bool
	// continuationPolicy opts this agent into a host-owned same-Run
	// continuation flow. Reserved: the current run loop never branches on it,
	// matching upstream, where nothing reads it either.
	continuationPolicy ContinuationPolicy
	// strictAlternatingRoles coalesces adjacent user turns on the provider
	// projection so strict providers (which reject two user messages in a row)
	// accept a repaired history.
	strictAlternatingRoles bool

	// sink receives the turn's typed event stream (reasoning/text deltas, tool
	// dispatch/results, usage, notices). The agent no longer formats output
	// itself — a frontend's Sink decides how to render. Never nil; New defaults
	// it to event.Discard.
	sink event.Sink

	// lastUsage caches the most recent per-turn telemetry the provider reported so
	// the CLI can expose a context gauge without re-scraping the usage line. The
	// run loop writes it while a frontend's status line reads it, so it is atomic.
	lastUsage atomic.Pointer[provider.Usage]

	// sessCacheHit/sessCacheMiss accumulate cache tokens across every API call
	// this session, so frontends can show the aggregate hit-rate (Σhit/Σ(hit+miss))
	// — a steadier, cost-oriented number than the single-turn rate. They are NOT
	// reset on compaction (compaction only rewrites session.Messages), so the
	// aggregate never craters when the prefix is summarized away. Atomic: the run
	// loop accumulates them while the status line reads them. Some providers do
	// not report cache tokens, so these stay at 0 for providers that omit cache stats.
	sessCacheHit  atomic.Int64
	sessCacheMiss atomic.Int64

	// lastPrefixShape records the previous provider request's cacheable prefix
	// so usage events can explain prefix churn on the next request.
	lastPrefixShape     PrefixShape
	haveLastPrefixShape bool

	// planMode, when true, refuses any tool call whose ReadOnly() is false.
	// The system prompt and tool list never change with the toggle so the
	// prompt-cache prefix stays valid; the gating happens at execute time
	// and the model sees a "blocked" result it can adapt to. Toggled from
	// the outside via SetPlanMode.
	planMode atomic.Bool

	// skipReadiness, when true, disables the final-answer readiness gate
	// (finalReadinessCheck) for the current Run. compose sets this during its
	// implement/verify/review phases so the gate doesn't force "finish
	// everything in one turn" and conflict with compose's own retry loop.
	// Toggled via SetSkipReadiness; default false preserves existing behavior.
	skipReadiness atomic.Bool

	// gate, when non-nil, is the per-call permission gate consulted after the
	// plan-mode check. nil disables gating entirely.
	gate Gate

	// hooks, when non-nil, fires PreToolUse / PostToolUse shell hooks around each
	// tool call. nil disables hook firing.
	hooks ToolHooks

	// asker, when non-nil, lets the `ask` tool put questions to the user. nil in
	// headless runs (no interactive user). Set via SetAsker.
	asker Asker

	// onPreEdit, when non-nil, is called with a writer tool's previewed change
	// just before it runs — the seam the checkpoint store uses to snapshot a
	// file's pre-edit content. Only fires for non-ReadOnly tools that implement
	// tool.Previewer (so bash, whose targets are unknowable, is never tracked).
	// Set via SetPreEditHook.
	onPreEdit func(diff.Change)

	// onPostEdit, when non-nil, is called with each previewed path after the
	// writer tool succeeded — the seam the checkpoint store uses to record the
	// file's post-edit content hash, so a rewind preview can tell agent edits
	// from external changes made since. Set via SetPostEditHook.
	onPostEdit func(string)

	// contextFilter, when non-nil, projects the session messages into the
	// context sent to the model — a read-side transform that can rewrite or
	// summarize specific messages (e.g. swap a full expert-collab transcript
	// for its synthesis-only view) without mutating the stored session. nil
	// means the raw session messages go through verbatim. Set via
	// SetContextFilter.
	contextFilter func([]provider.Message) []provider.Message

	// jobs, when non-nil, is the session's background-job manager. executeOne
	// stamps it onto each tool call's context so the background tools (bash
	// run_in_background, task run_in_background, background_job) can
	// reach it. nil leaves those tools to degrade gracefully.
	jobs *jobs.Manager

	// cacheKey routes provider prefix-cache affinity for this conversation
	// (OpenAI prompt_cache_key). Set from the session path by the controller.
	cacheKey string

	// steerQueue holds mid-turn user messages queued while the agent is
	// running. Each is consumed once per loop iteration, persisted to the
	// session for history replay, and sent to the model as guidance (not a
	// new task). Cache miss for the next API call is unavoidable but limited
	// to one call — the prefix stays stable otherwise.
	steerMu       sync.Mutex
	steerQueue    []string
	steerConsumed bool

	// followUpQueue holds prompts queued while running that should run as
	// INDEPENDENT next turns once the current one finishes (upgrade spec 2-4)
	// — the pi "followUp" semantics, in contrast to steer's "guide the current
	// task". The controller drains it between turns: when a Run ends, the
	// first queued prompt starts a fresh turn automatically.
	followUpMu    sync.Mutex
	followUpQueue []string

	// pauseState drives graceful pause/resume. Pause closes pauseCh (non-nil =>
	// a pause is requested); the run loop blocks on resumeCh at the top of each
	// step until Resume closes it. Both are recreated per Run so a stale signal
	// from a previous turn can't affect the next. Pause is distinct from Cancel:
	// Cancel aborts the in-flight LLM call and discards partial work; Pause lets
	// the current step finish, then freezes the agent with full state intact so
	// a Resume continues from exactly where it stopped. Guarded by pauseMu so
	// Pause/Resume/IsPaused can be called from any goroutine safely.
	pauseMu  sync.Mutex
	pauseCh  chan struct{} // closed = pause requested; recreated per Run
	resumeCh chan struct{} // closed = resume signaled; recreated per pause
	paused   bool          // true while blocked on resumeCh (for IsPaused)

	// evidence is a per-user-turn ledger of host-observed tool receipts. It lets
	// complete_step validate that cited evidence happened before the claim.
	evidence *evidence.Ledger

	// todoState is the host's canonical task list: the latest successful
	// todo_write with completions applied by complete_step. Unlike the per-turn
	// ledger it survives turn boundaries and compaction (it never rides in the
	// prompt), so the final-answer gate still sees an unfinished plan a later
	// turn would otherwise hide. Rebuilt from the session in SetSession.
	todoMu    sync.Mutex
	todoState []evidence.TodoItem

	// hostAdvanceSeq guarantees unique tool IDs across turns: every
	// emitTodoState call increments it so the frontend always sees a fresh
	// dispatch even when the same panel index is signed off in different turns.
	hostAdvanceSeq atomic.Int64

	// projectChecks are structured project instructions that complete_step can
	// verify against same-turn bash receipts after a write-backed completion.
	projectChecks []instruction.VerifyCheck

	// memQueue, when non-nil, lets the remember/forget tools fold a turn-tail note
	// about a just-made memory change into the next turn, so it applies this
	// session without touching the cache-stable prefix. Set via SetMemoryQueue.
	memQueue memory.Queue

	// Context management: when a turn's prompt nears contextWindow, the older
	// middle of the session is summarized away, keeping a token-bounded recent
	// tail verbatim (recentKeep is the message floor) and archiving the originals
	// under archiveDir. compactStuck latches when compaction can't get the prompt
	// under the window (consecutiveCompacts crosses the limit), so auto-compaction
	// pauses instead of looping. softCompactNoticed gates the one-shot soft-ratio
	// notice so it fires once per approach, not every turn.
	contextWindow       int
	contextBudgetFrac   float64 // ContextBudgetPercent / 100; 1.0 = full window
	softCompactRatio    float64
	compactRatio        float64
	compactForceRatio   float64
	softCompactNoticed  bool
	recentKeep          int
	archiveDir          string
	compactStuck        bool
	consecutiveCompacts int
	// Standby trim (no-window sessions): with contextWindow unset nothing ever
	// compacts, so Session.Messages keeps every full tool output in memory for
	// the life of the tab. standbyTrimClock rate-limits the memory-only
	// soft-trim to once per standbyTrimEvery turns; standbyTrimNoticed fires
	// the one-shot explanatory notice.
	standbyTrimClock   int
	standbyTrimNoticed bool

	// stormSig / stormCount track a run of turns that keep failing the same way so
	// the loop can break a death-spiral. The signature is each call's (tool, error)
	// in order, NOT (tool, args): a stuck model reliably reworks the arguments
	// cosmetically (a re-worded essay, a reordered object) while the call fails
	// identically every time — keying on args misses the loop entirely (observed
	// live against truncated tool-call arguments). Because errors that embed their
	// subject (e.g. "file not found: /x") differ per target, genuine varied probing
	// does not collapse to one signature. Reset whenever a turn does anything else
	// (a different failure shape, or any success). See applyStormBreaker.
	stormSig   string
	stormCount int

	// repeatSuccessCounts tracks write-like tool calls that have already
	// succeeded in this user turn. This catches the complementary loop shape to
	// stormSig: a model keeps doing the same successful write, so there is no
	// error for the failure-only storm breaker to see.
	repeatSuccessCounts map[string]int
	// repeatText detects streamed-output repetition (the same passage emitted
	// over and over) within a single answer. Advisory only: it surfaces a
	// Notice on detection, it never aborts the turn — distinct from the
	// tool-level guards above, which key on tool calls, not narrated text.
	repeatText       repeatTextMonitor
	repeatTextWarned bool
	// opGate is the operation-level failure-recovery gate (SPEC v2 §3.1): stops
	// an agent from looping on the same failing write/command. A third guard
	// alongside stormSig (batch-tail, name+error) and repeatSuccessCounts
	// (write success). Keys on a name+args fingerprint so the exact failing op is
	// stopped while unrelated work continues. nil disables (default: on via
	// newOpGate in Options). Pure host logic — the model never sees it.
	opGate *opGate
}

// SetPlanMode flips the read-only gate. While true, executeOne refuses any
// non-ReadOnly tool the model calls and returns a "blocked" result instead of
// running it. The cache-friendly bits — system prompt, tools schema, message
// history — are left untouched, so the toggle costs nothing in cache hits.
func (a *Agent) SetPlanMode(v bool) { a.planMode.Store(v) }

// SetSkipReadiness toggles the final-answer readiness gate. compose uses this
// to suppress the gate during its phased implement/verify/review runs, where
// the gate would otherwise force the model to complete all todos in a single
// turn or hard-error after maxFinalReadinessBlocks. See audit finding C3.
func (a *Agent) SetSkipReadiness(v bool) { a.skipReadiness.Store(v) }

// planModeReadOK reports whether a tool call may proceed under plan mode.
// Statically read-only tools (ReadOnly()==true) always pass. A tool that
// implements tool.ReadOnlyCallChecker gets a per-call vote so a nominally
// writer tool can still let a specific read-only invocation through — bash is
// the reason this exists: "git log" and "find ." are safe, "rm" and "> file"
// are not, and the static ReadOnly() can't tell them apart.
func planModeReadOK(t tool.Tool, args json.RawMessage) bool {
	if t.ReadOnly() {
		return true
	}
	if rc, ok := t.(tool.ReadOnlyCallChecker); ok {
		return rc.ReadOnlyCall(args)
	}
	return false
}

// SetGate installs the per-call permission gate. Used by `hiq chat` to swap the
// headless gate built in setup for an interactive one that prompts the user;
// nil disables gating. Safe to call before the run loop starts.
func (a *Agent) SetGate(g Gate) {
	if nilutil.IsNil(g) {
		g = nil
	}
	a.gate = g
}

// Provider returns the agent's LLM provider, available for auxiliary calls
// such as the goal judge.
func (a *Agent) Provider() provider.Provider { return a.prov }

// SetAsker installs the asker the `ask` tool uses to question the user.
// Interactive frontends wire one in; headless runs leave it nil.
func (a *Agent) SetAsker(as Asker) { a.asker = as }

// SetMemoryQueue installs the sink the remember/forget tools use to apply a
// memory change in the current session. The controller wires itself in.
func (a *Agent) SetMemoryQueue(q memory.Queue) { a.memQueue = q }

// SetPreEditHook installs the pre-edit snapshot hook (see onPreEdit). The
// controller wires it to its per-session checkpoint store; nil disables capture.
func (a *Agent) SetPreEditHook(fn func(diff.Change)) { a.onPreEdit = fn }

// SetPostEditHook installs the post-edit hash hook (see onPostEdit). The
// controller wires it to its checkpoint store's NotePostEdit; nil disables the
// rewind safety classification.
func (a *Agent) SetPostEditHook(fn func(string)) { a.onPostEdit = fn }

// PreviewToolCall computes the per-file changes the named tool call would make,
// for the approval card a frontend renders before the permission decision.
// ok is false for read-only, unknown, or unpreviewable tools — callers render
// the plain subject in that case. It never writes.
func (a *Agent) PreviewToolCall(name string, args json.RawMessage) ([]diff.Change, bool) {
	t, ok := a.tools.Get(name)
	if !ok {
		return nil, false
	}
	return tool.PreviewChanges(t, args)
}

// SetContextFilter installs a read-side transform applied to session messages
// before they're sent to the model. It lets a caller (e.g. the experts engine)
// keep a full-fidelity message in the transcript while showing the model a
// compact projection of it, so the context window isn't bloated. nil (the
// default) passes messages through unchanged.
func (a *Agent) SetContextFilter(fn func([]provider.Message) []provider.Message) {
	a.contextFilter = fn
}

// Session returns the agent's current conversation, useful for persistence
// hooks that need to read the message log between turns. sessMu serialises this
// pointer read against SetSession, so a frontend (serve's concurrent /history and
// /new handlers) can't race the swap. The run loop touches a.session directly and
// only swaps it via SetSession while idle, so its reads need no lock.
func (a *Agent) Session() *Session {
	a.sessMu.Lock()
	defer a.sessMu.Unlock()
	return a.session
}

// SetSession replaces the agent's conversation wholesale. Used by
// `hiq chat --resume` to load a saved JSONL transcript before the first turn,
// so the model picks up exactly where it left off. Callers serialise it against a
// running turn (it only fires while idle); sessMu guards the pointer swap itself.
func (a *Agent) SetSession(s *Session) {
	a.sessMu.Lock()
	a.session = s
	a.sessMu.Unlock()
	if s != nil {
		a.rebuildTodoState(s.Snapshot())
	}
}

// LastUsage returns the most recent per-turn token telemetry the provider
// reported (nil if no turn has run yet). The TUI uses it to show a context
// gauge alongside the prompt; the actual cache decisions still live inside
// maybeCompact.
func (a *Agent) LastUsage() *provider.Usage { return a.lastUsage.Load() }

// SessionCache returns the cumulative cache hit/miss prompt tokens across every
// API call this session — the basis for the status line's aggregate hit-rate.
func (a *Agent) SessionCache() (hit, miss int) {
	return int(a.sessCacheHit.Load()), int(a.sessCacheMiss.Load())
}

// ContextWindow returns the configured context-window size in tokens. 0
// means compaction is disabled for this agent.
func (a *Agent) ContextWindow() int { return a.contextWindow }

// effectiveContextWindow returns the window the agent actually treats as
// available for compaction decisions: contextWindow scaled by the user's
// context-budget percentage (SPEC v2 §3.6). A budget of 80% makes compaction
// trigger as if the window were 20% smaller — saving cost on pricing tiers
// and avoiding quality drop near the edge. When no budget is set (frac=1.0),
// this equals contextWindow, so default behavior is unchanged.
func (a *Agent) effectiveContextWindow() int {
	if a.contextWindow <= 0 {
		return 0
	}
	frac := a.contextBudgetFrac
	if frac <= 0 || frac > 1 {
		frac = 1
	}
	return int(float64(a.contextWindow) * frac)
}

// mid-turn steer marker.
const midTurnSteerPrefix = "[Mid-turn steer queued by the user. Do not treat this as a new task; use it only as additional guidance for the current task after completing the current step.]"

func midTurnSteerMessage(text string) string {
	return midTurnSteerPrefix + "\n" + text
}

// SetCacheKey installs the provider cache-affinity key (hash of the session
// path); empty disables explicit routing.
func (a *Agent) SetCacheKey(key string) { a.cacheKey = key }

// ModelRef returns the canonical "provider/model" ref this agent's turns are
// attributed to, or "" when the caller did not supply one. hiq's event
// stream carries no model-ref field yet, so this is currently consumed by
// callers that label their own diagnostics; upstream Reasonix threads the same
// value into usage events and the prompt-cache key.
func (a *Agent) ModelRef() string { return a.modelRef }

// Steer queues a message for mid-turn injection.
func (a *Agent) Steer(text string) {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	a.steerQueue = append(a.steerQueue, text)
	a.steerConsumed = false
}

// SteerConsumed returns true when the steer queue became empty after the last consume.
func (a *Agent) SteerConsumed() bool {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	return a.steerConsumed
}

// FollowUp queues a prompt to run as an independent next turn once the current
// Run finishes (contrast Steer, which guides the in-flight task). The
// controller drains the queue between turns.
func (a *Agent) FollowUp(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	a.followUpMu.Lock()
	defer a.followUpMu.Unlock()
	a.followUpQueue = append(a.followUpQueue, text)
}

// DrainFollowUp pops the oldest queued follow-up prompt, if any.
func (a *Agent) DrainFollowUp() (string, bool) {
	a.followUpMu.Lock()
	defer a.followUpMu.Unlock()
	if len(a.followUpQueue) == 0 {
		return "", false
	}
	next := a.followUpQueue[0]
	a.followUpQueue = a.followUpQueue[1:]
	return next, true
}

// FollowUps peeks the queued follow-up prompts (for the UI's queue strip).
func (a *Agent) FollowUps() []string {
	a.followUpMu.Lock()
	defer a.followUpMu.Unlock()
	return append([]string(nil), a.followUpQueue...)
}

// Steers peeks the queued steer messages (for the UI's queue strip).
func (a *Agent) Steers() []string {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	return append([]string(nil), a.steerQueue...)
}

func (a *Agent) consumeSteer() (string, bool) {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	if len(a.steerQueue) == 0 {
		return "", false
	}
	t := a.steerQueue[0]
	a.steerQueue = a.steerQueue[1:]
	a.steerConsumed = len(a.steerQueue) == 0
	return t, true
}

func (a *Agent) clearSteerQueue() {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	a.steerQueue = nil
	a.steerConsumed = false
}

func (a *Agent) steerQueueLen() int {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	return len(a.steerQueue)
}

// Pause requests a graceful pause. The run loop finishes the current step (it
// does NOT interrupt an in-flight LLM call), then blocks at the top of the next
// iteration until Resume is called. State — session, todos, history — is fully
// preserved, so Resume continues from exactly where the agent stopped. Calling
// Pause when no run is active, or when already paused, is a no-op. Idempotent
// and safe to call from any goroutine.
func (a *Agent) Pause() {
	a.pauseMu.Lock()
	defer a.pauseMu.Unlock()
	if a.pauseCh == nil || a.paused {
		return
	}
	select {
	case <-a.pauseCh:
		// already requested
	default:
		close(a.pauseCh)
	}
}

// Resume unblocks a paused run. If the run isn't paused (or none is active),
// it's a no-op. Safe from any goroutine.
func (a *Agent) Resume() {
	a.pauseMu.Lock()
	defer a.pauseMu.Unlock()
	if a.resumeCh == nil {
		return
	}
	select {
	case <-a.resumeCh:
		// already resumed
	default:
		close(a.resumeCh)
	}
}

// IsPaused reports whether the agent is currently blocked on a pause (between
// steps, awaiting Resume). False when running normally or not running at all.
func (a *Agent) IsPaused() bool {
	a.pauseMu.Lock()
	defer a.pauseMu.Unlock()
	return a.paused
}

// resetPauseStateLocked recreates the pause/resume channels for a fresh run.
// Caller MUST hold pauseMu. A stale pauseCh from the previous turn would
// otherwise immediately re-trigger a pause; recreating both channels means each
// Run starts unpaused and a Pause must be issued afresh.
func (a *Agent) resetPauseStateLocked() {
	a.pauseCh = make(chan struct{})
	a.resumeCh = make(chan struct{})
	a.paused = false
}

// awaitPause blocks at the top of a loop iteration if a pause has been
// requested. It emits a Paused event before blocking and a Resumed event after
// unblocking, so a frontend can reflect the frozen state. Returns the context
// error if the run is cancelled while paused (the only way out besides Resume).
func (a *Agent) awaitPause(ctx context.Context) error {
	a.pauseMu.Lock()
	ch := a.pauseCh
	a.pauseMu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		// pause requested — fall through to block
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil // no pause requested
	}
	// We are pausing. Emit, set the flag, block on resume (or cancellation).
	a.pauseMu.Lock()
	a.paused = true
	resume := a.resumeCh
	a.pauseMu.Unlock()
	a.sink.Emit(event.Event{Kind: event.Paused, Text: "paused between steps — state preserved"})
	select {
	case <-resume:
		a.pauseMu.Lock()
		a.paused = false
		a.pauseMu.Unlock()
		a.sink.Emit(event.Event{Kind: event.Resumed})
		return nil
	case <-ctx.Done():
		a.pauseMu.Lock()
		a.paused = false
		a.pauseMu.Unlock()
		return ctx.Err()
	}
}

// CompactRatio returns the fraction of the window at which auto-compaction
// fires (e.g. 0.8). The status line uses it to show headroom to the next compact.
func (a *Agent) CompactRatio() float64 { return a.compactRatio }

// CompactNow runs one compaction pass immediately, regardless of the
// usage-ratio threshold maybeCompact normally honours. Used by the chat
// TUI's `/compact` command so the user can reset the prefix before it
// naturally fills up.
func (a *Agent) CompactNow(ctx context.Context, instructions string) error {
	return a.compact(ctx, "manual", instructions, true)
}

// Options configures an Agent.
type Options struct {
	MaxSteps int
	// MaxStepsKey names the configuration knob shown when the MaxSteps guard is
	// hit. Empty defaults to agent.max_steps.
	MaxStepsKey string
	Temperature float64
	Pricing     *provider.Pricing // optional, for per-turn cost display

	// Gate is the per-call permission gate. nil disables gating.
	Gate Gate

	// Context management. ContextWindow <= 0 disables compaction. Ratios and
	// RecentKeep fall back to defaults when unset.
	ContextWindow     int
	SoftCompactRatio  float64
	CompactRatio      float64
	CompactForceRatio float64
	RecentKeep        int
	ArchiveDir        string
	// ContextBudgetPercent caps the effective context window the agent treats as
	// available, triggering compaction earlier (SPEC v2 §3.6). 0 or 100 = use
	// the full window (the default, zero user config). 80 = compact as if the
	// window were 80% of its real size — useful for cost tiers (input past a
	// provider's pricing breakpoint doubles) and for models whose quality
	// degrades near the window edge. Clamped to (0,100].
	ContextBudgetPercent int

	// Hooks fires PreToolUse / PostToolUse shell hooks around tool calls. nil
	// disables hook firing.
	Hooks ToolHooks

	// PreEditHook, when set, receives each writer tool's previewed change just
	// before it runs — the seam the checkpoint store uses to snapshot pre-edit
	// file state. The controller installs it on the main loop via
	// SetPreEditHook; passing the same function here extends checkpoint capture
	// to sub-agents (run_skill/task), whose file writes must rewind the same way.
	PreEditHook func(diff.Change)

	// PostEditHook, when set, receives each previewed path after the writer
	// tool succeeded — paired with PreEditHook so the store can record the
	// post-edit hash for the rewind safety classification.
	PostEditHook func(string)

	// Jobs is the session's background-job manager (nil disables background tools).
	Jobs *jobs.Manager

	// ProjectChecks are host-observable structured checks extracted during boot.
	ProjectChecks []instruction.VerifyCheck

	// ModelRef names the canonical "provider/model" ref backing this agent's
	// turns. It labels usage and seeds prompt-cache attribution upstream; empty
	// is fine for callers that do not report model identity.
	ModelRef string

	// RequireVisibleFinal makes internal callers reject reasoning-only clean
	// stops: a no-tool turn without visible text is retried (bounded) instead
	// of being accepted as the final answer. Hiq's interactive loop,
	// sub-agents, and the guardian all set it; leaving it off adopts upstream's
	// harness-style termination, which accepts a reasoning-only clean stop.
	RequireVisibleFinal bool

	// ContinuationPolicy is the internal host policy for synthetic same-Run
	// continuation. It is not a user configuration key. Reserved: nothing in
	// the run loop branches on it yet, matching upstream.
	ContinuationPolicy ContinuationPolicy

	// StrictAlternatingRoles merges adjacent user turns on the outgoing
	// request, for providers that reject two user messages in a row. The
	// stored session keeps its logical turn boundaries.
	StrictAlternatingRoles bool
}

// New constructs an Agent. MaxSteps <= 0 means no cap — the run loop continues
// until the model gives a final answer, the context is cancelled, or the
// provider errors (compaction keeps the context bounded). A nil sink is replaced
// with event.Discard so the agent can always emit unconditionally.
func New(prov provider.Provider, tools *tool.Registry, session *Session, opts Options, sink event.Sink) *Agent {
	if opts.SoftCompactRatio <= 0 {
		opts.SoftCompactRatio = defaultSoftCompactRatio
	}
	if opts.CompactRatio <= 0 {
		opts.CompactRatio = defaultCompactRatio
	}
	if opts.CompactForceRatio <= 0 {
		opts.CompactForceRatio = defaultCompactForceRatio
	}
	if opts.RecentKeep <= 0 {
		opts.RecentKeep = minRecentKeep
	}
	// Context budget: default 100 (use the full window). Clamp into (0,100].
	budgetPct := opts.ContextBudgetPercent
	if budgetPct <= 0 || budgetPct > 100 {
		budgetPct = 100
	}
	if nilutil.IsNil(sink) {
		sink = event.Discard
	}
	gate := opts.Gate
	if nilutil.IsNil(gate) {
		gate = nil
	}
	hooks := opts.Hooks
	if nilutil.IsNil(hooks) {
		hooks = nil
	}
	maxStepsKey := opts.MaxStepsKey
	if strings.TrimSpace(maxStepsKey) == "" {
		maxStepsKey = "agent.max_steps"
	}
	return &Agent{
		prov:                   prov,
		tools:                  tools,
		session:                session,
		maxSteps:               opts.MaxSteps,
		maxStepsKey:            maxStepsKey,
		temperature:            opts.Temperature,
		pricing:                opts.Pricing,
		modelRef:               strings.TrimSpace(opts.ModelRef),
		requireVisibleFinal:    opts.RequireVisibleFinal,
		continuationPolicy:     opts.ContinuationPolicy,
		strictAlternatingRoles: opts.StrictAlternatingRoles,
		// 4-1 dual-track: the agent's sink IS the item adapter, which
		// forwards to the real sink AND emits structured ItemEvents. Every
		// a.sink.Emit call produces both forms — old sinks see zero
		// difference, new consumers render from Item.
		sink:              event.NewItemAdapter(sink),
		gate:              gate,
		hooks:             hooks,
		onPreEdit:         opts.PreEditHook,
		onPostEdit:        opts.PostEditHook,
		jobs:              opts.Jobs,
		evidence:          evidence.NewLedger(),
		projectChecks:     append([]instruction.VerifyCheck(nil), opts.ProjectChecks...),
		contextWindow:     opts.ContextWindow,
		contextBudgetFrac: float64(budgetPct) / 100.0,
		softCompactRatio:  opts.SoftCompactRatio,
		compactRatio:      opts.CompactRatio,
		compactForceRatio: opts.CompactForceRatio,
		recentKeep:        opts.RecentKeep,
		archiveDir:        opts.ArchiveDir,
		opGate:            newOpGate(),
	}
}

// Run appends the user input and drives the tool loop until the model returns a
// final answer (no tool calls), the context is cancelled, or the provider errors.
// With maxSteps <= 0 the loop is unbounded — the natural termination is the model
// finishing, and the real safety bounds are user cancellation and compaction, not
// a round count. A positive maxSteps imposes an optional hard guard, surfaced as
// a resumable notice when hit.
func (a *Agent) Run(ctx context.Context, input any) error {
	defer a.clearSteerQueue()
	a.steerMu.Lock()
	a.steerConsumed = false
	a.steerMu.Unlock()
	// Fresh pause/resume channels for this turn: a leftover pauseCh from a prior
	// turn would immediately re-pause. resetPauseStateLocked also clears the
	// paused flag so IsPaused is accurate before the loop starts.
	a.pauseMu.Lock()
	a.resetPauseStateLocked()
	a.pauseMu.Unlock()
	if a.evidence != nil {
		a.evidence.Reset()
	}
	a.repeatSuccessCounts = nil
	a.repeatText.reset()
	a.repeatTextWarned = false
	if a.opGate != nil {
		a.opGate.reset()
	}
	a.sink.Emit(event.Event{Kind: event.TurnStarted})
	a.session.Add(provider.MessageFromInput(input))

	finalReadinessBlocks := 0
	emptyFinalBlocks := 0
	streamRecoveries := 0

	for step := 0; a.maxSteps <= 0 || step < a.maxSteps; step++ {
		// Graceful pause point: if the user requested a pause, finish any prior
		// step (we got here because the last iteration completed), then block
		// here until Resume. This sits at the TOP of the loop so the in-flight
		// LLM call of the previous iteration has already finished and its result
		// is persisted — pause never interrupts a streaming response, it only
		// gates entry to the next one. State is fully preserved across the pause.
		if err := a.awaitPause(ctx); err != nil {
			return err
		}
		// Stop gate: if the turn was cancelled while a prior step was winding
		// down (e.g. between a tool call returning and the next stream), exit
		// now instead of starting another LLM call. runGuarded maps a cancelled
		// ctx to a clean TurnDone (ctx.Err()!=nil → nil Err) so the UI treats
		// this as a normal stop, not a failure.
		if err := ctx.Err(); err != nil {
			return err
		}
		// Consume a queued steer and persist it to the session so it
		// survives tab switches and history replay. The model sees it as
		// guidance (with a prefix), not a new task. One cache miss per
		// steer is unavoidable — the model must see the new instruction.
		if text, ok := a.consumeSteer(); ok {
			a.session.Add(provider.Message{Role: provider.RoleUser, Content: midTurnSteerMessage(text)})
			a.sink.Emit(event.Event{Kind: event.Steer, Text: text})
		}
		schemas := a.tools.Schemas()
		prefixShape := a.capturePrefixShape(schemas)
		prevPrefixShape := a.lastPrefixShape
		if !a.haveLastPrefixShape {
			prevPrefixShape = prefixShape
		}

		text, reasoning, signature, calls, usage, interrupted, partialToolStarted, err := a.stream(ctx, step+1)
		if err != nil {
			if interrupted && streamRecoveries < maxStreamRecoveries {
				streamRecoveries++
				if hasVisibleFinalAnswer(text) {
					a.session.Add(provider.Message{
						Role:               provider.RoleAssistant,
						Content:            text,
						ReasoningContent:   reasoning,
						ReasoningSignature: signature,
					})
				}
				a.session.Add(provider.Message{
					Role:    provider.RoleUser,
					Content: streamRecoveryMessage(hasVisibleFinalAnswer(text), partialToolStarted),
				})
				a.sink.Emit(event.Event{Kind: event.Retrying, RetryAttempt: streamRecoveries, RetryMax: maxStreamRecoveries})
				step-- // recovery retries do not consume the tool-round maxSteps budget
				continue
			}
			return err
		}
		streamRecoveries = 0
		cacheDiagnostics := CompareShape(prevPrefixShape, prefixShape, usage)
		a.lastPrefixShape = prefixShape
		a.haveLastPrefixShape = true
		if usage != nil && usage.TotalTokens > 0 {
			a.sink.Emit(event.Event{Kind: event.Usage, Usage: usage, Pricing: a.pricing,
				CacheDiagnostics: &cacheDiagnostics,
				SessionHit:       int(a.sessCacheHit.Load()), SessionMiss: int(a.sessCacheMiss.Load())})
		}
		if msg, ok := finishReasonMessage(usage); ok {
			a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: msg})
		}

		// Truncation safety: a "length"/"repetition_truncation" finish means the
		// output was cut off mid-generation, so a tool call's JSON arguments may be
		// half-written (e.g. edit_file old_string ends mid-token). Executing such a
		// call can silently corrupt a file — a truncated old_string may match the
		// wrong location, a truncated write_file content writes partial bytes. Fail
		// every call in the turn instead, persist the tool-role messages so the next
		// round re-injects the failure, and loop so the model retries. content_filter
		// is excluded: the call args are complete, only the text was filtered.
		// Truncation safety interceptor (spec 3-11): a "length" finish means
		// tool-call args are truncated mid-token. Fail every call, persist the
		// tool-role messages, and loop so the model retries.
		if (truncationInterceptor{agent: a}).intercepted(calls, usage) {
			a.maybeCompact(ctx, usage)
			continue
		}

		// Keep reasoning_content on the assistant turn for display and session
		// archive. It is NOT re-uploaded to the API: the openai provider drops it
		// when building the request, since re-sent reasoning is billable prompt
		// input for no cache or coherence gain.
		a.session.Add(provider.Message{
			Role:               provider.RoleAssistant,
			Content:            text,
			ReasoningContent:   reasoning,
			ReasoningSignature: signature,
			ToolCalls:          calls,
		})

		if len(calls) == 0 {
			// Pre-answer interceptor chain (spec 3-11): readiness (todo/
			// project-check enforcement) then empty-answer, each with its own
			// retry budget. The first block wins; a clean pass means the model
			// produced a verified final answer.
			chain := newInterceptorChain(a)
			verdict := chain.checkAll(preAnswerState{
				Text: text, Reasoning: reasoning, Usage: usage, Step: step,
				finalBlocks: finalReadinessBlocks, emptyBlocks: emptyFinalBlocks,
			})
			if verdict.BlockReason != "" {
				if verdict.BlockReason == "empty-final" {
					emptyFinalBlocks++
					if emptyFinalBlocks >= maxEmptyFinalBlocks {
						return fmt.Errorf("model finished without a visible final answer %d times", emptyFinalBlocks)
					}
				} else {
					finalReadinessBlocks++
					r := a.finalReadinessCheck() // same state the chain just saw
					if finalReadinessBlocks >= maxFinalReadinessBlocks {
						event.RecordReadinessAudit(a.sink, r.audit(evidence.ReadinessErrored, false))
						return fmt.Errorf("final-answer readiness failed %d times: %s", finalReadinessBlocks, verdict.BlockReason)
					}
					event.RecordReadinessAudit(a.sink, r.audit(evidence.ReadinessBlocked, false))
				}
				a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn,
					Text: blockNotice(verdict.BlockReason, usage, a.prov.Name(), len(reasoning))})
				a.session.Add(provider.Message{Role: provider.RoleUser,
					Content: nudgeFor(verdict.BlockReason, usage, a.prov.Name(), len(reasoning))})
				a.maybeCompact(ctx, usage)
				continue
			}
			if verdict.Applies {
				event.RecordReadinessAudit(a.sink, a.finalReadinessCheck().audit(evidence.ReadinessAllowed, finalReadinessBlocks > 0))
			}
			if a.steerQueueLen() > 0 {
				continue
			}
			// A final-answer turn otherwise skips compaction, so a large context
			// carries into the next turn un-folded and can overflow the model window.
			// No-op below the trigger, so normal turns keep their warm cache.
			a.maybeCompact(ctx, usage)
			return nil // model gave a final answer
		}
		emptyFinalBlocks = 0

		results := a.executeBatch(ctx, calls)
		for i, call := range calls {
			a.session.Add(provider.Message{
				Role:       provider.RoleTool,
				Content:    results[i],
				ToolCallID: call.ID,
				Name:       call.Name,
			})
		}

		// The prompt only grows from here; compact before the next turn so it
		// stays within the model's window.
		a.maybeCompact(ctx, usage)
	}
	// Only reached when a positive maxSteps guard is configured. The work so far
	// is already in the session, so the user can just send another message to pick
	// up where it left off.
	return fmt.Errorf("paused after %d tool-call rounds (%s) — the work so far is saved; send another message to continue, or set %s higher or to 0 for no limit", a.maxSteps, a.maxStepsKey, a.maxStepsKey)
}

func (a *Agent) finalReadinessFailure() string {
	return a.finalReadinessCheck().reason
}

type finalReadinessCheck struct {
	applies              bool
	reason               string
	missingProjectChecks int
	incompleteTodos      int
}

func (c finalReadinessCheck) audit(result evidence.ReadinessAuditResult, recovered bool) evidence.ReadinessAudit {
	return evidence.ReadinessAudit{
		Result:                 result,
		Recovered:              recovered,
		MissingProjectChecks:   c.missingProjectChecks,
		IncompleteTodos:        c.incompleteTodos,
		CommandMismatchMissing: c.missingProjectChecks,
	}
}

func (a *Agent) finalReadinessCheck() finalReadinessCheck {
	if a.evidence == nil {
		return finalReadinessCheck{}
	}
	// compose phases set skipReadiness so their phased implement/verify/review
	// runs aren't forced to finish all todos in one turn (which would either
	// derail the retry loop or hard-error after maxFinalReadinessBlocks).
	if a.skipReadiness.Load() {
		return finalReadinessCheck{}
	}
	var missing []string
	out := finalReadinessCheck{}
	if !a.planMode.Load() {
		incomplete, hasTodos := a.evidence.IncompleteLatestTodos()
		if !hasTodos && a.evidence.HasAnySuccessfulReceipt() {
			incomplete, hasTodos = a.incompleteCanonicalTodos()
		}
		if hasTodos && len(incomplete) > 0 {
			out.applies = true
			out.incompleteTodos = len(incomplete)
			missing = append(missing, finalReadinessIncompleteTodos(incomplete))
		}
	}
	writer, hasWriter := a.evidence.LatestSuccessfulWriterIndex()
	if !hasWriter {
		if len(missing) > 0 {
			out.reason = strings.Join(missing, "; ")
		}
		return out
	}
	hasProjectChecks := len(a.projectChecks) > 0
	hasTodoReceipt := a.evidence.HasSuccessfulTodoWrite()
	if !hasProjectChecks && !hasTodoReceipt && len(missing) == 0 {
		return finalReadinessCheck{}
	}
	out.applies = true
	for _, check := range a.projectChecks {
		command := strings.TrimSpace(check.Command)
		if command == "" {
			continue
		}
		if !a.evidence.HasSuccessfulCommandAfter(command, writer) {
			out.missingProjectChecks++
			missing = append(missing, fmt.Sprintf("run %q from %s after the latest write", command, finalReadinessCheckSource(check)))
		}
	}

	if len(missing) == 0 {
		return out
	}
	out.reason = strings.Join(missing, "; ")
	return out
}

func finalReadinessIncompleteTodos(items []evidence.TodoStepMatch) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		label := strings.TrimSpace(item.Content)
		if label == "" {
			label = fmt.Sprintf("todo %d", item.Index)
		}
		parts = append(parts, fmt.Sprintf("%s: %s", label, item.Status))
	}
	return "latest successful todo_write still has incomplete items: " + strings.Join(parts, ", ")
}

func (a *Agent) setTodoState(todos []evidence.TodoItem) {
	a.todoMu.Lock()
	a.todoState = append([]evidence.TodoItem(nil), todos...)
	a.todoMu.Unlock()
}

func (a *Agent) incompleteCanonicalTodos() ([]evidence.TodoStepMatch, bool) {
	a.todoMu.Lock()
	defer a.todoMu.Unlock()
	if len(a.todoState) == 0 {
		return nil, false
	}
	return evidence.IncompleteTodos(a.todoState), true
}

// advanceCanonicalTodo flips the canonical todo matching a signed-off step to
// completed (promoting the next pending item to in_progress) and emits a
// synthetic todo_write so the task panel reflects it without the model
// re-sending the whole list. No-op when nothing matches or it is already done.
func (a *Agent) advanceCanonicalTodo(step string) {
	a.todoMu.Lock()
	if len(a.todoState) == 0 {
		a.todoMu.Unlock()
		return
	}
	m, ok := evidence.MatchStep(step, a.todoState)
	if !ok || canonicalTodoStatus(a.todoState[m.Index-1].Status) == "completed" {
		a.todoMu.Unlock()
		return
	}
	a.todoState[m.Index-1].Status = "completed"
	promoteNextPendingTodo(a.todoState)
	snapshot := append([]evidence.TodoItem(nil), a.todoState...)
	a.todoMu.Unlock()
	a.recordTodoState(snapshot)
	a.emitTodoState(snapshot, m.Index)
}

// recordTodoState logs the host-advanced list as a synthetic todo_write receipt
// so the per-turn final gate (which reads the ledger's latest todo_write) sees
// the advance — the model no longer has to re-send a todo_write to mark the
// completion. It bypasses the todo_write tool, so the completion-transition
// guard never runs on it.
func (a *Agent) recordTodoState(todos []evidence.TodoItem) {
	if a.evidence == nil {
		return
	}
	args, err := json.Marshal(map[string]any{"todos": todos})
	if err != nil {
		return
	}
	a.evidence.Record(evidence.ReceiptFromToolCall("todo_write", json.RawMessage(args), true, true))
}

func promoteNextPendingTodo(todos []evidence.TodoItem) {
	for _, t := range todos {
		if canonicalTodoStatus(t.Status) == "in_progress" {
			return
		}
	}
	for i := range todos {
		if canonicalTodoStatus(todos[i].Status) == "pending" {
			todos[i].Status = "in_progress"
			return
		}
	}
}

func canonicalTodoStatus(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "pending"
	}
	return s
}

// emitTodoState emits a synthetic todo_write event so the frontend task panel
// reflects a host-advanced completion without the model re-sending the list.
// itemIndex is the 1-based position of the completed todo in the panel.
func (a *Agent) emitTodoState(todos []evidence.TodoItem, itemIndex int) {
	args, err := json.Marshal(map[string]any{"todos": todos})
	if err != nil {
		return
	}
	id := fmt.Sprintf("host-advance-%d-%d", a.hostAdvanceSeq.Add(1), itemIndex)
	t := event.Tool{ID: id, Name: "todo_write", Args: string(args), ReadOnly: true}
	a.sink.Emit(event.Event{Kind: event.ToolDispatch, Tool: t})
	t.Output = "task list advanced by complete_step"
	a.sink.Emit(event.Event{Kind: event.ToolResult, Tool: t})
}

// rebuildTodoState reconstructs the canonical task list from a transcript: the
// latest successful todo_write is the base, then every complete_step after it
// advances an item. Deterministic from persisted messages, so it survives a
// fresh load or a rewind (the truncated history yields the historical state).
// Empty after compaction drops the todo_write — no worse than no canonical list.
func (a *Agent) rebuildTodoState(msgs []provider.Message) {
	successful := successfulToolCallIDs(msgs)
	var todos []evidence.TodoItem
	baseIdx := -1
	for i, msg := range msgs {
		for _, tc := range msg.ToolCalls {
			if tc.Name != "todo_write" || !successful[tc.ID] {
				continue
			}
			if rec := evidence.ReceiptFromToolCall(tc.Name, json.RawMessage(tc.Arguments), true, true); len(rec.Todos) > 0 {
				todos = append([]evidence.TodoItem(nil), rec.Todos...)
				baseIdx = i
			}
		}
	}
	if baseIdx < 0 {
		a.setTodoState(nil)
		return
	}
	for i := baseIdx; i < len(msgs); i++ {
		for _, tc := range msgs[i].ToolCalls {
			if tc.Name != "complete_step" || !successful[tc.ID] {
				continue
			}
			rec := evidence.ReceiptFromToolCall(tc.Name, json.RawMessage(tc.Arguments), true, true)
			if m, ok := evidence.MatchStep(rec.Step, todos); ok && canonicalTodoStatus(todos[m.Index-1].Status) != "completed" {
				todos[m.Index-1].Status = "completed"
				promoteNextPendingTodo(todos)
			}
		}
	}
	a.setTodoState(todos)
}

func successfulToolCallIDs(msgs []provider.Message) map[string]bool {
	successful := map[string]bool{}
	for _, msg := range msgs {
		if msg.Role != provider.RoleTool || msg.ToolCallID == "" {
			continue
		}
		if !toolResultFailed(provider.ContentString(msg.Content)) {
			successful[msg.ToolCallID] = true
		}
	}
	return successful
}

func toolResultFailed(content string) bool {
	content = strings.TrimSpace(content)
	return strings.HasPrefix(content, "error:") ||
		strings.HasPrefix(content, "blocked:") ||
		strings.HasPrefix(content, "Error:") ||
		strings.HasPrefix(content, "[error")
}

func finalReadinessCheckSource(check instruction.VerifyCheck) string {
	source := strings.TrimSpace(check.SourcePath)
	if source == "" {
		source = "project memory"
	}
	if check.Line > 0 {
		return fmt.Sprintf("%s:%d", source, check.Line)
	}
	return source
}

func finalReadinessRetryMessage(reason string) string {
	return "Host final-answer readiness check failed. Before giving a final answer, address the missing host-observable receipts: " + reason + ". Run the required tool calls, then answer when readiness is satisfied."
}

func hasVisibleFinalAnswer(text string) bool {
	return strings.TrimSpace(text) != ""
}

func emptyFinalRetryMessage() string {
	return "The previous assistant response finished without any visible answer text. Continue the same task now and provide a concise visible answer to the user. Do not send reasoning only."
}

func emptyFinalNotice(prov string, u *provider.Usage, reasoningLen int) string {
	finish := "unknown"
	if u != nil && u.FinishReason != "" {
		finish = u.FinishReason
	}
	return fmt.Sprintf("empty final answer blocked: %s returned no visible answer text (finish=%s, reasoning=%d chars); retrying", prov, finish, reasoningLen)
}

func streamRecoveryMessage(hasPartialText, hadPartialTool bool) string {
	switch {
	case hadPartialTool:
		return "The previous assistant response was interrupted while a tool call was streaming. Continue the same task now. If a tool is still needed, issue a fresh complete tool call from scratch; do not rely on any partial tool-call arguments from the interrupted stream."
	case hasPartialText:
		return "The previous assistant response was interrupted during streaming. Continue the same task from immediately after the partial assistant message above. Do not repeat text that is already visible."
	default:
		return "The previous assistant response was interrupted during streaming before visible answer text was completed. Continue the same task now and provide the next useful response."
	}
}

// stream runs one completion, emitting reasoning and text deltas as typed
// events and collecting complete tool calls. A Message event closes the text
// stream so a sink can re-render the streamed raw text as styled markdown. The
// accumulated text and reasoning are also returned so the caller can round-trip
// reasoning on the next turn.
func (a *Agent) stream(ctx context.Context, turn int) (string, string, string, []provider.ToolCall, *provider.Usage, bool, bool, error) {
	ctx = provider.WithRetryNotify(ctx, func(info provider.RetryInfo) {
		a.sink.Emit(event.Event{Kind: event.Retrying, RetryAttempt: info.Attempt, RetryMax: info.Max,
			RetryAfterMs: info.Delay.Milliseconds()})
	})
	// Build the context: the stored session messages, optionally projected by
	// a read-side filter (e.g. expert-collab messages reduced to their synthesis
	// so a multi-expert transcript never bloats the window).
	// We MUST clone the slice to avoid mutating the session's backing array.
	msgs := append([]provider.Message(nil), a.session.Messages...)
	if a.contextFilter != nil {
		msgs = a.contextFilter(msgs)
	}

	// Inject real-time clock context into the final user message so the model
	// never hallucinates its cutoff year even in long-running sessions.
	if len(msgs) > 0 {
		lastIdx := len(msgs) - 1
		if msgs[lastIdx].Role == provider.RoleUser {
			now := time.Now()
			weekdays := []string{"日", "一", "二", "三", "四", "五", "六"}
			timeNotice := fmt.Sprintf("\n\n<ADDITIONAL_METADATA>\n【系统通知】当前实时系统时间: %s 周%s\n</ADDITIONAL_METADATA>",
				now.Format("2006-01-02 15:04:05"), weekdays[int(now.Weekday())])

			lastMsg := msgs[lastIdx]
			lastMsg.Content += timeNotice
			msgs[lastIdx] = lastMsg
		}
	}

	// Providers that reject two user turns in a row get a coalesced outbound
	// copy; the stored session keeps its logical turn boundaries.
	if a.strictAlternatingRoles {
		msgs = coalesceProjectionUserRuns(msgs)
	}

	ch, err := a.prov.Stream(ctx, provider.Request{
		Messages:    msgs,
		Tools:       a.tools.Schemas(),
		Temperature: a.temperature,
		CacheKey:    a.cacheKey,
	})
	if err != nil {
		return "", "", "", nil, nil, false, false, err
	}

	// A PostLLMCall hook rewrites the whole reasoning block, so when one is wired
	// up we buffer reasoning silently and emit the transformed text once after the
	// stream. With no such hook the reasoning streams live, chunk by chunk, as
	// before — the common case must not lose its live "thinking…" display.
	transformReasoning := a.hooks != nil && a.hooks.HasPostLLMCall()

	var text, reasoning strings.Builder
	var argsAccums map[string]*argsPreviewAccum
	var signature string // provider-issued proof for the reasoning (Anthropic thinking)
	var calls []provider.ToolCall
	var usage *provider.Usage
	var partialToolStarted bool
	finishReasoning := func() (stored, display string) {
		original := reasoning.String()
		display = original
		if transformReasoning && original != "" {
			display = a.hooks.PostLLMCall(ctx, original, turn)
			if display != "" {
				a.sink.Emit(event.Event{Kind: event.Reasoning, Text: display})
			}
		}
		stored = display
		if signature != "" {
			stored = original
		}
		return stored, display
	}
	for chunk := range ch {
		switch chunk.Type {
		case provider.ChunkReasoning:
			reasoning.WriteString(chunk.Text)
			if chunk.Signature != "" {
				signature = chunk.Signature
			}
			if chunk.Text != "" && !transformReasoning {
				a.sink.Emit(event.Event{Kind: event.Reasoning, Text: chunk.Text})
			}
		case provider.ChunkText:
			text.WriteString(chunk.Text)
			a.sink.Emit(event.Event{Kind: event.Text, Text: chunk.Text})
			// Advisory text-repetition guard: detect when the model loops on the
			// same passage. Notice only (once per turn), never aborts — false
			// positives on legitimate repeated phrasing are worse than a miss.
			if !a.repeatTextWarned && a.repeatText.append(chunk.Text) {
				a.repeatTextWarned = true
				a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn,
					Text: "output appears to be repeating the same passage; consider wrapping up the answer"})
			}
		case provider.ChunkToolCallStart:
			partialToolStarted = true
			// Surface the tool card as soon as the call begins — before its
			// (possibly large) arguments finish streaming — so the user sees it
			// working instead of a stall. executeBatch emits the full dispatch
			// (with args) once the call completes; the frontend merges by ID.
			if tc := chunk.ToolCall; tc != nil {
				a.sink.Emit(event.Event{Kind: event.ToolDispatch, Tool: event.Tool{
					ID: tc.ID, Name: tc.Name, ReadOnly: a.toolReadOnly(tc.Name), Partial: true,
				}})
			}
		case provider.ChunkToolCall:
			partialToolStarted = true
			calls = append(calls, *chunk.ToolCall)
		case provider.ChunkToolArgsDelta:
			partialToolStarted = true
			// Live patch preview (upgrade spec 3-3): accumulate the raw args
			// and, for apply_patch, emit the partial patchText throttled —
			// the frontend renders an incomplete-but-live diff while the
			// model is still writing.
			if tc := chunk.ToolCall; tc != nil && tc.Name == "apply_patch" && chunk.Text != "" {
				acc := argsAccums[tc.ID]
				if acc == nil {
					acc = &argsPreviewAccum{}
					if argsAccums == nil {
						argsAccums = map[string]*argsPreviewAccum{}
					}
					argsAccums[tc.ID] = acc
				}
				acc.raw.WriteString(chunk.Text)
				now := time.Now().UnixMilli()
				if now-acc.lastEmit >= minPreviewIntervalMs {
					acc.lastEmit = now
					if patch, ok := patchFromArgs(acc.raw.String()); ok && patch != "" {
						a.sink.Emit(event.Event{Kind: event.ToolArgsDelta, Text: patch,
							Tool: event.Tool{ID: tc.ID, Name: tc.Name}})
					}
				}
			}
		case provider.ChunkUsage:
			usage = chunk.Usage
			a.lastUsage.Store(chunk.Usage)
			a.sessCacheHit.Add(int64(chunk.Usage.CacheHitTokens))
			// sessCacheMiss keeps the legacy "non-hit" semantics (uncached input
			// + cache writes) so the session hit-rate the frontend shows stays
			// comparable across versions. Cache writes are billed separately via
			// Usage.CacheWriteTokens / Pricing.cache_write, not double-counted.
			a.sessCacheMiss.Add(int64(chunk.Usage.CacheMissTokens + chunk.Usage.CacheWriteTokens))
		case provider.ChunkError:
			if provider.IsStreamInterrupted(chunk.Err) {
				stored, _ := finishReasoning()
				return text.String(), stored, signature, calls, usage, true, partialToolStarted, chunk.Err
			}
			return "", "", "", nil, nil, false, false, chunk.Err
		}
	}
	// With a PostLLMCall hook, the live stream was suppressed above; transform the
	// full reasoning now and emit it once so the sink never sees the untranslated
	// text. Without a hook this is skipped — the chunk-by-chunk events already fired.
	stored, display := finishReasoning()
	// Store the transformed reasoning — except when a provider signature pins it to
	// the original text (Anthropic extended thinking). That signed thinking block is
	// replayed verbatim on the next tool-call turn; re-uploading transformed text
	// under the original signature is rejected, so keep the original for storage
	// while the user still sees the transformed version live. finishReasoning did
	// that choice above.
	// Close the text stream: a sink may re-render the streamed raw text as
	// styled markdown now that it is complete. Reasoning rides along so the sink
	// has the full chain if it wants it.
	if text.Len() > 0 || display != "" {
		a.sink.Emit(event.Event{Kind: event.Message, Text: text.String(), Reasoning: display})
	}
	return text.String(), stored, signature, calls, usage, false, false, nil
}

func (a *Agent) capturePrefixShape(schemas []provider.ToolSchema) PrefixShape {
	return CaptureShape(a.systemPrompt(), schemas, a.session.RewriteVersion())
}

func (a *Agent) systemPrompt() string {
	var b strings.Builder
	for _, m := range a.session.Messages {
		if m.Role != provider.RoleSystem {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(provider.ContentString(m.Content))
	}
	return b.String()
}

// executeBatch dispatches one model turn's tool calls. A ToolDispatch event is
// emitted for every call up front, in call order, so a frontend can show the
// timeline chronologically. Contiguous known ReadOnly calls fan out across
// goroutines; unknown and writer calls run as single-call serial segments so
// write/read ordering stays provider-ordered. ToolResult events are emitted
// after the batch in call order, so emission stays serial even when execution
// parallelised.
func (a *Agent) executeBatch(ctx context.Context, calls []provider.ToolCall) []string {
	// Tool-progress status line (upgrade spec 2-2): the composer strip shows
	// which tool is running and how far through the batch it is
	// ("edit_file 2/5"). Phase items don't render in the transcript — the
	// composer consumes the latest one — so one event per call is cheap.
	for i, c := range calls {
		a.sink.Emit(event.Event{Kind: event.Phase, Text: fmt.Sprintf("%s %d/%d", c.Name, i+1, len(calls))})
	}
	// Previews are computed once here — before anything in the batch runs — and
	// handed to executeOne, which feeds them to the checkpoint hook. Computing
	// at dispatch time is also the freshest correct restore baseline: the
	// checkpoint store dedupes per path, so the first writer of a path in the
	// batch snapshots its pre-batch content, which is exactly what a rewind
	// needs. (The old flow previewed twice — once for the event, once for the
	// snapshot — reading and diffing every file twice per writer call.)
	previews := make([][]diff.Change, len(calls))
	for i, c := range calls {
		t, ok := a.tools.Get(c.Name)
		ev := event.Tool{ID: c.ID, Name: c.Name, Args: c.Arguments, ReadOnly: ok && t.ReadOnly()}
		if ok {
			if chs, pok := tool.PreviewChanges(t, json.RawMessage(c.Arguments)); pok {
				previews[i] = chs
				if len(chs) == 1 {
					ev.FileDiff = event.FileDiff{Diff: chs[0].Diff, Added: chs[0].Added, Removed: chs[0].Removed}
				} else {
					var added, removed int
					parts := make([]string, 0, len(chs))
					for _, ch := range chs {
						parts = append(parts, strings.TrimRight(ch.Diff, "\n"))
						added += ch.Added
						removed += ch.Removed
					}
					ev.FileDiff = event.FileDiff{Diff: strings.Join(parts, "\n"), Added: added, Removed: removed}
				}
			}
			if pr, ok := t.(interface {
				ResolveProfile(json.RawMessage) *event.Profile
			}); ok {
				ev.Profile = pr.ResolveProfile(json.RawMessage(c.Arguments))
			}
		}
		a.sink.Emit(event.Event{Kind: event.ToolDispatch, Tool: ev})
	}

	results := make([]string, len(calls))
	outcomes := make([]toolOutcome, len(calls))
	durations := make([]int64, len(calls))
	run := func(i int) {
		start := time.Now()
		outcomes[i] = a.executeOne(ctx, calls[i], previews[i])
		durations[i] = time.Since(start).Milliseconds()
		results[i] = outcomes[i].output
	}

	for _, batch := range partitionToolCalls(a.tools, calls, previews) {
		if batch.parallel && batch.end-batch.start > 1 {
			runParallel(batch.start, batch.end, run)
			continue
		}
		for i := batch.start; i < batch.end; i++ {
			run(i)
		}
	}

	for i, c := range calls {
		o := outcomes[i]
		t, ok := a.tools.Get(c.Name)
		a.sink.Emit(event.Event{Kind: event.ToolResult, Tool: event.Tool{
			ID:          c.ID,
			Name:        c.Name,
			Args:        c.Arguments,
			Output:      o.output,
			Err:         o.errMsg,
			ReadOnly:    ok && t.ReadOnly(),
			Truncated:   o.truncated,
			DurationMs:  durations[i],
			Attachments: o.attachments,
		}})
		if o.truncated && o.truncMsg != "" {
			a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: o.truncMsg})
		}
	}
	a.applyStormBreaker(calls, outcomes, results)
	return results
}

type toolCallBatch struct {
	start    int
	end      int
	parallel bool
}

// partitionToolCalls keeps provider order while letting contiguous known
// read-only tools run together. Unknown and writer tools are single-call serial
// batches so they cannot reorder around reads or produce surprising errors.
// complete_step and todo_write are read-only but never join a parallel run: they
// read the turn's evidence ledger, so every prior call's receipt must be recorded
// before they run.
func partitionToolCalls(r *tool.Registry, calls []provider.ToolCall, previews [][]diff.Change) []toolCallBatch {
	var batches []toolCallBatch
	i := 0
	for i < len(calls) {
		if parallelisable(r, calls[i].Name) {
			start := i
			i++
			for i < len(calls) && parallelisable(r, calls[i].Name) {
				i++
			}
			batches = append(batches, toolCallBatch{start: start, end: i, parallel: true})
			continue
		}
		// Writer run (upgrade spec 3-1): consecutive writer calls fan out
		// when their previewed file sets are disjoint — same-path edits stay
		// ordered, different-file edits stop queueing behind each other. A
		// writer whose preview carries no paths (bash, plugin tools) can't
		// prove disjointness and runs alone; complete_step/todo_write never
		// join because they read the evidence ledger the others write.
		previewAt := func(idx int) []diff.Change {
			if idx < len(previews) {
				return previews[idx]
			}
			return nil
		}
		if paths := writerPaths(previewAt(i)); len(paths) > 0 {
			start := i
			claimed := make(map[string]bool, len(paths))
			for _, p := range paths {
				claimed[p] = true
			}
			i++
			for i < len(calls) && !parallelisable(r, calls[i].Name) &&
				calls[i].Name != "complete_step" && calls[i].Name != "todo_write" {
				next := writerPaths(previewAt(i))
				if len(next) == 0 {
					break
				}
				overlap := false
				for _, p := range next {
					if claimed[p] {
						overlap = true
						break
					}
				}
				if overlap {
					break
				}
				for _, p := range next {
					claimed[p] = true
				}
				i++
			}
			batches = append(batches, toolCallBatch{start: start, end: i, parallel: i-start > 1})
			continue
		}
		batches = append(batches, toolCallBatch{start: i, end: i + 1})
		i++
	}
	return batches
}

// writerPaths returns the paths a writer call would touch, from its
// precomputed preview; nil when there is no preview (unknown footprint).
func writerPaths(preview []diff.Change) []string {
	if len(preview) == 0 {
		return nil
	}
	paths := make([]string, 0, len(preview))
	for _, ch := range preview {
		paths = append(paths, ch.Path)
	}
	return paths
}

func parallelisable(r *tool.Registry, name string) bool {
	if name == "complete_step" || name == "todo_write" {
		return false
	}
	t, ok := r.Get(name)
	return ok && t.ReadOnly()
}

func runParallel(start, end int, run func(int)) {
	const maxParallel = 8
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i := start; i < end; i++ {
		i := i
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			run(i)
		}()
	}
	wg.Wait()
}

// stormBreakThreshold is how many times in a row the same tool may fail the same
// way before the loop stops echoing the raw error back and instead returns a
// directive to change approach. Two natural self-corrections are healthy; the
// third identical failure is a death-spiral — the dominant case being a tool call
// whose arguments are truncated at the output-token ceiling, which the model then
// re-emits (re-worded but still over-long), truncating the same way again.
const stormBreakThreshold = 3

// repeatSuccessBreakThreshold is how many identical write-like successes the
// agent allows before refusing another copy in the same user turn. Two gives the
// model room for a natural self-correction; the third repeat is usually a
// no-op/write loop and should be redirected to a different tool or final answer.
const repeatSuccessBreakThreshold = 2

// applyStormBreaker detects a run of identically-failing turns and, past the
// threshold, rewrites the model-facing result (results[0]) into a directive to
// change approach. It keys on each call's (tool, error) — not its args — because a
// stuck model reworks the arguments cosmetically while failing identically (see
// the stormSig field doc). A turn is a fixation candidate only when every one of
// its calls errored and none was merely blocked by plan mode / permissions (those
// carry a clear, distinct message the model can already act on). Any success, any
// block, or a different batch shape is varied work, so it resets the counter. This
// covers both the single-call spiral and a repeated multi-call batch. The hard
// maxSteps guard remains the ultimate backstop; this just keeps the loop from
// burning that whole budget bouncing off the same failure.
func (a *Agent) applyStormBreaker(calls []provider.ToolCall, outcomes []toolOutcome, results []string) {
	sig, ok := batchStormSignature(calls, outcomes)
	if !ok {
		a.stormSig, a.stormCount = "", 0
		return
	}
	if sig != a.stormSig {
		a.stormSig, a.stormCount = sig, 1
		return
	}
	a.stormCount++
	if a.stormCount < stormBreakThreshold {
		return
	}
	subject := fmt.Sprintf("%q", calls[0].Name)
	short := calls[0].Name
	if len(calls) > 1 {
		subject = fmt.Sprintf("this batch of %d tool calls", len(calls))
		short = fmt.Sprintf("a batch of %d calls", len(calls))
	}
	results[0] = outcomes[0].output + fmt.Sprintf(
		"\n\n[loop guard] %s has now failed %d times in a row with the same error. Re-sending it — even with the wording changed — will not help: the calls keep failing the same way. Change approach: if an argument is being truncated, write less in one call and split the work into several smaller calls; otherwise fix the arguments, use a different tool, or explain the blocker in your final answer.",
		subject, a.stormCount)
	a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: fmt.Sprintf(
		"loop guard: %s failed %d× the same way — nudging the model to change approach",
		short, a.stormCount)})
}

// batchStormSignature returns a per-turn fixation signature — each call's
// (name, error) in order — and ok=true only when every call errored and none was
// merely blocked. ok=false (any success or block) means the turn made varied
// progress, so the caller resets the counter. Keying on the error rather than the
// args is deliberate: a stuck model reworks the arguments while failing the same
// way, so identical-args matching would miss the loop.
func batchStormSignature(calls []provider.ToolCall, outcomes []toolOutcome) (string, bool) {
	if len(calls) == 0 {
		return "", false
	}
	var sb strings.Builder
	for i := range calls {
		if outcomes[i].errMsg == "" || outcomes[i].blocked {
			return "", false
		}
		sb.WriteString(calls[i].Name)
		sb.WriteByte(0)
		sb.WriteString(outcomes[i].errMsg)
		sb.WriteByte(0)
	}
	return sb.String(), true
}

// toolOutcome is one tool call's result, split into the model-facing output and
// the display-facing notice bits. errMsg is the short failure reason (empty on
// success) — a refused call, an unknown tool, or an execution error — so a sink
// renders the result as failed ("⊘ name <errMsg>" / a red card) instead of OK;
// blocked narrows that to a refusal (plan mode / permission). truncMsg is set
// (without the "· " prefix) when the output was head+tailed.
type toolOutcome struct {
	output      string
	blocked     bool
	errMsg      string
	truncated   bool
	truncMsg    string
	attachments []event.Attachment
}

// executeOne runs a single tool call. It is pure with respect to the event sink
// — the caller emits ToolDispatch/ToolResult — so it is safe to invoke from
// parallel goroutines. preview carries the per-file changes computed by the
// caller's dispatch loop (nil for read-only or unpreviewable calls); the
// checkpoint hook consumes it instead of re-previewing, so each writer call
// reads and diffs its files once per turn.
func (a *Agent) executeOne(ctx context.Context, call provider.ToolCall, preview []diff.Change) toolOutcome {
	t, ok := a.tools.Get(call.Name)
	if !ok {
		return toolOutcome{
			output: fmt.Sprintf("error: unknown tool %q", call.Name),
			errMsg: fmt.Sprintf("unknown tool %q", call.Name),
		}
	}
	// Central required-args check (upgrade spec 3-2): fails fast with one
	// uniform message before any hook or gate observes the call.
	if verr := tool.ValidateArgs(t, json.RawMessage(call.Arguments)); verr != nil {
		return toolOutcome{
			output: fmt.Sprintf("error: %s: %v", call.Name, verr),
			errMsg: verr.Error(),
		}
	}
	if out, blocked := a.repeatedSuccessBlock(call, t); blocked {
		return toolOutcome{
			output:  out,
			blocked: true,
			errMsg:  "blocked by loop guard",
		}
	}
	if a.planMode.Load() && !planModeReadOK(t, json.RawMessage(call.Arguments)) {
		return toolOutcome{
			output:  fmt.Sprintf("blocked: %q is a writer tool and plan mode is read-only. Keep exploring with read-only tools, then write your plan as your reply — the user will be asked to approve it before any changes are made.", call.Name),
			blocked: true,
			errMsg:  "blocked: plan mode is read-only",
		}
	}
	if a.gate != nil {
		allow, reason, err := a.gate.Check(ctx, call.Name, json.RawMessage(call.Arguments), t.ReadOnly())
		if err != nil {
			return toolOutcome{
				output:  fmt.Sprintf("blocked: %s (%v)", reason, err),
				blocked: true,
				errMsg:  fmt.Sprintf("blocked: %v", err),
			}
		}
		if !allow {
			return toolOutcome{
				output:  "blocked: " + reason,
				blocked: true,
				errMsg:  "blocked by permission policy",
			}
		}
	}
	// PreToolUse hooks run after permission is granted but before the call: a
	// gating hook (exit 2) refuses it, surfaced to the model like a gate denial.
	if a.hooks != nil {
		if block, msg := a.hooks.PreToolUse(ctx, call.Name, json.RawMessage(call.Arguments)); block {
			if msg == "" {
				msg = "blocked by a PreToolUse hook"
			}
			return toolOutcome{
				output:  "blocked: " + msg,
				blocked: true,
				errMsg:  "blocked by PreToolUse hook",
			}
		}
	}
	// Operation-level failure-recovery gate (SPEC v2 §3.1): refuse a write/command
	// op that has already failed the same way past its budget this turn. Runs after
	// all gating (permission/hooks) so a policy denial never reaches it, and only
	// for non-read-only tools — diagnosis must always be possible. Pure host logic;
	// the model only sees the returned tool-result message, never "fingerprint".
	if a.opGate != nil && !t.ReadOnly() {
		if msg, stop := a.opGate.beforeMutation(opFingerprint(call)); stop {
			a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn,
				Text: fmt.Sprintf("op-recovery: %s blocked — already failing past this turn's budget", call.Name)})
			return toolOutcome{
				output:  msg,
				blocked: true,
				errMsg:  "blocked by op-recovery gate",
			}
		}
	}
	// Checkpoint the file this writer is about to change, so the turn can be
	// rewound. Fires after all gating (the edit is cleared to run) using the
	// preview the dispatch loop already computed; a nil preview means the tool
	// can't describe its change or the preview errored (the edit will likely
	// fail anyway), so we skip rather than snapshot a stale state.
	if a.onPreEdit != nil && !t.ReadOnly() {
		for _, change := range preview {
			a.onPreEdit(change)
		}
	}
	cctx := withCallContext(ctx, call.ID, a.sink, a.asker)
	if a.evidence != nil {
		cctx = evidence.WithLedger(cctx, a.evidence)
		cctx = evidence.WithSessionMessages(cctx, a.session.Snapshot())
	}
	if len(a.projectChecks) > 0 {
		cctx = instruction.WithChecks(cctx, a.projectChecks)
	}
	if a.jobs != nil {
		cctx = jobs.WithManager(cctx, a.jobs)
	}
	if a.memQueue != nil {
		cctx = memory.WithQueue(cctx, a.memQueue)
	}
	callID := call.ID
	cctx = tool.WithProgress(cctx, func(chunk string) {
		a.sink.Emit(event.Event{Kind: event.ToolProgress, Tool: event.Tool{ID: callID, Output: chunk}})
	})
	result, err := t.Execute(cctx, json.RawMessage(call.Arguments))
	if a.evidence != nil {
		if call.Name == "complete_step" {
			if err == nil {
				rec := evidence.ReceiptFromToolCall(call.Name, json.RawMessage(call.Arguments), true, t.ReadOnly())
				a.evidence.Record(rec)
				a.advanceCanonicalTodo(rec.Step)
			}
		} else {
			rec := evidence.ReceiptFromToolCall(call.Name, json.RawMessage(call.Arguments), err == nil, t.ReadOnly())
			a.evidence.Record(rec)
			if err == nil && call.Name == "todo_write" {
				a.setTodoState(rec.Todos)
			}
		}
	}
	// PostToolUse hooks observe the result (they can't block); fired whether the
	// call succeeded or errored, since the tool did run.
	if a.hooks != nil {
		a.hooks.PostToolUse(ctx, call.Name, json.RawMessage(call.Arguments), result)
	}
	// Post-edit hash: after a writer tool succeeds, record the touched files'
	// new content hashes so a rewind preview can tell agent edits from external
	// changes made since (the safe/unsafe/ignored classification).
	if a.onPostEdit != nil && err == nil && !t.ReadOnly() {
		for _, change := range preview {
			a.onPostEdit(change.Path)
		}
	}
	if err != nil {
		detail := result
		// Malformed-args failures are a transient model JSON glitch (e.g. options
		// written as ["a":"b"] → "invalid character ':' after array element"). The
		// args can't be safely re-parsed, but echoing the tool's schema makes the
		// retry land valid instead of repeating the same broken shape.
		if !json.Valid([]byte(call.Arguments)) {
			detail = strings.TrimRight(detail, "\n") + "\nThe arguments were not valid JSON. Re-emit them exactly per this schema:\n" + string(t.Schema())
		}
		body, truncMsg := truncateToolOutput(fmt.Sprintf("error: %v\n%s", err, detail))
		errMsg := firstLine(err.Error())
		outcome := toolOutcome{output: body, errMsg: errMsg, truncated: truncMsg != "", truncMsg: truncMsg}
		// Observe the failure for the op-recovery gate. A qualifying failure (not
		// a block, not read-only, not transient) increments the budget; if it
		// crosses a threshold the gate returns a guidance nudge appended to the
		// model-facing output. We surface a Notice too so the user sees the stop.
		if a.opGate != nil {
			if guidance := a.opGate.observeResult(opFingerprint(call), errMsg, false, t.ReadOnly()); guidance != "" {
				outcome.output = strings.TrimRight(outcome.output, "\n") + guidance
				a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn,
					Text: fmt.Sprintf("op-recovery: %s failed past this turn's budget — stopping retries", call.Name)})
			}
		}
		return outcome
	}
	// Success: a write/command success is real progress — clear that op's failure
	// history so a later unrelated failure starts fresh.
	if a.opGate != nil {
		a.opGate.observeResult(opFingerprint(call), "", false, t.ReadOnly())
	}
	a.recordRepeatSuccess(call, t)
	// A foreground `task` sub-agent just finished — its result is the final answer.
	// (A backgrounded one returns a "Started…" string and stops later in a job, so
	// it doesn't fire here.) SubagentStop lets a hook react to delegated work.
	if a.hooks != nil && call.Name == "task" && !isBackgroundTaskCall(call.Arguments) {
		a.hooks.SubagentStop(ctx, result)
	}
	body, truncMsg := truncateToolOutput(result)
	return toolOutcome{output: body, truncated: truncMsg != "", truncMsg: truncMsg, attachments: extractImageAttachments(body)}
}

// attachmentImageRe matches image file paths under .hiq/attachments/ that a
// tool may emit in its result text (e.g. image_generate's ![image](...) output).
// We surface them as structured attachments so the frontend can render the
// picture under the tool card without depending on the model echoing the path.
var attachmentImageRe = regexp.MustCompile(`\.hiq/attachments/[^\s)'"]+\.(?:png|jpg|jpeg|gif|webp|bmp|svg|tif|tiff)`)

// extractImageAttachments pulls deduped .hiq/attachments image paths from a
// tool result string, preserving first-seen order.
func extractImageAttachments(s string) []event.Attachment {
	matches := attachmentImageRe.FindAllString(s, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]event.Attachment, 0, len(matches))
	for _, m := range matches {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, event.Attachment{Path: m, Kind: "image"})
	}
	return out
}

func (a *Agent) repeatedSuccessBlock(call provider.ToolCall, t tool.Tool) (string, bool) {
	sig, ok := repeatSuccessSignature(call, t)
	if !ok || a.repeatSuccessCounts == nil {
		return "", false
	}
	count := a.repeatSuccessCounts[sig]
	if count < repeatSuccessBreakThreshold {
		return "", false
	}
	return fmt.Sprintf(
		"blocked: [loop guard] %q has already succeeded %d times with the same write-like arguments in this user turn. Re-running it is unlikely to help and may burn tokens or repeat file writes. Change approach: use edit_file for file changes, verify with a read/test command, or explain the blocker in your final answer.",
		call.Name, count), true
}

func (a *Agent) recordRepeatSuccess(call provider.ToolCall, t tool.Tool) {
	sig, ok := repeatSuccessSignature(call, t)
	if !ok {
		return
	}
	if a.repeatSuccessCounts == nil {
		a.repeatSuccessCounts = make(map[string]int)
	}
	a.repeatSuccessCounts[sig]++
}

func repeatSuccessSignature(call provider.ToolCall, t tool.Tool) (string, bool) {
	if t.ReadOnly() {
		return "", false
	}
	switch call.Name {
	case "write_file", "edit_file":
		return call.Name + "\x00" + canonicalToolArgs(call.Arguments), true
	case "bash":
		var p struct {
			Command         string `json:"command"`
			RunInBackground bool   `json:"run_in_background"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &p); err != nil {
			return "", false
		}
		if p.RunInBackground || !isShellFileWriteCommand(p.Command) {
			return "", false
		}
		return "bash\x00" + normalizeShellCommand(p.Command), true
	default:
		return "", false
	}
}

func canonicalToolArgs(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return strings.TrimSpace(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, b); err != nil {
		return string(b)
	}
	return compact.String()
}

func normalizeShellCommand(command string) string {
	return strings.Join(strings.Fields(command), " ")
}

func isShellFileWriteCommand(command string) bool {
	lower := strings.ToLower(command)
	switch {
	case shellPythonOpenWrites(lower):
		return true
	case strings.Contains(lower, "set-content") || strings.Contains(lower, "add-content") || strings.Contains(lower, "out-file"):
		return true
	case strings.Contains(lower, "sed -i") || strings.Contains(lower, "perl -pi"):
		return true
	case hasShellWriteRedirect(command):
		return true
	default:
		return false
	}
}

func shellPythonOpenWrites(lower string) bool {
	if !strings.Contains(lower, "open(") {
		return false
	}
	if strings.Contains(lower, ".write(") {
		return true
	}
	for _, marker := range []string{", 'w", `, "w`, ", 'a", `, "a`, ", 'x", `, "x`, "mode='w", `mode="w`, "mode='a", `mode="a`, "mode='x", `mode="x`} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func hasShellWriteRedirect(command string) bool {
	var quote rune
	var prev rune
	for _, r := range command {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			prev = r
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			prev = r
			continue
		}
		if r == '>' {
			if prev == '2' {
				prev = r
				continue
			}
			return true
		}
		prev = r
	}
	return false
}

// isBackgroundTaskCall reports whether a `task` call set run_in_background, so a
// fire-and-return dispatch isn't mistaken for a sub-agent that has stopped.
func isBackgroundTaskCall(args string) bool {
	var p struct {
		RunInBackground bool `json:"run_in_background"`
	}
	_ = json.Unmarshal([]byte(args), &p)
	return p.RunInBackground
}

// toolReadOnly reports a tool's ReadOnly classification by name (false for an
// unknown tool), for stamping early ToolDispatch events.
func (a *Agent) toolReadOnly(name string) bool {
	t, ok := a.tools.Get(name)
	return ok && t.ReadOnly()
}

// firstLine returns s up to its first newline — a one-line failure summary for
// the display Err, while the full error stays in the model-facing output.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// truncateToolOutput head+tails s when it exceeds maxToolOutputBytes, slicing
// on rune boundaries so we never split a multibyte glyph. Returns the possibly
// trimmed body plus a one-line user-facing notice when truncation happened
// (empty when it didn't, without the "· " display prefix).
func truncateToolOutput(s string) (string, string) {
	if len(s) <= maxToolOutputBytes {
		return s, ""
	}
	keep := maxToolOutputBytes / 2
	head := snapToRuneBoundary(s, 0, keep)
	tail := snapToRuneBoundary(s, len(s)-keep, len(s))
	omitted := len(s) - len(head) - len(tail)
	notice := fmt.Sprintf("tool output truncated: %d of %d bytes elided", omitted, len(s))
	body := head + fmt.Sprintf("\n\n…[truncated %d of %d bytes — rerun with narrower args to see the middle]…\n\n", omitted, len(s)) + tail
	return body, notice
}

// snapToRuneBoundary returns s[lo:hi] with the bounds nudged outward until
// both land on rune-start positions.
func snapToRuneBoundary(s string, lo, hi int) string {
	for lo > 0 && !utf8.RuneStart(s[lo]) {
		lo--
	}
	for hi < len(s) && !utf8.RuneStart(s[hi]) {
		hi++
	}
	return s[lo:hi]
}

// finishReasonMessage maps an abnormal finish_reason to a one-line warning,
// returning ok=false for the normal terminations ("stop", "tool_calls") and a
// nil usage. The sink renders the message; the "! " prefix is presentation.
func finishReasonMessage(u *provider.Usage) (string, bool) {
	if u == nil {
		return "", false
	}
	switch u.FinishReason {
	case "length":
		return "response truncated: hit max output tokens", true
	case "content_filter":
		return "response blocked by content filter", true
	case "repetition_truncation":
		return "response truncated: model repetition detected", true
	default:
		return "", false
	}
}
