package present

import (
	"path/filepath"
	"testing"

	"github.com/zzycxz/hiq/internal/event"
)

func TestFromEventProjectsFields(t *testing.T) {
	cases := []struct {
		name string
		e    event.Event
		want Kind
	}{
		{"turn_started", event.Event{Kind: event.TurnStarted}, KindTurnStarted},
		{"reasoning", event.Event{Kind: event.Reasoning, Text: "thinking..."}, KindReasoning},
		{"notice warn", event.Event{Kind: event.Notice, Text: "careful", Level: event.LevelWarn}, KindNotice},
		{"tool dispatch", event.Event{Kind: event.ToolDispatch, Tool: event.Tool{ID: "c1", Name: "bash", ReadOnly: false}}, KindToolDispatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, ok := FromEvent(c.e)
			if !ok {
				t.Fatalf("FromEvent returned ok=false for %s", c.name)
			}
			if rec.Kind != c.want {
				t.Errorf("kind = %q, want %q", rec.Kind, c.want)
			}
		})
	}
}

func TestFromEventSkipsNonPresentable(t *testing.T) {
	// ApprovalRequest / AskRequest / TurnDone have no replay value.
	for _, k := range []event.Kind{event.ApprovalRequest, event.AskRequest, event.TurnDone} {
		if _, ok := FromEvent(event.Event{Kind: k}); ok {
			t.Errorf("kind %d should be skipped (not presentable)", k)
		}
	}
}

func TestFromEventToolCarriesRichFields(t *testing.T) {
	// The whole point of the sidecar is that these fields — lost in
	// provider.Message — survive in the record.
	e := event.Event{Kind: event.ToolResult, Tool: event.Tool{
		ID: "c1", Name: "bash", Output: "done", ReadOnly: false,
		DurationMs: 1234, Truncated: true, ParentID: "parent1",
		Attachments: []event.Attachment{{Path: "x.png", Kind: "image"}},
		Profile:     &event.Profile{Model: "glm-5", Effort: "high"},
	}}
	rec, ok := FromEvent(e)
	if !ok {
		t.Fatal("FromEvent returned ok=false")
	}
	if rec.Tool == nil {
		t.Fatal("Tool is nil")
	}
	if rec.Tool.DurationMs != 1234 {
		t.Errorf("DurationMs = %d, want 1234", rec.Tool.DurationMs)
	}
	if !rec.Tool.Truncated {
		t.Error("Truncated lost")
	}
	if rec.Tool.ParentID != "parent1" {
		t.Errorf("ParentID = %q, want parent1", rec.Tool.ParentID)
	}
	if len(rec.Tool.Attachments) != 1 || rec.Tool.Attachments[0].Path != "x.png" {
		t.Errorf("Attachments lost: %+v", rec.Tool.Attachments)
	}
	if rec.Tool.Profile == nil || rec.Tool.Profile.Model != "glm-5" {
		t.Errorf("Profile lost: %+v", rec.Tool.Profile)
	}
}

func TestRecorderAppendAndTurnBoundaries(t *testing.T) {
	r := NewRecorder()
	// Turn 1: started + one tool
	r.Append(event.Event{Kind: event.TurnStarted})
	r.Append(event.Event{Kind: event.ToolDispatch, Tool: event.Tool{ID: "a"}})
	// Turn 2: started + one tool
	r.Append(event.Event{Kind: event.TurnStarted})
	r.Append(event.Event{Kind: event.ToolDispatch, Tool: event.Tool{ID: "b"}})

	recs := r.Records()
	// 2 turn markers + 2 tool dispatches = 4 records
	if len(recs) != 4 {
		t.Fatalf("len(records) = %d, want 4", len(recs))
	}
	if len(r.turnStartIndex) != 2 {
		t.Errorf("turnStartIndex len = %d, want 2", len(r.turnStartIndex))
	}
}

func TestRecorderSyncBeforeSaveReseedsOnCompaction(t *testing.T) {
	r := NewRecorder()
	// Three turns of records accumulate.
	for turn := 0; turn < 3; turn++ {
		r.Append(event.Event{Kind: event.TurnStarted})
		r.Append(event.Event{Kind: event.ToolDispatch, Tool: event.Tool{ID: string(rune('a' + turn))}})
	}
	// First save: version 1, no compaction yet — records preserved.
	r.SyncBeforeSave(1, event.Event{})
	if len(r.Records()) != 6 {
		t.Fatalf("after first sync len = %d, want 6 (no compaction)", len(r.Records()))
	}
	// A compaction happens: CompactionDone observed, version bumps to 2.
	done := event.Event{Kind: event.CompactionDone, Compaction: event.Compaction{Trigger: "auto", Messages: 4, Summary: "folded earlier"}}
	r.SyncBeforeSave(2, done)
	recs := r.Records()
	// After re-seed: only the compaction_done record survives.
	if len(recs) != 1 {
		t.Fatalf("after compaction sync len = %d, want 1 (compaction card only)", len(recs))
	}
	if recs[0].Kind != KindCompactionDone {
		t.Errorf("kept record kind = %q, want compaction_done", recs[0].Kind)
	}
}

func TestRecorderSyncBeforeSaveNoOpWithoutVersionChange(t *testing.T) {
	r := NewRecorder()
	r.Append(event.Event{Kind: event.TurnStarted})
	r.Append(event.Event{Kind: event.ToolDispatch, Tool: event.Tool{ID: "a"}})
	r.SyncBeforeSave(5, event.Event{})
	r.SyncBeforeSave(5, event.Event{}) // same version — no reset
	if len(r.Records()) != 2 {
		t.Errorf("same-version sync reset records: len = %d, want 2", len(r.Records()))
	}
}

func TestRecorderSyncBeforeSaveIgnoresCompactionWithoutVersionChange(t *testing.T) {
	// A CompactionDone seen but version unchanged (e.g. abort path) must NOT reset.
	r := NewRecorder()
	r.Append(event.Event{Kind: event.TurnStarted})
	r.SyncBeforeSave(5, event.Event{})
	done := event.Event{Kind: event.CompactionDone, Compaction: event.Compaction{Summary: "x"}}
	r.SyncBeforeSave(5, done) // same version — even with done, no reset
	if len(r.Records()) != 1 {
		t.Errorf("same-version sync with done reset records: len = %d, want 1", len(r.Records()))
	}
}

func TestRecorderSyncBeforeSaveClearsOnRewriteWithoutCompaction(t *testing.T) {
	// prune.go's SoftTrim/Prune bump RewriteVersion and rewrite Messages but
	// emit only a Notice — no CompactionDone. The sync must STILL reset (gated
	// on the version change, not on lastDone), or the sidecar keeps records
	// describing messages the model no longer has. With no CompactionDone the
	// recorder clears outright (no compaction card to keep).
	r := NewRecorder()
	r.Append(event.Event{Kind: event.TurnStarted})
	r.Append(event.Event{Kind: event.ToolDispatch, Tool: event.Tool{ID: "a"}})
	r.Append(event.Event{Kind: event.Notice, Text: "before prune"})
	r.SyncBeforeSave(1, event.Event{}) // establish baseline version
	if len(r.Records()) != 3 {
		t.Fatalf("baseline len = %d, want 3", len(r.Records()))
	}
	// A prune happens: version bumps to 2, but NO CompactionDone observed
	// (lastDone stays the zero event — exactly what prune.go produces).
	r.SyncBeforeSave(2, event.Event{})
	recs := r.Records()
	if len(recs) != 0 {
		t.Errorf("after prune-style rewrite len = %d, want 0 (cleared, no card to keep)", len(recs))
	}
}

func TestRecorderNilIsNoOp(t *testing.T) {
	// A nil recorder must never panic — callers invoke Append/Save without nil checks.
	var r *Recorder
	r.Append(event.Event{Kind: event.TurnStarted})
	r.SetRewriteVersion(3)
	r.SyncBeforeSave(4, event.Event{})
	if err := r.Save(""); err != nil {
		t.Errorf("Save(\"\") = %v, want nil", err)
	}
	if recs := r.Records(); recs != nil {
		t.Errorf("Records() = %v, want nil", recs)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.present.jsonl")
	r := NewRecorder()
	r.Append(event.Event{Kind: event.TurnStarted})
	r.Append(event.Event{Kind: event.ToolDispatch, Tool: event.Tool{ID: "c1", Name: "bash"}})
	r.Append(event.Event{Kind: event.Notice, Text: "hello", Level: event.LevelInfo})
	r.SyncBeforeSave(42, event.Event{})

	if err := r.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, ver, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if ver != 42 {
		t.Errorf("rewriteVersion = %d, want 42", ver)
	}
	// turn_started + tool_dispatch + notice = 3 (header excluded from count).
	if len(loaded) != 3 {
		t.Fatalf("loaded len = %d, want 3", len(loaded))
	}
	if loaded[0].Kind != KindTurnStarted {
		t.Errorf("loaded[0].Kind = %q, want turn_started", loaded[0].Kind)
	}
	if loaded[2].Kind != KindNotice || loaded[2].Text != "hello" {
		t.Errorf("loaded[2] = %+v, want notice/hello", loaded[2])
	}
}

func TestLoadMissingFileReturnsNil(t *testing.T) {
	loaded, ver, err := Load(filepath.Join(t.TempDir(), "nope.present.jsonl"))
	if err != nil {
		t.Fatalf("Load missing file should not error: %v", err)
	}
	if loaded != nil {
		t.Errorf("loaded = %v, want nil", loaded)
	}
	if ver != 0 {
		t.Errorf("ver = %d, want 0", ver)
	}
}

func TestPresentPath(t *testing.T) {
	got := PresentPath("/x/y/session.jsonl")
	want := "/x/y/session.jsonl.present.jsonl"
	if got != want {
		t.Errorf("PresentPath = %q, want %q", got, want)
	}
	if PresentPath("") != "" {
		t.Error("PresentPath(\"\") should be empty")
	}
}

func TestSeedFromPathPreventsOverwrite(t *testing.T) {
	// Regression: a freshly-built controller (after close+reopen) has an empty
	// recorder. Without seeding, its first Save would rewrite the sidecar with
	// only the new turn — erasing prior turns. SeedFromPath must load the
	// existing records so the next Save extends rather than overwrites.
	dir := t.TempDir()
	path := filepath.Join(dir, "s.present.jsonl")

	// Simulate a prior session's sidecar: two records, version 7.
	prior := NewRecorder()
	prior.Append(event.Event{Kind: event.TurnStarted})
	prior.Append(event.Event{Kind: event.Notice, Text: "old turn"})
	prior.SyncBeforeSave(7, event.Event{})
	if err := prior.Save(path); err != nil {
		t.Fatalf("prior Save: %v", err)
	}

	// New controller, new recorder — seed from the existing sidecar.
	fresh := NewRecorder()
	if err := fresh.SeedFromPath(path); err != nil {
		t.Fatalf("SeedFromPath: %v", err)
	}
	loaded := fresh.Records()
	if len(loaded) != 2 {
		t.Fatalf("after seed len = %d, want 2 (prior records)", len(loaded))
	}
	// A new turn arrives.
	fresh.Append(event.Event{Kind: event.TurnStarted})
	fresh.Append(event.Event{Kind: event.Notice, Text: "new turn"})
	// Save — must contain BOTH old and new, not just new.
	fresh.SyncBeforeSave(7, event.Event{}) // same version — no reset
	if err := fresh.Save(path); err != nil {
		t.Fatalf("fresh Save: %v", err)
	}
	reloaded, _, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	// 2 old + 2 new = 4. Without seed this would be just 2 (the overwrite bug).
	if len(reloaded) != 4 {
		t.Errorf("reloaded len = %d, want 4 (seeded + new turn)", len(reloaded))
	}
}

func TestSeedFromPathMissingFileIsNoOp(t *testing.T) {
	r := NewRecorder()
	if err := r.SeedFromPath(filepath.Join(t.TempDir(), "nope.present.jsonl")); err != nil {
		t.Errorf("SeedFromPath missing file: %v", err)
	}
	if len(r.Records()) != 0 {
		t.Error("missing file should leave recorder empty")
	}
}

func TestSeedFromPathIdempotent(t *testing.T) {
	// Seeding twice must not double-load or clobber live state.
	dir := t.TempDir()
	path := filepath.Join(dir, "s.present.jsonl")
	prior := NewRecorder()
	prior.Append(event.Event{Kind: event.Notice, Text: "old"})
	prior.SyncBeforeSave(5, event.Event{})
	_ = prior.Save(path)

	r := NewRecorder()
	_ = r.SeedFromPath(path)
	r.Append(event.Event{Kind: event.Notice, Text: "live"}) // live state after first seed
	_ = r.SeedFromPath(path)                                 // second seed must NOT clobber
	recs := r.Records()
	// old (seeded) + live, not old + old (re-seed) or just live.
	if len(recs) != 2 {
		t.Errorf("after double-seed len = %d, want 2", len(recs))
	}
}
