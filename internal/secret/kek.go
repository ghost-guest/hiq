package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
)

// KEK (key-encryption key) architecture: the secrets file is encrypted with
// AES-256-GCM under a per-store KEK. Where the KEK itself lives is the only
// platform-specific decision, and it is what equalizes at-rest strength across
// Windows/macOS/Linux:
//
//	Windows  random KEK, DPAPI-wrapped, stored in the file itself (self-
//	        contained; only the same Windows user can unwrap it)
//	macOS    random KEK in the Keychain (via /usr/bin/security, zero CGO)
//	Linux    random KEK in the Secret Service (gnome-keyring/KWallet, D-Bus)
//	         — headless (no session keyring): passphrase-derived (argon2id) if
//	         HIQ_SECRET_PASSPHRASE[_FILE] is configured, else a
//	         machine-bound fallback flagged as degraded (see kek_machine.go)
//
// kekId (random per store) doubles as the derivation salt for the
// deterministic providers and as the keystore account name, so the main store
// and mobilebridge.enc.json each get their own KEK.

// errNoKEK reports that a provider holds no KEK for the given id (distinct
// from a transport failure — it means "try the next provider").
var errNoKEK = errors.New("secret: provider holds no KEK for this store")

const (
	kekSize = 32
	// machineProviderName marks the machine-bound fallback; SecurityMode
	// reports degraded=true whenever it is the active backend.
	machineProviderName = "machine"
)

// kekProvider is the per-platform source of the KEK.
type kekProvider interface {
	// Name is a stable backend identifier surfaced via SecurityMode/doctor
	// ("dpapi", "keychain", "secret-service", "passphrase", "machine").
	Name() string
	// Available reports whether the backend can be reached right now (probe,
	// cheap; called before every Create/Fetch attempt).
	Available() bool
	// Create provisions a NEW KEK for a store identified by id. Keystore
	// backends generate random bytes and persist them; deterministic backends
	// (passphrase/machine) derive instead — the returned key is whatever
	// Fetch(id, inFile) would later return. inFile is the optional
	// to-be-embedded blob (Windows DPAPI wrap); nil when the KEK lives
	// out-of-file.
	Create(id string) (kek, inFile []byte, err error)
	// Fetch returns the KEK for id, or errNoKEK when this provider has none.
	Fetch(id string, inFile []byte) ([]byte, error)
}

// sealSecret encrypts plaintext under kek: base64(nonce ‖ AES-256-GCM).
func sealSecret(kek, plaintext []byte) (string, error) {
	gcm, err := newGCM(kek)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// openSecret inverts sealSecret. Fails on any wrong key (GCM auth).
func openSecret(kek []byte, enc string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(kek)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("secret: entry shorter than a nonce")
	}
	return gcm.Open(nil, raw[:ns], raw[ns:], nil)
}

func newGCM(kek []byte) (cipher.AEAD, error) {
	if len(kek) != kekSize {
		return nil, errors.New("secret: invalid KEK size")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// randomKekID returns a fresh hex store identifier (also the derivation salt).
func randomKekID() string {
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic("secret: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
