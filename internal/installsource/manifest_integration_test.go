package installsource

import (
	"os"
	"path/filepath"
	"testing"
)

// TestApplyCopySkill_RecordsManifest confirms a real copy install writes the
// skill's provenance (source + hash) into the per-scope .installed.json.
func TestApplyCopySkill_RecordsManifest(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	tl := NewTool(Options{ProjectRoot: project, HomeDir: home})
	it := tl.(*installSourceTool)

	// Install a skill via the apply path.
	req := request{Kind: "skill", Scope: "project"}
	act := it.skillAction(req, skillCandidate{
		Name:    "provenance-test",
		Content: "---\nname: provenance-test\ndescription: tests manifest recording\n---\n# Test\nA body.",
	}, "copy")
	if err := it.applyCopySkill(req, &act); err != nil {
		t.Fatalf("applyCopySkill: %v", err)
	}

	// The manifest should now contain the skill with source + hash.
	skillsRoot := filepath.Join(project, ".hiq", "skills")
	m := loadManifest(skillsRoot)
	entry, ok := m.Skills["provenance-test"]
	if !ok {
		t.Fatal("installed skill should be recorded in the manifest")
	}
	if entry.ContentHash == "" {
		t.Error("ContentHash should be recorded")
	}
	if entry.Mode != "copy" {
		t.Errorf("Mode = %q, want copy", entry.Mode)
	}
}

// TestApplyRemoveSkill_ForgetsManifest confirms uninstall removes the provenance
// entry alongside the skill files.
func TestApplyRemoveSkill_ForgetsManifest(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	tl := NewTool(Options{ProjectRoot: project, HomeDir: home})
	it := tl.(*installSourceTool)

	// Install, then uninstall.
	req := request{Kind: "skill", Scope: "project"}
	act := it.skillAction(req, skillCandidate{
		Name:    "to-remove",
		Content: "---\nname: to-remove\ndescription: x\n---\nbody",
	}, "copy")
	if err := it.applyCopySkill(req, &act); err != nil {
		t.Fatalf("applyCopySkill: %v", err)
	}
	skillsRoot := filepath.Join(project, ".hiq", "skills")
	if _, ok := loadManifest(skillsRoot).Skills["to-remove"]; !ok {
		t.Fatal("skill should be in manifest after install")
	}

	// Uninstall.
	removeAct := action{
		Action: "remove_skill",
		Target: act.Target,
		Scope:  "project",
		Skills: []string{"to-remove"},
	}
	if err := it.applyRemoveSkill(req, &removeAct); err != nil {
		t.Fatalf("applyRemoveSkill: %v", err)
	}
	// The skill dir is gone.
	if _, err := os.Stat(act.Target); err == nil {
		t.Error("skill file should be removed")
	}
	// The manifest entry is gone too.
	if _, ok := loadManifest(skillsRoot).Skills["to-remove"]; ok {
		t.Error("skill should be forgotten from manifest after uninstall")
	}
}
