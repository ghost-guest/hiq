package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// isolateUserConfig points every platform's user-config/home resolution at a
// fresh temp dir and clears the memory-root probe cache. Windows reads %AppData%
// (XDG_CONFIG_HOME alone does NOT isolate there) and HOME/USERPROFILE back
// os.UserHomeDir — missing any of these lets a test read or write the
// developer's real config.
func isolateUserConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	t.Setenv(MemoryRootEnv, "")
	resetMemoryRootProbe()
	return home
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestMemoryRootDefaultsToUserConfigDir(t *testing.T) {
	isolateUserConfig(t)
	want := userDir()
	if want == "" {
		t.Fatal("userDir() must resolve under an isolated HOME/AppData")
	}
	if got := MemoryRoot(); got != want {
		t.Fatalf("MemoryRoot() = %q, want the default %q", got, want)
	}
	if got := MemoryUserDir(); got != want {
		t.Fatalf("MemoryUserDir() = %q, want %q", got, want)
	}
}

func TestMemoryRootFromUserConfigAndEnv(t *testing.T) {
	isolateUserConfig(t)
	target := filepath.Join(t.TempDir(), "hiq-data")
	cfg := Default()
	cfg.Memory = MemoryConfig{Root: target}
	if err := cfg.SaveTo(UserConfigPath()); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	resetMemoryRootProbe()
	if got := MemoryRoot(); got != filepath.Clean(target) {
		t.Fatalf("MemoryRoot() = %q, want the configured %q", got, target)
	}

	// The env var outranks the config file (portable / per-run override).
	envRoot := filepath.Join(t.TempDir(), "env-data")
	t.Setenv(MemoryRootEnv, envRoot)
	if got := MemoryRoot(); got != filepath.Clean(envRoot) {
		t.Fatalf("MemoryRoot() = %q, want the env override %q", got, envRoot)
	}
}

// A cloned repo must not be able to relocate the user's memory tree.
func TestMemoryConfigPinsToUserConfig(t *testing.T) {
	isolateUserConfig(t)
	userRoot := filepath.Join(t.TempDir(), "user-data")
	projRoot := filepath.Join(t.TempDir(), "proj-data")

	cfg := Default()
	cfg.Memory = MemoryConfig{Root: userRoot, Provider: "yyqwen", Model: "cheap-1"}
	if err := cfg.SaveTo(UserConfigPath()); err != nil {
		t.Fatalf("SaveTo user: %v", err)
	}

	project := t.TempDir()
	writeFile(t, filepath.Join(project, "hiq.toml"),
		"[memory]\nroot = "+strconv.Quote(projRoot)+"\nprovider = \"attacker\"\n")

	resetMemoryRootProbe()
	got, err := LoadForRoot(project)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if got.Memory.Root != userRoot {
		t.Fatalf("project config leaked into [memory].root: got %q, want the user value %q", got.Memory.Root, userRoot)
	}
	if got.Memory.Provider != "yyqwen" {
		t.Fatalf("project config leaked into [memory].provider: got %q", got.Memory.Provider)
	}
	if ref := got.MemoryMaintenanceRef(); ref != "yyqwen/cheap-1" {
		t.Fatalf("MemoryMaintenanceRef() = %q, want yyqwen/cheap-1", ref)
	}
}

func TestMemoryMaintenanceRefFallback(t *testing.T) {
	cases := []struct {
		name string
		mem  MemoryConfig
		fast string
		want string
	}{
		{"provider+model", MemoryConfig{Provider: "yyqwen", Model: "m1"}, "other/m2", "yyqwen/m1"},
		{"provider-only", MemoryConfig{Provider: "yyqwen"}, "", "yyqwen"},
		{"model-only", MemoryConfig{Model: "cheap"}, "", "cheap"},
		{"falls-back-to-fast-task", MemoryConfig{}, "aiaaa/deepseek-v4.1-flash", "aiaaa/deepseek-v4.1-flash"},
		{"unset", MemoryConfig{}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.Memory = tc.mem
			c.Agent.FastTaskModel = tc.fast
			if got := c.MemoryMaintenanceRef(); got != tc.want {
				t.Fatalf("MemoryMaintenanceRef() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMemoryRootRendersAndReloads(t *testing.T) {
	isolateUserConfig(t)
	target := filepath.Join(t.TempDir(), "data")
	c := Default()
	c.Memory = MemoryConfig{Root: target, Provider: "yyqwen", Model: "m", Effort: "low"}
	body := RenderTOMLForScope(c, RenderScopeUser)
	if !strings.Contains(body, "[memory]") {
		t.Fatalf("user config must render a [memory] section:\n%s", body)
	}
	writeFile(t, UserConfigPath(), body)
	resetMemoryRootProbe()
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Memory.Root != target || got.Memory.Provider != "yyqwen" || got.Memory.Model != "m" || got.Memory.Effort != "low" {
		t.Fatalf("[memory] did not round-trip: %+v", got.Memory)
	}
	// Project scope keeps it out: the section is user-global.
	if project := RenderTOMLForScope(c, RenderScopeProject); strings.Contains(project, "[memory]") {
		t.Fatal("[memory] must not be rendered into a project hiq.toml")
	}
}

func TestMigrateMemoryTreeCopiesWithoutOverwriting(t *testing.T) {
	from := t.TempDir()
	to := t.TempDir()

	writeFile(t, filepath.Join(from, "profile", "user.md"), "# 关于用户\n后端工程师")
	writeFile(t, filepath.Join(from, "memory", "dev", "MEMORY.md"), "# Memory\n")
	writeFile(t, filepath.Join(from, "projects", "-proj", "dev", "sessions", "s1.jsonl"), `{"a":1}`)
	writeFile(t, filepath.Join(from, "AGENTS.md"), "user-global doc")
	writeFile(t, filepath.Join(from, "skill_usage.json"), `{}`)

	// A pre-existing destination file must win (idempotent re-runs).
	writeFile(t, filepath.Join(to, "memory", "dev", "MEMORY.md"), "# Newer\n")

	rep, err := MigrateMemoryTree(from, to)
	if err != nil {
		t.Fatalf("MigrateMemoryTree: %v", err)
	}
	for _, rel := range []string{
		"profile/user.md",
		"projects/-proj/dev/sessions/s1.jsonl",
		"AGENTS.md",
	} {
		if _, err := os.Stat(filepath.Join(to, filepath.FromSlash(rel))); err != nil {
			t.Errorf("expected %s at the destination: %v", rel, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(to, "memory", "dev", "MEMORY.md")); string(got) != "# Newer\n" {
		t.Errorf("destination file was overwritten: %q", got)
	}
	// The source is left intact — migration copies, never moves.
	if _, err := os.Stat(filepath.Join(from, "profile", "user.md")); err != nil {
		t.Errorf("source must survive the migration: %v", err)
	}
	if rep.Files == 0 || rep.Bytes == 0 {
		t.Errorf("report must account for the copied files: %+v", rep)
	}
	if len(rep.Copied) == 0 {
		t.Errorf("report must list the copied entries: %+v", rep)
	}
}

func TestMigrateMemoryTreeRejectsBadTargets(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "profile", "user.md"), "x")

	if _, err := MigrateMemoryTree(root, root); err == nil {
		t.Error("same source and destination must be rejected")
	}
	if _, err := MigrateMemoryTree(root, filepath.Join(root, "nested")); err == nil {
		t.Error("a destination inside the source must be rejected")
	}
	if _, err := MigrateMemoryTree(root, ""); err == nil {
		t.Error("an empty destination must be rejected")
	}
	if _, err := MigrateMemoryTree(filepath.Join(root, "missing"), t.TempDir()); err == nil {
		t.Error("a missing source must be rejected")
	}
}
