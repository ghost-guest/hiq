package netdev

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/zzycxz/hiq/internal/netdev/driver"
)

// snmp.go — the metrics channel: SNMP v2c GET / bounded WALK against network
// devices. vendor=snmp entries carry the target address and a community read
// from the secret store (passwordEnv); the OID allowlist is the seal —
// standard MIB-2 only (interfaces, IP/TCP/UDP tables, host resources). Vendor
// enterprise trees stay refused until a user extends them (same doctrine as
// extra_read): the tree below is what we can vouch is pure counters.
//
// SNMP is UDP with no confidentiality — v2c communities cross the wire in the
// clear, which is exactly why reads here are counter-class OIDs and the
// community lives in the secret store, never in config or context.

// snmpAllowedOIDPrefixes: read-only standard MIB-2 subtrees.
var snmpAllowedOIDPrefixes = []string{
	"1.3.6.1.2.1.1",   // system: description, uptime, contact, location
	"1.3.6.1.2.1.2",   // interfaces table: admin/oper status, counters
	"1.3.6.1.2.1.3",   // atTable: ARP-ish address translation
	"1.3.6.1.2.1.4",   // ip: addr table, routing table, counters, ICMP stats
	"1.3.6.1.2.1.5",   // icmp counters
	"1.3.6.1.2.1.6",   // tcp: conn table, counters
	"1.3.6.1.2.1.7",   // udp: listeners, counters
	"1.3.6.1.2.1.8",   // egp
	"1.3.6.1.2.1.9",   // transmission
	"1.3.6.1.2.1.10",  // snmp metrics of data links (dot3 stats etc.)
	"1.3.6.1.2.1.25",  // host resources (hrStorage/hrProcessor/hrSWRun)
	"1.3.6.1.2.1.31",  // ifMIB: high-capacity counters, ifName/ifAlias
	"1.3.6.1.2.1.105", // ifMIB extensions where vendors publish them
}

// snmpOIDAllowed reports whether an OID (numeric, dotted) sits inside an
// allowed subtree. Pure function — unit-tested both ways.
func snmpOIDAllowed(oid string) bool {
	oid = strings.TrimSuffix(strings.TrimSpace(oid), ".")
	if oid == "" {
		return false
	}
	for _, p := range snmpAllowedOIDPrefixes {
		if oid == p || strings.HasPrefix(oid, p+".") {
			return true
		}
	}
	return false
}

// snmpMaxVars bounds one WALK's variable bindings — a runaway agent must not
// turn a walk into a table dump of the whole device.
const snmpMaxVars = 120

// SnmpQuery runs ONE GET (oid exact) or a bounded WALK (prefix) against a
// vendor=snmp device. Results render as `OID = value` lines, redacted.
func (m *Manager) SnmpQuery(ctx context.Context, deviceName, oid, mode string) (string, error) {
	oid = strings.TrimSpace(oid)
	if mode == "" {
		mode = "get"
	}
	label := "SNMP " + strings.ToUpper(mode) + " " + oid
	if strings.ContainsAny(oid, " \t\r\n;|&$`()<>") {
		return "", fmt.Errorf("snmp: malformed OID")
	}
	if r, allow := m.guardrailCheck(deviceName, label); !allow {
		m.liveCmdRefused(deviceName, label, "guardrail", r.Refusal)
		b, _ := json.Marshal(r.Refusal)
		return string(b), nil
	}
	device, ok := m.cfg.NetDevDeviceByName(deviceName)
	if !ok {
		return "", fmt.Errorf("snmp: device %q is not in the inventory", deviceName)
	}
	if !strings.EqualFold(device.Vendor, "snmp") {
		return "", fmt.Errorf("snmp: device %q is vendor %q — only snmp devices take this path", deviceName, device.Vendor)
	}
	if !snmpOIDAllowed(oid) {
		_ = AppendAudit(Audit{Device: deviceName, Command: label, Class: "guardrail", Status: AuditRefused, OutputBytes: 0})
		m.liveCmdRefused(deviceName, label, "guardrail", fmt.Sprintf("OID %q is outside the read-only MIB-2 allowlist", oid))
		return "", fmt.Errorf("snmp: OID %q is outside the read-only MIB-2 allowlist (system/interfaces/ip/icmp/tcp/udp/host-resources/ifMIB)", oid)
	}
	// Live lifecycle: SNMP is a UDP one-shot — start/end only, no stream.
	start := m.liveCmdStart(deviceName, label, "read")
	status := "failure"
	defer func() { m.liveCmdEnd(deviceName, label, "read", status, start, 0, "") }()
	community := "public"
	if device.PasswordEnv != "" {
		if v, ok, _ := secretGetter(SecretKindPassword, device.PasswordEnv); ok && v != "" {
			community = v
		}
	}
	port := device.Port
	if port == 0 {
		port = 161
	}

	g := &gosnmp.GoSNMP{
		Target:    device.Address,
		Port:      uint16(port),
		Community: community,
		Version:   gosnmp.Version2c,
		Timeout:   4 * time.Second,
		Retries:   1,
		MaxOids:   gosnmp.MaxOids,
		Context:   ctx,
	}
	if err := g.Connect(); err != nil {
		m.audit(device, label, driver.Read, AuditFailure, 0, err)
		return "", fmt.Errorf("snmp: %w", err)
	}
	defer g.Conn.Close()

	var lines []string
	switch mode {
	case "get":
		res, err := g.Get([]string{oid})
		if err != nil {
			m.audit(device, label, driver.Read, AuditFailure, 0, err)
			return "", fmt.Errorf("snmp: %w", err)
		}
		for _, v := range res.Variables {
			lines = append(lines, formatSnmpVar(v))
		}
	case "walk":
		err := g.Walk(oid, func(v gosnmp.SnmpPDU) error {
			if len(lines) >= snmpMaxVars {
				lines = append(lines, fmt.Sprintf("…（已达单次上限 %d 个变量，继续请缩小 OID 范围）", snmpMaxVars))
				return fmt.Errorf("bounded")
			}
			lines = append(lines, formatSnmpVar(v))
			return nil
		})
		if err != nil && len(lines) == 0 {
			m.audit(device, label, driver.Read, AuditFailure, 0, err)
			return "", fmt.Errorf("snmp: %w", err)
		}
	default:
		return "", fmt.Errorf("snmp: mode must be get or walk")
	}

	m.turnSpend()
	out := strings.Join(lines, "\n")
	out2, n := RedactCounted(out)
	m.audit(device, label, driver.Read, AuditOK, len(out2), nil)
	status = AuditOK
	if n > 0 {
		out2 += fmt.Sprintf("\n\n[安全提醒] 输出中 %d 处敏感字段已脱敏后才进入上下文与审计。", n)
	}
	return out2, nil
}

func formatSnmpVar(v gosnmp.SnmpPDU) string {
	val := ""
	switch v.Type {
	case gosnmp.OctetString:
		val = string(v.Value.([]byte))
	default:
		val = fmt.Sprintf("%v", v.Value)
	}
	return fmt.Sprintf("%s = %s", v.Name, val)
}
