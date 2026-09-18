package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/zzycxz/fairpeer/internal/taskmonitor"
	teampkg "github.com/zzycxz/fairpeer/internal/team"
)

// Shared-context archive + checkpoints on the desktop side (open-vetta 移植
// P1-4). The domain logic is covered in internal/team; these tests cover the
// wiring that only exists here: the member's history tool, the archive write
// that follows a finished card, and the checkpoint pass's degradation path.

// TestTeamRegistryExposesTheHistoryReaderToMembersOnly is the P1-4 wiring
// contract: the member's registry carries team_read_shared_history, it is
// read-only, and it is a COPY — registering it must not mutate the registry the
// main session reads, whose tool list is part of the cached prompt prefix.
func TestTeamRegistryExposesTheHistoryReaderToMembersOnly(t *testing.T) {
	a, store := teamTestApp(t)
	tm := seedTeam(t, store, taskmonitor.TaskStateQueued, 2)

	reg := a.teamRegistry(store, tm, tm.Members[0])
	tl, ok := reg.Get(teamSharedHistoryToolName)
	if !ok {
		t.Fatalf("member registry is missing %s: %v", teamSharedHistoryToolName, reg.Names())
	}
	if !tl.ReadOnly() {
		t.Errorf("%s must be read-only so a batch of reads may run in parallel", teamSharedHistoryToolName)
	}
	// The tool must be scoped to THIS team and never name a team in its schema.
	var schema map[string]any
	if err := json.Unmarshal(tl.Schema(), &schema); err != nil {
		t.Fatalf("tool schema is not valid JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	for _, forbidden := range []string{"team_id", "teamId", "path"} {
		if _, ok := props[forbidden]; ok {
			t.Errorf("the tool must not accept %q — the team is captured, not supplied", forbidden)
		}
	}
}

// A member asking for history gets its own team's notes, newest first, with
// authors resolved to display names.
func TestTeamSharedHistoryToolPagesTheTeamsOwnNotes(t *testing.T) {
	_, store := teamTestApp(t)
	tm := seedTeam(t, store, taskmonitor.TaskStateQueued, 2)
	member := tm.Members[0]
	for _, txt := range []string{"第一条", "第二条", "第三条"} {
		if _, err := store.AddNote(tm.ID, teampkg.Note{Author: member.ID, Text: txt}); err != nil {
			t.Fatalf("AddNote: %v", err)
		}
	}
	fresh, _ := store.Get(tm.ID)

	history := &teamSharedHistoryTool{store: store, team: fresh}
	out, err := history.Execute(context.Background(), json.RawMessage(`{"limit":2}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "第三条") || !strings.Contains(out, "第二条") {
		t.Fatalf("the newest page should carry the newest notes:\n%s", out)
	}
	if strings.Contains(out, "第一条") {
		t.Fatalf("a limit of 2 must not return the third note:\n%s", out)
	}
	if !strings.Contains(out, member.Name) {
		t.Errorf("the author should render as a name, not an id:\n%s", out)
	}
	if !strings.Contains(out, "共 3 条") {
		t.Errorf("the page should state the archive size:\n%s", out)
	}

	// A malformed argument object must degrade to the newest page, not fail.
	loose, err := history.Execute(context.Background(), json.RawMessage(`"not-an-object"`))
	if err != nil {
		t.Fatalf("a malformed argument must not fail the read: %v", err)
	}
	if !strings.Contains(loose, "第三条") {
		t.Errorf("a malformed argument should still return the newest page:\n%s", loose)
	}
}

// TestFinishTeamTaskArchivesPublishedNotes closes the loop that used to lose
// data: a note the digest has stopped rendering must still be on disk.
func TestFinishTeamTaskArchivesPublishedNotes(t *testing.T) {
	a, store := teamTestApp(t)
	tm := seedTeam(t, store, taskmonitor.TaskStateRunning, 2)
	card := tm.Tasks[0]

	answer := "接口写完了，文件在 internal/api/orders.go。\n\n【共享笔记】\n- 订单号字段是 order_no\n- 分页统一用 page/size"
	a.finishTeamTask(tm.ID, card.ID, answer, nil)

	fresh, _ := store.Get(tm.ID)
	if fresh.Context.Notes == nil || len(fresh.Context.Notes) != 2 {
		t.Fatalf("both notes should land on the blackboard, got %+v", fresh.Context.Notes)
	}
	if strings.Contains(fresh.Tasks[0].Deliverable, "共享笔记") {
		t.Errorf("the deliverable must keep the clean body:\n%s", fresh.Tasks[0].Deliverable)
	}
	page, err := store.ReadNotes(tm.ID, 0, 10)
	if err != nil {
		t.Fatalf("ReadNotes: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("the archive should hold both notes, got %d", page.Total)
	}
	// The archived copy must carry the same identity as the stored one, so a
	// note can be traced across the window and the archive.
	archived := map[string]bool{}
	for _, n := range page.Notes {
		if n.ID == "" {
			t.Fatalf("archived note has no id: %+v", n)
		}
		if n.Author != card.AssigneeID || n.TaskID != card.ID {
			t.Errorf("archived note lost its provenance: %+v", n)
		}
		archived[n.ID] = true
	}
	for _, n := range fresh.Context.Notes {
		if !archived[n.ID] {
			t.Errorf("note %s is in the window but not the archive", n.ID)
		}
	}

	// A duplicate note must be archived in neither place.
	again := "又干了一遍。\n\n【共享笔记】\n- 订单号字段是 order_no"
	a.finishTeamTask(tm.ID, card.ID, again, nil)
	if page, err = store.ReadNotes(tm.ID, 0, 10); err != nil {
		t.Fatalf("ReadNotes after a duplicate: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("a deduped note must not be archived, got %d entries", page.Total)
	}
}

// The checkpoint pass must never block or fail a run: with no usable model it
// degrades to an index-style summary and the covered notes still leave the
// window (they remain in the archive).
func TestRunTeamCheckpointDegradesWithoutAModel(t *testing.T) {
	a, store := teamTestApp(t)
	tm := seedTeam(t, store, taskmonitor.TaskStateQueued, 2)

	// Fill the hot window past the trigger. Seeding goes through AddNote (not a
	// raw Update) because that is what also appends to the archive — the point
	// of the last assertion below.
	for i := 0; i < teampkg.CheckpointTriggerNotes; i++ {
		txt := "早期结论 " + strings.Repeat("x", 1+i%5) + " #" + testIndex(i)
		if _, err := store.AddNote(tm.ID, teampkg.NewNote("m1", "", txt)); err != nil {
			t.Fatalf("seed note %d: %v", i, err)
		}
	}

	a.runTeamCheckpoint(tm.ID)

	fresh, _ := store.Get(tm.ID)
	if len(fresh.Context.Checkpoints) != 1 {
		t.Fatalf("a checkpoint should have been recorded, got %d", len(fresh.Context.Checkpoints))
	}
	cp := fresh.Context.Checkpoints[0]
	if !cp.Degraded {
		t.Errorf("with no usable model the checkpoint must be marked degraded")
	}
	if cp.Summary == "" {
		t.Errorf("a degraded checkpoint must still say something")
	}
	if cp.Count != teampkg.CheckpointFoldCount {
		t.Errorf("checkpoint folded %d notes, want %d", cp.Count, teampkg.CheckpointFoldCount)
	}
	if want := teampkg.CheckpointTriggerNotes - teampkg.CheckpointFoldCount; len(fresh.Context.Notes) != want {
		t.Errorf("hot window = %d notes, want %d left", len(fresh.Context.Notes), want)
	}
	// Nothing was lost: the archive still holds every note.
	if n, err := store.CountNotes(tm.ID); err != nil || n != teampkg.CheckpointTriggerNotes {
		t.Fatalf("archive count = %d (err %v), want %d", n, err, teampkg.CheckpointTriggerNotes)
	}
	// And every member now renders the checkpoint, identically.
	for _, m := range fresh.Members {
		prompt := teampkg.MemberRunPromptWithArchive(fresh, m, fresh.Tasks[0], 0)
		if !strings.Contains(prompt, cp.Summary) {
			t.Errorf("member %s does not see the checkpoint", m.Name)
		}
	}
}

// A window below the trigger must leave the team alone: checkpointing costs a
// model call, so it only runs when the summary actually pays for itself.
func TestRunTeamCheckpointIsANoOpBelowTheTrigger(t *testing.T) {
	a, store := teamTestApp(t)
	tm := seedTeam(t, store, taskmonitor.TaskStateQueued, 2)
	if _, err := store.AddNote(tm.ID, teampkg.Note{Text: "只有一条笔记"}); err != nil {
		t.Fatalf("AddNote: %v", err)
	}

	a.runTeamCheckpoint(tm.ID)

	fresh, _ := store.Get(tm.ID)
	if len(fresh.Context.Checkpoints) != 0 {
		t.Fatalf("a small window must not be checkpointed: %+v", fresh.Context.Checkpoints)
	}
	if len(fresh.Context.Notes) != 1 {
		t.Fatalf("notes changed: %d", len(fresh.Context.Notes))
	}
}

// Only one checkpoint pass may run per team at a time, or a burst of members
// finishing together would fold the same notes repeatedly.
func TestTeamCheckpointSlotIsExclusive(t *testing.T) {
	a, _ := teamTestApp(t)
	if !a.beginTeamCheckpoint("team_x") {
		t.Fatal("the first claim should win the slot")
	}
	if a.beginTeamCheckpoint("team_x") {
		t.Fatal("a second concurrent pass must be refused")
	}
	if !a.beginTeamCheckpoint("team_y") {
		t.Fatal("a different team has its own slot")
	}
	a.endTeamCheckpoint("team_x")
	if !a.beginTeamCheckpoint("team_x") {
		t.Fatal("releasing the slot must allow the next pass")
	}
	a.endTeamCheckpoint("team_x")
	a.endTeamCheckpoint("team_y")
}

// Deleting a team must take its archive with it, or a deleted team's notes stay
// on disk with nothing that can reach them.
func TestDeleteTeamProjectRemovesTheArchive(t *testing.T) {
	a, store := teamTestApp(t)
	tm := seedTeam(t, store, taskmonitor.TaskStateQueued, 2)
	if _, err := store.AddNote(tm.ID, teampkg.Note{Text: "会随团队一起删掉"}); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	path, err := store.NotesArchivePath(tm.ID)
	if err != nil {
		t.Fatalf("NotesArchivePath: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the archive should exist before deleting: %v", err)
	}

	if err := a.DeleteTeamProject(tm.ID); err != nil {
		t.Fatalf("DeleteTeamProject: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the archive should be gone, stat err = %v", err)
	}
}

// testIndex keeps the note bodies distinct without pulling strconv into a test
// whose point is the wiring, not the formatting.
func testIndex(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
