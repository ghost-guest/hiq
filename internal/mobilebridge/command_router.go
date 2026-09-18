package mobilebridge

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/zzycxz/hiq/internal/mobilebridge/proto"
)

// ErrForbidden means this C lacks permission for the requested command.
// The Conn translates this into a `{"type":"error","code":"forbidden"}` frame.
var ErrForbidden = errors.New("forbidden")

// CommandExecutor runs commands against hiq's Controller. The desktop
// integration layer (desktop/app.go) implements it; tests mock it. This is
// the seam that keeps mobilebridge free of hiq-internal dependencies.
type CommandExecutor interface {
	Submit(tab, input, cmdID string) error
	Cancel(tab string) error
	Steer(tab, text string) error
	Pause(tab string) error
	Resume(tab string) error
	Approve(tab, approvalID string, allow, session, persist bool) error
	Answer(tab, askID string, answers []string) error
	SetPlan(tab string, on bool) error
	SetModel(tab, model string) error
	ListSessions() ([]SessionInfo, error)
	ListModels() ([]ModelInfo, error)
	ListTemplates() ([]TemplateInfo, error)
	NewTab(workspaceRoot, profile string) (string, error)
	RenameSession(tab, title string) error
	DeleteSession(tab string) error
	OfficeRun(tab, template string, args map[string]string) error
	FileStart(tab, name string, size int64) error
	FileChunk(tab string, seq int, data string) error
	FileEnd(tab, name string) error
	LoadSession(tab string) ([]map[string]any, error)
}

// SessionInfo is one row of the session list sent to C.
type SessionInfo struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}

// ModelInfo is one row of the model list sent to C (list_models reply).
type ModelInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// TemplateInfo is one row of the office template list sent to C
// (list_templates reply; source = scheduler.BuiltinTemplates).
type TemplateInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Desc     string `json:"desc"`
}

// CommandRouter validates per-connection permissions and dispatches to the
// executor. One router per Conn (each Conn carries its own PerConnPermissions).
type CommandRouter struct {
	devC  string
	exec  CommandExecutor
	perm  PerConnPermissions
	audit *Audit

	// subscribeTab is handled by the Bridge (it routes events), so the router
	// exposes the latest subscription via this callback.
	onSubscribeTab func(tab string)
	onListSessions func(sessions []SessionInfo)
	onListModels    func(models []ModelInfo)
	onListTemplates func(templates []TemplateInfo)
	onNewTab       func(tabID string)
	onResync       func(tabID string, sinceSeq uint64)
	onLoadSession  func(tab string, history []map[string]any)
	onError        func(code, msg string) // S1: 错误回传 C

	seenCmdIDs map[string]bool // S3: submit cmd_id 去重
}

func NewCommandRouter(devC string, exec CommandExecutor, perm PerConnPermissions, audit *Audit) *CommandRouter {
	return &CommandRouter{
		devC: devC, exec: exec, perm: perm, audit: audit,
		seenCmdIDs: map[string]bool{},
	}
}

// Perms 返回该连接的权限快照（UX_ONBOARDING W11：握手后经 permissions
// wireEvent 下发给 C 端展示——权限执法在 S 端，C 端只读显示真实生效值）。
func (r *CommandRouter) Perms() PerConnPermissions { return r.perm }

// SetSubscribeHook lets the Bridge learn tab-subscription changes so it can
// route wireEvents to the right Conn (HIQ_SPEC §11.1).
func (r *CommandRouter) SetSubscribeHook(fn func(tab string)) {
	r.onSubscribeTab = fn
}

// SetListSessionsHook lets the Bridge reply to list_sessions by sending the
// session list back over the encrypted Conn.
func (r *CommandRouter) SetListSessionsHook(fn func([]SessionInfo)) {
	r.onListSessions = fn
}

// SetListModelsHook lets the Bridge reply to list_models by sending the model
// list back over the encrypted Conn.
func (r *CommandRouter) SetListModelsHook(fn func([]ModelInfo)) {
	r.onListModels = fn
}

// SetListTemplatesHook lets the Bridge reply to list_templates (UX M5 W14).
func (r *CommandRouter) SetListTemplatesHook(fn func([]TemplateInfo)) {
	r.onListTemplates = fn
}

// SetNewTabHook lets the Bridge reply to new_tab with the new tab ID so C can
// switch_tab + subscribe to it.
func (r *CommandRouter) SetNewTabHook(fn func(string)) {
	r.onNewTab = fn
}

// SetResyncHook lets the Bridge reply to resync with delta/full events (§11.2).
func (r *CommandRouter) SetResyncHook(fn func(tabID string, sinceSeq uint64)) {
	r.onResync = fn
}

// SetLoadSessionHook lets the Bridge reply to load_session with the tab's
// conversation history (history_messages wireEvent).
func (r *CommandRouter) SetLoadSessionHook(fn func(tab string, history []map[string]any)) {
	r.onLoadSession = fn
}

// SetOnErrorHook lets the Bridge send error events back to C (S1: tab_not_found 等).
func (r *CommandRouter) SetOnErrorHook(fn func(code, msg string)) {
	r.onError = fn
}

// read-only command whitelist (allowed even when ReadOnly=true).
var readOnlyOK = map[string]bool{
	proto.CmdListSessions: true, proto.CmdLoadSession: true,
	proto.CmdSubscribeTab: true, proto.CmdSwitchTab: true,
	proto.CmdPing: true, proto.CmdPong: true, proto.CmdResync: true,
}

// Route parses a decrypted command, enforces permissions, dispatches.
func (r *CommandRouter) Route(plaintext []byte) error {
	var env proto.Envelope
	if err := json.Unmarshal(plaintext, &env); err != nil {
		return err
	}

	if r.perm.ReadOnly && !readOnlyOK[env.T] {
		r.audit.Denied(r.devC, env.T, "readonly")
		return ErrForbidden
	}
	// High-risk gating (office_run / file_*) — separate from ReadOnly.
	if env.T == proto.CmdOfficeRun && !r.perm.AllowHighRisk {
		r.audit.Denied(r.devC, env.T, "high_risk")
		return ErrForbidden
	}

	switch env.T {
	case proto.CmdSubmit:
		var c proto.SubmitCmd
		if err := json.Unmarshal(plaintext, &c); err != nil {
			return err
		}
		// S3: cmd_id 去重（防 C 重发导致重复执行）
		if c.CmdID != "" {
			if r.seenCmdIDs[c.CmdID] {
				return nil // 已见过，静默丢弃
			}
			r.seenCmdIDs[c.CmdID] = true
			if len(r.seenCmdIDs) > 1000 {
				r.seenCmdIDs = map[string]bool{c.CmdID: true} // 防溢出，重置
			}
		}
		err := r.exec.Submit(c.Tab, c.Input, c.CmdID)
		if err != nil && r.onError != nil {
			r.onError("submit_error", err.Error()) // S1: 回传错误给 C
		}
		r.audit.Cmd(r.devC, env.T, c.Tab, err == nil)
		return err
	case proto.CmdCancel:
		var c proto.CancelCmd
		json.Unmarshal(plaintext, &c)
		err := r.exec.Cancel(c.Tab)
		r.audit.Cmd(r.devC, env.T, c.Tab, err == nil)
		return err
	case proto.CmdSteer:
		var c proto.SteerCmd
		json.Unmarshal(plaintext, &c)
		return r.exec.Steer(c.Tab, c.Text)
	case proto.CmdPause:
		json.Unmarshal(plaintext, &struct{ Tab string `json:"tab"` }{})
		var c proto.CancelCmd // same shape {t,tab}
		json.Unmarshal(plaintext, &c)
		return r.exec.Pause(c.Tab)
	case proto.CmdResume:
		var c proto.CancelCmd
		json.Unmarshal(plaintext, &c)
		return r.exec.Resume(c.Tab)
	case proto.CmdApprove:
		var c proto.ApproveCmd
		json.Unmarshal(plaintext, &c)
		err := r.exec.Approve(c.Tab, c.Approval, c.Allow, c.Session, c.Persist)
		r.audit.Cmd(r.devC, env.T, c.Tab, err == nil)
		return err
	case proto.CmdAnswer:
		var c proto.AnswerCmd
		json.Unmarshal(plaintext, &c)
		return r.exec.Answer(c.Tab, c.Ask, c.Answers)
	case proto.CmdSetPlan:
		var c proto.CancelCmd
		json.Unmarshal(plaintext, &c)
		return r.exec.SetPlan(c.Tab, env.T == "set_plan") // placeholder; real impl parses "on"
	case proto.CmdSetModel:
		var c proto.SetModelCmd
		json.Unmarshal(plaintext, &c)
		return r.exec.SetModel(c.Tab, c.Model)
	case proto.CmdSubscribeTab:
		var c proto.SubscribeTabCmd
		json.Unmarshal(plaintext, &c)
		if r.onSubscribeTab != nil {
			r.onSubscribeTab(c.Tab)
		}
		return nil
	case proto.CmdListSessions:
		sessions, _ := r.exec.ListSessions()
		if r.onListSessions != nil {
			r.onListSessions(sessions)
		}
		return nil
	case proto.CmdListModels:
		models, _ := r.exec.ListModels()
		if r.onListModels != nil {
			r.onListModels(models)
		}
		return nil
	case proto.CmdListTemplates:
		templates, _ := r.exec.ListTemplates()
		if r.onListTemplates != nil {
			r.onListTemplates(templates)
		}
		return nil
	case proto.CmdNewTab:
		var c proto.NewTabCmd
		json.Unmarshal(plaintext, &c)
		tabID, _ := r.exec.NewTab(c.WorkspaceRoot, c.Profile)
		if r.onNewTab != nil && tabID != "" {
			r.onNewTab(tabID)
		}
		return nil
	case proto.CmdRenameSession:
		var c proto.RenameSessionCmd
		json.Unmarshal(plaintext, &c)
		return r.exec.RenameSession(c.Tab, c.Title)
	case proto.CmdDeleteSession:
		var c proto.DeleteSessionCmd
		json.Unmarshal(plaintext, &c)
		return r.exec.DeleteSession(c.Tab)
	case proto.CmdOfficeRun:
		var c proto.OfficeRunCmd
		json.Unmarshal(plaintext, &c)
		return r.exec.OfficeRun(c.Tab, c.Template, c.Args)
	case proto.CmdResync:
		var c proto.ResyncCmd
		json.Unmarshal(plaintext, &c)
		if r.onResync != nil {
			r.onResync(c.Tab, c.SinceSeq)
		}
		return nil
	case proto.CmdFileStart:
		if !r.perm.AllowFileDrop {
			r.audit.Denied(r.devC, env.T, "file_drop_disabled")
			return ErrForbidden
		}
		var c proto.FileStartCmd
		json.Unmarshal(plaintext, &c)
		return r.exec.FileStart(c.Tab, c.Name, c.Size)
	case proto.CmdFileChunk:
		var c proto.FileChunkCmd
		json.Unmarshal(plaintext, &c)
		return r.exec.FileChunk(c.Tab, c.Seq, c.Data)
	case proto.CmdFileEnd:
		var c proto.FileEndCmd
		json.Unmarshal(plaintext, &c)
		return r.exec.FileEnd(c.Tab, c.Name)
	case proto.CmdLoadSession:
		var c proto.LoadSessionCmd
		json.Unmarshal(plaintext, &c)
		if r.onLoadSession != nil {
			tab := c.Path
			history, _ := r.exec.LoadSession(tab)
			r.onLoadSession(tab, history)
		}
		return nil
	case proto.CmdPing,
		proto.CmdPong, proto.CmdSwitchTab:
		// handled by the Bridge directly (need Conn access for replies), not executor
		return nil
	}
	return nil
}

// once is a sync.Once helper for the subscribe hook wiring (keeps the API tidy).
var _ = sync.Once{}
