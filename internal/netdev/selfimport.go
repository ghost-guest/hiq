package netdev

// selfimport.go — 状态导入（§5.6 迁移向导 / completion-spec §6 #11）：读导出
// JSON → 待确认区（diff 预览：新增/冲突设备）→ 人工核对后合并。凭证永不
// 迁移——只迁清单骨架；历史 findings 只读引用不覆盖本地。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

// ImportPreview is the human-review diff for one import run.
type ImportPreview struct {
	// NewDevices would be ADDED (name not in current inventory).
	NewDevices []ImportDevice `json:"new_devices"`
	// ConflictDevices exist by name — their address/vendor may differ; the
	// user decides keep-local vs take-imported per device.
	ConflictDevices []ImportDeviceConflict `json:"conflict_devices"`
	// DBNew / DBOverride mirror the same split for db_sources.
	DBNew     []ImportDBSource `json:"db_new"`
	DBOverlap []string         `json:"db_overlap"`
	// SkippedCounts are informational: findings/series/audit tails are
	// import-read-only (the local audit chain stays append-only).
	FindingsSeen int `json:"findings_seen"`
	ExportedAt   string `json:"exported_at"`
	Source       string `json:"source"`
}

// ImportDevice is the skeleton from the export file.
type ImportDevice struct {
	Name    string   `json:"name"`
	Vendor  string   `json:"vendor"`
	Kind    string   `json:"kind"`
	Address string   `json:"address"`
	Group   string   `json:"group"`
	Via     []string `json:"via"`
}

// ImportDeviceConflict adds the local view for side-by-side comparison.
type ImportDeviceConflict struct {
	Imported ImportDevice `json:"imported"`
	Local    ImportDevice `json:"local"`
}

// ImportDBSource is a db_sources skeleton.
type ImportDBSource struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Host string `json:"host"`
	Port int    `json:"port"`
	Via  []string `json:"via"`
}

// importFile is the on-disk shape of ExportState's output (subset).
type importFile struct {
	ExportedAt string           `json:"exported_at"`
	Product    string           `json:"product"`
	Devices    []ImportDevice   `json:"devices"`
	DBSources  []ImportDBSource `json:"db_sources"`
	Findings   []struct {
		ID string `json:"id"`
	} `json:"findings"`
}

// ImportPreview reads an export file and builds the confirm-area diff.
// No mutation happens here — preview only.
func (m *Manager) ImportPreview(path string) (*ImportPreview, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取导出文件失败: %w", err)
	}
	var f importFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("解析导出 JSON 失败: %w", err)
	}
	if f.Product != "hiq-netdev" {
		return nil, fmt.Errorf("不是 hiq 运维导出文件（产品标识=%q）", f.Product)
	}
	pv := &ImportPreview{ExportedAt: f.ExportedAt, Source: path, FindingsSeen: len(f.Findings)}

	byName := map[string]config.NetDevDevice{}
	for _, d := range m.cfg.NetDev.Devices {
		byName[d.Name] = d
	}
	for _, imp := range f.Devices {
		if imp.Name == "" {
			continue
		}
		if local, exists := byName[imp.Name]; exists {
			pv.ConflictDevices = append(pv.ConflictDevices, ImportDeviceConflict{
				Imported: imp,
				Local: ImportDevice{Name: local.Name, Vendor: local.Vendor, Kind: local.Kind,
					Address: local.Address, Group: local.Group, Via: local.Via},
			})
			continue
		}
		pv.NewDevices = append(pv.NewDevices, imp)
	}

	dbNames := map[string]bool{}
	for _, s := range m.cfg.NetDev.DBSources {
		dbNames[s.Name] = true
	}
	for _, s := range f.DBSources {
		if s.Name == "" {
			continue
		}
		if dbNames[s.Name] {
			pv.DBOverlap = append(pv.DBOverlap, s.Name)
			continue
		}
		pv.DBNew = append(pv.DBNew, s)
	}
	return pv, nil
}

// ImportApply performs the reviewed merge: adds the named new devices and
// takes the imported side of the named conflicts. Credentials are NOT
// migrated — the skeleton lands with empty credential fields and the user
// re-enters them in settings (spec: 凭证不迁移).
func (m *Manager) ImportApply(path string, addNames, takeOvernames []string) (int, error) {
	pv, err := m.ImportPreview(path)
	if err != nil {
		return 0, err
	}
	add := map[string]bool{}
	for _, n := range addNames {
		add[n] = true
	}
	take := map[string]bool{}
	for _, n := range takeOvernames {
		take[n] = true
	}

	applied := 0
	for _, imp := range pv.NewDevices {
		if !add[imp.Name] {
			continue
		}
		m.cfg.NetDev.Devices = append(m.cfg.NetDev.Devices, config.NetDevDevice{
			Name: imp.Name, Vendor: imp.Vendor, Kind: imp.Kind,
			Address: imp.Address, Group: imp.Group, Via: imp.Via,
		})
		applied++
	}
	for _, c := range pv.ConflictDevices {
		if !take[c.Imported.Name] {
			continue
		}
		for i := range m.cfg.NetDev.Devices {
			if m.cfg.NetDev.Devices[i].Name == c.Imported.Name {
				d := &m.cfg.NetDev.Devices[i]
				d.Vendor, d.Kind, d.Address, d.Group, d.Via =
					c.Imported.Vendor, c.Imported.Kind, c.Imported.Address, c.Imported.Group, c.Imported.Via
				break
			}
		}
		applied++
	}
	for _, s := range pv.DBNew {
		if !add["db:"+s.Name] {
			continue
		}
		m.cfg.NetDev.DBSources = append(m.cfg.NetDev.DBSources, config.NetDevDBSource{
			Name: s.Name, Type: s.Type, Host: s.Host, Port: s.Port, Via: s.Via,
		})
		applied++
	}
	if applied == 0 {
		return 0, fmt.Errorf("没有勾选任何待合并项")
	}
	_ = AppendAudit(Audit{Device: "(import)", Command: "import " + path, Class: "write", Status: AuditOK,
		OutputBytes: applied})
	return applied, nil
}

// importNowStamp keeps the timestamp helper local (used by tests).
func importNowStamp() string { return time.Now().Format("20060102-150405") }


// Cfg exposes the manager's live config (the desktop import-apply bridge
// mirrors the merged inventory into the config being persisted).
func (m *Manager) Cfg() *config.Config { return m.cfg }

// ImportStageFile persists an in-browser-read export payload to the state
// dir and returns the staged path — Wails' file dialog gives the frontend
// the file CONTENT (not an OS path the Go side can reopen), so the wizard
// stages content first, then previews/applies by path.
func ImportStageFile(content string) (string, error) {
	if !json.Valid([]byte(content)) {
		return "", fmt.Errorf("不是合法的 JSON 格式")
	}
	if err := os.MkdirAll(netdevStateDir(), 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(netdevStateDir(), "import-staged.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
