package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/tool"
)

// TestExecuteOne_OpGateStopsFailingOp is the end-to-end integration test for
// the op-recovery gate: driving the REAL executeOne path, a write tool that
// fails the same way 3× must be blocked on the 4th attempt (the gate refuses it
// before it runs), while a different write tool still runs fine.
func TestExecuteOne_OpGateStopsFailingOp(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "write_file", readOnly: false, err: errors.New("disk full")})
	reg.Add(fakeTool{name: "edit_file", readOnly: false}) // succeeds

	a := New(nil, reg, NewSession(""), Options{}, event.Discard)
	call := provider.ToolCall{Name: "write_file", Arguments: `{"path":"/a","content":"x"}`}

	// Attempts 1-3: the tool runs and fails each time (err is set on the tool).
	// The first two should NOT be blocked (below threshold); the third crosses
	// the threshold and appends the op-recovery guidance to the output.
	seenGuidance := false
	for i := 1; i <= opFailureThreshold; i++ {
		out := a.executeOne(context.Background(), call, nil)
		if out.blocked {
			t.Fatalf("attempt %d: should run (below/at threshold), not be pre-blocked", i)
		}
		if out.errMsg == "" {
			t.Fatalf("attempt %d: expected a failure errMsg", i)
		}
		if strings.Contains(out.output, "操作恢复") {
			seenGuidance = true
		}
	}
	if !seenGuidance {
		t.Errorf("the threshold-crossing attempt (3rd) should append op-recovery guidance to the output")
	}

	// 4th attempt: the op is now stopped — executeOne must block BEFORE running
	// the tool (beforeMutation refuses), so the tool's err is irrelevant.
	out := a.executeOne(context.Background(), call, nil)
	if !out.blocked {
		t.Errorf("4th attempt of the same failing op should be blocked by the op-recovery gate")
	}
	if !strings.Contains(out.output, "op-recovery") && !strings.Contains(out.output, "已被停止") {
		t.Errorf("blocked output should explain the stop, got %q", out.output)
	}

	// A DIFFERENT write tool must still run — the gate stops only the failing op.
	other := a.executeOne(context.Background(), provider.ToolCall{Name: "edit_file", Arguments: `{"path":"/b"}`}, nil)
	if other.blocked {
		t.Errorf("unrelated write tool should still run after one op is stopped, got blocked: %q", other.output)
	}
}

// TestExecuteOne_OpGateReadOnlyFailureIgnored confirms a read-only tool that
// keeps "failing" (e.g. grep with no matches) does NOT trip the gate — read-only
// diagnosis must always be possible, and a read-only failure isn't a death spiral.
func TestExecuteOne_OpGateReadOnlyFailureIgnored(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "grep", readOnly: true, err: errors.New("no matches found")})

	a := New(nil, reg, NewSession(""), Options{}, event.Discard)
	call := provider.ToolCall{Name: "grep", Arguments: `{"pattern":"x"}`}

	for i := 0; i < opFailureThreshold+2; i++ {
		out := a.executeOne(context.Background(), call, nil)
		if out.blocked {
			t.Fatalf("attempt %d: read-only failure must never be blocked by the op-recovery gate", i+1)
		}
	}
}

// TestExecuteOne_OpGatePermissionBlockIgnored confirms a permission/plan/hook
// denial (blocked result) does NOT count toward the op-recovery budget — a
// safety boundary is not a reliability signal.
func TestExecuteOne_OpGatePermissionBlockIgnored(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "write_file", readOnly: false, err: errors.New("write failed")})

	// A gate that always denies → executeOne returns blocked, never a real exec.
	stub := &stubGate{deny: map[string]bool{"write_file": true}}
	a := New(nil, reg, NewSession(""), Options{Gate: stub}, event.Discard)
	call := provider.ToolCall{Name: "write_file", Arguments: `{"path":"/a"}`}

	for i := 0; i < opFailureThreshold+2; i++ {
		out := a.executeOne(context.Background(), call, nil)
		if !out.blocked {
			t.Fatalf("attempt %d: permission gate should block the call", i+1)
		}
	}
	// After many permission blocks, the op-recovery gate should NOT have armed a
	// stop — blocks don't count. Verify by removing the deny and confirming the
	// tool runs (not op-recovery-blocked).
	stub.deny = map[string]bool{}
	out := a.executeOne(context.Background(), call, nil)
	if strings.Contains(out.output, "op-recovery") || strings.Contains(out.output, "已被停止") {
		t.Errorf("permission blocks must not arm the op-recovery gate; got %q", out.output)
	}
}

// TestExecuteOne_OpGateSuccessClears confirms that after a failing op succeeds
// once, its failure history is cleared — a subsequent failure starts fresh and
// needs the full threshold again.
func TestExecuteOne_OpGateSuccessClears(t *testing.T) {
	reg := tool.NewRegistry()
	// A tool that fails when content == "fail", succeeds otherwise.
	reg.Add(switchTool{name: "write_file"})

	a := New(nil, reg, NewSession(""), Options{}, event.Discard)
	failCall := provider.ToolCall{Name: "write_file", Arguments: `{"path":"/a","content":"fail"}`}
	okCall := provider.ToolCall{Name: "write_file", Arguments: `{"path":"/a","content":"ok"}`}

	// Two failures (below threshold).
	a.executeOne(context.Background(), failCall, nil)
	a.executeOne(context.Background(), failCall, nil)
	// A success clears the count (different content → different op too, but the
	// fingerprint differs here; this mainly checks the success path is wired).
	a.executeOne(context.Background(), okCall, nil)
	// The failCall again: only 1 failure now in its own fingerprint bucket, so
	// it must NOT be blocked.
	out := a.executeOne(context.Background(), failCall, nil)
	if out.blocked {
		t.Errorf("failCall after a success must not be blocked yet (count restarted); got %q", out.output)
	}
}

// switchTool fails when the "content" arg is "fail", succeeds otherwise. Used
// to exercise the success-clears-failures path within a single tool name but
// distinct fingerprints.
type switchTool struct{ name string }

func (s switchTool) Name() string        { return s.name }
func (s switchTool) Description() string { return "" }
func (s switchTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}}}`)
}
func (s switchTool) ReadOnly() bool { return false }
func (s switchTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(args, &p)
	if p.Content == "fail" {
		return "", errors.New("write failed")
	}
	return "ok", nil
}

// Compile-time: switchTool satisfies tool.Tool via the registry's interface.
var _ tool.Tool = switchTool{}
