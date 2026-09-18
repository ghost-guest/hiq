package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zzycxz/hiq/internal/tool"
)

// rememberTool lets the model persist a durable fact to the auto-memory store.
// It is stateful (bound to one project's Store), so boot constructs it and adds
// it to the registry — the same pattern as the task tool — rather than
// self-registering as a stateless built-in.
//
// v0.4 rewrite: the schema is deliberately tiny — name + body + an optional
// profile partition. The old 11-field taxonomy (type/category/importance/
// valid_from/to/ttl/tags …), conflict detection, and supersede chaining were
// removed because they made every save an effort for the model and every saved
// row heavy to inject. Saved facts are no longer injected per turn anyway (only
// the portrait layer is), so the metadata that existed to govern injection
// (status/importance/decay) has no job. Same-name save overwrites; history is
// the user's VCS.
type rememberTool struct{ store Store }

// NewRememberTool returns the `remember` tool bound to store. A zero/disabled
// store yields a tool that reports the store is unavailable rather than silently
// dropping saves.
func NewRememberTool(store Store) tool.Tool { return rememberTool{store: store} }

func (rememberTool) Name() string { return "remember" }

func (rememberTool) Description() string {
	return "Save a durable fact to memory so it survives across sessions. " +
		"Use for things worth remembering long-term: who the user is and their preferences, " +
		"guidance on how to work, ongoing goals or constraints not derivable from the code, " +
		"or pointers to external resources. " +
		"Set `level` to say how far the fact reaches: l1 = about the user / how they work, " +
		"true everywhere; l2 = this project's conventions, decisions and constraints; " +
		"l3 = working notes for the current task only (never shown automatically, and expected to go stale). " +
		"Add `tags` so the fact can be found again by keyword. " +
		"Do NOT save what the repo already records (code structure, git history) or facts that " +
		"only matter to the current conversation. " +
		"Reusing a name overwrites that memory — do that to update an existing fact rather than " +
		"create a near-duplicate; use `forget` to drop one that is now wrong. " +
		"Saved facts appear as a one-line index (level · name · tags) in every session; use `recall` " +
		"to read a fact's full body or to search by level, tag or keyword."
}

func (rememberTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Short kebab-case slug identifying the fact, e.g. \"prefers-tabs\". Reusing a name overwrites that memory — do that to update an existing fact. Omit to derive one from the body's first line."},
			"body": {"type": "string", "description": "The fact itself (Markdown). The first line doubles as the index label shown in the memory panel."},
			"level": {"type": "string", "enum": ["l1", "l2", "l3"], "description": "How far the fact reaches. l1 (default) = about the user / how to work, shared across every project. l2 = specific to this project (conventions, decisions, constraints). l3 = working memory for the current task only: kept out of the automatic index, retrieved on demand."},
			"tags": {"type": "string", "description": "Comma-separated retrieval keywords, e.g. \"build, toolchain, ci\". Used by the memory index and by recall's search."},
			"profile": {"type": "string", "enum": ["global", "dev", "cowork"], "description": "Which product mode this fact belongs to. \"global\" (default) is shared across dev and cowork; \"dev\"/\"cowork\" are only visible in that mode. Omit when the fact is not mode-specific."},
			"project": {"type": "boolean", "description": "Legacy shorthand for level \"l2\": store under the current project instead of the shared bucket. Prefer the level field."}
		},
		"required": ["body"]
	}`)
}

func (t rememberTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Name    string `json:"name"`
		Body    string `json:"body"`
		Level   string `json:"level"`
		Tags    string `json:"tags"`
		Profile string `json:"profile"`
		Project bool   `json:"project"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(in.Body) == "" {
		return "", fmt.Errorf("body is required")
	}
	name := in.Name
	if name == "" {
		// Derive a slug from the first non-empty line of the body so a bare-body
		// save still gets a stable, human-readable file name.
		name = firstLine(in.Body)
	}

	// Level decides the tree the fact lives in. An explicit level always wins;
	// otherwise the legacy `project` flag still means "this project" so existing
	// callers keep landing their facts where they always did.
	level, explicit := ParseLevelArg(in.Level)
	if !explicit && in.Project {
		level = LevelProject
	}

	m := Memory{
		Name:    name,
		Body:    in.Body,
		Profile: normalizeSaveProfile(in.Profile, in.Project),
		Level:   level,
		Tags:    ParseTags(in.Tags),
	}
	path, err := t.store.Save(m)
	if err != nil {
		return "", err
	}
	if q, ok := QueueFromContext(ctx); ok {
		q.QueueMemory(fmt.Sprintf("Saved %s memory %q: %s", level.Label(), slug(name), oneLine(firstLine(in.Body))))
	}
	return fmt.Sprintf("Saved %s memory to %s (it appears in the memory index and is readable via recall in future sessions).",
		level.Label(), path), nil
}

func (rememberTool) ReadOnly() bool { return false }

// normalizeSaveProfile resolves the partition a save lands in. A project save
// always goes to the project-scoped bucket (the Store's Dir already encodes the
// active mode, so it stays mode-isolated). A non-project save honours an
// explicit profile ("global"/"dev"/"cowork"), defaulting to "global" so a
// bare-body remember is shared across modes rather than silently hidden in one.
func normalizeSaveProfile(profile string, project bool) string {
	if project {
		return "project"
	}
	p := NormalizeProfileScope(profile)
	if p == "" {
		return "global"
	}
	return p
}

// NormalizeProfileScope coerces a save's profile argument to a known partition
// or "" (caller defaults to "global"). Unlike NormalizeProfile (which defaults
// unknowns to "dev" for *path* derivation), this returns "" for unknowns so the
// caller can apply the "shared by default" rule distinct from the path floor.
func NormalizeProfileScope(s string) string {
	p := strings.ToLower(strings.TrimSpace(s))
	switch p {
	case "global", "dev", "cowork":
		return p
	}
	return ""
}

// firstLine returns the first non-empty line of s, trimmed. Used to derive a
// memory's name (and index label) from a bare-body save.
func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return s
}
