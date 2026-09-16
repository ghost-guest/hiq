package team

import (
	"strings"
	"testing"
)

func TestParseMemberDraftPlainJSON(t *testing.T) {
	raw := `{"name":"后端实现","role":"实现服务端","skills":["go","sql"],"system_prompt":"你就是后端实现。","avatar":"🛠"}`
	m, err := ParseMemberDraft(raw)
	if err != nil {
		t.Fatalf("ParseMemberDraft: %v", err)
	}
	if m.Name != "后端实现" || m.Role != "实现服务端" {
		t.Fatalf("parsed %+v", m)
	}
	if len(m.Skills) != 2 {
		t.Fatalf("skills = %v", m.Skills)
	}
}

func TestParseMemberDraftToleratesFenceAndProse(t *testing.T) {
	raw := "好的，这是配置：\n```json\n{\n  \"name\": \"调研员\",\n  \"role\": \"竞品调研\",\n  \"skills\": [\"research\"]\n}\n```\n希望有帮助！"
	m, err := ParseMemberDraft(raw)
	if err != nil {
		t.Fatalf("ParseMemberDraft: %v", err)
	}
	if m.Name != "调研员" {
		t.Fatalf("name = %q", m.Name)
	}
}

func TestParseMemberDraftBraceInsideString(t *testing.T) {
	raw := `前缀 {"name":"X","role":"处理 {\"a\":1} 这类结构" } 后缀`
	m, err := ParseMemberDraft(raw)
	if err != nil {
		t.Fatalf("ParseMemberDraft: %v", err)
	}
	if !strings.Contains(m.Role, "{") {
		t.Fatalf("role should keep the literal braces, got %q", m.Role)
	}
}

func TestParseMemberDraftErrors(t *testing.T) {
	if _, err := ParseMemberDraft("no json here"); err == nil {
		t.Fatal("expected error for missing JSON")
	}
	if _, err := ParseMemberDraft(`{"role":"no name"}`); err == nil {
		t.Fatal("expected error for missing name")
	}
	if _, err := ParseMemberDraft(`{"name":"X"`); err == nil {
		t.Fatal("expected error for unterminated JSON")
	}
}

func TestParseTeamDraftLeaderInvariant(t *testing.T) {
	raw := `{
	  "name": "交付团队",
	  "goal": "三个月上线结算系统",
	  "constraints": "预算有限",
	  "leader_index": 0,
	  "members": [
	    {"name":"团长","role":"拆解与跟踪","skills":["planning"],"is_leader":true},
	    {"name":"开发","role":"实现","skills":["go"]}
	  ]
	}`
	tm, err := ParseTeamDraft(raw)
	if err != nil {
		t.Fatalf("ParseTeamDraft: %v", err)
	}
	if tm.Name != "交付团队" || tm.Context.Goal != "三个月上线结算系统" {
		t.Fatalf("parsed %+v", tm)
	}
	l, ok := tm.Leader()
	if !ok || l.Name != "团长" {
		t.Fatalf("leader = %+v ok=%v", l, ok)
	}
	if tm.Context.Version == 0 {
		t.Fatal("normalize should set a context version")
	}
}

func TestParseTeamDraftPromotesWhenNoLeaderFlagged(t *testing.T) {
	raw := `{"name":"T","goal":"g","members":[{"name":"甲"},{"name":"乙"}]}`
	tm, err := ParseTeamDraft(raw)
	if err != nil {
		t.Fatalf("ParseTeamDraft: %v", err)
	}
	l, ok := tm.Leader()
	if !ok || l.Name != "甲" {
		t.Fatalf("first member should be promoted to leader, got %+v ok=%v", l, ok)
	}
}

func TestParseTeamDraftErrors(t *testing.T) {
	if _, err := ParseTeamDraft(`{"name":"","members":[{"name":"a"}]}`); err == nil {
		t.Fatal("expected error for empty name")
	}
	if _, err := ParseTeamDraft(`{"name":"T","members":[]}`); err == nil {
		t.Fatal("expected error for no members")
	}
}

func TestBlackboardDigestIndexesAndTruncates(t *testing.T) {
	tm := Team{
		Name: "黑板书",
		Context: TeamContext{
			Goal:        "上线结算系统",
			Constraints: "预算 50 万",
			Decisions:   []Decision{{Text: "用 Go"}, {Text: "先做 MVP"}},
			Artifacts:   []Artifact{{Title: "PRD", Path: "/docs/prd.md"}},
			OpenQuestions: []string{
				"是否需要支持多币种",
			},
		},
	}
	d := BlackboardDigest(tm, 0)
	for _, want := range []string{"上线结算系统", "预算 50 万", "用 Go", "PRD", "多币种"} {
		if !strings.Contains(d, want) {
			t.Fatalf("digest missing %q:\n%s", want, d)
		}
	}

	// Tight budget must truncate at a line boundary and never exceed max+ellipsis.
	long := Team{Context: TeamContext{
		Goal: strings.Repeat("目标", 400),
	}}
	got := BlackboardDigest(long, 100)
	if len(got) > 200 {
		t.Fatalf("digest not truncated: %d bytes", len(got))
	}
	if strings.HasSuffix(got, "目") {
		t.Fatalf("digest cut mid-line: %q", got[len(got)-10:])
	}
}

func TestMemberIdentityPromptFallback(t *testing.T) {
	m := Member{Name: "测试员", Role: "接口测试", Skills: []string{"api", "pytest"}}
	p := m.IdentityPrompt()
	for _, want := range []string{"测试员", "接口测试", "api"} {
		if !strings.Contains(p, want) {
			t.Fatalf("identity prompt missing %q: %s", want, p)
		}
	}
	// An explicit system prompt wins.
	m.SystemPrompt = "自定义人设"
	if got := m.IdentityPrompt(); got != "自定义人设" {
		t.Fatalf("explicit prompt not used: %q", got)
	}
}
