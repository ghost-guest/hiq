package trustdomain

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
)

// b32 mirrors mobilebridge's Crockford alphabet so fingerprints render the
// same way across hiq surfaces (QR pairing, member cards, audit logs).
var b32 = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// idLen is 128 bits: enough to be collision-safe as a registry key for the
// lifetime of a domain. mobilebridge.Fingerprint (8 bytes) is deliberately
// shorter for human QR comparison; registry identity wants more bits.
const idLen = 16

// ID returns the registry identifier for a public key:
// base32(SHA256(pub)[:16]). Anyone can recompute it from the key, so the
// ledger stores IDs in state and pubkeys alongside for verification.
func ID(pub []byte) string {
	h := sha256.Sum256(pub)
	return b32.EncodeToString(h[:idLen])
}

// Identity is a long-term Ed25519 keypair. In production the private key
// lives in secret.Store (DPAPI/Keychain/Secret Service — spec §17); this
// type only wraps signing operations.
type Identity struct {
	Public  ed25519.PublicKey
	Private ed25519.PrivateKey
}

// GenerateIdentity creates a fresh long-term identity keypair.
func GenerateIdentity() (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{Public: pub, Private: priv}, nil
}

// ID is this identity's registry identifier.
func (id *Identity) ID() string { return ID(id.Public) }

// Sign returns a signature over msg, or nil if the identity is unusable.
func (id *Identity) Sign(msg []byte) []byte {
	if len(id.Private) != ed25519.PrivateKeySize {
		return nil
	}
	return ed25519.Sign(id.Private, msg)
}

// errNilKey guards callers that construct Identity by hand in tests.
var errNilKey = errors.New("trustdomain: identity has no private key")
