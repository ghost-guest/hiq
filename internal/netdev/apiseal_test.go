package netdev

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// The kind=docker/k8s/firewall channels must ride the same operating seal as
// every other channel: audit rows, redaction before the model context, and
// the group-scope guardrail.
func TestSealAPIGetAuditsAndRedacts(t *testing.T) {
	dir := t.TempDir()
	SetAuditPath(filepath.Join(dir, "audit.jsonl"))
	auditLastHash = ""
	defer func() { SetAuditPath(""); auditLastHash = "" }()

	cfg := &config.Config{}
	cfg.NetDev.Enabled = true
	cfg.NetDev.Devices = []config.NetDevDevice{{Name: "dock1", Kind: "docker", Address: "127.0.0.1", Docker: &config.NetDevDockerConfig{Socket: "unix:///nonexistent.sock"}}}
	m := NewManager(cfg)

	out, err := m.sealAPIGet("dock1", "docker inspect web", func() (string, error) {
		return `{"Config":{"Env":["DB_PASSWORD=hunter2","PATH=/usr/bin"]}}`, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "hunter2") {
		t.Errorf("secret survived the seal: %s", out)
	}
	if !strings.Contains(out, "<redacted>") {
		t.Errorf("no redaction marker: %s", out)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if !strings.Contains(string(b), `"docker inspect web"`) || !strings.Contains(string(b), `"dock1"`) {
		t.Errorf("audit row missing: %s", string(b))
	}
}

func TestSealAPIGetGroupScopeRefused(t *testing.T) {
	dir := t.TempDir()
	SetAuditPath(filepath.Join(dir, "audit.jsonl"))
	auditLastHash = ""
	defer func() { SetAuditPath(""); auditLastHash = "" }()

	cfg := &config.Config{}
	cfg.NetDev.Enabled = true
	cfg.NetDev.Guardrails.AllowedGroups = []string{"core"}
	cfg.NetDev.Groups = []config.NetDevGroup{{Name: "core"}, {Name: "edge"}}
	cfg.NetDev.Devices = []config.NetDevDevice{{Name: "k8sprod", Kind: "k8s", Group: "edge", Address: "10.0.0.9", K8s: &config.NetDevK8sConfig{KubeconfigEnv: "X"}}}
	m := NewManager(cfg)

	out, err := m.sealAPIGet("k8sprod", "k8s pods", func() (string, error) { return "should never run", nil })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "allowed device groups") {
		t.Errorf("expected guardrail refusal, got: %s", out)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if !strings.Contains(string(b), AuditRefused) {
		t.Errorf("refusal must be audited: %s", string(b))
	}
}
