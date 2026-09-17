package main

import (
	"testing"

	"github.com/zzycxz/fairpeer/internal/config"
)

func TestCanonicalPatrolModeRejectsUnknown(t *testing.T) {
	ok := map[string]string{
		"":         "readonly",
		"off":      "off",
		"READONLY": "readonly",
		" assist ": "assist",
	}
	for in, want := range ok {
		got, err := canonicalPatrolMode(in)
		if err != nil {
			t.Errorf("canonicalPatrolMode(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("canonicalPatrolMode(%q) = %q, want %q", in, got, want)
		}
	}
	// A typo must be refused, not coerced: silently downgrading would hide a UI
	// bug behind a feature that looks like it works.
	if _, err := canonicalPatrolMode("readonlyy"); err == nil {
		t.Error("canonicalPatrolMode accepted an unknown mode")
	}
	if _, err := canonicalPatrolMode("write"); err == nil {
		t.Error("canonicalPatrolMode accepted the alias 'write' (the panel must send canonical values)")
	}
}

func TestNormalizePatrolChecksDropsUnknown(t *testing.T) {
	got := normalizePatrolChecks([]string{"git", "bogus", " Markers ", "", "markers"})
	want := []string{"git", "markers", "markers"}
	if len(got) != len(want) {
		t.Fatalf("normalizePatrolChecks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizePatrolChecks = %v, want %v", got, want)
		}
	}
	if len(normalizePatrolChecks(nil)) != 0 {
		t.Error("nil checks should stay empty (which the kernel reads as 'all')")
	}
}

func TestPatrolViewReflectsConfig(t *testing.T) {
	on := true
	cfg := config.Default()
	cfg.Patrol.Enabled = &on
	cfg.Patrol.Mode = "assist"
	cfg.Patrol.AllowWrite = &on
	cfg.Patrol.IntervalSec = 60
	cfg.Patrol.Checks = []string{"git"}

	v := patrolView(cfg)
	if !v.Enabled || v.Mode != "assist" || !v.AllowWrite {
		t.Errorf("view did not reflect config: %+v", v)
	}
	if v.IntervalSec != 60 || len(v.Checks) != 1 || v.Checks[0] != "git" {
		t.Errorf("view interval/checks wrong: %+v", v)
	}
	// boot has not run in a unit test, so there is no live manager: the panel
	// must show "not running" rather than claim a heartbeat.
	if v.Running || v.Passes != 0 || v.Fired != 0 {
		t.Errorf("live status should be zero without a manager: %+v", v)
	}
	// Checks must never be nil (the panel renders an array).
	if v.Checks == nil {
		t.Error("checks should be a non-nil slice")
	}
}

func TestPatrolViewWhenUnreadableIsSafe(t *testing.T) {
	v := patrolViewWhenUnreadable()
	if v.Enabled {
		t.Error("unreadable config must not report patrol as enabled")
	}
	if v.Mode != "readonly" {
		t.Errorf("mode = %q, want readonly", v.Mode)
	}
	if v.AllowWrite {
		t.Error("unreadable config must not report write permission")
	}
	if v.IntervalSec != config.DefaultPatrolIntervalSec {
		t.Errorf("interval = %d, want the default", v.IntervalSec)
	}
}
