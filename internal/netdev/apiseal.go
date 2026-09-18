package netdev

// apiseal.go — the sealed wrapper for the kind=docker / k8s / firewall API
// channels (NETDEV_SPEC_V2 §2). The GET whitelists are the structural seal;
// this adds the operating seal every other channel already has: guardrails
// (group scope + per-turn budget), live events, audit, and redaction before
// the body crosses into the model context (docker inspect Config.Env and
// pod/container logs are secret-dense surfaces).

import (
	"fmt"

	"github.com/zzycxz/hiq/internal/netdev/driver"
)

// sealAPIGet runs one whitelisted API GET under the full netdev seal.
func (m *Manager) sealAPIGet(deviceName, label string, fn func() (string, error)) (string, error) {
	d, _ := m.cfg.NetDevDeviceByName(deviceName)
	if r, allow := m.guardrailCheck(deviceName, label); !allow {
		m.liveCmdRefused(deviceName, label, "guardrail", r.Refusal)
		return r.Refusal, nil
	}
	start := m.liveCmdStart(deviceName, label, "read")
	status := AuditFailure
	defer func() { m.liveCmdEnd(deviceName, label, "read", status, start, 0, "") }()
	out, err := fn()
	if err != nil {
		m.audit(d, label, driver.Read, AuditFailure, len(out), err)
		return out, err
	}
	m.turnSpend()
	red, n := RedactCounted(out)
	m.audit(d, label, driver.Read, AuditOK, len(red), nil)
	status = AuditOK
	if n > 0 {
		red += fmt.Sprintf("\n[安全提醒] %d 处敏感字段已脱敏后才进入上下文与审计。", n)
	}
	return red, nil
}
