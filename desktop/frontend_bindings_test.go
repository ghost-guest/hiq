package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestWailsBindingsMatchAppMethods guards the shell↔frontend binding surface.
//
// The frontend reaches the shell through window.go.main.App.<Method>. Wails
// generates frontend/wailsjs/go/main/App.{js,d.ts} as a BUILD artifact, and the
// hand-written AppBindings interface in src/lib/bridge.ts is what TypeScript
// checks against — so forgetting `wails generate module` after adding an App
// method leaves the generated wrapper silently stale while everything still
// compiles. That is the shape of the v0.1.21 "the dock shows the page but every
// click is ignored" report: the shipped bindings predated the whole BrowserPanel
// API.
//
// Cheap and file-based on purpose: no build tags, no browser, no fixtures.
func TestWailsBindingsMatchAppMethods(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	srcFiles, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(srcFiles) == 0 {
		t.Fatalf("no shell sources found in %s: %v", dir, err)
	}
	methodRe := regexp.MustCompile(`^func \([a-zA-Z_]+ \*App\) ([A-Z][A-Za-z0-9_]*)`)
	appMethods := map[string]bool{}
	for _, f := range srcFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if m := methodRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				appMethods[m[1]] = true
			}
		}
	}
	if len(appMethods) == 0 {
		t.Fatal("found no exported App methods — the parser is out of date")
	}

	dts := filepath.Join(dir, "frontend", "wailsjs", "go", "main", "App.d.ts")
	raw, err := os.ReadFile(dts)
	if err != nil {
		t.Skipf("generated bindings missing (%s) — run `wails generate module` in desktop/", dts)
	}
	genRe := regexp.MustCompile(`^export function ([A-Za-z0-9_]+)`)
	generated := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if m := genRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			generated[m[1]] = true
		}
	}

	var missing []string
	for m := range appMethods {
		if !generated[m] {
			missing = append(missing, m)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d App method(s) missing from frontend/wailsjs/go/main/App.d.ts:\n  %s\n"+
			"Run `wails generate module` in desktop/ and rebuild the frontend.",
			len(missing), strings.Join(missing, "\n  "))
	}
	t.Logf("%d App methods, all present in the generated bindings", len(appMethods))
}
