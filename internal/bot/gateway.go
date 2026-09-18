package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/boot"
	"github.com/zzycxz/hiq/internal/control"
	"github.com/zzycxz/hiq/internal/event"
)

// GatewayConfig 是 BotGateway 的配置。
type GatewayConfig struct {
	Model         string
	MaxSteps      int
	WorkspaceRoot string
	Channels      map[Platform]ChannelConfig
	Allowlist     AllowlistConfig
	Enabled       map[Platform]bool
	Debounce      time.Duration
	// SessionIdleTimeout controls how long a bot session can sit idle (no
	// incoming messages) before its controller is closed to reclaim memory.
	// A busy bot serving many users/-groups would otherwise keep every
	// controller alive forever. Default 30m; 0 disables reaping.
	SessionIdleTimeout time.Duration
	// AllowlistSaver 当新用户被自动加入白名单时调用，用于持久化。
	// 参数为更新后的完整 AllowlistConfig。nil 表示不持久化。
	AllowlistSaver func(AllowlistConfig)
	// OnTurnFinished 在一轮对话结束后调用（无论成功或出错），用于让上层（desktop）
	// 把对话方的会话来源（platform/chatType/chatID/userID）和本次会话的本地 transcript
	// 路径回写到 BotConnection 的 SessionMappings —— 否则只有手动「测试连接」才记录
	// remoteID、而 SessionID（本地话题）永远为空，UI 显示「等待首条消息」。传整个
	// SessionSource 而非单个 remoteID，是为了让 chatType/chatID 也带上：同一人在不同
	// 群里的对话才能分开。放在 turn 结束后是因为 sessionPath 要等首轮 RunTurn 才确定
	// （prewarm 时为空）。nil 表示不回写。
	OnTurnFinished func(src SessionSource, sessionPath string)
	// Netdev，非 nil 时启用 /netdev 系列命令（发现列表 / 证据详情）——
	// 运维告警与早报推送的回话侧（FDE 的耳朵）。
	Netdev NetdevBridge
	// Desktop，非 nil 时启用 /desktop 系列命令（远程观察 + 审批桌面 live 会话）。
	// 由桌面端进程注入；独立 bot 进程（无桌面）保持 nil，/desktop 会提示不可用。
	Desktop DesktopBridge
}

// ChannelConfig overrides gateway defaults for one IM channel.
type ChannelConfig struct {
	Model         string
	WorkspaceRoot string
}

// AllowlistConfig 控制哪些用户/群可以使用 bot。
type AllowlistConfig struct {
	Enabled  bool
	AllowAll bool
	Mode     string // "open"（自动加入）| "review"（需审批）
	Users    map[Platform][]string
	Groups   map[Platform][]string
}

// BotGateway 是 hiq bot 消息网关，管理 Controller 生命周期、session 并发、
// 事件渲染和平台适配器。
type BotGateway struct {
	cfg      GatewayConfig
	adapters map[Platform]Adapter
	sessions *SessionManager

	mu             sync.Mutex
	controllers    map[string]*sessionState // session key -> active state
	allowlist      map[Platform]map[string]bool
	groupAllowlist map[Platform]map[string]bool

	logger *slog.Logger

	// recentMu guards recentChats — a bounded ring of recently-seen chats,
	// surfaced to the UI so the user can pick an IM destination without
	// hand-typing a chatID. Separate from mu to avoid contending the hot
	// controller map on every inbound message.
	recentMu    sync.Mutex
	recentChats []RecentChat
}

// recentChatsMax bounds the ring buffer so a busy bot doesn't grow it forever.
const recentChatsMax = 100

// recordRecentChat remembers an inbound chat so the UI can offer it as a push
// destination. Dedups by platform+chatID (promoting the entry to most-recent
// and refreshing LastSeen/UserName). Called on every inbound message — cheap
// (cap-bound slice, short lock hold).
func (gw *BotGateway) recordRecentChat(msg InboundMessage) {
	if strings.TrimSpace(msg.ChatID) == "" {
		return
	}
	entry := RecentChat{
		Platform: msg.Platform,
		ChatType: msg.ChatType,
		ChatID:   msg.ChatID,
		UserName: msg.UserName,
		LastSeen: time.Now().Unix(),
	}
	dedup := strings.ToLower(string(msg.Platform)) + ":" + msg.ChatID

	gw.recentMu.Lock()
	defer gw.recentMu.Unlock()
	// Promote existing entry (update its fields + move to end).
	for i, c := range gw.recentChats {
		if strings.ToLower(string(c.Platform))+":"+c.ChatID == dedup {
			gw.recentChats[i] = entry
			// move-to-back: swap with last then slice-extend isn't needed since
			// we keep newest-last ordering by appending; simplest is remove+append.
			gw.recentChats = append(gw.recentChats[:i], gw.recentChats[i+1:]...)
			gw.recentChats = append(gw.recentChats, entry)
			return
		}
	}
	gw.recentChats = append(gw.recentChats, entry)
	if len(gw.recentChats) > recentChatsMax {
		gw.recentChats = gw.recentChats[len(gw.recentChats)-recentChatsMax:]
	}
}

// RecentChats returns a snapshot of recently-seen chats, newest first. Used by
// the desktop layer to populate the IM-target picker in the task form.
func (gw *BotGateway) RecentChats() []RecentChat {
	gw.recentMu.Lock()
	defer gw.recentMu.Unlock()
	out := make([]RecentChat, len(gw.recentChats))
	// Copy in reverse so newest is first (ring is newest-last internally).
	for i, c := range gw.recentChats {
		out[len(gw.recentChats)-1-i] = c
	}
	return out
}

type sessionState struct {
	ctrl        *control.Controller
	sink        *sessionEventSink
	cancel      context.CancelFunc
	pendingAsks map[string][]event.AskQuestion
	createdAt   time.Time
	lastActive  time.Time
}

type sessionEventSink struct {
	mu     sync.RWMutex
	target event.Sink
}

func (s *sessionEventSink) setTarget(target event.Sink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.target = target
}

func (s *sessionEventSink) Emit(e event.Event) {
	s.mu.RLock()
	target := s.target
	s.mu.RUnlock()
	if target != nil {
		target.Emit(e)
	}
}

// NewGateway 创建一个新的 BotGateway。
func NewGateway(cfg GatewayConfig, adapters map[Platform]Adapter, logger *slog.Logger) *BotGateway {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Debounce <= 0 {
		cfg.Debounce = 1500 * time.Millisecond
	}
	if cfg.SessionIdleTimeout <= 0 {
		cfg.SessionIdleTimeout = 30 * time.Minute
	}
	gw := &BotGateway{
		cfg:            cfg,
		adapters:       adapters,
		sessions:       NewSessionManager(cfg.Debounce),
		controllers:    make(map[string]*sessionState),
		allowlist:      make(map[Platform]map[string]bool),
		groupAllowlist: make(map[Platform]map[string]bool),
		logger:         logger.With("component", "bot_gateway"),
	}
	gw.buildAllowlist()
	return gw
}

func (gw *BotGateway) buildAllowlist() {
	for _, plat := range []Platform{PlatformQQ, PlatformFeishu, PlatformWeixin, PlatformTelegram} {
		gw.allowlist[plat] = make(map[string]bool)
		if !gw.cfg.Allowlist.Enabled {
			continue
		}
		for _, uid := range gw.cfg.Allowlist.Users[plat] {
			gw.allowlist[plat][uid] = true
		}
		gw.groupAllowlist[plat] = make(map[string]bool)
		for _, gid := range gw.cfg.Allowlist.Groups[plat] {
			gw.groupAllowlist[plat][gid] = true
		}
	}
}

// Start 启动所有已启用的平台适配器并开始处理消息。
func (gw *BotGateway) Start(ctx context.Context) error {
	for plat, adapter := range gw.adapters {
		if !gw.cfg.Enabled[plat] {
			gw.logger.Info("platform disabled, skipping", "platform", plat)
			continue
		}
		gw.logger.Info("starting adapter", "platform", plat)
		if err := adapter.Start(ctx); err != nil {
			return fmt.Errorf("start adapter %s: %w", plat, err)
		}
	}

	// 合并所有适配器的消息通道
	for plat, adapter := range gw.adapters {
		if !gw.cfg.Enabled[plat] {
			continue
		}
		go gw.dispatchLoop(ctx, plat, adapter)
	}

	// Idle-session reaper: periodically closes controllers whose sessions
	// haven't received a message in SessionIdleTimeout. Prevents unbounded
	// memory growth in long-running bot instances serving many users/groups.
	if gw.cfg.SessionIdleTimeout > 0 {
		go gw.reapIdleSessions(ctx)
	}

	// Pre-build a "default" session so the first incoming message doesn't
	// block on boot.Build (which loads config, resolves the provider, and
	// registers all tools — several seconds). The pre-built session is
	// keyed to "" and claimed atomically by the first user; subsequent users
	// build their own (warm now because config/provider caches are hot).
	go gw.prewarmSession(ctx)

	return nil
}

// prewarmSession builds a controller under a synthetic key and parks it so the
// first real user's getOrCreateSession finds it ready. The session uses the
// gateway's default model + workspace, so it works for any platform that
// doesn't override those. Once claimed (renamed to the real session key), it's
// indistinguishable from a normally-built session.
func (gw *BotGateway) prewarmSession(ctx context.Context) {
	model, workspaceRoot := gw.sessionOptionsForPlatform(PlatformFeishu) // any platform, just need defaults
	sessionSink := &sessionEventSink{}
	ctrl, err := boot.Build(ctx, boot.Options{
		StatsSource:   "bot",
		Model:         model,
		MaxSteps:      gw.cfg.MaxSteps,
		RequireKey:    true,
		Sink:          sessionSink,
		WorkspaceRoot: workspaceRoot,
	})
	if err != nil {
		gw.logger.Warn("prewarm session build failed (first user will have cold start)", "err", err)
		return
	}
	ctrl.EnableInteractiveApproval()
	gw.mu.Lock()
	// Only park if nobody claimed a prewarm slot yet.
	if _, exists := gw.controllers["__prewarm__"]; !exists {
		gw.controllers["__prewarm__"] = &sessionState{
			ctrl:        ctrl,
			sink:        sessionSink,
			pendingAsks: make(map[string][]event.AskQuestion),
			createdAt:   time.Now(),
			lastActive:  time.Now(),
		}
		gw.mu.Unlock()
		gw.logger.Info("prewarm session ready — first user will have instant response")
	} else {
		gw.mu.Unlock()
		ctrl.Close() // someone already prewarmed; discard this one
	}
}

// reapIdleSessions periodically scans for sessions past their idle timeout and
// closes them. A session that is mid-turn (running) is never reaped — only
// truly idle ones (lastActive > timeout AND not running) are cleaned up.
func (gw *BotGateway) reapIdleSessions(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			gw.mu.Lock()
			now := time.Now()
			for key, state := range gw.controllers {
				if key == "__prewarm__" {
					continue // never reap the parked prewarm slot
				}
				if now.Sub(state.lastActive) > gw.cfg.SessionIdleTimeout && !state.ctrl.Running() {
					gw.logger.Info("reaping idle bot session", "key", key, "idle", now.Sub(state.lastActive))
					if state.cancel != nil {
						state.cancel()
					}
					state.ctrl.Close()
					delete(gw.controllers, key)
				}
			}
			gw.mu.Unlock()
		}
	}
}

// Stop 停止所有适配器并关闭所有 session。
func (gw *BotGateway) Stop() {
	gw.mu.Lock()
	for key, state := range gw.controllers {
		if state.cancel != nil {
			state.cancel()
		}
		state.ctrl.Close()
		delete(gw.controllers, key)
	}
	gw.mu.Unlock()

	for _, adapter := range gw.adapters {
		if err := adapter.Stop(); err != nil {
			gw.logger.Warn("error stopping adapter", "err", err)
		}
	}
}

// Push sends a text message to a specific chat on a given platform, independent
// of the inbound-message flow. Used by the scheduler and calendar reminder
// engine to deliver results to IM (OutputMode="im").
//
// dest formats:
//   - "platform:chatID"            — e.g. "feishu:oc_xxx", "weixin:wxid_xxx".
//     ChatType is left empty; feishu/weixin route by ChatID alone.
//   - "platform:chatType:chatID"   — e.g. "qq:group:xxxx". Needed for QQ,
//     whose send URL is chosen by ChatType (dm/group/guild/direct). Omitting
//     chatType for QQ defaults to dm, which fails for group/channel IDs.
//
// No-op (returns nil) if the platform adapter isn't connected — a scheduled
// push shouldn't fail the task run just because IM is offline.
func (gw *BotGateway) Push(ctx context.Context, dest, text string) error {
	plat, chatType, chatID := splitPushDest(dest)
	if plat == "" || chatID == "" {
		return fmt.Errorf("invalid IM dest %q (want \"platform:chatID\" or \"platform:chatType:chatID\")", dest)
	}
	adapter, ok := gw.adapters[plat]
	if !ok {
		gw.logger.Warn("push: platform adapter not connected", "platform", plat)
		return nil
	}
	out := OutboundMessage{ChatID: chatID, Text: text}
	if chatType != "" {
		out.ChatType = chatType
	}
	_, err := adapter.Send(ctx, out)
	return err
}

// splitPushDest parses a push dest into platform, optional chatType, and
// chatID. Supports two shapes:
//   - "platform:chatID"            → (plat, "", chatID)
//   - "platform:chatType:chatID"   → (plat, chatType, chatID)
//
// Returns ("", "", "") if the shape is wrong. platform is lower-cased; chatType
// is matched against the ChatType constants (dm/group/guild/direct/thread) and
// returned as-is when recognized, else "" (so an unknown middle segment isn't
// mistaken for a chatType and the dest is treated as 2-segment with the middle
// folded into chatID only when it can't be a chatType — but we keep it strict:
// a 3-segment dest with an unrecognized middle is rejected upstream to avoid
// silent misdelivery).
func splitPushDest(dest string) (Platform, ChatType, string) {
	dest = strings.TrimSpace(dest)
	parts := strings.SplitN(dest, ":", 3)
	if len(parts) < 2 {
		return "", "", ""
	}
	plat := Platform(strings.ToLower(strings.TrimSpace(parts[0])))
	if len(parts) == 2 {
		return plat, "", strings.TrimSpace(parts[1])
	}
	// 3-segment: validate the middle is a known chatType; if not, the user
	// likely meant a 2-segment dest whose chatID contains a colon (rare but
	// possible) — treat parts[1]+parts[2] as the chatID to stay permissive.
	mid := strings.ToLower(strings.TrimSpace(parts[1]))
	switch mid {
	case "dm", "group", "guild", "direct", "thread":
		return plat, ChatType(mid), strings.TrimSpace(parts[2])
	default:
		// Unknown middle segment: treat the whole tail as chatID (2-segment
		// semantics) so we never silently drop a dest.
		return plat, "", parts[1] + ":" + parts[2]
	}
}

func (gw *BotGateway) dispatchLoop(ctx context.Context, plat Platform, adapter Adapter) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-adapter.Messages():
			if !ok {
				return
			}
			gw.logger.Info("[gateway] message received", "platform", plat, "user_id", msg.UserID)
			gw.handleMessage(ctx, plat, adapter, msg)
		}
	}
}

func (gw *BotGateway) handleMessage(ctx context.Context, plat Platform, adapter Adapter, msg InboundMessage) {
	msg.Platform = plat
	// Remember this chat so the UI can offer it as a push destination for
	// scheduled tasks / calendar reminders. Recorded before allowlist so even
	// pending (review-mode) chats appear in the picker once approved.
	gw.recordRecentChat(msg)

	// allowlist 检查
	if !gw.checkAllowlist(plat, msg) {
		if gw.cfg.Allowlist.Mode == "review" {
			// 审核模式：拒绝并显示 user_id
			gw.logger.Info("user not in allowlist (review mode)", "platform", plat, "user_id", msg.UserID)
			_ = gw.sendText(ctx, adapter, msg, fmt.Sprintf("⏳ 您尚未获得使用权限。\n\n您的 user_id: %s\n\n请将此 ID 发送给管理员，等待审批通过后即可使用。", msg.UserID))
			return
		}
		// 开放模式（默认）：自动加入
		gw.logger.Info("user not in allowlist, auto-adding", "platform", plat, "user_id", msg.UserID)
		gw.autoAddToAllowlist(plat, msg.UserID)
		_ = gw.sendText(ctx, adapter, msg, "👋 您好！您已被自动加入白名单，现在可以正常使用了。")
	}

	src := msg.Session()
	key := BuildSessionKey(src)

	// 斜杠命令处理
	if IsSlashBypass(msg.Text) {
		gw.handleSlashCommand(ctx, adapter, key, msg)
		return
	}

	// session 并发控制
	acquired, merged := gw.sessions.TryAcquire(key, msg)
	if merged {
		gw.logger.Debug("message merged to pending queue", "session", key[:8])
		return
	}
	if !acquired {
		gw.logger.Debug("session busy, queued", "session", key[:8])
		return
	}

	gw.runTurn(ctx, adapter, key, msg)
}

// autoAddToAllowlist 将用户自动加入内存白名单，并通过回调持久化。
func (gw *BotGateway) autoAddToAllowlist(plat Platform, userID string) {
	gw.mu.Lock()
	if gw.allowlist[plat] == nil {
		gw.allowlist[plat] = make(map[string]bool)
	}
	gw.allowlist[plat][userID] = true
	// 构建当前完整的 allowlist 配置用于持久化
	cfg := gw.snapshotAllowlist()
	gw.mu.Unlock()

	if gw.cfg.AllowlistSaver != nil {
		gw.cfg.AllowlistSaver(cfg)
	}
}

// snapshotAllowlist 从内存白名单生成 AllowlistConfig。调用方需持锁。
func (gw *BotGateway) snapshotAllowlist() AllowlistConfig {
	cfg := gw.cfg.Allowlist
	cfg.Enabled = true
	cfg.AllowAll = false
	cfg.Users = map[Platform][]string{
		PlatformFeishu:   allowlistKeys(gw.allowlist[PlatformFeishu]),
		PlatformWeixin:   allowlistKeys(gw.allowlist[PlatformWeixin]),
		PlatformQQ:       allowlistKeys(gw.allowlist[PlatformQQ]),
		PlatformTelegram: allowlistKeys(gw.allowlist[PlatformTelegram]),
	}
	return cfg
}

func allowlistKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (gw *BotGateway) checkAllowlist(plat Platform, msg InboundMessage) bool {
	if gw.cfg.Allowlist.AllowAll {
		return true
	}
	if !gw.cfg.Allowlist.Enabled {
		return false
	}
	if !gw.allowlist[plat][msg.UserID] {
		return false
	}
	groups := gw.groupAllowlist[plat]
	if chatUsesGroupAllowlist(msg.ChatType) && len(groups) > 0 && !groups[msg.ChatID] {
		return false
	}
	return true
}

func chatUsesGroupAllowlist(chatType ChatType) bool {
	switch chatType {
	case ChatGroup, ChatGuild, ChatThread:
		return true
	default:
		return false
	}
}

func (gw *BotGateway) handleSlashCommand(ctx context.Context, adapter Adapter, key string, msg InboundMessage) {
	switch {
	case strings.HasPrefix(msg.Text, "/netdev"):
		_ = gw.sendText(ctx, adapter, msg, gw.handleNetdevCommand(msg))
	case strings.HasPrefix(msg.Text, "/stop"):
		gw.mu.Lock()
		state, ok := gw.controllers[key]
		gw.mu.Unlock()
		if ok && state.cancel != nil {
			state.cancel()
		}
		gw.sessions.ForceRelease(key)
		_ = gw.sendText(ctx, adapter, msg, "已停止当前任务。")

	case strings.HasPrefix(msg.Text, "/new") || strings.HasPrefix(msg.Text, "/reset"):
		gw.mu.Lock()
		state, ok := gw.controllers[key]
		gw.mu.Unlock()
		if ok {
			if state.cancel != nil {
				state.cancel()
			}
			if err := state.ctrl.NewSession(); err != nil {
				gw.logger.Warn("new session failed", "err", err)
			}
		}
		gw.sessions.ForceRelease(key)
		_ = gw.sendText(ctx, adapter, msg, "已开始新会话。")

	case strings.HasPrefix(msg.Text, "/approve"):
		// 从消息中解析 approval ID
		parts := strings.Fields(msg.Text)
		if len(parts) < 2 {
			_ = gw.sendText(ctx, adapter, msg, "用法: /approve <id>")
			return
		}
		gw.mu.Lock()
		state, ok := gw.controllers[key]
		gw.mu.Unlock()
		if ok {
			state.ctrl.Approve(parts[1], true, false, false)
			_ = gw.sendText(ctx, adapter, msg, "已批准。")
		}

	case strings.HasPrefix(msg.Text, "/deny"):
		parts := strings.Fields(msg.Text)
		if len(parts) < 2 {
			_ = gw.sendText(ctx, adapter, msg, "用法: /deny <id>")
			return
		}
		gw.mu.Lock()
		state, ok := gw.controllers[key]
		gw.mu.Unlock()
		if ok {
			state.ctrl.Approve(parts[1], false, false, false)
			_ = gw.sendText(ctx, adapter, msg, "已拒绝。")
		}

	case strings.HasPrefix(msg.Text, "/answer"):
		parts := strings.Fields(msg.Text)
		if len(parts) < 3 {
			_ = gw.sendText(ctx, adapter, msg, "用法: /answer <id> <选项或 q1=选项;q2=选项>")
			return
		}
		askID := parts[1]
		rawAnswer := strings.TrimSpace(strings.Join(parts[2:], " "))
		gw.mu.Lock()
		state, ok := gw.controllers[key]
		var questions []event.AskQuestion
		if ok {
			questions = state.pendingAsks[askID]
			delete(state.pendingAsks, askID)
		}
		gw.mu.Unlock()
		if !ok || state.ctrl == nil {
			_ = gw.sendText(ctx, adapter, msg, "没有找到当前会话。")
			return
		}
		answers := parseAskAnswers(questions, rawAnswer)
		state.ctrl.AnswerQuestion(askID, answers)
		_ = gw.sendText(ctx, adapter, msg, "已提交回答。")

	case strings.HasPrefix(msg.Text, "/status"):
		active := gw.sessions.ActiveCount()
		gw.mu.Lock()
		sessions := len(gw.controllers)
		gw.mu.Unlock()
		_ = gw.sendText(ctx, adapter, msg, fmt.Sprintf("活跃任务数: %d\n保留会话数: %d", active, sessions))

	case strings.HasPrefix(msg.Text, "/whoami"):
		_ = gw.sendText(ctx, adapter, msg, fmt.Sprintf("平台: %s\n用户 ID: %s", msg.Platform, msg.UserID))

	case strings.HasPrefix(msg.Text, "/plan"):
		// /plan toggles plan mode on; /plan off turns it off. Without this,
		// IM users had no way to enter plan mode manually (only auto_plan could
		// trigger it) and no way to exit once in it. See audit finding C10.
		gw.mu.Lock()
		state, ok := gw.controllers[key]
		gw.mu.Unlock()
		if !ok || state.ctrl == nil {
			_ = gw.sendText(ctx, adapter, msg, "没有找到当前会话。")
			return
		}
		arg := strings.TrimSpace(strings.TrimPrefix(msg.Text, "/plan"))
		turnOn := !strings.EqualFold(arg, "off")
		state.ctrl.SetPlanMode(turnOn)
		if turnOn {
			_ = gw.sendText(ctx, adapter, msg, "已进入规划模式（只读探索，写操作需审批）。")
		} else {
			_ = gw.sendText(ctx, adapter, msg, "已退出规划模式。")
		}

	case strings.HasPrefix(msg.Text, "/desktop"):
		_ = gw.sendText(ctx, adapter, msg, gw.handleDesktopCommand(msg))

	case strings.HasPrefix(msg.Text, "/help"):
		help := "可用命令:\n" +
			"/stop - 停止当前任务\n" +
			"/new - 开始新会话\n" +
			"/reset - 重置会话\n" +
			"/approve <id> - 批准操作\n" +
			"/deny <id> - 拒绝操作\n" +
			"/answer <id> <选项> - 回答 ask 问题\n" +
			"/plan - 进入规划模式（只读）\n" +
			"/plan off - 退出规划模式\n" +
			"/desktop - 远程查看/审批桌面会话\n" +
			"/status - 查看状态\n" +
			"/whoami - 查看你的用户 ID\n" +
			"/help - 显示帮助"
		_ = gw.sendText(ctx, adapter, msg, help)
	}
}

func (gw *BotGateway) runTurn(ctx context.Context, adapter Adapter, key string, msg InboundMessage) {
	turnStart := time.Now()
	defer func() {
		gw.logger.Info("turn finished", "session", key[:8], "total_ms", time.Since(turnStart).Milliseconds())
		// 检查是否有等待队列中的消息
		next := gw.sessions.Release(key)
		if next != nil {
			gw.runTurn(ctx, adapter, key, *next)
			return
		}
	}()

	// 构建输入文本：群聊中在消息前加上发送者名
	input := msg.Text
	if msg.ChatType == ChatGroup {
		input = fmt.Sprintf("[%s] %s", msg.UserName, msg.Text)
	}

	// 获取或创建 Controller
	state := gw.getOrCreateSession(ctx, key, msg)
	gw.logger.Info("session acquired", "session", key[:8], "acquire_ms", time.Since(turnStart).Milliseconds())
	if state == nil || state.ctrl == nil {
		gw.logger.Warn("failed to create session", "session", key[:8])
		_ = gw.sendText(ctx, adapter, msg, "内部错误：无法创建会话。")
		return
	}

	// 多数 IM（含飞书）无原生 typing API，SendTyping 是空实现；曾在这里补一条
	// 「思考中」占位文本作即时反馈，但实测它在真正的回答前先到达、且无信息量，
	// 反成噪音，已移除。即时反馈改由 pending reaction（OnIt emoji，异步）承担。
	_ = adapter.SendTyping(ctx, msg.ChatID)

	// 创建事件渲染 sink
	sink := newRenderSink(ctx, adapter, msg.ChatID, msg.ChatType, msg.MessageID, gw.logger, func(ask event.Ask) {
		gw.mu.Lock()
		if state.pendingAsks == nil {
			state.pendingAsks = make(map[string][]event.AskQuestion)
		}
		state.pendingAsks[ask.ID] = ask.Questions
		gw.mu.Unlock()
	})
	state.sink.setTarget(sink)
	defer state.sink.setTarget(nil)

	// 创建带取消和超时的 context。Bot 没有交互式 UI 让用户感知一个永久
	// running 的 turn，且 IM 会话被 SessionManager 锁定直到 Release——一个卡死
	// 的 turn（模型 hang / 死循环 / 审批无人响应）会永久占住该 session key，
	// 后续消息全部排队等待。10 分钟兜底让卡死的 turn 自动释放。/stop 仍可
	// 即时取消（cancel 是 WithTimeout 返回的，外部调用兼容）。见审计 E5。
	const botTurnTimeout = 10 * time.Minute
	turnCtx, cancel := context.WithTimeout(ctx, botTurnTimeout)
	defer cancel()

	gw.mu.Lock()
	state.cancel = cancel
	state.lastActive = time.Now()
	gw.mu.Unlock()

	// 运行一轮对话
	sink.ctrl = state.ctrl
	err := state.ctrl.RunTurn(turnCtx, input)
	sink.Emit(event.Event{Kind: event.TurnDone, Err: err})
	if err != nil {
		if errors.Is(turnCtx.Err(), context.DeadlineExceeded) {
			_ = gw.sendText(ctx, adapter, msg, "本轮对话超时（超过 10 分钟），已自动停止。可用 /new 重开会话。")
		}
		gw.logger.Warn("turn error", "session", key[:8], "err", err)
	}

	// 回写远端 ID 与本地会话路径到 SessionMappings。放 turn 之后是因为
	// sessionPath 要等首轮 RunTurn 才确定（prewarm 时为空）。desktop 侧会用
	// remoteID 填「远端 ID」、用 sessionPath 填「本地话题」，让 UI 不再显示
	// 「等待首条消息」。只传非空值，desktop 侧自行去重落盘。
	if gw.cfg.OnTurnFinished != nil && strings.TrimSpace(msg.UserID) != "" {
		gw.cfg.OnTurnFinished(msg.Session(), state.ctrl.SessionPath())
	}
}

func (gw *BotGateway) getOrCreateSession(ctx context.Context, key string, msg InboundMessage) *sessionState {
	gw.mu.Lock()
	if state, ok := gw.controllers[key]; ok {
		state.lastActive = time.Now()
		gw.mu.Unlock()
		return state
	}
	// Claim the prewarmed session if one is available — avoids the 3-10s
	// boot.Build cold-start for the first user.
	if pre, ok := gw.controllers["__prewarm__"]; ok {
		delete(gw.controllers, "__prewarm__")
		pre.lastActive = time.Now()
		gw.controllers[key] = pre
		gw.mu.Unlock()
		gw.logger.Info("claimed prewarmed session for new user", "key", key[:8])
		return pre
	}
	gw.mu.Unlock()

	// 没有预热 session 可用，正常创建
	sessionSink := &sessionEventSink{}
	model, workspaceRoot := gw.sessionOptionsForPlatform(msg.Platform)
	ctrl, err := boot.Build(ctx, boot.Options{
		StatsSource:   "bot",
		Model:         model,
		MaxSteps:      gw.cfg.MaxSteps,
		RequireKey:    true,
		Sink:          sessionSink,
		WorkspaceRoot: workspaceRoot,
	})
	if err != nil {
		gw.logger.Error("build controller failed", "err", err)
		return nil
	}
	ctrl.EnableInteractiveApproval()

	gw.mu.Lock()
	gw.controllers[key] = &sessionState{
		ctrl:        ctrl,
		sink:        sessionSink,
		pendingAsks: make(map[string][]event.AskQuestion),
		createdAt:   time.Now(),
		lastActive:  time.Now(),
	}
	state := gw.controllers[key]
	gw.mu.Unlock()

	return state
}

func (gw *BotGateway) sessionOptionsForPlatform(plat Platform) (model string, workspaceRoot string) {
	model = gw.cfg.Model
	workspaceRoot = gw.cfg.WorkspaceRoot
	if gw.cfg.Channels == nil {
		return model, workspaceRoot
	}
	channel, ok := gw.cfg.Channels[plat]
	if !ok {
		return model, workspaceRoot
	}
	if value := strings.TrimSpace(channel.Model); value != "" {
		model = value
	}
	if value := strings.TrimSpace(channel.WorkspaceRoot); value != "" {
		workspaceRoot = value
	}
	return model, workspaceRoot
}

func (gw *BotGateway) sendText(ctx context.Context, adapter Adapter, msg InboundMessage, text string) error {
	_, err := adapter.Send(ctx, OutboundMessage{
		ChatID:       msg.ChatID,
		ChatType:     msg.ChatType,
		Text:         text,
		ReplyToMsgID: msg.MessageID,
	})
	return err
}

func parseAskAnswers(questions []event.AskQuestion, raw string) []event.AskAnswer {
	raw = strings.TrimSpace(raw)
	if len(questions) == 0 {
		return []event.AskAnswer{{Selected: []string{raw}}}
	}
	byID := make(map[string]*event.AskQuestion, len(questions))
	for i := range questions {
		q := &questions[i]
		byID[q.ID] = q
		byID[fmt.Sprintf("%d", i+1)] = q
	}
	answerMap := make(map[string][]string, len(questions))
	if strings.Contains(raw, "=") {
		for _, part := range strings.Split(raw, ";") {
			k, v, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			q := byID[strings.TrimSpace(k)]
			if q == nil {
				continue
			}
			answerMap[q.ID] = normalizeAskSelection(*q, strings.TrimSpace(v))
		}
	} else if len(questions) == 1 {
		answerMap[questions[0].ID] = normalizeAskSelection(questions[0], raw)
	}
	out := make([]event.AskAnswer, 0, len(questions))
	for _, q := range questions {
		out = append(out, event.AskAnswer{QuestionID: q.ID, Selected: answerMap[q.ID]})
	}
	return out
}

func normalizeAskSelection(q event.AskQuestion, raw string) []string {
	parts := []string{raw}
	if q.Multi && strings.Contains(raw, ",") {
		parts = strings.Split(raw, ",")
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if idx, err := strconv.Atoi(part); err == nil && idx >= 1 && idx <= len(q.Options) {
			out = append(out, q.Options[idx-1].Label)
			continue
		}
		out = append(out, part)
	}
	return out
}
