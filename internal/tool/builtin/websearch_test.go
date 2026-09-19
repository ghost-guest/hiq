package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func withTestEndpoints(t *testing.T, anySearchURL string) {
	t.Helper()
	old := anySearchAPIURL
	if anySearchURL != "" {
		anySearchAPIURL = anySearchURL
	}
	t.Cleanup(func() { anySearchAPIURL = old })
}

// newAnySearchStub returns an httptest server answering with a valid AnySearch
// payload, plus a pointer recording whether the Authorization header was set.
func newAnySearchStub(t *testing.T, authSeen *atomic.Bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			authSeen.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    0,
			"message": "success",
			"data": map[string]any{
				"results": []map[string]any{
					{"title": "Result A", "url": "https://example.com/a", "snippet": "snippet a"},
					{"title": "Result B", "url": "https://example.com/b", "content": "content b"},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSearchAnySearchAnonymous verifies that an empty key omits the
// Authorization header entirely (anonymous access) and results still parse.
func TestSearchAnySearchAnonymous(t *testing.T) {
	var authSeen atomic.Bool
	srv := newAnySearchStub(t, &authSeen)
	withTestEndpoints(t, srv.URL)

	results, err := searchAnySearch(context.Background(), srv.Client(), "", "golang release")
	if err != nil {
		t.Fatalf("anonymous search failed: %v", err)
	}
	if authSeen.Load() {
		t.Fatal("anonymous mode must not send an Authorization header")
	}
	if len(results) != 2 || results[0].Title != "Result A" || results[1].Snippet != "content b" {
		t.Fatalf("unexpected results: %+v", results)
	}
}

// TestSearchAnySearchWithKey verifies the keyed path still sends the bearer.
func TestSearchAnySearchWithKey(t *testing.T) {
	var authSeen atomic.Bool
	srv := newAnySearchStub(t, &authSeen)
	withTestEndpoints(t, srv.URL)

	if _, err := searchAnySearch(context.Background(), srv.Client(), "as_sk_test", "q"); err != nil {
		t.Fatalf("keyed search failed: %v", err)
	}
	if !authSeen.Load() {
		t.Fatal("keyed mode must send the Authorization header")
	}
}

func clearSearchEnv(t *testing.T) {
	t.Helper()
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	t.Setenv("EXA_API_KEY", "")
	t.Setenv("LINKUP_API_KEY", "")
	t.Setenv("ANYSEARCH_API_KEY", "")
}

// TestBuildSearchEnginesAlwaysHasFallback verifies the zero-config guarantee:
// with no keys configured, the chain still contains the anonymous AnySearch
// engine; with a key configured it becomes the keyed variant.
func TestBuildSearchEnginesAlwaysHasFallback(t *testing.T) {
	clearSearchEnv(t)

	engines := buildSearchEngines(webSearch{}, &http.Client{}, "q")
	// Zero-config chain: direct-scrape engines first, anonymous AnySearch last.
	if len(engines) != 3 || engines[0].name != "Bing (direct)" || engines[1].name != "Baidu (direct)" || engines[2].name != "AnySearch (free)" {
		t.Fatalf("no-key config should yield direct engines + anonymous AnySearch, got %+v", engines)
	}

	t.Setenv("ANYSEARCH_API_KEY", "as_sk_x")
	t.Setenv("EXA_API_KEY", "exa_x")
	engines = buildSearchEngines(webSearch{}, &http.Client{}, "q")
	// Keyed engines come first (in key order), then the always-on fallbacks.
	if len(engines) != 5 || engines[0].name != "Exa" || engines[1].name != "AnySearch" || engines[len(engines)-1].name != "AnySearch (free)" {
		t.Fatalf("keyed config should yield keyed engines before the fallbacks, got %+v", engines)
	}
}

// TestRunHedgedSearchFirstSuccessWins verifies hedging: a slow (but
// eventually successful) first engine is outrun by the second engine, and the
// winner's name is reported.
func TestRunHedgedSearchFirstSuccessWins(t *testing.T) {
	clearSearchEnv(t)
	hedge := searchHedgeDelay
	t.Cleanup(func() { searchHedgeDelay = hedge })
	searchHedgeDelay = 30 * time.Millisecond

	slow := searchEngine{"slow", func(ctx context.Context) ([]searchResultItem, error) {
		time.Sleep(300 * time.Millisecond)
		return []searchResultItem{{Title: "slow"}}, nil
	}}
	fast := searchEngine{"fast", func(ctx context.Context) ([]searchResultItem, error) {
		return []searchResultItem{{Title: "fast"}}, nil
	}}

	results, name, err := runHedgedEngines(context.Background(), []searchEngine{slow, fast})
	if err != nil {
		t.Fatalf("hedged run failed: %v", err)
	}
	if name != "fast" || len(results) != 1 || results[0].Title != "fast" {
		t.Fatalf("expected fast engine to win, got name=%q results=%+v", name, results)
	}
}

// TestRunHedgedSearchFailurePromotesNext verifies that a failing engine
// immediately promotes the next one without waiting for the hedge timer.
func TestRunHedgedSearchFailurePromotesNext(t *testing.T) {
	clearSearchEnv(t)
	fail := searchEngine{"fail", func(ctx context.Context) ([]searchResultItem, error) {
		return nil, context.DeadlineExceeded
	}}
	ok := searchEngine{"ok", func(ctx context.Context) ([]searchResultItem, error) {
		return []searchResultItem{{Title: "ok"}}, nil
	}}

	results, name, err := runHedgedEngines(context.Background(), []searchEngine{fail, ok})
	if err != nil {
		t.Fatalf("hedged run failed: %v", err)
	}
	if name != "ok" || len(results) != 1 {
		t.Fatalf("expected ok engine to win, got name=%q results=%+v", name, results)
	}
}

// TestRunHedgedSearchAllFail verifies the aggregated error when every engine
// fails, and that the last error is surfaced.
func TestRunHedgedSearchAllFail(t *testing.T) {
	clearSearchEnv(t)
	mk := func(name string) searchEngine {
		return searchEngine{name, func(ctx context.Context) ([]searchResultItem, error) {
			return nil, fmt.Errorf("%s down", name)
		}}
	}

	_, _, err := runHedgedEngines(context.Background(), []searchEngine{mk("a"), mk("b")})
	if err == nil {
		t.Fatal("expected an error when all engines fail")
	}
	if !strings.Contains(err.Error(), "all search engines failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
