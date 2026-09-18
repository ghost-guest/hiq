//go:build !windows

package builtin

import "github.com/zzycxz/hiq/internal/tool"

// ScreenTools returns the desktop-automation tools available on macOS/Linux.
//
// All tools work cross-platform:
//   - screen_click/type/scroll/key: via cliclick (macOS) / xdotool (Linux)
//   - screen_perceive: VLM-only (screenshot → vision model → coordinates, no UIA)
//
// screenCapture is not included here; it's registered separately in the
// screenshot-hotkey pipeline (capture_darwin.go / capture_linux.go).
func ScreenTools() []tool.Tool {
	tools := baseScreenTools()
	tools = append(tools, screenPerceive{})
	return tools
}
