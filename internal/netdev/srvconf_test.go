package netdev

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev/transport"
)

// srvConfTestManager: two linux devices (prod / stage groups) on one sim, both
// whitelisting /etc/nginx — the §7.3 world.
func srvConfTestManager(t *testing.T) *Manager {
	t.Helper()
	simFSMu.Lock()
	simFS = map[string][]byte{}
	simFSMu.Unlock()
	sim := startSimDevice(t)
	host, portStr, _ := net.SplitHostPort(sim.addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	cfg := config.Default()
	cfg.NetDev = config.NetDevConfig{
		Enabled: true,
		Devices: []config.NetDevDevice{
			{Name: "ng-prod", Vendor: "linux", Group: "prod", Address: host, Port: port, Username: "root", PasswordEnv: "TEST_ENV", ConfigPaths: []string{"/etc/nginx"}},
			{Name: "ng-stage", Vendor: "linux", Group: "stage", Address: host, Port: port, Username: "root", PasswordEnv: "TEST_ENV", ConfigPaths: []string{"/etc/nginx"}},
		},
		Groups: []config.NetDevGroup{{Name: "prod"}, {Name: "stage"}},
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
	srvConfDirOverride = t.TempDir()
	t.Cleanup(func() { srvConfDirOverride = "" })
	netdevStateDirOverr = t.TempDir()
	t.Cleanup(func() { netdevStateDirOverr = "" })
	SetAuditPath(filepath.Join(netdevStateDirOverr, "audit.jsonl"))
	t.Cleanup(func() { SetAuditPath("") })

	m := NewManager(cfg)
	t.Cleanup(m.Close)
	return m
}

// 抓快照 → 改现场 → 再拍 → 两版本 unified diff 可见变化。
func TestSrvConfSnapshotAndDiff(t *testing.T) {
	m := srvConfTestManager(t)
	simFSPut("/etc/nginx/nginx.conf", []byte("worker_processes 1;\nworker_connections 1024;\n"))

	v1, err := m.SrvConfSnapshot(context.Background(), "ng-prod", "/etc/nginx/nginx.conf")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	simFSPut("/etc/nginx/nginx.conf", []byte("worker_processes 2;\nworker_connections 1024;\n"))
	v2, err := m.SrvConfSnapshot(context.Background(), "ng-prod", "/etc/nginx/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	diff, err := SrvConfDiff(v1.ID, v2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "worker_processes") {
		t.Fatalf("diff misses the change:\n%s", diff)
	}
	vers := SrvConfVersions("ng-prod", "/etc/nginx/nginx.conf")
	if len(vers) != 2 || vers[0].ID != v2.ID {
		t.Fatalf("versions = %+v", vers)
	}

	// 白名单外的路径拒绝。
	if _, err := m.SrvConfSnapshot(context.Background(), "ng-prod", "/etc/shadow"); err == nil {
		t.Fatal("path outside config_paths accepted")
	}
}

// 环境 Drift 视图：同一后端读出 same；路径缺失的判读。内容分叉的 diff 由
// TestSrvConfSnapshotAndDiff 的 diff 组件覆盖（sim 是共享文件系统后端，两
// 台设备无法在同一 map 上读出不同内容）。
func TestSrvConfDrift(t *testing.T) {
	m := srvConfTestManager(t)
	simFSPut("/etc/nginx/nginx.conf", []byte("keepalive_timeout 65;\n"))
	rows, err := m.SrvConfDrift(context.Background(), "/etc/nginx/nginx.conf", []string{"ng-prod", "ng-stage"})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Status != "same" || rows[1].Status != "same" {
		t.Fatalf("identical files drifted: %+v", rows)
	}

	simFSDel("/etc/nginx/nginx.conf")
	rows, err = m.SrvConfDrift(context.Background(), "/etc/nginx/nginx.conf", []string{"ng-prod", "ng-stage"})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Status != "absent" || rows[1].Status != "absent" {
		t.Fatalf("absent files misread: %+v", rows)
	}
}

// 备份恢复演练全链路：prod 快照 → restore-verify 提案（接收方 stage）→ 批准
// 执行 → stage 拿到 prod 的内容 → 回滚恢复 stage 原文件。
func TestRestoreVerifyProposalE2E(t *testing.T) {
	m := srvConfTestManager(t)
	simFSPut("/etc/nginx/nginx.conf", []byte("# prod golden\nworker_processes 4;\n"))
	snap, err := m.SrvConfSnapshot(context.Background(), "ng-prod", "/etc/nginx/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	simFSPut("/etc/nginx/nginx.conf", []byte("# stage scratch\nworker_processes 1;\n"))

	p := &Proposal{Intent: "演练：prod 配置恢复到 stage 并验证", Steps: []ProposalStep{{
		Device: "ng-stage", Type: StepRestoreVerify,
		RestoreDevice: "ng-prod", RestoreVersion: snap.ID,
		RemotePath: "/etc/nginx/nginx.conf",
		VerifyCmd:  "nginx -t",
	}}}
	if err := m.ValidateProposal(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	SaveProposal(p)
	if _, err := m.ApproveProposal(p.ID, false); err != nil {
		t.Fatal(err)
	}
	got, err := m.ExecuteProposal(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ProposalDone || !got.Steps[0].Applied {
		t.Fatalf("status=%s step=%+v", got.Status, got.Steps[0])
	}
	cur, _ := simFSGet("/etc/nginx/nginx.conf")
	if !strings.Contains(string(cur), "prod golden") {
		t.Fatalf("stage content = %q — restore did not land", cur)
	}

	// 回滚恢复 stage 的原文件。
	if _, err := m.RollbackProposal(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ = simFSGet("/etc/nginx/nginx.conf")
	if !strings.Contains(string(cur), "stage scratch") {
		t.Fatalf("rollback content = %q", cur)
	}
}

// 演练安全门：同组（生产）接收方拒绝；缺验证命令拒绝；白名单外路径拒绝。
func TestRestoreVerifyGuards(t *testing.T) {
	m := srvConfTestManager(t)
	base := ProposalStep{Device: "ng-stage", Type: StepRestoreVerify, RestoreDevice: "ng-prod", RemotePath: "/etc/nginx/nginx.conf", VerifyCmd: "nginx -t"}

	sameGroup := base
	sameGroup.Device = "ng-prod"
	if err := m.validateStep(&sameGroup, devByName(m, "ng-prod")); err == nil || !strings.Contains(err.Error(), "演练接收方") {
		t.Fatalf("same-group receiver accepted: %v", err)
	}

	noVerify := base
	noVerify.VerifyCmd = ""
	if err := m.validateStep(&noVerify, devByName(m, "ng-stage")); err == nil || !strings.Contains(err.Error(), "verify") {
		t.Fatalf("verify-less drill accepted: %v", err)
	}

	outside := base
	outside.RemotePath = "/etc/passwd"
	if err := m.validateStep(&outside, devByName(m, "ng-stage")); err == nil || !strings.Contains(err.Error(), "config_paths") {
		t.Fatalf("outside-whitelist path accepted: %v", err)
	}

	badVersion := base
	badVersion.RestoreVersion = "sc@ghost@abc@1"
	if err := m.validateStep(&badVersion, devByName(m, "ng-stage")); err == nil {
		t.Fatal("missing snapshot version accepted")
	}
}

func devByName(m *Manager, name string) config.NetDevDevice {
	d, _ := m.cfg.NetDevDeviceByName(name)
	return d
}
