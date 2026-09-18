package main

import (
	"log/slog"
	"sync"

	"fyne.io/systray"

	"github.com/zzycxz/hiq/internal/config"
)

type desktopTray struct {
	end            func()
	openItem       *systray.MenuItem
	screenshotItem *systray.MenuItem // nil when screenshot feature is off
	quitItem       *systray.MenuItem
	once           sync.Once
}

func (a *App) startTray() {
	if !traySupported() {
		return
	}
	a.mu.Lock()
	if a.tray != nil {
		a.mu.Unlock()
		return
	}
	t := &desktopTray{}
	a.tray = t
	a.mu.Unlock()

	t.end = startDesktopTray(func() {
		systray.SetIcon(trayIconBytes)
		systray.SetTitle("hiq")
		systray.SetTooltip("hiq")
		// Run off the systray Win32 message loop: SetOnTapped fires inside wndProc,
		// so a blocking showFromTray (a wedged webview after sleep freezes
		// runtime.WindowShow) would stall the whole tray's message pump (#3834). The
		// menu items below are already decoupled via goroutines for the same reason.
		systray.SetOnTapped(func() { go a.showFromTray() })
		// Keep secondary/right-click on systray's native menu path.
		systray.SetOnSecondaryTapped(nil)

		labels := trayMenuLabels(a.trayLocale())
		t.openItem = systray.AddMenuItem(labels.openTitle, labels.openTooltip)

		// Add screenshot solve menu item when the feature is enabled.
		// This is the cross-platform reliable trigger — works on Windows,
		// macOS, and Linux without depending on keyboard hooks.
		if cfg, err := config.Load(); err == nil && cfg.Cowork.ScreenshotEnabled {
			t.screenshotItem = systray.AddMenuItem(labels.screenshotTitle, labels.screenshotTooltip)
			slog.Info("tray: screenshot menu item added (feature enabled)")
		}

		t.quitItem = systray.AddMenuItem(labels.quitTitle, labels.quitTooltip)

		a.mu.Lock()
		a.trayReady = true
		a.mu.Unlock()

		go func() {
			for range t.openItem.ClickedCh {
				a.showFromTray()
			}
		}()
		// Screenshot solve click handler — triggers the same flow as the
		// keyboard hotkey. This is the cross-platform reliable trigger.
		if t.screenshotItem != nil {
			go func() {
				for range t.screenshotItem.ClickedCh {
					slog.Info("tray: screenshot menu item clicked")
					a.triggerScreenshotSolve()
				}
			}()
		}
		go func() {
			for range t.quitItem.ClickedCh {
				a.quitFromTray()
			}
		}()
	}, func() {
		a.mu.Lock()
		a.trayReady = false
		a.mu.Unlock()
	})
}

func (a *App) stopTray() {
	a.mu.RLock()
	t := a.tray
	a.mu.RUnlock()
	if t == nil || t.end == nil {
		return
	}
	t.once.Do(t.end)
}

func (a *App) updateTrayLocale(locale string) {
	a.mu.RLock()
	t := a.tray
	var openItem, screenshotItem, quitItem *systray.MenuItem
	if t != nil {
		openItem = t.openItem
		screenshotItem = t.screenshotItem
		quitItem = t.quitItem
	}
	a.mu.RUnlock()
	if openItem == nil || quitItem == nil {
		return
	}
	labels := trayMenuLabels(locale)
	openItem.SetTitle(labels.openTitle)
	openItem.SetTooltip(labels.openTooltip)
	if screenshotItem != nil {
		screenshotItem.SetTitle(labels.screenshotTitle)
		screenshotItem.SetTooltip(labels.screenshotTooltip)
	}
	quitItem.SetTitle(labels.quitTitle)
	quitItem.SetTooltip(labels.quitTooltip)
}

func (a *App) trayLocale() string {
	cfg, _, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		return ""
	}
	return cfg.DesktopLanguage()
}

func (a *App) showFromTray() {
	a.showMainWindow()
}

func (a *App) quitFromTray() {
	a.quitApp()
}

type trayLabels struct {
	openTitle         string
	openTooltip       string
	screenshotTitle   string
	screenshotTooltip string
	quitTitle         string
	quitTooltip       string
}

func trayMenuLabels(locale string) trayLabels {
	if locale == "zh" {
		return trayLabels{
			openTitle:         "打开",
			openTooltip:       "打开 hiq 窗口",
			screenshotTitle:   "📷 截图解题",
			screenshotTooltip: "截取屏幕并 AI 解题，结果通过 IM 推送",
			quitTitle:         "退出",
			quitTooltip:       "退出 hiq",
		}
	}
	return trayLabels{
		openTitle:         "Open",
		openTooltip:       "Open the hiq window",
		screenshotTitle:   "📷 Screenshot Solve",
		screenshotTooltip: "Capture screen and AI solve, push result via IM",
		quitTitle:         "Quit",
		quitTooltip:       "Quit hiq",
	}
}
