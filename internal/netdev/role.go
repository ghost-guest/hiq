package netdev

import (
	"regexp"
	"strings"

	"github.com/zzycxz/hiq/internal/config"
)

// role.go — the device-class vocabulary shared by the topology icons (T1),
// the design-import semantic layer (T2) and the fingerprint tiers (F4).
// Role is ORTHOGONAL to tier: tier says which band a node sits in
// (core/agg/access), role says what kind of box it is (router/switch/…).
// Health stays on stroke color, managed on solid/dashed — one dimension per
// visual channel, never overloaded.

// Role values (NETDEV_IMPORT_AND_FINGERPRINT_SPEC §2.1). Keep in sync with
// the frontend TopoIcon set and the §2.3 word table below.
const (
	RoleRouter   = "router"
	RoleSwitch   = "switch"
	RoleFirewall = "firewall"
	RoleIPS      = "ips"
	RoleVPN      = "vpn"
	RoleBastion  = "bastion"
	RoleServer   = "server"
	RoleAP       = "ap"
	RoleCloud    = "cloud"
	RoleUnknown  = ""
)

// RoleSource records how a role was derived, coarse-grained for the UI's
// confidence hints: config > kind > group > model > vendor > neighbor/label
// (import/remote) > none.
const (
	RoleSourceConfig   = "config"
	RoleSourceKind     = "kind"
	RoleSourceGroup    = "group"
	RoleSourceModel    = "model"
	RoleSourceVendor   = "vendor"
	RoleSourceNeighbor = "neighbor"
	RoleSourceLabel    = "label"
	RoleSourceNone     = "none"
)

// normalizeRole maps free-text (config file / imported design) onto the
// vocabulary; unknown spellings collapse to "" so the UI renders the
// unknown icon instead of silently inventing a new class.
func normalizeRole(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "router", "路由器", "路由":
		return RoleRouter
	case "switch", "交换机", "交换":
		return RoleSwitch
	case "firewall", "防火墙", "fw":
		return RoleFirewall
	case "ips", "ids", "入侵检测", "入侵防御":
		return RoleIPS
	case "vpn", "vpn网关", "vpn-网关":
		return RoleVPN
	case "bastion", "堡垒机":
		return RoleBastion
	case "server", "服务器":
		return RoleServer
	case "ap", "无线", "wireless":
		return RoleAP
	case "cloud", "云", "internet":
		return RoleCloud
	default:
		return RoleUnknown
	}
}

// roleMatcher is one row of the §2.3 word table. Chinese needles match as
// substrings; English/model tokens match with word boundaries so "SW-01"
// hits switch but "answer" or "SNAP" never does.
type roleMatcher struct {
	role   string
	re     *regexp.Regexp
	substr []string
}

// Word-boundary helper: the pattern is interpolated as-is, so every row is
// written with \b…\b around bare tokens.
func mkRole(role string, pattern string, substr ...string) roleMatcher {
	return roleMatcher{role: role, re: regexp.MustCompile(`(?i)` + pattern), substr: substr}
}

// roleTable is checked IN ORDER — first hit wins, so put the most specific
// classes (security gear) above the generic ones.
var roleTable = []roleMatcher{
	mkRole(RoleFirewall, `\bfw\b|firewall|\busg|\basa\b|\bftd|\baf[-_]`, "防火墙", "山石"),
	mkRole(RoleRouter, `router|\brt[-_]|\bar\d+|\bne\d+|\bisr|\basr`, "路由"),
	mkRole(RoleSwitch, `\bsw\b|switch|\bs\d{4}\b|\bce\d{4}\b|catalyst|nexus|\bls[-_]`, "交换"),
	// ips keeps its trailing boundary: `\bips` alone would swallow "ipsec".
	mkRole(RoleIPS, `\bips\b|\bips\d+|\bids\b|\bids\d+`, "入侵检测", "入侵防御"),
	mkRole(RoleVPN, `\bvpn\b|ipsec|ssl-?gw|ssl-?网关`),
	mkRole(RoleBastion, `bastion|\bjump\b|\bjumpserver\b`, "堡垒"),
	mkRole(RoleAP, `\bap\d*\b|\bac\d*\b|\bwap\b`, "无线", "wifi"),
	mkRole(RoleCloud, `internet|\bisp\b|\bwan\b|cloud`, "运营商"),
	// server stays last: its tokens (node/vm/server) are the most generic.
	mkRole(RoleServer, `\bsrv\b|server|esxi|\bpve\b|\bvm\b|\bnode\b|\bwindows\b|\blinux\b`),
}

// roleFromWords runs the table over one free-text field. Wireless "wlan"
// reads as AP-class too but is kept out of the regex to avoid matching
// SSID-ish config strings; the Chinese 无线 covers the common case.
func roleFromWords(s string) (string, bool) {
	if s == "" {
		return RoleUnknown, false
	}
	for _, m := range roleTable {
		for _, sub := range m.substr {
			if strings.Contains(s, sub) {
				return m.role, true
			}
		}
		if m.re.MatchString(s) {
			return m.role, true
		}
	}
	return RoleUnknown, false
}

// InferDeviceRole is the §2.3 priority chain for an inventory device:
// explicit config role > data-plane kind > group words > model/name words >
// vendor default > unknown. The neighbor-platform rung arrives with F2.
func InferDeviceRole(d config.NetDevDevice) (role, source string) {
	if r := normalizeRole(d.Role); r != RoleUnknown {
		return r, RoleSourceConfig
	}
	switch strings.ToLower(strings.TrimSpace(d.Kind)) {
	case "firewall":
		return RoleFirewall, RoleSourceKind
	case "docker", "k8s":
		return RoleServer, RoleSourceKind
	}
	if r, ok := roleFromWords(d.Group); ok {
		return r, RoleSourceGroup
	}
	if r, ok := roleFromWords(d.Model + " " + d.Name); ok {
		return r, RoleSourceModel
	}
	switch d.Vendor {
	case "huawei-vrp", "cisco-ios", "zte-zxr10":
		// Enterprise networks are switch-majority; the plan-view tooltip says
		// inferred and the fix is an explicit role in the inventory.
		return RoleSwitch, RoleSourceVendor
	case "esxi", "redfish", "linux", "windows":
		return RoleServer, RoleSourceVendor
	}
	return RoleUnknown, RoleSourceNone
}

// RoleFromName classifies a node that exists only as a label — an unmanaged
// LLDP/CDP neighbor or an imported design shape. Weaker than the inventory
// chain (no vendor/config to lean on): keyword hit or unknown.
func RoleFromName(name string) (role, source string) {
	if r, ok := roleFromWords(name); ok {
		return r, RoleSourceLabel
	}
	return RoleUnknown, RoleSourceNone
}
