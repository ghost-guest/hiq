# hiq 桌面端（S）实现规范

> 状态：v1
> 日期：2026-08-11
> 范围：hiq 桌面端为 linkpeer 提供桥接的**实现层**规范——模块结构、接口、数据流、安全要点、测试。
> 上游协议：[`LINKPEER_PROTOCOL.md`](./LINKPEER_PROTOCOL.md)。本规范是其 S 端实现的细化。

---

## 1. 职责边界

**S 做**：
- 持有 Controller、会话历史、API Key、文件系统（hiq 本体）。
- 维持到 K 的信令长连 WS（被动可达）。
- 响应 C 的 WebRTC 连接、握手、命令；向 C 广播 wireEvent。
- 配对管理（生成二维码 + 双向确认 + 吊销）。

**S 不做**：
- 不主动连 C（C 总是发起方）。
- 不存任何 C 的私钥（只存 C 的公钥）。
- 不向 K 上报业务/遥测。

---

## 2. 模块架构

### 2.1 新增包 `internal/mobilebridge/`

```
internal/mobilebridge/
├── bridge.go            # Bridge: 生命周期、多 C 连接管理（conns map[connId]*Conn）
├── pairing.go           # 配对状态机: 生成 code/pairId、二维码、双向确认、吊销
├── signal_client.go     # 到 K 的长连 WS 客户端（出站，穿 NAT）
├── peer.go              # pion PeerConnection、DataChannel、ICE 候选收集、UPnP
├── handshake.go         # hello_c/hello_s、X25519 派生、Finished 验证
├── frame.go             # AEAD 帧编解码、seq 计数、防重放
├── crypto.go            # Ed25519 长期密钥、X25519 临时、AES-GCM、constant-time 比较
├── command_router.go    # 命令分发 + 权限校验（每命令按 connId 的权限）
├── event_sink.go        # 实现 event.Sink，注入 tabEventSink 的转发点
├── history.go           # list_sessions / load_session（复用 desktop/sessions.go）
├── audit.go             # 审计日志（devId + 命令摘要 + 结果）
├── config.go            # 加载 [mobilebridge] toml
└── proto/
    └── types.go         # 所有 JSON 消息 struct（与 K、C 共享契约）
```

### 2.2 桌面侧改动

| 文件 | 改动 |
|---|---|
| `desktop/mobilebridge_app.go`（新建） | Wails 绑定：`MobileBridgeStartPairing/Status/Unpair/SetReadOnly/ListDevices` |
| `desktop/app.go` | `App` 持有 `bridge *mobilebridge.Bridge`；startup 初始化；beforeClose 优雅关闭 |
| `desktop/tabs.go` | `tabEventSink.Emit` 末尾加一行：若该 tab 被某 C 订阅，转发 wireEvent |
| `internal/secret/` | 新增 key 命名空间 `mobilebridge.device.{id}.priv`、`mobilebridge.peer.{id}.pub`、`mobilebridge.revoked` |
| `hiq.example.toml` | 新增 `[mobilebridge]` 段 |

---

## 3. 关键接口

### 3.1 Bridge（核心对象）

```go
type Bridge struct {
    cfg      Config
    secret   *secret.Store
    longTerm *LongTermKeys        // Ed25519，从 secret.Store 加载或生成
    signal   *SignalClient        // 到 K 的长连
    conns    map[string]*Conn     // connId → 活动连接
    tabs     map[string]string    // tabId → 订阅它的 connId（MVP 单 C/Tab；多 C 时改 set）
    audit    *AuditLog
    mu       sync.RWMutex
}

func New(cfg Config, secret *secret.Store, app *App) (*Bridge, error)
func (b *Bridge) Start(ctx context.Context) error      // 建信令长连
func (b *Bridge) StartPairing() (code, qrURL string)   // 触发配对
func (b *Bridge) ConfirmPairing(devC string) error     // 桌面用户确认（双向）
func (b *Bridge) Unpair(devC string) error             // 吊销
func (b *Bridge) Stop()                                // 优雅关闭所有连接
```

### 3.2 与 K 的接口（signal_client.go）

- 出站 WSS 连 K（PROTOCOL §4.1）：`GET /session/ws?dev=<devS>&ts=<ts>&sig=<Ed25519(L_S,devS||ts)>`。
- 维持长连（PROTOCOL §4.5）：30s WS ping + 25s 应用层 `kp`。
- 收到 C 发来的 offer/ice → 转 peer.go 处理；回 answer/ice 经 K 路由给 C。
- 重连：backoff + jitter（PROTOCOL §7.2）。

### 3.3 与 C 的接口（handshake.go + frame.go）

- DataChannel 建立后，读明文 NDJSON 握手消息（hello_c）。
- 验签、查吊销、回 hello_s、派生密钥、等 Finished。
- 之后所有读写走 `frame.go` 的 AEAD 编解码。

### 3.4 与 Controller 的接口（event_sink.go）

```go
// event_sink.go 提供 Sink，由 tabEventSink 调用
func (b *Bridge) ForwardEvent(tabId string, e event.Event) {
    b.mu.RLock(); connId := b.tabs[tabId]; b.mu.RUnlock()
    if connId == "" { return }
    conn := b.conn(connId)
    if conn != nil && conn.encrypted {
        conn.sendFrame(toWire(e))   // AEAD 加密 + DataChannel 发
    }
}

// desktop/tabs.go tabEventSink.Emit 末尾追加：
if b.app.mobilebridge != nil {
    b.app.mobilebridge.ForwardEvent(s.tabID, e)
}
```

### 3.5 与 App 的接口（command_router.go）

收到 C 命令（解密后）→ 按权限校验 → 调 App 方法：

| 命令 | App 方法 | 权限 |
|---|---|---|
| submit | `SubmitToTab` | 非 readonly |
| cancel | `CancelTab` | 非 readonly |
| approve | `ApproveTab` | 非 readonly |
| office_run | 办公工具 | 非 readonly + 路径白名单校验 |
| file_drop | 写 incoming/ | allow_file_drop + 类型白名单 |
| subscribe_tab | 更新 b.tabs | 无（只改订阅） |
| list_sessions | loadSessionTitles | 无 |
| load_session | 读 JSONL 分片 | 无 |

---

## 4. 数据流

### 4.1 事件下行（S → C）

```
Controller.Emit(e)
  → tabEventSink.Emit (desktop/tabs.go)
    → metrics / wails EventsEmit / telemetry（已有）
    → [新增] mobilebridge.ForwardEvent(tabId, e)   ← 单点注入
      → 查 b.tabs[tabId] 找订阅 connId
      → conn.sendFrame(toWire(e))
        → frame.go: AES-GCM(s2c_key, seq++) 加密
        → DataChannel.Send(密文帧)
          → pion DTLS 自动加密 → 网络
```

### 4.2 命令上行（C → S）

```
C DataChannel.Send(密文帧)
  → pion 收 → DTLS 解 → 密文帧
  → frame.go: AES-GCM(c2s_key) 解密 → 明文 NDJSON
  → command_router.Route(cmd)
    → 权限校验（按 connId 的权限）→ 拒则回 forbidden
    → 调 App.*ToTab
    → audit.go 记日志
```

---

## 5. 配置（`hiq.toml [mobilebridge]`）

```toml
[mobilebridge]
enabled          = false
signal_url       = "wss://signal.example.com"
stun_servers     = ["stun:signal.example.com:3478"]
turn_enabled     = false                     # opt-in 兜底
turn_servers     = ["turn:signal.example.com:5349"]
upnp             = true
readonly_default = false
require_approval = false
allow_file_drop  = true
allow_high_risk  = false
max_connections  = 4
log_level        = "info"
```

---

## 6. 安全要点（S 端强制）

| 项 | 实现 |
|---|---|
| 长期私钥 | `secret.Store`（DPAPI 加密），永不出端 |
| 配对双向确认 | exchange 后桌面弹指纹确认，用户点确认才存 pubC（PROTOCOL §11.2①） |
| 命令权限校验 | command_router 每命令按 connId 权限，超权 forbidden（§11.2②） |
| office_run 参数 | 模板白名单 + 路径规范（禁 `..`）+ 工作区根限定（§11.2③） |
| file_drop | 落地 `incoming/` + 类型白名单 + 真实 MIME 校验 + 不自动执行 |
| 审计日志 | audit.go 记 devId+命令+结果，脱敏，本地（§11.2④） |
| 吊销 | Unpair 删 pubC + 写 revoked 列表；握手阶段拒绝 |
| 密钥比较 | 所有签名/指纹比较用 `crypto/subtle.ConstantTimeCompare`，防时序 |
| seq 防重放 | frame.go 维护 recvMaxSeq，旧帧丢 |
| 会话密钥 | 每连接临时 X25519 派生，断即弃 |

---

## 7. 集成决策：同进程 vs 独立进程

**默认：同进程**（mobilebridge 作为 hiq 进程内的 Go package，pion 同进程）。
- 优点：简单、共享 Controller/App 对象、无 IPC。
- 风险：pion 依赖进 hiq 二进制（增大 ~8MB），可能引入 cgo 破坏单静态二进制。

**Contingency（独立进程）**：若 M0 spike 发现 pion 破坏单二进制，退回独立 `hiq-bridge` 进程，hiq 主进程通过本地 socket（gRPC/JSON-RPC）与之通信。代价是 IPC 复杂 + 多一个二进制。

**决策依据**：M0 pion spike 结果（echo DataChannel + `go build` 跨平台 + cgo check）。

---

## 8. 测试要点

| 层 | 测试 |
|---|---|
| crypto.go | Ed25519/X25519/AES-GCM 往返；constant-time 比较防时序 |
| frame.go | seq 倒退丢帧；tag 错丢帧；分方向密钥反射失败 |
| handshake.go | hello_c 验签；未配对/吊销拒绝（不回 hello_s）；Finished 错断连 |
| command_router.go | 各命令权限校验；超权 forbidden；office_run 路径注入拒绝 |
| pairing.go | 双向确认流程；code 冲突重试；指纹不符拒；吊销后再连失败 |
| Go e2e | 同进程双 goroutine 模拟 C/S，经 mock K，验证整条链路（M2 前可跑） |

---

## 9. 与其他端的一致性

| 依赖 | 一致性要求 |
|---|---|
| K 端 | WS 认证格式（devS+ts+sig）、消息路由（to 字段）、配对端点（/pair/register）字段完全一致——以 `proto/types.go` 为单一事实源 |
| C 端 | 握手消息字段（eph/nc/cid/sid/ts/sig）、帧格式（ver/seq/nonce/ciphertext/tag）、密钥派生（HKDF info 串）字节级一致 |
| 验证方式 | `protocol-compat` CI 流水线：Go/Dart 双向解析 JSON 样本，字段不一致 fail |

---

## 10. 风险与缓解

| 风险 | 缓解 |
|---|---|
| pion 引入 cgo 破单二进制 | M0 spike；contingency 独立进程 |
| tabEventSink 注入影响桌面性能 | ForwardEvent 是非阻塞（conn 满 buffer 丢帧，不阻塞 Controller） |
| 多 C 并发写 Controller 竞态 | Controller 本身串行处理（已有），不新增 |

---

## 11. v1.1 优化补充（基于复审）

### 11.1 多 C 并发与事件广播（修正 §3.1）

原 §3.1 `tabs map[string]string`（tabId→单 connId）与 PROTOCOL §15.1「一 S 多 C」冲突。修正为双向映射：

```go
type Bridge struct {
    // ...
    tabSubs  map[string]map[string]bool  // tabId → 订阅它的 connId 集合
    connTab  map[string]string           // connId → 它当前订阅的 tabId（每 C 同时只看一个 tab）
}

func (b *Bridge) ForwardEvent(tabId string, e event.Event) {
    b.mu.RLock(); subs := b.tabSubs[tabId]; b.mu.RUnlock()
    for connId := range subs {
        if conn := b.conn(connId); conn != nil && conn.encrypted {
            conn.sendFrame(toWire(e))   // 广播给所有订阅者
        }
    }
}
// subscribe_tab 命令: 更新 connTab[connId] + tabSubs（从旧 tab 移除，加到新 tab）
// 连接断开: 从两边清理
```

### 11.2 Bridge 生命周期

**启动顺序**（App.startup 阶段）：
1. 加载 `[mobilebridge]` 配置（§5）
2. 加载/生成 Ed25519 长期密钥（`secret.Store`）
3. 加载已配对设备列表 + 吊销列表（`secret.Store`）
4. 若 `enabled && 已配对数>0` → 建 K 信令长连（signal_client）
5. 注册 Wails 绑定，前端可调

**关闭顺序**（App.beforeClose）：
1. 停止接受新配对/新连接
2. 给所有活动 C 发 `{"type":"bye"}` + close DataChannel
3. 关闭信令长连
4. flush 审计日志
5. beforeClose 返回 true（让 App 退出）

**与 App 生命周期协调**：startup 同步初始化（阻塞前端 ready），beforeClose 异步等清理完成（≤5s 超时强退）。

### 11.3 错误处理与降级矩阵

| 场景 | S 响应 | C 侧 |
|---|---|---|
| 命令的 tab 不存在 | `{"type":"error","code":"tab_not_found"}` | toast"会话已关闭" |
| tab 正在重建（model switch） | `code:"tab_busy"` | C 等 1s 重试 |
| Controller 正跑 turn 时 submit | 入队（Controller 自带队列） | 正常流式回包 |
| Controller 正跑时 approve | 立即生效（approval 是独立通道） | 卡片状态更新 |
| 重复 submit（C 重发） | dedup by `cmd_id`（C 生成 UUID），二次忽略 | 透明 |
| 信令长连带断 | 标 RECONNECTING；活动 P2P 不受影响（已建立） | C 无感 |
| `max_connections` 超（第 5 个 C） | hello_c 阶段查连接数，超则直接 close，不回 hello_s | C"连接被拒" |

### 11.4 与 `internal/bot` 的关系澄清

mobilebridge **不复用** `BotGateway` 代码。"第 5 个 bot 适配器"是**概念类比**，不是代码复用：

| | bot（飞书/QQ/TG/微信） | mobilebridge |
|---|---|---|
| 语义 | IM 出站：消息→Controller→回投 IM | 远程镜像：事件流 + 双向命令 |
| 传输 | 各 IM 平台 API | WebRTC P2P + 信令 |
| 安全 | 平台 token | E2E 加密 + 配对 |
| 状态 | 每平台 session | 每连接握手 + 会话密钥 |

**共享的只有** `control.Controller` 和 `event.Event`（这是 hiq 的稳定内核契约）。所以 mobilebridge 是独立 package，与 bot 平级。

### 11.5 GCM 安全边界（连接寿命）

AES-256-GCM 用随机 12B nonce。单 key 下 nonce 碰撞概率：~2^48 次加密后达 50%（生日悖论）。取安全余量 **2^32 次加密/key**（碰撞概率可忽略）。

- 单帧平均 1KB → 2^32 帧 ≈ 4TB/连接（实际远到不了）。
- **强制不变量**：seq（uint64）到 2^32 时主动触发 **rekey**（新 X25519 ECDH，新会话密钥，seq 归零）。三端都要实现。
- 这是协议级安全边界，VERIFICATION §1 列为不变量。

### 11.6 UPnP-IGD 实现

- 库：`huin/goupnp`（纯 Go）。
- 流程：启动时探测路由器 SSDP → 若支持 IGD，请求 `AddPortMapping`（外部随机端口 → 内部 PC 端口）→ 得到一个 `srflx`-like candidate 加入 ICE。
- 超时 3s（不阻塞启动）；失败静默降级（无 UPnP 候选，靠 STUN）。
- 家宽成功率高；CGNAT 后无效（预期）。
