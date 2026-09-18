# 三端 Spec 一致性验证与安全复审

> 状态：v1
> 日期：2026-08-11
> 范围：对 [HIQ_SPEC](./LINKPEER_HIQ_SPEC.md)、[SIGNAL_SPEC](./LINKPEER_SIGNAL_SPEC.md)、[LINKPEER_SPEC](./LINKPEER_LINKPEER_SPEC.md) 三份规范做**横向一致性验证**和**攻击者视角安全复审**，确认设计正确、无遗漏。
> 上游：[PROTOCOL](./LINKPEER_PROTOCOL.md)。

---

## 1. 一致性验证矩阵

三端在协议消息字段、密钥派生、签名格式上必须字节级一致。下表逐项核对，✅ = 已在对应 spec 明确。

### 1.1 协议消息字段一致性

| 消息 | K 端 | S 端 | C 端 | 一致 |
|---|---|---|---|---|
| `POST /pair/register` {code,pubS,fpS,devS} | ✅ §3.1 | ✅ §3.2（pairing.go） | — | ✅ |
| `POST /pair/exchange` {pairId,code,pubC,fpC,devC} | ✅ §3.2 | — | ✅ §6（pairing） | ✅ |
| WS 认证 `dev+ts+sig` | ✅ §3.3 | ✅ §3.2（signal_client） | ✅ §4（signaling） | ✅ |
| offer/answer/ice `{type,from,to,ts,sig,sdp,cand}` | ✅ §5（透传） | ✅ §3.3（peer.go） | ✅ §4.2 | ✅ |
| ClientHello `{t:hello_c,ver,eph,nc,cid,sid,ts,sig}` | — | ✅ §3.3（handshake） | ✅ §4.1 | ✅ |
| ServerHello `{t:hello_s,...,ns,...}` | — | ✅ | ✅ | ✅ |
| AEAD 帧 `[ver|seq|nonce|ciphertext|tag]`，AAD=`ver||seq` | — | ✅ §3.3（frame.go） | ✅ §4.1（transport） | ✅ |
| wireEvent `{kind,text,reasoning,tool,approval,ask,usage,...}` | — | ✅ §3.4（event_sink） | ✅ §4.1（渲染） | ✅ |

**单一事实源**：`internal/mobilebridge/proto/types.go`（Go），Dart `data/models/` 镜像，CI `protocol-compat` 校验。

### 1.2 密码学参数一致性

| 参数 | 值 | 三端 |
|---|---|---|
| 长期身份 | Ed25519 | ✅✅✅ |
| 临时 ECDH | X25519 | ✅（S handshake / C transport） |
| 密钥派生 | HKDF-SHA256，salt=`nc‖ns`，info 分别 `"linkpeer v1 c2s"`/`"linkpeer v1 s2c"`，32B | ✅✅ |
| 对称加密 | AES-256-GCM，nonce=12B 随机，AAD=`ver(1B)‖seq(8B)` | ✅✅ |
| 指纹 | `base32(SHA256(pub)[:8])`，字母表去 `0O1IL` | ✅（S pairing / C pairing / K 校验） |
| 签名输入 | `SHA256("lpc1.hello_c"‖eph‖nc‖cid‖sid‖ts)` 等，见 PROTOCOL §5.1 | ✅✅ |

**关键**：HKDF info 串、签名前缀字符串、AAD 字节序必须在三端字节级一致。这是最容易漂移的地方，**必须 CI 覆盖**（用同一组测试向量跨 Go/Dart 验证）。

### 1.3 角色与发起方向一致性

| 行为 | 发起方 | 验证 |
|---|---|---|
| 建信令 WS 长连 | S 主动（出站，穿 NAT） | ✅ S §3.2 |
| 建信令 WS 按需 | C 主动（连 S 时） | ✅ C §4 |
| WebRTC offer | C 创建（C 总是连接发起方） | ✅ C §4.1 / S §3.3 |
| 配对 register | S | ✅ |
| 配对 exchange | C | ✅ |
| 双向确认 | S 用户点确认 | ✅ S §6（§11.2①） |

无角色冲突。

---

## 2. 攻击者视角安全复审

重新审视，按攻击者能力分类，逐项确认防御覆盖。

### 2.1 被动窃听者

| 资产 | 防御 | 状态 |
|---|---|---|
| 信令流量（C↔K, S↔K） | WSS over TLS 1.3（Caddy） | ✅ |
| P2P 业务流量（C↔S） | DTLS + 应用层 AEAD 双层 | ✅ |
| STUN 反射 | 仅 IP:port，非敏感 | ✅（设计如此） |
| 包大小/时序 | 流量分析可见 | ⚠️ MVP 不防（padding 是 future） |

**结论**：窃听者看不到内容，仅能看到流量存在 + 大小。这是可接受 limitation。

### 2.2 主动中间人

| 攻击 | 防御 | 状态 |
|---|---|---|
| 改信令消息 | TLS 防改；即便 TLS 被企业 CA MITM，SDP/ICE 有 Ed25519 签名两端验签 | ✅ |
| 改 P2P 流量 | DTLS + AEAD 双层 | ✅ |
| 注入伪造 SDP | Ed25519 签名校验拒（§4.2） | ✅ |
| 降级 TLS/DTLS | 固定最低版本（TLS 1.2+，Caddy 配置） | ✅ |
| 降级应用层 | 固定 `ver=1`，不协商向下（§16） | ✅ |

**结论**：MITM 无法篡改任何有效数据。

### 2.3 K 被完全攻破

| 资产 | 暴露 | 状态 |
|---|---|---|
| 业务内容 | 不可见（P2P + AEAD） | ✅ |
| SDP/ICE（含公网候选 IP） | 可见，但签名防篡改 | ✅（改了被两端拒） |
| devId 连接图（metadata） | 可见 | ⚠️ 已知，日志 7 天轮转缓解 |
| 拒绝服务 | 可行 | ⚠️ 不可避免（任何中心化组件） |
| 长期密钥 | K 不持有 | ✅ |

**结论**：K 沦陷 ≠ 数据泄露，只 = DoS。达成设计目标。

### 2.4 恶意已配对客户端（C）

| 攻击 | 防御 | 状态 |
|---|---|---|
| 读其他 C 的流量 | 独立会话密钥 | ✅ |
| 越权命令 | S command_router 每命令按权限校验（§11.2②） | ✅ |
| 连其他 S | S 握手拒绝未配对（§5.4） | ✅ |
| 枚举其他 devId | K WS 认证 + S 握手不回未配对（不泄露存在性） | ✅ |
| 重放旧命令 | seq 防重放 | ✅ |

**结论**：权限模型 + 独立密钥兜住。

### 2.5 第三方未配对攻击者

| 攻击 | 防御 | 状态 |
|---|---|---|
| 冒充某 S | 无 S 私钥，签不了 hello_s | ✅ |
| 爆破配对码 | 5 次锁 + 60s TTL + IP/devId 限速 | ✅ |
| 抢先 exchange（偷看二维码） | **S 端双向确认**（§11.2①） | ✅（v1.1 加强） |
| 注册洪泛 DoS | 全局 active pair 上限 50000 + 限速 | ✅ |
| WS 未认证连接 | dev+ts+sig 验签 | ✅ |

**结论**：所有路径覆盖。

### 2.6 端点被攻破

| 场景 | 防御 | 状态 |
|---|---|---|
| C 手机丢失 | Keychain/Keystore 硬件保护私钥 + S 端解绑吊销 | ✅ |
| C 被解锁控制 | 攻击者可冒充 C；S 审计日志记录（§11.2④）事后追查；用户可重置 | ⚠️ 端点安全，超协议层 |
| S 桌面被入侵 | 攻击者取 secret.Store 私钥；用户可重置密钥 + 全部 C 重新配对 | ⚠️ 端点安全，超协议层 |

**结论**：端点被攻破是终极风险，靠物理安全 + 吊销 + 审计。文档明确这是协议层之外的 limitation。

### 2.7 时序侧信道

| 攻击 | 防御 | 状态 |
|---|---|---|
| 签名/指纹比较时序泄露 | 全部用 `constant-time compare`（S crypto.go / C crypto / K 校验） | ✅（三端 spec §安全要点均已强调） |
| 握手失败响应时间差泄露存在性 | 未配对/吊销统一"不回 hello_s 直接 close"，无时间差 | ✅ |

---

## 3. 验证发现的遗漏与修复

复审中发现并已修复的问题（前几轮迭代）：

| # | 问题 | 修复 | 位置 |
|---|---|---|---|
| 1 | S↔K 长连通道未细化（敲门前提不清） | §4.5 详写建立/认证/保活/重连 | PROTOCOL |
| 2 | 配对可被"抢先 exchange"劫持 | S 端双向确认 | PROTOCOL §11.2① / S SPEC §6 |
| 3 | 命令无 S 端权限强制 | command_router 每命令校验 | S SPEC §3.5/§6 |
| 4 | office_run/file_drop 参数可注入 | 白名单 + 路径校验 | S SPEC §6 |
| 5 | 无操作审计 | audit.go | S SPEC §6 |
| 6 | K 注册洪积 DoS | 全局上限 + TTL 清扫 | SIGNAL SPEC §8 |
| 7 | 限速误伤共享 NAT 用户 | 改按 devId 为主 | SIGNAL SPEC §6 |
| 8 | K 重启连接风暴 | backoff + jitter | PROTOCOL §7.2 |
| 9 | 对称 NAT 必失败无兜底 | TURN opt-in（默认关） | PROTOCOL §4.4 |
| 10 | 5000 规模 fd 爆 | LimitNOFILE=65535 | ENGINEERING §10.2 |

---

## 4. 技术选型调优结论

复审确认的选型（含调优点）：

| 项 | 选型 | 评估 | 决策 |
|---|---|---|---|
| 对称加密 | AES-256-GCM | vs ChaCha20：现代设备都有 AES 硬件，GCM 更通用 | **保持** |
| 身份/ECDH | Ed25519 / X25519 | 成熟、小、快、广泛实现 | **保持** |
| WebRTC (S) | pion/webrtc v4 | 纯 Go、活跃、生产用 | **保持**（spike 验证） |
| WebRTC (C) | flutter_webrtc | 官方、跨平台；APK 增 ~15-20MB | **保持**（注意包大小） |
| STUN/TURN | coturn | 成熟标准 | **M0 用**；**后期评估 pion/turn 嵌入信令**（调优点） |
| 反代/TLS | Caddy | 自动 Let's Encrypt、WS 支持好 | **保持** |
| 信令格式 | JSON | 量小、可读、易调试 | **保持** |
| 状态管理 (C) | Riverpod | 现代、测试友好 | **保持** |
| 本地存储 (C) | sqflite + flutter_secure_storage | 成熟 | **保持** |
| 桌面集成 | 同进程 pion | 简单 | **保持**；独立进程 contingency |

**调优点汇总**：
- coturn → pion/turn（后期，简化部署）
- 同进程 → 独立进程（仅 pion spike 失败时）

---

## 5. 残余风险（已知接受）

| 风险 | 程度 | 接受理由 |
|---|---|---|
| 流量分析（包大小/时序） | 低 | MVP 不防；padding 是 future |
| 端点被攻破（手机丢/桌面入侵） | 中 | 物理安全 + 吊销 + 审计；超协议层 |
| 双对称 NAT 必失败 | 中 | 物理 NAT 限制；TURN opt-in 兜底 |
| iOS 后台保活不达标 | 中 | MVP 仅前台；APNs 后期 |
| 信令 K 单点（即便 standby） | 低 | 90s RTO 可接受；多实例留待规模需要 |

---

## 6. 验证结论

**一致性**：三端在协议消息、密码学参数、角色方向上已字节级对齐，单一事实源 + CI 兼容测试保障。

**安全性**：覆盖被动窃听、主动 MITM、K 沦陷、恶意客户端、第三方攻击、端点失控、时序侧信道七类威胁。残余风险（流量分析、端点、对称 NAT）已知接受并有缓解路径。

**选型**：所有选型经复审合理，2 个调优点（coturn→pion/turn、同进程 contingency）有明确触发条件。

**spec 正确性**：三份 spec 互相引用无矛盾，职责边界清晰，无重叠或遗漏模块。

**可进入实现**：spec 层已完整，下一步 M0 pion spike → 信令服务 + 桥接器骨架。

---

## 7. 协议不变量（v1.1 补充）

三端必须共同遵守的硬约束，违反即安全漏洞或互操作失败：

| 不变量 | 约束 | 实现位置 |
|---|---|---|
| AES-GCM nonce 不碰撞 | 单 key ≤ 2^32 次加密，到则 rekey（新 X25519） | S frame.go / C transport |
| seq 单调递增 | uint64，每方向独立；接收方拒绝 ≤ recvMaxSeq | frame.go / transport |
| devId 自洽 | `devId == base32(SHA256(pub)[:10])` | 三端配对/认证时校验 |
| WS 认证无状态 | K 不查表，仅靠 pub+sig+ts 自洽验证（§4.1） | K session.go |
| 帧版本固定 | `ver=1`，不协商向下，不符即拒 | frame.go / transport |
| 握手签名绑定身份 | hello 签名输入含 cid+sid+eph+ts，防跨连接重用 | handshake.go / connection |
| 指纹带外校验 | C 必须本地算 SHA256(pubS) 与二维码 fp 比对，不信 K 返回值 | C pairing |
| 路由按 to 精确 | K 只投给 `to` 指定的 devId，不广播 | K route() |
| 命令 S 端强制权限 | 每命令按 connId 权限校验，不信任 C | S command_router |

---

## 8. 测试向量与 fuzz 计划（v1.1 补充）

### 8.1 标准测试向量

放 `internal/mobilebridge/proto/testvectors/`，CI 跑 Go/Dart 双向对照：

| 向量 | 输入 | 期望输出 |
|---|---|---|
| Ed25519 | 固定 seed | 公钥 + 签名样本 |
| X25519 | 固定双方 ephemeral | shared secret |
| HKDF | 固定 (shared, salt=nc‖ns, info) | c2s_key / s2c_key |
| AES-GCM | 固定 (key, seq, nonce, plaintext) | ciphertext + tag |
| devId 派生 | 固定 pub | `base32(SHA256(pub)[:10])` |
| 完整握手 | 固定双方长期密钥 + ephemeral | 派生的会话密钥两端一致 |
| 帧 | 固定 (key, seq, plaintext) | 完整帧字节 |
| WS 认证 | 固定 (devId, ts, pub, sig) | 验证通过/失败 |

任一端输出与向量不符 → CI fail。

### 8.2 Fuzz 计划

| 目标 | 语料策略 | 期望 |
|---|---|---|
| hello_c / hello_s | 随机字节、畸形 JSON、超长字段 | 不 panic，拒绝 |
| 帧字节流 | 随机长度、错 tag、错 seq | 丢帧不 panic |
| 签名验证 | 篡改 1 bit 签名/公钥 | 验签失败，不通过 |
| SDP/ICE JSON | 嵌套过深、超大字符串、注入 | 限大小，不 panic |
| WS 认证 | 错 pub/devId/ts 组合 | 4401/4402/4403 正确分类 |
| 配对码 | 边界字符、超长、空 | 正确处理 |

Go 端 `go test -fuzz`，Dart 端 `package:fuzz`。CI nightly 跑 fuzz 累计 ≥1h。

---

## 9. v1.1 复审发现并修复的问题（追加 §3 清单）

| # | 问题 | 修复 | 位置 |
|---|---|---|---|
| 11 | HIQ_SPEC 多 C 广播与 PROTOCOL 不一致 | tabs 改 set 映射 + 双向 | HIQ_SPEC §11.1 |
| 12 | WS 认证依赖 sessionToken，K 重启失效 | 改完全无状态认证（pub+sig 自洽） | PROTOCOL §4.1 / SIGNAL_SPEC §13.1 |
| 13 | 缺 Bridge 生命周期顺序 | 启动/关闭流程 | HIQ_SPEC §11.2 |
| 14 | 缺错误降级矩阵 | 7 类错误处理 | HIQ_SPEC §11.3 |
| 15 | 缺 App 生命周期协调 | 后台/前台/被杀/冷启 | LINKPEER_SPEC §11.1 |
| 16 | 缺增量同步协议 | resync 命令 + S 环形缓冲 | LINKPEER_SPEC §11.2 |
| 17 | 缺错误 UX 状态机 | 7 类错误 UI | LINKPEER_SPEC §11.3 |
| 18 | 缺网络变化检测 | connectivity_plus 触发 ICE restart | LINKPEER_SPEC §11.4 |
| 19 | 缺离线命令队列 | submit 排队补发 | LINKPEER_SPEC §11.5 |
| 20 | 缺流式渲染性能策略 | 节流 + 虚拟列表 + 懒加载 | LINKPEER_SPEC §11.6 |
| 21 | 缺 App 锁 | 生物识别 + 隐私屏幕 | LINKPEER_SPEC §11.7 |
| 22 | 缺 GCM 连接寿命边界 | seq 到 2^32 rekey | HIQ_SPEC §11.5 / §7 不变量 |
| 23 | 缺测试向量 | 标准向量跨端对照 | §8.1 |
| 24 | 缺 fuzz 计划 | 6 类目标 | §8.2 |
