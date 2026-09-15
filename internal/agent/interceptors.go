// interceptors.go — upgrade spec 3-11: the agent's built-in guardrails as a
// composable interceptor chain instead of inline code in Run. The main loop
// stays a pure state machine (stream → tools → loop); policy decisions live
// here where they can be tested, disabled, or extended independently.
//
// Each interceptor sees the turn's state at its decision point and returns
// either "proceed" or a redirect (a user-role nudge message to re-enter the
// loop with). This is deliberately NOT the user-configureable hook system
// (ToolHooks) — these are the harness's own guardrails, always-on unless the
// corresponding feature is disabled.
package agent

import (
	"github.com/zzycxz/fairpeer/internal/event"
	"github.com/zzycxz/fairpeer/internal/provider"
)

// preAnswerState is what the readiness/empty-answer interceptors see after a
// stream completes with no tool calls — the "model is trying to finish" point.
type preAnswerState struct {
	Text        string
	Reasoning   string
	Usage       *provider.Usage
	Step        int
	finalBlocks int
	emptyBlocks int
}

// preAnswerVerdict is an interceptor's decision.
type preAnswerVerdict struct {
	// Block with this reason (non-empty) → re-enter the loop with a nudge.
	BlockReason string
	// Audit applies (only the readiness interceptor sets it).
	Applies bool
}

// proceed is the "no objection" verdict.
var proceed = preAnswerVerdict{}

// readinessInterceptor wraps finalReadinessCheck as a chain member. It enforces
// that the model's final answer is backed by completed todo steps and verified
// host checks — the anti-hallucination gate.
type readinessInterceptor struct {
	agent *Agent
}

func (ri readinessInterceptor) check(s preAnswerState) preAnswerVerdict {
	r := ri.agent.finalReadinessCheck()
	if r.reason != "" {
		return preAnswerVerdict{BlockReason: r.reason, Applies: r.applies}
	}
	return preAnswerVerdict{Applies: r.applies}
}

func (ri readinessInterceptor) allowed(s preAnswerState) preAnswerVerdict {
	r := ri.agent.finalReadinessCheck()
	return preAnswerVerdict{Applies: r.applies}
}

// emptyAnswerInterceptor catches the model finishing with no visible text
// (e.g. it only produced reasoning, or the stream ended early).
type emptyAnswerInterceptor struct {
	agent *Agent
}

func (ei emptyAnswerInterceptor) check(s preAnswerState) preAnswerVerdict {
	// Harness-style termination accepts a reasoning-only clean stop. Only
	// callers that require visible output — sub-agents whose result is parsed,
	// the guardian, and fairpeer's interactive loop, which all set
	// Options.RequireVisibleFinal — keep the bounded synthetic retry that
	// re-prompts the model for an answer the user or caller can actually read.
	if !ei.agent.requireVisibleFinal || hasVisibleFinalAnswer(s.Text) {
		return proceed
	}
	return preAnswerVerdict{BlockReason: "empty-final"}
}

// truncationInterceptor: a "length" finish means tool-call arguments may be
// truncated mid-token. Instead of executing corrupted args, fail every call
// and re-enter the loop.
type truncationInterceptor struct {
	agent *Agent
}

// checkTruncated returns true when the stream was truncated and the calls
// were failed (already persisted); the caller should continue the loop.
func (ti truncationInterceptor) intercepted(calls []provider.ToolCall, usage *provider.Usage) bool {
	if usage == nil || (usage.FinishReason != "length" && usage.FinishReason != "repetition_truncation") || len(calls) == 0 {
		return false
	}
	const skipMsg = "tool call skipped: response was truncated (output ended mid-token), arguments may be incomplete. Re-issue the call in full."
	for _, call := range calls {
		ev := event.Tool{ID: call.ID, Name: call.Name, Args: call.Arguments}
		ti.agent.sink.Emit(event.Event{Kind: event.ToolDispatch, Tool: ev})
		ti.agent.sink.Emit(event.Event{Kind: event.ToolResult, Tool: event.Tool{
			ID:     call.ID,
			Name:   call.Name,
			Args:   call.Arguments,
			Output: skipMsg,
			Err:    "skipped: response truncated",
		}})
		ti.agent.session.Add(provider.Message{
			Role:       provider.RoleTool,
			Content:    skipMsg,
			ToolCallID: call.ID,
			Name:       call.Name,
		})
	}
	return true
}

// interceptorChain is the ordered set of pre-answer guardrails the Run loop
// consults before accepting a final answer.
type interceptorChain struct {
	readiness readinessInterceptor
	empty     emptyAnswerInterceptor
}

func newInterceptorChain(a *Agent) interceptorChain {
	return interceptorChain{
		readiness: readinessInterceptor{agent: a},
		empty:     emptyAnswerInterceptor{agent: a},
	}
}

// checkAll runs every interceptor; the first block wins.
func (ic interceptorChain) checkAll(s preAnswerState) preAnswerVerdict {
	v := ic.readiness.check(s)
	if v.BlockReason != "" {
		return v
	}
	// The readiness interceptor's "proceed" still carries Applies — preserve
	// it for the audit even when the empty interceptor also passes.
	if v2 := ic.empty.check(s); v2.BlockReason != "" {
		return v2
	}
	return v
}

// nudgeFor maps a block reason to the user-role message that re-enters the
// loop. The Run loop uses this to redirect the model.
func nudgeFor(reason string, usage *provider.Usage, provName string, reasoningLen int) string {
	switch reason {
	case "empty-final":
		return emptyFinalRetryMessage()
	default:
		return finalReadinessRetryMessage(reason)
	}
}

// blockNotice is what the user sees in the transcript for a block.
func blockNotice(reason string, usage *provider.Usage, provName string, reasoningLen int) string {
	if reason == "empty-final" {
		return emptyFinalNotice(provName, usage, reasoningLen)
	}
	return "final-answer readiness blocked: " + reason
}
