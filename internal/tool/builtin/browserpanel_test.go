package builtin

import (
	"testing"

	"github.com/chromedp/cdproto/input"
)

const (
	inputMouseButtonLeft   = input.Left
	inputMouseButtonMiddle = input.Middle
	inputMouseButtonRight  = input.Right
)

// The interactive panel's kernel surface is mostly thin CDP wiring; what is
// unit-testable without a browser is the stream-state lifecycle (lazy init,
// idempotent start/stop, visibility flow control) — exercised here.

func TestPanelStreamLazyInitAndIdempotence(t *testing.T) {
	s := &browserSession{id: "panel-test"}
	if s.panelStream.Load() != nil {
		t.Fatal("panel stream state must start nil")
	}
	st := panelStream(s)
	if st == nil {
		t.Fatal("panelStream must lazily create state")
	}
	if again := panelStream(s); again != st {
		t.Fatal("panelStream must return the same state instance")
	}
	if st.active {
		t.Fatal("stream must start inactive")
	}
	// stopLocked on an inactive stream is a no-op (no panic, stays inactive).
	st.mu.Lock()
	st.stopLocked()
	st.mu.Unlock()
	if st.active {
		t.Fatal("stop on inactive stream must stay inactive")
	}
}

func TestSetPanelStreamingWithoutSessions(t *testing.T) {
	// No sink and no sessions: must be a safe no-op (desktop calls this on
	// panel mount before any browser_open).
	SetPanelStreaming(true)
	SetPanelStreaming(false)
	if panelStreamDefaultVisible.Load() {
		t.Fatal("default visibility must follow the last SetPanelStreaming call")
	}
	SetPanelStreaming(true)
	if !panelStreamDefaultVisible.Load() {
		t.Fatal("default visibility must follow the last SetPanelStreaming call")
	}
}

func TestPanelMouseButtonMapping(t *testing.T) {
	if got := panelMouseButton("right"); got != inputMouseButtonRight {
		t.Errorf("right → %v", got)
	}
	if got := panelMouseButton("MIDDLE"); got != inputMouseButtonMiddle {
		t.Errorf("MIDDLE → %v", got)
	}
	if got := panelMouseButton(""); got != inputMouseButtonLeft {
		t.Errorf("default → %v", got)
	}
}

func TestPanelNamedKeysCoverEssentials(t *testing.T) {
	for _, k := range []string{"Enter", "Backspace", "Escape", "ArrowDown", "Tab", "F5"} {
		if _, ok := panelNamedKeys[k]; !ok {
			t.Errorf("missing named key %q", k)
		}
	}
}
