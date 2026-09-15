// Package agent wires a Provider, a tool Registry, and a Session into the
// harness loop that drives a coding task to completion.
package agent

import (
	"sync"

	"github.com/zzycxz/fairpeer/internal/provider"
)

// Session holds the conversation history for one task. The run loop (one turn at
// a time) is the only writer, but a frontend can read History/Save from another
// goroutine while a turn appends, so mu guards Messages. Direct Messages reads on
// the run-loop goroutine stay lock-free (serial with its own writes); cross-
// goroutine access goes through Snapshot.
type Session struct {
	mu             sync.RWMutex
	Messages       []provider.Message
	rewriteVersion int // bumped each time the log is rewritten (compact/fold)
	// pendingContentReasons records why the provider-visible content changed
	// since the last drain, for cache-diagnostics attribution.
	pendingContentReasons []string
}

// NewSession initializes a session with an optional system prompt.
func NewSession(system string) *Session {
	s := &Session{}
	if system != "" {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleSystem, Content: system})
	}
	return s
}

// Add appends a message.
func (s *Session) Add(m provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, m)
}

// Replace swaps the whole message log — used by compaction, which rewrites the
// middle of the history.
func (s *Session) Replace(msgs []provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = msgs
}

// Snapshot returns a copy of the messages, safe to read from another goroutine
// while a turn appends. Frontends (History, Save) use it instead of touching the
// live slice.
func (s *Session) Snapshot() []provider.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]provider.Message(nil), s.Messages...)
}

// Len reports the number of messages in the log.
func (s *Session) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Messages)
}

// RewriteVersion returns the current rewrite version.
func (s *Session) RewriteVersion() int { return s.rewriteVersion }

// IncrementRewrite bumps the rewrite version by 1.
func (s *Session) IncrementRewrite() { s.rewriteVersion++ }

// Rewrite atomically replaces the message log and bumps the rewrite version —
// the shape a caller must use when it changed provider-visible content in place
// (a compaction fold, a guardian merge, a rewind truncation). reason names the
// change and is queued for the next DrainContentRewriteReasons call, which
// feeds cache-diagnostics attribution. Callers whose edit only touches
// local-only display metadata must use Replace instead, so they never report a
// cache-prefix change that did not happen.
func (s *Session) Rewrite(msgs []provider.Message, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = msgs
	s.rewriteVersion++
	if reason != "" {
		s.pendingContentReasons = append(s.pendingContentReasons, reason)
	}
}

// DrainContentRewriteReasons returns the queued rewrite reasons and clears the
// queue. It is safe to call on a session that never rewrote.
func (s *Session) DrainContentRewriteReasons() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pendingContentReasons) == 0 {
		return nil
	}
	out := s.pendingContentReasons
	s.pendingContentReasons = nil
	return out
}

// HasContent returns true when the session carries at least one user,
// assistant, or tool message — i.e. more than just a system prompt. An
// "empty" conversation that has never been used should not be persisted.
func (s *Session) HasContent() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.Messages {
		if m.Role != provider.RoleSystem {
			return true
		}
	}
	return false
}
