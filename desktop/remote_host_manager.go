package main

// remote_host_manager.go — one live host link per (kind, target, user),
// shared by every remote tab on that connection. The transport (WSL in P1)
// spawns the host process and provisions its binary; this file owns link
// lifetime, the desktop→host model-config push, and reconnect: when the host
// dies, registered sessions are marked offline and one respawn attempt is
// scheduled, reattaching every session by its pinned transcript path.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/remotehost"
)

// remoteTransport dials one host connection for a remote ref. Implementations
// own process spawn and binary provisioning (WSL: wsl.exe + UNC copy; later:
// docker exec / ssh / http).
type remoteTransport interface {
	// Dial spawns (or attaches to) the host and returns its stdio pipes.
	Dial(ctx context.Context, ref RemoteRef) (stdin io.Reader, stdout io.Writer, proc remoteProcess, err error)
}

// managedLink is one connection plus the sessions riding it.
type managedLink struct {
	ref  RemoteRef
	link *remoteHostLink

	mu       sync.Mutex
	sessions []*remoteSession
	dead     bool
}

func (m *managedLink) registerSession(s *remoteSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions = append(m.sessions, s)
}

func (m *managedLink) removeSession(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.sessions[:0]
	for _, s := range m.sessions {
		if s.id != id {
			out = append(out, s)
		}
	}
	m.sessions = out
}

// remoteHostManager owns the live links. Zero value is ready to use; the
// transport is resolved per connection kind (wsl, docker in P1).
type remoteHostManager struct {
	app *App

	mu           sync.Mutex
	links        map[string]*managedLink
	sshCreds     map[string]*sshCredentials
	serverTokens map[string]string
}

// transportFor resolves the dialer for a connection kind. Kinds without a
// transport yet (ssh, server) fail with errRemoteUnsupported.
func (m *remoteHostManager) transportFor(kind string) remoteTransport {
	return m.transportForRef(RemoteRef{Kind: kind})
}

// transportForRef resolves the dialer for a connection. SSH needs the
// credential registry (falling back to the secret store) because RemoteRef
// deliberately persists no secrets.
func (m *remoteHostManager) transportForRef(ref RemoteRef) remoteTransport {
	switch strings.ToLower(strings.TrimSpace(ref.Kind)) {
	case "wsl":
		return newWSLTransport()
	case "docker":
		return &dockerTransport{}
	case "ssh":
		return &sshTransport{creds: m.loadSSHCredentials(ref, ref.KeyPath)}
	case "server":
		return &serverTransport{token: m.loadServerToken(ref.Target)}
	default:
		return nil
	}
}

func remoteRefKey(ref RemoteRef) string {
	return strings.ToLower(ref.Kind) + "|" + ref.Target + "|" + ref.User
}

// ensureLink returns a live link for the ref, dialing and handshaking a new
// host process when needed. Sessions registered on a dead link are reattached
// by the caller paths (buildRemoteTabController calls reattach itself).
func (m *remoteHostManager) ensureLink(ctx context.Context, ref RemoteRef) (*remoteHostLink, error) {
	key := remoteRefKey(ref)
	m.mu.Lock()
	if ml := m.links[key]; ml != nil {
		ml.mu.Lock()
		dead := ml.dead
		ml.mu.Unlock()
		if !dead {
			m.mu.Unlock()
			return ml.link, nil
		}
	}
	m.mu.Unlock()
	transport := m.transportForRef(ref)
	if transport == nil {
		return nil, fmt.Errorf("remote kind %q is not supported yet", ref.Kind)
	}
	stdin, stdout, proc, err := transport.Dial(ctx, ref)
	if err != nil {
		return nil, err
	}
	link := newRemoteHostLink(ctx, stdin, stdout, proc)
	ml := &managedLink{ref: ref, link: link}
	link.onClose(func() {
		ml.mu.Lock()
		ml.dead = true
		sessions := append([]*remoteSession(nil), ml.sessions...)
		ml.mu.Unlock()
		m.mu.Lock()
		if m.links[key] == ml {
			delete(m.links, key)
		}
		m.mu.Unlock()
		for _, s := range sessions {
			s.markOffline()
		}
		m.app.emitRemoteStatus(ref, "offline")
		m.scheduleRespawn(ref, sessions)
	})
	m.mu.Lock()
	m.links[key] = ml
	m.mu.Unlock()

	// Handshake + first-run model push.
	var hello remotehost.HelloResult
	if err := link.call(ctx, "host/hello", map[string]any{}, &hello); err != nil {
		link.close()
		m.mu.Lock()
		delete(m.links, key)
		m.mu.Unlock()
		return nil, err
	}
	if !hello.HasModelConfig {
		m.pushModelConfig(ctx, link)
	}
	return link, nil
}

// pushModelConfig mirrors the desktop's active model configuration to the
// host once, only when the host reports nothing usable (host/configure itself
// refuses to overwrite an existing setup).
func (m *remoteHostManager) pushModelConfig(ctx context.Context, link *remoteHostLink) {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	model := strings.TrimSpace(cfg.DefaultModel)
	entry, ok := cfg.ResolveModel(model)
	if !ok {
		return
	}
	snap := remotehost.ProviderSnapshot{
		Name:      entry.Name,
		Kind:      entry.Kind,
		BaseURL:   entry.BaseURL,
		APIKeyEnv: entry.APIKeyEnv,
		APIKey:    entry.APIKey(),
		Vision:    entry.Vision,
	}
	if entry.ContextWindow > 0 {
		snap.ContextWindow = entry.ContextWindow
	}
	if len(entry.Models) > 0 {
		snap.Models = entry.Models
	} else if entry.Model != "" {
		snap.Models = []string{entry.Model}
	}
	_ = link.call(ctx, "host/configure", remotehost.ConfigureParams{
		DefaultModel: model,
		Providers:    []remotehost.ProviderSnapshot{snap},
	}, nil)
}

// scheduleRespawn tries one delayed reconnect so a briefly-killed host
// (distro restart, wsl.exe hiccup) recovers without user action.
func (m *remoteHostManager) scheduleRespawn(ref RemoteRef, sessions []*remoteSession) {
	if len(sessions) == 0 {
		return
	}
	go func() {
		time.Sleep(4 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		link, err := m.ensureLink(ctx, ref)
		if err != nil {
			m.app.emitRemoteStatus(ref, "offline")
			return
		}
		for _, s := range sessions {
			if err := s.reattach(ctx, link); err == nil {
				s.markOnline()
			}
		}
		m.app.emitRemoteStatus(ref, "connected")
	}()
}

// closeAll drops every link (app shutdown).
func (m *remoteHostManager) closeAll() {
	m.mu.Lock()
	links := m.links
	m.links = make(map[string]*managedLink)
	m.mu.Unlock()
	for _, ml := range links {
		ml.link.close()
	}
}

// markOffline/markOnline flag connection state for the frontend badge; the
// actual state machine (TabMeta.RemoteState) reads these.
func (s *remoteSession) markOffline() {
	if s.sink != nil {
		s.notifyOffline()
	}
}

func (s *remoteSession) markOnline() {}

// emitRemoteStatus tells the webview a connection changed state.
func (a *App) emitRemoteStatus(ref RemoteRef, state string) {
	a.emitRemoteStatusWails(remoteStatusPayload{Kind: ref.Kind, Target: ref.Target, State: state})
}

type remoteStatusPayload struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	State  string `json:"state"`
}

// buildRemoteTabController is buildTabController's remote branch: acquire (or
// reattach over) the host link, open the session with the tab's persisted
// knobs and pinned transcript, and swap the tab's controller to the proxy.
func (a *App) buildRemoteTabController(tab *WorkspaceTab, root string, wailsCtx context.Context) {
	if a.remoteManager == nil {
		a.remoteManager = &remoteHostManager{app: a}
	}
	ctx := a.bootContext()

	fail := func(err string) {
		a.mu.Lock()
		tab.StartupErr = err
		tab.Ready = true
		a.mu.Unlock()
		a.emitReady(wailsCtx, tab.ID)
	}

	key := remoteRefKey(*tab.Remote)
	a.mu.RLock()
	var existing *remoteSession
	if tab.Ctrl != nil {
		if rs, ok := tab.Ctrl.(*remoteSession); ok {
			existing = rs
		}
	}
	a.mu.RUnlock()

	var rs *remoteSession
	if existing != nil {
		a.mu.RLock()
		ml := a.remoteManager.links[key]
		var link *remoteHostLink
		if ml != nil {
			ml.mu.Lock()
			dead := ml.dead
			ml.mu.Unlock()
			if !dead {
				link = ml.link
			}
		}
		a.mu.RUnlock()
		if link != nil {
			// Rebuild over a live link (model switch): open a fresh session
			// carrying the pinned transcript, then swap.
			rs = existing
		}
	}

	if rs == nil {
		link, err := a.remoteManager.ensureLink(ctx, *tab.Remote)
		if err != nil {
			fail("remote host: " + err.Error())
			return
		}
		effort := ""
		if tab.effort != nil {
			effort = *tab.effort
		}
		p := remotehost.SessionNewParams{
			SessionID:        tab.ID,
			Cwd:              root,
			Model:            strings.TrimSpace(tab.model),
			Effort:           effort,
			Profile:          strings.TrimSpace(tab.profile),
			Mode:             tab.mode,
			ToolApprovalMode: normalizeToolApprovalMode(tab.toolApprovalMode),
			RagScope:         tab.ragScope,
			Goal:             tab.goal,
			SessionPath:      strings.TrimSpace(tab.SessionPath),
		}
		newRS, err := newRemoteSession(ctx, link, *tab.Remote, p)
		if err != nil {
			fail("remote session: " + err.Error())
			return
		}
		rs = newRS
	}

	rs.bindSink(tab.sink)
	a.mu.Lock()
	if tab.Ctrl != nil && tab.Ctrl != tabSession(rs) {
		// A concurrent build won the race — abandon this proxy.
		a.mu.Unlock()
		rs.Close()
		return
	}
	tab.Ctrl = rs
	tab.Label = rs.Label()
	tab.Ready = true
	tab.StartupErr = ""
	if path := rs.SessionPath(); path != "" {
		tab.SessionPath = path
	}
	a.saveTabsLocked()
	a.mu.Unlock()
	a.emitRemoteStatus(*tab.Remote, "connected")
	a.emitReady(wailsCtx, tab.ID)
}

// emitRemoteStatusWails pushes a remote:status event to the webview.
func (a *App) emitRemoteStatusWails(p remoteStatusPayload) {
	a.mu.RLock()
	ctx := a.ctx
	a.mu.RUnlock()
	if ctx != nil {
		runtime.EventsEmit(ctx, "remote:status", p)
	}
}

// notifyOffline surfaces a dropped host connection in the tab's transcript.
func (s *remoteSession) notifyOffline() {
	s.sink.Emit(event.Event{
		Kind:  event.Notice,
		Level: event.LevelWarn,
		Text:  "远程主机连接已断开，正在尝试重新连接…",
	})
}
