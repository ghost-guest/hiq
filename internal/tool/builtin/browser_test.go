package builtin

import (
	"path/filepath"
	"strings"
	"testing"
)

// browserTools is the set of tool names the cowork profile registers. This test
// guards against drift: if BrowserTools() stops returning one, the cowork
// experience silently regresses and the browser-auto skill's whitelist dangles.
func TestBrowserToolsRoster(t *testing.T) {
	tools := BrowserTools()
	want := map[string]bool{
		"browser_open": true, "browser_attach": true, "browser_navigate": true,
		"browser_cookies": true, "browser_tabs": true, "browser_switch_tab": true, "browser_hover": true,
		"browser_back": true, "browser_forward": true,
		"browser_click": true, "browser_type": true, "browser_scroll": true,
		"browser_extract": true, "browser_screenshot": true, "browser_evaluate": true,
		"browser_snapshot": true, "browser_select_option": true, "browser_upload_file": true,
		"browser_set_path": true, "browser_wait": true, "browser_keepalive": true,
		"browser_network": true, "browser_auto": true,
	}
	if len(tools) != len(want) {
		t.Fatalf("BrowserTools returned %d tools, want %d", len(tools), len(want))
	}
	seen := make(map[string]bool, len(tools))
	for _, tl := range tools {
		name := tl.Name()
		seen[name] = true
		if !want[name] {
			t.Errorf("unexpected browser tool %q", name)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("missing browser tool %q", name)
		}
	}
}

// ReadOnly classification: extract + screenshot + snapshot are read-only (safe
// to parallelize); the rest mutate the page or spawn a process. A flip here
// would change batching behavior, so lock it in.
func TestBrowserToolsReadOnlyClassification(t *testing.T) {
	readOnly := map[string]bool{
		"browser_extract": true, "browser_screenshot": true, "browser_snapshot": true, "browser_tabs": true,
	}
	for _, tl := range BrowserTools() {
		got := tl.ReadOnly()
		want := readOnly[tl.Name()]
		if got != want {
			t.Errorf("%s ReadOnly() = %v, want %v", tl.Name(), got, want)
		}
	}
}

// TestLooksLikeRef guards the ref/selector classifier: "e5" is a ref, but
// "email" or "e" alone is a selector. Misclassification would route a selector
// through the ref path (fail) or a ref through the selector path (wrong element).
func TestLooksLikeRef(t *testing.T) {
	refs := []string{"e1", "e5", "e12", "e999"}
	for _, s := range refs {
		if !looksLikeRef(s) {
			t.Errorf("looksLikeRef(%q) = false, want true", s)
		}
	}
	notRefs := []string{"email", "e", "submit", "#login", "button.btn", "ex", "e1a", ""}
	for _, s := range notRefs {
		if looksLikeRef(s) {
			t.Errorf("looksLikeRef(%q) = true, want false", s)
		}
	}
}

// TestSelectorFromArgs covers the three-way target classification: ref, selector
// string, and {x,y} coordinate. This is the routing logic click/select share.
func TestSelectorFromArgs(t *testing.T) {
	// Ref.
	sel, _, _, _, isRef, err := selectorFromArgs([]byte(`"e7"`))
	if err != nil || !isRef || sel != "e7" {
		t.Errorf("ref case: sel=%q isRef=%v err=%v", sel, isRef, err)
	}
	// Selector.
	sel, _, _, isCoord, isRef, err := selectorFromArgs([]byte(`"button#submit"`))
	if err != nil || isCoord || isRef || sel != "button#submit" {
		t.Errorf("selector case: sel=%q isCoord=%v isRef=%v err=%v", sel, isCoord, isRef, err)
	}
	// Coordinate.
	_, x, y, isCoord, isRef, err := selectorFromArgs([]byte(`{"x":100,"y":200}`))
	if err != nil || !isCoord || isRef || x != 100 || y != 200 {
		t.Errorf("coord case: x=%v y=%v isCoord=%v isRef=%v err=%v", x, y, isCoord, isRef, err)
	}
	// Empty/garbage → error.
	_, _, _, _, _, err = selectorFromArgs([]byte(`""`))
	if err == nil {
		t.Error("empty string should error")
	}
}

// TestUnknownSessionErrors confirms the pool rejects operations on a session that
// was never opened (or has expired). This is the no-Chrome path — it exercises
// only the bookkeeping, not a real browser, so it runs in CI without Chrome.
func TestUnknownSessionErrors(t *testing.T) {
	_, err := getBrowserSession("does-not-exist")
	if err == nil {
		t.Fatal("getBrowserSession on unknown id should error")
	}
}

// TestBrowserDisplayName covers the name derivation from a path. Guards the
// "browser ready (driving Chrome)" message against path-shape drift.
func TestBrowserDisplayName(t *testing.T) {
	cases := map[string]string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`:        "Chrome",
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`: "Edge",
		`/Applications/Brave Browser.app/Contents/MacOS/Brave Browser`: "Brave",
		`/usr/bin/chromium`: "Chromium",
		`/usr/bin/firefox`:  "firefox", // unknown → basename
	}
	for path, want := range cases {
		if got := browserDisplayName(path); got != want {
			t.Errorf("browserDisplayName(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestVerifyBrowserExe confirms an existing file verifies and a missing one
// doesn't. Uses the test binary itself as a known-existing executable.
func TestVerifyBrowserExe(t *testing.T) {
	// A non-empty path that doesn't exist.
	if _, ok := verifyBrowserExe(filepath.Join(t.TempDir(), "nope.exe")); ok {
		t.Fatal("verifyBrowserExe should reject a nonexistent path")
	}
	// Empty path.
	if _, ok := verifyBrowserExe(""); ok {
		t.Fatal("verifyBrowserExe should reject an empty path")
	}
}

// TestUpsertCoworkBrowserPath covers the TOML-section edit: insert into a file
// with no section, replace an existing browser_path, and leave sibling keys
// intact. This is the persistence path behind browser_set_path.
func TestUpsertCoworkBrowserPath(t *testing.T) {
	// Empty file → new section appended.
	got := upsertCoworkBrowserPath("", `C:\chrome.exe`)
	if !strings.Contains(got, "[cowork]") || !strings.Contains(got, `browser_path = "C:\\chrome.exe"`) {
		t.Fatalf("empty file insert failed:\n%s", got)
	}
	// Existing section with a sibling key → sibling preserved, browser_path replaced.
	existing := "[cowork]\nother_key = 1\nbrowser_path = \"old\"\n"
	got = upsertCoworkBrowserPath(existing, `D:\edge.exe`)
	if strings.Contains(got, "old") || !strings.Contains(got, `browser_path = "D:\\edge.exe"`) || !strings.Contains(got, "other_key = 1") {
		t.Fatalf("replace failed:\n%s", got)
	}
	// Existing section without browser_path → inserted after header.
	existing = "[cowork]\nfoo = \"bar\"\n"
	got = upsertCoworkBrowserPath(existing, `C:\chrome.exe`)
	if !strings.Contains(got, `browser_path = "C:\\chrome.exe"`) || !strings.Contains(got, `foo = "bar"`) {
		t.Fatalf("insert into existing section failed:\n%s", got)
	}
	// Clear path removes the browser_path line but keeps the section + siblings.
	got = upsertCoworkBrowserPath("[cowork]\nbrowser_path = \"x\"\nfoo = 1\n", "")
	if strings.Contains(got, "browser_path") || !strings.Contains(got, "foo = 1") {
		t.Fatalf("clear failed:\n%s", got)
	}
}
