# linkpeer 连接协议与安全规范

> 状态：规范草案 v1
> 日期：2026-08-11
> 范围：定义 hiq 桌面端（S, server peer）与 linkpeer 移动端（C, client peer）之间**如何建立连接、服务器提供什么、如何保证安全**。读完应可直接实现。
> 相关：架构概览见 [`MOBILE_CLIENT_PLAN.md`](./MOBILE_CLIENT_PLAN.md)；本文是其协议层细化。

---

## 0. 设计原则

1. **不信任云端、不信任网络**——只有配对时本地校验过的长期公钥可信。
2. **业务流量永不过服务器**——服务器只见敲门信令，看不到任何对话/命令/文件。
3. **纵深防御**——WebRTC 自带 DTLS，但应用层再独立做一次认证 + AEAD，不依赖 DTLS 配置正确。
4. **前向保密**——每条连接用临时 X25519 派生会话密钥；一条连接泄露不影响其他连接。
5. **不自创密码学**——全用成熟原语（Ed25519 / X25519 / HKDF-SHA256 / AES-256-GCM）。

---

## 1. 连接生命周期（6 阶段）

```
A. 配对        一次性，建立长期信任（带外指纹 + 扫码）
B. 会话建立    每次连接，经信令交换签名过的 SDP/ICE → WebRTC DataChannel
C. 加密握手    DataChannel 上做类 TLS1.3 握手（Ed25519 认证 + X25519 派生）
D. 数据传输    AEAD 帧，分方向密钥 + seq 防重放
E. 保活重连    ping/pong、断线 backoff、ICE restart
F. 吊销解绑    单向/双向吊销，已吊销设备无法再握手
```

每阶段都有独立安全保证，见 §10 威胁矩阵。

---

### 1.1 日常连接全链路时序（配对后常态——用户每次开 App 走的路径）

```
C(linkpeer)              K(信令)                S(hiq 桌面)
   │ App 启动              │                      │ (启动时即维持长连 WS 在 K)
   │                       │ ◄═══ WS 长连保活 ════│
   │ 1.WS 认证连 K         │                      │
   │ ────────────────────► │                      │
   │ ◄── 认证通过 ──────── │                      │
   │                       │                      │
   │ 2.建 PC+DC，生 offer  │                      │
   │ 3.offer(签名)经 K 转发│                      │
   │ ────────────────────► │ ─── offer 推给 S ──► │
   │                       │                      │ 4.S 验签、建 PC、生 answer
   │                       │ ◄── answer(签名) ────│
   │ ◄── answer 回 C ───── │                      │
   │                       │                      │
   │   5.双方 trickle ICE 候选(签名)，经 K 互转                          │
   │ ◄══════════════════════════════════════════════════════════════► │
   │                       │                      │
   │   6.ICE 连通 → DataChannel 建立                                    │
   │                       │                      │
   │   7.加密握手 (hello_c / hello_s / Finished)                        │
   │ ◄══════════════════════════════════════════════════════════════► │
   │                       │                      │
   │   8.ENCRYPTED，业务帧流动（对话事件 / 命令）                        │
   │     K 此时仅作 WS 保活，不参与业务                                  │
   │ ◄══════════════════════════════════════════════════════════════► │
```

**关键前提**：S 的长连 WS 必须在 C 发起前就已建立，否则 K 查不到 S 在线、C 收到 `unavailable`。所以 hiq 启动 + `[mobilebridge] enabled` + 已配对时，**S 立即建立到 K 的长连**（详见 §4.5）。

---

## 2. 角色、ID 与密钥体系

### 2.1 角色

| 符号 | 角色 | 平台 |
|---|---|---|
| **S** | hiq 桌面端（server peer，服务侧） | Windows/macOS/Linux |
| **C** | linkpeer 移动端（client peer，发起侧） | Android/iOS |
| **K** | 信令服务（knock server，仅敲门） | 公网 VPS |

C 总是连接发起方（`who calls who`），S 是被动方。这是 P2P 语义上的「client/server」，与 NAT 穿透方向无关。

### 2.2 标识符

| 标识 | 生成 | 用途 |
|---|---|---|
| `deviceId` | `base32(SHA256(Pub)[:10])`（去除易混字符） | 每个安装唯一；由公钥推出，不必单独存 |
| `pairId` | 128bit 随机，base64url | 一次配对会话的标识，配对完即弃 |
| `connId` | 128bit 随机 | 一次 WebRTC 连接的标识，重连换新 |
| `fp`（fingerprint） | `base32(SHA256(Pub)[:8])`，分组 `ABCD-EFGH-JKMN` | 人类可比对的公钥指纹，用于带外校验 |

### 2.3 密钥

| 密钥 | 类型 | 存放 | 用途 | 生命周期 |
|---|---|---|---|---|
| **长期身份** `L_X` / `Pub_X` | Ed25519 | S: `secret.Store`（DPAPI）；C: Keychain/Keystore | 签名/认证身份 | 永久，直到解绑 |
| **临时 ECDH** `eph_X` | X25519 | 内存 | 派生会话密钥 | 单次连接，断开即弃 |
| **会话密钥** `c2s_key` / `s2c_key` | 256bit | 内存 | AEAD 加密帧 | 单次连接，断开即弃 |

**长期密钥仅认证身份，从不加密流量。** 流量只用临时派生的会话密钥。

### 2.4 base32 字母表

去除易混字符 `0 O 1 I L`，共 31 字符：
`ABCDEFGHJKMNPQRSTUVWXYZ23456789`

---

## 3. 阶段 A：配对协议

### 3.1 流程

```
S(桌面)                  K(信令)                  C(移动)
  │ 1.生 code/pairId       │                        │
  │ 2.POST /pair/register  │                        │
  │ ─────────────────────► │                        │
  │ ◄────── {pairId, exp}  │                        │
  │                        │                        │
  │ 3.显示二维码(含code,fp_S,relay,pairId)            │
  │    ◄═══════════════ 相机扫描 ════════════════════ │
  │                        │  4.POST /pair/exchange │
  │                        │ ◄────────────────────-─ │
  │                        │    (code,pub_C,fp_C)    │
  │                        │ ─── 校验code ───►       │
  │ ◄── WS推送 {pub_C,...} │ ──► {pub_S, fp_S} ────► │
  │                        │                        │
  │                        │    5.C 本地算 fp_S' =  │
  │                        │      SHA256(pub_S)      │
  │                        │    比对二维码 fp_S:      │
  │                        │    一致→存 pub_S        │
  │ 6.S 存 pub_C 到 secret │                        │
  │                        │                        │
  配对完成，pairId 从 K 删除
```

### 3.2 消息定义

**POST /pair/register**（S → K）

```jsonc
{
  "code": "7KQ9XM",            // 6 位 base32（去易混），S 生成，~29 bit 熵
  "pubS":  "<base64 Ed25519 公钥>",
  "fpS":   "ABCD-EFGH-JKMN",   // base32(SHA256(pubS)[:8])
  "devS":  "<S 的 deviceId>"
}
```
响应：
```jsonc
{ "pairId": "<base64url 16B>", "expiresAt": 1723352061 }
```

服务端校验：同 `devS` 60s 内最多 3 个活跃 pair；`fpS` 与 `SHA256(pubS)` 自洽（服务端也算一遍防傻）。

**POST /pair/exchange**（C → K）

```jsonc
{
  "pairId": "<从二维码>", "code": "7KQ9XM",
  "pubC": "<base64 Ed25519 公钥>", "fpC": "...",
  "devC": "<C 的 deviceId>"
}
```
响应：
```jsonc
{ "pubS": "...", "fpS": "...", "sessionToken": "<base64url 24B>", "tokenTtl": 2592000 }
```

服务端校验：`pairId` 存在且未过期；`code` 匹配；失败次数 < 5；`fpC` 自洽。任一不符返回 4xx，`failedCount++`，到 5 锁该 `pairId`。

**关键防 MITM 点**：响应里的 `fpS` 是信令给的，C **不直接信任**。C 自己算 `SHA256(pubS)` 与**二维码里**的 `fpS` 比对（二维码是带外信道，经屏幕→相机，不经信令）。不一致则丢弃、提示用户。

### 3.3 二维码内容

```
linkpeer://pair?pid=<pairId>&code=7KQ9XM&fp=ABCD-EFGH-JKMN&dev=<devS>&relay=wss://signal.example.com
```

字段：`pid` 配对会话、`code` 短码、`fp` **S 的公钥指纹（带外校验用）**、`dev` S 的 deviceId、`relay` 信令 WSS 地址。

### 3.4 配对码安全参数

| 参数 | 值 | 说明 |
|---|---|---|
| 字符集 | base32 去易混（31 字符） | 人眼/手输友好 |
| 长度 | 6 | ~29 bit 熵 |
| TTL | 60s | 过期作废 |
| 单 pairId 失败上限 | 5 | 超过锁该 pairId |
| 单 IP 失败上限 | 20/小时 | 防扫号 |
| 单 devS 并发 pair | 3 | 防注册洪泛 |

5 次锁定 + 60s 窗口下，在线爆破成功概率 ≈ 5/2^29，可忽略。

---

## 4. 阶段 B：会话建立（信令 + SDP/ICE）

### 4.1 WebSocket 认证（完全无状态，v1.1 优化）

配对后双方各持自己 Ed25519 长期密钥。C 发起连接（或 S 常驻）时，与 K 建 WSS：

```
GET /session/ws?dev=<devId>&ts=<unix_s>&pub=<base64公钥>&sig=<base64 Ed25519_Sign(L, devId||ts)>
```

K 验证（**零状态，不查任何表**）：
1. `devId == base32(SHA256(pub)[:10])` —— 身份自洽（devId 是公钥派生，§2.2）
2. `Ed25519.Verify(pub, devId||ts, sig)` —— 证明持有对应私钥
3. `|now - ts| < 60s` —— 防重放窗口

全过 → 登记 `peers[devId] = wsConn`，保持。否则关闭（4401 身份不符 / 4402 签名错 / 4403 ts 过期）。

**关键优势**：K 完全无状态——不存 pub、不存配对关系、不存 token。重启零影响，任何已配对设备重连即过。配对合法性由 S 端握手拒绝（§5.4）兜底；K 只验"你持有 devId 对应的私钥"，不验"你配对过谁"。

S 侧：hiq 启动 + `[mobilebridge] enabled` + 已配对时，立即建这条 WSS 并常驻（§4.5）。
C 侧：按需连（要连 S 时建，连完可断）。

### 4.2 SDP / ICE 交换（所有消息 Ed25519 签名）

**offer（C → K → S）：**
```jsonc
{
  "type": "offer",
  "connId": "<C 生成的 16B 随机>",
  "from": "<devC>", "to": "<devS>",
  "ts": 1723352100,
  "sdp": "<WebRTC SDP>",
  "sig": "<base64 Ed25519_Sign(L_C, SHA256('offer'||connId||from||to||ts||sdp))>"
}
```

**answer（S → K → C）：** 同结构，`type:"answer"`，`sig` 用 `L_S` 签。

**ice（双向 trickle）：**
```jsonc
{
  "type": "ice", "connId":"...", "from":"...", "to":"...", "ts":...,
  "cand": "<ICE candidate string>",
  "sig": "<base64 Ed25519_Sign(L_from, SHA256('ice'||connId||from||to||ts||cand))>"
}
```

**bye**（结束）：`{"type":"bye","connId":"...","sig":...}`

**关键**：信令 K **不验签**（它没有密钥也无法验）。两端各自用对方公钥验签：`sig` 不符、`ts` 超 ±300s、`to` 不是自己 → 丢弃。这保证即使 K 被攻破或作恶，也无法篡改 SDP 偷渡攻击者 ICE 候选（§10 T2）。

### 4.3 信令路由表

K 内存维护：
```
peers[devId] = { pubKey, wsConn, lastSeen }   // 在线注册表
```
配对完成时，K 把 `[devC, devS]` 记为「已配对对」。路由消息时按 `to` 查 `peers`，找到 wsConn 转发；找不到则缓存 30s 等对端上线，超时回 `504 peer unavailable`。

K 重启后 `peers` 清空——无所谓，因为它无状态，两端重连即可。

### 4.4 ICE 候选策略（分层降级）

`iceTransportPolicy: all`，candidate 按优先级降级尝试：

```
1. host candidate      —— 同 LAN 直连（100% 可达，零依赖）
2. srflx (STUN)        —— 公网反射打洞（cone NAT 成功；对称 NAT 多失败）
3. prflx               —— 对等反射（打洞包撞开端口，best-effort）
4. relay (TURN)        —— 仅当用户主动开启 turn_enabled=true（默认 false）
```

- STUN：自建 coturn（§10）为主，可叠加公共 STUN 兜底。
- S 端额外 UPnP-IGD（探测路由器映射端口）。
- DataChannel 协商：C 创建 `PC` + `dc`，发 offer；S 应答。

**TURN opt-in 兜底（v1.1 降级方案）**：

默认 `turn_enabled = false`（尊重"纯 P2P，失败即断"决策）。但在 `[mobilebridge]` 配置里提供 opt-in：

```toml
[mobilebridge]
turn_enabled = false                # 默认关
turn_servers = ["turn:signal.example.com:5349"]  # 同 VPS 自建
turn_credentials = "hiq-user"  # coturn 静态凭据（自建可信）
```

开启后，ICE 在 host/srflx 都失败时回退 TURN。UI 明示"当前为加密中继模式"。因 TURN 跑在用户自建 VPS + 端到端加密，TURN 只见密文，不违反"业务不经过第三方云"原则。

coturn 配置在 §10.3 的基础上，开 TURN 时去掉 `no-udp-relay`/`no-tcp-relay`，开 `5349/tls`（TURN over TLS）+ relay 端口范围 `49152-65535/udp`。

---

### 4.5 S 侧信令长连通道（敲门机制的前提）

S 在 NAT 后、无公网 IP。C 经 K 找到 S 的**前提是 S 主动维持一条到 K 的出站长连 WS**（出站连接能穿 NAT）。K 据此维护 `peers[devS]` 在线表，C 发来 offer 时才能沿这条长连推给 S。**这条长连是 hiq 桌面端 mobilebridge 的核心常驻组件——它不建立，整个敲门机制无法运作。**

**建立时机**：
- hiq 启动、`[mobilebridge] enabled = true`、且已配对至少一台 C 时，立即建到 K 的 WSS。
- 配对码生成期间也要连（需接收 C 的 exchange 通知 + 后续 offer）。
- 未配对且不在配对中 → 不必连（无人会找它）。

**认证**：与 C 完全相同，`GET /session/ws?dev=<devS>&ts=<ts>&sig=<base64 Ed25519_Sign(L_S, devS||ts)>`，K 用 `pubS`（配对时记录）验签。

**保活**（双层）：
- WS 协议层 ping：每 30s。
- 应用层 `{"type":"kp"}` / `{"type":"kp_ack"}`：每 25s。
- K 据此更新 `peers[devS].lastSeen`，超过 90s 无活动 → 标离线（从 peers 表移除）。

**重连**（S 侧）：
- WS 断开 → backoff 重连：2s, 4s, 8s, 16s, 30s, 30s… 上限 30s。
- 重连成功 → K 的 peers 表自动恢复 devS 在线（K 无状态，重新注册即可）。
- S 端网络切换（断网/换网）→ 同 backoff。

**K 路由失败处理**（C 发 offer 时）：
- 查 `peers[devS]`：不存在（离线）→ 回 `{"type":"unavailable","reason":"peer_offline"}`，C 提示「桌面端离线」。
- 存在但 WS 推送失败 → K 缓存 30s，等 S 重连补投；超时回 504。
- 因此 **S 的长连健康度直接决定 C 的可达性**——S 端需在 UI 显示「已连接到信令 / 重连中」状态，便于用户排查。

**S↔K 长连与 C↔K WS 的区别**：
- C 是「按需连」（要连 S 时才建 WS，连完可断）。
- S 是「常驻连」（一直在线等 offer）。
- 两端都走同一个 `/session/ws` 端点，K 用 devId 在 peers 表区分。

---

## 5. 阶段 C：加密握手（DataChannel 上，类 TLS 1.3）

DataChannel 建立后，两端在其上明文交换 hello（DataChannel 已 DTLS 加密，但应用层独立认证 + 派生新密钥）。明文用 NDJSON。

### 5.1 握手消息

**C → S：ClientHello**
```jsonc
{
  "t": "hello_c", "ver": 1,
  "eph": "<base64 X25519 临时公钥 32B>",
  "nc":  "<base64 16B 随机>",
  "cid": "<devC>", "sid": "<devS>",
  "ts":  1723352100123,
  "sig": "<base64 Ed25519_Sign(L_C, SHA256('lpc1.hello_c'||eph||nc||cid||sid||ts))>"
}
```

**S → C：ServerHello**（同构，`t:"hello_s"`，`ns` 替代 `nc`，`sig` 用 `L_S` 签）

### 5.2 密钥派生

双方各自：
```
shared = X25519(本地 eph 私钥, 对端 eph 公钥)
prk    = HKDF-Extract(salt = nc || ns, ikm = shared)
c2s_key = HKDF-Expand(prk, "linkpeer v1 c2s", 32)   // C 发 / S 收
s2c_key = HKDF-Expand(prk, "linkpeer v1 s2c", 32)   // S 发 / C 收
transcript = SHA256(ClientHello_json || ServerHello_json)
```

### 5.3 Finished

握手后第一条数据帧各自发 Finished（用各自发送密钥加密，§6 帧格式）：
```
plaintext = {"t":"fin","role":"c"|"s","th":<base64 transcript[:8]>}
```
对端能解密 + `th` 一致 → 进入 `ENCRYPTED` 状态。失败 → 关闭 DataChannel。

### 5.4 握手时的吊销检查

S 收到 `hello_c` 时：
1. 查 `cid` 是否在已配对表（`secret.Store` 里有 `pubC`）。
2. 查 `cid` 是否在吊销列表（§8）。
3. 不在已配对 / 在吊销列表 → **不回 hello_s**，直接 `dc.Close()`。这避免给攻击者任何身份存在性信号。

---

## 6. 阶段 D：数据帧格式与防重放

握手后所有消息封装为二进制帧：

```
┌──────┬──────────────┬────────────┬─────────────────┬───────────┐
│ ver  │ seq (uint64) │ nonce(12B) │ ciphertext(var) │ tag(16B)  │
│ 1B   │  8B LE       │            │                 │           │
└──────┴──────────────┴────────────┴─────────────────┴───────────┘
AAD = ver || seq          （GCM 也保护这两个字段）
key = c2s_key (C→S) 或 s2c_key (S→C)
seq  = 每方向独立计数器，从 0 单调递增
nonce = 12B 随机（每帧新随机，GCM 不要求 nonce 计数器，随机即可，key+nonce 重复概率可忽略）
```

**明文** = 一行 NDJSON（一条消息），如 `{"t":"text","tab":"...","text":"..."}`。

### 6.1 防重放

接收方每方向维护 `recvMaxSeq`：
- `seq <= recvMaxSeq` → 丢弃（重放/乱序）。
- `seq > recvMaxSeq` → 更新 `recvMaxSeq`，处理。
- 解密失败（tag 不符）→ 丢弃 + 错误计数；超过阈值（如 10/分钟）→ 断连。

### 6.2 分方向密钥防反射

C→S 与 S→C 用不同 key，攻击者无法把 S 发给 C 的帧「反射」回 S（key 不对，解密失败）。

### 6.3 大消息分片

SCTP 单消息 ≤16KB。>16KB 的明文（如历史 JSONL 回看）应用层分片：
```
plaintext → chunk → 每片独立成帧
接收方按 connId+msgId+seq 重组
```
分片协议见 PLAN §4.3。

---

## 7. 阶段 E：保活与重连

### 7.1 ping/pong（封装为加密帧）

```
每 25s 发 {"t":"ping","ts":now_ms}
对端回  {"t":"pong","ts":<同上>}
RTT = now - pong.ts；UI 显示连接质量。
连续 30s 无任何帧（含 pong）→ 视为断线。
```

### 7.2 重连策略

| 事件 | 动作 |
|---|---|
| DataChannel `onClose` | C 端 backoff 重发 offer：1s,2s,4s,8s,16s,30s,30s… 上限 30s，**每档加 ±50% jitter**（防 K 重启时 5000 连接羊群风暴） |
| 移动网络切换（WiFi↔4G） | 立即 ICE restart：新 ufrag/pwd，重收集 candidate，重发 offer |
| 信令 WSS 断 | 5s（+jitter）后重连 WS；重连成功后重发 offer |
| 重连成功 | **全新握手**（新 eph，新会话密钥，seq 归零） |

### 7.3 NAT 保活

CGNAT 映射 60–120s 回收，25s 的 ping 兼作 STUN channel keepalive，足够维持反射端口。

---

## 8. 阶段 F：吊销与解绑

### 8.1 桌面端解绑移动设备（S 吊销 C）

UI 点「解绑」：
1. `secret.Store` 删除 `mobilebridge.peer.<devC>`（`pubC`）。
2. 写入吊销列表 `secret.Store["mobilebridge.revoked"]`（追加 `devC` + 时间戳）。
3. 若当前有活动连接，主动 `dc.Close()` + 发 `bye`。

C 再发起握手时，S 在 `hello_c` 阶段查不到 `pubC` → 直接关闭（§5.4）。

### 8.2 移动端解绑桌面（C 解绑 S）

C 删除 `pubS`，本地不再连该 S。这是单向停止使用。若要双向（让 S 也认账），靠 S 的 UI 操作。

### 8.3 长期密钥泄露应急

- 改密（重新配对）：S 生成新 Ed25519 密钥对，旧 deviceId 作废加入吊销，所有已配对 C 需重新扫码。
- 由于流量密钥是临时的，旧连接的历史流量无法被解（前向保密）。

---

## 9. 信令服务（K）完整规格

### 9.1 端点总览

| 方法 路径 | 用途 | 认证 |
|---|---|---|
| `POST /pair/register` | S 注册配对 | 无（限速） |
| `POST /pair/exchange` | C 换公钥 | pairId+code |
| `GET /session/ws` | 双向信令通道 | dev+ts+sig（Ed25519） |
| `GET /healthz` | 存活探针 | 无 |

### 9.2 状态表（仅内存）

```
pairs[pairId] = {
  code, devS, pubS, fpS,
  pubC, fpC, devC,          // exchange 后填
  failedCount, createdAt, expiresAt
}
peers[devId]  = { wsConn, lastSeen }
paired[devId] = [对端 devId, ...]   // 配对完成时双向记录
tokens[token] = { devId, expiresAt } // sessionToken
```

重启即清空——K 无状态，重启后两端重连即可。

### 9.3 限速

5000+ S 并发规模下，按 devId 为主（防误伤共享 NAT 出口的用户）：

| 维度 | 上限 |
|---|---|
| 每 devId `/pair/*` | 5/小时 |
| 每 IP `/pair/*` | 200/小时（仅防明显洪泛） |
| 单 `pairId` 失败 | 5 次锁 |
| 单 `devS` 并发 pair | 3 |
| 每 devId WS 消息 | 50/s |
| WS 消息大小 | 32 KB |
| 全局 active pair 上限 | 50000（防失控，超拒 503） |

### 9.4 日志策略

K **只记元数据**（devId、连接/断开时间、错误码），**绝不记** code、公钥内容、SDP、ICE、业务消息。日志供运维排查可达性，不含可还原会话的信息。

### 9.5 内部架构与路由（5000+ 规模连接管理）

K 是无状态路由器。核心是 `devId → wsConn` 哈希表 + 每条 WS 一个读 goroutine。

**数据结构：**
```go
type Server struct {
    mu    sync.RWMutex
    peers map[string]*peerConn  // devId → 连接
}
type peerConn struct {
    devId    string
    ws       *websocket.Conn
    writeMu  sync.Mutex        // 串行写（gorilla/websocket 禁并发写同连接）
    lastSeen atomic.Int64
}
```

**每条 WS 生命周期**：连入 → 验签 → `peers[devId]=conn` → 起读 goroutine → 阻塞读，每条消息调 `route()` → 断开时从 peers 删除、goroutine 退出。

**路由（O(1)，非广播）：**
```go
func (s *Server) route(from *peerConn, raw []byte) {
    var m struct{ To string `json:"to"` }
    json.Unmarshal(raw, &m)
    s.mu.RLock(); to := s.peers[m.To]; s.mu.RUnlock()
    if to == nil {
        s.send(from, `{"type":"unavailable","reason":"peer_offline"}`)
        return
    }
    to.writeMu.Lock()
    to.ws.WriteMessage(websocket.TextMessage, raw)
    to.writeMu.Unlock()
}
```

**关键**：消息只发给 `to` 字段指定的那一台，不广播。5000 台规模对单次路由 O(1)，完全不碰其他 4999 台。

**性能（5000 并发）**：
| 项 | 开销 |
|---|---|
| 单次路由 | < 10μs |
| 稳态消息量 | ~400 msg/s（S 长连保活） |
| 峰值（早高峰敲门） | ~100 msg/s |
| Go 处理上限 | 数万 msg/s，远超需求 |
| peers 表内存 | 5000 条 ≈ 2MB |
| goroutine | ~10000（每 WS 读+写），Go 轻松 |

瓶颈是 fd 数（`LimitNOFILE=65535`）和内存（~400MB），不是转发逻辑。CPU 稳态 <10%。

**工程要点**：
- 每条 WS 独立 `writeMu`（gorilla/websocket 禁并发写）。
- peers 表 RWMutex（读多写少）。
- 后台 goroutine 周期扫，90s 无活动踢僵尸连接（防 S 异常断但 TCP 未关）。
- 优雅关闭：K 退出时给所有 WS 发 close 帧，客户端 backoff 重连。
- **K 不存配对关系**——配对在两端本地（S `secret.Store` + C Keychain），K 只做路由；配对合法性由 S 端握手拒绝（§5.4）兜底。K 不需要也不该知道谁配对了谁，这是隐私 + 简化的双重好处。

---

## 10. 服务器部署（docker-compose）

### 10.1 拓扑

```
公网 → Caddy:443 ──反代 WSS──► linkpeer-signal:8080
      → Caddy:443/udp? 否       coturn:3478/udp（直连，STUN only）
```

一台 VPS（1C1G 足够），一个域名（如 `signal.example.com`）。

### 10.2 `docker-compose.yml`

```yaml
version: "3.8"
services:
  signal:
    image: ghcr.io/zzycxz/linkpeer-signal:latest   # 或本地构建
    restart: unless-stopped
    volumes: ["./signal.toml:/etc/linkpeer/signal.toml:ro"]
    networks: [internal]

  coturn:
    image: coturn/coturn:latest
    restart: unless-stopped
    network_mode: host          # STUN 需要 UDP 直连，host 网络最省事
    volumes: ["./turnserver.conf:/etc/turnserver.conf:ro"]
    command: ["-c", "/etc/turnserver.conf"]

  caddy:
    image: caddy:2
    restart: unless-stopped
    ports: ["80:80", "443:443"]
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
    networks: [internal]
    depends_on: [signal]

networks: { internal: {} }
volumes: { caddy_data: {}, caddy_config: {} }
```

### 10.3 `turnserver.conf`（仅 STUN，禁 relay）

```conf
listening-port=3478
listening-ip=0.0.0.0
fingerprint
lt-cred-mech=false            # STUN 无需认证
no-multicast-peers
no-cli
no-tls
no-dtls
no-tcp-relay                  # 关掉 TCP 中继
no-udp-relay                  # 关掉 UDP 中继 ← 关键：强制只做 STUN
stale-nonce=600
max-allocate-timeout=3
rejected-ip-policies
log-file=stdout
simple-log
```

> 重点：`no-udp-relay` + `no-tcp-relay` 让 coturn 只回答 STUN binding 请求，绝不中继媒体。这是「纯 P2P」决策的服务端兑现。

### 10.4 `signal.toml`

```toml
[server]
listen = "127.0.0.1:8080"      # 只听本地，由 Caddy 反代
public_relay = "wss://signal.example.com"

[pair]
code_ttl = 60                 # 秒
max_fail_per_pair = 5
max_fail_per_ip_per_hour = 20
max_concurrent_per_dev = 3

[session]
token_ttl = 2592000           # 30 天
ws_msg_per_sec = 10
ws_max_msg_bytes = 32768
offer_ts_skew = 300           # 秒

[stun]
servers = ["stun:signal.example.com:3478"]

[log]
level = "info"                # info/warn/error；绝不 debug 记业务内容
```

### 10.5 `Caddyfile`（自动 TLS + WSS 反代）

```caddy
signal.example.com {
    reverse_proxy signal:8080
    # WebSocket 升级 Caddy 自动处理
    log {
        output file /data/access.log
        format json
    }
}
```

Caddy 自动申请 Let's Encrypt 证书并续签，前提是域名 A 记录已指向 VPS、80/443 开放。

### 10.6 防火墙（ufw）

```bash
ufw default deny incoming
ufw allow 22/tcp         # SSH（建议改用密钥 + 限源 IP）
ufw allow 80/tcp         # Caddy HTTP（ACME 挑战 + 重定向）
ufw allow 443/tcp        # Caddy HTTPS / WSS
ufw allow 3478/udp       # coturn STUN
ufw enable
```

**仅开 4 个端口**。业务流量（WebRTC）走 P2P，不在此列。

### 10.7 部署步骤

1. VPS 装 Docker + docker-compose。
2. 域名 `signal.example.com` A 记录指向 VPS IP。
3. 放 `docker-compose.yml` / `signal.toml` / `turnserver.conf` / `Caddyfile` 到 `/opt/linkpeer/`。
4. `docker-compose up -d`。
5. 验证：`curl https://signal.example.com/healthz` → `{"ok":true}`；`stunclient signal.example.com` 能拿到反射地址。

### 10.8 VPS 规格（5000+ S 并发规模）

| 配置 | 能撑 | 说明 |
|---|---|---|
| 2C4G | 5000 并发 WS（起步最低） | 紧但可行 |
| **4C8G** | 5000 + 余量 | **推荐** |
| 8C16G | 15000+ | 垂直扩展空间 |

**必须**：`LimitNOFILE=65535`（signal + Caddy 都设，默认 1024 必爆 fd）。详细容量推算、冗余、DDoS 防护、演进路径见 [`LINKPEER_ENGINEERING.md`](./LINKPEER_ENGINEERING.md) §10。月成本 ~$30。standby 实例同区域备灾，DNS 切换（TTL 60s），RTO ~90s。

---

## 11. 威胁模型与对策矩阵

| ID | 威胁 | 攻击者能力 | 对策 | 协议位置 |
|---|---|---|---|---|
| T1 | 配对 MITM | 劫持信令，替换 S 公钥 | 二维码带外指纹 `fp`，C 本地比对 `SHA256(pubS)` | §3.2 |
| T2 | 会话 MITM | 篡改 SDP/ICE 偷渡攻击者候选 | SDP/ICE 经 Ed25519 签名，两端验签，信令不验 | §4.2 |
| T3 | 重放 | 录下旧帧重发 | 每方向 seq 单调，拒绝旧 seq | §6.1 |
| T4 | 伪造命令 | 未授权设备发命令 | 每条命令在加密帧内，key 仅配对双方有 | §5/§6 |
| T5 | 配对码爆破 | 猜 6 位码 | 60s TTL + 5 次锁 + IP 限速，~29bit 熵 | §3.4 |
| T6 | 信令 DoS | 注册洪泛 | IP 限速 + 并发 pair 上限 + 内存态重启即清 | §9.3 |
| T7 | **信令被攻破** | 拿到 K 全部数据 | K 无私钥、不存业务、SDP/ICE 有签名（T2）；即便如此攻击者也只能看到密文+签名信令 | §0/§4.2 |
| T8 | 设备丢失 | 拿到 C 的手机 | 桌面一键吊销 `devC`；私钥在 Keychain/Keystore 硬件保护 | §8.1 |
| T9 | 长期密钥泄露 | 拿到 L_X | 重新配对 + 吊销旧 deviceId；历史流量因临时 key 无法解（前向保密） | §8.3 |
| T10 | 降级攻击 | 强制弱 DTLS cipher | 应用层独立 AEAD，固定 ver=1，不依赖 DTLS | §6 |
| T11 | metadata 泄露 | K 知道谁连谁 | K 只见 devId（公钥派生），不见用户身份；日志最小化 | §9.4 |

### 11.1 「信令被攻破」最恶劣场景分析

即便 K 完全沦陷（攻击者拿到 root + 全部内存 + 全部流量录制）：
- 看不到任何对话/命令/文件内容（业务走 P2P，且应用层 AEAD）。
- 看到 `pubC/pubS`（公钥本就公开，无害）。
- 看到 SDP/ICE（IP/端口候选）——但无法篡改偷渡（T2 签名）；也解不开后续流量。
- 能做的仅是拒绝服务（断信令），这是任何中心化组件都不可避免的。

这就是「云端最多敲门」的安全含义：敲门服务被攻破 ≠ 数据泄露。

### 11.2 安全加强项（v1.1，基于威胁复审）

复审后追加 6 项加强，影响多个章节，集中列此：

**① 配对双向确认（堵「抢先 exchange」）** —— 影响 §3

漏洞：攻击者偷看到桌面二维码（shoulder surfing / 截图泄露），能在真用户之前用自己的 `pub_C` + `code` 完成 exchange，抢先绑定恶意设备。指纹校验防不了这个（指纹防的是 MITM，不是抢跑）。

对策：`/pair/exchange` 成功后**不自动落库**，而是 S 桌面弹确认：

```
┌─────────────────────────────────────┐
│ linkpeer 请求配对                    │
│ 设备指纹：ABCD-EFGH-JKMN            │
│ （请与手机端显示的指纹比对）         │
│       [确认绑定]      [拒绝]         │
└─────────────────────────────────────┘
```

S 用户点确认 → S 才把 `pub_C` 写入 `secret.Store` + 回 ack 给 C。60s 内未确认 → pairId 过期，C 提示"桌面未确认"。

**② 命令级权限校验（强制授权）** —— 影响 §6 / FEATURES §8.2

S 端 `commandRouter` 在分发每条命令前，按该 `connId` 对应 C 的权限独立校验：

```
readonly=true 的 C 发 submit   → 拒绝 forbidden
allow_high_risk=false 的 C 发 shell/exec → 拒绝 forbidden
allow_file_drop=false 的 C 发 file_start → 拒绝 forbidden
```

不信任 C 端自觉，S 端强制。拒绝时回 `{"t":"error","code":"forbidden","cmd":"..."}`，C 提示"桌面端未授权此操作"。

**③ office_run / file_drop 参数校验（防注入）** —— 影响 FEATURES §3/§4

- `office_run`：S 端对模板名走白名单（仅 `desktop/default_registry.json` 已注册的），参数做路径规范校验（`filepath.Clean` + 禁 `..` + 限定在工作区根下），复用 hiq 现有 office 工具的安全沙箱。
- `file_drop`：落地路径强制 `incoming/` 子目录，文件类型白名单（`.jpg/.png/.pdf/.docx/.xlsx/.txt/.md/.zip`），扩展名校验真实 MIME（不只看后缀），不自动执行，需用户/agent 显式 read。

**④ S 端审计日志（事后追查）** —— 影响 §8

mobilebridge 记审计日志（脱敏，存本地 `~/.hiq/mobilebridge/audit.log`）：

```
2026-08-11T14:23:01Z  devC=K7QM...9XC  cmd=submit      tab=tab_a1b2  ok
2026-08-11T14:24:15Z  devC=K7QM...9XC  cmd=approve     id=ap3        ok
2026-08-11T14:30:00Z  devC=K7QM...9XC  cmd=office_run  tpl=周报      ok
2026-08-11T14:31:00Z  devC=K7QM...9XC  cmd=shell       → forbidden (allow_high_risk=false)
```

记录 devId（公钥派生）+ 命令类型 + 摘要 + 结果，**不记**命令参数全文（可能含敏感文本）。便于事后追查哪个设备做过什么。桌面端 UI 可查看最近 100 条。

**⑤ K 全局内存上限（防注册洪积 DoS）** —— 影响 §9

除了按 IP/devId 限速，再加：
- 全局 active pair 上限 10000（超过拒新注册，返回 503 `capacity_full`）。
- pairId 60s TTL 由后台清扫协程主动清，不依赖被动过期检查。
- `peers`/`tokens` 表同样设上限（如 50000），超过踢最旧。

这样攻击者即便用代理池刷，K 内存也有硬顶。

**⑥ K 日志保留策略（metadata 最小化）** —— 影响 §9.4

- 日志仅保留 **7 天**，自动轮转。
- 不记 SDP/ICE/IP 明文，只记 `candidate_type`。
- devId 是公钥派生，不直接关联真实身份。
- 无业务日志，重启即清内存态。

---

## 12. 密码学选型与库

| 用途 | 原语 | Go 库（S/K） | Dart 库（C） |
|---|---|---|---|
| 身份/签名 | Ed25519 | `crypto/ed25519`（标准库） | `package:cryptography` |
| 临时 ECDH | X25519 | `crypto/ecdh`（标准库） | `package:cryptography` |
| 密钥派生 | HKDF-SHA256 | `golang.org/x/crypto/hkdf` | `package:cryptography` |
| 对称加密 | AES-256-GCM | `crypto/aes` + `crypto/cipher`（标准库） | `package:cryptography` |
| WebRTC | DTLS（libwebrtc 自带） | `github.com/pion/webrtc/v4` | `flutter_webrtc` |

全部标准原语，无自创。Go 端基本只用标准库 + pion + x/crypto。

---

## 13. 错误码与 UX 映射

### 13.1 错误码完整表

| 场景 | HTTP / WS 码 | 错误标识 | C 端 UX | S 端行为 |
|---|---|---|---|---|
| 配对：pairId 不存在/过期 | 404 | `pair_not_found` | "配对码已失效，请在桌面重新生成" | — |
| 配对：code 不符 | 401 | `code_mismatch` | "配对码不对" | failedCount++ |
| 配对：失败 5 次 | 423 | `pair_locked` | "尝试过多，已锁定，请重新生成" | pairId 锁 |
| 配对：指纹本地不符 | — | `fp_mismatch`（C 本地） | "公钥指纹不符，可能被监听，已中止" | 不存 pubC |
| WS：未配对 | — | 关闭 4401 | "设备未配对" | — |
| WS：签名错 | — | 关闭 4402 | "认证失败，请重新配对" | — |
| WS：ts 过期（±60s 外） | — | 关闭 4403 | "本地时间不准，请校准" | — |
| 限速 | 429 / 关闭 4404 | `rate_limited` | "操作过快，稍后重试" | — |
| 路由：对端离线 | — | `unavailable`/`peer_offline` | "桌面端离线" | — |
| 路由：对端 WS 推送失败 | 504 | `peer_unreachable` | "桌面端无响应，重试中" | 30s 缓存 |
| 握手：hello 验签失败 | — | DataChannel 直接 close | "连接失败，正在重试"+ 重试 | 不回 hello_s |
| 握手：身份未配对/已吊销 | — | DataChannel close（同上） | "设备未授权" | 不回 hello_s |
| 握手：Finished 错 | — | DataChannel close | "连接失败" | 断 |
| 帧：解密失败（tag 错） | — | 丢帧 + 计数 | 连续多次→"连接异常，重连" | 超 10/min 断 |
| 帧：seq 倒退 | — | 静默丢帧 | 无 | — |
| 帧：版本不符 | — | `version_mismatch` | "请更新到最新版" | — |
| 命令：权限不足 | — | `forbidden` | "桌面端未授权此操作" | 拒绝 |
| 命令：tab 不存在 | — | `tab_not_found` | "会话已关闭" | — |

### 13.2 失败处理原则

- **静默 vs 提示**：协议级瞬态错误（seq 丢帧、偶发 tag 错）静默；会话级错误（断、未授权、版本不符）必须提示用户。
- **重试 vs 放弃**：网络类错误 backoff 重试；认证/吊销/版本错误不重试，引导操作。
- **不泄露信息**：握手阶段对未授权设备直接 close，不回任何"你不配对"的明文（防设备枚举）。

---

## 18. K 监控指标

K 在 `/metrics` 暴露 Prometheus 格式：

| 指标 | 类型 | 标签 | 说明 |
|---|---|---|---|
| `linkpeer_peers_online` | gauge | `type={s,c}` | 当前在线 S/C 数 |
| `linkpeer_pairs_active` | gauge | — | 进行中配对数 |
| `linkpeer_pair_total` | counter | `result={success,fail,expire,locked}` | 配对结果计数 |
| `linkpeer_ws_connections` | gauge | — | 活跃 WS 连接数 |
| `linkpeer_ws_msgs_total` | counter | `type={offer,answer,ice,bye}` | 转发消息计数 |
| `linkpeer_signal_errors_total` | counter | `code={4401,4402,4403,4404,...}` | 错误计数 |
| `linkpeer_ratelimit_hits_total` | counter | `dim={ip,pair,fail}` | 限速命中 |
| `linkpeer_msg_bytes_total` | counter | `dir={in,out}` | 信令流量（仅元数据量级） |
| `linkpeer_uptime_seconds` | gauge | — | 进程运行时长 |
| `process_*` | — | — | Go runtime 标准指标 |

告警阈值见 ENGINEERING §3.2。指标本身不含用户身份或业务内容，符合 §9.4 日志原则。

---

## 14. 三端状态机

### 14.1 linkpeer（C）单连接状态机

```
IDLE ──配对成功──► PAIRED
PAIRED ──用户选 S / App 启动──► SIGNALING (WS 认证 + offer/answer)
SIGNALING ──DataChannel 开──► CONNECTED (DC 已建, 握手前)
SIGNALING ──K 回 unavailable / 超时──► PAIRED (提示「桌面离线」, 等重试)
CONNECTED ──Finished 通过──► ENCRYPTED (业务可用)
CONNECTED ──握手失败──► DISCONNECTED → backoff → SIGNALING
ENCRYPTED ──DC 断──► DISCONNECTED → backoff → SIGNALING
任意 ──用户解绑──► IDLE (删 pubS)
```

### 14.2 hiq（S）

**S 自身常驻状态**（与具体 C 无关）：
```
OFFLINE (mobilebridge disabled / 未配对)
ONLINE (长连 K 已建, 可被发现) ◄──► RECONNECTING (WS 断, backoff 中)
```

**S 对单个 C 的连接状态机**（可同时多个 C 各自处于不同状态）：
```
LISTEN (长连在线, 等 offer)
  ──收到 offer──► NEGOTIATING (建 PC)
NEGOTIATING ──DC 开──► HANDSHAKING (等 hello_c)
HANDSHAKING ──hello_c 验签 + 发 hello_s──► FIN_WAIT (等 Finished)
FIN_WAIT ──Finished 通过──► ACTIVE (注入 tabEventSink, 开始转发事件)
ACTIVE ──DC 断 / 收 bye──► CLOSED (清理, 回 LISTEN)
任意 ──桌面解绑该 C──► CLOSED + 写吊销列表
```

### 14.3 云端（K）：无状态机

K 是无状态内存表，重启即清。每个条目有自己的小生命周期：
- `pairId`：60s TTL，配对成功即删。
- `sessionToken`：30 天 TTL。
- `peers[devId]` WS 会话：连接断即移除。

无全局状态转换，纯查表路由。

---

## 15. 多端并发与 tab 路由

### 15.1 一 S 多 C（一个桌面被多台手机连）

hiq 桌面可被多台 linkpeer 同时连接。

- S 维护 `conns map[connId]*Conn`，每个 Conn 独立握手 + 独立会话密钥。
- **事件广播**：`tabEventSink.Emit(e)` 把事件发给**所有当前订阅了该 tab 的 C**。订阅关系 `connId → subscribedTab`，每 C 同一时刻只看一个 tab（`subscribe_tab` 命令切换）。
- **命令并发**：多 C 同时向同一 tab 发 `submit` 允许——Controller 自己排队（已有机制），按到达顺序处理。
- 资源开销：每连接一条读 goroutine + 共享 Controller；内存成本 = 会话密钥 + 帧缓冲，可忽略。建议上限 4 条并发连接（超出拒绝新连，防滥用）。

### 15.2 一 C 多 S（一个手机绑多台桌面）

linkpeer 可绑定多台 hiq 桌面。

- C 维护 `desktops: map[devS] → {label, pubS, lastConnectedAt, lastState}`。
- **MVP：单活动连接**——同时只与一台 S 建 P2P，切换桌面时断旧建新。简单、省电、省流量。
- 后期可扩「多并发镜像」（多桌面同时看），目前不必要。
- C UI 顶部下拉切换「当前桌面」，切换 = 断当前 P2P + 建新 P2P。

### 15.3 tab 订阅路由（S 端实现）

```go
// C 发: {"t":"subscribe_tab","tab":"<id>"}
// S 处理:
conns[connId].subscribedTab = id
// 立即回投该 tab 当前快照:
send(connId, snapshotEvents(id))   // 最近 N 条 wireEvent + tab 状态(model/effort/running)

// tabEventSink.Emit(e) 改造(原 desktop/tabs.go):
for connId, conn := range conns {
    if conn.subscribedTab == e.tab {
        conn.sendEncrypt(toWire(e))   // 新增的转发点
    }
}
```

### 15.4 新 C 连上时的初始化

握手进入 ACTIVE 后，S 主动推送：
1. **tab 清单**：所有打开的 tab（id / workspace / model / running 状态）。
2. C 据此渲染 UI，默认 `subscribe_tab` 到第一个 tab 或「上次看过的 tab」。

---

## 16. 协议版本协商

- `ClientHello` / `ServerHello` 含 `ver` 字段（当前 `1`）。
- 收到不支持的 `ver` → 回 `{"t":"error","code":"version_mismatch","max":1}`，关闭连接。
- 信令 K 与版本无关（只转字节），但 WS 认证时可带 `clientver`，K 仅作运维统计记录。
- 升 `ver` 策略（MVP）：**不做向后兼容**，强升级——旧端连新端直接失败，提示「请更新到最新版」。重新配对可恢复（长期密钥不变，只是协议层重谈）。未来若需兼容，在 hello 里加 `supported_versions` 数组协商。

---

## 17. 实现顺序（落地映射）

1. **K**：`cmd/linkpeer-signal`——HTTP+WS、内存表、限速、签名转发（不验）。对应 §9。
2. **共享协议包** `internal/mobilebridge/proto`：JSON 消息 struct（Go），供 S 与 K 复用。linkpeer 端手写对应 Dart class（与 Go 对齐）。
3. **S** `internal/mobilebridge`：配对状态机（§3）+ 信令 WS 客户端（§4）+ pion DataChannel + 握手（§5）+ 帧编解码（§6）+ `tabEventSink` 注入。
4. **C** linkpeer：镜像协议层 + UI。

每一步都有独立的单测：配对状态机、签名/验签、帧加解密、seq 防重放、握手成功/失败路径。
