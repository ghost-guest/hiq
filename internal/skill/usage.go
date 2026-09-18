package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// usageStateName is the JSON file recording per-skill usage, written beside
// dream_state.json (the parent of the session dir, i.e. the .hiq/ root).
// Like dream_state.json it exists because skill usage is a cross-session
// concern that must outlive any single agent run.
const usageStateName = "skill_usage.json"

// usageHistory caps how many entries we keep, keeping the file tiny even if a
// user churns through many transient skills over time.
const usageHistory = 200

// UsageTracker records the last-used timestamp of each invoked skill, so
// long-unused skills can be retired from the prompt index (mirroring memory's
// dormant/archive decay). It is best-effort throughout: a missing or corrupt
// state file is not an error — the tracker simply reports "never used", so no
// skill is retired until real usage data accumulates.
//
// The zero value (path == "") is a no-op: Record/LastUsed/ColdSkillNames all
// return without touching disk, so callers that don't pass a StateDir (tests,
// older code paths) are unaffected.
type UsageTracker struct {
	path       string
	legacyPath string // pre-profile-partition fallback; read when path is absent
	mu         sync.Mutex
}

type usageFile struct {
	Skills map[string]usageEntry `json:"skills"` // key = canonical skill name
}

type usageEntry struct {
	LastUsed string `json:"last_used"` // RFC3339; "" = unknown
	Count    int    `json:"count"`     // total invocations
}

// NewUsageTracker builds a tracker backed by <stateDir>/skill_usage.json.
// A "" stateDir returns a no-op tracker (all methods are safe but do nothing).
// legacyPath, when non-empty, is read as a fallback when the primary file is
// absent — used to recover pre-profile-partition skill usage (the old file sat
// at <userDir>/skill_usage.json; sessions then moved under <profile>/sessions,
// shifting the state dir by one level).
func NewUsageTracker(stateDir string, legacyPath ...string) *UsageTracker {
	if stateDir == "" {
		return &UsageTracker{}
	}
	t := &UsageTracker{path: filepath.Join(stateDir, usageStateName)}
	if len(legacyPath) > 0 && legacyPath[0] != "" && legacyPath[0] != t.path {
		t.legacyPath = legacyPath[0]
	}
	return t
}

// Record logs one invocation of name: bumps the count and stamps LastUsed to
// now, then writes the file. Best-effort — a write failure is swallowed because
// losing one usage sample must never break the skill call that triggered it.
func (u *UsageTracker) Record(name string) {
	if u == nil || u.path == "" || name == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()

	st := u.loadLocked()
	if st.Skills == nil {
		st.Skills = map[string]usageEntry{}
	}
	e := st.Skills[name]
	e.LastUsed = time.Now().UTC().Format(time.RFC3339)
	e.Count++
	st.Skills[name] = e
	u.trimLocked(&st)
	u.storeLocked(st)
}

// LastUsed returns the most recent invocation time of name, or the zero Time
// when the skill has never been recorded (or tracking is disabled).
func (u *UsageTracker) LastUsed(name string) time.Time {
	if u == nil || u.path == "" || name == "" {
		return time.Time{}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	st := u.loadLocked()
	e, ok := st.Skills[name]
	if !ok || e.LastUsed == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, e.LastUsed)
	return t
}

// ColdSkillNames returns the names of skills whose last use is older than
// threshold (or that have never been recorded, when includeNeverUsed is true).
// A skill used within the threshold is excluded. The never-used policy is
// configurable: built-in skills (explore/research/…) should not be retired
// just because they haven't been called yet, so callers pass includeNeverUsed
// only for user-authored skills.
func (u *UsageTracker) ColdSkillNames(threshold time.Duration, includeNeverUsed bool, known []string) []string {
	if u == nil || u.path == "" || threshold <= 0 {
		return nil
	}
	u.mu.Lock()
	st := u.loadLocked()
	u.mu.Unlock()

	now := time.Now()
	var cold []string
	for _, name := range known {
		e, ok := st.Skills[name]
		if !ok || e.LastUsed == "" {
			if includeNeverUsed {
				cold = append(cold, name)
			}
			continue
		}
		t, err := time.Parse(time.RFC3339, e.LastUsed)
		if err != nil {
			continue // malformed timestamp: don't retire on garbage
		}
		if now.Sub(t) > threshold {
			cold = append(cold, name)
		}
	}
	return cold
}

// loadLocked reads the state file. A missing/corrupt file yields an empty
// usageFile — never an error, so a fresh workspace or a hand-deleted file
// behaves as "nothing used yet".
func (u *UsageTracker) loadLocked() usageFile {
	var st usageFile
	if u.path == "" {
		return st
	}
	b, err := os.ReadFile(u.path)
	if err != nil && u.legacyPath != "" {
		// Fallback to the pre-profile-partition location: sessions moved under
		// <profile>/sessions shifted the state dir's parent by one level,
		// orphaning the old skill_usage.json. Recover it so cold-skill
		// detection and usage counts survive the upgrade.
		if lb, lerr := os.ReadFile(u.legacyPath); lerr == nil {
			err = nil
			b = lb
		}
	}
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st) // corrupt → empty; best-effort
	return st
}

func (u *UsageTracker) storeLocked(st usageFile) {
	if u.path == "" {
		return
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(u.path, b, 0o644)
}

// trimLocked caps the entry count, evicting the oldest LastUsed first so the
// file stays bounded even as distill keeps adding skills. Built-in skills are
// exempt from eviction (they're cheap to re-track).
func (u *UsageTracker) trimLocked(st *usageFile) {
	if len(st.Skills) <= usageHistory {
		return
	}
	type kv struct {
		name  string
		stamp time.Time
	}
	items := make([]kv, 0, len(st.Skills))
	for name, e := range st.Skills {
		t, _ := time.Parse(time.RFC3339, e.LastUsed)
		items = append(items, kv{name, t})
	}
	// Partial sort: move the (len - usageHistory) oldest to the front, drop them.
	target := len(items) - usageHistory
	if target < 1 {
		return
	}
	// Simple selection of oldest N by timestamp.
	for i := 0; i < target && i < len(items); i++ {
		min := i
		for j := i + 1; j < len(items); j++ {
			if items[j].stamp.Before(items[min].stamp) {
				min = j
			}
		}
		items[i], items[min] = items[min], items[i]
		delete(st.Skills, items[i].name)
	}
}
