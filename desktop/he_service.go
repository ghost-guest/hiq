package main

// he_service.go manages the Hyper-Extract Python server lifecycle.
// It starts the server on first use and stops it on app shutdown.

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

	"github.com/zzycxz/hiq/internal/proc"
	"github.com/zzycxz/hiq/internal/rag"
	runtimepkg "github.com/zzycxz/hiq/internal/runtime"
)

const (
	defaultHEPort       = 18900
	heStartupTimeout    = 10 * time.Second
	heHealthCheckPeriod = 30 * time.Second
)

// HEService manages the Hyper-Extract Python server process.
type HEService struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	client   *rag.HEClient
	port     int
	python   string
	script   string
	running  bool
	heAvail  bool // Hyper-Extract library actually loaded (not just HTTP up)
	cancelFn context.CancelFunc
}

// NewHEService creates a new Hyper-Extract service.
func NewHEService(pythonPath string, scriptPath string, port int) *HEService {
	if port <= 0 {
		port = defaultHEPort
	}
	if pythonPath == "" {
		// Use the unified runtime resolver (prefers uv if bundled).
		if cmd, _, err := runtimepkg.ResolvePython(); err == nil {
			pythonPath = cmd
		} else {
			pythonPath = "python3"
			if runtime.GOOS == "windows" {
				pythonPath = "python"
			}
		}
	}
	return &HEService{
		port:   port,
		python: pythonPath,
		script: scriptPath,
		client: rag.NewHEClient(port),
	}
}

// Client returns the Hyper-Extract HTTP client.
func (s *HEService) Client() *rag.HEClient {
	return s.client
}

// Start launches the Python server if not already running.
func (s *HEService) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}
	if s.script == "" {
		return fmt.Errorf("Hyper-Extract script path not configured")
	}
	// Check if script exists.
	if _, err := os.Stat(s.script); os.IsNotExist(err) {
		return fmt.Errorf("Hyper-Extract script not found: %s", s.script)
	}
	// Probe the port: if something is already listening (an orphaned HE process
	// from a prior crash, or another service), report it clearly instead of
	// letting the Python subprocess fail with an opaque EADDRINUSE. The user can
	// override the port via [cowork] he_port to sidestep the conflict.
	if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port)); err == nil {
		_ = ln.Close()
	} else {
		return fmt.Errorf("Hyper-Extract port %d already in use (set [cowork] he_port to a free port): %w", s.port, err)
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

	if err := s.cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start Hyper-Extract: %w", err)
	}
	s.running = true
	slog.Info("Hyper-Extract server starting", "pid", s.cmd.Process.Pid, "port", s.port)

	// Wait for server to be ready.
	go s.waitForReady()
	go s.monitor()
	return nil
}

// waitForReady polls the health endpoint until the server is ready. It captures
// he_available so IsRunning() can distinguish "Python HTTP server is up" from
// "Hyper-Extract library actually loaded and usable" — the latter is what
// callers (extract/summarize/embed) truly need.
func (s *HEService) waitForReady() {
	ctx, cancel := context.WithTimeout(context.Background(), heStartupTimeout)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			slog.Warn("Hyper-Extract server did not become ready in time")
			return
		default:
			ok, heAvail, err := s.client.Health(ctx)
			if err == nil && ok {
				s.mu.Lock()
				s.heAvail = heAvail
				s.mu.Unlock()
				if heAvail {
					slog.Info("Hyper-Extract server ready (library available)")
				} else {
					slog.Warn("Hyper-Extract HTTP server is up but the Python library is NOT available — extract/summarize/embed will fail. Ensure langchain_core + hyperextract are installed and ~/.he/config.toml exists.")
				}
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
}

// monitor watches the process and logs if it exits.
func (s *HEService) monitor() {
	if s.cmd == nil {
		return
	}
	err := s.cmd.Wait()
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
	if err != nil {
		slog.Error("Hyper-Extract server exited", "err", err)
	} else {
		slog.Info("Hyper-Extract server stopped")
	}
}

// Stop kills the Python server.
func (s *HEService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelFn != nil {
		s.cancelFn()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	s.running = false
}

// Port returns the server port.
func (s *HEService) Port() int { return s.port }

// IsRunning returns whether the Python HTTP server process is up. This does
// NOT mean Hyper-Extract is usable — use IsReady for that.
func (s *HEService) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// IsReady returns whether the server is up AND the Hyper-Extract library is
// actually loaded (he_available=true from /health). Callers that need real HE
// capabilities (extract / summarize / embed) should check this, not IsRunning —
// otherwise they'll get a 500 from a server whose Python deps are missing.
func (s *HEService) IsReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running && s.heAvail
}

// FindScript locates the hyper_extract_server.py script.
func FindScript() string {
	// Check relative to the executable.
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		candidates := []string{
			filepath.Join(dir, "hyper_extract_server.py"),
			filepath.Join(dir, "..", "hyper_extract_server.py"),
			filepath.Join(dir, "..", "..", "hyper_extract_server.py"),
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	// Check current working directory.
	if _, err := os.Stat("hyper_extract_server.py"); err == nil {
		return "hyper_extract_server.py"
	}
	return ""
}
