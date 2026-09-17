// Package patrol gives a session a periodic, self-driven inspection heartbeat:
// while the user is away, it looks at the workspace on its own and reports what
// it finds — the "proactive patrol" idea borrowed from openhanako's per-agent
// heartbeat loop, reimplemented in Go on top of fairpeer's existing pieces.
//
// Patrol is deliberately thin. It does not run an agent, own a scheduler, or
// talk to the network. Each tick it (1) asks a Target supplier which sessions
// are live, (2) runs a small set of read-only Inspectors over each target, and
// (3) hands anything new to the shared deferred.Coordinator as an ordinary
// deferred result. Delivery, durability, retry, crash recovery and the
// user-facing notice therefore all come from internal/deferred for free — the
// same path a finished background job already travels.
//
// # The permission dial (user-configurable)
//
// The feature exists to save the user work, so it must never surprise them. Two
// settings, both in [patrol], scope exactly what patrol may do:
//
//   - mode selects the level of autonomy:
//     off       patrol never runs (the default);
//     readonly  inspect and report only — never writes, never spends a turn;
//     assist    patrol may start a turn so the agent investigates a signal.
//   - allow_write, meaningful only under assist, decides whether an
//     assist-mode turn may modify files. When false (the default) the patrol
//     message tells the agent to investigate and report, not to edit; the normal
//     permission gate still applies either way, so this is an extra ceiling on
//     top of the tool policy rather than a bypass of it.
//
// The mapping onto delivery is direct and auditable: readonly enqueues with
// IntentNotifyUIOnly (the report rides the next turn's context, nothing is
// spent now); assist enqueues with IntentTriggerTurn (the report becomes its
// own turn subject to the session's deferred policy).
//
// # State and idempotence
//
// The "did this already get reported?" memory is an in-process map keyed by
// (session, inspector, finding id) holding the last fingerprint seen. A finding
// is fresh only when its fingerprint changed, so a dirty tree left dirty across
// ticks reports once, not every interval. The map is intentionally not
// persisted: after a restart patrol may re-report one already-known signal,
// which is harmless, whereas persisting it would add a second on-disk format to
// keep consistent with the deferred store. The reports themselves ARE durable —
// they land in the deferred store before delivery is attempted.
package patrol
