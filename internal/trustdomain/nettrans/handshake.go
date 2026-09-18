// Package nettrans implements the trust domain Peer transport over TCP
// with mutual authentication: both sides sign ephemeral X25519 keys with
// their long-term Ed25519 domain identity, and each side resolves the
// other's static key from its OWN copy of the ledger — the chain's member
// registry IS the trust store (spec §四/§5.1). Unknown or revoked peers
// fail the lookup and the connection closes without revealing why
// (enumeration protection, same posture as linkpeer).
//
// Reuses the mobilebridge handshake/frame layer verbatim (Ed25519 +
// X25519 + HKDF + AES-256-GCM, PROTOCOL §5/§6). LAN/direct addresses are
// served by this TCP path; WebRTC/NAT traversal will arrive as an
// alternative dialer — the Peer surface does not care.
package nettrans

import (
	"crypto/cipher"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/zzycxz/hiq/internal/mobilebridge"
	"github.com/zzycxz/hiq/internal/mobilebridge/proto"
	"github.com/zzycxz/hiq/internal/trustdomain"
)

// KeyLookup resolves an active member ID to its registered public key —
// backed by Chain.State() at the time of the handshake. Revoked or unknown
// members must return (nil, false): the handshake then fails closed.
type KeyLookup func(memberID string) ([]byte, bool)

// ChainLookup adapts a chain into a KeyLookup over its active members.
func ChainLookup(c *trustdomain.Chain) KeyLookup {
	return func(memberID string) ([]byte, bool) {
		m := c.State().Member(memberID)
		if m == nil {
			return nil, false
		}
		return m.PublicKey, true
	}
}

// errRejected masks all lookup/signature failures to one error — callers
// close the connection silently, never disclosing which check failed.
var errRejected = errors.New("nettrans: peer rejected")

const (
	hsTimeout = 10 * time.Second
	maxRawLen = 1 << 20 // handshake and message size ceiling (1 MiB)
)

// session is one authenticated, encrypted connection.
type session struct {
	conn   net.Conn
	sendAE cipher.AEAD
	recvAE cipher.AEAD
	sendMu chan struct{}
	sendSN uint64
	recvSN uint64
	PeerID string
	MyID   string
}

// dialHandshake runs the client side: hello, verify the server against our
// own chain, derive keys, confirm. Returns the authenticated session.
func dialHandshake(conn net.Conn, id *trustdomain.Identity, lookup KeyLookup) (*session, error) {
	_ = conn.SetDeadline(time.Now().Add(hsTimeout))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	eph, err := mobilebridge.GenerateEphemeral()
	if err != nil {
		return nil, err
	}
	nc, err := mobilebridge.Random(16)
	if err != nil {
		return nil, err
	}
	// sid="" : the dialer doesn't know the listener's ID beforehand; it
	// verifies the ServerHello against its own ledger lookup instead.
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
	key, ok := lookup(sh.Sid)
	if !ok || len(key) != ed25519.PublicKeySize {
		return nil, errRejected
	}
	if err := mobilebridge.VerifyServerHello(ed25519.PublicKey(key), sh); err != nil {
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

	// Key confirmation: dialer sends Finished under c2s, listener under s2c.
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
	return s, nil
}

// serveHandshake runs the server side: verify the dialer against our own
// chain (active members only), reply, derive, confirm.
func serveHandshake(conn net.Conn, id *trustdomain.Identity, lookup KeyLookup) (*session, error) {
	_ = conn.SetDeadline(time.Now().Add(hsTimeout))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	chRaw, err := readRaw(conn)
	if err != nil {
		return nil, err
	}
	var ch proto.ClientHello
	if err := json.Unmarshal(chRaw, &ch); err != nil {
		return nil, errRejected
	}
	key, ok := lookup(ch.Cid)
	if !ok || len(key) != ed25519.PublicKeySize {
		return nil, errRejected
	}
	if err := mobilebridge.VerifyClientHello(ed25519.PublicKey(key), ch); err != nil {
		return nil, errRejected
	}
	peerEph, err := mobilebridge.ClientEphPub(ch)
	if err != nil {
		return nil, errRejected
	}
	nc, err := mobilebridge.ClientNonce(ch)
	if err != nil {
		return nil, errRejected
	}

	eph, err := mobilebridge.GenerateEphemeral()
	if err != nil {
		return nil, err
	}
	ns, err := mobilebridge.Random(16)
	if err != nil {
		return nil, err
	}
	sh := mobilebridge.BuildServerHello(id.Private, eph.PublicKey().Bytes(), ns, ch.Cid, id.ID(), time.Now().UnixMilli())
	shJSON, err := json.Marshal(sh)
	if err != nil {
		return nil, err
	}
	if err := writeRaw(conn, shJSON); err != nil {
		return nil, err
	}
	hs, err := mobilebridge.CompleteHandshake(eph, peerEph, nc, ns, chRaw, shJSON)
	if err != nil {
		return nil, err
	}
	// Server receives under c2s and sends under s2c.
	s, err := newSession(conn, hs, false, id.ID(), ch.Cid)
	if err != nil {
		return nil, err
	}

	var finC proto.Finished
	if err := s.recv(&finC); err != nil {
		return nil, err
	}
	if err := mobilebridge.VerifyFinished(finC, "c", hs.Transcript); err != nil {
		return nil, err
	}
	if err := s.send(mobilebridge.FinishedMessage("s", hs.Transcript)); err != nil {
		return nil, err
	}
	return s, nil
}

// newSession wraps AEADs around the completed handshake. isClient selects
// direction keys (client sends c2s, server sends s2c — PROTOCOL §5).
func newSession(conn net.Conn, hs *mobilebridge.CompletedHandshake, isClient bool, myID, peerID string) (*session, error) {
	c2s, err := mobilebridge.NewAEAD(hs.C2S)
	if err != nil {
		return nil, err
	}
	s2c, err := mobilebridge.NewAEAD(hs.S2C)
	if err != nil {
		return nil, err
	}
	s := &session{conn: conn, sendMu: make(chan struct{}, 1), MyID: myID, PeerID: peerID}
	if isClient {
		s.sendAE, s.recvAE = c2s, s2c
	} else {
		s.sendAE, s.recvAE = s2c, c2s
	}
	s.sendMu <- struct{}{}
	return s, nil
}

// --- raw length-prefixed JSON for hellos (pre-encryption) --------------------

func writeRaw(conn net.Conn, data []byte) error {
	if len(data) > maxRawLen {
		return fmt.Errorf("nettrans: raw message too large")
	}
	var len4 [4]byte
	binary.BigEndian.PutUint32(len4[:], uint32(len(data)))
	if _, err := conn.Write(len4[:]); err != nil {
		return err
	}
	_, err := conn.Write(data)
	return err
}

func readRaw(conn net.Conn) ([]byte, error) {
	var len4 [4]byte
	if _, err := io.ReadFull(conn, len4[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(len4[:])
	if n > maxRawLen {
		return nil, fmt.Errorf("nettrans: raw message too large")
	}
	data := make([]byte, n)
	_, err := io.ReadFull(conn, data)
	return data, err
}
