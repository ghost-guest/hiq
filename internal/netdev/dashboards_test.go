package netdev

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

// dashboards_test.go — 四屏组装函数表驱动单测（DASHBOARD spec §9.10）。
// 全部走临时状态目录，不碰真实 netdev state。

type dashEnv struct {
	dir string
}

func dashTestEnv(t *testing.T) *dashEnv {
	t.Helper()
	dir := t.TempDir()
	env := &dashEnv{dir: dir}
	prevJournal, prevFindings, prevDisc, prevAudit := journalDirOverr, findingsDirOverr, discoveredDirOverr, auditPath
	prevProp, prevCut, prevJob, prevCase, prevRun, prevDesign := proposalsDirOverride, cutoversDirOverride, jobsDirOverride, casesDirOverr, discoveryRunOver, designFileOverr
	journalDirOverr = dir
	findingsDirOverr = dir
	discoveredDirOverr = dir
	auditPath = filepath.Join(dir, "audit.jsonl")
	proposalsDirOverride = dir
	cutoversDirOverride = dir
	jobsDirOverride = dir
	casesDirOverr = dir
	discoveryRunOver = filepath.Join(dir, "discovery-run.json")
	designFileOverr = filepath.Join(dir, "topology-design.json")
	auditStatsCache.stats = nil // drop the (size,mtime) cache between tests
	t.Cleanup(func() {
		journalDirOverr, findingsDirOverr, discoveredDirOverr, auditPath = prevJournal, prevFindings, prevDisc, prevAudit
		proposalsDirOverride, cutoversDirOverride, jobsDirOverride, casesDirOverr, discoveryRunOver, designFileOverr = prevProp, prevCut, prevJob, prevCase, prevRun, prevDesign
		auditStatsCache.stats = nil
	})
	return env
}

func (e *dashEnv) writeAudit(t *testing.T, entries ...Audit) {
	t.Helper()
	f, err := os.OpenFile(auditPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range entries {
		if a.Time.IsZero() {
			a.Time = time.Now()
		}
		b, _ := json.Marshal(a)
		f.Write(append(b, '\n'))
	}
	f.Close()
	auditStatsCache.stats = nil
}

func TestAuditWindowCountsAndCache(t *testing.T) {
	env := dashTestEnv(t)
	now := time.Now()
	env.writeAudit(t,
		Audit{Time: now, Device: "SW-01", Command: "display version", Class: "read", Status: AuditOK},
		Audit{Time: now, Device: "SW-01", Command: "save", Class: "write", Status: AuditOK},
		Audit{Time: now, Device: "SW-01", Command: "delete", Class: "guardrail", Status: AuditRefused},
		Audit{Time: now, Device: "(proposal)", Command: "P-1 apply", Class: "proposal-write", Status: AuditOK},
		Audit{Time: now.AddDate(0, 0, -40), Device: "SW-01", Command: "display clock", Class: "read", Status: AuditOK}, // outside 30d window
	)
	st := AuditWindow(30)
	if st.Read24h != 1 || st.Write24h != 2 || st.Guardrail24h != 1 {
		t.Errorf("24h counters = %+v", st)
	}
	if st.Entries != 4 || st.ProposalWrites != 1 {
		t.Errorf("window = %+v", st)
	}
	// Append → cache invalidated by size change.
	env.writeAudit(t, Audit{Time: now, Device: "SW-01", Command: "display cpu", Class: "read", Status: AuditOK})
	if st2 := AuditWindow(30); st2.Read24h != 2 || st2.Entries != 5 {
		t.Errorf("cache not invalidated: %+v", st2)
	}
}

func TestBuildInvestigationChainCaseAndFold(t *testing.T) {
	env := dashTestEnv(t)
	now := time.Now()

	f1 := &Finding{
		ID: "F-100", Title: "OSPF 邻居 Down", Severity: SeverityCritical, Devices: []string{"SW-03"},
		Source: "syslog:SW-03:ospf-adjacency", Status: "active", CreatedAt: now,
		Evidence: []Evidence{
			{Device: "SW-03", Command: "display logbuffer", Output: "(redacted)"},
			{Device: "SW-03", Command: "display ospf peer", Output: "(redacted)"},
			{Device: "SW-03", Command: "display interface brief", Output: "(redacted)"},
			{Device: "SW-03", Command: "display cpu-usage", Output: "(redacted)"},
			{Device: "SW-03", Command: "display memory-usage", Output: "(redacted)"},
		},
	}
	f2 := &Finding{
		ID: "F-101", Title: "OSPF 邻居震荡（续）", Severity: SeverityWarning, Devices: []string{"SW-03"},
		Source: "syslog:SW-03:ospf-adjacency", Status: "active", CreatedAt: now,
		Evidence: []Evidence{{Device: "SW-03", Command: "display ospf peer", Output: "(redacted)"}},
	}
	if err := SaveFinding(f1); err != nil {
		t.Fatal(err)
	}
	if err := SaveFinding(f2); err != nil {
		t.Fatal(err)
	}
	c := &IncidentCase{ID: "C-1", Title: "OSPF 邻居震荡·SW-03", Status: "open", Devices: []string{"SW-03"},
		Entries: []CaseEntry{
			{Time: now, Kind: "finding", Device: "SW-03", Ref: "F-100"},
			{Time: now, Kind: "finding", Device: "SW-03", Ref: "F-101"},
		},
		UpdatedAt: now}
	if err := SaveCase(c); err != nil {
		t.Fatal(err)
	}
	// A remediation touching the case device, terminal-done.
	p := &Proposal{ID: "P-1", Intent: "调整 hello 定时器", Status: ProposalDone,
		Steps:     []ProposalStep{{Device: "SW-03", Commands: []string{"ospf timer hello 2"}}},
		CreatedAt: now, ExecutedAt: now}
	b, _ := json.Marshal(p)
	_ = os.WriteFile(filepath.Join(env.dir, "P-1.json"), b, 0o600)
	env.writeAudit(t, Audit{Time: now, Device: "SW-03", Command: "display logbuffer", Class: "read", Status: AuditOK})

	m := NewManager(&config.Config{})
	ch := m.BuildInvestigationChain("C-1", "", 24)
	if !ch.HasCase || ch.CaseID != "C-1" {
		t.Fatalf("case resolution = %+v", ch)
	}
	if ch.Counts[ChainKindConclusion] != 2 {
		t.Errorf("conclusions = %d", ch.Counts[ChainKindConclusion])
	}
	// Same Source → ONE event node carrying Group=2.
	if ch.Counts[ChainKindEvent] != 1 {
		t.Fatalf("events = %d (same-source findings must group)", ch.Counts[ChainKindEvent])
	}
	for _, n := range ch.Nodes {
		if n.Kind == ChainKindEvent && n.Group != 2 {
			t.Errorf("event group = %+v", n)
		}
	}
	// F-100: 5 evidence → 3 shown + 1 fold node (group=2); F-101: 1 evidence → 1 node.
	if ch.Counts[ChainKindEvidence] != 5 {
		t.Errorf("evidence nodes = %d (want 3+1 fold +1)", ch.Counts[ChainKindEvidence])
	}
	fold := false
	for _, n := range ch.Nodes {
		if n.Kind == ChainKindEvidence && n.Group == 2 {
			fold = true
		}
	}
	if !fold {
		t.Error("fold node missing")
	}
	if ch.Counts[ChainKindRemediation] != 1 || ch.Counts[ChainKindVerification] < 1 {
		t.Errorf("remediation/verification = %+v", ch.Counts)
	}
	// Deep-link landing: findingID resolves the case + marks the field.
	ch2 := m.BuildInvestigationChain("", "F-100", 24)
	if !ch2.HasCase || ch2.FindingID != "F-100" {
		t.Errorf("finding deep link = %+v", ch2)
	}
	// Every edge endpoint resolves to a real node id.
	ids := map[string]bool{}
	for _, n := range ch.Nodes {
		ids[n.ID] = true
	}
	for _, e := range ch.Edges {
		if !ids[e.From] || !ids[e.To] {
			t.Errorf("dangling edge %+v", e)
		}
	}
}

func TestBuildCutoverBoardPipeline(t *testing.T) {
	env := dashTestEnv(t)
	deadline := time.Now().Add(47 * time.Minute)
	run := &CutoverRun{
		ID: "CO-1", Name: "核心-SW 上联迁移", Status: CutoverRunning, Deadline: deadline, CreatedAt: time.Now().Add(-time.Hour),
		Steps: []CutoverStep{
			{Label: "预检", Status: CutoverStepDone, Device: "SW-01"},
			{Label: "配置下发", Status: CutoverStepRunning, Device: "SW-02", ProposalID: "P-9"},
			{Label: "验证门", Status: CutoverStepPending, Device: "R-05", Gate: &CutoverGate{Device: "R-05", Command: "display ospf peer", Expect: "Full"}},
		},
		PreSnapshot: map[string]string{"SW-01": "B-1", "SW-02": "B-2"},
	}
	b, _ := json.Marshal(run)
	_ = os.WriteFile(filepath.Join(env.dir, "CO-1.json"), b, 0o600)
	env.writeAudit(t, Audit{Time: time.Now(), Device: "SW-02", Command: "shut x/0/1", Class: "write", Status: AuditOK})

	board := BuildCutoverBoard("")
	if !board.Found || !board.HasActive || board.ID != "CO-1" {
		t.Fatalf("board = %+v", board)
	}
	if len(board.Steps) != 3 || !board.Steps[2].Gate {
		t.Errorf("steps = %+v", board.Steps)
	}
	if board.Frozen != 3 {
		t.Errorf("frozen devices = %d", board.Frozen)
	}
	// SW-01 all done; SW-02 running; R-05 pending.
	st := map[string]string{}
	for _, d := range board.Devices {
		st[d.Device] = d.Status
	}
	if st["SW-01"] != "done" || st["SW-02"] != "running" || st["R-05"] != "pending" {
		t.Errorf("device states = %+v", st)
	}
	if !board.RollbackReady {
		t.Errorf("rollback readiness = %+v", board)
	}
	rb := map[string]bool{}
	for _, d := range board.Devices {
		rb[d.Device] = d.RollbackReady
	}
	if !rb["SW-01"] || !rb["SW-02"] || rb["R-05"] {
		t.Errorf("per-device rollback points = %+v", rb)
	}
	if board.RemainingSec <= 0 || board.RemainingSec > 60*60 {
		t.Errorf("remaining = %d", board.RemainingSec)
	}
	if len(board.Audit) == 0 {
		t.Error("audit stream empty")
	}
}

func TestBuildDiscoveryBoardFunnel(t *testing.T) {
	env := dashTestEnv(t)
	cfg := &config.Config{}
	cfg.NetDev.Enabled = true
	cfg.NetDev.Devices = []config.NetDevDevice{
		{Name: "SW-01", Address: "10.0.0.1"},
		{Name: "R-01", Address: "10.0.0.2"},
	}
	m := NewManager(cfg)

	// Two leads: one with a fingerprint, one bare.
	fpData, _ := json.Marshal(DiscoveredHost{IP: "10.1.1.5", FirstSeen: time.Now(), LastSeen: time.Now(),
		Sources: []string{SourceDiscover}, VendorHint: "huawei", Layer: 1,
		Ports: []DiscoveredPort{{Port: 22, At: time.Now(), FirstSeen: time.Now()}}})
	_ = os.WriteFile(filepath.Join(env.dir, "10.1.1.5.json"), fpData, 0o600)
	bareData, _ := json.Marshal(DiscoveredHost{IP: "10.1.1.6", FirstSeen: time.Now(), LastSeen: time.Now(),
		Sources: []string{SourceDiscover}, Layer: 1})
	_ = os.WriteFile(filepath.Join(env.dir, "10.1.1.6.json"), bareData, 0o600)

	RecordPromotion("SW-99", "10.9.9.9")
	_ = SaveDiscoveryRun(&DiscoveryRunState{ID: "R1", Vantage: "SW-01",
		Cidrs:     []string{"10.1.1.0/24", "10.1.2.0/24", "10.1.3.0/24"},
		DoneCidrs: []string{"10.1.1.0/24"}, Status: "done",
		StartedAt: "09:00", UpdatedAt: "09:05"})

	board := m.BuildDiscoveryBoard()
	if board.Leads != 2 || board.Fingerprinted != 1 || board.Pending != 2 {
		t.Errorf("funnel top = %+v", board)
	}
	if board.Promoted != 1 || board.Managed != 2 {
		t.Errorf("promoted/managed = %d/%d", board.Promoted, board.Managed)
	}
	if board.SubnetsDone != 1 || board.SubnetsTotal != 3 {
		t.Errorf("subnets = %d/%d", board.SubnetsDone, board.SubnetsTotal)
	}
	if len(board.Funnel) != 5 {
		t.Errorf("funnel steps = %d", len(board.Funnel))
	}
}

func TestBuildExposureBoardMatrixAndSim(t *testing.T) {
	dashTestEnv(t)
	cfg := &config.Config{}
	cfg.NetDev.Enabled = true
	cfg.NetDev.Devices = []config.NetDevDevice{{Name: "EDGE-1", Address: "10.0.0.1"}, {Name: "SRV-1", Address: "10.0.0.9"}}
	m := NewManager(cfg)

	ev := []Evidence{{Device: "EDGE-1", Command: "(assess)", Output: "(redacted)"}}
	if err := SaveFinding(&Finding{ID: "F-1", Title: "弱口令确认：EDGE-1 ssh", Severity: SeverityCritical, Devices: []string{"EDGE-1"}, Status: "active", Evidence: ev}); err != nil {
		t.Fatal(err)
	}
	if err := SaveFinding(&Finding{ID: "F-2", Title: "telnet-enabled：SRV-1", Severity: SeverityWarning, Devices: []string{"SRV-1"}, Status: "active", Evidence: ev}); err != nil {
		t.Fatal(err)
	}
	if err := SaveFinding(&Finding{ID: "F-3", Title: "已修复的弱口令", Severity: SeverityCritical, Devices: []string{"EDGE-1"}, Status: "resolved", Evidence: ev}); err != nil {
		t.Fatal(err)
	}

	board := m.BuildExposureBoard()
	if !board.Simulated {
		t.Error("must carry 推演 label")
	}
	if board.Critical != 1 || board.Warning != 1 {
		t.Errorf("tallies = %+v", board)
	}
	row := map[string]ExposureMatrixRow{}
	for _, r := range board.Matrix {
		row[r.Device] = r
	}
	if row["EDGE-1"].Critical != 1 || row["SRV-1"].Warning != 1 {
		t.Errorf("matrix = %+v", board.Matrix)
	}
	if !row["EDGE-1"].Managed {
		t.Error("managed flag missing")
	}
	// No CVE feed imported in the temp env → guide state, never zero.
	if !board.CVENeedsFeed {
		t.Error("CVE needs-feed must be true without a feed")
	}
}

// TestAuditWindowTenKBudget — §9.12：1 万条注入的重放在 CI 宽松上限
// （250ms = 预算 50ms × 5）内完成；超限说明窗口扫描退化成了全量重算。
func TestAuditWindowTenKBudget(t *testing.T) {
	dashTestEnv(t)
	f, err := os.Create(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 10000; i++ {
		b, _ := json.Marshal(Audit{Time: now.Add(-time.Duration(i%3600) * time.Second),
			Device: "SW-01", Command: "display version", Class: "read", Status: AuditOK})
		f.Write(append(b, '\n'))
	}
	f.Close()
	start := time.Now()
	st := AuditWindow(30)
	elapsed := time.Since(start)
	if st.Entries != 10000 {
		t.Fatalf("entries = %d", st.Entries)
	}
	if elapsed > 250*time.Millisecond {
		t.Errorf("10k replay took %v (budget 250ms CI-loose)", elapsed)
	}
	t.Logf("10k replay: %v", elapsed)
}

// BuildTopoReconcile：设计↔规划对账 + 平台覆盖计数（纯离线，零探测）。
func TestBuildTopoReconcile(t *testing.T) {
	dashTestEnv(t)
	cfg := &config.Config{}
	cfg.NetDev.Enabled = true
	cfg.NetDev.Devices = []config.NetDevDevice{{Name: "SW-A", Address: "10.0.0.1"}, {Name: "SW-B", Address: "10.0.0.2"}}
	// 无设计导入：全部 OnlyPlan，平台计数只有规划侧（bastion 边）。
	r := BuildTopoReconcile(cfg)
	if r.Tri.Plan != 2 || r.Tri.OnlyPlan != 2 || r.Tri.Matched != 0 {
		t.Errorf("no-design reconcile = %+v", r.Tri)
	}
	// 导入一份设计：SW-A 吻合、GHOST-X 仅设计有。
	d := &TopologyDesign{Graph: TopologyGraph{
		Nodes: []TopologyNode{{Name: "SW-A"}, {Name: "GHOST-X"}},
		Edges: []TopologyEdge{{LocalDevice: "SW-A", RemoteDevice: "GHOST-X", Source: "design"}},
	}}
	if err := SaveTopologyDesign(d); err != nil {
		t.Fatal(err)
	}
	r = BuildTopoReconcile(cfg)
	if r.Tri.Matched != 1 || r.Tri.OnlyDesign != 1 || r.Tri.OnlyPlan != 1 {
		t.Errorf("with-design reconcile = %+v", r.Tri)
	}
	if r.Platforms["design"] != 1 {
		t.Errorf("platforms = %+v", r.Platforms)
	}
}
