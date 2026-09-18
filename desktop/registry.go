package main

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// registry.go implements the provider-template registry: the list of known
// vendors shown in the onboarding wizard and Settings "add provider" picker.
//
// Data sources (4-layer fallback):
//  1. In-memory cache (populated on startup)
//  2. Local cache file (~/.hiq/registry-cache.json, 12h TTL)
//  3. models.dev remote registry (https://models.dev/api.json)
//  4. Embedded snapshot (default_registry.json) — the ultimate fallback
//
// The embedded snapshot ships with the binary so Hiq always has a working
// vendor list offline. The remote fetch (Step 2) refreshes BaseURL/Models/
// ContextWindow/Vision from models.dev; DisplayName/DocURL/role fields stay
// from the snapshot (they don't change often and models.dev doesn't carry them).

//go:embed default_registry.json
var embedRegistryJSON []byte

// registryTTL is how long the local cache is considered fresh.
const registryTTL = 12 * time.Hour

// modelsDevURL is the remote registry endpoint.
const modelsDevURL = "https://models.dev/api.json"

// registryCacheFilename is the local cache file name (inside the hiq config dir).
const registryCacheFilename = "registry-cache.json"

// trackedVendors maps models.dev vendor IDs to our provider names. Only these
// vendors are pulled from the remote registry; everything else is ignored.
// Multiple models.dev IDs can map to the same provider (e.g. alibaba-cn and
// alibaba both enrich "qwen" — we prefer the CN endpoint since Hiq targets
// Chinese users). Coding-plan variants map to their "-coding" counterparts.
var trackedVendors = map[string]string{
	// Direct vendors (prefer CN endpoints for Chinese users)
	"alibaba-cn":    "qwen",
	"alibaba":       "qwen", // fallback to intl if CN absent
	"deepseek":      "deepseek",
	"volcengine":    "volcengine",
	"zhipuai":       "zhipu", // CN endpoint (bigmodel.cn)
	"zai":           "zhipu", // intl endpoint (z.ai) — fallback
	"minimax-cn":    "minimax",
	"minimax":       "minimax", // intl fallback
	"moonshotai-cn": "moonshot",
	"moonshotai":    "moonshot", // intl fallback
	"xiaomi":        "mimo",
	"stepfun":       "stepfun", // CN endpoint
	"stepfun-ai":    "stepfun", // intl fallback
	"xfyun":         "xfyun",
	"anthropic":     "anthropic",
	"openai":        "openai",
	"xai":           "xai",

	// Aggregators
	"siliconflow-cn": "siliconflow",
	"siliconflow":    "siliconflow",
	"openrouter":     "openrouter",

	// Coding plans
	"alibaba-coding-plan-cn": "qwen-coding",
	"alibaba-coding-plan":    "qwen-coding",
	"zhipuai-coding-plan":    "zhipu-coding",
	"zai-coding-plan":        "zhipu-coding",
	"stepfun-step-plan":      "stepfun-coding",
	"stepfun-ai-step-plan":   "stepfun-coding",
	"tencent-coding-plan":    "tencent-coding",
}

// ModelRegistry holds the in-memory vendor list + cache metadata.
type ModelRegistry struct {
	mu        sync.RWMutex
	templates []ProviderTemplate
	updatedAt time.Time
}

// globalRegistry is the singleton, initialized on startup.
var globalRegistry = &ModelRegistry{}

// loadEmbedSnapshot parses the embedded default_registry.json. Always succeeds
// (the JSON is compile-time-validated); returns the snapshot templates.
func loadEmbedSnapshot() []ProviderTemplate {
	var ts []ProviderTemplate
	if err := json.Unmarshal(embedRegistryJSON, &ts); err != nil {
		// Should never happen — the JSON is committed and tested.
		return nil
	}
	return ts
}

// snapshotByName indexes the embedded snapshot by provider name for quick
// lookup when merging remote data (role fields come from the snapshot).
func snapshotByName(ts []ProviderTemplate) map[string]ProviderTemplate {
	m := make(map[string]ProviderTemplate, len(ts))
	for _, t := range ts {
		m[t.Name] = t
	}
	return m
}

// Get returns the current registry templates (thread-safe). If the registry
// hasn't been initialized yet (startup race), falls back to the embed snapshot
// so the UI always has something to show.
func (r *ModelRegistry) Get() []ProviderTemplate {
	r.mu.RLock()
	if len(r.templates) > 0 {
		out := make([]ProviderTemplate, len(r.templates))
		copy(out, r.templates)
		r.mu.RUnlock()
		return out
	}
	r.mu.RUnlock()
	// Not initialized yet — return embed snapshot immediately (non-blocking).
	return loadEmbedSnapshot()
}

// Find returns the template for a provider name, or nil when unknown.
// Same initialization fallback as Get (embed snapshot on startup race).
func (r *ModelRegistry) Find(providerName string) *ProviderTemplate {
	if providerName == "" {
		return nil
	}
	ts := r.Get()
	for i := range ts {
		if ts[i].Name == providerName {
			return &ts[i]
		}
	}
	return nil
}

// UpdatedAt returns the last successful remote/cache load time.
func (r *ModelRegistry) UpdatedAt() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.updatedAt
}

// set replaces the in-memory templates (called after a successful load/fetch).
func (r *ModelRegistry) set(ts []ProviderTemplate, updatedAt time.Time) {
	// Sort by name for stable UI ordering.
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
	r.mu.Lock()
	r.templates = ts
	r.updatedAt = updatedAt
	r.mu.Unlock()
}

// registryCachePath returns the local cache file path inside the hiq
// config directory. Returns "" if the config dir can't be resolved.
func registryCachePath() string {
	dir := userConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, registryCacheFilename)
}

// userConfigDir is the hiq config directory (same as config.toml).
// Reuses the config package's resolution to stay consistent.
func userConfigDir() string {
	// config.userDir() is the canonical path; replicate its logic without
	// importing the config package (avoid a desktop→config dependency here
	// — userDir uses os.UserConfigDir + the "hiq" dirname).
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "hiq")
}

// GetProviderTemplates returns the current vendor templates for the onboarding
// wizard and Settings "add provider" picker. Returns the in-memory registry if
// initialized, otherwise the embed snapshot (so the UI always renders).
// registryReasoningAmong filters models down to the ones models.dev flags
// reasoning-capable for this provider. Display-only (UI badges); the
// behaviour layer never reads registry data (MODEL_ROUTING_SPEC §5).
func registryReasoningAmong(providerName string, models []string) []string {
	if providerName == "" || len(models) == 0 {
		return nil
	}
	tmpl := globalRegistry.Find(providerName)
	if tmpl == nil || len(tmpl.ReasoningModels) == 0 {
		return nil
	}
	flags := make(map[string]bool, len(tmpl.ReasoningModels))
	for _, m := range tmpl.ReasoningModels {
		flags[m] = true
	}
	out := make([]string, 0, len(models))
	for _, m := range models {
		if flags[m] {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (a *App) GetProviderTemplates() []ProviderTemplate {
	return globalRegistry.Get()
}

// GetRegistryStatus returns metadata about the registry cache for the Settings
// panel "model library" section: when it was last updated.
type RegistryStatus struct {
	UpdatedAt string `json:"updatedAt"` // RFC3339; "" = never (using embed snapshot)
	Source    string `json:"source"`    // "cache" | "remote" | "embed"
}

// GetRegistryStatus reports the registry's freshness for the Settings panel.
func (a *App) GetRegistryStatus() RegistryStatus {
	if t := globalRegistry.UpdatedAt(); !t.IsZero() {
		return RegistryStatus{UpdatedAt: t.Format(time.RFC3339), Source: "cache"}
	}
	return RegistryStatus{Source: "embed"}
}

// RefreshRegistry and initRegistry live in registry_fetcher.go (remote + cache).
