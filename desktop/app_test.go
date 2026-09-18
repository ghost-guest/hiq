package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/control"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/plugin"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/stats"
)

// setTestCtrl creates a minimal workspace tab (if needed) and sets its
// controller, so tests don't depend on the old App.ctrl field.
func (a *App) setTestCtrl(ctrl *control.Controller, model string) {
	if len(a.tabs) == 0 {
		tab := &WorkspaceTab{
			ID:          "test",
			Scope:       "global",
			Ready:       true,
			disabledMCP: map[string]ServerView{},
		}
		a.tabs = map[string]*WorkspaceTab{"test": tab}
		a.activeTabID = "test"
	}
	tab := a.tabs["test"]
	tab.Ctrl = ctrl
	a.bindControllerDisplayRecorder(ctrl)
	tab.model = model
}

// seedTestProviderConfig writes the standard test-provider user config. The
// provider-agnostic architecture ships NO providers in Default() and a fresh
// install intentionally has no selectable model (onboarding guides setup), so
// any test that needs a bootable model / effort surface seeds one explicitly
// — the "test-provider-only" convention the skipped legacy tests reference.
// Effort follows the unified vocabulary (auto/low/medium/high); legacy values
// like "max" migrate to "high" on write (config.NormalizeEffort).
func seedTestProviderConfig(t *testing.T) {
	t.Helper()
	isolateDesktopUserDirs(t)
	t.Setenv("HIQ_API_KEY", "sk-test")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "test-provider/test-model-a"

[[providers]]
name = "test-provider"
kind = "openai"
base_url = "https://api.example.com"
models = ["test-model-a", "test-model-b"]
default = "test-model-a"
api_key_env = "HIQ_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func isolateDesktopUserDirs(t *testing.T) string {
	t.Helper()
	// Sessions built by the desktop now record usage (boot.Options.StatsSource
	// "desktop"), which opens a process-wide SQLite projection under the state
	// home in use at the time. Fence it before redirecting the home and again
	// before TempDir cleanup, or Windows cannot delete the still-open database.
	closeUsageCatalogsForTest(t)
	home := robustTempDir(t)
	xdg := filepath.Join(home, ".config")
	appData := filepath.Join(home, "AppData")
	for _, dir := range []string{xdg, appData} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("AppData", appData)
	t.Cleanup(func() { closeUsageCatalogsForTest(t) })
	return home
}

// closeUsageCatalogsForTest closes the shared usage projection, waiting for the
// flush so the handle is really gone before the caller deletes its temp dir.
func closeUsageCatalogsForTest(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := stats.CloseUsageCatalogs(ctx); err != nil {
		t.Fatalf("close usage catalogs: %v", err)
	}
}

func providerNamesFromView(providers []ProviderView) []string {
	out := make([]string, 0, len(providers))
	for _, p := range providers {
		out = append(out, p.Name)
	}
	return out
}

func modelRefsFromView(models []ModelInfo) map[string]bool {
	out := map[string]bool{}
	for _, m := range models {
		out[m.Ref] = true
	}
	return out
}

func TestCommandsIncludesEffortNotThinking(t *testing.T) {
	app := NewApp()
	cmds := app.Commands()
	if !hasCommand(cmds, "effort") {
		t.Fatalf("Commands() should include effort: %+v", cmds)
	}
	if hasCommand(cmds, "thinking") {
		t.Fatalf("Commands() should not include thinking: %+v", cmds)
	}
}

func TestEffortDefaultsBeforeStartup(t *testing.T) {
	// Fresh install: no providers, no default model — onboarding owns setup and
	// Effort() reports the neutral not-supported shape (Current stays "auto").
	isolateDesktopUserDirs(t)
	if got := NewApp().Effort(); got.Supported || got.Current != "auto" {
		t.Fatalf("fresh-install Effort() = %+v, want unsupported with current auto", got)
	}

	// With a seeded provider (the test-provider-only convention), the pre-startup
	// Effort() surface is the unified capability: auto/low/medium/high, default auto.
	seedTestProviderConfig(t)
	got := NewApp().Effort()
	if !got.Supported || got.Current != "auto" || got.Default != "auto" {
		t.Fatalf("pre-startup Effort() = %+v, want supported auto with unified default auto", got)
	}
	if !hasLevel(got.Levels, "auto") || !hasLevel(got.Levels, "low") || !hasLevel(got.Levels, "medium") || !hasLevel(got.Levels, "high") {
		t.Fatalf("pre-startup Effort() levels = %v, want the unified auto/low/medium/high set", got.Levels)
	}
}

func TestBeforeCloseAllowsSystemQuitWhenBackgroundCloseEnabled(t *testing.T) {
	isolateDesktopUserDirs(t)
	consumeSystemQuitRequested()
	t.Cleanup(func() { consumeSystemQuitRequested() })

	userCfg := config.LoadForEdit(config.UserConfigPath())
	if err := userCfg.SetDesktopCloseBehavior("background"); err != nil {
		t.Fatal(err)
	}
	if err := userCfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}

	markSystemQuitRequested()
	if prevent := NewApp().beforeClose(context.Background()); prevent {
		t.Fatal("system quit should bypass background close-to-tray behavior")
	}
	if consumeSystemQuitRequested() {
		t.Fatal("system quit marker should be consumed by beforeClose")
	}
}

func TestBackgroundCloseHideStrategyByPlatform(t *testing.T) {
	tests := []struct {
		goos string
		want bool
	}{
		{goos: "darwin", want: true},
		{goos: "windows", want: false},
		{goos: "linux", want: false},
		{goos: "freebsd", want: false},
	}
	for _, tt := range tests {
		if got := backgroundCloseUsesApplicationHide(tt.goos); got != tt.want {
			t.Fatalf("backgroundCloseUsesApplicationHide(%q) = %v, want %v", tt.goos, got, tt.want)
		}
	}
}

func TestBackgroundRestoreMaximiseStrategy(t *testing.T) {
	tests := []struct {
		goos      string
		maximised bool
		want      bool
	}{
		{goos: "windows", maximised: true, want: true},
		{goos: "linux", maximised: true, want: true},
		{goos: "darwin", maximised: true, want: false},
		{goos: "windows", maximised: false, want: false},
	}
	for _, tt := range tests {
		if got := backgroundRestoreShouldMaximise(tt.goos, tt.maximised); got != tt.want {
			t.Fatalf("backgroundRestoreShouldMaximise(%q, %v) = %v, want %v", tt.goos, tt.maximised, got, tt.want)
		}
	}
}

func TestBackgroundRestorePlanAvoidsNormalWindowFlash(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		maximised bool
		want      backgroundRestorePlan
	}{
		{
			name:      "maximised Windows window",
			goos:      "windows",
			maximised: true,
			want:      backgroundRestorePlan{maximiseBeforeShow: true},
		},
		{
			name:      "normal Windows window",
			goos:      "windows",
			maximised: false,
			want:      backgroundRestorePlan{unminimiseAfterShow: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := backgroundRestorePlanFor(tt.goos, tt.maximised)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("backgroundRestorePlanFor(%q, %v) = %v, want %v", tt.goos, tt.maximised, got, tt.want)
			}
		})
	}
}

func TestEmitReadyInvokesReadyHook(t *testing.T) {
	app := NewApp()
	var calls int32
	app.readyHook = func() {
		atomic.AddInt32(&calls, 1)
	}

	app.emitReady(context.TODO(), "tab1")

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("ready hook calls = %d, want 1", got)
	}
}

func TestSetEffortPersistsAndAutoClears(t *testing.T) {
	seedTestProviderConfig(t)

	app := NewApp()
	// "max" is a legacy level: NormalizeEffort migrates it to the unified "high".
	if err := app.SetEffort("max"); err != nil {
		t.Fatalf("SetEffort(max): %v", err)
	}
	if got := app.Effort().Current; got != "high" {
		t.Fatalf("Effort current = %q, want high (max migrates to high)", got)
	}
	if err := app.SetEffort("auto"); err != nil {
		t.Fatalf("SetEffort(auto): %v", err)
	}
	if got := app.Effort().Current; got != "auto" {
		t.Fatalf("Effort current = %q, want auto", got)
	}
	body, err := os.ReadFile(config.UserConfigPath())
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(body), `effort      = "high"`) || strings.Contains(string(body), `effort      = "max"`) {
		t.Fatalf("auto should clear explicit effort:\n%s", body)
	}
}

func TestSettingsUsesUserDesktopPreferencesNotProjectConfig(t *testing.T) {
	isolateDesktopUserDirs(t)

	project := robustTempDir(t)
	if err := os.WriteFile(filepath.Join(project, "hiq.toml"), []byte(`
[desktop]
language = "zh"
theme = "light"
theme_style = "glacier"
close_behavior = "quit"
`), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	userCfg := config.LoadForEdit(config.UserConfigPath())
	if err := userCfg.SetDesktopLanguage("en"); err != nil {
		t.Fatalf("set desktop language: %v", err)
	}
	if err := userCfg.SetDesktopAppearance("dark", "graphite"); err != nil {
		t.Fatalf("set desktop appearance: %v", err)
	}
	if err := userCfg.SetDesktopCloseBehavior("background"); err != nil {
		t.Fatalf("set desktop close behavior: %v", err)
	}
	if err := userCfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save user config: %v", err)
	}

	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(project); err != nil {
		t.Fatalf("chdir project: %v", err)
	}

	got := NewApp().Settings()
	if got.DesktopLanguage != "en" || got.DesktopTheme != "dark" || got.DesktopThemeStyle != "graphite" || got.CloseBehavior != "background" {
		t.Fatalf("desktop settings = lang:%q theme:%q style:%q close:%q, want user-level desktop prefs", got.DesktopLanguage, got.DesktopTheme, got.DesktopThemeStyle, got.CloseBehavior)
	}
}

func TestSettingsSeedsMissingUserConfigFromLegacyProjectConfig(t *testing.T) {
	isolateDesktopUserDirs(t)

	project := robustTempDir(t)
	if err := os.WriteFile(filepath.Join(project, "hiq.toml"), []byte(`
default_model = "legacy-provider/legacy-model"

[desktop]
language = "zh"
theme = "light"
theme_style = "glacier"
close_behavior = "quit"
`), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(project); err != nil {
		t.Fatalf("chdir project: %v", err)
	}

	app := NewApp()
	got := app.Settings()
	if got.ConfigPath != config.UserConfigPath() {
		t.Fatalf("Settings configPath = %q, want user config %q", got.ConfigPath, config.UserConfigPath())
	}
	if got.DefaultModel != "legacy-provider/legacy-model" || got.DesktopLanguage != "zh" || got.DesktopTheme != "light" || got.DesktopThemeStyle != "glacier" || got.CloseBehavior != "quit" {
		t.Fatalf("Settings did not seed from legacy project config: %+v", got)
	}
	if _, err := os.Stat(config.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("Settings() should not write user config before an edit, stat err = %v", err)
	}
	if err := app.SetDesktopLanguage("en"); err != nil {
		t.Fatalf("SetDesktopLanguage: %v", err)
	}
	userCfg := config.LoadForEdit(config.UserConfigPath())
	if userCfg.DesktopLanguage() != "en" || userCfg.DesktopTheme() != "light" || userCfg.DesktopThemeStyle() != "glacier" || userCfg.DesktopCloseBehavior() != "quit" {
		t.Fatalf("saved user config did not preserve seeded desktop prefs: lang:%q theme:%q style:%q close:%q", userCfg.DesktopLanguage(), userCfg.DesktopTheme(), userCfg.DesktopThemeStyle(), userCfg.DesktopCloseBehavior())
	}
}

func TestSettingsSubagentDefaultsRoundTrip(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("HIQ_API_KEY", "sk-test")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "test-provider/test-model-a"

[[providers]]
name = "test-provider"
kind = "openai"
base_url = "https://api.example.com"
models = ["test-model-a", "test-model-b"]
default = "test-model-a"
api_key_env = "HIQ_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	app := NewApp()
	if err := app.SetSubagentModel("test-provider/test-model-b"); err != nil {
		t.Fatalf("SetSubagentModel: %v", err)
	}
	// "max" migrates to the unified "high" vocabulary on write.
	if err := app.SetSubagentEffort("max"); err != nil {
		t.Fatalf("SetSubagentEffort: %v", err)
	}

	got := app.Settings()
	if got.SubagentModel != "test-provider/test-model-b" || got.SubagentEffort != "high" {
		t.Fatalf("subagent settings = model:%q effort:%q", got.SubagentModel, got.SubagentEffort)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if cfg.Agent.SubagentModel != "test-provider/test-model-b" || cfg.Agent.SubagentEffort != "high" {
		t.Fatalf("saved config = model:%q effort:%q", cfg.Agent.SubagentModel, cfg.Agent.SubagentEffort)
	}
}

func TestSettingsSurfacesOfficialProviderTemplatesSeparately(t *testing.T) {
	t.Skip("Incompatible with test-provider-only architecture")
}

func TestSettingsRepairsLegacyOfficialProviderWithoutModel(t *testing.T) {
	t.Skip("Incompatible with test-provider-only architecture")
}

func TestSettingsTreatsReservedProviderNameWithExternalEndpointAsCustom(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("HIQ_API_KEY", "sk-test")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "test-provider/test-model-a"

[desktop]
provider_access = ["test-provider"]

[[providers]]
name = "test-provider"
kind = "openai"
base_url = "https://opencode.ai/zen/go/v1"
models = ["test-model-a", "test-model-b", "glm-5"]
default = "test-model-a"
api_key_env = "HIQ_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got := NewApp().Settings()
	var custom *ProviderView
	for i := range got.Providers {
		if got.Providers[i].Name == "test-provider" {
			custom = &got.Providers[i]
			break
		}
	}
	if custom == nil {
		t.Fatalf("settings providers missing test-provider: %+v", got.Providers)
	}
	if custom.BuiltIn {
		t.Fatalf("external test-provider endpoint should be custom, got built-in provider: %+v", *custom)
	}
	if !custom.Added || !custom.KeySet || custom.BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("external test-provider provider = %+v, want added key-set custom opencode endpoint", *custom)
	}
	for _, p := range got.OfficialProviders {
		if p.Name == "test-provider" && p.Added {
			t.Fatalf("official test-provider template should not be marked added by external endpoint: %+v", p)
		}
	}
}

func TestSettingsInfersLegacyProviderAccessWhenMissing(t *testing.T) {
	t.Skip("Incompatible with test-provider-only architecture")
}

func TestSettingsDoesNotInferProviderAccessWhenExplicitlyEmpty(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("HIQ_API_KEY", "sk-test")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "test-provider/test-model-a"

[desktop]
provider_access = []

[[providers]]
name = "test-provider"
kind = "openai"
base_url = "https://api.example.com"
models = ["test-model-a"]
default = "test-model-a"
api_key_env = "HIQ_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got := NewApp().Settings()
	for _, p := range got.Providers {
		if p.Added {
			t.Fatalf("provider %+v should not be inferred as added when provider_access is explicit empty", p)
		}
	}
}

func TestSettingsInfersConfiguredBuiltInsWithoutConfigFile(t *testing.T) {
	t.Skip("Incompatible with test-provider-only architecture")
}

func TestSettingsDoesNotInferBuiltInsWithoutKeys(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("HIQ_API_KEY", "")
	t.Setenv("HIQ_API_KEY", "")

	got := NewApp().Settings()
	for _, p := range got.Providers {
		if p.Added {
			t.Fatalf("provider %+v should not be inferred as added without a configured key", p)
		}
	}
}

func TestAddOfficialProviderAccessReplacesLegacyProviderWithoutModel(t *testing.T) {
	t.Skip("Incompatible with test-provider-only architecture")
}

func TestRemoveBuiltInProviderAccessRetargetsDefaultToRemainingAccess(t *testing.T) {
	t.Skip("Incompatible with test-provider-only architecture")
}

func TestModelsForTabOnlyListsProviderAccessWhenConfigured(t *testing.T) {
	t.Skip("Incompatible with test-provider-only architecture")
}

func TestModelsForTabListsTestProviderAPIPaidAccess(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("HIQ_API_KEY", "sk-test")

	// Default() ships no providers — seed the test-provider entry explicitly
	// (test-provider-only convention) so Models() has something to list.
	cfg := config.Default()
	cfg.DefaultModel = "test-provider/test-model-a"
	cfg.Desktop.ProviderAccess = []string{"test-provider"}
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name:      "test-provider",
		Kind:      "openai",
		BaseURL:   "https://api.example.com",
		Models:    []string{"test-model-a", "test-model-b"},
		Default:   "test-model-a",
		APIKeyEnv: "HIQ_API_KEY",
	})
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	models := NewApp().Models()
	refs := modelRefsFromView(models)
	if !refs["test-provider/test-model-a"] {
		t.Fatalf("Models() refs = %+v, missing test-provider/test-model-a", models)
	}
	if len(models) == 0 {
		t.Fatalf("Models() len = 0, want > 0")
	}
}

func TestSetModelForTabRejectsProviderOutsideAccess(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("HIQ_API_KEY", "sk-test")
	t.Setenv("HIQ_API_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "test-provider/test-model-a"
	cfg.Desktop.ProviderAccess = []string{"test-provider"}
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name:    "other",
		Kind:    "openai",
		BaseURL: "https://example.com/v1",
		Models:  []string{"model-x"},
	})
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{ID: "tab_a", Scope: "global", Ready: true, model: "test-provider/test-model-a"}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	err := app.SetModelForTab(tab.ID, "other/model-x")
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("SetModelForTab hidden provider error = %v, want not available", err)
	}
}

func TestSetDefaultModelRejectsProviderWithoutKey(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("HIQ_API_KEY", "")

	cfg := config.Default()
	cfg.Desktop.ProviderAccess = []string{"test-provider"}
	// Seed the provider entry itself (keyless) — the guard under test fires on
	// a KNOWN provider without a key, not on an unknown-model lookup.
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name:      "test-provider",
		Kind:      "openai",
		BaseURL:   "https://api.example.com",
		Models:    []string{"test-model-a", "test-model-b"},
		Default:   "test-model-a",
		APIKeyEnv: "HIQ_API_KEY",
	})
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	tab := &WorkspaceTab{ID: "tab_a", Scope: "global", Ready: true, model: "test-provider/test-model-a"}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	err := app.SetDefaultModel("test-provider/test-model-b")
	if err == nil || !strings.Contains(err.Error(), "has no key") {
		t.Fatalf("SetDefaultModel no-key error = %v, want has no key", err)
	}
	if tab.model != "test-provider/test-model-a" {
		t.Fatalf("tab model after failed default change = %q, want previous", tab.model)
	}
}

func TestSaveProviderPersistsReasoningProtocol(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if err := app.SaveProvider(ProviderView{
		Name:              "test-provider",
		Kind:              "openai",
		BaseURL:           "https://proxy.example.com/v1",
		Models:            []string{"test-model-a"},
		Default:           "test-model-a",
		APIKeyEnv:         "test-provider_PROXY_KEY",
		ReasoningProtocol: "none",
		SupportedEfforts:  []string{"high", "max"},
		DefaultEffort:     "max",
	}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	cfg := config.LoadForEdit(config.UserConfigPath())
	got, ok := cfg.Provider("test-provider")
	if !ok {
		t.Fatal("saved provider not found")
	}
	if got.ReasoningProtocol != "none" || got.DefaultEffort != "max" {
		t.Fatalf("saved provider = %+v, want reasoning_protocol none and default_effort max", got)
	}

	view := app.Settings()
	for _, p := range view.Providers {
		if p.Name == "test-provider" {
			if p.ReasoningProtocol != "none" {
				t.Fatalf("settings reasoningProtocol = %q, want none", p.ReasoningProtocol)
			}
			return
		}
	}
	t.Fatalf("Settings() missing saved provider: %+v", view.Providers)
}

func TestDeleteProviderMigratesConfigAndOpenTabs(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("HIQ_TEST_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "prov-a/model-a2"
	cfg.Providers = []config.ProviderEntry{
		{Name: "prov-a", Kind: "openai", BaseURL: "https://a.example.com", Model: "model-a1", Models: []string{"model-a1", "model-a2"}, APIKeyEnv: "HIQ_TEST_KEY"},
		{Name: "prov-b", Kind: "openai", BaseURL: "https://b.example.com", Model: "model-b1", APIKeyEnv: "HIQ_TEST_KEY"},
	}
	cfg.Agent.PlannerModel = "prov-a"
	cfg.Desktop.ProviderAccess = []string{"prov-a", "prov-b"}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	ctrl := control.New(control.Options{Label: "old"})
	defer ctrl.Close()
	app := NewApp()
	tab := &WorkspaceTab{ID: "tab_a", Scope: "global", Ctrl: ctrl, Label: "prov-a/model-a1", Ready: true, model: "prov-a/model-a1"}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	if err := app.DeleteProvider("prov-a"); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	if _, ok := got.Provider("prov-a"); ok {
		t.Fatal("prov-a should be removed")
	}
	if got.DefaultModel != "prov-b" || got.Agent.PlannerModel != "prov-b" {
		t.Fatalf("model refs after delete = default:%q planner:%q, want prov-b", got.DefaultModel, got.Agent.PlannerModel)
	}
	if providerAccessSet(got.Desktop.ProviderAccess)["prov-a"] {
		t.Fatalf("provider access still contains prov-a: %+v", got.Desktop.ProviderAccess)
	}
	if tab.model != "prov-b/model-b1" || tab.Label != "prov-b/model-b1" {
		t.Fatalf("tab model after delete = model:%q label:%q, want prov-b/model-b1", tab.model, tab.Label)
	}
	if tab.Ctrl != nil {
		t.Fatal("tab controller should be closed and cleared when retargeted without a running app context")
	}
}

func TestDeleteProviderRejectsRunningAffectedTab(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("HIQ_TEST_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "prov-a/model-a1"
	cfg.Providers = []config.ProviderEntry{
		{Name: "prov-a", Kind: "openai", BaseURL: "https://a.example.com", Model: "model-a1", APIKeyEnv: "HIQ_TEST_KEY"},
		{Name: "prov-b", Kind: "openai", BaseURL: "https://b.example.com", Model: "model-b1", APIKeyEnv: "HIQ_TEST_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Runner: runner}), "prov-a/model-a1")
	ctrl := app.activeCtrl()
	ctrl.Submit("work")
	<-runner.started

	err := app.DeleteProvider("prov-a")
	if err == nil || !strings.Contains(err.Error(), "finish or cancel") {
		t.Fatalf("DeleteProvider while running error = %v, want finish/cancel guard", err)
	}
	if _, ok := config.LoadForEdit(config.UserConfigPath()).Provider("prov-a"); !ok {
		t.Fatal("provider should remain after rejected deletion")
	}

	close(runner.release)
	waitNotRunning(t, ctrl)
	ctrl.Close()
}

func TestMigrateDesktopPreferencesDoesNotOverwriteExistingConfig(t *testing.T) {
	isolateDesktopUserDirs(t)

	userCfg := config.LoadForEdit(config.UserConfigPath())
	if err := userCfg.SetDesktopLanguage("en"); err != nil {
		t.Fatalf("set desktop language: %v", err)
	}
	if err := userCfg.SetDesktopAppearance("dark", "graphite"); err != nil {
		t.Fatalf("set desktop appearance: %v", err)
	}
	if err := userCfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save user config: %v", err)
	}

	if err := NewApp().MigrateDesktopPreferences("zh", "light", "glacier"); err != nil {
		t.Fatalf("migrate desktop preferences: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	if got.DesktopLanguage() != "en" || got.DesktopTheme() != "dark" || got.DesktopThemeStyle() != "graphite" {
		t.Fatalf("desktop prefs after migration = lang:%q theme:%q style:%q, want existing config preserved", got.DesktopLanguage(), got.DesktopTheme(), got.DesktopThemeStyle())
	}
}

func TestSetEffortRebuildsController(t *testing.T) {
	seedTestProviderConfig(t)

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	old := control.New(control.Options{Label: "old-controller"})
	app.setTestCtrl(old, "test-provider/test-model-a")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	// "max" migrates to the unified "high" level; the rebuild is the point.
	if err := app.SetEffort("max"); err != nil {
		t.Fatalf("SetEffort(max): %v", err)
	}
	if c := app.activeCtrl(); c == nil {
		t.Fatal("SetEffort should leave a rebuilt controller")
	}
	if c := app.activeCtrl(); c == old {
		t.Fatal("SetEffort should rebuild the active controller so the provider sees the new effort")
	}
	if got := app.Effort().Current; got != "high" {
		t.Fatalf("Effort current = %q, want high (max migrates to high)", got)
	}
}

func TestSetEffortRejectsRunningTurn(t *testing.T) {
	isolateDesktopUserDirs(t)

	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Runner: runner}), "")
	app.activeCtrl().Submit("work")
	<-runner.started

	err := app.SetEffort("max")
	if err == nil || !strings.Contains(err.Error(), "finish or cancel") {
		t.Fatalf("SetEffort while running error = %v, want finish/cancel guard", err)
	}

	close(runner.release)
	waitNotRunning(t, app.activeCtrl())
}

func TestSearchFileRefsFindsNestedBasename(t *testing.T) {
	orig, _ := os.Getwd()
	defer os.Chdir(orig)

	dir := robustTempDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "frontend", "wailsjs", "runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "frontend", "wailsjs", "runtime", "runtime.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "frontend", "Thumbs.db"), []byte("noise"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "frontend", ".DS_Store"), []byte("noise"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "runtime.js"), []byte("noise"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".codegraph", "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codegraph", "cache", "runtime.js"), []byte("index"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, noise := range []string{".codex", ".npm", ".pnpm-store", "bin", "dist", "stage", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, noise), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, noise, "runtime.js"), []byte("noise"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "desktop", "frontend", "wailsjs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "desktop", "frontend", "wailsjs", "runtime.js"), []byte("generated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "product", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "product", "bin", "runtime.js"), []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	listed := app.ListDir("")
	for _, hidden := range []string{".codex", ".codegraph", ".npm", ".pnpm-store", "bin", "dist", "stage", "tmp"} {
		if hasDirEntry(listed, hidden) {
			t.Fatalf("ListDir should hide local noise %q, got %+v", hidden, listed)
		}
	}
	desktopFrontend := app.ListDir("desktop/frontend")
	if hasDirEntry(desktopFrontend, "wailsjs") {
		t.Fatalf("ListDir should hide generated Wails bindings, got %+v", desktopFrontend)
	}
	frontendEntries := app.ListDir("frontend")
	for _, hidden := range []string{".DS_Store", "Thumbs.db"} {
		if hasDirEntry(frontendEntries, hidden) {
			t.Fatalf("ListDir should hide local noise file %q, got %+v", hidden, frontendEntries)
		}
	}

	got := app.SearchFileRefs("runtime.js")
	if !hasDirEntry(got, "frontend/wailsjs/runtime/runtime.js") {
		t.Fatalf("SearchFileRefs(runtime.js) should find nested workspace file, got %+v", got)
	}
	if !hasDirEntry(got, "product/bin/runtime.js") {
		t.Fatalf("SearchFileRefs should keep non-root bin directories searchable, got %+v", got)
	}
	if hasDirEntry(got, "node_modules/pkg/runtime.js") {
		t.Fatalf("SearchFileRefs should skip node_modules noise, got %+v", got)
	}
	for _, hidden := range []string{
		".codex/runtime.js",
		".codegraph/cache/runtime.js",
		".npm/runtime.js",
		".pnpm-store/runtime.js",
		"bin/runtime.js",
		"desktop/frontend/wailsjs/runtime.js",
		"dist/runtime.js",
		"stage/runtime.js",
		"tmp/runtime.js",
	} {
		if hasDirEntry(got, hidden) {
			t.Fatalf("SearchFileRefs should skip local noise %q, got %+v", hidden, got)
		}
	}
	if noise := app.SearchFileRefs("Thumbs"); hasDirEntry(noise, "frontend/Thumbs.db") {
		t.Fatalf("SearchFileRefs should skip Thumbs.db noise, got %+v", noise)
	}
	if noise := app.SearchFileRefs(".DS"); hasDirEntry(noise, "frontend/.DS_Store") {
		t.Fatalf("SearchFileRefs should skip .DS_Store noise even for dot-prefixed search, got %+v", noise)
	}
}

func TestFileRefsUseActiveTabWorkspaceRoot(t *testing.T) {
	orig, _ := os.Getwd()
	defer os.Chdir(orig)

	launchRoot := robustTempDir(t)
	projectRoot := robustTempDir(t)
	if err := os.WriteFile(filepath.Join(launchRoot, "launch-only.txt"), []byte("wrong"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, "frontend", "wailsjs", "runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	projectFile := filepath.Join(projectRoot, "frontend", "wailsjs", "runtime", "runtime.js")
	if err := os.WriteFile(projectFile, []byte("right workspace"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(launchRoot); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	tab := &WorkspaceTab{ID: "project", Scope: "project", WorkspaceRoot: projectRoot}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.activeTabID = tab.ID

	listed := app.ListDir("")
	if !hasDirEntry(listed, "frontend") {
		t.Fatalf("ListDir should list active project root, got %+v", listed)
	}
	if hasDirEntry(listed, "launch-only.txt") {
		t.Fatalf("ListDir leaked launch cwd entries, got %+v", listed)
	}

	found := app.SearchFileRefs("runtime.js")
	if !hasDirEntry(found, "frontend/wailsjs/runtime/runtime.js") {
		t.Fatalf("SearchFileRefs should search active project root, got %+v", found)
	}
	preview := app.ReadFile("frontend/wailsjs/runtime/runtime.js")
	if preview.Err != "" || preview.Body != "right workspace" {
		t.Fatalf("ReadFile active project preview = %+v, want project file", preview)
	}
}

func TestDeleteSessionRejectsActiveRelativePath(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "active.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"hello"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{SessionDir: dir, SessionPath: path, Label: "test"}), "")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	if err := app.DeleteSession(filepath.Base(path)); err != errActiveSession {
		t.Fatalf("DeleteSession(active basename) error = %v, want errActiveSession", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active session should remain: %v", err)
	}
}

func TestDeleteSessionRejectsInactiveOpenTab(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	activePath := filepath.Join(dir, "active.jsonl")
	inactivePath := filepath.Join(dir, "inactive.jsonl")
	otherPath := filepath.Join(dir, "other.jsonl")
	for _, path := range []string{activePath, inactivePath, otherPath} {
		if err := os.WriteFile(path, []byte(`{"role":"user","content":"hello"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write session %s: %v", path, err)
		}
	}

	activeCtrl := control.New(control.Options{SessionDir: dir, SessionPath: activePath, Label: "active"})
	inactiveCtrl := control.New(control.Options{SessionDir: dir, SessionPath: inactivePath, Label: "inactive"})
	defer activeCtrl.Close()
	defer inactiveCtrl.Close()

	app := &App{
		tabs: map[string]*WorkspaceTab{
			"active":   {ID: "active", Scope: "global", Ctrl: activeCtrl, Ready: true},
			"inactive": {ID: "inactive", Scope: "global", Ctrl: inactiveCtrl, Ready: true},
		},
		tabOrder:    []string{"active", "inactive"},
		activeTabID: "active",
	}

	if err := app.DeleteSession(filepath.Base(inactivePath)); err != errActiveSession {
		t.Fatalf("DeleteSession(inactive open basename) error = %v, want errActiveSession", err)
	}
	if _, err := os.Stat(inactivePath); err != nil {
		t.Fatalf("inactive open session should remain: %v", err)
	}

	sessions := app.ListSessions()
	current := map[string]bool{}
	open := map[string]bool{}
	for _, s := range sessions {
		current[filepath.Base(s.Path)] = s.Current
		open[filepath.Base(s.Path)] = s.Open
	}
	if !current[filepath.Base(activePath)] {
		t.Fatalf("ListSessions should mark active session current, got %#v", current)
	}
	if current[filepath.Base(inactivePath)] {
		t.Fatalf("ListSessions should not mark inactive open session current, got %#v", current)
	}
	if current[filepath.Base(otherPath)] {
		t.Fatalf("ListSessions marked unopened session current, got %#v", current)
	}
	if !open[filepath.Base(activePath)] || !open[filepath.Base(inactivePath)] {
		t.Fatalf("ListSessions should mark active and inactive open sessions open, got %#v", open)
	}
	if open[filepath.Base(otherPath)] {
		t.Fatalf("ListSessions marked unopened session open, got %#v", open)
	}
}

func TestDesktopSessionAPIsUseControllerSessionDir(t *testing.T) {
	isolateDesktopUserDirs(t)

	dirA := filepath.Join(t.TempDir(), "workspace-a-sessions")
	dirB := filepath.Join(t.TempDir(), "workspace-b-sessions")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatalf("mkdir dirA: %v", err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatalf("mkdir dirB: %v", err)
	}
	pathA := filepath.Join(dirA, "a.jsonl")
	pathB := filepath.Join(dirB, "b.jsonl")
	if err := os.WriteFile(pathA, []byte(`{"role":"user","content":"workspace A"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write pathA: %v", err)
	}
	if err := os.WriteFile(pathB, []byte(`{"role":"user","content":"workspace B"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write pathB: %v", err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{SessionDir: dirA, SessionPath: pathA, Label: "test"}), "")
	defer app.activeCtrl().Close()

	sessions := app.ListSessions()
	if len(sessions) != 1 || sessions[0].Path != pathA || sessions[0].Preview != "workspace A" {
		t.Fatalf("ListSessions should read the active controller session dir only, got %+v", sessions)
	}
	if err := app.RenameSession(pathA, "A title"); err != nil {
		t.Fatalf("RenameSession in active session dir: %v", err)
	}
	if titles := loadSessionTitles(dirA); titles["a.jsonl"] != "A title" {
		t.Fatalf("title should be written beside the active session, got %+v", titles)
	}
	if titles := loadSessionTitles(dirB); len(titles) != 0 {
		t.Fatalf("inactive workspace title sidecar should remain untouched, got %+v", titles)
	}
}

func TestResumeSessionRejectsPathOutsideControllerSessionDir(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	activePath := filepath.Join(dirA, "active.jsonl")
	outsidePath := filepath.Join(dirB, "outside.jsonl")
	for _, path := range []string{activePath, outsidePath} {
		if err := os.WriteFile(path, []byte(`{"role":"user","content":"hello"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{SessionDir: dirA, SessionPath: activePath, Label: "test"}), "")
	defer app.activeCtrl().Close()

	if _, err := app.ResumeSession(outsidePath); err == nil {
		t.Fatal("ResumeSession should reject a transcript outside the active session dir")
	}
	if _, err := app.PreviewSession(outsidePath); err == nil {
		t.Fatal("PreviewSession should reject a transcript outside the active session dir")
	}
}

func BenchmarkDesktopListSessionsScoped(b *testing.B) {
	dirA := filepath.Join(b.TempDir(), "workspace-a-sessions")
	dirB := filepath.Join(b.TempDir(), "workspace-b-sessions")
	for _, dir := range []string{dirA, dirB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatalf("mkdir %s: %v", dir, err)
		}
		for i := 0; i < 120; i++ {
			path := filepath.Join(dir, fmt.Sprintf("session-%03d.jsonl", i))
			body := fmt.Sprintf(`{"role":"user","content":"session %03d"}`+"\n", i)
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				b.Fatalf("write session: %v", err)
			}
		}
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{SessionDir: dirA, SessionPath: filepath.Join(dirA, "session-000.jsonl"), Label: "test"}), "")
	defer app.activeCtrl().Close()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sessions := app.ListSessions()
		if len(sessions) != 120 {
			b.Fatalf("ListSessions len = %d, want 120", len(sessions))
		}
	}
}

type appendingDesktopRunner struct {
	session *agent.Session
	started chan string
}

func (r *appendingDesktopRunner) Run(_ context.Context, input any) error {
	strInput, _ := input.(string)
	r.started <- strInput
	r.session.Add(provider.Message{Role: provider.RoleUser, Content: strInput})
	r.session.Add(provider.Message{Role: provider.RoleAssistant, Content: "ok"})
	return nil
}

func TestSubmitToTabHistoryDisplaysRawInputAfterMemoryCompose(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "memory-display.jsonl")
	sess := agent.NewSession("sys")
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	runner := &appendingDesktopRunner{session: sess, started: make(chan string, 1)}
	ctrl := control.New(control.Options{
		Runner:      runner,
		Executor:    exec,
		Sink:        event.Discard,
		SessionDir:  dir,
		SessionPath: path,
		Label:       "test",
	})
	defer ctrl.Close()

	app := NewApp()
	app.setTestCtrl(ctrl, "test-provider/test")
	ctrl.QueueMemory(`Saved memory "hiq-contributions": contribution count updated`)

	const prompt = "不要，删了"
	app.SubmitToTab("test", prompt)
	composed := <-runner.started
	waitNotRunning(t, ctrl)

	if !strings.Contains(composed, "<memory-update>") || !strings.HasSuffix(composed, prompt) {
		t.Fatalf("model input should include memory update followed by prompt, got %q", composed)
	}
	got := app.HistoryForTab("test")
	if len(got) < 2 {
		t.Fatalf("history length = %d, want user + assistant", len(got))
	}
	if got[0].Role != "system" || got[1].Role != "user" {
		t.Fatalf("history roles = %+v, want system then user", got[:min(len(got), 2)])
	}
	if got[1].Content != prompt {
		t.Fatalf("displayed user content = %q, want %q", got[1].Content, prompt)
	}
	if strings.Contains(got[1].Content, "<memory-update>") {
		t.Fatalf("displayed user content leaked memory update: %q", got[1].Content)
	}
}

func TestForkCreatesActiveTabWithoutSwitchingSourceController(t *testing.T) {
	isolateDesktopUserDirs(t)

	workspace := robustTempDir(t)
	if err := os.WriteFile(filepath.Join(workspace, "hiq.toml"), []byte("[codegraph]\nenabled = false\n"), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := agent.NewSessionPath(dir, "test")
	sess := agent.NewSession("sys")
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	runner := &appendingDesktopRunner{session: sess, started: make(chan string, 2)}
	ctrl := control.New(control.Options{
		Runner:        runner,
		Executor:      exec,
		Sink:          event.Discard,
		SessionDir:    dir,
		SessionPath:   path,
		Label:         "test",
		WorkspaceRoot: workspace,
	})
	app := NewApp()
	app.setTestCtrl(ctrl, "test-provider/test")
	app.tabs["test"].Scope = "project"
	app.tabs["test"].WorkspaceRoot = workspace
	app.tabs["test"].TopicID = "topic_source"
	app.tabs["test"].TopicTitle = "Source topic"
	defer ctrl.Close()

	ctrl.Submit("first")
	<-runner.started
	waitNotRunning(t, ctrl)
	ctrl.Submit("second")
	<-runner.started
	waitNotRunning(t, ctrl)
	if got := len(ctrl.History()); got != 5 {
		t.Fatalf("source history len before fork = %d, want 5", got)
	}

	meta, err := app.Fork(1)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if !meta.Active || meta.ID == "" || meta.ID == "test" {
		t.Fatalf("fork meta = %+v, want a new active tab", meta)
	}
	if got := app.activeTabID; got != meta.ID {
		t.Fatalf("active tab = %q, want fork tab %q", got, meta.ID)
	}
	if got := ctrl.SessionPath(); got != path {
		t.Fatalf("source controller session path = %q, want %q", got, path)
	}
	if got := len(ctrl.History()); got != 5 {
		t.Fatalf("source history len after fork = %d, want 5", got)
	}
	if got, want := meta.TopicTitle, "Source topic · 分叉"; got != want {
		t.Fatalf("fork topic title = %q, want %q", got, want)
	}

	var forkPath string
	// Dual-layout scan (2026-08-21): the fork may be a flat sibling or live in
	// its own session folder.
	candidates := sessionTranscriptCandidates(dir)
	for _, candidate := range candidates {
		{
			if candidate == path {
				continue
			}
			m, ok, err := agent.LoadBranchMeta(candidate)
			if err != nil {
				t.Fatalf("load fork meta: %v", err)
			}
			if ok && m.TopicID == meta.TopicID {
				forkPath = candidate
				if m.ParentID != agent.BranchID(path) || m.ForkTurn != 1 || m.ForkMessageIndex != 3 {
					t.Fatalf("fork branch meta = %+v, want parent %q turn 1 index 3", m, agent.BranchID(path))
				}
				if m.Scope != "project" || m.WorkspaceRoot != workspace || m.TopicTitle != "Source topic · 分叉" {
					t.Fatalf("fork topic meta = %+v", m)
				}
			}
		}
	}
	if forkPath == "" {
		t.Fatalf("fork session with topic %q not found in %s", meta.TopicID, dir)
	}
}

func TestCapabilitiesShowsDefaultMCPAsInitializingNotDisabled(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "hiq.toml"), []byte(`
[codegraph]
enabled = false

[[plugins]]
name = "playwright"
command = "npx"
args = ["-y", "@playwright/mcp"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "playwright" {
			if s.Status != "initializing" {
				t.Fatalf("default MCP status = %q, want initializing; server = %+v", s.Status, s)
			}
			return
		}
	}
	t.Fatalf("playwright MCP missing from Capabilities: %+v", view.Servers)
}

func TestCapabilitiesShowsDefaultCodegraphDisabled(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "codegraph" {
			if s.Status != "disabled" {
				t.Fatalf("codegraph status = %q, want disabled; server = %+v", s.Status, s)
			}
			if !s.BuiltIn || !s.Configured {
				t.Fatalf("codegraph builtIn/configured = %v/%v, want true/true; server = %+v", s.BuiltIn, s.Configured, s)
			}
			if s.AutoStart {
				t.Fatalf("codegraph autoStart = true, want false; server = %+v", s)
			}
			if s.Tier != "background" {
				t.Fatalf("codegraph tier = %q, want background; server = %+v", s.Tier, s)
			}
			return
		}
	}
	t.Fatalf("codegraph missing from Capabilities: %+v", view.Servers)
}

func TestCapabilitiesMarksBackgroundRemoteMCPAuthPossible(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "hiq.toml"), []byte(`
[codegraph]
enabled = false

[[plugins]]
name = "dida"
type = "http"
url = "https://mcp.dida365.com"
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "dida" {
			if s.Status != "initializing" || s.AuthStatus != "possible" || s.AuthURL != "https://mcp.dida365.com" {
				t.Fatalf("dida auth diagnosis = %+v", s)
			}
			return
		}
	}
	t.Fatalf("dida MCP missing from Capabilities: %+v", view.Servers)
}

func TestCapabilitiesDoesNotMarkRemoteMCPWithAuthHeaderPossible(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "hiq.toml"), []byte(`
[codegraph]
enabled = false

[[plugins]]
name = "stripe"
type = "http"
url = "https://mcp.stripe.com"
headers = { Authorization = "Bearer ${STRIPE_TOKEN}" }
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "stripe" {
			if s.AuthStatus != "none" {
				t.Fatalf("stripe auth status = %q, want none; server = %+v", s.AuthStatus, s)
			}
			return
		}
	}
	t.Fatalf("stripe MCP missing from Capabilities: %+v", view.Servers)
}

func TestCapabilitiesMarksAuthFailureRequired(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "hiq.toml"), []byte(`
[codegraph]
enabled = false

[[plugins]]
name = "figma"
type = "http"
url = "https://mcp.figma.com/mcp"
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	host := plugin.NewHost()
	host.RecordFailure(plugin.Spec{Name: "figma", Type: "http", URL: "https://mcp.figma.com/mcp"}, errors.New("connect: 401 unauthorized"))
	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: host}), "")
	defer app.activeCtrl().Close()

	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "figma" {
			if s.Status != "failed" || s.AuthStatus != "required" || s.AuthURL != "https://mcp.figma.com/mcp" {
				t.Fatalf("figma auth diagnosis = %+v", s)
			}
			return
		}
	}
	t.Fatalf("figma MCP missing from Capabilities: %+v", view.Servers)
}

func TestClearMCPServerAuthenticationClearsConfigAndFailure(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "hiq.toml"), []byte(`
[codegraph]
enabled = false

[[plugins]]
name = "figma"
type = "http"
url = "https://mcp.figma.com/mcp?access_token=abc&workspace=main"
headers = { Authorization = "Bearer ${FIGMA_TOKEN}", "X-Org" = "team" }
env = { FIGMA_TOKEN = "${FIGMA_TOKEN}", DEBUG = "1" }
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	host := plugin.NewHost()
	host.RecordFailure(plugin.Spec{Name: "figma", Type: "http", URL: "https://mcp.figma.com/mcp"}, errors.New("connect: 401 unauthorized"))
	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: host}), "")
	defer app.activeCtrl().Close()

	if err := app.ClearMCPServerAuthentication("figma"); err != nil {
		t.Fatalf("ClearMCPServerAuthentication: %v", err)
	}
	if failures := host.Failures(); len(failures) != 0 {
		t.Fatalf("failure should be cleared: %+v", failures)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Plugins[0]
	if p.URL != "https://mcp.figma.com/mcp?workspace=main" {
		t.Fatalf("url = %q", p.URL)
	}
	if _, ok := p.Headers["Authorization"]; ok {
		t.Fatalf("auth header should be removed: %v", p.Headers)
	}
	if p.Headers["X-Org"] != "team" {
		t.Fatalf("ordinary header should be preserved: %v", p.Headers)
	}
	if _, ok := p.Env["FIGMA_TOKEN"]; ok {
		t.Fatalf("auth env should be removed: %v", p.Env)
	}
	if p.Env["DEBUG"] != "1" {
		t.Fatalf("ordinary env should be preserved: %v", p.Env)
	}
	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "figma" {
			if s.Status != "initializing" || s.AuthStatus != "possible" {
				t.Fatalf("figma should return to background possible auth: %+v", s)
			}
			return
		}
	}
	t.Fatalf("figma MCP missing from Capabilities: %+v", view.Servers)
}

func TestUpdateMCPServerMigratesLegacyTierToBackground(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "hiq.toml"), []byte(`
[codegraph]
enabled = false

[[plugins]]
name = "playwright"
command = "npx"
args = ["-y", "@playwright/mcp"]
env = { TOKEN = "${PLAYWRIGHT_TOKEN}" }
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	if err := app.UpdateMCPServer("playwright", MCPServerInput{
		Name:      "playwright",
		Transport: "stdio",
		Command:   "node",
		Args:      []string{"server.js"},
	}); err != nil {
		t.Fatalf("UpdateMCPServer: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Plugins[0].Command; got != "node" {
		t.Fatalf("updated command = %q, want node", got)
	}
	if got := cfg.Plugins[0].Env["TOKEN"]; got != "${PLAYWRIGHT_TOKEN}" {
		t.Fatalf("env TOKEN = %q, want preserved env", got)
	}
	userCfg := config.LoadForEdit(config.UserConfigPath())
	userPlugin, ok := findPluginEntry(userCfg.Plugins, "playwright")
	if !ok {
		t.Fatalf("playwright should be migrated to user config: %+v", userCfg.Plugins)
	}
	if userPlugin.Command != "node" || userPlugin.Env["TOKEN"] != "${PLAYWRIGHT_TOKEN}" {
		t.Fatalf("user plugin after migration = %+v", userPlugin)
	}
	if userPlugin.Tier != "" {
		t.Fatalf("user plugin tier = %q, want migrated empty", userPlugin.Tier)
	}
	projectCfg := config.LoadForEdit(filepath.Join(dir, "hiq.toml"))
	if _, ok := findPluginEntry(projectCfg.Plugins, "playwright"); ok {
		t.Fatalf("project plugin should be removed after desktop migration: %+v", projectCfg.Plugins)
	}
	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "playwright" {
			if s.Status != "failed" {
				t.Fatalf("updated MCP status = %q, want failed after immediate reconnect attempt; server = %+v", s.Status, s)
			}
			if s.Command != "node" || len(s.Args) != 1 || s.Args[0] != "server.js" {
				t.Fatalf("server command not refreshed: %+v", s)
			}
			return
		}
	}
	t.Fatalf("playwright MCP missing from Capabilities: %+v", view.Servers)
}

func TestUpdateMCPServerSplitsPastedCommandLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Flaky on Windows due to TempDir cleanup of running process")
	}
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "hiq.toml"), []byte(`
[codegraph]
enabled = false

[[plugins]]
name = "playwright"
command = "npx"
args = ["-y", "@playwright/mcp"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	if err := app.UpdateMCPServer("playwright", MCPServerInput{
		Name:      "playwright",
		Transport: "stdio",
		Command:   "npx -y @modelcontextprotocol/server-filesystem .",
	}); err != nil {
		t.Fatalf("UpdateMCPServer: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Plugins[0]
	if p.Command != "npx" {
		t.Fatalf("command = %q, want npx", p.Command)
	}
	if got := strings.Join(p.Args, "\x00"); got != strings.Join([]string{"-y", "@modelcontextprotocol/server-filesystem", "."}, "\x00") {
		t.Fatalf("args = %v", p.Args)
	}
}

func TestUpdateMCPServerRecordsReconnectFailure(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "hiq.toml"), []byte(`
[codegraph]
enabled = false

[[plugins]]
name = "broken"
command = "npx"
tier = "background"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	if err := app.UpdateMCPServer("broken", MCPServerInput{
		Name:      "broken",
		Transport: "stdio",
		Command:   "hiq-missing-mcp-binary",
	}); err != nil {
		t.Fatalf("UpdateMCPServer should persist config even when reconnect fails: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Plugins[0].Command; got != "hiq-missing-mcp-binary" {
		t.Fatalf("updated command = %q, want missing binary", got)
	}
	if got := cfg.Plugins[0].Tier; got != "" {
		t.Fatalf("updated tier = %q, want migrated empty", got)
	}
	if !mcpFailed(app.activeCtrl(), "broken") {
		t.Fatalf("Host.Failures() = %+v, want broken failure recorded", app.activeCtrl().Host().Failures())
	}
	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "broken" {
			if s.Status != "failed" {
				t.Fatalf("server status = %q, want failed; server = %+v", s.Status, s)
			}
			if s.Command != "hiq-missing-mcp-binary" || s.Tier != "background" {
				t.Fatalf("server config not refreshed after failed reconnect: %+v", s)
			}
			return
		}
	}
	t.Fatalf("broken MCP missing from Capabilities: %+v", view.Servers)
}

func TestSetMCPServerTierRecordsConnectFailure(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "hiq.toml"), []byte(`
[codegraph]
enabled = false

[[plugins]]
name = "broken"
command = "hiq-missing-mcp-binary"
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	if err := app.SetMCPServerTier("broken", "background"); err != nil {
		t.Fatalf("SetMCPServerTier legacy binding: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Plugins[0].Tier; got != "" {
		t.Fatalf("saved tier = %q, want migrated empty", got)
	}
	userCfg := config.LoadForEdit(config.UserConfigPath())
	userPlugin, ok := findPluginEntry(userCfg.Plugins, "broken")
	if !ok {
		t.Fatalf("broken should be migrated to user config: %+v", userCfg.Plugins)
	}
	if userPlugin.Tier != "" {
		t.Fatalf("user plugin tier = %q, want migrated empty", userPlugin.Tier)
	}
	projectCfg := config.LoadForEdit(filepath.Join(dir, "hiq.toml"))
	if _, ok := findPluginEntry(projectCfg.Plugins, "broken"); ok {
		t.Fatalf("project plugin should be removed after desktop migration: %+v", projectCfg.Plugins)
	}
	if !mcpFailed(app.activeCtrl(), "broken") {
		t.Fatalf("Host.Failures() = %+v, want broken failure recorded", app.activeCtrl().Host().Failures())
	}
	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "broken" {
			if s.Status != "failed" {
				t.Fatalf("server status = %q, want failed; server = %+v", s.Status, s)
			}
			if s.Tier != "background" {
				t.Fatalf("server tier = %q, want background so radio selection does not jump back", s.Tier)
			}
			return
		}
	}
	t.Fatalf("broken MCP missing from Capabilities: %+v", view.Servers)
}

func TestSetMCPServerTierEnablesCodegraphAndIgnoresLegacyTier(t *testing.T) {
	t.Setenv("HOME", robustTempDir(t))
	t.Setenv("USERPROFILE", robustTempDir(t))
	t.Setenv("XDG_CONFIG_HOME", robustTempDir(t))
	t.Setenv("AppData", robustTempDir(t))
	t.Setenv("PATH", robustTempDir(t))
	t.Setenv("HIQ_CACHE_DIR", robustTempDir(t)) // isolate the codegraph bundle cache so Resolve fails deterministically
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "hiq.toml"), []byte(`
[codegraph]
enabled = false
auto_install = true
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	if err := app.SetMCPServerTier("codegraph", "eager"); err != nil {
		t.Fatalf("SetMCPServerTier(codegraph): %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Codegraph.Enabled {
		t.Fatal("codegraph enabled = false, want true after legacy tier update")
	}
	if got := cfg.Codegraph.Tier; got != "" {
		t.Fatalf("codegraph tier = %q, want ignored legacy tier", got)
	}
	userCfg := config.LoadForEdit(config.UserConfigPath())
	if !userCfg.Codegraph.Enabled {
		t.Fatal("user codegraph enabled = false, want true after legacy tier update")
	}
	if got := userCfg.Codegraph.Tier; got != "" {
		t.Fatalf("user codegraph tier = %q, want ignored legacy tier", got)
	}
	if !mcpFailed(app.activeCtrl(), "codegraph") {
		t.Fatalf("Host.Failures() = %+v, want codegraph failure recorded for missing runtime", app.activeCtrl().Host().Failures())
	}
	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "codegraph" {
			if s.Status != "failed" {
				t.Fatalf("codegraph status = %q, want failed; server = %+v", s.Status, s)
			}
			if !s.BuiltIn || !s.Configured || s.Tier != "background" || !s.AutoStart {
				t.Fatalf("codegraph view did not preserve built-in config: %+v", s)
			}
			return
		}
	}
	t.Fatalf("codegraph missing from Capabilities: %+v", view.Servers)
}

func TestSetMCPServerEnabledPersistsCodegraphOff(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "hiq.toml"), []byte(`
[codegraph]
enabled = true
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	if err := app.SetMCPServerEnabled("codegraph", false); err != nil {
		t.Fatalf("SetMCPServerEnabled(codegraph,false): %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Codegraph.Enabled {
		t.Fatal("codegraph enabled = true, want false after disabling")
	}
	userCfg := config.LoadForEdit(config.UserConfigPath())
	if userCfg.Codegraph.Enabled {
		t.Fatal("user codegraph enabled = true, want false after disabling")
	}
	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "codegraph" {
			if s.Status != "disabled" || s.AutoStart {
				t.Fatalf("codegraph disabled view = %+v, want disabled with autoStart=false", s)
			}
			return
		}
	}
	t.Fatalf("codegraph missing from Capabilities: %+v", view.Servers)
}

func TestCapabilitiesMigratesFailedMCPConfiguredTierAfterRestart(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "hiq.toml"), []byte(`
[codegraph]
enabled = false

[[plugins]]
name = "broken"
command = "hiq-missing-mcp-binary"
tier = "eager"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()
	recordMCPFailure(app.activeCtrl(), config.PluginEntry{
		Name:    "broken",
		Command: "hiq-missing-mcp-binary",
		Tier:    "eager",
	}, errors.New("connect: missing binary"))

	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "broken" {
			if s.Status != "failed" {
				t.Fatalf("server status = %q, want failed; server = %+v", s.Status, s)
			}
			if s.Tier != "background" {
				t.Fatalf("server tier = %q, want migrated background default", s.Tier)
			}
			if !s.Configured {
				t.Fatalf("server configured = false, want true; server = %+v", s)
			}
			return
		}
	}
	t.Fatalf("broken MCP missing from Capabilities: %+v", view.Servers)
}

func TestRunShellForTabRoutesToRequestedTab(t *testing.T) {
	isolateDesktopUserDirs(t)

	activeEvents := make(chan event.Event, 16)
	inactiveEvents := make(chan event.Event, 16)
	activeCtrl := control.New(control.Options{Sink: event.FuncSink(func(e event.Event) { activeEvents <- e })})
	inactiveCtrl := control.New(control.Options{Sink: event.FuncSink(func(e event.Event) { inactiveEvents <- e })})
	defer activeCtrl.Close()
	defer inactiveCtrl.Close()

	app := &App{
		tabs: map[string]*WorkspaceTab{
			"active":   {ID: "active", Scope: "global", Ctrl: activeCtrl, Ready: true},
			"inactive": {ID: "inactive", Scope: "global", Ctrl: inactiveCtrl, Ready: true},
		},
		tabOrder:    []string{"active", "inactive"},
		activeTabID: "active",
	}

	app.RunShellForTab("inactive", "echo route-test")

	sawDispatch := false
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e := <-inactiveEvents:
			if e.Kind == event.ToolDispatch && strings.Contains(e.Tool.Args, "route-test") {
				sawDispatch = true
			}
			if e.Kind == event.TurnDone {
				if !sawDispatch {
					t.Fatal("inactive tab finished without receiving shell dispatch")
				}
				select {
				case active := <-activeEvents:
					t.Fatalf("active tab received event for inactive shell: %+v", active)
				default:
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for inactive shell turn")
		}
	}
}

type blockingRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockingRunner) Run(ctx context.Context, _ any) error {
	close(r.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.release:
		return nil
	}
}

func waitNotRunning(t *testing.T, ctrl tabSession) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for ctrl.Running() {
		if time.Now().After(deadline) {
			t.Fatal("controller still running")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func hasLevel(levels []string, want string) bool {
	for _, level := range levels {
		if level == want {
			return true
		}
	}
	return false
}

func hasCommand(cmds []CommandInfo, name string) bool {
	for _, cmd := range cmds {
		if cmd.Name == name {
			return true
		}
	}
	return false
}

func hasDirEntry(entries []DirEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name == name {
			return true
		}
	}
	return false
}

func TestSessionActionsWithoutControllerReturnError(t *testing.T) {
	app := &App{tabs: map[string]*WorkspaceTab{}}
	if err := app.NewSession(); err == nil {
		t.Error("NewSession with no controller must surface an error, not silently no-op")
	}
	if err := app.ClearSession(); err == nil {
		t.Error("ClearSession with no controller must surface an error")
	}

	app = &App{
		tabs:        map[string]*WorkspaceTab{"t1": {ID: "t1", StartupErr: "boot exploded"}},
		activeTabID: "t1",
	}
	err := app.NewSession()
	if err == nil || !strings.Contains(err.Error(), "boot exploded") {
		t.Errorf("error should carry the tab's startup failure, got %v", err)
	}
}
