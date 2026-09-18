package netdev

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev/transport"
)

// testManager wires a Manager against the simulator with an isolated audit
// file and a stubbed secret store.
func testManager(t *testing.T, sim *simDevice) (*Manager, string) {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(sim.addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	cfg := config.Default()
	cfg.NetDev = config.NetDevConfig{
		Enabled: true,
		Devices: []config.NetDevDevice{{
			Name: "sw1", Vendor: "huawei", OS: "vrp8",
			Address: host, Port: port, Username: "admin",
			PasswordEnv: "TEST_ENV",
		}, {
			// Points at a closed port: a refused (pre-connection) command must
			// never even try to dial it.
			Name: "dead", Vendor: "huawei", OS: "vrp8",
			Address: "127.0.0.1", Port: 1, Username: "admin",
			PasswordEnv: "TEST_ENV",
		}},
	}

	orig := secretGetter
	secretGetter = func(kind, name string) (string, bool, error) {
		if name == "TEST_ENV" {
			return sim.password, true, nil
		}
		return "", false, nil
	}
	t.Cleanup(func() { secretGetter = orig })

	// Isolate the managed known_hosts file: tests must never append to the
	// user's real state tree.
	origKnownHosts := transport.ManagedKnownHostsOverride
	transport.ManagedKnownHostsOverride = filepath.Join(t.TempDir(), "known_hosts")
	t.Cleanup(func() { transport.ManagedKnownHostsOverride = origKnownHosts })

	// TOFU auto-accept with an isolated managed file (strict mode is the
	// production default; tests exercise the prompt path).
	origPrompt := HostKeyPrompt
	HostKeyPrompt = func(ctx context.Context, q transport.HostKeyQuestion) (bool, error) { return true, nil }
	t.Cleanup(func() { HostKeyPrompt = origPrompt })

	proposalsDirOverride = filepath.Join(t.TempDir(), "proposals")
	t.Cleanup(func() { proposalsDirOverride = "" })
	findingsDirOverr = filepath.Join(t.TempDir(), "findings")
	t.Cleanup(func() { findingsDirOverr = "" })

	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	SetAuditPath(auditPath)
	t.Cleanup(func() { SetAuditPath("") })

	m := NewManager(cfg)
	t.Cleanup(m.Close)
	return m, auditPath
}

func readAudit(t *testing.T, path string) []Audit {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []Audit
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var e Audit
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad audit line: %v", err)
		}
		out = append(out, e)
	}
	return out
}

func TestExecReadCommand(t *testing.T) {
	sim := startSimDevice(t)
	m, auditPath := testManager(t, sim)

	res := m.Exec(context.Background(), "sw1", "display version")
	if res.Refused {
		t.Fatalf("read command refused: %s", res.Refusal)
	}
	if res.IsError || !strings.Contains(res.Output, "Version 8.180") {
		t.Fatalf("unexpected result: %+v", res)
	}

	entries := readAudit(t, auditPath)
	if len(entries) == 0 {
		t.Fatal("no audit entry written")
	}
	last := entries[len(entries)-1]
	if last.Device != "sw1" || last.Command != "display version" || last.Class != "read" || last.Status != AuditOK {
		t.Fatalf("audit entry = %+v", last)
	}
}

func TestExecRefusesWriteDangerousUnknown(t *testing.T) {
	sim := startSimDevice(t)
	m, auditPath := testManager(t, sim)

	cases := []struct {
		cmd, class string
	}{
		{"undo stp enable", "write"},
		{"system-view", "write"},
		{"reboot", "dangerous"},
		{"mystery-verb arg", "unknown"},
	}
	for _, c := range cases {
		res := m.Exec(context.Background(), "sw1", c.cmd)
		if !res.Refused {
			t.Errorf("%q not refused", c.cmd)
		}
		if res.Class != c.class {
			t.Errorf("%q class = %s, want %s", c.cmd, res.Class, c.class)
		}
		if res.Output != "" {
			t.Errorf("%q produced output despite refusal", c.cmd)
		}
	}
	// The refusals never connected (device "dead" has a closed port): a
	// refusal there must be the classifier message, not a dial error.
	res := m.Exec(context.Background(), "dead", "undo stp enable")
	if !res.Refused || !strings.Contains(res.Refusal, "structurally read-only") {
		t.Fatalf("refusal should precede dialing, got: %+v", res)
	}

	entries := readAudit(t, auditPath)
	if len(entries) != len(cases)+1 {
		t.Fatalf("audit entries = %d, want %d", len(entries), len(cases)+1)
	}
	for _, e := range entries {
		if e.Status != AuditRefused {
			t.Fatalf("audit status = %s, want refused (%+v)", e.Status, e)
		}
	}
}

func TestExecUnknownDeviceRefused(t *testing.T) {
	sim := startSimDevice(t)
	m, _ := testManager(t, sim)

	res := m.Exec(context.Background(), "not-in-inventory", "display version")
	if !res.Refused || !strings.Contains(res.Refusal, "not in the user-global netdev inventory") {
		t.Fatalf("unknown device not refused: %+v", res)
	}
}

// TestTopologyToolEndToEnd: exec → clean → parse → edges, through the
// simulated Huawei CLI.
func TestTopologyToolEndToEnd(t *testing.T) {
	sim := startSimDevice(t)
	m, _ := testManager(t, sim)
	res := m.Exec(context.Background(), "sw1", "display lldp neighbor")
	if res.Refused {
		t.Fatalf("neighbor query refused: %s", res.Refusal)
	}
	edges, err := parseNeighbors("huawei-vrp", res.Output)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 3 {
		t.Fatalf("edges = %d, want 3 (got %+v)", len(edges), edges)
	}
	if edges[0].RemoteDevice != "ACCESS-SW-2" || edges[0].RemotePort != "GigabitEthernet0/0/24" {
		t.Fatalf("edge[0] = %+v", edges[0])
	}
}

// TestExecSealAcrossSession: the seal holds on a live session too — after a
// successful read, a write on the same cached session is still refused, and
// the session stays usable afterwards.
func TestExecSealAcrossSession(t *testing.T) {
	sim := startSimDevice(t)
	m, _ := testManager(t, sim)

	if res := m.Exec(context.Background(), "sw1", "display version"); res.Refused {
		t.Fatalf("setup read refused: %s", res.Refusal)
	}
	if res := m.Exec(context.Background(), "sw1", "delete vrpcfg.zip"); !res.Refused {
		t.Fatal("dangerous command executed on live session")
	}
	// Session still usable: a read command runs (unknown to the sim, so the
	// device flags an error — but it is NOT refused, i.e. the session works).
	if res := m.Exec(context.Background(), "sw1", "display clock"); res.Refused {
		t.Fatalf("session unusable after refusal: %+v", res)
	}
}

func TestFindingRequiresEvidence(t *testing.T) {
	startSimDevice(t) // testManager needs a sim; findings isolated anyway
	m, _ := testManager(t, simForFindings(t))

	bare := &Finding{Title: "something wrong"}
	if err := SaveFinding(bare); err == nil || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("evidence-less finding accepted: %v", err)
	}

	f := &Finding{
		Title:    "OSPF neighbor 10.2.0.3 down: hello timer mismatch",
		Severity: SeverityWarning,
		Devices:  []string{"sw1"},
		Evidence: []Evidence{{Device: "sw1", Command: "display ospf error", Output: "hello timer mismatch on GE0/0/1"}},
	}
	if err := SaveFinding(f); err != nil {
		t.Fatalf("SaveFinding: %v", err)
	}
	if f.ID == "" {
		t.Fatal("no id")
	}

	list, err := ListFindings()
	if err != nil || len(list) != 1 || list[0].ID != f.ID {
		t.Fatalf("list = %v err = %v", list, err)
	}

	bad := &Finding{Title: "x", Severity: "fatal", Evidence: f.Evidence}
	if err := SaveFinding(bad); err == nil {
		t.Fatal("bad severity accepted")
	}
	_ = m
}

func simForFindings(t *testing.T) *simDevice {
	return startSimDevice(t)
}

func TestRunInspection(t *testing.T) {
	sim := startSimDevice(t)
	m, _ := testManager(t, sim)
	// The simulator answers version/interface-brief; the battery's other
	// commands fall to its unknown-command error, which must surface as
	// problems (severity warning) — evidence still collected.
	f, err := m.RunInspection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.Severity != SeverityWarning || len(f.Evidence) == 0 {
		t.Fatalf("finding = %+v", f)
	}
	list, _ := ListFindings()
	if len(list) == 0 {
		t.Fatal("inspection finding not persisted")
	}
}

func TestNetconfGet(t *testing.T) {
	sim := startSimDevice(t)
	m, _ := testManager(t, sim)

	reply, err := m.NetconfRPC(context.Background(), "sw1", "<get/>")
	if err != nil {
		t.Fatalf("NetconfRPC: %v", err)
	}
	if !strings.Contains(reply, "GE0/0/1") || !strings.Contains(reply, "</rpc-reply>") {
		t.Fatalf("reply = %.200s", reply)
	}

	// Write ops refused at the allowlist.
	if _, err := m.NetconfRPC(context.Background(), "sw1", "<edit-config><target><running/></target></edit-config>"); err == nil {
		t.Fatal("edit-config accepted")
	}
}
