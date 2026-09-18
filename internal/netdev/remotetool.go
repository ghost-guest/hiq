package netdev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/trustdomain"
	"github.com/zzycxz/hiq/internal/trustdomain/nettrans"
)

// The agent-facing surface of the trust domain (TRUSTDOMAIN_SPEC §7.3/§15):
// two read-only tools, netdev_fleet (local board) and netdev_remote
// (delegated work). They appear only when [trustdomain] is enabled — the
// fleet is invisible to agents on hosts that never joined a domain.

var (
	remoteOnce sync.Once
	remoteNode *trustdomain.Node
	remoteErr  error
)

// SharedRemoteNode lazily opens this host's embedded trust-domain node:
// identity + persisted ledger + peers from [trustdomain].bootstrap_peers.
// One instance per process (the CLI daemon and agent tools must share it).
func SharedRemoteNode(cfg *config.Config) (*trustdomain.Node, error) {
	remoteOnce.Do(func() {
		td := cfg.TrustDomain
		dir := td.DataDirOrDefault()
		if dir == "" {
			remoteErr = errors.New("trustdomain: data dir unavailable — set [trustdomain].data_dir")
			return
		}
		id, err := trustdomain.LoadOrCreateIdentity(dir)
		if err != nil {
			remoteErr = err
			return
		}
		store, err := trustdomain.OpenStore(dir)
		if err != nil {
			remoteErr = err
			return
		}
		chain, err := store.Load()
		if err != nil {
			remoteErr = fmt.Errorf("trustdomain: 未入域（先 hiq trustdomain init/join）: %w", err)
			return
		}
		var node *trustdomain.Node
		node = trustdomain.NewNode(id, chain, func() []trustdomain.Peer {
			var peers []trustdomain.Peer
			for _, addr := range td.BootstrapPeers {
				if addr = strings.TrimSpace(addr); addr != "" {
					peers = append(peers, nettrans.NewNetPeer(addr, id, nettrans.ChainLookup(node.Chain())))
				}
			}
			return peers
		}, trustdomain.NodeOptions{CheckpointEvery: td.CheckpointEveryBlocks, Store: store})
		remoteNode = node
	})
	return remoteNode, remoteErr
}

// findCoveringToken picks this member's active token whose scope covers
// (resource, operation) — the agent states intent; capability discovery is
// mechanical. Wildcards ("*") on the token side count.
func findCoveringToken(st *trustdomain.State, subject, resource, operation string, now uint64) *trustdomain.TokenInfo {
	var fallback *trustdomain.TokenInfo
	for _, tok := range st.MemberTokens(subject) {
		if tok.ExpiresAt != 0 && now > tok.ExpiresAt {
			continue
		}
		if tok.Resource != "*" && tok.Resource != resource {
			continue
		}
		for _, op := range tok.Operations {
			if op == "*" {
				if fallback == nil {
					fallback = tok
				}
			}
			if op == operation {
				return tok
			}
		}
	}
	return fallback
}

// fleetTool — netdev_fleet: the local view of the domain. Read-only, no
// network required (one best-effort sync tick first when peers exist).
type fleetTool struct{ cfg *config.Config }

func (t *fleetTool) Name() string { return "netdev_fleet" }

func (t *fleetTool) Description() string {
	return "Show THIS host's private trust-domain fleet board: members (identity, display name, admin/revoked state), each member's latest self-attestation (version/policy/audit head), chain height, and whether the quorum emergency brake (PAUSE) is engaged. " +
		"Read-only and local — it never contacts peers by itself. Use it first when a task mentions the fleet, other hiq hosts, or before netdev_remote calls."
}

func (t *fleetTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *fleetTool) ReadOnly() bool { return true }

func (t *fleetTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	node, err := SharedRemoteNode(t.cfg)
	if err != nil {
		return "", err
	}
	node.Tick() // best-effort freshen; errors ignored (local view is fine)
	st := node.State()

	type member struct {
		ID, Name, Role, Attestation string
		Revoked                     bool
	}
	var out struct {
		Domain  string   `json:"domain"`
		Height  uint64   `json:"height"`
		Paused  bool     `json:"paused"`
		Quorum  string   `json:"quorum"`
		Me      string   `json:"me"`
		Members []member `json:"members"`
		Revoked []string `json:"revoked"`
	}
	out.Domain = trustdomain.DomainID(node.Chain())
	out.Height = node.Chain().Height()
	out.Paused = st.Paused
	out.Quorum = fmt.Sprintf("%d/%d", st.QuorumM, len(st.Admins()))
	out.Me = node.Identity()
	for _, id := range st.MemberIDs() {
		info := st.Member(id)
		m := member{ID: id, Name: info.DisplayName, Role: "member"}
		if info.Admin {
			m.Role = "admin"
		}
		if a := st.LatestAttestation(id); a != nil {
			m.Attestation = fmt.Sprintf("v%s policy=%s audit=%s", a.Version, a.PolicyHash, a.AuditHead)
		}
		out.Members = append(out.Members, m)
	}
	out.Revoked = st.RevokedIDs()
	b, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// remoteTool — netdev_remote: delegated read-only work on another member,
// gated by capability tokens (spec §7.3). The agent states intent; the
// tool finds a covering token, syncs once, then sends the signed
// delegation — the remote §7.3 gate runs before any executor code.
type remoteTool struct{ cfg *config.Config }

func (t *remoteTool) Name() string { return "netdev_remote" }

func (t *remoteTool) Description() string {
	return "Run a READ-ONLY delegated diagnostic on ANOTHER hiq fleet member over the trust domain (netdev resources today: netdev/health, netdev/triage). " +
		"Pick the target host:port from netdev_fleet/bootstrap peers. A capability token covering (resource, operation) must already be issued to THIS host — the tool selects it automatically and refuses otherwise (ask an admin: hiq trustdomain token). " +
		"Refused when the fleet PAUSE brake is engaged."
}

func (t *remoteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"host":     {"type": "string", "description": "target member address host:port"},
			"resource": {"type": "string", "enum": ["netdev/health", "netdev/triage"], "description": "remote diagnostic surface"},
			"operation": {"type": "string", "enum": ["read"], "description": "read-only by vocabulary"},
			"payload":  {"type": "object", "description": "resource parameters, e.g. {\"device\":\"jump-01\"} for netdev/triage"}
		},
		"required": ["host", "resource", "operation"]
	}`)
}

func (t *remoteTool) ReadOnly() bool { return true }

func (t *remoteTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Host      string          `json:"host"`
		Resource  string          `json:"resource"`
		Operation string          `json:"operation"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Host == "" {
		return "", errors.New("netdev_remote: host is required (see netdev_fleet / [trustdomain].bootstrap_peers)")
	}
	if a.Payload == nil {
		a.Payload = json.RawMessage("{}")
	}
	node, err := SharedRemoteNode(t.cfg)
	if err != nil {
		return "", err
	}
	node.Tick() // freshen the local ledger before token selection

	now := uint64(time.Now().Unix())
	st := node.State()
	if st.Paused {
		return "", errors.New("netdev_remote: fleet PAUSE engaged — delegated work refused until an admin resumes (hiq trustdomain resume)")
	}
	tok := findCoveringToken(st, node.Identity(), a.Resource, a.Operation, now)
	if tok == nil {
		return "", fmt.Errorf("netdev_remote: no capability token covers %s %s — ask an admin: hiq trustdomain token <本机ID前缀> %s %s <秒>",
			a.Resource, a.Operation, a.Resource, a.Operation)
	}

	peer := nettrans.NewNetPeer(a.Host, node.Self(), nettrans.ChainLookup(node.Chain()))
	out, err := node.RequestWork(peer, tok.TokenID, a.Resource, a.Operation, a.Payload, 300, now)
	if err != nil {
		return "", fmt.Errorf("netdev_remote: %w", err)
	}
	return string(out), nil
}
