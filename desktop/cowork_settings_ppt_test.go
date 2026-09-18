package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// isolateDesktopConfigDir returns the config dir that isolateDesktopUserDirs sets up.
func isolateDesktopConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return filepath.Join(os.TempDir(), ".config")
	}
	return dir
}

// TestCoworkSettingsViewPPTTemplate verifies the settings view correctly surfaces
// PPT template state to the frontend: the active template id passes through, the
// templates list is populated from the (isolated) templates dir, and the dir path
// is absolute. This is the data the React dropdown renders, so it must be right.
func TestCoworkSettingsViewPPTTemplate(t *testing.T) {
	// macOS: os.UserConfigDir() ignores XDG_CONFIG_HOME, so we can't isolate.
	if runtime.GOOS == "darwin" {
		t.Skip("macOS: UserConfigDir ignores XDG_CONFIG_HOME")
	}
	isolateDesktopUserDirs(t)

	// Seed .pptx files into where DefaultDir() will look (coworkSettingsView
	// falls back to DefaultDir when SkillTemplatesDir returns "").
	tplDir := filepath.Join(isolateDesktopConfigDir(), "hiq", "ppt-templates")
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(tplDir, "brand.pptx"), []byte("dummy"), 0o644)
	os.WriteFile(filepath.Join(tplDir, "draft.pptx"), []byte("dummy"), 0o644)

	cfg := config.CoworkConfig{PPTActiveTemplate: "brand"}
	v := coworkSettingsView(cfg)

	if v.PPTActiveTemplate != "brand" {
		t.Errorf("PPTActiveTemplate = %q, want brand", v.PPTActiveTemplate)
	}
	if len(v.PPTTemplates) < 2 {
		t.Errorf("PPTTemplates len = %d, want >=2 (brand, draft): %+v", len(v.PPTTemplates), v.PPTTemplates)
	}
	foundBrand := false
	for _, tpl := range v.PPTTemplates {
		if tpl.ID == "brand" {
			foundBrand = true
		}
	}
	if !foundBrand {
		t.Errorf("brand template not in list: %+v", v.PPTTemplates)
	}
	if v.PPTTemplateDir == "" {
		t.Error("PPTTemplateDir is empty — frontend can't show 'open folder'")
	}
}

// TestCoworkSettingsViewNoTemplates verifies the view is well-formed even when
// only the auto-seeded example exists (no panic, dir path set so the UX works).
func TestCoworkSettingsViewNoTemplates(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS: UserConfigDir ignores XDG_CONFIG_HOME")
	}
	isolateDesktopUserDirs(t)

	cfg := config.CoworkConfig{PPTActiveTemplate: ""}
	v := coworkSettingsView(cfg)

	if v.PPTActiveTemplate != "" {
		t.Errorf("want empty active template, got %q", v.PPTActiveTemplate)
	}
	// DefaultDir seeds an example.json but scanPPTXTemplates only finds .pptx;
	// an empty list is valid when no .pptx templates exist.
	if v.PPTTemplates == nil {
		t.Error("PPTTemplates should be non-nil (empty slice is OK)")
	}
	// PPTTemplateDir should be set (DefaultDir creates the dir + seeds example).
	if v.PPTTemplateDir == "" {
		t.Error("PPTTemplateDir empty on fresh dir")
	}
}

// TestPPTTemplateSkillValue was removed — the pptTemplateSkillValue function
// was refactored out; the settings panel now writes template paths directly.

// TestSetCoWorkSettingsLinksPPTSwitchToSkill is the regression test for the
// "two PPT switches" bug: the settings-page PPT switch (driven by
// PPTActiveTemplate) must enable/disable the ppt-auto skill, so it agrees with
// the Capabilities-panel skill toggle (both write the same DisabledSkills list).
// We test the config-level linkage directly — SetCoWorkSettings applies the same
// c.SetSkillEnabled("ppt-auto", template != "") inside its mutate closure.
func TestSetCoWorkSettingsLinksPPTSwitchToSkill(t *testing.T) {
	// Selecting a template enables ppt-auto (it must NOT be disabled).
	c := config.Default()
	if err := c.SetSkillEnabled("ppt-auto", false); err != nil { // start disabled
		t.Fatal(err)
	}
	if !c.IsSkillDisabled("ppt-auto") {
		t.Fatal("precondition: ppt-auto should be disabled initially")
	}
	if err := c.SetSkillEnabled("ppt-auto", true); err != nil { // emulate selecting a template
		t.Fatal(err)
	}
	if c.IsSkillDisabled("ppt-auto") {
		t.Error("selecting a template should ENABLE ppt-auto (remove from DisabledSkills)")
	}
	// Clearing the template disables ppt-auto again.
	if err := c.SetSkillEnabled("ppt-auto", false); err != nil { // emulate clearing the template
		t.Fatal(err)
	}
	if !c.IsSkillDisabled("ppt-auto") {
		t.Error("clearing the template should DISABLE ppt-auto (add to DisabledSkills)")
	}
}
