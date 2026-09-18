package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// isolateUserConfig redirects the hiq user config dir at a temp directory
// so these tests never read or write the real %AppData%\hiq (which holds
// the user's actual wallpaper).
func isolateUserConfig(t *testing.T) string {
	t.Helper()
	// os.UserConfigDir reads %AppData% on Windows and XDG_CONFIG_HOME/HOME on
	// Unix; setting all three covers every platform the desktop ships on.
	home := t.TempDir()
	t.Setenv("AppData", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("HOME", home)

	dir := wallpaperDir()
	if dir == "" {
		t.Fatal("wallpaperDir resolved to empty after redirecting the config dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create wallpaper dir: %v", err)
	}
	return dir
}

// passThroughHandler stands in for the embedded frontend bundle: anything the
// wallpaper middleware does not claim must reach it unchanged.
func passThroughHandler() (http.Handler, *bool) {
	reached := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fallthrough"))
	})
	return handler, &reached
}

func TestWallpaperMiddlewareServesStoredImage(t *testing.T) {
	dir := isolateUserConfig(t)
	payload := []byte("\x89PNG\r\n\x1a\nnot-a-real-png-but-that-is-fine")
	if err := os.WriteFile(filepath.Join(dir, "wallpaper.png"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	next, reached := passThroughHandler()
	handler := app.wallpaperMiddleware()(next)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, wallpaperRoutePrefix+"wallpaper.png", nil))

	if *reached {
		t.Fatal("wallpaper request fell through to the frontend handler")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content-type = %q, want image/png", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=0, must-revalidate" {
		t.Fatalf("cache-control = %q, want revalidation so a replaced image is never stale", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("body mismatch: got %q", rec.Body.Bytes())
	}
}

func TestWallpaperMiddlewareRejectsTraversalAndOtherFiles(t *testing.T) {
	dir := isolateUserConfig(t)
	if err := os.WriteFile(filepath.Join(dir, "wallpaper.png"), []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file that exists just outside the wallpaper directory: the route must
	// never be able to reach it.
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "secret.png"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	next, _ := passThroughHandler()
	handler := app.wallpaperMiddleware()(next)

	cases := []struct {
		name   string
		target string
		method string
		want   int
	}{
		{"traversal with encoded slash", wallpaperRoutePrefix + "..%2Fsecret.png", http.MethodGet, http.StatusNotFound},
		{"traversal with dots", wallpaperRoutePrefix + "../secret.png", http.MethodGet, http.StatusNotFound},
		{"bare parent", wallpaperRoutePrefix + "..", http.MethodGet, http.StatusNotFound},
		{"non-image extension", wallpaperRoutePrefix + "config.toml", http.MethodGet, http.StatusNotFound},
		{"missing image", wallpaperRoutePrefix + "wallpaper.jpg", http.MethodGet, http.StatusNotFound},
		{"empty name", wallpaperRoutePrefix, http.MethodGet, http.StatusNotFound},
		{"write method", wallpaperRoutePrefix + "wallpaper.png", http.MethodPost, http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))
			if rec.Code != tc.want {
				t.Fatalf("%s %s: status = %d, want %d", tc.method, tc.target, rec.Code, tc.want)
			}
		})
	}
}

func TestWallpaperMiddlewarePassesOtherRoutesThrough(t *testing.T) {
	isolateUserConfig(t)

	app := NewApp()
	next, reached := passThroughHandler()
	handler := app.wallpaperMiddleware()(next)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.html", nil))

	if !*reached {
		t.Fatal("non-wallpaper route was not passed through to the frontend handler")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestActiveWallpaperFileRejectsPathsOutsideWallpaperDir(t *testing.T) {
	dir := isolateUserConfig(t)

	inside := filepath.Join(dir, "wallpaper.png")
	if err := os.WriteFile(inside, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := activeWallpaperFile(inside); got == "" {
		t.Fatal("stored path inside the wallpaper dir should resolve")
	}

	outside := filepath.Join(t.TempDir(), "elsewhere.png")
	if err := os.WriteFile(outside, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := activeWallpaperFile(outside); got != "" {
		t.Fatalf("path outside the wallpaper dir resolved to %q, want \"\"", got)
	}

	if got := activeWallpaperFile(filepath.Join(dir, "wallpaper.txt")); got != "" {
		t.Fatalf("non-image extension resolved to %q, want \"\"", got)
	}
	if got := activeWallpaperFile(filepath.Join(dir, "gone.png")); got != "" {
		t.Fatalf("missing file resolved to %q, want \"\"", got)
	}
	if got := activeWallpaperFile(""); got != "" {
		t.Fatalf("empty path resolved to %q, want \"\"", got)
	}
}

func TestWallpaperInfoDefaultsWhenUnset(t *testing.T) {
	isolateUserConfig(t)

	info := NewApp().WallpaperInfo()
	if info.Active {
		t.Fatal("no wallpaper configured should not report active")
	}
	if info.URL != "" {
		t.Fatalf("URL = %q, want empty", info.URL)
	}
	if info.Blur != config.DesktopWallpaperBlurDefault {
		t.Fatalf("blur = %d, want default %d", info.Blur, config.DesktopWallpaperBlurDefault)
	}
	if info.Dim != config.DesktopWallpaperDimDefault {
		t.Fatalf("dim = %d, want default %d", info.Dim, config.DesktopWallpaperDimDefault)
	}
	if info.Fit != "cover" {
		t.Fatalf("fit = %q, want cover", info.Fit)
	}
}

func TestRemoveStoredWallpapersOnlyDropsImages(t *testing.T) {
	dir := isolateUserConfig(t)
	for _, name := range []string{"wallpaper.png", "wallpaper.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("img"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Not a wallpaper: must survive the sweep.
	keep := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	removeStoredWallpapers(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "notes.txt" {
		t.Fatalf("after sweep dir = %v, want only notes.txt", names)
	}
}

// TestSetDesktopWallpaperNormalizes pins the config contract the frontend
// depends on: out-of-range tuning is clamped, not rejected, so a slider can
// never drive the app into an error state.
func TestSetDesktopWallpaperNormalizes(t *testing.T) {
	var c config.Config
	if err := c.SetDesktopWallpaper("/tmp/a.png", 999, -5, "TILE"); err != nil {
		t.Fatalf("SetDesktopWallpaper: %v", err)
	}
	if got := c.DesktopWallpaperBlur(); got != config.DesktopWallpaperBlurMax {
		t.Fatalf("blur = %d, want clamped to %d", got, config.DesktopWallpaperBlurMax)
	}
	if got := c.DesktopWallpaperDim(); got != 0 {
		t.Fatalf("dim = %d, want clamped to 0", got)
	}
	if got := c.DesktopWallpaperFit(); got != "tile" {
		t.Fatalf("fit = %q, want tile", got)
	}
	if err := c.SetDesktopWallpaper("/tmp/a.png", 10, 10, "stretch"); err == nil {
		t.Fatal("unknown fit should be rejected")
	}

	c.ClearDesktopWallpaper()
	if c.DesktopWallpaperPath() != "" || c.DesktopWallpaperBlur() != config.DesktopWallpaperBlurDefault {
		t.Fatal("ClearDesktopWallpaper should drop path and reset tuning to defaults")
	}
}
