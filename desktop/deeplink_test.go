package main

import "testing"

// parseDeepLink 表驱动（DASHBOARD spec §4.12 红线）：导航型 only、
// fail-closed——白名单外的 host（尤其是动作型 approve/execute）、query、
// 路径穿越、多级路径、未知屏名一律拒绝。
func TestParseDeepLink(t *testing.T) {
	valid := map[string]DeepLinkRoute{
		"hiq://finding/F-100":    {Kind: "finding", ID: "F-100"},
		"hiq://case/C20260831-1": {Kind: "case", ID: "C20260831-1"},
		"hiq://cutover/CO-9":     {Kind: "cutover", ID: "CO-9"},
		"hiq://proposal/P_7":     {Kind: "proposal", ID: "P_7"},
		"hiq://screen/chain":     {Kind: "screen", ID: "chain"},
		"hiq://screen/exposure":  {Kind: "screen", ID: "exposure"},
		"  hiq://finding/x9  ":   {Kind: "finding", ID: "x9"}, // 首尾空白容忍
	}
	for raw, want := range valid {
		got, ok := parseDeepLink(raw)
		if !ok || got != want {
			t.Errorf("parseDeepLink(%q) = %+v,%v want %+v", raw, got, ok, want)
		}
	}
	invalid := []string{
		"",                             // 空
		"http://finding/F-1",           // 非 hiq 协议
		"HIQ://finding/F-1",       // scheme 大小写敏感（严格）
		"hiq://",                  // 无 host
		"hiq://finding",           // 无 id
		"hiq://finding/",          // 空 id
		"hiq://approve/P-1",       // 动作型目的地——永久红线
		"hiq://execute/x",         // 动作型目的地
		"hiq://rollback/x",        // 动作型目的地
		"hiq://finding/F-1?x=1",   // query 拒绝
		"hiq://finding/../etc",    // 路径穿越
		"hiq://finding/a/b",       // 多级路径
		"hiq://finding/F 1",       // id 含空格
		"hiq://finding/F%201",     // id 含百分号
		"hiq://unknown/x",         // 未知 host
		"hiq://screen/notascreen", // 未知屏名
		"hiq://finding/F-1/extra", // 尾段
	}
	for _, raw := range invalid {
		if _, ok := parseDeepLink(raw); ok {
			t.Errorf("parseDeepLink(%q) must fail (fail-closed)", raw)
		}
	}
}

func TestDeepLinkArgScan(t *testing.T) {
	if got := deepLinkArg([]string{"hiq.exe", "--deep-link", "hiq://finding/F-7"}); got != "hiq://finding/F-7" {
		t.Errorf("split form = %q", got)
	}
	if got := deepLinkArg([]string{"hiq.exe", "hiq://cutover/CO-1"}); got != "hiq://cutover/CO-1" {
		t.Errorf("bare form = %q", got)
	}
	if got := deepLinkArg([]string{"hiq.exe", "--flag", "http://x", "hiq://approve/P-1"}); got != "" {
		t.Errorf("invalid URLs must not pass through: %q", got)
	}
	if got := deepLinkArg(nil); got != "" {
		t.Errorf("empty argv = %q", got)
	}
}

func TestPendingDeepLinkStashConsume(t *testing.T) {
	if r := consumePendingDeepLink(); r != nil {
		t.Fatalf("clean state consumed %+v", r)
	}
	stashPendingDeepLink("hiq://finding/F-42")
	r := consumePendingDeepLink()
	if r == nil || r.Kind != "finding" || r.ID != "F-42" {
		t.Fatalf("consume = %+v", r)
	}
	if again := consumePendingDeepLink(); again != nil {
		t.Fatalf("second consume must be nil, got %+v", again)
	}
	// fail-closed：坏 URL 不落暂存。
	stashPendingDeepLink("hiq://approve/P-1")
	if r := consumePendingDeepLink(); r != nil {
		t.Fatalf("invalid stash leaked %+v", r)
	}
}
