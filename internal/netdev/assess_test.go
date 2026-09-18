package netdev

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

func assessTestManager(t *testing.T, sim *simDevice) *Manager {
	t.Helper()
	m, _ := testManager(t, sim)
	m.cfg.NetDev.Assessment = config.NetDevAssessment{
		EngagementID: "ASSESS-TEST-1",
		Scopes:       []string{"127.0.0.0/8"},
		Expires:      time.Now().AddDate(0, 0, 7).Format("2006-01-02"),
		Approver:     "tester",
	}
	return m
}

func TestAssessmentGate(t *testing.T) {
	if err := AssessmentActive(config.NetDevConfig{}); err == nil {
		t.Fatal("no envelope accepted")
	}
	expired := config.NetDevConfig{Assessment: config.NetDevAssessment{
		EngagementID: "X", Expires: "2020-01-01"}}
	if err := AssessmentActive(expired); err == nil || err.Error() == "" {
		t.Fatal("expired accepted")
	}
	ok := config.NetDevConfig{Assessment: config.NetDevAssessment{
		EngagementID: "X", Expires: time.Now().AddDate(0, 0, 1).Format("2006-01-02")}}
	if err := AssessmentActive(ok); err != nil {
		t.Fatalf("valid rejected: %v", err)
	}
}

func TestWeakCredBasicNoFalsePositive(t *testing.T) {
	sim := startSimDevice(t) // real password "pw" — NOT in the basic set
	m := assessTestManager(t, sim)

	res, err := m.WeakCredCheck(context.Background(), "sw1", WeakTierBasic, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Weak {
		t.Fatal("false positive: basic tier confirmed a non-basic password")
	}
	if res.Attempts > weakBudgetBasic {
		t.Fatalf("budget exceeded: %d", res.Attempts)
	}
}

func TestWeakCredDictionaryFindsWeak(t *testing.T) {
	sim := startSimDevice(t)
	m := assessTestManager(t, sim)

	dict := filepath.Join(t.TempDir(), "dict.txt")
	if err := os.WriteFile(dict, []byte("firstguess\nsecondguess\npw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := m.WeakCredCheck(context.Background(), "sw1", WeakTierDict, dict)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Weak || res.Attempts != 3 {
		t.Fatalf("res = %+v, want weak on attempt 3", res)
	}
}

func TestWeakCredRequiresEngagement(t *testing.T) {
	sim := startSimDevice(t)
	m, _ := testManager(t, sim) // no assessment envelope
	if _, err := m.WeakCredCheck(context.Background(), "sw1", WeakTierBasic, ""); err == nil {
		t.Fatal("weak-cred without engagement accepted")
	}
}

func TestWeakCredDictBudgetCap(t *testing.T) {
	sim := startSimDevice(t)
	m := assessTestManager(t, sim)
	dict := filepath.Join(t.TempDir(), "big.txt")
	var body string
	for i := 0; i < 30; i++ {
		body += "guess" + string(rune('a'+i)) + "\n"
	}
	if err := os.WriteFile(dict, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := m.WeakCredCheck(context.Background(), "sw1", WeakTierDict, dict)
	if err != nil {
		t.Fatal(err)
	}
	if res.Attempts > weakBudgetDictMax {
		t.Fatalf("hard cap exceeded: %d attempts", res.Attempts)
	}
}

// The agent tool surface (netdev_assess) refuses without an engagement
// envelope BEFORE any dial, and the refusal lands as a VISIBLE live event —
// the 操作实况 panel must show the agent being stopped.
func TestAssessToolRefusesWithoutEnvelope(t *testing.T) {
	sim := startSimDevice(t)
	m, _ := testManager(t, sim) // no assessment envelope

	col := &liveCollector{}
	m.SetLiveObserver(col.on)

	tool := &assessTool{m: m}
	out, err := tool.Execute(context.Background(), []byte(`{"device":"sw1"}`))
	if err == nil {
		t.Fatalf("tool must refuse without an envelope; got output %q", out)
	}
	if !strings.Contains(err.Error(), "engagement") {
		t.Fatalf("tool error = %v, want engagement-envelope refusal", err)
	}
	col.waitKind(t, LiveCmdRefused, 1)

	// With the envelope, the tool runs the sealed path end to end and
	// brackets it with live lifecycle events.
	m.cfg.NetDev.Assessment = config.NetDevAssessment{
		EngagementID: "ASSESS-TOOL-TEST",
		Expires:      time.Now().AddDate(0, 0, 1).Format("2006-01-02"),
		Approver:     "tester",
	}
	out, err = tool.Execute(context.Background(), []byte(`{"device":"sw1","tier":"basic"}`))
	if err != nil {
		t.Fatalf("tool with envelope: %v", err)
	}
	if !strings.Contains(out, "no weak credential") {
		t.Fatalf("tool output = %q", out)
	}
	col.waitKind(t, LiveCmdStart, 1)
	col.waitKind(t, LiveCmdEnd, 1)
}
