package main

// Strict three-profile project isolation (2026-08-21): dev/cowork/netdev
// project indexes never mix. Regression tests for the guard set that keeps a
// foreign profile from claiming another mode's workspace root — the original
// leak was the mode switcher carrying the active coding workspace into the
// newly-opened ops index.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

// seedOwnedProject registers a project with one topic under a profile,
// making that profile the root's owner.
func seedOwnedProject(t *testing.T, root, profile string) string {
	t.Helper()
	app := NewApp()
	topic, err := app.CreateTopic("project", root, profile, "owner topic")
	if err != nil {
		t.Fatalf("seed project topic: %v", err)
	}
	return topic.ID
}

func projectRootsForProfile(profile string) map[string]bool {
	roots := map[string]bool{}
	for _, p := range loadProjectsFile(profile).Projects {
		roots[normalizeProjectRoot(p.Root)] = true
	}
	return roots
}

// EnsureBlankTab with a foreign root lands on the profile home and never
// registers the foreign root in this profile's index.
func TestEnsureBlankTabRefusesForeignProjectRoot(t *testing.T) {
	isolateDesktopUserDirs(t)

	devRoot := t.TempDir()
	seedOwnedProject(t, devRoot, "dev")

	app := NewApp()
	meta, err := app.EnsureBlankTab("project", devRoot, "netdev")
	if err != nil {
		t.Fatalf("EnsureBlankTab: %v", err)
	}
	if got, home := normalizeProjectRoot(meta.WorkspaceRoot), normalizeProjectRoot(profileHomeRoot("netdev")); got != home {
		t.Fatalf("blank tab root = %q, want netdev home %q", got, home)
	}
	if roots := projectRootsForProfile("netdev"); roots[normalizeProjectRoot(devRoot)] {
		t.Fatalf("foreign root %s leaked into netdev index: %v", devRoot, roots)
	}
}

// OpenProjectTab and CreateTopic reject a foreign root outright.
func TestOpenAndCreateRejectForeignProjectRoot(t *testing.T) {
	isolateDesktopUserDirs(t)

	devRoot := t.TempDir()
	topicID := seedOwnedProject(t, devRoot, "dev")

	app := NewApp()
	if _, err := app.OpenProjectTab(devRoot, topicID, "cowork"); err == nil {
		t.Fatalf("OpenProjectTab on foreign root succeeded, want isolation error")
	}
	if _, err := app.CreateTopic("project", devRoot, "cowork", " trespass"); err == nil {
		t.Fatalf("CreateTopic on foreign root succeeded, want isolation error")
	}
	if roots := projectRootsForProfile("cowork"); roots[normalizeProjectRoot(devRoot)] {
		t.Fatalf("foreign root leaked into cowork index: %v", roots)
	}
}

// Home roots belong to exactly one profile: another profile's home is foreign.
func TestHomeRootsAreProfilePrivate(t *testing.T) {
	isolateDesktopUserDirs(t)

	if !rootOwnedByOtherProfile(profileHomeRoot("dev"), "netdev") {
		t.Fatalf("dev home root should be foreign to netdev")
	}
	if rootOwnedByOtherProfile(profileHomeRoot("dev"), "dev") {
		t.Fatalf("dev home root should belong to dev")
	}
	if rootOwnedByOtherProfile(filepath.Join(t.TempDir(), "free"), "cowork") {
		t.Fatalf("fresh root with no owner anywhere must be registerable")
	}
}

// pruneForeignProjects repairs an index polluted before the guard existed:
// a foreign entry with only a blank topic disappears; the owner keeps the root.
func TestPruneForeignProjectsRepairsPollutedIndex(t *testing.T) {
	isolateDesktopUserDirs(t)

	devRoot := t.TempDir()
	seedOwnedProject(t, devRoot, "dev")

	// Simulate the pre-guard pollution: netdev registered the dev root with
	// a blank topic.
	if _, err := NewApp().CreateTopic("project", devRoot, "netdev", ""); err != nil {
		// The guard already blocks it — plant the entry directly instead.
		f := loadProjectsFile("netdev")
		f.Projects = append(f.Projects, desktopProject{Root: normalizeProjectRoot(devRoot), Topics: []string{"topic_stray"}})
		if err := saveProjectsFile(f, "netdev"); err != nil {
			t.Fatalf("plant stray entry: %v", err)
		}
	}

	pruneForeignProjects()

	if roots := projectRootsForProfile("netdev"); roots[normalizeProjectRoot(devRoot)] {
		t.Fatalf("foreign root survived prune: %v", roots)
	}
	if roots := projectRootsForProfile("dev"); !roots[normalizeProjectRoot(devRoot)] {
		t.Fatalf("owner lost its root after prune: %v", roots)
	}
}

// The pre-profile shared workspace list (desktop-workspaces.json) fed only
// dev, once, and is retired: it must never generate entries in a non-dev
// profile's index. (Formerly ListWorkspaces injected it into whatever
// profile asked first — the coding workspace appearing in a fresh ops
// interface.)
func TestLegacyWorkspaceListFeedsOnlyDevOnce(t *testing.T) {
	isolateDesktopUserDirs(t)

	shared := filepath.Join(t.TempDir(), "shared-root")
	p := workspaceListPath()
	if p == "" {
		t.Skip("workspace list path unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal([]string{shared})
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}

	migrateLegacyWorkspacesIntoDevOnce()

	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("legacy workspace list should be retired after the one-time drain: %v", err)
	}
	if roots := projectRootsForProfile("dev"); !roots[normalizeProjectRoot(shared)] {
		t.Fatalf("dev should absorb the legacy root: %v", roots)
	}
	for _, profile := range []string{"cowork", "netdev"} {
		if roots := projectRootsForProfile(profile); roots[normalizeProjectRoot(shared)] {
			t.Fatalf("legacy root leaked into %s: %v", profile, roots)
		}
	}
}

// Legacy unscoped sessions in the DEV partition never feed a non-dev
// profile's tree: each profile's legacy adoption reads only its own
// partition. (The dev partition dir was previously passed for every
// profile — a fresh ops interface could absorb coding-era conversations
// into its home project.)
func TestLegacyAdoptionReadsOnlyOwnPartition(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLegacySession(t, dir, "partition-legacy.jsonl", "dev era conversation", time.Now().Add(-time.Hour))

	app := NewApp()
	nodes := app.ListProjectTree("netdev")
	if project := findTreeProject(nodes, profileHomeRoot("netdev")); project != nil && len(project.Children) != 0 {
		t.Fatalf("netdev home absorbed dev-partition legacy sessions: %#v", project.Children)
	}

	devNodes := app.ListProjectTree("dev")
	if project := findTreeProject(devNodes, profileHomeRoot("dev")); project == nil || len(project.Children) == 0 {
		t.Fatalf("dev home should absorb its own partition's legacy sessions: %#v", devNodes)
	}
}
