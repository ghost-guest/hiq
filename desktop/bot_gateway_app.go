package main

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/bot"
	"github.com/zzycxz/hiq/internal/bot/feishu"
	"github.com/zzycxz/hiq/internal/bot/qq"
	"github.com/zzycxz/hiq/internal/bot/telegram"
	"github.com/zzycxz/hiq/internal/bot/weixin"
	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev"
	"github.com/zzycxz/hiq/internal/tool/builtin"
)

// startBotGateway 启动内嵌的 bot gateway。
// 调用方需确保 cfg.Bot.Enabled == true。
func (a *App) startBotGateway(cfg *config.Config) {
	if a.botGW.Load() != nil {
		return // already running
	}

	// 预热 knownRemoteIDs：启动时把已持久化的 SessionMappings 读进内存去重表，
	// 这样已记录过的用户再发消息时直接命中、零 IO；只有新用户/新话题才落盘。
	for _, conn := range cfg.Bot.Connections {
		for _, m := range conn.SessionMappings {
			if id := strings.TrimSpace(m.RemoteID); id != "" {
				key := conn.Provider + ":" + id
				a.knownRemoteIDs.Store(key, true)
				// 已有 SessionID（本地话题）的，连 session key 一起预热，
				// 避免重启后首轮 turn 重复回写 sessionPath。
				if strings.TrimSpace(m.SessionID) != "" {
					a.knownRemoteIDs.Store(key+":session", true)
				}
			}
		}
	}

	// 桌面 GUI 模式下 stderr 不可见（见 app.go startup 注释），全局 slog 已被
	// 重定向到配置目录的 app.log。复用全局默认 handler，让 bot 的连接/收发日志
	//（含飞书 WebSocket 状态、消息接收时序）也写入 app.log，否则诊断信息全丢。
	logger := slog.Default()

	// 构建 adapter
	adapters := make(map[bot.Platform]bot.Adapter)
	if cfg.Bot.QQ.Enabled {
		adapters[bot.PlatformQQ] = qq.New(cfg.Bot.QQ, logger)
	}
	if cfg.Bot.Feishu.Enabled {
		adapters[bot.PlatformFeishu] = feishu.New(cfg.Bot.Feishu, logger)
	}
	if cfg.Bot.Weixin.Enabled {
		adapters[bot.PlatformWeixin] = weixin.New(cfg.Bot.Weixin, logger)
	}
	if cfg.Bot.Telegram.Enabled {
		adapters[bot.PlatformTelegram] = telegram.New(cfg.Bot.Telegram, logger)
	}

	if len(adapters) == 0 {
		logger.Warn("bot enabled but no channels enabled, skipping gateway start")
		return
	}

	// 构建 gateway 配置
	modelName := strings.TrimSpace(cfg.Bot.Model)
	if modelName == "" {
		modelName = cfg.DefaultModel
	}

	gwCfg := bot.GatewayConfig{
		Model:    modelName,
		MaxSteps: cfg.Bot.MaxSteps,
		Enabled: map[bot.Platform]bool{
			bot.PlatformQQ:       cfg.Bot.QQ.Enabled,
			bot.PlatformFeishu:   cfg.Bot.Feishu.Enabled,
			bot.PlatformWeixin:   cfg.Bot.Weixin.Enabled,
			bot.PlatformTelegram: cfg.Bot.Telegram.Enabled,
		},
		Allowlist: bot.AllowlistConfig{
			Enabled:  cfg.Bot.Allowlist.Enabled,
			AllowAll: cfg.Bot.Allowlist.AllowAll,
			Mode:     cfg.Bot.Allowlist.Mode,
			Users: map[bot.Platform][]string{
				bot.PlatformQQ:       cfg.Bot.Allowlist.QQUsers,
				bot.PlatformFeishu:   cfg.Bot.Allowlist.FeishuUsers,
				bot.PlatformWeixin:   cfg.Bot.Allowlist.WeixinUsers,
				bot.PlatformTelegram: cfg.Bot.Allowlist.TelegramUsers,
			},
			Groups: map[bot.Platform][]string{
				bot.PlatformQQ:       cfg.Bot.Allowlist.QQGroups,
				bot.PlatformFeishu:   cfg.Bot.Allowlist.FeishuGroups,
				bot.PlatformWeixin:   cfg.Bot.Allowlist.WeixinGroups,
				bot.PlatformTelegram: cfg.Bot.Allowlist.TelegramGroups,
			},
		},
		Channels: botChannelConfigsFromConnections(cfg.Bot.Connections),
		Debounce: time.Duration(cfg.Bot.DebounceMs) * time.Millisecond,
		AllowlistSaver: func(alCfg bot.AllowlistConfig) {
			if err := a.applyConfigOnly(func(c *config.Config) error {
				c.Bot.Allowlist.Enabled = alCfg.Enabled
				c.Bot.Allowlist.AllowAll = alCfg.AllowAll
				c.Bot.Allowlist.FeishuUsers = alCfg.Users[bot.PlatformFeishu]
				c.Bot.Allowlist.WeixinUsers = alCfg.Users[bot.PlatformWeixin]
				c.Bot.Allowlist.QQUsers = alCfg.Users[bot.PlatformQQ]
				c.Bot.Allowlist.TelegramUsers = alCfg.Users[bot.PlatformTelegram]
				c.Bot.Allowlist.TelegramGroups = alCfg.Groups[bot.PlatformTelegram]
				return nil
			}); err != nil {
				logger.Warn("failed to persist allowlist", "err", err)
			}
		},
		OnTurnFinished: func(src bot.SessionSource, sessionPath string) {
			// 一轮对话结束后回写：src.UserID 填「远端 ID」、sessionPath 填「本地话题」。
			// chatType+chatID 一起带上，让同一人在不同群里的对话分开（见 SetSessionIMSource）。
			provider := botPlatformToProvider(src.Platform)
			if provider == "" {
				return
			}
			userID := strings.TrimSpace(src.UserID)
			chatType := strings.TrimSpace(string(src.ChatType))
			chatID := strings.TrimSpace(src.ChatID)
			// 内存级去重，避免每轮 turn 都 read-modify-write config.toml。key 带上
			// chatType+chatID：同一人在两个群里的首轮各自落一次，而不是互相吞掉。
			remoteKey := provider + ":" + chatType + ":" + chatID + ":" + userID
			sessionKey := remoteKey + ":session"
			needRemote := true
			if _, loaded := a.knownRemoteIDs.LoadOrStore(remoteKey, true); loaded {
				needRemote = false
			}
			needSession := false
			if sessionPath != "" {
				if _, loaded := a.knownRemoteIDs.LoadOrStore(sessionKey, true); !loaded {
					needSession = true
				}
			}
			if !needRemote && !needSession {
				return // 都已记录，零 IO
			}
			// 异步落盘，不阻塞消息处理。
			go func() {
				// Mark this transcript as a bot IM session with its origin so the
				// desktop sidebar can list it under the right contact. Idempotent.
				if sessionPath != "" {
					if err := agent.SetSessionIMSource(sessionPath, string(src.Platform), userID, chatType, chatID); err != nil {
						logger.Warn("set bot session IM source meta failed", "err", err)
					}
				}
				conns, err := config.Load()
				if err != nil {
					if needRemote {
						a.knownRemoteIDs.Delete(remoteKey)
					}
					if needSession {
						a.knownRemoteIDs.Delete(sessionKey)
					}
					return
				}
				for _, conn := range conns.Bot.Connections {
					if !conn.Enabled || conn.Provider != provider {
						continue
					}
					_ = a.rememberBotConnectionRemote(conn.ID, userID, chatType, chatID, sessionPath)
				}
			}()
		},
	}

	// 注入桌面桥：让 /desktop 命令能观察+审批桌面 live 会话。用 typed local
	// 避免 nil *botBridgeHub 被包成非 nil 接口（Go 经典坑）。
	if hub := a.botBridge.Load(); hub != nil {
		gwCfg.Desktop = hub
	}
	// 运维回话侧：/netdev 发现 | /netdev 详情 <编号>（告警/早报推送的延伸）。
	gwCfg.Netdev = netdevIMBridge{}

	gw := bot.NewGateway(gwCfg, adapters, logger)
	if err := gw.Start(a.ctx); err != nil {
		logger.Error("bot gateway start failed", "err", err)
		return
	}

	a.botGW.Store(gw)
	// 运维通知出口共用这个网关：告警 Finding 直推 notify_bot_dest。
	netdev.SetNotifyPusher(gw)
	// Inject the live gateway into the builtin tool package so im_send can push
	// through it (mirrors SetRAGStore / SetScheduler). restartBotGateway calls
	// stop (which clears this) then start, so the tool always reflects the
	// current gateway state. im_send depends only on the imPusher interface
	// (Push method) to avoid an import cycle, so the concrete *bot.BotGateway
	// satisfies it implicitly.
	builtin.SetIMPusher(gw)

	logger.Info("bot gateway started", "model", modelName, "channels", len(adapters))
}

// stopBotGateway 停止内嵌的 bot gateway。
func (a *App) stopBotGateway() {
	gw := a.botGW.Swap(nil)
	netdev.SetNotifyPusher(nil)
	if gw != nil {
		gw.Stop()
	}
	// Clear the injected pusher so im_send reports offline cleanly instead of
	// pushing through a stopped gateway.
	builtin.SetIMPusher(nil)
}

// restartBotGateway 热重启 bot gateway（先停后启）。
func (a *App) restartBotGateway() {
	a.stopBotGateway()
	cfg, err := config.Load()
	if err != nil || !cfg.Bot.Enabled {
		return
	}
	a.startBotGateway(cfg)
}

// RecentChatView is the JSON-friendly projection of bot.RecentChat for the IM-
// target picker in the task form. Mirrors bot.RecentChat with json tags.
type RecentChatView struct {
	Platform string `json:"platform"`
	ChatType string `json:"chatType"`
	ChatID   string `json:"chatId"`
	UserName string `json:"userName"`
	LastSeen int64  `json:"lastSeen"`
}

// ListRecentBotChats returns recently-seen IM chats (newest first), so the task
// form can offer them as push destinations instead of forcing the user to
// hand-type a chatID. Returns [] when the bot isn't running (no chats seen yet).
func (a *App) ListRecentBotChats() []RecentChatView {
	gw := a.botGW.Load()
	if gw == nil {
		return []RecentChatView{}
	}
	src := gw.RecentChats()
	out := make([]RecentChatView, 0, len(src))
	for _, c := range src {
		out = append(out, RecentChatView{
			Platform: string(c.Platform),
			ChatType: string(c.ChatType),
			ChatID:   c.ChatID,
			UserName: c.UserName,
			LastSeen: c.LastSeen,
		})
	}
	return out
}

// BotDockStatusView is the lightweight bot status shown in the dock's Today
// panel: whether the gateway is online, which platforms are connected, and how
// many recent chats (proxy for "new messages") exist.
type BotDockStatusView struct {
	Online      bool     `json:"online"`
	Platforms   []string `json:"platforms"`
	RecentCount int      `json:"recentCount"`
}

// BotDockStatus returns a compact bot status for the dock Today panel, replacing
// the previous hardcoded "已连接 / 暂无新件" placeholder. Online = the gateway
// goroutine is running (botGW non-nil). Platforms = which adapters connected.
// RecentCount = number of recently-seen chats (a rough "activity" indicator).
func (a *App) BotDockStatus() BotDockStatusView {
	gw := a.botGW.Load()
	if gw == nil {
		return BotDockStatusView{Online: false, Platforms: []string{}, RecentCount: 0}
	}
	recent := gw.RecentChats()
	// Platforms are derived from the recent-chats list (the platforms that have
	// actually exchanged messages). This is more meaningful than configured-but-
	// unconnected adapters.
	seen := map[string]bool{}
	platforms := make([]string, 0, 3)
	for _, c := range recent {
		p := string(c.Platform)
		if !seen[p] {
			seen[p] = true
			platforms = append(platforms, p)
		}
	}
	return BotDockStatusView{
		Online:      true,
		Platforms:   platforms,
		RecentCount: len(recent),
	}
}

// botPlatformToProvider maps a bot.Platform to its connection Provider string
// (they share the same lowercase identifier: feishu/weixin/qq). Returns "" for
// unknown platforms so callers can skip. Used by the OnInboundMessage callback
// to find the connection whose SessionMappings should record the remote ID.
func botPlatformToProvider(plat bot.Platform) string {
	switch plat {
	case bot.PlatformFeishu, bot.PlatformWeixin, bot.PlatformQQ, bot.PlatformTelegram:
		return string(plat)
	}
	return ""
}

// botChannelConfigsFromConnections 从 connections 配置中提取每个平台的覆盖参数。
func botChannelConfigsFromConnections(connections []config.BotConnectionConfig) map[bot.Platform]bot.ChannelConfig {
	if len(connections) == 0 {
		return nil
	}
	out := make(map[bot.Platform]bot.ChannelConfig)
	for _, conn := range connections {
		if !conn.Enabled {
			continue
		}
		plat := bot.Platform(strings.TrimSpace(conn.Provider))
		switch plat {
		case bot.PlatformQQ, bot.PlatformFeishu, bot.PlatformWeixin, bot.PlatformTelegram:
		default:
			continue
		}
		channel := out[plat]
		if v := strings.TrimSpace(conn.Model); v != "" {
			channel.Model = v
		}
		if v := strings.TrimSpace(conn.WorkspaceRoot); v != "" {
			channel.WorkspaceRoot = v
		}
		if channel.Model != "" || channel.WorkspaceRoot != "" {
			out[plat] = channel
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// netdevIMBridge adapts the netdev findings store into the bot gateway's
// /netdev commands (read-only; the store text is already redacted at capture).
type netdevIMBridge struct{}

func (netdevIMBridge) NetdevActiveFindings() []bot.NetdevFindingSummary {
	fs, err := netdev.ListFindings()
	if err != nil {
		return nil
	}
	var out []bot.NetdevFindingSummary
	for _, f := range fs {
		if f.Status == "resolved" {
			continue
		}
		out = append(out, bot.NetdevFindingSummary{ID: f.ID, Severity: f.Severity, Title: f.Title, Devices: f.Devices, Status: f.Status})
	}
	return out
}

func (netdevIMBridge) NetdevFindingByID(id string) bot.NetdevFindingDetail {
	fs, err := netdev.ListFindings()
	if err != nil {
		return bot.NetdevFindingDetail{NotFound: true}
	}
	for _, f := range fs {
		if f.ID != id {
			continue
		}
		d := bot.NetdevFindingDetail{ID: f.ID, Severity: f.Severity, Title: f.Title, Devices: f.Devices, Detail: f.Detail, Status: f.Status}
		for _, e := range f.Evidence {
			d.Evidence = append(d.Evidence, bot.NetdevEvidenceView{Device: e.Device, Command: e.Command, Output: e.Output})
		}
		return d
	}
	return bot.NetdevFindingDetail{NotFound: true}
}

// The mutating IM commands (completion-spec §5.3): ack and proposal decisions
// run the SAME guarded backend paths as the desktop UI — group policy,
// change-window checks and audit all apply; IM is just another human surface.

func (netdevIMBridge) NetdevAckFinding(id string) bot.NetdevActionResult {
	if err := netdev.AckFindingByID(id); err != nil {
		return bot.NetdevActionResult{OK: false, Msg: fmt.Sprintf("确认失败：%v", err)}
	}
	return bot.NetdevActionResult{OK: true}
}

func (netdevIMBridge) NetdevProposals() []bot.NetdevProposalSummary {
	ps, err := netdev.ListProposals()
	if err != nil {
		return nil
	}
	var out []bot.NetdevProposalSummary
	for _, p := range ps {
		if p.Status == "rejected" || p.Status == "rolledback" {
			continue
		}
		out = append(out, bot.NetdevProposalSummary{ID: p.ID, Status: p.Status, Title: p.Intent})
	}
	return out
}

func (netdevIMBridge) NetdevProposalApprove(id string) bot.NetdevActionResult {
	cfg, err := config.Load()
	if err != nil {
		return bot.NetdevActionResult{Msg: err.Error()}
	}
	p, err := netdev.SharedManager(cfg).ApproveProposal(id, false)
	if err != nil {
		return bot.NetdevActionResult{Msg: err.Error()}
	}
	return bot.NetdevActionResult{OK: true, Msg: p.Intent}
}

func (netdevIMBridge) NetdevProposalReject(id, reason string) bot.NetdevActionResult {
	if reason == "" {
		reason = "(IM 驳回，未填原因)"
	}
	cfg, err := config.Load()
	if err != nil {
		return bot.NetdevActionResult{Msg: err.Error()}
	}
	p, err := netdev.SharedManager(cfg).RejectProposal(id, reason)
	if err != nil {
		return bot.NetdevActionResult{Msg: err.Error()}
	}
	return bot.NetdevActionResult{OK: true, Msg: p.Intent}
}
