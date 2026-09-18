package main

import (
	"log/slog"
	"path/filepath"

	"github.com/zzycxz/hiq/internal/plugin"
)

// sharedPluginHost is a reference-counted plugin.Host shared across desktop tabs
// that share the same workspace root. Multiple controllers (one per tab) use the
// same Host so MCP subprocesses (CodeGraph, etc.) are spawned once per project,
// not once per tab — without this, a user opening N tabs on the same project
// spawns N codegraph daemons, exhausting memory and file handles.
//
// Ported from DeepSeek-Reasonix (PR #4793). The refcount lets a tab close and
// rebuild its controller (model switch, effort change) without tearing down
// subprocesses other tabs still depend on: the rebuild acquires first, the old
// controller's Close releases second, so the count never hits zero mid-switch.
type sharedPluginHost struct {
	host *plugin.Host
	refs int
}

// acquireSharedHost returns a shared *plugin.Host for the given workspace root.
// The first call creates the host; subsequent calls increment a refcount and
// return the same host. The caller MUST call releaseSharedHost when the tab (or
// rebuilt controller) no longer needs the host. An empty root (the global tab)
// gets its own host keyed by "" so it never collides with project tabs.
func (a *App) acquireSharedHost(root string) *plugin.Host {
	root = normalizeSharedHostRoot(root)
	a.sharedHostsMu.Lock()
	defer a.sharedHostsMu.Unlock()

	if a.sharedHosts == nil {
		a.sharedHosts = make(map[string]*sharedPluginHost)
	}

	if entry, ok := a.sharedHosts[root]; ok {
		entry.refs++
		slog.Debug("shared host acquired (reused)", "root", root, "refs", entry.refs)
		return entry.host
	}

	host := plugin.NewHost()
	a.sharedHosts[root] = &sharedPluginHost{host: host, refs: 1}
	slog.Debug("shared host acquired (new)", "root", root)
	return host
}

// lookupSharedHost returns an existing shared host for the given root, or nil.
// Unlike acquireSharedHost, it does NOT increment the refcount — use this when
// rebuilding a controller for an existing tab that already holds a reference.
func (a *App) lookupSharedHost(root string) *plugin.Host {
	root = normalizeSharedHostRoot(root)
	a.sharedHostsMu.Lock()
	defer a.sharedHostsMu.Unlock()
	if entry, ok := a.sharedHosts[root]; ok {
		return entry.host
	}
	return nil
}

// releaseSharedHost drops one reference to the shared host for root. When the
// count reaches zero the host is closed (its subprocesses torn down) and the
// entry is dropped, so a project whose last tab closes frees its MCP servers.
// Releasing a root with no live entry is a no-op.
func (a *App) releaseSharedHost(root string) {
	root = normalizeSharedHostRoot(root)
	a.sharedHostsMu.Lock()
	entry, ok := a.sharedHosts[root]
	if !ok {
		a.sharedHostsMu.Unlock()
		return
	}
	entry.refs--
	refs := entry.refs
	if refs <= 0 {
		delete(a.sharedHosts, root)
	}
	a.sharedHostsMu.Unlock()

	if refs <= 0 {
		slog.Debug("shared host released (closed)", "root", root)
		entry.host.Close()
	} else {
		slog.Debug("shared host released (kept)", "root", root, "refs", refs)
	}
}

// normalizeSharedHostRoot canonicalizes the workspace root key so different
// spellings of the same directory (trailing slash, relative vs absolute) collapse
// to one host. An empty root stays empty (the global tab gets its own host).
func normalizeSharedHostRoot(root string) string {
	root = filepath.Clean(root)
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return root
}
