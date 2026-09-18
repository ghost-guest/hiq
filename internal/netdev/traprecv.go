package netdev

// traprecv.go — the passive SNMP trap receiver (v2c), the syslog receiver's
// sibling (NETDEV_SPEC_V2 §5.1). Traps land in per-device rings; linkDown /
// coldStart escalate to Findings with the same 10-minute per-device+class
// throttle. Pure observer — nothing here ever executes.

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/zzycxz/hiq/internal/config"
)

const trapRingCap = 500

var (
	trapMu       sync.Mutex
	trapRings    = map[string][]syslogLine{} // same shape as syslog rings
	trapListener *gosnmp.TrapListener
	trapPortLive int
	trapLastFire = map[string]time.Time{}
)

// trapOIDs: standard v2c notification OIDs (RFC 3418) → class.
var trapOIDs = map[string]struct {
	class, severity string
}{
	"1.3.6.1.6.3.1.1.5.1": {"cold-start", SeverityWarning},
	"1.3.6.1.6.3.1.1.5.2": {"warm-start", SeverityInfo},
	"1.3.6.1.6.3.1.1.5.3": {"link-down", SeverityWarning},
	"1.3.6.1.6.3.1.1.5.4": {"link-up", SeverityInfo},
}

// EnsureTrapReceiver starts (port > 0) or stops (port 0) the trap listener.
// Idempotent; called from the desktop bridge with every settings load.
func EnsureTrapReceiver(cfg *config.Config) {
	port := 0
	if cfg != nil {
		port = cfg.NetDev.Trap.Port
	}
	trapMu.Lock()
	cur, curPort := trapListener, trapPortLive
	trapMu.Unlock()
	if port <= 0 {
		if cur != nil {
			cur.Close()
			trapMu.Lock()
			trapListener, trapPortLive = nil, 0
			trapMu.Unlock()
		}
		return
	}
	if cur != nil {
		if curPort != port {
			cur.Close()
			trapMu.Lock()
			trapListener, trapPortLive = nil, 0
			trapMu.Unlock()
		} else {
			return
		}
	}
	l := gosnmp.NewTrapListener()
	l.Params = gosnmp.Default
	l.Params.Community = "public" // v2c community check is per-trap below
	l.OnNewTrap = func(p *gosnmp.SnmpPacket, addr *net.UDPAddr) {
		trapHandle(p, addr, cfg)
	}
	go func() {
		_ = l.Listen(fmt.Sprintf(":%d", port))
	}()
	trapMu.Lock()
	trapListener, trapPortLive = l, port
	trapMu.Unlock()
}

// TrapReceiverStatus reports the listener state for the UI.
func TrapReceiverStatus() (listening bool, port int, buffered int) {
	trapMu.Lock()
	defer trapMu.Unlock()
	if trapListener != nil {
		listening, port = true, trapPortLive
	}
	for _, r := range trapRings {
		buffered += len(r)
	}
	return
}

// trapHandle ingests one trap packet: ring line + escalation.
func trapHandle(p *gosnmp.SnmpPacket, addr *net.UDPAddr, cfg *config.Config) {
	if p.Version != gosnmp.Version2c && p.Version != gosnmp.Version1 {
		return
	}
	device := "(unknown)"
	if cfg != nil && addr != nil {
		for _, d := range cfg.NetDev.Devices {
			if strings.EqualFold(d.Address, addr.IP.String()) {
				device = d.Name
				break
			}
		}
	}
	// v2c: the notification OID rides the SnmpTrapOID var (1.3.6.1.6.3.1.1.4.1.0).
	trapOID := ""
	var parts []string
	for _, v := range p.Variables {
		parts = append(parts, fmt.Sprintf("%s=%v", v.Name, v.Value))
		if v.Name == "1.3.6.1.6.3.1.1.4.1.0" {
			if s, ok := v.Value.(string); ok {
				trapOID = s
			}
		}
	}
	// v1: generic trap numbers (0 coldStart, 2 linkDown, 3 linkUp).
	if p.Version == gosnmp.Version1 {
		switch p.GenericTrap {
		case 0:
			trapOID = "1.3.6.1.6.3.1.1.5.1"
		case 2:
			trapOID = "1.3.6.1.6.3.1.1.5.3"
		case 3:
			trapOID = "1.3.6.1.6.3.1.1.5.4"
		}
	}
	label := trapOID
	if c, ok := trapOIDs[trapOID]; ok {
		label = c.class
	}
	text := fmt.Sprintf("trap %s %s", label, strings.Join(parts, " "))
	now := time.Now()
	trapMu.Lock()
	ring := append(trapRings[device], syslogLine{Time: now, Text: text})
	if len(ring) > trapRingCap {
		ring = ring[len(ring)-trapRingCap:]
	}
	trapRings[device] = ring
	trapMu.Unlock()
	trapEscalate(device, trapOID, text)
}

// trapEscalate: link-down / cold-start → one Finding per device+class per 10min.
func trapEscalate(device, oid, text string) {
	if device == "(unknown)" {
		return
	}
	c, ok := trapOIDs[oid]
	if !ok {
		return
	}
	key := device + ":trap-" + c.class
	trapMu.Lock()
	last, seen := trapLastFire[key]
	trapLastFire[key] = time.Now()
	trapMu.Unlock()
	if seen && time.Since(last) < 10*time.Minute {
		return
	}
	sev, degraded := suppressedSeverity("trap:"+key, c.severity)
	_ = SaveFinding(&Finding{
		Title:    fmt.Sprintf("[trap] %s @ %s", c.class, device),
		Severity: sev,
		Devices:  []string{device},
		Detail:   fmt.Sprintf("被动 SNMP trap 命中 %s（10 分钟去重）。", oid),
		Evidence: []Evidence{{Device: device, Command: "snmptrap (被动接收)", Output: truncateRunes(text, 500)}},
		Source:   "trap:" + key,
		Status:   "active",
	})
	_ = degraded
}

// TrapEventsSince returns one device's trap ring entries since t.
func TrapEventsSince(device string, since time.Time) []NetDevEvent {
	trapMu.Lock()
	ring := append([]syslogLine(nil), trapRings[device]...)
	trapMu.Unlock()
	var out []NetDevEvent
	for _, l := range ring {
		if l.Time.After(since) {
			out = append(out, NetDevEvent{Time: l.Time, Text: l.Text})
		}
	}
	return out
}

// TrapTail reads a device's trap ring (newest last).
func TrapTail(device string, tailN int) []string {
	if tailN <= 0 || tailN > trapRingCap {
		tailN = 100
	}
	trapMu.Lock()
	ring := append([]syslogLine(nil), trapRings[device]...)
	trapMu.Unlock()
	out := make([]string, 0, len(ring))
	for _, l := range ring {
		out = append(out, fmt.Sprintf("%s %s", l.Time.Format("15:04:05"), l.Text))
	}
	if len(out) > tailN {
		out = out[len(out)-tailN:]
	}
	return out
}
