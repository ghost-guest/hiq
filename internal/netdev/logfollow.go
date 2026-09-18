package netdev

// logfollow.go — streaming log follow (tail -F / journalctl -f / docker logs
// -f) over a dedicated non-PTY exec session on the device's supervised SSH
// client. The follow command is composed from the same validated LogSource
// grammar as netdev_log_read (never model-written shell); every line is
// redacted before it crosses the observer boundary, and three hard caps
// ([netdev.log_follow]: lines / bytes / seconds) stop the stream no matter
// what — an unbounded follow is an incident, not a feature.

import (
	"bufio"
	"context"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

// LogFollowEvent is one streaming callback: a chunk of (redacted, line-aligned)
// text, or the terminal done event carrying the stop reason.
type LogFollowEvent struct {
	Device string `json:"device"`
	Source string `json:"source"`
	Chunk  string `json:"chunk,omitempty"`
	Done   bool   `json:"done,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// composeLogFollowCommand builds the streaming variant of a log source. Same
// source grammar and validations as composeLogCommand (logsource.go).
func composeLogFollowCommand(d config.NetDevDevice, source string) (string, error) {
	kind, rest, ok := strings.Cut(source, ":")
	if !ok || rest == "" {
		return "", fmt.Errorf("source must be file:<abs path> | journal:<unit> | docker:<container>, got %q", source)
	}
	switch kind {
	case "file":
		rest = strings.TrimSpace(rest)
		if !logPathAllowed(rest, LogAllowedRoots(d)) {
			return "", fmt.Errorf("path %q is outside the device's log whitelist (/var/log or the device's log_paths)", rest)
		}
		return "tail -F -n 0 " + path.Clean(rest), nil
	case "journal":
		if !logUnitRe.MatchString(rest) {
			return "", fmt.Errorf("invalid systemd unit %q", rest)
		}
		return "journalctl -f -u " + rest + " -n 0 --no-pager -q", nil
	case "docker":
		if !logUnitRe.MatchString(rest) {
			return "", fmt.Errorf("invalid container name %q", rest)
		}
		return "docker logs -f --tail 0 " + rest, nil
	default:
		return "", fmt.Errorf("unknown log source kind %q (file|journal|docker)", kind)
	}
}

// followState tracks one active follow for stop + dedup.
type followState struct {
	cancel  context.CancelFunc
	started time.Time
	source  string
}

// logFollowMu guards the per-device follow registry (one follow per device:
// a second start on the same device replaces the first — the UI is single-view).
var logFollowMu sync.Mutex
var logFollows = map[string]*followState{}

// LogFollow starts (or replaces) the streaming follow of one log source on
// one device. Events stream to onEvent until a cap trips, the device drops,
// or LogFollowStop is called; the final event is always Done with a reason.
func (m *Manager) LogFollow(deviceName, source string, onEvent func(LogFollowEvent)) error {
	device, ok := m.cfg.NetDevDeviceByName(deviceName)
	if !ok {
		return fmt.Errorf("device %q is not in the inventory", deviceName)
	}
	cmd, err := composeLogFollowCommand(device, source)
	if err != nil {
		return err
	}
	caps := m.cfg.NetDev.LogFollow.Capped()

	ctx, cancel := context.WithCancel(context.Background())
	fs := &followState{cancel: cancel, started: time.Now(), source: source}

	logFollowMu.Lock()
	if prev, ok := logFollows[deviceName]; ok {
		prev.cancel()
	}
	logFollows[deviceName] = fs
	logFollowMu.Unlock()

	_ = AppendAudit(Audit{Device: deviceName, Command: "log follow " + source, Class: "read", Status: AuditOK, OutputBytes: 0})

	go func() {
		reason := m.runFollow(ctx, deviceName, cmd, caps, onEvent, source)
		logFollowMu.Lock()
		if logFollows[deviceName] == fs {
			delete(logFollows, deviceName)
		}
		logFollowMu.Unlock()
		_ = AppendAudit(Audit{Device: deviceName, Command: "log follow " + source, Class: "read", Status: "stopped", Error: reason})
		onEvent(LogFollowEvent{Device: deviceName, Source: source, Done: true, Reason: reason})
	}()
	return nil
}

// LogFollowStop kills the device's active follow, if any.
func (m *Manager) LogFollowStop(deviceName string) {
	logFollowMu.Lock()
	fs := logFollows[deviceName]
	logFollowMu.Unlock()
	if fs != nil {
		fs.cancel()
	}
}

// LogFollowActive reports the device's active follow source ("" = none).
func (m *Manager) LogFollowActive(deviceName string) string {
	logFollowMu.Lock()
	defer logFollowMu.Unlock()
	if fs := logFollows[deviceName]; fs != nil {
		return fs.source
	}
	return ""
}

// followClient returns the device's supervised transport client, dialing on
// demand (same caching discipline as runRead, minus the PTY session — the
// follow rides its own exec session).
func (m *Manager) followClient(ctx context.Context, deviceName string) (*managedConn, error) {
	m.mu.Lock()
	existing, ok := m.conns[deviceName]
	if ok {
		existing.lastUse = time.Now()
	}
	m.mu.Unlock()
	if ok {
		return existing, nil
	}
	device, found := m.cfg.NetDevDeviceByName(deviceName)
	if !found {
		return nil, fmt.Errorf("device %q vanished from config", deviceName)
	}
	drv, ok := m.driverFor(device)
	if !ok {
		return nil, fmt.Errorf("no driver for %s/%s", device.Vendor, device.OS)
	}
	client, session, err := m.connect(ctx, device, drv)
	if err != nil {
		return nil, err
	}
	conn := &managedConn{client: client, session: session, drv: drv, lastUse: time.Now()}
	m.mu.Lock()
	if other, exists := m.conns[deviceName]; exists {
		m.mu.Unlock()
		client.Close() // lost the race; ride the existing connection
		return other, nil
	}
	m.conns[deviceName] = conn
	use := m.vtySnapshotLocked(deviceName)
	m.mu.Unlock()
	m.emitLive(LiveEvent{Kind: LiveConn, Device: deviceName, State: LiveConnConnected, VTYUse: use, VTYCap: m.vtyCap()})
	return conn, nil
}

// runFollow opens the raw exec session and streams capped, redacted lines.
// Returns the stop reason (surfaced in the Done event and the audit entry).
func (m *Manager) runFollow(ctx context.Context, deviceName, cmd string, caps config.NetDevLogFollow, onEvent func(LogFollowEvent), source string) string {
	conn, err := m.followClient(ctx, deviceName)
	if err != nil {
		return "connect: " + err.Error()
	}
	cl, err := conn.client.SSH()
	if err != nil {
		return "ssh client: " + err.Error()
	}
	sess, err := cl.NewSession()
	if err != nil {
		return "session: " + err.Error()
	}
	defer sess.Close()

	stdout, err := sess.StdoutPipe()
	if err != nil {
		return "stdout pipe: " + err.Error()
	}
	if err := sess.Start(cmd); err != nil {
		return "start: " + err.Error()
	}

	timer := time.AfterFunc(time.Duration(caps.MaxSeconds)*time.Second, func() { sess.Close() })
	defer timer.Stop()

	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()

	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	nLines, totalBytes := 0, 0
	var linesCh chan string = lines
	for {
		if linesCh == nil {
			// Scanner finished; only the session-exit case remains.
			err := <-done
			if ctx.Err() != nil {
				return "stopped by user"
			}
			if err != nil {
				return "stream ended: " + err.Error()
			}
			return "stream ended"
		}
		select {
		case <-ctx.Done():
			return "stopped by user"
		case err := <-done:
			if err == nil && linesCh != nil {
				// Session exited but the scanner may still have buffered
				// lines; drain once the channel closes (next receive).
				continue
			}
			if ctx.Err() != nil {
				return "stopped by user"
			}
			if err != nil {
				return "stream ended: " + err.Error()
			}
			return "stream ended"
		case line, open := <-linesCh:
			if !open {
				linesCh = nil
				continue
			}
			nLines++
			totalBytes += len(line) + 1
			if out := Redact(line); out != "" {
				onEvent(LogFollowEvent{Device: deviceName, Source: source, Chunk: out + "\n"})
			}
			if nLines >= caps.MaxLines {
				sess.Close()
				return fmt.Sprintf("line cap reached (%d lines)", caps.MaxLines)
			}
			if totalBytes >= caps.MaxBytes {
				sess.Close()
				return fmt.Sprintf("byte cap reached (%d bytes)", caps.MaxBytes)
			}
		}
	}
}
