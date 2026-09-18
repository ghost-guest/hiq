package builtin

// capture_linux.go implements CaptureFullScreen for Linux by trying multiple
// screenshot tools in order of preference. No CGO required.
//
// Supported tools (tried in order):
//   1. scrot        — lightweight, X11, widely available
//   2. gnome-screenshot — GNOME desktop default
//   3. grim         — Wayland compositor screenshot tool
//
// If none are available, returns an error with installation instructions.

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
)

// screenshotTool represents a Linux screenshot command and its arguments.
type screenshotTool struct {
	name string
	args []string
}

// linuxScreenshotTools is the ordered list of tools to try.
var linuxScreenshotTools = []screenshotTool{
	{"scrot", []string{"-o"}},            // -o overwrite output file
	{"gnome-screenshot", []string{"-f"}}, // -f output file
	{"grim", []string{}},                 // Wayland, no extra flags
}

// CaptureFullScreen captures the full primary screen on Linux. Tries scrot,
// gnome-screenshot, and grim in order. Returns an image.RGBA compatible with
// the Windows implementation.
func CaptureFullScreen() (*image.RGBA, error) {
	tmpFile := "/tmp/hiq_screenshot.png"
	defer os.Remove(tmpFile)

	var lastErr error
	for _, tool := range linuxScreenshotTools {
		if _, err := exec.LookPath(tool.name); err != nil {
			continue // tool not installed, try next
		}

		args := make([]string, len(tool.args))
		copy(args, tool.args)
		args = append(args, tmpFile)

		cmd := exec.Command(tool.name, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			lastErr = fmt.Errorf("%s failed: %v (%s)", tool.name, err, stderr.String())
			continue // tool errored, try next
		}

		data, err := os.ReadFile(tmpFile)
		if err != nil {
			lastErr = fmt.Errorf("read screenshot: %w", err)
			continue
		}

		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			lastErr = fmt.Errorf("decode screenshot PNG: %w", err)
			continue
		}

		if rgba, ok := img.(*image.RGBA); ok {
			return rgba, nil
		}
		bounds := img.Bounds()
		rgba := image.NewRGBA(bounds)
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				rgba.Set(x, y, img.At(x, y))
			}
		}
		return rgba, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("no screenshot tool worked: %w (install scrot, gnome-screenshot, or grim)", lastErr)
	}
	return nil, fmt.Errorf("no screenshot tool found (install scrot, gnome-screenshot, or grim)")
}
