package main

// scheduler_app.go exposes the app-level scheduler to the frontend via Wails
// bindings (ListScheduledTasks, CreateScheduledTask, …). The methods mirror the
// scheduler.Scheduler API but use JSON-friendly "View" structs — time fields are
// pre-formatted as "2006-01-02 15:04" strings so the React layer can render them
// directly without a date lib, and omitempty keeps payloads small.
//
// Events:
//   - "scheduler:changed"  emitted on any mutation (create/update/delete/run),
//                          payload-free — the frontend re-lists. This keeps the
//                          task list live across the UI without each component
//                          wiring its own refresh.
//   - "scheduler:notice"  emitted when a task with OutputMode="notify" fires,
//                          payload {name, result} — surfaced as an in-app toast.

import (
	"context"
	"fmt"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/zzycxz/hiq/internal/apihelper"
	"github.com/zzycxz/hiq/internal/scheduler"
	"github.com/zzycxz/hiq/internal/tool/builtin"
)

// TaskView is the JSON-friendly projection of scheduler.ScheduledTask for the UI.
type TaskView struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Expression     string `json:"expression"`
	Prompt         string `json:"prompt"`
	Profile        string `json:"profile"`
	Enabled        bool   `json:"enabled"`
	OneShot        bool   `json:"oneShot"`
	LastRun        string `json:"lastRun"` // "" if never
	NextRun        string `json:"nextRun"` // "" if paused / one-shot fired
	RunCount       int    `json:"runCount"`
	LastResult     string `json:"lastResult"`
	OutputMode     string `json:"outputMode"`
	OutputDest     string `json:"outputDest"`
	OutputAccount  string `json:"outputAccount"` // named mailbox for "email" mode; "" = default
	OutputDir      string `json:"outputDir"`
	Color          string `json:"color"`
	Location       string `json:"location"`
	Plain          bool   `json:"plain"`          // 纯提醒：到点直接弹原文，不调 AI
	LastDeliverErr string `json:"lastDeliverErr"` // "" if last delivery was ok / skipped
	LastDeliverAt  string `json:"lastDeliverAt"`  // "" if never delivered
	HumanSchedule  string `json:"humanSchedule"`  // friendly description, e.g. "每天 18:00"
}

// RunRecordView is the JSON-friendly projection of scheduler.RunRecord.
type RunRecordView struct {
	TaskID     string `json:"taskId"`
	Name       string `json:"name"`
	At         string `json:"at"`
	Status     string `json:"status"`
	Result     string `json:"result"`
	OutputMode string `json:"outputMode"`
}

// TemplateView is the JSON-friendly projection of scheduler.Template.
type TemplateView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	Desc       string `json:"desc"`
	Expression string `json:"expression"`
	Prompt     string `json:"prompt"`
	OutputMode string `json:"outputMode"`
	OutputHint string `json:"outputHint"`
	OneShot    bool   `json:"oneShot"`
}

// SchedulePreview is returned by PreviewSchedule: it converts a natural-language
// or relative phrase into the canonical stored form and the absolute fire time,
// so the create form can show "→ 2026-06-24 15:00" live as the user types.
type SchedulePreview struct {
	InputText    string `json:"inputText"`
	Expression   string `json:"expression"`   // canonical form ("at ..." / "daily ..." etc.)
	AbsoluteTime string `json:"absoluteTime"` // "" for recurring (no single instant)
	Kind         string `json:"kind"`         // "oneshot" | "recurring" | "unknown"
	Note         string `json:"note"`         // human note / error hint
}

// TaskInput is the create/update payload from the UI. Empty ID = create new.
type TaskInput struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Expression    string `json:"expression"`
	Prompt        string `json:"prompt"`
	OutputMode    string `json:"outputMode"`
	OutputDest    string `json:"outputDest"`
	OutputAccount string `json:"outputAccount"`
	OutputDir     string `json:"outputDir"`
	Color         string `json:"color"`
	Location      string `json:"location"`
	Plain         bool   `json:"plain"` // 纯提醒：到点直接弹原文，不调 AI
}

const (
	timeFmt = "2006-01-02 15:04"
)

// ListScheduledTasks returns all tasks (including paused / fired one-shots) for
// the automation panel. Newest-created first isn't tracked — we return store
// order, which is creation order.
func (a *App) ListScheduledTasks() []TaskView {
	if a.scheduler == nil {
		return []TaskView{}
	}
	tasks := a.scheduler.List(false)
	out := make([]TaskView, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, toTaskView(t))
	}
	return out
}

// CreateScheduledTask creates a task from UI input and emits scheduler:changed.
func (a *App) CreateScheduledTask(in TaskInput) (TaskView, error) {
	if a.scheduler == nil {
		return TaskView{}, fmt.Errorf("scheduler offline")
	}
	created, err := a.scheduler.Create(scheduler.ScheduledTask{
		Name:          in.Name,
		Expression:    in.Expression,
		Prompt:        in.Prompt,
		OutputMode:    in.OutputMode,
		OutputDest:    in.OutputDest,
		OutputAccount: in.OutputAccount,
		OutputDir:     in.OutputDir,
		Color:         in.Color,
		Location:      in.Location,
		Plain:         in.Plain,
	})
	if err != nil {
		return TaskView{}, err
	}
	a.emitSchedulerChanged()
	return toTaskView(created), nil
}

// UpdateScheduledTask updates an existing task (identified by in.ID).
func (a *App) UpdateScheduledTask(in TaskInput) (TaskView, error) {
	if a.scheduler == nil {
		return TaskView{}, fmt.Errorf("scheduler offline")
	}
	updated, err := a.scheduler.Update(in.ID, func(t *scheduler.ScheduledTask) {
		t.Name = in.Name
		t.Expression = in.Expression
		t.Prompt = in.Prompt
		t.OutputMode = in.OutputMode
		t.OutputDest = in.OutputDest
		t.OutputAccount = in.OutputAccount
		t.OutputDir = in.OutputDir
		t.Color = in.Color
		t.Location = in.Location
		t.Plain = in.Plain
	})
	if err != nil {
		return TaskView{}, err
	}
	a.emitSchedulerChanged()
	return toTaskView(updated), nil
}

// DeleteScheduledTask removes a task permanently.
func (a *App) DeleteScheduledTask(id string) error {
	if a.scheduler == nil {
		return fmt.Errorf("scheduler offline")
	}
	if !a.scheduler.Delete(id) {
		return fmt.Errorf("task not found")
	}
	a.emitSchedulerChanged()
	return nil
}

// PauseScheduledTask sets enabled=false (keeps the task; recomputes nothing).
func (a *App) PauseScheduledTask(id string) error {
	if a.scheduler == nil {
		return fmt.Errorf("scheduler offline")
	}
	if _, err := a.scheduler.Update(id, func(t *scheduler.ScheduledTask) { t.Enabled = false }); err != nil {
		return err
	}
	a.emitSchedulerChanged()
	return nil
}

// ResumeScheduledTask sets enabled=true and recomputes NextRun.
func (a *App) ResumeScheduledTask(id string) error {
	if a.scheduler == nil {
		return fmt.Errorf("scheduler offline")
	}
	if _, err := a.scheduler.Update(id, func(t *scheduler.ScheduledTask) { t.Enabled = true }); err != nil {
		return err
	}
	a.emitSchedulerChanged()
	return nil
}

// RunScheduledTaskNow fires a task on demand (delivery + history run as usual).
// Returns the truncated result string so the UI can show it inline.
func (a *App) RunScheduledTaskNow(id string) (string, error) {
	if a.scheduler == nil {
		return "", fmt.Errorf("scheduler offline")
	}
	res, err := a.scheduler.RunNow(id)
	if err != nil {
		return "", err
	}
	a.emitSchedulerChanged()
	return res, nil
}

// ScheduledTaskHistory returns recent run records, newest first, optionally
// filtered to one task.
func (a *App) ScheduledTaskHistory(taskID string) []RunRecordView {
	if a.scheduler == nil {
		return []RunRecordView{}
	}
	recs := a.scheduler.History(taskID)
	out := make([]RunRecordView, 0, len(recs))
	for _, r := range recs {
		out = append(out, RunRecordView{
			TaskID:     r.TaskID,
			Name:       r.Name,
			At:         r.At.Format(timeFmt),
			Status:     r.Status,
			Result:     r.Result,
			OutputMode: r.OutputMode,
		})
	}
	return out
}

// ScheduledTaskTemplates returns the builtin template catalog for the "模板" menu.
func (a *App) ScheduledTaskTemplates() []TemplateView {
	src := scheduler.Templates()
	out := make([]TemplateView, 0, len(src))
	for _, t := range src {
		out = append(out, TemplateView{
			ID:         t.ID,
			Name:       t.Name,
			Category:   t.Category,
			Desc:       t.Desc,
			Expression: t.Expression,
			Prompt:     t.Prompt,
			OutputMode: t.OutputMode,
			OutputHint: t.OutputHint,
			OneShot:    t.OneShot,
		})
	}
	return out
}

// PreviewSchedule converts a natural-language / relative phrase into the stored
// canonical form + absolute fire time. The UI calls this on each keystroke in
// the expression field to show "→ 后天 15:00 (2026-06-24 15:00)".
//
// Resolution order:
//  1. If the phrase is recognized Chinese relative text → resolve to absolute
//     "at YYYY-MM-DD HH:MM" (one-shot).
//  2. Else try to parse as a scheduler expression (every/daily/at/in/cron).
//     "in X" is normalized to "at ..." against now.
//  3. Else return kind="unknown" with a hint.
func (a *App) PreviewSchedule(text string) SchedulePreview {
	text = trim(text)
	if text == "" {
		return SchedulePreview{InputText: text, Kind: "unknown", Note: "输入时间或计划"}
	}
	now := time.Now()
	// (1) Chinese relative phrase → absolute one-shot.
	if t, err := scheduler.ResolveRelativeTime(text, now); err == nil {
		expr := "at " + t.Format("2006-01-02 15:04")
		return SchedulePreview{
			InputText:    text,
			Expression:   expr,
			AbsoluteTime: t.Format(timeFmt),
			Kind:         "oneshot",
			Note:         "一次性任务，触发后自动停用",
		}
	}
	// (2) Scheduler expression. Normalize "in X" first.
	// NOTE: PreviewSchedule is called on every keystroke (debounced in the UI),
	// so it deliberately uses ONLY the cheap regex + expression paths here. When
	// both fail the result is kind="unknown" and the UI shows a manual "🔍 智能
	// 解析" button; clicking it calls SmartParseSchedule, which is the only place
	// the fast_task_model (迅捷任务模型) is invoked for time parsing — one call
	// per click, never per keystroke.
	expr, err := scheduler.NormalizeExpression(text, now)
	if err != nil {
		return SchedulePreview{InputText: text, Kind: "unknown", Note: "无法识别（支持：每天/后天/15:00/daily/every/at/in）"}
	}
	oneShot := scheduler.IsOneShot(expr)
	nr := scheduler.NextRunPublic(expr, now)
	preview := SchedulePreview{
		InputText:  text,
		Expression: expr,
		Kind:       "recurring",
	}
	if oneShot {
		preview.Kind = "oneshot"
		preview.AbsoluteTime = nr.Format(timeFmt)
		preview.Note = "一次性任务"
		if nr.IsZero() {
			preview.Note = "该时间已过去，请选择未来时间"
		}
	} else {
		// Recurring: show the next fire as a hint.
		if !nr.IsZero() {
			preview.Note = "下次：" + nr.Format(timeFmt)
		}
	}
	return preview
}

// SmartParseSchedule is the explicit, on-demand LLM time parser. It is called
// ONLY when the user clicks the "🔍 智能解析" button in the create form — never
// during typing (typing goes through the regex-only PreviewSchedule). It asks
// the fast_task_model (迅捷任务模型) to resolve phrases the regex can't —
// "下下周五下午3点", "两个星期后", "下个月初" — into a concrete one-shot time.
//
// Returns the resolved SchedulePreview on success. When the model says the text
// isn't a time phrase, or the call fails, it returns a kind="unknown" preview
// carrying an error note so the UI can surface why the parse didn't work.
func (a *App) SmartParseSchedule(text string) SchedulePreview {
	text = trim(text)
	if text == "" {
		return SchedulePreview{InputText: text, Kind: "unknown", Note: "输入时间或计划"}
	}
	now := time.Now()
	t, err := llmParseTime(context.Background(), text, now)
	if err != nil {
		return SchedulePreview{InputText: text, Kind: "unknown", Note: "智能解析失败：" + err.Error()}
	}
	if t.IsZero() {
		// Model responded "N/A" — the text isn't recognized as a time phrase.
		return SchedulePreview{InputText: text, Kind: "unknown", Note: "未能识别为时间表达，请换种说法"}
	}
	return SchedulePreview{
		InputText:    text,
		Expression:   "at " + t.Format("2006-01-02 15:04"),
		AbsoluteTime: t.Format(timeFmt),
		Kind:         "oneshot",
		Note:         "一次性任务（智能解析）",
	}
}

// emitSchedulerChanged notifies the frontend that the task list mutated. The
// frontend re-lists on this event (no payload — keeps it simple).
func (a *App) emitSchedulerChanged() {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "scheduler:changed")
}

// --- scheduler EmailSender / Notifier bridges -------------------------------

// schedulerEmailSender implements scheduler.EmailSender by routing through
// builtin.SendPlainTextAs, which resolves the named account (or default when
// empty) to the right SMTP config. Same multi-account config as the email_send
// tool.
type schedulerEmailSender struct{}

func (schedulerEmailSender) Send(ctx context.Context, account, to, subject, body string) error {
	return builtin.SendPlainTextAs(ctx, account, to, subject, body)
}

// schedulerNotifier implements scheduler.Notifier by emitting a "scheduler:notice"
// event the frontend turns into an in-app toast. This fires even when the user
// isn't on the automation tab, so scheduled reminders surface visibly.
type schedulerNotifier struct{ app *App }

func (n schedulerNotifier) Notify(name, result string) {
	if n.app == nil || n.app.ctx == nil {
		return
	}
	runtime.EventsEmit(n.app.ctx, "scheduler:notice", map[string]string{
		"name":   name,
		"result": result,
	})
	// Fire a LONG-duration (25s) OS toast so the reminder stays on screen long
	// enough to read (the default 7s flashes and is easy to miss), then persists
	// to Windows Action Center. We bypass Wails' SendNotification (hardcoded 7s
	// Short, no override) and call go-toast directly with Duration=Long, reusing
	// the AUMID/COM registration from InitializeNotifications. Best-effort.
	go notifyLongDurationToast(name, apihelper.Truncate(result, 200))
}

// --- helpers ----------------------------------------------------------------

func toTaskView(t scheduler.ScheduledTask) TaskView {
	v := TaskView{
		ID:             t.ID,
		Name:           t.Name,
		Expression:     t.Expression,
		Prompt:         t.Prompt,
		Profile:        t.Profile,
		Enabled:        t.Enabled,
		OneShot:        t.OneShot,
		RunCount:       t.RunCount,
		LastResult:     t.LastResult,
		OutputMode:     t.OutputMode,
		OutputDest:     t.OutputDest,
		OutputAccount:  t.OutputAccount,
		OutputDir:      t.OutputDir,
		Color:          t.Color,
		Location:       t.Location,
		Plain:          t.Plain,
		LastDeliverErr: t.LastDeliverErr,
		HumanSchedule:  describeSchedule(t.Expression),
	}
	if !t.LastRun.IsZero() {
		v.LastRun = t.LastRun.Format(timeFmt)
	}
	if !t.NextRun.IsZero() {
		v.NextRun = t.NextRun.Format(timeFmt)
	}
	if !t.LastDeliverAt.IsZero() {
		v.LastDeliverAt = t.LastDeliverAt.Format(timeFmt)
	}
	return v
}

// describeSchedule renders an expression as a friendly Chinese phrase for the
// card ("daily 09:00 Mon-Fri" → "工作日 09:00"). Falls back to the raw
// expression for forms we don't special-case (cron / at).
func describeSchedule(expr string) string {
	return scheduler.Describe(expr)
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
