<p align="center">
  <img src="docs/assets/hiq_banner_wide.png" alt="hiq" />
</p>

<p align="center">
  <strong>English</strong>
  &nbsp;·&nbsp;
  <a href="./README_cn.md">简体中文</a>
  &nbsp;·&nbsp;
  <a href="./docs/GUIDE.md">User Guide</a>
  &nbsp;·&nbsp;
  <a href="./docs/SPEC.md">Architecture Spec</a>
  &nbsp;·&nbsp;
  <a href="#-acknowledgements">Acknowledgements</a>
</p>

<p align="center">
  <a href="https://github.com/ghost-guest/hiq/releases"><img src="https://img.shields.io/badge/version-v0.1.11-0153e5?style=flat-square" alt="Version 0.1.11"/></a>
  <a href="https://github.com/ghost-guest/hiq/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/ghost-guest/hiq/ci.yml?style=flat-square&label=ci&labelColor=161b22&logo=githubactions&logoColor=white" alt="CI"/></a>
  <a href="./LICENSE"><img src="https://img.shields.io/github/license/ghost-guest/hiq.svg?style=flat-square&color=8b949e&labelColor=161b22" alt="license"/></a>
  <a href="https://github.com/ghost-guest/hiq/stargazers"><img src="https://img.shields.io/github/stars/ghost-guest/hiq.svg?style=flat-square&color=dbab09&labelColor=161b22&logo=github&logoColor=white" alt="GitHub stars"/></a>
</p>

<br/>

<h3 align="center">Universal Multi-Vendor AI Coding Assistant + Office Automation Platform.</h3>
<p align="center">
  Connect to 18 vendors (11 direct + 7 Coding Plan aggregators), 300+ models.<br/>
  Built-in Word/Excel/PPT office automation capabilities.<br/>
  Single static Go binary, zero runtime dependencies, seamless cross-platform coverage.
</p>

<br/>

## What is hiq?

hiq is a **universal AI coding assistant** that supports 11 direct vendors and 7 Coding Plan aggregator platforms, providing complete office automation capabilities.

It is a **derivative work (二开) of [fairpeer](https://github.com/zzycxz/fairpeer)**, which itself is a derivative work of **[Reasonix / DeepSeek-Reasonix](https://github.com/esengine/DeepSeek-Reasonix)**. hiq keeps the upstream engineering architecture (transport-agnostic `control.Controller` → `agent.Agent` ReAct loop → pluggable `provider.Provider` / `tool.Registry`), and continues it as a rebranded, independently maintained distribution with its own desktop app, memory subsystem, project knowledge hub, expert teams, and live usage telemetry.

The same core engine drives every front end — **terminal (TUI)**, **desktop client (Wails + React)**, **HTTP/SSE server**, **IM bots** (WeCom / Feishu / QQ), and **ACP** — so behaviour, tools and permissions stay identical no matter where you talk to it.

### Core Features

- 🌐 **Multi-Vendor Support** — 18 vendors (11 direct + 7 Coding Plan aggregators), 300+ models, 100% config-driven
- 💻 **Code Editing** — 5 editing strategies + 5-level fuzzy matching + checkpoint rollback (`/rewind`)
- 📄 **Office Automation** — Full Word / Excel / PPT pipelines, mail integration, calendar tasks
- 🧠 **Smart Memory** — Two-layer long-term memory + Dream / Distill self-evolution + RAG knowledge base
- 🎯 **Task Orchestration** — Auto-plan, Goal mode with independent Judge, Expert Teams with HITL approvals
- 🔧 **Plugin System** — Markdown Skills + MCP plugins (stdio / HTTP)
- 🕸️ **CodeGraph Engine** — tree-sitter + SQLite local code graph, zero API cost symbol & call tracing
- 📊 **Live Usage Telemetry** — real-time tokens/s estimation and cache hit-rate beside the composer
- 📷 **Screenshot Solver** — One-hotkey exam assistant: AI reads the screen and answers

---

## 🙏 Acknowledgements

hiq would not exist without the open-source work of the projects below. **Sincere thanks to all of them.**

<table>
<tr>
<td width="50%" valign="top">

### [fairpeer](https://github.com/zzycxz/fairpeer)

**The direct base of this project.** hiq is a second-development (二开) fork of fairpeer, which extended the original engine into a universal multi-vendor AI coding assistant with built-in Word/Excel/PPT automation, RAG knowledge base, smart memory and screenshot solving across terminal, desktop, API and IM bot front ends.

Almost all of hiq's foundational engineering — the Provider abstraction, MCP plugin ecosystem, office automation toolchain, checkpoint system, memory and RAG design, and the whole `internal/` package layout — originates from fairpeer. Thank you, **[@zzycxz](https://github.com/zzycxz)**.

</td>
<td width="50%" valign="top">

### [Reasonix / DeepSeek-Reasonix](https://github.com/esengine/DeepSeek-Reasonix)

**The original upstream.** fairpeer (and therefore hiq) is forked from Reasonix — the engine behind the transport-agnostic agent core: the ReAct loop, the `control.Controller` session driver, the tool registry sandbox and the event-stream architecture that everything else is built on.

Every architectural decision hiq inherits traces back here. Thank you to the Reasonix authors.

</td>
</tr>
</table>

**Lineage** (each step under the MIT license):

```
Reasonix  ──fork──▶  fairpeer  ──二开 / fork──▶  hiq
(engine core)        (multi-vendor + office      (rebrand, desktop app, memory,
                      automation expansion)       knowledge hub, expert teams)
```

**What hiq itself adds on top of the base:** the `hiq` rebrand across app art / exe icon / docs, the Wails v2 + React 19 desktop distribution (single-file portable build), the tiered memory subsystem (`l1/l2/l3` + prompt-index injection), the project knowledge hub (`internal/projectkb`), expert-team runtime hardening (per-team concurrency pool, shared-context archiving, checkpoints, HITL approvals), usage statistics with truncation observability, and the live tok/s + cache readout in the composer.

If you find this project useful, please also consider supporting the upstream projects above. 🙏

---

## 🚀 Quick Start

### Download the desktop app (recommended)

Portable, self-contained build — no runtime dependencies:

**[⬇️ Download hiq-desktop-v0.1.11.zip](https://github.com/ghost-guest/hiq/releases/download/v0.1.11/hiq-desktop-v0.1.11.zip)** (Windows x64, ~118 MB)

Unzip anywhere and run `hiq-desktop.exe`. Config and data live in `%APPDATA%\hiq` on Windows, so the app is fully portable.

### Install the CLI

```bash
# macOS / Linux
curl -fsSL https://hiq.dev/install.sh | bash

# Windows (PowerShell)
irm https://hiq.dev/install.ps1 | iex

# Or build from source
git clone https://github.com/ghost-guest/hiq.git
cd hiq && go build -o hiq ./cmd/hiq
```

> **⚠️ macOS note:** pre-built `.zip` desktop apps are unsigned (no Apple Developer certificate). If macOS reports *"App is damaged and can't be opened"*, clear the quarantine flag:
> ```sh
> xattr -cr ~/Downloads/hiq.app
> ```

### Configuration

```bash
hiq setup              # interactive wizard → writes ./hiq.toml
export DEEPSEEK_API_KEY=your-key   # or put it in .env
vim ~/.config/hiq/hiq.toml         # global config
```

### Run

```bash
hiq chat                     # interactive terminal UI
hiq run "Implement all TODOs in main.go"
hiq run --model deepseek/deepseek-v4-flash "Add unit tests"
echo "explain this code" | hiq run
```

---

## 🌐 Supported Vendors

### Direct Vendors (11)

| Vendor | Base URL | Default Model |
|--------|----------|---------------|
| Qwen | `https://dashscope.aliyuncs.com/compatible-mode/v1` | qwen3.7-max |
| DeepSeek | `https://api.deepseek.com` | deepseek-v4-pro |
| Volcengine | `https://ark.cn-beijing.volces.com/api/v3` | doubao-seed-evolving |
| Zhipu AI | `https://open.bigmodel.cn/api/paas/v4` | glm-5.2 |
| MiniMax | `https://api.minimaxi.com/v1` | minimax-m3 |
| Moonshot | `https://api.moonshot.cn/v1` | kimi-k3 |
| MiMo | `https://api.xiaomimimo.com/v1` | mimo-v2.5-pro |
| StepFun | `https://api.stepfun.com/v1` | step-3.7-flash |
| iFlytek MaaS | `https://spark-api-open.xf-yun.com/v1` | glm-5.2 |
| Anthropic | `https://api.anthropic.com` | claude-sonnet-5 |
| OpenAI | `https://api.openai.com/v1` | gpt-5.6-terra |

### Coding Plan Aggregators (7)

| Platform | Base URL | Description |
|----------|----------|-------------|
| Qwen Coding | `https://coding.dashscope.aliyuncs.com/v1` | Aggregates qwen/kimi/glm/minimax |
| Zhipu GLM Coding | `https://open.bigmodel.cn/api/coding/paas/v4` | GLM series |
| Volcengine Coding | `https://ark.cn-beijing.volces.com/api/coding/v3` | Aggregates doubao/deepseek/kimi |
| Baidu Qianfan | `https://qianfan.baidubce.com/v2/coding` | Aggregates ernie/glm/kimi/deepseek |
| Tencent TokenHub | `https://api.lkeap.cloud.tencent.com/coding/v1` | Aggregates deepseek/glm/kimi/minimax |
| StepFun Step Plan | `https://api.stepfun.com/step_plan/v1` | step series |
| iFlytek MaaS Coding | `https://maas-token-api.cn-huabei-1.xf-yun.com/v2` | Unified model ID |

Any OpenAI- or Anthropic-compatible endpoint can be plugged in through `hiq.toml`; see [`hiq.example.toml`](./hiq.example.toml) for the full template.

---

## 📄 Office Automation

### Word Documents

```bash
hiq run "Create a project report.docx with title, paragraphs and tables"
hiq run "Insert logo.png into report.docx"
hiq run "Generate table of contents for report.docx"
```

### Excel Spreadsheets

```bash
hiq run "Create sales_data.xlsx with months and sales"
hiq run "Add bar chart to sales_data.xlsx"
hiq run "Add conditional formatting to sales column in sales_data.xlsx"
```

### PowerPoint Presentations

```bash
hiq run "Create a presentation about AI"
hiq run "Create PPT using template, topic is digital transformation"
hiq run "Add fade-in animation to PPT"
```

Mail (multi-account, scheduled sending) and calendar (cron / ICS) tasks are covered in the [Office Automation Guide](docs/OFFICE_GUIDE.md).

---

## 🧠 Smart Memory System

### Long-term Memory

1. **Document Layer** — `hiq.md` in the project root, automatically loaded into context
2. **Auto Memory** — the Dream agent periodically consolidates session knowledge into project memory
3. **Tiered injection** — `l1 / l2 / l3` levels; only a compact prompt index is injected into the system prompt (byte-stable, cache-prefix safe), while full bodies are fetched on demand through `recall`

### Self-Evolution

- **Dream** (7-day cycle) — consolidates session knowledge into project memory
- **Distill** (30-day cycle) — detects repeated workflows and packages them into reusable Skills
- **Memory Archive** — deletions move to `.archive/` and stay recoverable
- **Profile-isolated memory** — dev and cowork modes keep separate memory roots

### RAG Knowledge Base

```bash
hiq run "Import this PDF into knowledge base"
hiq run "Search for authentication system knowledge"
hiq run "Answer this technical question using knowledge base"
```

### Project Knowledge Hub

A self-maintaining map (`map.md` + `kb.json`, with revisions and rollback) built from four sources — code, docs, memory and team output — plus a polling watcher that re-syncs on change and exposes `kb_sync / kb_map / kb_search / kb_summary / kb_history / kb_rollback` tools.

---

## 🎯 Task Orchestration

### Auto-plan

```bash
hiq run "Refactor authentication module, add JWT support, write unit tests"
# → plan → user approval → execute
```

### Goal Mode

```bash
hiq goal "Fix all TypeScript errors"
# → autonomous work → blocker detection → completion report
```
Completion is judged by an **independent LLM judge** (evidence-based, temperature=0) so the agent cannot optimistically stop.

### Expert Teams

```bash
hiq team review "Review this PR"
# → security expert + performance expert + architecture expert → consolidated report
```
Each team member runs in its own sub-session with a per-team concurrency pool (`MaxParallel`), shared-context notes, checkpoints, replan-on-failure and card-level human-in-the-loop approvals.

---

## 🔧 Plugin System

### Skills

```bash
hiq skill list
hiq skill install code-review
hiq skill new my-skill
```

### MCP Plugins

```toml
# hiq.toml
[[plugins]]
name = "my-mcp-server"
type = "stdio"
command = "node"
args = ["server.js"]
```

Plugins are trust-tiered, and tool output passes through PII scrubbing before it reaches the model.

---

## 🛠️ Built-in Toolbox

An IDE-grade native toolchain: `bash` · `read_file` · `write_file` · `edit_file` · `grep` (with timeout) · `web_fetch` · `web_search` · `todo_write` · `complete_step` · `codegraph_*` (tree-sitter based symbol / call-graph search), plus **CodeGraph** (local AST index in SQLite, zero API overhead), **LSP integration** (diagnostics, go-to-definition, cross references) and an **embedded web search chain** (Brave → Exa → Linkup → AnySearch fallback, no external MCP required).

Domain capabilities — browser automation, desktop automation, mail, knowledge base, documents, calendar, expert teams — ship as subagent skills invoked on demand through `run_skill`.

---

## 🔒 Safety & Reliability

- **Permission subject evaluation** — write tools and irreversible operations are approved against glob-matched subject paths; deny rules cannot be bypassed
- **Path traversal guards** — checkpoints use `filepath.IsLocal`; memory store uses `safeJoin`
- **Plan mode** — high-risk operations are intercepted until a plan is approved and signed off
- **Checkpoint / rewind** — file snapshots with `/rewind` as a one-key safety net
- **Summarizer timeout** — 90s cap prevents a stuck stream from blocking compaction forever
- **Fail-closed config writes** — strict load-for-edit path so a broken config never silently overwrites user settings
- **Cache-prefix guard** — a dedicated test asserts the system prompt bytes stay stable across turns

---

## ⚙️ Configuration

### Config File Locations

```
~/.config/hiq/hiq.toml    # Global config (Windows: %APPDATA%\hiq\config.toml)
./hiq.toml                # Project config
./.env                    # Environment variables
```

### Configuration Example

```toml
default_model = "deepseek/deepseek-v4-pro"

[[providers]]
name = "deepseek"
kind = "openai"
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"
default = "deepseek-v4-pro"
fast_model = "deepseek-v4-flash"
context_window = 1000000

[agent]
soft_compact_ratio = 0.5
compact_ratio = 0.8
compact_force_ratio = 0.9

[cowork]
vlm_model = "openai/gpt-4o"
screenshot_vlm_model = "openai/gpt-4o"
```

> **💡 Tip:** put your team conventions in `hiq.md` at the project root — the agent reads it on every turn.

---

## 🖥️ All Front Ends

| Front end | Command | Notes |
|-----------|---------|-------|
| Terminal TUI | `hiq chat` | Bubble Tea immersive terminal UI |
| Desktop client | `hiq-desktop` | Wails v2 + React 19, multi-tab |
| API server | `hiq serve` | HTTP / SSE |
| IM bots | `hiq bot start` | WeCom / Feishu / QQ |
| ACP | `hiq acp` | Agent Control Protocol bridge |

---

## 🛠️ Development

### Repository layout

```
hiq/            # root Go module: CLI, server, agent core, internal packages
desktop/        # separate Go module: Wails v2 shell + React 19 frontend
```

> **Note:** the root Go module path is still `github.com/zzycxz/hiq`, inherited from the upstream line. Clone-and-build works as-is; `go install github.com/zzycxz/hiq/cmd/hiq@latest` targets the upstream path, not this repository.

### Build the CLI

```bash
git clone https://github.com/ghost-guest/hiq.git
cd hiq
go mod download
go build -o hiq ./cmd/hiq        # or: make build
make cross                       # cross-compile for 6 platforms into dist/
```

### Build the desktop app

```bash
cd desktop/frontend && npm install && npm run build
cd .. && wails build -tags "desktop,production,codegraph_embed"
```

For a trimmed release build, add `-trimpath -ldflags "-s -w -H windowsgui -X main.version=<ver>"`. The embedded CodeGraph runtime (`codegraph_embed`) ships as a portable zero-network binary verified by SHA-256 checksums.

### Test

```bash
go test ./...                    # root module
cd desktop && go test ./...      # desktop module
cd desktop/frontend && npm test  # frontend
```

---

## 📚 Documentation

| Reference | Content |
|-----------|---------|
| **[User Guide](docs/GUIDE.md)** | Permissions, sandbox, MCP plugins, slash commands, `@` syntax, Plan mode |
| **[Architecture Spec](docs/SPEC.md)** | Engineering contract: system architecture, registry mechanism, data types |
| **[Features](docs/HIQ_FEATURES.md)** | Complete feature panorama, architecture diagram, capability matrix |
| **[Office Automation](docs/OFFICE_GUIDE.md)** | Word/Excel/PPT automation, email integration, calendar tasks |
| **[RAG Knowledge Base](docs/RAG_GUIDE.md)** | Document import, knowledge graph, entity extraction, semantic search |
| **[Expert Teams](docs/EXPERT_GUIDE.md)** | Multi-model collaboration, team configuration, collaboration modes |
| **[Checkpoints](docs/CHECKPOINTS.md)** | File-snapshot safety net design |
| **[Contributing](CONTRIBUTING.md)** | How to add new tools, providers and bot channels |
| **[Changelog](CHANGELOG.md)** | Historical release records and feature iterations |

---

## 🤝 Contributing

Contributions are welcome! Please read [CONTRIBUTING.md](CONTRIBUTING.md) first.

---

## 📄 License

MIT License — see [LICENSE](LICENSE).

hiq inherits the MIT license of its upstream line. Copyright notices of the upstream projects (fairpeer, Reasonix) are acknowledged above and remain in effect.

---

## 🔗 Links

- [GitHub](https://github.com/ghost-guest/hiq)
- [Releases](https://github.com/ghost-guest/hiq/releases)
- [Issues](https://github.com/ghost-guest/hiq/issues)
- [Upstream: fairpeer](https://github.com/zzycxz/fairpeer)
- [Upstream: Reasonix](https://github.com/esengine/DeepSeek-Reasonix)

---

<p align="center">
  <sub>Built on the shoulders of <a href="https://github.com/zzycxz/fairpeer">fairpeer</a> and <a href="https://github.com/esengine/DeepSeek-Reasonix">Reasonix</a> — thank you. ❤️</sub>
</p>
