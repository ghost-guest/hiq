package nettrans

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/trustdomain"
)

func TestBeaconVerify(t *testing.T) {
	f := newFleet(t)
	A := f.admins[0]

	chainA := f.genesis()
	nodeA := trustdomain.NewNode(A, chainA, func() []trustdomain.Peer { return nil }, trustdomain.NodeOptions{})
	domain := trustdomain.DomainID(chainA)
	now := time.Now()

	good := BuildBeacon(A, nodeA, "10.0.0.21:7123", now.UnixMilli())
	if !good.Verify(nodeA.Chain().State(), domain, now.UnixMilli()) {
		t.Fatal("valid beacon rejected")
	}

	// Foreign domain.
	bad := good
	bad.DomainID = "other-domain"
	if bad.Verify(nodeA.Chain().State(), domain, now.UnixMilli()) {
		t.Fatal("foreign-domain beacon accepted")
	}

	// Stale timestamp (replayed old address).
	stale := BuildBeacon(A, nodeA, "10.0.0.99:7123", now.Add(-10*time.Minute).UnixMilli())
	if stale.Verify(nodeA.Chain().State(), domain, now.UnixMilli()) {
		t.Fatal("stale beacon accepted")
	}

	// Stranger's key (not a member).
	stranger, _ := trustdomain.GenerateIdentity()
	forged := BuildBeacon(stranger, nodeA, "10.0.0.66:7123", now.UnixMilli())
	if forged.Verify(nodeA.Chain().State(), domain, now.UnixMilli()) {
		t.Fatal("stranger beacon accepted")
	}

	// Tampered address after signing.
	tampered := good
	tampered.Addr = "10.0.0.66:7123"
	if tampered.Verify(nodeA.Chain().State(), domain, now.UnixMilli()) {
		t.Fatal("tampered beacon accepted")
	}
}

func TestDiscoveryLoopback(t *testing.T) {
	f := newFleet(t)
	A, B := f.admins[0], f.admins[1]

	// B serves a real node; A listens for beacons.
	f.addrB, f.nodeB = f.serve(B, f.genesis(), nil)
	chainA := f.genesis()
	nodeA := trustdomain.NewNode(A, chainA, func() []trustdomain.Peer { return nil }, trustdomain.NodeOptions{})

	disc := NewDiscoverer(3 * DefaultBeaconInterval)
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ListenBeacons(ctx, pc, disc, nodeA) }()
	defer pc.Close()

	// Unicast a valid beacon (loopback stands in for the broadcast channel).
	dst := pc.LocalAddr().String()
	conn, err := net.Dial("udp4", dst)
	if err != nil {
		t.Fatal(err)
	}
	beacon := BuildBeacon(B, f.nodeB, f.addrB, time.Now().UnixMilli())
	if err := SendBeacon(conn, beacon); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		addrs := disc.Addresses(nodeA.Identity())
		for _, a := range addrs {
			if a == f.addrB {
				goto found
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("beacon not discovered; addrs=%v", addrs)
		}
		time.Sleep(20 * time.Millisecond)
	}
found:
	// A foreign-domain beacon is dropped, not remembered.
	foreign := BuildBeacon(B, f.nodeB, f.addrB, time.Now().UnixMilli())
	foreign.DomainID = "other"
	if err := SendBeacon(conn, foreign); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	for _, a := range disc.Addresses(nodeA.Identity()) {
		if a != f.addrB {
			t.Fatalf("foreign beacon leaked: %v", a)
		}
	}
}
