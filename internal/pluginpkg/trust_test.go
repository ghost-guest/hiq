package pluginpkg

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// trustFullManifest declares every contribution class so the gate can be
// exercised against a package that spans both declarative and executing
// surfaces.
const trustFullManifest = `{
  "apiVersion": "hiq.io/plugin/v2",
  "name": "full",
  "version": "1.0.0",
  "contributes": {
    "skills": ["skills"],
    "commands": ["commands"],
    "prompts": ["prompts"],
    "themes": ["themes/*.hiq-theme"]
  },
  "hooks": {"PreToolUse": [{"match": "Bash", "command": "./pre.sh"}]},
  "mcpServers": {"srv": {"type": "stdio", "command": "./srv"}},
  "runtime": {"command": "./run"}%s
}`

// writeTrustPlugin materializes a full-surface package; extra is spliced before
// the closing brace (e.g. `,\n  "trust": "full-access"`).
func writeTrustPlugin(t *testing.T, extra string) Package {
	t.Helper()
	root := t.TempDir()
	writeV2Plugin(t, root, fmt.Sprintf(trustFullManifest, extra))
	writeTestFile(t, filepath.Join(root, "skills", "demo", "SKILL.md"), "---\ndescription: demo\n---\nDemo")
	writeTestFile(t, filepath.Join(root, "commands", "ship.md"), "---\ndescription: ship\n---\nShip")
	writeTestFile(t, filepath.Join(root, "prompts", "plan.md"), "---\ndescription: plan\n---\nPlan")
	writeTestFile(t, filepath.Join(root, "themes", "neon.hiq-theme"), "bytes")
	writeTestFile(t, filepath.Join(root, "pre.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(root, "srv"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(root, "run"), "#!/bin/sh\n")
	pkg, _, err := ParseDir(root)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	return pkg
}

func TestNormalizeTrustTier(t *testing.T) {
	cases := []struct {
		in   TrustTier
		want TrustTier
	}{
		{"", TrustRestricted},
		{"  ", TrustRestricted},
		{"restricted", TrustRestricted},
		{"full-access", TrustFullAccess},
		{"full_access", TrustRestricted}, // unknown spelling → safe default
		{"FULL-ACCESS", TrustRestricted}, // case-sensitive → safe default
		{"root", TrustRestricted},
	}
	for _, tc := range cases {
		if got := NormalizeTrustTier(tc.in); got != tc.want {
			t.Errorf("NormalizeTrustTier(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if !TrustRestricted.Valid() || !TrustFullAccess.Valid() {
		t.Fatal("both defined tiers must be valid")
	}
	if TrustTier("root").Valid() {
		t.Fatal("unknown tier must not be valid")
	}
}

func TestGateRestrictedWithholdsExecutingSurfaces(t *testing.T) {
	pkg := writeTrustPlugin(t, "")
	gate := GateFor(pkg, TrustRestricted)

	if !gate.Withholds(SurfaceRuntime) || !gate.Withholds(SurfaceHook) || !gate.Withholds(SurfaceMCPServer) {
		t.Fatalf("restricted withheld = %+v, want runtime+hook+mcpServer", gate.Blocked)
	}
	if gate.AllowsExecution() {
		t.Fatal("restricted must not allow execution")
	}
	if gate.BlockedCount() != 3 { // 1 runtime + 1 hook + 1 MCP server
		t.Fatalf("BlockedCount = %d, want 3", gate.BlockedCount())
	}
	// Declarative surfaces survive untouched.
	if gate.Allowed.Skills != 1 || gate.Allowed.Commands != 1 || gate.Allowed.Prompts != 1 || gate.Allowed.Themes != 1 {
		t.Fatalf("declarative surfaces altered: %+v", gate.Allowed)
	}
	if gate.Allowed.Runtime || gate.Allowed.Hooks != 0 || gate.Allowed.MCPServers != 0 {
		t.Fatalf("executing surfaces not withheld from Allowed: %+v", gate.Allowed)
	}
	for _, want := range []string{"1 runtime", "1 hook", "1 MCP server"} {
		if !strings.Contains(gate.Summary(), want) {
			t.Fatalf("Summary() = %q, want it to contain %q", gate.Summary(), want)
		}
	}
}

func TestGateFullAccessAllowsEverything(t *testing.T) {
	pkg := writeTrustPlugin(t, "")
	gate := GateFor(pkg, TrustFullAccess)

	if !gate.AllowsExecution() || gate.BlockedCount() != 0 {
		t.Fatalf("full-access gate = %+v, want nothing withheld", gate)
	}
	if !gate.Allowed.Runtime || gate.Allowed.Hooks != 1 || gate.Allowed.MCPServers != 1 {
		t.Fatalf("full-access Allowed = %+v, want executing surfaces present", gate.Allowed)
	}
	if gate.Summary() != "" {
		t.Fatalf("Summary() = %q, want empty", gate.Summary())
	}
}

func TestGateForInstalledUsesGrantedTier(t *testing.T) {
	pkg := writeTrustPlugin(t, "")

	// Empty grant falls back to restricted — a missing grant never widens.
	if got := EffectiveTrustTier(InstalledPlugin{Name: "full"}); got != TrustRestricted {
		t.Fatalf("empty grant = %q, want restricted", got)
	}
	if gate := GateForInstalled(InstalledPlugin{Name: "full"}, pkg); gate.AllowsExecution() {
		t.Fatal("unset grant must gate execution")
	}
	granted := InstalledPlugin{Name: "full", TrustTier: TrustFullAccess}
	if gate := GateForInstalled(granted, pkg); !gate.AllowsExecution() {
		t.Fatalf("full-access grant gate = %+v, want execution allowed", gate)
	}
}

func TestRequestedTrustReadsManifestHint(t *testing.T) {
	plain := writeTrustPlugin(t, "")
	if got := plain.RequestedTrust(); got != TrustRestricted {
		t.Fatalf("no hint = %q, want restricted", got)
	}
	asked := writeTrustPlugin(t, ",\n  \"trust\": \"full-access\"")
	if got := asked.RequestedTrust(); got != TrustFullAccess {
		t.Fatalf("hint = %q, want full-access", got)
	}
}

func TestParseRejectsUnknownTrustTier(t *testing.T) {
	root := t.TempDir()
	writeV2Plugin(t, root, `{
  "apiVersion": "hiq.io/plugin/v2",
  "name": "typo",
  "trust": "root"
}`)
	_, _, err := ParseDir(root)
	if err == nil || !strings.Contains(err.Error(), "trust must be") {
		t.Fatalf("err = %v, want a trust-tier rejection", err)
	}
}

func TestSetTrustTierPersistsAndValidates(t *testing.T) {
	home := t.TempDir()
	if err := SaveState(home, State{Version: 1, Plugins: []InstalledPlugin{
		{Name: "p", Root: "plugins/p", Enabled: true},
	}}); err != nil {
		t.Fatal(err)
	}

	if err := SetTrustTier(home, "p", TrustFullAccess); err != nil {
		t.Fatalf("SetTrustTier: %v", err)
	}
	st, err := LoadState(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Plugins) != 1 || st.Plugins[0].TrustTier != TrustFullAccess {
		t.Fatalf("persisted state = %+v, want full-access", st.Plugins)
	}

	if err := SetTrustTier(home, "p", TrustTier("root")); err == nil {
		t.Fatal("SetTrustTier accepted an unknown tier")
	}
	if err := SetTrustTier(home, "missing", TrustRestricted); err == nil {
		t.Fatal("SetTrustTier accepted an unknown plugin")
	}
}

func TestUpsertPreservesExistingTrustGrant(t *testing.T) {
	home := t.TempDir()
	if err := Upsert(home, InstalledPlugin{Name: "p", Root: "plugins/p", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := SetTrustTier(home, "p", TrustFullAccess); err != nil {
		t.Fatal(err)
	}

	// A reinstall/upgrade passes a fresh entry with no tier; the granted tier
	// must survive so an upgrade never silently drops the user's decision.
	if err := Upsert(home, InstalledPlugin{Name: "p", Root: "plugins/p", Version: "2.0.0", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Plugins) != 1 || st.Plugins[0].TrustTier != TrustFullAccess {
		t.Fatalf("grant dropped on upsert: %+v", st.Plugins)
	}
	if st.Plugins[0].Version != "2.0.0" {
		t.Fatalf("upsert did not refresh the entry: %+v", st.Plugins[0])
	}
}
