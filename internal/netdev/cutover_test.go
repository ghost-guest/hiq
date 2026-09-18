package netdev

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

func cutoverTestManager(t *testing.T) *Manager {
	t.Helper()
	m, _ := guardrailManager(t, config.NetDevGuardrails{})
	jobsDirOverride = t.TempDir()
	t.Cleanup(func() { jobsDirOverride = "" })
	cutoversDirOverride = t.TempDir()
	t.Cleanup(func() { cutoversDirOverride = "" })
	netdevStateDirOverr = t.TempDir()
	t.Cleanup(func() { netdevStateDirOverr = "" })
	SetBackupsDir(t.TempDir())
	return m
}

func waitCutover(t *testing.T, id, want string, notePart string) *CutoverRun {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		c, err := GetCutover(id)
		if err != nil {
			t.Fatalf("get cutover: %v", err)
		}
		if c.Status == want && (notePart == "" || strings.Contains(c.HoldNote, notePart)) {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("cutover %s: status %s (note %q), want %s/%q; steps %+v", id, c.Status, c.HoldNote, want, notePart, c.Steps)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func approvedVlanProposal(t *testing.T, m *Manager) string {
	t.Helper()
	p := &Proposal{Intent: "cutover payload: vlan 100", Steps: []ProposalStep{{
		Device: "sw1", Commands: []string{"vlan 100"}, Rollback: []string{"undo vlan 100"},
	}}}
	if err := m.ValidateProposal(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := SaveProposal(p); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApproveProposal(p.ID, false); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

// The full §7.2 chain: gate-sustained step → proposal step holds at its
// decision point → 继续 finishes → before/after snapshots + report.
func TestCutoverRunbookE2E(t *testing.T) {
	m := cutoverTestManager(t)
	pid := approvedVlanProposal(t, m)

	run, err := m.CutoverStart(&CutoverRun{
		Name:     "core-sw cutover",
		Deadline: time.Now().Add(30 * time.Minute),
		Steps: []CutoverStep{
			{Label: "确认版本", Device: "sw1", Command: "display version", EstSec: 30,
				Gate: &CutoverGate{Device: "sw1", Command: "display version", Expect: "Versatile", SustainSec: 1, TimeoutSec: 10}},
			{Label: "下发 VLAN 100", ProposalID: pid, EstSec: 60, DecisionPoint: true, Impact: "VLAN 100 已下发，核心侧生效"},
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(run.PreSnapshot) != 1 || run.PreSnapshot["sw1"] == "" {
		t.Fatalf("pre-snapshot = %v", run.PreSnapshot)
	}

	held := waitCutover(t, run.ID, CutoverHold, "决策点")
	if held.Steps[0].Status != CutoverStepDone {
		t.Fatalf("gate step status = %s (err %q)", held.Steps[0].Status, held.Steps[0].Error)
	}
	if held.Steps[1].Status != CutoverStepApproved {
		t.Fatalf("proposal step status = %s", held.Steps[1].Status)
	}

	if _, err := m.CutoverContinue(run.ID); err != nil {
		t.Fatalf("continue: %v", err)
	}
	done := waitCutover(t, run.ID, CutoverDone, "")
	if done.PostSnapshot["sw1"] == "" {
		t.Fatalf("post-snapshot = %v", done.PostSnapshot)
	}
	if !strings.Contains(done.Report, "前后配置对比") || !strings.Contains(done.Report, "sw1") {
		t.Fatalf("report = %.300q", done.Report)
	}
}

// At a decision point the human can press 回退 instead: the executed proposal
// unwinds and the run ends aborted with a report.
func TestCutoverRollbackAtDecisionPoint(t *testing.T) {
	m := cutoverTestManager(t)
	pid := approvedVlanProposal(t, m)

	run, err := m.CutoverStart(&CutoverRun{
		Name:     "rollback path",
		Deadline: time.Now().Add(30 * time.Minute),
		Steps: []CutoverStep{
			{Label: "变更", ProposalID: pid, EstSec: 60, DecisionPoint: true, Impact: "变更已下发"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitCutover(t, run.ID, CutoverHold, "决策点")

	rb, err := m.CutoverRollback(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rb.Status != CutoverAborted || rb.Steps[0].Status != CutoverStepRolled {
		t.Fatalf("run = %s, step = %s (note %q)", rb.Status, rb.Steps[0].Status, rb.HoldNote)
	}
	p, _ := GetProposal(pid)
	if p.Status != ProposalDraft || p.Steps[0].Applied {
		t.Fatalf("proposal after rollback: %s applied=%v", p.Status, p.Steps[0].Applied)
	}
	if !strings.Contains(rb.Report, "↩️") {
		t.Fatalf("report misses rolled marker: %.200q", rb.Report)
	}
}

// A never-matching gate stops the run at the failure with the impact text.
func TestCutoverGateFailureHolds(t *testing.T) {
	m := cutoverTestManager(t)
	pid := approvedVlanProposal(t, m)
	run, err := m.CutoverStart(&CutoverRun{
		Name:     "bad gate",
		Deadline: time.Now().Add(30 * time.Minute),
		Steps: []CutoverStep{
			{Label: "变更", ProposalID: pid, EstSec: 60, Impact: "OSPF 未收敛",
				Gate: &CutoverGate{Device: "sw1", Command: "display version", Expect: "NX-OS", SustainSec: 1, TimeoutSec: 2}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	held := waitCutover(t, run.ID, CutoverHold, "验证门未过")
	if !strings.Contains(held.HoldNote, "OSPF 未收敛") {
		t.Fatalf("hold note = %q — impact text missing", held.HoldNote)
	}
}

// The total countdown is a wall: continuing past the deadline refuses to run
// more steps.
func TestCutoverCountdownExhausted(t *testing.T) {
	m := cutoverTestManager(t)
	run, err := m.CutoverStart(&CutoverRun{
		Name:     "tight window",
		Deadline: time.Now().Add(2 * time.Second),
		Steps: []CutoverStep{
			{Label: "一步", Device: "sw1", Command: "display version", EstSec: 30, DecisionPoint: true, Impact: "检查点"},
			{Label: "下一步", Device: "sw1", Command: "display version", EstSec: 30},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitCutover(t, run.ID, CutoverHold, "决策点")
	time.Sleep(2200 * time.Millisecond) // let the window lapse
	if _, err := m.CutoverContinue(run.ID); err != nil {
		t.Fatalf("continue: %v", err)
	}
	held := waitCutover(t, run.ID, CutoverHold, "倒计时")
	if held.Steps[1].Status != CutoverStepPending {
		t.Fatalf("step 2 ran past the deadline: %+v", held.Steps[1])
	}
}

// Runbooks referencing unapproved proposals never start.
func TestCutoverRequiresApprovedProposals(t *testing.T) {
	m := cutoverTestManager(t)
	p := &Proposal{Intent: "draft only", Steps: []ProposalStep{{Device: "sw1", Commands: []string{"vlan 100"}, Rollback: []string{"undo vlan 100"}}}}
	SaveProposal(p)
	if _, err := m.CutoverStart(&CutoverRun{
		Name: "bad", Deadline: time.Now().Add(time.Hour),
		Steps: []CutoverStep{{Label: "x", ProposalID: p.ID}},
	}); err == nil || !strings.Contains(err.Error(), "approve") {
		t.Fatalf("unapproved proposal accepted: %v", err)
	}
	if _, err := m.CutoverStart(&CutoverRun{
		Name: "bad", Deadline: time.Now().Add(time.Hour),
		Steps: []CutoverStep{{Label: "x", Device: "sw1"}},
	}); err == nil {
		t.Fatal("empty step accepted")
	}
}
