# hiq coWork 实施规划

> 版本: v2.2 | 最后更新: 2026-06-23 | 状态: Phase 0–3 + 后续 5 项全部完成，待真机测试
>
> **本文档是 coWork 的唯一真相来源**，反映代码实际状态。v1.0（原 18 周/9 Phase 全量规划）已废弃，归档于 git 历史。

---

## 一、产品定位与策略

hiq coWork 是 hiq 的办公智能体模式。核心诉求：「一个接口直接切换成 coWork」。实现方式：**profile 切换**——一个 profile 是一束 `boot.Options` 覆盖（model + system prompt + skills + tools + MCP servers），切换时复用已证明的 `SetModelForTab` rebuild 流程。

### 策略：MVP 先行（非 v1.0 的全量并行）

v1.0 原计划 18 周/9 Phase 全量并行。v2.0 改为：

1. **先把 profile 开关做出来**（1-2 周，有 `SetModelForTab` 现成先例）——开关本身即交付价值
2. **第一批 cowork 能力只做用户选定的「必须有」**：浏览器自动化 + 桌面自动化
3. **后续能力独立排期、各自上线**，不绑架 roadmap
4. **删掉** Skill 开放市场（风险/收益不划算）和 Live Artifacts 第一版（依赖过重）

### v1.0 事实修正（动工前已用代码核实）

| v1.0 说法 | 代码事实 | 影响 |
|---|---|---|
| 「config 有 profile section 可扩展」| `config.go`/`example.toml` **没有** `[profile]` | profile section 从零设计 |
| 「web_search 是 MCP 通道」| `websearch.go`/`webfetch.go` 是编译进 Go 的 built-in（Brave/Exa/Linkup/AnySearch），非 MCP | 架构图纠错 |
| 「ppt 复用」| `wps-ppt-mcp-server` 是仓库外独立 Python server，hiq **零引用** | 接入是新工作（但省 greenfield）|
| 「App.tsx 2700 行必须先拆」| 2705 行属实，但 `AppChrome`/`ProjectTree`/`Transcript`/`Composer`/`WorkspacePanel` 已是独立组件 | 不必先全量拆，增量改即可 |
| 「SetSkillEnabled 可 live 切」| `controller.go:2219` docstring 明说**需 rebuild 才生效** | profile 统一走 rebuild |

---

## 二、架构总则

**profile = 一束 `boot.Options` 覆盖**。切换流程（复用 `desktop/app.go:3444` 的 `SetModelForTab` 先例）：

```
acquireSharedHost  →  snapshot 历史  →  Ctrl.Close()
        →  boot.Build(profile 派生的 Options)  →  rebind  →  Resume
```

`shared_host.go` 的 refcount 保证 MCP 子进程不被 teardown 杀掉；`boot.go:683` 重建 session；controller.go resume 历史。这个流程被生产代码证明，profile 切换是低风险、有先例的小机制。

### cowork profile 注册的工具（boot.go，仅 `opts.Profile.Name == "cowork"` 时）

| 类别 | 工具 | 平台/配置依赖 |
|---|---|---|
| 浏览器（11）| `browser_*` 全套（含 snapshot/select/set_path）| 需 Chromium 内核浏览器；**built-in，dev + cowork 都可用**（与 web_search 同级）|
| 桌面（5）| `screenshot`/`screen_click`/`screen_type`/`screen_scroll`/`get_ui_tree`（含子控件枚举）| **仅 Windows**（Win32 BitBlt/SendInput/EnumWindows/EnumChildWindows）|
| PPT（23）| `mcp__wps-ppt__*`（via wps-ppt-mcp-server）| 需 `[cowork] wps_ppt_server_path` + Python(fastmcp/pywin32) + WPS Office |
| 定时（4）| `schedule_create`/`list`/`delete`/`update`（含 IM/file 推送）| 需 desktop app 注入 Runner（CLI/TUI 下报 offline）|
| 邮件（3）| `email_send`（SMTP）/ `email_read`（IMAP）/ `email_search`（IMAP FROM）| send 需 `[cowork.smtp]`；read/search 需 `[cowork.imap]` |
| RAG（4）| `rag_import`/`search`（FTS5 + 可选语义 rerank）/`list`/`delete`| 需 desktop app 注入 Store；语义 rerank 需 `[cowork] embedding_model` |
| 文档（7）| `doc_read`（含 docx/xlsx/pptx 解析）/`doc_write`/`csv_read`/`csv_write`/`xlsx_read`/`xlsx_write`/`doc_convert`| 无依赖（纯 stdlib，OOXML zip+XML 解析）|
| 既有 | `image_understand`/`web_search`/`web_fetch`/`task`/`parallel_tasks`/`read_file`/`write_file`/`bash`...| 全平台 |
| IM bot | 飞书/QQ/微信（`internal/bot/`，含 `gw.Push` 出站推送）| 已有，3 套真实适配器 |

**技能**：`browser-auto` / `desktop-auto` / `ppt-wizard`（3 个 cowork 专属 subagent/inline）+ 既有 `explore`/`research`/`review`/`test` 等。

---

## 三、已实现内容（Phase 0–3）

### Phase 0 — Profile 切换机制 ✅

**后端：**
- `internal/config/profile.go`（新）— `Profile` 类型 + 内置 `dev`/`cowork` 默认 + `ResolveProfile`/`PluginAllowedByProfile`/`ResolveSkillDisabled`，5 个单元测试
- `config.go` — `Config.Profiles []Profile`（TOML `[[profiles]]`）+ `CoworkConfig`（`browser_path`/`wps_ppt_server_path`/`wps_ppt_python`/`SMTP`/`SMTPConfig`）
- `boot.go` — `Options.Profile` 字段 + 5 处覆盖（model/effort/system-prompt/skills/plugins）
- `desktop/app.go` — `SwitchProfile`/`SwitchProfileForTab`（复刻 rebuild 流程）+ `Profile`/`ProfileForTab`/`Profiles` 查询 + `profile:changed` 事件
- `desktop/tabs.go` — `WorkspaceTab.profile` 持久化 + `TabMeta.Profile` 下发前端

**前端：**
- `lib/profile.tsx`（新）— `ProfileProvider` + `useProfile`
- `lib/bridge.ts` — `onProfileChanged` + `ProfileInfo` 类型 + 5 个 binding + dev mock
- `layouts/CoWorkLayout.tsx`（新）— 三栏骨架（DocLibrary/TaskCenter/DocPreview）+ 截屏分析按钮
- `App.tsx` — `coworkActive` state + 同步 effect + `switchProfile` handler + `.app--cowork` 类
- `AppChrome.tsx` — 顶栏 profile 切换按钮（dev ↔ cowork）
- `styles.css` — cowork 布局网格 + 标准 body 隐藏 + 切换按钮样式
- `locales/{en,zh}.ts` — 21 个 `cowork.*` i18n key

**验收**：切换 < 2s、历史 100% 保留、MCP 子进程不被杀、cowork prompt 与 dev 不同。

### Phase 1 — 浏览器自动化 ✅

**`internal/tool/builtin/browser.go` + `browserdetect.go` + `browsersnapshot.go`**（11 工具）：
- 会话池：进程级单例 chromedp allocator（共享，不每 tab 启独立 chrome）+ 10 分钟空闲回收 + 30s 操作超时 + `CHROME_PATH` 覆盖
- 浏览器自动发现：Chrome → Edge → Brave（跨平台候选路径）+ `[cowork] browser_path` 持久化 + `browser_set_path` 引导闭环（找不到→ask 用户→set_path→重试）
- **ref 系统**（对标 Playwright MCP）：`browser_snapshot` 返回无障碍树 + ref(e1..)，`click`/`type`/`select_option` 接受 ref（dom.ResolveNode + runtime.CallFunctionOn）；navigate 后 refs 自动失效
- React/Vue 兼容：type 用原生 setter + dispatch input/change 事件
- 7 个单元测试（roster/readOnly/looksLikeRef/selectorFromArgs/displayName/verifyExe/upsert）

**依赖**：`chromedp`（纯 Go，零 CGO）—— **go.mod 唯一新增依赖**

### Phase 2 — 桌面自动化（Windows 原生）✅

**`screen_windows.go` + `screen_other.go`（stub）+ `uitree_windows.go`**（5 工具）：
- `screenshot`：BitBlt + GetDIBits（BGRA→RGBA），存 PNG + base64 缩略图
- `screen_click`/`type`/`scroll`：SetCursorPos + SendInput（鼠标按键/双击/Unicode 键盘/滚轮）
- `get_ui_tree`：EnumWindows + GetWindowRect + GetWindowText，返回可见窗口的标题/类名/精确矩形（VLM 定位辅助）
- 跨平台：非 Windows `ScreenTools()` 返回 nil，cowork 仍可用（浏览器+VLM），仅缺桌面控制
- `desktop-auto` skill（截图→image_understand→get_ui_tree 精确定位→操作→再截图验证）
- 3 个测试（roster/readOnly/absInt）

**路线决定**：弃 robotgo（CGO 构建脆弱），用 Win32 syscall（`golang.org/x/sys/windows` 已直接依赖，go-ole 已 indirect）。元素级 UIA COM 留作后续（当前做窗口级树，覆盖主要痛点）。

### Phase 3 — 办公能力矩阵 ✅

**PPT（接入 wps-ppt-mcp-server）**：
- `config.Cowork.WPSPPTServerPath` + `builtinmcp.WPSPPTEntry()`（stdio MCP，background tier）+ `boot.go` 去重注入 + `EnsureWPSPPTDeps`（pip install）
- `ppt-wizard` skill（前置依赖检查 + 14 元素/12 布局/4 预设）
- 5 个测试

**定时任务**（`internal/scheduler/`，新包）：
- 表达式引擎：`every 30m` / `daily 09:00 Mon-Fri` / 5 字段 cron，**自研无 robfig 依赖**
- JSON 持久化（重启保留）+ desktop 启动加载 + Runner 桥接活动 controller
- 4 工具 + 11 个测试（含跨重启持久化、CJK、mid-run Update 回归）

**自动化面板（定时任务 UI + 引擎升级）** ✅：
- 表达式引擎升级：新增 `at 2026-06-24 15:00`（一次性，触发后自动停用）+ `in 2h30m`/`in 3d`（相对偏移，存储前归一化为绝对 `at`）。
  `reltime.go` 解析中文相对词（后天/下周X/下午3点 等）→绝对时间戳。`NormalizeExpression`/`IsOneShot`/`Describe`（中文人化）导出。
- 运行历史：环形缓冲（最近 100 条，`scheduled_tasks.json.history`）+ `schedule_history` 工具 + `RunNow`（立即运行，不影响计划）。
- 投递模式：`OutputMode` 扩为 `"" | "im" | "email" | "notify" | "file"`。新增 `EmailSender`/`Notifier` 接口（SMTP / `runtime.EventsEmit` toast）。
- OneShot 自动停用：触发后 `Enabled=false`，保留记录；`Load` 检测过期 one-shot 自动停用；拒绝过去时间。
- 5 个内置模板（`templates.go`）：日报/周报/会议提醒 + 数据抓取 + 系统巡检。
- Go→前端桥（`desktop/scheduler_app.go`）：10 个导出方法 + `scheduler:changed`/`scheduler:notice` 事件 + 实时预览（`PreviewSchedule`）。
- 前端（`components/cowork/`）：`AutomationPanel` + `TaskForm`（三段式）+ `TaskCard` + `RunHistory`（抽屉）。50+ i18n key（zh/en）。
- 测试：scheduler 包新增 at/in/reltime/normalize 测试（共 20+ 个）。


**邮件**（`email.go`）：
- `email_send`（SMTP）：text/html、CC/BCC、附件、TLS/STARTTLS、密码走环境变量
- 4 个测试。**IMAP read/search 留作后续**

**RAG 知识库**（`internal/rag/`，新包）：
- FTS5 全文检索（CJK-aware，复用 memory tokenizer 经验）
- 文档导入→分块→按 collection 检索→删除
- 4 工具 + 6 个测试。**embedding 向量层留作后续接口扩展**（FTS5 是 Phase 3 的 80% 价值，零新依赖）

**RAG 知识库面板（分层检索 v2：FTS5 + 结构化抽取）** ✅：
- **痛点与决策**：FTS5 切块在跨段事实组合上精度弱；研究 Hyper-Extract 后借鉴其"抽取+合并"思路，但**纯 Go 自研**（不引入 Python/faiss/langchain）。FTS5（即时）与结构化抽取（显式触发，用户控成本）并存。
- **数据层（`entities.go`）**：SQLite 新增 rag_jobs/rag_chunks/rag_entities/rag_relations 4 表。SIMPLE 合并（normalizeName=lower+trim；同义实体不合并，留作未来 LLM 增强）。
- **抽取引擎（`extract.go` + `platform_extractor.go`）**：队列+限速 worker（默认串行+3s 间隔）+ 指数退避重试 + slidingWindow 滑动平均 ETA。平台 API/OpenAI `/chat/completions` + json_object + 围栏剥离容错。
- **工具升级**：`rag_search` 返回 FTS5+结构化两层合并；新增 `rag_graph`（纯结构化查询）。
- **前端（`components/cowork/`）**：`RagPanel`（导入+collection+检索框+树+拖拽）+ `RagNode`（递归树+状态徽章+进度条+ETA hover Tooltip）。`desktop/rag_app.go` 9 个桥方法+2 事件。`[cowork] extract_model/interval/concurrency` 配置。
- 12 个新测试（合并/同义/关系/空跳过/job/全失败/JSON/围栏/pipeline 端到端/重试/取消/滑动窗口）。


**文档处理**（`document.go`）：
- `doc_read`/`write`（csv/json/md/txt/html）+ `csv_read`/`write` + `doc_convert`（md↔html, json 美化）
- 6 个测试。**二进制 docx/xlsx 留作后续**（需 unioffice/excelize；ppt 已走 WPS COM）

**IM 增强**：scheduler 结果在 `schedule_list` 可见（last_result）；full IM push 留作后续。

### BUG 修复轮（专项审查后）✅

| # | 严重度 | 问题 | 修复 |
|---|---|---|---|
| 1 | 🔴 数据竞争 | browser `s.refs` 无锁读写（navigate 清 nil 时 snapshot/click 并发读 race）| `atomic.Pointer[snapshotRefs]` 无锁化 |
| 2 | 🔴 数据错误 | scheduler `fireDue` 用运行前捕获的旧 Expression 覆盖 mid-run Update | 持锁重读 `s.tasks[idx].Expression` + 回归测试 |
| 3 | 🟡 数据错误 | `SwitchProfileForTab` effortOverride 没归一化（切到不支持 "high" 的模型传非法值）| 加 `NormalizeEffort`（对齐 SetModel）+ 修双重 clone |
| 4 | 🟡 配置矛盾 | `email_send` 说 `[smtp]` 但读 `[cowork.smtp]`（用户照提示配会被 TOML 静默丢弃）| 文案全改 + example.toml 补文档 |
| 5 | 🟢 软矛盾 | cowork prompt addon 无条件承诺 screen/ppt/email，但工具依赖平台/配置 | 改条件描述 |

---

## 四、配置参考

### `[cowork]` 配置（全可选，空 = 自动探测/禁用）

```toml
[cowork]
# 浏览器路径（browser_* 工具）。空 = 自动发现 Chrome→Edge→Brave
browser_path = ""

# PPT 生成（wps-ppt-mcp-server）。需 Python + fastmcp/pywin32 + WPS Office
wps_ppt_server_path = ""
wps_ppt_python = ""   # 可选，指定 venv 的 python

# 出站邮件（email_send）。密码走环境变量，不存 TOML
[cowork.smtp]
host         = "smtp.example.com"
port         = 587
from         = "agent@example.com"
username     = "agent@example.com"
password_env = "COWORK_SMTP_PASSWORD"
use_tls      = false   # true=隐式 TLS(465), false=STARTTLS(587)/plain(25)

# 入站邮件（email_read/search）。空 host = 禁用读取
[cowork.imap]
host         = "imap.example.com"
port         = 993     # 993=隐式 TLS, 143=STARTTLS/plain
username     = "agent@example.com"
password_env = "COWORK_IMAP_PASSWORD"

# RAG 语义检索（可选）。配了 rag_search 会用 embedding 重排 FTS5 结果；空=纯 FTS5
embedding_model = ""   # 一个支持 embedding 的 provider model ref
```

### `[[profiles]]` 可选（覆盖内置 dev/cowork）

```toml
[[profiles]]
name = "cowork"
display_name = "My Office"
model = "qwen/qwen3-max"                # 可选：pin 模型
system_prompt_addon = ""               # 可选：追加 prompt
enabled_skills = []                    # 可选：skill 白名单
plugins = ["wps-ppt"]                  # 可选：plugin 白名单
workspace_type = "document"
```

---

## 五、待办与后续

### 真机测试（当前优先）
1. profile 切换 rebuild 时序（切 cowork→布局变+工具出现，切回 dev 还原）
2. 浏览器 snapshot+ref 闭环（snapshot → 用 ref click/type）
3. 桌面截图 + image_understand（Win32 BitBlt）
4. RAG（导入 md → rag_search 检索）
5. 定时任务（建 every 1m → 1 分钟后看 schedule_list 的 last_result）
6. **自动化面板 UI**：侧栏点「自动化」→ 任务卡片列表 + 模板快选 → 新建任务（试相对词"后天下午3点"看预览）→ 立即运行 → 查运行历史抽屉
7. **RAG 知识库面板**：侧栏点「资料库」→ 导入文件夹（看 FTS5 秒级就绪）→ 点⚡深度提取（看进度条+ETA hover）→ 完成后检索框查实体 → rag_search 验证双层命中
8. PPT（配好 wps_ppt_server_path 后做 3 页 PPT）
9. 邮件（配好 [cowork.smtp] 后发测试邮件）

### 后续能力扩展（v2.1 全部完成 ✅）

| 能力 | 现状 | 实现方式 |
|---|---|---|
| 真 .docx/.xlsx/pptx 二进制 | ✅ | `officedoc.go`：**xlsx 读写用 excelize**（样式/公式/多 sheet 全支持）；docx/pptx 文本提取用 stdlib（archive/zip + encoding/xml，无轻量 docx 库替代）。写 .docx 仍不支持（用源 app 或 ppt 工具）|
| 元素级 UI 树 | ✅ | `get_ui_tree` 用 EnumChildWindows 枚举窗口子控件（按钮/编辑框/标签 + 精确 rect），VLM 可点控件中心而非猜坐标。full IUIAutomation COM 未做（EnumChildWindows 覆盖主要痛点，零 COM 依赖）|
| embedding 向量层 | ✅ | `rag/embedding.go`：rag.Search 过取 top_k×4 → `Store.Rerank` 用 embedding cosine + BM25 混合重排。`[cowork] embedding_model` 配置平台 embedder；空 = 纯 FTS5（graceful degradation）|
| IMAP 邮件读取 | ✅ | `email_imap.go`：**go-imap + go-message**（协议级正确：完整 SEARCH、RFC 2047 编码头解码、multipart MIME、字符集转换）。`email_read`/`email_search` 工具，`[cowork.imap]` 配置 |
| IM push 全打通 | ✅ | `bot.BotGateway.Push(dest, text)` 出站推送；scheduler `OutputMode="im"`（dest=`platform:chatID`）/`"file"` 自动路由；desktop 懒绑定 botGW（bot 后启动也能推）|
| 自动化面板（定时任务 UI） | ✅ | `internal/scheduler/` 引擎升级（at/in + 中文相对词 `reltime.go` + OneShot + 历史环形缓冲）；投递扩 email/notify；5 个内置模板；`desktop/scheduler_app.go` 10 个桥方法 + 2 个事件；`components/cowork/` 四件套（AutomationPanel/TaskForm/TaskCard/RunHistory）+ 50+ i18n key |
| RAG 知识库面板（分层检索） | ✅ | 借鉴 Hyper-Extract 但纯 Go 自研：FTS5（即时）+ 结构化抽取（显式触发）。`entities.go` 4 表 + SIMPLE 合并；`extract.go` 队列+限速+重试+滑动平均 ETA；`platform_extractor.go` 走 `/chat/completions`；rag_search 双层合并 + rag_graph 工具；`desktop/rag_app.go` 9 桥方法+2 事件；`RagPanel`/`RagNode`（递归树+进度条+ETA Tooltip+拖拽）。12 测试 |

---

## 六、验证状态（截至 v2.1）

| 检查 | 结果 |
|---|---|
| `go build ./...` + `desktop go build` | ✅ |
| 8 个 Go 测试套件（config/boot/skill/tool-builtin/builtinmcp/scheduler/rag/bot）| ✅ 全绿 |
| `go test -race` | ✅ 无数据竞争 |
| `desktop go mod tidy` | ✅ |
| 前端 `tsc --noEmit` + `npm run build` | ✅ |
| go.mod 新增依赖 | `chromedp`（浏览器）、`excelize/v2`（xlsx）、`go-imap` + `go-message`（IMAP 邮件）。scheduler/rag/email-send/document 全 stdlib |
| 主进程 CGO | 零新增（Windows 原生 syscall）|

---

## 七、与 v1.0 的差异（为何重写）

v1.0（git 历史）的 18 周/9 Phase 全量规划存在三个问题，v2.0 修正了：

1. **贪大求全 + 时间线乐观**：6 大引擎里 5 个是 greenfield 还带新 CGO/COM 依赖。v2.0 先交付开关本身（1-2 周），再聚焦用户选定的两项核心能力（浏览器/桌面），其余独立排期。
2. **把小机制埋进大计划**：profile 开关有 `SetModelForTab` 现成先例，本应 1-2 周独立交付，却被当作 4.5 个月大计划的前置。v2.0 把它独立出来先做。
3. **依赖现实风险低估**：v1.0 选 robotgo（CGO 跨平台编译脆弱）。v2.0 改 Windows 原生 syscall（`x/sys` 已直接依赖，go-ole 已 indirect），零新增 CGO。
