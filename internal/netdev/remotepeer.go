package netdev

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zzycxz/hiq/internal/trustdomain"
	"github.com/zzycxz/hiq/internal/trustdomain/nettrans"
)

// RemoteWorkHandler adapts netdev's read-only diagnostics onto the trust
// domain's delegated-work surface (TRUSTDOMAIN_SPEC §7.3 as-built) — the
// first real WorkHandler consumer. A fleet member holding a scoped token
// can pull this host's health board or run a host-triage battery on one of
// this host's OWN managed devices; everything flows the same read-only
// pipeline (guardrails, audit, refusal ledger) as local agent use.
//
// 能力隔离 (spec §1.4 #7): there is simply no write operation on this
// path — the vocabulary below cannot express one, and Serve's §7.3 gate
// runs before any of this code.
//
// Resource/operation vocabulary:
//
//	netdev/health  read                      → HealthSnapshot JSON
//	netdev/triage  read  {"device":"name"}   → TriageReport JSON
func (m *Manager) RemoteWorkHandler() nettrans.WorkHandler {
	return func(_ *trustdomain.Node, del *trustdomain.Delegation, payload []byte) ([]byte, error) {
		if del.Operation != "read" {
			return nil, fmt.Errorf("netdev executor is read-only (op %q refused)", del.Operation)
		}
		switch del.Resource {
		case "netdev/health":
			return json.Marshal(m.HealthSnapshot())
		case "netdev/triage":
			var p struct {
				Device string `json:"device"`
			}
			if err := json.Unmarshal(payload, &p); err != nil || p.Device == "" {
				return nil, fmt.Errorf(`triage payload must be {"device":"<inventory name>"}`)
			}
			return json.Marshal(m.Triage(context.Background(), p.Device))
		default:
			return nil, fmt.Errorf("unknown netdev resource %q (available: netdev/health, netdev/triage)", del.Resource)
		}
	}
}
