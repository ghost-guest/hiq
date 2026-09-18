package netdev

import (
	"net"
	"sort"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

// iptopo.go — the LOCAL topology view (the user's rendering doctrine):
// the program computes the network's architecture itself, mostly from the
// intranet IP plan, with ZERO device sessions and ZERO model calls. The map
// shows on click; real link data (LLDP/CDP) only arrives when the user
// explicitly asks for the measured sweep — the two never mix silently.
//
// Inference order per device (first hit wins):
//  1. the user's own group assignment (核心/汇聚/接入… — their words are the
//     ground truth, the view is just another surface for it);
//  2. name conventions (CORE/AGG/ACC/FW prefixes common in IP plans);
//  3. subnet ordering: with nothing else to go on, devices in the numerically
//     lowest /24 read as closer to the core (10.0.0.x under 10.0.2.x in a
//     planned network) — marked inferred, correctable by assigning a group.
//
// Links are NEVER fabricated from IPs: two devices sharing a management
// subnet proves nothing about their physical wiring. The plan view draws
// structure (bands + subnet clusters); edges exist only from real neighbor
// data or an explicit bastion (via) chain.
func InferTopology(cfg *config.Config) TopologyGraph {
	// Nodes/Edges start as EMPTY slices, never nil: the bridge serializes nil
	// as JSON null and the map's stats line reads graph.edges.length — a nil
	// slice crashed the packaged app on first open (mock always sent []).
	g := TopologyGraph{At: time.Now().Format("15:04:05"), Nodes: []TopologyNode{}, Edges: []TopologyEdge{}}
	if cfg == nil {
		return g
	}

	// Collect the /24 of every device address; subnets are both a clustering
	// signal and the tooltip's "网段" annotation.
	subnets := map[string]bool{}
	type row struct {
		d      config.NetDevDevice
		subnet string
	}
	rows := make([]row, 0, len(cfg.NetDev.Devices))
	for _, d := range cfg.NetDev.Devices {
		subnet := subnetOf(d.Address)
		subnets[subnet] = true
		rows = append(rows, row{d, subnet})
	}
	ordered := make([]string, 0, len(subnets))
	for s := range subnets {
		ordered = append(ordered, s)
	}
	sort.Strings(ordered) // numeric-ish: "10.0.0.0/24" < "10.0.2.0/24" lexicographically
	subnetRank := map[string]int{}
	for i, s := range ordered {
		subnetRank[s] = i
	}

	for _, r := range rows {
		role, src := InferDeviceRole(r.d)
		g.Nodes = append(g.Nodes, TopologyNode{
			Name:       r.d.Name,
			Managed:    true,
			DeviceIP:   r.d.Address,
			Subnet:     r.subnet,
			Tier:       inferTier(r.d, r.subnet, subnetRank, len(ordered)),
			Role:       role,
			RoleSource: src,
		})
	}

	// Bastion chains are real, configured facts — safe to draw.
	hopByName := map[string]bool{}
	for _, h := range cfg.NetDev.Hops {
		hopByName[h.Name] = true
	}
	for _, d := range cfg.NetDev.Devices {
		for _, via := range d.Via {
			if hopByName[via] {
				g.Edges = append(g.Edges, TopologyEdge{LocalDevice: via, RemoteDevice: d.Name, Source: "bastion"})
			}
		}
	}
	return g
}

// inferTier maps a device to a band: 0 core / 1 aggregation / 2 access /
// 3 unmanaged. Group words win; then name prefixes; then subnet position.
func inferTier(d config.NetDevDevice, subnet string, rank map[string]int, nSubnets int) int {
	g := strings.ToLower(d.Group + " " + d.Name)
	switch {
	case strings.Contains(g, "核心") || strings.Contains(strings.ToLower(d.Name), "core") || hasAnyFold(g, "fw", "firewall", "防火墙"):
		return 0
	case strings.Contains(g, "汇聚") || strings.Contains(strings.ToLower(d.Name), "agg"):
		return 1
	case strings.Contains(g, "接入") || strings.Contains(strings.ToLower(d.Name), "acc"):
		return 2
	}
	// Subnet heuristic only when there is more than one subnet to order by:
	// first third → core band, middle → aggregation, rest → access. Rough on
	// purpose — the tooltip says 推断 and the fix is a group assignment.
	if nSubnets > 1 {
		r := rank[subnet]
		switch {
		case r == 0:
			return 0
		case r < nSubnets/2:
			return 1
		default:
			return 2
		}
	}
	return 2
}

func hasAnyFold(s string, needles ...string) bool {
	l := strings.ToLower(s)
	for _, n := range needles {
		if strings.Contains(l, n) {
			return true
		}
	}
	return false
}

// subnetOf collapses an address to its /24 key; unparseable addresses keep
// their literal text so the tooltip still shows something honest.
func subnetOf(addr string) string {
	ip := net.ParseIP(strings.TrimSpace(strings.Split(addr, ":")[0]))
	if ip == nil {
		return strings.TrimSpace(addr)
	}
	ip = ip.To4()
	if ip == nil {
		return ip.String() + "/64"
	}
	return ip.Mask(net.CIDRMask(24, 32)).String() + "/24"
}
