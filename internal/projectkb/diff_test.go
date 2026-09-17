package projectkb

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiffCapturesAddedChangedRemoved(t *testing.T) {
	prev := map[string]Node{
		"code:pkg/a":    {ID: "code:pkg/a", Kind: KindCode, Title: "a", FP: "f1"},
		"doc:docs/x.md": {ID: "doc:docs/x.md", Kind: KindDoc, Title: "x", FP: "f1", Summary: "旧描述"},
		"doc:docs/gone.md": {
			ID: "doc:docs/gone.md", Kind: KindDoc, Title: "gone", FP: "f1",
		},
	}
	next := []Node{
		{ID: "code:pkg/a", Kind: KindCode, Title: "a", FP: "f1"},                         // unchanged
		{ID: "doc:docs/x.md", Kind: KindDoc, Title: "x", FP: "f2", Summary: "新描述"},       // changed
		{ID: "doc:docs/new.md", Kind: KindDoc, Title: "new", FP: "f1", Summary: "刚加的文档"}, // added
	}
	d := buildDiff(prev, next)
	if len(d.Added) != 1 || d.Added[0].ID != "doc:docs/new.md" {
		t.Fatalf("added = %+v", d.Added)
	}
	if len(d.Changed) != 1 || d.Changed[0].ID != "doc:docs/x.md" {
		t.Fatalf("changed = %+v", d.Changed)
	}
	if len(d.Removed) != 1 || d.Removed[0].ID != "doc:docs/gone.md" {
		t.Fatalf("removed = %+v", d.Removed)
	}
	if d.Count() != 3 {
		t.Errorf("count = %d, want 3", d.Count())
	}
	// A changed node must carry its before → after gist.
	if d.Changed[0].Before != "旧描述" || d.Changed[0].After != "新描述" {
		t.Errorf("changed gist = %q → %q", d.Changed[0].Before, d.Changed[0].After)
	}
	if d.IsEmpty() {
		t.Error("a non-empty diff must not report empty")
	}
}

func TestDiffLineAndKinds(t *testing.T) {
	d := Diff{
		Added:   []Change{{Kind: KindDoc, ID: "doc:a"}},
		Changed: []Change{{Kind: KindCode, ID: "code:b"}, {Kind: KindCode, ID: "code:c"}},
	}
	if got := d.Line(); !strings.Contains(got, "新增 1") || !strings.Contains(got, "变更 2") {
		t.Errorf("line = %q", got)
	}
	if got := d.Kinds(); got[string(KindCode)] != 2 || got[string(KindDoc)] != 1 {
		t.Errorf("kinds = %+v", got)
	}
	if got := (Diff{}).Line(); got != "无变化" {
		t.Errorf("empty line = %q", got)
	}
	if got := (Diff{}).Markdown(0); got != "" {
		t.Errorf("empty diff must render nothing, got %q", got)
	}
}

func TestDiffMarkdownIsBounded(t *testing.T) {
	var d Diff
	for i := 0; i < 50; i++ {
		d.Added = append(d.Added, Change{
			Op: OpAdded, Kind: KindDoc, ID: "doc:" + strings.Repeat("x", i+1),
			Title: "文档", After: "新增内容",
		})
	}
	got := d.Markdown(5)
	if strings.Count(got, "- [") > 5 {
		t.Errorf("markdown should honour maxItems=5:\n%s", got)
	}
	if !strings.Contains(got, "其余 45 项") {
		t.Errorf("truncation should be declared:\n%s", got)
	}
}

func TestSyncReportCarriesSummaryAndLastDiff(t *testing.T) {
	repo := workDir(t)
	root := workDir(t)
	seedRepo(t, repo, root)

	h, err := New(root, repo, "dev")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	first, err := h.Sync("test")
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if first.Summary == "" || !strings.Contains(first.Summary, "本次变更") {
		t.Fatalf("a first sync must render a summary, got %q", first.Summary)
	}
	if len(first.Diff.Added) == 0 {
		t.Fatalf("first sync should report additions: %+v", first.Diff)
	}

	// A no-op sync moves nothing: no summary, no revision, and the persisted
	// "last change" record must survive so the reader still sees what happened.
	second, err := h.Sync("noop")
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if second.Summary != "" || second.Revision != "" {
		t.Fatalf("a no-op sync must stay quiet: %+v", second)
	}
	if h.State().LastChangeLine() == "" {
		t.Fatal("the last-change gist must survive a no-op sync")
	}

	// Now actually change something.
	write(t, filepath.Join(repo, "docs", "guide.md"), "# 接入指南\n\n先跑 make build，再跑 make test。\n（新增了一节）\n")
	third, err := h.Sync("test")
	if err != nil {
		t.Fatalf("third sync: %v", err)
	}
	if third.Changed == 0 {
		t.Fatalf("editing a doc should register a change: %+v", third)
	}
	if !strings.Contains(third.Summary, "变更") {
		t.Errorf("summary should describe the change:\n%s", third.Summary)
	}
	if !strings.Contains(h.State().LastChangeLine(), "变更") {
		t.Errorf("persisted gist = %q", h.State().LastChangeLine())
	}
}

func TestRevisionSummaryIsStoredPerSnapshot(t *testing.T) {
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
	if rep.Revision == "" {
		t.Fatal("expected a revision for a map-moving sync")
	}
	got, err := h.RevisionSummary(rep.Revision)
	if err != nil {
		t.Fatalf("RevisionSummary: %v", err)
	}
	if !strings.Contains(got, "本次变更") {
		t.Fatalf("revision summary = %q", got)
	}
	revs := h.History()
	if len(revs) == 0 || revs[0].Added != rep.Added {
		t.Fatalf("revision counts should mirror the report: %+v vs %+v", revs, rep)
	}
	if _, err := h.RevisionSummary("../../etc/passwd"); err == nil {
		t.Fatal("an unsafe revision ID must be rejected")
	}
}

func TestPromptIndexMentionsTheLastChange(t *testing.T) {
	repo := workDir(t)
	root := workDir(t)
	seedRepo(t, repo, root)

	h, err := New(root, repo, "dev")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := h.Sync("test"); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	idx := h.Index(DefaultIndexMaxChars)
	if !strings.Contains(idx, "上次变更") {
		t.Fatalf("the index should carry the incremental gist:\n%s", idx)
	}
	// The index is still bounded after adding a line.
	if n := len([]rune(idx)); n > DefaultIndexMaxChars {
		t.Fatalf("index grew past its budget: %d > %d", n, DefaultIndexMaxChars)
	}
}

func TestRollbackRecordsWhatItUndid(t *testing.T) {
	repo := workDir(t)
	root := workDir(t)
	seedRepo(t, repo, root)

	h, err := New(root, repo, "dev")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	base, err := h.Sync("test")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	write(t, filepath.Join(repo, "docs", "extra.md"), "# 额外文档\n\n后来加的。\n")
	if _, err := h.Sync("test"); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := h.Rollback(base.Revision); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	idx := h.Index(DefaultIndexMaxChars)
	if !strings.Contains(idx, "上次变更") {
		t.Fatalf("rollback should leave a change record:\n%s", idx)
	}
	// The rollback snapshot itself must explain itself.
	revs := h.History()
	if len(revs) == 0 || revs[0].Trigger != "rollback" {
		t.Fatalf("history head should be the rollback: %+v", revs)
	}
	got, err := h.RevisionSummary(revs[0].ID)
	if err != nil {
		t.Fatalf("RevisionSummary: %v", err)
	}
	if !strings.Contains(got, "移除") {
		t.Errorf("rolling back an added doc should report a removal:\n%s", got)
	}
}

// --- watcher ---------------------------------------------------------------

func TestScanFingerprintNoticesContentAndAppearance(t *testing.T) {
	repo := workDir(t)
	write(t, filepath.Join(repo, "README.md"), "# Demo\n")
	before := scanFingerprint([]string{repo}, 100)

	write(t, filepath.Join(repo, "docs", "new.md"), "# New\n")
	added := scanFingerprint([]string{repo}, 100)
	if added == before {
		t.Fatal("a new Markdown file must move the fingerprint")
	}

	// An irrelevant file must NOT move it (the scan stays cheap and quiet).
	write(t, filepath.Join(repo, "logo.png"), "not really a png")
	if got := scanFingerprint([]string{repo}, 100); got != added {
		t.Fatal("an unrelated file type must not move the fingerprint")
	}
}

func TestWatchSyncsOnChange(t *testing.T) {
	repo := workDir(t)
	root := workDir(t)
	seedRepo(t, repo, root)

	h, err := New(root, repo, "dev")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := h.Sync("test"); err != nil {
		t.Fatalf("initial Sync: %v", err)
	}

	reports := make(chan Report, 4)
	w := h.Watch(WatchOptions{
		Interval: 40 * time.Millisecond,
		Debounce: 60 * time.Millisecond,
		Roots:    []string{repo},
	}, func(rep Report) { reports <- rep })

	w.Start()
	if !w.Running() {
		t.Fatal("watcher should be running after Start")
	}
	defer w.Stop()

	// Simulate a dev session editing a doc.
	write(t, filepath.Join(repo, "docs", "guide.md"),
		"# 接入指南\n\n先跑 make build，再跑 make test。\n（文件监听应触发同步）\n")

	select {
	case rep := <-reports:
		if rep.Changed == 0 {
			t.Fatalf("auto-sync should have seen the doc change: %+v", rep)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for the watcher to auto-sync")
	}

	if running, syncs, _ := w.Stats(); !running || syncs == 0 {
		t.Fatalf("stats = running:%v syncs:%d", running, syncs)
	}
	w.Stop() // idempotent
	if w.Running() {
		t.Fatal("watcher should be stopped")
	}
}

func TestWatchDebouncesABurstIntoOneSync(t *testing.T) {
	repo := workDir(t)
	root := workDir(t)
	seedRepo(t, repo, root)

	h, err := New(root, repo, "dev")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := h.Sync("test"); err != nil {
		t.Fatalf("initial Sync: %v", err)
	}

	var n int
	w := h.Watch(WatchOptions{
		Interval: 30 * time.Millisecond,
		Debounce: 150 * time.Millisecond,
		Roots:    []string{repo},
	}, func(Report) { n++ })
	w.Start()
	defer w.Stop()

	// Five saves in quick succession = one logical edit.
	for i := 0; i < 5; i++ {
		write(t, filepath.Join(repo, "docs", "guide.md"),
			"# 接入指南\n\n改动 "+strings.Repeat("x", i+1)+"\n")
		time.Sleep(10 * time.Millisecond)
	}
	// Give the debounce window time to elapse, then check the sync count.
	time.Sleep(600 * time.Millisecond)
	_, syncs, _ := w.Stats()
	if syncs == 0 {
		t.Fatal("the burst should have produced at least one sync")
	}
	if syncs > 2 {
		t.Fatalf("a debounced burst should coalesce, got %d syncs", syncs)
	}
}

func TestWatchSkipDirCoversTheHubOwnOutput(t *testing.T) {
	for _, name := range []string{".git", "node_modules", "dist", subdir, ".cache"} {
		if !watchSkipDir(name) {
			t.Errorf("%q should be skipped", name)
		}
	}
	if watchSkipDir("internal") {
		t.Error("a real source directory must be watched")
	}
}
