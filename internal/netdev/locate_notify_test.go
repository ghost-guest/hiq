package netdev

import (
	"net/http"

	"github.com/zzycxz/hiq/internal/config"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMatchLocateLinesAndIface(t *testing.T) {
	lines := []string{
		"10.0.0.5  aa-bb-cc-dd-ee-ff  Vlan10  Dynamic",
		"10.0.0.6  11-22-33-44-55-66  GE1/0/5  Dynamic",
		"Internet  10.0.0.7  5   arpa  aabb.ccdd.eeff  Vlan20",
		"10.0.0.8 dev eth0 lladdr aa:bb:cc:00:00:01 REACHABLE",
	}
	hits := matchLocateLines("sw-1", lines, "10.0.0.5")
	if len(hits) != 1 || hits[0].Interface != "Vlan10" {
		t.Fatalf("ip match: %+v", hits)
	}
	hits = matchLocateLines("sw-1", lines, "AA:BB:CC:00:00:01")
	if len(hits) != 1 || hits[0].Interface != "eth0" {
		t.Fatalf("mac case-insensitive + dev iface: %+v", hits)
	}
	if hits = matchLocateLines("sw-1", lines, "10.9.9.9"); len(hits) != 0 {
		t.Fatalf("no match expected, got %+v", hits)
	}
}

func TestNotifyWebhookFiresOnSeverity(t *testing.T) {
	_ = config.Config{}
	var got atomic.Int64
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 4096)
		n, _ := r.Body.Read(b)
		body.Store(string(b[:n]))
		got.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	cfg := &config.Config{}
	cfg.NetDev.NotifyWebhook = srv.URL
	cfg.NetDev.NotifyMinSeverity = "warning"
	EnsureNotifier(cfg)

	_ = SaveFinding(&Finding{Title: "notify-test-warn", Severity: "warning", Devices: []string{"x"}, Detail: "d", Source: "notify-test", Evidence: []Evidence{{Device: "x", Command: "test", Output: "o"}}})
	_ = SaveFinding(&Finding{Title: "notify-test-info", Severity: "info", Devices: []string{"x"}, Detail: "d", Source: "notify-test-info", Evidence: []Evidence{{Device: "x", Command: "test", Output: "o"}}})
	deadline := time.Now().Add(2 * time.Second)
	for got.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got.Load() != 1 {
		t.Fatalf("expected exactly 1 POST (warning fires, info filtered), got %d", got.Load())
	}
	if b, _ := body.Load().(string); !strings.Contains(b, "hiq://finding/") || !strings.Contains(b, "notify-test-warn") {
		t.Fatalf("payload missing deep link/title: %s", b)
	}
	EnsureNotifier(nil) // off
}
