// Package tool defines the Tool abstraction and a Registry. Built-in tools live
// in tool/builtin and self-register via init(); plugin-provided tools are added
// to a runtime Registry alongside the enabled built-ins. The agent sees only a
// *Registry, never the global built-in set directly.
package tool

import (
	"context"
	"encoding/json"
	"maps"
	"sort"
	"strings"
	"sync"

	"github.com/zzycxz/hiq/internal/diff"
	"github.com/zzycxz/hiq/internal/provider"
)

// Tool is a capability the model can invoke.
type Tool interface {
	Name() string
	Description() string
	// Schema returns the JSON Schema for the tool's parameters.
	Schema() json.RawMessage
	// Execute parses the model-generated raw JSON args and returns result text
	// to feed back to the model.
	Execute(ctx context.Context, args json.RawMessage) (string, error)
	// ReadOnly reports whether the tool has no observable side effects on the
	// host. The agent parallelises a batch of tool calls only when every call
	// in the batch is ReadOnly; mixed batches stay sequential so write/read
	// ordering is preserved. bash and plugin tools must return false because
	// their effects can't be inferred statically from args.
	ReadOnly() bool
}

// Previewer is an optional capability a writer Tool may implement: given the
// same raw JSON args Execute would receive, compute the file change the call
// *would* make — without touching disk. A front-end uses it to show an approval
// card or a changed-files panel before the call runs (the permission gate, not
// Preview, decides whether it may proceed). Type-assert a Tool to Previewer to
// discover support; the file-writing built-ins implement it, most tools do not.
type Previewer interface {
	Preview(args json.RawMessage) (diff.Change, error)
}

// MultiPreviewer is the multi-file form of Previewer, for writer tools whose
// single call touches several files (apply_patch). It returns one Change per
// file so the checkpoint hook can snapshot each and an approval card can list
// them individually; a move is expressed as a delete of the source plus a
// create of the destination. Tools implementing only Previewer are wrapped
// into a one-element slice by PreviewChanges.
type MultiPreviewer interface {
	PreviewFiles(args json.RawMessage) ([]diff.Change, error)
}

// ReadOnlyCallChecker is an optional capability a Tool may implement to give a
// per-call read-only vote, overriding its static ReadOnly()=false. This exists
// for tools whose side effects depend on the arguments and can't be classified
// statically — bash is the canonical case: "git log" is read-only, "rm" is not.
// Under plan mode the agent consults it to let a specific read-only invocation
// through without unconditionally admitting the tool. Tools that don't implement
// it fall back to their static ReadOnly() value.
type ReadOnlyCallChecker interface {
	ReadOnlyCall(args json.RawMessage) bool
}

// PreviewChange returns the change a writer tool would make for args, or ok=false
// when there's nothing renderable: t is read-only, doesn't implement Previewer,
// the preview errored (the edit will likely fail too), or the file is binary.
func PreviewChange(t Tool, args json.RawMessage) (diff.Change, bool) {
	if t == nil || t.ReadOnly() {
		return diff.Change{}, false
	}
	pv, ok := t.(Previewer)
	if !ok {
		return diff.Change{}, false
	}
	ch, err := pv.Preview(args)
	if err != nil || ch.Binary {
		return diff.Change{}, false
	}
	return ch, true
}

// PreviewChanges returns the per-file changes a writer tool would make for
// args, or ok=false under the same conditions as PreviewChange. MultiPreviewer
// tools return their per-file list (binary entries dropped); plain Previewer
// tools are wrapped into a one-element slice. One computation serves both the
// dispatch event's diff preview and the checkpoint hook — the caller computes
// once and passes the result through instead of re-previewing.
func PreviewChanges(t Tool, args json.RawMessage) ([]diff.Change, bool) {
	if t == nil || t.ReadOnly() {
		return nil, false
	}
	if mp, ok := t.(MultiPreviewer); ok {
		chs, err := mp.PreviewFiles(args)
		if err != nil {
			return nil, false
		}
		keep := make([]diff.Change, 0, len(chs))
		for _, ch := range chs {
			if !ch.Binary {
				keep = append(keep, ch)
			}
		}
		return keep, len(keep) > 0
	}
	ch, ok := PreviewChange(t, args)
	if !ok {
		return nil, false
	}
	return []diff.Change{ch}, true
}

// --- process-global built-in set (populated by builtin subpackage init) ---

var builtins = map[string]Tool{}

// RegisterBuiltin registers a compile-time built-in tool. Intended for init().
// It panics on a duplicate name, which is a compile-time wiring mistake.
func RegisterBuiltin(t Tool) {
	name := t.Name()
	if _, dup := builtins[name]; dup {
		panic("tool: duplicate built-in " + name)
	}
	builtins[name] = t
}

// Builtins returns all registered built-in tools, sorted by name.
func Builtins() []Tool {
	names := make([]string, 0, len(builtins))
	for n := range builtins {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Tool, 0, len(names))
	for _, n := range names {
		out = append(out, builtins[n])
	}
	return out
}

// LookupBuiltin returns a registered built-in by name.
func LookupBuiltin(name string) (Tool, bool) {
	t, ok := builtins[name]
	return t, ok
}

// --- per-run registry instance ---

// Registry is a per-run set of tools: enabled built-ins plus plugin tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
	canon map[string]json.RawMessage
	// hidden marks tools that stay callable (Get still resolves them) but are
	// omitted from Schemas() so the model never sees them in its tool list.
	// Used by cowork to register browser/desktop/calendar tools that subagents
	// reach via FilterRegistry without cluttering the main loop's schema.
	hidden map[string]bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}, canon: map[string]json.RawMessage{}, hidden: map[string]bool{}}
}

// Hide marks a registered tool as hidden: it stays callable by name (Get still
// resolves it, so subagents and explicit calls work) but is omitted from
// Schemas() so the main-loop model never sees it in its tool list. Used when a
// tool is meant to be driven only through a subagent skill (e.g. browser tools
// via run_skill("desktop-auto")) rather than advertised to the top-level model.
// Hiding a name that isn't registered is a no-op.
func (r *Registry) Hide(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; ok {
		r.hidden[name] = true
	}
}

// IsHidden reports whether a tool has been hidden from the model's schema list.
func (r *Registry) IsHidden(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hidden[name]
}

// Clone returns an independent copy: same tools, same insertion order, same
// hidden set. A caller that needs to add a run-scoped tool must clone first —
// mutating the live registry would leak that tool into every other session,
// including the main one, whose tool list is part of the cached prompt prefix.
func (r *Registry) Clone() *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := &Registry{
		tools:  make(map[string]Tool, len(r.tools)),
		order:  make([]string, len(r.order)),
		canon:  make(map[string]json.RawMessage, len(r.canon)),
		hidden: make(map[string]bool, len(r.hidden)),
	}
	copy(out.order, r.order)
	maps.Copy(out.tools, r.tools)
	maps.Copy(out.canon, r.canon)
	maps.Copy(out.hidden, r.hidden)
	return out
}

// VisibleCount returns the number of tools that will be sent to the model
// (total registered minus hidden). Used for startup logging so the operator
// can verify the Hide list is taking effect.
func (r *Registry) VisibleCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, name := range r.order {
		if !r.hidden[name] {
			count++
		}
	}
	return count
}

// Add inserts (or replaces) a tool, preserving first-seen order. The schema is
// canonicalized once here — it never changes after registration, so Schemas()
// (called every turn) reuses the result instead of re-marshaling.
func (r *Registry) Add(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := t.Name()
	if _, ok := r.tools[name]; !ok {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
	r.canon[name] = provider.CanonicalizeSchema(t.Schema())
}

// MCPNamePrefix is the namespace every MCP tool name carries: the
// model-visible name is "mcp__<server>__<tool>".
const MCPNamePrefix = "mcp__"

// SplitMCPName splits a model-visible MCP tool name "mcp__<server>__<tool>" into
// its server and tool parts. ok is false for non-MCP (built-in) names and for
// malformed names missing either part.
func SplitMCPName(name string) (server, tool string, ok bool) {
	if !strings.HasPrefix(name, MCPNamePrefix) {
		return "", "", false
	}
	rest := name[len(MCPNamePrefix):]
	parts := strings.SplitN(rest, "__", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// RemovePrefix unregisters every tool whose name starts with prefix — used to
// drop an MCP server's "mcp__<server>__" namespace when it's disconnected — and
// returns the count removed.
func (r *Registry) RemovePrefix(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	kept := r.order[:0]
	removed := 0
	for _, name := range r.order {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
			delete(r.canon, name)
			removed++
			continue
		}
		kept = append(kept, name)
	}
	r.order = kept
	return removed
}

// Get looks up a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.tools[name]
	return t, ok
}

// Len returns the number of registered tools.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.order)
}

// Names returns the registered tool names in insertion order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Schemas exports tool definitions in stable name order for the provider.
func (r *Registry) Schemas() []provider.ToolSchema {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, len(r.order))
	copy(names, r.order)
	sort.Strings(names)

	out := make([]provider.ToolSchema, 0, len(names))
	for _, name := range names {
		if r.hidden[name] {
			continue
		}
		t := r.tools[name]
		if t == nil {
			continue
		}
		out = append(out, provider.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  r.canon[name],
		})
	}
	return out
}
