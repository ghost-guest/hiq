package persistentshell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/fairpeer/internal/sandbox"
	"github.com/zzycxz/fairpeer/internal/tool"
)

var (
	// ErrUnavailable reports that the manager can no longer start or reuse a
	// persistent shell (sealed after the last controller owner released).
	ErrUnavailable = errors.New("persistent shell unavailable")
	errEmptyArgv   = errors.New("empty argv")
)

const (
	startupTimeout = 10 * time.Second
	readChunk      = 4096
)

// Request is one foreground command to run in the session-scoped PTY.
type Request struct {
	Argv    []string
	Dir     string
	Env     []string
	Command string
	Timeout time.Duration
	Shell   sandbox.Shell
	// Progress receives live output chunks. Callers pass the shared capped
	// writer; this package does not re-implement the live-output bound.
	Progress io.Writer
}

// Result is the structured outcome of one persistent-shell command.
type Result struct {
	Output    string
	ExitCode  int
	TimedOut  bool
	Canceled  bool
	ShellDied bool
	Started   bool
	// Reset reports that the shell was retired, so the next command starts from
	// the workspace with a fresh directory and environment. The model is told,
	// because it otherwise keeps reasoning about a cwd that no longer exists.
	Reset        bool
	State        string
	FailurePhase string
	Err          error
}

// Manager owns one logical session's persistent PTY. Controllers Retain/Release
// it so hot rebuilds share the live shell; Rotate closes it so a new logical
// session cannot inherit cwd or environment.
type Manager struct {
	mu     sync.Mutex
	runMu  sync.Mutex
	owners int
	sealed bool
	live   *session
}

type session struct {
	mu          sync.Mutex
	conn        ptyConn
	fp          string
	closed      bool
	san         sanitizer
	pendingRead chan readChunkResult
}

type readChunkResult struct {
	data []byte
	err  error
}

// New returns a Manager with zero controller owners. Callers must Retain
// before Run.
func New() *Manager {
	return &Manager{}
}

// OrNew returns m when it is non-nil, otherwise a fresh Manager.
func OrNew(m *Manager) *Manager {
	if m != nil {
		return m
	}
	return New()
}

// Retain adds a Controller owner reference. Hot rebuilds Retain the shared
// Manager before publishing the replacement Controller.
func (m *Manager) Retain() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if !m.sealed {
		m.owners++
	}
	m.mu.Unlock()
}

// Release drops a Controller owner reference. The last owner seals the manager
// and closes the live PTY.
func (m *Manager) Release() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.owners > 0 {
		m.owners--
	}
	if m.owners == 0 {
		m.sealed = true
		live := m.live
		m.live = nil
		m.mu.Unlock()
		if live != nil {
			live.close()
		}
		return
	}
	m.mu.Unlock()
}

// Sealed reports whether the last Controller owner has released the Manager.
func (m *Manager) Sealed() bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sealed
}

// Rotate closes the live PTY so the next Run starts a fresh shell. Rotate on a
// sealed Manager is a no-op.
func (m *Manager) Rotate() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.sealed {
		m.mu.Unlock()
		return
	}
	live := m.live
	m.live = nil
	m.mu.Unlock()
	if live != nil {
		live.close()
	}
}

// Close is Rotate plus an explicit shutdown of the live PTY without sealing.
// Tests call it to reap the process; controllers use Release.
func (m *Manager) Close() {
	m.Rotate()
}

// Run executes command in the session-scoped PTY, creating the shell on first
// use. Commands with a matching launch fingerprint reuse cwd and environment.
func (m *Manager) Run(ctx context.Context, req Request) Result {
	if m == nil {
		return failResult(fmt.Errorf("%w: manager is nil", ErrUnavailable), tool.ShellPhaseLaunch)
	}
	if !Supports(req.Shell) {
		return failResult(fmt.Errorf("%w: %s", ErrUnavailable, unsupportedShellReason), tool.ShellPhaseLaunch)
	}
	m.runMu.Lock()
	defer m.runMu.Unlock()
	sess, err := m.sessionFor(req)
	if err != nil {
		return failResult(err, tool.ShellPhaseLaunch)
	}
	res := sess.run(ctx, req)
	if res.ShellDied || res.TimedOut || res.Canceled {
		m.drop(sess)
		res.Reset = true
	}
	return res
}

func (m *Manager) sessionFor(req Request) (*session, error) {
	fp := fingerprint(req)
	m.mu.Lock()
	if m.sealed || m.owners == 0 {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: manager closed", ErrUnavailable)
	}
	if m.live != nil && m.live.fp != fp {
		old := m.live
		m.live = nil
		m.mu.Unlock()
		old.close()
		m.mu.Lock()
		if m.sealed || m.owners == 0 {
			m.mu.Unlock()
			return nil, fmt.Errorf("%w: manager closed", ErrUnavailable)
		}
	}
	if m.live != nil {
		sess := m.live
		m.mu.Unlock()
		return sess, nil
	}
	m.mu.Unlock()

	sess, err := startSession(req, fp)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.sealed || m.owners == 0 {
		m.mu.Unlock()
		sess.close()
		return nil, fmt.Errorf("%w: manager closed", ErrUnavailable)
	}
	if m.live != nil {
		// Another caller won the start race; keep the existing shell.
		existing := m.live
		m.mu.Unlock()
		sess.close()
		return existing, nil
	}
	m.live = sess
	m.mu.Unlock()
	return sess, nil
}

func (m *Manager) drop(sess *session) {
	if m == nil || sess == nil {
		return
	}
	m.mu.Lock()
	if m.live == sess {
		m.live = nil
	}
	m.mu.Unlock()
	sess.close()
}

func fingerprint(req Request) string {
	h := sha256.New()
	for _, a := range req.Argv {
		h.Write([]byte(a))
		h.Write([]byte{0})
	}
	h.Write([]byte{1})
	h.Write([]byte(req.Dir))
	h.Write([]byte{1})
	for _, e := range req.Env {
		h.Write([]byte(e))
		h.Write([]byte{0})
	}
	h.Write([]byte{1})
	h.Write([]byte(req.Shell.Kind.String()))
	return hex.EncodeToString(h.Sum(nil))
}

func failResult(err error, phase string) Result {
	return Result{
		Err:          err,
		State:        tool.ShellStateFailed,
		FailurePhase: phase,
	}
}

func startSession(req Request, fp string) (*session, error) {
	conn, err := startPTY(req.Argv, req.Dir, req.Env)
	if err != nil {
		return nil, err
	}
	s := &session{
		conn: conn,
		fp:   fp,
	}
	s.startReader()
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()
	if err := s.writeScript(posixSetupScript()); err != nil {
		s.close()
		return nil, err
	}
	var buf strings.Builder
	if err := s.pump(ctx, func(text string) bool {
		buf.WriteString(text)
		return readyLine(buf.String())
	}); err != nil {
		s.close()
		return nil, fmt.Errorf("persistent shell startup: %w", err)
	}
	return s, nil
}

func (s *session) startReader() {
	s.pendingRead = make(chan readChunkResult, 1)
	go func() {
		buf := make([]byte, readChunk)
		for {
			n, err := s.conn.Read(buf)
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.pendingRead <- readChunkResult{data: chunk, err: err}
			if err != nil {
				return
			}
		}
	}()
}

func (s *session) writeScript(script string) error {
	_, err := s.conn.Write([]byte(script))
	return err
}

func (s *session) run(ctx context.Context, req Request) Result {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Result{
			ShellDied:    true,
			State:        tool.ShellStateFailed,
			FailurePhase: tool.ShellPhaseLaunch,
			Err:          errors.New("persistent shell closed"),
		}
	}
	s.mu.Unlock()

	runCtx := ctx
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	id := newMarkerID()
	start := "REASONIX_START_" + id
	// The status digits must follow the end marker immediately, so echoed
	// wrapper source can never fabricate a completion.
	end := "REASONIX_END_" + id + ":"
	if err := s.writeScript(posixCommandScript(req.Command, start, end)); err != nil {
		s.markClosed()
		return Result{
			ShellDied:    true,
			Started:      true,
			State:        tool.ShellStateFailed,
			FailurePhase: tool.ShellPhaseExecution,
			Err:          err,
		}
	}

	capt := newCapture(start, end, req.Progress)
	err := s.pump(runCtx, func(text string) bool {
		capt.push(text)
		return capt.done
	})
	if capt.done {
		res := Result{Output: capt.body(), ExitCode: capt.exitCode, Started: true}
		if capt.exitCode != 0 {
			res.State = tool.ShellStateFailed
			res.FailurePhase = tool.ShellPhaseExecution
			res.Err = fmt.Errorf("exit status %d", capt.exitCode)
		} else {
			res.State = tool.ShellStateCompleted
		}
		return res
	}
	// No status marker: whatever the command printed before it stopped is the
	// only evidence the model gets, so it is reported rather than discarded.
	res := Result{Output: capt.partial(), Started: true}
	switch {
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled):
		res.Canceled = true
		res.State = tool.ShellStateCancelled
		res.FailurePhase = tool.ShellPhaseCancellation
		res.Err = ctx.Err()
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
		res.State = tool.ShellStateTimedOut
		res.FailurePhase = tool.ShellPhaseTimeout
		res.Err = fmt.Errorf("command timed out (> %s)", req.Timeout)
	case err != nil:
		res.ShellDied = true
		res.State = tool.ShellStateFailed
		res.FailurePhase = tool.ShellPhaseExecution
		res.Err = err
	default:
		res.ShellDied = true
		res.State = tool.ShellStateFailed
		res.FailurePhase = tool.ShellPhaseExecution
		res.Err = errors.New("persistent shell exited before command completed")
	}
	s.markClosed()
	return res
}

func (s *session) markClosed() {
	s.mu.Lock()
	already := s.closed
	s.closed = true
	conn := s.conn
	s.mu.Unlock()
	if !already && conn != nil {
		_ = conn.Close()
	}
}

// pump feeds sanitized PTY reads to step until it reports completion. Each read
// is handed over once, so the cost of a command is linear in its output rather
// than quadratic in a re-scanned transcript.
func (s *session) pump(ctx context.Context, step func(string) bool) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case chunk, ok := <-s.pendingRead:
			if !ok {
				return errors.New("persistent shell reader closed")
			}
			if len(chunk.data) > 0 && step(s.san.push(chunk.data)) {
				return nil
			}
			if chunk.err != nil {
				if text := s.san.flush(); text != "" && step(text) {
					return nil
				}
				return chunk.err
			}
		}
	}
}

func (s *session) close() {
	s.markClosed()
}

// unsupportedShellReason explains why a shell has no session PTY. Callers fall
// back to one-shot execution, which is correct for every shell.
const unsupportedShellReason = "PowerShell has no session shell; a PowerShell host needs prompt-marker readiness and echo handling that this package does not implement"

// Supports reports whether sh can host a session PTY. PowerShell cannot: its
// host echoes submitted input and settles readiness through a prompt protocol,
// which the marker wrapper here does not speak, so a PowerShell shell never
// reports ready. Those hosts keep one-shot execution.
func Supports(sh sandbox.Shell) bool {
	return sh.Kind != sandbox.ShellPowerShell
}

// InteractiveArgv is the long-lived interpreter argv (no -c / -Command) used
// as the PTY child, before sandbox wrapping.
func InteractiveArgv(sh sandbox.Shell) []string {
	path := sh.Path
	if path == "" {
		path = sh.Kind.String()
	}
	switch sh.Kind {
	case sandbox.ShellPowerShell:
		return []string{path, "-NoLogo", "-NoProfile"}
	case sandbox.ShellZsh:
		return []string{path, "-f", "-i"}
	case sandbox.ShellSh:
		return []string{path, "-i"}
	default:
		return []string{path, "--noprofile", "--norc", "-i"}
	}
}
