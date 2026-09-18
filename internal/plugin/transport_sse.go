package plugin

// transport_sse.go — the legacy 2024-11-05 HTTP+SSE MCP transport.
//
// Protocol: the client opens a GET /sse stream; the server sends an "endpoint"
// event with the POST URL for JSON-RPC messages. Every request is a POST to
// that endpoint; responses come back on the same SSE stream.
//
// Deprecated upstream ("avoid for new work") but still needed for older
// remote servers that haven't migrated to Streamable HTTP. New configs
// should use type="http".

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// sseTransport implements the legacy SSE MCP transport: one persistent GET
// stream for server→client messages, POST for client→server.
type sseTransport struct {
	name     string
	baseURL  string // server root (e.g. https://example.com/mcp)
	headers  map[string]string
	client   *http.Client
	endpoint string // POST URL from the endpoint event
	eventCh  chan json.RawMessage
	errCh    chan error
	closeCh  chan struct{}

	mu     sync.Mutex
	nextID int
	// pending maps request id → response channel; the SSE dispatcher routes
	// responses by id.
	pending map[int]chan json.RawMessage
}

func newSSETransport(ctx context.Context, s Spec) (*sseTransport, error) {
	if s.URL == "" {
		return nil, fmt.Errorf("sse plugin %q: url is required", s.Name)
	}
	t := &sseTransport{
		name:    s.Name,
		baseURL: s.URL,
		headers: s.Headers,
		client:  &http.Client{},
		eventCh: make(chan json.RawMessage, 64),
		errCh:   make(chan error, 1),
		closeCh: make(chan struct{}),
		pending: make(map[int]chan json.RawMessage),
	}
	go t.readStream(ctx)
	return t, nil
}

// readStream opens the GET /sse connection and dispatches events.
func (t *sseTransport) readStream(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL, nil)
	if err != nil {
		t.errCh <- fmt.Errorf("sse %q: build GET: %w", t.name, err)
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		t.errCh <- fmt.Errorf("sse %q: connect: %w", t.name, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.errCh <- fmt.Errorf("sse %q: server answered %s", t.name, resp.Status)
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), maxHTTPBody)
	var eventType, dataBuf strings.Builder
	for scanner.Scan() {
		select {
		case <-t.closeCh:
			return
		default:
		}
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			eventType.Reset()
			eventType.WriteString(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimPrefix(line, "data:")
			if eventType.String() == "endpoint" {
				t.mu.Lock()
				t.endpoint = t.resolveURL(strings.TrimSpace(data))
				t.mu.Unlock()
				continue
			}
			dataBuf.WriteString(data)
		case line == "":
			// Event boundary — dispatch
			if dataBuf.Len() > 0 {
				raw := json.RawMessage(dataBuf.String())
				dataBuf.Reset()
				eventType.Reset()
				go t.dispatch(raw)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case <-t.closeCh:
		default:
			t.errCh <- fmt.Errorf("sse %q: stream ended: %w", t.name, err)
		}
	}
}

// dispatch routes a JSON-RPC response to its pending caller by id.
func (t *sseTransport) dispatch(raw json.RawMessage) {
	var env struct {
		ID     *int            `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.ID == nil {
		return // notification or malformed — ignore (hiq is a consumer)
	}
	t.mu.Lock()
	ch, ok := t.pending[*env.ID]
	delete(t.pending, *env.ID)
	t.mu.Unlock()
	if ok && ch != nil {
		ch <- raw
	}
}

// resolveURL turns the endpoint event's relative/absolute path into a full URL.
func (t *sseTransport) resolveURL(ep string) string {
	if strings.HasPrefix(ep, "http://") || strings.HasPrefix(ep, "https://") {
		return ep
	}
	// Relative to the SSE URL
	base := t.baseURL
	if idx := strings.Index(base, "//"); idx >= 0 {
		if slash := strings.Index(base[idx+2:], "/"); slash >= 0 {
			base = base[:idx+2+slash]
		}
	}
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(ep, "/")
}

func (t *sseTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	// Wait for the endpoint event
	t.mu.Lock()
	ep := t.endpoint
	t.mu.Unlock()
	for ep == "" {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-t.errCh:
			return nil, err
		default:
		}
		t.mu.Lock()
		ep = t.endpoint
		t.mu.Unlock()
		if ep == "" {
			return nil, fmt.Errorf("sse %q: no endpoint received (server didn't send the POST URL)", t.name)
		}
	}

	t.mu.Lock()
	t.nextID++
	id := t.nextID
	ch := make(chan json.RawMessage, 1)
	t.pending[id] = ch
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
	}()

	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sse %q: POST %s: %w", t.name, method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return nil, fmt.Errorf("sse %q: POST %s: server answered %s", t.name, method, resp.Status)
	}

	// Response arrives on the SSE stream
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case raw := <-ch:
		return raw, nil
	case err := <-t.errCh:
		return nil, err
	}
}

func (t *sseTransport) notify(ctx context.Context, method string, params any) error {
	// SSE transport has no reliable fire-and-forget; send as a call and discard.
	_, err := t.call(ctx, method, params)
	return err
}

func (t *sseTransport) close() {
	close(t.closeCh)
}
