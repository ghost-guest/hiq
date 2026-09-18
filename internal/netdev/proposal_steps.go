package netdev

// proposal_steps.go — 结构化提案步骤（NETDEV_SPEC_V2 §7.1）：cli 之外的四种
// 步骤类型的载荷校验、备份、执行与回滚。全部类型继承现有状态机（首败冻
// 结、组策略、变更窗口），且只能从 ExecuteProposal / RollbackProposal 到
// 达——agent 的手没有到这里的路径。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zzycxz/hiq/internal/config"
)

// absentMarker in step.Backup means the remote file did not exist before the
// upload — rolling back removes it again.
const absentMarker = "\x00absent"

// dangerVerbRe flags destructive verbs in any step's command/script text
// (§7.1: delete/scale-down 类动词落 dangerous + confirm2).
var dangerVerbRe = regexp.MustCompile(`(?i)\b(delete|drop|truncate|erase|undo|reset|shutdown|reboot|restart|scale[- ]?down|rm\s+-[rf])\b`)

// dangerScan reports whether any of the step's executable text carries a
// destructive verb.
// dangerScan reports whether the step's CHANGE text carries a destructive
// verb. Rollback/down plans are excluded on purpose: they are the recovery
// contract a human approves with the proposal (a huawei rollback IS an undo —
// scanning it would demand confirm2 for every network change).
func dangerScan(s *ProposalStep) bool {
	texts := append([]string{}, s.Commands...)
	texts = append(texts, s.UpSQL, s.ReloadCmd, s.YAML)
	for _, t := range texts {
		if dangerVerbRe.MatchString(t) {
			return true
		}
	}
	return false
}

// remotePathRe constrains upload target paths: absolute, no quoting hazards
// (the upload command embeds the path in single quotes).
var remotePathRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)

func validateUploadPaths(device, local, remote string) error {
	if strings.TrimSpace(local) == "" {
		return fmt.Errorf("proposal: upload step for %q has no local path", device)
	}
	st, err := os.Stat(local)
	if err != nil {
		return fmt.Errorf("proposal: upload step for %q: local file: %v", device, err)
	}
	if st.IsDir() {
		return fmt.Errorf("proposal: upload step for %q: local path is a directory", device)
	}
	if st.Size() > sftpMaxBytes {
		return fmt.Errorf("proposal: upload step for %q: local file exceeds the %d MB cap", device, sftpMaxBytes>>20)
	}
	if !remotePathRe.MatchString(remote) {
		return fmt.Errorf("proposal: upload step for %q: remote path %q must be absolute and quote-safe ([A-Za-z0-9._/-])", device, remote)
	}
	return nil
}

// ── k8s-apply ────────────────────────────────────────────────────────────────

// kubePlural maps the manifest's Kind to the REST path segment (server-side
// apply needs the plural resource name; v1 covers the common workload kinds —
// unknown kinds are refused rather than guessed).
var kubePlural = map[string]string{
	"Pod":                   "pods",
	"Service":               "services",
	"ConfigMap":             "configmaps",
	"ServiceAccount":        "serviceaccounts",
	"PersistentVolumeClaim": "persistentvolumeclaims",
	"Namespace":             "namespaces",
	"Node":                  "nodes",
	"Deployment":            "deployments",
	"StatefulSet":           "statefulsets",
	"DaemonSet":             "daemonsets",
	"ReplicaSet":            "replicasets",
	"Job":                   "jobs",
	"CronJob":               "cronjobs",
	"Ingress":               "ingresses",
	"NetworkPolicy":         "networkpolicies",
	"Role":                  "roles",
	"RoleBinding":           "rolebindings",
	"ClusterRole":           "clusterroles",
	"ClusterRoleBinding":    "clusterrolebindings",
}

var kubeClusterScoped = map[string]bool{
	"Namespace": true, "Node": true, "ClusterRole": true, "ClusterRoleBinding": true,
}

// kubeManifest is the minimal manifest shape needed to build a resource path.
type kubeManifest struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Namespace string `yaml:"namespace"`
		Name      string `yaml:"name"`
	} `yaml:"metadata"`
}

// kubeResourceRef is a manifest's location on the API server.
type kubeResourceRef struct {
	Kind    string
	Name    string
	Path    string // full resource path (server excluded)
	Cluster bool   // cluster-scoped (no namespace segment)
}

// kubeResourcePath parses the manifest and builds its REST path. defaultNS is
// used when the manifest carries no namespace (the pinned context's default).
func kubeResourcePath(yamlText, defaultNS string) (*kubeResourceRef, error) {
	var mf kubeManifest
	if err := yaml.Unmarshal([]byte(yamlText), &mf); err != nil {
		return nil, fmt.Errorf("manifest parse: %v", err)
	}
	plural, ok := kubePlural[mf.Kind]
	if !ok {
		return nil, fmt.Errorf("kind %q is outside the apply allowlist (has: %s)", mf.Kind, "Pod Service ConfigMap Deployment …")
	}
	if mf.Kind == "Secret" {
		return nil, fmt.Errorf("kind Secret is refused — secret material changes go through the secret store, not proposals")
	}
	if mf.Metadata.Name == "" || !kubeNameRe.MatchString(mf.Metadata.Name) {
		return nil, fmt.Errorf("manifest metadata.name %q is missing/invalid", mf.Metadata.Name)
	}
	ns := mf.Metadata.Namespace
	if ns == "" {
		ns = defaultNS
	}
	if ns == "" {
		ns = "default"
	}
	if !kubeNameRe.MatchString(ns) {
		return nil, fmt.Errorf("namespace %q is invalid", ns)
	}
	prefix := ""
	if mf.APIVersion == "v1" {
		prefix = "/api/v1"
	} else if mf.APIVersion == "" {
		return nil, fmt.Errorf("manifest has no apiVersion")
	} else {
		prefix = "/apis/" + mf.APIVersion
	}
	path := prefix
	if !kubeClusterScoped[mf.Kind] {
		path += "/namespaces/" + ns
	}
	path += "/" + plural + "/" + mf.Metadata.Name
	return &kubeResourceRef{Kind: mf.Kind, Name: mf.Metadata.Name, Path: path, Cluster: kubeClusterScoped[mf.Kind]}, nil
}

// kubeRequest issues one request against the device's pinned cluster. Body may
// be nil; contentType applies when body is set.
func (m *Manager) kubeRequest(ctx context.Context, device, method, path, contentType string, body []byte) (int, []byte, error) {
	t, err := m.kubeTarget(device)
	if err != nil {
		return 0, nil, err
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.server+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, kubeBodyCap))
	return resp.StatusCode, out, nil
}

// execK8sApply backs up the live object (resourceVersion pinned — a restore
// onto drifted state fails instead of clobbering), server-side applies the
// manifest, and verifies the object is readable again.
func (m *Manager) execK8sApply(ctx context.Context, s *ProposalStep) error {
	t, err := m.kubeTarget(s.Device)
	if err != nil {
		return err
	}
	ref, err := kubeResourcePath(s.YAML, t.namespace)
	if err != nil {
		return err
	}
	// Backup: the live object verbatim (restore PUTs these exact bytes — no
	// redaction, or the restore payload would diverge from what was live).
	code, live, err := m.kubeRequest(ctx, s.Device, http.MethodGet, ref.Path, "", nil)
	if err != nil {
		return fmt.Errorf("backup GET %s: %w", ref.Path, err)
	}
	switch code {
	case http.StatusOK:
		s.Backup = string(live)
	case http.StatusNotFound:
		s.Backup = absentMarker // rollback = delete the object again
	default:
		return fmt.Errorf("backup GET %s: status %d: %.200s", ref.Path, code, live)
	}

	q := ref.Path + "?fieldManager=hiq-proposal&force=true"
	code, out, err := m.kubeRequest(ctx, s.Device, http.MethodPatch, q, "application/apply-patch+yaml", []byte(s.YAML))
	if err != nil {
		return fmt.Errorf("apply PATCH %s: %w", ref.Path, err)
	}
	_ = AppendAudit(Audit{Device: s.Device, Command: "k8s-apply " + ref.Kind + "/" + ref.Name, Class: "proposal-write",
		Status: map[bool]string{true: AuditOK, false: AuditFailure}[code >= 200 && code < 300], OutputBytes: len(out),
		Error: map[bool]string{true: "", false: fmt.Sprintf("status %d: %.200s", code, out)}[code >= 200 && code < 300]})
	if code < 200 || code >= 300 {
		return fmt.Errorf("apply %s: status %d: %.200s", ref.Path, code, out)
	}

	// Verify the object is back-readable after the apply.
	code, _, err = m.kubeRequest(ctx, s.Device, http.MethodGet, ref.Path, "", nil)
	if err != nil || code != http.StatusOK {
		return fmt.Errorf("post-apply verify GET %s: status %d (%v)", ref.Path, code, err)
	}
	return nil
}

// execK8sRestore PUTs the backed-up object. The pinned resourceVersion makes a
// restore onto changed state fail loudly (§7.1).
func (m *Manager) execK8sRestore(ctx context.Context, s *ProposalStep) error {
	t, err := m.kubeTarget(s.Device)
	if err != nil {
		return err
	}
	ref, err := kubeResourcePath(s.YAML, t.namespace)
	if err != nil {
		return err
	}
	var code int
	var out []byte
	if s.Backup == absentMarker {
		code, out, err = m.kubeRequest(ctx, s.Device, http.MethodDelete, ref.Path, "", nil)
	} else {
		code, out, err = m.kubeRequest(ctx, s.Device, http.MethodPut, ref.Path, "application/json", []byte(s.Backup))
	}
	_ = AppendAudit(Audit{Device: s.Device, Command: "k8s-rollback " + ref.Kind + "/" + ref.Name, Class: "proposal-rollback",
		Status: map[bool]string{true: AuditOK, false: AuditFailure}[code >= 200 && code < 300], Error: fmt.Sprintf("status %d: %.200s", code, out)})
	if err != nil || code < 200 || code >= 300 {
		return fmt.Errorf("restore %s: status %d: %.200s (%v)", ref.Path, code, out, err)
	}
	return nil
}

// ── sql-migration ────────────────────────────────────────────────────────────

// splitSQLStatements splits a migration script on top-level ";". v1 limitation:
// no procedures/triggers (their bodies contain ";") — those belong in files,
// not proposal payloads.
func splitSQLStatements(script string) []string {
	var out []string
	for _, part := range strings.Split(script, ";") {
		if strings.TrimSpace(part) != "" {
			out = append(out, strings.TrimSpace(part))
		}
	}
	return out
}

func migrationDriverName(engine string) (string, error) {
	switch engine {
	case "mysql":
		return "mysql", nil
	case "postgres":
		return "pgx", nil
	case "mssql":
		return "sqlserver", nil
	default:
		return "", fmt.Errorf("engine %q not supported for migrations (v1: mysql/postgres/mssql)", engine)
	}
}

// dbExecStatement runs ONE write statement on the source — reachable only from
// the proposal executor (the diagnostic DBQuery path stays sealed read-only).
func (m *Manager) dbExecStatement(ctx context.Context, src config.NetDevDBSource, stmt string) error {
	pass, ok, err := secretGetter(SecretKindPassword, src.PasswordEnv)
	if err != nil || !ok {
		return fmt.Errorf("secret %s not set", src.PasswordEnv)
	}
	driverName, err := migrationDriverName(src.Type)
	if err != nil {
		return err
	}
	db, err := sql.Open(driverName, dbDSN(src, pass))
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return err
	}
	return nil
}

// execSQLMigration runs the up script statement by statement. The down script
// is the rollback basis (validated submittable-only-with-one); each statement
// is audited.
func (m *Manager) execSQLMigration(ctx context.Context, s *ProposalStep) error {
	return m.runMigrationScript(ctx, s.Device, s.UpSQL, "proposal-write")
}

func (m *Manager) execSQLRollback(ctx context.Context, s *ProposalStep) error {
	return m.runMigrationScript(ctx, s.Device, s.DownSQL, "proposal-rollback")
}

func (m *Manager) runMigrationScript(ctx context.Context, source, script, auditClass string) error {
	src, ok := m.dbSourceByName(source)
	if !ok {
		return fmt.Errorf("db_source %q vanished from config", source)
	}
	for _, stmt := range splitSQLStatements(script) {
		err := m.dbExecStatement(ctx, src, stmt)
		st := AuditOK
		var msg string
		if err != nil {
			st, msg = AuditFailure, err.Error()
		}
		_ = AppendAudit(Audit{Device: "(db:" + src.Name + ")", Command: "sql " + firstLine(stmt), Class: auditClass, Status: st, Error: msg})
		if err != nil {
			return fmt.Errorf("statement %q: %w", firstLine(stmt), err)
		}
	}
	return nil
}

// ── file-upload / cert-replace（SSH exec 通道）──────────────────────────────

// sshCatFile reads a remote file over a one-shot exec channel (backup path).
func (m *Manager) sshCatFile(ctx context.Context, d config.NetDevDevice, remotePath string) (string, bool, error) {
	client, err := m.dialDeviceClient(ctx, d)
	if err != nil {
		return "", false, err
	}
	defer client.Close()
	res, err := client.ExecInput(ctx, "cat '"+remotePath+"'", nil)
	if err != nil {
		return "", false, err
	}
	if res.ExitCode != 0 {
		return "", false, nil // nonzero exit = absent/unreadable — caller decides
	}
	return string(res.Stdout), true, nil
}

// sshB64Upload streams content to remotePath through `base64 -d` on a one-shot
// exec channel and verifies the sha256 of what landed. This is the ONLY upload
// path in netdev (§6.2: uploads exist solely as proposal steps).
func (m *Manager) sshB64Upload(ctx context.Context, d config.NetDevDevice, content []byte, remotePath string) error {
	client, err := m.dialDeviceClient(ctx, d)
	if err != nil {
		return err
	}
	defer client.Close()
	if _, err := client.ExecInput(ctx, "base64 -d > '"+remotePath+"'", []byte(base64Of(content))); err != nil {
		return fmt.Errorf("upload %s: %w", remotePath, err)
	}
	res, err := client.ExecInput(ctx, "sha256sum '"+remotePath+"'", nil)
	if err != nil {
		return fmt.Errorf("checksum %s: %w", remotePath, err)
	}
	got := strings.Fields(strings.TrimSpace(string(res.Stdout)))
	if len(got) == 0 {
		return fmt.Errorf("checksum %s: empty sha256sum output", remotePath)
	}
	if got[0] != sha256Hex(content) {
		return fmt.Errorf("checksum %s mismatch: uploaded %s, remote %s", remotePath, sha256Hex(content), got[0])
	}
	return nil
}

func base64Of(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// execFileUpload: 备份目标现文件 → 上传 → 校验和验证（§6.2 流程）。
func (m *Manager) execFileUpload(ctx context.Context, d config.NetDevDevice, s *ProposalStep) error {
	content, err := os.ReadFile(s.LocalPath)
	if err != nil {
		return fmt.Errorf("read local %s: %w", s.LocalPath, err)
	}
	if len(content) > sftpMaxBytes {
		return fmt.Errorf("local file exceeds the %d MB cap", sftpMaxBytes>>20)
	}
	// Declared-checksum guard fires BEFORE anything is dialed or uploaded.
	if s.Checksum != "" && s.Checksum != sha256Hex(content) {
		return fmt.Errorf("local file sha256 %s does not match the declared checksum %s", sha256Hex(content), s.Checksum)
	}
	if cur, ok, err := m.sshCatFile(ctx, d, s.RemotePath); err != nil {
		return fmt.Errorf("backup read: %w", err)
	} else if ok {
		s.Backup = cur
	} else {
		s.Backup = absentMarker
	}
	_ = AppendAudit(Audit{Device: s.Device, Via: d.Via, Command: "file-upload " + s.LocalPath + " → " + s.RemotePath, Class: "proposal-write", Status: AuditOK})
	return m.sshB64Upload(ctx, d, content, s.RemotePath)
}

// execFileRestore writes the backed-up bytes back (or removes the file when it
// did not exist before the upload).
func (m *Manager) execFileRestore(ctx context.Context, d config.NetDevDevice, remotePath, backup string) error {
	if backup == "" {
		return fmt.Errorf("no backup captured for %s", remotePath)
	}
	if backup == absentMarker {
		client, err := m.dialDeviceClient(ctx, d)
		if err != nil {
			return err
		}
		defer client.Close()
		if _, err := client.ExecInput(ctx, "rm -f '"+remotePath+"'", nil); err != nil {
			return fmt.Errorf("remove %s: %w", remotePath, err)
		}
		_ = AppendAudit(Audit{Device: d.Name, Command: "file-restore(rm) " + remotePath, Class: "proposal-rollback", Status: AuditOK})
		return nil
	}
	_ = AppendAudit(Audit{Device: d.Name, Command: "file-restore " + remotePath, Class: "proposal-rollback", Status: AuditOK})
	return m.sshB64Upload(ctx, d, []byte(backup), remotePath)
}

// execCertReplace: 旧证书对备份 → 上传新证书+私钥 → reload（§7.1 表行 5）。
func (m *Manager) execCertReplace(ctx context.Context, d config.NetDevDevice, s *ProposalStep) error {
	cert, err := os.ReadFile(s.LocalPath)
	if err != nil {
		return fmt.Errorf("read cert %s: %w", s.LocalPath, err)
	}
	key, err := os.ReadFile(s.KeyLocalPath)
	if err != nil {
		return fmt.Errorf("read key %s: %w", s.KeyLocalPath, err)
	}
	// Back up the current pair (raw — restore PUTs these exact bytes).
	if cur, ok, err := m.sshCatFile(ctx, d, s.RemotePath); err != nil {
		return fmt.Errorf("cert backup: %w", err)
	} else {
		s.Backup = backupPair(cur, ok)
	}
	if cur, ok, err := m.sshCatFile(ctx, d, s.KeyRemotePath); err != nil {
		return fmt.Errorf("key backup: %w", err)
	} else {
		s.Backup += "\x00" + backupPair(cur, ok)
	}
	_ = AppendAudit(Audit{Device: s.Device, Via: d.Via, Command: "cert-replace " + s.RemotePath + " + reload", Class: "proposal-write", Status: AuditOK})
	if err := m.sshB64Upload(ctx, d, cert, s.RemotePath); err != nil {
		return err
	}
	if err := m.sshB64Upload(ctx, d, key, s.KeyRemotePath); err != nil {
		return err
	}
	return m.sshReload(ctx, d, s.ReloadCmd)
}

func backupPair(cur string, ok bool) string {
	if !ok {
		return absentMarker
	}
	return cur
}

func (m *Manager) sshReload(ctx context.Context, d config.NetDevDevice, reloadCmd string) error {
	client, err := m.dialDeviceClient(ctx, d)
	if err != nil {
		return err
	}
	defer client.Close()
	res, err := client.ExecInput(ctx, "sh -c '"+reloadCmd+"'", nil)
	_ = AppendAudit(Audit{Device: d.Name, Command: "reload " + reloadCmd, Class: "proposal-write",
		Status: auditStatus(Result{IsError: err != nil || res.ExitCode != 0}, err), Error: string(res.Stderr)})
	if err != nil || res.ExitCode != 0 {
		return fmt.Errorf("reload %q: exit %d: %.200s", reloadCmd, res.ExitCode, res.Stderr)
	}
	return nil
}

// execCertRestore: 恢复旧证书对 → reload（回滚依据 = 旧证书备份 + reload）。
func (m *Manager) execCertRestore(ctx context.Context, d config.NetDevDevice, s *ProposalStep) error {
	parts := strings.SplitN(s.Backup, "\x00", 2)
	if len(parts) != 2 {
		return fmt.Errorf("cert backup missing — cannot restore")
	}
	certBak, keyBak := parts[0], parts[1]
	if certBak == absentMarker {
		client, err := m.dialDeviceClient(ctx, d)
		if err != nil {
			return err
		}
		defer client.Close()
		if _, err := client.ExecInput(ctx, "rm -f '"+s.RemotePath+"'", nil); err != nil {
			return fmt.Errorf("remove old cert: %w", err)
		}
	} else if err := m.sshB64Upload(ctx, d, []byte(certBak), s.RemotePath); err != nil {
		return err
	}
	if keyBak == absentMarker {
		client, err := m.dialDeviceClient(ctx, d)
		if err != nil {
			return err
		}
		defer client.Close()
		if _, err := client.ExecInput(ctx, "rm -f '"+s.KeyRemotePath+"'", nil); err != nil {
			return fmt.Errorf("remove old key: %w", err)
		}
	} else if err := m.sshB64Upload(ctx, d, []byte(keyBak), s.KeyRemotePath); err != nil {
		return err
	}
	_ = AppendAudit(Audit{Device: d.Name, Command: "cert-restore " + s.RemotePath, Class: "proposal-rollback", Status: AuditOK})
	return m.sshReload(ctx, d, s.ReloadCmd)
}
