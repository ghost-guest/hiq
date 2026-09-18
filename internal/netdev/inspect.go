package netdev

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/netdev/driver"
)

// RunInspection sweeps every inventory device with a fixed read battery and
// files one Finding with the evidence — the manual form of 定时巡检 (the
// scheduler wiring lands with the jobs integration). Everything runs through
// the sealed read path (classifier + redaction + audit per command).
func (m *Manager) RunInspection(ctx context.Context) (*Finding, error) {
	if !m.cfg.NetDev.Enabled || len(m.cfg.NetDev.Devices) == 0 {
		return nil, fmt.Errorf("netdev disabled or no devices configured")
	}
	start := time.Now()
	var evidence []Evidence
	devices := make([]string, 0, len(m.cfg.NetDev.Devices))
	var problems []string

	for _, d := range m.cfg.NetDev.Devices {
		drv, ok := m.driverFor(d)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: no driver (%s/%s)", d.Name, d.Vendor, d.OS))
			continue
		}
		devices = append(devices, d.Name)
		for _, cmd := range inspectionBattery(drv) {
			res := m.Exec(ctx, d.Name, cmd)
			if res.Refused {
				problems = append(problems, fmt.Sprintf("%s: %s refused (%s)", d.Name, cmd, res.Class))
				continue
			}
			evidence = append(evidence, Evidence{Device: d.Name, Command: cmd, Output: res.Output})
			if res.IsError {
				problems = append(problems, fmt.Sprintf("%s: %s → device error", d.Name, cmd))
			}
		}
	}

	severity := SeverityInfo
	if len(problems) > 0 {
		severity = SeverityWarning
	}
	f := &Finding{
		Title:    fmt.Sprintf("巡检 %d 台设备，%d 项异常", len(devices), len(problems)),
		Severity: severity,
		Devices:  devices,
		Detail: fmt.Sprintf("battery ran %s–%s over %d devices; %d evidence items collected",
			start.Format("15:04:05"), time.Now().Format("15:04:05"), len(devices), len(evidence)),
		Evidence: evidence,
	}
	if len(problems) > 0 {
		f.Detail += "; problems: " + joinAll(problems, "; ")
	}
	if err := SaveFinding(f); err != nil {
		return nil, err
	}
	// R1 journal（best-effort）：巡检一行汇总 + 电池里 interface-brief 输出的
	// 行数启发式接口计数（趋势输入，不是逐口台账）。失败不影响巡检结果。
	crit, warn, info := OpenFindingTallies()
	ifBrief := map[string]IfBriefCounts{}
	for _, ev := range evidence {
		if ev.Device == "" || ev.Output == "" {
			continue
		}
		cl := strings.ToLower(ev.Command)
		if strings.Contains(cl, "interface brief") || strings.Contains(cl, "interfaces status") {
			c := SummarizeIfBrief(ev.Output)
			prev := ifBrief[ev.Device]
			ifBrief[ev.Device] = IfBriefCounts{Up: prev.Up + c.Up, Down: prev.Down + c.Down}
		}
	}
	_ = AppendInspectionRow(InspectionJournalRow{
		Kind: "inspection", Devices: len(devices), Checked: len(devices),
		Critical: crit, Warning: warn, Info: info, IfBrief: ifBrief,
	})
	return f, nil
}

// inspectionBattery is the per-driver read battery (all display/show-prefixed,
// so the classifier passes them).
func inspectionBattery(drv driver.Driver) []string {
	switch drv.Key() {
	case "huawei-vrp":
		return []string{
			"display version",
			"display cpu-usage",
			"display memory-usage",
			"display interface brief",
		}
	case "cisco-ios":
		return []string{
			"show version",
			"show processes cpu sorted | exclude 0.00",
			"show interfaces status",
		}
	case "zte-zxr10":
		return []string{
			"show version",
			"show processor cpu",
			"show interface brief",
		}
	default:
		return []string{"show version"}
	}
}

func joinAll(items []string, sep string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
