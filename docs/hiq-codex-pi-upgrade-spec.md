# Hiq 对标 Codex / Pi 的编码体验升级 Spec

> **状态**: 规划中
> **基线**: `feat/mindmap-read-loop` 分支 (2026-08-21)
> **范围**: 编码模块显示、审批 UX、进度面板、流式输出 —— 主要涉及 `desktop/frontend/src/components` 与部分后端 `internal/present` / `internal/diff`
> **对标来源**:
> - **Codex** (OpenAI) — `Swarm-OS/codex/codex-rs/tui` — Rust ratatui 终端应用
> - **Pi** (Earendil Works) — `Swarm-OS/pi/packages` — TypeScript 自研 TUI
> **红线**: 功能零缺失;现有 UI 改版后所有入口仍可达

---

## 一、背景与动机

Hiq 当前的编码体验在三个维度上落后于 Codex 和 Pi：

1. **Diff 显示** — 只有 CodeMirror side-by-side MergeView，没有文件级摘要、行数统计、unified diff 模式
2. **审批 UX** — ApprovalModal 只显示工具名 + subject 文本，看不到实际代码变更和命令内容
3. **进度展示** — ToolCard 缺少实时流式输出、exit code、stderr 高亮；没有全局 Agent 仪表板

本文档基于对 Codex (`codex-rs/tui`) 和 Pi (`pi/packages`) 源码的逐文件分析，给出 Hiq 的分阶段升级方案。

---

## 二、三方架构对比

### 2.1 技术栈

| 维度 | Hiq | Codex | Pi |
|------|----------|-------|-----|
| 核心语言 | Go | Rust | TypeScript |
| 前端框架 | React + Wails (Web 渲染) | ratatui (终端即时渲染) | 自研 pi-tui (终端差分渲染) |
| Diff 引擎 | CodeMirror MergeView | `diffy` + 自研 `diff_render.rs` | `diff` npm 包 + fuzzy match |
| 代码高亮 | CodeMirror / highlight.js | syntect (按 hunk 整块高亮) | 无独立高亮 |
| 插件体系 | Go plugin | Rust 模块 | Extension API (TS) |
| 运行形态 | 桌面 GUI | 终端 TUI | 终端 TUI |

### 2.2 Hiq 的结构性优势（不可丢弃）

| 能力 | 说明 |
|------|------|
| 桌面 GUI 渲染 | Web 渲染天然支持富文本、图片、LaTeX、Mermaid |
| Loop 自动化 | LoopPanel 预设/队列/时间线，Codex/Pi 均无 |
| Bot 集成 | 飞书/QQ/Telegram/微信，多渠道入口 |
| PPT 生成 | pptauto 模板填充 + SVG 叠加 |
| Output Style | 人格切换（explanatory/learning/concise） |
| 热区/冷区渲染 | Transcript HOT_TURNS=30，大对话性能优 |
| 多 Provider | Anthropic + OpenAI 双通道 |

---

## 三、差距分析（逐维度）

### 3.1 代码 Diff 显示

#### 当前状态

Hiq 的 diff 渲染链路：

```
ToolCard → DiffView → CodeMirrorDiff (MergeView)
```

- [DiffView.tsx](desktop/frontend/src/components/DiffView.tsx) — lazy import CodeMirrorDiff
- [CodeMirrorDiff.tsx](desktop/frontend/src/components/editors/CodeMirrorDiff.tsx) — `new MergeView({ a, b })` 纯左右对比
- [diff.go](internal/diff/diff.go) — 后端 Myers O(ND) 算法，生成 unified diff 文本

**问题**：后端已经计算了 unified diff（`Change.Diff` 字段），但前端只用了 `OldText` / `NewText` 做 side-by-side，`Diff` 字段被丢弃。

#### Codex 做法

```
FileChange → diff_render.rs → HistoryCell (PatchHistoryCell)
```

- [diff_model.rs](../../codex/codex-rs/tui/src/diff_model.rs) — `FileChange::Update { unified_diff, move_path }`
- [diff_render.rs](../../codex/codex-rs/tui/src/diff_render.rs) — 390 行，含：
  - 暗色/亮色主题自适应（`DiffTheme`）
  - 按 hunk 整块语法高亮（保持 parser 状态）
  - 行号列 + gutter 符号（`+`/`-`/` `）
  - 长行自动换行，样式跨行保持
  - 256 色 / 16 色回退
- [patches.rs](../../codex/codex-rs/tui/src/history_cell/patches.rs) — `create_diff_summary` 生成文件级摘要（`A path/file.rs`）
- [apply_patch_header.rs](../../codex/codex-rs/tui/src/bottom_pane/apply_patch_header.rs) — 审批弹窗头部：Thread + Description + Destination 列表

#### Pi 做法

```
edit tool → edit-diff.ts → renderCall/renderResult (Extension API)
```

- [edit-diff.ts](../../pi/packages/agent/src/harness/tools/edit-diff.ts) — `Diff` 库 + fuzzy match + 行级替换
- [built-in-tool-renderer.ts](../../pi/packages/coding-agent/examples/extensions/built-in-tool-renderer.ts) — 自定义渲染：
  - `renderCall` — 显示 `edit path` + 行范围
  - `renderResult` — 紧凑 `+N/-M` 统计，展开后显示前 15 行
  - `expanded` 标志支持 ctrl+e 切换

#### 差距总结

| 特性 | Hiq | Codex | Pi |
|------|----------|-------|-----|
| 文件级变更摘要 | ❌ | ✅ `A/M/D` 文件列表 | ✅ 缩略 |
| 行数统计 `+N/-M` | ❌ | ✅ | ✅ |
| Unified diff 模式 | ❌（后端有，前端未用） | ✅ 默认 | ✅ |
| 语法高亮 diff | CodeMirror 内置 | syntect 按 hunk 高亮 | ❌ |
| 主题自适应 | ❌ | ✅ 暗色/亮色 | ✅ theme |
| 大 diff 容错 | ❌ | ✅ maxDiffEdits=2000 | ✅ 截断 |
| 行号 | ✅ CodeMirror | ✅ 右对齐列 | ❌ |
| Hunk 折叠 | ❌ | ✅ | ✅ ctrl+e |

---

### 3.2 审批（Approval）UX

#### 当前状态

- [ApprovalModal.tsx](desktop/frontend/src/components/ApprovalModal.tsx) — 120 行
  - 显示 `approval.subject`（工具名 + 参数摘要）
  - 操作：允许 / 允许+记住 / 始终允许 / 拒绝
  - 计划模式：修订 / 执行 / 退出
  - 快捷键：1/2/3/4 数字键
  - **不显示**：代码变更、命令内容、文件路径

#### Codex 做法

- [approval_overlay.rs](../../codex/codex-rs/tui/src/bottom_pane/approval_overlay.rs) — 400+ 行
  - 四种审批类型：`Exec` / `ApplyPatch` / `Permissions` / `McpElicitation`
  - `Exec` — 显示命令 + bash 语法高亮 + 网络权限上下文
  - `ApplyPatch` — 显示文件列表 + diff 预览 + destination 路径
  - `Permissions` — 显示请求的权限 profile
  - 每种类型有独立的 `available_decisions` 列表
  - 完整 keymap 系统（`ApprovalKeymap`）

#### 差距总结

| 特性 | Hiq | Codex |
|------|----------|-------|
| Diff 预览 | ❌ | ✅ 完整 diff |
| 命令预览 | ❌ | ✅ bash 高亮 |
| 权限粒度 | 工具级 | 工具+路径+网络 |
| 审批类型 | 通用 | 4 种专用类型 |
| 文件列表 | ❌ | ✅ |
| 网络上下文 | ❌ | ✅ |

---

### 3.3 进度与状态显示

#### 当前状态

- [ToolCard.tsx](desktop/frontend/src/components/ToolCard.tsx) — 250 行
  - 状态：`running`（Loader2 旋转）/ `error`（XCircle）/ 完成（无图标）
  - Shell 输出：预览 10 行 + "显示全部" 按钮
  - 耗时：`durationMs` 格式化
  - 子任务嵌套：`subcalls` 渲染
  - **缺少**：实时流式输出、exit code、stderr 分离

- [ProcessCard.tsx](desktop/frontend/src/components/ProcessCard.tsx) — 100 行
  - 状态图标：running/done/failed/waiting/stopped
  - **缺少**：进度百分比、活动描述

#### Codex 做法

- [exec.rs](../../codex/codex-rs/tui/src/history_cell/exec.rs) — `UnifiedExecProcessDetails`
  - `command_display` — 命令文本
  - `recent_chunks` — 最近 N 个输出块（实时追加）
  - 后台终端交互显示（stdin 回显）
- [agent_status_feed.rs](../../codex/codex-rs/tui/src/app/agent_status_feed.rs) — Sub-agent 状态
  - 3 行预览限制（`AGENT_STATUS_PREVIEW_LINES`）
  - 6 项限制（`AGENT_STATUS_PREVIEW_ITEMS`）
  - 240 字符限制（`AGENT_STATUS_PREVIEW_GRAPHEMES`）
- [agents_overview_view.rs](../../codex/codex-rs/tui/src/app/agents_overview_view.rs) — 仪表板
  - 分组：`NeedsYou` / `Working` / `Ready` / `Finished`
  - 搜索、重命名、线程管理
  - 当前线程高亮

#### 差距总结

| 特性 | Hiq | Codex | Pi |
|------|----------|-------|-----|
| Shell 实时输出 | ❌ 仅最终结果 | ✅ recent chunks | ✅ partial |
| Exit code 显示 | ❌ | ✅ | ✅ |
| stderr 高亮 | ❌ | ✅ 红色 | ✅ theme.error |
| Agent 仪表板 | ❌ | ✅ 4 组分组 | ❌ |
| "需要你" 提示 | ❌ | ✅ NeedsYou 组 | ❌ |
| Reasoning 展示 | 脑图标 | ✅ 标题提取 + thinking | ❌ |
| 活动预览 | ❌ | ✅ 3 行 + 240 字符 | ❌ |

---

### 3.4 流式输出

#### 当前状态

- [Transcript.tsx](desktop/frontend/src/components/Transcript.tsx) — `LiveStreamContext`
  - 文本流式：✅ 实时追加
  - Reasoning：❌ 无独立显示
  - 打字光标：✅ `injectStreamingCursor`
  - 流式 → 历史合并：✅

#### Codex 做法

- [streaming.rs](../../codex/codex-rs/tui/src/chatwidget/streaming.rs)
  - `stream_controller` — 流控制器，管理 tail cell
  - `reasoning_buffer` — reasoning 内容缓冲
  - `extract_first_bold` — 从 reasoning 提取标题作为状态显示
  - `TerminalTitleStatusKind::Thinking` / `Working` — 终端标题状态
  - interrupt deferral — 中断延迟处理
  - adaptive chunking — 自适应分块

#### 差距总结

| 特性 | Hiq | Codex |
|------|----------|-------|
| 文本流式 | ✅ | ✅ |
| Reasoning 流式 | ❌ | ✅ buffer + header |
| Thinking 状态 | ❌ | ✅ 标题栏显示 |
| 中断恢复 | ❌ | ✅ deferral |
| 自适应分块 | ❌ | ✅ adaptive_chunking |

---

## 四、升级方案（分 Phase）

### Phase 1: Diff 与代码显示升级

> **目标**：让用户在审批弹窗和工具卡片中看到完整的代码变更信息
> **优先级**：🔴 P0（影响最大）
> **改动范围**：前端 `components/` + 后端 `internal/present`

#### WP-1.1: 审批弹窗内嵌 Diff 预览

**问题**：ApprovalModal 只显示 `approval.subject`（工具名 + 参数），用户不知道具体要改什么。

**方案**：

```
ApprovalModal
├── 头部：工具名 + 状态
├── 文件摘要栏：A file1.rs | M file2.rs | D file3.rs (+42/-12)
├── Diff 预览区：CodeMirror unified diff (可折叠)
└── 操作栏：允许 / 拒绝 / 快捷键
```

**改动文件**：
- `ApprovalModal.tsx` — 新增 `changes` prop，渲染文件摘要 + DiffView
- `lib/types.ts` — `WireApproval` 扩展 `changes?: WireFileChange[]`
- 后端 `internal/present/record.go` — 审批事件携带 diff 数据

**数据结构**（参考 Codex `ApplyPatchApprovalRequest`）：

```typescript
interface WireFileChange {
  path: string;
  kind: "create" | "modify" | "delete";
  added: number;    // +N
  removed: number;  // -M
  diff: string;     // unified diff 文本（复用后端 Change.Diff）
}
```

**验收标准**：
- [ ] 审批弹窗显示待变更文件列表（`A/M/D` 标记 + 路径）
- [ ] 每个文件可展开查看 unified diff
- [ ] 头部显示 `+N/-M` 总行数统计
- [ ] 快捷键 `j/k` 上下切换文件，`e` 展开/折叠

#### WP-1.2: 文件级变更摘要

**问题**：ToolCard 只显示工具名 + subject，多文件变更时无法一目了然。

**方案**：在 ToolCard 头部增加文件摘要行。

```
edit_file  src/foo.ts, src/bar.ts  +42/-12  230ms
  M src/foo.ts (+30/-8)
  M src/bar.ts (+12/-4)
```

**改动文件**：
- `ToolCard.tsx` — 头部新增摘要行
- `lib/tools.ts` — `diffsFor` 返回值增加 `added`/`removed` 字段

**验收标准**：
- [ ] 多文件变更时显示文件列表
- [ ] 显示 `+N/-M` 行数统计
- [ ] 单文件变更时显示文件名

#### WP-1.3: Unified Diff 视图选项

**问题**：当前只有 side-by-side MergeView，开发者更习惯 unified diff。

**方案**：为 DiffView 增加视图模式切换。

```
DiffView
├── 模式切换按钮：[Unified | Split]
├── Unified 模式：CodeMirror unified diff（复用后端 Diff 字段）
└── Split 模式：现有 MergeView
```

**改动文件**：
- `DiffView.tsx` — 增加 `mode` prop + 切换按钮
- `editors/CodeMirrorUnified.tsx` — 新增 unified diff 渲染组件
- `styles.css` — unified diff 样式（+绿色背景 / -红色背景）

**验收标准**：
- [ ] 默认显示 unified diff 模式
- [ ] 可切换到 side-by-side 模式
- [ ] 行号正确对齐
- [ ] 语法高亮正常工作

#### WP-1.4: Diff 主题适配

**问题**：diff 背景色固定，暗色模式下对比度差。

**方案**：参考 Codex `DiffTheme` 系统，根据当前主题动态调整 diff 背景色。

**改动文件**：
- `styles.css` — diff 相关 CSS 变量
- `editors/CodeMirrorDiff.tsx` — 读取主题变量

**色值参考**（来自 Codex `diff_render.rs`）：

```css
/* 暗色模式 */
--diff-add-bg: #213A2B;
--diff-del-bg: #4A221D;

/* 亮色模式 */
--diff-add-bg: #dafbe1;
--diff-del-bg: #ffebe9;
--diff-add-gutter-bg: #aceebb;
--diff-del-gutter-bg: #ffcecb;
```

**验收标准**：
- [ ] 暗色模式下 diff 背景色柔和不刺眼
- [ ] 亮色模式下 diff 背景色参考 GitHub 风格
- [ ] gutter（行号列）背景色与内容区区分

---

### Phase 2: 审批体验升级

> **目标**：让用户在审批时看到完整的操作上下文
> **优先级**：🟡 P1
> **改动范围**：前端 `ApprovalModal` + 后端审批事件

#### WP-2.1: 命令预览

**问题**：bash 工具审批时不知道要执行什么命令。

**方案**：在审批弹窗中显示完整命令 + 语法高亮。

```
ApprovalModal (bash 类型)
├── 命令预览区：syntax highlighted bash
├── 工作目录：/path/to/project
├── 网络权限提示（如有）
└── 操作栏
```

**改动文件**：
- `ApprovalModal.tsx` — 增加命令渲染分支
- 后端 — 审批事件携带完整命令字符串

**验收标准**：
- [ ] bash 命令显示完整内容（不截断）
- [ ] 命令有 bash 语法高亮
- [ ] 显示工作目录

#### WP-2.2: 权限粒度细化

**问题**：当前只有工具级允许/拒绝，无法对特定路径或网络设限。

**方案**：参考 Codex 的 `FileSystemSandboxEntry` + `NetworkPolicyRuleAction`。

**数据结构**：

```typescript
interface PermissionRequest {
  tool: string;
  paths?: string[];         // 涉及的文件路径
  network?: {
    host: string;
    port?: number;
    protocol: string;
  };
  decisions: PermissionDecision[];
}

interface PermissionDecision {
  label: string;            // "Allow for this file"
  scope: "once" | "session" | "always";
  restrictions?: {
    allowedPaths?: string[];
    deniedPaths?: string[];
    allowedHosts?: string[];
  };
}
```

**改动文件**：
- `ApprovalModal.tsx` — 多级决策 UI
- `lib/types.ts` — 新增权限类型
- 后端 `internal/permission/` — 权限评估逻辑

**验收标准**：
- [ ] 可选择"允许此文件" / "允许此目录" / "允许所有"
- [ ] 网络请求显示目标 host + port
- [ ] 权限决策可持久化

#### WP-2.3: 审批快捷键增强

**问题**：当前只有 1/2/3/4 数字键，缺少导航能力。

**方案**：

| 快捷键 | 功能 |
|--------|------|
| `1/2/3/4` | 保持现有操作 |
| `j/k` | 上下切换变更文件 |
| `Tab` | 在文件列表和操作栏之间切换焦点 |
| `e` | 展开/折叠当前文件 diff |
| `Enter` | 确认当前选中操作 |
| `Esc` | 拒绝/取消 |

**改动文件**：
- `ApprovalModal.tsx` — 键盘事件处理扩展

**验收标准**：
- [ ] `j/k` 可在文件列表中导航
- [ ] `e` 可展开/折叠 diff
- [ ] 焦点状态有视觉反馈

---

### Phase 3: 进度与状态面板

> **目标**：让用户实时看到 Agent 的工作进展
> **优先级**：🟡 P1
> **改动范围**：前端 `ToolCard` / `ProcessCard` + 新增 `AgentDashboard`

#### WP-3.1: Shell 实时流式输出

**问题**：ToolCard 的 shell 输出只有最终结果，长命令执行时用户看不到中间过程。

**方案**：running 状态下实时追加输出块。

```
ToolCard (bash, running)
├── 头部：$ npm test (running...)
├── 输出区：实时追加的 stdout/stderr
│   > PASS src/foo.test.ts
│   > PASS src/bar.test.ts
│   > Running... 3/15
└── 尾部：自动滚动到最新
```

**改动文件**：
- `ToolCard.tsx` — running 状态下监听 output 增量
- `lib/tools.ts` — 增加 `streamingOutput` 状态
- 后端 — shell 执行过程中推送输出块

**验收标准**：
- [ ] bash 工具 running 时输出实时更新
- [ ] 输出区自动滚动到最新行
- [ ] 完成后显示 exit code（0 绿色 / 非 0 红色）

#### WP-3.2: Exit Code 与 stderr 高亮

**问题**：shell 命令失败时，用户需要在大段输出中找错误信息。

**方案**：

```
ToolCard (bash, failed)
├── 头部：$ npm test  exit 1  2.3s
├── stdout 区：正常颜色
├── stderr 区：红色背景高亮
└── 错误摘要：最后 5 行 stderr
```

**改动文件**：
- `ToolCard.tsx` — 区分 stdout/stderr 渲染
- 后端 — shell 执行结果分 stdout/stderr

**验收标准**：
- [ ] exit code 显示在头部（0 绿色 / 非 0 红色）
- [ ] stderr 用红色背景或红色文字高亮
- [ ] 失败时 ToolCard 自动展开

#### WP-3.3: Agent 仪表板

**问题**：多 Agent 并行工作时，没有全局视图。

**方案**：新增 Agent 仪表板面板，参考 Codex `AgentsOverviewView`。

```
AgentDashboard
├── 搜索栏
├── 分组：
│   ├── 🔴 Needs Action (等待审批/输入)
│   ├── 🟡 Working (正在执行)
│   ├── 🟢 Ready (空闲)
│   └── ⚪ Finished (已完成)
└── 每个 Agent 行：
    ├── 名称 + 模型
    ├── 当前任务摘要
    ├── 状态图标
    └── 操作：切换 / 停止
```

**改动文件**：
- 新增 `components/AgentDashboard.tsx`
- `AppChrome.tsx` — 新增入口按钮
- `lib/bridge.ts` — 新增 Agent 列表 API

**验收标准**：
- [ ] 显示所有活跃 Agent 的分组列表
- [ ] 点击可切换到对应 Agent 的对话
- [ ] "Needs Action" 组有醒目提示
- [ ] 支持搜索过滤

#### WP-3.4: Reasoning 内容展示

**问题**：ProcessBrainIcon 只是个图标，用户看不到 Agent 的思考过程。

**方案**：在 ProcessCard 中展示 reasoning 摘要。

```
ProcessCard (thinking)
├── 🧠 Thinking: Analyzing the file structure...
├── 展开区：完整 reasoning 内容
└── 状态：thinking / working / done
```

**改动文件**：
- `ProcessCard.tsx` — 增加 reasoning 预览区
- `lib/useController.ts` — reasoning 数据透传

**验收标准**：
- [ ] thinking 状态显示 reasoning 摘要（前 100 字符）
- [ ] 可展开查看完整 reasoning
- [ ] thinking → working → done 状态切换有过渡动画

---

### Phase 4: 流式体验优化

> **目标**：对齐 Codex 的流式输出质量
> **优先级**：🟢 P2
> **改动范围**：前端流式渲染 + 后端流式推送

#### WP-4.1: Reasoning 流式显示

**问题**：reasoning 内容没有实时流式渲染。

**方案**：参考 Codex `streaming.rs` 的 `reasoning_buffer`。

**改动文件**：
- `Message.tsx` — `AssistantMessage` 增加 reasoning 流式区
- `lib/useController.ts` — LiveStream 增加 reasoning 字段
- 后端 — reasoning delta 实时推送

**验收标准**：
- [ ] reasoning 内容实时流式显示
- [ ] reasoning 区域有视觉区分（灰色背景 / 斜体）
- [ ] reasoning 完成后折叠为摘要

#### WP-4.2: Thinking/Working 状态指示

**问题**：用户不知道 Agent 当前是在思考还是在执行。

**方案**：参考 Codex 的 `TerminalTitleStatusKind`。

**改动文件**：
- `StatusBar.tsx` — 显示当前状态
- `Message.tsx` — 状态指示器

**状态定义**：

```typescript
type AgentStatus = "idle" | "thinking" | "working" | "streaming" | "waiting";
```

**验收标准**：
- [ ] 状态栏显示当前 Agent 状态
- [ ] thinking 状态有动画指示器
- [ ] 状态切换有平滑过渡

#### WP-4.3: 流式中断与恢复

**问题**：长回复无法中断后继续。

**方案**：支持用户中断流式输出，保留已生成内容。

**改动文件**：
- `Composer.tsx` — 流式中显示"停止"按钮
- 后端 — 支持中断信号

**验收标准**：
- [ ] 流式输出时显示"停止生成"按钮
- [ ] 中断后保留已生成内容
- [ ] 可从中断点继续生成

---

### Phase 5: 高级能力

> **目标**：对齐 Codex 的安全和可观测性能力
> **优先级**：🔵 P3
> **改动范围**：后端沙箱 + 前端可视化

#### WP-5.1: Token 用量可视化

**问题**：用户不知道当前对话消耗了多少 token。

**方案**：

```
StatusBar / ContextPanel
├── Token 使用量：12,345 / 200,000 (6.2%)
├── 进度条：绿色 < 50% | 黄色 50-80% | 红色 > 80%
└── 警告：接近上下文限制时弹出提示
```

**改动文件**：
- `ContextPanel.tsx` — 增加 token 用量显示
- `StatusBar.tsx` — 紧凑模式用量指示
- 后端 — token 计数 API

**验收标准**：
- [ ] 显示 input/output/total token 数
- [ ] 进度条颜色随使用率变化
- [ ] 超过 80% 时显示警告

#### WP-5.2: 容器化沙箱

**问题**：bash 命令直接在宿主机执行，有安全风险。

**方案**：参考 Codex 的 Gondolin + OpenShell 方案。

**实现路径**：
1. Docker 沙箱 — bash 命令在容器中执行
2. 路径白名单 — 只允许访问项目目录
3. 网络隔离 — 默认禁止网络访问

**改动文件**：
- 后端 `internal/sandbox/` — 沙箱执行引擎
- 前端 — 沙箱状态指示

**验收标准**：
- [ ] bash 命令在沙箱中执行
- [ ] 文件访问限制在项目目录内
- [ ] 网络访问默认禁止，可按 host 白名单开放

#### WP-5.3: 多文件编辑预览

**问题**：一次审批多个文件变更时，没有全局视图。

**方案**：参考 Codex 的 `ApplyPatchApprovalRequest` 设计。

```
ApprovalModal (multi-file)
├── 文件列表：可点击切换
│   ├── M src/foo.ts (+30/-8)  ← 当前选中
│   ├── M src/bar.ts (+12/-4)
│   └── A src/baz.ts (+50/-0)
├── Diff 预览：当前选中文件的 diff
├── 全局统计：+92/-12 across 3 files
└── 操作：Accept All / Reject All / 逐文件审批
```

**改动文件**：
- `ApprovalModal.tsx` — 多文件审批 UI
- `DiffView.tsx` — 支持文件切换

**验收标准**：
- [ ] 文件列表可点击切换
- [ ] 显示全局 `+N/-M` 统计
- [ ] 支持逐文件审批
- [ ] 支持一键全部接受/拒绝

---

## 五、实施优先级总览

| 优先级 | 编号 | 项目 | 影响 | 工作量 | 依赖 |
|--------|------|------|------|--------|------|
| 🔴 P0 | WP-1.1 | 审批内嵌 Diff 预览 | 高 | 中 | 无 |
| 🔴 P0 | WP-1.2 | 文件级变更摘要 | 高 | 小 | 无 |
| 🟡 P1 | WP-2.1 | 命令预览 | 中 | 小 | 无 |
| 🟡 P1 | WP-3.1 | Shell 实时流式输出 | 高 | 中 | 后端改动 |
| 🟡 P1 | WP-3.2 | Exit Code / stderr 高亮 | 中 | 小 | 后端改动 |
| 🟢 P2 | WP-1.3 | Unified Diff 视图 | 中 | 中 | WP-1.1 |
| 🟢 P2 | WP-1.4 | Diff 主题适配 | 小 | 小 | WP-1.3 |
| 🟢 P2 | WP-2.3 | 审批快捷键增强 | 小 | 小 | WP-1.1 |
| 🟢 P2 | WP-3.3 | Agent 仪表板 | 高 | 大 | 无 |
| 🟢 P2 | WP-3.4 | Reasoning 展示 | 中 | 小 | 无 |
| 🟢 P2 | WP-4.1 | Reasoning 流式 | 中 | 中 | 后端改动 |
| 🟢 P2 | WP-4.2 | 状态指示器 | 小 | 小 | 无 |
| 🔵 P3 | WP-2.2 | 权限粒度细化 | 中 | 大 | 后端改动 |
| 🔵 P3 | WP-4.3 | 流式中断恢复 | 中 | 中 | 后端改动 |
| 🔵 P3 | WP-5.1 | Token 用量可视化 | 中 | 中 | 后端 API |
| 🔵 P3 | WP-5.2 | 容器化沙箱 | 高 | 大 | 后端改动 |
| 🔵 P3 | WP-5.3 | 多文件编辑预览 | 中 | 中 | WP-1.1 |

---

## 六、实施建议

### 6.1 推荐启动顺序

```
Phase 1 (2-3 周)
├── WP-1.1 审批内嵌 Diff        ← 最高 ROI
├── WP-1.2 文件级变更摘要       ← 低成本高收益
└── WP-2.1 命令预览             ← 安全相关

Phase 2 (3-4 周)
├── WP-3.1 Shell 实时输出
├── WP-3.2 Exit Code 高亮
├── WP-1.3 Unified Diff
├── WP-1.4 Diff 主题适配
└── WP-2.3 审批快捷键

Phase 3 (4-6 周)
├── WP-3.3 Agent 仪表板
├── WP-3.4 Reasoning 展示
├── WP-4.1 Reasoning 流式
└── WP-4.2 状态指示器

Phase 4 (按需)
├── WP-2.2 权限粒度
├── WP-4.3 流式中断
├── WP-5.1 Token 可视化
├── WP-5.2 容器化沙箱
└── WP-5.3 多文件编辑预览
```

### 6.2 验收标准

每个 WP 完成后需通过：
1. **功能验收** — 按本文档的验收标准逐项勾检
2. **回归测试** — 确保现有功能零缺失（§1.2 红线）
3. **跨平台** — Windows / macOS / Linux 三平台验证
4. **主题兼容** — 6 个 theme_style × 亮暗/auto 主题下正常显示
5. **性能** — 大 diff（1000+ 行）渲染不卡顿

### 6.3 参考实现索引

| Hiq 组件 | Codex 参考 | Pi 参考 |
|---------------|-----------|---------|
| DiffView | `diff_render.rs` | `edit-diff.ts` |
| ApprovalModal | `approval_overlay.rs` + `apply_patch_header.rs` | N/A |
| ToolCard | `exec.rs` + `agent_status_feed.rs` | `built-in-tool-renderer.ts` |
| ProcessCard | `agents_overview_view.rs` | N/A |
| 流式输出 | `streaming.rs` | N/A |
| 主题系统 | `DiffTheme` (diff_render.rs) | theme 系统 |

---

## 七、附录

### A. 后端 diff.go 已有但前端未用的字段

```go
// internal/diff/diff.go
type Change struct {
    Path    string `json:"path"`
    Kind    Kind   `json:"kind"`      // "create" | "modify" | "delete"
    OldText string `json:"old_text"`
    NewText string `json:"new_text"`
    Added   int    `json:"added"`     // ← 前端未用
    Removed int    `json:"removed"`   // ← 前端未用
    Diff    string `json:"diff"`      // ← 前端未用（unified diff 文本）
    Binary  bool   `json:"binary"`
}
```

WP-1.1 和 WP-1.2 可直接复用这些字段，无需后端改动。

### B. Codex diff_render.rs 核心渲染流程

```
FileChange::Update { unified_diff }
  → diffy::Patch::from_str(unified_diff)
  → 遍历 hunks
    → 遍历 lines
      → 分类：Insert / Delete / Context
      → 语法高亮：highlight_code_to_styled_spans (按 hunk 整块)
      → 主题着色：DiffTheme 选择背景色
      → 行号计算：old_line / new_line 递增
      → 换行处理：长行 hard-wrap + 样式保持
  → 输出：Vec<Line<'static>>
```

### C. Pi built-in-tool-renderer 核心模式

```typescript
pi.registerTool({
  name: "edit",
  async execute(...) { /* 委托给原始工具 */ },
  renderCall(args, theme) {
    // 显示：edit path (lines X-Y)
    return new Text(theme.fg("toolTitle", "edit ") + theme.fg("accent", args.path));
  },
  renderResult(result, { expanded }, theme) {
    const details = result.details as EditToolDetails;
    // 紧凑：+N/-M lines changed
    // 展开：前 15 行 diff
    let text = theme.fg("success", `+${details.added}/-${details.removed} lines`);
    if (expanded) { /* ... */ }
    return new Text(text);
  },
});
```

---

> **文档版本**: v1.0
> **最后更新**: 2026-08-21
> **作者**: 基于 Codex / Pi 源码分析自动生成
