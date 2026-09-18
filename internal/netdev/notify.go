package netdev

// notify.go — Finding 通知出口 v2（NETDEV_SPEC_V2 §5.2）：三个独立出口——
// webhook（generic/feishu/dingtalk/wecom）、SMTP、内嵌 IM 网关直推——
// 任选组合，互不依赖。防轰炸：同一 source 的告警在聚合窗口（默认 5 分钟）
// 内合并，窗口关闭时补一条带计数的汇总；severity 门槛统一在最前面。
// NotifyPushText 是通用文本出口（每日早报等）。脱敏不变：推的是 Finding
// 本身（标题/设备/截断摘要 + hiq:// 深链），不含原始回显。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

// NotifyPusher is the embedded IM gateway's push seam (desktop injects the
// live *bot.BotGateway at start/stop — interface to avoid the import cycle,
// mirroring builtin.SetIMPusher).
type NotifyPusher interface {
	Push(ctx context.Context, dest, text string) error
}

var (
	notifyMu     sync.Mutex
	notifyURL    string
	notifyMinSev string
	notifyFmt    string // generic | feishu | dingtalk | wecom
	notifySMTP   *smtpConfig
	notifyBotDst string
	notifyPusher NotifyPusher
)

func SetNotifyPusher(p NotifyPusher) {
	notifyMu.Lock()
	notifyPusher = p
	notifyMu.Unlock()
}

type smtpConfig struct {
	host, user, pass, from string
	port                   int
	to                     []string
}

// EnsureNotifier wires the outlets from config (idempotent; SharedManager
// calls it on every config load so settings changes go live without restart).
func EnsureNotifier(cfg *config.Config) {
	// 升级链巡检（§5.2）：出口一就位即启动，进程内只起一次。
	StartEscalationWatcher()
	notifyMu.Lock()
	defer notifyMu.Unlock()
	if cfg == nil {
		notifyURL, notifyMinSev, notifySMTP, notifyBotDst = "", "", nil, ""
		return
	}
	notifyURL = strings.TrimSpace(cfg.NetDev.NotifyWebhook)
	notifyMinSev = strings.TrimSpace(cfg.NetDev.NotifyMinSeverity)
	if notifyMinSev == "" {
		notifyMinSev = "warning"
	}
	notifyFmt = strings.TrimSpace(cfg.NetDev.NotifyFormat)
	notifySMTP = nil
	if h := strings.TrimSpace(cfg.NetDev.NotifySMTPHost); h != "" && len(cfg.NetDev.NotifySMTPTo) > 0 {
		pass := ""
		if env := strings.TrimSpace(cfg.NetDev.NotifySMTPPassEnv); env != "" {
			if v, ok, _ := secretGetter(SecretKindPassword, env); ok {
				pass = v
			}
		}
		notifySMTP = &smtpConfig{host: h, user: cfg.NetDev.NotifySMTPUser, pass: pass, from: cfg.NetDev.NotifySMTPFrom, port: cfg.NetDev.NotifySMTPPort, to: cfg.NetDev.NotifySMTPTo}
		if notifySMTP.port == 0 {
			notifySMTP.port = 587
		}
	}
	notifyBotDst = strings.TrimSpace(cfg.NetDev.NotifyBotDest)
}

var sevRank = map[string]int{"info": 0, "warning": 1, "critical": 2}

// anyOutlet: at least one outlet configured (caller holds the lock).
func anyOutletLocked() bool {
	return notifyURL != "" || notifySMTP != nil || (notifyBotDst != "" && notifyPusher != nil)
}

// ── 聚合窗口：同 source 的重复告警合并，窗口到期补汇总 ───────────────────────

const notifyAggWindow = 5 * time.Minute

type aggState struct {
	count int
	timer *time.Timer
}

var (
	aggMu            sync.Mutex
	aggStateBySource = map[string]*aggState{}
)

// notifyFindingAsync fires the configured outlets for a new Finding when the
// severity clears the bar, with per-source aggregation. Best-effort: failures
// log, never block.
func notifyFindingAsync(f *Finding) {
	if f == nil {
		return
	}
	notifyMu.Lock()
	min := notifyMinSev
	if sevRank[f.Severity] < sevRank[min] || !anyOutletLocked() {
		notifyMu.Unlock()
		return
	}
	notifyMu.Unlock()

	// Human/AI findings have no source: never aggregate (each is unique).
	if f.Source == "" {
		notifyFindingNow(f, 1)
		return
	}
	aggMu.Lock()
	if st, ok := aggStateBySource[f.Source]; ok {
		// Inside the window: suppress this copy, bump the count — the flush
		// at window close carries the total.
		st.count++
		aggMu.Unlock()
		return
	}
	st := &aggState{count: 1}
	aggStateBySource[f.Source] = st
	st.timer = time.AfterFunc(notifyAggWindow, func() {
		aggMu.Lock()
		delete(aggStateBySource, f.Source)
		n := st.count
		aggMu.Unlock()
		if n > 1 {
			notifySummary(f, n)
		}
	})
	aggMu.Unlock()
	notifyFindingNow(f, 1)
}

// notifySummary: the window-closed digest for suppressed duplicates.
func notifySummary(f *Finding, n int) {
	clone := *f
	clone.Title = fmt.Sprintf("%s（窗口内共 %d 条，已合并）", f.Title, n)
	notifyFindingNow(&clone, n)
}

// outletSnapshot reads the outlet config under one lock acquisition.
type outletSnapshot struct {
	url, fmt_, botDst string
	smc               *smtpConfig
	pusher            NotifyPusher
}

func outlets() outletSnapshot {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	return outletSnapshot{url: notifyURL, fmt_: notifyFmt, botDst: notifyBotDst, smc: notifySMTP, pusher: notifyPusher}
}

// notifyFindingNow renders one Finding and fans it out.
func notifyFindingNow(f *Finding, count int) {
	detail := f.Detail
	if len(detail) > 500 {
		detail = detail[:500] + "…"
	}
	o := outlets()

	// The project tag leads the title so an off-hours push answers "which
	// site" first ("" = 未分组 → no tag; the tag is the save-time snapshot).
	proj := ""
	if f.Project != "" {
		proj = "[" + f.Project + "] "
	}
	text := fmt.Sprintf("[hiq 运维] %s%s（%s，%s）%s\n%s\n回复 /netdev 详情 %s 查看证据；确认收到回 /netdev ack %s", proj, f.Title, f.Severity, strings.Join(f.Devices, "、"), "hiq://finding/"+f.ID, detail, f.ID, f.ID)
	if o.smc != nil {
		subject := fmt.Sprintf("[hiq 运维] %s%s（%s，%s）", proj, f.Title, f.Severity, strings.Join(f.Devices, "、"))
		go smtpSendText(o.smc, subject, text)
	}
	if o.botDst != "" && o.pusher != nil {
		pushBotText(o.pusher, o.botDst, text)
	}
	if o.url == "" {
		return
	}
	var body []byte
	var err error
	switch o.fmt_ {
	case "feishu":
		body, err = json.Marshal(map[string]any{"msg_type": "text", "content": map[string]string{"text": text}})
	case "dingtalk", "wecom":
		body, err = json.Marshal(map[string]any{"msgtype": "text", "text": map[string]string{"content": text}})
	default:
		payload := map[string]any{
			"source":     "hiq-netdev",
			"kind":       "finding",
			"id":         f.ID,
			"title":      f.Title,
			"severity":   f.Severity,
			"devices":    f.Devices,
			"detail":     detail,
			"agg_count":  count,
			"deep_link":  "hiq://finding/" + f.ID,
			"created_at": f.CreatedAt.Format(time.RFC3339),
		}
		if f.Project != "" {
			payload["project"] = f.Project
		}
		body, err = json.Marshal(payload)
	}
	if err != nil {
		return
	}
	postWebhookJSON(o.url, body)
}

// NotifyConfigured reports whether at least one outlet is live (the test
// button's guard: clicking with nothing configured must explain, not no-op).
func NotifyConfigured() bool {
	o := outlets()
	return o.url != "" || o.smc != nil || (o.botDst != "" && o.pusher != nil)
}

// NotifyPushText pushes one outbound text (the daily briefing, future
// digests) through every configured outlet. The webhook variant carries it
// as JSON {kind, title, text}.
func NotifyPushText(kind, title, text string) {
	o := outlets()
	if o.smc != nil {
		go smtpSendText(o.smc, title, text)
	}
	if o.botDst != "" && o.pusher != nil {
		pushBotText(o.pusher, o.botDst, title+"\n\n"+text)
	}
	if o.url == "" {
		return
	}
	var body []byte
	var err error
	switch o.fmt_ {
	case "feishu":
		body, err = json.Marshal(map[string]any{"msg_type": "text", "content": map[string]string{"text": title + "\n\n" + text}})
	case "dingtalk", "wecom":
		body, err = json.Marshal(map[string]any{"msgtype": "text", "text": map[string]string{"content": title + "\n\n" + text}})
	default:
		body, err = json.Marshal(map[string]any{"source": "hiq-netdev", "kind": kind, "title": title, "text": text})
	}
	if err != nil {
		return
	}
	postWebhookJSON(o.url, body)
}

func pushBotText(pusher NotifyPusher, dest, text string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := pusher.Push(ctx, dest, text); err != nil {
			slog.Warn("netdev notify bot push failed", "err", err)
		}
	}()
}

func postWebhookJSON(url string, body []byte) {
	go func() {
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			slog.Warn("netdev notify webhook failed", "err", err)
			return
		}
		_ = resp.Body.Close()
	}()
}

// smtpSendText mails a plain-text notice. Best-effort like every outlet:
// failures log, never block.
func smtpSendText(c *smtpConfig, subject, body string) {
	msg := []byte("To: " + strings.Join(c.to, ",") + "\r\n" +
		"From: " + c.from + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" +
		body)
	addr := fmt.Sprintf("%s:%d", c.host, c.port)
	var auth smtp.Auth
	if c.user != "" {
		auth = smtp.PlainAuth("", c.user, c.pass, c.host)
	}
	err := smtp.SendMail(addr, auth, c.from, c.to, msg)
	if err != nil {
		slog.Warn("netdev notify smtp failed", "err", err)
	}
}
