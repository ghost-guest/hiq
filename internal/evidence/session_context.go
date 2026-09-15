// This file is a fairpeer-specific addition to the evidence package. Everything
// else in this directory is ported verbatim from upstream Reasonix; keeping the
// extra API in its own file means a future re-port (a straight file copy) never
// overwrites it.
package evidence

import (
	"context"

	"github.com/zzycxz/fairpeer/internal/provider"
)

// sessionMessagesKey is the context key under which WithSessionMessages stores
// the conversation transcript.
type sessionMessagesKey struct{}

// WithSessionMessages attaches the full conversation history so step-evidence
// verification can fall back to scanning the transcript when the per-turn
// ledger misses a command (cross-turn references, non-bash tool calls,
// truncated command strings).
func WithSessionMessages(ctx context.Context, msgs []provider.Message) context.Context {
	return context.WithValue(ctx, sessionMessagesKey{}, msgs)
}

// SessionMessagesFromContext retrieves the conversation history attached by
// WithSessionMessages.
func SessionMessagesFromContext(ctx context.Context) ([]provider.Message, bool) {
	msgs, ok := ctx.Value(sessionMessagesKey{}).([]provider.Message)
	return msgs, ok
}
