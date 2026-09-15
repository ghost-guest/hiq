// This file is a fairpeer-specific addition to the agent package. It holds the
// provider-projection helpers ported from upstream Reasonix, adapted to
// fairpeer's provider.Message (which carries Content/Images/Audio rather than
// the upstream Responses-API replay fields). Keeping them in their own file
// means a future re-port never overwrites the adaptation.
package agent

import (
	"strings"

	"github.com/zzycxz/fairpeer/internal/provider"
)

// coalesceProjectionUserRuns keeps provider request copies compatible with
// providers that require strict user/assistant alternation. The stored session
// retains logical user-turn boundaries — compaction, rewind, and range anchors
// all key off them — so only the outbound copy is merged.
func coalesceProjectionUserRuns(msgs []provider.Message) []provider.Message {
	if len(msgs) < 2 {
		return msgs
	}
	out := make([]provider.Message, 0, len(msgs))
	for _, msg := range msgs {
		if len(out) == 0 || msg.Role != provider.RoleUser || out[len(out)-1].Role != provider.RoleUser {
			clone := msg
			clone.Images = append([]string(nil), msg.Images...)
			clone.Audio = append([]provider.InputAudio(nil), msg.Audio...)
			clone.ToolCalls = append([]provider.ToolCall(nil), msg.ToolCalls...)
			clone.MemoryCitations = append([]provider.MemoryCitation(nil), msg.MemoryCitations...)
			out = append(out, clone)
			continue
		}

		prev := &out[len(out)-1]
		if isCompactionSummary(msg) && !isCompactionSummary(*prev) {
			// A digest folds the history that precedes it, so it must be read
			// first: keep the summary text ahead of the retained turns.
			prev.Content = strings.TrimRight(msg.Content, "\n") + "\n\n" + prev.Content
		} else {
			prev.Content = strings.TrimRight(prev.Content, "\n") + "\n\n" + msg.Content
		}
		prev.Images = append(prev.Images, msg.Images...)
		prev.Audio = append(prev.Audio, msg.Audio...)
		prev.ToolCalls = append(prev.ToolCalls, msg.ToolCalls...)
		prev.MemoryCitations = append(prev.MemoryCitations, msg.MemoryCitations...)
	}
	return out
}
