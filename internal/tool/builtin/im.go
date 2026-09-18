package builtin

// im.go exposes the im_send tool: the agent's way to proactively push a
// message to a connected IM bot conversation (feishu/weixin) during a chat.
// Before this tool existed, asking the agent "给飞书发条消息" made it reach for
// browser automation to log into web Feishu — wrong path. im_send reuses the
// single live bot gateway (the same one the screenshot hotkey and scheduler
// push through), so a message lands in the chat the user actually talks to the
// bot in.
//
// To avoid an import cycle (internal/bot imports internal/boot which imports
// this package), im.go depends only on a minimal imPusher interface. The
// concrete *bot.BotGateway satisfies it; the desktop app injects the gateway
// via SetIMPusher (mirrors SetCalendarStore / SetScheduler). Under the CLI/TUI
// or when the bot is off the tool returns a clear "offline" error.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/tool"
)

// imPusher is the minimal contract im_send needs from an IM gateway. The
// concrete *bot.BotGateway satisfies it (Push has the same signature), so the
// desktop can inject the live gateway without this package importing internal/bot
// (which would create a cycle via internal/boot).
type imPusher interface {
	Push(ctx context.Context, dest, text string) error
}

// globalIMPusher is the desktop-owned IM gateway, injected once at startup.
// nil = bot not started / not desktop mode; the tool returns a clear error.
var globalIMPusher imPusher

// SetIMPusher injects the IM gateway (e.g. *bot.BotGateway). Called from
// desktop/bot_gateway_app.go after gw.Start succeeds, mirroring SetCalendarStore
// / SetScheduler. Passing nil (e.g. on stopBotGateway) disables the tool so it
// reports offline cleanly instead of nil-derefing.
func SetIMPusher(p imPusher) { globalIMPusher = p }

// requireIMPusher returns the injected gateway or a clear error explaining why
// the tool is unavailable (mirrors requireCalendarStore / requireScheduler).
func requireIMPusher() (imPusher, error) {
	if globalIMPusher == nil {
		return nil, errors.New("IM gateway is offline — start the bot in Settings, or this isn't the desktop app")
	}
	return globalIMPusher, nil
}

// IMTools returns the IM-push tools for cowork registration. Hidden from the
// dev main-loop schema (via reg.Hide in boot.go) but callable by subagents,
// matching EmailTools / CalendarTools / SchedulerTools.
func IMTools() []tool.Tool { return []tool.Tool{imSend{}} }

// imSend pushes a text message to an IM bot conversation.
type imSend struct{}

func (imSend) Name() string { return "im_send" }

func (imSend) ReadOnly() bool { return false }

func (imSend) Description() string {
	return "Send a text message to a connected IM bot conversation (Feishu/Lark, WeChat, or Telegram). " +
		"Use this when the user asks to send/notify/push something to Feishu/WeChat/Telegram/IM — do NOT use a browser to log into web IM. " +
		"dest is optional: omit it to send to the user's default connected conversation (recommended); " +
		"or set dest explicitly as \"platform:chatID\" (e.g. \"feishu:oc_xxx\", \"weixin:wxid_xxx\", \"telegram:123456\"). " +
		"text is the message body (markdown supported on Feishu/Telegram)."
}

func (imSend) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "text":{"type":"string","description":"The message body to send."},
  "dest":{"type":"string","description":"Optional destination as \"platform:chatID\" (e.g. \"feishu:oc_xxx\"). Omit to use the user's default connected conversation."}
},
"required":["text"]
}`)
}

// imSendParams is the request shape for im_send.
type imSendParams struct {
	Text string `json:"text"`
	Dest string `json:"dest"`
}

func (imSend) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p imSendParams
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return "", errors.New("text is required")
	}

	pusher, err := requireIMPusher()
	if err != nil {
		return "", err
	}

	// Resolve the destination: explicit dest wins, else fall back to the shared
	// "default connected conversation" heuristic (same one the screenshot hotkey
	// and scheduler use, so messages consistently land where the user talks to
	// the bot).
	dest := strings.TrimSpace(p.Dest)
	if dest == "" {
		cfg, err := config.Load()
		if err != nil {
			return "", fmt.Errorf("load config to pick IM destination: %w", err)
		}
		dest = defaultIMPushDest(cfg.Bot.Connections)
		if dest == "" {
			return "", errors.New("no connected Feishu/WeChat/Telegram conversation found — connect a bot in Settings > IM and message it once so it remembers the chat")
		}
	}

	if err := pusher.Push(ctx, dest, text); err != nil {
		return "", fmt.Errorf("send to %s: %w", dest, err)
	}
	return fmt.Sprintf("Message sent to %s.", dest), nil
}

// defaultIMPushDest picks the best "platform:chatID" push destination from the
// configured bot connections. It prefers a connected feishu or weixin
// connection that has a known conversation (a remembered SessionMappings
// RemoteID), so the message lands where the user actually talks to the bot.
// qq is excluded — its API doesn't allow desktop-initiated sends. Returns ""
// when nothing suitable is configured.
//
// This mirrors desktop's screenshotPushDest; both kept in sync by reusing the
// same logic. Duplicated here (rather than shared in internal/bot) to avoid an
// import cycle (internal/bot → internal/boot → internal/tool/builtin).
func defaultIMPushDest(conns []config.BotConnectionConfig) string {
	for _, conn := range conns {
		if !conn.Enabled || conn.Status != "connected" {
			continue
		}
		if conn.Provider != "feishu" && conn.Provider != "weixin" && conn.Provider != "telegram" {
			continue // qq doesn't support desktop-initiated sends; feishu/weixin/telegram do
		}
		for _, m := range conn.SessionMappings {
			if id := strings.TrimSpace(m.RemoteID); id != "" {
				return conn.Provider + ":" + id
			}
		}
	}
	return ""
}
