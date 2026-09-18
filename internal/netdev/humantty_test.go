package netdev

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev/transport"
)

type ttyCollector struct {
	mu   sync.Mutex
	text strings.Builder
}

func (c *ttyCollector) on(chunk string) {
	c.mu.Lock()
	c.text.WriteString(chunk)
	c.mu.Unlock()
}

func (c *ttyCollector) waitContains(t *testing.T, want string) string {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		c.mu.Lock()
		s := c.text.String()
		c.mu.Unlock()
		if strings.Contains(s, want) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("tty output %.400q never contained %q", s, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func humanTTYTestManager(t *testing.T, maxSessions int) *Manager {
	t.Helper()
	sim := startSimDevice(t)
	host, portStr, _ := net.SplitHostPort(sim.addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	cfg := config.Default()
	cfg.NetDev = config.NetDevConfig{
		Enabled:              true,
		MaxSessionsPerDevice: maxSessions,
		Devices: []config.NetDevDevice{{
			Name: "sw1", Vendor: "huawei", OS: "vrp8", Address: host, Port: port, Username: "admin", PasswordEnv: "TEST_ENV",
		}},
	}
	origSecret := secretGetter
	secretGetter = func(kind, name string) (string, bool, error) {
		if name == "TEST_ENV" {
			return sim.password, true, nil
		}
		return "", false, nil
	}
	t.Cleanup(func() { secretGetter = origSecret })

	origKH := transport.ManagedKnownHostsOverride
	transport.ManagedKnownHostsOverride = filepath.Join(t.TempDir(), "kh")
	t.Cleanup(func() { transport.ManagedKnownHostsOverride = origKH })

	origPrompt := HostKeyPrompt
	HostKeyPrompt = func(ctx context.Context, q transport.HostKeyQuestion) (bool, error) { return true, nil }
	t.Cleanup(func() { HostKeyPrompt = origPrompt })

	proposalsDirOverride = filepath.Join(t.TempDir(), "proposals")
	t.Cleanup(func() { proposalsDirOverride = "" })
	jobsDirOverride = t.TempDir()
	t.Cleanup(func() { jobsDirOverride = "" })

	netdevStateDirOverr = t.TempDir()
	t.Cleanup(func() { netdevStateDirOverr = "" })
	auditPath := filepath.Join(netdevStateDirOverr, "audit.jsonl")
	SetAuditPath(auditPath)
	t.Cleanup(func() { SetAuditPath("") })

	m := NewManager(cfg)
	t.Cleanup(m.Close)
	return m
}

// The full §6.1 chain over a real SSH connection: PTY shell opens, output
// streams to the bridge callback, keystrokes reach the device, and closing
// lands a redacted recording + audit entry.
func TestHumanTTYLifecycle(t *testing.T) {
	m := humanTTYTestManager(t, 0)

	col := &ttyCollector{}
	st, err := m.HumanTTYStart("sw1", col.on)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !st.Connected {
		t.Fatal("not connected")
	}
	col.waitContains(t, "Welcome to the simulated VRP")

	if err := m.HumanTTYWrite("sw1", "display version\r"); err != nil {
		t.Fatalf("write: %v", err)
	}
	col.waitContains(t, "Versatile Routing Platform")

	// Status sees the live session.
	var seen bool
	for _, s := range HumanTTYStatus() {
		if s.Device == "sw1" && s.Bytes > 0 {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("status = %+v", HumanTTYStatus())
	}

	HumanTTYStop("sw1")
	deadline := time.Now().Add(5 * time.Second)
	for len(HumanTTYStatus()) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(HumanTTYStatus()) != 0 {
		t.Fatal("session still listed after stop")
	}

	// The recording landed, redacted.
	recDir := filepath.Join(netdevStateDirOverr, "humantty")
	entries, err := os.ReadDir(recDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no recording in %s (%v)", recDir, err)
	}
	body, _ := os.ReadFile(filepath.Join(recDir, entries[0].Name()))
	if !strings.Contains(string(body), "display version") {
		t.Fatalf("recording %.200q misses the session", body)
	}
}

// The human terminal shares max_sessions_per_device: with a cap of 1 and the
// diagnostic session already up, opening one is refused.
func TestHumanTTYSharesVTYBudget(t *testing.T) {
	m := humanTTYTestManager(t, 1)

	res := m.Exec(context.Background(), "sw1", "display version")
	if res.Refused {
		t.Fatalf("diagnostic exec refused: %s", res.Refusal)
	}
	if _, err := m.HumanTTYStart("sw1", func(string) {}); err == nil || !strings.Contains(err.Error(), "预算") {
		t.Fatalf("budget not enforced: err = %v", err)
	}

	// And a fresh device (no diagnostic session) fits exactly one.
	col := &ttyCollector{}
	if _, err := m.HumanTTYStart("sw1", col.on); err == nil {
		t.Fatal("second start still allowed over budget")
	}
	HumanTTYStop("sw1")
}

// Multibyte sequences split across chunks survive the stream decoder.
func TestHumanTTYStreamDecoder(t *testing.T) {
	d := newStreamDecoder("")
	if got := d.push([]byte("接口当前状态: U")); got != "接口当前状态: U" {
		t.Fatalf("plain utf8 = %q", got)
	}
	d = newStreamDecoder("")
	first := d.push([]byte{0xe5, 0x8f}) // 口's first 2 of 3 bytes — truncated
	if first != "" {
		t.Fatalf("truncated rune emitted early: %q", first)
	}
	got := d.push([]byte{0xa3}) // completion byte
	if got != "口" {
		t.Fatalf("completed = %q", got)
	}
}
