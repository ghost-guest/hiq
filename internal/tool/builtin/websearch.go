package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zzycxz/fairpeer/internal/netclient"
	"github.com/zzycxz/fairpeer/internal/tool"
)

func init() { tool.RegisterBuiltin(webSearch{}) }

// webSearchErrorMaxRead caps how much of a non-200 error response body we read
// into the returned error. A misbehaving/abusive search backend could stream
// a huge body; without a cap that unbounded read bloats memory. (The success
// path parses structured JSON, so it's not affected.)
const webSearchErrorMaxRead = 8 << 10 // 8 KiB

// searchCacheTTL is the default TTL for search cache entries.
const searchCacheTTL = 10 * time.Minute

// searchEndpointVars holds the upstream search API endpoints. Vars (not
// consts) so tests can point them at an httptest server.
var (
	braveAPIURL     = "https://api.search.brave.com/res/v1/web/search"
	exaAPIURL       = "https://api.exa.ai/search"
	linkupAPIURL    = "https://api.linkup.so/v1/search"
	anySearchAPIURL = "https://api.anysearch.com/v1/search"
)

// searchCache is the global search cache instance.
// Initialized lazily on first use.
var searchCache *SearchCache

// initSearchCache initializes the search cache if not already done.
func initSearchCache() {
	if searchCache != nil {
		return
	}

	// Get cache directory from environment or use default
	cacheDir := os.Getenv("FAIRPEER_CACHE_DIR")
	if cacheDir == "" {
		homeDir, _ := os.UserHomeDir()
		cacheDir = filepath.Join(homeDir, ".fairpeer", "cache")
	}

	dbPath := filepath.Join(cacheDir, "search-cache.db")
	cache, err := NewSearchCache(dbPath, searchCacheTTL)
	if err != nil {
		// Log error but don't fail - caching is optional
		fmt.Fprintf(os.Stderr, "Warning: Failed to initialize search cache: %v\n", err)
		return
	}

	searchCache = cache
}

type webSearch struct {
	proxySpec netclient.ProxySpec
}

const webSearchTimeout = 10 * time.Second

// searchHedgeDelay is how long we wait before launching the next engine in
// parallel while earlier engines are still in flight (hedged requests). The
// first engine to return a success wins; slower ones are cancelled. This
// bounds worst-case latency instead of paying each engine's full timeout
// serially. Var (not const) so tests can shorten it.
var searchHedgeDelay = 3500 * time.Millisecond

func (webSearch) Name() string { return "web_search" }

func (webSearch) Description() string {
	return "Perform a web search to find current information, news, or reference material. Works out of the box with zero configuration: if no API key is configured it falls back to AnySearch free anonymous search. Configured engines (Brave, Exa, Linkup, AnySearch) are raced in parallel with a hedged fallback chain. Returns a formatted markdown list of search results including titles, URLs, and snippets."
}

func (webSearch) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "query":{"type":"string","description":"The search query string."}
},
"required":["query"]
}`)
}

func (webSearch) ReadOnly() bool { return true }

func (ws webSearch) proxyURLFor(req *http.Request) (string, error) {
	return netclient.ProxyURLFor(ws.proxySpec, req)
}

type searchResultItem struct {
	Title   string
	URL     string
	Snippet string
}

func (ws webSearch) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Query) == "" {
		return "", fmt.Errorf("query is required")
	}

	// Initialize cache if needed
	initSearchCache()

	// Check cache first
	if searchCache != nil {
		if cachedResults, engine, found := searchCache.Get(p.Query); found && len(cachedResults) > 0 {
			// Return cached results
			var sb strings.Builder
			fmt.Fprintf(&sb, "Search results for %q (via %s, cached):\n\n", p.Query, engine)
			for i, r := range cachedResults {
				fmt.Fprintf(&sb, "### %d. [%s](%s)\n", i+1, r.Title, r.URL)
				if r.Snippet != "" {
					fmt.Fprintf(&sb, "> %s\n", strings.ReplaceAll(strings.TrimSpace(r.Snippet), "\n", "\n> "))
				}
				sb.WriteString("\n")
			}
			return WrapUntrusted("web", sb.String()), nil
		}
	}

	client := ssrfGuardedClient(ws.proxyURLFor)

	results, engineUsed, err := runHedgedSearch(ctx, client, p.Query)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return fmt.Sprintf("No results found for %q using %s.", p.Query, engineUsed), nil
	}

	// Cache the results
	if searchCache != nil {
		searchCache.Set(p.Query, results, engineUsed)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Search results for %q (via %s):\n\n", p.Query, engineUsed)
	for i, r := range results {
		fmt.Fprintf(&sb, "### %d. [%s](%s)\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&sb, "> %s\n", strings.ReplaceAll(strings.TrimSpace(r.Snippet), "\n", "\n> "))
		}
		sb.WriteString("\n")
	}
	// Wrap as untrusted content so the model treats result titles/snippets
	// (which are attacker-controllable web page content) as data, not
	// instructions — same defense as web_fetch and rag_search.
	return WrapUntrusted("web", sb.String()), nil
}

// searchEngine is one candidate backend in the hedged search chain.
type searchEngine struct {
	name string
	run  func(ctx context.Context) ([]searchResultItem, error)
}

// buildSearchEngines returns the candidate engines in preference order:
// configured engines first (they have paid quotas and better quality), then
// AnySearch — always, because it works anonymously without an API key. That
// last entry is what makes web_search zero-config out of the box.
func buildSearchEngines(client *http.Client, query string) []searchEngine {
	var engines []searchEngine
	braveKey := os.Getenv("BRAVE_API_KEY")
	if braveKey == "" {
		braveKey = os.Getenv("BRAVE_SEARCH_API_KEY")
	}
	if braveKey != "" {
		engines = append(engines, searchEngine{"Brave Search", func(ctx context.Context) ([]searchResultItem, error) {
			return searchBrave(ctx, client, braveKey, query)
		}})
	}
	if key := os.Getenv("EXA_API_KEY"); key != "" {
		engines = append(engines, searchEngine{"Exa", func(ctx context.Context) ([]searchResultItem, error) {
			return searchExa(ctx, client, key, query)
		}})
	}
	if key := os.Getenv("LINKUP_API_KEY"); key != "" {
		engines = append(engines, searchEngine{"Linkup", func(ctx context.Context) ([]searchResultItem, error) {
			return searchLinkup(ctx, client, key, query)
		}})
	}
	if key := os.Getenv("ANYSEARCH_API_KEY"); key != "" {
		engines = append(engines, searchEngine{"AnySearch", func(ctx context.Context) ([]searchResultItem, error) {
			return searchAnySearch(ctx, client, key, query)
		}})
	} else {
		// Zero-config fallback: AnySearch supports anonymous access (lower
		// rate limits, full features). This keeps web_search usable with no
		// configuration at all.
		engines = append(engines, searchEngine{"AnySearch (free)", func(ctx context.Context) ([]searchResultItem, error) {
			return searchAnySearch(ctx, client, "", query)
		}})
	}
	return engines
}

func runHedgedSearch(ctx context.Context, client *http.Client, query string) ([]searchResultItem, string, error) {
	return runHedgedEngines(ctx, buildSearchEngines(client, query))
}

// runHedgedEngines is the engine-agnostic core of the hedged search. It
// launches the preferred engine immediately and hedges: if it has not
// answered within searchHedgeDelay (or it fails outright), the next engine is
// launched in parallel. The first success wins and the rest are cancelled —
// worst-case latency stays bounded instead of paying each engine's full
// timeout serially.
func runHedgedEngines(ctx context.Context, engines []searchEngine) ([]searchResultItem, string, error) {
	if len(engines) == 0 {
		return nil, "", fmt.Errorf("no search engines available")
	}

	type outcome struct {
		idx     int
		results []searchResultItem
		err     error
	}

	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	outcomes := make(chan outcome, len(engines))
	launch := func(i int) {
		go func() {
			reqCtx, c := context.WithTimeout(hctx, webSearchTimeout)
			defer c()
			results, err := engines[i].run(reqCtx)
			outcomes <- outcome{idx: i, results: results, err: err}
		}()
	}

	launch(0)
	next := 1
	timer := time.NewTimer(searchHedgeDelay)
	defer timer.Stop()

	var lastErr error
	for received := 0; received < len(engines); received++ {
		select {
		case oc := <-outcomes:
			if oc.err == nil {
				return oc.results, engines[oc.idx].name, nil
			}
			lastErr = oc.err
			if next < len(engines) {
				launch(next) // failed → immediately promote the next engine
				next++
			}
		case <-timer.C:
			if next < len(engines) {
				launch(next) // too slow → hedge with the next engine
				next++
			}
			timer.Reset(searchHedgeDelay)
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	return nil, "", fmt.Errorf("all search engines failed. Last error: %w", lastErr)
}

func searchBrave(ctx context.Context, client *http.Client, key, query string) ([]searchResultItem, error) {
	reqCtx, cancel := context.WithTimeout(ctx, webSearchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", braveAPIURL, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Add("q", query)
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", key)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, webSearchErrorMaxRead))
		return nil, fmt.Errorf("brave search returned status %d: %s", resp.StatusCode, string(body))
	}

	var data struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	var out []searchResultItem
	for _, r := range data.Web.Results {
		out = append(out, searchResultItem{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Description,
		})
	}
	return out, nil
}

func searchExa(ctx context.Context, client *http.Client, key, query string) ([]searchResultItem, error) {
	reqCtx, cancel := context.WithTimeout(ctx, webSearchTimeout)
	defer cancel()

	payload := map[string]any{
		"query": query,
		"type":  "auto",
		"contents": map[string]any{
			"highlights": true,
		},
	}
	bodyData, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(reqCtx, "POST", "https://api.exa.ai/search", bytes.NewReader(bodyData))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, webSearchErrorMaxRead))
		return nil, fmt.Errorf("exa search returned status %d: %s", resp.StatusCode, string(body))
	}

	var data struct {
		Results []struct {
			Title      string   `json:"title"`
			URL        string   `json:"url"`
			Text       string   `json:"text"`
			Highlights []string `json:"highlights"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	var out []searchResultItem
	for _, r := range data.Results {
		snippet := r.Text
		if len(r.Highlights) > 0 {
			snippet = strings.Join(r.Highlights, " ... ")
		}
		if len(snippet) > 800 {
			snippet = snippet[:800] + "..."
		}
		out = append(out, searchResultItem{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: snippet,
		})
	}
	return out, nil
}

func searchLinkup(ctx context.Context, client *http.Client, key, query string) ([]searchResultItem, error) {
	reqCtx, cancel := context.WithTimeout(ctx, webSearchTimeout)
	defer cancel()

	payload := map[string]any{
		"q":          query,
		"depth":      "standard",
		"outputType": "searchResults",
	}
	bodyData, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(reqCtx, "POST", linkupAPIURL, bytes.NewReader(bodyData))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, webSearchErrorMaxRead))
		return nil, fmt.Errorf("linkup search returned status %d: %s", resp.StatusCode, string(body))
	}

	var data struct {
		Results []struct {
			Name    string `json:"name"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	var out []searchResultItem
	for _, r := range data.Results {
		snippet := r.Content
		if len(snippet) > 800 {
			snippet = snippet[:800] + "..."
		}
		out = append(out, searchResultItem{
			Title:   r.Name,
			URL:     r.URL,
			Snippet: snippet,
		})
	}
	return out, nil
}

// searchAnySearch queries the AnySearch API. An empty key means anonymous
// access: the request is sent without the Authorization header. AnySearch
// allows anonymous use with lower rate limits, which is what keeps web_search
// working with zero configuration.
func searchAnySearch(ctx context.Context, client *http.Client, key, query string) ([]searchResultItem, error) {
	reqCtx, cancel := context.WithTimeout(ctx, webSearchTimeout)
	defer cancel()

	payload := map[string]any{
		"query":       query,
		"max_results": 10,
	}
	bodyData, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(reqCtx, "POST", anySearchAPIURL, bytes.NewReader(bodyData))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, webSearchErrorMaxRead))
		return nil, fmt.Errorf("anysearch returned status %d: %s", resp.StatusCode, string(body))
	}

	var data struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Results []struct {
				Title   string `json:"title"`
				URL     string `json:"url"`
				Snippet string `json:"snippet"`
				Content string `json:"content"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	if data.Code != 0 {
		return nil, fmt.Errorf("anysearch error: %s", data.Message)
	}

	var out []searchResultItem
	for _, r := range data.Data.Results {
		snippet := r.Snippet
		if snippet == "" {
			snippet = r.Content
		}
		if len(snippet) > 800 {
			snippet = snippet[:800] + "..."
		}
		out = append(out, searchResultItem{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: snippet,
		})
	}
	return out, nil
}
