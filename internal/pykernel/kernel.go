// Package pykernel implements the persistent Python kernel behind the
// python_cell tool: the Code-as-Action runtime ported from
// PKU-YuanGroup/OpenAI4S (MIT). The model's action space is a persistent
// Python namespace — one code cell replaces many JSON tool round-trips for
// data-heavy work, and a cheap orchestrating model can lean on the kernel the
// way Claude Science leans on its compute plane.
//
// Protocol: JSON-per-line over the child's stdin (in) and stdout (out). The
// worker dups its original stdout at startup and re-points fd1 at the
// diagnostics pipe, so stray fd-level writes and cell subprocess output can
// never corrupt the protocol line (see worker.py). R channels, mid-cell GPU
// dispatch and OS-sandbox adapters from upstream are intentionally not ported.
//
// Locking: Kernel.mu is held by Cell for the entire cell lifetime (spawn,
// write, read, host-call serving). The in-flight cell read runs on a separate
// goroutine but always under the umbrella of Cell's lock — helper methods
// named *Locked must only be called while Cell owns the mutex (directly or by
// being the cell goroutine itself).
package pykernel

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	hiruntime "github.com/zzycxz/hiq/internal/runtime"
)

//go:embed worker.py
var workerScript string

const (
	// frameMax caps one protocol line; a worker gone rogue cannot allocate
	// the host out of memory with a single frame.
	frameMax = 8 << 20
	// defaultCellTimeout bounds one cell when the caller passes none. Cells
	// are meant to be substantial (that is the paradigm), so the budget is
	// generous compared to a shell command.
	defaultCellTimeout = 5 * time.Minute
	// diagKeep keeps the last diagnostics bytes (fd2) for error reports.
	diagKeep = 8 * 1024
	// idleTTL reaps an idle kernel; the next cell transparently respawns a
	// fresh namespace.
	idleTTL = 30 * time.Minute
)

// HostCaller serves mid-cell host.* RPCs. The python_cell tool wires a
// dispatcher that routes hiq.tool(name, args) to read-only built-ins through
// the session's tool registry; submit_output is handled by the worker itself.
type HostCaller interface {
	HostCall(ctx context.Context, method string, args map[string]any) (any, error)
}

// Result is one executed cell.
type Result struct {
	Stdout     string         `json:"stdout"`
	Stderr     string         `json:"stderr"`
	Error      string         `json:"error,omitempty"`
	Completion map[string]any `json:"completion,omitempty"`
	DurationMs int64          `json:"duration_ms"`
	// Generation increments every time the worker process is started. A
	// result from a higher generation means state was lost — compaction never
	// restarts the kernel, but timeouts, kills and resets do.
	Generation int  `json:"generation"`
	Restarted  bool `json:"restarted,omitempty"`
}

// diagBuffer is a bounded ring for the worker's fd2 diagnostics stream.
type diagBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (d *diagBuffer) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf = append(d.buf, p...)
	if len(d.buf) > diagKeep {
		d.buf = d.buf[len(d.buf)-diagKeep:]
	}
	return len(p), nil
}

func (d *diagBuffer) snapshot() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(d.buf)
}

// Kernel is one worker process with its persistent namespace.
type Kernel struct {
	mu sync.Mutex
	// killMu serializes killLocked: the cell goroutine (read error / ctx
	// done) and Cell's timeout path can both reach it concurrently, and the
	// outer mu is held by Cell either way, so re-entrancy needs its own lock.
	killMu  sync.Mutex
	host    HostCaller
	proc    *exec.Cmd
	stdin   io.WriteCloser
	out     *bufio.Reader
	diag    *diagBuffer
	gen     int
	lastUse time.Time
}

// NewKernel returns an unstarted kernel. host may be nil (host.* calls then
// fail with a clear error; the kernel itself still runs).
func NewKernel(host HostCaller) *Kernel {
	return &Kernel{host: host, lastUse: time.Now()}
}

// Cell executes one code cell, respawning the worker if needed. ctx
// cancellation or timeout kills the process; the namespace is lost and
// Generation bumps on the next call.
func (k *Kernel) Cell(ctx context.Context, code string, timeout time.Duration) (Result, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.lastUse = time.Now()
	if timeout <= 0 {
		timeout = defaultCellTimeout
	}
	restarted := k.alive() == false && k.gen > 0
	if err := k.ensureSpawned(); err != nil {
		return Result{}, err
	}
	cellCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	id := k.gen*1_000_000 + int(time.Now().UnixNano()%1_000_000)
	if err := k.writeFrameLocked(map[string]any{"id": id, "type": "exec", "code": code}); err != nil {
		// Worker died between cells; respawn once and retry the write.
		if spawnErr := k.spawnLocked(); spawnErr != nil {
			return Result{}, spawnErr
		}
		restarted = true
		if err := k.writeFrameLocked(map[string]any{"id": id, "type": "exec", "code": code}); err != nil {
			return Result{}, fmt.Errorf("pykernel: worker not accepting cells: %w", err)
		}
	}

	type readRes struct {
		res Result
		err error
	}
	ch := make(chan readRes, 1)
	go func() {
		res, err := k.readResponseLocked(cellCtx, id)
		ch <- readRes{res, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return Result{}, r.err
		}
		r.res.Generation = k.gen
		r.res.Restarted = restarted
		return r.res, nil
	case <-cellCtx.Done():
		k.killLocked()
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("pykernel: cell cancelled: %w", ctx.Err())
		}
		return Result{}, fmt.Errorf("pykernel: cell timed out after %s; the kernel was killed and its namespace is lost (the next cell starts fresh)", timeout)
	}
}

// Ok reports whether the cell finished without a Python-level error.
func (r Result) Ok() bool { return r.Error == "" }

// Generation reports the current kernel generation (0 = never started).
func (k *Kernel) Generation() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.gen
}

// Reset kills the worker; the next cell starts a fresh namespace.
func (k *Kernel) Reset() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.killLocked()
}

// ensureSpawned starts the worker if it is not running.
func (k *Kernel) ensureSpawned() error {
	if k.alive() {
		return nil
	}
	return k.spawnLocked()
}

func (k *Kernel) alive() bool {
	return k.proc != nil && k.proc.Process != nil && k.proc.ProcessState == nil
}

// resolvePython is the interpreter resolution seam; tests override it to pin
// a specific interpreter (e.g. the bundled embeddable distribution).
var resolvePython = hiruntime.ResolvePython

func (k *Kernel) spawnLocked() error {
	py, prefix, err := resolvePython()
	if err != nil {
		return fmt.Errorf("pykernel: Python is not available — python_cell needs a Python 3.8+ interpreter (bundled runtimes/python/, py or python3 on PATH, or uv): %w", err)
	}
	cmd := exec.Command(py, append(prefix, "-I", "-X", "utf8", "-c", workerScript)...)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("pykernel: stdin pipe: %w", err)
	}
	protoOut, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pykernel: protocol pipe: %w", err)
	}
	diag := &diagBuffer{}
	cmd.Stderr = diag
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("pykernel: spawn %s: %w", py, err)
	}
	k.proc = cmd
	k.stdin = stdin
	k.out = bufio.NewReaderSize(protoOut, 64*1024)
	k.diag = diag
	k.gen++
	return nil
}

func (k *Kernel) writeFrameLocked(frame map[string]any) error {
	line, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("pykernel: encode frame: %w", err)
	}
	if len(line)+1 > frameMax {
		return fmt.Errorf("pykernel: frame exceeds %d bytes", frameMax)
	}
	if _, err := k.stdin.Write(append(line, '\n')); err != nil {
		k.killLocked()
		return fmt.Errorf("pykernel: write frame: %w", err)
	}
	return nil
}

// readResponseLocked reads protocol frames until the response for id arrives,
// serving host_call frames inline (the cell blocks on them). Runs on the cell
// goroutine under Cell's mutex; ctx is enforced by readLineLocked.
func (k *Kernel) readResponseLocked(ctx context.Context, id int) (Result, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		line, err := k.readLineLocked(ctx)
		if err != nil {
			return Result{}, err
		}
		var frame struct {
			ID     int            `json:"id"`
			Type   string         `json:"type"`
			Ok     bool           `json:"ok"`
			Stdout string         `json:"stdout"`
			Stderr string         `json:"stderr"`
			Error  string         `json:"error"`
			Comp   map[string]any `json:"completion"`
			DurMs  int64          `json:"duration_ms"`
			Method string         `json:"method"`
			Args   map[string]any `json:"args"`
		}
		if err := json.Unmarshal(line, &frame); err != nil {
			return Result{}, fmt.Errorf("pykernel: bad frame (%.120s): %w", line, err)
		}
		switch frame.Type {
		case "response":
			if frame.ID != id {
				continue // stale response from a killed cell; ignore
			}
			return Result{
				Stdout:     frame.Stdout,
				Stderr:     frame.Stderr,
				Error:      frame.Error,
				Completion: frame.Comp,
				DurationMs: frame.DurMs,
			}, nil
		case "host_call":
			k.serveHostCall(ctx, frame.ID, frame.Method, frame.Args)
		}
	}
}

// readLineLocked reads one protocol line. The blocking read cannot be
// cancelled directly, so ctx is enforced by killing the process on Done —
// which unblocks the read with an error.
func (k *Kernel) readLineLocked(ctx context.Context) ([]byte, error) {
	type lineRes struct {
		line []byte
		err  error
	}
	ch := make(chan lineRes, 1)
	go func() {
		line, err := k.out.ReadBytes('\n')
		ch <- lineRes{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			k.killLocked()
			return nil, fmt.Errorf("pykernel: worker stopped: %w; recent diagnostics: %s", r.err, k.diagSnapshot())
		}
		if len(r.line) > frameMax {
			k.killLocked()
			return nil, fmt.Errorf("pykernel: frame exceeds %d bytes", frameMax)
		}
		return r.line, nil
	case <-ctx.Done():
		k.killLocked()
		return nil, ctx.Err()
	}
}

func (k *Kernel) diagSnapshot() string {
	if k.diag == nil {
		return ""
	}
	s := strings.TrimSpace(k.diag.snapshot())
	if len(s) > 600 {
		s = s[len(s)-600:]
	}
	return s
}

func (k *Kernel) serveHostCall(ctx context.Context, id int, method string, args map[string]any) {
	var value any
	errMsg := ""
	if k.host == nil {
		errMsg = "no host capabilities are wired into this kernel"
	} else if v, err := k.host.HostCall(ctx, method, args); err != nil {
		errMsg = err.Error()
	} else {
		value = v
	}
	resp := map[string]any{"id": id, "type": "host_response", "ok": errMsg == ""}
	if errMsg != "" {
		resp["error"] = errMsg
	} else {
		resp["value"] = value
	}
	// A failed write means the worker is gone; the in-flight cell read will
	// observe the closed pipe and report it.
	_ = k.writeFrameLocked(resp)
}

func (k *Kernel) killLocked() {
	k.killMu.Lock()
	defer k.killMu.Unlock()
	proc := k.proc
	stdin := k.stdin
	if proc == nil {
		return
	}
	k.proc = nil
	k.stdin = nil
	k.out = nil
	if p := proc.Process; p != nil {
		if runtime.GOOS == "windows" {
			// Cells may spawn subprocesses; taskkill /T walks the tree.
			_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(p.Pid)).Run()
		} else {
			_ = p.Kill()
		}
	}
	_ = stdin.Close()
	// cmd.Wait releases the pipes once the process is gone; background so the
	// killer never blocks on a wedged child.
	go func() { _ = proc.Wait() }()
}
