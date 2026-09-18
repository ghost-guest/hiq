# hiq 工程规格

> hiq 是一个智能编程 Agent：它是一个轻量级的核心骨架，负责驱动多个大模型，**所有的能力均由配置和插件提供**。本文档是项目的工程契约 —— 代码必须遵循此规范。如需修改，请先修改本契约文档，再修改代码。

## 1. 设计原则

1. **配置与插件驱动的核心。** 核心代码只关心接口。具体的模型和工具通过名称从注册表（registry）中解析、在配置中声明，或由插件注入。绝不允许代码中出现硬编码的 `switch model`。
2. **单一静态二进制文件。** `CGO_ENABLED=0`；支持一键交叉编译；CLI 工具开箱即用。
3. **极简依赖。** 默认使用 Go 标准库。任何引入的第三方依赖必须是纯 Go 实现、轻量级，且绝不能影响项目的单一二进制/跨平台/分发特性。目前唯一接受的外部依赖是 TOML 解析库。
4. **两层扩展体系。** 编译时内置功能（通过 `init()` 自动注册），以及运行时外部插件（通过 stdio JSON-RPC 通信的子进程，兼容 MCP 协议）。
5. **接口优先 & 基于注册表。** `Provider` 和 `Tool` 均为抽象接口。
6. **持续演进，拒绝过度设计。**

语言：**英语是所有代码的首选语言** —— 包括注释、面向用户的字符串、工具描述、系统提示词（Prompt）以及本规格说明。README 采用双语（`README.md` 英文 + `README.zh-CN.md` 中文）。由于本次需求要求中文化，本文档及核心 Markdown 将提供中文版本。

## 2. 目录布局

```
hiq/
├── go.mod / go.sum          # Go 模块文件；仅依赖 BurntSushi/toml
├── Makefile                 # 包含 build / cross / vet / fmt / test 指令
├── README.md / README.zh-CN.md
├── hiq.example.toml    # 示例配置文件
├── docs/SPEC.md             # 本文件
├── cmd/hiq/main.go     # 主入口；通过空白导入加载内置 providers/tools
├── cmd/hiq-plugin-example/  # 参考 MCP stdio 插件实现（一个可运行的示例）
└── internal/
    ├── cli/                 # 子命令路由、flag 解析、组装、退出码处理
    ├── config/              # TOML 配置加载 (按 flag > project > user > defaults 优先级)
    ├── provider/            # Provider 接口 + 类型 + kind→factory 注册表
    │   └── openai/          # 兼容 OpenAI 的实现; init() 自动注册 "openai"
    ├── tool/                # Tool 接口 + 注册表
    │   └── builtin/         # 基础文件与终端工具 (read_file/write_file/edit_file/bash/ls/glob/grep)
    ├── permission/          # 权限拦截层：allow/ask/deny 规则判断
    ├── command/             # 从 .hiq/commands/*.md 加载自定义斜杠命令
    ├── plugin/              # stdio JSON-RPC (MCP) 客户端；负责适配远程工具
    └── agent/               # 会话 Session + Agent 主循环
```

依赖方向（无环）：`cli → {agent, plugin, config} → {tool, provider}`。
内置的子包（如 `provider/openai`, `tool/builtin`）会导入它们的父包以完成自我注册；父包绝不能导入子包。

## 3. 核心抽象

### 3.1 Provider 与注册表 (`internal/provider`)

```go
type Provider interface {
    Name() string
    Stream(ctx context.Context, req Request) (<-chan Chunk, error)
}

// Factory 根据解析后的配置实例构建一个 Provider
type Factory func(cfg Config) (Provider, error)

// Register 在 init() 中调用，注册一个指定 kind 的 Factory (例如 "openai")
func Register(kind string, f Factory)

// New 实例化指定 kind 的 Provider
func New(kind string, cfg Config) (Provider, error)

type Config struct {
    Name    string         // 实例名称, 例如 "deepseek"
    BaseURL string
    Model   string
    APIKey  string
    Extra   map[string]any // 特定 kind 的附加选项
}
```

- `openai` 这种 kind 实现了兼容 OpenAI 的 `/chat/completions` 接口。
- **配置实例不属于代码修改**，比如新增 `kind = "openai"` 的接入，只需修改配置中的 `base_url` / `model` / `api_key_env`，而无需改动代码。
- **一个 provider 代表一个厂商端点**（由一组 `base_url` + `api_key_env` 组成），它可以提供一个或多个模型。配置项中可以声明单个 `model = "..."`，也可以声明一个 `models = ["...", "..."]` 列表（带可选的 `default`）；列表形式允许同一个厂商暴露多个模型而无需重复配置密钥，选择模型时复用同一连接。**模型引用**（如 `default_model`、`--model` 参数、桌面端切换器）会通过 `Config.ResolveModel` 解析，支持接收 provider 名称（→ 对应默认模型）、裸模型名称，或显式的 `provider/model` 格式。`context_window` 和 `price` 等参数是随 provider 配置的，如果不同模型需要独立设置这些值，则应分开作为单个 `model` 配置录入。
- 流式输出的工具调用数据会在 provider 内部按索引累加，只有当解析出完整的 `ToolCall` 时才会抛出给上层。

### 3.2 Tool 与注册表 (`internal/tool`)

```go
type Tool interface {
    Name() string
    Description() string
    Schema() json.RawMessage // 参数的 JSON Schema
    Execute(ctx context.Context, args json.RawMessage) (string, error)
}
```

- 内置工具通过 `init()` 调用 `tool.RegisterBuiltin(t)` 自我注册到全局内置集合中；`tool.Builtins()` 可列出所有内置工具。
- 每次运行时，会组装一个临时的 `*Registry`：包含被启用的内置工具（由配置过滤）**加上** 插件提供的外部工具。Agent 只与这个 `*Registry` 交互。
- `Execute` 方法自行解析原生 JSON 参数。执行错误作为返回值返回，而不会导致程序崩溃 —— Agent 会将错误反馈给模型，让模型进行自我纠错。

### 3.3 插件 (`internal/plugin`) — MCP 客户端

外部插件本质上是在配置中声明的 MCP 服务器。所有情况下的底层通信协议都是 **JSON-RPC 2.0**；唯一的区别是传输层（transport）。`transport` 接口（封装了 `call` / `notify` / `close`）将这一层抽象出来，因此 MCP 层的核心逻辑（握手, `tools/list`, `tools/call` 等）只需编写一次。

- **传输层 Transports** (配置中的 `type`):
  - `stdio` (默认) — 启动本地子进程；子进程的标准输入/输出通过每行一个 JSON 消息进行通信（符合 MCP stdio 规范）。通过 `command` / `args` / `env` 声明；在上下文取消或关机时终止。
  - `http` (又称 `streamable-http`) — 位于 `url` 的远程服务器。每个请求都是 HTTP POST；服务器返回 `application/json`（单次响应）或 `text/event-stream`（包含响应及后续服务器通知的 SSE 流）。如果接收到 `Mcp-Session-Id` 响应头，后续请求会原样回传。静态 `headers`（例如 bearer token）会在每次请求中发送。OAuth 目前不在支持范围内（见 §9）。
  - `sse` — 兼容旧版（2024-11-05）的 HTTP+SSE 传输；能被识别但已延期处理（上游已弃用 —— 建议使用 `http`）。配置该类型将直接返回明确的错误。
- `${VAR}` / `${VAR:-default}` 会在 `command`, `args`, `env`, `url` 和 `headers` 中进行环境变量展开，确保密钥来自系统环境变量而非写死在配置文件中。
- 生命周期：`initialize` → `notifications/initialized` → `tools/list`；通过 `tools/call {name, arguments}` 进行调用。
- 每个远程工具会被适配成标准的 `Tool` 接口，并注入运行时的注册表中，命名空间统一为 `mcp__<server>__<tool>`（空格转为下划线），以确保标准兼容性并避免冲突。
- 工具的 MCP `annotations.readOnlyHint` 会映射到 `Tool.ReadOnly()`。它默认为 false（远程工具是黑盒 —— 我们无法确定其副作用），因此插件可以通过在 `tools/list` 中声明 `readOnlyHint: true`，将工具标记为只读，从而接入权限层的只读自动放行机制，以及参与并行调度。
- `prompts/list` + `prompts/get` 会映射为 `/mcp__<server>__<prompt>` 斜杠命令；`resources/list` + `resources/read` 可以在聊天中通过 `@<server>:<uri>` 进行引用。`/mcp` 命令可显示已连接的服务器及其统计数量。
- `cmd/hiq-plugin-example` 提供了一个可直接运行的 stdio 参考服务器实现（包含 `echo`, `wordcount`），并配有能将其构建为真实二进制文件的端到端测试。

### 3.4 Agent (`internal/agent`)

- `Session` 包含会话历史的 `[]Message`。
- `Run(ctx, input)` 主循环：构建 `Request`（带工具 Schema）→ 调用 `provider.Stream` → 实时打印文本输出，收集完整的工具调用 → 如果没有调用则结束；否则执行每一个工具（内置或插件）并将结果追加回会话 → 循环，受到 `maxSteps` 限制。`ctx` 贯穿始终（Ctrl-C 会中止请求）。
- `Runner` 泛指任何拥有 `Run(ctx, input) error` 方法的对象；`Agent` 和 `Coordinator` 均实现了该接口，所以 CLI 并不需要区分单模型还是双模型模式。

### 3.5 双模型协同 (`Coordinator`)

当 `agent.planner_model` 配置的提供商与主执行器不同时，`Coordinator` 会启动两个模型，并在 **独立的 session** 中运行，以此来保持各自提示词前缀（prompt prefix）缓存的稳定性：

- **规划器 (planner)**（低频运行）在自己的 session 中运行，共享记忆上下文，并拥有一组精简过的只读研究工具，用于产出简明的执行计划。它在规划前可以检查文件/文档，但无法使用写操作或工作流工具。`agent.planner_max_steps` 独立限制其只读探索的轮数，与执行器的 `agent.max_steps` 互不干扰。
- 生成的计划会以结构化文本的形式交接给 **执行器 (executor)** —— 它是一个在自己 session 里拥有完整工具使用权限的 `Agent`，负责贯彻执行该计划。
- 这两个 session 永远不会混用，因此双方的 prompt prefix 不会被对方的轮次打乱；两者都保持“只增不减”的追加模式，极度缓存友好。这就调和了“缓存优先”与“双模型协同”之间的矛盾：如果*在一个共享对话中*不断切换模型，会导致前缀缓存失效，缓存命中率大幅下降，所以我们选择隔离。

### 3.6 上下文管理 (压缩 Compaction)

长时间运行的任务最终会占满模型的上下文窗口。hiq 通过 **低频压缩机制** 来解决这个问题，并且同样遵循缓存优先设计：

- 每个 provider 都会声明自己的 `context_window`（Token 数）。当某一轮返回的 `prompt_tokens` 达到该窗口的 `compactRatio`（默认为 `0.8`）时，执行器会在下一轮开始前触发 **一次** 压缩。
- 压缩操作会将 session 中间较早的历史对话归纳成一段简明的总结报告 —— 仅使用执行器自身的 provider 进行，不带任何工具 —— 并就地替换：整个 session 会变成 `系统设定 + 总结 + recentKeep`（默认为 8）条原封不动的近期消息。压缩边界会向前对齐到某个工具的返回结果处，确保近期保留的尾部历史绝不会以一条“孤儿”工具消息（其对应的 `tool_calls` 被压缩删除了）开头。
- 被删除的原始对话会被归档到 `~/.config/hiq/archive/<timestamp>.jsonl`（每行一条消息），以确保完整历史可追溯。

这是 **唯一** 会改变 prompt prefix 的地方 —— 一个刻意设计的、低频的“缓存重置点”。在两次压缩之间，session 始终保持追加写入，缓存命中率（核心的观测指标）维持在高水位。将配置设为 `context_window = 0` 即可关闭对应实例的压缩功能。

### 3.7 权限系统 (`internal/permission`) — 调用级拦截

智能编程 Agent 具备自主执行 shell 命令和编辑文件的能力。权限层会在 **每次工具调用时** 决定该操作是放行 (allow)、阻断 (deny)，还是需要向用户请求批准 (ask)。权限层独立于模型和 CLI —— Agent 在执行前必须咨询 `Gate` 接口；该门控由静态的 `Policy` 和可选的交互式 `Approver` 组成。

```go
type Decision int            // 权限包
const (Allow Decision = iota; Ask; Deny)

// Policy 根据静态规则评估一个工具调用。无副作用，无 I/O。
type Policy struct { Mode Decision; Allow, Ask, Deny []Rule }
func (p Policy) Decide(toolName string, readOnly bool, args json.RawMessage) Decision
```

- **规则语法.** 一条规则可以是 `Tool`（匹配该工具家族下的所有调用）或者 `Tool(specifier)`（当调用的*主体*匹配该标识符时生效）。Bash 和文件变异工具的授权采用标准工具家族形式，例如 `Bash(npm run build)`、`Bash(npm run test:*)` 和 `Edit(docs/**)`。出于兼容性，传统的全小写工具 ID 和 `tool=literal` 规则依然支持加载。`:*` 后缀代表前缀放行；生成的 Bash 前缀规则还会自动拒绝后续引入的 Shell 管道操作符，例如 `Bash(go test:*)` 并不能授权执行 `go test ./... && rm -rf tmp`。旧的 `Bash(go test *)` 前缀规则依然兼容，但新保存的规则将采用 `Bash(go test:*)` 格式。系统通过一组预知键 —— `command` (bash), `path` / `file_path` (文件修改工具), `pattern` (grep/glob) —— 从 JSON 参数中通用提取主体，工具代码无需调整。如果调用参数无法提取出主体，那么只有裸的 `Tool` 规则能匹配。
- **优先级.** `deny` > `ask` > `allow` > 兜底默认值 (fallback)。默认兜底情况下，只读工具为 `Allow`，写入工具则取决于 `Mode` (默认为 `Ask`)。`deny` 永远最高级，因此一个宽泛的 `allow = ["Bash"]` 依然能被 `deny = ["Bash(rm -rf*)"]` 拦截；反之，`ask` 也能在宽泛的 `allow` 中强制对某个高危子集进行确认拦截。
- **解析 `Ask`.** 交互式前端（终端 TUI）通过 `Approver` 向用户弹出确认提示 —— 选择允许一次 / 本次会话内允许该范围 / 始终允许该范围（持久化保存） / 拒绝。对于 Bash 操作，默认授权范围是具体的完整命令，用户也可以选择退一步授予较保守的前缀授权（例如 `Bash(go test:*)`），这样同一会话或后续运行类似指令时就不会再次弹窗。对于文件写操作，单次会话授权意味着本会话内自由编辑文件；持久化授权则在具备路径信息时，作为 `Edit(<path>)` 路径规则保存，所有内置的变异类文件工具共享此规则。非交互式运行场景（`hiq run`、子代理、无 TTY 等）无法弹窗，它会将所有的 `Ask` 解析为 **允许 (allow)** —— 从而保持其自主运行能力。而在*所有*模式下，`Deny` 都是绝对的阻断：工具绝对不会执行，模型会收到“被拒绝”的反馈，让它自我调整。
- **与计划模式 (Plan mode) 的关系.** 计划模式 (§3.4) 是一种粗粒度的、正交的前置拦截，它无视上述策略直接拒绝*所有*的写操作。权限层则是其底层始终在线的细粒度安全网。
- **用户决策与工具批准的剥离.** 运行时的工具审批向用户呈现三种状态：`ask` ("需要批准")、`auto` ("自动批准") 和 `yolo` ("Yolo批准")。`auto` 状态下，权限策略会对所有的兜底写操作自动放行，但显式的 ask/deny 规则仍会触发；`yolo` 状态则会跳过所有包含写操作和 Bash 在内的工具层审批弹窗。这两种姿态都无法越俎代庖帮用户回答显式的 `ask` 询问或审批 `exit_plan_mode` 的计划放行。自动计划 (Auto-plan) 也是一个独立的特性开关：当开启时，即使用户处于 YOLO 状态，复杂的任务依然可能进入计划模式。用户批准计划后，控制器会开放一个短暂的 `approvedPlanAutoApproveTools` 执行窗口，让模型一口气完成计划中的写操作而无需再受弹窗打扰；但这个短窗口并不会顺带自动批准未来的后续计划。在无头 (headless) 的 `ask` 执行环境中，任何兜底的自动放行都会在日志中被标记为“模型自作主张 (model assumption)”，而不是用户的显式决策。
- **协作模式与工具审批的剥离.** 桌面端 Composer 提供的协作模式分为：`normal` ("正常模式")、`plan` ("计划模式") 和 `goal` ("目标模式")。输入 `/goal <objective>` 会启动一个自主的、会话作用域内的活跃目标：控制器会在缓存稳定的系统 prompt 外围，为用户的输入前置目标上下文，并自动连续派发对话，直到模型报告任务完成、连续 3 次返回同一被阻断状态、被用户人工中止，或触发安全轮次上限。阻断状态的匹配做了大小写、空格与标点的归一化处理，避免细微字眼变化导致审计失效；重新启动 Goal 会开启一轮新的阻断审计。`/goal clear` 可清除当前目标。在桌面 UI 切换进入 plan/normal 模式会隐式清除活动目标，从而确保协作模式仅仅是三选一的功能状态，而底层的工具审批姿态仍被严格保留。

| 工具审批姿态 | 工具放行策略 | 计划放行 | 遇到 `ask` 问题时 |
| --- | --- | --- | --- |
| 需要批准 / `ask` | 遵循权限策略 (`Ask` 会弹窗询问) | 等待用户确认 | 等待用户确认 |
| 自动批准 / `auto` | 写入兜底自动放行；显式 ask/deny 规则依然生效 | 等待用户确认 | 等待用户确认 |
| YOLO 批准 / `yolo` | 工具审批弹窗自动放行 (除非命中 deny 规则) | 等待用户确认 | 等待用户确认 |
| 批准计划后的执行窗口 | 已批准计划中的工具调用自动放行 (除非命中 deny) | 今后的计划仍需等待确认 | 等待用户确认 |

在默认开箱状态下（`mode = "ask"`, 无预设规则），`hiq run` 表现得和以前一样（在无 TTY 时将 `Ask` 视为允许）；而 `hiq chat` 现在会在每一次执行文件写入或 bash 前进行询问。任何 `deny` 规则都会同步强化这两种模式的安全性。

### 3.8 斜杠命令 (`internal/command`)

终端 TUI 支持输入 `/command`。共有三种命令共享同一个分发入口：

- **内置动作** (`/compact`, `/new`, `/clear`, `/effort`, `/mcp`, `/help`) 纯粹在本地操作会话状态，不会发送给模型。`/new` 会开启新会话，同时将上一次的会话存档备用。`/clear` 需要确认，确认后直接丢弃当前上下文且不存档；它不会删除项目层面的通用记忆。
- **自定义命令** 是位于 `.hiq/commands/`（项目级）和 `~/.config/hiq/commands/`（用户级）下的 Markdown 文件；如果同名，项目级覆盖用户级。文件 `review.md` 将映射为 `/review` 命令；子目录会创建命名空间（`git/commit.md` → `/git:commit`）。调用它们会将内容渲染后作为下一轮用户输入发送给模型。
- **MCP Prompts** (§3.3) 映射为 `/mcp__<server>__<prompt>`。

```markdown
---
description: 审查已暂存的 diff
argument-hint: [关注区域]
---
请审查当前暂存的代码差异。重点关注 $ARGUMENTS，列出可能存在的 Bug（需包含 file:line）。
```

- Frontmatter 是一块以 `---` 包裹的可选区域，只需包含简单的 `key: value` 格式；目前解析 `description` 和 `argument-hint`（系统不引入 YAML 解析库 —— 保持极简）。剩下的部分即为模板正文。
- 模板支持以下替换：`$ARGUMENTS` (随命令附带的所有参数，空格拼接)、`$1`…`$N` (按位置获取参数，缺失为空)、`$$` (转义成字面量的 `$`)。参数就是斜杠命令之后跟随的空格分隔单词。
- 命令加载逻辑是纯函数化且经过充分测试的（`command.Load(dirs...)`）；格式错误的命令文件将被静默跳过而不会导致崩溃。自定义命令和 MCP Prompt 都会被解析为纯文本，并复用正常的“开启新一轮对话”的数据流。

#### CLI 模态与输入框的焦点归属

Bubble Tea 终端 TUI 底层仅有一个输入框 (Composer)。斜杠命令或浮层必须声明其是否“独占”键盘输入焦点：

- **模态浮层** 独占导航键/确认/取消等事件，弹出时必须隐藏底部的输入框。示例包括：`/mcp`、`/resume`、`/rewind` 界面、工具审批弹窗以及无需打字的 `ask` 选择卡片。
- **依附输入的浮层** 会贴靠在文本框上方，输入框需保持可见。示例包括：斜杠或 `@` 的自动补全菜单以及允许自由打字的 `ask` 模式。

所有新开发的 CLI 浮层都必须更新 `chat_tui.hideComposer()` 状态管理，并增加/扩展布局测试，以确保 `bottomRows()` 能够正确计算 `面板 + 状态栏` 或 `面板 + 输入框 + 状态栏` 的排版。这避免了未激活的输入框被渲染在模态面板下方产生视觉重叠。

### 3.9 聊天引用 (`@`)

聊天消息中可以嵌入 `@` 引用；在回合发出前，每个引用会被解析并作为带有明确 XML 标签的上下文块，预先附加在消息体中供模型读取。

- `@<server>:<uri>`（其中 `<server>` 为已连接的 MCP 服务器）→ 会映射为 MCP 资源 (`resources/read`)，并使用 `<resource ref="…">…</resource>` 包裹。
- 普通 `@<path>` → 代表 **本地文件或目录**，但前提是**该路径在磁盘上真实存在**。存在性检查是其防错的消歧义机制：一个普通的名字 `@mention` 或是邮箱地址，因找不到对应文件，会直接当作普通文本保留。文件会被包裹成 `<file path="…">…</file>`（且受大小限制拦截，二进制文件仅提示不读取内容）；目录则会变成递归的文件清单列表（深度优先遍历，自动忽略 `.git` 和 `node_modules` 等无关噪点）。
- 引用解析是异步进行的（脱离 TUI 主循环）；获取失败只会在界面上显示一条提示，而不会阻塞对话进程。读取操作是由用户触发且只读的 —— 它们 **不经过** 权限拦截门控 (§3.7)。
- 输入 `/` 或 `@` 会在输入框上方弹出自动补全菜单。`@` 菜单支持 **逐层目录导航**（通过 `os.ReadDir` 步进，绝不一次性递归扫描 —— 避免超大项目卡顿）：进入目录会展示下一级，选中文件则补全路径，MCP 资源也会在顶层展现。底部补全菜单仅在用户进行这些离散操作时才会改变高度，流式输出生成期间不改变高度，确保屏幕上方的历史滚动记录保持丝滑稳定。

## 4. 数据类型定义 (`internal/provider`)

```go
type Role string
const (RoleSystem Role = "system"; RoleUser Role = "user"
       RoleAssistant Role = "assistant"; RoleTool Role = "tool")

type Message struct {
    Role       Role       `json:"role"`
    Content    string     `json:"content,omitempty"`
    ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
    ToolCallID string     `json:"tool_call_id,omitempty"`
    Name       string     `json:"name,omitempty"`
}

type ToolCall   struct { ID, Name, Arguments string }              // Arguments 为原生 JSON 字符串
type ToolSchema struct { Name, Description string; Parameters json.RawMessage }
type Request    struct { Messages []Message; Tools []ToolSchema; Temperature float64; MaxTokens int }

type ChunkType int
const (ChunkText ChunkType = iota; ChunkToolCall; ChunkDone; ChunkError)

type Chunk struct {
    Type     ChunkType
    Text     string    // 仅在 ChunkText 时有效
    ToolCall *ToolCall // 仅在 ChunkToolCall 时有效
    Err      error     // 仅在 ChunkError 时有效
}
```

## 5. 配置体系 (TOML)

配置文件解析优先级：**CLI 启动参数 > 项目配置 `./hiq.toml` > 用户全局配置 `~/.config/hiq/config.toml` > 系统硬编码默认值**。密钥信息通过 `api_key_env` 环境变量注入，永远不应该被明文写入任何配置文件。如果工作区存在 `.env` 文件，系统启动时会自动加载解析。类似 `max_steps` 这种步数偏好通常应设在用户全局配置中；项目的 `hiq.toml` 只在整个代码库需要强制统一步数上限时才覆写它们。

```toml
default_model = "deepseek"   # provider 的名称 (将指向其默认模型) 或者是 "provider/model" 显式格式
# language    = "zh"     # 界面语言环境；为空则根据 $LANG / $HIQ_LANG 自动检测

[agent]
system_prompt = "You are hiq, a coding agent..."  # 或者使用 system_prompt_file = "..." 引入外部文件
max_steps         = 0    # 主执行器允许的工具调用上限；0 代表无限制
planner_max_steps = 12   # 规划器专属的只读工具调用上限；0 代表无限制
temperature       = 0.0
# planner_model = ""   # 可选项: 启用双模型协同 (配置一个低频规划器)
# subagent_model = "deepseek/deepseek-v4-flash"   # 可选: 为 runAs=subagent 的指令指定默认兜底模型
# subagent_models = { review = "deepseek/deepseek-v4-flash", security_review = "deepseek/deepseek-v4-flash" }

# 这是一个厂商端点示例，它在同一个 base_url/key 下暴露了多个模型。
[[providers]]
name           = "deepseek"
kind           = "openai"
base_url       = "https://api.deepseek.com"
models         = ["deepseek-v4-pro", "deepseek-v4-flash"]
default        = "deepseek-v4-pro"   # 可选项; 若缺省则默认采用 models[0]
api_key_env    = "DEEPSEEK_API_KEY"
context_window = 1000000   # Token 窗口上限; agent 会在接近该数值时压缩陈旧历史 (设为 0 表示禁用压缩)

# 这是一个单一模型配置项示例 (当某一模型需要独立的 base_url / context_window / price 时使用)。
[[providers]]

[[providers]]

[tools]
enabled = []   # 省略/为空 = 默认全量开启所有内置工具
bash_timeout_seconds = 120   # 终端前台安全超时上限；设为 0 代表不对工具强制设时

[skills]
# paths = ["~/my-skills", "../shared/skills"]   # 额外加载的自定义技能目录
# excluded_paths = ["~/.agents/skills"]         # 隐藏某些约定俗成的技能目录，但不在磁盘上删除它们
# disabled_skills = ["review"]                  # 被禁用的技能 (不会出现在系统提示词、斜杠补全或工具选项中)

[permissions]
mode  = "ask"                              # 当没有任何规则命中时，写操作的兜底处理: ask|allow|deny
deny  = ["Bash(rm -rf*)", "Bash(git push*)"]   # 绝对阻断的规则 (所有模式下均生效)
allow = ["Bash(go test:*)", "Bash(git status:*)"]  # 自动放行的规则 (永远无需人工确认)
ask   = []                                 # 强制进行人工确认拦截的规则

[sandbox]
# workspace_root = ""          # 文件写工具将被死死限制在此目录层级内；为空 = 限制在当前工作目录 (保证写操作不逃逸出项目外)
# allow_write    = ["/tmp"]    # 除 workspace_root 外，额外开放给写工具读写权限的目录

[[plugins]]
name    = "example"            # type 默认为 "stdio"
command = "hiq-plugin-example"
args    = []
# env   = { FOO = "bar" }

# [[plugins]]                  # 这是一个采用 Streamable HTTP 的远程 MCP 服务器接入示例
# name    = "stripe"
# type    = "http"             # 可选 "stdio" (默认) | "http" | "sse"
# url     = "https://mcp.stripe.com"
# headers = { Authorization = "Bearer ${STRIPE_KEY}" }   # 支持 ${VAR} / ${VAR:-default} 的环境变量安全注入
```

初次运行 `hiq setup` 会生成一份默认配置，以确保 CLI 能够开箱即用。

对于使用其他标准 MCP 客户端的团队，您也可以将项目根目录下的 `.mcp.json` 文件直接放置在此，hiq 会使用标准的 `mcpServers` schemas 完美兼容读取该配置（包括 `command`/`args`/`env`, `type`/`url`/`headers` 以及 `${VAR}` 展开）。它会在读取 TOML 文件后合并至内部的 `[[plugins]]`；若出现同名冲突，则以更明确、属于本项目优先级的 `hiq.toml` 为准。这确保了已经为标准 MCP 客户端配置好的服务器可以“零改动”地在 hiq 中顺滑运行。

```json
{ "mcpServers": {
  "stripe": { "type": "http", "url": "https://mcp.stripe.com",
              "headers": { "Authorization": "Bearer ${STRIPE_KEY}" } }
} }
```

`[sandbox]` 配置代表的是强制*执法层*，而权限系统（permissions）代表的是*策略层*。
Phase 0 阶段已经将所有内置的文件写操作（`write_file`, `edit_file`, `multi_edit`）禁锢在了 `workspace_root`（默认为当前工作区）以及 `allow_write` 配置允许的目录内：任何目标越界的文件写入操作 —— 会被严格解析成消除 symlink 软链接的绝对路径以防止通过 `..` 打洞逃逸 —— 都会被直接拒绝，并将拒绝结果反馈给模型重试。文件沙盒默认开启（root=cwd），确保 AI 不会把系统搞乱；但读取操作是不受限的。在 macOS 下，`bash` 本身更是默认进入系统级的强制沙盒（`[sandbox] bash = "enforce"`, 基于 Seatbelt 技术）：每一条命令都强制在 `sandbox-exec` 下运行，且只有和文件沙盒一模一样的目录（外加工具链的必要缓存）有写权限，只有当配置了 `network = true` 时才能访问外部网络。其他平台因系统底层差异目前暂时不进行进程沙盒包装。“越界重新申请放行”以及 Linux 下的 landlock 支持属于后续的 Phase 1 阶段（见 §9）。

## 6. 异常与错误处理

- 内部库代码统一使用 `fmt.Errorf("...: %w", err)` 往上包装错误并抛出；禁止在底层打印日志或调用 `os.Exit`。
- 只有顶层的 `cli` / `main` 包有权力决定退出码（Exit Codes）和向用户展示具体的错误提示。
- 工具执行时抛出的错误必须反馈给大模型，这些不属于致命崩溃。
- 网络层在遇到 429 或 5xx 等错误时，应当遵循并应用受控的指数退避（Exponential backoff）重试机制（目前接口已预留；实际逻辑可能在后续迭代）。

## 7. 代码规范

- 必须保持 `gofmt` 和 `go vet` 的检查为全绿通过状态；包命名应全小写；导出的标识符必须具有说明性文档注释；所有的注释必须解释*为什么这么做* (Why)，而不是解释代码表面在*做什么* (What)。
- 拒绝过早泛化与过度设计。倾向于最清晰、最直接的代码实现。

## 8. 编译发布

- 本地构建指令: `CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$(VERSION)" -o hiq ./cmd/hiq`
- 跨平台交叉编译矩阵涵盖: `darwin|linux|windows` 乘以 `amd64|arm64`。
- 版本号采用注入机制传递至 ldflags 中 (`git describe --tags --always`)。
- 分发方式包含: 提供预编译独立二进制包 / `go install` / 未来会提供对应的 `brew tap` 源。

## 9. 路线图演进 (不在当前版本范围内)

- Sandbox Phase 1: 真正操作系统级别的 `bash` 沙箱禁锢机制，使得不仅仅是文件写入内置工具 (Phase 0) 受到限制，所有的终端命令都完全被囚禁在工作区之内。**针对 macOS 的技术实现 (Seatbelt 依托于 `sandbox-exec`) 现已合入并默认开启** (参考 §5)。未来待完成：(a) 沙箱逃逸探针 —— 自动拦截沙盒拒绝的权限失败并给予用户弹窗选项，询问是否通过权限网开一面“免沙盒”重试一次（而在 `hiq run` 中则依然判定失败并交由模型自行排查），这补齐了“默认受限，边缘确认”的最佳实践闭环；(b) 针对 Linux 平台的沙盒支持 (采用 bubblewrap / landlock 技术)。实现手段纯依赖 OS 内置方案以便二进制文件继续保持真正的零外部依赖；Windows 由于技术局限暂不规划进程沙盒。一旦该系统完全成型，原来依赖人工审核的权限持久化机制将逐渐退居二线，成为可选手段。
- MCP 协议的长尾特性（目前被刻意推迟 —— 生态圈暂未完全成熟）：包括支持 OAuth 2.0 及用于提供身份验证的 `headersHelper`；兼容剩余部分的 `.mcp.json` 作用域加载 (例如本地级别/用户级别 —— 目前项目中只提供了工程目录级的读取，参考 §5)；工具检索机制的后置；`list_changed` 机制实现的事件推送热更新；渠道适配 / 启发性诱导询问 / Roots 支持等；支持那些不只是提供简单外部工具、而是通过插件提供完整的**大模型 Provider 引擎**接入。
- 支持更加原生的 Generic Provider 抽象以释放更为底层的控制能力（例如原生的 prompt-cache 前缀缓存管理），用以证明我们这套基于接口注册表的体系能从容驾驭超越单一协议格式的通信方案。
- 实现规则沉淀闭环机制：将用户勾选的“永远允许”权限规则实时写回到项目层级的配置文件中；为 `hiq run` 增加通过参数提供会话级别的单次一键全放行 override 配置选项。
