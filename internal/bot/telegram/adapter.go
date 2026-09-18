// Package telegram 实现 Telegram Bot API 适配器。
// 协议要点：
//   - 鉴权用静态 Bot Token（从 @BotFather 获取，形如 123456:ABC-DEF），无需动态刷新
//   - 接收消息用 getUpdates long polling（offset + timeout），比 WebSocket 简单且无需公网回调
//   - 发送消息/typing/审批按钮均走 REST（sendMessage / sendChatAction / answerCallbackQuery）
//   - 审批按钮复用 bot.InlineKeyboard，在 Send 里翻译成 Telegram 的 reply_markup.inline_keyboard
//
// 参考 internal/bot/qq 的三段式结构（adapter 薄壳 + gateway 收发），删去 WebSocket 握手/心跳。
package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/zzycxz/hiq/internal/bot"
	"github.com/zzycxz/hiq/internal/config"
)

// New 创建 Telegram Bot 适配器。
func New(cfg config.TelegramBotConfig, logger *slog.Logger) bot.Adapter {
	return &adapter{
		cfg:    cfg,
		logger: logger.With("platform", "telegram"),
		// 长轮询 timeout=30s，HTTP client 给 35s 余量；其余请求复用同一 client
		httpCli: &http.Client{Timeout: 35 * time.Second},
	}
}

type adapter struct {
	cfg     config.TelegramBotConfig
	logger  *slog.Logger
	msgCh   chan bot.InboundMessage
	cancel  context.CancelFunc
	httpCli *http.Client

	// token 在 Start 时从环境变量读一次，Telegram token 不过期，无需 mutex/刷新
	token string
}

func (a *adapter) Platform() bot.Platform { return bot.PlatformTelegram }
func (a *adapter) Name() string           { return "telegram" }

func (a *adapter) Start(ctx context.Context) error {
	// 校验 token：必须在 Start 时就读，pollLoop 依赖它
	token := os.Getenv(a.cfg.TokenEnv)
	if token == "" {
		return fmt.Errorf("telegram bot token not set: env %s is empty", a.cfg.TokenEnv)
	}
	a.token = token

	a.msgCh = make(chan bot.InboundMessage, 64)
	ctx, a.cancel = context.WithCancel(ctx)

	go a.pollLoop(ctx)
	return nil
}

func (a *adapter) Stop() error {
	if a.cancel != nil {
		a.cancel()
	}
	return nil
}

func (a *adapter) Send(ctx context.Context, msg bot.OutboundMessage) (bot.SendResult, error) {
	return a.sendMessage(ctx, msg)
}

// SendTyping 发送"正在输入"状态。Telegram 原生支持 sendChatAction（不像 QQ/飞书的空实现）。
func (a *adapter) SendTyping(ctx context.Context, chatID string) error {
	return a.callGetNoResult(ctx, a.apiURL("sendChatAction"), map[string]string{
		"chat_id": chatID,
		"action":  "typing",
	})
}

func (a *adapter) Messages() <-chan bot.InboundMessage {
	return a.msgCh
}
