package mobilebridge

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
)

// Frame layout on the DataChannel (PROTOCOL §6):
//
//	[1B ver=1][8B seq LE][12B nonce][N B ciphertext][16B GCM tag]
//
// AAD = ver(1) || seq(8): the header is itself integrity-protected, so an
// attacker can't tamper with seq or version without failing authentication.
//
// Each direction uses its own key (c2s / s2c) and its own seq counter
// starting at 0. Receivers reject any seq ≤ their recvMaxSeq to defeat replay.
//
// The 2^32-rekey invariant (HIQ_SPEC §11.5): when a direction's seq
// approaches 2^32, the connection MUST rekey (new X25519, new keys). This
// keeps AES-GCM nonce-collision probability negligible for the key's lifetime.
const (
	FrameVer    = 1
	frameHdrLen = 1 + 8 // ver + seq
	frameNonce  = 12
	frameTag    = 16
	frameMinLen = frameHdrLen + frameNonce + frameTag

	// RekeyThreshold is the seq at which a direction must rotate keys.
	// 2^32 frames at ~1KB each ≈ 4TB — far beyond any real connection, but
	// we enforce the invariant rather than relying on that.
	RekeyThreshold = 1 << 32
)

var (
	ErrShortFrame = errors.New("frame too short")
	ErrBadVersion = errors.New("unsupported frame version")
)

// SealFrame encrypts plaintext into a complete frame. The caller supplies the
// 12-byte nonce (random per frame) and tracks seq (monotonic per direction).
func SealFrame(aead cipher.AEAD, seq uint64, nonce, plaintext []byte) []byte {
	var hdr [frameHdrLen]byte
	hdr[0] = FrameVer
	binary.LittleEndian.PutUint64(hdr[1:], seq)
	out := make([]byte, 0, frameHdrLen+frameNonce+len(plaintext)+frameTag)
	out = append(out, hdr[:]...)
	out = append(out, nonce...)
	// Seal appends ciphertext+tag; AAD = header bytes.
	return aead.Seal(out, nonce, plaintext, hdr[:])
}

// OpenFrame authenticates+decrypts a frame. It returns the seq (so the caller
// can enforce anti-replay against its recvMaxSeq) and the plaintext.
// Authentication failure (tag mismatch / truncation / version mismatch) → error.
func OpenFrame(aead cipher.AEAD, frame []byte) (seq uint64, plaintext []byte, err error) {
	if len(frame) < frameMinLen {
		return 0, nil, ErrShortFrame
	}
	if frame[0] != FrameVer {
		return 0, nil, ErrBadVersion
	}
	seq = binary.LittleEndian.Uint64(frame[1:9])
	nonce := frame[9 : 9+frameNonce]
	ciphertext := frame[9+frameNonce:]
	hdr := frame[:frameHdrLen] // AAD
	plaintext, err = aead.Open(nil, nonce, ciphertext, hdr)
	return seq, plaintext, err
}
