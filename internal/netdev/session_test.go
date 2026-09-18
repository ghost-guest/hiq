package netdev

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/zzycxz/hiq/internal/netdev/driver"
	"github.com/zzycxz/hiq/internal/netdev/transport"
)

// simDevice is an in-process SSH server emulating a Huawei-VRP-like CLI:
// echoes commands, prints "<SimSW>" prompts, pages long output behind a
// "---- More ----" prompt, answers with error lines on unknown commands, and
// can emit GBK bytes on demand to exercise the decoder.
type simDevice struct {
	addr     string
	password string
	prompt   string // shell prompt the simulator emits (driver-specific)
}

func startSimDevice(t *testing.T) *simDevice {
	return startSimDeviceWithPrompt(t, simPrompt)
}

// startSimDeviceWithPrompt boots the sim with a custom shell prompt — e.g. a
// bash-style prompt for vendor=linux devices (the linux driver's prompt
// pattern differs from the VRP one).
func startSimDeviceWithPrompt(t *testing.T, prompt string) *simDevice {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	sim := &simDevice{password: "pw", prompt: prompt}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == sim.password {
				return nil, nil
			}
			return nil, errors.New("bad password")
		},
	}
	cfg.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sim.addr = listener.Addr().String()
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveSimConn(conn, cfg, prompt)
		}
	}()
	return sim
}

func serveSimConn(conn net.Conn, cfg *ssh.ServerConfig, prompt string) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			continue
		}
		go serveSimSession(ch, chReqs, prompt)
	}
	_ = sconn.Close()
}

const simPrompt = "<SimSW>"

// simFS backs the sim's exec-channel file commands (cat/base64 -d/sha256sum/
// rm) — the e2e surface for the proposal file-upload path.
var (
	simFSMu sync.Mutex
	simFS   = map[string][]byte{}
)

func simFSGet(path string) ([]byte, bool) {
	simFSMu.Lock()
	defer simFSMu.Unlock()
	b, ok := simFS[path]
	return append([]byte(nil), b...), ok
}

func simFSPut(path string, b []byte) {
	simFSMu.Lock()
	simFS[path] = append([]byte(nil), b...)
	simFSMu.Unlock()
}

func simFSDel(path string) {
	simFSMu.Lock()
	delete(simFS, path)
	simFSMu.Unlock()
}

func serveSimSession(ch ssh.Channel, reqs <-chan *ssh.Request, prompt string) {
	for req := range reqs {
		switch req.Type {
		case "shell", "pty-req", "subsystem":
			if req.WantReply {
				req.Reply(true, nil)
			}
			if req.Type == "shell" {
				go runSimShell(ch, prompt)
				return
			}
			if req.Type == "subsystem" && strings.Contains(string(req.Payload), "netconf") {
				go runSimNetconf(ch)
				return
			}
		case "exec":
			cmd := parseSimExec(req.Payload)
			if req.WantReply {
				req.Reply(true, nil)
			}
			runSimExec(ch, cmd)
			return
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// parseSimExec decodes the RFC 4254 §6.5 exec payload (uint32 len + command).
func parseSimExec(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	n := int(payload[0])<<24 | int(payload[1])<<16 | int(payload[2])<<8 | int(payload[3])
	if n > len(payload)-4 || n < 0 {
		return ""
	}
	return string(payload[4 : 4+n])
}

func simExit(ch ssh.Channel, code uint32) {
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
	_ = ch.Close()
}

// runSimExec serves the exec-channel commands the proposal upload path issues.
// Everything else exits 127 — the sim is deliberately minimal.
func runSimExec(ch ssh.Channel, cmd string) {
	cmd = strings.TrimSpace(cmd)
	unquote := func(s string) string { return strings.Trim(s, "'\"") }
	switch {
	case strings.HasPrefix(cmd, "cat "):
		path := unquote(strings.TrimSpace(strings.TrimPrefix(cmd, "cat ")))
		if b, ok := simFSGet(path); ok {
			_, _ = ch.Write(b)
			simExit(ch, 0)
			return
		}
		simExit(ch, 1)
	case strings.HasPrefix(cmd, "base64 -d > "):
		path := unquote(strings.TrimSpace(strings.TrimPrefix(cmd, "base64 -d > ")))
		all, _ := io.ReadAll(ch) // stdin until the client's CloseWrite EOF
		dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(all)))
		if err != nil {
			_, _ = ch.Write([]byte("base64: " + err.Error() + "\n"))
			simExit(ch, 1)
			return
		}
		simFSPut(path, dec)
		simExit(ch, 0)
	case strings.HasPrefix(cmd, "sha256sum "):
		path := unquote(strings.TrimSpace(strings.TrimPrefix(cmd, "sha256sum ")))
		if b, ok := simFSGet(path); ok {
			sum := sha256.Sum256(b)
			fmt.Fprintf(ch, "%x  %s\n", sum, path)
			simExit(ch, 0)
			return
		}
		simExit(ch, 1)
	case strings.HasPrefix(cmd, "rm -f "):
		path := unquote(strings.TrimSpace(strings.TrimPrefix(cmd, "rm -f ")))
		simFSDel(path)
		simExit(ch, 0)
	case strings.HasPrefix(cmd, "sh -c "):
		// reload commands just succeed — the sim has no services.
		simExit(ch, 0)
	default:
		_, _ = ch.Write([]byte("sim: unsupported exec\n"))
		simExit(ch, 127)
	}
}

func runSimShell(ch ssh.Channel, prompt string) {
	defer ch.Close()
	write := func(s string) { _, _ = ch.Write([]byte(s)) }
	write("Welcome to the simulated VRP.\n")
	write(prompt)

	line := make([]byte, 0, 256)
	buf := make([]byte, 256)
	for {
		n, err := ch.Read(buf)
		if n > 0 {
			for _, b := range buf[:n] {
				if b == '\n' || b == '\r' {
					cmd := strings.TrimSpace(string(line))
					line = line[:0]
					if cmd != "" {
						write(cmd + "\n") // CLI echo of the completed line
						simDispatch(ch, cmd)
					}
					write(prompt)
					continue
				}
				line = append(line, b)
			}
		}
		if err != nil {
			return
		}
	}
}

func simDispatch(ch ssh.Channel, cmd string) {
	switch {
	case cmd == "screen-length 0 temporary" || cmd == "terminal length 0" || cmd == "terminal width 511":
		// acknowledged silently; caller prints the next prompt.
	case cmd == "display version":
		ch.Write([]byte("\nHuawei Versatile Routing Platform Software\nVRP (R) Software, Version 8.180 (S5735 V200R019C10)\nCopyright (C) 2000-2019 Huawei Technologies Co., Ltd.\n"))
	case cmd == "who":
		// One logged-in session — the §7.1 pre-execution online check's datum.
		ch.Write([]byte("\nroot     pts/0        2026-08-29 09:00 (10.1.0.50)\n"))
	case cmd == "display interface brief":
		ch.Write([]byte("\nPHY: Physical\n*down: administratively down\n(l): loopback\n(s): spoofing\nInUti/OutUti: input utility/output utility\n"))
	case cmd == "display gbk":
		// GBK-encoded Chinese output.
		gbk, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("\n接口当前状态: UP\n线路协议状态: UP\n"))
		ch.Write(gbk)
	case cmd == "display current-configuration":
		ch.Write([]byte("\n# current-configuration snapshot (simulated)\n#vlan 100 simulated block\nreturn\n"))
	case cmd == "vlan 100" || cmd == "description IoT" || cmd == "undo vlan 100" || cmd == "no-op":
		// Simulated writes succeed silently (the executor's path).
	case cmd == "display lldp neighbor":
		// The LLDP fixture from topology_test.go, verbatim device output.
		ch.Write([]byte("\n" + huaweiLLDPFixture))
	case cmd == "display long":
		// 60 lines in chunks of 24 behind a More prompt. The marker's bytes
		// stay in the stream (as on real devices whose ANSI erase sequences
		// vanish under strip, leaving the text); cleanOutput scrubs the line.
		const total = 60
		written := 0
		for written < total {
			chunk := total - written
			if chunk > 24 {
				chunk = 24
			}
			var b strings.Builder
			for i := 0; i < chunk; i++ {
				fmt.Fprintf(&b, "line %03d payload data for the paging test\n", written+i+1)
			}
			ch.Write([]byte(b.String()))
			written += chunk
			if written < total {
				ch.Write([]byte("  ---- More ----"))
				one := make([]byte, 1)
				for {
					n, err := ch.Read(one)
					if err != nil {
						return
					}
					if n == 1 && one[0] == ' ' {
						break
					}
				}
				ch.Write([]byte("\r\n"))
			}
		}
	default:
		ch.Write([]byte("\nError: Unrecognized command found at '^' position.\n"))
	}
}

// simConnection dials the simulator with auto-accept host keys and a client
// ready for session use.
func simConnection(t *testing.T, sim *simDevice) *transport.Client {
	t.Helper()
	host, portStr, err := net.SplitHostPort(sim.addr)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	policy := &transport.HostKeyPolicy{
		SystemKnownHosts: []string{filepath.Join(t.TempDir(), "none")},
		ManagedPath:      filepath.Join(t.TempDir(), "known_hosts"),
		Prompt:           func(ctx context.Context, q transport.HostKeyQuestion) (bool, error) { return true, nil },
	}
	c, err := transport.New(transport.Options{
		Host:        transport.ResolvedHost{HostName: host, Port: port, User: "admin"},
		Auth:        transport.AuthOptions{Password: func() (string, error) { return sim.password, nil }},
		HostKeys:    policy,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestSessionReadCommand(t *testing.T) {
	sim := startSimDevice(t)
	c := simConnection(t, sim)
	drv, _ := driver.For("huawei", "vrp8")

	s, err := OpenSession(context.Background(), c, drv, "auto")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()

	res, err := s.Run(context.Background(), "display version")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("read command flagged as error: %q", res.Output)
	}
	for _, want := range []string{"Huawei Versatile Routing Platform", "Version 8.180"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("output missing %q: %q", want, res.Output)
		}
	}
	if strings.Contains(res.Output, simPrompt) {
		t.Fatalf("prompt leaked into cleaned output: %q", res.Output)
	}
	if strings.Contains(res.Output, "display version") {
		t.Fatalf("echo leaked into cleaned output: %q", res.Output)
	}
}

func TestSessionPaging(t *testing.T) {
	sim := startSimDevice(t)
	c := simConnection(t, sim)
	drv, _ := driver.For("huawei", "vrp8")
	s, err := OpenSession(context.Background(), c, drv, "auto")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()

	res, err := s.Run(context.Background(), "display long")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Output, "line 001") || !strings.Contains(res.Output, "line 060") {
		t.Fatalf("paged output truncated: has first=%v last=%v",
			strings.Contains(res.Output, "line 001"), strings.Contains(res.Output, "line 060"))
	}
	if strings.Contains(res.Output, "More") {
		t.Fatalf("pager marker leaked: %q", res.Output)
	}
}

func TestSessionErrorDetection(t *testing.T) {
	sim := startSimDevice(t)
	c := simConnection(t, sim)
	drv, _ := driver.For("huawei", "vrp8")
	s, err := OpenSession(context.Background(), c, drv, "auto")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()

	res, err := s.Run(context.Background(), "display bogus")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("error output not flagged: %q", res.Output)
	}
	if !strings.Contains(res.Output, "Unrecognized") {
		t.Fatalf("error text lost: %q", res.Output)
	}
}

func TestSessionGBKDecode(t *testing.T) {
	sim := startSimDevice(t)
	c := simConnection(t, sim)
	drv, _ := driver.For("huawei", "vrp8")
	s, err := OpenSession(context.Background(), c, drv, "gbk")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()

	res, err := s.Run(context.Background(), "display gbk")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Output, "接口当前状态") {
		t.Fatalf("GBK decode failed: %q", res.Output)
	}
}

// runSimNetconf answers a minimal NETCONF agent: hello + <get> → fixed data.
func runSimNetconf(ch ssh.Channel) {
	defer ch.Close()
	write := func(doc string) {
		_, _ = ch.Write([]byte(fmt.Sprintf("\n#%d\n", len(doc))))
		_, _ = ch.Write([]byte(doc))
		_, _ = ch.Write([]byte("\n##\n"))
	}
	write("<hello xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\"><capabilities><capability>urn:ietf:params:netconf:base:1.0</capability></capabilities></hello>")
	sc := bufio.NewScanner(ch)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var msg string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || line == "" || line == "##" {
			continue
		}
		msg += line
		if strings.Contains(msg, "</rpc>") {
			switch {
			case strings.Contains(msg, "<get/>") || strings.Contains(msg, "<get>"):
				write("<?xml version=\"1.0\"?><rpc-reply xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\" message-id=\"1\"><data><interfaces><interface><name>GE0/0/1</name><admin-status>up</admin-status></interface></interfaces></data></rpc-reply>")
			default:
				write("<?xml version=\"1.0\"?><rpc-reply xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\" message-id=\"1\"><rpc-error><error-tag>operation-not-supported</error-tag></rpc-error></rpc-reply>")
			}
			msg = ""
		}
	}
}
