package remotehost

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/acp"
	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/control"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/eventwire"
)

// permissionTimeout bounds how long the host waits for the desktop to answer a
// permission/ask round-trip before denying. Long enough for a user who stepped
// away; finite so a disconnected desktop can't leave the run loop hung forever.
const permissionTimeout = 30 * time.Minute

// Factory assembles one session's controller, mirroring the desktop's
// buildTabController on the host side (config load for the root, model fallback
// resolution, per-project session dir, presentation sidecar). The production
// wiring lives in internal/cli; tests supply a stub.
type Factory interface {
	NewController(ctx context.Context, p SessionNewParams, sink event.Sink) (*control.Controller, error)
}

// HelloInfo identifies the host build; ConfigureFunc and HasModelConfigFunc
// implement host/configure (see protocol.go). All three are optional — Serve
// answers host/hello with Version alone when they are nil.
type HelloInfo struct {
	Version string
	Goos    string
	Arch    string
	Home    string
	ConfigRoot string
}

type ConfigureFunc func(p ConfigureParams) (ConfigureResult, error)

type HasModelConfigFunc func() bool

// Serve runs a remote host on r/w (stdin/stdout in production) until the input
// ends or ctx is cancelled. stdout is the JSON-RPC channel; all diagnostics
// must go to stderr.
func Serve(ctx context.Context, r io.Reader, w io.Writer, factory Factory, info HelloInfo, configure ConfigureFunc, hasModel HasModelConfigFunc) error {
	conn := acp.NewConn(r, w)
	h := &host{
		conn:    conn,
		factory: factory,
		info:    info,
		sessions: make(map[string]*hostSession),
	}
	if configure != nil {
		h.configure = configure
	}
	if hasModel != nil {
		h.hasModelConfig = hasModel
	}
	h.register()
	defer h.closeAll()
	return conn.Serve(ctx)
}

// ListenServe runs a remote host as a TCP server (the Server connection kind):
// every accepted connection speaks the same NDJSON JSON-RPC protocol after a
// one-line token handshake. Sessions are shared across connections, so a
// desktop can reconnect without losing state. An empty token refuses the
// handshake (Server mode requires one).
//
// certDir non-empty upgrades the listener to TLS with a self-signed
// certificate persisted there (generated on first start, reused afterwards so
// the desktop's pinned fingerprint survives restarts).
func ListenServe(ctx context.Context, addr, token, certDir string, factory Factory, info HelloInfo, configure ConfigureFunc, hasModel HasModelConfigFunc) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("listen mode requires --token")
	}
	var ln net.Listener
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if certDir != "" {
		tlsCfg, cerr := selfSignedTLSConfig(certDir)
		if cerr != nil {
			ln.Close()
			return cerr
		}
		ln = tls.NewListener(ln, tlsCfg)
	}
	defer ln.Close()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	shared := &sharedHostState{
		factory:   factory,
		info:      info,
		configure: configure,
		hasModel:  hasModel,
		sessions:  make(map[string]*hostSession),
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go shared.serveConn(ctx, c, token)
	}
}

// AuthParams is the mandatory first line of a Server-mode connection.
type AuthParams struct {
	Token string `json:"token"`
}

// sharedHostState carries the factory and registration across connections.
type sharedHostState struct {
	factory   Factory
	info      HelloInfo
	configure ConfigureFunc
	hasModel  HasModelConfigFunc

	mu       sync.Mutex
	sessions map[string]*hostSession
}

// serveConn handshakes one connection then serves the protocol on it. The
// connection's handlers share the host-wide session registry.
func (s *sharedHostState) serveConn(ctx context.Context, c net.Conn, token string) {
	defer c.Close()
	br := bufio.NewReader(c)
	line, err := readAuthLine(br)
	if err != nil {
		return
	}
	var auth AuthParams
	if json.Unmarshal(line, &auth) != nil || subtle.ConstantTimeCompare([]byte(auth.Token), []byte(token)) != 1 {
		return
	}
	h := &host{
		conn:     acp.NewConn(br, c),
		factory:  s.factory,
		info:     s.info,
		configure: s.configure,
		hasModelConfig: s.hasModel,
		shared:   s,
	}
	h.register()
	_ = h.conn.Serve(ctx)
}

// readAuthLine reads the single handshake line (bounded like a protocol frame).
func readAuthLine(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	return bytes.TrimSpace(line), nil
}

type host struct {
	conn      *acp.Conn
	factory   Factory
	info      HelloInfo
	configure ConfigureFunc
	hasModelConfig HasModelConfigFunc
	// shared, when set (Server listen mode), makes this connection's session
	// registry process-wide so a reconnecting desktop finds its sessions.
	shared *sharedHostState

	mu       sync.Mutex
	sessions map[string]*hostSession
}

// sessionReg returns the session registry guarding it: the shared one in
// Server mode, the connection-local one in stdio mode.
func (h *host) sessionReg() (map[string]*hostSession, *sync.Mutex) {
	if h.shared != nil {
		return h.shared.sessions, &h.shared.mu
	}
	return h.sessions, &h.mu
}

// hostSession is one open session: its controller, its sink (which forwards
// events and round-trips approvals), and the root every fs/git call is
// confined to.
type hostSession struct {
	id   string
	ctrl *control.Controller
	sink *hostSink
	cwd  string
}

func (h *host) register() {
	c := h.conn
	c.Handle("host/hello", h.hello)
	c.Handle("host/configure", h.hostConfigure)

	c.Handle("session/new", h.sessionNew)
	c.Handle("session/submit", h.sessionSubmit)
	c.Handle("session/steer", h.sessionSteer)
	c.Handle("session/followUp", h.sessionFollowUp)
	c.Handle("session/queued", h.sessionQueued)
	c.Handle("session/cancel", h.sessionCancel)
	c.Handle("session/pause", h.sessionPause)
	c.Handle("session/resumeTurn", h.sessionResumeTurn)
	c.Handle("session/state", h.sessionState)
	c.Handle("session/runShell", h.sessionRunShell)
	c.Handle("session/approve", h.sessionApprove)
	c.Handle("session/answer", h.sessionAnswer)
	c.Handle("session/replayPendingPrompts", h.sessionReplayPendingPrompts)
	c.Handle("session/setMode", h.sessionSetMode)
	c.Handle("session/setToolApprovalMode", h.sessionSetToolApprovalMode)
	c.Handle("session/setGoal", h.sessionSetGoal)
	c.Handle("session/goalStatus", h.sessionGoalStatus)
	c.Handle("session/setRagScope", h.sessionSetRagScope)
	c.Handle("session/compact", h.sessionCompact)
	c.Handle("session/newSession", h.sessionNewSession)
	c.Handle("session/clearSession", h.sessionClearSession)
	c.Handle("session/setSessionPath", h.sessionSetSessionPath)
	c.Handle("session/snapshot", h.sessionSnapshot)
	c.Handle("session/history", h.sessionHistory)
	c.Handle("session/present", h.sessionPresent)
	c.Handle("session/searchText", h.sessionSearchText)
	c.Handle("session/checkpoints", h.sessionCheckpoints)
	c.Handle("session/checkpointDiff", h.sessionCheckpointDiff)
	c.Handle("session/rewindPreview", h.sessionRewindPreview)
	c.Handle("session/checkpointHasBoundary", h.sessionCheckpointHasBoundary)
	c.Handle("session/rewind", h.sessionRewind)
	c.Handle("session/fork", h.sessionFork)
	c.Handle("session/branches", h.sessionBranches)
	c.Handle("session/switchBranch", h.sessionSwitchBranch)
	c.Handle("session/summarize", h.sessionSummarize)
	c.Handle("session/setModel", h.sessionSetModel)
	c.Handle("session/close", h.sessionClose)
	c.Handle("session/list", h.sessionList)

	c.Handle("fs/list", h.fsList)
	c.Handle("fs/read", h.fsRead)
	c.Handle("fs/search", h.fsSearch)
	c.Handle("git/status", h.gitStatus)
}

func invalidParams(method, msg string) error {
	return &acp.RPCError{Code: acp.ErrInvalidParams, Message: method + ": " + msg}
}

func internalErr(method string, err error) error {
	return &acp.RPCError{Code: acp.ErrInternal, Message: method + ": " + err.Error()}
}

func (h *host) hello(context.Context, json.RawMessage) (any, error) {
	res := HelloResult{
		Version: h.info.Version,
		Goos:    h.info.Goos,
		Arch:    h.info.Arch,
		Home:    h.info.Home,
		ConfigRoot: h.info.ConfigRoot,
	}
	if h.hasModelConfig != nil {
		res.HasModelConfig = h.hasModelConfig()
	}
	return res, nil
}

func (h *host) hostConfigure(_ context.Context, raw json.RawMessage) (any, error) {
	if h.configure == nil {
		return nil, &acp.RPCError{Code: acp.ErrMethodNotFound, Message: "host/configure not wired"}
	}
	var p ConfigureParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, invalidParams("host/configure", err.Error())
		}
	}
	res, err := h.configure(p)
	if err != nil {
		return nil, internalErr("host/configure", err)
	}
	return res, nil
}

// sessionNew opens a session: build the sink first (events need the id), let
// the Factory assemble the controller against it, switch on interactive
// approval so gates surface as ApprovalRequest events the sink round-trips to
// the desktop, apply the tab's mode knobs, and pin/derive the transcript path.
func (h *host) sessionNew(ctx context.Context, raw json.RawMessage) (any, error) {
	var p SessionNewParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, invalidParams("session/new", err.Error())
		}
	}
	cwd := filepath.Clean(strings.TrimSpace(p.Cwd))
	if cwd == "" || !filepath.IsAbs(cwd) {
		return nil, invalidParams("session/new", "cwd must be an absolute path")
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return nil, invalidParams("session/new", "cwd is not a directory: "+cwd)
	}
	id := strings.TrimSpace(p.SessionID)
	if id == "" {
		var err error
		id, err = newSessionID()
		if err != nil {
			return nil, internalErr("session/new", err)
		}
	}
	sessions, mu := h.sessionReg()
	mu.Lock()
	_, dup := sessions[id]
	mu.Unlock()
	if dup {
		return nil, invalidParams("session/new", "session already open: "+id)
	}

	sink := newHostSink(h.conn, id)
	p.Cwd = cwd
	ctrl, err := h.factory.NewController(ctx, p, sink)
	if err != nil {
		return nil, internalErr("session/new", err)
	}
	ctrl.EnableInteractiveApproval()
	sink.bind(ctrl.Approve, ctrl.AnswerQuestion)
	applyMode(ctrl, p.Mode)
	if m := strings.TrimSpace(p.ToolApprovalMode); m != "" {
		ctrl.SetToolApprovalMode(m)
	}
	applyRagScope(ctrl, p.RagScope)
	ctrl.SetGoal(p.Goal)

	sess := &hostSession{id: id, ctrl: ctrl, sink: sink, cwd: cwd}
	// Pin the caller's transcript when it exists here; otherwise derive a fresh
	// session file (the desktop persists the returned path for restore).
	if path := strings.TrimSpace(p.SessionPath); path != "" && fileExists(path) {
		if loaded, err := agent.LoadSession(path); err == nil {
			ctrl.Resume(loaded, path)
		} else {
			ctrl.SetSessionPath(path)
		}
	} else if dir := ctrl.SessionDir(); dir != "" {
		path := agent.NewSessionPath(dir, ctrl.Label())
		ctrl.SetSessionPath(path)
	}

	mu.Lock()
	sessions[id] = sess
	mu.Unlock()

	res := SessionNewResult{SessionID: id, SessionPath: ctrl.SessionPath(), Label: ctrl.Label()}
	return res, nil
}

// applyMode mirrors the desktop's applyTabModeToController mapping.
func applyMode(ctrl *control.Controller, mode string) {
	plan := false
	yolo := false
	switch strings.TrimSpace(mode) {
	case "plan":
		plan = true
	case "yolo", "full", "full-access", "bypass":
		yolo = true
	case "plan-yolo":
		plan, yolo = true, true
	}
	if plan || yolo {
		ctrl.SetMode(plan, yolo)
	}
}

func applyRagScope(ctrl *control.Controller, scope string) {
	if s := strings.TrimSpace(scope); s != "" {
		ctrl.SetRAGScope(s)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (h *host) session(id string) (*hostSession, error) {
	sessions, mu := h.sessionReg()
	mu.Lock()
	defer mu.Unlock()
	s := sessions[id]
	if s == nil {
		return nil, invalidParams("", "unknown session "+id)
	}
	return s, nil
}

func decodeParams[P any](method string, raw json.RawMessage, p *P) error {
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, p); err != nil {
			return invalidParams(method, err.Error())
		}
	}
	return nil
}

func (h *host) sessionSubmit(_ context.Context, raw json.RawMessage) (any, error) {
	var p SubmitParams
	if err := decodeParams("session/submit", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	if display := strings.TrimSpace(p.Display); display != "" {
		s.ctrl.SubmitDisplay(display, p.Input)
	} else {
		s.ctrl.Submit(p.Input)
	}
	return struct{}{}, nil
}

func (h *host) sessionSteer(_ context.Context, raw json.RawMessage) (any, error) {
	var p SteerParams
	if err := decodeParams("session/steer", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	s.ctrl.Steer(p.Text)
	return struct{}{}, nil
}

func (h *host) sessionFollowUp(_ context.Context, raw json.RawMessage) (any, error) {
	var p FollowUpParams
	if err := decodeParams("session/followUp", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	s.ctrl.FollowUp(p.Input)
	return struct{}{}, nil
}

func (h *host) sessionQueued(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("session/queued", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	steer, followUp := s.ctrl.QueuedMessages()
	return QueuedResult{Steer: steer, FollowUp: followUp}, nil
}

func (h *host) sessionCancel(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("session/cancel", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	s.ctrl.Cancel()
	return struct{}{}, nil
}

func (h *host) sessionPause(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("session/pause", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	s.ctrl.Pause()
	return struct{}{}, nil
}

func (h *host) sessionResumeTurn(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("session/resumeTurn", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	s.ctrl.ResumeTurn()
	return struct{}{}, nil
}

func (h *host) sessionState(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("session/state", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	used, window := s.ctrl.ContextSnapshot()
	res := SessionStateResult{
		Running:          s.ctrl.Running(),
		Paused:           s.ctrl.Paused(),
		Label:            s.ctrl.Label(),
		WorkspaceRoot:    s.ctrl.WorkspaceRoot(),
		SessionPath:      s.ctrl.SessionPath(),
		SessionDir:       s.ctrl.SessionDir(),
		ToolApprovalMode: s.ctrl.ToolApprovalMode(),
		PlanMode:         s.ctrl.PlanMode(),
		Goal:             s.ctrl.Goal(),
		GoalStatus:       s.ctrl.GoalStatus(),
		ContextUsed:      used,
		ContextWindow:    window,
		CompactRatio:     s.ctrl.CompactRatio(),
	}
	return res, nil
}

func (h *host) sessionRunShell(_ context.Context, raw json.RawMessage) (any, error) {
	var p RunShellParams
	if err := decodeParams("session/runShell", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	s.ctrl.RunShell(p.Command)
	return struct{}{}, nil
}

func (h *host) sessionApprove(_ context.Context, raw json.RawMessage) (any, error) {
	var p ApproveParams
	if err := decodeParams("session/approve", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	s.ctrl.Approve(p.ID, p.Allow, p.Session, p.Persist)
	return struct{}{}, nil
}

func (h *host) sessionAnswer(_ context.Context, raw json.RawMessage) (any, error) {
	var p AnswerParams
	if err := decodeParams("session/answer", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	answers := make([]event.AskAnswer, len(p.Answers))
	for i, a := range p.Answers {
		answers[i] = event.AskAnswer{QuestionID: a.QuestionID, Selected: a.Selected}
	}
	s.ctrl.AnswerQuestion(p.ID, answers)
	return struct{}{}, nil
}

func (h *host) sessionReplayPendingPrompts(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("session/replayPendingPrompts", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	s.ctrl.ReplayPendingPrompts()
	return struct{}{}, nil
}

func (h *host) sessionSetMode(_ context.Context, raw json.RawMessage) (any, error) {
	var p SetModeParams
	if err := decodeParams("session/setMode", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	applyMode(s.ctrl, p.Mode)
	return struct{}{}, nil
}

func (h *host) sessionSetToolApprovalMode(_ context.Context, raw json.RawMessage) (any, error) {
	var p SetToolApprovalModeParams
	if err := decodeParams("session/setToolApprovalMode", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	s.ctrl.SetToolApprovalMode(p.Mode)
	return struct{}{}, nil
}

func (h *host) sessionSetGoal(_ context.Context, raw json.RawMessage) (any, error) {
	var p SetGoalParams
	if err := decodeParams("session/setGoal", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	s.ctrl.SetGoal(p.Goal)
	return struct{}{}, nil
}

func (h *host) sessionGoalStatus(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("session/goalStatus", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	return GoalStatusResult{Goal: s.ctrl.Goal(), Status: s.ctrl.GoalStatus()}, nil
}

func (h *host) sessionSetRagScope(_ context.Context, raw json.RawMessage) (any, error) {
	var p SetRagScopeParams
	if err := decodeParams("session/setRagScope", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	s.ctrl.SetRAGScope(p.Scope)
	return struct{}{}, nil
}

func (h *host) sessionCompact(ctx context.Context, raw json.RawMessage) (any, error) {
	var p CompactParams
	if err := decodeParams("session/compact", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := s.ctrl.Compact(ctx, p.Instructions); err != nil {
		return nil, internalErr("session/compact", err)
	}
	return struct{}{}, nil
}

func (h *host) sessionNewSession(_ context.Context, raw json.RawMessage) (any, error) {
	var p NewSessionParams
	if err := decodeParams("session/newSession", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := s.ctrl.NewSession(); err != nil {
		return nil, internalErr("session/newSession", err)
	}
	if dir := s.ctrl.SessionDir(); dir != "" && s.ctrl.SessionPath() == "" {
		s.ctrl.SetSessionPath(agent.NewSessionPath(dir, s.ctrl.Label()))
	}
	return NewSessionResult{SessionPath: s.ctrl.SessionPath()}, nil
}

func (h *host) sessionClearSession(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("session/clearSession", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := s.ctrl.ClearSession(); err != nil {
		return nil, internalErr("session/clearSession", err)
	}
	return struct{}{}, nil
}

func (h *host) sessionSetSessionPath(_ context.Context, raw json.RawMessage) (any, error) {
	var p SetSessionPathParams
	if err := decodeParams("session/setSessionPath", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	if path := strings.TrimSpace(p.SessionPath); path != "" && fileExists(path) {
		if loaded, err := agent.LoadSession(path); err == nil {
			s.ctrl.Resume(loaded, path)
			return struct{}{}, nil
		}
	}
	s.ctrl.SetSessionPath(strings.TrimSpace(p.SessionPath))
	return struct{}{}, nil
}

func (h *host) sessionSnapshot(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("session/snapshot", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := s.ctrl.Snapshot(); err != nil {
		return nil, internalErr("session/snapshot", err)
	}
	return struct{}{}, nil
}

// rawJSON marshals v into a RawMessage result field; nil on marshal error
// (should not happen — the payloads are plain data).
func rawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

func (h *host) sessionHistory(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("session/history", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	return HistoryResult{Messages: rawJSON(s.ctrl.History())}, nil
}

func (h *host) sessionPresent(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("session/present", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	records, version, ok := s.ctrl.PresentRecords()
	return PresentResult{Records: rawJSON(records), RewriteVersion: version, OK: ok}, nil
}

func (h *host) sessionSearchText(_ context.Context, raw json.RawMessage) (any, error) {
	var p FsSearchParams
	if err := decodeParams("session/searchText", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	return FsSearchResult{Results: rawJSON(s.ctrl.SearchSessionText(p.Query))}, nil
}

func (h *host) sessionCheckpoints(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("session/checkpoints", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	return CheckpointsResult{Checkpoints: rawJSON(s.ctrl.Checkpoints())}, nil
}

func (h *host) sessionCheckpointDiff(_ context.Context, raw json.RawMessage) (any, error) {
	var p CheckpointDiffParams
	if err := decodeParams("session/checkpointDiff", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	return CheckpointDiffResult{Changes: rawJSON(s.ctrl.CheckpointDiff(p.Turn))}, nil
}

func (h *host) sessionRewindPreview(_ context.Context, raw json.RawMessage) (any, error) {
	var p CheckpointDiffParams
	if err := decodeParams("session/rewindPreview", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	return RewindPreviewResult{Classes: rawJSON(s.ctrl.RewindPreview(p.Turn))}, nil
}

func (h *host) sessionCheckpointHasBoundary(_ context.Context, raw json.RawMessage) (any, error) {
	var p CheckpointDiffParams
	if err := decodeParams("session/checkpointHasBoundary", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	return CheckpointHasBoundaryResult{Has: s.ctrl.CheckpointHasBoundary(p.Turn)}, nil
}

func (h *host) sessionRewind(_ context.Context, raw json.RawMessage) (any, error) {
	var p RewindParams
	if err := decodeParams("session/rewind", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	var scope control.RewindScope
	switch strings.ToLower(strings.TrimSpace(p.Scope)) {
	case "", "both":
		scope = control.RewindBoth
	case "code":
		scope = control.RewindCode
	case "conversation":
		scope = control.RewindConversation
	default:
		return nil, invalidParams("session/rewind", "unknown scope "+p.Scope)
	}
	if err := s.ctrl.Rewind(p.Turn, scope); err != nil {
		return nil, internalErr("session/rewind", err)
	}
	return struct{}{}, nil
}

func (h *host) sessionFork(_ context.Context, raw json.RawMessage) (any, error) {
	var p ForkParams
	if err := decodeParams("session/fork", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	var path string
	var ferr error
	if name := strings.TrimSpace(p.Name); name != "" {
		path, ferr = s.ctrl.ForkSession(p.Turn, name)
	} else {
		path, ferr = s.ctrl.Fork(p.Turn)
	}
	if ferr != nil {
		return nil, internalErr("session/fork", ferr)
	}
	return ForkResult{SessionPath: path}, nil
}

func (h *host) sessionBranches(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("session/branches", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	branches, berr := s.ctrl.Branches()
	if berr != nil {
		return nil, internalErr("session/branches", berr)
	}
	return BranchesResult{Branches: rawJSON(branches)}, nil
}

func (h *host) sessionSwitchBranch(_ context.Context, raw json.RawMessage) (any, error) {
	var p SwitchBranchParams
	if err := decodeParams("session/switchBranch", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	info, serr := s.ctrl.SwitchBranch(p.Ref)
	if serr != nil {
		return nil, internalErr("session/switchBranch", serr)
	}
	return ForkResult{SessionPath: info.Path}, nil
}

func (h *host) sessionSummarize(ctx context.Context, raw json.RawMessage) (any, error) {
	var p SummarizeParams
	if err := decodeParams("session/summarize", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	var serr error
	if p.UpTo {
		serr = s.ctrl.SummarizeUpTo(ctx, p.Turn)
	} else {
		serr = s.ctrl.SummarizeFrom(ctx, p.Turn)
	}
	if serr != nil {
		return nil, internalErr("session/summarize", serr)
	}
	return struct{}{}, nil
}

func (h *host) sessionSetModel(ctx context.Context, raw json.RawMessage) (any, error) {
	var p SetModelParams
	if err := decodeParams("session/setModel", raw, &p); err != nil {
		return nil, err
	}
	s, err := h.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	if s.ctrl.Running() {
		return nil, &acp.RPCError{Code: acp.ErrInvalidRequest, Message: "session/setModel: turn in flight"}
	}
	old := s.ctrl
	_ = old.Snapshot()
	hist := old.History()
	path := old.SessionPath()

	np := SessionNewParams{
		Cwd:          s.cwd,
		Model:        p.Model,
		Effort:       p.Effort,
		SessionPath:  path,
	}
	newCtrl, err := h.factory.NewController(ctx, np, s.sink)
	if err != nil {
		// Keep the old controller alive — the model switch just fails.
		return nil, internalErr("session/setModel", err)
	}
	newCtrl.EnableInteractiveApproval()
	s.sink.bind(newCtrl.Approve, newCtrl.AnswerQuestion)
	if path != "" && len(hist) > 0 {
		newCtrl.Resume(&agent.Session{Messages: hist}, agent.ContinueSessionPath(path, newCtrl.SessionDir(), newCtrl.Label()))
	} else if path != "" {
		newCtrl.SetSessionPath(path)
	}
	old.Close()
	s.ctrl = newCtrl
	return SetModelResult{Label: newCtrl.Label()}, nil
}

func (h *host) sessionClose(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionRef
	if err := decodeParams("session/close", raw, &p); err != nil {
		return nil, err
	}
	sessions, mu := h.sessionReg()
	mu.Lock()
	s := sessions[p.SessionID]
	delete(sessions, p.SessionID)
	mu.Unlock()
	if s != nil {
		s.ctrl.Close()
	}
	return struct{}{}, nil
}

// sessionList enumerates the host-side transcripts stored under a root, newest
// first. The desktop merges this with its own tab/titles state when the
// connection is up.
func (h *host) sessionList(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionListParams
	if err := decodeParams("session/list", raw, &p); err != nil {
		return nil, err
	}
	cwd := filepath.Clean(strings.TrimSpace(p.Cwd))
	if cwd == "" || !filepath.IsAbs(cwd) {
		return nil, invalidParams("session/list", "cwd must be an absolute path")
	}
	dir := sessionDirFor(cwd)
	out := SessionListResult{Sessions: []SessionEntry{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, nil // no sessions yet
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		entry := SessionEntry{
			Path:      filepath.Join(dir, e.Name()),
			ModTimeMs: info.ModTime().UnixMilli(),
		}
		if meta, ok, _ := agent.LoadBranchMeta(entry.Path); ok {
			entry.Turns = meta.CachedTurns
			entry.TopicID = meta.TopicID
			entry.TopicTitle = meta.TopicTitle
			entry.WorkspaceRoot = meta.WorkspaceRoot
			entry.Scope = meta.Scope
			entry.Preview = meta.CachedPreview
		}
		out.Sessions = append(out.Sessions, entry)
	}
	// Newest first.
	for i := 1; i < len(out.Sessions); i++ {
		for j := i; j > 0 && out.Sessions[j].ModTimeMs > out.Sessions[j-1].ModTimeMs; j-- {
			out.Sessions[j], out.Sessions[j-1] = out.Sessions[j-1], out.Sessions[j]
		}
	}
	return out, nil
}

// sessionDirFor is the per-project session dir on this side; overridable in
// tests.
var sessionDirFor = defaultSessionDirFor

func defaultSessionDirFor(root string) string {
	return projectSessionDir(root)
}

func (h *host) closeAll() {
	if h.shared != nil {
		return // Server mode: sessions outlive single connections
	}
	h.mu.Lock()
	sessions := h.sessions
	h.sessions = make(map[string]*hostSession)
	h.mu.Unlock()
	for _, s := range sessions {
		s.ctrl.Cancel()
		s.ctrl.Close()
	}
}

// newSessionID returns a random id for a caller-opened session.
func newSessionID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("s-%x", b[:]), nil
}

// hostSink forwards one session's events to the desktop and round-trips
// approval/ask decisions back into the blocked run loop.
type hostSink struct {
	conn     *acp.Conn
	sessionID string
	approve  func(id string, allow, session, persist bool)
	answer   func(id string, answers []event.AskAnswer)
}

func newHostSink(conn *acp.Conn, sessionID string) *hostSink {
	return &hostSink{conn: conn, sessionID: sessionID}
}

// bind installs the controller's decision callbacks once it exists.
func (s *hostSink) bind(approve func(id string, allow, session, persist bool), answer func(id string, answers []event.AskAnswer)) {
	s.approve = approve
	s.answer = answer
}

// Emit implements event.Sink. Events are serialized in order; approval/ask
// round-trips continue off-thread so Emit never blocks the run loop.
func (s *hostSink) Emit(e event.Event) {
	w := eventwire.ToWire(e)
	_ = s.conn.Notify("event", EventParams{SessionID: s.sessionID, Event: w})
	switch e.Kind {
	case event.ApprovalRequest:
		go s.roundTripPermission(e)
	case event.AskRequest:
		go s.roundTripAsk(e)
	}
}

func (s *hostSink) roundTripPermission(e event.Event) {
	if s.approve == nil {
		return
	}
	allow, session, persist := false, false, false
	ctx, cancel := context.WithTimeout(context.Background(), permissionTimeout)
	defer cancel()
	if raw, err := s.conn.Request(ctx, "permission/request", PermissionRequestParams{
		SessionID: s.sessionID,
		Event:     eventwire.ToWire(e),
	}); err == nil {
		var res PermissionRequestResult
		if json.Unmarshal(raw, &res) == nil {
			allow, session, persist = res.Allow, res.Session, res.Persist
		}
	} else if !errors.Is(err, context.DeadlineExceeded) {
		// Connection-level failure: wait out the timeout so a brief reconnect
		// window still lets the desktop answer; deny after that.
		select {
		case <-ctx.Done():
		case <-time.After(permissionTimeout):
		}
	}
	s.approve(e.Approval.ID, allow, session, persist)
}

func (s *hostSink) roundTripAsk(e event.Event) {
	if s.answer == nil {
		return
	}
	var answers []AskAnswer
	ctx, cancel := context.WithTimeout(context.Background(), permissionTimeout)
	defer cancel()
	if raw, err := s.conn.Request(ctx, "ask/request", AskRequestParams{
		SessionID: s.sessionID,
		Event:     eventwire.ToWire(e),
	}); err == nil {
		var res AskRequestResult
		if json.Unmarshal(raw, &res) == nil {
			answers = res.Answers
		}
	}
	wire := make([]event.AskAnswer, len(answers))
	for i, a := range answers {
		wire[i] = event.AskAnswer{QuestionID: a.QuestionID, Selected: a.Selected}
	}
	s.answer(e.Ask.ID, wire)
}


// selfSignedTLSConfig loads (or generates on first start) a self-signed
// server certificate from dir and returns the TLS config for the listener.
func selfSignedTLSConfig(dir string) (*tls.Config, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, "remote-host-tls.crt")
	keyPath := filepath.Join(dir, "remote-host-tls.key")
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		key, kerr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if kerr != nil {
			return nil, kerr
		}
		serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		tmpl := x509.Certificate{
			SerialNumber: serial,
			Subject:      pkix.Name{CommonName: "hiq-remote-host"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().AddDate(10, 0, 0),
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, cerr := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
		if cerr != nil {
			return nil, cerr
		}
		keyDER, _ := x509.MarshalECPrivateKey(key)
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		if werr := os.WriteFile(certPath, certPEM, 0o600); werr != nil {
			return nil, werr
		}
		if werr := os.WriteFile(keyPath, keyPEM, 0o600); werr != nil {
			return nil, werr
		}
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}
