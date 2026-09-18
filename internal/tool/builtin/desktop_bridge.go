//go:build windows

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	rt "github.com/zzycxz/hiq/internal/runtime"
)

// desktop_bridge.go — Unified Python bridge for all desktop interactions.
//
// Go's SendInput doesn't work with Win11 Notepad (WinUI 3 / DirectComposition).
// Python's pyautogui + pyperclip handle it correctly. This file provides a
// single Go→Python bridge layer that all desktop tools can call.
//
// Protocol: python desktop_bridge.py <action> --args '{"key":"val"}'
// Response: {"ok": true, ...} or {"ok": false, "error": "..."}

var (
	bridgePython string
	bridgeScript string
	bridgeOnce   sync.Once
	bridgeReady  bool
)

// initBridge finds Python and the bridge script once. Called by both
// DesktopBridgeAvailable and callDesktopBridge.
func initBridge() {
	bridgeOnce.Do(func() {
		// Use runtime.ResolvePython for unified uv/PATH resolution.
		if cmd, _, err := rt.ResolvePython(); err == nil {
			bridgePython = cmd
		}
		bridgeScript = findBridgeScript()
		bridgeReady = bridgePython != "" && bridgeScript != ""
		fmt.Printf("[bridge] python=%q script=%q ready=%v\n", bridgePython, bridgeScript, bridgeReady)
	})
}

// callDesktopBridge calls the Python bridge script with the given action and args.
// ctx is the turn context: cancelling it (Stop) kills the Python subprocess
// promptly via exec.CommandContext instead of waiting out the 10s timeout.
// Returns the full JSON response as a map, or an error.
func callDesktopBridge(ctx context.Context, action string, args map[string]any) (map[string]any, error) {
	initBridge()

	if !bridgeReady {
		return nil, fmt.Errorf("desktop bridge not available (python=%q script=%q)", bridgePython, bridgeScript)
	}

	// Marshal args to JSON.
	argsJSON := "{}"
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("marshal args: %w", err)
		}
		argsJSON = string(b)
	}

	// Layer a 10s ceiling on top of the turn ctx so a hung bridge can't block
	// forever even when the turn isn't cancelled. Turn-cancel propagates: the
	// subprocess is killed as soon as ctx is cancelled.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bridgePython, bridgeScript, action, "--args", argsJSON)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("bridge %s: %s", action, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("bridge %s: %w", action, err)
	}

	// Parse response.
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("bridge %s: parse response: %w", action, err)
	}

	if ok, _ := resp["ok"].(bool); !ok {
		errMsg, _ := resp["error"].(string)
		return nil, fmt.Errorf("bridge %s: %s", action, errMsg)
	}

	return resp, nil
}

// DesktopBridgeAvailable checks if the Python desktop bridge is usable.
func DesktopBridgeAvailable() bool {
	initBridge()
	return bridgeReady
}

// findBridgeScript locates scripts/desktop_bridge.py.
func findBridgeScript() string {
	// Try relative to executable.
	if exe, err := os.Executable(); err == nil {
		dir := exeDir(exe)
		for _, p := range []string{
			filepathJoin(dir, "scripts", "desktop_bridge.py"),
			filepathJoin(dir, "..", "scripts", "desktop_bridge.py"),
			filepathJoin(dir, "..", "hiq", "scripts", "desktop_bridge.py"),
		} {
			if fileExists(p) {
				return p
			}
		}
	}
	// Try CWD and common subdirectories.
	for _, p := range []string{
		"scripts/desktop_bridge.py",
		"hiq/scripts/desktop_bridge.py",
		"../scripts/desktop_bridge.py",
		"../hiq/scripts/desktop_bridge.py",
	} {
		if fileExists(p) {
			return p
		}
	}
	// Try relative to this source file (dev mode).
	_, src, _, ok := runtime.Caller(0)
	if ok {
		dir := exeDir(src) // internal/tool/builtin
		p := filepathJoin(dir, "..", "..", "..", "scripts", "desktop_bridge.py")
		if abs, err := filepath.Abs(p); err == nil && fileExists(abs) {
			return abs
		}
	}
	return ""
}

func exeDir(exe string) string {
	for i := len(exe) - 1; i >= 0; i-- {
		if exe[i] == '\\' || exe[i] == '/' {
			return exe[:i]
		}
	}
	return "."
}

func filepathJoin(parts ...string) string {
	return strings.Join(parts, string(os.PathSeparator))
}

// fileExists reports whether path names an existing regular file. Used by
// findBridgeScript to probe candidate script locations without surfacing an
// error for each miss.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
