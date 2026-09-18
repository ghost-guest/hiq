//go:build !windows

package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"os"
)

// Legacy v1 Protect/Unprotect and the machine-key source for the degraded
// KEK fallback (see kek_machine.go). The derived key is NOT secret — any local
// process can recompute it from hostname + home dir — which is why v2 stores
// use an OS-keystore KEK (Keychain/Secret Service/passphrase) instead, and
// this key material survives only to (a) decrypt pre-v2 files during the
// one-time upgrade and (b) keep headless machines without any keystore
// working, flagged as degraded via SecurityMode.

func machineKey() []byte {
	host, _ := os.Hostname()
	home, _ := os.UserHomeDir()
	h := sha256.Sum256([]byte(legacySecretSalt + host + ":" + home))
	return h[:]
}

// Protect encrypts plaintext with AES-GCM under a machine-derived key.
func Protect(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	block, err := aes.NewCipher(machineKey())
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	// nonce is prepended; ciphertext = nonce || gcm-tag'd data.
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Unprotect decrypts an AES-GCM blob produced by Protect.
func Unprotect(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, nil
	}
	block, err := aes.NewCipher(machineKey())
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, errors.New("secret: ciphertext too short")
	}
	return gcm.Open(nil, ciphertext[:ns], ciphertext[ns:], nil)
}
