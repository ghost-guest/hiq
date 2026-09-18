package netdev

import (
	"context"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// The allowlist is the seal's face for Redfish: pure function, pinned by tests
// on both sides — every read-only resource family passes, every mutation or
// credential-bearing path fails.
func TestRedfishPathAllowed(t *testing.T) {
	allowed := []string{
		"/redfish/v1",
		"/redfish/v1/",
		"/redfish/v1/Systems",
		"/redfish/v1/Systems/1",
		"/redfish/v1/Systems/1/Memory",
		"/redfish/v1/Chassis/1/Thermal",
		"/redfish/v1/Chassis/1/Power",
		"/redfish/v1/Managers/1/EthernetInterfaces",
		"/redfish/v1/Managers/1/LogServices/SEL/Entries",
		"/Systems",
		"/redfish/v1/TaskService/Tasks",
		"/redfish/v1/Registries",
		"/redfish/v1/UpdateService/FirmwareInventory",
	}
	for _, p := range allowed {
		if !redfishPathAllowed(p) {
			t.Errorf("path %q should be allowed", p)
		}
	}
	refused := []string{
		// Actions are mutations — never readable, never callable.
		"/redfish/v1/Systems/1/Actions/ComputerSystem.Reset",
		"/redfish/v1/Managers/1/VirtualMedia/1/Actions/VirtualMedia.InsertMedia",
		// Account service: credentials live there.
		"/redfish/v1/AccountService/Accounts",
		"/redfish/v1/AccountService",
		// Sessions POST is how you log in; even GET is manager territory.
		"/redfish/v1/SessionService/Sessions/1",
		// Event subscriptions mutate receiver lists.
		"/redfish/v1/EventService/Subscriptions/1",
		"/redfish/v1/anythingelse",
	}
	for _, p := range refused {
		if redfishPathAllowed(p) {
			t.Errorf("path %q must be refused", p)
		}
	}
}

// Out-of-allowlist and wrong-vendor requests refuse BEFORE any dial: the
// allowlist gate runs ahead of the HTTP client, and refusals land in the audit.
func TestRedfishGetRefusedBeforeDial(t *testing.T) {
	m, auditPath := testManager(t, startSimDevice(t))

	if _, err := m.RedfishGet(context.Background(), "sw1", "/redfish/v1/Systems"); err == nil {
		t.Fatal("non-redfish device accepted a Redfish query")
	} else if !strings.Contains(err.Error(), "vendor") {
		t.Fatalf("unexpected error: %v", err)
	}
	// sw1 is huawei — vendor gate fires before the allowlist; now use a
	// redfish-flavored entry by pointing the dead device's vendor through a
	// direct config tweak on the same manager.
	cfg := *m.cfg
	cfg.NetDev.Devices = append(cfg.NetDev.Devices, config.NetDevDevice{
		Name: "bmc-1", Vendor: "redfish", OS: "bmc",
		Address: "127.0.0.1", Port: 1, Username: "root", PasswordEnv: "TEST_ENV",
	})
	m.cfg = &cfg

	if _, err := m.RedfishGet(context.Background(), "bmc-1", "/redfish/v1/AccountService/Accounts"); err == nil {
		t.Fatal("out-of-allowlist path accepted")
	} else if !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("unexpected error: %v", err)
	}
	entries := readAudit(t, auditPath)
	found := false
	for _, e := range entries {
		if e.Device == "bmc-1" && e.Class == "guardrail" && e.Status == AuditRefused {
			found = true
		}
	}
	if !found {
		t.Fatal("refused Redfish path left no guardrail audit row")
	}
}
