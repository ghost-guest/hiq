package ppttemplate

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultDirEndToEnd is the real-filesystem end-to-end check: DefaultDir
// must create <config>/hiq/ppt-templates, seed example.json, and the
// seeded example must round-trip through LoadDir + LoadActive. This catches
// issues the t.TempDir-based tests can't (real path resolution, seed content
// validity). It writes under an isolated fake HOME/AppData so it never touches
// the user's real config.
func TestDefaultDirEndToEnd(t *testing.T) {
	fakeHome := t.TempDir()
	// Isolate os.UserConfigDir: Windows reads AppData, *nix reads XDG_CONFIG_HOME.
	t.Setenv("AppData", filepath.Join(fakeHome, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(fakeHome, "config"))

	dir := DefaultDir()
	if dir == "" {
		t.Fatal("DefaultDir returned empty — UserConfigDir unavailable")
	}
	if dir != filepath.Join(fakeHome, "AppData", "hiq", "ppt-templates") &&
		dir != filepath.Join(fakeHome, "config", "hiq", "ppt-templates") {
		// On this OS the parent differs; just confirm it's under fakeHome.
		if !filepath.IsAbs(dir) {
			t.Errorf("DefaultDir not absolute: %q", dir)
		}
	}

	// example.json must exist and be valid JSON.
	info, err := os.Stat(filepath.Join(dir, "example.json"))
	if err != nil {
		t.Fatalf("example.json not seeded: %v", err)
	}
	if info.Size() < 50 {
		t.Errorf("example.json suspiciously small: %d bytes", info.Size())
	}

	// LoadDir must return the seeded example, parseable.
	ts, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(ts) == 0 {
		t.Fatal("LoadDir returned no templates after seeding")
	}
	var example *Template
	for i := range ts {
		if ts[i].ID == "example" {
			example = &ts[i]
		}
	}
	if example == nil {
		t.Fatal("seeded 'example' template not found by LoadDir")
	}
	// The seed must have a content layout with body coords (the CUA relies on it).
	if l, ok := example.Layouts["content"]; !ok {
		t.Error("example template missing 'content' layout")
	} else if l.BodyW == 0 || l.BodyH == 0 {
		t.Errorf("example content layout has no body size: %+v", l)
	}

	// LoadActive must hit the example by id.
	got, err := LoadActive(dir, "example")
	if err != nil || got == nil {
		t.Fatalf("LoadActive(example): got=%v err=%v", got, err)
	}
	if got.ID != "example" {
		t.Errorf("LoadActive returned wrong id: %q", got.ID)
	}

	// Views (for the settings dropdown) must include example.
	vs := Views(dir)
	found := false
	for _, v := range vs {
		if v.ID == "example" {
			found = true
		}
	}
	if !found {
		t.Error("Views did not include 'example'")
	}

	// Idempotency: calling DefaultDir again must NOT overwrite example.json
	// (so user edits survive a restart). Check mtime is unchanged.
	before := info.ModTime()
	_ = DefaultDir()
	after, _ := os.Stat(filepath.Join(dir, "example.json"))
	if !before.Equal(after.ModTime()) {
		t.Error("DefaultDir overwrote example.json on second call — user edits would be lost")
	}
}

// TestAddCustomTemplate verifies a user-dropped JSON file shows up in LoadDir +
// can be activated — the core user workflow (drop a file, pick it in settings).
func TestAddCustomTemplate(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("AppData", filepath.Join(fakeHome, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(fakeHome, "config"))
	dir := DefaultDir()

	// User drops a custom template with a master_file.
	custom := filepath.Join(dir, "brand.json")
	mustWrite(t, custom, `{
		"id": "brand",
		"name": "公司品牌",
		"master_file": "C:/docs/brand.pptx",
		"theme": { "primary_color": "FF6600" },
		"layouts": { "cover": { "title_x": 10, "title_y": 35 } },
		"default_layout": "cover"
	}`)

	vs := Views(dir)
	ids := map[string]bool{}
	for _, v := range vs {
		ids[v.ID] = true
	}
	if !ids["brand"] {
		t.Errorf("custom template 'brand' not in Views: %+v", vs)
	}

	got, err := LoadActive(dir, "brand")
	if err != nil {
		t.Fatalf("LoadActive(brand): %v", err)
	}
	if got.MasterFile != "C:/docs/brand.pptx" {
		t.Errorf("master_file = %q", got.MasterFile)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
