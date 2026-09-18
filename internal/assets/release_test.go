package assets

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsurePPTAutoSkill_ReleasesEmbeddedTree verifies that EnsurePPTAutoSkill
// actually walks the embedded ppt-auto tree and writes it under the user's
// ~/.hiq/skills/ppt-auto/, including dot/underscore entries that `all:` is
// required for. It uses a fake HOME so it never touches the real user dir.
func TestEnsurePPTAutoSkill_ReleasesEmbeddedTree(t *testing.T) {
	// Sandbox HOME so the test never writes to the real ~/.hiq.
	tmp := t.TempDir()
	// t.Setenv automatically handles cleanup, no need for manual defer/unset
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

	if err := EnsurePPTAutoSkill(); err != nil {
		t.Fatalf("EnsurePPTAutoSkill: %v", err)
	}

	// The canonical release dir.
	dir, err := PPTAutoSkillDir()
	if err != nil {
		t.Fatalf("PPTAutoSkillDir: %v", err)
	}

	// SKILL.md must be released.
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md not released: %v", err)
	}
	// setup_python.sh (executable) and setup_python.bat must be released.
	for _, f := range []string{"setup_python.sh", "setup_python.bat", "requirements.txt", "template_config.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not released: %v", f, err)
		}
	}
	// scripts/ and templates/ subtrees must be present.
	if _, err := os.Stat(filepath.Join(dir, "scripts", "svg_to_pptx.py")); err != nil {
		t.Errorf("scripts/svg_to_pptx.py not released: %v", err)
	}
	// Underscore-prefixed entries require the `all:` embed prefix; their presence
	// proves all: is wired correctly (a bare embed would silently drop them).
	foundUnderscore := false
	_ = filepath.Walk(filepath.Join(dir, "templates"), func(p string, _ os.FileInfo, _ error) error {
		if filepath.Base(p) == "_index.md" {
			foundUnderscore = true
		}
		return nil
	})
	if !foundUnderscore {
		t.Errorf("_index.md (underscore entry) not released — all: prefix broken")
	}

	// Version marker must be written so a second call is a no-op.
	v, ok := readVersion(dir)
	if !ok {
		t.Fatalf("version marker not written")
	}
	if v != SkillVersion {
		t.Fatalf("version = %q, want %q", v, SkillVersion)
	}

	// Idempotency: calling again must succeed and not error.
	if err := EnsurePPTAutoSkill(); err != nil {
		t.Fatalf("second EnsurePPTAutoSkill: %v", err)
	}
}
