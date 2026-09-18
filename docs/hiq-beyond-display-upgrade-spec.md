# Hiq 对标 Codex / Pi — 展示层之外的深层差距与提升规划

> **状态**: 规划中
> **基线**: `feat/mindmap-read-loop` 分支 (2026-08-21)
> **范围**: 展示层之外的架构级能力差距 — 扩展系统、子Agent隔离、MCP客户端、会话生命周期、沙箱安全、文本处理、键盘系统等
> **前序文档**: `hiq-codex-pi-upgrade-spec.md`（聚焦 Diff/审批/进度的显示层升级）
> **对标来源**:
> - **Codex** — `codex-rs/tui` (Rust ratatui) — 重点分析 `subagents.rs`, `event_dispatch.rs`, `approval_overlay.rs`, `bottom_pane/mod.rs`
> - **Pi** — `pi/packages` (TypeScript) — 重点分析 `tui.ts`, `session.ts`, `types.ts`, `tool-execution.ts`

---

## 一、总结：展示层之外的 10 大差距

前一份 spec 聚焦"看到什么"（Diff、审批、进度），本文档聚焦"能做什么"——架构级能力差距。

| # | 能力维度 | Hiq 现状 | Codex 做法 | Pi 做法 | 差距等级 |
|---|----------|--------------|-----------|---------|----------|
| 1 | 扩展系统 | Go plugin（后端） | Rust 模块 | `ExtensionAPI` (TS) | 🔴 严重 |
| 2 | 子Agent 隔离 | 无隔离 | 3 种隔离模式 | 无 | 🔴 严重 |
| 3 | MCP 客户端 | builtinmcp (仅 Context7) | 完整 MCP 客户端 | 无 | 🟡 中等 |
| 4 | 会话生命周期 | 基础 resume | 完整 thread 管理 | compact/export/fork | 🟡 中等 |
| 5 | 沙箱安全 | 无 | Docker/Gondolin/OpenShell | 无 | 🔴 严重 |
| 6 | 权限模型 | 工具级 allow/deny | 工具+路径+网络 级 | 工具级 | 🟡 中等 |
| 7 | 文本处理 | 标准 textarea | 终端原生 | CJK/emoji/paste marker | 🟡 中等 |
| 8 | 键盘系统 | 最小快捷键 | 完整 keymap 系统 | emacs/vim 绑定 | 🟡 中等 |
| 9 | Token 可视化 | 基础计数 | 用量+速率+上下文警告 | 无 | 🟡 中等 |
| 10 | 渲染架构 | React 组件 | `Renderable` trait | Box/Text 组件树 | 🟢 轻微 |

---

## 二、逐项差距分析

### 2.1 扩展系统（差距等级：🔴 严重）

#### Hiq 现状

Hiq 的工具系统是**纯后端硬编码**：

```
internal/tool/registry.go → tool.Registry → 工具注册在 Go 编译时完成
internal/agent/builtin/    → bash, edit_file, read_file 等内置工具
```

前端没有扩展点。`ToolCard.tsx` 通过 `item.name` 做 if/else 分支渲染（`isShellTool` 判断），每新增一种工具都需要修改前端代码。

#### Pi 做法

Pi 有一套完整的前端扩展 API：

```typescript
// 注册工具渲染器
pi.registerTool({
  name: "edit",
  async execute(...) { /* 委托给原始工具 */ },
  renderCall(args, theme) { return new Text(...); },
  renderResult(result, { expanded }, theme) { return new Text(...); },
});

// 注册消息渲染器
pi.registerMessageRenderer("status-update", (message, { expanded }, theme) => {
  return new Box(outputPad, 1, (t) => theme.bg("customMessageBg", t));
});

// 注册命令
pi.registerCommand("status", { handler: async (args) => { ... } });

// 注册 UI 区域
pi.registerHeader(({ theme }) => new Text(...));
pi.registerFooter(({ theme }) => new Text(...));
pi.registerStatusBar(({ theme, cancel }) => ...);
```

#### Codex 做法

Codex 的扩展是 Rust 模块级的，通过 `HistoryCell` trait 实现：

```rust
pub(crate) trait HistoryCell: std::fmt::Debug {
    fn display_lines(&self, width: u16) -> Vec<Line<'static>>;
    fn raw_lines(&self) -> Vec<Line<'static>>;
    fn estimated_line_count(&self) -> usize;
    // ... 更多方法
}
```

每种内容类型（Patch, Exec, Messages, Plans）实现自己的 `HistoryCell`，新增类型只需实现 trait。

#### 差距

| 特性 | Hiq | Codex | Pi |
|------|----------|-------|-----|
| 前端扩展点 | ❌ | Rust trait | `ExtensionAPI` |
| 自定义工具渲染 | ❌ 硬编码 | ✅ `HistoryCell` | ✅ `registerTool` |
| 自定义消息渲染 | ❌ | ✅ | ✅ `registerMessageRenderer` |
| 自定义命令 | ❌ | ❌ | ✅ `registerCommand` |
| 自定义 UI 区域 | ❌ | ❌ | ✅ header/footer/statusBar |
| 主题系统 | CSS 变量 | ratatui style | `theme` 对象 |

#### 提升方案

**WP-EXT-1: 前端 Extension Registry**

```typescript
// lib/extension.ts
interface ToolRenderer {
  name: string;
  renderCall?(args: string, theme: Theme): ReactNode;
  renderResult?(result: ToolResult, opts: { expanded: boolean }, theme: Theme): ReactNode;
}

interface MessageRenderer {
  kind: string;
  render(message: Item, opts: { expanded: boolean }, theme: Theme): ReactNode;
}

const registry = {
  toolRenderers: new Map<string, ToolRenderer>(),
  messageRenderers: new Map<string, MessageRenderer>(),
  registerTool(r: ToolRenderer) { registry.toolRenderers.set(r.name, r); },
  registerMessage(kind: string, r: MessageRenderer) { registry.messageRenderers.set(kind, r); },
};
```

- `ToolCard.tsx` 渲染时先查 `registry.toolRenderers`，有注册则用自定义渲染，否则用默认
- 新增工具无需改前端代码，只需注册渲染器

---

### 2.2 子Agent 隔离（差距等级：🔴 严重）

#### Hiq 现状

Hiq 有 `task` 和 `parallel_tasks` 工具，子Agent 在**同一进程**中运行：

```go
// internal/agent/parallel_tasks.go
go func(idx int, ...) {
    answer, err := p.taskTool.runSubSession(ctx, task.Prompt, subReg, taskSink, ...)
    results[idx] = result{...}
}(i, t, run.Session, prov, pricing, ctxWin, maxSteps, sink)
```

子Agent 共享：
- 同一文件系统（无沙箱）
- 同一网络（无隔离）
- 同一 git 工作区（无 worktree）

#### Codex 做法

Codex 有 3 种子Agent 隔离模式：

```rust
pub(crate) enum SubAgentIsolationMode {
    SameContainer,    // 共享容器（最轻量）
    NewContainer,     // 新容器（Docker/Gondolin 隔离）
    NewWorktree,      // 新 git worktree（文件系统隔离但共享 git 历史）
}
```

- `SameContainer` — 共享文件系统，适合只读任务
- `NewContainer` — Docker 容器隔离，适合写入任务
- `NewWorktree` — git worktree 隔离，多个子Agent 可以并行修改不同文件而不冲突

#### 差距

| 特性 | Hiq | Codex |
|------|----------|-------|
| 隔离模式 | ❌ 无 | ✅ 3 种 |
| Git worktree | ❌ | ✅ 并行开发 |
| 容器隔离 | ❌ | ✅ Docker/Gondolin |
| 文件系统沙箱 | ❌ | ✅ |
| 网络隔离 | ❌ | ✅ |

#### 提升方案

**WP-ISO-1: Git Worktree 隔离**

```go
// internal/agent/worktree.go
type WorktreeManager struct {
    repoRoot string
    baseRef  string
}

func (w *WorktreeManager) Create(name string) (string, error) {
    // git worktree add .worktrees/<name> -b <name>
}

func (w *WorktreeManager) Remove(name string) error {
    // git worktree remove .worktrees/<name>
}
```

- 子Agent 默认在新 worktree 中运行
- 完成后自动合并或保留 worktree 供审查
- 并行任务每个子任务一个 worktree

**WP-ISO-2: 容器沙箱**

```go
// internal/sandbox/docker.go
type DockerSandbox struct {
    image   string
    volumes []VolumeMount
    network string
}

func (d *DockerSandbox) Run(command string, opts RunOpts) (Output, error) {
    // docker run --rm -v ... <image> sh -c <command>
}
```

---

### 2.3 MCP 客户端（差距等级：🟡 中等）

#### Hiq 现状

Hiq 有 MCP 支持，但范围有限：

```go
// internal/builtinmcp/builtinmcp.go — 仅 Context7
func Entries() []config.PluginEntry {
    return []config.PluginEntry{context7Entry()}
}

// internal/acp/protocol.go — ACP 协议支持 MCP
type MCPServerSpec struct {
    Name    string   `json:"name"`
    Type    string   `json:"type,omitempty"`
    Command string   `json:"command,omitempty"`
    Args    []string `json:"args,omitempty"`
    Env     MCPEnv   `json:"env,omitempty"`
}
```

- 只有 Context7 一个内置 MCP 服务器
- ACP 协议支持 stdio MCP，但无 http/sse
- 无 MCP 服务器健康监控
- 无 MCP 工具发现/注册 UI

#### Codex 做法

Codex 有完整的 MCP 客户端：

```rust
// McpConnectionStatus
pub(crate) enum McpConnectionStatus {
    Connecting,
    Connected,
    Disconnected,
    Failed(String),
}

// 连接管理
fn connect_to_mcp_server(server: &McpServerConfig) -> Result<McpConnection>;
fn disconnect_from_mcp_server(connection: &McpConnection);

// 工具发现
fn list_mcp_tools(connection: &McpConnection) -> Vec<McpTool>;

// 健康监控
fn check_mcp_health(connection: &McpConnection) -> McpHealthStatus;

// UI 集成
fn render_mcp_status(connections: &[McpConnection]) -> String;
```

- 支持 stdio + http + sse 三种传输
- 有连接状态指示器
- 有工具发现和注册
- 有健康监控和自动重连

#### 差距

| 特性 | Hiq | Codex |
|------|----------|-------|
| 内置 MCP 服务器 | Context7 only | 多个 |
| 传输协议 | stdio | stdio + http + sse |
| 连接状态 UI | ❌ | ✅ |
| 工具发现 | ❌ | ✅ |
| 健康监控 | ❌ | ✅ |
| 自动重连 | ❌ | ✅ |
| 用户配置 UI | ❌ | ✅ |

#### 提升方案

**WP-MCP-1: MCP 服务器管理 UI**

```typescript
// components/McpPanel.tsx
interface McpServer {
  name: string;
  status: "connecting" | "connected" | "disconnected" | "failed";
  tools: McpTool[];
  lastPing: number;
}

// 设置页新增 MCP 服务器管理
// - 添加/删除 MCP 服务器
// - 查看连接状态
// - 查看可用工具列表
// - 手动重连
```

**WP-MCP-2: HTTP/SSE 传输支持**

```go
// internal/mcp/http.go
type HTTPTransport struct {
    baseURL string
    client  *http.Client
}

// internal/mcp/sse.go
type SSETransport struct {
    endpoint string
    events   chan SSEEvent
}
```

---

### 2.4 会话生命周期（差距等级：🟡 中等）

#### Hiq 现状

Hiq 的会话管理：

```typescript
// useController.ts
newSession()      // 新建会话
clearSession()    // 清空会话
resumeSession()   // 恢复会话
listSessions()    // 列出会话
deleteSession()   // 删除会话
renameSession()   // 重命名会话
compact()         // 压缩上下文
rewind()          // 回退到某轮
```

已有的能力：
- ✅ 会话持久化（session.jsonl）
- ✅ 会话恢复（resumeSession）
- ✅ 会话重命名/删除
- ✅ 上下文压缩（compaction）
- ✅ 回退/分叉（rewind/fork）
- ✅ 多 Tab 管理

#### Codex 做法

Codex 的会话管理更精细：

```rust
// 线程生命周期
LoadThread { thread_id, source }  // 加载线程
CloseThread { thread_id }         // 关闭线程
TurnStart { thread_id }           // 开始一轮

// 线程状态
enum ThreadStatus {
    Active { active_flags: Set<ThreadActiveFlag> },
    Idle,
    SystemError,
    NotLoaded,
}

// 活动标志
enum ThreadActiveFlag {
    WaitingOnApproval,
    WaitingOnUserInput,
}

// 线程元数据
struct Thread {
    id: ThreadId,
    label: String,
    status: ThreadStatus,
    source: SessionSource,
}
```

#### Pi 做法

Pi 的会话管理：

```typescript
// session.ts
compact()                    // 压缩上下文
exportTranscript(format)     // 导出对话记录
listThreads()                // 列出子线程
getCurrentThread()           // 获取当前线程
startThread(opts)            // 启动子线程
switchToThread(id)           // 切换线程
closeThread(opts)            // 关闭线程
getDefaultThread()           // 获取默认线程
```

#### 差距

| 特性 | Hiq | Codex | Pi |
|------|----------|-------|-----|
| 会话持久化 | ✅ | ✅ | ✅ |
| 会话恢复 | ✅ | ✅ | ✅ |
| 多 Tab | ✅ | ❌ | ❌ |
| 子线程 | ❌ | ✅ | ✅ |
| 线程状态 | ❌ | ✅ 4 种状态 | ✅ |
| 导出对话 | ❌ | ❌ | ✅ compact/export |
| 会话标签 | ✅ | ✅ | ❌ |
| 会话分组 | ❌ | ✅ (agents 分组) | ❌ |

#### 提升方案

**WP-SESS-1: 对话导出**

```typescript
// useController.ts
const exportSession = useCallback(async (format: "markdown" | "json" | "html") => {
  if (!activeTabId) return;
  const data = await app.ExportSession(activeTabId, format);
  // 触发下载
}, [activeTabId]);
```

**WP-SESS-2: 会话分组与标签**

```typescript
// 会话元数据扩展
interface SessionMeta {
  // ... 现有字段
  tags?: string[];        // 标签
  group?: string;         // 分组
  pinned?: boolean;       // 置顶
  archived?: boolean;     // 归档
}
```

---

### 2.5 沙箱安全（差距等级：🔴 严重）

#### Hiq 现状

**无沙箱**。bash 工具直接在宿主机执行：

```go
// internal/tool/bash.go
func (b *BashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
    cmd := exec.CommandContext(ctx, "bash", "-c", args.Command)
    cmd.Dir = args.Cwd
    // 直接执行，无隔离
}
```

#### Codex 做法

Codex 有多层沙箱：

```rust
// 文件系统沙箱
struct FileSystemSandboxEntry {
    path: PathBuf,
    writable: bool,
}

// 网络策略
enum NetworkPolicyRuleAction {
    Allow,
    Deny,
    Ask,
}

// 容器沙箱
enum SandboxMode {
    Docker { image: String },
    Gondolin,
    OpenShell,
}
```

- 文件系统：白名单控制可访问路径
- 网络：按 host/port 控制出站请求
- 容器：Docker/Gondinux/OpenShell 三种方案

#### 差距

| 特性 | Hiq | Codex |
|------|----------|-------|
| 文件系统沙箱 | ❌ | ✅ 白名单 |
| 网络沙箱 | ❌ | ✅ host/port 级 |
| 容器隔离 | ❌ | ✅ 3 种方案 |
| 命令审计 | ❌ | ✅ |

#### 提升方案

**WP-SANDBOX-1: 文件系统白名单**

```go
// internal/sandbox/fs.go
type FSSandbox struct {
    allowedPaths []string  // 允许访问的路径
    deniedPaths  []string  // 禁止访问的路径
    readOnly     bool      // 是否只读
}

func (f *FSSandbox) CheckAccess(path string, mode AccessMode) error {
    // 检查路径是否在白名单内
}
```

**WP-SANDBOX-2: 网络策略**

```go
// internal/sandbox/net.go
type NetPolicy struct {
    allowedHosts []string
    deniedHosts  []string
    allowedPorts []int
}

func (n *NetPolicy) CheckConnection(host string, port int) error {
    // 检查网络连接是否允许
}
```

---

### 2.6 权限模型（差距等级：🟡 中等）

#### Hiq 现状

```typescript
// useController.ts
approve(id, allow, session, persist)
// allow: 本次允许
// session: 本会话允许
// persist: 永久允许

// ApprovalModal.tsx
// 操作：允许 / 允许+记住 / 始终允许 / 拒绝
```

只有工具级粒度，无路径/网络级控制。

#### Codex 做法

```rust
// approval_overlay.rs
enum ApprovalType {
    Exec { command: String, cwd: PathBuf },
    ApplyPatch { changes: Vec<FileChange> },
    Permissions { profile: PermissionProfile },
    McpElicitation { server: String, prompt: String },
}

// 每种类型有不同的 decisions
struct PermissionProfile {
    filesystem: Vec<FileSystemSandboxEntry>,
    network: Vec<NetworkPolicyRule>,
}
```

#### 差距

| 特性 | Hiq | Codex |
|------|----------|-------|
| 审批类型 | 通用 | 4 种专用 |
| 路径级权限 | ❌ | ✅ |
| 网络级权限 | ❌ | ✅ |
| 权限 profile | ❌ | ✅ |
| 审批内 Diff | ❌ | ✅ |
| 命令预览 | ❌ | ✅ bash 高亮 |

（审批 UI 的具体改进见前一份 spec）

---

### 2.7 文本处理（差距等级：🟡 中等）

#### Hiq 现状

使用标准 `<textarea>` + CodeMirror：

```typescript
// Composer.tsx
<textarea
  value={text}
  onChange={e => setText(e.target.value)}
  onKeyDown={handleKeyDown}
/>
```

问题：
- 无 CJK 感知分词
- 无 paste marker（粘贴大段代码时无标记）
- 无 emacs/vim 键绑定
- 无 kill-ring（剪切环）

#### Pi 做法

Pi 有完整的终端文本处理：

```typescript
// editor.ts
// CJK 感知分词
const cjkBreakRegex = /[\u{3000}-\u{9FFF}\u{F900}-\u{FAFF}\u{FE30}-\u{FE4F}]/u;

// Paste marker（粘贴标记）
const PASTE_MARKER_REGEX = /\[paste #(\d+)( (\+\d+ lines|\d+ chars))?\]/g;

// Kill ring（剪切环）
class KillRing {
  push(text: string): void;
  pop(): string;
  rotate(): string;
}

// Undo stack（撤销栈）
class UndoStack {
  push(state: EditorState): void;
  undo(): EditorState;
  redo(): EditorState;
}

// Emacs 键绑定
const emacsKeybindings = {
  "C-a": "beginning-of-line",
  "C-e": "end-of-line",
  "C-k": "kill-line",
  "C-y": "yank",
  // ...
};

// Vim 键绑定
const vimKeybindings = {
  // normal mode
  "h": "cursor-left",
  "j": "cursor-down",
  "k": "cursor-up",
  "l": "cursor-right",
  // ...
};
```

#### 差距

| 特性 | Hiq | Codex | Pi |
|------|----------|-------|-----|
| CJK 分词 | ❌ | 终端原生 | ✅ `Intl.Segmenter` |
| Paste marker | ❌ | ❌ | ✅ 原子标记 |
| Kill ring | ❌ | 终端原生 | ✅ |
| Undo/redo | 浏览器原生 | 终端原生 | ✅ 自定义栈 |
| Emacs 绑定 | ❌ | 终端原生 | ✅ |
| Vim 绑定 | ❌ | 终端原生 | ✅ |
| 词级导航 | ❌ | 终端原生 | ✅ |

#### 提升方案

**WP-TEXT-1: Composer 增强**

```typescript
// lib/editorUtils.ts
// CJK 感知的词级导航
function findWordBoundary(text: string, pos: number, direction: "forward" | "backward"): number {
  // 使用 Intl.Segmenter 进行 CJK 感知分词
  const segmenter = new Intl.Segmenter("zh", { granularity: "word" });
  // ...
}

// Paste marker
function wrapPastedContent(text: string, lineCount: number): string {
  const id = nextPasteId();
  return `[paste #${id} +${lineCount} lines]\n${text}\n[/paste #${id}]`;
}
```

**WP-TEXT-2: 键绑定配置**

```typescript
// lib/keybindings.ts
type KeyProfile = "default" | "emacs" | "vim";

const keybindingProfiles: Record<KeyProfile, KeyBindingMap> = {
  default: { /* 标准绑定 */ },
  emacs: { "Ctrl+a": "home", "Ctrl+e": "end", "Ctrl+k": "kill-line", ... },
  vim: { /* vim 绑定 */ },
};
```

---

### 2.8 键盘系统（差距等级：🟡 中等）

#### Hiq 现状

最小快捷键集：

```typescript
// ApprovalModal.tsx
case "1": handleAllowOnce();
case "2": handleAllowAlways();
case "3": handleDenyOnce();
case "4": handleDenyAlways();

// ToolCard.tsx
// Ctrl+B 展开/折叠 shell 输出
```

#### Codex 做法

完整的 keymap 系统：

```rust
// keymap.rs
struct RuntimeKeymap {
    contexts: HashMap<KeymapContext, Vec<KeyBinding>>,
}

enum KeymapContext {
    Global,
    Approval,
    AgentsOverview,
    FileSearch,
    InputEditor,
}

struct KeyBinding {
    key: KeyEvent,
    action: Box<dyn Fn()>,
    description: String,
}

// 快捷键提示
fn render_shortcut_hints(context: KeymapContext) -> Vec<ShortcutHint>;
```

#### 差距

| 特性 | Hiq | Codex |
|------|----------|-------|
| 快捷键数量 | ~10 | ~50+ |
| 上下文感知 | ❌ | ✅ |
| 快捷键提示 | ❌ | ✅ |
| 可配置性 | ❌ | ✅ |
| 快捷键冲突检测 | ❌ | ✅ |

#### 提升方案

**WP-KEY-1: Keymap Registry**

```typescript
// lib/keymap.ts
interface KeyBinding {
  key: string;           // "Ctrl+Shift+P"
  action: () => void;
  description: string;
  context: "global" | "approval" | "transcript" | "composer";
}

const keymap: KeyBinding[] = [];

export function registerKeybinding(binding: KeyBinding) {
  keymap.push(binding);
}

export function getKeybinding(context: string, event: KeyboardEvent): KeyBinding | undefined {
  return keymap.find(b => b.context === context && matchesKey(b.key, event));
}
```

---

### 2.9 Token 可视化（差距等级：🟡 中等）

#### Hiq 现状

```typescript
// useController.ts
interface State {
  usage?: WireUsage;
  context: ContextInfo;      // { used, window, sessionTokens }
  turnTokens: number;        // 当前轮 token
  turnTotalTokens: number;   // 当前轮总 token
  sessionTokens: number;     // 会话总 token
}
```

有数据但前端展示有限。

#### Codex 做法

```rust
// usage tracking
struct TokenUsage {
    prompt_tokens: u32,
    completion_tokens: u32,
    total_tokens: u32,
    cache_hit_tokens: u32,
    cache_miss_tokens: u32,
}

// 速率限制
struct RateLimit {
    requests_remaining: u32,
    tokens_remaining: u32,
    reset_at: Instant,
}

// 上下文警告
fn check_context_usage(used: u32, window: u32) -> Option<ContextWarning> {
    let ratio = used as f64 / window as f64;
    if ratio > 0.9 {
        Some(ContextWarning::Critical)
    } else if ratio > 0.7 {
        Some(ContextWarning::High)
    } else {
        None
    }
}
```

#### 差距

| 特性 | Hiq | Codex |
|------|----------|-------|
| Token 计数 | ✅ | ✅ |
| 速率限制 | ❌ | ✅ |
| 上下文警告 | ❌ | ✅ |
| 缓存命中率 | ❌ | ✅ |
| 费用估算 | ❌ | ✅ |
| 可视化进度条 | ❌ | ✅ |

#### 提升方案

**WP-TOKEN-1: Token 用量面板**

```typescript
// components/TokenUsage.tsx
function TokenUsageBar({ used, window }: { used: number; window: number }) {
  const ratio = used / window;
  const color = ratio > 0.9 ? "red" : ratio > 0.7 ? "yellow" : "green";
  return (
    <div className="token-usage">
      <div className="token-usage__bar" style={{ width: `${ratio * 100}%`, backgroundColor: color }} />
      <span className="token-usage__text">{used.toLocaleString()} / {window.toLocaleString()}</span>
    </div>
  );
}
```

---

### 2.10 渲染架构（差距等级：🟢 轻微）

#### Hiq 现状

标准 React 组件：

```tsx
// Transcript.tsx
<div className="transcript">
  {items.map(item => {
    switch (item.kind) {
      case "user": return <UserMessage />;
      case "assistant": return <AssistantMessage />;
      case "tool": return <ToolCard />;
      // ...
    }
  })}
</div>
```

#### Codex 做法

`Renderable` trait + 组合模式：

```rust
trait Renderable {
    fn render(&self, area: Rect, buf: &mut Buffer);
}

// 组合
struct Column { children: Vec<Box<dyn Renderable>> }
struct Row { children: Vec<Box<dyn Renderable>> }
struct Inset { child: Box<dyn Renderable>, margins: Insets }

// 响应式
fn display_lines(&self, width: u16) -> Vec<Line<'static>>;
```

#### Pi 做法

Box/Text 组件树：

```typescript
const box = new Box(outputPad, 1, (t) => theme.bg("customMessageBg", t));
box.addChild(new Text("Hello", 0, 0));
box.addChild(new Text("World", 0, 1));
```

#### 差距

Hiq 的 React 渲染已经足够好，差距不大。主要改进点：
- 组件拆分（SettingsPanel 5,342 行、Composer 2,229 行）
- 纯搬移不改逻辑（已在 ui-redesign-spec 中规划）

---

## 三、实施优先级总览

| 优先级 | 编号 | 项目 | 影响 | 工作量 | 依赖 |
|--------|------|------|------|--------|------|
| 🔴 P0 | WP-EXT-1 | 前端 Extension Registry | 高 | 中 | 无 |
| 🔴 P0 | WP-SANDBOX-1 | 文件系统白名单 | 高 | 中 | 无 |
| 🔴 P0 | WP-SANDBOX-2 | 网络策略 | 高 | 中 | 无 |
| 🟡 P1 | WP-ISO-1 | Git Worktree 隔离 | 高 | 大 | 无 |
| 🟡 P1 | WP-MCP-1 | MCP 服务器管理 UI | 中 | 中 | 无 |
| 🟡 P1 | WP-MCP-2 | HTTP/SSE 传输 | 中 | 大 | 无 |
| 🟡 P1 | WP-KEY-1 | Keymap Registry | 中 | 中 | 无 |
| 🟡 P1 | WP-TOKEN-1 | Token 用量面板 | 中 | 小 | 无 |
| 🟡 P1 | WP-TEXT-1 | Composer 增强 | 中 | 中 | 无 |
| 🟡 P1 | WP-TEXT-2 | 键绑定配置 | 小 | 小 | 无 |
| 🟢 P2 | WP-ISO-2 | 容器沙箱 | 高 | 大 | WP-SANDBOX-1 |
| 🟢 P2 | WP-SESS-1 | 对话导出 | 小 | 小 | 无 |
| 🟢 P2 | WP-SESS-2 | 会话分组与标签 | 小 | 中 | 无 |

---

## 四、与前一份 Spec 的关系

| Spec | 聚焦领域 | 状态 |
|------|----------|------|
| `hiq-codex-pi-upgrade-spec.md` | 显示层：Diff、审批、进度、流式 | 规划中 |
| `hiq-beyond-display-upgrade-spec.md` | 架构层：扩展、隔离、MCP、沙箱、文本 | 规划中（本文档） |

两份 spec 互补：
- 前一份解决"看到什么"——让用户在 UI 中看到更好的代码变更和进度信息
- 本文档解决"能做什么"——让 Hiq 具备 Codex/Pi 级的架构能力

**建议实施顺序**：
1. 先做前一份 spec 的 WP-1.1（审批内嵌 Diff）+ WP-1.2（文件摘要）— 最高 ROI
2. 再做本文档的 WP-EXT-1（Extension Registry）— 为后续扩展打基础
3. 然后 WP-SANDBOX-1/2（沙箱）— 安全相关
4. 最后 WP-ISO-1（Worktree）+ WP-MCP-1/2（MCP）— 高级能力

---

## 五、附录：关键源码索引

| 能力 | Hiq 源码 | Codex 参考 | Pi 参考 |
|------|--------------|-----------|---------|
| 工具注册 | `internal/tool/registry.go` | `codex-rs/tui/src/history_cell/` | `pi/packages/coding-agent/src/agent/types.ts` |
| 子Agent | `internal/agent/parallel_tasks.go` | `codex-rs/tui/src/chatwidget/subagents.rs` | N/A |
| MCP | `internal/builtinmcp/` + `internal/acp/` | `codex-rs/tui/src/bottom_pane/mod.rs` | N/A |
| 会话 | `useController.ts` | `codex-rs/tui/src/app/event_dispatch.rs` | `pi/packages/tui/src/session.ts` |
| 权限 | `internal/permission/` | `codex-rs/tui/src/bottom_pane/approval_overlay.rs` | `pi/packages/coding-agent/src/agent/types.ts` |
| 文本编辑 | `Composer.tsx` (textarea) | N/A (终端原生) | `pi/packages/tui/src/components/editor.ts` |
| 键绑定 | 散布在各组件 | `codex-rs/tui/src/keymap.rs` | `pi/packages/tui/src/keybindings.ts` |
| Token | `useController.ts` | `codex-rs/tui/src/chatwidget/` | N/A |

---

> **文档版本**: v1.0
> **最后更新**: 2026-08-21
> **作者**: 基于 Codex / Pi 源码分析自动生成
