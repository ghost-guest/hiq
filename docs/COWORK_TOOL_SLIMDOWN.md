# cowork 工具面精简方案（工程级 spec）

> 状态：设计稿　·　日期：2026-06-30　·　关联：[COWORK_IMPLEMENTATION_PLAN.md](./COWORK_IMPLEMENTATION_PLAN.md)
> 目标读者：实施者。本文件应做到无需再设计即可动手。所有"已验证"项均有代码出处。

---

## 1. 已验证事实（代码钉死）

| 事实 | 出处 | 含义 |
|---|---|---|
| cowork 真注册 ~94 工具 | 枚举 builtin `Name()` | 前提属实，非估算 |
| 工具每轮现序列化 | `internal/agent/agent.go:1072` `Tools: a.tools.Schemas()` | executor 用 coreReg，每轮只见 coreReg 内工具 |
| `FilterRegistry(reg,names)` 能建小 reg | `internal/agent/task.go:358` | `coreReg` 零新代码 |
| `Registry.Add` 按名幂等 | `internal/tool/tool.go:115-125` | 工具按名去重，重复注册安全 |
| tools 是 per-request | agent 每次 Stream 带 `Tools` | provider 接受工具集，无需配合 |
| browser 注册在 cowork 门外 | `internal/boot/boot.go:340-343` | browser 属 **universal**，dev/cowork 都有 |
| skill 有 inline/subagent 两态 | `internal/skill/skill.go:39-44, tools.go:98` | inline 仅注入正文文本、**不暴露工具**；subagent 在子代理用 scoped 工具 |
| 内置 skill 已能包工具族 | `internal/skill/builtins.go:382,391` | browser_auto/desktop_auto 已把 screen_\*/browser_\* 包成 skill——能力即 skill 是现成模式 |

## 2. 工具三桶分解（核心洞察）

"编码工具"非铁板一块——拆 **universal 文件层** 与 **代码重构层**。dev/cowork 关系不对称：

| 桶 | 内容 | 谁用 |
|---|---|---|
| **Universal**（共享脊柱） | bash, read_file, edit_file, grep, glob, ls, move_file, web_search, web_fetch, browser_\*(cowork 并成 perceive/act), todo_write, ask, remember, run_skill, task, slash_command | dev + cowork |
| **Coding-specific** | apply_patch, delete_range, delete_symbol, code_index, notebook_edit, multi_edit, lsp_\*, codegraph_\* | **dev 加** |
| **Office-specific** | screen_\*, window_\*, email_\*, schedule_\*, rag_\*, document_\*, expert_team, media | **cowork 加** |

→ **dev = Universal + Coding；cowork = Universal + Office。** cowork 需要"编码工具"里的 universal 文件层，不需要代码重构层；dev 需要 browser（universal），不需要 office/GUI 层。

```
工具 → profile 组装
  Universal(共享脊柱) ─────────────┬─→ dev 核    (+ Coding-specific)
  bash/read/edit/grep/glob/browser/ │
  todo/ask/remember/run_skill/      └─→ cowork 核 (+ Office-specific)
  task/slash
  Coding-specific ───→ 仅 dev
  Office-specific ───→ 仅 cowork
```

## 3. 目标 + token 预算

顶层每轮可见 **~15-18**（保留 universal 文件工具的代价；Step 0 A/B 若证明 bash 替代 grep/glob 同样可靠，可压到 ~13-15）。操作工具 24 个、95% 步要用、需主 loop 屏幕状态 → **不能进 skill 子代理** → **P2 合并 perceive/act 是去低个位数的必经路**。全部领域能力走 `run_skill`，可达且开世界可扩展。先 cowork，dev 复用框架换配置。

**token 预算（粗估，待 Step 0 校准）**：现状 ~94 工具 × ~200 tok ≈ **~19K**；P2 后 ~17 扁平 schema ≈ **~3.4K**。能力走 skill（底层工具 scoped 进子代理、主 prompt 零成本），N 个能力 = N×~15 tok 技能索引（cached）。100 能力 ≈ 1.5K+3K；500 能力 ≈ 7.5K+3K——远胜全摊平（500 工具 ≈ 100K）。

## 4. LLM 可见拓扑

顶层 **1 agent（主 CUA）** + ~15-18 扁平工具（唯一门户 `run_skill`）；能力全在幕后子代理（默认不在主 prompt、技能索引一行描述、`run_skill` 才生成）。底层工具 scoped 进子代理，主 prompt 零成本。

```
用户任务 → ┌ 主 CUA（1 agent，~15-18 工具）──────────────┐
           │ perceive·act·window    ← P2: 24 操作工具合并 │
           │ bash·read_file·edit_file                     │
           │ grep·glob·ls·move_file·web_search·web_fetch  │ ← universal 文件层
           │ todo_write·ask·remember                      │
           │ run_skill  (唯一门户)                         │
           └────────────┬─────────────────────────────────┘
       95%步: 感知/操作/文件直接调           │
                                 委派→独立子代理
                       ┌──────────┴──────────────────────┐
                       │ run_skill 子代理（全部能力，开世界）│
                       │  email·schedule·rag·document      │
                       │  media·ppt·office_expert·memory   │
                       │  + research·review·ppt_wizard     │
                       │  + desktop_auto·browser_auto     │
                       │  + 后续新增 / 应用市场下载的 skill │
                       └───────────────────────────────────┘
                        底层工具 scoped 进子代理，主 prompt 零成本
```

**调用逻辑**：
| 场景 | 走法 | 在哪 |
|---|---|---|
| 看屏/点/敲/读改文件/命令（~95% 步） | perceive/act/bash/read_file/edit_file/grep/glob/web_search | 主 loop |
| 任务管理/澄清/记记忆 | todo_write/ask/remember | 主 loop |
| 领域能力（email/rag/ppt/…，含后续新增与市场下载） | `run_skill(X, task)`→子代理(scoped tools) | 独立子 loop，返结果 |

### 4.1 调用链示例（4 种典型）

**① 纯 CUA（最常见，领域工具从不进 prompt）**
```
"用记事本写 hello 存到桌面"
todo_write → bash(notepad.exe) → window(focus,"记事本")
→ perceive(scope:desktop) → act(type, 文本框, "hello") → perceive(验证)
→ act(key, ctrl+s) → perceive(存盘框) → act(click, 文件名框)
→ act(type, 绝对路径) → act(key, enter) → bash(ls 验证) → 完成
```

**② 领域能力（run_skill 子代理，返结果）**
```
"读我最近 3 封邮件并总结"
todo_write → run_skill("email", "读最近3封邮件并总结")
  └─ 子代理(scoped: email_search/email_read) → 返总结
→ 完成
```

**③ 交织任务（无头能力返数据 + 主 loop 操作 GUI）**
```
"把最新邮件里的会议时间填进 OA 网页表单"
run_skill("email", "取最新邮件里的会议时间") → 返时间
→ perceive(scope:web) → act(type, 时间字段, 时间) → act(click, 提交) → perceive(确认)
```

**④ 重活委派（run_skill 子代理，父上下文保持干净）**
```
"调研 LangGraph vs AutoGen 哪个适合我们"
run_skill("research", "…")
  └─ 子代理(独立 session, scoped: web_search/read_file) 多轮调研 → 返回结论
→ 主 loop 收结论 → (可选 edit_file 落盘) → 完成
```

## 5. 机制实现（concrete types）

### 5.1 Profile 可见层（`internal/config/profile.go`）
```go
type Profile struct {
    // ...existing...
    EnabledTools  []string `toml:"enabled_tools"`   // 可见层白名单；空=全可见(向后兼容)
    DisabledTools []string `toml:"disabled_tools"`  // 可见层黑名单
}
// cowork 内置 profile 设默认 EnabledTools = coreNames（见 5.4）
```
**分层（仅作用于 builtin，用 `tool.LookupBuiltin(name)` 判来源）**：
- `cfg.Tools.Enabled`（`[tools].enabled`）= **注册层**（哪些 builtin 进 reg）。
- `EnabledTools` 非空 → **白名单**（cowork 用：枚举 ~15 核，其余 builtin 由 skill 承载）。
- 否则用 `DisabledTools` **黑名单**（dev 用：把 `lsp_*`/`codegraph_*`/`notebook_edit`/`code_index`/`delete_symbol` 交给 skill，其余 builtin 仍扁平——新加 builtin 自动可见、不漏）。
- **MCP/plugin 工具豁免**此过滤（否则用户配的 MCP server 会消失）；MCP 可见性由现有 `Profile.Plugins` 单独管。
- 故 `coreReg = (过滤后 builtins) ∪ (reg 中全部 plugin/MCP 工具)`；executor 每轮 `Schemas()` 只看 coreReg。
- dev 的"20+"= ~19 builtin 核 + 用户配的 N 个 MCP 工具（builtin 固定、MCP 浮动）。

### 5.2 能力 = `runAs: subagent` skill（开世界，复用现成模式）

每个能力做成一个 skill（照 `browser_auto`/`desktop_auto` 写法，[builtins.go:382,391](hiq/internal/skill/builtins.go#L382)）：声明 `allowed-tools`（底层工具，scoped 进子代理），落进 skill 目录即自动发现、自动进技能索引。**`run_skill` 是唯一门户**——不手工枚举组、闭世界 GroupMap 已弃用。

内置能力 skill（在 `internal/skill/builtins.go` 声明 `runAs: subagent` + `allowed-tools`）：
| skill | 底层工具（allowed-tools） |
|---|---|
| email | email_send, email_read, email_search |
| schedule | schedule_create/list/update/delete/run_now/history |
| rag | rag_import/search/graph/mindmap/list/delete |
| document | doc_read/write, csv_read/write, xlsx_read/write, doc_convert |
| media | image_generate, video_understand |
| ppt | 外挂 skill（.hiq/skills/ppt-auto/） |
| office_expert | expert_team_run/list |
| memory | forget/recall/memory_compact/memory_profile/memory_status |

**为什么用 skill**：office 能力都是**无头数据操作**（干活返数据、不与屏幕状态交织），子代理做完返结果即可。唯一不能当 skill 的是 GUI 操作（perceive/act，95% 步、需主 loop 状态）→ 留扁平核。

### 5.3 run_skill 单门户
```jsonc
run_skill({ name: "email"|"schedule"|"rag"|…|"browser_auto"|…, arguments: "读最近3封邮件并总结" })
→ 子代理(scoped: 该 skill 的 allowed-tools) 干活 → 返结果文本
```
发现性靠技能索引（names+一行描述、cache-stable）；新加 skill / 市场下载 → 自动进索引、`run_skill` 可达。

### 5.4 cowork 默认 coreNames（P2 后）
```
perceive, act, window, bash, read_file, edit_file, grep, glob, ls, move_file,
web_search, web_fetch, todo_write, ask, remember, memory_query, run_skill, task
（~18；grep/glob/ls 靠 bash 可去 3-4 → ~14）
```

### 5.5 perceive / act / window（P2 重档，显式 scope 路由）
```jsonc
perceive({ scope: "desktop"|"web"|"visual", window?, task_hint? })
  → { channel, elements:[{id:"s3", type, label, x,y,w,h}], scene, recommended_click?, vlm_confidence }
act({ action: "click"|"type"|"key"|"scroll"|"drag", target:{ref?,x?,y?}, text?, modifiers?, scope? })
window({ action: "focus"|"maximize"|"restore"|"move"|"close", title? })
```
路由状态机（`act.target.ref` 前缀决定后端，杀多上下文错路由）：
```
ref "s*" 或 scope=desktop  → screen_click/type/key/scroll   (物理屏坐标)
ref "b*" 或 scope=web      → browser_click/type/scroll       (视口坐标)
无 ref、纯 x/y + scope=visual → screen 点击(经 VLM 坐标)
```
原始 screen_\*/browser_\*/window_\* 实现留 `reg`，经 desktop_auto/browser_auto 技能可达（回退/裸用）；image_understand 并入 perceive 作 visual 通道。

### 5.6 扩展性（开世界：加 skill / 应用市场）

**核心不变量**：扁平核固定 ~15；能力全走 skill 索引。加再多能力，主 prompt 只长"技能索引"（每条 ~15 tok、cached），扁平核与底层工具 schema 不长。

- **加新能力**（改 PPT、加 email 操作、加 skill）：写成 skill 文件 → 自动进索引 → `run_skill` 可达。底层工具注册进 `reg` 但不进 coreReg → 主 prompt 零成本。
- **应用市场下载**：skill 包 → 落 skill 目录 → 自动索引 → `run_skill` 可达；MCP server → 工具 `mcp__*` → 豁免 builtin 过滤、归 `Profile.Plugins`。
- **安全前置**：v0.1.9 删市场因 prompt injection/SSRF/ZIP 穿越风险；重开市场须先补信任/沙箱 gating。与本设计正交、兼容。
- **纪律**：新工具默认包进 skill，不进扁平核；只有"几乎每步都用"的 universal 工具才进核。

## 6. 关键数据流

### 6.1 一个 turn（含 run_skill 委派）
```
用户消息
  ↓ agent.step
a.prov.Stream({ Messages, Tools: a.tools.Schemas() })   ← 每轮现取(已验证)
  ↓ 模型回 tool_call
  ├─ coreReg 内普通工具(perceive/act/bash/read/edit/grep/…) ─→ 直接执行
  └─ run_skill("email", task) → FilterRegistry(reg, sk.AllowedTools) → 子代理 → 返结果
  ↓ 工具结果入 session → 下一轮
```

### 6.2 boot 里 coreReg 的构建点（`internal/boot/boot.go` Build 末尾）
```
reg := NewRegistry()
addBuiltins(reg, …)            // universal + coding builtins
PlatformTools / BrowserTools → reg                 // universal
if cowork: Screen/Window/PPT/Schedule/Email/RAG/Document/Expert → reg
plugins / lsp / codegraph → reg
task / skill / memory / ask → reg
═══════════════ 注册结束 ═══════════════
coreNames := profile.EnabledTools（或 cowork 默认）
coreReg := agent.FilterRegistry(reg, coreNames)     // ★ 可见集（仅 builtin 过滤；mcp__* 豁免）
executor := agent.New(execProv, coreReg, …)          // 用 coreReg
// task 工具 / skill runner 构造时仍传入全 reg → 子代理可达全部能力
```

## 7. 编码场景（dev）工具计划

dev 与 cowork **共用同一框架**（`Profile.EnabledTools` 可见层 + `run_skill`）。差异：**dev 不驱动 GUI → 无 perceive/act/window**；痛点轻（~19-27 工具，非 ~94）→ **默认 mostly-flat**，`run_skill` 只承载偶尔用、flag 门控的代码智能。

**dev 扁平核 ~19**：
```
[Universal 脊柱] bash read_file edit_file grep glob ls move_file
                 web_search todo_write ask remember
                 run_skill task slash_command
[Coding-常驻]   apply_patch multi_edit delete_range delete_symbol
```

**dev 能力 skill**（`run_skill` 委派；flag 门控）：
| skill | 底层工具（scoped） | 何时 |
|---|---|---|
| code | lsp_hover/definition/references | `[lsp] enabled` |
| codegraph | codegraph_\*（MCP 命名空间） | `[codegraph] enabled` |
| notebook | notebook_edit | Jupyter 场景 |

**dev run_skill 子代理**（编码味，dev 专属；cowork 不暴露）：explore/research/review/security_review/test/init/install_capability

**dev 不含**：perceive/act/window（不驱动 GUI）、email/schedule/rag/document/ppt/expert/media（office 专属）。

**优先级：低**。dev 基础计数已小，框架就绪后可选；要明显受益得等 dev 挂大量 MCP。Phase 1 后即可配（零新代码）。

**cowork vs dev 对照：**
| | cowork（办公） | dev（编码） |
|---|---|---|
| 扁平核 | perceive/act/window + universal 文件 | apply_patch/multi_edit/delete_\* + universal 文件 |
| run_skill 能力 | email/schedule/rag/document/media/ppt/office_expert/memory + research/review/ppt_wizard/desktop_auto/browser_auto | code/codegraph/notebook + explore/research/review/security_review/test/init/install_capability |
| 不含 | coding 重构层 | perceive/act + office 全套 |

## 8. Step 0 — 实测基线（先做，gating）

**命令**
```bash
# 1. 真实工具清单（临时在 Build 末尾 log reg.Names() + 按 skill/三桶分类）
./bin/hiq.exe run --profile cowork --max-steps 2 "List every tool you can call, one per line"
# 2. token 体积：log JSON-marshal(coreReg.Schemas()) 字节数 proxy；有 tokenizer 则精确算。
```

**10 个样本任务**：纯 CUA（记事本存盘/计算器/整理文件）、领域（读邮件/建日程/RAG 搜）、交织多上下文（邮件填 OA 表单/记事本抄进浏览器/邮件→docx→回复/网页数据填 Excel）。

**选错率评分表**：①选错工具 ②坐标混用 ③冗余步数（实际−参考最短）。

**A/B**：grep/glob/ls 留扁平 vs 靠 bash——决定 cowork 核 ~18 vs ~14。

**email run_skill scoped 闭环验证**：run_skill("email")→子代理拿到 email_* scoped 工具→返结果；未知 skill 报错；缺配置优雅降级。

## 9. 分阶段（每阶段独立验收门）

### Phase 0 — 去重 + 踢真·代码重构工具　~94 → ~80
- 只踢 cowork 核的代码重构层（dev 保留）：apply_patch/delete_range/delete_symbol/code_index/notebook_edit/multi_edit。（grep/glob/ls/move_file/web_fetch/wait/mindmap 是 universal，保留。）
- 合并：bash_output+kill_shell→bash；complete_step→todo_write；write_file+edit_file→edit_file；web_fetch→web_search。
- memory：remember+memory_query 扁平；其余→memory skill。
- 归位：task/parallel_tasks→折进 run_skill；install_source/install_skill/read_skill→settings。
- **验收门**：cowork demo 全过；`go test ./...` 绿；可见 ~80。

### Phase 1 — 解耦可见核 + 能力 skill 化　~80 → ~40
- boot 末尾建 `coreReg` 传 executor；`Profile` 加 `EnabledTools`/`DisabledTools`（仅 builtin）；新增能力 skill（builtins.go，runAs:subagent+allowed-tools）；写发现性 prompt（技能索引 + "需领域→run_skill"）。
- 移出顶层：email/schedule/rag/document/expert/media/ppt/memory-mgmt（全转为 skill）。
- 操作工具 24 个仍扁平（不能进 skill，需主 loop 状态）→ 数量大降在 P2。
- **验收门**：可见 ≤40；`"读最近3封邮件"` 经 run_skill("email") 跑通；选错率较基线降；新增 `run_skill_scoped_test`。

### Phase 2 — 操作工具合并（必做）　~40 → ~15-18
- 实现 perceive/act/window（显式 scope 路由，§5.5）。
- **解风险：`[cowork] unified_ops` flag（默认 false）**。false=旧扁平 screen_\*/browser_\*（零回归）；true=perceive/act。A/B demos、选错率降才翻默认。
- 重写 `coworkDefaultPromptAddon` R1–R6/H1–H4（§10）。
- **策略（数据驱动）**：轻档（screen_\*/browser_\*/window_\* 各归一个 skill、不改 prompt 操作规则）始终做；重档（统一 perceive/act+改 prompt）flag 灰度，由选错率触发。
- **验收门**：demos 步数/错误率不升反降；`cua_vlm_test` 过、多上下文路由正确；可见 ~15-18。

## 10. P2 prompt 重写示例（高风险，单列）

`coworkDefaultPromptAddon`（`internal/config/profile.go`）逐条映射：

| 原文（节选） | 改写 |
|---|---|
| R1: `screen_click {x,y} MUST be a coordinate you read out of screen_perceive…` | R1: `act {target:{x,y}, scope:"desktop"} MUST use a coord from perceive(scope:"desktop")'s element list. For web, perceive(scope:"web") returns refs b1…; act by ref. Never invent coords or mix desktop/web coords.` |
| R2: `Keyboard shortcuts go through screen_key…` | R2: `Shortcuts via act({action:"key", text:"ctrl+s", scope:"desktop"})…` |
| 通道选择: `browser_snapshot for web, screen_perceive for desktop` | `perceive(scope:"web"|"desktop"|"visual") — pick by what you're operating` |

原则：增量改、保 raw 工具经 desktop_auto/browser_auto 可达作回退、每改一条跑 cua demo 回归。

## 11. 边界与异常处理

| 情况 | 处理 |
|---|---|
| `run_skill(未知 skill)` | 现有逻辑报 "unknown skill" + 列可用 |
| skill 底层工具缺配置（如 email 无 IMAP） | 子代理内该工具被调时按现有逻辑返配置错误 |
| 应用市场下载的 skill 未受信 | 信任/沙箱 gating 先行（§5.6）；未受信则不索引/不可调 |
| `perceive(scope:"web")` 但无浏览器 | browser 工具返 "no session"（现有）；prompt 教先 browser_open |
| `act` 用过期 ref（页面已跳转） | 返 "ref invalid, re-perceive" |
| 多显示器 | screen 坐标跨屏物理坐标（现有 screen_\* 不变）|
| 无焦点窗口 | `perceive(scope:"desktop")` 返当前桌面；`window({action:"focus"})` 先聚焦 |
| 升级后老用户工具集变 | `EnabledTools` 空=全可见（兼容）；新核写 CHANGELOG；flag 灰度 |

## 12. 测试矩阵

| 测试 | 覆盖 |
|---|---|
| `profile_filter_test` | EnabledTools/DisabledTools 产出正确 coreReg；空=全可见；**MCP 工具豁免不过滤** |
| `run_skill_scoped_test` | run_skill 子代理拿到 AllowedTools scoped 工具；未知 skill 报错；底层缺配置优雅降级 |
| `perceive_act_routing_test` | s\*/b\* ref 路由；scope 回退；过期 ref 报错 |
| `multi_context_test` | desktop+web 交织（记事本+浏览器）路由不混 |
| 回归 | cua demos（notepad/浏览器/填表）、`go test ./internal/{config,agent,skill}/...` |

## 13. 关键文件

| 文件 | 改动 |
|---|---|
| `internal/config/profile.go` | 加 `EnabledTools`/`DisabledTools`（仅 builtin）；cowork 默认 coreNames；P2 改 `coworkDefaultPromptAddon` |
| `internal/skill/builtins.go` | 新增能力 skill（email/schedule/rag/document/media/ppt/office_expert/memory，runAs:subagent + allowed-tools，照 browser_auto 写法） |
| `internal/boot/boot.go` | 末尾 `coreReg:=FilterRegistry(reg,coreNames)` 传 executor（MCP 豁免）；P2 注册 perceive/act/window + 读 `unified_ops` |
| `internal/agent/task.go` | 复用 `FilterRegistry`；task 折进 run_skill |
| `internal/tool/builtin/` | P0 合并 bash/memory/edit；P2 perceive/act/window 路由层（原实现留 reg，供 skill scoped） |
| 测试 | `internal/config/profile_test.go`、`internal/skill/`、`tests/cua_vlm_test.go`、scripts/cua-demo\* |

## 14. 风险矩阵

| 风险 | 缓解 |
|---|---|
| P2 重写 prompt 致 CUA 回归 | `unified_ops` flag 默认关；A/B demos；保 raw 回退；增量改 |
| perceive/act 多上下文错路由 | 显式 `scope`，不自动猜；ref 按 channel 命名空间化 |
| 能力走 run_skill 子代理 | 复用既有 skill 机制（browser_auto 先例）；email 原型验证 scoped 工具 |
| 升级改变老用户工具集 | `EnabledTools` 空=全可见；CHANGELOG；flag 灰度 |
| 模型不懂何时 run_skill | 发现性 prompt + 技能索引（names+一行描述）已在 prompt |
| 能力经子代理跳转变慢 | 顶层 read/edit/bash 当胶水；office 能力本就无头、返数据即退场 |

## 15. 发布与迁移

| 版本 | 内容 | 风险 |
|---|---|---|
| vN.1 | Phase 0（去重+踢重构工具） | 低，纯缩减 |
| vN.2 | Phase 1（coreReg+能力 skill 化），cowork 默认开 | 中，behind `EnabledTools=[]` 兼容 |
| vN.3 | Phase 2 轻档（screen/browser 归 skill） | 低-中 |
| vN.4 | Phase 2 重档（perceive/act），`unified_ops` 默认关，A/B 后翻 | 中-高 |
| 随时 | dev fast-follower（Phase 1 框架就绪后） | 低 |

CHANGELOG 每版注明"cowork 默认工具集变更；`EnabledTools=[]` 还原全可见"。

## 16. 验证（端到端 + 测量门）
1. Step 0 基线：~94 + token + 选错率 + grep/bash A/B。2. 可见数：P0 ~80 / P1 ~40 / P2 ~15-18。3. 核心操作 demos。4. 领域+交织+多上下文（`"读3封邮件，把会议时间填进OA网页"`）。5. `go test ./internal/{config,agent,skill}/...` 绿 + scoped/路由测试。6. 选错率每阶段单调降。

## 17. 待决（Open，归 Step 0）
- universal 文件工具（grep/glob/ls）留扁平 vs bash 替代。
- P2 重档多上下文路由边界（无焦点窗口、多显示器）。
- prompt 重写后选错率达标阈值（Step 0 定基线后给）。
- dev 快速跟进的具体 `EnabledTools` 默认。
- screen 工具是否经 `init()` 漏进 dev（核实 bucket 边界）。

## 18. 取舍备忘
- 三桶：Universal(共享) / Coding-specific(dev) / Office-specific(cowork)。browser 属 universal。
- 载体 = `runAs:subagent` skill（开世界，索引 scale，复用 browser_auto 先例）；操作工具例外（扁平 perceive/act，95% 步要用、需主 loop 状态）。
- `Profile.EnabledTools`=可见层（仅 builtin），叠加 `[tools].enabled`=注册层；MCP 豁免、归 `Profile.Plugins`。正交。
- 第一步永远 Step 0；目标 ~15-18（可压 ~13-15）与触发条件引用基线。
- P2 必做、`unified_ops` flag 灰度、数据驱动轻→重。
- 闭世界 use_group/GroupMap 已弃用；新能力（含应用市场）一律落 skill 目录、自动索引。
