// Package linkpeersignal implements the cloud signaling server (K) for the
// linkpeer × hiq mobile bridge. It is a stateless router: pair matching,
// public-key exchange, and SDP/ICE forwarding between a desktop hiq (S)
// and a mobile linkpeer (C). It never sees business traffic (that flows P2P
// between S and C) and never holds long-term keys.
//
// See docs/LINKPEER_PROTOCOL.md for the protocol and docs/LINKPEER_SIGNAL_SPEC.md
// for the implementation spec.
package linkpeersignal

import (
	"crypto/sha256"
	"encoding/base32"
)

// b32 is a Crockford base32 alphabet (0-9 + ABCDEFGHJKMNPQRSTVWXYZ, with
// I/L/O/U removed to avoid visual confusion). Standard 32-char base32 requires
// exactly 32 symbols, so we cannot also drop 0/1 — but with O/I gone, 0 and 1
// are unambiguous. Used for deviceId and fingerprint encoding.
var b32 = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// devID derives the device identifier from a public key:
// base32(SHA256(pub)[:10]). This is the self-consistency invariant K relies on
// for stateless WS auth (PROTOCOL §4.1): anyone can recompute it from pub, so
// K never needs to store a devId→pub mapping.
func devID(pub []byte) string {
	h := sha256.Sum256(pub)
	return b32.EncodeToString(h[:10])
}

// fingerprint derives the human-comparable fingerprint base32(SHA256(pub)[:8]).
// Used by K only to sanity-check that pub matches the fp a client claims
// (defensive; the real anti-MITM check is done out-of-band by C comparing the
// QR code fp locally).
func fingerprint(pub []byte) string {
	h := sha256.Sum256(pub)
	return b32.EncodeToString(h[:8])
}
