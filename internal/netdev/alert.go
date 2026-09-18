package netdev

// alert.go — the alert-rule engine (P2): threshold rules over the SNMP health
// snapshot evaluated on every poll. A firing rule creates ONE active Finding
// (deduped by source); when the condition clears, the same Finding flips to
// resolved. Human/AI findings are untouched (empty source/status).

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

// ruleCmp evaluates value op threshold.
func ruleCmp(value, threshold int64, op string) bool {
	if op == "" {
		op = ">="
	}
	switch op {
	case ">=":
		return value >= threshold
	case "<=":
		return value <= threshold
	case "==":
		return value == threshold
	}
	return false
}

// ruleMetricValue extracts a rule's metric from one device's health.
func ruleMetricValue(metric string, h DeviceHealth, prevUptime int64) int64 {
	switch metric {
	case "reachable":
		if h.Reachable {
			return 1
		}
		return 0
	case "if_down_count":
		return int64(h.IfDown())
	case "flap_count":
		// Reachability up↔down transitions in the last hour — a flapping
		// device answers most polls yet is clearly unhealthy.
		return int64(FlapCount(h.Device, time.Hour))
	case "if_down_above_p90":
		// Current down interfaces minus the historical 90th percentile:
		// "worse than usual" instead of a static threshold. Thin history →
		// no baseline → 0, never fires.
		p90, ok := P90IfDown(h.Device, 24*time.Hour)
		if !ok {
			return 0
		}
		v := int64(h.IfDown() - p90)
		if v < 0 {
			return 0
		}
		return v
	case "uptime_reset":
		// A reboot: uptime dropped since the previous poll (and the device
		// is still up). prevUptime 0 = no baseline yet → never fire.
		if prevUptime > 0 && h.Reachable && h.UptimeSec > 0 && h.UptimeSec < prevUptime {
			return 1
		}
		return 0
	}
	return 0
}

// prevUptimes carries the previous poll's uptime per device (the uptime_reset
// baseline) across polls.
var prevUptimes = map[string]int64{}

// evaluateAlerts runs every enabled rule against the fresh poll results and
// updates the uptime baseline. Called at the end of PollHealthOnce.
func (m *Manager) evaluateAlerts(fresh map[string]DeviceHealth) {
	rules := m.cfg.NetDev.AlertRules
	if len(rules) == 0 {
		for name, h := range fresh {
			prevUptimes[name] = h.UptimeSec
		}
		return
	}
	active, _ := m.activeFindingsBySource()
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		for name, h := range fresh {
			src := "alert:" + r.Name + ":" + name
			v := ruleMetricValue(r.Metric, h, prevUptimes[name])
			fired := ruleCmp(v, r.Value, r.Op)
			_, hasActive := active[src]
			switch {
			case fired && !hasActive:
				m.fireAlertFinding(src, r, name, h, v)
				active[src] = "new"
			case !fired && hasActive:
				m.resolveFindingBySource(src)
				delete(active, src)
			}
		}
	}
	for name, h := range fresh {
		prevUptimes[name] = h.UptimeSec
	}
}

func (m *Manager) fireAlertFinding(src string, r config.NetDevAlertRule, device string, h DeviceHealth, v int64) {
	sev := r.Severity
	if sev == "" {
		sev = SeverityWarning
	}
	detail := fmt.Sprintf("规则「%s」：%s %s %d（当前值 %d）。", r.Name, r.Metric, opLabel(r.Op), r.Value, v)
	ev := Evidence{Device: device, Command: "SNMP 健康轮询", Output: fmt.Sprintf("reachable=%v uptime=%ds ifUp=%d ifDown=%d lastError=%s", h.Reachable, h.UptimeSec, h.IfUp(), h.IfDown(), h.LastError)}
	f := &Finding{
		Title:    fmt.Sprintf("[告警] %s @ %s", ruleTitle(r.Metric), device),
		Severity: sev,
		Devices:  []string{device},
		Detail:   detail,
		Evidence: []Evidence{ev},
		Source:   src,
		Status:   "active",
	}
	_ = SaveFinding(f)
	notifyHealth(h) // nudge the UI (findings + health cards refresh)
}

func opLabel(op string) string {
	if op == "" {
		return ">="
	}
	return op
}

func ruleTitle(metric string) string {
	switch metric {
	case "reachable":
		return "设备不可达"
	case "if_down_count":
		return "接口掉线"
	case "flap_count":
		return "链路抖动（一小时翻转）"
	case "if_down_above_p90":
		return "掉线口数偏离基线（>P90）"
	case "uptime_reset":
		return "设备重启（uptime 回绕）"
	}
	return metric
}

// activeFindingsBySource lists auto-findings still active, keyed by source.
func (m *Manager) activeFindingsBySource() (map[string]string, error) {
	fs, err := ListFindings()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, f := range fs {
		if f.Source != "" && f.Status != "resolved" {
			out[f.Source] = f.ID
		}
	}
	return out, nil
}

// ResolveFindingByID manually resolves one finding (the 发现 card's button).
func ResolveFindingByID(id string) error {
	fs, err := ListFindings()
	if err != nil {
		return err
	}
	for _, f := range fs {
		if f.ID == id && f.Status != "resolved" {
			now := time.Now()
			f.Status = "resolved"
			f.ResolvedAt = &now
			StateEventSnap(StateEventFindResolve, id, StateActorUser, filepath.Join(FindingsDir(), id+".json"))
			return SaveFinding(f)
		}
	}
	return nil
}

// resolveFindingBySource rewrites one auto-finding as resolved.
func (m *Manager) resolveFindingBySource(src string) {
	fs, err := ListFindings()
	if err != nil {
		return
	}
	for _, f := range fs {
		if f.Source == src && f.Status != "resolved" {
			now := time.Now()
			f.Status = "resolved"
			f.ResolvedAt = &now
			if f.Detail != "" && !strings.Contains(f.Detail, "已恢复") {
				f.Detail += "（条件已清除，自动恢复）"
			}
			_ = SaveFinding(f)
			return
		}
	}
}
