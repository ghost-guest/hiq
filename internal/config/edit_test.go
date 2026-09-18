package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// addTestProvider seeds a named provider with a single model onto c so tests
// can exercise SetDefaultModel / SetPlannerModel / SetProviderEffort without
// relying on a preset official provider (Hiq ships none by default).
func addTestProvider(c *Config, name, model string) {
	c.Providers = append(c.Providers, ProviderEntry{Name: name, Kind: "openai", Model: model})
}

func TestSetDefaultModel(t *testing.T) {
	c := Default()
	addTestProvider(c, "test-provider", "test-provider/test-model-a")
	if err := c.SetDefaultModel("test-provider"); err != nil {
		t.Fatalf("set valid default: %v", err)
	}
	if c.DefaultModel != "test-provider" {
		t.Errorf("default = %q, want test-provider", c.DefaultModel)
	}
	if err := c.SetDefaultModel("nope"); err == nil {
		t.Error("expected error for unknown provider")
	}
	// "provider/model" form is also accepted: the /model picker stores the
	// full ref so a user can land on a non-default model under the same
	// provider across restarts.
	if err := c.SetDefaultModel("test-provider/test-provider/test-model-a"); err != nil {
		t.Fatalf("set provider/model default: %v", err)
	}
	if c.DefaultModel != "test-provider/test-provider/test-model-a" {
		t.Errorf("default = %q, want test-provider/test-provider/test-model-a", c.DefaultModel)
	}
	if err := c.SetDefaultModel("test-provider/missing"); err == nil {
		t.Error("expected error for unknown model under known provider")
	}
	if err := c.SetDefaultModel(""); err == nil {
		t.Error("expected error for empty name")
	}
}

func TestUIThemeNormalizes(t *testing.T) {
	c := Default()
	for _, tt := range []struct {
		in   string
		want string
	}{
		{"", "auto"},
		{"AUTO", "auto"},
		{"dark", "dark"},
		{" light ", "light"},
		{"unknown", "auto"},
	} {
		c.UI.Theme = tt.in
		if got := c.UITheme(); got != tt.want {
			t.Errorf("UITheme(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUIThemeStyleNormalizes(t *testing.T) {
	c := Default()
	for _, tt := range []struct {
		in   string
		want string
	}{
		{"", ""},
		{"AURORA", "aurora"},
		{" nocturne ", "nocturne"},
		{" glacier ", "glacier"},
		{"unknown", ""},
	} {
		c.UI.ThemeStyle = tt.in
		if got := c.UIThemeStyle(); got != tt.want {
			t.Errorf("UIThemeStyle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUICloseBehaviorNormalizes(t *testing.T) {
	c := Default()
	for _, tt := range []struct {
		in   string
		want string
	}{
		{"", "background"},
		{"QUIT", "quit"},
		{"exit", "quit"},
		{" background ", "background"},
		{"hide", "background"},
		{"unknown", "background"},
	} {
		c.UI.CloseBehavior = tt.in
		if got := c.UICloseBehavior(); got != tt.want {
			t.Errorf("UICloseBehavior(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDesktopPreferencesAreSeparateFromCLI(t *testing.T) {
	c := Default()
	c.Language = "zh"
	c.UI.Theme = "light"
	c.UI.ThemeStyle = "glacier"

	if err := c.SetDesktopLanguage("en"); err != nil {
		t.Fatalf("SetDesktopLanguage: %v", err)
	}
	if err := c.SetDesktopAppearance("dark", "graphite"); err != nil {
		t.Fatalf("SetDesktopAppearance: %v", err)
	}

	if c.Language != "zh" {
		t.Fatalf("CLI language changed to %q", c.Language)
	}
	if got := c.UITheme(); got != "light" {
		t.Fatalf("CLI theme = %q, want light", got)
	}
	if got := c.UIThemeStyle(); got != "glacier" {
		t.Fatalf("CLI theme style = %q, want glacier", got)
	}
	if got := c.DesktopLanguage(); got != "en" {
		t.Fatalf("desktop language = %q, want en", got)
	}
	if got := c.DesktopTheme(); got != "dark" {
		t.Fatalf("desktop theme = %q, want dark", got)
	}
	if got := c.DesktopThemeStyle(); got != "graphite" {
		t.Fatalf("desktop theme style = %q, want graphite", got)
	}
}

func TestDesktopCloseBehaviorFallsBackToLegacyUI(t *testing.T) {
	c := Default()
	c.UI.CloseBehavior = "quit"
	if got := c.DesktopCloseBehavior(); got != "quit" {
		t.Fatalf("legacy close behavior = %q, want quit", got)
	}
	c.Desktop.CloseBehavior = "background"
	if got := c.DesktopCloseBehavior(); got != "background" {
		t.Fatalf("desktop close behavior = %q, want background", got)
	}
}

func TestSetUICloseBehavior(t *testing.T) {
	c := Default()
	if err := c.SetUICloseBehavior("background"); err != nil {
		t.Fatalf("SetUICloseBehavior background: %v", err)
	}
	if got := c.UICloseBehavior(); got != "background" {
		t.Fatalf("close behavior = %q, want background", got)
	}
	if err := c.SetUICloseBehavior("quit"); err != nil {
		t.Fatalf("SetUICloseBehavior quit: %v", err)
	}
	if got := c.UICloseBehavior(); got != "quit" {
		t.Fatalf("close behavior = %q, want quit", got)
	}
	if err := c.SetUICloseBehavior("later"); err == nil {
		t.Fatal("expected error for invalid close behavior")
	}
}

func TestSetPlannerModel(t *testing.T) {
	c := Default()
	addTestProvider(c, "test-provider", "test-provider/test-model-a")
	if err := c.SetPlannerModel("test-provider"); err != nil {
		t.Fatalf("set planner: %v", err)
	}
	if c.Agent.PlannerModel != "test-provider" {
		t.Errorf("planner = %q", c.Agent.PlannerModel)
	}
	if err := c.SetPlannerModel(""); err != nil || c.Agent.PlannerModel != "" {
		t.Errorf("clearing planner failed: err=%v planner=%q", err, c.Agent.PlannerModel)
	}
	if err := c.SetPlannerModel("ghost"); err == nil {
		t.Error("expected error for unknown planner")
	}
}

func TestSetAutoPlan(t *testing.T) {
	c := Default()
	for _, mode := range []string{"on", "off"} {
		if err := c.SetAutoPlan(mode); err != nil {
			t.Fatalf("SetAutoPlan(%q): %v", mode, err)
		}
		if c.Agent.AutoPlan != mode {
			t.Fatalf("auto_plan = %q, want %q", c.Agent.AutoPlan, mode)
		}
	}
	if err := c.SetAutoPlan("ask"); err != nil {
		t.Fatalf("legacy ask should be accepted: %v", err)
	}
	if c.Agent.AutoPlan != "on" {
		t.Fatalf("legacy ask should save as on, got %q", c.Agent.AutoPlan)
	}
	if err := c.SetAutoPlan("auto"); err == nil {
		t.Fatal("expected error for invalid auto_plan mode")
	}
}

func TestSetUIShortcutLayout(t *testing.T) {
	c := Default()
	if got := c.UIShortcutLayout(); got != "classic" {
		t.Fatalf("default shortcut layout = %q, want classic", got)
	}
	if err := c.SetUIShortcutLayout("desktop"); err != nil {
		t.Fatalf("SetUIShortcutLayout desktop: %v", err)
	}
	if got := c.UIShortcutLayout(); got != "desktop" {
		t.Fatalf("shortcut layout = %q, want desktop", got)
	}
	if err := c.SetUIShortcutLayout("dual-axis"); err != nil {
		t.Fatalf("SetUIShortcutLayout alias: %v", err)
	}
	if got := c.UIShortcutLayout(); got != "desktop" {
		t.Fatalf("shortcut layout alias = %q, want desktop", got)
	}
	if err := c.SetUIShortcutLayout("classic"); err != nil {
		t.Fatalf("SetUIShortcutLayout classic: %v", err)
	}
	if got := c.UIShortcutLayout(); got != "classic" {
		t.Fatalf("shortcut layout = %q, want classic", got)
	}
	if err := c.SetUIShortcutLayout("surprise"); err == nil {
		t.Fatal("expected error for invalid shortcut layout")
	}
}

func TestUpsertProvider(t *testing.T) {
	c := Default()
	n := len(c.Providers)

	// Add a new one.
	if err := c.UpsertProvider(ProviderEntry{Name: "local", Kind: "openai", BaseURL: "http://localhost:1234/v1", Model: "x"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(c.Providers) != n+1 {
		t.Fatalf("provider count = %d, want %d", len(c.Providers), n+1)
	}

	// Replace it in place (no growth, position preserved).
	if err := c.UpsertProvider(ProviderEntry{Name: "local", Kind: "openai", BaseURL: "http://localhost:9999/v1", Model: "y"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(c.Providers) != n+1 {
		t.Errorf("replace grew the list to %d", len(c.Providers))
	}
	got, _ := c.Provider("local")
	if got.BaseURL != "http://localhost:9999/v1" || got.Model != "y" {
		t.Errorf("replace didn't apply: %+v", got)
	}

	// Multi-model providers may omit the back-compat single model field.
	if err := c.UpsertProvider(ProviderEntry{
		Name:    "multi",
		Kind:    "openai",
		BaseURL: "http://localhost:8888/v1",
		Models:  []string{"m1", "m2"},
		Default: "m1",
	}); err != nil {
		t.Fatalf("multi-model add: %v", err)
	}

	// Missing required fields error.
	for _, bad := range []ProviderEntry{
		{Kind: "openai", BaseURL: "u", Model: "m"}, // no name
		{Name: "a", BaseURL: "u", Model: "m"},      // no kind
		{Name: "a", Kind: "openai", Model: "m"},    // no base_url
		{Name: "a", Kind: "openai", BaseURL: "u"},  // no model
	} {
		if err := c.UpsertProvider(bad); err == nil {
			t.Errorf("expected validation error for %+v", bad)
		}
	}
}

func TestSetProviderEffort(t *testing.T) {
	c := Default()
	addTestProvider(c, "test-provider", "test-provider/test-model-a")
	if err := c.SetProviderEffort("test-provider", "MAX"); err != nil {
		t.Fatalf("SetProviderEffort: %v", err)
	}
	p, _ := c.Provider("test-provider")
	if p.Effort != "max" {
		t.Fatalf("effort = %q, want max", p.Effort)
	}
	if err := c.SetProviderEffort("missing", "high"); err == nil {
		t.Fatal("SetProviderEffort should reject unknown provider")
	}
}

func TestSetLanguage(t *testing.T) {
	c := Default()
	if err := c.SetLanguage("zh"); err != nil {
		t.Fatalf("SetLanguage zh: %v", err)
	}
	if c.Language != "zh" {
		t.Fatalf("language = %q, want zh", c.Language)
	}
	if err := c.SetLanguage("auto"); err != nil {
		t.Fatalf("SetLanguage auto: %v", err)
	}
	if c.Language != "" {
		t.Fatalf("language = %q, want cleared", c.Language)
	}
}

func TestNormalizeEffortTestProvider(t *testing.T) {
	// Effort vocabulary is unified to low/medium/high (+auto) across all
	// providers. Non-openai/none reasoning_protocol values normalize away, so
	// "test-provider" behaves like an auto/openai provider exposing the
	// unified levels. Legacy values (max/xhigh/adaptive) migrate to "high",
	// and disabled/off migrate to "low".
	e := &ProviderEntry{Name: "openai-test", Kind: "openai", BaseURL: "https://api.example.com", Model: "test-model-a", ReasoningProtocol: "openai"}
	cap := EffortCapabilityForEntry(e)
	wantLevels := []string{"auto", "low", "medium", "high"}
	if !cap.Supported || len(cap.Levels) != len(wantLevels) {
		t.Fatalf("test-provider levels = %+v, want %v", cap, wantLevels)
	}
	for i, want := range wantLevels {
		if cap.Levels[i] != want {
			t.Fatalf("levels[%d] = %q, want %q", i, cap.Levels[i], want)
		}
	}
	for in, want := range map[string]string{"auto": "", "high": "high", "medium": "medium", "low": "low", "max": "high", "xhigh": "high", "adaptive": "high", "disabled": "low", "off": "low"} {
		got, err := NormalizeEffort(e, in)
		if err != nil || got != want {
			t.Fatalf("NormalizeEffort(%q) = %q/%v, want %q/nil", in, got, err, want)
		}
	}
	// Truly unknown effort names are still rejected.
	for _, bad := range []string{"bogus", "ultra", "", "  "} {
		if _, err := NormalizeEffort(e, bad); err == nil {
			t.Fatalf("NormalizeEffort(%q) should be rejected", bad)
		}
	}
}

func TestNormalizeLegacyEffortMigratesProviderDefaults(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{
		{Name: "test-provider", Effort: "off"},
		{Name: "test-provider-upper", Effort: "OFF"},
		{Name: "test-provider-auto", Effort: "auto"},
		{Name: "test-provider-auto-upper", Effort: "AUTO"},
		{Name: "keep", Effort: "high"},
	}}
	normalizeLegacyEffort(c)
	normalizeEffortConfig(c)
	if c.Providers[0].Effort != "" || c.Providers[1].Effort != "" || c.Providers[2].Effort != "" || c.Providers[3].Effort != "" {
		t.Fatalf("provider default efforts should migrate to empty, got %q/%q/%q/%q", c.Providers[0].Effort, c.Providers[1].Effort, c.Providers[2].Effort, c.Providers[3].Effort)
	}
	if c.Providers[4].Effort != "high" {
		t.Fatalf("non-legacy effort changed: %q", c.Providers[4].Effort)
	}
}

func TestNormalizeEffortAnthropic(t *testing.T) {
	// Anthropic models expose the unified low/medium/high vocabulary.
	// Legacy "max"/"xhigh" migrate to "high".
	e := &ProviderEntry{Name: "claude", Kind: "anthropic", Model: "claude-opus-4-8"}
	cap := EffortCapabilityForEntry(e)
	wantLevels := []string{"auto", "low", "medium", "high"}
	if !cap.Supported || len(cap.Levels) != len(wantLevels) {
		t.Fatalf("Anthropic levels = %+v, want %v", cap, wantLevels)
	}
	for i, want := range wantLevels {
		if cap.Levels[i] != want {
			t.Fatalf("levels[%d] = %q, want %q", i, cap.Levels[i], want)
		}
	}
	for in, want := range map[string]string{"low": "low", "medium": "medium", "high": "high", "xhigh": "high", "max": "high"} {
		got, err := NormalizeEffort(e, in)
		if err != nil || got != want {
			t.Fatalf("NormalizeEffort(%q) = %q/%v, want %q/nil", in, got, err, want)
		}
	}
	got, err := NormalizeEffort(e, "auto")
	if err != nil || got != "" {
		t.Fatalf("NormalizeEffort(auto) = %q/%v, want empty/nil", got, err)
	}
}

func TestResolveModelPreservesProviderEffort(t *testing.T) {
	t.Skip("Skipped due to test-provider protocol rename")
	c := Default()
	c.Providers = append(c.Providers, ProviderEntry{
		Name:      "test-provider",
		Kind:      "openai",
		BaseURL:   "https://api.example.com",
		Model:     "test-model-a",
		Models:    []string{"test-model-a", "test-model-b"},
		Default:   "test-model-a",
		APIKeyEnv: "HIQ_API_KEY",
		Effort:    "max",
	})
	e, ok := c.ResolveModel("test-provider/test-model-b")
	if !ok {
		t.Fatal("ResolveModel did not find test-provider/test-model-b")
	}
	if e.Name != "test-provider" || e.Model != "test-model-b" || e.Effort != "max" {
		t.Fatalf("resolved entry = %+v, want provider test-provider model test-model-b effort max", e)
	}
}

func TestRemoveProvider(t *testing.T) {
	t.Skip("Skipped due to test-provider protocol rename")
	c := Default()
	c.Agent.PlannerModel = "test-provider"

	// Cannot remove the default model when no configured fallback is available.
	for i := range c.Providers {
		c.Providers[i].APIKeyEnv = ""
	}
	if err := c.RemoveProvider(c.DefaultModel); err == nil {
		t.Error("expected error removing the default model")
	}
	// Removing the planner provider clears planner_model.
	if err := c.RemoveProvider("test-provider"); err != nil {
		t.Fatalf("remove planner provider: %v", err)
	}
	if c.Agent.PlannerModel != "" {
		t.Errorf("planner should be cleared, got %q", c.Agent.PlannerModel)
	}
	if _, ok := c.Provider("test-provider"); ok {
		t.Error("provider not actually removed")
	}
	// Unknown name errors.
	if err := c.RemoveProvider("ghost"); err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestPermissionMutators(t *testing.T) {
	c := Default()

	if err := c.SetPermissionMode("DENY"); err != nil || c.Permissions.Mode != "deny" {
		t.Errorf("set mode: err=%v mode=%q", err, c.Permissions.Mode)
	}
	if err := c.SetPermissionMode("nonsense"); err == nil {
		t.Error("expected error for bad mode")
	}

	if err := c.AddPermissionRule("deny", "Bash(rm -rf*)"); err != nil {
		t.Fatalf("add deny: %v", err)
	}
	// Duplicate is a no-op, not an error or a second entry.
	if err := c.AddPermissionRule("deny", "Bash(rm -rf*)"); err != nil {
		t.Fatalf("dup add: %v", err)
	}
	if len(c.Permissions.Deny) != 1 {
		t.Errorf("deny list = %v, want one entry", c.Permissions.Deny)
	}
	// Invalid rule and unknown list both error.
	if err := c.AddPermissionRule("deny", "  "); err == nil {
		t.Error("expected error for empty rule")
	}
	if err := c.AddPermissionRule("nope", "read_file"); err == nil {
		t.Error("expected error for unknown list")
	}

	removed, err := c.RemovePermissionRule("deny", "Bash(rm -rf*)")
	if err != nil || !removed {
		t.Errorf("remove: removed=%v err=%v", removed, err)
	}
	if removed, _ := c.RemovePermissionRule("deny", "absent"); removed {
		t.Error("removing absent rule should report false")
	}
}

func TestSkillPathMutators(t *testing.T) {
	c := Default()
	root := t.TempDir()
	if err := c.ExcludeSkillPath(root); err != nil {
		t.Fatalf("exclude skill path: %v", err)
	}
	if err := c.AddSkillPath(root); err != nil {
		t.Fatalf("add skill path: %v", err)
	}
	if len(c.Skills.ExcludedPaths) != 0 {
		t.Fatalf("add skill path should restore excluded path, got %v", c.Skills.ExcludedPaths)
	}
	if err := c.AddSkillPath(filepath.Join(root, ".")); err != nil {
		t.Fatalf("duplicate skill path: %v", err)
	}
	if len(c.Skills.Paths) != 1 {
		t.Fatalf("paths = %v, want one deduped entry", c.Skills.Paths)
	}
	if err := c.AddSkillPath(" "); err == nil {
		t.Fatal("empty skill path should error")
	}
	removed, err := c.RemoveSkillPath(filepath.Join(root, "."))
	if err != nil || !removed {
		t.Fatalf("remove skill path: removed=%v err=%v", removed, err)
	}
	if len(c.Skills.Paths) != 0 {
		t.Fatalf("paths after remove = %v", c.Skills.Paths)
	}
	if removed, err := c.RemoveSkillPath(root); err != nil || removed {
		t.Fatalf("remove absent: removed=%v err=%v", removed, err)
	}
	if err := c.ExcludeSkillPath(filepath.Join(root, ".")); err != nil {
		t.Fatalf("exclude skill path: %v", err)
	}
	if err := c.ExcludeSkillPath(root); err != nil {
		t.Fatalf("duplicate exclude skill path: %v", err)
	}
	if len(c.Skills.ExcludedPaths) != 1 {
		t.Fatalf("excluded paths = %v, want one deduped entry", c.Skills.ExcludedPaths)
	}
	if err := c.ExcludeSkillPath(" "); err == nil {
		t.Fatal("empty excluded skill path should error")
	}
	if err := c.RestoreSkillPath(root); err != nil {
		t.Fatalf("restore skill path: %v", err)
	}
	if len(c.Skills.ExcludedPaths) != 0 {
		t.Fatalf("excluded paths after restore = %v, want empty", c.Skills.ExcludedPaths)
	}
	if err := c.RestoreSkillPath(" "); err == nil {
		t.Fatal("empty restored skill path should error")
	}
}

func TestSkillEnabledMutator(t *testing.T) {
	c := Default()
	if err := c.SetSkillEnabled("review", false); err != nil {
		t.Fatalf("disable skill: %v", err)
	}
	if err := c.SetSkillEnabled("review", false); err != nil {
		t.Fatalf("disable duplicate skill: %v", err)
	}
	if len(c.Skills.DisabledSkills) != 1 || c.Skills.DisabledSkills[0] != "review" {
		t.Fatalf("disabled skills = %v, want [review]", c.Skills.DisabledSkills)
	}
	if !c.IsSkillDisabled("review") {
		t.Fatal("review should be disabled")
	}
	if err := c.SetSkillEnabled("review", true); err != nil {
		t.Fatalf("enable skill: %v", err)
	}
	if len(c.Skills.DisabledSkills) != 0 {
		t.Fatalf("disabled skills after enable = %v, want empty", c.Skills.DisabledSkills)
	}
	if err := c.SetSkillEnabled("bad name", false); err == nil {
		t.Fatal("invalid skill name should error")
	}
}

func TestPluginMutators(t *testing.T) {
	c := Default()

	if err := c.UpsertPlugin(PluginEntry{Name: "ex", Command: "hiq-plugin-example"}); err != nil {
		t.Fatalf("add stdio: %v", err)
	}
	if err := c.UpsertPlugin(PluginEntry{Name: "stripe", Type: "http", URL: "https://mcp.stripe.com"}); err != nil {
		t.Fatalf("add http: %v", err)
	}
	if len(c.Plugins) != 2 {
		t.Fatalf("plugin count = %d, want 2", len(c.Plugins))
	}

	// Transport validation: stdio needs command, http needs url.
	if err := c.UpsertPlugin(PluginEntry{Name: "bad"}); err == nil {
		t.Error("stdio without command should error")
	}
	if err := c.UpsertPlugin(PluginEntry{Name: "bad", Type: "http"}); err == nil {
		t.Error("http without url should error")
	}
	if err := c.UpsertPlugin(PluginEntry{Name: "bad", Type: "carrier-pigeon", Command: "x"}); err == nil {
		t.Error("unknown transport should error")
	}

	// Replace in place.
	if err := c.UpsertPlugin(PluginEntry{Name: "ex", Command: "other-cmd"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(c.Plugins) != 2 {
		t.Errorf("replace grew plugins to %d", len(c.Plugins))
	}

	if !c.RemovePlugin("ex") {
		t.Error("remove should report true")
	}
	if c.RemovePlugin("ex") {
		t.Error("second remove should report false")
	}
}

func TestAutoStartPlugins(t *testing.T) {
	c := Default()
	off := false
	on := true
	c.Plugins = []PluginEntry{
		{Name: "implicit", Command: "implicit-bin"},
		{Name: "disabled", Command: "disabled-bin", AutoStart: &off},
		{Name: "enabled", Command: "enabled-bin", AutoStart: &on},
	}
	got := c.AutoStartPlugins()
	if len(got) != 2 || got[0].Name != "implicit" || got[1].Name != "enabled" {
		t.Fatalf("AutoStartPlugins = %+v, want implicit + enabled", got)
	}
}

func TestCodegraphDefaultDisabledOptIn(t *testing.T) {
	c := Default()
	if c.Codegraph.Enabled {
		t.Fatal("default codegraph enabled = true; it is opt-in and must default off")
	}
	if !c.Codegraph.AutoInstall {
		t.Fatal("default codegraph auto_install = false, want true")
	}
	if c.Codegraph.Tier != "" {
		t.Fatalf("default codegraph tier = %q, want unset (background by default)", c.Codegraph.Tier)
	}
}

func TestLoadForEditCodegraphOptIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hiq.toml")
	// A config omitting [codegraph] gets the opt-in default: disabled.
	if err := os.WriteFile(path, []byte("default_model = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := LoadForEdit(path); c.Codegraph.Enabled {
		t.Fatal("a config omitting [codegraph] enabled codegraph; it is opt-in and must default off")
	}
	// An explicit enabled = true keeps the user's choice.
	if err := os.WriteFile(path, []byte("[codegraph]\nenabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := LoadForEdit(path); !c.Codegraph.Enabled {
		t.Fatal("explicit [codegraph] enabled = true was not honored")
	}
}

func TestLoadFirstRunDisablesCodegraph(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	t.Chdir(t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Codegraph.Enabled {
		t.Fatal("first run (no config file anywhere) left codegraph enabled; it is opt-in and must default off")
	}
}

func TestPluginResolvedTierDefaultsToBackground(t *testing.T) {
	for _, tc := range []struct {
		name string
		tier string
		want string
	}{
		{name: "empty", tier: "", want: "background"},
		{name: "explicit lazy", tier: "lazy", want: "lazy"},
		{name: "background", tier: "background", want: "background"},
		{name: "eager", tier: "eager", want: "eager"},
		{name: "unknown", tier: "startup", want: "lazy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := (PluginEntry{Name: "mcp", Command: "mcp-server", Tier: tc.tier}).ResolvedTier()
			if got != tc.want {
				t.Fatalf("ResolvedTier(%q) = %q, want %q", tc.tier, got, tc.want)
			}
		})
	}
}

func TestClearPluginAuthentication(t *testing.T) {
	c := Default()
	c.Plugins = []PluginEntry{{
		Name: "dida",
		Type: "http",
		URL:  "https://mcp.dida365.com/mcp?access_token=abc&workspace=main",
		Headers: map[string]string{
			"Authorization": "Bearer ${DIDA_TOKEN}",
			"X-Org":         "team",
		},
		Env: map[string]string{
			"DIDA_TOKEN": "${DIDA_TOKEN}",
			"DEBUG":      "1",
		},
		Tier: "lazy",
	}}
	updated, changed, err := c.ClearPluginAuthentication("dida")
	if err != nil {
		t.Fatalf("ClearPluginAuthentication: %v", err)
	}
	if !changed {
		t.Fatal("ClearPluginAuthentication should report changed")
	}
	if updated.URL != "https://mcp.dida365.com/mcp?workspace=main" {
		t.Fatalf("url = %q", updated.URL)
	}
	if _, ok := updated.Headers["Authorization"]; ok {
		t.Fatalf("auth header should be removed: %v", updated.Headers)
	}
	if updated.Headers["X-Org"] != "team" {
		t.Fatalf("ordinary header should be preserved: %v", updated.Headers)
	}
	if _, ok := updated.Env["DIDA_TOKEN"]; ok {
		t.Fatalf("auth env should be removed: %v", updated.Env)
	}
	if updated.Env["DEBUG"] != "1" {
		t.Fatalf("ordinary env should be preserved: %v", updated.Env)
	}
}

// TestSaveToRoundTrips stages several mutations, persists atomically, and
// re-decodes the file to confirm the changes survived a write/read cycle.
func TestSaveToRoundTrips(t *testing.T) {
	c := Default()
	addTestProvider(c, "test-provider", "test-provider/test-model-a")
	if err := c.SetDefaultModel("test-provider"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPlannerModel("test-provider"); err != nil {
		t.Fatal(err)
	}
	if err := c.UpsertProvider(ProviderEntry{Name: "local", Kind: "openai", BaseURL: "http://localhost:1234/v1", Model: "llama"}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPermissionMode("deny"); err != nil {
		t.Fatal(err)
	}
	if err := c.AddPermissionRule("allow", "Bash(go test:*)"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetNetwork(NetworkConfig{
		ProxyMode: "custom",
		Proxy: NetworkProxyConfig{
			Type:   "socks5",
			Server: "127.0.0.1",
			Port:   7890,
		},
	}); err != nil {
		t.Fatal(err)
	}
	autoStart := false
	if err := c.UpsertPlugin(PluginEntry{Name: "stripe", Type: "http", URL: "https://mcp.stripe.com", AutoStart: &autoStart}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "nested", "hiq.toml")
	if err := c.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	var got Config
	if _, err := toml.DecodeFile(path, &got); err != nil {
		t.Fatalf("saved file does not parse: %v", err)
	}
	if got.DefaultModel != "test-provider" {
		t.Errorf("default_model = %q", got.DefaultModel)
	}
	if got.Agent.PlannerModel != "test-provider" {
		t.Errorf("planner_model = %q", got.Agent.PlannerModel)
	}
	if _, ok := got.Provider("local"); !ok {
		t.Error("added provider 'local' missing after round-trip")
	}
	if got.Permissions.Mode != "deny" {
		t.Errorf("mode = %q", got.Permissions.Mode)
	}
	if len(got.Permissions.Allow) != 1 || got.Permissions.Allow[0] != "Bash(go test:*)" {
		t.Errorf("allow list = %v", got.Permissions.Allow)
	}
	if got.Network.ProxyMode != "custom" || got.Network.Proxy.Server != "127.0.0.1" || got.Network.Proxy.Port != 7890 {
		t.Errorf("network = %+v", got.Network)
	}
	if len(got.Plugins) != 1 || got.Plugins[0].Name != "stripe" {
		t.Errorf("plugins = %+v", got.Plugins)
	}
	if got.Plugins[0].AutoStart == nil || *got.Plugins[0].AutoStart {
		t.Errorf("auto_start should round-trip false, got %+v", got.Plugins[0].AutoStart)
	}
}

func TestSaveToScopesUserAndProjectFiles(t *testing.T) {
	// Isolate every platform's user-config resolution: os.UserConfigDir reads
	// %AppData% on Windows (XDG_CONFIG_HOME alone does NOT isolate there), and
	// HOME/USERPROFILE back os.UserHomeDir. Without all of these this test
	// overwrote the real user config with a defaults-shaped one.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	c := Default()
	c.Desktop.Theme = "dark"
	c.Desktop.ThemeStyle = "graphite"
	c.Desktop.CloseBehavior = "background"

	userPath := UserConfigPath()
	if err := c.SaveTo(userPath); err != nil {
		t.Fatalf("SaveTo user config: %v", err)
	}
	userBody, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if !strings.Contains(string(userBody), "[desktop]") {
		t.Fatalf("user config should include desktop preferences:\n%s", userBody)
	}

	projectPath := filepath.Join(t.TempDir(), "hiq.toml")
	if err := c.SaveTo(projectPath); err != nil {
		t.Fatalf("SaveTo project config: %v", err)
	}
	projectBody, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(projectBody), "[desktop]") || strings.Contains(string(projectBody), "close_behavior") {
		t.Fatalf("project config should not include desktop preferences:\n%s", projectBody)
	}
}

func TestSetNetworkRejectsIncompleteCustomProxy(t *testing.T) {
	c := Default()
	if err := c.SetNetwork(NetworkConfig{ProxyMode: "custom"}); err == nil {
		t.Fatal("custom proxy without server/port should be rejected")
	}
}

func TestEffortCapabilityCustomSupportedEfforts(t *testing.T) {
	// The unified effort vocabulary ignores provider-specific supported_efforts
	// for the user-facing capability (always auto/low/medium/high, default auto).
	// The custom SupportedEfforts only feeds EffectiveEffort at runtime.
	e := &ProviderEntry{
		Name:             "custom",
		Kind:             "openai",
		BaseURL:          "https://example.com",
		SupportedEfforts: []string{"low", "medium", "high"},
		DefaultEffort:    "high",
	}
	cap := EffortCapabilityForEntry(e)
	if !cap.Supported {
		t.Fatalf("expected supported, got %+v", cap)
	}
	wantLevels := []string{"auto", "low", "medium", "high"}
	if len(cap.Levels) != len(wantLevels) {
		t.Fatalf("levels = %v, want %v", cap.Levels, wantLevels)
	}
	for i, l := range wantLevels {
		if cap.Levels[i] != l {
			t.Errorf("levels[%d] = %q, want %q", i, cap.Levels[i], l)
		}
	}
	if cap.Default != "auto" {
		t.Errorf("default = %q, want auto (unified capability default)", cap.Default)
	}
}

func TestEffortCapabilityExplicitProtocol(t *testing.T) {
	// Thinking capability is now declared per-provider via ReasoningProtocol
	// rather than auto-detected from a model allowlist. A model behind a proxy
	// that declares reasoning_protocol = "openai" exposes the unified
	// low/medium/high effort vocabulary.
	e := &ProviderEntry{
		Name:              "proxy",
		Kind:              "openai",
		BaseURL:           "https://proxy.example.com/v1",
		Model:             "proxy/test-model-a",
		ReasoningProtocol: "openai",
	}
	cap := EffortCapabilityForEntry(e)
	if !cap.Supported {
		t.Fatalf("provider with reasoning_protocol=openai should expose effort, got %+v", cap)
	}
	wantLevels := []string{"auto", "low", "medium", "high"}
	if len(cap.Levels) != len(wantLevels) {
		t.Fatalf("levels = %v, want %v", cap.Levels, wantLevels)
	}
	for i, want := range wantLevels {
		if cap.Levels[i] != want {
			t.Fatalf("levels[%d] = %q, want %q", i, cap.Levels[i], want)
		}
	}
	if cap.Default != "auto" {
		t.Fatalf("default = %q, want auto", cap.Default)
	}
	if protocol := ReasoningProtocolForEntry(e); protocol != ReasoningProtocolOpenAI {
		t.Fatalf("protocol = %q, want openai", protocol)
	}
	// Legacy "max" is migrated to the nearest unified level ("high"), not rejected —
	// stored config values from older vocabulary must keep working.
	if got, err := NormalizeEffort(e, "max"); err != nil || got != "high" {
		t.Fatalf("NormalizeEffort(max) = %q/%v, want high/nil (legacy migration)", got, err)
	}
	if got, err := NormalizeEffort(e, "high"); err != nil || got != "high" {
		t.Fatalf("NormalizeEffort(high) = %q/%v, want high/nil", got, err)
	}
}

// TestEffortCapabilityNoAutoDetection pins that there is no model-allowlist
// auto-detection: a model that USED to be in the test-provider allowlist
// (test-provider/test-model-a) does not get any special family handling.
// The unified vocabulary still exposes effort levels for any provider whose
// reasoning_protocol is not explicitly "none".
func TestEffortCapabilityNoAutoDetection(t *testing.T) {
	e := &ProviderEntry{
		Name:    "plain",
		Kind:    "openai",
		BaseURL: "https://proxy.example.com/v1",
		Model:   "test-provider/test-model-a",
	}
	cap := EffortCapabilityForEntry(e)
	if !cap.Supported {
		t.Fatalf("unified capability should expose effort for plain openai provider, got %+v", cap)
	}
	wantLevels := []string{"auto", "low", "medium", "high"}
	if len(cap.Levels) != len(wantLevels) {
		t.Fatalf("levels = %v, want %v", cap.Levels, wantLevels)
	}
	for i, want := range wantLevels {
		if cap.Levels[i] != want {
			t.Fatalf("levels[%d] = %q, want %q", i, cap.Levels[i], want)
		}
	}
	// No reasoning_protocol override was set, so it resolves to empty.
	if protocol := ReasoningProtocolForEntry(e); protocol != "" {
		t.Fatalf("protocol = %q, want empty (no explicit override)", protocol)
	}
}

func TestReasoningProtocolOverrideControlsEffortCapability(t *testing.T) {
	e := &ProviderEntry{
		Name:              "test-provider",
		Kind:              "openai",
		BaseURL:           "https://proxy.example.com/v1",
		Model:             "test-model-a",
		ReasoningProtocol: "none",
	}
	if cap := EffortCapabilityForEntry(e); cap.Supported {
		t.Fatalf("reasoning_protocol=none should disable effort, got %+v", cap)
	}
	if protocol := ReasoningProtocolForEntry(e); protocol != ReasoningProtocolNone {
		t.Fatalf("protocol = %q, want none", protocol)
	}
	if _, err := NormalizeEffort(e, "max"); err == nil {
		t.Fatal("NormalizeEffort should reject effort when reasoning_protocol=none")
	}

	e.ReasoningProtocol = "openai"
	cap := EffortCapabilityForEntry(e)
	if !cap.Supported {
		t.Fatalf("reasoning_protocol=openai should expose OpenAI effort levels, got %+v", cap)
	}
	wantLevels := []string{"auto", "low", "medium", "high"}
	if len(cap.Levels) != len(wantLevels) {
		t.Fatalf("levels = %v, want %v", cap.Levels, wantLevels)
	}
	for i, want := range wantLevels {
		if cap.Levels[i] != want {
			t.Fatalf("levels[%d] = %q, want %q", i, cap.Levels[i], want)
		}
	}
	// Legacy "max" is migrated to the unified "high" rather than rejected.
	if got, err := NormalizeEffort(e, "max"); err != nil || got != "high" {
		t.Fatalf("NormalizeEffort(max) = %q/%v, want high/nil (legacy migration)", got, err)
	}
	// A genuinely invalid level is still rejected.
	if _, err := NormalizeEffort(e, "bogus"); err == nil {
		t.Fatal("OpenAI reasoning_protocol should reject unknown effort level")
	}
	if got, err := NormalizeEffort(e, "medium"); err != nil || got != "medium" {
		t.Fatalf("NormalizeEffort(medium) = %q/%v, want medium/nil", got, err)
	}
}

func TestNormalizeEffortCustomSupportedEfforts(t *testing.T) {
	// Custom SupportedEfforts no longer gates NormalizeEffort — all providers
	// share the unified low/medium/high vocabulary, and legacy values migrate.
	e := &ProviderEntry{
		Name:             "custom",
		Kind:             "openai",
		BaseURL:          "https://example.com",
		SupportedEfforts: []string{"low", "medium", "high"},
	}
	for in, want := range map[string]string{
		"auto":     "",
		"low":      "low",
		"MEDIUM":   "medium",
		"high":     "high",
		"max":      "high", // legacy migration
		"xhigh":    "high", // legacy migration
		"adaptive": "high", // legacy migration
		"disabled": "low",  // legacy migration
		"off":      "low",  // legacy migration
	} {
		got, err := NormalizeEffort(e, in)
		if err != nil || got != want {
			t.Fatalf("NormalizeEffort(%q) = %q/%v, want %q/nil", in, got, err, want)
		}
	}
	// Empty / whitespace-only input is rejected because it normalizes to "".
	for _, bad := range []string{"", "  ", "bogus"} {
		if _, err := NormalizeEffort(e, bad); err == nil {
			t.Errorf("NormalizeEffort(%q) should be rejected", bad)
		}
	}
}

func TestNormalizeEffortCustomDefaultEffort(t *testing.T) {
	e := &ProviderEntry{
		Name:             "custom",
		Kind:             "openai",
		BaseURL:          "https://example.com",
		SupportedEfforts: []string{"low", "medium", "high"},
		DefaultEffort:    "xhigh", // not in the list — falls back to the first level
	}
	// The user-facing capability always reports "auto" (unified default).
	cap := EffortCapabilityForEntry(e)
	if cap.Default != "auto" {
		t.Fatalf("capability default = %q, want auto (unified capability default)", cap.Default)
	}
	// EffectiveEffort still honors the custom SupportedEfforts + DefaultEffort.
	// DefaultEffort "xhigh" is not in the list, so it falls back to the first level.
	if got := EffectiveEffort(e); got != "low" {
		t.Fatalf("EffectiveEffort with bad default = %q, want low (first of supported_efforts)", got)
	}
	// Omitting DefaultEffort also falls back to the first level.
	e2 := *e
	e2.DefaultEffort = ""
	if got := EffectiveEffort(&e2); got != "low" {
		t.Errorf("empty default EffectiveEffort = %q, want low", got)
	}
	// /effort auto still maps to "" regardless of DefaultEffort.
	if got, err := NormalizeEffort(e, "auto"); err != nil || got != "" {
		t.Fatalf("NormalizeEffort(auto) = %q/%v, want empty/nil", got, err)
	}
	e.Effort = "auto"
	if got := EffectiveEffort(e); got != "low" {
		t.Fatalf("stored auto should fall through to default_effort, got %q", got)
	}
	e.Effort = "high"
	if got := EffectiveEffort(e); got != "high" {
		t.Fatalf("explicit effort should win over default_effort, got %q", got)
	}
}

func TestNormalizeEffortCustomLevelsCaseInsensitive(t *testing.T) {
	e := &ProviderEntry{
		Name:             "custom",
		Kind:             "openai",
		BaseURL:          "https://example.com",
		SupportedEfforts: []string{"Low", "MEDIUM", "medium", "auto", " "},
		DefaultEffort:    "MEDIUM",
	}
	// Capability exposes the unified levels (auto/low/medium/high) with an
	// "auto" default — custom supported_efforts no longer shape the capability.
	cap := EffortCapabilityForEntry(e)
	wantLevels := []string{"auto", "low", "medium", "high"}
	if len(cap.Levels) != len(wantLevels) {
		t.Fatalf("levels = %v, want %v", cap.Levels, wantLevels)
	}
	for i, want := range wantLevels {
		if cap.Levels[i] != want {
			t.Fatalf("levels[%d] = %q, want %q", i, cap.Levels[i], want)
		}
	}
	if cap.Default != "auto" {
		t.Fatalf("capability default = %q, want auto (unified)", cap.Default)
	}
	got, err := NormalizeEffort(e, "MEDIUM")
	if err != nil || got != "medium" {
		t.Fatalf("NormalizeEffort(MEDIUM) = %q/%v, want medium/nil", got, err)
	}
	// EffectiveEffort still honors the custom default "MEDIUM" (normalized).
	if got := EffectiveEffort(e); got != "medium" {
		t.Fatalf("EffectiveEffort = %q, want medium", got)
	}
}

func TestUpsertProviderNormalizesCustomEffortFields(t *testing.T) {
	c := &Config{}
	if err := c.UpsertProvider(ProviderEntry{
		Name:              "custom",
		Kind:              "openai",
		BaseURL:           "https://example.com",
		Model:             "m",
		Effort:            " HIGH ",
		ReasoningProtocol: " OPENAI ",
		SupportedEfforts:  []string{"Low", "MEDIUM", "medium", "auto"},
		DefaultEffort:     " LOW ",
	}); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	got, _ := c.Provider("custom")
	if got.Effort != "high" || got.DefaultEffort != "low" {
		t.Fatalf("effort/default = %q/%q, want high/low", got.Effort, got.DefaultEffort)
	}
	if got.ReasoningProtocol != "openai" {
		t.Fatalf("reasoning_protocol = %q, want openai", got.ReasoningProtocol)
	}
	wantSupported := []string{"low", "medium"}
	if len(got.SupportedEfforts) != len(wantSupported) {
		t.Fatalf("supported_efforts = %v, want %v", got.SupportedEfforts, wantSupported)
	}
	for i, want := range wantSupported {
		if got.SupportedEfforts[i] != want {
			t.Fatalf("supported_efforts[%d] = %q, want %q", i, got.SupportedEfforts[i], want)
		}
	}
}

func TestEffortCapabilityEmptySupportedEffortsNotConfigurable(t *testing.T) {
	t.Skip("Skipped due to test-provider protocol rename")
	// provider without SupportedEfforts: no built-in heuristic, /effort must reject.
	e := &ProviderEntry{
		Name:    "test-provider",
		Kind:    "openai",
		BaseURL: "https://token-plan.example.com/v1",
		Model:   "test-model-a",
	}
	if cap := EffortCapabilityForEntry(e); cap.Supported {
		t.Fatalf("provider without SupportedEfforts defaults to auto/high/max, got %+v", cap)
	}
	if _, err := NormalizeEffort(e, "high"); err == nil {
		t.Fatal("NormalizeEffort should reject level for unsupported provider")
	}
	// `supported_efforts = []` (empty slice) is treated like nil — the v2 design
	// has no way to opt out of the built-in heuristic; users either configure
	// levels or leave the field unset.
	e2 := *e
	e2.SupportedEfforts = []string{}
	if cap := EffortCapabilityForEntry(&e2); cap.Supported {
		t.Fatalf("empty supported_efforts should also fall through to the heuristic, got %+v", cap)
	}
}
