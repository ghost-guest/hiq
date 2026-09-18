// Package compose implements hiq's structured development workflow:
// after a plan is approved, a multi-task feature runs through Implement →
// Verify → (Review) with bounded retries, instead of a single execution
// turn that never validates the result.
//
// The runner is a deterministic Go orchestrator (mirroring MiMo-Code's
// compose.js design choice): it drives subagent calls in a fixed phase order
// and gates transitions on structured verify results — it does NOT rely on
// the model to self-navigate the phases. The model's job is to implement and
// fix; the orchestrator's job is to decide when to verify, retry, and stop.
//
// This package is intentionally decoupled from control: the Controller calls
// ShouldCompose to decide whether to hand off, then calls Run inside the
// approved-plan execution window. The runner borrows the controller's
// Runner/Sink/Executor to dispatch turns, but owns its own cross-turn state
// (verify results, attempt counts) because the agent's evidence ledger resets
// every Run (agent.go:630).
package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/event"
)

// MaxImplementAttempts bounds the Implement→Verify retry loop. After this many
// failed verify rounds the run gives up and returns the last failure to the
// caller (the controller marks the plan todos complete regardless, since the
// user approved the plan and the work partially landed).
const MaxImplementAttempts = 3

// MaxReplans bounds how many times the workflow can pause and re-plan when the
// model discovers the approved plan's assumptions are wrong (VERDICT:
// PLAN_INVALID). Each replan is a fresh planning turn + a user approval, so
// this also bounds how many times the user is interrupted for re-approval.
const MaxReplans = 2

// MinTasksForCompose is the task-count threshold above which the controller
// hands off to the compose runner instead of the single-turn execution path.
// Below this, the plan is small enough that one execution turn suffices.
const MinTasksForCompose = 3

// ShouldCompose reports whether an approved plan's seeded todo list is large
// enough to warrant the compose workflow. The controller calls this with the
// JSON string returned by seedPlanTodos to decide which execution path to take.
func ShouldCompose(seededTodosJSON string) bool {
	todos, err := parseSeededTodos(seededTodosJSON)
	if err != nil {
		return false
	}
	return len(todos) >= MinTasksForCompose
}

// seedTodo mirrors the controller's seedTodo struct (controller.go) so we can
// parse the seeded JSON without importing control's unexported type.
type seedTodo struct {
	Content string `json:"content"`
	Status  string `json:"status"`
	Level   int    `json:"level,omitempty"`
}

func parseSeededTodos(seededTodosJSON string) ([]seedTodo, error) {
	if strings.TrimSpace(seededTodosJSON) == "" {
		return nil, nil
	}
	var p struct {
		Todos []seedTodo `json:"todos"`
	}
	if err := json.Unmarshal([]byte(seededTodosJSON), &p); err != nil {
		return nil, err
	}
	return p.Todos, nil
}

// Runner orchestrates the Implement → Verify → (Review) loop for an approved
// plan. It borrows the controller's Runner (to dispatch execution turns) and
// Sink (to surface progress), and holds cross-turn state that the agent's
// per-turn evidence ledger cannot (the ledger resets every Run).
type Runner struct {
	runner          agent.Runner
	sink            event.Sink
	synthesize      func(text string) string                               // Controller.ComposeSynthetic, to wrap nudges
	history         func() string                                          // reads last assistant reply for verdict parsing
	implementNudge  string                                                 // base nudge for the implement phase (plan-approved message)
	requestApproval func(ctx context.Context, reason string) (bool, error) // asks the user to approve a plan change

	// gaveUp is set true when Run exits after exhausting MaxImplementAttempts (or
	// the user declined a replan) without a clean review. Run still returns nil
	// in that case ("not a hard error — the work partially landed"), so the
	// controller uses GaveUp() to decide whether to mark the plan todos
	// completed (clean exit) or leave them in their actual state (partial work).
	// See audit finding C5.
	gaveUp bool
}

// GaveUp reports whether the last Run gave up without a clean review (attempts
// exhausted or replan declined). When true, the work only partially landed and
// the caller should not mark the plan as fully completed.
func (r *Runner) GaveUp() bool { return r.gaveUp }

// NewRunner builds a Runner that borrows the controller's dispatch primitives.
// implementNudge is the plan-approved message the controller would normally send
// (passed in to avoid compose importing control). history returns the last
// assistant reply text so the runner can parse the verify verdict.
// requestApproval is called when the workflow needs the user to confirm a
// mid-run plan revision (VERDICT: PLAN_INVALID); nil disables replanning.
func NewRunner(r agent.Runner, sink event.Sink, synthesizeFn func(string) string, historyFn func() string, implementNudge string, requestApprovalFn func(ctx context.Context, reason string) (bool, error)) *Runner {
	return &Runner{
		runner:          r,
		sink:            sink,
		synthesize:      synthesizeFn,
		history:         historyFn,
		implementNudge:  implementNudge,
		requestApproval: requestApprovalFn,
	}
}

// Run executes the compose workflow for an approved plan. It is called inside
// the controller's approved-plan execution window (approvedPlanAutoApproveTools
// is already true), so writer tools auto-approve during each Implement turn.
//
// The workflow:
//  1. Implement — drive the model to execute the plan (one full runner.Run)
//  2. Verify — ask the model to run the project's tests/build and report
//     pass/fail with evidence (a second runner.Run with a verify nudge)
//  3. If verify failed and attempts remain, feed failures back and retry from 1
//  4. Stop on first green verify, or after MaxImplementAttempts
//  5. PLAN_INVALID: at any verify/review failure, if the model judges the plan's
//     assumptions are wrong (not just a code bug), it emits VERDICT: PLAN_INVALID.
//     The runner pauses, notifies the user, and requests approval to re-plan.
//     On approval, the model re-plans the remaining work and execution resumes.
//     This is bounded by MaxReplans so the user isn't interrupted indefinitely.
//
// The runner does NOT do Review in this MVP (A6 adds it); verify is the gate.
func (r *Runner) Run(ctx context.Context, proposal string, seededTodosJSON string) error {
	if r == nil || r.runner == nil {
		return fmt.Errorf("compose: runner not initialized")
	}
	// Reset the gave-up flag at the start of each Run so GaveUp() reflects only
	// this invocation's outcome (Run may be called multiple times on one Runner).
	r.gaveUp = false

	failures := ""
	skipImplement := false
	replans := 0
	attempt := 0
	for {
		attempt++
		if attempt > MaxImplementAttempts {
			// All implement attempts exhausted. If we still have replan budget,
			// this is the moment to escalate: the repeated failures likely mean
			// the plan is wrong, not just the code. Offer a replan before giving up.
			if replans < MaxReplans && r.requestApproval != nil {
				r.notice("compose: implement attempts exhausted; the plan may need revision")
				approved, err := r.requestApproval(ctx, "Implement failed "+fmt.Sprintf("%d", MaxImplementAttempts)+" times. The plan's assumptions may be wrong. Re-plan the remaining work?")
				if err != nil {
					return fmt.Errorf("compose replan approval: %w", err)
				}
				if approved {
					replans++
					r.notice(fmt.Sprintf("compose: re-planning (replan %d/%d)", replans, MaxReplans))
					if err := r.replan(ctx); err != nil {
						return fmt.Errorf("compose replan %d: %w", replans, err)
					}
					attempt = 0 // reset implement counter for the new plan
					failures = ""
					continue
				}
			}
			break
		}

		// Phase 1: Implement.
		if !skipImplement {
			r.notice(fmt.Sprintf("compose: implement attempt %d/%d", attempt, MaxImplementAttempts))
			implementNudge := r.implementNudge
			if failures != "" {
				implementNudge = fmt.Sprintf("%s\n\nPrevious verification failed:\n%s\nFix the failures, then verify again.%s", implementNudge, failures, implementDiscipline)
			} else {
				// First attempt: attach TDD discipline for new feature work.
				implementNudge += tddDiscipline
			}
			if err := r.runner.Run(ctx, r.applySynthesize(implementNudge)); err != nil {
				return fmt.Errorf("compose implement (attempt %d): %w", attempt, err)
			}
		}
		skipImplement = false

		// Phase 2: Verify.
		r.notice("compose: verify")
		verifyNudge := "Run the project's verification commands now." + verifyDiscipline
		if err := r.runner.Run(ctx, r.applySynthesize(verifyNudge)); err != nil {
			return fmt.Errorf("compose verify (attempt %d): %w", attempt, err)
		}

		reply := r.lastReply()
		if hasPlanInvalidVerdict(reply) {
			// The model says the plan's premise is wrong, not just the code.
			// This is the structured signal MiMo's debug skill lacks (its
			// architecture-questioning never reaches the workflow controller).
			// Here it directly steers the control flow: pause and ask the user.
			if replans >= MaxReplans {
				r.notice("compose: model reports plan invalid but replan budget exhausted; stopping")
				break
			}
			reason := extractPlanInvalidReason(reply)
			r.notice(fmt.Sprintf("compose: model reports plan invalid — %s", truncate(reason, 300)))
			if r.requestApproval == nil {
				r.notice("compose: replan unavailable (no approval channel); stopping")
				break
			}
			approved, err := r.requestApproval(ctx, "The model reports the plan is invalid: "+reason+". Revise the plan for the remaining work?")
			if err != nil {
				return fmt.Errorf("compose plan-invalid approval: %w", err)
			}
			if !approved {
				r.notice("compose: user declined re-plan; stopping")
				break
			}
			replans++
			r.notice(fmt.Sprintf("compose: re-planning (replan %d/%d)", replans, MaxReplans))
			if err := r.replan(ctx); err != nil {
				return fmt.Errorf("compose replan %d: %w", replans, err)
			}
			attempt = 0
			failures = ""
			continue
		}

		verdict := readVerifyVerdict(reply)
		if verdict.passed {
			// Phase 3: Review.
			r.notice("compose: review")
			if err := r.runner.Run(ctx, r.applySynthesize(reviewNudge)); err != nil {
				return fmt.Errorf("compose review (attempt %d): %w", attempt, err)
			}
			reviewVerdict := readReviewVerdict(r.lastReply())
			if reviewVerdict.clean {
				r.notice("compose: review clean — done")
				return nil
			}
			r.notice(fmt.Sprintf("compose: review found issues, re-verifying: %s", truncate(reviewVerdict.issues, 200)))
			failures = "Review found critical issues fixed in-turn. Re-verify to confirm: " + reviewVerdict.issues
			skipImplement = true
			continue
		}
		failures = verdict.failures
		if failures == "" {
			failures = "(verify did not report a clear verdict)"
		}
		r.notice(fmt.Sprintf("compose: verification failed (attempt %d), retrying", attempt))
	}

	r.notice(fmt.Sprintf("compose: gave up after %d attempts; last failures:\n%s", MaxImplementAttempts, truncate(failures, 500)))
	r.gaveUp = true
	return nil // not a hard error — the work partially landed; controller completes todos
}

// applySynthesize wraps a nudge through the controller's ComposeSynthetic if
// available, so reasoning-language transforms still apply.
func (r *Runner) applySynthesize(text string) any {
	if r.synthesize != nil {
		return r.synthesize(text)
	}
	return text
}

// lastReply returns the model's most recent assistant text via the injected
// history reader (the controller's lastAssistantText helper).
func (r *Runner) lastReply() string {
	if r.history != nil {
		return r.history()
	}
	return ""
}

func (r *Runner) notice(msg string) {
	if r.sink == nil {
		return
	}
	r.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: msg})
}

// verifyVerdict is the structured result parsed from the model's verify reply.
type verifyVerdict struct {
	passed   bool
	failures string
}

// readVerifyVerdict scans the model's verify-turn reply for a VERDICT: marker.
// This is the lightweight MVP contract (no JSON schema yet — provider.Request
// lacks ResponseFormat). The model is instructed to emit VERDICT: PASS or
// VERDICT: FAIL; if neither appears, we treat it as a non-pass (retry).
func readVerifyVerdict(reply string) verifyVerdict {
	upper := strings.ToUpper(reply)
	switch {
	case strings.Contains(upper, "VERDICT: PASS"):
		return verifyVerdict{passed: true}
	case strings.Contains(upper, "VERDICT: FAIL"):
		// Capture the text after the marker as the failure summary.
		idx := strings.Index(upper, "VERDICT: FAIL")
		rest := reply[idx+len("VERDICT: FAIL"):]
		return verifyVerdict{passed: false, failures: strings.TrimSpace(rest)}
	default:
		return verifyVerdict{passed: false, failures: ""}
	}
}

// reviewVerdict is the structured result parsed from the model's review reply.
type reviewVerdict struct {
	clean  bool
	issues string
}

// readReviewVerdict scans the model's review-turn reply for VERDICT: CLEAN or
// VERDICT: ISSUES. CLEAN means no critical issues; ISSUES means the model found
// critical problems (it was told to fix them in-turn, so we re-verify).
func readReviewVerdict(reply string) reviewVerdict {
	upper := strings.ToUpper(reply)
	switch {
	case strings.Contains(upper, "VERDICT: CLEAN"):
		return reviewVerdict{clean: true}
	case strings.Contains(upper, "VERDICT: ISSUES"):
		idx := strings.Index(upper, "VERDICT: ISSUES")
		rest := reply[idx+len("VERDICT: ISSUES"):]
		return reviewVerdict{clean: false, issues: strings.TrimSpace(rest)}
	default:
		// No clear VERDICT marker. Treat as ISSUES (conservative) rather than
		// CLEAN: review is the final quality gate before declaring done, and a
		// missing verdict usually means the model's reply was truncated or it
		// described problems without the formal marker. Sending it back through
		// re-verify gives the loop another chance to catch real issues instead
		// of silently approving. The loop is bounded by MaxImplementAttempts so
		// a model that never emits a verdict won't loop forever. See audit C4.
		return reviewVerdict{clean: false, issues: "review did not emit a clear VERDICT: CLEAN/ISSUES marker (reply may be truncated or non-conformant); re-verifying to be safe"}
	}
}

// hasPlanInvalidVerdict reports whether the model declared the plan's premise
// is wrong. This is the structured signal that lets the orchestrator
// distinguish "code bug" (retry implement) from "plan assumption wrong"
// (pause + re-plan). The model emits it during verify or implement when it
// discovers the plan cannot work as designed — e.g. an assumed API doesn't
// exist, a dependency conflicts, or the approach is fundamentally flawed.
func hasPlanInvalidVerdict(reply string) bool {
	return strings.Contains(strings.ToUpper(reply), "VERDICT: PLAN_INVALID")
}

// extractPlanInvalidReason pulls the human-readable explanation that follows
// the PLAN_INVALID marker, so the approval request can show the user why the
// model thinks the plan needs revision.
func extractPlanInvalidReason(reply string) string {
	upper := strings.ToUpper(reply)
	idx := strings.Index(upper, "VERDICT: PLAN_INVALID")
	if idx < 0 {
		return "(no reason given)"
	}
	rest := reply[idx+len("VERDICT: PLAN_INVALID"):]
	return strings.TrimSpace(strings.TrimRight(rest, "\n"))
}

// replan drives a fresh planning turn. It tells the model: "the current plan
// doesn't work; revise the REMAINING work based on what you've already done
// and what you discovered." The model revises the todo list (todo_write) and
// the next implement attempt uses the updated list. Completed tasks stay
// completed — we only re-plan what's left.
func (r *Runner) replan(ctx context.Context) error {
	nudge := `The current plan cannot be completed as designed. Based on what you've already implemented and what you discovered, revise the REMAINING work (not the completed steps). Update the task list with todo_write to reflect the revised plan, keeping completed steps marked completed. Then continue executing the revised plan.` + implementDiscipline
	return r.runner.Run(ctx, r.applySynthesize(nudge))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
