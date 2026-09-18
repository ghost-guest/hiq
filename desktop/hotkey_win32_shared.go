//go:build windows

package main

// hotkey_win32_shared.go contains Win32 API declarations and helpers shared
// between the screenshot hotkey (GetAsyncKeyState polling) and the emergency-
// stop hotkey (RegisterHotKey + WM_HOTKEY message loop).

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Win32 constants for hotkey registration + message loop.
const (
	hotkeyID   = 0x7A21 // screenshot hotkey id
	wmHotkey   = 0x0312
	pmNoRemove = 0x0000
	pmRemove   = 0x0001
	modAlt     = 0x0001
	modControl = 0x0002
	modShift   = 0x0004
	modWin     = 0x0008
)

// Win32 API procs shared by hotkey subsystems.
var (
	procCreateWindowEx   = user32DLL.NewProc("CreateWindowExW")
	procDefWindowProc    = user32DLL.NewProc("DefWindowProcW")
	procDestroyWindow    = user32DLL.NewProc("DestroyWindow")
	procGetMessage       = user32DLL.NewProc("GetMessageW")
	procPeekMessage      = user32DLL.NewProc("PeekMessageW")
	procTranslateMessage = user32DLL.NewProc("TranslateMessage")
	procDispatchMessage  = user32DLL.NewProc("DispatchMessageW")
	procRegisterClassEx  = user32DLL.NewProc("RegisterClassExW")
	procRegisterHotKey   = user32DLL.NewProc("RegisterHotKey")
	procUnregisterHotKey = user32DLL.NewProc("UnregisterHotKey")

	kernel32DLL          = syscall.NewLazyDLL("kernel32.dll")
	procGetModuleHandleW = kernel32DLL.NewProc("GetModuleHandleW")
	procGetLastError     = kernel32DLL.NewProc("GetLastError")
)

// createMessageWindow creates a hidden message-only window for receiving
// WM_HOTKEY. It registers a WNDCLASSEX with a per-call unique class name, then
// creates a message-only window (HWND_MESSAGE parent) via CreateWindowEx.
func createMessageWindow() uintptr {
	className, _ := syscall.UTF16PtrFromString("HiqHotkey_" + strconv.Itoa(int(time.Now().UnixNano())))

	// WNDCLASSEX must match the Win32 layout EXACTLY: 12 fields, 80 bytes on
	// 64-bit. Missing hIconSm (field 12) corrupted the struct and made
	// RegisterClassEx fail silently.
	var wc struct {
		Size       uint32
		Style      uint32
		WndProc    uintptr
		ClsExtra   int32
		WndExtra   int32
		Instance   uintptr
		Icon       uintptr
		Cursor     uintptr
		Background uintptr
		MenuName   *uint16
		ClassName  *uint16
		IconSm     uintptr
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	wc.Style = 0
	wc.WndProc = syscall.NewCallback(defWindowProc)
	hMod, _, _ := procGetModuleHandleW.Call(0)
	wc.Instance = hMod
	wc.ClassName = className

	atom, _, _ := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		lastErr, _, _ := procGetLastError.Call()
		slog.Warn("hotkey: RegisterClassEx returned 0", "struct_size", wc.Size, "hInstance", hMod, "last_error", lastErr)
		return 0
	}

	windowName, _ := syscall.UTF16PtrFromString("")
	hwnd, _, _ := procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		0,
		0, 0, 0, 0,
		0, 0,
		hMod, 0,
	)
	if hwnd == 0 {
		lastErr, _, _ := procGetLastError.Call()
		slog.Warn("hotkey: CreateWindowEx returned 0 after successful RegisterClassEx", "class", className, "last_error", lastErr)
	}
	return hwnd
}

// defWindowProc is the default window procedure — just calls DefWindowProc.
func defWindowProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	ret, _, _ := procDefWindowProc.Call(hwnd, msg, wParam, lParam)
	return ret
}

// parseEStopHotkey converts "Ctrl+Shift+Pause" → (MOD_CONTROL|MOD_SHIFT, VK_PAUSE).
func parseEStopHotkey(s string) (mod, vk int, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, fmt.Errorf("empty hotkey")
	}
	parts := strings.Split(s, "+")
	vk = 0
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch strings.ToLower(p) {
		case "ctrl", "control":
			mod |= 0x0002
		case "shift":
			mod |= 0x0004
		case "alt":
			mod |= 0x0001
		case "win", "super", "meta":
			mod |= 0x0008
		default:
			if vk != 0 {
				return 0, 0, fmt.Errorf("multiple keys in hotkey %q", s)
			}
			vk = estopKeyToVK(p)
			if vk == 0 {
				return 0, 0, fmt.Errorf("unknown key %q in hotkey", p)
			}
		}
	}
	if vk == 0 {
		return 0, 0, fmt.Errorf("no key in hotkey %q", s)
	}
	return mod, vk, nil
}

// estopKeyToVK maps a single key name to its Windows virtual-key code.
func estopKeyToVK(key string) int {
	if len(key) == 1 {
		c := key[0]
		if c >= 'A' && c <= 'Z' {
			return int(c)
		}
		if c >= 'a' && c <= 'z' {
			return int(c - 32)
		}
		if c >= '0' && c <= '9' {
			return int(c)
		}
	}
	switch strings.ToUpper(key) {
	case "PAUSE", "BREAK":
		return 0x13
	case "ESC", "ESCAPE":
		return 0x1B
	case "SPACE":
		return 0x20
	case "ENTER":
		return 0x0D
	case "TAB":
		return 0x09
	case "F1":
		return 0x70
	case "F2":
		return 0x71
	case "F3":
		return 0x72
	case "F4":
		return 0x73
	case "F5":
		return 0x74
	case "F6":
		return 0x75
	case "F7":
		return 0x76
	case "F8":
		return 0x77
	case "F9":
		return 0x78
	case "F10":
		return 0x79
	case "F11":
		return 0x7A
	case "F12":
		return 0x7B
	}
	return 0
}
