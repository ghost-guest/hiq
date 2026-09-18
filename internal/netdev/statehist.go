// State history (NETDEV_SPEC_V2 §状态历史): pre-mutation snapshots of the ops
// state files, modeled after the coding-mode checkpoints (Claude Code / ZCode
// alignment). Every state transition (proposal approve/execute/rollback, job
// lifecycle, cutover lifecycle, template saves, finding verdicts, inventory and
// config edits) snapshots the entity files it is about to overwrite; a suffix
// rewind then restores every file touched since a chosen event to its
// pre-event content.
//
// Layer contract — restoring here reverts LOCAL RECORDS ONLY. Device-side
// rollback stays on the proposal/cutover path; the UI must say so. The audit
// log remains the immutable what-happened trail; state history is the
// restorable what-it-looked-like-before trail. Excluded by design: append-only
// journals (audit.jsonl, inspections, port events), telemetry (series,
// metrics.db), add-only libraries (backups, srvconf), and secrets.enc.json.
package netdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/checkpoint"
	"github.com/zzycxz/hiq/internal/diff"
	fileenc "github.com/zzycxz/hiq/internal/fileutil/encoding"
)

// netdevStateEventLimit caps retained state events (one per transition, not per
// conversation turn, so higher than the session checkpoint default).
const netdevStateEventLimit = 200

// Actors for event attribution.
const (
	StateActorUser   = "user"   // desktop bridge (a human clicked)
	StateActorAgent  = "agent"  // LLM tool call (netdev_propose, netdev_finding…)
	StateActorIM     = "im"     // bot gateway approval / ack
	StateActorSystem = "system" // runners, schedulers, watchers
)

// Event kinds — structured values stored per event and localized in the
// frontend via ndv.hist.kind.*.
const (
	StateEventPropose      = "propose"
	StateEventApprove      = "approve"
	StateEventReject       = "reject"
	StateEventExecute      = "execute"
	StateEventRollback     = "rollback"
	StateEventDelete       = "delete"
	StateEventCloseWatch   = "close-watch"
	StateEventJobStart     = "job-start"
	StateEventJobPause     = "job-pause"
	StateEventJobResume    = "job-resume"
	StateEventJobAbort     = "job-abort"
	StateEventJobFreeze    = "job-freeze"
	StateEventJobFinish    = "job-finish"
	StateEventCutoverStart = "cutover-start"
	StateEventCutoverGo    = "cutover-continue"
	StateEventCutoverBack  = "cutover-rollback"
	StateEventCutoverAbort = "cutover-abort"
	StateEventCutoverHold  = "cutover-hold"
	StateEventCutoverDone  = "cutover-finish"
	StateEventTplSave      = "template-save"
	StateEventTplDelete    = "template-delete"
	StateEventTplApply     = "template-apply"
	StateEventFindAck      = "finding-ack"
	StateEventFindFP       = "finding-false-positive"
	StateEventFindResolve  = "finding-resolve"
	StateEventFindDismiss  = "finding-dismiss"
	StateEventFindClear    = "findings-clear"
	StateEventSettings     = "settings-save"
	StateEventPromote      = "hosts-promote"
	StateEventLeadDismiss  = "lead-dismiss"
	StateEventImport       = "import-apply"
	StateEventTopo         = "topo-apply"
	StateEventGolden       = "golden-set"
	StateEventCaseSave     = "case-save"
	StateEventCaseDelete   = "case-delete"
	// StateEventRestoreKeep marks the synthetic reverse event written just
	// before a restore; restoring back to it replays (redoes) the rewound
	// changes.
	StateEventRestoreKeep = "restore-keep"
)

// StateLiveEntity names an entity the suffix of a candidate restore touches
// that is currently mid-flight; restoring under it would fight its runner.
type StateLiveEntity struct {
	Type   string `json:"type"`   // proposal | job | cutover
	ID     string `json:"id"`     // entity id
	Status string `json:"status"` // why it is live (executing / running / hold…)
}

// StateEventMeta is the timeline-facing summary of one state event.
type StateEventMeta struct {
	ID         int               `json:"id"`
	Time       time.Time         `json:"time"`
	Kind       string            `json:"kind"`
	Entity     string            `json:"entity,omitempty"`
	Actor      string            `json:"actor"`
	Paths      []string          `json:"paths"`
	Live       []StateLiveEntity `json:"live,omitempty"`
	CanRestore bool              `json:"canRestore"`
	CanRedo    bool              `json:"canRedo"`
}

// StateRestoreResult reports what a suffix restore wrote back / deleted and
// the synthetic reverse event that enables redo.
type StateRestoreResult struct {
	Written        []string `json:"written"`
	Deleted        []string `json:"deleted"`
	ReverseEventID int      `json:"reverseEventId"`
}

// stateLabel is the structured payload encoded into the checkpoint's Prompt
// field (this store is machine-facing; the session store is the one whose
// prompts surface in coding-mode pickers).
type stateLabel struct {
	Kind   string `json:"k"`
	Entity string `json:"e,omitempty"`
	Actor  string `json:"a"`
}

var (
	// stateEventMu serializes event creation and restore so snapshots never
	// interleave across events (the checkpoint store locks per call, not per
	// event).
	stateEventMu sync.Mutex
	// stateHistMu guards the cached store, rebuilt when the state-dir test
	// override changes.
	stateHistMu   sync.Mutex
	stateStore    *checkpoint.Store
	stateStoreDir string
)

func stateHist() *checkpoint.Store {
	dir := netdevStateDir()
	stateHistMu.Lock()
	defer stateHistMu.Unlock()
	if stateStore == nil || stateStoreDir != dir {
		stateStore = checkpoint.NewWithLimit(filepath.Join(dir, "state.ckpt"), filepath.Dir(dir), netdevStateEventLimit)
		stateStoreDir = dir
	}
	return stateStore
}

// stateHistRoot is the snapshot root: the hiq config dir, so both
// netdev/** (proposals, jobs, cutovers…) and config.toml are coverable.
func stateHistRoot() string { return filepath.Dir(netdevStateDir()) }

type stateActorCtxKey struct{}

// CtxStateActor tags a context with the state-history actor for transitions
// invoked through it — the cutover runner executes proposals as "system" while
// the desktop/IM bridges keep the "user" default.
func CtxStateActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, stateActorCtxKey{}, actor)
}

func stateActorFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(stateActorCtxKey{}).(string); ok && v != "" {
		return v
	}
	return StateActorUser
}

// StateEventSnap records one restorable event: the current (pre-mutation)
// content of the given absolute paths. Call it immediately before the mutation,
// co-located with the transition's audit entry. Best-effort — a snapshot
// failure logs and never blocks the mutation itself. Returns the event id, or
// -1 when nothing was snapshotted.
func StateEventSnap(kind, entity, actor string, absPaths ...string) int {
	stateEventMu.Lock()
	defer stateEventMu.Unlock()
	return stateEventSnapLocked(kind, entity, actor, absPaths...)
}

func stateEventSnapLocked(kind, entity, actor string, absPaths ...string) int {
	if len(absPaths) == 0 {
		return -1
	}
	s := stateHist()
	root := stateHistRoot()
	var rels []string
	for _, abs := range absPaths {
		rel, err := filepath.Rel(root, filepath.Clean(abs))
		if err != nil || rel == "." || !filepath.IsLocal(rel) {
			slog.Warn("statehist: path outside state root, skipped", "path", abs, "root", root)
			continue
		}
		rels = append(rels, filepath.ToSlash(rel))
	}
	if len(rels) == 0 {
		return -1
	}

	id := s.NextTurn()
	label, _ := json.Marshal(stateLabel{Kind: kind, Entity: entity, Actor: actor})
	s.Begin(id, string(label), 0)
	for _, rel := range rels {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		data, err := os.ReadFile(abs)
		if err != nil {
			// Absent now — the mutation creates it; a restore deletes it.
			s.Snapshot(diff.Change{Path: rel, Kind: diff.Create})
			continue
		}
		enc, _ := fileenc.Detect(data)
		s.Snapshot(diff.Change{Path: rel, Kind: diff.Modify, OldText: string(fileenc.Decode(data, enc))})
	}
	s.Finalize()
	return id
}

// StateEventMetas lists events newest-first with the ZCode-style safety
// classification: an event is restorable unless the suffix it would revert
// touches an entity that is currently live (a proposal executing/watching, a
// job running/paused, a cutover running/hold). Redo is offered only on the
// newest event when it is a restore-keep — a newer event would be collateral
// of the redo's own suffix restore.
func StateEventMetas() []StateEventMeta {
	metas := stateHist().List() // oldest first
	events := make([]StateEventMeta, 0, len(metas))
	for _, m := range metas {
		var lb stateLabel
		_ = json.Unmarshal([]byte(m.Prompt), &lb)
		ev := StateEventMeta{ID: m.Turn, Time: m.Time, Kind: lb.Kind, Entity: lb.Entity, Actor: lb.Actor, Paths: m.Paths}
		if ev.Kind == "" {
			ev.Kind = "unknown"
		}
		if ev.Actor == "" {
			ev.Actor = StateActorSystem
		}
		events = append(events, ev)
	}
	for i := range events {
		touched := map[string]bool{}
		for j := i; j < len(events); j++ {
			for _, p := range events[j].Paths {
				touched[p] = true
			}
		}
		paths := make([]string, 0, len(touched))
		for p := range touched {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		events[i].Live = stateLiveEntities(paths)
		events[i].CanRestore = len(events[i].Live) == 0 && len(events[i].Paths) > 0
	}
	if n := len(events); n > 0 && events[n-1].Kind == StateEventRestoreKeep && len(events[n-1].Live) == 0 {
		events[n-1].CanRedo = true
	}
	// Newest first for the timeline.
	out := make([]StateEventMeta, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- {
		out = append(out, events[i])
	}
	return out
}

// StateEventDiff renders what a rewind to before this event would revert,
// reusing the checkpoint store's preview (old = current on-disk, new =
// snapshotted pre-event).
func StateEventDiff(id int) []diff.Change {
	return stateHist().DiffForTurn(id)
}

// StateRestore rewinds local state to just before event id: every path touched
// by id or any later event is restored to its earliest post-id snapshot
// (deleted when it did not exist). Before touching anything it re-checks the
// live-entity classification (the preview may be stale) and writes a
// restore-keep reverse event so the rewind itself is redoable. Local records
// only — devices are never contacted.
func StateRestore(id int, actor string) (StateRestoreResult, error) {
	stateEventMu.Lock()
	defer stateEventMu.Unlock()

	s := stateHist()
	root := stateHistRoot()
	metas := s.List()
	found := false
	seen := map[string]bool{}
	var rels []string
	for _, m := range metas {
		if m.Turn == id {
			found = true
		}
		if !found {
			continue
		}
		for _, p := range m.Paths {
			if !seen[p] {
				seen[p] = true
				rels = append(rels, p)
			}
		}
	}
	if !found {
		return StateRestoreResult{}, fmt.Errorf("state event %d not found (pruned or never existed)", id)
	}
	if len(rels) == 0 {
		return StateRestoreResult{}, fmt.Errorf("state event %d has no snapshots to restore", id)
	}
	if live := stateLiveEntities(rels); len(live) > 0 {
		return StateRestoreResult{}, fmt.Errorf("restore blocked: %s", formatLive(live))
	}

	abs := make([]string, 0, len(rels))
	for _, rel := range rels {
		abs = append(abs, filepath.Join(root, filepath.FromSlash(rel)))
	}
	rev := stateEventSnapLocked(StateEventRestoreKeep, fmt.Sprintf("#%d", id), actor, abs...)

	written, deleted, err := s.RestoreCode(id)
	if err != nil {
		return StateRestoreResult{ReverseEventID: rev}, fmt.Errorf("restore incomplete: %w", err)
	}
	_ = AppendAudit(Audit{Device: "(state)", Command: fmt.Sprintf("state-restore event=%d written=%d deleted=%d reverse=%d", id, len(written), len(deleted), rev), Class: "state", Status: AuditOK})
	return StateRestoreResult{Written: written, Deleted: deleted, ReverseEventID: rev}, nil
}

// stateEntityOf maps a state file path to its entity type and id, or "" when
// the path is not a live-checkable entity.
func stateEntityOf(abs string) (typ, id string) {
	for _, e := range []struct{ typ, dir string }{
		{"proposal", ProposalsDir()},
		{"job", jobsDir()},
		{"cutover", cutoversDir()},
	} {
		if strings.HasPrefix(abs, e.dir+string(os.PathSeparator)) {
			return e.typ, strings.TrimSuffix(filepath.Base(abs), ".json")
		}
	}
	return "", ""
}

// stateLiveEntities loads the current state of every proposal/job/cutover file
// in paths and reports the ones mid-flight. Unreadable files are skipped —
// restore will recreate them from the snapshot anyway.
func stateLiveEntities(relPaths []string) []StateLiveEntity {
	root := stateHistRoot()
	seen := map[string]bool{}
	var out []StateLiveEntity
	add := func(typ, id, status string) {
		if seen[typ+id] {
			return
		}
		seen[typ+id] = true
		out = append(out, StateLiveEntity{Type: typ, ID: id, Status: status})
	}
	for _, rel := range relPaths {
		typ, id := stateEntityOf(filepath.Join(root, filepath.FromSlash(rel)))
		switch typ {
		case "proposal":
			if p, err := GetProposal(id); err == nil && (p.Status == ProposalExecuting || p.Status == ProposalWatching) {
				add(typ, id, p.Status)
			}
		case "job":
			if j, err := GetJob(id); err == nil && (j.Status == JobRunning || j.Status == JobPaused) {
				add(typ, id, j.Status)
			}
		case "cutover":
			if c, err := GetCutover(id); err == nil && (c.Status == CutoverRunning || c.Status == CutoverHold) {
				add(typ, id, c.Status)
			}
		}
	}
	return out
}

func formatLive(live []StateLiveEntity) string {
	parts := make([]string, 0, len(live))
	for _, l := range live {
		parts = append(parts, fmt.Sprintf("%s %s is %s", l.Type, l.ID, l.Status))
	}
	return strings.Join(parts, "; ")
}
