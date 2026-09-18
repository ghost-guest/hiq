package pluginpkg

import (
	"fmt"
	"strings"
)

// Trust tiers for installed plugin packages.
//
// The openhanako reference divides plugins into "restricted" and "full-access"
// around the surfaces it lets a plugin add — extensions/, routes, providers,
// pages and lifecycle hooks. Hiq plugins cannot contribute routes,
// providers or pages at all; their executing surfaces are the runtime process,
// hooks and MCP servers, while skills, agents, commands, prompts and themes are
// purely declarative (they shape prompts and UI and cannot run anything). A
// verbatim port would therefore gate the wrong things, so the tiers are
// re-anchored on Hiq's real capability surface instead of copied.
//
// Two tiers:
//
//   - restricted (default): only the declarative contributions load.
//   - full-access: additionally activates runtime, hooks and MCP servers.
//
// The manifest may *request* a tier (Manifest.TrustHint), but the effective
// tier is granted by the user and persisted in InstalledPlugin.TrustTier; an
// unset grant always falls back to restricted.
type TrustTier string

const (
	// TrustRestricted loads only declarative contributions: skills, agents,
	// commands, prompts and themes. Nothing the package ships can execute.
	TrustRestricted TrustTier = "restricted"
	// TrustFullAccess additionally activates the executing surfaces: the
	// runtime process, hooks (shell commands) and MCP servers (spawned
	// processes or outbound connections).
	TrustFullAccess TrustTier = "full-access"
)

// Valid reports whether t is one of the two defined tiers.
func (t TrustTier) Valid() bool {
	return t == TrustRestricted || t == TrustFullAccess
}

// NormalizeTrustTier maps empty or unknown values to the safe default
// (restricted), so a missing or corrupted grant can never widen access.
func NormalizeTrustTier(t TrustTier) TrustTier {
	if TrustTier(strings.TrimSpace(string(t))) == TrustFullAccess {
		return TrustFullAccess
	}
	return TrustRestricted
}

// BlockedSurface names one executing capability withheld in restricted mode.
type BlockedSurface string

const (
	// SurfaceRuntime is the package's runtime process (FULL TRUST).
	SurfaceRuntime BlockedSurface = "runtime"
	// SurfaceHook is all hook declarations (event-driven shell commands).
	SurfaceHook BlockedSurface = "hook"
	// SurfaceMCPServer is all MCP server declarations.
	SurfaceMCPServer BlockedSurface = "mcpServer"
)

// CapabilityGate is the intersection of what a package declares and what its
// effective trust tier permits. Callers must consult this instead of the raw
// CapabilitySummary when loading contributions: Allowed lists what may load,
// Blocked names what was withheld and why.
type CapabilityGate struct {
	Tier    TrustTier
	Allowed CapabilitySummary
	// Blocked counts withheld contributions per executing surface. A nil map
	// means nothing was withheld (fully allowed).
	Blocked map[BlockedSurface]int
}

// BlockedCount is the total number of withheld contributions.
func (g CapabilityGate) BlockedCount() int {
	n := 0
	for _, c := range g.Blocked {
		n += c
	}
	return n
}

// AllowsExecution reports whether the tier permits every executing surface.
func (g CapabilityGate) AllowsExecution() bool { return g.BlockedCount() == 0 }

// Withholds reports whether a specific executing surface is withheld.
func (g CapabilityGate) Withholds(surface BlockedSurface) bool {
	_, ok := g.Blocked[surface]
	return ok
}

// Summary renders the withheld surfaces as a compact phrase, e.g.
// "1 runtime, 2 hooks, 1 MCP server". Empty when nothing is withheld.
func (g CapabilityGate) Summary() string {
	if len(g.Blocked) == 0 {
		return ""
	}
	// Stable order so the text never reshuffles between runs.
	order := []struct {
		surface BlockedSurface
		label   string
	}{
		{SurfaceRuntime, "runtime"},
		{SurfaceHook, "hook"},
		{SurfaceMCPServer, "MCP server"},
	}
	var parts []string
	for _, o := range order {
		if n := g.Blocked[o.surface]; n > 0 {
			label := o.label
			if n != 1 {
				label += "s"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	return strings.Join(parts, ", ")
}

// RequestedTrust is the tier the manifest asks for, defaulting to restricted.
func (p Package) RequestedTrust() TrustTier {
	return NormalizeTrustTier(p.Manifest.TrustHint)
}

// EffectiveTrustTier resolves the tier a plugin actually runs under: the grant
// stored in installed state, defaulting to restricted when unset. The
// manifest's requested tier never grants by itself.
func EffectiveTrustTier(installed InstalledPlugin) TrustTier {
	return NormalizeTrustTier(installed.TrustTier)
}

// GateFor applies an explicit tier to a parsed package.
func GateFor(pkg Package, tier TrustTier) CapabilityGate {
	summary := pkg.CapabilitySummary()
	g := CapabilityGate{Tier: NormalizeTrustTier(tier), Allowed: summary}
	if g.Tier == TrustFullAccess {
		return g // nothing withheld
	}
	blocked := map[BlockedSurface]int{}
	if summary.Runtime {
		blocked[SurfaceRuntime] = 1
		g.Allowed.Runtime = false
	}
	if summary.Hooks > 0 {
		blocked[SurfaceHook] = summary.Hooks
		g.Allowed.Hooks = 0
	}
	if summary.MCPServers > 0 {
		blocked[SurfaceMCPServer] = summary.MCPServers
		g.Allowed.MCPServers = 0
	}
	if len(blocked) > 0 {
		g.Blocked = blocked
	}
	return g
}

// GateForInstalled applies the plugin's effective tier to a parsed package.
func GateForInstalled(installed InstalledPlugin, pkg Package) CapabilityGate {
	return GateFor(pkg, EffectiveTrustTier(installed))
}

// SetTrustTier grants a tier to an installed plugin and persists it. Unknown
// tiers are rejected so a typo cannot silently widen access.
func SetTrustTier(hiqHome, name string, tier TrustTier) error {
	if !tier.Valid() {
		return fmt.Errorf("trust tier %q must be %q or %q", tier, TrustRestricted, TrustFullAccess)
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	st, err := LoadState(hiqHome)
	if err != nil {
		return err
	}
	for i := range st.Plugins {
		if st.Plugins[i].Name == name {
			st.Plugins[i].TrustTier = NormalizeTrustTier(tier)
			return SaveState(hiqHome, st)
		}
	}
	return fmt.Errorf("plugin %q is not installed", name)
}
