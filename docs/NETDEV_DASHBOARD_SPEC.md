# NETDEV 大屏家族 SPEC（总览 / 调查链 / 割接 / 发现 / 暴露面）

> **版本**: v2.0 | **日期**: 2026-08-29 | **状态**: **已实施**（D1–D5 全批落地，
> 偏差见 §12；v1.0"总览"部分与 v2.0 扩容一并交付）
>
> **来源**: ①现状核对——netdev 11 个页签全为深挖视图、无聚合状态层（NetDevLayout 页签清单）；
> 早报 `NetDevDailyBriefing`（netdev_app.go:1084）已是文字版聚合；metrics.db 指标环表
> （metrics.go）、findings/proposals/jobs/cutover/审计数据源齐备。②DesRedTeam 界面调研——
> dashboard.js 的加权风险分/新鲜度徽章/状态生命周期、fact-graph 的认知状态可视化、
> monitor.js 的实况时间线。③dsh-pentest 界面调研（无 LICENSE，**只学模式不抄代码**）——
> 分层 DAG 链路图、卡片节点类型标签、连线关系词、顶部内联统计行、子页签切换、图从日志重放。
> ④对话裁决——蓝队主打、五屏按"正在做的事"划分、六列调查链语义、SVG 分层选型（不引
> React Flow）、投影模式、L0/L1/L2 统计分层。
>
> **关联**: OPS_AUTOMATION_PLATFORM_SPEC.md §12（本 spec 是其"§12.0 总览视图"的先行落地）；
> NETDEV_IMPORT_AND_FINGERPRINT_SPEC.md（发现屏依赖其 F1 待确认区/F4 分层发现/F5 暴露面推演，
> 均已落地）；NETDEV_COMPLETION_SPEC.md R4（割接屏依赖其结构化提案步骤/割接模式，已落地）。

---

## §0 背景与问题

**现状**：netdev 界面 11 个页签（context/devices/topology/audit/live/logs/health/browser/jobs/
findings/proposals）全部是"深挖视图"，缺一层"现在的状态是什么"的聚合视图。用户打开应用后
第一个动作是逐个点页签找状态——这正是 COMPLETION_SPEC §0 诊断的"深埋"问题的另一半：
场景卡解决了"入口在哪"，没解决"状态在哪"。

**已有底座**：`NetDevDailyBriefing`（netdev_app.go:1084-1140）每天定时采集"近 24h findings +
提案队列 + 审计读/写/护栏计数 + 备份数"喂给 LLM 出文字早报。它的**客观数据采集部分就是
看板要的聚合器**——本 spec 把这部分抽出为共享结构，早报与看板同源，杜绝两套口径漂移。

**边界澄清**（与既有界面的关系）：
- **live 页签（LiveOpsPanel）= 此刻 agent 在干什么**（秒级流：预算条/设备卡/护栏行）；
  **大屏 = 网络与队列的状态**（分钟级快照）。互不替代，大屏深链到 live。
- 早报 = 同一份聚合的 LLM 叙事端；大屏 = 同一份聚合的可视化端。
- **为什么是五屏**：按"正在做的事"切场景——值守（无任务）/排查（调查任务）/割接（变更
  任务）/发现（纳管任务）/审视（安全任务）。五屏互相不重叠、覆盖运维的一天；砍掉的候选
  （Job 引擎、浏览器控制台、报告导出）分别是既有面板/工作台/导出动作，不占态势屏。

---

## §1 范围

**做**（五批）：
- **D1 后端聚合**——`buildOverviewData()` 共享聚合函数 + `NetDevOverview()` 桥方法 + 内存缓存
- **D2 前端总览屏**——OverviewPanel 六区块 + 深链 + 空态 + i18n
- **D3 早报同源重构**——`NetDevDailyBriefing` 改为消费 `buildOverviewData()`，行为不回归
- **D4 大屏壳与四屏**——§4 壳（场景感知/投影模式/事件驱动刷新）+ 调查链/割接/发现/暴露面
- **D5 统计与留存**——§7 L0 统计口径落地 + L1 三件 journal 留存

**不做**（停车场）：
- 自定义组件/拖拽布局的 BI 化路线；用户自定义屏内容（v1）
- 月级长期趋势图（指标环表仅约两周 5min 点位，不画假精度；journal 压实后可议，见 §7.2）
- 任何写操作、告警规则配置（已有 AlertSetupWizard）、导出（报告体系归 OPS spec §13.2）
- 移动端；**多屏输出/视频流级"大屏投放"**（§4.11 的投影模式是应用内形态，不冲突）
- 时序数据库、WebSocket（JSONL journal + Wails 事件够用，选型记录见 §8.5）
- 其余四屏的 dock 迷你版（两档制只留给总览，避免五份缩略实现）

**安全不变量**：
1. **纯只读**：五屏聚合过程零命令执行、零写桥调用；数据全部来自既有状态（findings/metrics/
   jobs/proposals/audit/config/discovered/cases/cutover）。审计链校验复用 `VerifyAuditChain`
   只读路径。D5 的 journal 写入挂**既有生产者**（巡检/发现/接收器）之后，best-effort，
   journal 失败绝不 fail 生产者（与 RecordDiscovered 同模式）。
2. **诚实分母**：任何百分比/比率必须携带分母与未覆盖原因（§6，不变量级）。
3. **不绕过页签语义**：深链只做页签切换 + 过滤参数，不新建任何直达写操作的快捷入口。
4. **凭证与原始输出不进快照**：所有屏的组装函数只含计数/状态/标题级摘要。调查链屏读取的
   证据文本是 Finding.Evidence 与审计条目中**已脱敏入库**的存量（Exec 出库前已过脱敏），
   组装函数永不触碰原始流。
5. **推演角标**：暴露面屏的路径图与"剪断建议"永带"推演"角标（attackpath.go `Simulated`
   语义），文案不得写成事实断言。

---

## §2 数据模型：五屏组装函数与 OverviewSnapshot

### 2.1 家族组装函数总表（全部纯函数、零会话、可单测）

| 屏 | 组装函数 | 输入面（全部现成） | 桥方法 |
|---|---|---|---|
| 总览 | `buildOverviewData()` (D1) | findings/metrics/jobs/proposals/cutover/audit/config | `NetDevOverview()` |
| 调查链 | `netdev.BuildInvestigationChain(caseID, findingID, hours)` | findings（含 Evidence/Source）+ 审计尾部 + cases entries + proposals + job 步骤态 | `NetDevInvestigationChain()` |
| 割接 | `netdev.BuildCutoverBoard(cutoverID)` | cutover 状态 + 结构化提案步骤 + jobs（预算燃尽）+ restore-verify + 审计流（按单过滤） | `NetDevCutoverBoard()` |
| 发现 | `netdev.BuildDiscoveryBoard()` | 待确认区（discovered.go）+ 层级账本（discovery-layers.json）+ 子网分类/RunState + 拓扑三源 | `NetDevDiscoveryBoard()` |
| 暴露面 | `netdev.BuildExposureBoard()` | 复用 `BuildAttackPaths`（attackpath.go）+ findings 过滤 + CVE 匹配（cve.go）+ 拓扑邻接 | `NetDevExposureBoard()` |

前端 `types.ts` 同步镜像类型；bridge 增五个方法。任一屏组装失败降级为该屏空态
（"数据暂不可用"），不阻塞壳与其他屏。

### 2.2 OverviewSnapshot（总览屏）

```go
type OverviewSnapshot struct {
    GeneratedAt  int64                 // ms；前端据此算新鲜度
    StaleAfterSec int                  // 默认 300（5 分钟，学 DRT stale 阈值）

    Coverage OverviewCoverage          // 资产覆盖（诚实分母）
    Health   OverviewHealth            // 健康状态
    Risk     OverviewRisk              // 风险聚合（含加权分）
    Inflight OverviewInflight          // 在途动作（置顶区块）
    Events   []OverviewEvent           // 近期事件（≤20 条）
    Audit    OverviewAudit             // 审计链健康
    Stats    OverviewStats             // L0 统计扩展（§7.1，v2.0 新增）
}

type OverviewCoverage struct {
    Managed     int   // 清单设备总数（config TOML [netdev.devices]）
    Discovered  int   // 待确认区主机数（指纹 spec F1，已落地）
    Unreachable int   // 最近一次健康轮询不可达数
    NoSNMP      int   // 清单内未配置 SNMP 轮询的设备数（健康分母的显式缺口）
}

type OverviewHealth struct {
    Polled       int      // 参与 SNMP 轮询的设备数（可达率分母）
    Reachable    int
    LastPollAt   int64    // 最近一次轮询完成时间
    FlapAlerts   int      // FlapCount 超限设备数（metrics.go:124 派生信号）
    P90Alerts    int      // P90IfDown 超限设备数（metrics.go:144）
    UptimeSpark  map[string][]float64 // 每设备 uptime 序列（sparkline，取环表近 96 点）
}

type OverviewRisk struct {
    Findings struct {
        Critical, Warning, Info int    // 仅 status != resolved 的 open 项
        OpenTotal               int
    }
    WeightedScore int                // §5 加权分
    RiskLevel     string             // safe|low|medium|high|critical
    CVEMatches    int                // NetDevCVEMatches 命中数
    CVENeedsFeed  bool               // feed 未导入 → 引导态而非 0
    WeakCreds     int                // source=assess 的 open critical Finding 数
}

type OverviewInflight struct {
    ProposalsPending int             // draft 状态（等审批）
    ProposalsWatchable int           // watching 且 WatchUntil 未到期（v2.0 实现恢复该字段）
    JobsRunning, JobsPaused int
    CutoversActive   int             // running/hold 状态
    TerminalsOpen    int             // 人工终端会话数
}

type OverviewEvent struct {
    ID, Severity, Title string
    Source  string                     // syslog|trap|notify|assess|baseline|cve|job
    At      string                     // "2006-01-02T15:04"
}

type OverviewAudit struct {
    ChainOK     bool                  // VerifyAuditChain 最近一次结果
    LastEntryAt string
    Read24h, Write24h, Guardrail24h int // 与早报同源的三计数
}

type OverviewStats struct {            // §7.1 L0 统计——字段级出处见 §7.1 表
    MTTRHours      *float64            // 平均处置时长（resolved 有 CreatedAt+ResolvedAt 才有值）
    Baseline       *BaselineAgg        // nil = 从未跑过基线（引导态，不是 0）
    CVEBySeverity  map[string]int      // 依赖 feed；无 feed 走 CVENeedsFeed 引导
    JobSuccess     [2]int              // {成功, 总完成}（失败+中止计分母）
    CmdMix         map[string]int      // 审计 class 计数（窗口内）
    DeviceByRole   map[string]int      // role 推断分布（role.go）
    ProposalFunnel map[string]int      // 状态机分布
}
```

### 2.3 总览区块 ↔ 数据源 ↔ 深链目标

| 区块 | 数据源（现成） | 深链目标页签 |
|---|---|---|
| 在途动作 ⭐ | proposals（proposal.go 状态）/ jobs（job.go 状态目录）/ cutover / humantty 会话 | proposals / jobs / live（终端） |
| 资产覆盖 | config 设备清单 + health.go 轮询 + DiscoveredHost（discovered.go） | devices（/ 待确认区） |
| 健康 | metrics.db `metric_points` + FlapCount/P90IfDown | health |
| 风险 | findings（finding.go，severity 三级）+ CVE 匹配 + assess | findings |
| 事件流 | findings 按 source=syslog/trap 过滤倒序取 20 | findings（带 source 过滤） |
| 审计 | audit.go VerifyAuditChain（:218-250）+ audit tail 三计数 | audit |
| 统计条（v2.0） | §7.1 L0 全部 | 各明细页签 |

---

## §3 后端：聚合、同源、缓存与事件推送

### 3.1 D1 共享聚合函数

- 新函数 `buildOverviewData() OverviewSnapshot`（放 desktop 侧聚合层，或 internal/netdev 暴露
  原始计数、desktop 组装——实现时按现有分层惯例定）。
- 采集实现**复用现有桥方法的内部路径**（`a.NetDevFindings()` / `NetDevProposals()` /
  `NetDevAuditTail` / `netdev.ListBackups` 同族），不重复造查询。
- **D3 同源重构**：`NetDevDailyBriefing`（netdev_app.go:1084）的"客观数据采集"段替换为消费
  `buildOverviewData()`；LLM prompt 与 markdown 输出格式不变（行为回归验收）。

### 3.2 桥方法与缓存

- `App.NetDevOverview()` → OverviewSnapshot。
- **内存缓存 30s**：看板被多页签/频繁刷新调用时不重算；`force` 参数（页签激活时传 true）穿透。
- **审计链校验降频**：VerifyAuditChain 全链重算开销大，改为"启动时 + 每 10 分钟 + 手动"后台
  刷新缓存值，快照只读缓存（不阻塞聚合）。
- **metrics 聚合下推 SQLite**：UptimeSpark/FlapAlerts 用 SQL 聚合（GROUP BY device + 窗口截取），
  不把环表整表拉回内存。

### 3.3 CVE 空态语义

feed 未导入时 `CVENeedsFeed=true`：风险区块显示"先导入 feed"引导（深链安全工作台 CVE 区），
**不得显示 0**——0 命中与没数据是两件事（诚实分母的特例）。

### 3.4 事件驱动刷新（v2.0：写侧推送替代纯轮询）

- 后端在 findings/审计/jobs/cutover/待确认区/notify **写入成功后**发一个窗口事件
  `hiq:netdev-dash`（payload 仅带 `{screens:[...]}` 变更屏枚举，不带数据本体——数据
  仍走桥方法拉取，避免事件里出现未脱敏内容）。
- 前端消费：dock 迷你总览**零定时器**（纯事件驱动刷新）；bench 当前屏收到事件即拉一次。
- 60s 轮询降级为**兜底**：仅 bench 可见时启用（§4.4 第 4 条暂停规则不变）。
- 事件发送 best-effort（与 journal 同纪律）：发送失败静默，兜底轮询保证最终一致。

---

## §4 前端：大屏家族（1 壳 5 屏）

### 4.1 壳与五屏

五屏共用一个 bench 壳：顶部页签条 + 场景主画布 + 底部审计 ticker。

```text
┌─ 大屏 ── [总览] [调查链] [割接] [发现] [暴露面] ── [⇲收起右栏] [⟳自动/∥暂停] [⛶投影] [×] ┐
│  ┌────────────────────────────────────────────────────────────┐  │
│  │              场景主画布（§4.2/§4.6-§4.9 各自布局）            │  │
│  └────────────────────────────────────────────────────────────┘  │
│  底条（全屏共用）：最近审计动作 ticker ── 点击任意条深链审计原文      │
└───────────────────────────────────────────────────────────────────┘
```

**场景感知默认页**（"正在做的事"决定落点；人工切换后本次会话记住，重启回总览）：

| 激活条件 | 默认落点 |
|---|---|
| 割接模式 running/hold | 割接屏 |
| 分层发现 run 运行中（DiscoveryRunState 活跃） | 发现屏 |
| 经 `hiq://finding/<id>` 深链进入 | 调查链屏（高亮该 Finding 所在链） |
| 其余 | 总览屏 |

**入口三件套**（§4.4 bench 家族约定，五屏一致）：bench chip + 命令面板项（`?bench=overview|
chain|cutover|discovery|exposure`）+ 工作区入口按钮（Finding 卡"查看证据链"、割接横幅
"进入割接大屏"、发现对话框"查看发现大屏"、attack-path 卡"放大"）。另有 Alt+1..5 直切。

**总览是唯一两档屏**（§4.2 dock 迷你档 + bench 档同组件响应式）；其余四屏 bench 专属。
dock 迷你总览与 bench 总览**必须读同一份 OverviewSnapshot**——单一数据源，两处数字永不漂移。

### 4.2 总览屏（D2）

- NetDevLayout 页签条最前新增 `overview`（"总览"），**默认落地页签**（原默认 context 降为第二；
  是否合并 context 见 §11 开放问题）。
- **两档尺寸，一套数据**（全部复用既有机制，零新布局基建）：
  - **Dock 档**（默认 320px，可拖 280–1200）：紧凑单列——在途横条置顶 + 资产/健康/风险三张
    竖排数字卡（无 sparkline、水位条缩为色点+分数）+ 事件 5 条。这是"路过扫一眼"形态。
  - **Bench 档（大屏形态）**：主栏"平级子页卡"——`ndv-bench` 切换条（NetDevLayout.tsx:1102，
    现有 对话/日志/安全 三 chip）**新增"大屏"chip**（v1.0 的单"总览"chip 升级为壳入口，
    点击落在场景感知默认页），主栏整体切换（CutoverView 同款接管模式，:1115-1123），
    Esc/切回 chip 恢复对话（display:none 保挂载，对话上下文不丢）。bench 头部提供
    "收起右侧栏"按钮（走既有 `dockOnClose`/`closeWorkspacePanel` 路径）。
  - 两档渲染**同一个 OverviewPanel 组件**（响应式断点：<520px 紧凑单列，≥800px 双列），
    共享同一份快照状态，切换档位不重新请求。
- 数据：挂载时 `NetDevOverview({force:true})`；刷新节奏见 §3.4（事件驱动 + 可见时兜底轮询）。
- **新鲜度徽章**：`now - GeneratedAt > StaleAfterSec` → 右上角"上次更新"徽章变灰 + ⚠️（DRT 手法）。

布局（wireframe，Bench 档；Dock 档为其紧凑单列变体）：

```text
┌─ [总览] [↻ 刷新] [上次更新 10:32 ●]              [⇲ 收起右侧栏] [×] ─┐
│ ┌── ⭐ 在途动作（横条，bench 档滚动时 sticky 钉顶）──────────────────┐ │
│ │ 待批提案 2 · 运行中 Job 1 · 暂停 Job 1 ⏸ · 割接进行中 0 · 终端 1     │ │
│ └──────────────────────────────────────────────────────────────────────┘ │
│ ┌── 资产覆盖 ───────────┐ ┌── 健康 ────────────────────────────────────┐ │
│ │ 纳管 15               │ │ 可达 12/15（3 台未开 SNMP 轮询）          │ │
│ │ 待确认 7  [导入]      │ │ last poll 10:30 · Flap 告警 1 · P90 告警 0 │ │
│ │ 不可达 1 · 无SNMP 3   │ │ [uptime sparkline × 每设备]               │ │
│ └───────────────────────┘ └───────────────────────────────────────────┘ │
│ ┌── 风险 ──────────────────────────────────────────────────────────────┐ │
│ │ ▓▓▓▓▓░░░ 加权分 23 · 等级 高    critical 1 · warning 4 · info 6     │ │
│ │ 紧急项: CVE 命中 2 [→] · 弱口令 1 [→]                               │ │
│ └──────────────────────────────────────────────────────────────────────┘ │
│ ┌── 近期事件 ─────────────────────────┐ ┌── 审计 ──────────────────────┐ │
│ │ [critical] OSPF 邻居 Down (SW-03)  │ │ 链校验 ✓ · 24h 读 312/写 4/  │ │
│ │ [warning]  link-flap …             │ │ 护栏拒绝 2                    │ │
│ └─────────────────────────────────────┘ └──────────────────────────────┘ │
│ ┌── 统计条（§7.1 L0，v2.0）─────────────────────────────────────────────┐ │
│ │ MTTR 6.2h · 基线 命中 3/规则 41（22/22 台）· Job 成功 8/9 · 变更 4/24h│ │
│ │ 设备构成: 交换机 9 · 路由器 4 · 防火墙 2 · …   提案: 待批 2/完成 11   │ │
│ └──────────────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────────────┘
```

- 组件：新 `components/netdev/OverviewPanel.tsx`，区块卡片复用 `ndv__card` 样式语言；
  sparkline 复用 HealthPanel 现有画法。
- 风险水位条：按 RiskLevel 着色（safe 绿 / low 浅绿 / medium 黄 / high 橙 / critical 红），
  紧急项格子学 DRT dashboard 的 urgent-item 样式。

### 4.3 交互规则（五屏通用）

- **深链一切**：每个数字/事件条目点击 → 切换目标页签并带入过滤（如 findings 页签带
  source=syslog 过滤）。大屏自身不承载详情，延续"结果归位"。
- **空态即引导**（诚实分母的交互面）：
  1. 总览零设备：引导文案 + 复用场景卡/Composer 建议 chips（"先添加设备/跑一次发现"）；
  2. 有设备无轮询数据：健康区显示"等待首轮轮询"，不可达/可达不显示；
  3. 无 findings：风险区显示"无未处理发现"（这是真 0，与 CVE 无 feed 的引导态区分）；
  4. 调查链无案例：引导"告警或 Finding 出现后自动成链"；
  5. 发现屏零线索：引导"导入 drawio/vsdx 或从 vantage 发起探测"；
  6. 暴露面无 feed 且无 findings：引导"导入 CVE feed / 跑一次基线"；
  7. 割接屏无进行中变更：显示最近一次已收尾变更 + "从提案中心发起"入口。
- **统一设备聚焦**：五屏中任何设备（矩阵行/链上节点/割接设备条/漏斗条目）点击 → 发
  `hiq:netdev-device-focus` 事件 `{device}` → 既有设备面板打开。全家族一套处理器，
  禁止各屏私写点击逻辑。
- i18n：全部文案进 zh/en locale（`ndv.ovw.*`、`ndv.dash.*`、`ndv.chain.*` 键族）。

### 4.4 打开与关闭（动线设计——全部复用既有惯例，零新交互范式）

**打开入口（六条，照抄既有通道）**：

| # | 入口 | 实现依据（既有惯例） |
|---|---|---|
| 1 | Dock"总览"页签（默认落地） | 页签体系；打开即见，零成本发现 |
| 2 | Dock 总览卡片头部"**大屏查看**"按钮 | LogPanel 的 `onOpenWorkbench` 同款（NetDevLayout.tsx:1280——日志工作台今天的主打开路径） |
| 3 | Bench 条"大屏"chip | `ndv-bench` 既有 chips（对话/日志/安全）；首次经入口 2 打开后 bench 条常驻（`logsBenchEverOpened` 同机制） |
| 4 | 命令面板 + `?bench=<screen>` 深链（五屏各一项） | `hiq:netdev-bench` 自定义事件与 `?bench=` 参数两条既有通道（:424-434） |
| 5 | 快捷键 **`o`**（无输入焦点时 toggle 总览：再按回对话）；**Alt+1..5** 五屏直切 | 与 `r` 刷新、`/` 搜索同一无修饰单键家族（:462 注释"快捷键最小集"） |
| 6 | 左下角运维导航"总览"按钮（§4.5） | 既有底栏语法（icon+label，active/badge 规则见 §4.5） |

**关闭/退出（四条）**：

1. **Esc 层级链**——接入既有退出序：bench≠chat → 回对话 → 关 cutover（:479-484），大屏
   不新增层级（投影模式先退出投影，再走此链）；
2. 再点"大屏"chip（toggle）/ 点"对话"chip；
3. bench 头部 `×` 按钮（CutoverView onClose 先例）；
4. **刷新自动暂停**——bench 不可见（切走/`display:none`）或窗口失焦
   （`document.visibilitychange`）时停兜底轮询，回来 force 拉一次。省资源也避免后台刷屏。
   **例外**：割接屏在窗口未到期时兜底轮询不因失焦停（进行时的屏优先连续性，回到可见
   立即 force 对齐）。

**右栏开合（bench 头部）**：`⇲ 收起右侧栏` 按钮走既有 `dockOnClose`/`closeWorkspacePanel`
路径，收起后按钮变"展开右侧栏"（toggle，不自动恢复——用户可能开着 live 页签盯实况）。
底部左侧导航的既有唤回入口不变。

**状态记忆（刻意的最小化）**：
- bench 态**不持久化**（重启回对话）——与日志/安全 bench 行为一致，保持 chat-first；
  场景感知默认页只在"未手动切过"的会话内生效；
- dock 开关/宽度沿用既有持久化；
- 总览 dock 页签每次启动默认落地（不记忆上次页签）。

**活动信号（"该看了"的被动提醒，复用 hotTabs）**：
- Dock 总览页签 hot dot：risk ≥ high 或出现新 critical Finding 时亮（`markHot` 既有机制）；
- Bench chip 风险色点：按 RiskLevel 着色（safe 灰 / medium 黄 / high 橙 / critical 红），
  不打开也能从 chip 角感知等级；
- （将来项）桌面通知点击 → deep link 大屏对应屏。

**Bench 家族扩展约定**（§4.4 v1.0 立的规矩，v2.0 兑现为五屏；后续新屏照此加入，不另造
交互范式）：每个大屏视图 = chip + 命令面板项 + 工作区入口按钮三件套 + Esc 层级退出 +
场景感知落点。拓扑大图、审计报告等将来照此加入。

### 4.5 左下角运维导航扩容（4 → 6/8）

现状四项（NetDevLayout.tsx:1055-1095 `cowork-sidebar__group` 钉底组）：设备清单（开 dock
devices，active 态）、立即巡检（直接动作 + 五项子菜单 + busy 态文案）、审计（开 dock audit +
今日计数徽章）、运维偏好（开设置）。扩容**完全沿用这套语法**，不发明新组件。

**推荐 8 项布局**（按"看状态 → 查资产 → 做动作 → 处队列 → 调配置"的工作流排序）：

| # | 入目 | 图标（lucide） | 动作 | 徽章/active |
|---|---|---|---|---|
| 1 | **大屏** | LayoutDashboard | 开 bench 大屏（场景感知默认页） | 风险色点（RiskLevel 着色）；active 当 bench 处于五屏任一 |
| 2 | 设备清单 | Server（现有） | 开 dock devices（现有） | active 现有 |
| 3 | 拓扑 | Network | 开 dock topology（openDockTabFn 按需加页签） | active 同语法 |
| 4 | 立即巡检 | ScanSearch（现有） | 直接动作 + 子菜单（现有） | busy 态文案现有 |
| 5 | 安全工作台 | ShieldCheck | 开 bench sec | hot dot（新 critical Finding） |
| 6 | 提案中心 | FileCheck | 开 dock proposals | **待批计数徽章**（draft 数，审计同款样式） |
| 7 | 审计 | ScrollText（现有） | 开 dock audit（现有） | 今日计数（现有） |
| 8 | 运维偏好 | SlidersHorizontal（现有） | 开设置（现有） | — |

**6 项最小版**：去掉 #3 拓扑、#5 安全工作台（dock 页签/命令面板仍可达），保 大屏/提案中心
两个新增——大屏是日常主入口，提案中心的待批徽章是"等你动作"信号，这两个的到达频率最高。

**语法规则（扩容不自破）**：
- **active 态**：开 dock 页签的项 `dockOpen && tab === X`；开 bench 的项 `bench === X`——
  两类并存时都亮，用户能看出"哪层开着"；
- **徽章三型**：计数徽章（审计/提案，数字）> 风险色点（大屏，无数字）> hot dot（安全，闪点），
  视觉重量递减，避免八个按钮七个红点的告警疲劳；
- **子菜单**：只有"直接动作"类允许（立即巡检先例）；导航类一律单击直达，不长子菜单；
- **竖向空间**：8 项 × ~28px ≈ 224px；项目工作区 section 已 `minHeight:0` 可压缩，1080p 无
  压力；小屏兜底——组内纵向滚动或图标紧凑模式（label 截断），v1 先不做、留观察项；
- i18n：`ndv.nav.*` 键族，zh/en 双语。

### 4.6 调查链屏（排查：结论怎么得出的 + 处置掉了没有）

红队链路止于"找到漏洞"，蓝队链路止于"**修复已验证**"——六列语义：

| 列 | 语义 | 节点数据（现成） | 色 |
|---|---|---|---|
| 1 | 事件源 | Finding.Source（syslog:<device>:<class> / trap / 告警规则；按 Source 去重聚合） | 黄 |
| 2 | 诊断动作 | 审计条目（device+command+class）+ Job 步骤态 + triage 电池 | 绿 |
| 3 | 事实证据 | Finding.Evidence（命令+已脱敏输出）+ Case IOC 命中 | 蓝 |
| 4 | 结论 | Finding 本体（severity=红/橙标签，同 dsh-pentest 的"高危"标签位） | 红/橙 |
| 5 | 处置 | 提案节点（状态机状态直接做节点态：draft/approved/done/rolled-back） | 紫 |
| 6 | 验证 | restore-verify 结果 / 割接 gate / 观察期结论 | 灰绿 |

```text
│ 案例选择 [OSPF 邻居震荡·SW-03 ▾]    事件2·诊断3·事实7·结论1·处置1·验证1 │
│ ⚡事件源 ─触发→ 🔍诊断动作 ─执行→ 📎事实证据 ─产生→ ⚠️结论 ─证实→ 📝处置 ─验证→ ✅验证 │
│ 底条（该屏专用）：案例时间线 ── 与链上节点互相高亮（时间轴↔节点双向同步）   │
```

- **组装**：`BuildInvestigationChain(caseID, findingID, hours)`——零新数据面。案例过滤走
  cases.go entries；`findingID` 非空时高亮包含该 Finding 的子链（`hiq://finding/<id>`
  深链的落点）。跨案例聚合视图 v1 不做（§11）。
- **渲染**：分层 SVG（选型记录 §4.10），六列固定、确定性布局；节点卡片带类型色标签 +
  标题级摘要（不含输出全文，点击深链原文）。
- **降噪**：节点上限 150；同型证据折叠为"N× 命令采集"组节点（展开显示明细）；自动
  Finding 已按 Source 去重（finding.go 既有语义）。
- **统计行**：内联"事件 N·诊断 M·事实 K·结论 L·处置 P·验证 V"（学 dsh-pentest 统计行，
  口径与节点数一致，分母可查）。

### 4.7 割接屏（变更窗口：到哪了/谁没好/能不能退）

```text
│ 割接单 #P-31 核心-SW 上联迁移   窗口 02:00–04:00 剩 47m   冻结 4 台      │
│ 回退就绪 ✓（快照 R-12 已验证）                                          │
│ ┌预检✓┐→┌gate:张工✓┐→┌配置下发 ⏳进行中┐→┌验证门 ─┐→┌观察期 ─┐→┌收尾 ─┐ │
│ 受影响设备(4)：SW-01 ✓ │ SW-02 执行中 │ R-05 待 │ FW-01 待   [回退点 4/4] │
│ 预算：墙钟 12/30min · 命令 45/200 · 连续失败 0/3                       │
│ 底条：本单命令流 ▸ display int brief ✓ │ shut x/0/1 ⏳ （红=失败即停）    │
```

- **横轴**=提案步骤流水线（R4 结构化步骤状态机直接映射：待/进行/完成/失败/断点待确认；
  gate 节点带复核人）；**第二行**=受影响设备各自进度与回退点就绪度；**第三行**=Job 预算
  燃尽（job.go Budget：墙钟/命令数/连续失败）；**底条**=审计流按变更单过滤。
- 割接屏是唯一"进行时"屏：兜底轮询不因失焦停（§4.4 例外条款）；窗口倒计时归零后恢复
  常规暂停规则。
- 无进行中变更时空态显示最近一次已收尾变更（终态复盘视角）+ "从提案中心发起"入口。

### 4.8 发现屏（纳管：底账建得怎么样）

```text
│ 待确认 12 │ 已转正 8 │ 纳管 22 │ 网段覆盖 6/9 │ 层深 2/2 (max_hops=2)     │
│ 漏斗：37 线索 → 24 有指纹 → 12 待确认 → 8 转正 → 22 纳管（合计口径）      │
│ （按来源分色：探测/导入/拓扑/日志——discovered.go Sources）               │
│ 层级图：NAS(L0) ─┬─ 10.1.1.0/24 (L1·直连小 ✓ 3h前)                      │
│                 └─ SW-03(L1) ── 192.168.8.0/24 (L2·3h前 ✓)              │
│ 图实一致性：设计 41 节点 / 快照 38 吻合 · 漂移 3（仅设计有 2 · 仅快照有 1）│
│ 底条：子网计划卡 ─ 10.0.0.0/8 大网段·路由分解·待确认（黄）；192.168.1.0/24 ✓ │
```

- **左主区转化漏斗**（哪一步丢了多少线索）；**右主区层级树**（discovery-layers.json 层级
  账本 + classifySubnet 分类 + RunState 时间戳）；底条计划卡与发现对话框同构。
- **图实一致性**：拓扑三源对账（plan/design/snapshot 按名称/IP 折叠匹配，iptopo/topoimport
  既有折叠键），"仅设计有"=漂移信号，深链拓扑页签带 topoSource 过滤。
- **端口突变卡**（依赖 D5-R2 journal）：新开/新关端口事件流，红点标注管理面端口
  （22/23/161/443/830 之外的新开高危端口如 445/3389 加警示色）。

### 4.9 暴露面屏（安全审视：哪里暴露/推演到哪/先剪哪条）

```text
│ 严重 2 │ 警告 9 │ 推演路径 14 条（推演） │ 最深 3 跳 │ 未纳管终点 5      │
│ 暴露矩阵：设备×严重度热力（行=设备，列=严重度，格子=条数，点击进 Finding） │
│ 推演图：暴露点(红) →─→ 邻接跳(灰) →─→ 高价值终点(紫)，路径按 score 排     │
│         剪断建议以虚线双端标出（SW-03 ⇢ R-05：断此边消 5 条路径）（推演）  │
│ 底条：弱口令/明文协议 findings（脱敏）+ CVE 融合：版本命中 CVE 按 severity 排│
```

- **推演图复用 `BuildAttackPaths` 输出**（v2 只是把卡片里的文字列表升级为链路图渲染），
  永带"推演"角标；剪断建议为无向对（attackpath.go 既有语义）。
- **CVE 融合**：CVEMatch 按 device 并入矩阵行（版本暴露×已知漏洞是天然增强；无 feed 走
  引导态）。
- 节点上限：路径按 score 取 top-50 + "显示更多"。

### 4.10 图形选型记录（v2.0 决策）

- **不引 React Flow/ELK**。调查链与暴露面推演图复用 TopologyMap 的**分层 SVG** 模式：
  确定性分层布局本来就是本家族哲学（拓扑 tier 分带、绝不力导向）；调查链是比拓扑更规整的
  六列 DAG。缩放平移用 ~50 行 wheel/drag transform 挂 SVG 容器。
- 依据：前端 bundle 已背 `react-force-graph-3d + three`（package.json），再引 React Flow
  是第二套图形依赖 + ~100KB 包体 + 两套节点视觉语言。dsh-pentest 的 React Flow 选型是它的
  第三个独立验证，但**我们自己的 TopologyMap SVG 已在仓内验证过同型场景**——照自己的来。
- TopoIcon 10 个角色图标全家族复用（矩阵表头、链上设备节点、割接设备条）。

### 4.11 投影/值守墙模式（应用内）

- 入口：壳头部 `⛶ 投影` 按钮 / Alt+P；退出：Esc（先退投影再走 §4.4 Esc 链）。
- 形态：全屏单视图、字号整体 +1 档、高对比；**五屏自动轮播**（默认 30s/屏，可调 15/30/60）；
  鼠标悬停/任意交互暂停轮播（值守人员要细看时不被切走）。
- 数据节奏：投影态下事件驱动刷新保持，兜底轮询按各屏规则（割接屏连续）。
- **边界**：这是应用内显示模式，不做多屏输出/视频流/远程投屏（§1 停车场维持）。

### 4.12 深链路由（把断头路接通）

- **`hiq://` 系统协议（已实施，Windows 首版）**：HKCU 用户级注册（启动幂等，
  HIQ_DEV 跳过防 dev 抢注）；热路径 = 第二实例 args 路由（单实例基建已在，
  OnSecondInstanceLaunch 解析后 EventsEmit）；冷路径 = 启动 argv 暂存 → 前端 boot 经
  NetDevConsumeDeepLink 取走。**路由表（导航型 only，永久红线——approve/execute 之类
  动作型目的地永久拒绝）**：`finding/<id>`→调查链高亮 · `case/<id>`→调查链 ·
  `cutover/<id>`→割接屏 · `proposal/<id>`→提案中心定位（tab+深链过滤） ·
  `screen/<name>`→五屏任达。解析 fail-closed：scheme 严格、host 白名单、id 限
  `[A-Za-z0-9_-]`、无 query/多级路径（表驱动测试覆盖，含动作型拒绝）。
  **产地**：割接决策点/验证门未过推送（此前无推送，本轮补，带 cutover 链接——
  半夜窗口期的"回来决策"召回）；早报推送副本尾附三屏直达链接（应用内渲染的原文
  不变）；Finding 卡"复制链接"按钮（上下文分享给同事——链接是跨人协作载体）。
- **`hiq:netdev-device-focus`**：§4.3 统一设备聚焦事件（窗口事件，进程内）。
- `?bench=<screen>` 五枚参数与命令面板五项一一对应（§4.4 入口 4）。

---

## §5 加权风险分（参数表，从 DRT 学、按本地数据本地化；总览屏与 chip 色点共用）

DRT 用五级 severity（critical×10/high×5/medium×2/low×0.5）；我们 Finding 是三级
（info/warning/critical，finding.go），本地化参数：

| 项 | 权重 |
|---|---|
| open critical Finding | ×10 |
| open warning Finding | ×3 |
| open info Finding | ×0.5 |

等级阈值（保守风格，学 DRT：1 个 critical 就该被看见）：

| 分数 | 等级 |
|---|---|
| 0 | safe |
| ≤3 | low |
| ≤10 | medium |
| ≤30 | high |
| >30 | critical |

**floor 规则**：CVE 命中 ≥1 条 critical 级、或弱口令 confirmed ≥1，等级**至少 high**
（紧急项独立展示不混入分数，避免双重计分，但必须有下限抬升）。

**定位声明**：加权分是排序/引流工具（决定用户先看哪块），不是 SLA 度量，UI 文案不得
称之为"安全评分"。参数进配置 `[netdev.dashboard] risk_weights`（可选覆盖，默认如上）。

---

## §6 诚实分母原则（不变量级，五屏自检）

1. 每个比率显示为 `x/y` 而非百分比；y 后必须括注未覆盖原因（"3 台未开 SNMP 轮询"）。
2. **0 与无数据严格区分**：无数据显示引导态（"先导入 feed"/"等待首轮轮询"），绝不显示 0。
3. 每个数字必须可深链到构成它的明细列表（分母可查——L0 统计落到字段级，见 §7.1 出处表）。
4. 快照携带 GeneratedAt/StaleAfterSec，前端强制展示新鲜度——过期数据必须看起来过期。

---

## §7 统计与留存（v2.0：三层——L0 零改动 / L1 轻留存 / L2 停车场）

### 7.1 L0：零后端改动即可统计（组装函数直接聚合，字段级出处）

| 统计 | 字段出处（已核实） | 落点屏 |
|---|---|---|
| **MTTR 处置时长** | finding.go `Finding.CreatedAt + ResolvedAt`（Status=resolved）→ 平均/最慢 | 总览统计条 |
| **基线合规** | baseline.go `BaselineSummary{Devices,Checked,Rules,Hits}` + BaselineViolation 按 rule | 总览统计条 |
| **CVE 匹配面** | cve.go `CVEMatch{Device,CVEID,Severity,Product}` + MatchCVEs | 总览/暴露面 |
| **Job 成功率/耗时** | job.go `Job{Status,ActiveMS,Commands,Cursor,BreakpointOK,StartedAt,EndedAt,CreatedBy}` | 总览统计条/割接屏预算行 |
| **审计活动画像** | audit.go `Audit{Time,Device,Command,Class,Status}`；class=read/write/dangerous/unknown；变更=timeline.go `timelineChangeClasses`（write/dangerous/proposal-write/proposal-rollback） | 总览统计条/底条 ticker |
| **提案漏斗/回退率** | proposal.go 状态机分布；**状态迁移时刻从审计链重放**（proposal-write/proposal-rollback 条目自带时间戳——dsh-pentest log-replay 哲学，零 schema 改动） | 总览统计条/割接屏 |
| **设备构成** | role.go `InferDeviceRole` + config kind/vendor → 按角色分布 | 总览统计条 |
| **值班视角** | handoff.go `HandoffReport`（本时段新 Finding/未闭环/写操作概览）——做总览"值班条"折叠区 | 总览 |
| **告警 ack 率** | alertqueue.go ack 记录 + notify.go `aggStateBySource` 聚合抑制 | 总览统计条 |
| **图实一致性** | 拓扑三源对账：topoimport.go TopologyDesign × snapshot 填充 × iptopo plan（名称/IP 折叠键既有） | 发现屏 |
| **暴露面推演计数** | attackpath.go `BuildAttackPaths`（Score/Hops/EndManaged/cut 建议） | 暴露面屏 |

### 7.2 L1：数据有但被丢弃——三件 journal 留存（一次 PR 量，挂既有生产者）

| # | journal | 现状缺口（已核实） | 设计 |
|---|---|---|---|
| R1 | **巡检汇总 journal**（`inspections.jsonl`） | 每次巡检/基线的汇总数字只活在 Finding 文本里，算完即弃 | 每次巡检/基线追加一行 `{at, devices, checked, critical, warning, info, baseline_hits}`；风险分走势/发现增长曲线的数据源。**不重复采 up/uptime**——metric_points 环表已覆盖（metrics.go：up/uptime/if_up/if_dn，5min 点位），journal 只记巡检事件级汇总 |
| R2 | **端口突变 journal**（`port-events.jsonl`） | discovered.go `RecordDiscoveredPorts`（:141）按端口号合并且只增不减——端口关闭记录仍在、新开与常开不可分辨 | DiscoveredPort 增 `FirstSeen`；变化时追加事件 `{at, ip, port, kind: newly-opened/newly-closed}`。发现屏端口突变卡/暴露面警示的数据源；蓝队强信号（内部设备新开 23/445） |
| R3 | **syslog 计数 journal**（按天滚动） | syslogrecv.go 环形缓冲是内存 map（`syslogRings`），重启即失——事件量趋势无从谈起 | 按 `{日期, 小时, device, class}` 计数落盘（每日一文件滚动）；原始 ring 语义不变（尾部明细仍走内存环）。量级：计数而非原文，很小 |
| R4 | **转正账本**（`promotions.jsonl`） | 待确认区转正后即从 store 移除，漏斗第 4 级的历史数无从对账 | promote 桥追加一行（at/device/ip），CountPromotions 汇总——实现时补充的第四件微型账本 |

- **写入纪律**：三件全部 best-effort 挂既有写入路径之后（巡检完成、RecordDiscoveredPorts、
  syslog 接收器），journal 失败**绝不 fail 生产者**（RecordDiscovered 同模式，§1 不变量 1）。
- **压实策略**：原始 journal 保留 90 天，之后压实为按日 min/avg/max（接口明细量级参考：
  22 台×48 口×365 天 ≈ 38 万行/年，不压实迟早难受）；R2 端口事件只在变化时追加天然小，
  不压实；R3 本身按天。
- **提案状态迁移不建 journal**：从审计链重放（§7.1），append-only 文件 + 缓存失效判断
  见 §8.2。
- **接口误码趋势（ifBrief）**：`parseIfBrief` 解析器现成（layerdiscover.go），但 CRC/input
  error 是**累计计数器**——journal 记原始计数器 + 当时 uptime，delta 只在同一 uptime 段内
  计算（设备重启清零不产生负跳变误报）。v1 记录进 R1 行（每设备一行 ifBrief 汇总），
  独立趋势卡随数据积累再上。

### 7.3 L2 停车场（真正的新后端能力，按价值排序，立项另议）

| 能力 | 说明 | 前置 |
|---|---|---|
| ~~巡检调度器~~ **已有，v2.0 复查纠错** | app 启动即拉起三调度器：`inspection_interval`（巡检+golden 漂移）、`backup_interval`（备份）、`briefing_push_time`（早报，每日定时）——本表初稿"目前仅手动"的说法有误。**本轮补齐的是执行戳 + 巡检合规卡（昨晚跑了吗）与 `scheduled_baseline`（基线并入调度循环）** | 已落地 |
| ~~SNMP 水位 OID 扩展~~ **已实施** | metric_points 加 cpu/mem/in_oct/out_oct 四列（旧库 PRAGMA 对账 + ALTER 迁移）；pollDeviceHealth 顺带采厂商 cpu/mem OID（huawei/cisco 表，表外设备留 0=未采集）+ ifXTable/octets 求和 walk（HC 优先 32bit 兜底）；健康行显示水位徽章（≥80% 转警示）、总览健康区加"全网最高水位"行；顺带修复 PollHealthOnce 的重复 RecordMetricPoint 写入 bug | 轮询框架已有（health.go） |
| ~~配置漂移定时 diff~~ **降级为已有相邻能力** | golden 基线漂移检查已挂在巡检调度循环（RunGoldenCheck，漂移即 Finding）；"运行配置 vs 昨日备份"的 diff 有手动桥（NetDevBackupDiff），自动版价值被 golden 覆盖大半，留在停车场观察 | — |
| ~~凭证健康~~ **已实施** | secret 库为单文件加密 JSON、无逐条时间戳——健康语义定为"条数 + 整库文件 mtime（最近一次任意凭证变更）"，>90 天提示轮换；`Store.HealthStats()` 不解密（锁库可读），netdev 侧按命名空间过滤计数；总览统计条新增"凭证健康"格。逐条年龄需 schema v3，暂不做 | 落地 |

---

## §8 性能预算与刷新纪律（v2.0：把"不卡"从事后测试变成设计约束）

### 8.1 组装预算

| 路径 | 预算（fixture：22 设备/500 findings/1 万审计条） |
|---|---|
| buildOverviewData / 四屏组装函数 | ≤100ms/次（内存缓存 30s 兜底，§3.2） |
| 审计链重放（提案漏斗/活动画像） | ≤50ms/次（§8.2 缓存） |
| 图渲染（调查链/推演图） | 空闲零定时器；交互帧不做承诺但首帧 ≤300ms |

### 8.2 审计重放有界（append-only 的红利）

- 默认窗口 **30 天**（提案漏斗/审计画像）；"全量"是显式用户动作（按钮），不默认。
- 重放结果缓存，key=(文件大小, 末条哈希)——append-only 文件让失效判断变成一次比较。
- 底部 ticker 走 audit tail 既有路径（本来就是尾部截取），不参与全量重放。

### 8.3 图规模护栏

- 调查链 ≤150 节点（同型证据折叠"N×"组节点，§4.6）；暴露面路径 top-50 by score + 
  "显示更多"（§4.9）；事件流 ≤20 条（§2.2）；ticker ≤50 条。

### 8.4 刷新纪律

- 写侧 `hiq:netdev-dash` 事件（§3.4）：dock 迷你总览零定时器、bench 事件即拉；
- 兜底轮询仅 bench 可见时（割接屏例外条款见 §4.4）；
- journal 写入与事件发送均 best-effort，失败静默、兜底轮询保证最终一致。

### 8.5 明确不做（YAGNI，防范围蔓延）

时序数据库（JSONL+压实够用）、WebSocket（Wails 事件够用）、用户自定义布局（v1）、
调查链 3D（force-graph-3d 留在它现有服务处）、其余四屏 dock 迷你版。

---

## §9 验收

1. 打开应用默认落在总览页签；六区块+统计条按 wireframe 渲染（两档：Dock 紧凑单列 + Bench 双列）。
2. **动线验收**：六条打开路径逐条走通（dock 页签/卡片"大屏查看"按钮/bench chip/命令面板+
   `?bench=<screen>`×5/`o`+Alt+1..5/左下角导航）；Esc 层级链正确（投影 → bench → 对话 →
   cutover 不串）；bench 不可见或窗口失焦时兜底轮询暂停、割接屏例外生效；"收起右侧栏"
   toggle 后底部左侧导航可唤回；全部数字可深链到正确页签+过滤。
3. **场景感知**：割接 running 时开大屏落割接屏；发现 run 活跃落发现屏；`hiq://finding/x`
   落调查链屏并高亮；手动切换后会话内记住。
4. **同源一致性**（核心）：`buildOverviewData()` 快照单测——构造已知 findings/jobs/proposals
   fixture，断言各计数与 Stats 各字段；早报（D3 重构后）的客观数据段与快照字段一一对应
   （同一 fixture 双端断言）；dock 迷你与 bench 总览同源（同一快照渲染断言）。
5. 早报行为回归：重构后 `NetDevDailyBriefing` 输出 markdown 结构不变（既有测试/金样通过）。
6. 空态各档正确（尤其 CVE 无 feed 显示引导而非 0；基线从未跑过 `Baseline=nil` 引导态）。
7. 新鲜度徽章：mock GeneratedAt 超阈值，徽章变灰加 ⚠️；force 刷新后恢复。
8. 加权分表驱动单测（含 floor 规则、resolved 不计入）；bench chip 风险色点随 RiskLevel 变色。
9. **只读断言**：五屏聚合路径代码评审确认零 Exec/零写桥；快照/board JSON 序列化后不含
   命令输出与凭证（grep 断言测试）。
10. **四屏组装单测**：InvestigationChain（案例过滤/Source 去重/节点上限/折叠组/findingID
    高亮）、CutoverBoard（步骤态映射/预算燃尽/回退就绪）、DiscoveryBoard（漏斗口径/层级树/
    三源对账）、ExposureBoard（top-50/推演角标/CVE 融合）各自 fixture 驱动。
11. **L1 journal 单测**：R1 巡检追加行；R2 FirstSeen+变化事件（含"关闭后重开"序列）；R3 按
    天滚动；三件失败注入不 fail 生产者；压实函数按 fixture 断言。
12. **审计重放**：30 天窗口截取正确；缓存 key 失效（追加一条即重算）；10k 条注入计时
    ≤50ms（CI 宽松上限 ×5）。
13. **事件驱动**：findings/jobs 写入触发 `hiq:netdev-dash`；dock 迷你零定时器断言
    （组件内无 setInterval）；bench 收到事件拉取一次。
14. i18n：zh/en 双语全键覆盖（含五屏/投影/统计条）。
15. 回归：11 个既有页签不受影响；既有 `r`/`/`/Esc 快捷键不与 `o`/Alt+N 冲突；live 页签
    与大屏互链可用。
16. **左下角导航**：扩容后各项动作/active/徽章逐项正确（大屏 risk 色点、提案中心待批计数、
    安全工作台 hot dot）；6 项与 8 项两种配置渲染正常；既有四项行为不回归。
17. **投影模式**：进入/退出、轮播间隔 15/30/60 切换、悬停暂停、割接屏投影态连续刷新。

---

## §10 分期、依赖与体量

```
D1 后端聚合 + D3 早报同源重构（先做，1-2 天）
        └→ D2 前端总览屏（2-3 天）
                └→ D4a 壳+场景感知+事件驱动+投影模式（1-2 天）
                        ├→ D4c 割接屏（1-2 天，最便宜、依赖全现成）
                        ├→ D4b 调查链屏（2-3 天，含审计重放）
                        ├→ D4d 发现屏（1-2 天，图实一致性需对账函数）
                        └→ D4e 暴露面屏（1-2 天，复用 BuildAttackPaths）
D5 L1 三件 journal（1-2 天，可与 D4 并行；R2 端口突变卡依赖它，先做 R2）
```

| 依赖 | 说明 |
|---|---|
| 指纹 spec F1-F5 | **已全部落地**（待确认区/分层发现/暴露面推演），发现屏与暴露面屏数据齐备 |
| COMPLETION_SPEC R4 | **已落地**（结构化提案步骤/割接模式/restore-verify），割接屏数据齐备 |
| proposal watching 状态修复（评审发现的死状态 bug） | Inflight.ProposalsWatchable v1.0 已删；若恢复观察期字段需先修 |
| OPS spec Phase 1（Request/Run） | 落地后总览在途区块扩展"进行中的请求/计划"行——本 spec 的扩展点 |
| ~~hiq:// 协议注册~~ | **已实施**（Windows 首版：HKCU + 单实例 args 路由 + 冷启动 argv + fail-closed 解析；macOS 打包版随 plist 声明另议） |

---

## §11 开放问题（待评审拍板）

1. **默认页签**：overview 直接取代 context 成为落地页（本 spec 立场），还是把 context 的
   "当前上下文设备卡"合并进总览顶部？
2. **事件流条数**：20 条是否合适；是否需要按 source 的过滤 chips（本 spec 立场：v1 不做，
   深链到 findings 过滤即够）。
3. **缓存 TTL**：服务端 30s + 事件驱动（v2.0 已将 60s 轮询降为兜底）是否还需要保留兜底
   （立场：保留——事件 best-effort 丢包时兜底保证最终一致）。
4. **CVE/弱口令是否计入加权分**：本 spec 立场"独立展示 + floor 抬升"，不混入分数
   （避免与 findings 双重计分）。
5. **投影轮播默认间隔**：30s（立场）还是 60s？轮播顺序固定五屏序还是可配置？
6. **调查链跨案例聚合**：v1 单案例（立场）；"同设备多案例合并视图"留 v2 观察。
7. **syslog 计数 journal 设备口径**：无 IP/设备名缺失的行归 `unknown` 桶（立场）还是丢弃？
8. **ifBrief 累计计数器容错**：立场已定（记原始计数器+uptime，同段内算 delta，§7.2），
   待实现时验证设备重启识别的准确率。

---

## §12 实施与偏差记录（v2.0 落地，2026-08-29）

**已交付**：D1（`buildOverviewData` + `NetDevOverview` 桥 + 30s 缓存 + 链校验 10min
缓存）、D2（OverviewPanel 两档 + dock 总览页签默认落地 + 一次性补种 flag）、D3（早报
三计数同源，快照失败回退旧口径）、D4（壳 DashShell：五页签/场景感知/投影轮播/底条
ticker；调查链六列分层 SVG/割接流水线/发现漏斗/暴露面矩阵；`o` 键 + Alt+1..5 +
`?bench=dash&screen=&finding=` + bench chip + 左下导航 8 项 + `hiq:netdev-dash`
写侧推送 + `hiq://finding` 应用内拦截 + `hiq:netdev-device-focus` 统一聚焦）、
D5（R1 巡检 journal 含 ifBrief 行数启发式 + R2 端口突变含 FirstSeen 回填 +
R3 syslog 计数按天滚动 + R4 转正账本 + 90 天压实；四屏组装函数与三件 journal 全部
表驱动单测覆盖）。

**入口三件套补全（复查轮）**：命令面板五项（⌘K → 大屏·总览/调查链/割接/发现/
暴露面）、dock 总览卡片"大屏查看"按钮、Finding 卡"证据链"按钮、割接视图头部
"进入割接大屏"、提案中心割接卡入口、发现对话框"查看发现大屏"、attack-path 卡
"放大"——全部经 `hiq:netdev-open-screen` 事件抵达壳。补测：riskLevelFromScore
表驱动（desktop）、journal 失败注入（broken dir 不 panic、best-effort 静默）、
审计 10k 重放计时（实测 24.7ms ≤ 预算 50ms）、前端渲染冒烟 + dock 档零定时器
源码断言（dash-boards.test.tsx，已入 npm test 序列）。

**L2 复查轮（2026-08-30）**：§7.3 初稿"巡检调度器目前仅手动"系误记——仓库在 app 启动
即拉起三调度器（inspection_interval 巡检+golden 漂移 / backup_interval 备份 /
briefing_push_time 早报），spec 已纠错。本轮补齐的真实缺口：**调度执行戳**
（schedule-last.json，调度循环每次写入，巡检合规卡的数据源——"昨晚跑了吗"可答）、
**scheduled_baseline**（基线电池并入调度巡检，默认关，设置→调度分组可勾）、总览
统计条新增"巡检合规"格（✓ 上次时间 / ⚠ 上次失败 / 未跑过；仅统计调度执行，手动
巡检不计）。配置漂移一项降级为"已有相邻能力"（golden 漂移已挂调度循环），留在
停车场观察。

**页卡整理轮（2026-08-31）**：dock 页签 12→10（+overview）——**context 并入 devices**
（列表点行就地展开设备 360 卡；ScenarioHub+早报移总览页签作落地页；拓扑节点点击
落 devices），**jobs 并入 live**（实况+Job 堆叠，Job 卡加"成功 x/n·均 s"统计条；旧
jobs 深链自动重定向 live）。四个太空页签充实（全部同域现成数据）：日志页签顶部
**事件量 24h**（R3 journal 首次上屏，点类目联动 grep）、审计页签**审计画像**（24h
三计数+哈希链+30 天类目）、拓扑页签**图实对账**（设计↔规划+邻接来源覆盖，
`BuildTopoReconcile` 纯函数+表驱动测试）、设备页签**统计行**（角色构成+SNMP 覆盖+
最近备份）。退役页签参数归一化（?dock=context→devices、jobs→live），兜底页签
context→overview。§0 的"11 个页签"为历史快照，现状以本节为准。

**偏差（对照正文，全部有意为之）**：

1. ~~深链过滤参数 v1 只切页签~~ **已兑现（L2 落地轮）**：findings/proposals 面板
   接入外部过滤层（severity:/id:/device:/assess/baseline/syslog 七种语义 +
   子串兜底），过滤 chip 一键清除；jobs/audit 跳转仍只切页签（面板无过滤态）。
2. **图实一致性口径 = 设计 ↔ IP 规划**（两份离线存量）；spec 原文的"快照"侧是
   LLDP 活探测，不为看屏发起连接（纯读不变量优先），活快照对账留待巡检侧落库后并入。
3. ~~`hiq://finding/<id>` 接收端为应用内锚点拦截~~ **系统注册已实施**（§4.12）：
   应用内拦截保留（markdown 里的链接直达），系统通路（热/冷路径）与产地扩展同轮落地。
   现实约束保留一条：IM 客户端是否将自定义 scheme 渲染为可点链接不在我们控制内
   （飞书/钉钉部分版本只自动链 http(s)）——bot 命令路径（/netdev 详情）仍是移动端/
   不可点场景的兜底。
4. **写侧事件覆盖面**：挂桌面桥写路径（提案五桥/巡检/基线/发现/割接/Job/健康
   观察者/promote/dismiss）+ 30s 兜底轮询；内部守护直写路径（syslog escalate、
   告警规则）不发事件，由兜底轮询覆盖——§3.4 的 best-effort 语义。
5. **割接屏 Job 行为全网在途**（诚实标注"全网"），未与割接单绑定——提案与 Job
   无外键可查；预算行展示在 Job 行内。
6. **UptimeSpark 单位 = 小时**（MetricHistory 的 UptimeSec 换算）；sparkline 取
   环表 96 点。
7. **SummarizeIfBrief 为行数启发式**（down 优先于 up 判行；§7.2 已声明），不是
   逐口误码台账。
