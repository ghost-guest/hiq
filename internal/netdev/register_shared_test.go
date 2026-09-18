package netdev

import (
	"testing"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/tool"
)

// The agent's netdev tools and the desktop bridge must share ONE Manager:
// NetDevTurnBegin's budget reset and NetDevEmergencyStop's KillAllConnections
// only reach the tool side if RegisterTools hands out SharedManager. A private
// NewManager here was the split-brain that silently disabled both guardrails.
func TestRegisterToolsUsesSharedManager(t *testing.T) {
	sharedMu.Lock()
	saved := shared
	shared = nil
	sharedMu.Unlock()
	t.Cleanup(func() {
		sharedMu.Lock()
		shared = saved
		sharedMu.Unlock()
	})

	cfg := config.Default()
	reg := tool.NewRegistry()
	RegisterTools(reg, cfg)

	for _, name := range []string{"netdev_exec", "netdev_discover", "netdev_topology", "netdev_propose", "netdev_netconf", "netdev_baseline"} {
		got, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		var m *Manager
		switch tt := got.(type) {
		case *execTool:
			m = tt.m
		case *discoverTool:
			m = tt.m
		case *topologyTool:
			m = tt.m
		case *proposeTool:
			m = tt.m
		case *netconfTool:
			m = tt.m
		case *baselineTool:
			m = tt.m
		default:
			t.Fatalf("%s has unexpected tool type %T", name, got)
		}
		if m != SharedManager(cfg) {
			t.Fatalf("%s holds a private Manager — budget reset / emergency stop miss the agent path", name)
		}
	}
}
