package team

import (
	"strings"
	"testing"

	"github.com/zzycxz/fairpeer/internal/taskmonitor"
)

func sharedStore(t *testing.T) (*Store, string) {
	t.Helper()
	s := newTestStore(t)
	tm := mustCreate(t, s, "共享上下文档")
	tm, err := s.AddMember(tm.ID, Member{Name: "调研员", Skills: []string{"research"}})
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	return s, tm.ID
}

func TestAddNoteLandsOnBlackboardAndDigest(t *testing.T) {
	s, teamID := sharedStore(t)
	tm, err := s.AddNote(teamID, Note{Text: "接口用 POST /v2/orders，不要再改回 v1"})
	if err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	if len(tm.Context.Notes) != 1 {
		t.Fatalf("note not stored: %+v", tm.Context.Notes)
	}
	if tm.Context.Version < 2 {
		t.Errorf("the blackboard version should bump, got %d", tm.Context.Version)
	}
	digest := BlackboardDigest(tm, 0)
	if !strings.Contains(digest, "共享笔记") {
		t.Errorf("digest must carry the shared notes section:\n%s", digest)
	}
	if !strings.Contains(digest, "POST /v2/orders") {
		t.Errorf("note text missing from digest:\n%s", digest)
	}
}

func TestAddNoteNamesItsAuthorInDigest(t *testing.T) {
	s, teamID := sharedStore(t)
	tm, _ := s.Get(teamID)
	author := tm.Members[0].ID
	if _, err := s.AddNote(teamID, Note{Author: author, Text: "老接口会在 12 月下线"}); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	tm, _ = s.Get(teamID)
	if got := tm.ResolveNoteAuthor(author); got != "调研员" {
		t.Fatalf("author should resolve to the member name, got %q", got)
	}
	if digest := BlackboardDigest(tm, 0); !strings.Contains(digest, "[调研员]") {
		t.Errorf("digest should attribute the note:\n%s", digest)
	}
	if got := tm.ResolveNoteAuthor(""); got != "用户" {
		t.Errorf("a blank author is the user, got %q", got)
	}
}

func TestAddNoteRejectsEmptyText(t *testing.T) {
	s, teamID := sharedStore(t)
	if _, err := s.AddNote(teamID, Note{Text: "   "}); err == nil {
		t.Fatal("expected an error for a blank note")
	}
}

func TestAddNoteDedupesIdenticalTextFromSameAuthor(t *testing.T) {
	s, teamID := sharedStore(t)
	tm, _ := s.Get(teamID)
	author := tm.Members[0].ID
	for i := 0; i < 3; i++ {
		if _, err := s.AddNote(teamID, Note{Author: author, Text: "同一句话"}); err != nil {
			t.Fatalf("AddNote: %v", err)
		}
	}
	tm, _ = s.Get(teamID)
	if len(tm.Context.Notes) != 1 {
		t.Fatalf("identical notes should collapse, got %d", len(tm.Context.Notes))
	}
}

func TestAddNoteCapsRetainedCount(t *testing.T) {
	s, teamID := sharedStore(t)
	for i := 0; i < MaxSharedNotes+5; i++ {
		if _, err := s.AddNote(teamID, Note{Text: "note-" + string(rune('a'+i%26)) + strings.Repeat("x", i)}); err != nil {
			t.Fatalf("AddNote %d: %v", i, err)
		}
	}
	tm, _ := s.Get(teamID)
	if len(tm.Context.Notes) != MaxSharedNotes {
		t.Fatalf("notes should be capped at %d, got %d", MaxSharedNotes, len(tm.Context.Notes))
	}
}

func TestSharedNotesAreNewestFirst(t *testing.T) {
	s, teamID := sharedStore(t)
	for _, txt := range []string{"第一条", "第二条", "第三条"} {
		if _, err := s.AddNote(teamID, Note{Text: txt}); err != nil {
			t.Fatalf("AddNote: %v", err)
		}
	}
	tm, _ := s.Get(teamID)
	got := tm.SharedNotes()
	if len(got) != 3 || got[0].Text != "第三条" {
		t.Fatalf("SharedNotes should be newest-first, got %+v", got)
	}
}

func TestExtractSharedNotesStripsTheSection(t *testing.T) {
	answer := `我完成了接口实现，文件在 internal/api/orders.go。
验收：已本地跑通。

【共享笔记】
- 订单号字段是 order_no，不是 orderNumber
- 分页统一用 page/size，后端已支持`
	notes, body := ExtractSharedNotes(answer)
	if len(notes) != 2 {
		t.Fatalf("want 2 notes, got %d: %v", len(notes), notes)
	}
	if !strings.Contains(notes[0], "order_no") {
		t.Errorf("note 1 lost: %q", notes[0])
	}
	if !strings.Contains(notes[1], "page/size") {
		t.Errorf("note 2 lost: %q", notes[1])
	}
	if strings.Contains(body, "共享笔记") || strings.Contains(body, "order_no") {
		t.Errorf("the notes section must be stripped from the deliverable:\n%s", body)
	}
	if !strings.Contains(body, "internal/api/orders.go") || !strings.Contains(body, "验收") {
		t.Errorf("body must keep everything before the marker:\n%s", body)
	}
}

func TestExtractSharedNotesHandlesHeadingForm(t *testing.T) {
	answer := "干完了。\n\n## 共享笔记\n* 用 sqlite 而不是 postgres\n* 迁移脚本还没写"
	notes, body := ExtractSharedNotes(answer)
	if len(notes) != 2 {
		t.Fatalf("want 2 notes, got %d: %v", len(notes), notes)
	}
	if strings.Contains(body, "sqlite") {
		t.Errorf("body should not retain the notes:\n%s", body)
	}
}

func TestExtractSharedNotesWithoutMarkerIsAllBody(t *testing.T) {
	answer := "  只有正文，没有共享笔记。  "
	notes, body := ExtractSharedNotes(answer)
	if len(notes) != 0 {
		t.Fatalf("want no notes, got %v", notes)
	}
	if body != "只有正文，没有共享笔记。" {
		t.Fatalf("body = %q", body)
	}
}

func TestExtractSharedNotesCapsPerAnswer(t *testing.T) {
	var b strings.Builder
	b.WriteString("done\n\n【共享笔记】\n")
	for i := 0; i < MaxNotesPerAnswer+6; i++ {
		b.WriteString("- 事项 " + strings.Repeat("z", i+1) + "\n")
	}
	notes, _ := ExtractSharedNotes(b.String())
	if len(notes) != MaxNotesPerAnswer {
		t.Fatalf("notes should be capped at %d, got %d", MaxNotesPerAnswer, len(notes))
	}
}

func TestResultArtifactCarriesADeliverableGist(t *testing.T) {
	tk := Task{ID: "t1", Title: "接口实现"}
	art := ResultArtifact(tk, "internal/api/orders.go\n\n实现了下单与查询两个接口，含参数校验。")
	if art.Path != "internal/api/orders.go" {
		t.Errorf("path = %q", art.Path)
	}
	if !strings.Contains(art.Summary, "下单与查询") {
		t.Errorf("summary should describe the deliverable, got %q", art.Summary)
	}
}

func TestDigestIncludesArtifactSummaries(t *testing.T) {
	tm := Team{
		ID:   "t1",
		Name: "x",
		Context: TeamContext{
			Goal: "交付",
			Artifacts: []Artifact{
				{ID: "a1", Title: "接口实现", Path: "internal/api/orders.go", Summary: "含参数校验"},
			},
		},
	}
	tm.normalize()
	digest := BlackboardDigest(tm, 0)
	if !strings.Contains(digest, "含参数校验") {
		t.Errorf("digest should carry the artifact gist:\n%s", digest)
	}
}

func TestMemberRunPromptTeachesSharedNotes(t *testing.T) {
	tm := Team{ID: "t1", Name: "x", Context: TeamContext{Goal: "交付"}}
	tm.normalize()
	m := Member{ID: "m1", Name: "后端", Role: "实现"}
	tk := Task{ID: "task1", Title: "写接口", Status: taskmonitor.TaskStateRunning}
	got := MemberRunPrompt(tm, m, tk)
	if !strings.Contains(got, SharedNotesMarker) {
		t.Errorf("the run prompt must teach the shared-notes channel:\n%s", got)
	}
}
