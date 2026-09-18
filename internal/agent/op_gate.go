package agent

// op_gate.go is the operation-level failure-recovery gate (SPEC v2 §3.1), a
// host-side guard that stops an agent from looping on the same failing
// operation. It is the third line of defense alongside stormBreaker (batch-tail,
// (name,error), cross-turn) and repeatSuccessBlock (write-like success, in-turn):
//
//   - stormBreaker: coarse (name+error), cross-turn, fires after a whole batch.
//   - repeatSuccessBlock: in-turn, write-tool *success* repetition.
//   - opGate (this file): fine (name+args fingerprint), in-turn, *failure*
//     repetition. Detects "the model keeps re-running the exact same write/command
//     and it keeps failing the same way" — the case the other two miss.
//
// Design (adapted from DeepSeek-Reasonix's internal/recovery, simplified):
//
//   - Pure decision: decide(opFacts) opRoute has no side effects, no locks →
//     fully unit-testable. All mutable state lives on opGate.
//   - Operation fingerprint = sha256(tool + canonicalArgs). Reuses canonicalToolArgs.
//   - Qualifying-failure filter: permission/plan/hook denials (blocked), read-only
//     tool failures, and transient (timeout) errors NEVER enter the budget. A
//     safety boundary (permission deny) is not a reliability signal.
//   - "Stop only the failing op, let unrelated work continue": counts are keyed by
//     fingerprint; a different operation is still allowed even after one is stopped.
//   - Two budgets: per-operation (3 same-fingerprint failures) and per-turn
//     (6 total qualifying failures). The turn budget stops ALL writes but still
//     allows read-only diagnosis.
//
// Two constraints from SPEC v2 §2.0 are honored: (1) zero user learning cost —
// the gate is always on, the user only sees a natural-language Notice; (2) no
// prompt bloat — the gate is pure host logic, the model never sees "fingerprint"
// or "budget", only a tool-result message when an op is stopped.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/zzycxz/hiq/internal/provider"
)

// Budget constants (ported from Reasonix budget.go, tuned to the same values
// proven in production there).
const (
	opFailureThreshold  = 3 // a single operation fingerprint may fail this many times before it is stopped
	episodeFailureLimit = 6 // a turn may accumulate this many qualifying failures before writes are paused
)

// opGate is the host-side state machine for operation-level failure recovery.
// It is safe for concurrent use (executeOne may run from parallel goroutines for
// read-only tools). All state is per-turn and reset by reset() at turn start.
type opGate struct {
	mu              sync.Mutex
	opFailures      map[string]int // fingerprint → qualifying-failure count this turn
	stoppedOps      map[string]bool
	episodeFailures int  // total qualifying failures this turn (any operation)
	episodeStopped  bool // turn write-budget exhausted
}

func newOpGate() *opGate {
	return &opGate{
		opFailures: make(map[string]int),
		stoppedOps: make(map[string]bool),
	}
}

// reset clears all per-turn state. Called at the start of each turn (mirrors
// repeatSuccessCounts = nil).
func (g *opGate) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k := range g.opFailures {
		delete(g.opFailures, k)
	}
	for k := range g.stoppedOps {
		delete(g.stoppedOps, k)
	}
	g.episodeFailures = 0
	g.episodeStopped = false
}

// opRoute is the pure decision output.
type opRoute int

const (
	routeAllow    opRoute = iota // let the operation proceed
	routeStop                    // stop this exact operation (others may continue)
	routeStopTurn                // pause all writes this turn (read-only diagnosis still allowed)
)

func (r opRoute) String() string {
	switch r {
	case routeAllow:
		return "allow"
	case routeStop:
		return "stop"
	case routeStopTurn:
		return "stop_turn"
	}
	return "unknown"
}

// opFacts is the pure, side-effect-free input to decide. Built by the caller
// from opGate's current state + the proposed call. It never carries locks or
// mutable references, so decide is trivially unit-testable.
type opFacts struct {
	readOnly            bool // the tool is read-only
	episodeStopped      bool // the turn write-budget is already exhausted
	episodeFailureCount int  // total qualifying failures this turn
	sameStoppedOp       bool // the proposed op's fingerprint is in stoppedOps
	opFailureCount      int  // the proposed op's qualifying-failure count this turn
}

// decide is the pure decision function (ported from Reasonix Decide, simplified
// to the 5 cases that matter for Hiq's model). Order matters — earlier
// rules short-circuit:
//
//  1. readOnly → allow (diagnosis must always be possible, even after a stop)
//  2. episodeStopped (writes paused for the turn) → stopTurn
//  3. the op was already stopped this turn → stop (don't let it run again)
//  4. the op has hit its per-op failure threshold → stop
//  5. otherwise → allow (unrelated work continues despite other ops failing)
func decide(f opFacts) opRoute {
	if f.readOnly {
		return routeAllow
	}
	if f.episodeStopped {
		return routeStopTurn
	}
	if f.sameStoppedOp {
		return routeStop
	}
	if f.opFailureCount >= opFailureThreshold {
		return routeStop
	}
	return routeAllow
}

// opFingerprint returns a stable SHA-256 of (tool name + canonical args). Two
// calls that differ only in JSON whitespace share a fingerprint, so a model
// re-running the "same" operation with cosmetic arg changes is still detected.
// (Reasonix separates operation vs authorization fingerprints using a preview
// field; Hiq has no preview layer, so one fingerprint suffices.)
func opFingerprint(call provider.ToolCall) string {
	h := sha256.New()
	fmt.Fprintf(h, "tool=%s\n", call.Name)
	fmt.Fprintf(h, "args=%s\n", canonicalToolArgs(call.Arguments))
	return hex.EncodeToString(h.Sum(nil))
}

// isQualifyingFailure reports whether a failed tool result should count against
// the failure budget (ported from Reasonix QualifyingFailure, adapted to
// Hiq's toolOutcome). Safety boundaries are NOT reliability signals:
//
//   - success (errMsg == "") → no
//   - blocked by permission/plan-mode/hook → no (a deny is a policy decision,
//     not the operation "failing"; counting it would punish correct gating)
//   - read-only tool failure → no (a failed grep/search isn't a death spiral)
//   - transient (timeout/deadline) → no (handled by provider retry, not this gate)
//
// Only genuine execution failures of state-changing tools count.
func isQualifyingFailure(errMsg string, blocked, toolReadOnly bool) bool {
	if errMsg == "" || blocked {
		return false
	}
	if toolReadOnly {
		return false
	}
	if isTransientErr(errMsg) {
		return false
	}
	return true
}

// isTransientErr matches the transient-error keywords Reasonix recognized
// (rules.go transientFailureText), which signal a timeout/deadline that the
// provider retry layer already handles — not a deterministic loop.
//
// Also excludes validation-script failures (check_svg.py exit 1/2) — these
// are intentional quality-gate results ("SVG has errors"), not execution
// failures. Counting them would make the gate fire during normal validate-mode
// PPT generation.
func isTransientErr(s string) bool {
	l := strings.ToLower(s)
	for _, k := range []string{
		"command timed out",
		"timed out after",
		"timed out (>",
		"context deadline exceeded",
		"deadline exceeded",
		"execution timeout",
	} {
		if strings.Contains(l, k) {
			return true
		}
	}
	// Validation scripts: check_svg.py / check_svg return non-zero exit codes
	// when they find issues (exit 1=warnings, exit 2=errors). These are
	// quality-gate results, not execution failures.
	for _, k := range []string{
		"check_svg",
		"check-svg",
		"[error]",
		"[warn]",
		"validation failed",
	} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

// observeResult records a completed tool call's outcome. Called from executeOne
// for BOTH success and failure: a success clears that operation's failure count
// (real progress), a qualifying failure increments per-op and turn budgets and
// may flip stoppedOps / episodeStopped. Returns a guidance message for the model
// (empty when nothing changed); the caller feeds it back as part of the tool
// result and emits a Notice for the user.
func (g *opGate) observeResult(fp string, errMsg string, blocked, toolReadOnly bool) (guidance string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Success of this operation = real progress: drop its failure history so a
	// later unrelated failure of the same op starts fresh. (Reasonix clearNoProgress.)
	if errMsg == "" {
		delete(g.opFailures, fp)
		delete(g.stoppedOps, fp)
		return ""
	}
	if !isQualifyingFailure(errMsg, blocked, toolReadOnly) {
		return ""
	}

	g.opFailures[fp]++
	g.episodeFailures++

	var sb strings.Builder
	if g.opFailures[fp] >= opFailureThreshold && !g.stoppedOps[fp] {
		g.stoppedOps[fp] = true
		fmt.Fprintf(&sb, opStopMessage, g.opFailures[fp])
	}
	if g.episodeFailures >= episodeFailureLimit && !g.episodeStopped {
		g.episodeStopped = true
		if sb.Len() == 0 {
			sb.WriteString(turnStopMessage)
		}
	}
	return sb.String()
}

// beforeMutation is the pre-execution admission check for a write/command tool.
// Returns (message, true) when the op must be refused this turn. The caller
// turns the message into a blocked toolOutcome. Read-only tools bypass this
// entirely (decide short-circuits on readOnly).
func (g *opGate) beforeMutation(fp string) (message string, stop bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := decide(opFacts{
		episodeStopped:      g.episodeStopped,
		episodeFailureCount: g.episodeFailures,
		sameStoppedOp:       g.stoppedOps[fp],
		opFailureCount:      g.opFailures[fp],
	})
	switch r {
	case routeStop:
		return alreadyStoppedMessage, true
	case routeStopTurn:
		return turnStopMessage, true
	}
	return "", false
}

// opStopMessage is appended to a tool result when an operation hits its
// per-op failure threshold. Natural language only — the model/user never sees
// "fingerprint" or "threshold". Chinese-first because the model-facing nudge
// doubles as the user-facing context.
const opStopMessage = "\n\n[操作恢复] 该操作已连续失败 %d 次，Hiq 已停止重复尝试它。已完成的工作已保留。请换一种方法（改参数、换工具、或拆分步骤），或在下一条消息里说明如何继续。其他无关操作不受影响。"

// alreadyStoppedMessage is returned to the model when it re-proposes an
// operation that was already stopped this turn.
const alreadyStoppedMessage = "blocked by op-recovery gate: 该操作本轮已被停止（连续失败达上限）。请换一种方法，不要重复同一个操作。"

// turnStopMessage is returned when the whole turn's write-budget is exhausted.
// Read-only diagnosis tools are still allowed (decide short-circuits readOnly).
const turnStopMessage = "blocked by op-recovery gate: 本轮累计失败已达上限，写操作已暂停（只读检查仍可继续）。已完成的工作已保留。请在下一条消息里说明新的方向。"
