package team

import "sync"

// Concurrency admission for member runs (P1-1).
//
// Policy.MaxParallel is a *bound*, not a suggestion: a team that fans out ten
// cards must not put ten member sessions in flight just because the user
// pressed ten buttons. This file is the enforcement point.
//
// The pool is deliberately pure domain — it decides WHO may run and WHO waits,
// but never starts anything. The desktop layer owns provider wiring, the
// goroutine and the cancel func, so the kernel's dependency direction
// (team → taskmonitor only) stays intact and the queueing rules are testable
// without a model, a tool registry or a UI.
//
// Queueing semantics (mirrors the behaviour of open-vetta's SubagentPool, whose
// contract is "different members run in parallel, the same member queues"):
//   - a free slot admits the card immediately;
//   - a full pool holds the card in a FIFO queue instead of rejecting it, so
//     pressing "run" on a busy board is still meaningful;
//   - re-admitting a card that is already running OR already queued is a
//     duplicate, so a double click can neither start two runs nor occupy two
//     queue slots.

// AdmitState is what Pool.Admit decided about a card.
type AdmitState uint8

const (
	// AdmitDuplicate means the card already has an in-flight or queued run.
	AdmitDuplicate AdmitState = iota
	// AdmitRunning means a concurrency slot was free: start the run now.
	AdmitRunning
	// AdmitQueued means the pool was full: the card is now waiting, in order.
	AdmitQueued
)

// PoolSnapshot is an immutable view of a pool, for the board and for recovery.
type PoolSnapshot struct {
	// Max is the current concurrency bound (always >= 1).
	Max int
	// Running lists in-flight card keys, in admission order.
	Running []string
	// Queued lists waiting card keys, in FIFO order.
	Queued []string
}

// Pool bounds how many member runs may execute at once.
//
// A nil *Pool is usable and behaves like an unbounded pool with no tracking —
// every Admit returns AdmitRunning and every query is empty. That keeps the
// desktop layer free of nil checks if the store failed to initialise.
type Pool struct {
	mu      sync.Mutex
	max     int
	running map[string]struct{}
	order   []string // running keys in admission order (for stable snapshots)
	queue   []string // waiting keys, head = next to admit
}

// NewPool returns a pool bounded by max. max <= 0 falls back to
// defaultMaxParallel, matching Team.normalize's policy default.
func NewPool(max int) *Pool {
	return &Pool{
		max:     normalizeMaxParallel(max),
		running: map[string]struct{}{},
	}
}

// normalizeMaxParallel clamps a requested bound to a usable one.
func normalizeMaxParallel(max int) int {
	if max <= 0 {
		return defaultMaxParallel
	}
	return max
}

// Max reports the current bound.
func (p *Pool) Max() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.max
}

// Admit asks permission to start key.
//
// The caller MUST start the run only on AdmitRunning. On AdmitQueued the card
// stays parked until a later Release promotes it (see Release / SetMax), which
// returns the key to start.
func (p *Pool) Admit(key string) AdmitState {
	if p == nil {
		return AdmitRunning
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.running[key]; ok {
		return AdmitDuplicate
	}
	if p.queuedContains(key) {
		return AdmitDuplicate
	}
	if len(p.running) < p.max {
		p.running[key] = struct{}{}
		p.order = append(p.order, key)
		return AdmitRunning
	}
	p.queue = append(p.queue, key)
	return AdmitQueued
}

// Release frees key's slot and, when one is waiting and a slot is now free,
// promotes the queue head. The returned key (when ok) MUST be started by the
// caller — the pool has already booked the slot for it, so the caller must NOT
// call Admit for that key again.
func (p *Pool) Release(key string) (next string, ok bool) {
	if p == nil {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.remove(key)
	return p.promoteLocked()
}

// SetMax changes the bound (a Policy edit). Lowering it never kills a running
// run — it only stops admitting new ones. Raising it promotes as many queued
// cards as there are new slots, in FIFO order; every promoted key is returned
// and MUST be started by the caller.
func (p *Pool) SetMax(max int) (promoted []string) {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.max = normalizeMaxParallel(max)
	for {
		next, ok := p.promoteLocked()
		if !ok {
			return promoted
		}
		promoted = append(promoted, next)
	}
}

// CancelAll empties the pool: it forgets every queued card and returns the
// in-flight keys so the caller can cancel their runs, plus the keys that were
// merely waiting (which need no cancellation — they never started).
//
// This is the "stop everything now" primitive (P1-3), the counterpart of
// open-vetta's work.stop-all.
func (p *Pool) CancelAll() (running []string, queued []string) {
	if p == nil {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	running = append(running, p.order...)
	queued = append(queued, p.queue...)
	p.running = map[string]struct{}{}
	p.order = nil
	p.queue = nil
	return running, queued
}

// Snapshot returns a copy of the pool's state. Used by the board's "which cards
// are spinning" query and by startup recovery.
func (p *Pool) Snapshot() PoolSnapshot {
	if p == nil {
		return PoolSnapshot{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return PoolSnapshot{
		Max:     p.max,
		Running: append([]string(nil), p.order...),
		Queued:  append([]string(nil), p.queue...),
	}
}

// Running reports whether key currently holds a slot.
func (p *Pool) Running(key string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.running[key]
	return ok
}

// promoteLocked moves the queue head into a free slot, if both exist. The
// caller must hold p.mu.
func (p *Pool) promoteLocked() (string, bool) {
	if len(p.queue) == 0 || len(p.running) >= p.max {
		return "", false
	}
	key := p.queue[0]
	p.queue = p.queue[1:]
	// Copy the tail so the backing array does not retain popped keys.
	if len(p.queue) == 0 {
		p.queue = nil
	}
	p.running[key] = struct{}{}
	p.order = append(p.order, key)
	return key, true
}

// remove drops key from the running set and the admission order. The caller
// must hold p.mu.
func (p *Pool) remove(key string) {
	if _, ok := p.running[key]; !ok {
		return
	}
	delete(p.running, key)
	for i, k := range p.order {
		if k == key {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
}

// queuedContains reports whether key is waiting. The caller must hold p.mu.
func (p *Pool) queuedContains(key string) bool {
	for _, k := range p.queue {
		if k == key {
			return true
		}
	}
	return false
}

// Dequeue removes key from the waiting queue without starting anything, and
// reports whether it was waiting. It is how "cancel" works for a card that was
// queued rather than running — there is no run to cancel, only a wait to end.
func (p *Pool) Dequeue(key string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, k := range p.queue {
		if k == key {
			p.queue = append(p.queue[:i], p.queue[i+1:]...)
			if len(p.queue) == 0 {
				p.queue = nil
			}
			return true
		}
	}
	return false
}

// Pools keeps one bounded Pool per team.
//
// Policy.MaxParallel is a per-team policy, so the bound is per-team too: two
// teams each get their own slots, and a busy team cannot starve another. Team
// IDs are the map key; a pool is created on first admission and dropped when
// the team is deleted.
type Pools struct {
	mu     sync.Mutex
	byTeam map[string]*Pool
}

// NewPools returns an empty per-team pool registry.
func NewPools() *Pools {
	return &Pools{byTeam: map[string]*Pool{}}
}

// Get returns a team's pool, or nil when it has never admitted work. Callers
// that only need to observe state use this; admission uses Ensure.
func (ps *Pools) Get(teamID string) *Pool {
	if ps == nil {
		return nil
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.byTeam[teamID]
}

// Ensure returns a team's pool, creating it with max when absent. An existing
// pool keeps its bound so a transient read cannot silently rewrite policy; use
// SetMax for an intentional change.
func (ps *Pools) Ensure(teamID string, max int) *Pool {
	if ps == nil {
		return nil
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if p, ok := ps.byTeam[teamID]; ok {
		return p
	}
	p := NewPool(max)
	ps.byTeam[teamID] = p
	return p
}

// SetMax changes a team's bound, creating the pool when absent, and returns the
// keys promoted out of its queue. Promoted keys already hold a slot, so the
// caller MUST start them without calling Admit again.
func (ps *Pools) SetMax(teamID string, max int) []string {
	if ps == nil {
		return nil
	}
	ps.mu.Lock()
	p, ok := ps.byTeam[teamID]
	if !ok {
		p = NewPool(max)
		ps.byTeam[teamID] = p
		ps.mu.Unlock()
		return nil
	}
	ps.mu.Unlock()
	return p.SetMax(max)
}

// Drop forgets a team's pool, returning the keys that were in flight and the
// keys that were only waiting. Used when a team is deleted, so its runs neither
// leak a slot nor keep reporting as live.
func (ps *Pools) Drop(teamID string) (running, queued []string) {
	if ps == nil {
		return nil, nil
	}
	ps.mu.Lock()
	p, ok := ps.byTeam[teamID]
	if ok {
		delete(ps.byTeam, teamID)
	}
	ps.mu.Unlock()
	if !ok {
		return nil, nil
	}
	return p.CancelAll()
}

// CancelAll drains every pool, returning the in-flight keys grouped by team so
// the caller can cancel each run's context. Queued keys are dropped (they never
// started, so there is nothing to cancel).
func (ps *Pools) CancelAll() map[string][]string {
	if ps == nil {
		return nil
	}
	ps.mu.Lock()
	teams := make([]string, 0, len(ps.byTeam))
	for id := range ps.byTeam {
		teams = append(teams, id)
	}
	ps.mu.Unlock()
	out := map[string][]string{}
	for _, id := range teams {
		p := ps.Get(id)
		if p == nil {
			continue
		}
		running, _ := p.CancelAll()
		if len(running) > 0 {
			out[id] = running
		}
	}
	return out
}

// Teams lists team IDs that currently have a pool.
func (ps *Pools) Teams() []string {
	if ps == nil {
		return nil
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make([]string, 0, len(ps.byTeam))
	for id := range ps.byTeam {
		out = append(out, id)
	}
	return out
}
