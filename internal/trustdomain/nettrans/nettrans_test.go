package nettrans

import (
	"context"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/trustdomain"
)

// fleet is a real-socket three-node fleet: two founding admins (quorum 2)
// and a later-admitted member. Peers are statically wired full-mesh — the
// bootstrap_peers deployment shape (spec §四): gossip is pull-based, so
// every member must be able to reach every other.
type fleet struct {
	t       *testing.T
	admins  [2]*trustdomain.Identity
	guest   *trustdomain.Identity
	genesis func() *trustdomain.Chain

	nodeA, nodeB, nodeC *trustdomain.Node
	addrA, addrB, addrC string
	cancel              context.CancelFunc
}

func newFleet(t *testing.T) *fleet {
	t.Helper()
	f := &fleet{t: t}
	for i := range f.admins {
		var err error
		if f.admins[i], err = trustdomain.GenerateIdentity(); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	if f.guest, err = trustdomain.GenerateIdentity(); err != nil {
		t.Fatal(err)
	}
	gen, err := trustdomain.BuildGenesis(f.admins[:], 2, "net-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	f.genesis = func() *trustdomain.Chain {
		c, err := trustdomain.ValidateChain([]*trustdomain.Block{gen})
		if err != nil {
			t.Fatalf("genesis: %v", err)
		}
		return c
	}
	return f
}

// serve starts a listener-backed node for identity id over chain c and
// returns its address. handler (nil = refuse delegations) is the work
// executor registered on this node.
func (f *fleet) serve(id *trustdomain.Identity, c *trustdomain.Chain, handler WorkHandler) (string, *trustdomain.Node) {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if f.cancel == nil {
		f.cancel = cancel
	} else {
		// Chain the cancels so Stop releases everything.
		prev := f.cancel
		f.cancel = func() { prev(); cancel() }
	}
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		f.t.Fatal(err)
	}
	node := trustdomain.NewNode(id, c, func() []trustdomain.Peer { return nil },
		trustdomain.NodeOptions{})
	go func() { _ = Serve(ctx, ln, id, node, handler) }()
	f.t.Cleanup(f.stop)
	return ln.Addr().String(), node
}

func (f *fleet) stop() {
	if f.cancel != nil {
		f.cancel()
	}
}

// TestNetFleetLifecycle runs the full §14.2 pipeline across real TCP
// sockets: quorum approval travels the wire, members sync by pulling.
func TestNetFleetLifecycle(t *testing.T) {
	f := newFleet(t)
	A, B := f.admins[0], f.admins[1]

	// All three nodes serve from the start; peers are wired full-mesh.
	f.addrA, f.nodeA = f.serve(A, f.genesis(), nil)
	f.addrB, f.nodeB = f.serve(B, f.genesis(), nil)
	f.addrC, f.nodeC = f.serve(f.guest, f.genesis(), nil)
	mesh := func() {
		f.nodeA.SetPeers(func() []trustdomain.Peer {
			return []trustdomain.Peer{
				NewNetPeer(f.addrB, A, ChainLookup(f.nodeA.Chain())),
				NewNetPeer(f.addrC, A, ChainLookup(f.nodeA.Chain())),
			}
		})
		f.nodeB.SetPeers(func() []trustdomain.Peer {
			return []trustdomain.Peer{
				NewNetPeer(f.addrA, B, ChainLookup(f.nodeB.Chain())),
				NewNetPeer(f.addrC, B, ChainLookup(f.nodeB.Chain())),
			}
		})
		f.nodeC.SetPeers(func() []trustdomain.Peer {
			return []trustdomain.Peer{
				NewNetPeer(f.addrA, f.guest, ChainLookup(f.nodeC.Chain())),
				NewNetPeer(f.addrB, f.guest, ChainLookup(f.nodeC.Chain())),
			}
		})
	}
	mesh()

	// 1. Admit the guest: A signs, B's approval crosses the wire.
	if err := f.nodeA.ProposeQuorum(func(parent trustdomain.Hash) (*trustdomain.Record, error) {
		rec := trustdomain.NewMemberRecord(f.guest.Public, "host-c", false, 1000)
		if err := rec.SignAs(A, parent); err != nil {
			return nil, err
		}
		return rec, nil
	}); err != nil {
		t.Fatalf("admit over wire: %v", err)
	}

	// 2. B pulls the admission from A.
	f.nodeB.Tick()
	if !f.nodeB.State().IsMember(f.guest.ID()) {
		t.Fatal("B did not sync the admission")
	}

	// 3. The guest node catches up its own admission.
	f.nodeC.Tick()
	if !f.nodeC.State().IsMember(f.guest.ID()) {
		t.Fatal("guest did not catch up its own admission")
	}

	// 4. Guest attests; A pulls the attestation from C's listener.
	if err := f.nodeC.Attest(trustdomain.AttestationPayload{Version: "v-net", PolicyHash: "ph"}, 2000); err != nil {
		t.Fatalf("attest: %v", err)
	}
	f.nodeA.Tick()
	if a := f.nodeA.State().LatestAttestation(f.guest.ID()); a == nil || a.Version != "v-net" {
		t.Fatal("attestation did not propagate to A")
	}

	// 5. Revoke the guest (quorum over the wire); B pulls and sees it.
	if err := f.nodeA.ProposeQuorum(func(parent trustdomain.Hash) (*trustdomain.Record, error) {
		rec := trustdomain.NewRevocationRecord(f.guest.ID(), "test", 3000)
		if err := rec.SignAs(A, parent); err != nil {
			return nil, err
		}
		return rec, nil
	}); err != nil {
		t.Fatalf("revoke over wire: %v", err)
	}
	f.nodeB.Tick()
	if !f.nodeB.State().IsRevoked(f.guest.ID()) {
		t.Fatal("revocation did not propagate to B")
	}
}

// setPeers was folded into Node.SetPeers (wiring hook).

// TestNetHandshakeRejectsStranger: an identity not in the listener's
// ledger cannot even complete the handshake — and sees no explanation.
func TestNetHandshakeRejectsStranger(t *testing.T) {
	f := newFleet(t)
	A := f.admins[0]
	addr, nodeA := f.serve(A, f.genesis(), nil)

	stranger, err := trustdomain.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	peer := NewNetPeer(addr, stranger, ChainLookup(f.genesis()))
	if st := peer.Status(); st.Height != 0 || st.Head != (trustdomain.Hash{}) {
		t.Fatalf("stranger extracted a status: %+v", st)
	}
	if blocks := peer.Blocks(0, nodeA.Chain().Height()); blocks != nil {
		t.Fatalf("stranger pulled %d blocks", len(blocks))
	}
}

// TestNetReconnectAfterDrop: a broken connection is transparently
// redialed on the next use.
func TestNetReconnectAfterDrop(t *testing.T) {
	f := newFleet(t)
	A, B := f.admins[0], f.admins[1]
	f.addrA, f.nodeA = f.serve(A, f.genesis(), nil)
	f.addrB, f.nodeB = f.serve(B, f.genesis(), nil)

	peer := NewNetPeer(f.addrA, B, ChainLookup(f.nodeB.Chain()))
	if st := peer.Status(); st.Height != 0 {
		t.Fatalf("expected height 0, got %d", st.Height)
	}
	peer.drop()
	if st := peer.Status(); st.Height != 0 {
		t.Fatalf("redial failed: %+v", st)
	}
}

// TestDelegationOverWire: a member exercises a capability token against a
// remote executor over a real TCP session — the §7.3 end-to-end path:
// gate on the executor side, handler only sees verified work, out-of-scope
// requests refused.
func TestDelegationOverWire(t *testing.T) {
	f := newFleet(t)
	A, B := f.admins[0], f.admins[1]

	var executed []*trustdomain.Delegation
	handler := func(node *trustdomain.Node, del *trustdomain.Delegation, payload []byte) ([]byte, error) {
		executed = append(executed, del)
		return append([]byte("out:"), payload...), nil
	}
	f.addrA, f.nodeA = f.serve(A, f.genesis(), handler)
	f.addrB, f.nodeB = f.serve(B, f.genesis(), nil)
	// A reaches B for quorum co-signatures.
	f.nodeA.SetPeers(func() []trustdomain.Peer {
		return []trustdomain.Peer{NewNetPeer(f.addrB, A, ChainLookup(f.nodeA.Chain()))}
	})

	// Admit the guest over the wire, then issue it a scoped token.
	if err := f.nodeA.ProposeQuorum(func(parent trustdomain.Hash) (*trustdomain.Record, error) {
		rec := trustdomain.NewMemberRecord(f.guest.Public, "host-c", false, 1000)
		if err := rec.SignAs(A, parent); err != nil {
			return nil, err
		}
		return rec, nil
	}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	now := uint64(time.Now().Unix())
	if err := f.nodeA.IssueToken(trustdomain.TokenPayload{
		TokenID: "tok-net", SubjectID: f.guest.ID(), Resource: "res-db",
		Operations: []string{"read"}, ExpiresAt: now + 3600,
	}, now); err != nil {
		t.Fatalf("issue: %v", err)
	}

	// The guest joins the mesh and syncs admission + token.
	f.addrC, f.nodeC = f.serve(f.guest, f.genesis(), nil)
	f.nodeC.SetPeers(func() []trustdomain.Peer {
		return []trustdomain.Peer{NewNetPeer(f.addrA, f.guest, ChainLookup(f.nodeC.Chain()))}
	})
	f.nodeC.Tick()

	// Authorized work executes and returns the handler's output.
	peerA := NewNetPeer(f.addrA, f.guest, ChainLookup(f.nodeC.Chain()))
	out, err := f.nodeC.RequestWork(peerA, "tok-net", "res-db", "read", []byte("uptime"), 600, now+1)
	if err != nil {
		t.Fatalf("delegated work failed: %v", err)
	}
	if string(out) != "out:uptime" {
		t.Fatalf("handler output = %q", out)
	}
	if len(executed) != 1 || executed[0].Operation != "read" {
		t.Fatalf("handler saw %d delegations", len(executed))
	}

	// Out-of-scope operation is refused BEFORE the handler (§7.3 gate).
	if _, err := f.nodeC.RequestWork(peerA, "tok-net", "res-db", "write", []byte("rm"), 600, now+1); err == nil {
		t.Fatal("out-of-scope work executed")
	}
	if len(executed) != 1 {
		t.Fatalf("handler ran for refused work: %d", len(executed))
	}

	// Tampered payload does not match the delegation's hash.
	if _, err := peerA.Delegate(&trustdomain.Delegation{
		TokenID: "tok-net", SubjectID: f.guest.ID(),
		Resource: "res-db", Operation: "read",
		PayloadHash: []byte("wrong"), IssuedAt: now, ExpiresAt: now + 600,
	}, []byte("swapped")); err == nil {
		t.Fatal("payload swap accepted")
	}
	if len(executed) != 1 {
		t.Fatalf("handler ran for tampered work: %d", len(executed))
	}
}
