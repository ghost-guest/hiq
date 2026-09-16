package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// bodyRecorder wraps the mock provider so the guard can inspect the exact
// request bytes the client sent, without touching the mock's own bookkeeping.
type bodyRecorder struct {
	inner  http.HandlerFunc
	bodies [][]byte
}

func (r *bodyRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.bodies = append(r.bodies, b)
	req.Body = io.NopCloser(bytes.NewReader(b))
	r.inner(w, req)
}

// conversationBodies returns the recorded request bodies for conversation
// turns only (compaction issues a tool-less summarize request with a different
// system prompt; the mock excludes it from its own prefix bookkeeping too).
func (r *bodyRecorder) conversationBodies() [][]byte {
	out := make([][]byte, 0, len(r.bodies))
	for _, b := range r.bodies {
		if isSummarizeRequest(b) {
			continue
		}
		out = append(out, b)
	}
	return out
}

// The provider prefix cache can only serve a request whose leading bytes are
// IDENTICAL to a request it already saw. The e2e curve tests in
// cachehit_e2e_test.go assert on reported cache TOKENS, which the mock cannot
// produce, so they are skipped — this guard asserts the property the client
// actually controls: everything but the previous request's FINAL message must be
// re-sent byte-identically, and that one message may differ only by the
// per-turn clock notice appended to the last user turn (the "turn tail").
//
// Prefix invariance is a product-level guarantee (long sessions must keep
// hitting the provider cache), so this test is deliberately NOT skipped.
func TestRequestPrefixIsAppendOnly(t *testing.T) {
	cases := []struct {
		name  string
		mock  *mockTestProvider
		turns int
	}{
		{
			name:  "plain-dialogue",
			mock:  &mockTestProvider{t: t, reasoning: longReasoning},
			turns: 12,
		},
		{
			name:  "tool-loop",
			mock:  &mockTestProvider{t: t, withTools: true, reasoning: longReasoning, toolRounds: 8},
			turns: 6,
		},
		{
			name:  "no-reasoning-round-trip",
			mock:  &mockTestProvider{t: t, withTools: true, toolRounds: 4},
			turns: 6,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &bodyRecorder{}
			rec.inner = tc.mock.handler
			srv := httptest.NewServer(rec)
			defer srv.Close()

			a, _ := newAgent(t, srv.URL, tc.mock.tools(), 0, 0)
			for i := 0; i < tc.turns; i++ {
				msg := fmt.Sprintf("Turn %d: %s", i, strings.Repeat("please consider this requirement. ", 6))
				if err := a.Run(context.Background(), msg); err != nil {
					t.Fatalf("Run %d: %v", i, err)
				}
			}

			bodies := rec.conversationBodies()
			if len(bodies) < 2 {
				t.Fatalf("expected several conversation requests, got %d", len(bodies))
			}

			// 1. Every message except the previous request's last one must be
			// re-sent byte-identically — the cacheable core of the prefix.
			var firstSystem []byte
			for i := 1; i < len(bodies); i++ {
				prev, cur := decodeMessages(bodies[i-1]), decodeMessages(bodies[i])
				if len(cur) <= len(prev) {
					t.Errorf("request %d: %d messages, want more than the previous request's %d "+
						"(history must grow, never shrink or reorder)", i, len(cur), len(prev))
					continue
				}
				for k := 0; k < len(prev)-1; k++ {
					if !bytes.Equal(prev[k], cur[k]) {
						t.Errorf("request %d: message %d changed bytes; the prefix up to the previous "+
							"turn's last message must stay byte-identical\nprev: %s\ncur:  %s",
							i, k, truncateForLog(prev[k]), truncateForLog(cur[k]))
						break
					}
				}
				// 2. The one message allowed to differ is the previous turn's
				// final user message, and it may differ ONLY by the clock
				// notice the agent appends to the outgoing copy (the stored
				// session keeps the user's clean text).
				prevLast := prev[len(prev)-1]
				curAligned := cur[len(prev)-1]
				if bytes.Equal(prevLast, curAligned) {
					continue // ended on a tool/assistant message: fully append-only
				}
				var before, after struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				}
				_ = json.Unmarshal(prevLast, &before)
				_ = json.Unmarshal(curAligned, &after)
				if before.Role != "user" || after.Role != "user" {
					t.Errorf("request %d: message %d drifted on a non-user message (roles %q/%q) — "+
						"more than the clock notice changed", i, len(prev)-1, before.Role, after.Role)
					continue
				}
				if !strings.HasPrefix(before.Content, after.Content) {
					t.Errorf("request %d: the previous turn's final user message was rewritten beyond "+
						"appending a notice\nprev: %q\ncur:  %q", i, truncateForLog(prevLast), truncateForLog(curAligned))
					continue
				}
				added := strings.TrimPrefix(before.Content, after.Content)
				if !strings.Contains(added, "ADDITIONAL_METADATA") {
					t.Errorf("request %d: the final user message gained unexpected content %q "+
						"(only the clock notice may ride the turn tail)", i, truncateForLog([]byte(added)))
				}
			}

			// 3. The system prompt — the cached head — is byte-identical in
			// every request, and still the configured prompt text.
			for i, body := range bodies {
				msgs := decodeMessages(body)
				if len(msgs) == 0 {
					t.Fatalf("request %d carries no messages", i)
				}
				var m struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				}
				if err := json.Unmarshal(msgs[0], &m); err != nil {
					t.Fatalf("request %d: decode system message: %v", i, err)
				}
				if m.Role != "system" {
					t.Fatalf("request %d: first message role = %q, want system", i, m.Role)
				}
				if i == 0 {
					firstSystem = msgs[0]
					if m.Content != systemPrompt {
						t.Fatalf("system prompt drifted from the configured text:\ngot  %q\nwant %q", m.Content, systemPrompt)
					}
					continue
				}
				if !bytes.Equal(msgs[0], firstSystem) {
					t.Errorf("request %d: system message bytes differ from request 0 — the cached head changed mid-session", i)
				}
			}
			t.Logf("%s: prefix preserved across %d requests (only the final user turn carries the clock notice), cached head byte-stable",
				tc.name, len(bodies))
		})
	}
}

func truncateForLog(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
