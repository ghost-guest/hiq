package team

import (
	"strings"
	"testing"

	"github.com/zzycxz/fairpeer/internal/taskmonitor"
)

// gateStore builds a one-member team with a single card, so each test can focus
// on the gate itself.
func gateStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	s := newTestStore(t)
	tm := mustCreate(t, s, "带闸门的团队")
	tm, err := s.AddMember(tm.ID, Member{Name: "后端", Skills: []string{"go"}})
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	memberID := tm.Members[0].ID
	tm, err = s.AddTask(tm.ID, Task{Title: "写接口", AssigneeID: memberID, RequiredSkills: []string{"go"}})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	return s, tm.ID, tm.Tasks[0].ID
}

func taskOf(t *testing.T, s *Store, teamID, taskID string) Task {
	t.Helper()
	tm, ok := s.Get(teamID)
	if !ok {
		t.Fatal("team vanished")
	}
	tk, ok := tm.Task(taskID)
	if !ok {
		t.Fatal("task vanished")
	}
	return tk
}

func TestGateBeforeRequiresApprovalToRun(t *testing.T) {
	s, teamID, taskID := gateStore(t)
	tm, err := s.SetTaskApproval(teamID, taskID, ApprovalBefore)
	if err != nil {
		t.Fatalf("SetTaskApproval: %v", err)
	}
	tk := tm.Tasks[0]
	if !tk.GateBefore() || !tk.NeedsGate() {
		t.Fatalf("a before-gate card must need confirmation: %+v", tk)
	}
	if tk.GateAfter() {
		t.Error("GateAfter should be false for a before gate")
	}
}

func TestRequestApprovalParksCardInApprovalColumn(t *testing.T) {
	s, teamID, taskID := gateStore(t)
	if _, err := s.SetTaskApproval(teamID, taskID, ApprovalBefore); err != nil {
		t.Fatalf("SetTaskApproval: %v", err)
	}
	tm, err := s.RequestApproval(teamID, taskID, "改动涉及线上 schema")
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	tk := tm.Tasks[0]
	if !tk.PendingApproval() {
		t.Fatalf("card should be pending, got state=%q", tk.ApprovalState)
	}
	if tk.ApprovalNote != "改动涉及线上 schema" {
		t.Errorf("note not stored: %q", tk.ApprovalNote)
	}
	if tk.Status != taskmonitor.TaskStateWaiting {
		t.Errorf("status = %q, want waiting", tk.Status)
	}
	if got := ColumnFor(tm, tk); got != ColumnApproval {
		t.Errorf("column = %q, want %q", got, ColumnApproval)
	}
}

func TestResolveApprovalBeforeGateReturnsCardToReady(t *testing.T) {
	s, teamID, taskID := gateStore(t)
	if _, err := s.SetTaskApproval(teamID, taskID, ApprovalBefore); err != nil {
		t.Fatalf("SetTaskApproval: %v", err)
	}
	if _, err := s.RequestApproval(teamID, taskID, ""); err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	tm, err := s.ResolveApproval(teamID, taskID, true, "可以上")
	if err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	tk := tm.Tasks[0]
	if !tk.Approved() {
		t.Fatalf("state = %q, want approved", tk.ApprovalState)
	}
	if tk.Status != taskmonitor.TaskStateQueued {
		t.Errorf("status = %q, want queued (放行后应回到待开始)", tk.Status)
	}
	if got := ColumnFor(tm, tk); got != ColumnReady {
		t.Errorf("column = %q, want %q", got, ColumnReady)
	}
	if !hasEvidence(tk, "人工确认放行") {
		t.Errorf("approval should leave an evidence trail: %v", tk.Evidence)
	}
}

func TestResolveApprovalAfterGateCompletesCard(t *testing.T) {
	s, teamID, taskID := gateStore(t)
	if _, err := s.SetTaskApproval(teamID, taskID, ApprovalAfter); err != nil {
		t.Fatalf("SetTaskApproval: %v", err)
	}
	// The after-gate never blocks the run; it holds the result.
	tk := taskOf(t, s, teamID, taskID)
	if tk.NeedsGate() {
		t.Fatal("an after-gate must not block the run")
	}
	if _, err := s.RequestApproval(teamID, taskID, "请验收产出"); err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	tm, err := s.ResolveApproval(teamID, taskID, true, "")
	if err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	if got := tm.Tasks[0].Status; got != taskmonitor.TaskStateSucceeded {
		t.Errorf("status = %q, want succeeded once the deliverable is accepted", got)
	}
}

func TestResolveApprovalRejectionFailsCardAndRaisesQuestion(t *testing.T) {
	s, teamID, taskID := gateStore(t)
	if _, err := s.SetTaskApproval(teamID, taskID, ApprovalAfter); err != nil {
		t.Fatalf("SetTaskApproval: %v", err)
	}
	if _, err := s.RequestApproval(teamID, taskID, ""); err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	tm, err := s.ResolveApproval(teamID, taskID, false, "没覆盖边界情况")
	if err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	tk := tm.Tasks[0]
	if tk.ApprovalState != ApprovalStateRejected {
		t.Fatalf("state = %q, want rejected", tk.ApprovalState)
	}
	if tk.Status != taskmonitor.TaskStateFailed {
		t.Errorf("status = %q, want failed", tk.Status)
	}
	if !strings.Contains(tk.Error, "没覆盖边界情况") {
		t.Errorf("rejection reason lost: %q", tk.Error)
	}
	if !containsSubstr(tm.Context.OpenQuestions, "人工驳回") {
		t.Errorf("rejection must surface on the blackboard: %v", tm.Context.OpenQuestions)
	}
}

func TestResolveApprovalIsNoopWithoutPendingGate(t *testing.T) {
	s, teamID, taskID := gateStore(t)
	before := taskOf(t, s, teamID, taskID)
	if _, err := s.ResolveApproval(teamID, taskID, true, "随口一说"); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	after := taskOf(t, s, teamID, taskID)
	if after.ApprovalState != before.ApprovalState || after.Status != before.Status {
		t.Fatalf("a no-pending resolve must not change the card: %+v → %+v", before, after)
	}
}

func TestSetTaskApprovalResetsStandingDecision(t *testing.T) {
	s, teamID, taskID := gateStore(t)
	if _, err := s.SetTaskApproval(teamID, taskID, ApprovalBefore); err != nil {
		t.Fatalf("SetTaskApproval: %v", err)
	}
	if _, err := s.RequestApproval(teamID, taskID, ""); err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if _, err := s.ResolveApproval(teamID, taskID, true, "ok"); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	tm, err := s.SetTaskApproval(teamID, taskID, ApprovalAfter)
	if err != nil {
		t.Fatalf("SetTaskApproval: %v", err)
	}
	tk := tm.Tasks[0]
	if tk.ApprovalState != ApprovalStateNone || tk.ApprovalNote != "" {
		t.Fatalf("changing the gate must clear the old decision: %+v", tk)
	}
	if tk.Approved() {
		t.Error("a stale 'approved' must never survive a mode change")
	}
}

func TestSetTaskApprovalRejectsUnknownMode(t *testing.T) {
	s, teamID, taskID := gateStore(t)
	if _, err := s.SetTaskApproval(teamID, taskID, ApprovalMode("whenever")); err == nil {
		t.Fatal("expected an error for an unknown gate mode")
	}
}

func TestMoveTaskOutOfApprovalResolvesGate(t *testing.T) {
	s, teamID, taskID := gateStore(t)
	if _, err := s.SetTaskApproval(teamID, taskID, ApprovalAfter); err != nil {
		t.Fatalf("SetTaskApproval: %v", err)
	}
	if _, err := s.RequestApproval(teamID, taskID, ""); err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	// Dragging the card onto 已完成 is as explicit as clicking 通过.
	tm, err := s.MoveTaskToColumn(teamID, taskID, ColumnDone)
	if err != nil {
		t.Fatalf("MoveTaskToColumn: %v", err)
	}
	tk := tm.Tasks[0]
	if tk.PendingApproval() {
		t.Fatal("dropping out of 待确认 must resolve the gate")
	}
	if tk.Status != taskmonitor.TaskStateSucceeded {
		t.Errorf("status = %q, want succeeded", tk.Status)
	}
}

func TestMoveTaskOntoApprovalColumnParksIt(t *testing.T) {
	s, teamID, taskID := gateStore(t)
	tm, err := s.MoveTaskToColumn(teamID, taskID, ColumnApproval)
	if err != nil {
		t.Fatalf("MoveTaskToColumn: %v", err)
	}
	tk := tm.Tasks[0]
	if !tk.PendingApproval() {
		t.Fatal("dropping onto 待确认 should park the card for review")
	}
	if tk.Approval != ApprovalAfter {
		t.Errorf("anchoring a gated-less card should declare an after-gate, got %q", tk.Approval)
	}
}

func TestReplanSkipsHumanRejectedCard(t *testing.T) {
	s, teamID, taskID := gateStore(t)
	if _, err := s.SetTaskApproval(teamID, taskID, ApprovalBefore); err != nil {
		t.Fatalf("SetTaskApproval: %v", err)
	}
	if _, err := s.RequestApproval(teamID, taskID, ""); err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if _, err := s.ResolveApproval(teamID, taskID, false, "方向不对"); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	got := Replan(teamOf(t, s, teamID), taskOf(t, s, teamID, taskID))
	if got.Outcome != ReplanDisabled {
		t.Fatalf("outcome = %q, want disabled — a rejection is a decision, not a failure", got.Outcome)
	}
}

func TestNormalizeRepairsStaleApprovalState(t *testing.T) {
	tm := Team{
		ID:   "t1",
		Name: "修补",
		Tasks: []Task{
			{ID: "a", Title: "无闸门却有状态", Approval: ApprovalNone, ApprovalState: ApprovalStatePending},
			{ID: "b", Title: "非法闸门", Approval: ApprovalMode("sometimes"), ApprovalState: ApprovalStateApproved},
		},
	}
	tm.normalize()
	if tm.Tasks[0].ApprovalState != ApprovalStateNone {
		t.Errorf("a gate-less card must not carry gate state: %q", tm.Tasks[0].ApprovalState)
	}
	if tm.Tasks[1].Approval != ApprovalNone {
		t.Errorf("an unknown gate must degrade to none, got %q", tm.Tasks[1].Approval)
	}
	if tm.Tasks[1].ApprovalState != ApprovalStateNone {
		t.Errorf("degraded gate must clear state, got %q", tm.Tasks[1].ApprovalState)
	}
}

// --- helpers ---

func teamOf(t *testing.T, s *Store, teamID string) Team {
	t.Helper()
	tm, ok := s.Get(teamID)
	if !ok {
		t.Fatal("team vanished")
	}
	return tm
}

func hasEvidence(tk Task, want string) bool {
	for _, e := range tk.Evidence {
		if strings.Contains(e, want) {
			return true
		}
	}
	return false
}

func containsSubstr(list []string, want string) bool {
	for _, s := range list {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}
