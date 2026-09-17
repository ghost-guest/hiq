package jobs

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// hookRecorder collects completions handed to the hook.
type hookRecorder struct {
	mu   sync.Mutex
	got  []Completion
	seen chan struct{}
}

func newHookRecorder() *hookRecorder {
	return &hookRecorder{seen: make(chan struct{}, 8)}
}

func (h *hookRecorder) fn(c Completion) {
	h.mu.Lock()
	h.got = append(h.got, c)
	h.mu.Unlock()
	select {
	case h.seen <- struct{}{}:
	default:
	}
}

func (h *hookRecorder) waitFor(t *testing.T, n int) []Completion {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		h.mu.Lock()
		got := append([]Completion(nil), h.got...)
		h.mu.Unlock()
		if len(got) >= n {
			return got
		}
		select {
		case <-h.seen:
		case <-deadline:
			t.Fatalf("timed out waiting for %d completion(s), got %d", n, len(got))
		}
	}
}

// The hook is the push path's entry point: a finished job must arrive with its
// actual output, and the pull summary must stay empty so the result is never
// reported twice.
func TestCompletionHookReceivesOutputAndSuppressesDrainNote(t *testing.T) {
	m := NewManager(nil)
	defer m.Close()
	rec := newHookRecorder()
	m.SetCompletionHook(rec.fn)

	m.Start("bash", "build", func(_ context.Context, out io.Writer) (string, error) {
		io.WriteString(out, "compiling\n")
		io.WriteString(out, "done\n")
		return "", nil
	})

	got := rec.waitFor(t, 1)[0]
	if got.Status != Done {
		t.Errorf("status = %q, want %q", got.Status, Done)
	}
	if got.Kind != "bash" || got.Label != "build" {
		t.Errorf("kind/label = %q/%q, want bash/build", got.Kind, got.Label)
	}
	if !strings.Contains(got.Output, "compiling") || !strings.Contains(got.Output, "done") {
		t.Errorf("completion should carry the job output, got %q", got.Output)
	}
	if got.At.IsZero() {
		t.Errorf("completion timestamp missing")
	}
	if note := m.DrainCompletedNote(); note != "" {
		t.Errorf("drain note must stay empty when a hook owns reporting, got %q", note)
	}
}

func TestCompletionHookReportsFailures(t *testing.T) {
	m := NewManager(nil)
	defer m.Close()
	rec := newHookRecorder()
	m.SetCompletionHook(rec.fn)

	m.Start("task", "summarize", func(context.Context, io.Writer) (string, error) {
		return "", errors.New("model returned 429")
	})

	got := rec.waitFor(t, 1)[0]
	if got.Status != Failed {
		t.Fatalf("status = %q, want %q", got.Status, Failed)
	}
	if got.Err == nil || !strings.Contains(got.Err.Error(), "429") {
		t.Errorf("error should be reported, got %v", got.Err)
	}
	if !strings.Contains(got.Output, "429") {
		t.Errorf("output should fall back to the error text, got %q", got.Output)
	}
}

func TestCompletionHookSeesTaskJobAnswer(t *testing.T) {
	m := NewManager(nil)
	defer m.Close()
	rec := newHookRecorder()
	m.SetCompletionHook(rec.fn)

	m.Start("task", "answer", func(context.Context, io.Writer) (string, error) {
		return "the answer is 42", nil
	})

	got := rec.waitFor(t, 1)[0]
	if got.Output != "the answer is 42" {
		t.Fatalf("task answer should be the completion output, got %q", got.Output)
	}
}

// Without a hook the legacy pull behavior must be untouched.
func TestDrainNoteStillWorksWithoutHook(t *testing.T) {
	m := NewManager(nil)
	defer m.Close()

	m.Start("bash", "noop", func(context.Context, io.Writer) (string, error) { return "", nil })
	// Wait for the terminal state so the note is queued before we drain.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if note := m.DrainCompletedNote(); note != "" {
			if !strings.Contains(note, "bash-1") || !strings.Contains(note, string(Done)) {
				t.Fatalf("drain note should name the job and its status, got %q", note)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("drain note never appeared without a hook")
}

// A killed job surface as a kill, not a failure — the deferred layer maps it to
// an abort rather than an error.
func TestCompletionHookReportsKilled(t *testing.T) {
	m := NewManager(nil)
	defer m.Close()
	rec := newHookRecorder()
	m.SetCompletionHook(rec.fn)

	release := make(chan struct{})
	j := m.Start("bash", "server", func(ctx context.Context, _ io.Writer) (string, error) {
		select {
		case <-release:
			return "", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	if !m.Kill(j.ID) {
		t.Fatalf("kill should report a running job")
	}
	close(release)

	got := rec.waitFor(t, 1)[0]
	if got.Status != Killed {
		t.Fatalf("status = %q, want %q", got.Status, Killed)
	}
}
