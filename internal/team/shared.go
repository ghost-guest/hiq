package team

import (
	"fmt"
	"strings"
	"time"
)

// Shared context (P4 共享上下文).
//
// The blackboard already carries the leader's settled state (goal / constraints
// / decisions / artifacts / open questions). P4 adds the missing half: a
// member-writable scratchpad. While a member works it can publish notes
// (findings, gotchas, interface agreements, hand-off hints) that every LATER
// member reads in its blackboard digest — so knowledge flows sideways between
// contexts that never share a window, instead of only upward to the leader.
//
// Bounds are deliberate: notes are capped in count and length and deduplicated
// by text, so a chatty member can neither bloat nor dominate the digest that
// every other member pays for.

const (
	// MaxSharedNotes caps how many notes a team retains (newest win).
	MaxSharedNotes = 60
	// maxNoteChars caps one note's length.
	maxNoteChars = 500
	// notesDigestMax caps how many recent notes the blackboard digest renders.
	notesDigestMax = 12
)

// AddNote appends a shared-context note and trims the list to MaxSharedNotes.
// The blackboard version is bumped so cache/consumers can tell it moved.
//
// The note is ALSO appended to the team's durable archive: the hot window is a
// rendering bound, not a retention bound, so a note stays retrievable (via
// team_read_shared_history) long after it scrolls out of the digest.
func (s *Store) AddNote(teamID string, n Note) (Team, error) {
	if strings.TrimSpace(n.Text) == "" {
		return Team{}, fmt.Errorf("%w: note text is required", ErrInvalid)
	}
	added := false
	team, err := s.Update(teamID, func(t *Team) {
		if n.ID == "" {
			n.ID = newID("note")
		}
		if n.At.IsZero() {
			n.At = time.Now().UTC()
		}
		var appended bool
		t.Context.Notes, appended = addNoteTo(t.Context.Notes, n)
		if appended {
			t.Context.Version++
			added = true
		}
	})
	if err != nil {
		return Team{}, err
	}
	if added {
		// Best-effort: the in-store note is already accepted, so an archive
		// failure must not fail the user's write. It is logged by the caller
		// only in the sense that the note simply stays window-only.
		_ = s.AppendNotes(teamID, []Note{n})
	}
	return team, nil
}

// AddNoteTo appends a note to a list applying the same dedupe + bound policy as
// AddNote, for callers that already hold a Store.Update transaction (and so
// cannot re-enter the store).
func AddNoteTo(notes []Note, n Note) []Note {
	out, _ := addNoteTo(notes, n)
	return out
}

// NewNote builds a note with a fresh identity and its final (trimmed, clipped)
// text, so a caller that must keep the hot window and the durable archive in
// step can hand the SAME note to both and get identical ids on each side.
func NewNote(author, taskID, text string) Note {
	return Note{
		ID:     newID("note"),
		Author: author,
		TaskID: taskID,
		Text:   clipNote(text, maxNoteChars),
		At:     time.Now().UTC(),
	}
}

// AddNotes appends notes under the shared-note policy and reports which ones
// the hot window actually accepted — a blank note or a duplicate the dedupe
// dropped is absent from added, so a caller can archive exactly what was kept.
func AddNotes(notes []Note, add ...Note) (next []Note, added []Note) {
	next = notes
	for _, n := range add {
		var ok bool
		next, ok = addNoteTo(next, n)
		if ok {
			added = append(added, next[len(next)-1])
		}
	}
	return next, added
}

// addNoteTo is AddNoteTo plus whether the note was actually kept, so a caller
// can archive exactly the notes the hot window accepted (a duplicate the dedupe
// dropped must not appear in the archive either).
func addNoteTo(notes []Note, n Note) ([]Note, bool) {
	if strings.TrimSpace(n.Text) == "" {
		return notes, false
	}
	n.Text = clipNote(n.Text, maxNoteChars)
	kept := appendNoteUnique(notes, n)
	if len(kept) == len(notes) {
		return notes, false
	}
	if len(kept) > MaxSharedNotes {
		kept = kept[len(kept)-MaxSharedNotes:]
	}
	return kept, true
}

// appendNoteUnique appends a note unless an identical text is already present
// (from the same author), so a member restating itself doesn't fill the digest.
func appendNoteUnique(notes []Note, n Note) []Note {
	for _, ex := range notes {
		if ex.Author == n.Author && ex.Text == n.Text {
			return notes
		}
	}
	return append(notes, n)
}

// SharedNotes returns a team's notes newest-first (the panel's order).
func (t Team) SharedNotes() []Note {
	out := make([]Note, 0, len(t.Context.Notes))
	for i := len(t.Context.Notes) - 1; i >= 0; i-- {
		out = append(out, t.Context.Notes[i])
	}
	return out
}

// ResolveNoteAuthor names a note's author for display: the member's name, the
// raw ID, or 用户 for a user-authored note.
func (t Team) ResolveNoteAuthor(id string) string {
	if strings.TrimSpace(id) == "" {
		return "用户"
	}
	if m, ok := t.Member(id); ok {
		return m.Name
	}
	return id
}

// SharedNotesMarker opens the section a member appends to publish notes.
const SharedNotesMarker = "共享笔记"

// MaxNotesPerAnswer caps how many notes one member answer may publish.
const MaxNotesPerAnswer = 8

// ExtractSharedNotes splits a member's answer into its body and any shared notes
// it published.
//
// A member publishes notes by ending its answer with a 【共享笔记】 (or
// "## 共享笔记") section; the bullet lines under it become notes and are stripped
// from the body, so the stored deliverable stays clean prose.
//
// Parsing the answer — rather than exposing a write tool to the sub-session — is
// deliberate: it works for every model and provider without widening the
// member's tool surface or threading a run-scoped store handle through the tool
// registry.
func ExtractSharedNotes(answer string) (notes []string, body string) {
	lines := strings.Split(answer, "\n")
	cut := -1
	for i, line := range lines {
		if isNotesHeader(line) {
			cut = i
			break
		}
	}
	if cut < 0 {
		return nil, strings.TrimSpace(answer)
	}
	for _, line := range lines[cut+1:] {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		t = strings.TrimSpace(strings.TrimLeft(t, "-*•·>"))
		if t == "" || isNotesHeader(t) {
			continue
		}
		if n := clipNote(t, 300); n != "" {
			notes = append(notes, n)
		}
		if len(notes) >= MaxNotesPerAnswer {
			break
		}
	}
	return notes, strings.TrimSpace(strings.Join(lines[:cut], "\n"))
}

// isNotesHeader reports whether a line opens the shared-notes section, tolerating
// the decorations a model tends to add (a heading marker, the 【】 brackets).
func isNotesHeader(line string) bool {
	t := strings.TrimSpace(line)
	t = strings.TrimSpace(strings.TrimLeft(t, "#*>【 "))
	t = strings.TrimSpace(strings.TrimSuffix(t, "】"))
	return t == SharedNotesMarker
}

// clipNote trims a note to max runes, appending an ellipsis when it cut.
func clipNote(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return strings.TrimSpace(strings.Join(strings.Fields(string(r)), " "))
	}
	if max <= 1 {
		return string(r[:max])
	}
	return strings.TrimSpace(string(r[:max-1])) + "…"
}
