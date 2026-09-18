// Package browseruse is the Go client for the Python browser-use sidecar
// (browseruse_server.py). The sidecar runs an autonomous browsing loop driven
// by browser-use: it connects to a browser over CDP, snapshots the page, asks
// the LLM what to do next, and acts — repeating until the goal is met.
//
// The host (hiq) launches the browser itself (see internal/browserlaunch)
// and hands the sidecar a wsURL, so there is exactly one shared browser: the
// agent drives it, the in-app panel mirrors it. This client speaks the
// sidecar's localhost HTTP+SSE protocol:
//
//	GET  /health  -> { "ok": bool, "browser_use_available": bool }
//	POST /run     -> SSE stream of step events (thought/action/screenshot/done/error)
//	POST /stop    -> cancel the current run
//
// The protocol shape mirrors Hyper-Extract's he_client.go so the two sidecars
// feel uniform in the codebase.
package browseruse

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is the HTTP client for the browser-use sidecar.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient creates a client pointing at the sidecar on the given port.
func NewClient(port int) *Client {
	return &Client{
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		// A long overall timeout is fine; individual calls carry their own
		// context deadline (an agentic run can take minutes).
		http: &http.Client{Timeout: 30 * time.Minute},
	}
}

// HealthReport is the /health response.
type HealthReport struct {
	OK              bool `json:"ok"`
	BrowserUseAvail bool `json:"browser_use_available"`
}

// Health checks whether the server is up and the browser-use library actually
// loaded (BrowserUseAvail). Callers should gate real runs on BrowserUseAvail,
// not just OK, otherwise a server with missing Python deps will 500.
func (c *Client) Health(ctx context.Context) (*HealthReport, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/health", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r HealthReport
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// RunRequest is the body of POST /run.
type RunRequest struct {
	// Goal is the natural-language task ("fill the login form and submit").
	Goal string `json:"goal"`
	// URL is an optional starting URL to navigate to before the loop begins.
	URL string `json:"url,omitempty"`
	// CDPURL is REQUIRED: the ws:// endpoint of the browser to drive. The
	// sidecar calls connect_over_cdp(this) — it never spawns its own browser,
	// so the host and the sidecar share one instance.
	CDPURL string `json:"cdp_url"`
	// MaxSteps caps the agentic loop (default applied by the sidecar if 0).
	MaxSteps int `json:"max_steps,omitempty"`
	// Model is the LLM model NAME (no provider prefix) the sidecar should use
	// (e.g. "gpt-4o", "claude-sonnet-4-5"). The host resolves the hiq
	// "provider/model" ref down to this bare name before sending.
	Model string `json:"model,omitempty"`
	// ProviderKind selects the LLM client family: "openai" (OpenAI-compatible,
	// the default — also covers azure-compatible via BaseURL) or "anthropic".
	ProviderKind string `json:"provider_kind,omitempty"`
	// BaseURL overrides the LLM provider endpoint (for OpenAI-compatible gateways
	// like vendor gateways). Empty = the client's default (api.openai.com / anthropic).
	BaseURL string `json:"base_url,omitempty"`
	// APIKeyEnv names the environment variable holding the API key. The sidecar
	// reads os.environ[APIKeyEnv] — the key is never sent over the wire, matching
	// hiq's credentials-via-env model. Empty = fall back to the standard
	// OPENAI_API_KEY / ANTHROPIC_API_KEY based on ProviderKind.
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// Proxy is an optional proxy URL for the LLM client (not the browser —
	// the browser proxy is set at launch time by browserlaunch).
	Proxy string `json:"proxy,omitempty"`
}

// EventType discriminates a StepEvent.
type EventType string

const (
	EventThought    EventType = "thought"    // the model's reasoning text
	EventAction     EventType = "action"     // a parsed action description
	EventScreenshot EventType = "screenshot" // a base64 frame (data URL)
	EventDone       EventType = "done"       // run finished (final summary)
	EventError      EventType = "error"      // run failed (final)
)

// StepEvent is one SSE-delivered event from the agentic loop.
type StepEvent struct {
	Type  EventType `json:"type"`
	Step  int       `json:"step,omitempty"`
	Text  string    `json:"text,omitempty"`  // thought/action/done/error text
	Image string    `json:"image,omitempty"` // data URL for screenshot events
	URL   string    `json:"url,omitempty"`   // current page URL, when known
	Done  bool      `json:"done,omitempty"`  // true on the terminal done/error event
}

// RunStream posts a run request and returns a channel of step events. The
// channel closes when the stream ends (done/error) or the context is cancelled.
// The first non-nil error from the stream (e.g. HTTP 500) is delivered as a
// StepEvent with Type=EventError and Done=true, then the channel closes — this
// keeps the caller's loop uniform (it never has to handle a separate error
// return per event).
func (c *Client) RunStream(ctx context.Context, req RunRequest) (<-chan StepEvent, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/run", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	// Use a client with no overall timeout for the streaming response; the
	// per-call context governs cancellation instead.
	streamClient := &http.Client{}
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("browser-use /run failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	ch := make(chan StepEvent, 16)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		pumpSSE(resp.Body, ch)
	}()
	return ch, nil
}

// pumpSSE reads the Server-Sent Events body and pushes parsed events. Each SSE
// message is a "data: <json>\n\n" block (possibly multiple data lines). We
// accumulate data lines and JSON-decode the joined payload.
func pumpSSE(r io.Reader, ch chan<- StepEvent) {
	scanner := bufio.NewScanner(r)
	// Agentic screenshots can be large base64 payloads; raise the per-line cap
	// well above the default 64KB.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var dataLines []string
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		var ev StepEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			// Malformed event — surface as an error rather than dropping it.
			ch <- StepEvent{Type: EventError, Text: "malformed sidecar event: " + err.Error()}
			return
		}
		ch <- ev
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		// Other SSE fields (event:, id:, retry:, comments) are ignored — the
		// sidecar only emits data: lines.
	}
	flush()
	if err := scanner.Err(); err != nil {
		ch <- StepEvent{Type: EventError, Text: "stream read error: " + err.Error(), Done: true}
	}
}

// Stop asks the sidecar to cancel the in-flight run (if any). It is
// best-effort: the sidecar may have already finished.
func (c *Client) Stop(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/stop", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
