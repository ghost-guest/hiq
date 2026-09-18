//go:build windows

package secret

import (
	"crypto/rand"
	"io"
)

// Windows KEK backend: a random KEK DPAPI-wrapped and stored inside the
// secrets file itself. The file stays self-contained (no Credential Manager
// entry needed) and the ciphertext remains bound to the current Windows user
// exactly like the pre-v2 per-entry format — only the structure changed.

type dpapiKekProvider struct{}

func platformKekProviders() []kekProvider {
	// No fallback chain on Windows: DPAPI is always present, and silently
	// downgrading a broken DPAPI to weak encryption would hide the problem.
	return []kekProvider{dpapiKekProvider{}}
}

func (dpapiKekProvider) Name() string    { return "dpapi" }
func (dpapiKekProvider) Available() bool { return true }

func (dpapiKekProvider) Create(id string) ([]byte, []byte, error) {
	kek := make([]byte, kekSize)
	if _, err := io.ReadFull(rand.Reader, kek); err != nil {
		return nil, nil, err
	}
	blob, err := Protect(kek)
	if err != nil {
		return nil, nil, err
	}
	return kek, blob, nil
}

func (dpapiKekProvider) Fetch(id string, inFile []byte) ([]byte, error) {
	if len(inFile) == 0 {
		return nil, errNoKEK
	}
	return Unprotect(inFile)
}
