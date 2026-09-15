package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/fairpeer/internal/config"
	"github.com/zzycxz/fairpeer/internal/event"
	"github.com/zzycxz/fairpeer/internal/provider"
	"github.com/zzycxz/fairpeer/internal/tool"
)

// minSpawnGap prevents rapid-fire automatic dream/distill triggers within a
// single session (e.g. several turns fired in quick succession). It only
// applies to the automatic path; manual triggers are gated by inFlight instead.
const minSpawnGap = 10 * time.Second

// dreamTimeout / distillTimeout bound the background sub-agent runs.
const (
	dreamTimeout   = 5 * time.Minute
	distillTimeout = 10 * time.Minute
)

// dreamStateName is the JSON file recording Dream/Distill run history, written
// in the workspace's .fairpeer/ directory (the parent of sessionDir). It exists
// because the sub-agents spawned here reuse the parent session in memory and
// never persist their own .jsonl transcript — so the previous design of scanning
// sessions/*.jsonl.meta for a topicTitle marker could never match (nothing was
// ever written). This dedicated state file is the single source of truth for
// "when did Dream/Distill last run" and the cadence gate.
const dreamStateName = "dream_state.json"

// dreamState caps how many run records we keep per kind, keeping the file tiny.
const dreamStateHistory = 20

// DreamKind identifies which self-evolution agent a record describes.
type DreamKind string

const (
	KindDream   DreamKind = "dream"
	KindDistill DreamKind = "distill"
)

// DreamTrigger records how a run was initiated.
type DreamTrigger string

const (
	TriggerAuto   DreamTrigger = "auto"
	TriggerManual DreamTrigger = "manual"
)

// DreamRun is one completed (or failed) Dream/Distill invocation.
type DreamRun struct {
	Kind      DreamKind    `json:"kind"`
	Trigger   DreamTrigger `json:"trigger"`
	StartedAt time.Time    `json:"started_at"`
	Duration  string       `json:"duration,omitempty"`
	Status    string       `json:"status"`             // "ok" | "error" | "timeout"
	Error     string       `json:"error,omitempty"`    // set when status != ok
	Memories  int          `json:"memories,omitempty"` // best-effort count when discoverable
}

// dreamStateFile is the on-disk shape.
type dreamStateFile struct {
	Runs []DreamRun `json:"runs"`
}

// spawnCoordinator serialises Dream/Distill spawning within one process. The
// previous design used package-level time.Time vars with no lock, which raced
// once manual triggers (from the desktop UI) and automatic triggers (from the
// turn loop) could fire concurrently. inFlight is the hard concurrency gate
// (only one Dream and one Distill run at a time, per kind); lastAuto is the
// automatic-path debounce that replaces minSpawnGap's stateful side effect.
//
// The cadence decision itself NEVER consults this state — it reads the on-disk
// dream_state.json so a manual run cannot perturb the automatic schedule.
type spawnCoordinator struct {
	mu       sync.Mutex
	inFlight map[DreamKind]bool
	lastAuto map[DreamKind]time.Time
}

var dreamCoord = &spawnCoordinator{
	inFlight: make(map[DreamKind]bool),
	lastAuto: make(map[DreamKind]time.Time),
}

// distillCompleteHook is the post-distill callback boot.go installs to retire
// cold skills (hard-decay skills unused longer than the configured threshold).
// It returns a status string the caller may log. Guarded by dreamCoord.mu so a
// concurrent RegisterDistillComplete can't race the read in notifyDistillComplete.
// nil = no callback (dream disabled, or boot hasn't wired one yet).
var distillCompleteHook func() string

// RegisterDistillComplete installs the callback fired after a successful Distill
// run. boot.go passes a closure that retires cold skills; passing nil disables
// it (e.g. when [dream] is off). Safe to call at any time; the next Distill run
// picks up the new hook.
func RegisterDistillComplete(fn func() string) {
	dreamCoord.mu.Lock()
	distillCompleteHook = fn
	dreamCoord.mu.Unlock()
}

// notifyDistillComplete fires the registered hook (if any) after a Distill run
// completes, returning whatever status string the hook produced. Empty when no
// hook is registered or the hook returned nothing.
func notifyDistillComplete() string {
	dreamCoord.mu.Lock()
	fn := distillCompleteHook
	dreamCoord.mu.Unlock()
	if fn == nil {
		return ""
	}
	return fn()
}

// DreamTask is the prompt fed to a background agent for portrait consolidation.
// The dream agent MAINTAINS THE PORTRAIT FILES directly — profile/user.md,
// profile/memory.md, profile/<mode>.md — because those are the only memories
// injected into every turn. Its output must be concise, human-prose, and merged,
// not a list of scattered facts. It writes with write_file, NOT remember.
const DreamTask = `You are a memory consolidation agent. Your single job is to keep the portrait files concise and current, because they are injected into EVERY future turn — bloat here costs tokens forever.

## The portrait files

Four files hold the always-injected memory (find them under the user config dir, in a "profile/" subdirectory). They are split by WHAT the fact is about (nature), not just which mode — because a fast-changing deadline shouldn't share a file with a slow-changing identity:

- profile/user.md — WHO the user is, shared across all modes: identity, role, expertise, durable preferences, communication style, red lines. Changes slowly (months/years). Target ≤ 400 characters.
- profile/memory.md — WHAT is true about the world, shared across all modes: system environment, project locations, commonly used tools, cross-project experience, deadlines. Changes faster (days/weeks). Target ≤ 400 characters.
- profile/<mode>.md — mode-specific: coding preferences + the current coding tasks (dev), or office preferences + current office tasks (cowork). Target ≤ 300 characters.

## What to do

1. Read the portrait files (use read_file) to see what is already recorded.
2. Review the recent session history for NEW durable knowledge:
   - Who the user is, their role, expertise, communication style → user.md
   - Stable technical or working preferences ("prefers Go", "结论优先") → user.md or the mode file
   - Project-level decisions, constraints, deadlines, environment facts → memory.md or the mode file
   - The mode's current goal/task state → <mode>.md
3. MERGE the new knowledge into the right file — do NOT append bullet lists.
   - Fold a new fact into an existing line when it refines it ("prefers Go" → "prefers Go; 缩进用空格").
   - Drop or rewrite lines the session proved stale or wrong (a passed deadline, a changed decision).
   - Keep it as tight, human prose — like a memo a colleague would write, not a log.
4. Write the updated files with the write_file tool. Do NOT use the remember tool — remember is for the un-injected scattered archive; the portrait is what every turn sees.
5. If nothing material changed, write nothing. A no-op run is correct.

## Hard rules

- NEVER exceed the character targets. If a file is full, compress by merging or cutting the least-valuable line — do not grow it.
- NEVER save transient debug state (a current stack trace, file contents being inspected, today's throwaway todo) — those belong nowhere in the portrait.
- NEVER duplicate the same fact across files. Mode-specific → <mode>.md; mode-agnostic identity → user.md; mode-agnostic world fact → memory.md.
- NEVER duplicate a fact already in the file — before writing, check each line is not a restatement of an existing one (a "prefers Go" line already covers "likes Go"; merge instead of adding). If two lines say nearly the same thing, fold them into one.
- Identity facts (user.md) are the most stable — only change them on strong evidence; prefer refining over rewriting.
- RESPECT protected sections: any region wrapped in <!-- protected --> ... <!-- /protected --> tags is a user-declared red line (a core constraint, a hard preference). You may NOT modify, delete, or move text inside these tags. Write around them — update the unprotected parts only. If a file has protected regions, output them verbatim in your rewrite.
- Prefer concrete specifics over generic praise ("后端工程师，8年Go经验" not "experienced developer").`

// Portrait file size targets (characters). dreamCompactThreshold is the HARD
// trigger at which dream switches from "merge new knowledge" to "compress what's
// there". A portrait that drifts past its target but under the threshold is
// tolerated; past the threshold dream actively shrinks it.
const (
	portraitTargetUser   = 400 // user.md  — slow-changing identity
	portraitTargetMemory = 400 // memory.md — faster world facts
	portraitTargetMode   = 300 // dev.md / cowork.md — mode prefs + tasks
	// dreamCompactThreshold is the fraction of a file's target at which dream
	// switches to compression mode. 1.3 = 130%: a 400-char target compacts at
	// 520 chars.
	dreamCompactThreshold = 1.3
)

// DreamCompactTask is the prompt fed to dream when one or more portrait files
// have grown past their compact threshold. This run's PRIMARY job is to SHRINK
// the bloated files back under target — by merging redundant lines, dropping
// stale facts, and rewriting verbose prose tighter.
const DreamCompactTask = `You are a memory consolidation agent in COMPRESSION MODE. One or more portrait files have grown past their size budget, so this run's PRIMARY job is to SHRINK them back under target — not to add new content.

## Why this matters

The portrait files are injected into EVERY future turn. A bloated file means the tail gets truncated at injection, so anything past the budget is wasted disk the model never sees. Your job is to make every character count.

## What to do

1. Read the portrait files (use read_file).
2. For each file that is over budget, COMPRESS it down to its target:
   - Merge redundant or overlapping lines into one.
   - DROP facts the session proved stale — do not keep them "just in case".
   - Rewrite verbose prose tighter.
   - Keep concrete identifiers (names, paths, versions) — compress phrasing, not specificity.
3. Only AFTER compression, fold in genuinely new durable knowledge — and only if it still fits under the target.
4. Write the updated files with write_file. Do NOT use remember.

## Targets (characters, hard)

- profile/user.md ≤ 400
- profile/memory.md ≤ 400
- profile/<mode>.md ≤ 300

## Hard rules

- The post-compression file MUST be at or under its target. If you can't get there by merging, cut the least-valuable line.
- Never drop a fact to make room for a vaguer one — specifics beat generalities.
- Never duplicate a fact across files.
- If a file is already under target, leave its compression alone (you may still merge new knowledge into it).`

// dreamTaskFor picks the dream prompt for this run. It sizes the active mode's
// portrait files and returns DreamCompactTask when any has crossed its compact
// threshold, DreamTask otherwise. This makes dream self-correcting: it
// accumulates in normal runs and shrinks in compact runs. A missing userDir or
// unreadable file is treated as size 0 (under threshold).
func dreamTaskFor(profile string) string {
	userDir := config.MemoryUserDir()
	if userDir == "" {
		return DreamTask
	}
	mode := strings.ToLower(strings.TrimSpace(profile))
	if mode == "" {
		mode = "dev"
	}
	root := filepath.Join(userDir, "profile")
	type fc struct {
		path string
		tgt  int
	}
	checks := []fc{
		{filepath.Join(root, "user.md"), portraitTargetUser},
		{filepath.Join(root, "memory.md"), portraitTargetMemory},
		{filepath.Join(root, mode+".md"), portraitTargetMode},
	}
	for _, c := range checks {
		if info, err := os.Stat(c.path); err == nil {
			if float64(info.Size()) > float64(c.tgt)*dreamCompactThreshold {
				return DreamCompactTask
			}
		}
	}
	return DreamTask
}

// DistillTask is the prompt fed to a background agent for workflow extraction.
const DistillTask = `You are a workflow distillation agent. Your job is to review recent sessions and identify repeated manual workflows that could be automated.

## Instructions

1. Review recent session history for patterns where the same sequence of steps was repeated across multiple sessions.
2. For each repeated workflow:
   - Create a skill file (.md) that documents the workflow
   - Include clear step-by-step instructions
   - Reference specific tools and commands needed
   - Make it reusable across similar tasks
3. Save skills to .fairpeer/skills/ directory.
4. Focus on workflows that would save significant time if automated.

## Good candidates for skills
- Common debugging sequences (e.g., "investigate test failure" → check logs → reproduce → fix → verify)
- Project setup patterns (e.g., "add new feature" → create branch → implement → test → PR)
- Repetitive code patterns (e.g., "add new API endpoint" → handler → route → test → docs)
- Multi-step build/deploy processes

## Not good candidates
- One-off tasks unlikely to repeat
- Tasks too specific to a single bug/feature
- Simple single-tool operations`

// dreamConfig loads the live Dream config (live-load so a settings change takes
// effect on the next turn without restarting the session). A nil error with a
// zero-value config means "feature disabled / unavailable" — callers treat that
// as "do nothing".
func dreamConfig() (config.DreamConfig, bool) {
	cfg, err := config.Load()
	if err != nil {
		return config.DreamConfig{}, false
	}
	return cfg.Dream, true
}

// dreamStatePath resolves the state file location for the given session dir.
// sessionDir is .../.fairpeer/sessions; the state file lives one level up in
// .../.fairpeer/. An empty sessionDir yields "" (state disabled).
func dreamStatePath(sessionDir string) string {
	if sessionDir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(sessionDir), dreamStateName)
}

// loadDreamState reads the run history. A missing/corrupt file is not an error
// — it just means no runs recorded yet.
func loadDreamState(sessionDir string) dreamStateFile {
	var st dreamStateFile
	path := dreamStatePath(sessionDir)
	if path == "" {
		return st
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st)
	return st
}

// appendDreamRun records a run and trims to the last dreamStateHistory per kind.
// Write failures are non-fatal: the run still happened; we just lose the record.
func appendDreamRun(sessionDir string, run DreamRun) {
	path := dreamStatePath(sessionDir)
	if path == "" {
		return
	}
	st := loadDreamState(sessionDir)
	st.Runs = append(st.Runs, run)
	st.Runs = trimDreamRuns(st.Runs)
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o644)
}

// trimDreamRuns keeps only the most recent dreamStateHistory records of each
// kind, preserving chronological order.
func trimDreamRuns(runs []DreamRun) []DreamRun {
	byKind := make(map[DreamKind][]DreamRun)
	order := []DreamKind{KindDream, KindDistill}
	for _, r := range runs {
		byKind[r.Kind] = append(byKind[r.Kind], r)
	}
	var out []DreamRun
	for _, k := range order {
		all := byKind[k]
		if len(all) > dreamStateHistory {
			all = all[len(all)-dreamStateHistory:]
		}
		out = append(out, all...)
	}
	return out
}

// LastDreamRun returns the most recent recorded run of the given kind (zero
// DreamRun if none). It reads only from disk — the cadence gate uses this so a
// manual run is visible to the next automatic decision.
func LastDreamRun(sessionDir string, kind DreamKind) (DreamRun, bool) {
	st := loadDreamState(sessionDir)
	for i := len(st.Runs) - 1; i >= 0; i-- {
		if st.Runs[i].Kind == kind {
			return st.Runs[i], true
		}
	}
	return DreamRun{}, false
}

// DreamHistory returns the recorded runs of the given kind, newest first.
func DreamHistory(sessionDir string, kind DreamKind) []DreamRun {
	st := loadDreamState(sessionDir)
	var out []DreamRun
	for i := len(st.Runs) - 1; i >= 0; i-- {
		if st.Runs[i].Kind == kind {
			out = append(out, st.Runs[i])
		}
	}
	return out
}

// workspaceOldEnough reports whether the workspace's oldest session file is
// older than interval — a cold-start gate so a brand-new project doesn't fire
// consolidation before it has any history worth consolidating. A missing or
// empty sessions directory returns false (nothing to consolidate yet).
func workspaceOldEnough(sessionDir string, interval time.Duration) bool {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return false
	}
	var oldest time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		mt := info.ModTime()
		if oldest.IsZero() || mt.Before(oldest) {
			oldest = mt
		}
	}
	if oldest.IsZero() {
		return false
	}
	return time.Since(oldest) >= interval
}

// shouldAutoRun decides whether an automatic Dream/Distill run is due. It
// combines: the master switch, the per-kind cadence, the inFlight gate, and the
// minSpawnGap debounce. The cadence itself is read from disk via LastDreamRun,
// never from in-memory state. A cold-start grace (first session age) only
// matters when there is no prior run at all.
func shouldAutoRun(sessionDir string, kind DreamKind, intervalDays int) bool {
	if sessionDir == "" {
		return false
	}
	dreamCoord.mu.Lock()
	if dreamCoord.inFlight[kind] {
		dreamCoord.mu.Unlock()
		return false
	}
	if time.Since(dreamCoord.lastAuto[kind]) < minSpawnGap {
		dreamCoord.mu.Unlock()
		return false
	}
	dreamCoord.mu.Unlock()

	interval := time.Duration(intervalDays) * 24 * time.Hour
	if last, ok := LastDreamRun(sessionDir, kind); ok {
		// Prior run on record: wait out the cadence.
		if time.Since(last.StartedAt) < interval {
			return false
		}
	} else {
		// Cold start: never run for this kind. Wait until the workspace has
		// accumulated enough session history (oldest .jsonl mtime as a proxy
		// for project age) that consolidation is worthwhile.
		if !workspaceOldEnough(sessionDir, interval) {
			return false
		}
	}
	dreamCoord.mu.Lock()
	dreamCoord.lastAuto[kind] = time.Now()
	dreamCoord.mu.Unlock()
	return true
}

// ShouldAutoDream reports whether the Dream agent should run this turn, based on
// the live config (master switch + cadence). It is the entry point called from
// the controller turn loop.
func ShouldAutoDream(sessionDir string) bool {
	dream, ok := dreamConfig()
	if !ok || !dream.Enabled {
		return false
	}
	return shouldAutoRun(sessionDir, KindDream, dream.DreamIntervalDays())
}

// ShouldAutoDistill reports whether the Distill agent should run this turn.
func ShouldAutoDistill(sessionDir string) bool {
	dream, ok := dreamConfig()
	if !ok || !dream.Enabled {
		return false
	}
	return shouldAutoRun(sessionDir, KindDistill, dream.DistillIntervalDays())
}

// runKind executes a Dream or Distill task under the coordinator, recording the
// outcome to dream_state.json. trigger distinguishes automatic vs manual. The
// automatic path has already passed the cadence + master-switch gate in
// ShouldAutoDream/ShouldAutoDistill before this is invoked, so here we only
// apply the master switch for manual runs, take the inFlight lock, run, and
// record. Returns the run record (including any error) and whether a run
// actually executed.
// quietDreamSink hides a background dream/distill turn from the hosting tab's
// UI: only accounting (Usage), persistence (TurnDone), and toasts (Notice)
// pass through. Content events (the task brief, reasoning, tool traffic)
// stay invisible.
type quietDreamSink struct{ inner event.Sink }

func (q quietDreamSink) Emit(e event.Event) {
	switch e.Kind {
	case event.Usage, event.Notice, event.TurnDone:
		if q.inner != nil {
			q.inner.Emit(e)
		}
	}
}

func runKind(ctx context.Context, sessionDir string, kind DreamKind, task string, timeout time.Duration, prov provider.Provider, reg *tool.Registry, sess *Session, sink event.Sink, trigger DreamTrigger) (DreamRun, bool) {
	run := DreamRun{Kind: kind, Trigger: trigger, StartedAt: time.Now()}

	// Manual triggers honor the master switch (a disabled feature cannot be
	// force-run). The automatic path was already gated upstream.
	if trigger == TriggerManual {
		if dream, ok := dreamConfig(); ok && !dream.Enabled {
			run.Status = "error"
			run.Error = "self-evolution is disabled in settings"
			return run, false
		}
	}

	// Concurrency gate. inFlight is per-kind so Dream and Distill can run in
	// parallel, but two Dreams cannot.
	dreamCoord.mu.Lock()
	if dreamCoord.inFlight[kind] {
		dreamCoord.mu.Unlock()
		run.Status = "error"
		run.Error = "a " + string(kind) + " run is already in progress"
		return run, false
	}
	dreamCoord.inFlight[kind] = true
	dreamCoord.mu.Unlock()
	defer func() {
		dreamCoord.mu.Lock()
		dreamCoord.inFlight[kind] = false
		dreamCoord.mu.Unlock()
	}()

	bgCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// 完全隐藏 (ppt-capability-upgrade-spec P3): the background self-evolution
	// turn runs on the shared session but must never render in the tab's
	// transcript or composer — the task brief leaking into the input box was
	// the reported bug. The quiet sink drops all content/streaming events;
	// Usage keeps the tab's token accounting honest, TurnDone keeps the
	// autosave, Notice keeps the distill-completion toast. Dream status is
	// surfaced separately via DreamRunView.
	sub := New(prov, reg, sess, Options{RequireVisibleFinal: true}, quietDreamSink{inner: sink})
	err := sub.Run(bgCtx, task)
	run.Duration = time.Since(run.StartedAt).Truncate(time.Second).String()
	switch err {
	case nil:
		run.Status = "ok"
	case context.DeadlineExceeded:
		run.Status = "timeout"
		run.Error = err.Error()
	default:
		run.Status = "error"
		run.Error = err.Error()
	}
	appendDreamRun(sessionDir, run)
	// A successful Distill may have produced new skills or surfaced stale ones;
	// fire the post-distill hook (boot uses it to retire cold skills). Best-effort
	// — the hook's output is logged, never blocks the run's return.
	if kind == KindDistill && run.Status == "ok" {
		if note := notifyDistillComplete(); note != "" {
			sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: note})
		}
	}
	return run, true
}

// SpawnDream kicks off a background dream agent if an automatic run is due. It
// runs asynchronously — the caller does not block on completion. profile selects
// the portrait sizing check (which determines compress vs merge mode). wg, when
// non-nil, is Add(1)/Done()'d so a caller can drain it on shutdown.
func SpawnDream(ctx context.Context, sessionDir, profile string, prov provider.Provider, reg *tool.Registry, sess *Session, sink event.Sink, wg *sync.WaitGroup) bool {
	if !ShouldAutoDream(sessionDir) {
		return false
	}
	if wg != nil {
		wg.Add(1)
	}
	go func() {
		if wg != nil {
			defer wg.Done()
		}
		_, _ = runKind(ctx, sessionDir, KindDream, dreamTaskFor(profile), dreamTimeout, prov, reg, sess, sink, TriggerAuto)
	}()
	return true
}

// SpawnDistill kicks off a background distill agent if an automatic run is due.
// See SpawnDream for the wg draining contract.
func SpawnDistill(ctx context.Context, sessionDir string, prov provider.Provider, reg *tool.Registry, sess *Session, sink event.Sink, wg *sync.WaitGroup) bool {
	if !ShouldAutoDistill(sessionDir) {
		return false
	}
	if wg != nil {
		wg.Add(1)
	}
	go func() {
		if wg != nil {
			defer wg.Done()
		}
		_, _ = runKind(ctx, sessionDir, KindDistill, DistillTask, distillTimeout, prov, reg, sess, sink, TriggerAuto)
	}()
	return true
}

// RunDreamOnce triggers a manual Dream run. It blocks until the run completes
// (or times out) and returns the resulting record + whether a run actually
// executed. The caller (controller → desktop) surfaces the status to the user.
func RunDreamOnce(ctx context.Context, sessionDir, profile string, prov provider.Provider, reg *tool.Registry, sess *Session, sink event.Sink) (DreamRun, bool) {
	return runKind(ctx, sessionDir, KindDream, dreamTaskFor(profile), dreamTimeout, prov, reg, sess, sink, TriggerManual)
}

// RunDistillOnce triggers a manual Distill run. See RunDreamOnce.
func RunDistillOnce(ctx context.Context, sessionDir string, prov provider.Provider, reg *tool.Registry, sess *Session, sink event.Sink) (DreamRun, bool) {
	return runKind(ctx, sessionDir, KindDistill, DistillTask, distillTimeout, prov, reg, sess, sink, TriggerManual)
}

// DreamInFlight reports whether a run of the given kind is currently executing.
// Used by the desktop UI to show a "running" state and disable the trigger button.
func DreamInFlight(kind DreamKind) bool {
	dreamCoord.mu.Lock()
	defer dreamCoord.mu.Unlock()
	return dreamCoord.inFlight[kind]
}
