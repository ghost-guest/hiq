package netdev

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

func TestRuleCmpAndMetrics(t *testing.T) {
	if !ruleCmp(0, 0, "==") || !ruleCmp(3, 1, ">=") || !ruleCmp(0, 1, "<=") {
		t.Error("basic comparisons broken")
	}
	if ruleCmp(1, 0, "==") || ruleCmp(0, 1, ">=") || ruleCmp(2, 1, "<=") {
		t.Error("comparisons must not fire")
	}
	h := DeviceHealth{Reachable: true, Interfaces: []IfHealth{
		{Name: "up", AdminUp: true, OperUp: true},
		{Name: "down", AdminUp: true, OperUp: false},
		{Name: "shut", AdminUp: false, OperUp: false},
	}}
	if ruleMetricValue("reachable", h, 0) != 1 {
		t.Error("reachable should be 1")
	}
	if got := ruleMetricValue("if_down_count", h, 0); got != 1 {
		t.Errorf("if_down_count should be 1 (admin-up oper-down only), got %d", got)
	}
	// uptime_reset: fire only on a drop with a baseline.
	if ruleMetricValue("uptime_reset", DeviceHealth{Reachable: true, UptimeSec: 100}, 0) != 0 {
		t.Error("no baseline → never fire")
	}
	if ruleMetricValue("uptime_reset", DeviceHealth{Reachable: true, UptimeSec: 100}, 50000) != 1 {
		t.Error("uptime drop → reboot detected")
	}
	if ruleMetricValue("uptime_reset", DeviceHealth{Reachable: true, UptimeSec: 60000}, 50000) != 0 {
		t.Error("uptime growth → no reboot")
	}
}

func TestSyslogEscalateDedup(t *testing.T) {
	dir := t.TempDir()
	old := findingsDirOverr
	findingsDirOverr = dir
	defer func() { findingsDirOverr = old }()
	syslogMu.Lock()
	syslogLastFire = map[string]time.Time{}
	syslogMu.Unlock()

	dev := config.NetDevDevice{Name: "sw1", Vendor: "huawei", Address: "10.0.0.1"}
	_ = dev
	syslogEscalate("sw1", "%LINK-3-UPDOWN: Interface Gi0/0/1, changed state to down")
	fs, _ := ListFindings()
	if len(fs) != 1 || fs[0].Source != "syslog:sw1:link-flap" || fs[0].Status != "active" {
		t.Fatalf("expected one active link-flap finding, got %+v", fs)
	}
	// Same class within the throttle window: no second finding.
	syslogEscalate("sw1", "another line protocol down message")
	fs, _ = ListFindings()
	if len(fs) != 1 {
		t.Fatalf("dedup failed: %d findings", len(fs))
	}
	// A clean line escalates nothing.
	syslogEscalate("sw1", "interface counters polled")
	fs, _ = ListFindings()
	if len(fs) != 1 {
		t.Fatalf("clean line should not escalate: %d", len(fs))
	}
}

func TestAuditChain(t *testing.T) {
	dir := t.TempDir()
	old := auditPath
	SetAuditPath(filepath.Join(dir, "audit.jsonl"))
	auditLastHash = ""
	defer func() {
		SetAuditPath(old)
		auditLastHash = ""
	}()

	for i := 0; i < 3; i++ {
		if err := AppendAudit(Audit{Device: "sw1", Command: "display version", Class: "read", Status: AuditOK}); err != nil {
			t.Fatal(err)
		}
	}
	st := VerifyAuditChain()
	if !st.OK || st.Chained != 3 || st.Total != 3 {
		t.Fatalf("chain should verify: %+v", st)
	}

	// Tamper with the middle line → chain must break.
	path := filepath.Join(dir, "audit.jsonl")
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	lines[1] = strings.Replace(lines[1], "display version", "undo stp enable", 1)
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	auditLastHash = ""
	st = VerifyAuditChain()
	if st.OK || st.FirstBroken == "" {
		t.Fatalf("tampering must break the chain: %+v", st)
	}
}
