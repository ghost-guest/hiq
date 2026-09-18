package netdev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev/driver"
	"github.com/zzycxz/hiq/internal/netdev/transport"
	"github.com/zzycxz/hiq/internal/tool"
)

// HostKeyPrompt is the interactive TOFU hook. nil (default) = strict mode:
// unknown host keys are rejected. The desktop settings panel (P1 UI) injects
// a dialog-backed prompt here at startup.
var HostKeyPrompt transport.HostKeyPrompt

// Manager owns per-device connections and enforces the structural read-only
// seal: every netdev_exec command passes the driver classifier, and anything
// that is not ClassRead is refused and audited — never executed. There is no
// code path from the agent to a write; writes arrive with the proposal
// pipeline (P4), which a human approves.
type Manager struct {
	cfg *config.Config

	mu              sync.Mutex
	conns           map[string]*managedConn
	turnCommands    int // read commands spent in the current user turn (TurnBegin resets)
	netconfInflight map[string]int
	liveMu          sync.Mutex
	liveFn          func(LiveEvent)
}

type managedConn struct {
	client  *transport.Client
	session *Session
	drv     driver.Driver
	lastUse time.Time
}

// idleAfter: an unused device session is closed (freeing the device's scarce
// VTY line, NETDEV_SPEC §6.5/B-6) even though the supervisor would happily
// keep the TCP/SSH link alive.
const idleAfter = 5 * time.Minute

// SharedManager returns the process-wide Manager, refreshing its config.
// The desktop bridge and the scheduler call this instead of NewManager so
// reaper goroutines and per-device session caches exist exactly once (a
// Manager per call would leak a reaper and re-dial VTYs needlessly).
func SharedManager(cfg *config.Config) *Manager {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared == nil {
		shared = NewManager(cfg)
	} else {
		shared.cfg = cfg
	}
	EnsureNotifier(cfg)
	return shared
}

var (
	sharedMu sync.Mutex
	shared   *Manager
)

func NewManager(cfg *config.Config) *Manager {
	m := &Manager{cfg: cfg, conns: map[string]*managedConn{}, netconfInflight: map[string]int{}}
	go m.reaper(context.Background())
	return m
}

// TurnBegin resets the per-turn command budget. The desktop bridge calls it
// on every user submit (netdev mode), making turn_command_budget a true
// per-ask control: each question the user asks buys a fresh budget of read
// commands, and nothing carries over.
func (m *Manager) TurnBegin() {
	m.mu.Lock()
	m.turnCommands = 0
	m.mu.Unlock()
	// The live panel's budget meter follows the per-ask reset (§6.6: the
	// frontend calls this on every user submit).
	m.emitLive(LiveEvent{Kind: LiveTurnBegin, Device: "(turn)"})
}

// guardrailCheck enforces the [netdev.guardrails] controls that gate BEFORE
// anything else — device-group scope and the per-turn command budget. Both
// refuse locally (zero TCP) and audit the refusal as a guardrail event.
func (m *Manager) guardrailCheck(deviceName, command string) (ExecResult, bool) {
	g := m.cfg.NetDev.Guardrails
	if len(g.AllowedGroups) > 0 {
		if d, ok := m.cfg.NetDevDeviceByName(deviceName); ok {
			if !contains(g.AllowedGroups, d.Group) {
				r := ExecResult{Device: deviceName, Command: command, Refused: true, Class: "guardrail",
					Refusal: fmt.Sprintf("device %q is outside this conversation's allowed device groups (%s) — adjust [netdev.guardrails].allowed_groups in 运维设置. Do not retry.", deviceName, strings.Join(g.AllowedGroups, ", "))}
				_ = AppendAudit(Audit{Device: deviceName, Command: command, Class: "guardrail", Status: AuditRefused, OutputBytes: 0})
				return r, false
			}
		}
	}
	if g.TurnCommandBudget > 0 {
		m.mu.Lock()
		spent := m.turnCommands
		m.mu.Unlock()
		if spent >= g.TurnCommandBudget {
			r := ExecResult{Device: deviceName, Command: command, Refused: true, Class: "guardrail",
				Refusal: fmt.Sprintf("turn command budget exhausted (%d/%d read commands this turn) — a guardrail against runaway loops. Summarize what you have and ask the user before continuing.", spent, g.TurnCommandBudget)}
			_ = AppendAudit(Audit{Device: deviceName, Command: command, Class: "guardrail", Status: AuditRefused, OutputBytes: 0})
			return r, false
		}
	}
	return ExecResult{}, true
}

// turnSpend consumes one unit of the per-turn budget (called only after a
// command actually ran).
func (m *Manager) turnSpend() {
	m.mu.Lock()
	m.turnCommands++
	m.mu.Unlock()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ApplyExtraRead wires the [netdev.extra_read] vendor tables into the
// drivers' runtime read extensions — the B-1 knowledge-growth path: users
// teach the read table (via settings or the one-click refusal chip), never
// the model. boot's RegisterTools and the desktop settings bridge call it
// after every config change so the tables go live without a restart.
func ApplyExtraRead(cfg *config.Config) {
	if cfg == nil {
		return
	}
	for vendor, prefixes := range cfg.NetDev.ExtraRead {
		drv, ok := driver.For(vendor, "")
		if !ok {
			continue
		}
		driver.SetExtraRead(drv.Key(), prefixes)
	}
}

// KillAllConnections is the emergency stop: every device connection and CLI
// session is closed immediately (freeing all VTY lines), audited as such. The
// Manager stays usable — the next diagnostic command reconnects on demand.
// Human terminals die with it too (§6.1 紧急停止同样杀人工终端).
func (m *Manager) KillAllConnections() int {
	m.mu.Lock()
	n := len(m.conns)
	names := make([]string, 0, n)
	for name, c := range m.conns {
		c.session.Close()
		c.client.Close()
		delete(m.conns, name)
		names = append(names, name)
	}
	m.mu.Unlock()
	for _, name := range names {
		m.emitConnLive(name, LiveConnStopped)
	}
	human := HumanTTYKillAll()
	_ = AppendAudit(Audit{Device: "(emergency-stop)", Command: fmt.Sprintf("kill all connections (+%d human terminals, +%d discovery runs)", human, CancelDiscoverRuns()), Class: "guardrail", Status: AuditOK, OutputBytes: n})
	return n
}

// Close tears down every cached connection.
func (m *Manager) Close() {
	// Same lock discipline as the reaper: collect under the lock, close
	// outside it (client.Close waits for the supervisor goroutine, whose
	// status publish feeds the live observer — closing under m.mu deadlocks).
	m.mu.Lock()
	conns := make([]*managedConn, 0, len(m.conns))
	for name, c := range m.conns {
		conns = append(conns, c)
		delete(m.conns, name)
	}
	m.mu.Unlock()
	for _, c := range conns {
		c.session.Close()
		c.client.Close()
	}
}

func (m *Manager) reaper(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Collect under the lock, close OUTSIDE it: client.Close waits on
			// the supervisor goroutine, whose status publish feeds our live
			// observer — closing under m.mu would deadlock the two.
			m.mu.Lock()
			var idled []*managedConn
			var names []string
			for name, c := range m.conns {
				if time.Since(c.lastUse) > idleAfter {
					idled = append(idled, c)
					names = append(names, name)
					delete(m.conns, name)
				}
			}
			m.mu.Unlock()
			for _, c := range idled {
				c.session.Close()
				c.client.Close()
			}
			// Idle-close frees the device's scarce VTY line — visible in the
			// live panel as the status dot going grey.
			for _, name := range names {
				m.emitConnLive(name, LiveConnIdleClosed)
			}
		}
	}
}

// ExecResult is the tool-facing outcome of one command.
type ExecResult struct {
	Device  string `json:"device"`
	Command string `json:"command"`
	Class   string `json:"class"`
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
	Refused bool   `json:"refused,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

// Exec runs one command on one configured device under the read-only seal.
func (m *Manager) Exec(ctx context.Context, deviceName, command string) ExecResult {
	// One command per call, ENFORCED (the tool description says so, but the
	// model — or an injection riding device output — must not be able to lean
	// on a newline: the PTY would execute the extra lines as keystrokes,
	// bypassing the classifier entirely ("display version\nundo stp" classifies
	// as read after whitespace collapse). Refused before anything else.
	if strings.ContainsAny(command, "\n\r\x00\v\f") {
		_ = AppendAudit(Audit{Device: deviceName, Command: "(multi-line command)", Class: "guardrail", Status: AuditRefused, OutputBytes: 0})
		m.liveCmdRefused(deviceName, "(multi-line command)", "guardrail", "one command per call — a newline would execute unclassified lines")
		return ExecResult{Device: deviceName, Command: command, Refused: true, Class: "guardrail",
			Refusal: "multi-line command refused — one command per call, because a newline would execute unclassified lines on the device. Split it into separate calls so every line goes through the read-only classifier."}
	}

	device, ok := m.cfg.NetDevDeviceByName(deviceName)
	if !ok {
		return ExecResult{Device: deviceName, Command: command, Refused: true,
			Refusal: fmt.Sprintf("device %q is not in the user-global netdev inventory (add it in 运维设置; the agent cannot add devices itself)", deviceName)}
	}

	// [netdev.guardrails] gate: group scope + per-turn budget, refused BEFORE
	// any driver work (and long before a socket opens).
	if r, allow := m.guardrailCheck(deviceName, command); !allow {
		m.liveCmdRefused(deviceName, command, "guardrail", r.Refusal)
		return r
	}

	drv, ok := driver.For(device.Vendor, device.OS)
	if !ok {
		return ExecResult{Device: deviceName, Command: command, Refused: true,
			Refusal: fmt.Sprintf("no driver for %s/%s (available: %s)", device.Vendor, device.OS, strings.Join(driver.Keys(), ", "))}
	}

	// Shell-driver metacharacter guard: a pipe, semicolon, backtick or
	// redirect would smuggle unclassified commands past the word-prefix
	// classifier into the PTY ("ps aux | sh -c 'reboot'"). Network CLIs are
	// exempt — their `| include` is a device-side filter, not a chain.
	if driver.IsShellMetacharDriver(drv.Key()) && strings.ContainsAny(command, driver.ShellMetachars) {
		_ = AppendAudit(Audit{Device: deviceName, Command: "(shell metacharacters)", Class: "guardrail", Status: AuditRefused, OutputBytes: 0})
		m.liveCmdRefused(deviceName, "(shell metacharacters)", "guardrail", "shell metacharacters would chain unclassified commands")
		return ExecResult{Device: deviceName, Command: command, Refused: true, Class: "guardrail",
			Refusal: "shell metacharacters refused — this device runs a general shell, and ; | & ` $ ( ) < > would chain commands the classifier never examined. Run ONE plain command per call (no pipes/redirects/substitutions)."}
	}

	class := drv.Classify(command)
	if class != driver.Read {
		// Log-whitelist bypass (logsource.go): tail/head/grep/wc with one path
		// inside the device's log roots (/var/log + log_paths) is read even off
		// the built-in table. Still one plain metachar-free line at this point.
		if c, ok := logPathReadOverride(device, drv, command); ok {
			class = c
		}
	}
	base := ExecResult{Device: deviceName, Command: command, Class: class.String()}
	if class != driver.Read {
		base.Refused = true
		switch class {
		case driver.Write:
			base.Refusal = "write command — not executed. netdev's diagnostic hand is structurally read-only; configuration changes go through a human-approved change proposal (变更提案). Tell the user what change is needed and why."
		case driver.Dangerous:
			base.Refusal = "dangerous command — not executed. This class of command (reboot/delete/erase…) is proposal-only and requires secondary confirmation."
		default:
			base.Refusal = "unknown command — conservatively treated as write and not executed. Ask the user to confirm whether it is a read-only diagnostic; if so they can add it to the driver's read table (知识库归类)."
		}
		m.audit(device, command, class, AuditRefused, 0, nil)
		m.liveCmdRefused(deviceName, command, class.String(), base.Refusal)
		return base
	}

	start := m.liveCmdStart(deviceName, command, class.String())
	res, err := m.runRead(ctx, device, drv, command)
	if err != nil {
		m.audit(device, command, class, AuditFailure, 0, err)
		m.liveCmdEnd(deviceName, command, class.String(), "failure", start, 0, err.Error())
		base.Refused = true
		base.Refusal = "connection/session failure: " + err.Error()
		return base
	}
	status := AuditOK
	if res.IsError {
		status = AuditDeviceError
	}
	m.turnSpend()
	// Redact BEFORE the text crosses into the model context: credential lines
	// in device output never reach the LLM (or the audit's byte count). The
	// count becomes a visible reminder — the user sees that masking happened.
	redacted, redactedN := RedactCounted(res.Output)
	m.audit(device, command, class, status, len(redacted), nil)
	m.liveCmdEnd(deviceName, command, class.String(), status, start, len(redacted), "")
	base.Output = redacted
	if redactedN > 0 {
		base.Output += fmt.Sprintf("\n\n[安全提醒] 输出中 %d 处敏感字段（密码/密钥/团体字）已脱敏后才进入上下文与审计；原文只存在于内存会话缓冲。", redactedN)
	}
	base.IsError = res.IsError
	return base
}

func (m *Manager) audit(d config.NetDevDevice, cmd string, class driver.Class, status string, outBytes int, err error) {
	e := Audit{
		Device:      d.Name,
		Via:         d.Via,
		Command:     cmd,
		Class:       class.String(),
		Status:      status,
		OutputBytes: outBytes,
	}
	if err != nil {
		e.Error = err.Error()
	}
	if aerr := AppendAudit(e); aerr != nil {
		// Surfaced in the result stream is the manager's business; audit file
		// errors go to stderr via the tool error path only when fatal.
		_ = aerr
	}
}

// driverFor resolves a device's driver.
func (m *Manager) driverFor(d config.NetDevDevice) (driver.Driver, bool) {
	if d.Name == "" && d.Vendor == "" {
		return nil, false
	}
	return driver.For(d.Vendor, d.OS)
}

func drvKey(d config.NetDevDevice) string {
	drv, ok := driver.For(d.Vendor, d.OS)
	if !ok {
		return ""
	}
	return drv.Key()
}

// runRead obtains (or establishes) the device's cached session and runs the
// already-classified-Read command.
func (m *Manager) runRead(ctx context.Context, d config.NetDevDevice, drv driver.Driver, command string) (Result, error) {
	m.mu.Lock()
	existing, ok := m.conns[d.Name]
	if ok {
		existing.lastUse = time.Now()
	}
	m.mu.Unlock()
	if ok {
		return existing.session.Run(ctx, command)
	}

	client, session, err := m.connect(ctx, d, drv)
	if err != nil {
		return Result{}, err
	}
	m.mu.Lock()
	// Lost a race creating the same device's connection: close the loser.
	if other, ok := m.conns[d.Name]; ok {
		m.mu.Unlock()
		session.Close()
		client.Close()
		other.lastUse = time.Now()
		return other.session.Run(ctx, command)
	}
	m.conns[d.Name] = &managedConn{client: client, session: session, drv: drv, lastUse: time.Now()}
	use := m.vtySnapshotLocked(d.Name)
	m.mu.Unlock()
	m.emitLive(LiveEvent{Kind: LiveConn, Device: d.Name, State: LiveConnConnected, VTYUse: use, VTYCap: m.vtyCap()})
	return session.Run(ctx, command)
}

// connect resolves the route (device + via hop chain), credentials, and host
// key policy, then establishes a supervised transport client and CLI session.
func (m *Manager) connect(ctx context.Context, d config.NetDevDevice, drv driver.Driver) (*transport.Client, *Session, error) {
	client, err := m.dialDeviceClient(ctx, d)
	if err != nil {
		return nil, nil, err
	}
	m.subscribeConnState(d.Name, client)
	session, err := OpenSession(ctx, client, drv, d.Encoding)
	if err != nil {
		client.Close()
		return nil, nil, err
	}
	// Live tap: route this session's incremental output to the observer as
	// this device's 操作实况 stream (sanitized inside the session layer).
	session.SetOutputObserver(func(chunk string) {
		m.emitLive(LiveEvent{Kind: LiveCmdOutput, Device: d.Name, Chunk: chunk})
	})
	return client, session, nil
}

// dialDeviceClient establishes a supervised transport client WITHOUT the
// driver CLI session. Callers that need a raw channel of their own (human
// terminal §6.1, proposal file-upload §7.1) dial this so their lifecycle is
// independent of the cached diagnostic session (and its idle reaper).
func (m *Manager) dialDeviceClient(ctx context.Context, d config.NetDevDevice) (*transport.Client, error) {
	if !m.cfg.NetDev.Enabled {
		return nil, errors.New("[netdev] is disabled in the user config")
	}
	lookup := m.lookupEntry()
	resolved, err := transport.ResolveHost(lookup, d.Name, nil)
	if err != nil {
		return nil, fmt.Errorf("resolve device: %w", err)
	}
	jumps, err := transport.ResolveJumpHosts(lookup, d.Via, nil)
	if err != nil {
		return nil, fmt.Errorf("resolve route: %w", err)
	}

	auth := transport.AuthOptions{
		Password:   secretReader(SecretKindPassword, d.PasswordEnv),
		Passphrase: secretReader(SecretKindPassphrase, d.PassphraseEnv),
	}
	if d.PasswordEnv == "" && d.IdentityFile == "" {
		return nil, fmt.Errorf("device %q has no credentials configured (set password_env or identity_file)", d.Name)
	}
	hops := make([]transport.JumpHostOptions, 0, len(jumps))
	for i, j := range jumps {
		hopCfg := m.hopByRaw(d.Via[i])
		ja := transport.AuthOptions{
			Password:   secretReader(SecretKindPassword, hopCfg.PasswordEnv),
			Passphrase: secretReader(SecretKindPassphrase, hopCfg.PassphraseEnv),
		}
		hops = append(hops, transport.JumpHostOptions{Host: j, Auth: ja})
	}

	client, err := transport.New(transport.Options{
		Host:      resolved,
		Auth:      auth,
		JumpHosts: hops,
		HostKeys:  &transport.HostKeyPolicy{Prompt: HostKeyPrompt, ManagedPath: transport.ManagedKnownHostsOverride},
	})
	if err != nil {
		return nil, err
	}
	if err := client.Start(ctx); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// lookupEntry adapts the pinned config into transport's name→entry lookup.
func (m *Manager) lookupEntry() transport.LookupEntry {
	return func(name string) (transport.HostEntry, bool) {
		if d, ok := m.cfg.NetDevDeviceByName(name); ok {
			return transport.HostEntry{
				Name: d.Name, Host: d.Address, Port: d.Port, User: d.Username,
				IdentityFile: d.IdentityFile, PassphraseEnv: d.PassphraseEnv,
				PasswordEnv: d.PasswordEnv, UseSSHConfig: d.UseSSHConfig,
			}, true
		}
		if h, ok := m.cfg.NetDevHopByName(name); ok {
			return transport.HostEntry{
				Name: h.Name, Host: h.Host, Port: h.Port, User: h.User,
				IdentityFile: h.IdentityFile, PassphraseEnv: h.PassphraseEnv,
				PasswordEnv: h.PasswordEnv, ProxyJump: h.ProxyJump, UseSSHConfig: h.UseSSHConfig,
			}, true
		}
		return transport.HostEntry{}, false
	}
}

func (m *Manager) hopByRaw(name string) config.NetDevHop {
	h, _ := m.cfg.NetDevHopByName(name)
	return h
}

// secretGetter is the credential read seam; production uses the secret store,
// tests stub it (the real store is a user-global singleton).
var secretGetter = GetSecret

// secretReader builds a credential closure from the secret store; nil when
// no env name is configured (transport then falls back to the next method).
func secretReader(kind, envName string) func() (string, error) {
	if strings.TrimSpace(envName) == "" {
		return nil
	}
	return func() (string, error) {
		v, ok, err := secretGetter(kind, envName)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("secret %s/%s not set — add it in 运维设置 (credential values live in the secret store, never in TOML)", kind, envName)
		}
		return v, nil
	}
}

// ── Tools ────────────────────────────────────────────────────────────────────

// RegisterTools adds the netdev tool family to the registry. boot calls this
// ONLY inside the netdev profile branch, so dev/cowork sessions never see
// these tools (the reverse half of the hard seal; NETDEV_SPEC §7.1).
func RegisterTools(reg *tool.Registry, cfg *config.Config) {
	// Same instance the desktop bridge uses: NetDevTurnBegin's budget reset
	// and NetDevEmergencyStop's KillAllConnections must reach the Manager the
	// agent's tools actually hold (NETDEV_SPEC §6.5/§6.6). A private Manager
	// here would leave the tool-side budget counter accumulating forever and
	// emergency stop leaving the agent's device sessions alive.
	m := SharedManager(cfg)
	ApplyExtraRead(cfg)
	reg.Add(&execTool{m: m})
	reg.Add(&devicesTool{cfg: cfg})
	reg.Add(&discoverTool{m: m})
	reg.Add(&topologyTool{m: m})
	reg.Add(&proposeTool{m: m})
	reg.Add(&findingTool{})
	reg.Add(&netconfTool{m: m})
	reg.Add(&snmpTool{m: m})
	reg.Add(&assessTool{m: m})
	reg.Add(&baselineTool{m: m})
	reg.Add(&redfishTool{m: m})
	reg.Add(&logReadTool{m: m})
	reg.Add(&logSearchTool{m: m})
	reg.Add(&triageTool{m: m})
	reg.Add(&dockerTool{m: m})
	reg.Add(&kubeTool{m: m})
	reg.Add(&firewallTool{m: m})
	reg.Add(&locateTool{m: m})
	reg.Add(&dbQueryTool{m: m})
	// fanout (completion-spec §6 #9): one read-only command across many
	// devices, tabulated — the runtime version of a diagnostic preset.
	reg.Add(&fanoutTool{m: m, cfg: cfg})
	// Trust-domain fleet surface (TRUSTDOMAIN_SPEC §15): agent tools exist
	// only when the host joined a domain — invisible otherwise.
	if cfg.TrustDomain.Enabled {
		reg.Add(&fleetTool{cfg: cfg})
		reg.Add(&remoteTool{cfg: cfg})
		InitAuditAnchoring(cfg) // audit chain head cross-anchors (spec §八)
	}
}

// fanoutTool — netdev_fanout: run ONE read-only command across many devices
// (explicit list or group filter; empty = all in scope) and tabulate results.
// Every per-device execution goes through the same sealed Exec path as
// netdev_exec (classifier / budget / redaction / audit) — this tool only adds
// the fan-out loop and the table rendering.
type fanoutTool struct {
	m   *Manager
	cfg *config.Config
}

func (t *fanoutTool) Name() string { return "netdev_fanout" }

func (t *fanoutTool) Description() string {
	return "Run one READ-ONLY command across many devices at once and get a compact per-device table " +
		"(device → first lines of output / refusal). Targets: explicit devices list, or group filter, or all devices. " +
		"The command is classifier-checked per device exactly like netdev_exec — writes are refused."
}

func (t *fanoutTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "the read-only command, e.g. display clock"},
			"devices": {"type": "array", "items": {"type": "string"}, "description": "explicit device names"},
			"group": {"type": "string", "description": "device-group filter (empty with no devices = all)"}
		},
		"required": ["command"]
	}`)
}

func (t *fanoutTool) ReadOnly() bool { return true }

func (t *fanoutTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Command string   `json:"command"`
		Devices []string `json:"devices"`
		Group   string   `json:"group"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Command) == "" {
		return "", errors.New("netdev_fanout: command is required")
	}
	allow := t.cfg.NetDev.Guardrails.AllowedGroups
	want := map[string]bool{}
	for _, d := range a.Devices {
		want[strings.TrimSpace(d)] = true
	}
	var names []string
	for _, d := range t.cfg.NetDev.Devices {
		if d.Vendor == "snmp" {
			continue // SNMP-only targets have no CLI session
		}
		if len(allow) > 0 && !contains(allow, d.Group) {
			continue // outside this conversation's scope — invisible
		}
		if len(want) > 0 && !want[d.Name] {
			continue
		}
		if a.Group != "" && d.Group != a.Group {
			continue
		}
		names = append(names, d.Name)
	}
	if len(names) == 0 {
		return "no matching devices in scope", nil
	}
	type row struct {
		Device string `json:"device"`
		Class  string `json:"class"`
		Output string `json:"output"`
	}
	rows := make([]row, 0, len(names))
	for _, n := range names {
		r := t.m.Exec(ctx, n, a.Command)
		out := r.Output
		if r.Refused {
			out = r.Refusal
		}
		if r.IsError {
			out = "[device error] " + out
		}
		// keep the table compact: first 6 non-empty lines
		lines := []string{}
		for _, l := range strings.Split(out, "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, strings.TrimSpace(l))
			}
			if len(lines) == 6 {
				break
			}
		}
		rows = append(rows, row{Device: n, Class: r.Class, Output: strings.Join(lines, " ⏎ ")})
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// dbQueryTool — read-only database diagnostics against a configured
// [[netdev.db_sources]] entry: exact-statement allowlist + least-privilege
// account. Connection-count, slow-query, replication-lag class questions.
type dbQueryTool struct{ m *Manager }

func (t *dbQueryTool) Name() string { return "netdev_db_query" }

func (t *dbQueryTool) Description() string {
	return "Run ONE allowlisted read-only diagnostic query on a configured database source (mysql|postgres|redis). " +
		"The source's allowlist (exact statements like SHOW PROCESSLIST / SELECT * FROM information_schema.processlist) is the seal — anything else is refused before connecting. " +
		"Use for connection counts, slow queries, replication lag, lock waits. NOT a data browser."
}

func (t *dbQueryTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"source": {"type": "string", "description": "db source name from 运维设置"},
			"query":  {"type": "string", "description": "one plain statement; must match the source's allowlist"}
		},
		"required": ["source", "query"]
	}`)
}

func (t *dbQueryTool) ReadOnly() bool { return true }

func (t *dbQueryTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Source string `json:"source"`
		Query  string `json:"query"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Source) == "" || strings.TrimSpace(a.Query) == "" {
		return "", errors.New("netdev_db_query: source and query are required")
	}
	return t.m.DBQuery(ctx, strings.TrimSpace(a.Source), a.Query)
}

// redfishTool — the BMC channel: ONE GET-only Redfish request against a
// vendor=redfish device. Sealed at the transport (no write verb exists in the
// client) and the path allowlist (Systems/Chassis/Managers/...).
type redfishTool struct{ m *Manager }

func (t *redfishTool) Name() string { return "netdev_redfish" }

func (t *redfishTool) Description() string {
	return "Run ONE read-only Redfish GET on a device's BMC (vendor=redfish): hardware inventory, thermal/power sensors, and the System Event Log. Paths are allowlisted to read-only resources (/redfish/v1/Systems, /Chassis, /Managers, /Managers/1/LogServices/SEL/Entries …); there is no write path at all."
}

func (t *redfishTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type": "object", "properties": {"device": {"type": "string"}, "path": {"type": "string", "description": "resource path, e.g. /redfish/v1/Chassis/1/Thermal"}}, "required": ["device", "path"]}`)
}

func (t *redfishTool) ReadOnly() bool { return true }

func (t *redfishTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Device string `json:"device"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Device == "" || strings.TrimSpace(a.Path) == "" {
		return "", errors.New("netdev_redfish: device and path are required")
	}
	return t.m.RedfishGet(ctx, a.Device, a.Path)
}

// baselineTool exposes the config-security baseline battery to the agent —
// read-only (configs come through the sealed Exec path, rules run locally).
type baselineTool struct{ m *Manager }

func (t *baselineTool) Name() string { return "netdev_baseline" }

func (t *baselineTool) Description() string {
	return "Run the configuration security baseline check over every managed device: reads each running-config (read-only, redacted) and evaluates precise local rules (telnet/SNMPv1v2c/plaintext passwords/SSHv1/NTP/syslog). Violations are filed as Findings with evidence. Use it when asked to audit security posture."
}

func (t *baselineTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type": "object", "properties": {}, "required": []}`)
}

func (t *baselineTool) ReadOnly() bool { return true }

func (t *baselineTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	f, err := t.m.RunBaseline(ctx)
	if err != nil {
		return "", err
	}
	return f.Title + "。逐项结果见「发现」（每条命中都带脱敏证据与修复建议，修复变更请起草提案）。", nil
}

type netconfTool struct{ m *Manager }

// snmpTool exposes the sealed one-shot SNMP query to the agent (vendor=snmp
// devices): v2c GET or bounded WALK over the MIB-2 allowlist, secrets from the
// secret store, output redacted. No set/write path exists at all.
type snmpTool struct{ m *Manager }

func (t *snmpTool) Name() string { return "netdev_snmp" }

func (t *snmpTool) Description() string {
	return "Run ONE read-only SNMP v2c query on a vendor=snmp device: GET a single OID or WALK a subtree (bounded to 120 variables). " +
		"OIDs are allowlisted to standard MIB-2 subtrees (system/interfaces/ip/icmp/tcp/udp/host-resources/ifMIB) — interface counters, uptime, " +
		"IP stats. Community comes from the device's password_env credential; output is redacted. There is no SNMP set path."
}

func (t *snmpTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"device": {"type": "string", "description": "vendor=snmp device name from netdev_devices"},
			"oid": {"type": "string", "description": "dotted OID, e.g. 1.3.6.1.2.1.1.3.0 (sysUpTime) or a subtree root for walk"},
			"mode": {"type": "string", "enum": ["get", "walk"], "description": "get = one OID (default), walk = bounded subtree sweep"}
		},
		"required": ["device", "oid"]
	}`)
}

func (t *snmpTool) ReadOnly() bool { return true }

func (t *snmpTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Device string `json:"device"`
		OID    string `json:"oid"`
		Mode   string `json:"mode"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Device) == "" || strings.TrimSpace(a.OID) == "" {
		return "", errors.New("netdev_snmp: device and oid are required")
	}
	return t.m.SnmpQuery(ctx, a.Device, a.OID, a.Mode)
}

func (t *netconfTool) Name() string { return "netdev_netconf" }

func (t *netconfTool) Description() string {
	return "Run ONE read-only NETCONF RPC (<get> or <get-config>) on a device over its SSH connection " +
		"and return the raw <rpc-reply> XML. Write operations are refused — changes go through the proposal pipeline."
}

func (t *netconfTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"device": {"type": "string"},
			"rpc": {"type": "string", "description": "inner element, e.g. \"<get/>\" or \"<get-config><source><running/></source></get-config>\""}
		},
		"required": ["device", "rpc"]
	}`)
}

func (t *netconfTool) ReadOnly() bool { return true }

func (t *netconfTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Device string `json:"device"`
		RPC    string `json:"rpc"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Device == "" || strings.TrimSpace(a.RPC) == "" {
		return "", errors.New("netdev_netconf: device and rpc are required")
	}
	if r, allow := t.m.guardrailCheck(a.Device, "netconf: "+a.RPC); !allow {
		b, _ := json.Marshal(r)
		return string(b), nil
	}
	reply, err := t.m.NetconfRPC(ctx, a.Device, a.RPC)
	if err != nil && reply == "" {
		return "", err
	}
	out, n := RedactCounted(reply)
	if n > 0 {
		out += fmt.Sprintf("\n\n[安全提醒] 输出中 %d 处敏感字段（密码/密钥/团体字）已脱敏后才进入上下文与审计。", n)
	}
	return out, nil
}

// findingTool records a diagnosis conclusion WITH its evidence — the unit the
// user reviews. No evidence, no finding.
type findingTool struct{}

func (t *findingTool) Name() string { return "netdev_finding" }

func (t *findingTool) Description() string {
	return "Record one diagnostic finding: a conclusion backed by the command outputs that support it. " +
		"Always attach evidence (device + command + the relevant output excerpt — outputs you saw via netdev_exec are already redacted). " +
		"Findings are what the user reviews; end a diagnosis with findings, not prose."
}

func (t *findingTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"title": {"type": "string", "description": "one-line conclusion, e.g. \"OSPF neighbor 10.2.0.3 down: holding timer mismatch\""},
			"severity": {"type": "string", "enum": ["info", "warning", "critical"]},
			"devices": {"type": "array", "items": {"type": "string"}},
			"detail": {"type": "string", "description": "reasoning: what was checked, what correlated"},
			"evidence": {"type": "array", "items": {
				"type": "object",
				"properties": {
					"device": {"type": "string"},
					"command": {"type": "string"},
					"output": {"type": "string", "description": "the supporting excerpt"}
				},
				"required": ["device", "command", "output"]
			}},
			"suggestion": {"type": "string", "description": "optional: the change worth drafting via netdev_propose"}
		},
		"required": ["title", "evidence"]
	}`)
}

func (t *findingTool) ReadOnly() bool { return true }

func (t *findingTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var f Finding
	if err := json.Unmarshal(args, &f); err != nil {
		return "", err
	}
	if err := SaveFinding(&f); err != nil {
		return "", err
	}
	_ = AppendAudit(Audit{Device: "(finding)", Command: f.Title, Class: "read", Status: AuditOK})
	return "finding " + f.ID + " recorded (" + f.Severity + ") with " + fmt.Sprint(len(f.Evidence)) + " evidence items", nil
}

// proposeTool lets the agent DRAFT a change proposal. Drafting is a read-only
// act: nothing executes, nothing connects. Approval and execution are
// human-only (desktop proposal UI → Manager.ApproveProposal/ExecuteProposal);
// there is deliberately NO agent path to them.
type proposeTool struct{ m *Manager }

func (t *proposeTool) Name() string { return "netdev_propose" }

func (t *proposeTool) Description() string {
	return "Draft a change proposal (NOT executed): intent + per-device commands + a rollback plan. " +
		"A human reviews the whole proposal and decides — approving, executing, and rolling back happen only in 运维提案界面. " +
		"Every step needs a rollback plan authored with the change; devices in read-only groups are refused."
}

func (t *proposeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"intent": {"type": "string", "description": "what changes and why, one or two sentences the approver reads"},
			"steps": {"type": "array", "items": {
				"type": "object",
				"properties": {
					"device": {"type": "string"},
					"commands": {"type": "array", "items": {"type": "string"}},
					"rollback": {"type": "array", "items": {"type": "string"}, "description": "reverse commands, applied newest-first on rollback"}
				},
				"required": ["device", "commands", "rollback"]
			}}
		},
		"required": ["intent", "steps"]
	}`)
}

func (t *proposeTool) ReadOnly() bool { return true } // drafts change nothing on any device

func (t *proposeTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Intent string         `json:"intent"`
		Steps  []ProposalStep `json:"steps"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	p := &Proposal{Intent: a.Intent, Steps: a.Steps, Status: ProposalDraft}
	if err := t.m.ValidateProposal(p); err != nil {
		return "", err
	}
	p.ID = newProposalID() // pre-assigned so the state-history create-marker knows the path
	StateEventSnap(StateEventPropose, p.ID, StateActorAgent, filepath.Join(ProposalsDir(), p.ID+".json"))
	if err := SaveProposal(p); err != nil {
		return "", err
	}
	_ = AppendAudit(Audit{Device: "(proposal)", Command: "draft " + p.ID + " (" + a.Intent + ")", Class: "proposal", Status: AuditRefused})
	needs2 := t.m.ProposalNeedsConfirm2(p)
	return fmt.Sprintf("proposal %s drafted (status: draft). The user reviews it in 设置 → 运维 → 提案中查看; approval and execution are theirs, not yours.%s",
		p.ID, map[bool]string{true: " Note: a device in this proposal is in a proposal+confirm2 group — approval demands secondary confirmation.", false: ""}[needs2]), nil
}

type topologyTool struct{ m *Manager }

func (t *topologyTool) Name() string { return "netdev_topology" }

func (t *topologyTool) Description() string {
	return "Read one device's CDP/LLDP neighbor table and return adjacency edges (local port ↔ remote device/port/IP). " +
		"Neighbor names not in the inventory are UNMANAGED — visible but not connectable until a human registers them. " +
		"Call once per device to assemble a topology; correlate edges by interface name (abbreviations are normalized)."
}

func (t *topologyTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"device": {"type": "string", "description": "device name from netdev_devices"}
		},
		"required": ["device"]
	}`)
}

func (t *topologyTool) ReadOnly() bool { return true }

func (t *topologyTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Device string `json:"device"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Device) == "" {
		return "", errors.New("netdev_topology: device is required")
	}
	res := t.m.Exec(ctx, a.Device, t.neighborCommand(a.Device))
	if res.Refused {
		return "", fmt.Errorf("netdev_topology: %s", res.Refusal)
	}
	if res.IsError {
		return "", fmt.Errorf("netdev_topology: device reported an error for the neighbor query: %s", firstLine(res.Output))
	}
	device, _ := t.m.cfg.NetDevDeviceByName(a.Device)
	edges, err := parseNeighbors(drvKey(device), res.Output)
	if err != nil {
		return "", err
	}
	for i := range edges {
		edges[i].LocalDevice = a.Device
	}
	b, err := json.MarshalIndent(edges, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// neighborCommand resolves the driver's neighbor query (read-classified by
// construction: display/show prefixes).
func (t *topologyTool) neighborCommand(deviceName string) string {
	device, ok := t.m.cfg.NetDevDeviceByName(deviceName)
	if !ok {
		return ""
	}
	cmd, _ := NeighborCommand(drvKey(device))
	return cmd
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

type discoverTool struct{ m *Manager }

func (t *discoverTool) Name() string { return "netdev_discover" }

func (t *discoverTool) Description() string {
	return "TCP-probe a CIDR for open device ports (default 22/23) and grab banners, optionally THROUGH a configured hop " +
		"(via = hop name; empty = direct). The CIDR must be inside the configured discovery scopes — out-of-scope probes are refused. " +
		"Tunnel mode only (no UDP/ICMP); /20+ networks are refused (use the probe for those)."
}

func (t *discoverTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"cidr": {"type": "string", "description": "target network, e.g. 10.30.2.0/24"},
			"ports": {"type": "array", "items": {"type": "integer"}, "description": "ports to probe; default [22, 23]"},
			"via": {"type": "string", "description": "hop name to probe through; empty = direct"}
		},
		"required": ["cidr"]
	}`)
}

func (t *discoverTool) ReadOnly() bool { return true }

func (t *discoverTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		CIDR  string `json:"cidr"`
		Ports []int  `json:"ports"`
		Via   string `json:"via"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.CIDR) == "" {
		return "", errors.New("netdev_discover: cidr is required")
	}
	res, err := t.m.DiscoverTCP(ctx, a.Via, a.CIDR, a.Ports)
	if err != nil {
		return "", err
	}
	if err := AppendAudit(Audit{
		Device: "(discover)", Command: "tcp-probe " + a.CIDR + " via=" + strings.TrimSpace(a.Via),
		Class: "read", Status: AuditOK,
	}); err != nil {
		_ = err
	}
	if len(res) == 0 {
		return "no responsive hosts in " + a.CIDR, nil
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type execTool struct{ m *Manager }

// logReadTool — the structured log-source read: the agent names a log source
// (file:/journal:/docker:) instead of free-handing shell; the composed command
// still rides the sealed Exec path (classifier, budget, redaction, audit).
type logReadTool struct{ m *Manager }

func (t *logReadTool) Name() string { return "netdev_log_read" }

func (t *logReadTool) Description() string {
	return "Read ONE log source on a configured device (usually vendor=linux). " +
		"Source forms: file:/var/log/nginx/error.log (path must be under /var/log or the device's log_paths whitelist), " +
		"journal:nginx (a systemd unit, use since/grep for time windows), docker:<container>. " +
		"Returns the last tail_n lines (default 100, max 1000), optionally filtered by since (ISO date-time or -1h style) and grep (regex), " +
		"with secrets redacted before they reach the context. Runs through the same read-only seal as netdev_exec."
}

func (t *logReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"device": {"type": "string", "description": "device name from netdev_devices"},
			"source": {"type": "string", "description": "file:/abs/path | journal:<unit> | docker:<container>"},
			"tail_n": {"type": "integer", "description": "lines to return (default 100, max 1000)"},
			"since":  {"type": "string", "description": "keep lines from this time on: 2026-08-27 10:00:00 or -1h (journal sources pass it server-side)"},
			"grep":   {"type": "string", "description": "regex filter applied to lines"}
		},
		"required": ["device", "source"]
	}`)
}

func (t *logReadTool) ReadOnly() bool { return true }

func (t *logReadTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Device string `json:"device"`
		Source string `json:"source"`
		TailN  int    `json:"tail_n"`
		Since  string `json:"since"`
		Grep   string `json:"grep"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Device == "" || strings.TrimSpace(a.Source) == "" {
		return "", errors.New("netdev_log_read: device and source are required")
	}
	b, err := json.Marshal(t.m.LogRead(ctx, a.Device, strings.TrimSpace(a.Source), a.TailN, strings.TrimSpace(a.Since), strings.TrimSpace(a.Grep)))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (t *execTool) Name() string { return "netdev_exec" }

func (t *execTool) Description() string {
	return "Run ONE read-only diagnostic command on a configured network device (display/show/ping/tracert…). " +
		"Write and dangerous commands are structurally refused — propose changes to the user instead. " +
		"Output is cleaned (paging/echo/prompt stripped) and device errors are flagged."
}

func (t *execTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"device": {"type": "string", "description": "device name from netdev_devices"},
			"command": {"type": "string", "description": "single CLI command, no newlines"}
		},
		"required": ["device", "command"]
	}`)
}

func (t *execTool) ReadOnly() bool { return true } // enforced by the classifier seal

func (t *execTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Device  string `json:"device"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Device == "" || strings.TrimSpace(a.Command) == "" {
		return "", errors.New("netdev_exec: device and command are required")
	}
	b, err := json.Marshal(t.m.Exec(ctx, a.Device, a.Command))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type devicesTool struct{ cfg *config.Config }

func (t *devicesTool) Name() string { return "netdev_devices" }

func (t *devicesTool) Description() string {
	return "List the configured network devices (name, vendor/OS, address, route via, group). Use the names with netdev_exec."
}

func (t *devicesTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type": "object", "properties": {}}`)
}

func (t *devicesTool) ReadOnly() bool { return true }

func (t *devicesTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	type row struct {
		Name, Vendor, OS, Model, Address, Group string
		Via                                     []string
	}
	// Scope the model's world: when allowed_groups is set, devices outside it
	// are invisible to the agent entirely (not just unexecutable) — the ask
	// itself is controlled before the first token is spent.
	allow := t.cfg.NetDev.Guardrails.AllowedGroups
	rows := make([]row, 0, len(t.cfg.NetDev.Devices))
	for _, d := range t.cfg.NetDev.Devices {
		if len(allow) > 0 && !contains(allow, d.Group) {
			continue
		}
		rows = append(rows, row{d.Name, d.Vendor, d.OS, d.Model, d.Address, d.Group, d.Via})
	}
	if len(rows) == 0 {
		if len(t.cfg.NetDev.Devices) > 0 && len(allow) > 0 {
			return "all devices are outside this conversation's allowed groups (" + strings.Join(allow, ", ") + ") — adjust [netdev.guardrails].allowed_groups in 运维设置", nil
		}
		return "no devices configured — add them (and their credentials) in 运维设置; [netdev] lives in the USER config only", nil
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
