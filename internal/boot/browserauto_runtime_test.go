package boot

import (
	"testing"

	"github.com/zzycxz/hiq/internal/tool/builtin"
)

func TestLastAutoAction(t *testing.T) {
	steps := []builtin.BrowserAutoStep{
		{Type: "thought", Step: 1, Text: "thinking about the page"},
		{Type: "action", Step: 1, Text: "goto https://example.com"},
		{Type: "screenshot", Step: 1},
		{Type: "thought", Step: 2, Text: "need to click"},
		{Type: "action", Step: 2, Text: "click('Login')"},
		{Type: "screenshot", Step: 2},
	}
	if got := lastAutoAction(steps); got != "click('Login')" {
		t.Errorf("expected latest action, got %q", got)
	}
	if got := lastAutoAction(nil); got != "" {
		t.Errorf("expected empty for no steps, got %q", got)
	}
	if got := lastAutoAction([]builtin.BrowserAutoStep{{Type: "thought", Text: "only a thought"}}); got != "" {
		t.Errorf("expected empty with no actions, got %q", got)
	}
}
