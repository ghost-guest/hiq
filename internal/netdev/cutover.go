package netdev

// cutover.go — 割接模式（NETDEV_SPEC_V2 §7.2）：整份 runbook 带总倒计时逐
// 步执行。步骤要么执行一份「已批准」提案（变更动作的唯一形态），要么跑
// 一条只读命令；步后可挂语义验证门（Expect 持续 SustainSec 才算过），门
// 不过或到预设回退决策点即 hold——回退按钮与影响描述并列，决策是人按
// 的。割接前后各拍一次基线快照（复用配置备份库），结束时产出前后对比
// 报告。把「Word 文档 + 对讲机」的深夜割接变成可执行、可回退、可复盘
// 的流程。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cutover statuses.
const (
	CutoverRunning = "running"
	CutoverHold    = "hold" // stopped at a decision point / failed gate / countdown spent
	CutoverDone    = "done"
	CutoverFailed  = "failed"
	CutoverAborted = "aborted"
)

// Cutover step statuses.
const (
	CutoverStepPending  = "pending"
	CutoverStepRunning  = "running"
	CutoverStepGating   = "gating"
	CutoverStepDone     = "done"
	CutoverStepFailed   = "failed"
	CutoverStepSkipped  = "skipped"
	CutoverStepRolled   = "rolled-back"
	CutoverStepApproved = "approved" // a proposal step that finished its change
)

// CutoverGate is a semantic verification gate: Command's output must match
// Expect CONTINUOUSLY for SustainSec (e.g. "OSPF 邻居 Full 且持续 60s").
type CutoverGate struct {
	Device     string `json:"device"`
	Command    string `json:"command"`
	Expect     string `json:"expect"`
	SustainSec int    `json:"sustain_sec,omitempty"` // default 30
	TimeoutSec int    `json:"timeout_sec,omitempty"` // default 2×sustain+90
}

// CutoverStep is one runbook entry.
type CutoverStep struct {
	Label         string       `json:"label"`
	EstSec        int          `json:"est_sec,omitempty"`     // 预计耗时（倒计时排程显示）
	ProposalID    string       `json:"proposal_id,omitempty"` // execute an APPROVED proposal
	Device        string       `json:"device,omitempty"`      // …or one sealed read command
	Command       string       `json:"command,omitempty"`
	Gate          *CutoverGate `json:"gate,omitempty"`           // post-step verification gate
	DecisionPoint bool         `json:"decision_point,omitempty"` // 回退决策点：完成后 hold 等人
	Impact        string       `json:"impact,omitempty"`         // 影响描述（决策点并列展示）

	Status    string     `json:"status"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Output    string     `json:"output,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// CutoverRun is one cutover execution.
type CutoverRun struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Deadline time.Time     `json:"deadline"` // 总倒计时（割接窗口结束）
	Steps    []CutoverStep `json:"steps"`
	Status   string        `json:"status"`
	HoldNote string        `json:"hold_note,omitempty"`
	Cursor   int           `json:"cursor"`

	// 割接前后基线快照（device → backup id）+ 对比报告。
	PreSnapshot  map[string]string `json:"pre_snapshot,omitempty"`
	PostSnapshot map[string]string `json:"post_snapshot,omitempty"`
	Report       string            `json:"report,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// cutoversDirOverride isolates cutover storage in tests.
var cutoversDirOverride string

func cutoversDir() string {
	if cutoversDirOverride != "" {
		return cutoversDirOverride
	}
	return filepath.Join(netdevStateDir(), "cutovers")
}

var (
	cutoverMu  sync.Mutex
	cutoverSeq int
)

func newCutoverID() string {
	cutoverMu.Lock()
	defer cutoverMu.Unlock()
	cutoverSeq++
	return fmt.Sprintf("C%s-%d", time.Now().Format("20060102"), cutoverSeq)
}

func saveCutover(c *CutoverRun) error {
	if err := os.MkdirAll(cutoversDir(), 0o700); err != nil {
		return err
	}
	cutoverMu.Lock()
	defer cutoverMu.Unlock()
	return saveCutoverLocked(c)
}

// saveCutoverLocked is saveCutover with the caller holding cutoverMu — the
// write half of an atomic load→check→set→save transition.
func saveCutoverLocked(c *CutoverRun) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cutoversDir(), c.ID+".json"), b, 0o600)
}

// GetCutover loads one run.
func GetCutover(id string) (*CutoverRun, error) {
	b, err := os.ReadFile(filepath.Join(cutoversDir(), id+".json"))
	if err != nil {
		return nil, err
	}
	var c CutoverRun
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// ListCutovers returns runs newest-first.
func ListCutovers() ([]*CutoverRun, error) {
	entries, err := os.ReadDir(cutoversDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*CutoverRun
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if c, err := GetCutover(strings.TrimSuffix(e.Name(), ".json")); err == nil {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CreatedAt.After(out[k].CreatedAt) })
	return out, nil
}

// ── runner registry ──────────────────────────────────────────────────────────

var (
	cutoverRunsMu sync.Mutex
	cutoverRuns   = map[string]context.CancelFunc{}
)

// CutoverStart validates the runbook, snapshots the network baseline, and
// launches the runner. Every proposal a step references must already be
// approved — the cutover executes the approved change, it never approves.
func (m *Manager) CutoverStart(def *CutoverRun) (*CutoverRun, error) {
	if def.ID != "" {
		return nil, fmt.Errorf("cutover: new runs take no ID (assigned here)")
	}
	if strings.TrimSpace(def.Name) == "" {
		return nil, fmt.Errorf("cutover: name is required")
	}
	if len(def.Steps) == 0 {
		return nil, fmt.Errorf("cutover %q: no steps", def.Name)
	}
	if def.Deadline.IsZero() || !def.Deadline.After(time.Now()) {
		return nil, fmt.Errorf("cutover %q: deadline must be in the future (总倒计时)", def.Name)
	}
	devices := map[string]bool{}
	for i := range def.Steps {
		s := &def.Steps[i]
		if s.Label == "" {
			s.Label = fmt.Sprintf("step-%d", i+1)
		}
		if s.EstSec <= 0 {
			s.EstSec = 60
		}
		switch {
		case s.ProposalID != "":
			p, err := GetProposal(s.ProposalID)
			if err != nil {
				return nil, fmt.Errorf("cutover step %q: proposal %s: %v", s.Label, s.ProposalID, err)
			}
			if p.Status != ProposalApproved {
				return nil, fmt.Errorf("cutover step %q: proposal %s is %s — approve it before the cutover starts", s.Label, s.ProposalID, p.Status)
			}
			for _, st := range p.Steps {
				devices[st.Device] = true
			}
		case s.Device != "" && s.Command != "":
			if strings.ContainsAny(s.Command, "\n\r\x00") {
				return nil, fmt.Errorf("cutover step %q: one command per step", s.Label)
			}
			if _, ok := m.cfg.NetDevDeviceByName(s.Device); !ok {
				return nil, fmt.Errorf("cutover step %q: device %q not in inventory", s.Label, s.Device)
			}
			devices[s.Device] = true
		default:
			return nil, fmt.Errorf("cutover step %q: needs proposal_id or device+command", s.Label)
		}
		if s.Gate != nil {
			if s.Gate.Device == "" || s.Gate.Command == "" || s.Gate.Expect == "" {
				return nil, fmt.Errorf("cutover step %q: gate needs device+command+expect", s.Label)
			}
			if _, err := regexp.Compile(s.Gate.Expect); err != nil {
				return nil, fmt.Errorf("cutover step %q: gate expect: %v", s.Label, err)
			}
			if s.Gate.SustainSec <= 0 {
				s.Gate.SustainSec = 30
			}
		}
		s.Status = CutoverStepPending
	}

	def.ID = newCutoverID()
	StateEventSnap(StateEventCutoverStart, def.ID, StateActorUser, filepath.Join(cutoversDir(), def.ID+".json"))
	def.Status = CutoverRunning
	def.CreatedAt = time.Now()
	now := time.Now()
	def.StartedAt = &now
	def.Cursor = 0

	// 割接前全网基线快照（§7.2：割接前后自动各拍一次）。
	def.PreSnapshot = map[string]string{}
	for name := range devices {
		d, ok := m.cfg.NetDevDeviceByName(name)
		if !ok {
			continue
		}
		if bc := backupCommand(drvKey(d)); bc == "" {
			continue // snapshots cover config-bearing network devices
		}
		vers, err := m.RunBackup(context.Background(), name)
		if err != nil || len(vers) == 0 {
			continue // snapshot best-effort; the report notes what it missed
		}
		def.PreSnapshot[name] = vers[len(vers)-1].ID
	}

	if err := saveCutover(def); err != nil {
		return nil, err
	}
	_ = AppendAudit(Audit{Device: "(cutover)", Command: "start " + def.ID + " " + def.Name, Class: "cutover", Status: AuditOK})
	m.cutoverLaunch(def.ID)
	return def, nil
}

func (m *Manager) cutoverLaunch(id string) {
	ctx, cancel := context.WithCancel(context.Background())
	cutoverRunsMu.Lock()
	if old, ok := cutoverRuns[id]; ok {
		old()
	}
	cutoverRuns[id] = cancel
	cutoverRunsMu.Unlock()
	go m.cutoverRunner(ctx, id)
}

// CutoverContinue presses 继续 at a hold.
func (m *Manager) CutoverContinue(id string) (*CutoverRun, error) {
	cutoverMu.Lock()
	c, err := GetCutover(id)
	if err != nil {
		cutoverMu.Unlock()
		return nil, err
	}
	if c.Status != CutoverHold {
		cutoverMu.Unlock()
		return nil, fmt.Errorf("cutover %s: status %s — only held cutovers continue", id, c.Status)
	}
	StateEventSnap(StateEventCutoverGo, id, StateActorUser, filepath.Join(cutoversDir(), id+".json"))
	c.Status = CutoverRunning
	c.HoldNote = ""
	if err := saveCutoverLocked(c); err != nil {
		cutoverMu.Unlock()
		return nil, err
	}
	cutoverMu.Unlock()
	_ = AppendAudit(Audit{Device: "(cutover)", Command: "continue " + id, Class: "cutover", Status: AuditOK})
	m.cutoverLaunch(id)
	return c, nil
}

// CutoverRollback presses 回退 at a hold: the run's executed proposals unwind
// newest-first (each still audited); the run ends aborted.
func (m *Manager) CutoverRollback(ctx context.Context, id string) (*CutoverRun, error) {
	c, err := GetCutover(id)
	if err != nil {
		return nil, err
	}
	if c.Status != CutoverHold {
		return nil, fmt.Errorf("cutover %s: status %s — rollback happens at a decision point", id, c.Status)
	}
	StateEventSnap(StateEventCutoverBack, id, StateActorUser, filepath.Join(cutoversDir(), id+".json"))
	failed := ""
	for i := len(c.Steps) - 1; i >= 0; i-- {
		s := &c.Steps[i]
		if s.Status != CutoverStepApproved && s.Status != CutoverStepDone {
			continue
		}
		if s.ProposalID == "" {
			continue // read steps leave nothing to roll back
		}
		if _, err := m.RollbackProposal(ctx, s.ProposalID); err != nil {
			failed = fmt.Sprintf("%s: %v", s.ProposalID, err)
			s.Error = failed
			break
		}
		s.Status = CutoverStepRolled
		_ = saveCutover(c)
	}
	if failed != "" {
		c.Status = CutoverFailed
		c.HoldNote = "回退失败：" + failed + " — 人工接管（备份在提案里）"
		_ = saveCutover(c)
		return c, nil
	}
	c.Status = CutoverAborted
	c.HoldNote = "已按决策点回退"
	m.cutoverFinishReport(context.Background(), c)
	return c, nil
}

// CutoverAbort stops everything for good (running or held).
func (m *Manager) CutoverAbort(id string) (*CutoverRun, error) {
	cutoverMu.Lock()
	c, err := GetCutover(id)
	if err != nil {
		cutoverMu.Unlock()
		return nil, err
	}
	if c.Status != CutoverRunning && c.Status != CutoverHold {
		cutoverMu.Unlock()
		return nil, fmt.Errorf("cutover %s: status %s", id, c.Status)
	}
	StateEventSnap(StateEventCutoverAbort, id, StateActorUser, filepath.Join(cutoversDir(), id+".json"))
	cutoverRunsMu.Lock()
	if cancel, ok := cutoverRuns[id]; ok {
		cancel()
	}
	cutoverRunsMu.Unlock()
	c.Status = CutoverAborted
	c.HoldNote = "人工终止"
	for i := range c.Steps {
		if c.Steps[i].Status == CutoverStepPending {
			c.Steps[i].Status = CutoverStepSkipped
		}
	}
	now := time.Now()
	c.EndedAt = &now
	if err := saveCutoverLocked(c); err != nil {
		cutoverMu.Unlock()
		return nil, err
	}
	cutoverMu.Unlock()
	_ = AppendAudit(Audit{Device: "(cutover)", Command: "abort " + id, Class: "cutover", Status: AuditFailure})
	return c, nil
}

// ── the runner ───────────────────────────────────────────────────────────────

func (m *Manager) cutoverRunner(ctx context.Context, id string) {
	defer func() {
		cutoverRunsMu.Lock()
		delete(cutoverRuns, id)
		cutoverRunsMu.Unlock()
	}()
	for {
		c, err := GetCutover(id)
		if err != nil || c.Status != CutoverRunning {
			return
		}

		// 总倒计时：窗口耗尽即 hold——深夜割接最不该发生的是「计划外继续」。
		if time.Now().After(c.Deadline) {
			m.cutoverHold(id, "总倒计时耗尽——剩余步骤未执行，等待决策")
			return
		}
		if c.Cursor >= len(c.Steps) {
			m.cutoverFinish(id)
			return
		}
		step := c.Steps[c.Cursor]

		// Execute the step.
		state, gateErr := m.cutoverExecStep(ctx, c, step)

		c, err = GetCutover(id)
		if err != nil || c.Status != CutoverRunning {
			return
		}
		if c.Cursor < len(c.Steps) {
			c.Steps[c.Cursor] = state
		}

		if gateErr != nil {
			// 门不过即停在回退决策点（§7.2）：回退按钮 + 影响描述并列。
			impact := step.Impact
			if impact == "" {
				impact = "步骤 " + step.Label + " 验证未通过"
			}
			c.HoldNote = "验证门未过：" + gateErr.Error() + " — " + impact
			c.Status = CutoverHold
			_ = saveCutover(c)
			_ = AppendAudit(Audit{Device: "(cutover)", Command: "hold " + id + " @" + step.Label, Class: "cutover", Status: AuditFailure, Error: gateErr.Error()})
			// 深链召回（§4.12）：半夜窗口期的"回来决策"——IM 推送带
			// hiq://cutover/<id>，点开直达割接大屏（无出口配置时静默）。
			NotifyPushText("cutover", "[hiq 运维] 割接验证门未过："+c.Name,
				"步骤 "+step.Label+" 验证未过，已停在回退决策点。\n"+c.HoldNote+"\nhiq://cutover/"+c.ID)
			return
		}

		c.Cursor++
		if step.DecisionPoint {
			impact := step.Impact
			if impact == "" {
				impact = "决策点 " + step.Label
			}
			c.HoldNote = "决策点：" + impact + " — 继续 or 回退，决策是人按的"
			c.Status = CutoverHold
			_ = saveCutover(c)
			NotifyPushText("cutover", "[hiq 运维] 割接到达决策点："+c.Name,
				c.HoldNote+"\nhiq://cutover/"+c.ID)
			_ = AppendAudit(Audit{Device: "(cutover)", Command: "hold " + id + " @" + step.Label + " (decision)", Class: "cutover", Status: AuditOK})
			return
		}
		if err := saveCutover(c); err != nil {
			return
		}
	}
}

// cutoverExecStep runs one step (change or read) plus its gate.
func (m *Manager) cutoverExecStep(ctx context.Context, c *CutoverRun, step CutoverStep) (CutoverStep, error) {
	now := time.Now()
	step.Status = CutoverStepRunning
	step.StartedAt = &now
	step.Error = ""

	if step.ProposalID != "" {
		p, err := m.ExecuteProposal(CtxStateActor(ctx, StateActorSystem), step.ProposalID)
		if err != nil {
			step.Status = CutoverStepFailed
			step.Error = err.Error()
			end := time.Now()
			step.EndedAt = &end
			return step, fmt.Errorf("提案 %s 执行失败: %v", step.ProposalID, err)
		}
		if p.Status == ProposalPartial || p.Status == ProposalFailed {
			step.Status = CutoverStepFailed
			step.Error = "提案 " + step.ProposalID + " 冻结为 " + p.Status + "（首败冻结）"
			end := time.Now()
			step.EndedAt = &end
			return step, fmt.Errorf("%s", step.Error)
		}
		step.Status = CutoverStepApproved
	} else {
		res := m.Exec(ctx, step.Device, step.Command)
		step.Output = tailStr(res.Output, 2048)
		if res.Refused {
			step.Status = CutoverStepFailed
			step.Error = res.Refusal
			end := time.Now()
			step.EndedAt = &end
			return step, fmt.Errorf("命令被拒：%s", res.Refusal)
		}
		if res.IsError {
			step.Status = CutoverStepFailed
			step.Error = "device error: " + firstLine(res.Output)
			end := time.Now()
			step.EndedAt = &end
			return step, fmt.Errorf("%s", step.Error)
		}
		step.Status = CutoverStepDone
	}

	if step.Gate != nil {
		step.Status = CutoverStepGating
		if err := m.gateWait(ctx, step.Gate); err != nil {
			step.Status = CutoverStepFailed
			step.Error = "gate: " + err.Error()
			end := time.Now()
			step.EndedAt = &end
			return step, err
		}
		if step.Status == CutoverStepApproved {
			// keep proposal semantics
		} else {
			step.Status = CutoverStepDone
		}
	}
	end := time.Now()
	step.EndedAt = &end
	return step, nil
}

// gateWait polls the gate command until Expect matches CONTINUOUSLY for
// SustainSec. Timeout = TimeoutSec, default 2×sustain + 90s.
func (m *Manager) gateWait(ctx context.Context, g *CutoverGate) error {
	re := regexp.MustCompile(g.Expect)
	sustain := time.Duration(g.SustainSec) * time.Second
	timeout := time.Duration(g.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = sustain*2 + 90*time.Second
	}
	interval := sustain / 4
	if interval < time.Second {
		interval = time.Second
	}
	deadline := time.Now().Add(timeout)
	var matchedSince time.Time
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		res := m.Exec(ctx, g.Device, g.Command)
		ok := !res.Refused && !res.IsError && re.MatchString(res.Output)
		if ok {
			if matchedSince.IsZero() {
				matchedSince = time.Now()
			}
			if time.Since(matchedSince) >= sustain {
				return nil
			}
		} else {
			matchedSince = time.Time{} // continuity broken — restart the window
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s %q 在 %v 内未持续 %v 匹配 %q", g.Device, g.Command, timeout, sustain, g.Expect)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (m *Manager) cutoverHold(id, note string) {
	cutoverMu.Lock()
	defer cutoverMu.Unlock()
	c, err := GetCutover(id)
	if err != nil || c.Status != CutoverRunning {
		return
	}
	StateEventSnap(StateEventCutoverHold, id, StateActorSystem, filepath.Join(cutoversDir(), id+".json"))
	c.Status = CutoverHold
	c.HoldNote = note
	_ = saveCutoverLocked(c)
	_ = AppendAudit(Audit{Device: "(cutover)", Command: "hold " + id, Class: "cutover", Status: AuditFailure, Error: note})
}

// cutoverFinish completes the run: post-snapshot + before/after report.
func (m *Manager) cutoverFinish(id string) {
	c, err := GetCutover(id)
	if err != nil || c.Status != CutoverRunning {
		return
	}
	m.cutoverFinishReport(context.Background(), c)
}

func (m *Manager) cutoverFinishReport(ctx context.Context, c *CutoverRun) {
	c.PostSnapshot = map[string]string{}
	for name := range c.PreSnapshot {
		vers, err := m.RunBackup(ctx, name)
		if err != nil || len(vers) == 0 {
			continue
		}
		c.PostSnapshot[name] = vers[len(vers)-1].ID
	}
	c.Report = m.cutoverReport(c)
	c.Status = CutoverDone
	if c.HoldNote == "已按决策点回退" {
		c.Status = CutoverAborted
	}
	now := time.Now()
	c.EndedAt = &now
	cutoverMu.Lock()
	StateEventSnap(StateEventCutoverDone, c.ID, StateActorSystem, filepath.Join(cutoversDir(), c.ID+".json"))
	_ = saveCutoverLocked(c)
	cutoverMu.Unlock()
	_ = AppendAudit(Audit{Device: "(cutover)", Command: "end " + c.ID + " " + c.Status, Class: "cutover", Status: AuditOK})
}

// cutoverReport builds the before/after comparison (§7.2: 哪些接口/路由/流量变了).
func (m *Manager) cutoverReport(c *CutoverRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 割接对比报告 %s — %s\n\n", c.ID, c.Name)
	fmt.Fprintf(&b, "- 开始：%s\n", c.StartedAt.Format("15:04:05"))
	if c.EndedAt != nil {
		fmt.Fprintf(&b, "- 结束：%s（%s）\n", c.EndedAt.Format("15:04:05"), c.Status)
	}
	fmt.Fprintf(&b, "- 步骤：%d 项\n\n", len(c.Steps))
	for _, s := range c.Steps {
		mark := map[string]string{
			CutoverStepApproved: "✅", CutoverStepDone: "✅", CutoverStepFailed: "❌",
			CutoverStepRolled: "↩️", CutoverStepSkipped: "⏭", CutoverStepPending: "⬜",
			CutoverStepRunning: "…", CutoverStepGating: "…",
		}[s.Status]
		fmt.Fprintf(&b, "## %s %s（%s）\n", mark, s.Label, s.Status)
		if s.Error != "" {
			fmt.Fprintf(&b, "- 错误：%s\n", s.Error)
		}
	}
	if len(c.PreSnapshot) == 0 {
		b.WriteString("\n（无基线快照——涉及设备不提供配置备份）\n")
		return b.String()
	}
	b.WriteString("\n## 前后配置对比\n")
	for name, preID := range c.PreSnapshot {
		postID, ok := c.PostSnapshot[name]
		if !ok {
			fmt.Fprintf(&b, "\n### %s\n（割接后快照缺失）\n", name)
			continue
		}
		diff, err := DiffBackups(name, preID, postID)
		if err != nil {
			fmt.Fprintf(&b, "\n### %s\n（对比失败：%v）\n", name, err)
			continue
		}
		if strings.TrimSpace(diff) == "" {
			fmt.Fprintf(&b, "\n### %s\n无变化\n", name)
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n```diff\n%s\n```\n", name, diff)
	}
	return b.String()
}
