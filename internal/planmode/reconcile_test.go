package planmode_test

import (
	"testing"

	"github.com/zzycxz/hiq/internal/planmode"
	"github.com/zzycxz/hiq/internal/tool"

	_ "github.com/zzycxz/hiq/internal/tool/builtin"
)

func TestBuiltinPhaseClassifiersMatchPolicy(t *testing.T) {
	builtins := tool.Builtins()
	if len(builtins) == 0 {
		t.Fatal("tool.Builtins() is empty")
	}
	for _, tl := range builtins {
		safety := planmode.PlanSafetyUnknown
		if classifier, ok := tl.(tool.PlanModeClassifier); ok {
			if classifier.PlanModeSafe() {
				safety = planmode.PlanSafetySafe
			} else {
				safety = planmode.PlanSafetyUnsafe
			}
		}
		got := (planmode.Policy{}).Decide(planmode.Call{
			Name:     tl.Name(),
			ReadOnly: tl.ReadOnly(),
			Safety:   safety,
		})
		if got.Blocked != (safety == planmode.PlanSafetyUnsafe) {
			t.Errorf("builtin %q safety=%v decision=%+v", tl.Name(), safety, got)
		}
	}
}

// TestCompleteStepRetainedAsHiqBuiltin documents a deliberate divergence.
//
// Upstream DeepSeek-Reasonix retired the complete_step builtin (its Name() now
// returns an error telling the model to use todo_write instead), so upstream
// asserts the tool is no longer discoverable. hiq keeps a fully functional
// complete_step: it validates a step receipt against host evidence and advances
// the todo ledger, and the agent loop still emits "task list advanced by
// complete_step" (internal/agent/agent.go). Removing it would drop a hiq
// capability, so the tool stays registered and discoverable.
//
// This test guards that intent: if a future merge silently deletes hiq's
// complete_step, it fails loudly instead of letting the capability vanish.
func TestCompleteStepRetainedAsHiqBuiltin(t *testing.T) {
	tl, ok := tool.LookupBuiltin("complete_step")
	if !ok {
		t.Fatal("hiq intentionally retains the complete_step builtin; it is now missing")
	}
	if !tl.ReadOnly() {
		t.Fatal("complete_step must stay ReadOnly so it remains available without approval")
	}
	if got := (planmode.Policy{}).Decide(planmode.Call{Name: "complete_step", ReadOnly: tl.ReadOnly(), Safety: planmode.PlanSafetyUnsafe}); !got.Blocked {
		t.Fatalf("complete_step phase opt-out must remain enforced in plan mode, got %+v", got)
	}
}
