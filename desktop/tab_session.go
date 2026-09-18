package main

// tabSession is the per-tab controller surface the desktop drives. It is
// satisfied structurally by *control.Controller (local tabs) and by remoteSession
// (tabs whose workspace lives on a remote host — WSL/Docker/SSH — where the
// real controller runs in the host process and every call crosses the RPC wire).
// Keeping the field name (WorkspaceTab.Ctrl) means every existing call site
// keeps compiling; the compiler polices the method set.

import (
	"context"
	"time"

	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/checkpoint"
	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/command"
	"github.com/zzycxz/hiq/internal/control"
	"github.com/zzycxz/hiq/internal/diff"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/jobs"
	"github.com/zzycxz/hiq/internal/memory"
	"github.com/zzycxz/hiq/internal/plugin"
	"github.com/zzycxz/hiq/internal/present"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/skill"
)

type tabSession interface {
	// Turn driving.
	Submit(input string)
	SubmitDisplay(display, input string)
	Steer(text string)
	FollowUp(input string)
	QueuedMessages() (steer []string, followUp []string)
	Cancel()
	Running() bool
	Turn() int
	WaitTurn(d time.Duration)
	Pause()
	ResumeTurn()
	Paused() bool
	RunShell(command string)
	Run(ctx context.Context, input string) error

	// Approvals / asks.
	Approve(id string, allow, session, persist bool)
	AnswerQuestion(id string, answers []event.AskAnswer)
	ReplayPendingPrompts()

	// Mode / goal / knowledge scope.
	SetMode(plan, autoApproveTools bool)
	SetPlanMode(v bool)
	PlanMode() bool
	SetToolApprovalMode(mode string)
	ToolApprovalMode() string
	SetAutoApproveTools(on bool)
	AutoApproveTools() bool
	SetGoal(goal string)
	Goal() string
	GoalStatus() string
	SetRAGScope(scope string)

	// Checkpoints / branches / summarize.
	Checkpoints() []checkpoint.Meta
	CheckpointDiff(turn int) []diff.Change
	CheckpointHasBoundary(turn int) bool
	RewindPreview(turn int) []control.RewindFileClass
	Rewind(turn int, scope control.RewindScope) error
	ForkSession(turn int, name string) (string, error)
	Branches() ([]agent.BranchInfo, error)
	SwitchBranch(ref string) (agent.BranchInfo, error)
	SummarizeFrom(ctx context.Context, turn int) error
	SummarizeUpTo(ctx context.Context, turn int) error

	// Session state / persistence.
	Resume(s *agent.Session, path string)
	SetSessionPath(p string)
	SessionPath() string
	SessionDir() string
	History() []provider.Message
	PresentRecords() (records []present.Record, rewriteVersion int, ok bool)
	SearchSessionText(query string) []agent.SearchHit
	Snapshot() error
	NewSession() error
	ClearSession() error
	Compact(ctx context.Context, instructions string) error

	// Read-only surface the UI polls.
	Label() string
	ContextSnapshot() (int, int)
	CompactRatio() float64
	LastUsage() *provider.Usage
	Jobs() []jobs.View
	Commands() []command.Command
	Skills() []skill.Skill
	AllSkills() []skill.Skill
	DisabledSkills() []skill.Skill
	ConfiguredMCPNames() []string
	DisconnectedMCPNames() []string
	Host() *plugin.Host
	SkillEnabled(name string) bool
	SetSkillEnabled(name string, enabled bool) error

	// Dream/distill (host-side in P2; remote sessions report none).
	TriggerDream(ctx context.Context) (agent.DreamRun, bool)
	TriggerDistill(ctx context.Context) (agent.DreamRun, bool)
	LastDreamRun(kind agent.DreamKind) (agent.DreamRun, bool)

	// MCP hot-add (desktop-local plugin host; remote sessions reject).
	AddMCPServer(e config.PluginEntry) (int, error)
	ConnectMCPServer(e config.PluginEntry) (int, error)
	DisconnectMCPServer(name string) bool
	RemoveMCPServer(name string) (disconnected bool, err error)
	ConnectCodegraphMCPServer(cfg *config.Config) (int, error)

	// Memory / profile (host-side in P2; remote sessions return zero values).
	Memory() *memory.Set
	QuickAdd(scope memory.Scope, note string) (string, error)
	SaveDoc(path, body string) (string, error)
	ForgetMemory(name string) error
	QueueMemory(note string)
	Profile() control.ProfileView
	ProfilePresets() control.ProfilePresetsView
	SaveProfilePresets(f memory.PresetFile) (string, error)

	// Expert collab card (local expert sessions only).
	AppendExpertCollab(content string) error
	EmitExpertCollab(collab event.Collab)
	DeleteExpertCollab(ordinal int) error

	Close()
}
