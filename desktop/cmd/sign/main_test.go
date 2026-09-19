package main

import (
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aead.dev/minisign"

	"github.com/zzycxz/hiq/desktop/internal/update"
)

// TestSignFiles signs a file with a throwaway key pair (injected via env, exactly
// as CI passes the real key) and verifies the produced .minisig validates under the
// matching public key.
func TestSignFiles(t *testing.T) {
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := minisign.EncryptKey("pw", priv)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINISIGN_PRIVATE_KEY", string(enc))
	t.Setenv("MINISIGN_PASSWORD", "pw")

	dir := t.TempDir()
	artifact := filepath.Join(dir, "hiq-linux-amd64.tar.gz")
	payload := []byte("pretend this is a release tarball")
	if err := os.WriteFile(artifact, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := signFiles([]string{artifact}); err != nil {
		t.Fatalf("signFiles: %v", err)
	}
	sig, err := os.ReadFile(artifact + ".minisig")
	if err != nil {
		t.Fatalf("read signature: %v", err)
	}
	if !minisign.Verify(pub, payload, sig) {
		t.Fatal("produced signature does not verify under the signing key")
	}
}

// TestGenManifest builds a manifest from a directory of fake artifacts and checks
// every platform is listed with a download URL, a parallel .minisig URL, and a
// non-empty digest. The .minisig and latest.json files must be ignored.
func TestGenManifest(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"hiq-darwin-arm64.zip",
		"hiq-darwin-amd64.zip",
		"hiq-windows-amd64.exe", // CI-produced binary (updater channel)
		"hiq-linux-amd64.tar.gz",
		"hiq-linux-amd64.deb",            // human download, not the updater channel
		"hiq-linux-amd64.tar.gz.minisig", // must be skipped
		"README.txt",                          // unmatched, must be skipped
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GITHUB_REPOSITORY", "ghost-guest/hiq")

	if err := genManifest(dir, "v1.2.0", "desktop-v1.2.0"); err != nil {
		t.Fatalf("genManifest: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m update.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("latest.json is not valid: %v", err)
	}
	if m.Version != "v1.2.0" {
		t.Fatalf("version = %q, want v1.2.0", m.Version)
	}
	if len(m.Platforms) != 4 {
		t.Fatalf("want 4 platforms, got %d: %v", len(m.Platforms), m.Platforms)
	}
	win, ok := m.Platforms["windows-amd64"]
	if !ok {
		t.Fatal("windows-amd64 missing")
	}
	wantURL := "https://github.com/ghost-guest/hiq/releases/download/desktop-v1.2.0/hiq-windows-amd64.exe"
	if win.URL != wantURL {
		t.Fatalf("windows url = %q, want %q", win.URL, wantURL)
	}
	wantSig := "https://github.com/ghost-guest/hiq/releases/download/desktop-v1.2.0-sigs/hiq-windows-amd64.exe.minisig"
	if win.Sig != wantSig {
		t.Fatalf("windows sig = %q, want %q", win.Sig, wantSig)
	}
	if win.SHA256 == "" || win.Size == 0 {
		t.Fatalf("windows asset missing digest/size: %+v", win)
	}
	// The Linux updater channel must stay the .tar.gz; the co-located .deb is a
	// human download and must not shadow the linux-amd64 key.
	lin, ok := m.Platforms["linux-amd64"]
	if !ok {
		t.Fatal("linux-amd64 missing")
	}
	if !strings.HasSuffix(lin.URL, "/hiq-linux-amd64.tar.gz") {
		t.Fatalf("linux-amd64 url = %q, want the .tar.gz, not the .deb", lin.URL)
	}
}
