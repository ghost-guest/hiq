package rag

// llm_semantic.go adds LLM-driven semantic search over FTS5 (SPEC v2 §3.6),
// without any embedding model or vector store. It uses the LLM provider the
// user already configured (DeepSeek/GLM/Qwen/etc.) for two complementary jobs:
//
//  1. Query expansion: "薪资" → ["薪资", "工资", "薪水", "报酬", "salary", "wage"]
//     and "running" → ["running", "run", "ran"]. This fixes FTS5's three blind
//     spots in one shot: CJK synonyms, English stemming (run/running/ran), and
//     cross-language alignment (数据库↔database). The expanded terms are OR'd
//     into the FTS5 query so recall improves before any reranking.
//
//  2. LLM rerank: take FTS5's top-N candidates and ask the LLM which are
//     genuinely relevant to the query, producing a precision-ordered list. This
//     catches results that share a word but aren't semantically on-topic.
//
// Both calls reuse the existing provider.Stream single-turn pattern
// (agent/goal_judge.go:46). Both cache by query hash so repeated searches cost
// zero extra LLM calls. Both degrade gracefully: any failure (no provider,
// timeout, parse error) returns the input unchanged, so the search never breaks
// — it just falls back to plain FTS5. The user sees no error, only possibly
// lower-quality results, which matches "works offline / works without a model".
//
// Design constraints (SPEC v2 §2.0): zero user learning cost (the provider is
// already configured; this is automatic), zero prompt bloat (this is a search-
// time tool-internal call, never enters the system prompt).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/provider"
)

// LLMSemantic wraps a provider for query expansion + reranking. A nil provider
// (or nil LLMSemantic) means "off" — every method degrades to a no-op/pass-through.
type LLMSemantic struct {
	prov provider.Provider

	// Caches keyed by query string. Tiny LRU would be nicer, but a bounded map
	// with a TTL is enough for the personal-knowledge-base scale and keeps this
	// dependency-free. The mutex guards both maps.
	mu       sync.Mutex
	expCache map[string]expandCacheEntry
	rerCache map[string]rerankCacheEntry

	// callTimeout bounds each LLM call so a slow provider can't stall a search.
	callTimeout time.Duration
}

type expandCacheEntry struct {
	terms    []string
	expireAt time.Time
}

type rerankCacheEntry struct {
	order    []int // indexes into the original results slice
	expireAt time.Time
}

// cacheTTL is how long a query's expansion/rerank stays cached. Repeated
// searches for the same concept (common during a research session) reuse it.
const cacheTTL = 30 * time.Minute

// defaultCallTimeout bounds a single LLM call. A semantic boost that takes
// longer than this isn't worth waiting for — fall back to FTS5.
const defaultCallTimeout = 12 * time.Second

// NewLLMSemantic returns an LLMSemantic over the given provider. prov may be
// nil (all methods then no-op), letting the caller wire unconditionally.
func NewLLMSemantic(prov provider.Provider) *LLMSemantic {
	return &LLMSemantic{
		prov:        prov,
		expCache:    make(map[string]expandCacheEntry),
		rerCache:    make(map[string]rerankCacheEntry),
		callTimeout: defaultCallTimeout,
	}
}

// --- Query expansion --------------------------------------------------------

// ExpandQuery returns the original query plus LLM-generated synonyms/variants,
// suitable for OR-ing into an FTS5 MATCH. On any failure (no provider, timeout,
// unparseable reply) it returns []string{query} — the caller always gets at
// least the original query, so search quality never drops below plain FTS5.
//
// The LLM is prompted to produce bilingual variants, which simultaneously fixes
// CJK synonyms, English stemming, and cross-language alignment.
func (l *LLMSemantic) ExpandQuery(ctx context.Context, query string) []string {
	if l == nil || l.prov == nil || strings.TrimSpace(query) == "" {
		return []string{query}
	}
	// Cache hit?
	if entry, ok := l.expandCacheGet(query); ok {
		return entry
	}
	// Bound the call so a slow/hung provider can't stall the search.
	cctx, cancel := context.WithTimeout(ctx, l.callTimeout)
	defer cancel()

	raw, err := l.callLLM(cctx, expandSystemPrompt, buildExpandUserMsg(query))
	if err != nil {
		return []string{query}
	}
	terms := parseExpandReply(raw, query)
	l.expandCacheSet(query, terms)
	return terms
}

// expandSystemPrompt asks for a compact JSON array of search terms. Bilingual
// so a single mechanism covers CJK synonyms, English stemming, and cross-
// language alignment. Kept short to minimize tokens.
const expandSystemPrompt = `You expand a search query into synonymous or related search terms for a full-text search engine. Include: the original term, same-language synonyms, English word-stem variants (run/running/ran, repository/repositories), and cross-language equivalents (数据库↔database, 认证↔authentication). Return ONLY a JSON array of short terms (max 8), no explanation. Example input "薪资" → ["薪资","工资","薪水","报酬","salary","wage"]. Example input "running tests" → ["running","run","tests","test","testing","执行","测试"].`

func buildExpandUserMsg(query string) string {
	return fmt.Sprintf("Query: %s\n\nReturn the JSON array now.", query)
}

// parseExpandReply extracts a []string from the LLM reply, always ensuring the
// original query is present (the caller must be able to search even if the LLM
// omits it). Tolerates prose/fence wrapping around the JSON.
func parseExpandReply(raw, original string) []string {
	raw = strings.TrimSpace(raw)
	raw = extractJSONArray(raw)
	var terms []string
	if err := json.Unmarshal([]byte(raw), &terms); err != nil {
		return []string{original}
	}
	// Dedup (case-insensitive) + guarantee the original is first.
	seen := make(map[string]bool, len(terms)+1)
	out := make([]string, 0, len(terms)+1)
	add := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" {
			return
		}
		k := strings.ToLower(t)
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, t)
	}
	add(original)
	for _, t := range terms {
		add(t)
	}
	if len(out) == 0 {
		return []string{original}
	}
	return out
}

// --- LLM rerank -------------------------------------------------------------

// Rerank reorders results by LLM-judged relevance to the query. It returns the
// results reordered (best first); any item the LLM didn't place keeps its
// relative order (stable). On any failure it returns results unchanged — the
// caller's BM25 ordering is preserved, so search never breaks.
//
// To stay within a tiny token budget, only each result's Snippet (truncated)
// is sent, capped at maxRerankCandidates.
const maxRerankCandidates = 20

func (l *LLMSemantic) Rerank(ctx context.Context, query string, results []Result) []Result {
	if l == nil || l.prov == nil || len(results) <= 1 {
		return results
	}
	// Cache hit? Keyed by query+result-identity so a repeated search reuses.
	key := rerankKey(query, results)
	if order, ok := l.rerankCacheGet(key); ok {
		return applyRerankOrder(results, order)
	}
	// Over-fetch guard: don't send more than maxRerankCandidates to the LLM.
	pool := results
	if len(pool) > maxRerankCandidates {
		pool = pool[:maxRerankCandidates]
	}
	cctx, cancel := context.WithTimeout(ctx, l.callTimeout)
	defer cancel()
	raw, err := l.callLLM(cctx, rerankSystemPrompt, buildRerankUserMsg(query, pool))
	if err != nil {
		return results
	}
	order := parseRerankReply(raw, len(pool))
	l.rerankCacheSet(key, order)
	return applyRerankOrder(results, order)
}

const rerankSystemPrompt = `You rerank search results by relevance to a query. You receive numbered snippets; reply with ONLY a JSON array of the numbers in relevance order (most relevant first). Omit numbers whose snippets are clearly off-topic. Example: query "薪资结构" with snippets [1]工资单 [2]会议纪要 [3]薪酬等级 → [3,1]. No explanation.`

func buildRerankUserMsg(query string, results []Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Query: %s\n\nSnippets:\n", query)
	for i, r := range results {
		snip := strings.TrimSpace(r.Snippet)
		if snip == "" {
			snip = strings.TrimSpace(r.Path)
		}
		// Cap each snippet to keep the call cheap.
		if len(snip) > 200 {
			snip = snip[:200] + "…"
		}
		fmt.Fprintf(&b, "[%d] %s\n", i+1, snip)
	}
	b.WriteString("\nReturn the JSON array of numbers now.")
	return b.String()
}

// parseRerankReply extracts a 0-based index order from the LLM's 1-based array.
func parseRerankReply(raw string, n int) []int {
	raw = strings.TrimSpace(raw)
	raw = extractJSONArray(raw)
	var ones []int
	if err := json.Unmarshal([]byte(raw), &ones); err != nil {
		return nil // fall back to original order
	}
	order := make([]int, 0, n)
	seen := make(map[int]bool, n)
	for _, one := range ones {
		idx := one - 1
		if idx < 0 || idx >= n || seen[idx] {
			continue
		}
		seen[idx] = true
		order = append(order, idx)
	}
	// Append any unranked indexes in their original order (stable tail).
	for i := 0; i < n; i++ {
		if !seen[i] {
			order = append(order, i)
		}
	}
	return order
}

// applyRerankOrder reorders results by a 0-based index order. If order is nil
// or shorter than results, the untouched tail keeps its original order.
func applyRerankOrder(results []Result, order []int) []Result {
	if len(order) == 0 || len(results) <= 1 {
		return results
	}
	out := make([]Result, 0, len(results))
	placed := make([]bool, len(results))
	for _, idx := range order {
		if idx >= 0 && idx < len(results) && !placed[idx] {
			out = append(out, results[idx])
			placed[idx] = true
		}
	}
	for i, r := range results {
		if !placed[i] {
			out = append(out, r)
		}
	}
	return out
}

// --- shared LLM call helper -------------------------------------------------

// callLLM does a single non-streaming-over-channel completion and returns the
// concatenated text. Mirrors agent/goal_judge.go:46's pattern but lives here so
// the rag package doesn't depend on agent.
func (l *LLMSemantic) callLLM(ctx context.Context, system, user string) (string, error) {
	ch, err := l.prov.Stream(ctx, provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: system},
			{Role: provider.RoleUser, Content: user},
		},
		Temperature: 0,
		MaxTokens:   512, // expansions/reranks are tiny; cap cost.
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case chunk, ok := <-ch:
			if !ok {
				return b.String(), nil
			}
			switch chunk.Type {
			case provider.ChunkText:
				b.WriteString(chunk.Text)
			case provider.ChunkError:
				return "", chunk.Err
			}
		}
	}
}

// extractJSONArray pulls the first [...] substring out of raw, tolerating
// ```json fences or leading prose ("Here are the terms: [...]").
func extractJSONArray(raw string) string {
	start := strings.Index(raw, "[")
	if start < 0 {
		return raw
	}
	end := strings.LastIndex(raw, "]")
	if end <= start {
		return raw
	}
	return raw[start : end+1]
}

// --- rerank key (query + result identity) -----------------------------------

func rerankKey(query string, results []Result) string {
	var b strings.Builder
	b.WriteString(query)
	for _, r := range results {
		b.WriteByte('|')
		b.WriteString(r.Collection)
		b.WriteByte(':')
		b.WriteString(r.Path)
		b.WriteByte(':')
		b.WriteString(r.Snippet)
	}
	return b.String()
}

// --- cache accessors (mutex-guarded) ----------------------------------------

func (l *LLMSemantic) expandCacheGet(query string) ([]string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.expCache[query]
	if !ok || time.Now().After(e.expireAt) {
		return nil, false
	}
	return e.terms, true
}

func (l *LLMSemantic) expandCacheSet(query string, terms []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expCache[query] = expandCacheEntry{terms: terms, expireAt: time.Now().Add(cacheTTL)}
}

func (l *LLMSemantic) rerankCacheGet(key string) ([]int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.rerCache[key]
	if !ok || time.Now().After(e.expireAt) {
		return nil, false
	}
	return e.order, true
}

func (l *LLMSemantic) rerankCacheSet(key string, order []int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rerCache[key] = rerankCacheEntry{order: order, expireAt: time.Now().Add(cacheTTL)}
}
