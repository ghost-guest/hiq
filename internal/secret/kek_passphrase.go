package secret

import (
	"os"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Passphrase-derived KEK: opt-in strong mode for machines without an OS
// keystore (typically headless Linux running serve/acp/bot). Configure via
//   HIQ_SECRET_PASSPHRASE=some-long-secret        (direct)
//   HIQ_SECRET_PASSPHRASE_FILE=/path/to/secret    (file, e.g. a docker
//                                                      secret or 0600 file)
// The KEK is argon2id(passphrase, salt=kekSalt+kekId) — nothing is
// stored, derivation is deterministic, and the per-store kekId salt means two
// stores on one machine never share a key. Reaching the same strength as
// DPAPI/Keychain requires the passphrase to stay out of the encrypted file's
// reach, hence the env/file indirection.

const (
	envSecretPassphrase     = "HIQ_SECRET_PASSPHRASE"
	envSecretPassphraseFile = "HIQ_SECRET_PASSPHRASE_FILE"
)

type passphraseKekProvider struct{}

func (passphraseKekProvider) Name() string { return "passphrase" }

func (passphraseKekProvider) Available() bool {
	_, ok := readSecretPassphrase()
	return ok
}

func (passphraseKekProvider) Create(id string) ([]byte, []byte, error) {
	pw, ok := readSecretPassphrase()
	if !ok {
		return nil, nil, errNoKEK
	}
	return derivePassphraseKek(pw, id), nil, nil
}

func (passphraseKekProvider) Fetch(id string, inFile []byte) ([]byte, error) {
	pw, ok := readSecretPassphrase()
	if !ok {
		return nil, errNoKEK
	}
	return derivePassphraseKek(pw, id), nil
}

func readSecretPassphrase() (string, bool) {
	if v := strings.TrimSpace(os.Getenv(envSecretPassphrase)); v != "" {
		return v, true
	}
	if p := strings.TrimSpace(os.Getenv(envSecretPassphraseFile)); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				return v, true
			}
		}
	}
	return "", false
}

// argon2id: 64 MiB, t=1, p=4 — ~50-100ms on a desktop CPU, comfortably above
// the OWASP minimum configuration line for interactive-adjacent derivations.
func derivePassphraseKek(passphrase, kekID string) []byte {
	return argon2.IDKey([]byte(passphrase), []byte(kekSalt+kekID), 1, 64*1024, 4, kekSize)
}
