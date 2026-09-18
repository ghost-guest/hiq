package main

// team_app.go exposes the 团队 (team) feature to the frontend: persistent teams
// with a 团长 (leader) + 团员 (members), a kanban board, and natural-language
// member/team creation.
//
// Relationship to experts_app.go: that file wires 专家团 (one-shot multi-model
// collaboration). This one wires 团队 — a long-lived project whose leader plans
// and assigns work while each member owns an independent context. The two
// features stay separate on purpose (different mental models, different data).
//
// Events:
//   - "team:changed" (TeamProjectView) — a team/member/task was mutated; the
//     payload is the refreshed team so the panel can re-render without a
//     follow-up round-trip.
//
// Storage: team projects live under the memory data root
// (<root>/teams/team_projects.json) so they follow the user's configured data
// location instead of being pinned to the system config dir.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/taskmonitor"
	teampkg "github.com/zzycxz/hiq/internal/team"
)

// --- DTOs (JSON-friendly projections of the team domain model) ---

// TeamProjectView is a team project as the frontend sees it.
type TeamProjectView struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Goal      string           `json:"goal"`
	Members   []TeamMemberView `json:"members"`
	Tasks     []TeamTaskView   `json:"tasks"`
	Context   TeamContextView  `json:"context"`
	Policy    TeamPolicyView   `json:"policy"`
	CreatedAt string           `json:"createdAt,omitempty"`
	UpdatedAt string           `json:"updatedAt,omitempty"`
}

// TeamMemberView is one 团员 (or the 团长).
type TeamMemberView struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Model        string   `json:"model"`
	Effort       string   `json:"effort"`
	Skills       []string `json:"skills"`
	Tools        []string `json:"tools"`
	SystemPrompt string   `json:"systemPrompt"`
	IsLeader     bool     `json:"isLeader"`
	Avatar       string   `json:"avatar"`
}

// TeamTaskView is one kanban card.
type TeamTaskView struct {
	ID             string              `json:"id"`
	Title          string              `json:"title"`
	Desc           string              `json:"desc"`
	AssigneeID     string              `json:"assigneeId"`
	AssigneeName   string              `json:"assigneeName"`
	RequiredSkills []string            `json:"requiredSkills"`
	Status         string              `json:"status"`
	Column         string              `json:"column"`
	Deps           []string            `json:"deps"`
	ParentID       string              `json:"parentId"`
	Acceptance     []TeamCriterionView `json:"acceptance"`
	Deliverable    string              `json:"deliverable"`
	Attempts       int                 `json:"attempts"`
	Evidence       []string            `json:"evidence"`
	Progress       string              `json:"progress"`
	Error          string              `json:"error"`
	// Approval is this card's human-in-the-loop gate ("", "before", "after").
	Approval string `json:"approval"`
	// ApprovalState is the live gate status ("", "pending", "approved",
	// "rejected") — what the board's 待确认 column keys off.
	ApprovalState string `json:"approvalState"`
	// ApprovalNote is the human's confirmation/rejection reason.
	ApprovalNote string `json:"approvalNote"`
	Order        int    `json:"order"`
}

// TeamCriterionView is one acceptance check.
type TeamCriterionView struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// TeamContextView is the shared blackboard.
type TeamContextView struct {
	Goal          string             `json:"goal"`
	Constraints   string             `json:"constraints"`
	Decisions     []TeamDecisionView `json:"decisions"`
	Artifacts     []TeamArtifactView `json:"artifacts"`
	OpenQuestions []string           `json:"openQuestions"`
	// Notes is the member-writable shared scratchpad (P4 共享上下文).
	Notes []TeamNoteView `json:"notes"`
	// Checkpoints are rolling summaries of notes that have aged out of the hot
	// window (P1-4 step B), so the panel can show what the team remembers from
	// before. Degraded marks an index-style fallback (the summariser failed).
	Checkpoints []TeamCheckpointView `json:"checkpoints"`
	Version     int                  `json:"version"`
}

// TeamCheckpointView is one shared-note checkpoint (a compressed span of older
// notes that all members render).
type TeamCheckpointView struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	Count    int    `json:"count"`
	Degraded bool   `json:"degraded"`
	UpTo     string `json:"upTo,omitempty"`
	At       string `json:"at,omitempty"`
}

// TeamNoteView is one shared-context note posted by a member (or the user).
type TeamNoteView struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	AuthorName string `json:"authorName"`
	TaskID     string `json:"taskId"`
	Text       string `json:"text"`
	At         string `json:"at,omitempty"`
}

// TeamDecisionView is a settled decision on the blackboard.
type TeamDecisionView struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	By   string `json:"by"`
	At   string `json:"at,omitempty"`
}

// TeamArtifactView indexes a produced artifact.
type TeamArtifactView struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	TaskID  string `json:"taskId"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

// TeamPolicyView bounds team autonomy.
type TeamPolicyView struct {
	MaxRounds   int  `json:"maxRounds"`
	MaxParallel int  `json:"maxParallel"`
	AutoAssign  bool `json:"autoAssign"`
	AutoReplan  bool `json:"autoReplan"`
}

// TeamColumnView is one kanban column.
type TeamColumnView struct {
	Key    string         `json:"key"`
	Label  string         `json:"label"`
	States []string       `json:"states"`
	Tasks  []TeamTaskView `json:"tasks"`
}

// TeamBoardView is the kanban projection.
type TeamBoardView struct {
	TeamID  string           `json:"teamId"`
	Columns []TeamColumnView `json:"columns"`
	Counts  map[string]int   `json:"counts"`
	Total   int              `json:"total"`
}

// TeamCandidateView is a scored assignment candidate (capability routing).
type TeamCandidateView struct {
	MemberID string   `json:"memberId"`
	Name     string   `json:"name"`
	Score    int      `json:"score"`
	Matched  []string `json:"matched"`
	Missing  []string `json:"missing"`
	Load     int      `json:"load"`
	IsLeader bool     `json:"isLeader"`
}

// TeamDraftInput drives natural-language creation.
type TeamDraftInput struct {
	// TeamID targets an existing team (member drafting). Empty = draft a whole
	// team (the instruction is then treated as a project goal).
	TeamID string `json:"teamId"`
	// Instruction is the user's natural-language request.
	Instruction string `json:"instruction"`
	// Model optionally pins the drafting model ("provider/model"). Empty uses
	// the configured default model.
	Model string `json:"model"`
	// Adopt, when true, writes the drafted result to the store immediately.
	// False (default) returns a preview the UI can show for confirmation.
	Adopt bool `json:"adopt"`
}

// --- bound methods ---

// ListTeamProjects returns every saved team project.
func (a *App) ListTeamProjects() []TeamProjectView {
	if a.teamStore == nil {
		return []TeamProjectView{}
	}
	teams := a.teamStore.List()
	out := make([]TeamProjectView, 0, len(teams))
	for _, t := range teams {
		out = append(out, toTeamProjectView(t))
	}
	return out
}

// GetTeamProject loads one team project by ID.
func (a *App) GetTeamProject(id string) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	t, ok := store.Get(id)
	if !ok {
		return TeamProjectView{}, fmt.Errorf("团队不存在: %s", id)
	}
	return toTeamProjectView(t), nil
}

// CreateTeamProject saves a new team project. Members/tasks supplied inline are
// created with it (IDs are assigned when empty).
func (a *App) CreateTeamProject(tv TeamProjectView) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	created, err := store.Create(teamProjectViewToModel(tv))
	if err != nil {
		return TeamProjectView{}, err
	}
	a.emitTeamChanged(created)
	return toTeamProjectView(created), nil
}

// UpdateTeamProject replaces a team's mutable fields (name/goal/blackboard/policy).
func (a *App) UpdateTeamProject(tv TeamProjectView) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	updated, err := store.Update(tv.ID, func(t *teampkg.Team) {
		t.Name = tv.Name
		t.Goal = tv.Goal
		if tv.Context.Goal != "" || tv.Context.Constraints != "" ||
			len(tv.Context.Decisions) > 0 || len(tv.Context.Artifacts) > 0 ||
			len(tv.Context.OpenQuestions) > 0 {
			t.Context.Goal = firstNonBlankTeam(tv.Context.Goal, tv.Goal)
			t.Context.Constraints = tv.Context.Constraints
			t.Context.Decisions = decisionsToModel(tv.Context.Decisions)
			t.Context.Artifacts = artifactsToModel(tv.Context.Artifacts)
			t.Context.OpenQuestions = trimList(tv.Context.OpenQuestions)
			t.Context.Version++
		}
		t.Policy = policyToModel(tv.Policy)
	})
	if err != nil {
		return TeamProjectView{}, err
	}
	// Policy.MaxParallel may have been edited: re-bound the team's pool and start
	// whatever the new bound now allows to leave the queue (P1-1).
	for _, key := range a.teamPoolsInit().SetMax(updated.ID, updated.Policy.MaxParallel) {
		a.startPromotedTeamRun(key)
	}
	a.emitTeamChanged(updated)
	return toTeamProjectView(updated), nil
}

// DeleteTeamProject removes a team project.
func (a *App) DeleteTeamProject(id string) error {
	store, err := a.requireTeamStore()
	if err != nil {
		return err
	}
	if !store.Delete(id) {
		return fmt.Errorf("团队不存在: %s", id)
	}
	// The shared-note archive lives beside the store file rather than inside it,
	// so deleting the team must delete its history too — otherwise a deleted
	// team's notes stay on disk forever with nothing that can reach them.
	if path, err := store.NotesArchivePath(id); err == nil {
		_ = os.Remove(path)
	}
	// A deleted team's runs must not keep holding slots or streaming into a
	// board that no longer exists.
	if running, _ := a.teamPoolsInit().Drop(id); len(running) > 0 {
		a.teamRunsMu.Lock()
		cancels := make([]context.CancelFunc, 0, len(running))
		for _, key := range running {
			if cancel, ok := a.teamRuns[key]; ok {
				cancels = append(cancels, cancel)
				delete(a.teamRuns, key)
			}
		}
		a.teamRunsMu.Unlock()
		for _, cancel := range cancels {
			cancel()
		}
	}
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "team:changed", map[string]string{"id": id, "deleted": "1"})
	}
	return nil
}

// AddTeamMember appends a 团员 to a team.
func (a *App) AddTeamMember(teamID string, mv TeamMemberView) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	updated, err := store.AddMember(teamID, memberViewToModel(mv))
	if err != nil {
		return TeamProjectView{}, err
	}
	a.emitTeamChanged(updated)
	return toTeamProjectView(updated), nil
}

// UpdateTeamMember replaces a member's fields by ID.
func (a *App) UpdateTeamMember(teamID string, mv TeamMemberView) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	updated, err := store.UpdateMember(teamID, memberViewToModel(mv))
	if err != nil {
		return TeamProjectView{}, err
	}
	a.emitTeamChanged(updated)
	return toTeamProjectView(updated), nil
}

// RemoveTeamMember drops a member (and unassigns their cards).
func (a *App) RemoveTeamMember(teamID, memberID string) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	updated, err := store.RemoveMember(teamID, memberID)
	if err != nil {
		return TeamProjectView{}, err
	}
	a.emitTeamChanged(updated)
	return toTeamProjectView(updated), nil
}

// AddTeamTask appends a kanban card.
func (a *App) AddTeamTask(teamID string, tv TeamTaskView) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	updated, err := store.AddTask(teamID, taskViewToModel(tv))
	if err != nil {
		return TeamProjectView{}, err
	}
	a.emitTeamChanged(updated)
	return toTeamProjectView(updated), nil
}

// UpdateTeamTask replaces a card's fields by ID.
func (a *App) UpdateTeamTask(teamID string, tv TeamTaskView) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	updated, err := store.UpdateTask(teamID, taskViewToModel(tv))
	if err != nil {
		return TeamProjectView{}, err
	}
	a.emitTeamChanged(updated)
	return toTeamProjectView(updated), nil
}

// MoveTeamTask drops a card onto a kanban column. Backlog/Ready are derived
// columns (queued ± assignee); Ready auto-routes an unassigned card.
func (a *App) MoveTeamTask(teamID, taskID, column string) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	updated, err := store.MoveTaskToColumn(teamID, taskID, column)
	if err != nil {
		return TeamProjectView{}, err
	}
	a.emitTeamChanged(updated)
	return toTeamProjectView(updated), nil
}

// RemoveTeamTask deletes a card.
func (a *App) RemoveTeamTask(teamID, taskID string) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	updated, err := store.RemoveTask(teamID, taskID)
	if err != nil {
		return TeamProjectView{}, err
	}
	a.emitTeamChanged(updated)
	return toTeamProjectView(updated), nil
}

// TeamBoard returns the kanban projection for a team.
func (a *App) TeamBoard(teamID string) (TeamBoardView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamBoardView{}, err
	}
	t, ok := store.Get(teamID)
	if !ok {
		return TeamBoardView{}, fmt.Errorf("团队不存在: %s", teamID)
	}
	return toTeamBoardView(t), nil
}

// SuggestTeamAssignee ranks members for a card via capability routing, so the
// UI can show why the leader would pick someone (and offer alternatives).
func (a *App) SuggestTeamAssignee(teamID, taskID string) ([]TeamCandidateView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return nil, err
	}
	t, ok := store.Get(teamID)
	if !ok {
		return nil, fmt.Errorf("团队不存在: %s", teamID)
	}
	task, ok := t.Task(taskID)
	if !ok {
		return nil, fmt.Errorf("任务不存在: %s", taskID)
	}
	cands := teampkg.RankCandidates(t, task)
	out := make([]TeamCandidateView, 0, len(cands))
	for _, c := range cands {
		out = append(out, TeamCandidateView(c))
	}
	return out, nil
}

// SetTeamContext replaces a team's shared blackboard (L0).
func (a *App) SetTeamContext(teamID string, ctxView TeamContextView) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	updated, err := store.SetContext(teamID, teampkg.TeamContext{
		Goal:          ctxView.Goal,
		Constraints:   ctxView.Constraints,
		Decisions:     decisionsToModel(ctxView.Decisions),
		Artifacts:     artifactsToModel(ctxView.Artifacts),
		OpenQuestions: trimList(ctxView.OpenQuestions),
	})
	if err != nil {
		return TeamProjectView{}, err
	}
	a.emitTeamChanged(updated)
	return toTeamProjectView(updated), nil
}

// TeamBlackboardDigest renders the compact blackboard index a member receives
// before each task, so the UI can preview exactly what a 团员 will be told
// about the team direction.
func (a *App) TeamBlackboardDigest(teamID string, maxChars int) (string, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return "", err
	}
	t, ok := store.Get(teamID)
	if !ok {
		return "", fmt.Errorf("团队不存在: %s", teamID)
	}
	return teampkg.BlackboardDigest(t, maxChars), nil
}

// --- P4: 共享上下文 (shared context) + HITL (human-in-the-loop) --------------

// AddTeamNote posts a shared-context note (P4 共享上下文). Members publish notes
// from their own run output; this is the user's (or the leader's) way in, so a
// human can steer the team's shared understanding without running a card.
func (a *App) AddTeamNote(teamID, taskID, text string) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	if strings.TrimSpace(text) == "" {
		return TeamProjectView{}, fmt.Errorf("随手记点东西吧")
	}
	updated, err := store.AddNote(teamID, teampkg.Note{
		TaskID: strings.TrimSpace(taskID),
		Text:   text,
	})
	if err != nil {
		return TeamProjectView{}, err
	}
	a.emitTeamChanged(updated)
	return toTeamProjectView(updated), nil
}

// TeamNotes lists a team's shared-context notes, newest first.
func (a *App) TeamNotes(teamID string) ([]TeamNoteView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return nil, err
	}
	t, ok := store.Get(teamID)
	if !ok {
		return nil, fmt.Errorf("团队不存在: %s", teamID)
	}
	notes := t.SharedNotes()
	out := make([]TeamNoteView, 0, len(notes))
	for _, n := range notes {
		out = append(out, TeamNoteView{
			ID: n.ID, Author: n.Author, AuthorName: t.ResolveNoteAuthor(n.Author),
			TaskID: n.TaskID, Text: n.Text, At: formatTeamTime(n.At),
		})
	}
	return out, nil
}

// SetTeamTaskApproval sets or clears a card's HITL gate ("" | "before" |
// "after"). Changing the gate clears any standing decision, so a mode switch can
// never silently inherit an "approved".
func (a *App) SetTeamTaskApproval(teamID, taskID, mode string) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	m := approvalModeFromView(mode)
	if strings.TrimSpace(mode) != "" && !m.Valid() {
		return TeamProjectView{}, fmt.Errorf("未知的确认方式: %s", mode)
	}
	updated, err := store.SetTaskApproval(teamID, taskID, m)
	if err != nil {
		return TeamProjectView{}, err
	}
	a.emitTeamChanged(updated)
	return toTeamProjectView(updated), nil
}

// ApproveTeamTask is the human's "通过" on a card parked in 待确认. A before-gate
// card is放行 and immediately dispatched (the point of HITL is that confirming
// resumes the work, not that it needs a second click); an after-gate card's
// deliverable is accepted and the card completes.
func (a *App) ApproveTeamTask(teamID, taskID, note string) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	updated, err := store.ResolveApproval(teamID, taskID, true, note)
	if err != nil {
		return TeamProjectView{}, err
	}
	a.emitTeamChanged(updated)

	if tk, ok := updated.Task(taskID); ok && tk.Status == taskmonitor.TaskStateQueued &&
		strings.TrimSpace(tk.AssigneeID) != "" {
		// Best-effort: if the card can't start (e.g. a prerequisite was
		// reopened), the approval still stands and the board says why.
		if started, err := a.RunTeamTask(teamID, taskID); err == nil {
			return started, nil
		}
	}
	return toTeamProjectView(updated), nil
}

// RejectTeamTask is the human's "驳回": the card fails with the reason, and the
// reason is raised on the blackboard so the leader/re-planner sees it.
func (a *App) RejectTeamTask(teamID, taskID, note string) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	updated, err := store.ResolveApproval(teamID, taskID, false, note)
	if err != nil {
		return TeamProjectView{}, err
	}
	a.emitTeamChanged(updated)
	return toTeamProjectView(updated), nil
}

// DraftTeamMember turns a natural-language request into a 团员 configuration
// ("帮我加一个擅长后端的团员"). With Adopt=true the member is created; otherwise
// the draft is returned for the user to review first.
func (a *App) DraftTeamMember(in TeamDraftInput) (TeamMemberView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamMemberView{}, err
	}
	t, ok := store.Get(in.TeamID)
	if !ok {
		return TeamMemberView{}, fmt.Errorf("团队不存在: %s", in.TeamID)
	}
	if strings.TrimSpace(in.Instruction) == "" {
		return TeamMemberView{}, fmt.Errorf("请描述你想要的团员")
	}
	raw, err := a.runTeamDraftLLM(in.Model, teampkg.MemberDraftSystemPrompt(), teampkg.MemberDraftUserPrompt(t, in.Instruction))
	if err != nil {
		return TeamMemberView{}, err
	}
	m, err := teampkg.ParseMemberDraft(raw)
	if err != nil {
		return TeamMemberView{}, err
	}
	// A drafted member never silently claims leadership; promoting is an
	// explicit user action in the UI.
	m.IsLeader = false
	if in.Adopt {
		updated, err := store.AddMember(in.TeamID, m)
		if err != nil {
			return TeamMemberView{}, err
		}
		a.emitTeamChanged(updated)
		for _, got := range updated.Members {
			if got.Name == m.Name && got.Role == m.Role {
				return toTeamMemberView(got), nil
			}
		}
	}
	return toTeamMemberView(m), nil
}

// DraftTeamProject turns a natural-language goal into a whole team (团长 +
// 团员). With Adopt=true the team is created; otherwise the draft is returned.
func (a *App) DraftTeamProject(in TeamDraftInput) (TeamProjectView, error) {
	if strings.TrimSpace(in.Instruction) == "" {
		return TeamProjectView{}, fmt.Errorf("请描述你的项目目标")
	}
	raw, err := a.runTeamDraftLLM(in.Model, teampkg.TeamDraftSystemPrompt(), in.Instruction)
	if err != nil {
		return TeamProjectView{}, err
	}
	t, err := teampkg.ParseTeamDraft(raw)
	if err != nil {
		return TeamProjectView{}, err
	}
	if !in.Adopt {
		return toTeamProjectView(t), nil
	}
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	created, err := store.Create(t)
	if err != nil {
		return TeamProjectView{}, err
	}
	a.emitTeamChanged(created)
	return toTeamProjectView(created), nil
}

// --- internals ---

// PlanTeamTasks is the 团长's planning step: it asks the model to decompose a
// goal into verifiable cards, routes each card to the best-skilled member
// (capability matching), records the goal on the shared blackboard, and adds
// every card in one atomic write.
//
// Cards whose required skills nobody covers are deliberately left unassigned so
// the capability gap is visible on the board rather than silently parked on an
// idle member.
func (a *App) PlanTeamTasks(teamID, goal, model string) (TeamProjectView, error) {
	store, err := a.requireTeamStore()
	if err != nil {
		return TeamProjectView{}, err
	}
	t, err := store.GetOrErr(teamID)
	if err != nil {
		return TeamProjectView{}, err
	}
	if len(t.Members) == 0 {
		return TeamProjectView{}, fmt.Errorf("请先添加团员，再让团长拆解任务")
	}
	if strings.TrimSpace(goal) == "" {
		goal = firstNonBlankTeam(t.Context.Goal, t.Goal)
	}
	if strings.TrimSpace(goal) == "" {
		return TeamProjectView{}, fmt.Errorf("请先填写项目目标")
	}
	raw, err := a.runTeamDraftLLM(model, teampkg.PlanSystemPrompt(), teampkg.PlanUserPrompt(t, goal))
	if err != nil {
		return TeamProjectView{}, err
	}
	plan, err := teampkg.ParseTaskPlan(raw)
	if err != nil {
		return TeamProjectView{}, err
	}
	if len(plan) == 0 {
		return TeamProjectView{}, teampkg.ErrNoPlan
	}
	tasks := teampkg.Materialize(t, plan, nil)
	if len(tasks) == 0 {
		return TeamProjectView{}, teampkg.ErrNoPlan
	}
	updated, err := store.AddTasks(teamID, tasks)
	if err != nil {
		return TeamProjectView{}, err
	}
	// Record the goal on the blackboard so every member starts aligned with it.
	if g := strings.TrimSpace(goal); g != "" && g != updated.Context.Goal {
		ctx := updated.Context
		ctx.Goal = g
		if updated, err = store.SetContext(teamID, ctx); err != nil {
			return TeamProjectView{}, err
		}
	}
	a.emitTeamChanged(updated)
	return toTeamProjectView(updated), nil
}

// --- internals ---

// requireTeamStore returns the team store, initialising it lazily if startup
// failed to create it (e.g. the config dir was briefly unwritable).
func (a *App) requireTeamStore() (*teampkg.Store, error) {
	if a.teamStore != nil {
		return a.teamStore, nil
	}
	a.initTeams()
	if a.teamStore == nil {
		return nil, fmt.Errorf("团队存储不可用")
	}
	return a.teamStore, nil
}

// initTeams creates the team store at startup. It lives under the memory data
// root so team projects follow the user's configured data location rather than
// the system config dir.
func (a *App) initTeams() {
	dir := filepath.Join(config.MemoryUserDir(), "teams")
	store, err := teampkg.NewStore(filepath.Join(dir, "team_projects.json"))
	if err != nil {
		return
	}
	a.teamStore = store
	a.teamPoolsInit()
	// A member run exists only in memory, so any card still marked running here
	// is a leftover from the previous process (P1-2).
	a.recoverTeamRuns()
}

// runTeamDraftLLM runs one drafting completion. It reuses the expert runner's
// provider resolution (so drafting honours the same provider config, proxy and
// rate-limit budget as the rest of the app) in one-shot mode — a draft is a
// single structured reply, no tools.
func (a *App) runTeamDraftLLM(model, systemPrompt, userPrompt string) (string, error) {
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	runner := &desktopExpertRunner{app: a}
	out, err := runner.Run(ctx, model, systemPrompt, userPrompt, false, nil)
	if err != nil {
		return "", fmt.Errorf("生成配置失败: %w", err)
	}
	return out, nil
}

// emitTeamChanged pushes a refreshed team (or a deletion notice) to the panel.
func (a *App) emitTeamChanged(t teampkg.Team) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "team:changed", toTeamProjectView(t))
}

// --- conversions ---

func toTeamProjectView(t teampkg.Team) TeamProjectView {
	members := make([]TeamMemberView, 0, len(t.Members))
	for _, m := range t.Members {
		members = append(members, toTeamMemberView(m))
	}
	tasks := make([]TeamTaskView, 0, len(t.Tasks))
	for _, tk := range t.Tasks {
		tasks = append(tasks, toTeamTaskView(t, tk))
	}
	return TeamProjectView{
		ID:        t.ID,
		Name:      t.Name,
		Goal:      t.Goal,
		Members:   members,
		Tasks:     tasks,
		Context:   toTeamContextView(t),
		Policy:    TeamPolicyView(t.Policy),
		CreatedAt: formatTeamTime(t.CreatedAt),
		UpdatedAt: formatTeamTime(t.UpdatedAt),
	}
}

func toTeamMemberView(m teampkg.Member) TeamMemberView {
	return TeamMemberView{
		ID:           m.ID,
		Name:         m.Name,
		Role:         m.Role,
		Model:        m.Model,
		Effort:       m.Effort,
		Skills:       nonNilStrings(m.Skills),
		Tools:        nonNilStrings(m.Tools),
		SystemPrompt: m.SystemPrompt,
		IsLeader:     m.IsLeader,
		Avatar:       m.Avatar,
	}
}

func toTeamTaskView(t teampkg.Team, tk teampkg.Task) TeamTaskView {
	acc := make([]TeamCriterionView, 0, len(tk.Acceptance))
	for _, c := range tk.Acceptance {
		acc = append(acc, TeamCriterionView{Text: c.Text, Done: c.Done})
	}
	return TeamTaskView{
		ID:             tk.ID,
		Title:          tk.Title,
		Desc:           tk.Desc,
		AssigneeID:     tk.AssigneeID,
		AssigneeName:   t.MemberName(tk.AssigneeID),
		RequiredSkills: nonNilStrings(tk.RequiredSkills),
		Status:         string(tk.Status),
		Column:         teampkg.ColumnFor(t, tk),
		Deps:           nonNilStrings(tk.Deps),
		ParentID:       tk.ParentID,
		Acceptance:     acc,
		Deliverable:    tk.Deliverable,
		Attempts:       tk.Attempts,
		Evidence:       nonNilStrings(tk.Evidence),
		Progress:       tk.Progress,
		Error:          tk.Error,
		Approval:       string(tk.Approval),
		ApprovalState:  string(tk.ApprovalState),
		ApprovalNote:   tk.ApprovalNote,
		Order:          tk.Order,
	}
}

func toTeamContextView(t teampkg.Team) TeamContextView {
	c := t.Context
	decisions := make([]TeamDecisionView, 0, len(c.Decisions))
	for _, d := range c.Decisions {
		decisions = append(decisions, TeamDecisionView{
			ID: d.ID, Text: d.Text, By: d.By, At: formatTeamTime(d.At),
		})
	}
	artifacts := make([]TeamArtifactView, 0, len(c.Artifacts))
	for _, art := range c.Artifacts {
		artifacts = append(artifacts, TeamArtifactView{
			ID: art.ID, Title: art.Title, Path: art.Path, TaskID: art.TaskID,
			Kind: art.Kind, Summary: art.Summary,
		})
	}
	notes := make([]TeamNoteView, 0, len(c.Notes))
	for _, n := range c.Notes {
		notes = append(notes, TeamNoteView{
			ID: n.ID, Author: n.Author, AuthorName: t.ResolveNoteAuthor(n.Author),
			TaskID: n.TaskID, Text: n.Text, At: formatTeamTime(n.At),
		})
	}
	checkpoints := make([]TeamCheckpointView, 0, len(c.Checkpoints))
	for _, ck := range c.Checkpoints {
		checkpoints = append(checkpoints, TeamCheckpointView{
			ID: ck.ID, Summary: ck.Summary, Count: ck.Count,
			Degraded: ck.Degraded, UpTo: formatTeamTime(ck.UpTo), At: formatTeamTime(ck.At),
		})
	}
	return TeamContextView{
		Goal:          c.Goal,
		Constraints:   c.Constraints,
		Decisions:     decisions,
		Artifacts:     artifacts,
		OpenQuestions: nonNilStrings(c.OpenQuestions),
		Notes:         notes,
		Checkpoints:   checkpoints,
		Version:       c.Version,
	}
}

func toTeamBoardView(t teampkg.Team) TeamBoardView {
	b := teampkg.BoardOf(t)
	cols := make([]TeamColumnView, 0, len(b.Columns))
	for _, c := range b.Columns {
		tasks := make([]TeamTaskView, 0, len(c.Tasks))
		for _, tk := range c.Tasks {
			tasks = append(tasks, toTeamTaskView(t, tk))
		}
		cols = append(cols, TeamColumnView{
			Key: c.Key, Label: c.Label, States: c.States, Tasks: tasks,
		})
	}
	counts := b.Counts
	if counts == nil {
		counts = map[string]int{}
	}
	return TeamBoardView{TeamID: b.TeamID, Columns: cols, Counts: counts, Total: b.Total}
}

func teamProjectViewToModel(tv TeamProjectView) teampkg.Team {
	members := make([]teampkg.Member, 0, len(tv.Members))
	for _, m := range tv.Members {
		members = append(members, memberViewToModel(m))
	}
	tasks := make([]teampkg.Task, 0, len(tv.Tasks))
	for _, tk := range tv.Tasks {
		tasks = append(tasks, taskViewToModel(tk))
	}
	return teampkg.Team{
		ID:      tv.ID,
		Name:    tv.Name,
		Goal:    tv.Goal,
		Members: members,
		Tasks:   tasks,
		Context: teampkg.TeamContext{
			Goal:          tv.Context.Goal,
			Constraints:   tv.Context.Constraints,
			Decisions:     decisionsToModel(tv.Context.Decisions),
			Artifacts:     artifactsToModel(tv.Context.Artifacts),
			OpenQuestions: trimList(tv.Context.OpenQuestions),
			Notes:         notesToModel(tv.Context.Notes),
		},
		Policy: policyToModel(tv.Policy),
	}
}

func memberViewToModel(mv TeamMemberView) teampkg.Member {
	return teampkg.Member{
		ID:           mv.ID,
		Name:         strings.TrimSpace(mv.Name),
		Role:         strings.TrimSpace(mv.Role),
		Model:        strings.TrimSpace(mv.Model),
		Effort:       strings.TrimSpace(mv.Effort),
		Skills:       teampkg.NormalizeSkills(mv.Skills),
		Tools:        trimList(mv.Tools),
		SystemPrompt: mv.SystemPrompt,
		IsLeader:     mv.IsLeader,
		Avatar:       strings.TrimSpace(mv.Avatar),
	}
}

func taskViewToModel(tv TeamTaskView) teampkg.Task {
	acc := make([]teampkg.Criterion, 0, len(tv.Acceptance))
	for _, c := range tv.Acceptance {
		acc = append(acc, teampkg.Criterion{Text: c.Text, Done: c.Done})
	}
	return teampkg.Task{
		ID:             tv.ID,
		Title:          strings.TrimSpace(tv.Title),
		Desc:           tv.Desc,
		AssigneeID:     tv.AssigneeID,
		RequiredSkills: teampkg.NormalizeSkills(tv.RequiredSkills),
		Status:         taskStateFromView(tv.Status),
		Deps:           trimList(tv.Deps),
		ParentID:       tv.ParentID,
		Acceptance:     acc,
		Deliverable:    tv.Deliverable,
		Attempts:       tv.Attempts,
		Evidence:       trimList(tv.Evidence),
		Progress:       tv.Progress,
		Error:          tv.Error,
		Approval:       approvalModeFromView(tv.Approval),
		ApprovalState:  teampkg.ApprovalState(strings.TrimSpace(tv.ApprovalState)),
		ApprovalNote:   strings.TrimSpace(tv.ApprovalNote),
		Order:          tv.Order,
	}
}

// approvalModeFromView maps the wire gate value onto the domain type. An
// unknown value degrades to none rather than failing the save, so a stale panel
// can never wedge the board.
func approvalModeFromView(s string) teampkg.ApprovalMode {
	m := teampkg.ApprovalMode(strings.TrimSpace(s))
	if !m.Valid() {
		return teampkg.ApprovalNone
	}
	return m
}

func decisionsToModel(ds []TeamDecisionView) []teampkg.Decision {
	if len(ds) == 0 {
		return nil
	}
	out := make([]teampkg.Decision, 0, len(ds))
	now := time.Now().UTC()
	for _, d := range ds {
		at := now
		if d.At != "" {
			if parsed, err := time.Parse(time.RFC3339, d.At); err == nil {
				at = parsed
			}
		}
		out = append(out, teampkg.Decision{ID: d.ID, Text: d.Text, By: d.By, At: at})
	}
	return out
}

func artifactsToModel(as []TeamArtifactView) []teampkg.Artifact {
	if len(as) == 0 {
		return nil
	}
	out := make([]teampkg.Artifact, 0, len(as))
	for _, a := range as {
		out = append(out, teampkg.Artifact{
			ID: a.ID, Title: a.Title, Path: a.Path, TaskID: a.TaskID,
			Kind: a.Kind, Summary: a.Summary,
		})
	}
	return out
}

// notesToModel converts wire notes back to the domain type. An empty author is
// the user (a note typed in the panel), which the digest renders as 用户.
func notesToModel(ns []TeamNoteView) []teampkg.Note {
	if len(ns) == 0 {
		return nil
	}
	out := make([]teampkg.Note, 0, len(ns))
	now := time.Now().UTC()
	for _, n := range ns {
		at := now
		if n.At != "" {
			if parsed, err := time.Parse(time.RFC3339, n.At); err == nil {
				at = parsed
			}
		}
		out = append(out, teampkg.Note{
			ID: n.ID, Author: n.Author, TaskID: n.TaskID, Text: n.Text, At: at,
		})
	}
	return out
}

func policyToModel(p TeamPolicyView) teampkg.Policy { return teampkg.Policy(p) }

// taskStateFromView maps a card's wire status onto the shared task lifecycle.
// An empty value is left empty so the store applies its queued default (rather
// than pinning a wrong state on create).
func taskStateFromView(s string) taskmonitor.TaskState {
	return taskmonitor.TaskState(strings.TrimSpace(s))
}

func formatTeamTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func nonNilStrings(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	return in
}

func firstNonBlankTeam(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
