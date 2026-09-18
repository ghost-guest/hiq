package netdev

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/crypto/ssh"
	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/zzycxz/hiq/internal/netdev/driver"
	"github.com/zzycxz/hiq/internal/netdev/transport"
)

// Session is one PTY-driven interactive CLI session with a network device,
// built on a transport.Client's SSH connection. It exists because network
// devices generally do not support one-shot exec channels: the CLI must be
// driven interactively (paging off, prompt detection, echo stripping) — the
// driver layer supplies the vendor knowledge.
type Session struct {
	client *transport.Client
	drv    driver.Driver
	sess   *ssh.Session
	stdin  io.WriteCloser
	out    *syncBuffer // device bytes since the shell started (pre-decode)

	encode func([]byte) string

	// onLive, when set, receives incremental CLEANED+REDACTED output text as
	// it arrives during Run (the 操作实况 tap). Line-aligned: a chunk is
	// emitted only once its trailing newline has arrived, so redaction (which
	// is line-scoped) never misses a secret split across ticks. The trailing
	// prompt line is flushed when the command completes.
	onLive func(string)

	mu     sync.Mutex
	closed bool
}

// SetOutputObserver installs the live output tap (nil removes it). Callers
// own the callback's thread-safety; Manager's observer coalesces off-thread.
func (s *Session) SetOutputObserver(fn func(string)) {
	s.mu.Lock()
	s.onLive = fn
	s.mu.Unlock()
}

// Session timing defaults.
const (
	defaultCommandTimeout = 30 * time.Second
	sessionOpenTimeout    = 15 * time.Second
)

// pagerPattern matches the interactive "--More--" / "---- More ----" prompt
// at the end of pending output. When seen, the engine sends a space.
var pagerPattern = regexp.MustCompile(`(?:^|\n)\s*-{2,4}\s*More\s*-{2,4}\s*$`)

// pagerLinePattern matches a pager marker that ended up as (part of) a line in
// the finished output — real devices "erase" it with cursor sequences, but the
// bytes stay in the stream once ANSI escapes are stripped, so the cleaner
// scrubs the remnant lines explicitly (the netmiko/scrapli approach).
var pagerLinePattern = regexp.MustCompile(`^\s*-{0,4}\s*More\s*-{0,4}\s*$`)

// syncBuffer serializes the ssh mux's session writes against the engine's
// polls — bytes.Buffer is not goroutine-safe and the mux writes concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *syncBuffer) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// OpenSession establishes the interactive CLI over an already-connected
// transport.Client: requests a PTY, starts the shell, disables paging per the
// driver, and waits for the first prompt.
func OpenSession(ctx context.Context, client *transport.Client, drv driver.Driver, encoding string) (*Session, error) {
	sshClient, err := client.SSH()
	if err != nil {
		return nil, err
	}
	sess, err := sshClient.NewSession()
	if err != nil {
		return nil, fmt.Errorf("netdev: new session: %w", err)
	}
	// A wide terminal avoids devices wrapping long lines into visual noise.
	if err := sess.RequestPty("vt100", 40, 512, ssh.TerminalModes{}); err != nil {
		sess.Close()
		return nil, fmt.Errorf("netdev: request pty: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	out := &syncBuffer{}
	sess.Stdout = out
	sess.Stderr = out // devices interleave diagnostics on stderr
	if err := sess.Shell(); err != nil {
		sess.Close()
		return nil, fmt.Errorf("netdev: shell: %w", err)
	}

	s := &Session{
		client: client,
		drv:    drv,
		sess:   sess,
		stdin:  stdin,
		out:    out,
		encode: decoderFor(encoding),
	}

	ctx, cancel := context.WithTimeout(ctx, sessionOpenTimeout)
	defer cancel()
	for _, cmd := range drv.PagingOff() {
		if _, err := s.Run(ctx, cmd); err != nil {
			s.Close()
			return nil, fmt.Errorf("netdev: paging-off %q: %w", cmd, err)
		}
	}
	// Drain the shell's pre-command bytes (banner + first prompt) so the
	// FIRST Run's output is clean — line-counting consumers (§7.1 who/quser
	// online check) must not see the banner as a session row. Drivers without
	// paging-off (linux) never waited for a prompt; wait for it now, then
	// flush whatever preceded it.
	deadline := time.Now().Add(sessionOpenTimeout)
	for {
		if drv.Prompt().MatchString(s.encode(s.out.snapshot())) {
			s.out.reset()
			return s, nil
		}
		if time.Now().After(deadline) {
			// No prompt seen (some devices are slow/bannerless) — return the
			// session as-is; the first Run tolerates the leftover bytes.
			return s, nil
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// Result is one command's outcome.
type Result struct {
	Command string
	Output  string // cleaned text: echo and trailing prompt removed
	IsError bool   // a driver error pattern matched
}

// Run sends one command and returns when the device prompt reappears.
// The command must already be classified Read by the caller — the exec tool
// enforces that; the session layer stays classification-agnostic.
func (s *Session) Run(ctx context.Context, cmd string) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Result{}, errors.New("netdev: session closed")
	}
	cmd = strings.TrimRight(cmd, "\r\n")
	if cmd == "" {
		return Result{}, errors.New("netdev: empty command")
	}
	deadline := time.Now().Add(defaultCommandTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	s.out.reset()
	// Live tap bookkeeping: prevText is the decoded output already accounted
	// for; each tick emits the newly-arrived COMPLETE lines (sanitized), and
	// the trailing partial (usually the prompt) flushes on completion.
	prevText := ""
	emitLive := func(all bool) {
		if s.onLive == nil {
			return
		}
		text := s.encode(s.out.snapshot())
		if len(text) < len(prevText) || !strings.HasPrefix(text, prevText) {
			// Buffer reset or rewrite (should not happen inside one Run) —
			// resync silently rather than replay output.
			prevText = text
			return
		}
		delta := text[len(prevText):]
		prevText = text
		cut := len(delta)
		if !all {
			if i := strings.LastIndexByte(delta, '\n'); i >= 0 {
				cut = i + 1
			} else {
				return // no complete new line yet
			}
		}
		if chunk := sanitizeLiveChunk(delta[:cut]); chunk != "" {
			s.onLive(chunk)
		}
	}
	if _, err := io.WriteString(s.stdin, cmd+"\n"); err != nil {
		return Result{}, fmt.Errorf("netdev: write command: %w", err)
	}

	// Poll until the driver prompt anchors at the end of the output and the
	// echo (or at least a full output line) has arrived. Polling is bounded by
	// the ticker; completion is decided by the prompt match, never by EOF.
	tick := time.NewTicker(15 * time.Millisecond)
	defer tick.Stop()
	for {
		text := s.encode(s.out.snapshot())
		if pagerPattern.MatchString(text) {
			if _, err := io.WriteString(s.stdin, " "); err != nil {
				return Result{}, fmt.Errorf("netdev: advance pager: %w", err)
			}
		} else if s.completed(cmd, text) {
			emitLive(true) // flush the trailing prompt line
			return s.finish(cmd, text), nil
		}
		emitLive(false)
		if time.Now().After(deadline) {
			return Result{}, fmt.Errorf("netdev: timeout waiting for prompt after %q (partial output: %.200s)", cmd, text)
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		<-tick.C
	}
}

// completed reports whether the device finished executing: the prompt sits at
// the end of the output AND the echoed command (or, when the device does not
// echo, at least one output line) has been seen. The echo/line guard prevents
// matching the leftover prompt that precedes the device's processing of the
// command.
func (s *Session) completed(cmd, text string) bool {
	if !s.drv.Prompt().MatchString(text) {
		return false
	}
	if strings.Contains(strings.ToLower(text), strings.ToLower(cmd)) {
		return true
	}
	return strings.Count(text, "\n") >= 2
}

func (s *Session) finish(cmd, text string) Result {
	out := cleanOutput(text, cmd, s.drv)
	isErr := false
	for _, line := range strings.Split(out, "\n") {
		for _, re := range s.drv.Errors() {
			if re.MatchString(line) {
				isErr = true
				break
			}
		}
		if isErr {
			break
		}
	}
	return Result{Command: cmd, Output: out, IsError: isErr}
}

// cleanOutput strips ANSI sequences, normalizes newlines, removes the echoed
// command line, the trailing prompt line, and pager remnant lines.
func cleanOutput(text, cmd string, drv driver.Driver) string {
	text = ansi.Strip(text)
	text = strings.ReplaceAll(text, "\b", "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	// Drop pager remnants (lines that are only a More marker).
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if pagerLinePattern.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	lines = kept

	// Drop the echoed command: the first line that contains it (the device
	// prefixes it with the prompt).
	norm := strings.ToLower(cmd)
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), norm) {
			lines = append(lines[:i], lines[i+1:]...)
			break
		}
	}
	// Drop the trailing prompt line.
	if n := len(lines); n > 0 {
		last := strings.TrimRight(lines[n-1], " ")
		if drv.Prompt().MatchString("\n" + last) {
			lines = lines[:n-1]
		}
	}
	// Trim empty head/tail lines left by the stripping.
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// decoderFor builds the raw-bytes→text conversion per the configured
// encoding. "auto": strict UTF-8 first, GBK when the bytes are not valid
// UTF-8 (domestic Huawei/ZTE builds may emit GBK).
func decoderFor(encoding string) func([]byte) string {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "gbk":
		return decodeGBK
	case "utf-8":
		return decodeUTF8
	default: // auto
		return func(b []byte) string {
			if utf8.Valid(b) {
				return string(b)
			}
			return decodeGBK(b)
		}
	}
}

func decodeUTF8(b []byte) string { return string(b) }

func decodeGBK(b []byte) string {
	// Whole-buffer decode: chunk boundaries can split multibyte sequences, and
	// CLI outputs are small enough to re-decode per poll.
	out, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
	if err != nil {
		return string(b) // best effort: show raw
	}
	return string(out)
}

// Close ends the session (the transport Client stays connected).
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.stdin.Close()
	return s.sess.Close()
}
