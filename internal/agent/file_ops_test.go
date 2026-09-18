package agent

import (
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/provider"
)

// msgWithCalls builds an assistant message carrying the given tool calls.
func msgWithCalls(calls ...provider.ToolCall) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, ToolCalls: calls}
}

// tc builds a tool call with raw-JSON arguments.
func tc(name, argsJSON string) provider.ToolCall {
	return provider.ToolCall{ID: name, Name: name, Arguments: argsJSON}
}

func TestExtractFileOps_SingleToolEach(t *testing.T) {
	msgs := []provider.Message{
		msgWithCalls(tc("read_file", `{"path":"/a.go"}`)),
		msgWithCalls(tc("write_file", `{"path":"/b.go","content":"x"}`)),
		msgWithCalls(tc("edit_file", `{"path":"/c.go","old_string":"x","new_string":"y"}`)),
		msgWithCalls(tc("multi_edit", `{"path":"/d.go","edits":[{"old_string":"x","new_string":"y"}]}`)),
	}
	ops := ExtractFileOps(msgs)
	if _, ok := ops.Read["/a.go"]; !ok {
		t.Error("read_file path missing from Read")
	}
	if _, ok := ops.Written["/b.go"]; !ok {
		t.Error("write_file path missing from Written")
	}
	if _, ok := ops.Edited["/c.go"]; !ok {
		t.Error("edit_file path missing from Edited")
	}
	if _, ok := ops.Edited["/d.go"]; !ok {
		t.Error("multi_edit path missing from Edited")
	}
}

func TestExtractFileOps_IgnoresNonAssistantAndUnknownTools(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "hi", ToolCalls: []provider.ToolCall{tc("read_file", `{"path":"/a.go"}`)}},
		{Role: provider.RoleTool, Content: "result", ToolCalls: []provider.ToolCall{tc("write_file", `{"path":"/b.go"}`)}},
		msgWithCalls(tc("glob", `{"pattern":"*.go"}`)),
		msgWithCalls(tc("grep", `{"pattern":"foo"}`)),
		msgWithCalls(tc("bash", `{"command":"ls"}`)),
		msgWithCalls(tc("mcp__fs__read", `{"path":"/e.go"}`)),
	}
	ops := ExtractFileOps(msgs)
	if !ops.Empty() {
		t.Errorf("expected no ops, got Read=%v Written=%v Edited=%v", ops.Read, ops.Written, ops.Edited)
	}
}

func TestExtractFileOps_MalformedJSONSkipped(t *testing.T) {
	msgs := []provider.Message{
		msgWithCalls(
			tc("read_file", `{"path":"/a.go"}`),
			tc("edit_file", `{"path":"/b.go"`),  // truncated JSON
			tc("write_file", `not json at all`), // garbage
			tc("read_file", `{}`),               // no path field
			tc("read_file", `{"path":""}`),      // empty path
			tc("read_file", `{"path":"   "}`),   // whitespace-only path
			tc("write_file", `{"path":123}`),    // wrong type
		),
	}
	ops := ExtractFileOps(msgs)
	if len(ops.Read) != 1 {
		t.Errorf("expected only 1 entry in Read, got %v", ops.Read)
	}
	if _, ok := ops.Read["/a.go"]; !ok {
		t.Errorf("expected /a.go in Read, got %v", ops.Read)
	}
	if len(ops.Written) != 0 {
		t.Errorf("expected no Written (all malformed), got %v", ops.Written)
	}
	if len(ops.Edited) != 0 {
		t.Errorf("expected no Edited, got %v", ops.Edited)
	}
}

func TestExtractFileOps_DeduplicatesAcrossCalls(t *testing.T) {
	msgs := []provider.Message{
		msgWithCalls(tc("read_file", `{"path":"/a.go"}`)),
		msgWithCalls(tc("read_file", `{"path":"/a.go"}`)),     // same file read twice
		msgWithCalls(tc("edit_file", `{"path":"/a.go",...}`)), // edit on same path
	}
	// Note: the edit_file args above are intentionally invalid JSON; fix it:
	msgs[2] = msgWithCalls(tc("edit_file", `{"path":"/a.go","old_string":"x","new_string":"y"}`))
	ops := ExtractFileOps(msgs)
	if len(ops.Read) != 1 {
		t.Errorf("Read should dedupe to 1, got %v", ops.Read)
	}
	if len(ops.Edited) != 1 {
		t.Errorf("Edited should have /a.go, got %v", ops.Edited)
	}
}

func TestFileOperations_Format_Empty(t *testing.T) {
	ops := newFileOperations()
	if got := ops.Format(); got != "" {
		t.Errorf("empty ops Format() = %q, want empty", got)
	}
	if !ops.Empty() {
		t.Error("Empty() should be true")
	}
}

func TestFileOperations_Format_SeparatesReadAndModified(t *testing.T) {
	ops := newFileOperations()
	ops.Read["/read_only.go"] = struct{}{}
	ops.Written["/created.go"] = struct{}{}
	ops.Edited["/patched.go"] = struct{}{}

	got := ops.Format()
	// Read-only file under <read-files>.
	if !strings.Contains(got, "<read-files>\n/read_only.go\n</read-files>") {
		t.Errorf("missing read-files block in:\n%s", got)
	}
	// Both written and edited under <modified-files>, sorted.
	if !strings.Contains(got, "<modified-files>\n/created.go\n/patched.go\n</modified-files>") {
		t.Errorf("missing/incorrect modified-files block in:\n%s", got)
	}
}

func TestFileOperations_Format_ReadThatWasAlsoModifiedGoesToModifiedOnly(t *testing.T) {
	// A file both read and later edited must appear ONLY under modified — that's
	// what the agent needs to know on resume (it changed, not just was inspected).
	ops := newFileOperations()
	ops.Read["/a.go"] = struct{}{}
	ops.Edited["/a.go"] = struct{}{}
	ops.Read["/b.go"] = struct{}{} // read-only, stays under read-files

	got := ops.Format()
	if strings.Contains(got+"<sep>", "/a.go\n</read-files>") || strings.Contains(got, "<read-files>\n/a.go") {
		t.Errorf("/a.go should NOT be in read-files (it was modified):\n%s", got)
	}
	if !strings.Contains(got, "<modified-files>\n/a.go\n</modified-files>") {
		t.Errorf("/a.go should be in modified-files:\n%s", got)
	}
	if !strings.Contains(got, "<read-files>\n/b.go\n</read-files>") {
		t.Errorf("/b.go should be in read-files:\n%s", got)
	}
}

func TestFileOperations_Format_DeterministicOrder(t *testing.T) {
	// Calling Format twice must yield identical output (sorted keys).
	ops := newFileOperations()
	ops.Edited["/z.go"] = struct{}{}
	ops.Edited["/a.go"] = struct{}{}
	ops.Edited["/m.go"] = struct{}{}
	first := ops.Format()
	second := ops.Format()
	if first != second {
		t.Errorf("Format not deterministic:\nfirst:  %q\nsecond: %q", first, second)
	}
	// Verify sorted order within the block.
	if !strings.Contains(first, "<modified-files>\n/a.go\n/m.go\n/z.go\n</modified-files>") {
		t.Errorf("paths not sorted:\n%s", first)
	}
}

func TestExtractFileOps_RealisticMixedTurn(t *testing.T) {
	// A realistic assistant turn: explore then edit.
	msgs := []provider.Message{
		msgWithCalls(
			tc("read_file", `{"path":"/main.go"}`),
			tc("read_file", `{"path":"/util.go"}`),
			tc("grep", `{"pattern":"TODO","path":"/main.go"}`), // grep ignored
		),
		msgWithCalls(tc("edit_file", `{"path":"/main.go","old_string":"x","new_string":"y"}`)),
		msgWithCalls(tc("write_file", `{"path":"/new.go","content":"package main"}`)),
	}
	ops := ExtractFileOps(msgs)
	got := ops.Format()
	// /main.go was read AND edited → modified only.
	// /util.go was read only → read-files.
	// /new.go was written → modified.
	if !strings.Contains(got, "<read-files>\n/util.go\n</read-files>") {
		t.Errorf("util.go should be read-only:\n%s", got)
	}
	if !strings.Contains(got, "<modified-files>\n/main.go\n/new.go\n</modified-files>") {
		t.Errorf("main.go and new.go should be modified:\n%s", got)
	}
}
