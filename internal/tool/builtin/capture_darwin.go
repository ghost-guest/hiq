package builtin

// capture_darwin.go implements CaptureFullScreen for macOS using the built-in
// screencapture command. No CGO required — shelling out to the CLI tool that
// ships with every macOS install.
//
// screencapture flags:
//   -x  no sound
//   -C  include cursor
//   -r  capture screen rectangle (not used here, full screen is default)

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
)

// CaptureFullScreen captures the full primary screen on macOS via the built-in
// screencapture command. Returns an image.RGBA compatible with the Windows
// implementation so the screenshot hotkey pipeline works identically.
func CaptureFullScreen() (*image.RGBA, error) {
	// Write to a temp file — screencapture doesn't support stdout on macOS.
	tmpFile := "/tmp/hiq_screenshot.png"
	defer os.Remove(tmpFile)

	cmd := exec.Command("screencapture", "-x", "-C", tmpFile)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("screencapture failed: %v (%s)", err, stderr.String())
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("read screenshot: %w", err)
	}

	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode screenshot PNG: %w", err)
	}

	// Convert to *image.RGBA to match the Windows CaptureFullScreen signature.
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba, nil
	}
	// If the decoded image isn't RGBA (e.g. NRGBA), convert it.
	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			rgba.Set(x, y, img.At(x, y))
		}
	}
	return rgba, nil
}
