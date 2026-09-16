package team

import (
	"path/filepath"
	"testing"

	"github.com/zzycxz/fairpeer/internal/taskmonitor"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "team_projects.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func mustCreate(t *testing.T, s *Store, name string) Team {
	t.Helper()
	tm, err := s.Create(Team{Name: name, Goal: "ship it"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return tm
}

func TestStoreCreateGetListDelete(t *testing.T) {
	s := newTestStore(t)
	tm := mustCreate(t, s, "交付团队")
	if tm.ID == "" {
		t.Fatal("expected an assigned team ID")
	}
	if tm.Context.Goal != "ship it" {
		t.Fatalf("goal should seed the blackboard, got %q", tm.Context.Goal)
	}
	if tm.Policy.MaxRounds != defaultMaxRounds || tm.Policy.MaxParallel != defaultMaxParallel {
		t.Fatalf("policy defaults not applied: %+v", tm.Policy)
	}
	if got, ok := s.Get(tm.ID); !ok || got.Name != "交付团队" {
		t.Fatalf("Get returned %+v ok=%v", got, ok)
	}
	if len(s.List()) != 1 {
		t.Fatalf("List len = %d, want 1", len(s.List()))
	}
	// Duplicate ID rejected.
	if _, err := s.Create(Team{ID: tm.ID, Name: "dup"}); err == nil {
		t.Fatal("expected duplicate-ID create to fail")
	}
	if !s.Delete(tm.ID) {
		t.Fatal("Delete returned false")
	}
	if len(s.List()) != 0 {
		t.Fatal("team not deleted")
	}
	if s.Delete(tm.ID) {
		t.Fatal("second Delete should report not-found")
	}
}

func TestStoreRejectsEmptyName(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(Team{Name: "   "}); err == nil {
		t.Fatal("expected empty-name create to fail")
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "teams.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tm := mustCreate(t, s, "持久化")
	if _, err := s.AddMember(tm.ID, Member{Name: "团长", IsLeader: true, Skills: []string{"planning"}}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := s.AddTask(tm.ID, Task{Title: "任务一", RequiredSkills: []string{"go"}}); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	reopened, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.Get(tm.ID)
	if !ok {
		t.Fatal("team lost after reopen")
	}
	if len(got.Members) != 1 || !got.Members[0].IsLeader {
		t.Fatalf("members not persisted: %+v", got.Members)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].Title != "任务一" {
		t.Fatalf("tasks not persisted: %+v", got.Tasks)
	}
	if got.Tasks[0].Status != taskmonitor.TaskStateQueued {
		t.Fatalf("zero status should default to queued, got %q", got.Tasks[0].Status)
	}
}

func TestSingleLeaderInvariant(t *testing.T) {
	s := newTestStore(t)
	tm := mustCreate(t, s, "单一团长")
	if _, err := s.AddMember(tm.ID, Member{Name: "A", IsLeader: true}); err != nil {
		t.Fatalf("AddMember A: %v", err)
	}
	got, err := s.AddMember(tm.ID, Member{Name: "B", IsLeader: true})
	if err != nil {
		t.Fatalf("AddMember B: %v", err)
	}
	leaders := 0
	for _, m := range got.Members {
		if m.IsLeader {
			leaders++
			if m.Name != "B" {
				t.Fatalf("newest leader flag should win, got %q", m.Name)
			}
		}
	}
	if leaders != 1 {
		t.Fatalf("leader count = %d, want 1", leaders)
	}

	// UpdateMember promoting A must demote B.
	a := got.Members[0]
	a.IsLeader = true
	got, err = s.UpdateMember(tm.ID, a)
	if err != nil {
		t.Fatalf("UpdateMember: %v", err)
	}
	leaders = 0
	for _, m := range got.Members {
		if m.IsLeader {
			leaders++
		}
	}
	if leaders != 1 {
		t.Fatalf("leader count after update = %d, want 1", leaders)
	}
	if l, ok := got.Leader(); !ok || l.Name != "A" {
		t.Fatalf("Leader() = %+v ok=%v, want A", l, ok)
	}
}

func TestRemoveMemberUnassignsTasks(t *testing.T) {
	s := newTestStore(t)
	tm := mustCreate(t, s, "移除团员")
	tm, err := s.AddMember(tm.ID, Member{Name: "Worker"})
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	worker := tm.Members[0]
	tm, err = s.AddTask(tm.ID, Task{Title: "T", AssigneeID: worker.ID})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	got, err := s.RemoveMember(tm.ID, worker.ID)
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if len(got.Members) != 0 {
		t.Fatalf("member not removed: %+v", got.Members)
	}
	if got.Tasks[0].AssigneeID != "" {
		t.Fatalf("task still assigned to removed member: %q", got.Tasks[0].AssigneeID)
	}
}

func TestMoveTaskTransitions(t *testing.T) {
	s := newTestStore(t)
	tm := mustCreate(t, s, "看板流转")
	tm, err := s.AddMember(tm.ID, Member{Name: "Dev"})
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	dev := tm.Members[0]

	tm, err = s.AddTask(tm.ID, Task{Title: "实现", AssigneeID: dev.ID})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	taskID := tm.Tasks[0].ID

	// queued → running is legal and bumps attempts.
	tm, err = s.MoveTask(tm.ID, taskID, taskmonitor.TaskStateRunning)
	if err != nil {
		t.Fatalf("MoveTask running: %v", err)
	}
	if tm.Tasks[0].Status != taskmonitor.TaskStateRunning || tm.Tasks[0].Attempts != 1 {
		t.Fatalf("after running: %+v", tm.Tasks[0])
	}

	// running → succeeded is legal.
	tm, err = s.MoveTask(tm.ID, taskID, taskmonitor.TaskStateSucceeded)
	if err != nil {
		t.Fatalf("MoveTask succeeded: %v", err)
	}
	if tm.Tasks[0].Status != taskmonitor.TaskStateSucceeded {
		t.Fatalf("after succeeded: %+v", tm.Tasks[0])
	}

	// succeeded is terminal: a drag back to running must be refused.
	tm, err = s.MoveTask(tm.ID, taskID, taskmonitor.TaskStateRunning)
	if err != nil {
		t.Fatalf("MoveTask illegal: %v", err)
	}
	if tm.Tasks[0].Status != taskmonitor.TaskStateSucceeded {
		t.Fatalf("illegal transition should be ignored, got %q", tm.Tasks[0].Status)
	}
}

func TestMoveTaskToColumnDerivedColumns(t *testing.T) {
	s := newTestStore(t)
	tm := mustCreate(t, s, "派生列")
	tm, err := s.AddMember(tm.ID, Member{Name: "Go 开发", Skills: []string{"go"}})
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	dev := tm.Members[0]

	// A fresh card starts queued + unassigned → Backlog.
	tm, err = s.AddTask(tm.ID, Task{Title: "实现", RequiredSkills: []string{"go"}})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	taskID := tm.Tasks[0].ID
	if got := ColumnFor(tm, tm.Tasks[0]); got != ColumnBacklog {
		t.Fatalf("new card should be in Backlog, got %q", got)
	}

	// Dropping onto Ready auto-routes to the best-skilled member.
	tm, err = s.MoveTaskToColumn(tm.ID, taskID, ColumnReady)
	if err != nil {
		t.Fatalf("MoveTaskToColumn ready: %v", err)
	}
	got, _ := tm.Task(taskID)
	if got.AssigneeID != dev.ID {
		t.Fatalf("Ready drop should auto-assign, got assignee %q", got.AssigneeID)
	}
	if col := ColumnFor(tm, got); col != ColumnReady {
		t.Fatalf("auto-assigned card should be Ready, got %q", col)
	}

	// Dropping back onto Backlog clears the assignee.
	tm, err = s.MoveTaskToColumn(tm.ID, taskID, ColumnBacklog)
	if err != nil {
		t.Fatalf("MoveTaskToColumn backlog: %v", err)
	}
	got, _ = tm.Task(taskID)
	if got.AssigneeID != "" || got.Status != taskmonitor.TaskStateQueued {
		t.Fatalf("backlog drop should unassign: %+v", got)
	}

	// A failed card dragged into Doing is a retry, not an illegal transition.
	if _, err := s.MoveTask(tm.ID, taskID, taskmonitor.TaskStateRunning); err != nil {
		t.Fatalf("MoveTask running: %v", err)
	}
	if _, err := s.MoveTask(tm.ID, taskID, taskmonitor.TaskStateFailed); err != nil {
		t.Fatalf("MoveTask failed: %v", err)
	}
	tm, err = s.MoveTaskToColumn(tm.ID, taskID, ColumnDoing)
	if err != nil {
		t.Fatalf("MoveTaskToColumn doing: %v", err)
	}
	got, _ = tm.Task(taskID)
	if got.Status != taskmonitor.TaskStateRunning {
		t.Fatalf("failed card should be requeueable back to Running, got %q", got.Status)
	}

	// An unknown column is a no-op.
	tm, err = s.MoveTaskToColumn(tm.ID, taskID, "bogus")
	if err != nil {
		t.Fatalf("MoveTaskToColumn bogus: %v", err)
	}
	if got, _ = tm.Task(taskID); got.Status != taskmonitor.TaskStateRunning {
		t.Fatalf("bogus column should not move the card, got %q", got.Status)
	}
}

func TestRemoveTaskPrunesDeps(t *testing.T) {
	s := newTestStore(t)
	tm := mustCreate(t, s, "依赖清理")
	tm, err := s.AddTask(tm.ID, Task{Title: "A"})
	if err != nil {
		t.Fatalf("AddTask A: %v", err)
	}
	a := tm.Tasks[0].ID
	tm, err = s.AddTask(tm.ID, Task{Title: "B", Deps: []string{a}})
	if err != nil {
		t.Fatalf("AddTask B: %v", err)
	}
	var b string
	for _, tk := range tm.Tasks {
		if tk.Title == "B" {
			b = tk.ID
		}
	}
	if _, err := s.RemoveTask(tm.ID, a); err != nil {
		t.Fatalf("RemoveTask A: %v", err)
	}
	got, ok := s.Get(tm.ID)
	if !ok {
		t.Fatal("team vanished")
	}
	tb, ok := got.Task(b)
	if !ok {
		t.Fatal("task B vanished")
	}
	if len(tb.Deps) != 0 {
		t.Fatalf("dangling dep not pruned: %+v", tb.Deps)
	}
}

func TestMoveTaskUnknownIDsAreNoops(t *testing.T) {
	s := newTestStore(t)
	tm := mustCreate(t, s, "空操作")
	if _, err := s.MoveTask(tm.ID, "nope", taskmonitor.TaskStateRunning); err != nil {
		t.Fatalf("MoveTask on unknown task should be a no-op, got %v", err)
	}
	if _, err := s.UpdateMember(tm.ID, Member{ID: "nope", Name: "X", IsLeader: true}); err != nil {
		t.Fatalf("UpdateMember on unknown member should be a no-op, got %v", err)
	}
	if _, err := s.Update("nope", func(*Team) {}); err == nil {
		t.Fatal("Update on unknown team should error")
	}
}

func TestSetContextBumpsVersion(t *testing.T) {
	s := newTestStore(t)
	tm := mustCreate(t, s, "黑板")
	v1 := tm.Context.Version
	got, err := s.SetContext(tm.ID, TeamContext{Goal: "新目标"})
	if err != nil {
		t.Fatalf("SetContext: %v", err)
	}
	if got.Context.Version <= v1 {
		t.Fatalf("version not bumped: %d -> %d", v1, got.Context.Version)
	}
	if got.Context.Goal != "新目标" {
		t.Fatalf("goal not updated: %q", got.Context.Goal)
	}
}
