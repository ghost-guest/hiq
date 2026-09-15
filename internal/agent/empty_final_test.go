package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/zzycxz/fairpeer/internal/event"
	"github.com/zzycxz/fairpeer/internal/provider"
	"github.com/zzycxz/fairpeer/internal/tool"
)

// TestRunAcceptsReasoningOnlyFinalAnswer pins the harness-style default: with
// Options{} (RequireVisibleFinal unset) a reasoning-only clean stop is a valid
// terminal — the harness must NOT inject a synthetic visible-answer nudge. Only
// callers that explicitly require visible output opt into the retry.
func TestRunAcceptsReasoningOnlyFinalAnswer(t *testing.T) {
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{
			{Type: provider.ChunkReasoning, Text: "I should answer the user."},
			{Type: provider.ChunkDone},
		},
	}}
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{}, event.Discard)

	if err := a.Run(context.Background(), "answer me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if prov.call != 1 {
		t.Fatalf("provider calls = %d, want one clean reasoning-only completion", prov.call)
	}
	if got := lastAssistantContent(a.session); got != "" {
		t.Fatalf("last assistant content = %q, want empty content beside reasoning", got)
	}
	if sessionHasUserMessageContaining(a.session, "visible answer") {
		t.Fatal("must not inject a synthetic visible-answer retry when the caller does not require a visible final")
	}
}

// TestRunRetriesReasoningOnlyFinalWhenVisibleFinalRequired keeps the old retry
// behaviour but scopes it to the case that still triggers it: a caller that
// sets Options.RequireVisibleFinal (sub-agents, guardian, the interactive loop).
func TestRunRetriesReasoningOnlyFinalWhenVisibleFinalRequired(t *testing.T) {
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{
			{Type: provider.ChunkReasoning, Text: "I should answer the user."},
			{Type: provider.ChunkDone},
		},
		{
			{Type: provider.ChunkText, Text: "visible reply"},
			{Type: provider.ChunkDone},
		},
	}}
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{RequireVisibleFinal: true}, event.Discard)

	if err := a.Run(context.Background(), "answer me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if prov.call != 2 {
		t.Fatalf("provider calls = %d, want retry after reasoning-only answer", prov.call)
	}
	if got := lastAssistantContent(a.session); got != "visible reply" {
		t.Fatalf("last assistant content = %q, want visible reply", got)
	}
	if !sessionHasUserMessageContaining(a.session, "visible answer") {
		t.Fatal("missing synthetic visible-answer retry message")
	}
}

// TestRunStopsAfterRepeatedEmptyFinalAnswers pins the bounded retry ceiling
// when a visible final is explicitly required.
func TestRunStopsAfterRepeatedEmptyFinalAnswers(t *testing.T) {
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{{Type: provider.ChunkReasoning, Text: "thinking 1"}, {Type: provider.ChunkDone}},
		{{Type: provider.ChunkReasoning, Text: "thinking 2"}, {Type: provider.ChunkDone}},
		{{Type: provider.ChunkReasoning, Text: "thinking 3"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{RequireVisibleFinal: true}, event.Discard)

	err := a.Run(context.Background(), "answer me")
	if err == nil {
		t.Fatal("expected repeated empty final answers to stop the run")
	}
	if !strings.Contains(err.Error(), "visible final answer") {
		t.Fatalf("error = %v, want visible final answer", err)
	}
	if prov.call != maxEmptyFinalBlocks {
		t.Fatalf("provider calls = %d, want %d bounded empty-answer attempts", prov.call, maxEmptyFinalBlocks)
	}
}

func lastAssistantContent(s *Session) string {
	var out string
	for _, m := range s.Messages {
		if m.Role == provider.RoleAssistant {
			out = provider.ContentString(m.Content)
		}
	}
	return out
}

func BenchmarkHasVisibleFinalAnswer(b *testing.B) {
	cases := []struct {
		name string
		text string
	}{
		{"normal", "visible reply"},
		{"leading-space", strings.Repeat(" ", 256) + "visible reply"},
		{"all-space", strings.Repeat(" \n\t", 256)},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			var got bool
			for i := 0; i < b.N; i++ {
				got = hasVisibleFinalAnswer(tc.text)
			}
			_ = got
		})
	}
}
