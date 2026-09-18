# RAG 知识库改造方案

> 版本: v4.0 | 日期: 2026-07-07

---

## 核心理念

**效果驱动。** 用户导入文档后，必须能直观看到知识图谱、实体关系、结构化内容。看不到效果 = 没有功能。

```
导入文档 → 即时索引(FTS5) → 深度提取(LLM) → 可视化展示(图谱/实体/关系)
                          │                    ↓
                          │          用户看到效果 → 信任知识库 → 主动使用
                          │
                          └→ 静默理解：导入后自动后台排队提取，不阻塞用户
                          └→ 立即理解：用户手动点击，显示进度条等待结果
```

---

## 用户看到什么？

> 图谱优先设计：知识图谱占据 CoWork center 全部空间，成为视觉核心。
> 右侧 CoworkDock 改造为知识库导航（集合/实体/文件），点击时显示详情。
> 所有颜色用 CSS 变量，自动适配 6 种主题。

### 整体布局

```
CoWork sidebar        Center（图谱全屏）              CoworkDock
(已有按钮)            (RagPanel 改造)                 (改造为知识库导航)

┌──────┐  ┌───────────────────────────────────────┐  ┌──────────────┐
│      │  │  集合: [项目文档 ▼]  [导入] [导出]     │  │  集合         │
│ 任务  │  │                                      │  │  ──────       │
│ 偏好  │  │                                      │  │  [x] 项目文档 │
│ 日程  │  │          知 识 图 谜                  │  │  [ ] 会议纪要 │
│ 专家  │  │                                      │  │              │
│[知识库]│  │      (React Flow 交互式画布)         │  │  实体 (156)   │
│      │  │                                      │  │  ──────       │
│      │  │      节点: 实体（按类型着色）          │  │  hiq  8  │
│      │  │      边:   关系（带标签）              │  │  Wails      5 │
│      │  │                                      │  │  Go          3 │
│      │  │                                      │  │  ...          │
│      │  │                                      │  │              │
│      │  │  [类型筛选] [选择模式] [自动布局]      │  │  文件 (32)    │
│      │  │                              图例:     │  │  ──────       │
│      │  │                          ● 产品       │  │  需求文档 ok  │
│      │  │                          ● 技术       │  │  技术方案 ..  │
│      │  │                          ● 功能       │  │              │
│      │  └───────────────────────────────────────┘  └──────────────┘
└──────┘
```

### 空状态（首次打开 / 无文档）

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│  集合: [全部 ▼]                                                [导入]              │
│                                                                                     │
│                                                                                     │
│                                                                                     │
│                              拖入文件或文件夹开始                                    │
│                              支持 md / docx / pdf / xlsx                            │
│                                                                                     │
│                                        [导入文件]                                   │
│                                                                                     │
│                                                                                     │
│                                                                                     │
│                                                                                     │
└─────────────────────────────────────────────────────────────────────────────────────┘
```

简单一句提示 + 一个按钮，不需要复杂引导。

### 点击实体 → 右侧详情

```
CoworkDock 切换到实体详情：

┌──────────────┐
│ hiq     │
│ 产品          │
│ ──────────── │
│ 基于 Wails   │
│ v2 的桌面    │
│ AI 助手      │
│              │
│ ── 关系 ──── │
│ → 使用 Wails │
│ → 使用 Go    │
│ → 包含       │
│   ExpertPanel│
│ → 包含 RAG   │
│              │
│ ── 来源 ──── │
│ 需求文档.md#0│
│ 技术方案#2   │
│              │
│ [编辑] ←弹窗  │
│ [在图谱高亮] │
│ [导出]       │
└──────────────┘
```

### 编辑实体 → 弹窗编辑

详情面板点 [编辑] → 居中弹出 modal：

```
┌─ 编辑实体 ──────────────────────────────────────────────┐
│                                                          │
│  名称     [hiq                        ]             │
│  类型     [产品 ▼]                                        │
│  描述     [基于 Wails v2 的桌面 AI 助手        ]          │
│                                                          │
│  [合并重复实体...]                                       │
│                                                          │
│                                    [取消]  [保存]        │
└──────────────────────────────────────────────────────────┘
```

点 [合并重复实体] → 展开合并区域（同弹窗内）：

```
┌─ 编辑实体 ──────────────────────────────────────────────┐
│                                                          │
│  名称     [hiq                        ]             │
│  类型     [产品 ▼]                                        │
│  描述     [基于 Wails v2 的桌面 AI 助手        ]          │
│                                                          │
│  ── 合并重复实体 ──────────────────────────────────────  │
│  勾选要合并到 "hiq" 的实体，关系自动迁移            │
│                                                          │
│  [x] hiq Desktop         3 关系                     │
│  [ ] hiq App             1 关系                     │
│                                                          │
│  合并后将迁移 3 条关系                                   │
│                                                          │
│                        [取消]  [合并并保存]              │
└──────────────────────────────────────────────────────────┘
```

### 搜索 → 图谱高亮

```
搜索框输入 "Wails" → 匹配节点高亮，其余半透明：

┌───────────────────────────────────────┐
│ 搜索: [Wails___]              [清除]  │
│                                       │
│         ┌───────────┐                 │
│         │ hiq  │                 │
│         │  (半透明)  │                 │
│         └─────┬─────┘                 │
│            使用│                       │
│               ▼                       │
│         ┌───────────┐                 │
│         │  Wails    │  ← 高亮(橙边框) │
│         │  (技术)   │                 │
│         └───────────┘                 │
│                                       │
│  匹配: 1 节点 · 2 条边                │
└───────────────────────────────────────┘
```

### 深度提取 → 模板选择 + 进度

```
点击 [深度提取] → CoworkDock 切换到提取面板：

┌──────────────┐
│ 深度提取     │
│ ──────────── │
│ 集合         │
│ [项目文档 ▼] │
│              │
│ ── 模板 ──── │
│ > 通用图谱   │
│   金融分析   │
│   医学知识   │
│   法律文书   │
│   工业知识   │
│ [更多...]    │
│              │
│ [静默理解]   │
│ [立即理解]   │
│              │
│ ── 进度 ──── │
│ 需求文档 ok  │
│ 技术方案 ..  │
│ 测试报告 !!  │
│   [重试]     │
└──────────────┘
```

### 选择知识引用 → 写 PPT/Word 时使用

用户写文档时，从图谱里选中实体/关系，传递给 skill 作为上下文。

**交互流程：**

1. 工具栏点 [选择模式] → 进入多选状态（按钮高亮）
2. 单击节点/边选中（选中项加橙色虚线边框）
3. 底部出现引用条
4. 点 [用于: xxx ▼] → 弹出 skill 选择弹窗
5. 确认后 → Go 写临时文件 → 调 skill → 自动退出选择模式

**引用条（画布底部）：**

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│ 已选 3 个实体 · 2 条关系                              [清除] [用于: ▼]              │
│                                                                                     │
│  hiq · Wails · ExpertPanel · hiq→使用→Wails · hiq→包含→ExpertPanel  │
└─────────────────────────────────────────────────────────────────────────────────────┘
```

**skill 选择弹窗（点击"用于"后弹出）：**

```
┌─ 使用知识引用 ─────────────────────────────────────┐
│                                                     │
│  将选中的 3 个实体 · 2 条关系传递给：               │
│                                                     │
│    [x] PPT 生成                                     │
│    [ ] Word 文档撰写                                │
│    [ ] 自定义 skill...                              │
│                                                     │
│  ┌─ 预览 ──────────────────────────────────────┐   │
│  │ 实体:                                        │   │
│  │ - hiq (产品): 基于 Wails v2 的桌面...   │   │
│  │ - Wails (技术): Go 桌面应用框架              │   │
│  │ - ExpertPanel (功能): 专家团协作面板         │   │
│  │                                              │   │
│  │ 关系:                                        │   │
│  │ - hiq → 使用 → Wails                    │   │
│  │ - hiq → 包含 → ExpertPanel              │   │
│  └──────────────────────────────────────────────┘   │
│                                                     │
│                              [取消]  [确认运行]     │
└─────────────────────────────────────────────────────┘
```

**底层流程（用户无感知）：**

```
用户点 [确认运行]
    │
    ▼
Go: WriteKnowledgeRef(collection, entityNames, relationKeys)
    → 格式化为 markdown → 写入 {tempDir}/knowledge_ref.md
    │
    ▼
Go: RunSkillWithKnowledge("ppt-auto", "知识参考: {tempDir}/knowledge_ref.md")
    → 调用 run_skill，arguments 包含文件路径
    │
    ▼
Skill 启动 → 读取 arguments → read_file 读取知识文件 → 开始工作
    │
    ▼
Skill 结束 → 自动删除临时文件
```

**支持的 skill（硬编码列表）：**
- PPT 生成（ppt-auto）
- Word 文档撰写（document-auto）

翻译/总结不列入——没有内置 skill，用户可自建。

**skill body 改动：**

ppt-auto 和 document-auto 的 skill body 各加一句：
```
如果 arguments 包含 "知识参考:" 开头的文件路径，先用 read_file 读取该文件，
将其中的实体和关系信息作为生成内容的参考依据。
```

### Obsidian 导出

```
vault/
├── hiq.md          ← 实体笔记，含 YAML 前置信息 + 关系 wikilinks
├── Wails.md
├── ExpertPanel.md
├── RAG.md
└── ...
```

每个笔记内容：
```markdown
---
type: product
sources: [需求文档.md#0, 技术方案.docx#2]
---

# hiq

基于 Wails v2 的桌面 AI 助手

## 关系

- 使用 → [[Wails]]
- 使用 → [[Go]]
- 包含 → [[ExpertPanel]]
- 包含 → [[RAG]]

## 来源

> "hiq 是一个基于 Wails v2 框架的桌面 AI 助手..."
> — 需求文档.md
```

用户用 Obsidian 打开 vault，看到完整的知识图谱，可以自由浏览、搜索、编辑。

### 节点/边样式（CSS 变量，自动适配 6 种主题）

```
节点：
  背景:   var(--bg-elev)
  边框:   var(--border)         选中: var(--accent)
  文字:   var(--fg)             副文字: var(--fg-dim)
  圆角:   var(--list-row-radius)  7px
  阴影:   var(--shadow-1)
  大小:   按关系统计缩放（120-200px 宽）

类型着色（复用已有语义色）：
  产品:   var(--accent)         橙色系
  技术:   var(--ok)             绿色系
  功能:   var(--mode-auto-bg)   蓝色系
  人物:   var(--warn)           黄色系
  事件:   var(--fg-faint)       灰色系

边：
  线条:   1px solid var(--border-soft)
  标签:   12px var(--fg-faint)  背景 var(--bg-soft)
  高亮:   2px solid var(--accent)

画布：
  背景:   var(--bg)
```

## 四个模块

### 模块 1：图谱优先 + 右侧详情面板

**核心模块。** 知识图谱占据 CoWork center 全部空间，CoworkDock 改造为知识库导航。

#### 1.1 布局架构

```
CoWorkLayout 现有三栏 Grid：
  sidebar(centeral) | center(activePanel) | CoworkDock

RAG 模式下的分工：
  - center：GraphCanvas（React Flow 画布，全屏）
  - CoworkDock：三个 tab（集合/实体/文件），点击实体/文件时切到详情
  - CoWork sidebar：已有"知识库"按钮，点击切换到 RAG 面板
```

#### 1.2 GraphCanvas（图谱画布）

技术选型：**React Flow**（React 原生，内置拖拽/缩放/自动布局）

```tsx
// GraphCanvas.tsx — 占据 center 全部空间

interface GraphNode {
  id: string;          // 实体名
  label: string;       // 显示名
  type: string;        // 实体类型（产品/技术/功能/人物）
  description: string;
  sources: Source[];
  relations: number;   // 关系统计
}

interface GraphEdge {
  source: string;
  target: string;
  type: string;        // 关系类型
  description: string;
}
```

功能：
- 节点按类型着色（产品=accent，技术=ok，功能=mode-auto-bg，人物=warn）
- 节点大小按关系统计缩放（关系越多越大）
- 点击节点 → CoworkDock 切到实体详情
- 点击边 → 显示关系描述 tooltip
- 支持拖拽、缩放、自动布局（dagre 算法）
- 搜索高亮（输入关键词，匹配节点高亮，其余半透明）
- **类型筛选**：工具栏按类型勾选，不匹配节点隐藏（不是半透明，直接隐藏，保持图谱干净）
- **选择模式**：工具栏一个开关按钮，开启后单击多选实体/关系，用于知识引用
- 类型筛选（不匹配节点淡出）
- 导出 PNG / 全屏模式

#### 1.3 CoworkDock 改造（知识库导航）

现有 CoworkDock 有三个 tab（today/mail/files）。RAG 模式下改造为：

| Tab | 内容 | 交互 |
|-----|------|------|
| 集合 | 集合列表 + 激活勾选 | 勾选激活集合，切换图谱数据源 |
| 实体 | 实体列表，按类型分组 | 点击实体 → 切到详情视图 |
| 文件 | 文件列表 + 提取状态 | 点击文件 → 切到预览视图 |

详情视图（覆盖 tab 内容）：
- 实体详情：类型/描述/关系列表/原文引用 + 编辑/合并按钮
- 文件预览：原文内容 + chunk 高亮 + "立即理解"按钮

#### 1.4 实体编辑与合并

用户修正 LLM 抽取错误的手段：

- **编辑实体**：详情面板点击"编辑"→ 弹出编辑弹窗（名称/类型/描述）→ 保存
- **合并重复实体**：编辑弹窗中点"合并重复实体"→ 弹出合并弹窗 → 勾选候选 → 确认合并
- 合并时保留目标实体的名称，被合并实体的关系全部迁移过来
- 合并前弹窗显示影响预览（将迁移多少条关系）

#### 1.5 文档原文预览

用户验证提取质量的手段：

- 点击文件列表中的文件 → CoworkDock 切到预览视图
- 原文中被提取的 chunk 高亮显示
- 方便用户对比"LLM 提取了什么"和"原文写了什么"

#### 1.6 模板选择与提取路径

深度提取的入口：

- 点击 [深度提取] → CoworkDock 切到提取面板
- 自动推荐最匹配的模板（基于文档内容分析）
- 用户可手动选择其他模板（金融/医学/法律/工业/通用）
- 提供两种提取路径：
  - **静默理解**：后台自动排队提取，用户继续工作
  - **立即理解**：手动触发，显示进度条等待结果

#### 1.7 知识引用选择（写 PPT/Word 时使用）

**核心消费路径。** 用户从图谱中选中实体/关系，通过临时文件传递给 skill。

交互流程：
1. 工具栏点 [选择模式] → 进入多选状态（按钮高亮）
2. 单击节点/边选中，选中项加橙色虚线边框
3. 底部出现引用条：已选数量 + 清除按钮 + "用于"按钮
4. 点"用于" → 弹出 skill 选择弹窗（含预览）
5. 确认 → Go 写临时文件 → 调 skill → 自动退出选择模式

弹窗（居中 modal）：
- 标题：使用知识引用
- 内容：选中数量 + skill 单选（PPT 生成 / Word 文档撰写）+ 预览区
- 预览区：展示将要传递的实体/关系列表
- 按钮：取消 / 确认运行

底层流程：
- WriteKnowledgeRef() → 格式化 markdown → 写入 {tempDir}/knowledge_ref.md
- RunSkillWithKnowledge() → arguments="知识参考: {path}" → 调用 run_skill
- skill 读取 arguments → read_file 读取知识文件 → 开始工作
- skill 结束 → 自动删除临时文件

```go
// rag_app.go

// WriteKnowledgeRef 将选中的实体/关系写入临时 markdown 文件
func (a *RagApp) WriteKnowledgeRef(collection string, entityNames []string, relationKeys []string) (string, error) {
    // 1. 从 SQLite 查询实体/关系详情
    // 2. 格式化为 markdown（实体: 名称(类型): 描述；关系: 源→类型→目标）
    // 3. 写入 {os.TempDir()}/hiq_knowledge_ref_{timestamp}.md
    // 4. 返回文件路径
}

// RunSkillWithKnowledge 调用 skill 并传递知识引用文件路径
func (a *RagApp) RunSkillWithKnowledge(skillName string, refPath string) error {
    arguments := fmt.Sprintf("知识参考: %s", refPath)
    // 调用 run_skill，arguments 包含文件路径
    // skill 结束后清理临时文件
}
```

skill body 改动（ppt-auto + document-auto 各加一句）：
```
如果 arguments 包含 "知识参考:" 开头的文件路径，先用 read_file 读取该文件，
将其中的实体和关系信息作为生成内容的参考依据。
```

#### 1.8 后端支持

```go
// rag_app.go 新增

// GetGraphData 返回集合的图谱数据（节点+边）
func (a *RagApp) GetGraphData(collection string) (*GraphData, error) {
    entities, _ := a.store.SearchEntities("", collection, 1000) // 全量
    var nodes []GraphNode
    var edges []GraphEdge
    for _, e := range entities {
        nodes = append(nodes, GraphNode{
            ID: e.Name, Label: e.NameRaw, Type: e.Type,
            Description: e.Description, Sources: e.Sources,
        })
        rels, _ := a.store.RelationsOf(collection, e.Name, true)
        for _, r := range rels {
            edges = append(edges, GraphEdge{
                Source: r.Source, Target: r.Target,
                Type: r.Type, Description: r.Description,
            })
        }
    }
    return &GraphData{Nodes: nodes, Edges: edges}, nil
}

// GetEntityDetail 返回单个实体的完整信息
func (a *RagApp) GetEntityDetail(collection, name string) (*EntityDetail, error) {
    // 实体信息 + 所有关系 + 原文引用
}

// UpdateEntity 编辑实体字段（名称/类型/描述）
func (a *RagApp) UpdateEntity(collection, name string, patch EntityPatch) error {
    // 直接修改 entities 表，同步更新 relations 中的 source/target
}

// MergeEntities 合并重复实体（关系自动迁移）
func (a *RagApp) MergeEntities(collection string, keepName string, mergeNames []string) error {
    // 将 mergeNames 的关系迁移到 keepName，删除被合并实体
    // 同步更新 relations 表中的 source/target 引用
}

// GetDocumentPreview 返回文档原文（用于对比提取质量）
func (a *RagApp) GetDocumentPreview(collection, docPath string) (*DocPreview, error) {
    // 返回原文 + chunk 高亮信息
}

// ExportObsidian 导出为 Obsidian vault
func (a *RagApp) ExportObsidian(collection, outputDir string) error {
    // 通过 Hyper-Extract 的 export_obsidian 接口
    // 或者自己生成 Markdown + wikilinks
}
```

#### 1.7 Obsidian 导出

两种路径：
- **Hyper-Extract 已安装** → 调用 `/export` 接口，生成标准 Obsidian vault
- **Hyper-Extract 未安装** → Go 侧自己生成简单 Markdown（实体笔记 + wikilinks）

```go
// rag/obsidian.go — 简单的 Markdown 导出（不需要 Hyper-Extract）

func ExportToObsidian(store *Store, collection, outputDir string) error {
    entities, _ := store.SearchEntities("", collection, 1000)
    for _, e := range entities {
        md := generateEntityMarkdown(store, collection, e)
        os.WriteFile(filepath.Join(outputDir, e.NameRaw+".md"), md, 0644)
    }
    return nil
}

func generateEntityMarkdown(store *Store, collection string, e Entity) []byte {
    // YAML front matter + 关系 wikilinks + 原文引用
}
```

---

### 模块 2：会话级集合选择

**做什么：** 用户勾选激活集合，rag_search 自动限定范围。

#### 数据模型

```go
type SessionRAGContext struct {
    ActiveCollections []string `json:"activeCollections"` // 空=全部
}
```

#### UI

RagPanel 底部增加激活选择：

```
本次激活: ☑ 项目文档  ☐ 会议纪要  ☑ 技术规范
          [全选] [全不选]
```

#### 后端

```go
// rag_app.go
func (a *RagApp) SetSessionCollections(collections []string) { ... }
func (a *RagApp) GetSessionCollections() []string { ... }

// rag.go rag_search 改造
// session 有激活集合且未指定 collection → 只搜激活集合
```

---

### 模块 3：二进制文档支持

**做什么：** 支持 docx/xlsx/pptx/pdf 导入。

```go
// rag/store.go 改造 readDoc
func readDoc(path string) (string, string, error) {
    ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
    switch ext {
    case "docx":  return readDocx(path)
    case "xlsx":  return readXlsx(path)
    case "pptx":  return readPptx(path)
    case "pdf":   return readPdf(path)
    // ...原有逻辑...
    }
}
```

RagPanel 放开文件类型限制，支持拖拽 Office/PDF。

---

### 模块 4：Hyper-Extract 集成 + 专家团 RAG

**做什么：**
- Hyper-Extract 作为深度提取后端（模板化提取，质量更高）
- 专家团运行时可引用知识库

#### Hyper-Extract 集成

```
hiq (Go)
    │
    ├─ 首次深度提取时 spawn → hyper-extract-server.py (localhost)
    │                          ├─ /parse     — 提取
    │                          ├─ /search    — 语义搜索
    │                          ├─ /templates — 模板列表
    │                          └─ /export    — Obsidian 导出
    │
    ├─ rag/he_client.go — Go HTTP 客户端
    └─ desktop/he_service.go — 生命周期管理
```

深度提取流程（两种路径）：
```
路径 A：静默理解（默认）
  导入文件 → FTS5 即时索引 → 自动排队后台提取 → 用户继续工作 → 提取完成通知
                                                  ↓
                                        用户点击通知 → 看到图谱

路径 B：立即理解（手动触发）
  导入文件 → FTS5 即时索引 → 用户点击"立即理解" → 显示进度条 → 等待完成 → 看到图谱
```

- LLM 请求量大时自动排队，避免并发过高
- 后台提取失败不阻塞用户，提供重试按钮
- 提取完成后 RagPanel 角标显示新增实体/关系数量

#### 后台提取队列

```go
// rag/extract_queue.go

type ExtractJob struct {
    ID         string
    Collection string
    DocPath    string
    Template   string
    Status     string    // "pending" | "running" | "done" | "failed"
    Progress   int       // 0-100
    CreatedAt  time.Time
    Error      string
}

type ExtractQueue struct {
    jobs    chan *ExtractJob
    mu      sync.Mutex
    running map[string]*ExtractJob
}

// Submit 提交提取任务（静默理解时自动调用）
func (q *ExtractQueue) Submit(job *ExtractJob) { ... }

// RunNow 立即理解（用户手动触发，优先执行）
func (q *ExtractQueue) RunNow(job *ExtractJob) { ... }

// GetProgress 查询任务进度
func (q *ExtractQueue) GetProgress(jobID string) (*ExtractJob, error) { ... }
```

#### 专家团 RAG

```go
// team.go 新增
type Team struct {
    AllowRAG       bool     `json:"allow_rag"`
    RAGCollections []string `json:"rag_collections,omitempty"`
}

// orchestrator.go 改造
// 运行前执行一次 rag_search → 结果注入任务描述 → 所有专家共享
```

ExpertPanel UI 增加"关联知识库"下拉。

#### 模板选择 UI

```
 深度提取 · 项目文档                                                  [静默理解] [立即理解]
 ────────────────────────────────────────────────────────────────────────────────────────
 集合     [项目文档 ▼]

 ┌─ 推荐模板 ──────────────────────────────────────────────────────────────────────────┐
 │  > 通用知识图谱 (general/base_graph)                   ← 默认                      │
 │    金融分析 (finance/base_graph)                                                      │
 │    医学知识 (medicine/base_graph)                                                     │
 │    法律文书 (legal/base_graph)                                                        │
 │    工业知识 (industry/base_graph)                                                     │
 │                                                                                     │
 │    [查看所有模板...]                                                                 │
 └─────────────────────────────────────────────────────────────────────────────────────┘

 ┌─ 提取进度 ──────────────────────────────────────────────────────────────────────────┐
 │    需求文档.md                    ok  12实体 · 8关系                                 │
 │    技术方案.docx                  ..  提取中 3/5                                     │
 │    测试报告.pdf                   !!  失败                    [重试]                 │
 └─────────────────────────────────────────────────────────────────────────────────────┘
```

内置团队更新：文档撰写/翻译/会议/数据分析团队开启 AllowRAG。

---

## 上下文控制

| 控制点 | 做法 |
|--------|------|
| 不自动注入 | 不塞 system prompt |
| 按需检索 | agent 需要时调用 rag_search |
| 知识引用 | 用户从图谱选中实体/关系 → 格式化注入 skill 上下文 |
| 输出截断 | 每片段 ≤300 字，总输出 ≤3000 字 |
| prompt 引导 | cowork prompt 提示"有需要时搜知识库" |

---

## 实施计划

### Phase 1：图谱优先 + 右侧详情（2.5 周）⭐ 核心

**图谱占据 center 全部空间，CoworkDock 改造为知识库导航。这是用户看到的第一个"效果"。**

| 任务 | 文件 | 工作量 |
|------|------|--------|
| 1.1 GetGraphData/GetEntityDetail Wails 绑定 | `rag_app.go` | 1d |
| 1.2 GraphCanvas 画布（React Flow，节点着色/缩放/拖拽/布局） | `GraphCanvas.tsx` | 4d |
| 1.3 GraphToolbar 工具栏（搜索高亮/类型筛选/全屏/导出 PNG） | `GraphToolbar.tsx` | 1.5d |
| 1.4 CoworkDock 改造（集合/实体/文件三个 tab） | `CoworkDock.tsx` | 2d |
| 1.5 实体详情面板（Dock 内，关系列表 + 原文引用） | `EntityDetail.tsx` | 1.5d |
| 1.6 实体编辑（名称/类型/描述可改） | `EntityDetail.tsx` + `rag_app.go` | 1d |
| 1.7 重复实体合并（选中→合并→关系迁移） | `EntityDetail.tsx` + `rag_app.go` | 1d |
| 1.8 文档原文预览（Dock 内） | `DocPreview.tsx` + `rag_app.go` | 1d |
| 1.9 Obsidian 导出（Go 侧简单版） | `rag/obsidian.go` | 2d |
| 1.10 RagPanel 改造（图谱优先布局 + Dock 联动） | `RagPanel.tsx` + `CoWorkLayout.tsx` | 1.5d |
| 1.11 图谱样式（CSS 变量，6 种主题适配） | `styles.css` | 1d |
| 1.12 知识引用选择（选择模式 + 引用条 + skill 注入） | `GraphCanvas.tsx` + `rag/context.go` | 2d |
| 1.13 测试 | — | 1.5d |

**交付：** 空状态引导 → 导入文档 → 深度提取 → 全屏交互式知识图谱 → 类型筛选 → 点击节点右侧详情 → 编辑/合并实体 → 选择模式选中实体/关系注入 skill → 预览原文 → 导出 Obsidian。

### Phase 2：集合选择 + 二进制支持（1.5 周）

| 任务 | 文件 | 工作量 |
|------|------|--------|
| 2.1 SessionRAGContext 数据模型 | `rag/context.go` | 0.5d |
| 2.2 Set/GetSessionCollections 绑定 | `rag_app.go` | 0.5d |
| 2.3 rag_search 改造：限定激活集合 | `rag.go` | 1d |
| 2.4 CoworkDock 集合 tab：激活勾选 | `CoworkDock.tsx` | 1.5d |
| 2.5 readDoc 扩展：docx | `store.go` | 1d |
| 2.6 readDoc 扩展：xlsx | `store.go` | 1d |
| 2.7 readDoc 扩展：pptx | `store.go` | 0.5d |
| 2.8 readDoc 扩展：pdf | `store.go` | 1d |
| 2.9 测试 | — | 1d |

**交付：** 勾选激活集合 + 拖拽 Office/PDF 导入。

### Phase 3：Hyper-Extract 深度提取（2 周）

| 任务 | 文件 | 工作量 |
|------|------|--------|
| 3.1 Python HTTP 服务脚本 | `hyper_extract_server.py` | 2d |
| 3.2 Go HTTP 客户端 | `rag/he_client.go` | 1.5d |
| 3.3 服务生命周期管理 | `desktop/he_service.go` | 1.5d |
| 3.4 模板列表 + 自动推荐 | `rag/templates.go` | 1d |
| 3.5 深度提取流程整合 | `rag_app.go` | 2d |
| 3.6 后台提取队列（静默理解 + 立即理解） | `rag/extract_queue.go` | 2d |
| 3.7 模板选择 UI + "立即理解"按钮（Dock 内） | `TemplateSelect.tsx` + `CoworkDock.tsx` | 1.5d |
| 3.8 rag_search 整合 Hyper-Extract 结果 | `rag.go` | 1.5d |
| 3.9 Obsidian 导出增强（Hyper-Extract 版） | `rag_app.go` | 1d |
| 3.10 打包集成 | `desktop/` | 1.5d |
| 3.11 测试 | — | 1.5d |

**交付：** 模板化深度提取 + 静默/立即两种理解路径 + 语义搜索 + 高质量图谱。

### Phase 4：专家团 RAG + 增量更新 + 批量操作（1.5 周）

| 任务 | 文件 | 工作量 |
|------|------|--------|
| 4.1 Team 结构扩展 | `team.go` | 0.5d |
| 4.2 Orchestrator RAG 集成 | `orchestrator.go` | 1.5d |
| 4.3 ExpertPanel UI | `ExpertPanel.tsx` | 1.5d |
| 4.4 内置团队更新 | `team.go` | 0.5d |
| 4.5 增量更新（feed_text） | `rag_app.go` | 1.5d |
| 4.6 批量导入文件夹 | `rag_app.go` + `RagPanel.tsx` | 1.5d |
| 4.7 批量深度提取 + 进度 | `rag_app.go` + `RagPanel.tsx` | 1.5d |
| 4.8 rag-auto skill 扩展 | `builtins.go` | 0.5d |
| 4.9 测试 | — | 1d |

**交付：** 专家团引用知识库 + 增量更新 + 批量操作。

---

## 文件变更清单

```
新增：
  frontend/src/components/cowork/GraphCanvas.tsx    — 图谱画布（React Flow，全屏）
  frontend/src/components/cowork/GraphToolbar.tsx   — 图谱工具栏（搜索/类型筛选/选择模式/布局）
  frontend/src/components/cowork/GraphLegend.tsx    — 图例组件
  frontend/src/components/cowork/EntityDetail.tsx   — 实体详情面板（Dock 内）
  frontend/src/components/cowork/DocPreview.tsx     — 文件预览面板（Dock 内）
  frontend/src/components/cowork/TemplateSelect.tsx — 模板选择 UI（Dock 内）
  internal/rag/context.go                           — 会话激活状态 + 知识引用选择
  internal/rag/obsidian.go                          — Obsidian 导出
  internal/rag/he_client.go                         — Hyper-Extract 客户端
  internal/rag/templates.go                         — 模板管理
  internal/rag/extract_queue.go                     — 后台提取队列（静默/立即）
  desktop/he_service.go                             — Python 服务管理
  hyper_extract_server.py                           — Python HTTP 服务

修改：
  internal/rag/store.go                             — readDoc 扩展 + 实体编辑/合并
  internal/rag/extract.go                           — Hyper-Extract 集成
  internal/tool/builtin/rag.go                      — rag_search 改造
  internal/skill/builtins.go                        — rag-auto 扩展 + ppt-auto/document-auto body 加知识引用读取
  internal/experts/team.go                          — Team 扩展
  internal/experts/orchestrator.go                  — 专家 RAG
  internal/config/profile.go                        — cowork prompt
  desktop/rag_app.go                                — Wails 绑定（+编辑/合并/预览/知识引用）
  frontend/src/components/cowork/RagPanel.tsx        — 改为图谱优先布局
  frontend/src/components/cowork/CoworkDock.tsx      — RAG 模式下改为知识库导航
  frontend/src/layouts/CoWorkLayout.tsx              — RAG 面板全屏 + Dock 联动
  frontend/src/styles.css                            — 图谱相关样式（CSS 变量）
  frontend/src/components/cowork/ExpertPanel.tsx     — 关联知识库
```

---

## 配置项

```toml
[cowork]
hyper_extract_enabled = true
hyper_extract_python = "python3"
hyper_extract_idle_timeout = 600
embedding_model = ""
embedding_provider = ""
default_template = "general/graph"
rag_max_snippet_length = 300
rag_max_total_output = 3000
```

---

## 关键决策

| 决策 | 理由 |
|------|------|
| 图谱可视化放 Phase 1 | 效果驱动，用户看到图谱才信任知识库 |
| React Flow 而非 D3 | React 生态原生，节点拖拽/缩放开箱即用 |
| Obsidian 导出双路径 | Hyper-Extract 版质量高，Go 简易版零依赖 |
| HTTP 微服务集成 Hyper-Extract | Python 依赖链长，不适合嵌入 Go |
| 不自动注入 system prompt | token 成本高，大部分对话不需要 |
| 专家团只搜索一次 | 省 token，保证一致性 |

---

## 风险

| 风险 | 缓解 |
|------|------|
| Python 服务启动失败 | 降级到 FTS5-only + PlatformExtractor |
| LLM 限流导致提取慢 | 已有 RPM limiter + 进度条 |
| docx/pdf 提取质量差 | 复用已验证的 doc_read |
| 图谱节点过多（>500） | 按关系统计截断 top-N，支持搜索筛选 |
| React Flow 大图性能 | 虚拟化渲染 + 按需加载 |
