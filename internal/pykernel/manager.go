package pykernel

import (
	"context"
	"sync"
	"time"
)

// Manager owns the process-level kernel (one per hiq process for now: the
// desktop app runs one user's sessions, and kernel state is exactly the
// cross-cell memory the paradigm sells). Cell serializes everything.
type Manager struct {
	mu      sync.Mutex
	k       *Kernel
	host    HostCaller
	ttl     time.Duration
	stop    chan struct{}
	stopped sync.Once
	// reaperStarted gates the reap goroutine: it must not exist until the
	// first cell runs, or every test binary that blank-imports the builtin
	// tools fails goleak with a sleeping goroutine nobody uses.
	reaperStarted bool
	onClose       func()
}

// NewManager returns a manager with the given host dispatcher and idle TTL
// (ttl <= 0 uses the default). The reaper goroutine starts lazily with the
// first Cell and stops on Close.
func NewManager(host HostCaller, ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = idleTTL
	}
	return &Manager{host: host, ttl: ttl, stop: make(chan struct{})}
}

var (
	defaultOnce sync.Once
	defaultMgr  *Manager
)

// Default returns the process-wide manager used by the python_cell tool,
// creating it (and its reaper) on first use. The RegistryHost serves
// host.tool(...) from the registry stamped into each cell's ctx, so a shared
// manager needs no per-session wiring.
func Default() *Manager {
	defaultOnce.Do(func() {
		defaultMgr = NewManager(RegistryHost{}, 0)
	})
	return defaultMgr
}

// Close stops the reaper and kills the kernel. Safe to call multiple times.
func (m *Manager) Close() {
	m.stopped.Do(func() { close(m.stop) })
	m.mu.Lock()
	k := m.k
	m.k = nil
	onClose := m.onClose
	m.mu.Unlock()
	if k != nil {
		k.Reset()
	}
	if onClose != nil {
		onClose()
	}
}

// Cell executes one cell on the shared kernel. reset=true kills the worker
// first so the model can start a clean namespace explicitly.
func (m *Manager) Cell(ctx context.Context, code string, timeout time.Duration, reset bool) (Result, error) {
	m.mu.Lock()
	if reset && m.k != nil {
		m.k.Reset()
	}
	if m.k == nil {
		m.k = NewKernel(m.host)
	}
	k := m.k
	if !m.reaperStarted {
		m.reaperStarted = true
		go m.reapLoop()
	}
	m.mu.Unlock()
	return k.Cell(ctx, code, timeout)
}

// Generation reports the current kernel generation (0 = never started).
func (m *Manager) Generation() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.k == nil {
		return 0
	}
	return m.k.Generation()
}

func (m *Manager) reapLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
		m.mu.Lock()
		k := m.k
		m.mu.Unlock()
		if k == nil {
			continue
		}
		k.mu.Lock()
		idle := time.Since(k.lastUse)
		k.mu.Unlock()
		if idle > m.ttl {
			// Idle past the TTL: reap. The next cell respawns a fresh
			// namespace (Generation bumps, and the result reports it).
			m.Close()
		}
	}
}
