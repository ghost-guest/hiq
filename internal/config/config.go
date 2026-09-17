// Package config loads fairpeer's runtime configuration from TOML. Resolution order:
// flag > project ./fairpeer.toml > user ~/.config/fairpeer/config.toml > built-in defaults.
// Secrets come from the environment via api_key_env and are never stored in
// config files.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/zzycxz/fairpeer/internal/netclient"
	"github.com/zzycxz/fairpeer/internal/provider"
)

var validSkillName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// IsValidSkillName reports whether name is a usable skill identifier.
func IsValidSkillName(name string) bool { return validSkillName.MatchString(name) }

// SkillNameKey normalizes a skill identifier for config comparisons.
func SkillNameKey(name string) string {
	name = strings.TrimSpace(name)
	if !IsValidSkillName(name) {
		return ""
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(name)
	}
	return name
}

// Config is fairpeer's runtime configuration.
type Config struct {
	ConfigVersion int    `toml:"config_version"`
	DefaultModel  string `toml:"default_model"`
	Language      string `toml:"language"` // ui/model language tag (e.g. "zh"); empty = auto-detect from $LANG / $FAIRPEER_LANG
	// ReasoningLanguage steers ONLY the visible thinking/reasoning text language
	// (auto|zh|en), independent of the final-answer language. Default "auto" leaves
	// it to the provider. It is injected as a transient per-turn block, never into
	// the cache-stable system prompt prefix.
	ReasoningLanguage string              `json:"-" toml:"reasoning_language"` // auto|zh|en; empty = auto
	UI                UIConfig            `toml:"ui"`
	Desktop           DesktopConfig       `toml:"desktop"`
	Notifications     NotificationsConfig `toml:"notifications"`
	Agent             AgentConfig         `toml:"agent"`
	Providers         []ProviderEntry     `toml:"providers"`
	Tools             ToolsConfig         `toml:"tools"`
	Permissions       PermissionsConfig   `toml:"permissions"`
	Sandbox           SandboxConfig       `toml:"sandbox"`
	Network           NetworkConfig       `toml:"network"`
	Plugins           []PluginEntry       `toml:"plugins"`
	// NetDev ([netdev]) is pinned to the USER config after the project merge
	// (pinNetDev in LoadForRoot): a cloned repo must never inject devices, hop
	// chains, or scan scopes. See internal/config/netdev.go and NETDEV_SPEC §7.3.
	NetDev    NetDevConfig    `toml:"netdev"`
	Skills    SkillsConfig    `toml:"skills"`
	Codegraph CodegraphConfig `toml:"codegraph"`
	Dream     DreamConfig     `toml:"dream"`
	// Memory ([memory]) is a USER-GLOBAL control: where the memory data tree
	// lives and which model maintains it. Pinned to the user config by
	// LoadForRoot (pinMemory) so a project file cannot redirect it. See
	// internal/config/memory.go.
	Memory     MemoryConfig     `toml:"memory"`
	Statusline StatuslineConfig `toml:"statusline"`
	LSP        LSPConfig        `toml:"lsp"`
	Bot        BotConfig        `toml:"bot"`
	// Deferred ([deferred]) controls pushing finished background-task results
	// back into the session: whether a result may start its own turn, and which
	// results raise an immediate notice. See DeferredConfig.
	Deferred DeferredConfig `toml:"deferred"`
	// Cowork holds coWork (office) profile settings — currently just the browser
	// path override. Empty means auto-detect; a non-empty path is tried first
	// (and the user is guided to set it when no browser is found).
	Cowork CoworkConfig `toml:"cowork"`
	// LLM holds the global request budget (rate limiting) applied to all
	// providers. RPM=0 (the default) disables limiting for backward compat.
	LLM LLMConfig `toml:"llm"`
	// Profiles holds optional [[profiles]] entries that override the built-in
	// dev/cowork profiles by name. A name collision with a builtin replaces it,
	// so users can customise a profile's model/prompt/skills without code. Empty
	// means only the builtins are available (dev + cowork).
	Profiles []Profile `toml:"profiles"`
	// MobileBridge configures the linkpeer mobile companion bridge (desktop ↔
	// phone P2P). Empty means mobilebridge uses its built-in defaults; the
	// LINKPEER_SIGNAL env var still overrides signal_url for ad-hoc dev.
	MobileBridge MobileBridgeConfig `toml:"mobilebridge"`
	// TrustDomain ([trustdomain]) is the private-network trust domain
	// ledger (docs/TRUSTDOMAIN_SPEC.md): cross-profile infrastructure,
	// off by default. Independent of agent profiles and providers.
	TrustDomain TrustDomainConfig `toml:"trustdomain"`

	// injectedLocalPresets records which keyless local presets (Ollama,
	// llama.cpp) load injected because no config file defines [[providers]].
	// Injected-but-not-added entries are "ambient" (see AmbientLocalPreset):
	// visible in Settings, but never an implicit model choice. Unexported on
	// purpose — it is load-time state, never decoded from or rendered to TOML,
	// and a config whose file defines [[providers]] simply has none.
	injectedLocalPresets map[string]bool
}

// MobileBridgeConfig is the user-facing [mobilebridge] section. Only the
// fields a user realistically edits live here; the rest of
// mobilebridge.Config comes from DefaultConfig(). signal_url is what makes
// the bridge actually connect — set it to your linkpeer-signal K base URL,
// e.g. signal_url = "http://192.168.1.48:8080".
type MobileBridgeConfig struct {
	SignalURL   string   `toml:"signal_url"`   // linkpeer-signal K base URL; empty = DefaultConfig placeholder
	STUNServers []string `toml:"stun_servers"` // extra STUN servers for cross-network P2P (M3)
	LogLevel    string   `toml:"log_level"`    // trace|debug|info|warn|error; empty = info
	AutoConfirm bool     `toml:"auto_confirm"` // 联调：收到 exchange 自动确认，不等用户点允许
	// PairAddress 钉死配对二维码使用哪块网卡的 IP（多网卡环境用户手选）。
	// 空 = 自动：默认路由出口优先 + 全部真实网卡作为多候选（手机端自动匹配）。
	PairAddress string `toml:"pair_address"`
	// UDPKnock 单包敲门（M3 NAT 穿透辅助，默认关）：S 从 ICE 同一 UDP
	// socket 向 C 的公网映射（srflx，经 KnockServer 探得）发敲门包，
	// 提前打开 S 侧 NAT。双对称 NAT 无效（协议 §7）。
	UDPKnock    bool   `toml:"udp_knock"`
	KnockServer string `toml:"knock_server"` // 敲门依赖的远程 STUN，如 stun:host:3478
	// CloudSignalURL 公网跳板 K（跨网配对/信令候选）：非空时 S 维持第二条
	// 出站 WSS 长连到该云 K，二维码 relay 追加它为末位候选——手机同网自动
	// 选局域网直连，跨网回退到云 K 打洞。空 = 关（纯局域网/单 K，零云）。
	CloudSignalURL string `toml:"cloud_signal_url"`
	// TURN 中转兜底（跨网打洞全败时经 coturn 中继；ICE 仍优先直连）。
	// 凭据为 coturn use-auth-secret（REST）模式：user=时间戳，pass=HMAC。
	TURNEnabled bool     `toml:"turn_enabled"`
	TURNServers []string `toml:"turn_servers"` // 如 ["turn:signal.example.com:3478?transport=udp"]
	TURNUser    string   `toml:"turn_user"`
	TURNPass    string   `toml:"turn_pass"`
}

// UIConfig controls CLI presentation-only settings. Desktop appearance is kept in
// DesktopConfig so desktop preferences cannot alter terminal output or prompts.
type UIConfig struct {
	Theme          string `toml:"theme"`           // auto|dark|light; empty resolves to auto
	ThemeStyle     string `toml:"theme_style"`     // graphite|aurora|slate|carbon|nocturne|amber and legacy aliases
	ShortcutLayout string `toml:"shortcut_layout"` // classic|desktop; accepted for compatibility
	CloseBehavior  string `toml:"close_behavior"`  // legacy desktop close behavior; prefer desktop.close_behavior
	ShowReasoning  bool   `toml:"show_reasoning"`  // Ctrl+O / /verbose: show thinking text in CLI; false = collapsed
}

// DesktopConfig controls desktop-only UI preferences. It is intentionally
// separate from top-level language and [ui] so desktop choices do not affect CLI
// language, terminal colours, or provider-visible prompt/request data.
type DesktopConfig struct {
	Language       string   `toml:"language"`        // auto|en|zh; empty/auto = browser/OS auto-detect
	Theme          string   `toml:"theme"`           // auto|dark|light; empty resolves to dark
	ThemeStyle     string   `toml:"theme_style"`     // graphite|aurora|slate|carbon|nocturne|amber and legacy aliases
	CloseBehavior  string   `toml:"close_behavior"`  // quit|background; desktop window close behavior
	DisplayMode    string   `toml:"display_mode"`    // standard|compact|minimal; transcript display mode
	CheckUpdates   *bool    `toml:"check_updates"`   // startup update checks; nil keeps the default enabled
	Telemetry      *bool    `toml:"telemetry"`       // anonymous launch ping (install id + version + OS); nil keeps the default enabled
	Metrics        *bool    `toml:"metrics"`         // opt-in aggregate agent metrics (anonymous signal/bucket counts; no content); nil = disabled
	ProviderAccess []string `toml:"provider_access"` // desktop-only list of provider entries shown in Settings > Model > Access
	ExpandThinking bool     `toml:"expand_thinking"` // true = show reasoning text expanded by default; false = collapsed

	// Wallpaper controls the optional custom background image. Like the other
	// desktop fields it is UI-only: it never affects prompts, requests, or CLI
	// behaviour. The image itself lives in the desktop wallpaper directory and
	// is served to the webview over a local asset route.
	WallpaperPath string `toml:"wallpaper_path"` // absolute path of the active image; empty = no wallpaper
	WallpaperBlur *int   `toml:"wallpaper_blur"` // 0-40 px gaussian blur on the wallpaper layer; nil keeps the default
	WallpaperDim  *int   `toml:"wallpaper_dim"`  // 0-85 percent theme scrim over the wallpaper; readability guard
	WallpaperFit  string `toml:"wallpaper_fit"`  // cover|contain|tile
}

// NotificationsConfig controls optional system notifications for CLI chat/run.
type NotificationsConfig struct {
	Enabled         bool `toml:"enabled"`
	TurnDone        bool `toml:"turn_done"`
	ApprovalRequest bool `toml:"approval_request"`
	AskRequest      bool `toml:"ask_request"`
}

// DeferredConfig controls pushing finished background-task results into the
// session instead of waiting for the next turn to drain a one-line summary.
//
// The defaults are deliberately conservative: results are always carried into
// the next turn's context, the user is told immediately about failures, and no
// turn is ever started without the user opting in (each auto-started turn costs
// model tokens).
type DeferredConfig struct {
	// Enabled turns push delivery on. nil = on.
	Enabled *bool `toml:"enabled"`
	// TriggerParentTurn lets a *finished* result start its own turn. nil = off;
	// failures still raise a notice, they just don't spend a turn.
	TriggerParentTurn *bool `toml:"trigger_parent_turn"`
	// NotifyOnFailure raises an immediate notice when a background job fails.
	// nil = on.
	NotifyOnFailure *bool `toml:"notify_on_failure"`
	// NotifyOnSuccess raises an immediate notice when a bash-style job
	// succeeds. nil = off (task results always notify — their answer is the
	// point of running them).
	NotifyOnSuccess *bool `toml:"notify_on_success"`
	// RetryIntervalSec is how often pending results are retried. 0 = 30s.
	RetryIntervalSec int `toml:"retry_interval_sec"`
	// MaxAttempts caps delivery retries before a result is dropped. 0 = 20.
	MaxAttempts int `toml:"max_attempts"`
	// BodyLimitBytes truncates a stored result body. 0 = 4000.
	BodyLimitBytes int `toml:"body_limit_bytes"`
}

// Defaults for [deferred].
const (
	DefaultDeferredRetryIntervalSec = 30
	DefaultDeferredMaxAttempts      = 20
	DefaultDeferredBodyLimitBytes   = 4000
)

func boolOrNil(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

// EnabledEffective reports whether push delivery is on (default: on).
func (d DeferredConfig) EnabledEffective() bool { return boolOrNil(d.Enabled, true) }

// TriggerParentTurnEffective reports whether a finished result may start its
// own turn (default: off).
func (d DeferredConfig) TriggerParentTurnEffective() bool { return boolOrNil(d.TriggerParentTurn, false) }

// NotifyOnFailureEffective reports whether failures notify immediately
// (default: on).
func (d DeferredConfig) NotifyOnFailureEffective() bool { return boolOrNil(d.NotifyOnFailure, true) }

// NotifyOnSuccessEffective reports whether successful bash-style jobs notify
// immediately (default: off).
func (d DeferredConfig) NotifyOnSuccessEffective() bool { return boolOrNil(d.NotifyOnSuccess, false) }

// RetryIntervalSecondsEffective returns the retry cadence in seconds, applying
// the default when unset.
func (d DeferredConfig) RetryIntervalSecondsEffective() int {
	if d.RetryIntervalSec > 0 {
		return d.RetryIntervalSec
	}
	return DefaultDeferredRetryIntervalSec
}

// MaxAttemptsEffective returns the retry cap, applying the default when unset.
func (d DeferredConfig) MaxAttemptsEffective() int {
	if d.MaxAttempts > 0 {
		return d.MaxAttempts
	}
	return DefaultDeferredMaxAttempts
}

// BodyLimitBytesEffective returns the stored-body cap, applying the default
// when unset.
func (d DeferredConfig) BodyLimitBytesEffective() int {
	if d.BodyLimitBytes > 0 {
		return d.BodyLimitBytes
	}
	return DefaultDeferredBodyLimitBytes
}

// UITheme normalizes ui.theme (dark/light/auto). Empty or unrecognized falls
// back to "auto", which follows the OS shell preference.
func (c *Config) UITheme() string {
	switch strings.ToLower(strings.TrimSpace(c.UI.Theme)) {
	case "dark":
		return "dark"
	case "light":
		return "light"
	default:
		return "auto"
	}
}

// UIThemeStyle normalizes ui.theme_style. Empty means "pick the default style
// for the resolved light/dark shell".
func (c *Config) UIThemeStyle() string {
	return normalizeThemeStyle(c.UI.ThemeStyle)
}

// UIShortcutLayout normalizes the legacy CLI shortcut layout setting. It is kept
// for compatibility; Shift+Tab toggles Plan and Ctrl+Y toggles YOLO in both
// layouts.
func (c *Config) UIShortcutLayout() string {
	switch strings.ToLower(strings.TrimSpace(c.UI.ShortcutLayout)) {
	case "desktop", "dual", "dual-axis", "dual_axis":
		return "desktop"
	default:
		return "classic"
	}
}

func normalizeThemeStyle(style string) string {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "graphite", "aurora", "slate", "carbon", "nocturne", "amber", "ember", "midnight", "sandstone", "porcelain", "linen", "glacier":
		return strings.ToLower(strings.TrimSpace(style))
	default:
		return ""
	}
}

func normalizeCloseBehavior(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "quit", "exit":
		return "quit"
	default:
		return "background"
	}
}

// DesktopLanguage normalizes the desktop UI language. Empty means auto-detect
// from the browser/OS locale; it deliberately does not read top-level language,
// which is used by the CLI/model-facing runtime.
func (c *Config) DesktopLanguage() string {
	switch strings.ToLower(strings.TrimSpace(c.Desktop.Language)) {
	case "en":
		return "en"
	case "zh":
		return "zh"
	default:
		return ""
	}
}

// DesktopTheme normalizes desktop.theme. New desktop users default to the light
// graphite product look; an explicit auto/light/dark is preserved.
func (c *Config) DesktopTheme() string {
	switch strings.ToLower(strings.TrimSpace(c.Desktop.Theme)) {
	case "auto":
		return "auto"
	case "light":
		return "light"
	case "dark":
		return "dark"
	default:
		return "light"
	}
}

// DesktopThemeStyle normalizes desktop.theme_style. Empty means the frontend
// chooses the default style for the resolved desktop theme.
func (c *Config) DesktopThemeStyle() string {
	return normalizeThemeStyle(c.Desktop.ThemeStyle)
}

// DesktopCloseBehavior normalizes the desktop close-window preference. It falls
// back to the legacy ui.close_behavior value for configs written before [desktop]
// existed.
func (c *Config) DesktopCloseBehavior() string {
	if strings.TrimSpace(c.Desktop.CloseBehavior) != "" {
		return normalizeCloseBehavior(c.Desktop.CloseBehavior)
	}
	return normalizeCloseBehavior(c.UI.CloseBehavior)
}

// UICloseBehavior is the legacy name for DesktopCloseBehavior.
func (c *Config) UICloseBehavior() string {
	return c.DesktopCloseBehavior()
}

// DesktopDisplayMode normalizes the transcript display mode. Default is
// "minimal" (collapsed model-generated intermediate items).
func (c *Config) DesktopDisplayMode() string {
	switch strings.ToLower(strings.TrimSpace(c.Desktop.DisplayMode)) {
	case "standard":
		return "standard"
	case "compact":
		return "compact"
	case "minimal":
		return "minimal"
	default:
		return "minimal"
	}
}

// DesktopCheckUpdates reports whether the desktop should check for updates on
// startup. Missing configs default to true so existing users keep update notices.
func (c *Config) DesktopCheckUpdates() bool {
	if c == nil || c.Desktop.CheckUpdates == nil {
		return true
	}
	return *c.Desktop.CheckUpdates
}

// DesktopTelemetry reports whether the desktop sends the anonymous launch ping.
// It carries no conversation, key, or file data — see desktop/README.md.
func (c *Config) DesktopTelemetry() bool {
	if c == nil || c.Desktop.Telemetry == nil {
		return true
	}
	return *c.Desktop.Telemetry
}

// DesktopMetrics reports whether the desktop sends opt-in aggregate agent
// metrics — anonymous (signal, bucket) counters, never content. Default off.
func (c *Config) DesktopMetrics() bool {
	if c == nil || c.Desktop.Metrics == nil {
		return false
	}
	return *c.Desktop.Metrics
}

// DesktopWallpaperPath returns the active wallpaper image path, or "" when no
// wallpaper is configured. The path is validated for existence at serve time,
// not here, so a user moving their config between machines degrades to the
// default background instead of failing to load.
func (c *Config) DesktopWallpaperPath() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.Desktop.WallpaperPath)
}

// Wallpaper blur, dim and fit bounds. Blur is capped low enough that the image
// stays recognisable; dim keeps a floor so the UI text never sits on a busy
// photo at full contrast.
const (
	DesktopWallpaperBlurDefault = 14
	DesktopWallpaperBlurMax     = 40
	DesktopWallpaperDimDefault  = 40
	DesktopWallpaperDimMax      = 85
)

// DesktopWallpaperBlur normalizes desktop.wallpaper_blur into [0, BlurMax].
// Unset values resolve to the default so existing configs get a soft-focus
// wallpaper rather than a harsh full-contrast photo.
func (c *Config) DesktopWallpaperBlur() int {
	if c == nil || c.Desktop.WallpaperBlur == nil {
		return DesktopWallpaperBlurDefault
	}
	return clampInt(*c.Desktop.WallpaperBlur, 0, DesktopWallpaperBlurMax)
}

// DesktopWallpaperDim normalizes desktop.wallpaper_dim into [0, DimMax]. This is
// the theme-tinted scrim between the wallpaper and the UI; it is what keeps the
// interface readable over a photo.
func (c *Config) DesktopWallpaperDim() int {
	if c == nil || c.Desktop.WallpaperDim == nil {
		return DesktopWallpaperDimDefault
	}
	return clampInt(*c.Desktop.WallpaperDim, 0, DesktopWallpaperDimMax)
}

// DesktopWallpaperFit normalizes desktop.wallpaper_fit to cover|contain|tile,
// defaulting to cover.
func (c *Config) DesktopWallpaperFit() string {
	if c == nil {
		return "cover"
	}
	switch strings.ToLower(strings.TrimSpace(c.Desktop.WallpaperFit)) {
	case "contain":
		return "contain"
	case "tile", "repeat":
		return "tile"
	default:
		return "cover"
	}
}

// clampInt bounds v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// LSPConfig governs the optional Language Server Protocol tools (lsp_definition,
// lsp_references, lsp_hover, lsp_diagnostics). Enabled defaults to true; the
// servers themselves are never bundled — each resolves on PATH and the tool
// returns an install hint when it is missing, so the capability is dormant until
// the user installs a server. Servers overrides or extends the built-in language
// → server map, keyed by language id (e.g. "go", "rust", "python").
type LSPConfig struct {
	Enabled bool                 `toml:"enabled"`
	Servers map[string]LSPServer `toml:"servers"`
}

// LSPServer overrides a built-in language's server or, when keyed by a new
// language, adds one. An empty field falls back to the built-in default for that
// language; Extensions is required when adding a language the built-ins don't
// cover (e.g. ".ex" for Elixir) so files route to it.
type LSPServer struct {
	Command     string            `toml:"command"`
	Args        []string          `toml:"args"`
	Env         map[string]string `toml:"env"`
	LanguageID  string            `toml:"language_id"`
	Extensions  []string          `toml:"extensions"`
	InstallHint string            `toml:"install_hint"`
}

// StatuslineConfig configures a custom status line. Command, when set, is run at
// startup and after each turn; its first line of stdout replaces the built-in
// status data row. A JSON payload (model, context tokens, cwd) is fed on stdin.
type StatuslineConfig struct {
	Command string `toml:"command"`
}

// CodegraphConfig governs the built-in CodeGraph MCP server — symbol/call-graph
// code intelligence (tree-sitter + SQLite) that gives the agent codegraph_*
// search / context / explore / trace / node tools. Enabled is opt-in (default
// false): users turn it on in Settings or by writing
// [codegraph] enabled = true; an explicit value always wins. AutoInstall
// (default true) lets fairpeer fetch the CodeGraph runtime into its cache when
// CodeGraph is enabled but missing; set false to require an explicit
// `fairpeer codegraph install` (e.g. for air-gapped or headless runs). Path
// overrides binary resolution; empty resolves the cache, then a `codegraph` on
// PATH, then a bundle beside the executable. CodeGraph always starts in the
// background when enabled; legacy tier values are ignored and removed during
// config load.
type CodegraphConfig struct {
	Enabled     bool   `toml:"enabled"`
	AutoInstall bool   `toml:"auto_install"`
	Path        string `toml:"path"` // local binary path (skips download)
	Tier        string `toml:"tier"`
	DownloadURL string `toml:"download_url"` // custom download base URL for air-gapped/intranet (replaces GitHub default)
}

func (c CodegraphConfig) ShouldAutoStart() bool {
	return c.Enabled
}

func (c CodegraphConfig) ResolvedTier() string {
	return "background"
}

// DreamConfig controls the background self-evolution agents: Dream consolidates
// session knowledge into project memory, Distill extracts repeated workflows
// into reusable skills. Intervals are in days; a value <= 0 falls back to the
// default so a partially-specified [dream] section still behaves sanely.
type DreamConfig struct {
	Enabled         bool `toml:"enabled"`          // master switch; false disables both background agents
	DreamInterval   int  `toml:"dream_interval"`   // days between automatic Dream runs; 0 = default 7
	DistillInterval int  `toml:"distill_interval"` // days between automatic Distill runs; 0 = default 30
	SkillColdDays   int  `toml:"skill_cold_days"`  // days a skill is unused before cold-retirement; 0 = default 90
	IdleMinutes     int  `toml:"idle_minutes"`     // minutes of user inactivity before a Dream run may fire; 0 = default 10
}

// DefaultDreamInterval is the Dream run cadence when [dream].dream_interval is unset.
const DefaultDreamInterval = 7

// DefaultSkillColdDays is the inactivity threshold for skill retirement when
// [dream].skill_cold_days is unset: 90 days mirrors memory's ColdDays default.
const DefaultSkillColdDays = 90

// DefaultIdleMinutes is how long the user must be inactive before an idle Dream
// run may fire, when [dream].idle_minutes is unset. Dream is meant to run in the
// user's downtime, not while they are actively working — this bounds resource
// contention and matches the "consolidate when idle" intent.
const DefaultIdleMinutes = 10

// DefaultFastTaskModel is the model dream/distill/rag-extract run on. Empty
// means unconfigured — at runtime an empty agent.fast_task_model falls back to
// the default model; this constant no longer hardcodes a vendor model. Phase 3
// will resolve it from the configured provider's fast_model role.
const DefaultFastTaskModel = ""

// IdleMinutesEffective returns the effective user-inactivity threshold in
// minutes before an idle Dream run may fire, applying the default when the
// configured value is non-positive. A negative value disables idle triggering
// entirely (Dream then only runs via manual trigger).
func (d DreamConfig) IdleMinutesEffective() int {
	if d.IdleMinutes != 0 {
		return d.IdleMinutes
	}
	return DefaultIdleMinutes
}

// SkillColdDaysEffective returns the effective skill-cold threshold in days,
// applying the default when the configured value is non-positive.
func (d DreamConfig) SkillColdDaysEffective() int {
	if d.SkillColdDays > 0 {
		return d.SkillColdDays
	}
	return DefaultSkillColdDays
}

// DefaultDistillInterval is the Distill run cadence when [dream].distill_interval is unset.
const DefaultDistillInterval = 30

// DreamIntervalDays returns the effective Dream cadence in days, applying the
// default when the configured value is non-positive.
func (d DreamConfig) DreamIntervalDays() int {
	if d.DreamInterval > 0 {
		return d.DreamInterval
	}
	return DefaultDreamInterval
}

// DistillIntervalDays returns the effective Distill cadence in days.
func (d DreamConfig) DistillIntervalDays() int {
	if d.DistillInterval > 0 {
		return d.DistillInterval
	}
	return DefaultDistillInterval
}

// BotConfig 控制多渠道 IM bot 消息网关。
type BotConfig struct {
	Enabled     bool                  `toml:"enabled"`
	Model       string                `toml:"model"` // 用于 bot 的模型名，空则用 default_model
	MaxSteps    int                   `toml:"max_steps"`
	DebounceMs  int                   `toml:"debounce_ms"` // 消息合并窗口，毫秒
	Allowlist   BotAllowlist          `toml:"allowlist"`
	QQ          QQBotConfig           `toml:"qq"`
	Feishu      FeishuBotConfig       `toml:"feishu"`
	Weixin      WeixinBotConfig       `toml:"weixin"`
	Telegram    TelegramBotConfig     `toml:"telegram"`
	Connections []BotConnectionConfig `toml:"connections"`
	// DesktopWatchers 是持久化的"桌面事件订阅"列表：哪些 IM 聊天要接收桌面
	// agent 的审批/提问/完成推送（/desktop watch on 订阅，跨重启保留）。
	DesktopWatchers []BotDesktopWatcher `toml:"desktop_watchers"`
}

// BotDesktopWatcher 是一条持久化的桌面事件订阅（哪个 IM 聊天收桌面推送）。
type BotDesktopWatcher struct {
	Platform string `toml:"platform"`
	ChatType string `toml:"chat_type"`
	ChatID   string `toml:"chat_id"`
}

// CoworkConfig holds coWork (office) profile settings: browser path, PPT
// generation, email (SMTP/IMAP), RAG/knowledge base, screenshot recognition,
// hotkeys, and the Hyper-Extract service. BrowserPath overrides the
// auto-detected Chromium-based browser; when empty, browser_* tools probe the
// standard Chrome/Edge/Brave install locations and fall back to CHROME_PATH.
// Users set this when the browser isn't in a standard location — the agent
// surfaces a clear error guiding them to fill [cowork] browser_path.
type CoworkConfig struct {
	BrowserPath string `toml:"browser_path"` // absolute path to a Chromium-based browser exe; empty = auto-detect
	// BrowserHeadless controls whether the driven browser runs headless. Default
	// false (headed/visible) — a visible browser behaves closer to a human user,
	// keeps login state in a persistent profile, and avoids the rendering
	// quirks headless has on JS-heavy/anti-bot sites (e.g. GitHub's challenge
	// page). Set true for servers/CI where there is no display.
	BrowserHeadless bool `toml:"browser_headless"`
	// BrowserUserDataDir gives the driven browser a persistent user-data
	// directory. Empty = a fresh temp profile per launch (state is lost on
	// restart). Set to a fixed path to keep cookies/login across sessions —
	// essential for sites that require sign-in, and it reduces the "verify you
	// are human" friction on revisit.
	BrowserUserDataDir string `toml:"browser_user_data_dir"`
	// BrowserAttachURL makes browser automation ATTACH to an already-running
	// debug-enabled browser instead of launching a fresh instance per task
	// (e.g. "http://127.0.0.1:9222" — the endpoint the desktop's managed
	// browser uses). Chromium only accepts CDP connections when started with
	// --remote-debugging-port (and, since Chrome 136, only with a non-default
	// user-data-dir), so the target is normally the dedicated managed browser
	// started from Settings → 办公, where logins persist and the window stays
	// open between tasks. Empty = current behavior (launch + own lifecycle
	// per browser_auto run).
	BrowserAttachURL string `toml:"browser_attach_url"`
	// PPTActiveTemplate is the id of the active PPT template (from the templates
	// dir <user-config>/fairpeer/ppt-templates/<id>.json). When set, the ppt-wizard
	// skill generates decks from that template: it opens the template's master_file
	// in WPS (if any) and places content at the template's pre-defined layout
	// coordinates, so most slides don't need per-step VLM perception. Empty = no
	// template, the CUA builds from a blank deck.
	PPTActiveTemplate string `toml:"ppt_active_template"`
	// PPTMode selects how the ppt-wizard skill builds decks. "fast" (default
	// when empty) generates in one pass with no rework; "validate" generates,
	// then checks and reworks on issues. The desktop cowork settings panel
	// exposes this as a dropdown; PPTActiveTemplate still selects the template
	// used in either mode.
	PPTMode string `toml:"ppt_mode"`
	// SMTP configures outbound email (email_send). All fields required to enable
	// sending; empty SMTPHost disables email_send (it returns a config error).
	SMTP SMTPConfig `toml:"smtp"`
	// RAGEnabled is the master switch for the knowledge base (RAG). nil/unset =
	// enabled (the historical default, backward compatible). Set to false to
	// fully disable the knowledge base: no auto-injection into messages, the
	// rag_search/rag_import/... tools are not registered, and expert teams skip
	// knowledge-base context. This is distinct from EmbeddingModel (which only
	// toggles semantic reranking on top of FTS5) — RAGEnabled governs whether RAG
	// runs at all.
	RAGEnabled *bool `toml:"rag_enabled"`
	// EmbeddingModel enables semantic RAG reranking. When set to a provider model
	// ref that supports embeddings, rag_search computes a query embedding and
	// reranks FTS5 hits by cosine similarity. Empty = FTS5-only (the default,
	// works offline). Set to a real embedding model (e.g. a provider with kind
	// "embedding") to upgrade RAG to hybrid.
	EmbeddingModel string `toml:"embedding_model"`
	// VLMBackend is legacy: VLM now always uses the provider multimodal chat path
	// (base64 image_url content parts). Kept for TOML backward-compat but ignored.
	// Use VLMModel / ScreenshotVLMModel to pick the vision model.
	VLMBackend string `toml:"vlm_backend"`
	// VLMModel is the provider model ref for image recognition (screen_perceive).
	// E.g. "<provider>/<model>". Must be vision-capable (provider vision=true).
	VLMModel string `toml:"vlm_model"`
	// IMAP configures inbound email (email_read/search). Empty Host = read tools
	// return "not configured". Reading uses go-imap + go-message (protocol-level
	// correct: full SEARCH, RFC 2047 header decoding, multipart MIME).
	IMAP IMAPConfig `toml:"imap"`
	// EmailAccounts holds the mailboxes FairPeer can talk to. At load time
	// normalizeEmailAccounts folds the legacy single [cowork.smtp]/[cowork.imap]
	// pair above into EmailAccounts[0] when this slice is empty, so existing
	// single-account configs keep working unchanged; new configs may use either
	// form. Tools select an account by Name; Default (or [0]) is the fallback.
	EmailAccounts []EmailAccount `toml:"email_accounts"`
	// ExtractModel is the LLM used by the RAG deep-extraction pipeline (turns
	// imported documents into a structured entity/relation graph). Empty = fall
	// back to the active profile's main model. Pair with ExtractInterval /
	// ExtractConcurrency to tune request cadence; the pipeline is conservative
	// by default (1 concurrent chunk, 3s between chunks) to avoid rate limits.
	ExtractModel string `toml:"extract_model"`
	// ExtractInterval is the pause between chunk extractions (default "3s").
	// Raise it on rate-limited endpoints; lower on generous local models.
	ExtractInterval string `toml:"extract_interval"`
	// ExtractConcurrency is how many chunks extract in parallel (default 1).
	// Keep low to avoid tripping rate limits — extraction is a background task
	// where throughput matters less than "no errors".
	ExtractConcurrency int `toml:"extract_concurrency"`

	// ScreenshotEnabled turns on the global-hotkey screenshot-to-VLM feature.
	// When true, pressing ScreenshotHotkey anywhere (even when FairPeer is in
	// the background) captures the screen, sends it to ScreenshotVLMModel for
	// recognition, and replies via IM bot + in-app toast. Default false — the
	// user opts in via the cowork settings tab.
	ScreenshotEnabled bool `toml:"screenshot_enabled"`
	// ScreenshotHotkey is the global hotkey combination (e.g. "Ctrl+Shift+Alt+W").
	// Detected via GetAsyncKeyState polling so it fires even when FairPeer isn't
	// focused. Default "Ctrl+Shift+Alt+W".
	ScreenshotHotkey string `toml:"screenshot_hotkey"`
	// ScreenshotVLMModel is the model used for screenshot recognition.
	// This is the SINGLE place all image-recognition config lives —
	// set it once in the cowork settings page.
	ScreenshotVLMModel string `toml:"screenshot_vlm_model"`
	// VoiceModel is the provider model ref for speech-to-text, used by voice
	// input (mic button) and audio-attachment understanding. E.g.
	// "stepfun/stepaudio-2.5-asr" or "zhipu/glm-asr-2512". It must point at an
	// OpenAI-compatible /audio/transcriptions endpoint. Independent of the main
	// chat model — any main model works, since audio is transcribed to text
	// first and the text is then sent to the main model. Empty = voice input
	// disabled (the mic button is disabled with a hint to configure a model).
	VoiceModel string `toml:"voice_model"`
	// ScreenshotPrompt is the user prompt sent with the screenshot image to the
	// VLM model. Users can customize this to change the solving behavior (e.g.
	// focus on specific subjects, require verification, etc.). Empty means use
	// the built-in default.
	ScreenshotPrompt string `toml:"screenshot_prompt"`

	// EStopHotkey is the global EMERGENCY-STOP hotkey for coWork desktop
	// automation. Pressing it anywhere (even with FairPeer minimized) cancels
	// the in-flight turn on the active tab — the kill switch for screen_* tools,
	// whose clicks/typing are irreversible. Registered via Win32 RegisterHotKey
	// like the screenshot hotkey. Default "Ctrl+Shift+Pause". Set to "off" to
	// disable the feature entirely.
	EStopHotkey string `toml:"estop_hotkey"`
	// HEPort is the port for the Hyper-Extract Python server. Default 0 means
	// use the built-in default (18900).
	HEPort int `toml:"he_port"`
	// BrowserUseEnabled controls whether the browser-use autonomous-browsing
	// sidecar is wired up. When false (the zero-value DEFAULT), browser_auto
	// returns a clear "disabled" error and no Python sidecar is started. This
	// is intentionally opt-in: the sidecar needs the browser-use Python package
	// installed (and a provider client), so users who haven't set that up are
	// not bothered by startup failures. Set browser_use_enabled = true once
	// the environment is ready.
	BrowserUseEnabled bool `toml:"browser_use_enabled"`
	// BrowserUsePython overrides the Python interpreter used to run the
	// browser-use sidecar. Empty = "python" (Windows) / "python3" (other). In
	// the packaged build this resolves to the bundled runtime's python.exe.
	BrowserUsePython string `toml:"browser_use_python"`
	// BrowserUsePort is the port for the browser-use sidecar. Default 0 means
	// use the built-in default (18901, distinct from HE's 18900).
	BrowserUsePort int `toml:"browser_use_port"`
	// BrowserUseModel is the provider model ref the sidecar uses for the
	// agentic loop (e.g. "<provider>/<model>"). Empty = fall back to VLMModel,
	// then the main agent model. A strong vision-capable model is strongly
	// recommended — the loop reads screenshots/accessibility trees.
	BrowserUseModel string `toml:"browser_use_model"`
	// BrowserUseMaxSteps caps the agentic loop. Default 0 means let the sidecar
	// pick a sensible bound. Set lower for cheaper/faster runs, higher for
	// complex multi-page tasks.
	BrowserUseMaxSteps int `toml:"browser_use_max_steps"`
	// FastLLMBaseDomain overrides the base URL for direct /chat/completions calls
	// made by the scheduler time-parser and RAG ask (legacy path; Phase 3 will
	// route these through the resolved fast-task provider instead). Empty = the
	// built-in default.
	FastLLMBaseDomain string `toml:"fast_llm_base_domain"`
}

// RAGEnabledOrDefault reports whether the knowledge base (RAG) is enabled. A nil
// RAGEnabled means the user never set it → enabled (backward compatible). Only
// an explicit false disables RAG. Callers that gate RAG behaviour should use
// this rather than dereferencing the pointer directly.
func (c CoworkConfig) RAGEnabledOrDefault() bool {
	if c.RAGEnabled == nil {
		return true
	}
	return *c.RAGEnabled
}

// LLMConfig holds the global LLM request budget (rate limiting). It applies
// across ALL providers via a decorator, so main-agent + subagent + RAG
// extraction + IM bot responses all share the same per-API-key RPM quota.
//
// RPM reflects the user's real API-key rate limit (default 60/min;
// higher tiers or other providers may allow more). Leave at 0 to disable
// rate limiting entirely (unlimited, backward-compatible).
//
// ReserveMain keeps main-agent requests always answerable even when background
// tasks (expert teams, extraction) have consumed most of the per-minute quota:
// background requests wait for the next window when remaining <= reserve.
type LLMConfig struct {
	RPM         int `toml:"rpm"`          // max requests/minute per API key (0 = unlimited)
	TPM         int `toml:"tpm"`          // max tokens/minute (0 = unlimited; reserved, not enforced yet)
	ReserveMain int `toml:"reserve_main"` // requests reserved for main-agent priority (default 2)
}

// IMAPConfig holds inbound mail server settings for email_read/search.
type IMAPConfig struct {
	Host          string `toml:"host"`            // IMAP server host, e.g. imap.example.com
	Port          int    `toml:"port"`            // IMAP port (993 for implicit TLS, 143 for STARTTLS/plain)
	Username      string `toml:"username"`        // mailbox login
	PasswordEnv   string `toml:"password_env"`    // env var holding the password
	SkipTLSVerify bool   `toml:"skip_tls_verify"` // opt in to skip TLS cert verification (self-signed/corporate CAs). Verification stays ON unless this is true.
}

// SMTPConfig holds outbound mail server settings. Secrets come from the env via
// PasswordEnv (never stored in the TOML).
type SMTPConfig struct {
	Host           string `toml:"host"`            // SMTP server host, e.g. smtp.example.com
	Port           int    `toml:"port"`            // SMTP port (587 for STARTTLS, 465 for implicit TLS, 25 plain)
	From           string `toml:"from"`            // sender address, e.g. agent@example.com
	Username       string `toml:"username"`        // SMTP auth username (often = From); empty = no auth
	PasswordEnv    string `toml:"password_env"`    // env var holding the SMTP password (never stored)
	UseTLS         bool   `toml:"use_tls"`         // implicit TLS (port 465); false = STARTTLS/plain. DEPRECATED: use encryption_mode.
	EncryptionMode string `toml:"encryption_mode"` // "tls" (implicit, 465) | "starttls" (587) | "none" (25). Empty → migrate from use_tls.
}

// EmailAccount bundles one mailbox's inbound (IMAP) and outbound (SMTP) settings
// under a user-chosen name, so FairPeer can talk to multiple mailboxes at once
// (e.g. a personal 139 box and a work CMCC box). Tools/scheduler select an
// account by Name; the one flagged Default (or else the first) is used when the
// caller omits a name.
type EmailAccount struct {
	Name    string     `toml:"name"`    // stable handle tools/scheduler address (e.g. "personal-139")
	Default bool       `toml:"default"` // used when a tool omits an account name
	SMTP    SMTPConfig `toml:"smtp"`
	IMAP    IMAPConfig `toml:"imap"`
}

// normalizeSMTP migrates the deprecated use_tls bool onto encryption_mode and
// validates the value. Called once after config load.
func normalizeSMTP(c *SMTPConfig) {
	switch strings.ToLower(strings.TrimSpace(c.EncryptionMode)) {
	case "tls", "starttls", "none":
		return // already canonical
	}
	// Migrate from legacy use_tls: true → tls, false → starttls.
	if c.UseTLS {
		c.EncryptionMode = "tls"
	} else {
		c.EncryptionMode = "starttls"
	}
}

// normalizeEmailAccounts migrates the legacy single [cowork.smtp]/[cowork.imap]
// pair into the EmailAccounts slice when the slice is empty, re-runs per-account
// SMTP normalization (encryption_mode migration), and ensures exactly one
// account is flagged Default. It then mirrors the default account back onto the
// single SMTP/IMAP fields, so older code paths that still read cfg.Cowork.SMTP /
// cfg.Cowork.IMAP (e.g. the desktop settings panel) keep seeing the active
// mailbox. Existing multi-account configs are left intact apart from this.
func normalizeEmailAccounts(c *CoworkConfig) {
	for i := range c.EmailAccounts {
		normalizeSMTP(&c.EmailAccounts[i].SMTP)
	}
	if len(c.EmailAccounts) == 0 {
		// Fold the legacy single pair into a one-element account when either
		// side is configured. Name "primary" for a stable handle; mark Default.
		if strings.TrimSpace(c.SMTP.Host) != "" || strings.TrimSpace(c.IMAP.Host) != "" {
			c.EmailAccounts = []EmailAccount{{
				Name:    "primary",
				Default: true,
				SMTP:    c.SMTP,
				IMAP:    c.IMAP,
			}}
		}
		return
	}
	// Ensure exactly one Default: flag the first Default found, clear the rest;
	// if none is flagged, flag the first account.
	seenDefault := false
	for i := range c.EmailAccounts {
		if c.EmailAccounts[i].Default {
			if seenDefault {
				c.EmailAccounts[i].Default = false
			} else {
				seenDefault = true
			}
		}
	}
	if !seenDefault {
		c.EmailAccounts[0].Default = true
	}
	// Keep the legacy single fields in sync with the default account.
	if a, ok := c.DefaultEmailAccount(); ok {
		c.SMTP = a.SMTP
		c.IMAP = a.IMAP
	}
}

// DefaultEmailAccount returns the account to use when no name is given: the one
// flagged Default, else the first, else a zero account with ok=false.
func (c CoworkConfig) DefaultEmailAccount() (EmailAccount, bool) {
	for _, a := range c.EmailAccounts {
		if a.Default {
			return a, true
		}
	}
	if len(c.EmailAccounts) > 0 {
		return c.EmailAccounts[0], true
	}
	return EmailAccount{}, false
}

// EmailAccountByName returns the account whose Name matches (case-insensitive),
// or the default account when name is empty. ok=false when the name is non-empty
// and unknown, or when there are no accounts at all.
func (c CoworkConfig) EmailAccountByName(name string) (EmailAccount, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return c.DefaultEmailAccount()
	}
	for _, a := range c.EmailAccounts {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return EmailAccount{}, false
}

// BotAllowlist 控制哪些用户可以使用 bot。
type BotAllowlist struct {
	Enabled        bool     `toml:"enabled"`
	AllowAll       bool     `toml:"allow_all"`
	Mode           string   `toml:"mode"` // "open"（默认，自动加入）| "review"（需管理员审批）
	QQUsers        []string `toml:"qq_users"`
	FeishuUsers    []string `toml:"feishu_users"`
	WeixinUsers    []string `toml:"weixin_users"`
	TelegramUsers  []string `toml:"telegram_users"`
	QQGroups       []string `toml:"qq_groups"`
	FeishuGroups   []string `toml:"feishu_groups"`
	WeixinGroups   []string `toml:"weixin_groups"`
	TelegramGroups []string `toml:"telegram_groups"`
}

// QQBotConfig QQ 官方 Bot API v2 配置。
type QQBotConfig struct {
	Enabled      bool   `toml:"enabled"`
	AppID        string `toml:"app_id"`
	AppSecretEnv string `toml:"app_secret_env"` // 环境变量名，如 QQ_BOT_APP_SECRET
}

// FeishuBotConfig 飞书自建应用 Bot 配置。
type FeishuBotConfig struct {
	Enabled           bool   `toml:"enabled"`
	Domain            string `toml:"domain"` // feishu（默认）| lark
	AppID             string `toml:"app_id"`
	AppSecretEnv      string `toml:"app_secret_env"`     // 如 FEISHU_BOT_APP_SECRET
	VerificationToken string `toml:"verification_token"` // 事件订阅验证 token
	Mode              string `toml:"mode"`               // webhook（默认）| websocket
	WebhookPort       int    `toml:"webhook_port"`       // webhook 模式端口
	RequireMention    bool   `toml:"require_mention"`
}

// WeixinBotConfig 微信 iLink Bot 配置。
type WeixinBotConfig struct {
	Enabled   bool   `toml:"enabled"`
	AccountID string `toml:"account_id"`
	TokenEnv  string `toml:"token_env"` // 环境变量名，如 WEIXIN_BOT_TOKEN
	APIBase   string `toml:"api_base"`  // iLink API base URL
}

// TelegramBotConfig Telegram Bot API 配置。鉴权用单个静态 Bot Token（@BotFather 颁发）。
type TelegramBotConfig struct {
	Enabled  bool   `toml:"enabled"`
	TokenEnv string `toml:"token_env"` // 环境变量名，如 TELEGRAM_BOT_TOKEN
	APIBase  string `toml:"api_base"`  // 可选：自建 Bot API 服务器，空 = 官方 https://api.telegram.org
}

// BotConnectionConfig is the desktop-friendly connection record for IM bot
// channels. It keeps install/runtime state separate from legacy per-provider
// knobs so the UI can expose a simple "connect first" flow while old configs
// keep working.
type BotConnectionConfig struct {
	ID              string                        `toml:"id"`
	Provider        string                        `toml:"provider"` // qq|feishu|weixin|telegram
	Domain          string                        `toml:"domain"`   // feishu|lark|weixin|qq|telegram
	Label           string                        `toml:"label"`
	Enabled         bool                          `toml:"enabled"`
	Status          string                        `toml:"status"` // disconnected|pending|connected|error
	Model           string                        `toml:"model"`
	WorkspaceRoot   string                        `toml:"workspace_root"`
	Credential      BotConnectionCredential       `toml:"credential"`
	SessionMappings []BotConnectionSessionMapping `toml:"session_mappings"`
	LastError       string                        `toml:"last_error"`
	CreatedAt       string                        `toml:"created_at"`
	UpdatedAt       string                        `toml:"updated_at"`
}

type BotConnectionCredential struct {
	AppID        string `toml:"app_id"`
	AppSecretEnv string `toml:"app_secret_env"`
	AccountID    string `toml:"account_id"`
	TokenEnv     string `toml:"token_env"`
}

type BotConnectionSessionMapping struct {
	RemoteID      string `toml:"remote_id"`
	ChatType      string `toml:"chat_type"`
	ChatID        string `toml:"chat_id"`
	SessionID     string `toml:"session_id"`
	Scope         string `toml:"scope"`
	WorkspaceRoot string `toml:"workspace_root"`
	UpdatedAt     string `toml:"updated_at"`
}

// NetworkConfig controls ordinary outbound HTTP traffic such as model providers,
// updater checks, CodeGraph downloads, and web_fetch.
// web_fetch reuses these proxy settings while keeping its own SSRF-guarded
// dialer.
type NetworkConfig struct {
	// ProxyMode is "auto" (default; environment proxy for now), "env", "custom",
	// or "off". auto leaves room for OS proxy detection later without changing the
	// config shape.
	ProxyMode string `toml:"proxy_mode"`
	// ProxyURL is an advanced custom override such as "socks5://127.0.0.1:7890".
	// When set and proxy_mode = "custom", it wins over the structured proxy table.
	ProxyURL string `toml:"proxy_url"`
	// NoProxy is honored for custom proxies. Env/auto modes use NO_PROXY from the
	// process environment instead.
	NoProxy string             `toml:"no_proxy"`
	Proxy   NetworkProxyConfig `toml:"proxy"`
}

// NetworkProxyConfig is the structured custom-proxy editor shape. Password is
// optional and supports ${VAR} expansion, so users can avoid storing it literally.
type NetworkProxyConfig struct {
	Type     string `toml:"type"` // http|https|socks5|socks5h
	Server   string `toml:"server"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
}

// NetworkProxySpec returns the expanded proxy settings used by netclient.
func (c *Config) NetworkProxySpec() netclient.ProxySpec {
	return netclient.ProxySpec{
		Mode:        c.Network.ProxyMode,
		URL:         ExpandVars(c.Network.ProxyURL),
		NoProxy:     ExpandVars(c.Network.NoProxy),
		Type:        c.Network.Proxy.Type,
		Server:      ExpandVars(c.Network.Proxy.Server),
		Port:        c.Network.Proxy.Port,
		Username:    ExpandVars(c.Network.Proxy.Username),
		Password:    ExpandVars(c.Network.Proxy.Password),
		DirectHosts: c.directProxyHosts(),
	}
}

// directProxyHosts collects the base_url hosts of providers marked no_proxy, so
// netclient bypasses the proxy for them without knowing any provider by name.
//
// Only for an auto-detected proxy (auto/env): that proxy is typically a
// GFW-circumvention one not meant for domestic endpoints, so keep
// them direct. An explicit proxy_mode = "custom" is the user saying "route
// everything through this" — e.g. a mandatory corporate proxy — so honor it for
// every provider; a custom-proxy user who wants a host direct uses
// network.no_proxy instead (#3635).
func (c *Config) directProxyHosts() []string {
	if c.NetworkProxyMode() == netclient.ModeCustom {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range c.Providers {
		if !p.NoProxy {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(p.BaseURL))
		if err != nil {
			continue
		}
		if h := u.Hostname(); h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// NetworkProxyMode normalizes network.proxy_mode to a known value.
func (c *Config) NetworkProxyMode() string {
	return netclient.NormalizeMode(c.Network.ProxyMode)
}

// SkillsConfig configures skill discovery. Paths adds extra "custom"-scope skill
// roots — each a directory of SKILL.md / <name>.md playbooks — scanned between
// the project roots (.fairpeer/.agents/.agent/.claude under the workspace) and
// the global roots. ExcludedPaths hides matching discovery roots without deleting
// folders. ~, relative paths, and ${VAR} expansion are supported. DisabledSkills
// hides named skills from the agent prompt, slash invocation, and skill tools
// while keeping them manageable.
type SkillsConfig struct {
	Paths          []string `toml:"paths"`
	ExcludedPaths  []string `toml:"excluded_paths"`
	DisabledSkills []string `toml:"disabled_skills"`
	MaxDepth       int      `toml:"max_depth"`
}

// SkillCustomPaths returns the configured custom skill roots with ${VAR}
// expanded; empty entries are dropped.
func (c *Config) SkillCustomPaths() []string {
	var out []string
	for _, p := range c.Skills.Paths {
		if p = ExpandVars(p); strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// SkillExcludedPaths returns configured skill roots that should be hidden from
// discovery, with ${VAR} expanded and empty entries dropped.
func (c *Config) SkillExcludedPaths() []string {
	var out []string
	for _, p := range c.Skills.ExcludedPaths {
		if p = ExpandVars(p); strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// SkillMaxDepth bounds nested skill discovery. Depth 3 favors bundled skill
// packs while Store keeps nested markdown safe by requiring descriptions.
func (c *Config) SkillMaxDepth() int {
	const (
		defaultDepth = 3
		maxDepth     = 5
	)
	if c == nil || c.Skills.MaxDepth == 0 {
		return defaultDepth
	}
	if c.Skills.MaxDepth < 1 {
		return 1
	}
	if c.Skills.MaxDepth > maxDepth {
		return maxDepth
	}
	return c.Skills.MaxDepth
}

// DisabledSkillNames returns valid disabled skill identifiers, preserving the
// first spelling and dropping duplicates/empty entries.
func (c *Config) DisabledSkillNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range c.Skills.DisabledSkills {
		name = strings.TrimSpace(name)
		if !IsValidSkillName(name) {
			continue
		}
		key := SkillNameKey(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	return out
}

// IsSkillDisabled reports whether name is configured as disabled.
func (c *Config) IsSkillDisabled(name string) bool {
	key := SkillNameKey(name)
	if key == "" {
		return false
	}
	for _, disabled := range c.DisabledSkillNames() {
		if SkillNameKey(disabled) == key {
			return true
		}
	}
	return false
}

// SandboxConfig bounds the blast radius of tool calls (Phase 0: file-writer
// confinement). WorkspaceRoot is the directory the built-in file writers
// (write_file / edit_file / multi_edit) may modify; empty means the current
// working directory, so writes stay inside the project by default. AllowWrite
// lists extra directories writers may also touch (e.g. a sibling repo or a temp
// dir). Both support ${VAR} / ${VAR:-default} expansion. Reads are unrestricted;
// confining `bash` is Phase 1 (OS-level sandbox).
type SandboxConfig struct {
	WorkspaceRoot string   `toml:"workspace_root"`
	AllowWrite    []string `toml:"allow_write"`
	// Bash is the OS-sandbox mode for the bash tool: "enforce" (default) jails
	// each command, "off" runs it unconfined. Phase 1; macOS only for now, with
	// a graceful fallback elsewhere (see internal/sandbox).
	Bash string `toml:"bash"`
	// Network allows network egress from inside the bash sandbox. Defaults true
	// so module/package downloads keep working; the boundary is then writes.
	Network bool `toml:"network"`
	// RequireAvailable, when true, makes bash mode "enforce" fail-closed (refuse
	// all commands) if no OS sandbox is available on this platform, rather than
	// silently degrading to unconfined. False (default) keeps the graceful
	// fallback so fairpeer stays usable on unsupported OSes.
	RequireAvailable bool `toml:"require_available"`
	// StrictWrites narrows the macOS Seatbelt toolchain-cache grants to true
	// cache subdirs only (~/.cargo/registry/cache, ~/Library/Caches, …) so a
	// prompt-injected command can't drop an executable in ~/.cargo/bin or
	// ~/.npm. Default false: the broad grants are needed for `go install`/
	// `cargo build`/`npm install` (they write to bin/pkg dirs). Turn on for
	// high-security deployments that don't run build tools. Audit A8. macOS only.
	StrictWrites bool `toml:"strict_writes"`
	// ReadRoots confines read_file/grep to these directories (opt-in). Empty =
	// unconfined reads (the default), because an agent legitimately reads /etc,
	// system headers, ~/.gitconfig, package caches, etc. A high-security
	// deployment that wants read/data isolation sets this; boot then wires
	// ConfineReaders to override the unconfined defaults. Note: bash is NOT
	// read-confined even when this is set, so this is defense-in-depth. Audit A7.
	ReadRoots []string `toml:"read_roots"`
}

// WriteRoots returns the directories file-writer tools may modify: the
// workspace root (defaulting to the current working directory when unset) plus
// any AllowWrite extras, with ${VAR} expanded. The roots are returned as given
// (relative or absolute); the confiner resolves them to absolute, symlink-free
// paths. The result is always non-empty, so confinement is on by default.
func (c *Config) WriteRoots() []string {
	return c.WriteRootsForRoot(".")
}

// WriteRootsForRoot is like WriteRoots but falls back to fallbackRoot when the
// config doesn't explicitly set a workspace_root. Desktop tabs pass their
// project root here so tool confinement is correct without changing cwd.
func (c *Config) WriteRootsForRoot(fallbackRoot string) []string {
	root := ExpandVars(c.Sandbox.WorkspaceRoot)
	if root == "" {
		root = fallbackRoot
		if root == "" || root == "." {
			if wd, err := os.Getwd(); err == nil {
				root = wd
			} else {
				root = "."
			}
		}
	}
	roots := []string{root}
	for _, d := range c.Sandbox.AllowWrite {
		if d = ExpandVars(d); d != "" {
			roots = append(roots, d)
		}
	}
	return roots
}

// ReadRoots returns the directories read_file/grep are confined to, with ${VAR}
// expanded. Empty (the default) means reads are unconfined — the safe default
// that preserves the agent's ability to read system files. boot passes this to
// builtin.ConfineReaders only when non-empty. Audit A7.
func (c *Config) ReadRoots() []string {
	var out []string
	for _, r := range c.Sandbox.ReadRoots {
		if r = strings.TrimSpace(ExpandVars(r)); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// BashMode normalises the bash-sandbox mode: only an explicit "off" disables
// it; empty or any other value resolves to "enforce", so the sandbox is on by
// default and fails safe.
func (c *Config) BashMode() string {
	if c.Sandbox.Bash == "off" {
		return "off"
	}
	return "enforce"
}

// AgentConfig configures the harness loop. PlannerModel is optional: when set
// to another provider's name it enables two-model collaboration, where the
// planner handles low-frequency planning in its own session (kept separate so
// each model's prompt prefix stays cache-stable; some providers do not report
// cache tokens). SubagentModel is the optional default for runAs=subagent
// skills; SubagentModels overrides it per skill name.
type AgentConfig struct {
	SystemPrompt     string            `toml:"system_prompt"`
	SystemPromptFile string            `toml:"system_prompt_file"`
	MaxSteps         int               `toml:"max_steps"`         // tool-call rounds per turn; 0 = unlimited
	PlannerMaxSteps  int               `toml:"planner_max_steps"` // planner read-only tool-call rounds; 0 = unlimited
	Temperature      float64           `toml:"temperature"`
	PlannerModel     string            `toml:"planner_model"`
	SubagentModel    string            `toml:"subagent_model"`
	SubagentModels   map[string]string `toml:"subagent_models"`
	SubagentEffort   string            `toml:"subagent_effort"`
	SubagentEfforts  map[string]string `toml:"subagent_efforts"`
	FastTaskModel    string            `toml:"fast_task_model"` // lightweight model for dream/distill background tasks
	// OutputStyle selects a persona/tone block folded into the system prompt at
	// startup (a built-in like "explanatory"/"learning"/"concise", or a custom
	// .fairpeer/output-styles/<name>.md). Empty = the unmodified prompt.
	OutputStyle string `toml:"output_style"`
	// AutoPlan controls whether interactive turns that look multi-step start in
	// plan mode automatically: "off" keeps plan mode manual, "on" enables the
	// approval gate. Legacy "ask" is treated as "on".
	AutoPlan string `toml:"auto_plan"`
	// AutoPlanClassifier optionally names a provider/model used to classify
	// borderline auto-plan decisions. Empty keeps the zero-cost heuristic path.
	AutoPlanClassifier string `toml:"auto_plan_classifier"`
	// Compaction window fractions: soft = notice only, compact = trigger, force = hard ceiling.
	SoftCompactRatio  float64 `toml:"soft_compact_ratio"`
	CompactRatio      float64 `toml:"compact_ratio"`
	CompactForceRatio float64 `toml:"compact_force_ratio"`
	// ContextBudgetPercent caps the effective window for compaction decisions
	// (SPEC v2 §3.6). 0/100 = full window (default, zero config). E.g. 80 =
	// compact as if the window were 80% of its real size — saves cost on input
	// pricing tiers and avoids quality drop near the window edge.
	ContextBudgetPercent int `toml:"context_budget_percent"`
}

// ProviderEntry declares a model provider instance. ContextWindow is the model's
// token budget; the harness compacts older history as a turn's prompt approaches
// it (see agent compaction). 0 disables compaction for the instance.
type ProviderEntry struct {
	Name      string   `toml:"name"`
	Kind      string   `toml:"kind"`
	BaseURL   string   `toml:"base_url"`
	Model     string   `toml:"model"`      // a single model (back-compat)
	Models    []string `toml:"models"`     // a vendor's model list (one base_url/key, many models)
	ModelsURL string   `toml:"models_url"` // auto-fetch models from this URL on startup
	Default   string   `toml:"default"`    // default model when Models is set (else Models[0])
	// FastModel is the lightweight model used for background/fast tasks
	// (dream/distill/rag-extract, scheduler time-parse). Empty = fall back to
	// Default at runtime. This is the per-provider "fast" role; the global
	// agent.fast_task_model can override it.
	FastModel     string            `toml:"fast_model"`
	APIKeyEnv     string            `toml:"api_key_env"`
	ContextWindow int               `toml:"context_window"`
	Price         *provider.Pricing `toml:"price"`
	// Thinking / Effort are provider-kind-specific knobs forwarded to the provider
	// via Config.Extra. The anthropic provider reads Thinking="adaptive" to enable
	// extended thinking and Effort ("low".."max") to tune depth. The
	// openai-compatible provider forwards Effort as reasoning_effort for
	// thinking-capable models.
	// Empty = provider default.
	Thinking string `toml:"thinking"`
	Effort   string `toml:"effort"`
	// ReasoningProtocol selects the request shape for OpenAI-compatible reasoning
	// models. Empty/auto uses the model capability registry plus endpoint
	// heuristics; none disables automatic reasoning controls for this provider.
	ReasoningProtocol string `toml:"reasoning_protocol"`
	// SupportedEfforts lists the /effort levels this provider/model exposes.
	// When non-empty, it overrides the built-in defaults. All providers
	// support the unified low/medium/high vocabulary by default. "auto" is
	// the implicit prefix — always accepted.
	SupportedEfforts []string `toml:"supported_efforts"`
	// DefaultEffort is the /effort level used when the user picks "auto" or
	// has not set Effort. Ignored when SupportedEfforts is empty.
	DefaultEffort string `toml:"default_effort"`
	// Vision enables image support for this provider. When true, user-attached
	// images are sent as image_url content parts. When false (default), images
	// are stripped before sending to avoid 400 errors from text-only models.
	Vision bool `toml:"vision"`
	// VisionDetail controls the image detail level sent to the API ("auto",
	// "low", "high"). Only effective when Vision is true.
	VisionDetail string `toml:"vision_detail"`
	// NoProxy reaches this provider's base_url directly, never through the proxy.
	// For China-only endpoints a foreign-exit proxy resets the TLS handshake (#2803).
	NoProxy bool `toml:"no_proxy"`
	// CodingOnly marks this provider as consuming a Coding Plan subscription
	// quota (vs the regular token quota). UI surfaces a "consumes subscription
	// quota" hint; optionally restricts to coding-tool use per vendor terms.
	CodingOnly bool `toml:"coding_only"`
	// Aggregator marks this provider as a model-aggregation platform that can
	// call multiple vendors' models under one endpoint+key (e.g. a Coding Plan).
	// UI groups these under an "aggregators" section; not a routing branch.
	Aggregator bool `toml:"aggregator"`
}

// ModelList returns the models this provider exposes: the explicit `models` list,
// or the single `model` as a one-element list (back-compat). Empty if neither set.
func (e *ProviderEntry) ModelList() []string {
	if len(e.Models) > 0 {
		return e.Models
	}
	if e.Model != "" {
		return []string{e.Model}
	}
	return nil
}

// IsLikelyChatModel reports whether a model ID looks like a chat/completion
// model rather than a specialised audio/vision/embedding model. It applies a
// conservative name-based heuristic — the OpenAI-compatible /models API does
// not return capability/modality metadata, so this is the most reliable
// fallback until providers add such fields.
//
// The heuristic works in two passes:
//  1. Multi-word substring check for compound terms that span separators
//     (e.g. "text-embedding", "text-to-speech").
//  2. Token-level check: the model ID is split on common separators (- _ . / :)
//     and each token is compared against a set of known non-chat keywords.
//
// "voice" is intentionally absent from the non-chat set because it is too
// broad — legitimate future chat models may include it in their name.
// IsLikelyChatModel filters non-chat models (TTS/STT/embedding/rerank/image/
// video…) out of the chat model picker. BLACKLIST-BY-TOKEN maintenance model:
// when a provider ships a NEW non-chat modality, its token must be added here
// manually (last additions: grok-imagine / sora → "imagine", "video").
// Allowlist-style classification lives in the registry layer instead
// (ProviderTemplate.ReasoningModels); this function must stay zero-dependency.
func IsLikelyChatModel(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	lower := strings.ToLower(model)

	// Pass 1: compound terms that span separator boundaries.
	var compoundNonChat = []string{
		"text-embedding", "text-to-speech", "speech-to-text",
	}
	for _, c := range compoundNonChat {
		if strings.Contains(lower, c) {
			return false
		}
	}

	// Pass 2: token-level check.
	tokens := strings.FieldsFunc(lower, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '/' || r == ':'
	})
	var nonChatTokens = map[string]bool{
		"asr": true, "stt": true, "tts": true,
		"whisper": true, "embedding": true,
		"moderation": true, "rerank": true, "dall": true,
		"transcription": true,
		"imagine":       true, "video": true, // image/video generation (grok-imagine-*, sora-*-video)
	}
	for _, tok := range tokens {
		if nonChatTokens[tok] {
			return false
		}
	}
	return true
}

// ChatModelList returns ModelList filtered to likely chat/completion models.
// Non-chat models (TTS, STT, ASR, embedding, etc.) are excluded so they do
// not appear in the chat model picker. Use ModelList() only when the full
// raw provider model list is needed, such as config serialization, provider
// diagnostics, or model-fetch editing.
func (e *ProviderEntry) ChatModelList() []string {
	raw := e.ModelList()
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, m := range raw {
		if IsLikelyChatModel(m) {
			out = append(out, m)
		}
	}
	return out
}

// DefaultModel returns the provider's default model: the explicit `default`, else
// the first of ModelList.
func (e *ProviderEntry) DefaultModel() string {
	if e.Default != "" {
		return e.Default
	}
	if l := e.ModelList(); len(l) > 0 {
		return l[0]
	}
	return ""
}

// HasModel reports whether m is one of the provider's models.
func (e *ProviderEntry) HasModel(m string) bool {
	for _, x := range e.ModelList() {
		if x == m {
			return true
		}
	}
	return false
}

// ToolsConfig selects which built-in tools are enabled. Empty means all of them.
type ToolsConfig struct {
	Enabled            []string     `toml:"enabled"`
	BashTimeoutSeconds *int         `toml:"bash_timeout_seconds"`
	Search             SearchConfig `toml:"search"`
}

const defaultBashTimeoutSeconds = 120

// BashTimeoutSeconds returns the foreground bash timeout in seconds. An omitted
// config keeps the historical 120s safety cap, explicit 0 disables the
// tool-local cap, and positive values set a custom cap. Negative values fall
// back to the default so a typo cannot silently remove the safety net.
func (c *Config) BashTimeoutSeconds() int {
	if c.Tools.BashTimeoutSeconds == nil || *c.Tools.BashTimeoutSeconds < 0 {
		return defaultBashTimeoutSeconds
	}
	return *c.Tools.BashTimeoutSeconds
}

// SearchConfig tunes the grep tool's engine. Engine is "auto" (default — use
// ripgrep when it's on PATH, else the native Go scanner), "native" (always Go),
// or "rg" (require ripgrep; warn at startup and fall back to native if absent).
// RgPath optionally points at a specific ripgrep binary instead of a PATH lookup.
type SearchConfig struct {
	Engine string `toml:"engine"`
	RgPath string `toml:"rg_path"`
}

// PermissionsConfig declares the per-call permission policy (see
// internal/permission). Mode is the fallback decision for writer tools when no
// rule matches ("ask" | "allow" | "deny"; default "ask"); read-only tools always
// fall back to allow. Allow/Ask/Deny are rule lists of the form "ToolName" or
// "ToolName(glob)". Precedence: deny > ask > allow > fallback.
type PermissionsConfig struct {
	Mode  string   `toml:"mode"`
	Allow []string `toml:"allow"`
	Ask   []string `toml:"ask"`
	Deny  []string `toml:"deny"`
}

// PluginEntry declares an external MCP server. Type selects the transport:
// "stdio" (default) launches Command/Args/Env as a subprocess; "http"
// (a.k.a. streamable-http) and "sse" connect to a remote URL with optional
// static Headers. String fields support ${VAR} / ${VAR:-default} expansion so
// secrets (bearer tokens, keys) come from the environment, not the file. The
// fields mirror Claude Code's mcpServers spec, so entries can come from either
// fairpeer.toml's [[plugins]] or a project-root .mcp.json (see loadMCPJSON).
type PluginEntry struct {
	Name    string            `toml:"name"`
	Type    string            `toml:"type"` // "stdio" (default) | "http" | "sse"
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	URL     string            `toml:"url"`
	Headers map[string]string `toml:"headers"`
	// AutoStart controls whether the server connects during session startup.
	// Nil preserves historical behavior: configured servers start automatically.
	AutoStart *bool `toml:"auto_start"`
	// Tier selects how aggressively the server is connected at boot:
	//   "eager"      — blocks startup until the handshake completes; required for
	//                  servers whose tools the system prompt depends on.
	//   "lazy"       — registers placeholder tools immediately (from on-disk
	//                  schema cache when available) and only spawns the real
	//                  subprocess on first model use. Kept for legacy configs.
	//   "background" — placeholder + spawn fired at boot but not waited on;
	//                  swap happens once the spawn finishes.
	// Empty defaults to "background" so enabled MCPs connect automatically
	// without blocking chat. Unknown non-empty values fall back to "lazy".
	Tier string `toml:"tier"`
	// CallTimeout overrides the per-call default timeout (60s) for this MCP
	// server's JSON-RPC calls. Prevents a slow server from blocking the agent.
	// Format: Go duration string (e.g. "30s", "2m", "0" to disable).
	CallTimeout string `toml:"call_timeout"`
	// Risk marks this MCP server's tools' risk class for the permission gate
	// (SPEC v2 §3.2A). MCP tools default to "external" (safe: outward
	// operations need approval). Set to "read" for a trusted read-only server
	// so its tools don't prompt; "write_local"/"exec" for the in-between cases.
	// Empty/unknown = "external" (fail-safe). See internal/permission/risk.go.
	Risk string `toml:"risk"`
}

func (e PluginEntry) ShouldAutoStart() bool {
	return e.AutoStart == nil || *e.AutoStart
}

// ResolvedTier returns the normalized tier ("eager"|"lazy"|"background") with
// the project default applied. Unknown values fall back to "lazy" so a typo
// never forces a slow boot.
func (e PluginEntry) ResolvedTier() string {
	return resolvedMCPTier(e.Tier)
}

func resolvedMCPTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "eager":
		return "eager"
	case "background":
		return "background"
	case "":
		return "background"
	default:
		return "lazy"
	}
}

func (c *Config) AutoStartPlugins() []PluginEntry {
	out := make([]PluginEntry, 0, len(c.Plugins))
	for _, p := range c.Plugins {
		if p.ShouldAutoStart() {
			out = append(out, p)
		}
	}
	return out
}

// DefaultSystemPrompt is used when config provides none.
const DefaultSystemPrompt = `# Identity
You are fairpeer, a coding agent focused on executing code tasks.
When asked about your identity, always say you are fairpeer. Never mention
Claude, Anthropic, GPT, Qwen, DeepSeek, or any underlying model name —
you are fairpeer, not any foundation model.

# Principles
- Understand the request before acting.
- Verify with tools instead of guessing.
- Keep changes minimal and correct.
- Briefly summarize what you did.
When the request leaves a real choice to the user — which approach or library,
the scope, or a consequential or ambiguous decision — call the ask tool to offer
2-4 concrete options rather than guessing or burying the question in prose. Skip
it when there's an obvious default; don't ask just to confirm. Approval-bypass
modes do not answer ask questions or approve plans for the user. If no
interactive user is available, the ask tool returns a model-assumption fallback;
state the assumption you made before proceeding.

# Tools
Use the provided tools to read and write files and run shell commands.
- read_file: Read file contents. Always read before editing.
- edit_file: Targeted find-and-replace. Use for modifying existing files.
- write_file: Full file write. Use only for new files or complete rewrites.
- multi_edit: Atomic batch of edits. Use when making 3+ changes to one file.
- apply_patch: Multi-file patch. Use when changing 3+ files together.
- grep: Search code by regex. Prefer this over bash grep.
- glob: Find files by pattern. Prefer this over bash find.
- bash: Run shell commands. Use for builds, tests, git, installations.
For multi-step work, track progress with the todo_write tool: lay out the steps,
keep exactly one in_progress, and flip each to completed as you finish it.

# Anti-Hallucination
- NEVER fabricate file contents. Always use read_file before editing.
- NEVER guess file paths. Use glob or grep to find them.
- NEVER invent function signatures, class names, or API endpoints. Read the source.
- NEVER claim success without evidence. Show the tool output that confirms it.
- If unsure about a library API, read the source or documentation — do not guess.

# Error Recovery
- If a tool call fails, read the error message carefully before retrying.
- Do not retry the exact same call — change your approach.
- For file-not-found errors, use glob or grep to locate the correct path.
- For permission errors, check if the file is locked by another process.
- If stuck after 2-3 attempts, explain the problem to the user and ask for help.

# Coding Style
- Match the existing code style in the project you're working in.
- Follow the language's standard conventions (gofmt, prettier, black, etc.).
- Add comments only when the intent is non-obvious.
- Handle errors explicitly — do not silently ignore them.
- Write tests for new functionality when the project has existing tests.

# Safety
- Do not run destructive commands (rm -rf, sudo) without explicit user request.
- Do not read or modify .env, credentials, or SSH keys unless explicitly asked.
- Do not make network requests to unexpected endpoints.

# Context Management
- Earlier messages may be summarized to stay within the context window.
- Rely on file contents and todo_write state rather than remembering what was
  discussed earlier. If you need something from earlier, re-read the relevant file.

# Plan Mode
In plan mode the harness blocks writer tools: do read-only research, then write a
concise plan as your reply and stop. The user is asked to approve before anything
is changed; once approved, work through the steps, updating the task list as you go.`

// LanguagePolicy is the auto fallback appended to the system prompt when no
// concrete UI language is resolved. It is static English text, so it stays part
// of the cache-stable prefix and avoids per-turn language injection.
const LanguagePolicy = `Reply in the same language the user is using in their most recent message: ` +
	`if they write in Chinese answer in Chinese, in English answer in English, and switch ` +
	`whenever they switch. Let this also guide the language you think in. Always keep code, ` +
	`identifiers, file paths, shell commands, and technical terms in their original form — never translate them.`

// Default returns the built-in default configuration with no providers. The
// keyless local presets (Ollama, llama.cpp) are injected by Load/LoadForEdit
// when no config file defines [[providers]]; cloud providers stay
// user-configured via the CLI setup wizard (fairpeer chat/run) or the desktop
// onboarding/settings panel. The local presets alone never suppress first-run
// onboarding (they are skipped by Configured()-gated fallbacks and onboarding
// checks).
func Default() *Config {
	return &Config{
		ConfigVersion: 2,
		DefaultModel:  "",
		UI:            UIConfig{Theme: "light"},
		Desktop:       DesktopConfig{Theme: "light", ThemeStyle: "slate"},
		Cowork: CoworkConfig{
			EmbeddingModel:    "",
			PPTActiveTemplate: "default",
		},
		Notifications: NotificationsConfig{
			Enabled:         false,
			TurnDone:        true,
			ApprovalRequest: true,
			AskRequest:      true,
		},
		Agent: AgentConfig{
			SystemPrompt: DefaultSystemPrompt,
			// 0 = no step cap: the agent loops until the model gives a final answer,
			// the user cancels, or the provider errors. Context stays bounded by
			// compaction, not by a round count. Set a positive agent.max_steps only
			// if you want a hard guard against runaway.
			MaxSteps:          0,
			PlannerMaxSteps:   12,
			AutoPlan:          "off",
			FastTaskModel:     DefaultFastTaskModel,
			SoftCompactRatio:  0.5,
			CompactRatio:      0.8,
			CompactForceRatio: 0.9,
		},
		// Mode "ask" with no rules keeps `fairpeer run` autonomous (no TTY → ask
		// resolves to allow) while `fairpeer chat` prompts before writers. Users add
		// deny/allow rules to harden or quiet specific tools.
		Permissions: PermissionsConfig{Mode: "ask"},
		// Sandbox on by default: bash is jailed (macOS), network allowed so
		// builds/downloads work. Set bash = "off" to disable. Network=true here
		// so an absent [sandbox] in a user's file keeps egress (zero value would
		// wrongly deny it).
		Sandbox: SandboxConfig{Bash: "enforce", Network: true},
		// CodeGraph is opt-in everywhere (same policy as context7): default off,
		// users enable it in Settings or via [codegraph] enabled = true. Explicit
		// config keeps the user's choice. AutoInstall fetches the runtime into
		// the cache when enabled and missing. The ONE exception is an EMBEDDED
		// build (codegraph_embed): it carries the runtime inside the binary, so
		// it defaults ON — a copied/portable exe then works with zero network and
		// zero setup. See codegraphDefaultEnabled.
		Codegraph: CodegraphConfig{Enabled: codegraphDefaultEnabled(), AutoInstall: true},
		// Background self-evolution (Dream/Distill) on by default; 7/30 day cadence.
		Dream: DreamConfig{Enabled: true, DreamInterval: DefaultDreamInterval, DistillInterval: DefaultDistillInterval, SkillColdDays: DefaultSkillColdDays},
		// [memory] defaults to "no override": the data tree stays in the OS user
		// config dir and the maintenance model falls back to fast_task_model.
		Memory: MemoryConfig{},
		// LSP tools on by default, but dormant until a language server is on PATH;
		// a missing server yields an install hint rather than an error.
		LSP:     LSPConfig{Enabled: true},
		Network: NetworkConfig{ProxyMode: netclient.ModeAuto},
		Bot: BotConfig{
			MaxSteps:   25,
			DebounceMs: 1500,
			Allowlist:  BotAllowlist{Enabled: true},
			QQ:         QQBotConfig{AppSecretEnv: "QQ_BOT_APP_SECRET"},
			Feishu:     FeishuBotConfig{Domain: "feishu", AppSecretEnv: "FEISHU_BOT_APP_SECRET", Mode: "webhook", WebhookPort: 8080, RequireMention: true},
			Weixin:     WeixinBotConfig{AccountID: "default", TokenEnv: "WEIXIN_BOT_TOKEN", APIBase: "https://ilinkai.weixin.qq.com"},
			Telegram:   TelegramBotConfig{TokenEnv: "TELEGRAM_BOT_TOKEN"},
		},
		// RPM default 60: a conservative per-key rate cap that suits most
		// OpenAI-compatible providers. Users on higher tiers can raise this.
		// ReserveMain=2 keeps requests in reserve for the main agent under
		// concurrent load.
		LLM: LLMConfig{RPM: 60, ReserveMain: 2},
		// Keyless local-model presets ship for every install: no API key, and
		// they only ever talk to this machine. Cloud providers stay unset — the
		// user configures those via setup wizard / settings panel. Defining any
		// [[providers]] in a config file replaces this list wholesale (TOML
		// array-of-tables semantics), so existing configs never see surprise
		// additions.
		// Providers stay empty here: baked-in non-nil providers would
		// field-merge with user [[providers]] entries during TOML decode
		// (array-of-tables decode merges by index). The keyless local presets
		// are injected by LoadForRoot/LoadForEdit instead — only when no config
		// file defines [[providers]].
		Providers: nil,
	}
}

// BuiltinLocalProviders returns fresh copies of the keyless local-model
// presets (Ollama, llama.cpp) that ship for every install: no API key, and
// they only ever talk to this machine. Load injects them when no config file
// defines [[providers]] — a file that does define providers replaces them
// wholesale, so existing setups never see surprise additions.
func BuiltinLocalProviders() []ProviderEntry {
	return []ProviderEntry{
		{
			Name:    "ollama",
			Kind:    "openai",
			BaseURL: "http://127.0.0.1:11434/v1",
			Models:  []string{"qwen3-coder:30b", "qwen3:8b", "deepseek-r1:32b", "llama3.3:70b"},
			Default: "qwen3-coder:30b",
			// 8192 keeps history trimming conservative: Ollama's default
			// num_ctx is 4096; raise context_window alongside OLLAMA_CONTEXT_LENGTH.
			ContextWindow: 8192,
			NoProxy:       true,
		},
		{
			Name:    "lmstudio",
			Kind:    "openai",
			BaseURL: "http://127.0.0.1:1234/v1",
			// LM Studio serves whatever is loaded; discovery replaces this.
			Models:        []string{"local-model"},
			Default:       "local-model",
			ContextWindow: 8192,
			NoProxy:       true,
		},
		{
			Name:    "llamacpp",
			Kind:    "openai",
			BaseURL: "http://127.0.0.1:8080/v1",
			// llama-server serves whatever GGUF it was loaded with and ignores
			// the model field unless routing; "local-model" is a placeholder the
			// user replaces via model auto-discovery.
			Models:        []string{"local-model"},
			Default:       "local-model",
			ContextWindow: 8192,
			NoProxy:       true,
		},
	}
}

// appendBuiltinLocalProviders adds the keyless local presets for providers not
// already present by name (a user-defined "ollama" entry always wins) and marks
// the injected names so AmbientLocalPreset can tell them apart from entries the
// user actually wrote or added.
func appendBuiltinLocalProviders(c *Config) {
	for _, b := range BuiltinLocalProviders() {
		if _, ok := c.Provider(b.Name); !ok {
			c.Providers = append(c.Providers, b)
			if c.injectedLocalPresets == nil {
				c.injectedLocalPresets = map[string]bool{}
			}
			c.injectedLocalPresets[b.Name] = true
		}
	}
}

// AmbientLocalPreset reports whether name is a keyless local preset that load
// auto-injected (no config file defines [[providers]]) and that the user has
// NOT explicitly added via desktop provider_access. Ambient presets exist so
// local models are one click away in Settings, but they must never become an
// implicit model choice: ResolveModelWithFallback skips them, and the desktop
// drops persisted tab models that resolve only to them. Clearing is opt-in per
// name — adding "ollama" leaves a sibling "llamacpp" ambient.
func (c *Config) AmbientLocalPreset(name string) bool {
	if c == nil || len(c.injectedLocalPresets) == 0 {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" || !c.injectedLocalPresets[name] {
		return false
	}
	return !desktopProviderAccessMap(c.Desktop.ProviderAccess)[name]
}

// Load builds the configuration: defaults, then user config, then project
// config, then MCP servers from Claude Code's .mcp.json, then (lowest priority)
// the v0.x ~/.fairpeer/config.json's mcpServers. A .env in the working directory
// is loaded first so api_key_env can resolve.
func Load() (*Config, error) {
	return LoadForRoot(".")
}

// LoadForRoot builds the configuration with project files resolved from root
// instead of the current working directory. When root is "" or ".", it behaves
// like Load(). This is the workspace-aware entry point: desktop tabs use it so
// each project's fairpeer.toml + .env + .mcp.json are resolved independently
// without changing the process cwd.
func LoadForRoot(root string) (*Config, error) {
	root = resolveRoot(root)
	loadDotEnvForRoot(root)
	cfg := Default()

	projectTOML := "fairpeer.toml"
	if root != "." {
		projectTOML = filepath.Join(root, "fairpeer.toml")
	}

	var tomlSources []string
	if uc := userConfigPath(); uc != "" {
		tomlSources = append(tomlSources, uc)
	}
	tomlSources = append(tomlSources, projectTOML)
	providersDefined := false
	for _, path := range tomlSources {
		if _, err := os.Stat(path); err == nil {
			if err := migrateLegacyMCPTiersFile(path); err != nil {
				slog.Warn("config: legacy mcp tier migration failed", "path", path, "err", err)
			}
		}
		defined, err := mergeFile(cfg, path)
		if err != nil {
			return nil, err
		}
		if defined {
			providersDefined = true
		}
	}
	// Defining any [[providers]] replaces the built-in keyless local presets
	// (Ollama, llama.cpp) wholesale; without user-defined providers the presets
	// apply so local models work out of the box.
	if !providersDefined {
		appendBuiltinLocalProviders(cfg)
	}
	// toml.DecodeFile replaces [[plugins]] wholesale, so cfg.Plugins now holds
	// only the last file's. Re-merge by name across all sources (later wins) so a
	// project fairpeer.toml doesn't drop the global config's MCP servers.
	plugins, err := mergeTOMLPlugins(tomlSources)
	if err != nil {
		return nil, err
	}
	cfg.Plugins = plugins

	// Claude Code's .mcp.json (project root) is read last and merged into
	// [[plugins]], so a server configured for Claude works here unchanged.
	// fairpeer.toml wins on a name collision (see mergeMCPJSON).
	mcpFile := mcpJSONFile
	if root != "." {
		mcpFile = filepath.Join(root, mcpJSONFile)
	}
	entries, err := loadMCPJSON(mcpFile)
	if err != nil {
		return nil, err
	}
	cfg.mergeMCPJSON(entries)

	// Lowest priority: the v0.x ~/.fairpeer/config.json's mcpServers, so upgrading
	// from the TypeScript line keeps MCP servers without rewriting them. Anything
	// the v2 config or .mcp.json already declared wins on a name collision.
	cfg.mergeMCPJSON(loadLegacyMCP(legacyConfigPath()))
	normalizePluginCommandLines(cfg)
	normalizeLegacyEffort(cfg)
	normalizeLegacyMCPTiers(cfg)
	normalizeLegacyProviderModels(cfg)
	normalizeLocalProviderNoProxy(cfg)
	normalizeDesktopOfficialProviderAccess(cfg)
	normalizeEffortConfig(cfg)
	normalizeCoworkDefaults(cfg)
	normalizeSMTP(&cfg.Cowork.SMTP)
	normalizeEmailAccounts(&cfg.Cowork)
	normalizePermissionDefaults(&cfg.Permissions)
	// [netdev] is a user-global security control: pin it back to the user
	// config so the project merge above cannot have injected devices/hops/
	// scopes, then validate the pinned result.
	pinNetDev(cfg)
	if err := ValidateNetDev(cfg.NetDev); err != nil {
		return nil, err
	}
	// [memory] is likewise user-global: a project file must not be able to
	// relocate the memory tree or redirect the maintenance model.
	pinMemory(cfg)
	return cfg, nil
}

func resolveRoot(root string) string {
	if root == "" || root == "." {
		return "."
	}
	return filepath.Clean(root)
}

// normalizeLocalProviderNoProxy forces no_proxy on for providers whose
// base_url points at this machine (Ollama, llama.cpp, LM Studio, …). Routing a
// loopback request through an upstream proxy breaks local inference even though
// the endpoint is otherwise correct.
func normalizeLocalProviderNoProxy(c *Config) {
	if c == nil {
		return
	}
	for i := range c.Providers {
		if c.Providers[i].BaseURL != "" && baseURLIsLoopback(c.Providers[i].BaseURL) {
			c.Providers[i].NoProxy = true
		}
	}
}

func baseURLIsLoopback(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// normalizeLegacyEffort migrates the retired effort="off" (the old
// /thinking off that disabled thinking) to the provider default, so a config
// written by an older version keeps loading instead of erroring on a value the
// provider no longer accepts.
func normalizeLegacyEffort(c *Config) {
	for i := range c.Providers {
		if strings.EqualFold(strings.TrimSpace(c.Providers[i].Effort), "off") {
			c.Providers[i].Effort = ""
		}
	}
}

// normalizeCoworkDefaults fills in cowork defaults that the user hasn't set.
// ScreenshotVLMModel is left empty when unset — there is no built-in default
// model, since the vision model depends on which provider the user configured
// (resolved in Phase 3 from the per-provider vision_model role). An empty value
// means CallVLM returns a "no VLM model configured" error until the user picks
// one in Settings. ScreenshotHotkey defaults to Ctrl+Shift+Alt+W; EStopHotkey
// defaults to Ctrl+Shift+Pause. These are only applied when the TOML didn't
// specify them (empty → default), so explicit user config always wins.
func normalizeCoworkDefaults(c *Config) {
	if strings.TrimSpace(c.Cowork.ScreenshotHotkey) == "" {
		c.Cowork.ScreenshotHotkey = "Ctrl+Shift+Alt+W"
	}
	if strings.TrimSpace(c.Cowork.EStopHotkey) == "" {
		c.Cowork.EStopHotkey = "Ctrl+Shift+Pause"
	}
}

// coworkDefaultAskRules are the coWork tools that always prompt for approval on
// first use: email_send (irreversible, outward-facing — once sent it's gone) and
// rag_delete (irreversible — a deleted knowledge base can't be restored). These
// are the narrow HITL scope from the coWork Harness security plan: browser and
// screen_* are intentionally NOT here (browser is reversible, screen_* has the
// emergency-stop hotkey), so coWork stays usable while the genuinely dangerous
// actions still ask. The user can approve-and-remember per session so it's not
// repetitive, or remove these rules in config to go fully autonomous.
var coworkDefaultAskRules = []string{"email_send", "rag_delete"}

// normalizePermissionDefaults ensures the coWork irreversible-action ask rules
// (email_send, rag_delete) are present. It ADDS any that are missing without
// touching rules the user already configured, so:
//   - A config with no [permissions] section gets the defaults.
//   - A config with a custom ask list KEEPS those rules and gains the cowork
//     ones it's missing.
//   - A user who explicitly denies/allows email_send is respected (deny has
//     higher precedence; an explicit allow rule is left as-is, but the ask is
//     still added — precedence deny > ask > allow means a user allow alone
//     would still prompt; if they truly want no prompt they should use the
//     permission UI's "remember allow" which adds to Allow, and we leave the
//     ask in place as a baseline. To fully silence, deny the ask rule in config.)
//
// We de-duplicate case-insensitively against existing rules.
func normalizePermissionDefaults(p *PermissionsConfig) {
	for _, rule := range coworkDefaultAskRules {
		if permissionRuleListHas(p.Ask, rule) {
			continue
		}
		p.Ask = append(p.Ask, rule)
	}
}

// permissionRuleListHas reports whether rules already contains target, matching
// the tool name case-insensitively (a rule may carry a subject glob like
// "email_send:example.com"; we only compare the tool-name prefix).
func permissionRuleListHas(rules []string, target string) bool {
	t := strings.ToLower(strings.TrimSpace(target))
	for _, r := range rules {
		// A rule is "ToolName" or "ToolName(subject)". Compare the tool prefix.
		tool := strings.TrimSpace(r)
		if i := strings.IndexAny(tool, "(:"); i >= 0 {
			tool = tool[:i]
		}
		if strings.ToLower(strings.TrimSpace(tool)) == t {
			return true
		}
	}
	return false
}

// mergeTOMLPlugins merges [[plugins]] across TOML sources by name (later source wins).
func mergeTOMLPlugins(paths []string) ([]PluginEntry, error) {
	var merged []PluginEntry
	index := map[string]int{}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		var f Config
		if _, err := toml.DecodeFile(path, &f); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
		for _, p := range f.Plugins {
			p, _ = NormalizePluginCommandLine(p)
			if i, ok := index[p.Name]; ok {
				merged[i] = p
				continue
			}
			index[p.Name] = len(merged)
			merged = append(merged, p)
		}
	}
	return merged, nil
}

// LoadForEdit returns a config to seed the `fairpeer setup` wizard when reconfiguring:
// the built-in defaults with the file at path (if present) decoded on top, so a
// reconfigure preserves the user's existing providers and agent settings instead
// of resetting to defaults. .env is loaded so api_key_env resolution works while
// the wizard decides which keys are still missing.
//
// Read-path resilience: when the file exists but cannot be parsed this falls
// back to defaults with a warning instead of failing. Never use it before a
// save — a defaults-shaped config written back over a file we failed to read
// destroys the user's providers and settings. Write paths must use
// LoadForEditStrict.
func LoadForEdit(path string) *Config {
	cfg, _, err := loadForEditFile(path)
	if err != nil {
		slog.Warn("config: load for edit failed, using defaults", "path", path, "err", err)
	}
	return cfg
}

// LoadForEditStrict is the write-path counterpart of LoadForEdit: it returns
// an error when the config file exists but cannot be parsed, instead of
// silently degrading to defaults. Anything that loads a config in order to
// persist it back (settings mutations, migrations, project overrides) must use
// this — failing closed beats writing a defaults-shaped config over a file we
// could not read.
func LoadForEditStrict(path string) (*Config, error) {
	cfg, _, err := loadForEditFile(path)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func loadForEditFile(path string) (*Config, bool, error) {
	loadDotEnv()
	cfg := Default()
	if _, err := os.Stat(path); err == nil {
		if err := migrateLegacyMCPTiersFile(path); err != nil {
			slog.Warn("config: legacy mcp tier migration failed", "path", path, "err", err)
		}
	}
	defined, err := mergeFile(cfg, path)
	if err != nil {
		return cfg, defined, err
	}
	if !defined {
		// Mirror Load: no user-defined [[providers]] → seed the keyless local
		// presets so the setup wizard starts from the same provider set.
		appendBuiltinLocalProviders(cfg)
	}
	normalizePluginCommandLines(cfg)
	normalizeLegacyEffort(cfg)
	normalizeLegacyMCPTiers(cfg)
	normalizeLegacyProviderModels(cfg)
	normalizeLocalProviderNoProxy(cfg)
	normalizeDesktopOfficialProviderAccess(cfg)
	normalizeEffortConfig(cfg)
	return cfg, defined, nil
}

// mergeFile decodes a TOML file onto cfg if it exists. An absent file is not an
// error. The bool reports whether the file defines [[providers]] — Load uses it
// to decide whether the built-in local presets apply.
func mergeFile(cfg *Config, path string) (providersDefined bool, err error) {
	if _, err := os.Stat(path); err != nil {
		return false, nil
	}
	md, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return false, fmt.Errorf("config %s: %w", path, err)
	}
	return md.IsDefined("providers"), nil
}

// normalizeLegacyMCPTiers keeps loaded legacy config files on the new product
// behavior: enabled MCP servers connect in the background by default, and the
// retired per-server startup tier is no longer a user-facing setting.
func normalizeLegacyMCPTiers(c *Config) {
	if c == nil {
		return
	}
	c.Codegraph.Tier = ""
	for i := range c.Plugins {
		c.Plugins[i].Tier = ""
	}
}

func migrateLegacyMCPTiersFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	next, changed := stripLegacyMCPTierLines(string(raw))
	if !changed {
		return nil
	}
	return os.WriteFile(path, []byte(next), info.Mode().Perm())
}

func stripLegacyMCPTierLines(raw string) (string, bool) {
	lines := strings.Split(raw, "\n")
	section := ""
	changed := false
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if header := tomlSectionHeader(line); header != "" {
			section = header
		}
		if (section == "codegraph" || section == "plugins") && isTOMLKeyAssignment(line, "tier") {
			changed = true
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), changed
}

func tomlSectionHeader(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return ""
	}
	if i := strings.Index(trimmed, "#"); i >= 0 {
		trimmed = strings.TrimSpace(trimmed[:i])
	}
	switch trimmed {
	case "[codegraph]":
		return "codegraph"
	case "[[plugins]]":
		return "plugins"
	default:
		return "other"
	}
}

func isTOMLKeyAssignment(line, key string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") || !strings.HasPrefix(trimmed, key) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, key))
	return strings.HasPrefix(rest, "=")
}

// normalizeLegacyProviderModels repairs provider entries written by older
// desktop builds that carried the official provider name/endpoint but omitted the
// model field. The repair is intentionally narrow: valid user-provided model
// lists are left untouched, while known official aliases get the model implied by
// their preset name so model pickers and provider validation have an option.
func normalizeLegacyProviderModels(c *Config) {
	if c == nil {
		return
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		if providerHasAnyModel(*p) {
			continue
		}
		if model := legacyOfficialProviderModel(p.Name); model != "" {
			p.Model = model
		}
	}
}

func legacyOfficialProviderModel(_ string) string {
	return ""
}

func normalizeDesktopOfficialProviderAccess(c *Config) {
	if c == nil || len(c.Desktop.ProviderAccess) == 0 {
		return
	}
	seen := desktopProviderAccessMap(nil)
	next := make([]string, 0, len(c.Desktop.ProviderAccess))
	for _, name := range c.Desktop.ProviderAccess {
		name = canonicalDesktopOfficialProviderName(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		next = append(next, name)
	}
	c.Desktop.ProviderAccess = next
	retargetDesktopOfficialRefs(c, seen)
}

// NormalizeLegacyDesktopProviderAccess seeds the desktop provider-access list
// for configs written before Settings tracked explicit provider access. Callers
// should only use this when they know the TOML did not declare provider_access;
// an explicit empty list means the user removed all access entries.
func NormalizeLegacyDesktopProviderAccess(c *Config) {
	if c == nil || len(c.Desktop.ProviderAccess) > 0 {
		return
	}
	seen := desktopProviderAccessMap(nil)
	var access []string
	add := func(name string) {
		name = canonicalDesktopOfficialProviderName(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		access = append(access, name)
	}
	addRef := func(ref string) {
		if entry, ok := c.ResolveModel(ref); ok {
			if !entry.Configured() {
				return
			}
			add(entry.Name)
		}
	}
	addRef(c.DefaultModel)
	addRef(c.Agent.PlannerModel)
	addRef(c.Agent.SubagentModel)
	addRef(c.Agent.AutoPlanClassifier)
	for _, ref := range c.Agent.SubagentModels {
		addRef(ref)
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.Configured() {
			add(p.Name)
		}
	}
	if len(access) == 0 {
		return
	}
	c.Desktop.ProviderAccess = access
	normalizeDesktopOfficialProviderAccess(c)
}

// canonicalDesktopOfficialProviderName normalizes alternative provider names.
// FairPeer ships no preset official providers, so this is currently a passthrough
// trim; the indirection is kept so future official aliases can plug in here.
func canonicalDesktopOfficialProviderName(name string) string {
	return strings.TrimSpace(name)
}

// CanonicalDesktopOfficialProviderName returns the Settings Center provider ID
// for built-in official provider aliases.
func CanonicalDesktopOfficialProviderName(name string) string {
	return canonicalDesktopOfficialProviderName(name)
}

func desktopProviderAccessMap(names []string) map[string]bool {
	out := map[string]bool{}
	for _, name := range names {
		name = canonicalDesktopOfficialProviderName(name)
		if name != "" {
			out[name] = true
		}
	}
	return out
}

func retargetDesktopOfficialRefs(c *Config, access map[string]bool) {
	c.DefaultModel = retargetDesktopOfficialRef(c.DefaultModel, access)
	c.Agent.PlannerModel = retargetDesktopOfficialRef(c.Agent.PlannerModel, access)
	c.Agent.SubagentModel = retargetDesktopOfficialRef(c.Agent.SubagentModel, access)
	c.Agent.AutoPlanClassifier = retargetDesktopOfficialRef(c.Agent.AutoPlanClassifier, access)
	for skill, ref := range c.Agent.SubagentModels {
		c.Agent.SubagentModels[skill] = retargetDesktopOfficialRef(ref, access)
	}
}

func retargetDesktopOfficialRef(ref string, _ map[string]bool) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	provider, _, _ := strings.Cut(ref, "/")
	switch provider {
	default:
		return ref
	}
}

const userDirname = "fairpeer"

// userDir returns the fairpeer user config dir (~/.config/fairpeer on Linux,
// ~/Library/Application Support/fairpeer on macOS, %AppData%/fairpeer on Windows).
// FairPeer is an independent project — no legacy data migration from upstream sources.
func userDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, userDirname)
}

func userConfigPath() string {
	dir := userDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.toml")
}

// UserConfigPath is the user-global config file (~/.config/fairpeer/config.toml),
// or "" when the user config dir can't be resolved.
func UserConfigPath() string { return userConfigPath() }

// UserCredentialsPath is the fairpeer-owned global secrets file, beside
// config.toml in the user config dir (e.g. ~/.config/fairpeer/credentials). It
// holds KEY=value lines loaded into the environment by loadDotEnv. The setup
// wizard writes API keys here, deliberately NOT named .env: keys never land in a
// project's own .env (which can't be selectively gitignored), never get
// committed, and resolve from any working directory. "" when the user config dir
// can't be resolved.
func UserCredentialsPath() string {
	dir := userDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "credentials")
}

// ArchiveDir is where compacted conversation history is archived for
// traceability (one timestamped .jsonl per compaction). Empty if the user config
// directory cannot be resolved, in which case archiving is skipped.
func ArchiveDir() string {
	dir := userDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "archive")
}

// SessionDir is where chat sessions are persisted (one .jsonl per session).
// Used by `fairpeer chat --continue` / `--resume` to find the recent ones. Empty
// if the user config dir can't be resolved — sessions then aren't saved.
func SessionDir() string {
	dir := userDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "sessions")
}

// SessionDirFor returns the session directory for a given profile. The default
// profile (empty name, or the builtin "dev"/"default") shares the top-level
// <userDir>/sessions so existing --continue/--resume history stays intact; a
// named profile (e.g. "cowork") partitions under <userDir>/sessions/<key> so its
// transcripts don't mix with the default's. Returns "" when the user dir can't
// be resolved. boot.go uses this so --profile cowork lands in its own partition.
func SessionDirFor(profile string) string {
	base := SessionDir()
	if base == "" {
		return ""
	}
	key := ProfileNameKey(profile)
	if key == "" || key == ProfileNameKey("") || key == "dev" || key == "default" {
		return base // default partition — backward compatible with pre-profile sessions
	}
	return filepath.Join(base, key)
}

// ProjectSessionDir is the per-workspace session directory the desktop sidebar
// lists: <config root>/projects/<slug>/sessions. Empty when either the config
// root or workspaceRoot doesn't resolve.
func ProjectSessionDir(workspaceRoot string) string {
	base := MemoryUserDir()
	root := strings.TrimSpace(workspaceRoot)
	if base == "" || root == "" {
		return ""
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return filepath.Join(base, "projects", WorkspaceSlug(root), "sessions")
}

// ProjectSessionDirFor returns the per-workspace session directory partitioned
// by profile: <config root>/projects/<slug>/<profileKey>/sessions. The default
// profile (empty/"dev") is backward compatible with ProjectSessionDir — it
// returns the un-profiled path. Empty when the workspace root doesn't resolve.
// Used by desktopSessionDirFor so each tab's session lands in its own profile
// partition and dev/cowork conversations don't mix.
func ProjectSessionDirFor(workspaceRoot, profile string) string {
	base := MemoryUserDir()
	root := strings.TrimSpace(workspaceRoot)
	if base == "" || root == "" {
		return ""
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	key := ProfileNameKey(profile)
	if key == "" || key == ProfileDev || key == "default" {
		// Default partition: un-profiled path (backward compatible).
		return filepath.Join(base, "projects", WorkspaceSlug(root), "sessions")
	}
	return filepath.Join(base, "projects", WorkspaceSlug(root), key, "sessions")
}

// WorkspaceSlug flattens an absolute workspace path into the directory name
// used under <config root>/projects.
func WorkspaceSlug(absPath string) string {
	return strings.NewReplacer(string(os.PathSeparator), "-", "/", "-", "\\", "-", ":", "-").Replace(absPath)
}

// CacheDir is the per-user cache root for derived/regenerable artefacts: MCP
// handshake snapshots, plugin startup-latency telemetry. Lives beside the
// existing dirs (UserConfigDir/fairpeer/...) so the whole fairpeer state tree
// shares one root the user can wipe in a single rm. Empty when the OS dir is
// unavailable — callers must tolerate that (caching is best-effort).
func CacheDir() string {
	dir := userDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "cache")
}

// MemoryUserDir returns the root of fairpeer's user DATA tree — the parent of
// the user-global fairpeer.md/AGENTS.md docs, the portrait layer (profile/),
// the auto-memory store (memory/, projects/<slug>/memory) and per-project
// session state (projects/<slug>/sessions). It is [memory] root when the user
// configured one (or $FAIRPEER_MEMORY_ROOT is set), else the OS user config
// dir based default. Empty when neither resolves, which disables user-scoped
// memory.
//
// It is deliberately NOT the same thing as userDir(): config.toml, the
// credential store and the cache stay in the OS config dir even when the data
// tree is relocated — config.toml is what defines the relocation.
func MemoryUserDir() string {
	return MemoryRoot()
}

// ConventionDirs are the parent directories scanned for agent assets (skills,
// commands), in canonical-first order. .fairpeer is ours; .agents / .agent /
// .claude let users drop in assets authored for other agent tools without moving
// files. Shared so skills (internal/skill) and commands (CommandDirs) discover
// the same set. Note: hooks are NOT scanned across these — a .claude/settings.json
// uses a different hook schema that can't be parsed as ours, so hooks stay in
// .fairpeer/settings.json (see internal/hook).
var ConventionDirs = []string{".fairpeer", ".agents", ".agent", ".claude"}

// conventionSubdirsAsc joins sub under each ConventionDir of base, in ascending
// priority (reverse of ConventionDirs) so the canonical .fairpeer ends up the
// highest-priority entry — command.Load lets a later directory win on a clash.
func conventionSubdirsAsc(base, sub string) []string {
	out := make([]string, 0, len(ConventionDirs))
	for i := len(ConventionDirs) - 1; i >= 0; i-- {
		out = append(out, filepath.Join(base, ConventionDirs[i], sub))
	}
	return out
}

// CommandDirs returns the directories scanned for custom slash commands, lowest
// priority first, so a later (more specific) directory overrides an earlier one
// on a name clash. Order: home-dir convention dirs (~/.claude/commands … ~/.fairpeer/commands),
// the legacy XDG user dir (~/.config/fairpeer/commands), then the project's
// convention dirs (.claude/commands … .fairpeer/commands). Scanning the .claude /
// .agents / .agent dirs lets commands authored for other agent tools (same .md +
// frontmatter format) work here unchanged.
func CommandDirs() []string {
	return CommandDirsForRoot(".")
}

// CommandDirsForRoot is like CommandDirs but resolves the project convention
// dirs under root instead of the current working directory. Global (home/XDG)
// dirs are unchanged — they are always user-scoped.
func CommandDirsForRoot(root string) []string {
	root = resolveRoot(root)
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, conventionSubdirsAsc(home, "commands")...)
	}
	if dir, err := os.UserConfigDir(); err == nil {
		dirs = append(dirs, filepath.Join(dir, "fairpeer", "commands"))
	}
	dirs = append(dirs, conventionSubdirsAsc(root, "commands")...)
	return dirs
}

// SourcePath returns the highest-priority config file that exists, or "" if none.
func SourcePath() string {
	return SourcePathForRoot(".")
}

// SourcePathForRoot returns the highest-priority config file that exists under
// root, or "" if none. Equivalent to SourcePath() when root is ".".
func SourcePathForRoot(root string) string {
	root = resolveRoot(root)
	projectTOML := "fairpeer.toml"
	if root != "." {
		projectTOML = filepath.Join(root, "fairpeer.toml")
	}
	if _, err := os.Stat(projectTOML); err == nil {
		return projectTOML
	}
	if uc := userConfigPath(); uc != "" {
		if _, err := os.Stat(uc); err == nil {
			return uc
		}
	}
	return ""
}

// WriteFile writes the configuration to path as annotated TOML.
func (c *Config) WriteFile(path string) error {
	return os.WriteFile(path, []byte(RenderTOMLForScope(c, renderScopeForPath(path))), 0o644)
}

// Provider returns the named provider entry.
func (c *Config) Provider(name string) (*ProviderEntry, bool) {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i], true
		}
	}
	return nil, false
}

// ResolveModel resolves a model reference to a provider entry whose Model is the
// selected model string (a copy, so the config's lists stay intact). It accepts:
//   - "provider/model" — that exact model under that provider;
//   - a provider name   — the provider's default model;
//   - a bare model name — the (first) provider that lists it.
//
// The returned entry is ready to build a provider from (NewProvider reads .Model),
// so a single "vendor with many models" entry yields one instance per model
// without duplicating base_url/api_key_env. Single-`model` entries still resolve
// by provider name, keeping older configs working unchanged.
func (c *Config) ResolveModel(ref string) (*ProviderEntry, bool) {
	if ref == "" {
		return nil, false
	}
	if access := desktopProviderAccessMap(c.Desktop.ProviderAccess); len(access) > 0 {
		ref = retargetDesktopOfficialRef(ref, access)
	}
	// "provider/model"
	if prov, model, ok := strings.Cut(ref, "/"); ok {
		if e, found := c.Provider(prov); found && e.HasModel(model) {
			cp := *e
			cp.Model = model
			return &cp, true
		}
	}
	// a provider name → its default model
	if e, found := c.Provider(ref); found {
		cp := *e
		cp.Model = e.DefaultModel()
		return &cp, true
	}
	// a bare model name → the provider that lists it
	for i := range c.Providers {
		if c.Providers[i].HasModel(ref) {
			cp := c.Providers[i]
			cp.Model = ref
			return &cp, true
		}
	}
	return nil, false
}

// ResolveModelWithFallback resolves a model reference to the canonical
// "provider/model" form used by the desktop runtime. If ref is stale or empty,
// it tries the user's configured default_model before falling back to the first
// configured provider — so preference isn't overwritten by iteration order.
// Ambient local presets (injected, never added via provider_access) never
// resolve here: an explicit CLI --model still reaches them through
// ResolveModel, but tabs and fallbacks must not land on a provider the user
// never chose.
func (c *Config) ResolveModelWithFallback(ref string) (resolvedRef string, fallback bool, ok bool) {
	ref = strings.TrimSpace(ref)
	if ref != "" {
		if e, found := c.ResolveModel(ref); found && !c.AmbientLocalPreset(e.Name) {
			return e.Name + "/" + e.Model, false, true
		}
	}
	// Before falling back to the first configured provider (which may not be the
	// user's preferred choice), try the configured default_model.  Skip when ref
	// already WAS the DefaultModel (it already failed above, so retrying won't
	// help) or when the default provider has no API key configured.
	if ref != c.DefaultModel && c.DefaultModel != "" {
		if e, found := c.ResolveModel(c.DefaultModel); found && e.Configured() {
			return e.Name + "/" + e.Model, true, true
		}
	}
	// The final loop serves two very different callers and must keep them apart:
	// an EMPTY ref (fresh install / onboarding) must never auto-select keyless
	// local presets (TestResolveModelWithFallbackNeverAutoSelectsLocalPresets),
	// while a NON-EMPTY stale ref (a persisted tab model whose provider was since
	// removed) prefers a working provider over bricking startup with "unknown
	// model" — desktop-tabs.json regularly outlives config edits. Keyless
	// entries qualify only when the user chose them: ambient injected presets
	// are skipped in both cases, so a dead tab ref surfaces as ok=false and the
	// desktop shows its Welcome state instead of silently chatting on an
	// unselected local endpoint.
	staleRef := ref != ""
	for i := range c.Providers {
		p := &c.Providers[i]
		if len(p.ModelList()) == 0 {
			continue
		}
		if p.APIKeyEnv != "" && !p.Configured() {
			continue // keyed provider without its key can't serve
		}
		if p.APIKeyEnv == "" && (!staleRef || c.AmbientLocalPreset(p.Name)) {
			continue // keyless: never on a fresh install, and ambient presets never
		}
		return p.Name + "/" + p.DefaultModel(), true, true
	}
	return "", false, false
}

// APIKey resolves the entry's API key from its api_key_env.
func (e *ProviderEntry) APIKey() string {
	if e.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(e.APIKeyEnv)
}

// Configured reports whether the provider's api_key_env is set — the same check
// Validate enforces, so pickers can filter on it.
func (e *ProviderEntry) Configured() bool {
	return e.APIKey() != ""
}

// ResolveSystemPrompt returns the system prompt, reading system_prompt_file if set.
func (c *Config) ResolveSystemPrompt() (string, error) {
	if c.Agent.SystemPromptFile != "" {
		b, err := os.ReadFile(c.Agent.SystemPromptFile)
		if err != nil {
			return "", fmt.Errorf("system_prompt_file: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if strings.TrimSpace(c.Agent.SystemPrompt) == "" {
		return DefaultSystemPrompt, nil
	}
	return c.Agent.SystemPrompt, nil
}

// Validate checks that the selected model's provider is usable.
func (c *Config) Validate(model string) error {
	e, ok := c.ResolveModel(model)
	if !ok {
		return fmt.Errorf("unknown model %q (configured: %s)", model, c.providerNames())
	}
	if e.Kind == "" {
		return fmt.Errorf("provider %q: kind is required", model)
	}
	if e.BaseURL == "" {
		return fmt.Errorf("provider %q: base_url is required", model)
	}
	if e.APIKey() == "" && e.APIKeyEnv != "" {
		return fmt.Errorf("provider %q: missing env %s", model, e.APIKeyEnv)
	}
	return nil
}

func (c *Config) providerNames() string {
	names := make([]string, len(c.Providers))
	for i, p := range c.Providers {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}
