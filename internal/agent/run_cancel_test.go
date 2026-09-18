package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/tool"
)

// countingProvider returns a tool-call on the first Stream, then would return a
// final answer on the second. It counts Stream calls so the test can assert the
// loop exited before the second stream when ctx was cancelled between the tool
// call and the next iteration.
type countingProvider struct {
	calls atomic.Int32
}

func (c *countingProvider) Name() string { return "counting" }

func (c *countingProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	n := c.calls.Add(1)
	ch := make(chan provider.Chunk, 4)
	if n == 1 {
		// First stream: emit a tool call. The tool runs fast; then the loop
		// re-enters the top where (after the fix) it checks ctx.Err().
		ch <- provider.Chunk{
			Type: provider.ChunkText,
			Text: "I'll use the echo tool.",
		}
		ch <- provider.Chunk{
			Type:     provider.ChunkToolCall,
			ToolCall: &provider.ToolCall{ID: "t1", Name: "echo", Arguments: "{}"},
		}
		ch <- provider.Chunk{Type: provider.ChunkDone}
		close(ch)
		return ch, nil
	}
	// Second+ stream: this must NOT be reached if ctx was cancelled between
	// the tool call and the next iteration (the fix's ctx.Err() gate stops it).
	// If we DO get here, block until ctx is cancelled so the test still ends.
	select {
	case <-ctx.Done():
		close(ch)
		return ch, ctx.Err()
	case <-time.After(10 * time.Second):
		close(ch)
		return ch, errors.New("stream timed out waiting for cancel")
	}
}

// TestRunLoopExitsOnCancelBetweenSteps proves the agent loop checks ctx.Err()
// at the top of each iteration (between a completed tool call and the next
// stream). Without that check, a Stop click during the gap between a tool
// returning and the next stream starting would still call Stream once more,
// delaying the TurnDone. With the check, Run returns promptly after cancel.
func TestRunLoopExitsOnCancelBetweenSteps(t *testing.T) {
	prov := &countingProvider{}
	reg := tool.NewRegistry()
	// Register the tool the provider emits (name must match the ToolCall.Name).
	// A long delay keeps the tool in flight when we cancel, so cancellation
	// lands DURING the tool call; when it returns, the loop re-enters the top
	// where the ctx.Err() gate must fire — proving Stop exits between steps
	// instead of starting another LLM stream.
	reg.Add(fakeTool{name: "echo", readOnly: true, delay: 800 * time.Millisecond})
	sess := NewSession("sys")
	a := New(prov, reg, sess, Options{}, event.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel while the tool is still in its 800ms delay (so ctx is already
	// cancelled by the time the loop reaches the top of iteration 2).
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := a.Run(ctx, "use the echo tool")
	elapsed := time.Since(start)

	// The fix: the loop saw ctx.Err() at the top of iteration 2 and returned,
	// instead of calling Stream a second time. Exactly one Stream call means
	// the gate worked.
	if got := prov.calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 Stream call (loop should exit before the 2nd), got %d", got)
	}
	// Run returns the ctx error (runGuarded maps ctx.Err()!=nil to a clean end
	// for the UI, but Run itself surfaces the error here).
	if err == nil {
		t.Error("expected Run to return a non-nil error after cancel, got nil")
	}
	// Must return well before the 10s safety net in the 2nd-stream branch.
	if elapsed > 5*time.Second {
		t.Errorf("Run took %v to return after cancel — the ctx gate did not fire promptly", elapsed)
	}
	// The first iteration (stream + tool) had to actually run before cancel
	// landed at 300ms; a sub-200ms elapsed would mean nothing executed. The tool
	// honors ctx, so it returns right when cancelled (~300ms), not at 800ms.
	if elapsed < 200*time.Millisecond {
		t.Errorf("Run returned too fast (%v) — the first iteration didn't actually run", elapsed)
	}
	t.Logf("Run exited %v after cancel, Stream calls=%d, err=%v", elapsed, prov.calls.Load(), err)
}
