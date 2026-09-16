package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zzycxz/fairpeer/internal/tool"
)

// recallTool lets the model look up facts it saved earlier. Saved (scattered)
// memories are NOT injected into the per-turn prompt (only the portrait layer
// is), so without this tool the archive is a write-only black hole: remember can
// store a fact but nothing can fetch it back. recall is the read side — a pure
// filesystem scan with zero LLM cost.
//
// Two modes:
//   - No name (or empty): list every visible saved memory as "name — first line",
//     so the model knows what it has recorded and can pick one to read in full.
//   - With a name: return that memory's full body.
//
// Visibility follows the active profile partition (global + current mode), same
// as the rest of the store, so dev never sees cowork facts and vice versa.
type recallTool struct{ store Store }

// NewRecallTool returns the `recall` tool bound to store.
func NewRecallTool(store Store) tool.Tool { return recallTool{store: store} }

func (recallTool) Name() string { return "recall" }

func (recallTool) Description() string {
	return "Look up a fact previously saved with `remember` — memory works like a small knowledge base: every session shows a one-line index of saved facts, and this tool fetches the content. " +
		"Call with a `name` to read one fact's full body. " +
		"Call with no name to list saved facts; narrow the list with `level` (l1 global, l2 project, l3 session), `tag`, or `query` (keyword search over name, tags and body). " +
		"This is a local file read — it costs nothing and never calls a model."
}

func (recallTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "The slug of a saved memory to read in full. Omit to list/search instead."},
			"level": {"type": "string", "enum": ["l1", "l2", "l3"], "description": "When listing, show only facts at this level. l1 = global (user identity/preferences), l2 = this project, l3 = session working memory."},
			"tag": {"type": "string", "description": "When listing, show only facts carrying this tag."},
			"query": {"type": "string", "description": "When listing, keep only facts whose name, tags or body contain this text (case-insensitive keyword search)."}
		}
	}`)
}

func (t recallTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Name  string `json:"name"`
		Level string `json:"level"`
		Tag   string `json:"tag"`
		Query string `json:"query"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	name := strings.TrimSpace(in.Name)

	// Single-fact read: return the full body.
	if name != "" {
		path := t.store.Path(name)
		if path == "" {
			return "", fmt.Errorf("no memory named %q", name)
		}
		m, ok := loadMemory(path)
		if !ok {
			return "", fmt.Errorf("memory %q not found", name)
		}
		body := strings.TrimSpace(m.Body)
		if body == "" {
			return fmt.Sprintf("(memory %q is empty)", name), nil
		}
		return fmt.Sprintf("[%s] %s\n\n%s", LevelOf(m).Label(), m.Name, body), nil
	}

	// List/search mode: level + tag + keyword narrowed.
	facts := t.store.List()
	if len(facts) == 0 {
		return "No saved memories yet. Use `remember` to save a durable fact.", nil
	}
	levelFilter, hasLevel := ParseLevelArg(in.Level)
	tagFilter := strings.ToLower(strings.TrimSpace(in.Tag))
	query := strings.ToLower(strings.TrimSpace(in.Query))
	matched := make([]Memory, 0, len(facts))
	for _, m := range facts {
		if hasLevel && LevelOf(m) != levelFilter {
			continue
		}
		if tagFilter != "" && !hasTag(m, tagFilter) {
			continue
		}
		if query != "" && !matchesQuery(m, query) {
			continue
		}
		matched = append(matched, m)
	}
	if len(matched) == 0 {
		return fmt.Sprintf("No saved memory matched level=%q tag=%q query=%q (%d saved in total). Call recall with no filters to list everything.",
			in.Level, in.Tag, in.Query, len(facts)), nil
	}
	var b strings.Builder
	if len(matched) == len(facts) {
		fmt.Fprintf(&b, "%d saved memor%s:\n", len(facts), pluralMem(len(facts)))
	} else {
		fmt.Fprintf(&b, "%d of %d saved memories:\n", len(matched), len(facts))
	}
	for _, m := range matched {
		b.WriteString(promptIndexLine(m, LevelOf(m)))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), nil
}

// hasTag reports whether m carries tag (compared against its normalized tags).
func hasTag(m Memory, tag string) bool {
	for _, t := range m.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// matchesQuery reports whether q (already lower-cased) occurs in the memory's
// name, tags or body.
func matchesQuery(m Memory, q string) bool {
	if strings.Contains(strings.ToLower(m.Name), q) || strings.Contains(strings.ToLower(m.Body), q) {
		return true
	}
	for _, t := range m.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}

func (recallTool) ReadOnly() bool { return true }

// pluralMem returns "y" for one, "ies" otherwise — for the list header.
func pluralMem(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
