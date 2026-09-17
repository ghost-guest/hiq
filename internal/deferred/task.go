package deferred

import (
	"fmt"
	"strings"
	"time"
)

// Status is a deferred task's lifecycle state.
type Status string

const (
	// Pending means the producing work has not finished yet.
	Pending Status = "pending"
	// Resolved means the work finished successfully.
	Resolved Status = "resolved"
	// Failed means the work finished with an error.
	Failed Status = "failed"
	// Aborted means the work was cancelled (user stop, process shutdown).
	Aborted Status = "aborted"
)

// Terminal reports whether the status can be delivered.
func (s Status) Terminal() bool {
	return s == Resolved || s == Failed || s == Aborted
}

// DeliveryIntent overrides the policy for one task. The empty value means
// "decide from the status and the session policy".
type DeliveryIntent string

const (
	// IntentAuto lets Policy decide.
	IntentAuto DeliveryIntent = ""
	// IntentTriggerTurn always starts a new turn when the task is delivered.
	IntentTriggerTurn DeliveryIntent = "trigger_parent_turn"
	// IntentNotifyUIOnly only records a notice plus next-turn context; it never
	// spends a turn on its own.
	IntentNotifyUIOnly DeliveryIntent = "notify_ui_only"
	// IntentExternal means another surface (an IM bridge, a notification
	// channel) owns delivery. The task is recorded but never pushed by us.
	IntentExternal DeliveryIntent = "external"
)

// Meta carries per-task delivery hints. Pointer fields distinguish "producer
// said nothing" (nil → session policy) from an explicit true/false.
type Meta struct {
	Intent            DeliveryIntent `json:"intent,omitempty"`
	TriggerParentTurn *bool          `json:"triggerParentTurn,omitempty"`
	NotifyOnFailure   *bool          `json:"notifyOnFailure,omitempty"`
	NotifyOnSuccess   *bool          `json:"notifyOnSuccess,omitempty"`
	// Kind is the producing subsystem ("bash" | "task" | "team" | ...). It
	// feeds the default notify rule: a task job's answer is worth surfacing,
	// a bash job's exit is usually noise.
	Kind string `json:"kind,omitempty"`
	// Label is the human-facing producer name (job label, task title).
	Label string `json:"label,omitempty"`
}

// Task is one deferred result.
type Task struct {
	ID          string `json:"id"`
	SessionPath string `json:"sessionPath"`
	Source      string `json:"source,omitempty"` // "job" | ...
	Title       string `json:"title"`
	Body        string `json:"body,omitempty"`
	Status      Status `json:"status"`
	Meta        Meta   `json:"meta,omitempty"`

	Delivered          bool   `json:"delivered,omitempty"`
	DeliverySuppressed bool   `json:"deliverySuppressed,omitempty"`
	SuppressReason     string `json:"suppressReason,omitempty"`
	Attempts           int    `json:"attempts,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Deliverable reports whether delivery should be attempted at all.
func (t Task) Deliverable() bool {
	if !t.Status.Terminal() {
		return false
	}
	if t.Delivered || t.DeliverySuppressed {
		return false
	}
	return t.Meta.Intent != IntentExternal
}

// Message renders the task as plain text for the model. The title carries the
// signal (what finished, and how); the body carries the output tail; the last
// line tells the model what to do about it, so the text works whether it
// arrives as its own turn or folded into a later one.
func (t Task) Message() string {
	var b strings.Builder
	b.WriteString(t.Title)
	if body := strings.TrimRight(t.Body, "\n"); body != "" {
		b.WriteString("\n\n")
		b.WriteString(body)
	}
	if action := t.actionLine(); action != "" {
		b.WriteString("\n\n")
		b.WriteString(action)
	}
	return b.String()
}

// Wrapped renders the message inside the marker the agent already uses for
// synthetic, host-originated input, so the model reads it as an automated
// notification rather than as something the user asked for.
func (t Task) Wrapped() string {
	var b strings.Builder
	fmt.Fprintf(&b, `<deferred-result source="%s" id="%s" status="%s"`, t.Source, t.ID, t.Status)
	if t.Meta.Label != "" {
		fmt.Fprintf(&b, ` label="%s"`, strings.ReplaceAll(t.Meta.Label, `"`, "'"))
	}
	b.WriteString(">\n")
	b.WriteString(t.Message())
	b.WriteString("\n</deferred-result>")
	return b.String()
}

func (t Task) actionLine() string {
	switch t.Status {
	case Failed, Aborted:
		return "This background result reports a failure. If the current work depends on it, diagnose and fix it now; otherwise note it in one line and continue."
	case Resolved:
		return "This background result is ready. Use it if the current task needs it; do not restart the work that produced it."
	default:
		return ""
	}
}

// Policy holds the session-level delivery defaults, mapped from [deferred]
// config. Zeros are the conservative choice: never start a turn, tell the user
// only about failures.
type Policy struct {
	// TriggerParentTurn lets a finished task start a turn by itself.
	TriggerParentTurn bool
	// NotifyOnFailure / NotifyOnSuccess control the immediate user-facing
	// notice (the task is always carried into the next turn's context).
	NotifyOnFailure bool
	NotifyOnSuccess bool
}

// TriggerTurn decides whether delivering this task should start a new turn.
// Precedence: explicit intent > per-task override > policy.
func (p Policy) TriggerTurn(t Task) bool {
	switch t.Meta.Intent {
	case IntentNotifyUIOnly, IntentExternal:
		return false
	case IntentTriggerTurn:
		return true
	}
	if t.Meta.TriggerParentTurn != nil {
		return *t.Meta.TriggerParentTurn
	}
	return p.TriggerParentTurn && t.Status == Resolved
}

// Notify decides whether the user gets an immediate notice. Failures default
// to yes (a silent failure is the whole problem this package exists to fix);
// successes default to "only when the producer is a task, whose result is the
// point" and otherwise fall to the session policy.
func (p Policy) Notify(t Task) bool {
	if t.Meta.Intent == IntentExternal {
		return false
	}
	if t.Status == Failed || t.Status == Aborted {
		if t.Meta.NotifyOnFailure != nil {
			return *t.Meta.NotifyOnFailure
		}
		return p.NotifyOnFailure
	}
	if t.Meta.NotifyOnSuccess != nil {
		return *t.Meta.NotifyOnSuccess
	}
	if t.Meta.Kind == "task" {
		return true
	}
	return p.NotifyOnSuccess
}
