package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/fairpeer/internal/agent"
	"github.com/zzycxz/fairpeer/internal/boot"
	"github.com/zzycxz/fairpeer/internal/config"
	"github.com/zzycxz/fairpeer/internal/installsource"
	"github.com/zzycxz/fairpeer/internal/provider"
	"github.com/zzycxz/fairpeer/internal/secret"
)

// settings_app.go is the desktop Settings panel's command surface: it reads the
// resolved config and applies edits through internal/config/edit.go (the
// purpose-built mutation API), then rebuilds the controller so the change takes
// effect live — the same snapshot→reload→resume pattern as SetModel. Secrets are
// the exception: they go to the encrypted secret store (upsertCredential), since
// config stores only the env-var name, not the key.

// --- read ---

type ProviderView struct {
	Name              string   `json:"name"`
	BuiltIn           bool     `json:"builtIn"`
	Added             bool     `json:"added"`
	Kind              string   `json:"kind"`
	BaseURL           string   `json:"baseUrl"`
	Models            []string `json:"models"`
	ModelsURL         string   `json:"modelsUrl"`
	Default           string   `json:"default"`
	APIKeyEnv         string   `json:"apiKeyEnv"`
	KeySet            bool     `json:"keySet"` // the env var currently resolves to a non-empty value
	ContextWindow     int      `json:"contextWindow"`
	ReasoningProtocol string   `json:"reasoningProtocol"`
	SupportedEfforts  []string `json:"supportedEfforts"`
	DefaultEffort     string   `json:"defaultEffort"`
	// ReasoningModels are this provider's chat models that the models.dev
	// registry flags reasoning-capable. Display-only (pickers show a badge);
	// never consulted by the behaviour layer (MODEL_ROUTING_SPEC §5).
	ReasoningModels []string `json:"reasoningModels,omitempty"`
}

type PermissionsView struct {
	Mode  string   `json:"mode"`
	Allow []string `json:"allow"`
	Ask   []string `json:"ask"`
	Deny  []string `json:"deny"`
}

type SandboxView struct {
	Bash          string   `json:"bash"`
	Network       bool     `json:"network"`
	WorkspaceRoot string   `json:"workspaceRoot"`
	AllowWrite    []string `json:"allowWrite"`
}

type NetworkProxyView struct {
	Type     string `json:"type"`
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type NetworkView struct {
	ProxyMode string           `json:"proxyMode"`
	ProxyURL  string           `json:"proxyUrl"`
	NoProxy   string           `json:"noProxy"`
	Proxy     NetworkProxyView `json:"proxy"`
}

type AgentView struct {
	Temperature     float64 `json:"temperature"`
	MaxSteps        int     `json:"maxSteps"`
	PlannerMaxSteps int     `json:"plannerMaxSteps"`
	SystemPrompt    string  `json:"systemPrompt"`
	RPM             int     `json:"rpm"` // max requests/minute; 0 = unlimited
}

type BotAllowlistView struct {
	Enabled      bool     `json:"enabled"`
	AllowAll     bool     `json:"allowAll"`
	Mode         string   `json:"mode"` // "open" | "review"
	QQUsers      []string `json:"qqUsers"`
	FeishuUsers  []string `json:"feishuUsers"`
	WeixinUsers  []string `json:"weixinUsers"`
	QQGroups     []string `json:"qqGroups"`
	FeishuGroups []string `json:"feishuGroups"`
	WeixinGroups []string `json:"weixinGroups"`
}

type QQBotView struct {
	Enabled      bool   `json:"enabled"`
	AppID        string `json:"appId"`
	AppSecretEnv string `json:"appSecretEnv"`
	SecretSet    bool   `json:"secretSet"`
}

type FeishuBotView struct {
	Enabled           bool   `json:"enabled"`
	Domain            string `json:"domain"`
	AppID             string `json:"appId"`
	AppSecretEnv      string `json:"appSecretEnv"`
	SecretSet         bool   `json:"secretSet"`
	VerificationToken string `json:"verificationToken"`
	Mode              string `json:"mode"`
	WebhookPort       int    `json:"webhookPort"`
	RequireMention    bool   `json:"requireMention"`
}

type WeixinBotView struct {
	Enabled   bool   `json:"enabled"`
	AccountID string `json:"accountId"`
	TokenEnv  string `json:"tokenEnv"`
	TokenSet  bool   `json:"tokenSet"`
	APIBase   string `json:"apiBase"`
}

type BotSettingsView struct {
	Enabled     bool                `json:"enabled"`
	Model       string              `json:"model"`
	MaxSteps    int                 `json:"maxSteps"`
	DebounceMs  int                 `json:"debounceMs"`
	Allowlist   BotAllowlistView    `json:"allowlist"`
	QQ          QQBotView           `json:"qq"`
	Feishu      FeishuBotView       `json:"feishu"`
	Weixin      WeixinBotView       `json:"weixin"`
	Connections []BotConnectionView `json:"connections"`
}

type WebSearchView struct {
	BraveKeySet     bool `json:"braveKeySet"`
	ExaKeySet       bool `json:"exaKeySet"`
	LinkupKeySet    bool `json:"linkupKeySet"`
	AnySearchKeySet bool `json:"anysearchKeySet"`
}

// SettingsView is the whole Settings panel payload.
type SettingsView struct {
	DefaultModel string `json:"defaultModel"`
	PlannerModel string `json:"plannerModel"`
	// FastTaskModel is the lightweight model dream/distill run on (background
	// tasks). The SettingsPanel exposes a per-model picker next to the default
	// model so the user can route background tasks to a cheaper/faster model.
	FastTaskModel     string             `json:"fastTaskModel"`
	SubagentModel     string             `json:"subagentModel"`
	SubagentEffort    string             `json:"subagentEffort"`
	AutoPlan          string             `json:"autoPlan"`
	Providers         []ProviderView     `json:"providers"`
	OfficialProviders []ProviderView     `json:"officialProviders"`
	Permissions       PermissionsView    `json:"permissions"`
	Sandbox           SandboxView        `json:"sandbox"`
	Network           NetworkView        `json:"network"`
	Agent             AgentView          `json:"agent"`
	Bot               BotSettingsView    `json:"bot"`
	Cowork            CoWorkSettingsView `json:"cowork"`
	WebSearch         WebSearchView      `json:"webSearch"`
	// Patrol is the proactive-inspection heartbeat's permission dial plus live
	// status. See patrol_app.go.
	Patrol            PatrolView `json:"patrol"`
	DesktopLanguage   string     `json:"desktopLanguage"`
	DesktopTheme      string     `json:"desktopTheme"`
	DesktopThemeStyle string     `json:"desktopThemeStyle"`
	CloseBehavior     string     `json:"closeBehavior"`
	DisplayMode       string     `json:"displayMode"`
	CheckUpdates      bool       `json:"checkUpdates"`
	Telemetry         bool       `json:"telemetry"`
	Metrics           bool       `json:"metrics"`
	ExpandThinking    bool       `json:"expandThinking"`
	ConfigPath        string     `json:"configPath"`
	// ProviderKinds lists the provider implementations the kernel actually
	// registered (provider.Kinds()), so the editor's "kind" picker offers only
	// kinds that resolve — selecting an unregistered one would fail the rebuild.
	ProviderKinds []string `json:"providerKinds"`
	// AutoApproveTools is the live YOLO/full-access state (runtime-only, not from
	// config), so the panel's toggle reflects whether tool approvals are currently
	// being skipped this session.
	AutoApproveTools bool `json:"autoApproveTools"`
	// Bypass is the legacy JSON key for the same live state.
	Bypass bool `json:"bypass"`
	// SecretStore reports the at-rest encryption backend of the credential
	// store so the panel can badge a degraded (machine-bound) mode or an
	// unreachable keystore.
	SecretStore SecretStoreView `json:"secretStore"`
}

// SecretStoreView mirrors secret.Store.SecurityMode: backend is one of
// dpapi/keychain/secret-service/passphrase/machine, or "unavailable" when an
// existing store's KEK cannot be reached (keystore locked/reset).
type SecretStoreView struct {
	Backend  string `json:"backend"`
	Degraded bool   `json:"degraded"`
}

func secretStoreView() SecretStoreView {
	backend, degraded := secret.Default().SecurityMode()
	return SecretStoreView{Backend: backend, Degraded: degraded}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func providerRemovalFallbackRef(c *config.Config, name string) string {
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.Name == name || !p.Configured() || len(p.ModelList()) == 0 {
			continue
		}
		return p.Name + "/" + p.DefaultModel()
	}
	return ""
}

func desktopModelRefsProvider(c *config.Config, ref, name string) bool {
	if config.ModelRefsProvider(ref, name) {
		return true
	}
	if e, ok := c.ResolveModel(ref); ok {
		return e.Name == name
	}
	return false
}

func officialProviderHost(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// officialProviderKindFromEntry previously detected built-in official providers.
// FairPeer ships no official presets, so this always returns "".
func officialProviderKindFromEntry(p config.ProviderEntry) string {
	return ""
}

func isOfficialBuiltInProvider(p config.ProviderEntry) bool {
	return officialProviderKindFromEntry(p) != ""
}

func providerAccessSet(names []string) map[string]bool {
	out := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			out[name] = true
		}
	}
	return out
}

func addProviderAccess(c *config.Config, names ...string) {
	seen := providerAccessSet(c.Desktop.ProviderAccess)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		c.Desktop.ProviderAccess = append(c.Desktop.ProviderAccess, name)
		seen[name] = true
	}
}

func removeProviderAccess(c *config.Config, names ...string) {
	remove := providerAccessSet(names)
	if len(remove) == 0 {
		return
	}
	out := c.Desktop.ProviderAccess[:0]
	for _, name := range c.Desktop.ProviderAccess {
		if !remove[name] {
			out = append(out, name)
		}
	}
	c.Desktop.ProviderAccess = out
}

func providerViewFromEntry(p config.ProviderEntry, builtIn, added bool) ProviderView {
	models := nonNil(p.ChatModelList())
	return ProviderView{
		Name: p.Name, BuiltIn: builtIn, Added: added, Kind: p.Kind, BaseURL: p.BaseURL,
		Models: models, ModelsURL: p.ModelsURL, Default: p.DefaultModel(),
		APIKeyEnv: p.APIKeyEnv,
		// KeySet doubles as "ready to use": keyless providers (local endpoints)
		// are ready by definition, so pickers don't filter them out.
		KeySet:            p.APIKeyEnv == "" || os.Getenv(p.APIKeyEnv) != "",
		ContextWindow:     p.ContextWindow,
		ReasoningProtocol: p.ReasoningProtocol,
		SupportedEfforts:  nonNil(p.SupportedEfforts),
		DefaultEffort:     p.DefaultEffort,
		// Registry-flagged reasoning models among the enabled chat models
		// (display-only badge; unknown providers yield nil).
		ReasoningModels: registryReasoningAmong(p.Name, models),
	}
}

func officialProviderViews(added map[string]bool) []ProviderView {
	// FairPeer ships no preset official providers, so this is always empty —
	// but the frontend expects a non-nil [] (not null), so allocate it.
	return []ProviderView{}
}

func officialProviderAddedSet(cfg *config.Config) map[string]bool {
	out := map[string]bool{}
	if cfg == nil {
		return out
	}
	access := providerAccessSet(cfg.Desktop.ProviderAccess)
	for i := range cfg.Providers {
		p := cfg.Providers[i]
		if !access[p.Name] {
			continue
		}
		if kind := officialProviderKindFromEntry(p); kind != "" {
			out[kind] = true
		}
	}
	return out
}

// Settings returns the current configuration for the Settings panel.
func (a *App) Settings() SettingsView {
	cfg, cfgPath, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		return SettingsView{
			Providers:         []ProviderView{},
			OfficialProviders: officialProviderViews(map[string]bool{}),
			ProviderKinds:     nonNil(provider.Kinds()),
			Permissions: PermissionsView{
				Mode:  "ask",
				Allow: []string{},
				Ask:   []string{},
				Deny:  []string{},
			},
			Sandbox: SandboxView{Bash: "enforce", AllowWrite: []string{}},
			Agent:   AgentView{PlannerMaxSteps: 12},
			Bot:     botSettingsView(config.BotConfig{}),
			Cowork:  coworkSettingsView(config.CoworkConfig{}),
			WebSearch: WebSearchView{
				BraveKeySet:     os.Getenv("BRAVE_API_KEY") != "" || os.Getenv("BRAVE_SEARCH_API_KEY") != "",
				ExaKeySet:       os.Getenv("EXA_API_KEY") != "",
				LinkupKeySet:    os.Getenv("LINKUP_API_KEY") != "",
				AnySearchKeySet: os.Getenv("ANYSEARCH_API_KEY") != "",
			},
			AutoPlan:          "off",
			DesktopTheme:      "light",
			DesktopThemeStyle: "slate",
			CloseBehavior:     "background",
			DisplayMode:       "minimal",
			CheckUpdates:      true,
			Telemetry:         true,
			Metrics:           false,
			ExpandThinking:    false,
			SecretStore:       secretStoreView(),
			Patrol:            patrolViewWhenUnreadable(),
		}
	}
	ctrl := a.activeCtrl()
	bash := cfg.Sandbox.Bash
	if bash == "" {
		bash = "enforce"
	}
	v := SettingsView{
		DefaultModel:      cfg.DefaultModel,
		PlannerModel:      cfg.Agent.PlannerModel,
		FastTaskModel:     cfg.Agent.FastTaskModel,
		SubagentModel:     cfg.Agent.SubagentModel,
		SubagentEffort:    cfg.Agent.SubagentEffort,
		AutoPlan:          desktopAutoPlanMode(cfg.Agent.AutoPlan),
		Providers:         []ProviderView{},
		OfficialProviders: []ProviderView{},
		Permissions: PermissionsView{
			Mode:  orDefault(cfg.Permissions.Mode, "ask"),
			Allow: nonNil(cfg.Permissions.Allow),
			Ask:   nonNil(cfg.Permissions.Ask),
			Deny:  nonNil(cfg.Permissions.Deny),
		},
		Sandbox: SandboxView{
			Bash: bash, Network: cfg.Sandbox.Network,
			WorkspaceRoot: cfg.Sandbox.WorkspaceRoot, AllowWrite: nonNil(cfg.Sandbox.AllowWrite),
		},
		Network: NetworkView{
			ProxyMode: cfg.NetworkProxyMode(),
			ProxyURL:  cfg.Network.ProxyURL,
			NoProxy:   cfg.Network.NoProxy,
			Proxy: NetworkProxyView{
				Type:     orDefault(cfg.Network.Proxy.Type, "socks5"),
				Server:   cfg.Network.Proxy.Server,
				Port:     cfg.Network.Proxy.Port,
				Username: cfg.Network.Proxy.Username,
				Password: cfg.Network.Proxy.Password,
			},
		},
		Agent: AgentView{Temperature: cfg.Agent.Temperature, MaxSteps: cfg.Agent.MaxSteps, PlannerMaxSteps: cfg.Agent.PlannerMaxSteps, SystemPrompt: cfg.Agent.SystemPrompt, RPM: cfg.LLM.RPM},
		Bot:   botSettingsView(cfg.Bot),
		Cowork: func() CoWorkSettingsView {
			cv := coworkSettingsView(cfg.Cowork)
			// Reflect whether email_send is in permissions.Allow (the headless
			// auto-send toggle). Read from the loaded config so the checkbox
			// shows the true state after a save/restart.
			for _, rule := range cfg.Permissions.Allow {
				if strings.EqualFold(strings.TrimSpace(rule), "email_send") {
					cv.AllowHeadlessEmail = true
					break
				}
			}
			return cv
		}(),
		WebSearch: WebSearchView{
			BraveKeySet:     os.Getenv("BRAVE_API_KEY") != "" || os.Getenv("BRAVE_SEARCH_API_KEY") != "",
			ExaKeySet:       os.Getenv("EXA_API_KEY") != "",
			LinkupKeySet:    os.Getenv("LINKUP_API_KEY") != "",
			AnySearchKeySet: os.Getenv("ANYSEARCH_API_KEY") != "",
		},
		Patrol:            patrolView(cfg),
		DesktopLanguage:   cfg.DesktopLanguage(),
		DesktopTheme:      cfg.DesktopTheme(),
		DesktopThemeStyle: cfg.DesktopThemeStyle(),
		CloseBehavior:     cfg.DesktopCloseBehavior(),
		DisplayMode:       cfg.DesktopDisplayMode(),
		CheckUpdates:      cfg.DesktopCheckUpdates(),
		Telemetry:         cfg.DesktopTelemetry(),
		Metrics:           cfg.DesktopMetrics(),
		ExpandThinking:    cfg.Desktop.ExpandThinking,
		ConfigPath:        cfgPath,
		ProviderKinds:     nonNil(provider.Kinds()),
		AutoApproveTools:  ctrl != nil && ctrl.AutoApproveTools(),
		Bypass:            ctrl != nil && ctrl.AutoApproveTools(),
		SecretStore:       secretStoreView(),
	}
	added := providerAccessSet(cfg.Desktop.ProviderAccess)
	v.OfficialProviders = officialProviderViews(officialProviderAddedSet(cfg))
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		v.Providers = append(v.Providers, providerViewFromEntry(*p, isOfficialBuiltInProvider(*p), added[p.Name]))
	}
	return v
}

func botSettingsView(b config.BotConfig) BotSettingsView {
	mode := strings.TrimSpace(b.Feishu.Mode)
	if mode == "" {
		mode = "websocket"
	}
	return BotSettingsView{
		Enabled:    b.Enabled,
		Model:      b.Model,
		MaxSteps:   b.MaxSteps,
		DebounceMs: b.DebounceMs,
		Allowlist: BotAllowlistView{
			Enabled:      b.Allowlist.Enabled,
			AllowAll:     b.Allowlist.AllowAll,
			Mode:         allowlistModeOrDefault(b.Allowlist.Mode),
			QQUsers:      nonNil(b.Allowlist.QQUsers),
			FeishuUsers:  nonNil(b.Allowlist.FeishuUsers),
			WeixinUsers:  nonNil(b.Allowlist.WeixinUsers),
			QQGroups:     nonNil(b.Allowlist.QQGroups),
			FeishuGroups: nonNil(b.Allowlist.FeishuGroups),
			WeixinGroups: nonNil(b.Allowlist.WeixinGroups),
		},
		QQ: QQBotView{
			Enabled:      b.QQ.Enabled,
			AppID:        b.QQ.AppID,
			AppSecretEnv: b.QQ.AppSecretEnv,
			SecretSet:    strings.TrimSpace(b.QQ.AppSecretEnv) != "" && os.Getenv(b.QQ.AppSecretEnv) != "",
		},
		Feishu: FeishuBotView{
			Enabled:           b.Feishu.Enabled,
			Domain:            orDefault(strings.TrimSpace(b.Feishu.Domain), "feishu"),
			AppID:             b.Feishu.AppID,
			AppSecretEnv:      b.Feishu.AppSecretEnv,
			SecretSet:         strings.TrimSpace(b.Feishu.AppSecretEnv) != "" && os.Getenv(b.Feishu.AppSecretEnv) != "",
			VerificationToken: b.Feishu.VerificationToken,
			Mode:              mode,
			WebhookPort:       b.Feishu.WebhookPort,
			RequireMention:    b.Feishu.RequireMention,
		},
		Weixin: WeixinBotView{
			Enabled:   b.Weixin.Enabled,
			AccountID: b.Weixin.AccountID,
			TokenEnv:  b.Weixin.TokenEnv,
			TokenSet:  strings.TrimSpace(b.Weixin.TokenEnv) != "" && os.Getenv(b.Weixin.TokenEnv) != "",
			APIBase:   b.Weixin.APIBase,
		},
		Connections: botConnectionViews(b.Connections),
	}
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func allowlistModeOrDefault(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "review" {
		return "review"
	}
	return "open"
}

func botDomainOrDefault(domain string) string {
	if strings.EqualFold(strings.TrimSpace(domain), "lark") {
		return "lark"
	}
	return "feishu"
}

// --- apply (write config, then rebuild the controller so it's live) ---

// configApplyMu serializes the load→mutate→save read-modify-write over the
// user config TOML. The file write itself is atomic (temp+rename), but two
// concurrent settings saves (e.g. netdev vs bot) reading before either wrote
// would silently drop the other's section.
var configApplyMu sync.Mutex

// applyConfigChange mutates the user-global config and rebuilds the controller so
// the change takes effect this session. Desktop settings such as providers and
// keys are account-level, not per-project: writing them to the global config
// rather than the cwd's fairpeer.toml is what lets them survive a workspace switch.
func (a *App) applyConfigChange(mutate func(*config.Config) error) error {
	if err := a.applyConfigOnly(mutate); err != nil {
		return err
	}
	return a.rebuild()
}

func (a *App) applyConfigOnly(mutate func(*config.Config) error) error {
	configApplyMu.Lock()
	defer configApplyMu.Unlock()
	cfg, path, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		return err
	}
	if err := mutate(cfg); err != nil {
		return err
	}
	return cfg.SaveTo(path)
}

func (a *App) loadDesktopUserConfigForEdit() (*config.Config, string, error) {
	userPath := config.UserConfigPath()
	if userPath == "" {
		return nil, "", fmt.Errorf("cannot resolve user config directory")
	}
	// Strict load: these configs are loaded in order to be saved back. If the
	// file exists but cannot be parsed, refusing to save (fail closed) is the
	// only safe move — silently persisting a defaults-shaped config would wipe
	// the user's providers and every other setting.
	if _, err := os.Stat(userPath); err == nil {
		cfg, err := config.LoadForEditStrict(userPath)
		if err != nil {
			return nil, "", err
		}
		normalizeLegacyDesktopProviderAccessForSettings(cfg, userPath)
		return cfg, userPath, nil
	}
	cfg, err := config.LoadForEditStrict(userPath)
	if err != nil {
		return nil, "", err
	}
	legacyPath := config.SourcePathForRoot(a.activeWorkspaceRoot())
	if legacyPath == "" || sameConfigPath(legacyPath, userPath) {
		normalizeLegacyDesktopProviderAccessForSettings(cfg, userPath)
		return cfg, userPath, nil
	}
	legacyCfg, err := config.LoadForEditStrict(legacyPath)
	if err != nil {
		return nil, "", fmt.Errorf("seed from legacy config: %w", err)
	}
	normalizeLegacyDesktopProviderAccessForSettings(legacyCfg, legacyPath)
	legacyCfg.ConfigVersion = config.Default().ConfigVersion
	return legacyCfg, userPath, nil
}

func normalizeLegacyDesktopProviderAccessForSettings(cfg *config.Config, path string) {
	if cfg == nil || len(cfg.Desktop.ProviderAccess) > 0 || configDeclaresProviderAccess(path) {
		return
	}
	config.NormalizeLegacyDesktopProviderAccess(cfg)
}

func configDeclaresProviderAccess(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		if before, _, ok := strings.Cut(line, "#"); ok {
			line = before
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "provider_access") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, "provider_access"))
			return strings.HasPrefix(rest, "=")
		}
	}
	return false
}

func (a *App) activeWorkspaceRoot() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if tab := a.activeTabLocked(); tab != nil {
		return tab.WorkspaceRoot
	}
	return "."
}

func projectConfigPathForRoot(root string) string {
	if strings.TrimSpace(root) == "" || root == "." {
		return "fairpeer.toml"
	}
	return filepath.Join(root, "fairpeer.toml")
}

func sameConfigPath(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	aAbs, aErr := filepath.Abs(a)
	bAbs, bErr := filepath.Abs(b)
	if aErr == nil && bErr == nil {
		return filepath.Clean(aAbs) == filepath.Clean(bAbs)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// rebuild tears down the controller and rebuilds it from the (just-changed)
// config, carrying the conversation forward. It keeps the active model if it
// still resolves; otherwise it falls back to the new default. Mirrors SetModel.
func (a *App) rebuild() error {
	if a.ctx == nil {
		return nil
	}
	tab := a.activeTab()
	if tab == nil {
		return fmt.Errorf("no active tab")
	}
	// Snapshot old controller + scalar fields under RLock to avoid TOCTOU on tab.Ctrl.
	a.mu.RLock()
	oldCtrl := tab.Ctrl
	model := tab.model
	root := tab.WorkspaceRoot
	sink := tab.sink
	effort := tab.effort
	a.mu.RUnlock()

	var carried []provider.Message
	prevPath := ""
	if oldCtrl != nil {
		prevPath = oldCtrl.SessionPath()
		_ = oldCtrl.Snapshot()
		carried = oldCtrl.History()
	}
	if cfg, err := config.LoadForRoot(root); err == nil {
		if resolved, fallback, ok := cfg.ResolveModelWithFallback(model); ok {
			if fallback && strings.TrimSpace(model) != "" {
				a.noticeForTab(tab.ID, fmt.Sprintf("model %q is no longer available; switched to %s", model, resolved))
			}
			model = resolved
		}
	}
	ctrl, err := boot.Build(a.bootContext(), boot.Options{
		Model: model, RequireKey: false,
		Sink:           sink,
		WorkspaceRoot:  root,
		SessionDir:     tabSessionDir(tab),
		EffortOverride: cloneStringPtr(effort),
	})
	if err != nil {
		a.mu.Lock()
		tab.StartupErr = err.Error()
		tab.Ready = true
		a.mu.Unlock()
		a.emitReady(a.ctx, tab.ID)
		return err
	}
	// boot.Build (re)built the global RPM budget; rebind it into RAG extraction
	// and the platform direct-call path so a runtime RPM change propagates to all
	// request paths, not just the main conversation. cfg resolves the RAG bucket
	// key to the configured extract model; nil falls back to the platform key.
	rebuildCfg, _ := config.LoadForRoot(root)
	boot.RebindRAGBudget(a.ragExtractor, rebuildCfg)

	a.bindControllerDisplayRecorder(ctrl)
	a.mu.Lock()
	if tab.Ctrl == oldCtrl {
		tab.Ctrl = ctrl
		if oldCtrl != nil {
			oldCtrl.Close()
		}
	} else {
		// Another goroutine rebuilt this tab between our RUnlock and Lock, so
		// the controller it installed (current tab.Ctrl) differs from our
		// snapshot (oldCtrl). Both are superseded by ctrl: close whichever of
		// them we're overwriting, plus our snapshot, so neither leaks its
		// background jobs/hooks.
		superseded := tab.Ctrl // the concurrently-installed controller we replace
		tab.Ctrl = ctrl
		if superseded != nil && superseded != ctrl {
			superseded.Close()
		}
		if oldCtrl != nil && oldCtrl != ctrl && oldCtrl != superseded {
			oldCtrl.Close()
		}
	}
	tab.model = model
	tab.Label = ctrl.Label()
	tab.StartupErr = ""
	tab.Ready = true
	a.saveTabsLocked()
	a.mu.Unlock()
	a.emitReady(a.ctx, tab.ID)
	ctrl.EnableInteractiveApproval()
	applyTabModeToController(ctrl, tab.mode)
	applyTabRagScopeToController(ctrl, tab.ragScope)
	path := agent.ContinueSessionPath(prevPath, ctrl.SessionDir(), ctrl.Label())
	if len(carried) > 0 {
		carried = withFreshSystemPrompt(carried, systemPromptFrom(ctrl.History()))
		ctrl.Resume(&agent.Session{Messages: carried}, path)
	} else if path != "" {
		ctrl.SetSessionPath(path)
	}
	a.persistTabSessionPath(tab, path)
	return nil
}

func systemPromptFrom(messages []provider.Message) string {
	for _, m := range messages {
		if m.Role == provider.RoleSystem {
			return provider.ContentString(m.Content)
		}
	}
	return ""
}

func withFreshSystemPrompt(messages []provider.Message, system string) []provider.Message {
	if strings.TrimSpace(system) == "" {
		return messages
	}
	out := append([]provider.Message(nil), messages...)
	for i := range out {
		if out[i].Role == provider.RoleSystem {
			out[i].Content = system
			out[i].ReasoningContent = ""
			out[i].ReasoningSignature = ""
			out[i].ToolCalls = nil
			out[i].ToolCallID = ""
			out[i].Name = ""
			return out
		}
	}
	return append([]provider.Message{{Role: provider.RoleSystem, Content: system}}, out...)
}

// SetDefaultModel sets the config default and switches the live model to it.
// Tabs parked in the Welcome state are revived afterwards so EVERY composer
// shows the new default, not just the active tab's.
func (a *App) SetDefaultModel(ref string) error {
	tab := a.activeTab()
	if tab == nil {
		return fmt.Errorf("no active tab")
	}
	prev := tab.model
	tab.model = ref
	if err := a.applyConfigChange(func(c *config.Config) error {
		resolved, err := selectableDesktopModelRef(c, ref)
		if err != nil {
			return err
		}
		c.DefaultModel = resolved
		tab.model = resolved
		return nil
	}); err != nil {
		tab.model = prev
		return err
	}
	a.reviveParkedTabs()
	return nil
}

// SetPlannerModel sets (or, with "", clears) the two-model planner.
func (a *App) SetPlannerModel(ref string) error {
	return a.applyConfigChange(func(c *config.Config) error {
		if ref != "" {
			resolved, err := selectableDesktopModelRef(c, ref)
			if err != nil {
				return err
			}
			ref = resolved
		}
		c.Agent.PlannerModel = ref
		return nil
	})
}

// SetSubagentModel sets (or clears) the default model used by subagent entry points.
func (a *App) SetSubagentModel(ref string) error {
	return a.applyConfigChange(func(c *config.Config) error {
		ref = strings.TrimSpace(ref)
		if ref != "" {
			resolved, err := selectableDesktopModelRef(c, ref)
			if err != nil {
				return err
			}
			ref = resolved
		}
		c.Agent.SubagentModel = ref
		return nil
	})
}

func selectableDesktopModelRef(c *config.Config, ref string) (string, error) {
	entry, ok := c.ResolveModel(ref)
	if !ok {
		return "", fmt.Errorf("unknown model %q", ref)
	}
	if !modelProviderAccessAllowed(providerAccessSet(c.Desktop.ProviderAccess), entry.Name) {
		return "", fmt.Errorf("model %q is not available because provider %q is not added", ref, entry.Name)
	}
	// Keyless providers (empty api_key_env — local Ollama / llama.cpp endpoints)
	// need no key; everything else still requires its env to resolve.
	if entry.APIKeyEnv != "" && !entry.Configured() {
		return "", fmt.Errorf("model %q is not available because provider %q has no key", ref, entry.Name)
	}
	return entry.Name + "/" + entry.Model, nil
}

// SetSubagentEffort sets (or clears) the default effort used by subagent entry points.
func (a *App) SetSubagentEffort(level string) error {
	return a.applyConfigChange(func(c *config.Config) error {
		level = strings.TrimSpace(level)
		if level == "" || level == "auto" {
			c.Agent.SubagentEffort = ""
			return nil
		}
		model := strings.TrimSpace(c.Agent.SubagentModel)
		if model == "" {
			model = c.DefaultModel
		}
		entry, ok := c.ResolveModel(model)
		if !ok {
			return fmt.Errorf("unknown subagent model %q", model)
		}
		effort, err := config.NormalizeEffort(entry, level)
		if err != nil {
			return err
		}
		c.Agent.SubagentEffort = effort
		return nil
	})
}

// SetAutoPlan updates the automatic plan-mode gate (off|on).
func (a *App) SetAutoPlan(mode string) error {
	return a.applyConfigChange(func(c *config.Config) error { return c.SetAutoPlan(mode) })
}

func desktopAutoPlanMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "on", "ask":
		return "on"
	default:
		return "off"
	}
}

// officialProviderTemplate returns the provider entries for a curated desktop
// provider kind. FairPeer ships no preset official providers (it is
// provider-agnostic over the public network), so every kind is unknown until a
// future template is registered here. Callers receiving this error should
// surface it to the user as "no official provider templates available".
func officialProviderTemplate(kind string) ([]config.ProviderEntry, string, error) {
	return nil, "", fmt.Errorf("no official provider templates available (requested %q)", kind)
}

func chatProviderModels(models []string) []string {
	out := make([]string, 0, len(models))
	seen := map[string]bool{}
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] || !config.IsLikelyChatModel(model) {
			continue
		}
		seen[model] = true
		out = append(out, model)
	}
	return out
}

func providerDefaultForModels(currentDefault string, models []string) string {
	currentDefault = strings.TrimSpace(currentDefault)
	if currentDefault != "" {
		for _, model := range models {
			if model == currentDefault {
				return currentDefault
			}
		}
	}
	if len(models) > 0 {
		return models[0]
	}
	return ""
}

// SaveProvider adds or updates a provider. A single model fills `model`; several
// fill `models` (with `default`). The shared key/endpoint live on the entry.
func (a *App) SaveProvider(p ProviderView) error {
	return a.applyConfigChange(func(c *config.Config) error {
		e := config.ProviderEntry{Name: p.Name}
		for i := range c.Providers {
			if c.Providers[i].Name == p.Name {
				e = c.Providers[i]
				break
			}
		}
		e.Name = p.Name
		e.Kind = p.Kind
		e.BaseURL = p.BaseURL
		e.ModelsURL = p.ModelsURL
		e.APIKeyEnv = p.APIKeyEnv
		e.ContextWindow = p.ContextWindow
		e.ReasoningProtocol = p.ReasoningProtocol
		e.SupportedEfforts = p.SupportedEfforts
		e.DefaultEffort = p.DefaultEffort
		e.Model = ""
		e.Models = nil
		e.Default = ""
		models := chatProviderModels(p.Models)
		if len(models) > 0 {
			e.Model = models[0] // also satisfies validateProvider's model requirement
			if len(models) > 1 {
				e.Models = models
				e.Default = providerDefaultForModels(p.Default, models)
			}
		}
		if err := c.UpsertProvider(e); err != nil {
			return err
		}
		addProviderAccess(c, p.Name)
		return nil
	})
}

// AddOfficialProviderAccess adds one curated desktop provider template to the
// Settings > Model > Access list. The runtime default providers still exist
// independently; this only records the user's explicit access setup.
func (a *App) AddOfficialProviderAccess(kind, key string) error {
	entries, keyEnv, err := officialProviderTemplate(kind)
	if err != nil {
		return err
	}
	if strings.TrimSpace(key) != "" && keyEnv != "" {
		if err := upsertCredential(keyEnv, key); err != nil {
			return err
		}
	}
	return a.applyConfigChange(func(c *config.Config) error {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if err := c.UpsertProvider(e); err != nil {
				return err
			}
			names = append(names, e.Name)
		}
		addProviderAccess(c, names...)
		return nil
	})
}

// FetchProviderModels probes the provider's OpenAI-compatible model-list
// endpoint and returns the available model IDs. This is a settings-only helper:
// it never touches chat request serialization or provider-visible prompt data.
func (a *App) FetchProviderModels(p ProviderView) ([]string, error) {
	e := config.ProviderEntry{
		Name:      p.Name,
		BaseURL:   p.BaseURL,
		ModelsURL: p.ModelsURL,
		APIKeyEnv: p.APIKeyEnv,
	}
	ctx, cancel := context.WithTimeout(a.reqCtx(), 15*time.Second)
	defer cancel()
	models, err := e.FetchModels(ctx)
	if err != nil {
		return []string{}, err
	}
	return nonNil(chatProviderModels(models)), nil
}

// DeleteProvider removes a provider and retargets open idle tabs that used it.
func (a *App) DeleteProvider(name string) error {
	return a.deleteProviderAndRetargetTabs(name)
}

// RemoveProviderAccess hides a provider from Settings > Model > Access and from
// settings model pickers. Built-in provider entries remain in the runtime config
// for back-compat, but visible defaults and idle tabs are retargeted away from
// the removed access entry when another accessed provider is available. Custom
// providers are deleted outright.
func (a *App) RemoveProviderAccess(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("remove provider access: empty provider name")
	}
	cfg, _, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		return err
	}
	if p, ok := cfg.Provider(name); ok && isOfficialBuiltInProvider(*p) {
		return a.removeBuiltInProviderAccessAndRetargetTabs(name)
	}
	return a.deleteProviderAndRetargetTabs(name)
}

type providerRemovalTab struct {
	id   string
	ctrl tabSession
}

func providerAccessFallbackRef(c *config.Config, name string) string {
	name = strings.TrimSpace(name)
	for _, candidate := range c.Desktop.ProviderAccess {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || candidate == name {
			continue
		}
		p, ok := c.Provider(candidate)
		if !ok || len(p.ModelList()) == 0 {
			continue
		}
		return p.Name + "/" + p.DefaultModel()
	}
	return ""
}

func retargetProviderReferences(c *config.Config, name, fallbackRef string) {
	if strings.TrimSpace(fallbackRef) == "" {
		return
	}
	if desktopModelRefsProvider(c, c.DefaultModel, name) {
		c.DefaultModel = fallbackRef
	}
	if desktopModelRefsProvider(c, c.Agent.PlannerModel, name) {
		c.Agent.PlannerModel = fallbackRef
	}
	if desktopModelRefsProvider(c, c.Agent.SubagentModel, name) {
		c.Agent.SubagentModel = fallbackRef
	}
	for skill, ref := range c.Agent.SubagentModels {
		if desktopModelRefsProvider(c, ref, name) {
			c.Agent.SubagentModels[skill] = fallbackRef
		}
	}
}

func (a *App) removeBuiltInProviderAccessAndRetargetTabs(name string) error {
	cfg, path, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		return err
	}
	fallbackRef := providerAccessFallbackRef(cfg, name)

	var affected []providerRemovalTab
	if fallbackRef != "" {
		a.mu.RLock()
		for _, id := range a.orderedTabIDsLocked() {
			tab := a.tabs[id]
			if tab == nil {
				continue
			}
			ref := tab.model
			if strings.TrimSpace(ref) == "" {
				ref = cfg.DefaultModel
			}
			if !desktopModelRefsProvider(cfg, ref, name) {
				continue
			}
			if tab.Ctrl != nil && tab.Ctrl.Running() {
				a.mu.RUnlock()
				return fmt.Errorf("finish or cancel conversations using %q before removing the provider access", name)
			}
			affected = append(affected, providerRemovalTab{id: id, ctrl: tab.Ctrl})
		}
		a.mu.RUnlock()
	}

	retargetProviderReferences(cfg, name, fallbackRef)
	removeProviderAccess(cfg, name)
	if err := cfg.SaveTo(path); err != nil {
		return err
	}
	if len(affected) == 0 {
		return a.rebuild()
	}
	for _, item := range affected {
		if item.ctrl != nil {
			_ = item.ctrl.Snapshot()
			item.ctrl.Close()
		}
	}

	var rebuildTabs []*WorkspaceTab
	a.mu.Lock()
	for _, item := range affected {
		tab := a.tabs[item.id]
		if tab == nil {
			continue
		}
		tab.Ctrl = nil
		tab.model = fallbackRef
		tab.Label = fallbackRef
		tab.StartupErr = ""
		tab.Ready = a.ctx == nil
		if a.ctx != nil {
			rebuildTabs = append(rebuildTabs, tab)
		}
	}
	a.saveTabsLocked()
	a.mu.Unlock()

	for _, tab := range rebuildTabs {
		go a.buildTabController(tab)
	}
	return nil
}

func (a *App) deleteProviderAndRetargetTabs(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("remove provider: empty provider name")
	}
	cfg, path, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		return err
	}
	fallbackRef := providerRemovalFallbackRef(cfg, name)

	var affected []providerRemovalTab
	a.mu.RLock()
	for _, id := range a.orderedTabIDsLocked() {
		tab := a.tabs[id]
		if tab == nil {
			continue
		}
		ref := tab.model
		if strings.TrimSpace(ref) == "" {
			ref = cfg.DefaultModel
		}
		if !desktopModelRefsProvider(cfg, ref, name) {
			continue
		}
		if tab.Ctrl != nil && tab.Ctrl.Running() {
			a.mu.RUnlock()
			return fmt.Errorf("finish or cancel conversations using %q before deleting the provider", name)
		}
		affected = append(affected, providerRemovalTab{id: id, ctrl: tab.Ctrl})
	}
	a.mu.RUnlock()

	if len(affected) > 0 && fallbackRef == "" {
		return fmt.Errorf("remove provider: %q is used by open tabs and no other configured provider exists", name)
	}
	if err := cfg.RemoveProvider(name); err != nil {
		return err
	}
	removeProviderAccess(cfg, name)
	if err := cfg.SaveTo(path); err != nil {
		return err
	}

	if len(affected) == 0 {
		return a.rebuild()
	}
	for _, item := range affected {
		if item.ctrl != nil {
			_ = item.ctrl.Snapshot()
			item.ctrl.Close()
		}
	}

	var rebuildTabs []*WorkspaceTab
	a.mu.Lock()
	for _, item := range affected {
		tab := a.tabs[item.id]
		if tab == nil {
			continue
		}
		tab.Ctrl = nil
		tab.model = fallbackRef
		tab.Label = fallbackRef
		tab.StartupErr = ""
		tab.Ready = a.ctx == nil
		if a.ctx != nil {
			rebuildTabs = append(rebuildTabs, tab)
		}
	}
	a.saveTabsLocked()
	a.mu.Unlock()

	for _, tab := range rebuildTabs {
		go a.buildTabController(tab)
	}
	return nil
}

// SetProviderKey writes a secret to the encrypted secret store under the given
// env-var name (the one a provider's api_key_env points at) and rebuilds so it
// resolves immediately.
func (a *App) SetProviderKey(apiKeyEnv, value string) error {
	if strings.TrimSpace(apiKeyEnv) == "" {
		return fmt.Errorf("this provider has no api_key_env set")
	}
	if err := upsertCredential(apiKeyEnv, value); err != nil {
		return err
	}
	return a.rebuild()
}

// ClearProviderKey removes a provider secret from the encrypted secret store
// and rebuilds so the provider immediately becomes unauthenticated.
func (a *App) ClearProviderKey(apiKeyEnv string) error {
	if strings.TrimSpace(apiKeyEnv) == "" {
		return fmt.Errorf("this provider has no api_key_env set")
	}
	if err := removeCredential(apiKeyEnv); err != nil {
		return err
	}
	return a.rebuild()
}

// SetPermissionMode sets the writer-fallback mode (ask|allow|deny).
func (a *App) SetPermissionMode(mode string) error {
	return a.applyConfigChange(func(c *config.Config) error { return c.SetPermissionMode(mode) })
}

// AddPermissionRule appends a rule to the allow/ask/deny list.
func (a *App) AddPermissionRule(list, rule string) error {
	return a.applyConfigChange(func(c *config.Config) error { return c.AddPermissionRule(list, rule) })
}

// RemovePermissionRule drops a rule from the allow/ask/deny list.
func (a *App) RemovePermissionRule(list, rule string) error {
	return a.applyConfigChange(func(c *config.Config) error {
		_, err := c.RemovePermissionRule(list, rule)
		return err
	})
}

// SetSandbox updates the bash sandbox mode, network egress, and write roots.
func (a *App) SetSandbox(bash string, network bool, workspaceRoot string, allowWrite []string) error {
	return a.applyConfigChange(func(c *config.Config) error {
		c.Sandbox.Bash = bash
		c.Sandbox.Network = network
		c.Sandbox.WorkspaceRoot = strings.TrimSpace(workspaceRoot)
		c.Sandbox.AllowWrite = trimList(allowWrite)
		return nil
	})
}

// SetNetwork updates ordinary outbound proxy settings.
func (a *App) SetNetwork(n NetworkView) error {
	return a.applyConfigChange(func(c *config.Config) error {
		return c.SetNetwork(config.NetworkConfig{
			ProxyMode: n.ProxyMode,
			ProxyURL:  n.ProxyURL,
			NoProxy:   n.NoProxy,
			Proxy: config.NetworkProxyConfig{
				Type:     n.Proxy.Type,
				Server:   n.Proxy.Server,
				Port:     n.Proxy.Port,
				Username: n.Proxy.Username,
				Password: n.Proxy.Password,
			},
		})
	})
}

func (a *App) SetBotSettings(b BotSettingsView) error {
	err := a.applyConfigOnly(func(c *config.Config) error {
		c.Bot.Enabled = b.Enabled
		c.Bot.Model = strings.TrimSpace(b.Model)
		c.Bot.MaxSteps = b.MaxSteps
		c.Bot.DebounceMs = b.DebounceMs
		c.Bot.Allowlist = config.BotAllowlist{
			Enabled:      b.Allowlist.Enabled,
			AllowAll:     b.Allowlist.AllowAll,
			Mode:         allowlistModeOrDefault(b.Allowlist.Mode),
			QQUsers:      trimList(b.Allowlist.QQUsers),
			FeishuUsers:  trimList(b.Allowlist.FeishuUsers),
			WeixinUsers:  trimList(b.Allowlist.WeixinUsers),
			QQGroups:     trimList(b.Allowlist.QQGroups),
			FeishuGroups: trimList(b.Allowlist.FeishuGroups),
			WeixinGroups: trimList(b.Allowlist.WeixinGroups),
		}
		c.Bot.QQ = config.QQBotConfig{
			Enabled:      b.QQ.Enabled,
			AppID:        strings.TrimSpace(b.QQ.AppID),
			AppSecretEnv: strings.TrimSpace(b.QQ.AppSecretEnv),
		}
		c.Bot.Feishu = config.FeishuBotConfig{
			Enabled:           b.Feishu.Enabled,
			Domain:            botDomainOrDefault(b.Feishu.Domain),
			AppID:             strings.TrimSpace(b.Feishu.AppID),
			AppSecretEnv:      strings.TrimSpace(b.Feishu.AppSecretEnv),
			VerificationToken: strings.TrimSpace(b.Feishu.VerificationToken),
			Mode:              strings.TrimSpace(b.Feishu.Mode),
			WebhookPort:       b.Feishu.WebhookPort,
			RequireMention:    b.Feishu.RequireMention,
		}
		c.Bot.Weixin = config.WeixinBotConfig{
			Enabled:   b.Weixin.Enabled,
			AccountID: strings.TrimSpace(b.Weixin.AccountID),
			TokenEnv:  strings.TrimSpace(b.Weixin.TokenEnv),
			APIBase:   strings.TrimRight(strings.TrimSpace(b.Weixin.APIBase), "/"),
		}
		c.Bot.Connections = botConnectionConfigs(b.Connections)
		return nil
	})
	if err != nil {
		return err
	}
	// 热重启 gateway
	if b.Enabled {
		a.restartBotGateway()
	} else {
		a.stopBotGateway()
	}
	return nil
}

func (a *App) SetBotSecret(envName, value string) error {
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return fmt.Errorf("bot secret env name is empty")
	}
	if err := upsertCredential(envName, value); err != nil {
		return err
	}
	return nil
}

func (a *App) ClearBotSecret(envName string) error {
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return fmt.Errorf("bot secret env name is empty")
	}
	return removeCredential(envName)
}

// SetCloseBehavior updates desktop-only window close behavior without rebuilding
// the active controller. It must stay out of provider-visible prompt/request data.
func (a *App) SetCloseBehavior(mode string) error {
	return a.applyConfigOnly(func(c *config.Config) error { return c.SetDesktopCloseBehavior(mode) })
}

// SetDisplayMode updates the transcript display mode. UI-only, no rebuild needed.
func (a *App) SetDisplayMode(mode string) error {
	return a.applyConfigOnly(func(c *config.Config) error { return c.SetDesktopDisplayMode(mode) })
}

// SetDesktopLanguage updates only the desktop UI language. It deliberately does
// not touch config.language, which the CLI/model-facing runtime uses.
func (a *App) SetDesktopLanguage(lang string) error {
	if err := a.applyConfigOnly(func(c *config.Config) error { return c.SetDesktopLanguage(lang) }); err != nil {
		return err
	}
	a.updateTrayLocale(lang)
	return nil
}

// SetTrayLocale mirrors the resolved desktop UI language into the native tray
// menu. It is runtime-only; the persisted preference remains [desktop].language.
func (a *App) SetTrayLocale(locale string) error {
	if locale != "zh" {
		locale = "en"
	}
	a.updateTrayLocale(locale)
	return nil
}

// SetDesktopAppearance updates only desktop theme preferences. It does not
// rebuild the active controller and must stay out of provider-visible requests.
func (a *App) SetDesktopAppearance(theme, style string) error {
	return a.applyConfigOnly(func(c *config.Config) error { return c.SetDesktopAppearance(theme, style) })
}

// SetDesktopCheckUpdates updates only the desktop startup update-check
// preference. Manual checks in Settings are unaffected.
func (a *App) SetDesktopCheckUpdates(enabled bool) error {
	return a.applyConfigOnly(func(c *config.Config) error { return c.SetDesktopCheckUpdates(enabled) })
}

// SetDesktopTelemetry sets whether the desktop sends the anonymous launch ping.
func (a *App) SetDesktopTelemetry(enabled bool) error {
	return a.applyConfigOnly(func(c *config.Config) error { return c.SetDesktopTelemetry(enabled) })
}

// SetDesktopMetrics sets whether the desktop sends opt-in aggregate agent metrics,
// starting or stopping the live aggregator so the toggle takes effect immediately.
func (a *App) SetDesktopMetrics(enabled bool) error {
	if err := a.applyConfigOnly(func(c *config.Config) error { return c.SetDesktopMetrics(enabled) }); err != nil {
		return err
	}
	switch {
	case enabled && a.metrics.Load() == nil && version != "dev":
		a.metrics.Store(newMetricsAggregator(filepath.Dir(config.UserConfigPath())))
	case !enabled:
		a.metrics.Store(nil)
	}
	return nil
}

// SetExpandThinking sets whether reasoning text is expanded by default on
// the desktop. It is desktop-only and does not rebuild the controller.
func (a *App) SetExpandThinking(on bool) error {
	return a.applyConfigOnly(func(c *config.Config) error { return c.SetExpandThinking(on) })
}

// MigrateDesktopPreferences imports old browser-local desktop preferences into
// the user config once. Existing [desktop] values win so stale localStorage never
// overwrites an explicit config edit.
func (a *App) MigrateDesktopPreferences(language, theme, style string) error {
	return a.applyConfigOnly(func(c *config.Config) error {
		if strings.TrimSpace(c.Desktop.Language) == "" {
			if err := c.SetDesktopLanguage(language); err != nil {
				return err
			}
		}
		if strings.TrimSpace(c.Desktop.Theme) == "" && strings.TrimSpace(c.Desktop.ThemeStyle) == "" {
			if err := c.SetDesktopAppearance(theme, style); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetAgentParams updates sampling temperature, optional step guards, and the
// base system prompt.
func (a *App) SetAgentParams(temperature float64, maxSteps int, plannerMaxSteps int, systemPrompt string) error {
	return a.applyConfigChange(func(c *config.Config) error {
		c.Agent.Temperature = temperature
		c.Agent.MaxSteps = maxSteps
		c.Agent.PlannerMaxSteps = plannerMaxSteps
		c.Agent.SystemPrompt = systemPrompt
		return nil
	})
}

// SetRPM updates the global LLM requests-per-minute budget. 0 means unlimited.
// The change takes effect immediately via controller rebuild.
func (a *App) SetRPM(rpm int) error {
	return a.applyConfigChange(func(c *config.Config) error {
		c.LLM.RPM = rpm
		return nil
	})
}

// trimList drops blank entries from a string slice (and returns a non-nil slice).
func trimList(in []string) []string {
	out := []string{}
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// --- Skill Marketplace (SPEC v2 §3.4C) ---------------------------------------
// The desktop UI calls these to browse/search the skill catalog and install from
// it, without going through the agent's natural-language flow. They reuse the
// same installSourceTool (SSRF guard, safety scan, manifest) as skill_market.

// SkillMarketBrowse returns a browsable catalog of skills from the builtin
// source (offline, always available). For network sources (Anthropic/OpenAI/
// ClawHub), use SkillMarketSearch which handles network access.
func (a *App) SkillMarketBrowse() ([]installsource.CatalogEntry, error) {
	return installsource.BuiltinCatalog(), nil
}

// SkillMarketSources returns the list of default market sources for the UI.
func (a *App) SkillMarketSources() []installsource.MarketSourceMeta {
	return installsource.DefaultMarketSourceMetas()
}

// SkillMarketSearch searches across all default sources by keyword. Returns
// structured JSON (CatalogEntry[]) so the frontend can render directly without
// fragile text parsing.
func (a *App) SkillMarketSearch(query string, sourceID string) ([]installsource.CatalogEntry, error) {
	if sourceID == "builtin-mcp" {
		all := installsource.BuiltinMcpCatalog()
		var filtered []installsource.CatalogEntry
		q := strings.ToLower(query)
		for _, e := range all {
			if strings.Contains(strings.ToLower(e.Name), q) || strings.Contains(strings.ToLower(e.Description), q) {
				filtered = append(filtered, e)
			}
		}
		return filtered, nil
	}

	if a.ctx == nil {
		return nil, fmt.Errorf("app not initialized")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	// Use the market tool which internally calls SearchCatalog and returns text.
	// The frontend parses the text — this is the existing contract.
	mt := installsource.NewMarketToolFromOptions(installsource.Options{
		ProjectRoot: home,
		HomeDir:     home,
	})
	raw, _ := json.Marshal(map[string]any{"action": "search", "query": query, "source": sourceID})
	out, err := mt.Execute(ctx, raw)
	if err != nil {
		return nil, err
	}
	// Parse the text output back into CatalogEntry for structured JSON return.
	entries := parseSearchText(out)
	return entries, nil
}

// parseSearchText parses the formatted search output into CatalogEntry structs.
// Each line: "- **name** [source] — desc (N installs) `installRef: url`"
func parseSearchText(text string) []installsource.CatalogEntry {
	var entries []installsource.CatalogEntry
	for _, line := range strings.Split(text, "\n") {
		// Match: - **name** [source] — desc (N installs) `installRef: url`
		// or:    - **name** [source] — desc `installRef: url`
		parts := strings.SplitN(line, "`installRef: ", 2)
		if len(parts) != 2 {
			continue
		}
		installRef := strings.TrimSuffix(strings.TrimSpace(parts[1]), "`")
		// Parse the prefix: "- **name** [source] — desc"
		prefix := parts[0]
		if !strings.HasPrefix(prefix, "- **") {
			continue
		}
		rest := strings.TrimPrefix(prefix, "- **")
		idxName := strings.Index(rest, "**")
		if idxName < 0 {
			continue
		}
		name := rest[:idxName]
		rest = rest[idxName+2:] // after **
		// Extract [source]
		if !strings.HasPrefix(strings.TrimSpace(rest), "[") {
			continue
		}
		rest = strings.TrimSpace(rest)
		idxSourceEnd := strings.Index(rest, "]")
		if idxSourceEnd < 0 {
			continue
		}
		source := rest[1:idxSourceEnd]
		rest = rest[idxSourceEnd+1:] // after ]
		// Extract desc (after " — ")
		desc := strings.TrimSpace(strings.TrimPrefix(rest, " — "))
		// Extract installs if present
		installs := 0
		if idxP := strings.LastIndex(desc, " ("); idxP >= 0 && strings.HasSuffix(desc, " installs)") {
			numStr := desc[idxP+2 : len(desc)-len(" installs)")]
			if n, err := strconv.Atoi(numStr); err == nil {
				installs = n
			}
			desc = desc[:idxP]
		}
		entries = append(entries, installsource.CatalogEntry{
			Source:      source,
			Name:        name,
			Description: desc,
			Installs:    installs,
			ContentURL:  installRef,
			InstallRef:  installRef,
		})
	}
	return entries
}

// SkillMarketInstall installs a skill from the marketplace by its installRef
// (obtained from browse/search results). Goes through the same plan→apply
// pipeline as install_source (safety scan + manifest). Returns the install
// result summary. When apply=false, returns the plan for review.
func (a *App) SkillMarketInstall(installRef, name, scope string, apply bool) (string, error) {
	if a.ctx == nil {
		return "", fmt.Errorf("app not initialized")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()
	tl := installsource.NewMarketToolFromOptions(installsource.Options{
		ProjectRoot: home,
		HomeDir:     home,
	})
	args := map[string]any{
		"action":     "install",
		"installRef": installRef,
		"apply":      apply,
	}
	if name != "" {
		args["name"] = name
	}
	if scope != "" {
		args["scope"] = scope
	}
	raw, _ := json.Marshal(args)
	result, err := tl.Execute(ctx, raw)
	if err != nil {
		return result, err
	}
	// Refresh the skill store so the new skill is immediately available.
	if apply {
		_ = a.RefreshSkills()
	}
	// Append install path to the result for user feedback.
	if name != "" {
		result += "\n" + filepath.Join(home, ".fairpeer", "skills", name)
	}
	return result, nil
}

// SkillMarketUninstall removes a previously installed skill by name.
func (a *App) SkillMarketUninstall(name string, scope string) (string, error) {
	if a.ctx == nil {
		return "", fmt.Errorf("app not initialized")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	if scope == "" {
		scope = "global"
	}
	// Use install_source op=uninstall.
	tl := installsource.NewTool(installsource.Options{ProjectRoot: home, HomeDir: home})
	args := map[string]any{
		"op":    "uninstall",
		"kind":  "skill",
		"name":  name,
		"scope": scope,
		"apply": true,
	}
	raw, _ := json.Marshal(args)
	result, err := tl.Execute(ctx, raw)
	if err != nil {
		return result, err
	}
	_ = a.RefreshSkills()
	return result, nil
}

// SkillMarketInstalledNames returns the set of skill names installed via
// the marketplace, with their source URLs. Used by the frontend to mark
// search results as "already installed".
func (a *App) SkillMarketInstalledNames() (map[string]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}
	return installsource.InstalledSkillNames(home), nil
}
