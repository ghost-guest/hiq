package patrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/fairpeer/internal/deferred"
)

// Mode is the user-configurable autonomy level. See the package doc.
type Mode string

const (
	// ModeOff disables patrol entirely.
	ModeOff Mode = "off"
	// ModeReadOnly inspects and reports; it never writes and never starts a
	// turn on its own.
	ModeReadOnly Mode = "readonly"
	// ModeAssist lets a fresh signal start a turn so the agent can investigate.
	ModeAssist Mode = "assist"
)

// ParseMode normalizes a config string. Anything unrecognized falls back to the
// safe level (read-only), never to assist.
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "false", "none", "disabled":
		return ModeOff
	case "assist", "act", "write", "auto":
		return ModeAssist
	case "readonly", "read-only", "notify", "on", "true", "report":
		return ModeReadOnly
	default:
		return ModeReadOnly
	}
}

// Severity ranks a finding. It is a small string so it round-trips through logs
// and the deferred store without a lookup table.
const (
	SevInfo = "info"
	SevWarn = "warn"
	SevHigh = "high"
)

// severityRank orders severities for the "is this worth a turn" decision.
func severityRank(s string) int {
	switch s {
	case SevHigh:
		return 3
	case SevWarn:
		return 2
	default:
		return 1
	}
}

// Target is one session to patrol.
type Target struct {
	// SessionPath is where the deferred result is filed. Required.
	SessionPath string
	// Root is the directory to inspect (usually the session's workspace root).
	Root string
	// Label names the target in the report (repo name, project name).
	Label string
}

func (t Target) usable() bool {
	return strings.TrimSpace(t.SessionPath) != "" && strings.TrimSpace(t.Root) != ""
}

// Finding is one signal an inspector surfaced.
type Finding struct {
	// ID is stable for the kind of signal (e.g. "git.dirty"), so the manager
	// can remember "we already said this".
	ID string
	// Kind groups findings ("git", "workspace").
	Kind string
	// Severity is SevInfo / SevWarn / SevHigh.
	Severity string
	// Summary is one line fit for a notice.
	Summary string
	// Detail is optional richer context for the model.
	Detail string
	// Paths lists the files involved (bounded by the inspector).
	Paths []string
	// Fingerprint changes whenever the finding's substance changes. Two
	// findings with the same ID and Fingerprint are the same news.
	Fingerprint string
}

// Result is what one inspector returns for one target in one tick. State is an
// opaque per-inspector blob carried to the next tick (e.g. a previous tree
// fingerprint); the manager stores it without interpreting it.
type Result struct {
	Findings []Finding
	State    string
}

// Inspector examines one target. Implementations MUST be read-only: patrol's
// contract is that it never changes the workspace by itself.
type Inspector interface {
	// Name identifies the inspector in the dedupe key and in logs.
	Name() string
	// Inspect returns findings plus the state to carry forward. prevState is
	// the value this inspector returned last tick ("" on the first tick).
	Inspect(ctx context.Context, t Target, prevState string) Result
}

// Deliverer is the sink patrol hands reports to. deferred.Coordinator
// satisfies it, which is the whole point: patrol reuses the durable delivery
// path instead of inventing one.
type Deliverer interface {
	Enqueue(sessionPath string, t deferred.Task) error
}

// Options configures a Manager. Zero values pick the documented defaults.
type Options struct {
	// Mode is the autonomy level. Off (the zero value) makes Start a no-op.
	Mode Mode
	// AllowWrite is the extra ceiling for assist mode. Ignored otherwise.
	AllowWrite bool
	// Interval is the patrol cadence. 0 → DefaultInterval.
	Interval time.Duration
	// Inspectors run in order on every tick. Empty → DefaultInspectors.
	Inspectors []Inspector
	// Targets supplies the sessions to patrol. Called once per tick, so it can
	// follow session rebinds; nil means there is nothing to patrol.
	Targets func() []Target
	// Deliverer receives reports. Nil disables delivery (findings are counted
	// and logged only) — used by tests and by a UI that wants a dry run.
	Deliverer Deliverer
	// Logf is a best-effort logger. Nil discards.
	Logf func(string, ...any)
	// Now is the clock, injectable for tests. Nil → time.Now.
	Now func() time.Time
	// BodyLimit truncates a report body (bytes). 0 → DefaultBodyLimit.
	BodyLimit int
}

// Defaults for Options.
const (
	DefaultInterval  = 15 * time.Minute
	DefaultBodyLimit = 4000
)

// DefaultInspectors is the read-only inspection set patrol runs when the caller
// does not supply one.
func DefaultInspectors() []Inspector {
	return []Inspector{GitInspector{}, MarkerInspector{}}
}

// Manager drives the patrol loop. Safe for concurrent use.
type Manager struct {
	opts Options

	mu        sync.Mutex
	seen      map[string]string // dedupeKey → last fingerprint
	baselined map[string]bool   // sessionPath → first pass already taken
	running   bool
	closed    bool
	stopCh    chan struct{}
	doneCh    chan struct{}
	passes    int
	fired     int
}

// New builds a Manager. Call Start to begin ticking.
func New(opts Options) *Manager {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.BodyLimit <= 0 {
		opts.BodyLimit = DefaultBodyLimit
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if len(opts.Inspectors) == 0 {
		opts.Inspectors = DefaultInspectors()
	}
	return &Manager{
		opts:      opts,
		seen:      map[string]string{},
		baselined: map[string]bool{},
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// Mode reports the configured autonomy level.
func (m *Manager) Mode() Mode { return m.opts.Mode }

// Running reports whether the loop is ticking.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// Stats reports liveness for a panel: completed passes, reports fired.
func (m *Manager) Stats() (passes, fired int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.passes, m.fired
}

// Start launches the ticker. It is a no-op when mode is off or it is already
// running, so callers can wire it unconditionally.
func (m *Manager) Start() {
	if m.opts.Mode == ModeOff {
		return
	}
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	stop := m.stopCh
	m.mu.Unlock()

	go func() {
		defer close(m.doneCh)
		t := time.NewTicker(m.opts.Interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				m.Tick(context.Background())
			}
		}
	}()
}

// Close stops the ticker and waits for the loop to exit. Safe to call twice.
//
// It may block for as long as the in-flight Tick takes (a git probe is bounded
// by the inspector timeout), so a caller that must stay responsive — boot
// retiring a previous build's manager — should run it on its own goroutine.
// Once Close returns, Tick is inert: see the closed flag.
func (m *Manager) Close() {
	m.mu.Lock()
	if !m.running {
		m.closed = true
		m.mu.Unlock()
		return
	}
	m.running = false
	m.closed = true
	stop := m.stopCh
	m.mu.Unlock()
	close(stop)
	<-m.doneCh
}

// Tick runs one patrol pass over every target and returns how many reports it
// enqueued. Exported so a UI can offer "check now" and so tests can drive the
// loop deterministically.
//
// A closed manager is inert: a tick that was queued just before Close can still
// be running, and it must not push a report with the permissions the user just
// replaced.
func (m *Manager) Tick(ctx context.Context) int {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed || m.opts.Mode == ModeOff || m.opts.Targets == nil {
		return 0
	}
	targets := m.opts.Targets()

	fired := 0
	for _, t := range targets {
		if !t.usable() {
			continue
		}
		fresh := m.inspect(ctx, t)
		if len(fresh) == 0 {
			continue
		}
		if m.report(ctx, t, fresh) {
			fired++
		}
	}

	m.mu.Lock()
	m.passes++
	m.fired += fired
	m.mu.Unlock()
	return fired
}

// inspect runs every inspector over one target, updates the carry-forward
// state, and returns only the findings whose fingerprint changed since the last
// time that (target, inspector, finding id) was seen.
//
// The first pass for a target is a baseline: it records everything but speaks
// up only about high-severity truths (a tree already full of merge conflicts is
// worth saying; a pre-existing TODO is the standing state of the world, not
// news). Later passes report any change.
func (m *Manager) inspect(ctx context.Context, t Target) []Finding {
	first := m.markBaselined(t)
	var fresh []Finding
	for _, ins := range m.opts.Inspectors {
		prev := m.state(t, ins.Name())
		res := ins.Inspect(ctx, t, prev)
		m.setState(t, ins.Name(), res.State)
		for _, f := range res.Findings {
			key := dedupeKey(t, ins.Name(), f)
			if m.seenFingerprint(key) == f.Fingerprint {
				continue // same news as last tick
			}
			m.remember(key, f.Fingerprint)
			if first && severityRank(f.Severity) < severityRank(SevHigh) {
				continue // baseline: recorded, not reported
			}
			fresh = append(fresh, f)
		}
	}
	return fresh
}

// markBaselined reports whether this is the first pass for the target, and
// records that it has now happened.
func (m *Manager) markBaselined(t Target) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.baselined[t.SessionPath] {
		return false
	}
	m.baselined[t.SessionPath] = true
	return true
}

// report renders the fresh findings as one deferred task and hands it to the
// deliverer. It returns whether a report was enqueued.
func (m *Manager) report(_ context.Context, t Target, fresh []Finding) bool {
	if m.opts.Deliverer == nil {
		m.opts.Logf("patrol: %d finding(s) for %s (no deliverer)", len(fresh), t.Label)
		return false
	}
	// Highest severity leads the decision: a high signal may be worth a turn
	// in assist mode even if it arrived alongside minor ones.
	worst := 0
	for _, f := range fresh {
		if r := severityRank(f.Severity); r > worst {
			worst = r
		}
	}

	// Read-only mode never spends a turn. Assist mode may, but only for a
	// signal that actually deserves one — an all-info batch still just rides
	// the next turn's context, so patrol cannot burn the budget on trivia.
	intent := deferred.IntentNotifyUIOnly
	if m.opts.Mode == ModeAssist && worst >= severityRank(SevWarn) {
		intent = deferred.IntentTriggerTurn
	}

	title := fmt.Sprintf("patrol: %d signal(s) in %s", len(fresh), displayLabel(t))
	body := clampBody(m.renderBody(t, fresh), m.opts.BodyLimit)

	task := deferred.Task{
		// A fresh report is a distinct event, not a re-send of an old one, so
		// the id carries a nonce: repeat signals that recur after a change must
		// deliver again, which a content-stable id would block (the store treats
		// a re-put of a delivered id as already handled).
		ID:          reportID(t, fresh, m.opts.Now()),
		SessionPath: t.SessionPath,
		Source:      "patrol",
		Title:       title,
		Body:        body,
		Status:      deferred.Resolved,
		Meta: deferred.Meta{
			Intent: intent,
			Kind:   "patrol",
			Label:  displayLabel(t),
		},
	}
	if err := m.opts.Deliverer.Enqueue(t.SessionPath, task); err != nil {
		m.opts.Logf("patrol: enqueue for %s: %v", t.SessionPath, err)
		return false
	}
	return true
}

// renderBody formats the findings for the model, ordered most severe first, and
// appends the instruction that matches the configured permission.
func (m *Manager) renderBody(t Target, fresh []Finding) string {
	ordered := make([]Finding, len(fresh))
	copy(ordered, fresh)
	sort.SliceStable(ordered, func(i, j int) bool {
		return severityRank(ordered[i].Severity) > severityRank(ordered[j].Severity)
	})

	var b strings.Builder
	for _, f := range ordered {
		fmt.Fprintf(&b, "- [%s] %s\n", f.Severity, f.Summary)
		if f.Detail != "" {
			for _, line := range strings.Split(strings.TrimRight(f.Detail, "\n"), "\n") {
				if line = strings.TrimRight(line, " \t"); line != "" {
					b.WriteString("    " + line + "\n")
				}
			}
		}
		if len(f.Paths) > 0 {
			b.WriteString("    affected: " + strings.Join(f.Paths, ", ") + "\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(m.instruction(t))
	return b.String()
}

// instruction is the directive that matches the configured permission. It is
// the human-readable half of the permission dial: the mode and allow_write
// settings decide what patrol is allowed to ask for.
func (m *Manager) instruction(t Target) string {
	switch m.opts.Mode {
	case ModeAssist:
		if m.opts.AllowWrite {
			return fmt.Sprintf("This is an assist-mode patrol report for %s. Investigate the signals above and fix the ones that clearly need fixing, using the normal tool permissions. Keep changes scoped to what the signals point at.", displayLabel(t))
		}
		return fmt.Sprintf("This is an assist-mode patrol report for %s. Investigate the signals above and report what you find. Do not modify files unless the user asks — patrol is not permitted to write here.", displayLabel(t))
	default:
		return fmt.Sprintf("This is a read-only patrol report for %s, produced automatically while the user was away. Surface anything important to the user in one short line each. Do not start unrelated work and do not modify files.", displayLabel(t))
	}
}

func displayLabel(t Target) string {
	l := strings.TrimSpace(t.Label)
	if l == "" {
		return "workspace"
	}
	return l
}

// reportID mints an id unique to this report. See the comment at the call site.
func reportID(t Target, fresh []Finding, now time.Time) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00", t.SessionPath)
	for _, f := range fresh {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", f.Kind, f.ID, f.Fingerprint)
	}
	fmt.Fprintf(h, "%d", now.UnixNano())
	sum := hex.EncodeToString(h.Sum(nil))
	return "patrol:" + sum[:16]
}

func dedupeKey(t Target, inspector string, f Finding) string {
	return t.SessionPath + "\x00" + inspector + "\x00" + f.ID
}

func (m *Manager) state(t Target, inspector string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen["state\x00"+dedupeKey(t, inspector, Finding{})]
}

func (m *Manager) setState(t Target, inspector, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen["state\x00"+dedupeKey(t, inspector, Finding{})] = state
}

func (m *Manager) seenFingerprint(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen[key]
}

func (m *Manager) remember(key, fingerprint string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen[key] = fingerprint
}

// ForgetTarget drops the remembered state for one session (used when a session
// closes, so a later session at the same path starts clean).
func (m *Manager) ForgetTarget(sessionPath string) {
	prefix := sessionPath + "\x00"
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.seen {
		if strings.HasPrefix(k, prefix) || strings.HasPrefix(k, "state\x00"+prefix) {
			delete(m.seen, k)
		}
	}
	delete(m.baselined, sessionPath)
}

// clampBody keeps the head and tail of an oversized report with a marker
// between them, and never returns more than limit bytes (the marker is charged
// against the budget, not added on top of it).
func clampBody(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	marker := fmt.Sprintf("\n... (truncated %d bytes) ...\n", len(s)-limit)
	if len(marker) >= limit {
		return s[:limit]
	}
	remaining := limit - len(marker)
	head := remaining * 2 / 3
	tail := remaining - head
	return s[:head] + marker + s[len(s)-tail:]
}
