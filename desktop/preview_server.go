package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// previewLocal serves workspace files to the desktop preview pane over a
// loopback-only HTTP server. The pane's iframe cannot navigate file:// URLs
// (WebView2 blocks mixed file origins from the wails:// app), so local HTML
// artifacts — the "open the generated page" path every coding agent needs —
// go through here instead.
//
// Design: files are registered explicitly by PreviewLocalFile and addressed by
// index (/f/<n>/...), so there is no query-parameter path to traverse with.
// A registered file's sibling resources (css/js/img a page references) resolve
// under the file's own directory, containment-checked per request.
type previewLocal struct {
	mu      sync.Mutex
	srv     *http.Server
	port    int
	entries []string // registered absolute file paths; index == position
}

// previewableExt lists the extensions the pane can meaningfully render.
// Everything else is rejected up front so the server never becomes a generic
// file reader.
var previewableExt = map[string]string{
	".html": "text/html; charset=utf-8",
	".htm":  "text/html; charset=utf-8",
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
	".pdf":  "application/pdf",
	".md":   "text/plain; charset=utf-8", // display, don't download
	".txt":  "text/plain; charset=utf-8",
	".json": "application/json; charset=utf-8",
	".xml":  "text/plain; charset=utf-8",
}

var appPreview = &previewLocal{}

// PreviewLocalFile registers an absolute file path with the loopback preview
// server (started lazily on first use) and returns its pane URL. Only existing
// regular files with a previewable extension are accepted; each registration
// returns a fresh URL so the iframe key change forces a reload.
func (a *App) PreviewLocalFile(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if _, ok := previewableExt[strings.ToLower(filepath.Ext(abs))]; !ok {
		return "", fmt.Errorf("unsupported preview type %q — html/svg/images/pdf/text only", filepath.Ext(abs))
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("cannot preview a directory")
	}
	port, err := appPreview.start()
	if err != nil {
		return "", err
	}
	appPreview.mu.Lock()
	idx := len(appPreview.entries)
	appPreview.entries = append(appPreview.entries, abs)
	appPreview.mu.Unlock()
	return fmt.Sprintf("http://127.0.0.1:%d/f/%d/", port, idx), nil
}

// start boots the loopback server once; the port is stable for the process
// lifetime so earlier preview URLs keep working.
func (p *previewLocal) start() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.srv != nil {
		return p.port, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("preview server listen: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/f/", p.handle)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	p.srv = srv
	p.port = ln.Addr().(*net.TCPAddr).Port
	return p.port, nil
}

// handle serves /f/<n>/ (the registered file) and /f/<n>/<relative> (siblings
// of it, for pages that reference ./style.css and friends). Any attempt to
// climb out of the file's directory is a 404, not an error page.
func (p *previewLocal) handle(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/f/")
	idxStr, rel, _ := strings.Cut(rest, "/")
	var entry string
	p.mu.Lock()
	if n, ok := parseIndex(idxStr); ok && n >= 0 && n < len(p.entries) {
		entry = p.entries[n]
	}
	p.mu.Unlock()
	if entry == "" {
		http.NotFound(w, r)
		return
	}

	target := entry
	if rel != "" && rel != "/" {
		rel = strings.TrimPrefix(rel, "/")
		clean := filepath.Clean(filepath.Join(filepath.Dir(entry), rel))
		base, err := filepath.EvalSymlinks(filepath.Dir(entry))
		if err != nil {
			base = filepath.Dir(entry)
		}
		cleanBase, err := filepath.EvalSymlinks(filepath.Dir(clean))
		if err != nil || !samePathFold(cleanBase, base) {
			http.NotFound(w, r)
			return
		}
		target = clean
	}

	f, err := os.Open(target)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	// Longest registered prefix wins so a page's own directory index is the
	// fallback only when the extension says nothing.
	ct := previewableExt[strings.ToLower(filepath.Ext(target))]
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(target), info.ModTime(), f)
}

// parseIndex keeps the URL index strictly numeric.
func parseIndex(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > 1<<20 {
			return 0, false
		}
	}
	return n, true
}

// samePathFold compares cleaned absolute paths case-insensitively (Windows
// filesystems); on POSIX the fold is harmless since paths rarely differ by case.
func samePathFold(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
