package pykernel

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestKernelSubprocessOutputDoesNotTouchProtocol runs a cell that spawns a
// child process: the child inherits the worker's redirected fd1 (diagnostics),
// never the protocol line. This is the realistic loop shape — cells shell out
// to ffmpeg, compilers, whatever — and it must neither hang the protocol nor
// leak child noise into the model-visible output.
func TestKernelSubprocessOutputDoesNotTouchProtocol(t *testing.T) {
	requirePython(t)
	k := NewKernel(nil)
	defer k.Reset()

	code := "import subprocess, sys, json\n" +
		"r = subprocess.run([sys.executable, '-c', \"print('CHILD-NOISE')\"], capture_output=True, text=True, timeout=30)\n" +
		"print('child rc', r.returncode)\n" +
		"print('captured:', r.stdout.strip())"
	res, err := k.Cell(context.Background(), code, 2*time.Minute)
	if err != nil {
		t.Fatalf("cell: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("error: %s", res.Error)
	}
	if !strings.Contains(res.Stdout, "child rc 0") || !strings.Contains(res.Stdout, "captured: CHILD-NOISE") {
		t.Fatalf("stdout = %q", res.Stdout)
	}

	// A second cell proves the protocol survived the child process.
	res, err = k.Cell(context.Background(), "print('alive-after-subprocess')", time.Minute)
	if err != nil {
		t.Fatalf("cell 2: %v", err)
	}
	if !strings.Contains(res.Stdout, "alive-after-subprocess") {
		t.Fatalf("protocol corrupted after subprocess: %q", res.Stdout)
	}
}

// TestKernelInheritedStdoutChild covers the un-captured variant: the child
// writes straight to the inherited fd1. With the dup2 re-point that lands in
// diagnostics; the protocol line stays clean and the cell result contains only
// the cell's own prints.
func TestKernelInheritedStdoutChild(t *testing.T) {
	requirePython(t)
	k := NewKernel(nil)
	defer k.Reset()

	code := "import subprocess, sys\n" +
		"subprocess.run([sys.executable, '-c', \"print('INHERITED-NOISE')\"], timeout=30)\n" +
		"print('cell-own-output')"
	res, err := k.Cell(context.Background(), code, 2*time.Minute)
	if err != nil {
		t.Fatalf("cell: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("error: %s", res.Error)
	}
	if strings.Contains(res.Stdout, "INHERITED-NOISE") {
		t.Fatalf("child noise leaked into cell output: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "cell-own-output") {
		t.Fatalf("stdout = %q, want cell-own-output", res.Stdout)
	}
}
