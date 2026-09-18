package netdev

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/netdev/transport"
)

// Discovery (tunnel mode): TCP reachability probing through the SSH chain —
// each probe opens a direct-tcpip channel on the hop's connection (or dials
// direct when via is empty), so the SYN comes from the hop's network position.
// UDP/ICMP and /16-and-up sweeps need the netprobe binary (P3 later phase);
// the tunnel mode covers targeted /24-class checks.

// DiscoverPortProbe is one port's outcome on one host.
type DiscoverPortProbe struct {
	Port   int    `json:"port"`
	Open   bool   `json:"open"`
	Banner string `json:"banner,omitempty"` // first bytes, e.g. "SSH-2.0-..."
}

// DiscoverHostResult is one live host.
type DiscoverHostResult struct {
	IP    string              `json:"ip"`
	Ports []DiscoverPortProbe `json:"ports"`
}

// discoverLimits bound one scan.
const (
	discoverMaxHosts       = 4096 // refuse beyond /20
	discoverDefaultRate    = 50
	discoverProbeTimeout   = 3 * time.Second
	discoverBannerTimeout  = 2 * time.Second
	discoverBannerMaxBytes = 256
)

// DiscoverTCP probes ip:port combinations across a CIDR. via names a
// configured hop ("" = dial directly from this machine). The requested CIDR
// must sit INSIDE the configured [netdev.discovery] scopes — enforced here at
// the dial boundary, not trusted to the caller (guardrail invariant 3).
// Results land in the 待确认区 store (F1): parse passively, keep forever.
func (m *Manager) DiscoverTCP(ctx context.Context, via, cidr string, ports []int) ([]DiscoverHostResult, error) {
	if !m.cfg.NetDev.Enabled {
		return nil, fmt.Errorf("[netdev] is disabled")
	}
	ctx, stopRun := runCtx(ctx, "tcp")
	defer stopRun()
	if len(ports) == 0 {
		// Spec §4.2.5: the management-plane fingerprint set (was 22,23).
		ports = []int{22, 23, 161, 443, 830}
	}
	target, targetNet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}
	hosts, err := expandCIDR(target, targetNet)
	if err != nil {
		return nil, err
	}
	if !m.scopeAllows(targetNet) {
		return nil, fmt.Errorf("CIDR %s is outside the configured discovery scopes — probing is refused (scopes are a never-off guardrail)", cidr)
	}
	// cache_ttl_hours: fresh leads are skipped, only gaps get probed.
	hosts = cacheTTLFilter(hosts, m.cfg.NetDev.Discovery.CacheTTLHours, time.Now())

	dialer, closeHop, err := m.dialerFor(ctx, via)
	if err != nil {
		return nil, err
	}
	defer closeHop()

	return m.probeHosts(ctx, dialer, hosts, ports)
}

// Spec §4.7 pacing helpers — zero takes the spec default, and fast_mode
// multiplies the rate for authorized windows (the red lines don't move).
func discoveryEffectiveRate(cfgRate int, fast bool) int {
	if cfgRate <= 0 || cfgRate > 256 {
		cfgRate = discoverDefaultRate
	}
	if fast {
		cfgRate *= 4
	}
	if cfgRate > 256 {
		cfgRate = 256
	}
	return cfgRate
}

func discoveryHostCap(cfgMax int) int {
	if cfgMax <= 0 {
		return 65536
	}
	return cfgMax
}

func discoveryPerHostDelayMs(cfgMs int) int {
	switch {
	case cfgMs == 0:
		return 800 // spec default (±30% jitter)
	case cfgMs < 0:
		return 0 // explicitly off
	default:
		return cfgMs
	}
}

// discoveryHostsWithinCap sums one plan's hosts and refuses past the cap
// with guidance instead of silently truncating (spec: 超出计划卡指引调参).
func discoveryHostsWithinCap(cidrs []string, cfgMax int) (int, error) {
	total := 0
	for _, c := range cidrs {
		_, ipnet, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			continue
		}
		ones, _ := ipnet.Mask.Size()
		total += 1 << uint(32-ones)
	}
	if cap := discoveryHostCap(cfgMax); total > cap {
		return total, fmt.Errorf("plan covers %d addresses, over the max_hosts_per_job budget of %d — split the run or raise [netdev.discovery] max_hosts_per_job", total, cap)
	}
	return total, nil
}

// discoverRuns: in-flight discovery contexts, so the global emergency stop
// cancels probes together with device sessions and terminals (§15.1 断线即停).
var (
	discoverRunsMu sync.Mutex
	discoverRuns   = map[string]context.CancelFunc{}
)

func registerDiscoverRun(id string, cancel context.CancelFunc) {
	discoverRunsMu.Lock()
	defer discoverRunsMu.Unlock()
	discoverRuns[id] = cancel
}

func dropDiscoverRun(id string) {
	discoverRunsMu.Lock()
	defer discoverRunsMu.Unlock()
	delete(discoverRuns, id)
}

// CancelDiscoverRuns cancels every in-flight discovery run; returns how many.
func CancelDiscoverRuns() int {
	discoverRunsMu.Lock()
	defer discoverRunsMu.Unlock()
	n := 0
	for id, cancel := range discoverRuns {
		cancel()
		delete(discoverRuns, id)
		n++
	}
	return n
}

// PauseDiscoverRun cancels ONE run by id (the bridge's 暂停 button); false
// when the run already finished.
func PauseDiscoverRun(id string) bool {
	discoverRunsMu.Lock()
	defer discoverRunsMu.Unlock()
	if cancel, ok := discoverRuns[id]; ok {
		cancel()
		delete(discoverRuns, id)
		return true
	}
	return false
}

// runCtx wraps a discovery context with the registry under a run id.
func runCtx(ctx context.Context, id string) (context.Context, func()) {
	c, cancel := context.WithCancel(ctx)
	registerDiscoverRun(id, cancel)
	return c, func() { dropDiscoverRun(id); cancel() }
}

// probeHosts is the shared polite worker pool (rate-capped, banner-grabbing
// probes through one dialer) — used by the hop path and the F4 vantage path.
func (m *Manager) probeHosts(ctx context.Context, dialer transport.Dialer, hosts []string, ports []int) ([]DiscoverHostResult, error) {
	rate := discoveryEffectiveRate(m.cfg.NetDev.Discovery.Rate, m.cfg.NetDev.Discovery.FastMode)
	delayMs := discoveryPerHostDelayMs(m.cfg.NetDev.Discovery.PerHostDelayMS)

	type job struct{ ip string }
	jobs := make(chan job)
	results := make(chan DiscoverHostResult, rate)
	var wg sync.WaitGroup
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i := 0; i < rate; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				select {
				case <-workerCtx.Done():
					return
				default:
				}
				var open []DiscoverPortProbe
				for _, port := range ports {
					p := probeTCP(ctx, dialer, j.ip, port)
					if p.Open {
						open = append(open, p)
					}
				}
				if len(open) > 0 {
					results <- DiscoverHostResult{IP: j.ip, Ports: open}
				}
				if delayMs > 0 {
					// Politeness jitter (±30%): a steady drumbeat reads as a
					// scanner, a jittered one as office hours.
					d := time.Duration(delayMs) * time.Millisecond
					jitter := time.Duration(float64(d) * (0.7 + 0.6*rand.Float64()))
					select {
					case <-workerCtx.Done():
					case <-time.After(jitter):
					}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, ip := range hosts {
			select {
			case jobs <- job{ip: ip}:
			case <-workerCtx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	var out []DiscoverHostResult
	for r := range results {
		out = append(out, r)
	}
	// F1: results persist to the 待确认区 (best-effort — a store hiccup must
	// not fail the scan; the next run re-merges). Swept semantics: the probed
	// list is exact, so closes within it fire R2 newly-closed events.
	if len(out) > 0 {
		_ = RecordDiscoveredSwept(SourceDiscover, out, ports)
	}
	// F2: when a community is configured, one sysDescr/sysName GET per host
	// with an open 161 (single attempt, no retry — the probe constitution).
	if community := strings.TrimSpace(m.cfg.NetDev.Discovery.SnmpCommunity); community != "" {
		for _, h := range out {
			has161 := false
			for _, p := range h.Ports {
				if p.Port == 161 {
					has161 = true
					break
				}
			}
			if !has161 {
				continue
			}
			desc, _ := snmpFingerprint(ctx, h.IP, community)
			if desc == "" {
				continue
			}
			vendor, role := hintsFromSysDescr(desc)
			_ = RecordDiscoveredHints(SourceDiscover, h.IP, vendor, role)
		}
	}
	// F3: opt-in application fingerprint — one standard GET per web port,
	// nothing more (title/Server/cert only land when http_probe is on).
	if m.cfg.NetDev.Discovery.HTTPProbe {
		for _, h := range out {
			for _, p := range h.Ports {
				if !httpFingerprintPorts[p.Port] {
					continue
				}
				if fp := httpFingerprint(ctx, dialer, h.IP, p.Port, p.Port == 443 || p.Port == 8443); fp != nil {
					_ = RecordDiscoveredHTTP(h.IP, p.Port, fp)
				}
			}
		}
	}
	return out, ctx.Err()
}

// probeTCP dials ip:port through dialer and grabs a banner when open.
func probeTCP(ctx context.Context, dialer transport.Dialer, ip string, port int) DiscoverPortProbe {
	dctx, cancel := context.WithTimeout(ctx, discoverProbeTimeout)
	defer cancel()
	conn, err := dialer.DialContext(dctx, "tcp", net.JoinHostPort(ip, fmt.Sprint(port)))
	if err != nil {
		return DiscoverPortProbe{Port: port}
	}
	p := DiscoverPortProbe{Port: port, Open: true}
	_ = conn.SetReadDeadline(time.Now().Add(discoverBannerTimeout))
	buf := make([]byte, discoverBannerMaxBytes)
	n, _ := io.ReadFull(conn, buf[:1])
	if n > 0 {
		// Read whatever follows the first byte (banner or nothing).
		rest, _ := conn.Read(buf[1:])
		banner := strings.TrimSpace(string(buf[:1+rest]))
		if isPrintableBanner(banner) {
			p.Banner = banner
		}
	}
	conn.Close()
	return p
}

func isPrintableBanner(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\t' {
			return false
		}
	}
	return len(s) > 0
}

// dialerFor returns the first-hop dialer: direct, or a hop's SSH connection
// (probes then travel as direct-tcpip channels through that hop).
func (m *Manager) dialerFor(ctx context.Context, via string) (transport.Dialer, func(), error) {
	cleanup := func() {}
	if strings.TrimSpace(via) == "" {
		return directDialer{timeout: discoverProbeTimeout}, cleanup, nil
	}
	hop, ok := m.cfg.NetDevHopByName(via)
	if !ok {
		return nil, cleanup, fmt.Errorf("hop %q is not configured (hops are human-registered only)", via)
	}
	lookup := m.lookupEntry()
	resolved, err := transport.ResolveHost(lookup, hop.Name, nil)
	if err != nil {
		return nil, cleanup, err
	}
	auth := transport.AuthOptions{
		Password:   secretReader(SecretKindPassword, hop.PasswordEnv),
		Passphrase: secretReader(SecretKindPassphrase, hop.PassphraseEnv),
	}
	client, err := transport.New(transport.Options{
		Host:     resolved,
		Auth:     auth,
		HostKeys: &transport.HostKeyPolicy{Prompt: HostKeyPrompt},
	})
	if err != nil {
		return nil, cleanup, err
	}
	if err := client.Start(ctx); err != nil {
		client.Close()
		return nil, cleanup, err
	}
	sshClient, err := client.SSH()
	if err != nil {
		client.Close()
		return nil, cleanup, err
	}
	cleanup = func() { client.Close() }
	return sshDialer{client: sshClient}, cleanup, nil
}

// directDialer dials straight from this machine (no hop).
type directDialer struct{ timeout time.Duration }

func (d directDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var nd net.Dialer
	if d.timeout > 0 {
		nd.Timeout = d.timeout
	}
	return nd.DialContext(ctx, network, addr)
}

// sshDialer adapts an established *ssh.Client to the Dialer interface:
// every probe becomes a direct-tcpip channel through the hop's connection.
type sshDialer struct {
	client interface {
		DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	}
}

func (d sshDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.client.DialContext(ctx, network, addr)
}

// scopeAllows reports whether target lies inside one of the configured scopes.
func (m *Manager) scopeAllows(target *net.IPNet) bool {
	for _, s := range m.cfg.NetDev.Discovery.Scopes {
		_, scopeNet, err := net.ParseCIDR(strings.TrimSpace(s))
		if err != nil {
			continue
		}
		if cidrContains(scopeNet, target) {
			return true
		}
	}
	return false
}

func cidrContains(outer, inner *net.IPNet) bool {
	if !outer.Contains(inner.IP) {
		return false
	}
	ob, _ := outer.Mask.Size()
	ib, _ := inner.Mask.Size()
	return ob <= ib
}

// expandCIDR lists usable host IPs (network/broadcast excluded for IPv4).
func expandCIDR(ip net.IP, ipNet *net.IPNet) ([]string, error) {
	var out []string
	for cur := ip.Mask(ipNet.Mask); ipNet.Contains(cur); incIP(cur) {
		out = append(out, cur.String())
		if len(out) > discoverMaxHosts {
			return nil, fmt.Errorf("CIDR too large for tunnel-mode probing (>%d hosts); use netprobe", discoverMaxHosts)
		}
	}
	if len(out) > 2 && strings.Contains(out[0], ".") {
		return out[1 : len(out)-1], nil // drop network + broadcast
	}
	return out, nil
}

func incIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			return
		}
	}
}
