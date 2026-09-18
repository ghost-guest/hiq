// Package apihelper provides a small set of shared HTTP helpers reused by the
// scheduler LLM time-parser, the RAG ask path, and the RAG extractor. It
// centralizes the base-URL override, a shared proxy-aware HTTP client, and a
// response-truncation utility so these callers do not duplicate the HTTP
// boilerplate.
//
// (The former private-protocol APICall helper was removed in WP-2.6 along with the
// multimodal tools; only the generic Client/BaseURL/Truncate utilities remain,
// borrowed by the /chat/completions callers above.)
package apihelper

import (
	"net/http"
	"strings"
	"time"
)

// defaultBaseURL is empty — Hiq ships no built-in API endpoint. The
// scheduler/RAG direct /chat/completions callers must get their base URL from
// the resolved provider config (Cowork.FastLLMBaseDomain override, or the
// fast-task model's provider entry). An empty BaseURL means "not configured".
const defaultBaseURL = ""

// BaseURL is the API root for direct /chat/completions calls made by the
// scheduler time-parser and RAG ask. It's a var (not a const) so a private
// deployment or proxy can override it at boot via SetBaseDomain.
var BaseURL = defaultBaseURL

// Client is a shared HTTP client for the direct /chat/completions callers.
var Client = &http.Client{Timeout: 120 * time.Second}

// SetClient replaces the shared HTTP client. boot.go calls this so direct
// /chat/completions calls go through the same configured client (proxy,
// timeouts) as the rest of the app; without it callers use the default 120s
// client and may fail with EOF in proxy-only environments.
func SetClient(c *http.Client) {
	if c != nil {
		Client = c
	}
}

// SetBaseDomain overrides the API base URL used by the direct
// /chat/completions callers. Pass the full base (e.g.
// "https://api.example.com/v1"); an empty value resets
// to the default. boot.go derives the value from [cowork] fast_llm_base_domain
// so a private deployment or proxy can redirect these calls without code
// changes.
func SetBaseDomain(base string) {
	base = strings.TrimSpace(base)
	if base == "" {
		BaseURL = defaultBaseURL
		return
	}
	BaseURL = base
}

// Truncate shortens s to n bytes, snapping to the last space to avoid
// cutting mid-word. Returns s unchanged if it's already short enough.
func Truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > n/2 && cut < len(s) && s[cut] != ' ' {
		cut--
	}
	if cut <= n/2 {
		cut = n
	}
	return s[:cut] + "..."
}
