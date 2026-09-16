package team

import (
	"strings"
	"testing"
)

func TestMemberRunPromptComposition(t *testing.T) {
	tm := Team{
		Name: "登录重构",
		Goal: "把登录改成 OAuth",
		Members: []Member{
			{ID: "m1", Name: "阿珂", Role: "后端实现", Skills: []string{"go", "auth"}},
		},
		Context: TeamContext{
			Goal:        "把登录改成 OAuth",
			Constraints: "不能停机",
			Decisions:   []Decision{{ID: "d1", Text: "用 PKCE"}},
			Version:     2,
		},
	}
	m := tm.Members[0]
	tk := Task{
		ID: "t1", Title: "实现授权码流程", Desc: "含 PKCE",
		RequiredSkills: []string{"go"},
		Acceptance:     []Criterion{{Text: "单测通过"}, {Text: "无停机"}},
	}

	got := MemberRunPrompt(tm, m, tk)
	wants := []string{
		"阿珂", "后端实现", "go", // composed identity
		"把登录改成 OAuth", "不能停机", "用 PKCE", // L0 blackboard
		"实现授权码流程", "含 PKCE", "单测通过", "无停机", "验收标准", // task frame
		"团队上下文",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("MemberRunPrompt missing %q\n---\n%s", w, got)
		}
	}
}

func TestMemberRunPromptPrefersCustomPersona(t *testing.T) {
	tm := Team{Name: "x", Goal: "g"}
	m := Member{ID: "m", Name: "小周", SystemPrompt: "你是一名安全审计员，只做只读审计，不改代码。"}
	got := MemberRunPrompt(tm, m, Task{ID: "t", Title: "审计"})

	if !strings.Contains(got, "安全审计员") {
		t.Fatalf("custom persona missing:\n%s", got)
	}
	if strings.Contains(got, "你是团队成员「小周」") {
		t.Fatalf("composed identity must be skipped when SystemPrompt is set:\n%s", got)
	}
}

func TestMemberRunPromptLeaderFallback(t *testing.T) {
	got := MemberRunPrompt(Team{Name: "x"}, Member{ID: "L", Name: "团长", IsLeader: true}, Task{ID: "t", Title: "t"})
	if !strings.Contains(got, "团长（负责人）") {
		t.Fatalf("leader fallback prompt missing:\n%s", got)
	}
}

// The blackboard digest is what keeps a member from drifting; an empty team
// context must simply omit the section rather than emit an empty header.
func TestMemberRunPromptOmitsEmptyBlackboard(t *testing.T) {
	got := MemberRunPrompt(Team{Name: "x"}, Member{ID: "m", Name: "n"}, Task{ID: "t", Title: "t"})
	if strings.Contains(got, "团队上下文") {
		t.Fatalf("empty team context should not emit a blackboard section:\n%s", got)
	}
}

func TestTaskBrief(t *testing.T) {
	tm := Team{Goal: "总目标"}
	tk := Task{ID: "t1", Title: "标题", Desc: "说明", Acceptance: []Criterion{{Text: "标准A"}}}
	got := TaskBrief(tm, tk)
	for _, w := range []string{"标题", "说明", "标准A", "总目标"} {
		if !strings.Contains(got, w) {
			t.Errorf("TaskBrief missing %q\n---\n%s", w, got)
		}
	}
}

func TestResultArtifactAndPathExtraction(t *testing.T) {
	tk := Task{ID: "t1", Title: "产出"}
	got := ResultArtifact(tk, "已完成，改动如下：\n\nsrc/auth/pkce.go\n其余略")
	if got.TaskID != "t1" || got.Title != "产出" || got.Kind != "deliverable" {
		t.Fatalf("artifact metadata wrong: %+v", got)
	}
	if got.Path != "src/auth/pkce.go" {
		t.Fatalf("path = %q, want src/auth/pkce.go", got.Path)
	}
	if !strings.HasPrefix(got.ID, "art") {
		t.Fatalf("artifact id %q should be minted with the art prefix", got.ID)
	}

	// Bare filename fallback, and prose must never be mistaken for a path.
	if p := ResultArtifact(tk, "\n\n      \nmain.go").Path; p != "main.go" {
		t.Fatalf("bare filename path = %q, want main.go", p)
	}
	if p := ResultArtifact(tk, "这是一段中文说明，没有任何文件名。").Path; p != "" {
		t.Fatalf("prose must not yield a path, got %q", p)
	}
}

func TestLooksLikeFilename(t *testing.T) {
	cases := map[string]bool{
		"main.go":         true,
		"a/b/c.tsx":       true,
		"说明":              false,
		"Done.":           false, // trailing dot, no extension
		"two words.go":    false, // contains a space
		"archive.tar.gz2": true,  // still a single plausible token
		".env":            false, // no basename before the dot
	}
	for in, want := range cases {
		if got := looksLikeFilename(in); got != want {
			t.Errorf("looksLikeFilename(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTrimAll(t *testing.T) {
	got := trimAll([]string{" go ", "", "  ", "auth"})
	if len(got) != 2 || got[0] != "go" || got[1] != "auth" {
		t.Fatalf("trimAll = %#v", got)
	}
}
