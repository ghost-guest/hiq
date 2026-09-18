# linkpeer × hiq 移动端方案（决策锁定版）

> 状态：方案确定，待 M0 启动
> 日期：2026-08-11
> 关联代码：`internal/bot`、`internal/serve`、`internal/event`、`desktop/app.go`、`desktop/tabs.go`、`desktop/sessions.go`、`internal/secret`
> 产品入口：[`../LINKPEER.md`](../LINKPEER.md)

---

## 0. 产品定位

**linkpeer** = hiq 的移动伴侣端（Android / iOS，Flutter）。hiq 桌面端跑模型、读写文件、做办公自动化；linkpeer 在手机上实时镜像对话、远程批操作、触发办公任务。两端点对点直连，云端只做敲门，业务流量全程端到端加密。

| | hiq（桌面） | linkpeer（移动） |
|---|---|---|
| 角色 | 服务 peer | 连接 peer |
| 持有 | Controller、会话历史、API Key、文件系统 | 仅本地私钥 + 会话缓存 |
| 职责 | 跑模型、执行工具、办公自动化 | 镜像事件流、下发命令、查看结果 |
| 平台 | Windows / macOS / Linux（Wails） | Android / iOS（Flutter） |

### 0.1 命名约定

- **linkpeer**：移动端产品名、Flutter 仓库名、移动端 package 前缀。
- **hiq**：桌面端，不变。
- **mobilebridge**：hiq 桌面端内部为 linkpeer 提供桥接的 Go package（`internal/mobilebridge`），是 hiq 的改动，不是 linkpeer 的一部分。
- 协议层中立命名：双方交换的消息用 `wireEvent` / `cmd.*`，不含任一产品名。

---

## 1. 决策记录（已锁定）

| 决策点 | 选择 | 含义 |
|---|---|---|
| 打洞兜底 | **纯 P2P，失败即断** | 不部署 TURN；对称 NAT 双端打不通时连接失败，UX 引导切网络 |
| 传输方案 | **WebRTC + 自建信令** | `flutter_webrtc`（移动）+ pion/webrtc（桌面）+ 自建轻量信令（仅敲门） |
| 配对方式 | **扫码配对** | 桌面二维码 + 6 位配对码 + Ed25519 指纹本地校验 |
| 移动平台 | **Android + iOS** | 同一 Flutter 代码库 |

### 1.1 关于「纯 P2P」的可达性实话（对应需求点 6）

纯 P2P（STUN 辅助打洞，无中继）的可达性由两端 NAT 类型决定：

| 桌面端 NAT | 移动端 NAT | 结果 |
|---|---|---|
| 公网 IP / 端口转发 | 任意 | ✅ 直连 |
| Cone NAT（多数家宽） | Cone NAT | ✅ 打洞成功 |
| Cone NAT | 对称 NAT（移动 4G 常见） | ⚠️ 多数可打 |
| 对称 NAT | 对称 NAT | ❌ 打不通 → 失败提示 |

需求点 6 的「双端都在中国移动大 NAT」最坏场景，**在纯 P2P 约束下无法保证连通**——届时优雅失败并提示用户切到可达网络。这是 NAT 行为的物理限制，非实现问题。

把无中继成功率压到上限的措施（全部不引入中继流量）：

1. **多 STUN 源**：公共 STUN（`stun.l.google.com` 等）+ 自建 STUN-only 服务。
2. **UPnP-IGD 主动打洞**：桌面端探测路由器 UPnP，自动映射端口（家宽场景成功率可观；CGNAT 后无效）。
3. **对称 NAT 端口预测**：best-effort，不保证。
4. **同 LAN 直连**：两端同 WiFi 走 host candidate，100% 可达、零依赖。

> 架构上保留「可选自建 TURN」的干净接缝（仅 ICE 配置切换即可启用），默认不部署、不启用——尊重决策，同时不在未来把自己锁死。

---

## 2. 架构总览

**核心定位：linkpeer = 第 5 个 bot 适配器（远端镜像语义）。** hiq 已有 Feishu/QQ/TG/WeChat 四个 IM 适配器跑通「远端用户 → `BotGateway` → `Controller` → 事件回投」闭环；linkpeer 是同构问题。关键基础设施已具备：

- `desktop/tabs.go` 的 `tabEventSink.Emit` 是**单点注入**：已把事件分发给 metrics、wails `EventsEmit`、telemetry。linkpeer 只需在此加一行「转发给 mobilebridge（若该 tab 被移动端订阅）」。**不改 Controller。**
- `App.SubmitToTab` / `App.CancelTab` / `App.ApproveTab` / `tabByIDLocked` 已为多 tab 做好——上行命令纯转发。
- `serve.toWire` / `wireEvent` 已是稳定 JSON 契约——下行直接复用。
- `secret.Store`（DPAPI 加密）可存设备配对密钥。

真正要新建的只有：**① 桌面桥接（`internal/mobilebridge` + pion/webrtc）② linkpeer Flutter App ③ 云端轻量信令**。hiq 业务逻辑层零新增。

### 数据流

```
                    ┌─────────────────┐
                    │  云端信令（敲门） │  仅：配对码 / 公钥指纹 / ICE 候选
                    └────────┬────────┘
              配对+候选交换    │    配对+候选交换
        ┌─────────────────────┴─────────────────────┐
        ▼                                             ▼
┌────────────────────┐  WebRTC DataChannel（E2E 加密） ┌────────────────────┐
│  hiq 桌面      │ ◄════════════════════════════► │  linkpeer 移动      │
│  Controller        │   下行：wireEvent（复用 SSE 流） │  Flutter UI         │
│  tabEventSink +mobilebridge │ 上行：cmd.*（submit…） │  Android / iOS      │
│  secret.Store      │                                   │  Keychain/Keystore  │
└────────────────────┘                                   └────────────────────┘
```

---

## 3. 公平分工：双端改动清单

### 3.1 hiq 桌面端改动（Go + Wails）

| 文件/包 | 改动 | 性质 |
|---|---|---|
| **`internal/mobilebridge/`**（新建） | 桥接核心：`Bridge`（设备+连接生命周期）、`eventSink`（实现 `event.Sink`，写 DataChannel）、`commandRouter`（分发 `cmd.*` 到 `App.*ToTab`）、`pairing`、`crypto`、`webrtc`（pion）、`history`（复用 `desktop/sessions.go`） | 新建 |
| **`desktop/tabs.go`** | `tabEventSink.Emit` 末尾加一行：若该 `tabID` 被某 linkpeer 连接订阅，转发 `wireEvent` 给 `mobilebridge`。新增字段记录「该 tab 是否被远程订阅 + 订阅者连接 ID」 | 微改（单点注入，~10 行） |
| **`desktop/mobilebridge_app.go`**（新建） | Wails 绑定：`MobileBridgeStartPairing() / MobileBridgeStatus() / MobileBridgeUnpair(id) / MobileBridgeSetReadOnly(id,ro) / MobileBridgeListDevices() / MobileBridgeSetTabSubscription(connID, tabID)` | 新建 |
| **`desktop/app.go`** | `App` 持有 `mobilebridge *Bridge`；`startup` 初始化；`beforeClose` 优雅关闭；`createTabEntry` 时把 sink 接好 | 微改 |
| **`internal/secret/`** | 新增 key 命名空间 `mobilebridge.device.{id}.priv`（Ed25519 长期私钥）、`mobilebridge.peer.{id}.pub`（已配对端公钥） | 复用，无代码改动 |
| **`go.mod`** | 加 `github.com/pion/webrtc/v4`、`github.com/pion/datachannel`、`golang.org/x/crypto`（curve25519/aes） | 新依赖（需 spike 验证无冲突） |
| **`hiq.example.toml`** | 新增 `[mobilebridge]` 段：`enabled`、`signal_url`、`stun_servers`、`upnp`、`readonly_default`、`require_approval` | 配置 |
| **`desktop/frontend/`** | 设置页加「移动端」面板：配对二维码、已配对设备列表、只读开关、连接状态；通知栏显示「linkpeer 已连接」 | 新建组件 |
| **`internal/netclient/`** | 复用现有 proxy 配置（信令 WS 走用户代理设置）；无需改动 | 复用 |

**关键不变量**：Controller 零改动；`tabEventSink` 仍是单点；mobilebridge 是 Controller 的「兄弟订阅者」，不是 Controller 的依赖。

### 3.2 linkpeer 移动端（全新 Flutter 仓库 `linkpeer`）

```
linkpeer/
  lib/
    main.dart  app.dart
    core/
      webrtc/        # PeerConnection、DataChannel、ICE 收集、保活
      crypto/        # Ed25519 / X25519 / AES-256-GCM
      signaling/     # 与 hiq 云端信令通信（WS）
      pairing/       # 扫码 + 指纹本地校验
      transport/     # NDJSON 编解码 + AEAD 封装 + 分片重组
    data/
      models/        # wireEvent 的 Dart 镜像（与 Go wire.go 对齐）
      session_store.dart   # sqflite 缓存
    features/
      chat/          # 对话（编码助手）—— wireEvent → 气泡/工具卡/diff
      office/        # 办公 —— 模板触发 + 结果预览
      pairing/       # 配对引导
      settings/      # 设备管理、只读开关、连接质量
      shared/        # 事件渲染组件
  android/  ios/     # 平台壳
  test/
```

**技术栈**：
- Flutter（Dart）—— 一套代码出 Android + iOS。
- `flutter_webrtc` —— DataChannel + ICE。
- `mobile_scanner` —— 扫码配对。
- `flutter_secure_storage` —— iOS Keychain / Android Keystore 存 Ed25519 私钥。
- `sqflite` —— 会话列表/缓存。
- Riverpod —— 状态管理。
- `flutter_markdown` + `flutter_tex` —— 复用 hiq 前端的 katex 思路；mermaid 走 webview 或桌面预渲染图回传。

### 3.3 云端信令（自建 Go，独立小服务）

放在 `cmd/linkpeer-signal/`（hiq 仓库内）或独立仓库。无状态，~300 行。

---

## 4. 协议设计（单 DataChannel，NDJSON，端到端加密）

### 4.1 下行（hiq → linkpeer）：复用 `serve.wireEvent`

直接复用 `internal/serve/wire.go` 的 `wireEvent` 结构，linkpeer 拿到与桌面 webview 完全相同的事件流：

```jsonc
{ "kind": "text",        "text": "..." }
{ "kind": "reasoning",   "reasoning": "..." }
{ "kind": "tool",        "tool": { "id":"...", "name":"edit", "args":"...", "output":"..." } }
{ "kind": "approval",    "approval": { "id":"...", "tool":"...", "subject":"..." } }
{ "kind": "ask",         "ask": { "id":"...", "questions":[...] } }
{ "kind": "usage",       "usage": { ... } }
{ "kind": "turn_done",   "err": "" }
{ "kind": "compaction",  "compaction": { ... } }
```

### 4.2 上行（linkpeer → hiq）：命令 `cmd.*`

所有命令均映射到现有 `App.*ToTab` 方法，mobilebridge 纯转发：

```jsonc
{ "t":"submit",   "tab":"<id>", "input":"..." }
{ "t":"cancel",   "tab":"<id>" }
{ "t":"steer",    "tab":"<id>", "text":"..." }
{ "t":"pause",    "tab":"<id>" }  { "t":"resume", "tab":"<id>" }
{ "t":"approve",  "tab":"<id>", "approvalId":"...", "allow":true, "session":false, "persist":true }
{ "t":"answer",   "tab":"<id>", "askId":"...", "answers":[...] }
{ "t":"set_plan", "tab":"<id>", "on":true }
{ "t":"list_sessions" }
{ "t":"load_session", "path":"..." }
{ "t":"new_tab",   "workspaceRoot":"...", "profile":"...", "topicId":"..." }
{ "t":"switch_tab","tab":"<id>" }
{ "t":"subscribe_tab","tab":"<id>" }   // linkpeer 声明当前关注哪个 tab，决定下行路由
{ "t":"office_run","template":"...", "args":{...} }   // 办公触发（M5）
```

### 4.3 分片（>16KB 载荷，如历史 JSONL 回看）

```jsonc
{ "t":"chunk_start","id":"<msgId>","total":<n>,"mime":"application/jsonl" }
{ "t":"chunk",      "id":"<msgId>","seq":0,"b64":"<base64>" }
{ "t":"chunk_end",  "id":"<msgId>" }
```

### 4.4 加密封装（应用层 AEAD，包在 NDJSON 外）

每条 NDJSON 明文用会话密钥 AES-256-GCM 加密后，包成帧：

```
[1B version=1][8B seq(LE)][12B nonce][N ciphertext][16B tag]
```

序列号同时防重放（接收方拒绝 ≤ 已见最大 seq 的帧）。

---

## 5. 配对与密钥

### 5.1 一次性配对流程

1. hiq 桌面端生成 Ed25519 长期密钥对，存入 `secret.Store`（key `mobilebridge.device.{desktopId}.priv`）。
2. 桌面端显示二维码 + 6 位配对码：
   `linkpeer://pair?code=123456&fp=AB:CD:EF:...&relay=wss://signal.example.com&dev=<desktopId>`
3. linkpeer 扫码 → 经云端信令用配对码换桌面端公钥 → **本地**比对二维码里的 `fp` 指纹（防 MITM；不信任云端传回的公钥本身）。
4. linkpeer 生成自己的 Ed25519 密钥对（存 Keychain/Keystore），回传公钥，双向绑定。
5. 配对码 60s 失效；一次配对成功后两端持久化对方公钥，后续直连无需再扫码。

### 5.2 会话密钥

每次 WebRTC 连接建立后，双方用临时 X25519 ECDH 派生本次会话对称密钥（HKDF-SHA256）。长期 Ed25519 仅用于身份认证，不直接加密流量。会话密钥随连接断开而丢弃。

---

## 6. 安全模型

原则：**不信任云端、不信任网络。只有配对时本地校验过的长期公钥可信。**

- **端到端加密**：每条 NDJSON 用会话密钥 AES-256-GCM 加密（§4.4）。即使 DTLS 被降级或配置错误，攻击者也只能看到密文。
- **命令鉴权**：每条上行命令带设备 ID + HMAC（会话密钥）；未知设备/签名不符一律拒绝。
- **权限分级**（桌面端控制）：
  - **只读模式**：linkpeer 只看不动。
  - **移动操作需桌面批准**：复用现有 `ApprovalRequest`——linkpeer 提交等价于桌面端弹批准框。
  - **高危工具默认关闭**：linkpeer 默认不暴露文件系统写、shell 等执行权，除非桌面用户显式授权。
- **配对码防爆破**：信令服务对单 code 失败尝试限速 + 60s 过期。

---

## 7. 传输与打洞

### 7.1 ICE 候选策略（`iceTransportPolicy: all`，但无 TURN）

- **Host candidate**：同 LAN 直连，100% 可达。
- **STUN reflexive**：多 STUN 源（公共 + 自建 STUN-only）。
- **UPnP-IGD**：桌面端探测路由器 UPnP，成功则得到一个可对外映射的 candidate。
- **无 TURN candidate**：尊重「纯 P2P」决策。

### 7.2 保活与重连

- CGNAT 映射 60–120s 回收 → 每 ~25s 发应用层 ping（兼作 RTT 探测，UI 显示连接质量）。
- linkpeer 切 WiFi/4G 触发 **ICE restart**：经信令重发新 offer/answer，重协商不断连接。
- DataChannel 断开后，linkpeer 经信令重新发起（信令无状态，重连代价低）。

### 7.3 失败 UX（纯 P2P 打不通时）

明确提示，不静默失败：
- 「当前网络无法直连（可能双方都在运营商对称 NAT 后）。」
- 建议：① 两端接同一 WiFi；② 桌面端切换到公网 IP 或开启路由器 UPnP / 端口转发；③ linkpeer 切换网络。
- 显示本次 ICE 选用的候选类型（host/srflx/失败），帮助用户判断。

---

## 8. 云端信令服务规格（M0，自建 Go）

**默认自建 Go 小服务**（匹配 hiq「单静态 Go 二进制、零运行时依赖」工程哲学；部署在用户自有的公网 VPS）。

### 职责边界（严格）

✅ 配对码生成与撮合 / 公钥指纹中转 / SDP offer-answer 中转 / trickle ICE 候选中转 / 在线心跳
❌ 不存私钥 / 不转发任何业务流量 / 不持久化会话内容

### HTTP/WebSocket API（草案）

```
POST /pair/register
  → { code:"123456", desktopPubKey:"...", desktopFp:"AB:CD:..", relayTs:... }
  ← { pairId:"..." }

POST /pair/exchange   (linkpeer 扫码后调用)
  body: { code:"123456", mobilePubKey:"...", mobileFp:".." }
  ← { desktopPubKey:"...", desktopFp:".." }      // linkpeer 仍需本地比对二维码 fp

GET  /session/<pairId>/ws                          (双方各自建 WS，交换 SDP/ICE)
  消息: { "type":"sdp"|"ice"|"bye", payload:... }
```

- 配对码 60s 过期；单 code 失败 ≥5 次拉黑。
- 所有 WS 消息端到端由双方校验签名，信令不验（它没有密钥）。
- 无状态：pairId 仅存活到 DataChannel 建立或超时。

---

## 9. 跨平台细节（Android / iOS）

| 项 | Android | iOS |
|---|---|---|
| WebRTC | `flutter_webrtc`（Google 官方 libwebrtc） | 同 |
| 私钥存储 | Android Keystore | Keychain |
| 后台网络 | 前台服务（ongoing notification）保活 DataChannel | 受限：后台 socket 系统会挂起，需 voip push 或前台保持 |
| 网络权限 | `INTERNET` / `ACCESS_NETWORK_STATE` / 前台服务 | 默认出口网络 |
| 相机（扫码） | `mobile_scanner` | 同 |
| 分发 | 自签名 APK / Play Store | App Store（需审核；WebRTC、加密需声明 ITSAppUsesNonExemptEncryption=false 因仅本地加密） |

**iOS 特别注意**：
- 后台保活最难。MVP 策略：**仅前台运行**（用户切后台即暂停接收，回前台后拉取增量）。后续如需后台推送，走 APNs 静默推送唤醒（但 Apple 对此审核严格）。
- 加密合规：app 仅做端到端加密、不传输用户数据给第三方，`ITSAppUsesNonExemptEncryption=NO`（或 false 声明），免除出口加密申报。

**Android 特别注意**：
- 用前台服务（带通知）维持 DataChannel，否则 Doze 模式会断。
- targetSdk 升级时关注前台服务类型声明（`dataSync` / `connectedDevice`）。

---

## 10. 路线图

> **阶段策略（当前）**：M0–M5 以 **Android 为唯一验证平台**（Windows 本机即可完成全部开发与打包，开发者当前只能用 Windows）。iOS 留到 M6 Mac 接入后首次编译。WebRTC P2P / 端到端加密 / 打洞 / 保活在 Android 上的验证结论可直接迁移到 iOS——iOS 独有的只有 Keychain、后台保活、App Store 合规这些「包装层」。

| 阶段 | 目标 | 产出 | 周期（估） |
|---|---|---|---|
| **M0 信令服务** | 云端敲门上线 | `cmd/linkpeer-signal`：配对码、公钥撮合、候选中转 | 3–5 天 |
| **M1 桌面桥接器** | `internal/mobilebridge`：订阅 tabEventSink，暴露 DataChannel | hiq「移动端配对」入口（扫码+状态） | 5–7 天 |
| **M2 本地 P2P** | 同 WiFi 下 hiq↔linkpeer WebRTC + E2E 加密 | 收事件 / 发 cmd 打通 | 5–7 天 |
| **M3 跨网打洞** | STUN/UPnP 接入 | 多 STUN 源、UPnP 自动映射、ICE restart、失败 UX | 5 天 |
| **M4 对话界面** | linkpeer 对话 Tab 完整可用 | wireEvent 渲染、批准、历史回看 | 7–10 天 |
| **M5 办公界面** | linkpeer 办公 Tab | 模板触发、结果预览 | 5–7 天 |
| **M6 iOS 首次编译 + 双端上架** | Mac 接入，iOS 首次 `build ipa`；Android 已在 M0–M5 持续验证 | iOS Keychain / 后台保活 / App Store 合规、TestFlight、电量、通知 | 持续 |

---

## 11. 开发环境与打包

> **当前阶段（Mac 暂不可用，只能用 Windows）**：先 **All-in Android**——Windows 本机完成全部开发、调试、出 APK 自测，不碰 iOS。等 Mac 可用后再接 iOS（§11.7）。Android 上能验证 linkpeer 几乎全部能力（UI / WebRTC / 打洞 / 加密 / 保活 / 业务功能），iOS 独有的只是 Keychain、后台、App Store 合规这些「包装层」。

### 11.1 双机工作流（Windows 主 + Mac 辅）

linkpeer 以 Windows 为日常开发机，Mac 仅用于 iOS 出包。Flutter 跨平台特性让 95% 工作在 Windows 完成：

| 任务 | 在哪 |
|---|---|
| 写 Dart 代码 | Windows |
| 调对话 UI / 样式 | Windows：`flutter run -d windows` 或 `-d chrome`（跨平台所见即所得） |
| Android 真机/模拟器调试、出 APK | Windows |
| iOS 模拟器 / 真机 debug | Mac |
| `flutter build ipa` / 签名 / TestFlight / 上架 | Mac |
| 双端 release 自动出包（可选） | GitHub Actions / Codemagic |

**理由**：Flutter UI 声明式跨平台，Windows 桌面预览的对话界面到 Android/iOS 视觉几乎一致（仅状态栏 / 安全区 / 系统字体微调）。linkpeer 重 UI、重逻辑，Windows + Android 真机能验证 95%，iOS 真机只在验收期上 Mac。

### 11.2 账号与签名

- **Apple Developer Program**：$99/年（个人），上架 / TestFlight 必需。
- **Android**：自签名 keystore 出 APK 侧载免费；上 Google Play 一次性 $25 注册费。
- 证书 / 密钥不入库：Android `keystore.jks` 走 `local.properties`；iOS 证书存 Mac 钥匙串。

### 11.3 版本与最低部署

- **Flutter**：stable channel，版本锁在 `linkpeer/pubspec.yaml` + Dart SDK 同步锁。
- **Android**：`minSdk 24`（Android 7.0+，覆盖率 >95%），`targetSdk` 跟最新。
- **iOS**：15+（覆盖率 >98%，WebRTC + Keychain 稳定）。

### 11.4 打包命令

```bash
# Android（Windows 即可）
flutter build appbundle --release     # 上架 Play 用 .aab
flutter build apk --release           # 侧载 / 分发用 .apk

# iOS（Mac）
flutter build ipa --release           # 出 .ipa
flutter build ios --release           # Xcode 归档
```

### 11.5 CI（可选，有 Mac 也推荐）

配 GitHub Actions：push tag 触发 `macos-latest` runner 出 iOS + Android 双端包并自动 release。省手动归档，兼异地备份。

### 11.6 hiq 桌面端依赖注意（M0 前 spike）

桌面桥接 `internal/mobilebridge` 要引入 `github.com/pion/webrtc/v4`。hiq 现为**纯 Go（无 cgo）**（SQLite 用 `modernc.org/sqlite`，无 C 依赖）。pion/webrtc 也是纯 Go，理论干净接入。**M0 启动前必须 spike 验证**：pion 加进 `go.mod`、跑通 echo DataChannel、确认无依赖冲突、交叉编译不破——hiq 的「单静态二进制跨平台」不能因 pion 引入 cgo。

### 11.7 将来接 iOS：Silicon Mac 优先，Intel Mac 备用

Mac 可用后，两台里**优先用 Silicon（M 系列）**做 iOS 主力：能跑最新 Xcode、支持最新 iOS SDK、编译快、还能本地跑 iOS 模拟器 / 直接在 Mac 上运行 iOS App。**Intel Mac 作备用**或本地 CI runner——⚠️ 注意它能否升级到当前 Xcode 要求的 macOS 版本（Xcode 16 需 macOS Sonoma 14.5+）；若 Intel Mac 太老升不上去，可能打不了 App Store 要求的新 SDK 包，届时只能靠 Silicon。**一台 Mac 即可完成 iOS 全流程，无需两台并行。**

---

## 12. UI 设计（豆包 / Gemini 风格）

### 12.1 设计语言

**全屏沉浸式对话流**，参考豆包 / Gemini / ChatGPT 移动端。AI 消息不套气泡、占满宽度（带 avatar），用户消息右气泡；底部输入区圆角带附件 / 语音 / 发送；顶栏极简；历史会话侧抽屉。底部 Tab 仍保留「对话 / 办公 / 我的」（豆包样式是对话 Tab **内部**的样式）。

### 12.2 品牌一致（沿用 hiq 设计 token）

linkpeer 视觉继承 hiq 桌面端，保证产品线一致：

- **品牌主色 `#d97757`**（hiq `--accent`，暖橙）—— 恰好与豆包橙色调同频，天然契合。
- accent-strong `#e58a6b`、accent-soft `rgba(217,119,87,0.14)`、border `#343945` 直接复用。
- **深色为默认**（hiq `color-scheme: dark`，bg `#090a0c`，fg `#f4f5f7`），浅色可选。
- 移动端字号比桌面放大一档（手机阅读距离）。

### 12.3 对话 Tab（线框）

```
┌────────────────────────────┐
│ ☰  hiq·coding     ⌄ ⋮ │  顶栏：抽屉 / 会话名 / 模型 / 菜单
├────────────────────────────┤
│                            │
│ 🤖  正在思考…          ▾    │  reasoning 折叠卡（点开看全程）
│                            │
│ 🤖  我来帮你改这个函数：     │  AI 消息：无气泡，左 avatar
│     ```go                  │  Markdown + 代码高亮 + 复制按钮
│     func Serve() {         │
│       ...                  │
│     }                      │
│     ```                    │
│     🔧 edit  serve.go   ▸  │  工具卡（点开看 diff）
│     ✅ read   main.go      │
│                            │
│                帮我接着改   │  用户消息：右气泡，accent 底
│                            │
│ 🤖  好的…▌                  │  流式光标
├────────────────────────────┤
│ [📎] 消息…         [🎤][↑] │  底部输入：圆角，附件/语音/发送
└────────────────────────────┘
```

要点：
- AI 消息直接渲染 Markdown（`flutter_markdown` + `flutter_highlight`）；代码块带复制按钮 + 语言标。
- 工具调用收成卡片（`tool` 事件 → `ToolCard`），点开看 args / output / diff。
- 思考过程（`reasoning`）默认折叠，避免占屏。
- 审批请求（`approval`）弹底部 sheet，大按钮允许 / 拒绝。
- 流式：`StreamBuilder` 接 `text` delta，配打字光标。
- 长按消息：复制 / 重新生成 / 分享。

### 12.4 办公 Tab（线框）

```
┌────────────────────────────┐
│  办公                       │
├────────────────────────────┤
│ 📄 Word 模板                │
│   ┌─ 周报 ─┐  ┌─ 通报 ─┐    │  模板网格
│   └────────┘  └────────┘    │
│ 📊 Excel 处理               │
│ 📈 PPT 生成                 │
│ 📥 文档转换 (PDF/...)       │
├────────────────────────────┤
│ 最近结果                    │
│  • 周报-0811.docx     ▸     │  结果预览 / 下载
└────────────────────────────┘
```

### 12.5 配对 / 我的 Tab

- 首次打开 → 全屏扫码引导（相机预览 + 输入配对码兜底）。
- 「我的」：已配对桌面列表、连接质量（host / srflx / 失败）、只读开关、解绑、深色模式。

### 12.6 组件清单

`ChatMessageBubble`、`MarkdownBody`、`CodeBlock`、`ToolCard`、`ApprovalSheet`、`ThinkingCollapse`、`Composer`（输入区）、`SessionDrawer`（会话抽屉）、`OfficeTemplateGrid`、`PairingScanner`。

### 12.7 推荐包

- `flutter_markdown` + `flutter_highlight` —— Markdown / 代码高亮。
- `mobile_scanner` —— 扫码配对。
- `flutter_webrtc` —— DataChannel。
- `riverpod` —— 状态管理。
- `flutter_secure_storage` —— Keychain / Keystore。
- 自写：消息气泡、工具卡、输入区（豆包样式不复杂，自写可控性高）。

---

## 13. 遗留微决策（不阻塞 M0）

1. 信令服务：自建 Go（默认）vs Cloudflare Worker + Durable Objects。
2. linkpeer 是否本地缓存会话历史支持离线查看（影响是否镜像 JSONL 到 `sqflite`）。
3. 是否支持「一个 linkpeer 绑定多个 hiq 桌面端」（一人多机）。
4. 首屏先做对话（建议）还是办公。
5. iOS 后台策略：仅前台（MVP）vs APNs 静默推送唤醒（后期）。
