package netdev

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

// collectLive buffers live events with their own lock; tests wait on a
// condition rather than sleeping.
type liveCollector struct {
	mu     sync.Mutex
	events []LiveEvent
}

func (c *liveCollector) on(ev LiveEvent) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

func (c *liveCollector) snapshot() []LiveEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]LiveEvent(nil), c.events...)
}

func (c *liveCollector) waitKind(t *testing.T, kind string, n int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		got := 0
		for _, ev := range c.snapshot() {
			if ev.Kind == kind {
				got++
			}
		}
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d %q events; got %d of %v", n, kind, got, c.snapshot())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The full command lifecycle flows to the observer: start (classified read) →
// incremental output chunks → end(ok). A write command surfaces as a
// cmd_refused with the classifier class — being stopped must be VISIBLE.
func TestLiveObserverCommandLifecycle(t *testing.T) {
	m, _ := guardrailManager(t, config.NetDevGuardrails{})
	col := &liveCollector{}
	m.SetLiveObserver(col.on)

	res := m.Exec(context.Background(), "sw1", "display version")
	if res.Refused {
		t.Fatalf("read command refused: %s", res.Refusal)
	}
	col.waitKind(t, LiveCmdStart, 1)
	col.waitKind(t, LiveCmdEnd, 1)

	var start, end LiveEvent
	var outputs int
	for _, ev := range col.snapshot() {
		switch ev.Kind {
		case LiveCmdStart:
			start = ev
		case LiveCmdEnd:
			end = ev
		case LiveCmdOutput:
			outputs++
		}
	}
	if start.Device != "sw1" || start.Command != "display version" || start.Class != "read" {
		t.Fatalf("cmd_start = %+v", start)
	}
	if end.Status != AuditOK || end.Device != "sw1" || end.MS < 0 {
		t.Fatalf("cmd_end = %+v", end)
	}
	if outputs == 0 {
		t.Fatal("no incremental cmd_output events for a command that produced output")
	}
	// Connection event fired when the session was established. Two connected
	// events exist (the transport subscriber's async one may race ahead of the
	// conn registration and read VTYUse=0); the SYNCHRONOUS one from runRead
	// always carries real accounting — assert on that.
	sawConn := false
	for _, ev := range col.snapshot() {
		if ev.Kind == LiveConn && ev.State == LiveConnConnected && ev.VTYUse >= 1 && ev.VTYCap >= 1 {
			sawConn = true
		}
	}
	if !sawConn {
		t.Fatal("no connected conn event with VTY accounting for a fresh session")
	}

	// A write command is refused and surfaces as cmd_refused with class=write.
	before := len(col.snapshot())
	res = m.Exec(context.Background(), "sw1", "undo stp")
	if !res.Refused {
		t.Fatal("write command was not refused")
	}
	col.waitKind(t, LiveCmdRefused, 1)
	var refused LiveEvent
	for _, ev := range col.snapshot()[before:] {
		if ev.Kind == LiveCmdRefused {
			refused = ev
		}
	}
	if refused.Class != "write" || refused.Device != "sw1" || refused.Reason == "" {
		t.Fatalf("cmd_refused = %+v", refused)
	}
}

// Live chunks are sanitized before they leave the package: ANSI stripped and
// credential-looking lines redacted — a secret never reaches the UI stream.
func TestLiveChunkSanitization(t *testing.T) {
	in := "\x1b[32minterface GE0/0/1\r\nsnmp-server community public RO\n"
	out := SanitizeForLive(in)
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("ANSI escape leaked: %q", out)
	}
	if strings.Contains(out, "public") {
		t.Fatalf("community string leaked into the live stream: %q", out)
	}
	if !strings.Contains(out, "<redacted>") {
		t.Fatalf("expected a redaction marker, got %q", out)
	}
	if !strings.Contains(out, "interface GE0/0/1") {
		t.Fatalf("non-secret line mangled: %q", out)
	}
}

// max_sessions_per_device is enforced on the NETCONF path: with the budget
// fully claimed, a concurrent RPC is refused at the cap; below it, claims
// acquire and release cleanly.
func TestLiveVTYCapEnforced(t *testing.T) {
	m, _ := guardrailManager(t, config.NetDevGuardrails{})
	// Claim the whole default budget (2) via in-flight RPCs — no fake conn
	// needed (a conn entry must hold live sessions; nil ones would panic on
	// Close).
	m.mu.Lock()
	m.netconfInflight["sw1"] = 2
	m.mu.Unlock()

	if err := m.acquireNetconfVTY("sw1"); err == nil {
		t.Fatal("acquire beyond the default cap (2) must fail")
	} else if !strings.Contains(err.Error(), "session cap") {
		t.Fatalf("cap error = %v", err)
	}

	// Below the cap the claim succeeds and releases cleanly.
	m.mu.Lock()
	m.netconfInflight["sw1"] = 1
	m.mu.Unlock()
	if err := m.acquireNetconfVTY("sw1"); err != nil {
		t.Fatalf("acquire within cap: %v", err)
	}
	m.releaseNetconfVTY("sw1")
	m.releaseNetconfVTY("sw1") // release is idempotent at zero
	m.mu.Lock()
	if _, ok := m.netconfInflight["sw1"]; ok {
		m.mu.Unlock()
		t.Fatal("inflight entry should be removed at zero")
	}
	m.mu.Unlock()
}

// The mount-time snapshot reports inventory + VTY accounting + budget.
func TestLiveStateSnapshot(t *testing.T) {
	m, _ := guardrailManager(t, config.NetDevGuardrails{TurnCommandBudget: 7})
	m.Exec(context.Background(), "sw1", "display version")

	snap := m.LiveState()
	if snap.Budget != 7 {
		t.Fatalf("budget = %d, want 7", snap.Budget)
	}
	if snap.Spent != 1 {
		t.Fatalf("spent = %d, want 1", snap.Spent)
	}
	var sw1 *LiveDeviceState
	for i := range snap.Devices {
		if snap.Devices[i].Device == "sw1" {
			sw1 = &snap.Devices[i]
		}
	}
	if sw1 == nil {
		t.Fatal("sw1 missing from snapshot")
	}
	if !sw1.Connected || sw1.VTYUse != 1 || sw1.VTYCap != 2 {
		t.Fatalf("sw1 state = %+v", sw1)
	}
}
