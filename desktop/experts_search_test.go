package main

// experts_search_test.go covers the two helpers that make a search-capable
// expert resilient to the agent's step-cap: lastAssistantText recovers a
// usable partial answer from the session when Run hits maxSteps, and
// isMaxStepsPaused distinguishes that benign case from a real failure.
//
// The full mini-agent path (provider + registry + agent loop) is integration-
// level and verified on-device; these helpers are the unit-testable core of the
// "an expert never hard-fails just because it ran out of search steps" guarantee.

import (
	"errors"
	"testing"

	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/provider"
)

func TestLastAssistantText(t *testing.T) {
	// Empty session → no text.
	if got := lastAssistantText(agent.NewSession("")); got != "" {
		t.Errorf("empty session: got %q, want empty", got)
	}
	// System-only session → no assistant text.
	if got := lastAssistantText(agent.NewSession("you are an expert")); got != "" {
		t.Errorf("system-only session: got %q, want empty", got)
	}
	// Session with a trailing assistant answer → returns it.
	sess := agent.NewSession("sys")
	sess.Messages = append(sess.Messages,
		provider.Message{Role: provider.RoleUser, Content: "task"},
		provider.Message{Role: provider.RoleAssistant, Content: "the answer"},
	)
	if got, want := lastAssistantText(sess), "the answer"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Session ending in a tool-call assistant message (no text) followed by an
	// earlier text answer → returns the earlier text (the recovery path for a
	// step-cap mid-search).
	sess2 := agent.NewSession("sys")
	sess2.Messages = append(sess2.Messages,
		provider.Message{Role: provider.RoleUser, Content: "task"},
		provider.Message{Role: provider.RoleAssistant, Content: "partial finding"}, // usable text
		provider.Message{Role: provider.RoleUser, Content: "[tool result]"},
		provider.Message{Role: provider.RoleAssistant, Content: ""}, // tool-call, no text
	)
	if got, want := lastAssistantText(sess2), "partial finding"; got != want {
		t.Errorf("step-cap recovery: got %q, want %q (last non-empty assistant text)", got, want)
	}
	// Whitespace-only assistant messages are skipped.
	sess3 := agent.NewSession("sys")
	sess3.Messages = append(sess3.Messages,
		provider.Message{Role: provider.RoleAssistant, Content: "   \n  "},
	)
	if got := lastAssistantText(sess3); got != "" {
		t.Errorf("whitespace-only assistant: got %q, want empty", got)
	}
}

func TestIsMaxStepsPaused(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"paused exact", errors.New("paused after 4 tool-call rounds (agent.max_steps) — the work so far is saved"), true},
		{"paused partial text", errors.New("sub-agent: paused after 2 tool-call rounds"), true},
		{"provider error", errors.New("connection refused"), false},
		{"context cancel", errors.New("context canceled"), false},
		{"empty", errors.New(""), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isMaxStepsPaused(c.err); got != c.want {
				t.Errorf("isMaxStepsPaused(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
