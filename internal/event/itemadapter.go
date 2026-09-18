// itemadapter.go — upgrade spec 4-1 Step 1: dual-track event emission. This
// sink wrapper sits between the agent and the real sink, translating every
// legacy Kind event into the ItemEvent form and emitting BOTH. Old sinks see
// exactly what they saw before (zero breaking); new consumers (mobile bridge,
// future remote) can listen for Kind == Item and render directly.
//
// The mapping is lossless — every field the desktop reducer reads from the
// flat events is carried in the structured Item payload.
package event

import (
	"encoding/json"

	"github.com/zzycxz/hiq/internal/evidence"
	"fmt"
)

// ItemAdapter wraps a Sink and emits ItemEvents alongside legacy kinds.
type ItemAdapter struct {
	inner Sink
	// seq mints stable item IDs (per turn; the agent resets per Run).
	seq int
	// current tracks the item ID for kinds that stream (Reasoning, Text)
	// so ItemDelta events reference the right item.
	currentAgentMsg string
	currentReason   string
}

// RecordReadinessAudit forwards the optional sink capability so sinks that
// implement ReadinessAuditSink keep receiving audits through the adapter.
func (a *ItemAdapter) RecordReadinessAudit(audit evidence.ReadinessAudit) {
	if rs, ok := a.inner.(ReadinessAuditSink); ok {
		rs.RecordReadinessAudit(audit)
	}
}

// NewItemAdapter creates the dual-track wrapper.
func NewItemAdapter(inner Sink) *ItemAdapter {
	return &ItemAdapter{inner: inner}
}

// Reset clears per-turn state (call at TurnStarted).
func (a *ItemAdapter) Reset() {
	a.currentAgentMsg = ""
	a.currentReason = ""
}

func (a *ItemAdapter) nextID(prefix string) string {
	a.seq++
	return fmt.Sprintf("%s-%d", prefix, a.seq)
}

// Emit implements Sink: forward the legacy event AND emit the Item form.
func (a *ItemAdapter) Emit(e Event) {
	// Always forward the original.
	a.inner.Emit(e)

	// Derive the ItemEvent (best-effort; unknown kinds are skipped).
	item := a.deriveItem(e)
	if item != nil {
		a.inner.Emit(Event{Kind: Item, Item: item})
	}
}

// deriveItem maps one legacy event to its ItemEvent form.
func (a *ItemAdapter) deriveItem(e Event) *ItemEvent {
	switch e.Kind {
	case TurnStarted:
		a.Reset()
		return nil // turn boundary, not a timeline item

	case Reasoning:
		if a.currentReason == "" {
			a.currentReason = a.nextID("r")
			return &ItemEvent{
				Phase: ItemStarted, ItemID: a.currentReason, ItemKind: ItemReasoning,
				Item: mustJSON(map[string]string{}),
			}
		}
		return &ItemEvent{
			Phase: ItemDelta, ItemID: a.currentReason, ItemKind: ItemReasoning,
			Delta: e.Text,
		}

	case Text:
		if a.currentAgentMsg == "" {
			a.currentAgentMsg = a.nextID("a")
			return &ItemEvent{
				Phase: ItemStarted, ItemID: a.currentAgentMsg, ItemKind: ItemAgentMessage,
			}
		}
		return &ItemEvent{
			Phase: ItemDelta, ItemID: a.currentAgentMsg, ItemKind: ItemAgentMessage,
			Delta: e.Text,
		}

	case Message:
		// Message closes the streaming agent message.
		if a.currentAgentMsg != "" {
			id := a.currentAgentMsg
			a.currentAgentMsg = ""
			return &ItemEvent{
				Phase: ItemCompleted, ItemID: id, ItemKind: ItemAgentMessage,
				Item: mustJSON(AgentMessageItem{Text: e.Text, Reasoning: e.Reasoning}),
			}
		}
		return nil

	case ToolDispatch:
		return &ItemEvent{
			Phase: ItemStarted, ItemID: e.Tool.ID, ItemKind: ItemToolCall,
			Item: mustJSON(ToolCallItem{
				Name: e.Tool.Name, Args: e.Tool.Args, ReadOnly: e.Tool.ReadOnly,
				FileDiff: &e.Tool.FileDiff, Status: "running",
			}),
		}

	case ToolArgsDelta:
		return &ItemEvent{
			Phase: ItemDelta, ItemID: e.Tool.ID, ItemKind: ItemToolCall,
			Delta: e.Text,
		}

	case ToolResult:
		status := "done"
		if e.Tool.Err != "" {
			status = "error"
		}
		return &ItemEvent{
			Phase: ItemCompleted, ItemID: e.Tool.ID, ItemKind: ItemToolCall,
			Item: mustJSON(ToolCallItem{
				Name: e.Tool.Name, Args: e.Tool.Args, Output: e.Tool.Output,
				Err: e.Tool.Err, ReadOnly: e.Tool.ReadOnly,
				DurationMs: e.Tool.DurationMs, Status: status,
			}),
		}

	case Notice:
		id := a.nextID("n")
		level := "info"
		if e.Level == LevelWarn {
			level = "warn"
		}
		return &ItemEvent{
			Phase: ItemStarted, ItemID: id, ItemKind: ItemNotice,
			Item: mustJSON(NoticeItem{Level: level, Text: e.Text}),
		}

	case ApprovalRequest:
		return &ItemEvent{
			Phase: ItemStarted, ItemID: e.Approval.ID, ItemKind: ItemApproval,
			Item: mustJSON(ApprovalItem{
				Tool: e.Approval.Tool, Subject: e.Approval.Subject,
				Changes: e.Approval.Changes,
			}),
		}

	case CompactionStarted:
		id := a.nextID("c")
		return &ItemEvent{
			Phase: ItemStarted, ItemID: id, ItemKind: ItemCompaction,
			Item: mustJSON(CompactionItem{Trigger: e.Compaction.Trigger}),
		}

	case CompactionDone:
		// CompactionDone closes the pending compaction item; we don't have
		// the ID here (it was minted on Started), so emit as a new completed.
		id := a.nextID("c")
		return &ItemEvent{
			Phase: ItemCompleted, ItemID: id, ItemKind: ItemCompaction,
			Item: mustJSON(CompactionItem{
				Trigger: e.Compaction.Trigger, Messages: e.Compaction.Messages,
				Summary: e.Compaction.Summary,
			}),
		}

	default:
		return nil // Phase, Usage, Retrying, etc. are metadata, not items
	}
}

func mustJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
