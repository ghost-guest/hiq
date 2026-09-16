package event

// StreamAttemptAction classifies one local sampling-attempt lifecycle step.
type StreamAttemptAction string

const (
	StreamAttemptBegin   StreamAttemptAction = "begin"
	StreamAttemptDiscard StreamAttemptAction = "discard"
	StreamAttemptCommit  StreamAttemptAction = "commit"
)

// StreamAttemptInfo is the host-local payload of a StreamAttempt event. IDs are
// never provider-visible; the information exists so a durable ledger can
// reconstruct retry ladders without parsing notice text.
//
// Ported from DeepSeek-Reasonix internal/event (upstream defines this in
// event.go; kept here in a standalone file so fairpeer's own event.go stays a
// minimal, merge-friendly superset).
type StreamAttemptInfo struct {
	ID      string
	Action  StreamAttemptAction
	Attempt int // 1-based attempt number
	Max     int // total attempts including the first (typically 6)
	Reason  string
}
