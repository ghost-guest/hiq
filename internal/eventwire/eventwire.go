// Package eventwire is the shared JSON codec for event.Event streams that cross
// a process boundary: the remote host serializes typed controller events into
// the wire form, and the desktop deserializes them back so its tab sink sees the
// same stream a local controller would emit. The wire shape is field-for-field
// the one desktop/wire.go and internal/serve/wire.go already exchange with the
// React frontend, so all three consumers stay contract-compatible.
package eventwire

import (
	"errors"

	"github.com/zzycxz/fairpeer/internal/agent"
	"github.com/zzycxz/fairpeer/internal/event"
	"github.com/zzycxz/fairpeer/internal/provider"
)

// Event is the JSON shape an event.Event takes across the wire.
type Event struct {
	Kind         string                `json:"kind"`
	MessageID    string                `json:"messageId,omitempty"`
	AttemptID    string                `json:"attemptId,omitempty"`
	Source       string                `json:"source,omitempty"`
	SessionID    string                `json:"sessionId,omitempty"`
	RuntimeEpoch string                `json:"runtimeEpoch,omitempty"`
	SubmissionID string                `json:"submissionId,omitempty"`
	TurnID       string                `json:"turnId,omitempty"`
	Sequence     uint64                `json:"seq,omitempty"`
	Status       string                `json:"status,omitempty"`
	ItemID       string                `json:"itemId,omitempty"`
	Text         string                `json:"text,omitempty"`
	Reasoning    string                `json:"reasoning,omitempty"`
	Level        string                `json:"level,omitempty"`
	Tool         *Tool                 `json:"tool,omitempty"`
	Usage        *Usage                `json:"usage,omitempty"`
	Approval     *Approval             `json:"approval,omitempty"`
	Ask          *Ask                  `json:"ask,omitempty"`
	Compaction   *Compaction           `json:"compaction,omitempty"`
	Recovery     *event.RecoveryStatus `json:"recovery,omitempty"`
	Err          string                `json:"err,omitempty"`
	Outcome      string                `json:"outcome,omitempty"`
	RetryAttempt int                   `json:"retryAttempt,omitempty"`
	RetryMax     int                   `json:"retryMax,omitempty"`
	RetryAfterMs int64                 `json:"retryAfterMs,omitempty"`
}

type Compaction struct {
	Trigger  string `json:"trigger,omitempty"`
	Messages int    `json:"messages,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Archive  string `json:"archive,omitempty"`
}

type AskOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type AskQuestion struct {
	ID      string      `json:"id"`
	Header  string      `json:"header,omitempty"`
	Prompt  string      `json:"prompt"`
	Options []AskOption `json:"options"`
	Multi   bool        `json:"multi,omitempty"`
}

type Ask struct {
	ID        string        `json:"id"`
	Questions []AskQuestion `json:"questions"`
}

type Tool struct {
	ID          string       `json:"id,omitempty"`
	Name        string       `json:"name"`
	Args        string       `json:"args,omitempty"`
	Output      string       `json:"output,omitempty"`
	Err         string       `json:"err,omitempty"`
	ReadOnly    bool         `json:"readOnly"`
	Truncated   bool         `json:"truncated,omitempty"`
	DurationMs  int64        `json:"durationMs,omitempty"`
	Partial     bool         `json:"partial,omitempty"`
	ParentID    string       `json:"parentId,omitempty"`
	Profile     *Profile     `json:"profile,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	FileDiff    *FileDiff    `json:"fileDiff,omitempty"`
	// Todos/TodoWritten let a durable ledger reconstruct todo state transitions
	// that ride on terminal tool results. Additive over the original wire shape.
	Todos       []event.Todo `json:"todos,omitempty"`
	TodoWritten bool         `json:"todoWritten,omitempty"`
	// RunState/AttemptID/ResolvedName/CapabilityID are host-local ledger facts.
	RunState     provider.ToolRunState `json:"runState,omitempty"`
	AttemptID    string                `json:"attemptId,omitempty"`
	ResolvedName string                `json:"resolvedName,omitempty"`
	CapabilityID string                `json:"capabilityId,omitempty"`
	// StartedAt/EndedAt are unix-ms execution bounds (zero when never run).
	StartedAt int64 `json:"startedAt,omitempty"`
	EndedAt   int64 `json:"endedAt,omitempty"`
}

// FileDiff is the JSON form of event.FileDiff (empty Diff = nothing to show).
type FileDiff struct {
	Diff    string `json:"diff"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

type Attachment struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type Profile struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
	CacheHitTokens   int `json:"cacheHitTokens"`
	CacheMissTokens  int `json:"cacheMissTokens"`
	CacheWriteTokens int `json:"cacheWriteTokens"`
	ReasoningTokens  int `json:"reasoningTokens,omitempty"`
	// Session-cumulative cache tokens; mapped back onto event.Event's
	// SessionHit/SessionMiss by FromWire.
	SessionCacheHitTokens  int     `json:"sessionCacheHitTokens"`
	SessionCacheMissTokens int     `json:"sessionCacheMissTokens"`
	Cost                   float64 `json:"cost,omitempty"`
	Currency               string  `json:"currency,omitempty"`
}

type Approval struct {
	ID      string       `json:"id"`
	Tool    string       `json:"tool"`
	Subject string       `json:"subject"`
	Args    string       `json:"args,omitempty"`
	Changes []FileChange `json:"changes,omitempty"`
}

// FileChange is one file within a previewed multi-file approval.
type FileChange struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // "create" | "modify" | "delete"
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Diff    string `json:"diff"`
}

// kindNames maps the event.Kind enum to stable wire strings.
var kindNames = map[event.Kind]string{
	event.TurnStarted:       "turn_started",
	event.Reasoning:         "reasoning",
	event.Text:              "text",
	event.Message:           "message",
	event.ToolDispatch:      "tool_dispatch",
	event.ToolResult:        "tool_result",
	event.ToolArgsDelta:     "tool_args_delta",
	event.Usage:             "usage",
	event.Notice:            "notice",
	event.Phase:             "phase",
	event.ApprovalRequest:   "approval_request",
	event.AskRequest:        "ask_request",
	event.TurnDone:          "turn_done",
	event.CompactionStarted: "compaction_started",
	event.CompactionDone:    "compaction_done",
	event.ToolProgress:      "tool_progress",
	event.MCPSurfaceReady:   "mcp_surface_ready",
	event.Retrying:          "retrying",
	event.Steer:             "steer",
	event.ToolStarted:       "tool_started",
	event.StreamAttempt:     "stream_attempt",
	event.TurnStatusChanged: "turn_status",
	event.PromptAnswered:    "prompt_answered",
}

// KindName returns the stable wire name of one event kind, or false for a kind
// outside the known set.
func KindName(kind event.Kind) (string, bool) {
	name, ok := kindNames[kind]
	return name, ok
}

// KindNames returns every known wire kind name, bounded by event.KindCount.
func KindNames() []string {
	names := make([]string, 0, int(event.KindCount))
	for kind := event.Kind(0); kind < event.KindCount; kind++ {
		if name, ok := kindNames[kind]; ok {
			names = append(names, name)
		}
	}
	return names
}

// wireKinds is the reverse map, plus the few kinds that share no wire name and
// are dropped (ExpertCollab, Item, Paused, Resumed — same set desktop's toWire
// drops today; see desktop/wire.go).
var wireKinds = func() map[string]event.Kind {
	m := make(map[string]event.Kind, len(kindNames))
	for k, name := range kindNames {
		m[name] = k
	}
	return m
}()

// ToWire converts an event.Event into its wire form. It matches desktop's
// toWire, including the goal-marker strip on live Message text.
func ToWire(e event.Event) Event {
	w := Event{
		Kind:         kindNames[e.Kind],
		MessageID:    e.MessageID,
		AttemptID:    e.AttemptID,
		Source:       e.Source,
		SessionID:    e.SessionID,
		RuntimeEpoch: e.RuntimeEpoch,
		SubmissionID: e.SubmissionID,
		TurnID:       e.TurnID,
		Sequence:     e.Sequence,
		ItemID:       e.ItemID,
		Outcome:      e.Outcome,
		Text:         e.Text,
		Reasoning:    e.Reasoning,
	}
	if e.Status != "" {
		w.Status = string(e.Status)
	}
	if e.Recovery != nil {
		w.Recovery = e.Recovery
	}
	switch e.Kind {
	case event.Notice:
		if e.Level == event.LevelWarn {
			w.Level = "warn"
		} else {
			w.Level = "info"
		}
	case event.ToolArgsDelta:
		w.Text = e.Text
		w.Tool = &Tool{ID: e.Tool.ID, Name: e.Tool.Name, ReadOnly: true}
	case event.ToolDispatch, event.ToolStarted, event.ToolResult, event.ToolProgress:
		wt := &Tool{
			ID: e.Tool.ID, Name: e.Tool.Name, Args: e.Tool.Args,
			Output: e.Tool.Output, Err: e.Tool.Err,
			ReadOnly: e.Tool.ReadOnly, Truncated: e.Tool.Truncated,
			DurationMs: e.Tool.DurationMs, Partial: e.Tool.Partial,
			ParentID: e.Tool.ParentID,
			// Host-local ledger facts carried so a durable ledger can rebuild
			// todo state and run state from the wire record.
			RunState: e.Tool.RunState, AttemptID: e.Tool.AttemptID,
			ResolvedName: e.Tool.ResolvedName, CapabilityID: e.Tool.CapabilityID,
			StartedAt: e.Tool.StartedAt, EndedAt: e.Tool.EndedAt,
			TodoWritten: e.Tool.TodoWritten,
		}
		if len(e.Tool.Todos) > 0 {
			wt.Todos = e.Tool.Todos
		}
		if len(e.Tool.Attachments) > 0 {
			wt.Attachments = make([]Attachment, len(e.Tool.Attachments))
			for i, a := range e.Tool.Attachments {
				wt.Attachments[i] = Attachment{Path: a.Path, Kind: a.Kind}
			}
		}
		if e.Tool.Profile != nil {
			wt.Profile = &Profile{Model: e.Tool.Profile.Model, Effort: e.Tool.Profile.Effort}
		}
		if e.Tool.FileDiff.Diff != "" || e.Tool.FileDiff.Added != 0 || e.Tool.FileDiff.Removed != 0 {
			wt.FileDiff = &FileDiff{Diff: e.Tool.FileDiff.Diff, Added: e.Tool.FileDiff.Added, Removed: e.Tool.FileDiff.Removed}
		}
		w.Tool = wt
	case event.Usage:
		if u := e.Usage; u != nil {
			w.Usage = &Usage{
				PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
				TotalTokens: u.TotalTokens, CacheHitTokens: u.CacheHitTokens,
				CacheMissTokens: u.CacheMissTokens, CacheWriteTokens: u.CacheWriteTokens, ReasoningTokens: u.ReasoningTokens,
				SessionCacheHitTokens: e.SessionHit, SessionCacheMissTokens: e.SessionMiss,
			}
		}
	case event.ApprovalRequest:
		wa := &Approval{ID: e.Approval.ID, Tool: e.Approval.Tool, Subject: e.Approval.Subject, Args: e.Approval.Args}
		if len(e.Approval.Changes) > 0 {
			wa.Changes = make([]FileChange, len(e.Approval.Changes))
			for i, ch := range e.Approval.Changes {
				wa.Changes[i] = FileChange{Path: ch.Path, Kind: ch.Kind, Added: ch.Added, Removed: ch.Removed, Diff: ch.Diff}
			}
		}
		w.Approval = wa
	case event.AskRequest:
		w.Ask = toWireAsk(e.Ask)
	case event.CompactionStarted, event.CompactionDone:
		w.Compaction = &Compaction{
			Trigger: e.Compaction.Trigger, Messages: e.Compaction.Messages,
			Summary: e.Compaction.Summary, Archive: e.Compaction.Archive,
		}
	case event.TurnDone:
		if e.Err != nil {
			w.Err = e.Err.Error()
		}
	case event.Retrying:
		w.RetryAttempt = e.RetryAttempt
		w.RetryMax = e.RetryMax
		w.RetryAfterMs = e.RetryAfterMs
	case event.Message:
		w.Text = agent.StripGoalMarkers(e.Text)
	}
	return w
}

func toWireAsk(a event.Ask) *Ask {
	qs := make([]AskQuestion, len(a.Questions))
	for i, q := range a.Questions {
		opts := make([]AskOption, len(q.Options))
		for j, o := range q.Options {
			opts[j] = AskOption{Label: o.Label, Description: o.Description}
		}
		qs[i] = AskQuestion{ID: q.ID, Header: q.Header, Prompt: q.Prompt, Options: opts, Multi: q.Multi}
	}
	return &Ask{ID: a.ID, Questions: qs}
}

func fromWireAsk(a Ask) event.Ask {
	out := event.Ask{ID: a.ID}
	out.Questions = make([]event.AskQuestion, len(a.Questions))
	for i, q := range a.Questions {
		opts := make([]event.AskOption, len(q.Options))
		for j, o := range q.Options {
			opts[j] = event.AskOption{Label: o.Label, Description: o.Description}
		}
		out.Questions[i] = event.AskQuestion{ID: q.ID, Header: q.Header, Prompt: q.Prompt, Options: opts, Multi: q.Multi}
	}
	return out
}

// FromWire decodes a wire event back into an event.Event so a desktop-side sink
// can consume a remote host's stream exactly like a local controller's. It is
// the inverse of ToWire; the only lossy fields are Pricing (its cost is
// precomputed into Usage.Cost on the wire) and a TurnDone error's type (a
// string error is rebuilt).
func FromWire(w Event) event.Event {
	e := event.Event{
		Text:         w.Text,
		Reasoning:    w.Reasoning,
		MessageID:    w.MessageID,
		AttemptID:    w.AttemptID,
		Source:       w.Source,
		SessionID:    w.SessionID,
		RuntimeEpoch: w.RuntimeEpoch,
		SubmissionID: w.SubmissionID,
		TurnID:       w.TurnID,
		Sequence:     w.Sequence,
		ItemID:       w.ItemID,
		Outcome:      w.Outcome,
	}
	if w.Status != "" {
		e.Status = event.TurnStatus(w.Status)
	}
	if w.Recovery != nil {
		e.Recovery = w.Recovery
	}
	if k, ok := wireKinds[w.Kind]; ok {
		e.Kind = k
	}
	switch e.Kind {
	case event.Notice:
		if w.Level == "warn" {
			e.Level = event.LevelWarn
		} else {
			e.Level = event.LevelInfo
		}
	case event.ToolArgsDelta:
		if w.Tool != nil {
			e.Tool = event.Tool{ID: w.Tool.ID, Name: w.Tool.Name, ReadOnly: true}
		}
	case event.ToolDispatch, event.ToolStarted, event.ToolResult, event.ToolProgress:
		if t := w.Tool; t != nil {
			e.Tool = event.Tool{
				ID: t.ID, Name: t.Name, Args: t.Args, Output: t.Output, Err: t.Err,
				ReadOnly: t.ReadOnly, Truncated: t.Truncated, DurationMs: t.DurationMs,
				Partial: t.Partial, ParentID: t.ParentID,
				RunState: t.RunState, AttemptID: t.AttemptID,
				ResolvedName: t.ResolvedName, CapabilityID: t.CapabilityID,
				StartedAt: t.StartedAt, EndedAt: t.EndedAt,
				TodoWritten: t.TodoWritten, Todos: t.Todos,
			}
			if len(t.Attachments) > 0 {
				e.Tool.Attachments = make([]event.Attachment, len(t.Attachments))
				for i, a := range t.Attachments {
					e.Tool.Attachments[i] = event.Attachment{Path: a.Path, Kind: a.Kind}
				}
			}
			if t.Profile != nil {
				e.Tool.Profile = &event.Profile{Model: t.Profile.Model, Effort: t.Profile.Effort}
			}
			if t.FileDiff != nil {
				e.Tool.FileDiff = event.FileDiff{Diff: t.FileDiff.Diff, Added: t.FileDiff.Added, Removed: t.FileDiff.Removed}
			}
		}
	case event.Usage:
		if u := w.Usage; u != nil {
			e.Usage = &provider.Usage{
				PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
				TotalTokens: u.TotalTokens, CacheHitTokens: u.CacheHitTokens,
				CacheMissTokens: u.CacheMissTokens, CacheWriteTokens: u.CacheWriteTokens,
				ReasoningTokens: u.ReasoningTokens,
			}
			e.SessionHit = u.SessionCacheHitTokens
			e.SessionMiss = u.SessionCacheMissTokens
		}
	case event.ApprovalRequest:
		if a := w.Approval; a != nil {
			e.Approval = event.Approval{ID: a.ID, Tool: a.Tool, Subject: a.Subject, Args: a.Args}
			if len(a.Changes) > 0 {
				e.Approval.Changes = make([]event.FileChange, len(a.Changes))
				for i, ch := range a.Changes {
					e.Approval.Changes[i] = event.FileChange{Path: ch.Path, Kind: ch.Kind, Added: ch.Added, Removed: ch.Removed, Diff: ch.Diff}
				}
			}
		}
	case event.AskRequest:
		if a := w.Ask; a != nil {
			e.Ask = fromWireAsk(*a)
		}
	case event.CompactionStarted, event.CompactionDone:
		if c := w.Compaction; c != nil {
			e.Compaction = event.Compaction{Trigger: c.Trigger, Messages: c.Messages, Summary: c.Summary, Archive: c.Archive}
		}
	case event.TurnDone:
		if w.Err != "" {
			e.Err = errors.New(w.Err)
		}
	case event.Retrying:
		e.RetryAttempt = w.RetryAttempt
		e.RetryMax = w.RetryMax
		e.RetryAfterMs = w.RetryAfterMs
	}
	return e
}
