package netdev

import (
	"path/filepath"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

func discoverTestManager(t *testing.T, scopes ...string) *Manager {
	t.Helper()
	cfg := config.Default()
	cfg.NetDev = config.NetDevConfig{
		Enabled:   true,
		Discovery: config.NetDevDiscovery{Scopes: scopes, Rate: 8},
	}
	m := NewManager(cfg)
	t.Cleanup(m.Close)
	return m
}

func TestDiscoverTCPFindsOpenPort(t *testing.T) {
	sim := startSimDevice(t) // an SSH server on 127.0.0.1
	host, portStr, _ := net.SplitHostPort(sim.addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	m := discoverTestManager(t, "127.0.0.0/8")
	// Isolate the 待确认区 store: the cache-TTL filter would otherwise skip
	// hosts recorded by a previous run in the real state dir.
	oldDir := discoveredDirOverr
	discoveredDirOverr = filepath.Join(t.TempDir(), "discovered")
	t.Cleanup(func() { discoveredDirOverr = oldDir })
	res, err := m.DiscoverTCP(context.Background(), "", host+"/32", []int{port, port + 1})
	if err != nil {
		t.Fatalf("DiscoverTCP: %v", err)
	}
	if len(res) != 1 || res[0].IP != host {
		t.Fatalf("results = %+v, want the sim host only", res)
	}
	if len(res[0].Ports) != 1 || res[0].Ports[0].Port != port || !res[0].Ports[0].Open {
		t.Fatalf("ports = %+v", res[0].Ports)
	}
	if !strings.HasPrefix(res[0].Ports[0].Banner, "SSH-") {
		t.Fatalf("banner = %q, want SSH-", res[0].Ports[0].Banner)
	}
}

// The scope whitelist is enforced at the dial boundary: a CIDR outside the
// configured scopes is refused outright, no packets sent.
func TestDiscoverTCPRefusesOutsideScopes(t *testing.T) {
	m := discoverTestManager(t, "10.30.0.0/16")
	_, err := m.DiscoverTCP(context.Background(), "", "127.0.0.1/32", []int{22})
	if err == nil || !strings.Contains(err.Error(), "outside the configured discovery scopes") {
		t.Fatalf("err = %v, want scope refusal", err)
	}
}

func TestDiscoverTCPRefusesUnconfiguredHop(t *testing.T) {
	m := discoverTestManager(t, "127.0.0.0/8")
	_, err := m.DiscoverTCP(context.Background(), "ghost-hop", "127.0.0.1/32", []int{22})
	if err == nil || !strings.Contains(err.Error(), "human-registered") {
		t.Fatalf("err = %v, want hop refusal", err)
	}
}

func TestDiscoverTCPCidrTooLarge(t *testing.T) {
	m := discoverTestManager(t, "10.0.0.0/8")
	_, err := m.DiscoverTCP(context.Background(), "", "10.20.0.0/16", []int{22})
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want size refusal", err)
	}
}

func TestExpandCIDRIPv4(t *testing.T) {
	ip, ipNet, _ := net.ParseCIDR("192.168.1.0/30")
	hosts, err := expandCIDR(ip, ipNet)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || hosts[0] != "192.168.1.1" || hosts[1] != "192.168.1.2" {
		t.Fatalf("hosts = %v", hosts)
	}
}
