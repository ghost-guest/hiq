// Package control is the transport-agnostic session driver. A Controller owns
// the agent run loop and session lifecycle, takes commands (Send/Cancel/Approve/
// SetPlanMode/Compact/NewSession/…), and emits everything that happens —
// reasoning, tool calls, approvals, turn completion — as a typed event stream to
// a single event.Sink.
//
// The point is one orchestration layer behind every frontend: a terminal TUI, a
// desktop webview, or an HTTP/SSE server each drive the Controller identically
// (issue commands, render events) and none of them re-implement turn lifecycle,
// cancellation, or approval. The Controller depends on no frontend.
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/zzycxz/fairpeer/internal/agent"
	"github.com/zzycxz/fairpeer/internal/checkpoint"
	"github.com/zzycxz/fairpeer/internal/codegraph"
	"github.com/zzycxz/fairpeer/internal/command"
	"github.com/zzycxz/fairpeer/internal/compose"
	"github.com/zzycxz/fairpeer/internal/config"
	"github.com/zzycxz/fairpeer/internal/deferred"
	"github.com/zzycxz/fairpeer/internal/diff"
	"github.com/zzycxz/fairpeer/internal/event"
	"github.com/zzycxz/fairpeer/internal/hook"
	"github.com/zzycxz/fairpeer/internal/i18n"
	"github.com/zzycxz/fairpeer/internal/jobs"
	"github.com/zzycxz/fairpeer/internal/memory"
	"github.com/zzycxz/fairpeer/internal/nilutil"
	"github.com/zzycxz/fairpeer/internal/permission"
	"github.com/zzycxz/fairpeer/internal/plugin"
	"github.com/zzycxz/fairpeer/internal/present"
	"github.com/zzycxz/fairpeer/internal/provider"
	"github.com/zzycxz/fairpeer/internal/sandbox"
	"github.com/zzycxz/fairpeer/internal/skill"
	"github.com/zzycxz/fairpeer/internal/tool"
)

// ErrTurnRunning reports that a caller tried to start a second foreground turn
// while one is already active in the same Controller.
var ErrTurnRunning = errors.New("turn already running")

// Controller drives one chat session. Construct with New; drive with the command
// methods; observe through the Sink passed in Options.
type Controller struct {
	runner   agent.Runner
	executor *agent.Agent
	// dreamProvider is the lightweight model dream/distill run on (the configured
	// fast_task_model); nil falls back to the executor's main provider. Wiring it
	// here lets the idle-dream path spawn on a cheap model without touching the
	// main agent.
	dreamProvider provider.Provider
	sink          event.Sink
	policy        permission.Policy
	// riskOverrides is the per-tool risk-class map (SPEC v2 §3.2A) built at boot
	// from [[plugins]] risk config, so trusted MCP servers' tools can skip the
	// external-risk default. Used by newInteractiveGate; nil = MCP tools external.
	riskOverrides map[string]permission.RiskClass

	label         string
	systemPrompt  string
	sessionDir    string
	host          *plugin.Host
	commands      []command.Command
	skills        []skill.Skill
	allSkills     []skill.Skill
	skillStore    *skill.Store
	allSkillStore *skill.Store
	hooks         *hook.Runner // session hook runner; nil-safe (no hooks configured)
	mem           *memory.Set
	cleanup       func()
	autoPlan      string
	goalJudge     func(ctx context.Context, prov provider.Provider, transcript []provider.Message, condition string) agent.GoalVerdict
	classifier    autoPlanClassifier
	startedOnce   bool                                                         // guards the one-shot SessionStart hook on first turn
	onRemember    func(rule string) RememberResult                             // set via Options; invoked when user picks "always allow"
	onTurnEnd     func(ctx context.Context, lastUserMsg, lastAssistant string) // set via Options; passive memory capture
	ragContextFn  func(ctx context.Context, query, collection string) string   // set via Options; auto-retrieves knowledge-base context
	ragScope      string                                                       // knowledge-base collection to scope auto-injection to; "" = don't inject (guarded by c.mu)
	// present records the rich presentation event stream (tool dispatches with
	// readOnly/profile/parentId, results with durationMs/truncated/attachments,
	// notice/phase/compaction cards) so a frontend can rebuild the exact transcript
	// a user saw after a tab switch/reload/crash. nil = sidecar disabled (the
	// degraded provider.Message-only history is used). Never feeds the LLM.
	present            *present.Recorder
	presentPath        string       // session path the recorder's records describe (guarded by c.mu); "" = never bound
	lastCompactionDone *event.Event // points at the sink-wrapper's lastDone var; snapshot reads it for SyncBeforeSave

	// jobs is the session-scoped background-job manager. The agent's background
	// tools spawn into it; Compose drains its completion notes into the next turn;
	// Close cancels its still-running jobs.
	jobs *jobs.Manager

	// reg is the live tool registry the executor reads each turn; pluginCtx is the
	// session-scoped context a hot-added stdio server binds its subprocess to.
	// Together they let AddMCPServer connect a server mid-session and have its tools
	// available on the next turn (see AddMCPServer / RemoveMCPServer).
	reg       *tool.Registry
	pluginCtx context.Context

	// Checkpoints (snapshot-based rewind). cp is the per-session store rebound when
	// the session path changes; cpRoot is the workspace root used to guard restore
	// writes. cpTurn is the monotonic turn counter (decoupled from the store so it
	// never collides after a restructure); cpBound[turn] records len(Session.Messages)
	// at that turn's start — the truncation boundary for a conversation rewind/fork.
	// Boundaries are persisted in each checkpoint and rebuilt from the store on
	// resume (so a reopened session can still rewind conversation / fork), but
	// dropped after a summarize restructures the log so those operations report
	// "unavailable" rather than mis-truncating; code rewind (file-based) is unaffected.
	cp      *checkpoint.Store
	cpRoot  string
	cpTurn  int
	cpBound map[int]int

	// promptMu serialises approval and ask prompts so at most one user decision is
	// outstanding at a time (parallel read-only tool calls don't normally gate,
	// writers run serially — but this keeps the contract explicit). Held across
	// the blocking wait, so it must never be taken by the Approve/Answer paths.
	promptMu sync.Mutex

	// mu guards the run state and approval bookkeeping; every critical section
	// under it is short and non-blocking.
	mu         sync.Mutex
	cancel     context.CancelFunc
	running    bool
	autosaveWG sync.WaitGroup
	turnWG     sync.WaitGroup // tracks the in-flight turn goroutine so a cancelling caller can drain it (WaitTurn)
	planMode   bool
	goal       string
	goalStatus string
	goalTurns  int
	goalBlocks int
	goalBlock  string
	// goalIdleTurns counts consecutive goal turns whose assistant reply made no
	// tool calls — a sign the agent is stuck narrating instead of working.
	// Reaching maxGoalIdleTurns stops the goal so the user can intervene.
	// Ported from DeepSeek-Reasonix goal enforcement (idle detection).
	goalIdleTurns int
	// goalStrict requires every todo step to be evidenced (complete_step) and a
	// final self-check before a goal may complete, so a strict goal can't be
	// declared done on the agent's word alone. Off by default; set via SetGoalStrict.
	goalStrict  bool
	sessionPath string
	approvals   map[string]pendingApproval
	asks        map[string]pendingAsk
	granted     map[string]bool
	nextID      int
	// turn counts model turns this session, passed to hooks in their payload.
	turn int
	// approvedPlanAutoApproveTools auto-allows writer tool calls without prompting.
	// Set only while executing a just-approved plan: approving the plan is the
	// go-ahead, so the model shouldn't re-prompt for every write of the work it
	// just got cleared to do. Deny rules still bite (those never reach the
	// approver). Reset when the execution turn returns.
	approvedPlanAutoApproveTools bool

	// idleDreamTimer fires Dream/Distill after the user goes quiet for the
	// configured idle threshold (default 10 min), instead of at turn start. Dream
	// is meant to run in the user's downtime; triggering it while they are
	// actively working contends for model/context resources and interrupts the
	// "consolidate when idle" intent. lastActivity is stamped on every turn and
	// reset; the timer goroutine (idleDreamDone) sleeps until the threshold, then
	// checks cadence + master switch before spawning. Stopped in Close.
	lastActivity time.Time
	idleDreamMu  sync.Mutex
	idleTimer    *time.Timer

	// toolApprovalMode is the runtime approval posture for permission-gated tool
	// calls. "ask" prompts by default, "auto" lets the policy auto-approve the
	// writer fallback while preserving ask/deny rules, and "yolo" skips every
	// tool approval prompt except plan approval. It never answers AskRequest.
	toolApprovalMode string

	// autoApproveTools is "YOLO/full access" mode: while set, every tool approval
	// request is auto-allowed for the rest of the session (writers and bash run
	// without asking). It is a deliberate, session-scoped opt-in (the
	// --dangerously-skip-permissions flag or a runtime toggle), never persisted.
	// Deny rules are unaffected — they're resolved before the approver, so a
	// denied tool is still blocked. It never answers AskRequest or plan approval:
	// those remain user decisions.
	autoApproveTools bool

	// pendingMemory holds memory notes added mid-session (via "#" quick-add or a
	// memory edit) that haven't yet been folded into a turn. Compose drains it
	// onto the next outgoing turn — never into the cache-stable system prefix — so
	// a fresh memory takes effect this session without busting the prompt cache;
	// it joins the prefix naturally on the next session. (some providers do not
	// report cache tokens; the prefix stability still reduces token transmission.)
	pendingMemory []string

	// pendingDeferred holds background results pushed while they could not
	// become their own turn (push delivery's "context only" mode). Compose
	// drains it into the next outgoing turn under <deferred-results>, the same
	// way pendingMemory and the drained job note ride the turn instead of the
	// cache-stable system prefix.
	pendingDeferred []string

	// deferred is the shared background-result coordinator (nil when push
	// delivery is off). The controller is its Deliverer: it decides whether a
	// finished job becomes its own turn (FollowUp) or only rides the next one.
	deferred *deferred.Coordinator

	// closed flips in Close so a late background result is never pushed into a
	// torn-down session.
	closed bool

	displayRecorder func(content, display string)

	// contextFilter, when set, projects session messages into the context sent
	// to the model — a read-side transform (e.g. expert-collab records reduced to
	// synthesis). nil passes messages through unchanged.
	contextFilter func([]provider.Message) []provider.Message
}

type approvalReply struct {
	allow   bool
	session bool
	persist bool // true = write "always allow" rule to config
}

type pendingApproval struct {
	tool      string
	subject   string
	autoDrain bool
	reply     chan approvalReply
}

// pendingAsk is an in-flight ask question batch. questions is retained so the
// AskRequest can be re-emitted to a frontend that reconnected after the original
// event (see ReplayPendingPrompts).
type pendingAsk struct {
	questions []event.AskQuestion
	reply     chan []event.AskAnswer
}

const (
	ToolApprovalAsk  = "ask"
	ToolApprovalAuto = "auto"
	ToolApprovalYolo = "yolo"
)

const (
	maxGoalAutoTurns = 50
	// maxGoalIdleTurns caps consecutive goal turns with no tool calls. Past this
	// the goal stops — the agent is narrating rather than making progress, so
	// surfacing control to the user beats burning the turn budget on talk.
	maxGoalIdleTurns = 2
	goalContinueTurn = "Continue pursuing the active goal. If it is complete, provide the concise final result and end with [goal:complete]. If it is truly blocked on a user-owned decision after trying sensible defaults, end with [goal:blocked:<short reason>]. Otherwise do the next useful work and end with [goal:continue]."
)

// RememberResult describes what happened when an approval rule was persisted.
type RememberResult struct {
	Rule      string
	Path      string
	Saved     bool
	CoveredBy string
	Err       error
}

// Options carries the already-built pieces setup assembles. Lifecycle metadata
// lets the controller mint and rotate session files; Host/Commands are surfaced
// to frontends that resolve MCP prompts and slash commands.
type Options struct {
	Runner        agent.Runner
	Executor      *agent.Agent
	DreamProvider provider.Provider // lightweight model for dream/distill; nil = main
	Sink          event.Sink
	Policy        permission.Policy
	Label         string
	SystemPrompt  string
	SessionDir    string
	SessionPath   string
	Host          *plugin.Host
	Commands      []command.Command
	Skills        []skill.Skill
	AllSkills     []skill.Skill
	SkillStore    *skill.Store
	AllSkillStore *skill.Store
	Hooks         *hook.Runner
	Memory        *memory.Set
	Cleanup       func()
	// Jobs is the session-scoped background-job manager (nil disables background jobs).
	Jobs *jobs.Manager
	// Registry is the executor's live tool set, and PluginCtx the session-scoped
	// context; both are needed for hot-adding MCP servers via AddMCPServer.
	Registry  *tool.Registry
	PluginCtx context.Context
	// WorkspaceRoot is the project root checkpoint restores are confined to ("" =
	// no confinement). Frontends pass the cwd they launched the session in.
	WorkspaceRoot string
	// Present enables the presentation sidecar: the controller records the rich
	// view-only event stream (tool dispatches/results/progress, notice/phase/
	// compaction cards) to <session>.present.jsonl so a frontend can rebuild the
	// exact transcript after a reload. The sidecar never feeds the LLM. Off by
	// default; boot turns it on for desktop sessions that render a rich UI.
	Present  bool
	AutoPlan string
	// GoalJudge enables the independent goal judge: when the model reports
	// [goal:complete], a separate LLM call verifies completion based on the
	// transcript. nil disables the judge (model self-report is trusted).
	GoalJudge  func(ctx context.Context, prov provider.Provider, transcript []provider.Message, condition string) agent.GoalVerdict
	Classifier autoPlanClassifier
	// OnRemember, when set, is invoked with a new allow rule the user chose to
	// persist to disk (e.g. "Bash(go test:*)"). The callback is wired into the
	// permission Gate on EnableInteractiveApproval.
	OnRemember func(rule string) RememberResult
	// OnTurnEnd, when set, is invoked after each turn completes (both the
	// interactive and headless paths). It receives the last user message and
	// the final assistant reply text, enabling passive memory capture: boot
	// wires this to an LLM fact extractor that saves candidate memories with
	// Status "pending" for the user to confirm. The callback must be
	// fire-and-forget — it runs on the turn's goroutine and any error must be
	// swallowed internally so it can never stall or crash the foreground turn.
	// A nil callback disables auto-capture entirely.
	OnTurnEnd func(ctx context.Context, lastUserMsg, lastAssistant string)
	// RAGContextFn, when set, is called with the user's message before each
	// turn to auto-retrieve knowledge-base context. The returned string is
	// prepended to the user's input (like @reference context). A nil callback
	// or "" return disables injection for that turn. collection scopes the
	// retrieval to one knowledge-base collection ("" = let the callback decide,
	// typically meaning "don't inject" for the auto-injection path).
	RAGContextFn func(ctx context.Context, query, collection string) string
}

// New builds a Controller. A nil Sink is replaced with event.Discard.
func New(opts Options) *Controller {
	sink := opts.Sink
	if nilutil.IsNil(sink) {
		sink = event.Discard
	}
	classifier := opts.Classifier
	if nilutil.IsNil(classifier) {
		classifier = nil
	}
	pluginCtx := opts.PluginCtx
	if pluginCtx == nil {
		pluginCtx = context.Background()
	}
	// The presentation recorder captures the rich event stream so a frontend can
	// rebuild the transcript a user saw after a reload. We wrap the sink: every
	// event still flows to the original sink unchanged, and is also fed to the
	// recorder (which drops non-presentable kinds like ApprovalRequest). When
	// Present is off the recorder stays nil and the wrapper is skipped, so CLI /
	// headless paths that don't render a rich UI pay no overhead.
	var rec *present.Recorder
	lastDone := event.Event{} // shared with c via pointer; snapshot reads it
	var lastDonePtr = &lastDone
	if opts.Present {
		rec = present.NewRecorder()
		orig := sink
		sink = event.FuncSink(func(e event.Event) {
			// Remember the most recent successful compaction so snapshot can
			// re-seed the recorder around it when it detects a rewrite.
			if e.Kind == event.CompactionDone && e.Compaction.Summary != "" {
				*lastDonePtr = e
			}
			rec.Append(e)
			orig.Emit(e)
		})
	}
	c := &Controller{
		runner:             opts.Runner,
		executor:           opts.Executor,
		dreamProvider:      opts.DreamProvider,
		sink:               sink,
		policy:             opts.Policy,
		label:              opts.Label,
		systemPrompt:       opts.SystemPrompt,
		sessionDir:         opts.SessionDir,
		sessionPath:        opts.SessionPath,
		host:               opts.Host,
		commands:           opts.Commands,
		skills:             opts.Skills,
		allSkills:          opts.AllSkills,
		skillStore:         opts.SkillStore,
		allSkillStore:      opts.AllSkillStore,
		hooks:              opts.Hooks,
		mem:                opts.Memory,
		cleanup:            opts.Cleanup,
		autoPlan:           normalizeAutoPlan(opts.AutoPlan),
		goalJudge:          opts.GoalJudge,
		classifier:         classifier,
		onRemember:         opts.OnRemember,
		onTurnEnd:          opts.OnTurnEnd,
		ragContextFn:       opts.RAGContextFn,
		jobs:               opts.Jobs,
		reg:                opts.Registry,
		pluginCtx:          pluginCtx,
		cpRoot:             opts.WorkspaceRoot,
		toolApprovalMode:   ToolApprovalAsk,
		approvals:          map[string]pendingApproval{},
		asks:               map[string]pendingAsk{},
		granted:            map[string]bool{},
		present:            rec,
		presentPath:        opts.SessionPath,
		lastCompactionDone: lastDonePtr,
	}
	// Checkpoints: bind a store to the session and route writer pre-edits into it.
	c.rebindCheckpoints(opts.SessionPath)
	if c.executor != nil {
		c.executor.SetPreEditHook(c.PreEditSnapshotter())
		c.executor.SetPostEditHook(c.PostEditHasher())
		c.executor.SetMemoryQueue(c)
	}
	return c
}

// PreEditSnapshotter returns the checkpoint snapshot closure for sub-agent
// spawn sites (task / run_skill): boot threads it into a SharedPreEditHook so
// sub-agent writer tools snapshot into this controller's store — their edits
// rewind exactly like main-loop edits.
func (c *Controller) PreEditSnapshotter() func(diff.Change) {
	return func(ch diff.Change) {
		if c.cp != nil {
			c.cp.Snapshot(ch)
		}
	}
}

// PostEditHasher returns the post-edit hash closure for sub-agent spawn sites
// (mirrors PreEditSnapshotter): after a writer tool completes, the touched
// file's on-disk hash is recorded so RewindPreview can tell agent edits from
// external changes made since.
func (c *Controller) PostEditHasher() func(string) {
	return func(path string) {
		if c.cp != nil {
			c.cp.NotePostEdit(path)
		}
	}
}

// SetContextFilter installs a read-side transform applied to session messages
// before they reach the model. It is the seam the experts engine uses to keep a
// full-fidelity expert-collab message in the transcript while showing the model
// a synthesis-only projection of it. Pass nil to restore the identity filter.
// Safe to call before or after the executor is built; applied (re-applied) on
// the next executor wiring.
func (c *Controller) SetContextFilter(fn func([]provider.Message) []provider.Message) {
	c.mu.Lock()
	c.contextFilter = fn
	if c.executor != nil {
		c.executor.SetContextFilter(fn)
	}
	c.mu.Unlock()
}

// SetDisplayRecorder installs an optional hook used by frontends that persist a
// shorter user-facing transcript than the fully composed model prompt.
func (c *Controller) SetDisplayRecorder(fn func(content, display string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.displayRecorder = fn
}

func (c *Controller) recordDisplay(content, display string) {
	if strings.TrimSpace(display) == "" || content == display {
		return
	}
	c.mu.Lock()
	record := c.displayRecorder
	c.mu.Unlock()
	if record != nil {
		record(content, display)
	}
}

func (c *Controller) recordDisplayForNewUser(startMessages int, display string) {
	if strings.TrimSpace(display) == "" {
		return
	}
	msgs := c.History()
	if startMessages > len(msgs) {
		startMessages = len(msgs)
	}
	for _, m := range msgs[startMessages:] {
		if m.Role == provider.RoleUser {
			c.recordDisplay(provider.ContentString(m.Content), display)
			return
		}
	}
}

// ckptDir derives a session's checkpoint directory from its file path
// (…/<id>.jsonl → …/<id>.ckpt). Empty path → empty (in-memory checkpoints).
func ckptDir(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return strings.TrimSuffix(sessionPath, ".jsonl") + ".ckpt"
}

// rebindCheckpoints points the store at the (possibly new) session, loading any
// checkpoints already on disk, and resets the turn boundaries. Called on
// construction and whenever the session path changes (NewSession/Resume/SetSessionPath).
func (c *Controller) rebindCheckpoints(sessionPath string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cp = checkpoint.New(ckptDir(sessionPath), c.cpRoot)
	c.cpTurn = c.cp.NextTurn() // continue numbering past any checkpoints on disk
	c.cpBound = c.cp.Bounds()  // rebuilt from persisted checkpoints so a resumed
	if c.cpBound == nil {      // session can still rewind conversation / fork
		c.cpBound = map[int]int{}
	}
	// Binding (or re-binding) a session is the moment to push background results
	// that finished while it was closed: a job that failed in the background
	// must not stay invisible until the user happens to ask something.
	if c.deferred != nil && sessionPath != "" {
		coord, path := c.deferred, sessionPath
		go coord.FlushSession(path)
	}
}

// beginCheckpoint opens a checkpoint for the turn about to run, recording the
// current message count as the conversation-rewind boundary. Called at the top of
// runTurn, before the user message is appended.
func (c *Controller) beginCheckpoint(input string) {
	if c.cp == nil || c.executor == nil {
		return
	}
	c.mu.Lock()
	turn := c.cpTurn
	c.cpTurn++
	msgIndex := len(c.executor.Session().Messages)
	c.cpBound[turn] = msgIndex
	c.mu.Unlock()
	c.cp.Begin(turn, input, msgIndex)
}

// --- commands (frontend → controller) ---

// runGuarded runs body on a background goroutine under a fresh cancellable
// context, guarding against concurrent turns and emitting a TurnDone event when
// it finishes (Err set on failure; nil also for a user Cancel). A no-op if a
// turn is already in flight.
func (c *Controller) runGuarded(body func(ctx context.Context) error) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.running = true
	c.mu.Unlock()

	c.autosaveWG.Add(1)
	go func() {
		defer c.autosaveWG.Done()
		c.autosaveWhileRunning(ctx)
	}()
	c.turnWG.Add(1)
	go func() {
		defer c.turnWG.Done()
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				c.mu.Lock()
				c.running = false
				c.cancel = nil
				c.mu.Unlock()
				c.sink.Emit(event.Event{Kind: event.TurnDone, Err: fmt.Errorf("internal error: %v", r)})
			}
		}()
		err := body(ctx)
		c.mu.Lock()
		c.running = false
		c.cancel = nil
		c.mu.Unlock()
		// A user-initiated cancel (ctx.Err != nil) is a normal end of the turn,
		// not a failure: the running LLM/tool returns context.Canceled (or a
		// wrapper). Emit TurnDone with a nil Err so every frontend (CLI/desktop/
		// bot) treats Stop as a clean end instead of surfacing a red "error"
		// notice. Real failures (provider 4xx, panics above) keep their Err.
		if ctx.Err() != nil {
			err = nil
		}
		c.sink.Emit(event.Event{Kind: event.TurnDone, Err: explainError(err)})
		// Follow-up drain (upgrade spec 2-4): prompts queued while the agent
		// was busy start as independent next turns. Only on natural completion
		// — a user-initiated Stop empties nothing (they asked for a halt, not
		// for the backlog to run). Submitted after running was cleared, so
		// runGuarded accepts the new turn.
		if ctx.Err() == nil && c.executor != nil {
			if next, ok := c.executor.DrainFollowUp(); ok {
				c.submit(next, next)
			}
		}
	}()
}

// FollowUp queues a prompt to run as an independent next turn once the current
// one finishes (upgrade spec 2-4). When the controller is idle it just runs
// now. Contrast Steer, which guides the in-flight task instead.
func (c *Controller) FollowUp(input string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return
	}
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if running && c.executor != nil {
		c.executor.FollowUp(trimmed)
		c.notice("queued (next turn): " + truncateForNotice(trimmed))
		return
	}
	c.submit(trimmed, trimmed)
}

// QueuedMessages reports the live steer and follow-up queues for the UI's
// queue strip (upgrade spec 2-3/2-4): steer entries guide the current task,
// follow-up entries will start as independent turns.
func (c *Controller) QueuedMessages() (steer []string, followUp []string) {
	if c.executor == nil {
		return nil, nil
	}
	return c.executor.Steers(), c.executor.FollowUps()
}

// truncateForNotice keeps a queue notice to one glanceable line.
func truncateForNotice(s string) string {
	if len(s) <= 80 {
		return s
	}
	if i := strings.IndexByte(s[:80], '\n'); i >= 0 {
		return s[:i] + "…"
	}
	return s[:80] + "…"
}

// --- Background-result push delivery (internal/deferred) ---
//
// The pull path (jobs.DrainCompletedNote folded in by Compose) only tells the
// model that a job finished, and only when it next runs. Push delivery hands a
// finished job's actual output to a session as soon as it exists — as its own
// turn when the policy says so, otherwise as context plus a notice — with the
// hand-off recorded on disk so a crash before delivery cannot lose it.

// SetDeferred attaches the shared background-result coordinator and installs
// this controller as its Deliverer. Call after the session is bound; calling
// again after a rebind is fine (the coordinator is shared, only the binding
// changes). nil disables push delivery.
func (c *Controller) SetDeferred(coord *deferred.Coordinator) {
	if coord == nil {
		return
	}
	c.mu.Lock()
	c.deferred = coord
	path := c.sessionPath
	c.mu.Unlock()
	coord.SetDeliverer(c)
	// Push anything a previous process left undelivered for this session — a
	// crashed run, or a result that landed while the session was closed.
	go coord.FlushSession(path)
}

// DeferredSessionRunnable implements deferred.Deliverer: results may only be
// pushed into the session that is currently bound, and only while it is open.
func (c *Controller) DeferredSessionRunnable(sessionPath string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	return sessionPath != "" && c.sessionPath == sessionPath
}

// DeliverDeferred implements deferred.Deliverer. With triggerTurn the result
// becomes its own turn (FollowUp runs it now when idle, else queues it as the
// next turn); without it the result only joins the session's context for the
// next turn, so nothing is spent on it yet.
func (c *Controller) DeliverDeferred(sessionPath, text string, triggerTurn bool) error {
	if !c.DeferredSessionRunnable(sessionPath) {
		return deferred.ErrSessionInactive
	}
	if triggerTurn {
		c.FollowUp(text)
		return nil
	}
	c.mu.Lock()
	c.pendingDeferred = append(c.pendingDeferred, text)
	c.mu.Unlock()
	return nil
}

// NotifyDeferred implements deferred.Deliverer: a best-effort user-visible line
// about a finished background result. The full text still rides the next turn,
// so a notice failure never loses the result.
func (c *Controller) NotifyDeferred(_ string, title, body string) error {
	text := strings.TrimSpace(title)
	if b := strings.TrimSpace(body); b != "" {
		if i := strings.IndexByte(b, '\n'); i >= 0 {
			b = b[:i]
		}
		text += " — " + truncateForNotice(b)
	}
	c.notice(text)
	return nil
}

// FlushDeferred pushes results still pending for a session (used when a session
// is (re)bound: the user re-opening a closed session must see what finished
// while they were away).
func (c *Controller) FlushDeferred(sessionPath string) {
	c.mu.Lock()
	coord := c.deferred
	c.mu.Unlock()
	if coord != nil && strings.TrimSpace(sessionPath) != "" {
		coord.FlushSession(sessionPath)
	}
}

// Send starts a turn with an uncomposed message. The controller applies
// auto-plan, plan-mode, memory, and background-job framing inside the async turn
// path so frontends do not block on classifier I/O.
func (c *Controller) Send(input string) {
	c.SendWithRaw(input, input)
}

// SendWithRaw starts a turn with separate model input and raw prompt text. The
// raw prompt is used only for auto-plan scoring; it deliberately excludes
// resolved @-reference payloads so referenced file contents cannot inflate the
// complexity score.
func (c *Controller) SendWithRaw(input, raw string) {
	c.runGuarded(func(ctx context.Context) error { return c.runGoalLoopWithRaw(ctx, input, raw) })
}

// PlanApprovalTool is the Tool name on the ApprovalRequest the controller emits
// to gate a proposed plan. Frontends key their plan-approval UI on it (the
// desktop renders a plan card; the chat TUI a plan banner). This is the single
// source of truth — frontends import this constant instead of re-declaring it.
const PlanApprovalTool = "exit_plan_mode"

// composeReplanTool is the Tool name on the ApprovalRequest the controller
// emits when the compose workflow discovers the approved plan's assumptions
// are wrong and wants to revise the remaining work. Like PlanApprovalTool it
// is never auto-approved (not bypassed by YOLO or the plan-execution window):
// revising a plan is a user decision, even in approval-bypass modes.
const composeReplanTool = "compose_replan"

// PlanApprovedMessage is the follow-up turn sent once the user approves a plan —
// the in-context nudge to execute and keep the (already-seeded) task list honest.
const PlanApprovedMessage = "Plan approved — plan mode is off; you’re cleared to make the changes without asking again. Implement the plan now. Use this serial workflow: 1) mark the first sub-step in_progress with todo_write (this establishes the task list); 2) execute the sub-step; 3) call complete_step with evidence — the host then marks that sub-step completed and moves the next one to in_progress for you. Repeat 2–3 for each remaining sub-step. You don’t need another todo_write to mark steps completed; each complete_step advances the list. Sign off one sub-step at a time — never batch multiple completions."

// runTurn runs one model turn, then applies the plan-approval gate. This is the
// single, frontend-agnostic plan flow: in plan mode the model just researches
// (writers are blocked) and writes its plan as a normal answer — no special tool.
// When the turn ends with a text proposal, the controller asks the user to
// approve (reusing the ApprovalRequest channel both frontends already render);
// on approval it exits plan mode, seeds the task list from the plan, and
// continues straight into execution; on rejection it stays in plan mode so the
// next turn can revise. Plan mode is only ever set interactively, so the headless
// `Run` path (which doesn't call this) never blocks on a prompt.
func (c *Controller) runTurn(ctx context.Context, input string) error {
	return c.runGoalLoopWithRaw(ctx, input, input)
}

// RunTurn executes one foreground turn synchronously through the same lifecycle
// used by interactive frontends: auto-plan, transient memory/background-job
// composition, checkpoints, hooks, and plan approval. It is for transports that
// need a blocking request/response boundary, such as ACP session/prompt.
func (c *Controller) RunTurn(ctx context.Context, input string) error {
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		cancel()
		return ErrTurnRunning
	}
	c.cancel = cancel
	c.running = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.running = false
		c.cancel = nil
		c.mu.Unlock()
		cancel()
	}()
	err := c.runTurn(ctx, input)
	// A user-initiated cancel is a normal end of the turn, not a failure:
	// surface a nil error so callers (bot gateway, ACP) don't treat the
	// context.Canceled as a turn error to display/log.
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (c *Controller) runTurnWithRaw(ctx context.Context, input, raw string) error {
	return c.runTurnWithRawDisplay(ctx, input, raw, "")
}

func (c *Controller) runGoalLoopWithRaw(ctx context.Context, input, raw string) error {
	return c.runGoalLoopWithRawDisplay(ctx, input, raw, "")
}

func (c *Controller) runGoalLoopWithRawDisplay(ctx context.Context, input any, raw, display string) error {
	if err := c.runTurnWithRawDisplay(ctx, input, raw, display); err != nil {
		if ctx.Err() != nil {
			c.stopGoal(GoalStatusStopped)
		}
		return err
	}
	return c.continueGoal(ctx)
}

func (c *Controller) runTurnWithRawDisplay(ctx context.Context, input any, raw, display string) error {
	c.maybeSessionStart(ctx)
	c.ensureSessionPath() // first turn: assign a transcript file so autosave & bot mapping work
	c.touchActivity()     // user is active; the idle-dream countdown restarts from now
	c.maybeAutoPlan(ctx, raw)
	ctx = agent.WithParentSession(ctx, c.parentSessionID())
	composedText := c.Compose(provider.ContentString(input))
	// Apply the transient reasoning-language preference (auto|zh|en). It only
	// steers the visible thinking text and never overrides the final-answer
	// language. Default "auto" is a no-op (WithReasoningLanguage returns content
	// unchanged), so this never perturbs turns when unset. Read live so a desktop
	// settings change takes effect without rebuilding the controller.
	if cfg, err := config.Load(); err == nil {
		composedText = agent.WithReasoningLanguage(composedText, cfg.ReasoningLanguage)
	}
	startMessages := c.messageCount()
	defer c.snapshotActivityIfChanged(startMessages)
	defer c.recordDisplayForNewUser(startMessages, display)
	// Open a checkpoint for this turn before the user message is appended, so the
	// recorded message boundary precedes it and pre-edit snapshots land here.
	c.beginCheckpoint(composedText)
	// UserPromptSubmit / Stop hooks bracket the whole turn (incl. the plan
	// research + approved-execution sub-turns below): a gating UserPromptSubmit
	// aborts before any model call; Stop fires once when the turn returns.
	if c.hooks.Enabled() {
		c.mu.Lock()
		c.turn++
		turn := c.turn
		c.mu.Unlock()
		if block, _ := c.hooks.PromptSubmit(ctx, composedText, turn); block {
			return nil // the hook's notify callback already surfaced the reason
		}
		defer func() { c.hooks.Stop(ctx, lastAssistantText(c.History()), turn) }()
	}
	// Passive memory capture: after the turn completes, hand the last user
	// message + final assistant text to the extractor (if wired). This runs
	// synchronously on the turn goroutine but the extractor is fire-and-forget
	// (10s timeout, swallows all errors), so it can never stall the foreground.
	// A detached context is used so an upstream turn-cancellation (e.g. the user
	// hitting Stop) doesn't abort the extraction mid-flight.
	if c.onTurnEnd != nil {
		defer func() {
			c.onTurnEnd(context.Background(), composedText, lastAssistantText(c.History()))
		}()
	}
	// For multimodal content (images), reconstruct with composed text; otherwise use composed text directly.
	var runInput any
	if parts, ok := input.([]provider.ContentPart); ok {
		composed := []provider.ContentPart{{Type: "text", Text: composedText}}
		// Append only non-text parts (images) from the original
		for _, p := range parts {
			if p.Type != "text" {
				composed = append(composed, p)
			}
		}
		runInput = composed
	} else {
		runInput = composedText
	}
	if err := c.runner.Run(ctx, runInput); err != nil {
		return err
	}
	// Turn reply is complete and shown to the user; arm the idle-dream countdown.
	// If the user stays quiet past the threshold, dream/distill consolidate in
	// the background. Any new turn calls touchActivity above and cancels this.
	c.touchActivity()
	c.mu.Lock()
	plan := c.planMode
	c.mu.Unlock()
	if !plan {
		return nil
	}
	proposal := lastAssistantText(c.History())
	if proposal == "" {
		return nil // no substantive proposal to gate
	}
	// The plan is already visible as the assistant's answer, so the request
	// carries no subject — it's purely the gate.
	allow, _, err := c.requestApproval(ctx, PlanApprovalTool, "", nil)
	if err != nil {
		return err
	}
	if !allow {
		return nil // keep planning; plan mode stays on
	}
	c.SetPlanMode(false)
	seededTodos := c.seedPlanTodos(proposal)
	// The plan is the go-ahead: don't re-prompt for each write of the approved
	// work. Auto-approve writers for the duration of this execution turn only.
	c.mu.Lock()
	c.approvedPlanAutoApproveTools = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.approvedPlanAutoApproveTools = false
		c.mu.Unlock()
	}()
	// Dispatch: large plans (≥ compose.MinTasksForCompose tasks) run through
	// the compose workflow (Implement → Verify → retry), small plans run a
	// single execution turn as before. The split keeps simple plans fast
	// while giving multi-step features a verification loop the bare agent lacks.
	if compose.ShouldCompose(seededTodos) {
		runner := compose.NewRunner(
			c.runner, c.sink, c.ComposeSynthetic,
			func() string { return lastAssistantText(c.History()) },
			PlanApprovedMessage,
			func(ctx context.Context, reason string) (bool, error) {
				allow, _, err := c.requestApproval(ctx, composeReplanTool, reason, nil)
				return allow, err
			},
		)
		// compose's phased implement/verify/review runs are self-bounded by
		// MaxImplementAttempts; the agent's final-answer readiness gate would
		// otherwise force "finish all todos in one turn" or hard-error after
		// maxFinalReadinessBlocks, conflicting with compose's retry semantics.
		// Suppress the gate for the whole compose window, then restore. C3.
		if a, ok := c.runner.(*agent.Agent); ok {
			a.SetSkipReadiness(true)
			defer a.SetSkipReadiness(false)
		}
		if err := runner.Run(ctx, proposal, seededTodos); err != nil {
			return err
		}
		// compose returns nil even when it gave up (attempts exhausted / replan
		// declined) — "not a hard error, work partially landed". Marking the
		// plan fully completed in that case misleads the user into thinking the
		// whole task is done. When compose gave up, leave the todos in their
		// actual state (the gave-up notice already told the user what happened).
		// See audit finding C5.
		if runner.GaveUp() {
			return nil
		}
	} else {
		if err := c.runner.Run(ctx, c.ComposeSynthetic(PlanApprovedMessage)); err != nil {
			return err
		}
	}
	c.completePlanTodos(seededTodos)
	return nil
}

func (c *Controller) continueGoal(ctx context.Context) error {
	for {
		cont := c.advanceGoalAfterTurn(ctx)
		if !cont {
			return nil
		}
		if err := ctx.Err(); err != nil {
			c.stopGoal(GoalStatusStopped)
			return err
		}
		if err := c.runTurnWithRawDisplay(ctx, goalContinueTurn, goalContinueTurn, ""); err != nil {
			if ctx.Err() != nil {
				c.stopGoal(GoalStatusStopped)
			}
			return err
		}
	}
}

func (c *Controller) advanceGoalAfterTurn(ctx context.Context) bool {
	reply := lastAssistantText(c.History())
	status, reason, _ := parseGoalStatusMarker(reply)
	var notice string
	c.mu.Lock()
	if strings.TrimSpace(c.goal) == "" || c.goalStatus != GoalStatusRunning {
		c.mu.Unlock()
		return false
	}
	goal := c.goal
	c.goalTurns++
	switch status {
	case GoalStatusComplete:
		// Independent judge verification: when configured, call a separate
		// model to confirm the goal is actually complete based on the
		// transcript evidence. This prevents premature stops.
		judge := c.goalJudge
		c.mu.Unlock()
		if judge != nil && c.executor != nil && c.executor.Provider() != nil {
			// Derive the judge timeout from the TURN context, not
			// context.Background(), so Cancel() aborts this LLM call
			// immediately instead of parking for up to 60s. The 60s cap
			// still bounds a stuck provider.
			judgeCtx, judgeCancel := context.WithTimeout(ctx, 60*time.Second)
			verdict := judge(judgeCtx, c.executor.Provider(), c.History(), goal)
			judgeCancel()
			if !verdict.OK {
				c.mu.Lock()
				c.notice("goal judge: " + verdict.Reason)
				return true // Continue the goal loop.
			}
		}
		c.mu.Lock()
		// Re-check goal state after re-acquiring the lock: between the unlock
		// above and here, SetGoal("") from another goroutine (user cancel,
		// tab close) may have cleared the goal. Publishing "goal complete" on
		// an already-stopped goal would revive a dead loop (TOCTOU).
		if strings.TrimSpace(c.goal) == "" || c.goalStatus != GoalStatusRunning {
			c.mu.Unlock()
			return false
		}
		// Strict mode: a goal may not be declared complete unless the agent did
		// actual tool-backed work toward it this session — a bare-text "I'm done"
		// is rejected and the loop continues. This pairs with complete_step's
		// evidence system; it does NOT replace the independent judge above.
		strict := c.goalStrict
		if strict && !sessionMadeToolCalls(c.History()) {
			c.goalBlocks = 0
			c.goalBlock = ""
			c.goalIdleTurns = 0
			c.mu.Unlock()
			c.notice("goal strict: completion requires tool-backed work; continuing")
			return true
		}
		c.goal = ""
		c.goalStatus = GoalStatusComplete
		c.goalBlocks = 0
		c.goalBlock = ""
		notice = "goal complete"
	case GoalStatusBlocked:
		reason = cleanGoalBlockReason(reason)
		if reason == "" {
			reason = "blocked"
		}
		if sameGoalBlock(c.goalBlock, reason) {
			c.goalBlocks++
		} else {
			c.goalBlocks = 1
			c.goalBlock = reason
		}
		if c.goalBlocks >= 3 {
			c.goalStatus = GoalStatusBlocked
			notice = "goal blocked: " + reason
		}
	default:
		c.goalBlocks = 0
		c.goalBlock = ""
		// Idle detection: a goal turn whose assistant reply made no tool calls
		// is narrating, not progressing. Count consecutive such turns; past
		// maxGoalIdleTurns, stop the goal and hand control back to the user
		// rather than burning the turn budget on talk. Any tool activity or a
		// marker-bearing reply resets the counter. (PR #4827 goal enforcement.)
		if !lastAssistantMadeToolCalls(c.History()) {
			c.goalIdleTurns++
			if c.goalIdleTurns >= maxGoalIdleTurns {
				c.goalStatus = GoalStatusBlocked
				c.goalBlock = "goal idle: no tool activity for consecutive turns"
				notice = c.goalBlock
			}
		} else {
			c.goalIdleTurns = 0
		}
	}
	// A marker-bearing turn (complete/continue/blocked) always counts as
	// activity, so reset idle on those branches too. Keep this after the switch
	// so only the default branch's idle logic above can set notice.
	if status != "" {
		c.goalIdleTurns = 0
	}
	if notice == "" && c.goalTurns >= maxGoalAutoTurns {
		c.goalStatus = GoalStatusBlocked
		c.goalBlock = "goal continuation limit reached"
		notice = c.goalBlock
	}
	cont := notice == ""
	c.mu.Unlock()
	if notice != "" {
		c.notice(notice)
	}
	return cont
}

func parseGoalStatusMarker(text string) (status, reason string, ok bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		switch lower {
		case "[goal:complete]":
			return GoalStatusComplete, "", true
		case "[goal:continue]":
			return GoalStatusRunning, "", true
		}
		const blockedPrefix = "[goal:blocked:"
		if strings.HasPrefix(lower, blockedPrefix) && strings.HasSuffix(line, "]") {
			return GoalStatusBlocked, strings.TrimSpace(line[len(blockedPrefix) : len(line)-1]), true
		}
		return "", "", false
	}
	return "", "", false
}

func sameGoalBlock(a, b string) bool {
	return normalizeGoalBlockReason(a) == normalizeGoalBlockReason(b)
}

func cleanGoalBlockReason(reason string) string {
	return strings.Trim(strings.TrimSpace(reason), " \t\r\n:：,，.。;；!！?？-—_[]()（）")
}

func normalizeGoalBlockReason(reason string) string {
	reason = strings.ToLower(cleanGoalBlockReason(reason))
	var b strings.Builder
	lastSpace := true
	for _, r := range reason {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		default:
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func (c *Controller) stopGoal(status string) {
	c.mu.Lock()
	if strings.TrimSpace(c.goal) != "" && c.goalStatus == GoalStatusRunning {
		c.goalStatus = status
	}
	c.mu.Unlock()
}

// lastAssistantText returns the content of the most recent assistant message with
// non-empty text — the model's final answer for the turn (its plan, in plan mode).
func lastAssistantText(msgs []provider.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == provider.RoleAssistant && strings.TrimSpace(provider.ContentString(msgs[i].Content)) != "" {
			return provider.ContentString(msgs[i].Content)
		}
	}
	return ""
}

// lastAssistantMadeToolCalls reports whether the most recent assistant message
// carried tool calls — i.e. the agent acted this turn rather than only talking.
// Used by idle detection to spot a goal loop that has stalled into narration.
func lastAssistantMadeToolCalls(msgs []provider.Message) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != provider.RoleAssistant {
			continue
		}
		return len(msgs[i].ToolCalls) > 0
	}
	return false
}

// sessionMadeToolCalls reports whether any assistant turn in the session
// dispatched at least one tool call. A strict goal refuses to complete without
// tool-backed work — a purely conversational "I'm done" does not count.
func sessionMadeToolCalls(msgs []provider.Message) bool {
	for _, m := range msgs {
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

// Submit is the one-call entry for a simple frontend: it takes raw user input
// and does everything — slash-command dispatch, @-reference expansion, plan-mode
// composition — emitting all output as events. The HTTP/SSE server uses this so
// a browser client only POSTs the typed line.
//
// Slash commands route to the matching primitive: /compact, /new, and /clear
// run their session op and emit a Notice; /mcp__server__prompt and custom /commands
// resolve to a turn; an unknown slash emits a Notice. Anything else is a normal
// turn with its @-references resolved first.
func (c *Controller) Submit(input string) {
	c.submit(input, "")
}

// SubmitDisplay runs input as a turn while remembering the user-facing display
// text for transcript replay when controller-side composition expands input.
func (c *Controller) SubmitDisplay(display, input string) {
	c.submit(input, display)
}

func (c *Controller) submit(input, display string) {
	trimmed := strings.TrimSpace(input)
	if note, ok := MemoryQuickAddNote(trimmed); ok {
		c.rememberProjectNote(note)
		return
	}
	if note, ok := RememberCommandNote(trimmed); ok {
		c.rememberProjectNote(note)
		return
	}
	if c.applyPlanCommand(trimmed, display) {
		return
	}
	if c.applyGoalCommand(trimmed, display) {
		return
	}
	if strings.HasPrefix(trimmed, "!") {
		c.RunShell(trimmed[1:])
		return
	}
	switch {
	case trimmed == "/compact" || strings.HasPrefix(trimmed, "/compact "):
		focus := strings.TrimSpace(strings.TrimPrefix(trimmed, "/compact"))
		go func() {
			if err := c.Compact(context.Background(), focus); err != nil {
				c.notice("compaction failed: " + err.Error())
			} else {
				c.notice("compacted")
				if err := c.Snapshot(); err != nil {
					slog.Warn("controller: snapshot after compact", "err", err)
				}
			}
		}()
	case trimmed == "/new":
		go func() {
			if err := c.NewSession(); err != nil {
				c.notice("new session failed: " + err.Error())
			} else {
				c.notice("new session")
			}
		}()
	case trimmed == "/clear":
		go func() {
			if err := c.ClearSession(); err != nil {
				c.notice("clear context failed: " + err.Error())
			} else {
				c.notice("context cleared")
			}
		}()
	case strings.HasPrefix(trimmed, "/mcp__"):
		c.runGuarded(func(ctx context.Context) error {
			sent, found, err := c.MCPPrompt(ctx, trimmed)
			if err != nil {
				return err
			}
			if !found {
				c.notice("unknown command: " + trimmed)
				return nil
			}
			return c.runGoalLoopWithRawDisplay(ctx, sent, sent, display)
		})
	case strings.HasPrefix(trimmed, "//"):
		// Double-slash — not a command. Common in code snippets (JS
		// comments, file:// URLs). Run as a normal turn.
		c.runRefTurn(input, display)
	case strings.HasPrefix(trimmed, "/"):
		if ref, ok := FileRefLine(trimmed); ok {
			c.runRefTurn(ref, display)
			return
		}
		if ref, ok := SlashPathLineRef(trimmed, c.cpRoot); ok {
			c.runRefTurnWithRefs(input, ref, display)
			return
		}
		if SlashPathLikeLine(trimmed) {
			c.runRefTurn(input, display)
			return
		}
		// Read-only management verbs (/model /memory /skills /hooks /mcp) emit a
		// listing Notice, so Submit-based frontends (desktop, HTTP) get them with
		// no extra wiring. (The chat TUI handles these itself with richer output.)
		fields := strings.Fields(trimmed)
		switch fields[0] {
		case "/tree":
			c.notice(c.BranchTreeText())
			return
		case "/branch":
			args := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			if turn, name, fromTurn, err := ParseBranchTarget(args); err != nil {
				c.notice(err.Error())
			} else if fromTurn {
				if _, err := c.ForkNamed(turn-1, name); err != nil {
					c.notice(err.Error())
				}
			} else {
				if _, err := c.Branch(name); err != nil {
					c.notice(err.Error())
				}
			}
			return
		case "/switch":
			ref := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			if _, err := c.SwitchBranch(ref); err != nil {
				c.notice(err.Error())
			}
			return
		case "/rewind":
			args := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			turn, scope, err := parseRewind(args, c.Checkpoints())
			if err != nil {
				c.notice("usage: /rewind [turn] [code|conversation|both]")
				return
			}
			if err := c.Rewind(turn, scope); err != nil {
				c.notice(err.Error())
			}
			return
		}
		if c.managementNotice(trimmed) {
			return
		}
		// A custom command wins over a skill of the same name; both resolve to a
		// turn. (Built-in slash verbs like /compact are handled above.)
		if sent, ok := c.CustomCommand(trimmed); ok {
			c.runGuarded(func(ctx context.Context) error {
				return c.runGoalLoopWithRawDisplay(ctx, sent, sent, display)
			})
			return
		}
		if sent, ok := c.RunSkill(trimmed); ok {
			c.runGuarded(func(ctx context.Context) error {
				return c.runGoalLoopWithRawDisplay(ctx, sent, sent, display)
			})
			return
		}
		c.notice("unknown command: " + trimmed)
	default:
		c.runRefTurn(input, display)
	}
}

func (c *Controller) rememberProjectNote(note string) {
	if note == "" {
		c.notice("nothing to remember")
		return
	}
	if path, err := c.QuickAdd(memory.ScopeProject, note); err != nil {
		c.notice("memory: " + err.Error())
	} else {
		c.notice("remembered → " + path)
	}
}

// applyPlanCommand handles /plan slash commands. /plan toggles plan mode;
// /plan <text> enters plan mode and sends the text as a user turn so the model
// starts planning that task; /plan off exits plan mode. Plan and goal are
// mutually exclusive — entering plan clears any active goal.
func (c *Controller) applyPlanCommand(input, display string) bool {
	cmd, ok := ParsePlanCommand(input)
	if !ok {
		return false
	}
	if cmd.Off {
		c.SetPlanMode(false)
		c.notice("plan mode off")
		return true
	}
	// Enter plan mode (clears goal — they're mutually exclusive).
	c.SetGoal("")
	c.SetPlanMode(true)
	if cmd.Text != "" {
		// /plan <text>: send the task as a user turn so the model starts
		// exploring and planning it in read-only mode immediately.
		c.notice("plan mode on — planning: " + ShortGoalForNotice(cmd.Text))
		c.runGuarded(func(ctx context.Context) error {
			return c.runTurnWithRawDisplay(ctx, cmd.Text, cmd.Text, display)
		})
	} else {
		c.notice("plan mode on — your next message will be planned read-only")
	}
	return true
}

func (c *Controller) applyGoalCommand(input, display string) bool {
	cmd, ok := ParseGoalCommand(input)
	if !ok {
		return false
	}
	switch cmd.Action {
	case GoalCommandSet:
		c.SetPlanMode(false)
		c.SetGoal(cmd.Text)
		c.notice(fmt.Sprintf(i18n.M.GoalSetFmt, ShortGoalForNotice(cmd.Text)))
		if c.runner != nil {
			c.runGuarded(func(ctx context.Context) error {
				return c.runGoalLoopWithRawDisplay(ctx, "Start pursuing the active goal now.", cmd.Text, display)
			})
		}
	case GoalCommandClear:
		c.ClearGoal()
		c.notice(i18n.M.GoalCleared)
	default:
		goal := c.Goal()
		if strings.TrimSpace(goal) == "" {
			c.notice(i18n.M.GoalEmpty)
		} else {
			c.notice(fmt.Sprintf(i18n.M.GoalCurrentFmt, goal))
		}
	}
	return true
}

func ShortGoalForNotice(goal string) string {
	goal = strings.Join(strings.Fields(goal), " ")
	runes := []rune(goal)
	const max = 160
	if len(runes) <= max {
		return goal
	}
	return string(runes[:max]) + "..."
}

// shellTimeout is the maximum time a user-invoked "!command" may run. Matches
// the bash tool's timeout so behaviour is consistent across invocation paths.
const shellTimeout = 120 * time.Second

// shellWaitDelay bounds how long cmd.Run() waits after context cancellation for
// the child's pipes to drain, matching the bash tool's WaitDelay.
const shellWaitDelay = 5 * time.Second

// shellWriter forwards each chunk of shell output to a callback, so RunShell
// can stream live progress to the frontend as the command produces output.
type shellWriter struct{ emit func(string) }

func (w *shellWriter) Write(p []byte) (int, error) {
	w.emit(string(p))
	return len(p), nil
}

// RunShell executes a shell command directly (bypassing the model) and streams
// the output as ToolDispatch/ToolProgress/ToolResult events. It uses the same
// bash-tool infrastructure (shell resolution, timeout) and shares the runGuarded
// lock with model turns — only one can run at a time. User-invoked "!" commands
// run without the OS sandbox (the user typed the command explicitly).
func (c *Controller) RunShell(command string) {
	command = strings.TrimSpace(command)
	if command == "" {
		c.notice(i18n.M.ShellExecEmpty)
		return
	}
	c.runGuarded(func(ctx context.Context) error {
		sh := sandbox.ResolveShell("", "", nil)
		argv, _ := sandbox.Command(sandbox.Spec{}, sh, command) // false = unsandboxed (user invoked)

		preview := []rune(command)
		if len(preview) > 32 {
			preview = preview[:32]
		}
		id := "shell-" + string(preview)

		c.sink.Emit(event.Event{
			Kind: event.ToolDispatch,
			Tool: event.Tool{
				ID:   id,
				Name: "bash",
				Args: fmt.Sprintf(`{"command":%q}`, command),
			},
		})

		ctx, cancel := context.WithTimeout(ctx, shellTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		setShellKillTree(cmd)
		cmd.WaitDelay = shellWaitDelay
		cmd.Dir = c.cpRoot
		var buf bytes.Buffer
		w := io.MultiWriter(&buf, &shellWriter{emit: func(chunk string) {
			c.sink.Emit(event.Event{
				Kind: event.ToolProgress,
				Tool: event.Tool{ID: id, Output: chunk},
			})
		}})
		cmd.Stdout = w
		cmd.Stderr = w
		start := time.Now()
		err := cmd.Run()
		durationMs := time.Since(start).Milliseconds()
		out := buf.String()

		if ctx.Err() == context.DeadlineExceeded {
			c.sink.Emit(event.Event{
				Kind: event.ToolResult,
				Tool: event.Tool{ID: id, Name: "bash", Output: out, Err: fmt.Sprintf(i18n.M.ShellExecTimeoutFmt, shellTimeout), DurationMs: durationMs},
			})
			return nil
		}
		if err != nil {
			c.sink.Emit(event.Event{
				Kind: event.ToolResult,
				Tool: event.Tool{ID: id, Name: "bash", Output: out, Err: fmt.Sprintf(i18n.M.ShellExecFailedFmt, err), DurationMs: durationMs},
			})
			return nil
		}
		c.sink.Emit(event.Event{
			Kind: event.ToolResult,
			Tool: event.Tool{ID: id, Name: "bash", Output: out, DurationMs: durationMs},
		})
		return nil
	})
}

// runRefTurn resolves a line's @references into a context block and starts a
// turn with it prepended (or the raw line when nothing resolved).
func (c *Controller) runRefTurn(input, display string) {
	c.runRefTurnWithRefs(input, input, display)
}

// runRefTurnWithRefs resolves references from refLine while preserving input as
// the user's actual prompt text. This lets compiler diagnostics such as
// "/path/File.kt:12: error" attach @/path/File.kt without rewriting the error.
func (c *Controller) runRefTurnWithRefs(input, refLine, display string) {
	c.runGuarded(func(ctx context.Context) error {
		block, errs := c.ResolveRefs(ctx, refLine)
		for _, e := range errs {
			c.notice(e)
		}
		// Auto-retrieve knowledge-base context (if a RAG store is wired in).
		// ragScope is the collection the user picked in the Composer dropdown;
		// "" means "不使用" → skip injection entirely (default opt-out).
		ragCtx := ""
		if c.ragContextFn != nil {
			c.mu.Lock()
			scope := c.ragScope
			c.mu.Unlock()
			if scope != "" {
				ragCtx = c.ragContextFn(ctx, input, scope)
			}
		}
		var sent any
		// Build the context preamble from @references + RAG auto-search.
		var preamble string
		switch b := block.(type) {
		case string:
			if b != "" {
				preamble += "Referenced context:\n\n" + b + "\n\n"
			}
		case []provider.ContentPart:
			preamble += "Referenced context:\n\n" + provider.ContentString(block) + "\n\n"
		}
		if ragCtx != "" {
			preamble += "知识库参考（自动检索）：\n\n" + ragCtx + "\n\n"
		}
		switch b := block.(type) {
		case []provider.ContentPart:
			// Multimodal: prepend text context, then image parts
			textBlock := preamble + input
			parts := []provider.ContentPart{{Type: "text", Text: textBlock}}
			parts = append(parts, b...)
			sent = parts
		default:
			if preamble != "" {
				sent = preamble + input
			} else {
				sent = input
			}
		}
		return c.runGoalLoopWithRawDisplay(ctx, sent, input, display)
	})
}

// notice emits an informational Notice event.
func (c *Controller) notice(text string) {
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: text})
}

// Run executes a turn synchronously, returning the agent's error. Used by the
// headless `fairpeer run` path, where the Sink renders to stdout and the caller
// just needs the exit status — no TurnDone event, no cancel bookkeeping.
func (c *Controller) Run(ctx context.Context, input string) error {
	c.maybeSessionStart(ctx)
	ctx = agent.WithParentSession(ctx, c.parentSessionID())
	startMessages := c.messageCount()
	defer c.snapshotActivityIfChanged(startMessages)
	if c.hooks.Enabled() {
		c.turn++
		if block, _ := c.hooks.PromptSubmit(ctx, input, c.turn); block {
			return nil
		}
		defer func() { c.hooks.Stop(ctx, lastAssistantText(c.History()), c.turn) }()
	}
	// Passive memory capture (mirrors the interactive path). A detached context
	// keeps the extraction alive even if the headless run's ctx is cancelled.
	if c.onTurnEnd != nil {
		defer func() {
			c.onTurnEnd(context.Background(), input, lastAssistantText(c.History()))
		}()
	}
	return c.runner.Run(ctx, input)
}

// Cancel aborts the in-flight turn. A goroutine blocked awaiting approval
// unblocks via the cancelled context.
func (c *Controller) Cancel() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Running reports whether a turn is currently in flight.
func (c *Controller) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// WaitTurn blocks until no turn goroutine is in flight, or the timeout elapses.
// A caller that just cancelled a running turn uses this to let the goroutine
// finish its final session writes BEFORE taking a closing snapshot (CloseTab,
// shutdown) — otherwise a step completing between Cancel and Snapshot lands
// only in memory, and with the tab/controller already gone no later snapshot
// can ever flush it. Bounded so a stuck provider call can't block teardown.
func (c *Controller) WaitTurn(d time.Duration) {
	done := make(chan struct{})
	go func() {
		c.turnWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// Pause requests a graceful pause of the in-flight turn. Unlike Cancel (which
// aborts and discards partial work), Pause lets the current step finish, then
// freezes the agent with full state preserved so ResumeTurn continues from the
// break point. No-op when no turn is running or when already paused. Safe from
// any goroutine; the run loop checks the pause request at the top of each step.
func (c *Controller) Pause() {
	c.mu.Lock()
	exec := c.executor
	running := c.running
	c.mu.Unlock()
	if exec == nil || !running {
		return
	}
	exec.Pause()
}

// ResumeTurn unblocks a paused turn. Named ResumeTurn (not Resume) to avoid a
// clash with the session-lifecycle Resume(session, path). No-op when not
// paused. Safe from any goroutine.
func (c *Controller) ResumeTurn() {
	c.mu.Lock()
	exec := c.executor
	c.mu.Unlock()
	if exec == nil {
		return
	}
	exec.Resume()
}

// Paused reports whether the in-flight turn is currently frozen on a pause
// (between steps, awaiting ResumeTurn). False when running normally, not
// running, or no executor is bound.
func (c *Controller) Paused() bool {
	c.mu.Lock()
	exec := c.executor
	c.mu.Unlock()
	if exec == nil {
		return false
	}
	return exec.IsPaused()
}

// Turn returns the current turn number (0 before the first submit).
func (c *Controller) Turn() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.turn
}

// Approve answers a pending ApprovalRequest by ID: allow runs the call, session
// also remembers a grant for the rest of the session so the same approval scope
// is not re-prompted. Unknown/expired IDs are ignored.
func (c *Controller) Approve(id string, allow, session, persist bool) {
	c.mu.Lock()
	pending := c.approvals[id]
	delete(c.approvals, id)
	c.mu.Unlock()
	if pending.reply != nil {
		pending.reply <- approvalReply{allow: allow, session: session, persist: persist} // buffered, never blocks
	}
}

// SetRiskOverrides installs the per-tool risk-class map (SPEC v2 §3.2A) built
// from [[plugins]] risk config. Called once at boot after plugins are resolved.
// A nil/empty map keeps the safe default (MCP tools external-risk).
func (c *Controller) SetRiskOverrides(overrides map[string]permission.RiskClass) {
	c.mu.Lock()
	c.riskOverrides = overrides
	c.mu.Unlock()
}

// EnableInteractiveApproval swaps the executor's gate for one that routes
// approval decisions to the frontend via ApprovalRequest events, and wires the
// controller in as the executor's Asker so the `ask` tool can question the user.
// Interactive frontends (chat, desktop) call this; the headless run keeps the
// silent gate and a nil asker from setup.
func (c *Controller) EnableInteractiveApproval() {
	if c.executor != nil {
		c.executor.SetGate(c.newInteractiveGate())
		c.executor.SetAsker(c)

	}
}

func normalizeToolApprovalMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ToolApprovalAuto, "approve", "allow":
		return ToolApprovalAuto
	case ToolApprovalYolo, "full", "full-access", "bypass":
		return ToolApprovalYolo
	default:
		return ToolApprovalAsk
	}
}

func (c *Controller) newInteractiveGate() *permission.Gate {
	policy := c.policy
	c.mu.Lock()
	mode := normalizeToolApprovalMode(c.toolApprovalMode)
	c.mu.Unlock()
	switch mode {
	case ToolApprovalAuto, ToolApprovalYolo:
		policy.Mode = permission.Allow
	default:
		policy.Mode = permission.Ask
	}
	gate := permission.NewGate(policy, gateApprover{c})
	gate.RiskOverrides = c.riskOverrides
	gate.OnRemember = func(rule string) {
		if c.onRemember != nil {
			_ = c.onRemember(rule)
		}
	}
	return gate
}

func (c *Controller) refreshInteractiveGate() {
	if c.executor != nil {
		c.executor.SetGate(c.newInteractiveGate())
	}
}

// Steer queues mid-turn guidance without interrupting the in-flight request.
func (c *Controller) Steer(text string) {
	c.mu.Lock()
	exec := c.executor
	running := c.running
	c.mu.Unlock()
	if exec == nil {
		return
	}
	if running {
		exec.Steer(text)
		return
	}
	// Agent not running — frontend's runningRef was stale.
	// Convert to a new turn so the user gets a response. SubmitDisplay routes
	// through submit → runGuarded, which re-checks running atomically and
	// spawns its own tracked turn goroutine, so calling it synchronously here
	// (instead of a fire-and-forget go func) avoids an untracked goroutine that
	// Close() would not wait on. runGuarded is a no-op if a turn started in the
	// window between the running read above and now.
	c.SubmitDisplay(text, text)
}

// SteerConsumed returns true when the steer queue is empty after the last consume.
func (c *Controller) SteerConsumed() bool {
	c.mu.Lock()
	exec := c.executor
	c.mu.Unlock()
	if exec != nil {
		return exec.SteerConsumed()
	}
	return true
}

// Ask implements agent.Asker: it emits an AskRequest and blocks until
// AnswerQuestion(ID, …) answers or ctx is cancelled. promptMu serialises it
// against tool-approval prompts so at most one user prompt is outstanding.
// Unlike tool-approval gates, Ask is NOT bypassed in YOLO mode — the `ask`
// tool exists to get a genuine user decision, and YOLO only auto-approves
// tool calls; it must not answer the user's questions for them.
func (c *Controller) Ask(ctx context.Context, questions []event.AskQuestion) ([]event.AskAnswer, error) {
	c.promptMu.Lock()
	defer c.promptMu.Unlock()

	c.mu.Lock()
	c.nextID++
	id := strconv.Itoa(c.nextID)
	reply := make(chan []event.AskAnswer, 1)
	c.asks[id] = pendingAsk{questions: questions, reply: reply}
	c.mu.Unlock()

	c.sink.Emit(event.Event{Kind: event.AskRequest, Ask: event.Ask{ID: id, Questions: questions}})

	select {
	case ans := <-reply:
		return ans, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.asks, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// AnswerQuestion resolves a pending AskRequest by ID with the user's selections.
// Unknown/expired IDs are ignored.
func (c *Controller) AnswerQuestion(id string, answers []event.AskAnswer) {
	c.mu.Lock()
	pending, ok := c.asks[id]
	delete(c.asks, id)
	c.mu.Unlock()
	if ok {
		pending.reply <- answers // buffered, never blocks
	}
}

// ReplayPendingPrompts re-emits the ApprovalRequest / AskRequest event for every
// prompt currently blocking the run loop. A frontend that reconnected or reloaded
// after the original event has no way to rebuild its approval/ask modal otherwise,
// so the blocked gate goroutine stays stuck forever while the session shows a
// "waiting" status with no actionable prompt. promptMu serialises Ask and
// requestApproval, so in practice at most one prompt is outstanding; the loops
// stay general so a future concurrent prompt would still replay correctly.
func (c *Controller) ReplayPendingPrompts() {
	c.mu.Lock()
	approvals := make([]event.Approval, 0, len(c.approvals))
	for id, p := range c.approvals {
		approvals = append(approvals, event.Approval{ID: id, Tool: p.tool, Subject: p.subject})
	}
	asks := make([]event.Ask, 0, len(c.asks))
	for id, p := range c.asks {
		asks = append(asks, event.Ask{ID: id, Questions: p.questions})
	}
	c.mu.Unlock()
	for _, a := range approvals {
		c.sink.Emit(event.Event{Kind: event.ApprovalRequest, Approval: a})
	}
	for _, a := range asks {
		c.sink.Emit(event.Event{Kind: event.AskRequest, Ask: a})
	}
}

// SetPlanMode flips the executor's read-only gate without touching the
// cache-stable prompt prefix, and remembers the state so Compose can prepend the
// plan-mode marker to outgoing turns.
func (c *Controller) SetPlanMode(v bool) {
	c.mu.Lock()
	c.planMode = v
	// Install/clear HardDeny as a data-layer guarantee. When plan mode is on,
	// all writer tools (edit_file, write_file, bash, task, run_skill, ...) are
	// hard-denied at the permission layer — not just by the runtime boolean
	// gate in agent.executeOne. This means even a user who configured
	// "*": "allow" cannot bypass plan-mode read-only. When plan mode is off,
	// HardDeny is cleared so normal operation resumes.
	if v {
		writerTools := []string{
			"edit_file", "write_file", "multi_edit",
			"run_skill",
			"task",
		}
		// Bash is handled by ReadOnlyCallChecker (per-command read-only
		// detection) in agent.executeOne, so we don't hard-deny it here —
		// that would block read-only commands like "git log" that plan mode
		// intentionally allows.
		rules := make([]string, 0, len(writerTools))
		rules = append(rules, writerTools...)
		c.policy = c.policy.SetHardDeny(rules)
	} else {
		c.policy = c.policy.SetHardDeny(nil)
	}
	c.mu.Unlock()
	if c.executor != nil {
		c.executor.SetPlanMode(v)
	}
}

// SetAutoPlan updates the interactive auto-plan gate for subsequent turns.
func (c *Controller) SetAutoPlan(mode string) {
	c.mu.Lock()
	c.autoPlan = normalizeAutoPlan(mode)
	c.mu.Unlock()
}

// PlanMode reports whether outgoing turns currently receive the plan-mode
// marker. Frontends use it after Compose because auto-plan may flip the mode.
func (c *Controller) PlanMode() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.planMode
}

// SetGoal stores a session-scoped active goal. Compose injects it into outgoing
// user turns, not the system prompt or tool schema, so it does not disturb the
// cache-stable prefix.
func (c *Controller) SetGoal(goal string) {
	goal = strings.TrimSpace(goal)
	c.mu.Lock()
	defer c.mu.Unlock()
	if goal == "" {
		c.goal = ""
		c.goalStatus = GoalStatusStopped
		c.goalTurns = 0
		c.goalBlocks = 0
		c.goalBlock = ""
		c.goalIdleTurns = 0
		return
	}
	if c.goal == goal && c.goalStatus == GoalStatusRunning {
		return
	}
	c.goal = goal
	c.goalStatus = GoalStatusRunning
	c.goalTurns = 0
	c.goalBlocks = 0
	c.goalBlock = ""
	c.goalIdleTurns = 0
}

// SetGoalStrict toggles strict enforcement on the active goal: when on, the goal
// may not be declared complete unless the session produced tool-backed work, so
// a bare "I'm done" cannot satisfy it. Setting strict on a stopped/no goal is
// remembered but has no effect until a goal runs. (PR #4827 goal enforcement.)
func (c *Controller) SetGoalStrict(strict bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.goalStrict = strict
}

// GoalStrict reports whether the active goal is under strict enforcement.
func (c *Controller) GoalStrict() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.goalStrict
}

func (c *Controller) ClearGoal() {
	c.SetGoal("")
}

func (c *Controller) Goal() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.goal
}

func (c *Controller) GoalStatus() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(c.goal) == "" && c.goalStatus == "" {
		return GoalStatusStopped
	}
	if c.goalStatus == "" {
		return GoalStatusStopped
	}
	return c.goalStatus
}

// Compact runs one compaction pass on the executor's session on demand.
// instructions is optional `/compact <focus>` guidance steering what to keep.
func (c *Controller) Compact(ctx context.Context, instructions string) error {
	if c.executor == nil {
		return nil
	}
	return c.executor.CompactNow(ctx, instructions)
}

// maybeSessionStart fires the SessionStart hook exactly once per session, lazily
// on the first turn — by then the sink/notify is wired, and a resumed session
// fires it too (its first post-resume turn).
func (c *Controller) maybeSessionStart(ctx context.Context) {
	c.mu.Lock()
	if c.startedOnce {
		c.mu.Unlock()
		return
	}
	c.startedOnce = true
	c.mu.Unlock()
	c.hooks.SessionStart(ctx)
}

// NewSession snapshots the current conversation, rotates to a fresh file, and
// resets the executor to a clean session carrying the same system prompt. It
// ends the old session and starts the new one for lifecycle hooks.
func (c *Controller) NewSession() error {
	if c.executor == nil {
		return nil
	}
	// Refuse while a turn is running: the agent's loop appends to the live
	// Session, and swapping it out mid-turn tears the transcript. The check and
	// the swap below are each under c.mu; between them only non-session work
	// (snapshot, hooks) happens, so runGuarded cannot start a turn that writes
	// to a session we are about to replace.
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return fmt.Errorf("cannot start a new session while a turn is running")
	}
	c.mu.Unlock()
	if err := c.Snapshot(); err != nil {
		return err
	}
	c.hooks.SessionEnd(context.Background())
	newSess := agent.NewSession(c.systemPrompt)
	c.mu.Lock()
	c.executor.SetSession(newSess)
	if c.sessionDir != "" {
		c.sessionPath = agent.NewSessionPath(c.sessionDir, c.label)
	}
	c.startedOnce = true // NewSession fires SessionStart itself; don't re-fire on the next turn
	path := c.sessionPath
	c.mu.Unlock()
	c.rebindPresent(path)
	c.rebindCheckpoints(path)
	c.hooks.SessionStart(context.Background())
	return nil
}

// ClearSession discards the current conversation without preserving it in
// resume/history, then rotates to a clean session carrying the same system prompt.
func (c *Controller) ClearSession() error {
	if c.executor == nil {
		return nil
	}
	c.mu.Lock()
	running := c.running
	oldPath := c.sessionPath
	c.mu.Unlock()
	if running {
		return fmt.Errorf("cannot clear while a turn is running")
	}
	if err := removeSessionArtifacts(oldPath); err != nil {
		return err
	}
	c.hooks.SessionEnd(context.Background())
	newSess := agent.NewSession(c.systemPrompt)
	// Swap the session under one critical section so a turn that passes the
	// running check and starts between the unlock above and here cannot write a
	// session we are about to replace (transcript tear). running was false at
	// the check; this lock is held continuously across the swap.
	c.mu.Lock()
	c.executor.SetSession(newSess)
	if c.sessionDir != "" {
		c.sessionPath = agent.NewSessionPath(c.sessionDir, c.label)
	}
	c.startedOnce = true
	path := c.sessionPath
	c.mu.Unlock()
	c.rebindPresent(path)
	c.rebindCheckpoints(path)
	c.hooks.SessionStart(context.Background())
	return nil
}

func removeSessionArtifacts(path string) error {
	if path == "" {
		return nil
	}
	for _, p := range []string{path, agent.BranchMetaPath(path)} {
		if p == "" {
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if dir := ckptDir(path); dir != "" {
		if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// RewindScope selects what a Rewind restores.
type RewindScope int

const (
	RewindCode         RewindScope = iota // files only
	RewindConversation                    // message log only
	RewindBoth                            // both
)

// Checkpoints lists the session's rewind points (one per user turn), oldest first.
func (c *Controller) Checkpoints() []checkpoint.Meta {
	if c.cp == nil {
		return nil
	}
	return c.cp.List()
}

// CheckpointDiff (upgrade spec 3-6) returns the per-file changes a code-scope
// rewind of `turn` would make, so the rewind UI can preview instead of asking
// the user to trust a file count.
func (c *Controller) CheckpointDiff(turn int) []diff.Change {
	if c.cp == nil {
		return nil
	}
	return c.cp.DiffForTurn(turn)
}

// RewindFileClass is one path's ZCode-style safety verdict for a code rewind:
// safe (unchanged since the agent's last write — rewinding loses nothing
// external), unsafe (modified or removed outside the agent since — the rewind
// would clobber that), ignored (no hash provenance — legacy snapshots — it
// will be restored but can't be vouched for).
type RewindFileClass struct {
	Path  string
	Class string // "safe" | "unsafe" | "ignored"
}

// RewindPreview classifies every file a code-scope rewind of `turn` would
// restore, comparing the current on-disk hash against the last hash the
// agent's own writes produced.
func (c *Controller) RewindPreview(turn int) []RewindFileClass {
	if c.cp == nil {
		return nil
	}
	out := []RewindFileClass{}
	for _, fi := range c.cp.SuffixInfo(turn) {
		cur := c.cp.CurrentHash(fi.Path)
		switch {
		case fi.PostHash != "":
			if cur == fi.PostHash {
				out = append(out, RewindFileClass{Path: fi.Path, Class: "safe"})
			} else {
				out = append(out, RewindFileClass{Path: fi.Path, Class: "unsafe"})
			}
		case fi.PreHash != "":
			if cur == fi.PreHash {
				out = append(out, RewindFileClass{Path: fi.Path, Class: "safe"})
			} else {
				out = append(out, RewindFileClass{Path: fi.Path, Class: "ignored"})
			}
		default:
			out = append(out, RewindFileClass{Path: fi.Path, Class: "ignored"})
		}
	}
	return out
}

// rewindKeepPrefix marks the synthetic reverse checkpoint a code rewind leaves
// behind; restoring back to it replays (reapplies) the rewound edits.
const rewindKeepPrefix = "rewind-keep"

// rewindFail emits the error as a Warn notice (so a frontend that swallows the
// returned error — e.g. the desktop bridge's .catch — still shows the user why
// the rewind did nothing) and returns it.
func (c *Controller) rewindFail(err error) error {
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: err.Error()})
	return err
}

// Rewind restores the session to the start of `turn`: Code reverts every file that
// turn (or a later one) changed to its pre-turn content; Conversation truncates the
// message log back to that turn; Both does both. Refused while a turn is running.
// Conversation rewind relies on the live boundary recorded at turn start, so it is
// unavailable for turns inherited from a resumed session (code rewind still works).
// Frontends re-render their transcript from History after the call.
func (c *Controller) Rewind(turn int, scope RewindScope) error {
	if c.cp == nil || c.executor == nil {
		return c.rewindFail(fmt.Errorf("checkpoints unavailable"))
	}
	c.mu.Lock()
	running := c.running
	boundary, hasBound := c.cpBound[turn]
	c.mu.Unlock()
	if running {
		return c.rewindFail(fmt.Errorf("cannot rewind while a turn is running"))
	}

	if scope == RewindConversation || scope == RewindBoth {
		if !hasBound {
			return c.rewindFail(fmt.Errorf("conversation rewind unavailable for turn %d (resumed session)", turn))
		}
		s := c.executor.Session()
		// boundary is the message-log index at turn start; compaction shrinks the
		// log without rewriting boundaries, so a stale boundary past the end means
		// the turn was compacted away — fail loudly instead of skipping silently.
		if boundary > len(s.Messages) {
			return c.rewindFail(fmt.Errorf("conversation rewind unavailable for turn %d: the conversation was compacted past this point", turn))
		}
		s.Messages = s.Messages[:boundary]
		c.mu.Lock()
		c.cpTurn = turn // renumber future turns from here; later turns are gone
		for k := range c.cpBound {
			if k >= turn {
				delete(c.cpBound, k)
			}
		}
		c.mu.Unlock()
		if err := c.Snapshot(); err != nil {
			slog.Warn("controller: snapshot after rewind", "err", err)
		}
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
			Text: fmt.Sprintf("rewound conversation to turn %d", turn)})
	}
	if scope == RewindCode || scope == RewindBoth {
		// Reverse keep (reapply support): capture the CURRENT values of every
		// suffix path as a synthetic checkpoint BEFORE restoring, so the rewind
		// itself is replayable. After the conversation branch above so the
		// synthetic turn's boundary matches the post-truncation log.
		if paths := c.cp.SuffixPaths(turn); len(paths) > 0 {
			c.cp.KeepCurrent(fmt.Sprintf("%s → %d", rewindKeepPrefix, turn), len(c.executor.Session().Messages), paths)
		}
		written, deleted, err := c.cp.RestoreCode(turn)
		if err != nil {
			return c.rewindFail(fmt.Errorf("rewind code: %w", err))
		}
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
			Text: fmt.Sprintf("rewound code to turn %d — %d file(s) restored, %d removed", turn, len(written), len(deleted))})
	}
	return nil
}

// Fork branches the conversation at the start of turn into a NEW session file,
// preserving the current one as the branch point, and switches to the branch. Code
// is untouched (it's a conversation operation). Like a conversation rewind it needs
// the live boundary, so it is unavailable for resumed-session turns and refused
// while a turn runs. Returns the new session path.
func (c *Controller) Fork(turn int) (string, error) {
	return c.ForkNamed(turn, "")
}

func (c *Controller) ForkNamed(turn int, name string) (string, error) {
	return c.forkNamed(turn, name, true)
}

// ForkSession copies the conversation at the start of turn into a new session
// file without switching this controller to it. Desktop uses this to open the
// branch in a new tab while the source tab keeps its current transcript.
func (c *Controller) ForkSession(turn int, name string) (string, error) {
	return c.forkNamed(turn, name, false)
}

func (c *Controller) forkNamed(turn int, name string, switchToFork bool) (string, error) {
	if c.executor == nil {
		return "", c.rewindFail(fmt.Errorf("checkpoints unavailable"))
	}
	if c.sessionDir == "" {
		return "", c.rewindFail(fmt.Errorf("fork needs session persistence, which is disabled"))
	}
	c.mu.Lock()
	running := c.running
	boundary, hasBound := c.cpBound[turn]
	c.mu.Unlock()
	if running {
		return "", c.rewindFail(fmt.Errorf("cannot fork while a turn is running"))
	}
	if !hasBound {
		return "", c.rewindFail(fmt.Errorf("fork unavailable for turn %d (resumed session)", turn))
	}

	// Persist the current conversation first so the branch point survives, then
	// seed a fresh session with the messages up to the fork and switch to it.
	if err := c.Snapshot(); err != nil {
		slog.Warn("controller: pre-fork snapshot", "err", err)
	}
	parentPath := c.SessionPath()
	parentID := agent.BranchID(parentPath)
	src := c.executor.Session().Snapshot()
	if boundary > len(src) {
		boundary = len(src)
	}
	forked := append([]provider.Message(nil), src[:boundary]...)
	sess := agent.NewSession("")
	sess.Messages = forked

	newPath := agent.NewSessionPath(c.sessionDir, c.label)
	if err := sess.Save(newPath); err != nil {
		return "", c.rewindFail(err)
	}
	if err := agent.SaveBranchMeta(newPath, agent.BranchMeta{
		Name:             strings.TrimSpace(name),
		ParentID:         parentID,
		ForkTurn:         turn,
		ForkMessageIndex: boundary,
	}); err != nil {
		return "", c.rewindFail(err)
	}
	if switchToFork {
		c.executor.SetSession(sess)
		c.mu.Lock()
		c.sessionPath = newPath
		c.mu.Unlock()
		c.rebindPresent(newPath)
		c.rebindCheckpoints(newPath)
	}
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
		Text: fmt.Sprintf("forked conversation at turn %d into a new session", turn)})
	return newPath, nil
}

func (c *Controller) CheckpointHasBoundary(turn int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.cpBound[turn]
	return ok
}

// Branch copies the current conversation into a child branch and switches to it.
// Unlike Fork, it branches at the current tip and does not require a checkpoint.
func (c *Controller) Branch(name string) (string, error) {
	if c.executor == nil {
		return "", c.rewindFail(fmt.Errorf("branch unavailable"))
	}
	if c.sessionDir == "" {
		return "", c.rewindFail(fmt.Errorf("branch needs session persistence, which is disabled"))
	}
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if running {
		return "", c.rewindFail(fmt.Errorf("cannot branch while a turn is running"))
	}
	if !c.executor.Session().HasContent() {
		return "", c.rewindFail(fmt.Errorf("nothing to branch yet"))
	}
	if err := c.Snapshot(); err != nil {
		return "", c.rewindFail(err)
	}
	parentPath := c.SessionPath()
	parentID := agent.BranchID(parentPath)
	src := c.executor.Session().Snapshot()
	branched := append([]provider.Message(nil), src...)
	sess := agent.NewSession("")
	sess.Messages = branched

	newPath := agent.NewSessionPath(c.sessionDir, c.label)
	if err := sess.Save(newPath); err != nil {
		return "", c.rewindFail(err)
	}
	if err := agent.SaveBranchMeta(newPath, agent.BranchMeta{
		Name:             strings.TrimSpace(name),
		ParentID:         parentID,
		ForkTurn:         -1,
		ForkMessageIndex: len(branched),
	}); err != nil {
		return "", c.rewindFail(err)
	}
	c.executor.SetSession(sess)
	c.mu.Lock()
	c.sessionPath = newPath
	c.mu.Unlock()
	c.rebindPresent(newPath)
	c.rebindCheckpoints(newPath)
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
		Text: fmt.Sprintf("created branch %s", agent.BranchID(newPath))})
	return newPath, nil
}

// Branches lists saved conversation branches in this controller's session dir.
func (c *Controller) Branches() ([]agent.BranchInfo, error) {
	if c.sessionDir == "" {
		return nil, fmt.Errorf("session persistence is disabled")
	}
	if err := c.Snapshot(); err != nil {
		return nil, err
	}
	return agent.ListBranches(c.sessionDir)
}

func (c *Controller) SwitchBranch(ref string) (agent.BranchInfo, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return agent.BranchInfo{}, c.rewindFail(fmt.Errorf("usage: /switch <branch id|name>"))
	}
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if running {
		return agent.BranchInfo{}, c.rewindFail(fmt.Errorf("cannot switch branches while a turn is running"))
	}
	branches, err := c.Branches()
	if err != nil {
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	match, err := resolveBranch(branches, ref)
	if err != nil {
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	loaded, err := agent.LoadSession(match.Path)
	if err != nil {
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	if c.executor != nil {
		c.executor.SetSession(loaded)
	}
	c.mu.Lock()
	c.sessionPath = match.Path
	c.mu.Unlock()
	c.rebindPresent(match.Path)
	c.rebindCheckpoints(match.Path)
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
		Text: fmt.Sprintf("switched to branch %s", branchDisplayName(match))})
	return match, nil
}

func resolveBranch(branches []agent.BranchInfo, ref string) (agent.BranchInfo, error) {
	refLower := strings.ToLower(ref)
	var matches []agent.BranchInfo
	for _, b := range branches {
		nameLower := strings.ToLower(strings.TrimSpace(b.Name))
		switch {
		case b.ID == ref || strings.EqualFold(b.ID, ref):
			return b, nil
		case b.Name != "" && nameLower == refLower:
			matches = append(matches, b)
		case strings.HasPrefix(strings.ToLower(b.ID), refLower):
			matches = append(matches, b)
		case strings.HasPrefix(strings.ToLower(shortBranchID(b.ID)), refLower):
			matches = append(matches, b)
		case b.Path == ref:
			return b, nil
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return agent.BranchInfo{}, fmt.Errorf("branch %q is ambiguous", ref)
	}
	return agent.BranchInfo{}, fmt.Errorf("branch %q not found", ref)
}

func branchDisplayName(b agent.BranchInfo) string {
	if strings.TrimSpace(b.Name) != "" {
		return fmt.Sprintf("%s (%s)", b.Name, b.ID)
	}
	return b.ID
}

// SummarizeFrom compresses the conversation from turn onward into one summary;
// SummarizeUpTo compresses everything before it. Both are Claude Code's "summarize
// from/up to here" — they restructure the message log (keeping code untouched), so
// afterwards the per-turn boundaries no longer map and conversation rewind/fork
// report "unavailable" until new turns rebuild them (code rewind, file-based, is
// unaffected). Refused while a turn runs; need the live boundary.
func (c *Controller) SummarizeFrom(ctx context.Context, turn int) error {
	return c.summarizeAt(ctx, turn, true)
}

func (c *Controller) SummarizeUpTo(ctx context.Context, turn int) error {
	return c.summarizeAt(ctx, turn, false)
}

func (c *Controller) summarizeAt(ctx context.Context, turn int, from bool) error {
	if c.executor == nil {
		return c.rewindFail(fmt.Errorf("checkpoints unavailable"))
	}
	c.mu.Lock()
	running := c.running
	boundary, hasBound := c.cpBound[turn]
	c.mu.Unlock()
	if running {
		return c.rewindFail(fmt.Errorf("cannot summarize while a turn is running"))
	}
	if !hasBound {
		return c.rewindFail(fmt.Errorf("summarize unavailable for turn %d (resumed session)", turn))
	}
	var err error
	if from {
		err = c.executor.SummarizeFrom(ctx, boundary)
	} else {
		err = c.executor.SummarizeUpTo(ctx, boundary)
	}
	if err != nil {
		return c.rewindFail(err)
	}
	// The log was restructured; existing boundaries no longer map. Drop them (keep
	// cpTurn monotonic so new turns don't collide with the store) — conversation
	// rewind degrades to "unavailable" until fresh turns rebuild boundaries.
	c.mu.Lock()
	c.cpBound = map[int]int{}
	c.mu.Unlock()
	if err := c.Snapshot(); err != nil {
		slog.Warn("controller: post-summarize snapshot", "err", err)
	}
	return nil
}

// Resume seeds the session from a loaded transcript and pins the active file to
// its path so auto-save keeps appending there.
func (c *Controller) Resume(s *agent.Session, path string) {
	if c.executor != nil {
		c.executor.SetSession(s)
	}
	c.mu.Lock()
	c.sessionPath = path
	c.mu.Unlock()
	c.rebindPresent(path)
	c.rebindCheckpoints(path)
	c.maybeColdResumePrune(path)
	c.restoreModeFromMeta(path)
}

// rebindPresent keeps the presentation recorder aligned with the session the
// controller just (re)bound to. When the path CHANGED, the accumulated records
// describe the old conversation — reset and re-seed from the new session's
// sidecar so they neither leak into the new file on the next Save nor get
// served by PresentRecords as if they belonged to it. Rebinding the same path
// (or the first binding) keeps the live records and just seeds, so a
// PresentRecords/Save issued during the session's first ~30s (before the
// mid-turn autosave flushes) still sees prior turns' rich fields and cards.
// No-op without a recorder (CLI/headless). Best-effort: a seed read failure is
// logged, not propagated.
func (c *Controller) rebindPresent(path string) {
	if c.present == nil {
		return
	}
	c.mu.Lock()
	changed := c.presentPath != "" && c.presentPath != path
	c.presentPath = path
	c.mu.Unlock()
	if changed {
		c.present.Reset()
	}
	if err := c.present.SeedFromPath(present.PresentPath(path)); err != nil {
		slog.Warn("controller: present sidecar seed on rebind", "err", err)
	}
}

// PresentRecords returns the presentation records for the current session from
// the in-memory recorder, so in-process consumers see every event up to the
// call — reading the sidecar on disk instead would lag by up to the mid-turn
// autosave interval (30s) and drop the newest tool cards exactly when a
// frontend rebuilds mid-run. The recorder is seeded from disk on first access,
// so turns recorded before this controller was built are included. ok is false
// when the presentation recorder is disabled (CLI/headless); callers fall back
// to reading the sidecar file themselves.
func (c *Controller) PresentRecords() (records []present.Record, rewriteVersion int, ok bool) {
	if c.present == nil {
		return nil, 0, false
	}
	if err := c.present.SeedFromPath(present.PresentPath(c.SessionPath())); err != nil {
		slog.Warn("controller: present sidecar seed on read", "err", err)
	}
	return c.present.Records(), c.present.RewriteVersion(), true
}

// restoreModeFromMeta reads the plan mode and tool-approval stance from the
// session's BranchMeta sidecar and re-applies them. This closes the gap where
// a user who had Shift+Tab'd into plan mode or toggled YOLO, then restarted
// with --resume/--continue, lost that runtime state and silently dropped back
// to defaults (plan off / ask). Goal is intentionally NOT restored (it would
// auto-resume the goal loop; see BranchMeta.Goal comment). See audit C8.
func (c *Controller) restoreModeFromMeta(path string) {
	if path == "" {
		return
	}
	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok {
		return
	}
	// PlanMode: restore as-is (it only restricts writes; safe to resume).
	c.SetPlanMode(meta.PlanMode)
	// ToolApprovalMode: restore only if explicitly recorded (omitempty → empty
	// means "legacy sidecar, leave the boot default untouched").
	if meta.ToolApprovalMode != "" {
		c.SetToolApprovalMode(meta.ToolApprovalMode)
	}
	// Tell the user what was restored so a resumed plan/YOLO mode isn't a
	// silent surprise ("why can't I write files?"). Only emit when something
	// non-default was actually restored — empty legacy sidecars stay quiet.
	// See UX review finding P1.
	var parts []string
	if meta.PlanMode {
		parts = append(parts, "plan mode (Shift+Tab to exit)")
	}
	if meta.ToolApprovalMode != "" && meta.ToolApprovalMode != "ask" {
		parts = append(parts, meta.ToolApprovalMode+" approval mode")
	}
	if len(parts) > 0 {
		c.notice("resumed session with " + strings.Join(parts, " + ") + " still active")
	}
}

// cacheColdAfter approximates how long the provider keeps a prompt prefix
// cached. A session idle longer than this resumes against a cold cache, so a
// history rewrite at that moment costs no extra cache misses — it only shrinks
// the full-price first request. Deliberately conservative: too small burns a
// live cache (~4× the miss tokens, measured), too large only forgoes a prune.
// Tighten from benchmarks/cache-ttl-probe data, never below measured retention.
var cacheColdAfter = 24 * time.Hour

// maybeColdResumePrune elides stale tool results when a resumed session has
// been idle past the provider's cache retention, then persists the pruned
// transcript so the saved file and the prompt stay in sync.
func (c *Controller) maybeColdResumePrune(path string) {
	if c.executor == nil || path == "" {
		return
	}
	// Idle time comes from branch meta only — every session the controller has
	// ever snapshotted carries one. A meta-less transcript (e.g. a legacy import
	// not yet saved) skips the prune until its first snapshot creates the meta.
	m, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok || m.UpdatedAt.IsZero() {
		return
	}
	last := m.UpdatedAt
	if time.Since(last) < cacheColdAfter {
		return
	}
	st, err := c.executor.PruneStaleToolResults()
	if err != nil || st.Results == 0 {
		return
	}
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: fmt.Sprintf(
		"resumed after %s idle (provider cache expired) — elided %d stale tool results to cheapen the cold restart",
		time.Since(last).Round(time.Minute), st.Results)})
	if err := c.Snapshot(); err != nil {
		slog.Warn("controller: post-prune snapshot", "err", err)
	}
}

// Snapshot writes the executor's conversation to the active session file. No-op
// when persistence is unavailable or the session has never been used (no user
// interaction). Called after every turn so a crash loses at most one in-flight
// prompt.
func (c *Controller) Snapshot() error {
	return c.snapshot(false)
}

// SnapshotActivity writes the active conversation and marks the session as
// recently active. Use it only after a real user/model turn changes the
// transcript; switch/close snapshots should call Snapshot so they do not reorder
// recent-session pickers.
func (c *Controller) SnapshotActivity() error {
	return c.snapshot(true)
}

// midTurnSnapshotInterval is atomic (nanoseconds) so a test shrinking it
// cannot race a previous test's still-parking autosave goroutine.
var midTurnSnapshotInterval atomic.Int64

func init() { midTurnSnapshotInterval.Store(int64(30 * time.Second)) }

// autosaveWhileRunning snapshots the session periodically while a turn runs,
// so an abrupt kill (SSH drop, force-quit) loses at most one interval of a
// long turn instead of all of it (#3772). Session.Save copies under the lock
// and replaces the file atomically, so racing the turn's appends is safe.
func (c *Controller) autosaveWhileRunning(ctx context.Context) {
	t := time.NewTicker(time.Duration(midTurnSnapshotInterval.Load()))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.snapshot(false); err != nil {
				slog.Warn("controller: mid-turn snapshot", "err", err)
			}
		}
	}
}

func (c *Controller) snapshot(markActivity bool) error {
	c.mu.Lock()
	path := c.sessionPath
	c.mu.Unlock()
	if c.executor == nil || path == "" {
		return nil
	}
	s := c.executor.Session()
	if !s.HasContent() {
		return nil
	}
	// Seed the presentation recorder from the on-disk sidecar the first time we
	// snapshot. A freshly-built controller (after close+reopen, or app restart)
	// starts with an empty recorder; without seeding, the first post-reopen Save
	// would full-rewrite the sidecar with only the new turn's records, erasing
	// every prior turn's rich fields and cards. SeedFromPath is idempotent.
	if c.present != nil {
		if err := c.present.SeedFromPath(present.PresentPath(path)); err != nil {
			slog.Warn("controller: present sidecar seed", "err", err)
		}
	}
	if !markActivity {
		if _, err := agent.EnsureBranchMeta(path); err != nil {
			return err
		}
	}
	if err := s.Save(path); err != nil {
		return err
	}
	// Persist the presentation sidecar alongside session.jsonl: same rewrite
	// strategy (full rewrite per save), so a compaction that shrank Messages is
	// reflected here too. The rewriteVersion captured on the recorder lets a
	// stale sidecar (saved before a compaction that hasn't flushed yet) be
	// detected on load. Best-effort: a sidecar write failure is logged, not
	// propagated — losing it only degrades reload fidelity, not the session.
	if c.present != nil {
		// Sync against the session before saving: if a compaction rewrote Messages
		// since the last save (RewriteVersion moved), re-seed the recorder around
		// the compaction so the sidecar never carries records describing messages
		// the model has forgotten.
		lastDone := event.Event{}
		if c.lastCompactionDone != nil {
			lastDone = *c.lastCompactionDone
		}
		c.present.SyncBeforeSave(s.RewriteVersion(), lastDone)
		if perr := c.present.Save(present.PresentPath(path)); perr != nil {
			slog.Warn("controller: present sidecar save", "err", perr)
		}
	}
	// Persist the current plan/tool-approval mode into the BranchMeta sidecar
	// so a later Resume can restore it (see restoreModeFromMeta). Done after
	// s.Save so we load-modify-write the same meta s.Save just touched.
	c.syncModeToMeta(path)
	if markActivity {
		return agent.TouchBranchMeta(path)
	}
	return nil
}

// syncModeToMeta writes the controller's current plan mode and tool-approval
// stance into the session's BranchMeta sidecar (load-modify-write, preserving
// all other fields). Best-effort: a write failure is logged, not propagated,
// since losing the mode cache only means a restart falls back to defaults
// rather than corrupting the session. See audit C8.
func (c *Controller) syncModeToMeta(path string) {
	if path == "" {
		return
	}
	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok {
		return
	}
	planMode := c.PlanMode()
	toolApproval := c.ToolApprovalMode()
	if meta.PlanMode == planMode && meta.ToolApprovalMode == toolApproval {
		return // already current; avoid an unnecessary write
	}
	meta.PlanMode = planMode
	meta.ToolApprovalMode = toolApproval
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		slog.Warn("controller: syncModeToMeta", "err", err)
	}
}

func (c *Controller) messageCount() int {
	if c.executor == nil {
		return 0
	}
	return len(c.executor.Session().Snapshot())
}

func (c *Controller) snapshotActivityIfChanged(startMessages int) {
	if c.messageCount() <= startMessages {
		return
	}
	if err := c.SnapshotActivity(); err != nil {
		slog.Warn("controller: activity snapshot", "err", err)
	}
}

// SetSessionPath pins where auto-save lands (a fresh session file minted by the
// caller when no resume path applies).
func (c *Controller) SetSessionPath(p string) {
	c.mu.Lock()
	c.sessionPath = p
	c.mu.Unlock()
	c.rebindPresent(p)
	c.rebindCheckpoints(p)
	// Cache-affinity key (upgrade spec 4-6): derived from the session path so
	// the same conversation keeps its provider cache shard across restarts.
	if c.executor != nil {
		c.executor.SetCacheKey(sessionCacheKey(p))
	}
}

// sessionCacheKey hashes a session path into a short stable cache-affinity key
// (paths contain user names we'd rather not ship to the provider verbatim).
func sessionCacheKey(p string) string {
	if p == "" {
		return ""
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(p))
	return fmt.Sprintf("%x", h.Sum64())
}

// SessionDir reports the directory new session files land in ("" disables
// persistence), so the caller can decide whether to mint a path.
func (c *Controller) SessionDir() string { return c.sessionDir }

// SearchSessionText (upgrade spec 4-5) full-text searches this session
// directory's transcripts and returns hits with excerpts.
func (c *Controller) SearchSessionText(query string) []agent.SearchHit {
	if c.sessionDir == "" {
		return nil
	}
	return agent.SearchSessions(c.sessionDir, query)
}

// SessionPath reports the file the current conversation auto-saves to ("" when
// persistence is disabled), so a history view can mark the active session.
func (c *Controller) SessionPath() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionPath
}

// ensureSessionPath lazily assigns a transcript file the first time a turn
// runs, so autosave (snapshot) and the bot gateway's session-mapping callback
// see a non-empty path. NewSession() rotates to a fresh file on explicit
// /new; this covers the common case where a controller is built without a
// pinned SessionPath (bot gateway, CLI) and would otherwise never persist —
// snapshot() bails on empty path, and the bot UI stayed on "等待首条消息".
// No-op once a path is set. Idempotent under c.mu.
func (c *Controller) ensureSessionPath() {
	c.mu.Lock()
	if c.sessionPath != "" || c.sessionDir == "" {
		c.mu.Unlock()
		return
	}
	p := agent.NewSessionPath(c.sessionDir, c.label)
	c.sessionPath = p
	c.mu.Unlock()
	c.rebindCheckpoints(p)
}

func (c *Controller) parentSessionID() string {
	return agent.BranchID(c.SessionPath())
}

// History returns the executor's current message log (for repopulating a
// resumed frontend's view).
func (c *Controller) History() []provider.Message {
	if c.executor == nil {
		return nil
	}
	return c.executor.Session().Snapshot() // copy — a turn may be appending concurrently
}

// AppendExpertCollab persists a finished expert-team collaboration into the
// active session as a folded-block message (a tool message whose Content is
// the full collab record JSON, with the context layer projecting it down to a
// synthesis-only summary for the model). It then emits an ExpertCollab event
// so the frontend renders an expandable card, and snapshots the session so the
// record survives restarts.
//
// It refuses while a turn is running (the agent's run loop reads session
// messages lock-free, so mid-turn mutation would race). The collab is then
// surfaced on the next render rather than interleaved with a live turn.
func (c *Controller) AppendExpertCollab(content string) error {
	c.mu.Lock()
	if c.executor == nil {
		c.mu.Unlock()
		return fmt.Errorf("controller has no executor")
	}
	if c.running {
		c.mu.Unlock()
		return fmt.Errorf("cannot append expert collaboration while a turn is running")
	}
	sess := c.executor.Session()
	sess.Add(provider.Message{Role: provider.RoleTool, Content: content, Name: "expert_team_collab"})
	path := c.sessionPath
	c.mu.Unlock()
	// Persist outside the lock; Session.Save takes its own lock.
	if path == "" {
		return fmt.Errorf("AppendExpertCollab: sessionPath is empty, cannot persist")
	}
	if err := c.snapshot(true); err != nil {
		return fmt.Errorf("AppendExpertCollab: snapshot failed: %w", err)
	}
	return nil
}

// EmitExpertCollab fires an ExpertCollab event on this controller's sink so the
// frontend renders the folded card. Kept separate from AppendExpertCollab so a
// caller can append-then-emit (or emit for a panel-initiated run whose data it
// already has) without coupling the two.
func (c *Controller) EmitExpertCollab(collab event.Collab) {
	if c.sink == nil {
		return
	}
	c.sink.Emit(event.Event{Kind: event.ExpertCollab, Collab: collab})
}

// DeleteExpertCollab removes the Nth expert-collab message (0-based among
// expert_team_collab tool messages) from the session and re-persists. This is
// the "不采纳" affordance: a user discards a collaboration from both the stored
// transcript and the model's context. The ordinal is stable — deleting one
// doesn't shift the others' ordinals — because it's recomputed over the
// remaining set each call. Refused while a turn is running, like the append.
func (c *Controller) DeleteExpertCollab(ordinal int) error {
	c.mu.Lock()
	if c.executor == nil {
		c.mu.Unlock()
		return fmt.Errorf("controller has no executor")
	}
	if c.running {
		c.mu.Unlock()
		return fmt.Errorf("cannot delete expert collaboration while a turn is running")
	}
	sess := c.executor.Session()
	// Find the ordinal-th expert_team_collab message in the snapshot and map it
	// back to a live index to remove. Computing over a snapshot then re-locking
	// is safe because c.running is false (the run loop is the only other writer).
	snap := sess.Snapshot()
	target := -1
	seen := 0
	for i, m := range snap {
		if m.Role == provider.RoleTool && m.Name == "expert_team_collab" {
			if seen == ordinal {
				target = i
				break
			}
			seen++
		}
	}
	if target < 0 {
		c.mu.Unlock()
		return fmt.Errorf("no expert collaboration at ordinal %d", ordinal)
	}
	// Rebuild without the target message.
	filtered := make([]provider.Message, 0, len(snap)-1)
	filtered = append(filtered, snap[:target]...)
	filtered = append(filtered, snap[target+1:]...)
	sess.Replace(filtered)
	c.mu.Unlock()
	_ = c.snapshot(true)
	return nil
}

// ContextSnapshot returns (promptTokens, contextWindow) from the most recent
// turn. Both zero means no data yet — a gauge hides itself.
func (c *Controller) ContextSnapshot() (int, int) {
	if c.executor == nil {
		return 0, 0
	}
	u := c.executor.LastUsage()
	if u == nil {
		return 0, c.executor.ContextWindow()
	}
	return u.PromptTokens, c.executor.ContextWindow()
}

// CompactRatio returns the auto-compaction threshold as a fraction of the window
// (0 when the executor is unset). The status line shows headroom against it.
func (c *Controller) CompactRatio() float64 {
	if c.executor == nil {
		return 0
	}
	return c.executor.CompactRatio()
}

// LastUsage returns the most recent turn's token telemetry (nil before the first
// turn), so frontends can derive the prompt cache-hit rate for the status line.
func (c *Controller) LastUsage() *provider.Usage {
	if c.executor == nil {
		return nil
	}
	return c.executor.LastUsage()
}

// SessionCache returns cumulative cache hit/miss prompt tokens for the session,
// so a frontend can render the aggregate (session-wide) cache-hit rate — steadier
// than the single-turn rate and unaffected by compaction. some providers do not
// report cache tokens, so this returns zeros for providers that omit cache stats.
func (c *Controller) SessionCache() (hit, miss int) {
	if c.executor == nil {
		return 0, 0
	}
	return c.executor.SessionCache()
}

// Host returns the running MCP host (nil when no plugins), for frontends that
// list servers / resolve MCP prompts.
func (c *Controller) Host() *plugin.Host { return c.host }

// Commands returns the loaded custom slash commands.
func (c *Controller) Commands() []command.Command { return c.commands }

// Skills returns the discoverable skills (for the slash menu and `/skills`).
// When a live Store is available, scan it on demand so skills installed during
// this session appear without rewriting the cache-stable system prompt.
func (c *Controller) Skills() []skill.Skill {
	if c.skillStore != nil {
		return c.skillStore.List()
	}
	return c.skills
}

// AllSkills returns every discoverable skill, including disabled ones, for
// management surfaces that need to re-enable a hidden skill.
func (c *Controller) AllSkills() []skill.Skill {
	if c.allSkillStore != nil {
		return c.allSkillStore.List()
	}
	if len(c.allSkills) > 0 {
		return c.allSkills
	}
	return c.skills
}

// DisabledSkills returns all discoverable skills that are disabled in config.
func (c *Controller) DisabledSkills() []skill.Skill {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	var out []skill.Skill
	for _, sk := range c.AllSkills() {
		if cfg.IsSkillDisabled(sk.Name) {
			out = append(out, sk)
		}
	}
	return out
}

// SkillEnabled reports whether a discoverable skill is enabled.
func (c *Controller) SkillEnabled(name string) bool {
	cfg, err := config.Load()
	if err != nil {
		return true
	}
	return !cfg.IsSkillDisabled(name)
}

// SetSkillEnabled persists a skill enable/disable preference. The caller should
// rebuild the controller for the prompt/tool registry to reflect it immediately.
func (c *Controller) SetSkillEnabled(name string, enabled bool) error {
	found := false
	for _, sk := range c.AllSkills() {
		if config.SkillNameKey(sk.Name) == config.SkillNameKey(name) {
			name = sk.Name
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("unknown skill: %s", name)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if err := cfg.SetSkillEnabled(name, enabled); err != nil {
		return err
	}
	return cfg.SaveTo(config.UserConfigPath())
}

// HookRunner returns the session's hook runner (nil-safe; may hold zero hooks),
// so a frontend can list the active hooks via `/hooks`.
func (c *Controller) HookRunner() *hook.Runner { return c.hooks }

// profileForDream returns the active profile name for dream's portrait sizing,
// defaulting to "dev" when memory isn't loaded yet. dream uses it to pick the
// right portrait file to size-check (and thus whether to run in compress mode).
func (c *Controller) profileForDream() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mem != nil && c.mem.ProfileName != "" {
		return c.mem.ProfileName
	}
	return "dev"
}

// maybeDreamDistill spawns Dream/Distill from an idle trigger. It is the idle
// counterpart of the old turn-start hook: instead of firing while the user is
// busy, it fires only after the idle timer confirms the user has gone quiet.
// Cadence + master-switch + inFlight gates still apply inside agent.SpawnDream/
// SpawnDistill, so an idle fire that is not yet due is a cheap no-op.
// dreamProv returns the provider dream/distill run on: the configured
// fast_task_model when wired, else the main executor provider.
func (c *Controller) dreamProv() provider.Provider {
	if c.dreamProvider != nil {
		return c.dreamProvider
	}
	if c.executor != nil {
		return c.executor.Provider()
	}
	return nil
}

func (c *Controller) maybeDreamDistill() {
	if c.sessionDir == "" || c.executor == nil {
		return
	}
	ctx := context.Background()
	prov := c.dreamProv()
	agent.SpawnDream(ctx, c.sessionDir, c.profileForDream(), prov, c.reg, c.executor.Session(), c.sink, &c.autosaveWG)
	agent.SpawnDistill(ctx, c.sessionDir, prov, c.reg, c.executor.Session(), c.sink, &c.autosaveWG)
}

// touchActivity records that the user is active (turn start or turn end) and
// (re)arms the idle-dream countdown. Called on every turn so a continuous
// conversation keeps pushing the idle fire out; only a genuine pause lets the
// timer reach zero. No-op when self-evolution is disabled or no threshold set.
func (c *Controller) touchActivity() {
	if c.sessionDir == "" || c.executor == nil {
		return
	}
	// Read the live idle threshold; a negative/zero config disables idle dream.
	cfg, err := config.Load()
	if err != nil || !cfg.Dream.Enabled {
		return
	}
	threshold := cfg.Dream.IdleMinutesEffective()
	if threshold <= 0 {
		return // idle triggering disabled
	}
	c.idleDreamMu.Lock()
	c.lastActivity = time.Now()
	// (Re)start the single pending timer. A running timer is stopped first so we
	// never accumulate goroutines across rapid turns.
	if c.idleTimer != nil {
		c.idleTimer.Stop()
	}
	delay := time.Duration(threshold) * time.Minute
	c.idleTimer = time.AfterFunc(delay, c.idleDreamFired)
	c.idleDreamMu.Unlock()
}

// idleDreamFired is the timer callback: the user has been quiet past the
// threshold. Re-check the gate (config may have changed, or a turn may have
// started racing the timer) before spawning, then consolidate. Runs on the
// timer goroutine.
func (c *Controller) idleDreamFired() {
	c.idleDreamMu.Lock()
	last := c.lastActivity
	c.idleTimer = nil
	c.idleDreamMu.Unlock()
	if c.sessionDir == "" || c.executor == nil {
		return
	}
	// Re-read config: the threshold could have been raised/disabled since the
	// timer was armed. If the user is no longer idle past it, skip.
	cfg, err := config.Load()
	if err != nil || !cfg.Dream.Enabled {
		return
	}
	threshold := time.Duration(cfg.Dream.IdleMinutesEffective()) * time.Minute
	if threshold > 0 && time.Since(last) < threshold {
		return // a turn touched activity after we fired; not idle anymore
	}
	c.maybeDreamDistill()
}

// stopIdleDream cancels any pending idle-dream timer. Called from Close so a
// shutting-down controller doesn't spawn a dream into a closing store.
func (c *Controller) stopIdleDream() {
	c.idleDreamMu.Lock()
	if c.idleTimer != nil {
		c.idleTimer.Stop()
		c.idleTimer = nil
	}
	c.idleDreamMu.Unlock()
}

// TriggerDream runs a Dream consolidation pass on demand, blocking until it
// finishes or times out. Returns the run record (status/error included). A
// false "ran" means the run did not execute (e.g. disabled, or already running).
// Intended for the desktop "run now" button.
func (c *Controller) TriggerDream(ctx context.Context) (agent.DreamRun, bool) {
	if c.executor == nil {
		return agent.DreamRun{Status: "error", Error: "no active session"}, false
	}
	return agent.RunDreamOnce(ctx, c.sessionDir, c.profileForDream(), c.dreamProv(), c.reg, c.executor.Session(), c.sink)
}

// TriggerDistill runs a Distill workflow-extraction pass on demand. See TriggerDream.
func (c *Controller) TriggerDistill(ctx context.Context) (agent.DreamRun, bool) {
	if c.executor == nil {
		return agent.DreamRun{Status: "error", Error: "no active session"}, false
	}
	return agent.RunDistillOnce(ctx, c.sessionDir, c.dreamProv(), c.reg, c.executor.Session(), c.sink)
}

// LastDreamRun exposes the most recent recorded run of a kind for this session,
// for the desktop "last run" status display.
func (c *Controller) LastDreamRun(kind agent.DreamKind) (agent.DreamRun, bool) {
	return agent.LastDreamRun(c.sessionDir, kind)
}

// DreamInFlight reports whether a run of the given kind is currently executing.
func (c *Controller) DreamInFlight(kind agent.DreamKind) bool {
	return agent.DreamInFlight(kind)
}

// AddMCPServer connects an MCP server live and persists it to the config file. Its
// tools are registered immediately and become available on the next turn (the
// agent reads the registry per turn). The raw entry — ${VARS} intact — is what's
// written to disk; the live connection uses the expanded form. Returns the number
// of tools the server exposed. A save failure after a successful connect is
// reported but non-fatal: the server still works this session.
func (c *Controller) AddMCPServer(e config.PluginEntry) (int, error) {
	n, err := c.connectMCPServer(e)
	if err != nil {
		return 0, err
	}
	cfg, lerr := config.Load()
	if lerr != nil {
		return n, fmt.Errorf("connected, but reloading config to save failed: %w", lerr)
	}
	if err := cfg.UpsertPlugin(e); err != nil {
		return n, fmt.Errorf("connected, but config rejected the entry: %w", err)
	}
	if err := cfg.Save(); err != nil {
		return n, fmt.Errorf("connected, but saving config failed: %w", err)
	}
	return n, nil
}

// ConnectMCPServer connects an MCP server entry for this session without writing
// it to config. Desktop owns config placement so it can keep user-level settings
// out of project fairpeer.toml while preserving the CLI AddMCPServer semantics.
func (c *Controller) ConnectMCPServer(e config.PluginEntry) (int, error) {
	return c.connectMCPServer(e)
}

func (c *Controller) connectMCPServer(e config.PluginEntry) (int, error) {
	exp := e.ExpandedPlugin()
	return c.connectMCPSpec(plugin.Spec{
		Name:    exp.Name,
		Type:    exp.Type,
		Command: exp.Command,
		Args:    exp.Args,
		Env:     exp.Env,
		URL:     exp.URL,
		Headers: exp.Headers,
	})
}

func (c *Controller) connectMCPSpec(s plugin.Spec) (int, error) {
	if !c.profileAllowsMCP(s.Name) {
		return 0, fmt.Errorf("MCP server %q is not allowed in the %q profile (plugin allowlist/hidden filter)", s.Name, c.profileForDream())
	}
	if c.host == nil {
		c.host = plugin.NewHost()
	}
	tools, err := c.host.Add(c.pluginCtx, s)
	if err != nil {
		return 0, err
	}
	if c.reg != nil {
		c.reg.RemovePrefix(plugin.ToolPrefix(s.Name))
		for _, t := range tools {
			c.reg.Add(t)
		}
	}
	return len(tools), nil
}

// profileAllowsMCP enforces the profile plugin gates on HOT connects. Boot
// already filters the startup spec list (netdev PluginAllowlist hides every
// external MCP); this closes the runtime paths (ConnectMCPServer /
// ConnectConfiguredMCPServer / AddMCPServer / import) so a sealed profile
// cannot gain registry tools mid-session — MCP tool names sit outside the
// tool_scope RemovePrefix seal, so a write/exec MCP would otherwise punch
// straight through it. Fails closed for named profiles when config can't be
// read (the seal must hold even on a broken config), open for dev.
func (c *Controller) profileAllowsMCP(name string) bool {
	profileName := c.profileForDream()
	cfg, err := config.Load()
	if err != nil {
		return profileName == "" || profileName == config.ProfileDev
	}
	p, err := cfg.ResolveProfile(profileName)
	if err != nil || p == nil {
		return profileName == "" || profileName == config.ProfileDev
	}
	return config.PluginAllowedByProfile(p, name) && !config.PluginHiddenByProfile(p, name)
}

// ImportMCPEntries persists selected MCP entries and attempts to connect them
// live. A connection failure does not roll back the config import: the user can
// fix local dependencies and reconnect in a later session.
func (c *Controller) ImportMCPEntries(entries []config.PluginEntry) (total, added, updated, connected, failed, skipped int, err error) {
	cfg, lerr := config.Load()
	if lerr != nil {
		return 0, 0, 0, 0, 0, 0, lerr
	}
	existing := make(map[string]bool, len(cfg.Plugins))
	for _, p := range cfg.Plugins {
		existing[p.Name] = true
	}
	for _, e := range entries {
		if existing[e.Name] {
			updated++
		} else {
			added++
		}
		if err := cfg.UpsertPlugin(e); err != nil {
			return 0, 0, 0, 0, 0, 0, err
		}
		existing[e.Name] = true
	}
	if err := cfg.Save(); err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}
	for _, e := range entries {
		if c.host != nil && containsString(c.host.ServerNames(), e.Name) {
			skipped++
			continue
		}
		if _, err := c.AddMCPServer(e); err != nil {
			failed++
			continue
		}
		connected++
	}
	return len(entries), added, updated, connected, failed, skipped, nil
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func (c *Controller) ConfiguredMCPNames() []string {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(cfg.Plugins))
	for _, p := range cfg.Plugins {
		names = append(names, p.Name)
	}
	return names
}

func (c *Controller) DisconnectedMCPNames() []string {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	connected := map[string]bool{}
	if c.host != nil {
		for _, name := range c.host.ServerNames() {
			connected[name] = true
		}
	}
	var names []string
	for _, p := range cfg.Plugins {
		if !connected[p.Name] {
			names = append(names, p.Name)
		}
	}
	return names
}

func (c *Controller) ConnectConfiguredMCPServer(name string) (int, error) {
	cfg, err := config.Load()
	if err != nil {
		return 0, err
	}
	for _, p := range cfg.Plugins {
		if p.Name == name {
			return c.connectMCPServer(p)
		}
	}
	if name == "codegraph" {
		return c.connectCodegraphMCPServer(cfg)
	}
	return 0, fmt.Errorf("no configured MCP server named %q", name)
}

// ConnectCodegraphMCPServer connects the built-in CodeGraph server using an
// already-resolved config. Desktop uses this after saving user-level settings so
// a stale project config cannot override the just-applied choice.
func (c *Controller) ConnectCodegraphMCPServer(cfg *config.Config) (int, error) {
	return c.connectCodegraphMCPServer(cfg)
}

func (c *Controller) connectCodegraphMCPServer(cfg *config.Config) (int, error) {
	if !cfg.Codegraph.Enabled {
		return 0, fmt.Errorf("codegraph is disabled in config")
	}
	bin, ok := codegraph.Resolve(cfg.Codegraph.Path)
	if !ok {
		return 0, fmt.Errorf("codegraph is not installed")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 0, err
	}
	if !codegraph.IndexableRoot(cwd) {
		return 0, fmt.Errorf("codegraph: refusing to index %q — a filesystem root would index the whole volume", cwd)
	}
	if err := codegraph.EnsureInit(c.pluginCtx, bin, cwd); err != nil {
		return 0, fmt.Errorf("codegraph init: %w", err)
	}
	return c.connectMCPSpec(plugin.Spec{
		Name:              "codegraph",
		Command:           bin,
		Args:              []string{"serve", "--mcp"},
		Dir:               cwd,
		ReadOnlyToolNames: codegraph.ReadOnlyToolNames(),
	})
}

// RemoveMCPServer disconnects a live MCP server — its tools vanish from the next
// turn — and removes it from the config file. It reports whether a live server was
// disconnected; an error only when the name is neither connected nor in config (or
// the config save fails). A server declared in .mcp.json disconnects for this
// session but returns on the next start, since that file isn't ours to edit.
func (c *Controller) RemoveMCPServer(name string) (disconnected bool, err error) {
	if c.host != nil {
		if prefix, ok := c.host.Remove(name); ok {
			disconnected = true
			if c.reg != nil {
				c.reg.RemovePrefix(prefix)
			}
		}
	}
	cfg, lerr := config.Load()
	if lerr != nil {
		return disconnected, lerr
	}
	inConfig := cfg.RemovePlugin(name)
	if inConfig {
		if !disconnected && c.reg != nil {
			c.reg.RemovePrefix(plugin.ToolPrefix(name))
		}
		if serr := cfg.Save(); serr != nil {
			return disconnected, serr
		}
	}
	if !disconnected && !inConfig {
		return false, fmt.Errorf("no MCP server named %q", name)
	}
	return disconnected, nil
}

// DisconnectMCPServer disconnects a live server for this session without touching
// config — the connector toggle's "off". Its tools vanish next turn; it reconnects
// on the next session start, or now via ConnectConfiguredMCPServer (the "on").
// Reports whether a live server was actually disconnected.
func (c *Controller) DisconnectMCPServer(name string) bool {
	disconnected := false
	if c.host != nil {
		if prefix, ok := c.host.Remove(name); ok {
			disconnected = true
			if c.reg != nil {
				c.reg.RemovePrefix(prefix)
			}
		}
	}
	removedPlaceholder := 0
	if !disconnected && c.reg != nil {
		removedPlaceholder = c.reg.RemovePrefix(plugin.ToolPrefix(name))
	}
	return disconnected || removedPlaceholder > 0
}

// Label returns the human-readable model label, e.g. "openai/gpt-4o".
func (c *Controller) Label() string { return c.label }

// WorkspaceRoot returns the workspace root for this controller's session
// (the directory that file-writers and @-references are scoped to).
// Empty means no scoping is in effect.
func (c *Controller) WorkspaceRoot() string { return c.cpRoot }

// ToolRegistry exposes the live, fully-configured tool registry this session
// runs with (workspace-bound writers, sandboxed bash, plugin/MCP tools). It lets
// a satellite runner — e.g. a 团队 (team) member executing in its own isolated
// sub-session — reuse the exact same tool environment as the main loop instead
// of trying to reconstruct the sandbox/root configuration itself.
func (c *Controller) ToolRegistry() *tool.Registry { return c.reg }

// HeadlessGate returns the permission gate a satellite sub-session inherits: the
// session's policy wired to a nil approver, matching how the kernel's `task`
// sub-agents run (sub-agents always run headless — no UI to answer a prompt).
// Hard-deny rules still bite in every mode; an "ask"-level call resolves without
// a prompt because there is nobody attached to the run to ask.
func (c *Controller) HeadlessGate() agent.Gate {
	return permission.NewGate(c.policy, nil)
}

// Close stops plugin subprocesses and releases resources. A session that ever
// started fires SessionEnd so a teardown hook runs.
func (c *Controller) Close() {
	c.mu.Lock()
	started := c.startedOnce
	// Mark closed before anything else: a background result that finishes during
	// teardown must be left for the next process (the coordinator keeps it on
	// disk) instead of being pushed into a dying session.
	c.closed = true
	c.mu.Unlock()
	if started {
		c.hooks.SessionEnd(context.Background())
	}
	c.stopIdleDream() // cancel any pending idle-dream so it can't fire into a closing store
	if c.jobs != nil {
		c.jobs.Close() // cancel any still-running background jobs
	}
	// Wait for turn-spawned goroutines (autosave ticker, hook notifications) so
	// Close doesn't leave them writing to a torn-down controller. Bounded: a
	// stuck hook subprocess is still cancelled by its turn ctx, and a runaway
	// goroutine can't block Close indefinitely.
	waitDone := make(chan struct{})
	go func() { c.autosaveWG.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
	}
	if c.cleanup != nil {
		c.cleanup()
	}
}

// InheritLifecycleFrom carries lifecycle state from a previous controller into
// this one, preventing duplicate SessionStart hooks and preserving the turn
// counter across controller rebuilds (e.g. model switch that preserves the
// conversation). Call this before the first turn on the new controller.
func (c *Controller) InheritLifecycleFrom(prev *Controller) {
	if prev == nil {
		return
	}
	prev.mu.Lock()
	c.startedOnce = prev.startedOnce
	c.turn = prev.turn
	prev.mu.Unlock()
}

// SessionDestroyHandle separates session teardown into phases so background
// jobs can drain before artifacts are removed.
type SessionDestroyHandle struct {
	sessionPath string
	jobs        *jobs.Manager
}

// Wait cancels background jobs for the session and waits for them to drain.
func (h *SessionDestroyHandle) Wait() {
	if h.jobs != nil {
		h.jobs.Close()
	}
}

// BeginDestroySession starts a two-phase session destroy. Call Wait() to cancel
// and drain background jobs, then Finish() to release resources. If the session
// has no background job manager, Wait is a no-op.
func (c *Controller) BeginDestroySession(sessionPath string) *SessionDestroyHandle {
	return &SessionDestroyHandle{
		sessionPath: sessionPath,
		jobs:        c.jobs,
	}
}

// IsDestroyingSession reports whether the given session path is currently in
// the destroy window (between BeginDestroySession and artifact removal).
func (c *Controller) IsDestroyingSession(sessionPath string) bool {
	// Not tracked in this implementation; always false.
	// A full implementation would maintain a map of active destroy handles.
	return false
}

// Jobs returns the still-running background jobs for the status bar (nil when
// background jobs are disabled).
func (c *Controller) Jobs() []jobs.View {
	if c.jobs == nil {
		return nil
	}
	return c.jobs.Running()
}

// SetToolApprovalMode changes the runtime approval posture for permission-gated
// tools. It does not answer business asks or plan approval.
func (c *Controller) SetToolApprovalMode(mode string) {
	mode = normalizeToolApprovalMode(mode)
	var pending []chan approvalReply

	c.mu.Lock()
	c.toolApprovalMode = mode
	c.autoApproveTools = mode == ToolApprovalYolo
	switch mode {
	case ToolApprovalAuto:
		pending = c.drainApprovalsLocked(false)
	case ToolApprovalYolo:
		pending = c.drainApprovalsLocked(true)
	}
	c.mu.Unlock()

	c.refreshInteractiveGate()
	for _, reply := range pending {
		reply <- approvalReply{allow: true}
	}
}

func (c *Controller) ToolApprovalMode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return normalizeToolApprovalMode(c.toolApprovalMode)
}

// SetAutoApproveTools turns YOLO/full-access mode on or off for the session:
// while on, every tool approval request is auto-allowed (writers and bash run
// without asking). Ask requests and plan approval still reach the user. Deny
// rules still block. Runtime-only — never written to config.
func (c *Controller) SetAutoApproveTools(on bool) {
	if on {
		c.SetToolApprovalMode(ToolApprovalYolo)
		return
	}
	c.SetToolApprovalMode(ToolApprovalAsk)
}

// SetBypass is the legacy name for SetAutoApproveTools. Keep it for existing
// desktop/serve bindings and CLI code that still uses the bypass wording.
func (c *Controller) SetBypass(on bool) {
	c.SetAutoApproveTools(on)
}

// SetMode applies plan (read-only) and tool auto-approval together so a turn
// submitted right after a composer mode switch can't observe a half-applied
// gate. Turning tool auto-approval on drains any pending tool approval.
func (c *Controller) SetMode(plan, autoApproveTools bool) {
	c.mu.Lock()
	c.planMode = plan
	c.mu.Unlock()

	if c.executor != nil {
		c.executor.SetPlanMode(plan)
	}
	if autoApproveTools {
		c.SetToolApprovalMode(ToolApprovalYolo)
	} else {
		c.SetToolApprovalMode(ToolApprovalAsk)
	}
}

// SetRAGScope sets the knowledge-base collection that auto-injection searches.
// Pass "" to disable injection for this session ("不使用" in the Composer
// dropdown). The scope is read at the start of each turn (under c.mu) so a
// mid-flight change can't race the RAG call. Mirrors the per-tab pattern of
// SetToolApprovalMode.
func (c *Controller) SetRAGScope(scope string) {
	c.mu.Lock()
	c.ragScope = scope
	c.mu.Unlock()
}

// drainApprovalsLocked removes every pending approval gate and returns their
// reply channels; caller holds c.mu and sends {allow:true} after unlocking.
func (c *Controller) drainApprovalsLocked(includeExplicitAsk bool) []chan approvalReply {
	pending := make([]chan approvalReply, 0, len(c.approvals))
	for id, approval := range c.approvals {
		if approval.tool == PlanApprovalTool || approval.tool == composeReplanTool {
			continue
		}
		if !includeExplicitAsk && !approval.autoDrain {
			continue
		}
		delete(c.approvals, id)
		pending = append(pending, approval.reply)
	}
	return pending
}

// AutoApproveTools reports whether YOLO/full-access tool auto-approval is on,
// for status indicators and mode persistence.
func (c *Controller) AutoApproveTools() bool {
	return c.ToolApprovalMode() == ToolApprovalYolo
}

// Bypass is the legacy name for AutoApproveTools.
func (c *Controller) Bypass() bool {
	return c.AutoApproveTools()
}

// --- memory ---
//
// c.mem is treated as an immutable snapshot guarded by c.mu: reads take the lock
// and return the pointer; writes mutate disk then swap in a freshly discovered
// snapshot. A turn-tail note is queued for each write so the change applies this
// session without disturbing the cache-stable system prefix (it folds into the
// prefix on the next session). All of these are no-ops returning "" when memory
// is disabled.

// QuickAdd appends a one-line note to the doc-memory file for scope (project
// fairpeer.md by default) — the write side of "#<note>". Returns the file written.
func (c *Controller) QuickAdd(scope memory.Scope, note string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mem == nil {
		return "", nil
	}
	path := c.mem.DocPath(scope)
	if path == "" {
		return "", fmt.Errorf("no target file for memory scope %q", scope)
	}
	if err := memory.AppendDoc(path, note); err != nil {
		return "", err
	}
	c.pendingMemory = append(c.pendingMemory, note)
	c.refreshMemoryLocked()
	return path, nil
}

// SaveDoc overwrites a recognized memory doc with body — the save side of the
// desktop panel's in-place editor. Returns the file written.
func (c *Controller) SaveDoc(path, body string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mem == nil {
		return "", nil
	}
	written, err := c.mem.WriteDoc(path, body)
	if err != nil {
		return "", err
	}
	// Inject the new content once on the next turn: the cached prefix still holds
	// the pre-edit version this session, so handing the model the current text
	// avoids a stale-guidance gap until the next session re-folds it into the
	// prefix. Trimmed to a single tail note (drained by Compose), not per-turn.
	c.pendingMemory = append(c.pendingMemory,
		"Memory file "+written+" was just edited. Its current contents:\n"+strings.TrimSpace(body))
	c.refreshMemoryLocked()
	return written, nil
}

// ForgetMemory deletes a saved auto-memory by name — the panel/TUI delete action,
// the manual counterpart to the model's `forget` tool. It queues a turn-tail note
// so the deletion applies this session (the cached prefix still lists the fact
// until the next session re-folds the index).
func (c *Controller) ForgetMemory(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mem == nil {
		return nil
	}
	if err := c.mem.Store.Delete(name); err != nil {
		return err
	}
	c.pendingMemory = append(c.pendingMemory,
		"Deleted memory \""+name+"\" — disregard its line still shown in the saved-memories index until next session.")
	c.refreshMemoryLocked()
	return nil
}

// QueueMemory implements memory.Queue: when the model runs the remember/forget
// tool, the tool calls this with a note that rides the next turn so the change
// applies this session without touching the cache-stable prefix. It also
// refreshes the snapshot a memory panel reads.
func (c *Controller) QueueMemory(note string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pendingMemory = append(c.pendingMemory, note)
	c.refreshMemoryLocked()
}

// Memory returns the loaded memory snapshot (nil when memory is disabled), for
// frontends that surface a memory panel or the /memory command. The returned
// *Set is immutable — mutations go through QuickAdd / SaveDoc.
func (c *Controller) Memory() *memory.Set {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mem
}

// ProfileView is the payload the workspace preference panel reads: the path of
// the active mode's portrait file and its current contents. Path is "" when the
// user config dir is unresolvable; Content is "" when the file does not exist
// yet (a fresh mode the user has never written to).
type ProfileView struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Profile returns the active mode's portrait for the preference panel. Read-only
// — saves go through SaveDoc (the profile path is whitelisted). The panel calls
// this on open to populate its editor.
func (c *Controller) Profile() ProfileView {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mem == nil {
		return ProfileView{}
	}
	return ProfileView{Path: c.mem.ProfilePath(), Content: c.mem.ProfileContent()}
}

// ProfilePresetsView is the payload the workspace preference panel reads and
// writes for the mode's selectable preference presets (cowork-presets.json
// under cowork): the items, the id in use, and the file path for display.
type ProfilePresetsView struct {
	Active string                 `json:"active"`
	Items  []memory.ProfilePreset `json:"items"`
	Path   string                 `json:"path"`
}

// ProfilePresets reads the active mode's preference presets. A fresh install
// returns the factory defaults without touching disk; the file is only written
// on the first save.
func (c *Controller) ProfilePresets() ProfilePresetsView {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mem == nil {
		return ProfilePresetsView{}
	}
	f := memory.LoadPresets(c.mem.UserDir, c.mem.ProfileName)
	return ProfilePresetsView{Active: f.Active, Items: f.Items, Path: c.mem.PresetsPath()}
}

// SaveProfilePresets normalizes and writes the mode's preference presets, then
// mirrors SaveDoc's session-effect mechanics: a turn-tail note hands the model
// the newly selected preset so it applies this session, and refreshMemoryLocked
// re-reads the portrait (discoverProfile folds the preset into the prefix next
// session). Returns the file written.
func (c *Controller) SaveProfilePresets(f memory.PresetFile) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mem == nil {
		return "", nil
	}
	written, err := memory.SavePresets(c.mem.UserDir, c.mem.ProfileName, f)
	if err != nil {
		return "", err
	}
	// Re-load to echo exactly what was normalized onto disk, not what was submitted.
	saved := memory.LoadPresets(c.mem.UserDir, c.mem.ProfileName)
	if p := saved.ActivePreset(); p != nil {
		c.pendingMemory = append(c.pendingMemory,
			"The user switched the active preference preset to \""+p.Name+"\". Follow it from now on:\n"+p.Content)
	} else {
		c.pendingMemory = append(c.pendingMemory,
			"The user cleared the active preference preset — no preset-specific style directive applies from now on.")
	}
	c.refreshMemoryLocked()
	return written, nil
}

// refreshMemoryLocked re-discovers memory from disk so a later Memory() reflects
// a just-applied write. Caller holds c.mu. ProfileName is carried through so a
// reload keeps the mode partition — without it, a mid-session remember/forget
// would re-load under the default profile and drop the dev/cowork isolation.
func (c *Controller) refreshMemoryLocked() {
	if c.mem == nil {
		return
	}
	c.mem = memory.Load(memory.Options{CWD: c.mem.CWD, UserDir: c.mem.UserDir, Profile: c.mem.ProfileName})
}

// --- approval bridge (agent gate → events) ---

// gateApprover adapts the Controller to permission.Approver. It is distinct
// from the public Approve command (different signature, different direction).
type gateApprover struct{ c *Controller }

// GateApprover returns the controller's interactive approver for external
// bridging (upgrade spec 3-10: the desktop wires it into boot's sub-agent
// gate). Nil when the controller has no interactive approval.
func (c *Controller) GateApprover() permission.Approver {
	return gateApprover{c}
}

func (g gateApprover) Approve(ctx context.Context, tool, subject string, args json.RawMessage) (bool, bool, error) {
	// Auto-allow without prompting while executing a just-approved plan (the plan
	// was the approval) or while YOLO/full-access tool auto-approval is on. Deny
	// rules already bit before this point, so they still block.
	g.c.mu.Lock()
	auto := g.c.approvalBypassAllowsLocked(tool)
	g.c.mu.Unlock()
	if auto {
		return true, false, nil
	}
	return g.c.requestApproval(ctx, tool, subject, args)
}

type seedTodo struct {
	Content string `json:"content"`
	Status  string `json:"status"`
	Level   int    `json:"level,omitempty"`
}

// seedPlanTodos turns an approved plan into a starter task list and emits it as a
// synthetic todo_write event, so the live task panel populates the instant the
// user approves — a structural guarantee, not a prompt the model might ignore.
// The model still flips item status as it works (only it knows its own
// progress); this just makes the list exist. No-op when the plan has no list.
func (c *Controller) seedPlanTodos(plan string) string {
	args := planTodosJSON(plan)
	if args == "" {
		return ""
	}
	t := event.Tool{ID: "plan-seed", Name: "todo_write", Args: args, ReadOnly: true}
	c.sink.Emit(event.Event{Kind: event.ToolDispatch, Tool: t})
	t.Output = "task list seeded from the approved plan"
	c.sink.Emit(event.Event{Kind: event.ToolResult, Tool: t})
	return args
}

func (c *Controller) completePlanTodos(args string) {
	if args == "" {
		return
	}
	done := completedPlanTodosJSON(args)
	if done == "" {
		return
	}
	t := event.Tool{ID: "plan-seed", Name: "todo_write", Args: done, ReadOnly: true}
	c.sink.Emit(event.Event{Kind: event.ToolDispatch, Tool: t})
	t.Output = "approved plan finished"
	c.sink.Emit(event.Event{Kind: event.ToolResult, Tool: t})
}

// planTodosJSON parses an approved plan's markdown into todo_write-shaped args
// JSON ({"todos":[...]}), or "" when the plan has no list items. Used by
// seedPlanTodos to turn the approved plan into a starter checklist.
func planTodosJSON(plan string) string {
	items := parsePlanTodos(plan)
	if len(items) == 0 {
		return ""
	}
	b, err := json.Marshal(map[string]any{"todos": items})
	if err != nil {
		return ""
	}
	return string(b)
}

func completedPlanTodosJSON(args string) string {
	var p struct {
		Todos []seedTodo `json:"todos"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil || len(p.Todos) == 0 {
		return ""
	}
	for i := range p.Todos {
		p.Todos[i].Status = "completed"
	}
	b, err := json.Marshal(map[string]any{"todos": p.Todos})
	if err != nil {
		return ""
	}
	return string(b)
}

// parsePlanTodos extracts a starter task list from an approved plan's markdown
// list items (bulleted or numbered): the first is in_progress, the rest pending,
// capped so a long plan can't flood the panel. It understands ONLY markdown lists
// — an unambiguous, standard structure — and deliberately does not guess at prose,
// tables, or arrow sequences (those need brittle, language-specific heuristics).
// The plan-mode marker steers the model to present its plan as a list, so this
// catches the normal case; anything it misses is covered by the model's own
// todo_write calls as it executes.
func parsePlanTodos(plan string) []seedTodo {
	var todos []seedTodo
	for _, raw := range strings.Split(plan, "\n") {
		item, level, ok := listItem(raw)
		if !ok {
			continue
		}
		status := "pending"
		if len(todos) == 0 {
			status = "in_progress"
		}
		todos = append(todos, seedTodo{Content: item, Status: status, Level: level})
		if len(todos) >= 20 {
			break
		}
	}
	return todos
}

// listItem parses a markdown list line ("- x", "* x", "1. x", "2) x") into its
// task text and a nesting level derived from leading indentation (0 for a
// top-level item, 1 for an indented sub-step — capped at 1 since the plan is
// two-level). ok is false when the line isn't a list item. Light inline-markdown
// stripping keeps the checklist readable.
func listItem(line string) (content string, level int, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return "", 0, false
	}
	indent := 0
	for _, c := range line[:len(line)-len(trimmed)] {
		if c == '\t' {
			indent += 4
		} else {
			indent++
		}
	}
	s := trimmed
	// A numbered markdown heading ("### 1. Add the loader") is how models often
	// write a phase even when asked for a list; strip the heading marker and
	// treat it as a top-level phase. A heading without a number (a section
	// title like "## Plan") falls through and is ignored.
	heading := false
	if h := strings.TrimLeft(s, "#"); h != s && strings.HasPrefix(h, " ") {
		heading = true
		s = strings.TrimSpace(h)
	}
	switch {
	case strings.HasPrefix(s, "- "), strings.HasPrefix(s, "* "), strings.HasPrefix(s, "+ "):
		s = s[2:]
	default:
		// numbered: leading digits, then "." or ")", then a space
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == 0 || i+1 >= len(s) || (s[i] != '.' && s[i] != ')') || s[i+1] != ' ' {
			return "", 0, false
		}
		s = s[i+2:]
	}
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[ ] ")
	s = strings.TrimPrefix(s, "[x] ")
	s = strings.ReplaceAll(s, "`", "")
	s = strings.ReplaceAll(s, "**", "")
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0, false
	}
	if heading {
		return s, 0, true // a heading is always a top-level phase
	}
	if indent >= 2 {
		return s, 1, true
	}
	return s, 0, true
}

// parseRewind parses the arguments after "/rewind". The user may provide:
//
//	/rewind              → latest checkpoint, both
//	/rewind <turn>       → that turn, both
//	/rewind <turn> <scope> → that turn, code|conversation|both
//
// If no turn is given, the latest checkpoint is used. If no scope is given, Both is assumed.
func parseRewind(args string, cps []checkpoint.Meta) (int, RewindScope, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		if len(cps) == 0 {
			return 0, RewindBoth, fmt.Errorf("no checkpoints available")
		}
		return cps[len(cps)-1].Turn, RewindBoth, nil
	}
	turn, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, RewindBoth, fmt.Errorf("invalid turn: %w", err)
	}
	scope := RewindBoth
	if len(fields) >= 2 {
		switch strings.ToLower(fields[1]) {
		case "code":
			scope = RewindCode
		case "conversation":
			scope = RewindConversation
		case "both":
			scope = RewindBoth
		default:
			return 0, RewindBoth, fmt.Errorf("unknown scope %q", fields[1])
		}
	}
	return turn, scope, nil
}

// requestApproval emits an ApprovalRequest and blocks until Approve(ID, …)
// answers or ctx is cancelled. A prior session grant for the same approval scope
// short-circuits. promptMu serialises outstanding prompts. args is the raw
// tool-call arguments (nil for synthetic plan/replan approvals); writer tools
// get a per-file preview attached so the approval card can show the diff
// instead of a blind subject line.
func (c *Controller) requestApproval(ctx context.Context, tool, subject string, args json.RawMessage) (bool, bool, error) {
	c.mu.Lock()
	// YOLO/full access and the just-approved-plan execution window auto-allow
	// approval-gated tools without prompting. Plan approval is a user decision,
	// not a tool permission, so it deliberately stays interactive.
	if c.approvalBypassAllowsLocked(tool) || c.sessionGrantAllowsLocked(tool, subject) {
		c.mu.Unlock()
		return true, false, nil
	}
	c.mu.Unlock()

	c.promptMu.Lock()
	defer c.promptMu.Unlock()

	// Re-check the grant: a session grant may have landed while we queued behind
	// another prompt for the same subject.
	c.mu.Lock()
	if c.approvalBypassAllowsLocked(tool) || c.sessionGrantAllowsLocked(tool, subject) {
		c.mu.Unlock()
		return true, false, nil
	}
	c.nextID++
	id := strconv.Itoa(c.nextID)
	reply := make(chan approvalReply, 1)
	c.approvals[id] = pendingApproval{tool: tool, subject: subject, autoDrain: c.autoApprovalWouldAllowLocked(tool, subject), reply: reply}
	c.mu.Unlock()

	// Rich approval context: raw args plus the previewed file changes (when the
	// tool can describe them), so the frontend card shows what the call would
	// actually do. Preview runs before any permission decision and never writes.
	ap := event.Approval{ID: id, Tool: tool, Subject: subject}
	if len(args) > 0 {
		ap.Args = string(args)
		if c.executor != nil {
			if chs, ok := c.executor.PreviewToolCall(tool, args); ok {
				ap.Changes = make([]event.FileChange, len(chs))
				for i, ch := range chs {
					ap.Changes[i] = event.FileChange{
						Path: ch.Path, Kind: string(ch.Kind),
						Added: ch.Added, Removed: ch.Removed, Diff: ch.Diff,
					}
				}
			}
		}
	}
	c.sink.Emit(event.Event{Kind: event.ApprovalRequest, Approval: ap})
	// The agent now needs the user's attention; a Notification hook can ping an
	// external channel (desktop notice, phone) while the run blocks on the reply.
	// Tracked on autosaveWG so Close drains it instead of leaving a hook
	// subprocess running after teardown.
	notify := func(msg string) {
		c.autosaveWG.Add(1)
		go func() {
			defer c.autosaveWG.Done()
			c.hooks.Notification(ctx, msg)
		}()
	}
	if subject != "" {
		notify("approval needed: " + tool + " " + subject)
	} else {
		notify("approval needed: " + tool)
	}

	select {
	case r := <-reply:
		// Plan approvals are one-shot — never persist a session grant for them, or
		// every future plan would auto-approve.
		if r.allow && r.session && tool != PlanApprovalTool && tool != composeReplanTool {
			rule := permission.SessionGrantRuleForScope(tool, subject)
			c.mu.Lock()
			c.granted[rule] = true
			c.mu.Unlock()
		}
		if r.allow && r.persist && tool != PlanApprovalTool && tool != composeReplanTool && c.onRemember != nil {
			c.emitRememberResult(c.onRemember(permission.RememberRuleForScope(tool, subject)))
		}
		return r.allow, false, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.approvals, id)
		c.mu.Unlock()
		return false, false, ctx.Err()
	}
}

func (c *Controller) approvalBypassAllowsLocked(tool string) bool {
	// Plan approval and compose replan are user decisions that must never be
	// bypassed by YOLO or the approved-plan execution window.
	if tool == PlanApprovalTool || tool == composeReplanTool {
		return false
	}
	return c.toolApprovalMode == ToolApprovalYolo || c.approvedPlanAutoApproveTools
}

func (c *Controller) autoApprovalWouldAllowLocked(tool, subject string) bool {
	if tool == PlanApprovalTool || tool == composeReplanTool {
		return false
	}
	policy := c.policy
	policy.Mode = permission.Allow
	return policy.DecideSubject(tool, false, subject) == permission.Allow
}

func (c *Controller) sessionGrantAllowsLocked(tool, subject string) bool {
	for rule := range c.granted {
		if permission.RuleMatchesString(rule, tool, subject) {
			return true
		}
	}
	return false
}

func (c *Controller) emitRememberResult(r RememberResult) {
	if r.Err != nil {
		c.sink.Emit(event.Event{
			Kind:  event.Notice,
			Level: event.LevelWarn,
			Text:  fmt.Sprintf(i18n.M.PermissionSaveFailedFmt, r.Rule, r.Err),
		})
		return
	}
	switch {
	case r.Saved:
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: fmt.Sprintf(i18n.M.PermissionSavedFmt, r.Path, r.Rule)})
	case strings.TrimSpace(r.CoveredBy) != "":
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: fmt.Sprintf(i18n.M.PermissionAlreadyAllowedFmt, r.Path, r.CoveredBy)})
	}
}
