package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

// missing_methods.go补齐 7/15 release exe 调用但当前 Go 后端缺失的 5 个 App 方法。
// 这些方法被前端 SettingsPanel / CalendarTaskPanel / 项目树右键菜单使用，
// 缺失时调用会失败但被前端 .catch 吞掉，导致对应 UI 元素静默失效。
//
// 全部从 7/15 exe 的真实调用上下文反推签名 + 复用已有 config 字段实现。

// --- Fast-LLM base domain（legacy: custom private deployment/proxy）---------

// GetFastLLMBaseDomain returns the configured fast-LLM base URL override, or ""
// when unset. The field lives in [cowork] fast_llm_base_domain.
func (a *App) GetFastLLMBaseDomain() string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Cowork.FastLLMBaseDomain)
}

// SetFastLLMBaseDomain persists a fast-LLM base URL override ("" = reset).
func (a *App) SetFastLLMBaseDomain(domain string) error {
	domain = strings.TrimSpace(domain)
	return a.applyConfigChange(func(c *config.Config) error {
		c.Cowork.FastLLMBaseDomain = domain
		return nil
	})
}

// --- Fast-task model（dream/distill 用）---------------------------------

// SetFastTaskModel sets the lightweight model used for background dream/distill
// runs (config [agent] fast_task_model, default derived from the fast-task provider). The
// SettingsPanel exposes this as a per-model picker next to the default-model
// picker, so the user can route background tasks to a cheaper/faster model
// while keeping the main agent on a stronger one.
func (a *App) SetFastTaskModel(ref string) error {
	ref = strings.TrimSpace(ref)
	return a.applyConfigChange(func(c *config.Config) error {
		if ref != "" {
			resolved, err := selectableDesktopModelRef(c, ref)
			if err != nil {
				return err
			}
			ref = resolved
		}
		c.Agent.FastTaskModel = ref
		return nil
	})
}

// --- 定时任务 → 日历事件 ----------------------------------------------

// ListScheduledTasksAsEvents returns scheduled tasks as calendar events within
// [since, before) (RFC3339-ish "2006-01-02T15:04" boundaries, matching
// ListCalendarEvents). CalendarTaskPanel calls this to overlay scheduled-task
// firings on the calendar grid alongside manual/calendar events, so the user
// sees a unified timeline. Tasks without a next-run in range are skipped.
//
// Each task maps to one event:
//   - ID: "task:" prefix + task ID (so the calendar can dedup vs calendar events)
//   - Title: task name
//   - Start: task nextRun
//   - Source: "agent" (so the UI styles it as a task, not a manual event)
//   - Color: task color (or "" = calendar default)
//   - TaskID: the underlying task ID (so clicking the event can open the task)
func (a *App) ListScheduledTasksAsEvents(since, before string) []CalendarEventView {
	if a.scheduler == nil {
		return []CalendarEventView{}
	}
	sinceT, errSince := parseCalendarBoundary(since)
	beforeT, errBefore := parseCalendarBoundary(before)
	if errSince != nil || errBefore != nil {
		return []CalendarEventView{}
	}
	tasks := a.scheduler.List(false)
	out := make([]CalendarEventView, 0, len(tasks))
	for _, t := range tasks {
		view := toTaskView(t)
		if view.NextRun == "" || !view.Enabled {
			continue
		}
		nextT, err := parseCalendarBoundary(view.NextRun)
		if err != nil {
			continue
		}
		// Half-open [since, before): include tasks whose next-run is in range.
		if nextT.Before(sinceT) || !nextT.Before(beforeT) {
			continue
		}
		start := nextT.Format("2006-01-02T15:04")
		end := nextT.Add(30 * time.Minute).Format("2006-01-02T15:04")
		out = append(out, CalendarEventView{
			ID:        "task:" + view.ID,
			Title:     view.Name,
			Start:     start,
			End:       end,
			AllDay:    false,
			Color:     view.Color,
			Location:  view.Location,
			Status:    "confirmed",
			Source:    "agent",
			TaskID:    view.ID,
			CreatedAt: start,
		})
	}
	return out
}

// parseCalendarBoundary parses a "2006-01-02T15:04" (or RFC3339) boundary
// string from the UI. Empty returns a zero time + error so the caller can
// decide to widen the window.
func parseCalendarBoundary(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty boundary")
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02 15:04", time.RFC3339, "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable boundary %q", s)
}

// --- 专家团 session 回收 -------------------------------------------

// TrashExpertSession moves an expert-team session tab to the trash. Equivalent
// to TrashTopic for normal tabs, but the call comes from the expert-session
// tab's context-menu (project-tree right-click → "移到回收站") instead of the
// normal topic context-menu. The teamID linkage is preserved in the trashed
// session's .meta so restoring it reopens the same expert-session view.
//
// topicID is the expert-session tab's TopicID (a stable identifier); an empty
// value is rejected. On success the tab is closed (controller torn down, tab
// removed from the sidebar); the underlying session .jsonl is moved to the
// session trash dir for the active profile.
func (a *App) TrashExpertSession(topicID string) error {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return fmt.Errorf("topicID is required")
	}
	// Delegate to the shared TrashTopic path — it already handles the
	// controller teardown + session-file move, and works for expert-session
	// tabs (which have IsExpertSession=true) the same way it works for normal
	// topic tabs. The expert-specific entry point exists only so the frontend
	// can call a clearly-named method from the expert context-menu.
	return a.TrashTopic(topicID)
}
