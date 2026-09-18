package netdev

import (
	"context"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev/driver"
)

// Linux/Windows: the diagnosis' other half. Reads pass, writes refuse, and —
// the shell-specific hazard — metacharacters never reach the PTY.
func TestHostDriversClassify(t *testing.T) {
	lin, ok := driver.For("linux", "")
	if !ok || lin.Key() != "linux-shell" {
		t.Fatalf("linux resolve: %v", ok)
	}
	for _, c := range []string{"ip addr", "ss -tlnp", "systemctl status nginx", "journalctl -u ssh", "cat /proc/net/dev", "ping 10.0.0.1", "df -h", "docker ps"} {
		if got := lin.Classify(c); got != driver.Read {
			t.Errorf("linux read %q -> %v", c, got)
		}
	}
	for _, c := range []string{"systemctl restart nginx", "ip addr add 10.1.1.1/24 dev eth0", "echo pwned > /tmp/x", "useradd backdoor", "apt install nmap"} {
		if got := lin.Classify(c); got == driver.Read {
			t.Errorf("linux write %q classified READ", c)
		}
	}
	if got := lin.Classify("reboot"); got != driver.Dangerous {
		t.Errorf("reboot -> %v", got)
	}
	// /etc/shadow stays unreadable: only /proc, /sys, and two named /etc files.
	if got := lin.Classify("cat /etc/shadow"); got != driver.Unknown {
		t.Errorf("cat /etc/shadow -> %v, want Unknown (refused)", got)
	}

	win, ok := driver.For("windows", "")
	if !ok || win.Key() != "windows-powershell" {
		t.Fatalf("windows resolve: %v", ok)
	}
	for _, c := range []string{"Get-NetAdapter", "Get-NetTCPConnection", "systeminfo", "ipconfig", "tasklist", "test-netconnection 10.0.0.1 -Port 3389"} {
		if got := win.Classify(c); got != driver.Read {
			t.Errorf("windows read %q -> %v", c, got)
		}
	}
	for _, c := range []string{"Stop-Service Spooler", "New-NetIPAddress -IPAddress 10.9.9.9", "netsh interface ip set", "Remove-Item C:\\x"} {
		if got := win.Classify(c); got == driver.Read {
			t.Errorf("windows write %q classified READ", c)
		}
	}
	if got := win.Classify("shutdown /r /t 0"); got != driver.Dangerous {
		t.Errorf("shutdown -> %v", got)
	}
}

// The smuggling guard: a read-classified command chained with a pipe or `;`
// must refuse BEFORE dial on every shell driver (linux/esxi/windows).
func TestShellMetacharGuard(t *testing.T) {
	m, auditPath := testManager(t, startSimDevice(t))
	cfg := *m.cfg
	cfg.NetDev.Devices = append(cfg.NetDev.Devices, config.NetDevDevice{
		Name: "srv-1", Vendor: "linux", OS: "",
		Address: "127.0.0.1", Port: 1, Username: "root", PasswordEnv: "TEST_ENV",
	})
	m.cfg = &cfg

	for _, payload := range []string{
		"ps aux | sh -c reboot",
		"cat /proc/cpuinfo; reboot",
		"ping 8.8.8.8 > /tmp/x",
		"echo `reboot`",
		"ip addr $(reboot)",
	} {
		res := m.Exec(context.Background(), "srv-1", payload)
		if !res.Refused || res.Class != "guardrail" {
			t.Errorf("payload %q not refused by metachar guard: %+v", payload, res)
		}
		if !strings.Contains(res.Refusal, "metacharacters") && !strings.Contains(res.Refusal, "ONE plain command") {
			t.Errorf("payload %q refusal lacks guidance: %s", payload, res.Refusal)
		}
	}
	entries := readAudit(t, auditPath)
	guardRows := 0
	for _, e := range entries {
		if e.Class == "guardrail" {
			guardRows++
		}
	}
	if guardRows < 5 {
		t.Fatalf("guardrail audit rows = %d, want >= 5", guardRows)
	}
}

// SNMP: the OID allowlist is the seal — MIB-2 counters pass, enterprise trees
// and credential-bearing subtrees refuse BEFORE any UDP dial.
func TestSnmpOIDAllowlistAndRefusal(t *testing.T) {
	for _, oid := range []string{
		"1.3.6.1.2.1.1.3.0",      // sysUpTime
		"1.3.6.1.2.1.2.2.1.8",    // ifOperStatus column
		"1.3.6.1.2.1.4.22.1.2",   // ipNetToMediaTable (ARP)
		"1.3.6.1.2.1.31.1.1.1.6", // ifHCInOctets
		"1.3.6.1.2.1.25.3.3.1.2", // hrProcessorLoad
	} {
		if !snmpOIDAllowed(oid) {
			t.Errorf("OID %q should be allowed", oid)
		}
	}
	for _, oid := range []string{
		"1.3.6.1.4.1.9.9.109",   // CISCO-PROCESS-MIB (enterprise)
		"1.3.6.1.4.1.2011.2.23", // HUAWEI enterprise
		"1.3.6.1.3",             // experimental
		"",
		"1.3.6.1.2.1.9999",
	} {
		if snmpOIDAllowed(oid) {
			t.Errorf("OID %q must be refused", oid)
		}
	}

	m, auditPath := testManager(t, startSimDevice(t))
	cfg := *m.cfg
	cfg.NetDev.Devices = append(cfg.NetDev.Devices, config.NetDevDevice{
		Name: "sw-snmp", Vendor: "snmp", OS: "",
		Address: "127.0.0.1", Port: 1161, Username: "", PasswordEnv: "TEST_ENV",
	})
	m.cfg = &cfg

	_, err := m.SnmpQuery(context.Background(), "sw-snmp", "1.3.6.1.4.1.9.9.109.1.1.1.1.7", "get")
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("enterprise OID accepted: %v", err)
	}
	found := false
	for _, e := range readAudit(t, auditPath) {
		if e.Device == "sw-snmp" && e.Class == "guardrail" {
			found = true
		}
	}
	if !found {
		t.Fatal("refused OID left no guardrail audit row")
	}
}
