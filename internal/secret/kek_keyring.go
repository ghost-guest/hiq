//go:build darwin || linux

package secret

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"runtime"
	"sync"

	"github.com/zalando/go-keyring"
)

// macOS/Linux KEK backend: a random KEK stored in the OS keystore — Keychain
// on macOS (via /usr/bin/security, zero CGO) or the Secret Service on Linux
// (gnome-keyring/KWallet via D-Bus). This is what lifts at-rest protection to
// the same OS-user-bound level as Windows DPAPI.
//
// Accepted trade-off (macOS): go-keyring passes the value through the
// `security` CLI's argv at write time, briefly visible to same-user `ps`. It
// happens exactly once per store (KEK creation), the material is a random
// machine-local key rather than a user password, and a same-user attacker has
// far cheaper paths — so the portability win of zero CGO wins here.

// keyringService is the Keychain / Secret Service entry name. It is a
// wire-format constant, not branding: renaming it would orphan the KEK of every
// existing macOS/Linux install, so it keeps its pre-rebrand spelling. Windows
// (DPAPI) never touches it.
const (
	keyringService  = "fairpeer.secret"
	keyringProbeAcc = "availability-probe"
)

func keyringAccount(id string) string { return "store:" + id }

type keyringKekProvider struct{}

// keyringAvailable probes the keystore once per process: a clean "not found"
// proves the backend answers; anything else (no D-Bus session, locked login
// keyring, missing `security`) means unavailable and the chain falls through.
var keyringAvailable = sync.OnceValue(func() bool {
	_, err := keyring.Get(keyringService, keyringProbeAcc)
	return err == nil || errors.Is(err, keyring.ErrNotFound)
})

func platformKekProviders() []kekProvider {
	return []kekProvider{keyringKekProvider{}, passphraseKekProvider{}, machineKekProvider{}}
}

func (keyringKekProvider) Name() string {
	if runtime.GOOS == "darwin" {
		return "keychain"
	}
	return "secret-service"
}

func (keyringKekProvider) Available() bool { return keyringAvailable() }

func (keyringKekProvider) Create(id string) ([]byte, []byte, error) {
	kek := make([]byte, kekSize)
	if _, err := io.ReadFull(rand.Reader, kek); err != nil {
		return nil, nil, err
	}
	// hex keeps the value ASCII-safe through every keystore backend
	// (`security` CLI quoting, Secret Service) — no binary/quoting hazards.
	if err := keyring.Set(keyringService, keyringAccount(id), hex.EncodeToString(kek)); err != nil {
		return nil, nil, err
	}
	return kek, nil, nil
}

func (keyringKekProvider) Fetch(id string, inFile []byte) ([]byte, error) {
	v, err := keyring.Get(keyringService, keyringAccount(id))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, errNoKEK
		}
		return nil, err
	}
	kek, err := hex.DecodeString(v)
	if err != nil || len(kek) != kekSize {
		return nil, errNoKEK
	}
	return kek, nil
}
