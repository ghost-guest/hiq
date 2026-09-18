package mobilebridge

import (
	"errors"
	"sync"
)

// ErrNotFound is returned by KeyStore.Get for a missing key.
var ErrNotFound = errors.New("key not found")

// KeyStore persists long-term secrets: this device's Ed25519 private key and
// each paired peer's public key. Production wires this to hiq's
// secret.Store (DPAPI-encrypted at rest, HIQ_SPEC §6); tests use
// MemoryKeyStore. The abstraction keeps mobilebridge testable without hiq internals.
type KeyStore interface {
	Get(key string) ([]byte, error) // ErrNotFound if missing
	Set(key string, val []byte) error
	Delete(key string) error
}

// MemoryKeyStore is an in-process KeyStore for tests and ephemeral runs.
type MemoryKeyStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewMemoryKeyStore() *MemoryKeyStore { return &MemoryKeyStore{m: map[string][]byte{}} }

func (s *MemoryKeyStore) Get(key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (s *MemoryKeyStore) Set(key string, val []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, len(val))
	copy(out, val)
	s.m[key] = out
	return nil
}

func (s *MemoryKeyStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}
