package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zzycxz/hiq/internal/rag"
	"github.com/zzycxz/hiq/internal/tool"
)

// RAG knowledge-base tools (Phase 3 of coWork). Wrap a process-global rag.Store
// so the agent can import documents into named collections and search them. The
// store persists to the user config dir; search is FTS5 (full-text, CJK-aware)
// in Phase 3 — an embedding/vector layer can be added behind rag_search later.
//
// The store is injected via SetRAGStore (boot.go, cowork). When nil, the tools
// return a clear "offline" error.

var (
	globalRAGStore *rag.Store
)

// SetRAGStore injects the knowledge-base store. Called once at cowork boot.
func SetRAGStore(s *rag.Store) { globalRAGStore = s }

// SetRAGEmbedder injects an embedder for hybrid (FTS5 + semantic) reranking.
// Called at boot when [cowork] embedding_model is configured; nil = FTS5-only.
func SetRAGEmbedder(e rag.Embedder) { globalRAGEmbedder = e }

var globalRAGEmbedder rag.Embedder

// globalRAGLLM is the LLM-driven semantic layer (query expansion + rerank),
// reusing the user's already-configured provider. nil = off (plain FTS5).
var globalRAGLLM *rag.LLMSemantic

// SetRAGLLM injects the LLM semantic layer (SPEC v2 §3.6). Called at boot when
// a provider is available; nil = FTS5-only (graceful degradation). The layer
// is automatic — the user never configures it, it just improves recall/precision
// using the provider key they already set up.
func SetRAGLLM(l *rag.LLMSemantic) { globalRAGLLM = l }

// globalRAGSessionResolver returns the session's active collections so that
// rag_search can auto-scope when the caller omits the collection parameter.
// Injected via SetRAGSessionResolver (desktop app.go). nil = no session scope
// (search all collections, the original behavior).
var globalRAGSessionResolver func() []string

// SetRAGSessionResolver injects a callback that returns the session's active
// collections. This bridges the desktop UI's "activate collection" control to
// the agent's rag_search calls — without it, the UI selection has no effect on
// LLM-driven searches. Called once at cowork boot.
func SetRAGSessionResolver(fn func() []string) { globalRAGSessionResolver = fn }

// autoSearchMaxChars is the soft budget for RAG context injected into each user
// message. Items (entities, snippets) are added one by one; when the budget is
// reached, remaining items are omitted entirely (not truncated mid-sentence).
// ~3000 chars ≈ 1500-2000 tokens — bounded so it never eats a disproportionate
// chunk of the context window or triggers premature compaction.
const autoSearchMaxChars = 3000

// AutoSearch performs a lightweight knowledge-base retrieval for auto-injection
// into the main chat. Returns a formatted context string (entities + snippets)
// or "" when there are no matches or the store is offline. This lets the
// controller prepend knowledge-base context to user messages without exposing
// the rag_search tool to the main agent loop.
//
// collection is the knowledge-base collection to search (the user's Composer
// "知识库" dropdown selection). It MUST be non-empty — an empty collection
// means the user opted out ("不使用") and AutoSearch returns "" immediately.
// The controller gate enforces the same rule before calling, but AutoSearch
// defends in depth so direct callers stay correct. resolveRAGScope is NOT
// consulted here (that resolver serves the rag_search tool, not auto-injection)
// so a single active session collection can never trigger surprise injection.
func AutoSearch(ctx context.Context, query, collection string) string {
	if globalRAGStore == nil {
		return ""
	}
	// Empty collection = user opted out. Don't fall back to resolveRAGScope —
	// that resolver serves rag_search (where a single active collection is a
	// reasonable default), not auto-injection (which must stay opt-in).
	collection = strings.TrimSpace(collection)
	if collection == "" {
		return ""
	}
	// Skip injection for very short queries (likely conversational, not
	// knowledge-seeking). Avoids wasting context on "你好" / "谢谢" etc.
	q := strings.TrimSpace(query)
	if len([]rune(q)) < 2 {
		return ""
	}
	hasEntities, _ := globalRAGStore.HasEntities(collection)
	const topK = 5
	const maxDescRunes = 60

	var b strings.Builder
	budget := autoSearchMaxChars

	// tryWrite appends a line only if the remaining budget allows it; returns
	// false if the line was skipped to stay within budget. This guarantees no
	// entry is truncated mid-sentence — it's either fully included or omitted.
	tryWrite := func(format string, args ...any) bool {
		line := fmt.Sprintf(format, args...)
		if len([]rune(line)) > budget {
			return false
		}
		b.WriteString(line)
		budget -= len([]rune(line))
		return true
	}

	// Layer 1: structured entities (if deep-extracted).
	entityShown := 0
	if hasEntities {
		entities, err := globalRAGStore.SearchEntities(query, collection, topK)
		if err == nil && len(entities) > 0 {
			tryWrite("知识库命中实体：\n")
			for _, e := range entities {
				desc := e.Description
				if dr := []rune(desc); len(dr) > maxDescRunes {
					// Trim at nearest space boundary to avoid cutting mid-word.
					desc = string(dr[:maxDescRunes])
					if sp := strings.LastIndex(desc, " "); sp > maxDescRunes/2 {
						desc = desc[:sp]
					}
					desc += "…"
				}
				if !tryWrite("- %s [%s] %s\n", e.NameRaw, e.Type, desc) {
					break
				}
				entityShown++
			}
			omitted := len(entities) - entityShown
			if omitted > 0 {
				tryWrite("（另有 %d 条实体因篇幅省略）\n", omitted)
			}
			tryWrite("\n")
		}
	}

	// Layer 2: FTS5 original-text snippets. When the LLM semantic layer is
	// available, expand the query first (synonyms/stems/cross-language) to
	// improve recall, then rerank the over-fetched pool for precision.
	expandedQuery := query
	overFetch := topK
	if globalRAGLLM != nil {
		if terms := globalRAGLLM.ExpandQuery(ctx, query); len(terms) > 1 {
			expandedQuery = strings.Join(terms, " OR ")
		}
		overFetch = topK * 4
		if overFetch < 20 {
			overFetch = 20
		}
	}
	results, err := globalRAGStore.Search(expandedQuery, collection, overFetch)
	if err == nil && len(results) > 0 {
		// Rerank: prefer the embedding layer when available (cheaper — vector
		// cosine, no LLM call, no candidate cap); fall back to the LLM reranker.
		// Query expansion above still uses the LLM, so the hybrid pipeline is
		// LLM expands (recall) → embeddings precision-rank (or LLM if no embedder).
		if globalRAGEmbedder != nil {
			results = globalRAGStore.Rerank(ctx, query, results, globalRAGEmbedder, 0.5)
			if len(results) > topK {
				results = results[:topK]
			}
		} else if globalRAGLLM != nil {
			results = globalRAGLLM.Rerank(ctx, query, results)
			if len(results) > topK {
				results = results[:topK]
			}
		}
		// Relative score filter: drop results scoring below 20% of the best
		// match. BM25 absolute scores aren't comparable across queries, so a
		// fixed threshold doesn't work — but a ratio within one result set does.
		if len(results) > 1 && results[0].Score > 0 {
			cutoff := results[0].Score * 0.2
			filtered := results[:1]
			for _, r := range results[1:] {
				if r.Score >= cutoff {
					filtered = append(filtered, r)
				}
			}
			results = filtered
		}
		tryWrite("知识库文档片段：\n")
		snippetShown := 0
		for _, r := range results {
			snippet := r.Snippet
			if snippet == "" {
				continue
			}
			label := filepath.Base(r.Path)
			if !tryWrite("【%s】%s\n\n", label, snippet) {
				break
			}
			snippetShown++
		}
		omitted := len(results) - snippetShown
		if omitted > 0 {
			tryWrite("（另有 %d 条片段因篇幅省略）\n", omitted)
		}
	}

	return b.String()
}

// resolveRAGScope returns the effective collection for a rag_search call: an
// explicit collection parameter always wins; otherwise, if the session has
// exactly one active collection, use it; otherwise "" (all collections).
// Note: this resolver serves the rag_search TOOL only. Auto-injection
// (AutoSearch) does NOT consult it — it requires an explicit non-empty
// collection so injection stays strictly opt-in.
func resolveRAGScope(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if globalRAGSessionResolver != nil {
		if active := globalRAGSessionResolver(); len(active) == 1 {
			return active[0]
		}
	}
	return ""
}

func requireRAG() (*rag.Store, error) {
	if globalRAGStore == nil {
		return nil, errors.New("RAG store is offline (knowledge base not enabled)")
	}
	return globalRAGStore, nil
}

// refuseNetDevNamespace enforces NETDEV_SPEC §7.2 from the cowork surface: the
// ops knowledge namespace ("netdev:*" collections) is invisible to dev/cowork
// sessions — searching, listing, importing into, or deleting it is refused.
func refuseNetDevNamespace(collection string) error {
	if rag.IsNetDevCollection(collection) {
		return fmt.Errorf("collection %q belongs to the ops (netdev) knowledge namespace and is not visible in this mode", collection)
	}
	return nil
}

// RAGTools returns the knowledge-base tools for cowork registration.
func RAGTools() []tool.Tool {
	return []tool.Tool{ragImport{}, ragSearch{}, ragGraph{}, ragMindMap{}, ragList{}, ragDelete{}}
}

// --- rag_import -------------------------------------------------------------

type ragImport struct{}

func (ragImport) Name() string { return "rag_import" }

func (ragImport) Description() string {
	return "Import a document into a named knowledge-base collection so it can be searched with rag_search. Supports text-like formats (txt, md, markdown, code, csv, json, html) — binary Office formats (docx/xlsx/pdf) must be converted to text first. Re-importing the same path replaces its content. The document is split into chunks for focused search snippets. Collection defaults to \"default\"; use names to group documents (e.g. \"product-specs\", \"meeting-notes\")."
}

func (ragImport) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Absolute path to the document file to import"},
  "collection":{"type":"string","description":"Collection name (default \"default\")"},
  "tags":{"type":"array","items":{"type":"string"},"description":"Deprecated/ignored. Reserved; pass null."}
},
"required":["path"]
}`)
}

func (ragImport) ReadOnly() bool { return false }

func (ragImport) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path       string   `json:"path"`
		Collection string   `json:"collection"`
		Tags       []string `json:"tags"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return "", errors.New("path is required")
	}
	abs, err := filepath.Abs(p.Path)
	if err != nil {
		return "", err
	}
	if p.Collection == "" {
		p.Collection = "default"
	}
	if err := refuseNetDevNamespace(p.Collection); err != nil {
		return "", err
	}
	s, err := requireRAG()
	if err != nil {
		return "", err
	}
	chunks, err := s.Import(p.Collection, abs, p.Tags)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("imported %s into collection %q (%d chunks)", abs, p.Collection, chunks), nil
}

// --- rag_search -------------------------------------------------------------

type ragSearch struct{}

func (ragSearch) Name() string { return "rag_search" }

func (ragSearch) Description() string {
	return "Search the knowledge base for documents matching a query. Returns two kinds of hits merged together: (1) structured entities + their relations (when the collection has been deep-extracted — high-value facts with no chunk-boundary noise), and (2) FTS5 original-text snippets (always available, for quotable source text). Scoped to one collection when set, else searches all. Use top_k to cap each layer (default 5). When the query looks like a name or a relation question (\"who is X\", \"X 负责 什么\"), the structured layer is the authoritative answer; the FTS5 layer backs it up with citations. Every structured hit is annotated with its provenance (source file + chunk), so you can cite where a fact came from; and when a hit is a topic/event, its members are expanded inline (\"成员：...\") so one hit reveals the whole group."
}

func (ragSearch) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "query":{"type":"string","description":"Search terms (a name, a relation question, or keywords)"},
  "collection":{"type":"string","description":"Limit to one collection (empty = all)"},
  "top_k":{"type":"integer","description":"Max results per layer (default 5)"}
},
"required":["query"]
}`)
}

func (ragSearch) ReadOnly() bool { return true }

func (ragSearch) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query      string `json:"query"`
		Collection string `json:"collection"`
		TopK       int    `json:"top_k"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Query) == "" {
		return "", errors.New("query is required")
	}
	if p.TopK <= 0 {
		p.TopK = 5
	}
	s, err := requireRAG()
	if err != nil {
		return "", err
	}
	// Resolve the effective collection scope: an explicit parameter wins,
	// otherwise fall back to the session's active collection (if exactly one
	// is active). This bridges the UI "activate collection" control to the
	// agent's rag_search calls.
	collection := resolveRAGScope(p.Collection)
	// NETDEV_SPEC §7.2: the ops knowledge namespace is invisible to the
	// cowork surface — refuse an explicitly-named netdev: collection (the
	// store already excludes it from empty-scope searches).
	if err := refuseNetDevNamespace(collection); err != nil {
		return "", err
	}
	return runRAGSearch(ctx, s, p.Query, collection, p.TopK)
}

// runRAGSearch is the shared two-layer search body behind rag_search and the
// netdev namespace's rag_search variant: structured entities/relations (when
// deep-extracted) + FTS5 snippets (with semantic rerank when configured).
// collection is the ALREADY-resolved scope; the §7.2 namespace boundary is
// enforced by the store (empty scope excludes it) and by the callers.
func runRAGSearch(ctx context.Context, s *rag.Store, query, collection string, topK int) (string, error) {
	p := struct {
		Query string
		TopK  int
	}{Query: query, TopK: topK}
	// Layer 1: structured entities + relations (only if this collection has been
	// deep-extracted). This is the high-precision layer — direct fact hits with
	// no chunk-boundary noise.
	hasEntities, _ := s.HasEntities(collection)
	var entities []rag.Entity
	var relations []rag.Relation
	if hasEntities {
		es, err := s.SearchEntities(p.Query, collection, p.TopK)
		if err != nil {
			return "", err
		}
		entities = es
		for _, e := range entities {
			rels, _ := s.RelationsOf(collection, e.Name, true)
			relations = append(relations, rels...)
		}
	}

	// Layer 2: FTS5 original-text snippets (always available; the quotable
	// source-text layer). Over-fetch + rerank when a semantic layer is present
	// (LLM preferred; embedding fallback). Query expansion improves recall
	// before reranking sharpens precision.
	expandedQuery := p.Query
	pool := p.TopK
	if globalRAGLLM != nil {
		if terms := globalRAGLLM.ExpandQuery(ctx, p.Query); len(terms) > 1 {
			expandedQuery = strings.Join(terms, " OR ")
		}
		pool = p.TopK * 4
		if pool < 20 {
			pool = 20
		}
	} else if globalRAGEmbedder != nil {
		pool = p.TopK * 4
		if pool < 20 {
			pool = 20
		}
	}
	results, err := s.Search(expandedQuery, collection, pool)
	if err != nil {
		return "", err
	}
	// Prefer the embedding reranker when available (cheaper, no LLM call); fall
	// back to the LLM reranker. The LLM still owns query expansion above, so the
	// hybrid pipeline keeps recall (LLM expand) + precision (embedding cosine).
	if globalRAGEmbedder != nil && len(results) > 0 {
		results = s.Rerank(ctx, p.Query, results, globalRAGEmbedder, 0.5)
		if len(results) > p.TopK {
			results = results[:p.TopK]
		}
	} else if globalRAGLLM != nil && len(results) > 0 {
		results = globalRAGLLM.Rerank(ctx, p.Query, results)
		if len(results) > p.TopK {
			results = results[:p.TopK]
		}
	}

	var b strings.Builder
	scope := "all collections"
	if collection != "" {
		scope = fmt.Sprintf("%q", collection)
	}
	if len(entities) == 0 && len(results) == 0 {
		return fmt.Sprintf("no matches in %s — import documents with rag_import first", scope), nil
	}

	// Structured layer first (higher value for fact questions). Each entity and
	// relation is annotated with its provenance (the source files/chunks it was
	// extracted from) so the agent can cite where a fact came from — this is the
	// data the DB already stores (Sources), now surfaced instead of hidden.
	if len(entities) > 0 {
		fmt.Fprintf(&b, "结构化命中（%d 个实体，%d 条关系）in %s：\n", len(entities), len(relations), scope)
		sourceFiles := map[string]bool{} // for the provenance summary at the end
		for _, e := range entities {
			fmt.Fprintf(&b, "- %s [%s]", e.NameRaw, e.Type)
			if e.Description != "" {
				fmt.Fprintf(&b, " · %s", e.Description)
			}
			if lbl := sourceLabel(e.Sources); lbl != "" {
				fmt.Fprintf(&b, " · 来源：%s", lbl)
				for _, s := range e.Sources {
					sourceFiles[filepath.Base(s.Path)] = true
				}
			}
			b.WriteString("\n")
			// Topic/event expansion (cog_rag-style): a topic entity acts as a
			// hub connecting its members via member_of/part_of relations. Surface
			// the membership as a compact "成员：" line so a hit on the topic
			// reveals everyone/thing it aggregates — "by point to face" retrieval.
			if isTopicType(e.Type) {
				var members []string
				for _, r := range relations {
					if r.Target == e.Name && (r.Type == "member_of" || r.Type == "part_of" || r.Type == "属于") {
						members = append(members, r.Source)
					}
				}
				if len(members) > 0 {
					fmt.Fprintf(&b, "    成员：%s\n", strings.Join(dedupStrings(members), "、"))
				}
			}
			for _, r := range relations {
				if r.Source == e.Name {
					// member_of/part_of into a topic is already shown above as
					// "成员"; skip re-printing it as a plain relation to avoid noise.
					if isTopicType(e.Type) && (r.Type == "member_of" || r.Type == "part_of") {
						continue
					}
					fmt.Fprintf(&b, "    关系：%s --[%s]--> %s", r.Source, r.Type, r.Target)
					if r.Description != "" {
						fmt.Fprintf(&b, "（%s）", r.Description)
					}
					if lbl := sourceLabel(r.Sources); lbl != "" {
						fmt.Fprintf(&b, " · 来源：%s", lbl)
					}
					b.WriteString("\n")
				} else if r.Target == e.Name {
					fmt.Fprintf(&b, "    关系：%s --[%s]--> %s（反向）", r.Source, r.Type, r.Target)
					if r.Description != "" {
						fmt.Fprintf(&b, "（%s）", r.Description)
					}
					if lbl := sourceLabel(r.Sources); lbl != "" {
						fmt.Fprintf(&b, " · 来源：%s", lbl)
					}
					b.WriteString("\n")
				}
			}
		}
		// Provenance summary: the distinct source files behind these hits, so the
		// agent can answer "where did these facts come from" without scanning
		// every line (mirrors HE's additional_kwargs retrieval-provenance idea).
		if len(sourceFiles) > 0 {
			files := make([]string, 0, len(sourceFiles))
			for f := range sourceFiles {
				files = append(files, f)
			}
			sort.Strings(files)
			fmt.Fprintf(&b, "溯源文件：%s\n", strings.Join(files, "、"))
		}
		if len(results) > 0 {
			b.WriteString("\n")
		}
	}
	// FTS5 layer.
	if len(results) > 0 {
		fmt.Fprintf(&b, "原文命中（%d 段）", len(results))
		if collection != "" {
			fmt.Fprintf(&b, " in %q", collection)
		}
		b.WriteString("：\n")
		for _, r := range results {
			fmt.Fprintf(&b, "- %s [%s · chunk %d · score %.3f]\n  %s\n", r.Path, r.Collection, r.Chunk, r.Score, r.Snippet)
		}
	}
	// Imported documents are external content (could carry prompt-injection
	// text from their source); wrap so the model treats snippets as data.
	return WrapUntrusted("rag", capOutput(b.String())), nil
}

// ragMaxOutputChars caps the total output of rag_search/rag_graph so a single
// call can't flood the model's context with an oversized result (each entity
// recursively pulls all its relations, and topic/event members expand inline).
// When truncating, we append a marker so the model knows results were trimmed.
const ragMaxOutputChars = 12000

func capOutput(s string) string {
	if len(s) <= ragMaxOutputChars {
		return s
	}
	// Truncate at a safe boundary and note how much was dropped.
	return s[:ragMaxOutputChars] + "\n…（结果过长，已截断；可用更具体的 query 或更小的 top_k 缩小范围）"
}

// --- rag_graph --------------------------------------------------------------

type ragGraph struct{}

func (ragGraph) Name() string { return "rag_graph" }

func (ragGraph) Description() string {
	return "Query the structured knowledge graph (entities + relations) of a collection directly, without FTS5 text snippets. Use when you want pure facts/relationships (e.g. \"list everyone who reports to 张三\", \"what does project Alpha relate to\"). Returns entities matching the query plus all their relations (both directions). Requires the collection to have been deep-extracted; returns 'no entities' otherwise (fall back to rag_search for FTS5-only collections)."
}

func (ragGraph) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "query":{"type":"string","description":"Entity name or keyword to look up"},
  "collection":{"type":"string","description":"Limit to one collection (empty = all)"},
  "top_k":{"type":"integer","description":"Max entities to return (default 10)"}
},
"required":["query"]
}`)
}

func (ragGraph) ReadOnly() bool { return true }

func (ragGraph) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query      string `json:"query"`
		Collection string `json:"collection"`
		TopK       int    `json:"top_k"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Query) == "" {
		return "", errors.New("query is required")
	}
	if p.TopK <= 0 {
		p.TopK = 10
	}
	s, err := requireRAG()
	if err != nil {
		return "", err
	}
	if err := refuseNetDevNamespace(p.Collection); err != nil {
		return "", err
	}
	has, _ := s.HasEntities(p.Collection)
	if !has {
		return "no entities in this collection — run deep extraction first (or use rag_search for FTS5-only)", nil
	}
	entities, err := s.SearchEntities(p.Query, p.Collection, p.TopK)
	if err != nil {
		return "", err
	}
	if len(entities) == 0 {
		return "no matching entities", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d entit(ies)", len(entities))
	if p.Collection != "" {
		fmt.Fprintf(&b, " in %q", p.Collection)
	}
	b.WriteString(":\n")
	for _, e := range entities {
		fmt.Fprintf(&b, "- %s [%s]", e.NameRaw, e.Type)
		if e.Description != "" {
			fmt.Fprintf(&b, " · %s", e.Description)
		}
		b.WriteString("\n")
		rels, _ := s.RelationsOf(p.Collection, e.Name, true)
		for _, r := range rels {
			arrow := "-->"
			if r.Target == e.Name {
				arrow = "<--"
			}
			fmt.Fprintf(&b, "    %s %s[%s]%s %s", r.Source, arrow, r.Type, arrow, r.Target)
			if r.Description != "" {
				fmt.Fprintf(&b, "（%s）", r.Description)
			}
			b.WriteString("\n")
		}
	}
	// Entity/relation descriptions originate from imported documents — same
	// untrusted-content fence as rag_search.
	return WrapUntrusted("rag", capOutput(b.String())), nil
}

type ragMindMap struct{}

func (ragMindMap) Name() string { return "rag_mindmap" }

func (ragMindMap) Description() string {
	return "Generate a mind map file from a collection's extracted knowledge graph, rooted at one entity. Walks the entity→relation graph outward from `root` up to `depth` levels, compiling each connected entity into a branch (relation type becomes the branch label). Output .md (Markdown, markmap/Obsidian-friendly) or .html (self-contained interactive SVG). Use to visualize 'who/what relates to X' as a tree. Requires the collection to have been deep-extracted."
}

func (ragMindMap) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "root":{"type":"string","description":"Entity name to use as the mind-map root (normalized or raw form both work)"},
  "collection":{"type":"string","description":"Collection to read from (empty = all)"},
  "path":{"type":"string","description":"Output path (.md or .html decides format)"},
  "depth":{"type":"integer","description":"Max graph-walk depth from root (default 3, capped at 5 to bound the tree)"},
  "format":{"type":"string","description":"\"md\" | \"html\" (default: infer from path ext)"}
},
"required":["root","path"]
}`)
}

func (ragMindMap) ReadOnly() bool { return false }

func (ragMindMap) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Root       string `json:"root"`
		Collection string `json:"collection"`
		Path       string `json:"path"`
		Depth      int    `json:"depth"`
		Format     string `json:"format"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Root) == "" {
		return "", errors.New("root entity is required")
	}
	if strings.TrimSpace(p.Path) == "" {
		return "", errors.New("output path is required")
	}
	if p.Depth <= 0 {
		p.Depth = 3
	}
	if p.Depth > 5 {
		p.Depth = 5 // bound the tree (a 5-deep relation graph is already huge)
	}
	s, err := requireRAG()
	if err != nil {
		return "", err
	}
	if err := refuseNetDevNamespace(p.Collection); err != nil {
		return "", err
	}
	has, _ := s.HasEntities(p.Collection)
	if !has {
		return "no entities in this collection — run deep extraction first", nil
	}
	// Normalize the root to match how entities are keyed (lower+trim).
	rootKey := normalizeRAGName(p.Root)
	// Build the tree by walking relations outward, tracking visited nodes so
	// cycles (A→B→A) don't loop forever.
	visited := map[string]bool{rootKey: true}
	branches := buildRAGBranches(s, p.Collection, rootKey, p.Depth, visited)
	abs, err := filepath.Abs(p.Path)
	if err != nil {
		return "", err
	}
	in := MMInput{Path: abs, Title: p.Root, Branches: branches, Format: p.Format}
	format, err := writeMindMap(in)
	if err != nil {
		return "", err
	}
	return describeMindMapOutput(abs, format), nil
}

// buildRAGBranches walks the relation graph outward from `name`, returning
// mind-map branches. Each connected entity becomes an MMNode whose Text is the
// relation type + target, and which recurses into the target's relations.
// `visited` prevents revisiting nodes (graph cycles + diamond shapes).
func buildRAGBranches(s *rag.Store, collection, name string, depth int, visited map[string]bool) []MMNode {
	if depth <= 0 {
		return nil
	}
	rels, err := s.RelationsOf(collection, name, true)
	if err != nil {
		return nil
	}
	var branches []MMNode
	for _, r := range rels {
		// Determine the OTHER endpoint (the one that isn't `name`).
		other := r.Target
		label := "[" + r.Type + "] " + r.Target
		if r.Source == name {
			other = r.Target
			label = "[" + r.Type + "] " + r.Target
		} else if r.Target == name {
			other = r.Source
			label = "[" + r.Type + "←] " + r.Source
		}
		otherKey := normalizeRAGName(other)
		if visited[otherKey] {
			// Already shown elsewhere — record the link but don't recurse
			// (avoids cycles). Keep it as a leaf with a note.
			branches = append(branches, MMNode{Text: label, Note: "见上文"})
			continue
		}
		visited[otherKey] = true
		node := MMNode{Text: label, Children: buildRAGBranches(s, collection, otherKey, depth-1, visited)}
		branches = append(branches, node)
	}
	return branches
}

// normalizeRAGName mirrors rag.normalizeName (lower+trim) so we can key the
// visited set without exporting the rag helper.
func normalizeRAGName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// --- rag_list ---------------------------------------------------------------

type ragList struct{}

func (ragList) Name() string { return "rag_list" }

func (ragList) Description() string {
	return "List knowledge-base collections with their document/chunk counts and indexed size. Pass a collection name to inspect one; omit for all. Use to see what's imported before searching."
}

func (ragList) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "collection":{"type":"string","description":"One collection to inspect (empty = all)"}
},
"required":[]
}`)
}

func (ragList) ReadOnly() bool { return true }

func (ragList) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Collection string `json:"collection"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &p)
	}
	if err := refuseNetDevNamespace(p.Collection); err != nil {
		return "", err
	}
	s, err := requireRAG()
	if err != nil {
		return "", err
	}
	cols, err := s.List(p.Collection)
	if err != nil {
		return "", err
	}
	// §7.2: the ops namespace never shows up in the cowork collection list.
	filtered := cols[:0]
	for _, c := range cols {
		if !rag.IsNetDevCollection(c.Name) {
			filtered = append(filtered, c)
		}
	}
	cols = filtered
	if len(cols) == 0 {
		return "no collections (import documents with rag_import)", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d collection(s):\n", len(cols))
	for _, c := range cols {
		fmt.Fprintf(&b, "- %s: %d doc(s), %d chunks, %s\n", c.Name, c.Documents, c.Chunks, humanSize(c.Size))
	}
	return b.String(), nil
}

// --- rag_delete -------------------------------------------------------------

type ragDelete struct{}

func (ragDelete) Name() string { return "rag_delete" }

func (ragDelete) Description() string {
	return "Remove a document (by path) or an entire collection from the knowledge base. Pass collection + path to delete one document; pass just collection to delete the whole collection."
}

func (ragDelete) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "collection":{"type":"string","description":"Collection to delete from"},
  "path":{"type":"string","description":"Document path to delete (empty = delete the whole collection)"}
},
"required":["collection"]
}`)
}

func (ragDelete) ReadOnly() bool { return false }

func (ragDelete) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Collection string `json:"collection"`
		Path       string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Collection) == "" {
		return "", errors.New("collection is required")
	}
	if err := refuseNetDevNamespace(p.Collection); err != nil {
		return "", err
	}
	s, err := requireRAG()
	if err != nil {
		return "", err
	}
	if err := s.Delete(p.Collection, p.Path); err != nil {
		return "", err
	}
	if p.Path == "" {
		return fmt.Sprintf("deleted collection %q", p.Collection), nil
	}
	return fmt.Sprintf("deleted %s from %q", p.Path, p.Collection), nil
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}

// isTopicType reports whether an entity acts as a topic/event hub — a node that
// aggregates members via member_of/part_of relations (cog_rag's theme concept,
// modeled here with the existing binary relation schema, no schema change).
func isTopicType(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "topic", "event", "theme", "group", "team":
		return true
	}
	return false
}

// dedupStrings returns s with duplicates removed, order preserved.
func dedupStrings(s []string) []string {
	seen := map[string]bool{}
	out := s[:0]
	for _, v := range s {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// sourceLabel renders an entity/relation's provenance as a compact citation
// like "doc.md#3、notes.md#1" (basename + chunk index). Empty when there are no
// sources. Deduplicated and capped to keep the line readable when an entity was
// extracted from many chunks.
func sourceLabel(srcs []rag.Source) string {
	if len(srcs) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var parts []string
	for _, s := range srcs {
		key := fmt.Sprintf("%s#%d", filepath.Base(s.Path), s.Chunk)
		if seen[key] {
			continue
		}
		seen[key] = true
		parts = append(parts, key)
	}
	sort.Strings(parts)
	if len(parts) > 4 {
		parts = append(parts[:4], "…")
	}
	return strings.Join(parts, "、")
}
