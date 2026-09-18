//go:build !windows

package secret

import (
	"crypto/hmac"
	"crypto/sha256"
)

// Machine-bound KEK fallback (non-Windows only). Deterministically derived
// from the machine identity — the same key material the pre-v2 format used,
// now demoted from "the encryption" to "last-resort KEK, flagged degraded".
// Any local process can recompute it, so SecurityMode reports degraded=true
// and boot/UI surface a warning when this backend is active. It exists so a
// headless Linux box (serve/acp/bot with no Secret Service and no configured
// passphrase) keeps working instead of losing secret storage outright.

type machineKekProvider struct{}

func (machineKekProvider) Name() string    { return machineProviderName }
func (machineKekProvider) Available() bool { return true }
func (machineKekProvider) Create(id string) ([]byte, []byte, error) {
	return machineDerivedKek(id), nil, nil
}
func (machineKekProvider) Fetch(id string, inFile []byte) ([]byte, error) {
	return machineDerivedKek(id), nil
}

func machineDerivedKek(id string) []byte {
	mac := hmac.New(sha256.New, machineKey())
	mac.Write([]byte(kekSalt + id))
	return mac.Sum(nil)
}
