package memory

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevelArg(t *testing.T) {
	cases := []struct {
		in       string
		want     Level
		explicit bool
	}{
		{"", LevelGlobal, false},
		{"l1", LevelGlobal, true},
		{"L2", LevelProject, true},
		{"l3", LevelSession, true},
		{"1", LevelGlobal, true},
		{"project", LevelProject, true},
		{"session", LevelSession, true},
		{"task", LevelSession, true},
		{"nonsense", LevelGlobal, true},
	}
	for _, tc := range cases {
		got, explicit := ParseLevelArg(tc.in)
		if got != tc.want || explicit != tc.explicit {
			t.Errorf("ParseLevelArg(%q) = (%q,%v), want (%q,%v)", tc.in, got, explicit, tc.want, tc.explicit)
		}
	}
}

// A memory tree written before the level hierarchy existed must keep its reach:
// the legacy `project: true` flag meant L2, everything else was shared.
func TestLevelOfInfersLegacyRecords(t *testing.T) {
	if got := LevelOf(Memory{Profile: "project"}); got != LevelProject {
		t.Errorf("legacy project profile should infer L2, got %q", got)
	}
	if got := LevelOf(Memory{Profile: "global"}); got != LevelGlobal {
		t.Errorf("legacy global profile should infer L1, got %q", got)
	}
	if got := LevelOf(Memory{Level: LevelSession, Profile: "project"}); got != LevelSession {
		t.Errorf("an explicit level must win over the legacy inference, got %q", got)
	}
}

func TestSaveRoutesByLevel(t *testing.T) {
	root := t.TempDir()
	store := StoreFor(root, filepath.Join(root, "proj"), "dev")
	if store.SessionDir == "" || store.SessionDir == store.Dir {
		t.Fatalf("session bucket must be a distinct subdirectory: %+v", store)
	}

	saved := map[Level]string{}
	for _, in := range []struct {
		level Level
		name  string
	}{
		{LevelGlobal, "user-role"},
		{LevelProject, "build-flags"},
		{LevelSession, "current-task"},
	} {
		p, err := store.Save(Memory{Name: in.name, Body: "body of " + in.name, Level: in.level, Tags: []string{"Alpha", " alpha ", "beta"}})
		if err != nil {
			t.Fatalf("Save(%s): %v", in.level, err)
		}
		saved[in.level] = p
	}
	if dir := filepath.Dir(saved[LevelGlobal]); dir != store.GlobalDir {
		t.Errorf("L1 landed in %q, want the global bucket %q", dir, store.GlobalDir)
	}
	if dir := filepath.Dir(saved[LevelProject]); dir != store.Dir {
		t.Errorf("L2 landed in %q, want the project bucket %q", dir, store.Dir)
	}
	if dir := filepath.Dir(saved[LevelSession]); dir != store.SessionDir {
		t.Errorf("L3 landed in %q, want the session bucket %q", dir, store.SessionDir)
	}

	// Tags are normalized (lower-cased, de-duplicated) on the way to disk.
	m, ok := loadMemory(saved[LevelSession])
	if !ok {
		t.Fatal("L3 memory must be readable")
	}
	if got := strings.Join(m.Tags, ","); got != "alpha,beta" {
		t.Errorf("tags not normalized: %q", got)
	}
	if LevelOf(m) != LevelSession {
		t.Errorf("level did not round-trip: %q", m.Level)
	}

	// List (and therefore recall) sees every level; the prompt index does not.
	if got := len(store.List()); got != 3 {
		t.Errorf("List should return all 3 levels, got %d", got)
	}
	idx := store.PromptIndex(0)
	if strings.Contains(idx, "current-task") {
		t.Errorf("L3 working memory must stay out of the injected index:\n%s", idx)
	}
	for _, want := range []string{"[L1/global] user-role", "[L2/project] build-flags", "#alpha"} {
		if !strings.Contains(idx, want) {
			t.Errorf("index is missing %q:\n%s", want, idx)
		}
	}
}

func TestPromptIndexTruncatesAtLineBoundary(t *testing.T) {
	root := t.TempDir()
	store := StoreFor(root, filepath.Join(root, "proj"), "dev")
	for i := 0; i < 12; i++ {
		name := "fact-" + strings.Repeat("x", 1) + string(rune('a'+i))
		if _, err := store.Save(Memory{Name: name, Body: "a reasonably long body line for " + name, Level: LevelGlobal}); err != nil {
			t.Fatal(err)
		}
	}
	full := store.PromptIndex(0)
	if strings.Contains(full, "truncated") {
		t.Fatalf("an unbounded index must not truncate: %q", full)
	}
	capped := store.PromptIndex(120)
	if !strings.Contains(capped, "truncated") {
		t.Fatalf("a small cap must truncate with a marker: %q", capped)
	}
	if len(capped) > 200 {
		t.Errorf("truncated index grew instead of shrinking: %d chars", len(capped))
	}
	for _, line := range strings.Split(capped, "\n") {
		if strings.HasPrefix(line, "- ") && !strings.Contains(line, "] ") {
			t.Errorf("truncation cut a line in half: %q", line)
		}
	}
}

func TestRecallFiltersByLevelTagAndQuery(t *testing.T) {
	root := t.TempDir()
	store := StoreFor(root, filepath.Join(root, "proj"), "dev")
	must := func(m Memory) {
		t.Helper()
		if _, err := store.Save(m); err != nil {
			t.Fatal(err)
		}
	}
	must(Memory{Name: "prefers-tabs", Body: "Indent with spaces.", Level: LevelGlobal, Tags: []string{"style"}})
	must(Memory{Name: "build-flags", Body: "Always pass -race.", Level: LevelProject, Tags: []string{"testing"}})
	must(Memory{Name: "scratch-note", Body: "Half-finished idea.", Level: LevelSession})

	tl := NewRecallTool(store)
	if !json.Valid(tl.Schema()) {
		t.Fatal("recall schema is not valid JSON")
	}
	run := func(args string) string {
		t.Helper()
		out, err := tl.Execute(context.Background(), []byte(args))
		if err != nil {
			t.Fatalf("recall %s: %v", args, err)
		}
		return out
	}

	all := run(`{}`)
	for _, want := range []string{"prefers-tabs", "build-flags", "scratch-note"} {
		if !strings.Contains(all, want) {
			t.Errorf("unfiltered recall should list %q:\n%s", want, all)
		}
	}
	if got := run(`{"level":"l2"}`); !strings.Contains(got, "build-flags") || strings.Contains(got, "prefers-tabs") {
		t.Errorf("level filter is wrong:\n%s", got)
	}
	if got := run(`{"tag":"style"}`); !strings.Contains(got, "prefers-tabs") || strings.Contains(got, "build-flags") {
		t.Errorf("tag filter is wrong:\n%s", got)
	}
	if got := run(`{"query":"race"}`); !strings.Contains(got, "build-flags") || strings.Contains(got, "prefers-tabs") {
		t.Errorf("query filter is wrong:\n%s", got)
	}
	if got := run(`{"query":"no-such-thing"}`); !strings.Contains(got, "No saved memory matched") {
		t.Errorf("an empty match should say so:\n%s", got)
	}
	// A body read reports the level so the model knows how far the fact reaches.
	if got := run(`{"name":"scratch-note"}`); !strings.Contains(got, "[L3/session]") {
		t.Errorf("single-fact read should label the level:\n%s", got)
	}
}

func TestRememberToolLevelAndTags(t *testing.T) {
	root := t.TempDir()
	store := StoreFor(root, filepath.Join(root, "proj"), "dev")
	tl := NewRememberTool(store)

	out, err := tl.Execute(context.Background(), []byte(`{"name":"proj-conv","body":"Tests must be table-driven.","level":"l2","tags":"go, testing"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "L2/project") {
		t.Fatalf("tool output should name the level: %q", out)
	}
	m, ok := loadMemory(store.Path("proj-conv"))
	if !ok {
		t.Fatal("fact was not written")
	}
	if LevelOf(m) != LevelProject || filepath.Dir(store.Path("proj-conv")) != store.Dir {
		t.Fatalf("explicit l2 did not route to the project bucket: %+v", m)
	}
	if strings.Join(m.Tags, ",") != "go,testing" {
		t.Fatalf("tags not persisted: %v", m.Tags)
	}

	// The legacy project flag still means L2 for callers that never learned levels.
	if _, err := tl.Execute(context.Background(), []byte(`{"name":"legacy-proj","body":"x","project":true}`)); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(store.Path("legacy-proj")) != store.Dir {
		t.Error("legacy project:true must still land in the project bucket")
	}
}

func TestBlockInjectsLevelScopedIndex(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "proj")
	store := StoreFor(root, cwd, "dev")
	if _, err := store.Save(Memory{Name: "user-role", Body: "Backend engineer.", Level: LevelGlobal, Tags: []string{"identity"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(Memory{Name: "task-scratch", Body: "temporary", Level: LevelSession}); err != nil {
		t.Fatal(err)
	}

	set := Load(Options{CWD: cwd, UserDir: root, Profile: "dev", InjectIndex: true})
	block := set.Block()
	if !strings.Contains(block, "已保存的记忆") || !strings.Contains(block, "[L1/global] user-role") {
		t.Fatalf("block must carry the fact index:\n%s", block)
	}
	if strings.Contains(block, "task-scratch") {
		t.Fatalf("L3 must not be injected:\n%s", block)
	}
	// The index is byte-stable for the same files — the cache-prefix invariant.
	if again := Load(Options{CWD: cwd, UserDir: root, Profile: "dev", InjectIndex: true}).Block(); again != block {
		t.Fatal("memory block must be deterministic for unchanged files")
	}

	// Opting out removes the index without touching the rest of the block.
	off := Load(Options{CWD: cwd, UserDir: root, Profile: "dev", InjectIndex: false})
	if strings.Contains(off.Block(), "user-role") {
		t.Fatalf("inject_index=false must drop the index:\n%s", off.Block())
	}
	if off.Empty() && !set.Empty() {
		t.Fatal("a set with an injected index is not empty")
	}
}

func TestPromptIndexCapsByDefault(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "proj")
	store := StoreFor(root, cwd, "dev")
	for i := 0; i < 200; i++ {
		name := "fact-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if _, err := store.Save(Memory{Name: name, Body: strings.Repeat("detail ", 20), Level: LevelGlobal}); err != nil {
			t.Fatal(err)
		}
	}
	set := Load(Options{CWD: cwd, UserDir: root, Profile: "dev", InjectIndex: true})
	if got := len(set.PromptIndex); got > DefaultPromptIndexMaxChars+120 {
		t.Fatalf("default cap not applied: %d chars", got)
	}
}
