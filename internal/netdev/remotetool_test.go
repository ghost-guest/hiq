package netdev

import (
	"testing"

	"github.com/zzycxz/hiq/internal/trustdomain"
)

// newTDHarness builds a small domain (3 admins, quorum 2, one admitted
// member) for token-selection tests.
func newTDHarness(t *testing.T) *tdHarness {
	t.Helper()
	h := &tdHarness{t: t}
	for i := 0; i < 3; i++ {
		id, err := trustdomain.GenerateIdentity()
		if err != nil {
			t.Fatal(err)
		}
		h.admins = append(h.admins, id)
	}
	member, err := trustdomain.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	h.member = member

	gen, err := trustdomain.BuildGenesis(h.admins, 2, "netdev-tool-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	if h.chain, err = trustdomain.ValidateChain([]*trustdomain.Block{gen}); err != nil {
		t.Fatal(err)
	}

	rec := trustdomain.NewMemberRecord(member.Public, "m", false, 1)
	if err := rec.SignAs(h.admins[0], h.chain.HeadHash()); err != nil {
		t.Fatal(err)
	}
	if err := rec.ApproveWith(h.admins[1], h.chain.HeadHash()); err != nil {
		t.Fatal(err)
	}
	b, err := trustdomain.NewBlock(h.chain.Height()+1, h.chain.HeadHash(), h.admins[2], []*trustdomain.Record{rec})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.chain.Append(b); err != nil {
		t.Fatal(err)
	}
	return h
}

type tdHarness struct {
	t      *testing.T
	admins []*trustdomain.Identity
	member *trustdomain.Identity
	chain  *trustdomain.Chain
}

// TestFindCoveringToken: exact scope wins over wildcard, expired tokens
// are skipped, no cover → nil.
func TestFindCoveringToken(t *testing.T) {
	h := newTDHarness(t)

	mk := func(id, res string, ops []string, exp uint64) {
		t.Helper()
		rec := trustdomain.NewTokenRecord(trustdomain.TokenPayload{
			TokenID: id, SubjectID: h.member.ID(), Resource: res,
			Operations: ops, ExpiresAt: exp,
		}, 1)
		if err := rec.SignAs(h.admins[0], h.chain.HeadHash()); err != nil {
			t.Fatal(err)
		}
		if err := rec.ApproveWith(h.admins[1], h.chain.HeadHash()); err != nil {
			t.Fatal(err)
		}
		b, err := trustdomain.NewBlock(h.chain.Height()+1, h.chain.HeadHash(), h.admins[2], []*trustdomain.Record{rec})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.chain.Append(b); err != nil {
			t.Fatal(err)
		}
	}
	mk("tok-exact", "netdev/health", []string{"read"}, 99_000)
	mk("tok-wild", "*", []string{"*"}, 99_000)
	mk("tok-dead", "netdev/triage", []string{"read"}, 1) // expired long ago

	st := h.chain.State()
	if got := findCoveringToken(st, h.member.ID(), "netdev/health", "read", 5_000); got == nil || got.TokenID != "tok-exact" {
		t.Fatalf("exact cover: %+v", got)
	}
	if got := findCoveringToken(st, h.member.ID(), "netdev/triage", "read", 5_000); got == nil || got.TokenID != "tok-wild" {
		t.Fatalf("wildcard fallback: %+v", got)
	}
	// Wildcard covers ANY op by definition — the executor's read-only
	// vocabulary is the final seal, not this selector.
	if got := findCoveringToken(st, h.member.ID(), "netdev/health", "write", 5_000); got == nil || got.TokenID != "tok-wild" {
		t.Fatalf("wildcard must cover any op: %+v", got)
	}
	if got := findCoveringToken(st, h.member.ID(), "netdev/triage", "read", 2); got != nil && got.TokenID == "tok-dead" {
		t.Fatalf("expired token selected: %+v", got)
	}
	if got := findCoveringToken(st, "stranger", "netdev/health", "read", 5_000); got != nil {
		t.Fatalf("tokens leaked across subjects: %+v", got)
	}
}
