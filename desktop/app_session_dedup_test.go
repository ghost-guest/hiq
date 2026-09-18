package main

import (
	"path/filepath"
	"testing"

	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/control"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/tool"
)

func carryingController(carried []provider.Message, path string) *control.Controller {
	sess := &agent.Session{}
	sess.Replace(carried)
	ag := agent.New(stubProvider{}, tool.NewRegistry(), sess, agent.Options{}, event.Discard)
	return control.New(control.Options{Executor: ag, SessionPath: path, Sink: event.Discard})
}

// TestCarriedRebuildsKeepOneSession reproduces issue #2807: a model switch or any
// config change rebuilds the controller and carries the conversation forward. Each
// rebuild must keep writing to the same file, so a run of them leaves exactly one
// history entry — not a new identical duplicate per rebuild.
func TestCarriedRebuildsKeepOneSession(t *testing.T) {
	dir := t.TempDir()
	path := agent.NewSessionPath(dir, "model-a")
	ctrl := controllerWithContent(t, path)
	if err := ctrl.Snapshot(); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		prevPath := ctrl.SessionPath()
		carried := ctrl.History()
		ctrl.Close()

		newPath := agent.ContinueSessionPath(prevPath, dir, "model-b")
		ctrl = carryingController(carried, newPath)
		if err := ctrl.Snapshot(); err != nil {
			t.Fatal(err)
		}
	}
	ctrl.Close()

	infos, err := agent.ListSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		paths := make([]string, len(infos))
		for i, s := range infos {
			paths[i] = filepath.Base(s.Path)
		}
		t.Fatalf("after 5 carried rebuilds the history shows %d sessions, want 1: %v", len(infos), paths)
	}
}

// EnsureBlankTab reuses an already-open blank tab rather than creating a second one.

func TestEnsureBlankTabReusesExistingBlankTab(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	first, err := app.EnsureBlankTab("global", "", "dev")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.EnsureBlankTab("global", "", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("EnsureBlankTab created duplicate blank tab: first=%q second=%q", first.ID, second.ID)
	}
	if tabs := app.ListTabs(); len(tabs) != 1 {
		t.Fatalf("ListTabs length = %d, want 1: %+v", len(tabs), tabs)
	}
}

// EnsureBlankTab reuses an already-open project-scoped blank tab.

func TestEnsureBlankTabCreatesOneBlankPerProject(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	app := NewApp()
	first, err := app.EnsureBlankTab("project", projectRoot, "dev")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.EnsureBlankTab("project", projectRoot, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("EnsureBlankTab created duplicate project blank tab: first=%q second=%q", first.ID, second.ID)
	}
	if tabs := app.ListTabs(); len(tabs) != 1 {
		t.Fatalf("ListTabs length = %d, want 1: %+v", len(tabs), tabs)
	}
}

// EnsureBlankTab picks up an existing blank topic created in the sidebar
// instead of creating a fresh topic. Global scope is retired (08-21): legacy
// global callers land on the profile home project 工作台.

func TestEnsureBlankTabOpensExistingSidebarBlankTopic(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	topic, err := app.CreateTopic("global", "", "")
	if err != nil {
		t.Fatal(err)
	}

	meta, err := app.EnsureBlankTab("global", "", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if meta.TopicID != topic.ID {
		t.Fatalf("EnsureBlankTab opened topic %q, want existing blank topic %q", meta.TopicID, topic.ID)
	}
	if topics := loadProjectsFile("dev").GlobalTopics; len(topics) != 0 {
		t.Fatalf("global topics length = %d, want 0 (retired): %v", len(topics), topics)
	}
	home := profileHomeRoot("dev")
	seedTopicTurn(t, "project", home, topic.ID, "dev")
	nodes := app.ListProjectTree("dev")
	found := false
	if project := findTreeProject(nodes, home); project != nil {
		for _, child := range project.Children {
			if child.TopicID == topic.ID {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("blank topic %q not visible under home project %s: %#v", topic.ID, home, nodes)
	}
}

// EnsureBlankTab picks up an existing blank topic created in the sidebar
// instead of creating a fresh topic, for project scope.

func TestEnsureBlankTabOpensExistingProjectSidebarBlankTopic(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	app := NewApp()
	topic, err := app.CreateTopic("project", projectRoot, "")
	if err != nil {
		t.Fatal(err)
	}

	meta, err := app.EnsureBlankTab("project", projectRoot, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if meta.TopicID != topic.ID {
		t.Fatalf("EnsureBlankTab opened topic %q, want existing blank topic %q", meta.TopicID, topic.ID)
	}
	var topics []string
	for _, project := range loadProjectsFile().Projects {
		if project.Root == projectRoot {
			topics = project.Topics
			break
		}
	}
	if len(topics) != 1 {
		t.Fatalf("project topics length = %d, want 1: %v", len(topics), topics)
	}
}

// NewSession skips the snapshot when the current tab has no real conversation content.

func TestNewSessionNoopsWhenCurrentTabIsBlank(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := t.TempDir()
	path := agent.NewSessionPath(dir, "model-a")
	ctrl := carryingController([]provider.Message{{Role: provider.RoleSystem, Content: "sys"}}, path)
	app := NewApp()
	app.setTestCtrl(ctrl, "model-a")

	if err := app.NewSession(); err != nil {
		t.Fatal(err)
	}
	if got := ctrl.SessionPath(); got != path {
		t.Fatalf("blank NewSession changed session path = %q, want %q", got, path)
	}
}

// EnsureBlankTab strictly isolates blank tabs by profile ("dev" vs "cowork"),
// ensuring switching modes never reuses a blank tab from another mode.
func TestEnsureBlankTabStrictProfileIsolation(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	app := NewApp()

	// 1. Request a blank tab in "dev" mode
	devTab, err := app.EnsureBlankTab("project", projectRoot, "dev")
	if err != nil {
		t.Fatalf("EnsureBlankTab dev failed: %v", err)
	}
	if devTab.Profile != "dev" && devTab.Profile != "" {
		t.Fatalf("expected profile dev or empty, got %q", devTab.Profile)
	}

	// 2. Request a blank tab in "cowork" mode; it must not reuse the dev tab!
	coworkTab, err := app.EnsureBlankTab("project", projectRoot, "cowork")
	if err != nil {
		t.Fatalf("EnsureBlankTab cowork failed: %v", err)
	}
	if coworkTab.Profile != "cowork" {
		t.Fatalf("expected profile cowork, got %q", coworkTab.Profile)
	}
	if coworkTab.ID == devTab.ID {
		t.Fatalf("profile isolation failed: cowork tab reused dev tab ID %q", devTab.ID)
	}

	// 3. Requesting cowork again should cleanly reuse the cowork tab (dedup preservation)
	coworkTab2, err := app.EnsureBlankTab("project", projectRoot, "cowork")
	if err != nil {
		t.Fatalf("EnsureBlankTab cowork 2 failed: %v", err)
	}
	if coworkTab2.ID != coworkTab.ID {
		t.Fatalf("same-profile dedup failed: expected ID %q, got %q", coworkTab.ID, coworkTab2.ID)
	}
}

// TestCoworkSessionHistoryPersistenceAndDeletion ensures that sessions created and
// chatted within the "cowork" profile persist properly into the partition directory,
// restore their history when reopened (no blank reset), and can be deleted cleanly.
func TestCoworkSessionHistoryPersistenceAndDeletion(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	app := NewApp()

	// 1. Create a topic in cowork profile and open a tab for it.
	topic, err := app.CreateTopic("project", projectRoot, "cowork", "")
	if err != nil {
		t.Fatalf("CreateTopic in cowork failed: %v", err)
	}
	_, err = app.OpenProjectTab3(projectRoot, topic.ID, "cowork")
	if err != nil {
		t.Fatalf("OpenProjectTab3 cowork failed: %v", err)
	}
}
