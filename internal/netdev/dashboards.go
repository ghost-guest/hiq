package netdev

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

// dashboards.go — 大屏家族的四个屏组装函数（DASHBOARD spec §2.1）。全部
// 纯读、零会话、失败降级为空值而不是 panic：任一屏组装错误在桥层转成该屏
// 空态，不阻塞壳。语义不变量：
//   - 只读存量面（findings/audit/cases/proposals/jobs/cutover/discovered/
//     layers/design/plan），零命令执行；
//   - 节点/行只带标题级摘要与计数，命令输出全文不进 board（脱敏存量除外——
//     Finding.Evidence 与审计条目入库前已过脱敏）；
//   - 推演内容（BuildAttackPaths 输出）永带 Simulated=true，前端角标强制。

// ---------------------------------------------------------------------------
// 审计窗口统计（§8.2：默认 30 天窗口 + (大小, mtime) 缓存）
// ---------------------------------------------------------------------------

// AuditWindowStats is the aggregate the overview stats bar and the cutover
// board read. Read24h/Write24h/Guardrail24h mirror the briefing counters.
type AuditWindowStats struct {
	ByClass          map[string]int
	Read24h          int
	Write24h         int // write + proposal-write
	Guardrail24h     int
	ProposalWrites   int
	ProposalRollback int
	Entries          int // window size (honest denominator for the mix)
}

const auditWindowDefaultDays = 30
const auditWindowLineCap = 50000 // hard bound: older/overflow lines stay out (documented)

var (
	auditStatsMu    sync.Mutex
	auditStatsCache struct {
		size  int64
		mtime time.Time
		stats *AuditWindowStats
	}
)

// AuditWindow aggregates the audit chain inside the window (days, default 30,
// cap 90). Cached by (file size, mtime) — the chain is append-only, so the
// cache key is a single comparison.
func AuditWindow(days int) *AuditWindowStats {
	if days <= 0 || days > 90 {
		days = auditWindowDefaultDays
	}
	raw, err := os.ReadFile(AuditPath())
	if err != nil {
		return &AuditWindowStats{ByClass: map[string]int{}}
	}
	fi, _ := os.Stat(AuditPath())
	auditStatsMu.Lock()
	if fi != nil && auditStatsCache.stats != nil &&
		auditStatsCache.size == fi.Size() && auditStatsCache.mtime == fi.ModTime() {
		s := auditStatsCache.stats
		auditStatsMu.Unlock()
		return s
	}
	auditStatsMu.Unlock()

	cut := time.Now().AddDate(0, 0, -days)
	dayAgo := time.Now().AddDate(0, 0, -1)
	out := &AuditWindowStats{ByClass: map[string]int{}}
	lines := strings.Split(string(raw), "\n")
	if len(lines) > auditWindowLineCap {
		lines = lines[len(lines)-auditWindowLineCap:]
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		var e Audit
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if e.Time.Before(cut) {
			continue
		}
		out.Entries++
		out.ByClass[e.Class]++
		switch e.Class {
		case "proposal-write":
			out.ProposalWrites++
		case "proposal-rollback":
			out.ProposalRollback++
		}
		if e.Time.After(dayAgo) {
			switch e.Class {
			case "read":
				out.Read24h++
			case "write", "proposal-write":
				out.Write24h++
			case "guardrail":
				out.Guardrail24h++
			}
		}
	}
	auditStatsMu.Lock()
	auditStatsCache.size, auditStatsCache.mtime, auditStatsCache.stats = fi.Size(), fi.ModTime(), out
	auditStatsMu.Unlock()
	return out
}

// ---------------------------------------------------------------------------
// 调查链屏（§4.6）
// ---------------------------------------------------------------------------

// Chain kinds — the six semantic columns.
const (
	ChainKindEvent        = "event"
	ChainKindAction       = "action"
	ChainKindEvidence     = "evidence"
	ChainKindConclusion   = "conclusion"
	ChainKindRemediation  = "remediation"
	ChainKindVerification = "verification"
)

// ChainNode is one card on the investigation chain (title-level only).
type ChainNode struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Device string `json:"device,omitempty"`
	At     string `json:"at,omitempty"`
	// RefType/RefID deep-link the card to its source row (finding/audit/
	// proposal/case-entry); the dash never carries the full text.
	RefType string `json:"ref_type,omitempty"`
	RefID   string `json:"ref_id,omitempty"`
	Status  string `json:"status,omitempty"` // finding severity | proposal status
	// Group>0 marks a collapsed sibling group ("N× 命令采集").
	Group int `json:"group,omitempty"`
}

// ChainEdge is one typed relation (触发/执行/产生/证实/处置/验证/闭环).
type ChainEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}

// InvestigationChain is the whole chain screen payload.
type InvestigationChain struct {
	CaseID    string `json:"case_id,omitempty"`
	CaseTitle string `json:"case_title,omitempty"`
	HasCase   bool   `json:"has_case"`
	FindingID string `json:"finding_id,omitempty"`
	// Counts carries per-kind node tallies — the inline stats row; the
	// denominators are the node counts themselves (可查 via deep links).
	Counts    map[string]int  `json:"counts"`
	Nodes     []ChainNode     `json:"nodes"`
	Edges     []ChainEdge     `json:"edges"`
	Timeline  []TimelineEvent `json:"timeline,omitempty"`
	Truncated bool            `json:"truncated"`
}

const (
	chainNodeCap       = 150
	chainEvidenceShown = 3 // per conclusion before folding into a group node
	chainActionCap     = 30
	chainAuditLineScan = 2000
	chainLabelMaxRunes = 48
)

func chainTruncate(s string) string {
	r := []rune(s)
	if len(r) <= chainLabelMaxRunes {
		return s
	}
	return string(r[:chainLabelMaxRunes-1]) + "…"
}

// BuildInvestigationChain assembles the six-column chain (Manager method only
// to reuse Timeline; everything else reads package-level stores).
func (m *Manager) BuildInvestigationChain(caseID, findingID string, hours int) *InvestigationChain {
	if hours <= 0 || hours > 168 {
		hours = 24
	}
	now := time.Now()
	ch := &InvestigationChain{Counts: map[string]int{}, Nodes: []ChainNode{}, Edges: []ChainEdge{}}

	// Resolve the case: explicit id → the case containing the finding → the
	// most recently updated open case.
	var target *IncidentCase
	cases, _ := ListCases()
	trimCaseID, trimFinding := strings.TrimSpace(caseID), strings.TrimSpace(findingID)
	for _, c := range cases {
		if trimCaseID != "" && c.ID == trimCaseID {
			target = c
			break
		}
	}
	if target == nil && trimFinding != "" {
		for _, c := range cases {
			for _, e := range c.Entries {
				if e.Ref == trimFinding {
					target = c
					break
				}
			}
			if target != nil {
				break
			}
		}
	}
	if target == nil && trimCaseID == "" && trimFinding == "" {
		for _, c := range cases {
			if c.Status != "closed" && (target == nil || c.UpdatedAt.After(target.UpdatedAt)) {
				target = c
			}
		}
	}

	ch.FindingID = trimFinding
	findings, _ := ListFindings()
	sort.Slice(findings, func(i, j int) bool { return findings[i].CreatedAt.After(findings[j].CreatedAt) })

	// Scope: the findings the chain is about.
	inScope := map[string]bool{}
	if trimFinding != "" {
		inScope[trimFinding] = true
	}
	if target != nil {
		ch.CaseID, ch.CaseTitle, ch.HasCase = target.ID, target.Title, true
		devs := map[string]bool{}
		for _, d := range target.Devices {
			devs[d] = true
		}
		for _, e := range target.Entries {
			if e.Kind == "finding" && e.Ref != "" {
				inScope[e.Ref] = true
			}
			if e.Device != "" {
				devs[e.Device] = true
			}
		}
		if len(inScope) == 0 {
			// no pinned findings: fall back to window findings touching the case devices
			for _, f := range findings {
				if !f.CreatedAt.After(now.Add(-time.Duration(hours)*time.Hour)) && anyIn(f.Devices, devs) {
					inScope[f.ID] = true
				}
				if len(inScope) >= 20 {
					break
				}
			}
		}
		// plus the deep-linked finding even when the case lacks it
		for _, f := range findings {
			if f.ID == trimFinding {
				inScope[f.ID] = true
			}
		}
	}
	if len(inScope) == 0 && trimFinding == "" {
		// case-less view: newest window findings (cap 20)
		for _, f := range findings {
			if f.CreatedAt.After(now.Add(-time.Duration(hours) * time.Hour)) {
				inScope[f.ID] = true
			}
			if len(inScope) >= 20 {
				break
			}
		}
	}

	// The device universe the chain gathers actions for.
	devUniverse := map[string]bool{}
	var scoped []*Finding
	for _, f := range findings {
		if inScope[f.ID] {
			scoped = append(scoped, f)
			for _, d := range f.Devices {
				if d != "" {
					devUniverse[d] = true
				}
			}
		}
	}
	if target != nil {
		for _, d := range target.Devices {
			devUniverse[d] = true
		}
	}

	addNode := func(n ChainNode) string {
		if ch.Counts[n.Kind] >= 60 { // per-kind sanity bound before the global cap
			return ""
		}
		n.ID = n.Kind + "-" + itoa(len(ch.Nodes))
		ch.Nodes = append(ch.Nodes, n)
		ch.Counts[n.Kind]++
		return n.ID
	}
	addEdge := func(from, to, label string) {
		if from != "" && to != "" {
			ch.Edges = append(ch.Edges, ChainEdge{From: from, To: to, Label: label})
		}
	}

	// Column 1 — event sources (auto findings grouped by Source).
	eventBySource := map[string]string{}
	for _, f := range scoped {
		if f.Source == "" {
			continue
		}
		src := f.Source
		if id, ok := eventBySource[src]; ok {
			for i := range ch.Nodes {
				if ch.Nodes[i].ID == id {
					ch.Nodes[i].Group++
				}
			}
			continue
		}
		id := addNode(ChainNode{
			Kind: ChainKindEvent, Label: chainTruncate(src), Device: firstDevice(f.Devices),
			At: f.CreatedAt.Format("01-02 15:04"), RefType: "finding", RefID: f.ID, Status: f.Status,
			Group: 1, // "N findings share this source" starts at the first one
		})
		if id != "" {
			eventBySource[src] = id
		}
	}

	// Column 4 — conclusions, then 3 — evidence feeding each.
	conclusionID := map[string]string{}
	for _, f := range scoped {
		cid := addNode(ChainNode{
			Kind: ChainKindConclusion, Label: chainTruncate(f.Title), Device: firstDevice(f.Devices),
			At: f.CreatedAt.Format("01-02 15:04"), RefType: "finding", RefID: f.ID, Status: f.Severity,
		})
		if cid == "" {
			continue
		}
		conclusionID[f.ID] = cid
		if id, ok := eventBySource[f.Source]; ok && f.Source != "" {
			addEdge(id, cid, "触发")
		}
		shown := 0
		var groupID string
		for _, ev := range f.Evidence {
			if shown >= chainEvidenceShown {
				break
			}
			eid := addNode(ChainNode{
				Kind: ChainKindEvidence, Label: chainTruncate(ev.Command), Device: ev.Device,
				At: f.CreatedAt.Format("01-02 15:04"), RefType: "finding", RefID: f.ID,
			})
			addEdge(eid, cid, "产生")
			shown++
		}
		if extra := len(f.Evidence) - shown; extra > 0 {
			groupID = addNode(ChainNode{
				Kind: ChainKindEvidence, Label: "命令采集 ×" + itoa(extra), Device: firstDevice(f.Devices),
				RefType: "finding", RefID: f.ID, Group: extra,
			})
			addEdge(groupID, cid, "产生")
		}
		// resolved findings close the loop themselves
		if f.Status == "resolved" && f.ResolvedAt != nil {
			vid := addNode(ChainNode{
				Kind: ChainKindVerification, Label: "已闭环", At: f.ResolvedAt.Format("01-02 15:04"),
				RefType: "finding", RefID: f.ID, Status: "resolved",
			})
			addEdge(cid, vid, "闭环")
		}
	}

	// Column 2 — diagnosis actions (audit rows on the device universe).
	if len(devUniverse) > 0 && len(ch.Nodes) < chainNodeCap {
		type row struct {
			t                   time.Time
			device, cmd, status string
		}
		var rows []row
		raw, err := os.ReadFile(AuditPath())
		if err == nil {
			lines := strings.Split(string(raw), "\n")
			if len(lines) > chainAuditLineScan {
				lines = lines[len(lines)-chainAuditLineScan:]
			}
			for _, line := range lines {
				if line == "" {
					continue
				}
				var e Audit
				if json.Unmarshal([]byte(line), &e) != nil {
					continue
				}
				if !e.Time.After(now.Add(-time.Duration(hours) * time.Hour)) {
					continue
				}
				if !devUniverse[e.Device] || strings.HasPrefix(e.Device, "(") {
					continue // pseudo-devices (cutover/attack-path) are not 诊断动作
				}
				rows = append(rows, row{e.Time, e.Device, e.Command, e.Status})
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].t.After(rows[j].t) })
		if len(rows) > chainActionCap {
			rows = rows[:chainActionCap]
		}
		// evidence rows keyed by device+command so actions link to what they produced
		prodKey := map[string]bool{}
		for _, f := range scoped {
			for _, ev := range f.Evidence {
				prodKey[ev.Device+"\x00"+ev.Command] = true
			}
		}
		for _, r := range rows {
			aid := addNode(ChainNode{
				Kind: ChainKindAction, Label: chainTruncate(r.cmd), Device: r.device,
				At: r.t.Format("01-02 15:04"), RefType: "audit", Status: r.status,
			})
			if aid == "" {
				continue
			}
			if prodKey[r.device+"\x00"+r.cmd] {
				for _, n := range ch.Nodes {
					if n.Kind == ChainKindEvidence && n.Device == r.device && n.Label == chainTruncate(r.cmd) {
						addEdge(aid, n.ID, "执行")
					}
				}
			}
		}
	}

	// Columns 5-6 — remediation proposals and their verification.
	proposals, _ := ListProposals()
	sort.Slice(proposals, func(i, j int) bool { return proposals[i].CreatedAt.After(proposals[j].CreatedAt) })
	for _, p := range proposals {
		touches := false
		for _, s := range p.Steps {
			if devUniverse[s.Device] {
				touches = true
				break
			}
		}
		if !touches {
			continue
		}
		rid := addNode(ChainNode{
			Kind: ChainKindRemediation, Label: chainTruncate(p.ID + " " + p.Intent),
			At: p.CreatedAt.Format("01-02 15:04"), RefType: "proposal", RefID: p.ID, Status: p.Status,
		})
		if rid == "" {
			continue
		}
		for fID, cid := range conclusionID {
			_ = fID
			addEdge(cid, rid, "处置")
		}
		switch p.Status {
		case ProposalDone, ProposalWatching, ProposalClosed:
			vid := addNode(ChainNode{
				Kind: ChainKindVerification, Label: "执行完成", At: fmtTime(p.ExecutedAt),
				RefType: "proposal", RefID: p.ID, Status: p.Status,
			})
			addEdge(rid, vid, "验证")
		case ProposalFailed:
			vid := addNode(ChainNode{
				Kind: ChainKindVerification, Label: "回退失败待人工", RefType: "proposal", RefID: p.ID, Status: p.Status,
			})
			addEdge(rid, vid, "验证")
		}
	}

	// Global cap: drop oldest actions first, then mark truncated.
	if len(ch.Nodes) > chainNodeCap {
		kept := ch.Nodes[:0]
		dropped := map[string]bool{}
		for _, n := range ch.Nodes {
			if n.Kind == ChainKindAction && len(ch.Nodes)-len(dropped) > chainNodeCap {
				dropped[n.ID] = true
				continue
			}
			kept = append(kept, n)
		}
		ch.Nodes = kept
		ch.Counts[ChainKindAction] -= len(dropped)
		edges := ch.Edges[:0]
		for _, e := range ch.Edges {
			if !dropped[e.From] && !dropped[e.To] {
				edges = append(edges, e)
			}
		}
		ch.Edges = edges
		ch.Truncated = true
	}

	if m != nil {
		tl := m.Timeline("", hours)
		if len(tl) > 40 {
			tl = tl[len(tl)-40:]
		}
		ch.Timeline = tl
	}
	return ch
}

// ---------------------------------------------------------------------------
// 割接屏（§4.7）
// ---------------------------------------------------------------------------

// CutoverBoardStep is one pipeline node (proposal steps + gates + reads).
type CutoverBoardStep struct {
	Label      string `json:"label"`
	Status     string `json:"status"`
	Device     string `json:"device,omitempty"`
	ProposalID string `json:"proposal_id,omitempty"`
	Gate       bool   `json:"gate,omitempty"`
	Decision   bool   `json:"decision_point,omitempty"`
	EstSec     int    `json:"est_sec,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	EndedAt    string `json:"ended_at,omitempty"`
}

// CutoverBoardDevice is one affected device's own progress + rollback point.
type CutoverBoardDevice struct {
	Device        string `json:"device"`
	Status        string `json:"status"` // done | running | pending | mixed
	RollbackReady bool   `json:"rollback_ready"`
}

// CutoverBoardJob is a live job row (budget burn view; jobs are fleet-wide,
// not tied to the cutover — labeled as such in the UI).
type CutoverBoardJob struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	ActiveMS    int64  `json:"active_ms"`
	Commands    int    `json:"commands"`
	MaxWallSec  int    `json:"max_wall_sec,omitempty"`
	MaxCommands int    `json:"max_commands,omitempty"`
}

// CutoverBoardAudit is one command-stream row for this cutover.
type CutoverBoardAudit struct {
	Time    string `json:"time"`
	Device  string `json:"device"`
	Command string `json:"command"`
	Status  string `json:"status"`
}

// CutoverBoard is the whole cutover screen payload.
type CutoverBoard struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Status        string               `json:"status"`
	Deadline      string               `json:"deadline,omitempty"`
	RemainingSec  int64                `json:"remaining_sec"` // >0 running; <=0 window over
	Frozen        int                  `json:"frozen"`        // affected device count
	Steps         []CutoverBoardStep   `json:"steps"`
	Devices       []CutoverBoardDevice `json:"devices"`
	RollbackReady bool                 `json:"rollback_ready"`
	RollbackNote  string               `json:"rollback_note,omitempty"`
	Jobs          []CutoverBoardJob    `json:"jobs"`
	Audit         []CutoverBoardAudit  `json:"audit"`
	Report        string               `json:"report,omitempty"`
	HasActive     bool                 `json:"has_active"` // any running/hold cutover exists
	Found         bool                 `json:"found"`      // false = no cutover at all → empty state
}

// BuildCutoverBoard assembles the cutover screen. id=="" picks the active
// cutover, else the newest one (终态复盘).
func BuildCutoverBoard(id string) *CutoverBoard {
	b := &CutoverBoard{Steps: []CutoverBoardStep{}, Devices: []CutoverBoardDevice{}, Jobs: []CutoverBoardJob{}, Audit: []CutoverBoardAudit{}}
	runs, err := ListCutovers()
	if err != nil || len(runs) == 0 {
		return b
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })
	for _, r := range runs {
		if r.Status == CutoverRunning || r.Status == CutoverHold {
			b.HasActive = true
			break
		}
	}
	var run *CutoverRun
	trim := strings.TrimSpace(id)
	if trim != "" {
		for _, r := range runs {
			if r.ID == trim {
				run = r
				break
			}
		}
	}
	if run == nil {
		for _, r := range runs {
			if r.Status == CutoverRunning || r.Status == CutoverHold {
				run = r
				break
			}
		}
	}
	if run == nil {
		run = runs[0]
	}

	b.ID, b.Name, b.Status, b.Report = run.ID, run.Name, run.Status, run.Report
	b.Deadline = run.Deadline.Format("01-02 15:04")
	b.RemainingSec = int64(time.Until(run.Deadline).Seconds())

	// Affected devices: step devices + proposal step devices.
	propByID := map[string]*Proposal{}
	if ps, err := ListProposals(); err == nil {
		for _, p := range ps {
			propByID[p.ID] = p
		}
	}
	devStatus := map[string][]string{}
	rollback := map[string]bool{}
	for name := range run.PreSnapshot {
		rollback[name] = true
	}
	for _, s := range run.Steps {
		st := CutoverBoardStep{
			Label: s.Label, Status: s.Status, Device: s.Device, ProposalID: s.ProposalID,
			Gate: s.Gate != nil, Decision: s.DecisionPoint, EstSec: s.EstSec,
		}
		if s.StartedAt != nil {
			st.StartedAt = s.StartedAt.Format("01-02 15:04")
		}
		if s.EndedAt != nil {
			st.EndedAt = s.EndedAt.Format("01-02 15:04")
		}
		b.Steps = append(b.Steps, st)
		devs := []string{}
		if s.Device != "" {
			devs = append(devs, s.Device)
		}
		if s.ProposalID != "" && propByID[s.ProposalID] != nil {
			for _, ps := range propByID[s.ProposalID].Steps {
				if ps.Device != "" {
					devs = append(devs, ps.Device)
				}
			}
		}
		for _, d := range devs {
			devStatus[d] = append(devStatus[d], s.Status)
		}
	}
	names := make([]string, 0, len(devStatus))
	for d := range devStatus {
		names = append(names, d)
	}
	sort.Strings(names)
	for _, d := range names {
		sts := devStatus[d]
		allDone, anyRun := true, false
		for _, s := range sts {
			if s != CutoverStepDone && s != CutoverStepApproved && s != CutoverStepRolled && s != CutoverStepSkipped {
				allDone = false
			}
			if s == CutoverStepRunning {
				anyRun = true
			}
		}
		st := "pending"
		switch {
		case allDone && len(sts) > 0:
			st = "done"
		case anyRun:
			st = "running"
		case len(distinct(sts)) > 1:
			st = "mixed"
		}
		b.Devices = append(b.Devices, CutoverBoardDevice{Device: d, Status: st, RollbackReady: rollback[d]})
	}
	b.Frozen = len(b.Devices)
	b.RollbackReady = len(run.PreSnapshot) > 0
	if b.RollbackReady {
		b.RollbackNote = "快照 " + itoa(len(run.PreSnapshot)) + " 台"
	}

	// Live jobs (fleet-wide, honestly labeled).
	if jobs, err := ListJobs(); err == nil {
		for _, j := range jobs {
			if j.Status != JobRunning && j.Status != JobPaused {
				continue
			}
			b.Jobs = append(b.Jobs, CutoverBoardJob{
				ID: j.ID, Name: j.Name, Status: j.Status, ActiveMS: j.ActiveMS, Commands: j.Commands,
				MaxWallSec: j.Budget.MaxWallSec, MaxCommands: j.Budget.MaxCommands,
			})
		}
	}

	// Command stream: audit rows touching the cutover or its devices (1h tail).
	devSet := map[string]bool{}
	for _, d := range b.Devices {
		devSet[d.Device] = true
	}
	if raw, err := os.ReadFile(AuditPath()); err == nil {
		lines := strings.Split(string(raw), "\n")
		if len(lines) > 400 {
			lines = lines[len(lines)-400:]
		}
		hourAgo := time.Now().Add(-time.Hour)
		for _, line := range lines {
			if line == "" {
				continue
			}
			var e Audit
			if json.Unmarshal([]byte(line), &e) != nil {
				continue
			}
			mine := (e.Device == "(cutover)" && strings.Contains(e.Command, run.ID)) || devSet[e.Device]
			if !mine || e.Time.Before(hourAgo) {
				continue
			}
			b.Audit = append(b.Audit, CutoverBoardAudit{
				Time: e.Time.Format("15:04:05"), Device: e.Device, Command: chainTruncate(e.Command), Status: e.Status,
			})
		}
		if len(b.Audit) > 30 {
			b.Audit = b.Audit[len(b.Audit)-30:]
		}
	}
	b.Found = true
	return b
}

// ---------------------------------------------------------------------------
// 发现屏（§4.8）
// ---------------------------------------------------------------------------

// DiscoveryFunnelStep is one funnel stage (Key: leads|fingerprinted|pending|
// promoted|managed — labels live in the frontend i18n).
type DiscoveryFunnelStep struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// DiscoveryLayerRow is one layer-ledger summary row.
type DiscoveryLayerRow struct {
	Layer int    `json:"layer"`
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
}

// TriSourceAgg is the design↔plan reconciliation (图实一致性, honest counts).
type TriSourceAgg struct {
	Design     int `json:"design"`
	Plan       int `json:"plan"`
	Matched    int `json:"matched"`
	OnlyDesign int `json:"only_design"`
	OnlyPlan   int `json:"only_plan"`
}

// DiscoveryBoard is the whole discovery screen payload.
type DiscoveryBoard struct {
	Leads         int `json:"leads"`
	Fingerprinted int `json:"fingerprinted"`
	Pending       int `json:"pending"`
	Promoted      int `json:"promoted"`
	Managed       int `json:"managed"`

	SubnetsDone  int `json:"subnets_done"`
	SubnetsTotal int `json:"subnets_total"`
	LayerDepth   int `json:"layer_depth"`
	MaxHops      int `json:"max_hops"`

	RunStatus    string `json:"run_status,omitempty"`
	RunVantage   string `json:"run_vantage,omitempty"`
	RunUpdatedAt string `json:"run_updated_at,omitempty"`

	Funnel     []DiscoveryFunnelStep `json:"funnel"`
	Layers     []DiscoveryLayerRow   `json:"layers"`
	TriSource  TriSourceAgg          `json:"tri_source"`
	PortEvents []PortEvent           `json:"port_events"`
}

// BuildDiscoveryBoard assembles the discovery screen (needs Manager only for
// the config inventory).
func (m *Manager) BuildDiscoveryBoard() *DiscoveryBoard {
	b := &DiscoveryBoard{Funnel: []DiscoveryFunnelStep{}, Layers: []DiscoveryLayerRow{}, PortEvents: []PortEvent{}}
	b.MaxHops = maxHopsEffective(m.cfg.NetDev.Discovery.MaxHops)
	hosts, _ := ListDiscoveredHosts()
	b.Leads = len(hosts)
	for _, h := range hosts {
		fp := h.VendorHint != "" || h.RoleHint != ""
		for _, p := range h.Ports {
			if p.Parsed.Kind != "" || p.HTTP != nil {
				fp = true
				break
			}
		}
		if fp {
			b.Fingerprinted++
		}
	}
	b.Pending = b.Leads
	b.Promoted = CountPromotions()
	b.Managed = len(m.cfg.NetDev.Devices)
	b.Funnel = []DiscoveryFunnelStep{
		{Key: "leads", Count: b.Leads},
		{Key: "fingerprinted", Count: b.Fingerprinted},
		{Key: "pending", Count: b.Pending},
		{Key: "promoted", Count: b.Promoted},
		{Key: "managed", Count: b.Managed},
	}

	if run, err := LoadDiscoveryRun(); err == nil && run != nil {
		b.SubnetsTotal = len(run.Cidrs)
		b.SubnetsDone = len(run.DoneCidrs)
		b.RunStatus, b.RunVantage, b.RunUpdatedAt = run.Status, run.Vantage, run.UpdatedAt
	} else if n := len(m.cfg.NetDev.Discovery.Scopes); n > 0 {
		b.SubnetsTotal = n // no run yet: the scopes are the honest denominator
	}

	// Layer ledger rows: managed device depths + lead depths.
	layers := LoadDeviceLayers()
	byDepth := map[int]int{}
	for _, d := range layers {
		byDepth[d]++
		if d > b.LayerDepth {
			b.LayerDepth = d
		}
	}
	for _, h := range hosts {
		if h.Layer > 0 {
			byDepth[h.Layer+1000]++ // leads live above the vantage rows (display sort)
		}
	}
	deps := make([]int, 0, len(byDepth))
	for d := range byDepth {
		deps = append(deps, d)
	}
	sort.Ints(deps)
	for _, d := range deps {
		row := DiscoveryLayerRow{Layer: d, Label: "L" + itoa(d), Note: itoa(byDepth[d]) + " 台"}
		if d >= 1000 {
			row.Layer = d - 1000
			row.Label = "L" + itoa(d-1000)
			row.Note = itoa(byDepth[d]) + " 条线索"
		}
		b.Layers = append(b.Layers, row)
	}

	// 图实一致性：设计 ↔ IP 规划（两份都是离线存量，零探测）。
	design := map[string]bool{}
	if d, err := LoadTopologyDesign(); err == nil && d != nil {
		for _, n := range d.Graph.Nodes {
			design[strings.ToLower(strings.TrimSpace(n.Name))] = true
		}
	}
	plan := InferTopology(m.cfg)
	planSet := map[string]bool{}
	for _, n := range plan.Nodes {
		planSet[strings.ToLower(strings.TrimSpace(n.Name))] = true
	}
	for name := range design {
		if planSet[name] {
			b.TriSource.Matched++
		} else {
			b.TriSource.OnlyDesign++
		}
	}
	for name := range planSet {
		if !design[name] {
			b.TriSource.OnlyPlan++
		}
	}
	b.TriSource.Design = len(design)
	b.TriSource.Plan = len(planSet)

	b.PortEvents = ListPortEvents(20)
	return b
}

// ---------------------------------------------------------------------------
// 暴露面屏（§4.9）——复用 BuildAttackPaths（推演），外加矩阵与 CVE 融合
// ---------------------------------------------------------------------------

// ExposureMatrixRow is one device's exposure tally.
type ExposureMatrixRow struct {
	Device      string `json:"device"`
	Critical    int    `json:"critical"`
	Warning     int    `json:"warning"`
	Info        int    `json:"info"`
	CveCritical int    `json:"cve_critical"`
	CveHigh     int    `json:"cve_high"`
	Managed     bool   `json:"managed"`
}

// ExposureBoard is the whole exposure screen payload.
type ExposureBoard struct {
	Simulated      bool                `json:"simulated"` // constant true — 推演角标
	GeneratedAt    string              `json:"generated_at"`
	Critical       int                 `json:"critical"`
	Warning        int                 `json:"warning"`
	Paths          []AttackPath        `json:"paths"`           // top 50 by score
	Cuts           []CutSuggestion     `json:"cut_suggestions"` // top 10
	ExposurePoints []ExposurePoint     `json:"exposure_points"`
	Matrix         []ExposureMatrixRow `json:"matrix"`
	CVEBySeverity  map[string]int      `json:"cve_by_severity,omitempty"`
	CVENeedsFeed   bool                `json:"cve_needs_feed"`
	UnmanagedEnds  int                 `json:"unmanaged_ends"`
	MaxHops        int                 `json:"max_hops"`
}

// TopologyFused is the zero-session graph the attack-path sim and the
// exposure board share: IP-plan inference + persisted design import merged.
func TopologyFused(cfg *config.Config) TopologyGraph {
	g := InferTopology(cfg)
	if d, err := LoadTopologyDesign(); err == nil && d != nil {
		g.Edges = append(g.Edges, d.Graph.Edges...)
		known := map[string]bool{}
		for _, n := range g.Nodes {
			known[n.Name] = true
		}
		for _, n := range d.Graph.Nodes {
			if !known[n.Name] {
				g.Nodes = append(g.Nodes, n)
			}
		}
	}
	return g
}

// BuildExposureBoard assembles the exposure screen (推演 + 事实矩阵).
func (m *Manager) BuildExposureBoard() *ExposureBoard {
	b := &ExposureBoard{
		Simulated: true, GeneratedAt: time.Now().Format("01-02 15:04"),
		Paths: []AttackPath{}, Cuts: []CutSuggestion{}, ExposurePoints: []ExposurePoint{},
		Matrix: []ExposureMatrixRow{},
	}
	findings, _ := ListFindings()
	managed := map[string]bool{}
	for _, d := range m.cfg.NetDev.Devices {
		managed[d.Name] = true
	}

	// Facts: open findings per device (matrix) + fleet tallies.
	rowByDev := map[string]*ExposureMatrixRow{}
	row := func(dev string) *ExposureMatrixRow {
		if r, ok := rowByDev[dev]; ok {
			return r
		}
		r := &ExposureMatrixRow{Device: dev, Managed: managed[dev]}
		rowByDev[dev] = r
		return r
	}
	for _, f := range findings {
		if f.Status == "resolved" {
			continue
		}
		switch f.Severity {
		case SeverityCritical:
			b.Critical++
		case SeverityWarning:
			b.Warning++
		}
		for _, d := range f.Devices {
			r := row(d)
			switch f.Severity {
			case SeverityCritical:
				r.Critical++
			case SeverityWarning:
				r.Warning++
			default:
				r.Info++
			}
		}
	}

	// CVE fusion (feed-gated; no feed → guide state, never 0).
	if ms, err := m.MatchCVEs(); err == nil {
		b.CVEBySeverity = map[string]int{}
		for _, mt := range ms {
			b.CVEBySeverity[mt.Severity]++
			r := row(mt.Device)
			switch strings.ToLower(mt.Severity) {
			case "critical":
				r.CveCritical++
			case "high":
				r.CveHigh++
			}
		}
	} else {
		b.CVENeedsFeed = true
	}

	// Simulation (推演): shared fused graph + BuildAttackPaths.
	report := BuildAttackPaths(TopologyFused(m.cfg), findings)
	b.ExposurePoints = report.ExposurePoints
	if len(report.Paths) > 50 {
		b.Paths = report.Paths[:50]
	} else {
		b.Paths = report.Paths
	}
	sort.SliceStable(b.Paths, func(i, j int) bool { return b.Paths[i].Score > b.Paths[j].Score })
	if len(report.Cuts) > 10 {
		b.Cuts = report.Cuts[:10]
	} else {
		b.Cuts = report.Cuts
	}
	for _, p := range report.Paths {
		if !p.EndManaged {
			b.UnmanagedEnds++
		}
		if p.Hops > b.MaxHops {
			b.MaxHops = p.Hops
		}
	}

	// Matrix rows: devices with findings or CVEs, hottest first, cap 30.
	rows := make([]ExposureMatrixRow, 0, len(rowByDev))
	for _, r := range rowByDev {
		rows = append(rows, *r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Critical != rows[j].Critical {
			return rows[i].Critical > rows[j].Critical
		}
		if rows[i].Warning != rows[j].Warning {
			return rows[i].Warning > rows[j].Warning
		}
		if rows[i].CveCritical != rows[j].CveCritical {
			return rows[i].CveCritical > rows[j].CveCritical
		}
		return rows[i].Device < rows[j].Device
	})
	if len(rows) > 30 {
		rows = rows[:30]
	}
	b.Matrix = rows
	return b
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func itoa(n int) string {
	return fmtInt(n)
}

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("01-02 15:04")
}

func firstDevice(devs []string) string {
	for _, d := range devs {
		if d != "" {
			return d
		}
	}
	return ""
}

func anyIn(list []string, set map[string]bool) bool {
	for _, s := range list {
		if set[s] {
			return true
		}
	}
	return false
}

func distinct(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// TopoReconcile is the topology tab's reconciliation card: the offline
// design↔plan diff plus neighbor-platform coverage (edges by source).
type TopoReconcile struct {
	Tri       TriSourceAgg        `json:"tri"`
	Platforms map[string]int      `json:"platforms"` // edge source → count (lldp/cdp/design/bastion)
}

// BuildTopoReconcile is pure over the two offline stores (design import +
// IP-plan inference); zero probes — a view, never a scan.
func BuildTopoReconcile(cfg *config.Config) *TopoReconcile {
	r := &TopoReconcile{Platforms: map[string]int{}}
	design := map[string]bool{}
	if d, err := LoadTopologyDesign(); err == nil && d != nil {
		for _, n := range d.Graph.Nodes {
			design[strings.ToLower(strings.TrimSpace(n.Name))] = true
		}
		for _, e := range d.Graph.Edges {
			r.Platforms[e.Source]++
		}
	}
	plan := InferTopology(cfg)
	planSet := map[string]bool{}
	for _, n := range plan.Nodes {
		planSet[strings.ToLower(strings.TrimSpace(n.Name))] = true
	}
	for _, e := range plan.Edges {
		r.Platforms[e.Source]++
	}
	for name := range design {
		if planSet[name] {
			r.Tri.Matched++
		} else {
			r.Tri.OnlyDesign++
		}
	}
	for name := range planSet {
		if !design[name] {
			r.Tri.OnlyPlan++
		}
	}
	r.Tri.Design = len(design)
	r.Tri.Plan = len(planSet)
	return r
}
