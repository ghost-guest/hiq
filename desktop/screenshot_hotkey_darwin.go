package main

// screenshot_hotkey_darwin.go implements hotkey detection for macOS using
// osascript to poll keyboard state. No CGO required.

import (
	"fmt"
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
	hotkey   string // e.g. "Ctrl+Shift+Alt+W"
}

func (a *App) StartScreenshotHotkey() {
	cfg, err := config.Load()
	if err != nil || !cfg.Cowork.ScreenshotEnabled {
		return
	}
	hk := &hotkeyManager{app: a, stopCh: make(chan struct{}), hotkey: cfg.Cowork.ScreenshotHotkey}
	a.mu.Lock()
	a.hotkeyMgr = hk
	a.mu.Unlock()
	slog.Info("screenshot: hotkey polling started (macOS)", "hotkey", cfg.Cowork.ScreenshotHotkey)
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
			pressed, err := checkKeysMacOS(h.hotkey)
			if err != nil {
				slog.Debug("screenshot: keycheck error", "err", err)
				continue
			}
			if pressed {
				lastTrigger = time.Now()
				slog.Warn("screenshot: HOTKEY DETECTED (macOS)")
				h.app.triggerScreenshotSolve()
			}
		}
	}
}

func (h *hotkeyManager) Stop() {
	h.stopOnce.Do(func() { close(h.stopCh) })
}

// checkKeysMacOS uses osascript to check if the required modifier keys are held.
// Returns true if the combination is currently pressed.
func checkKeysMacOS(hotkey string) (bool, error) {
	// Parse hotkey to determine required modifiers and key
	mod, _, err := parseHotkey(hotkey)
	if err != nil {
		return false, err
	}

	// Build osascript to check modifier state
	// macOS modifier flags: command=0x100000, shift=0x20000, option=0x80000, control=0x40000
	var checks []string
	if mod&0x0002 != 0 { // Control
		checks = append(checks, "control down")
	}
	if mod&0x0004 != 0 { // Shift
		checks = append(checks, "shift down")
	}
	if mod&0x0001 != 0 { // Alt/Option
		checks = append(checks, "option down")
	}
	if mod&0x0008 != 0 { // Win/Command
		checks = append(checks, "command down")
	}

	if len(checks) == 0 {
		return false, nil
	}

	script := fmt.Sprintf(`tell application "System Events" to return {%s}`, strings.Join(checks, ", "))
	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.Output()
	if err != nil {
		return false, nil // osascript may fail if not authorized
	}

	result := strings.TrimSpace(string(out))
	// osascript returns "true, true, ..." if all modifiers are pressed
	return strings.Contains(result, "true") && !strings.Contains(result, "false"), nil
}
