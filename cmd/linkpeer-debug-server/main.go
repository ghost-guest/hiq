// linkpeer-debug-server 是联调用 S（扮演 hiq 桌面端角色）。
// 直接复用 internal/mobilebridge.Bridge，启动即配对 + 自动确认 + 打印二维码链接，
// 并用一个打印式 CommandExecutor 证明 linkpeer 的加密命令能到达。
//
// 用法：
//
//	linkpeer-debug-server -signal http://<本机局域网IP>:8080
//
// 它不接 hiq 的 Controller/前端 —— 那是 M4 桌面端 UI 的事。这里只验证
// linkpeer(Dart, C) ↔ mobilebridge(Go, S) 的 配对/信令/WebRTC/握手/AEAD 链路。
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zzycxz/hiq/internal/mobilebridge"
	"github.com/zzycxz/hiq/internal/scheduler"
)

// replyExec 模拟 hiq 真实对话：submit 后回 reasoning + text + 工具卡 + turn_done，
// 让 linkpeer 对话页有真实的双向交互（不只是 [CMD] 打印）。
type replyExec struct {
	conn atomic.Pointer[mobilebridge.Conn]
	bri  atomic.Pointer[mobilebridge.Bridge] // 回复走 ForwardEvent：盖 seq 入 ring（真增量 resync 可测）
}

// sendTab 优先经 Bridge.ForwardEvent（seq 盖章 + 环形缓冲，对齐真实桌面
// tabs.go 的注入点）；bridge 未就绪时退回 conn 直发。
func (e *replyExec) sendTab(tab, evt string, d time.Duration) {
	time.Sleep(d)
	if b := e.bri.Load(); b != nil {
		b.ForwardEvent(tab, []byte(evt))
		return
	}
	_ = e.conn.Load().SendEvent([]byte(evt))
}

func (e *replyExec) Submit(tab, input, _ string) error {
	fmt.Printf("[CMD] ✓ submit  tab=%s input=%q\n", tab, input)
	go e.reply(tab, input)
	return nil
}

// reply 模拟 hiq 的回复：office tab → 办公结果；其他 → 对话流。
func (e *replyExec) reply(tab, input string) {
	c := e.conn.Load()
	if c == nil {
		return
	}
	_ = c // sendTab 优先走 bridge；conn 仅兜底
	send := func(evt string, d time.Duration) { e.sendTab(tab, evt, d) }
	if strings.HasPrefix(tab, "office") {
		// 办公任务：模拟生成文件
		send(`{"kind":"reasoning","reasoning":"办公任务：「`+input+`」。我来生成对应文档……"}`, 300*time.Millisecond)
		tpl := strings.TrimSuffix(strings.TrimPrefix(input, "办公：生成「"), "」")
		fname := tpl + "-" + time.Now().Format("0102") + ".docx"
		send(`{"kind":"text","text":"📁 办公任务完成\n\n生成文件：**`+fname+`**\n\n（模拟）真实环境下 hiq 会调 office 工具读模板、填数据、生成文件，保存在桌面端下载目录。"}`, 600*time.Millisecond)
		send(`{"kind":"turn_done","err":""}`, 200*time.Millisecond)
		fmt.Println("[EVT] ✓ 模拟办公结果已发：" + fname)
		return
	}
	// 普通对话：reasoning → text → tool dispatch → tool result → turn_done
	send(`{"kind":"reasoning","reasoning":"用户说：「`+input+`」。我来分析一下需求……"}`, 300*time.Millisecond)
	send(`{"kind":"text","text":"收到：**`+input+`** —— 这是 hiq 经 P2P 加密通道的回复。我能读写代码、执行工具、生成文档。这条回复模拟了真实对话流（reasoning 思考 → text → 工具卡 → turn_done）。"}`, 500*time.Millisecond)
	send(`{"kind":"tool_dispatch","tool":{"id":"t1","name":"read","args":"main.go","readOnly":true}}`, 300*time.Millisecond)
	send(`{"kind":"approval_request","approval":{"id":"a1","tool":"edit","subject":"将修改 main.go 的 main 函数（需要你批准）"}}`, 300*time.Millisecond)
	send(`{"kind":"ask_request","ask":{"id":"q1","questions":[{"id":"q1","header":"风格","prompt":"修改后想用哪种风格继续？","options":[{"label":"详细","description":"逐步解释每个改动"},{"label":"简洁","description":"只给结论，不展开"}]}]}}`, 300*time.Millisecond)
	send(`{"kind":"tool_result","tool":{"id":"t1","name":"read","output":"package main\n\nfunc main() {\n\tprintln(\"hello\")\n}"}}`, 400*time.Millisecond)
	send(`{"kind":"turn_done","err":""}`, 200*time.Millisecond)
	fmt.Println("[EVT] ✓ 模拟对话回复已发（reasoning + text + tool×2 + turn_done）")
}

func (e *replyExec) Cancel(tab string) error      { fmt.Printf("[CMD] cancel  tab=%s\n", tab); return nil }
func (e *replyExec) Steer(tab, text string) error { fmt.Printf("[CMD] steer   tab=%s\n", tab); return nil }
func (e *replyExec) Pause(string) error           { return nil }
func (e *replyExec) Resume(string) error          { return nil }
func (e *replyExec) Approve(string, string, bool, bool, bool) error { return nil }
func (e *replyExec) Answer(string, string, []string) error          { return nil }
func (e *replyExec) SetPlan(string, bool) error                     { return nil }
func (e *replyExec) SetModel(string, string) error                  { return nil }
func (e *replyExec) ListSessions() ([]mobilebridge.SessionInfo, error) { return nil, nil }
func (e *replyExec) ListModels() ([]mobilebridge.ModelInfo, error) {
	return []mobilebridge.ModelInfo{{ID: "mock/model", Label: "模拟模型"}}, nil
}

// ListTemplates 下发真实 scheduler 模板目录（与桌面端同一来源）。
func (e *replyExec) ListTemplates() ([]mobilebridge.TemplateInfo, error) {
	out := make([]mobilebridge.TemplateInfo, 0, len(scheduler.BuiltinTemplates))
	for _, t := range scheduler.BuiltinTemplates {
		out = append(out, mobilebridge.TemplateInfo{
			ID: t.ID, Name: t.Name, Category: t.Category, Desc: t.Desc})
	}
	return out, nil
}
func (e *replyExec) NewTab(string, string) (string, error) { return "tab_mock", nil }
func (e *replyExec) RenameSession(string, string) error     { return nil }
func (e *replyExec) DeleteSession(string) error             { return nil }
func (e *replyExec) OfficeRun(string, string, map[string]string) error {
	return nil
}
func (e *replyExec) FileStart(string, string, int64) error { return nil }
func (e *replyExec) FileChunk(string, int, string) error   { return nil }
func (e *replyExec) FileEnd(string, string) error          { return nil }
func (e *replyExec) LoadSession(string) ([]map[string]any, error) { return nil, nil }

func main() {
	signalURL := flag.String("signal", "http://192.168.1.48:8080", "K signal URL (手机可达的局域网地址)")
	flag.Parse()

	store := mobilebridge.NewMemoryKeyStore()
	pub, priv, err := mobilebridge.GenerateLongTerm()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen key: %v\n", err)
		os.Exit(1)
	}

	cfg := mobilebridge.DefaultConfig()
	cfg.SignalURL = *signalURL
	cfg.ReadOnlyDefault = false // 联调：允许 submit，方便验证上行

	audit := mobilebridge.NewAudit("info")
	exec := &replyExec{}
	bridge := mobilebridge.NewBridge(cfg, priv, pub, store, exec, audit)
	exec.bri.Store(bridge)

	// onReady：存 conn（供 replyExec 模拟回复）+ 发欢迎 wireEvent
	bridge.SetOnReady(func(c *mobilebridge.Conn) {
		exec.conn.Store(c)
		fmt.Println("[EVT] conn ready，0.5s 后发欢迎 wireEvent")
		go func() {
			time.Sleep(500 * time.Millisecond)
			evt := `{"kind":"text","text":"👋 我是 hiq，P2P 加密通道已建立。发消息试试——我会回复 reasoning + text + 工具卡（模拟真实对话流）。"}`
			if err := c.SendEvent([]byte(evt)); err != nil {
				fmt.Printf("[EVT] send err: %v\n", err)
			} else {
				fmt.Println("[EVT] ✓ 欢迎 wireEvent 已发")
			}
		}()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bridge.Start(ctx) // 连 K

	time.Sleep(1500 * time.Millisecond) // 等 SignalClient 连上 K

	// auto-confirm：C exchange 后立刻确认（真实桌面端会弹 UI 让用户点）。
	go func() {
		seen := map[string]bool{}
		for {
			for _, p := range bridge.PendingPairings() {
				if !seen[p.PairID] {
					seen[p.PairID] = true
					fmt.Printf("[PAIR] C exchange devC=%s fpC=%s → auto-confirm\n",
						p.DevC, p.FpC)
					if err := bridge.ConfirmPairing(p.PairID); err != nil {
						fmt.Printf("[PAIR] confirm err: %v\n", err)
					} else {
						fmt.Printf("[PAIR] ✓ confirmed，C 现在可连\n")
					}
				}
			}
			time.Sleep(10 * time.Millisecond) // 联调：快轮询，确保 confirm 早于 C 连接
		}
	}()

	code, qrURL, err := bridge.StartPairing()
	if err != nil {
		fmt.Fprintf(os.Stderr, "StartPairing: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n========================================\n")
	fmt.Printf(" linkpeer-debug-server (S)\n")
	fmt.Printf(" devId:  %s\n", mobilebridge.DevID(pub))
	fmt.Printf(" signal: %s\n", *signalURL)
	fmt.Printf(" code:   %s\n", code)
	fmt.Printf(" qrURL（粘到 linkpeer 配对页）:\n %s\n", qrURL)
	fmt.Printf("========================================\n\n")

	// 联调辅助：手机浏览器访问 http://<电脑IP>:8081/qr 拿新鲜 qrURL（每次访问
	// 重新 StartPairing，规避 60s 过期 + 解决跨设备文本传输难题）。
	go func() {
		http.HandleFunc("/qr", func(w http.ResponseWriter, r *http.Request) {
			_, qr, err := bridge.StartPairing()
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintf(w, "%s\n\n长按上面 linkpeer:// 整行 → 复制 → 切到 linkpeer 配对页粘贴 → 连接\n", qr)
			fmt.Printf("[QR] served to %s\n", r.RemoteAddr)
		})
		fmt.Println("QR 辅助端点: 手机浏览器访问 http://192.168.1.48:8081/qr 拿链接")
		if err := http.ListenAndServe(":8081", nil); err != nil {
			fmt.Fprintf(os.Stderr, "qr http: %v\n", err)
		}
	}()

	fmt.Printf("等 linkpeer 连接…（握手成功后打印 [CMD]，证明加密链路通）\n\n")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Println("\nshutting down")
}
