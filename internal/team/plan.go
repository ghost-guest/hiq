package team

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/taskmonitor"
)

// PlannedTask is one card the leader proposes when decomposing a goal.
//
// DependsOn indexes into the sibling list (0-based) rather than naming task IDs,
// because the model can't know IDs that don't exist yet — the caller resolves
// the indices to real IDs after materialising the cards.
type PlannedTask struct {
	Title          string   `json:"title"`
	Desc           string   `json:"desc"`
	RequiredSkills []string `json:"required_skills"`
	Acceptance     []string `json:"acceptance"`
	DependsOn      []int    `json:"depends_on"`
}

// TaskPlan is the JSON envelope the planner returns.
type TaskPlan struct {
	Tasks []PlannedTask `json:"tasks"`
}

// PlanSystemPrompt frames the model as the team's 团长 (leader) doing WBS: it
// must decompose a goal into verifiable cards with capability requirements, so
// the app can route each card to the right member.
func PlanSystemPrompt() string {
	return `你就是这个团队的团长。你的职责是分析目标、把它拆解成可执行、可验收的任务卡，并声明每张卡需要什么能力，以便按能力分派给合适的团员。

只输出一个 JSON 对象，不要解释、不要 Markdown 代码围栏：
{
  "tasks": [
    {
      "title": "任务标题（动词开头，一句话）",
      "desc": "要做什么、边界在哪（2-3 句）",
      "required_skills": ["所需能力标签，与团员能力标签用同一套词汇"],
      "acceptance": ["验收标准，每条都是可判断真假的"],
      "depends_on": [0]
    }
  ]
}

纪律：
- 拆到"可以分给一个人、有明确完成标准"的粒度，不要写"推进项目"这类空任务。
- 3-8 张卡为宜；有先后关系的用 depends_on 指向前面的下标（0 开始）。
- 至少留一张"验收/测试"性质的卡，形成闭环。
- required_skills 必须来自团队现有成员的能力标签里能对上号的词汇；如果目标需要的能力谁也不具备，就照实写出来（便于团长发现缺口）。
- 每张卡的 acceptance 至少 1 条。
- 只输出 JSON。`
}

// PlanUserPrompt builds the planner's user message: the roster (so the leader
// routes by real capabilities), the blackboard (so it doesn't contradict
// settled decisions) and the goal to decompose.
func PlanUserPrompt(t Team, goal string) string {
	var b strings.Builder
	b.WriteString("【团队】")
	b.WriteString(t.Name)
	if g := firstNonEmpty(goal, t.Context.Goal, t.Goal); g != "" {
		b.WriteString("\n【目标】")
		b.WriteString(g)
	}
	if c := t.Context.Constraints; c != "" {
		b.WriteString("\n【约束】")
		b.WriteString(c)
	}
	if len(t.Members) > 0 {
		b.WriteString("\n【成员与能力】")
		for _, m := range t.Members {
			b.WriteString("\n- ")
			b.WriteString(m.Name)
			if m.Role != "" {
				b.WriteString("（")
				b.WriteString(m.Role)
				b.WriteString("）")
			}
			if m.IsLeader {
				b.WriteString(" [团长]")
			}
			b.WriteString(" 能力: ")
			if len(m.Skills) == 0 {
				b.WriteString("(未标注)")
			} else {
				b.WriteString(strings.Join(m.Skills, ", "))
			}
		}
	}
	if len(t.Context.Decisions) > 0 {
		b.WriteString("\n【已定决策（不要推翻）】")
		for _, d := range t.Context.Decisions {
			b.WriteString("\n- ")
			b.WriteString(d.Text)
		}
	}
	if !t.DepsKnown() {
		b.WriteString("\n\n【已有任务卡】\n")
		for _, tk := range t.Tasks {
			b.WriteString("- ")
			b.WriteString(tk.Title)
			b.WriteString("\n")
		}
		b.WriteString("（不要与已有卡片重复。）")
	}
	return b.String()
}

// DepsKnown reports whether the team already has cards, so the planner prompt
// can warn against duplicating them.
func (t Team) DepsKnown() bool { return len(t.Tasks) == 0 }

// ParseTaskPlan extracts a task breakdown from a raw model reply.
func ParseTaskPlan(raw string) ([]PlannedTask, error) {
	// Prefer the {"tasks":[...]} envelope; accept a bare top-level array too,
	// since a model that answers with just an array is common enough not to fail.
	objIdx := strings.IndexByte(raw, '{')
	arrIdx := strings.IndexByte(raw, '[')
	if arrIdx >= 0 && (objIdx < 0 || arrIdx < objIdx) {
		arr, err := extractJSONValue(raw, '[')
		if err != nil {
			return nil, err
		}
		var tasks []PlannedTask
		if err := json.Unmarshal([]byte(arr), &tasks); err != nil {
			return nil, fmt.Errorf("team: parse task plan array: %w", err)
		}
		return filterPlanned(tasks), nil
	}
	obj, err := extractJSONObject(raw)
	if err != nil {
		return nil, err
	}
	var plan TaskPlan
	if err := json.Unmarshal([]byte(obj), &plan); err != nil {
		return nil, fmt.Errorf("team: parse task plan: %w", err)
	}
	return filterPlanned(plan.Tasks), nil
}

// filterPlanned drops blank entries and de-duplicates by title, so a chatty
// model can't inflate the board with duplicates.
func filterPlanned(in []PlannedTask) []PlannedTask {
	out := make([]PlannedTask, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, p := range in {
		p.Title = strings.TrimSpace(p.Title)
		if p.Title == "" {
			continue
		}
		key := strings.ToLower(p.Title)
		if seen[key] {
			continue
		}
		seen[key] = true
		p.Desc = strings.TrimSpace(p.Desc)
		p.RequiredSkills = NormalizeSkills(p.RequiredSkills)
		p.Acceptance = normalizeLines(p.Acceptance)
		out = append(out, p)
	}
	return out
}

func normalizeLines(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Materialize turns a plan into storable cards: it assigns each card to the
// best-skilled member (capability routing) and resolves DependsOn indices into
// real task IDs. Every card starts queued, so an assigned card lands in the
// board's Ready lane and an unassigned one in Backlog.
//
// deps are resolved within the plan only; a card may also depend on existing
// cards by passing their IDs through extraDeps.
func Materialize(t Team, plan []PlannedTask, extraDeps map[int][]string) []Task {
	now := t.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	ids := make([]string, len(plan))
	tasks := make([]Task, 0, len(plan))
	// First pass: mint IDs so the second pass can resolve dependencies.
	for i := range plan {
		ids[i] = newID("task")
	}
	// Seed the routing view with the minted cards so load-balancing sees them.
	view := t
	view.Tasks = append([]Task(nil), t.Tasks...)
	for i, p := range plan {
		card := Task{
			ID:             ids[i],
			TeamID:         t.ID,
			Title:          p.Title,
			Desc:           p.Desc,
			RequiredSkills: p.RequiredSkills,
			Status:         taskmonitor.TaskStateQueued,
			Acceptance:     criteriaFromLines(p.Acceptance),
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		for _, idx := range p.DependsOn {
			if idx >= 0 && idx < len(ids) && idx != i {
				card.Deps = append(card.Deps, ids[idx])
			}
		}
		if extra := extraDeps[i]; len(extra) > 0 {
			card.Deps = append(card.Deps, extra...)
		}
		// Route by capability — but only when someone actually covers the
		// required skills. An unmatched card stays unassigned (Backlog) so the
		// leader sees the capability gap instead of it being silently parked on
		// whoever happens to be idle. A card with no stated skills is assigned
		// purely by load.
		if len(card.RequiredSkills) == 0 {
			if m, ok := SuggestAssignee(view, card); ok {
				card.AssigneeID = m.ID
			}
		} else if cands := RankCandidates(view, card); len(cands) > 0 && cands[0].Score > 0 {
			card.AssigneeID = cands[0].MemberID
		}
		view.Tasks = append(view.Tasks, card)
		tasks = append(tasks, card)
	}
	return tasks
}

func criteriaFromLines(lines []string) []Criterion {
	out := make([]Criterion, 0, len(lines))
	for _, s := range lines {
		out = append(out, Criterion{Text: s})
	}
	return out
}

// AddTasks appends several cards in one atomic write (used by the leader's
// planning step so a half-written plan can't hit the board).
func (s *Store) AddTasks(teamID string, tasks []Task) (Team, error) {
	if len(tasks) == 0 {
		return s.GetOrErr(teamID)
	}
	for i := range tasks {
		if strings.TrimSpace(tasks[i].Title) == "" {
			return Team{}, fmt.Errorf("%w: task title is required", ErrInvalid)
		}
	}
	return s.Update(teamID, func(t *Team) {
		now := time.Now().UTC()
		for i := range tasks {
			tk := tasks[i]
			if tk.ID == "" {
				tk.ID = newID("task")
			}
			tk.TeamID = teamID
			if tk.Status == "" {
				tk.Status = taskmonitor.TaskStateQueued
			}
			if tk.CreatedAt.IsZero() {
				tk.CreatedAt = now
			}
			tk.UpdatedAt = now
			t.Tasks = append(t.Tasks, tk)
		}
	})
}

// GetOrErr returns a team or ErrNotFound.
func (s *Store) GetOrErr(id string) (Team, error) {
	if t, ok := s.Get(id); ok {
		return t, nil
	}
	return Team{}, fmt.Errorf("%w: team %q", ErrNotFound, id)
}

// ErrNoPlan is returned when the planner produced no usable cards.
var ErrNoPlan = errors.New("team: planner produced no tasks")
