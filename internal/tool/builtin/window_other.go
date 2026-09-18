//go:build !windows

package builtin

import "github.com/zzycxz/hiq/internal/tool"

// WindowTools returns the window-management tool set. On non-Windows platforms
// there's no Win32 window API to drive, so it returns nil — cowork mode simply
// registers no window_* tools there, matching how ScreenTools behaves. The
// Windows build (window_windows.go) provides the real implementations.
func WindowTools() []tool.Tool { return nil }
