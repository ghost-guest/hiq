package patrol

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zzycxz/fairpeer/internal/deferred"
)

// --- helpers ----------------------------------------------------------------

type fakeDeliverer struct {
	mu    sync.Mutex
	tasks []deferred.Task
	paths []string
	err   error
}

func (f *fakeDeliverer) Enqueue(sessionPath string, t deferred.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.tasks = append(f.tasks, t)
	f.paths = append(f.paths, sessionPath)
	return nil
}

func (f *fakeDeliverer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tasks)
}

func (f *fakeDeliverer) last() deferred.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tasks[len(f.tasks)-1]
}

// scriptedInspector returns predefined results, one per Inspect call, repeating
// the final one forever. It lets a test write "first tick sees X, later ticks
// see Y" without a real filesystem.
type scriptedInspector struct {
	name    string
	results []Result
	calls   int
}

func (s *scriptedInspector) Name() string { return s.name }

func (s *scriptedInspector) Inspect(_ context.Context, _ Target, _ string) Result {
	r := s.results[min(s.calls, len(s.results)-1)]
	s.calls++
	return r
}

func oneTarget(t Target) func() []Target { return func() []Target { return []Target{t} } }

func warnFinding(fp string) Finding {
	return Finding{ID: "x.warn", Kind: "x", Severity: SevWarn, Summary: "something changed", Fingerprint: fp}
}

func infoFinding(fp string) Finding {
	return Finding{ID: "x.info", Kind: "x", Severity: SevInfo, Summary: "fyi", Fingerprint: fp}
}

func highFinding(fp string) Finding {
	return Finding{ID: "x.high", Kind: "x", Severity: SevHigh, Summary: "broken", Fingerprint: fp}
}

// --- mode parsing -----------------------------------------------------------

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"":           ModeReadOnly,
		"off":        ModeOff,
		"OFF":        ModeOff,
		"disabled":   ModeOff,
		"readonly":   ModeReadOnly,
		"read-only":  ModeReadOnly,
		"notify":     ModeReadOnly,
		"assist":     ModeAssist,
		"write":      ModeAssist,
		"garbage":    ModeReadOnly, // never silently escalate
		"  Assist  ": ModeAssist,
	}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- off mode ---------------------------------------------------------------

func TestTickIsNoopWhenOff(t *testing.T) {
	fd := &fakeDeliverer{}
	ins := &scriptedInspector{name: "x", results: []Result{{Findings: []Finding{warnFinding("a")}}}}
	m := New(Options{
		Mode:       ModeOff,
		Inspectors: []Inspector{ins},
		Targets:    oneTarget(Target{SessionPath: "s", Root: "r"}),
		Deliverer:  fd,
	})
	if n := m.Tick(context.Background()); n != 0 {
		t.Fatalf("off mode fired %d reports, want 0", n)
	}
	if ins.calls != 0 {
		t.Fatalf("off mode ran inspectors %d times, want 0", ins.calls)
	}
	// Start must be a no-op too.
	m.Start()
	if m.Running() {
		t.Fatal("Start() began ticking in off mode")
	}
	m.Close()
}

func TestTickSkipsUnusableTargets(t *testing.T) {
	fd := &fakeDeliverer{}
	ins := &scriptedInspector{name: "x", results: []Result{{Findings: []Finding{highFinding("a")}}}}
	m := New(Options{
		Mode:       ModeReadOnly,
		Inspectors: []Inspector{ins},
		Targets: func() []Target {
			return []Target{
				{SessionPath: "", Root: "/r"},    // no session
				{SessionPath: "s", Root: "  "},   // no root
				{SessionPath: "s2", Root: "/r2"}, // usable
			}
		},
		Deliverer: fd,
	})
	if n := m.Tick(context.Background()); n != 1 {
		t.Fatalf("fired %d, want 1 (only the usable target)", n)
	}
}

// --- baseline & dedupe ------------------------------------------------------

func TestFirstPassBaselinesQuietFindingsButReportsHigh(t *testing.T) {
	fd := &fakeDeliverer{}
	ins := &scriptedInspector{name: "x", results: []Result{{Findings: []Finding{
		warnFinding("w"), infoFinding("i"), highFinding("h"),
	}}}}
	m := New(Options{
		Mode:       ModeReadOnly,
		Inspectors: []Inspector{ins},
		Targets:    oneTarget(Target{SessionPath: "s", Root: "r"}),
		Deliverer:  fd,
	})
	if n := m.Tick(context.Background()); n != 1 {
		t.Fatalf("first pass fired %d reports, want 1", n)
	}
	body := fd.last().Body
	if !strings.Contains(body, "broken") {
		t.Errorf("first pass report should carry the high-severity finding, got:\n%s", body)
	}
	if strings.Contains(body, "something changed") || strings.Contains(body, "fyi") {
		t.Errorf("first pass must not report baseline warn/info findings, got:\n%s", body)
	}
}

func TestUnchangedFindingDoesNotRefire(t *testing.T) {
	fd := &fakeDeliverer{}
	ins := &scriptedInspector{name: "x", results: []Result{{Findings: []Finding{warnFinding("same")}}}}
	m := New(Options{
		Mode:       ModeReadOnly,
		Inspectors: []Inspector{ins},
		Targets:    oneTarget(Target{SessionPath: "s", Root: "r"}),
		Deliverer:  fd,
	})
	ctx := context.Background()
	m.Tick(ctx) // baseline (recorded, not reported)
	if fd.count() != 0 {
		t.Fatalf("baseline pass reported %d, want 0", fd.count())
	}
	m.Tick(ctx) // unchanged → still quiet
	m.Tick(ctx)
	if fd.count() != 0 {
		t.Fatalf("unchanged finding refired %d times, want 0", fd.count())
	}
}

func TestChangedFingerprintRefires(t *testing.T) {
	fd := &fakeDeliverer{}
	ins := &scriptedInspector{name: "x", results: []Result{
		{Findings: []Finding{warnFinding("v1")}}, // baseline
		{Findings: []Finding{warnFinding("v2")}}, // changed
	}}
	m := New(Options{
		Mode:       ModeReadOnly,
		Inspectors: []Inspector{ins},
		Targets:    oneTarget(Target{SessionPath: "s", Root: "r"}),
		Deliverer:  fd,
	})
	ctx := context.Background()
	m.Tick(ctx)
	if fd.count() != 0 {
		t.Fatalf("baseline reported %d, want 0", fd.count())
	}
	if n := m.Tick(ctx); n != 1 {
		t.Fatalf("changed finding fired %d, want 1", n)
	}
	// And it is quiet again once the new value is the remembered one.
	m.Tick(ctx)
	if fd.count() != 1 {
		t.Fatalf("total reports = %d, want 1", fd.count())
	}
}

func TestDedupeIsPerSession(t *testing.T) {
	fd := &fakeDeliverer{}
	ins := &scriptedInspector{name: "x", results: []Result{{Findings: []Finding{highFinding("h")}}}}
	targets := []Target{
		{SessionPath: "s1", Root: "r1"},
		{SessionPath: "s2", Root: "r2"},
	}
	m := New(Options{
		Mode:       ModeReadOnly,
		Inspectors: []Inspector{ins},
		Targets:    func() []Target { return targets },
		Deliverer:  fd,
	})
	// Both sessions see the same high finding on their first pass: each must
	// get its own report (the dedupe key includes the session).
	if n := m.Tick(context.Background()); n != 2 {
		t.Fatalf("fired %d, want 2 (one per session)", n)
	}
	if fd.paths[0] == fd.paths[1] {
		t.Fatalf("both reports filed under the same session path %q", fd.paths[0])
	}
}

func TestForgetTargetResetsBaseline(t *testing.T) {
	fd := &fakeDeliverer{}
	ins := &scriptedInspector{name: "x", results: []Result{{Findings: []Finding{warnFinding("w")}}}}
	m := New(Options{
		Mode:       ModeReadOnly,
		Inspectors: []Inspector{ins},
		Targets:    oneTarget(Target{SessionPath: "s", Root: "r"}),
		Deliverer:  fd,
	})
	ctx := context.Background()
	m.Tick(ctx) // baseline
	m.ForgetTarget("s")
	// After forgetting, the next pass is a fresh baseline again → quiet.
	m.Tick(ctx)
	if fd.count() != 0 {
		t.Fatalf("reports after ForgetTarget = %d, want 0", fd.count())
	}
}

// --- permission dial --------------------------------------------------------

func TestReadOnlyNeverTriggersTurn(t *testing.T) {
	fd := &fakeDeliverer{}
	ins := &scriptedInspector{name: "x", results: []Result{{Findings: []Finding{highFinding("h")}}}}
	m := New(Options{
		Mode:       ModeReadOnly,
		Inspectors: []Inspector{ins},
		Targets:    oneTarget(Target{SessionPath: "s", Root: "r"}),
		Deliverer:  fd,
	})
	m.Tick(context.Background())
	task := fd.last()
	if task.Meta.Intent != deferred.IntentNotifyUIOnly {
		t.Fatalf("readonly intent = %q, want %q", task.Meta.Intent, deferred.IntentNotifyUIOnly)
	}
	if task.Meta.Kind != "patrol" {
		t.Fatalf("kind = %q, want patrol", task.Meta.Kind)
	}
	if task.Status != deferred.Resolved {
		t.Fatalf("status = %q, want resolved", task.Status)
	}
	if strings.Contains(task.Body, "fix the ones") {
		t.Errorf("readonly report must not invite file writes:\n%s", task.Body)
	}
}

func TestAssistModeTriggersTurnForWarnOrHigher(t *testing.T) {
	cases := []struct {
		name string
		sev  string
		want deferred.DeliveryIntent
	}{
		{"info stays notify-only", SevInfo, deferred.IntentNotifyUIOnly},
		{"warn earns a turn", SevWarn, deferred.IntentTriggerTurn},
		{"high earns a turn", SevHigh, deferred.IntentTriggerTurn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fd := &fakeDeliverer{}
			mk := func(fp string) Finding {
				return Finding{ID: "x", Kind: "x", Severity: tc.sev, Summary: "s", Fingerprint: fp}
			}
			// Two ticks with a changed fingerprint: the first baselines, the
			// second is the one that actually reports.
			ins := &scriptedInspector{name: "x", results: []Result{
				{Findings: []Finding{mk("v1")}},
				{Findings: []Finding{mk("v2")}},
			}}
			m := New(Options{
				Mode:       ModeAssist,
				Inspectors: []Inspector{ins},
				Targets:    oneTarget(Target{SessionPath: "s", Root: "r"}),
				Deliverer:  fd,
			})
			ctx := context.Background()
			m.Tick(ctx)
			m.Tick(ctx)
			if fd.count() == 0 {
				t.Fatal("no report was enqueued")
			}
			if got := fd.last().Meta.Intent; got != tc.want {
				t.Fatalf("intent = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAssistantInstructionReflectsAllowWrite(t *testing.T) {
	readonly := New(Options{Mode: ModeReadOnly}).instruction(Target{Label: "proj"})
	if !strings.Contains(readonly, "read-only") || !strings.Contains(readonly, "do not modify files") {
		t.Errorf("readonly instruction is not restrictive:\n%s", readonly)
	}

	noWrite := New(Options{Mode: ModeAssist}).instruction(Target{Label: "proj"})
	if !strings.Contains(noWrite, "Do not modify files") {
		t.Errorf("assist without allow_write must forbid edits:\n%s", noWrite)
	}

	write := New(Options{Mode: ModeAssist, AllowWrite: true}).instruction(Target{Label: "proj"})
	if strings.Contains(write, "Do not modify files") {
		t.Errorf("assist with allow_write must permit edits:\n%s", write)
	}
	if !strings.Contains(write, "fix the ones") {
		t.Errorf("assist with allow_write should invite a fix:\n%s", write)
	}
}

// --- body rendering ---------------------------------------------------------

func TestReportBodyOrdersBySeverityAndClamps(t *testing.T) {
	fd := &fakeDeliverer{}
	// Two ticks with changed fingerprints so all three severities actually
	// report (the first pass baselines warn/info).
	ins := &scriptedInspector{name: "x", results: []Result{
		{Findings: []Finding{infoFinding("i1"), highFinding("h1"), warnFinding("w1")}},
		{Findings: []Finding{infoFinding("i2"), highFinding("h2"), warnFinding("w2")}},
	}}
	m := New(Options{
		Mode:       ModeReadOnly,
		Inspectors: []Inspector{ins},
		Targets:    oneTarget(Target{SessionPath: "s", Root: "r", Label: "proj"}),
		Deliverer:  fd,
		BodyLimit:  200,
	})
	ctx := context.Background()
	m.Tick(ctx)
	m.Tick(ctx)
	body := fd.last().Body
	hi := strings.Index(body, "[high]")
	wa := strings.Index(body, "[warn]")
	if hi < 0 || wa < 0 || hi > wa {
		t.Errorf("body should list high before warn, got:\n%s", body)
	}
	if len(body) > 200 {
		t.Errorf("body length = %d, want <= 200", len(body))
	}
	if !strings.Contains(body, "proj") {
		t.Errorf("body should name the target label, got:\n%s", body)
	}
}

func TestReportIDIsUniquePerFiring(t *testing.T) {
	tgt := Target{SessionPath: "s", Root: "r"}
	f := []Finding{warnFinding("same")}
	now := time.Unix(1000, 0)
	a := reportID(tgt, f, now)
	b := reportID(tgt, f, now.Add(time.Second))
	if a == b {
		t.Fatal("report ids for distinct firings must differ, else a recurrence cannot redeliver")
	}
	if !strings.HasPrefix(a, "patrol:") {
		t.Fatalf("report id %q lacks the patrol: prefix", a)
	}
}

func TestEnqueueFailureDoesNotPanicOrCount(t *testing.T) {
	fd := &fakeDeliverer{err: fmt.Errorf("boom")}
	ins := &scriptedInspector{name: "x", results: []Result{{Findings: []Finding{highFinding("h")}}}}
	m := New(Options{
		Mode:       ModeReadOnly,
		Inspectors: []Inspector{ins},
		Targets:    oneTarget(Target{SessionPath: "s", Root: "r"}),
		Deliverer:  fd,
	})
	if n := m.Tick(context.Background()); n != 0 {
		t.Fatalf("fired %d on enqueue failure, want 0", n)
	}
}

// --- lifecycle --------------------------------------------------------------

func TestStartTicksAndCloseStops(t *testing.T) {
	fd := &fakeDeliverer{}
	ins := &scriptedInspector{name: "x", results: []Result{{Findings: []Finding{highFinding("h")}}}}
	m := New(Options{
		Mode:       ModeReadOnly,
		Interval:   5 * time.Millisecond,
		Inspectors: []Inspector{ins},
		Targets:    oneTarget(Target{SessionPath: "s", Root: "r"}),
		Deliverer:  fd,
	})
	m.Start()
	if !m.Running() {
		t.Fatal("Running() = false after Start")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if passes, _ := m.Stats(); passes > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("patrol never ticked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.Close()
	if m.Running() {
		t.Fatal("Running() = true after Close")
	}
	passes, _ := m.Stats()
	m.Close() // idempotent
	time.Sleep(20 * time.Millisecond)
	if after, _ := m.Stats(); after != passes {
		t.Fatalf("patrol kept ticking after Close: %d → %d", passes, after)
	}
}

// --- git inspector ----------------------------------------------------------

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestGitInspectorDetectsDirtyTree(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	// Clean tree → no git findings.
	res := GitInspector{}.Inspect(context.Background(), Target{Root: dir}, "")
	if len(res.Findings) != 0 {
		t.Fatalf("clean tree produced findings: %+v", res.Findings)
	}

	// Dirty tree → a warn finding naming the file.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = GitInspector{}.Inspect(context.Background(), Target{Root: dir}, "")
	f := findByID(res.Findings, "git.dirty")
	if f == nil {
		t.Fatalf("no git.dirty finding, got %+v", res.Findings)
	}
	if f.Severity != SevWarn {
		t.Errorf("severity = %q, want warn", f.Severity)
	}
	if len(f.Paths) == 0 || !strings.Contains(strings.Join(f.Paths, ","), "a.txt") {
		t.Errorf("finding should name a.txt, got %v", f.Paths)
	}

	// The same tree twice → the same fingerprint (so patrol stays quiet).
	again := findByID(GitInspector{}.Inspect(context.Background(), Target{Root: dir}, "").Findings, "git.dirty")
	if again == nil || again.Fingerprint != f.Fingerprint {
		t.Errorf("fingerprint drifted for an unchanged tree: %v vs %v", f.Fingerprint, again)
	}
}

func TestGitInspectorQuietOutsideRepo(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	res := GitInspector{}.Inspect(context.Background(), Target{Root: dir}, "")
	if len(res.Findings) != 0 {
		t.Fatalf("non-repo produced findings: %+v", res.Findings)
	}
}

func TestGitInspectorQuietOnBadPath(t *testing.T) {
	res := GitInspector{}.Inspect(context.Background(), Target{Root: filepath.Join(os.TempDir(), "definitely-not-here-9f8a")}, "")
	if len(res.Findings) != 0 {
		t.Fatalf("bad path produced findings: %+v", res.Findings)
	}
}

func findByID(fs []Finding, id string) *Finding {
	for i := range fs {
		if fs[i].ID == id {
			return &fs[i]
		}
	}
	return nil
}

// --- marker inspector -------------------------------------------------------

func TestMarkerInspectorFindsAndTracksMarkers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package a\n\n// TODO: wire this up\nfunc A() {}\n")
	writeFile(t, filepath.Join(dir, "sub", "b.ts"), "// FIXME broken\nexport const b = 1\n")

	res := MarkerInspector{}.Inspect(context.Background(), Target{Root: dir}, "")
	f := findByID(res.Findings, "workspace.markers")
	if f == nil {
		t.Fatalf("no marker finding, got %+v", res.Findings)
	}
	if !strings.Contains(f.Summary, "2") {
		t.Errorf("summary should count 2 markers, got %q", f.Summary)
	}
	if !strings.Contains(f.Detail, "a.go:3") {
		t.Errorf("detail should locate a.go:3, got:\n%s", f.Detail)
	}
	if len(f.Paths) != 2 {
		t.Errorf("paths = %v, want both files", f.Paths)
	}

	// Unchanged → identical fingerprint.
	same := findByID(MarkerInspector{}.Inspect(context.Background(), Target{Root: dir}, "").Findings, "workspace.markers")
	if same == nil || same.Fingerprint != f.Fingerprint {
		t.Fatalf("fingerprint drifted: %v vs %v", f.Fingerprint, same)
	}

	// Adding a marker changes the fingerprint; so does removing one.
	writeFile(t, filepath.Join(dir, "c.go"), "package c\n\n// HACK: temporary\n")
	changed := findByID(MarkerInspector{}.Inspect(context.Background(), Target{Root: dir}, "").Findings, "workspace.markers")
	if changed == nil || changed.Fingerprint == f.Fingerprint {
		t.Fatal("adding a marker did not change the fingerprint")
	}
	if err := os.Remove(filepath.Join(dir, "a.go")); err != nil {
		t.Fatal(err)
	}
	removed := findByID(MarkerInspector{}.Inspect(context.Background(), Target{Root: dir}, "").Findings, "workspace.markers")
	if removed == nil || removed.Fingerprint == changed.Fingerprint {
		t.Fatal("removing a marker did not change the fingerprint")
	}
}

func TestMarkerInspectorSkipsNoiseDirsAndExtensions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "node_modules", "dep", "x.js"), "// TODO noise\n")
	writeFile(t, filepath.Join(dir, ".git", "hooks", "y.sh"), "# TODO noise\n")
	writeFile(t, filepath.Join(dir, "notes.bin"), "TODO noise\n") // not source
	res := MarkerInspector{}.Inspect(context.Background(), Target{Root: dir}, "")
	if len(res.Findings) != 0 {
		t.Fatalf("noise dirs/extensions produced findings: %+v", res.Findings)
	}
}

func TestMarkerInspectorQuietWhenClean(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package a\n\nfunc A() {}\n")
	res := MarkerInspector{}.Inspect(context.Background(), Target{Root: dir}, "")
	if len(res.Findings) != 0 {
		t.Fatalf("clean tree produced findings: %+v", res.Findings)
	}
	if res.State == "" {
		t.Error("state should be recorded even with no findings")
	}
}

func TestMarkerInspectorRespectsMaxFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%d.go", i)), "package p\n// TODO x\n")
	}
	res := MarkerInspector{MaxFiles: 2}.Inspect(context.Background(), Target{Root: dir}, "")
	f := findByID(res.Findings, "workspace.markers")
	if f == nil {
		t.Fatal("expected a finding")
	}
	// 2 files scanned, 1 marker each.
	if !strings.Contains(f.Summary, "2") {
		t.Errorf("summary = %q, want it to reflect the 2-file cap", f.Summary)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- end-to-end through the real deferred coordinator -----------------------

// recordDeliverer is a minimal deferred.Deliverer that records what it was
// handed, so the test can prove patrol's Enqueue reaches a real session.
type recordDeliverer struct {
	mu      sync.Mutex
	texts   []string
	trigger []bool
	active  bool
}

func (r *recordDeliverer) DeferredSessionRunnable(string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

func (r *recordDeliverer) DeliverDeferred(_, text string, triggerTurn bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	r.trigger = append(r.trigger, triggerTurn)
	return nil
}

func (r *recordDeliverer) NotifyDeferred(_, _, _ string) error { return nil }

func (r *recordDeliverer) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.texts)
}

// TestManagerDeliversThroughRealCoordinator is the integration check that
// matters: patrol's report must travel the SAME durable path a finished job
// uses — stored on disk, then delivered — with no patrol-specific plumbing.
func TestManagerDeliversThroughRealCoordinator(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.jsonl")

	rec := &recordDeliverer{active: true}
	coord := deferred.NewCoordinator(deferred.Options{
		Policy:        deferred.Policy{NotifyOnFailure: true},
		RetryInterval: time.Hour, // keep the background ticker quiet in the test
	})
	coord.SetDeliverer(rec)
	defer coord.Close()

	ins := &scriptedInspector{name: "x", results: []Result{{Findings: []Finding{highFinding("h")}}}}
	m := New(Options{
		Mode:       ModeReadOnly,
		Inspectors: []Inspector{ins},
		Targets:    oneTarget(Target{SessionPath: sessionPath, Root: dir, Label: "proj"}),
		Deliverer:  coord,
	})
	if n := m.Tick(context.Background()); n != 1 {
		t.Fatalf("Tick fired %d reports, want 1", n)
	}

	// Enqueue delivers on its own goroutine.
	deadline := time.Now().Add(3 * time.Second)
	for rec.count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the coordinator never delivered the patrol report")
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec.mu.Lock()
	text, trigger := rec.texts[0], rec.trigger[0]
	rec.mu.Unlock()
	if trigger {
		t.Error("read-only patrol must not ask for a turn")
	}
	if !strings.Contains(text, `<deferred-result source="patrol"`) {
		t.Errorf("report not wrapped as a patrol deferred result:\n%s", text)
	}
	if !strings.Contains(text, "broken") {
		t.Errorf("report lost its finding:\n%s", text)
	}

	// And it is durable: the store recorded it with source=patrol.
	st, ok := coord.Store(sessionPath)
	if !ok {
		t.Fatal("coordinator has no store for the session")
	}
	var stored bool
	for _, tk := range st.List() {
		if tk.Source == "patrol" && tk.Status == deferred.Resolved {
			stored = true
		}
	}
	if !stored {
		t.Fatalf("patrol report not persisted in the deferred store (%s)", st.Path())
	}
}

func TestClampBody(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := clampBody(long, 100)
	if len(got) > 100 {
		t.Fatalf("clamped length = %d, want <= 100", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("clamped body should say it was truncated:\n%s", got)
	}
	if short := clampBody("hi", 100); short != "hi" {
		t.Errorf("short body was altered: %q", short)
	}
}

// After Close a manager must be inert: a tick queued just before Close can
// still be mid-flight, and it must not push a report carrying the permissions
// the user just replaced (a settings change rebuilds the manager).
func TestClosedManagerIsInert(t *testing.T) {
	fd := &fakeDeliverer{}
	ins := &scriptedInspector{name: "x", results: []Result{{Findings: []Finding{highFinding("h")}}}}
	m := New(Options{
		Mode:       ModeReadOnly,
		Inspectors: []Inspector{ins},
		Targets:    oneTarget(Target{SessionPath: "s", Root: "r"}),
		Deliverer:  fd,
	})
	m.Start()
	m.Close()
	if n := m.Tick(context.Background()); n != 0 {
		t.Fatalf("closed manager fired %d reports, want 0", n)
	}
	if fd.count() != 0 {
		t.Fatalf("closed manager enqueued %d reports, want 0", fd.count())
	}
	// Close must also be safe on a manager that was never started.
	never := New(Options{Mode: ModeReadOnly})
	never.Close()
	if n := never.Tick(context.Background()); n != 0 {
		t.Fatalf("closed-but-never-started manager fired %d, want 0", n)
	}
}
