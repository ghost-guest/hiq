package team

import (
	"sort"
	"time"

	"github.com/zzycxz/fairpeer/internal/taskmonitor"
)

// Board column keys. These are the stable identifiers the frontend renders as
// columns; they map onto taskmonitor.TaskState (never a second lifecycle).
const (
	ColumnBacklog   = "backlog"
	ColumnReady     = "ready"
	ColumnDoing     = "doing"
	ColumnApproval  = "approval"
	ColumnBlocked   = "blocked"
	ColumnDone      = "done"
	ColumnFailed    = "failed"
	ColumnCancelled = "cancelled"
)

// Column describes one kanban column and the cards in it.
type Column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// States lists the taskmonitor states this column represents (for the
	// frontend's "which state do I set on drop" decision).
	States []string `json:"states"`
	Tasks  []Task   `json:"tasks"`
}

// Board is the full kanban projection of a team.
type Board struct {
	TeamID  string   `json:"team_id"`
	Columns []Column `json:"columns"`
	// Counts is keyed by column key.
	Counts map[string]int `json:"counts"`
	// Total is the number of cards on the board.
	Total int `json:"total"`
}

// ColumnLabels are the display labels (zh) for each column. The frontend
// localizes further; these are the fallbacks embedded in the projection.
var ColumnLabels = map[string]string{
	ColumnBacklog:   "待分配",
	ColumnReady:     "待开始",
	ColumnDoing:     "进行中",
	ColumnApproval:  "待确认",
	ColumnBlocked:   "阻塞",
	ColumnDone:      "已完成",
	ColumnFailed:    "失败",
	ColumnCancelled: "已取消",
}

// boardColumnOrder is the fixed left-to-right column order. 待确认 sits between
// 待开始 and 进行中 because that is exactly where a human's attention is needed:
// after routing, before (or right at the end of) execution.
var boardColumnOrder = []string{
	ColumnBacklog, ColumnReady, ColumnApproval, ColumnDoing, ColumnBlocked, ColumnDone, ColumnFailed, ColumnCancelled,
}

// BoardOf projects a team's tasks onto kanban columns.
//
// The mapping is deliberately derived (not stored): a card's column is a
// function of its state plus assignment + dependency satisfaction, so the board
// can never disagree with the underlying state machine.
func BoardOf(t Team) Board {
	b := Board{
		TeamID:  t.ID,
		Columns: make([]Column, 0, len(boardColumnOrder)),
		Counts:  make(map[string]int, len(boardColumnOrder)),
	}
	idx := make(map[string]*Column, len(boardColumnOrder))
	for _, key := range boardColumnOrder {
		b.Columns = append(b.Columns, Column{
			Key:    key,
			Label:  ColumnLabels[key],
			States: statesForColumn(key),
			Tasks:  []Task{},
		})
	}
	for i := range b.Columns {
		idx[b.Columns[i].Key] = &b.Columns[i]
	}
	for _, tk := range t.Tasks {
		col := ColumnFor(t, tk)
		if c, ok := idx[col]; ok {
			c.Tasks = append(c.Tasks, tk)
			b.Counts[col]++
		}
		b.Total++
	}
	for i := range b.Columns {
		sortCards(b.Columns[i].Tasks)
	}
	return b
}

// ColumnFor returns the column key a card belongs to.
func ColumnFor(t Team, tk Task) string {
	// A pending HITL gate owns the card regardless of its lifecycle state: 待确认
	// is precisely where a human's attention is required (P4). This is why the
	// gate lives on the card rather than as a new taskmonitor state — the board
	// can surface "waiting on you" without forking the shared lifecycle.
	if tk.ApprovalState == ApprovalStatePending {
		return ColumnApproval
	}
	switch tk.Status {
	case taskmonitor.TaskStateRunning:
		return ColumnDoing
	case taskmonitor.TaskStateWaiting:
		return ColumnBlocked
	case taskmonitor.TaskStateSucceeded:
		return ColumnDone
	case taskmonitor.TaskStateFailed, taskmonitor.TaskStateStale:
		return ColumnFailed
	case taskmonitor.TaskStateCancelled:
		return ColumnCancelled
	case taskmonitor.TaskStateQueued, "":
		if tk.AssigneeID == "" {
			return ColumnBacklog
		}
		if !DepsSatisfied(t, tk) {
			return ColumnBlocked
		}
		return ColumnReady
	default:
		// Forward-compat: an unknown (future) state keeps the card visible
		// rather than silently dropping it.
		return ColumnBacklog
	}
}

// DepsSatisfied reports whether every task this card depends on has been
// completed (succeeded). A missing dep counts as unsatisfied — a dangling
// reference must block, not silently pass.
func DepsSatisfied(t Team, tk Task) bool {
	if len(tk.Deps) == 0 {
		return true
	}
	for _, depID := range tk.Deps {
		dep, ok := t.Task(depID)
		if !ok || dep.Status != taskmonitor.TaskStateSucceeded {
			return false
		}
	}
	return true
}

// statesForColumn maps a column to the taskmonitor states a drop should set.
// Backlog and Ready are both "queued" and are distinguished by assignment.
func statesForColumn(key string) []string {
	if st := stateForColumn(key); st != "" {
		return []string{string(st)}
	}
	return nil
}

// stateForColumn returns the primary state a column drop targets ("" for an
// unknown column).
func stateForColumn(key string) taskmonitor.TaskState {
	switch key {
	case ColumnBacklog, ColumnReady:
		return taskmonitor.TaskStateQueued
	case ColumnDoing:
		return taskmonitor.TaskStateRunning
	case ColumnBlocked:
		return taskmonitor.TaskStateWaiting
	case ColumnDone:
		return taskmonitor.TaskStateSucceeded
	case ColumnFailed:
		return taskmonitor.TaskStateFailed
	case ColumnCancelled:
		return taskmonitor.TaskStateCancelled
	default:
		return ""
	}
}

// MoveTaskToColumn implements a kanban drop. It maps a column to the resulting
// card state, handling the two derived columns the state machine doesn't know
// about:
//
//   - Backlog = queued AND unassigned (a drop here clears the assignee);
//   - Ready   = queued AND assigned (a drop here on an unassigned card
//     auto-routes it via capability matching, so "put it in Ready" lets the
//     team decide who owns it).
//
// A failed/stale card dropped back into the work lanes is treated as a requeue
// (mirroring taskmonitor's requeue affordance) rather than an illegal
// transition, so a retry is one drag away. Any other illegal transition (e.g.
// done → doing) is ignored, leaving the card where it was.
func (s *Store) MoveTaskToColumn(teamID, taskID, column string) (Team, error) {
	return s.Update(teamID, func(t *Team) {
		idx := -1
		for i := range t.Tasks {
			if t.Tasks[i].ID == taskID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return // unknown card: no-op
		}
		cur := t.Tasks[idx]
		// Dragging a card out of 待确认 IS the human's decision — a board gesture is
		// as explicit as clicking 通过 — so the gate resolves here. For a
		// before-gate that means "放行" (the card returns to 待开始/进行中); for an
		// after-gate it means the deliverable is accepted.
		if column != ColumnApproval && cur.ApprovalState == ApprovalStatePending {
			cur.ApprovalState = ApprovalStateApproved
			cur.ApprovalNote = ""
		}
		switch column {
		case ColumnApproval:
			// Parking a card for review. A card with no gate gets an after-gate:
			// "hold this and let me look at the result" is the natural intent.
			if cur.Approval == ApprovalNone {
				cur.Approval = ApprovalAfter
			}
			cur.ApprovalState = ApprovalStatePending
			cur.Status = taskmonitor.TaskStateWaiting
			cur.Progress, cur.Error = "", ""
		case ColumnBacklog:
			cur.Status = taskmonitor.TaskStateQueued
			cur.AssigneeID = ""
			cur.Progress, cur.Error = "", ""
			cur.ApprovalState = ApprovalStateNone // a reset forgets the decision
		case ColumnReady:
			cur.Status = taskmonitor.TaskStateQueued
			cur.Progress, cur.Error = "", ""
			if cur.AssigneeID == "" {
				if m, ok := SuggestAssignee(*t, cur); ok {
					cur.AssigneeID = m.ID
				}
			}
		default:
			next := stateForColumn(column)
			if next == "" {
				return // unknown column: no-op
			}
			requeue := cur.Status == taskmonitor.TaskStateFailed ||
				cur.Status == taskmonitor.TaskStateStale
			if cur.Status != next && !requeue && !cur.Status.ValidTransition(next) {
				return // illegal transition: ignore
			}
			cur.Status = next
			if next == taskmonitor.TaskStateRunning {
				cur.Attempts++
				cur.Error = ""
			}
			if next == taskmonitor.TaskStateSucceeded {
				cur.Progress, cur.Error = "", ""
				// Landing in 已完成 resolves an outstanding after-gate.
				cur.ApprovalState = ApprovalStateNone
			}
		}
		cur.UpdatedAt = time.Now().UTC()
		t.Tasks[idx] = cur
	})
}

// sortCards orders cards deterministically: manual Order first, then oldest
// created, then ID (so equal keys never flicker between renders).
func sortCards(cards []Task) {
	sort.SliceStable(cards, func(i, j int) bool {
		a, b := cards[i], cards[j]
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
}
