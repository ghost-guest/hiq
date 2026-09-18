package netdev

import (
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// Word-table coverage — the spec's §2.3 samples, bilingual, three per class.
func TestRoleFromWords(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// firewall
		{"FW-01", RoleFirewall},
		{"核心防火墙", RoleFirewall},
		{"USG6000V", RoleFirewall},
		{"asa-5525", RoleFirewall},
		// router
		{"AR201", RoleRouter},
		{"NE40E-M2", RoleRouter},
		{"出口路由器-01", RoleRouter},
		{"isr4451", RoleRouter},
		// switch
		{"CORE-SW-01", RoleSwitch},
		{"S5700-52C", RoleSwitch},
		{"CE6800", RoleSwitch},
		{"接入交换机-3F", RoleSwitch},
		{"catalyst9300", RoleSwitch},
		// ips / vpn / bastion
		{"IPS-探针", RoleIPS},
		{"IDS01", RoleIPS},
		{"入侵检测设备", RoleIPS},
		{"vpn-gw", RoleVPN},
		{"IPSEC网关", RoleVPN},
		{"堡垒机-jump", RoleBastion},
		{"jumpserver", RoleBastion},
		// ap / cloud
		{"AP-3F-02", RoleAP},
		{"AC-01", RoleAP},
		{"无线控制器", RoleAP},
		{"internet-exit", RoleCloud},
		{"运营商出口", RoleCloud},
		// server (most generic, checked last)
		{"SRV-ESXi-01", RoleServer},
		{"web-server-03", RoleServer},
		{"node-7", RoleServer},
		// negative / boundary
		{"answer-machine", RoleUnknown},   // "sw"/"ap" must not substring-match
		{"SNAPSHOT-01", RoleUnknown},      // \b boundaries
		{"ACC-01", RoleUnknown},           // access is a TIER word, not a role
		{"CORE-01", RoleUnknown},          // core likewise
		{"", RoleUnknown},                 // empty
		{"核心 CORE-01 核心层设备", RoleUnknown}, // tier words carry no role signal
	}
	for _, c := range cases {
		if got, ok := roleFromWords(c.in); got != c.want {
			t.Errorf("roleFromWords(%q) = %q (hit=%v), want %q", c.in, got, ok, c.want)
		}
	}
}

// The §2.3 priority chain: config > kind > group > model/name > vendor > none.
func TestInferDeviceRolePriority(t *testing.T) {
	cases := []struct {
		name   string
		dev    config.NetDevDevice
		want   string
		source string
	}{
		{
			name:   "config role wins over everything",
			dev:    config.NetDevDevice{Name: "S5700", Vendor: "huawei-vrp", Group: "核心", Role: "router"},
			want:   RoleRouter,
			source: RoleSourceConfig,
		},
		{
			name:   "chinese config alias normalizes",
			dev:    config.NetDevDevice{Name: "box-1", Vendor: "huawei-vrp", Role: "防火墙"},
			want:   RoleFirewall,
			source: RoleSourceConfig,
		},
		{
			name:   "kind=firewall beats group words",
			dev:    config.NetDevDevice{Name: "edge-1", Vendor: "huawei-vrp", Kind: "firewall", Group: "接入"},
			want:   RoleFirewall,
			source: RoleSourceKind,
		},
		{
			name:   "kind=k8s reads as server",
			dev:    config.NetDevDevice{Name: "k8s-node-1", Kind: "k8s"},
			want:   RoleServer,
			source: RoleSourceKind,
		},
		{
			name:   "group words beat model words",
			dev:    config.NetDevDevice{Name: "S5700", Vendor: "huawei-vrp", Group: "无线接入"},
			want:   RoleAP,
			source: RoleSourceGroup,
		},
		{
			name:   "model words beat vendor default",
			dev:    config.NetDevDevice{Name: "AR201", Vendor: "huawei-vrp"},
			want:   RoleRouter,
			source: RoleSourceModel,
		},
		{
			name:   "vendor default: network gear reads as switch",
			dev:    config.NetDevDevice{Name: "box-1", Vendor: "cisco-ios"},
			want:   RoleSwitch,
			source: RoleSourceVendor,
		},
		{
			name:   "vendor default: esxi reads as server",
			dev:    config.NetDevDevice{Name: "host-1", Vendor: "esxi"},
			want:   RoleServer,
			source: RoleSourceVendor,
		},
		{
			name:   "unknown vendor with no signals stays unknown",
			dev:    config.NetDevDevice{Name: "thing-1", Vendor: "custom"},
			want:   RoleUnknown,
			source: RoleSourceNone,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, src := InferDeviceRole(c.dev)
			if got != c.want || src != c.source {
				t.Errorf("InferDeviceRole = (%q, %q), want (%q, %q)", got, src, c.want, c.source)
			}
		})
	}
}

func TestRoleFromName(t *testing.T) {
	if r, src := RoleFromName("USG6000V"); r != RoleFirewall || src != RoleSourceLabel {
		t.Errorf("RoleFromName(USG6000V) = (%q,%q), want (firewall,label)", r, src)
	}
	if r, src := RoleFromName("CORE-01"); r != RoleUnknown || src != RoleSourceNone {
		t.Errorf("RoleFromName(CORE-01) = (%q,%q), want (\"\",none)", r, src)
	}
}
