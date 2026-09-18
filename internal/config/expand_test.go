package config

import "testing"

func TestExpandVars(t *testing.T) {
	t.Setenv("HIQ_TEST_TOKEN", "sk-123")
	t.Setenv("HIQ_TEST_EMPTY", "")

	cases := []struct{ in, want string }{
		{"Bearer ${HIQ_TEST_TOKEN}", "Bearer sk-123"},
		{"${HIQ_TEST_MISSING}", ""},                                   // unset, no default → empty
		{"${HIQ_TEST_MISSING:-fallback}", "fallback"},                 // unset → default
		{"${HIQ_TEST_EMPTY:-fallback}", "fallback"},                   // set-but-empty → default
		{"${HIQ_TEST_TOKEN:-fallback}", "sk-123"},                     // set → value, default ignored
		{"no vars here", "no vars here"},                                   // untouched
		{"a${HIQ_TEST_TOKEN}b${HIQ_TEST_MISSING}c", "ask-123bc"}, // multiple refs
	}
	for _, c := range cases {
		if got := ExpandVars(c.in); got != c.want {
			t.Errorf("ExpandVars(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExpandedPlugin(t *testing.T) {
	t.Setenv("HIQ_TEST_KEY", "secret")
	e := PluginEntry{
		Name:    "x",
		Type:    "http",
		URL:     "https://api/${HIQ_TEST_MISSING:-v1}",
		Args:    []string{"--token", "${HIQ_TEST_KEY}"},
		Env:     map[string]string{"K": "${HIQ_TEST_KEY}"},
		Headers: map[string]string{"Authorization": "Bearer ${HIQ_TEST_KEY}"},
	}
	out := e.ExpandedPlugin()
	if out.URL != "https://api/v1" {
		t.Errorf("URL = %q", out.URL)
	}
	if out.Args[1] != "secret" {
		t.Errorf("Args = %v", out.Args)
	}
	if out.Env["K"] != "secret" || out.Headers["Authorization"] != "Bearer secret" {
		t.Errorf("env/headers not expanded: %v %v", out.Env, out.Headers)
	}
	// The original entry must be untouched (we returned a copy).
	if e.Headers["Authorization"] != "Bearer ${HIQ_TEST_KEY}" {
		t.Error("ExpandedPlugin mutated the original entry")
	}
}
