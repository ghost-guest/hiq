package deferred

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testSession(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "s1.jsonl")
}

func terminalTask(id string, st Status, meta Meta) Task {
	return Task{ID: id, Source: "job", Title: "background bash " + id + " finished", Body: "output line 1\noutput line 2", Status: st, Meta: meta}
}

type deliveredMsg struct {
	session     string
	text        string
	triggerTurn bool
}

type fakeDeliverer struct {
	mu          sync.Mutex
	runnable    map[string]bool
	delivered   []deliveredMsg
	notices     []string
	failDeliver error
}

func (f *fakeDeliverer) DeferredSessionRunnable(sessionPath string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runnable == nil {
		return true
	}
	return f.runnable[sessionPath]
}

func (f *fakeDeliverer) DeliverDeferred(sessionPath, text string, triggerTurn bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDeliver != nil {
		return f.failDeliver
	}
	f.delivered = append(f.delivered, deliveredMsg{sessionPath, text, triggerTurn})
	return nil
}

func (f *fakeDeliverer) NotifyDeferred(_, title, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notices = append(f.notices, title)
	return nil
}

func (f *fakeDeliverer) snapshot() ([]deliveredMsg, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]deliveredMsg(nil), f.delivered...), append([]string(nil), f.notices...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDirForMatchesSessionDerivation(t *testing.T) {
	dir := t.TempDir()
	sess := filepath.Join(dir, "abc.jsonl")
	if got, want := DirFor(sess), filepath.Join(dir, "abc.deferred"); got != want {
		t.Fatalf("DirFor = %q, want %q", got, want)
	}
	if DirFor("") != "" {
		t.Fatalf("DirFor(\"\") should be empty")
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	sess := testSession(t)
	s, err := OpenStore(sess, nil)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := s.Put(terminalTask("job:bash-1", Resolved, Meta{Kind: "bash"})); err != nil {
		t.Fatalf("Put: %v", err)
	}

	reopened, err := OpenStore(sess, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.Query("job:bash-1")
	if !ok {
		t.Fatalf("task did not survive reopen")
	}
	if got.Status != Resolved || got.Body != "output line 1\noutput line 2" {
		t.Fatalf("task round-trip mismatch: %+v", got)
	}
	if n := len(reopened.ListUndelivered()); n != 1 {
		t.Fatalf("ListUndelivered = %d, want 1", n)
	}
}

func TestStoreDeliveredTaskIsNotResurrected(t *testing.T) {
	sess := testSession(t)
	s, err := OpenStore(sess, nil)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	task := terminalTask("job:bash-9", Resolved, Meta{Kind: "bash"})
	if err := s.Put(task); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.MarkDelivered(task.ID); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	// A producer re-reporting the same id (e.g. a retried bookkeeping pass) must
	// not make the result deliverable again.
	task.Body = "different body"
	if err := s.Put(task); err != nil {
		t.Fatalf("Put again: %v", err)
	}
	got, _ := s.Query(task.ID)
	if !got.Delivered {
		t.Fatalf("delivered flag was cleared by a re-put: %+v", got)
	}
	if n := len(s.ListUndelivered()); n != 0 {
		t.Fatalf("ListUndelivered = %d, want 0", n)
	}
}

func TestStoreCorruptFileIsSetAsideNotLost(t *testing.T) {
	sess := testSession(t)
	s, err := OpenStore(sess, nil)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := s.Put(terminalTask("job:bash-1", Resolved, Meta{})); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if _, err := OpenStore(sess, nil); err != nil {
		t.Fatalf("OpenStore on corrupt file should recover, got %v", err)
	}
	matches, _ := filepath.Glob(s.Path() + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("corrupt table should be preserved as .corrupt-*, got %v", matches)
	}
}

func TestPolicyTriggerTurn(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name   string
		policy Policy
		task   Task
		want   bool
	}{
		{"default never starts a turn", Policy{}, terminalTask("a", Resolved, Meta{}), false},
		{"policy opt-in on success", Policy{TriggerParentTurn: true}, terminalTask("a", Resolved, Meta{}), true},
		{"policy opt-in ignores failures", Policy{TriggerParentTurn: true}, terminalTask("a", Failed, Meta{}), false},
		{"intent forces a turn", Policy{}, terminalTask("a", Failed, Meta{Intent: IntentTriggerTurn}), true},
		{"ui-only never starts a turn", Policy{TriggerParentTurn: true}, terminalTask("a", Resolved, Meta{Intent: IntentNotifyUIOnly}), false},
		{"external never starts a turn", Policy{TriggerParentTurn: true}, terminalTask("a", Resolved, Meta{Intent: IntentExternal}), false},
		{"per-task false beats policy", Policy{TriggerParentTurn: true}, terminalTask("a", Resolved, Meta{TriggerParentTurn: &no}), false},
		{"per-task true beats policy", Policy{}, terminalTask("a", Resolved, Meta{TriggerParentTurn: &yes}), true},
	}
	for _, tc := range cases {
		if got := tc.policy.TriggerTurn(tc.task); got != tc.want {
			t.Errorf("%s: TriggerTurn = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPolicyNotify(t *testing.T) {
	no := false
	cases := []struct {
		name   string
		policy Policy
		task   Task
		want   bool
	}{
		{"failure notifies by default", Policy{NotifyOnFailure: true}, terminalTask("a", Failed, Meta{Kind: "bash"}), true},
		{"failure can be silenced", Policy{NotifyOnFailure: false}, terminalTask("a", Failed, Meta{Kind: "bash"}), false},
		{"abort counts as failure", Policy{NotifyOnFailure: true}, terminalTask("a", Aborted, Meta{Kind: "bash"}), true},
		{"bash success is quiet by default", Policy{NotifyOnFailure: true}, terminalTask("a", Resolved, Meta{Kind: "bash"}), false},
		{"task success notifies (the answer is the point)", Policy{NotifyOnFailure: true}, terminalTask("a", Resolved, Meta{Kind: "task"}), true},
		{"policy can notify successes", Policy{NotifyOnSuccess: true}, terminalTask("a", Resolved, Meta{Kind: "bash"}), true},
		{"per-task opt-out wins", Policy{NotifyOnSuccess: true}, terminalTask("a", Resolved, Meta{Kind: "task", NotifyOnSuccess: &no}), false},
		{"external is never notified", Policy{NotifyOnFailure: true}, terminalTask("a", Failed, Meta{Intent: IntentExternal}), false},
	}
	for _, tc := range cases {
		if got := tc.policy.Notify(tc.task); got != tc.want {
			t.Errorf("%s: Notify = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCoordinatorDeliversNotifiesAndMarksDelivered(t *testing.T) {
	sess := testSession(t)
	fd := &fakeDeliverer{}
	coord := NewCoordinator(Options{Policy: Policy{NotifyOnFailure: true}, RetryInterval: time.Hour})
	coord.SetDeliverer(fd)
	defer coord.Close()

	if err := coord.Enqueue(sess, terminalTask("job:bash-1", Failed, Meta{Kind: "bash", Label: "npm run build"})); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitFor(t, "delivery", func() bool { delivered, _ := fd.snapshot(); return len(delivered) == 1 })

	delivered, notices := fd.snapshot()
	if delivered[0].triggerTurn {
		t.Fatalf("default policy must not start a turn")
	}
	if !strings.Contains(delivered[0].text, `<deferred-result`) || !strings.Contains(delivered[0].text, "output line 1") {
		t.Fatalf("delivered text should carry the marker and the output: %q", delivered[0].text)
	}
	if len(notices) != 1 {
		t.Fatalf("expected exactly one notice for a failure, got %d", len(notices))
	}
	s, ok := coord.Store(sess)
	if !ok {
		t.Fatalf("store missing")
	}
	waitFor(t, "mark delivered", func() bool {
		got, _ := s.Query("job:bash-1")
		return got.Delivered
	})
}

func TestCoordinatorTriggersTurnWhenPolicyAllows(t *testing.T) {
	sess := testSession(t)
	fd := &fakeDeliverer{}
	coord := NewCoordinator(Options{Policy: Policy{TriggerParentTurn: true}, RetryInterval: time.Hour})
	coord.SetDeliverer(fd)
	defer coord.Close()

	if err := coord.Enqueue(sess, terminalTask("job:task-2", Resolved, Meta{Kind: "task"})); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitFor(t, "delivery", func() bool { delivered, _ := fd.snapshot(); return len(delivered) == 1 })
	delivered, _ := fd.snapshot()
	if !delivered[0].triggerTurn {
		t.Fatalf("policy opt-in should start a turn")
	}
}

func TestCoordinatorSuppressesAfterSessionGoesIdle(t *testing.T) {
	sess := testSession(t)
	fd := &fakeDeliverer{runnable: map[string]bool{sess: false}}
	coord := NewCoordinator(Options{MaxAttempts: 2, RetryInterval: time.Hour})
	coord.SetDeliverer(fd)
	defer coord.Close()

	s, ok := coord.Store(sess)
	if !ok {
		t.Fatalf("store missing")
	}
	if err := s.Put(terminalTask("job:bash-3", Resolved, Meta{Kind: "bash"})); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := coord.DeliverNow(sess, "job:bash-3"); !errors.Is(err, ErrSessionInactive) {
			t.Fatalf("attempt %d: err = %v, want ErrSessionInactive", i+1, err)
		}
	}
	got, _ := s.Query("job:bash-3")
	if !got.DeliverySuppressed || !strings.Contains(got.SuppressReason, "no longer active") {
		t.Fatalf("task should be suppressed after exhausting attempts: %+v", got)
	}
	if got.Delivered {
		t.Fatalf("a suppressed task must not be marked delivered")
	}
}

func TestCoordinatorSuppressesAfterDeliveryFailures(t *testing.T) {
	sess := testSession(t)
	fd := &fakeDeliverer{failDeliver: errors.New("session write failed")}
	coord := NewCoordinator(Options{MaxAttempts: 2, RetryInterval: time.Hour})
	coord.SetDeliverer(fd)
	defer coord.Close()

	s, _ := coord.Store(sess)
	if err := s.Put(terminalTask("job:bash-4", Resolved, Meta{Kind: "bash"})); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := coord.DeliverNow(sess, "job:bash-4"); err == nil {
			t.Fatalf("attempt %d should fail", i+1)
		}
	}
	got, _ := s.Query("job:bash-4")
	if !got.DeliverySuppressed || !strings.Contains(got.SuppressReason, "delivery failed") {
		t.Fatalf("task should be suppressed after repeated failures: %+v", got)
	}
}

// A result produced by a previous process (crash, or a job that finished while
// the session was closed) must be delivered when the session comes back.
func TestFlushSessionDeliversAfterRestart(t *testing.T) {
	sess := testSession(t)

	first := NewCoordinator(Options{RetryInterval: time.Hour})
	// No deliverer: the result is recorded but cannot be handed over yet.
	if err := first.Enqueue(sess, terminalTask("job:bash-5", Resolved, Meta{Kind: "task"})); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	first.Close()

	fd := &fakeDeliverer{}
	second := NewCoordinator(Options{RetryInterval: time.Hour})
	second.SetDeliverer(fd)
	defer second.Close()
	second.FlushSession(sess)

	delivered, _ := fd.snapshot()
	if len(delivered) != 1 {
		t.Fatalf("pending result should be delivered after restart, got %d", len(delivered))
	}
	s, _ := second.Store(sess)
	got, _ := s.Query("job:bash-5")
	if !got.Delivered {
		t.Fatalf("task should be marked delivered: %+v", got)
	}
}

func TestFlushSessionPrunesOldDeliveredRows(t *testing.T) {
	sess := testSession(t)
	base := time.Now()
	// The clock is read from delivery goroutines, so it needs its own lock
	// (the race detector is right to complain otherwise).
	var clockMu sync.Mutex
	clock := base
	clockNow := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		clock = clock.Add(d)
		clockMu.Unlock()
	}

	coord := NewCoordinator(Options{
		RetryInterval:   time.Hour,
		RetainDelivered: time.Hour,
		Now:             clockNow,
	})
	fd := &fakeDeliverer{}
	coord.SetDeliverer(fd)
	defer coord.Close()

	if err := coord.Enqueue(sess, terminalTask("job:bash-6", Resolved, Meta{Kind: "task"})); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitFor(t, "delivery", func() bool { delivered, _ := fd.snapshot(); return len(delivered) == 1 })
	// Wait for the bookkeeping too, so advancing the clock can't race the
	// delivery goroutine's MarkDelivered.
	s, _ := coord.Store(sess)
	waitFor(t, "mark delivered", func() bool {
		got, _ := s.Query("job:bash-6")
		return got.Delivered
	})

	advance(3 * time.Hour)
	coord.FlushSession(sess)
	if n := len(s.List()); n != 0 {
		t.Fatalf("expected the delivered row to be pruned, %d left", n)
	}
}

func TestTaskWrappedCarriesActionAndEscapedLabel(t *testing.T) {
	task := Task{
		ID:     "job:bash-7",
		Source: "job",
		Title:  `background bash job:bash-7 ("npm run build") failed: exit status 1`,
		Body:   "tsc error",
		Status: Failed,
		Meta:   Meta{Kind: "bash", Label: `npm "build"`},
	}
	got := task.Wrapped()
	for _, want := range []string{
		`<deferred-result source="job" id="job:bash-7" status="failed"`,
		`label="npm 'build'">`,
		"tsc error",
		"diagnose and fix it now",
		"</deferred-result>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Wrapped() missing %q in:\n%s", want, got)
		}
	}
	if strings.Count(got, "\n") < 3 {
		t.Errorf("Wrapped() should be multi-line:\n%s", got)
	}
}

func TestTruncateBodyKeepsHeadAndTail(t *testing.T) {
	long := strings.Repeat("A", 300) + strings.Repeat("B", 300)
	got := truncateBody(long, 120)
	if len(got) == 0 || len(got) > 200 {
		t.Fatalf("truncateBody length = %d, want a bounded value", len(got))
	}
	if !strings.HasPrefix(got, "AAAA") || !strings.HasSuffix(got, "BBBB") {
		t.Fatalf("truncateBody must keep head and tail, got %q", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("truncateBody should mark the omission: %q", got)
	}
	if short := truncateBody("small", 100); short != "small" {
		t.Fatalf("truncateBody modified a short body: %q", short)
	}
}

func TestEnqueueRequiresTerminalStatusForDelivery(t *testing.T) {
	sess := testSession(t)
	fd := &fakeDeliverer{}
	coord := NewCoordinator(Options{RetryInterval: time.Hour})
	coord.SetDeliverer(fd)
	defer coord.Close()

	task := terminalTask("job:bash-8", Pending, Meta{Kind: "bash"})
	if err := coord.Enqueue(sess, task); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if delivered, _ := fd.snapshot(); len(delivered) != 0 {
		t.Fatalf("a pending task must not be delivered")
	}
}
