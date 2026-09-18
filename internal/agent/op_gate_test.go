package agent

import (
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/provider"
)

// TestDecide_AllBranches covers every route of the pure decide function. Each
// subtest isolates one rule so a regression points at the exact branch.
func TestDecide_AllBranches(t *testing.T) {
	tests := []struct {
		name string
		f    opFacts
		want opRoute
	}{
		// Rule 1: read-only is ALWAYS allowed — even mid-death-spiral, diagnosis
		// must stay possible.
		{"read-only allowed despite episode stopped", opFacts{readOnly: true, episodeStopped: true}, routeAllow},
		{"read-only allowed despite op stopped", opFacts{readOnly: true, sameStoppedOp: true}, routeAllow},
		{"read-only allowed at threshold", opFacts{readOnly: true, opFailureCount: 99}, routeAllow},

		// Rule 2: turn budget exhausted → stop the turn (writes paused).
		{"episode stopped stops turn", opFacts{episodeStopped: true}, routeStopTurn},

		// Rule 3: op already stopped this turn → stop (don't let it rerun).
		{"already-stopped op refused", opFacts{sameStoppedOp: true}, routeStop},

		// Rule 4: op hit per-op threshold → stop.
		{"op at threshold stopped", opFacts{opFailureCount: opFailureThreshold}, routeStop},
		{"op above threshold stopped", opFacts{opFailureCount: opFailureThreshold + 1}, routeStop},

		// Rule 5 (default): fresh op or below threshold → allow. The crux of
		// "stop only the failing op, let unrelated work continue".
		{"fresh write op allowed", opFacts{}, routeAllow},
		{"op below threshold allowed", opFacts{opFailureCount: opFailureThreshold - 1}, routeAllow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decide(tt.f); got != tt.want {
				t.Errorf("decide(%+v) = %s, want %s", tt.f, got, tt.want)
			}
		})
	}
}

// TestDecide_OrderingPrecedence confirms rule precedence: readOnly beats
// episodeStopped (diagnosis always wins), and sameStoppedOp beats threshold.
func TestDecide_OrderingPrecedence(t *testing.T) {
	// readOnly + episodeStopped → allow (rule 1 before rule 2).
	if got := decide(opFacts{readOnly: true, episodeStopped: true}); got != routeAllow {
		t.Errorf("readOnly should beat episodeStopped, got %s", got)
	}
	// episodeStopped + sameStoppedOp → stopTurn (rule 2 before rule 3): once the
	// whole turn is stopped we report the stronger condition.
	if got := decide(opFacts{episodeStopped: true, sameStoppedOp: true}); got != routeStopTurn {
		t.Errorf("episodeStopped should beat sameStoppedOp, got %s", got)
	}
}

// TestIsQualifyingFailure covers the safety-boundary filter: policy denials,
// read-only failures, and transient errors must NOT count against the budget.
func TestIsQualifyingFailure(t *testing.T) {
	tests := []struct {
		name         string
		errMsg       string
		blocked      bool
		toolReadOnly bool
		want         bool
	}{
		{"success never qualifies", "", false, false, false},
		{"permission/plan/hook block never qualifies", "blocked by permission policy", true, false, false},
		{"read-only failure never qualifies", "no matches", false, true, false},
		{"transient timeout never qualifies", "command timed out after 30s", false, false, false},
		{"transient deadline never qualifies", "context deadline exceeded", false, false, false},
		{"genuine write failure qualifies", "exit status 1", false, false, true},
		{"genuine command failure qualifies", "permission denied: /root/x", false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isQualifyingFailure(tt.errMsg, tt.blocked, tt.toolReadOnly); got != tt.want {
				t.Errorf("isQualifyingFailure(%q, blocked=%v, ro=%v) = %v, want %v",
					tt.errMsg, tt.blocked, tt.toolReadOnly, got, tt.want)
			}
		})
	}
}

// TestOpFingerprint_Stable confirms two calls differing only in JSON whitespace
// share a fingerprint (the model re-running an op with cosmetic arg changes is
// still detected as the same operation).
func TestOpFingerprint_Stable(t *testing.T) {
	c1 := provider.ToolCall{Name: "bash", Arguments: `{"command":"ls -la"}`}
	c2 := provider.ToolCall{Name: "bash", Arguments: `{ "command" : "ls -la" }`} // whitespace diff
	if opFingerprint(c1) != opFingerprint(c2) {
		t.Errorf("whitespace-only arg difference should share a fingerprint")
	}
	// Different command → different fingerprint.
	c3 := provider.ToolCall{Name: "bash", Arguments: `{"command":"ls -lb"}`}
	if opFingerprint(c1) == opFingerprint(c3) {
		t.Errorf("different args should produce different fingerprints")
	}
	// Different tool name → different fingerprint.
	c4 := provider.ToolCall{Name: "edit_file", Arguments: `{"command":"ls -la"}`}
	if opFingerprint(c1) == opFingerprint(c4) {
		t.Errorf("different tool names should produce different fingerprints")
	}
}

// TestOpGate_StopOnlyFailingOp is the core behavioral guarantee: after op A
// hits its threshold and is stopped, an unrelated op B is still allowed.
func TestOpGate_StopOnlyFailingOp(t *testing.T) {
	g := newOpGate()
	fpA := "opA"
	fpB := "opB"

	// Fail op A three times (the threshold).
	for i := 0; i < opFailureThreshold; i++ {
		g.observeResult(fpA, "exit status 1", false, false)
	}
	// Op A is now stopped — re-proposing it is refused.
	if _, stop := g.beforeMutation(fpA); !stop {
		t.Errorf("op A should be stopped after %d failures", opFailureThreshold)
	}
	// Op B (unrelated) is still allowed — the loop on A must not block B.
	if _, stop := g.beforeMutation(fpB); stop {
		t.Errorf("unrelated op B must still be allowed after op A is stopped")
	}
}

// TestOpGate_SuccessClearsOpFailures confirms a success resets that operation's
// failure count (real progress), so a later failure starts fresh.
func TestOpGate_SuccessClearsOpFailures(t *testing.T) {
	g := newOpGate()
	fp := "opX"
	// Two failures (below threshold).
	g.observeResult(fp, "err1", false, false)
	g.observeResult(fp, "err2", false, false)
	// Success clears it.
	g.observeResult(fp, "", false, false)
	// Now two more failures should NOT stop it (count restarted).
	g.observeResult(fp, "err3", false, false)
	g.observeResult(fp, "err4", false, false)
	if _, stop := g.beforeMutation(fp); stop {
		t.Errorf("op should not be stopped after success cleared its count")
	}
}

// TestOpGate_TurnBudgetStopsAllWrites confirms the episodeFailureLimit stops
// ALL write ops but read-only diagnosis is still allowed.
func TestOpGate_TurnBudgetStopsAllWrites(t *testing.T) {
	g := newOpGate()
	// Spread failures across distinct ops so no single op hits its threshold,
	// but the turn total reaches episodeFailureLimit.
	for i := 0; i < episodeFailureLimit; i++ {
		g.observeResult("op"+string(rune('A'+i)), "err", false, false)
	}
	// Turn is now stopped — a fresh write op is refused.
	if _, stop := g.beforeMutation("freshWriteOp"); !stop {
		t.Errorf("fresh write should be refused once turn budget is exhausted")
	}
}

// TestOpGate_BlockedResultsDoNotCount: permission/plan/hook denials (blocked)
// must not increment the budget.
func TestOpGate_BlockedResultsDoNotCount(t *testing.T) {
	g := newOpGate()
	fp := "opBlocked"
	for i := 0; i < opFailureThreshold+2; i++ {
		g.observeResult(fp, "blocked by permission policy", true, false)
	}
	// Should NOT be stopped — blocks don't count.
	if _, stop := g.beforeMutation(fp); stop {
		t.Errorf("blocked (permission) results must not count toward the budget")
	}
	if g.episodeFailures != 0 {
		t.Errorf("episodeFailures = %d, want 0 (blocks don't count)", g.episodeFailures)
	}
}

// TestOpGate_ReadOnlyFailuresDoNotCount: a read-only tool failing repeatedly
// (e.g. grep finding nothing) must not trigger the gate.
func TestOpGate_ReadOnlyFailuresDoNotCount(t *testing.T) {
	g := newOpGate()
	fp := "grepOp"
	for i := 0; i < opFailureThreshold+2; i++ {
		g.observeResult(fp, "no matches", false, true) // toolReadOnly = true
	}
	if _, stop := g.beforeMutation(fp); stop {
		// beforeMutation on a read-only op isn't called in production, but the
		// state should still be clean.
		t.Errorf("read-only failures must not populate the gate")
	}
	if g.opFailures[fp] != 0 {
		t.Errorf("read-only failures should not be counted; got %d", g.opFailures[fp])
	}
}

// TestOpGate_TransientFailuresDoNotCount: timeout/deadline errors are handled
// by the provider retry layer and must not count here.
func TestOpGate_TransientFailuresDoNotCount(t *testing.T) {
	g := newOpGate()
	fp := "timeoutOp"
	for i := 0; i < opFailureThreshold+2; i++ {
		g.observeResult(fp, "command timed out after 30s", false, false)
	}
	if g.opFailures[fp] != 0 {
		t.Errorf("transient failures should not be counted; got %d", g.opFailures[fp])
	}
}

// TestOpGate_Reset confirms reset() wipes all per-turn state.
func TestOpGate_Reset(t *testing.T) {
	g := newOpGate()
	// Fill state.
	for i := 0; i < opFailureThreshold; i++ {
		g.observeResult("opA", "err", false, false)
	}
	g.observeResult("opB", "err", false, false)
	if g.episodeFailures == 0 || len(g.opFailures) == 0 {
		t.Fatal("expected non-empty state before reset")
	}
	// Reset.
	g.reset()
	if g.episodeFailures != 0 || g.episodeStopped {
		t.Errorf("reset did not clear episodeFailures/episodeStopped")
	}
	if len(g.opFailures) != 0 || len(g.stoppedOps) != 0 {
		t.Errorf("reset did not clear opFailures/stoppedOps")
	}
}

// TestOpGate_GuidanceReturned confirms observeResult returns a model-facing
// guidance message exactly when a threshold is crossed (not on every failure).
func TestOpGate_GuidanceReturned(t *testing.T) {
	g := newOpGate()
	fp := "opG"
	// First two failures: no guidance yet (below threshold).
	if msg := g.observeResult(fp, "err", false, false); msg != "" {
		t.Errorf("expected no guidance before threshold, got %q", msg)
	}
	if msg := g.observeResult(fp, "err", false, false); msg != "" {
		t.Errorf("expected no guidance before threshold, got %q", msg)
	}
	// Third failure crosses the threshold → guidance returned.
	msg := g.observeResult(fp, "err", false, false)
	if msg == "" || !strings.Contains(msg, "操作恢复") {
		t.Errorf("expected op-stop guidance at threshold, got %q", msg)
	}
	// Fourth failure of the same op: it's already stopped, observeResult still
	// increments episodeFailures but no NEW op-stop guidance (already stopped).
	msg2 := g.observeResult(fp, "err", false, false)
	if strings.Contains(msg2, "操作恢复") {
		t.Errorf("op already stopped: no duplicate op-stop guidance, got %q", msg2)
	}
}

// TestEffectiveContextWindow_Budget verifies the context-budget percentage
// scales the window used for compaction decisions (SPEC v2 §3.6). A separate
// test file would be cleaner but reusing this one avoids a new import block.
func TestEffectiveContextWindow_Budget(t *testing.T) {
	cases := []struct {
		window int
		pct    int
		want   int
	}{
		{100000, 0, 100000},   // 0/100 = full window (default)
		{100000, 100, 100000}, // 100 = full window
		{100000, 80, 80000},   // 80% budget
		{100000, 50, 50000},   // 50% budget
		{0, 80, 0},            // no window at all
	}
	for _, c := range cases {
		a := &Agent{contextWindow: c.window, contextBudgetFrac: float64(c.pct) / 100.0}
		// Guard against pct=0 → frac would be 0; effectiveContextWindow clamps
		// frac<=0 to 1.0, so reproduce that for the 0 case.
		if c.pct == 0 {
			a.contextBudgetFrac = 1.0
		}
		got := a.effectiveContextWindow()
		if got != c.want {
			t.Errorf("window=%d pct=%d → effective=%d, want %d", c.window, c.pct, got, c.want)
		}
	}
}
