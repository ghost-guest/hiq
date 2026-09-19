package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewLocalFileServesRegisteredFile(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "page.html")
	if err := os.WriteFile(page, []byte("<html><body>hiq preview</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	url, err := app.PreviewLocalFile(page)
	if err != nil {
		t.Fatalf("PreviewLocalFile: %v", err)
	}
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("url = %q, want loopback http URL", url)
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "hiq preview") {
		t.Fatalf("status=%d body=%q", resp.StatusCode, string(body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
}

func TestPreviewLocalFileServesSiblingWithinDirOnly(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "index.html")
	if err := os.WriteFile(page, []byte("<link rel=stylesheet href=style.css>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "style.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "outside.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	base, err := app.PreviewLocalFile(page)
	if err != nil {
		t.Fatalf("PreviewLocalFile: %v", err)
	}
	resp, err := http.Get(base + "style.css")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("sibling status = %d, want 200", resp.StatusCode)
	}
	// Traversal out of the file's directory must 404.
	for _, bad := range []string{"../outside.txt", "..%5coutside.txt", "a/../../outside.txt"} {
		resp, err := http.Get(base + bad)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("traversal %q status = %d, want 404", bad, resp.StatusCode)
		}
	}
}

func TestPreviewLocalFileRejectsBadInput(t *testing.T) {
	app := &App{}
	if _, err := app.PreviewLocalFile(""); err == nil {
		t.Fatal("empty path should error")
	}
	if _, err := app.PreviewLocalFile(filepath.Join(t.TempDir(), "missing.html")); err == nil {
		t.Fatal("missing file should error")
	}
	exe := filepath.Join(t.TempDir(), "evil.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := app.PreviewLocalFile(exe); err == nil {
		t.Fatal("non-previewable extension should error")
	}
	dir := t.TempDir()
	if _, err := app.PreviewLocalFile(dir); err == nil {
		t.Fatal("directory should error")
	}
}

func TestPreviewServerStablePortAcrossRegistrations(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.html")
	b := filepath.Join(dir, "b.html")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{}
	u1, err := app.PreviewLocalFile(a)
	if err != nil {
		t.Fatal(err)
	}
	u2, err := app.PreviewLocalFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if portOf(u1) != portOf(u2) {
		t.Fatalf("ports differ: %q vs %q", u1, u2)
	}
}

func portOf(url string) string {
	return strings.Split(strings.TrimPrefix(url, "http://127.0.0.1:"), "/")[0]
}

// TestPreviewInjectsFitScriptIntoHTML: served HTML carries the size-report
// script (the pane's zoom-to-fit depends on it), other types stay untouched.
func TestPreviewInjectsFitScriptIntoHTML(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "page.html")
	if err := os.WriteFile(page, []byte("<!doctype html><html><body><h1>fixed width</h1></body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(dir, "a.png")
	if err := os.WriteFile(img, []byte{0x89, 'P', 'N', 'G'}, 0o644); err != nil {
		t.Fatal(err)
	}
	port, err := appPreview.start()
	if err != nil {
		t.Fatal(err)
	}
	u1, err := (&App{}).PreviewLocalFile(page)
	if err != nil {
		t.Fatal(err)
	}
	u2, err := (&App{}).PreviewLocalFile(img)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(u1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "__hiqPreviewSize") {
		t.Fatal("html response missing the fit-report script")
	}
	if !strings.Contains(string(body), "fixed width") {
		t.Fatal("injection must not disturb the original markup")
	}
	// Sibling html (not the registered entry) is also instrumented; png is not.
	_ = port
	resp2, err := http.Get(u2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	raw2, _ := io.ReadAll(resp2.Body)
	if strings.Contains(string(raw2), "__hiqPreviewSize") {
		t.Fatal("non-html response must not be injected")
	}
}
