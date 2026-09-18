// Package event defines the typed event stream the agent emits as it runs a
// turn, and the Sink it emits to. It decouples "what happened" (the model
// produced reasoning, a tool was dispatched, a turn used N tokens) from "how to
// show it" (ANSI scrollback in a terminal, a card in a webview).
//
// The agent depends only on Sink; each frontend implements one. The chat TUI
// renders events to its scrollback; a headless run renders them to plain ANSI
// on stdout; a future GUI/serve transport forwards them to a webview or
// websocket. This replaces the old io.Writer contract, where the agent wrote
// pre-formatted ANSI and the consumer had to re-derive structure by matching
// line prefixes — fragile, and lossy for any frontend richer than a terminal.
package event

import (
	"github.com/zzycxz/hiq/internal/billing"
	"github.com/zzycxz/hiq/internal/evidence"
	"github.com/zzycxz/hiq/internal/nilutil"
	"github.com/zzycxz/hiq/internal/provider"
)

// Kind tags an Event. Read the field(s) documented for that kind.
type Kind int

const (
	// TurnStarted marks the start of one top-level Run (one user turn). Sinks
	// reset any per-turn rendering state on it. Carries no payload.
	TurnStarted Kind = iota
	// Reasoning is a thinking-mode reasoning delta (Text). Streamed before the
	// visible answer; sinks typically render it muted under a "thinking" header.
	Reasoning
	// Text is an answer-text delta (Text).
	Text
	// Message marks the assistant turn's text as complete: Text holds the full
	// answer and Reasoning the full chain-of-thought (both already streamed via
	// the deltas above). A sink may use it to re-render the streamed raw text as
	// styled markdown; a plain sink can ignore it.
	Message
	// ToolDispatch announces a tool call is about to run (Tool: ID/Name/Args/ReadOnly).
	ToolDispatch
	// ToolResult reports a finished tool call (Tool: Output/Err/Truncated set).
	ToolResult
	// ToolArgsDelta streams a tool call's raw argument fragment while the model
	// is still generating it (Tool: ID/Name; Text: the fragment) — apply_patch
	// previews render a live diff from these. Transient: not persisted.
	ToolArgsDelta
	// Usage carries per-turn token telemetry (Usage; Pricing optional, for cost).
	Usage
	// Notice is an out-of-band message — a warning, truncation, block, or
	// compaction notice (Level + Text).
	Notice
	// Phase marks a coordinator boundary, e.g. planner→executor handoff (Text =
	// label such as "planning").
	Phase
	// ApprovalRequest asks the frontend to approve a pending tool call
	// (Approval: ID/Tool/Subject). The run blocks until the controller's
	// Approve(ID, …) resolves it; a frontend shows a prompt and answers.
	ApprovalRequest
	// AskRequest asks the frontend to put one or more structured multiple-choice
	// questions to the user (Ask: ID + Questions). The run blocks until the
	// controller's AnswerQuestion(ID, …) resolves it. Powers the `ask` tool.
	AskRequest
	// TurnDone marks the end of one top-level Run (Err non-nil on failure;
	// nil also for a user cancellation, which is not an error). Always the
	// last event of a turn.
	TurnDone
	// CompactionStarted marks the start of a context-compaction pass (Compaction
	// payload: Trigger). A frontend shows a "compacting…" placeholder while the
	// summarizer runs; CompactionDone replaces it. Mirrors ToolDispatch/ToolResult.
	CompactionStarted
	// CompactionDone reports a finished compaction pass (Compaction payload:
	// Trigger/Messages/Summary/Archive). An aborted pass emits this with an empty
	// Summary so the placeholder still resolves. Replaces the older plain Notice
	// so a sink can render a distinct, expandable card.
	CompactionDone
	// ToolProgress streams a chunk of a still-running tool's combined output
	// (Tool: ID + Output = the new chunk). Emitted between ToolDispatch and
	// ToolResult for long tools like bash so a frontend can show live progress.
	// Appended last to keep the Kind values before it wire-stable.
	ToolProgress
	// MCPSurfaceReady fires once per server when its background-loaded surface
	// (prompts or resources) finishes after startup. Lets UIs refresh /mcp
	// status without polling. Text carries "<server>: <surface> ready (<count>
	// items)". Appended last to keep the Kind values before it wire-stable.
	MCPSurfaceReady
	// Retrying fires before each backoff sleep while the provider re-attempts the
	// connection+header phase after a transient failure (RetryAttempt of RetryMax).
	// A frontend shows a transient "retrying (n/m)" indicator that the next stream
	// event — or TurnDone — clears. Appended last to keep the Kind values before
	// it wire-stable.
	Retrying
	// Steer fires when a mid-turn steer message is consumed from the queue and
	// injected as a user message. Text carries the raw steer content (without the
	// wrapper prefix), so a frontend can display it to the user as confirmation.
	// Frontends use Steer to know a queued message has been delivered.
	Steer
	// Paused fires when the agent suspends itself between steps (a pause
	// request from the controller/frontend, e.g. "pause after the current
	// step"). State is preserved; Resumed follows when the run continues.
	// Appended last to keep the Kind values before it wire-stable.
	Paused
	// Resumed fires when a paused agent run continues. Clears a Paused
	// indicator in the frontend. Appended last to keep the Kind values before
	// it wire-stable.
	Resumed
	// ExpertCollab carries a finished expert-team collaboration record so the
	// frontend renders an expandable card (per-round expert answers + the
	// synthesis). Collab payload. Appended last to keep the Kind values before
	// it wire-stable.
	ExpertCollab
	// Item is the synthetic kind for the 4-1 item model (dual-track with
	// the flat kinds above). Item payload carries the structured form.
	Item
	// GuardianAssessment reports the outcome of a guardian sub-agent safety
	// review (Guardian payload: Outcome, RiskLevel, Rationale, review usage).
	// Appended last to keep the Kind values before it wire-stable.
	GuardianAssessment
	// ToolStarted is persisted after policy/validation and before execution, so
	// a run's durable ledger can distinguish "validated" from "finished".
	ToolStarted
	// StreamAttempt marks the local lifecycle of one sampling attempt within a
	// model round (begin | discard | commit). Host-local; never user-visible.
	StreamAttempt
	// TurnStatusChanged is a content-free lifecycle transition of the top-level
	// turn (Status carries the new event.TurnStatus; TurnID names the turn).
	TurnStatusChanged
	// PromptAnswered records that a durable Ask/approval item was answered and
	// the same turn resumed; ItemID carries the stable prompt id and answer
	// content remains in its purpose-built decision receipt.
	PromptAnswered
	// KindCount bounds the enum. Always the last entry.
	KindCount
)

// Level classifies a Notice so sinks can style or filter it.
type Level int

const (
	LevelInfo Level = iota
	LevelWarn
)

// Profile carries the subagent model/effort resolved for this call.
type Profile struct {
	Model  string
	Effort string
}

// Attachment is a file (e.g. a generated image) a tool produced alongside its
// text output, so a frontend can render it directly under the tool card without
// relying on the model to echo the path into its reply.
type Attachment struct {
	Path string `json:"path"` // repo-relative, under .hiq/attachments/
	Kind string `json:"kind"` // "image"
}

// Tool describes a tool call for ToolDispatch / ToolResult events. On dispatch
// only ID/Name/Args/ReadOnly are set; on result Output/Err/Truncated are filled
// in. Args is the raw JSON arguments — a sink compacts it for display.
type Tool struct {
	ID         string
	Name       string
	Args       string
	Output     string // ToolResult: the result text fed to the model
	Err        string // ToolResult: non-empty when the call failed or was blocked
	ReadOnly   bool
	Truncated  bool  // ToolResult: Output was head+tailed before display/model
	DurationMs int64 // ToolResult: wall-clock execution time in milliseconds
	// Partial marks an early ToolDispatch emitted when a call begins (ID/Name set,
	// Args still streaming) so a frontend can show the card immediately; a second,
	// full ToolDispatch (Partial false, Args set) follows when the call completes.
	Partial bool
	// ParentID, when set, is the ID of the tool call that spawned this one — a
	// sub-agent's calls carry the parent `task` call's ID so a frontend can nest
	// them under it. Empty for top-level calls.
	ParentID string
	// Attachments carries files the tool generated (e.g. image_generate pictures
	// saved under .hiq/attachments/), parsed from the result text so the
	// frontend can display them regardless of what the model writes back.
	Attachments []Attachment
	FileDiff
	Profile *Profile // ToolDispatch: subagent model/effort (set for task/skill calls)
	// AttemptID is the host-local stream_attempt id for speculative partials;
	// TodoWritten/Todos carry todo-state transitions that ride on a terminal
	// tool result so a durable ledger can reconstruct them.
	AttemptID   string
	TodoWritten bool
	Todos       []Todo
	// RunState is the host-local run state of the call; ResolvedName is the
	// canonical tool name after alias resolution; CapabilityID names the
	// capability that served the call (capability router). All host-local.
	RunState     provider.ToolRunState
	ResolvedName string
	CapabilityID string
	// StartedAt/EndedAt are unix-ms bounds of execution (zero when never run).
	StartedAt int64
	EndedAt   int64
}

// FileDiff is a previewed change carried on a writer tool's full ToolDispatch
// and on its ApprovalRequest, so a frontend can render +/- lines before the
// call runs. Diff is the unified diff (empty for read-only tools, binary files,
// or no-op changes); Added/Removed are its line tallies.
type FileDiff struct {
	Diff    string
	Added   int
	Removed int
}

// Decision is one option the user can pick on an approval card (spec 5-7).
// The default 3 (once/session/always) stay unchanged; fine-grained options
// appear only when the tool carries path or network restrictions.
type Decision struct {
	Label        string        `json:"label"`
	Scope        string        `json:"scope"` // once | session | always | path | host
	Restrictions *Restrictions `json:"restrictions,omitempty"`
}

// Restrictions narrows a grant to specific paths or hosts (spec 5-7).
type Restrictions struct {
	AllowedPaths []string `json:"allowed_paths,omitempty"` // glob patterns
	AllowedHosts []string `json:"allowed_hosts,omitempty"` // host[:port]
}

// Approval identifies a pending tool-call approval for an ApprovalRequest
// event. ID correlates the request with the controller's Approve(ID, …) reply.
type Approval struct {
	ID      string
	Tool    string
	Subject string
	// Decisions (spec 5-7) are the selectable options; nil = default 3.
	Decisions []Decision
	// Args is the raw JSON arguments of the call being approved, so a card can
	// show specifics itself (the bash command, target paths). Empty for
	// synthetic approvals (plan/replan) that have no tool args.
	Args string
	// Changes carries the previewed per-file changes for writer tools (via the
	// tool's Previewer, computed at request time — before the permission
	// decision). Nil for tools that can't describe their change.
	Changes []FileChange
}

// FileChange is one file within a previewed multi-file change (the Approval
// payload). Kind mirrors diff.Kind: "create" | "modify" | "delete".
type FileChange struct {
	Path    string
	Kind    string
	Added   int
	Removed int
	Diff    string
}

// AskOption is one choice the user can pick for an AskQuestion.
type AskOption struct {
	Label       string
	Description string // optional one-line explanation shown under the label
}

// AskQuestion is one structured question the `ask` tool puts to the user.
type AskQuestion struct {
	ID      string // stable per-question id, so answers correlate back
	Header  string // short label (the tab title)
	Prompt  string // the question text
	Options []AskOption
	Multi   bool // allow selecting more than one option
}

// Ask carries an AskRequest: a batch of questions and the ID that correlates the
// controller's AnswerQuestion(ID, …) reply.
type Ask struct {
	ID        string
	Questions []AskQuestion
}

// Compaction carries a context-compaction pass for the CompactionStarted /
// CompactionDone events. On CompactionStarted only Trigger is set. On
// CompactionDone, Messages/Summary/Archive are filled in (an aborted pass leaves
// Summary empty). Trigger is "auto" (the prompt reached the window threshold) or
// "manual" (the user ran /compact).
type Compaction struct {
	Trigger  string // "auto" | "manual"
	Messages int    // Done: how many messages were folded into the summary
	Summary  string // Done: the briefing the agent keeps relying on
	Archive  string // Done: path the dropped originals were archived to ("" if none)
}

// AskAnswer is the user's reply to one AskQuestion: the chosen option label(s)
// (a free-typed answer is carried as a single Selected entry).
type AskAnswer struct {
	QuestionID string
	Selected   []string
}

// CacheDiagnostics describes whether and why the cacheable prefix changed since
// the last turn. It rides on the Usage event so every frontend can show
// cache-churn attribution.
type CacheDiagnostics struct {
	PrefixHash          string
	PrefixChanged       bool
	PrefixChangeReasons []string // "system", "tools", "log_rewrite"
	SystemHash          string
	ToolsHash           string
	LogRewriteVersion   int
	ToolSchemaTokens    int
	CacheMissTokens     int
	CacheHitTokens      int
}

// CollabAnswer is one expert's answer within one round of a multi-model
// collaboration. ExpertName is the contributing team member; Text is its
// answer. Mirrors experts.ExpertAnswer at the event boundary so the event
// package doesn't depend on experts.
type CollabAnswer struct {
	ExpertName string
	Text       string
}

// Collab is the payload of an ExpertCollab event: everything a frontend needs
// to render a finished collaboration as an expandable card — provenance (team,
// task, mode), the per-round expert answers, the synthesis, and when it ran.
// Mirrors experts.CollabRecord (the persisted form) field-for-field.
type Collab struct {
	RunID     string
	TeamID    string
	TeamName  string
	Task      string
	Mode      string
	Rounds    [][]CollabAnswer
	Synthesis string
	CreatedAt int64 // unix ms
}

// Event is one increment in a turn's event stream. Read the field(s) documented
// for Kind; the others are zero.
type Event struct {
	Kind      Kind
	MessageID string // local identity shared by streaming and persisted messages
	AttemptID string // owning sampling attempt; never provider-visible
	Source    string // optional display/event source (executor, planner, subagent, ...)
	// TurnID is the stable id of the owning top-level turn; Sequence is the
	// monotonic session-local event sequence; Status is the post-event lifecycle
	// state. ItemID correlates durable session-inbox events (Steer/TurnDone).
	TurnID   string
	Sequence uint64
	Status   TurnStatus
	ItemID   string
	// SessionID/RuntimeEpoch/SubmissionID stamp durable display routing and the
	// originating controller incarnation; Recovery carries optional local recovery
	// details; StreamAttempt carries a sampling-attempt lifecycle step.
	SessionID     string
	RuntimeEpoch  string
	SubmissionID  string
	Recovery      *RecoveryStatus
	StreamAttempt StreamAttemptInfo
	Text          string            // Reasoning / Text / Message / Notice / Phase
	Reasoning     string            // Message: the full reasoning chain
	Tool          Tool              // ToolDispatch / ToolResult
	Usage         *provider.Usage   // Usage
	Pricing       *provider.Pricing // Usage: for cost display (nil = omit cost)
	// CostQuote is the host-side quote attached to a Usage event; sinks must not
	// reprice. UsageSource names the billable call source (executor, planner,
	// subagent, ...); empty means executor for compatibility.
	CostQuote        *billing.CostQuote
	UsageSource      string
	CacheDiagnostics *CacheDiagnostics // Usage: cache-churn attribution (nil = N/A)
	// SessionHit/SessionMiss carry cumulative cache tokens across the whole
	// session (Usage events only), so a frontend can show the aggregate hit-rate
	// — which doesn't crater on a short turn or after compaction — alongside
	// Usage's single-turn numbers.
	SessionHit  int      // Usage: cumulative cache-hit prompt tokens this session
	SessionMiss int      // Usage: cumulative cache-miss prompt tokens this session
	Level       Level    // Notice
	Approval    Approval // ApprovalRequest
	Ask         Ask      // AskRequest
	Err         error    // TurnDone: non-nil on failure
	// Outcome is an optional machine-readable recoverable outcome on TurnDone.
	Outcome      string
	Compaction   Compaction // Compaction
	RetryAttempt int        // Retrying: 1-based attempt about to be made
	RetryMax     int        // Retrying: total attempts before giving up
	RetryAfterMs int64      // Retrying: backoff delay before the attempt (0 = immediate)
	Collab       Collab     // ExpertCollab
	// ModelRef is the canonical "provider/model" ref that produced this event.
	// Set on Usage and GuardianAssessment events.
	ModelRef string
	// Guardian carries the guardian review payload (GuardianAssessment only).
	Guardian GuardianResult
	// Item carries the item-model payload (4-1 dual-track). Non-nil only on
	// the synthetic "Item" Kind; old sinks skip it naturally.
	Item *ItemEvent
}

// ReadinessAuditSink is an optional sink capability. Sinks that do not care
// about readiness audit receipts can implement only Sink and will ignore them.
type ReadinessAuditSink interface {
	RecordReadinessAudit(evidence.ReadinessAudit)
}

// RecordReadinessAudit forwards a readiness audit receipt to sinks that opt in.
func RecordReadinessAudit(s Sink, a evidence.ReadinessAudit) {
	if nilutil.IsNil(s) {
		return
	}
	if rs, ok := s.(ReadinessAuditSink); ok {
		rs.RecordReadinessAudit(a)
	}
}

// GuardianResult carries the outcome of a guardian sub-agent safety review.
// Emitted with Kind=GuardianAssessment after each review completes.
type GuardianResult struct {
	ID                string            // unique review id
	Tool              string            // tool being reviewed (e.g. "bash")
	Subject           string            // call subject (e.g. "rm -rf /tmp/build")
	Outcome           string            // "allow" | "deny"
	RiskLevel         string            // "low" | "medium" | "high" | "critical"
	UserAuthorization string            // "unknown" | "low" | "medium" | "high"
	Rationale         string            // one-sentence reason
	DurationMs        int64             // wall-clock review time
	Usage             *provider.Usage   // guardian review token telemetry
	Pricing           *provider.Pricing // for cost display (nil = omit cost)
}

// Sink consumes a turn's events. The agent calls Emit serially from its run
// loop (tool execution may fan out across goroutines, but emission does not),
// so an implementation need not be safe for concurrent Emit. Emit must not
// block indefinitely — a channel-backed sink should be buffered or drained by
// a live reader.
type Sink interface {
	Emit(Event)
}

// FuncSink adapts a plain function to a Sink.
type FuncSink func(Event)

// Emit calls the wrapped function.
func (f FuncSink) Emit(e Event) {
	if f != nil {
		f(e)
	}
}

// Discard is a Sink that drops every event. Useful in tests and for runs that
// only care about the final session state.
var Discard Sink = FuncSink(func(Event) {})
