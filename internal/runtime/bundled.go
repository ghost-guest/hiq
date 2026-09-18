package runtime

// bundled.go finds runtimes bundled alongside the hiq executable.
// Mirrors internal/codegraph's bundledBaseDir() pattern: look in the directory
// containing the executable for a <BundleDirName>/ subdirectory.

import (
	"os"
	"path/filepath"
	"runtime"
)

// bundledBaseDir returns the absolute path to the bundle directory next to the
// hiq executable (e.g. /opt/hiq/runtimes or C:\...\hiq\runtimes).
// Returns ("", false) if the executable path can't be determined.
func bundledBaseDir() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Join(filepath.Dir(exe), BundleDirName), true
}

// isExec reports whether path exists and is executable (or has .exe/.cmd/.bat
// extension on Windows).
func isExec(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return true
}

// bundledUV looks for uv in the exe-adjacent bundle directory.
func bundledUV() (string, bool) {
	base, ok := bundledBaseDir()
	if !ok {
		return "", false
	}
	for _, name := range uvNames() {
		// Check both flat (runtimes/uv.exe) and nested (runtimes/bin/uv.exe).
		for _, p := range []string{
			filepath.Join(base, name),
			filepath.Join(base, "bin", name),
		} {
			if isExec(p) {
				return p, true
			}
		}
	}
	return "", false
}

// lookPathInDir checks if a command exists in a specific directory (used for
// Windows common install locations that aren't on PATH for GUI processes).
func lookPathInDir(dir, name string) (string, bool) {
	if runtime.GOOS == "windows" {
		for _, ext := range []string{".exe", ".cmd", ".bat"} {
			p := filepath.Join(dir, name+ext)
			if isExec(p) {
				return p, true
			}
		}
	} else {
		p := filepath.Join(dir, name)
		if isExec(p) {
			return p, true
		}
	}
	return "", false
}
