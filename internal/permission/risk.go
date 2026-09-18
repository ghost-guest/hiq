package permission

// risk.go introduces a 4-level RiskClass for tool calls (SPEC v2 §3.2A),
// replacing the hardcoded isIrreversibleOutwardTool switch. The class is an
// internal implementation detail — users never see "RiskClass"; they only
// experience "local ops don't ask me, outward ops do" (which is the intuitive
// expectation). The class drives whether headless mode hard-denies (EXTERNAL)
// vs allows-with-autonomy (everything else).
//
// Adapted from openworker's risk.py 4-level model (READ/WRITE_LOCAL/EXEC/
// EXTERNAL), with Hiq-specific defaults: MCP tools (mcp__*) default to
// EXTERNAL (safe default — an external server's tool is treated as outward
// until configured otherwise), and builtin outward tools (email_send,
// rag_delete) are table-driven so adding one is a one-line change, not a code
// branch in Gate.Check.
//
// Two constraints from SPEC v2 §2.0: (1) zero user learning cost — the class
// is invisible, classification is automatic; (2) no prompt bloat — Classify is
// pure host logic, never reaches the model.

import "strings"

// RiskClass ranks a tool call's blast radius. Higher = more dangerous / less
// reversible. The order matters for comparisons (EXTERNAL > EXEC > etc.).
type RiskClass int

const (
	// RiskRead: read-only inspection (read_file, grep, ls). Cannot change state.
	RiskRead RiskClass = iota
	// RiskWriteLocal: writes inside the workspace (edit_file, write_file). Can be
	// rewound via checkpoint/rewind, so the blast radius is contained.
	RiskWriteLocal
	// RiskExec: shell command execution (bash). Side effects outside files
	// (processes, network) but within the user's machine.
	RiskExec
	// RiskExternal: outward, irreversible operations (email_send, rag_delete,
	// external API calls via MCP). Affects the world outside the workspace and
	// cannot be undone. Headless mode denies these unless explicitly allowed.
	RiskExternal
)

// builtinRisk is the table of built-in tools with a fixed risk class that
// differs from what the readOnly flag alone would imply. Adding a new outward
// builtin tool is a one-line addition here — no Gate.Check code change needed.
var builtinRisk = map[string]RiskClass{
	"email_send": RiskExternal, // sends real email to real recipients
	"rag_delete": RiskExternal, // deletes knowledge-base entries irreversibly
}

// Classify determines a tool call's RiskClass. Priority (first match wins):
//  1. overrides[toolName] — explicit per-tool config (exact name)
//  2. overrides prefix match — an "mcp__<server>__" key covers all that server's
//     tools (so [[plugins]] risk="read" marks every tool on that server without
//     naming them individually). Built from config at boot.
//  3. builtinRisk[toolName] — the table above (email_send, rag_delete, …)
//  4. MCP tools (mcp__ prefix) default to RiskExternal — a remote server's tool
//     is treated as outward until the user configures otherwise (safe default,
//     mirrors openworker's MCP requires_approval=True).
//  5. "bash" tool → RiskExec (shell execution has process/network side effects)
//  6. readOnly=true → RiskRead; otherwise RiskWriteLocal (the catch-all for
//     workspace writes like edit_file/write_file, which are rewind-recoverable)
//
// Classify is a pure function (no locks, no I/O) so it is trivially unit-testable.
func Classify(toolName string, readOnly bool, overrides map[string]RiskClass) RiskClass {
	if r, ok := overrides[toolName]; ok {
		return r
	}
	// Per-server prefix override: an "mcp__<server>__" key applies to all the
	// server's tools. We only need this for mcp__ names (builtins have no prefix).
	if strings.HasPrefix(toolName, "mcp__") {
		if r, ok := matchMCPServerOverride(toolName, overrides); ok {
			return r
		}
	}
	if r, ok := builtinRisk[toolName]; ok {
		return r
	}
	if strings.HasPrefix(toolName, "mcp__") {
		return RiskExternal
	}
	if toolName == "bash" {
		return RiskExec
	}
	if readOnly {
		return RiskRead
	}
	return RiskWriteLocal
}

// matchMCPServerOverride checks whether overrides contains a per-server prefix
// key matching toolName. An MCP tool name is "mcp__<server>__<tool>"; a config
// override keyed "mcp__<server>__" (trailing underscores) covers every tool on
// that server. Returns the class and ok=true on the longest matching prefix.
func matchMCPServerOverride(toolName string, overrides map[string]RiskClass) (RiskClass, bool) {
	// Find the server segment: "mcp__<server>__". The tool name has at least
	// three "__"-separated parts (mcp, server, tool); the server prefix is
	// everything up to and including the second "__".
	rest := strings.TrimPrefix(toolName, "mcp__")
	idx := strings.Index(rest, "__")
	if idx < 0 {
		return 0, false
	}
	serverPrefix := "mcp__" + rest[:idx] + "__"
	if r, ok := overrides[serverPrefix]; ok {
		return r, true
	}
	return 0, false
}

// IsExternal is the predicate Gate.Check consults in headless mode (it replaces
// the old isIrreversibleOutwardTool). A tool is "external" when its risk class
// is RiskExternal — i.e. outward and irreversible, the operations that must not
// silently fire from an unattended scheduled task.
func IsExternal(toolName string, readOnly bool, overrides map[string]RiskClass) bool {
	return Classify(toolName, readOnly, overrides) == RiskExternal
}

// ParseRiskClass maps a config string ("read"/"write_local"/"exec"/"external")
// to a RiskClass. Unknown/empty defaults to RiskExternal so a typo or omission
// fails safe (treats the tool as high-risk, prompting approval) rather than
// silently allowing an outward operation.
func ParseRiskClass(s string) RiskClass {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "read", "read-only", "readonly":
		return RiskRead
	case "write", "write_local", "write-local", "writelocal":
		return RiskWriteLocal
	case "exec", "execute":
		return RiskExec
	case "external", "outward", "outbound":
		return RiskExternal
	default:
		// Empty or unrecognized → safe default. An omitted risk on an MCP server
		// must not downgrade it to read-only silently.
		return RiskExternal
	}
}
