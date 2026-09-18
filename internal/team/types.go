// Package team implements the 团队 (team) feature: a long-lived multi-agent
// collaboration project with a leader (团长) + members (团员) and a kanban
// board.
//
// Relationship to the sibling package internal/experts:
//   - experts = 专家团: one-shot multi-model collaboration (parallel / debate /
//     pipeline) over a roster of stateless experts.
//   - team = 团队: a persistent project with a single leader that plans and
//     assigns work, members that each own an independent context, a shared team
//     blackboard, and a kanban board tracking every task's progress.
//
// The package is deliberately pure domain + persistence: it holds no provider,
// no tools and no UI. Execution (running a member as an isolated sub-session)
// and LLM-backed planning live in the desktop layer, which owns the provider
// wiring. That keeps this package independently testable and keeps the kernel
// dependency direction (team → taskmonitor only) intact.
//
// Open-source grounding for the design:
//   - Magentic-One (Microsoft): a lead Orchestrator that plans, tracks and
//     re-plans, keeping a task ledger + a progress ledger.
//   - Claude Agent Teams (Anthropic): a team lead + teammates, each teammate
//     working in its own context window, with shared task management.
//   - CrewAI: role/goal/skills per member + hierarchical manager that routes
//     work by capability.
//   - MetaGPT: SOP roles producing verifiable, structured artifacts.
package team

import (
	"strings"
	"time"

	"github.com/zzycxz/fairpeer/internal/taskmonitor"
)

// DefaultPolicy is applied when a Team leaves a policy field at its zero value.
const (
	defaultMaxRounds   = 12
	defaultMaxParallel = 3
)

// Member is one team participant: the leader (团长) or a member (团员).
//
// A member is not just a name — Role/Skills drive the leader's capability
// routing (R5), and SystemPrompt frames identity ("你就是 X") so an execution
// backend can spin it up as an isolated sub-agent with its own context (R6).
type Member struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Role is the member's responsibility in one line (CrewAI's `role`),
	// e.g. "后端实现" / "测试与验收".
	Role string `json:"role,omitempty"`
	// Model is an optional "provider/model" ref. Empty = the session default
	// model. Members may deliberately run on a cheaper model.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Skills are capability tags used for routing a task to a member (R5).
	// Matching is case-insensitive and order-independent.
	Skills []string `json:"skills,omitempty"`
	// Tools optionally narrows the tool whitelist for this member's runs.
	// Empty = inherit the default tool set.
	Tools []string `json:"tools,omitempty"`
	// SystemPrompt frames the member's identity/behaviour. When empty, the
	// execution layer composes one from Name + Role + Skills.
	SystemPrompt string `json:"system_prompt,omitempty"`
	// IsLeader marks the single 团长. A team has at most one leader.
	IsLeader bool `json:"is_leader,omitempty"`
	// Avatar is an optional emoji/short glyph for the board.
	Avatar    string    `json:"avatar,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Criterion is one acceptance check on a task (MetaGPT-style structured
// artifact): a deliverable is "done" only when its criteria are satisfied.
type Criterion struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// ApprovalMode declares where a card's human-in-the-loop (HITL) gate sits. The
// gate is expressed as card state only (Task.ApprovalState), so the shared
// taskmonitor lifecycle is never forked; the board derives a 待确认 column from
// it instead (see ColumnFor).
type ApprovalMode string

const (
	// ApprovalNone runs the card unattended (the default).
	ApprovalNone ApprovalMode = ""
	// ApprovalBefore requires a human to confirm before the card may run.
	ApprovalBefore ApprovalMode = "before"
	// ApprovalAfter lets the member run, but requires a human to confirm the
	// deliverable before the card counts as done.
	ApprovalAfter ApprovalMode = "after"
)

// Valid reports whether m is a known gate.
func (m ApprovalMode) Valid() bool {
	switch m {
	case ApprovalNone, ApprovalBefore, ApprovalAfter:
		return true
	default:
		return false
	}
}

// ApprovalState is a card's live gate status.
type ApprovalState string

const (
	ApprovalStateNone     ApprovalState = ""
	ApprovalStatePending  ApprovalState = "pending"
	ApprovalStateApproved ApprovalState = "approved"
	ApprovalStateRejected ApprovalState = "rejected"
)

// Checkpoint is a rolling summary of shared notes that have aged out of the
// digest window (open-vetta 移植 P1-4 步骤 B).
//
// The hot window + the paging tool already make old notes reachable, but
// reachable is not the same as known: a member only learns that something
// relevant exists if it knows to page. A checkpoint is the middle layer —
// a compact summary of everything older, rendered identically into every
// member's digest, so the team's accumulated knowledge stays visible in
// bounded space.
//
// Degraded marks a checkpoint produced without an LLM (the leader call failed
// or timed out): it is an index of first lines rather than a real synthesis.
// It is recorded honestly rather than silently, so a later pass can replace it.
type Checkpoint struct {
	ID   string    `json:"id"`
	UpTo time.Time `json:"up_to"`
	// Count is how many notes this checkpoint folds in.
	Count int `json:"count"`
	// Summary is the synthesized (or degraded) text.
	Summary string `json:"summary"`
	// Degraded is true when Summary is an index-style fallback.
	Degraded bool      `json:"degraded,omitempty"`
	At       time.Time `json:"at"`
}

// Note is one member-contributed entry on the shared context (P4 共享上下文):
// a finding, a caveat or a hand-off hint posted while a member works. Unlike a
// Decision (a settled call made by the leader) a note is raw working state any
// member may write, and every later member reads it in its blackboard digest —
// so knowledge flows sideways between contexts that never meet.
type Note struct {
	ID string `json:"id"`
	// Author is the posting member's ID ("" = the user, via the panel).
	Author string `json:"author,omitempty"`
	// TaskID is the card the note came from, when known.
	TaskID string    `json:"task_id,omitempty"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

// Task is a single kanban card (R3/R4). Its Status is the SAME state machine the
// rest of the kernel already speaks (taskmonitor.TaskState) so the board does
// not fork a second lifecycle — see Board() for the state→column mapping.
type Task struct {
	ID     string `json:"id"`
	TeamID string `json:"team_id"`
	Title  string `json:"title"`
	Desc   string `json:"desc,omitempty"`
	// AssigneeID is the owning member; empty = unassigned (Backlog).
	AssigneeID string `json:"assignee_id,omitempty"`
	// RequiredSkills drive capability routing (R5): the leader matches these
	// against each member's Skills.
	RequiredSkills []string              `json:"required_skills,omitempty"`
	Status         taskmonitor.TaskState `json:"status"`
	// Deps lists task IDs that must reach a terminal-success state first.
	Deps []string `json:"deps,omitempty"`
	// ParentID nests this task under another (WBS level 2).
	ParentID string `json:"parent_id,omitempty"`
	// Acceptance is the definition of done.
	Acceptance []Criterion `json:"acceptance,omitempty"`
	// Deliverable holds the produced result: text, or a file path.
	Deliverable string `json:"deliverable,omitempty"`
	// Attempts counts execution attempts (retry/re-plan visibility).
	Attempts int `json:"attempts,omitempty"`
	// Evidence records supporting notes (alignment checks, references).
	Evidence []string `json:"evidence,omitempty"`
	// Progress is the latest streamed snippet while running (board liveness).
	Progress string `json:"progress,omitempty"`
	// Error holds the last failure reason (state == failed/stale).
	Error string `json:"error,omitempty"`
	// Approval declares this card's HITL gate ("" = unattended).
	Approval ApprovalMode `json:"approval,omitempty"`
	// ApprovalState is the live gate status (pending/approved/rejected). It is
	// what the board's 待确认 column keys off, and it never touches the
	// taskmonitor state machine.
	ApprovalState ApprovalState `json:"approval_state,omitempty"`
	// ApprovalNote carries the human's confirmation/rejection reason.
	ApprovalNote string `json:"approval_note,omitempty"`
	// Order is the manual sort key within a column (ascending).
	Order     int       `json:"order,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Decision is a settled choice recorded on the team blackboard so members don't
// relitigate it (Magentic-One's task ledger, blackboard's "decisions" slot).
type Decision struct {
	ID   string    `json:"id"`
	Text string    `json:"text"`
	By   string    `json:"by,omitempty"`
	At   time.Time `json:"at"`
}

// Artifact indexes something the team produced (a file, a document, a result).
type Artifact struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Path   string `json:"path,omitempty"`
	TaskID string `json:"task_id,omitempty"`
	Kind   string `json:"kind,omitempty"`
	// Summary is a one-line excerpt of the deliverable, so a later member sees
	// the gist of what was produced without opening the file — the shared-context
	// payoff of 团队.
	Summary string `json:"summary,omitempty"`
}

// TeamContext is the L0 shared blackboard (R6): goal + constraints + decisions +
// artifact index + open questions. The leader writes it; members read a compact
// projection of it before每个 task so they stay aligned with the team direction.
//
// P4 adds Notes: the member-writable half of "共享上下文". Decisions/artifacts are
// the leader's settled state; notes are the working chatter members contribute
// sideways (see Note).
//
// Version is monotonic so downstream consumers (and prompt caches) can tell
// when the blackboard changed.
type TeamContext struct {
	Goal          string       `json:"goal,omitempty"`
	Constraints   string       `json:"constraints,omitempty"`
	Decisions     []Decision   `json:"decisions,omitempty"`
	Artifacts     []Artifact   `json:"artifacts,omitempty"`
	OpenQuestions []string     `json:"open_questions,omitempty"`
	Notes         []Note       `json:"notes,omitempty"`
	Checkpoints   []Checkpoint `json:"checkpoints,omitempty"`
	Version       int          `json:"version"`
}

// Policy bounds a team's autonomy (guards against runaway cost/loops).
type Policy struct {
	// MaxRounds caps orchestration rounds.
	MaxRounds int `json:"max_rounds,omitempty"`
	// MaxParallel caps concurrently running member tasks.
	MaxParallel int `json:"max_parallel,omitempty"`
	// AutoAssign lets the leader route an unassigned task automatically.
	AutoAssign bool `json:"auto_assign"`
	// AutoReplan lets the leader re-plan (reassign/retry) on failure.
	AutoReplan bool `json:"auto_replan"`
}

// Team is one collaboration project.
type Team struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Goal      string      `json:"goal,omitempty"`
	Members   []Member    `json:"members"`
	Tasks     []Task      `json:"tasks"`
	Context   TeamContext `json:"context"`
	Policy    Policy      `json:"policy"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// Leader returns the team's leader, if one is set.
func (t Team) Leader() (Member, bool) {
	for _, m := range t.Members {
		if m.IsLeader {
			return m, true
		}
	}
	return Member{}, false
}

// Member returns a member by ID.
func (t Team) Member(id string) (Member, bool) {
	for _, m := range t.Members {
		if m.ID == id {
			return m, true
		}
	}
	return Member{}, false
}

// Task returns a task by ID.
func (t Team) Task(id string) (Task, bool) {
	for _, tk := range t.Tasks {
		if tk.ID == id {
			return tk, true
		}
	}
	return Task{}, false
}

// MemberName resolves a member's display name, falling back to the raw ID and
// then to a "未分配" placeholder for an empty assignee.
func (t Team) MemberName(id string) string {
	if id == "" {
		return ""
	}
	if m, ok := t.Member(id); ok {
		return m.Name
	}
	return id
}

// normalize fills zero-value defaults and trims obvious noise so a directly
// constructed Team (e.g. from an LLM draft) is always storable.
func (t *Team) normalize() {
	t.Name = strings.TrimSpace(t.Name)
	if strings.TrimSpace(t.Context.Goal) == "" {
		t.Context.Goal = t.Goal
	}
	// A team that never expressed a policy at all (every field at its zero
	// value) gets the collaborative defaults, including auto-replan so a failed
	// card self-heals instead of sitting still on the board. A team that DID
	// express a policy keeps it verbatim — that is what makes an explicit
	// `auto_replan = false` stick.
	if t.Policy.MaxRounds <= 0 && t.Policy.MaxParallel <= 0 &&
		!t.Policy.AutoAssign && !t.Policy.AutoReplan {
		t.Policy.AutoReplan = true
	}
	if t.Policy.MaxRounds <= 0 {
		t.Policy.MaxRounds = defaultMaxRounds
	}
	if t.Policy.MaxParallel <= 0 {
		t.Policy.MaxParallel = defaultMaxParallel
	}
	ensureSingleLeader(t.Members)
	if t.Context.Version == 0 {
		t.Context.Version = 1
	}
	// Bound the checkpoint list the same way the note window is bounded: a
	// hand-edited or repeatedly-checkpointed store must not grow without limit.
	if len(t.Context.Checkpoints) > MaxCheckpoints {
		t.Context.Checkpoints = t.Context.Checkpoints[len(t.Context.Checkpoints)-MaxCheckpoints:]
	}
	// A card without a gate can never be sitting in 待确认: repair any stale gate
	// state so the board's approval column is always honest (e.g. after a
	// hand-edited store file or an upgrade).
	repairApprovals(t.Tasks)
}

// repairApprovals enforces the HITL invariant that gate state only exists while
// a gate is declared: an unknown mode degrades to ApprovalNone, and a card with
// no gate carries no pending/approved/rejected state.
func repairApprovals(tasks []Task) {
	for i := range tasks {
		if !tasks[i].Approval.Valid() {
			tasks[i].Approval = ApprovalNone
		}
		if tasks[i].Approval == ApprovalNone {
			tasks[i].ApprovalState = ApprovalStateNone
		}
	}
}

// ensureSingleLeader enforces the "有且仅有一个团长" invariant: if more than one
// member is flagged, only the first is kept as leader; the rest are demoted.
func ensureSingleLeader(members []Member) {
	seen := false
	for i := range members {
		if members[i].IsLeader {
			if seen {
				members[i].IsLeader = false
				continue
			}
			seen = true
		}
	}
}
