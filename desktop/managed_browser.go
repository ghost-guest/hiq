package main

// managed_browser.go implements the 可控浏览器 (managed attachable browser):
// a regular browser window the user keeps open, launched once with a stable
// CDP port (9222) and a dedicated persistent profile. Browser automation then
// ATTACHES to it ([cowork] browser_attach_url) instead of spawning a fresh
// temp browser per task — logins and cookies persist, the window (and its
// tabs) survive between tasks, and the user can watch/drive it too.
//
// Chromium constraints that shape this design: CDP requires
// --remote-debugging-port at start (no attaching to an arbitrarily running
// browser), and Chrome 136+ refuses the debug port on the DEFAULT user-data
// dir — hence a dedicated profile rather than the user's daily one. The
// profile lives under <user-config>/hiq/browser-profile (or the
// configured browser_user_data_dir), so a one-time login in this window
// sticks for every later task.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/zzycxz/hiq/internal/browserlaunch"
	"github.com/zzycxz/hiq/internal/config"
)

// managedBrowserPort is the fixed CDP port of the managed browser. Stable
// across restarts so the attach URL saved in settings keeps working.
const managedBrowserPort = 9222

// ManagedBrowserStatus reports the managed browser's state. The frontend
// composes its toasts/hints from these fields.
type ManagedBrowserStatus struct {
	// Running is true when a debug-enabled browser answers on the managed port.
	Running bool `json:"running"`
	// URL is the attach URL to save into browser_attach_url.
	URL string `json:"url"`
	// Browser is the display name ("Chrome"/"Edge"/…) of what's running.
	Browser string `json:"browser"`
	// Profile is the persistent user-data-dir in use.
	Profile string `json:"profile"`
	// AlreadyRunning is true when Start found the endpoint already live
	// (from an earlier launch, or the user started one manually).
	AlreadyRunning bool `json:"alreadyRunning"`
	// Detail carries error/diagnostic context (empty on success).
	Detail string `json:"detail,omitempty"`
}

// managedBrowserURL is the canonical attach endpoint of the managed browser.
func managedBrowserURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", managedBrowserPort)
}

// managedProfileDir resolves the persistent profile dir: the configured
// browser_user_data_dir when set, else <user-config>/hiq/browser-profile.
func managedProfileDir(c config.CoworkConfig) string {
	if d := filepath.Clean(c.BrowserUserDataDir); d != "" && d != "." {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return "hiq-browser-profile"
	}
	return filepath.Join(base, "hiq", "browser-profile")
}

// CheckManagedBrowser probes the managed endpoint without launching anything.
// Used by the settings panel to show live state.
func (a *App) CheckManagedBrowser() ManagedBrowserStatus {
	url := managedBrowserURL()
	status := ManagedBrowserStatus{URL: url}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if info, err := browserlaunch.ProbeAttach(ctx, url); err == nil {
		status.Running = true
		status.AlreadyRunning = true
		status.Browser = info.BrowserName
	} else {
		status.Detail = err.Error()
	}
	cfg, err := config.LoadForRoot(a.activeWorkspaceRoot())
	if err == nil {
		status.Profile = managedProfileDir(cfg.Cowork)
	}
	return status
}

// OpenURLInManagedBrowser ensures the managed browser is running (launching it
// if needed) and opens target as a new tab in it. The preview pane's
// companion-window tier (pane-system spec §3.6): full browser — logins,
// devtools, extensions — in the persistent managed profile, on every OS
// without any window-embedding native code.
func (a *App) OpenURLInManagedBrowser(target string) (ManagedBrowserStatus, error) {
	status, err := a.StartManagedBrowser()
	if err != nil {
		return status, err
	}
	if !status.Running {
		return status, fmt.Errorf("可控浏览器未就绪")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := browserlaunch.OpenTab(ctx, managedBrowserURL(), target); err != nil {
		return status, fmt.Errorf("在可控浏览器中打开失败: %w", err)
	}
	return status, nil
}

// StartManagedBrowser launches (or reports) the managed attachable browser:
// headed, fixed port 9222, persistent profile. It keeps running after the
// call returns — we deliberately never call Handle.Close, so the window stays
// open until the user closes it; a later Start re-probes and relaunches with
// the same profile when needed.
func (a *App) StartManagedBrowser() (ManagedBrowserStatus, error) {
	url := managedBrowserURL()

	// Already live? (Started earlier this session, a previous app session, or
	// manually by the user with the same flags.) Just report it — attaching
	// twice would open a redundant window.
	if status := a.CheckManagedBrowser(); status.Running {
		return status, nil
	}

	cfg, err := config.LoadForRoot(a.activeWorkspaceRoot())
	if err != nil {
		cfg = &config.Config{}
	}
	profile := managedProfileDir(cfg.Cowork)

	handle, err := browserlaunch.Launch(context.Background(), browserlaunch.LaunchOptions{
		ExecutablePath: cfg.Cowork.BrowserPath,
		UserDataDir:    profile,
		Port:           managedBrowserPort,
		// Always headed: this window is meant for the user to see and use —
		// logins happen here, and "watch the agent work" is half the point.
		Headless: false,
	})
	if err != nil {
		return ManagedBrowserStatus{URL: url, Profile: profile, Detail: err.Error()},
			fmt.Errorf("启动可控浏览器失败: %w", err)
	}

	a.mu.Lock()
	a.managedBrowser = handle
	a.mu.Unlock()
	slog.Info("managed browser started",
		"browser", handle.BrowserName, "cdp", handle.CDPURL, "profile", profile)

	return ManagedBrowserStatus{
		Running: true,
		URL:     url,
		Browser: handle.BrowserName,
		Profile: profile,
	}, nil
}
