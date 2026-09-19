package pykernel

import (
	"context"
	"encoding/json"
	"fmt"
)

// ReadOnlyTool is the slice of tool.Tool a cell may call: read-only built-ins
// only. Declared here (not imported from internal/tool) so the dependency
// arrow stays tool -> pykernel, never the reverse.
type ReadOnlyTool interface {
	ReadOnly() bool
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry is the slice of tool.Registry cells can see: look up by name.
type Registry interface {
	Get(name string) (ReadOnlyTool, bool)
}

type registryKey struct{}

// WithRegistry stamps ctx with the registry a cell's host.tool(...) calls are
// served from. The python_cell tool stamps it per call, unwrapping the
// agent-stamped tool registry into this package's narrow interface.
func WithRegistry(ctx context.Context, r Registry) context.Context {
	return context.WithValue(ctx, registryKey{}, r)
}

func registryFromCtx(ctx context.Context) (Registry, bool) {
	r, ok := ctx.Value(registryKey{}).(Registry)
	return r, ok && r != nil
}

// ErrNoRegistry is returned when a host.tool call arrives on a context with no
// registry stamped (plain contexts — tests, or calls outside the agent loop).
var ErrNoRegistry = fmt.Errorf("pykernel: no tool registry on this context")

// RegistryHost adapts the ctx-stamped registry into HostCaller. Only read-only
// tools are callable from inside a cell: the cell already passed the
// permission gate as a writer, but a nested write would bypass the per-call
// gates (plan mode, permission policy, hooks) the agent enforces per dispatch.
type RegistryHost struct{}

// HostCall serves the worker's host_call frames:
//
//	tool(name, args)       -> execute a read-only built-in, return its output text
//	submit_output(payload) -> terminal marker; the worker attaches it to the cell result
func (RegistryHost) HostCall(ctx context.Context, method string, args map[string]any) (any, error) {
	switch method {
	case "submit_output":
		// The worker records the payload itself; the host ack is all the
		// contract needs. Validation (non-empty payload) is worker-side.
		return nil, nil
	case "tool":
		return callRegistryTool(ctx, args)
	default:
		return nil, fmt.Errorf("unknown host method %q", method)
	}
}

func callRegistryTool(ctx context.Context, args map[string]any) (any, error) {
	r, ok := registryFromCtx(ctx)
	if !ok {
		return nil, ErrNoRegistry
	}
	name, _ := args["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("tool call requires a tool name")
	}
	t, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	if !t.ReadOnly() {
		return nil, fmt.Errorf("tool %q is not read-only; cells may only call read-only tools", name)
	}
	raw, err := marshalToolArgs(args["args"])
	if err != nil {
		return nil, err
	}
	return t.Execute(ctx, raw)
}

// marshalToolArgs converts the worker-side args (a decoded JSON object, or a
// raw JSON string) into the json.RawMessage the tool contract expects.
func marshalToolArgs(v any) (json.RawMessage, error) {
	switch t := v.(type) {
	case nil:
		return json.RawMessage("{}"), nil
	case string:
		if t == "" {
			return json.RawMessage("{}"), nil
		}
		if !json.Valid([]byte(t)) {
			return nil, fmt.Errorf("tool args string is not valid JSON")
		}
		return json.RawMessage(t), nil
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return nil, fmt.Errorf("encode tool args: %w", err)
		}
		return b, nil
	}
}
