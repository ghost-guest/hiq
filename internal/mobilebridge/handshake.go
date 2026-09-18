package mobilebridge

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/zzycxz/hiq/internal/mobilebridge/proto"
)

// Handshake (PROTOCOL §5): a TLS-1.3-flavored exchange run on the DataChannel
// after it opens. Long-term Ed25519 keys authenticate identity; ephemeral
// X25519 keys provide forward secrecy (discarded once keys are derived).
//
//   C→S ClientHello {eph, nc, sig}
//   S→C ServerHello {eph, ns, sig}
//   both derive c2s/s2c via HKDF(shared, salt=nc‖ns)
//   C→S Finished {th}        (AEAD-sealed with c2s)
//   S→C Finished {th}        (AEAD-sealed with s2c)
//
// On any failure (bad sig, unpaired deviceId, Finished mismatch) the side
// that notices closes the DataChannel without sending anything more — never
// revealing which check failed (enumeration protection).

const hsPrefix = "lpc1."

var (
	ErrBadSig          = errors.New("handshake signature invalid")
	ErrBadEphemeral    = errors.New("handshake ephemeral key invalid")
	ErrFinishedMismatch = errors.New("handshake finished transcript mismatch")
)

// int64Bytes returns ts as 8 little-endian bytes (fixed-width, unambiguous
// concatenation in the signed message).
func int64Bytes(ts int64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(ts))
	return b[:]
}

// hsMessage builds the exact byte layout signed/verified for a hello.
// Layout: "lpc1.<role>" || eph(32) || nonce(16) || cid || sid || ts(8).
// cid/sid are base32 strings (PROTOCOL §2.4) — no length prefix needed since
// they contain no NUL and the trailing ts is fixed-width.
func hsMessage(role string, eph, nonce []byte, cid, sid string, ts int64) []byte {
	m := []byte(hsPrefix + role)
	m = append(m, eph...)
	m = append(m, nonce...)
	m = append(m, []byte(cid)...)
	m = append(m, []byte(sid)...)
	m = append(m, int64Bytes(ts)...)
	return m
}

// BuildClientHello constructs a signed ClientHello from C's long-term key.
func BuildClientHello(longPriv ed25519.PrivateKey, ephPub, nc []byte, cid, sid string, ts int64) proto.ClientHello {
	sig := ed25519.Sign(longPriv, hsMessage("hello_c", ephPub, nc, cid, sid, ts))
	return proto.ClientHello{
		T: "hello_c", Ver: proto.Version,
		Eph: b64(ephPub), Nc: b64(nc),
		Cid: cid, Sid: sid, Ts: ts,
		Sig: b64(sig),
	}
}

// VerifyClientHello checks the signature under the given long-term public key
// and that the ephemeral/nonce are well-formed. Does NOT check pairing status
// — that's the caller's job (so it can refuse silently on revoked devices).
// ErrVersionMismatch 表示 ClientHello 声明的协议版本不符（T10 降级防护）。
var ErrVersionMismatch = errors.New("version_mismatch")

// ErrStaleTS 表示握手 ts 超出新鲜度窗口（P2-1 防握手重放）。
var ErrStaleTS = errors.New("stale_ts")

func VerifyClientHello(pub ed25519.PublicKey, ch proto.ClientHello) error {
	// 版本校验（T10 防降级）：ClientHello 必须声明当前协议版本。
	if ch.Ver != proto.Version {
		return ErrVersionMismatch
	}
	// P2-1: ts 新鲜度校验（±60s），防握手重放
	now := time.Now().UnixMilli()
	if now-ch.Ts > 60000 || ch.Ts-now > 60000 {
		return ErrStaleTS
	}
	eph, err := b64d(ch.Eph)
	if err != nil || len(eph) != 32 {
		return ErrBadEphemeral
	}
	nc, err := b64d(ch.Nc)
	if err != nil || len(nc) != 16 {
		return ErrBadEphemeral
	}
	sig, err := b64d(ch.Sig)
	if err != nil {
		return ErrBadSig
	}
	if !ed25519.Verify(pub, hsMessage("hello_c", eph, nc, ch.Cid, ch.Sid, ch.Ts), sig) {
		return ErrBadSig
	}
	return nil
}

// BuildServerHello constructs the signed S→C reply.
func BuildServerHello(longPriv ed25519.PrivateKey, ephPub, ns []byte, cid, sid string, ts int64) proto.ServerHello {
	sig := ed25519.Sign(longPriv, hsMessage("hello_s", ephPub, ns, cid, sid, ts))
	return proto.ServerHello{
		T: "hello_s", Ver: proto.Version,
		Eph: b64(ephPub), Ns: b64(ns),
		Cid: cid, Sid: sid, Ts: ts,
		Sig: b64(sig),
	}
}

func VerifyServerHello(pub ed25519.PublicKey, sh proto.ServerHello) error {
	eph, err := b64d(sh.Eph)
	if err != nil || len(eph) != 32 {
		return ErrBadEphemeral
	}
	ns, err := b64d(sh.Ns)
	if err != nil || len(ns) != 16 {
		return ErrBadEphemeral
	}
	sig, err := b64d(sh.Sig)
	if err != nil {
		return ErrBadSig
	}
	if !ed25519.Verify(pub, hsMessage("hello_s", eph, ns, sh.Cid, sh.Sid, sh.Ts), sig) {
		return ErrBadSig
	}
	return nil
}

// ClientEphPub / ServerEphPub decode the ephemeral public key from a hello.
func ClientEphPub(ch proto.ClientHello) ([]byte, error) { return b64d(ch.Eph) }
func ServerEphPub(sh proto.ServerHello) ([]byte, error) { return b64d(sh.Eph) }
func ClientNonce(ch proto.ClientHello) ([]byte, error)  { return b64d(ch.Nc) }
func ServerNonce(sh proto.ServerHello) ([]byte, error)  { return b64d(sh.Ns) }

// CompletedHandshake bundles the outputs a Conn needs after a successful
// handshake: the two direction keys (to wrap AEADs) and the transcript hash
// (to verify Finished).
type CompletedHandshake struct {
	C2S, S2C   []byte // 32B each
	Transcript []byte // SHA256(ClientHelloJSON || ServerHelloJSON)
}

// CompleteHandshake runs ECDH + HKDF from the two helos and our ephemeral
// private key, returning the session keys + transcript hash. Both sides call
// this with the same (ch, sh) pair and their own ephPriv.
func CompleteHandshake(ephPriv *ecdh.PrivateKey, peerEphPub, nc, ns []byte, chJSON, shJSON []byte) (*CompletedHandshake, error) {
	shared, err := ECDHShared(ephPriv, peerEphPub)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}
	c2s, s2c := DeriveSessionKeys(shared, nc, ns)
	h := sha256.New()
	h.Write(chJSON)
	h.Write(shJSON)
	return &CompletedHandshake{C2S: c2s, S2C: s2c, Transcript: h.Sum(nil)}, nil
}

// FinishedMessage builds the Finished plaintext (to be AEAD-sealed by caller).
func FinishedMessage(role string, transcript []byte) proto.Finished {
	return proto.Finished{
		T:    "fin",
		Role: role,
		Th:   b64(transcript[:8]),
	}
}

// VerifyFinished checks the decrypted Finished plaintext against the transcript.
func VerifyFinished(f proto.Finished, role string, transcript []byte) error {
	if f.T != "fin" || f.Role != role {
		return ErrFinishedMismatch
	}
	want := b64(transcript[:8])
	if !ConstantTimeEqual([]byte(f.Th), []byte(want)) {
		return ErrFinishedMismatch
	}
	return nil
}
