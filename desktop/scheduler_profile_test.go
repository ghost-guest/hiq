package main

import (
	"testing"

	"github.com/zzycxz/hiq/internal/control"
)

// The profiles are independent surfaces: a scheduled task must only run in a
// tab of its OWN profile. A cowork prompt firing into the active netdev tab
// (sealed tools) or dev tab used to be possible — this pins the routing.
func TestScheduledTargetTabMatchesProfile(t *testing.T) {
	mkTab := func(id, profile string, ready bool) *WorkspaceTab {
		tab := &WorkspaceTab{ID: id, profile: profile, Ready: ready}
		if ready {
			tab.Ctrl = &control.Controller{}
		}
		return tab
	}

	// Active tab of the task's profile → used directly.
	a := &App{tabs: map[string]*WorkspaceTab{
		"dev1":  mkTab("dev1", "dev", true),
		"ndv1":  mkTab("ndv1", "netdev", true),
	}, activeTabID: "ndv1"}
	if got := scheduledTargetTabLocked(a, "netdev"); got == nil || got.ID != "ndv1" {
		t.Fatalf("netdev task with active netdev tab: got %v", got)
	}

	// Active tab of ANOTHER profile → fall back to a ready tab of the task's
	// profile instead of executing in the focused tab.
	a.activeTabID = "dev1"
	got := scheduledTargetTabLocked(a, "netdev")
	if got == nil || got.ID != "ndv1" {
		t.Fatalf("netdev task with active dev tab: got %v, want ndv1", got)
	}
	if got = scheduledTargetTabLocked(a, "cowork"); got != nil {
		t.Fatalf("cowork task with no cowork tab: got %v, want nil (headless path)", got)
	}

	// No matching tab anywhere → nil, so Run falls through to headless.
	a.tabs = map[string]*WorkspaceTab{"dev1": mkTab("dev1", "dev", true)}
	a.activeTabID = "dev1"
	if got = scheduledTargetTabLocked(a, "cowork"); got != nil {
		t.Fatalf("cowork task with only dev tabs: got %v, want nil", got)
	}

	// Empty profile on tab or task means dev — legacy sessions stay routable.
	legacy := &WorkspaceTab{ID: "legacy", Ready: true, Ctrl: &control.Controller{}}
	a.tabs["legacy"] = legacy
	if got = scheduledTargetTabLocked(a, ""); got == nil || got.ID != "dev1" {
		t.Fatalf("dev task with legacy unprofiled tabs: got %v, want dev1", got)
	}
	a.activeTabID = "legacy"
	if got = scheduledTargetTabLocked(a, "dev"); got == nil || got.ID != "legacy" {
		t.Fatalf("dev task with active legacy tab: got %v, want legacy", got)
	}

	// Not-ready matching tabs are skipped (Run would otherwise submit into a
	// controller still booting).
	a.tabs["ndv2"] = mkTab("ndv2", "netdev", false)
	if got = scheduledTargetTabLocked(a, "netdev"); got != nil {
		t.Fatalf("netdev task with only a not-ready netdev tab: got %v, want nil", got)
	}
}
