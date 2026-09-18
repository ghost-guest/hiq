# 参与 hiq 开发贡献

感谢您对参与 hiq 贡献感兴趣！本指南涵盖了您开始贡献所需了解的一切。

有关完整的架构概述和命名规范，请参阅 [hiq.md](./hiq.md)。

## 前置要求

- **Go 1.25+** (工具链 go1.26.4) — 项目使用最新的 Go 稳定版本
- **Git** — 用于版本控制
- **Node.js** (可选) — 仅当您需要开发桌面客户端 (`desktop/`) 时才需要

## 快速开始

```bash
git clone https://github.com/zzycxz/hiq.git
cd hiq
make build    # 编译 CLI 二进制文件
make test     # 运行完整的测试套件
```

## 项目结构

| 目录 | 用途 |
|-----------|---------|
| `cmd/hiq` | CLI 入口点 (极简 — 委托给 `internal/cli` 处理) |
| `cmd/hiq-plugin-example` | 参考 MCP stdio 插件示例 |
| `cmd/e2ebench` | 端到端基准测试套件 |
| `internal/agent` | Agent 循环、会话、协调器、压缩机制、风暴拦截 |
| `internal/cli` | TUI、子命令、配置向导、Markdown 渲染 |
| `internal/control` | 协议无关的控制器 (单层编排) |
| `internal/config` | TOML 配置加载 (命令行参数 > 项目 > 用户 > 默认值) |
| `internal/provider` | Provider 接口 + 注册表 (kind → factory) |
| `internal/provider/openai` | 兼容 OpenAI 格式的 provider (任意兼容端点) |
| `internal/provider/anthropic` | Anthropic Messages API 及其扩展思维支持 |
| `internal/tool` | Tool 接口 + 注册表 |
| `internal/tool/builtin` | 20+ 内置工具 (bash, read/write/edit, glob, grep 等) |
| `internal/plugin` | MCP 客户端 (stdio + Streamable HTTP) |
| `internal/event` | 类型化的事件流 (Sink 接口) |
| `internal/hook` | 钩子拦截 (PreToolUse, PostToolUse, UserPromptSubmit, Stop) |
| `internal/memory` | hiq.md 知识分层 + 自动记忆存储 |
| `internal/skill` | Markdown 技能发现机制 + 内置技能 |
| `internal/sandbox` | 操作系统级沙盒 (macOS 的 Seatbelt) |
| `internal/permission` | 每次调用的权限策略: 允许/询问/拒绝 规则 |
| `internal/checkpoint` | 基于快照的时光倒流功能 |
| `internal/bot` | 多通道 IM 机器人网关 (QQ, 飞书, 微信) |
| `internal/acp` | 代理控制协议服务器 (Agent Control Protocol) |
| `internal/lsp` | LSP 客户端集成 |
| `internal/codegraph` | CodeGraph 集成 (基于 tree-sitter 的代码智能) |
| `internal/i18n` | 国际化 (en + zh) |
| `internal/evidence` | 最终答案生成前的工具回执账本 |
| `internal/fileref` | 文件引用搜索 (@path) |
| `internal/serve` | HTTP/SSE 服务器前端 |
| `internal/notify` | OS 系统通知发送 (平台特有) |
| `internal/command` | 从 Markdown 加载自定义斜杠命令 |
| `internal/diff` | 文件编辑差异计算 |
| `internal/doctor` | 环境诊断/报告 |
| `internal/boot` | 根据配置进行引导加载/组装 |
| `internal/jobs` | 后台任务管理器 |
| `internal/instruction` | 项目指令 + 验证检查 |
| `internal/installsource` | 从 URL 安装插件/技能 |
| `internal/fileutil` | 原子写入、编码检测 |
| `internal/frontmatter` | 技能/命令的 Frontmatter 解析 |
| `internal/inspect` | 模型响应结果审查 |
| `internal/mcpdiag` | MCP 鉴权诊断 |
| `internal/netclient` | HTTP 客户端抽象层 |
| `internal/nilutil` | 接口 nil 防御 |
| `internal/outputstyle` | 输出风格/角色人设配置 |
| `internal/proc` | 进程管理 (平台特有) |
| `internal/sysproxy` | 系统代理检测 |
| `desktop/` | 基于 Wails 的桌面应用 (独立的 Go 模块) |
| `npm/` | npm 分发包装器 |
| `site/` | 基于 Astro 的项目网站 |
| `workers/crash-report/` | 用于崩溃报告的 Cloudflare Worker |
| `docs/` | 工程规范、指南、迁移文档 |
| `benchmarks/` | 端到端基准测试任务集 |

### 依赖方向

```
cli → {agent, plugin, config} → {tool, provider}
```

内置的子包通过 `init()` 引入其父包以完成自我注册。
父包绝对不能导入子包。

## 开发工作流

### 编译构建

```bash
make build          # go build — 输出到 bin/ 目录
make test           # go test ./...
make vet            # go vet ./...
make fmt            # gofmt -w .
make hooks          # 安装 git hooks (pre-push: go vet)
make cross          # 交叉编译到 6 个不同的目标平台
```

### 运行测试

```bash
go test ./...                           # 运行所有测试
go test ./internal/agent/ -v            # 详细输出，运行单个包
go test ./internal/tool/builtin/ -run TestGrep  # 运行单个特定测试
```

### 代码风格

- CI 将强制执行 `gofmt` — 请在提交前格式化代码
- 遵循现有模式：使用 `fmt.Errorf("...: %w", err)` 包装错误
- 库级别的代码绝对不能调用 `os.Exit` 或向 stdout/stderr 打印日志
- 只有 `cli/` 和 `main/` 层负责决定退出码和面向用户的信息
- 导出的标识符必须包含文档注释
- **代码中主要使用英语** — 包括注释、面向用户的字符串、工具描述、系统 prompt 等。中文仅允许出现在国际化 (i18n) 消息文件 (`messages_zh.go`) 以及配置文件的示例注释中。

### 提交信息规范

请遵循 [Conventional Commits (约定式提交)](https://www.conventionalcommits.org/):

```
feat(glob): add ** recursive pattern support
fix: replace silent error discards with structured logging
test(event): add comprehensive unit tests for event package
docs: add CONTRIBUTING.md
ci: add golangci-lint and govulncheck
```

## 添加新的内置工具

1. 创建 `internal/tool/builtin/mytool.go`
2. 实现 `tool.Tool` 接口: `Name()`, `Description()`, `Schema()`, `ReadOnly()`, `Execute()`
3. 通过 `func init() { tool.RegisterBuiltin(myTool{}) }` 注册
4. 在 `internal/tool/builtin/mytool_test.go` 中添加测试
5. 该工具将自动可用 — `main` 会使用空白导入 (`_`) 引入 `builtin` 包

## 添加新的模型 Provider

(对于 MCP 工具服务器请参考 `internal/plugin` — 这是不同的抽象层。)

1. 创建 `internal/provider/myprovider/`
2. 实现 `provider.Provider`: `Name()`, `Stream()`
3. 通过 `func init() { provider.Register("mykind", New) }` 注册
4. 该 Provider 将可通过配置项 `kind = "mykind"` 使用

## 添加国际化 (i18n) 字符串

1. 在 `internal/i18n/i18n.go` (`Messages` 结构体) 中添加字段
2. 在 `internal/i18n/messages_en.go` 和 `messages_zh.go` 中提供具体的文案
3. 如果您漏掉了某个语言环境，`TestCatalogsComplete` 测试将会报错

## 添加机器人频道 (Bot Channel)

1. 在 `internal/bot/mychannel/` 中创建频道专属适配器
2. 在 `internal/bot/gateway.go` 中注册
3. 在 `hiq.example.toml` 中添加配置说明
4. 为频道特有的消息添加 i18n 字符串

## 提交更改

1. Fork 本代码库
2. 基于 `main` 分支创建一个特性 (feature) 分支
3. 编写代码并补充相应的测试
4. 确保 `go test ./...` 能够全部通过
5. 确保执行 `gofmt -l .` 后没有格式差异
6. 提交 Pull Request 至 `main` 分支

## 提交 Issue 报告

在 GitHub 上提出 Issue 时，请包含：
- 复现步骤
- 预期行为 vs 实际行为
- Go 版本和操作系统类型
- 相关的日志或错误信息

## 许可协议

一旦您参与贡献，即表示您同意您的所有贡献代码都将采用与本项目相同的 MIT 许可证发布。
