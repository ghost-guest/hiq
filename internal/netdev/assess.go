package netdev

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev/transport"
)

// Assessment mode (NETDEV_SPEC §6.2): gated by the engagement envelope —
// id + scopes + expiry + approver in the USER config. Weak-credential checking
// is tiered (Appendix B-9): "basic" runs a fixed small candidate set within
// the per-device budget; "dictionary" consumes a USER-SUPPLIED dictionary
// (hiq ships none) and needs the explicit larger budget. Every attempt
// is a full SSH auth dial and is audited; devices lock accounts after N
// failures, so budgets are hard caps, not suggestions.

// AssessmentActive validates the engagement envelope. nil = active.
func AssessmentActive(nd config.NetDevConfig) error {
	if strings.TrimSpace(nd.Assessment.EngagementID) == "" {
		return errors.New("assessment mode requires an engagement envelope ([netdev.assessment] engagement_id)")
	}
	exp := strings.TrimSpace(nd.Assessment.Expires)
	if exp == "" {
		return errors.New("assessment engagement has no expiry — set [netdev.assessment] expires")
	}
	t, err := time.ParseInLocation("2006-01-02", exp, time.Local)
	if err != nil {
		return fmt.Errorf("assessment expires %q: want YYYY-MM-DD", exp)
	}
	if time.Now().After(t.AddDate(0, 0, 1)) {
		return fmt.Errorf("assessment engagement %s expired on %s", nd.Assessment.EngagementID, exp)
	}
	return nil
}

// Weak-credential tiers.
const (
	WeakTierBasic     = "basic"      // fixed vendor-default/empty/username set
	WeakTierDict      = "dictionary" // user-supplied dictionary file
	weakBudgetBasic   = 3
	weakBudgetDictMax = 10 // hard cap even for dictionary tier (lockout guard)
)

// basicCandidates is the fixed basic-tier set (<= weakBudgetBasic entries).
func basicCandidates(username string) []string {
	set := []string{"", username, "admin"}
	out := make([]string, 0, weakBudgetBasic)
	seen := map[string]bool{}
	for _, c := range set {
		if !seen[c] && len(out) < weakBudgetBasic {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// WeakCredResult reports one device's check.
type WeakCredResult struct {
	Device   string `json:"device"`
	Tier     string `json:"tier"`
	Weak     bool   `json:"weak"`
	Attempts int    `json:"attempts"`
	Budget   int    `json:"budget"`
	Detail   string `json:"detail,omitempty"`
}

// WeakCredCheck tests a device's login against the tier's candidate set.
// Gated on the engagement envelope; every attempt audited (password NEVER
// logged — only the attempt ordinal).
func (m *Manager) WeakCredCheck(ctx context.Context, deviceName, tier, dictPath string) (WeakCredResult, error) {
	if err := AssessmentActive(m.cfg.NetDev); err != nil {
		return WeakCredResult{}, fmt.Errorf("assess mode gate: %w", err)
	}
	d, ok := m.cfg.NetDevDeviceByName(deviceName)
	if !ok {
		return WeakCredResult{}, fmt.Errorf("device %q not in inventory", deviceName)
	}

	var candidates []string
	budget := weakBudgetBasic
	switch tier {
	case "", WeakTierBasic:
		tier = WeakTierBasic
		candidates = basicCandidates(strings.TrimSpace(d.Username))
	case WeakTierDict:
		tier = WeakTierDict
		lines, err := readDict(dictPath)
		if err != nil {
			return WeakCredResult{}, err
		}
		candidates = lines
		budget = weakBudgetDictMax
		if len(candidates) > budget {
			candidates = candidates[:budget]
		}
	default:
		return WeakCredResult{}, fmt.Errorf("tier must be basic|dictionary")
	}

	res := WeakCredResult{Device: deviceName, Tier: tier, Budget: budget}
	for i, cand := range candidates {
		res.Attempts = i + 1
		ok, err := m.dialAuth(ctx, d, cand)
		status := AuditOK
		detail := "attempt " + fmt.Sprint(i+1) + " rejected"
		if err != nil {
			status = AuditFailure
			detail = "attempt " + fmt.Sprint(i+1) + " transport error: " + err.Error()
		} else if ok {
			res.Weak = true
			res.Detail = fmt.Sprintf("weak credential confirmed after %d attempt(s) — change it via a proposal", res.Attempts)
			_ = AppendAudit(Audit{Device: deviceName, Command: "weak-cred-check (" + tier + ") attempt " + fmt.Sprint(i+1) + ": CONFIRMED", Class: "assess", Status: AuditDeviceError})
			return res, nil
		}
		_ = AppendAudit(Audit{Device: deviceName, Command: "weak-cred-check (" + tier + ") attempt " + fmt.Sprint(i+1), Class: "assess", Status: status, Error: detail})
	}
	res.Detail = fmt.Sprintf("no weak credential in %d attempt(s) (budget %d)", res.Attempts, budget)
	return res, nil
}

// dialAuth attempts one SSH login with the candidate password. Host keys ride
// the normal TOFU policy (HostKeyPrompt / managed file).
func (m *Manager) dialAuth(ctx context.Context, d config.NetDevDevice, password string) (bool, error) {
	lookup := m.lookupEntry()
	resolved, err := transport.ResolveHost(lookup, d.Name, nil)
	if err != nil {
		return false, err
	}
	// Devices behind bastions must be checked through their route too — the
	// jump hosts authenticate with their OWN stored credentials, never with
	// the candidate under test.
	jumps, err := transport.ResolveJumpHosts(lookup, d.Via, nil)
	if err != nil {
		return false, err
	}
	hops := make([]transport.JumpHostOptions, 0, len(jumps))
	for i, j := range jumps {
		hopCfg := m.hopByRaw(d.Via[i])
		hops = append(hops, transport.JumpHostOptions{Host: j, Auth: transport.AuthOptions{
			Password:   secretReader(SecretKindPassword, hopCfg.PasswordEnv),
			Passphrase: secretReader(SecretKindPassphrase, hopCfg.PassphraseEnv),
		}})
	}
	client, err := transport.New(transport.Options{
		Host:        resolved,
		Auth:        transport.AuthOptions{Password: func() (string, error) { return password, nil }},
		JumpHosts:   hops,
		HostKeys:    &transport.HostKeyPolicy{Prompt: HostKeyPrompt, ManagedPath: transport.ManagedKnownHostsOverride},
		DialTimeout: 8 * time.Second,
	})
	if err != nil {
		return false, err
	}
	if err := client.Start(ctx); err != nil {
		if errors.Is(err, transport.ErrAuthFailed) {
			client.Close()
			return false, nil // candidate rejected — expected outcome
		}
		client.Close()
		return false, nil // transient errors count as a spent attempt (conservative)
	}
	client.Close()
	return true, nil // LOGIN SUCCEEDED with the candidate — weak
}

func readDict(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("dictionary: %w", err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("dictionary %s has no candidates", path)
	}
	return out, sc.Err()
}

// ── agent tool surface ───────────────────────────────────────────────────────

// assessTool exposes the engagement-gated weak-credential check to the agent.
// NOT read-only in effect: every candidate is a full SSH auth dial (devices
// may lock accounts), so the tool reports ReadOnly=false and conservative
// approval surfaces treat it as an active operation.
type assessTool struct{ m *Manager }

func (t *assessTool) Name() string { return "netdev_assess" }

func (t *assessTool) Description() string {
	return "Assessment-mode weak-credential check (NETDEV_SPEC §6.2): test a device's SSH login against the tier's candidate passwords. " +
		"Gated on the [netdev.assessment] engagement envelope (engagement_id + expires) — refused without an ACTIVE engagement; the user configures it in 设置 → 运维中配置. " +
		"basic tier = fixed ≤3 candidates (empty/username/admin); dictionary tier = a user-supplied file, hard-capped at 10 (lockout guard). " +
		"Every attempt is a full auth dial and is audited (passwords never logged). A confirmed weak credential is reported for the user to fix VIA A PROPOSAL — never changed directly."
}

func (t *assessTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"device": {"type": "string", "description": "device name from netdev_devices"},
			"tier": {"type": "string", "enum": ["basic", "dictionary"], "description": "basic = fixed ≤3 candidates (default); dictionary = user-supplied dict_path, capped at 10"},
			"dict_path": {"type": "string", "description": "dictionary file path (tier=dictionary only; one candidate per line, # comments)"}
		},
		"required": ["device"]
	}`)
}

func (t *assessTool) ReadOnly() bool { return false }

func (t *assessTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Device   string `json:"device"`
		Tier     string `json:"tier"`
		DictPath string `json:"dict_path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Device) == "" {
		return "", errors.New("netdev_assess: device is required")
	}
	label := "assess weak-cred (" + a.Tier + ") " + a.Device
	// Live-panel lifecycle: one-shot (attempts are audited individually inside
	// WeakCredCheck), so start/end brackets the whole check.
	start := t.m.liveCmdStart(a.Device, label, "assess")
	status := "failure"
	defer func() { t.m.liveCmdEnd(a.Device, label, "assess", status, start, 0, "") }()

	// The envelope gate doubles here so the tool refusal (not just the manager
	// error) lands as a VISIBLE live refusal in the panel.
	if err := AssessmentActive(t.m.cfg.NetDev); err != nil {
		t.m.liveCmdRefused(a.Device, label, "assess", err.Error())
		return "", fmt.Errorf("netdev_assess: %w", err)
	}

	res, err := t.m.WeakCredCheck(ctx, a.Device, a.Tier, a.DictPath)
	if err != nil {
		return "", err
	}
	status = AuditOK
	if res.Weak {
		status = AuditDeviceError
	}
	out := fmt.Sprintf("%s: tier=%s, %d/%d attempt(s) — %s", res.Device, res.Tier, res.Attempts, res.Budget, res.Detail)
	if res.Weak {
		out += "\n已确认弱口令：请起草变更提案修复（不要直接改密码——评估手不下发配置）。"
	}
	return out, nil
}
