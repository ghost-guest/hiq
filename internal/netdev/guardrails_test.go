package netdev

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev/transport"
	"github.com/zzycxz/hiq/internal/permission"
)

// guardrailManager is testManager plus a group assignment and guardrails, so
// the per-ask controls can be exercised against the same in-process sim.
func guardrailManager(t *testing.T, g config.NetDevGuardrails) (*Manager, string) {
	t.Helper()
	sim := startSimDevice(t)
	host, portStr, _ := net.SplitHostPort(sim.addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	cfg := config.Default()
	cfg.NetDev = config.NetDevConfig{
		Enabled: true,
		Devices: []config.NetDevDevice{{
			Name: "sw1", Vendor: "huawei", OS: "vrp8",
			Address: host, Port: port, Username: "admin",
			PasswordEnv: "TEST_ENV", Group: "edge",
		}, {
			Name: "dead", Vendor: "huawei", OS: "vrp8",
			Address: "127.0.0.1", Port: 1, Username: "admin",
			PasswordEnv: "TEST_ENV", Group: "core",
		}},
		Groups:     []config.NetDevGroup{{Name: "edge"}, {Name: "core"}},
		Guardrails: g,
	}

	orig := secretGetter
	secretGetter = func(kind, name string) (string, bool, error) {
		if name == "TEST_ENV" {
			return sim.password, true, nil
		}
		return "", false, nil
	}
	t.Cleanup(func() { secretGetter = orig })

	origKnownHosts := transport.ManagedKnownHostsOverride
	transport.ManagedKnownHostsOverride = filepath.Join(t.TempDir(), "known_hosts")
	t.Cleanup(func() { transport.ManagedKnownHostsOverride = origKnownHosts })

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

// The group scope refuses BEFORE any driver or socket work: an out-of-scope
// device pointed at a live simulator is never dialed, and the audit trail
// records the guardrail refusal.
func TestGuardrailAllowedGroups(t *testing.T) {
	m, auditPath := guardrailManager(t, config.NetDevGuardrails{AllowedGroups: []string{"core"}})

	res := m.Exec(context.Background(), "sw1", "display version")
	if !res.Refused {
		t.Fatal("out-of-group device was not refused")
	}
	if res.Class != "guardrail" || !strings.Contains(res.Refusal, "allowed device groups") {
		t.Fatalf("refusal = %+v", res)
	}
	entries := readAudit(t, auditPath)
	if len(entries) != 1 || entries[0].Class != "guardrail" || entries[0].Status != AuditRefused {
		t.Fatalf("audit = %+v", entries)
	}

	// netdev_devices scopes the model's world too: sw1 (edge) disappears.
	dt := &devicesTool{cfg: m.cfg}
	out, err := dt.Execute(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "sw1") {
		t.Fatalf("out-of-group device still visible to the model: %s", out)
	}
	if !strings.Contains(out, "dead") {
		t.Fatalf("in-group device missing: %s", out)
	}
}

// The per-turn budget caps read commands; TurnBegin (the frontend's
// on-submit hook) buys a fresh budget.
func TestGuardrailTurnCommandBudget(t *testing.T) {
	m, _ := guardrailManager(t, config.NetDevGuardrails{TurnCommandBudget: 2})

	for i := 0; i < 2; i++ {
		if res := m.Exec(context.Background(), "sw1", "display version"); res.Refused {
			t.Fatalf("command %d inside budget refused: %s", i+1, res.Refusal)
		}
	}
	res := m.Exec(context.Background(), "sw1", "display version")
	if !res.Refused || !strings.Contains(res.Refusal, "budget exhausted") {
		t.Fatalf("over-budget command not refused: %+v", res)
	}
	m.TurnBegin()
	if res := m.Exec(context.Background(), "sw1", "display version"); res.Refused {
		t.Fatalf("command after TurnBegin refused: %s", res.Refusal)
	}
}

// RedactCounted reports how many substitutions happened — the transparency
// reminder's source of truth.
func TestRedactCounted(t *testing.T) {
	in := "snmp-server community public RO\nkey-string 7 s3cr3t\nhostname r1\n"
	out, n := RedactCounted(in)
	if n != 2 {
		t.Fatalf("count = %d, want 2 (out=%q)", n, out)
	}
	if got := strings.Count(out, "<redacted>"); got != 2 {
		t.Fatalf("masked occurrences = %d, want 2", got)
	}
	if !strings.Contains(out, "hostname r1") {
		t.Fatal("non-secret line mangled")
	}
	if Redact(in) != out {
		t.Fatal("Redact and RedactCounted disagree")
	}
}

// The Ask rule injected by boot for confirm_each_command must outrank BOTH
// the readOnly fallback and YOLO/full-access mode — that ordering is the whole
// guarantee ("even in full-access mode, every network command asks first").
func TestConfirmAskRuleBeatsReadOnlyAndYolo(t *testing.T) {
	policy := permission.New("allow", nil, nil, nil)
	policy.Mode = permission.Allow // YOLO posture
	policy.Ask = append(policy.Ask, permission.Rule{Tool: "netdev_exec"})

	if got := policy.DecideSubject("netdev_exec", true, ""); got != permission.Ask {
		t.Fatalf("readOnly netdev_exec decision = %v, want Ask", got)
	}
	if got := policy.DecideSubject("bash", true, ""); got != permission.Allow {
		t.Fatalf("unrelated readOnly decision = %v, want Allow", got)
	}
}
