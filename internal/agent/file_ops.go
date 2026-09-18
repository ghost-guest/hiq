package agent

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/zzycxz/hiq/internal/provider"
)

// FileOperations tallies file paths touched by the path-taking built-in tools
// over a slice of messages. It is the deterministic source of the <read-files>
// and <modified-files> blocks appended to a compaction summary: rather than
// trust the summarizer to lift exact paths out of free-text transcripts, we
// read them straight from the tool-call arguments (mirroring pi's
// compaction/utils.ts extractFileOpsFromMessage).
type FileOperations struct {
	Read    map[string]struct{}
	Written map[string]struct{}
	Edited  map[string]struct{}
}

// pathArgsing tools expose a "path" field at the top level of their arguments.
// multi_edit is included here too — its outer object carries the path even
// though the per-edit steps carry old_string/new_string. read-only search
// tools (glob, grep) are intentionally excluded: they don't "read a file" in
// the resume-relevant sense and would noise up the list.
var pathArgsingTools = map[string]struct{}{
	"read_file":  {},
	"write_file": {},
	"edit_file":  {},
	"multi_edit": {},
}

// newFileOperations returns a zero-valued FileOperations with all maps ready.
func newFileOperations() FileOperations {
	return FileOperations{
		Read:    make(map[string]struct{}),
		Written: make(map[string]struct{}),
		Edited:  make(map[string]struct{}),
	}
}

// ExtractFileOps scans messages for assistant tool calls against the path-taking
// built-ins and collects their paths. It is best-effort: any tool call whose
// arguments don't parse as JSON (truncated mid-stream, malformed) is silently
// skipped — a missing path never aborts compaction. We deliberately do not
// invoke provider's internal argument-repair path here: a tool call whose
// "path" we can't read straight from the wire JSON is not trustworthy enough
// to attribute to a file anyway.
func ExtractFileOps(messages []provider.Message) FileOperations {
	ops := newFileOperations()
	for _, m := range messages {
		if m.Role != provider.RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		for _, tc := range m.ToolCalls {
			if _, ok := pathArgsingTools[tc.Name]; !ok {
				continue
			}
			path := extractPathArg(tc.Arguments)
			if path == "" {
				continue
			}
			switch tc.Name {
			case "read_file":
				ops.Read[path] = struct{}{}
			case "write_file":
				ops.Written[path] = struct{}{}
			case "edit_file", "multi_edit":
				ops.Edited[path] = struct{}{}
			}
		}
	}
	return ops
}

// extractPathArg pulls the top-level "path" string from a tool-call arguments
// JSON blob. Returns "" for missing/non-string/empty path or unparseable JSON.
func extractPathArg(argsJSON string) string {
	if argsJSON == "" {
		return ""
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return ""
	}
	return strings.TrimSpace(p.Path)
}

// Empty reports whether no file operation was recorded.
func (ops FileOperations) Empty() bool {
	return len(ops.Read)+len(ops.Written)+len(ops.Edited) == 0
}

// Format renders the operations as <read-files> and <modified-files> blocks
// suitable for appending to a compaction summary. modified = written ∪ edited;
// read-only = read − modified (a file both read and later edited is listed only
// under modified, which is what the agent needs when resuming). Paths are
// sorted for deterministic output. Returns "" when nothing was touched.
func (ops FileOperations) Format() string {
	if ops.Empty() {
		return ""
	}

	modified := make(map[string]struct{}, len(ops.Written)+len(ops.Edited))
	for p := range ops.Written {
		modified[p] = struct{}{}
	}
	for p := range ops.Edited {
		modified[p] = struct{}{}
	}
	readOnly := make(map[string]struct{}, len(ops.Read))
	for p := range ops.Read {
		if _, isMod := modified[p]; !isMod {
			readOnly[p] = struct{}{}
		}
	}

	var b strings.Builder
	if len(readOnly) > 0 {
		b.WriteString("<read-files>\n")
		for _, p := range sortedKeys(readOnly) {
			b.WriteString(p)
			b.WriteByte('\n')
		}
		b.WriteString("</read-files>")
	}
	if len(modified) > 0 {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("<modified-files>\n")
		for _, p := range sortedKeys(modified) {
			b.WriteString(p)
			b.WriteByte('\n')
		}
		b.WriteString("</modified-files>")
	}
	return b.String()
}

// sortedKeys returns the keys of m as a sorted slice (deterministic output).
func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
