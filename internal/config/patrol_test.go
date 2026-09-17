package config

import (
	"path/filepath"
	"testing"
)

// isolateConfigHome pins every path the config layer derives from the
// environment, so a test that writes a config file cannot touch the developer's
// real ~/.config/fairpeer (the project's mandatory four-variable isolation).
func isolateConfigHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
}

// The patrol permission dial must fail closed: an absent or malformed config
// must never enable the heartbeat, and must never raise its autonomy above
// "readonly".
func TestPatrolDefaultsAreOffAndReadOnly(t *testing.T) {
	var p PatrolConfig

	if p.EnabledEffective() {
		t.Error("patrol must be off by default")
	}
	if got := p.ModeEffective(); got != "readonly" {
		t.Errorf("ModeEffective() = %q, want readonly", got)
	}
	if p.AllowWriteEffective() {
		t.Error("allow_write must be off by default")
	}
	if got := p.IntervalSecondsEffective(); got != DefaultPatrolIntervalSec {
		t.Errorf("IntervalSecondsEffective() = %d, want %d", got, DefaultPatrolIntervalSec)
	}
	if got := p.BodyLimitBytesEffective(); got != DefaultPatrolBodyLimitBytes {
		t.Errorf("BodyLimitBytesEffective() = %d, want %d", got, DefaultPatrolBodyLimitBytes)
	}
}

func TestPatrolEnabledAndModeNormalization(t *testing.T) {
	on := true
	cases := []struct {
		mode string
		want string
	}{
		{"", "readonly"},
		{"READONLY", "readonly"},
		{" assist ", "assist"},
		{"off", "off"},
		{"disabled", "off"},
		{"nonsense", "readonly"}, // never silently escalate
	}
	for _, tc := range cases {
		p := PatrolConfig{Enabled: &on, Mode: tc.mode}
		if !p.EnabledEffective() {
			t.Errorf("EnabledEffective() = false for explicit true")
		}
		if got := p.ModeEffective(); got != tc.want {
			t.Errorf("ModeEffective(%q) = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestPatrolAllowWriteIsExplicitOnly(t *testing.T) {
	yes, no := true, false
	if !(PatrolConfig{AllowWrite: &yes}).AllowWriteEffective() {
		t.Error("explicit true must be honored")
	}
	if (PatrolConfig{AllowWrite: &no}).AllowWriteEffective() {
		t.Error("explicit false must be honored")
	}
	// Zero values pick the documented non-zero defaults.
	p := PatrolConfig{IntervalSec: 60, BodyLimitBytes: 512}
	if got := p.IntervalSecondsEffective(); got != 60 {
		t.Errorf("IntervalSecondsEffective() = %d, want 60", got)
	}
	if got := p.BodyLimitBytesEffective(); got != 512 {
		t.Errorf("BodyLimitBytesEffective() = %d, want 512", got)
	}
}

// The settings panel rewrites the whole user config from the struct through
// RenderTOMLForScope. A section the renderer does not emit is therefore ERASED
// by the next settings save — a silent settings-loss bug. This is the guard:
// both new sections must survive a save/load round trip.
func TestDeferredAndPatrolSectionsSurviveSave(t *testing.T) {
	isolateConfigHome(t)

	on := true
	cfg := Default()
	cfg.Deferred.TriggerParentTurn = &on
	cfg.Deferred.MaxAttempts = 7
	cfg.Deferred.BodyLimitBytes = 1234
	cfg.Patrol.Enabled = &on
	cfg.Patrol.Mode = "assist"
	cfg.Patrol.AllowWrite = &on
	cfg.Patrol.IntervalSec = 120
	cfg.Patrol.Checks = []string{"git"}
	cfg.Patrol.BodyLimitBytes = 2048

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	got, err := LoadForEditStrict(path)
	if err != nil {
		t.Fatalf("LoadForEditStrict: %v", err)
	}

	if got.Deferred.TriggerParentTurn == nil || !*got.Deferred.TriggerParentTurn {
		t.Error("[deferred] trigger_parent_turn was lost on save")
	}
	if got.Deferred.MaxAttempts != 7 {
		t.Errorf("[deferred] max_attempts = %d, want 7", got.Deferred.MaxAttempts)
	}
	if got.Deferred.BodyLimitBytes != 1234 {
		t.Errorf("[deferred] body_limit_bytes = %d, want 1234", got.Deferred.BodyLimitBytes)
	}
	if !got.Patrol.EnabledEffective() {
		t.Error("[patrol] enabled was lost on save")
	}
	if got.Patrol.ModeEffective() != "assist" {
		t.Errorf("[patrol] mode = %q, want assist", got.Patrol.ModeEffective())
	}
	if !got.Patrol.AllowWriteEffective() {
		t.Error("[patrol] allow_write was lost on save")
	}
	if got.Patrol.IntervalSecondsEffective() != 120 {
		t.Errorf("[patrol] interval = %d, want 120", got.Patrol.IntervalSecondsEffective())
	}
	if len(got.Patrol.Checks) != 1 || got.Patrol.Checks[0] != "git" {
		t.Errorf("[patrol] checks = %v, want [git]", got.Patrol.Checks)
	}
	if got.Patrol.BodyLimitBytesEffective() != 2048 {
		t.Errorf("[patrol] body_limit_bytes = %d, want 2048", got.Patrol.BodyLimitBytesEffective())
	}
}
