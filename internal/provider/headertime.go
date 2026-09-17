package provider

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultResponseHeaderTimeout is the default time-to-first-response-header
// budget for LLM API transports. With SSE streaming a healthy server sends
// response headers almost immediately — a model "thinking" happens after the
// 200 + headers, as delayed data chunks or keepalives. A server that cannot
// produce headers in this window is effectively down (dead relay, blackholed
// gateway), and a long per-attempt wait only delays the inevitable failure:
// with retries it used to multiply into tens of minutes of silence.
const DefaultResponseHeaderTimeout = 90 * time.Second

// headerTimeoutEnv lets users tune the budget per machine without a rebuild
// (seconds or a Go duration, e.g. "45s" / "120").
const headerTimeoutEnv = "FAIRPEER_RESPONSE_HEADER_TIMEOUT"

// ResponseHeaderTimeout returns the effective header timeout for LLM API
// transports: FAIRPEER_RESPONSE_HEADER_TIMEOUT when set to a positive
// duration, otherwise DefaultResponseHeaderTimeout.
func ResponseHeaderTimeout() time.Duration {
	v := strings.TrimSpace(os.Getenv(headerTimeoutEnv))
	if v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return DefaultResponseHeaderTimeout
}

// IsHeaderTimeout reports whether err is (or wraps) a transport-level
// "timeout awaiting response headers" failure — the server accepted the
// connection but never answered with headers. Unlike a mid-stream reset this
// pattern is deterministic for a dead endpoint, so retrying it ten times is
// pure waiting; SendWithRetry caps these separately.
func IsHeaderTimeout(err error) bool {
	if err == nil {
		return false
	}
	// net/http returns this exact message from Transport.ResponseHeaderTimeout.
	return strings.Contains(err.Error(), "timeout awaiting response headers")
}
