package team

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Shared-context archive (open-vetta 移植 P1-4 步骤 A).
//
// TeamContext.Notes is a bounded hot window (MaxSharedNotes) because it rides
// the JSON store and every member's blackboard digest. That bound used to be a
// silent data-loss bound: the 61st note evicted the 1st one, and nothing could
// ever get it back — so "knowledge flows sideways" quietly stopped holding for
// any team that ran long enough.
//
// This archive is the truth source. Every note a team ever publishes is
// appended as one JSON object per line to <storeDir>/<teamID>.notes.jsonl; the
// hot window is only what the digest renders. A member that needs an older
// entry reads it back through team_read_shared_history, which pages this file.
//
// The archive deliberately lives beside the JSON store rather than inside it:
// the store rewrites its whole file atomically on every mutation, which is fine
// for a bounded number of teams and cards but wrong for an unbounded log.
//
// Counterpart in open-vetta: packages/agent-team/shared-history.ts (public
// context is summarized at a checkpoint and the raw entries stay pageable).
// This is an independent Go implementation of the same idea.

const (
	// notesArchiveSuffix is the archive file's extension.
	notesArchiveSuffix = ".notes.jsonl"
	// DefaultNotesPageLimit is the page size used when a caller asks for none.
	DefaultNotesPageLimit = 20
	// MaxNotesPageLimit caps one page, so a single read can never blow a
	// member's context budget.
	MaxNotesPageLimit = 50
)

// NotesArchivePath returns a team's archive file. The path is derived from the
// store's own directory plus the team ID — never from a caller-supplied path —
// so a member can only ever read its own team's history.
func (s *Store) NotesArchivePath(teamID string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("%w: nil store", ErrInvalid)
	}
	clean, err := safeTeamID(teamID)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(s.path), clean+notesArchiveSuffix), nil
}

// safeTeamID rejects an ID that could escape the store directory. Team IDs are
// generated locally ("team_<millis>_<seq>"), so this only ever fires on a
// hand-edited store file — which is exactly when it matters.
func safeTeamID(teamID string) (string, error) {
	id := strings.TrimSpace(teamID)
	if id == "" {
		return "", fmt.Errorf("%w: empty team id", ErrInvalid)
	}
	if id != filepath.Base(id) || strings.ContainsAny(id, `/\`) ||
		strings.HasPrefix(id, ".") || strings.Contains(id, "..") {
		return "", fmt.Errorf("%w: unsafe team id %q", ErrInvalid, teamID)
	}
	return id, nil
}

// AppendNotes appends notes to a team's archive. Missing IDs/timestamps are
// filled here so an archived line is always self-contained. Callers must pass
// each note exactly once: the archive is append-only and does not de-duplicate.
func (s *Store) AppendNotes(teamID string, notes []Note) error {
	if len(notes) == 0 {
		return nil
	}
	path, err := s.NotesArchivePath(teamID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, n := range notes {
		if strings.TrimSpace(n.Text) == "" {
			continue
		}
		if n.ID == "" {
			n.ID = newID("note")
		}
		if n.At.IsZero() {
			n.At = time.Now().UTC()
		}
		b, err := json.Marshal(n)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if buf.Len() == 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// NotesPage is one page of a team's archived notes, newest first.
type NotesPage struct {
	Notes []Note `json:"notes"`
	// Total is the archive's full note count (not the page's), so a caller can
	// say "第 3 页，共 87 条" without a second read.
	Total   int  `json:"total"`
	Offset  int  `json:"offset"`
	Limit   int  `json:"limit"`
	HasMore bool `json:"has_more"`
}

// ReadNotes pages a team's archive, newest first. offset counts backwards from
// the newest note. A missing archive is an empty page, not an error: a team
// that never published a note simply has nothing to page through.
func (s *Store) ReadNotes(teamID string, offset, limit int) (NotesPage, error) {
	path, err := s.NotesArchivePath(teamID)
	if err != nil {
		return NotesPage{}, err
	}
	all, err := readNotesFile(path)
	if err != nil {
		return NotesPage{}, err
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = DefaultNotesPageLimit
	}
	if limit > MaxNotesPageLimit {
		limit = MaxNotesPageLimit
	}
	page := NotesPage{Total: len(all), Offset: offset, Limit: limit}
	if offset >= len(all) {
		return page, nil
	}
	out := make([]Note, 0, limit)
	for i := len(all) - 1 - offset; i >= 0 && len(out) < limit; i-- {
		out = append(out, all[i])
	}
	page.Notes = out
	page.HasMore = offset+len(out) < len(all)
	return page, nil
}

// CountNotes returns how many notes a team has archived. It is what lets the
// digest say "更早的 N 条已归档" without materialising them.
func (s *Store) CountNotes(teamID string) (int, error) {
	path, err := s.NotesArchivePath(teamID)
	if err != nil {
		return 0, err
	}
	notes, err := readNotesFile(path)
	if err != nil {
		return 0, err
	}
	return len(notes), nil
}

// readNotesFile decodes an archive file. A torn trailing line (the process died
// mid-append) is skipped rather than failing the read, matching the stats
// package's append-only convention.
func readNotesFile(path string) ([]Note, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Note
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var n Note
		if json.Unmarshal([]byte(line), &n) != nil {
			continue
		}
		if strings.TrimSpace(n.Text) == "" {
			continue
		}
		out = append(out, n)
	}
	return out, sc.Err()
}

// FormatNotesPage renders a page for a member (team_read_shared_history's
// result text). Author names are resolved through the caller so the member sees
// "张三" rather than "mem_17...", and the header states where the page sits in
// the archive so the model knows whether to page further.
func FormatNotesPage(page NotesPage, resolveAuthor func(string) string) string {
	if page.Total == 0 {
		return "这个团队还没有任何共享笔记。"
	}
	if len(page.Notes) == 0 {
		return fmt.Sprintf("没有更多了：共 %d 条笔记，你请求的起始位置（第 %d 条）已经超出。", page.Total, page.Offset+1)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "共享笔记归档：第 %d–%d 条（共 %d 条，最新在前）\n",
		page.Offset+1, page.Offset+len(page.Notes), page.Total)
	for _, n := range page.Notes {
		b.WriteString("- ")
		if resolveAuthor != nil {
			if who := strings.TrimSpace(resolveAuthor(n.Author)); who != "" {
				b.WriteString("[" + who + "] ")
			}
		}
		if !n.At.IsZero() {
			b.WriteString("(" + n.At.Local().Format("01-02 15:04") + ") ")
		}
		b.WriteString(n.Text)
		b.WriteString("\n")
	}
	if page.HasMore {
		fmt.Fprintf(&b, "\n还有更早的笔记：把 offset 设为 %d 继续往前翻。\n", page.Offset+len(page.Notes))
	} else {
		b.WriteString("\n已经到最早的笔记了。\n")
	}
	return b.String()
}
