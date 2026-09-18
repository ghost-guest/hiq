package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/tool"
)

// fakeProgressServer answers one tools/call with a notifications/progress
// (echoing the request's progressToken) followed by the result, driving the
// readLoop end-to-end over an in-memory pipe.
type fakeProgressServer struct {
	r *bufio.Reader
	w io.Writer
}

func (f *fakeProgressServer) serve() {
	for {
		line, err := f.r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID     int `json:"id"`
			Params struct {
				Meta struct {
					ProgressToken string `json:"progressToken"`
				} `json:"_meta"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &req) != nil {
			continue
		}
		// 3-7②: elicitation/create REQUEST first — the transport must surface
		// it to the registered handler and write our decision back here.
		elicit, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      9001,
			"method":  "elicitation/create",
			"params":  map[string]any{"message": "allow file access?"},
		})
		f.w.Write(append(elicit, '\n'))
		decisionLine, derr := f.r.ReadBytes('\n')
		behavior := "missing"
		if derr == nil {
			var d struct {
				Result struct {
					Behavior string `json:"behavior"`
				} `json:"result"`
			}
			if json.Unmarshal(decisionLine, &d) == nil && d.Result.Behavior != "" {
				behavior = d.Result.Behavior
			}
		}
		// Progress notification next (token from the request), then result.
		notif, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/progress",
			"params":  map[string]any{"progressToken": req.Params.Meta.ProgressToken, "message": "step 1/3"},
		})
		f.w.Write(append(notif, '\n'))
		resp, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{"content": []any{map[string]any{"type": "text", "text": "done elicit=" + behavior}}},
		})
		f.w.Write(append(resp, '\n'))
	}
}

// TestStdioProgressRoutesByToken (upgrade spec 3-7①) proves the full chain:
// callProgressive injects the token into params._meta, the readLoop routes the
// server's progress notification back by that token, and the message reaches
// the progress sink stamped on the ctx — before the call itself returns.
func TestStdioProgressRoutesByToken(t *testing.T) {
	// pipe A carries transport→server requests, pipe B server→transport
	// responses/notifications (directions crossed in the first draft, which
	// deadlocked the transport on its own echoed request).
	ar, aw := io.Pipe()
	br, bw := io.Pipe()
	go (&fakeProgressServer{r: bufio.NewReader(ar), w: bw}).serve()

	tr := &stdioTransport{
		name:    "progress-fake",
		stdin:   aw,
		stdout:  bufio.NewReader(br),
		pending: map[int]chan rpcResponse{},
		progress: map[string]func(string){},
	}
	go tr.readLoop()
	defer tr.close()
	// Tear both pipes down so the server and readLoop goroutines exit instead
	// of leaking past the test.
	defer aw.Close()
	defer ar.Close()
	defer bw.Close()
	defer br.Close()

	tr.setElicitation(func(id json.RawMessage, _ json.RawMessage) {
		if string(id) != "9001" {
			tr.writeElicitationDecision(id, false, "")
			return
		}
		tr.writeElicitationDecision(id, true, "ok")
	})

	var mu sync.Mutex
	var chunks []string
	ctx := tool.WithProgress(context.Background(), func(chunk string) {
		mu.Lock()
		chunks = append(chunks, chunk)
		mu.Unlock()
	})

	c := &Client{t: tr, name: "progress-fake"}
	res, err := c.callProgressive(ctx, "tools/call", map[string]any{
		"name":      "long_job",
		"arguments": map[string]any{},
	})
	if err != nil {
		t.Fatalf("callProgressive: %v", err)
	}
	if !strings.Contains(string(res), "done elicit=allow") {
		t.Fatalf("unexpected result: %s", res)
	}
	// The notification is written before the response; by the time the call
	// returns, the readLoop has processed it. Poll briefly for safety.
	for i := 0; i < 20; i++ {
		mu.Lock()
		n := len(chunks)
		mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(chunks) != 1 || chunks[0] != "step 1/3" {
		t.Fatalf("progress chunks = %v, want [step 1/3]", chunks)
	}
}
