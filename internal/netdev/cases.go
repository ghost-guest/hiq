package netdev

// cases.go — 案例与时间线（NETDEV_SPEC_V2 §4.6）：一次入侵排查/重大故障
// 就是一个 Case——时间线引用卡（Finding/日志命中/体检项/人工笔记）+ IOC
// 台账 + 复盘报告导出。本地 JSON 存储（state dir/cases/），与 Finding 一样
// 是纯本地工作产物；Bundle 写成 markdown 文件（诊断包的 zip+manifest 版
// 随 v1.1，报告先行）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CaseIOC is one indicator in the case ledger.
type CaseIOC struct {
	Value   string    `json:"value"`
	Type    string    `json:"type"` // ip | hash | domain | keyword
	Note    string    `json:"note,omitempty"`
	AddedAt time.Time `json:"added_at"`
}

// CaseEntry is one reference card pinned on the case timeline.
type CaseEntry struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"` // finding | log | audit | triage | note
	Device string    `json:"device,omitempty"`
	Text   string    `json:"text"`
	Ref    string    `json:"ref,omitempty"` // e.g. finding id
}

// IncidentCase is one investigation.
type IncidentCase struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	Status    string      `json:"status"` // open | closed
	Devices   []string    `json:"devices,omitempty"`
	Entries   []CaseEntry `json:"entries,omitempty"`
	IOCs      []CaseIOC   `json:"iocs,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// CasesDir stores one JSON per case.
func CasesDir() string {
	if casesDirOverr != "" {
		return casesDirOverr
	}
	return filepath.Join(netdevStateDir(), "cases")
}

var casesDirOverr string

// ListCases returns all cases, newest-updated first.
func ListCases() ([]*IncidentCase, error) {
	entries, err := os.ReadDir(CasesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*IncidentCase
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(CasesDir(), e.Name()))
		if err != nil {
			continue
		}
		var c IncidentCase
		if jsonUnmarshal(raw, &c) == nil {
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// SaveCase persists one case (id/时间戳在空时生成).
func SaveCase(c *IncidentCase) error {
	c.Title = strings.TrimSpace(c.Title)
	if c.Title == "" {
		return fmt.Errorf("case: title is required")
	}
	if c.Status == "" {
		c.Status = "open"
	}
	now := time.Now()
	if c.ID == "" {
		c.ID = fmt.Sprintf("C%s-%d", now.Format("20060102"), now.UnixNano()%10000)
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	StateEventSnap(StateEventCaseSave, c.ID, StateActorUser, filepath.Join(CasesDir(), c.ID+".json"))
	if err := os.MkdirAll(CasesDir(), 0o700); err != nil {
		return err
	}
	body, err := jsonMarshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(CasesDir(), c.ID+".json"), body, 0o600)
}

// DeleteCase removes one case by id.
func DeleteCase(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("case: bad id")
	}
	StateEventSnap(StateEventCaseDelete, id, StateActorUser, filepath.Join(CasesDir(), id+".json"))
	return os.Remove(filepath.Join(CasesDir(), id+".json"))
}

// CaseReport renders the case as a 复盘 markdown page.
func CaseReport(c *IncidentCase) string {
	var b strings.Builder
	b.WriteString("# " + c.Title + "\n\n")
	b.WriteString(fmt.Sprintf("状态：%s　创建：%s　更新：%s\n", c.Status, c.CreatedAt.Format("01-02 15:04"), c.UpdatedAt.Format("01-02 15:04")))
	if len(c.Devices) > 0 {
		b.WriteString("涉及设备：" + strings.Join(c.Devices, "、") + "\n")
	}
	if len(c.IOCs) > 0 {
		b.WriteString("\n## IOC 台账\n\n")
		for _, i := range c.IOCs {
			b.WriteString(fmt.Sprintf("- `%s`（%s）%s\n", i.Value, i.Type, i.Note))
		}
	}
	b.WriteString("\n## 时间线\n\n")
	for _, e := range c.Entries {
		dev := ""
		if e.Device != "" {
			dev = " @" + e.Device
		}
		b.WriteString(fmt.Sprintf("- **%s** %s%s：%s\n", e.Time.Format("01-02 15:04:05"), e.Kind, dev, e.Text))
	}
	b.WriteString("\n---\n由 hiq 运维案例中心导出。\n")
	return b.String()
}

// CaseBundle writes the 复盘报告 (case + related findings + audit excerpt)
// into the state dir and returns the path — the FDE/蓝队交接件 v1.
func CaseBundle(caseID string) (string, error) {
	cases, err := ListCases()
	if err != nil {
		return "", err
	}
	var target *IncidentCase
	for _, c := range cases {
		if c.ID == caseID {
			target = c
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("case %q not found", caseID)
	}
	var b strings.Builder
	b.WriteString(CaseReport(target))
	// 涉案设备的 Findings 摘要
	if fs, err := ListFindings(); err == nil {
		var related []*Finding
		for _, f := range fs {
			for _, d := range target.Devices {
				if containsStr(f.Devices, d) {
					related = append(related, f)
					break
				}
			}
		}
		if len(related) > 0 {
			b.WriteString("\n## 相关 Finding\n\n")
			for i, f := range related {
				if i >= 30 {
					b.WriteString(fmt.Sprintf("- …另有 %d 条\n", len(related)-30))
					break
				}
				b.WriteString(fmt.Sprintf("- **%s**（%s，%s）：%s\n", f.Title, f.Severity, strings.Join(f.Devices, "、"), firstLine(f.Detail)))
			}
		}
	}
	// 涉案设备的审计尾部（近 24h 写路径）
	since := time.Now().Add(-24 * time.Hour)
	b.WriteString("\n## 近 24h 变更（审计）\n\n")
	n := 0
	for _, e := range readAuditSince(since) {
		if !timelineChangeClasses[e.Class] {
			continue
		}
		if len(target.Devices) > 0 && !containsStr(target.Devices, e.Device) {
			continue
		}
		if n++; n > 30 {
			b.WriteString("- …更多见审计页签\n")
			break
		}
		b.WriteString(fmt.Sprintf("- %s %s %s\n", e.Time.Format("15:04:05"), e.Device, e.Command))
	}
	if n == 0 {
		b.WriteString("- （无）\n")
	}
	path := filepath.Join(netdevStateDir(), fmt.Sprintf("case-%s-%s.md", target.ID, time.Now().Format("20060102-150405")))
	if err := os.MkdirAll(netdevStateDir(), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }
func jsonMarshal(v any) ([]byte, error)     { b, err := json.MarshalIndent(v, "", "  "); return b, err }
