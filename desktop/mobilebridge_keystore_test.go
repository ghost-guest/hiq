package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zzycxz/hiq/internal/mobilebridge"
	"github.com/zzycxz/hiq/internal/secret"
)

// The secretKeyStore must satisfy the same contract the old plaintext
// fileKeyStore did: transparent byte round-trip and ErrNotFound for misses —
// the bridge code is unaware of the swap.

func TestSecretKeyStoreRoundTrip(t *testing.T) {
	ks := &secretKeyStore{store: secret.New(filepath.Join(t.TempDir(), "mobilebridge.enc.json"))}

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := ks.Set("mobilebridge.device.priv", priv); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := ks.Get("mobilebridge.device.priv")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(priv) {
		t.Fatal("round-trip mismatch")
	}
	if err := ks.Delete("mobilebridge.device.priv"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := ks.Get("mobilebridge.device.priv"); err != mobilebridge.ErrNotFound {
		t.Fatalf("Get after Delete err = %v, want ErrNotFound", err)
	}
}

func TestSecretKeyStoreMissingIsErrNotFound(t *testing.T) {
	ks := &secretKeyStore{store: secret.New(filepath.Join(t.TempDir(), "mobilebridge.enc.json"))}
	if _, err := ks.Get("absent"); err != mobilebridge.ErrNotFound {
		t.Fatalf("Get(absent) err = %v, want ErrNotFound", err)
	}
}

// The on-disk file must never contain the raw key material — the whole point of
// replacing the M1 plaintext fileKeyStore.
func TestSecretKeyStoreNoPlaintextOnDisk(t *testing.T) {
	dir := t.TempDir()
	ks := &secretKeyStore{store: secret.New(filepath.Join(dir, "mobilebridge.enc.json"))}
	key := []byte("raw-key-material-0123456789")
	if err := ks.Set("mobilebridge.device.priv", key); err != nil {
		t.Fatalf("Set: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		fb, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if bytes.Contains(fb, key) {
			t.Errorf("raw key material leaked into %s", e.Name())
		}
		if bytes.Contains(fb, []byte(base64.StdEncoding.EncodeToString(key))) {
			t.Errorf("base64 key material leaked into %s (old fileKeyStore format)", e.Name())
		}
	}
}

// migrateLegacyKeyFileAt must lift the M1 plaintext keystore into the encrypted
// store and remove the file only after the entry verifies.
func TestMigrateLegacyKeyFile(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "mobilebridge_keys.json")

	priv := []byte("legacy-device-private-key-bytes")
	entry := map[string]string{
		"mobilebridge.device.priv": base64.StdEncoding.EncodeToString(priv),
	}
	b, _ := json.Marshal(entry)
	if err := os.WriteFile(legacyPath, b, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ks := &secretKeyStore{store: secret.New(filepath.Join(dir, "mobilebridge.enc.json"))}
	ks.migrateLegacyKeyFileAt(legacyPath)

	got, err := ks.Get("mobilebridge.device.priv")
	if err != nil {
		t.Fatalf("Get after migration: %v", err)
	}
	if string(got) != string(priv) {
		t.Fatalf("migrated key = %q, want %q", got, priv)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatal("legacy plaintext keystore kept after verified migration")
	}
}
