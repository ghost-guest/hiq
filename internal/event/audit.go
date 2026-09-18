package event

import (
	"github.com/zzycxz/hiq/internal/evidence"
	"github.com/zzycxz/hiq/internal/nilutil"
)

// AnchorSafetyAudit is a content-free shadow decision for an anchor-based
// writer. It contains only bounded enums/counts; paths, anchors, source text,
// and digests never leave the host-side observation ledger.
type AnchorSafetyAudit struct {
	Mode                  string
	TaskMode              string
	RangeLines            int
	ObservationAge        int
	LegacyAllowed         bool
	ShadowAllowed         bool
	Reason                string
	SameBatchReadRejected bool
}

type AnchorSafetyAuditSink interface {
	RecordAnchorSafetyAudit(AnchorSafetyAudit)
}

func RecordAnchorSafetyAudit(s Sink, a AnchorSafetyAudit) {
	if nilutil.IsNil(s) {
		return
	}
	if as, ok := s.(AnchorSafetyAuditSink); ok {
		as.RecordAnchorSafetyAudit(a)
	}
}

// TurnCompletionSink is an optional sink capability for synchronous controller
// entry points that do not publish a TurnDone UI event. It keeps accounting
// independent from frontend event lifecycles without synthesizing an event that
// transports may mistake for an interactive completion.
type TurnCompletionSink interface {
	RecordTurnCompletion()
}

// RecordTurnCompletion records one successfully admitted top-level controller
// run on sinks that opt into completion accounting.
func RecordTurnCompletion(s Sink) {
	if nilutil.IsNil(s) {
		return
	}
	if ts, ok := s.(TurnCompletionSink); ok {
		ts.RecordTurnCompletion()
	}
}

// ProtocolRecoveryKind is a content-free internal observation about a provider
// protocol repair. It is deliberately separate from Event/Notice so recovery
// stays invisible in chat transcripts and frontends do not need to understand
// provider implementation details.
type ProtocolRecoveryKind string

const (
	ProtocolRecoveryMissingReasoningDetected        ProtocolRecoveryKind = "missing_reasoning_detected"
	ProtocolRecoveryMissingReasoningRetryAttempted  ProtocolRecoveryKind = "missing_reasoning_retry_attempted"
	ProtocolRecoveryMissingReasoningRetryRecovered  ProtocolRecoveryKind = "missing_reasoning_retry_recovered"
	ProtocolRecoveryMissingReasoningRetryReplaced   ProtocolRecoveryKind = "missing_reasoning_retry_replaced_response"
	ProtocolRecoveryMissingReasoningRetrySuppressed ProtocolRecoveryKind = "missing_reasoning_retry_suppressed"
	ProtocolRecoveryMissingReasoningFallback        ProtocolRecoveryKind = "missing_reasoning_fallback_used"
	ProtocolRecoveryReasoningOverflowDetected       ProtocolRecoveryKind = "reasoning_overflow_detected"
	ProtocolRecoveryClientToolRejected              ProtocolRecoveryKind = "client_tool_rejected_unreplayable_reasoning"
	ProtocolRecoveryServerSearchSalvaged            ProtocolRecoveryKind = "server_search_history_salvaged"
	ProtocolRecoveryHistoryRepaired                 ProtocolRecoveryKind = "unreplayable_history_repaired"
	ProtocolRecoveryReasoningReplay400Detected      ProtocolRecoveryKind = "reasoning_replay_400_detected"
	ProtocolRecoveryReasoningReplay400Recovered     ProtocolRecoveryKind = "reasoning_replay_400_recovered"
)

type ProtocolRecoveryAudit struct {
	Kind ProtocolRecoveryKind
}

// ContractShadowAudit is the shadow task-contract's end-of-turn summary:
// counts and enums only, never requirement text. Shadow means observed, not
// enforced — the old control logic still decides behavior.
type ContractShadowAudit struct {
	Intent                string
	Requirements          int
	RequirementsSatisfied int
	Checks                int
	ChecksSatisfied       int
	Epoch                 uint64
	Verdict               string
	Complete              bool
	ReadyToFinalize       bool
}

// ContractShadowAuditSink is an optional sink capability; implementations
// must keep it content-free, like every other audit channel.
type ContractShadowAuditSink interface {
	RecordContractShadow(ContractShadowAudit)
}

// RecordContractShadow forwards the shadow contract summary only to sinks
// that explicitly opt in. Ordinary UI sinks receive nothing.
func RecordContractShadow(s Sink, a ContractShadowAudit) {
	if nilutil.IsNil(s) {
		return
	}
	if cs, ok := s.(ContractShadowAuditSink); ok {
		cs.RecordContractShadow(a)
	}
}

// CompletionReportAudit is the host-authored completion report's end-of-turn
// summary: counts, enums, and gap kinds only, never paths or command text.
// The gap counters carry the point — what the turn left unproven.
type CompletionReportAudit struct {
	Verdict             string
	Risk                string
	Criteria            int
	CriteriaSatisfied   int
	Changes             int
	ChangesUnreviewed   int
	Verifications       int
	VerificationsFailed int
	VerificationsStale  int
	Gaps                int
	GapKinds            []string
	// ClaimsVerified counts the turn's own asserted verifications;
	// ClaimsUnbacked is how many of them the ledger did not support.
	ClaimsVerified int
	ClaimsUnbacked int
}

// CompletionReportAuditSink is an optional sink capability; implementations
// must keep it content-free, like every other audit channel.
type CompletionReportAuditSink interface {
	RecordCompletionReport(CompletionReportAudit)
}

// RecordCompletionReport forwards the completion summary only to sinks that
// explicitly opt in. Ordinary UI sinks receive nothing.
func RecordCompletionReport(s Sink, a CompletionReportAudit) {
	if nilutil.IsNil(s) {
		return
	}
	if cs, ok := s.(CompletionReportAuditSink); ok {
		cs.RecordCompletionReport(a)
	}
}

// MemoryRecallAudit summarizes one automatic-recall decision: identifiers,
// scores, and budget numbers only — never the query or fact text.
type MemoryRecallAudit struct {
	Hits       []MemoryRecallHit
	UsedChars  int
	Omitted    int
	Suppressed string // reason recall stayed silent; "" when hits were injected
	// Shadow is the Retrieval V2 ranking (telemetry only, never served).
	Shadow []MemoryRecallHit
}

// MemoryRecallHit is one recalled fact's content-free fingerprint.
type MemoryRecallHit struct {
	ID        string
	Revision  int
	Scope     string
	Type      string
	Freshness string
	Score     float64
}

// MemoryRecallSink is an optional sink capability; implementations must keep
// it content-free, like every other audit channel.
type MemoryRecallSink interface {
	RecordMemoryRecall(MemoryRecallAudit)
}

// RecordMemoryRecall forwards a recall decision only to sinks that explicitly
// opt in. Ordinary UI sinks receive nothing.
func RecordMemoryRecall(s Sink, a MemoryRecallAudit) {
	if nilutil.IsNil(s) {
		return
	}
	if mr, ok := s.(MemoryRecallSink); ok {
		mr.RecordMemoryRecall(a)
	}
}

// DelegationAdmissionAudit is the shadow admission verdict for one expensive
// delegation call: tool name and enums only, never the query or prompt text.
// Shadow means observed, not enforced — no call is blocked.
type DelegationAdmissionAudit struct {
	Tool    string
	Verdict string // "allow" | "deny"
	Reason  string // e.g. "local_fix_no_external_need"
	Intent  string // compatibility field; no longer classified from prompt text
}

// DelegationAdmissionSink is an optional sink capability; implementations
// must keep it content-free, like every other audit channel.
type DelegationAdmissionSink interface {
	RecordDelegationAdmission(DelegationAdmissionAudit)
}

// RecordDelegationAdmission forwards a shadow admission verdict only to sinks
// that explicitly opt in. Ordinary UI sinks receive nothing.
func RecordDelegationAdmission(s Sink, a DelegationAdmissionAudit) {
	if nilutil.IsNil(s) {
		return
	}
	if da, ok := s.(DelegationAdmissionSink); ok {
		da.RecordDelegationAdmission(a)
	}
}

// OutcomeProgressSink is an optional sink capability for the shadow outcome
// scorer's per-round samples: counts only, never paths or commands. Shadow
// means observed, not enforced — the novelty guard still decides behavior.
type OutcomeProgressSink interface {
	RecordOutcomeProgress(evidence.OutcomeSample)
}

// RecordOutcomeProgress forwards a shadow outcome sample only to sinks that
// explicitly opt in. Ordinary UI sinks receive nothing.
func RecordOutcomeProgress(s Sink, sample evidence.OutcomeSample) {
	if nilutil.IsNil(s) {
		return
	}
	if op, ok := s.(OutcomeProgressSink); ok {
		op.RecordOutcomeProgress(sample)
	}
}

// ProtocolRecoveryAuditSink is an optional sink capability. Implementations
// must keep it content-free; prompts, responses, endpoints, model names, and
// tool arguments do not belong in this audit channel.
type ProtocolRecoveryAuditSink interface {
	RecordProtocolRecovery(ProtocolRecoveryAudit)
}

// RecordProtocolRecovery forwards a content-free recovery observation only to
// sinks that explicitly opt in. Ordinary UI sinks receive nothing.
func RecordProtocolRecovery(s Sink, a ProtocolRecoveryAudit) {
	if nilutil.IsNil(s) {
		return
	}
	if rs, ok := s.(ProtocolRecoveryAuditSink); ok {
		rs.RecordProtocolRecovery(a)
	}
}
