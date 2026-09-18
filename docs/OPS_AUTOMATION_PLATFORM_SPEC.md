# Hiq 统一智能运维与安全编排平台规格说明书

> **版本**：v1.0-draft  
> **日期**：2026-08-30  
> **状态**：产品总规格 / 实施基线  
> **适用范围**：数通运维、主机系统运维、容器与 Kubernetes、数据库、安全蓝队、受控红队验证、自动化任务与报告闭环

## 0. 文档定位

Hiq 当前已经拥有一组成熟但相对分散的能力：`netdev` 数通诊断、Linux/Windows 主机体检、Docker/Kubernetes/数据库只读诊断、安全基线、CVE 匹配、Finding、案例、Job、割接和定时任务。

本文将这些能力统一为一个产品模型：

> 用户用自然语言提出请求，Hiq 自动识别领域、资产、风险和目标，生成可解释的执行计划；低风险只读步骤自动运行，高风险动作进入审批；结果沉淀为证据、Finding、案例、报告和可验证的修复闭环。

本文是上层总规格，不替代下列领域规格：

- [NETDEV_SPEC.md](C:\Users\13852\Desktop\Swarm-OS\hiq\docs\NETDEV_SPEC.md)：数通运维 v1.1
- [NETDEV_SPEC_V2.md](C:\Users\13852\Desktop\Swarm-OS\hiq\docs\NETDEV_SPEC_V2.md)：全栈运维扩展 v2.1
- [NETDEV_COMPLETION_SPEC.md](C:\Users\13852\Desktop\Swarm-OS\hiq\docs\NETDEV_COMPLETION_SPEC.md)：界面和能力补全
- [NETDEV_USAGE.md](C:\Users\13852\Desktop\Swarm-OS\hiq\docs\NETDEV_USAGE.md)：当前使用逻辑

若本文与已落地的安全不变量冲突，以领域规格中更严格的约束为准。

## 1. 产品目标

### 1.1 核心目标

1. **统一入口**：用户不需要知道应调用哪个工具、设备属于哪个厂商或日志位于哪个系统。
2. **自动分流**：自动判断数通、系统、数据库、容器、安全或变更场景。
3. **自动调查**：安全的只读诊断可以连续执行、关联和扩展，不要求用户逐条指导。
4. **可解释执行**：每次请求都能看到目标、计划、范围、步骤、证据、结论和下一步。
5. **人机协同变更**：AI 可以提出、解释和验证变更，但生产写操作必须经过现有提案、审批、备份、执行、验证和回滚链路。
6. **安全评估可控**：允许授权范围内的漏洞存在性验证，但不建设 getshell、提权、绕过、持久化或横向移动平台。
7. **自动闭环**：告警、诊断、Finding、案例、通知、修复提案和复核形成连续链路。

### 1.2 成功指标

| 指标 | 目标 |
|---|---|
| 首次请求到正确领域识别 | ≥95%（基于验收集） |
| 只读故障请求自动完成率 | ≥80% 无人工拆解 |
| 每个结论的证据覆盖率 | 100% 的 Finding 必须有来源证据 |
| 越权资产访问 | 0 次成功；越界请求必须硬拒绝并审计 |
| 高风险动作误执行 | 0 次 |
| 告警到自动初步结论 | 默认 ≤5 分钟，取决于资产连接和采集窗口 |
| 自动任务可恢复性 | Job 中断后可从最近断点继续 |
| 凭证泄露 | 0 次进入模型上下文、日志、导出包或 UI 输出 |

## 2. 范围与非目标

### 2.1 产品范围

- 网络设备：路由器、交换机、防火墙、VPN、BMC、SNMP 设备。
- 计算资源：Linux、Windows、VMware/ESXi、物理主机。
- 平台资源：Docker、Kubernetes、Web 服务。
- 数据服务：MySQL、PostgreSQL、Redis、MongoDB、MSSQL、ClickHouse、Elasticsearch。
- 安全运营：安全基线、弱口令评估、CVE 匹配、攻击面、IOC、事件时间线、证据包。
- 变更运营：提案、审批、备份、执行、验证、回滚、割接。
- 自动化运营：诊断 Job、定时任务、告警触发、报告、通知。

### 2.2 明确非目标

- 不让 AI 获得任意 shell、任意 SQL、任意 Kubernetes 写权限或任意网络转发能力。
- 不通过提示词约束替代代码级权限和数据面隔离。
- 不自动执行破坏性漏洞利用、getshell、提权、防护绕过、持久化或横向移动。
- 不分发密码字典、CVE feed、POC、payload 或外部攻击工具。
- 不做多租户 RBAC；当前产品以本地单用户和人工导出协作为边界。
- 不做设备侧常驻 Agent；长期采集需要另立分域探针规格。

## 3. 设计不变量

以下规则是所有新能力的发布门槛：

1. **结构性只读**：诊断数据面没有写路径；未知命令默认拒绝。
2. **提案式写入**：任何写操作只能通过结构化提案，由人工审批后执行。
3. **范围先于推理**：资产、项目、设备组、CIDR、命名空间和数据库源在请求进入模型前完成裁剪。
4. **凭证不出密钥库**：配置只存引用名；凭证值不进入 TOML、上下文、普通日志或导出包。
5. **完整审计**：成功、拒绝、超时、重试、人工批准、紧急停止和回滚都必须审计。
6. **证据可追溯**：结论必须能回指采集步骤、时间、资产、命令/端点和已脱敏输出。
7. **能力隔离**：`dev`、`cowork`、`netdev` 及未来 `redteam` profile 的工具面物理隔离。
8. **失败闭锁**：范围不明、目标不明、凭证状态不明、验证失败或风险无法分类时，暂停并请求人工决策。
9. **自动化有界**：每个请求都有墙钟、步骤、并发、输出大小和失败次数预算。
10. **红队默认不存在**：红队能力只有在独立 profile、有效 engagement 和重启后才注册。

## 4. 用户与请求类型

### 4.1 主要用户

| 用户 | 默认关注点 | 默认输出 |
|---|---|---|
| NOC/数通工程师 | 链路、路由、接口、邻居、设备健康 | 路径图、异常点、设备证据 |
| 系统运维 | 主机、服务、进程、日志、磁盘 | 主机体检、根因候选、修复提案 |
| 数据库工程师 | 慢查询、锁、连接、复制延迟 | TopSQL、等待树、参数/状态证据 |
| 安全响应人员 | IOC、登录、进程、漏洞、时间线 | 事件案例、影响面、证据包 |
| FDE/现场工程师 | 范围、自证、离场材料 | 审计报告、变更记录、交接包 |
| 管理者 | 风险、趋势、SLA、修复进度 | 周报、风险摘要、覆盖率 |

### 4.2 统一请求分类

每个请求至少归入一个 `intent`，允许主意图加辅意图：

```text
incident_diagnosis     故障诊断
health_check            健康检查
network_path            网络路径/连通性
configuration_audit     配置与基线
security_incident       安全事件响应
vulnerability_assess    漏洞与暴露面评估
credential_assess       弱口令评估
change_request          变更请求
cutover_runbook         割接/发布流程
reporting               报告与汇总
scheduled_monitoring    定时监控
inventory_discovery     资产发现
knowledge_lookup        运维知识查询
```

分类结果必须包含：`intent`、目标资产、项目/范围、时间范围、紧急程度、是否允许主动探测、是否可能产生写入、置信度和澄清问题。

## 5. 总体架构

```text
用户 / IM / 定时器 / Syslog / Trap / Health Alert
                         │
                         ▼
                 Request Gateway
                         │
                         ▼
             Intent + Asset + Risk Classifier
                         │
                         ▼
                    Scope Gate
                         │
                         ▼
                Plan Compiler / Playbook
                         │
                         ▼
               Job Orchestrator + Budgets
                  │          │          │
                  ▼          ▼          ▼
              数通面      系统面      安全面
              netdev      host      blue/red
                  │          │          │
                  └──────┬───┴──────────┘
                         ▼
              Evidence / Finding / Case Store
                         │
             ┌───────────┼───────────┐
             ▼           ▼           ▼
          提案审批      通知报告      复核验证
```

### 5.1 现有代码映射

| 平台层 | 当前实现 | 本 Spec 的增强方向 |
|---|---|---|
| 请求入口 | 对话、bot、scheduler | 统一请求信封和分类结果 |
| 数通工具 | `internal/netdev/tools.go` | 由计划编译器自动组合 |
| 主机体检 | `internal/netdev/triage.go` | 统一到资产能力矩阵 |
| 安全评估 | `assess.go`、`baseline.go`、`cve.go` | 增加安全工作流和覆盖率 |
| 长任务 | `job.go` | 支持 AI 编译、审批和恢复 |
| 变更/割接 | `proposal.go`、`cutover.go` | 与请求状态机和验证闭环统一 |
| 定时任务 | `internal/scheduler` | 支持告警触发和领域模板 |
| 运维 UI | `desktop/frontend/src/components/netdev` | 增加计划、状态和统一结果视图 |
| profile 隔离 | `internal/boot`、`internal/config/profile.go` | 增加独立红队 profile 门控 |

## 6. 统一资产模型

### 6.1 Asset

```json
{
  "id": "asset-prod-core-01",
  "name": "core-sw-1",
  "kind": "network-device",
  "vendor": "huawei",
  "os": "vrp8",
  "environment": "production",
  "project": "hq-production",
  "groups": ["core", "production"],
  "address": "10.30.0.1",
  "via": ["bastion-01"],
  "capabilities": ["cli-read", "netconf-read", "topology", "config-backup"],
  "scope": "managed",
  "labels": {"owner": "network", "criticality": "tier-0"}
}
```

`kind` 取值：`network-device`、`linux`、`windows`、`docker`、`k8s`、`db`、`web`、`firewall`、`bmc`、`vmware`。

### 6.2 资产状态

资产必须区分：

- `managed`：人工纳管，可按权限连接。
- `discovered`：发现但未纳管，只能进入待确认区。
- `unreachable`：配置存在但当前不可达。
- `quarantined`：被人工隔离，自动任务不得连接。
- `retired`：历史资产，只保留证据和报告引用。

发现结果不得自动升级为可连接资产。资产变更必须来源于人工设置、导入确认或批准的同步流程。

### 6.3 能力矩阵

每个资产声明能力，而不是让模型猜测：

```text
read.cli
read.netconf
read.snmp
read.logs
read.metrics
read.topology
read.config_snapshot
read.db_health
read.container
read.k8s
assess.credential
assess.vulnerability_readonly
propose.config
execute.approved_change
```

工具注册和计划编译必须同时检查资产能力、profile 能力、项目范围和当前 engagement。

## 7. 统一请求生命周期

### 7.1 状态机

```text
received
  → classified
  → scoped
  → clarified（必要时）
  → planned
  → approved（主动评估/变更/红队验证）
  → running
  → waiting_decision（失败、验证门或风险升级）
  → analyzed
  → completed
  → remediating（存在修复提案）
  → verified
  → archived
```

任何状态都可以进入 `aborted`，但必须写明原因和最后一个安全断点。

### 7.2 请求信封

```json
{
  "request_id": "REQ-20260830-0001",
  "source": "chat|bot|schedule|alert",
  "actor": "local-user",
  "text": "生产区 core-sw-1 最近经常丢包，帮我查一下",
  "project": "hq-production",
  "targets": ["core-sw-1"],
  "intent": "incident_diagnosis",
  "risk": "read",
  "scope_snapshot": "sha256:...",
  "budget": {"wall_sec": 600, "commands": 40, "parallel": 4}
}
```

`scope_snapshot` 固化请求启动时的范围，避免任务运行期间配置变化导致隐式扩大权限。

### 7.3 计划对象

计划由 AI 生成、由主机校验和规范化，不允许直接把模型输出当作命令执行：

```json
{
  "plan_id": "PLAN-...",
  "request_id": "REQ-...",
  "steps": [
    {
      "id": "s1",
      "kind": "read",
      "asset": "core-sw-1",
      "operation": "interface_health",
      "precondition": "asset reachable",
      "timeout_sec": 30,
      "on_failure": "continue"
    }
  ],
  "expected_outputs": ["suspect_interface", "last_good_hop", "evidence"],
  "approval": "none"
}
```

计划校验内容包括：目标存在、能力匹配、范围内、步骤类型合法、参数白名单、预算有效、禁止隐式写入、禁止循环扩张。

## 8. 自动化编排

### 8.1 计划编译器

新增逻辑组件 `internal/ops/orchestrator`，职责：

1. 解析自然语言和事件。
2. 生成分类和澄清项。
3. 根据领域模板选择只读步骤。
4. 根据资产能力填充具体工具。
5. 将计划编译成 `Job`。
6. 监听步骤结果，根据规则追加后续只读步骤。
7. 产出结构化结论和下一步动作。

建议接口：

```go
type Orchestrator interface {
    Classify(ctx context.Context, req Request) (Classification, error)
    Plan(ctx context.Context, req Request, c Classification) (Plan, error)
    Start(ctx context.Context, p Plan) (Run, error)
    Resume(ctx context.Context, runID string) (Run, error)
}
```

### 8.2 动态扩展规则

自动扩展只能由确定性规则触发，不能由模型无限自我发散：

```text
接口错误升高 → 追加接口计数/光模块/邻居/日志
OSPF 邻居 Down → 追加接口、认证、MTU、路由和日志
主机登录异常 → 追加安全日志、进程、监听端口、持久化位置
CVE 命中 → 追加版本确认和只读验证器
数据库慢查询 → 追加锁等待、连接、复制延迟和资源指标
```

扩展必须消耗同一个请求预算；预算耗尽时输出覆盖率和未完成项。

### 8.3 自动化级别

| 级别 | 行为 | 默认状态 |
|---|---|---|
| L0 | 只解释，不连接 | 支持 |
| L1 | 单资产只读诊断 | 默认开启 |
| L2 | 范围内批量只读诊断 | 需范围确认 |
| L3 | 告警触发的自动 Job | 用户配置后开启 |
| L4 | 弱口令/主动漏洞验证 | engagement + 审批 |
| L5 | 生产变更/割接 | 提案 + 人工审批 |

## 9. 领域工作流

### 9.1 数通故障诊断

默认流程：

```text
目标确认 → 设备清单 → 接口/邻居/路由 → 逐跳路径 → 日志/指标 → 拓扑关联 → Finding
```

必须输出：最后验证正常的节点、第一处异常或不可验证点、影响范围、证据、建议动作和是否需要提案。

### 9.2 主机系统运维

默认流程：

```text
可达性 → OS/版本 → CPU/内存/磁盘 → 进程/服务 → 端口/连接 → 系统日志 → 安全事件 → Finding
```

Linux/Windows 的命令必须继续使用现有读表和脱敏链路；用户不能通过自然语言把路径、命令或日志源扩大到白名单之外。

### 9.3 容器与 Kubernetes

- Docker 仅允许 GET 白名单、容器状态、日志、资源和事件读取。
- Kubernetes 仅允许固定 context、namespace 内的 get/list/watch/logs/describe。
- `apply`、`delete`、`scale`、Secret 变更必须转为结构化提案。
- kubeconfig 只进入密钥库，模型永远只接收资产名称。

### 9.4 数据库诊断

- 默认只读系统视图、连接、慢查询、锁等待、复制延迟和状态指标。
- 用户数据查询不在默认读集。
- 每个来源拥有精确 allowlist、查询次数、行数、耗时和输出大小预算。
- `EXPLAIN ANALYZE` 按可能真实执行处理，默认需要更高风险等级。

### 9.5 蓝队事件响应

默认五步：

1. 资产和时间窗口确认。
2. 主机体检和安全日志采集。
3. IOC 跨资产搜索。
4. 进程、端口、登录和持久化关联。
5. 生成时间线、影响面、置信度、证据包和修复提案。

蓝队自动化默认不隔离主机、不杀进程、不删除文件；这些动作必须进入人工批准的响应提案。

### 9.6 受控红队验证

红队验证只允许以下能力：资产情报、服务识别、版本确认、CVE 匹配、用户自备模板的只读验证、覆盖率报告。

必须同时满足：

- 独立 `redteam` profile。
- engagement 工单号、授权人、目标 scope、开始/过期时间。
- 重启后才生效；运行时不能通过普通设置切开。
- 独立工具注册表，netdev 诊断工具不可见，反之亦然。
- 每个任务有目标数、端口数、请求数、并发、墙钟和速率限制。
- 断线、过期或紧急停止立即终止主动验证。
- 模板、字典、payload 用户自备，内容不进入 LLM 上下文。
- 永久禁止 getshell、提权、绕过、持久化和横向移动。

## 10. 变更与修复闭环

所有修复动作统一进入：

```text
Finding
  → 修复建议
  → netdev_propose / structured proposal
  → 人工查看 diff、风险、影响和回滚
  → 批准
  → 变更前备份
  → 执行
  → 验证门
  → 成功关闭 / 失败暂停
  → 回滚或人工继续
  → Finding 复核
```

AI 不得把“建议修复”伪装成“已修复”。只有验证步骤通过，状态才可变为 `verified`。

## 11. 数据与证据模型

### 11.1 统一 Finding

```json
{
  "id": "F-...",
  "type": "health|security|vulnerability|compliance|change",
  "severity": "info|low|medium|high|critical",
  "confidence": "confirmed|probable|suspected|unknown",
  "title": "...",
  "assets": ["..."],
  "source": "triage|syslog|cve|baseline|assessment|job",
  "first_seen": "...",
  "last_seen": "...",
  "status": "open|acknowledged|mitigated|resolved|false_positive",
  "evidence_ids": ["E-..."],
  "suggested_actions": ["..."],
  "coverage": {"planned": 12, "completed": 10, "skipped": 2}
}
```

### 11.2 Evidence

证据必须保存：资产、来源、操作、时间、耗时、结果状态、脱敏后的输出摘要、哈希和关联请求/Job。原始秘密永不保存；敏感输出在进入上下文前脱敏。

### 11.3 Case

安全案例包含 Finding、日志命中、体检、人工笔记、IOC、时间线、影响资产和导出包。案例允许人工修改、拆分、合并和标记误报，所有人工动作都审计。

## 12. 用户界面规格

### 12.1 主视图

运维界面保留现有布局，不无限增加页签。主区增加统一“请求/计划”视图：

- 请求摘要：用户原话、分类、目标、风险。
- 计划：步骤、预算、预计影响、需要审批的步骤。
- 实况：当前 Job、设备连接、步骤输出、拒绝原因。
- 结果：Finding、证据、覆盖率、建议动作。
- 下一步：建案例、生成提案、复核、导出、设置定时任务。

### 12.2 结果卡

每类自动化都必须有结构化结果卡，不把成功结果显示为错误 banner：

- 网络路径卡：路径图、最后正常跳、第一异常点。
- 主机体检卡：健康摘要、异常项、证据。
- 安全事件卡：时间线、IOC、影响面、置信度。
- 漏洞卡：资产、版本、CVE、验证状态、覆盖率。
- Job 卡：状态、断点、预算、暂停原因、继续/终止。
- 修复卡：提案、审批、执行、验证、回滚。

### 12.3 交互原则

- 未纳管资产只显示为发现，不显示连接或执行按钮。
- 只读动作默认可直接执行；主动评估和变更显示清晰的审批门。
- 所有批量动作显示资产数量、并发、预计预算和取消入口。
- 高危动作必须二次确认，确认内容不能只写“确定”。
- 用户可以从 Finding 一键进入案例、计划、提案或报告。

## 13. 通知与报告

### 13.1 事件通知

通知按严重度、项目、资产组和去重键配置：

```text
事件 → 聚合/去重 → 规则判断 → 自动诊断（可选） → Finding → Webhook/IM/邮件/站内通知
```

通知必须包含：标题、严重度、资产、发生时间、当前状态、证据链接/案例 ID、是否需要人工动作。

### 13.2 报告

至少提供：

- 日常健康简报。
- 网络与系统周报。
- 安全事件报告。
- 漏洞与暴露面报告。
- 变更与割接报告。
- 现场交接/离场诊断包。
- 审计哈希链和覆盖率报告。

报告导出必须进行敏感模式扫描，禁止导出凭证、Token、私钥、完整 Cookie 或未脱敏配置。

## 14. 配置与 API 方向

### 14.1 配置新增方向

```toml
[ops.orchestrator]
enabled = true
default_wall_sec = 600
default_command_budget = 40
max_parallel = 4
auto_expand_readonly = true

[ops.automation]
allow_alert_trigger = false
allow_auto_case = true
allow_auto_proposal = true

[redteam]
enabled = false
profile = "redteam"
engagement_id = ""
approver = ""
expires = ""
scopes = []
```

红队配置不能在普通 netdev 会话中热启用；正式实现应通过启动期 profile 解析和工具注册控制。

### 14.2 建议桥接方法

```text
OpsClassifyRequest(text, project) → Classification
OpsCreatePlan(request) → Plan
OpsStartPlan(plan) → Run
OpsGetRun(id) → Run
OpsPauseRun(id)
OpsResumeRun(id)
OpsAbortRun(id)
OpsCoverage(id) → Coverage
OpsCreateCaseFromRun(id)
OpsCreateProposalFromFinding(id)
OpsExportEvidence(id) → path
```

### 14.3 建议 AI 工具

```text
ops_classify       只做分类和目标识别
ops_plan           生成经过 schema 校验的只读/提案计划
ops_run            启动已批准或低风险计划
ops_status         查询运行状态和预算
ops_finding        生成带证据的统一 Finding
ops_case           创建/更新事件案例
ops_coverage       输出计划覆盖率
ops_propose        将修复建议转为结构化提案
```

`ops_run` 不得成为任意命令执行入口；实际步骤仍需落到现有领域工具和分类器。

## 15. 安全、可靠性和可观测性

### 15.1 安全控制

- 请求范围快照。
- 资产和项目级白名单。
- 设备组策略。
- 命令/端点/SQL allowlist。
- 每轮、每 Job、每资产预算。
- 连接超时和并发上限。
- 输出行数、字节数和日志 follow 时间上限。
- 断线即停、到期即停、全局紧急停止。
- 输出脱敏和敏感模式回归测试。
- 审计哈希链和链完整性检查。

### 15.2 可靠性

- 每个步骤幂等标记。
- 失败策略：暂停、终止、继续三选一。
- 断点持久化。
- 任务恢复前重新校验配置、范围和 engagement。
- 部分成功必须明确标记，不能整体伪装为成功。
- 任何自动扩展都继承原请求预算。

### 15.3 指标

```text
request_classification_total
plan_started_total / plan_completed_total
plan_step_duration_seconds
plan_step_refused_total
scope_violation_total
budget_exhausted_total
finding_created_total{severity,type}
finding_time_to_ack_seconds
finding_time_to_resolve_seconds
proposal_approval_latency_seconds
job_pause_total{reason}
redaction_match_total
audit_chain_verify_failure_total
```

## 16. 测试与验收

### 16.1 请求理解验收

覆盖中文自然语言、简称、厂商术语、模糊目标、批量范围、时间窗口和安全事件表述。每条样例验证：领域、目标、风险、计划和澄清问题。

### 16.2 安全验收

- 非法命令、多行命令、参数注入、路径逃逸、SQL 走私全部拒绝。
- 未纳管设备不可连接。
- scope 外 CIDR、namespace、数据库地址不可访问。
- 失效 engagement 的主动评估不可执行。
- profile 未启用时不存在红队工具。
- 模板和 payload 不进入模型上下文。
- 模拟输出中的密码、Token、私钥、Cookie 全部脱敏。
- 紧急停止能停止设备连接、Job、follow 和主动评估。

### 16.3 自动化验收

- 一个请求能自动生成并启动 Job。
- Job 失败后能暂停并显示原因。
- Resume 从断点继续，不重复不可重复步骤。
- 告警可触发对应诊断 Runbook。
- Finding 可以生成案例、提案和报告。
- 变更验证失败时停在决策点，不自动继续。
- 预算耗尽时给出覆盖率，而不是无限重试。

### 16.4 端到端样例

1. “core-sw-1 丢包”→ 数通路径诊断→ Finding。
2. “所有生产 Linux 查异常登录”→ 批量主机体检→ IOC 搜索→案例。
3. “数据库为什么慢”→ TopSQL/锁/连接/复制诊断→报告。
4. “这批 IOS 是否受 CVE 影响”→ 指纹→匹配→只读确认→覆盖率。
5. “修复暴露的 Telnet”→ Finding→提案→审批→备份→执行→验证。
6. Syslog/Trap 告警→聚合→自动诊断→通知→案例。
7. 有效 engagement 下做只读漏洞验证；过期后同一请求必须拒绝。

## 17. 实施路线

### Phase 1：统一请求和结果

- 建立 Request、Classification、Plan、Run、Coverage 数据结构。
- 把现有 `Job`、Finding、Case、Proposal 统一关联到 `request_id`。
- 实现 `ops_classify`、`ops_plan`、`ops_status`。
- UI 增加计划和运行状态卡。

### Phase 2：AI 自动编排现有能力

- 将数通、主机、数据库、容器、安全能力注册为能力目录。
- 增加数通、主机、安全事件和数据库诊断模板。
- AI 生成计划并编译成现有 Job。
- 支持规则化动态扩展和覆盖率。

### Phase 3：告警与自动化闭环

- Syslog/Trap/Health → Finding → Job。
- 增加自动建案例、通知和日报/周报。
- 增加任务重试、暂停、恢复、取消的统一 UI。

### Phase 4：蓝队增强

- 统一 IOC、Finding、Case、Evidence 关系。
- 增加跨资产时间线和影响面。
- 增加修复提案和修复后复核。

### Phase 5：独立受控红队验证

- 仅在前述蓝队和审计能力稳定后立项。
- 实现独立 profile、engagement、范围快照、主动验证预算和模板隔离。
- 首批只做资产指纹、CVE 匹配和只读存在性验证。
- 通过专项安全评审后再决定是否扩大验证器范围。

## 18. 发布门槛

以下条件全部满足，才可将统一编排器设为默认入口：

1. 所有现有 netdev 单项能力回归通过。
2. 计划编译器没有任意命令旁路。
3. 计划、Job、Finding、Case、Proposal 全部具备关联 ID。
4. 只读、评估、提案、执行四类风险边界在 UI 和后端一致。
5. 关键安全测试、注入测试、脱敏测试、审计链测试通过。
6. 至少完成数通、主机、安全事件三个完整端到端流程。
7. 自动化失败时默认暂停或终止，不得静默继续。
8. 所有新工具完成 profile 可见性和工具范围断言。

## 19. 最终产品判断

Hiq 的长期产品形态不是“让 AI 代替运维工程师敲命令”，而是：

> 一个能理解用户目标、自动组织诊断能力、在边界内执行、留下可审计证据，并把发现转成修复闭环的本地优先智能运维平台。

现有代码已经具备大部分数据面和安全脊柱。优先级应放在统一请求编排、Job 自动生成、跨领域资产模型、告警闭环和结果归档，而不是继续孤立增加更多按钮或单点工具。

