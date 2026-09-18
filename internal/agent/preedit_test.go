package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/zzycxz/hiq/internal/agent/testutil"
	"github.com/zzycxz/hiq/internal/diff"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/tool"
)

// preEditRecorder collects the changes a PreEditHook receives.
type preEditRecorder struct {
	mu      sync.Mutex
	changes []diff.Change
}

func (r *preEditRecorder) record(ch diff.Change) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, ch)
}

func (r *preEditRecorder) paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.changes))
	for i, c := range r.changes {
		out[i] = c.Path
	}
	return out
}

// previewableWriter is the smallest writer tool the checkpoint seam needs:
// ReadOnly false + Previewer. Execute "writes" without touching disk — the
// hook fires before Execute and must see the previewed change.
type previewableWriter struct{}

func (previewableWriter) Name() string        { return "demo_write" }
func (previewableWriter) Description() string { return "test writer" }
func (previewableWriter) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
}
func (previewableWriter) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return "written", nil
}
func (previewableWriter) ReadOnly() bool { return false }
func (previewableWriter) Preview(args json.RawMessage) (diff.Change, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return diff.Change{}, err
	}
	return diff.Change{Path: a.Path, Kind: diff.Modify, OldText: "old", NewText: "new"}, nil
}

// TestAgentPreEditHookFiresForWriter verifies the Options.PreEditHook seam: a
// writer tool call during a turn delivers its previewed change to the hook —
// the wiring sub-agents rely on for checkpoint capture (WORKSPACE_GIT_SPEC G5).
func TestAgentPreEditHookFiresForWriter(t *testing.T) {
	rec := &preEditRecorder{}
	reg := tool.NewRegistry()
	reg.Add(previewableWriter{})
	mp := testutil.NewMock("test",
		testutil.Turn{ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "demo_write", Arguments: `{"path":"src/a.txt"}`},
		}},
		testutil.Turn{Text: "done"},
	)
	a := New(mp, reg, NewSession(""), Options{PreEditHook: rec.record}, nil)
	if err := a.Run(context.Background(), "write it"); err != nil {
		t.Fatal(err)
	}
	if got := rec.paths(); len(got) != 1 || got[0] != "src/a.txt" {
		t.Fatalf("pre-edit hook saw %v, want exactly [src/a.txt]", got)
	}
	if ch := rec.changes[0]; ch.Kind != diff.Modify {
		t.Fatalf("change kind = %q, want modify", ch.Kind)
	}
}

// TestSharedPreEditHookLateSet verifies the boot→controller hand-off shape:
// Fire before Set is a no-op (headless runs), and a hook installed after
// construction receives subsequent fires — controller binds its checkpoint
// store after boot.Build created the spawn sites.
func TestSharedPreEditHookLateSet(t *testing.T) {
	h := NewSharedPreEditHook()
	h.Fire(diff.Change{Path: "early.txt"}) // before Set: must not panic or deliver

	rec := &preEditRecorder{}
	h.Set(rec.record)
	h.Fire(diff.Change{Path: "late.txt"})
	h.Set(nil) // session rebind path: detach again
	h.Fire(diff.Change{Path: "after-detach.txt"})

	if got := rec.paths(); len(got) != 1 || got[0] != "late.txt" {
		t.Fatalf("recorder saw %v, want exactly [late.txt]", got)
	}
}
