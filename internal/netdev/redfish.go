package netdev

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/zzycxz/hiq/internal/netdev/driver"
	"strings"
	"time"
)

// redfish.go — the out-of-band hardware channel (DMTF Redfish over BMC):
// temperature/fans/power, inventory, and the System Event Log. The BMC answers
// when the OS and the network stack are both dead — the last row of evidence.
//
// The seal here is STRONGER than the CLI classifier's: this client implements
// GET and only GET — there is no POST/PATCH/DELETE code path to leak through —
// and the URL path must match the read-only resource allowlist below (same
// doctrine as the CLI read tables: user-extendable later, never agent-decided).
// Guardrails (device-group scope, per-turn budget) and audit apply unchanged.

// redfishReadPaths is the resource allowlist: a request path (query stripped,
// leading /redfish/v1 optional) is readable when it EQUALS one of these or
// continues with "/" beneath one of them. Member URIs discovered from
// collections (e.g. /redfish/v1/Chassis/1/Thermal) inherit the collection's
// allowance.
var redfishReadPaths = []string{
	"",               // the service root itself
	"/Systems",       // compute: inventory, power state, boot, memory summaries
	"/Chassis",       // enclosures: thermal, power, network adapters
	"/Managers",      // the BMC's own identity, firmware, EthernetInterfaces
	"/TaskService",   // read-only task status (long ops started elsewhere)
	"/EventService",  // event subscription visibility (read-only view)
	"/Registries",    // message registries (decoding SEL entries)
	"/UpdateService", // firmware inventory view
	"/Sessions",      // session listing is manager-visible diagnostics
}

// redfishPathAllowed reports whether a Redfish request path is inside the
// read-only allowlist. Pure function — unit-tested.
func redfishPathAllowed(rawPath string) bool {
	p := strings.SplitN(rawPath, "?", 2)[0]
	p = strings.TrimPrefix(p, "/redfish/v1")
	p = strings.TrimSuffix(p, "/")
	if p == "/redfish" || p == "" {
		return true // service root
	}
	// Deny-first overrides (the CLI drivers' dangerous-before-read ordering):
	// Actions are the mutation surface wherever they appear, and subscription
	// management mutates receiver lists.
	if strings.Contains(p, "/Actions/") || strings.HasSuffix(p, "/Actions") {
		return false
	}
	if p == "/EventService/Subscriptions" || strings.HasPrefix(p, "/EventService/Subscriptions/") {
		return false
	}
	for _, prefix := range redfishReadPaths {
		if prefix == "" {
			continue
		}
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// RedfishGet performs ONE read-only GET against a device's BMC and returns
// the JSON body (redacted). vendor=redfish devices carry the BMC address and
// a passwordEnv-resolved credential; the TLS skip is deliberate — BMC
// certificates are almost universally self-signed, and the channel carries
// read-only inventory data.
func (m *Manager) RedfishGet(ctx context.Context, deviceName, path string) (string, error) {
	if strings.ContainsAny(path, " \t\r\n") {
		return "", fmt.Errorf("redfish: path must not contain whitespace")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// [netdev.guardrails] gate: same per-ask controls as every other read.
	if r, allow := m.guardrailCheck(deviceName, "GET "+path); !allow {
		b, _ := json.Marshal(r.Refusal)
		return string(b), nil
	}
	device, ok := m.cfg.NetDevDeviceByName(deviceName)
	if !ok {
		return "", fmt.Errorf("redfish: device %q is not in the inventory", deviceName)
	}
	if !strings.EqualFold(device.Vendor, "redfish") {
		return "", fmt.Errorf("redfish: device %q is vendor %q — only redfish devices take this path", deviceName, device.Vendor)
	}
	if !redfishPathAllowed(path) {
		_ = AppendAudit(Audit{Device: deviceName, Command: "GET " + path, Class: "guardrail", Status: AuditRefused, OutputBytes: 0})
		return "", fmt.Errorf("redfish: path %q is outside the read-only resource allowlist (Systems/Chassis/Managers/TaskService/Registries/UpdateService/EventService)", path)
	}

	password := ""
	if device.PasswordEnv != "" {
		if v, ok, _ := secretGetter(SecretKindPassword, device.PasswordEnv); ok {
			password = v
		}
	}
	port := device.Port
	if port == 0 {
		port = 443
	}
	url := fmt.Sprintf("https://%s%s", joinHostPort(device.Address, port), path)

	hctx, hcancel := context.WithTimeout(ctx, 20*time.Second)
	defer hcancel()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(device.Username, password)
	req.Header.Set("Accept", "application/json")
	// GET-only client by construction: this Transport never sees another verb.
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	resp, err := client.Do(req)
	if err != nil {
		m.audit(device, "GET "+path, driver.Read, AuditFailure, 0, err)
		return "", fmt.Errorf("redfish: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	status := AuditOK
	if resp.StatusCode >= 400 {
		status = AuditDeviceError
	}
	m.turnSpend()
	out, n := RedactCounted(string(body))
	m.audit(device, "GET "+path, driver.Read, status, len(out), nil)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("redfish: HTTP %d: %.200s", resp.StatusCode, out)
	}
	if n > 0 {
		out += fmt.Sprintf("\n\n[安全提醒] 输出中 %d 处敏感字段已脱敏后才进入上下文与审计。", n)
	}
	return out, nil
}

func joinHostPort(host string, port int) string {
	if strings.Contains(host, ":") && !strings.Contains(host, "]") {
		return fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}
