package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadPresetsSeedsDefaults: a fresh install (no presets file) yields each
// mode's factory presets with NOTHING active — model behaviour (and the
// cache-stable prompt prefix) stays untouched until the user picks one.
func TestLoadPresetsSeedsDefaults(t *testing.T) {
	dir := t.TempDir()
	wantCount := map[string]int{"cowork": 3, "dev": 4}

	for _, mode := range []string{"cowork", "dev"} {
		f := LoadPresets(dir, mode)
		if len(f.Items) != wantCount[mode] {
			t.Fatalf("%s defaults: want %d items, got %d", mode, wantCount[mode], len(f.Items))
		}
		if f.Active != "" || f.ActivePreset() != nil {
			t.Fatalf("%s defaults: want no active preset, got %q", mode, f.Active)
		}
		for _, it := range f.Items {
			if !it.Builtin || it.Name == "" || it.Content == "" {
				t.Fatalf("%s default preset not fully formed: %+v", mode, it)
			}
		}
		// Seeding is in-memory only — nothing hits disk until the user saves.
		if _, err := os.Stat(presetsPath(dir, mode)); !os.IsNotExist(err) {
			t.Fatalf("LoadPresets(%s) wrote a file on read; defaults must stay in memory", mode)
		}
	}

	// The two modes' presets must not share ids: restore-builtins dedupes by id.
	seen := map[string]bool{}
	for _, it := range LoadPresets(dir, "cowork").Items {
		seen[it.ID] = true
	}
	for _, it := range LoadPresets(dir, "dev").Items {
		if seen[it.ID] {
			t.Fatalf("preset id %q shared between cowork and dev defaults", it.ID)
		}
	}
}

// TestSavePresetsRoundTrip verifies save→load keeps items and the active id,
// and that normalization fills ids, trims names, drops blank rows and clears a
// dangling active.
func TestSavePresetsRoundTrip(t *testing.T) {
	dir := t.TempDir()

	saved := PresetFile{
		Active: "keep",
		Items: []ProfilePreset{
			{ID: "keep", Name: "  严格Excel匹配  ", Content: "忠于原表"},
			{ID: "", Name: "新增", Content: "x"},        // id filled in
			{ID: "keep", Name: "重复id", Content: "y"},  // id regenerated
			{ID: "blank", Name: "  ", Content: "   "}, // dropped
		},
	}
	if _, err := SavePresets(dir, "cowork", saved); err != nil {
		t.Fatalf("SavePresets: %v", err)
	}
	got := LoadPresets(dir, "cowork")
	if len(got.Items) != 3 {
		t.Fatalf("want 3 items after normalize, got %d (%+v)", len(got.Items), got.Items)
	}
	if got.Items[0].Name != "严格Excel匹配" {
		t.Fatalf("name not trimmed: %q", got.Items[0].Name)
	}
	ids := map[string]bool{}
	for _, it := range got.Items {
		if it.ID == "" || ids[it.ID] {
			t.Fatalf("ids not unique/non-empty: %+v", got.Items)
		}
		ids[it.ID] = true
	}
	if got.Active != "keep" {
		t.Fatalf("active not preserved: %q", got.Active)
	}
}

// TestSavePresetsClearsDanglingActive: an active id pointing at a deleted item
// normalizes to "" (nothing in use), not a silent wrong selection.
func TestSavePresetsClearsDanglingActive(t *testing.T) {
	dir := t.TempDir()
	if _, err := SavePresets(dir, "cowork", PresetFile{
		Active: "gone",
		Items:  []ProfilePreset{{ID: "one", Name: "A", Content: "a"}},
	}); err != nil {
		t.Fatalf("SavePresets: %v", err)
	}
	if got := LoadPresets(dir, "cowork"); got.Active != "" {
		t.Fatalf("dangling active not cleared: %q", got.Active)
	}
}

// TestDiscoverProfileInjectsActivePreset: the selected preset lands in the
// injected portrait under its own heading, after the mode file, and an empty or
// cleared selection adds nothing.
func TestDiscoverProfileInjectsActivePreset(t *testing.T) {
	dir := t.TempDir()
	prof := filepath.Join(dir, profileDir)
	mustMkdir(t, prof)
	mustWrite(t, filepath.Join(prof, "cowork.md"), "# 办公画像\n\n深蓝配色。")

	if _, err := SavePresets(dir, "cowork", PresetFile{
		Active: "concise",
		Items:  []ProfilePreset{{ID: "concise", Name: "少描述只给总结", Content: "只给结论。"}},
	}); err != nil {
		t.Fatalf("SavePresets: %v", err)
	}

	set := Load(Options{UserDir: dir, Profile: "cowork"})
	p := set.Profile
	if !strings.Contains(p, "# 当前使用的偏好模板：少描述只给总结") || !strings.Contains(p, "只给结论。") {
		t.Fatalf("active preset not injected: %q", p)
	}
	if !strings.Contains(p, "# 办公画像") {
		t.Fatalf("portrait file missing from injection: %q", p)
	}
	if strings.Index(p, "# 办公画像") > strings.Index(p, "# 当前使用的偏好模板") {
		t.Fatalf("preset must come after the portrait files: %q", p)
	}

	// No selection → no preset section, portrait untouched.
	if _, err := SavePresets(dir, "cowork", PresetFile{
		Items: []ProfilePreset{{ID: "concise", Name: "少描述只给总结", Content: "只给结论。"}},
	}); err != nil {
		t.Fatalf("SavePresets: %v", err)
	}
	set = Load(Options{UserDir: dir, Profile: "cowork"})
	if strings.Contains(set.Profile, "偏好模板") {
		t.Fatalf("preset injected without a selection: %q", set.Profile)
	}
}

// TestDiscoverProfilePresetUnderBudget: the preset participates in the shared
// profileMaxChars cap — an oversized portrait+preset is truncated with the
// visible marker rather than bloating every turn.
func TestDiscoverProfilePresetUnderBudget(t *testing.T) {
	dir := t.TempDir()
	prof := filepath.Join(dir, profileDir)
	mustMkdir(t, prof)
	mustWrite(t, filepath.Join(prof, "cowork.md"), strings.Repeat("画", profileMaxChars))

	if _, err := SavePresets(dir, "cowork", PresetFile{
		Active: "x",
		Items:  []ProfilePreset{{ID: "x", Name: "模板", Content: "收尾内容"}},
	}); err != nil {
		t.Fatalf("SavePresets: %v", err)
	}
	set := Load(Options{UserDir: dir, Profile: "cowork"})
	if r := []rune(set.Profile); len(r) <= profileMaxChars {
		t.Fatalf("expected truncation marker to extend past the cap, got %d runes", len(r))
	}
	if !strings.Contains(set.Profile, "portrait truncated") {
		t.Fatalf("truncation marker missing: %q", tail(set.Profile, 120))
	}
}

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
