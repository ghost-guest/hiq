package feishu

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/zzycxz/hiq/internal/bot"
	"github.com/zzycxz/hiq/internal/config"
)

func TestVerificationTokenValidRequiresConfiguredToken(t *testing.T) {
	a := &adapter{cfg: config.FeishuBotConfig{VerificationToken: "expected"}}

	if a.verificationTokenValid("") {
		t.Fatal("missing token should be rejected when verification token is configured")
	}
	if a.verificationTokenValid("wrong") {
		t.Fatal("wrong token should be rejected")
	}
	if !a.verificationTokenValid("expected") {
		t.Fatal("matching token should be accepted")
	}

	a.cfg.VerificationToken = ""
	if !a.verificationTokenValid("") {
		t.Fatal("empty configured verification token should preserve unauthenticated mode")
	}
}

func TestMarkSeenConcurrent(t *testing.T) {
	a := &adapter{seen: make(map[string]bool)}
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = a.markSeen(fmt.Sprintf("evt-%d", i%5))
		}(i)
	}
	wg.Wait()

	if got := len(a.seen); got != 5 {
		t.Fatalf("seen size = %d, want 5", got)
	}
	if a.markSeen("evt-1") != true {
		t.Fatal("second markSeen call should report duplicate")
	}
	if a.markSeen("") {
		t.Fatal("empty event id should not be treated as duplicate")
	}
}

func TestHandleCardActionUsesChatType(t *testing.T) {
	a := &adapter{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		msgCh:  make(chan bot.InboundMessage, 1),
	}
	raw := []byte(`{
		"event": {
			"operator": {
				"operator_id": {"open_id": "open-user"}
			},
			"context": {
				"open_message_id": "msg-1",
				"open_chat_id": "chat-1"
			},
			"action": {
				"value": {
					"command": "/approve approval-1",
					"chat_type": "dm"
				}
			}
		}
	}`)

	if !a.handleCardAction(raw) {
		t.Fatal("handleCardAction returned false")
	}

	msg := <-a.msgCh
	if msg.ChatType != bot.ChatDM {
		t.Fatalf("chat type = %q, want %q", msg.ChatType, bot.ChatDM)
	}
	if msg.Text != "/approve approval-1" {
		t.Fatalf("text = %q, want /approve approval-1", msg.Text)
	}
}

func TestFeishuMarkdownPostContent(t *testing.T) {
	content, err := feishuMarkdownPostContent("hello [docs](https://example.com)")
	if err != nil {
		t.Fatalf("feishuMarkdownPostContent: %v", err)
	}
	var payload struct {
		ZhCn struct {
			Content [][]struct {
				Tag  string `json:"tag"`
				Text string `json:"text"`
				Href string `json:"href"`
			} `json:"content"`
		} `json:"zh_cn"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("post content should be valid json: %v", err)
	}
	if len(payload.ZhCn.Content) != 1 || len(payload.ZhCn.Content[0]) != 2 {
		t.Fatalf("content blocks = %#v, want one paragraph with text and link", payload.ZhCn.Content)
	}
	if payload.ZhCn.Content[0][0].Tag != "text" || payload.ZhCn.Content[0][0].Text != "hello " {
		t.Fatalf("first element = %#v, want text hello", payload.ZhCn.Content[0][0])
	}
	if payload.ZhCn.Content[0][1].Tag != "a" || payload.ZhCn.Content[0][1].Text != "docs" || payload.ZhCn.Content[0][1].Href != "https://example.com" {
		t.Fatalf("second element = %#v, want link", payload.ZhCn.Content[0][1])
	}
}

// TestFeishuReceiveIDType 验证主动发送（非回复）时 receive_id_type 按前缀正确推断。
// 推送链路用 SessionMappings 里的 open_id（ou_）当目标，飞书 API 要求类型严格匹配，
// 类型错配会被拒。这里覆盖所有支持的 ID 形态。
func TestFeishuReceiveIDType(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"ou_3a2bdd60640aaa95518186677b1f6d8c", "open_id"}, // 用户 open_id — 推送主场景
		{"on_abc123", "union_id"},                          // 跨应用 union_id
		{"u_abc123", "user_id"},                            // 应用内 user_id
		{"someone@example.com", "email"},                   // 邮箱
		{"oc_abc123", "chat_id"},                           // 群/会话 chat_id — 回复主场景
		{"oc_5dxx", "chat_id"},                             // 短 chat_id
		{"unknown_prefix_xx", "chat_id"},                   // 未识别 → 回退 chat_id（保留旧行为）
	}
	for _, c := range cases {
		got := feishuReceiveIDType(c.id)
		if got != c.want {
			t.Errorf("feishuReceiveIDType(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}
