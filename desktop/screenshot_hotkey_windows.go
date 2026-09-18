package main

// screenshot_hotkey_windows.go implements the Windows-specific hotkey detection
// for the screenshot solve feature. Uses GetAsyncKeyState polling (50ms interval)
// instead of RegisterHotKey + WM_HOTKEY messages for reliability.
//
// The cross-platform solving logic (capture → VLM → IM push) lives in
// screenshot_solve.go. Platform-specific capture lives in:
//   - internal/tool/builtin/screen_windows.go (Win32 BitBlt)
//   - internal/tool/builtin/capture_darwin.go (macOS screencapture)
//   - internal/tool/builtin/capture_linux.go (Linux scrot/gnome-screenshot/grim)

import (
	"fmt"
	"log/slog"
	"sync"
	"syscall"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

// Win32 API for keyboard state polling.
var (
	user32DLL            = syscall.NewLazyDLL("user32.dll")
	procGetAsyncKeyState = user32DLL.NewProc("GetAsyncKeyState")
)

// Win32 modifier key VK codes.
const (
	vkControl = 0x11
	vkShift   = 0x10
	vkAlt     = 0x12
	vkLWin    = 0x5B
	vkRWin    = 0x5C
)

// hotkeyManager polls keyboard state via GetAsyncKeyState to detect the
// configured hotkey combination.
type hotkeyManager struct {
	app      *App
	stopCh   chan struct{}
	stopOnce sync.Once
	mainVK   uint16
	ctrl     bool
	shift    bool
	alt      bool
	win      bool
}

// StartScreenshotHotkey begins polling for the configured hotkey.
func (a *App) StartScreenshotHotkey() {
	cfg, err := config.Load()
	if err != nil || !cfg.Cowork.ScreenshotEnabled {
		return
	}
	hk, err := newHotkeyManager(a, cfg.Cowork.ScreenshotHotkey)
	if err != nil {
		slog.Warn("screenshot: invalid hotkey config", "hotkey", cfg.Cowork.ScreenshotHotkey, "err", err)
		return
	}
	a.mu.Lock()
	a.hotkeyMgr = hk
	a.mu.Unlock()
	slog.Info("screenshot: hotkey polling started", "hotkey", cfg.Cowork.ScreenshotHotkey)
	go hk.loop()
}

// StopScreenshotHotkey stops the polling loop.
func (a *App) StopScreenshotHotkey() {
	a.mu.Lock()
	hk := a.hotkeyMgr
	a.hotkeyMgr = nil
	a.mu.Unlock()
	if hk != nil {
		hk.Stop()
	}
}

func newHotkeyManager(app *App, hotkeyStr string) (*hotkeyManager, error) {
	mod, vk, err := parseHotkey(hotkeyStr)
	if err != nil {
		return nil, err
	}
	return &hotkeyManager{
		app:    app,
		stopCh: make(chan struct{}),
		mainVK: uint16(vk),
		ctrl:   (mod & 0x0002) != 0,
		shift:  (mod & 0x0004) != 0,
		alt:    (mod & 0x0001) != 0,
		win:    (mod & 0x0008) != 0,
	}, nil
}

func (h *hotkeyManager) loop() {
	slog.Warn("screenshot: polling loop STARTED", "mainVK", fmt.Sprintf("0x%X", h.mainVK), "ctrl", h.ctrl, "shift", h.shift, "alt", h.alt)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	lastTrigger := time.Time{}
	wasPressed := false
	debugTick := time.NewTicker(5 * time.Second)
	defer debugTick.Stop()
	for {
		select {
		case <-h.stopCh:
			return
		case <-ticker.C:
			pressed := h.checkKeys()
			if pressed && !wasPressed && time.Since(lastTrigger) > 500*time.Millisecond {
				lastTrigger = time.Now()
				slog.Warn("screenshot: HOTKEY DETECTED via polling!")
				h.app.triggerScreenshotSolve()
			}
			wasPressed = pressed
		case <-debugTick.C:
			mainDown := isKeyDown(h.mainVK)
			ctrlDown := isKeyDown(vkControl)
			slog.Debug("screenshot: polling heartbeat", "main_key_down", mainDown, "ctrl_down", ctrlDown)
		}
	}
}

func (h *hotkeyManager) checkKeys() bool {
	if !isKeyDown(h.mainVK) {
		return false
	}
	if h.ctrl && !isKeyDown(vkControl) {
		return false
	}
	if h.shift && !isKeyDown(vkShift) {
		return false
	}
	if h.alt && !isKeyDown(vkAlt) {
		return false
	}
	if h.win && !isKeyDown(vkLWin) && !isKeyDown(vkRWin) {
		return false
	}
	return true
}

// isKeyDown reports whether the given virtual key is currently held, per
// GetAsyncKeyState. The API returns a SHORT whose high bit (0x8000) is set when
// the key is down. syscall.Call returns it as a uintptr (64-bit on amd64), so
// we mask to the low 16 bits first to drop any sign-extension / upper-word
// noise, then test the high bit. GetAsyncKeyState is global (not tied to the
// calling thread's message queue), so it works even when hiq lacks focus
// and does not require LockOSThread — unlike RegisterHotKey + WM_HOTKEY.
func isKeyDown(vk uint16) bool {
	ret, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return (uint16(ret) & 0x8000) != 0
}

func (h *hotkeyManager) Stop() {
	h.stopOnce.Do(func() {
		close(h.stopCh)
	})
}
