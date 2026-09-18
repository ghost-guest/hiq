# Hiq 全量提升方案

> 输入：三轮对标调研（vs Codex CLI / vs Pi，见 `hiq_vs_codex_pi_upgrade_plan.md` 第一~九节）＋ 两份前档（`hiq_vs_pi_remaining_gaps.md`、`fix_tool_visibility_plan.md`）。
> 输出：本档——可执行的工程实施方案，含全量问题总账、分阶段任务、里程碑、依赖与风险。
> 日期：2026-08-21。主轴：编码体验；协同：办公（cowork）/运维（netdev）三界面同步受益。

---

## 一、目标与范围

1. **补齐断链**：后端已有、前端丢失的能力全部接通（这类问题最多且最廉价）。
2. **展示层对齐**：工具卡体系、diff 渲染、进度与队列可视化达到 codex/pi 水位。
3. **循环层补课**：并发、校验、流式预览、cowork/netdev 工具适配。
4. **会话与模型工程**：搜索、prompt cache 主动化、结构化输出、事件架构。
5. **三界面协同**：利用单管道架构，一份工作三界面生效；卡点单列增补任务。

不在范围内：模型接入广度（18 厂商预设已领先）、产品线功能扩展（cowork/netdev 面板本身）、沙箱与权限模型重构（已领先）。

---

## 二、问题总账

严重度：**S0** 断链/信任（必修）｜**S1** 用户感知主战场｜**S2** 工程竞争力｜**S3** 按需生态。「归属」列为任务编号（第四节）。

### A. 断链与死代码（S0）

| # | 问题 | 证据 | 归属 |
|---|------|------|------|
| A1 | FileDiff 后端算好、wire 层丢弃 | `agent.go:1358` 生成 → `desktop/wire.go:60 wireTool` 无此字段（`toWire` :165 未填充；`tabs.go:441 toWireTab` 仅是包装层）→ `types.ts WireTool` 无 `fileDiff` | 0-1 |
| A2 | apply_patch 全链路无 diff（无 Previewer，前端 `diffsFor()` 也不认） | `apply_patch.go` 无 `Preview`；`tools.ts:60` | 0-2 |
| A3 | 审批盲签：Approval 只有 Subject 一行 | `event.go:169`（仅 ID/Tool/Subject）；`ApprovalModal.tsx:152` | 0-4 |
| A4 | Pricing 字段有、GUI 零金额显示 | `event.go:263`（挂在 Usage 事件上，wire 侧应加 `wireUsage` 而非 wireTool）；成本只在 CLI `textsink.go:210` 与 `serve/wire.go:188` | 0-6 |
| A5 | `summarize()` 的 `+N -N` 摘要未接入 ToolCard | `tools.ts:134`，仅 compact 路径使用 | 0-3 |
| A6 | ⌘K 快捷键注册是空实现 stub | `keyboardShortcuts.ts` `useGlobalShortcut` = `useEffect(() => {}, [])` | 0-7 |
| A7 | RPM `BudgetStatusView` 死类型（Go 侧无对应结构、无组件消费） | `types.ts:1613`；后端 `budget.go:136` 完整 | 0-8 |
| A8 | 三个孤立组件：`StatusBar.tsx`（0.1.9 底栏退役遗留）、`AutomationPanel.tsx`、`ExpertCollabCard.tsx`（仅注释引用，零 import） | grep 验证 | 0-10 |
| A9 | reasoning 正文纯文本、无 markdown | `Message.tsx:347-391` | 0-5 |

### B. 编码内容显示（S1）

| # | 问题 | 证据 | 归属 |
|---|------|------|------|
| B1 | 单一通用 ToolCard 渲染全部 40+ 工具（vs codex ~20 种专用 cell） | `ToolCard.tsx`（250 行） | 1-1 |
| B2 | diff 无词级 intra-line、无文件头统计、side-by-side 从 args 现算 | `CodeMirrorDiff.tsx`、`tools.ts:60 diffsFor` | 1-2 |
| B3 | 无只读调用分组降噪（codex Exploring/Explored） | `Transcript.tsx:882-896` 仅计数 | 1-3 |
| B4 | 无 turn 级改动汇总/一键 review（codex TurnDiff + /diff） | — | 1-4 |
| B5 | bash 输出 10 行预览（codex 头 5+尾 5+计数，全量进查看器） | `ToolCard.tsx:216-226` | 1-5 |
| B6 | web_search 无专用结果卡 | `tools.ts` 零特判 | 1-6 |
| B7 | 失败 turn 无手动重试按钮（只有自动重试提示） | `useController.ts:627-637` notice 卡无 action | 1-7 |
| B8 | cowork/netdev 工具零专用渲染：browser_* 15 个、screen_* 6 个、doc/csv/xlsx 9 个、email 3 个、netdev 9+2 个 | `subjectOf` 只认 7 个工具名 | 1-8 |
| B9 | netdev 备份 diff 前端手写 pre 着色（后端已复用 `internal/diff`） | `NetDevLayout.tsx:959-965` vs `backup.go:179-189` | 1-9 |

### C. 进度与队列（S1）

| # | 问题 | 证据 | 归属 |
|---|------|------|------|
| C1 | TodoPanel 无进度条/百分比/步骤耗时；complete_step 证据不可见 | `TodoPanel.tsx`（73 行） | 2-1 |
| C2 | 状态行缺语义（无"正在执行哪个工具/第几个"、无 reasoning 标题提取） | `Composer.tsx:1637-1649` 轮换动词 | 2-2 |
| C3 | steer 队列存在但 UI 不可见（只发一条 notice） | `agent.go:230`；`useController.ts:616` | 2-3 |
| C4 | 无 follow-up 队列：agent 忙时连发多条全部转为 mid-turn steer | `App.tsx handleSend` runningRef→steer | 2-4 |
| C5 | compaction 进行中缺状态行联动（transcript 卡有） | `compact.go:100` | 2-5 |
| C6 | 证据链名单只枚举编码工具；netdev_exec 命令证据不匹配 | `internal/evidence/evidence.go:570`（isWriterTool/isReaderTool 名单，**注意包路径：internal/evidence 非 internal/agent**）；`HasSuccessfulCommand` 只认 bash | 2-6 |

### D. 循环与工具（S2）

| # | 问题 | 证据 | 归属 |
|---|------|------|------|
| D1 | 写工具全局串行（不同文件也要排队） | `agent.go:1441 parallelisable` | 3-1 |
| D2 | 无集中参数校验 | `internal/tool/` 无 validate | 3-2 |
| D3 | 无流式补丁预览（codex：模型边写参数 UI 边显示 diff） | 无 ToolArgsDelta 类事件 | 3-3 |
| D4 | 终端非 PTY（v1 已留 ConPTY/xterm.js 演进位） | `TerminalPanel.tsx` | 3-4 |
| D5 | cowork 布局未接收 terminalNode（无终端面板） | `App.tsx:3365` 只传 NetDevLayout | 3-5 |
| D6 | rewind 前只有 filesChanged 文件计数、无 diff 预览 | `Message.tsx:237-241` | 3-6 |
| D7 | MCP：进度通知被显式丢弃、无 elicitation、无 OAuth | `transport_stdio.go:417`；`plugin.go:86` | 3-7 |
| D8 | cowork 写工具无 Previewer → 无 FileDiff、**不进 checkpoint（rewind 恢复不了办公产物）**、进不了队列 | Previewer 仅 6 个编码工具；`checkpoint.go:8-10` | 3-8 |
| D9 | netdev Proposal 审批用原生 `confirm()` 弹窗 | `ProposalCenter.tsx:38-66` | 3-9 |
| D10 | 子代理 headlessGate 对 Ask 级自动放行，与主循环审批语义不一致 | `permission.go:549-566`；`boot.go:1166` | 3-10 |

### E. 会话与模型工程（S2）

| # | 问题 | 证据 | 归属 |
|---|------|------|------|
| E1 | 跨 session 全文搜索缺失（只有标题/80 字符预览前端过滤） | `HistoryPanel.tsx:137-146` | 4-5 |
| E2 | prompt cache 无主动管理（无 cache_control/prompt_cache_key/预热） | `internal/provider` grep 零命中 | 4-6 |
| E3 | 主 agent 无结构化输出（仅 RAG 抽取用 json_object） | `provider` 无 response_format | 4-7 |
| E4 | 事件模型 18 种 Kind，无 item/delta 分层（codex ThreadItem ~60 变体） | `event.go` | 4-1 |
| E5 | UI 恢复靠 present sidecar 全量重放（100 turn 上限） | `internal/present/record.go` | 4-2 |
| E6 | 会话树/Fork 数据已有、无树导航 UI | `checkpoint`、`Fork(turn)` | 4-3 |
| E7 | 无结构化观测性（pi telemetry 契约式） | — | 4-4 |
| E8 | 桌面无输入历史（TUI 有现成实现） | `chat_tui.go:63` vs `Composer.tsx` | 0-9 |
| E9 | 无 429/RPM 倒计时显示 | — | 0-8 |
| E10 | 崩溃后正在跑的 turn 不续跑 | 无 recover 机制 | 5-2 |

### F. 生态（S3，按需）

| # | 问题 | 归属 |
|---|------|------|
| F1 | 跨 session/按天/按模型成本聚合 + cache re-billed 损失估算 | 5-1 |
| F2 | 本地模型接入（Ollama/llama.cpp 预设 + 加载管理） | 5-3 |
| F3 | 行号跳外部编辑器（`vscode://file/:line`） | 5-4 |
| F4 | 会话 HTML 导出/分享 | 5-5 |
| F5 | 内置图片生成工具（附件渲染管线已就绪，`event.go:149` 注释即以此为例） | 5-6 |
| F6 | 键位自定义（ShortcutsCheatsheet 是占位 stub） | backlog |
| F7 | deferred tool calls（三家生态都未成熟） | backlog |
| F8 | TTS 朗读（语音输入已有） | backlog |
| F9 | GUI git commit/PR | backlog |
| F10 | resume 时 transcript 预览增强 | backlog |

---

## 三、不动项（领先能力，回归保护清单）

改造期间这些是差异化优势，任何阶段不得退化，且需在对应里程碑跑回归：

1. Checkpoint 快照 + Rewind/Fork（两家参照物均无）；
2. `cache_shape` PrefixShape 诊断（cache miss 原因归因，独有；4-6 的收益验证工具）；
3. 4 级权限 + plan 模式 HardDeny + netdev 三道安全底线（`builtinFloor`）；
4. storm breaker / op_gate / 重复成功守卫 / 截断安全 / 中流重连（断流时**保留含 Final Answer 的部分内容**再注入恢复消息重试，`agent.go:765-777`，最多 3 次）/ RPM 后端；
5. 证据链 todo 机制（2-6 只扩名单不改语义）；
6. 保存完整性三重保障（tmp+rename + HMAC `.sig` + torn-tail 修复）——三家最强；
7. 子代理实时嵌套渲染、多 tab 状态保活、`@past:chats`、E-Stop、四格式导出、语音输入；
8. 三 profile 单管道架构本身（本方案的杠杆来源）。

---

## 四、分阶段实施方案

> 量级为净增/改动行数量级；「覆盖」= profile 受益情况（自动=单管道共享）。验收标准是可勾选的 DoD。

### 阶段 0：断链与死代码清零（1–2 天，风险全部低）

| # | 任务 | 文件 | 量 | 验收 | 覆盖 |
|---|------|------|----|------|------|
| 0-1 | FileDiff 通路：`wireTool`（`wire.go:60`）加 `{diff,added,removed}` 字段、`toWire`（:165）填充（`tabs.go toWireTab` 是包装层自动透传）；`WireTool` 加字段；ToolCard 优先服务端 diff、`diffsFor()` 降级兜底（历史 session 重放无 FileDiff，兜底必须保留）。**实现注记**：`executeBatch:1357`（事件用）与 `executeOne:1632`（checkpoint 用）目前各算一次 Preview——应合并为单次计算两处复用，兼得外部规格书提出的性能点 | `wire.go`、`tabs.go`、`types.ts`、`ToolCard.tsx` | ~30 | edit/write/multi_edit 卡片显示服务端 diff；重放旧会话仍正常 | 三界面自动 |
| 0-2 | apply_patch 实现 `Previewer`（parsePatch → 内存 apply → `diff.Build` 合并 Change） | `apply_patch.go` | ~40 | apply_patch dispatch 卡与审批弹窗有完整多文件 diff | dev（+cowork 子代理） |
| 0-3 | ToolCard 头部接入 `summarize()` 的 `+N -N / N lines` | `ToolCard.tsx`、`tools.ts:134` | ~10 | 编辑类卡片头带统计 | 三界面自动 |
| 0-4 | 审批带 diff：`event.Approval` 加 `Diff` 字段；`requestApproval` 填充该工具 PreviewChange 结果；`wireApproval`（`wire.go:110`）同步加字段；ApprovalModal 内嵌 DiffView | `event.go:169`、`controller.go`、`wire.go`、`ApprovalModal.tsx` | ~50 | Ask 模式下写工具审批可看到将要发生的改动（三 profile 工具审批同卡） | 三界面自动 |
| 0-5 | reasoning 正文用 `Markdown.tsx` 渲染 | `Message.tsx:347-391` | ~5 | thinking 块内 markdown/代码块正常渲染 | 三界面自动 |
| 0-6 | 成本金额：`wireUsage`（`wire.go:86`）加 Pricing 字段；UsageChip 悬停详情 + Composer 参数行渲染（宿主修正：StatusBar 已退役） | `wire.go`、`UsageChip.tsx` | ~20 | 悬停可见本 turn/累计金额 | 三界面自动 |
| 0-7 | 实现 `useGlobalShortcut`（window keydown 匹配 combo）或 App 级直接注册，打通 ⌘K | `keyboardShortcuts.ts`、`App.tsx:2682` | ~20 | Ctrl/Cmd+K 唤起 CommandPalette | 三界面自动 |
| 0-8 | 激活 RPM：Go 侧 `BudgetStatus()` 绑定（读 `RequestBudget.Status`）→ wire → UsageChip 消费；Retrying 事件带 Retry-After 秒数做倒计时 | `desktop/` 新绑定、`UsageChip.tsx`、`retry.go` | ~60 | 参数行可见 rpm used/remaining；429 时显示倒计时 | 三界面自动 |
| 0-9 | 桌面输入历史：上箭头翻已发送输入（移植 TUI `submittedInputs` 逻辑；`composerHistory.ts` 已按 profile 分储） | `Composer.tsx` | ~40 | ↑/↓ 翻历史、在途草稿保留 | 三界面自动 |
| 0-10 | 孤立组件处置：`StatusBar.tsx` 删除（git 留档）；`AutomationPanel`/`ExpertCollabCard` 要么补挂载点（CoWorkLayout 侧栏/Transcript）要么删除 | `components/` | 删/接 | 仓库无未挂载组件；`pnpm build` 无 dead export 告警 | — |

### 阶段 1：编码内容显示重构（1–2 周，主战场）

| # | 任务 | 说明 | 量/风险 | 验收 | 覆盖 |
|---|------|------|---------|------|------|
| 1-1 | 专用工具卡注册表 | `registry: Record<toolName, Renderer>`；第一步只做注册表+GenericCard 等价替换（纯重构零行为变化），再逐卡替换。已有专用：bash（shell 面板）、task（嵌套子代理） | ~300 / 中（分两步降险） | 全部工具经注册表渲染；行为回归通过 | 三界面自动（机制） |
| 1-2 | DiffView v2 | 统一 diff 模式（与 side-by-side 可切换）、行号 gutter、highlight.js 词法高亮、**词级 intra-line diff**（仅 1删1增，pi `diffWords` 模式）、文件头 `path (+a -r)`、hunk 折叠；数据源统一为 0-1 服务端 fileDiff | ~400 / 中 | 编辑卡/审批/rewind（3-6）/备份（1-9）共用同一组件 | dev 为主，cowork 经 writer 卡 |
| 1-3 | Exploring 分组 | 连续 quiet 只读调用合并为 `Read a.go, b.go · Searched "foo"` 一行（上限 32，codex 模式），点击展开 | ~120 | 长探索 turn 的卡片数显著下降 | 三界面自动（read/grep 同名） |
| 1-4 | Turn 改动汇总卡 | TurnDone 后插 `Edited N files (+X -Y)` 卡（前端按 fileDiff 累计），点击进入全量 diff 审查视图（与 WorkspacePanel 联动；顺带放开 cowork 的 changed 页签 gate `App.tsx:3241`） | ~150 | 每轮末尾可一键审查全部改动 | dev+cowork |
| 1-5 | bash 输出头尾折叠 | 头 5+尾 5+`… +N lines`；"show all" 进独立滚动查看器（保留 running 吸底） | ~60 | 超长输出不再撑爆卡片 | 三界面自动 |
| 1-6 | web_search 专用卡 | 标题/URL/摘要列表 + 折叠计数（codex WebSearchCell 模式） | ~80 | 搜索结果可扫读、可点开 | 三界面自动 |
| 1-7 | 手动重试 | turn 级失败 notice 卡加"重试"action（重发最后一条用户消息）；abort 保留 partial（pi 模式） | ~50 | 失败后一键重发 | 三界面自动 |
| 1-8 | cowork/netdev 专用卡批次 | BrowserCard（截图+动作时间线）、DocCard（文本 diff / 二进制摘要）、EmailCard（收件域+主题）、NetdevExecCard（设备+命令+输出折叠）、ScheduleCard | ~400 | 办公/运维工具不再落入通用 JSON 卡 | cowork/netdev |
| 1-9 | 备份 diff 统一 | NetDevLayout 手写 pre 替换为 DiffView v2（后端已复用 `internal/diff`，仅前端替换） | ~30 | 备份对比视图与编码 diff 同观感 | netdev |

### 阶段 2：进度与队列（~1 周）

| # | 任务 | 说明 | 量/风险 | 验收 | 覆盖 |
|---|------|------|---------|------|------|
| 2-1 | TodoPanel v2 | 进度条（done/total%）、每步耗时、complete_step 证据徽章（点开看证据调用）、与当前 turn 关联高亮 | ~200 / 低 | 三界面 todo 均可驱动且可核证 | 三界面自动 |
| 2-2 | 状态行语义化 | 后端 Phase 事件带工具名+序号（`Running edit_file (3/5)`）；流式 reasoning 首个 `**bold**` 提取为状态标题（codex 模式） | ~80 | 状态行始终回答"正在干什么" | 三界面自动 |
| 2-3 | steer 队列可视化 | Composer 上方列出已排队 steer，可取回编辑/删除（pi `updatePendingMessagesDisplay` 模式） | ~100 | 排队消息可见可控 | 三界面自动 |
| 2-4 | follow-up 队列语义 | `Agent` 增加 `FollowUp`（agent 空闲后作为独立新 turn 执行）；Composer Alt+Enter 显式排队；与 2-3 共用队列组件；busy 时默认行为改为"插入队尾"而非全转 steer | ~150 / 中（改 agent 生命周期，需测试 pause/resume 交互） | 连发多条各自成 turn，顺序执行 | 三界面自动 |
| 2-5 | compaction 运行态 | CompactionStarted→Done 状态行联动指示 | ~30 | 压缩期间 UI 有明确反馈 | 三界面自动 |
| 2-6 | 证据链名单扩展 | `internal/evidence/evidence.go:570` 的 isWriterTool/isReaderTool 加 doc_write/csv_write/xlsx_write/mindmap_create；`HasSuccessfulCommand` 加 netdev_exec 匹配 | ~40 | cowork/netdev 的 todo 能正常凑齐证据完成 | cowork/netdev |

### 阶段 3：循环与三界面补课（1–2 周）

| # | 任务 | 说明 | 量/风险 | 验收 | 覆盖 |
|---|------|------|---------|------|------|
| 3-1 | per-file mutation queue | 按 canonicalPath 互斥、不同文件并行；complete_step/todo_write 保持串行 | ~30 / 中（并发回归） | 同 turn 多文件编辑不再排队；同文件并发编辑安全 | dev+cowork |
| 3-2 | 参数集中校验 | `internal/tool/validate.go`：schema required 字段统一前置校验 | ~60 / 低 | 缺参工具统一报错格式 | 三界面自动 |
| 3-3 | 流式补丁预览 | apply_patch 参数增量解析（流式 parsePatch 逐行产出 hunk）+ 新事件 Kind（如 `ToolArgsDelta`）+ 前端 500ms 节流渲染。**注意**：新增 Kind 需检查全部 Sink 实现（TUI/serve/bot/ACP）的默认行为 | ~200 / 中 | 模型写补丁时 UI 实时滚动显示 diff | dev |
| 3-4 | TerminalPanel v2（PTY） | ConPTY + xterm.js 交互式终端；对齐 codex unified exec（长驻会话、stdin 写入）。**大项，可独立排期** | 大 / 高 | 可跑交互式命令（vim/ssh） | dev+netdev |
| 3-5 | cowork 接入终端 | `App.tsx:3365` 补传 terminalNode 给 CoWorkLayout | ~5 | 办公界面 Ctrl+` 可用 | cowork |
| 3-6 | rewind 前 diff 预览 | checkpoint 已有文件快照；`filesChanged` 计数升级为可展开逐文件 diff（复用 1-2） | ~80 | 回滚前可审查将被还原的改动 | dev+cowork（3-8 之后） |
| 3-7 | MCP 深度补课 | ① 进度通知透传（去掉 `transport_stdio.go:417` 的 drop → ToolProgress）；② elicitation → 复用 Approval 请求 UI；③ OAuth PKCE（本地回调 + token 存储）。分三步独立交付 | ~300 / 中 | 长 task MCP 工具有进度；elicitation 服务器可走通；OAuth 服务器免手配 token | dev+cowork（netdev 受 allowlist 限制） |
| 3-8 | cowork Previewer 适配 | doc_write/csv_write/mindmap_create 文本类（.md/.csv/.json）实现 Previewer；.docx/.xlsx 走 `diff.Change.Binary` 摘要卡。**一举三得**：FileDiff + checkpoint 快照 + mutation queue 同时打通 | ~120 / 低 | 办公产物有 diff、rewind 可恢复、审批可预览 | cowork |
| 3-9 | Proposal 审批升级 | 原生 `confirm()` → 应用内审批组件；带提案步骤 + 回滚命令预览（复用 proposal 落盘 JSON 结构） | ~150 | 运维提案全程应用内完成 | netdev |
| 3-10 | 审批语义统一 | headlessGate 写操作上报主 gate（或最低限度：UI 标注"由子代理执行"）；做成配置项避免破坏现有 skill 流程 | ~80 / 中 | 同一工具主/子代理审批策略一致或明确标注 | cowork |
| 3-11 | 主循环中间件化（吸收自外部 spec Phase 3） | `Run` 循环剥离 `finalReadinessCheck`、空回答重试、截断字符串拼装等业务逻辑，改为**内置拦截器链**挂回 Agent 实例，主循环回归纯状态机。**设计约束**：不得复用 `ToolHooks`（agent.go:117，那是用户可配置 shell 钩子，用户禁用 hooks 会连带干掉内置护栏）——新建独立的内置 interceptor 分区 | ~200 / 高（行为等价重构，需全量回归） | Run 循环行数显著下降；闲聊/无 Todo 模式不再触发证据校验；`go test ./internal/agent` 全绿 | 三界面自动 |

### 阶段 4：会话与模型工程（2–4 周，条目间可并行）

| # | 任务 | 说明 | 量/风险 | 验收 | 覆盖 |
|---|------|------|---------|------|------|
| 4-1 | 事件 item 化 | 18 种 Kind 向 `item_started/item_delta/item_completed` 分层演进（codex ThreadItem 模式）；**mobile bridge / 未来 remote 的战略前置**。分两步：先加 delta 语义（3-3 已开例），再收编存量 | 大 / 高（全 Sink 适配） | 前端从"翻译事件"变为"渲染 item"；mobile 复用同一协议 | 三界面 |
| 4-2 | 快照+增量协议 | pi `SessionSnapshot` 模式替代 present sidecar 全量重放（消除 100 turn 上限） | 大 / 高 | 任意长度会话秒开；崩溃后 UI 一致 | 三界面 |
| 4-3 | 会话树导航 UI | backtrack 式树视图（Fork/branch 数据已有）；resume 加 transcript 预览 | ~300 / 中 | 可视化跳转到任意分支点 | 三界面 |
| 4-4 | 结构化观测性 | vendor-neutral 生命周期事件（pi telemetry 契约模式）；先服务内部（turn_timing、cache_shape、retry 事实） | ~150 / 低 | 可导出一次 turn 的完整生命周期时间线 | 三界面 |
| 4-5 | 会话全文搜索 | 路线 A（先做）：ripgrep 扫 session.jsonl + 元数据聚合（codex 模式，实现成本低）；路线 B（长期）：SQLite FTS5。挂到 CommandPalette 已有 fuzzy 框架 | A ~120 / 低 | 按内容搜到历史会话并跳转 | 三界面（按 profile 分区索引） |
| 4-6 | prompt cache 主动化 | Anthropic `cache_control` 断点 + OpenAI `prompt_cache_key` 透传 + 启动预热（codex warmup 模式）；**用已有 cache_shape 诊断做前后量化**（独有优势） | ~150 / 中 | 同 workload 命中率可测量提升；UsageChip 显示收益 | 三界面 |
| 4-7 | turn 级结构化输出 | provider 层 `response_format: json_schema`；先供内部消费者（LoopPanel 校验、专家团编排、netdev_propose/finding 参数），再暴露用户 | ~150 / 中 | Loop 轮次的 verify 不再靠文本解析 | 三界面 |

### 阶段 5：生态（按需）

| # | 任务 | 说明 | 量 |
|---|------|------|----|
| 5-1 | 成本聚合 | 跨 session/按天/按模型汇总（serve wire 已有 Cost 字段，缺聚合与 UI）；进阶：pi 式 cache re-billed 损失估算（与 cache_shape 联动）。0-6 的自然延伸，**建议提前** | ~200 |
| 5-2 | 崩溃 turn 恢复 | codex `recover_turn_if_idle` 模式：重开识别未完成 turn 并续跑（依赖 4-1 更自然） | 大 |
| 5-3 | 本地模型接入 | Ollama/llama.cpp provider 预设 + 模型加载管理面板（hiq 预设体系接入成本低） | ~200 |
| 5-4 | 行号跳编辑器 | diff/文件预览加"在编辑器打开"（`vscode://file/<abs>:<line>`） | ~40 |
| 5-5 | 会话 HTML 导出/分享 | 补 HTML 格式（React DOM 渲染产物已在，比 pi 的 ANSI 转换容易） | ~100 |
| 5-6 | 图片生成工具 | 附件管线已就绪，补内置工具 + 设置 provider | ~80 |

---

## 五、里程碑

| 里程碑 | 时点 | 内容 | 出口条件（DoD） |
|--------|------|------|----------------|
| **M0 断链清零** | 第 1 周 | 阶段 0 全部（0-1~0-10） | A 类 9 项问题全部关闭；`go test ./...` + 前端 build 通过；三界面抽查 |
| **M1 展示对齐** | 第 2-3 周 | 阶段 1 全部 | B 类 9 项关闭；新开 session 与重放旧 session 均正常（兜底路径验证） |
| **M2 进度可用** | 第 4 周 | 阶段 2 全部 | C 类 6 项关闭；cowork/netdev 各跑一个真实 todo 场景验证证据链 |
| **M3 循环补课** | 第 5-6 周 | 阶段 3（3-4 PTY 可滑出） | D 类关闭（3-4 除外）；并发编辑/审批回归通过 |
| **M4 工程深化** | 第 7-10 周 | 阶段 4（按价值排序：4-5 → 4-6 → 4-7 → 4-3 → 4-4；4-1/4-2 视 mobile 需求启动） | E 类按序关闭 |
| **M5 生态** | 按需 | 阶段 5（5-1 建议随 M4 做） | F 类按产品节奏 |

总量核对（自查修正，2026-08-21）：**问题 54 项**（A9 + B9 + C6 + D10 + E10 + F10）、**任务 49 个**（阶段 0×10 + 1×9 + 2×6 + 3×11 + 4×7 + 5×6；初版误记"42 个"）；其中小时级 3 个、接线级 13 个、组件级 18 个、架构级 8 个。**三方合流定稿（58 问题/51 任务，新增 2-7 Agent 仪表板、5-7 权限粒度及 4 项新问题）以 `HIQ_UPGRADE_SPEC.md` 为准。**

---

## 六、依赖与并行策略

```
0-1 FileDiff ──┬─→ 1-2 DiffView v2 ──┬─→ 1-9 备份diff / 3-6 rewind预览
               ├─→ 1-4 Turn汇总 / 1-8 专用卡
               └─→ 3-8 cowork Previewer（先有通路再扩工具）
1-1 注册表 ─→ 1-6 / 1-8（渲染器挂载点）
2-6 证据名单 ─→ 2-1（TodoPanel v2 在 cowork/netdev 才有意义）
0-8 ─→ 5-1（成本/额度同源）
4-1 item化 ─→ 3-3 更自然 / 4-2 快照 / 5-2 崩溃恢复（但 3-3 可先用独立 Kind，不必等）
```

- **并行线 A（前端）**：阶段 0 → 1 → 2 基本纯前端+少量 Go 绑定；
- **并行线 B（后端）**：3-1/3-2/3-8/2-6/4-5/4-6/4-7 可与线 A 同时推进；
- **单人节奏**：严格按 0 → 1 → 2 → 3 →（4 按价值挑）→ 5；每阶段一个 PR 批次，M0/M1 各自可独立发版。

## 七、风险清单

| 风险 | 影响 | 缓解 |
|------|------|------|
| wire 加字段 vs 历史 present 重放 | 旧会话无 FileDiff/Pricing | `diffsFor()` 兜底永久保留（0-1 验收项） |
| ToolCard 注册表重构引入回归 | 全工具渲染 | 两步走：先纯重构等价，再逐卡替换；每卡配截图对比 |
| 新增事件 Kind 波及全部 Sink（TUI/serve/bot/ACP） | 编译/运行时遗漏 | 3-3/4-1 前先盘点 Sink 实现清单，给默认 no-op |
| per-file 队列改变并发模型 | 同文件竞态 | 专项并发测试（同 turn 同文件多 edit、edit+write 混合） |
| doc_write 产物 confine 在 writeRoots，checkpoint 有 workspace 逃逸检查 | 3-8 快照可能被 safePath 拒绝 | 实现前核对 writeRoots ⊆ workspace，或给 checkpoint 扩展受信根 |
| headlessGate 统一后 skill 流程被审批打断 | cowork 现有体验回退 | 做成配置（默认仅 UI 标注），灰度后再收紧 |
| PTY（ConPTY）复杂度 | 3-4 拖期 | 独立排期，不阻塞 M3 其余项 |
| 事件 item 化范围失控 | 4-1 变成重写 | 分两步：先增量 delta 语义，存量 Kind 收编放最后 |

## 八、测试与回归

- Go：`go test ./...`（重点 `desktop/app_test.go`、`tab_profile_test.go`、`internal/agent`、`internal/tool/builtin`）；
- 并发专项：mutation queue（3-1）与 steer/follow-up（2-4）生命周期测试；
- 兜底专项：无 FileDiff 的旧 present 重放（0-1）、无 Pricing 的 provider（0-6）；
- GUI 巡检：沿用 `gui-test-screenshots/` 截图对比流程，M1/M2 各跑一轮三 profile；
- 行为评测（长期，对应 pi evals 方法）：用 e2ebench 固化"编辑→diff 展示→审批→rewind"端到端链路。

## 九、与前档关系

| 前档 | 处置 |
|------|------|
| `hiq_vs_pi_remaining_gaps.md` | P2 → 本档 0-2；P3-1 → 3-1；P3-2 → 3-2（编号收编） |
| `fix_tool_visibility_plan.md` | 已完成项并入第三节回归保护 |
| `hiq_vs_codex_pi_upgrade_plan.md` | 问题论证与证据来源；本档任务编号与其 roadmap 兼容（阶段 3 重排：旧 3-10→新 3-5，旧 3-11→新 3-10） |

---

## 十、外部方案交叉验证（Gemini spec，2026-08-21）

> **归属更正**：本节验证的对象是用户粘贴的 **Gemini** 方案（初版误标为 `hiq-codex-pi-upgrade-spec.md`）；该文件实为 **MiMo** 的前端 UX 方案（另一个视角），其交叉验证与合流见 `HIQ_UPGRADE_SPEC.md` 附录 A。以下验证结论只依赖源码事实，不受署名影响，均维持有效。

外部 spec（架构重构视角）与本档（产品能力视角）交叉核对，全部断言已回源码验证。

### 10.1 外部 spec 说对了的（本档已采纳/修正）

| 断言 | 验证结果 | 本档处置 |
|------|---------|---------|
| `desktop/wire.go:164 toWire` 丢弃 FileDiff | **属实且比本档初版更准**——`tabs.go toWireTab` 只是包装层（:441 内部调 `toWire`），真正缺字段的是 `wire.go:60 wireTool` | 0-1 文件归因已修正 |
| 全局写串行（`parallelisable` :1441） | 属实 | = 本档 3-1 |
| Run 主循环耦合业务逻辑（:702-950） | 属实（`finalReadinessCheck`、截断字符串拼装硬编码在循环内） | 新增 3-11 |
| `ToolHooks` 接口存在但未用于内部解耦 | 属实（`agent.go:117-131`，目前只承载用户 shell 钩子） | 3-11 的设计依据（但见 10.2 的风险修正） |
| 参数生成阶段黑盒、无流式预演 | 属实 | = 本档 3-3 |
| Stream Recovery"保留含 Final Answer 的部分内容再重试" | **属实且修正了本档引述旧档的过时描述**（`agent.go:765-777`：`hasVisibleFinalAnswer(text)` 时先落盘 partial 再注入恢复消息） | 不动项 #4 描述已精确化 |
| PreviewChange 同步计算有性能成本 | 属实，且实际是**双算**（executeBatch:1357 事件用 + executeOne:1632 checkpoint 用，各一次） | 并入 0-1 实现注记：合并单次计算 |

### 10.2 外部 spec 有误或需辨证的

| 断言 | 问题 |
|------|------|
| "在 wireTool 中增加 Pricing 字段" | **对象错误**：Pricing 挂在 Usage 事件上（`event.go:263`），应加到 `wireUsage`（`wire.go:86`），不是 wireTool |
| "Checkpoint 与 OpGate 体系……强制拦截越权操作的 4 级鉴权" | **概念混淆**：OpGate（op_gate.go）是同类失败预算；4 级风险权限在 permission 包（RiskRead<RiskWriteLocal<RiskExec<RiskExternal），两者是不同机制 |
| Phase 1"PreviewChange 异步化或延迟到审批阶段" | **方向半对**：性能关切成立（双算），但"延迟到审批阶段"不可行——checkpoint 的 onPreEdit（`agent.go:1629-1640`）在任何模式下都需要执行前 Preview；且 auto 模式的 UI 卡片同样需要 diff。正解是**合并双算 + 大 diff 退化为统计摘要**，不是延迟 |
| Phase 3"吃狗粮：用 ToolHooks 挂载内置中间件" | **风险**：ToolHooks 是用户可配置的 shell 钩子语义（block/message），用户禁用 hooks 会连带禁掉内置护栏（证据链校验等）。3-11 改为独立内置拦截器链 |

### 10.3 外部 spec 的盲区（本档覆盖而其未覆盖）

展示层主体（专用卡/词级 diff/分组/Turn 汇总）、进度与队列（TodoPanel v2/steer+follow-up 可视化）、交互断链（⌘K stub/RPM 死类型/输入历史）、会话工程（全文搜索/崩溃恢复）、模型工程（cache 主动化/结构化输出/成本显示）、MCP 深度（elicitation/进度/OAuth）、**三 profile 传播**（cowork Previewer/证据链名单/Proposal 审批/headlessGate 语义）、以及工程管理面（里程碑 DoD/风险清单/依赖并行）。其"展示层落伍根源 = wire 断链"的判断只对了一部分：前端本身（单一 ToolCard、无词级 diff）是另一半根源。

### 10.4 结论

两份方案互补而非互斥：外部 spec 自底向上看循环内核（4 个问题全部与本档重合且经独立验证属实，交叉验证价值高），本档自顶向下看产品能力全景。已吸收其两处更准的归因（wire.go、stream recovery）与一个真增量（3-11 主循环中间件化，带设计约束修正）；其 Phase 1/2/4 分别映射本档 0-1/3-1/3-3，无冲突。
