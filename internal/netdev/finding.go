package netdev

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

// Findings (NETDEV_SPEC §10.2): the diagnostic hand's conclusions, each with
// the evidence (command outputs already redacted) that supports it. Findings
// are the unit the UI renders as Finding cards and the human reviews first —
// a finding without evidence is not a finding.

// Severities.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Evidence is one command-output pair backing a finding.
type Evidence struct {
	Device  string `json:"device"`
	Command string `json:"command"`
	Output  string `json:"output"` // redacted excerpt
}

// Finding is one diagnosis conclusion.
type Finding struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Severity   string     `json:"severity"`
	Devices    []string   `json:"devices"`
	Detail     string     `json:"detail"`
	Evidence   []Evidence `json:"evidence"`
	Suggestion string     `json:"suggestion,omitempty"` // what change to draft (netdev_propose), if any
	CreatedAt  time.Time  `json:"created_at"`
	// Source marks the automatic origin ("alert:<rule>:<device>",
	// "syslog:<device>:<class>") — auto-findings dedup and auto-resolve by it;
	// human/AI findings leave it empty.
	Source string `json:"source,omitempty"`
	// Status is the alert lifecycle: "" (human/AI finding) | active | resolved.
	Status     string     `json:"status,omitempty"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	// Project is the site-scope snapshot stamped at save time: the project
	// whose groups own the most of this finding's devices (ProjectForDevices).
	// "" = 未分组 — such findings (unknown-source syslog, "(all)" sweeps,
	// pre-Project legacy files) stay visible in EVERY project view so the
	// blind-spot rule holds: alerts that match no project are never hidden.
	// Stamped only when empty — a re-save keeps the original stamp; group
	// membership is a view concern, the stamp is audit history.
	Project string `json:"project,omitempty"`
}

var (
	findingsMu       sync.Mutex
	findingsDirOverr string
)

// FindingsDir stores findings as one JSON per finding.
func FindingsDir() string {
	if findingsDirOverr != "" {
		return findingsDirOverr
	}
	return filepath.Join(netdevStateDir(), "findings")
}

func findingValid(f *Finding) error {
	f.Title = strings.TrimSpace(f.Title)
	if f.Title == "" {
		return fmt.Errorf("finding: title is required")
	}
	switch f.Severity {
	case "", SeverityInfo, SeverityWarning, SeverityCritical:
		if f.Severity == "" {
			f.Severity = SeverityInfo
		}
	default:
		return fmt.Errorf("finding: severity must be info|warning|critical")
	}
	if len(f.Evidence) == 0 {
		return fmt.Errorf("finding: evidence is required — attach the command outputs that support the conclusion")
	}
	return nil
}

// loadConfigForProject is the config source for save-time stamping and
// list-time backfill; tests swap it. A load failure just leaves the stamp
// empty ("" = 未分组, visible everywhere) — never blocks the finding itself.
var loadConfigForProject = func() *config.Config {
	c, err := config.Load()
	if err != nil {
		return nil
	}
	return c
}

// ProjectForDevices maps a device list to the owning project by group
// membership: the project whose groups contain the most of the listed
// devices wins; ties go to config order. Ungrouped devices count as the
// "未分组" group (mirroring the frontend's inScope bucket). Pseudo-devices
// ("(all)", "(unknown)", "(cve-feed)") and empty lists match nothing → "".
func ProjectForDevices(cfg *config.Config, devices []string) string {
	if cfg == nil || len(devices) == 0 || len(cfg.NetDev.Projects) == 0 {
		return ""
	}
	groupOf := make(map[string]string, len(cfg.NetDev.Devices))
	for _, d := range cfg.NetDev.Devices {
		g := strings.TrimSpace(d.Group)
		if g == "" {
			g = "未分组"
		}
		groupOf[d.Name] = g
	}
	best, bestN := "", 0
	for _, p := range cfg.NetDev.Projects {
		gs := make(map[string]bool, len(p.Groups))
		for _, g := range p.Groups {
			if g = strings.TrimSpace(g); g != "" {
				gs[g] = true
			}
		}
		n := 0
		for _, name := range devices {
			if gs[groupOf[name]] {
				n++
			}
		}
		if n > bestN {
			best, bestN = p.Name, n
		}
	}
	return best
}

// DismissFinding deletes one finding by id — the findings queue's per-item ×.
// An unknown id is an error so the UI can tell "already gone" from "deleted".
func DismissFinding(id string) error {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, `/\`+string(filepath.Separator)) {
		return fmt.Errorf("finding: invalid id")
	}
	StateEventSnap(StateEventFindDismiss, id, StateActorUser, filepath.Join(FindingsDir(), id+".json"))
	findingsMu.Lock()
	defer findingsMu.Unlock()
	if err := os.Remove(filepath.Join(FindingsDir(), id+".json")); err != nil {
		return err
	}
	return nil
}

// ClearFindings deletes every persisted finding and returns how many — the
// queue's "clear all" (double-confirmed in the UI). Dev/test droppings are the
// main payload; real queues rebuild from the next inspection run.
func ClearFindings() (int, error) {
	findingsMu.Lock()
	defer findingsMu.Unlock()
	entries, err := os.ReadDir(FindingsDir())
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	var snapPaths []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		snapPaths = append(snapPaths, filepath.Join(FindingsDir(), e.Name()))
	}
	if len(snapPaths) > 0 {
		StateEventSnap(StateEventFindClear, fmt.Sprintf("%d findings", len(snapPaths)), StateActorUser, snapPaths...)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if err := os.Remove(filepath.Join(FindingsDir(), e.Name())); err == nil {
			n++
		}
	}
	return n, nil
}

// SaveFinding validates and persists one finding.
func SaveFinding(f *Finding) error {
	if err := findingValid(f); err != nil {
		return err
	}
	// Stamp the project snapshot when absent (see Finding.Project). Stamped
	// before the notify defer fires, so the notification text carries it too.
	if f.Project == "" {
		if cfg := loadConfigForProject(); cfg != nil {
			f.Project = ProjectForDevices(cfg, f.Devices)
		}
	}
	defer notifyFindingAsync(f) // §5.2 通知出口：配置了 webhook 且严重度过线才发
	findingsMu.Lock()
	defer findingsMu.Unlock()
	if f.ID == "" {
		// Collision-safe: the %10000 suffix can collide on back-to-back saves
		// (seen in CI) — re-roll until the file name is free.
		for {
			f.ID = fmt.Sprintf("F%s-%d", time.Now().Format("20060102"), time.Now().UnixNano()%10000)
			if _, err := os.Stat(filepath.Join(FindingsDir(), f.ID+".json")); os.IsNotExist(err) {
				break
			}
		}
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now()
	}
	if err := os.MkdirAll(FindingsDir(), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(FindingsDir(), f.ID+".json"), b, 0o600)
}

// ListFindings returns findings newest-first.
func ListFindings() ([]*Finding, error) {
	entries, err := os.ReadDir(FindingsDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Legacy files predate the Project stamp: backfill in-memory with the
	// CURRENT mapping (files stay untouched — a read must not rewrite
	// history). The config is loaded lazily, only when a backfill is needed.
	var cfg *config.Config
	var cfgLoaded bool
	backfill := func(f *Finding) {
		if f.Project != "" {
			return
		}
		if !cfgLoaded {
			cfg, cfgLoaded = loadConfigForProject(), true
		}
		f.Project = ProjectForDevices(cfg, f.Devices)
	}
	var out []*Finding
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(FindingsDir(), e.Name()))
		if err != nil {
			continue
		}
		var f Finding
		if json.Unmarshal(b, &f) == nil {
			backfill(&f)
			out = append(out, &f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
