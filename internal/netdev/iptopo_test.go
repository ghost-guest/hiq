package netdev

import (
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// The IP-plan view's contract: tiers come from the user's words first, then
// name conventions, then subnet ordering — and links are NEVER invented from
// shared subnets (only configured bastion chains are drawn).
func TestInferTopology(t *testing.T) {
	cfg := config.Default()
	cfg.NetDev = config.NetDevConfig{
		Enabled: true,
		Devices: []config.NetDevDevice{
			{Name: "core-sw-1", Address: "10.0.0.2", Group: "核心"},
			{Name: "AGG-SW-9", Address: "10.0.1.9"}, // name convention
			{Name: "acc-sw-3", Address: "10.0.2.3"}, // name convention
			{Name: "plain-1", Address: "10.0.2.11"}, // subnet heuristic → access
			{Name: "plain-0", Address: "10.0.0.51"}, // subnet heuristic → core band
			{Name: "edge-fw", Address: "10.0.9.9", Via: []string{"jumphost"}},
		},
		Hops: []config.NetDevHop{{Name: "jumphost", Host: "10.9.9.9"}},
	}

	g := InferTopology(cfg)
	tierByName := map[string]int{}
	subnetByName := map[string]string{}
	for _, n := range g.Nodes {
		tierByName[n.Name] = n.Tier
		subnetByName[n.Name] = n.Subnet
	}
	if tierByName["core-sw-1"] != 0 {
		t.Errorf("group word 核心 ignored: tier=%d", tierByName["core-sw-1"])
	}
	if tierByName["AGG-SW-9"] != 1 {
		t.Errorf("name convention AGG ignored: tier=%d", tierByName["AGG-SW-9"])
	}
	if tierByName["acc-sw-3"] != 2 {
		t.Errorf("name convention ACC ignored: tier=%d", tierByName["acc-sw-3"])
	}
	// Subnet ordering only as the fallback: 10.0.0.x ranks before 10.0.2.x.
	if tierByName["plain-0"] != 0 || tierByName["plain-1"] != 2 {
		t.Errorf("subnet heuristic wrong: plain-0=%d plain-1=%d", tierByName["plain-0"], tierByName["plain-1"])
	}
	if subnetByName["core-sw-1"] != "10.0.0.0/24" {
		t.Errorf("subnet annotation = %q", subnetByName["core-sw-1"])
	}

	// Links: exactly one — the configured bastion chain. core-sw-1 and
	// plain-0 share a /24 but MUST NOT get a fabricated edge.
	if len(g.Edges) != 1 || g.Edges[0].LocalDevice != "jumphost" || g.Edges[0].RemoteDevice != "edge-fw" || g.Edges[0].Source != "bastion" {
		t.Fatalf("edges = %+v, want exactly the bastion chain", g.Edges)
	}
}

// Empty inventory → empty graph, no panic (the tab renders the empty state).
func TestInferTopologyEmpty(t *testing.T) {
	cfg := config.Default()
	g := InferTopology(cfg)
	if len(g.Nodes) != 0 || len(g.Edges) != 0 {
		t.Fatalf("expected empty graph, got %+v", g)
	}
}

// Serialization contract: the bridge JSON-encodes these graphs, and the UI
// reads .length on nodes/edges — nil slices become JSON null and crashed the
// packaged app (mocks always sent arrays, so dev never caught it).
func TestInferTopologyNonNilCollections(t *testing.T) {
	g := InferTopology(config.Default())
	if g.Nodes == nil || g.Edges == nil {
		t.Fatalf("nil collection: nodes=%v edges=%v", g.Nodes == nil, g.Edges == nil)
	}
}
