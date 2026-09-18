package netdev

// template.go — 批量模板（NETDEV_SPEC_V2 §7.2）：模板 = 步骤序列 + 变量（按
// 设备属性渲染：接口名、网段、hostname）。生成时逐台渲染出「N 份结果」整
// 体预览（dry-run：无任何副作用，逐条标注分类与危险动词），人审后生成一
// 份多设备步骤的提案草稿——执行/首败冻结/回滚矩阵全部沿用提案既有语义
// （每台一行：已执行 applied / 已回滚 rolled / 未触达 pending）。变量值只
// 允许白名单字符集（附录 B-10），渲染产物仍逐条过分类器。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

// TemplateStep is one per-device step's COMMAND TEMPLATE ({{var}} placeholders).
type TemplateStep struct {
	Commands []string `json:"commands"`
	Rollback []string `json:"rollback"`
}

// Template is one batch change template. Vars carries omitempty so a
// variable-less template serializes without the key (the frontend's `?? []`
// guards the absent case) instead of as JSON null.
type Template struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Intent    string         `json:"intent"`
	Vars      []string       `json:"vars,omitempty"` // user-supplied variable names
	Steps     []TemplateStep `json:"steps"`          // per-device command templates
	Targets   []string       `json:"targets"`        // explicit device names
	CreatedAt time.Time      `json:"created_at"`
}

// TemplatePreviewDevice is one target's rendered result (dry-run, §7.2).
type TemplatePreviewDevice struct {
	Device    string                `json:"device"`
	Available bool                  `json:"available"` // device exists & not read-only-group
	Reason    string                `json:"reason,omitempty"`
	Steps     []TemplatePreviewStep `json:"steps"`
}

// TemplatePreviewStep is one rendered command with its classifier verdict.
type TemplatePreviewStep struct {
	Commands  []string `json:"commands"`
	Rollback  []string `json:"rollback"`
	Classes   []string `json:"classes"`   // per rendered command: read|write|dangerous|unknown
	Dangerous bool     `json:"dangerous"` // verb scan hit — confirm2 forced on apply
}

// varValueRe is the variable-value whitelist charset (附录 B-10): Unicode
// letters/digits (descriptions may carry Chinese) plus CLI-safe punctuation —
// shell/CLI metacharacters (;|&`$\<>'"{}!) and control chars never pass.
var varValueRe = regexp.MustCompile(`^[\p{L}\p{N}_.:/@%+() -]+$`)

var templatePlaceholderRe = regexp.MustCompile(`\{\{([A-Za-z0-9_]+)\}\}`)

// templatesDirOverride isolates template storage in tests.
var templatesDirOverride string

func templatesDir() string {
	if templatesDirOverride != "" {
		return templatesDirOverride
	}
	return filepath.Join(netdevStateDir(), "templates")
}

var (
	templateMu  sync.Mutex
	templateSeq int
)

func newTemplateID() string {
	templateMu.Lock()
	defer templateMu.Unlock()
	templateSeq++
	return fmt.Sprintf("T%s-%d", time.Now().Format("20060102"), templateSeq)
}

// SaveTemplate persists t (create or update; ID assigned on create).
func SaveTemplate(t *Template) error {
	if err := validateTemplateDef(t); err != nil {
		return err
	}
	if t.ID == "" {
		t.ID = newTemplateID()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	StateEventSnap(StateEventTplSave, t.ID, StateActorUser, filepath.Join(templatesDir(), t.ID+".json"))
	if err := os.MkdirAll(templatesDir(), 0o700); err != nil {
		return err
	}
	templateMu.Lock()
	defer templateMu.Unlock()
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(templatesDir(), t.ID+".json"), b, 0o600)
}

func validateTemplateDef(t *Template) error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("template: name is required")
	}
	if strings.TrimSpace(t.Intent) == "" {
		return fmt.Errorf("template %q: intent is required (what & why)", t.Name)
	}
	if len(t.Steps) == 0 {
		return fmt.Errorf("template %q: no steps", t.Name)
	}
	if len(t.Targets) == 0 {
		return fmt.Errorf("template %q: no targets", t.Name)
	}
	nameRe := regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	for _, v := range t.Vars {
		if !nameRe.MatchString(v) {
			return fmt.Errorf("template %q: variable name %q must be [A-Za-z0-9_]", t.Name, v)
		}
	}
	for _, s := range t.Steps {
		if len(s.Commands) == 0 || len(s.Rollback) == 0 {
			return fmt.Errorf("template %q: every step needs commands AND a rollback plan", t.Name)
		}
	}
	return nil
}

// GetTemplate loads one template.
func GetTemplate(id string) (*Template, error) {
	b, err := os.ReadFile(filepath.Join(templatesDir(), id+".json"))
	if err != nil {
		return nil, err
	}
	var t Template
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// ListTemplates returns templates newest-first.
func ListTemplates() ([]*Template, error) {
	entries, err := os.ReadDir(templatesDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Template
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if t, err := GetTemplate(strings.TrimSuffix(e.Name(), ".json")); err == nil {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CreatedAt.After(out[k].CreatedAt) })
	return out, nil
}

// DeleteTemplate removes one template.
func DeleteTemplate(id string) error {
	StateEventSnap(StateEventTplDelete, id, StateActorUser, filepath.Join(templatesDir(), id+".json"))
	return os.Remove(filepath.Join(templatesDir(), id+".json"))
}

// templateVars resolves the render variables for one device: device-derived
// attributes (name/hostname/address/vendor/group) plus the user-supplied set.
func templateVars(d config.NetDevDevice, user map[string]string) map[string]string {
	vars := map[string]string{
		"name": d.Name, "hostname": d.Name, "address": d.Address, "vendor": d.Vendor, "group": d.Group,
	}
	for k, v := range user {
		vars[k] = v
	}
	return vars
}

func renderTemplate(text string, vars map[string]string) (string, error) {
	var missing []string
	out := templatePlaceholderRe.ReplaceAllStringFunc(text, func(m string) string {
		key := strings.Trim(m, "{}")
		if v, ok := vars[key]; ok {
			return v
		}
		missing = append(missing, key)
		return m
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("unknown variable{{s}} %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// TemplateRender is the 逐台 dry-run（§7.2）：renders every target's commands,
// classifies each rendered product (渲染产物仍逐条过分类器), and flags
// destructive verbs — with NO side effects on any device.
func (m *Manager) TemplateRender(t *Template, userVars map[string]string) ([]TemplatePreviewDevice, error) {
	if err := validateTemplateDef(t); err != nil {
		return nil, err
	}
	for k, v := range userVars {
		if !varValueRe.MatchString(v) {
			return nil, fmt.Errorf("变量 %s=%q 含白名单外字符（字母/数字与 .:/@%%+()- 及空格，附录 B-10）", k, v)
		}
	}
	var out []TemplatePreviewDevice
	for _, name := range t.Targets {
		pd := TemplatePreviewDevice{Device: name, Available: true}
		d, ok := m.cfg.NetDevDeviceByName(name)
		if !ok {
			pd.Available = false
			pd.Reason = "设备不在清单"
			out = append(out, pd)
			continue
		}
		if d.Group != "" {
			if g, ok := m.cfg.NetDevGroupByName(d.Group); ok && g.Policy == config.NetDevPolicyReadOnly {
				pd.Available = false
				pd.Reason = "只读组 " + d.Group
			}
		}
		vars := templateVars(d, userVars)
		drv, hasDriver := m.driverFor(d)
		for _, ts := range t.Steps {
			ps := TemplatePreviewStep{}
			for _, c := range ts.Commands {
				rc, err := renderTemplate(c, vars)
				if err != nil {
					pd.Available = false
					pd.Reason = err.Error()
					continue
				}
				ps.Commands = append(ps.Commands, rc)
				cls := "unknown"
				if hasDriver {
					cls = drv.Classify(rc).String()
				}
				ps.Classes = append(ps.Classes, cls)
				// 变更侧危险动词才强制 confirm2（回滚计划豁免，§7.1）。
				if dangerVerbRe.MatchString(rc) {
					ps.Dangerous = true
				}
			}
			for _, c := range ts.Rollback {
				rc, err := renderTemplate(c, vars)
				if err != nil {
					pd.Available = false
					pd.Reason = err.Error()
					continue
				}
				ps.Rollback = append(ps.Rollback, rc)
			}
			pd.Steps = append(pd.Steps, ps)
		}
		out = append(out, pd)
	}
	return out, nil
}

// TemplateApply renders + validates + saves ONE draft proposal whose steps are
// per-device cli steps (rolling execution, first-failure freeze, per-device
// rollback matrix — the existing proposal pipeline unchanged, §7.2).
func (m *Manager) TemplateApply(t *Template, userVars map[string]string) (*Proposal, error) {
	preview, err := m.TemplateRender(t, userVars)
	if err != nil {
		return nil, err
	}
	p := &Proposal{
		Intent: fmt.Sprintf("[模板 %s] %s", t.Name, t.Intent),
	}
	for _, pd := range preview {
		if !pd.Available {
			return nil, fmt.Errorf("目标 %s 不可用（%s）——模板未生成提案", pd.Device, pd.Reason)
		}
		for _, ps := range pd.Steps {
			step := ProposalStep{Device: pd.Device, Commands: ps.Commands, Rollback: ps.Rollback, Dangerous: ps.Dangerous}
			p.Steps = append(p.Steps, step)
		}
	}
	if err := m.ValidateProposal(p); err != nil {
		return nil, fmt.Errorf("渲染产物未过校验：%w", err)
	}
	p.ID = newProposalID() // pre-assigned so the state-history create-marker knows the path
	StateEventSnap(StateEventTplApply, t.ID, StateActorUser, filepath.Join(ProposalsDir(), p.ID+".json"))
	if err := SaveProposal(p); err != nil {
		return nil, err
	}
	_ = AppendAudit(Audit{Device: "(template)", Command: "apply " + t.ID + " → proposal " + p.ID + " (" + t.Name + ")", Class: "proposal", Status: AuditOK})
	return p, nil
}
