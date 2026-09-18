package netdev

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	internaldiff "github.com/zzycxz/hiq/internal/diff"
)

// backup.go — the configuration backup vault: sealed reads of each device's
// running-config snapshotted as immutable versions. Only REDACTED text is
// ever stored (the same text the model sees); raw configs live solely in the
// in-memory session buffer. Any two versions diff into a unified view — the
// "what changed" backbone for diagnosis (变更↔故障关联) and drift detection.

type BackupVersion struct {
	ID     string `json:"id"` // <device>@<unixnano>
	Device string `json:"device"`
	At     string `json:"at"`    // 01-02 15:04:05
	Bytes  int    `json:"bytes"` // redacted text length
	Lines  int    `json:"lines"`
}

type storedBackup struct {
	Device string
	Nanos  int64
	Text   string // redacted
}

var (
	backupsMu   sync.Mutex
	backupsOver string // test override
)

func SetBackupsDir(p string) {
	backupsMu.Lock()
	defer backupsMu.Unlock()
	backupsOver = p
}

func backupsDir() string {
	backupsMu.Lock()
	defer backupsMu.Unlock()
	if backupsOver != "" {
		return backupsOver
	}
	return filepath.Join(netdevStateDir(), "backups")
}

// RunBackup reads one device's (or every device's, device == "") running
// config through the sealed Exec path and files each as a new version.
// Returns the versions written, in device order.
func (m *Manager) RunBackup(ctx context.Context, device string) ([]BackupVersion, error) {
	targets := make([]struct{ name string }, 0)
	if strings.TrimSpace(device) != "" {
		targets = append(targets, struct{ name string }{device})
	} else {
		if len(m.cfg.NetDev.Devices) == 0 {
			return nil, fmt.Errorf("no devices configured")
		}
		for _, d := range m.cfg.NetDev.Devices {
			targets = append(targets, struct{ name string }{d.Name})
		}
	}
	var out []BackupVersion
	var problems []string
	for _, t := range targets {
		d, ok := m.cfg.NetDevDeviceByName(t.name)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: not in inventory", t.name))
			continue
		}
		drv, ok := m.driverFor(d)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: no driver (%s/%s)", t.name, d.Vendor, d.OS))
			continue
		}
		cmd, ok := RunningConfigCommand(drv.Key())
		if !ok {
			continue
		}
		res := m.Exec(ctx, t.name, cmd)
		if res.Refused {
			problems = append(problems, fmt.Sprintf("%s: refused (%s)", t.name, res.Class))
			continue
		}
		if res.IsError {
			problems = append(problems, fmt.Sprintf("%s: device error", t.name))
			continue
		}
		v, err := saveBackup(t.name, res.Output)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: save: %v", t.name, err))
			continue
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no backups written: %s", strings.Join(problems, "；"))
	}
	return out, nil
}

// backupIDMu + lastBackupNanos keep backup IDs strictly increasing: clock
// granularity on Windows (and rapid successive saves anywhere) can hand two
// saves the same nanosecond — identical IDs would silently overwrite the
// older version and empty the diff.
var (
	backupIDMu      sync.Mutex
	lastBackupNanos int64
)

func nextBackupNanos() int64 {
	backupIDMu.Lock()
	defer backupIDMu.Unlock()
	nanos := time.Now().UnixNano()
	if nanos <= lastBackupNanos {
		nanos = lastBackupNanos + 1
	}
	lastBackupNanos = nanos
	return nanos
}

func saveBackup(device, text string) (BackupVersion, error) {
	dir := backupsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return BackupVersion{}, err
	}
	nanos := nextBackupNanos()
	sb := storedBackup{Device: device, Nanos: nanos, Text: text}
	b, err := json.Marshal(sb)
	if err != nil {
		return BackupVersion{}, err
	}
	id := fmt.Sprintf("%s@%d", device, nanos)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), b, 0o600); err != nil {
		return BackupVersion{}, err
	}
	return BackupVersion{
		ID: id, Device: device,
		At:    time.Unix(0, nanos).Format("01-02 15:04:05"),
		Bytes: len(text), Lines: strings.Count(text, "\n") + 1,
	}, nil
}

// ListBackups returns a device's versions, newest first ("" = every device).
func ListBackups(device string) []BackupVersion {
	dir := backupsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []BackupVersion
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		var sb storedBackup
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || json.Unmarshal(data, &sb) != nil {
			continue
		}
		if device != "" && sb.Device != device {
			continue
		}
		out = append(out, BackupVersion{
			ID: fmt.Sprintf("%s@%d", sb.Device, sb.Nanos), Device: sb.Device,
			At:    time.Unix(0, sb.Nanos).Format("01-02 15:04:05"),
			Bytes: len(sb.Text), Lines: strings.Count(sb.Text, "\n") + 1,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// GetBackupText returns a version's redacted config text.
func GetBackupText(device, id string) (string, error) {
	dir := backupsDir()
	var sb storedBackup
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil || json.Unmarshal(data, &sb) != nil {
		return "", fmt.Errorf("backup %s: not found or unreadable", id)
	}
	if sb.Device != device {
		return "", fmt.Errorf("backup %s does not belong to device %s", id, device)
	}
	return sb.Text, nil
}

// DiffBackups returns the unified diff between two of a device's versions
// (a = old, b = new), reusing the repo's myers diff.
func DiffBackups(device, idA, idB string) (string, error) {
	oldText, err := GetBackupText(device, idA)
	if err != nil {
		return "", err
	}
	newText, err := GetBackupText(device, idB)
	if err != nil {
		return "", err
	}
	ch := internaldiff.Build(device+" running-config", oldText, newText, internaldiff.Modify)
	return ch.Diff, nil
}
