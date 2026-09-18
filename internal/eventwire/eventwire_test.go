package eventwire

import (
	"errors"
	"testing"

	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/provider"
)

func TestWireRoundTrip(t *testing.T) {
	cases := []event.Event{
		{Kind: event.TurnStarted},
		{Kind: event.Text, Text: "hello"},
		{Kind: event.Reasoning, Reasoning: "thinking..."},
		{Kind: event.Notice, Text: "watch out", Level: event.LevelWarn},
		{
			Kind: event.ToolResult,
			Tool: event.Tool{
				ID: "t1", Name: "bash", Args: `{"command":"ls"}`, Output: "files...",
				DurationMs: 42, ParentID: "p1",
				Attachments: []event.Attachment{{Path: "a.png", Kind: "image"}},
				FileDiff:    event.FileDiff{Diff: "@@", Added: 3, Removed: 1},
				Profile:     &event.Profile{Model: "m", Effort: "high"},
			},
		},
		{
			Kind: event.Usage,
			Usage: &provider.Usage{
				PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
				CacheHitTokens: 4, ReasoningTokens: 2,
			},
			SessionHit: 100, SessionMiss: 7,
		},
		{
			Kind: event.ApprovalRequest,
			Approval: event.Approval{
				ID: "ap1", Tool: "bash", Subject: "rm -rf", Args: "{}",
				Changes: []event.FileChange{{Path: "x.go", Kind: "modify", Added: 1, Removed: 2, Diff: "-+"}},
			},
		},
		{
			Kind: event.AskRequest,
			Ask: event.Ask{
				ID: "q1",
				Questions: []event.AskQuestion{{
					ID: "q1.1", Header: "Pick", Prompt: "Which?",
					Options: []event.AskOption{{Label: "A", Description: "first"}},
					Multi: true,
				}},
			},
		},
		{Kind: event.TurnDone, Err: errors.New("boom")},
		{Kind: event.Retrying, RetryAttempt: 2, RetryMax: 4, RetryAfterMs: 1500},
	}
	for _, e := range cases {
		w := ToWire(e)
		got := FromWire(w)
		if got.Kind != e.Kind {
			t.Errorf("kind %v: round trip kind = %v", e.Kind, got.Kind)
		}
		switch e.Kind {
		case event.Notice:
			if got.Level != e.Level {
				t.Errorf("notice level: got %v want %v", got.Level, e.Level)
			}
		case event.ToolResult:
			if got.Tool.ID != e.Tool.ID || got.Tool.Output != e.Tool.Output ||
				got.Tool.DurationMs != e.Tool.DurationMs || got.Tool.ParentID != e.Tool.ParentID ||
				len(got.Tool.Attachments) != 1 || got.Tool.FileDiff.Added != 3 || got.Tool.Profile == nil {
				t.Errorf("tool round trip: %+v", got.Tool)
			}
		case event.Usage:
			if got.Usage == nil || got.Usage.PromptTokens != 10 || got.Usage.ReasoningTokens != 2 ||
				got.SessionHit != 100 || got.SessionMiss != 7 {
				t.Errorf("usage round trip: %+v %+v", got.Usage, got)
			}
		case event.ApprovalRequest:
			if got.Approval.ID != "ap1" || len(got.Approval.Changes) != 1 || got.Approval.Changes[0].Diff != "-+" {
				t.Errorf("approval round trip: %+v", got.Approval)
			}
		case event.AskRequest:
			if got.Ask.ID != "q1" || len(got.Ask.Questions) != 1 || len(got.Ask.Questions[0].Options) != 1 || !got.Ask.Questions[0].Multi {
				t.Errorf("ask round trip: %+v", got.Ask)
			}
		case event.TurnDone:
			if got.Err == nil || got.Err.Error() != "boom" {
				t.Errorf("turn_done err round trip: %v", got.Err)
			}
		case event.Retrying:
			if got.RetryAttempt != 2 || got.RetryMax != 4 || got.RetryAfterMs != 1500 {
				t.Errorf("retrying round trip: %+v", got)
			}
		}
	}
}
