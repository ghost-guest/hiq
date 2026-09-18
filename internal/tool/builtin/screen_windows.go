//go:build windows

package builtin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/zzycxz/hiq/internal/tool"

	"golang.org/x/sys/windows"
)

// Desktop automation tools (Phase 2 of coWork) — Windows-native implementation.
// These drive the user's actual desktop: screen capture (Win32 BitBlt), mouse
// and keyboard input (Win32 SendInput), for the screenshot→VLM→action loop that
// underpins desktop automation. Unlike browser automation (which has the DOM and
// accessibility tree), the desktop only exposes pixels + a UIA tree, so the VLM
// is the primary perception channel here, with get_ui_tree (separate file) as
// the precise-location assist.
//
// Why Windows-native instead of robotgo: the app is Windows-only in practice,
// and robotgo's CGO toolchain is fragile on Windows. These tools use only
// syscall into user32/gdi32 — no CGO, so the build never breaks on a missing C
// compiler. go-ole (already an indirect dep) handles the UIA COM calls.
//
// The Win32 procs are loaded once via NewLazySystemDLL (the codebase's existing
// pattern, see internal/sysproxy). Call signatures follow Microsoft's docs.

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	// GDI32 / USER32 — screen capture via BitBlt.
	procGetDC              = user32.NewProc("GetDC")
	procReleaseDC          = user32.NewProc("ReleaseDC")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBM = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procBitBlt             = gdi32.NewProc("BitBlt")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procGetDIBits          = gdi32.NewProc("GetDIBits")

	// USER32 — input synthesis via SendInput, plus screen geometry + cursor.
	procSendInput    = user32.NewProc("SendInput")
	procGetSystemSM  = user32.NewProc("GetSystemMetrics")
	procSetCursorPos = user32.NewProc("SetCursorPos")
)

// setDPIAware makes the process DPI-aware so screen coordinates and BitBlt
// captures operate in physical pixels (not virtualized logical pixels). Without
// this, GetSystemMetrics returns scaled dimensions and screenshots are blurry on
// high-DPI displays, and click coordinates drift. Mirrors Rooster's
// interaction.py:38-55 (shcore.SetProcessDpiAwareness → SetProcessDPIAware fallback).
//
// Called once at package init; safe to call multiple times (subsequent calls are
// no-ops). This affects the WHOLE process, so the Wails frontend's own DPI
// handling coexists (the frontend is a separate rendering concern).
func init() {
	// Try shcore.SetProcessDpiAwareness (Win 8.1+). Value 1 =
	// PROCESS_SYSTEM_DPI_AWARE — coordinates are in physical pixels.
	if shcore := windows.NewLazySystemDLL("shcore.dll"); shcore.Load() == nil {
		if proc := shcore.NewProc("SetProcessDpiAwareness"); proc.Find() == nil {
			if _, _, err := proc.Call(1); err == nil || err == windows.Errno(0) {
				return
			}
		}
	}
	// Fallback: user32.SetProcessDPIAware (Vista+).
	if proc := user32.NewProc("SetProcessDPIAware"); proc.Find() == nil {
		proc.Call()
	}
}

const (
	smCXScreen = 0 // GetSystemMetrics: screen width
	smCYScreen = 1 // GetSystemMetrics: screen height
	srccopy    = 0x00CC0020

	inputMouse    uint32 = 0
	inputKeyboard uint32 = 1

	mouseeventfLeftDown   uint32 = 0x0002
	mouseeventfLeftUp     uint32 = 0x0004
	mouseeventfRightDown  uint32 = 0x0008
	mouseeventfRightUp    uint32 = 0x0010
	mouseeventfMiddleDown uint32 = 0x0020
	mouseeventfMiddleUp   uint32 = 0x0040
	mouseeventfWheel      uint32 = 0x0800

	keyeventfKeyUp   uint32 = 0x0002
	keyeventfUnicode uint32 = 0x0004
)

// bitmapInfoHeader for GetDIBits pixel extraction (BGRA, top-down).
type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// ScreenTools returns the Windows desktop-automation tools: the cross-platform
// base set (screen_click/type/scroll/key from screen_tools.go) plus the
// Windows-native perception tools — screenshot (Win32 BitBlt), get_ui_tree
// (UIA), and screen_perceive (UIA + VLM fusion). On macOS/Linux the
// non-Windows ScreenTools (screen_other.go) returns the base set plus its own
// VLM-only perceive; this Windows build excludes that one.
func ScreenTools() []tool.Tool {
	tools := baseScreenTools()
	tools = append(tools,
		screenCapture{},
		getUITreeEnhanced{},
		screenPerceive{},
	)
	return tools
}

// --- screenshot -------------------------------------------------------------

type screenCapture struct{}

func (screenCapture) Name() string { return "screenshot" }

func (screenCapture) Description() string {
	return "Capture the current screen (or a region) as a PNG and return its file path plus a base64 thumbnail. The image is ready to pass to image_understand for visual analysis, or use the path as an attachment. This is the primary perception channel for desktop automation — take a screenshot, have image_understand describe what's on screen, decide the next action. Optional region {x,y,w,h} captures a sub-rectangle; default is the full primary screen."
}

func (screenCapture) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "region":{"type":"object","description":"Optional sub-rectangle to capture. Omit for the full screen.","properties":{"x":{"type":"integer"},"y":{"type":"integer"},"w":{"type":"integer"},"h":{"type":"integer"}}}
},
"required":[]
}`)
}

func (screenCapture) ReadOnly() bool { return true }

func (screenCapture) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Region *struct {
			X int `json:"x"`
			Y int `json:"y"`
			W int `json:"w"`
			H int `json:"h"`
		} `json:"region"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
	}
	var rx, ry, rw, rh int
	hasRegion := p.Region != nil
	if hasRegion {
		rx, ry, rw, rh = p.Region.X, p.Region.Y, p.Region.W, p.Region.H
	}
	img, err := captureScreen(hasRegion, rx, ry, rw, rh)
	if err != nil {
		return "", err
	}
	dir := screenAttachmentsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create attachments dir: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("screen-%d.png", time.Now().Unix()))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", fmt.Errorf("encode png: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return "", fmt.Errorf("write screenshot: %w", err)
	}
	thumb := base64.StdEncoding.EncodeToString(buf.Bytes())
	if len(thumb) > 4096 {
		thumb = thumb[:4096] + "…"
	}
	return fmt.Sprintf("screenshot saved: %s (%dx%d)\nbase64 (first 4k): %s", path, img.Bounds().Dx(), img.Bounds().Dy(), thumb), nil
}

// --- Win32 capture ----------------------------------------------------------

// CaptureFullScreen grabs the full primary screen as an image.RGBA. Exported
// wrapper around captureScreen for use by the desktop layer's screenshot-hotkey
// feature (which needs the raw pixels to PNG-encode for the VLM).
func CaptureFullScreen() (*image.RGBA, error) {
	return captureScreen(false, 0, 0, 0, 0)
}

// captureScreen grabs the full primary screen (or a region) via BitBlt into a
// Go RGBA image. Flow: GetDC(NULL) → compatible DC + bitmap → BitBlt → GetDIBits
// (BGRA pixels) → assemble image.RGBA. All GDI handles released.
func captureScreen(hasRegion bool, rx, ry, rw, rh int) (*image.RGBA, error) {
	screenW, err := systemMetrics(smCXScreen)
	if err != nil {
		return nil, err
	}
	screenH, err := systemMetrics(smCYScreen)
	if err != nil {
		return nil, err
	}
	x, y, w, h := 0, 0, screenW, screenH
	if hasRegion && rw > 0 && rh > 0 {
		x, y, w, h = rx, ry, rw, rh
	}

	hdc, _, callErr := procGetDC.Call(0)
	if hdc == 0 {
		return nil, fmt.Errorf("GetDC failed: %w", callErr)
	}
	defer procReleaseDC.Call(0, hdc)

	memDC, _, err := procCreateCompatibleDC.Call(hdc)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed: %w", err)
	}
	defer procDeleteDC.Call(memDC)

	hBmp, _, err := procCreateCompatibleBM.Call(hdc, uintptr(w), uintptr(h))
	if hBmp == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed: %w", err)
	}
	defer procDeleteObject.Call(hBmp)

	oldObj, _, _ := procSelectObject.Call(memDC, hBmp)
	defer procSelectObject.Call(memDC, oldObj)

	ok, _, err := procBitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h), hdc, uintptr(x), uintptr(y), uintptr(srccopy))
	if ok == 0 {
		return nil, fmt.Errorf("BitBlt failed: %w", err)
	}

	// GetDIBits: top-down BGRA. Negative height → row 0 at top.
	bi := bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(w),
		Height:      int32(-h),
		Planes:      1,
		BitCount:    32,
		Compression: 0, // BI_RGB
	}
	buf := make([]byte, w*h*4)
	procGetDIBits.Call(memDC, hBmp, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0)

	// BGRA → RGBA.
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			off := (row*w + col) * 4
			b, g, r := buf[off], buf[off+1], buf[off+2]
			// image.RGBA expects [R,G,B,A].
			img.Pix[off] = r
			img.Pix[off+1] = g
			img.Pix[off+2] = b
			img.Pix[off+3] = 255
		}
	}
	return img, nil
}

// systemMetrics wraps GetSystemMetrics.
func systemMetrics(index int) (int, error) {
	v, _, err := procGetSystemSM.Call(uintptr(index))
	if v == 0 {
		return 0, fmt.Errorf("GetSystemMetrics(%d): %w", index, err)
	}
	return int(int32(v)), nil
}

// --- Win32 input ------------------------------------------------------------

// mouseInput matches Win32 MOUSEINPUT.
type mouseInput struct {
	Type      uint32
	DX        int32
	DY        int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	Extra     uintptr
}

// keyboardInput matches Win32 KEYBDINPUT.
type keyboardInput struct {
	Type  uint32
	Vk    uint16
	Scan  uint16
	Flags uint32
	Time  uint32
	Extra uintptr
}

// moveMouse moves the cursor to (x,y) via SetCursorPos (physical pixels), with
// ±1px random jitter to mimic human imprecision. Anti-bot behavioral detection
// flags pixel-perfect clicks as synthetic; the jitter is cheap insurance. Mirrors
// Rooster interaction.py:129-138.
func moveMouse(x, y int) error {
	x += randInt(-1, 1)
	y += randInt(-1, 1)
	r, _, err := procSetCursorPos.Call(uintptr(x), uintptr(y))
	if r == 0 {
		return fmt.Errorf("SetCursorPos(%d, %d): %w", x, y, err)
	}
	return nil
}

// clickMouse presses + releases a button via SendInput, with a randomized hold
// duration (40-90ms) to mimic human click timing. Fixed timing is a bot
// fingerprint; humans vary. Mirrors Rooster interaction.py:131-138.
func clickMouse(button string) error {
	var down, up uint32
	switch button {
	case "", "left":
		down, up = mouseeventfLeftDown, mouseeventfLeftUp
	case "right":
		down, up = mouseeventfRightDown, mouseeventfRightUp
	case "middle":
		down, up = mouseeventfMiddleDown, mouseeventfMiddleUp
	default:
		return fmt.Errorf("unknown button %q", button)
	}
	if err := sendMouseEvent(down); err != nil {
		return err
	}
	time.Sleep(time.Duration(40+randInt(0, 50)) * time.Millisecond)
	return sendMouseEvent(up)
}

// scrollWheel sends mouse-wheel delta. WHEEL_DELTA=120; amount notches → units.
func scrollWheel(amount int) error {
	const wheelDelta = 120
	mi := mouseInput{
		Type:      inputMouse,
		MouseData: uint32(int32(amount) * wheelDelta),
		Flags:     mouseeventfWheel,
	}
	return sendInput(inputMouse, unsafe.Pointer(&mi), int(unsafe.Sizeof(mi)))
}

func sendMouseEvent(flags uint32) error {
	mi := mouseInput{Type: inputMouse, Flags: flags}
	return sendInput(inputMouse, unsafe.Pointer(&mi), int(unsafe.Sizeof(mi)))
}

// typeText types text via SendInput. For >5 characters it uses the clipboard +
// Ctrl+V (faster, handles CJK/emoji reliably, avoids per-key timing issues);
// for short text it uses per-character Unicode key synthesis. Mirrors
// Rooster/UI-TARS-desktop's clipboard fallback for long input.
func typeText(text string) error {
	if utf8RuneCount(text) > 5 {
		return typeViaClipboard(text)
	}
	return typeViaUnicode(text)
}

func typeViaUnicode(text string) error {
	for _, r := range text {
		if err := typeRune(r); err != nil {
			return err
		}
		time.Sleep(8 * time.Millisecond)
	}
	return nil
}

func typeRune(r rune) error {
	ki := keyboardInput{Type: inputKeyboard, Scan: uint16(r), Flags: keyeventfUnicode}
	if err := sendInput(inputKeyboard, unsafe.Pointer(&ki), int(unsafe.Sizeof(ki))); err != nil {
		return err
	}
	ki.Flags = keyeventfUnicode | keyeventfKeyUp
	return sendInput(inputKeyboard, unsafe.Pointer(&ki), int(unsafe.Sizeof(ki)))
}

// typeViaClipboard writes text to the clipboard then sends Ctrl+V. Faster for
// long text and handles CJK/emoji that Unicode scan codes can't (BMP >0xFFFF).
// Mirrors Rooster interaction.py:234 + UI-TARS-desktop operator.ts:88-104.
func typeViaClipboard(text string) error {
	if err := setClipboardText(text); err != nil {
		// Clipboard failed — fall back to per-character (may lose emoji).
		return typeViaUnicode(text)
	}
	// Ctrl down → V down → V up → Ctrl up. Names are translated to VK codes
	// inside pressKeyCombo (see input contract in input.go).
	if err := pressKeyCombo("ctrl", "v"); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	return nil
}

// pressKeyCombo presses one or more modifier keys + a key simultaneously, then
// releases. modName is a '+'-joined list of platform-agnostic modifier names
// ("ctrl", "ctrl+shift", ""); keyName is a single platform-agnostic key name
// ("s", "f5", "enter"). The names are translated to Windows VK codes here.
func pressKeyCombo(modName, keyName string) error {
	keyVK, err := parseVK(keyName)
	if err != nil {
		return err
	}
	modVKs, err := parseModifierVKs(modName)
	if err != nil {
		return err
	}
	// Modifiers down (in declared order).
	for _, vk := range modVKs {
		mi := keyboardInput{Type: inputKeyboard, Vk: vk}
		if err := sendInput(inputKeyboard, unsafe.Pointer(&mi), int(unsafe.Sizeof(mi))); err != nil {
			return err
		}
	}
	// Key down.
	ki := keyboardInput{Type: inputKeyboard, Vk: keyVK}
	if err := sendInput(inputKeyboard, unsafe.Pointer(&ki), int(unsafe.Sizeof(ki))); err != nil {
		return err
	}
	time.Sleep(30 * time.Millisecond)
	// Key up.
	ki.Flags = keyeventfKeyUp
	if err := sendInput(inputKeyboard, unsafe.Pointer(&ki), int(unsafe.Sizeof(ki))); err != nil {
		return err
	}
	// Modifiers up (reverse order, matching typical human release).
	for i := len(modVKs) - 1; i >= 0; i-- {
		mi := keyboardInput{Type: inputKeyboard, Vk: modVKs[i], Flags: keyeventfKeyUp}
		if err := sendInput(inputKeyboard, unsafe.Pointer(&mi), int(unsafe.Sizeof(mi))); err != nil {
			return err
		}
	}
	return nil
}

// parseModifierVKs splits a '+'-joined modifier string ("ctrl+shift") into VK
// codes, deduplicating. Empty string → no modifiers. Mirrors the modifier
// handling that used to live inline in the VK-returning parseKeyCombo.
func parseModifierVKs(modName string) ([]uint16, error) {
	seen := map[uint16]bool{}
	var vks []uint16
	for _, p := range splitModifiers(modName) {
		// splitModifiers doesn't lowercase; normalize here for safety. The
		// platform-agnostic parseKeyCombo already lowercases, but callers may
		// invoke pressKeyCombo directly (e.g. typeViaClipboard's "ctrl","v").
		switch strings.ToLower(p) {
		case "ctrl", "control":
			if !seen[0x11] {
				seen[0x11] = true
				vks = append(vks, 0x11)
			}
		case "shift":
			if !seen[0x10] {
				seen[0x10] = true
				vks = append(vks, 0x10)
			}
		case "alt":
			if !seen[0x12] {
				seen[0x12] = true
				vks = append(vks, 0x12)
			}
		default:
			return nil, fmt.Errorf("unknown modifier %q (use ctrl, shift, or alt)", p)
		}
	}
	return vks, nil
}

// setClipboardText puts text on the Windows clipboard via user32 (OpenClipboard
// → EmptyClipboard → SetClipboardData(CF_UNICODETEXT) → CloseClipboard).
var (
	procOpenClipboard  = user32.NewProc("OpenClipboard")
	procEmptyClipboard = user32.NewProc("EmptyClipboard")
	procSetClipData    = user32.NewProc("SetClipboardData")
	procCloseClipboard = user32.NewProc("CloseClipboard")
	procGlobalAlloc    = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalAlloc")
	procGlobalLock     = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalLock")
	procGlobalUnlock   = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalUnlock")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

func setClipboardText(text string) error {
	// Convert to UTF-16 (Windows native).
	utf16, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	byteLen := len(utf16) * 2

	// Allocate global memory for the clipboard data.
	hMem, _, err := procGlobalAlloc.Call(gmemMoveable, uintptr(byteLen))
	if hMem == 0 {
		return fmt.Errorf("GlobalAlloc: %w", err)
	}
	ptr, _, err := procGlobalLock.Call(hMem)
	if ptr == 0 {
		return fmt.Errorf("GlobalLock: %w", err)
	}
	// Copy UTF-16 bytes into the global memory.
	for i, v := range utf16 {
		*(*uint16)(unsafe.Pointer(ptr + uintptr(i*2))) = v
	}
	procGlobalUnlock.Call(hMem)

	// Set clipboard data.
	if r, _, err := procOpenClipboard.Call(0); r == 0 {
		return fmt.Errorf("OpenClipboard: %w", err)
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	r, _, _ := procSetClipData.Call(cfUnicodeText, hMem)
	if r == 0 {
		return fmt.Errorf("SetClipboardData failed")
	}
	return nil
}

// pressKey presses + releases a single key given its platform-agnostic name
// ("enter", "esc", "f5", "a", ...). The name is translated to a VK code here.
func pressKey(keyName string) error {
	vk, err := parseVK(keyName)
	if err != nil {
		return err
	}
	ki := keyboardInput{Type: inputKeyboard, Vk: vk}
	if err := sendInput(inputKeyboard, unsafe.Pointer(&ki), int(unsafe.Sizeof(ki))); err != nil {
		return err
	}
	time.Sleep(12 * time.Millisecond)
	ki.Flags = keyeventfKeyUp
	return sendInput(inputKeyboard, unsafe.Pointer(&ki), int(unsafe.Sizeof(ki)))
}

// sendInput wraps the Win32 SendInput call for a single INPUT record. The INPUT
// struct is { DWORD type; union{MOUSEINPUT;KEYBDINPUT;HARDWAREINPUT} }, 40 bytes
// on x64. We build it from the typed struct by copying its bytes into a 40-byte
// buffer and setting the type prefix.
func sendInput(inputType uint32, data unsafe.Pointer, size int) error {
	const inputSize = 40
	in := make([]byte, inputSize)
	copy(in, (*[40]byte)(data)[:size])
	*(*uint32)(unsafe.Pointer(&in[0])) = inputType
	sent, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&in[0])), inputSize)
	if sent == 0 {
		return fmt.Errorf("SendInput failed: %w", err)
	}
	return nil
}

// randInt returns a random int in [min, max]. Used for human-like jitter/timing
// in the Windows input backends (moveMouse / clickMouse).
func randInt(min, max int) int {
	if max <= min {
		return min
	}
	return min + rand.Intn(max-min+1)
}

// utf8RuneCount counts runes in a string (for the clipboard-vs-unicode decision
// in typeText).
func utf8RuneCount(s string) int {
	return len([]rune(s))
}

// parseVK maps a platform-agnostic key name to its Windows virtual-key code.
// Used by pressKey / pressKeyCombo (Windows input backend) to translate the
// string key names produced by the platform-agnostic parseKeyCombo.
func parseVK(name string) (uint16, error) {
	if len(name) == 1 {
		c := name[0]
		if c >= 'a' && c <= 'z' {
			return uint16(c - 'a' + 0x41), nil // 'a'=0x41 ... 'z'=0x5A
		}
		if c >= '0' && c <= '9' {
			return uint16(c - '0' + 0x30), nil // '0'=0x30 ... '9'=0x39
		}
	}
	switch name {
	case "enter", "return":
		return 0x0D, nil
	case "esc", "escape":
		return 0x1B, nil
	case "tab":
		return 0x09, nil
	case "space":
		return 0x20, nil
	case "delete", "del":
		return 0x2E, nil
	case "backspace":
		return 0x08, nil
	case "home":
		return 0x24, nil
	case "end":
		return 0x23, nil
	case "pageup":
		return 0x21, nil
	case "pagedown":
		return 0x22, nil
	case "arrowup", "up":
		return 0x26, nil
	case "arrowdown", "down":
		return 0x28, nil
	case "arrowleft", "left":
		return 0x25, nil
	case "arrowright", "right":
		return 0x27, nil
	case "f1":
		return 0x70, nil
	case "f2":
		return 0x71, nil
	case "f3":
		return 0x72, nil
	case "f4":
		return 0x73, nil
	case "f5":
		return 0x74, nil
	case "f6":
		return 0x75, nil
	case "f7":
		return 0x76, nil
	case "f8":
		return 0x77, nil
	case "f9":
		return 0x78, nil
	case "f10":
		return 0x79, nil
	case "f11":
		return 0x7A, nil
	case "f12":
		return 0x7B, nil
	}
	return 0, fmt.Errorf("unknown key %q", name)
}
