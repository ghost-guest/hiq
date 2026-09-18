package control

import (
	"context"
	"testing"

	"github.com/zzycxz/hiq/internal/event"
)

// TestReplayPendingPromptsReEmitsBlockedApproval proves a tool approval that is
// still blocking the gate is re-emitted on demand, so a frontend that reloaded
// after the original ApprovalRequest can rebuild its modal instead of leaving the
// gate stuck (#3844).
func TestReplayPendingPromptsReEmitsBlockedApproval(t *testing.T) {
	reqs := make(chan event.Approval, 8)
	c := New(Options{Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.ApprovalRequest {
			reqs <- e.Approval
		}
	})})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = gateApprover{c}.Approve(context.Background(), "bash", "go test ./...", nil)
	}()

	first := <-reqs
	if first.Tool != "bash" || first.Subject != "go test ./..." {
		t.Fatalf("first request = %+v, want bash / go test ./...", first)
	}

	c.ReplayPendingPrompts()

	replayed := <-reqs
	// Approval now carries a []FileChange, so the struct isn't directly
	// comparable — compare the fields the replay contract actually promises.
	if replayed.ID != first.ID || replayed.Tool != first.Tool || replayed.Subject != first.Subject ||
		replayed.Args != first.Args || len(replayed.Changes) != len(first.Changes) {
		t.Fatalf("replayed = %+v, want identical re-emit of %+v", replayed, first)
	}

	c.Approve(first.ID, true, false, false)
	<-done
}

// TestReplayPendingPromptsReEmitsBlockedAsk proves the same for a blocked `ask`
// question, including its question payload (which the controller now retains).
func TestReplayPendingPromptsReEmitsBlockedAsk(t *testing.T) {
	asks := make(chan event.Ask, 8)
	c := New(Options{Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.AskRequest {
			asks <- e.Ask
		}
	})})

	questions := []event.AskQuestion{{
		ID:      "q1",
		Header:  "Pick",
		Prompt:  "Which option?",
		Options: []event.AskOption{{Label: "A"}, {Label: "B"}},
	}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Ask(context.Background(), questions)
	}()

	first := <-asks
	c.ReplayPendingPrompts()
	replayed := <-asks

	if replayed.ID != first.ID || len(replayed.Questions) != 1 || replayed.Questions[0].Prompt != "Which option?" {
		t.Fatalf("replayed ask = %+v, want same id and questions as %+v", replayed, first)
	}

	c.AnswerQuestion(first.ID, []event.AskAnswer{{QuestionID: "q1", Selected: []string{"A"}}})
	<-done
}

// TestReplayPendingPromptsNoOpWhenIdle proves replay emits nothing when no prompt
// is outstanding, so a frontend (re)connect on an idle session is silent.
func TestReplayPendingPromptsNoOpWhenIdle(t *testing.T) {
	var count int
	c := New(Options{Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.ApprovalRequest || e.Kind == event.AskRequest {
			count++
		}
	})})

	c.ReplayPendingPrompts()
	if count != 0 {
		t.Fatalf("emitted %d prompts with nothing pending, want 0", count)
	}
}
