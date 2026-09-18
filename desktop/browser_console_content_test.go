package main

import (
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/tool/builtin"
)

func TestNormalizeSkillContent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"clean passthrough", "---\nname: x\n---\nbody", "---\nname: x\n---\nbody"},
		{"markdown fence", "```markdown\n---\nname: x\n---\nbody\n```", "---\nname: x\n---\nbody"},
		{"leading prose", "好的，以下是技能：\n---\nname: x\n---\nbody", "---\nname: x\n---\nbody"},
	}
	for _, c := range cases {
		if got := normalizeSkillContent(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSkillContentParsable(t *testing.T) {
	if !skillContentParsable("---\nname: x\ndescription: d\n---\nbody") {
		t.Error("valid frontmatter should parse")
	}
	if skillContentParsable("no frontmatter") {
		t.Error("missing frontmatter should not parse")
	}
	if skillContentParsable("```\n---\nname: x\n---\nbody\n```") {
		t.Error("fenced content should not parse (normalization happens first)")
	}
	if skillContentParsable("---\ndescription: d\n---\nbody") {
		t.Error("frontmatter without name should not parse")
	}
}

func TestNaiveSkillDraftNeverEmptyName(t *testing.T) {
	events := []builtin.ConsoleRecordEvent{{Type: "navigate", URL: "https://x/"}}
	// Chinese-only hint sanitizes to "" — the draft must still carry a valid
	// name in BOTH the returned struct and the frontmatter (E2E 2026-08-29).
	draft := naiveSkillDraft("百度搜索", events, "")
	if draft.Name != "browser-skill" {
		t.Errorf("struct name: got %q, want browser-skill", draft.Name)
	}
	if !strings.Contains(draft.Content, "name: browser-skill") {
		t.Error("frontmatter must contain the fallback name")
	}
	if skillContentParsable(draft.Content) != true {
		t.Error("naive draft must be editor-parsable")
	}
}

func TestNaiveSkillDraftHumanBreakpoints(t *testing.T) {
	events := []builtin.ConsoleRecordEvent{
		{Type: "navigate", URL: "https://portal.example/login"},
		{Type: "input", Selector: "#user", Name: "用户名", Value: "admin"},
		{Type: "input", Selector: "#pass", Name: "密码", Password: true},
		{Type: "input", Selector: "#sms", Name: "短信验证码", Value: "123456"},
		{Type: "click", Selector: "button[type=submit]", Name: "登录"},
	}
	draft := naiveSkillDraft("portal-login", events, "")
	content := draft.Content

	// Password input → human step, never a recorded value.
	if !strings.Contains(content, "| human | `` | 请在浏览器中输入密码并提交 |") {
		t.Errorf("password input must become a human breakpoint, got:\n%s", content)
	}
	// SMS-code input → human step naming the field.
	if !strings.Contains(content, "human") || !strings.Contains(content, "短信验证码") {
		t.Errorf("SMS-code input must become a human breakpoint mentioning the field, got:\n%s", content)
	}
	// The captured code value must NOT survive anywhere.
	if strings.Contains(content, "123456") {
		t.Errorf("recorded SMS code leaked into the skill:\n%s", content)
	}
	// Normal inputs stay mechanical, now with the visible label as a
	// fallback anchor (multi-anchor chain survives attribute churn).
	if !strings.Contains(content, "| type | `#user;;text=用户名` | admin |") {
		t.Errorf("regular input should carry css;;text anchors, got:\n%s", content)
	}
}

func TestLooksLikeHumanInput(t *testing.T) {
	yes := []struct{ name, sel string }{
		{"短信验证码", "#sms"},
		{"邮箱验证码", ""},
		{"captcha", ""},
		{"", "input[name=otp_code]"},
		{"安全口令", ""},
	}
	for _, c := range yes {
		if !looksLikeHumanInput(c.name, c.sel) {
			t.Errorf("looksLikeHumanInput(%q,%q) = false, want true", c.name, c.sel)
		}
	}
	if looksLikeHumanInput("用户名", "#user") {
		t.Error("username input is not a human breakpoint")
	}
}

func TestNaiveSkillDraftScrollSteps(t *testing.T) {
	events := []builtin.ConsoleRecordEvent{
		{Type: "navigate", URL: "https://x/long-page"},
		{Type: "scroll", Selector: "body", Value: "down 4"},
		{Type: "click", Selector: "#load-more", Name: "加载更多"},
	}
	draft := naiveSkillDraft("long-page", events, "")
	if !strings.Contains(draft.Content, "| scroll | `down` | 4 |") {
		t.Errorf("scroll event must become a scroll step (direction/amount), got:\n%s", draft.Content)
	}
	// Degenerate scroll values fall back to sensible defaults.
	events = []builtin.ConsoleRecordEvent{{Type: "scroll", Selector: "body", Value: "up"}}
	draft = naiveSkillDraft("s2", events, "")
	if !strings.Contains(draft.Content, "| scroll | `up` | 3 |") {
		t.Errorf("scroll without amount defaults to 3, got:\n%s", draft.Content)
	}
}

func TestSubstStepParams(t *testing.T) {
	s := BrowserConsoleStep{
		Type:       "type",
		Target:     "#order-{{工单号}}",
		Text:       "提交 {{工单号}}，备注 {{备注}}",
		URL:        "https://x/{{工单号}}",
		Value:      "{{工单号}}",
		Expression: "f({{工单号}})",
		Condition:  "url:/{{工单号}}",
		Files:      []string{"a-{{工单号}}.txt"},
	}
	substStepParams(&s, map[string]string{"工单号": "A123"})
	for i, got := range []string{s.Target, s.Text, s.URL, s.Value, s.Expression, s.Condition, s.Files[0]} {
		if strings.Contains(got, "{{工单号}}") || !strings.Contains(got, "A123") {
			t.Errorf("field %d: %q not substituted", i, got)
		}
	}
	// Unbound refs stay literal (visible in step output, not silently empty).
	s2 := BrowserConsoleStep{Type: "type", Target: "#x", Text: "给 {{未知}} 留言"}
	substStepParams(&s2, map[string]string{})
	if s2.Text != "给 {{未知}} 留言" {
		t.Errorf("unbound ref must stay literal, got %q", s2.Text)
	}
}
