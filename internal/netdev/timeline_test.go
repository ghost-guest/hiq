package netdev

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
	"time"
)

// The correlation stream: audit write-paths and findings land on the axis;
// plain reads stay out; the device filter narrows correctly.
func TestTimelineAssemblesChangesAndFindings(t *testing.T) {
	dir := t.TempDir()
	SetAuditPath(filepath.Join(dir, "audit.jsonl"))
	defer SetAuditPath("")
	oldDir := findingsDirOverr
	findingsDirOverr = dir
	defer func() { findingsDirOverr = oldDir }()

	_ = AppendAudit(Audit{Time: time.Now(), Device: "vm-1", Command: "systemctl restart nginx", Class: "proposal-write", Status: AuditOK})
	_ = AppendAudit(Audit{Time: time.Now(), Device: "vm-1", Command: "display version", Class: "read", Status: AuditOK})
	_ = SaveFinding(&Finding{Title: "磁盘水位高", Severity: "warning", Devices: []string{"vm-1"},
		Detail: "91%", Evidence: []Evidence{{Device: "vm-1", Command: "df -h", Output: "91%"}}})

	m := NewManager(&config.Config{})
	evs := m.Timeline("vm-1", 24)
	var changes, findings, reads int
	for _, e := range evs {
		switch e.Kind {
		case "change":
			changes++
			if !strings.Contains(e.Title, "nginx") {
				t.Fatalf("change title: %+v", e)
			}
		case "finding":
			findings++
		default:
			if strings.Contains(e.Title, "display version") {
				reads++
			}
		}
	}
	if changes != 1 || findings != 1 || reads != 0 {
		t.Fatalf("changes=%d findings=%d reads=%d (want 1/1/0): %+v", changes, findings, reads, evs)
	}
	// 别的设备过滤后为空
	if evs := m.Timeline("vm-2", 24); len(evs) != 0 {
		t.Fatalf("vm-2 should be empty: %+v", evs)
	}
}
