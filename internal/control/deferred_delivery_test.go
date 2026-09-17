package control

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/fairpeer/internal/deferred"
)

// Push delivery's "context only" mode: the result must join the next outgoing
// turn (so the model sees the real output) without spending a turn of its own,
// and it must be drained exactly once.
func TestDeliverDeferredRidesNextTurn(t *testing.T) {
	sess := filepath.Join(t.TempDir(), "s1.jsonl")
	c := New(Options{SessionPath: sess})

	result := "<deferred-result source=\"job\" id=\"job:bash-1\" status=\"failed\">\nbuild broke\n</deferred-result>"
	if err := c.DeliverDeferred(sess, result, false); err != nil {
		t.Fatalf("DeliverDeferred: %v", err)
	}

	got := c.Compose("user question")
	if !strings.Contains(got, "<deferred-results>") {
		t.Fatalf("first turn should carry the deferred block, got %q", got)
	}
	if !strings.Contains(got, "build broke") {
		t.Fatalf("deferred block should carry the result text, got %q", got)
	}
	if !strings.HasSuffix(got, "user question") {
		t.Fatalf("deferred block must precede the user text: %q", got)
	}

	if again := c.Compose("another question"); strings.Contains(again, "build broke") {
		t.Fatalf("the block must be drained after one turn: %q", again)
	}
}

// A result bound to another session (or to none) must never be pushed into this
// one — it stays pending for the session that owns it.
func TestDeliverDeferredRejectsForeignSession(t *testing.T) {
	c := New(Options{SessionPath: filepath.Join(t.TempDir(), "mine.jsonl")})

	for _, other := range []string{"", filepath.Join(t.TempDir(), "theirs.jsonl")} {
		err := c.DeliverDeferred(other, "payload", false)
		if !errors.Is(err, deferred.ErrSessionInactive) {
			t.Fatalf("DeliverDeferred(%q) err = %v, want ErrSessionInactive", other, err)
		}
	}
	if got := c.Compose("plain"); got != "plain" {
		t.Fatalf("nothing should have been buffered, got %q", got)
	}
}

func TestDeferredSessionRunnableTracksBindingAndClose(t *testing.T) {
	sess := filepath.Join(t.TempDir(), "s1.jsonl")
	c := New(Options{SessionPath: sess})

	if !c.DeferredSessionRunnable(sess) {
		t.Fatalf("the bound session should be runnable")
	}
	if c.DeferredSessionRunnable("") {
		t.Fatalf("an empty session path is never runnable")
	}

	// Close flips the flag so a result finishing during teardown is left for the
	// next process instead of being pushed into a dying session.
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	if c.DeferredSessionRunnable(sess) {
		t.Fatalf("a closed controller must refuse delivery")
	}
}

func TestNotifyDeferredEmitsNotice(t *testing.T) {
	sess := filepath.Join(t.TempDir(), "s1.jsonl")
	c := New(Options{SessionPath: sess})

	// A nil-text notice must not panic and must not be swallowed into a turn.
	if err := c.NotifyDeferred(sess, "background bash bash-3 failed: exit status 1", "line one\nline two"); err != nil {
		t.Fatalf("NotifyDeferred: %v", err)
	}
	if got := c.Compose("x"); strings.Contains(got, "line one") {
		t.Fatalf("a notice must not enter the turn context: %q", got)
	}
}
