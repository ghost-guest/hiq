package tool

import "context"

// registryKey carries the executing session's tool Registry through ctx. The
// python_cell kernel uses it to serve mid-cell host.tool(...) RPCs against the
// same registry the agent dispatched the cell from, so a running cell can call
// read-only built-ins (web_search, grep, ...) without a new agent round-trip.
type registryKey struct{}

// WithRegistry stamps ctx with the registry a tool call was dispatched from.
// The agent sets it per call in executeOne, alongside the other per-call ctx
// values (progress, evidence, jobs).
func WithRegistry(ctx context.Context, r *Registry) context.Context {
	return context.WithValue(ctx, registryKey{}, r)
}

// RegistryFrom returns the stamped registry, if any (ok is false for plain
// contexts — tests or calls outside the run loop).
func RegistryFrom(ctx context.Context) (*Registry, bool) {
	r, ok := ctx.Value(registryKey{}).(*Registry)
	return r, ok && r != nil
}
