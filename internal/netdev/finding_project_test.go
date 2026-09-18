package netdev

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

// projectTestCfg: 项目甲 = 核心+出口, 项目乙 = 存储. sw1/sw2 ∈ 核心, db1 ∈ 存储,
// nas1 无组（未分组桶）.
func projectTestCfg() *config.Config {
	cfg := &config.Config{}
	cfg.NetDev.Devices = []config.NetDevDevice{
		{Name: "sw1", Group: "核心"},
		{Name: "sw2", Group: "核心"},
		{Name: "db1", Group: "存储"},
		{Name: "nas1"},
	}
	cfg.NetDev.Projects = []config.NetDevProject{
		{Name: "项目甲", Groups: []string{"核心", "出口"}},
		{Name: "项目乙", Groups: []string{"存储"}},
	}
	return cfg
}

func TestProjectForDevices(t *testing.T) {
	cfg := projectTestCfg()
	cases := []struct {
		name string
		devs []string
		want string
	}{
		{"majority wins", []string{"sw1", "sw2", "db1"}, "项目甲"},
		{"single device", []string{"db1"}, "项目乙"},
		{"unknown device matches nothing", []string{"ghost"}, ""},
		{"pseudo (all) matches nothing", []string{"(all)"}, ""},
		{"pseudo (unknown) matches nothing", []string{"(unknown)"}, ""},
		{"empty list", nil, ""},
		{"ungrouped matches only a 未分组 project", []string{"nas1"}, ""},
	}
	for _, c := range cases {
		if got := ProjectForDevices(cfg, c.devs); got != c.want {
			t.Errorf("%s: ProjectForDevices(%v) = %q, want %q", c.name, c.devs, got, c.want)
		}
	}

	// 未分组 project does claim ungrouped devices (mirrors the frontend's
	// inScope bucket), and ties keep config order.
	cfg.NetDev.Projects = append(cfg.NetDev.Projects,
		config.NetDevProject{Name: "项目丙", Groups: []string{"未分组"}})
	if got := ProjectForDevices(cfg, []string{"nas1"}); got != "项目丙" {
		t.Errorf("ungrouped bucket: got %q, want 项目丙", got)
	}
	if ProjectForDevices(nil, []string{"sw1"}) != "" {
		t.Errorf("nil cfg must map to \"\"")
	}
	empty := &config.Config{}
	if ProjectForDevices(empty, []string{"sw1"}) != "" {
		t.Errorf("no projects configured must map to \"\"")
	}
}

// SaveFinding stamps the snapshot; a re-save with the field already set
// keeps the original stamp (audit history, not live membership).
func TestSaveFindingStampsProject(t *testing.T) {
	dir := t.TempDir()
	oldDir, oldLookup := findingsDirOverr, loadConfigForProject
	defer func() { findingsDirOverr, loadConfigForProject = oldDir, oldLookup }()
	findingsDirOverr = dir
	loadConfigForProject = projectTestCfg

	f := &Finding{Title: "t", Severity: SeverityWarning, Devices: []string{"sw1", "db1"},
		Evidence: []Evidence{{Device: "sw1", Command: "x", Output: "y"}}}
	if err := SaveFinding(f); err != nil {
		t.Fatalf("save: %v", err)
	}
	if f.Project != "项目甲" {
		t.Fatalf("stamped project = %q, want 项目甲", f.Project)
	}

	// Re-save after the device moved to another project's group: the stamp
	// must survive (only empty stamps are computed).
	cfg2 := projectTestCfg()
	cfg2.NetDev.Devices[2].Group = "核心" // db1 → 核心
	loadConfigForProject = func() *config.Config { return cfg2 }
	if err := SaveFinding(f); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if f.Project != "项目甲" {
		t.Fatalf("re-save rewrote the stamp: %q", f.Project)
	}
}

// Legacy files without a project field are backfilled in-memory on list —
// the file itself must stay untouched (a read never rewrites history).
func TestListFindingsBackfillsLegacyProject(t *testing.T) {
	dir := t.TempDir()
	oldDir, oldLookup := findingsDirOverr, loadConfigForProject
	defer func() { findingsDirOverr, loadConfigForProject = oldDir, oldLookup }()
	findingsDirOverr = dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	legacy := map[string]any{
		"id": "F20260901-1", "title": "legacy", "severity": "info",
		"devices": []string{"db1"},
		"evidence": []map[string]string{{"device": "db1", "command": "c", "output": "o"}},
		"created_at": time.Now().Format(time.RFC3339),
	}
	b, _ := json.Marshal(legacy)
	path := filepath.Join(dir, "F20260901-1.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	loadConfigForProject = projectTestCfg
	fs, err := ListFindings()
	if err != nil || len(fs) != 1 {
		t.Fatalf("list: %v n=%d", err, len(fs))
	}
	if fs[0].Project != "项目乙" {
		t.Fatalf("backfill = %q, want 项目乙", fs[0].Project)
	}
	ondisk, _ := os.ReadFile(path)
	var raw map[string]any
	if json.Unmarshal(ondisk, &raw) != nil {
		t.Fatal("legacy file must stay valid JSON")
	}
	if _, ok := raw["project"]; ok {
		t.Fatal("list must not rewrite the legacy file with a project field")
	}
}
