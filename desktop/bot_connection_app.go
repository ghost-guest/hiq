package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/bot"
	"github.com/zzycxz/hiq/internal/bot/feishu"
	"github.com/zzycxz/hiq/internal/bot/telegram"
	"github.com/zzycxz/hiq/internal/bot/weixin"
	"github.com/zzycxz/hiq/internal/config"
)

type BotConnectionCredentialView struct {
	AppID        string `json:"appId"`
	AppSecretEnv string `json:"appSecretEnv"`
	AccountID    string `json:"accountId"`
	TokenEnv     string `json:"tokenEnv"`
	SecretSet    bool   `json:"secretSet"`
}

type BotConnectionSessionMappingView struct {
	RemoteID      string `json:"remoteId"`
	ChatType      string `json:"chatType"`
	ChatID        string `json:"chatId"`
	SessionID     string `json:"sessionId"`
	Scope         string `json:"scope"`
	WorkspaceRoot string `json:"workspaceRoot"`
	UpdatedAt     string `json:"updatedAt"`
}

type BotConnectionView struct {
	ID              string                            `json:"id"`
	Provider        string                            `json:"provider"`
	Domain          string                            `json:"domain"`
	Label           string                            `json:"label"`
	Enabled         bool                              `json:"enabled"`
	Status          string                            `json:"status"`
	Model           string                            `json:"model"`
	WorkspaceRoot   string                            `json:"workspaceRoot"`
	Credential      BotConnectionCredentialView       `json:"credential"`
	SessionMappings []BotConnectionSessionMappingView `json:"sessionMappings"`
	LastError       string                            `json:"lastError"`
	CreatedAt       string                            `json:"createdAt"`
	UpdatedAt       string                            `json:"updatedAt"`
}

type BotInstallStartResult struct {
	OK         bool   `json:"ok"`
	Provider   string `json:"provider"`
	Domain     string `json:"domain"`
	InstallID  string `json:"installId"`
	URL        string `json:"url"`
	DeviceCode string `json:"deviceCode"`
	UserCode   string `json:"userCode"`
	Interval   int    `json:"interval"`
	ExpireIn   int    `json:"expireIn"`
	Message    string `json:"message"`
}

type BotInstallPollResult struct {
	Done       bool              `json:"done"`
	Connection BotConnectionView `json:"connection"`
	Status     string            `json:"status"`
	Message    string            `json:"message"`
	Error      string            `json:"error"`
}

type BotConnectionDiagnostic struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	MessageID string `json:"messageId"`
}

type botInstallSession struct {
	Provider   string
	Domain     string
	DeviceCode string
	UserCode   string
	StartedAt  time.Time
	ExpireAt   time.Time
	Weixin     *weixin.LoginSession
}

func (a *App) StartBotConnectionInstall(provider, domain string) (BotInstallStartResult, error) {
	provider, domain = normalizeBotInstallTarget(provider, domain)
	if provider == "weixin" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		session, err := weixin.StartLogin(ctx)
		if err != nil {
			return BotInstallStartResult{OK: false, Provider: provider, Domain: domain, Message: err.Error()}, nil
		}
		installID := randomInstallID()
		a.mu.Lock()
		if a.botInstalls == nil {
			a.botInstalls = map[string]*botInstallSession{}
		}
		a.botInstalls[installID] = &botInstallSession{
			Provider:   provider,
			Domain:     domain,
			DeviceCode: session.QRCode,
			StartedAt:  session.StartedAt,
			ExpireAt:   time.Now().Add(2 * time.Minute),
			Weixin:     session,
		}
		a.mu.Unlock()
		return BotInstallStartResult{
			OK: true, Provider: provider, Domain: domain, InstallID: installID, URL: firstNonEmptyBot(session.QRCodeURL, session.QRCode),
			DeviceCode: session.QRCode, Interval: 3, ExpireIn: 120, Message: "请使用微信扫码完成连接。",
		}, nil
	}
	if provider != "feishu" {
		return BotInstallStartResult{OK: false, Provider: provider, Domain: domain, Message: "unsupported bot provider"}, nil
	}
	return a.startFeishuConnectionInstall(domain)
}

func (a *App) PollBotConnectionInstall(installID string) (BotInstallPollResult, error) {
	installID = strings.TrimSpace(installID)
	a.mu.RLock()
	session := a.botInstalls[installID]
	a.mu.RUnlock()
	if session == nil {
		return BotInstallPollResult{Error: "install session not found"}, nil
	}
	if time.Now().After(session.ExpireAt) {
		a.deleteBotInstall(installID)
		return BotInstallPollResult{Status: "expired", Error: "install session expired"}, nil
	}
	if session.Provider == "weixin" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		result, status, err := weixin.PollLogin(ctx, session.Weixin)
		if err != nil {
			return BotInstallPollResult{Status: status, Error: err.Error()}, nil
		}
		if result == nil {
			return BotInstallPollResult{Status: status, Message: weixinInstallStatusMessage(status)}, nil
		}
		a.deleteBotInstall(installID)
		installerUserID := strings.TrimSpace(result.UserID)
		conn, err := a.upsertBotConnection(config.BotConnectionConfig{
			ID:         connectionID("weixin", "weixin"),
			Provider:   "weixin",
			Domain:     "weixin",
			Label:      "微信",
			Enabled:    true,
			Status:     "connected",
			Credential: config.BotConnectionCredential{AccountID: result.AccountID, TokenEnv: "WEIXIN_BOT_TOKEN"},
		}, func(c *config.Config) {
			c.Bot.Enabled = true
			c.Bot.Weixin.Enabled = true
			c.Bot.Weixin.AccountID = result.AccountID
			c.Bot.Weixin.APIBase = result.BaseURL
			if c.Bot.Weixin.TokenEnv == "" {
				c.Bot.Weixin.TokenEnv = "WEIXIN_BOT_TOKEN"
			}
			// 自动将安装者加入白名单
			if installerUserID != "" {
				c.Bot.Allowlist.Enabled = true
				c.Bot.Allowlist.AllowAll = false
				c.Bot.Allowlist.WeixinUsers = prependUniqueString(c.Bot.Allowlist.WeixinUsers, installerUserID)
			}
		})
		if err != nil {
			return BotInstallPollResult{Status: "error", Error: err.Error()}, nil
		}
		return BotInstallPollResult{Done: true, Status: "connected", Connection: conn, Message: "微信已连接。"}, nil
	}
	return a.pollFeishuConnectionInstall(installID, session)
}

// CompleteTelegramBotConnection 用用户粘贴的 Bot Token 完成 Telegram 连接（一步到位）。
// Telegram 不像飞书需要 OAuth、微信需要扫码——它只需一个 @BotFather 颁发的静态 token，
// 所以不走 StartBotConnectionInstall/PollBotConnectionInstall 两阶段，直接校验 + 落盘。
// 返回连接视图供前端刷新 UI。
func (a *App) CompleteTelegramBotConnection(token string) (BotInstallPollResult, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return BotInstallPollResult{Status: "error", Error: "请输入 Telegram Bot Token。"}, nil
	}
	// 用 getMe 校验 token 有效性，拿到 bot username 做连接标签
	cfg, err := config.Load()
	if err != nil {
		return BotInstallPollResult{Status: "error", Error: err.Error()}, nil
	}
	tgCfg := cfg.Bot.Telegram
	if strings.TrimSpace(tgCfg.TokenEnv) == "" {
		tgCfg.TokenEnv = "TELEGRAM_BOT_TOKEN"
	}
	// 临时把 token 写进环境变量，让 VerifyToken 能读到
	if err := os.Setenv(tgCfg.TokenEnv, token); err != nil {
		return BotInstallPollResult{Status: "error", Error: err.Error()}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	username, err := telegram.VerifyToken(ctx, tgCfg)
	if err != nil {
		return BotInstallPollResult{Status: "error", Error: "Token 校验失败：" + err.Error()}, nil
	}
	label := "Telegram"
	if username != "" {
		label = "Telegram @" + username
	}
	conn, err := a.upsertBotConnection(config.BotConnectionConfig{
		ID:         connectionID("telegram", "telegram"),
		Provider:   "telegram",
		Domain:     "telegram",
		Label:      label,
		Enabled:    true,
		Status:     "connected",
		Credential: config.BotConnectionCredential{TokenEnv: tgCfg.TokenEnv},
	}, func(c *config.Config) {
		c.Bot.Enabled = true
		c.Bot.Telegram.Enabled = true
		if c.Bot.Telegram.TokenEnv == "" {
			c.Bot.Telegram.TokenEnv = tgCfg.TokenEnv
		}
	})
	if err != nil {
		return BotInstallPollResult{Status: "error", Error: err.Error()}, nil
	}
	return BotInstallPollResult{Done: true, Status: "connected", Connection: conn, Message: "Telegram 已连接。"}, nil
}

func (a *App) DiagnoseBotConnection(id string) (BotConnectionDiagnostic, error) {
	cfg, err := config.Load()
	if err != nil {
		return BotConnectionDiagnostic{ID: id, Status: "error", Message: err.Error()}, nil
	}
	for _, conn := range cfg.Bot.Connections {
		if conn.ID == id {
			status := "ok"
			message := "连接配置已保存。"
			if !conn.Enabled {
				status = "disabled"
				message = "连接已保存但未启用。"
			} else if conn.Status != "connected" {
				status = firstNonEmptyBot(conn.Status, "pending")
				message = firstNonEmptyBot(conn.LastError, "连接还未完成。")
			} else if conn.Credential.AppSecretEnv != "" && strings.TrimSpace(conn.Credential.AppSecretEnv) != "" && !envIsSet(conn.Credential.AppSecretEnv) {
				status = "warning"
				message = conn.Credential.AppSecretEnv + " 未设置。"
			}
			return BotConnectionDiagnostic{ID: conn.ID, Label: conn.Label, Status: status, Message: message}, nil
		}
	}
	return BotConnectionDiagnostic{ID: id, Status: "missing", Message: "未找到连接。"}, nil
}

func (a *App) TestBotConnection(id, target string) (BotConnectionDiagnostic, error) {
	cfg, err := config.Load()
	if err != nil {
		return BotConnectionDiagnostic{ID: id, Status: "error", Message: err.Error()}, nil
	}
	var conn *config.BotConnectionConfig
	for i := range cfg.Bot.Connections {
		if cfg.Bot.Connections[i].ID == strings.TrimSpace(id) {
			conn = &cfg.Bot.Connections[i]
			break
		}
	}
	if conn == nil {
		return BotConnectionDiagnostic{ID: id, Status: "missing", Message: "未找到连接。"}, nil
	}
	target = firstNonEmptyBot(strings.TrimSpace(target), firstSessionRemoteID(conn.SessionMappings))
	if conn.Provider != "feishu" && conn.Provider != "weixin" && conn.Provider != "telegram" {
		return BotConnectionDiagnostic{ID: conn.ID, Label: conn.Label, Status: "warning", Message: "当前渠道暂不支持桌面端主动发送测试消息，可使用诊断检查基础配置。"}, nil
	}
	if target == "" {
		return BotConnectionDiagnostic{ID: conn.ID, Label: conn.Label, Status: "warning", Message: "请输入测试会话 ID 后再发送测试消息。"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var result bot.SendResult
	switch conn.Provider {
	case "feishu":
		feishuCfg := cfg.Bot.Feishu
		feishuCfg.Enabled = true
		feishuCfg.Domain = firstNonEmptyBot(conn.Domain, feishuCfg.Domain)
		feishuCfg.AppID = firstNonEmptyBot(conn.Credential.AppID, feishuCfg.AppID)
		feishuCfg.AppSecretEnv = firstNonEmptyBot(conn.Credential.AppSecretEnv, feishuCfg.AppSecretEnv)
		result, err = feishu.SendText(ctx, feishuCfg, target, "hiq bot 测试消息：连接和发送链路可用。")
	case "weixin":
		weixinCfg := cfg.Bot.Weixin
		weixinCfg.Enabled = true
		weixinCfg.AccountID = firstNonEmptyBot(conn.Credential.AccountID, weixinCfg.AccountID)
		weixinCfg.TokenEnv = firstNonEmptyBot(conn.Credential.TokenEnv, weixinCfg.TokenEnv)
		result, err = weixin.SendText(ctx, weixinCfg, target, "hiq bot 测试消息：连接和发送链路可用。")
	case "telegram":
		telegramCfg := cfg.Bot.Telegram
		telegramCfg.Enabled = true
		telegramCfg.TokenEnv = firstNonEmptyBot(conn.Credential.TokenEnv, telegramCfg.TokenEnv)
		result, err = telegram.SendText(ctx, telegramCfg, target, "hiq bot 测试消息：连接和发送链路可用。")
	}
	if err != nil {
		return BotConnectionDiagnostic{ID: conn.ID, Label: conn.Label, Status: "error", Message: err.Error()}, nil
	}
	_ = a.rememberBotConnectionRemote(conn.ID, target, "", "", "")
	msg := "测试消息已发送。"
	if result.MessageID != "" {
		msg += " Message ID: " + result.MessageID
	}
	return BotConnectionDiagnostic{ID: conn.ID, Label: conn.Label, Status: "ok", Message: msg, MessageID: result.MessageID}, nil
}

func (a *App) startFeishuConnectionInstall(domain string) (BotInstallStartResult, error) {
	base := feishuAccountsBase(domain)
	if _, err := postFeishuInstallForm(base, map[string]string{"action": "init"}); err != nil {
		return BotInstallStartResult{OK: false, Provider: "feishu", Domain: domain, Message: err.Error()}, nil
	}
	data, err := postFeishuInstallForm(base, map[string]string{
		"action": "begin", "archetype": "PersonalAgent", "auth_method": "client_secret", "request_user_info": "open_id",
	})
	if err != nil {
		return BotInstallStartResult{OK: false, Provider: "feishu", Domain: domain, Message: err.Error()}, nil
	}
	deviceCode := stringValue(data["device_code"])
	verifyURL := stringValue(data["verification_uri_complete"])
	userCode := stringValue(data["user_code"])
	if deviceCode == "" || verifyURL == "" {
		return BotInstallStartResult{OK: false, Provider: "feishu", Domain: domain, Message: "飞书/Lark 授权响应缺少 device_code 或二维码 URL。"}, nil
	}
	installID := randomInstallID()
	interval := intValue(data["interval"], 5)
	expireIn := intValue(firstAny(data["expire_in"], data["expires_in"]), 300)
	a.mu.Lock()
	if a.botInstalls == nil {
		a.botInstalls = map[string]*botInstallSession{}
	}
	a.botInstalls[installID] = &botInstallSession{
		Provider: "feishu", Domain: domain, DeviceCode: deviceCode, UserCode: userCode,
		StartedAt: time.Now(), ExpireAt: time.Now().Add(time.Duration(expireIn) * time.Second),
	}
	a.mu.Unlock()
	return BotInstallStartResult{OK: true, Provider: "feishu", Domain: domain, InstallID: installID, URL: verifyURL, DeviceCode: deviceCode, UserCode: userCode, Interval: interval, ExpireIn: expireIn}, nil
}

func (a *App) pollFeishuConnectionInstall(installID string, session *botInstallSession) (BotInstallPollResult, error) {
	data, statusCode, err := postFeishuInstallFormResult(feishuAccountsBase(session.Domain), map[string]string{"action": "poll", "device_code": session.DeviceCode})
	if err != nil {
		return BotInstallPollResult{Status: "error", Error: err.Error()}, nil
	}
	if errText := stringValue(data["error"]); errText != "" {
		if errText == "authorization_pending" || errText == "slow_down" {
			return BotInstallPollResult{Status: "pending", Message: "等待扫码授权。"}, nil
		}
		a.deleteBotInstall(installID)
		return BotInstallPollResult{Status: "error", Error: firstNonEmptyBot(stringValue(data["error_description"]), errText)}, nil
	}
	if statusCode >= 400 {
		a.deleteBotInstall(installID)
		return BotInstallPollResult{Status: "error", Error: fmt.Sprintf("HTTP %d", statusCode)}, nil
	}
	appID := stringValue(data["client_id"])
	appSecret := stringValue(data["client_secret"])
	if appID == "" || appSecret == "" {
		return BotInstallPollResult{Status: "pending", Message: "等待授权完成。"}, nil
	}
	a.deleteBotInstall(installID)
	domain := feishuInstallDomain(session.Domain, data)
	secretEnv := "FEISHU_BOT_APP_SECRET"
	if domain == "lark" {
		secretEnv = "LARK_BOT_APP_SECRET"
	}
	if err := upsertCredential(secretEnv, appSecret); err != nil {
		return BotInstallPollResult{Status: "error", Error: err.Error()}, nil
	}
	label := "飞书"
	if domain == "lark" {
		label = "Lark"
	}
	// 从 OAuth 响应中提取安装者的 open_id，自动加入白名单
	var installerOpenID string
	if userInfo, ok := data["user_info"].(map[string]any); ok {
		installerOpenID = strings.TrimSpace(stringValue(userInfo["open_id"]))
	}

	conn, err := a.upsertBotConnection(config.BotConnectionConfig{
		ID:         connectionID("feishu", domain),
		Provider:   "feishu",
		Domain:     domain,
		Label:      label,
		Enabled:    true,
		Status:     "connected",
		Credential: config.BotConnectionCredential{AppID: appID, AppSecretEnv: secretEnv},
	}, func(c *config.Config) {
		c.Bot.Enabled = true
		c.Bot.Feishu.Enabled = true
		c.Bot.Feishu.Domain = domain
		c.Bot.Feishu.AppID = appID
		c.Bot.Feishu.AppSecretEnv = secretEnv
		c.Bot.Feishu.Mode = "websocket"
		c.Bot.Feishu.RequireMention = true
		// 自动将安装者加入白名单
		if installerOpenID != "" {
			c.Bot.Allowlist.Enabled = true
			c.Bot.Allowlist.AllowAll = false
			c.Bot.Allowlist.FeishuUsers = prependUniqueString(c.Bot.Allowlist.FeishuUsers, installerOpenID)
		}
	})
	if err != nil {
		return BotInstallPollResult{Status: "error", Error: err.Error()}, nil
	}
	return BotInstallPollResult{Done: true, Status: "connected", Connection: conn, Message: label + " 已连接。"}, nil
}

func (a *App) upsertBotConnection(conn config.BotConnectionConfig, updateLegacy func(*config.Config)) (BotConnectionView, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if conn.CreatedAt == "" {
		conn.CreatedAt = now
	}
	conn.UpdatedAt = now
	if conn.Status == "" {
		conn.Status = "connected"
	}
	if conn.ID == "" {
		conn.ID = connectionID(conn.Provider, conn.Domain)
	}
	err := a.applyConfigOnly(func(c *config.Config) error {
		if updateLegacy != nil {
			updateLegacy(c)
		}
		replaced := false
		for i, existing := range c.Bot.Connections {
			if existing.ID == conn.ID {
				conn.CreatedAt = firstNonEmptyBot(existing.CreatedAt, conn.CreatedAt)
				c.Bot.Connections[i] = conn
				replaced = true
				break
			}
		}
		if !replaced {
			c.Bot.Connections = append(c.Bot.Connections, conn)
		}
		return nil
	})
	return botConnectionView(conn), err
}

func (a *App) rememberBotConnectionRemote(id, remoteID, chatType, chatID, sessionPath string) error {
	id = strings.TrimSpace(id)
	remoteID = strings.TrimSpace(remoteID)
	chatType = strings.TrimSpace(chatType)
	chatID = strings.TrimSpace(chatID)
	sessionPath = strings.TrimSpace(sessionPath)
	if id == "" || remoteID == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	return a.applyConfigOnly(func(c *config.Config) error {
		for i := range c.Bot.Connections {
			if c.Bot.Connections[i].ID != id {
				continue
			}
			for j := range c.Bot.Connections[i].SessionMappings {
				m := &c.Bot.Connections[i].SessionMappings[j]
				// 同一对话才命中：remoteID + chatType + chatID 三者全等。这样同一
				// 个人在不同群里的对话各自独立成条，而不是互相覆盖。
				if m.RemoteID == remoteID && m.ChatType == chatType && m.ChatID == chatID {
					// 已有该对话的映射：仅在拿到非空 sessionPath 且当前 SessionID 为
					// 空时补填「本地话题」，避免反复写盘。
					if sessionPath != "" && strings.TrimSpace(m.SessionID) == "" {
						m.SessionID = "path:" + sessionPath
						c.Bot.Connections[i].UpdatedAt = now
					}
					return nil
				}
			}
			scope := botMappingScope("", c.Bot.Connections[i].WorkspaceRoot)
			c.Bot.Connections[i].SessionMappings = append(c.Bot.Connections[i].SessionMappings, config.BotConnectionSessionMapping{
				RemoteID:      remoteID,
				ChatType:      chatType,
				ChatID:        chatID,
				SessionID:     botMappingSessionID(sessionPath),
				Scope:         scope,
				WorkspaceRoot: botMappingWorkspaceRoot(scope, c.Bot.Connections[i].WorkspaceRoot),
				UpdatedAt:     now,
			})
			c.Bot.Connections[i].UpdatedAt = now
			return nil
		}
		return nil
	})
}

// botMappingSessionID 把会话 transcript 路径转成 UI「本地话题」可识别的 SessionID。
// mappedSessionTarget 认 "path:xxx" 前缀，点击「打开对应会话」会用 resumeSession 打开。
func botMappingSessionID(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return "path:" + sessionPath
}

func firstSessionRemoteID(mappings []config.BotConnectionSessionMapping) string {
	for _, mapping := range mappings {
		if strings.TrimSpace(mapping.RemoteID) != "" {
			return strings.TrimSpace(mapping.RemoteID)
		}
	}
	return ""
}

// screenshotPushDest picks the best "platform:remoteID" push destination for a
// screenshot-recognition result. It prefers a connected feishu or weixin
// connection that has a known conversation (a remembered SessionMappings
// remoteID), so the result lands where the user actually talks to the bot.
// Returns "" when nothing suitable is configured — callers should then skip
// the push (best-effort). The im_send tool (builtin.defaultIMPushDest) uses the
// same logic; both kept in sync.
func (a *App) screenshotPushDest() string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	for _, conn := range cfg.Bot.Connections {
		if !conn.Enabled || conn.Status != "connected" {
			continue
		}
		if conn.Provider != "feishu" && conn.Provider != "weixin" && conn.Provider != "telegram" {
			continue // qq etc. don't support desktop-initiated sends
		}
		if remote := firstSessionRemoteID(conn.SessionMappings); remote != "" {
			return conn.Provider + ":" + remote
		}
	}
	return ""
}

func (a *App) deleteBotInstall(installID string) {
	a.mu.Lock()
	delete(a.botInstalls, installID)
	a.mu.Unlock()
}

func normalizeBotInstallTarget(provider, domain string) (string, string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	domain = strings.ToLower(strings.TrimSpace(domain))
	if provider == "lark" {
		provider = "feishu"
		domain = "lark"
	}
	if provider == "weixin" || provider == "wechat" {
		return "weixin", "weixin"
	}
	if domain != "lark" {
		domain = "feishu"
	}
	return "feishu", domain
}

func feishuAccountsBase(domain string) string {
	if domain == "lark" {
		return "https://accounts.larksuite.com"
	}
	return "https://accounts.feishu.cn"
}

func postFeishuInstallForm(base string, body map[string]string) (map[string]any, error) {
	data, status, err := postFeishuInstallFormResult(base, body)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", status, firstNonEmptyBot(stringValue(data["error_description"]), stringValue(data["message"])))
	}
	return data, nil
}

func postFeishuInstallFormResult(base string, body map[string]string) (map[string]any, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	reqBody := url.Values{}
	for k, v := range body {
		reqBody.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/oauth/v1/app/registration", strings.NewReader(reqBody.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, resp.StatusCode, err
	}
	return out, resp.StatusCode, nil
}

func botConnectionView(conn config.BotConnectionConfig) BotConnectionView {
	return BotConnectionView{
		ID: conn.ID, Provider: conn.Provider, Domain: conn.Domain, Label: conn.Label, Enabled: conn.Enabled, Status: conn.Status,
		Model: conn.Model, WorkspaceRoot: conn.WorkspaceRoot,
		Credential: BotConnectionCredentialView{
			AppID: conn.Credential.AppID, AppSecretEnv: conn.Credential.AppSecretEnv, AccountID: conn.Credential.AccountID, TokenEnv: conn.Credential.TokenEnv,
			SecretSet: botCredentialSecretSet(conn),
		},
		SessionMappings: botSessionMappingViews(conn.SessionMappings, conn.WorkspaceRoot),
		LastError:       conn.LastError, CreatedAt: conn.CreatedAt, UpdatedAt: conn.UpdatedAt,
	}
}

func botCredentialSecretSet(conn config.BotConnectionConfig) bool {
	if conn.Credential.AppSecretEnv != "" {
		return envIsSet(conn.Credential.AppSecretEnv)
	}
	if conn.Credential.TokenEnv != "" && envIsSet(conn.Credential.TokenEnv) {
		return true
	}
	if conn.Provider == "weixin" {
		return weixin.HasSavedAccount(conn.Credential.AccountID)
	}
	return false
}

func feishuInstallDomain(fallback string, data map[string]any) string {
	if userInfo, ok := data["user_info"].(map[string]any); ok {
		if strings.EqualFold(stringValue(userInfo["tenant_brand"]), "lark") {
			return "lark"
		}
		return "feishu"
	}
	if strings.EqualFold(fallback, "lark") {
		return "lark"
	}
	return "feishu"
}

func botConnectionViews(connections []config.BotConnectionConfig) []BotConnectionView {
	if connections == nil {
		return []BotConnectionView{}
	}
	out := make([]BotConnectionView, 0, len(connections))
	for _, conn := range connections {
		out = append(out, botConnectionView(conn))
	}
	return out
}

func botConnectionConfig(view BotConnectionView) config.BotConnectionConfig {
	return config.BotConnectionConfig{
		ID:            strings.TrimSpace(view.ID),
		Provider:      strings.TrimSpace(view.Provider),
		Domain:        strings.TrimSpace(view.Domain),
		Label:         strings.TrimSpace(view.Label),
		Enabled:       view.Enabled,
		Status:        strings.TrimSpace(view.Status),
		Model:         strings.TrimSpace(view.Model),
		WorkspaceRoot: strings.TrimSpace(view.WorkspaceRoot),
		Credential: config.BotConnectionCredential{
			AppID:        strings.TrimSpace(view.Credential.AppID),
			AppSecretEnv: strings.TrimSpace(view.Credential.AppSecretEnv),
			AccountID:    strings.TrimSpace(view.Credential.AccountID),
			TokenEnv:     strings.TrimSpace(view.Credential.TokenEnv),
		},
		SessionMappings: botSessionMappingConfigs(view.SessionMappings, view.WorkspaceRoot),
		LastError:       strings.TrimSpace(view.LastError),
		CreatedAt:       strings.TrimSpace(view.CreatedAt),
		UpdatedAt:       strings.TrimSpace(view.UpdatedAt),
	}
}

func botConnectionConfigs(views []BotConnectionView) []config.BotConnectionConfig {
	if views == nil {
		return nil
	}
	out := make([]config.BotConnectionConfig, 0, len(views))
	for _, view := range views {
		cfg := botConnectionConfig(view)
		if cfg.ID == "" || cfg.Provider == "" {
			continue
		}
		out = append(out, cfg)
	}
	return out
}

func botMappingScope(scope, workspaceRoot string) string {
	if strings.TrimSpace(scope) == "project" {
		return "project"
	}
	if strings.TrimSpace(workspaceRoot) != "" {
		return "project"
	}
	return "global"
}

func botMappingWorkspaceRoot(scope, workspaceRoot string) string {
	if botMappingScope(scope, workspaceRoot) != "project" {
		return ""
	}
	return strings.TrimSpace(workspaceRoot)
}

func botSessionMappingViews(mappings []config.BotConnectionSessionMapping, connectionWorkspaceRoot string) []BotConnectionSessionMappingView {
	if mappings == nil {
		return []BotConnectionSessionMappingView{}
	}
	out := make([]BotConnectionSessionMappingView, 0, len(mappings))
	for _, m := range mappings {
		workspaceRoot := firstNonEmptyBot(m.WorkspaceRoot, connectionWorkspaceRoot)
		scope := botMappingScope(m.Scope, workspaceRoot)
		out = append(out, BotConnectionSessionMappingView{
			RemoteID:      m.RemoteID,
			ChatType:      m.ChatType,
			ChatID:        m.ChatID,
			SessionID:     m.SessionID,
			Scope:         scope,
			WorkspaceRoot: botMappingWorkspaceRoot(scope, workspaceRoot),
			UpdatedAt:     m.UpdatedAt,
		})
	}
	return out
}

func botSessionMappingConfigs(mappings []BotConnectionSessionMappingView, connectionWorkspaceRoot string) []config.BotConnectionSessionMapping {
	if mappings == nil {
		return nil
	}
	out := make([]config.BotConnectionSessionMapping, 0, len(mappings))
	for _, m := range mappings {
		workspaceRoot := firstNonEmptyBot(m.WorkspaceRoot, connectionWorkspaceRoot)
		scope := botMappingScope(m.Scope, workspaceRoot)
		out = append(out, config.BotConnectionSessionMapping{
			RemoteID:      strings.TrimSpace(m.RemoteID),
			ChatType:      strings.TrimSpace(m.ChatType),
			ChatID:        strings.TrimSpace(m.ChatID),
			SessionID:     strings.TrimSpace(m.SessionID),
			Scope:         scope,
			WorkspaceRoot: botMappingWorkspaceRoot(scope, workspaceRoot),
			UpdatedAt:     strings.TrimSpace(m.UpdatedAt),
		})
	}
	return out
}

func connectionID(provider, domain string) string {
	return strings.Trim(strings.ToLower(provider+"-"+domain), "-")
}

func randomInstallID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("install-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func envIsSet(name string) bool {
	return strings.TrimSpace(name) != "" && strings.TrimSpace(os.Getenv(name)) != ""
}

func firstAny(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func firstNonEmptyBot(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func intValue(value any, fallback int) int {
	switch v := value.(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func weixinInstallStatusMessage(status string) string {
	switch status {
	case "scaned":
		return "已扫码，请在微信里确认。"
	case "scaned_but_redirect":
		return "已扫码，正在切换微信授权节点。"
	default:
		return "等待扫码。"
	}
}
