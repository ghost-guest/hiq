package main

// browser_panel_app.go — Wails bindings for the cowork dock's interactive
// browser panel (the live-tier companion of the mirror): panel visibility
// flow control, pointer/keyboard input forwarding, toolbar navigation, and
// the tab strip. All calls delegate to the kernel's browserpanel.go — the
// desktop layer is deliberately thin (validate + forward), mirroring how
// browser_console_app.go wraps the console session.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/zzycxz/hiq/internal/tool/builtin"
)

// BrowserPanelSetVisible turns live screencast streaming on/off for all
// browser sessions. The panel calls it on mount (dock tab active) / unmount.
func (a *App) BrowserPanelSetVisible(visible bool) {
	builtin.SetPanelStreaming(visible)
}

// BrowserPanelDispatch forwards one pointer/keyboard action to the session's
// current tab (coordinates arrive frame-normalized; kernel maps to CSS px).
func (a *App) BrowserPanelDispatch(sessionID string, ev builtin.PanelInputEvent) error {
	return builtin.PanelDispatchInput(sessionID, ev)
}

// BrowserPanelNavigate drives the address bar.
func (a *App) BrowserPanelNavigate(sessionID string, url string) error {
	return builtin.PanelNavigate(sessionID, url)
}

// BrowserPanelBack / BrowserPanelForward / BrowserPanelReload drive the
// toolbar buttons.
func (a *App) BrowserPanelBack(sessionID string) error {
	return builtin.PanelBack(sessionID)
}

func (a *App) BrowserPanelForward(sessionID string) error {
	return builtin.PanelForward(sessionID)
}

func (a *App) BrowserPanelReload(sessionID string) error {
	return builtin.PanelReload(sessionID)
}

// BrowserPanelTabs lists the session's page targets for the tab strip.
func (a *App) BrowserPanelTabs(sessionID string) ([]builtin.PanelTab, error) {
	return builtin.PanelTabs(sessionID)
}

// BrowserPanelSwitchTab moves the session (and the live stream) to the given
// target id.
func (a *App) BrowserPanelSwitchTab(sessionID string, tabID string) error {
	return builtin.PanelSwitchTab(sessionID, tabID)
}

// BrowserPanelStartPick arms the element picker: the page highlights hovered
// elements and the next click reports a descriptor (panel → chip).
func (a *App) BrowserPanelStartPick(sessionID string) error {
	return builtin.PanelStartPick(sessionID)
}

// BrowserPanelStopPick cancels an armed element picker.
func (a *App) BrowserPanelStopPick(sessionID string) error {
	return builtin.PanelStopPick(sessionID)
}

// BrowserPanelTakePick returns and clears the latest picked element, if any.
func (a *App) BrowserPanelTakePick(sessionID string) (*builtin.PickDescriptor, error) {
	return builtin.PanelTakePick(sessionID)
}

// --- phase 3: downloads / find / login-state / bookmarks ----------------------

// BrowserPanelDownloads lists the session's download records for the panel.
func (a *App) BrowserPanelDownloads(sessionID string) ([]builtin.PanelDownloadPayload, error) {
	return builtin.PanelDownloads(sessionID)
}

// BrowserPanelFindInPage highlights query occurrences (injected highlighter).
func (a *App) BrowserPanelFindInPage(sessionID string, query string) (int, error) {
	return builtin.PanelFindInPage(sessionID, query)
}

// BrowserPanelFindClear removes the find highlight.
func (a *App) BrowserPanelFindClear(sessionID string) error {
	return builtin.PanelFindClear(sessionID)
}

// BrowserPanelExportState saves the session's cookie store (login state) to
// a JSON file chosen by the user.
func (a *App) BrowserPanelExportState(sessionID string) (string, error) {
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           "导出登录状态",
		DefaultFilename: "hiq-login-state.json",
		Filters:         []wailsruntime.FileFilter{{DisplayName: "hiq 登录状态 (*.json)", Pattern: "*.json"}},
	})
	if err != nil || path == "" {
		return "", err
	}
	if err := builtin.PanelExportState(sessionID, path); err != nil {
		return "", err
	}
	return path, nil
}

// BrowserPanelImportState loads a login-state file and applies its cookies.
func (a *App) BrowserPanelImportState(sessionID string) (int, error) {
	path, err := wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title:   "导入登录状态",
		Filters: []wailsruntime.FileFilter{{DisplayName: "hiq 登录状态 (*.json)", Pattern: "*.json"}},
	})
	if err != nil || path == "" {
		return 0, err
	}
	return builtin.PanelImportState(sessionID, path)
}

// --- bookmarks (desktop-side JSON store) ---------------------------------------

// BrowserBookmark is one saved page shortcut.
type BrowserBookmark struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	CreatedAt int64  `json:"created_at_ms"`
}

func browserBookmarksPath() string {
	return filepath.Join(desktopConfigDir(), "browser_bookmarks.json")
}

// BrowserBookmarksList returns the saved bookmarks.
func (a *App) BrowserBookmarksList() ([]BrowserBookmark, error) {
	b, err := os.ReadFile(browserBookmarksPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []BrowserBookmark{}, nil
		}
		return nil, err
	}
	var out []BrowserBookmark
	if err := json.Unmarshal(b, &out); err != nil {
		return []BrowserBookmark{}, nil // corrupt file — start fresh, never block the panel
	}
	return out, nil
}

// BrowserBookmarkToggle adds the URL (with title) or removes it when already
// bookmarked; returns whether it is now bookmarked.
func (a *App) BrowserBookmarkToggle(url string, title string) (bool, error) {
	if url == "" {
		return false, errors.New("empty url")
	}
	existing, err := a.BrowserBookmarksList()
	if err != nil {
		return false, err
	}
	for i, b := range existing {
		if b.URL == url {
			out := append(existing[:i:i], existing[i+1:]...)
			bm, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return false, err
			}
			return false, os.WriteFile(browserBookmarksPath(), bm, 0o600)
		}
	}
	created := append(existing, BrowserBookmark{
		ID:        strconv.FormatInt(time.Now().UnixNano(), 36),
		Title:     title,
		URL:       url,
		CreatedAt: time.Now().UnixMilli(),
	})
	bm, err := json.MarshalIndent(created, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(browserBookmarksPath(), bm, 0o600)
}
