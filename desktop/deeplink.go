package main

import (
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// deeplink.go — hiq:// 统一深链接口（DASHBOARD spec §4.12）。
//
// 红线（永久）：路由表只允许**导航型**目的地——打开某个屏并高亮。
// 任何动作型目的地（approve/execute/rollback 之类）永久拒绝："点 IM 里
// 的链接 = 执行变更"会击穿人工门。解析 fail-closed：协议注册后任何网页
// 都能戳 hiq://，白名单之外一律丢弃。
//
// 通路：
//   热路径  第二实例启动（用户在 IM/邮件里点链接、应用已开）
//           → single_instance.go 的 OnSecondInstanceLaunch 解析 args
//           → EventsEmit("hiq:deep-link", {kind,id})
//   冷路径  应用未开、协议拉起本进程（main.go 扫 os.Args → stash 路由）
//           → 前端 boot 后 NetDevConsumeDeepLink() 一次性取走
// 两条路径汇到 App.tsx 的同一个 handler（切 profile → 落对应屏）。

// DeepLinkRoute is one parsed, validated navigation target.
type DeepLinkRoute struct {
	Kind string `json:"kind"` // finding | case | cutover | proposal | screen
	ID   string `json:"id"`   // finding/case/cutover/proposal id, or screen name
}

var (
	deepLinkKinds = map[string]bool{
		"finding": true, "case": true, "cutover": true, "proposal": true, "screen": true,
	}
	deepLinkScreens = map[string]bool{
		"overview": true, "chain": true, "cutover": true, "discovery": true, "exposure": true,
	}
)

// parseDeepLink validates one hiq:// URL. Fail-closed: exact scheme,
// whitelisted host, id charset [A-Za-z0-9_-], no query, no multi-segment path.
func parseDeepLink(raw string) (DeepLinkRoute, bool) {
	raw = strings.TrimSpace(raw)
	rest, ok := strings.CutPrefix(raw, "hiq://")
	if !ok || rest == "" {
		return DeepLinkRoute{}, false
	}
	kind, id, found := strings.Cut(rest, "/")
	if !found || kind == "" || id == "" {
		return DeepLinkRoute{}, false
	}
	if !deepLinkKinds[kind] {
		return DeepLinkRoute{}, false
	}
	if strings.ContainsAny(id, "?&=./\\:") || strings.ContainsAny(kind, "?&=./\\:") {
		return DeepLinkRoute{}, false
	}
	for _, r := range kind + id {
		if !(r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return DeepLinkRoute{}, false
		}
	}
	if kind == "screen" && !deepLinkScreens[id] {
		return DeepLinkRoute{}, false
	}
	return DeepLinkRoute{Kind: kind, ID: id}, true
}

// deepLinkArg scans argv/second-instance args for a hiq:// URL. The
// registry command is `"<exe>" --deep-link "%1"` but we accept the bare URL
// too (launchers, scripts) — both shapes carry the scheme prefix.
func deepLinkArg(args []string) string {
	for _, a := range args {
		a = strings.TrimSpace(a)
		if strings.HasPrefix(a, "hiq://") && parseableDeepLink(a) {
			return a
		}
	}
	return ""
}

func parseableDeepLink(raw string) bool {
	_, ok := parseDeepLink(raw)
	return ok
}

// ── 冷启动暂存 ──

var (
	pendingMu    sync.Mutex
	pendingRoute *DeepLinkRoute
)

// stashPendingDeepLink parses and stashes a cold-start route (main.go, before
// the frontend exists). Invalid URLs are dropped (fail-closed even here).
func stashPendingDeepLink(raw string) {
	r, ok := parseDeepLink(raw)
	if !ok {
		return
	}
	pendingMu.Lock()
	pendingRoute = &r
	pendingMu.Unlock()
}

// consumePendingDeepLink hands the stashed route to the frontend exactly once.
func consumePendingDeepLink() *DeepLinkRoute {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	r := pendingRoute
	pendingRoute = nil
	return r
}

// emitDeepLink is the warm path: parse + push to the frontend listener.
// Best-effort by design (ctx may be mid-shutdown).
func (a *App) emitDeepLink(raw string) {
	r, ok := parseDeepLink(raw)
	if !ok || a.ctx == nil {
		return
	}
	defer func() { _ = recover() }()
	runtime.EventsEmit(a.ctx, "hiq:deep-link", map[string]string{"kind": r.Kind, "id": r.ID})
}

// NetDevConsumeDeepLink is the cold-path bridge: the frontend calls it once on
// boot; null when the app was launched without a deep link (the normal case).
func (a *App) NetDevConsumeDeepLink() (*DeepLinkRoute, error) {
	return consumePendingDeepLink(), nil
}
