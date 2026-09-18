package netdev

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// Golden drift: set a baseline from a stored backup, swap the running config
// behind the Manager's back (write a newer backup directly), and check that
// the finding fires with the right diff — then clean the drift and check the
// auto-resolve.
func TestGoldenDriftLifecycle(t *testing.T) {
	dir := t.TempDir()
	SetBackupsDir(filepath.Join(dir, "backups"))
	goldenDirOverr = filepath.Join(dir, "golden")
	old := findingsDirOverr
	findingsDirOverr = dir
	defer func() {
		SetBackupsDir("")
		goldenDirOverr = ""
		findingsDirOverr = old
	}()

	// Seed one backup as the baseline candidate.
	v1, err := saveBackup("sw1", "vlan 10\nvlan 20\nstp enable\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetGoldenFromBackup("sw1", v1.ID); err != nil {
		t.Fatal(err)
	}
	gi := GoldenInfoOf("sw1")
	if !gi.Set || gi.Lines != 3 {
		t.Fatalf("golden info: %+v", gi)
	}

	cfg := &config.Config{}
	cfg.NetDev.Enabled = true
	cfg.NetDev.Devices = []config.NetDevDevice{{Name: "sw1", Vendor: "huawei", OS: "vrp8", Address: "10.0.0.1"}}
	m := NewManager(cfg)

	// Simulate drift: write a NEWER backup (the check reads the latest) with
	// an unexpected line and a missing one.
	if _, err := saveBackup("sw1", "vlan 10\nstp enable\nacl 3000\n"); err != nil {
		t.Fatal(err)
	}

	drifts, err := m.RunGoldenCheck(context.Background(), "sw1")
	if err != nil {
		t.Fatal(err)
	}
	if len(drifts) != 1 {
		t.Fatalf("expected 1 drift row, got %d", len(drifts))
	}
	d := drifts[0]
	if len(d.Extra) != 1 || d.Extra[0] != "acl 3000" {
		t.Errorf("extra = %v, want [acl 3000]", d.Extra)
	}
	if len(d.Missing) != 1 || d.Missing[0] != "vlan 20" {
		t.Errorf("missing = %v, want [vlan 20]", d.Missing)
	}
	fs, _ := ListFindings()
	if len(fs) != 1 || fs[0].Source != "golden:sw1" || fs[0].Status != "active" {
		t.Fatalf("expected one active golden finding, got %+v", fs)
	}
	if !strings.Contains(fs[0].Evidence[0].Output, "acl 3000") {
		t.Errorf("finding evidence lacks the extra line: %+v", fs[0].Evidence)
	}

	// Clean the drift: newest backup matches the baseline again.
	if _, err := saveBackup("sw1", "vlan 10\nvlan 20\nstp enable\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RunGoldenCheck(context.Background(), "sw1"); err != nil {
		t.Fatal(err)
	}
	fs, _ = ListFindings()
	if len(fs) != 1 || fs[0].Status != "resolved" {
		t.Fatalf("drift clean should auto-resolve, got %+v", fs)
	}
}

// Pure diff sanity: comments/blank lines never count as drift; trailing
// whitespace is normalized.
func TestGoldenDiffIgnoresNoise(t *testing.T) {
	d := diffGolden("# comment\n\nvlan 10\n", "\nvlan 10   \n")
	if len(d.Extra) != 0 || len(d.Missing) != 0 {
		t.Errorf("noise counted as drift: %+v", d)
	}
}
