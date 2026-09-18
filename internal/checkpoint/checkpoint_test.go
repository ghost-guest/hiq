package checkpoint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/zzycxz/hiq/internal/diff"
	fileenc "github.com/zzycxz/hiq/internal/fileutil/encoding"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func readBytes(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Two turns edit a.txt and create b.txt; rewinding restores each file to its
// state at the start of the chosen turn (b.txt being deleted when it post-dates it).
func TestRestoreToStartOfTurn(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.txt")
	b := filepath.Join(root, "sub", "b.txt")
	write(t, a, "v0")
	s := New("", root)

	s.Begin(0, "first", 0)
	s.Snapshot(diff.Change{Path: a, Kind: diff.Modify, OldText: "v0"})
	write(t, a, "v1") // the edit turn 0 made

	s.Begin(1, "second", 2)
	s.Snapshot(diff.Change{Path: a, Kind: diff.Modify, OldText: "v1"})
	s.Snapshot(diff.Change{Path: b, Kind: diff.Create})
	write(t, a, "v2")
	write(t, b, "new")

	// Rewind to the start of turn 1: a back to v1, b gone.
	if _, _, err := s.RestoreCode(1); err != nil {
		t.Fatal(err)
	}
	if got := read(t, a); got != "v1" {
		t.Fatalf("a = %q, want v1", got)
	}
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Fatalf("b should have been deleted, stat err=%v", err)
	}
}

func TestRestoreToTurnZero(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.txt")
	write(t, a, "v0")
	s := New("", root)
	s.Begin(0, "first", 0)
	s.Snapshot(diff.Change{Path: a, Kind: diff.Modify, OldText: "v0"})
	write(t, a, "v1")
	s.Begin(1, "second", 2)
	s.Snapshot(diff.Change{Path: a, Kind: diff.Modify, OldText: "v1"})
	write(t, a, "v2")

	if _, _, err := s.RestoreCode(0); err != nil {
		t.Fatal(err)
	}
	if got := read(t, a); got != "v0" {
		t.Fatalf("a = %q, want v0 (earliest snapshot)", got)
	}
}

func TestRestorePreservesGB18030Encoding(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "gbk.txt")
	original := "\u4f60\u597d\n\u65e7\u884c\n"
	edited := "\u4f60\u597d\n\u65b0\u884c\n"
	originalRaw := fileenc.Encode(original, fileenc.GB18030)
	if err := os.WriteFile(a, originalRaw, 0o644); err != nil {
		t.Fatal(err)
	}

	s := New("", root)
	s.Begin(0, "edit gbk", 0)
	s.Snapshot(diff.Change{Path: a, Kind: diff.Modify, OldText: original})
	if err := os.WriteFile(a, fileenc.Encode(edited, fileenc.GB18030), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.RestoreCode(0); err != nil {
		t.Fatal(err)
	}
	gotRaw := readBytes(t, a)
	if utf8.Valid(gotRaw) {
		t.Fatalf("restored GB18030 file became valid UTF-8 bytes: % x", gotRaw)
	}
	if !bytes.Equal(gotRaw, originalRaw) {
		t.Fatalf("restored bytes = % x, want original GB18030 bytes % x", gotRaw, originalRaw)
	}
}

func TestRestorePreservesGB18030EncodingAfterPersistence(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(t.TempDir(), "sess.ckpt")
	a := filepath.Join(root, "gbk.txt")
	original := "\u4f60\u597d\n\u65e7\u884c\n"
	edited := "\u4f60\u597d\n\u65b0\u884c\n"
	originalRaw := fileenc.Encode(original, fileenc.GB18030)
	if err := os.WriteFile(a, originalRaw, 0o644); err != nil {
		t.Fatal(err)
	}

	s := New(dir, root)
	s.Begin(0, "edit gbk", 0)
	s.Snapshot(diff.Change{Path: a, Kind: diff.Modify, OldText: original})

	resumed := New(dir, root)
	if err := os.WriteFile(a, fileenc.Encode(edited, fileenc.GB18030), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resumed.RestoreCode(0); err != nil {
		t.Fatal(err)
	}
	if gotRaw := readBytes(t, a); !bytes.Equal(gotRaw, originalRaw) {
		t.Fatalf("restored bytes after persistence = % x, want original GB18030 bytes % x", gotRaw, originalRaw)
	}
}

func TestRestoreLegacySnapshotFallsBackToCurrentEncoding(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(t.TempDir(), "sess.ckpt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(root, "gbk.txt")
	original := "\u4f60\u597d\n\u65e7\u884c\n"
	edited := "\u4f60\u597d\n\u65b0\u884c\n"
	originalRaw := fileenc.Encode(original, fileenc.GB18030)
	if err := os.WriteFile(a, fileenc.Encode(edited, fileenc.GB18030), 0o644); err != nil {
		t.Fatal(err)
	}

	legacy := Checkpoint{
		Turn:     0,
		Time:     time.Now(),
		Prompt:   "legacy",
		MsgIndex: 0,
		Files: []FileSnap{{
			Path:    a,
			Content: &original,
		}},
	}
	b, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "turn-0.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	resumed := New(dir, root)
	if _, _, err := resumed.RestoreCode(0); err != nil {
		t.Fatal(err)
	}
	if gotRaw := readBytes(t, a); !bytes.Equal(gotRaw, originalRaw) {
		t.Fatalf("legacy restored bytes = % x, want original GB18030 bytes % x", gotRaw, originalRaw)
	}
}

func TestSnapshotDedupsFirstTouchWins(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.txt")
	write(t, a, "orig")
	s := New("", root)
	s.Begin(0, "p", 0)
	s.Snapshot(diff.Change{Path: a, Kind: diff.Modify, OldText: "orig"})
	s.Snapshot(diff.Change{Path: a, Kind: diff.Modify, OldText: "edited-once"}) // ignored
	write(t, a, "edited-twice")
	if _, _, err := s.RestoreCode(0); err != nil {
		t.Fatal(err)
	}
	if got := read(t, a); got != "orig" {
		t.Fatalf("a = %q, want orig (first snapshot wins)", got)
	}
}

func TestRestoreRejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "evil.txt")
	write(t, outside, "keep")
	s := New("", root)
	s.Begin(0, "p", 0)
	s.Snapshot(diff.Change{Path: outside, Kind: diff.Modify, OldText: "hacked"})
	if _, _, err := s.RestoreCode(0); err == nil {
		t.Fatal("RestoreCode should reject a path outside the workspace")
	}
	if got := read(t, outside); got != "keep" {
		t.Fatalf("outside file was modified: %q", got)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(t.TempDir(), "sess.ckpt")
	a := filepath.Join(root, "a.txt")

	s := New(dir, root)
	s.Begin(0, "hello", 1)
	s.Snapshot(diff.Change{Path: a, Kind: diff.Modify, OldText: "v0"})
	s.Begin(1, "world", 5)

	// A fresh store over the same dir must see both turns and their boundaries.
	s2 := New(dir, root)
	metas := s2.List()
	if len(metas) != 2 {
		t.Fatalf("loaded %d checkpoints, want 2", len(metas))
	}
	if metas[0].Prompt != "hello" || metas[1].Prompt != "world" {
		t.Fatalf("prompts = %q, %q", metas[0].Prompt, metas[1].Prompt)
	}
	// Boundaries must survive the round-trip so a resumed session can rewind/fork.
	b := s2.Bounds()
	if b[0] != 1 || b[1] != 5 {
		t.Fatalf("bounds = %v, want {0:1, 1:5}", b)
	}
	if s2.NextTurn() != 2 {
		t.Fatalf("NextTurn = %d, want 2", s2.NextTurn())
	}
}

func BenchmarkRestoreGB18030Encoding(b *testing.B) {
	root := b.TempDir()
	a := filepath.Join(root, "gbk.txt")
	original := strings.Repeat("\u4f60\u597d\u4e16\u754c\n\u65e7\u884c\n", 8192)
	edited := strings.Repeat("\u4f60\u597d\u4e16\u754c\n\u65b0\u884c\n", 8192)
	originalRaw := fileenc.Encode(original, fileenc.GB18030)
	editedRaw := fileenc.Encode(edited, fileenc.GB18030)
	if err := os.WriteFile(a, originalRaw, 0o644); err != nil {
		b.Fatal(err)
	}

	s := New("", root)
	s.Begin(0, "edit gbk", 0)
	s.Snapshot(diff.Change{Path: a, Kind: diff.Modify, OldText: original})

	b.SetBytes(int64(len(originalRaw)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := os.WriteFile(a, editedRaw, 0o644); err != nil {
			b.Fatal(err)
		}
		if _, _, err := s.RestoreCode(0); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLazyDirectoryCreation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(t.TempDir(), "lazy-sess.ckpt")

	s := New(dir, root)

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory should not exist yet: %v", err)
	}

	s.Begin(0, "lazy", 0)

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("directory should now exist: %v", err)
	}
	turnPath := filepath.Join(dir, "turn-0.json")
	if _, err := os.Stat(turnPath); err != nil {
		t.Fatalf("turn file should now exist: %v", err)
	}
}

// An open turn's paths are hidden from List (in-progress turns must not
// propagate CanCode); Finalize closes it so the newest event reports its paths —
// the netdev state history depends on this to show its latest event.
func TestFinalizeMakesCurrentTurnVisibleInList(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.txt")
	write(t, a, "v0")
	s := New("", root)

	s.Begin(0, "event", 0)
	s.Snapshot(diff.Change{Path: a, Kind: diff.Modify, OldText: "v0"})

	if metas := s.List(); len(metas) != 1 || metas[0].Paths != nil {
		t.Fatalf("open turn should hide paths, got %+v", metas)
	}

	s.Finalize()
	metas := s.List()
	if len(metas) != 1 || len(metas[0].Paths) != 1 || metas[0].Paths[0] != a {
		t.Fatalf("finalized turn should report paths, got %+v", metas)
	}

	// Finalize with nothing open is a no-op (event stores call it unconditionally).
	s.Finalize()
	if metas := s.List(); len(metas) != 1 {
		t.Fatalf("double Finalize changed the list: %+v", metas)
	}
}

// Event-oriented stores exceed the default cap quickly; pruning must run during
// Begin (not only at load), deleting the oldest turn files on disk.
func TestNewWithLimitPrunesDuringBegin(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(t.TempDir(), "state.ckpt")
	s := NewWithLimit(dir, root, 2)

	for turn := 0; turn < 4; turn++ {
		s.Begin(turn, fmt.Sprintf("e%d", turn), 0)
		s.Finalize()
	}

	metas := s.List()
	if len(metas) != 2 {
		t.Fatalf("retained %d checkpoints, want 2 (the cap)", len(metas))
	}
	for _, m := range metas {
		if m.Turn < 2 {
			t.Fatalf("pruned the wrong end: kept turn %d", m.Turn)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "turn-0.json")); !os.IsNotExist(err) {
		t.Fatalf("turn-0.json should have been deleted from disk: %v", err)
	}
	if s.NextTurn() != 4 {
		t.Fatalf("NextTurn = %d, want 4 (numbering must not regress after prune)", s.NextTurn())
	}
}

// Snapshot paths may be root-relative (the netdev state store); DiffForTurn and
// restore must resolve them against root regardless of the process CWD.
func TestRootRelativePathsWorkFromAnyCWD(t *testing.T) {
	root := t.TempDir()
	rel := filepath.Join("netdev", "proposals", "p-1.json")
	abs := filepath.Join(root, rel)
	write(t, abs, "v0")
	write(t, filepath.Join(root, "config.toml"), "keep")

	t.Chdir(t.TempDir()) // CWD is nowhere near root

	s := New("", root)
	s.Begin(0, "approve", 0)
	s.Snapshot(diff.Change{Path: rel, Kind: diff.Modify, OldText: "v0"})
	write(t, abs, "v1")

	changes := s.DiffForTurn(0)
	if len(changes) != 1 {
		t.Fatalf("DiffForTurn returned %d changes, want 1", len(changes))
	}
	if changes[0].Path != rel {
		t.Fatalf("diff path = %q, want %q", changes[0].Path, rel)
	}

	if _, _, err := s.RestoreCode(0); err != nil {
		t.Fatal(err)
	}
	if got := read(t, abs); got != "v0" {
		t.Fatalf("restored = %q, want v0", got)
	}
}

// NotePostEdit + SuffixInfo power the rewind safety classification: a path
// whose current content matches the agent's last post-edit hash is safe; an
// external edit after that flips it to unsafe; a never-hashed (legacy) path
// reports no provenance.
func TestSuffixInfoClassification(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.txt")
	b := filepath.Join(root, "b.txt")
	write(t, a, "v0")
	write(t, b, "b0")
	s := New("", root)

	s.Begin(0, "edit", 0)
	s.Snapshot(diff.Change{Path: a, Kind: diff.Modify, OldText: "v0"})
	s.Snapshot(diff.Change{Path: b, Kind: diff.Modify, OldText: "b0"})
	write(t, a, "v1")
	write(t, b, "b1")
	s.NotePostEdit(a) // agent's last write to a is v1; b stays unhashed (legacy)
	s.Finalize()

	// Untouched since the agent's last write → PostHash known; a external edit
	// after v1 must show as a mismatch.
	write(t, a, "v2-external")

	info := map[string]SuffixFileInfo{}
	for _, fi := range s.SuffixInfo(0) {
		info[fi.Path] = fi
	}
	if len(info) != 2 {
		t.Fatalf("suffix info = %v, want 2 paths", info)
	}
	if info[a].PostHash == "" || info[a].PostHash == info[a].PreHash {
		t.Fatalf("a post-hash = %q, want the v1 hash (≠ pre v0 hash)", info[a].PostHash)
	}
	if info[b].PostHash != "" || info[b].PreHash == "" {
		t.Fatalf("b should be legacy (pre-hash only): %+v", info[b])
	}
	if cur := s.CurrentHash(a); cur == "" || cur == info[a].PostHash {
		t.Fatalf("current hash of a = %q, want the external v2 (≠ post-hash)", cur)
	}
	// b was edited (b0→b1) but never post-hash noted: legacy provenance — its
	// current hash is simply the on-disk b1 content, which no recorded hash
	// matches, so the preview classifies it "ignored".
	if cur := s.CurrentHash(b); cur != hashString("b1") {
		t.Fatalf("current hash of b = %q, want hash of b1", cur)
	}
}

// KeepCurrent is the reverse side of a rewind: current values become a
// finalized synthetic checkpoint whose restore replays the rewound edits,
// and a path absent at keep time replays as a delete.
func TestKeepCurrentReplaysRewind(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.txt")
	nb := filepath.Join(root, "new.txt")
	write(t, a, "v0")
	s := New("", root)

	s.Begin(0, "edit", 0)
	s.Snapshot(diff.Change{Path: a, Kind: diff.Modify, OldText: "v0"})
	s.Snapshot(diff.Change{Path: nb, Kind: diff.Create}) // pre-edit hook saw it absent
	write(t, a, "v1")
	write(t, nb, "created-during-turn")

	keep := s.KeepCurrent("rewind-keep → 0", 1, []string{a, nb})
	if keep < 0 {
		t.Fatal("KeepCurrent returned no turn")
	}

	if _, _, err := s.RestoreCode(0); err != nil {
		t.Fatal(err)
	}
	if got := read(t, a); got != "v0" {
		t.Fatalf("after rewind a = %q, want v0", got)
	}
	if _, err := os.Stat(nb); !os.IsNotExist(err) {
		t.Fatalf("rewind should delete the created file: %v", err)
	}

	// Reapply = restore back to the keep turn.
	if _, _, err := s.RestoreCode(keep); err != nil {
		t.Fatal(err)
	}
	if got := read(t, a); got != "v1" {
		t.Fatalf("after reapply a = %q, want v1", got)
	}
	if got := read(t, nb); got != "created-during-turn" {
		t.Fatalf("after reapply new file = %q, want it back", got)
	}

	// The keep turn shows its paths (finalized) and carries post-hashes.
	metas := s.List()
	last := metas[len(metas)-1]
	if last.Turn != keep || len(last.Paths) != 2 {
		t.Fatalf("keep meta = %+v, want turn %d with 2 paths", last, keep)
	}
}
