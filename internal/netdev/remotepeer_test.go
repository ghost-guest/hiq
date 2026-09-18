package netdev

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/trustdomain"
)

func TestRemoteWorkHandler(t *testing.T) {
	m := NewManager(&config.Config{})
	h := m.RemoteWorkHandler()

	// Health board: read succeeds and returns snapshot JSON.
	out, err := h(nil, &trustdomain.Delegation{Resource: "netdev/health", Operation: "read"}, []byte("{}"))
	if err != nil {
		t.Fatalf("health read: %v", err)
	}
	var snap map[string]any
	if err := json.Unmarshal(out, &snap); err != nil {
		t.Fatalf("health output not JSON: %v", err)
	}

	// Read-only by construction: any non-read op refused.
	if _, err := h(nil, &trustdomain.Delegation{Resource: "netdev/health", Operation: "write"}, []byte("{}")); err == nil ||
		!strings.Contains(err.Error(), "read-only") {
		t.Fatalf("write op not refused: %v", err)
	}

	// Unknown resource refused with the vocabulary listed.
	if _, err := h(nil, &trustdomain.Delegation{Resource: "netdev/reboot", Operation: "read"}, []byte("{}")); err == nil ||
		!strings.Contains(err.Error(), "netdev/health") {
		t.Fatalf("unknown resource not refused helpfully: %v", err)
	}

	// Triage on a device not in the inventory returns the report's
	// not-inventory summary (not an error — the report is the result).
	out, err = h(nil, &trustdomain.Delegation{Resource: "netdev/triage", Operation: "read"}, []byte(`{"device":"nope"}`))
	if err != nil {
		t.Fatalf("triage read: %v", err)
	}
	var rep TriageReport
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("triage output not a report: %v", err)
	}
	if !strings.Contains(rep.Summary, "not in the inventory") && !strings.Contains(rep.Summary, "inventory") {
		t.Fatalf("unexpected triage summary: %q", rep.Summary)
	}

	// Malformed triage payload refused with usage.
	if _, err := h(nil, &trustdomain.Delegation{Resource: "netdev/triage", Operation: "read"}, []byte(`{}`)); err == nil {
		t.Fatal("empty triage payload accepted")
	}
}
