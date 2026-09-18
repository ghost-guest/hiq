package netdev

// srvconf.go — 服务器配置文件管理（NETDEV_SPEC_V2 §7.3）：对 linux 目标的
// 白名单路径（config_paths，沿用 log_paths 同款授权模式）做 抓取快照 →
// 两版本 UnifiedDiff（复用网络配置 diff 组件）→ 环境 Drift（同名路径跨
// dev/staging/prod 目标的 diff 视图）。**修改不在此发生**：cp+sed 不允许，
// 编辑产物以 file-upload 步骤提交（人对整份内容签字）。备份恢复演练走
// restore-verify 提案步骤类型——把快照恢复到指定 staging 目标并跑验证读，
// 证明「备份真的能恢复」；生产目标（与源同组）不允许作为演练接收方。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	internaldiff "github.com/zzycxz/hiq/internal/diff"

	"github.com/zzycxz/hiq/internal/config"
)

// SrvConfVersion is one config-file snapshot.
type SrvConfVersion struct {
	ID     string `json:"id"`
	Device string `json:"device"`
	Path   string `json:"path"`
	At     string `json:"at"`
	Bytes  int    `json:"bytes"`
	Lines  int    `json:"lines"`
}

// SrvConfDriftRow is one device's drift verdict against the baseline device.
type SrvConfDriftRow struct {
	Device string `json:"device"`
	Group  string `json:"group"`
	Status string `json:"status"` // same | drift | error | absent
	Diff   string `json:"diff,omitempty"`
	Error  string `json:"error,omitempty"`
}

type storedSrvConf struct {
	Device string
	Path   string
	Nanos  int64
	Text   string
}

// srvConfDirOverride isolates snapshot storage in tests.
var srvConfDirOverride string

func srvConfDir() string {
	if srvConfDirOverride != "" {
		return srvConfDirOverride
	}
	return filepath.Join(netdevStateDir(), "srvconf")
}

func pathHash(p string) string {
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:6])
}

// srvConfAllowed reports whether path sits inside one of the device's
// config_paths roots (same authorization model as log_paths — human-registered
// only; the agent cannot widen it).
func srvConfAllowed(d config.NetDevDevice, path string) bool {
	clean := filepath.ToSlash(path)
	for _, root := range d.ConfigPaths {
		root = strings.TrimRight(filepath.ToSlash(root), "/")
		if root != "" && (clean == root || strings.HasPrefix(clean, root+"/")) {
			return true
		}
	}
	return false
}

func (m *Manager) srvConfDevice(deviceName string) (config.NetDevDevice, error) {
	d, ok := m.cfg.NetDevDeviceByName(deviceName)
	if !ok {
		return config.NetDevDevice{}, fmt.Errorf("device %q is not in the inventory", deviceName)
	}
	if d.Vendor != "linux" {
		return config.NetDevDevice{}, fmt.Errorf("device %q: 配置文件管理仅面向 linux SSH 目标（§7.3）", deviceName)
	}
	if len(d.ConfigPaths) == 0 {
		return config.NetDevDevice{}, fmt.Errorf("device %q 未配置 config_paths 白名单——在运维设置中登记（如 /etc/nginx）", deviceName)
	}
	return d, nil
}

// SrvConfSnapshot reads one whitelisted config file and stores a version.
func (m *Manager) SrvConfSnapshot(ctx context.Context, deviceName, path string) (*SrvConfVersion, error) {
	d, err := m.srvConfDevice(deviceName)
	if err != nil {
		return nil, err
	}
	if !srvConfAllowed(d, path) {
		return nil, fmt.Errorf("path %q is outside device %q's config_paths whitelist", path, deviceName)
	}
	text, ok, err := m.sshCatFile(ctx, d, path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if !ok {
		return nil, fmt.Errorf("read %s: file absent or unreadable", path)
	}
	v, err := saveSrvConf(deviceName, path, text)
	if err != nil {
		return nil, err
	}
	_ = AppendAudit(Audit{Time: time.Now(), Device: deviceName, Command: "srvconf-snapshot " + path, Class: "read", Status: AuditOK, OutputBytes: len(text)})
	return &v, nil
}

var srvConfIDMu sync.Mutex

var lastSrvConfNanos int64

func nextSrvConfNanos() int64 {
	srvConfIDMu.Lock()
	defer srvConfIDMu.Unlock()
	nanos := time.Now().UnixNano()
	if nanos <= lastSrvConfNanos {
		nanos = lastSrvConfNanos + 1
	}
	lastSrvConfNanos = nanos
	return nanos
}

func saveSrvConf(device, path, text string) (SrvConfVersion, error) {
	dir := srvConfDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return SrvConfVersion{}, err
	}
	nanos := nextSrvConfNanos()
	id := fmt.Sprintf("sc@%s@%s@%d", device, pathHash(path), nanos)
	sb := storedSrvConf{Device: device, Path: path, Nanos: nanos, Text: text}
	b, err := json.Marshal(sb)
	if err != nil {
		return SrvConfVersion{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), b, 0o600); err != nil {
		return SrvConfVersion{}, err
	}
	return SrvConfVersion{
		ID: id, Device: device, Path: path,
		At:    time.Unix(0, nanos).Format("01-02 15:04:05"),
		Bytes: len(text), Lines: strings.Count(text, "\n") + 1,
	}, nil
}

// SrvConfVersions lists a (device, path)'s snapshots, newest first.
func SrvConfVersions(device, path string) []SrvConfVersion {
	entries, err := os.ReadDir(srvConfDir())
	if err != nil {
		return nil
	}
	want := "sc@" + device + "@" + pathHash(path) + "@"
	var out []SrvConfVersion
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), want) {
			continue
		}
		var sb storedSrvConf
		data, err := os.ReadFile(filepath.Join(srvConfDir(), e.Name()))
		if err != nil || json.Unmarshal(data, &sb) != nil {
			continue
		}
		out = append(out, SrvConfVersion{
			ID: strings.TrimSuffix(e.Name(), ".json"), Device: sb.Device, Path: sb.Path,
			At:    time.Unix(0, sb.Nanos).Format("01-02 15:04:05"),
			Bytes: len(sb.Text), Lines: strings.Count(sb.Text, "\n") + 1,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// SrvConfText returns one snapshot's content.
func SrvConfText(id string) (string, error) {
	data, err := os.ReadFile(filepath.Join(srvConfDir(), id+".json"))
	if err != nil {
		return "", fmt.Errorf("srvconf %s: not found", id)
	}
	var sb storedSrvConf
	if err := json.Unmarshal(data, &sb); err != nil {
		return "", err
	}
	return sb.Text, nil
}

// SrvConfDiff returns the unified diff between two snapshots (a = old).
func SrvConfDiff(idA, idB string) (string, error) {
	oldText, err := SrvConfText(idA)
	if err != nil {
		return "", err
	}
	newText, err := SrvConfText(idB)
	if err != nil {
		return "", err
	}
	ch := internaldiff.Build("config", oldText, newText, internaldiff.Modify)
	return ch.Diff, nil
}

// SrvConfDrift compares the CURRENT content of one path across devices (the
// first device is the baseline): the 「同一份 nginx 配置三个环境差在哪」view
// (§7.3). Devices must share the path inside their own config_paths whitelist.
func (m *Manager) SrvConfDrift(ctx context.Context, path string, devices []string) ([]SrvConfDriftRow, error) {
	if len(devices) < 2 {
		return nil, fmt.Errorf("drift needs ≥2 devices")
	}
	var rows []SrvConfDriftRow
	var base string
	for i, name := range devices {
		row := SrvConfDriftRow{Device: name}
		d, err := m.srvConfDevice(name)
		if err == nil && !srvConfAllowed(d, path) {
			err = fmt.Errorf("路径不在 %s 的 config_paths 白名单中", name)
		}
		if err != nil {
			row.Status, row.Error = "error", err.Error()
			rows = append(rows, row)
			continue
		}
		row.Group = d.Group
		text, ok, err := m.sshCatFile(ctx, d, path)
		switch {
		case err != nil:
			row.Status, row.Error = "error", err.Error()
		case !ok:
			row.Status, row.Error = "absent", "文件不存在或不可读"
		case i == 0:
			base = text
			row.Status = "same" // the baseline itself
		default:
			if text == base {
				row.Status = "same"
			} else {
				row.Status = "drift"
				ch := internaldiff.Build("config", base, text, internaldiff.Modify)
				row.Diff = ch.Diff
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// ── restore-verify 提案步骤（§7.3 备份恢复演练）───────────────────────────

// validateRestoreVerify enforces the drill's structural safety: receiver is a
// linux target that whitelists the path in its OWN config_paths, and the
// receiver must be a different environment than the snapshot source (生产目标
// 不允许作为演练接收方 — same group = same environment, refused).
func (m *Manager) validateRestoreVerify(s *ProposalStep, target config.NetDevDevice) error {
	if target.Vendor != "linux" {
		return fmt.Errorf("proposal: restore-verify step for %q requires a linux SSH receiver", s.Device)
	}
	if strings.TrimSpace(s.RestoreDevice) == "" {
		return fmt.Errorf("proposal: restore-verify step for %q has no restore_device (snapshot source)", s.Device)
	}
	if strings.TrimSpace(s.VerifyCmd) == "" {
		return fmt.Errorf("proposal: restore-verify step for %q has no verify command — the drill must PROVE the restore (§7.3)", s.Device)
	}
	if !remotePathRe.MatchString(s.RemotePath) {
		return fmt.Errorf("proposal: restore-verify step for %q: remote path %q must be absolute and quote-safe", s.Device, s.RemotePath)
	}
	if !srvConfAllowed(target, s.RemotePath) {
		return fmt.Errorf("proposal: restore-verify step for %q: path %q is outside the receiver's config_paths whitelist", s.Device, s.RemotePath)
	}
	src, ok := m.cfg.NetDevDeviceByName(s.RestoreDevice)
	if !ok {
		return fmt.Errorf("proposal: restore-verify step for %q: source device %q not in inventory", s.Device, s.RestoreDevice)
	}
	if src.Vendor != "linux" || !srvConfAllowed(src, s.RemotePath) {
		return fmt.Errorf("proposal: restore-verify step for %q: source %q does not whitelist %s in config_paths", s.Device, s.RestoreDevice, s.RemotePath)
	}
	if src.Name == target.Name || src.Group == target.Group {
		return fmt.Errorf("proposal: restore-verify 演练接收方 %q 与备份源同设备/同组（%q）——生产环境目标不允许作为演练接收方（§7.3）", s.Device, src.Group)
	}
	if s.RestoreVersion != "" {
		if _, err := SrvConfText(s.RestoreVersion); err != nil {
			return fmt.Errorf("proposal: restore-verify step for %q: %v", s.Device, err)
		}
	}
	return nil
}

// execRestoreVerify: 备份接收方现文件 → 恢复快照 → 验证读。证明「备份真的
// 能恢复」；验证不过即冻结（提案首败语义），回滚恢复接收方原文件。
func (m *Manager) execRestoreVerify(ctx context.Context, target config.NetDevDevice, s *ProposalStep) error {
	// Resolve the snapshot ("" = the source's latest for this path).
	version := s.RestoreVersion
	if version == "" {
		vers := SrvConfVersions(s.RestoreDevice, s.RemotePath)
		if len(vers) == 0 {
			return fmt.Errorf("源设备 %s 没有 %s 的快照——请先创建快照再演练", s.RestoreDevice, s.RemotePath)
		}
		version = vers[0].ID
	}
	text, err := SrvConfText(version)
	if err != nil {
		return err
	}
	// Receiver's current file becomes the rollback basis.
	if cur, ok, err := m.sshCatFile(ctx, target, s.RemotePath); err != nil {
		return fmt.Errorf("receiver backup read: %w", err)
	} else if ok {
		s.Backup = cur
	} else {
		s.Backup = absentMarker
	}
	_ = AppendAudit(Audit{Device: s.Device, Command: "restore-verify " + s.RestoreDevice + " → " + s.Device + " " + s.RemotePath, Class: "proposal-write", Status: AuditOK})
	if err := m.sshB64Upload(ctx, target, []byte(text), s.RemotePath); err != nil {
		return err
	}
	// The verify read is what makes it a DRILL, not a copy.
	if err := m.sshReload(ctx, target, s.VerifyCmd); err != nil {
		return fmt.Errorf("verify %q: %w", s.VerifyCmd, err)
	}
	return nil
}
