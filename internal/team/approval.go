package team

import (
	"fmt"
	"strings"
	"time"

	"github.com/zzycxz/fairpeer/internal/taskmonitor"
)

// Human-in-the-loop (HITL) gates — the P4 "人在回路" half of the team design.
//
// A card may declare a gate (Task.Approval):
//
//	before — the card must be confirmed before it is allowed to run;
//	after  — the member runs, but the deliverable must be confirmed before the
//	         card counts as done.
//
// The gate is expressed entirely through Task.ApprovalState, so the shared
// taskmonitor lifecycle (Task.Status) is untouched and the board simply derives
// a 待确认 column from it (see ColumnFor). Every mutation here goes through
// Store.Update, so a gate change is persisted atomically with the rest of the
// board.

// SetTaskApproval sets (or clears) a card's HITL gate. Changing the gate resets
// any standing decision, so switching modes can never leave a stale "approved"
// that would silently bypass the new gate.
func (s *Store) SetTaskApproval(teamID, taskID string, mode ApprovalMode) (Team, error) {
	if !mode.Valid() {
		return Team{}, fmt.Errorf("%w: unknown approval mode %q", ErrInvalid, mode)
	}
	return s.Update(teamID, func(t *Team) {
		for i := range t.Tasks {
			if t.Tasks[i].ID != taskID {
				continue
			}
			t.Tasks[i].Approval = mode
			t.Tasks[i].ApprovalState = ApprovalStateNone
			t.Tasks[i].ApprovalNote = ""
			t.Tasks[i].UpdatedAt = time.Now().UTC()
			return
		}
	})
}

// PendingApproval reports whether a card is currently waiting on a human.
func (tk Task) PendingApproval() bool { return tk.ApprovalState == ApprovalStatePending }

// GateBefore reports whether a card must be confirmed before it may run.
func (tk Task) GateBefore() bool { return tk.Approval == ApprovalBefore }

// GateAfter reports whether a card's deliverable must be confirmed.
func (tk Task) GateAfter() bool { return tk.Approval == ApprovalAfter }

// Approved reports whether the card's gate has already been satisfied.
func (tk Task) Approved() bool { return tk.ApprovalState == ApprovalStateApproved }

// NeedsGate reports whether the card still needs a human decision before it can
// proceed to run (a before-gate that has not been approved). An after-gate never
// blocks the run — it only holds the result.
func (tk Task) NeedsGate() bool { return tk.GateBefore() && !tk.Approved() }

// GateDeliverable parks a finished card whose after-gate is unsatisfied. It is
// the finishing half of the "after" gate: the member has run, the deliverable is
// stored, but the card waits for a human before it counts as done.
func GateDeliverable(tk *Task, reason string) {
	if tk == nil {
		return
	}
	tk.ApprovalState = ApprovalStatePending
	tk.ApprovalNote = strings.TrimSpace(reason)
	tk.Status = taskmonitor.TaskStateWaiting
	tk.Progress = ""
}

// RequestApproval parks a card in 待确认: it records why the human is needed and
// holds the card without touching the lifecycle's legality rules (the card is
// simply waiting on a person, which is what waiting means).
func (s *Store) RequestApproval(teamID, taskID, reason string) (Team, error) {
	return s.Update(teamID, func(t *Team) {
		for i := range t.Tasks {
			if t.Tasks[i].ID != taskID {
				continue
			}
			GateDeliverable(&t.Tasks[i], reason)
			t.Tasks[i].UpdatedAt = time.Now().UTC()
			return
		}
	})
}

// ResolveApproval applies a human decision:
//
//   - approved: the gate is satisfied. A before-gate card returns to 待开始
//     (queued, still assigned) so it can run; an after-gate card's deliverable
//     is accepted and the card becomes 已完成.
//   - rejected: the card fails with the human's reason, and the reason is also
//     raised as a blackboard open question so the leader sees it and can re-plan
//     (a rejection is information, not just a dead end).
//
// Resolving a card with no pending gate is a no-op rather than an error, so a
// double-click (or a stale board) can never wedge the workflow.
func (s *Store) ResolveApproval(teamID, taskID string, approved bool, note string) (Team, error) {
	note = strings.TrimSpace(note)
	return s.Update(teamID, func(t *Team) {
		for i := range t.Tasks {
			tk := &t.Tasks[i]
			if tk.ID != taskID {
				continue
			}
			if tk.ApprovalState != ApprovalStatePending {
				return // nothing pending: no-op
			}
			tk.ApprovalNote = note
			tk.UpdatedAt = time.Now().UTC()
			if approved {
				tk.ApprovalState = ApprovalStateApproved
				tk.Error = ""
				if tk.GateAfter() {
					tk.Status = taskmonitor.TaskStateSucceeded
					tk.Progress = ""
					tk.Evidence = append(tk.Evidence, "人工确认通过"+reasonSuffix(note))
				} else {
					tk.Status = taskmonitor.TaskStateQueued
					tk.Progress = ""
					tk.Evidence = append(tk.Evidence, "人工确认放行"+reasonSuffix(note))
				}
				return
			}
			tk.ApprovalState = ApprovalStateRejected
			tk.Status = taskmonitor.TaskStateFailed
			tk.Progress = ""
			tk.Error = "人工驳回" + reasonSuffix(note)
			q := "任务「" + firstNonEmpty(tk.Title, tk.ID) + "」被人工驳回"
			if note != "" {
				q += "：" + note
			}
			t.Context.OpenQuestions = appendUnique(t.Context.OpenQuestions, q)
			t.Context.Version++
		}
	})
}

// reasonSuffix renders "：<note>" for evidence/error strings, "" when blank.
func reasonSuffix(note string) string {
	if strings.TrimSpace(note) == "" {
		return ""
	}
	return "：" + note
}
