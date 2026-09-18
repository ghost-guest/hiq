package cli

import (
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/zzycxz/hiq/internal/trustdomain"
)

func TestDecodePeerKey(t *testing.T) {
	id, err := trustdomain.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	// Hex form.
	got, err := decodePeerKey(hex.EncodeToString(id.Public))
	if err != nil || string(got) != string(id.Public) {
		t.Fatalf("hex decode: %v", err)
	}

	// Std base64 form.
	got, err = decodePeerKey(base64.StdEncoding.EncodeToString(id.Public))
	if err != nil || string(got) != string(id.Public) {
		t.Fatalf("base64 decode: %v", err)
	}

	if _, err := decodePeerKey("!!not-a-key!!"); err == nil {
		t.Fatal("garbage accepted as key")
	}
}

func TestFindMemberByPrefix(t *testing.T) {
	h := newTDHarness(t)

	id := h.member.ID()
	got, err := findMemberByPrefix(h.chain, id[:8])
	if err != nil || got != id {
		t.Fatalf("prefix lookup: %v %s", err, got)
	}
	if _, err := findMemberByPrefix(h.chain, "ZZZZ"); err == nil {
		t.Fatal("unknown prefix accepted")
	}
}

// newTDHarness builds a small domain (3 admins, quorum 2, one admitted
// member) for prefix/lookup tests.
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

	gen, err := trustdomain.BuildGenesis(h.admins, 2, "cli-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	h.chain, err = trustdomain.ValidateChain([]*trustdomain.Block{gen})
	if err != nil {
		t.Fatal(err)
	}

	// Admit the member (quorum 2 of the 3 admins).
	rec := trustdomain.NewMemberRecord(member.Public, "m", false, 2)
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
