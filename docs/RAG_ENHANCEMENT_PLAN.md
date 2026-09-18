# 资料库（RAG / 知识库）提升方案

> 版本：v1.1　|　日期：2026-07-10　|　状态：实施中
>
> 本方案基于对 hiq 资料库子系统的六轮代码审计，覆盖：已实现功能面、已知工程问题、测试覆盖与数据迁移、rag 工具与 agent 集成、前端 i18n/性能/a11y。
> 审计方法为逐文件阅读 + 针对性验证，所有结论均带 `file:line` 证据。

## 实施进度

| 波次 | 状态 | 说明 |
|------|------|------|
| **第一波 P0**（数据安全与功能修复） | ✅ 已完成 | P0-1~P0-6 全部实施 + 测试 + 验证通过 |
| **第二波 P1**（前后端接线） | ✅ 已完成 | P1-1~P1-8 全部实施 + typecheck 通过 |
| **第三波 P1.5**（安全与 agent 集成） | ✅ 已完成 | P1.5-1~P1.5-4 全部实施 + go test/build 通过 |
| **第四波 P2**（架构清理） | ✅ 已完成 | P2-1/2/5/6/7 全部实施 + go test/build 通过 |
| **第六波 P3**（i18n/a11y/规模化） | ✅ 完成 | P3-1b/2/3/4/5/6 实施；P3-1a 经核实为非 bug |
| 第五波 P2.5（测试补强） | 🔄 贯穿 | 各波已同步补测试 |

### P0 实施记录（已完成）

| 项 | 改动文件 | 验证 |
|----|---------|------|
| **P0-1** schema 迁移机制 | `internal/rag/store.go`（`Open` 拆分 baseSchema + `migrate()` + `PRAGMA user_version` + `execMigrationStep`/`parseAddColumn`/`columnExists`）、`internal/rag/migrate_test.go`（4 测试） | ✅ 测试通过 |
| **P0-2** 专家团注入防护 | `desktop/experts_app.go`（`ragSearcherAdapter.Search` 包 `<untrusted_content>`）、`internal/experts/orchestrator.go`（`buildExpertPrompt` 加 untrusted 纪律） | ✅ build + experts 测试通过 |
| **P0-3** 重导入去重 + Resume | `internal/rag/extract.go`（`enqueueFile` 加 content-hash 去重 + 新增 `Resume()`）、`internal/rag/entities.go`（`JobStatusForPath`/`ChunksByPath`/`ResumableJobs`）、`desktop/app.go`（启动调 `Resume()`）、`internal/rag/resume_dedup_test.go`（4 测试） | ✅ 测试通过 |
| **P0-5** 语义搜索断链 | `desktop/frontend/.../GraphCanvas.tsx`（切语义模式自动触发 `RagEmbedEntities` + `semanticStatus` 引导提示 + 不再静默吞错）、`styles.css`（`.rag-graph__sem-hint`） | ✅ typecheck 通过 |
| **P0-6** GraphCanvas 性能 | `desktop/frontend/.../GraphCanvas.tsx`（edge 构建 O(n·m)→O(n+m) Map 索引 + `EntityNode` 包 `React.memo`）、`GraphToolbar.tsx`（搜索框 300ms debounce） | ✅ typecheck 通过 |

### P1 实施记录（已完成）

| 项 | 改动文件 | 验证 |
|----|---------|------|
| **P1-1/2/3** RagNode 挂载 + 取消按钮 + progress 事件 | `CoworkDock.tsx`（用 `RagNode` 替换简版 `RagFileTree`，接 `RagStartExtract`/`RagCancelExtract`/`RagRemovePath` + 订阅 `onRagProgress`/`onRagChanged` 事件驱动刷新，删除死代码 `RagFileTree`）、`RagNode.tsx`（加 `onFileClick` prop，文件标签可点开预览） | ✅ typecheck |
| **P1-4** 静默吞错→toast | `TemplateSelect.tsx`（2 处 catch 改 `showToast`）、`EntityEditModal.tsx`（2 处 catch 改 `showToast`） | ✅ typecheck |
| **P1-5** HE 离线引导 | `TemplateSelect.tsx`（状态文案区分"模板抽取"vs"平台回退"，明确告知行为而非无声 fallback） | ✅ typecheck |
| **P1-6** 格式提示动态化 | `RagPanel.tsx`（调 `RagListTemplates()` 动态渲染支持格式，替代写死的"md/docx/pdf/xlsx"） | ✅ typecheck |
| **P1-7** 反向高亮断链修复 | `GraphCanvas.tsx`（监听 `rag:highlight-node` 事件，设 `highlightedName` 闪亮 + scrollIntoView 聚焦节点，2.5s 后清除） | ✅ typecheck |
| **P1-8** "全不选"假操作修复 | `CoworkDock.tsx`（清除 activeCollection + 加 title 说明搜索覆盖全部集合，消除"限定范围"误解） | ✅ typecheck |

### P1.5 实施记录（已完成）

| 项 | 改动文件 | 验证 |
|----|---------|------|
| **P1.5-1** SessionRAGContext 与 agent 贯通 | `internal/tool/builtin/rag.go`（新增 `SetRAGSessionResolver` + `resolveRAGScope`，`rag_search.Execute` 在 collection 参数为空时 fallback 到 session 唯一活跃集合）、`desktop/app.go`（启动注入 resolver 回调） | ✅ go test + build |
| **P1.5-2** rag_graph 死工具接入 | `internal/skill/builtins.go`（rag-auto AllowedTools 加入 `rag_graph`，body 补充 rag_graph 使用指引） | ✅ go test + build |
| **P1.5-3** 返回总长度上限 | `internal/tool/builtin/rag.go`（新增 `capOutput` 12000 字符截断 + 截断标记，应用于 rag_search 和 rag_graph 输出） | ✅ go test + build |
| **P1.5-4** 专家团多 collection | `internal/experts/orchestrator.go`（遍历全部 `RAGCollections` 搜索并合并结果，不再只取 `[0]`） | ✅ go test + build |

### P2 实施记录（已完成）

| 项 | 改动文件 | 验证 |
|----|---------|------|
| **P2-1** markitdown 公共包 | 新建 `internal/docconv/docconv.go`（统一 `FindScript`/`ConvertFile`/`ConvertText`，参数化脚本名使 OCR 也复用），三处调用点改为薄封装：`internal/rag/officedoc.go`、`internal/tool/builtin/officedoc.go`、`internal/control/refs.go`，清理各自未用的导入；新建 `docconv_test.go`（3 测试） | ✅ go test + build |
| **P2-2** 删除死代码 | 删除 `internal/rag/extract_queue.go`（273 行，`NewExtractQueue` 零调用方，经 grep 全仓确认无引用） | ✅ go test + build |
| **P2-5** HE 进程清理 | `desktop/app.go` shutdown 方法新增 `ragPipeline.Stop()` + `heService.Stop()`（此前 `Stop()` 定义了但从未调用，Python 进程在 Windows 上成孤儿） | ✅ go build |
| **P2-6** HE 端口配置化 + 占用检测 | `internal/config/config.go`（CoworkConfig 加 `HEPort`）、`desktop/app.go`（启动读配置传端口）、`desktop/he_service.go`（Start 前 `net.Listen` 探测端口占用，冲突时报清晰错误并指引 `[cowork] he_port`）、`hiq.example.toml`（加 he_port 示例） | ✅ go build |
| **P2-7** pruneSourcesByPath LIKE 转义 | `internal/rag/store.go`（LIKE 通配符 `%`/`_`/`\` 转义 + `ESCAPE '\'`，消除路径含通配符时的候选集误扩大） | ✅ go test |

### P3 实施记录（部分完成）

| 项 | 改动文件 | 验证 |
|----|---------|------|
| **P3-1b** 共享实体类型常量 | 新建 `entityTypes.ts`（ENTITY_TYPES/ENTITY_TYPE_COLORS/ENTITY_TYPE_LABELS + colorFor/labelFor），消除 GraphCanvas/GraphToolbar/GraphLegend/TemplateSelect 四处重复定义（~30 行） | ✅ typecheck |
| **P3-2** 着色同色修复 | `entityTypes.ts` + `GraphCanvas.tsx`（改用 Okabe-Ito 色盲友好色板，11 种类型各一独立色，修复 product=organization、technology=project=location、concept=topic 三组同色） | ✅ typecheck |
| **P3-3** 核心反馈 a11y | `GraphCanvas.tsx`（加载态加 `aria-live=polite aria-busy=true`）、`RagPanel.tsx`（导入区加 `role=region aria-label`）、`styles.css`（加 `.sr-only` 工具类）、确认 toast 容器 + 语义提示已有 live region | ✅ typecheck |
| **P3-1a** CoworkDock fallback | 经核实**非 bug**：`zh.ts` 含全部 coworkDock.* 键，`t()` 在中文环境返回中文值，`|| "中文"` 仅在键意外缺失时兜底。en/zh 键集 diff 确认完全对等。 | ✅ 核实 |
| **P3-5** FTS5 中文 bigram | `internal/rag/store.go`（新增 `expandCJKBigrams` 索引侧转换 + `cjkBigrams` 查询侧转换，CJK 连续字符展开为重叠 bigram，latin↔CJK 边界插空格；"渲染管线"→索引"渲染 染管 管线"，搜"管线"可命中）、2 个新测试（子串召回 + 中英混排） | ✅ go test |
| **P3-6** 向量检索规模熔断 | `internal/rag/entities.go`（`SearchEntitiesByVector` 加 `maxVectorScanEntities=10000` 计数守卫 + `ErrVectorScaleExceeded`）、`desktop/rag_app.go`（捕获该错误转为用户友好提示"请改用关键词搜索"） | ✅ go build |
| **P3-4** 响应式布局 | `styles.css`（新增 940px/760px 两个 `@media` 断点：工具栏搜索框 min-width + 选择器收窄、图例竖排、窄屏隐藏筛选/选择模式按钮、React Flow 控件移至左下避免遮挡） | ✅ typecheck |
| **P3-1** i18n 核心文案 | `locales/zh.ts` + `en.ts`（新增 10 个 cowork.rag* 键：图谱加载/空态/空态提示/拖拽区/导入启动/Obsidian导出/语义搜索提示×2）、`GraphCanvas.tsx` + `RagPanel.tsx`（接入 useT，替换硬编码中文） | ✅ typecheck |

---

## 一、现状评估

### 1.1 架构概览（已实现，运转中）

资料库采用 **三层融合架构**，串联 Swarm-OS 的三个组件：

```
markitdown(Python 文档解析)  →  hiq RAG(Go 存储/编排/检索)  →  Hyper-Extract(Python 抽取/向量化/摘要)
        ①                           ②                                 ③
```

- **① 文档解析层**：`internal/rag/officedoc.go` + `doc_converter.py`(markitdown) + `ocr_pdf.py`(PaddleOCR)。PDF 走 OCR 管线；docx/xlsx/pptx 走 markitdown→Go stdlib 回退；epub/xls/msg 仅 markitdown。
- **② 存储与编排层（核心）**：`internal/rag/store.go` 单文件 SQLite + FTS5，7 张表。FTS5 全文检索是永久基线（永不断电）；结构化实体/关系是 LLM 抽取的增值层。
- **③ 知识抽取层**：双路径——Go 原生 `PlatformExtractor`（直调平台 API，两阶段抽取，chunk 级 + RPM 限流）为主路径；Hyper-Extract Python 服务（`hyper_extract_server.py:18900`）为可选增强，提供模板抽取/embedding/summarize。

**关键设计**：FTS5 即时索引（秒级，用户导入即可搜索）+ 异步深度抽取（实体/关系/图谱），双层容灾——哪怕没配 LLM、没装 Python，基础检索始终可用。

### 1.2 已交付能力清单（成熟度高）

`RAG_KNOWLEDGE_BASE_OVERHAUL` v4.0 四阶段基本全落地：

| 能力 | 实现位置 | 状态 |
|------|---------|------|
| FTS5 全文检索（CJK 感知分词） | `store.go:860` | ✅ |
| 结构化实体/关系抽取（两阶段 LLM） | `platform_extractor.go` | ✅ |
| embedding 持久缓存 + 混合重排（BM25×0.5 + 余弦×0.5） | `embedding.go` | ✅ |
| 知识图谱可视化（React Flow 交互式） | `GraphCanvas.tsx` | ✅ |
| 实体编辑/合并/原文溯源 | `EntityDetail.tsx` / `rag_app.go:970,998,1010` | ✅ |
| Obsidian vault 导出 | `obsidian.go` / `rag_app.go:1082` | ✅ |
| 知识引用 → skill 注入 | `rag_app.go:1057,1070` | ✅ |
| 专家团 RAG 注入（Phase 4） | `experts/orchestrator.go:186` | ✅（有缺口，见 P0-2） |
| 表格表头驻留分块 / CJK rune 安全切片 | `store.go` chunkTabular/windowChunk | ✅ |
| 删除级联（FTS+实体+关系+jobs+chunks） | `store.go:322` | ✅ |

### 1.3 问题分布概览

经六轮审计，发现问题集中在五类：

| 类别 | 问题数 | 核心矛盾 |
|------|--------|---------|
| 数据安全与成本 | 3 | 重导入烧配额、重启丢任务、加列崩溃 |
| 前后端断链 | 8 | 后端有能力/前端没接线，功能"看起来坏了" |
| 架构与死代码 | 7 | 三套并发逻辑、markitdown 重复三份、死代码 |
| 安全与 agent 集成 | 5 | 专家团注入无防护、集合选择断层、死工具 |
| 性能与体验 | 12 | 全量加载卡顿、i18n 裸奔、着色同色、零 a11y |

---

## 二、P0 — 数据安全与功能性故障（立即修）

### P0-1　无 schema 迁移机制，加列型变更会让旧数据库运行时崩溃

**问题**：`store.go:39-140` 用 `CREATE TABLE IF NOT EXISTS` 一次性建全部 7 表，**无 `PRAGMA user_version`、无 `ALTER TABLE`、无版本号**。`IF NOT EXISTS` 对已存在的表完全无效——下次给任何表加列，旧用户的 `rag.db` 该表已存在，新列永远不会创建，运行时 SQL 报错。`store.go:36-38` 注释声称"new tables simply add alongside"，这只对加表成立，对加列是假的。

**影响**：任何未来的 schema 演进（如给 `rag_entities` 加 `aliases` 列）都会让升级用户的资料库直接不可用。当前 schema 已 7 张表，这是必然到来的地雷。

**提升内容**：
1. `store.go:Open()` 后读取 `PRAGMA user_version`，执行 forward-only 迁移循环。
2. 每个版本号对应一组 `ALTER TABLE ADD COLUMN` / 补建索引语句，包在 `IF NOT EXISTS` 逻辑里（SQLite 不支持 `ADD COLUMN IF NOT EXISTS`，需先查 `pragma_table_info`）。
3. 迁移完成后 `PRAGMA user_version = N`。
4. 加测试：用旧 schema 建库 → 升版本 → 验证迁移正确。

### P0-2　专家团 RAG 注入无 prompt-injection 防护

**问题**：同一份知识库内容，走 `rag_search` 工具会包 `<untrusted_content>` 标签（`rag.go:284`），但走专家团路径（`experts/orchestrator.go:186-196` → `desktop/experts_app.go:545-572 ragSearcherAdapter.Search`）是**裸文本**拼进每个 expert 的 task。expert 的 system prompt（`orchestrator.go:364-387 buildExpertPrompt`）也无 untrusted 指令。导入文档里若藏 prompt injection，会未经防护地进入所有 expert。

**影响**：安全漏洞。用户导入的不可信文档可劫持专家团的 expert 行为。

**提升内容**：
1. `desktop/experts_app.go:545` 的 `ragSearcherAdapter.Search` 返回值包裹 `wrapUntrusted("rag", content)`。
2. `buildExpertPrompt`（`orchestrator.go:364`）补充 untrusted content 指令（参照 `profile.go:118` cowork prompt 末尾写法）。
3. 附带修：`orchestrator.go:190` 只取 `RAGCollections[0]` → 支持多 collection；`topK=3` 硬编码 → 提取为配置项。

### P0-3　重新导入同一文档触发完整重抽取 + 重置进度（烧配额）

**问题**：`extract.go:236-284 enqueueFile` 无内容变更判断；`entities.go:448 CreateJob` 的 `ON CONFLICT(collection,path) DO UPDATE` 把 `done_chunks=0`、删旧 chunk 行。每次对同一文件点"导入"都触发完整重抽取。且 chunk 文本不持久化（`entities.go:466-471` 注释承认只在内存），重导入+中断 = 数据丢失。

**影响**：用户误操作或 UI 重复触发即烧 LLM 配额。

**提升内容**：`enqueueFile` 前比对 content-hash（FTS5 已有内容可重算 hash）或文件 mtime，未变则跳过抽取（FTS 层幂等已 OK，只补抽取层去重）。加测试覆盖 ON CONFLICT 路径。

### P0-4　Pipeline.Resume 不存在，重启丢失所有在途抽取

**问题**：代码注释在 4 处承诺重启恢复（`extract.go:20,178`、`entities.go:432,582`），`PendingChunksForJob` 已实现但**零调用方**，`Resume` 函数根本不存在。重启后 `rag_jobs` 里 pending/extracting 状态的 job 永远卡住，UI 显示假进度，无 worker 在跑，无告警。

**影响**：用户导入大文件夹后重启 = 静默丢失所有未完成抽取。

**提升内容**：
1. 实现 `Pipeline.Resume`：`Start()` 时遍历 `rag_jobs` 中 status=pending/extracting，用 `PendingChunksForJob` 取 chunk，从 FTS5 按 (path, chunk_idx) 回读文本重建队列。
2. 前端 `initRAG` 后触发一次"恢复中"状态提示。
3. 加测试。

### P0-5　语义搜索永远返回空（前后端断链）

**问题**：`GraphCanvas.tsx:147` 调 `RagSemanticSearch`，但前端**从不调** `RagEmbedEntities` 生成实体向量，后端 `rag_app.go:536` 按 `"he"` 向量库查无 embedding → 永远空。失败原因被 `:152` 静默吞掉。用户切"义"搜索以为功能坏了。

**提升内容**：
1. 抽取完成后自动触发 `RagEmbedEntities`（或 UI 提供"生成嵌入"按钮）。
2. 语义搜索无 embedding 时给"请先生成嵌入（需 HE 在线）"引导，而非静默空。
3. 此项与 P1-3（progress 事件）关联——抽取完成后的事件可联动触发嵌入。

### P0-6　GraphCanvas 性能地雷（大图谱卡死 + 搜索风暴）

**问题（三合一）**：
1. `GraphCanvas.tsx:135` 全量 `GetGraphData`，未用已暴露的 `GetGraphDataPaged`（`bridge.ts:391`），2000+ 节点一次性加载。
2. `GraphCanvas.tsx:161-274` 巨型 effect 在 `searchQuery/selectedEntities/...` 任一变化时全量 `.map()` 重建 nodes+edges；`EntityNode` 未 `React.memo`；edge 构建用 `nodes.find()` 是 **O(n·m)**。
3. 搜索框无 debounce（`GraphToolbar.tsx:93`），语义模式下**每击键一次**就发后端请求（`GraphCanvas.tsx:148,154` 依赖 searchQuery）。

**提升内容**：
1. edge 构建改用 `Map<id, node>` 索引，O(n·m) → O(n+m)。
2. `EntityNode` 包 `React.memo`，按 `data` 浅比较跳过重渲。
3. 搜索框加 debounce（300ms），语义搜索请求加"最小查询长度"门槛。
4. 长期：大图谱改用 `GetGraphDataPaged` 分页加载（阈值：节点数 > 500）。

---

## 三、P1 — 前后端接线与体验（高性价比）

> 这一层的共同特征：**后端能力都已实现，前端代码甚至写好了，只是没接线**。投入极小，用户感知极大。

### P1-1　挂载 RagNode 组件（取消/删除/重提取能力全在但未接线）

`RagNode.tsx`（170 行，带 ETA/进度条/取消/删除/逐文件重提取）是孤儿组件。CoworkDock 改用了自写的精简 `RagFileTree`（`CoworkDock.tsx:637-668`，只有文件名+✓/.../! 符号）。用户在 Dock 内无法对单文件操作。

**提升**：用 `RagNode` 替换精简 `RagFileTree`，接上 `RagRemovePath`(`:434`)/`RagCancelExtract`(`:419`)。这两个 bridge 方法已暴露，前端零调用。

### P1-2　取消按钮从未接线

`RagCancelExtract`（`rag_app.go:419`）已通过 bridge 暴露（`bridge.ts:376`），前端零调用。长任务无法中止。与 P1-1 一起在 RagNode 进度区接"取消"按钮。

### P1-3　rag:progress 实时事件无人订阅

`onRagProgress`（`bridge.ts:625`）全工程无消费者。进度靠 2s 轮询（`TemplateSelect.tsx:133`），且有 5min/150-tick 超时上限（长任务进度会"卡住"）。每个 RagNode 还有独立 3s 定时器——批量提取 50 文件 = 50 个并发定时器。

**提升**：`TemplateSelect`/`RagNode` 改订阅 `rag:progress` 事件驱动更新，轮询仅作兜底；消除 RagNode 多定时器放大。

### P1-4　静默吞错

`TemplateSelect.tsx:116,172`、`EntityEditModal.tsx:49,65` 都 `catch {}` 注释"handled silently"，用户点了"立即理解"失败时**无任何反馈**。

**提升**：catch 里 `showToast` 展示错误 + 可操作建议（如"HE 离线，已改用平台抽取，请等待"）。

### P1-5　HE 离线仍允许点提取

`TemplateSelect` 显示"离线"但按钮不 disable，点击后 fallback 失败又被静默吞 → "点了没反应"。

**提升**：HE 离线时明确标注将走平台回退，或 disable + 提示如何启用 HE。

### P1-6　格式提示错误

`RagPanel.tsx:151` 写死"支持 md/docx/pdf/xlsx"，实际支持 17 种（含代码/网页/csv/epub），且后端有 `RagListTemplates()`(`:661`) 可动态获取却没用。

**提升**：调用 `RagListTemplates()` 动态渲染支持格式。

### P1-7　反向高亮断链

`EntityDetail` 的"在图谱高亮"派发 `rag:highlight-node`（`CoWorkLayout.tsx:174`），但 `GraphCanvas` **没监听**该事件 → 点击无效。

**提升**：`GraphCanvas` 监听 `rag:highlight-node` 并聚焦/高亮节点。

### P1-8　"全不选"是假操作

`CoworkDock.tsx:544-552` 注释明说后端无法表达"none"，"全不选"只是 UI 显示，搜索仍跨所有集合 → 误导用户。

**提升**：实现 `ClearSessionCollections`，或改文案消除歧义。

---

## 四、P1.5 — 安全与 agent 集成

### P1.5-1　SessionRAGContext 与 agent rag_search 完全断层

**问题**：用户在面板"激活某集合"（`SessionRAGContext`）只影响 `App.RagSearch`（`rag_app.go:480`，面板搜索栏），**对 rag-auto 子代理的 `rag_search` 调用毫无影响**（`rag.go:130` 只读参数里的 collection，不引用 SessionRAGContext）。用户以为限定到某集合，LLM 实际搜全部。

**提升**：把 SessionRAGContext 注入到 rag 工具——`ragSearch.Execute` 在 collection 参数为空时 fallback 到 session 活跃集合。需要 rag 工具能访问 session 状态（通过 `builtin.SetRAGStore` 类似的注入机制）。

### P1.5-2　rag_graph / rag_mindmap 完全不可达

两个工具在 boot 时被 `reg.Hide()`（`boot.go:489`），且 `rag-auto` skill 的 `AllowedTools`（`builtins.go:447`）只含 `rag_import/rag_search/rag_list/rag_delete`，不含这两个。等于死代码（仅测试在调）。

**提升**：要么加入 rag-auto 的 AllowedTools（让子代理能用图查询/思维导图），要么删除减维护成本。`rag_mindmap` 自带 depth≤5 防溢出，是唯一带保护的工具，值得接入。

### P1.5-3　rag_search / rag_graph 返回总长度无上限

单段 snippet 有 1200 字符上限（`store.go:594`），但实体层无数量限制——每个 top 实体递归拉全部关系，topic/event 展开成员。极端情况可撑大上下文。

**提升**：输出前对总字符数设上限（如 8KB）+ "…（更多命中已省略）"。

### P1.5-4　专家团只取第一个 collection + task 原文做 query

`orchestrator.go:190` 只取 `RAGCollections[0]`，多 collection 静默丢失；`:192` 把整个 task 原文当 FTS5 query，长任务召回质量差。

**提升**：支持多 collection；query 提取关键词或摘要后再检索。

---

## 五、P2 — 架构与工程质量

### P2-1　markitdown 调用逻辑复制三份

`internal/rag/officedoc.go`、`internal/tool/builtin/officedoc.go`、`internal/control/refs.go` 三处近乎逐字复制 `findDocConverterScript` + `convert*WithMarkitdown`，且扩展名白名单不一致（control 多 `.xls/.html/.htm`，`.msg` 只在 RAG 里）。

**提升**：抽公共包 `internal/docconv`，统一脚本查找+执行+格式白名单。

### P2-2　extract_queue.go（273 行）是死代码

`NewExtractQueue` 全仓无调用方。其并发模型（`processNext`/`maxRun`）与活跃的 Pipeline 不同，会误导维护者以为有两套并发策略。

**提升**：删除。

### P2-3　三套抽取并发逻辑分裂

Pipeline（有限流+持久化+重试）vs `ragStartHEExtract` 内联 goroutine 池（4 并发，**绕过 RPM 限流和 job 持久化**，`rag_app.go:333-415`）vs 死代码 ExtractQueue。

**提升**：让 HE 路径也走 Pipeline 抽象（注入 `HEClient` 作为 `Extractor` 接口实现），统一限流/进度/ETA。

### P2-4　抽取日志全是 Debug 级

关键错误（upsert 失败、prune 统计、mark done 失败）都是 `slog.Debug`（`extract.go:368,373,379`），生产环境默认看不到。无吞吐/失败率/限流等待时长指标。

**提升**：关键路径提到 Info；补错误率/限流等待统计。

### P2-5　HEService.Stop() 从未被调用

`he_service.go:137` 定义了 `Stop()` 但零调用方。应用退出时 `hyper_extract_server.py` Python 进程成孤儿（Windows 尤其明显）。

**提升**：app 退出/`shutdown` 钩子调 `heService.Stop()`。

### P2-6　HE 端口 18900 硬编码

无配置化、无占用检测，冲突时静默禁用整个 HE。

**提升**：`[cowork] he_port` 配置项 + 启动时端口探测。

### P2-7　pruneSourcesByPath 用 LIKE 子串筛选

`store.go:395` 用 `sources LIKE '%path%'` 做候选筛选，path 互为子串时多扫且理论有边角风险（虽后续精确比较兜底）。

**提升**：改为先解析 sources JSON 再按 path 精确过滤。

---

## 六、P2.5 — 测试覆盖补强

> 审计发现多个关键路径**零测试**，是 P0 项之外的最大风险来源。

| 零测试路径 | 风险 | 补测内容 |
|-----------|------|---------|
| `SearchEntitiesByVector`（`entities.go:813`） | 支撑 RagSemanticSearch，float32 编解码 + cosine 全无验证 | 单测：编解码往返、cosine 数学、topK 截断 |
| `CreateJob` 的 ON CONFLICT 重导入（`entities.go:448`） | 复杂 SQL，旧 chunk 清理逻辑 | 单测：重导入→验证 done_chunks 重置 + chunk 清理 |
| markitdown/OCR 子进程回退（`officedoc.go:137,49`） | 零测试，且 `TestBinaryFormatRejected`(`store_test.go:147`) 与现行代码矛盾（断言 docx 被拒，但实际会处理） | 修正矛盾测试 + mock 子进程测回退 |
| `rag_app.go` 1312 行业务逻辑 | 导入/搜索/模板提取/HE 并发/图谱构建/合并，全无集成测试 | 端到端测试（mock store + HE） |
| FTS5 中文召回边界 | `store.go:864` 注释承认 unicode61 整段 token 问题，无回归测试 | 测 CJK+标点混排、子串召回 |
| schema 迁移（P0-1 新增） | 迁移本身需测试 | 旧 schema 建库→升版本→验证 |

---

## 七、P3 — i18n / a11y / 长期能力

### P3-1　i18n 严重不完整

11 个 cowork 组件**完全没有 import i18n**（GraphCanvas/GraphToolbar/GraphLegend/KnowledgeRefBar/SkillSelectModal/DocPreview/EntityDetail/EntityEditModal/TemplateSelect/TaskCard/CalendarTaskPanel），~199 行硬编码中文。图谱主界面（用户最先看到）对英文用户完全不可读。

**额外 bug**：`CoworkDock.tsx` 用 `t("coworkDock.today") || "今日"` 模式 48 处，但 key 在 en.ts 中**存在**，`t()` 返回英文值而非空串 → **中文用户反而看到英文**。

**提升**：抽共享实体类型常量 + i18n 键（消除 ~30 行重复）；修复 `||` fallback 逻辑 bug；逐步补全硬编码文案。

### P3-2　实体类型着色多组同色

`styles.css:21271-21277`：product=organization（蓝）、technology=project=location（绿）、concept=topic（灰）。色觉正常用户都无法区分，且对红绿色盲不友好。

**提升**：为 11 种类型分配可区分色板（参考 Okabe-Ito 色盲友好色板），颜色不只用于边框也用于填充。

### P3-3　可访问性几乎为零

- 整个 cowork 目录无 `aria-live`/`aria-busy`/`sr-only`（CSS 也无 `.sr-only` 类），进度/轮询/toast 完全无 live region 公告。
- 进度条/状态徽章零 `aria-label`。
- 拖拽导入无 SR 反馈。
- 类型筛选下拉非原生控件，无键盘导航。

### P3-4　资料库面板零响应式

`styles.css` 16 个 `max-width` 媒体查询，无一命中 `.rag`/`.cowork`/`.graph`。资料库完全为宽屏设计，窄屏/平板不可用。

### P3-5　FTS5 中文 bigram

当前 CJK 整段作一个 token（"渲染管线"是一个 token），搜"管线"匹配不到。补 bigram fallback 提升中文召回。

### P3-6　向量检索规模化

brute-force 余弦（注释自评 ~10K 上限）。加 ANN 索引/规模熔断/降级。

---

## 八、落地计划

### 第一波：P0 数据安全与功能修复（~4-5 天）

| 项 | 工作量 | 依赖 |
|----|--------|------|
| P0-1 schema 迁移机制 | 1 天 | 无 |
| P0-2 专家团注入防护 | 0.5 天 | 无 |
| P0-3 重导入去重 | 0.5 天 | 无 |
| P0-4 Pipeline.Resume | 1 天 | 无 |
| P0-5 语义搜索断链 | 0.5 天 | P1-3（progress 事件）联动 |
| P0-6 GraphCanvas 性能 | 1 天 | 无 |

> 这一波直接决定"功能是否正确工作 + 是否烧钱 + 是否安全"。

### 第二波：P1 前后端接线（~3-4 天）

P1-1~P1-8。**投入极小、用户感知极大**——RagNode 挂载 + 取消按钮 + progress 事件 + 静默吞错 + HE 离线引导 + 格式提示 + 反向高亮 + 全不选。其中 P1-1/P1-2/P1-3 可合并实施。

### 第三波：P1.5 安全与 agent 集成（~2-3 天）

P1.5-1（集合选择贯通）、P1.5-2（死工具处理）、P1.5-3（返回限长）、P1.5-4（专家团多 collection）。

### 第四波：P2 架构清理（~3-4 天）

P2-1 抽公共包、P2-2 删死代码、P2-3 统一抽取路径、P2-4~P2-7 工程加固。为后续扩展扫清障碍。

### 第五波：P2.5 测试补强（贯穿各波）

每波实施时同步补对应测试。重点：向量检索、ON CONFLICT、schema 迁移、rag_app 集成测试。

### 第六波：P3 i18n/a11y/规模化（按需排期）

---

## 附录 A：审计发现全量索引（按严重度）

### 🔴 P0（数据安全 / 功能性故障 / 安全）
1. 无 schema 迁移机制 → 加列崩溃（`store.go:39-140`）
2. 专家团 RAG 注入无 injection 防护（`experts_app.go:545` / `orchestrator.go:186`）
3. 重导入烧配额（`extract.go:236` / `entities.go:448`）
4. Pipeline.Resume 不存在，重启丢任务（`extract.go:20,178`）
5. 语义搜索永远空（`GraphCanvas.tsx:147` 缺 `RagEmbedEntities`）
6. GraphCanvas 性能地雷（全量加载 + O(n·m) + 无 debounce + 无 memo）

### 🟠 P1 / P1.5（前后端断链 / agent 集成）
7. RagNode 孤儿组件，取消/删除/重提取未接线（`RagNode.tsx` / `CoworkDock.tsx:637`）
8. RagCancelExtract 前端零调用（`bridge.ts:376`）
9. rag:progress 事件无人订阅，轮询有 5min 上限（`TemplateSelect.tsx:133`）
10. 静默吞错 4 处（`TemplateSelect.tsx:116,172` / `EntityEditModal.tsx:49,65`）
11. HE 离线仍允许点提取（`TemplateSelect.tsx:200`）
12. 格式提示错误（`RagPanel.tsx:151`）
13. 反向高亮断链（`CoWorkLayout.tsx:174` 派发 vs `GraphCanvas` 无监听）
14. "全不选"是假操作（`CoworkDock.tsx:544`）
15. SessionRAGContext 与 agent rag_search 断层（`rag.go:130` 不引用 session）
16. rag_graph/rag_mindmap 不可达（`boot.go:489` Hide + 不在 AllowedTools）
17. rag_search/rag_graph 返回无总长上限（`rag.go:199-281`）
18. 专家团只取第一个 collection + task 原文做 query（`orchestrator.go:190,192`）

### 🟡 P2（架构 / 工程质量 / 死代码）
19. markitdown 逻辑复制三份且不一致（`rag/officedoc.go` / `tool/builtin/officedoc.go` / `control/refs.go`）
20. extract_queue.go 273 行死代码（`NewExtractQueue` 无调用方）
21. 三套抽取并发逻辑分裂（HE 路径绕过限流和持久化）
22. 抽取日志全 Debug 级（`extract.go:368,373,379`）
23. HEService.Stop() 从未调用，Python 进程泄漏（`he_service.go:137`）
24. HE 端口 18900 硬编码无配置化（`he_service.go:21`）
25. pruneSourcesByPath 用 LIKE 子串筛选（`store.go:395`）

### 🟢 P3（i18n / a11y / 性能 / 长期）
26. i18n 严重不完整（11 文件零 i18n，199 行硬编码）
27. CoworkDock `|| "中文"` fallback 是逻辑 bug（48 处）
28. 实体类型着色多组同色（`styles.css:21271`）
29. 零 aria-live/aria-busy，进度/SR 完全无公告
30. 资料库面板零响应式
31. FTS5 无 CJK bigram，中文子串召回受限
32. 向量 brute-force 无 ANN，无规模熔断

---

## 附录 B：关键文件索引

| 关注点 | 文件 |
|--------|------|
| 存储核心 | `internal/rag/store.go`、`entities.go`、`embedding.go` |
| 抽取管线 | `internal/rag/extract.go`、`platform_extractor.go`、`extract_queue.go`(死代码) |
| 文档解析 | `internal/rag/officedoc.go`、`doc_converter.py`、`ocr_pdf.py` |
| HE 集成 | `internal/rag/he_client.go`、`desktop/he_service.go`、`hyper_extract_server.py` |
| 桥接层 | `desktop/rag_app.go`、`desktop/app.go(initRAG)` |
| rag 工具 | `internal/tool/builtin/rag.go`、`untrusted.go` |
| 专家团 | `internal/experts/orchestrator.go`、`desktop/experts_app.go` |
| 前端 | `frontend/src/components/cowork/`（21 文件）、`layouts/CoWorkLayout.tsx` |
| 配置 | `internal/config/config.go(CoworkConfig)`、`hiq.example.toml` |
| 测试 | `internal/rag/*_test.go`、`desktop/bound_array_contract_test.go` |
