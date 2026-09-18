package netdev

import (
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// Feed round-trip: import → match → findings (hit and clean paths).
func TestCVEFeedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	old := netdevStateDirOverr
	oldF := findingsDirOverr
	defer func() { netdevStateDirOverr = old; findingsDirOverr = oldF }()
	netdevStateDirOverr = dir
	findingsDirOverr = dir

	feed := `{"cves":[
		{"id":"CVE-2026-0001","desc":"IOS XE Web UI RCE","products":["cisco iosxe","cisco ios"],"severity":"critical"},
		{"id":"CVE-2026-0002","desc":"VRP buffer overflow","products":["huawei vrp"],"severity":"high"}
	]}`
	n, err := ImportCVEFeed(feed)
	if err != nil || n != 2 {
		t.Fatalf("import: %v n=%d", err, n)
	}
	cfg := &config.Config{}
	cfg.NetDev.Devices = []config.NetDevDevice{
		{Name: "core-sw-1", Vendor: "cisco", OS: "iosxe", Model: "9300"},
		{Name: "gw-1", Vendor: "huawei", OS: "vrp8", Model: "AR6300"},
		{Name: "vm-1", Vendor: "linux"},
	}
	m := NewManager(cfg)
	matches, err := m.MatchCVEs()
	if err != nil {
		t.Fatal(err)
	}
	// cisco hits both cve-0001 (iosxe) ; huawei hits cve-0002 ; linux none
	if len(matches) != 2 {
		t.Fatalf("matches: %+v", matches)
	}
	f, err := m.MatchCVEsToFindings()
	if err != nil || !strings.Contains(f.Title, "2 台命中") {
		t.Fatalf("finding: %v %+v", err, f)
	}
	if !strings.Contains(f.Detail, "CVE-2026-0001") || !strings.Contains(f.Detail, "CVE-2026-0002") {
		t.Fatalf("detail: %s", f.Detail)
	}
	// bad feed refuses
	if _, err := ImportCVEFeed(`{"cves":[]}`); err == nil {
		t.Fatal("empty feed must refuse")
	}
}
