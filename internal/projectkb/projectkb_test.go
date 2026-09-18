package projectkb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/memory"
	"github.com/zzycxz/hiq/internal/taskmonitor"
	"github.com/zzycxz/hiq/internal/team"
)

// workDir returns a scratch directory INSIDE the project tree. The project's
// disk rule forbids new data on C:, and t.TempDir() would land under %TEMP%
// (C: on this host), so cases get their own folder here instead.
func workDir(t *testing.T) string {
	t.Helper()
	base := filepath.Join("..", "..", ".cache", "projectkb-test")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	d, err := os.MkdirTemp(base, "case-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// seedRepo builds a miniature project with one file per source family.
func seedRepo(t *testing.T, repo, dataRoot string) {
	t.Helper()
	write(t, filepath.Join(repo, "go.mod"), "module example.com/demo\n\ngo 1.22\n")
	write(t, filepath.Join(repo, "internal", "alpha", "alpha.go"),
		"// Package alpha does the alpha thing.\npackage alpha\n")
	write(t, filepath.Join(repo, "docs", "guide.md"),
		"# 接入指南\n\n先跑 make build，再跑 make test。\n")
	write(t, filepath.Join(repo, "README.md"),
		"# Demo\n\n一个演示仓库。\n")

	store := memory.StoreFor(dataRoot, repo, "dev")
	if _, err := store.Save(memory.Memory{
		Name:  "data-root",
		Body:  "记忆数据根固定在 D 盘，不要落 C 盘。",
		Level: memory.LevelProject,
	}); err != nil {
		t.Fatalf("memory save: %v", err)
	}

	ts, err := team.NewStore(filepath.Join(dataRoot, "teams", "team_projects.json"))
	if err != nil {
		t.Fatalf("team store: %v", err)
	}
	tm, err := ts.Create(team.Team{Name: "演示团队", Goal: "把接口和测试做完"})
	if err != nil {
		t.Fatalf("team create: %v", err)
	}
	if _, err := ts.AddMember(tm.ID, team.Member{Name: "张三", IsLeader: true, Skills: []string{"go"}}); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if _, err := ts.AddTask(tm.ID, team.Task{
		Title: "写接口", Status: taskmonitor.TaskStateRunning,
	}); err != nil {
		t.Fatalf("add task: %v", err)
	}
	if _, err := ts.AddTask(tm.ID, team.Task{
		Title: "写测试", Status: taskmonitor.TaskStateFailed, Error: "编译不过",
	}); err != nil {
		t.Fatalf("add task: %v", err)
	}
}

func TestSyncCollectsEverySource(t *testing.T) {
	repo := workDir(t)
	root := workDir(t)
	seedRepo(t, repo, root)

	h, err := New(root, repo, "dev")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rep, err := h.Sync("test")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if rep.Added == 0 {
		t.Fatalf("expected additions, got %+v", rep)
	}

	counts := Counts(h.Nodes())
	if counts[string(KindCode)] < 2 {
		t.Errorf("code nodes = %d, want >= 2 (module + package)", counts[string(KindCode)])
	}
	if counts[string(KindDoc)] < 2 {
		t.Errorf("doc nodes = %d, want >= 2", counts[string(KindDoc)])
	}
	if counts[string(KindMemory)] != 1 {
		t.Errorf("memory nodes = %d, want 1", counts[string(KindMemory)])
	}
	// Team project + its two cards.
	if counts[string(KindTeam)] != 3 {
		t.Errorf("team nodes = %d, want 3", counts[string(KindTeam)])
	}

	m := h.Map()
	for _, want := range []string{"模块与包", "项目文档", "记忆（约定与决策）", "团队与进度", "接入指南"} {
		if !strings.Contains(m, want) {
			t.Errorf("map missing %q:\n%s", want, m)
		}
	}
	// The package doc line must carry through to the map (that is the mapping).
	if !strings.Contains(m, "does the alpha thing") {
		t.Errorf("map lost the package doc line:\n%s", m)
	}
}

func TestSyncIsIncrementalAndOnlyRevisionsOnChange(t *testing.T) {
	repo := workDir(t)
	root := workDir(t)
	seedRepo(t, repo, root)

	h, _ := New(root, repo, "dev")
	first, err := h.Sync("first")
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if first.Revision == "" {
		t.Fatalf("first sync should have written a revision")
	}

	second, err := h.Sync("second")
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if second.Added != 0 || second.Changed != 0 || second.Removed != 0 {
		t.Errorf("idempotent sync reported movement: %+v", second)
	}
	if second.Unchanged != second.Total || second.Total == 0 {
		t.Errorf("unchanged = %d, total = %d", second.Unchanged, second.Total)
	}
	if second.Revision != "" {
		t.Errorf("an unchanged sync must not write a revision, got %q", second.Revision)
	}
	if got := len(h.History()); got != 1 {
		t.Errorf("history = %d, want 1", got)
	}

	write(t, filepath.Join(repo, "docs", "guide.md"),
		"# 接入指南\n\n先跑 make build，再跑 make test，最后 make release。\n")
	third, err := h.Sync("doc changed")
	if err != nil {
		t.Fatalf("third sync: %v", err)
	}
	if third.Changed == 0 {
		t.Errorf("a changed doc must register as changed: %+v", third)
	}
	if third.Revision == "" {
		t.Errorf("a changed map must write a revision")
	}
	if got := len(h.History()); got != 2 {
		t.Errorf("history = %d, want 2", got)
	}
}

func TestRollbackRestoresPreviousMap(t *testing.T) {
	repo := workDir(t)
	root := workDir(t)
	seedRepo(t, repo, root)

	h, _ := New(root, repo, "dev")
	if _, err := h.Sync("v1"); err != nil {
		t.Fatalf("sync v1: %v", err)
	}
	hist := h.History()
	if len(hist) != 1 {
		t.Fatalf("history = %d, want 1", len(hist))
	}
	v1 := hist[0].ID
	before := h.Map()

	write(t, filepath.Join(repo, "docs", "extra.md"), "# 新增文档\n\n后来才加的。\n")
	if _, err := h.Sync("v2"); err != nil {
		t.Fatalf("sync v2: %v", err)
	}
	if !strings.Contains(h.Map(), "新增文档") {
		t.Fatalf("v2 map should contain the new doc")
	}

	if err := h.Rollback(v1); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if strings.Contains(h.Map(), "新增文档") {
		t.Errorf("rollback did not remove the later doc:\n%s", h.Map())
	}
	if h.Map() != before {
		t.Errorf("rollback map differs from v1 map")
	}
	// The rollback is itself recorded, so it can be undone.
	if got := len(h.History()); got != 3 {
		t.Errorf("history after rollback = %d, want 3", got)
	}
}

func TestRollbackRejectsUnsafeID(t *testing.T) {
	repo := workDir(t)
	root := workDir(t)
	seedRepo(t, repo, root)
	h, _ := New(root, repo, "dev")
	if _, err := h.Sync("test"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := h.Rollback("../../etc/passwd"); err == nil {
		t.Errorf("an unsafe revision id must be rejected")
	}
}

func TestSearchRanksTitleAndRequiresAllTokens(t *testing.T) {
	repo := workDir(t)
	root := workDir(t)
	seedRepo(t, repo, root)
	h, _ := New(root, repo, "dev")
	if _, err := h.Sync("test"); err != nil {
		t.Fatalf("sync: %v", err)
	}

	hits := h.Search("alpha", 5)
	if len(hits) == 0 {
		t.Fatalf("expected a hit for 'alpha'")
	}
	if !strings.Contains(hits[0].Node.Title, "alpha") {
		t.Errorf("top hit = %q, want the alpha package", hits[0].Node.Title)
	}

	// Two tokens must both match: a query combining a real term with a bogus
	// one returns nothing rather than everything.
	if got := h.Search("alpha zzzznotpresent", 5); len(got) != 0 {
		t.Errorf("AND semantics violated: got %d hits", len(got))
	}
	if got := h.Search("", 3); len(got) != 3 {
		t.Errorf("empty query should browse %d nodes, got %d", 3, len(got))
	}
}

func TestIndexIsBoundedAndPointsAtTools(t *testing.T) {
	repo := workDir(t)
	root := workDir(t)
	seedRepo(t, repo, root)
	h, _ := New(root, repo, "dev")
	if _, err := h.Sync("test"); err != nil {
		t.Fatalf("sync: %v", err)
	}

	idx := h.Index(0)
	if !strings.Contains(idx, "kb_map") || !strings.Contains(idx, "kb_search") {
		t.Errorf("index should advertise the tools:\n%s", idx)
	}
	if !strings.Contains(idx, "规模：") {
		t.Errorf("index should carry the size line:\n%s", idx)
	}
	if !strings.Contains(idx, "待关注：") {
		t.Errorf("index should surface failing cards:\n%s", idx)
	}

	tiny := h.Index(80)
	if len([]rune(tiny)) > 80+len([]rune("… (index truncated; call kb_map for the full map)")) {
		t.Errorf("index ignored its budget: %d runes", len([]rune(tiny)))
	}
	if !strings.Contains(tiny, "truncated") {
		t.Errorf("a clipped index must say so:\n%s", tiny)
	}
}

func TestLoadIndexReadsPersistedStateWithoutScanning(t *testing.T) {
	repo := workDir(t)
	root := workDir(t)
	h, _ := New(root, repo, "dev")
	if idx := LoadIndex(root, repo, "dev", 0); idx != "" {
		t.Errorf("unsynced hub should inject nothing, got %q", idx)
	}

	seedRepo(t, repo, root)
	if _, err := h.Sync("test"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	idx := LoadIndex(root, repo, "dev", 0)
	if !strings.Contains(idx, "项目知识地图") {
		t.Errorf("LoadIndex should read the persisted map:\n%s", idx)
	}
	// A different working directory has its own (empty) hub.
	if other := LoadIndex(root, filepath.Join(repo, "elsewhere"), "dev", 0); other != "" {
		t.Errorf("a different cwd must not inherit another project's map: %q", other)
	}
}

func TestSetSourceDisablesACollector(t *testing.T) {
	repo := workDir(t)
	root := workDir(t)
	seedRepo(t, repo, root)
	h, _ := New(root, repo, "dev")
	if _, err := h.Sync("test"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := h.SetSource(KindDoc, false); err != nil {
		t.Fatalf("SetSource: %v", err)
	}
	rep, err := h.Sync("docs off")
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := Counts(h.Nodes())[string(KindDoc)]; got != 0 {
		t.Errorf("doc nodes = %d after disabling the source", got)
	}
	if rep.Removed == 0 {
		t.Errorf("disabling a source should report removals")
	}
	// The switch survives a reload.
	h2, err := New(root, repo, "dev")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	for _, s := range h2.Sources() {
		if s.Kind == KindDoc && s.Enabled {
			t.Errorf("the disabled switch did not persist")
		}
	}
}

func TestTokenizeSplitsPathsAndKeepsCJK(t *testing.T) {
	got := tokenize("internal/team 记忆")
	want := map[string]bool{"internal": true, "team": true, "记忆": true}
	if len(got) != len(want) {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
	for _, tk := range got {
		if !want[tk] {
			t.Errorf("unexpected token %q in %v", tk, got)
		}
	}
	if got := tokenize("a x"); len(got) != 0 {
		t.Errorf("single ASCII letters should be dropped, got %v", got)
	}
}
