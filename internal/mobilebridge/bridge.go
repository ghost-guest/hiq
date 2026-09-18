package mobilebridge

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

// Bridge is the desktop-side mobilebridge entry object: it owns S's identity,
// the Pairing state machine, the SignalClient long-link to K, and the live
// Conn map. It implements SignalHandler (dispatching K messages) and exposes
// ForwardEvent for the tabEventSink injection point.
//
// Lifecycle: NewBridge → Start (connects to K) → runs until ctx done.
// The desktop layer (desktop/app.go) constructs it once at startup.
type Bridge struct {
	cfg     Config
	sPriv   ed25519.PrivateKey
	sPub    ed25519.PublicKey
	devS    string
	store   KeyStore
	exec    CommandExecutor
	pairing *Pairing
	signal  *SignalClient // 主链路：嵌入式/本地 K（LAN 模式）或公网 K（单 K 模式）
	cloud   *SignalClient // 云跳板长连（公网 K 第二链路）；nil = 未启用
	audit   *Audit

	mu       sync.Mutex
	conns    map[string]*Conn            // connId → Conn (one per C connection)
	byDev    map[string]*Conn            // devC → Conn (latest, for event routing)
	connLink map[string]*SignalClient    // connId → offer 到达的信令链路（answer/ice 原路返回）
	knockS   map[string]*net.UDPConn     // connId → 敲门用的 ICE 共享 UDP socket
	tabSub   map[string]map[string]bool  // tabId → set[connId]
	ringBuf  map[string]*tabRing         // tabId → 事件环形缓冲（resync 增量同步，§11.2）
	cloudMu  sync.Mutex                  // cloud 链路的启停互斥（SetCloudRelay 热切换）
	cloudCtx context.Context             // cloud 长连的运行 ctx（Close 用）
	cloudCancel context.CancelFunc
	ctx      context.Context

	onReady func(*Conn) // 全局 onReady hook（debug-server 注册，发测试 wireEvent）

	// resolveTab maps linkpeer's tab alias ("default"/"") to a real hiq tab
	// ID (UUID). Injected by the desktop layer; nil = use the alias as-is.
	resolveTab func(string) string
}

// tabRing 是每 tab 的事件环形缓冲（resync 增量同步用，§11.2）。纯内存，重启清空。
type tabRing struct {
	seq     uint64
	entries []ringEntry
}

type ringEntry struct {
	seq  uint64
	json []byte
}

const ringCap = 200 // 每 tab 保留最近 200 条事件

// NewBridge wires everything but does NOT connect yet (Start does that).
func NewBridge(cfg Config, sPriv ed25519.PrivateKey, sPub ed25519.PublicKey, store KeyStore, exec CommandExecutor, audit *Audit) *Bridge {
	b := &Bridge{
		cfg: cfg, sPriv: sPriv, sPub: sPub, devS: DevID(sPub),
		store: store, exec: exec, audit: audit,
		conns:    map[string]*Conn{},
		byDev:    map[string]*Conn{},
		connLink: map[string]*SignalClient{},
		knockS:   map[string]*net.UDPConn{},
		tabSub:   map[string]map[string]bool{},
		ringBuf:  map[string]*tabRing{},
	}
	b.pairing = NewPairing(cfg.SignalURL, sPub, store, audit)
	b.signal = b.newLink(cfg.SignalURL)
	return b
}

// newLink 建一条带来源标签的信令长连：消息到达时带上链路指针 + K 地址，
// answer/ice 原路返回、pair_exchange 记源。主链路（signal）与云跳板链路
// （cloud）都从这里构造。
func (b *Bridge) newLink(url string) *SignalClient {
	route := &linkRoute{b: b, url: url}
	sc := NewSignalClient(url, b.sPub, b.sPriv, route, b.audit)
	route.sc = sc
	return sc
}

// linkRoute 是挂在一条 SignalClient 上的 SignalHandler：把到达消息连同
// 来源链路转发给 Bridge.onSignalMsg。
type linkRoute struct {
	b   *Bridge
	sc  *SignalClient
	url string
}

func (l *linkRoute) OnSignalMsg(msg SignalMsg) { l.b.onSignalMsg(msg, l.sc, l.url) }

// Start connects the SignalClients to their Ks and keeps them connected.
// Blocks until ctx is done. Call in a goroutine.
func (b *Bridge) Start(ctx context.Context) error {
	b.ctx = ctx
	b.pairing.SetAutoConfirm(b.cfg.AutoConfirm)
	if b.cfg.CloudSignalURL != "" {
		b.SetCloudRelay(b.cfg.CloudSignalURL)
	}
	return b.signal.Run(ctx)
}

// SetCloudRelay 热切换云跳板长连（设置面板「公网跳板」开关）。url 为空 =
// 关闭并断开云链路，回到纯局域网/单 K 行为。turnParam 为进二维码 turn=
// 字段的凭据串（"user:pass@host:port"，空 = 不带 TURN）。
func (b *Bridge) SetCloudRelay(url string) {
	b.cloudMu.Lock()
	defer b.cloudMu.Unlock()
	if b.cloudCancel != nil {
		b.cloudCancel()
		b.cloud.Close()
		b.cloudCancel = nil
	}
	b.cloud = nil
	b.cfg.CloudSignalURL = url
	b.pairing.SetCloudRelay(url, b.turnQRParam())
	if url == "" {
		b.audit.Info("cloud_relay_off", b.devS, nil)
		return
	}
	var ctx context.Context
	if b.ctx != nil {
		ctx, b.cloudCancel = context.WithCancel(b.ctx)
	} else {
		// Start 未调（测试路径）：独立 ctx，Start 后由下一次 SetCloudRelay 接管。
		ctx, b.cloudCancel = context.WithCancel(context.Background())
	}
	b.cloud = b.newLink(url)
	b.audit.Info("cloud_relay_on", b.devS, "url", url, "turn", b.turnQRParam() != "")
	go b.cloud.Run(ctx)
}

// CloudRelayURL 返回当前云跳板配置（状态面板显示）。
func (b *Bridge) CloudRelayURL() string {
	b.cloudMu.Lock()
	defer b.cloudMu.Unlock()
	return b.cfg.CloudSignalURL
}

// CloudConnected 报告云跳板长连是否在线（状态面板）。
func (b *Bridge) CloudConnected() bool {
	b.cloudMu.Lock()
	sc := b.cloud
	b.cloudMu.Unlock()
	return sc != nil && sc.Connected()
}

// turnQRParam 从 Config 拼二维码 turn= 字段（取第一个 TURN 服务器的
// host:port + REST 凭据）。未启用 TURN 或缺凭据 → 空。
func (b *Bridge) turnQRParam() string {
	if !b.cfg.TURNEnabled || len(b.cfg.TURNServers) == 0 ||
		b.cfg.TURNUser == "" || b.cfg.TURNPass == "" {
		return ""
	}
	host := b.cfg.TURNServers[0]
	// "turn:host:port[?transport=x]" → "host:port"
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimPrefix(strings.TrimPrefix(host, "turn:"), "stun:")
	return b.cfg.TURNUser + ":" + b.cfg.TURNPass + "@" + host
}

// isLoopbackSignal：主链路是否为嵌入式/本地 K（LAN 模式）。
func isLoopbackSignal(signalURL string) bool {
	u, err := url.Parse(signalURL)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// linkIsCloud：offer 经公网路径到达（云跳板链路，或主链路本身就是公网 K）。
// 决定该连接的 ICE 配置：云路径 → STUN+TURN（跨网打洞 + 中转兜底）；
// 本地路径 → 纯 host candidate（同网直连，零云）。
func (b *Bridge) linkIsCloud(sc *SignalClient) bool {
	if sc == nil {
		return false
	}
	if sc == b.signal {
		return !isLoopbackSignal(b.cfg.SignalURL)
	}
	return true
}

// SetOnReady 注册全局 onReady hook：每个 Conn 握手完成时回调（debug-server 用它
// 在握手后发测试 wireEvent，模拟 hiq 下行）。
func (b *Bridge) SetOnReady(fn func(*Conn)) { b.onReady = fn }

// SetResolveTab injects the tab-alias resolver: linkpeer sends "default"/"" but
// hiq tabs are UUIDs. The desktop layer maps the alias to the active tab.
func (b *Bridge) SetResolveTab(fn func(string) string) { b.resolveTab = fn }

// SignalConnected reports whether the long-link to K is up. Surfaces K
// reachability in the settings panel so a stale/failed link is visible.
func (b *Bridge) SignalConnected() bool { return b.signal != nil && b.signal.Connected() }

// SignalURL returns the configured K base URL (for display in the panel).
func (b *Bridge) SignalURL() string { return b.cfg.SignalURL }

// KnockEnabled / KnockServer expose the UDP-knock config (settings panel).
func (b *Bridge) KnockEnabled() bool   { return b.cfg.UDPKnock }
func (b *Bridge) KnockServer() string { return b.cfg.KnockServer }

// StartPairing kicks off a new pairing session (Wails UI calls this).
func (b *Bridge) StartPairing() (code, qrURL string, err error) {
	return b.pairing.StartPairing()
}

// SetPairAddress 钉死二维码 relay 使用的网卡 IP（"" = 自动）。
func (b *Bridge) SetPairAddress(ip string) { b.pairing.SetPairAddress(ip) }

// ListPairNics 枚举「配对网卡」候选（含默认标记），设置面板下拉框用。
func (b *Bridge) ListPairNics() []NicInfo { return ListPairNics() }

// PendingPairings lists C's awaiting desktop confirm.
func (b *Bridge) PendingPairings() []PendingPair { return b.pairing.Pending() }

// ConfirmPairing accepts a C. Wails UI calls this after the user clicks confirm.
func (b *Bridge) ConfirmPairing(pairID string) error { return b.pairing.Confirm(pairID) }

// RejectPairing declines a C.
func (b *Bridge) RejectPairing(pairID string) { b.pairing.Reject(pairID) }

// Unpair removes + revokes a previously-paired C.
func (b *Bridge) Unpair(devC string) {
	b.pairing.Unpair(devC)
	// drop any active Conn from that device
	b.mu.Lock()
	conn := b.byDev[devC]
	b.mu.Unlock()
	if conn != nil {
		conn.close()
	}
}

// --- SignalHandler (K → S inbound) ---

// OnSignalMsg 是主链路的入口（兼容旧调用方/测试）。带来源的分发在
// onSignalMsg —— 云跳板链路也走那里。
func (b *Bridge) OnSignalMsg(msg SignalMsg) {
	b.onSignalMsg(msg, b.signal, b.cfg.SignalURL)
}

func (b *Bridge) onSignalMsg(msg SignalMsg, from *SignalClient, fromURL string) {
	b.audit.Info("signal_msg", "type", msg.Type, "from", msg.From, "to", msg.To, "connId", msg.ConnID)
	switch msg.Type {
	case "offer":
		b.handleOffer(msg, from)
	case "ice":
		b.handleICE(msg)
	case "pair_exchange":
		b.pairing.OnExchangeFrom(msg, fromURL)
	case "unavailable":
		// our target wasn't online; nothing to do (we're S, we don't initiate)
	default:
		// unknown — ignore (forward compat)
	}
}

func (b *Bridge) handleOffer(msg SignalMsg, from *SignalClient) {
	if msg.ConnID == "" {
		return
	}
	// P2-2: 入站 offer 验签（C 出站已签名，双端闭环）。明确不符 → 丢弃；
	// 无签名（旧客户端）放行——AEAD 握手才是身份硬门槛，这层是纵深防御。
	if msg.Sig != "" {
		if !b.verifyPeerSig(msg.From,
			"offer|"+msg.ConnID+"|"+msg.From+"|"+b.devS+"|"+msg.SDP, msg.Sig) {
			b.audit.Error("offer_bad_sig", msg.From, errors.New("ed25519 mismatch"))
			return
		}
	} else {
		b.audit.Info("offer_unsigned", msg.From, "")
	}
	// one Conn per connId; a SECOND offer on an existing connId is an ICE
	// restart（重协商：换网络/中转升级直连/failed 自愈）。DataChannel 与应用层
	// 加密会话不受影响（同 DTLS 证书，SCTP 保留），只换传输路径。
	b.mu.Lock()
	existing := b.conns[msg.ConnID]
	if existing == nil && b.cfg.MaxConnections > 0 && len(b.conns) >= b.cfg.MaxConnections {
		b.mu.Unlock()
		b.audit.Info("max_connections_reached", "devC", msg.From,
			"current", len(b.conns), "max", b.cfg.MaxConnections)
		return
	}
	b.mu.Unlock()
	if existing != nil {
		answerSDP, err := existing.HandleOffer(msg.SDP)
		if err != nil {
			b.audit.Error("ice_restart_offer", msg.From, err)
			return
		}
		ansSigMsg := "answer|" + msg.ConnID + "|" + b.devS + "|" + msg.From + "|" + answerSDP
		ansSig := ed25519.Sign(b.sPriv, []byte(ansSigMsg))
		_ = b.linkSend(msg.ConnID, SignalMsg{
			Type: "answer", ConnID: msg.ConnID,
			From: b.devS, To: msg.From,
			SDP:  answerSDP,
			Sig: base64.URLEncoding.EncodeToString(ansSig),
		})
		b.audit.Info("ice_restart_answered", msg.From, "connId", msg.ConnID)
		return
	}
	router := NewCommandRouter(msg.From, b.exec, b.cfg.DefaultPermissions(), b.audit)
	router.SetSubscribeHook(func(tab string) {
		origTab := tab
		if b.resolveTab != nil {
			tab = b.resolveTab(tab)
		}
		slog.Info("mobilebridge: subscribe_tab",
			"origTab", origTab, "resolvedTab", tab, "connID", msg.ConnID)
		b.setSubscription(msg.ConnID, tab)
	})
	router.SetListSessionsHook(func(sessions []SessionInfo) {
		// 回复会话列表给 C（加密 wireEvent，kind=sessions_list）。
		if conn := b.conns[msg.ConnID]; conn != nil {
			wire, _ := json.Marshal(map[string]any{"kind": "sessions_list", "sessions": sessions})
			_ = conn.SendEvent(wire)
		}
	})
	router.SetListModelsHook(func(models []ModelInfo) {
		if conn := b.conns[msg.ConnID]; conn != nil {
			wire, _ := json.Marshal(map[string]any{"kind": "models_list", "models": models})
			_ = conn.SendEvent(wire)
		}
	})
	router.SetListTemplatesHook(func(templates []TemplateInfo) {
		if conn := b.conns[msg.ConnID]; conn != nil {
			wire, _ := json.Marshal(map[string]any{"kind": "templates_list", "templates": templates})
			_ = conn.SendEvent(wire)
		}
	})
	router.SetNewTabHook(func(tabID string) {
		if conn := b.conns[msg.ConnID]; conn != nil {
			wire, _ := json.Marshal(map[string]any{"kind": "new_tab_result", "tabId": tabID})
			_ = conn.SendEvent(wire)
		}
	})
	router.SetResyncHook(func(tabID string, sinceSeq uint64) {
		if b.resolveTab != nil {
			tabID = b.resolveTab(tabID)
		}
		b.resyncTab(tabID, sinceSeq, msg.ConnID)
	})
	router.SetLoadSessionHook(func(tab string, history []map[string]any) {
		if conn := b.conns[msg.ConnID]; conn != nil {
			wire, _ := json.Marshal(map[string]any{"kind": "history_messages", "tab": tab, "messages": history})
			_ = conn.SendEvent(wire)
		}
	})
	router.SetOnErrorHook(func(code, errMsg string) {
		if conn := b.conns[msg.ConnID]; conn != nil {
			wire, _ := json.Marshal(map[string]any{"kind": "error", "code": code, "msg": errMsg})
			_ = conn.SendEvent(wire)
		}
	})
	conn, err := NewConn(b.sPriv, b.sPub, b.pairing, router, b.audit)
	if err != nil {
		b.mu.Unlock()
		b.audit.Error("newconn", msg.From, err)
		return
	}
	connID := msg.ConnID
	conn.SetOnReady(func(c *Conn) {
		b.mu.Lock()
		b.byDev[c.DevC()] = c
		b.mu.Unlock()
		// W11: 下发真实生效权限（C 端设置页只读展示；执法在本端 router）
		if permWire, err := json.Marshal(map[string]any{
			"kind": "permissions",
			"readonly": router.Perms().ReadOnly,
			"require_approval": router.Perms().RequireApproval,
			"allow_file_drop": router.Perms().AllowFileDrop,
			"allow_high_risk": router.Perms().AllowHighRisk,
		}); err == nil {
			_ = c.SendEvent(permWire)
		}
		if b.onReady != nil {
			b.onReady(c)
		}
	})
	conn.SetOnClose(func(c *Conn) {
		b.mu.Lock()
		delete(b.conns, connID)
		delete(b.connLink, connID)
		if kc, ok := b.knockS[connID]; ok {
			kc.Close() // 敲门 socket 随连接关闭
			delete(b.knockS, connID)
		}
		if c.DevC() != "" {
			if cur := b.byDev[c.DevC()]; cur == c {
				delete(b.byDev, c.DevC())
			}
		}
		b.removeSubscription(connID)
		b.mu.Unlock()
		b.audit.ConnClose(c.DevC())
	})
	b.mu.Lock()
	b.conns[connID] = conn
	b.connLink[connID] = from // answer/ice 原路返回（哪条链路来的 offer）
	b.mu.Unlock()

	// create PeerConnection with the per-link ICE servers:
	// 云路径（云跳板链路 / 主链路即公网 K）→ STUN+TURN 跨网打洞 + 中转兜底；
	// 本地路径（嵌入式 K 同网）→ 纯 host candidate，零云。
	// UDPKnock 开启时：专用 UDP socket + UDPMux 注入 ICE——敲门包从 ICE
	// 同一端口发出，打开的 NAT 映射对 ICE connectivity check 直接生效
	//（换了 socket 敲门是无效的：NAT 映射按内网五元组）。
	pcCfg := webrtc.Configuration{ICEServers: toICEServers(b.cfg, b.linkIsCloud(from))}
	if b.cfg.UDPKnock {
		uc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
		if err != nil {
			b.audit.Error("knock_listen", msg.From, err)
			conn.close()
			return
		}
		se := webrtc.SettingEngine{}
		se.SetICEUDPMux(ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: uc}))
		pc, pcErr := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(pcCfg)
		if pcErr != nil {
			uc.Close()
			b.audit.Error("newpc", msg.From, pcErr)
			conn.close()
			return
		}
		// socket 生命周期挂在 knockS map（OnClose 时统一关闭）
		b.mu.Lock()
		b.knockS[connID] = uc
		b.mu.Unlock()
		b.attachPC(msg, conn, pc, from)
		return
	}
	pc, err := webrtc.NewPeerConnection(pcCfg)
	if err != nil {
		b.audit.Error("newpc", msg.From, err)
		conn.close()
		return
	}
	b.attachPC(msg, conn, pc, from)
}

// attachPC 接上候选转发/状态回调、发 answer——knock 与普通两条 PC 创建
// 路径的公共尾部。answer/ice 经 linkSend 原路返回（offer 从哪条链路来）。
func (b *Bridge) attachPC(msg SignalMsg, conn *Conn, pc *webrtc.PeerConnection, from *SignalClient) {
	connID := msg.ConnID
	// forward our ICE candidates back to C via K（原路返回）
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		candJSON := c.ToJSON().Candidate
		// P2-2: 签名 ICE（防信令伪造）
		sigMsg := "ice|" + connID + "|" + b.devS + "|" + msg.From + "|" + candJSON
		sig := ed25519.Sign(b.sPriv, []byte(sigMsg))
		_ = b.linkSend(connID, SignalMsg{
			Type: "ice", ConnID: connID,
			From: b.devS, To: msg.From,
			Cand: candJSON,
			Sig: base64.URLEncoding.EncodeToString(sig),
		})
	})
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		b.audit.Info("ice_state", "devC", msg.From, "state", s.String())
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		b.audit.Info("pc_state", "devC", msg.From, "state", s.String())
	})
	conn.AttachPC(pc)

	answerSDP, err := conn.HandleOffer(msg.SDP)
	if err != nil {
		b.audit.Error("handleoffer", msg.From, err)
		conn.close()
		return
	}
	// P2-2: 签名 answer（防信令伪造）
	ansSigMsg := "answer|" + connID + "|" + b.devS + "|" + msg.From + "|" + answerSDP
	ansSig := ed25519.Sign(b.sPriv, []byte(ansSigMsg))
	_ = b.linkSend(connID, SignalMsg{
		Type: "answer", ConnID: connID,
		From: b.devS, To: msg.From,
		SDP:  answerSDP,
		Sig:  base64.URLEncoding.EncodeToString(ansSig),
	})
}

// linkSend 原路返回：answer/ice 从 offer 到达的信令链路发回（嵌入式 K 的
// answer 不会跑到云 K 去，反之亦然）。connId 未知时退回主链路。
func (b *Bridge) linkSend(connID string, msg SignalMsg) error {
	b.mu.Lock()
	sc := b.connLink[connID]
	b.mu.Unlock()
	if sc == nil {
		sc = b.signal
	}
	return sc.Send(msg)
}

func (b *Bridge) handleICE(msg SignalMsg) {
	// P2-2: 入站 ice 验签（假候选是信令层注入面）。明确不符 → 丢弃；
	// 无法验证/无签名放行（同 offer 的纵深防御取舍）。
	if msg.Sig != "" {
		if !b.verifyPeerSig(msg.From,
			"ice|"+msg.ConnID+"|"+msg.From+"|"+b.devS+"|"+msg.Cand, msg.Sig) {
			b.audit.Error("ice_bad_sig", msg.From, errors.New("ed25519 mismatch"))
			return
		}
	}
	b.mu.Lock()
	conn := b.conns[msg.ConnID]
	b.mu.Unlock()
	if conn == nil {
		return
	}
	_ = conn.AddICECandidate(msg.Cand)
	b.knockAt(msg.ConnID, msg.Cand)
}

// verifyPeerSig 验 C 的信令签名（P2-2）。签名串布局与 C 端出站签名同构：
// "<type>|<connId>|<devC>|<devS>|<payload>"。返回 false 仅在「明确不符」；
// pub 未知（配对确认异步进行中）放行——AEAD 握手才是身份硬门槛，这层是
// 信令伪造的纵深防御，不为时序竞争误伤正常配对。
func (b *Bridge) verifyPeerSig(devC, signed, sigB64 string) bool {
	pub, ok := b.pairing.PeerPub(devC)
	if !ok {
		b.audit.Info("sig_verify_skip_no_pub", devC, "")
		return true
	}
	sig, err := b64uAny(sigB64)
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), []byte(signed), sig)
}

// knockAt 单包敲门：收到 C 的 srflx 候选（C 的公网映射，经远程 STUN 探得）
// 时，从 ICE 共享 UDP socket 向其发 3 个敲门包。关键在从同一端口发出——
// NAT 映射按内网五元组，换 socket 敲门无效；共享后打开的映射对 ICE
// connectivity check 直接生效。对 cone NAT 有效；双对称 NAT 无解（§7）。
// 包体 = 魔数 LPKNOCK1|connId|nonce；C 的 ICE 对未知包静默丢弃，无副作用。
func (b *Bridge) knockAt(connID, candStr string) {
	b.mu.Lock()
	enabled := b.cfg.UDPKnock
	uc := b.knockS[connID]
	b.mu.Unlock()
	if !enabled || uc == nil {
		return
	}
	cand, err := ice.UnmarshalCandidate(candStr)
	if err != nil || cand.Type() != ice.CandidateTypeServerReflexive {
		return // 只敲 srflx（公网映射）；host 候选同网段无需敲门
	}
	ip := net.ParseIP(cand.Address())
	if ip == nil {
		return
	}
	addr := &net.UDPAddr{IP: ip, Port: cand.Port()}
	go func() {
		for i := 0; i < 3; i++ {
			nonce := make([]byte, 8)
			_, _ = rand.Read(nonce)
			pkt := append([]byte("LPKNOCK1|"+connID+"|"), nonce...)
			if _, err := uc.WriteToUDP(pkt, addr); err != nil {
				return // socket 已关（连接结束）
			}
			b.audit.Info("udp_knock", "connID", connID, "addr", addr.String())
			time.Sleep(250 * time.Millisecond)
		}
	}()
}

// SetKnock 运行时更新单包敲门开关/服务器（设置面板；影响之后新建的连接）。
func (b *Bridge) SetKnock(enabled bool, server string) {
	b.mu.Lock()
	b.cfg.UDPKnock = enabled
	b.cfg.KnockServer = server
	b.mu.Unlock()
}

// setSubscription records that connId wants events from tabId. Called when the
// C sends subscribe_tab. ForwardEvent consults this to route.
func (b *Bridge) setSubscription(connID, tabID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// remove from old tab
	for tab, set := range b.tabSub {
		if set[connID] {
			delete(set, connID)
			if len(set) == 0 {
				delete(b.tabSub, tab)
			}
		}
	}
	if b.tabSub[tabID] == nil {
		b.tabSub[tabID] = map[string]bool{}
	}
	b.tabSub[tabID][connID] = true
}

func (b *Bridge) removeSubscription(connID string) {
	for tab, set := range b.tabSub {
		if set[connID] {
			delete(set, connID)
			if len(set) == 0 {
				delete(b.tabSub, tab)
			}
		}
	}
}

// ForwardEvent is the injection point called by desktop/tabs.go's
// tabEventSink.Emit: it broadcasts the already-serialized wireEvent JSON to
// every Conn currently subscribed to tabId. Non-blocking — a slow/full Conn
// just drops that frame rather than stalling the Controller.
func (b *Bridge) ForwardEvent(tabID string, wireEventJSON []byte) {
	b.mu.Lock()
	subs := b.tabSub[tabID]
	conns := make([]*Conn, 0, len(subs))
	for connID := range subs {
		if c := b.conns[connID]; c != nil && c.IsEncrypted() {
			conns = append(conns, c)
		}
	}
	// 存入环形缓冲（resync 增量同步，§11.2）：重连的 C 发 resync 时从这里补拉。
	r := b.ringBuf[tabID]
	if r == nil {
		r = &tabRing{}
		b.ringBuf[tabID] = r
	}
	r.seq++
	// NR/AUDIT-4: seq 盖进事件本体——C 侧据此记录每 tab 的最新序号，
	// resync 发 sinceSeq 才能落在同一序号空间（此前 C 用本地计数器，
	// 两个空间对不上导致永远 resync_full 全量拉）。
	stamped := stampSeq(wireEventJSON, r.seq)
	r.entries = append(r.entries, ringEntry{seq: r.seq, json: stamped})
	if len(r.entries) > ringCap {
		r.entries = r.entries[len(r.entries)-ringCap:]
	}
	b.mu.Unlock()
	wireEventJSON = stamped
	slog.Info("mobilebridge: forward_event",
		"tabID", tabID, "subscribers", len(subs), "targets", len(conns), "eventLen", len(wireEventJSON))
	if len(conns) == 0 {
		slog.Warn("mobilebridge: forward_event dropped — no subscribers for tab", "tabID", tabID)
	}
	for _, c := range conns {
		_ = c.SendEvent(wireEventJSON)
	}
}

// resyncTab 处理 C 的 resync 请求（§11.2）：从环形缓冲查 sinceSeq 之后的事件，
// 够则回投 resync_delta；缓存已超（重启/超出 200 条）则 resync_full（C 重新拉全量）。
func (b *Bridge) resyncTab(tabID string, sinceSeq uint64, connID string) {
	b.mu.Lock()
	r := b.ringBuf[tabID]
	conn := b.conns[connID]
	b.mu.Unlock()
	if conn == nil {
		return
	}
	if r == nil || len(r.entries) == 0 || r.entries[0].seq > sinceSeq {
		wire, _ := json.Marshal(map[string]any{"kind": "resync_full", "tab": tabID})
		_ = conn.SendEvent(wire)
		return
	}
	delta := make([]json.RawMessage, 0, len(r.entries))
	for _, e := range r.entries {
		if e.seq > sinceSeq {
			delta = append(delta, e.json)
		}
	}
	wire, _ := json.Marshal(map[string]any{"kind": "resync_delta", "tab": tabID, "events": delta})
	_ = conn.SendEvent(wire)
}

// NotifySessionListChanged 广播 session_list_changed 给所有在线加密 Conn（方案B）。
// hiq 端 tab 增删改时调，让 linkpeer 自动刷新会话列表，无需用户手动下拉。
func (b *Bridge) NotifySessionListChanged() {
	b.mu.Lock()
	conns := make([]*Conn, 0, len(b.conns))
	for _, c := range b.conns {
		if c.IsEncrypted() {
			conns = append(conns, c)
		}
	}
	b.mu.Unlock()
	wire, _ := json.Marshal(map[string]any{"kind": "session_list_changed"})
	for _, c := range conns {
		_ = c.SendEvent(wire)
	}
}

// toICEServers builds the pion ICEServers for ONE connection.
// cloud=false（offer 经本地 K 到达，同网）：nil —— 纯 host candidate，
// 零云（局域网会话连 STUN 都不碰）。
// cloud=true（offer 经公网路径到达，跨网）：STUN 打洞 + TURN 中转兜底；
// TURN 带 REST 凭据（turnservers 配了 user/pass 才进，缺凭据的 TURN
// 会被 coturn 拒绝，宁可不配也别白试）。
func toICEServers(cfg Config, cloud bool) []webrtc.ICEServer {
	if !cloud {
		return nil
	}
	// 敲门依赖的远程 STUN 去重后并入——两端都要能探到 srflx，敲门才有目标。
	seen := map[string]bool{}
	var urls []string
	for _, s := range cfg.STUNServers {
		if !seen[s] {
			seen[s] = true
			urls = append(urls, s)
		}
	}
	if cfg.KnockServer != "" && !seen[cfg.KnockServer] {
		urls = append(urls, cfg.KnockServer)
	}
	servers := []webrtc.ICEServer{}
	for _, s := range urls {
		servers = append(servers, webrtc.ICEServer{URLs: []string{s}})
	}
	if cfg.TURNEnabled {
		for _, s := range cfg.TURNServers {
			if cfg.TURNUser != "" && cfg.TURNPass != "" {
				servers = append(servers, webrtc.ICEServer{
					URLs:       []string{s},
					Username:   cfg.TURNUser,
					Credential: cfg.TURNPass,
				})
			} else {
				servers = append(servers, webrtc.ICEServer{URLs: []string{s}})
			}
		}
	}
	return servers
}

// ErrNotStarted is returned when Bridge operations are attempted before Start.
var ErrNotStarted = errors.New("bridge not started")

var _ SignalHandler = (*Bridge)(nil)

// stampSeq 把 ring seq 写进 wireEvent JSON 的顶层 "seq" 字段（原值保留
// 兼容）。事件体 ≤32KB，chat 频率下 decode/encode 成本可忽略。
func stampSeq(wire []byte, seq uint64) []byte {
	var m map[string]any
	if json.Unmarshal(wire, &m) != nil {
		return wire // 非 JSON（不应发生）：原样转发不阻塞
	}
	m["seq"] = seq
	out, err := json.Marshal(m)
	if err != nil {
		return wire
	}
	return out
}
