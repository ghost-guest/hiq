//go:build !windows

package main

// remote_wsl_other.go — WSL only exists on Windows; other platforms have no P1
// transport (Docker/SSH/Server follow the same remoteTransport interface).

import (
	"context"
	"fmt"
	"io"
)

type wslTransport struct{}

func newWSLTransport() remoteTransport { return wslTransport{} }

// wslDistro mirrors the Windows-side shape. Off Windows there is no distro to
// enumerate, but the type must still exist: the binding generator walks the
// whole App method set on every platform, so a missing type breaks the
// macOS/Linux builds (Wails "Generating bindings" step).
type wslDistro struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Version int    `json:"version"`
	Default bool   `json:"default"`
}

// ListWSLDistros is a stub off Windows.
func (a *App) ListWSLDistros() []wslDistro { return nil }

func (t wslTransport) Dial(ctx context.Context, ref RemoteRef) (io.Reader, io.Writer, remoteProcess, error) {
	return nil, nil, nil, fmt.Errorf("wsl transport is Windows-only")
}

// wslHomeForProbe is a stub off Windows.
func wslHomeForProbe(distro, user string) (string, error) {
	return "", fmt.Errorf("wsl transport is Windows-only")
}
