package netdev

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/gosnmp/gosnmp"

	"github.com/zzycxz/hiq/internal/config"
)

// linkDown v2c trap → ring line + exactly ONE new Finding (throttled replay
// adds none). Idempotent against findings left by earlier runs.
func TestTrapHandleLinkDownEscalates(t *testing.T) {
	cfg := &config.Config{}
	cfg.NetDev.Devices = []config.NetDevDevice{{Name: "sw-1", Vendor: "huawei", Address: "10.0.0.9"}}
	countTrap := func() int {
		n := 0
		if fs, err := ListFindings(); err == nil {
			for _, f := range fs {
				if strings.HasPrefix(f.Source, "trap:sw-1:trap-link-down") {
					n++
				}
			}
		}
		return n
	}
	pkt := &gosnmp.SnmpPacket{
		Version: gosnmp.Version2c,
		Variables: []gosnmp.SnmpPDU{
			{Name: "1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: "1.3.6.1.6.3.1.1.5.3"},
		},
	}
	base := countTrap()
	trapHandle(pkt, &net.UDPAddr{IP: net.ParseIP("10.0.0.9")}, cfg)
	tail := TrapTail("sw-1", 10)
	if len(tail) == 0 || !strings.Contains(tail[len(tail)-1], "link-down") {
		t.Fatalf("ring: %+v", tail)
	}
	if n := countTrap() - base; n != 1 {
		t.Fatalf("expected exactly 1 new trap finding, got %d", n)
	}
	trapHandle(pkt, &net.UDPAddr{IP: net.ParseIP("10.0.0.9")}, cfg) // 10 分钟去重
	if n := countTrap() - base; n != 1 {
		t.Fatalf("throttle violated: %d findings after replay", n)
	}
}

// The tunnel: no Via = passthrough; unknown hop = clean refusal before any
// socket opens.
func TestDbTunnelViaGuards(t *testing.T) {
	cfg := &config.Config{}
	m := NewManager(cfg)
	src := config.NetDevDBSource{Name: "db", Type: "mysql", Host: "10.1.0.5", Port: 3306}
	t1, closer, err := m.dbTunnel(context.Background(), src)
	closer()
	if err != nil || t1.Host != "10.1.0.5" || t1.Port != 3306 {
		t.Fatalf("no-Via must pass through unchanged: %+v %v", t1, err)
	}
	bad := src
	bad.Via = []string{"no-such-hop"}
	if _, closer, err := m.dbTunnel(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "no-such-hop") {
		closer()
		t.Fatalf("unknown hop must refuse: %v", err)
	} else {
		closer()
	}
}
