package team

import (
	"fmt"
	"sync"
	"testing"
)

func TestPoolAdmitsUpToMax(t *testing.T) {
	p := NewPool(2)
	if got := p.Admit("t/a"); got != AdmitRunning {
		t.Fatalf("first admit = %v, want running", got)
	}
	if got := p.Admit("t/b"); got != AdmitRunning {
		t.Fatalf("second admit = %v, want running", got)
	}
	if got := p.Admit("t/c"); got != AdmitQueued {
		t.Fatalf("third admit = %v, want queued", got)
	}
	snap := p.Snapshot()
	if snap.Max != 2 {
		t.Fatalf("max = %d, want 2", snap.Max)
	}
	if len(snap.Running) != 2 {
		t.Fatalf("running = %v, want 2 entries", snap.Running)
	}
	if len(snap.Queued) != 1 || snap.Queued[0] != "t/c" {
		t.Fatalf("queued = %v, want [t/c]", snap.Queued)
	}
}

func TestPoolDuplicateIsRejectedNotQueued(t *testing.T) {
	p := NewPool(1)
	if got := p.Admit("t/a"); got != AdmitRunning {
		t.Fatalf("admit = %v, want running", got)
	}
	// Same card again while running: duplicate, and it must NOT take a queue slot.
	if got := p.Admit("t/a"); got != AdmitDuplicate {
		t.Fatalf("re-admit running = %v, want duplicate", got)
	}
	// Same card again while queued: still duplicate — one card, one wait slot.
	if got := p.Admit("t/b"); got != AdmitQueued {
		t.Fatalf("admit busy = %v, want queued", got)
	}
	if got := p.Admit("t/b"); got != AdmitDuplicate {
		t.Fatalf("re-admit queued = %v, want duplicate", got)
	}
	snap := p.Snapshot()
	if len(snap.Running) != 1 || len(snap.Queued) != 1 {
		t.Fatalf("snapshot = %+v, want 1 running + 1 queued", snap)
	}
}

func TestPoolReleasePromotesFIFO(t *testing.T) {
	p := NewPool(1)
	p.Admit("t/a")
	p.Admit("t/b")
	p.Admit("t/c")

	next, ok := p.Release("t/a")
	if !ok || next != "t/b" {
		t.Fatalf("release = (%q,%v), want (t/b,true)", next, ok)
	}
	// The promoted key already holds its slot: it must not be re-admitted.
	if got := p.Admit(next); got != AdmitDuplicate {
		t.Fatalf("admit(promoted) = %v, want duplicate", got)
	}
	next, ok = p.Release("t/b")
	if !ok || next != "t/c" {
		t.Fatalf("second release = (%q,%v), want (t/c,true)", next, ok)
	}
	if _, ok = p.Release("t/c"); ok {
		t.Fatalf("release with empty queue reported a promotion")
	}
	if snap := p.Snapshot(); len(snap.Running) != 0 || len(snap.Queued) != 0 {
		t.Fatalf("snapshot after drain = %+v, want empty", snap)
	}
}

func TestPoolReleaseUnknownKeyIsHarmless(t *testing.T) {
	p := NewPool(1)
	p.Admit("t/a")
	p.Admit("t/b")
	// Releasing a key that never held a slot must not free someone else's slot.
	if next, ok := p.Release("t/ghost"); ok || next != "" {
		t.Fatalf("ghost release = (%q,%v), want (\"\",false)", next, ok)
	}
	if snap := p.Snapshot(); len(snap.Running) != 1 || len(snap.Queued) != 1 {
		t.Fatalf("ghost release changed state: %+v", snap)
	}
}

func TestPoolSetMaxRaisesAndPromotes(t *testing.T) {
	p := NewPool(1)
	p.Admit("t/a")
	p.Admit("t/b")
	p.Admit("t/c")

	promoted := p.SetMax(3)
	if len(promoted) != 2 || promoted[0] != "t/b" || promoted[1] != "t/c" {
		t.Fatalf("promoted = %v, want [t/b t/c]", promoted)
	}
	if snap := p.Snapshot(); len(snap.Running) != 3 || len(snap.Queued) != 0 {
		t.Fatalf("snapshot = %+v, want 3 running", snap)
	}
	if again := p.SetMax(3); len(again) != 0 {
		t.Fatalf("idempotent SetMax promoted %v", again)
	}
}

func TestPoolSetMaxLowersWithoutKillingRuns(t *testing.T) {
	p := NewPool(3)
	p.Admit("t/a")
	p.Admit("t/b")
	p.Admit("t/c")

	p.SetMax(1)
	// Nothing is evicted; the bound only blocks new admissions.
	if snap := p.Snapshot(); len(snap.Running) != 3 {
		t.Fatalf("lowering max evicted runs: %+v", snap)
	}
}

func TestPoolSetMaxZeroFallsBackToDefault(t *testing.T) {
	p := NewPool(2)
	if got := p.SetMax(0); got != nil {
		t.Fatalf("SetMax(0) promoted %v", got)
	}
	if got := p.Max(); got != defaultMaxParallel {
		t.Fatalf("max = %d, want default %d", got, defaultMaxParallel)
	}
}

func TestPoolCancelAll(t *testing.T) {
	p := NewPool(2)
	p.Admit("t/a")
	p.Admit("t/b")
	p.Admit("t/c")
	p.Admit("t/d")

	running, queued := p.CancelAll()
	if len(running) != 2 || len(queued) != 2 {
		t.Fatalf("cancel = (%v,%v), want 2 running + 2 queued", running, queued)
	}
	if snap := p.Snapshot(); len(snap.Running) != 0 || len(snap.Queued) != 0 {
		t.Fatalf("pool not drained: %+v", snap)
	}
	// After a cancel the pool accepts work again.
	if got := p.Admit("t/a"); got != AdmitRunning {
		t.Fatalf("admit after cancel = %v, want running", got)
	}
}

func TestPoolNilAndZeroAreUsable(t *testing.T) {
	var nilPool *Pool
	if got := nilPool.Admit("t/a"); got != AdmitRunning {
		t.Fatalf("nil pool admit = %v, want running", got)
	}
	if _, ok := nilPool.Release("t/a"); ok {
		t.Fatalf("nil pool release promoted")
	}
	if _, q := nilPool.CancelAll(); q != nil {
		t.Fatalf("nil pool cancel = %v", q)
	}
	if got := nilPool.Snapshot(); got.Max != 0 || len(got.Running) != 0 {
		t.Fatalf("nil pool snapshot = %+v", got)
	}
	if nilPool.Running("t/a") {
		t.Fatalf("nil pool reported a running key")
	}

	// A zero/negative bound still admits one card rather than deadlocking.
	z := NewPool(0)
	if got := z.Admit("t/a"); got != AdmitRunning {
		t.Fatalf("zero-max pool admit = %v, want running", got)
	}
}

// TestPoolConcurrentAdmitRelease exercises the mutex under -race: N workers
// admit, immediately release, and one of them must always be promoted so the
// pool keeps making progress instead of wedging on a lost wakeup.
func TestPoolConcurrentAdmitRelease(t *testing.T) {
	const workers = 8
	const rounds = 40
	p := NewPool(2)

	var mu sync.Mutex
	started := 0
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			key := fmt.Sprintf("t/%d", w)
			for i := 0; i < rounds; i++ {
				switch p.Admit(key) {
				case AdmitRunning:
					mu.Lock()
					started++
					mu.Unlock()
					next, ok := p.Release(key)
					for ok {
						// Chain-promote so no slot is stranded.
						next, ok = p.Release(next)
					}
				case AdmitQueued:
					// Another worker will promote us; spin until admitted.
					// (The real caller waits on the board, not in a loop.)
					continue
				case AdmitDuplicate:
					continue
				}
			}
		}(w)
	}
	wg.Wait()

	if started == 0 {
		t.Fatalf("no worker ever got a slot")
	}
	if snap := p.Snapshot(); len(snap.Running) > 2 {
		t.Fatalf("bound violated under concurrency: %+v", snap)
	}
}

func TestPoolDequeue(t *testing.T) {
	p := NewPool(1)
	p.Admit("t/a")
	p.Admit("t/b")
	p.Admit("t/c")

	if !p.Dequeue("t/b") {
		t.Fatalf("dequeue of a waiting key reported false")
	}
	if p.Dequeue("t/b") {
		t.Fatalf("second dequeue of the same key reported true")
	}
	if p.Dequeue("t/a") {
		t.Fatalf("dequeue of a running key must not report a wait")
	}
	// Removing a middle entry must preserve FIFO order for the rest.
	if next, ok := p.Release("t/a"); !ok || next != "t/c" {
		t.Fatalf("release promoted (%q,%v), want (t/c,true)", next, ok)
	}
}

func TestPoolsEnsureKeepsExistingBound(t *testing.T) {
	ps := NewPools()
	p := ps.Ensure("t1", 5)
	if p.Max() != 5 {
		t.Fatalf("max = %d, want 5", p.Max())
	}
	// A second Ensure must not silently rewrite the bound.
	if again := ps.Ensure("t1", 9); again != p || again.Max() != 5 {
		t.Fatalf("Ensure recreated or rebound the pool: %v / %d", again == p, again.Max())
	}
	if ps.Get("nope") != nil {
		t.Fatalf("Get returned a pool for an unknown team")
	}
	// Teams are isolated: a second team gets its own slots.
	p2 := ps.Ensure("t2", 1)
	if p2 == p {
		t.Fatalf("two teams shared one pool")
	}
}

func TestPoolsSetMaxPromotesAndCreates(t *testing.T) {
	ps := NewPools()
	p := ps.Ensure("t1", 1)
	p.Admit("t1/a")
	p.Admit("t1/b")
	if promoted := ps.SetMax("t1", 2); len(promoted) != 1 || promoted[0] != "t1/b" {
		t.Fatalf("promoted = %v, want [t1/b]", promoted)
	}
	// Setting a bound for an unknown team creates it without promoting.
	if promoted := ps.SetMax("t9", 4); promoted != nil {
		t.Fatalf("SetMax on a new team promoted %v", promoted)
	}
	if got := ps.Ensure("t9", 1).Max(); got != 4 {
		t.Fatalf("created pool max = %d, want 4", got)
	}
}

func TestPoolsCancelAllAndDrop(t *testing.T) {
	ps := NewPools()
	a := ps.Ensure("t1", 1)
	a.Admit("t1/x")
	a.Admit("t1/y")
	b := ps.Ensure("t2", 2)
	b.Admit("t2/z")

	got := ps.CancelAll()
	if len(got) != 2 || len(got["t1"]) != 1 || got["t1"][0] != "t1/x" || len(got["t2"]) != 1 {
		t.Fatalf("cancel = %v, want t1:[t1/x], t2:[t2/z]", got)
	}
	if snap := a.Snapshot(); len(snap.Running) != 0 || len(snap.Queued) != 0 {
		t.Fatalf("t1 not drained: %+v", snap)
	}

	// Drop forgets the pool entirely and reports both halves.
	a.Admit("t1/x")
	a.Admit("t1/y")
	running, queued := ps.Drop("t1")
	if len(running) != 1 || len(queued) != 1 {
		t.Fatalf("drop = (%v,%v), want 1 running + 1 queued", running, queued)
	}
	if ps.Get("t1") != nil {
		t.Fatalf("pool survived Drop")
	}
	if teams := ps.Teams(); len(teams) != 1 || teams[0] != "t2" {
		t.Fatalf("teams = %v, want [t2]", teams)
	}
}

func TestPoolsNilSafe(t *testing.T) {
	var ps *Pools
	if ps.Get("t") != nil || ps.Ensure("t", 1) != nil {
		t.Fatalf("nil Pools handed out a pool")
	}
	if got := ps.SetMax("t", 1); got != nil {
		t.Fatalf("nil Pools SetMax = %v", got)
	}
	if r, q := ps.Drop("t"); r != nil || q != nil {
		t.Fatalf("nil Pools Drop = (%v,%v)", r, q)
	}
	if got := ps.CancelAll(); got != nil {
		t.Fatalf("nil Pools CancelAll = %v", got)
	}
	if got := ps.Teams(); got != nil {
		t.Fatalf("nil Pools Teams = %v", got)
	}
}
