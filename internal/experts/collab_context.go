package experts

// collab_context.go provides the context-layer transform for expert-collab
// messages. It is intentionally in the experts package (where the record type
// lives) rather than in the agent package, so the agent can call a single
// pure function without importing experts' types beyond this one hook.
//
// Usage: the agent's stream() builds the LLM request from session.Messages.
// Before handing them to the provider it calls CollabContextMessages(msgs),
// which returns a slice where every expert-collab tool message has its Content
// replaced by the record's synthesis-only summary. The stored session is
// untouched — this is a read-side projection only.

import (
	"github.com/zzycxz/fairpeer/internal/provider"
)

// CollabContextMessages returns a copy of msgs in which every persisted
// expert-collab tool message (Name == ExpertCollabToolName) is rewritten so
// its Content carries only the synthesis summary instead of the full JSON
// transcript. Non-collab messages are returned unchanged. The input slice is
// not mutated.
//
// If a collab message's Content fails to parse as a record (corrupt JSON,
// missing marker), it is left intact — better to send a large-but-valid tool
// result than to drop or mangle it.
func CollabContextMessages(msgs []provider.Message) []provider.Message {
	rewrote := false
	out := make([]provider.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		if m.Role != provider.RoleTool {
			continue
		}
		if m.Name != ExpertCollabToolName {
			continue
		}
		if s := m.Content; s != "" {
			if rec, ok := ParseCollabRecord(s); ok {
				out[i].Content = rec.ContextSummary()
				rewrote = true
			}
		}
	}
	// Return the original slice when nothing changed, so callers that compare
	// by identity (or just want to avoid an allocation on the hot path when
	// there are no collab messages at all) aren't surprised.
	if !rewrote {
		return msgs
	}
	return out
}
