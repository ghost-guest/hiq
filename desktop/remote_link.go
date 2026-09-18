package main

// remote_link.go — the desktop side of the remote-host connection. One
// remoteHostLink wraps one spawned host process (WSL in P1; Docker/SSH/Server
// later) over its stdio, demultiplexing sessions by id: host events fan out to
// the owning remoteSession's tab sink, host-initiated permission/ask
// round-trips block until the user decides in the UI, and every tabSession
// call becomes one JSON-RPC request. The link is transport-agnostic — WP2's
// WSL manager hands it the process's stdin/stdout.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/acp"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/eventwire"
	"github.com/zzycxz/hiq/internal/remotehost"
)

// remoteProcess is the transport's handle on the host process (kill + wait).
type remoteProcess interface {
	Kill() error
	Wait() error
}

// remoteHostLink is one live host connection shared by every remote tab on the
// same (kind, target, user).
type remoteHostLink struct {
	conn   *acp.Conn
	cancel context.CancelFunc
	proc   remoteProcess

	mu       sync.Mutex
	sessions map[string]*remoteSession
	closed   bool
	closeFn  []func()
}

// newRemoteHostLink wires a link over the host process's pipes and starts the
// read loop. Handlers are registered before Serve so no frame races them.
func newRemoteHostLink(ctx context.Context, stdin io.Reader, stdout io.Writer, proc remoteProcess) *remoteHostLink {
	conn := acp.NewConn(stdin, stdout)
	l := &remoteHostLink{
		conn:     conn,
		proc:     proc,
		sessions: make(map[string]*remoteSession),
	}
	serveCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	conn.HandleNotify("event", func(_ context.Context, raw json.RawMessage) {
		var p remotehost.EventParams
		if json.Unmarshal(raw, &p) != nil {
			return
		}
		if s := l.session(p.SessionID); s != nil {
			s.consumeEvent(eventwire.FromWire(p.Event))
		}
	})
	conn.Handle("permission/request", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p remotehost.PermissionRequestParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		s := l.session(p.SessionID)
		if s == nil {
			return remotehost.PermissionRequestResult{}, nil
		}
		return s.awaitPermission(ctx, eventwire.FromWire(p.Event))
	})
	conn.Handle("ask/request", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p remotehost.AskRequestParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		s := l.session(p.SessionID)
		if s == nil {
			return remotehost.AskRequestResult{}, nil
		}
		return s.awaitAsk(ctx, eventwire.FromWire(p.Event))
	})
	go func() {
		_ = conn.Serve(serveCtx)
		l.shutdown()
	}()
	return l
}

func (l *remoteHostLink) session(id string) *remoteSession {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sessions[id]
}

func (l *remoteHostLink) register(s *remoteSession) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sessions[s.id] = s
}

func (l *remoteHostLink) unregister(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.sessions, id)
}

// onClose registers a callback fired once when the host process or connection
// ends (used by the manager to mark the link dead / schedule respawn).
func (l *remoteHostLink) onClose(fn func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		go fn()
		return
	}
	l.closeFn = append(l.closeFn, fn)
}

func (l *remoteHostLink) shutdown() {
	l.mu.Lock()
	l.closed = true
	fns := l.closeFn
	l.closeFn = nil
	l.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// close tears the connection and (best-effort) the host process down.
func (l *remoteHostLink) close() {
	l.cancel()
	if l.proc != nil {
		_ = l.proc.Kill()
	}
}

// call performs one JSON-RPC request against the host. errRemoteClosed wraps
// transport failures so callers can distinguish them from method errors.
func (l *remoteHostLink) call(ctx context.Context, method string, params any, result any) error {
	raw, err := l.conn.Request(ctx, method, params)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if result != nil {
		if err := json.Unmarshal(raw, result); err != nil {
			return fmt.Errorf("%s: decode reply: %w", method, err)
		}
	}
	return nil
}

// errRemoteUnsupported marks desktop-local features that have no host-side
// implementation in P1 (memory panel, MCP hot-add, dream, expert collab).
var errRemoteUnsupported = errors.New("not available for remote sessions yet")

// notifyErr surfaces a link failure to the tab's transcript so the user sees
// why a click did nothing (the tabSession setters return no error).
func (s *remoteSession) notifyErr(what string, err error) {
	if s.sink == nil || err == nil {
		return
	}
	s.sink.Emit(event.Event{
		Kind:  event.Notice,
		Level: event.LevelWarn,
		Text:  fmt.Sprintf("%s failed: %v", what, err),
	})
}

// callTimeout bounds individual control calls (file previews and histories can
// be bigger; they pass their own ctx where it matters).
const callTimeout = 30 * time.Second

func (s *remoteSession) call(method string, params any, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.link.call(ctx, method, params, result)
}
