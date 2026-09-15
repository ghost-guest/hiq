// This file is a fairpeer-specific addition to the agent package. Everything
// else in this package predates or is ported from upstream Reasonix; keeping the
// extra status surface in its own file means a future re-port (a straight file
// copy) never overwrites it.
package agent

// ContextMaintenanceSnapshot is a read-only view of the context the agent will
// hand to the provider, so callers (the guardian, the desktop Context Panel) can
// tell whether a step altered it. The field set is fairpeer's projection of the
// upstream Reasonix snapshot: upstream derives ProjectionVersion from its
// projection sidecar, fairpeer from the session rewrite version, which is bumped
// on exactly the same events — a compaction fold, a rewind truncation, a
// guardian merge.
type ContextMaintenanceSnapshot struct {
	// CanonicalTokens estimates the token size of the full message log.
	CanonicalTokens int
	// ProjectedTokens estimates what actually reaches the provider. Equal to
	// CanonicalTokens in fairpeer: the whole log is sent, with only role
	// coalescing applied for strict-alternating providers.
	ProjectedTokens int
	// SummaryTokens estimates the size of the rolling compaction summary, or 0
	// when no fold has happened yet.
	SummaryTokens int
	// TriggerTokens is the context size at which automatic compaction fires.
	// Zero when no context window is configured (compaction disabled).
	TriggerTokens int
	// ProjectionVersion changes whenever the provider-visible projection is
	// rewritten. Callers use it to detect that a step changed the context.
	ProjectionVersion uint64
}

// ContextMaintenanceSnapshot returns the current context-maintenance view. It is
// safe on a nil agent (returns the zero value) so callers need no nil guard.
func (a *Agent) ContextMaintenanceSnapshot() ContextMaintenanceSnapshot {
	if a == nil {
		return ContextMaintenanceSnapshot{}
	}
	sess := a.Session()
	if sess == nil {
		return ContextMaintenanceSnapshot{}
	}
	msgs := sess.Snapshot()
	canonical := estimateMessagesTokens(msgs)
	trigger := 0
	if win := a.effectiveContextWindow(); win > 0 {
		trigger = int(float64(win) * a.compactRatio)
	}
	summary := 0
	if text, idx := latestCompactionSummary(msgs); idx >= 0 && text != "" {
		summary = estimateTextTokens(text)
	}
	return ContextMaintenanceSnapshot{
		CanonicalTokens:   canonical,
		ProjectedTokens:   canonical,
		SummaryTokens:     summary,
		TriggerTokens:     trigger,
		ProjectionVersion: uint64(sess.RewriteVersion()),
	}
}
