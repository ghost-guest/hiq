package team

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The archive is the only place an aged-out note survives, so these tests care
// about three things: nothing is lost, paging is exact, and a caller cannot
// talk the store into reading a path outside its own directory.

func TestNotesArchiveKeepsEveryNotePastTheHotWindow(t *testing.T) {
	s, teamID := sharedStore(t)
	const total = MaxSharedNotes + 40
	for i := 0; i < total; i++ {
		if _, err := s.AddNote(teamID, Note{Text: noteText(i)}); err != nil {
			t.Fatalf("AddNote %d: %v", i, err)
		}
	}
	tm, _ := s.Get(teamID)
	if len(tm.Context.Notes) != MaxSharedNotes {
		t.Fatalf("hot window should stay capped at %d, got %d", MaxSharedNotes, len(tm.Context.Notes))
	}
	got, err := s.CountNotes(teamID)
	if err != nil {
		t.Fatalf("CountNotes: %v", err)
	}
	if got != total {
		t.Fatalf("archive should retain every note: want %d, got %d", total, got)
	}
	// The oldest note — long since evicted from the window — must still be
	// readable, which is the whole point of the archive.
	page, err := s.ReadNotes(teamID, total-1, 1)
	if err != nil {
		t.Fatalf("ReadNotes: %v", err)
	}
	if len(page.Notes) != 1 || page.Notes[0].Text != noteText(0) {
		t.Fatalf("the oldest note must be recoverable, got %+v", page.Notes)
	}
}

func TestReadNotesPagesNewestFirst(t *testing.T) {
	s, teamID := sharedStore(t)
	const total = 25
	for i := 0; i < total; i++ {
		if _, err := s.AddNote(teamID, Note{Text: noteText(i)}); err != nil {
			t.Fatalf("AddNote %d: %v", i, err)
		}
	}
	first, err := s.ReadNotes(teamID, 0, 5)
	if err != nil {
		t.Fatalf("ReadNotes: %v", err)
	}
	if len(first.Notes) != 5 {
		t.Fatalf("want a 5-note page, got %d", len(first.Notes))
	}
	if first.Notes[0].Text != noteText(total-1) {
		t.Errorf("page must start at the newest note, got %q", first.Notes[0].Text)
	}
	if !first.HasMore {
		t.Errorf("the first page of a %d-note archive should report more remaining, got %+v", total, first)
	}
	if first.Total != total {
		t.Errorf("page Total = %d, want %d", first.Total, total)
	}
	// The second page must continue exactly where the first stopped.
	second, err := s.ReadNotes(teamID, 5, 5)
	if err != nil {
		t.Fatalf("ReadNotes page 2: %v", err)
	}
	if len(second.Notes) != 5 || second.Notes[0].Text != noteText(total-6) {
		t.Fatalf("page 2 must continue the sequence, got %+v", second.Notes)
	}
	// The last page is short and reports no more.
	last, err := s.ReadNotes(teamID, 20, 10)
	if err != nil {
		t.Fatalf("ReadNotes last page: %v", err)
	}
	if len(last.Notes) != 5 || last.HasMore {
		t.Fatalf("last page should be the 5 remaining notes with no more, got len=%d more=%v", len(last.Notes), last.HasMore)
	}
	if last.Notes[len(last.Notes)-1].Text != noteText(0) {
		t.Errorf("the last page must end at the oldest note, got %q", last.Notes[len(last.Notes)-1].Text)
	}
}

func TestReadNotesBoundsAndEmptyArchive(t *testing.T) {
	s, teamID := sharedStore(t)
	// A team that never published a note has an empty page, not an error.
	empty, err := s.ReadNotes(teamID, 0, 10)
	if err != nil {
		t.Fatalf("ReadNotes on an empty archive: %v", err)
	}
	if empty.Total != 0 || len(empty.Notes) != 0 {
		t.Fatalf("expected an empty page, got %+v", empty)
	}
	// Out-of-range offsets also come back as an empty page rather than failing.
	for i := 0; i < 3; i++ {
		if _, err := s.AddNote(teamID, Note{Text: "n-" + string(rune('a'+i))}); err != nil {
			t.Fatalf("AddNote: %v", err)
		}
	}
	beyond, err := s.ReadNotes(teamID, 99, 10)
	if err != nil {
		t.Fatalf("ReadNotes past the end: %v", err)
	}
	if len(beyond.Notes) != 0 || beyond.Total != 3 {
		t.Fatalf("expected an empty page reporting the real total, got %+v", beyond)
	}
	// A negative offset is treated as the beginning; limit is clamped.
	clamped, err := s.ReadNotes(teamID, -4, 10_000)
	if err != nil {
		t.Fatalf("ReadNotes with odd args: %v", err)
	}
	if clamped.Offset != 0 || clamped.Limit != MaxNotesPageLimit {
		t.Errorf("offset/limit should be normalized, got offset=%d limit=%d", clamped.Offset, clamped.Limit)
	}
	if len(clamped.Notes) != 3 {
		t.Errorf("want all 3 notes, got %d", len(clamped.Notes))
	}
}

func TestNotesArchiveRejectsUnsafeTeamPaths(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"", "   ", "../escape", "..", "a/b", `a\b`, ".hidden"} {
		if _, err := s.NotesArchivePath(id); err == nil {
			t.Errorf("team id %q must be rejected", id)
		}
		if _, err := s.ReadNotes(id, 0, 5); err == nil {
			t.Errorf("ReadNotes must reject team id %q", id)
		}
	}
	// A legitimate generated ID resolves inside the store directory.
	path, err := s.NotesArchivePath("team_1700000000_1")
	if err != nil {
		t.Fatalf("a generated id must be accepted: %v", err)
	}
	if filepath.Dir(path) != filepath.Dir(s.path) {
		t.Fatalf("archive must live beside the store file, got %s", path)
	}
	if !strings.HasSuffix(path, ".notes.jsonl") {
		t.Fatalf("unexpected archive name: %s", path)
	}
}

func TestAppendNotesSkipsBlankTextAndFillsIdentity(t *testing.T) {
	s := newTestStore(t)
	tm := mustCreate(t, s, "归档")
	if err := s.AppendNotes(tm.ID, []Note{
		{Text: "  "},
		{Author: "mem_1", Text: "有内容"},
	}); err != nil {
		t.Fatalf("AppendNotes: %v", err)
	}
	page, err := s.ReadNotes(tm.ID, 0, 10)
	if err != nil {
		t.Fatalf("ReadNotes: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("a blank note must not be archived, got %d entries", page.Total)
	}
	n := page.Notes[0]
	if n.ID == "" || n.At.IsZero() {
		t.Errorf("archived notes must carry an id and a timestamp: %+v", n)
	}
}

func TestArchiveHintOnlyAppearsWithOlderNotes(t *testing.T) {
	tm := Team{ID: "t1", Name: "x"}
	tm.normalize()
	// Nothing archived beyond the window → no hint, so the digest is unchanged
	// for teams that never overflow.
	if hint := ArchiveHint(tm, len(tm.Context.Notes)); hint != "" {
		t.Errorf("no hint expected when the archive adds nothing, got %q", hint)
	}
	tm.Context.Notes = []Note{{ID: "n1", Text: "a"}}
	if hint := ArchiveHint(tm, 1); hint != "" {
		t.Errorf("archive equal to the window must not produce a hint, got %q", hint)
	}
	hint := ArchiveHint(tm, 31)
	if !strings.Contains(hint, "30") || !strings.Contains(hint, "team_read_shared_history") {
		t.Errorf("hint should state the count and the tool: %q", hint)
	}
}

func TestDigestWithArchivePointsAtTheHistoryTool(t *testing.T) {
	tm := Team{ID: "t1", Name: "x"}
	tm.normalize()
	tm.Context.Notes = []Note{{ID: "n1", Author: "m1", Text: "接口用 POST"}}
	withArchive := BlackboardDigestWithArchive(tm, 0, 12)
	if !strings.Contains(withArchive, "team_read_shared_history") {
		t.Errorf("digest should point at the history tool:\n%s", withArchive)
	}
	// Without an archive count the digest renders exactly as before, which keeps
	// every existing caller byte-compatible.
	plain := BlackboardDigest(tm, 0)
	if strings.Contains(plain, "team_read_shared_history") {
		t.Errorf("the plain digest must not mention the tool:\n%s", plain)
	}
}

func TestFormatNotesPageStatesPositionAndAuthor(t *testing.T) {
	page := NotesPage{
		Notes:   []Note{{ID: "n1", Author: "mem_1", Text: "订单号是 order_no", At: time.Now()}},
		Total:   30,
		Offset:  4,
		Limit:   1,
		HasMore: true,
	}
	got := FormatNotesPage(page, func(id string) string {
		if id == "mem_1" {
			return "调研员"
		}
		return id
	})
	if !strings.Contains(got, "第 5–5 条") || !strings.Contains(got, "共 30 条") {
		t.Errorf("page header should state the position: %q", got)
	}
	if !strings.Contains(got, "[调研员]") {
		t.Errorf("author should render as a name: %q", got)
	}
	if !strings.Contains(got, "offset 设为 5") {
		t.Errorf("a paged result must say how to continue: %q", got)
	}
	if empty := FormatNotesPage(NotesPage{}, nil); !strings.Contains(empty, "还没有") {
		t.Errorf("an empty archive should read clearly, got %q", empty)
	}
}

// noteText builds a distinct, ordered note body (dedupe compares text, so each
// note must differ).
func noteText(i int) string {
	return "note-" + strings.Repeat("#", i%7) + "-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
