package builtin

// Network recorder for browser sessions (snow-app parity, agent-first): every
// request/response/failure is captured into a capped ring, and the
// browser_network tool exposes list/get-body/clear to the agent — so it can
// see which API a page actually called and read the response without
// re-firing the request. The panel may render the same log later.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cdpnetwork "github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// netEntry is one recorded request, correlated by requestId across events.
type netEntry struct {
	RequestID cdpnetwork.RequestID `json:"-"`
	Seq       int64                `json:"seq"` // recorder-wide arrival order
	StartedAt int64                `json:"started_at_ms"`
	Method    string               `json:"method"`
	URL       string               `json:"url"`
	Type      string               `json:"type,omitempty"`   // resource type (Document/XHR/…)
	Status    int64                `json:"status,omitempty"` // 0 = pending/failed
	Mime      string               `json:"mime,omitempty"`
	Error     string               `json:"error,omitempty"` // loading failed reason
	BodyTaken bool                 `json:"body_taken,omitempty"`
}

// netRecorder is the per-session ring (lazy, like panelStreamState).
type netRecorder struct {
	mu        sync.Mutex
	listenCtx context.Context // context the event listener is attached to
	seq       int64
	entries   []*netEntry // arrival order; capped
	byID      map[cdpnetwork.RequestID]*netEntry
}

// netCap bounds the ring; over the cap the oldest completed entries drop.
const netCap = 300

// netBodyMaxChars bounds get-response-body output (tool-result hygiene).
const netBodyMaxChars = 8000

func netRecorderFor(s *browserSession) *netRecorder {
	st := s.netLog.Load()
	if st == nil {
		st = &netRecorder{byID: map[cdpnetwork.RequestID]*netEntry{}}
		if !s.netLog.CompareAndSwap(nil, st) {
			st = s.netLog.Load()
		}
	}
	return st
}

// netEnsureListener installs the CDP event handler on the current tab's
// context (re-installed after tab switches; never duplicated).
func netEnsureListener(s *browserSession) {
	st := netRecorderFor(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.listenCtx == s.ctx {
		return
	}
	chromedp.ListenTarget(s.ctx, netHandler(s, st))
	st.listenCtx = s.ctx
}

func netHandler(s *browserSession, st *netRecorder) func(interface{}) {
	return func(ev interface{}) {
		switch e := ev.(type) {
		case *cdpnetwork.EventRequestWillBeSent:
			entry := &netEntry{
				RequestID: e.RequestID,
				Seq:       atomic.AddInt64(&st.seq, 1),
				StartedAt: time.Now().UnixMilli(),
				Method:    e.Request.Method,
				URL:       e.Request.URL,
			}
			if e.Type != "" {
				entry.Type = string(e.Type)
			}
			st.mu.Lock()
			st.byID[e.RequestID] = entry
			st.entries = append(st.entries, entry)
			// Trim the ring: drop oldest entries over the cap.
			if over := len(st.entries) - netCap; over > 0 {
				for _, old := range st.entries[:over] {
					delete(st.byID, old.RequestID)
				}
				st.entries = append(st.entries[:0:0], st.entries[over:]...)
			}
			st.mu.Unlock()
		case *cdpnetwork.EventResponseReceived:
			st.mu.Lock()
			if entry := st.byID[e.RequestID]; entry != nil {
				entry.Status = e.Response.Status
				entry.Mime = e.Response.MimeType
				if entry.Type == "" && e.Type != "" {
					entry.Type = string(e.Type)
				}
			}
			st.mu.Unlock()
		case *cdpnetwork.EventLoadingFailed:
			st.mu.Lock()
			if entry := st.byID[e.RequestID]; entry != nil && entry.Status == 0 {
				entry.Error = e.ErrorText
			}
			st.mu.Unlock()
		}
	}
}

// netFormatEntry renders one entry as a compact line for tool output.
// isProbablyText reports whether a response body looks textual (control
// bytes beyond tab/CR/LF mean binary — JSON/HTML/XML/CSV all pass).
func isProbablyText(b []byte) bool {
	n := len(b)
	if n > 1024 {
		n = 1024
	}
	nonText := 0
	for _, c := range b[:n] {
		if c < 0x09 || (c > 0x0d && c < 0x20) {
			nonText++
		}
	}
	return nonText*20 < n // <5% control bytes
}

func netFormatEntry(e *netEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s", e.Seq, e.Method)
	switch {
	case e.Error != "":
		b.WriteString(" FAILED(" + e.Error + ")")
	case e.Status > 0:
		fmt.Fprintf(&b, " %d", e.Status)
	default:
		b.WriteString(" pending")
	}
	if e.Type != "" {
		b.WriteString(" [" + e.Type + "]")
	}
	b.WriteString(" " + e.URL)
	if e.Mime != "" {
		b.WriteString(" (" + e.Mime + ")")
	}
	return b.String()
}

// --- browser_network tool -----------------------------------------------------

type browserNetwork struct{}

func (browserNetwork) Name() string { return "browser_network" }

func (browserNetwork) Description() string {
	return "Inspect the network traffic the current page actually made (captured since the tab attached): list recent requests with method/status/type/URL, read a response body without re-firing the request, or clear the log. Use BEFORE guessing endpoints from page text — the log shows the real API calls, their order, and their failures. Actions: list (default), body (needs request_id from list), clear."
}

func (browserNetwork) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Browser session id from browser_open"},
  "action":{"type":"string","enum":["list","body","clear"],"description":"list recent requests (default), read one response body, or clear the log"},
  "request_id":{"type":"string","description":"request id from list output (for action=body)"},
  "url_contains":{"type":"string","description":"only list entries whose URL contains this substring"},
  "limit":{"type":"integer","description":"max entries for list (default 20, most recent last)"}
},
"required":["session_id"]
}`)
}

func (browserNetwork) ReadOnly() bool { return false } // clear is destructive

func (browserNetwork) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		SessionID   string `json:"session_id"`
		Action      string `json:"action"`
		RequestID   string `json:"request_id"`
		URLContains string `json:"url_contains"`
		Limit       int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	s, err := getBrowserSession(in.SessionID)
	if err != nil {
		return "", err
	}
	st := netRecorderFor(s)
	// Make sure the listener rides the current tab (lazy on first tool use).
	netEnsureListener(s)

	switch in.Action {
	case "", "list":
		limit := in.Limit
		if limit <= 0 {
			limit = 20
		}
		st.mu.Lock()
		matched := make([]*netEntry, 0, len(st.entries))
		for _, e := range st.entries {
			if in.URLContains == "" || strings.Contains(e.URL, in.URLContains) {
				matched = append(matched, e)
			}
		}
		if len(matched) > limit {
			matched = matched[len(matched)-limit:]
		}
		out := make([]string, len(matched))
		for i, e := range matched {
			out[i] = netFormatEntry(e)
		}
		total := len(st.entries)
		st.mu.Unlock()
		if len(out) == 0 {
			return "no network activity recorded yet (capture starts when the tab attaches)", nil
		}
		return fmt.Sprintf("%d/%d requests (most recent last):\n%s", len(out), total, strings.Join(out, "\n")), nil

	case "body":
		if in.RequestID == "" {
			return "", fmt.Errorf("action=body needs request_id (from list output)")
		}
		bodyCtx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		defer cancel()
		var body []byte
		if err := chromedp.Run(bodyCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			body, err = cdpnetwork.GetResponseBody(cdpnetwork.RequestID(in.RequestID)).Do(ctx)
			return err
		})); err != nil {
			return "", fmt.Errorf("read body: %w (the resource may be evicted — use list to re-check availability)", err)
		}
		st.mu.Lock()
		if entry := st.byID[cdpnetwork.RequestID(in.RequestID)]; entry != nil {
			entry.BodyTaken = true
		}
		st.mu.Unlock()
		if !isProbablyText(body) {
			return fmt.Sprintf("binary body (%d bytes) — use browser_screenshot or browser_evaluate for binary resources", len(body)), nil
		}
		bodyText := string(body)
		truncated := ""
		if len(bodyText) > netBodyMaxChars {
			bodyText = bodyText[:netBodyMaxChars]
			truncated = "\n… (truncated)"
		}
		return bodyText + truncated, nil

	case "clear":
		st.mu.Lock()
		st.entries = nil
		st.byID = map[cdpnetwork.RequestID]*netEntry{}
		st.mu.Unlock()
		return "network log cleared", nil

	default:
		return "", fmt.Errorf("unknown action %q", in.Action)
	}
}
