package team

import (
	"fmt"
	"strings"
	"time"

	"github.com/zzycxz/fairpeer/internal/taskmonitor"
)

// P3 of 团队功能设计.md: 跟踪 + 再规划.
//
// When a card fails, sitting still is the worst outcome — nobody learns why and
// nobody retries. This file is the leader's "re-plan" step (Magentic-One's
// re-planning loop, grounded in the board's own state):
//
//   - if another member plausibly covers the required skills, the card is
//     REASSIGNED and requeued so it runs again without human intervention;
//   - otherwise it is ESCALATED — parked in 阻塞 with the reason recorded on the
//     blackboard's open questions, so a human (or the leader) picks it up
//     instead of the board silently accumulating corpses.
//
// The per-card attempt counter doubles as the round counter, and Policy.MaxRounds
// caps it: once a card has burned its rounds it escalates rather than looping
// forever. That is the single guard against a runaway re-plan/cost cycle.

// ReplanOutcome says what a re-plan decided.
type ReplanOutcome string

const (
	// ReplanDisabled means auto-replan is off, or the card is not failed.
	ReplanDisabled ReplanOutcome = "disabled"
	// ReplanReassign routes the card to a different member and requeues it.
	ReplanReassign ReplanOutcome = "reassign"
	// ReplanEscalate parks the card in 阻塞 for a human decision.
	ReplanEscalate ReplanOutcome = "escalate"
)

// ReplanResult reports a re-plan decision, including the reasoning, so the
// board (and the blackboard's evidence trail) can show WHY.
type ReplanResult struct {
	Outcome    ReplanOutcome `json:"outcome"`
	MemberID   string        `json:"member_id,omitempty"`
	MemberName string        `json:"member_name,omitempty"`
	Reason     string        `json:"reason,omitempty"`
}

// Attempted reports whether the outcome changed the card.
func (r ReplanResult) Attempted() bool {
	return r.Outcome == ReplanReassign || r.Outcome == ReplanEscalate
}

// isFailed reports whether a state counts as a failure worth re-planning.
func isFailed(s taskmonitor.TaskState) bool {
	return s == taskmonitor.TaskStateFailed || s == taskmonitor.TaskStateStale
}

// Replan decides what to do about a failed card.
//
// Excluding the member that just failed is the whole point: handing the same
// work back to the same agent expects a different result from an identical
// attempt. Only a member that actually covers at least one required skill is
// considered when the card states skills; a skill-less card falls back to load
// balancing (anyone but the one that failed).
func Replan(t Team, tk Task) ReplanResult {
	if !isFailed(tk.Status) {
		return ReplanResult{Outcome: ReplanDisabled, Reason: "卡片不是失败状态"}
	}
	// A human rejection is a decision, not a failure: re-planning it would
	// immediately undo the person's call (P4 HITL). The card waits for a new
	// human instruction instead.
	if tk.ApprovalState == ApprovalStateRejected {
		return ReplanResult{Outcome: ReplanDisabled, Reason: "已被人工驳回，等待人工处理"}
	}
	if !t.Policy.AutoReplan {
		return ReplanResult{Outcome: ReplanDisabled, Reason: "未开启自动再规划"}
	}
	rounds := t.Policy.MaxRounds
	if rounds <= 0 {
		rounds = defaultMaxRounds
	}
	if tk.Attempts >= rounds {
		return ReplanResult{
			Outcome: ReplanEscalate,
			Reason: fmt.Sprintf("已执行 %d 次仍未通过（上限 %d），升级为人工决策",
				tk.Attempts, rounds),
		}
	}

	prev := t.MemberName(tk.AssigneeID)
	for _, c := range RankCandidates(t, tk) {
		if c.MemberID == tk.AssigneeID {
			continue // the member that just failed is not a retry candidate
		}
		if len(tk.RequiredSkills) > 0 && c.Score == 0 {
			continue // covers none of the required skills: not a real candidate
		}
		reason := fmt.Sprintf("改派给「%s」重试（第 %d 次）", c.Name, tk.Attempts+1)
		if prev != "" {
			reason = fmt.Sprintf("上次由「%s」执行失败，%s", prev, reason)
		}
		return ReplanResult{
			Outcome:    ReplanReassign,
			MemberID:   c.MemberID,
			MemberName: c.Name,
			Reason:     reason,
		}
	}
	return ReplanResult{
		Outcome: ReplanEscalate,
		Reason:  "没有其他合适的人选，升级为人工决策",
	}
}

// ApplyReplan re-plans a failed card and persists the decision:
//
//   - reassign → the card moves to the new member and returns to queued (so it
//     lands in 待开始 and can run again immediately);
//   - escalate → the card is parked in waiting (阻塞) with the reason on both
//     the card and the blackboard's open questions, which is the P3 hook the
//     leader/human reads.
//
// A disabled/irrelevant decision leaves the card untouched. The returned Team is
// the refreshed copy, and the ReplanResult says what happened.
func (s *Store) ApplyReplan(teamID, taskID string) (Team, ReplanResult, error) {
	var res ReplanResult
	tm, err := s.Update(teamID, func(t *Team) {
		idx := -1
		for i := range t.Tasks {
			if t.Tasks[i].ID == taskID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return
		}
		tk := t.Tasks[idx]
		res = Replan(*t, tk)
		if !res.Attempted() {
			return
		}
		switch res.Outcome {
		case ReplanReassign:
			tk.AssigneeID = res.MemberID
			tk.Status = taskmonitor.TaskStateQueued
			tk.Progress = ""
			tk.Error = ""
			tk.Evidence = append(tk.Evidence, res.Reason)
		case ReplanEscalate:
			tk.Status = taskmonitor.TaskStateWaiting
			tk.Progress = ""
			tk.Error = res.Reason
			t.Context.OpenQuestions = appendUnique(t.Context.OpenQuestions, escalationQuestion(tk, res))
			t.Context.Version++
		}
		tk.UpdatedAt = time.Now().UTC()
		t.Tasks[idx] = tk
	})
	return tm, res, err
}

// escalationQuestion renders the open question an escalation adds to the
// blackboard, so the next leader/planning pass sees the blocked work.
func escalationQuestion(tk Task, res ReplanResult) string {
	title := strings.TrimSpace(tk.Title)
	if title == "" {
		title = "未命名任务"
	}
	return fmt.Sprintf("%s：%s", title, res.Reason)
}

// appendUnique appends s when it is not already present (trimmed comparison),
// so a repeatedly escalated card does not stack duplicate questions.
func appendUnique(list []string, s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return list
	}
	for _, ex := range list {
		if strings.TrimSpace(ex) == s {
			return list
		}
	}
	return append(list, s)
}
