package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/pykernel"
	"github.com/zzycxz/hiq/internal/tool"
)

func init() { tool.RegisterBuiltin(pythonCell{}) }

// pythonCell is the Code-as-Action tool: one persistent Python cell replaces
// many JSON tool round-trips for data-heavy work. Ported from
// PKU-YuanGroup/OpenAI4S (MIT); see internal/pykernel for the runtime.
//
// The description below is prompt surface: it teaches the dual-plane contract
// (JSON tools orchestrate; the kernel computes) exactly like OpenAI4S routes
// between its control and science planes. It must stay deterministic — it is
// part of the cache-stable prompt.
type pythonCell struct{}

func (pythonCell) Name() string { return "python_cell" }

func (pythonCell) Description() string {
	return `Execute one Python code cell in a persistent kernel and return its output. Unlike one-shot tools, the kernel keeps its full state (imports, variables, functions, open handles) between cells, so multi-step data work — load, filter, aggregate, plot, iterate — belongs in cells instead of many small tool round-trips.

Prefer python_cell whenever several shell/read calls would only shuffle data through the model: one cell computing an answer beats five calls piping text back and forth. JSON tools stay right for orchestration (web_search for lookups, file tools for edits the user should see).

The ` + "`hiq`" + ` object is available in every cell:
- hiq.tool(name, args) — call a read-only hiq tool (e.g. hiq.tool("web_search", {"query": "..."})).
- hiq.submit_output(summary=..., **data) — declare the final structured result of the task from inside a cell (at most once).

Notes: state survives across cells within the session; a timeout or explicit reset restarts the kernel and loses state; stdout/stderr are captured (64KB cap each); heavy third-party libraries are available only if installed in the machine's Python.`
}

func (pythonCell) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "code": {
      "type": "string",
      "description": "Python source for this cell. Runs with exec in the persistent namespace. Use print() to emit results the model should read."
    },
    "timeout_seconds": {
      "type": "number",
      "description": "Optional per-cell timeout in seconds (default 300). On timeout the kernel is killed and its state is lost."
    },
    "reset": {
      "type": "boolean",
      "description": "Restart the kernel before running this cell, discarding all previous state (default false)."
    }
  },
  "required": ["code"]
}`)
}

func (pythonCell) ReadOnly() bool { return false }

// Execute runs the cell through the process-wide kernel manager. The registry
// stamped by the agent (tool.WithRegistry) is re-stamped as the kernel's
// host-tool dispatcher so mid-cell hiq.tool(...) calls hit the same session's
// tools.
func (pythonCell) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Code           string  `json:"code"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
		Reset          bool    `json:"reset"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(in.Code) == "" {
		return "", fmt.Errorf("code is required")
	}
	if reg, ok := tool.RegistryFrom(ctx); ok {
		ctx = pykernel.WithRegistry(ctx, pyRegistry{reg})
	}
	timeout := time.Duration(in.TimeoutSeconds * float64(time.Second))
	res, err := pykernel.Default().Cell(ctx, in.Code, timeout, in.Reset)
	if err != nil {
		return "", err
	}
	return formatCellResult(res), nil
}

func formatCellResult(res pykernel.Result) string {
	var b strings.Builder
	if res.Restarted {
		b.WriteString("note: the kernel was restarted before this cell; previous state is gone\n")
	}
	if res.Stdout != "" {
		b.WriteString("--- stdout ---\n")
		b.WriteString(res.Stdout)
		if !strings.HasSuffix(res.Stdout, "\n") {
			b.WriteByte('\n')
		}
	}
	if res.Stderr != "" {
		b.WriteString("--- stderr ---\n")
		b.WriteString(res.Stderr)
		if !strings.HasSuffix(res.Stderr, "\n") {
			b.WriteByte('\n')
		}
	}
	if res.Error != "" {
		b.WriteString("--- error ---\n")
		b.WriteString(res.Error)
		if !strings.HasSuffix(res.Error, "\n") {
			b.WriteByte('\n')
		}
	}
	if len(res.Completion) > 0 {
		b.WriteString("--- submitted output ---\n")
		enc, err := json.MarshalIndent(res.Completion, "", "  ")
		if err != nil {
			fmt.Fprintf(&b, "%v", res.Completion)
		} else {
			b.Write(enc)
		}
		b.WriteByte('\n')
		b.WriteString("The task's final result has been submitted. Stop calling tools and give the user a short final answer summarizing it.\n")
	}
	if b.Len() == 0 {
		return "(cell completed with no output; state persists for the next cell)"
	}
	return strings.TrimRight(b.String(), "\n")
}

// pyRegistry adapts the agent-stamped tool registry to the kernel's narrow
// interface without creating an import cycle (tool -> pykernel only).
type pyRegistry struct{ reg *tool.Registry }

func (p pyRegistry) Get(name string) (pykernel.ReadOnlyTool, bool) {
	t, ok := p.reg.Get(name)
	if !ok {
		return nil, false
	}
	return pyReadOnly{t}, true
}

type pyReadOnly struct{ t tool.Tool }

func (p pyReadOnly) ReadOnly() bool { return p.t.ReadOnly() }
func (p pyReadOnly) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return p.t.Execute(ctx, args)
}
