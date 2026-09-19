package runtime

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindBundledPythonLayouts(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("bundle layout ships only with the Windows desktop release")
	}
	base := t.TempDir()
	if _, ok := findBundledPython(base); ok {
		t.Fatal("empty base must not resolve a bundled python")
	}

	// Flat layout: runtimes/python/python.exe.
	flat := filepath.Join(base, "python")
	if err := os.MkdirAll(flat, 0o755); err != nil {
		t.Fatal(err)
	}
	py := filepath.Join(flat, "python.exe")
	if err := os.WriteFile(py, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := findBundledPython(base)
	if !ok || got != py {
		t.Fatalf("flat layout: got %q ok=%v, want %q", got, ok, py)
	}

	// Nested layout wins nothing once flat matched, but must be found when
	// flat is absent: move the stub into python/bin/.
	if err := os.Remove(py); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(flat, "bin")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	py2 := filepath.Join(nested, "python.exe")
	if err := os.WriteFile(py2, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok = findBundledPython(base)
	if !ok || got != py2 {
		t.Fatalf("nested layout: got %q ok=%v, want %q", got, ok, py2)
	}
}
