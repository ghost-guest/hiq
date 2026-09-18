package netdev

import (
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/zzycxz/hiq/internal/netdev/transport"
)

// Live observation (操作实况): every observable moment of the ops surface —
// command lifecycle, incremental session output, connection state — so the
// desktop's right-dock panel can show WHAT the agent is doing on each device
// as it happens (supervision belongs to the human; NETDEV_SPEC §10).
//
// The observer is set by the desktop bridge (SharedManager.SetLiveObserver)
// and is nil-safe everywhere: with no observer the package behaves exactly as
// before. Output chunks are ANSI-stripped and REDACTED before they leave the
// package — a credential line never reaches the UI.

// Live event kinds.
const (
	LiveConn       = "conn"        // connection/session state change
	LiveCmdStart   = "cmd_start"   // a command began (classified, about to run)
	LiveCmdOutput  = "cmd_output"  // incremental cleaned+redacted output text
	LiveCmdEnd     = "cmd_end"     // command finished (ok / device-error / failure)
	LiveCmdRefused = "cmd_refused" // a guardrail/classifier refusal (visible!)
	LiveTurnBegin  = "turn"        // a new user turn: per-turn budget counters reset
)

// Connection states for LiveConn events.
const (
	LiveConnConnected  = "connected"
	LiveConnConnecting = "connecting"
	LiveConnReconnect  = "reconnecting"
	LiveConnStopped    = "stopped"
	LiveConnIdleClosed = "idle-closed" // reaper closed the idle session
)

// LiveEvent is one observable moment. Fields are grouped by kind; only the
// relevant subset is populated.
type LiveEvent struct {
	Kind   string `json:"kind"`
	Device string `json:"device"`
	Time   int64  `json:"time"` // unix milliseconds

	// LiveConn
	State  string `json:"state,omitempty"`
	VTYUse int    `json:"vtyUse,omitempty"` // sessions currently held on the device
	VTYCap int    `json:"vtyCap,omitempty"` // max_sessions_per_device (0 = unset)

	// LiveCmdStart / LiveCmdEnd / LiveCmdRefused
	Command string `json:"command,omitempty"`
	Class   string `json:"class,omitempty"` // read | write | dangerous | unknown | guardrail

	// LiveCmdOutput
	Chunk string `json:"chunk,omitempty"`

	// LiveCmdEnd / LiveCmdRefused
	Status string `json:"status,omitempty"` // ok | device-error | refused | failure
	MS     int64  `json:"ms,omitempty"`     // wall-clock duration of the command
	Bytes  int    `json:"bytes,omitempty"`  // redacted output size
	Reason string `json:"reason,omitempty"` // refusal / failure reason
}

// SetLiveObserver installs the live-event sink (replaces any previous one).
// The callback must be fast and non-blocking — the desktop bridge coalesces
// and forwards on its own goroutine.
func (m *Manager) SetLiveObserver(fn func(LiveEvent)) {
	m.liveMu.Lock()
	m.liveFn = fn
	m.liveMu.Unlock()
}

func (m *Manager) emitLive(ev LiveEvent) {
	m.liveMu.Lock()
	fn := m.liveFn
	m.liveMu.Unlock()
	if fn == nil {
		return
	}
	if ev.Time == 0 {
		ev.Time = time.Now().UnixMilli()
	}
	fn(ev)
}

// sanitizeLiveChunk strips ANSI escapes and redacts secrets from an
// incremental output chunk before it crosses into the UI.
func sanitizeLiveChunk(s string) string {
	if s == "" {
		return ""
	}
	return Redact(ansi.Strip(s))
}

// vtyCap resolves the effective per-device session cap (default 2 — devices
// have scarce VTY lines; NETDEV_SPEC §6.5/B-6).
func (m *Manager) vtyCap() int {
	if n := m.cfg.NetDev.MaxSessionsPerDevice; n > 0 {
		return n
	}
	return 2
}

// vtySnapshot reports the device's current session use under m.mu. The CLI
// session counts as 1 while cached; each in-flight NETCONF subsystem session
// adds 1; an open human terminal adds 1 (§6.1 共享 VTY 预算).
func (m *Manager) vtySnapshotLocked(device string) int {
	use := 0
	if _, ok := m.conns[device]; ok {
		use++ // persistent CLI session
	}
	use += m.netconfInflight[device]
	use += humanTTYCount(device)
	return use
}

// emitConnLive publishes a connection-state event with the current VTY use.
func (m *Manager) emitConnLive(device, state string) {
	m.mu.Lock()
	use := m.vtySnapshotLocked(device)
	m.mu.Unlock()
	m.emitLive(LiveEvent{Kind: LiveConn, Device: device, State: state, VTYUse: use, VTYCap: m.vtyCap()})
}

// subscribeConnState forwards the supervised client's state machine to live
// events, so the panel's status dot follows reconnects in real time. Delivery
// goes through a per-client buffered queue drained by one goroutine: the
// transport's publish loop must NEVER block on the manager lock (Close waits
// for the supervisor, the supervisor waits for publish — a synchronous
// callback taking m.mu deadlocks the pair), and the single drainer preserves
// event order. Overflow drops the newest transition; the next one catches up.
func (m *Manager) subscribeConnState(device string, client *transport.Client) {
	events := make(chan transport.StatusEvent, 16)
	client.Subscribe(func(e transport.StatusEvent) {
		select {
		case events <- e:
		default:
		}
	})
	go func() {
		for e := range events {
			var state string
			switch e.Status {
			case transport.StatusConnecting:
				state = LiveConnConnecting
			case transport.StatusConnected:
				state = LiveConnConnected
			case transport.StatusReconnecting:
				state = LiveConnReconnect
			case transport.StatusStopped:
				state = LiveConnStopped
			default:
				continue
			}
			m.emitConnLive(device, state)
		}
	}()
}

// liveCmdStart / liveCmdEnd / liveCmdRefused are the command-lifecycle
// helpers used by Exec and the one-shot paths (SNMP/NETCONF).
func (m *Manager) liveCmdStart(device, command, class string) time.Time {
	start := time.Now()
	m.emitLive(LiveEvent{Kind: LiveCmdStart, Device: device, Command: command, Class: class, Time: start.UnixMilli()})
	return start
}

func (m *Manager) liveCmdEnd(device, command, class, status string, start time.Time, bytes int, reason string) {
	ev := LiveEvent{Kind: LiveCmdEnd, Device: device, Command: command, Class: class, Status: status, Bytes: bytes}
	if reason != "" {
		ev.Reason = reason
	}
	if !start.IsZero() {
		ev.MS = time.Since(start).Milliseconds()
	}
	m.emitLive(ev)
}

func (m *Manager) liveCmdRefused(device, command, class, reason string) {
	m.emitLive(LiveEvent{Kind: LiveCmdRefused, Device: device, Command: command, Class: class, Status: "refused", Reason: reason})
}

// SanitizeForLive is the exported seam used by tests and the desktop bridge
// to apply the same chunk cleanup the session tap applies.
func SanitizeForLive(s string) string { return sanitizeLiveChunk(s) }

// LiveDeviceState is one device's snapshot entry for the panel's initial paint.
type LiveDeviceState struct {
	Device    string `json:"device"`
	Vendor    string `json:"vendor"`
	OS        string `json:"os,omitempty"`
	Group     string `json:"group,omitempty"`
	Connected bool   `json:"connected"`
	VTYUse    int    `json:"vtyUse"`
	VTYCap    int    `json:"vtyCap"`
}

// LiveSnapshot is the panel's mount-time state: per-device connection/VTY
// state plus the per-turn budget counters.
type LiveSnapshot struct {
	Devices []LiveDeviceState `json:"devices"`
	Spent   int               `json:"spent"`  // commands spent this turn
	Budget  int               `json:"budget"` // turn_command_budget (0 = unlimited)
}

// LiveState builds the snapshot over the configured inventory.
func (m *Manager) LiveState() LiveSnapshot {
	out := LiveSnapshot{Devices: []LiveDeviceState{}, Budget: m.cfg.NetDev.Guardrails.TurnCommandBudget}
	m.mu.Lock()
	out.Spent = m.turnCommands
	for _, d := range m.cfg.NetDev.Devices {
		_, connected := m.conns[d.Name]
		out.Devices = append(out.Devices, LiveDeviceState{
			Device: d.Name, Vendor: d.Vendor, OS: d.OS, Group: d.Group,
			Connected: connected,
			VTYUse:    m.vtySnapshotLocked(d.Name),
			VTYCap:    m.vtyCap(),
		})
	}
	m.mu.Unlock()
	return out
}
