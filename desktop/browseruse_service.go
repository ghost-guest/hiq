package main

// browseruse_service.go manages the browser-use Python sidecar lifecycle.
// It is a near-clone of he_service.go (Hyper-Extract): start on first use /
// at app boot, stop on app shutdown, expose an IsReady gate so callers don't
// hit a server whose deps are still loading. The sidecar runs an autonomous
// browser-use Agent loop over a browser that the Go host launches separately
// (see internal/browserlaunch).

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/browseruse"
	"github.com/zzycxz/hiq/internal/proc"
	runtimepkg "github.com/zzycxz/hiq/internal/runtime"
)

const (
	defaultBrowserUsePort       = 18901 // distinct from HE's 18900
	browserUseStartupTimeout    = 15 * time.Second
	browserUseHealthCheckPeriod = 60 * time.Second
)

// BrowserUseService manages the browser-use Python sidecar process.
type BrowserUseService struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	client   *browseruse.Client
	port     int
	python   string
	script   string
	running  bool
	buAvail  bool // browser-use library actually imported (not just HTTP up)
	cancelFn context.CancelFunc
}

// NewBrowserUseService creates a new browser-use sidecar service.
func NewBrowserUseService(pythonPath string, scriptPath string, port int) *BrowserUseService {
	if port <= 0 {
		port = defaultBrowserUsePort
	}
	if pythonPath == "" {
		if cmd, _, err := runtimepkg.ResolvePython(); err == nil {
			pythonPath = cmd
		} else {
			pythonPath = "python3"
			if runtime.GOOS == "windows" {
				pythonPath = "python"
			}
		}
	}
	return &BrowserUseService{
		port:   port,
		python: pythonPath,
		script: scriptPath,
		client: browseruse.NewClient(port),
	}
}

// Client returns the sidecar HTTP client.
func (s *BrowserUseService) Client() *browseruse.Client { return s.client }

// Port returns the sidecar port.
func (s *BrowserUseService) Port() int { return s.port }

// Start launches the Python sidecar if not already running.
func (s *BrowserUseService) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}
	if s.script == "" {
		return fmt.Errorf("browser-use script path not configured")
	}
	if _, err := os.Stat(s.script); os.IsNotExist(err) {
		return fmt.Errorf("browser-use script not found: %s", s.script)
	}
	// Probe the port for an early, clear EADDRINUSE error (mirrors HE).
	if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port)); err == nil {
		_ = ln.Close()
	} else {
		return fmt.Errorf("browser-use port %d already in use (set [cowork] browser_use_port to a free port): %w", s.port, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.cancelFn = cancel

	s.cmd = exec.CommandContext(ctx, s.python, s.script,
		"--port", fmt.Sprintf("%d", s.port),
		"--host", "127.0.0.1",
	)
	proc.HideWindow(s.cmd)
	s.cmd.Stdout = os.Stdout
	s.cmd.Stderr = os.Stderr
	// NOTE: cmd.Env is intentionally NOT set — the child inherits the host's
	// environment, which by boot time contains every LLM API key (loadDotEnv
	// ran). This is the same credential-flow pattern the HE server relies on.

	if err := s.cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start browser-use sidecar: %w", err)
	}
	s.running = true
	slog.Info("browser-use sidecar starting", "pid", s.cmd.Process.Pid, "port", s.port)

	go s.waitForReady()
	go s.monitor()
	return nil
}

// waitForReady polls /health until the sidecar responds and reports whether
// the browser-use library actually imported. IsReady reflects buAvail so
// callers don't 500 against a server whose Python deps are missing.
func (s *BrowserUseService) waitForReady() {
	ctx, cancel := context.WithTimeout(context.Background(), browserUseStartupTimeout)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			slog.Warn("browser-use sidecar did not become ready in time")
			return
		default:
			report, err := s.client.Health(ctx)
			if err == nil && report.OK {
				s.mu.Lock()
				s.buAvail = report.BrowserUseAvail
				s.mu.Unlock()
				if report.BrowserUseAvail {
					slog.Info("browser-use sidecar ready (library available)")
				} else {
					slog.Warn("browser-use HTTP server is up but the library is NOT available — ensure browser-use (and a provider client) are installed")
				}
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
}

// monitor watches the process and logs if it exits.
func (s *BrowserUseService) monitor() {
	if s.cmd == nil {
		return
	}
	err := s.cmd.Wait()
	s.mu.Lock()
	s.running = false
	s.buAvail = false
	s.mu.Unlock()
	if err != nil {
		slog.Error("browser-use sidecar exited", "err", err)
	} else {
		slog.Info("browser-use sidecar stopped")
	}
}

// Stop kills the Python sidecar.
func (s *BrowserUseService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelFn != nil {
		s.cancelFn()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.running = false
}

// IsRunning reports whether the sidecar HTTP process is up.
func (s *BrowserUseService) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// IsReady reports whether the sidecar is up AND the browser-use library
// imported successfully. Callers (browser_auto) should gate on this.
func (s *BrowserUseService) IsReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running && s.buAvail
}

// FindBrowserUseScript locates browseruse_server.py. The script lives at the
// repo root (next to the other Python scripts: hyper_extract_server.py,
// doc_converter.py). Resolution order:
//  1. Beside the executable / 1-2 levels up (installed layout: build/bin → root).
//  2. Up from the executable until a go.mod is found, then check there (handles
//     `wails dev`, which builds the exe into a system temp dir far from the repo).
//  3. The current working directory and its parents (go run / dev with cwd set).
func FindBrowserUseScript() string {
	const name = "browseruse_server.py"
	// 1. exe-relative candidates (installed build layout).
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, c := range []string{
			filepath.Join(dir, name),
			filepath.Join(dir, "..", name),
			filepath.Join(dir, "..", "..", name),
		} {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
		// 2. Walk up from the exe to the repo root (a dir containing go.mod),
		//    then look there. This is what makes `wails dev` (exe in %TEMP%)
		//    find the script at the repo root.
		if root := findRepoRootFrom(dir); root != "" {
			if c := filepath.Join(root, name); fileExists(c) {
				return c
			}
		}
	}
	// 3. cwd and its parents (go run from the repo root, or dev shells).
	cwd, err := os.Getwd()
	if err == nil {
		for d := cwd; d != ""; d = parentDir(d) {
			if c := filepath.Join(d, name); fileExists(c) {
				return c
			}
		}
	}
	return ""
}

// findRepoRootFrom walks up from start until it finds a directory containing
// go.mod (hiq's module marker), returning that directory or "".
func findRepoRootFrom(start string) string {
	for d := start; d != ""; d = parentDir(d) {
		if fileExists(filepath.Join(d, "go.mod")) {
			return d
		}
	}
	return ""
}

// parentDir returns the parent of dir, or "" at the filesystem root.
func parentDir(dir string) string {
	p := filepath.Dir(dir)
	if p == dir {
		return ""
	}
	return p
}
