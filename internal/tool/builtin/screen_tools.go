// Platform-agnostic screen-action tools: screen_click, screen_type,
// screen_scroll, screen_key. These four tools have IDENTICAL schemas and
// semantics on every platform — only their input backend differs. The backend
// is the six functions declared in input.go (moveMouse / clickMouse / typeText
// / pressKeyCombo / pressKey / scrollWheel), each implemented per-platform in
// screen_windows.go (Win32 SendInput) and input_other.go (xdotool/cliclick).
//
// Perception tools (screen_perceive, screenshot, get_ui_tree) stay
// platform-specific — they're added to the roster by each platform's own
// ScreenTools(), on top of baseScreenTools() defined here.

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/tool"
)

// baseScreenTools returns the four cross-platform action tools. Both the
// Windows ScreenTools (screen_windows.go) and the non-Windows ScreenTools
// (screen_other.go) build on this set, then add their own perception tools.
func baseScreenTools() []tool.Tool {
	return []tool.Tool{
		screenClick{},
		screenType{},
		screenScroll{},
		screenKey{},
	}
}

// --- screen_click -----------------------------------------------------------

type screenClick struct{}

func (screenClick) Name() string { return "screen_click" }

func (screenClick) Description() string {
	return "Click at screen coordinates (x, y). button is left (default)/right/middle; double sends a double-click. Coordinates are in physical screen pixels — get them from a screenshot analysis (image_understand) or get_ui_tree bounding boxes. Move + press + release is synthesized via the platform input backend (SendInput on Windows, xdotool/cliclick on macOS/Linux), so it works on any window the cursor can reach."
}

func (screenClick) Schema() json.RawMessage {
	return json.RawMessage(`{
	"type":"object",
	"properties":{
	  "x":{"type":"integer","description":"Screen X coordinate (pixels)"},
	  "y":{"type":"integer","description":"Screen Y coordinate (pixels)"},
	  "button":{"type":"string","enum":["left","right","middle"],"description":"Mouse button (default left)"},
	  "double":{"type":"boolean","description":"Double-click (default false)"}
	},
	"required":["x","y"]
}`)
}

func (screenClick) ReadOnly() bool { return false }

func (screenClick) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		X      int    `json:"x"`
		Y      int    `json:"y"`
		Button string `json:"button"`
		Double bool   `json:"double"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := moveMouse(p.X, p.Y); err != nil {
		return "", err
	}
	times := 1
	if p.Double {
		times = 2
	}
	for i := 0; i < times; i++ {
		if err := clickMouse(p.Button); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("clicked (%d, %d)%s%s", p.X, p.Y, buttonLabel(p.Button), doubleLabel(p.Double)), nil
}

// --- screen_type ------------------------------------------------------------

type screenType struct{}

func (screenType) Name() string { return "screen_type" }

func (screenType) Description() string {
	return "Type text at the current cursor focus via the platform keyboard backend. The target element must already have focus (click it first with screen_click). On Windows this uses SendInput per-character Unicode key synthesis (KEYEVENTF_UNICODE); on macOS/Linux it pipes UTF-8 text through cliclick/xdotool. Works in any focused field — native apps, browsers, Electron apps — regardless of keyboard layout, including non-ASCII characters. Optional press_enter sends Enter after typing."
}

func (screenType) Schema() json.RawMessage {
	return json.RawMessage(`{
	"type":"object",
	"properties":{
	  "text":{"type":"string","description":"Text to type"},
	  "press_enter":{"type":"boolean","description":"Press Enter after typing (default false)"}
	},
	"required":["text"]
}`)
}

func (screenType) ReadOnly() bool { return false }

func (screenType) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Text       string `json:"text"`
		PressEnter bool   `json:"press_enter"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := typeText(p.Text); err != nil {
		return "", err
	}
	if p.PressEnter {
		if err := pressKey("enter"); err != nil {
			return "", err
		}
	}
	suffix := ""
	if p.PressEnter {
		suffix = " + Enter"
	}
	return fmt.Sprintf("typed %d chars%s", len(p.Text), suffix), nil
}

// --- screen_scroll ----------------------------------------------------------

type screenScroll struct{}

func (screenScroll) Name() string { return "screen_scroll" }

func (screenScroll) Description() string {
	return "Scroll the mouse wheel at (x, y). amount is in notches (one notch ≈ 120 units on Windows); positive scrolls down/forward, negative up/back. Move + wheel synthesized via the platform input backend. Use to reach content below the fold before re-screenshotting."
}

func (screenScroll) Schema() json.RawMessage {
	return json.RawMessage(`{
	"type":"object",
	"properties":{
	  "x":{"type":"integer"},
	  "y":{"type":"integer"},
	  "amount":{"type":"integer","description":"Wheel notches: positive = down/forward, negative = up/back (default 3)"}
	},
	"required":["x","y"]
}`)
}

func (screenScroll) ReadOnly() bool { return false }

func (screenScroll) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		X      int `json:"x"`
		Y      int `json:"y"`
		Amount int `json:"amount"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	amount := p.Amount
	if amount == 0 {
		amount = 3
	}
	if err := moveMouse(p.X, p.Y); err != nil {
		return "", err
	}
	if err := scrollWheel(amount); err != nil {
		return "", err
	}
	dir := "down"
	if amount < 0 {
		dir = "up"
	}
	return fmt.Sprintf("scrolled %s %d notches at (%d, %d)", dir, absInt(amount), p.X, p.Y), nil
}

// --- screen_key -------------------------------------------------------------

// screenKey implements the `screen_key` tool: send a keyboard shortcut (e.g.
// "ctrl+s", "alt+tab", "shift+enter") or a single key (e.g. "enter", "esc",
// "f5") to whatever window currently has keyboard focus. This is the save-PPT
// path (Ctrl+S), the close-dialog path (Esc), the confirm path (Enter) — the
// shortcuts screen_type cannot express (it types text, not modifiers).
type screenKey struct{}

func (screenKey) Name() string { return "screen_key" }

func (screenKey) Description() string {
	return "Send a keyboard shortcut or single key to the focused window. Use for actions screen_type can't do: Ctrl+S (save), Ctrl+A (select all), Ctrl+C/V (copy/paste), Enter (confirm), Esc (cancel/close dialog), Tab, arrow keys, F-keys. The keys string uses '+' to combine a modifier (ctrl/shift/alt) with a key: 'ctrl+s', 'alt+tab', 'shift+arrowleft'. Single keys: 'enter', 'esc', 'tab', 'f5', 'delete', 'backspace'. Keys go to whatever window has focus — call window_focus first to be sure."
}

func (screenKey) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"keys": {"type": "string", "description": "Key combination, e.g. \"ctrl+s\", \"alt+tab\", \"enter\", \"esc\". Modifiers: ctrl, shift, alt. Keys: a-z, 0-9, enter, esc, tab, space, delete, backspace, home, end, pageup, pagedown, arrowup/down/left/right, f1-f12."}
		},
		"required": ["keys"]
	}`)
}

func (screenKey) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Keys string `json:"keys"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	keys := strings.TrimSpace(in.Keys)
	if keys == "" {
		return "", fmt.Errorf("keys is required")
	}
	mod, key, err := parseKeyCombo(keys)
	if err != nil {
		return "", err
	}
	if mod != "" {
		if err := pressKeyCombo(mod, key); err != nil {
			return "", fmt.Errorf("key combo %q failed: %w", keys, err)
		}
	} else {
		if err := pressKey(key); err != nil {
			return "", fmt.Errorf("key %q failed: %w", keys, err)
		}
	}
	time.Sleep(50 * time.Millisecond) // let the app react
	return fmt.Sprintf("Sent key %q.", keys), nil
}

func (screenKey) ReadOnly() bool { return false }

// --- key-combo parser (platform-agnostic) -----------------------------------

// parseKeyCombo parses a key combination string like "ctrl+shift+s" or "enter"
// into a '+'-joined modifier name ("ctrl", "ctrl+shift", "" if none) and a
// single platform-agnostic key name. It accepts both '+' and '-' as separators
// (so "ctrl+s" and "ctrl-s" both work). Supported modifiers: ctrl/control,
// shift, alt. The key name is validated against the platform-agnostic key
// vocabulary (see validKeyName). This is platform-agnostic on purpose: the
// per-platform backends translate the names (parseVK on Windows; osascript /
// xdotool key tokens elsewhere), so parsing happens once, here.
func parseKeyCombo(s string) (modName, keyName string, err error) {
	// Normalize: lowercase + replace dashes with plus signs.
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "-", "+")
	parts := strings.Split(normalized, "+")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return "", "", fmt.Errorf("empty key combo")
	}
	// All parts except the last are modifiers; dedupe + keep order.
	var mods []string
	seen := map[string]bool{}
	for _, p := range parts[:len(parts)-1] {
		p = strings.TrimSpace(p)
		var canon string
		switch p {
		case "ctrl", "control":
			canon = "ctrl"
		case "shift":
			canon = "shift"
		case "alt":
			canon = "alt"
		default:
			return "", "", fmt.Errorf("unknown modifier %q (use ctrl, shift, or alt)", p)
		}
		if !seen[canon] {
			seen[canon] = true
			mods = append(mods, canon)
		}
	}
	keyName = strings.TrimSpace(parts[len(parts)-1])
	if !validKeyName(keyName) {
		return "", "", fmt.Errorf("unknown key %q", keyName)
	}
	return strings.Join(mods, "+"), keyName, nil
}

// validKeyName reports whether name is a recognized platform-agnostic key name.
// This is the shared vocabulary the LLM-facing schema documents: single letters
// a-z, digits 0-9, and the named keys below (with common aliases). Each
// platform's backend maps these to its own representation (VK codes on Windows,
// osascript key codes / xdotool keysyms on macOS/Linux).
func validKeyName(name string) bool {
	if name == "" {
		return false
	}
	if len(name) == 1 {
		c := name[0]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			return true
		}
		return false
	}
	switch name {
	case "enter", "return", "esc", "escape", "tab", "space",
		"delete", "del", "backspace", "home", "end",
		"pageup", "pagedown",
		"arrowup", "up",
		"arrowdown", "down",
		"arrowleft", "left",
		"arrowright", "right",
		"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12":
		return true
	}
	return false
}

// splitModifiers splits a '+'-joined modName ("ctrl+shift") into its parts,
// trimming whitespace and dropping empties. "" → nil. Shared by the
// per-platform pressKeyCombo backends (parseModifierVKs on Windows re-parses
// for VK codes; mac/linux build their key-combo strings from these parts), so
// it lives here in platform-agnostic code.
func splitModifiers(modName string) []string {
	modName = strings.TrimSpace(modName)
	if modName == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(modName, "+") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// --- message helpers (shared by all platforms) ------------------------------

func buttonLabel(b string) string {
	if b == "" || b == "left" {
		return ""
	}
	return " " + b
}

func doubleLabel(d bool) string {
	if d {
		return " (double)"
	}
	return ""
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// screenAttachmentsDir returns the directory for saving screenshot/perceive
// attachments. Platform-agnostic (os.Getwd + filepath.Join).
func screenAttachmentsDir() string {
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, ".hiq", "attachments")
	}
	return filepath.Join(os.TempDir(), "hiq-screen")
}
