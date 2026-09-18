package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSearchSessionsFindsContent (upgrade spec 4-5): a query matching a
// session's transcript body — not its title or preview — is found, with an
// excerpt centered on the match; a too-short query returns nothing.
func TestSearchSessionsFindsContent(t *testing.T) {
	dir := t.TempDir()
	// One session folder layout whose body mentions the needle.
	id := "20990101-000000-abcd"
	folder := filepath.Join(dir, id)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"role":"user","content":"please fix the flurbishWidget handler"}`
	if err := os.WriteFile(filepath.Join(folder, id+".jsonl"), []byte(body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hits := SearchSessions(dir, "flurbishwidget")
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	if filepath.Base(filepath.Dir(hits[0].Path)) != id {
		t.Fatalf("hit path = %s, want session %s", hits[0].Path, id)
	}
	if len(hits[0].Excerpts) != 1 || !strings.Contains(hits[0].Excerpts[0], "flurbishWidget") {
		t.Fatalf("excerpts = %v", hits[0].Excerpts)
	}
	if got := SearchSessions(dir, "f"); len(got) != 0 {
		t.Fatalf("short query should return no hits, got %d", len(got))
	}
	if got := SearchSessions(dir, "not-present-anywhere"); len(got) != 0 {
		t.Fatalf("no-match query should return no hits, got %d", len(got))
	}
}

// seedSession writes one session folder with a transcript body and, when
// title is non-empty, the sidecar that carries the session's title.
func seedSession(t *testing.T, dir, id, title, body string, mtime time.Time) string {
	t.Helper()
	folder := filepath.Join(dir, id)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", id, err)
	}
	path := filepath.Join(folder, id+".jsonl")
	if err := os.WriteFile(path, []byte(body+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", id, err)
	}
	if title != "" {
		if err := SaveBranchMeta(path, BranchMeta{TopicTitle: title}); err != nil {
			t.Fatalf("meta %s: %v", id, err)
		}
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", id, err)
		}
	}
	return path
}

// TestSearchSessionsFindsTitleMatches covers the gap the title-first ranking was
// introduced to close: a session whose TITLE matches but whose transcript does
// not used to be invisible to search, because only the body was scanned.
func TestSearchSessionsFindsTitleMatches(t *testing.T) {
	dir := t.TempDir()
	path := seedSession(t, dir, "20990101-000000-aaaa", "flurbishWidget 迁移计划",
		`{"role":"user","content":"unrelated body text"}`, time.Time{})

	hits := SearchSessions(dir, "flurbishwidget")
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1 (a title-only match must be found)", len(hits))
	}
	if hits[0].Path != path {
		t.Fatalf("hit path = %s, want %s", hits[0].Path, path)
	}
	if !hits[0].TitleMatch {
		t.Errorf("a title match must be flagged TitleMatch")
	}
	if hits[0].Title != "flurbishWidget 迁移计划" {
		t.Errorf("title not carried through: %q", hits[0].Title)
	}
}

// TestSearchSessionsRanksTitlesFirst is the ordering contract: a title match
// outranks a body match even when the body match is the more recent session.
func TestSearchSessionsRanksTitlesFirst(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	titlePath := seedSession(t, dir, "20200101-000000-tttt", "flurbishWidget 迁移计划",
		`{"role":"user","content":"nothing relevant here"}`, old)
	bodyPath := seedSession(t, dir, "20260101-000000-bbbb", "",
		`{"role":"user","content":"we discussed flurbishWidget yesterday"}`, recent)

	hits := SearchSessions(dir, "flurbishwidget")
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].Path != titlePath {
		t.Fatalf("the OLDER title match should rank first, got %s", hits[0].Path)
	}
	if !hits[0].TitleMatch {
		t.Errorf("hits[0] should be flagged as a title match")
	}
	if hits[1].Path != bodyPath {
		t.Fatalf("hits[1] = %s, want the body match %s", hits[1].Path, bodyPath)
	}
	if hits[1].TitleMatch {
		t.Errorf("hits[1] is a body match and must not be flagged as a title match")
	}
	if len(hits[1].Excerpts) == 0 {
		t.Errorf("a body match should still carry its excerpt")
	}
}

// TestSearchSessionsStopsScanningOnceTitlesFillTheList proves the second pass is
// skipped when the title pass already filled the result list: the body-only
// session is newer, so under the old body-first scan it would have been the
// first hit.
func TestSearchSessionsStopsScanningOnceTitlesFillTheList(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < searchHitCap; i++ {
		id := fmt.Sprintf("20200101-%06d-tttt", i)
		seedSession(t, dir, id, fmt.Sprintf("flurbishWidget 计划 %d", i),
			`{"role":"user","content":"unrelated"}`, old)
	}
	bodyPath := seedSession(t, dir, "20260101-000000-bbbb", "",
		`{"role":"user","content":"flurbishWidget in the body"}`, recent)

	hits := SearchSessions(dir, "flurbishwidget")
	if len(hits) != searchHitCap {
		t.Fatalf("hits = %d, want the cap %d", len(hits), searchHitCap)
	}
	for i, h := range hits {
		if !h.TitleMatch {
			t.Fatalf("hits[%d] is not a title match; the body pass should not have run", i)
		}
	}
	for _, h := range hits {
		if h.Path == bodyPath {
			t.Fatalf("a body-only hit must not displace a title hit")
		}
	}
}
