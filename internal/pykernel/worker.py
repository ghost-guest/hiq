"""hiq persistent Python kernel worker.

Code-as-Action runtime, ported from PKU-YuanGroup/OpenAI4S (MIT). The worker is
spawned by the Go host with `python -I -X utf8 worker.py` and speaks a
JSON-per-line protocol:

  host -> worker (stdin):   {"id": N, "type": "exec", "code": "..."}
  worker -> host (proto):   {"id": N, "type": "response", "ok": bool, ...}
  worker -> host (proto):   {"id": M, "type": "host_call", "method": "...", "args": {...}}
  host -> worker (stdin):   {"id": M, "type": "host_response", "ok": bool, "value": ...}

Protocol-line protection (the OpenAI4S fd trick, portable to Windows because
os.dup2 wraps the CRT _dup2):

  1. The parent wires the child's fd0 -> protocol input and fd1 -> protocol
     output; fd2 goes to a diagnostics pipe the parent drains separately.
  2. At startup the worker dups fd1 into _PROTO_OUT, then dup2(fd2, fd1) so the
     process-level fd1 now points at diagnostics. Any stray os.write(1, ...) or
     subprocess inheriting stdout lands in diagnostics, never on the protocol
     line.
  3. During a cell, sys.stdout/sys.stderr are swapped to bounded in-memory
     buffers, so Python-level print() output is captured for the model instead
     of leaking into the protocol.

The module namespace _NS persists across cells: imports, variables and helper
functions survive from one cell to the next — that persistence is the whole
point of the Code-as-Action paradigm (one cell replaces many tool round-trips).
"""

import collections
import io
import json
import os
import sys
import threading
import time
import traceback

# Per-stream capture cap: head and tail are kept with a marker between them, so
# a runaway cell cannot flood the model context but early setup output and the
# final error both stay visible.
_STREAM_CAP = 64 * 1024
_HALF = _STREAM_CAP // 2

# Host-callable methods allowlist. Only these names reach the Go dispatcher.
_HOST_METHODS = ("tool", "submit_output")


class _BoundedBuffer(io.TextIOBase):
    """Text sink that keeps the first and last _HALF chars up to the cap."""

    def __init__(self):
        super().__init__()
        self._head = []
        self._head_len = 0
        self._tail = collections.deque()
        self._tail_len = 0
        self._dropped = 0

    def writable(self):
        return True

    def write(self, s):
        if not s:
            return 0
        remaining = _HALF - self._head_len
        if remaining > 0:
            take = s[:remaining]
            self._head.append(take)
            self._head_len += len(take)
            s = s[len(take):]
        if s:
            self._tail.append(s)
            self._tail_len += len(s)
            over = self._tail_len - _HALF
            if over > 0:
                # Drop from the front of the tail, recounting the overshoot.
                while self._tail and over >= len(self._tail[0]):
                    over -= len(self._tail[0])
                    self._dropped += len(self._tail[0])
                    self._tail_len -= len(self._tail[0])
                    self._tail.popleft()
                if over > 0 and self._tail:
                    self._tail[0] = self._tail[0][over:]
                    self._dropped += over
                    self._tail_len -= over
        return len(s)

    def value(self):
        parts = self._head[:]
        if self._dropped > 0:
            parts.append("\n...[%d chars truncated]...\n" % self._dropped)
        parts.extend(self._tail)
        return "".join(parts)


class _Host:
    """The `hiq` object injected into the cell namespace.

    Every method is a synchronous RPC to the Go host: the worker writes a
    host_call frame and blocks until the matching host_response arrives. The
    cell is frozen while the host serves the call, then resumes — this is the
    mid-cell Host RPC that distinguishes Code-as-Action from plain tool use.
    """

    def tool(self, name, args=None):
        """Call a read-only hiq built-in tool (web_search, grep, ...)."""
        if not isinstance(name, str) or not name:
            raise ValueError("hiq.tool: name must be a non-empty string")
        return _host_call("tool", {"name": name, "args": args if args is not None else {}})

    def submit_output(self, **kwargs):
        """Declare the task's final structured result.

        Exactly one submit per kernel lifetime: a second call raises, mirroring
        OpenAI4S's completion contract (a submitted result is terminal). The
        payload is attached to the current cell's response frame.
        """
        if _SUBMITTED[0]:
            raise RuntimeError("hiq.submit_output: the final output was already submitted")
        if not kwargs:
            raise ValueError("hiq.submit_output: pass at least one keyword argument")
        # Recorded kernel-side: no host round-trip, the contract is local to
        # the worker (upstream OpenAI4S routes it through the host because its
        # completion service lives there; ours rides the response frame).
        _SUBMITTED[0] = True
        _COMPLETION.update(kwargs)
        return None


def _host_call(method, args):
    if method not in _HOST_METHODS:
        raise ValueError("hiq: unknown host method %r" % (method,))
    with _HOST_CALL_LOCK:
        _HOST_CALL_SEQ[0] += 1
        call_id = _HOST_CALL_SEQ[0]
        _write_frame({"id": call_id, "type": "host_call", "method": method, "args": args})
        while True:
            frame = _wait_host_response(call_id)
            if frame is None:
                raise RuntimeError("hiq: host connection closed during %s()" % method)
            if not frame.get("ok", False):
                raise RuntimeError("hiq.%s failed: %s" % (method, frame.get("error", "unknown error")))
            return frame.get("value")


def _wait_host_response(call_id):
    """Block until the host_response frame for call_id arrives (or None on EOF)."""
    while True:
        with _RESP_COND:
            if call_id in _RESPONSES:
                return _RESPONSES.pop(call_id)
            if _STDIN_EOF.is_set():
                return None
            _RESP_COND.wait(timeout=1.0)


# --- worker globals ---

_NS = {"__name__": "__main__", "hiq": _Host()}
_COMPLETION = {}
_SUBMITTED = [False]
_HOST_CALL_LOCK = threading.Lock()
_HOST_CALL_SEQ = [0]
_RESPONSES = {}
_RESP_COND = threading.Condition()
_STDIN_EOF = threading.Event()

_PROTO_IN = None   # set in _setup_protocol_channels
_PROTO_OUT = None  # set in _setup_protocol_channels


def _setup_protocol_channels():
    global _PROTO_IN, _PROTO_OUT
    # Move BOTH protocol directions off the std fds before any cell runs:
    #   - _PROTO_IN is a private dup of fd0, and fd0 becomes devnull. Cells
    #     spawn subprocesses all the time; a child that inherited the
    #     protocol pipe being actively read by our reader thread deadlocks on
    #     Windows (reproduced: CreateProcess from a thread blocked in ReadFile
    #     on a handle the child inherits). With fd0 = devnull the child's std
    #     handles are harmless, and the actively-read handle is a non-std dup
    #     that close_fds=True never passes. Bonus on POSIX: a cell subprocess
    #     reading stdin can no longer swallow protocol frames.
    #   - _PROTO_OUT dups fd1, then fd1 is re-pointed at fd2 (diagnostics), so
    #     stray fd-level writes and child stdout land in diagnostics instead
    #     of corrupting the protocol line.
    _PROTO_IN = os.fdopen(os.dup(0), "rb")
    devnull = os.open(os.devnull, os.O_RDONLY)
    os.dup2(devnull, 0)
    os.close(devnull)
    _PROTO_OUT = os.fdopen(os.dup(1), "wb")
    os.dup2(2, 1)
    # Line-buffered text wrappers for the diagnostics fds so cell subprocess
    # output does not interleave badly; not strictly required.
    sys.stdout = io.TextIOWrapper(io.FileIO(1, "w", closefd=False), encoding="utf-8", line_buffering=True)
    sys.stderr = io.TextIOWrapper(io.FileIO(2, "w", closefd=False), encoding="utf-8", line_buffering=True)


def _write_frame(frame):
    _PROTO_OUT.write((json.dumps(frame, ensure_ascii=False, default=_json_default) + "\n").encode("utf-8"))
    _PROTO_OUT.flush()


def _json_default(o):
    if isinstance(o, (bytes, bytearray)):
        return o.decode("utf-8", "replace")
    return repr(o)


def _reader_thread():
    """Read protocol frames from stdin and dispatch them.

    exec frames go to the exec queue (consumed by the main loop); host_response
    frames are routed to the waiting _host_call. EOF sets _STDIN_EOF so blocked
    host calls unwind instead of hanging forever.
    """
    try:
        for line in _PROTO_IN:
            line = line.strip()
            if not line:
                continue
            try:
                frame = json.loads(line)
            except ValueError:
                _write_frame({"type": "log", "level": "warn", "text": "bad frame: %.200s" % line})
                continue
            ftype = frame.get("type")
            if ftype == "exec":
                _EXEC_QUEUE.append(frame)
            elif ftype == "host_response":
                with _RESP_COND:
                    _RESPONSES[frame.get("id")] = frame
                    _RESP_COND.notify_all()
            # Unknown frame types are ignored: forward compatibility.
    finally:
        _STDIN_EOF.set()
        with _RESP_COND:
            _RESP_COND.notify_all()


import collections

_EXEC_QUEUE = collections.deque()


def _run_cell(code):
    """Execute one code cell in the persistent namespace. Returns the response frame body."""
    out, err = _BoundedBuffer(), _BoundedBuffer()
    old_out, old_err = sys.stdout, sys.stderr
    sys.stdout, sys.stderr = out, err
    error_text = None
    t0 = _now_ms()
    try:
        try:
            exec(compile(code, "<cell>", "exec"), _NS)
        except SystemExit as e:
            code_val = e.code
            if code_val is not None and code_val is not True:
                error_text = "SystemExit: %s" % (code_val,)
        except BaseException:
            error_text = traceback.format_exc()
    finally:
        sys.stdout, sys.stderr = old_out, old_err
    body = {
        "type": "response",
        "ok": error_text is None,
        "stdout": out.value(),
        "stderr": err.value(),
        "duration_ms": _now_ms() - t0,
    }
    if error_text is not None:
        body["error"] = error_text
    if _COMPLETION:
        body["completion"] = dict(_COMPLETION)
        _COMPLETION.clear()
    return body


def _now_ms():
    return int(time.time() * 1000)


def _main_loop():
    """Execute exec frames serially; each response frame is written back in order."""
    while True:
        if not _EXEC_QUEUE:
            if _STDIN_EOF.is_set():
                break
            time.sleep(0.02)
            continue
        frame = _EXEC_QUEUE.popleft()
        code = frame.get("code") or ""
        body = _run_cell(code)
        body["id"] = frame.get("id")
        try:
            _write_frame(body)
        except OSError:
            break  # protocol line is gone; the host owns our lifetime now


def main():
    _setup_protocol_channels()
    t = threading.Thread(target=_reader_thread, daemon=True)
    t.start()
    _main_loop()


if __name__ == "__main__":
    main()
