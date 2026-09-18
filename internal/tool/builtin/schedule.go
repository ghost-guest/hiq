package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/scheduler"
	"github.com/zzycxz/hiq/internal/tool"
)

// Scheduled-task tools (Phase 3 of coWork). These wrap a process-global
// scheduler.Scheduler so the agent can create recurring prompts ("every weekday
// at 9am, compile the news digest"). The scheduler itself is app-level and
// persists across restarts; these tools are just the create/list/delete/update
// surface, profile-gated to cowork.
//
// The scheduler instance is injected via SetScheduler (called from boot.go when
// the cowork profile activates). When nil, the tools return a clear error so a
// dev-session call doesn't crash — the tools aren't registered there anyway.

var globalScheduler *scheduler.Scheduler

// SetScheduler injects the app-level scheduler the tools drive. Called once at
// cowork boot; passing nil disables the tools (they return "scheduler offline").
func SetScheduler(s *scheduler.Scheduler) { globalScheduler = s }

func requireScheduler() (*scheduler.Scheduler, error) {
	if globalScheduler == nil {
		return nil, errors.New("scheduler is offline (only available under the cowork profile with an active controller)")
	}
	return globalScheduler, nil
}

// SchedulerTools returns the scheduled-task tools for cowork registration.
func SchedulerTools() []tool.Tool {
	return []tool.Tool{
		scheduleCreate{},
		scheduleList{},
		scheduleDelete{},
		scheduleUpdate{},
		scheduleHistory{},
		scheduleRunNow{},
	}
}

// --- schedule_create --------------------------------------------------------

type scheduleCreate struct{}

func (scheduleCreate) Name() string { return "schedule_create" }

func (scheduleCreate) Description() string {
	return "Create a scheduled task that fires an agent prompt on a schedule, independent of any open chat tab. Expression formats: \"every 30m\", \"every 2h\", \"hourly\", \"daily 09:00\", \"daily 09:00 Mon-Fri\" (weekdays), \"daily 09:00 Mon,Wed,Fri\", \"at 2026-06-24 15:00\" (one-shot absolute time, auto-disables after firing), \"in 2h30m\" / \"in 3d\" (one-shot relative offset, normalized to at-form), or a 5-field cron (\"0 9 * * 1-5\"). " +
		"IMPORTANT for one-shot times: prefer relative words so the system resolves the correct date — \"明天下午3点\", \"下周一9点\", \"in 2h\", \"3号10点\" — instead of guessing an absolute \"at YYYY-MM-DD HH:MM\" (your year may be wrong). " +
		"The prompt runs under the cowork profile; its result is stored on the task and optionally delivered (im/email/notify/file). Use for recurring or one-off office work: daily digests, periodic reports, scheduled scraping, meeting reminders. Tasks persist across restarts."
}

func (scheduleCreate) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "name":{"type":"string","description":"Human-readable task name"},
  "expression":{"type":"string","description":"Schedule: \"every 30m\", \"daily 09:00\", \"daily 09:00 Mon-Fri\", \"at 2026-06-24 15:00\" (one-shot), \"in 2h\" (one-shot relative), or 5-field cron"},
  "prompt":{"type":"string","description":"The agent prompt to run on each fire"},
  "output_mode":{"type":"string","description":"Result routing: \"\" | \"im\" | \"email\" | \"notify\" | \"file\". Empty = store only. im = push to IM (feishu/QQ/WeChat). email = SMTP send. notify = in-app desktop toast. file = append to file."},
  "output_dest":{"type":"string","description":"Destination for the chosen output_mode. im=\"platform:chatID\" (e.g. feishu:oc_xxx); email=\"to\" or \"to;subject\"; file=absolute path; notify=unused."}
},
"required":["name","expression","prompt"]
}`)
}

func (scheduleCreate) ReadOnly() bool { return false }

func (scheduleCreate) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p scheduler.ScheduledTask
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	p.ID = "" // server assigns
	s, err := requireScheduler()
	if err != nil {
		return "", err
	}
	created, err := s.Create(p)
	if err != nil {
		// If the failure is a past-time one-shot (a common failure: the model's
		// training cutoff makes it write absolute dates in the wrong year), enrich
		// the error with the CURRENT date/time so the model can self-correct on a
		// retry. Absolute "at YYYY-..." expressions are the trap; relative words
		// ("明天下午3点", "in 2h") are resolved by the scheduler and never hit this.
		if strings.Contains(err.Error(), "past") || strings.Contains(err.Error(), "one-shot") {
			now := time.Now()
			return "", fmt.Errorf(
				"%w\n\n当前时间是 %s。请改用相对时间词（如「明天下午3点」「下周一9点」「in 2h」）让系统自动换算成正确的未来时间，或用 at %s 这样的未来绝对时间（注意年份必须是 %d）。",
				err, now.Format("2006-01-02 15:04 (周一)"),
				now.Add(2*time.Hour).Format("2006-01-02 15:04"),
				now.Year(),
			)
		}
		return "", err
	}
	return formatTask(created), nil
}

// --- schedule_list ----------------------------------------------------------

type scheduleList struct{}

func (scheduleList) Name() string { return "schedule_list" }

func (scheduleList) Description() string {
	return "List scheduled tasks. Set enabled_only=true to exclude paused tasks. Each task shows its expression, next fire time, last run, and run count."
}

func (scheduleList) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "enabled_only":{"type":"boolean","description":"Only list enabled (active) tasks (default false)"}
},
"required":[]
}`)
}

func (scheduleList) ReadOnly() bool { return true }

func (scheduleList) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		EnabledOnly bool `json:"enabled_only"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &p)
	}
	s, err := requireScheduler()
	if err != nil {
		return "", err
	}
	tasks := s.List(p.EnabledOnly)
	if len(tasks) == 0 {
		return "no scheduled tasks", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d task(s):\n", len(tasks))
	for _, t := range tasks {
		b.WriteString(formatTask(t) + "\n")
	}
	return b.String(), nil
}

// --- schedule_delete --------------------------------------------------------

type scheduleDelete struct{}

func (scheduleDelete) Name() string { return "schedule_delete" }

func (scheduleDelete) Description() string {
	return "Delete a scheduled task by id (permanently). To pause without deleting, use schedule_update with enabled=false."
}

func (scheduleDelete) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "id":{"type":"string","description":"Task id from schedule_list"}
},
"required":["id"]
}`)
}

func (scheduleDelete) ReadOnly() bool { return false }

func (scheduleDelete) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	s, err := requireScheduler()
	if err != nil {
		return "", err
	}
	if !s.Delete(p.ID) {
		return "", fmt.Errorf("task %q not found", p.ID)
	}
	return fmt.Sprintf("deleted task %q", p.ID), nil
}

// --- schedule_update --------------------------------------------------------

type scheduleUpdate struct{}

func (scheduleUpdate) Name() string { return "schedule_update" }

func (scheduleUpdate) Description() string {
	return "Update a scheduled task. Pass any of name/expression/prompt/enabled/output_mode/output_dest to change; omitted fields keep their current value. Set enabled=false to pause, true to resume (recomputes next fire). Changing the expression re-validates it."
}

func (scheduleUpdate) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "id":{"type":"string","description":"Task id from schedule_list"},
  "name":{"type":"string","description":"New display name (omit to keep current)"},
  "expression":{"type":"string","description":"New cron expression M H DoM Mon DoW (omit to keep current)"},
  "prompt":{"type":"string","description":"New prompt template (omit to keep current)"},
  "enabled":{"type":"boolean","description":"true to enable, false to disable"},
  "output_mode":{"type":"string","description":"im, email, notify, or file"},
  "output_dest":{"type":"string","description":"Target address/path (omit to clear)"}
},
"required":["id"]
}`)
}

func (scheduleUpdate) ReadOnly() bool { return false }

func (scheduleUpdate) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		ID         string  `json:"id"`
		Name       *string `json:"name"`
		Expression *string `json:"expression"`
		Prompt     *string `json:"prompt"`
		Enabled    *bool   `json:"enabled"`
		OutputMode *string `json:"output_mode"`
		OutputDest *string `json:"output_dest"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	s, err := requireScheduler()
	if err != nil {
		return "", err
	}
	updated, err := s.Update(p.ID, func(t *scheduler.ScheduledTask) {
		if p.Name != nil {
			t.Name = *p.Name
		}
		if p.Expression != nil {
			t.Expression = *p.Expression
		}
		if p.Prompt != nil {
			t.Prompt = *p.Prompt
		}
		if p.Enabled != nil {
			t.Enabled = *p.Enabled
		}
		if p.OutputMode != nil {
			t.OutputMode = *p.OutputMode
		}
		if p.OutputDest != nil {
			t.OutputDest = *p.OutputDest
		}
	})
	if err != nil {
		// Same past-time hint as schedule_create: enrich with current date so the
		// model can self-correct a wrong-year absolute date on retry.
		if strings.Contains(err.Error(), "past") || strings.Contains(err.Error(), "one-shot") {
			now := time.Now()
			return "", fmt.Errorf(
				"%w\n\n当前时间是 %s。请改用相对时间词（如「明天下午3点」「in 2h」）或用 at %s 这样的未来绝对时间（年份必须是 %d）。",
				err, now.Format("2006-01-02 15:04 (周一)"),
				now.Add(2*time.Hour).Format("2006-01-02 15:04"),
				now.Year(),
			)
		}
		return "", err
	}
	return formatTask(updated), nil
}

// formatTask renders one task for tool output.
func formatTask(t scheduler.ScheduledTask) string {
	status := "enabled"
	if !t.Enabled {
		status = "paused"
	}
	next := "—"
	if !t.NextRun.IsZero() {
		next = t.NextRun.Format("2006-01-02 15:04")
	}
	last := "never"
	if !t.LastRun.IsZero() {
		last = t.LastRun.Format("2006-01-02 15:04")
	}
	return fmt.Sprintf("- %s [%s] %q\n  expression: %s\n  next: %s · last: %s · runs: %d\n  prompt: %s",
		t.ID, status, t.Name, t.Expression, next, last, t.RunCount, truncatePrompt(t.Prompt))
}

func truncatePrompt(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

// --- schedule_history -------------------------------------------------------

type scheduleHistory struct{}

func (scheduleHistory) Name() string { return "schedule_history" }

func (scheduleHistory) Description() string {
	return "List recent scheduled-task run records (newest first), optionally filtered to one task by id. Each record shows the run time, status (ok/error/skipped), and a truncated result. Useful to confirm a task actually fired and see what it produced."
}

func (scheduleHistory) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "id":{"type":"string","description":"Optional task id to filter to one task's runs. Omit to list across all tasks."}
},
"required":[]
}`)
}

func (scheduleHistory) ReadOnly() bool { return true }

func (scheduleHistory) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		ID string `json:"id"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &p)
	}
	s, err := requireScheduler()
	if err != nil {
		return "", err
	}
	recs := s.History(p.ID)
	if len(recs) == 0 {
		return "no run history", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d record(s):\n", len(recs))
	for _, r := range recs {
		mode := r.OutputMode
		if mode == "" {
			mode = "store"
		}
		fmt.Fprintf(&b, "- %s [%s] %s · %s\n  %s\n", r.At.Format("2006-01-02 15:04"), r.Status, mode, r.Name, truncatePrompt(r.Result))
	}
	return b.String(), nil
}

// --- schedule_run_now -------------------------------------------------------

type scheduleRunNow struct{}

func (scheduleRunNow) Name() string { return "schedule_run_now" }

func (scheduleRunNow) Description() string {
	return "Fire a scheduled task immediately, outside its schedule. The task's normal delivery (im/email/notify/file) runs as usual, and a run-history record is appended. The task's schedule is unaffected (it still fires at its next scheduled time). Use to test a task or run it on demand."
}

func (scheduleRunNow) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "id":{"type":"string","description":"Task id from schedule_list"}
},
"required":["id"]
}`)
}

func (scheduleRunNow) ReadOnly() bool { return false }

func (scheduleRunNow) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	s, err := requireScheduler()
	if err != nil {
		return "", err
	}
	result, err := s.RunNow(p.ID)
	if err != nil {
		return "", err
	}
	return result, nil
}
