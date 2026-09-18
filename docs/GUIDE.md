# hiq Guide

<a href="../README_en.md">README</a>
&nbsp;·&nbsp;
<a href="./GUIDE.zh-CN.md">简体中文</a>
&nbsp;·&nbsp;
<a href="./SPEC.md">Spec</a>

> Day-to-day configuration and usage. For the engineering contract and internals
> (data types, registries, package layout, roadmap), see the **[Spec](./SPEC.md)**.

## Contents

- [Configuration](#configuration)
- [Mode shortcuts quick map](#mode-shortcuts-quick-map)
- [Permissions & sandbox](#permissions--sandbox)
- [Plugins (MCP)](#plugins-mcp)
- [Slash commands](#slash-commands)
- [@ references](#-references)
- [Two-model collaboration](#two-model-collaboration)
- [Office Automation](#office-automation)

## Configuration

Resolution order: **flag > `./hiq.toml` > `~/.config/hiq/config.toml` >
built-in defaults**. Secrets come from the environment via `api_key_env` and are
never stored in config files.

```toml
default_model = "deepseek/deepseek-v4-pro"   # executor; set [agent].planner_model to add a planner
# language    = "zh"               # ui language; empty = auto-detect from $LANG / $HIQ_LANG

[ui]
# shortcut_layout = "desktop"      # classic|desktop; compatibility setting

[agent]
max_steps = 0                    # executor tool-call rounds; 0 = no limit
planner_max_steps = 12           # planner read-only tool-call rounds; 0 = no limit
# planner_model = "deepseek/deepseek-v4-flash"          # optional low-frequency planner
# subagent_model = "deepseek/deepseek-v4-flash"   # optional default for runAs=subagent skills
# subagent_models = { review = "deepseek/deepseek-v4-flash", security_review = "deepseek/deepseek-v4-flash" }
auto_plan = "off"                  # off|on; off keeps plan mode manual
# auto_plan_classifier = "deepseek/deepseek-v4-flash"   # optional; only borderline tasks call it

[[providers]]
name        = "deepseek"
kind        = "openai"
base_url    = "https://api.deepseek.com"
models      = ["deepseek-v4-pro", "deepseek-v4-flash"]
default     = "deepseek-v4-pro"
api_key_env = "DEEPSEEK_API_KEY"

[tools]
enabled = []   # omit/empty = all built-ins
bash_timeout_seconds = 120   # foreground safety cap; set 0 for no tool-local cap

[skills]
# paths = ["~/my-skills", "../shared/skills"]   # extra custom skill roots
# excluded_paths = ["~/.agents/skills"]         # hide convention roots without deleting folders
# disabled_skills = ["review"]                  # hide skills until /skill enable <name>

[permissions]
mode  = "ask"                                # writer fallback when no rule matches: ask|allow|deny
deny  = ["Bash(rm -rf*)", "Bash(git push*)"] # hard-blocked in every mode
allow = ["Bash(go test:*)"]                  # never prompted

[sandbox]
# workspace_root = ""          # file-writers confined here; empty = current dir
# allow_write    = ["/tmp"]    # extra dirs write_file/edit_file/multi_edit may touch

[[plugins]]
name    = "example"
command = "hiq-plugin-example"
```

For the full schema and every field's contract, see [`SPEC.md` §5](./SPEC.md#5-configuration-toml).

## Mode shortcuts quick map

Shortcuts are documented by client because users usually look for the keys that
work in the surface they are using. The small rule is: `Shift+Tab` only controls
Plan, `Ctrl/Cmd+Y` only controls YOLO, and paste stays on the platform paste key.

### Desktop GUI

| Key or control | What it does | Notes |
| --- | --- | --- |
| `Shift+Tab` | Toggles Plan on/off | Composer shortcut. Plan is read-only planning and does not cycle Ask/Auto/YOLO. |
| `Ctrl+Y` / `Cmd+Y` | Toggles YOLO on/off | Composer shortcut. Turning YOLO off restores the previous Ask/Auto base when known. |
| Ask / Auto / YOLO approval controls | Picks the tool approval posture directly | Clicking these controls is unchanged by the keyboard shortcuts. |
| Plan control | Toggles Plan on/off | Same mode as `Shift+Tab`. |
| Goal item in the collaboration menu | Starts, views, or clears Goal | Goal is not in any keyboard cycle. |
| `Cmd+V` on macOS, `Ctrl+V` on Windows/Linux | Pastes clipboard content | Images can also be dropped into the composer. |

### CLI / TUI

| Key or command | What it does | Notes |
| --- | --- | --- |
| `Shift+Tab` | Toggles Plan on/off | Plan is read-only planning and does not cycle Ask/Auto/YOLO. |
| `Ctrl+Y` | Toggles YOLO on/off | Turning YOLO off restores the previous Ask/Auto base when known. Terminals that forward Command/Super may also send `Cmd+Y`, but `Ctrl+Y` is the reliable terminal shortcut. |
| `--yolo`, `--dangerously-skip-permissions` | Starts chat in YOLO | Same runtime mode as `Ctrl+Y`. |
| Ask / Auto | No keyboard cycle | Ask is the default interactive base. Auto is not entered through `Shift+Tab`; use clients or APIs that expose the tool approval posture directly. |
| `Ctrl+V` | Pastes clipboard content | The CLI tries a clipboard image first, then falls back to text paste. |
| `/paste-image` | Pastes a clipboard image | Use it when you want image-only paste or the terminal handles text paste itself. |
| `/goal <objective>`, `/goal status`, `/goal clear` | Starts, checks, or clears Goal | Goal is not in any keyboard cycle. |

`[ui].shortcut_layout` is still accepted for old configs, but the shortcut
behavior above is unified across layouts.

Mode meanings:

| Mode | Meaning |
| --- | --- |
| Ask | Prompts for fallback writer approvals. |
| Auto | Auto-allows fallback approvals; explicit `ask` / `deny` rules still apply. |
| YOLO | Skips ordinary tool approval prompts; `deny`, user `ask` questions, and plan approval prompts still wait. |
| Plan | Keeps the next work read-only until a plan is approved or Plan is turned off. |
| Goal | Pursues a saved objective until complete, blocked, or cleared. |

## Permissions & sandbox

Permissions gate each tool call: `deny` > `ask` > `allow` > fallback. Bash and
file mutation tools require approval by default; read-only tools generally do
not. Approvals are stored and matched as permission rules, not button labels:
for example `Bash(npm run build)`, `Bash(npm run test:*)`, and `Edit(docs/**)`.
`hiq chat` can grant Bash as an exact command or as a conservative command
prefix (for example `Bash(go test:*)`), while file-editing tools share session
edit grants and persist path-scoped rules such as `Edit(src/app.go)`.
`hiq run` stays autonomous but still honours `deny`.

Permissions are *policy* (which calls to allow / prompt). The **sandbox** is
*enforcement*: the file-writers (`write_file` / `edit_file` / `multi_edit`)
refuse any path outside `[sandbox] workspace_root` (default: the current dir, so
edits stay in the project), resolving symlinks and `..` so a link can't tunnel
out. Reads are unrestricted. `bash` is itself jailed on macOS by default
(`[sandbox] bash`, Seatbelt): commands may write only those same roots (plus
temp and toolchain caches) and reach the network only when `[sandbox] network`
is set. Other platforms fall back to running unconfined for now (see
[`SPEC.md` §9](./SPEC.md#9-roadmap-not-in-current-scope) for the escape-prompt and
Linux support still to come).

## Plugins (MCP)

hiq is an MCP client. A `[[plugins]]` entry's `type` selects the transport:
`stdio` (default) launches a local subprocess (`command`/`args`/`env`); `http`
(Streamable HTTP) connects to a remote `url` with optional static `headers`
(`${VAR}` / `${VAR:-default}` expanded from the environment, so tokens stay out
of the file). Tools surface to the model as `mcp__<server>__<tool>`; a tool
declaring MCP's `readOnlyHint: true` joins parallel dispatch and the permission
reader-default.

A server's **prompts** surface as `/mcp__<server>__<prompt>` slash commands
(positional args after the command); its **resources** are pulled in by writing
`@<server>:<uri>` in a message; `/mcp` lists connected servers and what each
exposes. `make build` also produces `bin/hiq-plugin-example` — a runnable
reference stdio server (`echo`, `wordcount`, a `review` prompt, a style-guide
resource) you can copy.

```toml
[[plugins]]                       # local stdio server
name    = "example"
command = "hiq-plugin-example"

[[plugins]]                       # remote server over Streamable HTTP
name    = "stripe"
type    = "http"
url     = "https://mcp.stripe.com"
headers = { Authorization = "Bearer ${STRIPE_KEY}" }
```

Enabled MCP servers start connecting automatically in the background after a
session begins, so chat stays usable while tools come online. Use `/mcp` or the
desktop MCP panel to refresh status, reconnect a server, inspect failures, or
disable a server for the current session.

**Already have an `.mcp.json`?** Drop it in the project root and hiq
reads it as-is — the `mcpServers` spec (`command`/`args`/`env`, `type`/`url`/
`headers`, `${VAR}` expansion) maps field-for-field onto `[[plugins]]`. Both
sources are merged; on a name collision `hiq.toml` wins.

```json
{
  "mcpServers": {
    "filesystem": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path"] },
    "stripe": { "type": "http", "url": "https://mcp.stripe.com", "headers": { "Authorization": "Bearer ${STRIPE_KEY}" } }
  }
}
```

**Upgrading from `0.x`?** Your old `~/.hiq/config.json` is still read for its
`mcpServers` (honouring `mcpDisabled`) as a lowest-priority source, so MCP servers
keep working — move them into `hiq.toml`'s `[[plugins]]` or a `.mcp.json` when
convenient.

## Slash commands

In `hiq chat`, built-in commands (`/compact`, `/new`, `/clear`, `/rewind`,
`/tree`, `/branch`, `/switch`, `/todo`, `/model`, `/mcp`, `/skills`, `/hooks`,
`/memory`, `/output-style`, `/sandbox`, `/language`, `/auto-plan`, `/help`) run
locally — `/help` lists them all. `/new` starts a new session while saving the
previous transcript for history/resume; `/clear` asks for confirmation, then
discards the current context without saving it. `/tree` shows saved conversation
branches, `/branch [name]` forks the current conversation tip, `/branch <turn>
[name]` forks from an earlier checkpointed turn, and `/switch <id|name>` loads
another branch. **Custom commands** are Markdown files under `.hiq/commands/`
(project) or `~/.config/hiq/commands/` (user) — `review.md` becomes
`/review`, a subdirectory namespaces it (`git/commit.md` → `/git:commit`). The
body is a prompt template; invoking the command sends it as a turn.

```markdown
---
description: Review the staged diff
argument-hint: [focus-area]
---
Review the staged diff. Focus on $ARGUMENTS, list bugs with file:line.
```

`$ARGUMENTS` expands to all space-separated args, `$1`…`$N` to positional ones.
MCP prompts also appear here as `/mcp__<server>__<prompt>`.

## @ references

Embed `@` references in a message and hiq resolves them before sending, as
tagged context blocks: `@path/to/file` (or `@dir`) injects a local file's
contents (or a directory listing), and `@<server>:<uri>` injects an MCP
resource. A local path is only treated as a reference when it actually exists,
so ordinary `@mentions` stay literal. Typing `/` or `@` opens an autocomplete
menu — slash commands, or hierarchical file navigation (one directory level at a
time, descend into folders) plus MCP resources.

## Two-model collaboration

`hiq setup` keeps first-run minimal: pick provider → keys (every SKU of a
chosen provider is enabled). Running two models together (executor + planner,
separate cache-stable sessions) is a one-line edit afterwards — set
`planner_model` to any other enabled provider:

```toml
[agent]
planner_model = "deepseek/deepseek-v4-flash"   # used as the low-frequency planner
planner_max_steps = 12           # read-only tool-call rounds before pausing
```

The planner sees loaded `hiq.md` / `AGENTS.md` memory and a small read-only
research tool set, so it can inspect relevant files before handing a plan to the
executor. Writer and workflow tools remain executor-only. `max_steps` limits the
executor; `planner_max_steps` limits only the planner, and either can be set to
`0` for no round limit.

Keep personal step-limit preferences in the user config. Add them to a project's
`./hiq.toml` only when that repository needs a shared override, such as a
larger planner limit for a very large codebase.

Subagent skills inherit the executor model by default. Set `subagent_model` to
run them on another configured model, or use `subagent_models` to override only
specific skills such as `review` or `security_review`.

For interactive frontends, plan mode is manual by default. Set
`agent.auto_plan = "on"` to make complex-looking tasks enter plan mode
automatically: hiq first drafts a read-only plan, then waits for approval
before editing or running side-effecting commands. `auto_plan_classifier` can
name a cheap provider such as `deepseek/deepseek-v4-flash`; it is only called for borderline
inputs and falls back to the heuristic if classification fails. Use
`/auto-plan off|on` in `hiq chat` to change the user-level setting, or
`hiq config auto-plan off|on` from a shell/script. Pass `--local` to the
shell command only when you intentionally want a project-local override.

The why behind separate sessions (keeping each model's prefix cache-stable) is in
[`SPEC.md` §3.5](./SPEC.md#35-two-model-collaboration-coordinator).

## Office Automation

hiq provides complete Office document processing capabilities through the `document-auto` and `ppt-auto` Skills.

### Word Documents

Use the `doc_write` tool to create and edit Word documents:

```json
{
  "path": "report.docx",
  "sections": [
    {"type": "heading", "text": "Chapter 1", "level": 1},
    {"type": "paragraph", "text": "Content here"},
    {"type": "image", "image_path": "/path/to/image.png", "image_alt": "Logo"},
    {"type": "toc", "toc_level": 3}
  ]
}
```

Supported features:
- Paragraphs, headings, lists, tables
- Image insertion (PNG/JPG/GIF/SVG)
- Table of contents generation
- Format control (fonts, colors, alignment)

### Excel Spreadsheets

Use the `xlsx_write` tool to create and edit Excel spreadsheets:

```json
{
  "path": "data.xlsx",
  "sheets": [{
    "name": "Sheet1",
    "cells": [
      {"ref": "A1", "value": "Month"},
      {"ref": "B1", "value": "Sales"}
    ],
    "cond_fmt": [{
      "range": "B2:B10",
      "type": "cell",
      "criteria": "greater_than",
      "value": "10000",
      "format": {"bg": "#00FF00"}
    }]
  }],
  "charts": [{
    "sheet": "Sheet1",
    "type": "bar",
    "title": "Monthly Sales",
    "data_range": "A1:B10",
    "position": "D2"
  }]
}
```

Supported features:
- Cell read/write, multiple sheets
- Charts (bar, line, pie, scatter)
- Conditional formatting (cell conditions, data bars, color scales)
- Formula support

### PowerPoint Presentations

Use the `ppt-auto` Skill to create professional presentations:

```bash
# Create PPT
hiq run "Create a presentation about AI"

# Use template
hiq run "Create PPT using template, topic is digital transformation"

# With animation
hiq run "Add fade-in animation to PPT"
```

Supported features:
- SVG free design
- Template filling
- Animation effects (fade, fly, zoom)
- Transition effects (fade, slide, zoom)
- VLM integration (vision model assisted design)

### Email Integration

```toml
[cowork]
[[cowork.email_accounts]]
name = "Work Email"
smtp_host = "smtp.gmail.com"
smtp_port = 587
imap_host = "imap.gmail.com"
imap_port = 993
username = "your@email.com"
password_env = "EMAIL_PASSWORD"
```

Supported features:
- Multi-account support (Gmail/Outlook/QQ/163 etc.)
- Send/read/search emails
- Scheduled sending

### Calendar Tasks

```toml
[cowork]
[[cowork.schedules]]
name = "Daily Reminder"
cron = "0 9 * * *"
prompt = "Remind me about the morning meeting"
```

Supported features:
- Cron expression scheduling
- Timed reminders/execution
- ICS format support
