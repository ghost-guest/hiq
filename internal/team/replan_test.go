package team

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/fairpeer/internal/taskmonitor"
)

// replanTeam is a two-member team where only "后端" covers the go skill, so a
// failed card on the leader has exactly one sensible new home.
func replanTeam(autoReplan bool) Team {
	t := Team{
		ID:   "t1",
		Name: "演示",
		Members: []Member{
			{ID: "m-lead", Name: "团长", IsLeader: true, Skills: []string{"规划"}},
			{ID: "m-back", Name: "后端", Skills: []string{"go"}},
		},
		Policy: Policy{AutoReplan: autoReplan, MaxRounds: 3},
	}
	t.normalize()
	return t
}

func failedCard(assignee string, attempts int) Task {
	return Task{
		ID:             "task1",
		TeamID:         "t1",
		Title:          "写接口",
		AssigneeID:     assignee,
		RequiredSkills: []string{"go"},
		Status:         taskmonitor.TaskStateFailed,
		Attempts:       attempts,
	}
}

func TestReplanReassignsToAnotherCapableMember(t *testing.T) {
	tm := replanTeam(true)
	got := Replan(tm, failedCard("m-lead", 1))
	if got.Outcome != ReplanReassign {
		t.Fatalf("outcome = %q, want reassign (%s)", got.Outcome, got.Reason)
	}
	if got.MemberID != "m-back" {
		t.Errorf("member = %q, want m-back", got.MemberID)
	}
	if !strings.Contains(got.Reason, "团长") || !strings.Contains(got.Reason, "后端") {
		t.Errorf("reason should name both members: %q", got.Reason)
	}
}

func TestReplanNeverPicksTheMemberThatJustFailed(t *testing.T) {
	tm := replanTeam(true)
	// The failing member is the only one covering "go": there is nobody else, so
	// this must escalate rather than hand the same work back.
	got := Replan(tm, failedCard("m-back", 1))
	if got.Outcome != ReplanEscalate {
		t.Fatalf("outcome = %q, want escalate (%s)", got.Outcome, got.Reason)
	}
}

func TestReplanEscalatesAfterRoundsExhausted(t *testing.T) {
	tm := replanTeam(true)
	tm.Policy.MaxRounds = 2
	got := Replan(tm, failedCard("m-lead", 2))
	if got.Outcome != ReplanEscalate {
		t.Fatalf("outcome = %q, want escalate", got.Outcome)
	}
	if !strings.Contains(got.Reason, "上限") {
		t.Errorf("reason should explain the cap: %q", got.Reason)
	}
}

func TestReplanRespectsDisabledPolicy(t *testing.T) {
	tm := replanTeam(false)
	got := Replan(tm, failedCard("m-lead", 1))
	if got.Outcome != ReplanDisabled {
		t.Fatalf("outcome = %q, want disabled", got.Outcome)
	}
}

func TestReplanIgnoresNonFailedCards(t *testing.T) {
	tm := replanTeam(true)
	tk := failedCard("m-lead", 1)
	tk.Status = taskmonitor.TaskStateRunning
	if got := Replan(tm, tk); got.Outcome != ReplanDisabled {
		t.Fatalf("outcome = %q, want disabled for a running card", got.Outcome)
	}
}

func TestApplyReplanReassignsAndRequeues(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "teams.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tm, err := s.Create(replanTeam(true))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tk := failedCard("m-lead", 1)
	tk.AssigneeID = ""
	if _, err := s.AddTask(tm.ID, tk); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	// Force the failure state on the stored card.
	if _, err := s.UpdateTask(tm.ID, failedCard("m-lead", 1)); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	updated, res, err := s.ApplyReplan(tm.ID, "task1")
	if err != nil {
		t.Fatalf("ApplyReplan: %v", err)
	}
	if res.Outcome != ReplanReassign {
		t.Fatalf("outcome = %q, want reassign (%s)", res.Outcome, res.Reason)
	}
	card, ok := updated.Task("task1")
	if !ok {
		t.Fatalf("card vanished")
	}
	if card.AssigneeID != "m-back" {
		t.Errorf("assignee = %q, want m-back", card.AssigneeID)
	}
	if card.Status != taskmonitor.TaskStateQueued {
		t.Errorf("status = %q, want queued", card.Status)
	}
	if card.Error != "" {
		t.Errorf("the stale error should be cleared: %q", card.Error)
	}
	if len(card.Evidence) == 0 {
		t.Errorf("the re-plan reason should be recorded as evidence")
	}
	// A reassigned card lands back in the Ready lane, not Failed.
	if col := ColumnFor(updated, card); col != ColumnReady {
		t.Errorf("column = %q, want ready", col)
	}
}

func TestApplyReplanEscalatesIntoBlockedWithOpenQuestion(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "teams.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tm, err := s.Create(replanTeam(true))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Only the failing member covers the skill → escalation.
	card := failedCard("m-back", 1)
	card.AssigneeID = ""
	if _, err := s.AddTask(tm.ID, card); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.UpdateTask(tm.ID, failedCard("m-back", 1)); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	before := tm.Context.Version
	updated, res, err := s.ApplyReplan(tm.ID, "task1")
	if err != nil {
		t.Fatalf("ApplyReplan: %v", err)
	}
	if res.Outcome != ReplanEscalate {
		t.Fatalf("outcome = %q, want escalate", res.Outcome)
	}
	got, _ := updated.Task("task1")
	if got.Status != taskmonitor.TaskStateWaiting {
		t.Errorf("status = %q, want waiting", got.Status)
	}
	if col := ColumnFor(updated, got); col != ColumnBlocked {
		t.Errorf("column = %q, want blocked", col)
	}
	if len(updated.Context.OpenQuestions) != 1 {
		t.Fatalf("open questions = %v, want exactly one", updated.Context.OpenQuestions)
	}
	if !strings.Contains(updated.Context.OpenQuestions[0], "写接口") {
		t.Errorf("the open question should name the card: %q", updated.Context.OpenQuestions[0])
	}
	if updated.Context.Version <= before {
		t.Errorf("escalating must bump the blackboard version (%d -> %d)",
			before, updated.Context.Version)
	}

	// Re-applying must not stack a duplicate question.
	again, _, err := s.ApplyReplan(tm.ID, "task1")
	if err != nil {
		t.Fatalf("second ApplyReplan: %v", err)
	}
	if len(again.Context.OpenQuestions) != 1 {
		t.Errorf("open questions stacked: %v", again.Context.OpenQuestions)
	}
}

func TestNormalizeDefaultsAutoReplanOnlyForPolicyLessTeams(t *testing.T) {
	bare := Team{Name: "裸团队"}
	bare.normalize()
	if !bare.Policy.AutoReplan {
		t.Errorf("a team with no policy should default to auto-replan")
	}
	if bare.Policy.MaxRounds != defaultMaxRounds || bare.Policy.MaxParallel != defaultMaxParallel {
		t.Errorf("defaults not applied: %+v", bare.Policy)
	}

	explicit := Team{Name: "明确关闭", Policy: Policy{AutoReplan: false, MaxRounds: 5}}
	explicit.normalize()
	if explicit.Policy.AutoReplan {
		t.Errorf("an explicit auto_replan=false must stick")
	}
	if explicit.Policy.MaxRounds != 5 {
		t.Errorf("MaxRounds = %d, want 5", explicit.Policy.MaxRounds)
	}
}
