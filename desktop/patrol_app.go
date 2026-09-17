package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/zzycxz/fairpeer/internal/boot"
	"github.com/zzycxz/fairpeer/internal/config"
)

// PatrolView is the settings-panel projection of [patrol]: the permission dial
// the user controls, plus live status so the panel can show that the heartbeat
// is genuinely running (and how much it has reported).
//
// Running/Passes/Fired are runtime-only (they come from the live manager, not
// from config); everything else is persisted.
type PatrolView struct {
	Enabled     bool     `json:"enabled"`
	Mode        string   `json:"mode"`
	AllowWrite  bool     `json:"allowWrite"`
	IntervalSec int      `json:"intervalSec"`
	Checks      []string `json:"checks"`
	Running     bool     `json:"running"`
	Passes      int      `json:"passes"`
	Fired       int      `json:"fired"`
}

// patrolView projects the config plus the live manager state.
func patrolView(cfg *config.Config) PatrolView {
	p := cfg.Patrol
	v := PatrolView{
		Enabled:     p.EnabledEffective(),
		Mode:        p.ModeEffective(),
		AllowWrite:  p.AllowWriteEffective(),
		IntervalSec: p.IntervalSecondsEffective(),
		Checks:      nonNil(append([]string{}, p.Checks...)),
	}
	if m := boot.PatrolManager(); m != nil {
		v.Running = m.Running()
		v.Passes, v.Fired = m.Stats()
	}
	return v
}

// patrolViewWhenUnreadable is the panel's fallback when the config cannot be
// loaded: it shows the safe defaults rather than pretending the feature is on.
func patrolViewWhenUnreadable() PatrolView {
	return PatrolView{
		Mode:        "readonly",
		IntervalSec: config.DefaultPatrolIntervalSec,
		Checks:      []string{},
	}
}

// SetPatrol updates the patrol permission dial and cadence. The two axes are
// explicit in the signature so the panel cannot raise autonomy by accident:
// mode selects off/readonly/assist, and allowWrite is the extra write ceiling
// that only assist honors.
//
// allow_write is cleared when the mode is not assist: persisting "may write"
// alongside a mode that never acts would leave a loaded gun behind the dial if
// the mode were later raised.
func (a *App) SetPatrol(enabled bool, mode string, allowWrite bool, intervalSec int, checks []string) error {
	canon, err := canonicalPatrolMode(mode)
	if err != nil {
		return err
	}
	if intervalSec < 0 {
		return fmt.Errorf("patrol interval must not be negative")
	}
	return a.applyConfigChange(func(c *config.Config) error {
		c.Patrol.Enabled = &enabled
		c.Patrol.Mode = canon
		if canon == "assist" {
			c.Patrol.AllowWrite = &allowWrite
		} else {
			off := false
			c.Patrol.AllowWrite = &off
		}
		if intervalSec > 0 {
			c.Patrol.IntervalSec = intervalSec
		}
		c.Patrol.Checks = normalizePatrolChecks(checks)
		return nil
	})
}

// PatrolCheckNow runs one patrol pass immediately and returns how many reports
// it produced, so the user can verify the heartbeat (and see it work) without
// waiting a whole interval. It is read-only, exactly like a scheduled tick.
func (a *App) PatrolCheckNow() (int, error) {
	m := boot.PatrolManager()
	if m == nil {
		return 0, fmt.Errorf("patrol is off — enable it before running a check")
	}
	return m.Tick(context.Background()), nil
}

// canonicalPatrolMode validates the mode the panel sends. Unknown values are
// rejected rather than coerced: silently downgrading a typo to readonly would
// hide a UI bug behind a working-looking feature.
func canonicalPatrolMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "off":
		return "off", nil
	case "", "readonly":
		return "readonly", nil
	case "assist":
		return "assist", nil
	default:
		return "", fmt.Errorf("unknown patrol mode %q (want off|readonly|assist)", mode)
	}
}

// normalizePatrolChecks keeps only the inspector names we actually have, so a
// stale entry in the UI cannot produce an inspector that silently does nothing.
func normalizePatrolChecks(checks []string) []string {
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		switch strings.ToLower(strings.TrimSpace(c)) {
		case "git":
			out = append(out, "git")
		case "markers":
			out = append(out, "markers")
		}
	}
	return out
}
