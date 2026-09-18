package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/bot"
	"github.com/zzycxz/hiq/internal/config"
)

const (
	// tgDefaultAPIBase 是 Telegram 官方 Bot API 端点。可被 cfg.APIBase 覆盖（自建反代/本地 Bot API Server）。
	tgDefaultAPIBase = "https://api.telegram.org"
	// tgLongPollTimeout 是 getUpdates 的长轮询超时（秒），也是事实上的心跳间隔。
	tgLongPollTimeout = 30
)

// apiBase 返回配置的 API base，空则用官方端点。
func (a *adapter) apiBase() string {
	if b := strings.TrimRight(a.cfg.APIBase, "/"); b != "" {
		return b
	}
	return tgDefaultAPIBase
}

// apiURL 拼出 {base}/bot{token}/{method}，token 在 URL 路径里（Telegram 鉴权方式）。
func (a *adapter) apiURL(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", a.apiBase(), a.token, method)
}

// fileURL 拼出文件下载地址：{base}/file/bot{token}/{file_path}。
func (a *adapter) fileURL(filePath string) string {
	return fmt.Sprintf("%s/file/bot%s/%s", a.apiBase(), a.token, filePath)
}

// tgUpdate 是 getUpdates 返回的单条 update。
type tgUpdate struct {
	UpdateID      int64           `json:"update_id"`
	Message       *tgMessage      `json:"message,omitempty"`
	CallbackQuery *tgCallbackQuery `json:"callback_query,omitempty"`
}

// tgMessage 是 Telegram 的 message 对象（仅取 hiq 需要的字段）。
type tgMessage struct {
	MessageID int64    `json:"message_id"`
	Text      string   `json:"text"`
	Caption   string   `json:"caption"` // 图片/文件的附注文字
	From      tgUser   `json:"from"`
	Chat      tgChat   `json:"chat"`
	Photo     []tgPhotoSize `json:"photo,omitempty"`
}

type tgUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type tgChat struct {
	ID     int64  `json:"id"`
	Type   string `json:"type"` // private | group | supergroup | channel
	Title  string `json:"title"`
}

// tgPhotoSize 是 message.photo[] 的单项，file_size 最大的就是原图。
type tgPhotoSize struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// tgCallbackQuery 是用户点击 inline keyboard 按钮时产生的事件。
type tgCallbackQuery struct {
	ID      string    `json:"id"`
	From    tgUser    `json:"from"`
	Data    string    `json:"data"` // = 按钮的 callback_data，hiq 复用为斜杠命令
	Message *tgMessage `json:"message,omitempty"` // 按钮所在的原消息
}

// pollLoop 是接收主循环：照抄 QQ gatewayLoop 的「外层 for + ctx.Done + Sleep 退避」骨架，
// 把 WebSocket 握手/心跳/事件读取整段替换成 getUpdates 长轮询。
func (a *adapter) pollLoop(ctx context.Context) {
	var offset int64 // 已确认处理的最后一个 update_id；下次请求传 offset = last+1
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		updates, err := a.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return // 关闭中，错误是 cancel 导致的，不重试
			}
			a.logger.Error("getUpdates failed", "err", err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, u := range updates {
			a.handleUpdate(ctx, u)
			offset = u.UpdateID + 1
		}
	}
}

// getUpdates 调用 Telegram getUpdates long polling。
func (a *adapter) getUpdates(ctx context.Context, offset int64) ([]tgUpdate, error) {
	u := a.apiURL("getUpdates")
	q := url.Values{}
	q.Set("offset", strconv.FormatInt(offset, 10))
	q.Set("timeout", strconv.Itoa(tgLongPollTimeout))
	// allowed_updates 只订阅消息和回调（按钮点击），不拉 edited_message/channel_post 等噪声
	q.Set("allowed_updates", `["message","callback_query"]`)
	resp, err := a.httpDo(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("telegram getUpdates error %d: %s", resp.StatusCode, string(body))
	}
	var apiResp struct {
		OK          bool       `json:"ok"`
		Description string     `json:"description"`
		Result      []tgUpdate `json:"result"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("decode getUpdates: %w", err)
	}
	if !apiResp.OK {
		return nil, fmt.Errorf("telegram getUpdates not ok: %s", apiResp.Description)
	}
	return apiResp.Result, nil
}

// handleUpdate 把一条 update 分流到 message 或 callback_query 处理。
func (a *adapter) handleUpdate(ctx context.Context, u tgUpdate) {
	switch {
	case u.Message != nil:
		a.handleMessage(ctx, u.Message)
	case u.CallbackQuery != nil:
		a.handleCallbackQuery(ctx, u.CallbackQuery)
	}
}

// handleMessage 把 Telegram message 翻译成 bot.InboundMessage 并投递。
func (a *adapter) handleMessage(ctx context.Context, m *tgMessage) {
	ib := bot.InboundMessage{
		Platform:  bot.PlatformTelegram,
		UserID:    strconv.FormatInt(m.From.ID, 10),
		UserName:  m.From.Username,
		Text:      m.Text,
		MessageID: strconv.FormatInt(m.MessageID, 10),
		ChatID:    strconv.FormatInt(m.Chat.ID, 10),
	}
	if ib.UserName == "" {
		// Telegram 很多用户没设 username，回退用 first_name 保证 UserName 非空
		ib.UserName = m.From.FirstName
	}
	// chat.type 分流：private→DM，group/supergroup→group，channel→guild（单向广播语义最接近）
	switch m.Chat.Type {
	case "private":
		ib.ChatType = bot.ChatDM
	case "group", "supergroup":
		ib.ChatType = bot.ChatGroup
	case "channel":
		ib.ChatType = bot.ChatGuild
	default:
		ib.ChatType = bot.ChatDM
	}
	// caption 作为文本补充（用户发图时配的文字）
	if ib.Text == "" && m.Caption != "" {
		ib.Text = m.Caption
	}
	// 图片支持：取 photo[] 里 file_size 最大的（原图），getFile 拼成可访问 URL
	if len(m.Photo) > 0 {
		if fileURL := a.largestPhotoURL(ctx, m.Photo); fileURL != "" {
			ib.MediaURLs = []string{fileURL}
		}
	}

	select {
	case a.msgCh <- ib:
	default:
		a.logger.Warn("message channel full, dropping message")
	}
}

// handleCallbackQuery 处理审批按钮点击。
// 关键设计：render.go 把审批按钮的 CallbackID 设成 "/approve <id>" 斜杠命令本身（见 approvalKeyboard），
// 所以这里把 callback_data 当作 InboundMessage.Text 投递，网关现有斜杠命令分发原样工作。
// 同时必须调 answerCallbackQuery，否则按钮会一直显示 loading 转圈（Telegram 协议要求应答）。
func (a *adapter) handleCallbackQuery(ctx context.Context, cq *tgCallbackQuery) {
	var chatID string
	if cq.Message != nil {
		chatID = strconv.FormatInt(cq.Message.Chat.ID, 10)
	}
	if chatID != "" && cq.Data != "" {
		ib := bot.InboundMessage{
			Platform:  bot.PlatformTelegram,
			ChatType:  bot.ChatDM,
			ChatID:    chatID,
			UserID:    strconv.FormatInt(cq.From.ID, 10),
			UserName:  cq.From.Username,
			Text:      cq.Data, // 形如 "/approve <id>" 或 "/deny <id>"
			MessageID: cq.ID,
		}
		select {
		case a.msgCh <- ib:
		default:
			a.logger.Warn("message channel full, dropping callback")
		}
	}
	// 应答 callback query，消除按钮 loading。show_alert=false 静默应答。
	_ = a.callGetNoResult(ctx, a.apiURL("answerCallbackQuery"), map[string]string{
		"callback_query_id": cq.ID,
	})
}

// largestPhotoURL 取 photo[] 中最大尺寸的下载 URL。
func (a *adapter) largestPhotoURL(ctx context.Context, photos []tgPhotoSize) string {
	if len(photos) == 0 {
		return ""
	}
	best := photos[0]
	for _, p := range photos[1:] {
		if p.FileSize > best.FileSize {
			best = p
		}
	}
	filePath, err := a.getFilePath(ctx, best.FileID)
	if err != nil {
		a.logger.Warn("getFile failed", "file_id", best.FileID, "err", err)
		return ""
	}
	return a.fileURL(filePath)
}

// getFilePath 调用 getFile 拿 file_path，再用它拼下载 URL。
func (a *adapter) getFilePath(ctx context.Context, fileID string) (string, error) {
	u := a.apiURL("getFile") + "?file_id=" + url.QueryEscape(fileID)
	resp, err := a.httpDo(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("telegram getFile error %d: %s", resp.StatusCode, string(body))
	}
	var apiResp struct {
		OK          bool `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", fmt.Errorf("decode getFile: %w", err)
	}
	if !apiResp.OK || apiResp.Result.FilePath == "" {
		return "", fmt.Errorf("telegram getFile not ok: %s", apiResp.Description)
	}
	return apiResp.Result.FilePath, nil
}

// sendMessage 调用 Telegram sendMessage REST。照抄 QQ sendMessage 的骨架，
// payload 改为 {chat_id, text, parse_mode}，审批按钮翻译成 reply_markup.inline_keyboard。
func (a *adapter) sendMessage(ctx context.Context, msg bot.OutboundMessage) (bot.SendResult, error) {
	payload := map[string]any{
		"chat_id":    msg.ChatID,
		"text":       msg.Text,
		"parse_mode": "Markdown", // render.go 输出含 **/` 等 markdown，Telegram 原生支持
	}

	// 审批按钮翻译：bot.InlineKeyboard.Rows[] → reply_markup.inline_keyboard[][]
	if msg.Keyboard != nil {
		keyboard := make([][]map[string]string, 0, len(msg.Keyboard.Rows))
		for _, row := range msg.Keyboard.Rows {
			buttons := make([]map[string]string, 0, len(row.Buttons))
			for _, btn := range row.Buttons {
				buttons = append(buttons, map[string]string{
					"text":          btn.Label,
					"callback_data": btn.CallbackID,
				})
			}
			keyboard = append(keyboard, buttons)
		}
		payload["reply_markup"] = map[string]any{"inline_keyboard": keyboard}
	}

	if msg.ReplyToMsgID != "" {
		// Telegram 要求 reply_to_message_id 是整数
		if msgID, err := strconv.ParseInt(msg.ReplyToMsgID, 10, 64); err == nil {
			payload["reply_to_message_id"] = msgID
		}
	}

	resp, err := a.postJSON(ctx, a.apiURL("sendMessage"), payload)
	if err != nil {
		return bot.SendResult{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return bot.SendResult{}, fmt.Errorf("telegram sendMessage error %d: %s", resp.StatusCode, string(body))
	}
	var apiResp struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return bot.SendResult{}, fmt.Errorf("decode sendMessage: %w", err)
	}
	if !apiResp.OK {
		return bot.SendResult{}, fmt.Errorf("telegram sendMessage not ok: %s", apiResp.Description)
	}
	return bot.SendResult{MessageID: strconv.FormatInt(apiResp.Result.MessageID, 10)}, nil
}

// --- 桌面端连接管理用的导出辅助函数 ---

// SendText 是桌面端 TestBotConnection / 主动推送用的便捷函数（照抄 feishu/weixin 的同名函数形态）。
// 它不依赖 adapter 实例（独立读 token），所以 TestBotConnection 不必先 Start adapter。
func SendText(ctx context.Context, cfg config.TelegramBotConfig, chatID, text string) (bot.SendResult, error) {
	a := &adapter{
		cfg:     cfg,
		httpCli: &http.Client{Timeout: 15 * time.Second},
		token:   tokenFromConfig(cfg),
	}
	if a.token == "" {
		return bot.SendResult{}, fmt.Errorf("telegram bot token not set: env %s is empty", cfg.TokenEnv)
	}
	return a.sendMessage(ctx, bot.OutboundMessage{ChatID: chatID, Text: text})
}

// VerifyToken 调用 getMe 校验 token 有效性，返回 bot 的 username。
// 供桌面 install 流在保存连接前校验用户粘贴的 token。
func VerifyToken(ctx context.Context, cfg config.TelegramBotConfig) (string, error) {
	a := &adapter{
		cfg:     cfg,
		httpCli: &http.Client{Timeout: 10 * time.Second},
		token:   tokenFromConfig(cfg),
	}
	if a.token == "" {
		return "", fmt.Errorf("telegram bot token not set: env %s is empty", cfg.TokenEnv)
	}
	u := a.apiURL("getMe")
	resp, err := a.httpDo(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("telegram getMe error %d: %s", resp.StatusCode, string(body))
	}
	var apiResp struct {
		OK     bool   `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", fmt.Errorf("decode getMe: %w", err)
	}
	if !apiResp.OK {
		return "", fmt.Errorf("telegram getMe not ok: %s", apiResp.Description)
	}
	return apiResp.Result.Username, nil
}

// --- HTTP 工具函数 ---

func tokenFromConfig(cfg config.TelegramBotConfig) string {
	return strings.TrimSpace(os.Getenv(cfg.TokenEnv))
}

// httpDo 发起带 context 的 HTTP 请求。body 为 nil 时 GET/DELETE，否则用 raw body。
func (a *adapter) httpDo(ctx context.Context, method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	return a.httpCli.Do(req)
}

// postJSON POST JSON 请求体。
func (a *adapter) postJSON(ctx context.Context, url string, payload map[string]any) (*http.Response, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return a.httpCli.Do(req)
}

// callGetNoResult 发 GET 请求但不解析结果体（用于 sendChatAction / answerCallbackQuery 这类只关心成功与否的调用）。
func (a *adapter) callGetNoResult(ctx context.Context, baseURL string, params map[string]string) error {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	full := baseURL + "?" + q.Encode()
	resp, err := a.httpDo(ctx, http.MethodGet, full, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram api error %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
