package team

import (
	"strings"
	"testing"
	"time"
)

// A checkpoint exists to keep old knowledge visible in bounded space, so these
// tests check the three properties that make it trustworthy: it only fires when
// the window is genuinely large, it always produces something (degrades instead
// of failing), and every member renders the same text.

func notesForCheckpoint(n int) []Note {
	out := make([]Note, 0, n)
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		out = append(out, Note{ID: "n" + itoa(i), Author: "mem_1", Text: noteText(i), At: base.Add(time.Duration(i) * time.Minute)})
	}
	return out
}

func TestPendingCheckpointNotesNeedsAFullWindow(t *testing.T) {
	tm := Team{ID: "t1", Name: "x"}
	tm.normalize()
	tm.Context.Notes = notesForCheckpoint(CheckpointTriggerNotes - 1)
	if got := PendingCheckpointNotes(tm); got != nil {
		t.Fatalf("a window below the trigger must not checkpoint, got %d notes", len(got))
	}
	tm.Context.Notes = notesForCheckpoint(CheckpointTriggerNotes)
	got := PendingCheckpointNotes(tm)
	if len(got) != CheckpointFoldCount {
		t.Fatalf("want the oldest %d notes, got %d", CheckpointFoldCount, len(got))
	}
	// It must fold the OLDEST notes, never the freshest ones.
	if got[0].ID != "n0" {
		t.Errorf("the fold must start at the oldest note, got %s", got[0].ID)
	}
	if got[len(got)-1].ID != "n"+itoa(CheckpointFoldCount-1) {
		t.Errorf("the fold must take a contiguous oldest run, got up to %s", got[len(got)-1].ID)
	}
}

func TestApplyCheckpointFoldsNotesAndKeepsTheRest(t *testing.T) {
	tm := Team{ID: "t1", Name: "x"}
	tm.normalize()
	tm.Context.Notes = notesForCheckpoint(CheckpointTriggerNotes)
	version := tm.Context.Version
	fold := PendingCheckpointNotes(tm)

	ApplyCheckpoint(&tm, fold, "早期结论：接口统一走 POST，分页用 page/size。", false)

	if len(tm.Context.Checkpoints) != 1 {
		t.Fatalf("checkpoint not recorded: %+v", tm.Context.Checkpoints)
	}
	cp := tm.Context.Checkpoints[0]
	if cp.Count != len(fold) || cp.Degraded || cp.ID == "" {
		t.Errorf("checkpoint metadata wrong: %+v", cp)
	}
	if cp.UpTo.IsZero() {
		t.Error("a checkpoint must record how far it covers")
	}
	if len(tm.Context.Notes) != CheckpointTriggerNotes-len(fold) {
		t.Fatalf("the covered notes should leave the window, got %d left", len(tm.Context.Notes))
	}
	// The survivors must be the NEWEST notes: the checkpoint took the oldest.
	if tm.Context.Notes[0].ID != "n"+itoa(len(fold)) {
		t.Errorf("wrong notes survived: first survivor is %s", tm.Context.Notes[0].ID)
	}
	if tm.Context.Version <= version {
		t.Error("folding must bump the blackboard version")
	}
	// Re-running with the same (now absent) notes must be a no-op rather than
	// double-counting them.
	ApplyCheckpoint(&tm, fold, "重复", false)
	if len(tm.Context.Checkpoints) != 2 {
		t.Fatalf("ApplyCheckpoint appends per call, got %d", len(tm.Context.Checkpoints))
	}
	if len(tm.Context.Notes) != CheckpointTriggerNotes-len(fold) {
		t.Errorf("re-folding removed unrelated notes: %d left", len(tm.Context.Notes))
	}
}

func TestApplyCheckpointDegradesInsteadOfFailing(t *testing.T) {
	tm := Team{ID: "t1", Name: "x"}
	tm.normalize()
	tm.Context.Notes = notesForCheckpoint(8)
	fold := tm.Context.Notes[:3]
	// An empty summary (the LLM pass failed or returned nothing) must still
	// leave a usable checkpoint rather than dropping the knowledge.
	ApplyCheckpoint(&tm, fold, "   ", true)
	cp := tm.Context.Checkpoints[0]
	if !cp.Degraded {
		t.Error("a blank summary must be marked degraded")
	}
	if !strings.Contains(cp.Summary, "索引") {
		t.Errorf("the degraded summary should announce itself: %q", cp.Summary)
	}
	if !strings.Contains(cp.Summary, "note-") {
		t.Errorf("the degraded summary should index the folded notes: %q", cp.Summary)
	}
}

func TestApplyCheckpointBoundsItsLength(t *testing.T) {
	tm := Team{ID: "t1", Name: "x"}
	tm.normalize()
	notes := notesForCheckpoint(5)
	ApplyCheckpoint(&tm, notes, strings.Repeat("啰", MaxCheckpointChars*3), false)
	if got := len([]rune(tm.Context.Checkpoints[0].Summary)); got > MaxCheckpointChars {
		t.Fatalf("summary must be bounded to %d runes, got %d", MaxCheckpointChars, got)
	}
}

func TestCheckpointsAreCappedAndNormalized(t *testing.T) {
	tm := Team{ID: "t1", Name: "x"}
	for i := 0; i < MaxCheckpoints+4; i++ {
		tm.Context.Checkpoints = append(tm.Context.Checkpoints, Checkpoint{Summary: "s" + itoa(i)})
	}
	tm.normalize()
	if len(tm.Context.Checkpoints) != MaxCheckpoints {
		t.Fatalf("checkpoints should be capped at %d, got %d", MaxCheckpoints, len(tm.Context.Checkpoints))
	}
	// The kept ones are the newest: older checkpoints cover a range a newer one
	// already supersedes.
	if last := tm.Context.Checkpoints[len(tm.Context.Checkpoints)-1].Summary; last != "s"+itoa(MaxCheckpoints+3) {
		t.Errorf("the newest checkpoints must survive, got %q", last)
	}
}

func TestFallbackCheckpointSummaryIsOneLinePerNote(t *testing.T) {
	notes := []Note{
		{Author: "mem_1", Text: "订单号字段是 order_no。后面还有一句不要。"},
		{Author: "mem_2", Text: "没有标点的一句话就这样结束"},
		{Text: "   "},
	}
	got := FallbackCheckpointSummary(notes)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 { // header + 2 notes (the blank one is dropped)
		t.Fatalf("want a header plus one line per non-blank note, got:\n%s", got)
	}
	if !strings.Contains(got, "order_no。") || strings.Contains(got, "不要") {
		t.Errorf("a degraded entry should keep just the first sentence: %q", got)
	}
}

func TestCheckpointPromptCarriesGoalAndAuthors(t *testing.T) {
	tm := Team{ID: "t1", Name: "x", Context: TeamContext{Goal: "交付订单服务"}}
	tm.Members = []Member{{ID: "mem_1", Name: "调研员"}}
	tm.normalize()
	system, user := CheckpointPrompt(tm, []Note{{Author: "mem_1", Text: "接口用 POST"}})
	if !strings.Contains(system, "摘要") {
		t.Errorf("system prompt should frame the summarisation task: %q", system)
	}
	if !strings.Contains(user, "交付订单服务") {
		t.Errorf("the team goal should orient the summary:\n%s", user)
	}
	if !strings.Contains(user, "[调研员]") {
		t.Errorf("notes should be attributed by name, not id:\n%s", user)
	}
}

func TestEveryMemberRendersTheSameCheckpoints(t *testing.T) {
	tm := Team{ID: "t1", Name: "x", Context: TeamContext{Goal: "交付"}}
	tm.Members = []Member{
		{ID: "mem_1", Name: "调研员"},
		{ID: "mem_2", Name: "后端"},
	}
	tm.normalize()
	ApplyCheckpoint(&tm, []Note{{ID: "n1", Text: "接口用 POST"}}, "接口统一 POST。", false)

	// Consistency is structural: the block is derived from the team record, so
	// two members with entirely different identities still read the same history.
	block := CheckpointsDigest(tm)
	if block == "" || !strings.Contains(block, "接口统一 POST") {
		t.Fatalf("checkpoint block missing:\n%s", block)
	}
	a := MemberRunPrompt(tm, tm.Members[0], Task{ID: "t1", Title: "调研"})
	b := MemberRunPrompt(tm, tm.Members[1], Task{ID: "t2", Title: "实现"})
	countA := strings.Count(a, "接口统一 POST")
	countB := strings.Count(b, "接口统一 POST")
	if countA != 1 || countB != 1 {
		t.Fatalf("both members must see the checkpoint exactly once (got %d/%d)", countA, countB)
	}
}

func TestDigestRendersAtMostTheNewestCheckpoints(t *testing.T) {
	tm := Team{ID: "t1", Name: "x"}
	tm.normalize()
	for i := 0; i < checkpointsDigestMax+3; i++ {
		tm.Context.Checkpoints = append(tm.Context.Checkpoints, Checkpoint{Summary: "cp-" + itoa(i)})
	}
	block := CheckpointsDigest(tm)
	if got := strings.Count(block, "cp-"); got != checkpointsDigestMax {
		t.Fatalf("digest should render the newest %d checkpoints, got %d:\n%s", checkpointsDigestMax, got, block)
	}
	if !strings.Contains(block, "cp-"+itoa(checkpointsDigestMax+2)) {
		t.Errorf("the newest checkpoint must be rendered:\n%s", block)
	}
	if strings.Contains(block, "cp-0") {
		t.Errorf("the oldest checkpoints must be dropped:\n%s", block)
	}
}

func TestDigestStaysBoundedWithCheckpointsAndNotes(t *testing.T) {
	tm := Team{ID: "t1", Name: "x", Context: TeamContext{Goal: "交付"}}
	tm.normalize()
	for i := 0; i < checkpointsDigestMax+2; i++ {
		tm.Context.Checkpoints = append(tm.Context.Checkpoints, Checkpoint{
			Summary: strings.Repeat("摘要内容", 60),
		})
	}
	tm.Context.Notes = notesForCheckpoint(MaxSharedNotes)
	tm.Context.OpenQuestions = []string{"还有一个未决问题"}
	digest := BlackboardDigest(tm, 0)
	if len(digest) > DefaultDigestMaxChars+2 {
		t.Fatalf("digest must stay bounded at ~%d bytes, got %d", DefaultDigestMaxChars, len(digest))
	}
}
