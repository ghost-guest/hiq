package pykernel

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/runtime"
)

// TestKernelBundledInterpreter pins the kernel to an interpreter supplied via
// HIQ_PYKERNEL_BUNDLE_PYTHON (CI sets it to the embeddable distribution under
// runtimes/python/) and asserts the full cell path works there — the same
// interpreter a fresh unzip uses on a machine without any Python install.
func TestKernelBundledInterpreter(t *testing.T) {
	py := os.Getenv("HIQ_PYKERNEL_BUNDLE_PYTHON")
	if py == "" {
		t.Skip("HIQ_PYKERNEL_BUNDLE_PYTHON not set")
	}
	if _, err := os.Stat(py); err != nil {
		t.Fatalf("bundled interpreter missing: %v", err)
	}
	orig := resolvePython
	resolvePython = func() (string, []string, error) { return py, nil, nil }
	t.Cleanup(func() { resolvePython = orig })

	k := NewKernel(RegistryHost{})
	defer k.Reset()
	if _, err := k.Cell(context.Background(), "bundle_mark = 41 + 1", time.Minute); err != nil {
		t.Fatalf("cell: %v", err)
	}
	res, err := k.Cell(context.Background(), "print(bundle_mark * 10)", time.Minute)
	if err != nil {
		t.Fatalf("second cell: %v", err)
	}
	if res.Error != "" || !strings.Contains(res.Stdout, "420") {
		t.Fatalf("persistence through bundle interpreter broken: err=%q stdout=%q", res.Error, res.Stdout)
	}
}

// requirePython skips the e2e suite when no interpreter is available (CI legs
// and portable installs without Python still run the pure-Go tests).
func requirePython(t *testing.T) {
	t.Helper()
	if _, _, err := runtime.ResolvePython(); err != nil {
		t.Skipf("python not available: %v", err)
	}
}

func TestKernelPersistenceAcrossCells(t *testing.T) {
	requirePython(t)
	k := NewKernel(nil)
	defer k.Reset()
	ctx := context.Background()

	res, err := k.Cell(ctx, "flowers = ['lily', 'rose', 'tulip']\nprint(len(flower_lookup := {f: i for i, f in enumerate(flowers)}))", time.Minute)
	if err != nil {
		t.Fatalf("cell 1: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("cell 1 error: %s", res.Error)
	}
	if !strings.Contains(res.Stdout, "3") {
		t.Fatalf("cell 1 stdout = %q, want 3", res.Stdout)
	}

	// Cell 2 sees cell 1's namespace — the whole point of the paradigm.
	res, err = k.Cell(ctx, "print(sorted(flowers)[0]); print(flower_lookup['rose'])", time.Minute)
	if err != nil {
		t.Fatalf("cell 2: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("cell 2 error: %s", res.Error)
	}
	if !strings.Contains(res.Stdout, "lily") || !strings.Contains(res.Stdout, "1") {
		t.Fatalf("cell 2 stdout = %q, want lily/1 (state did not persist)", res.Stdout)
	}
	if k.Generation() != 1 {
		t.Fatalf("generation = %d, want 1 (no respawn)", k.Generation())
	}
}

func TestKernelCapturesPrintAndErrors(t *testing.T) {
	requirePython(t)
	k := NewKernel(nil)
	defer k.Reset()

	res, err := k.Cell(context.Background(), "import sys\nprint('to stdout')\nprint('to stderr', file=sys.stderr)\nraise ValueError('boom')", time.Minute)
	if err != nil {
		t.Fatalf("cell: %v", err)
	}
	if res.Ok() {
		t.Fatalf("expected error result")
	}
	if !strings.Contains(res.Stdout, "to stdout") {
		t.Errorf("stdout = %q, want captured print", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "to stderr") {
		t.Errorf("stderr = %q, want captured print", res.Stderr)
	}
	if !strings.Contains(res.Error, "ValueError") || !strings.Contains(res.Error, "boom") {
		t.Errorf("error = %q, want traceback with ValueError", res.Error)
	}
}

func TestKernelSubmitOutputContract(t *testing.T) {
	requirePython(t)
	k := NewKernel(nil)
	defer k.Reset()

	res, err := k.Cell(context.Background(), "hiq.submit_output(summary='done', count=2)\nprint('after submit')", time.Minute)
	if err != nil {
		t.Fatalf("cell: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("error: %s", res.Error)
	}
	if res.Completion == nil || res.Completion["summary"] != "done" || res.Completion["count"] != float64(2) {
		t.Fatalf("completion = %v, want summary/count", res.Completion)
	}

	// A second submit is terminal-contract violation: RuntimeError inside the cell.
	res, err = k.Cell(context.Background(), "hiq.submit_output(summary='again')", time.Minute)
	if err != nil {
		t.Fatalf("cell 2: %v", err)
	}
	if res.Ok() || !strings.Contains(res.Error, "submit_output") {
		t.Fatalf("second submit: ok=%v error=%q, want RuntimeError", res.Ok(), res.Error)
	}
}

type fakeHost struct {
	calls int
	fail  error
	value any
}

func (f *fakeHost) HostCall(ctx context.Context, method string, args map[string]any) (any, error) {
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	if f.value != nil {
		return f.value, nil
	}
	return map[string]any{"echo": args, "method": method}, nil
}

func TestKernelMidCellHostRPC(t *testing.T) {
	requirePython(t)
	host := &fakeHost{}
	k := NewKernel(host)
	defer k.Reset()

	code := "x = hiq.tool('grep', {'pattern': 'TODO'})\nprint(x['method'], x['echo']['args']['pattern'])"
	res, err := k.Cell(context.Background(), code, time.Minute)
	if err != nil {
		t.Fatalf("cell: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("error: %s", res.Error)
	}
	if host.calls != 1 {
		t.Fatalf("host calls = %d, want 1", host.calls)
	}
	if !strings.Contains(res.Stdout, "tool TODO") {
		t.Fatalf("stdout = %q, want host round-trip echo", res.Stdout)
	}

	// Host error surfaces as a Python-side RuntimeError the cell can catch.
	host2 := &fakeHost{fail: errors.New("denied")}
	k2 := NewKernel(host2)
	defer k2.Reset()
	res, err = k2.Cell(context.Background(), "try:\n    hiq.tool('x', {})\nexcept RuntimeError as e:\n    print('caught:', e)", time.Minute)
	if err != nil {
		t.Fatalf("cell: %v", err)
	}
	if !strings.Contains(res.Stdout, "caught: hiq.tool failed: denied") {
		t.Fatalf("stdout = %q, want caught host error", res.Stdout)
	}
}

func TestKernelTimeoutKillsAndBumpsGeneration(t *testing.T) {
	requirePython(t)
	k := NewKernel(nil)
	defer k.Reset()

	_, err := k.Cell(context.Background(), "import time\ntime.sleep(30)", 1500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want timeout", err)
	}
	gen := k.Generation()
	if gen != 1 {
		t.Fatalf("generation = %d, want 1", gen)
	}
	// The next cell transparently respawns a fresh namespace.
	res, err := k.Cell(context.Background(), "print('fresh')", time.Minute)
	if err != nil {
		t.Fatalf("respawn cell: %v", err)
	}
	if !strings.Contains(res.Stdout, "fresh") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if k.Generation() != gen+1 {
		t.Fatalf("generation = %d, want %d after respawn", k.Generation(), gen+1)
	}
}

func TestKernelStrayFDWritesDoNotCorruptProtocol(t *testing.T) {
	requirePython(t)
	k := NewKernel(nil)
	defer k.Reset()

	// os.write(1, ...) bypasses Python-level redirection; the dup2 trick must
	// route it to diagnostics, keeping the protocol line clean.
	code := "import os\nos.write(1, b'STRAY-FD-WRITE')\nprint('legit')"
	res, err := k.Cell(context.Background(), code, time.Minute)
	if err != nil {
		t.Fatalf("cell: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("error: %s", res.Error)
	}
	if strings.Contains(res.Stdout, "STRAY-FD-WRITE") || strings.Contains(res.Stdout, "CHILD-NOISE") {
		t.Fatalf("fd-level writes leaked into the cell result: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "legit") {
		t.Fatalf("stdout = %q, want 'legit'", res.Stdout)
	}
}

func TestManagerResetAndCells(t *testing.T) {
	requirePython(t)
	m := NewManager(RegistryHost{}, 0)
	defer m.Close()
	if m.Generation() != 0 {
		t.Fatalf("fresh manager generation = %d, want 0", m.Generation())
	}
	if _, err := m.Cell(context.Background(), "v = 1", time.Minute, false); err != nil {
		t.Fatalf("cell: %v", err)
	}
	if m.Generation() != 1 {
		t.Fatalf("generation = %d, want 1", m.Generation())
	}
	// reset=true must drop the namespace: `del v` raises NameError in the
	// fresh kernel (a Go-level error would mean the transport broke).
	res, err := m.Cell(context.Background(), "del v", time.Minute, true)
	if err != nil {
		t.Fatalf("cell after reset: %v", err)
	}
	if res.Ok() || !strings.Contains(res.Error, "NameError") {
		t.Fatalf("cell after reset: ok=%v error=%q, want NameError", res.Ok(), res.Error)
	}
	if m.Generation() != 2 {
		t.Fatalf("generation = %d, want 2 after reset", m.Generation())
	}
}

func TestMarshalToolArgs(t *testing.T) {
	if got, err := marshalToolArgs(nil); err != nil || string(got) != "{}" {
		t.Fatalf("nil -> %s, %v", got, err)
	}
	if got, err := marshalToolArgs(`{"a":1}`); err != nil || string(got) != `{"a":1}` {
		t.Fatalf("string -> %s, %v", got, err)
	}
	if _, err := marshalToolArgs("not json"); err == nil {
		t.Fatal("invalid JSON string should error")
	}
	got, err := marshalToolArgs(map[string]any{"q": "x"})
	if err != nil || string(got) != `{"q":"x"}` {
		t.Fatalf("map -> %s, %v", got, err)
	}
}

func TestRegistryHostUnknownMethod(t *testing.T) {
	if _, err := (RegistryHost{}).HostCall(context.Background(), "bash", nil); err == nil {
		t.Fatal("unknown method should error")
	}
}
