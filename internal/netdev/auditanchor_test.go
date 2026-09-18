package netdev

import (
	"path/filepath"
	"testing"

	"github.com/zzycxz/hiq/internal/trustdomain"
)

// TestAuditAutoAnchoring: audit entries accumulate until the threshold,
// then the local chain head lands on the domain ledger (spec §八) — and
// exactly once per threshold window, honoring the low-frequency invariant.
func TestAuditAutoAnchoring(t *testing.T) {
	// A one-admin domain in a temp dir, node over its own store.
	dir := filepath.Join(t.TempDir(), "node")
	id, err := trustdomain.LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := trustdomain.BuildGenesis([]*trustdomain.Identity{id}, 1, "anchor-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := trustdomain.ValidateChain([]*trustdomain.Block{gen})
	if err != nil {
		t.Fatal(err)
	}
	store, err := trustdomain.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(chain); err != nil {
		t.Fatal(err)
	}
	disarm := armAuditAnchoringForTest(func() (*trustdomain.Node, error) {
		return trustdomain.NewNode(id, chain, func() []trustdomain.Peer { return nil },
			trustdomain.NodeOptions{Store: store}), nil
	})
	defer disarm()

	// Audit file in a temp location; entries BEFORE the threshold must NOT
	// anchor, the threshold entry must. Restore globals for later tests.
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	SetAuditPath(auditPath)
	t.Cleanup(func() { SetAuditPath(""); auditLastHash = "" })
	auditLastHash = "" // cold cache for the new file

	for i := 0; i < anchorEveryEntries-1; i++ {
		if err := AppendAudit(Audit{Device: "d", Command: "ps", Class: "read", Status: AuditOK}); err != nil {
			t.Fatal(err)
		}
	}
	if got := chainHeight(t, store); got != 0 { // genesis only
		t.Fatalf("anchored before threshold: height=%d", got)
	}
	if err := AppendAudit(Audit{Device: "d", Command: "ss", Class: "read", Status: AuditOK}); err != nil {
		t.Fatal(err)
	}
	// The anchor block must now be on the chain, carrying the local head.
	if h := chainHeight(t, store); h != 1 {
		t.Fatalf("threshold anchor missing: height=%d", h)
	}
	node := trustdomain.NewNode(id, mustLoad(t, store), nil, trustdomain.NodeOptions{})
	st := node.State()
	if got := st.AuditHead(id.ID()); got == "" || got != AuditChainHead() {
		t.Fatalf("ledger audit head %q != local head %q", got, AuditChainHead())
	}

	// A few more entries stay under the next threshold — no extra block.
	for i := 0; i < 3; i++ {
		if err := AppendAudit(Audit{Device: "d", Command: "who", Class: "read", Status: AuditOK}); err != nil {
			t.Fatal(err)
		}
	}
	if got := chainHeight(t, store); got != 1 {
		t.Fatalf("extra anchor before next threshold: height=%d", got)
	}
}

func chainHeight(t *testing.T, store *trustdomain.Store) int {
	t.Helper()
	c, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return int(c.Height())
}

func mustLoad(t *testing.T, store *trustdomain.Store) *trustdomain.Chain {
	t.Helper()
	c, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}
