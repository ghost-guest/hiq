# Hiq vs Codex CLI vs Pi — 编码体验对比与全面提升规划

> 对比对象：`Swarm-OS/codex`（OpenAI Codex CLI，Rust monorepo，130+ crate）、`Swarm-OS/pi`（Pi Agent Harness，TypeScript monorepo）、本仓库。
> 聚焦用户痛点：**编码模块、进度显示、编码内容展示**，二轮补充**交互、会话工程、模型工程、MCP、生态**。
> 日期：2026-08-21（一轮）／2026-08-21 二轮增补（第七、八节）。接续 `hiq_vs_pi_remaining_gaps.md`（该档 P2/P3 并入本规划阶段 3，P1 已验证属实）。
> **工程实施方案（全量问题总账 + 里程碑 + 风险）见 `docs/HIQ_UPGRADE_PLAN.md`**，本档保留为对比论证与证据来源。

---

## 一、总体判断

**hiq 的 agent 循环底座并不落后，真正落伍的是展示层。**

- 循环层：截断安全、edit 返回 diff、只读并行、steer 队列、compaction 分级触发、checkpoint/rewind —— 这些与 codex/pi 同水位，部分（checkpoint、证据链 todo、storm breaker、4 级权限）反而是独有领先。
- 展示层：与 codex/pi 差距明显。codex 的 TUI 有约 20 种专用 history cell、2559 行的 diff 渲染器、1976 行的流式渲染控制器；pi 有 per-tool 自定义渲染钩子、词级 diff、实时编辑预演。hiq 桌面端**用一个 250 行的通用 ToolCard 渲染全部 40+ 工具**，diff 靠前端从 args 现算，且后端算好的 `FileDiff` 在传输层被丢弃。

三个最刺眼的问题（均已在源码验证）：

| # | 问题 | 证据 |
|---|------|------|
| 1 | **FileDiff 断链**：后端在 dispatch 前算好了服务端 diff，但 wire 转换丢弃 | `internal/agent/agent.go:1358` 生成 `ev.FileDiff` → `desktop/tabs.go:440 toWireTab` 无此字段 → 前端 `types.ts WireTool` 无 `fileDiff` |
| 2 | **apply_patch 全链路无 diff**：无 Previewer、前端 `diffsFor()` 也不认 | `internal/tool/builtin/apply_patch.go` 无 `Preview`（`preview.go` 仅 write/edit/multiEdit 实现）；`desktop/frontend/src/lib/tools.ts:60 diffsFor()` 返回 `[]` |
| 3 | **审批盲签**：审批弹窗只有一行 Subject，无 diff 可看 | `internal/event/event.go:169 Approval` 仅 `ID/Tool/Subject`；`ApprovalModal.tsx` 只渲染文本 |

---

## 二、逐维度对比

### 2.1 编码内容显示（差距最大的维度）

| 维度 | Codex | Pi | Hiq | 判定 |
|------|-------|-----|----------|------|
| 工具展示体系 | ~20 种专用 HistoryCell（`tui/src/history_cell/`：User/AgentMessage/Patch/Plan/Exec/McpToolCall/WebSearch/Hook…），每种独立布局 | 工具自渲染：`registerTool` 内嵌 `renderCall/renderResult`（`core/tools/edit.ts` 自绘实时 diff） | 单一通用 `ToolCard.tsx`（250 行）渲染所有工具，靠 `subjectOf()` 一行摘要区分 | **落后** |
| diff 渲染 | `tui/src/diff_render.rs`（2559 行）：统一 diff + syntect 高亮（按 hunk 保持 parser 状态）+ 行号 gutter + hunk `⋮` 分隔 + 文件头 `(+a -r)` 统计 | `components/diff.ts`：统一 diff + highlight.js + **词级 intra-line diff**（1删1增时 `diffWords` 反显变化 token） | `CodeMirrorDiff.tsx` side-by-side MergeView，**无词级 diff**；数据源是前端从 args 现算（`diffsFor()`），apply_patch 算不出 | **落后** |
| 服务端 diff 通路 | `PatchApplyBegin/Updated/End` 事件 + `TurnDiff`（`core/src/turn_diff_tracker.rs` 跨工具累计） | edit 工具返回 `{diff, patch, firstChangedLine}` 三件套 | 后端 `PreviewChange` 已生成 `FileDiff`，**wire 层丢弃**（见上表 #1） | **断链** |
| 审批 diff 预览 | `ApplyPatchApprovalRequest` 携带完整补丁，审批 overlay 内看 diff | `renderCall` 在参数流完、执行前即渲染 diff（`EditPreview`） | `ApprovalModal` 仅 Subject 文本（见上表 #3） | **落后** |
| turn 级改动汇总 | `TurnDiff` 事件 + `/diff` 命令；摘要头 `• Edited N files (+X -Y)` | 无内置 turn 汇总块 | `WorkspacePanel` 有改动文件列表（含 latestPrompt 归因，不错），但**无聚合 diff / 一键 review all** | 部分落后 |
| bash 输出 | 头 5 行 + 尾 5 行 + `… +N lines`，完整输出进 Ctrl+T transcript | 默认 10 行预览 + ctrl+o 全局展开 | 10 行预览 + "show all"，running 时自动展开吸底（`ToolCard.tsx:216`） | **基本持平** |
| 只读调用降噪 | "Exploring/Explored" 分组：连续 Read/Search 折叠为一个 cell（`exec_cell/render.rs:293`，上限 32）；完成命令合并 "Ran N commands" | 工具块按状态着色 + 10 行 fallback 折叠 | quiet 卡整体淡化，**无分组**；`summarize()`（tools.ts:134）算好的 `+N -N` 摘要未接入标准 ToolCard | 略落后 |
| 流式 markdown | 两区模型（stable/tail）+ 表格 holdback + 提交滚入动画 + 结束后按源码重渲染（`tui/src/streaming/controller.rs`，1976 行） | differential rendering 只重绘脏行区 | react-markdown + `useDeferredValue` 降频 + 光标注入（`Message.tsx`） | 可用，精度低 |
| thinking 展示 | `ReasoningSummaryCell` markdown 渲染（`**标题**` 剥离成 header）；流式时只露状态行标题 | thinking 块可折叠，assistant 组件内渲染 | reasoning 正文**纯文本 div**，无 markdown（`Message.tsx:347-391`） | **落后** |
| 成本显示 | `/usage` 菜单 + 状态行 items（5h/weekly limit、credits） | footer 显示 `$0.123 (sub)` 成本分账 | 事件有 `Pricing` 字段（`event.go:263`），**前端不渲染任何金额** | 断链 |
| 图片 | 终端内 kitty/iTerm2 协议渲染 + 粘贴 | kitty/iterm2 + 半块字符降级 | lightbox + data URL 缩略图（GUI 天然优势） | 持平 |

### 2.2 进度显示

| 维度 | Codex | Pi | Hiq | 判定 |
|------|-------|-----|----------|------|
| turn 状态 | "Working" + elapsed + 中断提示；**reasoning 首个 bold 提取为状态行标题**（`chatwidget/streaming.rs:229`） | spinner / retry 倒计时 / compaction 指示器三种状态组件 | 轮换动词 + 耗时 + `↓ N tokens`（`Composer.tsx:1637`）+ Phase chip | 有基础，缺语义 |
| todo/plan 进度 | `PlanUpdateCell` 三态 checkbox（✔/□进行中/□待办，进行中高亮） | 无内置 plan 工具（扩展可自绘 widget） | `TodoPanel` 有 done/total 计数、两级步骤、in_progress 自动滚动；**无进度条/百分比/步骤耗时**；`complete_step` 证据链是独有强项但 UI 上是 quiet 淡化卡，不可见 | 各有胜负 |
| token/上下文 | TokenCount 事件 + 可配置状态行（ContextUsed/Remaining/TotalTokens…） | footer：成本 + `42.3%/200k` 上下文（>70% warn >90% err） | `composer/UsageChip` 填充条 + 悬停详情（命中率/拆分）、`ContextPanel` 环形图 —— 完整。注意：`StatusBar.tsx` 已随 0.1.9「底栏退役」成为**未挂载的孤立文件**（三轮验证：全 src 零 import） | **领先/持平** |
| 实时编辑预览 | **模型边写补丁参数、UI 边显示 diff**：`StreamingPatchParser` 流式解析 + 500ms 节流 `PatchApplyUpdated`（`tools/handlers/apply_patch.rs:86`） | 参数流完、执行前即预演 diff | 无（dispatch 时才有一次静态 FileDiff，且被 wire 丢弃） | **落后** |
| 消息队列可视化 | thread queue 事件 | Composer 上方列 `Steering: xxx / Follow-up: xxx` + 可取回编辑 | steer 队列存在（`agent.go:230 steerQueue`）但 UI 不可见；无 follow-up 队列语义 | 落后 |

### 2.3 编码模块（循环与工具）

| 维度 | Codex | Pi | Hiq | 判定 |
|------|-------|-----|----------|------|
| 主循环 | Session→Task→Turn 模型（`docs/protocol_v1.md`），pre-sampling compact、steering 排空、skills 注入 | `Agent`（有状态）+ `runAgentLoop`（无状态双层 while），steer/followUp 双队列 + QueueMode | `Run()` 单循环 + `executeBatch` + `finalReadnessCheck`；独有 storm breaker、op_gate、重复成功守卫 | **持平，各有护栏** |
| 工具并行 | RwLock 门：支持并行的工具拿读锁并发，否则写锁互斥（`tools/parallel.rs:152`） | 默认并行 `Promise.all`，写类工具 `executionMode:"sequential"` 覆盖 + **按 canonicalPath 的 file-mutation-queue**；结果按源顺序回传保证上下文稳定 | 连续只读并行（maxParallel=8）+ **全部写工具全局串行**（`agent.go:1441 parallelisable`）——不同文件编辑也要排队 | **落后（旧档 P3-1）** |
| edit 工具返回 | apply_patch 应用后生成 unified diff 供 UI | `{diff, patch, firstChangedLine}` 三件套 | edit_file 已返回 diff（旧档 P1-1 ✅）；**apply_patch 只返回 `A/M/D path` 摘要** | 部分落后 |
| 截断安全 | — | `stopReason==="length"` 时整批 fail，不执行半截参数 | 已实现（`agent.go:805-826`，旧档 P1-2 ✅） | **持平** |
| 参数校验 | tool spec 层 | schema 声明 + loop dispatch 前校验 | 各工具自行校验，无统一层（旧档 P3-2） | 落后 |
| 补丁格式 | 自家 `*** Begin Patch` 格式 + 流式解析器 + 模糊定位（`seek_sequence.rs`） | 无补丁工具（edit 多块编辑一次调用） | 自有 `parsePatch` 格式，无流式解析、无预览（旧档 P2） | 落后 |
| 终端 | unified_exec PTY + `TerminalInteraction` 写 stdin 交互 | bash 工具 + Operations 可委托远程 | TerminalPanel 一次性命令，非 PTY（v1 注释已留 ConPTY/xterm.js 演进位） | 落后（已知债） |
| 会话持久化 | rollout JSONL + SQLite 索引 + torn 处理 + **backtrack 树回退** + resume 预览 | **append-only JSONL 树**（id/parentId 原地分支、compaction boundary、branch summary、fork=新文件）；v4 加 seq/LaneRecord/崩溃恢复；SQLite+FTS5 后端 | 全量重写 JSONL + present sidecar（UI 字段旁路，100 turn 上限）+ **checkpoint/rewind/fork（独有强项）** + trash 恢复 | 各有胜负：hiq 缺树导航 UI，但 checkpoint 领先 |
| 子代理 | multi_agents v2 + CollabAgent 事件 + SubAgentActivity | 无（pi-chat 另项目） | TaskTool 子代理事件改写转发，前端**实时嵌套渲染**（`subcallsByParent`）——做得好 | **领先/持平** |

### 2.4 架构与生态

| 维度 | Codex | Pi | Hiq | 判定 |
|------|-------|-----|----------|------|
| UI 接入协议 | **协议优先**：`app-server-protocol` 约 150 个 RPC；ThreadItem 模型 + item/agentMessage/delta 等增量通知 + ServerRequest 审批问询；**TUI 本身只是 app-server 的一个客户端** | protocol/server/client 三包（实验）：CBOR 帧 + `SessionSnapshot`（快照为准）+ `TranscriptProgress`（增量）；另有成熟 `--mode rpc`（JSONL，~30 命令，含 RpcExtensionUIRequest 把扩展 UI 对话框转发给外部前端） | Wails `EventsEmit` 直连前端；18 种事件 Kind，无 item/delta 分层；Sink 抽象使 Agent 不感知传输（好底子） | **落后**（但 hiq 有 mobilebridge 场景，协议化的收益更大） |
| 扩展系统 | plugins/marketplace、skills、hooks（十余事件）、custom prompts | TS 扩展 API（~1800 行）：40+ 生命周期事件、registerTool/Command/Shortcut/MessageRenderer | hooks 12 事件 + skill + MCP + 插件示例，路线不同 | 各有路线 |
| 沙箱 | seatbelt/landlock/bwrap/Windows 沙箱 + execpolicy | 无内置（文档引导容器化） | 4 级风险 + YOLO/Auto/Ask + plan 模式 HardDeny（权限模型本身领先） | 各有路线 |
| Provider | OpenAI 系 | ~40 provider 统一 API + OAuth 全家桶 | 18 厂商 300+ 模型预设 + RPM 限流 + 中流重连（韧性领先） | 持平 |

---

## 三、hiq 领先项（改造时保持不退化）

来自循环层与产品层，这两块不是本次改造对象：

1. **Checkpoint 快照 + Rewind/Fork**（codex/pi 均无等价物）；
2. 证据链 Todo（complete_step 需本 turn 成功回执才能标记完成，防幻觉）；
3. 4 级权限 + plan 模式 HardDeny；
4. storm breaker / op_gate / 重复成功守卫等循环护栏；
5. 子代理实时嵌套渲染、多 tab 状态保活、token 命中率/上下文仪表盘；
6. 编码感知（GBK/UTF-16/BOM）、中流重连、RPM 限流、session trash；
7. 三 profile 产品面（dev/cowork/netdev）+ 桌面自动化 + mobile bridge —— 产品宽度远超两个参照物。

---

## 四、分阶段提升路线图

> 依赖关系：阶段 0 是一切展示层改造的地基（先把服务端 diff 通路接通）；阶段 1/2 可并行；阶段 3 的 3-3 依赖事件模型扩展。

### 阶段 0：断链修复（1–2 天，纯接线，风险低）

| # | 改动 | 文件 | 量 |
|---|------|------|----|
| 0-1 | `toWireTab` 透传 `FileDiff{diff, added, removed}`；`WireTool` 加 `fileDiff` 字段；ToolCard **优先用服务端 diff**，`diffsFor()` 降级为兜底 | `desktop/tabs.go:440`、`frontend/src/lib/types.ts`、`ToolCard.tsx` | ~30 行 |
| 0-2 | apply_patch 实现 `Previewer`（解析 PatchText → 内存 apply → `diff.Build`），自动进入 0-1 通路（即旧档 P2） | `internal/tool/builtin/apply_patch.go` | ~40 行 |
| 0-3 | ToolCard 头部接入 `summarize()` 的 `+N -N / N lines` 摘要行 | `ToolCard.tsx`、`tools.ts:134` | ~10 行 |
| 0-4 | `event.Approval` 增加 `Diff string` 字段，controller 发审批请求时带上该工具的 `PreviewChange` 结果；`ApprovalModal` 内嵌 DiffView | `event.go:169`、`internal/control/controller.go:3823`、`ApprovalModal.tsx` | ~50 行 |
| 0-5 | reasoning 正文用 `Markdown.tsx` 渲染（替换纯文本 div） | `Message.tsx:347-391` | ~5 行 |
| 0-6 | 成本金额渲染（宿主：`composer/UsageChip` 悬停详情与 Composer 参数行；`StatusBar.tsx` 已退役不可用） | `UsageChip.tsx` | ~20 行 |

**验收**：任何写文件工具（含 apply_patch）在 dispatch 卡片和审批弹窗里都能看到带统计的服务端 diff；reasoning 有格式；footer 有金额。

### 阶段 1：编码内容显示重构（1–2 周，核心战场）

| # | 改动 | 说明 |
|---|------|------|
| 1-1 | **专用工具卡注册表** | `registry: Record<toolName, Renderer>`，按 codex cell 体系拆分：EditCard（内联 diff）/ PatchCard（apply_patch 多文件）/ BashCard（现有 shell 面板独立）/ ReadCard / SearchCard / GenericCard 兜底。TaskCard 嵌套子代理已达标不动 |
| 1-2 | **DiffView v2** | 在 side-by-side 之外增加**统一 diff 模式**（可切换）；行号 gutter；highlight.js 词法高亮；**词级 intra-line diff**（仅 1删1增时，参考 pi `renderIntraLineDiff`）；文件头 `path (+a -r)` + hunk 折叠。数据源统一走 0-1 的服务端 fileDiff |
| 1-3 | **Exploring 分组** | 连续 quiet 的 read_file/grep/glob 合并为一行 `Read a.go, b.go · Searched "foo"`（codex `exploring_display_lines` 模式，上限 32），点击展开 |
| 1-4 | **Turn 改动汇总卡** | 每轮 TurnDone 后插入 `Edited N files (+X -Y)` 汇总卡（后端加 TurnDiff 聚合器，或前端按 fileDiff 累计），点击进入全量 diff 审查视图（与 WorkspacePanel 联动） |
| 1-5 | bash 输出头尾折叠 | 10 行预览改为头 5 + 尾 5 + `… +N lines`，完整输出进独立查看面板 |

### 阶段 2：进度显示升级（~1 周）

| # | 改动 | 说明 |
|---|------|------|
| 2-1 | **TodoPanel v2** | 进度条（done/total%）、每步耗时、`complete_step` 证据徽章（点开看证据工具调用）、与当前 turn 关联高亮 |
| 2-2 | **状态行语义化** | 后端 Phase 事件携带"正在执行的工具名 + 序号"（`Running edit_file (3/5)`）；流式 reasoning 的首个 `**bold**` 提取为状态标题（codex 模式），正文仍进折叠块 |
| 2-3 | **steer 队列可视化** | Composer 上方列出已排队 steer 消息（pi `updatePendingMessagesDisplay` 模式），支持取回编辑/删除 |
| 2-4 | compaction 运行态 | CompactionStarted→Done 之间在状态区显示进行中指示（transcript 卡已有，补状态行联动） |

### 阶段 3：编码循环补课（1–2 周，含旧档 P3）

| # | 改动 | 文件 | 量 |
|---|------|------|----|
| 3-1 | per-file mutation queue（旧档 P3-1）：写工具按 canonicalPath 加锁，不同文件可并行；`complete_step/todo_write` 保持串行 | `agent.go executeBatch/partitionToolCalls` | ~30 行 |
| 3-2 | 参数集中校验（旧档 P3-2）：`internal/tool/validate.go` 基于 schema required 字段做统一前置校验 | 新文件 | ~60 行 |
| 3-3 | **流式补丁预览**（对标 codex 最大亮点）：`apply_patch` 参数增量解析（流式 parsePatch，逐行产出完整 hunk 即渲染）；需新增 `ToolArgsDelta` 事件 Kind，前端 500ms 节流渲染 | `apply_patch.go`、`event.go`、前端 PatchCard | ~200 行 |
| 3-4 | TerminalPanel v2：ConPTY + xterm.js，交互式命令（对标 codex unified_exec），独立排期 | `TerminalPanel.tsx` | 大项 |

> 注：pi 的"参数流完即预演 diff"在阶段 0 完成后即等价获得（hiq 的 ToolDispatch+FileDiff 本就发生在执行前）；3-3 是进一步把预览提前到**模型正在生成参数时**。

### 阶段 4：架构演进（长期，按需）

| # | 方向 | 说明 |
|---|------|------|
| 4-1 | **事件模型 item 化** | 18 种 Kind 向 codex `ThreadItem + item_started/delta/completed` 分层靠拢。收益一：前端从"翻译事件"变为"渲染 item"；收益二：**mobile bridge / 未来 remote 直接受益**（hiq 已有 mobilebridge 场景，这是比两个参照物更强的动机） |
| 4-2 | 快照+增量协议 | pi `SessionSnapshot` 模式：任意时刻可从快照+增量恢复 UI，替代现在的 present sidecar 全量重放（100 turn 上限的根因） |
| 4-3 | 会话树导航 UI | backtrack 式树视图（数据层的 Fork/branch/checkpoint 已具备，缺 UI 入口）；resume 时预览旧会话转录 |
| 4-4 | 观测性 | 参考 pi telemetry 包的 vendor-neutral span 契约，结构化生命周期事件 |

---

## 五、优先级建议

1. **立刻做阶段 0**：六项全是接线级改动（合计 ~155 行），却直接消解"审批盲签""apply_patch 黑箱""diff 前端现算"三个最影响信任感的问题。二轮又发现两个**一小时级**的 bug 型断链，并入阶段 0：⌘K 快捷键空实现（0-7）、RPM 死类型（0-8），外加桌面输入历史（0-9）。
2. **阶段 1 是主战场**：用户感知的"编码内容显示落伍"80% 来自单一 ToolCard + 无词级 diff + 无分组降噪。1-1/1-2 做完后观感即可对齐 codex/pi 的水位。
3. **阶段 2 的 2-1/2-2** 让"进度"从装饰变为信息；2-3 与二轮新增的 2-5（follow-up 队列）一起补齐交互工程短板。
4. **阶段 3 里 3-1/3-2** 是旧档欠账，量小先清；**3-3 是唯一能反超 codex 体验感知的点**（GUI 渲染 diff 比终端更从容）。
5. **阶段 4 只在 mobile/remote 需求升温时启动**，4-1 是其中唯一有战略价值的；二轮增补的 4-5 会话全文搜索、4-6 prompt cache 主动化可以提前——前者用户可感知度高，后者直接省钱。
6. **阶段 5 生态项按需**：成本聚合（5-1）建议提前，因为它是 0-6 成本显示的自然延伸。

---

## 六、与前档的关系

| 前档条目 | 本档处置 |
|----------|---------|
| `hiq_vs_pi_remaining_gaps.md` P1-1/P1-2（已完成） | 验证属实：`editfile.go:118` 返回 diff、`agent.go:805` 截断拦截 |
| 同档 P2（apply_patch 预览） | 并入本档 **0-2** |
| 同档 P3-1（file mutation queue） | 并入本档 **3-1** |
| 同档 P3-2（集中校验） | 并入本档 **3-2** |
| 同档「PI 独有优势」表 | 会话树/队列可视化已吸收为本档 2-3/4-3；OTEL 吸收为 4-4 |

---

# 二轮增补（2026-08-21）

一轮聚焦展示层主链路；二轮覆盖**交互层、会话与持久化工程、模型工程、MCP 深度、生态工程化**五个面，hiq 侧为逐项存在性核查（每项「有/无/部分」均带证据）。新增差距编入第八节路线图增补。

## 七、二轮补充差距

### 7.1 交互层

| 能力 | Codex | Pi | Hiq | 判定 |
|------|-------|-----|----------|------|
| 命令面板快捷键 | /keymap 交互式重映射 UI（选 action→捕获键/chord，`tui/src/keymap_setup.rs`）+ vim 模式 | /hotkeys + `core/keybindings.ts` 声明合并，用户可自定义 | **⌘K 注册走 `useGlobalShortcut`——空实现 stub**（`lib/keyboardShortcuts.ts` 仅 `useEffect(() => {}, [])`，注释 "Future"），CommandPalette 只能点顶栏按钮打开 | **bug 级断链** |
| 输入历史（上箭头） | TUI textarea 自带 | 100 条 + 在途草稿保护（`tui/src/components/editor.ts:316`） | **TUI 有**（`internal/cli/chat_tui.go:63` submittedInputs）**桌面无**——功能已在仓库里，纯移植 | 缺失（易补） |
| follow-up 队列 | **持久层队列**（`ext/queue/` SQLite：enqueue/list/reorder/start，空闲自动派发 + 10s watch 外部写入）；turn 内 steering 在采样边界注入（`start_or_steer_turn` 三态结果） | steer/followUp 双队列 + QueueMode + **Alt+Enter 排队 / Alt+Up 取回编辑** | 只有 steer 队列（`agent.go:230`）；`App.tsx handleSend` 里 agent 忙时**一切发送都转 steer**——连发多条会被当作"当前任务的补充指导"而非独立新 turn | **落后** |
| transcript 内搜索 | transcript overlay（Ctrl+T，live tail）+ `thread/searchOccurrences` 线程内分页命中 | alt-screen 搜索高亮（`alt-screen-search.ts`，当前项反白） | 无（Transcript.tsx 无任何搜索逻辑；CommandPalette 只搜会话元数据） | **缺失** |
| 键位自定义 | 完整 keymap 系统 + 两段 chord + 交互式 UI | keybindings 全动作 `app.*`/`tui.*` 可自定义 | 固定硬编码；`ShortcutsCheatsheet.tsx` 是占位 stub | 落后（GUI 优先级中低） |
| bash 直通 | unified exec PTY + write_stdin | `!` 前缀直通 bash、`!!` 隐藏执行 | TerminalPanel（Ctrl+`，一次性命令，非 PTY） | 落后（并入 3-4） |
| web_search 展示 | 专用 `WebSearchCell`（Searched/Opened page + spinner） | — | 通用工具卡 + markdown 列表 | 落后（并入 1-6） |
| 失败后手动重试 | StreamError 事件语义（"handling it"非终止） | abort 合成消息 + partial 保留 + 自动重试可取消（Esc） | 只有自动重试提示 `(n/m)`，**无手动重试按钮**，abort 后 partial 保留情况未设计 | 落后（并入 1-7） |

### 7.2 会话与持久化工程

| 能力 | Codex | Pi | Hiq | 判定 |
|------|-------|-----|----------|------|
| 跨 session 全文搜索 | `thread/search`：**用 ripgrep 直接搜 rollout 文件内容** + state_db 元数据/分页/排序（`thread-store/src/local/search_threads.rs:33`） | SQLite 后端 **FTS5 全文索引**（`packages/session-backends/sqlite-node/`） | 只有标题/预览的前端内存过滤（preview=首条用户消息前 80 字符） | **缺失** |
| 崩溃恢复 | `recover_turn_if_idle`（保留原 turn_id 续跑）+ rollout 截断预算 | v4 `LaneRecord`（operation_started/finished 逐步持久化，为崩溃恢复设计，WIP） | tab/会话状态可恢复；**正在跑的 turn 不续跑**（重开即 idle，停在最后原子保存点） | 落后 |
| 保存完整性 | rollout JSONL append-only + 反向扫描 | 原子发布（tmp+rename）+ torn-tail 自修复（坏行原子截断） | **tmp+rename 原子写 + HMAC-SHA256 `.sig` 完整性校验 + `NormalizeSession` torn-tail 修复**（`save.go:129`、`fileutil/atomicwrite.go`） | **领先**（保持） |
| 回滚前预览 | —（无 checkpoint 概念） | —（无） | checkpoint 独有优势，但 rewind 前**只有 filesChanged 文件计数**，无 diff 预览 | 独有能力未做满（并入 3-6） |
| 导出/分享 | — | HTML 导出（ANSI→HTML + 工具渲染器复用 TUI 渲染）+ `/share` secret gist | MD/JSON/PDF/PNG 四格式（`sessionExport.tsx`）——格式更多；**无 HTML/分享链接** | 各有胜负 |
| resume 体验 | resume picker + **旧会话 transcript 预览** | 树选择器 + label 书签 | HistoryPanel preview（80 字符）较浅 | 落后（低优先） |

### 7.3 模型工程

| 能力 | Codex | Pi | Hiq | 判定 |
|------|-------|-----|----------|------|
| token 计数 | 字节/4 启发式 + 服务端 usage 归一（扣除 baseline） | usage 回传 + 压缩后未知时显示 `?` | **服务端 usage 为准**（`ContextSnapshot`）+ cache_shape 字节估算兜底 | 持平 |
| prompt cache 主动管理 | `prompt_cache_key`（默认 session_id）+ `previous_response_id` 复用校验（8 项一致性）+ **启动预热 warmup 请求**（`session_startup_prewarm.rs`，正式请求继承 response id） | **Anthropic `cache_control` 断点 + 1h 长缓存** + OpenAI `prompt_cache_key`（cacheRetention 可控）+ **cache waste 检测**（5min TTL 逐轮算 re-billed 损失金额，`core/cache-stats.ts`） | 稳定前缀策略（boot.go cache-stable prefix，好底子）+ **cache_shape 诊断（miss 原因归因 system/tools/log_rewrite——独有）**；但**无任何主动断点**（grep `cache_control/cached_content` 零命中）、无预热 | **落后**（并入 4-6） |
| 限流可见性 | `RateLimitSnapshot`（5h/weekly 窗口 + credits）→ 状态行 20 段 `█░` 进度条 + 本地化重置时间 | RetryStatusIndicator 倒计时 + **Esc 取消重试** | 后端 `RequestBudget` 完整（RPM per-key + 优先级），**前端 `BudgetStatusView` 是死类型**——`types.ts:1613` 定义并注释 "Mirrors desktop.BudgetStatusView on the Go side"，但 Go 侧无此结构、无任何组件消费（ExpertPanel 注释自认无预算指示器） | **bug 级断链**（并入 0-8） |
| 成本视图 | /usage 菜单 + credits + spend control | /session 分账（按 provider/model + Tools/summaries 桶）+ **Cache Re-billed 损失金额** + footer 累计 `$` | GUI **零金额显示**（Pricing 只到 CLI 状态行与 serve wire）；无跨 session/按天/按模型聚合 | **缺失**（并入 5-1） |
| thinking 档位 | reasoning effort 配置 + statusline 显示 | **7 档**（off~max）+ per-model 默认 + `thinkingLevelMap` 声明 + 档位边框色 + Shift+Tab 循环 | 4 档（auto/low/medium/high，`EffortSwitcher`）+ 交错思考（Anthropic 签名回放）已支持 | 基本持平 |
| 结构化输出 | **turn 级 `final_output_json_schema` 一等公民**（Responses API 受限采样；steer 时 schema 不一致会被拒） | strict schema 改写（`makeStrictJsonSchema`：递归 required + anyOf null 包装）+ grammar 工具约束，覆盖 7 家 API | 仅 RAG 抽取用 `json_object`；主 agent 无任何约束输出 | **落后**（并入 4-7） |
| OAuth/订阅账号 | ChatGPT OAuth 登录 | 全家桶：Claude Pro/Max（PKCE+本地回调）、ChatGPT、Copilot device-code、OpenRouter/xai/kimi…+ 双重检查锁刷新 + `!command` 动态 API key | 静态 key/env（18 厂商 300+ 预设是强项）；无订阅型账号 OAuth | 路线差异（国内厂商为主时影响有限） |
| deferred tools | 无 API 级支持（ToolExposure::Deferred 是"工具搜索发现"，另一概念） | 管线就绪（StopReason "deferred" + 挂起/恢复/取回），provider 未产出 | 无 | 低优先（生态未成熟） |
| 本地模型 | — | **llama.cpp 扩展**：router mode 加载/卸载 + HuggingFace 搜索下载量化模型（Q4_K_M 推荐）+ `/llama` 命令 | 无专门支持（可走 OpenAI 兼容 endpoint 手配） | 落后（并入 5-3，可选） |

### 7.4 MCP 深度

| 能力 | Codex | Pi | Hiq |
|------|-------|-----|----------|
| 基础 | tools/resources/prompts + 进程内传输 | **无 MCP** | tools/prompts/resources + 懒加载/热添加 + stdio/HTTP + session 过期重连（**领先 pi**） |
| OAuth | **PKCE + 本地回调 + OS keyring 存储 + 刷新锁 + 动态客户端注册** | — | 无（mcpdiag 只做 401 诊断、提示可打开的授权 URL，不做 token 交换） |
| elicitation | 服务化：`ElicitationService` 计数暂停，阻塞工具结果交付直到完成；app-server 转 Form 请求给 GUI | — | 无（grep 零命中） |
| 进度通知 | 协议已定义 `item/mcpToolCall/progress`，TUI 已消费 | — | **显式丢弃**（`transport_stdio.go:417` "drops server-initiated notifications/requests"） |

结论：hiq 的 MCP 在广度上领先 pi，但在深度上全面落后 codex——长任务 MCP 工具（视频渲染、大数据管道）无进度反馈、需二次授权的 server 无法走完流程。

### 7.5 生态与工程化

| 维度 | Codex | Pi | Hiq |
|------|-------|-----|----------|
| 行为评测 | analytics 事件事实模型（goal/steer/compaction/guardian 全家） | **vitest-evals 自举式评测**：让 agent 写扩展→reload→调用→judge 精确断言；还做 harness baseline vs candidate 对比（去掉 Guidelines 的系统提示跑对比） | e2ebench 目录存在（性质不同）；pi 的"用产品自身验证产品提示词"方法值得借鉴 |
| headless 自动化 | cloud tasks + daemon + app-server websocket（带 capability-token/JWT 鉴权） | `pi -p` print / `--mode json`（JSONL + 背压控制）；**GitHub Actions issue 分析实战**（label 触发→分析→gist 回评→附本地续跑命令） | serve/bot/ACP/LoopPanel（多形态领先）；但缺"issue→分析→回帖→可续跑"这类闭环示例 |
| 自更新 | npm 平台分包分发 | Windows 占用文件隔离替换（.node 移隔离区再恢复） | 自动更新已有 |
| 研发自举 | — | **用 pi 开发 pi**（.pi/extensions 里 4 个自用扩展，tps 实时统计 TUI redraw） | — |

### 7.6 二轮新发现的 hiq 领先项（补充第三节保持清单）

1. **保存完整性三家最强**：原子写 + HMAC-SHA256 签名边车 + torn-tail 修复三重保障；
2. **cache_shape 诊断独有**：PrefixShape 哈希对比可归因 cache miss 来源（system/tools/log_rewrite），codex/pi 都只有统计没有归因；
3. `@past:chats` 会话引用（Composer at 菜单），两家均无；
4. E-Stop 全局热键、Pause/Resume 优雅暂停、语音输入（STT）、四格式导出；
5. AGENTS.md host checks → `complete_step` 可验证硬检查的工程闭环；
6. TUI 形态成熟（diff chroma 渲染、流式、resume、hooks 视图）——同仓库双形态，且 TUI 的输入历史等能力可反向移植桌面。

## 八、路线图增补（并入第五节各阶段）

### 阶段 0 追加（一小时~一天级）

| # | 改动 | 文件 | 量 |
|---|------|------|----|
| 0-7 | **修复 ⌘K stub**：实现 `useGlobalShortcut`（window keydown 匹配 combo）或在 App.tsx 直接注册，打通 CommandPalette 快捷键入口 | `lib/keyboardShortcuts.ts`、`App.tsx:2682` | ~20 行 |
| 0-8 | **激活 RPM 死类型**：Go 侧补 `BudgetStatus()` 绑定（读 `RequestBudget.Status`），wire 到 `BudgetStatusView`，UsageChip/Composer 参数行消费；Retrying 事件带 Retry-After 秒数做倒计时 | `desktop/`（新绑定）、`types.ts:1613`、`UsageChip.tsx` | ~60 行 |
| 0-9 | **桌面输入历史**：上箭头翻已发送输入（移植 TUI `submittedInputs/submittedInputCursor` 逻辑） | `Composer.tsx` | ~40 行 |

### 阶段 1 追加

| # | 改动 | 说明 |
|---|------|------|
| 1-6 | web_search 专用结果卡 | 标题/URL/摘要列表 + 来源 favicon，折叠计数（codex WebSearchCell 模式） |
| 1-7 | 失败 turn 手动重试 | notice 卡加 "重试" action（重发最后一条用户消息）；abort 后 partial 内容保留策略对齐 pi |

### 阶段 2 追加

| # | 改动 | 说明 |
|---|------|------|
| 2-5 | **follow-up 队列语义** | `Agent.Steer` 之外加 `FollowUp`（排队为独立新 turn，agent 空闲后依次执行）；Composer 支持 Alt+Enter 显式排队 / 队列条目可取回编辑（pi 模式）；与 2-3 steer 队列共用一个可视化组件 |

### 阶段 3 追加

| # | 改动 | 说明 |
|---|------|------|
| 3-6 | rewind 前 diff 预览 | checkpoint 已存文件快照，把 `filesChanged` 计数升级为可展开的逐文件 diff（复用 1-2 DiffView） |
| 3-7 | MCP 深度补课 | ① 进度通知透传（去掉 transport 层 drop，转发为 ToolProgress）；② elicitation → 复用 Approval 请求 UI；③ OAuth PKCE 流（本地回调 + token 存储） |

### 阶段 4 追加

| # | 改动 | 说明 |
|---|------|------|
| 4-5 | **会话全文搜索** | 两条路线：低成本=codex 模式（ripgrep 扫 session.jsonl 内容 + 元数据聚合）；长期=pi 模式（SQLite FTS5 索引）。CommandPalette 已有 fuzzy 框架可直接挂 |
| 4-6 | **prompt cache 主动化** | Anthropic `cache_control` 断点 + OpenAI `prompt_cache_key` 透传 + 启动预热（codex warmup 模式）；用已有 cache_shape 诊断量化前后收益——hiq 在这件事上有独到的验证工具 |
| 4-7 | turn 级结构化输出 | provider 层支持 `response_format: json_schema`；先给 LoopPanel/专家团/子代理等内部消费者用，再暴露给用户 |

### 新增阶段 5：生态（可选，按需）

| # | 方向 | 说明 |
|---|------|------|
| 5-1 | **成本聚合视图** | 跨 session/按天/按模型汇总（serve wire 已有 Cost 字段，缺聚合与 UI）；进阶做 pi 式 cache re-billed 损失估算（与 cache_shape 联动） |
| 5-2 | 崩溃 turn 恢复 | codex `recover_turn_if_idle` 模式：崩溃重开后识别未完成 turn 并续跑（依赖 4-1 事件 item 化更自然） |
| 5-3 | 本地模型接入 | Ollama/llama.cpp provider 预设 + 模型加载管理面板（pi llama 扩展模式；hiq 的 provider 预设体系接入成本低） |
| 5-4 | 行号跳编辑器 | 工具卡 diff/文件预览加 "在编辑器打开"（`vscode://file/<abs>:<line>` 协议链接） |
| 5-5 | 会话 HTML 导出/分享 | 补 HTML 格式（渲染产物已有 React DOM，导出比 pi 的 ANSI 转换更容易） |

### 增补后全景优先级

```
小时级   0-7 ⌘K stub → 0-8 RPM 死类型 → 0-9 输入历史
1-2 天   阶段 0 其余（FileDiff 通路/apply_patch Preview/审批 diff/reasoning md/成本行/摘要行）
1-2 周   阶段 1（工具卡体系/DiffView v2/分组/汇总卡/web_search 卡/手动重试）
~1 周    阶段 2（TodoPanel v2/状态行/steer+follow-up 队列可视化）
1-2 周   阶段 3（mutation queue/校验/流式补丁预览/rewind diff/MCP 补课）
长期     阶段 4（事件 item 化/快照协议/全文搜索/cache 主动化/结构化输出）
按需     阶段 5（成本聚合优先，其余视产品方向）
```

---

# 三轮增补（2026-08-21）：三 profile 覆盖矩阵

要回答的问题：**上述修复完成后，办公（cowork）界面和运维（netdev）界面是否同时受益？** 答案：会话流主链路和后端共享层的修复**改一处三界面自动生效**；但有**四个卡点**需要为 cowork/netdev 增补工作，另有三处早前结论需要修正。

## 9.1 架构结论：会话流是三界面单管道

App.tsx 把会话区构建为三个可注入节点——`mainNode`（Transcript）、`footerNode`（TodoPanel + ApprovalModal + AskCard + Composer）、`terminalNode`（TerminalPanel）；dev 布局内联，cowork/netdev 布局接收**同一批组件实例**（`App.tsx:3317`、`:3362`、`:3559-3566`）。profile 仅是 `boot.Options` 的覆盖包（`internal/config/profile.go:9-33`），三 profile 跑同一个 Agent/Controller/tabEventSink。

因此以下修复**三界面自动生效，无需重复工作**：Transcript/Message/ToolCard 渲染、专用卡注册表机制、`lib/tools.ts` 纯函数、TodoPanel、状态行（Composer phase）、steer/follow-up 队列可视化、CommandPalette（会话项按 profile 隔离）、输入历史（per-profile 存储 `lib/composerHistory.ts`）、ApprovalModal 工具级审批（含 netdev 的 `confirm_each_command` 卡，`NetDevSection.tsx:215`）、以及全部后端共享层（compaction、cache 稳定前缀、限流、token 统计、参数校验、结构化输出、截断安全、MCP）。

## 9.2 修复传播矩阵（前端）

| 修复 | dev | cowork | netdev |
|------|-----|--------|--------|
| Transcript/Message/ToolCard/专用卡机制 | ✅ | ✅ 自动 | ✅ 自动 |
| TodoPanel v2 / 状态行 / 队列可视化 / CommandPalette / 输入历史 | ✅ | ✅ 自动 | ✅ 自动 |
| ApprovalModal diff 预览（工具级） | ✅ | ✅ 自动 | ✅ 自动（但 diff 内容不适用，见卡点 3 的 Proposal） |
| FileDiff 透传 | ✅ | ✅ 自动（cowork 有 edit_file/write_file/bash） | ❌ 不适用（工具硬封印无 file-write） |
| WorkspacePanel 增强 | ✅ | ⚠️ 部分（files 页签复用；changed 页签被 gate 仅 dev，`App.tsx:3241`） | ❌ 无此面板 |
| DiffView v2 | ✅ | ✅（经 ToolCard） | ❌ 备份 diff 是自绘 pre，需替换（1-9） |
| TerminalPanel | ✅ | ❌ **CoWorkLayout 未接收 terminalNode**（`App.tsx:3365` 只传 NetDevLayout） | ✅ |

## 9.3 四个卡点（不增补工作就不受益）

### 卡点 1：Previewer 工具集只有 6 个编码工具 → cowork 办公产物不进 FileDiff/checkpoint

实现 `tool.Previewer` 的只有 write_file/edit_file/multi_edit/notebook_edit/delete_range/delete_symbol。checkpoint 快照钩子**只在 Preview 成功时触发**（`agent.go:1629-1639`，`checkpoint.go:8-10` 注释自认）。后果：`doc_write`/`csv_write`/`mindmap_create` 的产物（1）无 FileDiff 预览，（2）**不进 checkpoint——rewind 的 code scope 恢复不了办公文档**，（3）未来 per-file 队列也入不了队。

**增补（3-8）**：文本类产物（.md/.csv/.json/.txt）实现 Previewer（直接复用 `diff.Build`）；.docx/.xlsx 走 `diff.Change.Binary` 语义做摘要卡（段落数/工作表数）。这是一举三得的适配点。netdev 基本不适用（9 个工具全 ReadOnly，设备写路径整体走人工 Proposal 流水线，`proposal.go:21-26` 注释明示设计意图）。

### 卡点 2：证据链工具名单只枚举编码工具

`internal/evidence/evidence.go:570` 的 `isWriterTool/isReaderTool` 没有 doc_write 等；`HasSuccessfulCommand` 只认 bash → `netdev_exec` 的命令证据匹配不上。不扩名单的话 **TodoPanel v2 在 cowork/netdev 形同虚设**（todo 永远凑不齐证据）。

**增补（2-6）**：扩 writer/reader 名单 + 为 netdev_exec 加命令证据匹配器。

### 卡点 3：netdev dock 全套自绘，绕开共享组件

- **备份 diff**：后端 `netdev/backup.go:179-189` 已经复用 `internal/diff.Build`（与 edit_file 同源！），但前端 `NetDevLayout.tsx:959-965` 手写 `+/-` 前缀着色的 `<pre>`，不用 DiffView——DiffView v2（1-2）完成后直接替换（1-9）。
- **Proposal 审批**：`ProposalCenter.tsx:38-66` 用**原生 `confirm()` 弹窗** + 桥 API，不走 ApprovalModal/AskCard。升级为应用内审批组件并带"提案步骤 + 回滚命令"预览（3-9；`netdev_propose` 已落盘结构化 JSON，可直接复用）。
- 快捷诊断输出（`ndv__pre`）、Findings 证据链 `<pre>` 可在专用卡批次（1-8）统一。

### 卡点 4：布局级缺口与三个孤立组件

- cowork 无终端面板：`App.tsx:3365` 只给 NetDevLayout 传 terminalNode（增补 3-10）。
- **三个孤立组件**（grep 验证：仅存在于注释引用，无实际 import）：
  - `components/StatusBar.tsx` —— 0.1.9「底栏退役」后的遗留文件；命中率/上下文指标的**现役宿主是 `composer/UsageChip.tsx`**。本档 2.2 节相关表述已修正。
  - `components/cowork/AutomationPanel.tsx` —— 自动化任务面板做完没上屏。
  - `components/ExpertCollabCard.tsx` —— 专家协作曲线卡无挂载点。
  
  处置（0-10）：删除归档，或补挂载点（AutomationPanel 若要启用需在 CoWorkLayout 侧栏加导航项）。
- cowork 的 WorkspacePanel 复用但 changed 页签入口被 gate（仅 dev）：办公产物（docx）目前只能去 files 页签翻找，可考虑放开或做产物卡（1-8 的 DocCard 兼任）。

## 9.4 专用卡覆盖差距：cowork/netdev 比 dev 更"裸"

`subjectOf()` 只识别 bash/grep/glob/web_fetch/task/remember；`diffsFor()` 只识别三个编辑工具。以下工具**零专用渲染**，全部落入通用卡：

- cowork：`browser_*` 15 个（截图只有 ToolAttachments 缩略图，无动作时间线）、`screen_*` 6 个、`doc/csv/xlsx` 9 个、`email_*` 3 个、`rag_*` 6 个、`schedule_*` 5 个、`expert_*` 2 个
- netdev：`netdev_exec/devices/discover/topology/propose/finding/netconf/baseline/redfish` 9 个 + `netdev_rag_*` 2 个

→ 1-1 注册表的**机制**三界面自动生效，但渲染器内容是按 profile 分批补的工作量。建议 1-8 批次：BrowserCard（截图 + 动作流）、DocCard（文本 diff / 二进制摘要）、EmailCard（收件域主题）、NetdevExecCard（设备 + 命令 + 输出折叠）。

## 9.5 审批语义不一致（安全项）

cowork 办公工具大多被 Hide、由 `run_skill` 子代理调用，而子代理 gate 是 `headlessGate`（Approver=nil）：Ask 对普通工具**自动放行**，仅 IsExternal（email_send、rag_delete、默认全部 MCP）直接拒（`permission.go:549-566`）。结果：同一个 `doc_write` 在主循环弹审批、在子代理静默执行。建议统一为"子代理写操作上报主 gate"或至少在 UI 标注执行者（3-11）。netdev 侧无此问题（confirm_each_command 卡走共享 ApprovalModal）。

## 9.6 增补项并入路线图

| 编号 | 内容 | 并入阶段 |
|------|------|---------|
| 0-10 | 处置三个孤立组件（StatusBar 删除/归档；AutomationPanel、ExpertCollabCard 定挂载点或删除） | 阶段 0 |
| 1-8 | cowork/netdev 专用卡批次：BrowserCard、DocCard、EmailCard、NetdevExecCard、ScheduleCard | 阶段 1 |
| 1-9 | netdev 备份 diff：手写 pre 着色替换为 DiffView v2（后端已复用 internal/diff，只差前端） | 阶段 1 |
| 2-6 | 证据链名单扩展（isWriterTool/isReaderTool + netdev_exec 命令匹配器） | 阶段 2 |
| 3-8 | cowork Previewer 适配（doc_write/csv_write/mindmap_create 文本类 + Binary 摘要卡）——打通 FileDiff/checkpoint/mutation queue 三机制 | 阶段 3 |
| 3-9 | Proposal 审批升级：原生 confirm() → 应用内组件 + 提案步骤/回滚命令预览 | 阶段 3 |
| 3-10 | cowork 接入 terminalNode（TerminalPanel 可用） | 阶段 3 |
| 3-11 | 子代理审批语义统一（headlessGate 写操作上报主 gate 或 UI 标注） | 阶段 3 |

### 三轮后的全景优先级（增补行）

```
小时级   + 0-10 孤立组件处置（可与 0-7/0-8/0-9 同批）
阶段 1   + 1-8 cowork/netdev 专用卡批次 + 1-9 备份 diff 统一
阶段 2   + 2-6 证据链名单扩展（否则 TodoPanel v2 在两条产品线失效）
阶段 3   + 3-8 cowork Previewer（一举三得）/ 3-9 Proposal 审批 / 3-10 终端接入 / 3-11 审批语义统一
```
