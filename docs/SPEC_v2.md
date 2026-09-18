# Hiq v2.0 技术规格说明书

> **版本**: v2.0（修订版）| **日期**: 2026-08-04 | **状态**: 待评审
>
> **修订说明**: 本版基于对 Hiq 当前代码的逐行核查（非凭印象），以及对 swarm-os 内
> MiMo-Code、DeepSeek-Reasonix、openwork、openworker、rooster、PromptHub、pi 六个项目的源码
> 研究，重新界定了"真实差距"与"可借鉴机制"。上一版多处把已实现的能力列为待做项，本版已修正。

---

## 一、现状评估（基于代码核查）

### 1.1 已完成的核心能力

| 能力 | 现状 | 证据 | 评级 |
|------|------|------|------|
| **代码编辑** | 5 种策略（edit_file/write_file/multi_edit/apply_patch/notebook_edit）+ 5 级模糊匹配（Level 0-4）+ 检查点回滚 | `internal/tool/builtin/edit*.go`、`internal/checkpoint/` | ⭐⭐⭐⭐ |
| **记忆系统** | portrait 始终注入（user.md + memory.md + mode.md）+ Dream/Distill 子代理 + 自动调度 + RAG 知识库（FTS5 + 实体向量） | `internal/memory/`、`internal/agent/dream.go`（581 行）、`internal/rag/` | ⭐⭐⭐⭐ |
| **错误恢复** | 11 次 HTTP 指数退避重试 + Retry-After + 状态码分类（429/5xx/401/403）+ RPM 限流 + **4 种循环检测**（n-gram/stormBreaker/repeatSuccessBlock/repetition_truncation） | `internal/provider/retry.go:19`、`internal/agent/agent.go:1395,1401`、`repeat_text.go` | ⭐⭐⭐⭐ |
| **模型管理** | **13 个内置模板**（11 直连 + 2 聚合）+ 统一 effort + 动态注册表 | `desktop/default_registry.json`（实测，非上版的 18） | ⭐⭐⭐⭐ |
| **任务编排** | Auto-plan + Goal 独立验收（goal_judge.go）+ **Max Mode 并行采样**（max_mode.go）+ parallel_tasks/task | `internal/agent/max_mode.go`、`goal_judge.go`、`internal/control/auto_plan.go` | ⭐⭐⭐⭐ |
| **规格驱动** | Compose 模式（grill→spec→implement→verify→review→finish） | `internal/compose/` | ⭐⭐⭐⭐ |
| **Hook 系统** | **12 种事件**（PreToolUse/PostToolUse/PermissionRequest/UserPromptSubmit/Stop/PostLLMCall/SessionStart/SessionEnd/SubagentStop/Notification/PreCompact/Startup）+ 成熟门控/超时/降级/信任 | `internal/hook/hook.go:33-60`（注：目录是单数 `internal/hook/`） | ⭐⭐⭐⭐ |
| **插件/Skill** | Markdown skill（项目/全局作用域）+ MCP 插件 + install_source（GitHub/URL 远程安装 + sha256 + uninstall） | `internal/skill/`、`internal/installsource/` | ⭐⭐⭐ |
| **向量检索（部分）** | 实体级向量搜索**已运行**（SearchEntitiesByVector + 并行余弦）；文档级混合重排**代码已就绪但默认关闭** | `internal/rag/entities.go:1142`、`embedding.go:30`、`boot.go:650`（SetRAGEmbedder(nil)） | ⭐⭐⭐ |

> **核查结论**：Hiq 已实现 MiMo-Code 的全部招牌功能（max_mode/goal_judge/dream/distill/compose），
> 甚至更简洁。MiMo-Code 对 Hiq 的增量价值有限（见 1.3）。

### 1.2 真实差距（代码证实的缺口）

| 差距 | 影响 | 用户痛点 | 优先级 |
|------|------|----------|--------|
| **无操作级故障恢复门** | 代理陷入死循环（反复跑同一失败命令/改同一文件），烧 token 烧用户耐心，用户被迫手动 Ctrl+C | 自主任务失败的首要原因，用户干预最多的场景 | **P0** |
| **权限只有 readOnly 布尔，无风险分级** | "改本地文件"和"发邮件/删知识库/调外部 API"风险完全不同，但当前一刀切；每加一个外向工具都要改 `isIrreversibleOutwardTool` 的 if | 误放行外向操作不可逆；MCP 工具风险无声明 | **P0** |
| **审批是同步阻塞，无 inbox 队列** | IM bot / scheduler 无人值守时，外向操作只能硬拒（用户无感知）；多子任务并行审批会乱序 | 不敢让 Hiq 自动跑批/自动发 PR；bot 场景审批体验差 | **P0** |
| **无编辑前验证** | 所有写入工具在 `writeFileEncoded` 前零语法/AST/类型校验；LSP 诊断只在写入**之后**跑 | 错误写入后才发现，要回滚重来 | **P1** |
| **skill 无内容安全扫描** | install_source 能从任意 GitHub 装 skill，但对 SKILL.md 内容本身无投毒检测（SSRF 只防网络层） | 用户不敢装外部 skill；供应链攻击面 | **P1** |
| **skill 无版本追踪/更新检测** | 装的 skill 过时了不知道；同名 skill 重复安装无法去重 | skill 维护成本高 | **P1** |
| **memory 写回无去重/无红线保护** | dream 的 merge 是软约束（靠 prompt），portrait 可能攒语义重复条目；用户标记的"红线"无硬保护 | portrait 臃肿挤占 context；用户不敢开 auto-dream | **P2** |
| **文档级向量检索被关闭** | 混合重排代码已写完，但 `boot.go:650` 设 nil，文档检索退化为纯 FTS5 | RAG 语义检索精度受限（实体检索不受影响） | **P2** |
| **无 per-model context 预算** | 用户无法控制"何时压缩"，只能吃模型默认窗口；长上下文质量下降+成本分级（如超 272K 翻倍） | 成本和质量不可控 | **P2** |
| **skill 资产无法导出到其他工具** | Hiq 里调好的 skill 锁在内部，导出到 Cursor/Claude Code 要手动拷文件 | "资产带不出去"，降低粘性 | **P2** |

### 1.3 已排除的改进项（上版误列为待做，实际已完成）

| 改进项 | 排除原因 |
|--------|----------|
| **错误恢复"6 层重试 + 4 循环检测"** | 已实现（`retry.go` MaxRetries=10 + 指数退避；`agent.go` 4 循环检测器）。仅缺统一目录，功能完整 |
| **Hook 系统优化** | 失实。实际 12 事件 + 成熟门控/降级/trust（目录是 `internal/hook/` 单数） |
| **向量搜索基础设施** | 已实现，只需接通 `boot.go:650` |
| **Max Mode / Goal / Dream / Compose** | 已实现 |
| **可写并行任务** | 文件冲突风险，维持只读并行 + 串行写入 |
| **向 system prompt 注入"如何避免循环/如何分级风险"的指令** | **违反约束 2**。这些必须是 host 侧硬机制，不能靠 prompt 说教（模型不可靠 + 膨胀提示词） |
| **要求用户理解 RiskClass 四级 / 操作指纹 / Inbox 状态机才能使用** | **违反约束 1**。这些是内部实现，用户只需体验"本地操作不问、外向操作会问" |
| **per-model context 预算的技术化配置（如 `{"openai/gpt-5.6"="272K"}`）** | **违反约束 1**。学习成本太高；改为带默认值的简单百分比开关 |
| **pi 式 TS 扩展系统 / openwork 云控制面 / GPG 沙箱** | 过度设计，偏离本地优先 + 安全定位 |

---

## 二、改进目标（基于真实差距 + 外部最佳实践）

### 2.0 两条硬约束（贯穿所有设计）

> Hiq 当前的 base system prompt 仅 ~900 字符（极克制，只有原则/工具用法/plan mode）。
> 任何新功能必须满足以下两条，否则不做：

1. **不增加用户学习成本** — 功能应在后台静默工作，用户"零配置"即可受益。
   凡是要用户理解新概念（RiskClass 四级？操作指纹？Inbox 状态机？）才能用的设计，都判为失败。
   用户的心智模型应保持极简：**"Hiq 自己会处理，我只在被问时点一下"**。

2. **不膨胀提示词** — 功能实现不得向 base system prompt 注入新指令。
   Hiq 的 base prompt 之所以只有 900 字符，是因为"能力靠工具 + 机制，不靠 prompt 说教"。
   凡是"要在 system prompt 里告诉模型如何循环检测/如何分级风险/如何去重"的设计，
   都应改为 **host 侧硬机制**（纯函数/代码逻辑），让模型完全无感。

### 2.1 核心目标（按用户痛点排序，均满足 2.0 约束）

1. **操作级故障恢复门** — host 侧硬机制，模型无感；用户只看到"已停止重复尝试"一句话（借鉴 DeepSeek-Reasonix）
2. **权限风险分级 + 审批收件箱** — 风险分级对用户透明（工具自动归类），inbox 只是"被问到的审批换个地方答"（借鉴 openworker）
3. **编辑前验证** — 写盘前自动校验，用户无感（除非真有错才提示）
4. **skill 安全与生态** — 安装前自动扫描，用户只在有风险时被问；版本追踪自动（借鉴 rooster / PromptHub）
5. **memory 写回去重 + 红线保护** — dream 内部逻辑，用户零感知
6. **context 预算 + 向量检索接通** — 配置项（默认值合理则无需用户操心）+ 复用已有代码

### 2.2 借鉴来源对照

| 目标 | 主要借鉴 | Hiq 现状 | 用户价值 |
|------|----------|--------------|----------|
| 故障恢复门 | **DeepSeek-Reasonix** `internal/recovery/`（三层预算 + 纯函数决策 + reviewer） | 只有文本 n-gram 重复检测，无操作级失败预算 | ⭐⭐⭐⭐⭐ |
| 风险分级 + inbox | **openworker** `risk.py`（4 级 RiskClass）+ `inbox.py`（pending→resolved 状态机） | readOnly 布尔 + 同步阻塞 Approver | ⭐⭐⭐⭐⭐ |
| 编辑前验证 | 通用实践 + Hiq 已有的 `go/parser`（codeindex.go 在用） | 0 覆盖，写入后才发现 | ⭐⭐⭐⭐ |
| skill 安全扫描 | **rooster** `_loader.py`（静态投毒检测）+ **PromptHub** `SkillSafetyReport`（AI 分级报告） | 只防网络层 SSRF，无内容扫描 | ⭐⭐⭐⭐ |
| skill 版本/分发 | **PromptHub**（fingerprint + installed_content_hash + 多平台目录分发） | 无版本追踪、无更新检测、无导出 | ⭐⭐⭐⭐ |
| memory 去重/红线 | **rooster** `user_writer.py`（写回去重）+ 红线保护思路 | portrait merge 是软约束，无硬去重/无红线硬保护 | ⭐⭐ |
| context 预算 | **MiMo-Code** `/context-limit`（per-model 工作预算，clamp 到实际窗口） | 固定阈值，用户不可配 | ⭐⭐⭐ |
| 向量检索接通 | 复用 Hiq 已有代码（`embedding.go` Rerank + HE embedder） | 代码就绪，`boot.go:650` 关闭 | ⭐⭐⭐ |

---

## 三、功能规格

### 3.1 操作级故障恢复门（P0）—— 借鉴 DeepSeek-Reasonix

> **满足两条约束**：① host 侧硬机制，模型完全无感（不注入任何 prompt）；② 用户零配置，
> 只在死循环被打破时看到一句自然语言提示（"已停止重复尝试，保留已完成工作"），无需理解"操作指纹/预算"等概念。

#### 用户痛点
代理反复跑同一条失败的 bash 命令、反复改同一个文件导致编译错误、反复重试同一个失败的测试。
用户不得不手动 Ctrl+C。这是自主任务失败的首要原因，也是用户干预最多的场景。

#### 设计

**三层失败预算**（host-owned，模型不可重置）：

| 预算 | 阈值 | 触发动作 | 借鉴 |
|------|------|----------|------|
| 操作级 | 同一操作指纹失败 **3 次** | 仅停止该操作，其他无关操作继续 | Reasonix `MaxOperationFailures=3` |
| 会话级 | 一个 turn 累计 **6 次**失败 | 停止本 turn 所有写/验证操作（**只读诊断仍允许**） | Reasonix `MaxEpisodeFailures=6` |
| 审查级 | reviewer 拒绝 **3 次** | 停止当前变更 | Reasonix `MaxReviewRejects=3` |

**纯函数决策路由**（借鉴 Reasonix `decision.go:103` Decide）：
- 输入 `Facts{操作指纹, 当前预算状态, 失败分类}`，固定 10 步优先级路由
- 输出 5 种 Route：`bypass / allow / review / stop / stop_turn`
- **关键设计**：失败是"执行可靠性信号"非"任务安全边界"——只停失败的那个操作，其他写操作继续放行

**操作指纹**：`hash(tool_name + 关键参数)`，用于去重"同一个操作"

**失败分类**（借鉴 Reasonix `types.go:43`）：
- `execution`（执行失败，可重试）/ `mutation`（变更失败）/ `transient`（网络等瞬时）/ `verification`（验证失败）
- `permission/sandbox/user-block` **永远不进** FailureEvent（这些是安全边界，非可靠性问题）

**独立 reviewer**（可选，Phase 2）：对"改了策略/扩了范围"的恢复提案，调独立模型判定 continue/confirm，附 `ChangeKind`（same_strategy/strategy/scope/risk）。复用 Hiq 多厂商 provider 调便宜模型。

#### 实现方案
```
internal/recovery/
├── gate.go         # 门协调器（拦截工具调用前后）—— 借鉴 Reasonix gate.go
├── decision.go     # 纯函数 Decide(f Facts) Route —— 借鉴 Reasonix decision.go:103
├── budget.go       # 三层预算常量 + 暂停文案 —— 借鉴 Reasonix budget.go
├── fingerprint.go  # 操作指纹
└── types.go        # Facts/Route/FailureEvent/ChangeKind
```
接入点：`internal/agent/agent.go` 工具执行前后（借鉴 Reasonix `execute_one.go:361` 的 BeforeMutation/ObserveResult）。

#### 验收标准
- [ ] 同一操作失败 3 次后自动停止该操作（不阻塞其他操作）
- [ ] 一个 turn 累计 6 次失败后停止写操作（只读诊断仍可跑）
- [ ] 暂停文案清晰（"已停止重复尝试，保留已完成工作，发 continue 开新一轮"）
- [ ] permission/sandbox 拒绝不计入失败预算

---

### 3.2 权限风险分级 + 审批收件箱（P0）—— 借鉴 openworker

> **满足两条约束**：① 风险分级是 host 内部实现细节，**用户永远看不到"RiskClass"这个词**——
> 用户感知到的只是"改文件不问我，发邮件会问我"（这本来就是直觉预期）。无需学习新概念。
> ② 分级是代码逻辑（`classify()` 函数 + 工具元数据），**不进 prompt**。模型不知道自己被分级了。
> inbox 对用户也只是"之前当面问你的审批，现在可以稍后答"——是体验改善，不是新概念。

#### 用户痛点
1. 改本地文件（可 rewind 兜底）和发邮件/删知识库（不可逆外溢）风险完全不同，但当前一刀切
2. IM bot / scheduler 无人值守时，外向操作直接 Deny，用户根本不知道错过了什么
3. 多子任务并行要审批时，同步 Approver 会乱序

> **用户视角**（关键）：用户不需要理解 4 级分类。他们只体验两件事：
> - **本地操作**（读/写文件/跑命令）：按现有权限策略走，和今天一样
> - **外向操作**（发邮件/删知识库/调外部 API）：总会被问一次，"always" 可记住
> 唯一新增的体验是："被问的审批，如果我此刻不在，它会等我回来答"（inbox）。

#### 设计

**4 级 RiskClass**（借鉴 openworker `risk.py:18-23`）：

| 级 | 含义 | 处理 | Hiq 对应 |
|----|------|------|--------------|
| `READ` | 只读 | 所有模式直接放行 | 现有 readOnly=true |
| `WRITE_LOCAL` | 本地写（可 rewind） | path-scoped 校验（必须在 writable root 内） | 现有 readOnly=false 的文件写 |
| `EXEC` | 执行命令 | 查 allowlist + **拒绝含 shell 操作符的命令进 allowlist**（防 `git status && rm -rf`） | bash 工具 |
| `EXTERNAL` | 外向操作（发邮件/删 KB/外部 API） | 默认需审批，进 inbox | 现有 `isIrreversibleOutwardTool` 的 if |

- **MCP 工具默认 `EXTERNAL` 风险**（借鉴 openworker `mcp/config.py:38` requires_approval=True），可 per-server 覆盖
- **per-tool override**：配置式声明 `risk: external`，无需改代码
- **task-scoped standing rule**（借鉴 openworker `permissions.py:62-80`）：用户答一次"Allow every time" → 写进 `task_rules` → 后续同 target 调用引擎层直接放行；只有 EXTERNAL 能进 standing rule，exec/write_local 永远不能

**审批收件箱 Inbox**（借鉴 openworker `inbox.py`）：

| 特性 | 设计 |
|------|------|
| 状态机 | `pending → resolved`（**一次性、首答者赢、幂等**） |
| 幂等 | 按 `(session_id, tool_call_id)` 去重，durable resume 重复触发复用已有 item |
| 5 种 kind | approval / question / notification / plan / directory |
| 可等待 | agent 挂起 `wait(item_id)` 直到人答复 |
| 双可见性 | `INLINE`（attended 会话，composer 里答）/ `INBOX`（unattended，跨会话队列） |
| resume 对账 | 返回该会话 pending 的 + 离开期间已被答的（recap） |

**无人值守优雅降级**（借鉴 openworker `server/manager.py:2686-2706`）：
- deliverable 类写操作（path-scoped 在任务工作区内）+ 任务显式 allow 的工具 → 自动 ONCE 放行
- 其他全 park 到 inbox 挂起，不硬拒

#### 实现方案
```
internal/permission/
├── risk.go         # RiskClass enum + classify() —— 借鉴 openworker risk.py
└── (改) policy.go  # Decide 从 readOnly bool 升级到 risk RiskClass
internal/inbox/     # 新增
├── store.go        # InboxStore（map + mutex + 持久化）
├── item.go         # InboxItem 状态机（借鉴 openworker inbox.py）
└── approver.go     # 把 permission.Approver 从同步接口改成"返回 item id + 等 channel"
```
- Hiq 已有 `internal/event` 事件总线 + bot 通道（feishu/weixin/qq），inbox 事件接到 bot 即跨会话审批
- UI 侧渲染"审批卡片"（plan_before/plan_after 对比，借鉴 Reasonix `types.go:181` ToEventApproval）

#### 验收标准
- [ ] 工具按 4 级风险分级，MCP 工具默认 EXTERNAL
- [ ] standing rule 支持"Allow every time"（仅 EXTERNAL）
- [ ] inbox 支持 pending→resolved，幂等，首答者赢
- [ ] 无人值守时外向操作 park 到 inbox（不硬拒），attendant 会话 INLINE 答
- [ ] resume 后对账（pending + recap）

---

### 3.3 编辑前验证（P1）

> **满足两条约束**：① 完全自动，用户零配置零学习——写文件时自动校验，没错就无感，
> 有错才提示（且不写盘）。用户不需要知道"L1/L2/L3"分层。② 校验是 host 侧代码（`go/parser`/`json.Valid`），
> 不进 prompt——模型只看到"写入失败：语法错误"，不知道有校验层。

#### 用户痛点
所有写入工具在 `writeFileEncoded` 前零校验；LSP 诊断只在写入**之后**跑。错误已经落盘，要回滚重来。

#### 设计（分层，渐进式）

| 层 | 内容 | 语言 | 延迟目标 |
|----|------|------|----------|
| L1 | 语法检查（parse） | Go（`go/parser`，codeindex.go 已在用）/ JSON（`json.Valid`）/ YAML / Python（`ast.parse`） | <100ms |
| L2 | 类型检查（可选，按配置） | Go（`go vet`）/ TypeScript（`tsc --noEmit`） | <2s |
| L3 | LSP 诊断（**前移**到写盘前） | 跟随现有 LSP 集成 | <2s |

- 写盘**前**跑 L1（必选）+ L2/L3（按配置），失败则不写盘并返回错误
- 复用 Hiq 已有的 `PreEditHook`（当前只做快照），加一个"校验"步骤
- 注意：`apply_patch.go:312` 的 Phase 1 Validate 只校验补丁可应用性，**不是**语法校验，需区分

#### 实现方案
```
internal/validation/
├── validator.go    # 校验器核心（按扩展名分发）
├── syntax.go       # L1 语法（go/parser、json、yaml、python ast）
└── typecheck.go    # L2 类型（go vet、tsc）
```
配置 `[validation] syntax=true type_check=false`。

#### 验收标准
- [ ] `.go`/`.json`/`.py`/`.yaml` 写盘前语法检查
- [ ] 语法错误时不写盘 + 清晰错误信息
- [ ] 类型检查可选，默认关（避免拖慢）

---

### 3.4 skill 安全与生态（P1）—— 借鉴 rooster / PromptHub

> **满足两条约束**：① 安全扫描/版本追踪全自动，用户只在"有风险"时被问（和今天装 skill 的体验一样，
> 只是多一道安检）。多平台分发是可选的"导出"按钮，不强制。② 扫描是 host 代码 + 可选 LLM 调用，
> 不进 base prompt。

#### 用户痛点
1. install_source 能从任意 GitHub 装 skill，但对 SKILL.md 内容无投毒检测
2. 装的 skill 过时了不知道，同名 skill 重复安装无法去重
3. Hiq 里调好的 skill 无法导出到 Cursor/Claude Code（此项视用户画像，可降级）

#### 设计

**A. 内容安全扫描**（借鉴 rooster `_loader.py` + PromptHub `SkillSafetyReport`）：
- plan 阶段加"内容安全扫描"步骤
- 静态检测：`eval`/base64 混淆/隐藏网络请求/skill 描述里的系统命令（借鉴 rooster `AdvancedGuard.verify_skill`）
- AI 分级报告（可选）：`level: safe/warn/high-risk/blocked` + findings 列表 + recommendedAction（借鉴 PromptHub `SkillSafetyReport` schema）
- high-risk 内容要求用户显式确认才 apply

**B. 版本追踪 + 更新检测**（借鉴 PromptHub）：
- 每个 skill 存 `source_url` + `installed_content_hash` + `installed_version`
- `directory_fingerprint`（目录级指纹）解决"同名 skill 重复安装"去重
- 启动时或手动触发对比远端 hash 提示更新
- Hiq 的 install_source 已有 plan/apply，加一个"检查更新"的 op

**C. 多平台 skill 分发**（借鉴 PromptHub `platforms.ts`）：
- 一份 skill 编辑一次，一键 copy/symlink 到 Claude Code / Cursor / Windsurf 的 skills 目录
- 维护"外部工具 → skills 目录"映射表（PromptHub `SkillPlatform` 含 darwin/win32/linux rootDir 可直接参考）
- symlink 在 Windows 需特权，用 PromptHub 的 copy fallback 机制
- **这是用户粘性的差异化能力**：用户机器上大概率同时有 Cursor/Claude Code

**D. GitHub 仓库作为 skill 源**（借鉴 PromptHub `SkillStoreSource` git-repo 类型）：
- 订阅一个 GitHub 仓库作为 skill 源，列出里面所有 SKILL.md 供一键安装
- 比 install_source 的"单 URL 安装"更进一步，提供"浏览发现"体验

#### 实现方案
```
internal/installsource/
├── (改) plan.go      # plan 阶段加内容安全扫描步骤
├── (改) types.go     # 加 source_url/installed_content_hash/installed_version 字段
├── safety.go         # 静态投毒检测（借鉴 rooster AdvancedGuard）
└── update.go         # 更新检测（对比远端 hash）
internal/skill/
└── export.go         # 多平台分发（借鉴 PromptHub platforms.ts 路径表）
```

#### 验收标准
- [ ] 安装前扫描 SKILL.md 内容，high-risk 要求确认
- [ ] skill 记录来源 + hash，支持更新检测
- [ ] 一键导出 skill 到 Cursor/Claude Code 目录
- [ ] 支持"GitHub 仓库作为 skill 源"浏览安装

---

### 3.5 memory 写回去重 + 红线保护（P2）—— 轻量借鉴 rooster

#### 用户痛点
dream agent 可整体改写 portrait（含用户核心约束如"永远不要自动提交代码"），用户不敢开 auto-dream。

#### 设计

**A. 写回去重**（借鉴 rooster `user_writer.py:60-78`）：
- dream 的 prompt 已要求"merge not append"（`dream.go:146`），但无硬性去重，portrait 可能攒出语义重复条目
- 同一字段 5 轮内不重复更新 + 前 40 字符前缀去重（廉价启发式）
- 更好：用 Hiq 已有的 embedding 做语义去重（比 rooster 更容易做好）

**B. 用户红线（red lines）保护**（轻量版，借鉴 rooster 章节白名单思路）：
- dream prompt 已把 user.md 里的"red lines"定义为"changes slowly (months/years)"（`dream.go:135`），但仍是软约束
- 仅对用户**显式标记**的 `<!-- protected -->` 区段做硬保护（dream 写回时跳过），不做全套章节分类
- 注：Hiq 的 portrait 是**事实画像**（user.md/memory.md），不是 rooster 的**行为契约 SOUL.md**，风险面更小，无需照搬全套白名单

> **不做**：rooster 的"三类信号分类（CORRECTION/PREFERENCE/MILESTONE）"——Hiq 的 memory 已有等价的 `Type` 系统
> （`store.go:64`：TypeUser/TypeFeedback/TypeProject/TypeReference），再加一套分类是重复造轮子。

#### 实现方案
- `internal/agent/dream.go` 写回逻辑加 `<!-- protected -->` 区段跳过 + 前缀/语义去重
- 不改 memory schema（复用现有 Type）

#### 验收标准
- [ ] `<!-- protected -->` 标记的区段 dream 不可改写
- [ ] 写回前去重（前缀 + 可选语义）

---

### 3.6 向量检索接通 + context 预算（P2）—— 复用已有代码 + 借鉴 MiMo-Code

> **满足两条约束**：① 向量检索接通对用户完全无感（RAG 搜索自动变准）；context 预算给**合理默认值**，
> 用户无需配置即可受益，高级用户才调。② 都不进 prompt（向量是检索层，预算是压缩层）。

#### A. 接通文档级向量检索（复用已有代码）—— 纯收益，优先做
- `boot.go:650` 把 `SetRAGEmbedder(nil)` 改成按配置构造 HE embedder
- 代码全在（`embedding.go` Rerank + `he_client.go` Embed），只需接线
- 对用户：rag_search 自动变准，零配置、零学习成本
- **难度极低**（半天，代码已就绪）

#### B. context 预算（借鉴 MiMo-Code `/context-limit`）—— 有默认值，用户可不配
- **默认开启一个保守预算**（如窗口的 80%），用户无需任何配置即省钱+提质量
- 高级用户**可选**配 `[compaction] context_budget_pct = 70`（一个百分比，不是 per-model 技术配置）
- 避免 MiMo-Code 那种 `max_context = {"openai/gpt-5.6"="272K"}` 的技术化配置（学习成本太高）
- 值永远 clamp 到 provider 实际窗口
- 实现位置：`internal/agent/compact.go` 触发判断
- **难度低**（1-2 天）

---

## 四、实施计划

### Phase 1: 可靠性（最高优先级，2-3 周）

| 任务 | 工作量 | 优先级 | 借鉴 |
|------|--------|--------|------|
| 操作级故障恢复门 | 1-2 周 | P0 | DeepSeek-Reasonix |
| 权限风险分级（4 级 RiskClass） | 3-5 天 | P0 | openworker |
| 审批收件箱（inbox + 优雅降级） | 1 周 | P0 | openworker |
| 编辑前验证（L1 语法） | 3 天 | P1 | 通用 |

### Phase 2: skill 生态（1.5 周）

| 任务 | 工作量 | 优先级 | 借鉴 |
|------|--------|--------|------|
| skill 内容安全扫描 | 3-5 天 | P1 | rooster + PromptHub |
| skill 版本追踪 + 更新检测 | 3 天 | P1 | PromptHub |
| 多平台 skill 分发 | 3-5 天 | P2 | PromptHub |

### Phase 3: 智能增强（1 周）

| 任务 | 工作量 | 优先级 | 借鉴 |
|------|--------|--------|------|
| 接通文档级向量检索 | 半天 | P2 | 复用已有代码 |
| context 预算（带默认值） | 1-2 天 | P2 | MiMo-Code |
| memory 写回去重 + 红线保护 | 2-3 天 | P2 | rooster（轻量版） |

**总计：约 5 周**（Phase 1 是核心，2-3 周；Phase 2/3 可并行或延后）

> **关于多平台 skill 分发的取舍**：借鉴 PromptHub 把 skill 导出到 Cursor/Claude Code，前提是 Hiq
> 用户群确实**同时使用**这些工具。若 Hiq 定位为"一站式桌面 AI 助手"（用户不再用 Cursor），此项价值
> 有限，应降级或推迟。建议落地前先确认用户画像。

---

## 五、技术决策

> **顶层原则**：所有功能的实现首选 host 侧硬机制（纯函数/代码逻辑），其次工具，最后才考虑 prompt。
> 凡是要往 base system prompt（当前 ~900 字符）加内容的设计，默认否决。

| 决策项 | 方案 | 理由 |
|--------|------|------|
| **故障恢复门** | 三层预算 + 纯函数决策 + 可选 reviewer | host 侧硬机制，模型无感，不进 prompt；借鉴 Reasonix |
| **权限分级** | 4 级 RiskClass（内部实现，用户不可见）+ MCP 默认 EXTERNAL | 用户只体验"本地不问/外向会问"，无需学概念；classify() 是代码不进 prompt |
| **审批** | inbox 队列 + 无人值守优雅降级 | 对用户是"审批可稍后答"的体验改善，不是新概念 |
| **编辑验证** | 写盘前自动语法检查（默认开），类型/Lint 可选 | 用户零配置；出错才提示；host 代码不进 prompt |
| **skill 安全** | 安装前自动静态检测 + 可选 AI 报告 | 用户只在有风险时被问，和今天装 skill 体验一致 |
| **memory 保护** | `<!-- protected -->` 红线 + 写回去重 | dream 内部逻辑，用户零感知 |
| **context 预算** | 带合理默认值（如窗口 80%），用户可不配；高级用户调一个百分比 | 避免技术化配置（学习成本）；默认值让多数用户零配置受益 |
| **向量检索** | 接通已有代码（boot.go 改一行） | 用户无感（搜索自动变准）；零 prompt 负担 |

---

## 六、风险与应对

| 风险 | 影响 | 应对 |
|------|------|------|
| **故障恢复门误判**（把正常重试当死循环停了） | 任务提前中止 | 只停失败操作不阻塞其他；只读诊断不受限；阈值参考 Reasonix 实战值（3/6/3） |
| **风险分级标签不全** | 工具被错分级 | MCP 默认 EXTERNAL（安全默认）；内置工具逐个标注 + 测试 |
| **inbox 持久化复杂** | resume 对账出错 | 借鉴 openworker 幂等设计（按 tool_call_id）；先做内存版，持久化分阶段 |
| **skill 安全扫描误报** | 用户装不了 skill | 静态检测保守（只拦明显恶意）；AI 报告分级，warn 不阻断 |
| **过度工程** | 浪费工时 | Reasonix 团队教训：曾用 1600 行数学最后等于一个开关。任何"智能调度"先验证影响模型几个百分点决策 |

---

## 七、验收标准

### 7.1 功能验收
- [ ] 同一操作失败 3 次自动停止（不阻塞其他）
- [ ] 一个 turn 累计 6 次失败停止写操作（只读仍可）
- [ ] 工具按 4 级风险分级，MCP 默认 EXTERNAL
- [ ] inbox 支持 pending→resolved，无人值守 park 不硬拒
- [ ] 编辑前语法检查（.go/.json/.py/.yaml）
- [ ] skill 安装前内容安全扫描
- [ ] skill 版本追踪 + 更新检测
- [ ] memory `<!-- protected -->` 区段 dream 不可改写 + 写回去重
- [ ] 文档级向量检索接通（默认开启，用户无感）
- [ ] context 预算带默认值（用户零配置即受益，高级用户可调百分比）

### 7.2 性能验收
- [ ] 故障恢复门决策 <1ms（纯函数）
- [ ] 编辑前语法检查 <100ms
- [ ] inbox 挂起/唤醒 <10ms
- [ ] 向量查询延迟 <200ms（实体检索已达标）

### 7.3 安全验收
- [ ] skill 高风险内容要求显式确认
- [ ] MCP 工具默认 EXTERNAL 需审批
- [ ] standing rule 只允许 EXTERNAL 生成

---

## 八、后续演进（v2.1+）

### 短期（v2.1）
- **故障恢复门 reviewer**（独立模型判定恢复提案）
- **skill 市场**（GitHub 仓库源 + 浏览发现，借鉴 PromptHub SkillStoreSource）
- **memory 语义去重**（用 embedding 替代前缀去重，超出 P2 的启发式版本）

### 中期（v2.2）
- **双工具 MCP 收口**（search/execute capabilities，借鉴 openwork；等 MCP 多到 token 爆了再做。注意：这是减少 prompt 的优化，不是增加）
- **智能路由 + 成本优化**（按任务选模型，用户无感）

### 不做（明确排除）
- **向 system prompt 注入能力说明/行为指令**（违反约束 2：能力靠工具和 host 机制，不靠 prompt 说教）
- **要求用户学习新概念才能用的功能**（违反约束 1：功能应后台静默工作）
- **pi 式 TS 扩展系统**（跑任意代码，与安全优先冲突 + 学习成本高）
- **openwork Den 云控制面**（太重，偏离本地优先定位）
- **GPG 签名沙箱**（过度设计，Claude Code 也不用）
- **shell hook JSON 输出变换**（增加 hook 作者学习成本，暂缓；现有 pass/block 够用）
- **DeepSeek Memory Compiler 全套**（Reasonix 自己删了 1600 行控制平面，证明易过度工程）

---

## 附录：借鉴来源索引（swarm-os 内源码）

| 项目 | 位置 | 主要借鉴点 |
|------|------|-----------|
| **DeepSeek-Reasonix** | `swarm-os/DeepSeek-Reasonix/` | 故障恢复门（`internal/recovery/`）、Memory Compiler 简化版（`internal/memorycompiler/`，慎抄） |
| **openworker** | `swarm-os/openworker/openworker-main/` | 4 级 RiskClass（`coworker/risk.py`）、approval inbox（`coworker/inbox.py`）、无人值守降级（`server/manager.py`） |
| **MiMo-Code** | `swarm-os/MiMo-Code/MiMo-Code-main/` | context-limit（`config.ts:259`）、Compose Never-Ask 降级、distill SQL 模板 |
| **rooster** | `swarm-os/rooster/` | 写回去重（`user_writer.py:60`）、红线保护思路（`soul_writer.py:22`，轻量借鉴）、skill 投毒检测（`skills/_loader.py:88`） |
| **PromptHub** | `swarm-os/PromptHub/` | 多平台分发（`platforms.ts`）、版本指纹（`skill.ts`）、AI 安全报告（`SkillSafetyReport`）、skill 源（`SkillStoreSource`） |
| **openwork** | `swarm-os/openwork/` | extensions-export 密钥脱敏（`extensions-export.ts`）、skill 目录约定（`skills.ts`） |
| **pi** | `swarm-os/pi/` | 生命周期事件总线（`extensions.md:276`）、项目信任模型（`project_trust`）—— 概念借鉴，务实版实现 |

---

**Hiq v2.0 — 聚焦真实差距，借鉴六项目最佳实践，成为最可靠的多厂商 AI 编程助手。**
