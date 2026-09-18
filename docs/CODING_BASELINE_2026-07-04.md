# hiq 编码能力升级 · 技术核查报告（合并定稿）

> **日期**：2026-07-04 · **状态**：经逐行源码复核
> **方法**：15 个 agent 分批逐行通读 controller/agent/input/auto_plan/task/evidence/chat_tui/desktop(app+tabs)/frontend(App+bridge+types+tools+useController)/serve/bot/acp/boot/config/skill/permission/checkpoint/compact/plugin/mcpdiag/proc/sandbox/hook + MiMo-Code compose/runtime/worktree/plan-agent + 全部文档
> **复核**：本报告每条断言均经直接读源码验证，标注 ✅

---

## 第零部分 · 核心结论

1. **Plan Mode 是活跃的对外 API**（`SetPlanMode`/`SetMode`/`PlanMode()` 有真实外部调用者：chat_tui、serve、desktop），不是死代码。重构必须改语义而非删除。
2. **重构 Plan Mode 牵动 ~30 个文件**——逐行核查后成立。正确方案是**增量叠加 compose，不动 Plan Mode 现有代码**。
3. **逐行核查发现 6 个真 bug + 8 个中危设计缺陷 + 11 处死代码 + 30 条文档债**。本报告逐条列出，每条附源码行号。
4. **MiMo compose 是确定性脚本不是 prompt 引导**——hiq 必须用 Go 写确定性编排器，不能让模型自己走 7 阶段。
5. **provider 层不支持 schema 是 compose 移植最大缺口**——`provider.Request` 无 ResponseFormat 字段。

---

## 第一部分 · 6 个真 bug / 功能缺陷（🔴 高危）

### H1 · serve 切换模型后工具审批静默失效 ✅
**位置**：`internal/serve/serve.go:100-104`（switchModel 重建 controller）vs `serve.go:247/255`（Run/RunGraceful 调 EnableInteractiveApproval）
**事实**：switchModel 用 `boot.Build` 重建 controller，但**没有**对新 controller 调 `EnableInteractiveApproval()`。boot 给的是 `headlessGate`（boot.go:783，sink=nil，不发 ApprovalRequest）。Run/RunGraceful 只对初始 controller 调过一次。
**后果**：在 serve 里执行 `/model x` 或 `/effort x` 后，工具审批不再作为 `approval_request` 事件推到 SSE，浏览器看不到、无法批。plan 审批因走 `requestApproval` 直发（不经 executor gate）反而仍工作。
**对照**：bot/ACP 在每次 NewSession 后都补调（gateway.go:526、service.go:245/323）唯独 serve 的 switchModel 漏。
**修复**：switchModel 在 `s.ctrl = newCtrl`（L117）后加 `newCtrl.EnableInteractiveApproval()`。

### H2 · plan mode 下 explore/research subagent skill 完全不可用 ✅
**位置**：`internal/skill/tools.go:54`（run_skill ReadOnly=false）、`:212`（subagentSkillTool ReadOnly=false）、`:160`（read_skill ReadOnly=true 但拒绝 subagent）
**事实**：dedicated 工具 ReadOnly=false + run_skill ReadOnly=false，全被 agent.go:1415 拦。read_skill ReadOnly=true 但 `tools.go:193` 拒绝 subagent。plan 阶段最需要的 explore/research 代码探索能力，通过 skill 体系完全用不了。
**后果**：plan 阶段只能用 read_file/grep 直接探索，subagent skill 体系在 plan 阶段是死的。

### H3 · PlanModeMarker 提示文字列了已删工具和被拦工具 ✅
**位置**：`internal/control/input.go:18`
**事实**：marker 字面写 `read_file, ls, grep, glob, web_fetch, task are available`。其中 `ls`/`glob` 工具早已删除（README/GUIDE 还在宣传），`task` ReadOnly=false（task.go:148）实际被 agent.go:1415 拦。
**后果**：模型照提示调用就收到 "blocked"，浪费一轮。

### H4 · TUI 双源同步缺口（auto-plan 触发后 TUI 状态错乱）✅
**位置**：`internal/cli/chat_tui.go`（m.planMode 字段，全文件零处 `ctrl.PlanMode()` 读取）+ `internal/control/auto_plan.go:35`
**事实**：TUI 的 `m.planMode` 只在 cycleMode（L3048）/审批（L2096）/`/goal`（L3557）时写，**从不**从 `m.ctrl.PlanMode()` 读回。controller 侧 auto-plan（auto_plan.go:35）会自主翻转 plan，TUI 不感知。
**后果**：auto-plan 触发后，TUI 模式标签（modeTagText L3084）始终显示 "Auto"/"Ask"，**从不显示 "Plan"**，用户气泡不带 `[plan]` 前缀，gitstatus 配色不变。同一功能（plan）两种视觉表现。
**修复方向**：删 TUI 的 `m.planMode`，所有读点改读 `m.ctrl.PlanMode()`（写点保留 SetPlanMode）。

### H5 · handleTabClose 漏删 4 个 per-tab map ✅
**位置**：`desktop/frontend/src/App.tsx:1876-1898`
**事实**：handleTabClose 只 `setModesByTab` 删 key（L1878-1883），没有 `setCollaborationModesByTab`/`setToolApprovalModesByTab`/`setGoalsByTab`/`setGoalDraftModesByTab` 的删除。后 4 个靠 L1896 `refreshTabMetas()` + tabMetas effect（L1031-1108）兜底。
**风险**：closeTab 失败或 refreshTabMetas 延迟时，关闭的 tab 的状态残留。

### H6 · pendingPlanRevision 跨 tab 串扰 ✅
**位置**：`desktop/frontend/src/App.tsx:897`（全局单值 state）+ `:1352-1357`（effect）+ `:2376-2379`（写入）
**事实**：`pendingPlanRevision` 是全局单值，不 per-tab，effect 依赖 `[pendingPlanRevision, send, state.running]` 无 activeTabId 守卫。用户在 tab A 的 plan 审批点 Revise 草拟文本，切到 tab B 且 B 的 running 变 false 时，L1356 会把 A 的 revision `send()` 到 B。

---

## 第二部分 · 8 个中危设计缺陷（🟡）

### M1 · handleSend 裸传 collaborationMode（goal 草稿态泄漏）✅
**位置**：`App.tsx:1460`
**事实**：`setControllerCollaborationMode(collaborationMode)` 直接传裸值，未走 `goalDraftMode.ts:44-51` 的 `controllerCollaborationMode()` helper。当 `collaborationMode === "goal"` 且 goal 为空（草稿态）时，把 "goal" 发给后端，而后端空 goal 时应收 "normal"。
**对照**：switchModel L1236 和 controllerReady effect L1277 都正确用了 helper。

### M2 · ACP 对 plan 审批推误导性 "Always allow" 选项 ✅
**位置**：`internal/acp/dispatch.go:219,254-263`
**事实**：`approvalOptions(a.Tool, a.Subject)` 不区分 planApprovalTool，会给 plan 审批推 OptAllowAlways/OptAllowPersistent（L258-259）。但 controller 对 plan 审批强制忽略 session/persist（controller.go:3274/3280）。host UI 显示无意义的"永久允许"按钮。

### M3 · bot 无法手动进入/退出 plan-mode ✅
**位置**：`internal/bot/gateway.go`（无 /plan 命令）+ `session.go:40-49`
**事实**：命令集只有 stop/new/reset/approve/deny/answer/status/help。用户唯一进 plan 途径是 `cfg.Agent.AutoPlan` 触发。进了之后只能照抄 ID 回 `/approve <id>`，无法退出。

### M4 · plan mode 下 bash 全部被拦（设计代价，非 bug）✅
**位置**：`internal/tool/builtin/bash.go:124`（`ReadOnly()=false`）+ `agent.go:1415`
**事实**：plan mode 下连 `ls`/`cat`/`echo`（真正只读命令）的 bash 也被拦。设计上无漏洞（不会到达 mv/rm/>），但代价是 plan 阶段失去 bash 合法读取能力（必须用 grep/read_file）。

### M5 · SubagentSpec 不传 planMode（潜在风险）✅
**位置**：`internal/agent/subagent_store.go:48-59` + `boot.go:903-913`
**事实**：SubagentSpec 无 planMode 字段，skillRunner 不传。当前无害（因 H2，runner 在 plan mode 下到不了），但若将来放开 plan mode 下 subagent，subagent 不继承只读约束。

### M6 · Skill 结构体缺 Hidden 字段 ✅
**位置**：`internal/skill/skill.go:53-78`
**事实**：`Disabled`/`Cold` 都是"留在索引让模型知道存在"，没有"从索引隐藏"语义。compose 隔离 skill 需新增 `Hidden bool`。

### M7 · boot 与 control 对 auto_plan 归一化语义漂移 ✅
**位置**：`boot.go:1097` vs `control/auto_plan.go:22-31`
**事实**：boot 用 `!EqualFold(TrimSpace(...),"off")`（任何非 off 值视为启用），control 用 `normalizeAutoPlan`（空串/未知值→off）。写 `auto_plan="true"` 会建分类器但永不调用。实际危害小（默认 off 不触发）。

### M8 · 前后端 AgentView 契约裂开（plannerMaxSteps）✅
**位置**：`desktop/frontend/src/lib/types.ts:843` + `desktop/settings_app.go:74-79`
**事实**：前端 AgentView 仍声明 `plannerMaxSteps: number`（必填），Go 后端已无该字段。bridge.ts:920 默认值 12 仍写死，SettingsPanel.tsx:1959 传死 0。

---

## 第三部分 · 11 处死代码（🟢 可清理）

| # | 位置 | 问题 | 复核 |
|---|------|------|------|
| D1 | agent.go:87-93 + 61,66 + 72-73 + 1464 | **PlanModeFromContext 死代码链**：导出但全仓零代码调用（grep 仅命中注释）。连带 callContext.planMode、withCallContext 参数、L1464 传值 | ✅ grep 确认零调用 |
| D2 | task.go:376-398 | **FilterReadOnlyRegistry**：定义无调用者（grep 仅命中注释）。看意图是为 subagent 限定只读（解决 M5），但未接线 | ✅ grep 确认零调用 |
| D3 | controller.go:3071 | **PlanTodosJSON** 导出但零外部调用者，注释声称 TUI 用（文档不符） | ✅ grep 确认零外部调用 |
| D4 | cli/acp.go:86,99,114 | acpBuiltinTools/acpTaskProfileDefaults/newACPSubagentProviderResolver 仅被各自测试引用，生产走 boot.Build | ✅ |
| D5 | permission.go:509-511 | rememberRule 仅 wrap RememberRuleForScope，无调用方 | ✅ |
| D6 | useController.ts:797 + App.tsx:1129/1139/2415 + Composer.tsx:1459 | **setControllerMode→applyMode→Composer.onSetMode 整条死链**：Composer 内 `void onSetMode` 永不调用 | ✅ |
| D7 | desktop/app.go 多处 | SetMode/SetPlanMode/SetGoal/SetAutoApproveTools/SetBypass/SetToolApprovalMode/SetCollaborationMode 不带 ForTab 的入口前端零调用（全用 *ForTab） | ✅ |
| D8 | bridge.ts:130 与 238 | **SetFastTaskModel 在 AppBindings 接口重复声明两次** | ✅ |
| D9 | chat_tui.go:3286-3287 | `case planApprovalTool:` 注释自承 "No longer a tool"，防御性死代码 | ✅ |
| D10 | input.go:199 | `ReplaceAll(goal, activeGoalClose, "【/目标】")`：activeGoalClose 就是 "【/目标】"，把字符串替换成自身，no-op | ✅ 源码确认 activeGoalClose 值 |
| D11 | bridge.ts:131,1717 | SetPlanMode 前端接口+mock 保留但前端全程无调用 | ✅ |

---

## 第四部分 · 改名残留 / 跨包重复 / 注释过时（🟢）

| # | 位置 | 问题 | 复核 |
|---|------|------|------|
| R1 | chat_tui.go:2085 vs controller.go:491 | **planApprovalTool = "exit_plan_mode" 跨包重复定义**：都是 unexported，靠注释维系，无编译期保证 | ✅ grep 确认两处 |
| R2 | controller.go:491 | `exit_plan_mode` 字符串值是旧工具名化石（注释 "no special tool"），改名破坏前端契约 | ✅ |
| R3 | 前端 13 处 | `"exit_plan_mode"` 前端硬编码（ApprovalModal/Transcript 8 处/App.tsx/bridge/tools.ts），无统一常量 | ✅ |
| R4 | 前端约 13 处 | `"plan"` 散布无枚举收口 | ✅ |
| R5 | input.go:18 末尾 | PlanModeMarker 字符串结尾孤立 `]`（无配对 `[`）历史残留 | ✅ |
| R6 | input.go:193-195 | **ComposeSynthetic no-op 但被 controller.go:657 实际调用**——潜在 bug：plan-approved 后续轮绕过 reasoning-language | ✅ 源码确认 return text |
| R7 | chat_tui.go:3045 注释 | cycleMode 是 plan↔normal 二态（注释准确），常被误述为"三态循环" | ✅ |
| R8 | tabs.go:1238 | topicTitleFromText 检查英文 `[Plan mode`/`Plan mode`，但 marker 已统一中文，英文分支永不命中 | ✅ |
| R9 | boot.go:4,123 + cli.go:732 | 注释提到 "two-model Coordinator" / "planner_model"，字段和类型都已删 | ✅ 源码确认 |
| R10 | boot.go:1030 | 注释 "3B-ACT MoE" 与实际默认 qwen3.6-35b（35B）不符 | ✅ 源码确认 |

---

## 第五部分 · 文档债（30 条，高危认知陷阱）✅

**最危险 3 处**（重构者若信文档会找不存在的类型/字段）：
1. **README.md:47 / README_en.md:46**："双模型协作引擎（规划器+执行器）"——核心卖点为虚构功能
2. **GUIDE.md:231-269 / GUIDE.zh-CN.md:208-239**：完整 planner_model/planner_max_steps 配置教程，用户照抄得未知字段被静默忽略
3. **SPEC.md:112-118（§3.5）**：整节 Coordinator 工程契约，最权威文档描述已删架构

### 完整文档债清单

| 文件 | 行号 | 类型 |
|------|------|------|
| README.md | 47, 190 | 双模型（核心卖点虚构）|
| README_en.md | 46, 189 | 双模型 |
| GUIDE.md | 20, 29, 37, 38, 231-269 | 双模型 + planner_model 教程 |
| GUIDE.zh-CN.md | 20, 28, 36, 37, 208-239 | 双模型 + planner_model 教程 |
| SPEC.md | 33, 110, 112-118, 114, 116, 143, 238, 240 | 双模型 + 已删工具(ls/glob) + planner_model |
| hiq.md | 51 | "协调器"残留 |
| CONTRIBUTING.md | 122 | `feat(glob):` 示例但 glob 已删 |
| serve/index.html | 764, 769 | multi_edit/notebook_edit/delete_range/delete_symbol 已删工具 |

---

## 第六部分 · Plan Mode 重构可行性（逐行核查结论）

### Plan Mode 代码全景（活跃，非死代码）

**controller.go**：planMode 字段(L125)、审批 gate(L626-661)、SetPlanMode(L1487)、SetMode(L2845)、seedPlanTodos(L3040)、completePlanTodos(L3052)、parsePlanTodos(L3108)、listItem(L3132)、planApprovalTool(L491)、planApprovedMessage(L495)、approvedPlanAutoApproveTools(L147-152)

**agent.go**：planMode atomic.Bool(L183)、SetPlanMode(L304)、executeOne 拦截门(L1415，在 Gate 之前)、finalReadinessCheck L809 跳过 todo 校验、evidence.Reset 每 Run(L630)

**input.go**：PlanModeMarker(L18，三重身份：注入前缀/strip 标记/测试基准)、Compose(L121)、StripComposePrefixes(L39)

**auto_plan.go**：maybeAutoPlan(L33)、shouldAutoPlan(L40)、autoPlanScore(L69)、isLowRiskQuestion(L105)、三张词表(L126-142)

### 4 个硬伤（compose 增量叠加必须处理）

| 硬伤 | 位置 | 处理 |
|------|------|------|
| 1. seedPlanTodos 返回 JSON 字符串非结构化 | controller.go:3040 | compose 自己 Unmarshal |
| 2. evidence per-Run Reset（不持久化） | agent.go:630, evidence.go:58 | compose runner 自维护跨轮状态，verify 靠 transcript 兜底（verifyCommandFromSession）|
| 3. approvedPlanAutoApproveTools defer 复位 | controller.go:650,652-656 | compose runner 持有自己的审批状态 |
| 4. finalReadinessCheck 与 planMode 耦合 | agent.go:809 | compose 内 runner.Run 保证每轮 todo 闭环 |

**好消息**：executeOne 拦截门(L1415) 与 finalReadinessCheck(L809) 不必绑定——功能独立可分离。`continue_from`（task.go:138）确实撑 review→fix 循环。`MatchStep` 的 parseStepIndex 只 Atoi（evidence.go:758）——树形 T1.1 编号会断。

### 增量方案接缝（确认可行）

`controller.go:657` 前（runner.Run 之前）插入分流：seedPlanTodos 返回的 JSON Unmarshal 读 task 数量，≥3 走 compose 循环，<3 走现状单次执行。**不动 Plan Mode 任何现有代码**。

---

## 第七部分 · compose 移植设计（MiMo-Code 逐行读后）

### 关键真相
1. **compose.js 是确定性 JS 脚本**（QuickJS 沙箱），调用 host 钩子 `agent()/phase()/parallel()`，**不是 prompt 驱动**。hiq 必须用 Go 写确定性编排器。
2. **provider 层不支持 schema**（provider.Request 无 ResponseFormat）—— compose 结构化输出契约无法按原样移植。推荐 v1 用 prompt + StructuredOutput 工具模拟。
3. **worktree 是并行隔离硬依赖**——不做则批次串行（对 1-3 任务常见 feature 无感）。

### compose 指导 skill 的 5 个提升
编排器强制调 skill（取代自由选）/ schema 约束 / 证据门+重试 / skill 隔离 / 阶段间状态传递。

### MVK 优先级
- **P0**：Go 编排器 → verify 阶段 → TDD 重试外环 → 结构化 Design
- **P1**：review+fix 循环 → 移植 plan/tdd/verify/debug skill
- **P2**：report → merge

---

## 第八部分 · 建议处理顺序

### 立即清理（零风险，1-2 小时）
- D1-D11 死代码（11 处）
- R5/R8/R9/R10 注释残留
- 文档债 30 条（README/GUIDE/SPEC 双模型描述）

### 修真 bug（中风险，半天）
- H1 serve switchModel 漏 EnableInteractiveApproval（一行修复）
- H4 TUI 双源同步（删 m.planMode 改读 ctrl.PlanMode()）
- H5 handleTabClose 漏删 map
- H6 pendingPlanRevision per-tab 化
- M1 handleSend 用 helper
- R6 ComposeSynthetic 补 reasoning-lang

### 设计决策（需讨论）
- H2+H3 plan mode 下 skill/bash 可用性：是否放开 explore/research 的 ReadOnly？怎么放开？
- M3 bot 手动 plan 入口
- M6 Skill.Hidden 字段（compose 需要）
- M5 SubagentSpec planMode 传递

### compose 编排器（大工程，2-4 周）
按 MVK 优先级，接缝在 controller.go:657。

---

## 附录 · 已明确不做

- ❌ 重构 Plan Mode 为 compose.Design（~30 文件，4 硬伤，成本远超收益）
- ❌ worktree（v0.3.7 决策，无并行执行阶段）
- ❌ 双模型 planner-executor（v0.3.9 决策，CHANGELOG 自认删除）
- ❌ 并行 subagent（task 串行化避 write race）
- ❌ memory FTS5 加回（v0.3.8 决策，除非重新论证）
- ❌ 重造 permission deny-rule（permission.go:108 已实现；且 bash 参数级只读判定 deny-rule 表达不了，这是 plan mode 用独立布尔门的根本原因）
- ❌ 用 prompt 引导模型走 compose 7 阶段（必须确定性 Go 编排器）
