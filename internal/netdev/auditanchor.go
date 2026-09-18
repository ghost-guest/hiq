package netdev

import (
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/trustdomain"
)

// Audit cross-anchoring (TRUSTDOMAIN_SPEC §八): the local audit hash chain
// (audit.go, B-batch) already makes local tampering detectable; anchoring
// its head to the domain ledger additionally survives "the operator with
// disk access rewrites the whole file" — peers hold the anchored heads.
//
// Frequency honors invariant #6 (low-frequency control plane): anchor when
// anchorEveryEntries new audit entries accumulated OR anchorEveryTime
// passed since the last anchor — never per entry.
const (
	anchorEveryEntries = 16
	anchorEveryTime    = 10 * time.Minute
)

type auditAnchorState struct {
	mu         sync.Mutex
	nodeFn     func() (*trustdomain.Node, error) // nil = disarmed
	pending    int
	lastAnchor time.Time
	lastHead   string
}

var auditAnchor auditAnchorState

// InitAuditAnchoring arms the hook with the shared embedded node. Called
// wherever the trust-domain runtime starts (RegisterTools, the CLI daemon's
// executor branch); hosts without [trustdomain] never arm — the hook is a
// no-op costing one mutex trip per audit entry.
func InitAuditAnchoring(cfg *config.Config) {
	auditAnchor.mu.Lock()
	defer auditAnchor.mu.Unlock()
	if auditAnchor.nodeFn != nil {
		return
	}
	auditAnchor.nodeFn = func() (*trustdomain.Node, error) { return SharedRemoteNode(cfg) }
}

// armAuditAnchoringForTest lets tests inject their own node source and
// thresholds; returns a disarm func.
func armAuditAnchoringForTest(nodeFn func() (*trustdomain.Node, error)) (disarm func()) {
	auditAnchor.mu.Lock()
	prevFn := auditAnchor.nodeFn
	prevPending, prevLast, prevHead := auditAnchor.pending, auditAnchor.lastAnchor, auditAnchor.lastHead
	auditAnchor.nodeFn = nodeFn
	auditAnchor.pending, auditAnchor.lastAnchor, auditAnchor.lastHead = 0, time.Time{}, ""
	auditAnchor.mu.Unlock()
	return func() {
		auditAnchor.mu.Lock()
		auditAnchor.nodeFn = prevFn
		auditAnchor.pending, auditAnchor.lastAnchor, auditAnchor.lastHead = prevPending, prevLast, prevHead
		auditAnchor.mu.Unlock()
	}
}

// maybeAnchorAudit runs after each successful audit append with the
// just-written chain head (the caller still holds auditMu — never read the
// audit state back here). Anchoring failures are swallowed but keep the
// pending count (the next entry retries): a fleet hiccup must never block
// the diagnostic hand.
func maybeAnchorAudit(head string) {
	auditAnchor.mu.Lock()
	if auditAnchor.nodeFn == nil {
		auditAnchor.mu.Unlock()
		return
	}
	auditAnchor.pending++
	due := auditAnchor.pending >= anchorEveryEntries ||
		(!auditAnchor.lastAnchor.IsZero() && time.Since(auditAnchor.lastAnchor) >= anchorEveryTime)
	if !due || head == "" {
		auditAnchor.mu.Unlock()
		return
	}
	nodeFn := auditAnchor.nodeFn
	auditAnchor.mu.Unlock()

	node, err := nodeFn()
	if err != nil {
		return // stay pending; retry on the next entry
	}
	if err := node.AnchorAudit(head, uint64(time.Now().Unix())); err != nil {
		return // stay pending; retry on the next entry
	}
	auditAnchor.mu.Lock()
	auditAnchor.pending = 0
	auditAnchor.lastAnchor = time.Now()
	auditAnchor.lastHead = head
	auditAnchor.mu.Unlock()
}
