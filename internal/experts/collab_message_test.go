package experts

// collab_message_test.go covers the persistence record + context-layer
// projection: the core of the "store full, send synthesis" design. The session
// saves a CollabRecord as a JSON-string tool message; the context layer detects
// it and swaps in a synthesis-only summary for the model.

import (
	"strings"
	"testing"

	"github.com/zzycxz/fairpeer/internal/provider"
)

func TestCollabRecordRoundTrip(t *testing.T) {
	res := &CollabResult{
		Synthesis: "建议采用方案A。",
		Rounds: [][]ExpertAnswer{
			{{ExpertName: "批判者", Text: "方案A风险较高但收益大。"}},
			{{ExpertName: "批判者", Text: "二轮补充。"}, {ExpertName: "建设者", Text: "同意A。"}},
		},
	}
	rec := NewCollabRecord("run_1", "team_1", "方案评审团", "选哪个方案", "debate", res, 1700000000000)
	if rec.Marker != collabMarker {
		t.Fatalf("marker = %q, want %q", rec.Marker, collabMarker)
	}

	content, err := rec.MarshalContent()
	if err != nil {
		t.Fatalf("MarshalContent: %v", err)
	}
	// The persisted form is a JSON string carrying the marker + every field.
	if !strings.Contains(content, collabMarker) {
		t.Fatalf("marshaled content lost the marker: %s", content)
	}
	if !strings.Contains(content, "方案评审团") {
		t.Fatalf("marshaled content lost team name: %s", content)
	}

	got, ok := ParseCollabRecord(content)
	if !ok {
		t.Fatalf("ParseCollabRecord rejected a valid record")
	}
	if got.TeamID != rec.TeamID || got.TeamName != rec.TeamName || got.Task != rec.Task {
		t.Fatalf("round-trip mismatch: got %+v", got)
	}
	if got.Synthesis != res.Synthesis {
		t.Fatalf("synthesis mismatch: %q", got.Synthesis)
	}
	if len(got.Rounds) != 2 || len(got.Rounds[1]) != 2 {
		t.Fatalf("rounds shape mismatch: %+v", got.Rounds)
	}
}

func TestParseCollabRecordRejectsForeignInput(t *testing.T) {
	cases := []string{
		"",                            // empty
		"not json",                    // malformed
		`{"teamId":"x"}`,              // valid JSON, no marker
		`{"__type":"something_else"}`, // wrong marker
	}
	for _, in := range cases {
		if _, ok := ParseCollabRecord(in); ok {
			t.Fatalf("ParseCollabRecord(%q) should reject (no marker), but accepted", in)
		}
	}
}

func TestCollabContextSummary(t *testing.T) {
	t.Run("with synthesis", func(t *testing.T) {
		rec := CollabRecord{TeamName: "评审团", Mode: "debate", Synthesis: "结论：选A。"}
		s := rec.ContextSummary()
		if !strings.Contains(s, "评审团") {
			t.Fatalf("summary lost team name: %q", s)
		}
		if !strings.Contains(s, "辩论") {
			t.Fatalf("summary lost mode label: %q", s)
		}
		if !strings.Contains(s, "结论：选A。") {
			t.Fatalf("summary lost synthesis body: %q", s)
		}
	})
	t.Run("empty synthesis emits a fallback note", func(t *testing.T) {
		rec := CollabRecord{TeamName: "评审团", Mode: "parallel"}
		s := rec.ContextSummary()
		if !strings.Contains(s, "未产出综合结论") {
			t.Fatalf("empty-synthesis summary should note the absence, got %q", s)
		}
	})
}

// TestCollabContextMessages is the key test: the stored tool message (full JSON)
// is projected to a synthesis-only string for the model, while ordinary messages
// pass through untouched, and non-collab tool results are never rewritten.
func TestCollabContextMessages(t *testing.T) {
	full := provider.Message{
		Role: provider.RoleTool, Name: ExpertCollabToolName,
		Content: mustMarshal(t, CollabRecord{
			Marker: collabMarker, TeamID: "t1", TeamName: "评审团", Mode: "debate",
			Task: "选方案", Synthesis: "综合：选A。",
			Rounds: [][]ExpertAnswer{{{ExpertName: "批判者", Text: "A好。"}}},
		}),
	}
	userMsg := provider.Message{Role: provider.RoleUser, Content: "用户提问"}
	otherTool := provider.Message{Role: provider.RoleTool, Name: "bash", Content: "ls output"}

	out := CollabContextMessages([]provider.Message{userMsg, full, otherTool})
	if len(out) != 3 {
		t.Fatalf("length changed: %d", len(out))
	}
	// userMsg and otherTool are byte-identical (unchanged).
	if out[0].Content != userMsg.Content {
		t.Fatalf("user message was mutated")
	}
	if out[2].Content != otherTool.Content {
		t.Fatalf("non-collab tool result was mutated")
	}
	// The collab message's content is now a plain synthesis summary string.
	proj := out[1].Content
	if !strings.Contains(proj, "综合：选A。") {
		t.Fatalf("projection lost synthesis: %q", proj)
	}
	if strings.Contains(proj, "批判者") {
		t.Fatalf("projection leaked expert transcript into context: %q", proj)
	}
}

// TestCollabContextMessagesNoOpWhenAbsent verifies that a message list with no
// collab messages returns the original slice unchanged (no allocation, identity
// preserved) — important for the hot path since this runs before every turn.
func TestCollabContextMessagesNoOpWhenAbsent(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "hi"},
		{Role: provider.RoleAssistant, Content: "hello"},
	}
	out := CollabContextMessages(msgs)
	if &out[0] != &msgs[0] {
		t.Fatalf("filter should return the original slice when nothing matches (got a copy)")
	}
}

func mustMarshal(t *testing.T, r CollabRecord) string {
	t.Helper()
	s, err := r.MarshalContent()
	if err != nil {
		t.Fatalf("MarshalContent: %v", err)
	}
	return s
}
