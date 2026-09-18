package netdev

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev/driver"
)

// NETCONF (RFC 6241/6242) over the device's SSH connection: "netconf"
// subsystem, chunked framing (base:1.0), hello exchange, then one RPC per
// call. READ-ONLY by allowlist: only <get> and <get-config> inner elements
// pass — the NETCONF equivalent of the CLI classifier (edit-config and friends
// are proposal material, and there is no proposal path to NETCONF yet).

var netconfMsgID int64

// allowedNetconfOps is the read allowlist for the inner RPC element.
var allowedNetconfOps = []string{"<get>", "<get-config"}

// NetconfRPC runs one read-only NETCONF RPC on the device and returns the
// <rpc-reply>…</rpc-reply> document (raw XML text, redacted upstream of the
// model by the exec-path caller if surfaced there).
func (m *Manager) NetconfRPC(ctx context.Context, deviceName, inner string) (string, error) {
	trimmed := strings.TrimSpace(inner)
	allowed := false
	for _, op := range allowedNetconfOps {
		if trimmed == op || strings.HasPrefix(trimmed, op[:len(op)-1]+" ") ||
			(strings.HasPrefix(trimmed, "<get") && strings.HasPrefix(op, "<get")) {
			allowed = true
		}
	}
	if !allowed || strings.Contains(trimmed, "edit-config") ||
		strings.Contains(trimmed, "copy-config") || strings.Contains(trimmed, "delete-config") ||
		strings.Contains(trimmed, "kill-session") || strings.Contains(trimmed, "commit") ||
		strings.Contains(trimmed, "lock") {
		_ = AppendAudit(Audit{Device: deviceName, Command: "netconf " + trimmed, Class: "write", Status: AuditRefused})
		m.liveCmdRefused(deviceName, "netconf "+trimmed, "write", "netconf is read-only: only <get>/<get-config> are allowed")
		return "", errors.New("netdev netconf is read-only: only <get> and <get-config> are allowed; changes go through the proposal pipeline")
	}

	// Command lifecycle for the live panel: netconf is one-shot (no mid-flight
	// output stream), so start/end brackets the whole RPC.
	start := m.liveCmdStart(deviceName, "netconf "+trimmed, "read")
	status := "failure"
	defer func() { m.liveCmdEnd(deviceName, "netconf "+trimmed, "read", status, start, 0, "") }()

	// VTY guard: this RPC opens a SECOND session on the device while the CLI
	// session may hold one — enforce max_sessions_per_device before the
	// subsystem request (NETDEV_SPEC §6.5/B-6).
	if err := m.acquireNetconfVTY(deviceName); err != nil {
		return "", err
	}
	defer m.releaseNetconfVTY(deviceName)

	d, ok := m.cfg.NetDevDeviceByName(deviceName)
	if !ok {
		return "", fmt.Errorf("device %q not in inventory", deviceName)
	}
	drv, ok := m.driverFor(d)
	if !ok {
		return "", fmt.Errorf("no driver for %s/%s", d.Vendor, d.OS)
	}

	// Reuse the device's supervised connection; NETCONF rides a NEW session
	// on it (the CLI session stays untouched).
	sshClient, err := m.sshFor(ctx, d, drv)
	if err != nil {
		return "", err
	}
	sess, err := sshClient.NewSession()
	if err != nil {
		return "", fmt.Errorf("netconf: new session: %w", err)
	}
	defer sess.Close()
	if err := sess.RequestSubsystem("netconf"); err != nil {
		return "", fmt.Errorf("netconf: subsystem refused (device may not run NETCONF): %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		return "", err
	}
	// StdoutPipe is the library's own pipe-backed reader (blocking semantics;
	// known-good pattern for netconf-over-SSH clients). Must be taken BEFORE
	// the subsystem request starts the session.
	pr, err := sess.StdoutPipe()
	if err != nil {
		return "", err
	}
	lr := newLineReader(pr)

	// Hello exchange (server hello read with a bounded wait).
	if _, err := stdin.Write(chunkFrame(helloDoc())); err != nil {
		return "", err
	}
	deadline := time.Now().Add(10 * time.Second)
	if _, err := dechunk(lr, deadline); err != nil { // server hello; contents unused
		return "", fmt.Errorf("netconf: hello: %w", err)
	}

	// One RPC; read until </rpc-reply>.
	if _, err := stdin.Write(chunkFrame(rpcDoc(trimmed))); err != nil {
		return "", err
	}
	deadline = time.Now().Add(30 * time.Second)
	doc, err := dechunk(lr, deadline)
	for err == nil && !rpcReplyEnd.Match(doc) {
		var more []byte
		more, err = dechunk(lr, deadline)
		doc = append(doc, more...)
	}
	if err != nil {
		return "", fmt.Errorf("netconf: rpc: %w", err)
	}
	reply := string(doc)
	if isRPCError(reply) {
		status = AuditDeviceError
	} else {
		status = AuditOK
	}
	_ = AppendAudit(Audit{Device: deviceName, Command: "netconf " + trimmed, Class: "read", Status: status, OutputBytes: len(reply)})
	if isRPCError(reply) {
		return reply, fmt.Errorf("netdev netconf: device returned rpc-error (see reply)")
	}
	return reply, nil
}

// acquireNetconfVTY claims one in-flight NETCONF subsystem session against the
// device's max_sessions_per_device cap (the CLI session and concurrent RPCs
// share the same VTY budget). Refused pre-dial with audit + a live refusal.
func (m *Manager) acquireNetconfVTY(deviceName string) error {
	m.mu.Lock()
	use := m.vtySnapshotLocked(deviceName)
	cap := m.vtyCap()
	if use >= cap {
		m.mu.Unlock()
		reason := fmt.Sprintf("device %q is at its session cap (%d/%d, max_sessions_per_device) — the CLI session and concurrent NETCONF RPCs share the device's VTY lines", deviceName, use, cap)
		_ = AppendAudit(Audit{Device: deviceName, Command: "(netconf)", Class: "guardrail", Status: AuditRefused})
		m.liveCmdRefused(deviceName, "(netconf)", "guardrail", reason)
		return errors.New(reason)
	}
	m.netconfInflight[deviceName]++
	m.mu.Unlock()
	return nil
}

// releaseNetconfVTY drops one in-flight NETCONF session claim.
func (m *Manager) releaseNetconfVTY(deviceName string) {
	m.mu.Lock()
	if n := m.netconfInflight[deviceName]; n > 1 {
		m.netconfInflight[deviceName] = n - 1
	} else {
		delete(m.netconfInflight, deviceName)
	}
	m.mu.Unlock()
}

// sshFor returns the device's connected ssh.Client, dialing if needed.
func (m *Manager) sshFor(ctx context.Context, d config.NetDevDevice, drv driver.Driver) (SSHClient, error) {
	m.mu.Lock()
	existing, ok := m.conns[d.Name]
	if ok {
		existing.lastUse = time.Now()
	}
	m.mu.Unlock()
	if ok {
		return existing.client.SSH()
	}
	client, session, err := m.connect(ctx, d, drv)
	if err != nil {
		return nil, err
	}
	// connect established a CLI session too; register it for reuse.
	m.mu.Lock()
	if other, ok := m.conns[d.Name]; ok {
		m.mu.Unlock()
		session.Close()
		client.Close()
		return other.client.SSH()
	}
	m.conns[d.Name] = &managedConn{client: client, session: session, drv: drv, lastUse: time.Now()}
	m.mu.Unlock()
	return client.SSH()
}

// SSHClient is the subset netconf needs (avoids importing ssh types further).
type SSHClient interface {
	NewSession() (*ssh.Session, error)
}

// ── framing (RFC 6242 chunked, base:1.0) ────────────────────────────────────

// chunkFrame wraps data in one chunk.
func chunkFrame(data []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "\n#%d\n", len(data))
	b.Write(data)
	b.WriteString("\n##\n")
	return b.Bytes()
}

// dechunk reads chunked output until the end-of-chunks marker, returning the
// concatenated payloads.
func dechunk(r io.Reader, deadline time.Time) ([]byte, error) {
	var out bytes.Buffer
	br := newLineReader(r)
	for {
		line, err := br.readLine(deadline)
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "##" {
			return out.Bytes(), nil
		}
		if !strings.HasPrefix(line, "#") {
			continue // between-chunks noise (e.g. EOM from servers)
		}
		n, err := strconv.Atoi(line[1:])
		if err != nil || n < 0 {
			continue
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil, err
		}
		out.Write(buf)
	}
}

// lineReader is a tiny buffered reader with a deadline per line.
type lineReader struct {
	r   io.Reader
	buf []byte
}

func newLineReader(r io.Reader) *lineReader { return &lineReader{r: r} }

func (l *lineReader) readLine(deadline time.Time) (string, error) {
	for {
		if i := bytes.IndexByte(l.buf, '\n'); i >= 0 {
			line := string(l.buf[:i+1])
			l.buf = l.buf[i+1:]
			return line, nil
		}
		if time.Now().After(deadline) {
			return "", errors.New("netconf: line deadline exceeded")
		}
		tmp := make([]byte, 4096)
		n, err := l.r.Read(tmp)
		if n > 0 {
			l.buf = append(l.buf, tmp[:n]...)
			continue
		}
		if err != nil {
			return "", err
		}
	}
}

func (l *lineReader) Read(p []byte) (int, error) {
	// Satisfy io.Reader for io.ReadFull on chunk payloads.
	for len(l.buf) == 0 {
		tmp := make([]byte, 4096)
		n, err := l.r.Read(tmp)
		if n > 0 {
			l.buf = tmp[:n]
			break
		}
		if err != nil {
			return 0, err
		}
	}
	n := copy(p, l.buf)
	l.buf = l.buf[n:]
	return n, nil
}

// helloDoc builds a base:1.0 client hello.
func helloDoc() []byte {
	h := xml.Header + "<hello xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\">" +
		"<capabilities><capability>urn:ietf:params:netconf:base:1.0</capability></capabilities>" +
		"</hello>"
	return []byte(h)
}

// rpcDoc wraps an inner element with an rpc envelope.
func rpcDoc(inner string) []byte {
	id := atomic.AddInt64(&netconfMsgID, 1)
	return []byte(xml.Header + fmt.Sprintf("<rpc xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\" message-id=\"%d\">%s</rpc>", id, inner))
}

var rpcReplyEnd = regexp.MustCompile(`</rpc-reply>`)

// isRPCError reports whether a reply doc carries <rpc-error>.
func isRPCError(reply string) bool {
	return strings.Contains(reply, "<rpc-error>")
}
