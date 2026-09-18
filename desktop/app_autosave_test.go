package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/control"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/tool"
)

type stubProvider struct{}

func (stubProvider) Name() string { return "stub" }

func (stubProvider) Stream(_ context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 1)
	close(ch)
	return ch, nil
}

func controllerWithContent(t *testing.T, path string) *control.Controller {
	t.Helper()
	sess := agent.NewSession("system")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "remember this turn"})
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "acknowledged"})
	ag := agent.New(stubProvider{}, tool.NewRegistry(), sess, agent.Options{}, event.Discard)
	return control.New(control.Options{Executor: ag, SessionPath: path, Sink: event.Discard})
}

func waitForFile(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session file %q never contained %q", path, want)
}

func waitForAutosaveIdle(t *testing.T, tab *WorkspaceTab) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tab.saveMu.Lock()
		idle := !tab.saving && !tab.saveAgain
		tab.saveMu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("autosave loop did not become idle")
}

func appWithTab(t *testing.T, path string) (*App, *WorkspaceTab) {
	t.Helper()
	ctrl := controllerWithContent(t, path)
	tab := &WorkspaceTab{
		ID:            "test_tab",
		Ctrl:          ctrl,
		Scope:         "global",
		WorkspaceRoot: "",
		Ready:         true,
		disabledMCP:   map[string]ServerView{},
	}
	tab.sink = &tabEventSink{tabID: tab.ID, app: nil}
	a := &App{
		tabs:        map[string]*WorkspaceTab{"test_tab": tab},
		activeTabID: "test_tab",
	}
	tab.sink.app = a
	return a, tab
}

// TestTurnDonePersistsSession proves a completed turn is written to disk without
// any explicit Snapshot call — the desktop autosave the data-loss fix adds. A
// nil sink ctx (no webview) must not disable persistence.
//
// Determinism: wait for the autosave loop to go IDLE first, then read the file
// once. Polling the file while the writer goroutine may still be in flight is
// the timing window that made this test flaky under full-suite load.
func TestTurnDonePersistsSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	a, tab := appWithTab(t, path)

	tab.sink.Emit(event.Event{Kind: event.TurnDone})

	waitForAutosaveIdle(t, tab)
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "remember this turn") {
		t.Fatalf("after autosave went idle, session file %q should contain the turn (err=%v)", path, err)
	}
	_ = a
}

// TestNonTurnDoneDoesNotPersist confirms only TurnDone triggers a save, so the
// per-token event storm doesn't thrash the disk.
func TestNonTurnDoneDoesNotPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	a, tab := appWithTab(t, path)
	_ = a

	tab.sink.Emit(event.Event{Kind: event.Text, Text: "tok"})

	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a non-TurnDone event wrote the session file (err=%v)", err)
	}
}

// TestScheduleSnapshotCoalesces hammers the scheduler concurrently to prove the
// single-flight loop neither panics nor drops the final write.
func TestScheduleSnapshotCoalesces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	a, tab := appWithTab(t, path)
	_ = a

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tab.sink.Emit(event.Event{Kind: event.TurnDone})
		}()
	}
	wg.Wait()

	waitForFile(t, path, "acknowledged")
	waitForAutosaveIdle(t, tab)
}
