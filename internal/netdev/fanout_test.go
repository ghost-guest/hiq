package netdev

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// fanout_test — completion-spec §6 #9：netdev_fanout 的作用域过滤与表格化。
// 逐设备执行走 Exec 的既有密封测试（tools_test.go），这里只钉扇出层的
// 目标选择契约。

// fanoutTestCfg mirrors testManager's device table (same sim addresses) so
// the tool's cfg-side filtering can be exercised without a second Manager.
func fanoutTestCfg(t *testing.T, sim *simDevice) *config.Config {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(sim.addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	cfg := config.Default()
	cfg.NetDev = config.NetDevConfig{
		Enabled: true,
		Devices: []config.NetDevDevice{
			{Name: "sw1", Vendor: "huawei", OS: "vrp8", Address: host, Port: port, Username: "admin", PasswordEnv: "TEST_ENV"},
			{Name: "dead", Vendor: "huawei", OS: "vrp8", Address: "127.0.0.1", Port: 1, Username: "admin", PasswordEnv: "TEST_ENV"},
		},
	}
	return cfg
}

func TestFanoutAcrossDevices(t *testing.T) {
	sim := startSimDevice(t)
	m, _ := testManager(t, sim)
	tool := &fanoutTool{m: m, cfg: fanoutTestCfg(t, sim)}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"display version"}`))
	if err != nil {
		t.Fatalf("fanout: %v", err)
	}
	var rows []struct {
		Device string `json:"device"`
		Class  string `json:"class"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("fanout output not tabulated: %v\n%s", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows (sw1 + dead), got %d: %s", len(rows), out)
	}
	byDev := map[string]string{}
	for _, r := range rows {
		byDev[r.Device] = r.Output
	}
	if !strings.Contains(byDev["sw1"], "Version") {
		t.Fatalf("sw1 row missing device output: %q", byDev["sw1"])
	}
	if byDev["dead"] == "" {
		t.Fatalf("dead row should still tabulate its refusal/error, got empty")
	}
}

func TestFanoutScopeAndGroupFilters(t *testing.T) {
	sim := startSimDevice(t)
	m, _ := testManager(t, sim)
	cfg := fanoutTestCfg(t, sim)
	cfg.NetDev.Guardrails.AllowedGroups = []string{"core"}
	tool := &fanoutTool{m: m, cfg: cfg}

	// All devices are outside the allowed groups → invisible to the fanout.
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"display version"}`))
	if err != nil {
		t.Fatalf("fanout: %v", err)
	}
	if !strings.Contains(out, "no matching devices") {
		t.Fatalf("expected scope refusal text, got %q", out)
	}

	// Explicit device list narrows to just that device.
	tool.cfg = fanoutTestCfg(t, sim)
	out, err = tool.Execute(context.Background(), json.RawMessage(`{"command":"display version","devices":["sw1"]}`))
	if err != nil {
		t.Fatalf("fanout: %v", err)
	}
	if !strings.Contains(out, `"sw1"`) || strings.Contains(out, `"dead"`) {
		t.Fatalf("device filter not applied: %s", out)
	}

	// Missing command is an argument error.
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("empty command should error")
	}
}
