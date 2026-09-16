package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/zzycxz/fairpeer/internal/config"
)

// Custom wallpaper for the desktop shell.
//
// The user picks one image; it is copied into the app wallpaper directory so the
// UI keeps working after the original file is moved, renamed or deleted. The
// webview then loads it through a read-only asset route. Blur and scrim strength
// are applied purely in CSS (see frontend/src/styles/wallpaper.css) so changing
// them is instant and never re-encodes the image.
//
// Everything here is UI-only: none of it reaches model prompts, provider
// requests, or CLI output.

const (
	// wallpaperRoutePrefix is served by wallpaperMiddleware through the Wails
	// AssetServer, bypassing the embedded frontend bundle.
	wallpaperRoutePrefix = "/__fairpeer_wallpaper/"

	// wallpaperMaxBytes caps the accepted source image. A larger file is
	// rejected with a readable message instead of freezing the webview while it
	// decodes a 100 MP photo.
	wallpaperMaxBytes = 32 << 20 // 32 MiB
)

// wallpaperExts maps accepted extensions to the MIME type served for them. The
// list is deliberately webview-safe: no TIFF/HEIC, which WebKit may refuse.
var wallpaperExts = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
	".bmp":  "image/bmp",
	".avif": "image/avif",
}

// WallpaperView is the wallpaper contract the frontend consumes. URL is empty
// when no wallpaper is active, and always carries a cache-busting version so
// replacing an image with the same extension is picked up immediately.
type WallpaperView struct {
	Active bool   `json:"active"`
	URL    string `json:"url"`
	Name   string `json:"name"`
	Blur   int    `json:"blur"`
	Dim    int    `json:"dim"`
	Fit    string `json:"fit"`
}

// wallpaperDir is <user config dir>/wallpapers. It sits beside config.toml and
// credentials so all fairpeer-owned user state lives in one place.
func wallpaperDir() string {
	cfgPath := config.UserConfigPath()
	if cfgPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(cfgPath), "wallpapers")
}

// activeWallpaperFile returns the absolute path of the stored image matching
// path (a config-recorded path), but only when it really exists inside the
// wallpaper directory. Anything else — a deleted file, a stale path from
// another machine, a path pointing outside the directory — resolves to "".
func activeWallpaperFile(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	dir := wallpaperDir()
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.ContainsAny(rel, `/\`) {
		return ""
	}
	if _, ok := wallpaperExts[strings.ToLower(filepath.Ext(abs))]; !ok {
		return ""
	}
	if info, err := os.Stat(abs); err != nil || info.IsDir() {
		return ""
	}
	return abs
}

// wallpaperURL builds the asset-route URL for a stored image. The version query
// is the file modification time, which changes on every replacement and keeps
// the webview from showing a cached copy.
func wallpaperURL(abs string) string {
	info, err := os.Stat(abs)
	if err != nil {
		return ""
	}
	version := fmt.Sprintf("%d", info.ModTime().UnixNano())
	return wallpaperRoutePrefix + filepath.Base(abs) + "?v=" + version
}

// readUserConfig loads the user-level config for reading. A missing file yields
// defaults rather than an error, so the desktop can always report a wallpaper
// state.
func (a *App) readUserConfig() *config.Config {
	path := config.UserConfigPath()
	if path == "" {
		return &config.Config{}
	}
	return config.LoadForEdit(path)
}

// WallpaperInfo reports the active wallpaper and its tuning. It never fails:
// an unreadable config or a missing image simply reports "not active", which
// leaves the user on the plain themed background.
func (a *App) WallpaperInfo() WallpaperView {
	cfg := a.readUserConfig()
	abs := activeWallpaperFile(cfg.DesktopWallpaperPath())

	view := WallpaperView{
		Blur: cfg.DesktopWallpaperBlur(),
		Dim:  cfg.DesktopWallpaperDim(),
		Fit:  cfg.DesktopWallpaperFit(),
	}
	if abs == "" {
		return view
	}
	view.Active = true
	view.URL = wallpaperURL(abs)
	view.Name = filepath.Base(abs)
	return view
}

// PickWallpaper opens a native file picker and adopts the chosen image as the
// wallpaper. The file is validated, copied into the wallpaper directory, and
// recorded in config. A cancelled picker returns the unchanged current state.
func (a *App) PickWallpaper() (WallpaperView, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择背景图片",
		Filters: []runtime.FileFilter{
			{DisplayName: "图片 (*.png;*.jpg;*.jpeg;*.webp;*.gif;*.bmp;*.avif)", Pattern: "*.png;*.jpg;*.jpeg;*.webp;*.gif;*.bmp;*.avif"},
		},
	})
	if err != nil {
		return WallpaperView{}, fmt.Errorf("打开文件选择器失败: %w", err)
	}
	if path == "" {
		// User cancelled — keep whatever is active.
		return a.WallpaperInfo(), nil
	}

	ext := strings.ToLower(filepath.Ext(path))
	if _, ok := wallpaperExts[ext]; !ok {
		return WallpaperView{}, fmt.Errorf("不支持的图片格式 %s（支持 png/jpg/jpeg/webp/gif/bmp/avif）", ext)
	}
	info, err := os.Stat(path)
	if err != nil {
		return WallpaperView{}, fmt.Errorf("读取图片失败: %w", err)
	}
	if info.IsDir() {
		return WallpaperView{}, fmt.Errorf("请选择图片文件而不是文件夹")
	}
	if info.Size() > wallpaperMaxBytes {
		return WallpaperView{}, fmt.Errorf("图片过大（%.1f MB），请选择小于 %d MB 的图片", float64(info.Size())/(1<<20), wallpaperMaxBytes>>20)
	}

	dir := wallpaperDir()
	if dir == "" {
		return WallpaperView{}, fmt.Errorf("无法定位用户配置目录")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return WallpaperView{}, fmt.Errorf("创建背景图目录失败: %w", err)
	}

	// One wallpaper at a time: drop any previous file (which may use a different
	// extension) so the directory never accumulates stale images.
	removeStoredWallpapers(dir)

	dest := filepath.Join(dir, "wallpaper"+ext)
	src, err := os.ReadFile(path)
	if err != nil {
		return WallpaperView{}, fmt.Errorf("读取图片失败: %w", err)
	}
	if err := os.WriteFile(dest, src, 0o644); err != nil {
		return WallpaperView{}, fmt.Errorf("保存背景图失败: %w", err)
	}

	cfg := a.readUserConfig()
	if err := a.applyConfigOnly(func(c *config.Config) error {
		return c.SetDesktopWallpaper(dest, cfg.DesktopWallpaperBlur(), cfg.DesktopWallpaperDim(), cfg.DesktopWallpaperFit())
	}); err != nil {
		return WallpaperView{}, fmt.Errorf("保存背景图设置失败: %w", err)
	}

	return a.WallpaperInfo(), nil
}

// ClearWallpaper removes the stored image and the preference, restoring the
// plain themed background.
func (a *App) ClearWallpaper() error {
	if dir := wallpaperDir(); dir != "" {
		removeStoredWallpapers(dir)
	}
	return a.applyConfigOnly(func(c *config.Config) error {
		c.ClearDesktopWallpaper()
		return nil
	})
}

// SetWallpaperOptions updates the blur/scrim/fit tuning without touching the
// image. Called live while the user drags a slider; out-of-range values are
// clamped rather than rejected so a slider can never produce an error state.
func (a *App) SetWallpaperOptions(blur, dim int, fit string) error {
	path := a.readUserConfig().DesktopWallpaperPath()
	return a.applyConfigOnly(func(c *config.Config) error {
		return c.SetDesktopWallpaper(path, blur, dim, fit)
	})
}

// removeStoredWallpapers deletes wallpaper.* files in dir, ignoring failures so
// a locked file never blocks adopting a new image.
func removeStoredWallpapers(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "wallpaper.") {
			continue
		}
		if _, ok := wallpaperExts[strings.ToLower(filepath.Ext(name))]; !ok {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// wallpaperMiddleware serves stored wallpaper images over
// /__fairpeer_wallpaper/{file}. Only a flat file name with an accepted image
// extension inside the wallpaper directory is served — no traversal, no other
// files on disk. The image is not secret, but the route stays deliberately
// narrow so a compromised webview cannot use it to read arbitrary files.
func (a *App) wallpaperMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, wallpaperRoutePrefix) {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}

			dir := wallpaperDir()
			if dir == "" {
				http.NotFound(w, r)
				return
			}

			name := strings.TrimPrefix(r.URL.Path, wallpaperRoutePrefix)
			// Reject anything that is not a single flat file name.
			if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
				http.NotFound(w, r)
				return
			}
			ext := strings.ToLower(filepath.Ext(name))
			mime, ok := wallpaperExts[ext]
			if !ok {
				http.NotFound(w, r)
				return
			}

			abs := filepath.Join(dir, name)
			f, err := os.Open(abs)
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

			w.Header().Set("Content-Type", mime)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			// Revalidate on every load: the file is tiny and local, and a
			// replaced image must never come from a stale cache.
			w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
			http.ServeContent(w, r, name, info.ModTime(), f)
		})
	}
}
