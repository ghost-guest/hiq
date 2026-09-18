package netdev

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev/transport"
)

// ── kubeResourcePath ─────────────────────────────────────────────────────────

func TestKubeResourcePath(t *testing.T) {
	deploy := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: prod\n"
	ref, err := kubeResourcePath(deploy, "default")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Path != "/apis/apps/v1/namespaces/prod/deployments/web" {
		t.Fatalf("path = %s", ref.Path)
	}
	if ref.Cluster {
		t.Fatal("deployment is namespaced")
	}

	pod := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: pi\n"
	ref, err = kubeResourcePath(pod, "ops")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Path != "/api/v1/namespaces/ops/pods/pi" {
		t.Fatalf("path = %s", ref.Path)
	}

	ns := "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: qa\n"
	ref, err = kubeResourcePath(ns, "default")
	if err != nil {
		t.Fatal(err)
	}
	if !ref.Cluster || ref.Path != "/api/v1/namespaces/qa" {
		t.Fatalf("cluster-scoped path = %s (cluster=%v)", ref.Path, ref.Cluster)
	}

	for _, bad := range []string{
		"apiVersion: v1\nkind: Secret\nmetadata:\n  name: tls\n",         // secrets refused
		"apiVersion: apps/v1\nkind: Frobnicator\nmetadata:\n  name: x\n", // unknown kind
		"apiVersion: v1\nkind: Pod\n",                                    // no name
		"kind: Pod\nmetadata:\n  name: x\n",                              // no apiVersion
		"apiVersion: v1\nkind: Pod\nmetadata:\n  name: 'a b'\n",          // invalid name
	} {
		if _, err := kubeResourcePath(bad, "default"); err == nil {
			t.Fatalf("accepted bad manifest: %.60q", bad)
		}
	}
}

// ── validation contracts ─────────────────────────────────────────────────────

func structuredManager(t *testing.T) *Manager {
	t.Helper()
	m, _ := guardrailManager(t, config.NetDevGuardrails{})
	jobsDirOverride = t.TempDir()
	t.Cleanup(func() { jobsDirOverride = "" })
	// A db source for sql-migration validation.
	m.cfg.NetDev.DBSources = []config.NetDevDBSource{{Name: "mysql-prod", Type: "mysql", Host: "10.1.0.5", Username: "root", PasswordEnv: "DB_PW"}}
	// A linux host for upload validation (points at the unreachable "dead"
	// address — validation never dials).
	m.cfg.NetDev.Devices = append(m.cfg.NetDev.Devices, config.NetDevDevice{
		Name: "host1", Vendor: "linux", Address: "127.0.0.1", Port: 1, Username: "root", PasswordEnv: "TEST_ENV",
	})
	return m
}

func TestStructuredStepValidation(t *testing.T) {
	m := structuredManager(t)
	local := filepath.Join(t.TempDir(), "app.conf")
	os.WriteFile(local, []byte("listen 8080\n"), 0o600)

	cases := []struct {
		name string
		step ProposalStep
		want string
	}{
		{"sql no down", ProposalStep{Device: "mysql-prod", Type: StepSQLMigration, UpSQL: "CREATE TABLE t (id INT)"}, "down script"},
		{"sql no source", ProposalStep{Device: "ghost-db", Type: StepSQLMigration, UpSQL: "x", DownSQL: "y"}, "db_sources"},
		{"upload missing local", ProposalStep{Device: "host1", Type: StepFileUpload, LocalPath: "/nope.conf", RemotePath: "/etc/nope.conf"}, "local file"},
		{"upload bad remote", ProposalStep{Device: "host1", Type: StepFileUpload, LocalPath: local, RemotePath: "etc/nope"}, "absolute"},
		// 注入（发布门禁 #2）：引号/元字符走私上传路径。
		{"upload quoted path", ProposalStep{Device: "host1", Type: StepFileUpload, LocalPath: local, RemotePath: "/etc/x'; rm -rf /"}, "quote-safe"},
		{"upload metachar path", ProposalStep{Device: "host1", Type: StepFileUpload, LocalPath: local, RemotePath: "/etc/$(reboot)"}, "quote-safe"},
		{"upload non-linux", ProposalStep{Device: "sw1", Type: StepFileUpload, LocalPath: local, RemotePath: "/etc/x"}, "linux SSH target"},
		{"cert no reload", ProposalStep{Device: "host1", Type: StepCertReplace, LocalPath: local, RemotePath: "/etc/tls/c.pem", KeyLocalPath: local, KeyRemotePath: "/etc/tls/k.pem"}, "reload command"},
		{"k8s on wrong target", ProposalStep{Device: "sw1", Type: StepK8sApply, YAML: "apiVersion: v1\nkind: Pod\nmetadata:\n  name: x\n"}, "kind=k8s"},
		{"unknown type", ProposalStep{Device: "sw1", Type: "ftp-mput"}, "unknown type"},
	}
	for _, c := range cases {
		p := &Proposal{Intent: "test", Steps: []ProposalStep{c.step}}
		err := m.ValidateProposal(p)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}

	// The full valid set passes, and old-style cli steps still validate.
	okLocal := ProposalStep{Device: "host1", Type: StepFileUpload, LocalPath: local, RemotePath: "/etc/app/app.conf", Checksum: ""}
	if err := m.validateStep(&okLocal, mustDev(m, "host1")); err != nil {
		t.Fatalf("valid upload rejected: %v", err)
	}
}

func mustDev(m *Manager, name string) config.NetDevDevice {
	d, _ := m.cfg.NetDevDeviceByName(name)
	return d
}

// Destructive verbs in the CHANGE force the secondary confirmation regardless
// of group policy (§7.1); rollback plans are exempt (recovery contract).
func TestDangerScanForcesConfirm2(t *testing.T) {
	m := structuredManager(t)
	p := &Proposal{Intent: "cleanup", Steps: []ProposalStep{{
		Device: "sw1", Commands: []string{"undo stp"}, Rollback: []string{"stp enable"},
	}}}
	if !dangerScan(&p.Steps[0]) {
		t.Fatal("undo not flagged dangerous")
	}
	if err := m.ValidateProposal(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !m.ProposalNeedsConfirm2(p) {
		t.Fatal("dangerous step does not demand confirm2")
	}

	benign := &Proposal{Intent: "x", Steps: []ProposalStep{{
		Device: "sw1", Commands: []string{"display version"}, Rollback: []string{"display clock"},
	}}}
	if dangerScan(&benign.Steps[0]) {
		t.Fatal("read commands flagged dangerous")
	}
	if m.ProposalNeedsConfirm2(benign) {
		t.Fatal("confirm2 demanded without danger or policy")
	}
}

// ── e2e: file-upload over the sim's exec channel ─────────────────────────────

func uploadManager(t *testing.T) *Manager {
	t.Helper()
	simFSMu.Lock()
	simFS = map[string][]byte{} // package-global; reset per test
	simFSMu.Unlock()
	sim := startSimDevice(t)
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
	jobsDirOverride = t.TempDir()
	t.Cleanup(func() { jobsDirOverride = "" })
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	SetAuditPath(auditPath)
	t.Cleanup(func() { SetAuditPath("") })

	m := NewManager(cfg)
	t.Cleanup(m.Close)
	return m
}

// The full §6.2 upload flow rides a real SSH connection: backup (absent) →
// base64 upload → sha256 verify; rollback removes what was not there before.
func TestFileUploadProposalE2E(t *testing.T) {
	m := uploadManager(t)
	local := filepath.Join(t.TempDir(), "app.conf")
	content := []byte("# v2 config\nlisten 8080\nmax_conn 512\n")
	if err := os.WriteFile(local, content, 0o600); err != nil {
		t.Fatal(err)
	}

	p := &Proposal{Intent: "roll out app.conf v2", Steps: []ProposalStep{{
		Device: "host1", Type: StepFileUpload, LocalPath: local, RemotePath: "/etc/app/app.conf",
	}}}
	if err := m.ValidateProposal(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := SaveProposal(p); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApproveProposal(p.ID, false); err != nil {
		t.Fatalf("approve: %v", err)
	}
	got, err := m.ExecuteProposal(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ProposalDone || !got.Steps[0].Applied {
		t.Fatalf("status=%s step=%+v", got.Status, got.Steps[0])
	}
	remote, ok := simFSGet("/etc/app/app.conf")
	if !ok || string(remote) != string(content) {
		t.Fatalf("remote content = %q (ok=%v)", remote, ok)
	}
	if got.Steps[0].Backup != absentMarker {
		t.Fatalf("backup = %q, want absent marker", got.Steps[0].Backup)
	}

	// Rollback removes the uploaded file (it did not exist before).
	if _, err := m.RollbackProposal(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := simFSGet("/etc/app/app.conf"); ok {
		t.Fatal("rollback left the uploaded file behind")
	}
}

// Overwriting an EXISTING remote file captures its bytes as the backup, and
// rollback restores them.
func TestFileUploadOverwriteAndRestoreE2E(t *testing.T) {
	m := uploadManager(t)
	simFSPut("/etc/app/app.conf", []byte("old contents"))
	local := filepath.Join(t.TempDir(), "app.conf")
	os.WriteFile(local, []byte("new contents"), 0o600)

	p := &Proposal{Intent: "swap", Steps: []ProposalStep{{
		Device: "host1", Type: StepFileUpload, LocalPath: local, RemotePath: "/etc/app/app.conf",
	}}}
	SaveProposal(p)
	m.ApproveProposal(p.ID, false)
	got, err := m.ExecuteProposal(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps[0].Backup != "old contents" {
		t.Fatalf("backup = %q", got.Steps[0].Backup)
	}
	if _, err := m.RollbackProposal(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	remote, _ := simFSGet("/etc/app/app.conf")
	if string(remote) != "old contents" {
		t.Fatalf("restored = %q", remote)
	}
}

// A checksum mismatch is caught at EXECUTE time before any upload leaves the
// machine (validation only checks structure; content may change in between).
func TestFileUploadChecksumGuard(t *testing.T) {
	m := uploadManager(t)
	local := filepath.Join(t.TempDir(), "app.conf")
	os.WriteFile(local, []byte("payload"), 0o600)
	p := &Proposal{Intent: "x", Steps: []ProposalStep{{
		Device: "host1", Type: StepFileUpload, LocalPath: local, RemotePath: "/etc/app/app.conf", Checksum: strings.Repeat("0", 64),
	}}}
	if err := m.ValidateProposal(p); err != nil {
		t.Fatalf("validate (structural only): %v", err)
	}
	SaveProposal(p)
	m.ApproveProposal(p.ID, false)
	got, _ := m.ExecuteProposal(context.Background(), p.ID)
	if got.Status != ProposalPartial || !strings.Contains(got.Steps[0].Error, "checksum") {
		t.Fatalf("status=%s err=%q", got.Status, got.Steps[0].Error)
	}
	if _, ok := simFSGet("/etc/app/app.conf"); ok {
		t.Fatal("mismatched checksum still uploaded the file")
	}
}
