// Package deferred makes background-task results *push* into the session
// instead of waiting to be pulled.
//
// Background jobs (jobs.Manager) already accumulate a one-line completion
// summary that the controller folds into the NEXT turn (DrainCompletedNote).
// That pull model has three holes this package closes:
//
//  1. The user only learns what a job produced when the model next runs — a
//     long-running task that fails while the user is away stays invisible.
//  2. The drained note carries no output, so the model cannot act on the
//     failure it is told about.
//  3. Nothing survives a process restart: a result produced just before the
//     app closed is simply gone.
//
// The design mirrors a proven shape from a sibling agent product
// (openhanako's DeferredResultCoordinator), adapted to hiq's primitives:
//
//   - A task is a durable record (Store) with a lifecycle
//     (pending → resolved/failed/aborted) and delivery bookkeeping
//     (delivered / suppressed), persisted per session so it survives a crash.
//   - Delivery has a POLICY, not a hardcoded behavior: a task may start a new
//     turn (push), only surface a notice, or be handled elsewhere (external).
//   - Delivery is idempotent and retried on a ticker; a task whose parent
//     session is gone is suppressed rather than delivered into a dead session.
//
// The coordinator never talks to the agent directly: the host implements
// Deliverer (control.Controller does) so this package stays free of
// session/agent dependencies.
package deferred
