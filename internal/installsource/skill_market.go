package installsource

// skill_market.go is the agent-facing tool for browsing/searching the skill
// marketplace (SPEC v2 §3.4C). It lets the agent:
//
//   - browse: list available skills from default sources (curated/Anthropic/
//     OpenAI/ClawHub) or a specific source
//   - search: search across all sources by keyword
//   - install: install a skill by its InstallRef (delegates to install_source's
//     plan→apply pipeline, so safety scan + manifest recording are automatic)
//
// The user-facing flow is natural language: "find me a code-review skill" →
// the agent calls skill_market search → lists matches → the user picks one →
// the agent calls skill_market install (or install_source directly). The agent
// is never told about "RiskClass" or "marketplace.json" — it sees a clean list
// of skills with names and descriptions.
//
// Design constraints (SPEC v2 §2.0): zero user learning cost (defaults work
// out of the box), zero prompt bloat (this is a tool's internal logic).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zzycxz/hiq/internal/tool"
)

// SkillMarketTool is the marketplace browse/search/install tool.
type SkillMarketTool struct {
	installer *installSourceTool
}

// NewMarketTool returns a skill_market tool wired to an existing installSourceTool
// (sharing the same httpClient, roots, and MCP connector for installs). The
// caller passes the *installSourceTool obtained from NewTool via type assertion.
func NewMarketTool(installer *installSourceTool) tool.Tool {
	return &SkillMarketTool{installer: installer}
}

// NewMarketToolFromOptions constructs both the installer and the market tool from
// Options, for boot wiring where the caller doesn't hold a *installSourceTool.
// The installer is built with the same SSRF-guarded httpClient as NewTool.
func NewMarketToolFromOptions(opts Options) tool.Tool {
	base := NewTool(opts).(*installSourceTool)
	return &SkillMarketTool{installer: base}
}

func (SkillMarketTool) Name() string { return "skill_market" }

func (SkillMarketTool) Description() string {
	return "Browse, search, and install skills from the skill marketplace. Supports multiple default sources (Anthropic Skills, OpenAI Skills, ClawHub Community, and a curated builtin catalog). Use action=browse to list available skills, action=search to find skills by keyword, or action=install to install a skill by its installRef (from browse/search results). Installation goes through the same safety-scan and plan flow as install_source."
}

func (SkillMarketTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "action":{"type":"string","enum":["browse","search","install"],"description":"browse lists available skills; search finds skills by keyword; install installs a skill by installRef."},
  "source":{"type":"string","description":"For browse and search: filter to one source (builtin, anthropics, openai, clawhub). Omit for all sources."},
  "query":{"type":"string","description":"For search: the keyword(s) to search for."},
  "installRef":{"type":"string","description":"For install: the installRef from a browse/search result (a URL to the skill's SKILL.md)."},
  "name":{"type":"string","description":"For install: optional skill name override."},
  "scope":{"type":"string","enum":["project","global"],"description":"For install: where to install. Defaults to project."},
  "apply":{"type":"boolean","description":"For install: false (default) returns a plan; true installs immediately."}
},
"required":["action"]
}`)
}

func (SkillMarketTool) ReadOnly() bool { return false }

func (m *SkillMarketTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Action     string `json:"action"`
		Source     string `json:"source"`
		Query      string `json:"query"`
		InstallRef string `json:"installRef"`
		Name       string `json:"name"`
		Scope      string `json:"scope"`
		Apply      bool   `json:"apply"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}

	switch strings.ToLower(p.Action) {
	case "browse":
		return m.browse(ctx, p.Source)
	case "search":
		return m.search(ctx, p.Query, p.Source)
	case "install":
		return m.install(ctx, p.InstallRef, p.Name, p.Scope, p.Apply)
	default:
		return "", fmt.Errorf("unknown action %q (use browse, search, or install)", p.Action)
	}
}

// browse lists skills from one or all default sources.
func (m *SkillMarketTool) browse(ctx context.Context, sourceFilter string) (string, error) {
	sources := DefaultMarketSources()
	if sourceFilter != "" {
		var filtered []MarketSource
		for _, s := range sources {
			if s.ID == sourceFilter {
				filtered = []MarketSource{s}
				break
			}
		}
		if len(filtered) == 0 {
			return "", fmt.Errorf("unknown source %q; available: %s", sourceFilter, sourceIDs(sources))
		}
		sources = filtered
	}
	var b strings.Builder
	for _, src := range sources {
		entries, err := m.installer.FetchCatalog(ctx, src)
		if err != nil {
			fmt.Fprintf(&b, "## %s\n(source unavailable: %v)\n\n", src.Name, err)
			continue
		}
		if len(entries) == 0 {
			fmt.Fprintf(&b, "## %s\n(no skills found)\n\n", src.Name)
			continue
		}
		fmt.Fprintf(&b, "## %s (%d skills)\n", src.Name, len(entries))
		limit := entries
		if len(limit) > 15 {
			limit = limit[:15]
		}
		for _, e := range limit {
			desc := e.Description
			if desc == "" {
				desc = "(no description)"
			}
			if len(desc) > 80 {
				desc = desc[:80] + "…"
			}
			fmt.Fprintf(&b, "- **%s** — %s", e.Name, desc)
			if e.Installs > 0 {
				fmt.Fprintf(&b, " (%d installs)", e.Installs)
			}
			fmt.Fprintf(&b, " `installRef: %s`\n", e.InstallRef)
		}
		if len(entries) > 15 {
			fmt.Fprintf(&b, "- …and %d more (use search to filter)\n", len(entries)-15)
		}
		b.WriteString("\n")
	}
	b.WriteString("To install: use action=install with the installRef, or use install_source directly.\n")
	b.WriteString("To search: use action=search with a query.\n")
	return b.String(), nil
}

// search searches across sources and returns matching skills.
func (m *SkillMarketTool) search(ctx context.Context, query string, source string) (string, error) {
	if strings.TrimSpace(query) == "" {
		// allow empty query to fetch default/trending list
	}
	entries, err := m.installer.SearchCatalog(ctx, query, source)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return fmt.Sprintf("No skills found matching %q across the default sources.\n", query), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d skill(s) matching %q:\n\n", len(entries), query)
	limit := entries
	if len(limit) > 20 {
		limit = limit[:20]
	}
	for _, e := range limit {
		desc := e.Description
		if desc == "" {
			desc = "(no description)"
		}
		if len(desc) > 80 {
			desc = desc[:80] + "…"
		}
		fmt.Fprintf(&b, "- **%s** [%s] — %s", e.Name, e.Source, desc)
		if e.Installs > 0 {
			fmt.Fprintf(&b, " (%d installs)", e.Installs)
		}
		fmt.Fprintf(&b, " `installRef: %s`\n", e.InstallRef)
	}
	if len(entries) > 20 {
		fmt.Fprintf(&b, "\n…and %d more. Refine your search to narrow results.\n", len(entries)-20)
	}
	b.WriteString("\nTo install: use action=install with the installRef.\n")
	return b.String(), nil
}

// install delegates to install_source's plan→apply pipeline by calling its
// Execute method with the right JSON args. This reuses all safety scanning,
// manifest recording, and risk-level logic.
func (m *SkillMarketTool) install(ctx context.Context, installRef, name, scope string, apply bool) (string, error) {
	if strings.TrimSpace(installRef) == "" {
		return "", fmt.Errorf("installRef is required (get it from browse/search results)")
	}
	if scope == "" {
		scope = "project"
	}
	args := map[string]any{
		"op":     "install",
		"source": installRef,
		"kind":   "skill",
		"scope":  scope,
		"apply":  apply,
	}
	if name != "" {
		args["name"] = name
	}
	raw, _ := json.Marshal(args)
	return m.installer.Execute(ctx, raw)
}

// sourceIDs returns a comma-separated list of source IDs for error messages.
func sourceIDs(sources []MarketSource) string {
	var ids []string
	for _, s := range sources {
		ids = append(ids, s.ID)
	}
	return strings.Join(ids, ", ")
}
