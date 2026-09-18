package assets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SkillVersion is bumped whenever the embedded ppt-auto payload changes in a way
// that should force a refresh of the released copy. Bump this when you update
// the embedded scripts/templates/SKILL.md and want existing users to get the
// new version on next launch.
const SkillVersion = "48" // 48: decisions — CJK width is the sole overflow criterion (S-05 enforced), fast mode gains hard content-floor/overflow/overlap checks (region blocks exempt via metadata), config.py dead SVG_CONSTRAINTS removed (rules live in template_config.json)

// versionFileName is written into the released skill dir so we can tell whether
// the on-disk copy matches the embedded version.
const versionFileName = ".embedded-version"

// skillDirName is the directory name the skill is released under.
const skillDirName = "ppt-auto"

// embedRoot is the path prefix inside the embed.FS (the directory name passed
// to //go:embed, which must NOT start with a dot or Go rejects the directive).
// It differs from skillDirName (the release name) by design: the embed tree is
// a build-time staging dir, the release dir is what users see on disk.
const embedRoot = "pptauto"

// EnsurePPTAutoSkill releases the embedded ppt-auto skill to the user's global
// skills directory (~/.hiq/skills/ppt-auto) if it is missing or stale.
// It is idempotent: when the on-disk .embedded-version matches SkillVersion,
// it does nothing. On a version bump it overwrites the existing copy.
//
// The release target is the global-scope skill root that the skill store scans
// (internal/skill: Store.roots → home/.hiq/skills), so both the CLI and the
// desktop app discover the released skill without any discovery-code changes.
//
// A nil error is returned (best-effort): a failure to release is logged but does
// not abort startup, because the user may already have a working ppt-auto from a
// previous release or a manual install. The error is still propagated so callers
// can log it.
func EnsurePPTAutoSkill() error {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return errors.New("assets: cannot determine user home dir")
	}
	dst := filepath.Join(home, ".hiq", "skills", skillDirName)

	// Skip if the on-disk copy is already at the embedded version.
	if current, ok := readVersion(dst); ok && current == SkillVersion {
		return nil
	}

	// Walk the embedded tree and write every entry under dst.
	if err := fs.WalkDir(pptauto, embedRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		// Map the embed path (prefix "pptauto") onto the destination.
		rel := strings.TrimPrefix(path, embedRoot)
		rel = strings.TrimPrefix(rel, string(filepath.Separator))
		target := filepath.Join(dst, rel)
		if rel == "" {
			target = dst
		}

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rerr := pptauto.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("read embedded %s: %w", path, rerr)
		}
		if werr := os.MkdirAll(filepath.Dir(target), 0o755); werr != nil {
			return fmt.Errorf("mkdir for %s: %w", target, werr)
		}
		// Skip writing if the target already exists with identical content.
		// This avoids rewriting 11000+ icon files on every SkillVersion bump
		// when only scripts changed — the biggest startup-latency source.
		if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, data) {
			return nil
		}
		// Write atomically-ish: write then chmod. Preserve executability for
		// shell scripts on POSIX (cosmetic on Windows).
		mode := os.FileMode(0o644)
		if shouldExec(rel) {
			mode = 0o755
		}
		if werr := os.WriteFile(target, data, mode); werr != nil {
			return fmt.Errorf("write %s: %w", target, werr)
		}
		return nil
	}); err != nil {
		return err
	}

	// Stamp the version so we skip the walk next launch unless the embedded
	// version changes. Best-effort: a failure here just means we re-walk once.
	_ = os.WriteFile(filepath.Join(dst, versionFileName), []byte(SkillVersion), 0o644)
	return nil
}

// PPTAutoSkillDir returns the absolute path where EnsurePPTAutoSkill releases
// the embedded skill (~/.hiq/skills/ppt-auto), regardless of whether it
// has been released yet. Useful for callers that want the canonical location.
func PPTAutoSkillDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("assets: cannot determine user home dir")
	}
	return filepath.Join(home, ".hiq", "skills", skillDirName), nil
}

// PPTAutoTemplatesDir returns the released skill's templates/ directory, or ""
// if it doesn't exist. Used by the settings page to surface bundled templates.
func PPTAutoTemplatesDir() string {
	dir, err := PPTAutoSkillDir()
	if err != nil {
		return ""
	}
	t := filepath.Join(dir, "templates")
	if _, err := os.Stat(t); err == nil {
		return t
	}
	return ""
}

// PPTAutoConfigPath returns the released skill's template_config.json path, or ""
// if it doesn't exist. Used by the settings page to read/update PPT config.
func PPTAutoConfigPath() string {
	dir, err := PPTAutoSkillDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, "template_config.json")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// SyncPPTMode writes the user's ppt_mode setting (from config.toml) into the
// released template_config.json's "mode" field. This must be called after
// EnsurePPTAutoSkill at startup, because the embedded config ships with
// mode="fast" and would overwrite the user's "validate" choice on every
// SkillVersion bump.
func SyncPPTMode(pptMode string) error {
	if pptMode != "fast" && pptMode != "validate" {
		return nil // ignore invalid values
	}
	configPath := PPTAutoConfigPath()
	if configPath == "" {
		return nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	cfg["mode"] = pptMode
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, out, 0o644)
}

// readVersion reads the .embedded-version marker from a released skill dir.
// Returns ("", false) if the dir or marker is absent.
func readVersion(skillDir string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(skillDir, versionFileName))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// shouldExec reports whether a released file should be marked executable.
// Only the shell setup script needs the bit; .bat is a no-op on Windows.
func shouldExec(rel string) bool {
	return strings.HasSuffix(rel, ".sh")
}

// helperScriptsDir is where EnsureHelperScripts releases the embedded helper
// scripts (~/.hiq/scripts) — a stable location probed by
// docconv.ScriptCandidates regardless of where the binary runs from.
func helperScriptsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("assets: cannot determine user home dir")
	}
	return filepath.Join(home, ".hiq", "scripts"), nil
}

// EnsureHelperScripts releases the embedded helper scripts (scripts/ tree) to
// ~/.hiq/scripts/, writing each file only when missing or content differs
// (so edits to the embedded copy propagate on upgrade). Best-effort: errors
// surface to the caller but a missing script just means the Go-side fallback
// probes (CWD / exe-relative) still apply.
func EnsureHelperScripts() error {
	dir, err := helperScriptsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("assets: create %s: %w", dir, err)
	}
	return fs.WalkDir(scripts, "scripts", func(path string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		data, rerr := scripts.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dir, filepath.Base(path))
		if existing, ferr := os.ReadFile(target); ferr == nil && bytes.Equal(existing, data) {
			return nil // unchanged — skip the write
		}
		return os.WriteFile(target, data, 0o644)
	})
}
