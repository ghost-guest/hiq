// Package remotehost is the hiq remote-workspace host: a headless process
// (spawned inside WSL, a container, or over SSH) that owns the real controllers
// for remote workspaces and exposes them to the desktop over a stdio NDJSON
// JSON-RPC connection. The desktop is a thin client: turns, tools, files, git,
// and session storage all execute on this side.
//
// The wire protocol reuses internal/acp's Conn (a transport-agnostic
// bidirectional JSON-RPC 2.0 link). Unlike ACP — an editor-facing protocol with
// its own update vocabulary — this surface mirrors what the desktop drives on a
// local *control.Controller, and the event stream uses the shared eventwire
// shape, so a remote tab's UI is pixel-equivalent to a local one.
package remotehost

import (
	"encoding/json"

	"github.com/zzycxz/hiq/internal/eventwire"
)

// --- host-level ------------------------------------------------------------

// HelloResult is the reply to host/hello. The desktop compares Version against
// its own build to decide whether to re-provision the host binary.
type HelloResult struct {
	Version    string `json:"version"`
	Goos       string `json:"goos"`
	Arch       string `json:"arch"`
	Home       string `json:"home"`
	ConfigRoot string `json:"configRoot"`
	// HasModelConfig reports whether this side already has at least one provider
	// with a resolvable API key (host/configure skips writing when true).
	HasModelConfig bool `json:"hasModelConfig"`
}

// ConfigureParams is the desktop pushing its model configuration so a fresh
// remote install can run without local setup. Keys land in the remote secret
// store; the provider entries land in the remote user config. Writing is
// skipped entirely when the remote side already has a usable provider.
type ConfigureParams struct {
	DefaultModel string             `json:"defaultModel"`
	Providers    []ProviderSnapshot `json:"providers"`
}

// ProviderSnapshot is one provider entry to mirror remotely.
type ProviderSnapshot struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	BaseURL   string   `json:"base_url,omitempty"`
	APIKeyEnv string   `json:"apiKeyEnv,omitempty"`
	APIKey    string   `json:"apiKey,omitempty"` // written to the remote secret store
	Models    []string `json:"models,omitempty"`
	ContextWindow int  `json:"contextWindow,omitempty"`
	Vision    bool     `json:"vision,omitempty"`
}

type ConfigureResult struct {
	Configured        bool `json:"configured"`
	AlreadyConfigured bool `json:"alreadyConfigured"`
}

// --- sessions ---------------------------------------------------------------

// SessionNewParams opens a session rooted at Cwd, mirroring the desktop's
// buildTabController: config load + model fallback resolution + boot.Build with
// the per-project session dir, then the tab's persisted mode knobs are applied.
type SessionNewParams struct {
	SessionID string `json:"sessionId,omitempty"` // caller-chosen id (desktop tab id); generated when empty
	Cwd       string `json:"cwd"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Profile   string `json:"profile,omitempty"`
	Mode      string `json:"mode,omitempty"`             // normal | plan | yolo | plan-yolo
	ToolApprovalMode string `json:"toolApprovalMode,omitempty"`
	RagScope  string `json:"ragScope,omitempty"`
	Goal      string `json:"goal,omitempty"`
	// SessionPath pins the exact transcript to continue (tab restore); empty =
	// fresh session file.
	SessionPath string `json:"sessionPath,omitempty"`
}

type SessionNewResult struct {
	SessionID   string `json:"sessionId"`
	SessionPath string `json:"sessionPath"`
	Label       string `json:"label"`
}

// SessionRef addresses an existing session.
type SessionRef struct {
	SessionID string `json:"sessionId"`
}

type SubmitParams struct {
	SessionID string `json:"sessionId"`
	Input     string `json:"input"`
	Display   string `json:"display,omitempty"`
}

type SteerParams struct {
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
}

type FollowUpParams struct {
	SessionID string `json:"sessionId"`
	Input     string `json:"input"`
}

type QueuedResult struct {
	Steer    []string `json:"steer"`
	FollowUp []string `json:"followUp"`
}

type RunShellParams struct {
	SessionID string `json:"sessionId"`
	Command   string `json:"command"`
}

type ApproveParams struct {
	SessionID string `json:"sessionId"`
	ID        string `json:"id"`
	Allow     bool   `json:"allow"`
	Session   bool   `json:"session"`
	Persist   bool   `json:"persist"`
}

// AskAnswer is the wire form of event.AskAnswer (which has no JSON tags).
type AskAnswer struct {
	QuestionID string   `json:"questionId"`
	Selected   []string `json:"selected"`
}

type AnswerParams struct {
	SessionID string      `json:"sessionId"`
	ID        string      `json:"id"`
	Answers   []AskAnswer `json:"answers"`
}

type SetModeParams struct {
	SessionID string `json:"sessionId"`
	Mode      string `json:"mode"`
}

type SetToolApprovalModeParams struct {
	SessionID string `json:"sessionId"`
	Mode      string `json:"mode"`
}

type SetGoalParams struct {
	SessionID string `json:"sessionId"`
	Goal      string `json:"goal"`
}

type GoalStatusResult struct {
	Goal   string `json:"goal"`
	Status string `json:"status"`
}

type SetRagScopeParams struct {
	SessionID string `json:"sessionId"`
	Scope     string `json:"scope"`
}

type CompactParams struct {
	SessionID    string `json:"sessionId"`
	Instructions string `json:"instructions"`
}

type NewSessionParams struct {
	SessionID string `json:"sessionId"`
}

type NewSessionResult struct {
	SessionPath string `json:"sessionPath"`
}

type SetSessionPathParams struct {
	SessionID   string `json:"sessionId"`
	SessionPath string `json:"sessionPath"`
}

// SessionStateResult batches the small getters the desktop polls per tab, so a
// remote reconcile costs one round-trip instead of six.
type SessionStateResult struct {
	Running           bool             `json:"running"`
	Paused            bool             `json:"paused"`
	Label             string           `json:"label"`
	Model             string           `json:"model"`
	WorkspaceRoot     string           `json:"workspaceRoot"`
	SessionPath       string           `json:"sessionPath"`
	SessionDir        string           `json:"sessionDir"`
	ToolApprovalMode  string           `json:"toolApprovalMode"`
	PlanMode          bool             `json:"planMode"`
	Goal              string           `json:"goal"`
	GoalStatus        string           `json:"goalStatus"`
	ContextUsed       int              `json:"contextUsed"`
	ContextWindow     int              `json:"contextWindow"`
	CompactRatio      float64          `json:"compactRatio"`
}

type HistoryResult struct {
	Messages json.RawMessage `json:"messages"` // []provider.Message
}

type PresentResult struct {
	Records        json.RawMessage `json:"records"`        // []present.Record
	RewriteVersion int             `json:"rewriteVersion"`
	OK             bool            `json:"ok"`
}

type CheckpointsResult struct {
	Checkpoints json.RawMessage `json:"checkpoints"` // []checkpoint.Meta
}

type CheckpointDiffParams struct {
	SessionID string `json:"sessionId"`
	Turn      int    `json:"turn"`
}

type CheckpointDiffResult struct {
	Changes json.RawMessage `json:"changes"` // []diff.Change
}

type CheckpointHasBoundaryResult struct {
	Has bool `json:"has"`
}

// RewindPreviewResult carries the per-path safety classification
// ([]control.RewindFileClass) for a code-scope rewind preview.
type RewindPreviewResult struct {
	Classes json.RawMessage `json:"classes"`
}

type RewindParams struct {
	SessionID string `json:"sessionId"`
	Turn      int    `json:"turn"`
	Scope     string `json:"scope"` // "code" | "conversation" | "both"
}

type ForkParams struct {
	SessionID string `json:"sessionId"`
	Turn      int    `json:"turn"`
	Name      string `json:"name,omitempty"`
}

type ForkResult struct {
	SessionPath string `json:"sessionPath"`
}

type BranchesResult struct {
	Branches json.RawMessage `json:"branches"` // []agent.BranchInfo
	Current  json.RawMessage `json:"current,omitempty"`
}

type SwitchBranchParams struct {
	SessionID string `json:"sessionId"`
	Ref       string `json:"ref"`
}

type SummarizeParams struct {
	SessionID string `json:"sessionId"`
	Turn      int    `json:"turn"`
	UpTo      bool   `json:"upTo"`
}

type SetModelParams struct {
	SessionID string `json:"sessionId"`
	Model     string `json:"model"`
	Effort    string `json:"effort,omitempty"`
}

type SetModelResult struct {
	Label string `json:"label"`
}

// SessionListParams enumerates the host-side transcripts stored under a root.
type SessionListParams struct {
	Cwd string `json:"cwd"`
}

type SessionEntry struct {
	Path      string `json:"path"`
	ModTimeMs int64  `json:"modTimeMs"`
	// Enriched from the .meta sidecar when present (the same metadata the
	// desktop's local tree reads from its own session dirs).
	Turns         int    `json:"turns,omitempty"`
	TopicID       string `json:"topicId,omitempty"`
	TopicTitle    string `json:"topicTitle,omitempty"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	Scope         string `json:"scope,omitempty"`
	Preview       string `json:"preview,omitempty"`
}

type SessionListResult struct {
	Sessions []SessionEntry `json:"sessions"`
}

// --- filesystem / git (host-side execution, rooted at the session cwd) ------

type FsListParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"` // relative to the session root; "" = root
}

type FsEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
}

type FsListResult struct {
	Entries []FsEntry `json:"entries"`
}

type FsReadParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
}

type FsReadResult struct {
	Kind      string `json:"kind"` // "text" | "image" | "pdf" | "video" | "audio" | "office" | "binary" | "missing"
	Mime      string `json:"mime,omitempty"`
	Text      string `json:"text,omitempty"`
	DataURL   string `json:"dataUrl,omitempty"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated,omitempty"`
}

type FsSearchParams struct {
	SessionID string `json:"sessionId"`
	Query     string `json:"query"`
}

type FsSearchResult struct {
	Results json.RawMessage `json:"results"` // []fileref.SearchResult
}

type GitStatusResult struct {
	Root     string     `json:"root"`
	Branch   string     `json:"branch"` // "" + Detached on detached HEAD; empty repo → IsRepo=false
	Detached bool       `json:"detached"`
	IsRepo   bool       `json:"isRepo"`
	Entries  []GitEntry `json:"entries"`
	// Added/Removed are `git diff --numstat HEAD` line totals (binary "-"
	// columns skipped); a failed probe leaves both zero.
	Added   int `json:"added,omitempty"`
	Removed int `json:"removed,omitempty"`
}

type GitEntry struct {
	Path   string `json:"path"`
	Change string `json:"change"` // porcelain XY code, e.g. "M", "A", "??"
}

// --- host → desktop notifications ------------------------------------------

// EventParams carries one controller event for a session.
type EventParams struct {
	SessionID string          `json:"sessionId"`
	Event     eventwire.Event `json:"event"`
}

// PermissionRequestParams is the outbound round-trip the host makes while the
// run loop is blocked on a gated tool call; the desktop replies with the
// user's decision (or an error/deny on disconnect).
type PermissionRequestParams struct {
	SessionID string          `json:"sessionId"`
	Event     eventwire.Event `json:"event"` // the approval_request wire event
}

type PermissionRequestResult struct {
	Allow   bool `json:"allow"`
	Session bool `json:"session"`
	Persist bool `json:"persist"`
}

// AskRequestParams is the outbound round-trip for ask_request events.
type AskRequestParams struct {
	SessionID string          `json:"sessionId"`
	Event     eventwire.Event `json:"event"` // the ask_request wire event
}

type AskRequestResult struct {
	Answers []AskAnswer `json:"answers"`
}
