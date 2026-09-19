package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/agent/testutil"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/provider"
)

func newLedgerTestAgent(t *testing.T) *Agent {
	t.Helper()
	s := NewSession("system prompt")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "refactor the exporter module"})
	s.Add(provider.Message{Role: provider.RoleAssistant, Content: "on it"})
	a := &Agent{session: s, contextWindow: 100000}
	return a
}

func TestEnsureTaskLedgerCreatesOnce(t *testing.T) {
	a := newLedgerTestAgent(t)
	a.ensureTaskLedger("refactor the exporter module")
	idx := a.ledgerIndex()
	if idx != 2 { // system, first user, ledger
		t.Fatalf("ledger index = %d, want 2", idx)
	}
	m := a.session.Messages[idx]
	if !isTaskLedger(m) {
		t.Fatal("inserted message is not tagged as a task ledger")
	}
	body := ledgerBody(m.Content)
	if !strings.Contains(body, "refactor the exporter module") {
		t.Fatal("fresh ledger Goal must carry the user's request")
	}
	first := m.Content
	a.ensureTaskLedger("a different goal") // second call must be a no-op
	if a.ledgerIndex() != idx || a.session.Messages[idx].Content != first {
		t.Fatal("ensureTaskLedger must not rewrite an existing ledger")
	}
}

func TestEnsureTaskLedgerResumedSessionWithoutOne(t *testing.T) {
	a := newLedgerTestAgent(t)
	a.ensureTaskLedger("goal A")
	// Simulate a legacy session that lost its ledger: insert without one.
	msgs := a.session.Messages
	without := make([]provider.Message, 0, len(msgs)-1)
	for _, m := range msgs {
		if !isTaskLedger(m) {
			without = append(without, m)
		}
	}
	a.session.Rewrite(without, "test strip")
	a.ensureTaskLedger("goal B")
	if a.ledgerIndex() != 2 {
		t.Fatalf("re-created ledger index = %d, want 2", a.ledgerIndex())
	}
	if !strings.Contains(ledgerBody(a.session.Messages[a.ledgerIndex()].Content), "goal B") {
		t.Fatal("re-created ledger must seed the Goal from the current input")
	}
}

func TestUpdateLedgerSections(t *testing.T) {
	a := newLedgerTestAgent(t)
	a.ensureTaskLedger("refactor the exporter")

	if _, err := a.UpdateLedger("decisions", "keep the streaming writer", "replace"); err != nil {
		t.Fatalf("update decisions: %v", err)
	}
	if _, err := a.UpdateLedger("ACCEPTANCE CRITERIA", "all tests green", "replace"); err != nil {
		t.Fatalf("alias section must normalize: %v", err)
	}
	if _, err := a.UpdateLedger("progress", "extracted interface", "append"); err != nil {
		t.Fatalf("append progress: %v", err)
	}
	if _, err := a.UpdateLedger("progress", "wired registry", "append"); err != nil {
		t.Fatalf("second append: %v", err)
	}

	secs := parseLedgerSections(a.session.Messages[a.ledgerIndex()].Content)
	if secs["Decisions & rationale"] != "keep the streaming writer" {
		t.Fatalf("decisions = %q", secs["Decisions & rationale"])
	}
	if secs["Acceptance criteria"] != "all tests green" {
		t.Fatalf("acceptance = %q", secs["Acceptance criteria"])
	}
	want := "- extracted interface\n- wired registry"
	if secs["Progress & artifacts"] != want {
		t.Fatalf("progress = %q, want %q", secs["Progress & artifacts"], want)
	}
	// Goal untouched by the updates.
	if secs["Goal"] != "refactor the exporter" {
		t.Fatalf("goal = %q", secs["Goal"])
	}
}

func TestUpdateLedgerNoOpSkipsRewrite(t *testing.T) {
	a := newLedgerTestAgent(t)
	a.ensureTaskLedger("goal")
	if _, err := a.UpdateLedger("goal", "goal", "replace"); err != nil {
		t.Fatalf("update: %v", err)
	}
	v := a.session.RewriteVersion()
	if _, err := a.UpdateLedger("goal", "goal", "replace"); err != nil {
		t.Fatalf("second update: %v", err)
	}
	if a.session.RewriteVersion() != v {
		t.Fatal("identical content must not rewrite the session (cache economics)")
	}
}

func TestUpdateLedgerValidation(t *testing.T) {
	a := newLedgerTestAgent(t)
	a.ensureTaskLedger("goal")
	if _, err := a.UpdateLedger("nonsense", "x", "replace"); err == nil {
		t.Fatal("unknown section must error")
	}
	if _, err := a.UpdateLedger("goal", "  ", "replace"); err == nil {
		t.Fatal("empty content must error")
	}
	if _, err := a.UpdateLedger("goal", "x", "upsert"); err == nil {
		t.Fatal("unknown mode must error")
	}
}

func TestLedgerPinnedAcrossCompaction(t *testing.T) {
	a := newLedgerTestAgent(t)
	a.ensureTaskLedger("refactor the exporter")
	if _, err := a.UpdateLedger("constraints", "module path must stay github.com/zzycxz/hiq", "replace"); err != nil {
		t.Fatalf("update constraints: %v", err)
	}

	// Fill the region with foldable filler after the ledger.
	for i := 0; i < 8; i++ {
		a.session.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("working ", 200)})
	}

	head := a.pinnedPrefixLen(a.session.Messages)
	if head < 3 {
		t.Fatalf("pinned prefix = %d, want >= 3 (system+user+ledger)", head)
	}
	ledgerIdx := a.ledgerIndex()
	if ledgerIdx >= head {
		t.Fatalf("ledger at %d must sit inside the pinned prefix (< %d)", ledgerIdx, head)
	}

	kept, fold := a.partitionFold(a.session.Messages[head:min(head+4, len(a.session.Messages))])
	_ = kept
	if len(fold) == 0 {
		t.Fatal("filler messages should be foldable")
	}

	// Even if the ledger somehow lands inside a fold region (legacy layout),
	// partitionFold must keep it verbatim.
	region := []provider.Message{
		{Role: provider.RoleAssistant, Content: strings.Repeat("x ", 300)},
		newTaskLedgerMessage("goal"),
		{Role: provider.RoleAssistant, Content: strings.Repeat("y ", 300)},
	}
	kept, fold = a.partitionFold(region)
	if len(kept) != 1 || !isTaskLedger(kept[0]) {
		t.Fatalf("partitionFold must keep the ledger verbatim; kept=%d fold=%d", len(kept), len(fold))
	}
}

func TestApplyLedgerReanchor(t *testing.T) {
	a := newLedgerTestAgent(t)
	a.ensureTaskLedger("ship the exporter")
	if _, err := a.UpdateLedger("acceptance", "make check passes", "replace"); err != nil {
		t.Fatalf("update acceptance: %v", err)
	}

	results := []string{"tool output"}
	a.applyLedgerReanchor(5, results) // below threshold
	if strings.Contains(results[0], "task-ledger re-anchor") {
		t.Fatal("re-anchor must not fire before the threshold")
	}

	a.applyLedgerReanchor(ledgerReanchorEvery, results)
	if !strings.Contains(results[0], "task-ledger re-anchor") {
		t.Fatal("re-anchor must fire at the threshold")
	}
	if !strings.Contains(results[0], "ship the exporter") || !strings.Contains(results[0], "make check passes") {
		t.Fatalf("re-anchor digest must carry goal and acceptance; got %q", results[0])
	}
	if a.lastReanchorStep != ledgerReanchorEvery {
		t.Fatalf("lastReanchorStep = %d, want %d", a.lastReanchorStep, ledgerReanchorEvery)
	}

	// Too soon after the last one: no second append.
	before := results[0]
	a.applyLedgerReanchor(ledgerReanchorEvery+1, results)
	if results[0] != before {
		t.Fatal("re-anchor must not fire again inside the interval")
	}

	// At the next interval: fires again.
	a.applyLedgerReanchor(2*ledgerReanchorEvery, results)
	if strings.Count(results[0], "task-ledger re-anchor") != 2 {
		t.Fatal("re-anchor must fire again after a full interval")
	}
}

func TestApplyLedgerReanchorWithoutLedger(t *testing.T) {
	a := &Agent{session: NewSession(""), contextWindow: 100000}
	results := []string{"out"}
	a.applyLedgerReanchor(ledgerReanchorEvery, results)
	if strings.Contains(results[0], "task-ledger re-anchor") {
		t.Fatal("no ledger, no re-anchor")
	}
}

func TestRunSeedsLedgerOnlyWhenOptIn(t *testing.T) {
	mpOff := testutil.NewMock("m", testutil.Turn{Text: "done"})
	off := New(mpOff, echoRegistry(), NewSession("sys"), Options{}, event.Discard)
	if err := off.Run(context.Background(), "do a thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if off.ledgerIndex() >= 0 {
		t.Fatal("the ledger must be opt-in: a default Run seeds nothing")
	}

	mpOn := testutil.NewMock("m", testutil.Turn{Text: "done"})
	on := New(mpOn, echoRegistry(), NewSession("sys"), Options{TaskLedger: true}, event.Discard)
	if err := on.Run(context.Background(), "do a thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	idx := on.ledgerIndex()
	if idx < 0 {
		t.Fatal("an opted-in Run must seed the ledger")
	}
	if !strings.Contains(ledgerBody(on.Session().Messages[idx].Content), "do a thing") {
		t.Fatal("seeded ledger Goal must carry the user's request")
	}
}

func TestLedgerRoundTripSaveLoad(t *testing.T) {
	a := newLedgerTestAgent(t)
	a.ensureTaskLedger("goal with exact paths")
	_, _ = a.UpdateLedger("constraints", "D:\\work only", "replace")
	// The ledger is an ordinary message in the log, so Save/Load round-trips it
	// verbatim — verified structurally here (serialization is covered by
	// session_test.go).
	m := a.session.Messages[a.ledgerIndex()]
	if !IsTaskLedger(m) || !strings.Contains(m.Content, "D:\\work only") {
		t.Fatal("ledger must round-trip as a first-class message")
	}
}
