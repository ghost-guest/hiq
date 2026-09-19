package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/tool"
)

// TestStandbySoftTrimBoundsNoWindowSessions covers the no-context-window path:
// nothing ever compacts, so the standby trim must head+tail-trim old oversized
// tool outputs — keeping the message count (checkpoint indexes depend on it)
// and the recent tail verbatim.
func TestStandbySoftTrimBoundsNoWindowSessions(t *testing.T) {
	sess := pruneFixture(strings.Repeat("x", 20000))
	a := New(nil, tool.NewRegistry(), sess, Options{RecentKeep: 2}, event.Discard) // no ContextWindow

	// Before the rate-limit window elapses, nothing is touched.
	a.standbySoftTrim()
	if got := provider.ContentString(sess.Snapshot()[3].Content); len(got) != 20000 {
		t.Fatalf("tool output trimmed before standby interval elapsed: len=%d", len(got))
	}

	// Advance past the rate limit; the trim then rewrites the old output.
	for i := 0; i < standbyTrimEvery; i++ {
		a.standbySoftTrim()
	}
	msgs := sess.Snapshot()
	if len(msgs) != 7 {
		t.Fatalf("message count changed: %d", len(msgs))
	}
	got := provider.ContentString(msgs[3].Content)
	if len(got) >= 20000 || !strings.Contains(got, "[... trimmed") {
		t.Fatalf("old tool output not soft-trimmed: len=%d head=%.40q", len(got), got)
	}
	// Recent tail stays verbatim.
	if got := provider.ContentString(msgs[6].Content); got != "ok" {
		t.Errorf("recent tail rewritten: %q", got)
	}
	// Small recent outputs are below SoftTrimThreshold and must be untouched.
	if got := provider.ContentString(msgs[5].Content); got != "next" {
		t.Errorf("recent user turn rewritten: %q", got)
	}
	// Repeat: the latch keeps the one-shot notice from re-firing (Discard sink;
	// this only proves the repeated path is stable).
	a.standbySoftTrim()
}

// TestMaybeCompactNoWindowRunsStandbyTrim pins the routing: with no window and
// no usage, maybeCompact must still drive the memory-only standby trim.
func TestMaybeCompactNoWindowRunsStandbyTrim(t *testing.T) {
	sess := pruneFixture(strings.Repeat("x", 20000))
	notices := 0
	a := New(nil, tool.NewRegistry(), sess, Options{RecentKeep: 2}, event.FuncSink(func(e event.Event) {
		if e.Kind == event.Notice {
			notices++
		}
	}))
	for i := 0; i < standbyTrimEvery+1; i++ {
		a.maybeCompact(context.Background(), nil)
	}
	got := provider.ContentString(sess.Snapshot()[3].Content)
	if len(got) >= 20000 || !strings.Contains(got, "[... trimmed") {
		t.Fatalf("standby trim never ran: len=%d", len(got))
	}
	if notices != 1 {
		t.Errorf("notice fired %d times, want exactly 1", notices)
	}
}
