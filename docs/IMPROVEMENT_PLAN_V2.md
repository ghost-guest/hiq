# Hiq 全面提升计划 v2（验证版）

> **版本**: v2.0 | **日期**: 2026-08-04 | **状态**: 已验证
>
> **目标**: 基于现有代码架构，以最小 token 开销实现最大能力提升

---

## 一、验证后的现状评估

### 1.1 已实现的能力（比我之前评估的更强）

| 能力 | 实际状态 | 之前评估 | 修正 |
|------|----------|----------|------|
| **错误恢复** | 12 项功能已实现 | ⭐⭐⭐ | ⭐⭐⭐⭐ |
| **Token 优化** | 三层截断 + ContextFilter | ⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ |
| **Skill 系统** | install_source 已有基础设施 | ⭐⭐⭐ | ⭐⭐⭐⭐ |
| **模型管理** | 18 供应商 + 动态注册表方案 | ⭐⭐⭐⭐ | ⭐⭐⭐⭐ |

### 1.2 真正需要做的工作

| 工作 | 实际工作量 | 之前估算 | 修正 |
|------|-----------|----------|------|
| **OfficeCLI 集成** | 3 天 | 3 天 | ✅ 准确 |
| **PPT 增强** | 3-5 天 | 5 天 | ⬇️ 减少 |
| **Skill 市场** | 3 天 | 5 天 | ⬇️ 减少 |
| **错误恢复补全** | 1-2 天 | 4 天 | ⬇️ 大幅减少 |
| **编辑前验证** | 2 天 | 3 天 | ⬇️ 减少 |
| **向量搜索** | 3 天 | 4 天 | ⬇️ 减少 |

**修正后总计：15-20 天（约 3-4 周）**，比之前估算的 30 天减少 33-50%。

---

## 二、详细实施方案

### 2.1 OfficeCLI 集成（3 天）

#### 2.1.1 架构设计

**集成模式：** Pattern B（内置 Go 工具组）— 注册+隐藏

```
┌─────────────────────────────────────────────────────────┐
│                    主循环（0 token 开销）                │
│  ┌─────────────────────────────────────────────────────┐│
│  │ Skill 索引（~30 token）                             ││
│  │ "- office-auto: 创建/编辑/预览 Word/Excel/PPT 文档" ││
│  └─────────────────────────────────────────────────────┘│
├─────────────────────────────────────────────────────────┤
│                    子代理（按需加载）                    │
│  ┌─────────────────────────────────────────────────────┐│
│  │ 工具列表（~600 token）                              ││
│  │ - officecli_create                                  ││
│  │ - officecli_read                                    ││
│  │ - officecli_edit                                    ││
│  │ - officecli_preview                                 ││
│  │ - officecli_validate                                ││
│  └─────────────────────────────────────────────────────┘│
│  ┌─────────────────────────────────────────────────────┐│
│  │ 结果（~200-500 token）                              ││
│  │ - 结构化摘要                                        ││
│  │ - 错误信息                                          ││
│  └─────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────┘
```

#### 2.1.2 实现步骤

**Day 1：工具定义**

创建 `internal/tool/builtin/officecli.go`：

```go
package builtin

import (
    "context"
    "encoding/json"
    "fmt"
    "os/exec"
    "strings"
)

// OfficeCLITools returns the OfficeCLI tool group.
func OfficeCLITools() []Tool {
    return []Tool{
        &officecliCreate{},
        &officecliRead{},
        &officecliEdit{},
        &officecliPreview{},
        &officecliValidate{},
    }
}

type officecliCreate struct{}

func (t *officecliCreate) Name() string { return "officecli_create" }
func (t *officecliCreate) Description() string {
    return "Create a Word/Excel/PPT document. Pass path (extension determines format)."
}
func (t *officecliCreate) Schema() json.RawMessage {
    return json.RawMessage(`{
        "type": "object",
        "properties": {
            "path": {"type": "string", "description": "Output file path (.docx/.xlsx/.pptx)"},
            "content": {"type": "string", "description": "Initial content (optional)"}
        },
        "required": ["path"]
    }`)
}
func (t *officecliCreate) ReadOnly() bool { return false }
func (t *officecliCreate) Execute(ctx context.Context, args json.RawMessage) (string, error) {
    // Parse args, call exec.CommandContext(ctx, "officecli", "create", path)
    // Return condensed result
}

// Similar implementations for read, edit, preview, validate...
```

**Day 2：注册和 Skill**

在 `internal/boot/boot.go` 中注册：

```go
// OfficeCLI tools (Word/Excel editing). Hidden from main loop schema.
for _, t := range builtin.OfficeCLITools() {
    reg.Add(t)
    reg.Hide(t.Name())
}
```

创建 Skill 文件 `.hiq/skills/office-auto/SKILL.md`：

```markdown
---
name: office-auto
description: 创建/编辑/预览 Word/Excel/PPT 文档
runAs: subagent
allowed-tools: officecli_create, officecli_read, officecli_edit, officecli_preview, officecli_validate, read_file, write_file, bash
---

# Office 自动化

使用 OfficeCLI 工具处理 Word/Excel/PPT 文档。

## 工作流程
1. 使用 officecli_read 了解文档结构
2. 规划编辑步骤
3. 使用 officecli_edit 执行编辑
4. 使用 officecli_preview 验证结果
5. 返回简洁摘要
```

**Day 3：测试和优化**

- 测试 Word/Excel/PPT 创建/读取/编辑/预览
- 优化输出格式（结构化摘要）
- 验证 token 开销

#### 2.1.3 Token 优化策略

| 策略 | 实现方式 | Token 节省 |
|------|----------|-----------|
| **主循环隐藏** | `reg.Hide()` | 600+ token/轮 |
| **子代理隔离** | `runAs: subagent` | 中间结果不进入主循环 |
| **结果压缩** | 解析 JSON，返回摘要 | 50-80% |
| **分页支持** | `offset/limit` 参数 | 避免一次性加载全部 |

#### 2.1.4 验收标准

- [ ] OfficeCLI 自动检测和安装
- [ ] Word 创建/读取/编辑/预览
- [ ] Excel 创建/读取/编辑/预览
- [ ] PPT 预览和验证
- [ ] 主循环 token 开销 < 30
- [ ] 错误处理和降级

---

### 2.2 PPT 增强（3-5 天）

#### 2.2.1 现状确认

需要检查 `internal/assets/pptauto/` 的实际实现：

**已实现：**
- ✅ SVG 路线生成
- ✅ 模板填充路线
- ✅ 模板颜色自动检测
- ✅ VLM 集成
- ✅ 演讲者备注

**待确认：**
- ❓ 图表支持
- ❓ 动画支持
- ❓ 过渡效果
- ❓ 实时预览

#### 2.2.2 增强方案

**Day 1-2：图表支持**

```python
# scripts/chart_generator.py

def generate_chart(chart_type, data, options=None):
    """生成图表 SVG"""
    if chart_type == "bar":
        return generate_bar_chart(data, options)
    elif chart_type == "line":
        return generate_line_chart(data, options)
    elif chart_type == "pie":
        return generate_pie_chart(data, options)
    elif chart_type == "scatter":
        return generate_scatter_chart(data, options)
```

**Day 3：动画支持**

```python
# scripts/animation_generator.py

def add_animation(slide, shape, animation_type, duration=0.5):
    """为形状添加动画"""
    # 使用 python-pptx 的动画 API
```

**Day 4：过渡效果**

```python
# scripts/transition_generator.py

def add_transition(slide, transition_type, duration=0.5):
    """添加幻灯片过渡效果"""
    # 使用 python-pptx 的过渡 API
```

**Day 5：实时预览（可选）**

```bash
# 添加 watch 模式
officecli watch slides.pptx
```

#### 2.2.3 Token 优化

**策略：** PPT 生成在子代理中运行，主循环仅看到最终结果。

**Token 开销：**
- 主循环：~30 token（skill 索引）
- 子代理：~1000-2000 token（生成过程）
- 最终结果：~200 token（文件路径 + 摘要）

#### 2.2.4 验收标准

- [ ] 支持 4 种图表类型（柱状图、折线图、饼图、散点图）
- [ ] 支持 5 种动画效果（淡入、飞入、缩放等）
- [ ] 支持 4 种过渡效果（淡入淡出、推入、擦除等）
- [ ] 实时预览可用（可选）

---

### 2.3 Skill 市场（3 天）

#### 2.3.1 架构设计

**关键发现：** `install_source` 工具已提供 80% 的基础设施！

**新增组件：**
1. 注册表客户端（`internal/marketplace/registry.go`）
2. 搜索工具（`internal/tool/builtin/search_skills.go`）
3. 配置字段（`[skills].registry_url`）

#### 2.3.2 实现步骤

**Day 1：注册表客户端**

```go
// internal/marketplace/registry.go

type Registry struct {
    baseURL    string
    httpClient *http.Client
}

type SkillInfo struct {
    Name        string   `json:"name"`
    Version     string   `json:"version"`
    Description string   `json:"description"`
    Author      string   `json:"author"`
    Keywords    []string `json:"keywords"`
    DownloadURL string   `json:"download_url"`
}

func (r *Registry) Search(query string) ([]SkillInfo, error) {
    // GET /skills?q={query}
    // 返回紧凑列表（name + description + version + author）
}

func (r *Registry) Get(id string) (*SkillInfo, error) {
    // GET /skills/{id}
}
```

**Day 2：搜索工具**

```go
// internal/tool/builtin/search_skills.go

type searchSkills struct {
    registry *marketplace.Registry
}

func (t *searchSkills) Name() string { return "search_skills" }
func (t *searchSkills) Description() string {
    return "Search the skill marketplace for available skills."
}
func (t *searchSkills) Schema() json.RawMessage {
    return json.RawMessage(`{
        "type": "object",
        "properties": {
            "query": {"type": "string", "description": "Search query"}
        },
        "required": ["query"]
    }`)
}
func (t *searchSkills) Execute(ctx context.Context, args json.RawMessage) (string, error) {
    // 调用 registry.Search()
    // 返回紧凑列表（每行 ~50 token）
    // 格式："- name [author] -- description"
}
```

**Day 3：集成和测试**

- 在 `boot.go` 中注册 `search_skills` 工具
- 添加 `[skills].registry_url` 配置
- 测试搜索/安装/更新流程

#### 2.3.3 Token 优化

**策略：**
- 搜索结果分层（每页 10-20 个）
- 使用 `clipRunes` 模式（每行 ~50 token）
- 本地缓存市场元数据
- 永不内联 Skill 内容

**Token 开销：**
- 搜索结果：~500-1000 token（10-20 个结果）
- 安装确认：~100 token
- 更新检查：~50 token

#### 2.3.4 验收标准

- [ ] 注册表 API 可用
- [ ] 搜索工具可用
- [ ] 安装/更新流程可用
- [ ] 版本管理可用
- [ ] Token 开销可控

---

### 2.4 错误恢复补全（1-2 天）

#### 2.4.1 缺失功能

| 功能 | 说明 | 工作量 |
|------|------|--------|
| **风暴断路器硬上限** | 添加 `maxStormBreaks` 计数器（5 次） | 0.5 天 |
| **错误分类** | 分类错误类型，选择不同恢复策略 | 0.5 天 |
| **Token 感知恢复** | 上下文紧张时压缩恢复消息 | 0.5 天 |
| **循环守卫去重** | 替换前一个 `[loop guard]` 而非追加 | 0.5 天 |

#### 2.4.2 实现方案

**风暴断路器硬上限：**

```go
// internal/agent/agent.go

const maxStormBreaks = 5

func (a *Agent) checkStormBreak(...) {
    // 现有逻辑...
    if a.stormBreakCount >= maxStormBreaks {
        return fmt.Errorf("storm breaker limit reached (%d), stopping to prevent infinite loop", maxStormBreaks)
    }
}
```

**错误分类：**

```go
// internal/agent/error_classifier.go

type ErrorType int

const (
    ErrorTypeTransient   ErrorType = iota // 网络、429、5xx
    ErrorTypeArgs                         // 参数错误
    ErrorTypePermission                   // 权限错误
    ErrorTypeUnknown                      // 未知错误
)

func classifyError(err error) ErrorType {
    // 根据错误类型分类
}
```

**Token 感知恢复：**

```go
// internal/agent/agent.go

func (a *Agent) streamRecoveryMessage(...) string {
    if a.contextUsage() > 0.9 {
        return "Continue."  // 上下文紧张时使用最短消息
    }
    return streamRecoveryMessage // 正常消息
}
```

**循环守卫去重：**

```go
// internal/agent/agent.go

func (a *Agent) deduplicateLoopGuard(messages []provider.Message) []provider.Message {
    // 找到前一个 [loop guard] 消息并替换
}
```

#### 2.4.3 验收标准

- [ ] 风暴断路器硬上限（5 次）
- [ ] 错误分类准确率 > 90%
- [ ] Token 感知恢复可用
- [ ] 循环守卫去重可用

---

### 2.5 编辑前验证（2 天）

#### 2.5.1 验证层级

| 层级 | 验证内容 | 语言支持 | 工作量 |
|------|----------|----------|--------|
| L1 | 语法检查 | Go, TypeScript, Python, JSON, YAML | 1 天 |
| L2 | AST 验证 | Go, TypeScript | 1 天 |

#### 2.5.2 实现方案

**语法检查：**

```go
// internal/validation/syntax.go

func ValidateSyntax(path string, content []byte) error {
    ext := filepath.Ext(path)
    switch ext {
    case ".go":
        return validateGoSyntax(content)
    case ".ts", ".tsx":
        return validateTypeScriptSyntax(content)
    case ".py":
        return validatePythonSyntax(content)
    case ".json":
        return validateJSONSyntax(content)
    case ".yaml", ".yml":
        return validateYAMLSyntax(content)
    default:
        return nil // 不支持的语言跳过验证
    }
}
```

**AST 验证：**

```go
// internal/validation/ast.go

func ValidateAST(path string, content []byte) error {
    ext := filepath.Ext(path)
    switch ext {
    case ".go":
        return validateGoAST(content)
    case ".ts", ".tsx":
        return validateTypeScriptAST(content)
    default:
        return nil
    }
}
```

#### 2.5.3 集成方式

在 `edit_file` 和 `write_file` 工具中添加验证：

```go
func (t *editFile) Execute(ctx context.Context, args json.RawMessage) (string, error) {
    // 现有逻辑...

    // 验证新内容
    if err := validation.ValidateSyntax(path, newContent); err != nil {
        return "", fmt.Errorf("syntax validation failed: %w", err)
    }

    // 写入文件
    // ...
}
```

#### 2.5.4 验收标准

- [ ] 语法检查支持 5+ 语言
- [ ] AST 验证支持 Go/TypeScript
- [ ] 验证失败提供清晰错误信息
- [ ] 验证不影响编辑性能（<100ms 延迟）

---

### 2.6 向量搜索（3 天）

#### 2.6.1 架构设计

**混合检索：**
```
查询
  ↓
┌─────────────────────────────────────┐
│  向量搜索（语义相似度）              │
│  ├─ 嵌入模型：text-embedding-3-small│
│  ├─ 向量数据库：SQLite + 向量扩展   │
│  └─ 相似度算法：余弦相似度          │
├─────────────────────────────────────┤
│  全文搜索（关键词匹配）              │
│  ├─ 引擎：FTS5                      │
│  └─ 排序：BM25                      │
├─────────────────────────────────────┤
│  结果融合                            │
│  ├─ 向量权重：0.6                   │
│  ├─ 全文权重：0.4                   │
│  └─ 去重 + 排序                     │
└─────────────────────────────────────┘
```

#### 2.6.2 实现步骤

**Day 1：向量存储**

```go
// internal/rag/vector.go

type VectorStore struct {
    db *sql.DB
}

func (vs *VectorStore) Insert(collection string, chunkID string, embedding []float32) error {
    // INSERT INTO rag_vectors (collection, chunk_id, embedding) VALUES (?, ?, ?)
}

func (vs *VectorStore) Search(collection string, query []float32, limit int) ([]SearchResult, error) {
    // 使用 SQLite 向量扩展进行余弦相似度搜索
}
```

**Day 2：嵌入生成**

```go
// internal/rag/embedding.go

type Embedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
    EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}

type OpenAIEmbedder struct {
    client *openai.Client
    model  string
}

func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
    // 调用 OpenAI embedding API
}
```

**Day 3：混合检索**

```go
// internal/rag/search.go

func HybridSearch(ctx context.Context, query string, collection string) ([]SearchResult, error) {
    // 1. 全文搜索
    ftsResults, _ := ftsSearch(query, collection)

    // 2. 向量搜索
    embedding, _ := embedder.Embed(ctx, query)
    vectorResults, _ := vectorStore.Search(collection, embedding, 100)

    // 3. 结果融合
    return mergeResults(ftsResults, vectorResults, 0.4, 0.6), nil
}
```

#### 2.6.3 验收标准

- [ ] 向量搜索支持 100K+ 文档
- [ ] 查询延迟 < 200ms
- [ ] 检索精度比纯 FTS5 提升 30%+
- [ ] 支持增量更新

---

## 三、Token 开销分析

### 3.1 各功能 Token 开销

| 功能 | 主循环开销 | 子代理开销 | 总计 |
|------|-----------|-----------|------|
| **OfficeCLI** | ~30 token | ~800 token | ~830 token |
| **PPT 增强** | ~30 token | ~2000 token | ~2030 token |
| **Skill 市场** | ~500 token | 0 | ~500 token |
| **错误恢复** | 0 | 0 | 0 |
| **编辑前验证** | 0 | 0 | 0 |
| **向量搜索** | 0 | 0 | 0 |

### 3.2 优化策略

| 策略 | 实现方式 | Token 节省 |
|------|----------|-----------|
| **主循环隐藏** | `reg.Hide()` | 600+ token/轮 |
| **子代理隔离** | `runAs: subagent` | 中间结果不进入主循环 |
| **结果压缩** | 解析 JSON，返回摘要 | 50-80% |
| **分页支持** | `offset/limit` 参数 | 避免一次性加载全部 |
| **ContextFilter** | 读侧投影 | 大文档自动压缩 |

---

## 四、实施计划

### 4.1 Phase 1: 核心能力（第 1-2 周）

| 任务 | 工作量 | 优先级 | 依赖 |
|------|--------|--------|------|
| OfficeCLI 集成 | 3 天 | P0 | 无 |
| PPT 图表支持 | 2 天 | P1 | 无 |
| PPT 动画支持 | 1 天 | P2 | 无 |

### 4.2 Phase 2: 生态建设（第 3 周）

| 任务 | 工作量 | 优先级 | 依赖 |
|------|--------|--------|------|
| Skill 市场基础 | 3 天 | P0 | 无 |
| 错误恢复补全 | 1-2 天 | P1 | 无 |

### 4.3 Phase 3: 智能增强（第 4 周）

| 任务 | 工作量 | 优先级 | 依赖 |
|------|--------|--------|------|
| 编辑前验证 | 2 天 | P1 | 无 |
| 向量搜索 | 3 天 | P2 | 无 |

**总计：15-20 天（约 3-4 周）**

---

## 五、验收标准

### 5.1 功能验收

- [ ] Word 创建/读取/编辑/预览
- [ ] Excel 创建/读取/编辑/预览
- [ ] PPT 图表支持（4 种类型）
- [ ] PPT 动画支持（5 种效果）
- [ ] Skill 市场可用（search/install/update）
- [ ] 风暴断路器硬上限（5 次）
- [ ] 语法检查支持 5+ 语言
- [ ] 向量搜索支持 100K+ 文档

### 5.2 性能验收

- [ ] 主循环 token 开销 < 50/轮
- [ ] Word/Excel 操作延迟 < 500ms
- [ ] PPT 图表生成延迟 < 2s
- [ ] 向量查询延迟 < 200ms
- [ ] 编辑验证延迟 < 100ms

### 5.3 安全验收

- [ ] Skill 签名验证通过
- [ ] 沙箱执行无逃逸
- [ ] 向量数据加密存储

---

## 六、风险与应对

| 风险 | 影响 | 应对措施 |
|------|------|----------|
| **OfficeCLI 兼容性** | 特定格式不支持 | 降级到原生处理 |
| **PPT 图表质量** | 图表不够专业 | 参考专业模板 |
| **Skill 市场安全** | 恶意 Skill 攻击 | 签名验证 + 沙箱 |
| **向量搜索性能** | 查询延迟高 | 索引优化 + 缓存 |

---

## 七、总结

### 关键改进

1. **工作量减少 33-50%** — 从 30 天减少到 15-20 天
2. **Token 开销最小化** — 主循环 < 50 token/轮
3. **复用现有架构** — 利用 install_source、ContextFilter 等
4. **渐进式实施** — 每个 Phase 独立可交付

### Hiq 提升后的定位

| 能力 | 评级 | 说明 |
|------|------|------|
| **AI 编程助手** | ⭐⭐⭐⭐⭐ | 多厂商 + 智能错误恢复 |
| **PPT 设计** | ⭐⭐⭐⭐⭐ | SVG + VLM + 图表动画 |
| **Word/Excel** | ⭐⭐⭐⭐ | OfficeCLI 完整能力 |
| **邮件/日历** | ⭐⭐⭐⭐⭐ | 独特优势 |
| **Skill 生态** | ⭐⭐⭐⭐ | 市场 + 版本管理 |
| **RAG 检索** | ⭐⭐⭐⭐ | 向量搜索 + 全文搜索 |

---

**Hiq — 以最小 token 开销实现最大能力提升！**
