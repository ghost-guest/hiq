# Hiq 架构与交互层差距分析

> 补充文档：覆盖 `hiq-codex-pi-upgrade-spec.md`（显示层）和
> `hiq-beyond-display-upgrade-spec.md`（架构层）未涉及的 20 项差距。
>
> 对标项目：Codex（OpenAI）、Pi（Earendil Works）
>
> 日期：2026-08-21

---

## 目录

1. [前端扩展系统](#1-前端扩展系统)
2. [CJK 感知文本编辑](#2-cjk-感知文本编辑)
3. [键盘系统](#3-键盘系统)
4. [终端集成](#4-终端集成)
5. [子代理 Git Worktree 隔离](#5-子代理-git-worktree-隔离)
6. [文件资源管理器](#6-文件资源管理器)
7. [Git 集成](#7-git-集成)
8. [自动补全](#8-自动补全)
9. [内联可视化](#9-内联可视化)
10. [自适应流式分块](#10-自适应流式分块)
11. [Diff 语法高亮](#11-diff-语法高亮)
12. [主题系统](#12-主题系统)
13. [审批类型分化](#13-审批类型分化)
14. [多代理仪表板](#14-多代理仪表板)
15. [会话检查点](#15-会话检查点)
16. [Token 预算可视化](#16-token-预算可视化)
17. [推理过程展示](#17-推理过程展示)
18. [MCP 客户端工具调用](#18-mcp-客户端工具调用)
19. [沙箱容器隔离](#19-沙箱容器隔离)
20. [细粒度权限模型](#20-细粒度权限模型)
21. [优先级排序](#优先级排序)

---

## 1. 前端扩展系统

**现状**：Hiq 的 `toolCards.tsx` 是内部注册表，无公开 API。第三方无法自定义工具卡片渲染。

**对标**：Pi 的 Extension API 提供三个核心接口：
- `registerTool({ name, renderCall, renderResult })` — 自定义工具调用/结果渲染
- `registerCommand({ name, description, action })` — 注册斜杠命令
- `registerMessageRenderer({ match, render })` — 自定义消息渲染器

**差距**：
- 无法让第三方插件注册自定义工具卡片
- 无法扩展斜杠命令（当前 SlashMenu 仅支持内置命令）
- 无法为特定消息类型（如 JSON、表格）注册自定义渲染器

**建议**：WP-EXT-1：暴露 `hiq.registerTool({ name, renderCall, renderResult })` API，允许扩展包注册工具卡片渲染器。

---

## 2. CJK 感知文本编辑

**现状**：Composer 是原生 `<textarea>`，无 CJK 分段能力。中文文本按字符移动光标，无法按词移动。

**对标**：Pi 使用 `Intl.Segmenter` 实现：
- CJK 词级光标移动（Ctrl+←/→ 按词跳转）
- 粘贴标记作为原子单元（折叠/展开）
- 粘贴内容不影响 undo 历史

**差距**：
- 中文粘贴后 Ctrl+←/→ 按字符移动而非按词
- 大块粘贴文本无折叠标记（Hiq 有 `shouldFoldPaste` 但仅用于显示，非原子单元）
- 无 CJK 词级选择（双击选中）

**建议**：
- WP-TEXT-1：集成 `Intl.Segmenter` 实现 CJK 词级移动和选择
- WP-TEXT-2：粘贴标记系统（折叠/展开、原子删除）

---

## 3. 键盘系统

**现状**：仅 5 个全局快捷键（Ctrl+K/N/B/,/ /），Composer 内无行编辑快捷键。

**对标**：Pi 提供完整的 readline 风格键绑定：
- Ctrl+A/E：行首/行尾
- Ctrl+F/B：前进/后退一个字符
- Ctrl+D：删除光标后字符
- Ctrl+K：删除到行尾
- Ctrl+Y：粘贴（kill-ring）
- Ctrl+U：删除到行首
- Alt+F/B：前进/后退一个词
- vim/emacs 模式切换

**差距**：
- Composer 内无 Ctrl+A/E/F/B/D/K/Y 等行编辑快捷键
- 无 kill-ring（剪切环）
- 无 undo-stack（撤销栈）
- 无 vim/emacs 模式

**建议**：
- WP-KEY-1：实现 readline 风格行编辑（Ctrl+A/E/F/B/D/K/Y/U）
- WP-KEY-2：kill-ring + undo-stack
- WP-KEY-3：vim/emacs 模式切换（可选）

---

## 4. 终端集成

**现状**：`TerminalPanel` 是一次性命令执行（通过 `RunShell` bridge），非交互式 PTY。源码注释明确说："A full interactive PTY (ConPTY + xterm.js) can later replace the transport"。

**对标**：ZCode/Cursor 的完整 xterm.js 终端：
- 交互式程序支持（vim、top、ssh）
- 命令历史（↑/↓）
- 终端分屏
- 终端搜索

**差距**：
- 无法运行交互式程序
- 无命令历史
- 无终端分屏
- 输出仅纯文本，无 ANSI 颜色

**建议**：WP-TERM-1：集成 ConPTY + xterm.js 实现完整 PTY 终端。

---

## 5. 子代理 Git Worktree 隔离

**现状**：`task.go` 支持子代理（`TaskTool`），但无 git worktree 隔离。并行子代理共享同一工作目录。

**对标**：Codex 的 `NewWorktree` 隔离模式：
- 每个子代理获得独立的 git worktree
- 文件写入互不冲突
- 完成后自动清理 worktree

**差距**：
- 并行子代理可能产生文件写入冲突
- 无自动冲突检测
- 无 worktree 生命周期管理

**建议**：WP-ISO-1：为子代理实现 `git worktree add` 隔离，完成后自动清理。

---

## 6. 文件资源管理器

**现状**：`WorkspacePanel` 有文件树+预览，但独立于对话区，仅支持查看。

**对标**：VS Code/Cursor 的集成文件资源管理器：
- 文件重命名/删除/创建
- 拖拽文件到编辑器
- 文件搜索（模糊匹配）
- 文件图标（按扩展名）

**差距**：
- 无法直接在文件树中重命名/删除/创建文件
- 无拖拽文件到 Composer 作为引用
- 无模糊文件搜索

**建议**：
- WP-FILE-1：增强 WorkspacePanel 支持文件操作（重命名/删除/创建）
- WP-FILE-2：拖拽文件到 Composer 作为 `@[path]` 引用

---

## 7. Git 集成

**现状**：WorkspacePanel 显示基本 git status（变更文件列表），有 `GitCommitView` 类型。

**对标**：VS Code 的 Git 面板：
- Git log 可视化（提交历史图）
- 行级 blame 注解
- 分支管理（创建/切换/合并）
- 暂存/取消暂存
- 提交历史对比

**差距**：
- 无 git log 可视化
- 无 blame 注解
- 无分支管理 UI
- 无暂存区管理

**建议**：
- WP-GIT-1：Git 日志面板（提交历史图）
- WP-GIT-2：行级 blame 注解
- WP-GIT-3：分支管理 UI

---

## 8. 自动补全

**现状**：SlashMenu 提供斜杠命令补全，`@` 触发文件引用但无路径补全。

**对标**：Cursor/Copilot 的上下文感知补全：
- `@` 文件路径补全（模糊匹配）
- 工具参数补全
- 历史命令补全

**差距**：
- 输入 `@` 后无文件路径补全（仅手动输入路径）
- 无工具参数智能补全
- 无历史命令补全

**建议**：
- WP-AC-1：`@` 文件路径模糊补全
- WP-AC-2：工具参数智能补全

---

## 9. 内联可视化

**现状**：Markdown 渲染支持 Mermaid 图表，有 `MermaidViewer` 组件。

**对标**：Cursor 的内联数据可视化：
- 内联表格渲染
- 代码执行结果可视化
- 数据图表（柱状图、折线图）

**差距**：
- 无法内联渲染表格数据（仅 Markdown 表格）
- 无代码执行结果可视化
- 无数据图表渲染

**建议**：
- WP-VIZ-1：内联数据表格渲染（可排序/筛选）
- WP-VIZ-2：代码执行结果可视化

---

## 10. 自适应流式分块

**现状**：流式输出按固定块渲染，有 stale-stream watchdog（120s 超时）。

**对标**：Codex 的 `streaming.rs`：
- 自适应分块（根据网络速度调整）
- 推理缓冲区（结构化展示推理过程）
- 头部提取（从流中提取元数据）

**差距**：
- 长输出可能卡顿（无自适应分块）
- 推理过程无结构化展示
- 无流中元数据提取

**建议**：
- WP-STREAM-1：自适应分块渲染（根据网络速度调整块大小）
- WP-STREAM-2：推理过程结构化展示（分段、时间线）

---

## 11. Diff 语法高亮

**现状**：`UnifiedDiff.tsx` 使用基本 +/- 着色，`CodeMirrorDiff.tsx` 使用 CodeMirror MergeView。

**对标**：Codex 的 `diff_render.rs`：
- 按 hunk 语法高亮（每行独立高亮）
- 主题适配（跟随终端主题）
- 行号显示
- gutter 符号（+/-/空）

**差距**：
- UnifiedDiff 无语法高亮（仅 +/- 颜色）
- CodeMirrorDiff 有语法高亮但无 word-level diff
- 无主题适配

**建议**：WP-DIFF-1：集成 highlight.js/Shiki 实现 diff 语法高亮（按行高亮）。

---

## 12. 主题系统

**现状**：dark/light/auto 三种模式，`desktopThemeStyle` 配置。

**对标**：Codex 的 per-component 主题适配：
- 编辑器语法主题（One Dark、GitHub、Solarized 等）
- 终端颜色方案
- Diff 颜色方案
- 自定义 CSS 变量

**差距**：
- 无法自定义编辑器语法主题
- 无终端颜色方案配置
- 无 per-component 主题覆盖

**建议**：WP-THEME-1：可配置语法主题（One Dark、GitHub、Solarized 等）。

---

## 13. 审批类型分化

**现状**：单一 `ApprovalModal` 处理所有审批，有文件变更预览和命令预览。

**对标**：Codex 的 4 种审批类型：
- **Exec**：命令执行审批（显示命令+工作目录）
- **ApplyPatch**：文件编辑审批（显示 diff 预览）
- **Permissions**：权限变更审批
- **McpElicitation**：MCP 工具审批

**差距**：
- 所有工具使用同一审批 UI
- 无法根据工具类型定制审批界面
- 无权限变更专用审批

**建议**：WP-APPR-1：按工具类型定制审批 UI（bash 显示命令预览，edit_file 显示 diff 预览）。

---

## 14. 多代理仪表板

**现状**：子代理在 ToolCard 中嵌套显示，有 `ExpertSessionView` 专家团队协作。

**对标**：Codex 的 `AgentsOverviewView`：
- **NeedsYou**：需要用户输入的代理
- **Working**：正在工作的代理
- **Ready**：就绪的代理
- **Finished**：完成的代理
- 全局状态概览

**差距**：
- 无全局代理状态概览面板
- 难以管理多个并行子代理
- 无代理状态分组

**建议**：WP-AGENT-1：多代理仪表板面板（NeedsYou/Working/Ready/Finished 分组）。

---

## 15. 会话检查点

**现状**：会话持久化到 JSONL，支持 rewind/fork（6 种范围：fork、summ-from、summ-upto、conversation、code、both）。

**对标**：Codex 的 checkpoint 系统：
- 显式检查点标记
- 检查点命名
- 检查点恢复
- 检查点对比

**差距**：
- 无显式检查点标记（仅有自动持久化）
- 无法命名检查点
- 无法恢复到特定检查点

**建议**：WP-CHK-1：检查点标记+命名+恢复。

---

## 16. Token 预算可视化

**现状**：ContextPanel 显示 token 使用量（prompt/completion/reasoning 分类），有 `BudgetStatusView`（rpm/used/remaining）。

**对标**：Codex 的上下文窗口可视化：
- Token 预算进度条
- 上下文窗口剩余预测
- 按类别着色（prompt/completion/reasoning）

**差距**：
- 无 token 预算进度条
- 无上下文窗口剩余预测
- 无按类别着色

**建议**：WP-TOKEN-1：Token 预算进度条+剩余预测+按类别着色。

---

## 17. 推理过程展示

**现状**：Message 组件支持 reasoning expand/collapse，有 `expandThinking` 配置。

**对标**：Codex 的推理缓冲区：
- 推理过程结构化分段
- 推理时间线
- 推理 token 统计

**差距**：
- 推理过程无结构化分段（仅纯文本展开）
- 无推理时间线
- 无推理 token 统计

**建议**：WP-REASON-1：推理过程结构化分段+时间线+token 统计。

---

## 18. MCP 客户端工具调用

**现状**：有 MCP 服务器管理 UI（`ServerView`、`MCPToolView`），工具调用通过后端处理。

**对标**：Codex 的 MCP 客户端：
- 前端直接展示 MCP 工具调用过程
- MCP 工具结果可视化
- MCP 工具错误处理

**差距**：
- 前端无法直接展示 MCP 工具的调用过程
- MCP 工具结果仅通过 ToolCard 通用展示
- 无 MCP 工具专用错误处理

**建议**：WP-MCP-1：MCP 工具调用前端可视化（专用卡片、结果渲染）。

---

## 19. 沙箱容器隔离

**现状**：`SandboxView` 配置 bash/network/workspace 权限，有 `SettingsPanel` 沙箱设置 UI。

**对标**：Codex 的容器隔离模式：
- Docker 容器隔离
- Firejail 沙箱
- 网络隔离
- 文件系统隔离

**差距**：
- 无实际容器隔离（仅权限配置）
- 无网络隔离
- 无文件系统隔离

**建议**：WP-SANDBOX-1：Docker/Firejail 容器隔离（可选启用）。

---

## 20. 细粒度权限模型

**现状**：`PermissionsView` 有 allow/ask/deny 列表，`ApprovalModeSwitcher` 切换审批模式。

**对标**：Codex 的 per-tool 权限：
- per-path 文件权限（允许读写 `/src`，禁止 `/etc`）
- per-command bash 权限（允许 `git`，禁止 `rm -rf`）
- 权限继承（目录权限传递给子文件）

**差距**：
- 权限粒度不够细（仅 tool 级别）
- 无 per-path 文件权限
- 无 per-command bash 权限

**建议**：
- WP-PERM-1：per-path 文件权限（glob 模式匹配）
- WP-PERM-2：per-command bash 权限（命令白名单/黑名单）

---

## 优先级排序

### P0：核心体验提升（1-2 周）

| 编号 | 工作包 | 说明 |
|------|--------|------|
| 1 | WP-KEY-1 | readline 风格行编辑（Ctrl+A/E/F/B/D/K/Y/U） |
| 2 | WP-TEXT-1 | CJK 感知文本编辑（Intl.Segmenter） |
| 3 | WP-DIFF-1 | Diff 语法高亮（highlight.js/Shiki） |
| 4 | WP-TERM-1 | 完整 PTY 终端（ConPTY + xterm.js） |

### P1：架构竞争力（2-4 周）

| 编号 | 工作包 | 说明 |
|------|--------|------|
| 5 | WP-EXT-1 | 前端扩展 API（registerTool） |
| 6 | WP-ISO-1 | 子代理 worktree 隔离 |
| 7 | WP-AGENT-1 | 多代理仪表板面板 |
| 8 | WP-STREAM-1 | 自适应流式分块 |

### P2：功能完善（4-6 周）

| 编号 | 工作包 | 说明 |
|------|--------|------|
| 9 | WP-FILE-1 | 文件操作增强（重命名/删除/创建） |
| 10 | WP-GIT-1 | Git 日志面板 |
| 11 | WP-AC-1 | 文件路径补全 |
| 12 | WP-APPR-1 | 审批类型分化 |
| 13 | WP-TOKEN-1 | Token 预算可视化 |

### P3：高级特性（6-8 周）

| 编号 | 工作包 | 说明 |
|------|--------|------|
| 14 | WP-VIZ-1 | 内联数据可视化 |
| 15 | WP-CHK-1 | 检查点系统 |
| 16 | WP-REASON-1 | 推理过程结构化 |
| 17 | WP-MCP-1 | MCP 工具前端可视化 |
| 18 | WP-SANDBOX-1 | 容器隔离 |
| 19 | WP-PERM-1/2 | 细粒度权限 |
| 20 | WP-THEME-1 | 可配置语法主题 |

---

## 与前两份文档的关系

| 文档 | 覆盖范围 | 工作包数 |
|------|----------|----------|
| `hiq-codex-pi-upgrade-spec.md` | 显示层（Diff、审批、进度、流式） | 17 |
| `hiq-beyond-display-upgrade-spec.md` | 架构层（扩展、隔离、MCP、会话） | 13 |
| **本文档** | 交互层（键盘、终端、文件、Git、补全） | 20 |
| **合计** | — | **50** |

---

## 附录：Hiq 已有能力（避免重复规划）

以下能力 Hiq 已实现，**无需升级**：

- ✅ UnifiedDiff 统一/分屏视图（`UnifiedDiff.tsx`）
- ✅ 文件变更摘要（A/M/D 标记 + +N/-M 统计）
- ✅ 审批内嵌 Diff 预览（`ApprovalModal.tsx`）
- ✅ 命令预览（bash CodeViewer + 工作目录）
- ✅ 审批快捷键（j/k/e 导航）
- ✅ MCP 服务器管理 UI
- ✅ 沙箱设置 UI
- ✅ 权限设置 UI
- ✅ 专家团队协作（debate/parallel/pipeline）
- ✅ Loop 工程面板（6 个预设）
- ✅ RAG 知识库
- ✅ 记忆系统（user/feedback/project/reference）
- ✅ Bot 集成
- ✅ 定时任务
- ✅ Hooks 系统
- ✅ Dream/Distill 自进化
- ✅ 多邮箱邮件集成
- ✅ 浏览器控制
- ✅ 会话 rewind/fork
- ✅ 子代理嵌套显示
- ✅ 会话导出（Markdown/JSON/PDF/Image）
