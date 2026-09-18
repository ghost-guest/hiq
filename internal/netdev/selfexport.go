package netdev

// selfexport.go — 自身数据导出 v1（NETDEV_SPEC_V2 §5.6）：把运维面自己的
// 状态打成一份 JSON（不含任何密钥/密文——设备只带骨架字段）。导入端与
// secret 迁移（口令重加密）随 v1.1；现在先让"换机至少带走台账与历史结论"。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ExportState writes one JSON snapshot into the netdev state dir and returns
// its path. Devices carry skeleton fields ONLY — credentials never leave the
// secret store.
func (m *Manager) ExportState() (string, error) {
	type devLite struct {
		Name, Vendor, Kind, Address, Group string
		Via                                []string
	}
	type dbLite struct {
		Name, Type, Host string
		Port             int
		Via              []string
	}
	type findingLite struct {
		ID, Title, Severity string
		Devices             []string
		CreatedAt           time.Time
		Detail              string
	}
	snap := map[string]any{
		"exported_at": time.Now().Format(time.RFC3339),
		"product":     "hiq-netdev",
	}
	var devs []devLite
	for _, d := range m.cfg.NetDev.Devices {
		devs = append(devs, devLite{Name: d.Name, Vendor: d.Vendor, Kind: d.Kind, Address: d.Address, Group: d.Group, Via: d.Via})
	}
	var dbs []dbLite
	for _, s := range m.cfg.NetDev.DBSources {
		dbs = append(dbs, dbLite{Name: s.Name, Type: s.Type, Host: s.Host, Port: s.Port, Via: s.Via})
	}
	var fs []findingLite
	if list, err := ListFindings(); err == nil {
		for _, f := range list {
			d := f.Detail
			if len(d) > 500 {
				d = d[:500]
			}
			fs = append(fs, findingLite{ID: f.ID, Title: f.Title, Severity: f.Severity, Devices: f.Devices, CreatedAt: f.CreatedAt, Detail: d})
		}
	}
	snap["devices"] = devs
	snap["db_sources"] = dbs
	snap["findings"] = fs
	if tail := fileTail(seriesFile(), 200); len(tail) > 0 {
		snap["series_tail"] = tail
	}
	if tail := fileTail(AuditPath(), 100); len(tail) > 0 {
		snap["audit_tail"] = tail
	}
	body, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(netdevStateDir(), fmt.Sprintf("export-%s.json", time.Now().Format("20060102-150405")))
	if err := os.MkdirAll(netdevStateDir(), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func fileTail(path string, n int) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
