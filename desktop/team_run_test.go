package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/taskmonitor"
	teampkg "github.com/zzycxz/hiq/internal/team"
)

// teamTestApp builds an App wired to a throwaway team store, so the pool,
// recovery and cascade paths can be exercised without a provider or a window.
// It reuses isolateDesktopUserDirs because requireTeamStore falls back to the
// user state root whenever teamStore is nil.
func teamTestApp(t *testing.T) (*App, *teampkg.Store) {
	t.Helper()
	isolateDesktopUserDirs(t)
	store, err := teampkg.NewStore(filepath.Join(robustTempDir(t), "team_projects.json"))
	if err != nil {
		t.Fatalf("new team store: %v", err)
	}
	a := &App{teamStore: store}
	a.teamPoolsInit()
	t.Cleanup(func() { a.CancelAllTeamRuns("test cleanup") })
	return a, store
}

// seedTeam creates a one-member team with a single card in the given state.
func seedTeam(t *testing.T, store *teampkg.Store, status taskmonitor.TaskState, maxParallel int) teampkg.Team {
	t.Helper()
	tm, err := store.Create(teampkg.Team{
		Name:    "回归团队",
		Members: []teampkg.Member{{ID: "m1", Name: "甲", Role: "实现", IsLeader: true}},
		Policy:  teampkg.Policy{MaxRounds: 5, MaxParallel: maxParallel},
	})
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	tm, err = store.AddTask(tm.ID, teampkg.Task{
		Title:      "写个模块",
		AssigneeID: "m1",
		Status:     status,
	})
	if err != nil {
		t.Fatalf("add task: %v", err)
	}
	return tm
}

func taskStatus(t *testing.T, store *teampkg.Store, teamID, taskID string) teampkg.Task {
	t.Helper()
	tm, ok := store.Get(teamID)
	if !ok {
		t.Fatalf("team %s vanished", teamID)
	}
	tk, ok := tm.Task(taskID)
	if !ok {
		t.Fatalf("task %s vanished", taskID)
	}
	return tk
}

// TestRecoverTeamRunsMarksStale is the P1-2 contract: a card persisted as
// running cannot have a live run behind it after a restart, because runs live in
// memory only. It must be reported honestly instead of spinning forever.
func TestRecoverTeamRunsMarksStale(t *testing.T) {
	a, store := teamTestApp(t)
	tm := seedTeam(t, store, taskmonitor.TaskStateRunning, 2)
	card := tm.Tasks[0]

	a.recoverTeamRuns()

	got := taskStatus(t, store, tm.ID, card.ID)
	if got.Status != taskmonitor.TaskStateStale {
		t.Fatalf("status = %q, want stale", got.Status)
	}
	if !strings.Contains(got.Error, "中断") {
		t.Fatalf("error = %q, want an interrupted-run note", got.Error)
	}
	if got.Progress != "" {
		t.Fatalf("progress = %q, want cleared", got.Progress)
	}
}

// A run that really is in flight must survive recovery untouched.
func TestRecoverTeamRunsKeepsLiveRun(t *testing.T) {
	a, store := teamTestApp(t)
	tm := seedTeam(t, store, taskmonitor.TaskStateRunning, 2)
	card := tm.Tasks[0]

	pool := a.teamPoolEnsure(tm.ID, 2)
	if state := pool.Admit(teamRunKey(tm.ID, card.ID)); state != teampkg.AdmitRunning {
		t.Fatalf("admit = %v, want running", state)
	}

	a.recoverTeamRuns()

	if got := taskStatus(t, store, tm.ID, card.ID); got.Status != taskmonitor.TaskStateRunning {
		t.Fatalf("live run was recovered away: status = %q", got.Status)
	}
}

// A card that is not running at all must not be touched.
func TestRecoverTeamRunsIgnoresOtherStates(t *testing.T) {
	a, store := teamTestApp(t)
	tm := seedTeam(t, store, taskmonitor.TaskStateSucceeded, 2)
	card := tm.Tasks[0]

	a.recoverTeamRuns()

	if got := taskStatus(t, store, tm.ID, card.ID); got.Status != taskmonitor.TaskStateSucceeded {
		t.Fatalf("status = %q, want succeeded", got.Status)
	}
}

// TestRunTeamTaskQueuesWhenPoolFull is the P1-1 contract end to end on the
// desktop side: with the single slot taken, starting a card parks it in the
// queue (status queued + a queue note) rather than opening a second session.
func TestRunTeamTaskQueuesWhenPoolFull(t *testing.T) {
	a, store := teamTestApp(t)
	tm := seedTeam(t, store, taskmonitor.TaskStateQueued, 1)
	card := tm.Tasks[0]

	pool := a.teamPoolEnsure(tm.ID, 1)
	if state := pool.Admit("other/occupier"); state != teampkg.AdmitRunning {
		t.Fatalf("failed to occupy the only slot: %v", state)
	}

	view, err := a.RunTeamTask(tm.ID, card.ID)
	if err != nil {
		t.Fatalf("RunTeamTask should queue, got error: %v", err)
	}

	got := taskStatus(t, store, tm.ID, card.ID)
	if got.Status != taskmonitor.TaskStateQueued {
		t.Fatalf("status = %q, want queued", got.Status)
	}
	if !strings.Contains(got.Progress, "排队中") {
		t.Fatalf("progress = %q, want a queue note", got.Progress)
	}
	if len(view.Tasks) == 0 {
		t.Fatalf("view carried no tasks")
	}
	if snap := pool.Snapshot(); len(snap.Queued) != 1 || snap.Queued[0] != teamRunKey(tm.ID, card.ID) {
		t.Fatalf("queue = %v, want the card key", snap.Queued)
	}
	// Starting it twice must be a duplicate, not a second queue slot.
	if _, err := a.RunTeamTask(tm.ID, card.ID); err == nil {
		t.Fatalf("re-running a queued card should be rejected")
	}
	if snap := pool.Snapshot(); len(snap.Queued) != 1 {
		t.Fatalf("duplicate took a second queue slot: %v", snap.Queued)
	}
}

// TestCancelAllTeamRunsDrainsEverything is the P1-3 contract: the shutdown path
// must cancel every in-flight run and empty every queue, leaving nothing to burn
// quota behind a closed window.
func TestCancelAllTeamRunsDrainsEverything(t *testing.T) {
	a, store := teamTestApp(t)
	tm := seedTeam(t, store, taskmonitor.TaskStateQueued, 1)
	card := tm.Tasks[0]

	pool := a.teamPoolEnsure(tm.ID, 1)
	liveKey := teamRunKey(tm.ID, card.ID)
	queuedKey := teamRunKey(tm.ID, "waiting-card")
	if state := pool.Admit(liveKey); state != teampkg.AdmitRunning {
		t.Fatalf("admit live = %v, want running", state)
	}
	if state := pool.Admit(queuedKey); state != teampkg.AdmitQueued {
		t.Fatalf("admit second = %v, want queued", state)
	}

	cancelled := 0
	a.teamRunsMu.Lock()
	a.teamRuns = map[string]context.CancelFunc{liveKey: func() { cancelled++ }}
	a.teamRunsMu.Unlock()

	// Give the queued card a note so clearing it is observable.
	if _, err := store.Update(tm.ID, func(x *teampkg.Team) {
		x.Tasks[0].Progress = "排队中（第 1/1 位，等前面的成员跑完自动开始）"
	}); err != nil {
		t.Fatalf("seed progress: %v", err)
	}

	a.CancelAllTeamRuns("测试")

	if cancelled != 1 {
		t.Fatalf("cancelled = %d, want 1", cancelled)
	}
	if snap := pool.Snapshot(); len(snap.Running) != 0 || len(snap.Queued) != 0 {
		t.Fatalf("pool not drained: %+v", snap)
	}
	a.teamRunsMu.Lock()
	left := len(a.teamRuns)
	a.teamRunsMu.Unlock()
	if left != 0 {
		t.Fatalf("cancel handles left behind: %d", left)
	}
	if got := taskStatus(t, store, tm.ID, card.ID); got.Progress != "" {
		t.Fatalf("queued note survived the cascade: %q", got.Progress)
	}
}

// A queued card is cancelled by leaving the queue — there is no run to cancel.
func TestCancelTeamTaskDequeues(t *testing.T) {
	a, store := teamTestApp(t)
	tm := seedTeam(t, store, taskmonitor.TaskStateQueued, 1)
	card := tm.Tasks[0]

	pool := a.teamPoolEnsure(tm.ID, 1)
	pool.Admit("other/occupier")
	key := teamRunKey(tm.ID, card.ID)
	if state := pool.Admit(key); state != teampkg.AdmitQueued {
		t.Fatalf("setup: admit = %v, want queued", state)
	}

	if err := a.CancelTeamTask(tm.ID, card.ID); err != nil {
		t.Fatalf("cancel queued card: %v", err)
	}
	if snap := pool.Snapshot(); len(snap.Queued) != 0 {
		t.Fatalf("queue not emptied: %v", snap.Queued)
	}
	// Cancelling something that neither runs nor waits is still an error.
	if err := a.CancelTeamTask(tm.ID, card.ID); err == nil {
		t.Fatalf("cancelling an idle card should report an error")
	}
}
