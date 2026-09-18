// Package present records the presentation event stream — the rich, view-only
// view of a turn (tool dispatches with readOnly/profile/parentId, tool progress
// chunks, results with durationMs/truncated/attachments, notice/phase/compaction
// cards, expert-collab cards) — so a frontend can rebuild the exact transcript a
// user saw even after a tab switch, reload, or crash.
//
// It exists because the durable session store (internal/agent Session.Save) only
// persists provider.Message — the model's view of the conversation. Everything
// that makes the UI informative (how long a tool took, whether its output was
// truncated, what notices fired, what a sub-agent did) is carried by event.Event
// and, before this package, lived only in the frontend's memory. Reloading from
// the durable store rebuilt a degraded transcript with those fields blank.
//
// The sidecar is <session>.present.jsonl: one Record per line, rewritten in full
// on each save (mirroring Session.Save's rewrite strategy) and TRUNCATED on
// compaction to stay consistent with the post-compact provider.Message array — a
// user sees exactly what the model still remembers, nothing the model forgot. It
// never feeds the LLM; provider.Request is built solely from session.Messages.
package present

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/fileutil"
)

// Kind is the string tag of a Record. It mirrors event.Kind's meaningful subset —
// the kinds a frontend renders — as a stable string so the JSONL is self-
// describing and survives event.Kind reordering. Kinds that only drive live
// interaction (ApprovalRequest, AskRequest, TurnStarted/Done) are either omitted
// or recorded as plain markers (a turn boundary marker) since replaying them has
// no interactive effect.
type Kind string

const (
	KindTurnStarted       Kind = "turn_started"
	KindReasoning         Kind = "reasoning"
	KindText              Kind = "text"
	KindMessage           Kind = "message"
	KindToolDispatch      Kind = "tool_dispatch"
	KindToolResult        Kind = "tool_result"
	KindToolProgress      Kind = "tool_progress"
	KindUsage             Kind = "usage"
	KindNotice            Kind = "notice"
	KindPhase             Kind = "phase"
	KindCompactionStarted Kind = "compaction_started"
	KindCompactionDone    Kind = "compaction_done"
	KindRetrying          Kind = "retrying"
	KindSteer             Kind = "steer"
	KindPaused            Kind = "paused"
	KindResumed           Kind = "resumed"
	KindExpertCollab      Kind = "expert_collab"
)

// Attachment mirrors event.Attachment (a tool-produced file) for JSON serialization.
type Attachment struct {
	Path string `json:"path,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// Profile mirrors event.Profile (subagent model/effort).
type Profile struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// Tool captures the presentation fields of an event.Tool. It is wider than what
// survives in provider.Message: ReadOnly, ParentID, DurationMs, Truncated,
// Attachments, Profile, and the streamed Progress chunks are all here. On dispatch
// only ID/Name/Args/ReadOnly/ParentID/Profile are set; on result Output/Err/
// Truncated/DurationMs/Attachments are filled; on progress only ID/Output(chunk).
type Tool struct {
	ID          string       `json:"id,omitempty"`
	Name        string       `json:"name,omitempty"`
	Args        string       `json:"args,omitempty"`
	Output      string       `json:"output,omitempty"`
	Err         string       `json:"err,omitempty"`
	ReadOnly    bool         `json:"readOnly,omitempty"`
	Truncated   bool         `json:"truncated,omitempty"`
	DurationMs  int64        `json:"durationMs,omitempty"`
	ParentID    string       `json:"parentId,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Profile     *Profile     `json:"profile,omitempty"`
	// FileDiff preserves a writer dispatch's server-side preview so a replayed
	// session shows the same diff cards the live one did.
	FileDiff *FileDiff `json:"fileDiff,omitempty"`
}

// FileDiff mirrors event.FileDiff on a Tool record.
type FileDiff struct {
	Diff    string `json:"diff"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// Usage carries the per-turn token telemetry, mirroring the subset of
// provider.Usage a frontend renders.
type Usage struct {
	InputTokens  int `json:"inputTokens,omitempty"`
	OutputTokens int `json:"outputTokens,omitempty"`
	TotalTokens  int `json:"totalTokens,omitempty"`
	CacheRead    int `json:"cacheRead,omitempty"`
}

// Compaction mirrors event.Compaction for the compaction cards.
type Compaction struct {
	Trigger  string `json:"trigger,omitempty"`
	Messages int    `json:"messages,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Archive  string `json:"archive,omitempty"`
}

// Collab mirrors event.Collab (expert-team collaboration) for the collab cards.
// Stored as raw JSON so the package need not depend on the experts shape; the
// frontend already deserializes the same structure from the live stream.
type Collab = json.RawMessage

// Record is one presentation event, persisted as one JSONL line in the sidecar.
// Only the fields meaningful for Kind are populated; the rest are zero/omitted.
type Record struct {
	Kind       Kind        `json:"kind"`
	Text       string      `json:"text,omitempty"`
	Reasoning  string      `json:"reasoning,omitempty"`
	Level      string      `json:"level,omitempty"` // "info"|"warn" for notice
	Tool       *Tool       `json:"tool,omitempty"`
	Usage      *Usage      `json:"usage,omitempty"`
	Compaction *Compaction `json:"compaction,omitempty"`
	Collab     Collab      `json:"collab,omitempty"`
	Retry      *Retry      `json:"retry,omitempty"`
}

// Retry carries Retrying event fields.
type Retry struct {
	Attempt int `json:"attempt"`
	Max     int `json:"max,omitempty"`
}

// FromEvent projects the presentation-relevant fields of an event.Event onto a
// Record. Returns ok=false for kinds that have no presentation value
// (ApprovalRequest, AskRequest, TurnDone) — these drive live interaction and are
// not worth persisting. TurnStarted is recorded as a turn-boundary marker so the
// recorder can truncate by turn on compaction.
func FromEvent(e event.Event) (Record, bool) {
	switch e.Kind {
	case event.TurnStarted:
		return Record{Kind: KindTurnStarted}, true
	case event.Reasoning:
		return Record{Kind: KindReasoning, Text: e.Text}, true
	case event.Text:
		return Record{Kind: KindText, Text: e.Text}, true
	case event.Message:
		return Record{Kind: KindMessage, Text: e.Text, Reasoning: e.Reasoning}, true
	case event.ToolDispatch:
		return Record{Kind: KindToolDispatch, Tool: toolFromEvent(e.Tool)}, true
	case event.ToolResult:
		return Record{Kind: KindToolResult, Tool: toolFromEvent(e.Tool)}, true
	case event.ToolProgress:
		return Record{Kind: KindToolProgress, Tool: &Tool{ID: e.Tool.ID, Output: e.Tool.Output}}, true
	case event.Usage:
		return Record{Kind: KindUsage, Usage: usageFromEvent(e)}, true
	case event.Notice:
		lvl := "info"
		if e.Level == event.LevelWarn {
			lvl = "warn"
		}
		return Record{Kind: KindNotice, Text: e.Text, Level: lvl}, true
	case event.Phase:
		return Record{Kind: KindPhase, Text: e.Text}, true
	case event.CompactionStarted:
		return Record{Kind: KindCompactionStarted, Compaction: &Compaction{Trigger: e.Compaction.Trigger}}, true
	case event.CompactionDone:
		return Record{Kind: KindCompactionDone, Compaction: &Compaction{
			Trigger: e.Compaction.Trigger, Messages: e.Compaction.Messages,
			Summary: e.Compaction.Summary, Archive: e.Compaction.Archive,
		}}, true
	case event.Retrying:
		return Record{Kind: KindRetrying, Retry: &Retry{Attempt: e.RetryAttempt, Max: e.RetryMax}}, true
	case event.Steer:
		return Record{Kind: KindSteer, Text: e.Text}, true
	case event.Paused:
		return Record{Kind: KindPaused}, true
	case event.Resumed:
		return Record{Kind: KindResumed}, true
	case event.ExpertCollab:
		// Marshal the collab payload once, here, so the line is self-contained.
		if raw, err := json.Marshal(e.Collab); err == nil {
			return Record{Kind: KindExpertCollab, Collab: raw}, true
		}
		return Record{Kind: KindExpertCollab}, true
	default:
		// ApprovalRequest, AskRequest, TurnDone, MCPSurfaceReady — no replay value.
		return Record{}, false
	}
}

func toolFromEvent(t event.Tool) *Tool {
	out := &Tool{
		ID: t.ID, Name: t.Name, Args: t.Args, Output: t.Output, Err: t.Err,
		ReadOnly: t.ReadOnly, Truncated: t.Truncated, DurationMs: t.DurationMs,
		ParentID: t.ParentID,
	}
	if t.FileDiff.Diff != "" || t.FileDiff.Added != 0 || t.FileDiff.Removed != 0 {
		out.FileDiff = &FileDiff{Diff: t.FileDiff.Diff, Added: t.FileDiff.Added, Removed: t.FileDiff.Removed}
	}
	if len(t.Attachments) > 0 {
		out.Attachments = make([]Attachment, len(t.Attachments))
		for i, a := range t.Attachments {
			out.Attachments[i] = Attachment{Path: a.Path, Kind: a.Kind}
		}
	}
	if t.Profile != nil {
		out.Profile = &Profile{Model: t.Profile.Model, Effort: t.Profile.Effort}
	}
	return out
}

func usageFromEvent(e event.Event) *Usage {
	if e.Usage == nil {
		return nil
	}
	u := &Usage{
		InputTokens: e.Usage.PromptTokens, OutputTokens: e.Usage.CompletionTokens,
		TotalTokens: e.Usage.TotalTokens,
	}
	if e.Usage.CacheHitTokens > 0 {
		u.CacheRead = e.Usage.CacheHitTokens
	}
	return u
}

// PresentPath returns the sidecar path for a session path:
// "<session>.jsonl" → "<session>.present.jsonl". For a session path that does not
// end in .jsonl it appends ".present.jsonl" anyway, so the sidecar stays a sibling.
func PresentPath(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return sessionPath + ".present.jsonl"
}

// maxRecorderTurns caps how many turns the recorder keeps in memory. Each turn
// averages ~7KB of records, so 100 turns ≈ 700KB — a generous ceiling that keeps
// long sessions bounded without dropping anything a user is likely to scroll
// back to. When exceeded the oldest turns are dropped (their records never reach
// the sidecar either, matching what the user sees: those turns are far off the
// top of the transcript). Compaction resets the counter anyway.
const maxRecorderTurns = 100

// Recorder accumulates the presentation records for one session and writes them
// as a sidecar. It is goroutine-safe. The zero value is a no-op recorder (all
// methods are safe to call); wire it via NewRecorder when a session path is known.
//
// Consistency model: the recorder keeps the full record list in memory and
// rewrites the sidecar on Save (mirroring Session.Save's strategy). On
// compaction the controller calls TruncateToTurn, which drops records from
// completed turns the model has forgotten — so the sidecar and the post-compact
// provider.Message array describe the same turns. RewriteVersion is captured on
// each Save so a stale sidecar (from before a compaction that didn't yet flush)
// can be detected and discarded on load.
type Recorder struct {
	mu sync.Mutex

	records        []Record
	turnStartIndex []int // indices into records where a TurnStarted marker sits
	rewriteVersion int   // session.RewriteVersion() at last Save
	seeded         bool  // SeedFromPath has run; guard against re-seeding

	// pending stores the records accumulated since the last TruncateToTurn/Save;
	// turnStartIndex lets TruncateToTurn find the boundary to keep from. The
	// kept slice always begins at a TurnStarted marker.
}

// NewRecorder returns a ready recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// SeedFromPath loads an existing sidecar into the recorder so a freshly-built
// controller (e.g. after close+reopen) extends the persisted history instead of
// overwriting it. It is meant to be called once, before the first Save, when the
// session path is known. A missing file is a no-op (first-ever turn). If the
// recorder already has records or a rewriteVersion, SeedFromPath is a no-op
// (idempotent against accidental double-seed). Best-effort: a read failure is
// logged via the returned error but does not block recording.
//
// Without this, the new controller's empty recorder would, on the first post-
// reopen turn's Save, full-rewrite the sidecar with only that turn's records —
// silently erasing every prior turn's rich fields and notice/phase/compaction
// cards. The session.jsonl skeleton survives (so text/tools are fine), but the
// presentation layer degrades to "no durations / no cards" for old turns.
func (r *Recorder) SeedFromPath(path string) error {
	if r == nil || path == "" {
		return nil
	}
	r.mu.Lock()
	if len(r.records) > 0 || r.rewriteVersion != 0 || r.seeded {
		// Already has in-memory state (live session) or already seeded — don't
		// clobber. The seeded flag guards the rare case where records is empty
		// but rewriteVersion is also 0 because the seeded file had a zero header.
		r.mu.Unlock()
		return nil
	}
	r.seeded = true
	r.mu.Unlock()
	records, ver, err := Load(path)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.records = records
	r.rewriteVersion = ver
	r.mu.Unlock()
	return nil
}

// Append records one event if it has presentation value. Safe to call on a nil
// recorder (no-op). TurnStarted marks a new turn boundary the retention cap and
// any future turn-aligned truncation can cut against.
func (r *Recorder) Append(e event.Event) {
	if r == nil {
		return
	}
	rec, ok := FromEvent(e)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.Kind == KindTurnStarted {
		r.turnStartIndex = append(r.turnStartIndex, len(r.records))
		// Bounded retention: once we exceed maxRecorderTurns, drop the oldest
		// turn's records so memory stays flat over a long session. The dropped
		// turns are old enough that they're typically off-screen and (in a
		// compacted session) already forgotten by the model.
		if len(r.turnStartIndex) > maxRecorderTurns {
			dropAt := r.turnStartIndex[0]
			r.records = append([]Record(nil), r.records[dropAt:]...)
			shifted := r.turnStartIndex[1:]
			for i := range shifted {
				shifted[i] -= dropAt
			}
			r.turnStartIndex = shifted
		}
	}
	r.records = append(r.records, rec)
}

// Reset drops all in-memory records and version state so the recorder can be
// re-seeded for a different session — the controller calls it when it rebinds
// to another session file (new session, clear, fork/branch switch, resume) and
// the accumulated records describe the OLD conversation. Without this, those
// records would leak into the new session's sidecar on the next Save, and an
// in-process reader (PresentRecords) would serve the old conversation's cards
// for the new session.
func (r *Recorder) Reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.records = nil
	r.turnStartIndex = nil
	r.rewriteVersion = 0
	r.seeded = false
	r.mu.Unlock()
}

// SetRewriteVersion records the session's current RewriteVersion, captured at
// Save so a stale sidecar can be detected on load. Called by the controller
// alongside Save.
func (r *Recorder) SetRewriteVersion(v int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.rewriteVersion = v
	r.mu.Unlock()
}

// SyncBeforeSave reconciles the recorder with the session right before a Save.
// If the session's RewriteVersion advanced since the last sync, the recorder is
// reset: a rewrite means compaction/prune/softTrim/summarize physically replaced
// Messages, so the sidecar must not keep records describing what the model has
// forgotten. On a compaction specifically, the CompactionDone card is re-seeded
// (if observed) so the user still sees "N messages compacted"; a prune/softTrim
// (which only emits Notice, no CompactionDone) clears the records outright —
// acceptable, since those rewrites are silent in the live stream too.
//
// This must trigger on ANY RewriteVersion change, not just CompactionDone,
// because prune.go's SoftTrim/Prune bump the version and rewrite Messages
// without emitting a Compaction event — gating on lastDone.Kind would let those
// rewrites leave stale records in the sidecar.
func (r *Recorder) SyncBeforeSave(currentVersion int, lastDone event.Event) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.rewriteVersion != 0 && currentVersion != r.rewriteVersion {
		// A rewrite happened since the last save. Reset; if the most-recent
		// rewrite was a compaction (lastDone observed), keep its card so the
		// user sees a compaction happened. Otherwise (prune/softTrim/summarize)
		// clear entirely.
		r.records = nil
		r.turnStartIndex = nil
		if lastDone.Kind == event.CompactionDone && lastDone.Compaction.Summary != "" {
			if rec, ok := FromEvent(lastDone); ok {
				r.records = append(r.records, rec)
			}
		}
	}
	r.rewriteVersion = currentVersion
	r.mu.Unlock()
}

// lastCompactionEvent is held by the controller (it sees the live stream) and
// passed to SyncBeforeSave; this package does not retain events.

// lastCompactionEvent is held by the controller (it sees the live stream) and
// passed to SyncBeforeSave; this package does not retain events.// Records returns a snapshot of the current records (for in-process consumers
// like PresentForTab that can read from memory without hitting disk).
func (r *Recorder) Records() []Record {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, len(r.records))
	copy(out, r.records)
	return out
}

// RewriteVersion returns the rewrite version the in-memory records are aligned
// to (0 = never saved or seeded). Pair with Records for in-process readers that
// need to detect a stale stream after a compaction, mirroring the sidecar's
// header line.
func (r *Recorder) RewriteVersion() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rewriteVersion
}

// Save writes the current records to path as JSONL (one Record per line), using
// the tmp-file-then-rename pattern from Session.Save so a crash mid-write can't
// leave a partial sidecar. An empty path is a no-op. The rewriteVersion is written
// as a header line (Kind="_header") so Load can detect a stale sidecar.
func (r *Recorder) Save(path string) error {
	if r == nil || path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create present dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".present.*.tmp")
	if err != nil {
		return fmt.Errorf("create present tmp: %w", err)
	}
	tmpPath := tmp.Name()
	enc := json.NewEncoder(tmp)
	// Header carries the rewriteVersion this sidecar was aligned to. On load, if
	// the session's current RewriteVersion differs AND compaction has happened
	// since, the loader should treat the sidecar as stale.
	header := recordWrapper{Record: Record{Kind: "_header"}, RewriteVersion: r.rewriteVersion}
	if err := enc.Encode(header); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("encode present header: %w", err)
	}
	r.mu.Lock()
	snap := make([]Record, len(r.records))
	copy(snap, r.records)
	r.mu.Unlock()
	for _, rec := range snap {
		if err := enc.Encode(recordWrapper{Record: rec}); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("encode present record: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return fileutil.ReplaceFile(tmpPath, path)
}

// recordWrapper is the on-disk line shape. The header line uses Kind="_header"
// and carries RewriteVersion; all other lines leave RewriteVersion at zero.
type recordWrapper struct {
	Record
	RewriteVersion int `json:"rewriteVersion,omitempty"`
}

// Load reads a sidecar produced by Save, returning the records and the
// rewriteVersion they were aligned to. A missing file returns (nil, 0, nil) —
// callers treat that as "no sidecar, fall back to degraded history".
func Load(path string) ([]Record, int, error) {
	if path == "" {
		return nil, 0, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()
	var out []Record
	var rewriteVersion int
	dec := json.NewDecoder(f)
	for dec.More() {
		var rw recordWrapper
		if err := dec.Decode(&rw); err != nil {
			// A truncated last line (crash mid-write before rename) shouldn't fail
			// the whole load — return what decoded cleanly. The atomic rename means
			// this is rare, but tolerating it is strictly safer.
			break
		}
		if rw.Kind == "_header" {
			rewriteVersion = rw.RewriteVersion
			continue
		}
		out = append(out, rw.Record)
	}
	return out, rewriteVersion, nil
}
