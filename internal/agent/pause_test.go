package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/agent/testutil"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/tool"
)

// This file tests the graceful pause/resume mechanism (coWork Harness module 2).
// Pause freezes the agent between steps with full state preserved; Resume
// continues from the break point. The design constraint under test: a Pause must
// never interrupt an in-flight LLM call — it only gates entry to the next step.

// pauseTestAgent bundles an Agent wired to a mock provider + a bash stub whose
// first Execute calls Pause on the agent (so the pause is guaranteed to land
// before the loop's awaitPause at the top of step 2).
type pauseTestAgent struct {
	*Agent
	sink *recordSink
	pt   *autoPausingBash
}

func newPauseTestAgent(t *testing.T) *pauseTestAgent {
	t.Helper()
	pt := newAutoPausingBash()
	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "t0", Name: "bash", Arguments: `{"command":"first"}`}}},
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "t1", Name: "bash", Arguments: `{"command":"second"}`}}},
		testutil.Turn{Text: "done"},
	)
	sink := &recordSink{}
	reg := tool.NewRegistry()
	reg.Add(pt)
	a := New(mp, reg, NewSession("sys"), Options{}, sink)
	pt.agent = a // wire so Execute can call Pause on the right agent
	return &pauseTestAgent{Agent: a, sink: sink, pt: pt}
}

// TestPauseFreezesBetweenStepsAndResumeContinues is the core safety test: after
// step 1's tool pauses the agent, the loop must block before step 2, and Resume
// must let it finish with both steps run.
func TestPauseFreezesBetweenStepsAndResumeContinues(t *testing.T) {
	pa := newPauseTestAgent(t)
	runDone := make(chan error, 1)
	go func() { runDone <- pa.Run(context.Background(), "do the work") }()

	// Wait for step 1 to execute (it pauses the agent from within Execute).
	pa.pt.waitForExecs(1, 5*time.Second)

	// The agent should now be paused. Poll IsPaused (awaitPause sets it under
	// the lock, but there's a brief window before the loop reaches it).
	deadline := time.Now().Add(2 * time.Second)
	for !pa.IsPaused() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !pa.IsPaused() {
		t.Fatal("agent should be paused between steps after the tool paused it")
	}

	// While paused, the turn must NOT finish and only step 1 ran.
	select {
	case <-runDone:
		t.Fatal("turn finished while paused — pause failed to block the loop")
	default:
	}
	if pa.pt.count() != 1 {
		t.Fatalf("expected 1 step before pause, got %d", pa.pt.count())
	}
	if len(pa.sink.kinds(event.Paused)) == 0 {
		t.Fatal("expected a Paused event when the agent blocks")
	}

	// Resume — the turn should complete with both steps run.
	pa.Resume()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("turn failed after resume: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not finish after Resume()")
	}
	if pa.pt.count() < 2 {
		t.Fatalf("expected both steps to run after resume, got %d", pa.pt.count())
	}
	if len(pa.sink.kinds(event.Resumed)) == 0 {
		t.Fatal("expected a Resumed event after Resume()")
	}
}

// TestPauseNoOpWhenNotRunning confirms Pause/Resume are safe when no turn is
// active and don't leave the agent in a state that blocks the next turn.
func TestPauseNoOpWhenNotRunning(t *testing.T) {
	mp := testutil.NewMock("m", testutil.Turn{Text: "hi"})
	sink := &recordSink{}
	reg := tool.NewRegistry()
	a := New(mp, reg, NewSession("sys"), Options{}, sink)

	a.Pause()
	a.Resume()
	if a.IsPaused() {
		t.Fatal("IsPaused should be false when no run is active")
	}
	if err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run after stray pause/resume failed: %v", err)
	}
}

// TestCancelWhilePausedUnblocks confirms cancelling the context (the Stop/Esc
// path) unblocks a paused agent — the only exit besides Resume. A user who
// pauses then hits Stop must not hang.
func TestCancelWhilePausedUnblocks(t *testing.T) {
	pa := newPauseTestAgent(t)
	// Trim the provider to one tool call + final answer for this scenario.
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- pa.Run(ctx, "work") }()

	pa.pt.waitForExecs(1, 5*time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for !pa.IsPaused() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !pa.IsPaused() {
		t.Fatal("agent should be paused")
	}

	cancel()
	select {
	case err := <-runDone:
		if err == nil {
			t.Fatal("expected the cancelled-while-paused turn to error, got nil")
		}
		if !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("expected context-canceled error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not unblock the paused agent — Stop would hang")
	}
}

// autoPausingBash is a bash stub that counts Execute calls and pauses its wired
// agent on the FIRST execution. Pausing from within Execute is the key to a
// race-free test: Execute is synchronous inside step 1, so the pause is recorded
// before the loop can reach awaitPause at the top of step 2.
type autoPausingBash struct {
	mu    sync.Mutex
	n     int
	cond  *sync.Cond
	agent pauser // set by the test before Run starts
}

// pauser is the minimal interface autoPausingBash needs from the Agent, kept
// tiny so the stub doesn't import the concrete type (avoids an import cycle in
// other test files that might reuse it).
type pauser interface{ Pause() }

func newAutoPausingBash() *autoPausingBash {
	c := &autoPausingBash{}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *autoPausingBash) Name() string        { return "bash" }
func (c *autoPausingBash) Description() string { return "auto-pausing bash stub" }
func (c *autoPausingBash) ReadOnly() bool      { return false }
func (c *autoPausingBash) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)
}
func (c *autoPausingBash) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	c.mu.Lock()
	c.n++
	first := c.n == 1
	c.cond.Broadcast()
	agent := c.agent
	c.mu.Unlock()
	// Pause on the first execution only — subsequent calls (after Resume) must
	// not re-pause, or the turn could never finish.
	if first && agent != nil {
		agent.Pause()
	}
	return "ok", nil
}
func (c *autoPausingBash) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
func (c *autoPausingBash) waitForExecs(want int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.n < want && time.Now().Before(deadline) {
		c.cond.Wait()
	}
}
