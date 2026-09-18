package runtime

// runtime.go provides unified runtime environment resolution for Hiq.
// It replaces the 6 scattered findPython()/pythonExe() helpers with a single
// source of truth that knows about uv (bundled or PATH), Python (direct or via
// uv), and Node.js.
//
// Resolution priority (borrowed from internal/codegraph's Resolve pattern):
//
//   uv:    config override → PATH lookup → exe-adjacent bundle → (download)
//   python: uv (`uv run python`) → python3/python/py on PATH
//   node:  node/npx on PATH (no bundle; Node is too large to bundle)
//
// The package is safe for concurrent use after init. All functions are
// idempotent and cache their first result.

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// BundleDirName is the subdirectory next to the hiq executable where
// bundled runtimes live (e.g. hiq.exe/runtimes/uv.exe).
const BundleDirName = "runtimes"

// uvNames returns the candidate filenames for uv on each platform.
func uvNames() []string {
	if runtime.GOOS == "windows" {
		return []string{"uv.exe", "uv.cmd", "uv.bat"}
	}
	return []string{"uv"}
}

// pythonNames returns the candidate command names for Python.
func pythonNames() []string {
	if runtime.GOOS == "windows" {
		return []string{"python", "py", "python3"}
	}
	return []string{"python3", "python"}
}

// nodeNames returns the candidate command names for Node.js.
func nodeNames() []string {
	return []string{"node"}
}

func npxNames() []string {
	if runtime.GOOS == "windows" {
		return []string{"npx", "npx.cmd"}
	}
	return []string{"npx"}
}

// --- cached results ---------------------------------------------------------

var (
	uvOnce  sync.Once
	uvPath  string
	uvFound bool

	pyOnce   sync.Once
	pyCmd    string
	pyPrefix []string
	pyErr    error

	nodeOnce  sync.Once
	nodePath  string
	npxPath   string
	nodeFound bool
)

// ResolveUV finds uv on the system. Checks PATH first, then the exe-adjacent
// bundle directory. Returns the absolute path and true if found.
func ResolveUV() (string, bool) {
	uvOnce.Do(func() {
		// 1. PATH lookup
		if p, err := exec.LookPath(uvNames()[0]); err == nil {
			uvPath, uvFound = p, true
			return
		}
		// 2. Cache (auto-downloaded by Install)
		if p, ok := cachedUV(); ok {
			uvPath, uvFound = p, true
			return
		}
		// 3. Bundle (exe-adjacent)
		if p, ok := bundledUV(); ok {
			uvPath, uvFound = p, true
			return
		}
		// 4. Windows: check common install dirs
		uvPath, uvFound = "", false
	})
	return uvPath, uvFound
}

// ResolvePython finds Python. Priority: uv (`uv run python`) → python3/python/py
// on PATH. When uv is used, returns ("uv", ["run", "python"]) so the caller
// can prepend the prefix args. When a direct Python is found, returns
// ("/usr/bin/python3", nil).
func ResolvePython() (cmd string, prefixArgs []string, err error) {
	pyOnce.Do(func() {
		// 1. If uv is available, prefer it (handles deps + venv isolation).
		if uvPath, ok := ResolveUV(); ok {
			pyCmd, pyPrefix, pyErr = uvPath, []string{"run", "python"}, nil
			return
		}
		// 2. Direct Python on PATH.
		for _, name := range pythonNames() {
			if p, err := exec.LookPath(name); err == nil {
				pyCmd, pyPrefix, pyErr = p, nil, nil
				return
			}
		}
		pyCmd, pyPrefix, pyErr = "", nil, errPythonNotFound
	})
	return pyCmd, pyPrefix, pyErr
}

// ResolveNode finds Node.js and npx on PATH. Returns absolute paths.
// Node is NOT bundled (too large); the caller should prompt the user to install
// if not found.
func ResolveNode() (nodePath, npxPath string, ok bool) {
	nodeOnce.Do(func() {
		for _, name := range nodeNames() {
			if p, err := exec.LookPath(name); err == nil {
				nodePath = p
				break
			}
		}
		for _, name := range npxNames() {
			if p, err := exec.LookPath(name); err == nil {
				npxPath = p
				break
			}
		}
		nodeFound = nodePath != "" || npxPath != ""
	})
	return nodePath, npxPath, nodeFound
}

// --- status for doctor/UI ---------------------------------------------------

// RuntimeStatus is a snapshot of all runtime environments, for the doctor
// command and the settings UI.
type RuntimeStatus struct {
	UV     RuntimeEntry `json:"uv"`
	Python RuntimeEntry `json:"python"`
	Node   RuntimeEntry `json:"node"`
	NPX    RuntimeEntry `json:"npx"`
}

// RuntimeEntry describes one runtime's availability.
type RuntimeEntry struct {
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Source    string `json:"source,omitempty"` // "path" | "bundle" | "uv" | ""
}

// DetectAll checks all runtimes and returns a status snapshot. Safe to call
// repeatedly (each sub-resolver caches its first result).
func DetectAll() RuntimeStatus {
	st := RuntimeStatus{}

	if p, ok := ResolveUV(); ok {
		st.UV = RuntimeEntry{Available: true, Path: p, Source: uvSource(p)}
	}

	if cmd, prefix, err := ResolvePython(); err == nil {
		pyPath := cmd
		if len(prefix) > 0 {
			pyPath = cmd + " " + filepath.Join(prefix...) // "uv run python"
		}
		source := "path"
		if len(prefix) > 0 {
			source = "uv"
		}
		st.Python = RuntimeEntry{Available: true, Path: pyPath, Source: source}
	}

	if np, nx, ok := ResolveNode(); ok {
		if np != "" {
			st.Node = RuntimeEntry{Available: true, Path: np, Source: "path"}
		}
		if nx != "" {
			st.NPX = RuntimeEntry{Available: true, Path: nx, Source: "path"}
		}
	}

	return st
}

// uvSource determines where uv was found ("path" or "bundle").
func uvSource(p string) string {
	if base, ok := bundledBaseDir(); ok && filepath.Dir(p) == base {
		return "bundle"
	}
	return "path"
}

// --- errors -----------------------------------------------------------------

var errPythonNotFound = &pythonNotFoundError{}

type pythonNotFoundError struct{}

func (*pythonNotFoundError) Error() string {
	return "python not found on PATH and uv is not available; install Python 3.10+ or uv (https://docs.astral.sh/uv/)"
}
