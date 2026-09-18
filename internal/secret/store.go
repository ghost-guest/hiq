package secret

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store is an encrypted key→value secret store. Values are sealed with
// AES-256-GCM under a per-store KEK (key-encryption key) whose custody is
// platform-bound to the OS user — DPAPI on Windows (KEK wrapped inside the
// file), Keychain on macOS, Secret Service on Linux desktops, passphrase
// derivation on passphrase-configured headless machines, and a degraded
// machine-bound fallback as the last resort (surfaced via SecurityMode).
// Use Get/Set/Delete at the call site.
//
// The intended integration is: secrets live here encrypted at rest, and at
// startup LoadIntoEnv() decrypts them into os.Setenv so existing tools keep
// reading via os.Getenv(passwordEnv) unchanged. Env becomes an in-memory
// decrypted view; the file is the encrypted source of truth.
type Store struct {
	path string
	mu   sync.Mutex

	// injectedKeys records the env-var names LoadIntoEnv actually set (i.e. those
	// that were NOT already present as explicit user/system env). UnloadFromEnv
	// uses this to clear only what we injected, never clobbering a user's own
	// env. This bounds the secret's lifetime in the process env: a caller that
	// Unloads on teardown avoids leaving plaintext secrets in os.Environ() for
	// every later child process (bash/MCP/LSP) to inherit. See audit finding A9.
	// NOTE: a full per-run isolation (injecting secrets only into specific child
	// cmd.Env rather than the global process env) is a larger architecture change
	// tracked separately; this Unload capability is the first defensive step.
	injectedKeys []string

	// Resolved KEK state (guarded by mu). Cached per Store instance so repeated
	// operations on one store don't re-probe the keystore. Multiple Store
	// values over the same path (fresh instances are created deliberately at
	// several call sites) each resolve independently — keystore reads are cheap.
	forced   kekProvider // test hook: pin the backend, skip platform resolution
	prov     kekProvider
	kek      []byte
	kekId    string
	degraded bool
}

const userDirname = "hiq"

// storeVersion2 is the on-disk format with the KEK. Files without a version
// are v1 (per-entry Protect under the legacy platform scheme) and are
// transparently upgraded on the next write.
const storeVersion2 = 2

// errKEKUnavailable wraps every "existing v2 store cannot be decrypted" case:
// keystore locked/reset/absent, or a passphrase that no longer matches the one
// the store was created with. Reads degrade to "treated as unset"; writes fail
// rather than silently re-keying (which would strand the existing entries).
var errKEKUnavailable = errors.New("secret: KEK unavailable for existing store (keystore locked, reset, or wrong passphrase)")

// defaultStoreOnce ensures Default() returns a single shared Store instance so
// that LoadIntoEnv (which records injected keys) and UnloadFromEnv (which
// clears them) operate on the same injectedKeys slice across calls.
var (
	defaultStore     *Store
	defaultStoreOnce sync.Once
)

// New returns a Store backed by path. The parent dir is created lazily on first
// Set. The file format (v2) is {"version":2, "kekId":…, "kek":…, "secrets":
// {key: base64(nonce ‖ AES-GCM(KEK))}} — "kek" holds the DPAPI-wrapped KEK on
// Windows and is omitted elsewhere (the KEK lives in the OS keystore).
func New(path string) *Store { return &Store{path: path} }

// DefaultPath returns the canonical store location, beside config.toml and the
// credentials file: os.UserConfigDir()/hiq/secrets.enc.json. This matches
// config.userDir()/desktopConfigDir() so a single migration sweep finds the
// legacy cowork.env and credentials files.
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = home
	}
	return filepath.Join(dir, userDirname, "secrets.enc.json")
}

// Default returns the singleton Store at DefaultPath(). The singleton matters
// because LoadIntoEnv records which env vars it injected into injectedKeys, and
// UnloadFromEnv (which may run much later at teardown) must read the same slice
// — two separate New() instances would each have their own (empty) list.
func Default() *Store {
	defaultStoreOnce.Do(func() {
		defaultStore = New(DefaultPath())
	})
	return defaultStore
}

// newWithKekProvider is the deterministic test hook: a Store whose KEK backend
// is pinned, bypassing platform resolution and keystore probing entirely.
func newWithKekProvider(path string, p kekProvider) *Store {
	return &Store{path: path, forced: p}
}

type onDisk struct {
	Version int               `json:"version,omitempty"`
	KEKId   string            `json:"kekId,omitempty"` // store identity + derivation salt
	KEK     string            `json:"kek,omitempty"`   // base64 in-file KEK wrap (Windows DPAPI)
	Secrets map[string]string `json:"secrets"`         // key -> base64(nonce ‖ AES-GCM)
}

func (s *Store) load() (onDisk, error) {
	var doc onDisk
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return doc, nil // first run — no secrets yet
		}
		return doc, err
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return doc, fmt.Errorf("secret: parse %s: %w", s.path, err)
	}
	if doc.Secrets == nil {
		doc.Secrets = map[string]string{}
	}
	return doc, nil
}

// saveLocked writes doc atomically (sibling tmp + rename). Caller holds s.mu.
func (s *Store) saveLocked(doc onDisk) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// providers returns the KEK backend chain in priority order (test-forced
// backend, if set, replaces the chain).
func (s *Store) providers() []kekProvider {
	if s.forced != nil {
		return []kekProvider{s.forced}
	}
	return platformKekProviders()
}

// resolveKekLocked populates s.prov/s.kek for a v2 doc, trying each backend in
// priority order; a candidate KEK is only accepted when it actually decrypts a
// known entry (GCM authentication makes wrong keys fail), which disambiguates
// the deterministic passphrase/machine backends from the keystore ones. No-op
// for v1/empty docs. Returns errKEKUnavailable (wrapped) when nothing works.
func (s *Store) resolveKekLocked(doc onDisk) error {
	if doc.Version != storeVersion2 || doc.KEKId == "" {
		return nil
	}
	if s.kek != nil && s.kekId == doc.KEKId {
		return nil
	}
	var inFile []byte
	if doc.KEK != "" {
		inFile, _ = base64.StdEncoding.DecodeString(doc.KEK)
	}
	for _, p := range s.providers() {
		if !p.Available() {
			continue
		}
		kek, err := p.Fetch(doc.KEKId, inFile)
		if err != nil {
			continue // includes errNoKEK: this backend doesn't hold the key
		}
		if !kekDecryptsAnEntry(kek, doc) {
			continue // derived a different key than the store was created with
		}
		s.prov, s.kek, s.kekId = p, kek, doc.KEKId
		s.degraded = p.Name() == machineProviderName
		return nil
	}
	return fmt.Errorf("%w: %s", errKEKUnavailable, s.path)
}

// kekDecryptsAnEntry verifies a candidate KEK against the doc's entries; with
// no entries yet there is nothing to verify and any KEK is accepted.
func kekDecryptsAnEntry(kek []byte, doc onDisk) bool {
	for _, enc := range doc.Secrets {
		if _, err := openSecret(kek, enc); err == nil {
			return true
		}
	}
	return len(doc.Secrets) == 0
}

// createKekLocked provisions a brand-new KEK via the first backend that can
// actually store/derive one.
func (s *Store) createKekLocked() (prov kekProvider, kek []byte, id, inFileB64 string, err error) {
	for _, p := range s.providers() {
		if !p.Available() {
			continue
		}
		kekID := randomKekID()
		k, blob, cerr := p.Create(kekID)
		if cerr != nil {
			continue
		}
		return p, k, kekID, base64.StdEncoding.EncodeToString(blob), nil
	}
	return nil, nil, "", "", errors.New("secret: no usable KEK provider on this system")
}

// decryptLegacyEntry decrypts a v1 entry (per-entry Protect under the legacy
// platform scheme: DPAPI on Windows, machine key elsewhere).
func (s *Store) decryptLegacyEntry(enc string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, err
	}
	return Unprotect(raw)
}

// decryptEntryLocked decrypts one entry of doc under the appropriate scheme.
// Callers treat any error as "unset" — matching the long-standing behavior for
// undecryptable v1 blobs (e.g. a file copied from another Windows user).
func (s *Store) decryptEntryLocked(doc onDisk, enc string) ([]byte, error) {
	if doc.Version == storeVersion2 && doc.KEKId != "" {
		if err := s.resolveKekLocked(doc); err != nil {
			return nil, err
		}
		return openSecret(s.kek, enc)
	}
	return s.decryptLegacyEntry(enc)
}

// prepareWriteLocked is the gate for every write: it returns the KEK to seal
// with and leaves doc upgraded to v2. A v1 doc (or a fresh one) is migrated
// here — legacy entries are decrypted and re-sealed under the new KEK;
// entries that no longer decrypt (e.g. copied from another user) are dropped
// with a warning, since they were already unreadable dead weight. An existing
// v2 doc whose KEK is unavailable fails with errKEKUnavailable instead of
// silently re-keying and stranding the stored entries — EXCEPT when it holds
// no entries yet, where a fresh KEK is harmless.
func (s *Store) prepareWriteLocked(doc *onDisk) ([]byte, error) {
	if doc.Version == storeVersion2 && doc.KEKId != "" {
		if err := s.resolveKekLocked(*doc); err != nil {
			if len(doc.Secrets) == 0 {
				// Empty v2 store (all secrets deleted) with a lost KEK:
				// nothing to strand, provision a new one.
				return s.rekeyEmptyV2Locked(doc)
			}
			return nil, err
		}
		return s.kek, nil
	}

	plain := make(map[string]string, len(doc.Secrets))
	dropped := 0
	for k, enc := range doc.Secrets {
		b, err := s.decryptLegacyEntry(enc)
		if err != nil {
			dropped++
			continue
		}
		plain[k] = string(b)
	}
	if dropped > 0 {
		slog.Warn("secret: dropped undecryptable legacy entries during v1→v2 upgrade", "count", dropped, "path", s.path)
	}

	prov, kek, id, inFileB64, err := s.createKekLocked()
	if err != nil {
		return nil, err
	}
	secrets := make(map[string]string, len(plain))
	for k, v := range plain {
		sealed, err := sealSecret(kek, []byte(v))
		if err != nil {
			return nil, err
		}
		secrets[k] = sealed
	}
	doc.Version, doc.KEKId, doc.KEK, doc.Secrets = storeVersion2, id, inFileB64, secrets
	s.prov, s.kek, s.kekId = prov, kek, id
	s.degraded = prov.Name() == machineProviderName
	return kek, nil
}

// rekeyEmptyV2Locked provisions a new KEK for an entry-less v2 doc, keeping
// its kekId (the id is identity, not secrecy).
func (s *Store) rekeyEmptyV2Locked(doc *onDisk) ([]byte, error) {
	prov, kek, id, inFileB64, err := s.createKekLocked()
	if err != nil {
		return nil, err
	}
	doc.Version, doc.KEKId, doc.KEK = storeVersion2, id, inFileB64
	s.prov, s.kek, s.kekId = prov, kek, id
	s.degraded = prov.Name() == machineProviderName
	return kek, nil
}

// Set stores value under key, encrypting it at rest. An empty value is allowed
// (it encrypts to a non-empty blob, so presence is still detectable via Get).
func (s *Store) Set(key, value string) error {
	if key == "" {
		return errors.New("secret: empty key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return err
	}
	kek, err := s.prepareWriteLocked(&doc)
	if err != nil {
		return err
	}
	sealed, err := sealSecret(kek, []byte(value))
	if err != nil {
		return err
	}
	if doc.Secrets == nil {
		doc.Secrets = map[string]string{}
	}
	doc.Secrets[key] = sealed
	return s.saveLocked(doc)
}

// Get returns the decrypted value for key. ok is false when the key is absent
// (not an error). A decrypt failure (e.g. keystore locked/reset) returns
// ok=false and the error so the caller can prompt to re-enter the secret.
// HealthStats reports the store's key list and last-change time WITHOUT
// decrypting any value (works while the keystore is locked): the count is
// the raw secrets map's size, the age is the file's mtime — the store is a
// single file, so any Set/Delete rewrites it whole.
func (s *Store) HealthStats() (keys []string, lastChanged time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, time.Time{}, nil // empty store: zero keys, zero time
		}
		return nil, time.Time{}, err
	}
	var doc onDisk
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, time.Time{}, err
	}
	for k := range doc.Secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if fi, err := os.Stat(s.path); err == nil {
		lastChanged = fi.ModTime()
	}
	return keys, lastChanged, nil
}

func (s *Store) Get(key string) (value string, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return "", false, err
	}
	enc, exists := doc.Secrets[key]
	if !exists {
		return "", false, nil
	}
	plain, err := s.decryptEntryLocked(doc, enc)
	if err != nil {
		return "", false, err
	}
	return string(plain), true, nil
}

// Delete removes key. No-op when the key is absent.
func (s *Store) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := doc.Secrets[key]; !ok {
		return nil
	}
	kek, err := s.prepareWriteLocked(&doc)
	if err != nil {
		return err
	}
	_ = kek // entries were re-sealed inside prepareWriteLocked if upgrading
	delete(doc.Secrets, key)
	return s.saveLocked(doc)
}

// Keys returns the names of all stored secrets (order unspecified).
func (s *Store) Keys() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(doc.Secrets))
	for k := range doc.Secrets {
		keys = append(keys, k)
	}
	return keys, nil
}

// LoadIntoEnv decrypts every secret and exports it into the process environment
// via os.Setenv, skipping any env var that is already set (explicit user/system
// env always wins over the file). Returns the count of secrets loaded. Secrets
// that fail to decrypt — e.g. the keystore was reset — are skipped with a
// warning; the caller treats them as unset and the tool reports a config error,
// prompting re-entry.
//
// Loaded keys are recorded so UnloadFromEnv can clear them later (audit A9:
// bound the plaintext secret's lifetime in os.Environ()). Only call this for
// injection whose lifetime you intend to END with UnloadFromEnv — e.g. a
// controller-scoped session. For process-lifetime injection (the config layer,
// which feeds scheduler/RAG/settings/CLI one-shots that outlive any controller)
// use LoadIntoEnvUntracked, otherwise the first controller teardown would strip
// secrets every later consumer still needs.
func (s *Store) LoadIntoEnv() (int, error) {
	return s.loadIntoEnv(true)
}

// LoadIntoEnvUntracked is LoadIntoEnv without recording injectedKeys: the vars
// stay for the process lifetime and UnloadFromEnv never clears them. The
// intended use is the config boot layer, whose consumers (desktop scheduler,
// RAG extraction, settings key-set indicators, CLI one-shot commands) read
// os.Getenv outside any controller's lifetime.
func (s *Store) LoadIntoEnvUntracked() (int, error) {
	return s.loadIntoEnv(false)
}

func (s *Store) loadIntoEnv(tracked bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return 0, err
	}
	v2 := doc.Version == storeVersion2 && doc.KEKId != ""
	if v2 {
		if err := s.resolveKekLocked(doc); err != nil {
			return 0, err
		}
	}
	n, skipped := 0, 0
	for key, enc := range doc.Secrets {
		if os.Getenv(key) != "" {
			continue
		}
		var plain []byte
		var err error
		if v2 {
			plain, err = openSecret(s.kek, enc)
		} else {
			plain, err = s.decryptLegacyEntry(enc)
		}
		if err != nil {
			skipped++
			continue
		}
		os.Setenv(key, string(plain))
		if tracked {
			// Record that WE injected this key (it was empty before), so
			// UnloadFromEnv can later clear it without touching env vars the
			// user set themselves.
			s.injectedKeys = append(s.injectedKeys, key)
		}
		n++
	}
	if skipped > 0 {
		slog.Warn("secret: skipped undecryptable entries during env load (treated as unset; re-enter those secrets)", "count", skipped, "path", s.path)
	}
	return n, nil
}

// UnloadFromEnv clears the env vars that LoadIntoEnv injected, bounding the
// plaintext secret's lifetime in the process environment. Vars the user/system
// set explicitly are never touched (LoadIntoEnv skips them, so they're absent
// from injectedKeys). Safe to call multiple times; the recorded list is cleared
// after unloading. Returns the count of vars unset.
//
// Note: os.Unsetenv only affects the current process and children spawned
// afterwards — already-running child processes retain the env they inherited at
// spawn. For full isolation, call UnloadFromEnv before spawning untrusted
// children rather than after.
func (s *Store) UnloadFromEnv() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, key := range s.injectedKeys {
		os.Unsetenv(key)
		n++
	}
	s.injectedKeys = nil
	return n
}

// SecurityMode reports the at-rest encryption backend and whether it is
// degraded (machine-bound rather than OS-user-bound). "unavailable" means an
// existing v2 store's KEK cannot be reached (keystore locked/reset) — also a
// warnable state. Surfaced by boot warnings, doctor, and the settings badge.
func (s *Store) SecurityMode() (backend string, degraded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return "unknown", false
	}
	if doc.Version == storeVersion2 && doc.KEKId != "" {
		if err := s.resolveKekLocked(doc); err != nil {
			return "unavailable", true
		}
		return s.prov.Name(), s.degraded
	}
	// v1 or empty store: report the backend a next write would adopt.
	for _, p := range s.providers() {
		if p.Available() {
			return p.Name(), p.Name() == machineProviderName
		}
	}
	return "none", false
}

// MigrateEnvFile performs a one-time migration of a legacy plaintext KEY=value
// env file (the pre-encryption credentials file) into the encrypted store.
// Parsing mirrors config.loadDotEnvFile — lenient: blank lines and # comments
// skipped, optional `export ` prefix and surrounding quotes stripped. Keys
// already present in the store are left untouched (the user may have re-entered
// a newer value after upgrade); empty values are skipped, an empty secret is
// meaningless. When every non-empty entry is safely stored the source file is
// removed; on any error the file is kept so the plaintext fallback keeps
// working and the next run finishes the migration. Returns the number of
// secrets newly stored. A missing file is a no-op.
func (s *Store) MigrateEnvFile(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	type kv struct{ key, val string }
	var entries []kv
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if key == "" || val == "" {
			continue
		}
		entries = append(entries, kv{key, val})
	}
	scanErr := sc.Err()
	// Close before any store write: on Windows an open handle blocks the os.Remove
	// that finishes a successful migration.
	if cerr := f.Close(); cerr != nil && scanErr == nil {
		scanErr = cerr
	}
	if scanErr != nil {
		return 0, scanErr
	}
	if len(entries) == 0 {
		// Comments-only or empty file: nothing secret left to protect, and the
		// file has no reason to exist.
		return 0, os.Remove(path)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return 0, err
	}
	kek, err := s.prepareWriteLocked(&doc)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if _, exists := doc.Secrets[e.key]; exists {
			continue
		}
		sealed, err := sealSecret(kek, []byte(e.val))
		if err != nil {
			return 0, err
		}
		doc.Secrets[e.key] = sealed
		n++
	}
	if n > 0 {
		if err := s.saveLocked(doc); err != nil {
			return 0, err
		}
	}
	// Every entry is now accounted for (stored or already present), so the
	// plaintext file goes.
	return n, os.Remove(path)
}
