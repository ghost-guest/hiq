package telegram

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/zzycxz/hiq/internal/bot"
)

// newTestAdapter 构造一个仅含 logger + msgCh 的 adapter（照抄 qq gateway_test.go 的范式），
// 不连真实 HTTP，只测 handleMessage/handleCallbackQuery 的纯函数逻辑。
func newTestAdapter(buf int) *adapter {
	return &adapter{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		msgCh:   make(chan bot.InboundMessage, buf),
		token:   "TEST:TOKEN",
		httpCli: nil, // 测试不触发 HTTP
	}
}

func TestHandleMessagePrivateChat(t *testing.T) {
	a := newTestAdapter(1)
	raw, err := json.Marshal(map[string]any{
		"message_id": 100,
		"text":       "hello",
		"from":       map[string]any{"id": 111, "first_name": "Alice", "username": "alice"},
		"chat":       map[string]any{"id": 222, "type": "private"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var m tgMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}

	a.handleMessage(nil, &m)

	msg := <-a.msgCh
	if msg.ChatType != bot.ChatDM {
		t.Fatalf("chat type = %q, want %q", msg.ChatType, bot.ChatDM)
	}
	if msg.ChatID != "222" {
		t.Fatalf("chat id = %q, want 222", msg.ChatID)
	}
	if msg.UserID != "111" {
		t.Fatalf("user id = %q, want 111", msg.UserID)
	}
	if msg.Text != "hello" {
		t.Fatalf("text = %q, want hello", msg.Text)
	}
	if msg.UserName != "alice" {
		t.Fatalf("user name = %q, want alice", msg.UserName)
	}
}

func TestHandleMessageGroupChat(t *testing.T) {
	a := newTestAdapter(1)
	raw, err := json.Marshal(map[string]any{
		"message_id": 200,
		"text":       "hi group",
		"from":       map[string]any{"id": 111, "first_name": "Bob"},
		"chat":       map[string]any{"id": 333, "type": "supergroup", "title": "Dev"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var m tgMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}

	a.handleMessage(nil, &m)

	msg := <-a.msgCh
	if msg.ChatType != bot.ChatGroup {
		t.Fatalf("chat type = %q, want %q", msg.ChatType, bot.ChatGroup)
	}
	if msg.ChatID != "333" {
		t.Fatalf("chat id = %q, want 333", msg.ChatID)
	}
	// 无 username 时应回退到 first_name
	if msg.UserName != "Bob" {
		t.Fatalf("user name = %q, want Bob (first_name fallback)", msg.UserName)
	}
}

func TestHandleCallbackQueryApproval(t *testing.T) {
	a := newTestAdapter(1)
	cq := &tgCallbackQuery{
		ID:   "cq-1",
		From: tgUser{ID: 111, Username: "alice"},
		Data: "/approve abc123",
		Message: &tgMessage{
			MessageID: 500,
			Chat:      tgChat{ID: 222, Type: "private"},
		},
	}

	a.handleCallbackQuery(nil, cq)

	// callback_query 的应答是异步 HTTP（answerCallbackQuery），此处 httpCli 为 nil 会报错，
	// 但它只影响 loading 转圈消除，不影响 InboundMessage 投递。验证投递结果即可。
	msg := <-a.msgCh
	if msg.Text != "/approve abc123" {
		t.Fatalf("text = %q, want /approve abc123", msg.Text)
	}
	if msg.ChatID != "222" {
		t.Fatalf("chat id = %q, want 222", msg.ChatID)
	}
	if msg.UserID != "111" {
		t.Fatalf("user id = %q, want 111", msg.UserID)
	}
}

func TestHandleUpdateDispatch(t *testing.T) {
	a := newTestAdapter(2)
	// message 类
	u1 := tgUpdate{
		UpdateID: 1001,
		Message: &tgMessage{
			MessageID: 10,
			Text:      "first",
			From:      tgUser{ID: 1, FirstName: "U"},
			Chat:      tgChat{ID: 2, Type: "private"},
		},
	}
	// callback_query 类
	u2 := tgUpdate{
		UpdateID: 1002,
		CallbackQuery: &tgCallbackQuery{
			ID:   "cq-2",
			From: tgUser{ID: 1},
			Data: "/deny xyz",
			Message: &tgMessage{
				Chat: tgChat{ID: 2, Type: "private"},
			},
		},
	}

	a.handleUpdate(nil, u1)
	a.handleUpdate(nil, u2)

	msg1 := <-a.msgCh
	if msg1.Text != "first" {
		t.Fatalf("msg1 text = %q, want first", msg1.Text)
	}
	msg2 := <-a.msgCh
	if msg2.Text != "/deny xyz" {
		t.Fatalf("msg2 text = %q, want /deny xyz", msg2.Text)
	}
}
