// search.go — upgrade spec 4-5: full-text search across a session directory.
// Sessions are small (single-digit MB at most), so a linear scan with a
// case-insensitive substring match is both simpler and faster than
// maintaining an index; results are aggregated per session with an excerpt
// around the first matches so the picker can show WHERE the query appeared,
// not just that it did. The meta sidecar (via ListSessions) supplies the
// title/preview columns the UI already renders.
//
// Ranked title-first (see SearchSessions): the title is a cheap, high-signal
// field, and a query that names a session should not be buried under incidental
// transcript matches — nor should answering it require reading any transcript.
package agent

import (
	"os"
	"strings"
	"unicode/utf8"
)

// SearchHit is one session that matched, with up to ExcerptCap excerpts of
// the matching lines.
type SearchHit struct {
	Path     string
	Excerpts []string
	// TitleMatch reports that the query matched the session's title (or its
	// preview line) rather than only its transcript. The flag exists because the
	// two are not equally interesting: someone who types a session's name is
	// looking for that session, while a body match may be incidental.
	TitleMatch bool
	// Routing/title fields mirrored from SessionInfo so a frontend hit can be
	// opened exactly like a regular session-list entry.
	Title         string
	TopicID       string
	Scope         string
	WorkspaceRoot string
	Profile       string
}

const (
	searchHitCap   = 50       // max sessions returned
	excerptCap     = 2        // excerpts per session
	excerptContext = 60       // chars of context around the match
	maxScanBytes   = 32 << 20 // skip sessions larger than 32MB ( pathological)
)

// SearchSessions returns the sessions under dir that match query
// (case-insensitive), capped at 50 hits.
//
// Two passes, deliberately ordered:
//
//  1. Title matches. The title lives in the sidecar metadata, so this pass costs
//     no transcript read at all — and it ranks first, because a query that names
//     a session should not be buried under incidental body matches.
//  2. Transcript matches, scanned only while pass 1 left room. A query that
//     already filled the result list from titles never opens a transcript.
//
// Only the title counts as tier 1. The preview line is derived from the first
// message, so treating it as a title would blur "you named this session" into
// "the needle appears somewhere early" — it is found by pass 2 instead, which
// also covers the untitled sessions a preview usually stands in for.
//
// A session matching both is reported once, as a title hit, with its excerpts
// attached. A query shorter than 2 bytes returns nil — callers gate on input.
func SearchSessions(dir, query string) []SearchHit {
	if len(query) < 2 {
		return nil
	}
	infos, err := ListSessions(dir)
	if err != nil || len(infos) == 0 {
		return nil
	}
	needle := strings.ToLower(query)
	hits := make([]SearchHit, 0, 16)
	seen := make(map[string]bool, len(infos))

	for _, si := range infos {
		if len(hits) >= searchHitCap {
			break
		}
		if !containsFold(si.TopicTitle, needle) {
			continue
		}
		seen[si.Path] = true
		hits = append(hits, hitFrom(si, matchExcerpts(si.Path, needle, excerptCap), true))
	}

	if len(hits) < searchHitCap {
		for _, si := range infos {
			if len(hits) >= searchHitCap {
				break
			}
			if seen[si.Path] {
				continue
			}
			excerpts := matchExcerpts(si.Path, needle, excerptCap)
			if len(excerpts) == 0 {
				continue
			}
			hits = append(hits, hitFrom(si, excerpts, false))
		}
	}
	return hits
}

func hitFrom(si SessionInfo, excerpts []string, titleMatch bool) SearchHit {
	return SearchHit{
		Path: si.Path, Excerpts: excerpts, TitleMatch: titleMatch,
		Title: si.TopicTitle, TopicID: si.TopicID, Scope: si.Scope,
		WorkspaceRoot: si.WorkspaceRoot, Profile: si.Profile,
	}
}

// containsFold is a nil-safe case-insensitive substring test. The needle is
// already lower-cased by the caller.
func containsFold(haystack, needle string) bool {
	if haystack == "" || needle == "" {
		return false
	}
	return strings.Contains(strings.ToLower(haystack), needle)
}

// matchExcerpts scans one transcript and returns up to cap excerpts of lines
// containing the needle. JSON lines carry role/content markup, so the excerpt
// centers on the match rather than the line start.
func matchExcerpts(path, needle string, cap int) []string {
	st, err := os.Stat(path)
	if err != nil || st.Size() > maxScanBytes {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if len(out) >= cap {
			break
		}
		idx := strings.Index(strings.ToLower(line), needle)
		if idx < 0 {
			continue
		}
		out = append(out, excerptAround(line, idx, len(needle)))
	}
	return out
}

// excerptAround trims a matched line to a readable window centered on the
// match, cutting at rune boundaries.
func excerptAround(line string, idx, nlen int) string {
	start := idx - excerptContext
	if start < 0 {
		start = 0
	}
	end := idx + nlen + excerptContext
	if end > len(line) {
		end = len(line)
	}
	for start > 0 && !utf8.RuneStart(line[start]) {
		start--
	}
	s := line[start:end]
	if start > 0 {
		s = "…" + s
	}
	if end < len(line) {
		s += "…"
	}
	// Collapse the JSONL noise that carries no signal in a preview.
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
