package netdev

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev/transport"
)

func onlineCheckManager(t *testing.T) *Manager {
	t.Helper()
	sim := startSimDeviceWithPrompt(t, "root@sim:~# ")
	host, portStr, _ := net.SplitHostPort(sim.addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	cfg := config.Default()
	cfg.NetDev = config.NetDevConfig{
		Enabled: true,
		Devices: []config.NetDevDevice{{
			Name: "host1", Vendor: "linux", Address: host, Port: port, Username: "root", PasswordEnv: "TEST_ENV",
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
	findingsDirOverr = filepath.Join(t.TempDir(), "findings")
	t.Cleanup(func() { findingsDirOverr = "" })
	netdevStateDirOverr = t.TempDir()
	t.Cleanup(func() { netdevStateDirOverr = "" })
	SetAuditPath(filepath.Join(netdevStateDirOverr, "audit.jsonl"))
	t.Cleanup(func() { SetAuditPath("") })

	m := NewManager(cfg)
	t.Cleanup(m.Close)
	return m
}

// §7.1 执行前在线检查：发现其他在线人员 → 暂停（spec：暂停并列出会话，
// 人确认后才继续）——第一次执行被拦下并记录，第二次执行视为确认放行。
// 状态保持在 approved，不产生半执行。
func TestPreExecOnlineCheckPausesForConfirmation(t *testing.T) {
	m := onlineCheckManager(t)
	p := &Proposal{Intent: "reload", Steps: []ProposalStep{{
		Device: "host1", Commands: []string{"systemctl reload nginx"}, Rollback: []string{"systemctl reload nginx"},
	}}}
	if err := m.ValidateProposal(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	SaveProposal(p)
	if _, err := m.ApproveProposal(p.ID, false); err != nil {
		t.Fatal(err)
	}

	// 第一次执行：设备上有人（sim 的 who 返回一个会话）→ 拦下。
	got, err := m.ExecuteProposal(context.Background(), p.ID)
	if err == nil || !strings.Contains(err.Error(), "在线") {
		t.Fatalf("first execute not paused: err = %v", err)
	}
	if got.Status != ProposalApproved {
		t.Fatalf("status = %s — pause must leave the proposal approved, not half-run", got.Status)
	}
	if !strings.Contains(got.Note, "[在线人员]") {
		t.Fatalf("note = %q — the session list must be recorded", got.Note)
	}
	if got.ExecutedAt != (time.Time{}) {
		t.Fatal("paused proposal must not carry ExecutedAt")
	}

	// 第二次执行：同样的会话清单已在 Note → 视为已确认，进入执行。
	got, err = m.ExecuteProposal(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("confirmed execute: %v", err)
	}
	if got.Status == ProposalApproved {
		t.Fatal("second execute still paused — confirmation not honored")
	}
}

// 网络设备（无 who 语义）不受在线检查拦截——现状回归保护。
func TestPreExecOnlineCheckSkipsNetworkCLIs(t *testing.T) {
	m, _ := guardrailManager(t, config.NetDevGuardrails{})
	p := &Proposal{Intent: "vlan", Steps: []ProposalStep{{
		Device: "sw1", Commands: []string{"vlan 100"}, Rollback: []string{"undo vlan 100"},
	}}}
	m.ValidateProposal(p)
	SaveProposal(p)
	m.ApproveProposal(p.ID, false)
	got, err := m.ExecuteProposal(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Status != ProposalDone {
		t.Fatalf("network proposal paused unexpectedly: %s (note %q)", got.Status, got.Note)
	}
}

// §7.1 观察期劣化检测：watching 提案目标从「可达 0 down」变为「2 口 down」
// → 最高级 Finding（带回滚提示）+ WatchNote 只告警一次。
func TestWatchDegradationRaisesFinding(t *testing.T) {
	m := onlineCheckManager(t)
	p := &Proposal{
		ID: "P-test-1", Intent: "core change", Status: ProposalWatching,
		Steps:      []ProposalStep{{Device: "host1", Commands: []string{"x"}, Rollback: []string{"y"}}},
		HealthBase: map[string]int{"host1": 0},
	}
	SaveProposal(p)

	fresh := map[string]DeviceHealth{"host1": {Device: "host1", Reachable: true, Interfaces: []IfHealth{
		{AdminUp: true, OperUp: false}, {AdminUp: true, OperUp: false},
	}}}
	m.checkWatchingProposals(fresh)

	got, _ := GetProposal(p.ID)
	if !strings.Contains(got.WatchNote, "劣化") || !strings.Contains(got.WatchNote, "2") {
		t.Fatalf("watch note = %q", got.WatchNote)
	}
	findings, _ := ListFindings()
	if len(findings) == 0 {
		t.Fatal("no finding raised")
	}
	f := findings[0]
	if f.Severity != SeverityCritical || !strings.Contains(f.Suggestion, "回滚") || f.Source != "watch:"+p.ID {
		t.Fatalf("finding = %+v", f)
	}

	// 同一劣化不重复告警。
	m.checkWatchingProposals(fresh)
	findings2, _ := ListFindings()
	if len(findings2) != len(findings) {
		t.Fatalf("degradation re-alerted: %d → %d", len(findings), len(findings2))
	}

	// 健康恢复（0 down）不再新增告警；基线即不可达（-1）的设备不因
	// 「仍不可达」告警。
	m.checkWatchingProposals(map[string]DeviceHealth{"host1": {Device: "host1", Reachable: true}})
	findings3, _ := ListFindings()
	if len(findings3) != len(findings) {
		t.Fatalf("recovery re-alerted: %d → %d", len(findings), len(findings3))
	}

	// 基线不可达 → 仍不可达：不是「劣化」。
	pDown := &Proposal{
		ID: "P-test-2", Intent: "was down", Status: ProposalWatching,
		Steps:      []ProposalStep{{Device: "host1", Commands: []string{"x"}, Rollback: []string{"y"}}},
		HealthBase: map[string]int{"host1": -1},
	}
	SaveProposal(pDown)
	m.checkWatchingProposals(map[string]DeviceHealth{"host1": {Device: "host1", Reachable: false}})
	note, _ := GetProposal(pDown.ID)
	if strings.Contains(note.WatchNote, "劣化") {
		t.Fatalf("already-down baseline flagged as degradation: %q", note.WatchNote)
	}
}
