package config

import (
	"strings"
	"testing"
)

func TestResolveProfileBuiltins(t *testing.T) {
	cfg := Default()
	// dev resolves and has empty overrides (identical to no profile).
	dev, err := cfg.ResolveProfile("dev")
	if err != nil {
		t.Fatalf("dev: %v", err)
	}
	if dev.SystemPromptAddon != "" || dev.Model != "" || len(dev.Plugins) != 0 {
		t.Fatalf("dev builtin must have empty overrides, got %+v", dev)
	}
	// Empty name resolves to dev.
	if got, err := cfg.ResolveProfile(""); err != nil || got.Name != ProfileDev {
		t.Fatalf("empty name should resolve to dev, got %v %v", got, err)
	}
	// cowork resolves with the office prompt addon.
	cw, err := cfg.ResolveProfile("cowork")
	if err != nil {
		t.Fatalf("cowork: %v", err)
	}
	if !strings.Contains(cw.SystemPromptAddon, "coWork") {
		t.Fatalf("cowork builtin missing prompt addon, got %q", cw.SystemPromptAddon)
	}
	if cw.WorkspaceType != "document" {
		t.Fatalf("cowork workspace type = %q, want document", cw.WorkspaceType)
	}
}

func TestResolveProfileCaseInsensitive(t *testing.T) {
	cfg := Default()
	for _, name := range []string{"cowork", "Cowork", "COWORK", " CoWork "} {
		if _, err := cfg.ResolveProfile(name); err != nil {
			t.Fatalf("ResolveProfile(%q): %v", name, err)
		}
	}
	if _, err := cfg.ResolveProfile("nope"); err == nil {
		t.Fatal("unknown profile should error")
	}
}

func TestResolveProfileConfigOverridesBuiltin(t *testing.T) {
	// A [[profiles]] entry with a builtin name replaces the builtin.
	cfg := Default()
	cfg.Profiles = []Profile{
		{Name: "cowork", DisplayName: "My Office", Model: "test-provider/test-provider/test-model-a", Plugins: []string{"wps-ppt"}},
	}
	cw, err := cfg.ResolveProfile("cowork")
	if err != nil {
		t.Fatalf("cowork: %v", err)
	}
	if cw.Model != "test-provider/test-provider/test-model-a" || cw.DisplayName != "My Office" {
		t.Fatalf("config did not override builtin: %+v", cw)
	}
	// The builtin prompt addon is dropped when the config entry replaces it
	// (config wins wholesale). This documents the merge contract.
	if cw.SystemPromptAddon != "" {
		t.Fatalf("config-overridden cowork should drop builtin addon, got %q", cw.SystemPromptAddon)
	}
}

func TestPluginAllowedByProfile(t *testing.T) {
	// nil profile → all allowed (dev behaviour).
	if !PluginAllowedByProfile(nil, "anything") {
		t.Fatal("nil profile should allow all plugins")
	}
	dev := &Profile{Name: "dev"} // empty Plugins list
	if !PluginAllowedByProfile(dev, "codegraph") {
		t.Fatal("empty plugin list should allow all")
	}
	cw := &Profile{Name: "cowork", Plugins: []string{"wps-ppt", "browser-control"}}
	if !PluginAllowedByProfile(cw, "wps-ppt") || !PluginAllowedByProfile(cw, "browser-control") {
		t.Fatal("whitelisted plugins must be allowed")
	}
	if PluginAllowedByProfile(cw, "codegraph") {
		t.Fatal("non-whitelisted plugin must be hidden under cowork")
	}
	// Case-insensitive match (the boot comment documents this contract).
	if !PluginAllowedByProfile(cw, "WPS-PPT") {
		t.Fatal("plugin whitelist matching should be case-insensitive")
	}
}

// The netdev MCP seal: PluginAllowlist turns an empty Plugins list from "all
// allowed" into "NONE allowed", so no external MCP (whose write/exec tools sit
// outside the tool_scope RemovePrefix seal) is visible by default. builtinFloor
// pins the flag so a [[profiles]] override can add names but cannot open the
// floodgates.
func TestPluginAllowlistSealsNetDev(t *testing.T) {
	sealed := &Profile{Name: "netdev", PluginAllowlist: true}
	if PluginAllowedByProfile(sealed, "feishu-bot") {
		t.Fatal("allowlist with empty Plugins must hide every external MCP")
	}
	if PluginAllowedByProfile(sealed, "codegraph") {
		t.Fatal("allowlist must hide coding MCPs too")
	}
	named := &Profile{Name: "netdev", PluginAllowlist: true, Plugins: []string{"my-nms"}}
	if !PluginAllowedByProfile(named, "my-nms") {
		t.Fatal("explicitly named server must be admitted")
	}
	if PluginAllowedByProfile(named, "anything-else") {
		t.Fatal("allowlist must still hide unnamed servers")
	}
	// Unrelated profiles keep the "empty = all" default.
	if !PluginAllowedByProfile(&Profile{Name: "dev"}, "codegraph") {
		t.Fatal("dev without allowlist flag keeps empty-list-allows-all")
	}
	// The builtin netdev profile resolves with the floor applied even when a
	// user override tries to disable the allowlist.
	cfg := Default()
	cfg.Profiles = []Profile{
		{Name: "netdev", DisplayName: "运维", PluginAllowlist: false, Plugins: nil},
	}
	p, err := cfg.ResolveProfile(ProfileNetDev)
	if err != nil {
		t.Fatal(err)
	}
	if !p.PluginAllowlist {
		t.Fatal("builtinFloor must pin netdev PluginAllowlist against user override")
	}
	if p.SealsExecutionTools() != true || !p.SkipProjectInstructions() {
		t.Fatal("builtin floor must keep the tool seal and instruction gate")
	}
	if PluginAllowedByProfile(p, "feishu-bot") {
		t.Fatal("user-installed MCP must stay hidden under the netdev allowlist")
	}
}

func TestPluginHiddenByProfile(t *testing.T) {
	// nil profile and empty HiddenPlugins hide nothing.
	if PluginHiddenByProfile(nil, "codegraph") {
		t.Fatal("nil profile should hide no plugins")
	}
	if PluginHiddenByProfile(&Profile{Name: "dev"}, "codegraph") {
		t.Fatal("empty hidden list should hide nothing")
	}
	// Builtin office/netdev profiles hide the coding-domain MCPs…
	for _, p := range builtinProfiles() {
		if p.Name == ProfileCowork || p.Name == ProfileNetDev {
			for _, name := range []string{"codegraph"} {
				if !PluginHiddenByProfile(&p, name) {
					t.Fatalf("%s profile must hide coding-domain MCP %q", p.Name, name)
				}
			}
		}
	}
	// …while dev must NOT hide them.
	dev := builtinProfiles()[0]
	if dev.Name != ProfileDev {
		t.Fatalf("expected first builtin profile to be dev, got %s", dev.Name)
	}
	if PluginHiddenByProfile(&dev, "codegraph") {
		t.Fatal("dev profile must not hide coding-domain MCPs")
	}
	// User-installed servers are unaffected — the whole point of HiddenPlugins
	// over a Plugins whitelist.
	if PluginHiddenByProfile(&Profile{Name: "cowork", HiddenPlugins: []string{"codegraph"}}, "feishu-bot") {
		t.Fatal("user-installed server must stay visible under HiddenPlugins")
	}
	// Case-insensitive and whitespace-tolerant.
	if !PluginHiddenByProfile(&Profile{HiddenPlugins: []string{" CodeGraph "}}, "codegraph") {
		t.Fatal("hidden matching should be case-insensitive and trimmed")
	}
}

func TestProfileResolveSkillDisabled(t *testing.T) {
	// Additive disables.
	cw := &Profile{Name: "cowork", DisabledSkills: []string{"security-review"}}
	got := cw.ResolveSkillDisabled([]string{"explore"})
	if !got[SkillNameKey("explore")] || !got[SkillNameKey("security-review")] {
		t.Fatalf("additive disable missing: %+v", got)
	}
	// Whitelist re-enables a config-disabled skill.
	cw2 := &Profile{Name: "cowork", EnabledSkills: []string{"explore"}}
	got2 := cw2.ResolveSkillDisabled([]string{"explore", "research"})
	if got2[SkillNameKey("explore")] {
		t.Fatal("whitelist should re-enable a config-disabled skill")
	}
	if !got2[SkillNameKey("research")] {
		t.Fatal("non-whitelisted config-disabled skill should stay disabled")
	}
}

// TestCoworkPromptAddonDropsDisabledRows verifies that disabling a skill removes
// its routing row from the cowork prompt, so the model isn't instructed to call
// a skill the user turned off. The historical bug: ppt-auto disabled → the
// prompt still said `run_skill("ppt-auto", task)` → the model retried instead of
// telling the user to re-enable it.
func TestCoworkPromptAddonDropsDisabledRows(t *testing.T) {
	// Baseline: the full add-on advertises every routing skill. (browser-auto
	// left this list on 2026-09-20 with the built-in browser: its row is gone
	// from the add-on entirely, see TestCoworkPromptDropsBrowserRows.)
	full := CoworkPromptAddon(nil)
	for _, name := range []string{"ppt-auto", "email-auto", "schedule-auto"} {
		if !strings.Contains(full, `run_skill("`+name+`"`) {
			t.Fatalf("full add-on missing routing row for %q", name)
		}
	}

	// Disable ppt-auto: its row must be gone, but other rows must remain.
	filtered := CoworkPromptAddon([]string{"ppt-auto"})
	if strings.Contains(filtered, `run_skill("ppt-auto"`) {
		t.Fatal("disabled skill ppt-auto still has a routing row in the filtered add-on")
	}
	for _, name := range []string{"email-auto", "desktop-auto", "knowledge-auto"} {
		if !strings.Contains(filtered, `run_skill("`+name+`"`) {
			t.Fatalf("filtering ppt-auto wrongly removed unrelated routing row for %q", name)
		}
	}

	// Name matching trims whitespace (SkillNameKey).
	if got := CoworkPromptAddon([]string{" ppt-auto "}); strings.Contains(got, `run_skill("ppt-auto"`) {
		t.Fatal("SkillNameKey normalization failed: ' ppt-auto ' did not match 'ppt-auto'")
	}
}

// TestBrowserFeatureIsFullyOffline keeps the built-in browser's retirement
// consistent across every place that advertises it. The surface was taken
// offline on 2026-09-20 (boot.browserToolsEnabled=false) because the driven
// Chrome process (~270MB resident headless browser plus a per-frame JPEG
// screencast) made the app laggy and memory-hungry.
//
// Prompts and tools must move together: advertising a tool that isn't
// registered makes the model promise an action it cannot take, and staying
// silent about the removal invites it to launch a browser through shell
// commands instead.
func TestBrowserFeatureIsFullyOffline(t *testing.T) {
	if strings.Contains(DefaultSystemPrompt, "- browser_open") {
		t.Error("DefaultSystemPrompt still advertises browser_open — that tool is no longer registered")
	}
	if !strings.Contains(DefaultSystemPrompt, "No browser automation") {
		t.Error("DefaultSystemPrompt should state plainly that this build has no browser automation")
	}
	for _, banned := range []string{`run_skill("browser-auto"`, "browser_open"} {
		if strings.Contains(coworkDefaultPromptAddon, banned) {
			t.Errorf("cowork prompt still routes work to %s — the browser surface is offline", banned)
		}
	}
	for _, p := range builtinProfiles() {
		for _, s := range p.EnabledSkills {
			if strings.EqualFold(strings.TrimSpace(s), "browser-auto") {
				t.Errorf("profile %q whitelists browser-auto, whose tools no longer exist", p.Name)
			}
		}
	}
}

// TestImageUnderstandNeverHiddenByBuiltinProfiles guards the single-track image
// path: image_understand must stay visible to the main model in BOTH builtin
// profiles. The feature relies on a semantic loop — ResolveRefs emits a text
// <image path="..."> reference that tells the model to "call image_understand",
// and that only works if the tool's schema is in Schemas(). If a profile lists
// image_understand in HiddenTools, the reference becomes a dead end and user
// images go unhandled. ZCode's analyze_image follows the same "always visible"
// policy; this test keeps hiq aligned with it across profile changes.
func TestImageUnderstandNeverHiddenByBuiltinProfiles(t *testing.T) {
	cfg := Default()
	for _, name := range []string{ProfileDev, ProfileCowork} {
		prof, err := cfg.ResolveProfile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, hidden := range prof.HiddenTools {
			if hidden == "image_understand" {
				t.Errorf("profile %q hides image_understand — user images would become dead references; "+
					"remove it from HiddenTools or make the image path dual-track", name)
			}
		}
	}
}
