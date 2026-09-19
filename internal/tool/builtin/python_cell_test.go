package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/pykernel"
	"github.com/zzycxz/hiq/internal/tool"
)

func TestPythonCellContract(t *testing.T) {
	p := pythonCell{}
	if p.Name() != "python_cell" {
		t.Fatalf("name = %q", p.Name())
	}
	if p.ReadOnly() {
		t.Fatal("python_cell must be a writer (cells can do anything)")
	}
	var schema map[string]any
	if err := json.Unmarshal(p.Schema(), &schema); err != nil {
		t.Fatalf("schema not JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["code"]; !ok {
		t.Fatal("schema missing code property")
	}
	// The description is prompt surface: it must mention the two host
	// capabilities and the completion contract, deterministically.
	desc := p.Description()
	for _, want := range []string{"hiq.tool", "hiq.submit_output", "persistent"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q", want)
		}
	}
}

func TestPythonCellValidateEmptyCode(t *testing.T) {
	p := pythonCell{}
	if _, err := p.Execute(context.Background(), json.RawMessage(`{"code": "  "}`)); err == nil {
		t.Fatal("empty code should error")
	}
	if _, err := p.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing code should error")
	}
}

func TestFormatCellResult(t *testing.T) {
	if got := formatCellResult(pykernel.Result{}); got != "(cell completed with no output; state persists for the next cell)" {
		t.Fatalf("empty = %q", got)
	}
	res := pykernel.Result{
		Stdout:     "hello\n",
		Stderr:     "warned\n",
		Error:      "Traceback ... ValueError: boom",
		Completion: map[string]any{"summary": "done"},
		Restarted:  true,
	}
	got := formatCellResult(res)
	for _, want := range []string{
		"kernel was restarted",
		"--- stdout ---", "hello",
		"--- stderr ---", "warned",
		"--- error ---", "ValueError",
		"--- submitted output ---", "The task's final result has been submitted",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted result missing %q:\n%s", want, got)
		}
	}
}

// TestPythonCellHostToolGatedByRegistry proves the mid-cell host.tool dispatch
// only reaches read-only tools of the ctx-stamped registry: a writer tool
// fails closed even though the kernel side would happily forward it, and a
// registry-less context fails closed too.
func TestPythonCellHostToolGatedByRegistry(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(fakeROTool{name: "grep", readOnly: true})
	reg.Add(fakeROTool{name: "write_file", readOnly: false})

	pyReg := pyRegistry{reg}
	rt, ok := pyReg.Get("grep")
	if !ok || !rt.ReadOnly() {
		t.Fatal("pyRegistry must expose the read-only tool")
	}

	host := pykernel.RegistryHost{}
	ctx := pykernel.WithRegistry(context.Background(), pyReg)
	if _, err := host.HostCall(ctx, "tool", map[string]any{"name": "write_file", "args": map[string]any{}}); err == nil {
		t.Fatal("writer tool must be rejected from inside a cell")
	}
	if _, err := host.HostCall(ctx, "tool", map[string]any{"name": "missing", "args": map[string]any{}}); err == nil {
		t.Fatal("unknown tool must be rejected")
	}
	out, err := host.HostCall(ctx, "tool", map[string]any{"name": "grep", "args": map[string]any{"pattern": "x"}})
	if err != nil {
		t.Fatalf("read-only call: %v", err)
	}
	if out != "grep done" {
		t.Fatalf("out = %v", out)
	}
	if _, err := host.HostCall(context.Background(), "tool", map[string]any{"name": "grep"}); err == nil {
		t.Fatal("registry-less ctx must fail closed")
	}
}

// --- small fakes (distinct from the agent package's fakeTool) ---

type fakeROTool struct {
	name     string
	readOnly bool
}

func (f fakeROTool) Name() string        { return f.name }
func (f fakeROTool) Description() string { return "fake" }
func (f fakeROTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (f fakeROTool) ReadOnly() bool { return f.readOnly }
func (f fakeROTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return f.name + " done", nil
}
