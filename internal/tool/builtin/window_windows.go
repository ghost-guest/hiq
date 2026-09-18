//go:build windows

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unsafe"

	"github.com/zzycxz/hiq/internal/tool"

	"golang.org/x/sys/windows"
)

// kernel32 holds the kernel32.dll handle. user32 is already a package var in
// screen_windows.go, but kernel32 isn't, so we declare our own here —
// GetCurrentThreadId (used by focusWindow) lives in kernel32, NOT user32.
var kernel32 = windows.NewLazySystemDLL("kernel32.dll")

// Window management tools for the CUA. These close the biggest gap in "operate a
// desktop like a human": before the agent can reliably click/type into a window,
// it must be able to BRING THAT WINDOW FORWARD, make sure it's not occluded, and
// ensure it's focused (keyboard input goes where you click, but typing goes to
// the focused window). Without these, screen_click lands on whatever happens to
// be on top, and screen_type sends keystrokes to the wrong window — exactly the
// "I clicked but nothing happened / text went elsewhere" failures we saw.
//
// All five tools resolve the target window by a case-insensitive substring match
// on its title (e.g. "notepad" matches "无标题 - 记事本" via class, or "Notepad"),
// mirroring how get_ui_tree reports windows. They're the "set up the workspace"
// step the agent should do right after launching an app and before perceiving it.

var (
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procShowWindowAsync     = user32.NewProc("ShowWindowAsync")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procIsIconic            = user32.NewProc("IsIconic")
	procBringWindowToTop    = user32.NewProc("BringWindowToTop")
	procAttachThreadInput   = user32.NewProc("AttachThreadInput")
	// GetCurrentThreadId lives in KERNEL32, NOT user32 — declaring it on user32
	// crashes with "Failed to find GetCurrentThreadId procedure in user32.dll" at
	// the first Call. This was the panic seen in the field.
	procGetCurrentThreadId  = kernel32.NewProc("GetCurrentThreadId")
	procGetWindowThreadProc = user32.NewProc("GetWindowThreadProcessId")
	procSetFocus            = user32.NewProc("SetFocus")
)

// ShowWindow command constants (Win32 SW_*).
const (
	swRestore      = 9
	swMaximize     = 3
	swMinimize     = 6
	swShowNoActive = 4
)

// HWND_TOPMOST / NOT, and SetWindowPos flags.
const (
	hwndTopmost   = ^uintptr(0) // (HWND)-1
	hwndNoTopmost = ^uintptr(1) // (HWND)-2
	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpNoActivate = 0x0010
	wmClose       = 0x0010
)

// resolveWindow finds the first visible window whose title contains `title`
// (case-insensitive). Returns its hwnd + a human-readable description. Used by
// every window_* tool so they all share one matching rule. Empty title returns an
// error — these tools always need a target.
func resolveWindow(title string) (uintptr, string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return 0, "", fmt.Errorf("title is required (substring of a visible window's title, e.g. \"记事本\" or \"notepad\")")
	}
	wins, err := enumVisibleWindows()
	if err != nil {
		return 0, "", fmt.Errorf("enumerate windows: %w", err)
	}
	lower := strings.ToLower(title)
	for _, w := range wins {
		if strings.Contains(strings.ToLower(w.Title), lower) {
			return w.Hwnd, fmt.Sprintf("%q (%s)", w.Title, w.Class), nil
		}
	}
	// Collect a few titles to help the caller fix a wrong/missing name, instead of
	// a bare "not found". This is the difference between "the tool failed" and "the
	// tool told me what IS there so I can retry correctly".
	have := make([]string, 0, 8)
	for _, w := range wins {
		if w.Title != "" {
			have = append(have, w.Title)
		}
		if len(have) >= 8 {
			break
		}
	}
	return 0, "", fmt.Errorf("no visible window title contains %q; visible titles: %s", title, strings.Join(have, ", "))
}

// focusWindow brings a window to the foreground AND gives it keyboard focus. This
// is the critical precondition for screen_type/screen_key to land in the right
// place. SetForegroundWindow alone often fails silently (Windows restricts it to
// prevent focus-stealing), so we use the AttachThreadInput trick: attach our
// thread's input queue to the target's, which lifts the restriction, then set
// foreground + focus + top. Restores from minimized first.
func focusWindow(hwnd uintptr) error {
	// Restore if minimized — a minimized window can't be interacted with.
	iconic, _, _ := procIsIconic.Call(hwnd)
	if iconic != 0 {
		_, _, _ = procShowWindowAsync.Call(hwnd, swRestore)
	}
	// AttachThreadInput dance: get the thread that owns the target window, attach
	// our input queue to it, then SetForegroundWindow/SetFocus succeed where they'd
	// otherwise be blocked by the foreground-lock. Detach after.
	fgThread, _, _ := procGetWindowThreadProc.Call(hwnd, 0)
	myThread, _, _ := procGetCurrentThreadId.Call()
	if fgThread != 0 && fgThread != myThread {
		_, _, _ = procAttachThreadInput.Call(myThread, fgThread, 1)
		defer procAttachThreadInput.Call(myThread, fgThread, 0)
	}
	_, _, _ = procBringWindowToTop.Call(hwnd)
	r1, _, _ := procSetForegroundWindow.Call(hwnd)
	_, _, _ = procSetFocus.Call(hwnd)
	// SetForegroundWindow returns 0 on failure, but per MSDN the return is
	// unreliable (it can be 0 even on success due to the focus lock). We don't
	// treat a 0 return as a hard error — the attach above should have done the job,
	// and the agent will verify with screen_perceive anyway.
	_ = r1
	return nil
}

// --- tools ---

// windowFocus brings a window to the foreground and focuses it so subsequent
// screen_type / screen_key land in it. Call this right after launching an app and
// before any perceive/act on it.
type windowFocus struct{}

func (windowFocus) Name() string   { return "window_focus" }
func (windowFocus) ReadOnly() bool { return false }
func (windowFocus) Description() string {
	return "Bring a window to the foreground and give it keyboard focus, by a substring of its title. This is the FIRST thing to do after launching an app and before typing/clicking into it: without focus, screen_type/screen_key go to whatever window happens to be active (the #1 cause of 'text didn't appear where I expected'). Also restores the window if minimized. Example: window_focus {\"title\":\"记事本\"}. Verify with screen_perceive after."
}
func (windowFocus) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","description":"substring of the target window's title, case-insensitive (e.g. \"记事本\", \"notepad\", \"保存\")"}},"required":["title"]}`)
}
func (windowFocus) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}

	// Try Python bridge first.
	if DesktopBridgeAvailable() {
		bridgeArgs := map[string]any{"title": p.Title}
		if resp, err := callDesktopBridge(ctx, "focus_window", bridgeArgs); err == nil {
			focused, _ := resp["focused"].(string)
			return fmt.Sprintf("focused window %s [bridge]", focused), nil
		}
	}

	hwnd, desc, err := resolveWindow(p.Title)
	if err != nil {
		return "", err
	}
	if err := focusWindow(hwnd); err != nil {
		return "", err
	}
	return fmt.Sprintf("focused window %s", desc), nil
}

// windowMaximize maximizes a window so its full content is visible and not
// occluded — a reliable workspace state before perceiving/clicking.
type windowMaximize struct{}

func (windowMaximize) Name() string   { return "window_maximize" }
func (windowMaximize) ReadOnly() bool { return false }
func (windowMaximize) Description() string {
	return "Maximize a window (by title substring) so its full content is visible and it's not occluded by other windows. Good to call before screen_perceive so the whole UI is on screen. Example: window_maximize {\"title\":\"记事本\"}."
}
func (windowMaximize) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","description":"substring of the target window's title, case-insensitive"}},"required":["title"]}`)
}
func (windowMaximize) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	hwnd, desc, err := resolveWindow(p.Title)
	if err != nil {
		return "", err
	}
	r, _, _ := procShowWindowAsync.Call(hwnd, swMaximize)
	if r == 0 {
		return "", fmt.Errorf("ShowWindowAsync(SW_MAXIMIZE) failed for %s", desc)
	}
	return fmt.Sprintf("maximized %s", desc), nil
}

// windowRestore un-maximizes / un-minimizes a window to its normal size.
type windowRestore struct{}

func (windowRestore) Name() string   { return "window_restore" }
func (windowRestore) ReadOnly() bool { return false }
func (windowRestore) Description() string {
	return "Restore a window (by title substring) from maximized or minimized state to its normal size. Use when you need a smaller, movable window or to undo a previous window_maximize. Example: window_restore {\"title\":\"记事本\"}."
}
func (windowRestore) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","description":"substring of the target window's title, case-insensitive"}},"required":["title"]}`)
}
func (windowRestore) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	hwnd, desc, err := resolveWindow(p.Title)
	if err != nil {
		return "", err
	}
	r, _, _ := procShowWindowAsync.Call(hwnd, swRestore)
	if r == 0 {
		return "", fmt.Errorf("ShowWindowAsync(SW_RESTORE) failed for %s", desc)
	}
	return fmt.Sprintf("restored %s", desc), nil
}

// windowMove moves/resizes a window to a specific (x, y, w, h). The agent can use
// this to place a window where it knows the coordinates will be (removing all
// ambiguity about where to click), or to reveal a window partially off-screen.
type windowMove struct{}

func (windowMove) Name() string   { return "window_move" }
func (windowMove) ReadOnly() bool { return false }
func (windowMove) Description() string {
	return "Move and/or resize a window to exact coordinates by title substring. Use it to place a window where you KNOW the layout will be (so screen_click coordinates are unambiguous), or to bring an off-screen window fully into view. Pass x,y for the new top-left and w,h for the new size (all in physical screen pixels). Example: window_move {\"title\":\"记事本\",\"x\":0,\"y\":0,\"w\":800,\"h\":600}."
}
func (windowMove) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","description":"substring of the target window's title, case-insensitive"},"x":{"type":"integer","description":"new left edge (screen pixels)"},"y":{"type":"integer","description":"new top edge (screen pixels)"},"w":{"type":"integer","description":"new width (screen pixels)"},"h":{"type":"integer","description":"new height (screen pixels)"}},"required":["title","x","y","w","h"]}`)
}
func (windowMove) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Title string `json:"title"`
		X     int    `json:"x"`
		Y     int    `json:"y"`
		W     int    `json:"w"`
		H     int    `json:"h"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	hwnd, desc, err := resolveWindow(p.Title)
	if err != nil {
		return "", err
	}
	// Restore first if minimized/maximized — SetWindowPos on a maximized window
	// can behave unexpectedly; restoring gives a clean normal state to move.
	iconic, _, _ := procIsIconic.Call(hwnd)
	if iconic != 0 {
		_, _, _ = procShowWindowAsync.Call(hwnd, swRestore)
	}
	r, _, _ := procSetWindowPos.Call(hwnd, 0, uintptr(p.X), uintptr(p.Y), uintptr(p.W), uintptr(p.H), 0)
	if r == 0 {
		return "", fmt.Errorf("SetWindowPos failed for %s", desc)
	}
	return fmt.Sprintf("moved %s to (%d,%d) %dx%d", desc, p.X, p.Y, p.W, p.H), nil
}

// windowClose closes a window cleanly (sends WM_CLOSE, same as clicking the X).
type windowClose struct{}

func (windowClose) Name() string   { return "window_close" }
func (windowClose) ReadOnly() bool { return false }
func (windowClose) Description() string {
	return "Close a window cleanly by title substring (sends WM_CLOSE, the same as clicking the close button). Cleaner than killing a process. Example: window_close {\"title\":\"记事本\"}."
}
func (windowClose) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","description":"substring of the target window's title, case-insensitive"}},"required":["title"]}`)
}
func (windowClose) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}

	// Try Python bridge first.
	if DesktopBridgeAvailable() {
		bridgeArgs := map[string]any{"title": p.Title}
		if resp, err := callDesktopBridge(ctx, "close_window", bridgeArgs); err == nil {
			closed, _ := resp["closed"].(string)
			return fmt.Sprintf("closed window %s [bridge]", closed), nil
		}
	}

	hwnd, desc, err := resolveWindow(p.Title)
	if err != nil {
		return "", err
	}
	r, _, _ := procPostMessageW.Call(hwnd, wmClose, 0, 0)
	if r == 0 {
		return "", fmt.Errorf("PostMessage(WM_CLOSE) failed for %s", desc)
	}
	return fmt.Sprintf("sent close to %s", desc), nil
}

// WindowTools returns the window-management tool set. Windows-only; on other
// platforms returns nil (the tools don't exist there). Registered in cowork mode
// alongside the screen_* tools.
func WindowTools() []tool.Tool {
	return []tool.Tool{
		windowFocus{},
		windowMaximize{},
		windowRestore{},
		windowMove{},
		windowClose{},
	}
}

// keep unsafe referenced (used in the syscalls above).
var _ = unsafe.Pointer(nil)
