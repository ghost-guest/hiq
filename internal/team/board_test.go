package team

import (
	"testing"
	"time"

	"github.com/zzycxz/fairpeer/internal/taskmonitor"
)

func card(id, title string, status taskmonitor.TaskState, assignee string, deps ...string) Task {
	return Task{
		ID:         id,
		Title:      title,
		Status:     status,
		AssigneeID: assignee,
		Deps:       deps,
		CreatedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestBoardColumnMapping(t *testing.T) {
	tm := Team{
		ID:   "t1",
		Name: "看板",
		Members: []Member{
			{ID: "m1", Name: "Dev"},
		},
		Tasks: []Task{
			card("c1", "待分配", taskmonitor.TaskStateQueued, ""),
			card("c2", "待开始", taskmonitor.TaskStateQueued, "m1"),
			card("c3", "进行中", taskmonitor.TaskStateRunning, "m1"),
			card("c4", "阻塞", taskmonitor.TaskStateWaiting, "m1"),
			card("c5", "完成", taskmonitor.TaskStateSucceeded, "m1"),
			card("c6", "失败", taskmonitor.TaskStateFailed, "m1"),
			card("c7", "陈旧", taskmonitor.TaskStateStale, "m1"),
			card("c8", "取消", taskmonitor.TaskStateCancelled, "m1"),
			card("c9", "依赖未满足", taskmonitor.TaskStateQueued, "m1", "c3"),
		},
	}
	b := BoardOf(tm)
	if b.Total != 9 {
		t.Fatalf("Total = %d, want 9", b.Total)
	}
	want := map[string][]string{
		ColumnBacklog:   {"c1"},
		ColumnReady:     {"c2"},
		ColumnDoing:     {"c3"},
		ColumnBlocked:   {"c4", "c9"},
		ColumnDone:      {"c5"},
		ColumnFailed:    {"c6", "c7"},
		ColumnCancelled: {"c8"},
	}
	byKey := map[string][]string{}
	for _, c := range b.Columns {
		ids := make([]string, 0, len(c.Tasks))
		for _, tk := range c.Tasks {
			ids = append(ids, tk.ID)
		}
		byKey[c.Key] = ids
	}
	for key, wantIDs := range want {
		got := byKey[key]
		if len(got) != len(wantIDs) {
			t.Fatalf("column %s = %v, want %v", key, got, wantIDs)
		}
		for i := range wantIDs {
			if got[i] != wantIDs[i] {
				t.Fatalf("column %s = %v, want %v", key, got, wantIDs)
			}
		}
	}
	if b.Counts[ColumnBlocked] != 2 {
		t.Fatalf("blocked count = %d, want 2", b.Counts[ColumnBlocked])
	}
}

func TestBoardDepsSatisfiedPromotesToReady(t *testing.T) {
	tm := Team{
		ID:   "t1",
		Name: "依赖",
		Members: []Member{
			{ID: "m1", Name: "Dev"},
		},
		Tasks: []Task{
			card("a", "前置", taskmonitor.TaskStateSucceeded, "m1"),
			card("b", "后续", taskmonitor.TaskStateQueued, "m1", "a"),
		},
	}
	if got := ColumnFor(tm, tm.Tasks[1]); got != ColumnReady {
		t.Fatalf("satisfied dep should be ready, got %q", got)
	}

	// A dangling dep must block, not pass.
	tm.Tasks[1].Deps = []string{"missing"}
	if got := ColumnFor(tm, tm.Tasks[1]); got != ColumnBlocked {
		t.Fatalf("missing dep should block, got %q", got)
	}
}

func TestBoardColumnsAlwaysPresentAndOrdered(t *testing.T) {
	b := BoardOf(Team{ID: "t1", Name: "空"})
	if len(b.Columns) != len(boardColumnOrder) {
		t.Fatalf("columns = %d, want %d", len(b.Columns), len(boardColumnOrder))
	}
	for i, key := range boardColumnOrder {
		if b.Columns[i].Key != key {
			t.Fatalf("column %d = %q, want %q", i, b.Columns[i].Key, key)
		}
		if b.Columns[i].Tasks == nil {
			t.Fatalf("column %q should render as an empty slice, not null", key)
		}
	}
}

func TestBoardCardOrdering(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tm := Team{
		ID:   "t1",
		Name: "排序",
		Members: []Member{
			{ID: "m1", Name: "Dev"},
		},
		Tasks: []Task{
			{ID: "z", Title: "later", Status: taskmonitor.TaskStateQueued, AssigneeID: "m1", Order: 2, CreatedAt: base},
			{ID: "a", Title: "first", Status: taskmonitor.TaskStateQueued, AssigneeID: "m1", Order: 1, CreatedAt: base},
			{ID: "m", Title: "unset", Status: taskmonitor.TaskStateQueued, AssigneeID: "m1", Order: 0, CreatedAt: base},
		},
	}
	b := BoardOf(tm)
	var ready []string
	for _, c := range b.Columns {
		if c.Key == ColumnReady {
			for _, tk := range c.Tasks {
				ready = append(ready, tk.ID)
			}
		}
	}
	want := []string{"m", "a", "z"}
	for i := range want {
		if ready[i] != want[i] {
			t.Fatalf("ready order = %v, want %v", ready, want)
		}
	}
}
