package boot

import (
	"testing"

	"github.com/zzycxz/fairpeer/internal/event"
	"github.com/zzycxz/fairpeer/internal/stats"
)

// Every entry point labels its usage records with a StatsSource tag, and that
// tag is what the usage panel's per-source filter matches. A host that sets no
// tag must keep its sink untouched (no accidental recording from embedded
// probes or tests), and a host that sets one must get the recorder — otherwise
// the panel shows zero for that entry point no matter how much it is used.
func TestStatsSinkInstallsRecorderOnlyWhenLabeled(t *testing.T) {
	isolateConfigHome(t) // the recorder resolves its own stats dir from the home
	discard := event.Discard

	for _, blank := range []string{"", "   ", "\t"} {
		got := statsSink(discard, blank)
		// event.Discard is a func-typed sink, so the result cannot be compared
		// with ==; assert on the type instead (and that nothing was dropped).
		if _, isRecorder := got.(*stats.Recorder); isRecorder {
			t.Errorf("label %q: sink was wrapped in a recorder; unlabeled hosts must not record", blank)
		}
		if got == nil {
			t.Errorf("label %q: sink must pass through unchanged", blank)
		}
	}

	got := statsSink(discard, "desktop")
	if _, ok := got.(*stats.Recorder); !ok {
		t.Fatalf("labeled host got %T, want *stats.Recorder", got)
	}

	// The sink may legitimately be nil (a headless build with no frontend); the
	// recorder tolerates a nil inner sink, so wiring must not panic on it.
	if wrapped := statsSink(nil, "cli"); wrapped == nil {
		t.Fatal("nil inner sink with a label must still install the recorder")
	}
}

// A typed-nil sink is the trap here: the interface is non-nil, so a plain
// `inner != nil` check passes and the panic only shows up when the first event
// is emitted into a nil receiver. Wrapping one must substitute a discard sink
// (which is what event.Sync would otherwise have done for it).
func TestStatsSinkNormalizesTypedNil(t *testing.T) {
	isolateConfigHome(t)
	var typedNil *countingSink // nil pointer in a non-nil interface

	rec, ok := statsSink(typedNil, "desktop").(*stats.Recorder)
	if !ok {
		t.Fatal("labeled typed-nil sink must still produce a recorder")
	}
	// Emitting through the recorder must be a no-op, not a nil dereference.
	rec.Emit(event.Event{Kind: event.Notice, Text: "probe"})
}

// countingSink is a minimal sink whose methods dereference their receiver, so a
// typed-nil passthrough would panic instead of silently doing nothing.
type countingSink struct{ n int }

func (s *countingSink) Emit(event.Event) { s.n++ }
