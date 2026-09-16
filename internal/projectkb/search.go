package projectkb

import (
	"sort"
	"strings"
	"unicode"
)

// MaxSearchLimit bounds a single search.
const MaxSearchLimit = 50

// Hit is one search result.
type Hit struct {
	Node  Node `json:"node"`
	Score int  `json:"score"`
}

// Search ranks indexed nodes against a free-text query.
//
// The scorer is deliberately simple and needs no index, because the hub's job
// is to POINT the model (or the user) at the right item — the item's own file
// or task is then read in full. Weighting: a title hit dominates, then a tag
// hit, then the ref (a path), then the summary. Every query token must match
// somewhere for a node to be considered at all, so a two-word query does not
// return everything that matches either word.
//
// An empty query lists the stored nodes in map order, which makes kb_search
// useful as a "show me everything" browse.
func (h *Hub) Search(query string, limit int) []Hit {
	if limit <= 0 {
		limit = 10
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}
	nodes := h.Nodes()
	tokens := tokenize(query)
	if len(tokens) == 0 {
		out := make([]Hit, 0, limit)
		for _, n := range nodes {
			if len(out) >= limit {
				break
			}
			out = append(out, Hit{Node: n})
		}
		return out
	}

	var hits []Hit
	for _, n := range nodes {
		score, ok := scoreNode(n, tokens)
		if !ok {
			continue
		}
		hits = append(hits, Hit{Node: n, Score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Node.Title != hits[j].Node.Title {
			return hits[i].Node.Title < hits[j].Node.Title
		}
		return hits[i].Node.ID < hits[j].Node.ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// scoreNode scores one node against every token. It reports false when any
// token is absent, implementing the AND semantics described on Search.
func scoreNode(n Node, tokens []string) (int, bool) {
	title := strings.ToLower(n.Title)
	ref := strings.ToLower(n.Ref)
	summary := strings.ToLower(n.Summary)
	tags := strings.ToLower(strings.Join(n.Tags, " "))

	total := 0
	for _, tok := range tokens {
		hit := 0
		if strings.Contains(title, tok) {
			hit += 8
			if strings.HasPrefix(title, tok) {
				hit += 4
			}
		}
		if tags != "" && strings.Contains(tags, tok) {
			hit += 4
		}
		if ref != "" && strings.Contains(ref, tok) {
			hit += 3
		}
		if summary != "" && strings.Contains(summary, tok) {
			hit += 1
		}
		if hit == 0 {
			return 0, false
		}
		total += hit
	}
	// A small kind bonus keeps structural entries ahead of narrative noise when
	// scores tie (a package beats a doc paragraph about the same word).
	if n.Kind == KindCode {
		total++
	}
	return total, true
}

// tokenize lowercases a query and splits it on anything that is not a letter,
// digit, underscore or CJK ideograph — so "internal/team" matches "internal"
// and "team", and CJK queries survive as whole runs.
func tokenize(q string) []string {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range q {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	// Drop single-character ASCII tokens (noise: "a", "x") but keep single CJK
	// characters, which are meaningful.
	kept := out[:0]
	for _, t := range out {
		if len([]rune(t)) == 1 && t[0] < 128 {
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}
