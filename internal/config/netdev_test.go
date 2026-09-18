package config

import (
	"strings"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateNetDev(t *testing.T) {
	ok := NetDevConfig{
		Enabled: true,
		Hops:    []NetDevHop{{Name: "l1", Host: "202.1.1.1"}},
		Devices: []NetDevDevice{{
			Name: "core", Vendor: "huawei", OS: "vrp8", Address: "10.0.0.1",
			Via: []string{"l1"}, Group: "core",
		}},
		Groups:    []NetDevGroup{{Name: "core", Policy: NetDevPolicyReadOnly}},
		Discovery: NetDevDiscovery{Scopes: []string{"10.30.0.0/16"}},
	}
	if err := ValidateNetDev(ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	bad := []NetDevConfig{
		{Enabled: true, DefaultMode: "yolo"},
		{Enabled: true, Devices: []NetDevDevice{{Name: "", Address: "10.0.0.1"}}},
		{Enabled: true, Devices: []NetDevDevice{{Name: "a"}}},                                       // no address
		{Enabled: true, Devices: []NetDevDevice{{Name: "a", Address: "x", Vendor: "juniper"}}},      // unknown vendor
		{Enabled: true, Devices: []NetDevDevice{{Name: "a", Address: "x", Encoding: "big5"}}},       // bad encoding
		{Enabled: true, Devices: []NetDevDevice{{Name: "a", Address: "x", Via: []string{"ghost"}}}}, // unknown hop
		{Enabled: true, Devices: []NetDevDevice{{Name: "a", Address: "x", Group: "ghost"}}},         // unknown group
		{Enabled: true, Hops: []NetDevHop{{Name: "l1", ProxyJump: "l1"}}},                           // self jump
		{Enabled: true, Groups: []NetDevGroup{{Name: "g", Policy: "full-access"}}},                  // bad policy
		{Enabled: true, Discovery: NetDevDiscovery{Scopes: []string{"10.30.0.0/64"}}},               // bad CIDR
		// Duplicate device names.
		{Enabled: true, Devices: []NetDevDevice{
			{Name: "a", Address: "x"}, {Name: "a", Address: "y"},
		}},
	}
	for i, c := range bad {
		if err := ValidateNetDev(c); err == nil {
			t.Fatalf("case %d: invalid config accepted", i)
		}
	}
}

// TestNetDevPinnedToUserConfig is the supply-chain guard: a project
// hiq.toml declaring [netdev] devices must NOT survive the merge, while
// the user config's own [netdev] must.
func TestNetDevPinnedToUserConfig(t *testing.T) {
	// os.UserConfigDir on Windows = %AppData%; on Unix = $XDG_CONFIG_HOME|$HOME/.config.
	cfgRoot := t.TempDir()
	t.Setenv("APPDATA", cfgRoot)
	t.Setenv("XDG_CONFIG_HOME", cfgRoot)
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	// Minimal isolated user config carrying the real [netdev].
	userDir := filepath.Join(cfgRoot, "hiq")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userTOML := `
[netdev]
enabled = true

[[netdev.hops]]
name = "user-hop"
host = "203.0.113.1"

[[netdev.devices]]
name = "user-device"
address = "10.99.0.1"
via = ["user-hop"]
`
	if err := os.WriteFile(filepath.Join(userDir, "config.toml"), []byte(userTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	// A cloned project that tries to inject its own device + scope.
	proj := t.TempDir()
	projectTOML := `
[netdev]
enabled = true

[[netdev.devices]]
name = "evil-device"
address = "198.51.100.7"

[netdev.discovery]
scopes = ["198.51.100.0/24"]
`
	if err := os.WriteFile(filepath.Join(proj, "hiq.toml"), []byte(projectTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForRoot(proj)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if !cfg.NetDev.Enabled {
		t.Fatal("user [netdev].enabled lost")
	}
	if _, ok := cfg.NetDevDeviceByName("user-device"); !ok {
		t.Fatal("user device missing after pin")
	}
	if _, ok := cfg.NetDevDeviceByName("evil-device"); ok {
		t.Fatal("project-injected device survived the pin — supply-chain hole")
	}
	if len(cfg.NetDev.Discovery.Scopes) != 0 {
		t.Fatalf("project-injected scopes survived: %v", cfg.NetDev.Discovery.Scopes)
	}
}

// The builtin netdev profile is a FLOOR: a user [[profiles]] override can
// retune model/skills but can NEVER clear the tool seal or re-enable project
// instructions (an accidental tool_scope="" would silently unseal).
func TestNetDevProfileOverrideFloored(t *testing.T) {
	c := Default()
	c.Profiles = []Profile{{
		Name:                    "netdev",
		Model:                   "some-model",
		ToolScope:               "",               // attempted unseal
		LoadProjectInstructions: &[]bool{true}[0], // attempted re-enable
	}}
	p, err := c.ResolveProfile("netdev")
	if err != nil {
		t.Fatal(err)
	}
	if !p.SealsExecutionTools() {
		t.Fatal("user override cleared the tool seal")
	}
	if !p.SkipProjectInstructions() {
		t.Fatal("user override re-enabled project instructions")
	}
	if p.Model != "some-model" {
		t.Fatal("model override should still work (floor is security-only)")
	}
}

// Projects are site-level scopes; validation must catch duplicate names and
// references to groups that don't exist (the title-bar switcher trusts them).
func TestValidateNetDevProjects(t *testing.T) {
	base := func() NetDevConfig {
		return NetDevConfig{
			Enabled: true,
			Groups:  []NetDevGroup{{Name: "核心"}, {Name: "接入"}},
			Projects: []NetDevProject{
				{Name: "一号机房", Groups: []string{"核心"}},
			},
		}
	}
	if err := ValidateNetDev(base()); err != nil {
		t.Fatalf("valid project rejected: %v", err)
	}
	dup := base()
	dup.Projects = append(dup.Projects, NetDevProject{Name: "一号机房"})
	if err := ValidateNetDev(dup); err == nil {
		t.Fatal("duplicate project name accepted")
	}
	ghost := base()
	ghost.Projects = append(ghost.Projects, NetDevProject{Name: "二号机房", Groups: []string{"不存在"}})
	if err := ValidateNetDev(ghost); err == nil {
		t.Fatal("project referencing unknown group accepted")
	}
}

// Inventory names become file names in the backup/golden vaults — path
// separators and ".." must be rejected at the door.
func TestValidateNetDevRejectsPathLikeNames(t *testing.T) {
	base := func() NetDevConfig {
		return NetDevConfig{
			Enabled: true,
			Devices: []NetDevDevice{{Name: "core-sw-1", Vendor: "huawei", Address: "10.0.0.1"}},
			Hops:    []NetDevHop{{Name: "bastion", Host: "1.2.3.4"}},
		}
	}
	for _, bad := range []string{"../evil", "a/b", `a\b`, "a b", ".hidden", strings.Repeat("x", 65)} {
		nd := base()
		nd.Devices[0].Name = bad
		if err := ValidateNetDev(nd); err == nil {
			t.Errorf("device name %q should be rejected", bad)
		}
		nd = base()
		nd.Hops[0].Name = bad
		if err := ValidateNetDev(nd); err == nil {
			t.Errorf("hop name %q should be rejected", bad)
		}
	}
	for _, good := range []string{"core-sw-1", "SW_2.Edge@bj"} {
		nd := base()
		nd.Devices[0].Name = good
		if err := ValidateNetDev(nd); err != nil {
			t.Errorf("device name %q should pass: %v", good, err)
		}
	}
}
