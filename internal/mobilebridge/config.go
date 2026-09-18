package mobilebridge

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
)

// Config is the [mobilebridge] section of hiq.toml. The desktop
// integration layer (desktop/app.go) fills this from hiq's config load
// and passes it to Bridge — mobilebridge itself never reads TOML, keeping it
// independent of hiq's config internals. See HIQ_SPEC §5.
type Config struct {
	Enabled         bool
	SignalURL       string   // e.g. "wss://signal.example.com"
	STUNServers     []string // e.g. ["stun:signal.example.com:3478"]
	CloudSignalURL  string   // 公网跳板 K（跨网候选信令）；空 = 关（纯局域网/单 K）
	TURNEnabled     bool     // opt-in relay (default off, pure-P2P)
	TURNServers     []string
	TURNUser        string // coturn REST 凭据（use-auth-secret 模式）
	TURNPass        string
	UPnP            bool // probe router for port mapping
	ReadOnlyDefault bool // new peers default to read-only
	RequireApproval bool // mobile submits require desktop approval
	AllowFileDrop   bool // allow phone→desktop file delivery
	AllowHighRisk   bool // allow triggering shell/exec via mobile
	MaxConnections  int
	LogLevel        string
	AutoConfirm     bool // 联调：收到 exchange 立即自动确认（不等用户点允许）

	// UDPKnock 单包敲门（M3 NAT 穿透辅助，默认关）：ICE 建连前 S 从 ICE
	// 同一 UDP socket 向 C 的 srflx 公网映射发敲门包，提前打开 S 侧 NAT，
	// 让 C 的 connectivity check 能进来。对 cone NAT 有效；双对称 NAT 无解
	// （PROTOCOL §7）。KnockServer 是敲门依赖的远程 STUN 服务器——两端
	// 靠它学到各自公网映射（srflx 候选），没有它敲门无目标可敲。
	UDPKnock    bool
	KnockServer string // e.g. "stun:stun.example.com:3478"；空 = 不追加
}

// DefaultConfig matches HIQ_SPEC §5 defaults. The SignalURL placeholder
// is overridden by the QR code's relay field at pairing time per device.
func DefaultConfig() Config {
	return Config{
		SignalURL:      "wss://signal.linkpeer.app",
		STUNServers:    []string{"stun:signal.linkpeer.app:3478"},
		UPnP:           true,
		AllowFileDrop:  true,
		MaxConnections: 4,
		LogLevel:       "info",
	}
}

// ApplyKnockDefault 补齐 knock_server 智能默认（UX_ONBOARDING W4）：开了
// 单包敲门但没填 STUN 地址时，取云 K 域名拼 coturn（云跳板同机部署）。
// 没配云 K 则保持空（调用方 UI 提示需手填）。返回最终生效值。
func ApplyKnockDefault(cfg Config) Config {
	if !cfg.UDPKnock || cfg.KnockServer != "" || cfg.CloudSignalURL == "" {
		return cfg
	}
	u, err := url.Parse(cfg.CloudSignalURL)
	if err != nil || u.Hostname() == "" {
		return cfg
	}
	cfg.KnockServer = "stun:" + net.JoinHostPort(u.Hostname(), "3478")
	return cfg
}

// turnCredRe 在任意粘贴文本中提取 `user:pass@host[:port]`——user 收敛为
// URL/TOML 安全字符（时间戳凭据、base64 pass 都覆盖），多行粘贴取第一个
// 匹配（turn-cred.sh 输出里凭据串唯一）。
var turnCredRe = regexp.MustCompile(
	`([A-Za-z0-9._%+-]{1,128}):([A-Za-z0-9+/=_-]{1,256})@([A-Za-z0-9.-]+)(?::(\d{1,5}))?`)

// ParseTurnCred 从 turn-cred.sh 输出（或任意包含凭据串的粘贴文本）解析
// `user:pass@host[:port]`（UX_ONBOARDING W3）。返回归一化的 TURN 配置；
// 解析不到返回 ok=false。
func ParseTurnCred(paste string) (user, pass, host string, port int, ok bool) {
	m := turnCredRe.FindStringSubmatch(paste)
	if m == nil || m[3] == "" {
		return "", "", "", 0, false
	}
	user, pass, host = m[1], m[2], m[3]
	port = 3478
	if p, err := strconv.Atoi(m[4]); err == nil && p > 0 && p < 65536 {
		port = p
	}
	return user, pass, host, port, true
}

// PerConnPermissions is what one connected C may do, derived from Config
// defaults and per-device overrides (set via the Wails MobileBridgeSetReadOnly
// binding). command_router enforces these on every inbound command.
type PerConnPermissions struct {
	ReadOnly        bool
	RequireApproval bool
	AllowFileDrop   bool
	AllowHighRisk   bool
}

// DefaultPermissions snapshots the config-level defaults for a new peer.
func (c Config) DefaultPermissions() PerConnPermissions {
	return PerConnPermissions{
		ReadOnly:        c.ReadOnlyDefault,
		RequireApproval: c.RequireApproval,
		AllowFileDrop:   c.AllowFileDrop,
		AllowHighRisk:   c.AllowHighRisk,
	}
}
