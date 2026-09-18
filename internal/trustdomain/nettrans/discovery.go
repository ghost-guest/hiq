package nettrans

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"sync"
	"time"

	"crypto/ed25519"

	"github.com/zzycxz/hiq/internal/trustdomain"
)

// LAN discovery (spec §四 priority ①, UDP-broadcast flavor — uniform on
// Windows/macOS/Linux with zero dependencies; mDNS/DNS-SD can layer on
// later as a nicer tier). A Beacon is a SIGNED presence announcement;
// receivers verify it against their own ledger before even keeping the
// address. Discovery yields addresses only — trust is established by the
// full membership handshake at dial time, never by the beacon itself
// (发现不是信任，只是地址).

const (
	// DefaultDiscoveryPort is the UDP port beacons are broadcast to.
	DefaultDiscoveryPort = 7125
	// beaconFreshness bounds replayed stale addresses (± window).
	beaconFreshness = 120 * time.Second
	// DefaultBeaconInterval is the announce cadence.
	DefaultBeaconInterval = 15 * time.Second
)

// Beacon is one member's "I am here" announcement on the LAN.
type Beacon struct {
	DomainID string `json:"domain"` // genesis hash — receivers filter foreign domains
	MemberID string `json:"member"` // announcer's trust domain ID
	Addr     string `json:"addr"`   // the member's trust listener (host:port)
	Ts       int64  `json:"ts"`     // unix millis — freshness
	Pub      []byte `json:"pub"`    // member public key
	Sig      []byte `json:"sig"`    // over material
}

// BuildBeacon signs a presence announcement for this node.
func BuildBeacon(id *trustdomain.Identity, node *trustdomain.Node, addr string, tsMs int64) Beacon {
	b := Beacon{
		DomainID: trustdomain.DomainID(node.Chain()),
		MemberID: id.ID(),
		Addr:     addr,
		Ts:       tsMs,
		Pub:      id.Public,
	}
	b.Sig = id.Sign(b.material())
	return b
}

// material is the canonical signing preimage (Beacon with Sig nil).
func (b Beacon) material() []byte {
	c := b
	c.Sig = nil
	raw, _ := json.Marshal(&c)
	return raw
}

// Verify checks the beacon against the receiver's OWN ledger view: same
// domain, active member, registered key matches the announced key,
// signature valid, timestamp fresh. Returns false on ANY mismatch —
// callers drop silently (no oracle about which check failed).
func (b Beacon) Verify(st *trustdomain.State, domainID string, nowMs int64) bool {
	if b.DomainID != domainID || b.Addr == "" {
		return false
	}
	if nowMs-b.Ts > beaconFreshness.Milliseconds() || b.Ts-nowMs > beaconFreshness.Milliseconds() {
		return false
	}
	m := st.Member(b.MemberID)
	if m == nil || len(m.PublicKey) != ed25519.PublicKeySize {
		return false
	}
	if !bytes.Equal(m.PublicKey, b.Pub) {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(b.Pub), b.material(), b.Sig)
}

// Discoverer remembers freshly-seen member addresses (per member ID, with
// expiry) for OUR domain only. Thread-safe; the node loop polls Addresses.
type Discoverer struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]seenAddr // MemberID → latest address + expiry
}

type seenAddr struct {
	addr    string
	expires time.Time
}

// NewDiscoverer builds a discoverer; entries expire after ttl (last-seen
// based: a member re-announcing keeps itself fresh).
func NewDiscoverer(ttl time.Duration) *Discoverer {
	return &Discoverer{ttl: ttl, seen: map[string]seenAddr{}}
}

// Ingest verifies and remembers a beacon. Returns whether it was kept.
func (d *Discoverer) Ingest(b Beacon, st *trustdomain.State, domainID string, now time.Time) bool {
	if !b.Verify(st, domainID, now.UnixMilli()) {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen[b.MemberID] = seenAddr{addr: b.Addr, expires: now.Add(d.ttl)}
	return true
}

// Addresses returns fresh member addresses, excluding selfID.
func (d *Discoverer) Addresses(selfID string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for id, e := range d.seen {
		if id == selfID || time.Now().After(e.expires) {
			continue
		}
		out = append(out, e.addr)
	}
	return out
}

// SendBeacon writes one announcement (caller owns the socket: broadcast
// conn in production, unicast in tests).
func SendBeacon(conn net.Conn, b Beacon) error {
	raw, err := json.Marshal(&b)
	if err != nil {
		return err
	}
	_, err = conn.Write(raw)
	return err
}

// ListenBeacons receives announcements until ctx is done, feeding the
// discoverer through the node's CURRENT ledger view (a member revoked
// mid-listen stops being ingestible — revocation-on-sight posture).
func ListenBeacons(ctx context.Context, pc net.PacketConn, d *Discoverer, node *trustdomain.Node) error {
	go func() { <-ctx.Done(); _ = pc.Close() }()
	buf := make([]byte, 4096)
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		var b Beacon
		if json.Unmarshal(buf[:n], &b) != nil {
			continue // noise on the broadcast channel — ignore
		}
		d.Ingest(b, node.Chain().State(), trustdomain.DomainID(node.Chain()), time.Now())
	}
}

// LocalAddrHint returns a plausible non-loopback local IPv4 for beacon
// announcements: first up, non-loopback IPv4 interface address. Empty when
// none exists (offline hosts keep working via bootstrap_peers).
func LocalAddrHint() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				return ipn.IP.String()
			}
		}
	}
	return ""
}
