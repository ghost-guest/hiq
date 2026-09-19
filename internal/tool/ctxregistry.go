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

// LedgerEditor is the session-side seam the task_ledger tool writes through:
// the agent implements it so the tool can edit the session's durable task
// ledger without the tool package depending on the agent package (same
// inversion as the python_cell host registry above).
type LedgerEditor interface {
	// UpdateLedger sets or appends content under one ledger section and
	// returns a short confirmation. section is one of the canonical ledger
	// headings (case-insensitive alias accepted); mode is "replace" or
	// "append".
	UpdateLedger(section, content, mode string) (string, error)
	// LedgerSnapshot returns the current ledger body (the text between the
	// task-ledger tags, tag wrapper excluded).
	LedgerSnapshot() string
}

type ledgerEditorKey struct{}

// WithLedgerEditor stamps ctx with the executing session's ledger editor.
// The agent sets it per call in executeOne, alongside the registry.
func WithLedgerEditor(ctx context.Context, e LedgerEditor) context.Context {
	return context.WithValue(ctx, ledgerEditorKey{}, e)
}

// LedgerEditorFrom returns the stamped ledger editor, if any (ok is false for
// plain contexts — tests or calls outside the run loop).
func LedgerEditorFrom(ctx context.Context) (LedgerEditor, bool) {
	e, ok := ctx.Value(ledgerEditorKey{}).(LedgerEditor)
	return e, ok && e != nil
}

// RegistryFrom returns the stamped registry, if any (ok is false for plain
// contexts — tests or calls outside the run loop).
func RegistryFrom(ctx context.Context) (*Registry, bool) {
	r, ok := ctx.Value(registryKey{}).(*Registry)
	return r, ok && r != nil
}
