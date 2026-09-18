package config

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
)

// DataDirOrDefault resolves the ledger home: explicit setting, else
// <hiq user state root>/trustdomain, else "" (OS dir unavailable —
// callers must require an explicit data_dir then).
func (c TrustDomainConfig) DataDirOrDefault() string {
	if c.DataDir != "" {
		return c.DataDir
	}
	if root := MemoryUserDir(); root != "" {
		return filepath.Join(root, "trustdomain")
	}
	return ""
}

// isValidBase64Key reports whether s decodes to Ed25519 public-key-sized
// material (32 bytes). Display-level check only — signatures on the chain
// are the real authority.
func isValidBase64Key(s string) bool {
	raw, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(raw) == 32
}

// TrustDomainConfig configures the private-network trust domain ledger
// (docs/TRUSTDOMAIN_SPEC.md §15.1). The domain is cross-profile
// infrastructure: it is off by default and independent of any agent profile.
//
// Trust placement (spec §1.4 #3): SignalURL points at the UNTRUSTED
// rendezvous server (linkpeer-signal) used only to arrange peer-to-peer
// connections — it holds no authority and sees only metadata.
type TrustDomainConfig struct {
	Enabled bool `toml:"enabled"`
	// SignalURL is the rendezvous point ("wss://host/knock"); empty means
	// LAN-only discovery (mDNS / bootstrap peers).
	SignalURL string `toml:"signal_url"`
	// DomainID is the genesis block hash (hex) — the domain's permanent
	// identity. Empty = this host is not yet admitted to any domain.
	DomainID string `toml:"domain_id"`
	// DataDir holds the ledger, identity key and related state. Empty =
	// <user config dir>/trustdomain.
	DataDir string `toml:"data_dir"`
	// BootstrapPeers lists known member addresses ("host:port"). Gossip is
	// pull-based, so a fleet configures these bidirectionally (mesh) —
	// spec §四/§17: servers should not rely on avahi/mDNS being present.
	BootstrapPeers []string `toml:"bootstrap_peers"`
	// ListenAddr is the TCP listener for inbound peers (default ":7123";
	// "127.0.0.1:0" for a private ephemeral port).
	ListenAddr string `toml:"listen_addr"`
	// Discover enables UDP-broadcast LAN discovery (spec §四①): signed
	// beacons on DiscoveryPort announce members of THIS domain; discovered
	// addresses merge into the peer set. Addresses only — trust stays with
	// the membership handshake.
	Discover bool `toml:"discover"`
	// DiscoveryPort is the UDP beacon port (default 7125).
	DiscoveryPort int `toml:"discovery_port"`
	// Admins lists founding/known admin public keys (base64, std encoding)
	// for display and admission flows; the authoritative set lives on the
	// chain itself.
	Admins []string `toml:"admins"`
	// QuorumM mirrors the domain's admin quorum threshold for pre-join
	// display; the chain is authoritative.
	QuorumM int `toml:"quorum_m"`

	// Limits tune the node loop (chain-internal caps like MaxBlockBytes
	// are constants in internal/trustdomain).
	AttestIntervalSec     int    `toml:"attest_interval_sec"`     // 0 = 60
	CheckpointEveryBlocks uint64 `toml:"checkpoint_every_blocks"` // 0 = off, manual only
	TickIntervalSec       int    `toml:"tick_interval_sec"`       // 0 = 5
}

// AttestIntervalOrDefault returns the attestation cadence in seconds.
func (c TrustDomainConfig) AttestIntervalOrDefault() int {
	if c.AttestIntervalSec > 0 {
		return c.AttestIntervalSec
	}
	return 60
}

// TickIntervalOrDefault returns the gossip loop cadence in seconds.
func (c TrustDomainConfig) TickIntervalOrDefault() int {
	if c.TickIntervalSec > 0 {
		return c.TickIntervalSec
	}
	return 5
}

// DiscoveryPortOrDefault returns the UDP beacon port.
func (c TrustDomainConfig) DiscoveryPortOrDefault() int {
	if c.DiscoveryPort > 0 {
		return c.DiscoveryPort
	}
	return 7125
}

// ListenAddrOrDefault returns the inbound peer listener address.
func (c TrustDomainConfig) ListenAddrOrDefault() string {
	if c.ListenAddr != "" {
		return c.ListenAddr
	}
	return ":7123"
}

// Validate applies cross-field sanity: a disabled section may hold anything
// (users pre-stage config), but an enabled one must be coherent.
func (c TrustDomainConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.QuorumM < 0 {
		return fmt.Errorf("trustdomain: quorum_m must be >= 0")
	}
	for _, a := range c.Admins {
		if !isValidBase64Key(a) {
			return fmt.Errorf("trustdomain: admin key %q is not valid base64 ed25519 material", a)
		}
	}
	return nil
}
