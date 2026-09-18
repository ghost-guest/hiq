package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/bot"
	"github.com/zzycxz/hiq/internal/event"
)

// desktopPendingPrompt 是一条待处理的审批/提问，桥记住它在哪个 tab。
type desktopPendingPrompt struct {
	tabID     string
	kind      string // "approval" | "ask"
	tool      string
	subject   string
	questions []event.AskQuestion
}

// desktopBridgeNotification 是一条要推送给订阅聊天的通知。
type desktopBridgeNotification struct {
	tabID     string
	id        string
	kind      string // "approval" | "ask" | "done"
	tool      string
	subject   string
	questions []event.AskQuestion
	err       string
}

// botBridgeDeps 是 hub 对宿主 App 的全部依赖（函数字段，便于测试注入）。
type botBridgeDeps struct {
	sessions        func() []bot.DesktopSessionInfo
	tabLabel        func(tabID string) string
	approveTab      func(tabID, id string, allow bool) error
	answerTab       func(tabID, id string, answers []event.AskAnswer) error
	notify          func(ctx context.Context, route bot.DesktopWatchRoute, text string) error
	persistWatchers func(watchers []bot.DesktopWatchRoute) error
}

// botBridgeHub 实现 bot.DesktopBridge：观察所有桌面 tab 的事件，把审批/提问/
// 完成推送给已订阅的 IM 聊天，并把 IM 的应答回填到对应 tab 的 controller。
//
// 不变式：Observe 在 controller 事件 goroutine 上同步调用，只做内存记账 + 入队，
// 绝不做网络调用（网络由 worker goroutine 异步处理，per-route 并发带超时）。
type botBridgeHub struct {
	deps botBridgeDeps

	mu       sync.Mutex
	watchers map[string]bot.DesktopWatchRoute
	pending  map[string]desktopPendingPrompt

	persistMu sync.Mutex

	queue chan desktopBridgeNotification
	stop  chan struct{}
	done  chan struct{}
}

const desktopBridgeQueueCap = 64

func newBotBridgeHub(deps botBridgeDeps, initialWatchers []bot.DesktopWatchRoute) *botBridgeHub {
	h := &botBridgeHub{
		deps:     deps,
		watchers: make(map[string]bot.DesktopWatchRoute),
		pending:  make(map[string]desktopPendingPrompt),
		queue:    make(chan desktopBridgeNotification, desktopBridgeQueueCap),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	for _, w := range initialWatchers {
		h.watchers[w.Key()] = w
	}
	go h.run()
	return h
}

func (h *botBridgeHub) Stop() {
	close(h.stop)
	<-h.done
}

// Observe 处理一条来自某 tab 的事件。只在 controller 事件 goroutine 上做内存记账 + 入队。
func (h *botBridgeHub) Observe(tabID string, e event.Event) {
	switch e.Kind {
	case event.ApprovalRequest:
		id := e.Approval.ID
		tool := e.Approval.Tool
		subject := e.Approval.Subject
		h.mu.Lock()
		if id != "" {
			h.pending[id] = desktopPendingPrompt{tabID: tabID, kind: "approval", tool: tool, subject: subject}
		}
		hasWatcher := len(h.watchers) > 0
		h.mu.Unlock()
		if hasWatcher && id != "" {
			h.enqueue(desktopBridgeNotification{tabID: tabID, id: id, kind: "approval", tool: tool, subject: subject})
		}
	case event.AskRequest:
		id := e.Ask.ID
		questions := e.Ask.Questions
		h.mu.Lock()
		if id != "" {
			h.pending[id] = desktopPendingPrompt{tabID: tabID, kind: "ask", questions: questions}
		}
		hasWatcher := len(h.watchers) > 0
		h.mu.Unlock()
		if hasWatcher && id != "" {
			h.enqueue(desktopBridgeNotification{tabID: tabID, id: id, kind: "ask", questions: questions})
		}
	case event.TurnDone:
		// 过滤 context canceled（桌面端主动停止不算完成通知噪音）。
		canceled := e.Err != nil && strings.Contains(e.Err.Error(), "context canceled")
		h.clearTabPending(tabID)
		if canceled {
			return
		}
		h.mu.Lock()
		hasWatcher := len(h.watchers) > 0
		h.mu.Unlock()
		if hasWatcher {
			errMsg := ""
			if e.Err != nil {
				errMsg = e.Err.Error()
			}
			h.enqueue(desktopBridgeNotification{tabID: tabID, kind: "done", err: errMsg})
		}
	}
}

func (h *botBridgeHub) clearTabPending(tabID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, p := range h.pending {
		if p.tabID == tabID {
			delete(h.pending, id)
		}
	}
}

func (h *botBridgeHub) enqueue(n desktopBridgeNotification) {
	select {
	case h.queue <- n:
	default:
		// 队列满，丢弃（绝不阻塞 controller 事件 goroutine）。
	}
}

func (h *botBridgeHub) run() {
	defer close(h.done)
	for {
		select {
		case <-h.stop:
			return
		case n := <-h.queue:
			h.deliver(n)
		}
	}
}

// deliver 把一条通知推送给所有订阅聊天（per-route 并发，带 15s 超时）。
func (h *botBridgeHub) deliver(n desktopBridgeNotification) {
	h.mu.Lock()
	routes := make([]bot.DesktopWatchRoute, 0, len(h.watchers))
	for _, r := range h.watchers {
		routes = append(routes, r)
	}
	h.mu.Unlock()
	if len(routes) == 0 {
		return
	}
	text := h.renderNotification(n)
	if strings.TrimSpace(text) == "" {
		return
	}
	var wg sync.WaitGroup
	for _, r := range routes {
		wg.Add(1)
		go func(route bot.DesktopWatchRoute) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = h.deps.notify(ctx, route, text)
		}(r)
	}
	wg.Wait()
}

func (h *botBridgeHub) renderNotification(n desktopBridgeNotification) string {
	label := strings.TrimSpace(h.deps.tabLabel(n.tabID))
	if label == "" {
		label = "(未命名)"
	}
	switch n.kind {
	case "approval":
		var b strings.Builder
		fmt.Fprintf(&b, "⚠️ 桌面会话「%s」需要批准操作\n", label)
		if tool := strings.TrimSpace(n.tool); tool != "" {
			fmt.Fprintf(&b, "工具: %s\n", tool)
		}
		if subj := strings.TrimSpace(n.subject); subj != "" {
			fmt.Fprintf(&b, "操作: %s\n", subj)
		}
		fmt.Fprintf(&b, "ID: %s\n用 /desktop approve %s 批准，/desktop deny %s 拒绝。\n桌面端先处理则以先到者为准。", n.id, n.id, n.id)
		return b.String()
	case "ask":
		var b strings.Builder
		fmt.Fprintf(&b, "❓ 桌面会话「%s」需要回答\n", label)
		for i, q := range n.questions {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "%d. %s\n", i+1, q.Prompt)
			for j, opt := range q.Options {
				letter := string(rune('A' + j))
				fmt.Fprintf(&b, "  %s. %s\n", letter, opt.Label)
			}
		}
		fmt.Fprintf(&b, "用 /desktop answer %s <选项> 回答。", n.id)
		return b.String()
	case "done":
		if n.err != "" {
			return fmt.Sprintf("❌ 桌面会话「%s」任务出错: %s", label, n.err)
		}
		return fmt.Sprintf("✅ 桌面会话「%s」任务完成。", label)
	}
	return ""
}

// Sessions 枚举所有桌面 live 会话，并把桥侧 pending 归类填进各 session。
func (h *botBridgeHub) Sessions() []bot.DesktopSessionInfo {
	sessions := h.deps.sessions()
	h.mu.Lock()
	defer h.mu.Unlock()
	byTab := make(map[string][]bot.DesktopPendingInfo)
	pendingPrompt := make(map[string]bool)
	for id, p := range h.pending {
		byTab[p.tabID] = append(byTab[p.tabID], bot.DesktopPendingInfo{ID: id, Kind: p.kind, Tool: p.tool})
		pendingPrompt[p.tabID] = true
	}
	for i := range sessions {
		sessions[i].Pending = byTab[sessions[i].TabID]
		if pendingPrompt[sessions[i].TabID] {
			sessions[i].PendingPrompt = true
		}
	}
	return sessions
}

func (h *botBridgeHub) Watching(route bot.DesktopWatchRoute) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.watchers[route.Key()]
	return ok
}

func (h *botBridgeHub) SetWatch(route bot.DesktopWatchRoute, enable bool) error {
	h.mu.Lock()
	changed := false
	if enable {
		if _, ok := h.watchers[route.Key()]; !ok {
			h.watchers[route.Key()] = route
			changed = true
		}
	} else {
		if _, ok := h.watchers[route.Key()]; ok {
			delete(h.watchers, route.Key())
			changed = true
		}
	}
	h.mu.Unlock()
	if !changed {
		return nil
	}
	return h.persistSnapshot()
}

// persistSnapshot 在 persistMu 锁内重新取 watchers 快照再落盘，
// 保证并发 SetWatch 不会用旧快照覆盖新快照。
func (h *botBridgeHub) persistSnapshot() error {
	h.persistMu.Lock()
	defer h.persistMu.Unlock()
	h.mu.Lock()
	snap := make([]bot.DesktopWatchRoute, 0, len(h.watchers))
	for _, r := range h.watchers {
		snap = append(snap, r)
	}
	h.mu.Unlock()
	return h.deps.persistWatchers(snap)
}

func (h *botBridgeHub) Approve(approvalID string, allow bool) (string, error) {
	h.mu.Lock()
	p, ok := h.pending[approvalID]
	if ok {
		delete(h.pending, approvalID)
	}
	h.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("未找到待处理的审批 %s（可能已在桌面端处理或已超时）", approvalID)
	}
	if err := h.deps.approveTab(p.tabID, approvalID, allow); err != nil {
		return "", err
	}
	if allow {
		return "已提交批准。桌面端若已先处理，以先到者为准。", nil
	}
	return "已提交拒绝。桌面端若已先处理，以先到者为准。", nil
}

func (h *botBridgeHub) AskQuestions(askID string) ([]event.AskQuestion, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.pending[askID]
	if !ok || p.kind != "ask" {
		return nil, false
	}
	return p.questions, true
}

func (h *botBridgeHub) Answer(askID string, answers []event.AskAnswer) (string, error) {
	h.mu.Lock()
	p, ok := h.pending[askID]
	if ok {
		delete(h.pending, askID)
	}
	h.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("未找到待回答的提问 %s", askID)
	}
	if err := h.deps.answerTab(p.tabID, askID, answers); err != nil {
		return "", err
	}
	return "已提交回答。桌面端若已先处理，以先到者为准。", nil
}
