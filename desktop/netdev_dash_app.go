package main

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev"
)

// netdev_dash_app.go — 大屏家族的 desktop 聚合层（DASHBOARD spec v2.0）：
// D1 总览快照（buildOverviewData + NetDevOverview，30s 缓存）、四屏桥、
// hiq:netdev-dash 写侧推送（§3.4）。总览与早报同源（D3）：早报的
// 客观计数全部读这份快照，两套口径永不漂移。

// ---------------------------------------------------------------------------
// 视图类型（前端 types.ts 镜像）
// ---------------------------------------------------------------------------

type NetDevOvCoverage struct {
	Managed     int `json:"managed"`
	Discovered  int `json:"discovered"`
	Unreachable int `json:"unreachable"`
	NoSNMP      int `json:"no_snmp"`
}

type NetDevOvHealth struct {
	Polled      int                  `json:"polled"`
	Reachable   int                  `json:"reachable"`
	LastPollAt  int64                `json:"last_poll_at"` // ms
	FlapAlerts  int                  `json:"flap_alerts"`
	P90Alerts   int                  `json:"p90_alerts"`
	UptimeSpark map[string][]float64 `json:"uptime_spark,omitempty"`
	// 水位（SNMP OID 扩展）：全网最高 cpu/mem 百分比与所在设备；0 = 未采集。
	MaxCpuPct int    `json:"max_cpu_pct"`
	MaxCpuDev string `json:"max_cpu_dev,omitempty"`
	MaxMemPct int    `json:"max_mem_pct"`
}

type NetDevOvRisk struct {
	Critical      int    `json:"critical"`
	Warning       int    `json:"warning"`
	Info          int    `json:"info"`
	OpenTotal     int    `json:"open_total"`
	WeightedScore int    `json:"weighted_score"`
	RiskLevel     string `json:"risk_level"` // safe|low|medium|high|critical
	CVEMatches    int    `json:"cve_matches"`
	CVENeedsFeed  bool   `json:"cve_needs_feed"`
	WeakCreds     int    `json:"weak_creds"`
}

type NetDevOvInflight struct {
	ProposalsPending   int `json:"proposals_pending"`
	ProposalsWatchable int `json:"proposals_watchable"`
	JobsRunning        int `json:"jobs_running"`
	JobsPaused         int `json:"jobs_paused"`
	CutoversActive     int `json:"cutovers_active"`
	TerminalsOpen      int `json:"terminals_open"`
}

type NetDevOvEvent struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Source   string `json:"source"`
	At       string `json:"at"`
}

type NetDevOvAudit struct {
	ChainOK      bool   `json:"chain_ok"`
	ChainTotal   int    `json:"chain_total"`
	LastEntryAt  string `json:"last_entry_at,omitempty"`
	Read24h      int    `json:"read_24h"`
	Write24h     int    `json:"write_24h"`
	Guardrail24h int    `json:"guardrail_24h"`
}

type NetDevOvStats struct {
	MTTRHours      *float64                      `json:"mttr_hours,omitempty"` // nil = 无已闭环样本（引导态，不是 0）
	Baseline       *netdev.BaselineAggView       `json:"baseline,omitempty"`   // nil = 从未跑过
	CVEBySeverity  map[string]int                `json:"cve_by_severity,omitempty"`
	CVENeedsFeed   bool                          `json:"cve_needs_feed"`
	JobDone        int                           `json:"job_done"`
	JobFinished    int                           `json:"job_finished"`  // done+failed+aborted（成功率分母）
	CmdMix         map[string]int                `json:"cmd_mix"`       // 30 天窗口 class 计数
	AuditEntries   int                           `json:"audit_entries"` // 窗口分母
	DeviceByRole   map[string]int                `json:"device_by_role"`
	ProposalFunnel map[string]int                `json:"proposal_funnel"`
	RiskTrend      []netdev.InspectionJournalRow `json:"risk_trend,omitempty"` // R1 尾部 30 行
	// 巡检合规（§7.3）：调度器启用与否 + 最近一次调度执行戳；nil = 调度未启用。
	InspectionCompliance *NetDevOvCompliance `json:"inspection_compliance,omitempty"`
	// 凭证健康：netdev 命名空间条数 + 库文件最近变更（粒度=整库）。
	CredHealth *NetDevOvCredHealth `json:"cred_health,omitempty"`
}

type NetDevOvCredHealth struct {
	Count         int    `json:"count"`
	LastChangedAt string `json:"last_changed_at,omitempty"`
	AgeDays       int    `json:"age_days"`
	Stale         bool   `json:"stale"` // > 90 天未变更 → 轮换提醒
}

type NetDevOvCompliance struct {
	Enabled   bool   `json:"enabled"` // inspection_interval 可解析且 > 0
	LastRunAt string `json:"last_run_at,omitempty"`
	Ok        bool   `json:"ok"`
	Title     string `json:"title,omitempty"`
	Note      string `json:"note,omitempty"`
}

type NetDevOverviewSnapshot struct {
	GeneratedAt   int64            `json:"generated_at"` // ms
	StaleAfterSec int              `json:"stale_after_sec"`
	Coverage      NetDevOvCoverage `json:"coverage"`
	Health        NetDevOvHealth   `json:"health"`
	Risk          NetDevOvRisk     `json:"risk"`
	Inflight      NetDevOvInflight `json:"inflight"`
	Events        []NetDevOvEvent  `json:"events"`
	Audit         NetDevOvAudit    `json:"audit"`
	Stats         NetDevOvStats    `json:"stats"`

	// Scenario hints for the shell's default landing (§4.1): cutover active /
	// discovery run active are cheap booleans the shell reads on open.
	ScenarioCutoverActive bool `json:"scenario_cutover_active"`
	ScenarioDiscoveryRun  bool `json:"scenario_discovery_run"`
}

// ---------------------------------------------------------------------------
// 缓存（§3.2：30s 快照缓存；审计链校验 10min 缓存——全链重算不进热路径）
// ---------------------------------------------------------------------------

var (
	dashSnapMu   sync.Mutex
	dashSnap     *NetDevOverviewSnapshot
	dashSnapAt   time.Time
	chainOKMu    sync.Mutex
	chainOKCache netdev.AuditChainStatus
	chainOKAt    time.Time
)

func auditChainStatusCached() netdev.AuditChainStatus {
	chainOKMu.Lock()
	defer chainOKMu.Unlock()
	if !chainOKAt.IsZero() && time.Since(chainOKAt) < 10*time.Minute {
		return chainOKCache
	}
	st := netdev.VerifyAuditChain()
	chainOKCache, chainOKAt = st, time.Now()
	return st
}

// riskLevelFromScore applies §5's thresholds + floor rule.
func riskLevelFromScore(score int, cveCritical, weakCreds int) string {
	lvl := "critical"
	switch {
	case score == 0:
		lvl = "safe"
	case score <= 3:
		lvl = "low"
	case score <= 10:
		lvl = "medium"
	case score <= 30:
		lvl = "high"
	}
	// floor: one confirmed critical CVE or weak credential is at least high
	if lvl == "safe" || lvl == "low" || lvl == "medium" {
		if cveCritical >= 1 || weakCreds >= 1 {
			lvl = "high"
		}
	}
	return lvl
}

func isWeakCredFinding(f *netdev.Finding) bool {
	t := strings.ToLower(f.Title)
	return strings.Contains(t, "弱口令") || strings.Contains(t, "weak")
}

// buildOverviewData is the shared aggregator (D1). Read-only: every number
// comes from an existing store; nothing here executes a command.
func (a *App) buildOverviewData(force bool) (*NetDevOverviewSnapshot, error) {
	dashSnapMu.Lock()
	if !force && dashSnap != nil && time.Since(dashSnapAt) < 30*time.Second {
		s := dashSnap
		dashSnapMu.Unlock()
		return s, nil
	}
	dashSnapMu.Unlock()

	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	m := netdev.SharedManager(cfg)
	snap := &NetDevOverviewSnapshot{
		GeneratedAt:   time.Now().UnixMilli(),
		StaleAfterSec: 300,
		Events:        []NetDevOvEvent{},
	}

	// ---- coverage / health ----
	snap.Coverage.Managed = len(cfg.NetDev.Devices)
	if leads, lerr := netdev.ListDiscoveredHosts(); lerr == nil {
		snap.Coverage.Discovered = len(leads)
	}
	hs := m.HealthSnapshot()
	polled := 0
	for _, d := range cfg.NetDev.Devices {
		if d.SNMP != nil {
			polled++
		}
	}
	snap.Health.Polled = polled
	for _, h := range hs.Devices {
		if h.Reachable {
			snap.Health.Reachable++
		}
		if t := h.Time.UnixMilli(); t > snap.Health.LastPollAt {
			snap.Health.LastPollAt = t
		}
	}
	snap.Coverage.NoSNMP = snap.Coverage.Managed - polled
	snap.Coverage.Unreachable = polled - snap.Health.Reachable
	for _, h := range hs.Devices {
		if h.CpuPct > snap.Health.MaxCpuPct {
			snap.Health.MaxCpuPct, snap.Health.MaxCpuDev = h.CpuPct, h.Device
		}
		if h.MemPct > snap.Health.MaxMemPct {
			snap.Health.MaxMemPct = h.MemPct
		}
	}
	snap.Health.UptimeSpark = map[string][]float64{}
	for _, d := range cfg.NetDev.Devices {
		if d.SNMP == nil {
			continue
		}
		pts := netdev.MetricHistory(d.Name, 96)
		if len(pts) == 0 {
			continue
		}
		// MetricHistory is newest-first; sparkline wants oldest→newest.
		ser := make([]float64, 0, len(pts))
		for i := len(pts) - 1; i >= 0; i-- {
			ser = append(ser, float64(pts[i].UptimeSec)/3600.0) // hours
		}
		snap.Health.UptimeSpark[d.Name] = ser
		if netdev.FlapCount(d.Name, 24*time.Hour) >= 3 {
			snap.Health.FlapAlerts++
		}
		if p90, ok := netdev.P90IfDown(d.Name, 24*time.Hour); ok && p90 > 0 {
			snap.Health.P90Alerts++
		}
	}

	// ---- risk ----
	findings, _ := netdev.ListFindings()
	sort.Slice(findings, func(i, j int) bool { return findings[i].CreatedAt.After(findings[j].CreatedAt) })
	cveCritical := 0
	cveMatches, cveErr := m.MatchCVEs()
	if cveErr == nil {
		snap.Risk.CVEMatches = len(cveMatches)
		snap.Stats.CVEBySeverity = map[string]int{}
		for _, mt := range cveMatches {
			snap.Stats.CVEBySeverity[mt.Severity]++
			if strings.EqualFold(mt.Severity, "critical") {
				cveCritical++
			}
		}
	} else {
		snap.Risk.CVENeedsFeed = true
		snap.Stats.CVENeedsFeed = true
	}
	for _, f := range findings {
		if f.Status == "resolved" {
			continue
		}
		snap.Risk.OpenTotal++
		switch f.Severity {
		case netdev.SeverityCritical:
			snap.Risk.Critical++
		case netdev.SeverityWarning:
			snap.Risk.Warning++
		default:
			snap.Risk.Info++
		}
		if f.Severity == netdev.SeverityCritical && isWeakCredFinding(f) {
			snap.Risk.WeakCreds++
		}
		src := f.Source
		if src != "" && (strings.HasPrefix(src, "syslog") || strings.HasPrefix(src, "trap") || strings.HasPrefix(src, "alert")) {
			if len(snap.Events) < 20 {
				snap.Events = append(snap.Events, NetDevOvEvent{
					ID: f.ID, Severity: f.Severity, Title: f.Title, Source: src,
					At: f.CreatedAt.Format("01-02 15:04"),
				})
			}
		}
	}
	snap.Risk.WeightedScore = snap.Risk.Critical*10 + snap.Risk.Warning*3 + int(float64(snap.Risk.Info)*0.5)
	snap.Risk.RiskLevel = riskLevelFromScore(snap.Risk.WeightedScore, cveCritical, snap.Risk.WeakCreds)

	// ---- inflight ----
	// ProposalFunnel must exist before the loop writes to it — a nil map write
	// panics the binding, and an unwritten nil serialises as JSON null, which
	// the overview's Object.entries(proposal_funnel) turns into a frontend
	// TypeError on the empty-proposals path.
	snap.Stats.ProposalFunnel = map[string]int{}
	if ps, perr := netdev.ListProposals(); perr == nil {
		now := time.Now()
		for _, p := range ps {
			switch p.Status {
			case netdev.ProposalDraft:
				snap.Inflight.ProposalsPending++
			case netdev.ProposalWatching:
				if p.WatchUntil != nil && p.WatchUntil.After(now) {
					snap.Inflight.ProposalsWatchable++
				}
			}
			snap.Stats.ProposalFunnel[p.Status]++
		}
	}
	if jobs, jerr := netdev.ListJobs(); jerr == nil {
		for _, j := range jobs {
			switch j.Status {
			case netdev.JobRunning:
				snap.Inflight.JobsRunning++
			case netdev.JobPaused:
				snap.Inflight.JobsPaused++
			case netdev.JobDone:
				snap.Stats.JobDone++
				snap.Stats.JobFinished++
			case netdev.JobFailed, netdev.JobAborted:
				snap.Stats.JobFinished++
			}
		}
	}
	if runs, cerr := netdev.ListCutovers(); cerr == nil {
		for _, c := range runs {
			if c.Status == "running" || c.Status == "hold" {
				snap.Inflight.CutoversActive++
			}
		}
		snap.ScenarioCutoverActive = snap.Inflight.CutoversActive > 0
	}
	if st := netdev.HumanTTYStatus(); st != nil {
		snap.Inflight.TerminalsOpen = len(st)
	}
	if run, rerr := netdev.LoadDiscoveryRun(); rerr == nil && run != nil && run.Status == "running" {
		snap.ScenarioDiscoveryRun = true
	}

	// ---- audit（同源三计数 + 链校验缓存）----
	aw := netdev.AuditWindow(30)
	snap.Audit.Read24h = aw.Read24h
	snap.Audit.Write24h = aw.Write24h
	snap.Audit.Guardrail24h = aw.Guardrail24h
	snap.Stats.CmdMix = aw.ByClass
	snap.Stats.AuditEntries = aw.Entries
	st := auditChainStatusCached()
	snap.Audit.ChainOK = st.OK
	snap.Audit.ChainTotal = st.Total
	if tail, terr := a.NetDevAuditTail(1); terr == nil && len(tail) > 0 {
		snap.Audit.LastEntryAt = tail[0].Time
	}

	// ---- stats（L0，§7.1）----
	var resolveSum float64
	resolveN := 0
	for _, f := range findings {
		if f.Status == "resolved" && f.ResolvedAt != nil {
			d := f.ResolvedAt.Sub(f.CreatedAt).Hours()
			if d >= 0 && d < 30*24 { // outliers (clock jumps) stay out of the mean
				resolveSum += d
				resolveN++
			}
		}
	}
	if resolveN > 0 {
		v := resolveSum / float64(resolveN)
		snap.Stats.MTTRHours = &v
	}
	snap.Stats.Baseline = netdev.LoadLastBaseline()
	snap.Stats.DeviceByRole = map[string]int{}
	for _, d := range cfg.NetDev.Devices {
		role, _ := netdev.InferDeviceRole(d)
		key := role
		if key == "" {
			key = "unknown"
		}
		snap.Stats.DeviceByRole[key]++
	}
	snap.Stats.RiskTrend = netdev.ReadInspectionRows(30)
	// 巡检合规：interval 解析成功即视为启用；执行戳来自调度循环。
	if d, perr := time.ParseDuration(strings.TrimSpace(cfg.NetDev.InspectionInterval)); perr == nil && d > 0 {
		c := &NetDevOvCompliance{Enabled: true}
		if st := netdev.LoadScheduleStamp(); st != nil {
			c.LastRunAt, c.Ok, c.Title, c.Note = st.At, st.Ok, st.Title, st.Note
		}
		snap.Stats.InspectionCompliance = c
	}
	// 凭证健康（§7.3 停车场补件）：条数 + 整库最近变更年龄；>90 天提示轮换。
	if n, at := netdev.SecretHealth(); n > 0 || !at.IsZero() {
		ch := &NetDevOvCredHealth{Count: n}
		if !at.IsZero() {
			ch.LastChangedAt = at.Format("2006-01-02")
			ch.AgeDays = int(time.Since(at).Hours() / 24)
			ch.Stale = ch.AgeDays > 90
		}
		snap.Stats.CredHealth = ch
	}

	dashSnapMu.Lock()
	dashSnap, dashSnapAt = snap, time.Now()
	dashSnapMu.Unlock()
	return snap, nil
}

// NetDevOverview is the D1 bridge. force=true (tab activation) bypasses the
// 30s cache.
func (a *App) NetDevOverview(force bool) (*NetDevOverviewSnapshot, error) {
	// 与 NetDevAttackPaths 同规矩：纯读聚合，不设 enabled 闸（关闭时各
	// 存量为空 → 空态快照，前端走引导）。
	return a.buildOverviewData(force)
}

// ---------------------------------------------------------------------------
// 四屏桥（§2.1）
// ---------------------------------------------------------------------------

// NetDevInvestigationChain assembles the 调查链 screen (§4.6).
func (a *App) NetDevInvestigationChain(caseID, findingID string, hours int) (*netdev.InvestigationChain, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return netdev.SharedManager(cfg).BuildInvestigationChain(caseID, findingID, hours), nil
}

// NetDevCutoverBoard assembles the 割接 screen (§4.7). id="" → active run,
// else newest (终态复盘).
func (a *App) NetDevCutoverBoard(id string) (*netdev.CutoverBoard, error) {
	return netdev.BuildCutoverBoard(id), nil
}

// NetDevDiscoveryBoard assembles the 发现 screen (§4.8).
func (a *App) NetDevDiscoveryBoard() (*netdev.DiscoveryBoard, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return netdev.SharedManager(cfg).BuildDiscoveryBoard(), nil
}

// NetDevExposureBoard assembles the 暴露面 screen (§4.9). The simulated
// paths are read-only graph math over existing findings (推演).
func (a *App) NetDevExposureBoard() (*netdev.ExposureBoard, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return netdev.SharedManager(cfg).BuildExposureBoard(), nil
}

// NetDevSyslogCounts surfaces the R3 journal (per-hour/device/class event
// volumes) for the logs tab's stats strip.
func (a *App) NetDevSyslogCounts(limit int) ([]netdev.SyslogCountRow, error) {
	return netdev.SyslogCountTail(limit), nil
}

// NetDevTopoReconcile is the topology tab's 对账卡（offline design↔plan diff
// + neighbor-platform coverage; zero probes).
func (a *App) NetDevTopoReconcile() (*netdev.TopoReconcile, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return netdev.BuildTopoReconcile(cfg), nil
}

// ---------------------------------------------------------------------------
// hiq:netdev-dash 写侧推送（§3.4）——payload 只有屏枚举，无数据本体
// ---------------------------------------------------------------------------

// dashEmit nudges the dash screens after a successful write. Best-effort:
// a failed emit is silent (the 60s fallback poll keeps the screens honest).
func (a *App) dashEmit(screens ...string) {
	if a.ctx == nil {
		return
	}
	defer func() { _ = recover() }() // ctx may be gone during shutdown
	runtime.EventsEmit(a.ctx, "hiq:netdev-dash", map[string]any{"screens": screens})
}
