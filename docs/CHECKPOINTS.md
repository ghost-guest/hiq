# 设计：检查点与时光倒流 (Checkpoints & Rewind)

状态：**第一阶段 + 第二阶段已实现** — 快照存储、捕获缝隙、Esc-Esc /
`/rewind` CLI 选择器以及桌面端悬浮回退按钮，并提供了全面的菜单：
恢复代码 / 对话 / 或两者、从此处产生分支、以及总结从此处开始或到此处为止的内容。
基于快照实现并遵循标准规范。可选的基于 git 的回滚模式是剩余的（较低优先级）后续任务。
这解决了 v1 版本中呼声最高的缺失功能——提供编辑安全网 / 撤销机制。

## 目标

允许用户将整个会话回滚到之前的状态，并恢复 **代码**、**对话**，或 **两者兼有** —
且完全不影响他们的 git 历史记录。支持时光倒流功能（Esc-Esc / `/rewind`），
CLI 和桌面端的交互驱动方式保持一致。

## 机制：基于文件快照，而非 git

根据设计，检查点属于 **文件快照**，独立于 git：

- **零 git 污染** — 永远不会提交 (commit)、暂存 (stage) 或修改 `.git/` 目录。
  可以在非 git 初始化的目录下工作。
- **仅跟踪编辑工具的变更** — 即 `write_file` / `edit_file` / `multi_edit`。
  `bash` 产生的副作用 **不会** 被跟踪（无法得知 shell 命令触碰了哪些文件），
  这是有意为之。高风险的 bash 操作已经被权限层拦截。
- 完整的前置编辑内容快照（实现简单；存储空间受下文提到的保留期限制）。

可选的 **基于 git 的模式**（即 v1 的 `auto-git-rollback`）可能作为第二阶段推出，
专为希望获得 git 级别安全性的用户提供；该功能明确不在本阶段考虑范围内。

## 锚点与捕获

- **用户每轮对话一个检查点。** 当一轮对话开始时（`Controller.Send` / `runTurn`），
  开启一个检查点，并以用户的提示词 (prompt) 作为标签。
- **编辑前快照。** 在 `agent.(*Agent).executeOne` 中，在执行一个 `ReadOnly()`
  为 false 并且实现了 `tool.Previewer` 的工具之前，调用 `Preview(args)` →
  `diff.Change{Path, Kind, OldText}`，并将该文件的快照记录到当前活动的检查点中。
  `tool.Previewer` 已经存在，且文件写入类工具均已实现它，因此这是一个中心化的缝隙
  — 不需要修改每个工具的代码。
  - 按路径按轮次去重：只为 **第一次** 触碰记录快照（即该文件在本轮开始时的状态）。
  - `Kind == create`（文件原先不存在）→ 存储 `Content = nil`，这样恢复时会*删除*它。
    `modify`/`delete` → 存储 `OldText`。
  - `bash` 没有实现 `Previewer`，所以它自然被排除了 — 这符合“仅限编辑工具”的契约。

## 数据模型

```go
type FileSnap struct {
    Path    string  // 相对工作区根目录的路径
    Content *string // nil 表示该文件在锚点时不存在（恢复时将删除它）
}

type Checkpoint struct {
    Turn   int        // 此快照锚定的用户消息索引 (从 0 开始)
    Time   time.Time
    Prompt string     // 用户消息文本 — 选择器的标签
    Files  []FileSnap // 在此轮对话期间触碰的独立文件，记录本轮开始时的状态
}
```

## 存储

- **Session 的边车 (Sidecar) 存储**，位于 `config.SessionDir()` 目录下：`<session-id>.ckpt/`
  其中每个检查点保存为一个 JSON 文件，外加一个小型索引（这是 v1 的布局 —
  删除成本低，损坏的快照只会影响自身）。它与消息记录 JSONL（`agent.Session.Save`）
  分开保存，因此 Session 格式保持不变。
- **跨会话持久化** — 恢复一个 session 会重新加载其检查点，因此重启后时光倒流仍然有效（标准行为）。
- **保留策略**：与 session 一起清理（默认 ~30 天，可配置），以限制全量内容快照占用的磁盘空间。

## Controller API（双前端驱动的统一接缝）

检查点功能实现在 `control.Controller` 上，与 `SetPlanMode` / `Compact` /
`NewSession` 并列，因此终端 TUI、桌面端 Webview 和 HTTP/SSE 服务器驱动时光倒流
的方式完全相同，无需重复实现。

```go
type RewindScope int // Code | Conversation | Both

func (c *Controller) Checkpoints() []CheckpointMeta      // 用于选择器
func (c *Controller) Rewind(turn int, scope RewindScope) error
```

- **代码 (Code)**：对于从 `turn` 到最新的每个检查点，提取每个路径最早的 `FileSnap`
  并将每个文件恢复到该内容（若为 `nil` 则删除）— 也就是说，撤销在 `turn` 及之后所做的所有编辑。
  路径越界将在实时的 workspace root 下重新进行安全校验。
- **对话 (Conversation)**：将 `Session.Messages` 截断至第 `turn` 轮用户消息之前，
  重新 `Save`，并将截断后的历史作为事件发出，让前端重新渲染。被截断的那轮用户 prompt
  会恢复到输入框中，以便重新发送或修改（标准行为）。
- **两者 (Both)**：代码 + 对话。

`Rewound` 事件（或复用替换历史的事件）让所有前端都能进行统一的重新渲染。

## CLI 交互体验 (遵循标准规范)

- 在输入框为空时按下 **`Esc Esc`**，或者输入 **`/rewind`**，会打开一个选择器，列出
  每一轮用户对话（时间和它修改了哪些文件）。`chat_tui` 已经支持双击 Esc 的时间追踪。
- 选中某一轮次 → 出现子菜单：**`[代码+对话] [对话] [代码] [取消]`**。
- 如果选择恢复对话或两者兼有，选中的 prompt 会预填入输入框。

## 桌面端交互体验 (遵循 VS Code 扩展规范)

- 聊天记录中的每条用户消息都会有一个悬浮的 **回退 (rewind)** 控件 → 菜单：
  **回退代码 / 回退对话 / 两者 / 从此处产生分支**。
- 它通过 Wails 绑定调用同样的 `controller.Rewind`；控制器的事件流推送恢复后的状态，
  React 随之重新渲染。前端无需处理任何回退逻辑。

## 非目标与边界情况

- **bash / 外部副作用**（如 `rm`, `mv`, 数据库写入, 部署）不会被跟踪 — 
  回退无法撤销它们（标准行为）。
- **在两次对话轮次之间发生外部编辑**：快照保存的是文件在本轮开始时的内容，
  因此恢复时将覆盖在这个期间由 hiq 外部做出的修改。
- **删除操作**：使用编辑工具进行的删除是可以恢复的（快照中有内容）；
  而 `bash rm` 无法恢复。
- **大文件**：目前的策略是全量快照 — 保留期清理策略会控制磁盘占用；
  如果占用成为问题，再考虑去重方案（基于内容寻址的快照）。

## 阶段规划

1. **第一阶段**：快照存储 + `executeOne` 捕获缝隙 + `Controller.Rewind`
   (代码/对话/两者) + CLI 选择器 (Esc-Esc + `/rewind`)。
2. **第二阶段**：桌面端悬浮回退 UI；“从此处产生分支”；“总结从此处开始/到此处为止的内容”；
   可选的基于 git 的回退模式。

## 待确定的问题

- 是否在 `/compact` 和 `NewSession` 边界处进行快照？
- 默认保留窗口期限，以及是否将其暴露在 `[checkpoints]` 配置中。
- 是否从一开始就使用基于内容寻址的去重方案，而不是“每个快照保存一份文件”。
