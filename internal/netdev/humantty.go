package netdev

// humantty.go — 人工终端（NETDEV_SPEC_V2 §6.1）：设备 PTY 直达，人可以，
// agent 不可以，全程留痕。与 agent 的 CLI 诊断会话相互独立——人工终端拨
// 自己的 transport client（生命周期不随诊断会话的 idle reaper 抖动），
// 但共享 max_sessions_per_device 预算：占满即拒绝。输出经 onData 流回桌
// 面层（Wails "netdev:humantty" 事件 → 前端 xterm），全程录制（ANSI 剥离
// + 脱敏后落 evidence 文件），审计记录会话起止与字节量；紧急停止同样杀
// 人工终端（KillAllConnections 联动）。

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/crypto/ssh"

	"github.com/zzycxz/hiq/internal/netdev/transport"
)

// HumanTTYState is the terminal session lifecycle.
type HumanTTYState struct {
	Device    string    `json:"device"`
	Connected bool      `json:"connected"`
	StartedAt time.Time `json:"started_at"`
	Bytes     int64     `json:"bytes"` // total output bytes (for audit)
	Error     string    `json:"error,omitempty"`
}

const (
	humanTTYCols = 120
	humanTTYRows = 30
	// humanTTYRecCap bounds the in-memory recording (长会话只保尾部).
	humanTTYRecCap = 8 << 20
)

var (
	humanTTYMu       sync.Mutex
	humanTTYSessions = map[string]*humanTTYSession{} // device → session
)

type humanTTYSession struct {
	device  string
	started time.Time
	bytes   int64 // output bytes streamed
	inBytes int64 // input bytes sent

	cancel context.CancelFunc
	onData func(chunk string) // the desktop bridge installs the Wails forwarder
	closed chan struct{}

	client *transport.Client
	sess   *ssh.Session
	stdin  io.WriteCloser
	decode *streamDecoder

	recMu     sync.Mutex
	rec       bytes.Buffer
	writeMu   sync.Mutex // serializes stdin writes
	lastError string
}

// HumanTTYStart opens a human terminal session to a device: its own transport
// client + a PTY shell channel. The onData callback receives decoded output
// chunks (the desktop layer forwards them as "netdev:humantty" Wails events).
func (m *Manager) HumanTTYStart(deviceName string, onData func(chunk string)) (*HumanTTYState, error) {
	d, ok := m.cfg.NetDevDeviceByName(deviceName)
	if !ok {
		return nil, fmt.Errorf("device %q is not in the inventory", deviceName)
	}
	if d.Kind == "docker" || d.Kind == "k8s" || d.Kind == "firewall" {
		return nil, fmt.Errorf("kind=%s targets have no SSH CLI — use the API quick actions instead", d.Kind)
	}

	// 共享 VTY 预算（§6.1）：diagnostic session + netconf + 人工终端都计数。
	m.mu.Lock()
	use := m.vtySnapshotLocked(deviceName)
	deviceCap := m.vtyCap()
	m.mu.Unlock()
	humanTTYMu.Lock()
	_, already := humanTTYSessions[deviceName]
	humanTTYMu.Unlock()
	if !already && use >= deviceCap {
		return nil, fmt.Errorf("device %q 的会话预算已满（%d/%d，max_sessions_per_device）——关掉其他会话或人工终端再试", deviceName, use, deviceCap)
	}

	// Replacing an existing session closes the old one first.
	HumanTTYStop(deviceName)

	ctx, cancel := context.WithCancel(context.Background())
	sess := &humanTTYSession{
		device:  deviceName,
		started: time.Now(),
		cancel:  cancel,
		onData:  onData,
		closed:  make(chan struct{}),
		decode:  newStreamDecoder(d.Encoding),
	}

	client, err := m.dialDeviceClient(ctx, d)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dial %s: %w", deviceName, err)
	}
	sess.client = client

	sshClient, err := client.SSH()
	if err != nil {
		client.Close()
		cancel()
		return nil, err
	}
	sshSess, err := sshClient.NewSession()
	if err != nil {
		client.Close()
		cancel()
		return nil, fmt.Errorf("new session: %w", err)
	}
	if err := sshSess.RequestPty("xterm-256color", humanTTYRows, humanTTYCols, ssh.TerminalModes{}); err != nil {
		sshSess.Close()
		client.Close()
		cancel()
		return nil, fmt.Errorf("request pty: %w", err)
	}
	stdin, err := sshSess.StdinPipe()
	if err != nil {
		sshSess.Close()
		client.Close()
		cancel()
		return nil, err
	}
	sshSess.Stdout = sess // chunkWriter below
	sshSess.Stderr = sess
	if err := sshSess.Shell(); err != nil {
		sshSess.Close()
		client.Close()
		cancel()
		return nil, fmt.Errorf("shell: %w", err)
	}
	sess.sess = sshSess
	sess.stdin = stdin

	humanTTYMu.Lock()
	humanTTYSessions[deviceName] = sess
	humanTTYMu.Unlock()

	// Audit: the human terminal session itself is audited (§6.1 全程留痕).
	_ = AppendAudit(Audit{
		Time: time.Now(), Device: deviceName, Command: "human-terminal (interactive)",
		Class: "read", Status: AuditOK,
	})
	m.emitConnLive(deviceName, LiveConnConnected)

	// The session ends when the shell exits, the ctx is cancelled, or the
	// transport drops — one cleanup path for all three.
	go func() {
		waitErr := sshSess.Wait()
		sess.finish(waitErr)
		client.Close()
		close(sess.closed)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = stdin.Close()
			_ = sshSess.Close()
		case <-sess.closed:
		}
	}()

	return &HumanTTYState{Device: deviceName, Connected: true, StartedAt: sess.started}, nil
}

// Write is the PTY output tap: decode → stream to the bridge → record.
func (s *humanTTYSession) Write(p []byte) (int, error) {
	s.recMu.Lock()
	s.bytes += int64(len(p))
	s.rec.Write(p)
	if s.rec.Len() > humanTTYRecCap {
		s.rec.Next(s.rec.Len() - humanTTYRecCap)
	}
	s.recMu.Unlock()
	if s.onData != nil {
		if text := s.decode.push(p); text != "" {
			s.onData(text)
		}
	}
	return len(p), nil
}

// HumanTTYWrite sends user keystrokes to the device PTY.
func (m *Manager) HumanTTYWrite(deviceName, input string) error {
	humanTTYMu.Lock()
	sess, ok := humanTTYSessions[deviceName]
	humanTTYMu.Unlock()
	if !ok {
		return fmt.Errorf("no human terminal session on %q", deviceName)
	}
	sess.writeMu.Lock()
	defer sess.writeMu.Unlock()
	n, err := io.WriteString(sess.stdin, input)
	sess.inBytes += int64(n)
	return err
}

// HumanTTYResize forwards the frontend terminal's size changes to the PTY.
func HumanTTYResize(deviceName string, cols, rows int) error {
	humanTTYMu.Lock()
	sess, ok := humanTTYSessions[deviceName]
	humanTTYMu.Unlock()
	if !ok {
		return fmt.Errorf("no human terminal session on %q", deviceName)
	}
	if cols <= 0 || rows <= 0 || cols > 1000 || rows > 500 {
		return nil
	}
	return sess.sess.WindowChange(rows, cols)
}

// HumanTTYStop closes the human terminal session (idempotent).
func HumanTTYStop(deviceName string) {
	humanTTYMu.Lock()
	sess, ok := humanTTYSessions[deviceName]
	if ok {
		delete(humanTTYSessions, deviceName)
	}
	humanTTYMu.Unlock()
	if !ok {
		return
	}
	sess.cancel()
	<-sess.closed
}

// HumanTTYKillAll is the emergency-stop hook (§6.1 紧急停止同样杀人工终端).
func HumanTTYKillAll() int {
	humanTTYMu.Lock()
	sessions := make([]*humanTTYSession, 0, len(humanTTYSessions))
	for name, s := range humanTTYSessions {
		delete(humanTTYSessions, name)
		sessions = append(sessions, s)
	}
	humanTTYMu.Unlock()
	for _, s := range sessions {
		s.cancel()
	}
	for _, s := range sessions {
		<-s.closed
	}
	return len(sessions)
}

// HumanTTYStatus reports all active sessions (for the live panel / audit tab).
func HumanTTYStatus() []HumanTTYState {
	humanTTYMu.Lock()
	defer humanTTYMu.Unlock()
	out := make([]HumanTTYState, 0, len(humanTTYSessions))
	for _, s := range humanTTYSessions {
		s.recMu.Lock()
		b := s.bytes
		s.recMu.Unlock()
		out = append(out, HumanTTYState{Device: s.device, Connected: true, StartedAt: s.started, Bytes: b})
	}
	return out
}

// humanTTYCount is the live human-terminal count (feeds the VTY snapshot).
func humanTTYCount(device string) int {
	humanTTYMu.Lock()
	defer humanTTYMu.Unlock()
	if _, ok := humanTTYSessions[device]; ok {
		return 1
	}
	return 0
}

// finish finalizes one session: recording → evidence file (ANSI 剥离 + 脱敏),
// audit close entry, registry cleanup.
func (s *humanTTYSession) finish(waitErr error) {
	humanTTYMu.Lock()
	if humanTTYSessions[s.device] == s {
		delete(humanTTYSessions, s.device)
	}
	humanTTYMu.Unlock()

	recPath := ""
	s.recMu.Lock()
	raw := append([]byte(nil), s.rec.Bytes()...)
	s.recMu.Unlock()
	if len(raw) > 0 {
		if p, err := saveHumanTTYRecording(s.device, raw); err == nil {
			recPath = p
		}
	}

	note := fmt.Sprintf("human-terminal close (%d bytes out, %d bytes in)", s.bytes, s.inBytes)
	if recPath != "" {
		note += " recording: " + recPath
	}
	if waitErr != nil && waitErr != io.EOF && !strings.Contains(waitErr.Error(), "closed") {
		note += fmt.Sprintf(" end: %v", waitErr)
	}
	_ = AppendAudit(Audit{Time: time.Now(), Device: s.device, Command: note, Class: "read", Status: AuditOK})
}

// saveHumanTTYRecording lands the session's tail as a redacted text file under
// the netdev state dir (回放复用审计回放视图).
func saveHumanTTYRecording(device string, raw []byte) (string, error) {
	dir := filepath.Join(netdevStateDir(), "humantty")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s.txt", sanitizeFileToken(device), time.Now().Format("20060102-150405"))
	p := filepath.Join(dir, name)
	// 录制脱敏（§6.1）：ANSI 剥离后走统一脱敏器——密码/密钥不落盘。
	if err := os.WriteFile(p, []byte(Redact(ansi.Strip(string(raw)))), 0o600); err != nil {
		return "", err
	}
	return p, nil
}

// recNameRe parses recording filenames "<device>-<yyyymmdd>-<hhmmss>" — the
// device token itself may contain dashes, so anchor on the trailing stamp.
var recNameRe = regexp.MustCompile(`^(.*)-(\d{8}-\d{6})$`)

// HumanTTYRecording is one saved session recording (completion-spec §6 #5:
// 回放查看——文件已在落盘时 ANSI 剥离 + 脱敏，此处只做只读罗列/读取).
type HumanTTYRecording struct {
	Device string `json:"device"`
	Path   string `json:"path"`
	At     string `json:"at"` // yyyymmdd-hhmmss
	Bytes  int64  `json:"bytes"`
}

// ListHumanTTYRecordings scans the recording dir, newest first.
func ListHumanTTYRecordings() ([]HumanTTYRecording, error) {
	dir := filepath.Join(netdevStateDir(), "humantty")
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := []HumanTTYRecording{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".txt")
		m := recNameRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		out = append(out, HumanTTYRecording{
			Device: m[1], At: m[2],
			Path:  filepath.Join(dir, e.Name()),
			Bytes: fi.Size(),
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].At > out[b].At })
	return out, nil
}

// ReadHumanTTYRecording returns one recording's (already redacted) text. The
// path is restricted to the recordings dir — evidence files elsewhere are not
// readable through this surface.
func ReadHumanTTYRecording(path string) (string, error) {
	dir := filepath.Join(netdevStateDir(), "humantty")
	clean := filepath.Clean(path)
	if filepath.Dir(clean) != dir {
		return "", fmt.Errorf("not a human-terminal recording path")
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func sanitizeFileToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ── stream decoder ───────────────────────────────────────────────────────────

// streamDecoder turns byte chunks into text without splitting multibyte
// sequences at chunk boundaries: an incomplete UTF-8 tail is held back until
// the next chunk. Genuinely non-UTF-8 bytes fall back to GBK in auto mode
// (same trade-off as the diagnostic session's whole-buffer re-decode).
type streamDecoder struct {
	mode    string // auto | utf-8 | gbk
	pending []byte
}

func newStreamDecoder(encoding string) *streamDecoder {
	mode := strings.ToLower(strings.TrimSpace(encoding))
	if mode == "" {
		mode = "auto"
	}
	return &streamDecoder{mode: mode}
}

func (d *streamDecoder) push(p []byte) string {
	buf := append(d.pending, p...)
	d.pending = nil
	if d.mode == "gbk" {
		return decodeGBK(buf)
	}
	valid := 0
	for valid < len(buf) {
		r, size := utf8.DecodeRune(buf[valid:])
		if r == utf8.RuneError && size <= 1 {
			break
		}
		valid += size
	}
	if tail := buf[valid:]; len(tail) > 0 {
		if len(tail) <= 3 && !utf8.FullRune(tail) {
			// truncated multibyte sequence at the chunk boundary — wait
			d.pending = append([]byte(nil), tail...)
			buf = buf[:valid]
		} else if d.mode == "auto" {
			return decodeGBK(buf) // mid-stream invalid bytes: GBK fallback
		}
	}
	return string(buf)
}
