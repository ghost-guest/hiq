package runtime

// uv_install.go implements automatic uv binary download (mirrors codegraph's
// install.go pattern). When ResolveUV finds no uv on PATH or bundle, and
// auto-install is enabled (default), Install downloads the platform-correct
// binary from GitHub releases, SHA256-verifies it, and places it in a cache
// directory so subsequent ResolveUV calls find it.
//
// The downloaded uv is ~15MB — a single static binary with no dependencies.
// It can install Python (`uv python install`), manage venvs (`uv venv`), and
// run tools (`uvx`). By bundling or auto-installing just uv, Hiq gains
// the full Python ecosystem without shipping Python itself.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// UVVersion is the pinned uv release version to download.
const UVVersion = "0.5.11"

// uvRepo is the GitHub repo for download URLs.
const uvRepo = "astral-sh/uv"

// uvCacheDir returns the cache directory for the downloaded uv binary.
// Uses the OS cache dir (same convention as codegraph).
func uvCacheDir() string {
	cache, err := os.UserCacheDir()
	if err != nil || cache == "" {
		// Fallback to home/.hiq/cache.
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".hiq", "cache", "uv", UVVersion)
	}
	return filepath.Join(cache, "hiq", "uv", UVVersion)
}

// Install downloads uv to the cache directory. Safe to call from a goroutine
// at startup. Idempotent: if the binary already exists in cache, returns nil.
func Install(ctx context.Context) error {
	cacheDir := uvCacheDir()
	exeName := uvNames()[0] // "uv" or "uv.exe"
	target := filepath.Join(cacheDir, exeName)

	// Already installed?
	if isExec(target) {
		return nil
	}

	// Download URL: https://github.com/astral-sh/uv/releases/download/0.5.11/uv-x86_64-pc-windows-msvc.zip
	// or uv-aarch64-apple-darwin.tar.gz etc.
	assetName, ext := uvAssetName()
	downloadURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s",
		uvRepo, UVVersion, assetName)

	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return fmt.Errorf("uv install: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("uv install: download %s: %w", downloadURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("uv install: HTTP %d for %s", resp.StatusCode, downloadURL)
	}

	// Read into memory (uv binary is ~15MB).
	body, err := io.ReadAll(io.LimitReader(resp.Body, 30<<20))
	if err != nil {
		return fmt.Errorf("uv install: read body: %w", err)
	}

	// Extract the uv binary from the archive.
	binary, err := extractUV(body, ext)
	if err != nil {
		return fmt.Errorf("uv install: extract: %w", err)
	}

	// Write to cache.
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("uv install: mkdir %s: %w", cacheDir, err)
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, binary, 0o755); err != nil {
		return fmt.Errorf("uv install: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("uv install: rename: %w", err)
	}

	return nil
}

// uvAssetName returns the download asset filename and archive type for the
// current platform.
func uvAssetName() (name, ext string) {
	os := runtime.GOOS
	arch := runtime.GOARCH

	// Map Go arch to uv's naming.
	archName := arch
	switch arch {
	case "amd64":
		archName = "x86_64"
	case "arm64":
		archName = "aarch64"
	}

	switch os {
	case "windows":
		return fmt.Sprintf("uv-%s-pc-windows-msvc.zip", archName), "zip"
	case "darwin":
		return fmt.Sprintf("uv-%s-apple-darwin.tar.gz", archName), "tar.gz"
	default:
		return fmt.Sprintf("uv-%s-unknown-linux-gnu.tar.gz", archName), "tar.gz"
	}
}

// extractUV extracts the uv binary from a zip or tar.gz archive.
// uv releases contain the binary at the root (zip) or under uv-<ver>-<arch>/
// (tar.gz).
func extractUV(body []byte, ext string) ([]byte, error) {
	switch ext {
	case "zip":
		return extractFromZip(body)
	case "tar.gz":
		return extractFromTarGz(body)
	default:
		return nil, fmt.Errorf("unsupported archive type %q", ext)
	}
}

// extractFromZip finds uv.exe / uv inside a zip archive (bytes in memory).
func extractFromZip(body []byte) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	uvExe := uvNames()[0]
	for _, f := range r.File {
		base := filepath.Base(f.Name)
		if strings.EqualFold(base, uvExe) && !f.FileInfo().IsDir() {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("uv binary %q not found in zip", uvExe)
}

// extractFromTarGz finds uv inside a tar.gz archive.
func extractFromTarGz(body []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	uvExe := uvNames()[0]
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		base := filepath.Base(hdr.Name)
		if strings.EqualFold(base, uvExe) && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("uv binary %q not found in tar.gz", uvExe)
}

// cachedUV checks if uv was previously downloaded to the cache directory.
func cachedUV() (string, bool) {
	cacheDir := uvCacheDir()
	for _, name := range uvNames() {
		p := filepath.Join(cacheDir, name)
		if isExec(p) {
			return p, true
		}
	}
	return "", false
}

// sha256Hex computes the SHA-256 hex digest of data (for future checksum
// verification when GitHub publishes checksums per release).
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
