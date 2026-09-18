package main

// screenshot_hotkey_linux.go implements hotkey detection for Linux using
// xset to poll modifier key state. No CGO required.
//
// Requires: xset (part of xorg, usually pre-installed on desktop Linux)

import (
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

type hotkeyManager struct {
	app      *App
	stopCh   chan struct{}
	stopOnce sync.Once
	ctrl     bool
	shift    bool
	alt      bool
	super    bool
}

func (a *App) StartScreenshotHotkey() {
	cfg, err := config.Load()
	if err != nil || !cfg.Cowork.ScreenshotEnabled {
		return
	}
	mod, _, err := parseHotkey(cfg.Cowork.ScreenshotHotkey)
	if err != nil {
		slog.Warn("screenshot: invalid hotkey config", "hotkey", cfg.Cowork.ScreenshotHotkey, "err", err)
		return
	}
	hk := &hotkeyManager{
		app:    a,
		stopCh: make(chan struct{}),
		ctrl:   (mod & 0x0002) != 0,
		shift:  (mod & 0x0004) != 0,
		alt:    (mod & 0x0001) != 0,
		super:  (mod & 0x0008) != 0,
	}
	a.mu.Lock()
	a.hotkeyMgr = hk
	a.mu.Unlock()
	slog.Info("screenshot: hotkey polling started (Linux)", "hotkey", cfg.Cowork.ScreenshotHotkey)
	go hk.loop()
}

func (a *App) StopScreenshotHotkey() {
	a.mu.Lock()
	hk := a.hotkeyMgr
	a.hotkeyMgr = nil
	a.mu.Unlock()
	if hk != nil {
		hk.Stop()
	}
}

func (h *hotkeyManager) loop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	lastTrigger := time.Time{}
	for {
		select {
		case <-h.stopCh:
			return
		case <-ticker.C:
			if time.Since(lastTrigger) < 1*time.Second {
				continue
			}
			if h.checkKeys() {
				lastTrigger = time.Now()
				slog.Warn("screenshot: HOTKEY DETECTED (Linux)")
				h.app.triggerScreenshotSolve()
			}
		}
	}
}

func (h *hotkeyManager) Stop() {
	h.stopOnce.Do(func() { close(h.stopCh) })
}

// checkKeys uses xset q to check modifier state. Main key detection is not
// reliably possible via CLI on Linux without CGO, so the hotkey triggers on
// modifier combination alone. The tray menu is the recommended trigger on Linux.
func (h *hotkeyManager) checkKeys() bool {
	cmd := exec.Command("xset", "q")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	output := string(out)
	if h.ctrl && !strings.Contains(output, "Ctrl") {
		return false
	}
	if h.shift && !strings.Contains(output, "Shift") {
		return false
	}
	if h.alt && !strings.Contains(output, "Mod1") {
		return false
	}
	if h.super && !strings.Contains(output, "Mod4") {
		return false
	}
	return true
}
