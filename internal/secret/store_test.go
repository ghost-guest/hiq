package secret

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "secrets.enc.json"))
	if err := s.Set("MAIL_PASSWORD", "abc123授权码"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := s.Get("MAIL_PASSWORD")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected secret present")
	}
	if got != "abc123授权码" {
		t.Fatalf("got %q, want %q", got, "abc123授权码")
	}
}

func TestStoreMissing(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "secrets.enc.json"))
	_, ok, err := s.Get("nope")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("expected missing")
	}
}

func TestStoreDelete(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "secrets.enc.json"))
	if err := s.Set("k", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Delete("k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := s.Get("k"); ok {
		t.Fatal("expected deleted")
	}
	// Deleting a missing key is a no-op.
	if err := s.Delete("absent"); err != nil {
		t.Fatalf("Delete absent: %v", err)
	}
}

func TestStoreCiphertextOnDisk(t *testing.T) {
	// The whole point: the plaintext must never appear in the file.
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc.json")
	s := New(path)
	if err := s.Set("MAIL_PASSWORD", "supersecret-value-123"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(b), "supersecret-value-123") {
		t.Fatal("plaintext leaked to disk")
	}
	// v2 markers must be present so the format is recognizable on disk.
	var doc onDisk
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse store file: %v", err)
	}
	if doc.Version != storeVersion2 || doc.KEKId == "" {
		t.Fatalf("expected v2 store with kekId, got version=%d kekId=%q", doc.Version, doc.KEKId)
	}
}

func TestStoreLoadIntoEnv(t *testing.T) {
	const key = "HIQ_TEST_SECRET_XYZ_001"
	os.Unsetenv(key)
	s := New(filepath.Join(t.TempDir(), "secrets.enc.json"))
	if err := s.Set(key, "hello-env"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	n, err := s.LoadIntoEnv()
	if err != nil {
		t.Fatalf("LoadIntoEnv: %v", err)
	}
	if n < 1 {
		t.Fatal("expected at least 1 secret loaded")
	}
	if os.Getenv(key) != "hello-env" {
		t.Fatalf("env not set, got %q", os.Getenv(key))
	}
}

// TestLoadIntoEnvUntrackedSurvivesUnload pins the config-layer lifetime
// contract: an untracked injection must NOT be cleared by UnloadFromEnv, while
// a tracked one must. The desktop relies on this — rebuild() closes the old
// controller after the new one is running, so a controller teardown must never
// strip process-lifetime secrets (provider keys) from the env.
func TestLoadIntoEnvUntrackedSurvivesUnload(t *testing.T) {
	const trackedKey = "HIQ_TEST_SECRET_TRACKED_1"
	const untrackedKey = "HIQ_TEST_SECRET_UNTRACKED_1"
	os.Unsetenv(trackedKey)
	os.Unsetenv(untrackedKey)
	t.Cleanup(func() {
		os.Unsetenv(trackedKey)
		os.Unsetenv(untrackedKey)
	})

	s := New(filepath.Join(t.TempDir(), "secrets.enc.json"))
	if err := s.Set(trackedKey, "v1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set(untrackedKey, "v2"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := s.LoadIntoEnvUntracked(); err != nil {
		t.Fatalf("LoadIntoEnvUntracked: %v", err)
	}
	if os.Getenv(untrackedKey) != "v2" {
		t.Fatalf("untracked env not set, got %q", os.Getenv(untrackedKey))
	}

	// The same store still tracks vars it injected itself (simulate the
	// controller-scoped path by unsetting first).
	os.Unsetenv(trackedKey)
	if _, err := s.LoadIntoEnv(); err != nil {
		t.Fatalf("LoadIntoEnv: %v", err)
	}

	if n := s.UnloadFromEnv(); n != 1 {
		t.Fatalf("UnloadFromEnv cleared %d vars, want 1 (only the tracked one)", n)
	}
	if _, ok := os.LookupEnv(trackedKey); ok {
		t.Fatal("tracked key should be unloaded")
	}
	if os.Getenv(untrackedKey) != "v2" {
		t.Fatal("untracked key must survive UnloadFromEnv")
	}
}

func TestStoreEmptyValue(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "secrets.enc.json"))
	if err := s.Set("k", ""); err != nil {
		t.Fatalf("Set empty: %v", err)
	}
	got, ok, err := s.Get("k")
	if err != nil || !ok {
		t.Fatalf("Get empty: ok=%v err=%v", ok, err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestMigrateEnvFile(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "credentials")
	// export prefix, quotes, comments, blanks, and an empty value — the same
	// lenient surface config.loadDotEnvFile parses.
	content := "# managed by hiq\n" +
		"OPENAI_API_KEY=sk-plain-1\n" +
		"\n" +
		"export DEEPSEEK_API_KEY=\"sk-quoted-2\"\n" +
		"EMPTY_SECRET=\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := New(filepath.Join(dir, "secrets.enc.json"))
	n, err := s.MigrateEnvFile(envPath)
	if err != nil {
		t.Fatalf("MigrateEnvFile: %v", err)
	}
	if n != 2 {
		t.Fatalf("migrated %d, want 2 (empty value skipped)", n)
	}
	for key, want := range map[string]string{
		"OPENAI_API_KEY":   "sk-plain-1",
		"DEEPSEEK_API_KEY": "sk-quoted-2",
	} {
		got, ok, err := s.Get(key)
		if err != nil || !ok {
			t.Fatalf("Get %s: ok=%v err=%v", key, ok, err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	// The plaintext file must be gone, and its values must never have landed
	// on disk in the store file.
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("plaintext file still exists after successful migration (stat err=%v)", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "secrets.enc.json"))
	if err != nil {
		t.Fatalf("ReadFile store: %v", err)
	}
	if strings.Contains(string(b), "sk-plain-1") || strings.Contains(string(b), "sk-quoted-2") {
		t.Fatal("plaintext leaked into the store file")
	}
}

func TestMigrateEnvFileMissingIsNoop(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "secrets.enc.json"))
	n, err := s.MigrateEnvFile(filepath.Join(t.TempDir(), "absent.env"))
	if err != nil || n != 0 {
		t.Fatalf("MigrateEnvFile(missing) = (%d, %v), want (0, nil)", n, err)
	}
}

func TestMigrateEnvFileKeepsExistingStoreValues(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "credentials")
	os.WriteFile(envPath, []byte("OPENAI_API_KEY=from-file\n"), 0o600)

	s := New(filepath.Join(dir, "secrets.enc.json"))
	// The user re-entered a newer key after upgrade: migration must not clobber it.
	if err := s.Set("OPENAI_API_KEY", "from-store"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if n, err := s.MigrateEnvFile(envPath); err != nil || n != 0 {
		t.Fatalf("MigrateEnvFile = (%d, %v), want (0, nil)", n, err)
	}
	got, _, _ := s.Get("OPENAI_API_KEY")
	if got != "from-store" {
		t.Fatalf("store value clobbered: %q", got)
	}
	// All entries were accounted for, so the file is still removed.
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatal("plaintext file kept despite a fully-covered migration")
	}
}

// --- v2 / KEK behavior ---

// fakeKekProvider is an in-memory keystore backend for deterministic tests:
// it holds random KEKs per id like a real keystore and can be "reset" to
// simulate keystore loss. name is configurable to exercise SecurityMode.
type fakeKekProvider struct {
	name  string
	keks  map[string][]byte
	fetch func(id string, inFile []byte) ([]byte, error) // optional override
}

func (f *fakeKekProvider) Name() string    { return f.name }
func (f *fakeKekProvider) Available() bool { return true }
func (f *fakeKekProvider) Create(id string) ([]byte, []byte, error) {
	if f.keks == nil {
		f.keks = map[string][]byte{}
	}
	kek := make([]byte, kekSize)
	for i := range kek {
		kek[i] = byte(i + 1)
	}
	kek[0] = byte(len(f.keks) + 1) // distinct per store
	f.keks[id] = kek
	return kek, nil, nil
}
func (f *fakeKekProvider) Fetch(id string, inFile []byte) ([]byte, error) {
	if f.fetch != nil {
		return f.fetch(id, inFile)
	}
	kek, ok := f.keks[id]
	if !ok {
		return nil, errNoKEK
	}
	return kek, nil
}

// writeV1Store fabricates a pre-v2 store file: per-entry Protect under the
// legacy platform scheme.
func writeV1Store(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	doc := onDisk{Secrets: map[string]string{}}
	for k, v := range entries {
		protected, err := Protect([]byte(v))
		if err != nil {
			t.Fatalf("legacy Protect(%s): %v", k, err)
		}
		doc.Secrets[k] = base64.StdEncoding.EncodeToString(protected)
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestV1MigrationUpgradesToV2 pins the transparent upgrade: a legacy store
// stays readable and any write re-seals every entry under a KEK.
func TestV1MigrationUpgradesToV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc.json")
	writeV1Store(t, path, map[string]string{"OLD_KEY": "old-value", "MAIL": "legacy-pw"})

	// Reads on the v1 doc work through the legacy path.
	s := New(path)
	if got, ok, err := s.Get("OLD_KEY"); err != nil || !ok || got != "old-value" {
		t.Fatalf("v1 Get = (%q, %v, %v), want (old-value, true, nil)", got, ok, err)
	}

	// Any write upgrades the whole file.
	if err := s.Set("NEW_KEY", "new-value"); err != nil {
		t.Fatalf("Set after v1: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc onDisk
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != storeVersion2 || doc.KEKId == "" {
		t.Fatalf("store not upgraded: version=%d kekId=%q", doc.Version, doc.KEKId)
	}
	// Every entry, old and new, is readable from a fresh Store instance.
	s2 := New(path)
	for k, want := range map[string]string{"OLD_KEY": "old-value", "MAIL": "legacy-pw", "NEW_KEY": "new-value"} {
		if got, ok, err := s2.Get(k); err != nil || !ok || got != want {
			t.Fatalf("post-upgrade Get(%s) = (%q, %v, %v), want (%q, true, nil)", k, got, ok, err, want)
		}
	}
	if strings.Contains(string(b), "old-value") || strings.Contains(string(b), "legacy-pw") {
		t.Fatal("plaintext leaked during upgrade")
	}
}

// TestKekLostTreatedAsUnset pins the keystore-loss story: reads degrade to
// "unset" (the long-standing v1 semantic for undecryptable blobs) and writes
// fail instead of silently re-keying and stranding the stored entries.
func TestKekLostTreatedAsUnset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc.json")
	prov := &fakeKekProvider{name: "fake-keychain"}
	if err := newWithKekProvider(path, prov).Set("API_KEY", "sk-lost"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	lost := &fakeKekProvider{name: "fake-keychain"} // holds nothing: keystore was reset
	s := newWithKekProvider(path, lost)
	_, ok, err := s.Get("API_KEY")
	if ok || err == nil {
		t.Fatalf("Get with lost KEK = ok=%v err=%v, want (false, err)", ok, err)
	}
	if !errors.Is(err, errKEKUnavailable) {
		t.Fatalf("Get error should wrap errKEKUnavailable, got %v", err)
	}
	if err := s.Set("API_KEY", "sk-new"); err == nil {
		t.Fatal("Set with lost KEK must fail rather than strand the existing entry")
	}
	if _, err := s.LoadIntoEnv(); err == nil {
		t.Fatal("LoadIntoEnv with lost KEK should surface the error")
	}
	backend, degraded := s.SecurityMode()
	if backend != "unavailable" || !degraded {
		t.Fatalf("SecurityMode = (%q, %v), want (unavailable, true)", backend, degraded)
	}
}

// TestEmptyV2StoreRegeneratesKek: an entry-less v2 store with a lost KEK has
// nothing to strand, so writes may provision a fresh KEK.
func TestEmptyV2StoreRegeneratesKek(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc.json")
	prov := &fakeKekProvider{name: "fake-keychain"}
	s1 := newWithKekProvider(path, prov)
	if err := s1.Set("K", "v"); err != nil {
		t.Fatal(err)
	}
	if err := s1.Delete("K"); err != nil {
		t.Fatal(err)
	}
	// Store now exists as v2 with zero entries; simulate keystore loss.
	lost := &fakeKekProvider{name: "fake-keychain"}
	s2 := newWithKekProvider(path, lost)
	if err := s2.Set("K2", "v2"); err != nil {
		t.Fatalf("Set on empty v2 store with lost KEK: %v", err)
	}
	if got, ok, err := s2.Get("K2"); err != nil || !ok || got != "v2" {
		t.Fatalf("Get after re-key = (%q, %v, %v)", got, ok, err)
	}
}

// TestWrongDeterministicKeyRejected: a deterministic provider (passphrase/
// machine style) deriving a different key than the store was created with must
// be rejected by entry verification, not silently corrupt reads.
func TestWrongDeterministicKeyRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc.json")
	prov := &fakeKekProvider{name: "fake-keychain"}
	if err := newWithKekProvider(path, prov).Set("API_KEY", "sk-real"); err != nil {
		t.Fatal(err)
	}

	wrong := &fakeKekProvider{
		name: "passphrase",
		fetch: func(id string, inFile []byte) ([]byte, error) {
			kek := make([]byte, kekSize) // deterministically wrong key
			return kek, nil
		},
	}
	s := newWithKekProvider(path, wrong)
	if got, ok, err := s.Get("API_KEY"); ok || err == nil {
		t.Fatalf("Get under wrong derived key = (%q, %v, %v), want (false, err)", got, ok, err)
	}
}

// TestPassphraseKekDerivation pins the deterministic derivation contract:
// same passphrase+id → same KEK, different id → different KEK.
func TestPassphraseKekDerivation(t *testing.T) {
	t.Setenv(envSecretPassphrase, "correct horse battery staple")
	a1 := derivePassphraseKek("correct horse battery staple", "id-a")
	a2 := derivePassphraseKek("correct horse battery staple", "id-a")
	b := derivePassphraseKek("correct horse battery staple", "id-b")
	if string(a1) != string(a2) {
		t.Fatal("same passphrase+id must derive the same KEK")
	}
	if string(a1) == string(b) {
		t.Fatal("different kekId must derive a different KEK")
	}
	if len(a1) != kekSize {
		t.Fatalf("derived %d bytes, want %d", len(a1), kekSize)
	}

	p := passphraseKekProvider{}
	if !p.Available() {
		t.Fatal("passphrase provider must be Available when env is set")
	}
	kek, err := p.Fetch("id-a", nil)
	if err != nil || len(kek) != kekSize {
		t.Fatalf("Fetch = (%d bytes, %v)", len(kek), err)
	}
}

// TestSecurityModeReportsDegradedMachine: the machine-bound fallback must be
// flagged degraded so boot/doctor/UI can warn.
func TestSecurityModeReportsDegradedMachine(t *testing.T) {
	s := newWithKekProvider(filepath.Join(t.TempDir(), "secrets.enc.json"), &fakeKekProvider{name: machineProviderName})
	if err := s.Set("K", "v"); err != nil {
		t.Fatal(err)
	}
	if backend, degraded := s.SecurityMode(); backend != machineProviderName || !degraded {
		t.Fatalf("SecurityMode = (%q, %v), want (%q, true)", backend, degraded, machineProviderName)
	}

	s2 := newWithKekProvider(filepath.Join(t.TempDir(), "secrets.enc.json"), &fakeKekProvider{name: "keychain"})
	if err := s2.Set("K", "v"); err != nil {
		t.Fatal(err)
	}
	if backend, degraded := s2.SecurityMode(); backend != "keychain" || degraded {
		t.Fatalf("SecurityMode = (%q, %v), want (keychain, false)", backend, degraded)
	}
}

// TestKekIdSeparatesStores: the main store and mobilebridge.enc.json each get
// their own KEK (distinct ids), so one never decrypts the other.
func TestKekIdSeparatesStores(t *testing.T) {
	dir := t.TempDir()
	prov := &fakeKekProvider{name: "fake-keychain"}
	if err := newWithKekProvider(filepath.Join(dir, "secrets.enc.json"), prov).Set("K", "main"); err != nil {
		t.Fatal(err)
	}
	if err := newWithKekProvider(filepath.Join(dir, "mobilebridge.enc.json"), prov).Set("K", "bridge"); err != nil {
		t.Fatal(err)
	}
	if len(prov.keks) != 2 {
		t.Fatalf("expected 2 distinct KEKs for 2 stores, got %d", len(prov.keks))
	}
	var mainDoc, bridgeDoc onDisk
	mb, _ := os.ReadFile(filepath.Join(dir, "secrets.enc.json"))
	bb, _ := os.ReadFile(filepath.Join(dir, "mobilebridge.enc.json"))
	json.Unmarshal(mb, &mainDoc)
	json.Unmarshal(bb, &bridgeDoc)
	if mainDoc.KEKId == bridgeDoc.KEKId {
		t.Fatal("two stores must not share a kekId")
	}
}

// HealthStats must count keys + report mtime WITHOUT decrypting (works on a
// locked store); an empty store reads zero/zero without error.
func TestHealthStats(t *testing.T) {
	dir := t.TempDir()
	st := New(filepath.Join(dir, "secrets.enc.json"))
	keys, at, err := st.HealthStats()
	if err != nil || len(keys) != 0 || !at.IsZero() {
		t.Fatalf("empty store = %v %v %v", keys, at, err)
	}
	if err := st.Set("netdev/password/P1", "x"); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("mail/token/T1", "y"); err != nil {
		t.Fatal(err)
	}
	keys, at, err = st.HealthStats()
	if err != nil || len(keys) != 2 || at.IsZero() {
		t.Fatalf("stats = %v %v %v", keys, at, err)
	}
	if keys[0] != "mail/token/T1" || keys[1] != "netdev/password/P1" {
		t.Errorf("keys not sorted: %v", keys)
	}
	if time.Since(at) > 5*time.Second {
		t.Errorf("mtime too old: %v", at)
	}
}
