package main

// trustdomain_app.go is the desktop bridge for the private-network trust
// domain (docs/TRUSTDOMAIN_SPEC.md §15.3): a read-mostly status view for
// the settings panel plus the three buttons that make sense from a UI
// (pause/resume brake, manual audit anchor). Everything mutating runs the
// same offline node path as the CLI — on multi-admin domains the quorum
// error surfaces and the panel points at the CLI on a networked node.

import (
	"fmt"
	"time"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev"
	"github.com/zzycxz/hiq/internal/trustdomain"
)

// TrustDomainMemberView is one member card.
type TrustDomainMemberView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Role         string `json:"role"` // admin | member
	Attestation  string `json:"attestation,omitempty"`
	AdmittedAt   uint64 `json:"admittedAt"`
}

// TrustDomainTokenView is one capability token row.
type TrustDomainTokenView struct {
	ID       string   `json:"id"`
	Resource string   `json:"resource"`
	Ops      []string `json:"ops"`
	Expires  uint64   `json:"expires"`
	Parent   string   `json:"parent,omitempty"` // set on delegated sub-tokens
}

// TrustDomainView is the panel payload. Enabled=false hides the panel
// content; Joined=false shows join guidance instead of the board.
type TrustDomainView struct {
	Enabled bool   `json:"enabled"`
	Joined  bool   `json:"joined"`
	Detail  string `json:"detail,omitempty"` // guidance when not joined / error

	Domain string `json:"domain,omitempty"`
	Height uint64 `json:"height"`
	Head   string `json:"head,omitempty"`
	Paused bool   `json:"paused"`
	Quorum string `json:"quorum,omitempty"` // "2/3"
	Me     string `json:"me,omitempty"`

	Members []TrustDomainMemberView `json:"members"`
	Revoked []string                `json:"revoked"`
	Tokens  []TrustDomainTokenView  `json:"tokens"`

	SuccessionConfigured bool     `json:"successionConfigured"`
	SuccessionAfterSec   uint32   `json:"successionAfterSec"`
	SuccessionMembers    []string `json:"successionMembers"`
	SuccessionLastActive uint64   `json:"successionLastActive"`
	SuccessionDue        bool     `json:"successionDue"`
}

// TrustDomainStatus renders the local ledger view. Read-only, no network
// round-trips (freshening is the CLI daemon's job — the UI must never
// block on a peer dial).
func (a *App) TrustDomainStatus() TrustDomainView {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		return TrustDomainView{Detail: fmt.Sprintf("配置读取失败: %v", err)}
	}
	td := cfg.TrustDomain
	if !td.Enabled {
		return TrustDomainView{}
	}
	view := TrustDomainView{Enabled: true}

	node, err := netdev.SharedRemoteNode(cfg)
	if err != nil {
		view.Detail = err.Error() // not joined yet — the panel shows guidance
		return view
	}
	view.Joined = true
	st := node.State()
	view.Domain = trustdomain.DomainID(node.Chain())
	view.Height = node.Chain().Height()
	view.Head = node.Chain().HeadHash().Hex()
	view.Paused = st.Paused
	view.Quorum = fmt.Sprintf("%d/%d", st.QuorumM, len(st.Admins()))
	view.Me = node.Identity()

	for _, id := range st.MemberIDs() {
		info := st.Member(id)
		m := TrustDomainMemberView{ID: id, Name: info.DisplayName, AdmittedAt: info.AdmittedAt, Role: "member"}
		if info.Admin {
			m.Role = "admin"
		}
		if at := st.LatestAttestation(id); at != nil {
			m.Attestation = fmt.Sprintf("v%s · audit %s", at.Version, at.AuditHead)
		}
		view.Members = append(view.Members, m)
	}
	view.Revoked = st.RevokedIDs()
	now := uint64(time.Now().Unix())
	for _, tok := range st.MemberTokens(node.Identity()) {
		view.Tokens = append(view.Tokens, TrustDomainTokenView{
			ID: tok.TokenID, Resource: tok.Resource, Ops: tok.Operations,
			Expires: tok.ExpiresAt, Parent: tok.ParentTokenID,
		})
	}
	due, members, after, last := st.SuccessionDue(now)
	view.SuccessionConfigured = after > 0
	view.SuccessionAfterSec = after
	view.SuccessionMembers = members
	view.SuccessionLastActive = last
	view.SuccessionDue = due
	return view
}

// TrustDomainPause engages the emergency brake (spec §6.4). Offline
// proposal — succeeds directly on single-admin domains; multi-admin
// surfaces the quorum error (run it from a networked node via CLI).
func (a *App) TrustDomainPause(reason string) error {
	return a.tdQuorumPause(false, reason)
}

// TrustDomainResume lifts the brake.
func (a *App) TrustDomainResume() error {
	return a.tdQuorumPause(true, "")
}

func (a *App) tdQuorumPause(resume bool, reason string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	node, err := netdev.SharedRemoteNode(cfg)
	if err != nil {
		return err
	}
	id := node.Self()
	rec := trustdomain.NewPauseRecord(resume, reason, uint64(time.Now().Unix()))
	// ProposeQuorum counts the issuer's own signature and collects peer
	// co-signatures — the node path IS the quorum path.
	return node.ProposeQuorum(func(parent trustdomain.Hash) (*trustdomain.Record, error) {
		if err := rec.SignAs(id, parent); err != nil {
			return nil, err
		}
		return rec, nil
	})
}

// TrustDomainAnchor manually cross-anchors the netdev audit chain head
// (spec §八) — the incident/scripted button behind the automatic cadence.
func (a *App) TrustDomainAnchor() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	node, err := netdev.SharedRemoteNode(cfg)
	if err != nil {
		return err
	}
	head := netdev.AuditChainHead()
	if head == "" {
		return fmt.Errorf("本地审计链为空（尚无带哈希的审计条目）")
	}
	return node.AnchorAudit(head, uint64(time.Now().Unix()))
}
