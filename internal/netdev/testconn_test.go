package netdev

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/netdev/transport"
	"golang.org/x/crypto/ssh"
)

// testAddr adapts an address string to net.Addr for the trust helpers.
type testAddr string

func (a testAddr) Network() string { return "tcp" }
func (a testAddr) String() string  { return string(a) }

func decoyKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer.PublicKey()
}

// TestTestConnectionUnknownKeyThenTrustThenOK exercises the full two-step
// TOFU flow against the simulator: first dial rejects with the fingerprint,
// trusting it (with the captured key) makes the second dial succeed.
func TestTestConnectionUnknownKeyThenTrustThenOK(t *testing.T) {
	sim := startSimDevice(t)
	m, _ := testManager(t, sim)

	r := m.TestConnection(context.Background(), "sw1")
	if r.Status != TestUnknownHostKey {
		t.Fatalf("status = %s (%s), want unknown-host-key", r.Status, r.Detail)
	}
	if r.Fingerprint == "" || r.KeyType == "" {
		t.Fatalf("question incomplete: %+v", r)
	}

	// Trusting a fingerprint that was never captured fails.
	if err := TrustHostKey("SHA256:never-captured"); err == nil {
		t.Fatal("uncaptured fingerprint accepted")
	}

	if err := TrustHostKey(r.Fingerprint); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}

	r2 := m.TestConnection(context.Background(), "sw1")
	if r2.Status != TestOK {
		t.Fatalf("after trust: %+v", r2)
	}

	// One-shot: the same fingerprint cannot be re-trusted (stale capture).
	if err := TrustHostKey(r.Fingerprint); err == nil {
		t.Fatal("stale capture re-trusted")
	}
}

// A key already on record for the address that CONTRADICTS the presented one
// is a hard failure — never promptable, never trustable.
func TestTestConnectionMismatchIsHardFail(t *testing.T) {
	sim := startSimDevice(t)
	m, _ := testManager(t, sim)

	host, portStr, _ := net.SplitHostPort(sim.addr)
	// Record a decoy key for the simulator's exact address before connecting.
	if err := transport.TrustKey(host, testAddr(net.JoinHostPort(host, portStr)), decoyKey(t)); err != nil {
		t.Fatal(err)
	}

	r := m.TestConnection(context.Background(), "sw1")
	if r.Status != TestError || !strings.Contains(r2Detail(r), "MISMATCH") {
		t.Fatalf("mismatch not surfaced as hard error: %+v", r)
	}
	// The mismatch never populated the trust cache.
	if err := TrustHostKey(r.Fingerprint); err == nil && r.Fingerprint != "" {
		t.Fatal("mismatch somehow became trustable")
	}
}

func r2Detail(r TestResult) string { return r.Detail }
