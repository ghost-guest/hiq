package netdev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev/driver"
)

// Proposal pipeline (NETDEV_SPEC §6.2–6.4): the ONLY write path. The agent
// may DRAFT (netdev_propose — a read-only act); a human APPROVES via the
// desktop settings/proposal UI (bridge methods, never a tool); execution
// rolls device-by-device (backup → apply → verify) and freezes on the first
// failure for human decision (Appendix B-2: no auto-rollback; the rollback
// plan is authored WITH the change and pressed by a human).

// Proposal statuses.
const (
	ProposalDraft     = "draft"
	ProposalApproved  = "approved"
	ProposalExecuting = "executing"
	ProposalDone      = "done"
	ProposalPartial   = "partial"  // some steps applied, later ones skipped — frozen for a human
	ProposalFailed    = "failed"   // rollback attempted and failed — alert
	ProposalRejected  = "rejected" // human vetoed (completion-spec §4.1): agent sees the reason next turn
)

// Structured step types (§7.1): the proposal step grew from a CLI command
// string into a discriminated union. Type "" behaves as cli everywhere — old
// proposals deserialize unchanged.
const (
	StepCLI           = "cli"
	StepK8sApply      = "k8s-apply"
	StepSQLMigration  = "sql-migration"
	StepFileUpload    = "file-upload"
	StepCertReplace   = "cert-replace"
	StepRestoreVerify = "restore-verify" // §7.3 备份恢复演练
)

// ProposalStep is one device's slice of a change.
type ProposalStep struct {
	Device   string   `json:"device"`
	Type     string   `json:"type,omitempty"`     // "" | cli | k8s-apply | sql-migration | file-upload | cert-replace
	Commands []string `json:"commands"`           // cli: the change, in order
	Rollback []string `json:"rollback,omitempty"` // cli: reverse commands, authored with the change

	// Structured payloads (§7.1) — only the fields of the step's type matter.
	YAML          string `json:"yaml,omitempty"`           // k8s-apply: full manifest
	UpSQL         string `json:"up_sql,omitempty"`         // sql-migration
	DownSQL       string `json:"down_sql,omitempty"`       // sql-migration — REQUIRED (missing ⇒ not submittable)
	LocalPath     string `json:"local_path,omitempty"`     // file-upload / cert-replace: local cert file
	RemotePath    string `json:"remote_path,omitempty"`    // file-upload / cert-replace: absolute target path
	KeyLocalPath  string `json:"key_local_path,omitempty"` // cert-replace: local private key file
	KeyRemotePath string `json:"key_remote_path,omitempty"`
	Checksum      string `json:"checksum,omitempty"`   // optional sha256 the uploaded bytes must match
	ReloadCmd     string `json:"reload_cmd,omitempty"` // cert-replace: service reload after the swap
	// restore-verify（§7.3 备份恢复演练）: restore a config snapshot to a
	// STAGING target and run the verify read. Device = receiver.
	RestoreDevice  string `json:"restore_device,omitempty"`  // snapshot source device
	RestoreVersion string `json:"restore_version,omitempty"` // snapshot id; "" = latest at execute time
	VerifyCmd      string `json:"verify_cmd,omitempty"`      // e.g. `nginx -t`

	Backup    string `json:"backup,omitempty"` // captured pre-change state (redacted)
	Applied   bool   `json:"applied"`
	Error     string `json:"error,omitempty"`
	Dangerous bool   `json:"dangerous,omitempty"` // destructive verb scan ⇒ forces confirm2 (§7.1)
}

// stepType normalizes the discriminator.
func stepType(s *ProposalStep) string {
	if s.Type == "" {
		return StepCLI
	}
	return s.Type
}

// Proposal is one change proposal.
const (
	ProposalWatching = "watching"
	ProposalClosed   = "closed"
)

type Proposal struct {
	ID         string         `json:"id"`
	Intent     string         `json:"intent"`
	Status     string         `json:"status"`
	Steps      []ProposalStep `json:"steps"`
	CreatedAt  time.Time      `json:"created_at"`
	ApprovedAt time.Time      `json:"approved_at,omitempty"`
	ExecutedAt time.Time      `json:"executed_at,omitempty"`
	Approver   string         `json:"approver,omitempty"`
	Confirm2   bool           `json:"confirm2"`       // secondary confirmation for proposal+confirm2 groups
	Note       string         `json:"note,omitempty"` // freeze/rollback reason trail
	// 驳回（completion-spec §4.1）：draft/approved 可被人否决；Reason 随提案
	// 持久化，agent 下一轮读到提案即见被拒原因。
	RejectedAt   time.Time `json:"rejected_at,omitempty"`
	RejectReason string    `json:"reject_reason,omitempty"`
	// 观察期（§7.1）：done → watching（默认 30 分钟）→ closed；劣化触发 Finding。
	WatchUntil *time.Time `json:"watch_until,omitempty"`
	WatchNote  string     `json:"watch_note,omitempty"`
	// HealthBase is the ifDown count per target device at watch start（-1 =
	// 当时不可达）——观察期劣化检测（健康轮询对比）的基线。仅 SNMP 配置
	// 设备有信号；其余设备跳过对比。
	HealthBase map[string]int `json:"health_base,omitempty"`
}

// proposalsDirOverride isolates proposal storage in tests.
var proposalsDirOverride string

// ProposalsDir stores one JSON per proposal under the netdev state dir.
func ProposalsDir() string {
	if proposalsDirOverride != "" {
		return proposalsDirOverride
	}
	return filepath.Join(netdevStateDir(), "proposals")
}

var (
	proposalMu  sync.Mutex
	proposalSeq int
	// proposalInflight marks ids with a long-running transition (execute,
	// rollback) so a concurrent delete can't yank the file mid-flight.
	proposalInflight = map[string]struct{}{}
)

// SaveProposal persists p (create or update).
func SaveProposal(p *Proposal) error {
	if p.ID == "" {
		p.ID = newProposalID()
	}
	if p.Status == "" {
		p.Status = ProposalDraft
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	if err := os.MkdirAll(ProposalsDir(), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	proposalMu.Lock()
	defer proposalMu.Unlock()
	return os.WriteFile(filepath.Join(ProposalsDir(), p.ID+".json"), b, 0o600)
}

// saveProposalLocked is SaveProposal with the caller holding proposalMu — the
// write half of an atomic load→check→set→save transition.
func saveProposalLocked(p *Proposal) error {
	if err := os.MkdirAll(ProposalsDir(), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(ProposalsDir(), p.ID+".json"), b, 0o600)
}

// claimProposalInflight reserves id for a long-running transition. false
// means one is already running.
func claimProposalInflight(id string) bool {
	proposalMu.Lock()
	defer proposalMu.Unlock()
	if _, busy := proposalInflight[id]; busy {
		return false
	}
	proposalInflight[id] = struct{}{}
	return true
}

func releaseProposalInflight(id string) {
	proposalMu.Lock()
	delete(proposalInflight, id)
	proposalMu.Unlock()
}

// GetProposal loads one proposal.
func GetProposal(id string) (*Proposal, error) {
	b, err := os.ReadFile(filepath.Join(ProposalsDir(), id+".json"))
	if err != nil {
		return nil, err
	}
	var p Proposal
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ListProposals returns proposals newest-first.
func ListProposals() ([]*Proposal, error) {
	entries, err := os.ReadDir(ProposalsDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Proposal
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if p, err := GetProposal(strings.TrimSuffix(e.Name(), ".json")); err == nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func newProposalID() string {
	proposalMu.Lock()
	defer proposalMu.Unlock()
	proposalSeq++
	return fmt.Sprintf("P%s-%d", time.Now().Format("20060102"), proposalSeq)
}

// ── policy gate ─────────────────────────────────────────────────────────────

// ValidateProposal checks a draft against the inventory policies: every step's
// device must exist, no step may target a read-only group, and each structured
// type's own contract must hold (§7.1 — e.g. sql-migration without a down
// script is not submittable). Group policy `proposal+confirm2` is legal but
// flags ApproveProposal for the secondary confirmation.
func (m *Manager) ValidateProposal(p *Proposal) error {
	if strings.TrimSpace(p.Intent) == "" {
		return errors.New("proposal: intent is required (what & why)")
	}
	if len(p.Steps) == 0 {
		return errors.New("proposal: no steps")
	}
	for i := range p.Steps {
		s := &p.Steps[i]
		d, ok := m.cfg.NetDevDeviceByName(s.Device)
		if !ok && stepType(s) != StepSQLMigration {
			return fmt.Errorf("proposal: step device %q not in inventory", s.Device)
		}
		if ok && d.Group != "" {
			if g, ok := m.cfg.NetDevGroupByName(d.Group); ok && g.Policy == config.NetDevPolicyReadOnly {
				return fmt.Errorf("proposal: device %q is in read-only group %q — proposals are not allowed", s.Device, d.Group)
			}
		}
		if err := m.validateStep(s, d); err != nil {
			return err
		}
	}
	return nil
}

// validateStep enforces one step's type contract (§7.1 表：载荷与回滚依据).
func (m *Manager) validateStep(s *ProposalStep, d config.NetDevDevice) error {
	s.Dangerous = dangerScan(s)
	switch stepType(s) {
	case StepCLI:
		if len(s.Commands) == 0 {
			return fmt.Errorf("proposal: step for %q has no commands", s.Device)
		}
		if len(s.Rollback) == 0 {
			return fmt.Errorf("proposal: step for %q has no rollback plan (authored with the change)", s.Device)
		}
	case StepK8sApply:
		if d.Kind != "k8s" {
			return fmt.Errorf("proposal: k8s-apply step for %q requires a kind=k8s target", s.Device)
		}
		if strings.TrimSpace(s.YAML) == "" {
			return fmt.Errorf("proposal: k8s-apply step for %q has no manifest", s.Device)
		}
		if _, err := kubeResourcePath(s.YAML, "default"); err != nil {
			return fmt.Errorf("proposal: k8s-apply step for %q: %v", s.Device, err)
		}
	case StepSQLMigration:
		src, ok := m.dbSourceByName(s.Device)
		if !ok {
			return fmt.Errorf("proposal: sql-migration step target %q is not a [[netdev.db_sources]] entry", s.Device)
		}
		switch src.Type {
		case "mysql", "postgres", "mssql":
		default:
			return fmt.Errorf("proposal: sql-migration on %q: engine %q not supported (v1: mysql/postgres/mssql)", s.Device, src.Type)
		}
		if strings.TrimSpace(s.UpSQL) == "" {
			return fmt.Errorf("proposal: sql-migration step for %q has no up script", s.Device)
		}
		if strings.TrimSpace(s.DownSQL) == "" {
			return fmt.Errorf("proposal: sql-migration step for %q has no down script — the type is not submittable without one (§7.1)", s.Device)
		}
	case StepFileUpload, StepCertReplace:
		if d.Vendor != "linux" {
			return fmt.Errorf("proposal: %s step for %q requires a linux SSH target (v1: exec-channel upload)", stepType(s), s.Device)
		}
		if err := validateUploadPaths(s.Device, s.LocalPath, s.RemotePath); err != nil {
			return err
		}
		if stepType(s) == StepCertReplace {
			if err := validateUploadPaths(s.Device, s.KeyLocalPath, s.KeyRemotePath); err != nil {
				return fmt.Errorf("proposal: cert-replace step for %q key pair: %v", s.Device, err)
			}
			if strings.TrimSpace(s.ReloadCmd) == "" {
				return fmt.Errorf("proposal: cert-replace step for %q has no reload command (rollback must be able to restore + reload)", s.Device)
			}
		}
	case StepRestoreVerify:
		if err := m.validateRestoreVerify(s, d); err != nil {
			return err
		}
	default:
		return fmt.Errorf("proposal: step for %q has unknown type %q", s.Device, s.Type)
	}
	return nil
}

// ProposalNeedsConfirm2 reports whether any step demands the secondary
// confirmation: a proposal+confirm2 group (§6.3) OR a step whose verbs scanned
// as destructive (§7.1 — delete/scale-down 类动词落 dangerous + confirm2).
func (m *Manager) ProposalNeedsConfirm2(p *Proposal) bool {
	for i := range p.Steps {
		s := &p.Steps[i]
		if s.Dangerous || dangerScan(s) {
			return true
		}
		if d, ok := m.cfg.NetDevDeviceByName(s.Device); ok && d.Group != "" {
			if g, ok := m.cfg.NetDevGroupByName(d.Group); ok && g.Policy == config.NetDevPolicyProposalConf {
				return true
			}
		}
	}
	return false
}

// changeWindow matches "tue,thu 22:00-24:00" (local time). Empty = always.
type changeWindow struct {
	days       map[string]bool // 3-letter lowercase weekdays
	start, end int             // minutes since midnight
}

var windowRe = regexp.MustCompile(`^\s*([a-z]{3}(?:\s*,\s*[a-z]{3})*)\s+(\d{1,2}):(\d{2})-(\d{1,2}):(\d{2})\s*$`)
var weekdayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func parseChangeWindow(s string) (*changeWindow, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return nil, nil
	}
	m := windowRe.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("change_window %q: want \"tue,thu 22:00-24:00\"", s)
	}
	w := &changeWindow{days: map[string]bool{}}
	for _, d := range strings.Split(m[1], ",") {
		d = strings.TrimSpace(d)
		valid := false
		for _, n := range weekdayNames {
			if d == n {
				valid = true
				break
			}
		}
		if !valid {
			return nil, fmt.Errorf("change_window: unknown day %q", d)
		}
		w.days[d] = true
	}
	w.start = atoi(m[2])*60 + atoi(m[3])
	w.end = atoi(m[4])*60 + atoi(m[5])
	if w.end <= w.start {
		return nil, fmt.Errorf("change_window %q: end must be after start", s)
	}
	return w, nil
}

func (w *changeWindow) contains(t time.Time) bool {
	if w == nil {
		return true
	}
	day := weekdayNames[int(t.Weekday())]
	if !w.days[day] {
		return false
	}
	mins := t.Hour()*60 + t.Minute()
	return mins >= w.start && mins <= w.end
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// ApproveProposal is the human gate. It enforces: current status draft, group
// policies (confirm2 when demanded), and the change window of every involved
// group. The agent has NO path here — approval arrives only from the desktop
// bridge with the human's click. The load→check→set→save runs as one critical
// section so a racing reject/execute can't interleave (last-write-wins used
// to silently drop a transition).
func (m *Manager) ApproveProposal(id string, confirm2 bool) (*Proposal, error) {
	proposalMu.Lock()
	defer proposalMu.Unlock()
	p, err := GetProposal(id)
	if err != nil {
		return nil, err
	}
	if p.Status != ProposalDraft {
		return nil, fmt.Errorf("proposal %s: status %s, only drafts can be approved", id, p.Status)
	}
	if m.ProposalNeedsConfirm2(p) && !confirm2 {
		return nil, fmt.Errorf("proposal %s demands secondary confirmation (group policy proposal+confirm2)", id)
	}
	// Change window: every involved group's window must contain now.
	for _, s := range p.Steps {
		d, ok := m.cfg.NetDevDeviceByName(s.Device)
		if !ok {
			return nil, fmt.Errorf("proposal %s: device %q vanished from inventory", id, s.Device)
		}
		if d.Group == "" {
			continue
		}
		g, ok := m.cfg.NetDevGroupByName(d.Group)
		if !ok {
			continue
		}
		w, err := parseChangeWindow(g.ChangeWindow)
		if err != nil {
			return nil, fmt.Errorf("proposal %s: %v", id, err)
		}
		if !w.contains(time.Now()) {
			return nil, fmt.Errorf("proposal %s: group %q change window (%s) is closed — approval blocked", id, d.Group, g.ChangeWindow)
		}
	}
	StateEventSnap(StateEventApprove, id, StateActorUser, filepath.Join(ProposalsDir(), id+".json"))
	p.Status = ProposalApproved
	p.ApprovedAt = time.Now()
	p.Approver = "local-user"
	p.Confirm2 = confirm2
	if err := saveProposalLocked(p); err != nil {
		return nil, err
	}
	_ = AppendAudit(Audit{Device: "(proposal)", Command: "approve " + id, Class: "proposal", Status: AuditOK})
	return p, nil
}

// RejectProposal is the human veto (completion-spec §4.1): the write path's
// gate could only say yes — this is the "no". draft/approved may be rejected
// (approved-but-not-executed vetoes cost nothing; executing states are past
// the point of veto and must ride rollback instead). The reason is persisted
// on the proposal so the agent's next turn sees WHY it was turned down.
func (m *Manager) RejectProposal(id, reason string) (*Proposal, error) {
	proposalMu.Lock()
	defer proposalMu.Unlock()
	p, err := GetProposal(id)
	if err != nil {
		return nil, err
	}
	if p.Status != ProposalDraft && p.Status != ProposalApproved {
		return nil, fmt.Errorf("proposal %s: status %s, only draft/approved proposals can be rejected (executing ones ride rollback)", id, p.Status)
	}
	StateEventSnap(StateEventReject, id, StateActorUser, filepath.Join(ProposalsDir(), id+".json"))
	p.Status = ProposalRejected
	p.RejectedAt = time.Now()
	p.RejectReason = strings.TrimSpace(reason)
	if p.RejectReason == "" {
		p.RejectReason = "(no reason given)"
	}
	// Note is the human-readable trail the existing UI already renders.
	p.Note = "驳回：" + p.RejectReason
	if err := saveProposalLocked(p); err != nil {
		return nil, err
	}
	_ = AppendAudit(Audit{Device: "(proposal)", Command: "reject " + id, Class: "proposal", Status: AuditFailure, Error: p.RejectReason})
	return p, nil
}

// DeleteProposal removes a proposal file. Only draft and terminal states
// (rejected/done/failed/closed) may be deleted — the live pipeline
// (approved/executing/partial/watching) must stay auditable. partial keeps
// its record because the applied steps are still on the devices.
func (m *Manager) DeleteProposal(id string) error {
	proposalMu.Lock()
	defer proposalMu.Unlock()
	if _, busy := proposalInflight[id]; busy {
		return fmt.Errorf("proposal %s: a transition is running (execute/rollback) — wait for it to finish before deleting", id)
	}
	p, err := GetProposal(id)
	if err != nil {
		return err
	}
	switch p.Status {
	case ProposalDraft, ProposalRejected, ProposalDone, ProposalFailed, ProposalClosed:
	default:
		return fmt.Errorf("proposal %s: status %s is in the live pipeline — reject or close it before deleting", id, p.Status)
	}
	StateEventSnap(StateEventDelete, id, StateActorUser, filepath.Join(ProposalsDir(), id+".json"))
	if err := os.Remove(filepath.Join(ProposalsDir(), id+".json")); err != nil {
		return err
	}
	_ = AppendAudit(Audit{Device: "(proposal)", Command: "delete " + id, Class: "proposal", Status: AuditOK})
	return nil
}

// backupCommand returns the driver's running-config dump command.
func backupCommand(drvKey string) string {
	switch drvKey {
	case "huawei-vrp":
		return "display current-configuration"
	case "cisco-ios":
		return "show running-config"
	default:
		return ""
	}
}

// ExecuteProposal rolls the approved change device-by-device: backup → apply
// → mark. The FIRST failure freezes the proposal as partial (later steps
// untouched) — a human then decides rollback (which runs the authored plan
// over the already-applied steps) or keep. There is no automatic rollback.
func (m *Manager) ExecuteProposal(ctx context.Context, id string) (*Proposal, error) {
	p, err := GetProposal(id)
	if err != nil {
		return nil, err
	}
	if p.Status != ProposalApproved {
		return nil, fmt.Errorf("proposal %s: status %s, only approved proposals execute", id, p.Status)
	}
	StateEventSnap(StateEventExecute, id, stateActorFromCtx(ctx), filepath.Join(ProposalsDir(), id+".json"))

	// 执行前在线检查（§7.1）：发现其他在线人员则**暂停并列出会话**，人确
	// 认后才继续——「我要变更，但同事正登着」是最常见的协作事故。确认语
	// 义 = 再次点击执行：本次会话清单已记入 Note，下一次执行看到同样的清
	// 单即视为已确认（会话有变化则重新要求确认）。
	online := m.preExecOnlineCheck(ctx, p)
	if online != "" && !strings.Contains(p.Note, "[在线人员] "+online) {
		p.Note = strings.TrimSpace(p.Note + "\n[在线人员] " + online)
		_ = SaveProposal(p)
		return p, fmt.Errorf("执行前确认：目标设备上有其他在线人员（%s）——确认没人正操作后再次点击「执行」（清单已记入提案备注；会话有变化会再次要求确认）", online)
	}

	// Claim approved→executing atomically: a racing approve/reject used to
	// interleave with this write (last-write-wins dropped a transition), and
	// the in-flight mark keeps delete away for the run's duration.
	proposalMu.Lock()
	p, err = GetProposal(id) // reload: the online check may have persisted a Note
	if err != nil {
		proposalMu.Unlock()
		return nil, err
	}
	if p.Status != ProposalApproved {
		proposalMu.Unlock()
		return nil, fmt.Errorf("proposal %s: status %s, only approved proposals execute", id, p.Status)
	}
	if _, busy := proposalInflight[id]; busy {
		proposalMu.Unlock()
		return nil, fmt.Errorf("proposal %s: a transition is already running", id)
	}
	proposalInflight[id] = struct{}{}
	p.Status = ProposalExecuting
	p.ExecutedAt = time.Now()
	if err := saveProposalLocked(p); err != nil {
		delete(proposalInflight, id)
		proposalMu.Unlock()
		return nil, err
	}
	proposalMu.Unlock()
	defer releaseProposalInflight(id)

	frozen := ""
	for i := range p.Steps {
		s := &p.Steps[i]

		// Structured types carry their own backup/apply/verify semantics
		// (§7.1); cli keeps the classic driver path.
		switch stepType(s) {
		case StepSQLMigration:
			if err := m.execSQLMigration(ctx, s); err != nil {
				s.Error = err.Error()
				frozen = s.Error
			} else {
				s.Applied = true
			}
		case StepK8sApply, StepFileUpload, StepCertReplace, StepRestoreVerify:
			d, ok := m.cfg.NetDevDeviceByName(s.Device)
			if !ok {
				s.Error = "device vanished from inventory"
				frozen = s.Error
				break
			}
			var err error
			switch stepType(s) {
			case StepK8sApply:
				err = m.execK8sApply(ctx, s)
			case StepFileUpload:
				err = m.execFileUpload(ctx, d, s)
			case StepCertReplace:
				err = m.execCertReplace(ctx, d, s)
			case StepRestoreVerify:
				err = m.execRestoreVerify(ctx, d, s)
			}
			if err != nil {
				s.Error = err.Error()
				frozen = s.Error
			} else {
				s.Applied = true
			}
		default: // cli
			d, ok := m.cfg.NetDevDeviceByName(s.Device)
			if !ok {
				s.Error = "device vanished from inventory"
				frozen = s.Error
				break
			}
			drv, ok := m.driverFor(d)
			if !ok {
				s.Error = "no driver"
				frozen = s.Error
				break
			}

			// Backup (redacted; stored with the proposal for human recovery).
			if bc := backupCommand(drv.Key()); bc != "" {
				if res, err := m.runUnclassified(ctx, d, drv, bc); err == nil && !res.IsError {
					s.Backup = Redact(res.Output)
				} else {
					s.Error = "backup failed: " + firstLine(errText(err, res))
					frozen = s.Error
					break
				}
			}

			// Apply (the only write path in netdev; audited per command).
			for _, cmd := range s.Commands {
				res, err := m.runUnclassified(ctx, d, drv, cmd)
				_ = AppendAudit(Audit{
					Device: s.Device, Via: d.Via, Command: cmd, Class: "proposal-write",
					Status: auditStatus(res, err), OutputBytes: len(res.Output),
					Error: errText(err, res),
				})
				if err != nil || res.IsError {
					s.Error = fmt.Sprintf("command %q failed: %s", cmd, firstLine(errText(err, res)))
					frozen = s.Error
					break
				}
			}
			if frozen == "" {
				s.Applied = true
			}
		}
		if frozen != "" {
			break
		}
		if err := SaveProposal(p); err != nil {
			return nil, err
		}
	}

	if frozen != "" {
		p.Status = ProposalPartial
		p.Note = "frozen: " + frozen + " — later steps untouched; a human decides rollback or keep"
	} else {
		p.Status = ProposalDone
		// 观察期（§7.1）：默认 30 分钟后自动 closed；观察期内的劣化检测由
		// 健康轮询承担（checkWatchingProposals——与 watch 起点基线对比，
		// 劣化即最高级 Finding + 回滚提示，回滚仍需人批准）。
		wu := time.Now().Add(30 * time.Minute)
		p.WatchUntil = &wu
		p.HealthBase = watchHealthBase(p)
			go func(id string, until time.Time) {
				time.Sleep(time.Until(until))
				proposalMu.Lock()
				defer proposalMu.Unlock()
				if pp, err := GetProposal(id); err == nil && pp.Status == ProposalWatching {
					StateEventSnap(StateEventCloseWatch, id, StateActorSystem, filepath.Join(ProposalsDir(), id+".json"))
					pp.Status = ProposalClosed
					pp.WatchNote = "观察期满，自动关闭"
					_ = saveProposalLocked(pp)
				}
			}(p.ID, wu)
	}
	if err := SaveProposal(p); err != nil {
		return nil, err
	}
	_ = AppendAudit(Audit{Device: "(proposal)", Command: "execute " + id, Class: "proposal", Status: auditStatusFor(p.Status)})
	return p, nil
}

// RollbackProposal runs the authored rollback plan over the APPLIED steps
// only, oldest-last so state unwinds in reverse. Frozen/failed proposals only.
// A rollback failure marks the proposal failed (alert) and stops.
func (m *Manager) RollbackProposal(ctx context.Context, id string) (*Proposal, error) {
	// The rollback loop runs device I/O for minutes — claim the proposal so a
	// concurrent rollback or delete can't interleave (double-rollback would run
	// the reverse commands twice).
	proposalMu.Lock()
	if _, busy := proposalInflight[id]; busy {
		proposalMu.Unlock()
		return nil, fmt.Errorf("proposal %s: a transition is already running", id)
	}
	proposalInflight[id] = struct{}{}
	proposalMu.Unlock()
	defer releaseProposalInflight(id)

	p, err := GetProposal(id)
	if err != nil {
		return nil, err
	}
	if p.Status != ProposalPartial && p.Status != ProposalDone {
		return nil, fmt.Errorf("proposal %s: status %s — only partial/done proposals roll back", id, p.Status)
	}
	StateEventSnap(StateEventRollback, id, stateActorFromCtx(ctx), filepath.Join(ProposalsDir(), id+".json"))
	for i := len(p.Steps) - 1; i >= 0; i-- {
		s := &p.Steps[i]
		if !s.Applied {
			continue
		}
		var rerr error
		switch stepType(s) {
		case StepSQLMigration:
			rerr = m.execSQLRollback(ctx, s)
		case StepK8sApply:
			rerr = m.execK8sRestore(ctx, s)
		case StepFileUpload:
			if d, ok := m.cfg.NetDevDeviceByName(s.Device); ok {
				rerr = m.execFileRestore(ctx, d, s.RemotePath, s.Backup)
			}
		case StepCertReplace:
			if d, ok := m.cfg.NetDevDeviceByName(s.Device); ok {
				rerr = m.execCertRestore(ctx, d, s)
			}
		case StepRestoreVerify:
			if d, ok := m.cfg.NetDevDeviceByName(s.Device); ok {
				rerr = m.execFileRestore(ctx, d, s.RemotePath, s.Backup)
			}
		default: // cli
			d, ok := m.cfg.NetDevDeviceByName(s.Device)
			if !ok {
				continue
			}
			drv, ok := m.driverFor(d)
			if !ok {
				continue
			}
			for _, cmd := range s.Rollback {
				res, err := m.runUnclassified(ctx, d, drv, cmd)
				_ = AppendAudit(Audit{
					Device: s.Device, Via: d.Via, Command: cmd, Class: "proposal-rollback",
					Status: auditStatus(res, err), Error: errText(err, res),
				})
				if err != nil || res.IsError {
					rerr = fmt.Errorf("%q: %s", cmd, firstLine(errText(err, res)))
					break
				}
			}
		}
		if rerr != nil {
			p.Status = ProposalFailed
			p.Note = "rollback FAILED on " + s.Device + ": " + firstLine(rerr.Error()) + " — manual recovery required; backups are stored in the proposal"
			if err := SaveProposal(p); err != nil {
				return nil, err
			}
			return p, nil
		}
		s.Applied = false
	}
	p.Status = ProposalDraft // rolled back cleanly; re-approval required to try again
	p.Note = "rolled back via the authored plan"
	if err := SaveProposal(p); err != nil {
		return nil, err
	}
	return p, nil
}

// runUnclassified runs ANY command on the device's session, bypassing the
// read-only classifier. This is the executor's private write path — the ONLY
// caller group outside Run's seal, and every command through it is audited by
// the callers above. Nothing reachable from the agent calls this.
func (m *Manager) runUnclassified(ctx context.Context, d config.NetDevDevice, drv driver.Driver, cmd string) (Result, error) {
	res, err := m.runRead(ctx, d, drv, cmd)
	if err != nil {
		return Result{}, err
	}
	return Result{Command: cmd, Output: res.Output, IsError: res.IsError}, nil
}

func auditStatus(res Result, err error) string {
	switch {
	case err != nil:
		return AuditFailure
	case res.IsError:
		return AuditDeviceError
	default:
		return AuditOK
	}
}

func auditStatusFor(status string) string {
	if status == ProposalDone {
		return AuditOK
	}
	return AuditFailure
}

func errText(err error, res Result) string {
	if err != nil {
		return err.Error()
	}
	if res.IsError {
		return firstLine(res.Output)
	}
	return ""
}

// preExecOnlineCheck runs who/quser on every unique target device — the
// "someone else is on the box" guard (§7.1 执行前置检查). Returns a summary
// line, empty when nobody else is on any target.
func (m *Manager) preExecOnlineCheck(ctx context.Context, p *Proposal) string {
	seen := map[string]bool{}
	for _, s := range p.Steps {
		if s.Device == "" || seen[s.Device] {
			continue
		}
		seen[s.Device] = true
	}
	var findings []string
	for name := range seen {
		d, ok := m.cfg.NetDevDeviceByName(name)
		if !ok {
			continue
		}
		var cmd string
		switch d.Vendor {
		case "linux", "vmware":
			cmd = "who"
		case "windows":
			cmd = "quser"
		default:
			continue // network CLIs have no meaningful "who"; VTY occupancy is live panel's
		}
		res := m.Exec(ctx, name, cmd)
		if res.Refused || res.IsError {
			continue
		}
		lines := strings.TrimSpace(res.Output)
		if lines == "" {
			continue
		}
		n := len(strings.Split(lines, "\n"))
		findings = append(findings, fmt.Sprintf("%s: %d 个会话", name, n))
	}
	return strings.Join(findings, "；")
}

// CloseProposalWatch manually ends the watching period.
func CloseProposalWatch(id string) error {
	proposalMu.Lock()
	defer proposalMu.Unlock()
	p, err := GetProposal(id)
	if err != nil {
		return err
	}
	if p.Status != ProposalWatching {
		return fmt.Errorf("proposal %s: status %s, only watching proposals close", id, p.Status)
	}
	StateEventSnap(StateEventCloseWatch, id, StateActorUser, filepath.Join(ProposalsDir(), id+".json"))
	p.Status = ProposalClosed
	p.WatchNote = "人工关闭"
	return saveProposalLocked(p)
}

// ── 观察期劣化检测（§7.1）─────────────────────────────────────────────────

// watchHealthBase snapshots each step device's ifDown count from the LAST
// health poll (-1 = unreachable). Devices without SNMP health have no signal
// and are simply absent — the comparison skips them.
func watchHealthBase(p *Proposal) map[string]int {
	seen := map[string]bool{}
	for _, s := range p.Steps {
		if s.Device != "" {
			seen[s.Device] = true
		}
	}
	base := map[string]int{}
	healthMu.Lock()
	for name := range seen {
		h, ok := healthState[name]
		if !ok {
			continue
		}
		if !h.Reachable {
			base[name] = -1
		} else {
			base[name] = h.IfDown()
		}
	}
	healthMu.Unlock()
	if len(base) == 0 {
		return nil
	}
	return base
}

// watchDegraded reports the devices whose health degraded vs the watch base:
// was reachable → now down, or ifDown grew.
func watchDegraded(p *Proposal, fresh map[string]DeviceHealth) []string {
	var worse []string
	for dev, base := range p.HealthBase {
		h, ok := fresh[dev]
		if !ok || !h.Reachable && base == -1 {
			continue // no fresh signal / already down at base
		}
		if base >= 0 && !h.Reachable {
			worse = append(worse, dev+"（失联）")
			continue
		}
		if h.Reachable && h.IfDown() > base {
			worse = append(worse, fmt.Sprintf("%s（down 口 %d→%d）", dev, base, h.IfDown()))
		}
	}
	sort.Strings(worse)
	return worse
}

// checkWatchingProposals compares every watching proposal's targets against
// the fresh health sweep; the FIRST degradation raises a top-severity Finding
// with the rollback hint (回滚仍需人批准——AI 的手永远慢一步) and marks the
// WatchNote so the alert fires once per proposal.
func (m *Manager) checkWatchingProposals(fresh map[string]DeviceHealth) {
	props, err := ListProposals()
	if err != nil {
		return
	}
	for _, p := range props {
		if p.Status != ProposalWatching || len(p.HealthBase) == 0 {
			continue
		}
		if strings.Contains(p.WatchNote, "劣化") {
			continue // already alerted
		}
		worse := watchDegraded(p, fresh)
		if len(worse) == 0 {
			continue
		}
		// Re-check-and-write under the lock: a concurrent watch-close or
		// auto-close used to interleave with this save.
		proposalMu.Lock()
		cur, err := GetProposal(p.ID)
		if err != nil || cur.Status != ProposalWatching || strings.Contains(cur.WatchNote, "劣化") {
			proposalMu.Unlock()
			continue
		}
		cur.WatchNote = "观察期劣化：" + strings.Join(worse, "；") + " — 可回滚（提案页签「回滚」，仍需人按）"
		_ = saveProposalLocked(cur)
		proposalMu.Unlock()
		_ = SaveFinding(&Finding{
			Title:      "变更观察期劣化：" + p.Intent,
			Severity:   SeverityCritical,
			Devices:    p.stepDevices(),
			Detail:     fmt.Sprintf("提案 %s 执行后观察期内健康劣化：%s。基线（watch 起点）若与变更相关，回滚是第一优先动作。", p.ID, strings.Join(worse, "；")),
			Evidence:   []Evidence{{Device: "(watch)", Command: "proposal " + p.ID, Output: p.WatchNote}},
			Suggestion: "在「提案」页签对 " + p.ID + " 按「回滚」执行已起草的回滚计划（回滚仍需人批准）",
			Source:     "watch:" + p.ID,
			Status:     FindingActive,
		})
		_ = AppendAudit(Audit{Device: "(proposal)", Command: "watch-degraded " + p.ID, Class: "proposal", Status: AuditFailure, Error: strings.Join(worse, "；")})
	}
}

// stepDevices lists the proposal's unique step target devices.
func (p *Proposal) stepDevices() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range p.Steps {
		if s.Device != "" && !seen[s.Device] {
			seen[s.Device] = true
			out = append(out, s.Device)
		}
	}
	return out
}
