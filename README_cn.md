<p align="center">
  <img src="docs/assets/hiq_banner_wide.png" alt="hiq" />
</p>

<p align="center">
  <a href="./README.md">English</a>
  &nbsp;·&nbsp;
  <strong>简体中文</strong>
  &nbsp;·&nbsp;
  <a href="./docs/GUIDE.zh-CN.md">使用指南</a>
  &nbsp;·&nbsp;
  <a href="./docs/SPEC.md">架构规范</a>
</p>

<p align="center">
  <a href="https://github.com/zzycxz/hiq/releases"><img src="https://img.shields.io/badge/version-v0.1.0-0153e5?style=flat-square" alt="Version 0.1.0"/></a>
  <a href="https://github.com/zzycxz/hiq/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/zzycxz/hiq/ci.yml?style=flat-square&label=ci&labelColor=161b22&logo=githubactions&logoColor=white" alt="CI"/></a>
  <a href="./LICENSE"><img src="https://img.shields.io/github/license/zzycxz/hiq.svg?style=flat-square&color=8b949e&labelColor=161b22" alt="license"/></a>
  <a href="https://github.com/zzycxz/hiq/stargazers"><img src="https://img.shields.io/github/stars/zzycxz/hiq.svg?style=flat-square&color=dbab09&labelColor=161b22&logo=github&logoColor=white" alt="GitHub stars"/></a>
</p>

<br/>

<h3 align="center">全场景通用 AI 编程助手 + 办公自动化平台。</h3>
<p align="center">
  对接 18 家供应商（11 直连 + 7 Coding Plan），300+ 模型。<br/>
  内置 Word/Excel/PPT 办公自动化能力。<br/>
  单一静态 Go 二进制，零运行时依赖，多平台无缝覆盖。
</p>

<br/>

## hiq 是什么？

hiq 是一款通用 AI 智能编程助手，以高度可配置化和 MCP 插件体系为核心驱动力。
它不仅提供强大的本地代码理解能力，更能深度接入主流大模型（如 DeepSeek、Qwen、GLM、Kimi、GPT、Claude 等 300+ 模型）实现自然语言驱动的自主编程。

Agent 可以在 **终端**（TUI）、**桌面客户端**（基于 Wails）、**HTTP/SSE 服务器** 或 **多通道 IM 机器人**（企业微信 / 飞书 / QQ）等全场景中运行——所有前端均由同一个高性能、传输无关的核心引擎驱动。

> **开源声明：** 本项目基于 [DeepSeek-Reasonix](https://github.com/esengine/DeepSeek-Reasonix) 进行二次开发，
> 针对通用多模型场景与企业级需求进行了深度的架构优化与扩展。

## 核心特性

### 工程化与生态

- **通用 Provider 架构** — 统一对接任意 OpenAI / Anthropic 兼容端点，支持 thinking mode 协议、reasoning_content 回传、11 个直连厂商 + 7 个聚合平台（Coding Plan），通过 `hiq.toml` 完全配置驱动。
- **MCP 插件生态** — 全面支持 Model Context Protocol (MCP)，外部工具以子进程形式通过 stdio / HTTP 运行，无限扩展 Agent 能力。
- **内置 Web Search** — 集成 Brave → Exa → Linkup → AnySearch 四引擎链式降级搜索，无需外部 MCP 即可联网检索。
- **极速轻量分发** — `CGO_ENABLED=0` 单二进制打包，极简部署，支持交叉编译 6 大操作系统架构。

### 内置智能工具箱

原生集成精简 IDE 级工具链：`bash` · `read_file`（兼并目录列表） · `write_file` · `edit_file` · `grep`（支持超时） ·
`web_fetch` · `web_search` · `todo_write` · `complete_step` ·
`codegraph_*`（基于 tree-sitter 的项目级符号与调用图谱精准搜索）。
领域能力（浏览器/桌面自动化/邮件/知识库/文档/日程/专家团队）封装为 subagent skill，按需通过 `run_skill` 调用。

### Office 自动化

- **Word 文档** — 创建、编辑、图片插入、目录生成、格式控制
- **Excel 表格** — 创建、编辑、图表（柱状图/折线图/饼图/散点图）、条件格式
- **PPT 演示** — SVG 自由设计、模板填充、动画效果、过渡效果、VLM 集成
- **邮件集成** — 多账号支持（Gmail/Outlook/QQ/163 等）、定时发送
- **日历任务** — cron 调度、ICS 格式、定时提醒

### 截图解题助手

- **一键截屏解题** — 按热键（Ctrl+Shift+Alt+W）自动识别题目、给出答案、验证结果
- **IM 推送** — 解题结果通过飞书/微信推送，适合考试/做题场景
- **可自定义提示词** — 自定义解题风格和输出格式

### 独家代码智能

- **CodeGraph 引擎** — 基于 tree-sitter + SQLite 构建本地化轻量级代码图谱。零 API 调用开销，后台静默建立 AST 索引，实现精准的方法调用与符号追踪。
- **全栈 LSP 集成** — 与主流语言服务器深度绑定，提供诊断、跳转定义与交叉引用能力。

### 自主智能与自进化

- **Goal 独立 Judge** — 目标达成评估由独立 LLM 模型执行（基于 transcript 证据，temperature=0），防止代理乐观停止。
- **Dream / Distill 自进化** — Dream（7 天周期）自动沉淀会话知识到项目记忆；Distill（30 天周期）自动发现重复工作流并打包为可复用 Skill。
- **Memory Archive 软删除** — 记忆删除后移至 `.archive/` 目录，可追溯恢复，不再永久丢失。
- **Profile 隔离记忆** — dev/cowork 模式记忆目录完全隔离，切换模式不丢失积累。用户偏好和反馈指导记忆在同模式所有项目间共享。

### 安全与可靠性

- **权限系统 subject 评估** — 写工具（`write_file`/`edit_file`）和不可逆操作（`email_send`/`rag_delete`）的 subject 路径经 glob 匹配审批，deny 规则不会被绕过。
- **Checkpoint 路径穿越防护** — `safePath` 使用 `filepath.IsLocal` 显式拒绝 `..`、UNC 路径等逃逸向量。
- **Memory store 路径防护** — `safeJoin` 防止通过 `remember` 工具注入路径穿越攻击。
- **Summarizer 超时保护** — 90 秒超时防止 LLM 流式卡死导致 compaction 永久阻塞。
- **Transient 401 重试** — 网关偶发认证失败自动重试，减少虚假会话中断。
- **检查点与时光倒流** — 引入代码修改快照系统，支持 `/rewind` 一键撤销，提供极致的容错安全网。

### 规划驱动模式

- **Plan Mode** — 自动拦截高危操作，Agent 在执行文件修改或敏感 Shell 命令前需提交"执行规划"并等待人工签核。
- **Evidence-Backed 完成** — 每个计划步骤必须引用证据（验证命令、diff、文件路径），防止代理声称完成而无实际产出。
- **PlanModeFromContext** — 工具可自查是否在 plan mode 下运行，条件性禁用写入相关界面。

## 全场景接入

| 前端形态 | 启动命令 | 场景说明 |
|------|------|------|
| **终端 TUI** | `hiq chat` | 极客首选：沉浸式终端界面（基于 Charm Bubble Tea） |
| **API 服务** | `hiq serve` | 开放能力：提供标准 HTTP/SSE 编程接入接口 |
| **桌面客户端** | Wails 图标启动 | UI 交互：提供原生 macOS / Windows / Linux 多标签体验 |
| **企业机器人** | `hiq bot start` | 团队协作：企业微信 / 飞书 / QQ 等 IM 网关接入 |
| **ACP 服务** | `hiq acp` | 协议桥接：Agent Control Protocol 远程控制层 |

## 安装指南

当前版本：**v0.1.0**

```sh
npm i -g hiq                        # 任意系统——自动拉取对应平台的原生二进制
brew install zzycxz/hiq/hiq    # macOS 用户
```

您也可以在 [GitHub Releases](https://github.com/zzycxz/hiq/releases) 获取预编译归档文件（支持 `darwin|linux|windows × amd64|arm64`）。

> **⚠️ macOS 桌面版安装必读：**
> 如果您下载了 `.zip` 格式的 macOS 桌面端应用，由于这是开源项目未进行 Apple 开发者签名，解压后双击运行可能会提示 **"App is damaged and can't be opened"（文件已损坏，请移至废纸篓）**。
>
> **解决办法：** 打开终端，运行以下命令解除隔离保护（假设 App 在下载目录）：
> ```sh
> xattr -cr ~/Downloads/hiq.app
> ```
> 然后即可正常双击运行。

### 从源码编译

```sh
make build    # 编译到 bin/ 目录
make cross    # 交叉编译至 dist/（生成 6 个目标平台二进制）
```
*(需安装 Go 1.25+)*

## 快速上手与配置

```sh
hiq setup                       # 启动配置向导 → 生成 ./hiq.toml
export DEEPSEEK_API_KEY=your-key     # 设置模型 API Key (或写入 .env)
hiq chat                        # 进入交互终端，输入 /init 生成项目上下文
hiq run "实现 main.go 里的所有 TODO"
hiq run --model deepseek/deepseek-v4-flash "补充单元测试"
echo "解释这段代码" | hiq run
```

## 接入模型 Provider

hiq 不绑定任何模型平台：通过统一的 Provider 抽象接入 18 家供应商（11 直连 + 7 Coding Plan）。完整模板见 [`hiq.example.toml`](./hiq.example.toml)。

### 直连供应商（11 家）

| 供应商 | Base URL | 默认模型 |
|--------|----------|----------|
| 通义千问 | `https://dashscope.aliyuncs.com/compatible-mode/v1` | qwen3.7-max |
| DeepSeek | `https://api.deepseek.com` | deepseek-v4-pro |
| 火山引擎 | `https://ark.cn-beijing.volces.com/api/v3` | doubao-seed-evolving |
| 智谱 AI | `https://open.bigmodel.cn/api/paas/v4` | glm-5.2 |
| MiniMax | `https://api.minimaxi.com/v1` | minimax-m3 |
| Moonshot | `https://api.moonshot.cn/v1` | kimi-k3 |
| MiMo | `https://api.xiaomimimo.com/v1` | mimo-v2.5-pro |
| 阶跃星辰 | `https://api.stepfun.com/v1` | step-3.7-flash |
| 讯飞 MaaS | `https://spark-api-open.xf-yun.com/v1` | glm-5.2 |
| Anthropic | `https://api.anthropic.com` | claude-sonnet-5 |
| OpenAI | `https://api.openai.com/v1` | gpt-5.6-terra |

### Coding Plan 聚合平台（7 个）

| 平台 | Base URL | 说明 |
|------|----------|------|
| 通义千问 Coding | `https://coding.dashscope.aliyuncs.com/v1` | 聚合 qwen/kimi/glm/minimax |
| 智谱 GLM Coding | `https://open.bigmodel.cn/api/coding/paas/v4` | GLM 系列 |
| 火山引擎 Coding | `https://ark.cn-beijing.volces.com/api/coding/v3` | 聚合 doubao/deepseek/kimi |
| 百度千帆 | `https://qianfan.baidubce.com/v2/coding` | 聚合 ernie/glm/kimi/deepseek |
| 腾讯云 TokenHub | `https://api.lkeap.cloud.tencent.com/coding/v1` | 聚合 deepseek/glm/kimi/minimax |
| 阶跃星辰 Step Plan | `https://api.stepfun.com/step_plan/v1` | step 系列 |
| 讯飞 MaaS Coding | `https://maas-token-api.cn-huabei-1.xf-yun.com/v2` | 统一模型 ID |

### 第一步：获取模型 API Key
以 DeepSeek 为例（其余厂商同理）：前往对应模型平台官网注册并创建 API Key。

### 第二步：配置环境变量
```sh
# Linux / macOS
export DEEPSEEK_API_KEY="您的真实密钥"

# Windows (PowerShell)
$env:DEEPSEEK_API_KEY = "您的真实密钥"
```

### 第三步：配置 Provider (`hiq.toml`)
在项目根目录创建或修改 `hiq.toml`：

```toml
default_model = "deepseek"

[[providers]]
name        = "deepseek"
kind        = "openai"
base_url    = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"
default     = "deepseek-v4-pro"
fast_model  = "deepseek-v4-flash"
models      = ["deepseek-v4-pro", "deepseek-v4-flash"]
```

完成配置后，只需执行 `hiq chat`，即可开始体验大模型的智能编程赋能。

> **💡 进阶技巧：定制 AI 身份与规范**
> 如果你想让 AI 更懂你们团队的开发规范，可以在项目根目录创建或修改 `hiq.md`，写上你的专属规则和身份声明。AI 会在每次对话时自动读取并遵循这些设定。

### 推荐模型

在 `model` 字段中，支持使用 `provider/模型名` 灵活切换。常用推荐：

| 模型 ID | 核心优势 | 适用场景 |
|---------|------|------|
| `deepseek/deepseek-v4-pro` | 综合能力强大，原生多模态 | 核心架构设计、复杂需求分析、主力编码 |
| `qwen/qwen3.7-max` | 1M 上下文，支持 thinking | 长上下文分析、复杂架构设计 |
| `zhipu/glm-5.2` | 开源 SOTA，1M 上下文 | 通用编码、跨模块重构 |
| `deepseek/deepseek-v4-flash` | 极速响应，代码专精 | 代码片段补全、快速重构、单元测试生成 |

> hiq 支持 18 家供应商、300+ 模型。详见 [`hiq.example.toml`](./hiq.example.toml) 的完整配置模板。只需修改 `default_model` 字段即可无缝切换，零代码侵入。

## 💰 赞助支持

如果您觉得 Hiq 对您有帮助，欢迎赞助我们。您的支持将用于：
- 🤖 购买 token 用于开发和测试
- 🚀 功能开发和维护
- 📚 文档改进

### PayPal

[![PayPal](https://img.shields.io/badge/PayPal-赞助-blue?style=flat-square&logo=paypal)](https://www.paypal.com/paypalme/zzycxz)

### 支付宝

<img src="docs/assets/zhifubao.jpg" alt="支付宝收款码" width="200"/>

## 文档指引

| 参考文档 | 涵盖内容 |
|------|------|
| **[使用指南](./docs/GUIDE.zh-CN.md)** | 权限控制、沙盒运行、MCP 插件、终端斜杠命令、`@` 语法、Plan 模式、后台模型 |
| **[架构规格](./docs/SPEC.md)** | 工程契约：系统架构、Registry 机制、数据类型约束与长期路线图 |
| **[功能特性](./docs/HIQ_FEATURES.md)** | 完整功能全景图、架构图、能力矩阵 |
| **[快照机制](./docs/CHECKPOINTS.md)** | 基于文件快照的代码修改安全网设计 |
| **[Session 架构](./docs/SESSION_REFERENCE_ARCHITECTURE.md)** | 会话生命周期管理、状态持久化与无缝恢复机制 |
| **[RAG 知识库指南](./docs/RAG_GUIDE.md)** | 文档导入、知识图谱、实体提取、语义搜索、@引用 |
| **[专家团指南](./docs/EXPERT_GUIDE.md)** | 多模型协作、团队配置、协作模式、会话历史 |
| **[办公自动化指南](./docs/OFFICE_GUIDE.md)** | Word/Excel/PPT 自动化、邮件集成、日历任务、定时任务 |
| **[贡献指南](./CONTRIBUTING.md)** | 开发者必读：如何添加新工具、新 Provider 以及定制机器人通道 |
| **[更新日志](./CHANGELOG.md)** | 历史发版记录与功能迭代 |

## 核心架构图

```
Developer → CLI / Desktop / HTTP / Bot / ACP
             ↓
             control.Controller  (传输协议无关的会话驱动层)
             ↓
             agent.Agent         (ReAct 循环核心: 思考流 → 工具调度 → 结果解析 → …)
             ↓
             provider.Provider   (对接任意 OpenAI/Anthropic 兼容大模型)
             tool.Registry       (执行器沙盒：内置 Native 工具 + MCP 外挂插件)
```

项目包含 40 余个严格解耦的 Internal 包，依赖图谱遵循严格的无环单向流动：
`cli → {agent, plugin, config} → {tool, provider}`

---

<p align="center">
  <sub>MIT License —— 详情参见 <a href="./LICENSE">LICENSE</a> 文件。</sub>
</p>
