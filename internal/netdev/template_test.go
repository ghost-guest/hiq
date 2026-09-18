package netdev

import (
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

func templateTestManager(t *testing.T) *Manager {
	t.Helper()
	m, _ := guardrailManager(t, config.NetDevGuardrails{})
	templatesDirOverride = t.TempDir()
	t.Cleanup(func() { templatesDirOverride = "" })
	return m
}

func vlanTemplate() *Template {
	return &Template{
		Name:   "IoT VLAN 批量下发",
		Intent: "为核心组每台设备下发 IoT VLAN 并描述",
		Vars:   []string{"vlan", "desc"},
		Steps: []TemplateStep{{
			Commands: []string{"vlan {{vlan}}", "description {{desc}}"},
			Rollback: []string{"undo vlan {{vlan}}"},
		}},
		Targets: []string{"sw1"},
	}
}

// 渲染走设备属性 + 用户变量；预览逐条标注分类；危险动词在变更侧标记。
func TestTemplateRenderPreview(t *testing.T) {
	m := templateTestManager(t)
	preview, err := m.TemplateRender(vlanTemplate(), map[string]string{"vlan": "100", "desc": "IoT-cam"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(preview) != 1 || !preview[0].Available {
		t.Fatalf("preview = %+v", preview)
	}
	st := preview[0].Steps[0]
	if len(st.Commands) != 2 || st.Commands[0] != "vlan 100" || st.Commands[1] != "description IoT-cam" {
		t.Fatalf("rendered = %v", st.Commands)
	}
	if len(st.Rollback) != 1 || st.Rollback[0] != "undo vlan 100" {
		t.Fatalf("rollback = %v", st.Rollback)
	}
	// huawei driver: vlan N = write; description = write — preview shows it.
	if st.Classes[0] != "write" {
		t.Fatalf("classes = %v", st.Classes)
	}
	if st.Dangerous {
		t.Fatal("vlan/description are writes but not danger-verb scans")
	}
}

// 变量值白名单（附录 B-10）：元字符直接拒绝，不进渲染。
func TestTemplateVarCharset(t *testing.T) {
	m := templateTestManager(t)
	for _, bad := range []string{"a; rm -rf /", "a\nb", "$(reboot)", "x `y`"} {
		if _, err := m.TemplateRender(vlanTemplate(), map[string]string{"vlan": "100", "desc": bad}); err == nil || !strings.Contains(err.Error(), "白名单") {
			t.Fatalf("value %q accepted (err=%v)", bad, err)
		}
	}
	// 合法字符集内的值通过（接口名/网段/描述的常见形态）。
	if _, err := m.TemplateRender(vlanTemplate(), map[string]string{"vlan": "100", "desc": "IoT-cam(栋2)/24"}); err != nil {
		t.Fatalf("legit value rejected: %v", err)
	}
}

// 未知占位符在预览里标不可用（不静默空串）；设备属性变量可用。
func TestTemplateUnknownPlaceholder(t *testing.T) {
	m := templateTestManager(t)
	tpl := vlanTemplate()
	tpl.Steps[0].Commands = []string{"vlan {{nope}}"}
	preview, err := m.TemplateRender(tpl, map[string]string{"vlan": "1", "desc": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if preview[0].Available || !strings.Contains(preview[0].Reason, "nope") {
		t.Fatalf("unknown placeholder accepted: %+v", preview[0])
	}
	if _, err := m.TemplateApply(tpl, map[string]string{"vlan": "1", "desc": "x"}); err == nil {
		t.Fatal("apply accepted an unrenderable target")
	}
	tpl.Steps[0].Commands = []string{"description {{hostname}}"}
	preview, err = m.TemplateRender(tpl, map[string]string{"vlan": "1", "desc": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if preview[0].Steps[0].Commands[0] != "description sw1" {
		t.Fatalf("device attr var = %v", preview[0].Steps[0].Commands)
	}
}

// 生成走完整提案管线：校验 + 草稿落盘；只读组目标整批拒绝。
func TestTemplateApplyCreatesDraftProposal(t *testing.T) {
	m := templateTestManager(t)
	p, err := m.TemplateApply(vlanTemplate(), map[string]string{"vlan": "100", "desc": "IoT-cam"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if p.Status != ProposalDraft || len(p.Steps) != 1 {
		t.Fatalf("proposal = %s steps=%d", p.Status, len(p.Steps))
	}
	if p.Steps[0].Commands[0] != "vlan 100" || p.Steps[0].Rollback[0] != "undo vlan 100" {
		t.Fatalf("step = %+v", p.Steps[0])
	}
	got, err := GetProposal(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Intent, "[模板") {
		t.Fatalf("intent = %q", got.Intent)
	}

	// 只读组目标：预览标记不可用，apply 整批拒绝。
	m.cfg.NetDev.Groups = append(m.cfg.NetDev.Groups, config.NetDevGroup{Name: "ro", Policy: config.NetDevPolicyReadOnly})
	m.cfg.NetDev.Devices = append(m.cfg.NetDev.Devices, config.NetDevDevice{
		Name: "sw-ro", Vendor: "huawei", OS: "vrp8", Group: "ro", Address: "127.0.0.1", Username: "x", PasswordEnv: "TEST_ENV",
	})
	tpl := vlanTemplate()
	tpl.Targets = []string{"sw-ro"}
	preview, _ := m.TemplateRender(tpl, map[string]string{"vlan": "1", "desc": "x"})
	if preview[0].Available || preview[0].Reason == "" {
		t.Fatalf("read-only target preview = %+v", preview[0])
	}
	if _, err := m.TemplateApply(tpl, map[string]string{"vlan": "1", "desc": "x"}); err == nil {
		t.Fatal("read-only target accepted")
	}
}

// 定义校验：无回滚计划的模板不落盘。
func TestTemplateValidation(t *testing.T) {
	tpl := vlanTemplate()
	tpl.Steps[0].Rollback = nil
	if err := SaveTemplate(tpl); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("rollback-less template saved: %v", err)
	}
	tpl = vlanTemplate()
	tpl.Vars = []string{"接口 名"}
	if err := SaveTemplate(tpl); err == nil {
		t.Fatal("bad var name accepted")
	}
}
