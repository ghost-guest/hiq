package netdev

// syslogrecv.go — the passive syslog receiver (P2): devices point their
// syslog at this UDP port; lines aggregate into per-device ring buffers the
// 日志 tab reads (source kind "syslog"), and known-bad patterns auto-escalate
// to Findings (throttled: one per device+class per 10 minutes). The receiver
// never executes anything — it is a pure observer.

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

func compileRegexpSafe(s string) (*regexp.Regexp, error) { return regexp.Compile(s) }

const syslogRingCap = 1000

type syslogLine struct {
	Time time.Time
	Text string // full raw line (RFC3164-ish)
}

var (
	syslogMu    sync.Mutex
	syslogRings = map[string][]syslogLine{}
	syslogConn  net.PacketConn
	// syslogCfg is the receiver's CURRENT view of the inventory, refreshed by
	// every EnsureSyslogReceiver call — device-name matching must not ride on
	// a config snapshot captured at listener startup.
	syslogCfg      *config.Config
	syslogLastFire = map[string]time.Time{} // device:class → last auto-Finding

	syslogCountFlusherOnce sync.Once
)

// syslogClasses: known-bad patterns → finding class. Deliberately short and
// network-ops focused; each is a case-insensitive substring.
var syslogClasses = []struct {
	class, pattern, severity string
}{
	{"link-flap", "link down", SeverityWarning},
	{"link-flap", "line protocol", SeverityWarning},
	{"link-flap", "state to down", SeverityWarning}, // Cisco %LINK-3-UPDOWN format
	{"link-flap", "changed state to up", SeverityInfo},
	{"ospf-adjacency", "adjacency", SeverityWarning},
	{"ospf-adjacency", "ospf", SeverityInfo},
	{"auth-failure", "authentication fail", SeverityWarning},
	{"auth-failure", "login failure", SeverityWarning},
	{"hardware-error", "hardware error", SeverityCritical},
	{"power-problem", "power supply", SeverityCritical},
}

// EnsureSyslogReceiver starts (or stops on port 0 / config change) the UDP
// listener. Idempotent; called from the desktop bridge on settings load/save.
// Every call also refreshes the receiver's view of the inventory, so devices
// added after startup still map by address.
func EnsureSyslogReceiver(cfg *config.Config) {
	port := 0
	if cfg != nil {
		port = cfg.NetDev.Syslog.Port
	}
	syslogMu.Lock()
	syslogCfg = cfg
	current := syslogConn
	syslogMu.Unlock()
	if port <= 0 {
		if current != nil {
			_ = current.Close()
		}
		return
	}
	if current != nil {
		return // already listening; a port change needs the app restart note in UI
	}
	pc, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return // bind failure: receiver stays off; surfaced via SyslogReceiverStatus
	}
	syslogMu.Lock()
	syslogConn = pc
	syslogMu.Unlock()
	go syslogLoop(pc)
	syslogCountFlusherOnce.Do(func() { go syslogCountFlusher() })
}

// syslogCountFlusher drains the R3 count buckets to the day files every 30s
// (the receiver itself never blocks on disk).
func syslogCountFlusher() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		FlushSyslogCounts()
	}
}

// syslogClassify returns the first matching rule class for the line, or
// "other" (R3 buckets).
func syslogClassify(line string) string {
	lower := strings.ToLower(line)
	for _, c := range syslogClasses {
		if strings.Contains(lower, c.pattern) {
			return c.class
		}
	}
	return "other"
}

// SyslogReceiverStatus reports the listener state for the UI.
func SyslogReceiverStatus() (listening bool, port int, buffered int) {
	syslogMu.Lock()
	defer syslogMu.Unlock()
	if c := syslogConn; c != nil {
		if _, ok := c.LocalAddr().(*net.UDPAddr); ok {
			port = c.LocalAddr().(*net.UDPAddr).Port
		}
		listening = true
	}
	for _, r := range syslogRings {
		buffered += len(r)
	}
	return
}

// SyslogStatusView is the bridge payload for the UI.
type SyslogStatusView struct {
	Listening bool `json:"listening"`
	Port      int  `json:"port"`
	Buffered  int  `json:"buffered"`
}

func syslogLoop(pc net.PacketConn) {
	buf := make([]byte, 8192)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		if n <= 0 {
			continue
		}
		line := strings.TrimRight(string(buf[:n]), "\r\n")
		if line == "" {
			continue
		}
		syslogMu.Lock()
		cfg := syslogCfg
		syslogMu.Unlock()
		device := syslogDeviceFor(addr, cfg)
		now := time.Now()
		syslogMu.Lock()
		ring := append(syslogRings[device], syslogLine{Time: now, Text: line})
		if len(ring) > syslogRingCap {
			ring = ring[len(ring)-syslogRingCap:]
		}
		syslogRings[device] = ring
		syslogMu.Unlock()
		syslogEscalate(device, line)
		syslogCountIncr(now, device, syslogClassify(line)) // R3（含 "(unknown)" 桶）
	}
}

// syslogDeviceFor maps the sender address to an inventory device name (by
// address match); unknown senders aggregate under "(unknown)".
func syslogDeviceFor(addr net.Addr, cfg *config.Config) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	if cfg != nil {
		for _, d := range cfg.NetDev.Devices {
			if strings.EqualFold(d.Address, host) {
				return d.Name
			}
		}
	}
	return "(unknown)"
}

// SyslogTail reads a device's ring buffer (newest last), optional grep filter
// (regex, literal fallback), bounded to tailN.
func SyslogTail(device string, tailN int, grep string) []string {
	if tailN <= 0 {
		tailN = 100
	}
	if tailN > 1000 {
		tailN = 1000
	}
	syslogMu.Lock()
	ring := append([]syslogLine(nil), syslogRings[device]...)
	syslogMu.Unlock()
	out := make([]string, 0, len(ring))
	for _, l := range ring {
		if grep != "" && !strings.Contains(l.Text, grep) && !grepLineMatch(grep, l.Text) {
			continue
		}
		out = append(out, fmt.Sprintf("%s %s", l.Time.Format("15:04:05"), l.Text))
	}
	if len(out) > tailN {
		out = out[len(out)-tailN:]
	}
	return out
}

// SyslogEventsSince returns one device's ring entries (structured) since t.
func SyslogEventsSince(device string, since time.Time) []NetDevEvent {
	syslogMu.Lock()
	ring := append([]syslogLine(nil), syslogRings[device]...)
	syslogMu.Unlock()
	var out []NetDevEvent
	for _, l := range ring {
		if l.Time.After(since) {
			out = append(out, NetDevEvent{Time: l.Time, Text: l.Text})
		}
	}
	return out
}

// syslogEscalate: known-bad patterns → one Finding per device+class per
// throttle window.
func syslogEscalate(device, line string) {
	if device == "(unknown)" {
		return
	}
	lower := strings.ToLower(line)
	for _, c := range syslogClasses {
		if !strings.Contains(lower, c.pattern) {
			continue
		}
		key := device + ":" + c.class
		syslogMu.Lock()
		last, seen := syslogLastFire[key]
		syslogLastFire[key] = time.Now()
		syslogMu.Unlock()
		if seen && time.Since(last) < 10*time.Minute {
			return
		}
		// 误报学习（§4.10）：此键被标记误报 ≥2 次后自动降级为 info。
		key0 := device + ":" + c.class
		sev, degraded := suppressedSeverity("syslog:"+key0, c.severity)
		now := time.Now()
		f := &Finding{
			Title:    fmt.Sprintf("[syslog] %s @ %s", c.class, device),
			Severity: sev,
			Devices:  []string{device},
			Detail:   fmt.Sprintf("被动 syslog 命中模式 %q（class %s，10 分钟去重）。", c.pattern, c.class),
			Evidence: []Evidence{{Device: device, Command: "syslog (被动接收)", Output: truncateRunes(line, 500)}},
			Source:   "syslog:" + key,
			Status:   "active",
			Suggestion: func() string {
				if degraded {
					return "此前被标记误报 ≥2 次，已自动降级为 info（误报学习）。"
				}
				return ""
			}(),
		}
		f.CreatedAt = now
		_ = SaveFinding(f)
		return
	}
}

func grepLineMatch(grep, line string) bool {
	if re, err := compileRegexpSafe(grep); err == nil && re != nil {
		return re.MatchString(line)
	}
	return false
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
