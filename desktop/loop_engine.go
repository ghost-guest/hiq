package main

// loop_engine.go — 循环工程 (Loop Engineering, docs/loop-engineering-spec.md).
// A supervised agent loop: sensor → problem selection → one agent round →
// verify → rollback-on-fail → bookkeeping, bounded by budgets and circuit
// breakers. MVP scope (spec §7):
//
//   - One active run app-wide; the task QUEUE lives in the frontend, which
//     chains LoopStart calls as runs finish.
//   - Rounds run on the tab that was active at LoopStart time.
//   - Sensor/verify/rollback execute directly (os/exec in the workspace root)
//     — deterministic, no agent in the measurement path.
//   - Budgets: max rounds + per-round wall clock + circuit breakers. Token
//     budget arrives in P2 (needs controller usage plumbing).
//   - Autonomy: L1 read-only (prompt-enforced), L2 per-round verify+rollback.
//     L3 is a UI slot only in the MVP.
//
// Events: every round emits "loop:round" with a full status snapshot so the
// frontend timeline renders from one source of truth.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/proc"
)

// ── wire types (JSON to/from the frontend) ──────────────────────────────────

type LoopConfig struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Goal             string   `json:"goal"`             // what the loop works toward
	SensorCommand    string   `json:"sensorCommand"`    // exploratory problem source ("" = goal-driven)
	VerifyCommand    string   `json:"verifyCommand"`    // acceptance gate, run every round
	Exploratory      bool     `json:"exploratory"`      // sensor feeds the problem queue
	Autonomy         string   `json:"autonomy"`         // "L1" | "L2" | "L3"
	MaxRounds        int      `json:"maxRounds"`        // budget: rounds
	MaxTokens        int      `json:"maxTokens"`        // budget: total tokens (0 = unlimited)
	IntervalSeconds  int      `json:"intervalSeconds"`  // pause between rounds (0 = none)
	CommandAllowlist []string `json:"commandAllowlist"` // L3: allowed command prefixes; empty = built-in safe set
	// Schedule: deferred start (P2, 2026-08-21). Both are wall-clock local time
	// in "HH:MM" (24h). TimeWindow bounds the run: past EndTime the loop stops.
	// StartAt schedules the first round (empty = start immediately).
	StartAt   string `json:"startAt,omitempty"`   // "23:00" — defer until this time
	EndTime   string `json:"endTime,omitempty"`   // "07:00" — hard stop at this time
}

type LoopRoundRecord struct {
	Round      int      `json:"round"`
	Problem    string   `json:"problem,omitempty"`
	Changed    []string `json:"changed,omitempty"`
	Verify     string   `json:"verify"` // "pass" | "fail-rolled-back" | "skipped"
	Note       string   `json:"note,omitempty"`
	DurationMs int64    `json:"durationMs"`
}

type LoopReport struct {
	RoundsRun    int    `json:"roundsRun"`
	Passed       int    `json:"passed"`
	RolledBack   int    `json:"rolledBack"`
	Skipped      int    `json:"skipped"`
	ChangedFiles int    `json:"changedFiles"`
	LastVerify   string `json:"lastVerify"`
	Headline     string `json:"headline"`
	Suggestion   string `json:"suggestion,omitempty"`
}

type LoopRunStatus struct {
	RunID     string            `json:"runId"`
	Config    LoopConfig        `json:"config"`
	// Target anchors: the loop acts on ONE project — recorded at start so the
	// panel can show it and audits can reproduce it even after tab switches.
	WorkspaceRoot string `json:"workspaceRoot"`
	TabLabel      string `json:"tabLabel"`
	State     string            `json:"state"` // running|stopping|done|aborted|failed
	Round     int               `json:"round"`
	StartedAt int64             `json:"startedAt"`
	EndedAt   int64             `json:"endedAt,omitempty"`
	StopNote  string            `json:"stopNote,omitempty"`
	TokensUsed int              `json:"tokensUsed"` // cumulative across rounds (P2)
	Timeline  []LoopRoundRecord `json:"timeline"`
	Report    *LoopReport       `json:"report,omitempty"`
}

// ── engine state ─────────────────────────────────────────────────────────────

type loopRun struct {
	mu       sync.Mutex
	status   LoopRunStatus
	tabID    string
	cwd      string
	stopCh   chan struct{}
	stopOnce sync.Once
	// consecutive no-progress rounds (fail-rolled-back) — breaker trips at 3.
	noProgress int
	// consecutive clean exploratory scans — 2 ⇒ natural completion.
	cleanScans int
	// token accumulator (P2): each round's LastUsage().TotalTokens is added;
	// MaxTokens > 0 stops the loop when exceeded.
	tokensUsed int
	// endTime, when non-zero, is the hard wall-clock stop (P2 time window).
	endTime time.Time
}

const (
	loopRoundTimeout   = 30 * time.Minute
	loopCmdTimeout     = 3 * time.Minute
	loopPollInterval   = 400 * time.Millisecond
	loopBreakerLimit   = 3
	loopCleanScanLimit = 2
	loopOutputTail     = 4096
)

// LoopStart validates and launches a run on the given tab ("" = the active
// tab). The tab must be a PROJECT session — sensor/verify/rollback run in
// that project's root, and the agent rounds submit to that tab's controller,
// so the loop stays anchored to one project even if the user switches tabs.
func (a *App) LoopStart(tabID string, cfg LoopConfig) error {
	a.mu.RLock()
	runner := a.loopRunState
	a.mu.RUnlock()
	if runner != nil {
		return fmt.Errorf("循环工程已有进行中的任务(先停止或等待完成)")
	}
	if strings.TrimSpace(cfg.VerifyCommand) == "" && cfg.Autonomy != "L1" {
		return fmt.Errorf("L2 及以上需要验收命令")
	}
	if cfg.MaxRounds <= 0 {
		cfg.MaxRounds = 30
	}
	ctrl := a.ctrlByTabID(tabID)
	if ctrl == nil {
		return fmt.Errorf("目标会话不存在或未就绪")
	}
	a.mu.RLock()
	tab := a.tabLockedByID(tabID)
	tabLabel := ""
	cwd := ""
	if tab != nil {
		tabLabel = tab.Label
		cwd = tab.WorkspaceRoot
		// Loop is a CODING-profile facility: sensor/verify/rollback run raw
		// shell in the project root (loopCommand bypasses the tool registry),
		// which would punch through the netdev structural read-only seal and
		// the cowork tool policy. Frontend hides the entry outside dev; this
		// is the hard backend refusal.
		if normalizeProfileName(tab.profile) != config.ProfileDev {
			a.mu.RUnlock()
			return fmt.Errorf("循环工程仅在编码模式可用(目标会话属于%s模式)", profileDisplayName(tab.profile))
		}
	}
	a.mu.RUnlock()
	if cwd == "" || cwd == "." {
		return fmt.Errorf("循环工程需要项目会话作为目标(当前是全局会话,请在项目工作区下的会话中运行)")
	}
	run := &loopRun{
		status: LoopRunStatus{
			RunID:         fmt.Sprintf("loop-%d", time.Now().UnixMilli()),
			Config:        cfg,
			WorkspaceRoot: cwd,
			TabLabel:      tabLabel,
			State:         "running",
			StartedAt:     time.Now().UnixMilli(),
			Timeline:      []LoopRoundRecord{},
		},
		tabID:  tabID,
		cwd:    cwd,
		stopCh: make(chan struct{}),
	}
	// P2 time window: parse "HH:MM" end time into the hard stop deadline.
	if cfg.EndTime != "" {
		if t, err := time.ParseInLocation("15:04", cfg.EndTime, time.Local); err == nil {
			now := time.Now()
			end := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.Local)
			if end.Before(now) {
				end = end.Add(24 * time.Hour) // e.g. 23:00→07:00 crosses midnight
			}
			run.endTime = end
		}
	}
	a.setLoopRun(run)
	go a.loopExecute(run, ctrl)
	return nil
}

// tabLockedByID resolves a tab by id; callers must hold mu (read is enough).
func (a *App) tabLockedByID(tabID string) *WorkspaceTab {
	if tabID == "" {
		return a.activeTabLocked()
	}
	return a.tabs[tabID]
}

// LoopStop requests a graceful stop: the current round finishes (and is
// verified/rolled back) before the run ends.
func (a *App) LoopStop(reason string) {
	a.mu.RLock()
	run := a.loopRunState
	a.mu.RUnlock()
	if run == nil {
		return
	}
	run.stopOnce.Do(func() { close(run.stopCh) })
	run.mu.Lock()
	if run.status.State == "running" {
		run.status.State = "stopping"
		run.status.StopNote = reason
	}
	run.mu.Unlock()
	a.loopEmit(run)
}

// LoopStatus returns the active (or last, pre-clear) run snapshot; nil when
// no run has ever started. The frontend also receives full snapshots via the
// "loop:round" event, so this is mostly for initial load.
func (a *App) LoopStatus() *LoopRunStatus {
	a.mu.RLock()
	run := a.loopRunState
	a.mu.RUnlock()
	if run == nil {
		return nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	snapshot := run.status
	snapshot.Timeline = append([]LoopRoundRecord(nil), run.status.Timeline...)
	return &snapshot
}

func (a *App) setLoopRun(run *loopRun) {
	a.mu.Lock()
	a.loopRunState = run
	a.mu.Unlock()
}

// ── execution ────────────────────────────────────────────────────────────────

func (a *App) loopExecute(run *loopRun, ctrl tabSession) {
	cfg := run.status.Config
	defer func() {
		run.mu.Lock()
		if run.status.State == "running" || run.status.State == "stopping" {
			run.status.State = "done"
		}
		run.status.EndedAt = time.Now().UnixMilli()
		run.status.Report = buildLoopReport(run)
		run.mu.Unlock()
		a.setLoopRun(nil)
		a.loopEmit(run)
	}()

	for round := 1; round <= cfg.MaxRounds; round++ {
		if a.loopShouldStop(run) {
			return
		}
		// P2 time window: past endTime, stop with a clear note.
		if !run.endTime.IsZero() && time.Now().After(run.endTime) {
			run.mu.Lock()
			run.status.State = "done"
			run.status.StopNote = fmt.Sprintf("时间窗结束(%s)", cfg.EndTime)
			run.mu.Unlock()
			return
		}
		// P2 token budget: exceeded → stop with a clear note.
		if cfg.MaxTokens > 0 && run.tokensUsed >= cfg.MaxTokens {
			run.mu.Lock()
			run.status.State = "done"
			run.status.StopNote = fmt.Sprintf("token 预算耗尽(%d/%d)", run.tokensUsed, cfg.MaxTokens)
			run.mu.Unlock()
			return
		}
		started := time.Now()
		rec := LoopRoundRecord{Round: round, Verify: "skipped"}

		// 1) Sensor — the exploratory problem source. Goal-driven loops skip
		//    it and let the agent work the user's goal directly.
		sensorOut := ""
		if cfg.Exploratory && strings.TrimSpace(cfg.SensorCommand) != "" {
			out, err := a.loopCommand(run, cfg.SensorCommand)
			if err != nil {
				rec.Note = "传感器执行失败: " + err.Error()
			} else {
				sensorOut = out
				if strings.TrimSpace(out) == "" {
					run.mu.Lock()
					run.cleanScans++
					cs := run.cleanScans
					run.mu.Unlock()
					if cs >= loopCleanScanLimit {
						rec.Note = fmt.Sprintf("连续 %d 轮未发现新问题,循环完成", cs)
						a.loopAppend(run, rec, started)
						run.mu.Lock()
						run.status.State = "done"
						run.status.StopNote = "问题队列清空"
						run.mu.Unlock()
						return
					}
				} else {
					run.mu.Lock()
					run.cleanScans = 0
					run.mu.Unlock()
				}
			}
		}
		if a.loopShouldStop(run) {
			a.loopAppend(run, rec, started)
			return
		}

		// 2) Agent round — the prompt carries the sensor output, the goal,
		//    the acceptance contract, and the output discipline.
		ctrl.Submit(loopRoundPrompt(cfg, round, sensorOut))
		if !a.loopWaitTurn(run, ctrl) {
			a.loopAppend(run, rec, started)
			return // stopped mid-round
		}

		// 3) Verify (L1 is read-only — nothing to verify).
		if cfg.Autonomy == "L1" || strings.TrimSpace(cfg.VerifyCommand) == "" {
			rec.Verify = "skipped"
			rec.Note = "L1 巡检:只读轮次"
		} else {
			rec.Changed = a.loopChangedFiles(run)
			if _, err := a.loopCommand(run, cfg.VerifyCommand); err == nil {
				rec.Verify = "pass"
				run.mu.Lock()
				run.noProgress = 0
				run.mu.Unlock()
			} else {
				// 4) Roll the round back — the anti-degradation guarantee:
				//    a night's worst outcome is "wasted", never "worse".
				if _, rbErr := a.loopCommand(run, "git checkout -- ."); rbErr != nil {
					rec.Verify = "fail-rolled-back"
					rec.Note = "验证失败且回滚失败: " + rbErr.Error()
				} else {
					rec.Verify = "fail-rolled-back"
				}
				run.mu.Lock()
				run.noProgress++
				np := run.noProgress
				run.mu.Unlock()
				if np >= loopBreakerLimit {
					rec.Note = appendNote(rec.Note, fmt.Sprintf("熔断:连续 %d 轮无进展", np))
					a.loopAppend(run, rec, started)
					run.mu.Lock()
					run.status.State = "aborted"
					run.mu.Unlock()
					return
				}
			}
		}
		// P2 token budget: accumulate this round's usage.
		if lu := ctrl.LastUsage(); lu != nil && lu.TotalTokens > 0 {
			run.mu.Lock()
			run.tokensUsed += lu.TotalTokens
			run.status.TokensUsed = run.tokensUsed
			run.mu.Unlock()
		}

		a.loopAppend(run, rec, started)

		// 5) Goal-driven loops stop at the first full pass.
		if !cfg.Exploratory && rec.Verify == "pass" {
			run.mu.Lock()
			run.status.State = "done"
			run.status.StopNote = "验收通过,目标达成"
			run.mu.Unlock()
			return
		}

		// 6) Inter-round cooldown.
		if cfg.IntervalSeconds > 0 {
			select {
			case <-run.stopCh:
				return
			case <-time.After(time.Duration(cfg.IntervalSeconds) * time.Second):
			}
		}
	}
	run.mu.Lock()
	run.status.State = "done"
	run.status.StopNote = fmt.Sprintf("达到最大轮次(%d)", cfg.MaxRounds)
	run.mu.Unlock()
}

// loopWaitTurn blocks until the controller's turn ends, the stop signal
// arrives, or the per-round timeout trips. Returns false when stopped.
func (a *App) loopWaitTurn(run *loopRun, ctrl tabSession) bool {
	// Give the turn a moment to register as running before we poll for idle.
	time.Sleep(2 * time.Second)
	deadline := time.Now().Add(loopRoundTimeout)
	for {
		if a.loopShouldStop(run) {
			return false
		}
		if time.Now().After(deadline) {
			return true // treat timeout as round end; verify will judge
		}
		if !ctrl.Running() {
			return true
		}
		time.Sleep(loopPollInterval)
	}
}

func (a *App) loopShouldStop(run *loopRun) bool {
	select {
	case <-run.stopCh:
		return true
	default:
		return false
	}
}

func (a *App) loopAppend(run *loopRun, rec LoopRoundRecord, started time.Time) {
	rec.DurationMs = time.Since(started).Milliseconds()
	run.mu.Lock()
	run.status.Round = rec.Round
	run.status.Timeline = append(run.status.Timeline, rec)
	run.mu.Unlock()
	a.loopEmit(run)
}

// loopCommand runs shell text in the run's cwd, returning combined output.
func (a *App) loopCommand(run *loopRun, command string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), loopCmdTimeout)
	defer cancel()
	var cmd *exec.Cmd
	if goruntime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
		proc.HideWindow(cmd)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = run.cwd
	cmd.Env = append(os.Environ(), "CI=1")
	out, err := cmd.CombinedOutput()
	return tailLoop(string(out), loopOutputTail), err
}

// loopChangedFiles lists files modified this round (git porcelain, first 10).
func (a *App) loopChangedFiles(run *loopRun) []string {
	out, err := a.loopCommand(run, "git status --porcelain")
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// porcelain: "XY path" — strip the two status columns.
		if len(line) > 3 {
			files = append(files, strings.TrimSpace(line[3:]))
		}
		if len(files) >= 10 {
			break
		}
	}
	return files
}

// ── prompt ───────────────────────────────────────────────────────────────────

func loopRoundPrompt(cfg LoopConfig, round int, sensorOut string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("[循环工程 · 第 %d/%d 轮]\n", round, cfg.MaxRounds))
	if cfg.Autonomy == "L1" {
		b.WriteString("模式:只读巡检。本轮只允许读取与只读命令(查看/测试跑批/lint 检查),禁止任何文件写入或修改。产出:发现的问题清单与建议,不动手修。\n")
	} else {
		b.WriteString("模式:自主修复。\n")
	}
	b.WriteString("\n目标:\n" + cfg.Goal + "\n")
	if sensorOut != "" {
		b.WriteString("\n本轮传感器输出(问题源,挑最值得处理的一项):\n" + tailLoop(sensorOut, 3000) + "\n")
	}
		if strings.TrimSpace(cfg.VerifyCommand) != "" && cfg.Autonomy != "L1" {
			b.WriteString("\n纪律:\n" +
				"1. 只处理选定问题,顺手小改可以,禁止无关重构;\n" +
				"2. 完成后必须运行验收命令:`" + cfg.VerifyCommand + "`,并贴出关键输出;\n" +
				"3. 若验收不过且你判断无法本轮解决,说明卡点后结束本轮(外部会回滚你的改动并记录);\n" +
				"4. 结尾用一行总结:本轮做了什么、验收结果。\n")
			// L3: command allowlist narrows the agent's shell surface (spec §6.5).
			if cfg.Autonomy == "L3" && len(cfg.CommandAllowlist) > 0 {
				b.WriteString("\n命令白名单(L3 隔夜档,仅允许这些前缀的命令):\n")
				for _, prefix := range cfg.CommandAllowlist {
					b.WriteString("  - " + prefix + "\n")
				}
				b.WriteString("白名单外的命令会被外部拦截并记录,请勿尝试。\n")
			}
		} else {
		b.WriteString("\n纪律:结束时给出一行总结(发现了什么/建议下一步)。\n")
	}
	return b.String()
}

// ── report & events ─────────────────────────────────────────────────────────

func buildLoopReport(run *loopRun) *LoopReport {
	run.mu.Lock()
	defer run.mu.Unlock()
	r := &LoopReport{RoundsRun: len(run.status.Timeline), LastVerify: "未运行"}
	changed := map[string]bool{}
	for _, rec := range run.status.Timeline {
		switch rec.Verify {
		case "pass":
			r.Passed++
			r.LastVerify = "通过"
		case "fail-rolled-back":
			r.RolledBack++
			r.LastVerify = "失败(已回滚)"
		default:
			r.Skipped++
		}
		for _, f := range rec.Changed {
			changed[f] = true
		}
	}
	r.ChangedFiles = len(changed)
	if run.status.State == "done" {
		r.Headline = fmt.Sprintf("完成:%d 轮 · 通过 %d · 回滚 %d", r.RoundsRun, r.Passed, r.RolledBack)
	} else {
		r.Headline = fmt.Sprintf("中止(%s):%d 轮 · 通过 %d · 回滚 %d", run.status.StopNote, r.RoundsRun, r.Passed, r.RolledBack)
	}
	if r.RolledBack > 0 {
		r.Suggestion = "存在回滚轮次,建议查看时间线中的失败原因后调整目标或验收命令"
	}
	return r
}

func (a *App) loopEmit(run *loopRun) {
	run.mu.Lock()
	snapshot := run.status
	snapshot.Timeline = append([]LoopRoundRecord(nil), run.status.Timeline...)
	run.mu.Unlock()
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "loop:round", snapshot)
	}
}

func appendNote(note, add string) string {
	if note == "" {
		return add
	}
	return note + ";" + add
}

func tailLoop(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-max:]
}
