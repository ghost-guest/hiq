package main

import (
	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/event"
)

// wireEvent is the JSON shape an event.Event takes when emitted to the webview.
// It mirrors the serve transport's SSE wire form field-for-field on purpose: both
// frontends consume the identical typed stream, so the React client and a browser
// SSE client can share contract types. The Kind enum becomes a stable string and
// the TurnDone error becomes a message, since neither serializes cleanly.
//
// (Kept in step with internal/serve/wire.go by hand for now — the two transports
// may diverge later; if they don't, this is the obvious thing to lift into a
// shared event.ToWire.)
type wireEvent struct {
	Kind         string          `json:"kind"`
	Text         string          `json:"text,omitempty"`
	Reasoning    string          `json:"reasoning,omitempty"`
	Level        string          `json:"level,omitempty"`
	Tool         *wireTool       `json:"tool,omitempty"`
	Usage        *wireUsage      `json:"usage,omitempty"`
	Approval     *wireApproval   `json:"approval,omitempty"`
	Ask          *wireAsk        `json:"ask,omitempty"`
	Compaction   *wireCompaction `json:"compaction,omitempty"`
	Err          string          `json:"err,omitempty"`
	RetryAttempt int             `json:"retryAttempt,omitempty"`
	RetryMax     int             `json:"retryMax,omitempty"`
	RetryAfterMs int64           `json:"retryAfterMs,omitempty"`
}

// wireCompaction is the JSON form of an event.Compaction. On a compaction_started
// event only Trigger is set; compaction_done carries the rest (an aborted pass
// leaves Summary empty so the frontend drops its placeholder).
type wireCompaction struct {
	Trigger  string `json:"trigger,omitempty"`
	Messages int    `json:"messages,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Archive  string `json:"archive,omitempty"`
}

type wireAskOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type wireAskQuestion struct {
	ID      string          `json:"id"`
	Header  string          `json:"header,omitempty"`
	Prompt  string          `json:"prompt"`
	Options []wireAskOption `json:"options"`
	Multi   bool            `json:"multi,omitempty"`
}

type wireAsk struct {
	ID        string            `json:"id"`
	Questions []wireAskQuestion `json:"questions"`
}

type wireTool struct {
	ID          string           `json:"id,omitempty"`
	Name        string           `json:"name"`
	Args        string           `json:"args,omitempty"`
	Output      string           `json:"output,omitempty"`
	Err         string           `json:"err,omitempty"`
	ReadOnly    bool             `json:"readOnly"`
	Truncated   bool             `json:"truncated,omitempty"`
	DurationMs  int64            `json:"durationMs,omitempty"`
	Partial     bool             `json:"partial,omitempty"`
	ParentID    string           `json:"parentId,omitempty"`
	Profile     *wireProfile     `json:"profile,omitempty"`
	Attachments []wireAttachment `json:"attachments,omitempty"`
	// FileDiff carries the previewed change on a writer tool's dispatch, so the
	// card renders the server-side diff (authoritative, covers apply_patch and
	// encoding-aware previews) instead of re-deriving one from the args.
	FileDiff *wireFileDiff `json:"fileDiff,omitempty"`
}

// wireFileDiff is the JSON form of event.FileDiff (empty Diff = nothing to show).
type wireFileDiff struct {
	Diff    string `json:"diff"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

type wireAttachment struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type wireProfile struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

type wireUsage struct {
	PromptTokens     int                   `json:"promptTokens"`
	CompletionTokens int                   `json:"completionTokens"`
	TotalTokens      int                   `json:"totalTokens"`
	CacheHitTokens   int                   `json:"cacheHitTokens"`
	CacheMissTokens  int                   `json:"cacheMissTokens"`
	CacheWriteTokens int                   `json:"cacheWriteTokens"`
	ReasoningTokens  int                   `json:"reasoningTokens,omitempty"`
	CacheDiagnostics *wireCacheDiagnostics `json:"cacheDiagnostics,omitempty"`
	// Session-cumulative cache tokens — the status line shows the aggregate
	// hit-rate Σhit/Σ(hit+miss), steadier than the single-turn CacheHitTokens.
	// some providers do not report cache tokens, so these stay at 0.
	SessionCacheHitTokens  int `json:"sessionCacheHitTokens"`
	SessionCacheMissTokens int `json:"sessionCacheMissTokens"`
	// Cost of this turn per the model's pricing table (nil = pricing unknown /
	// not configured, in which case the UI omits the amount). Mirrors serve.
	Cost     float64 `json:"cost,omitempty"`
	Currency string  `json:"currency,omitempty"`
	CostUSD  float64 `json:"costUsd,omitempty"`
}

type wireCacheDiagnostics struct {
	PrefixHash          string   `json:"prefixHash"`
	PrefixChanged       bool     `json:"prefixChanged"`
	PrefixChangeReasons []string `json:"prefixChangeReasons,omitempty"`
	SystemHash          string   `json:"systemHash"`
	ToolsHash           string   `json:"toolsHash"`
	LogRewriteVersion   int      `json:"logRewriteVersion"`
	ToolSchemaTokens    int      `json:"toolSchemaTokens"`
	CacheMissTokens     int      `json:"cacheMissTokens"`
	CacheHitTokens      int      `json:"cacheHitTokens"`
}

type wireApproval struct {
	ID      string `json:"id"`
	Tool    string `json:"tool"`
	Subject string `json:"subject"`
	// Args is the raw JSON of the call being approved, so the card can show
	// specifics (bash command, target path) itself. Changes carries the
	// previewed per-file diffs for writer tools — the approval is no longer a
	// blind sign-off on a one-line subject.
	Args    string           `json:"args,omitempty"`
	Changes []wireFileChange `json:"changes,omitempty"`
}

// wireFileChange is one file within a previewed multi-file approval.
type wireFileChange struct {
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
}

// toWireAsk converts an event.Ask into its JSON wire form.
func toWireAsk(a event.Ask) *wireAsk {
	qs := make([]wireAskQuestion, len(a.Questions))
	for i, q := range a.Questions {
		opts := make([]wireAskOption, len(q.Options))
		for j, o := range q.Options {
			opts[j] = wireAskOption{Label: o.Label, Description: o.Description}
		}
		qs[i] = wireAskQuestion{ID: q.ID, Header: q.Header, Prompt: q.Prompt, Options: opts, Multi: q.Multi}
	}
	return &wireAsk{ID: a.ID, Questions: qs}
}

// toWire converts an event.Event into its JSON wire form.
func toWire(e event.Event) wireEvent {
	w := wireEvent{Kind: kindNames[e.Kind], Text: e.Text, Reasoning: e.Reasoning}
	switch e.Kind {
	case event.Notice:
		if e.Level == event.LevelWarn {
			w.Level = "warn"
		} else {
			w.Level = "info"
		}
	case event.ToolArgsDelta:
		w.Text = e.Text
		w.Tool = &wireTool{ID: e.Tool.ID, Name: e.Tool.Name, ReadOnly: true}
	case event.ToolDispatch, event.ToolResult, event.ToolProgress:
		wt := &wireTool{
			ID: e.Tool.ID, Name: e.Tool.Name, Args: e.Tool.Args,
			Output: e.Tool.Output, Err: e.Tool.Err,
			ReadOnly: e.Tool.ReadOnly, Truncated: e.Tool.Truncated,
			DurationMs: e.Tool.DurationMs, Partial: e.Tool.Partial,
			ParentID: e.Tool.ParentID,
		}
		if len(e.Tool.Attachments) > 0 {
			wt.Attachments = make([]wireAttachment, len(e.Tool.Attachments))
			for i, a := range e.Tool.Attachments {
				wt.Attachments[i] = wireAttachment{Path: a.Path, Kind: a.Kind}
			}
		}
		if e.Tool.Profile != nil {
			wt.Profile = &wireProfile{Model: e.Tool.Profile.Model, Effort: e.Tool.Profile.Effort}
		}
		if e.Tool.FileDiff.Diff != "" || e.Tool.FileDiff.Added != 0 || e.Tool.FileDiff.Removed != 0 {
			wt.FileDiff = &wireFileDiff{Diff: e.Tool.FileDiff.Diff, Added: e.Tool.FileDiff.Added, Removed: e.Tool.FileDiff.Removed}
		}
		w.Tool = wt
	case event.Usage:
		if u := e.Usage; u != nil {
			w.Usage = &wireUsage{
				PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
				TotalTokens: u.TotalTokens, CacheHitTokens: u.CacheHitTokens,
				CacheMissTokens: u.CacheMissTokens, CacheWriteTokens: u.CacheWriteTokens, ReasoningTokens: u.ReasoningTokens,
				SessionCacheHitTokens: e.SessionHit, SessionCacheMissTokens: e.SessionMiss,
			}
			if e.Pricing != nil {
				cost := e.Pricing.Cost(u)
				w.Usage.Cost = cost
				w.Usage.Currency = e.Pricing.Symbol()
				w.Usage.CostUSD = cost
			}
			if e.CacheDiagnostics != nil {
				w.Usage.CacheDiagnostics = toWireCacheDiagnostics(e.CacheDiagnostics)
			}
		}
	case event.ApprovalRequest:
		wa := &wireApproval{ID: e.Approval.ID, Tool: e.Approval.Tool, Subject: e.Approval.Subject, Args: e.Approval.Args}
		if len(e.Approval.Changes) > 0 {
			wa.Changes = make([]wireFileChange, len(e.Approval.Changes))
			for i, ch := range e.Approval.Changes {
				wa.Changes[i] = wireFileChange{Path: ch.Path, Kind: ch.Kind, Added: ch.Added, Removed: ch.Removed, Diff: ch.Diff}
			}
		}
		w.Approval = wa
	case event.AskRequest:
		w.Ask = toWireAsk(e.Ask)
	case event.CompactionStarted, event.CompactionDone:
		w.Compaction = &wireCompaction{
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
		// Strip goal protocol markers ([goal:complete]/[goal:continue]/[goal:blocked])
		// from the live display path so users see natural language. The session
		// history retains them — the controller parses them to drive the goal loop.
		w.Text = agent.StripGoalMarkers(e.Text)
	}
	return w
}

func toWireCacheDiagnostics(d *event.CacheDiagnostics) *wireCacheDiagnostics {
	return &wireCacheDiagnostics{
		PrefixHash:          d.PrefixHash,
		PrefixChanged:       d.PrefixChanged,
		PrefixChangeReasons: append([]string(nil), d.PrefixChangeReasons...),
		SystemHash:          d.SystemHash,
		ToolsHash:           d.ToolsHash,
		LogRewriteVersion:   d.LogRewriteVersion,
		ToolSchemaTokens:    d.ToolSchemaTokens,
		CacheMissTokens:     d.CacheMissTokens,
		CacheHitTokens:      d.CacheHitTokens,
	}
}
