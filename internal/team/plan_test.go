package team

import (
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/taskmonitor"
)

func TestParseTaskPlanEnvelope(t *testing.T) {
	raw := "```json\n" + `{
	  "tasks": [
	    {"title":"落地结算接口","desc":"实现并自测","required_skills":["go","sql"],"acceptance":["接口 200","单测通过"],"depends_on":[]},
	    {"title":"验收测试","desc":"端到端验证","required_skills":["testing"],"acceptance":["用例全绿"],"depends_on":[0]}
	  ]
	}` + "\n```"
	plan, err := ParseTaskPlan(raw)
	if err != nil {
		t.Fatalf("ParseTaskPlan: %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("plan len = %d, want 2", len(plan))
	}
	if plan[1].DependsOn[0] != 0 {
		t.Fatalf("depends_on not parsed: %+v", plan[1])
	}
}

func TestParseTaskPlanBareArray(t *testing.T) {
	raw := `[{"title":"A"},{"title":"B"}]`
	plan, err := ParseTaskPlan(raw)
	if err != nil {
		t.Fatalf("ParseTaskPlan: %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("plan len = %d, want 2", len(plan))
	}
}

func TestParseTaskPlanDedupesAndDropsBlanks(t *testing.T) {
	raw := `{"tasks":[{"title":"A"},{"title":" a "},{"title":"  "},{"title":"B"}]}`
	plan, err := ParseTaskPlan(raw)
	if err != nil {
		t.Fatalf("ParseTaskPlan: %v", err)
	}
	if len(plan) != 2 || plan[0].Title != "A" || plan[1].Title != "B" {
		t.Fatalf("dedupe/blanks failed: %+v", plan)
	}
}

func TestParseTaskPlanErrors(t *testing.T) {
	if _, err := ParseTaskPlan("no json at all"); err == nil {
		t.Fatal("expected error for missing JSON")
	}
	if _, err := ParseTaskPlan(`{"tasks":`); err == nil {
		t.Fatal("expected error for unterminated JSON")
	}
}

func TestMaterializeRoutesAndResolvesDeps(t *testing.T) {
	tm := Team{
		ID:   "t1",
		Name: "交付",
		Members: []Member{
			{ID: "leader", Name: "团长", IsLeader: true, Skills: []string{"planning"}},
			{ID: "be", Name: "后端", Skills: []string{"go", "sql"}},
			{ID: "qa", Name: "测试", Skills: []string{"testing"}},
		},
	}
	plan := []PlannedTask{
		{Title: "落地接口", RequiredSkills: []string{"go", "sql"}, Acceptance: []string{"单测通过"}},
		{Title: "验收", RequiredSkills: []string{"testing"}, DependsOn: []int{0}},
		{Title: "无对口能力", RequiredSkills: []string{"devops"}},
	}
	tasks := Materialize(tm, plan, nil)
	if len(tasks) != 3 {
		t.Fatalf("materialized %d tasks, want 3", len(tasks))
	}
	if tasks[0].AssigneeID != "be" {
		t.Fatalf("card 0 should route to the backend member, got %q", tasks[0].AssigneeID)
	}
	if tasks[1].AssigneeID != "qa" {
		t.Fatalf("card 1 should route to QA, got %q", tasks[1].AssigneeID)
	}
	if len(tasks[1].Deps) != 1 || tasks[1].Deps[0] != tasks[0].ID {
		t.Fatalf("dep index not resolved to a real ID: %+v", tasks[1].Deps)
	}
	if len(tasks[0].Acceptance) != 1 || tasks[0].Acceptance[0].Text != "单测通过" {
		t.Fatalf("acceptance not carried over: %+v", tasks[0].Acceptance)
	}
	if tasks[0].Status != taskmonitor.TaskStateQueued {
		t.Fatalf("cards should start queued, got %q", tasks[0].Status)
	}
	// The card with no matching capability stays unassigned (Backlog) so the
	// leader can see the gap rather than mis-assigning it.
	if tasks[2].AssigneeID != "" {
		t.Fatalf("unmatched card should stay unassigned, got %q", tasks[2].AssigneeID)
	}
	// IDs are unique.
	seen := map[string]bool{}
	for _, tk := range tasks {
		if seen[tk.ID] {
			t.Fatalf("duplicate task ID %q", tk.ID)
		}
		seen[tk.ID] = true
	}
}

func TestMaterializeLandsAssignedCardsInReady(t *testing.T) {
	tm := Team{
		ID:      "t1",
		Name:    "看板落位",
		Members: []Member{{ID: "be", Name: "后端", Skills: []string{"go"}}},
	}
	tasks := Materialize(tm, []PlannedTask{{Title: "实现", RequiredSkills: []string{"go"}}}, nil)
	// Materialize returns only the new cards, so evaluate the column against a
	// team that contains them (which is what the store would persist).
	tm.Tasks = tasks
	if col := ColumnFor(tm, tasks[0]); col != ColumnReady {
		t.Fatalf("assigned card should be Ready, got %q", col)
	}
}

func TestAddTasksPersistsAtomically(t *testing.T) {
	s := newTestStore(t)
	tm := mustCreate(t, s, "批量")
	got, err := s.AddTasks(tm.ID, []Task{{Title: "A"}, {Title: "B", Status: taskmonitor.TaskStateQueued}})
	if err != nil {
		t.Fatalf("AddTasks: %v", err)
	}
	if len(got.Tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(got.Tasks))
	}
	if _, err := s.AddTasks(tm.ID, []Task{{Title: "  "}}); err == nil {
		t.Fatal("blank title should be rejected")
	}
	// An empty batch is a no-op that returns the current team.
	again, err := s.AddTasks(tm.ID, nil)
	if err != nil {
		t.Fatalf("empty AddTasks: %v", err)
	}
	if len(again.Tasks) != 2 {
		t.Fatalf("empty batch changed the team: %d tasks", len(again.Tasks))
	}
}

func TestPlanUserPromptMentionsRosterAndSkills(t *testing.T) {
	tm := Team{
		Name: "交付",
		Members: []Member{
			{Name: "团长", IsLeader: true, Skills: []string{"planning"}},
			{Name: "后端", Skills: []string{"go"}},
		},
		Context: TeamContext{Goal: "上线结算", Decisions: []Decision{{Text: "用 Go"}}},
	}
	p := PlanUserPrompt(tm, "上线结算系统")
	for _, want := range []string{"上线结算系统", "后端", "go", "团长", "用 Go"} {
		if !strings.Contains(p, want) {
			t.Fatalf("planner prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "【已有任务卡】") {
		t.Fatal("prompt should not warn about existing cards when the board is empty")
	}
}
