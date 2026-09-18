# Hiq 运维能力（netdev）规格说明书

> **版本**: v1.1 | **日期**: 2026-08-18 | **状态**: P0-P5 全部落地（13+ 次提交；附录 B-1/B-9 已裁决并实现）。遗留：真机验证、Linux CI race 检测、boot 级密封断言测试、netprobe SFTP 自动部署
> **关系**: 本篇为已落地系统的 as-built 规格；扩展规划（R1-R6：新目标类型/日志面/安全中心/事件时序/UI 契约）见 `NETDEV_SPEC_V2.md`，冲突处以 v2 附录 A 为准
>
> **目标**: 为 hiq 增加第三个产品 profile「运维」——面向华为/思科/中兴路由器、交换机、
> 安全设备的**诊断优先**管理能力：一只结构性只读的手帮人找到问题，一只人签字的执行手落地变更。

---

## 一、背景与定位

### 1.1 用户与关键时刻

| 用户 | 画像 | 核心诉求 |
|------|------|----------|
| 老张（资深网工） | 15 年经验，手速快，不信任 AI，最怕背锅 | 故障时 3 分钟内摆齐相关设备状态/日志/最近变更；AI 别挡路；审计自证清白 |
| 小李（新手） | 2 年经验，记不住厂商命令差异 | 自然语言意图 → 正确命令 → 结果解读 |
| 王经理（合规） | 要过等保，要审计 | 配置是否发云端（脱敏）；审计能否导出；变更谁批的 |

五个决定成败的时刻：**首台设备 2 分钟连上**、**第一个带证据链的 Finding**、**审阅第一份提案**、**事后审计回放**、**报告一键导出**。

### 1.2 产品定位

**诊断的手 + 人签字的执行器**。AI 的价值集中在读：跨设备采集、关联配置/日志/拓扑/指标、形成假设、只读验证、给出带证据的结论。写操作永远以「变更提案」形态出现，由人整份审批后经执行器落地。

关键安全哲学：**能力隔离优于策略限制**。诊断手的只读是代码结构保证（写命令无执行路径），不是提示词约束——即使模型被注入/被骗，也写不了任何东西。

### 1.3 非目标

- ❌ 不做 agent 自主写操作（无任何模式放开）
- ❌ 不做反向外联、常驻持久化、通用 socks/端口转发（与渗透工具的分水岭）
- ❌ 不做多用户 RBAC（单用户桌面产品，审计记录操作者为本机用户）
- ❌ 初期不做 gNMI/流式遥测、syslog/trap 常驻接收、中兴 NETCONF
- ❌ 不在设备侧安装任何组件（设备永远是零安装的服务端）

---

## 二、总体架构

### 2.1 集成形态：第三 profile

netdev 是现有 profile 体系的第三个成员（`dev` / `cowork` / **`netdev`**），复用现有机制：独立会话分区、独立记忆子树、独立技能白名单、可钉选模型、`SwitchProfile` 整体重建。工具以 `netdev_*` 家族注册，**仅**在 netdev profile 的 boot 分支组装。

### 2.2 分层

```
┌───────────────────────────────────────────────────────────────┐
│ UI 层: NetDevLayout（三栏）+ 管理面六页 + 设置页 netdev tab      │
├───────────────────────────────────────────────────────────────┤
│ 护栏层: 命令分类器 │ 提案流水线 │ scopes 白名单 │ 审计 │ 紧急停止│
├───────────────────────────────────────────────────────────────┤
│ 工具层: netdev_connect / exec / read_config / discover /       │
│         topology / proposal_* / inspect_*                      │
├───────────────────────────────────────────────────────────────┤
│ 驱动层: huawei(vrp) │ cisco(ios/iosxe) │ zte(zxr10) + 版本quirk│
├───────────────────────────────────────────────────────────────┤
│ 传输层: SSH 底座（Reasonix 移植）│ Telnet │ Serial │ SNMP │      │
│         NETCONF │ 跳板链(via) │ netprobe 探针                  │
└───────────────────────────────────────────────────────────────┘
```

### 2.3 双手模型

| | 诊断的手（默认） | 执行的手 |
|---|---|---|
| 能力 | 全部只读采集：display/show、SNMP get、日志、拓扑、ping/traceroute | 仅执行已批准提案 |
| 保障 | **结构性只读**：驱动层分类器使写命令无执行路径，收到即转为提案 | 人审整份提案（diff/风险/回滚）→ 备份 → 下发 → 验证 → 异常自动回滚 |
| 交互 | 范围内免确认 | 批准/驳回/导出手动执行，三者并列 |

---

## 三、连接与协议层

### 3.1 协议矩阵与分期

| 协议 | 用途 | 分期 | 备注 |
|------|------|------|------|
| SSH | CLI 主通道 | P0 | 移植 Reasonix 连接层 |
| Telnet | 老设备 | P1 | **显式 opt-in** + 明文警告（见 §3.3） |
| Serial | 初始化/带外 | P2 | go.bug.st/serial |
| SNMP v2c/v3 | 巡检采集、拓扑发现 | P2 | gosnmp；v2c 社区名同等脱敏 |
| NETCONF(830) | 结构化配置 | P2 | x/crypto/ssh 上的 XML 子系统 |
| RESTCONF/HTTPS API | 安全设备 | P3 | 复用 netclient |
| SFTP/SCP/TFTP | 备份/镜像 | P2 | |

### 3.2 SSH 底座（自 Reasonix `internal/remote` 移植）

移植清单（剥离 bootstrap/serve/workbench 等远端运行时）：
- `sshconfig.go`：`ssh -G` 权威解析 + 内嵌解析器回退 + 别名发现（含 Include 递归）
- `host.go`：三层优先级（TOML → ~/.ssh/config → 默认）、ad-hoc 目标解析、ProxyJump 链展开
- `dial.go`：逐跳拨号、逐跳验主机密钥、跳板凭证隔离、首跳走 netclient 代理、握手超时 watcher
- `auth.go`：agent → 密钥（多把按序）→ 密码 → keyboard-interactive；AuthCallback 多公钥源；密钥内存缓存静默重连
- `knownhosts.go`：系统 known_hosts 只读 + 自管 TOFU；**不匹配=硬失败永不弹窗**；算法协商
- 连接监督：keepalive、指数退避重连、状态机（Idle/Connecting/Connected/Reconnecting/Degraded/Stopped）

许可证：两项目同源 MIT，同属本组织，无障碍。

### 3.3 Telnet 约束

无加密、无主机密钥。约束：每设备显式 `allow_telnet = true`；UI 常驻明文警告；建议仅用于带外/隔离网；凭证仍入 secret store（但需知悉明文过网，见附录 B-7）。

### 3.4 多层网络与跳板链

`via` 声明式路由：设备/跳板声明上一跳，可达性为传递闭包。深层访问 = 在嵌套 `direct-tcpip` 通道的最后一跳开口。**跳板只能人注册**（agent 无工具自建穿透链）；`[netdev]` 全局钉死，项目级 TOML 注入无效。

SNMP（UDP）与 ICMP 不穿 SSH 隧道 → 由 netprobe 探针在跳板上执行（§5.2）。

---

## 四、驱动层

### 4.1 驱动矩阵

| driver | 覆盖 | P1 承诺范围（收窄） |
|--------|------|--------------------|
| huawei-vrp | VRP5/VRP8（S/CE/AR/USG） | VRP8 交换系列优先 |
| cisco-ios | IOS / IOS-XE | IOS 15 / IOS-XE 主线 |
| zte-zxr10 | ZXR10 系列 | P2（公开资料最少，最后做） |

同一 driver 内以**版本 quirk 表**处理语法差异，不另立 driver。

### 4.2 命令分类器（结构性只读的执行点）

每命令经 driver 分类为四类：

| 类 | 例子 | 处置 |
|----|------|------|
| read | display / show / ping / traceroute | 免确认执行 |
| write | 配置下发、undo、clear（含伪装成读的写，如 clear counters） | **不执行**，转提案 |
| dangerous | reboot、delete、format、镜像升级 | 只能出现在提案且触发二次确认 |
| **unknown** | 不在知识库中 | **默认按 write 处置**（转提案），Finding 内提示用户补充归类（见附录 B-1） |

### 4.3 回显处理

分页关闭（per-driver：`screen-length 0 temporary` / `terminal length 0`）、提示符状态机（用户视图/系统视图/配置视图）、命令回显剥离、**错误回显识别**（判断命令成败的依据）、编码处理（UTF-8 优先，按设备配置可切 GBK，`golang.org/x/text` 转换）。

---

## 五、探测与发现

### 5.1 两种模式

- **隧道模式**：经跳板链的 TCP 端口探测（22/23/161/830 + banner 指纹），适合 /24 定向核查
- **netprobe 模式**：静态 Go 单二进制（gosnmp + TCP/ICMP 内置），SFTP 上传至跳板执行，输出 JSON 后退出，零驻留。用于 UDP/ICMP/大网段

netprobe 不可部署时的降级路径见附录 B-4。

### 5.2 发现流水线

探测结果（存活 IP + 端口 + banner + SNMP sysDescr 厂商指纹）→ **待确认区**（SQLite）→ 人工补凭证 → 转正写入全局 TOML。发现不自动成跳板、不自动可连——**看到 ≠ 可连**。

### 5.3 拓扑

CDP/LLDP 邻居表 + ARP/MAC 关联生成 L2/L3 图。邻居数据里出现 scope 外设备仅展示为「未纳管」节点，不可点击连接。

---

## 六、护栏体系

### 6.1 六条不变量（任何模式不关闭）

1. 结构性只读：诊断手无写通道
2. 提案式写路径：确认对象是整份变更而非单条命令
3. 范围白名单：只可探测/连接清单内设备与 scopes 内网段
4. 全量审计：结构化记录（设备/命令/回显/模式/路由/操作者）
5. 人工跳板注册：agent 无自建穿透链能力
6. 模式人切：agent 无工具切换模式或修改 scopes

### 6.2 模式

- **诊断模式**（默认）：§2.3 诊断手
- **评估模式**：engagement 信封（工单号/scopes/有效期/授权人），到期自动回落；能力=范围内扫描、配置合规审计（CIS/等保基线）、默认/空口令核查（预算内）、攻击面分析、报告。不做 exploit 触发

### 6.3 提案状态机

```
draft → approved → executing → done
                    ↘ partial → (人工裁决: 回滚已执行部分 | 继续)
                    ↘ failed  → 自动回滚已执行部分
```

备份 → 逐台下发 → 逐台验证 → 任一台失败即停后续（滚动非齐发）。断线/紧急停止时 executing 的语义见附录 B-2/B-5。

### 6.4 组策略与变更窗口

设备组：`read-only`（连提案都不生成）/ `proposal`（可提案）/ `proposal+confirm2`（二次输密码）。变更窗口挂组（如 `tue,thu 22:00-24:00`），窗口外批准按钮禁用并显示原因。

### 6.5 紧急停止

顶栏红色按钮 + 全局快捷键：立即断开全部会话、暂停全部巡检任务、冻结提案执行。语义与执行中提案的交互见附录 B-5。

### 6.6 审计

记录：时间、操作者、模式、设备、路由、命令/提案、回显摘要、结果。落 evidence 体系；可按设备/时间/模式过滤导出；会话全程录像可回放。审计落盘前的脱敏见 §8.1 与附录 B-3。

---

## 七、隔离设计（与 dev/cowork 硬隔离）

### 7.1 工具面双向密封（新增 `tool_scope`）

| 方向 | 规则 | 防的攻击 |
|------|------|----------|
| dev/cowork → netdev | `netdev_*` 仅在 netdev profile boot 分支注册，其他 Registry 中**不存在**（不用 `HiddenTools`——那是软隔离） | 恶意仓库指令让编码会话摸设备 |
| netdev → dev/cowork | netdev profile **无 bash、无文件写**；工具集 = `netdev_*` + `rag_search`(netdev 命名空间) + 受限文本处理工具（见附录 B-12） | banner/MOTD 注入借 bash 旁路绕过分类器 |

### 7.2 数据面

- 凭证：secret store `netdev/*` 命名空间，仅 netdev 会话传输层内部解密；dev/cowork 的 bash 沙箱 `forbid_read` 覆盖凭证库路径
- 会话分区：profile 体系自带（设备配置全文不出 netdev 分区）
- RAG：`netdev:` collection 命名空间（厂商文档、配置备份）。边界在 Store 检索层强制（空域搜索排除该命名空间；`"netdev:"` 整域查询只搜该命名空间），工具层双侧钉死——netdev 会话只有 `netdev_rag_search`/`netdev_rag_import`（作用域锁死在命名空间内，无法用参数逃逸），dev/cowork 的 rag_search/rag_list/rag_graph/rag_mindmap/rag_import/rag_delete 显式点名 `netdev:` 前缀一律拒绝。有回归测试钉住（TestNetDevRAGNamespaceIsolation）

### 7.3 配置面

`[netdev]` 全局钉死；netdev 会话 `load_project_instructions = false`——不加载项目级 instruction/skills/hooks（运维对象是设备不是仓库，恶意仓库的 AGENTS.md 不得影响设备会话）。

### 7.4 MCP 面（plugin_allowlist 严格白名单）

外部 MCP 服务器是密封的工具面之外的旁路：MCP 工具名不在 `tool_scope` 的 RemovePrefix 剥除清单内，一个带写/exec 能力的 MCP 会直接穿透 §7.1 的只读封印。因此 netdev 采用 **`plugin_allowlist = true`**（builtin floor 钉死，用户不可关）：

- `plugins` 列表作为严格白名单：**空列表 = 全部外部 MCP 隐藏**（内置 web_search/web_fetch 等非 MCP 工具不受影响）。
- 用户要用某个 MCP，必须在 `[[profiles]] name="netdev"` 里显式点名——只能加名字，不能关白名单。
- 执行点双侧强制：boot 的启动 spec 过滤（含 ExtraPlugins）+ 热连接路径（`ConnectMCPServer` / `ConnectConfiguredMCPServer` / `AddMCPServer` / 导入，统一在 `connectMCPSpec` 汇合点拒绝）。配置读不出来时对命名 profile **fail-closed**。
- 设置页 MCP 列表按当前模式标注「当前模式已隐藏」（`Capabilities` 的 `profileHidden`），与技能页同体验。

### 7.5 技能面（继承编码全集）

netdev 的技能白名单 = 编码全集（init/install-capability/test/explore/research/review/security-review）+ `netdev-help`（用户方向 2026-08-20：运维先把编码内容全部拿过来）。分工原则：**白名单管可见性，封印管行为**——需要 shell/写路径的技能（test、init）在封印下降级为只读分析，这是预期效果而非缺陷。prompt addon 携带技能路由表，且与 cowork 共用「被禁技能的路由行自动裁剪」逻辑（`NetdevPromptAddon`/`CoworkPromptAddon` → `pruneSkillRoutingRows`）。

诊断 playbook 技能库（OSPF/BGP/接口，§P2 规划）与 rag 接入 netdev 命名空间（§7.2）为后续批次。

---

## 八、隐私与脱敏

### 8.1 脱敏器

设备回显/配置进入 **LLM 上下文之前**与**审计/transcript 落盘之前**双重过脱敏器：SNMP 团体字、`password cipher ...` 行、密钥材料、认证配置段，以规则（per-driver 正则 + 通用模式）替换为占位符，原文仅存于本地 evidence 加密存储。规则可在设置页预览与增补。

### 8.2 模型选择

netdev profile 可钉选独立模型（含本地模型）——「配置不出机器」的完全体，给合规敏感用户。

---

## 九、配置与设置

### 9.1 三个家（配置不都塞设置页）

| 家 | 内容 | 形态 |
|----|------|------|
| 设置 → 运维页（第 15 个 SettingsTab `"netdev"`） | 个人偏好与策略 | `NetDevSettingsView` + `SetNetDevSettings` 桥（照 cowork 模式） |
| `hiq.toml [netdev]` | 资产骨架，可复制、全局钉死 | TOML |
| 运维管理面（profile 内六页） | 资产/流程/证据（数据非配置） | SQLite + 全局 TOML |

### 9.2 `[netdev]` schema

```toml
[netdev]
enabled               = true
default_mode          = "diagnose"     # diagnose | assess
redact_before_llm     = true
audit_retention       = "180d"
proxy_device_traffic  = false          # 设备面默认不走系统代理
max_sessions_per_device = 2            # VTY 保护
snmp_rate_limit       = 50             # 包/秒

[[netdev.groups]]
name          = "core"
policy        = "read-only"
change_window = "tue,thu 22:00-24:00"

[[netdev.groups]]
name   = "access"
policy = "proposal+confirm2"

[[netdev.hops]]
name         = "l1"
host         = "202.1.1.1"
user         = "ops"
password_env = "NETDEV_PWD_L1"

[[netdev.devices]]
name         = "core-sw-1"
vendor       = "huawei"
os           = "vrp8"
model        = "S5735"
address      = "10.0.0.1"
via          = ["l1"]
group        = "core"
protocols    = ["ssh", "netconf"]
username     = "netops"
password_env = "NETDEV_PWD_CORE1"
encoding     = "auto"                # auto | utf-8 | gbk
allow_telnet = false

[netdev.devices.snmp]
version   = "v3"
username  = "snmpv3user"
auth_env  = "NETDEV_SNMP_AUTH"
priv_env  = "NETDEV_SNMP_PRIV"

[netdev.discovery]
scopes    = ["10.30.0.0/16"]
rate      = 50
mode      = "auto"                   # tunnel | probe | auto
probe_fallback = "tunnel"            # netprobe 不可部署时（附录 B-4）

[netdev.assessment]                  # 评估模式信封（激活时才生效）
engagement_id = "ASSESS-2026-018"
scopes        = ["10.30.0.0/16"]
expires       = "2026-08-25"
approver      = "zhang@corp"
```

密码/密钥永不入 TOML，只写 `*_env` 名，真值经设置页写入 secret store。

### 9.3 设置页字段

默认模式、脱敏开关与规则预览、审计保留期、驱动行为（分页/编码/超时）、采集并发与 SNMP 速率、模型选择（可指本地）、设备流量代理开关、紧急停止快捷键、自定义命令黑名单（追加到 dangerous 类）。

### 9.4 管理面六页

设备与跳板（含 `~/.ssh/config` 导入、待确认区）│范围与授权│提案中心│审计查询（过滤/回放/导出）│巡检任务│知识库（厂商文档导入，**用户自备，hiq 不分发**——版权，附录 B-10）

---

## 十、UI 设计

### 10.1 NetDevLayout（三栏，与 CoWorkLayout 平级）

```
┌─────────────────────────────────────────────────────────────┐
│ hiq · 运维    设备 24/在线 21    模式: 诊断·只读 🔒  [⏹停止]│
├───────────┬──────────────────────────────────┬──────────────┤
│ 侧栏       │ 对话流（诊断过程）                 │ 上下文面板     │
│ 设备分组    │  ToolCard: core-sw-1 ▸ display…  │ 当前设备摘要    │
│ 堡垒链     │  Finding: ⚠ OSPF 邻居 down        │ 拓扑缩略(高亮)  │
│ 巡检任务    │    证据 3 条 [展开] 建议: 提案 #12 │ 本次发现 n 条   │
├───────────┴──────────────────────────────────┴──────────────┤
│ 提案队列: #12 待审阅 · 今日审计: 只读 214 / 写 0 / 提案 1       │
└─────────────────────────────────────────────────────────────┘
```

### 10.2 八视图

对话流（ToolCard + Finding 卡片）│**操作实况**（右栏实时监视，见 §10.4）│拓扑图（可交互，故障域高亮）│设备清单页│提案审阅页（逐台 diff/风险/回滚 + 批准/导出手动执行/驳回）│审计回放│巡检报告│TOFU/凭证对话框（移植 Reasonix 三件套）

### 10.3 UI 不变量

模式徽章常驻顶栏；写操作永远以提案卡片出现、绝不以「已执行」出现；每条 Finding 证据可追溯至原始回显（脱敏后视图 + 本地密文原件）。

### 10.4 操作实况（live ops panel，已实施 2026-08-21）

聊天界面保持不动；右栏首屏页签「实况」把 agent 在设备上的每个动作实时呈现给人——监督权在人，先看得见才谈管得住。四层显示：

1. **预算条**：本轮命令 n/预算（`turn_command_budget`）、活动设备数、拦截/写计数——节奏与风险一眼可感。
2. **设备操作卡**（仅有活动的设备，空闲折叠）：连接状态灯（跟随 transport 状态机：连接中/已连接/重连中/空闲回收/断开）、VTY 占用 n/cap、执行中命令（耗时 + 读/写/危险徽章）、**终端质感输出尾随**（等宽字体、`[时刻] 设备#` 命令分隔行、流式上屏、悬停暂停、点击展开）、最近命令 chips（分类色 + 状态图标）。
3. **护栏事件行**：guardrail/分类器拒绝单独成行（红色），拒绝原因可见——「被拦下来」必须显眼。
4. **空态引导**。

数据面：`internal/netdev/live.go` 观察者（cmd_start/cmd_output/cmd_end/cmd_refused/conn），输出差量在 `Session.Run` 的 15ms 轮询里按行对齐发射，**出包前 ANSI 剥离 + 脱敏**（密码永不上屏）；`max_sessions_per_device` 在 NETCONF 路径强制（CLI 会话 + 并发 RPC 共享 VTY 预算，超限拒绝并审计）；桌面层 `netdev:live` 专用 Wails 通道 ~40ms 合帧；SNMP/NETCONF 一次性操作只有 start/end 生命周期。回归测试：`TestLiveObserverCommandLifecycle` / `TestLiveChunkSanitization` / `TestLiveVTYCapEnforced` / `TestLiveStateSnapshot`。

### 10.5 长任务与管控路线图（B-E 批）

「监控级管控」的分批路径（每批独立可验收）：

- **B 批·审计可信**：审计 JSONL 哈希链（每条带 prev/hash，SHA-256，追加式防篡改、启动校验、损坏点告警 Finding）；`audit_retention` 真正生效（按天滚动+过期清理）；审计回放视图（§10.2 既有规划）。
- **C 批·长任务 harness**：多步骤 Job 引擎（步骤带 expect/timeout/retry/on-fail，断点续跑，watchdog：断连暂停恢复 + 墙钟/命令数/熔断预算）；诊断 playbook（netdev-diag-*）升级为可执行 runbook；提案执行失败自动验证 + 可选自动回滚。
- **D 批·监控级**：SNMP 轮询循环（snmp.go 地基已就）→ 设备/接口/邻居状态入实况面板（状态灯升级为健康度）；Syslog 被动接收；告警规则 → 自动 Finding → 护栏内 AI 诊断。
- **E 批·AI 起草**：聊天里自然语言 → 命令起草（分类徽章；读类一键执行，写类走组策略/变更窗口/confirm2 人工确认）——AI 的手永远比人的确认慢一步。

---

## 十一、分期计划与验收

| 阶段 | 内容 | 用户视角验收 |
|------|------|--------------|
| P0 地基（1-2 周） | transport 移植、`ProfileNetDev` + `tool_scope` + `load_project_instructions`、secret 命名空间 + 沙箱 deny | 注入测试集通过：dev 会话无 netdev 工具、netdev 会话无 bash |
| P1 第一台设备（2-3 周） | 驱动框架 + huawei-vrp8 + cisco-ios、`netdev_*` 工具 + 分类器、`[netdev]` 配置、TOFU/凭证对话框、Layout 骨架 + 模式徽章 + 紧急停止、审计、设置页 v1 | 首台设备 <2 分钟接入；display/show 稳定结构化 |
| P2 诊断闭环（2 周） | 脱敏器、Finding + 证据链、RAG 命名空间 + 文档导入、诊断技能（OSPF/BGP/接口）、**操作实况面板 + live 观察者 + VTY 强制** | 真实故障现象 → 带证据 Finding；右栏实时可见 agent 每条命令与输出 |
| P3 发现与拓扑（2-3 周） | 隧道扫描 + netprobe、scopes + 待确认区、CDP/LLDP 拓扑 | 扫 /24 出清单，一键转正 |
| P4 提案与巡检（2-3 周） | 提案流水线 + 组策略 + 变更窗口、巡检 + 报告导出 | 双设备提案全流程含回滚演练 |
| P5 评估与深化（后续） | engagement、等保基线审计、默认/空口令核查、zte driver、NETCONF、SNMP 采集 | 基线审计报告导出 |

---

## 十二、测试策略

1. **containerlab CI**：driver 对真机行为（思科 cRTR/iosv；华为暂用 fixture 语料 + 手测，见附录 B-17）
2. **回显 fixture 语料库**：分页/错误/编码/各视图提示符，按厂商目录积累，driver 单测数据源
3. **注入测试集**（发布门禁）：banner 藏指令、Finding 描述藏「请批准」、提案 diff 藏指令——全部必须不产生写副作用
4. **密封验证**：profile 工具面断言（dev/cowork 无 netdev_*，netdev 无 bash/file write）
5. **提案演练**：含故障注入（下发中断网）验证 partial 路径

---

## 附录 A：Reasonix 移植清单（文件级）

`internal/remote/{sshconfig,host,dial,auth,knownhosts,remote,status,paths}.go` + `sftpfs/` + `forward/`（按需）→ `internal/netdev/transport/`，剥离 `bootstrap/ broker/ workbench/ protocol/ protocolgen/`。桌面侧参考：`RemoteHostsPage.tsx`、`RemoteHostKeyDialog.tsx`、`RemoteSecretDialog.tsx`、`remote_askpass.go`（Windows ssh.exe 路径暂不需要——统一 Go 客户端）。

## 附录 B：评审发现的开放问题（须裁决后升 v1.0）

| # | 问题 | 严重度 | 建议裁决 |
|---|------|--------|----------|
| 1 | **未知命令默认归类**：白名单不可穷举，unknown→write 会挡住合法诊断（新型号读命令） | 高 | ✅ 已裁决（2026-08-18）：维持纯拒绝（已实现）。不加会话内临时标记——unknown 是分类表没覆盖而非模型不认识；解药是知识库生长：P2 厂商文档导入驱动表扩充 + 设置页永久归类。注意放行依据永远是表，不是模型的自我声明（防注入） |
| 2 | **提案 partial 语义**：3 台改 2 台断线，回滚本身也可能失败 | 高 | partial 冻结等待人工裁决（默认建议回滚已执行部分，但按钮是人按）；回滚失败进入 failed-and-alert 状态，强提示 + 审计红色标记 |
| 3 | **审计/transcript 含敏感回显**：`display current` 输出含 cipher 密码行 | 高 | 脱敏器覆盖三处：LLM 前、审计落盘前、transcript 落盘前；密文原件单独加密存储并限时保留 |
| 4 | **netprobe 部署受阻**：生产跳板常见 noexec/只读 home/EDR 拦截 | 高 | `probe_fallback = "tunnel"`：降级为纯 TCP 隧道扫描（无 UDP/ICMP），并提示能力降级；不提供绕过手段 |
| 5 | **紧急停止 × 执行中提案**：停止即中断 executing，制造 partial | 中 | 紧急停止前若 executing：弹「提案 #N 执行中，停止将产生部分变更」二次确认，停止后直接进入 B-2 的 partial 裁决界面 |
| 6 | **并发与 VTY 枯竭**：巡检与对话同时跑，老设备仅 5 VTY | 中 | `max_sessions_per_device`（默认 2）+ 每设备信号量；巡检任务与交互会话共享配额，超限排队 |
| 7 | **Telnet 凭证明文过网** | 中 | 已有 opt-in + 警告；补充：telnet 设备的凭证在 secret store 标记 `cleartext-risk`，审计记录telnet 会话的明文性质 |
| 8 | **驱动承诺过宽**：vrp8 覆盖 S/CE/AR/USG 不现实 | 中 | 已在 §4.1 收窄 P1 范围；quirk 表随 fixture 语料渐进扩充 |
| 9 | **弱口令核查与尝试预算矛盾**：3 次预算无法做字典核查 | 中 | ✅ 已裁决（2026-08-18）：分级可选——默认档「默认/空/工号口令」（预算内安全）；高级档开放字典核查（字典用户自备、hiq 不分发），两档都强制尝试预算 + 先读设备锁定策略建模（防自锁）。评估模式整体按开放能力面设计 |
| 10 | **厂商文档版权** | 低 | 已写明用户自备、不分发 |
| 11 | **拓扑邻居含 scope 外设备** | 低 | 已定义「看到≠可连」，scope 外仅灰显 |
| 12 | **无 bash 后的诊断文本处理能力**：模型需要 join/统计/过滤多设备输出 | 中 | 新增受限 `netdev_notebook` 工具：仅对工具返回的结构化数据做内存态查询（类 jq），无文件/网络/进程能力 |
| 13 | **engagement 到期时任务在跑** | 低 | 到期不再受理新命令，进行中任务给 5 分钟宽限后强制收尾（只读无副作用，安全） |
| 14 | **会话 transcript 静态加密**：本地 jsonl 明文存设备配置 | 中 | netdev 分区 transcript 落盘前过脱敏器（同 B-3），密文原件另存；与现有会话存储格式的兼容方案 P1 实现前定 |
| 15 | **GBK 检测策略**：auto 如何判 | 低 | 先按 UTF-8 严格解码，失败且命中 GBK 特征字节再转；设备级 `encoding` 可强制 |
| 16 | **本地模型质量 vs 诊断效果** | 中 | 设置页明示权衡；诊断技能的 prompt 不依赖特定模型能力 |
| 17 | **华为设备 CI 缺真机**：containerlab 无 VRP 镜像（版权） | 中 | 华为 driver 以 fixture 语料 + 手测清单替代 CI；真机验证纳入 P1 验收人工步骤 |
