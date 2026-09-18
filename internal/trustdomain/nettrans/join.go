package nettrans

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/zzycxz/hiq/internal/mobilebridge"
	"github.com/zzycxz/hiq/internal/mobilebridge/proto"
	"github.com/zzycxz/hiq/internal/trustdomain"
)

var (
	ErrWrongDomain = errors.New("nettrans: peer serves a different domain")
	ErrNotAdmitted = errors.New("nettrans: this identity is not admitted to the domain yet")
)

// JoinChain is the new-member bootstrap: dial a member, pull and fully
// validate its ledger, and persist nothing (the caller saves).
//
// Trust anchoring: wantDomainID (the genesis hash, learned out-of-band —
// same posture as linkpeer's QR fingerprint comparison) is what makes the
// post-hoc verification sound. The server's hello signature is verified
// against the member key REGISTERED IN THE DOWNLOADED CHAIN after the
// fact; a MITM serving a forged chain produces a different genesis hash
// and fails the pin. The joiner must also find its own admission in the
// chain — you cannot join a domain that has not admitted you.
func JoinChain(addr string, id *trustdomain.Identity, wantDomainID string) (*trustdomain.Chain, error) {
	conn, err := net.DialTimeout("tcp", addr, hsTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(hsTimeout))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	// Handshake with deferred server authentication: encryption (X25519 +
	// HKDF) is active immediately; the server's identity claim (sh.Sid) is
	// verified against the pulled chain below.
	eph, err := mobilebridge.GenerateEphemeral()
	if err != nil {
		return nil, err
	}
	nc, err := mobilebridge.Random(16)
	if err != nil {
		return nil, err
	}
	ch := mobilebridge.BuildClientHello(id.Private, eph.PublicKey().Bytes(), nc, id.ID(), "", time.Now().UnixMilli())
	chJSON, err := json.Marshal(ch)
	if err != nil {
		return nil, err
	}
	if err := writeRaw(conn, chJSON); err != nil {
		return nil, err
	}
	shRaw, err := readRaw(conn)
	if err != nil {
		return nil, err
	}
	var sh proto.ServerHello
	if err := json.Unmarshal(shRaw, &sh); err != nil {
		return nil, errRejected
	}
	peerEph, err := mobilebridge.ServerEphPub(sh)
	if err != nil {
		return nil, errRejected
	}
	ns, err := mobilebridge.ServerNonce(sh)
	if err != nil {
		return nil, errRejected
	}
	hs, err := mobilebridge.CompleteHandshake(eph, peerEph, nc, ns, chJSON, shRaw)
	if err != nil {
		return nil, err
	}
	s, err := newSession(conn, hs, true, id.ID(), sh.Sid)
	if err != nil {
		return nil, err
	}
	if err := s.send(mobilebridge.FinishedMessage("c", hs.Transcript)); err != nil {
		return nil, err
	}
	var finOK proto.Finished
	if err := s.recv(&finOK); err != nil {
		return nil, err
	}
	if err := mobilebridge.VerifyFinished(finOK, "s", hs.Transcript); err != nil {
		return nil, err
	}

	// Pull the peer's full chain.
	var stMsg msg
	if err := s.send(msg{Kind: kindStatus}); err != nil {
		return nil, err
	}
	if err := s.recv(&stMsg); err != nil {
		return nil, err
	}
	if stMsg.Status == nil {
		return nil, fmt.Errorf("nettrans: no status reply")
	}
	var reply msg
	if err := s.send(msg{Kind: kindGetBlocks, From: 0, To: stMsg.Status.Height}); err != nil {
		return nil, err
	}
	if err := s.recv(&reply); err != nil {
		return nil, err
	}
	if reply.Kind != kindBlocks {
		return nil, fmt.Errorf("nettrans: unexpected join reply %q", reply.Kind)
	}
	chain, err := trustdomain.ValidateChain(reply.Blocks)
	if err != nil {
		return nil, fmt.Errorf("nettrans: peer served an invalid chain: %w", err)
	}

	// Pin check: the genesis hash must equal the expected domain ID.
	if got := trustdomain.DomainID(chain); got != wantDomainID {
		return nil, fmt.Errorf("%w: want %s got %s", ErrWrongDomain, wantDomainID, got)
	}

	// Post-hoc server authentication: the Sid the server claimed must be
	// an active member whose REGISTERED key verifies the hello signature —
	// proving the ECDH was with the real key holder, not a relay.
	member := chain.State().Member(sh.Sid)
	if member == nil {
		return nil, fmt.Errorf("nettrans: server identity %s not a member", sh.Sid)
	}
	if len(member.PublicKey) != ed25519.PublicKeySize {
		return nil, errRejected
	}
	if err := mobilebridge.VerifyServerHello(ed25519.PublicKey(member.PublicKey), sh); err != nil {
		return nil, errRejected
	}

	// The joiner must already be admitted — admission precedes joining.
	if !chain.State().IsMember(id.ID()) {
		return nil, ErrNotAdmitted
	}
	return chain, nil
}
