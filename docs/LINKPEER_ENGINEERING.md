# linkpeer 工程规范

> 状态：v1 草案
> 日期：2026-08-11
> 范围：非功能需求、测试、运维监控、发布分发、配置、CI/CD、仓库结构、隐私合规——把工程维度一次写齐。
> 相关：功能见 [`LINKPEER_FEATURES.md`](./LINKPEER_FEATURES.md)；协议见 [`LINKPEER_PROTOCOL.md`](./LINKPEER_PROTOCOL.md)。

---

## 1. 非功能需求（NFR）

### 1.1 性能

| 指标 | 目标 | 测量 |
|---|---|---|
| 冷启动到首屏 | < 2s（中端 Android） | 开发者选项计时 |
| 配对完成耗时 | < 3s（扫码到 ACTIVE） | 端到端测 |
| 连接建立（同 LAN） | < 3s（App 启动到 ENCRYPTED） | 测连接自检 |
| 连接建立（跨网打洞成功） | < 8s | 同上 |
| 消息端到端延迟 | < 300ms（同 LAN）、< 600ms（跨网） | ping/pong RTT |
| 事件流渲染帧率 | 流式稳定不卡顿 ≥ 55fps | performance overlay |

### 1.2 电量

| 场景 | 目标 |
|---|---|
| 前台活跃对话 | < 5% / 小时 |
| 前台空闲保活 | < 2% / 小时 |
| 后台（Android 前台服务） | < 1.5% / 小时 |
| 后台（iOS） | N/A（MVP 仅前台） |

### 1.3 流量

| 项 | 估算 |
|---|---|
| 一次连接信令流量 | ~10 KB（offer/answer/ice 几条） |
| STUN 反射 | ~1 KB / 次 |
| 业务流 | ≈ 事件流大小（文本流式几 KB / turn，工具输出几 KB） |
| 加密开销 | ~5% 帧开销（nonce+tag） |

业务流量走 P2P，不计费到 K。

### 1.4 资源

| 项 | 目标 |
|---|---|
| 内存（C） | 常驻 < 150 MB |
| 包大小（C） | Android APK < 25 MB、iOS IPA < 30 MB（WebRTC 库占大头） |
| 桌面 S 增量 | hiq 二进制因 pion 增大 < 8 MB |
| 并发（S） | ≤ 4 个 C 连接；超过拒绝新连 |

### 1.5 可靠性

| 项 | 目标 |
|---|---|
| 可达网络下连接成功率 | > 95% |
| 平均重连次数 / 小时 | < 2 |
| 单帧加解密失败率 | < 1e-6（超出即告警） |
| K 可用性 | 99.5%（单 VPS，无状态，重启快） |

---

## 2. 测试策略

### 2.1 单元测试

| 端 | 范围 | 工具 | 覆盖率目标 |
|---|---|---|---|
| **K（Go）** | 配对状态机、限速、路由、签名转发 | `go test` | > 80% |
| **S（Go）** | 握手、帧编解码、seq 防重放、吊销、命令路由、tabEventSink 注入 | `go test` | > 80% |
| **C（Dart）** | 协议消息编解码、加密、状态机、UI widget | `flutter test` | > 70% |

关键单测清单：
- 配对：成功 / 码错 / 码过期 / 失败 5 次锁 / 指纹不符拒。
- 签名：SDP 篡改后验签失败丢帧。
- 握手：hello_c 签名错→S 不回；Finished 错→断。
- 帧：seq 倒退丢帧；tag 错丢帧；分方向密钥反射失败。
- 命令：未授权 deviceId 的命令被拒；订阅路由正确。

### 2.2 集成测试

- K + S 同机：起 K，S 连 K 注册，模拟 C exchange → 验证 offer/answer 透传。
- S + mock C：跑握手 + 帧往返 + ping/pong。
- 全 Go 端到端（无 Flutter）：`internal/mobilebridge` 起两个 goroutine 模拟 C/S，经 mock K，验证整条链路。这是**最早能跑的 e2e**，M2 前就有。

### 2.3 端到端（真 P2P）

P2P 最难测。分层：
- **同进程双 PeerConnection**：pion 支持两个 PC 直连（不走网络），验证 DataChannel + 握手 + 帧。CI 可跑。
- **同 LAN 双设备**：S 在 PC、C 在手机，同一 WiFi。手动 + nightly lab。
- **跨网打洞**：手机切 4G，连家宽 S。手动 + 内部 dogfood；CI 难，靠日志诊断脚本。
- **对称 NAT 模拟**：用 `tc`/netem 在测试机模拟对称 NAT，验证"打不通→优雅失败"路径。

### 2.4 安全测试

- **Fuzz**：握手消息、帧字节、签名输入——`go test -fuzz`、Dart fuzz。
- **重放**：录帧重发，验证 seq 拒绝。
- **MITM 模拟**：中间改 SDP/公钥，验证签名校验拒绝。
- **配对码爆破**：脚本批量试码，验证限速 + 锁定。
- **吊销**：解绑后该设备连不上。

### 2.5 CI 矩阵

| 流水线 | 触发 | 内容 |
|---|---|---|
| `hiq-ci` | push/PR | `go test ./...` + `go build` 跨平台 + golangci-lint |
| `linkpeer-ci` | push/PR | `flutter test` + `dart analyze` + `flutter build apk --debug` |
| `protocol-compat` | release | 跑 Go-Dart 协议消息兼容表 |
| `release` | tag | 出 hiq 二进制（含 mobilebridge）+ Android APK + K docker image |

---

## 3. 运维与监控

### 3.1 K 监控指标（Prometheus，`/metrics` 暴露）

| 指标 | 类型 | 说明 |
|---|---|---|
| `linkpeer_peers_online` | gauge | 当前在线 devId 数（S + C） |
| `linkpeer_pairs_active` | gauge | 进行中配对数 |
| `linkpeer_pair_total{result}` | counter | 配对结果（success/fail/expire） |
| `linkpeer_ws_connections` | gauge | 活跃 WS 数 |
| `linkpeer_ws_msgs_total{type}` | counter | 转发的 offer/answer/ice 计数 |
| `linkpeer_signal_errors{code}` | counter | 4401/4402/429 等 |
| `linkpeer_ratelimit_hits{dim}` | counter | 限速命中 |
| `linkpeer_uptime_seconds` | gauge | 进程运行时长 |

K 自身也暴露 `coturn` 的 STUN 请求 QPS（coturn 有 stats 接口）。

### 3.2 K 告警阈值

| 条件 | 级别 |
|---|---|
| K 5 分钟内 `signal_errors` 环比 +200% | warn |
| `/healthz` 连续 3 次失败 | critical |
| `pair_total{result=fail}` 10 分钟 > 100 | warn（疑似爆破） |
| CPU > 80% 持续 5 分钟 | warn |

### 3.3 S 端诊断（桌面 hiq）

mobilebridge 在设置页暴露：
- 当前状态（OFFLINE / ONLINE / RECONNECTING）。
- 长连 WS 延迟。
- 各 C 连接状态 + ICE 候选类型（host/srflx）。
- 最近 N 条协议事件日志（带级别，可复制）。
- "导出诊断包"按钮（脱敏日志，见 §7）。

### 3.4 C 端"为什么连不上"自检

设置 → 诊断，逐项绿/红 + 建议：

```
✓ App 能访问信令服务（GET /healthz）
✓ STUN 反射成功（拿到公网 IP:port）
✗ 信令上能找到桌面端（→ 桌面离线？开 hiq）
✗ ICE 候选仅 host（→ 都在 NAT 后，切同 WiFi 或配公网）
✓ 握手密钥校验通过
```

这是把"纯 P2P 打不通"的排查路径交给用户，回应"失败即断"决策的体验问题。

### 3.5 日志脱敏原则

所有端日志**不得记录**：
- 长期私钥、会话密钥、临时密钥。
- 配对码明文（只记前 2 位 + 长度）。
- SDP/ICE 明文（含公网 IP，敏感）——只记 `candidate_type`。
- 任何 wireEvent 业务内容、命令内容、文件内容。
- sessionToken 全文（只记前 8 位）。

可记录：devId（公钥派生，本就公开）、消息类型/大小、时间戳、错误码、连接状态转换。

---

## 4. 发布与分发

### 4.1 版本号

- **三端独立语义化版本**：`hiq` / `linkpeer` / `linkpeer-signal` 各自 `MAJOR.MINOR.PATCH`。
- **协议版本 `ver` 独立**（当前 1），与三端 app 版本解耦。协议 ver 升 = 不兼容，需三端协同发布。
- 客户端在 ClientHello 里报 `ver`，不匹配 → 升级提示。

### 4.2 hiq 桌面端发版（含 mobilebridge）

- **feature flag**：`hiq.toml [mobilebridge] enabled = false` 默认关。
- 灰度：先在 dev 构建开 → beta 用户开 → 稳定版默认开。
- mobilebridge 不开时，pion 依赖仍打进二进制但零运行开销（不初始化）。
- 用户从 hiq v0.2.x 起获得移动端能力（具体版本号随发布定）。

### 4.3 linkpeer Android 发布

| 渠道 | 方式 |
|---|---|
| 内测 | debug APK 直接发，签名用 debug keystore |
| 公测 | Google Play 内部测试 → 封闭测试 → 开放 |
| 正式 | Play Store 正式版 |
| 旁路（国内） | 官网发 APK（自签名 release keystore），不依赖 Play |

签名：release 用专用 keystore（密钥存 1Password/硬件设备，备份冗余）。

### 4.4 linkpeer iOS 发布（Mac 可用后）

- Apple Developer $99/年。
- TestFlight 内测 → App Store。
- 合规：`ITSAppUsesNonExemptEncryption = NO`（仅本地端到端加密，免出口申报）。

### 4.5 K 升级策略

K **无状态**，滚动重启不影响客户端——重启期间在连 WS 断，两端 backoff 自动重连。升级步骤：
1. 部署新版 image。
2. `docker-compose up -d signal`（仅重启 signal，coturn/caddy 不动）。
3. 客户端自动重连。
回滚同理。K 不需要停机窗口。

### 4.6 灰度 / 回滚

- linkpeer 移动端：Play Store 分阶段发布（10% → 50% → 100%）。
- hiq：自有更新通道，可按比例推送。
- 出问题：停止灰度 + 推修复版 + 必要时协议 ver 协调。

---

## 5. 配置管理

### 5.1 `hiq.toml [mobilebridge]` 完整字段

```toml
[mobilebridge]
enabled          = false                    # 总开关，默认关
signal_url       = "wss://signal.example.com"
stun_servers     = ["stun:signal.example.com:3478"]
upnp             = true                     # 桌面探测 UPnP 自动打洞
readonly_default = false                    # 新配对设备默认只读
require_approval = false                    # 移动操作默认需桌面批准
allow_file_drop  = true                     # 允许手机投文件
allow_high_risk  = false                    # 允许触发高危工具
max_connections  = 4                        # 同时最多几个 C
log_level        = "info"                   # mobilebridge 模块日志级别
```

加载：复用 hiq 现有 config 加载链路（`internal/config`）。

### 5.2 linkpeer 配置

| 项 | 来源 | 默认 |
|---|---|---|
| 信令地址 | **扫码时带**（二维码 `relay`）+ 持久化到该桌面记录 | 无硬编码默认 |
| 兜底信令 | App 内置一个默认（官方公开信令） | `wss://signal.linkpeer.app`（占位） |
| STUN | 同信令一起下发（`signal.toml [stun]`） | 跟信令 |
| 主题 | 系统 / 浅 / 深 | 系统 |
| 字体缩放 | 系统 | 系统 |

二维码带信令地址的好处：用户自建 K 也能用，不锁官方。

### 5.3 K 配置

见 PROTOCOL §10.4 `signal.toml`，已完整。

---

## 6. 仓库结构与共享契约

### 6.1 仓库决策

**monorepo 子目录**（推荐），对称于 `desktop/`：

```
hiq/                       # 现有仓库
├── internal/mobilebridge/      # 桌面侧桥接（Go）
├── cmd/linkpeer-signal/        # 信令服务（Go）
├── desktop/                    # 现有桌面端
├── mobile/linkpeer/            # 新增：Flutter 工程
├── docs/LINKPEER_*.md          # 规划文档
└── ...
```

理由：
- Go 端（S + K）共享 `internal/mobilebridge/proto` 协议包，同仓库改动原子。
- Dart 端（C）虽不同语言，但放同仓库方便协议对齐、CI 一起跑、文档同源。
- 对称 `desktop/` + `mobile/` 结构清晰。

### 6.2 协议契约同步

**问题**：Go struct 和 Dart class 容易漂移。

策略：
- **单一事实源**：`internal/mobilebridge/proto/types.go` 定义所有消息 struct + 字段文档。
- **Dart 手写镜像**：`mobile/linkpeer/lib/data/models/`，每个 class 注释指向 Go 对应 struct。
- **CI 校验**：`protocol-compat` 流水线跑一个测试：从 JSON 样本双向解析（Go 和 Dart），对比字段，不一致 fail。
- 后期可选：用 `quicktype` 或 protobuf 自动生成，但 MVP 手写 + CI 校验够。

### 6.3 Go/Dart 消息字段命名

Go `json:"camelCase"` tag ↔ Dart 字段同名，确保 JSON wire 格式两端一致。例：
```go
type ClientHello struct {
    Ephemeral string `json:"eph"`
    Nonce     string `json:"nc"`
    ...
}
```
```dart
class ClientHello {
  final String eph, nc, ...;
  Map<String, dynamic> toJson() => {"eph": eph, "nc": nc, ...};
}
```

---

## 7. 隐私与合规

### 7.1 数据流向（公开给用户）

```
C(手机)
 ├─ 私钥：本地 Keychain/Keystore，不出端
 ├─ 信令：经 K（公网）—— 仅配对码/公钥指纹/SDP/ICE 签名信令
 ├─ 业务：P2P 到 S（桌面）—— 端到端加密，不经 K
 └─ 日志：本地，可导出诊断包（脱敏）

K(信令)
 ├─ 内存表，重启清空
 ├─ 不存业务、不存密钥、不存 SDP 明文
 └─ 日志：仅元数据（devId/时间/错误码）

S(桌面)
 ├─ 私钥：secret.Store（DPAPI 加密）
 ├─ 业务：本地处理，不外发
 └─ 不向 K 或 C 上报遥测（默认关，用户可开匿名统计）
```

### 7.2 App Store / Play Store 隐私问卷

- **不采集数据**：linkpeer 默认不上报任何遥测。
- 收集类型：无（或仅"崩溃日志"且用户主动发送）。
- 第三方 SDK：flutter_webrtc（开源）、mobile_scanner、riverpod 等均为开源库，不含广告/追踪 SDK。
- 加密：端到端，`ITSAppUsesNonExemptEncryption = NO`。

### 7.3 法规

- 个人信息保护法（中国）/ GDPR：因不采集，无需特别处理；隐私政策仍需写明"不采集"。
- 开源协议：linkpeer 代码拟用与 hiq 同 license（见 LICENSE）。
- 第三方库 license 清单：发布前 `flutter_oss_licenses` 生成。

---

## 8. 风险登记

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| pion/webrtc 引入 cgo 破坏 hiq 单二进制 | 中 | 高 | M0 前 spike，若破则评估备选（如独立 bridge 进程） |
| 双对称 NAT 打不通率高 | 高 | 中 | 多 STUN + UPnP + 同 LAN + 清晰失败 UX；留 TURN 接缝 |
| iOS 后台保活不达标 | 高 | 中 | MVP 仅前台，文档明示；后期 APNs |
| Go/Dart 协议漂移 | 中 | 中 | CI 协议兼容测试 |
| 信令 K 被墙 | 低（自建） | 高 | 可换 K 域名/IP；二维码带 relay，灵活切 |
| 公网 STUN 不稳 | 中 | 低 | 自建为主，公共兜底 |
| hiq 二进制变大影响分发 | 低 | 低 | pion 增量 < 8MB，可接受 |

---

## 9. 里程碑 Definition of Done

| 里程碑 | DoD |
|---|---|
| **M0** | K 单测过、docker-compose 起来、`/healthz` 绿、STUN 反射可用、限速生效 |
| **M0 spike** | pion 加进 hiq `go.mod`、echo DataChannel 跑通、`go build ./...` 跨平台纯 Go 不破 |
| **M1** | S 端配对+信令长连+握手+帧单测全过；Go e2e（双 goroutine 模拟 C/S）通 |
| **M2** | 同 WiFi Android 真机 ↔ hiq 桌面，对话流端到端通；加密帧验证 |
| **M3** | 跨网（4G ↔ 家宽）打洞成功率统计；失败 UX 全套；ICE restart 验证 |
| **M4** | 对话 Tab 全功能（豆包样式）在 Android 可用；历史回看；审批交互 |
| **M5** | 办公 Tab 触发桌面、结果预览 |
| **M6** | iOS 首次 `build ipa`、上架材料齐、TestFlight 内测 |

---

## 10. 容量规划（K，5000+ S 并发规模）

> 前提：K 承担至少 5000 台 hiq 桌面端的敲门。这改变了 §1.4 的"单 VPS 1C1G"假设——本节是规模化的工程参数。

### 10.1 并发推算

| 项 | 数 |
|---|---|
| 常驻 WS（S 长连） | 5000 |
| 峰值 C WS（按需，~50% S 同时有 C 连） | ~2500 |
| **总并发 WS** | **~7500** |
| signal 进程 fd | ~7500 |
| Caddy 反代 fd（双倍） | ~15000 |

### 10.2 资源配置

| 配置 | 能撑 | 说明 |
|---|---|---|
| 2C4G | 5000 并发 WS | 紧但可行（起步最低） |
| **4C8G** | 5000 + 余量 | **推荐** |
| 8C16G | 15000+ | 留垂直扩展空间 |

**必须的系统调优**：
```bash
# /etc/security/limits.conf 或 systemd unit
*  soft  nofile 65535
*  hard  nofile 65535
# systemd: LimitNOFILE=65535 (signal + caddy 都设)
# Linux 默认 1024 fd，5000+ WS 必爆
```

Go runtime：goroutine per WS 2 个（读+写），7500 × 2 = 15000 goroutine，无压力。

### 10.3 流量估算

| 项 | 估算 |
|---|---|
| S 长连保活（kp 25s） | ~1.7 GB/天 |
| P2P 信令转发（offer/answer/ice） | ~125 MB/天 |
| STUN（coturn 自身） | 几 GB/天 |
| **月出口** | < 100 GB |

### 10.4 K 无状态为何不影响持久配对（关键澄清）

配对的持久关系存在**两端本地**：
- S 的 `secret.Store` 持久存 `pubC`
- C 的 Keychain/Keystore 持久存 `pubS`

握手靠两端本地公钥验签，**不查 K 的 paired 表**。所以：
- K 重启清空内存表（peers/paired/pairs）→ **不影响任何已配对用户**。
- 仅影响：当前活动 WS（断后 backoff 重连）+ 60s 内 in-flight 的配对（重做）。
- **K 是「会话路由器」而非「配对注册中心」**，所以无状态可行，不需要数据库。

这是单实例 + 快速恢复能撑大场面的设计精髓。

### 10.5 配对码唯一性

6 位 base32（~29 bit）在 5000 用户规模：
- 60s 窗口内并发 active pair 实际很低（~10 个），生日碰撞概率极低。
- 但 K **必须做唯一性检查**：`/pair/register` 时 code 冲突 → 返回 409，S 端重新生成。
- pairId（128bit）是真主键，不碰撞。

### 10.6 限速策略调整（防误伤共享 NAT）

5000 用户中，某些公司 / 学校会共享一个公网 IP（如 100 台 hiq 同出口）。旧"每 IP 20/小时"会误伤。调整为主按 devId：

| 维度 | 上限 |
|---|---|
| 每 devId `/pair/*` | 5/小时 |
| 每 IP `/pair/*` | 200/小时（仅防明显洪泛） |
| 单 pairId 失败 | 5 次锁（不变） |
| 每 devId WS 消息 | 50/s |
| 全局 active pair 上限 | 50000 |

### 10.7 连接风暴防护

K 重启 = 5000 S 同时断 + 同时重连。客户端 backoff **必须加 jitter**（随机抖动 ±50%），打散羊群效应。例：base 2s → 实际 1–3s 随机。重启选低峰时段。多实例后用滚动重启。

### 10.8 冗余与故障切换

5000 用户依赖单 K，要冗余。但**双活需 Redis 路由层（5000 规模不值得）**。

- **同区域 standby 实例**：不日常承载，主挂时 DNS 切换（TTL 60s）。
- 客户端 backoff + jitter 已有，DNS 切换后自动重连 standby。
- RTO ≈ 90s（DNS 60s + 重连 30s）。
- K 无状态 → standby 无需数据同步，预热快。

### 10.9 DDoS 防护

5000 公网用户规模，K 是显眼目标：
- **Cloudflare 前置（推荐）**：CF 支持 WebSocket（仪表盘需开 WebSocket 开关）。免费计划够。隐藏源 IP，吸收 L3/L4 DDoS，CF Rate Limiting 防配对端点刷量。
- 或裸 VPS + Caddy + fail2ban + 严格限速（次选）。
- 配对端点（`/pair/*`）无认证、最暴露：CF Rate Limit + K 限速双保险。

### 10.10 演进路径（不要 Day 1 过度设计）

| 阶段 | 规模 | 方案 |
|---|---|---|
| **现在** | ≤ 5000 | 单实例 4C8G + standby + DNS 切换 |
| 中期 | 5000–2万 | 垂直扩到 16C32G（单实例仍够） |
| 长期 | >2万 | 多实例 + Redis pub/sub 路由 + L4 LB |

Redis 路由方案（长期）：
```
peers: devId → instanceId 存 Redis 哈希
跨实例消息：publish 到目标 instanceId 的 channel
L4 LB（HAProxy）分发 WS，无 sticky 需求（路由靠 Redis）
```
5000 规模不上 Redis，徒增复杂度与故障点。

### 10.11 监控加强（5000 规模必须有）

§3.1 的 Prometheus 指标基础上，加仪表盘 + 告警：
- **Grafana 仪表盘**：在线 S/C 趋势、WS 并发、消息 QPS、配对成功率、资源使用。
- **告警**：WS 并发突降（可能 K 故障）、配对失败率飙升（可能爆破或 bug）、CPU > 70%、内存 > 80%、STUN 5xx。
- **日志聚合**：可选（Loki/ELK），但 K 日志量小，本地轮转 + 按需 grep 也够。
