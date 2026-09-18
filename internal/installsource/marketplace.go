package installsource

// marketplace.go implements the skill-marketplace data layer (SPEC v2 §3.4C):
// multi-source catalog aggregation, Claude marketplace.json parsing, and an
// offline builtin catalog. It lets the user browse/search skills across several
// default sources (Anthropic/OpenAI GitHub repos, clawhub.ai community API, and
// a builtin curated list) and install any of them — reusing the existing
// install_source plan→apply pipeline (with safety scan + manifest).
//
// Design constraints (SPEC v2 §2.0): zero user learning cost (defaults work out
// of the box; "find me a code-review skill" is enough), zero prompt bloat (this
// is a tool's internal logic, never enters the system prompt).

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// MarketSource describes one catalog source the user can browse/search.
type MarketSource struct {
	ID   string `json:"id"`   // "anthropics" / "openai" / "clawhub" / "builtin"
	Name string `json:"name"` // human label
	Type string `json:"type"` // "github-repo" / "clawhub-api" / "builtin-catalog"
	URL  string `json:"url"`  // repo URL / API base / empty for builtin
}

// DefaultMarketSources returns the built-in default sources, available with no
// user configuration. Multiple sources avoid depending on a single service:
// if clawhub is down, GitHub repos still work; if GitHub rate-limits, the
// builtin catalog is offline-available.
func DefaultMarketSources() []MarketSource {
	return []MarketSource{
		{ID: "builtin", Name: "Curated", Type: "builtin-catalog", URL: ""},
		{ID: "clawhub", Name: "ClawHub Community", Type: "clawhub-api", URL: "https://clawhub.ai"},
		{ID: "anthropic", Name: "Anthropic Skills", Type: "github-repo", URL: "https://github.com/anthropics/skills"},
		{ID: "openai", Name: "OpenAI Skills", Type: "github-repo", URL: "https://github.com/openai/skills"},
		{ID: "mcp-servers", Name: "MCP Servers", Type: "github-repo", URL: "https://github.com/modelcontextprotocol/servers"},
	}
}

// BuiltinCatalog returns the offline curated skill catalog (exported for the
// desktop settings panel to show without network).
func BuiltinCatalog() []CatalogEntry {
	return builtinCatalog
}

// InstalledSkillNames reads the install manifest from both project and global
// scopes and returns a map of skill-name → source-URL. Used by the frontend
// to mark skills as "already installed" in search results.
func InstalledSkillNames(homeDir string) map[string]string {
	result := make(map[string]string)
	for _, root := range []string{
		filepath.Join(homeDir, ".hiq", "skills"),
	} {
		m := loadManifest(root)
		for name, entry := range m.Skills {
			result[name] = entry.Source
		}
	}
	return result
}

// MarketSourceMeta is the desktop-facing view of a market source (no internal
// types leaked).
type MarketSourceMeta struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// DefaultMarketSourceMetas returns the default sources as lightweight metadata
// for the frontend.
func DefaultMarketSourceMetas() []MarketSourceMeta {
	sources := DefaultMarketSources()
	out := make([]MarketSourceMeta, len(sources))
	for i, s := range sources {
		out[i] = MarketSourceMeta{ID: s.ID, Name: s.Name, Type: s.Type}
	}
	return out
}

// CatalogEntry is one installable skill from a market source, in a unified
// cross-source format. This is what browse/search returns and what the agent
// presents to the user.
type CatalogEntry struct {
	Source      string   `json:"source"`           // source ID
	Name        string   `json:"name"`             // skill name (install name)
	Slug        string   `json:"slug"`             // source-native identifier (clawhub slug)
	Author      string   `json:"author,omitempty"` // author handle (clawhub ownerHandle)
	Description string   `json:"description"`      // one-line summary
	Topics      []string `json:"topics"`           // categories/tags
	Installs    int      `json:"installs"`         // download count (clawhub has, GitHub doesn't)
	ContentURL  string   `json:"contentUrl"`       // URL to fetch SKILL.md (install path)
	InstallRef  string   `json:"installRef"`       // value to pass to install_source's `source`
}

// --- Claude marketplace.json parsing ----------------------------------------

// claudeMarketplacePlugin is one entry in a Claude Code marketplace.json's
// plugins[] array. We only extract the fields we need; extra fields are ignored.
type claudeMarketplacePlugin struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Source      json.RawMessage `json:"source"` // string or object
	Skills      []string        `json:"skills"` // skill directories
	Version     string          `json:"version"`
}

type claudeMarketplace struct {
	Name    string                    `json:"name"`
	Owner   json.RawMessage           `json:"owner"` // object, ignored
	Plugins []claudeMarketplacePlugin `json:"plugins"`
}

// fetchMarketplaceJSON fetches and parses a Claude Code marketplace.json from a
// GitHub repo URL. Returns the plugins array. Used by browse (to list a repo's
// skills) and by plan (to install from a marketplace repo).
//
// repoURL may be "github.com/owner/repo" or a full tree URL; we construct the
// raw URL for ".claude-plugin/marketplace.json" and fetch it.
func (t *installSourceTool) fetchMarketplaceJSON(ctx context.Context, repoURL string) (*claudeMarketplace, error) {
	src, ok := parseGitHubRepoSource(repoURL)
	if !ok {
		return nil, newErr(ErrUnsupportedKind, "%s is not a GitHub repo", repoURL)
	}
	branch := "main"
	if len(src.branches()) > 0 {
		branch = src.branches()[0]
	}
	path := joinURLPath(src.Path, ".claude-plugin", "marketplace.json")
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", src.Owner, src.Repo, branch, path)
	body, err := t.fetchText(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	var mp claudeMarketplace
	if err := json.Unmarshal([]byte(body), &mp); err != nil {
		return nil, newErr(ErrInvalidManifest, "marketplace.json parse error: %v", err)
	}
	if len(mp.Plugins) == 0 {
		return nil, newErr(ErrUnsupportedKind, "marketplace.json has no plugins")
	}
	return &mp, nil
}

// marketplaceToCatalog converts a parsed marketplace.json into CatalogEntries.
// Each plugin's skill directories become entries whose ContentURL points to the
// raw SKILL.md on GitHub.
func marketplaceToCatalog(repoURL, sourceID string, mp *claudeMarketplace) []CatalogEntry {
	src, _ := parseGitHubRepoSource(repoURL)
	branch := "main"
	if src.Owner != "" && len(src.branches()) > 0 {
		branch = src.branches()[0]
	}
	var out []CatalogEntry
	for _, p := range mp.Plugins {
		// If the plugin declares skill directories, each is a separate entry.
		if len(p.Skills) > 0 {
			for _, skillDir := range p.Skills {
				name := skillDir
				if idx := strings.LastIndexByte(skillDir, '/'); idx >= 0 {
					name = skillDir[idx+1:]
				}
				rawPath := joinURLPath(src.Path, skillDir, "SKILL.md")
				contentURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", src.Owner, src.Repo, branch, rawPath)
				out = append(out, CatalogEntry{
					Source:      sourceID,
					Name:        name,
					Description: p.Description,
					Topics:      []string{},
					ContentURL:  contentURL,
					InstallRef:  contentURL,
				})
			}
			continue
		}
		// Plugin with no skill dirs: treat the plugin itself as one entry.
		rawPath := joinURLPath(src.Path, p.Name, "SKILL.md")
		contentURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", src.Owner, src.Repo, branch, rawPath)
		out = append(out, CatalogEntry{
			Source:      sourceID,
			Name:        p.Name,
			Description: p.Description,
			Topics:      []string{},
			ContentURL:  contentURL,
			InstallRef:  contentURL,
		})
	}
	return out
}

// --- Builtin curated catalog -------------------------------------------------

// builtinCatalog is a small set of well-known skills indexed at compile time so
// the user can browse offline (content is fetched on install from GitHub raw).
// Curated from PromptHub's BUILTIN_SKILL_REGISTRY (Anthropic + OpenAI + community).
var builtinCatalog = []CatalogEntry{

	{Source: "builtin", Name: "skill-creator", Description: "Create new skills, edit existing skills, iterate wording", Topics: []string{"meta"}, ContentURL: "https://raw.githubusercontent.com/anthropics/skills/main/skill-creator/SKILL.md", InstallRef: "https://raw.githubusercontent.com/anthropics/skills/main/skill-creator/SKILL.md"},
	{Source: "builtin", Name: "artifacts-builder", Description: "Build interactive React artifacts for the web", Topics: []string{"frontend", "design"}, ContentURL: "https://raw.githubusercontent.com/anthropics/skills/main/artifacts-builder/SKILL.md", InstallRef: "https://raw.githubusercontent.com/anthropics/skills/main/artifacts-builder/SKILL.md"},
	{Source: "builtin", Name: "mcp-builder", Description: "Build MCP servers with TypeScript or Python", Topics: []string{"meta", "integration"}, ContentURL: "https://raw.githubusercontent.com/anthropics/skills/main/mcp-builder/SKILL.md", InstallRef: "https://raw.githubusercontent.com/anthropics/skills/main/mcp-builder/SKILL.md"},
	{Source: "builtin", Name: "webapp-testing", Description: "Test web apps with Playwright browser automation", Topics: []string{"testing", "frontend"}, ContentURL: "https://raw.githubusercontent.com/openai/skills/main/skills/.curated/webapp-testing/SKILL.md", InstallRef: "https://raw.githubusercontent.com/openai/skills/main/skills/.curated/webapp-testing/SKILL.md"},
	{Source: "builtin", Name: "gh-fix-ci", Description: "Fix failing CI/CD pipelines on GitHub Actions", Topics: []string{"devops", "ci"}, ContentURL: "https://raw.githubusercontent.com/openai/skills/main/skills/.curated/gh-fix-ci/SKILL.md", InstallRef: "https://raw.githubusercontent.com/openai/skills/main/skills/.curated/gh-fix-ci/SKILL.md"},
}

// builtinMcpCatalog is a curated list of official and popular MCP servers.
var builtinMcpCatalog = []CatalogEntry{
	{Source: "builtin", Name: "sqlite", Description: "Database interaction and querying for SQLite databases.", Topics: []string{"database", "sqlite"}, InstallRef: "npx -y @modelcontextprotocol/server-sqlite"},
	{Source: "builtin", Name: "filesystem", Description: "Securely read and write files to the local file system.", Topics: []string{"system", "files"}, InstallRef: "npx -y @modelcontextprotocol/server-filesystem"},
	{Source: "builtin", Name: "github", Description: "Interact with GitHub API: read repos, PRs, issues.", Topics: []string{"github", "vcs"}, InstallRef: "npx -y @modelcontextprotocol/server-github"},
	{Source: "builtin", Name: "puppeteer", Description: "Browser automation for web scraping and interaction.", Topics: []string{"web", "browser"}, InstallRef: "npx -y @modelcontextprotocol/server-puppeteer"},
	{Source: "builtin", Name: "brave-search", Description: "Web search and summarization using Brave Search API.", Topics: []string{"search", "web"}, InstallRef: "npx -y @modelcontextprotocol/server-brave-search"},
	{Source: "builtin", Name: "postgres", Description: "Database interaction and querying for PostgreSQL databases.", Topics: []string{"database", "postgres"}, InstallRef: "npx -y @modelcontextprotocol/server-postgres"},
}

// marketplaceActions builds install actions from a Claude marketplace.json by
// downloading each plugin's SKILL.md content and running it through skillAction
// (which applies the safety scan + risk classification). Called by tryGitHubRepo
// when a marketplace.json is found, so installing from a marketplace repo is
// identical to installing individual skills — same safety, same manifest.
func (t *installSourceTool) marketplaceActions(ctx context.Context, req request, src githubRepoSource, branch string, mp *claudeMarketplace) []action {
	entries := marketplaceToCatalog(req.Source, "marketplace", mp)
	var actions []action
	for _, e := range entries {
		// Fetch the SKILL.md content for this entry.
		body, err := t.fetchText(ctx, e.ContentURL)
		if err != nil {
			continue
		}
		cand, err := parseSkillContent(body, e.Name, e.ContentURL, req.strict())
		if err != nil {
			continue
		}
		actions = append(actions, t.skillAction(req, cand, "copy"))
	}
	return actions
}

// --- Cross-source aggregation ------------------------------------------------

// FetchCatalog fetches a browsable skill catalog from one source.
func (t *installSourceTool) FetchCatalog(ctx context.Context, src MarketSource) ([]CatalogEntry, error) {
	switch src.Type {
	case "builtin-catalog":
		return builtinCatalog, nil
	case "clawhub-api":
		entries, _, err := t.clawhubList(ctx, 50, "")
		return entries, err
	case "github-repo":
		if mp, err := t.fetchMarketplaceJSON(ctx, src.URL); err == nil {
			return marketplaceToCatalog(src.URL, src.ID, mp), nil
		}
		return t.catalogFromGitHubRepo(ctx, src)
	default:
		return nil, newErr(ErrUnsupportedKind, "unsupported source type %q", src.Type)
	}
}

// FetchMcpCatalog returns the curated MCP catalog
func (t *installSourceTool) FetchMcpCatalog(ctx context.Context, src MarketSource) ([]CatalogEntry, error) {
	switch src.Type {
	case "builtin-catalog":
		return builtinMcpCatalog, nil
	default:
		return []CatalogEntry{}, nil
	}
}

// BuiltinMcpCatalog returns the compiled curated list of MCP servers.
func BuiltinMcpCatalog() []CatalogEntry {
	return builtinMcpCatalog
}

// catalogFromGitHubRepo scans a GitHub repo for SKILL.md files and returns them
// as catalog entries. Reuses the existing scanGitHubSkills logic.
func (t *installSourceTool) catalogFromGitHubRepo(ctx context.Context, src MarketSource) ([]CatalogEntry, error) {
	gsrc, ok := parseGitHubRepoSource(src.URL)
	if !ok {
		return nil, newErr(ErrUnsupportedKind, "%s is not a GitHub repo", src.URL)
	}
	var out []CatalogEntry
	for _, branch := range gsrc.branches() {
		cands, _, err := t.scanGitHubSkills(ctx, request{Kind: "skill"}, gsrc, branch)
		if err != nil {
			continue
		}
		for _, c := range cands {
			desc := c.Description
			if len(desc) > 120 {
				desc = desc[:120] + "…"
			}
			out = append(out, CatalogEntry{
				Source:      src.ID,
				Name:        c.Name,
				Description: desc,
				Topics:      []string{},
				ContentURL:  c.SourcePath,
				InstallRef:  c.SourcePath,
			})
		}
		if len(out) > 0 {
			break
		}
	}
	return out, nil
}

// SearchCatalog searches across all default sources and returns matching entries.
// The search is a simple case-insensitive substring match on name + description
// + topics. For clawhub, it delegates to the server-side search API for better
// recall; other sources are filtered locally after fetching.
func (t *installSourceTool) SearchCatalog(ctx context.Context, query string, filterSource string) ([]CatalogEntry, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, nil
	}

	sources := DefaultMarketSources()
	if filterSource != "" {
		var filtered []MarketSource
		for _, s := range sources {
			if s.ID == filterSource {
				filtered = []MarketSource{s}
				break
			}
		}
		sources = filtered
	}

	type result struct {
		entries []CatalogEntry
		err     error
	}
	resChan := make(chan result, len(sources))

	for _, src := range sources {
		go func(s MarketSource) {
			var entries []CatalogEntry
			var err error
			if s.Type == "clawhub-api" {
				entries, err = t.clawhubSearch(ctx, query)
			} else {
				entries, err = t.FetchCatalog(ctx, s)
			}
			if err == nil {
				var filtered []CatalogEntry
				for _, e := range entries {
					if s.Type == "clawhub-api" {
						filtered = append(filtered, e)
					} else if catalogMatches(e, query) {
						filtered = append(filtered, e)
					}
				}
				resChan <- result{entries: filtered, err: nil}
			} else {
				resChan <- result{entries: nil, err: err}
			}
		}(src)
	}

	var results []CatalogEntry
	for i := 0; i < len(sources); i++ {
		res := <-resChan
		if res.err == nil && res.entries != nil {
			results = append(results, res.entries...)
		}
	}

	// Sort by installs descending (clawhub entries first), then name.
	for i := 1; i < len(results); i++ {
		for j := i; j > 0; j-- {
			if results[j].Installs > results[j-1].Installs ||
				(results[j].Installs == results[j-1].Installs && results[j].Name < results[j-1].Name) {
				results[j], results[j-1] = results[j-1], results[j]
			}
		}
	}
	return results, nil
}

// catalogMatches reports whether an entry matches the query in name/description/topics.
func catalogMatches(e CatalogEntry, query string) bool {
	if query == "" {
		return true
	}
	if strings.Contains(strings.ToLower(e.Name), query) {
		return true
	}
	if strings.Contains(strings.ToLower(e.Description), query) {
		return true
	}
	for _, t := range e.Topics {
		if strings.Contains(strings.ToLower(t), query) {
			return true
		}
	}
	return false
}
