package team

import (
	"testing"

	"github.com/zzycxz/fairpeer/internal/taskmonitor"
)

func TestSuggestAssigneeBySkill(t *testing.T) {
	tm := Team{
		ID:   "t1",
		Name: "路由",
		Members: []Member{
			{ID: "leader", Name: "团长", IsLeader: true, Skills: []string{"planning", "go"}},
			{ID: "fe", Name: "前端", Skills: []string{"frontend", "react"}},
			{ID: "be", Name: "后端", Skills: []string{"backend", "go", "sql"}},
		},
	}
	task := Task{ID: "k", Title: "接口", RequiredSkills: []string{"go", "sql"}}
	got, ok := SuggestAssignee(tm, task)
	if !ok {
		t.Fatal("SuggestAssignee returned no member")
	}
	if got.ID != "be" {
		t.Fatalf("expected the backend member (covers both skills), got %q", got.ID)
	}
}

func TestSuggestAssigneePrefersLowerLoad(t *testing.T) {
	tm := Team{
		ID:   "t1",
		Name: "负载",
		Members: []Member{
			{ID: "a", Name: "A", Skills: []string{"go"}},
			{ID: "b", Name: "B", Skills: []string{"go"}},
		},
		Tasks: []Task{
			{ID: "t1", AssigneeID: "a", Status: taskmonitor.TaskStateRunning},
			{ID: "t2", AssigneeID: "a", Status: taskmonitor.TaskStateQueued},
		},
	}
	got, ok := SuggestAssignee(tm, Task{ID: "new", RequiredSkills: []string{"go"}})
	if !ok {
		t.Fatal("no member")
	}
	if got.ID != "b" {
		t.Fatalf("equal skill should spread load to B, got %q", got.ID)
	}
}

func TestSuggestAssigneeDeprioritizesLeaderOnTie(t *testing.T) {
	tm := Team{
		ID:   "t1",
		Name: "团长不抢活",
		Members: []Member{
			{ID: "leader", Name: "团长", IsLeader: true, Skills: []string{"go"}},
			{ID: "dev", Name: "开发", Skills: []string{"go"}},
		},
	}
	got, ok := SuggestAssignee(tm, Task{ID: "k", RequiredSkills: []string{"go"}})
	if !ok {
		t.Fatal("no member")
	}
	if got.ID != "dev" {
		t.Fatalf("tie should go to the non-leader, got %q", got.ID)
	}
}

func TestSuggestAssigneeNoMembers(t *testing.T) {
	if _, ok := SuggestAssignee(Team{ID: "t1"}, Task{ID: "k"}); ok {
		t.Fatal("empty team should return ok=false")
	}
}

func TestRankCandidatesReportsMissingSkills(t *testing.T) {
	tm := Team{
		ID:   "t1",
		Name: "缺口",
		Members: []Member{
			{ID: "a", Name: "A", Skills: []string{"Go"}},
		},
	}
	cands := RankCandidates(tm, Task{RequiredSkills: []string{"go", "sql"}})
	if len(cands) != 1 {
		t.Fatalf("candidates = %d, want 1", len(cands))
	}
	c := cands[0]
	if c.Score != 1 || len(c.Matched) != 1 || c.Matched[0] != "go" {
		t.Fatalf("matched wrong: %+v", c)
	}
	if len(c.Missing) != 1 || c.Missing[0] != "sql" {
		t.Fatalf("missing wrong: %+v", c)
	}
}

func TestNormalizeSkillsDedupes(t *testing.T) {
	got := NormalizeSkills([]string{" Go ", "go", "", "SQL", "sql "})
	if len(got) != 2 || got[0] != "Go" || got[1] != "SQL" {
		t.Fatalf("NormalizeSkills = %v", got)
	}
}
