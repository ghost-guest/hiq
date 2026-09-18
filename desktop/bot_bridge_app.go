package main

import (
	"context"
	"fmt"

	"github.com/zzycxz/hiq/internal/bot"
	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/event"
)

// newBotBridge 构造桌面 bot 桥 hub，把 deps 绑到 App 方法上，并从配置加载已持久化的订阅。
func newBotBridge(a *App) *botBridgeHub {
	var initial []bot.DesktopWatchRoute
	if cfg, err := config.Load(); err == nil {
		initial = botWatchersFromConfig(cfg.Bot.DesktopWatchers)
	}
	deps := botBridgeDeps{
		sessions: a.bridgeSessions,
		tabLabel: func(tabID string) string {
			t := a.tabByID(tabID)
			if t == nil {
				return ""
			}
			if t.Label != "" {
				return t.Label
			}
			return t.TopicTitle
		},
		approveTab: func(tabID, id string, allow bool) error {
			a.ApproveTab(tabID, id, allow, false, false)
			return nil
		},
		answerTab: func(tabID, id string, answers []event.AskAnswer) error {
			qa := make([]QuestionAnswer, len(answers))
			for i, an := range answers {
				qa[i] = QuestionAnswer{QuestionID: an.QuestionID, Selected: an.Selected}
			}
			a.AnswerQuestionForTab(tabID, id, qa)
			return nil
		},
		notify: func(ctx context.Context, route bot.DesktopWatchRoute, text string) error {
			gw := a.botGW.Load()
			if gw == nil {
				return fmt.Errorf("bot 未启用")
			}
			dest := fmt.Sprintf("%s:%s:%s", route.Platform, route.ChatType, route.ChatID)
			return gw.Push(ctx, dest, text)
		},
		persistWatchers: func(watchers []bot.DesktopWatchRoute) error {
			return a.applyConfigOnly(func(c *config.Config) error {
				c.Bot.DesktopWatchers = watchersFromBot(watchers)
				return nil
			})
		},
	}
	return newBotBridgeHub(deps, initial)
}

// bridgeSessions 遍历所有桌面 tab，构造 DesktopSessionInfo 列表。
func (a *App) bridgeSessions() []bot.DesktopSessionInfo {
	a.mu.RLock()
	tabs := make([]*WorkspaceTab, 0, len(a.tabs))
	for _, t := range a.tabs {
		tabs = append(tabs, t)
	}
	a.mu.RUnlock()
	out := make([]bot.DesktopSessionInfo, 0, len(tabs))
	for _, t := range tabs {
		s := bot.DesktopSessionInfo{
			TabID:     t.ID,
			Label:     t.Label,
			Workspace: t.WorkspaceRoot,
			Topic:     t.TopicTitle,
			Ready:     t.Ready,
		}
		if t.Ctrl != nil {
			s.Running = t.Ctrl.Running()
		}
		out = append(out, s)
	}
	return out
}

func watchersFromBot(watchers []bot.DesktopWatchRoute) []config.BotDesktopWatcher {
	out := make([]config.BotDesktopWatcher, len(watchers))
	for i, w := range watchers {
		out[i] = config.BotDesktopWatcher{
			Platform: string(w.Platform),
			ChatType: string(w.ChatType),
			ChatID:   w.ChatID,
		}
	}
	return out
}

func botWatchersFromConfig(ws []config.BotDesktopWatcher) []bot.DesktopWatchRoute {
	out := make([]bot.DesktopWatchRoute, len(ws))
	for i, w := range ws {
		out[i] = bot.DesktopWatchRoute{
			Platform: bot.Platform(w.Platform),
			ChatType: bot.ChatType(w.ChatType),
			ChatID:   w.ChatID,
		}
	}
	return out
}
