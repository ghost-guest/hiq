# linkpeer 验证规划（实现过程 checklist）

> 状态：v1
> 日期：2026-08-11
> 范围：定义 M0–M6 每个阶段的**验证点、方法、通过标准、阻断规则**。每进下一阶段前，本阶段的验证 gate 必须全绿。
> 相关：设计层验证见 [`LINKPEER_VERIFICATION.md`](./LINKPEER_VERIFICATION.md)（协议不变量、威胁矩阵、测试向量）；本文是**实现过程**的验证规划。

---

## 0. 验证原则

1. **边做边验证**：每个里程碑有验证 gate，过了才进下一个，不堆到最后。
2. **验证左移**：协议一致性在 M1 就验（Go e2e），不等 M4 真机。
3. **阻断明确**：每项标注「阻断/非阻断」，阻断项失败必须修，非阻断项记录。
4. **自动化优先**：CI 跑贯穿验证，人工只做真机/跨网 dogfood。
5. **对端就绪才集成**：改 hiq（M1）前，K 和 linkpeer 的协议契约已验过一致。

---

## 1. 验证层次（8 类）

| 层 | 验什么 | 何时 | 自动化 |
|---|---|---|---|
| L1 协议一致性 | 消息字段/密钥派生/签名 字节级一致 | M1 起，CI 每次 | ✅ 测试向量 |
| L2 单元 | 模块函数行为 | 每个模块提交时 | ✅ go test / flutter test |
| L3 集成 | 模块拼接（配对/握手/路由） | M0-b、M1 | ✅ |
| L4 端到端 | 真实链路 | M1（Go e2e）、M2（真机） | 半自动 |
| L5 不变量 | 协议硬约束（seq/nonce/devId） | CI 每次 | ✅ 断言 |
| L6 安全 | fuzz/重放/MITM/爆破 | M1 后 nightly | ✅ fuzz |
| L7 性能容量 | NFR 目标、5000 规模 | M2、M4、上线前 | 半自动 |
| L8 跨平台 | Android/iOS/桌面一致 | M2（Android）、M6（iOS） | 人工 |

---

## 2. 验证基础设施（M0 前先建）

| 设施 | 位置 | 用途 |
|---|---|---|
| 测试向量库 | `internal/mobilebridge/proto/testvectors/` | L1 协议一致性对照（Go/Dart 双跑） |
| mock K | `internal/mobilebridge/testutil/mocksignal/` | L3/L4 集成测试用，不依赖真 K |
| Go e2e 框架 | `internal/mobilebridge/e2e_test.go` | 双 goroutine 模拟 C/S，同进程双 PeerConnection |
| 协议契约包 | `internal/mobilebridge/proto/types.go` | 单一事实源，K 和 S 共享 |
| CI 流水线 | `.github/workflows/` | 见 §4 |

---

## 3. 里程碑验证点

### M0-spike：pion 依赖验证

| 验证项 | 方法 | 通过标准 | 阻断 |
|---|---|---|---|
| 纯 Go 编译 | `CGO_ENABLED=0 go build ./...`（Win/macOS/Linux） | 三平台全过 | ✅ |
| echo DataChannel | 同进程两 PC 直连，echo 一条消息 | 消息往返成功 | ✅ |
| 二进制增量 | 编译 hiq with/without pion，对比大小 | < 10 MB | ✅（超则评估） |
| 依赖无冲突 | `go mod tidy` + 现有测试全过 | 无 regression | ✅ |

**失败处理**：CGO_ENABLED=0 失败 → 评估独立 bridge 进程 contingency，重新规划 M1。

### M0-a：linkpeer 工程 + 静态 UI

| 验证项 | 方法 | 通过标准 | 阻断 |
|---|---|---|---|
| Flutter 工程 | `flutter create` + `flutter pub get` | 无错误 | ✅ |
| flutter_webrtc 编入 | `flutter build apk --debug` | 成功 | ✅ |
| APK 大小 | 构建 release 测 | < 30 MB | 非阻断（记录） |
| 真机运行 | APK 装手机 | 显示豆包样式 UI | ✅ |
| UI 快照 | widget test 覆盖对话页 | 渲染正确 | ✅ |
| Dart 协议模型 | `data/models/` 与 Go proto 对照 | 字段一致（CI protocol-compat） | ✅ |

### M0-b：K 信令服务

| 验证项 | 方法 | 通过标准 | 阻断 |
|---|---|---|---|
| 配对状态机 | 单测：code 冲突 409、TTL 过期、5 次锁、全局上限 503 | 全过 | ✅ |
| WS 认证（无状态） | 单测：devId 自洽、签名错、ts 过期 | 4401/4402/4403 正确 | ✅ |
| 路由 O(1) | 单测：online/offline/unavailable、僵尸清理 | 全过 | ✅ |
| 限速 | 单测：devId/IP token bucket | 超限 429 | ✅ |
| 集成 | mock S + mock C，配对 + offer/answer 透传 | 完整流程通 | ✅ |
| 安全-爆破 | 脚本批量试码 | 5 次锁生效 | ✅ |
| 安全-伪造 | 错签名连 WS | 拒绝 | ✅ |
| 优雅关闭 | SIGTERM | 客户端收 server_shutdown | ✅ |
| 覆盖率 | `go test -cover` | > 80% | ✅ |
| 部署 | docker-compose up + /healthz | 绿 | ✅ |
| 协议向量 | K 与 proto/testvectors 对照 | 一致 | ✅ |

### M1：mobilebridge 桥接器

| 验证项 | 方法 | 通过标准 | 阻断 |
|---|---|---|---|
| crypto | 单测：Ed25519/X25519/AES-GCM 往返；constant-time 比较 | 全过 | ✅ |
| frame | 单测：seq 倒退丢、tag 错丢、分方向反射失败、2^32 rekey 触发 | 全过 | ✅ |
| handshake | 单测：hello_c 验签、未配对/吊销不回 hello_s、Finished 错断 | 全过 | ✅ |
| command_router | 单测：各命令权限、超权 forbidden、office_run 路径注入拒 | 全过 | ✅ |
| pairing | 单测：双向确认、code 冲突重试、指纹不符拒、吊销后连失败 | 全过 | ✅ |
| event_sink | 单测：多 C 广播、subscribe_tab 路由、tab 不存在 | 全过 | ✅ |
| tabEventSink 注入 | 验证 `desktop/tabs.go` 改动不影响桌面现有渲染 | 桌面回归测试过 | ✅ |
| **Go e2e** | 同进程双 goroutine C/S，经 mock K，配对→握手→帧→命令→事件 | 整链路通 | ✅ |
| 协议向量 | 与 K、与 proto/testvectors 三方对照 | 一致 | ✅ |
| 覆盖率 | `go test -cover` | > 80% | ✅ |

**Go e2e 是关键 gate**：M1 结束时整条协议链路在 Go 内验证完，不依赖手机。

### M2：同 WiFi 真机 + E2E 加密

| 验证项 | 方法 | 通过标准 | 阻断 |
|---|---|---|---|
| Go e2e 升级 | 真 PeerConnection（非 mock），DataChannel + 握手 + 帧 | 通 | ✅ |
| 真机配对 | Android 扫 hiq 二维码 | 配对成功 + 双向确认 | ✅ |
| 真机连接 | 同 WiFi，DataChannel 建立 + 加密握手 | ENCRYPTED 状态 | ✅ |
| 命令往返 | 手机发 submit，桌面执行，事件回流 | 端到端通 | ✅ |
| 加密验证 | Wireshark 抓包看 DataChannel 流量 | 仅 DTLS + 密文，无可读内容 | ✅ |
| 多 C | 2 个手机同时连一个桌面 | 各自独立流，不串台 | ✅ |

### M3：跨网打洞

| 验证项 | 方法 | 通过标准 | 阻断 |
|---|---|---|---|
| 同 LAN 直连 | host candidate | 100% 可达 | ✅ |
| 跨网打洞 | 手机 4G ↔ 家宽桌面，统计 50 次 | cone NAT 成功率 > 90% | 非阻断（统计） |
| UPnP | 路由器支持时 | 自动映射成功 | 非阻断 |
| 对称 NAT 失败 | tc/netem 模拟 | 优雅失败 + UX 提示 + 诊断 | ✅ |
| ICE restart | 手机切 WiFi↔4G | 自动重连不断 | ✅ |
| TURN opt-in | 开启 turn_enabled | 中继模式可用，UI 明示 | 非阻断 |
| 保活 | 25s ping，NAT 不回收 | 1 小时不断 | ✅ |

### M4：对话 Tab（Android）

| 验证项 | 方法 | 通过标准 | 阻断 |
|---|---|---|---|
| wireEvent 渲染 | widget 测试每种 kind | text/reasoning/tool/approval/usage/compaction 全渲染 | ✅ |
| 流式性能 | performance overlay，高频 delta | 稳定 ≥ 55 fps | ✅ |
| 历史回看 | list_sessions + load_session + 分片 | 完整加载 | ✅ |
| 审批交互 | approval sheet | 允许/拒绝生效 | ✅ |
| 增量同步 | 断线重连 resync | 补拉正确 | ✅ |
| 错误 UX | 各错误状态 | 提示正确（SPEC §11.3） | ✅ |
| 离线队列 | 断网时 submit | 重连后补发 | ✅ |
| App 生命周期 | 切后台/前台/被杀 | 状态恢复正确 | ✅ |

### M5：办公 Tab

| 验证项 | 方法 | 通过标准 | 阻断 |
|---|---|---|---|
| 模板列表 | 从桌面拉取 | 显示正确 | ✅ |
| 触发执行 | office_run | 过程回流 + 结果预览 | ✅ |
| 参数校验 | 路径注入/超大 | 拒绝（forbidden） | ✅ |
| file_drop | 投文件 + sha256 | 落地 incoming/ 正确 | ✅ |
| 类型白名单 | 投可执行文件 | 拒绝 | ✅ |

### M6：iOS + 上架

| 验证项 | 方法 | 通过标准 | 阻断 |
|---|---|---|---|
| iOS 编译 | Mac `flutter build ipa` | 成功 | ✅ |
| iOS 真机 | debug | 功能与 Android 一致 | ✅ |
| Keychain | 私钥存储 | 安全，App 重装不丢 | ✅ |
| 后台策略 | 切后台 | 仅前台 MVP，回前台 resync | ✅ |
| 合规 | `ITSAppUsesNonExemptEncryption=NO` | App Store 审核过 | ✅ |
| 隐私问卷 | App Store Connect | 填写正确 | ✅ |
| TestFlight | 内测分发 | 可安装 | ✅ |

---

## 4. 贯穿 CI（每次 PR / push 都跑）

| 流水线 | 内容 | 失败动作 |
|---|---|---|
| `hiq-ci` | `go vet` + `golangci-lint` + `go test ./...` + `CGO_ENABLED=0 go build`（Win/macOS/Linux） | 阻断合并 |
| `linkpeer-ci` | `dart analyze` + `flutter test` + `flutter build apk --debug` | 阻断合并 |
| `protocol-compat` | Go/Dart 双向解析 testvectors JSON，字段比对 | 阻断合并 |
| `invariants` | 跑不变量断言（seq 单调、nonce 不碰撞、devId 自洽、帧版本） | 阻断合并 |
| `fuzz`（nightly） | 握手/帧/签名/SDP fuzz，累计 ≥ 1h | 报告（严重则阻断） |

---

## 5. 验证矩阵（汇总）

| 验证类 | M0-spike | M0-a | M0-b | M1 | M2 | M3 | M4 | M5 | M6 |
|---|---|---|---|---|---|---|---|---|---|
| L1 协议一致 | — | ◐ | ✅ | ✅ | ✅ | — | ✅ | — | ✅ |
| L2 单元 | ◐ | ◐ | ✅ | ✅ | — | — | ✅ | ✅ | — |
| L3 集成 | — | — | ✅ | ✅ | — | — | — | — | — |
| L4 e2e | ◐ | — | — | ✅Go | ✅真机 | ✅跨网 | — | ✅ | ✅iOS |
| L5 不变量 | — | — | ✅ | ✅ | ✅ | — | — | — | — |
| L6 安全 | — | — | ✅ | ✅ | — | — | — | ✅ | — |
| L7 性能 | — | ◐ | — | — | ◐ | ✅ | ✅fps | — | — |
| L8 跨平台 | — | ✅Android | — | — | ✅ | ✅ | ✅ | ✅ | ✅iOS |

✅ = 该阶段必做；◐ = 部分涉及；— = 不涉及

---

## 6. 阻断规则与失败处理

### 阻断项（失败必须修，不进下一阶段）

- 所有 L1 协议一致性
- 所有 L5 不变量
- L2 单元测试
- L6 安全测试
- M0-spike 四项（技术可行性 gate）
- M1 的 Go e2e
- M2 的真机端到端 + 加密验证
- 性能：M4 的 fps ≥ 55

### 非阻断（记录、统计、迭代）

- 跨网打洞成功率（统计，但有最低线 90% 触发优化）
- APK/IPA 大小（超标警告）
- UPnP 成功率（家宽场景统计）
- fuzz 发现的非常规 crash（nightly 报告，严重才阻断）

### 失败处理流程

```
验证失败
  ├─ 阻断项 → 建issue → 修复 → 重验 → 过了才进下一阶段
  ├─ 协议一致失败 → 可能是设计问题 → 回 VERIFICATION 复审 → 修 proto + 三端同步
  └─ spike 失败 → 触发 contingency（如独立进程）→ 更新 PROTOCOL/SPEC → 重规划
```

---

## 7. 性能与容量验证（NFR）

| 指标 | 目标 | 验证阶段 | 方法 |
|---|---|---|---|
| 冷启动 < 2s | linkpeer | M4 | 真机计时 |
| 连接建立 < 3s（同 LAN） | 全链路 | M2 | 真机 |
| 连接建立 < 8s（跨网） | 全链路 | M3 | 真机 |
| 消息延迟 < 300ms（LAN） | 链路 | M2 | ping RTT |
| 流式 ≥ 55fps | UI | M4 | performance overlay |
| 电量 < 5%/h（前台） | App | M4 | 真机 1h |
| K 7500 并发 WS | 容量 | 上线前 | 压测脚本 |
| K CPU < 10% 稳态 | 容量 | 上线前 | 压测 |
| 内存 < 400MB（K） | 容量 | 上线前 | 压测 |

5000 规模压测在上线前用专用脚本（K 起来 + 5000 mock S 长连 + 模拟敲门），验证 ENGINEERING §10 的容量承诺。

---

## 8. 真机 / 跨网 dogfood（人工，但要有脚本辅助）

| 场景 | 频次 | 工具 |
|---|---|---|
| 同 WiFi 对话 | 每个 M2+ PR | 真机 + 桌面 |
| 4G 跨网 | M3 起 weekly | 手机切 4G |
| 多设备并发 | M3 后 | 2+ 手机 |
| 长时间保活（1h+） | M3 后 weekly | 真机后台跑 |
| 弱网（限速/丢包） | M4 | Android Studio 模拟器网络限速 |
| 网络切换 | M3 | WiFi↔4G 来回切 |

诊断包导出（SPEC §11.x）让 dogfood 期间的问题可上报。

---

## 9. 验证就绪定义（每个阶段的 Done）

每个里程碑「Done」= 该阶段所有阻断验证项绿 + CI 全过 + dogfood 无 P0 问题。非阻断项记录在 issue tracker，不阻塞进入下一阶段。

这是把 ENGINEERING §9 的 DoD 从「产出」细化到「验证通过」。
