# hiq 完整记忆系统设计方案

> 状态：Draft v3（定稿）| 创建：2026-06-24 | 修订：2026-06-24 | 目标版本：v0.4.0
> 本文档是 `memory-dual-time.md`（Phase 1-6）的超集，新增 Phase 7-9。

---

## A. 审查修正记录（v2→v3 变更说明）

> 基于代码审查（store.go 884 行）发现的 10 个问题，逐项修正。
> **以下修正已全部落实到正文对应章节，本节仅作变更记录参考。**

### P0 修正（影响实施准确性）

| # | 修正 | 落实位置 |
|---|------|---------|
| 1 | Phase 1 已完成，非"加字段"而是"激活序列化" | §7 Phase 1、§12 时间线 |
| 2 | §3.1 字段标注各字段所属 Phase | §3.1 |
| 3 | Phase 6 memory_query 应基于已有 SearchService 构建 | §7 Phase 6 |
| 4 | Phase 5 FTS 迁移需要 Rebuild() 步骤 | §7 Phase 5 |

### P1 修正（设计矛盾或歧义）

| # | 修正 | 落实位置 |
|---|------|---------|
| 5 | §14 与 §2 职责边界统一：文档层只放永久真理 | §14.1、§14.2 |
| 6 | §18 Q4 改为「全量索引 + 标注 status」 | §7 Phase 5、§18 Q4 |

### P2 修正（细节）

| # | 修正 | 落实位置 |
|---|------|---------|
| 7 | Decay() 需处理从未访问的记忆（CreatedAt 回退） | §7 Phase 7 代码 |
| 8 | §3.3 supersedes 字段语义说明 | §3.3 注释 |
| 9 | ListAll() 范围明确（active+dormant，不含 .archive/） | §7 Phase 7 |
| 10 | Phase 2 实施说明中加锁要求 | §7 Phase 2 代码 |

---

## B. 当前实现状态表（2026-06-24）

| Phase | 内容 | 状态 | 备注 |
|-------|------|------|------|
| Phase 1 | 全量字段 + 序列化 + 向后兼容 | ✅ **已完成** | store.go 884行，render/loadMemory/Save 均已实现 |
| Phase 2 | Supersede 机制 | 🔶 **部分完成** | `Supersede()`/`ListSuperseded()`/`archiveInDirWithStatus()` 已有；但 `remember.Execute()` 尚未在写入前调用 Supersede |
| Phase 3 | Valid Time / ListAsOf | ✅ **已完成** | `ListAsOf()` + `parseDate()` 已实现（L757-789） |
| Phase 4 | 写入时冲突检测 | ❌ 未开始 | conflict.go 不存在 |
| Phase 5 | FTS 过滤 + schema 迁移 | ❌ 未开始 | FTS schema 未更新，无 status 列 |
| Phase 6 | memory_query 工具 | ❌ 未开始 | 已有 SearchService，需加工具包装 |
| Phase 7 | 衰减 + TTL + memory_recall | ❌ 未开始 | 新字段未加入 Memory 结构体 |
| Phase 8 | 压缩 + 用户画像 | ❌ 未开始 | |
| Phase 9 | 健康仪表盘 + 文档 | ❌ 未开始 | |

**最近可执行任务（按优先级）：**
1. **Phase 2 补全**：在 `remember.Execute()` 中加入 `Supersede()` 调用，并加锁保护事务
2. **Phase 4**：实现 `conflict.go`（Phase 2 完成后自然衔接）
3. **Phase 5**：FTS schema 迁移 + status 过滤
4. **Phase 6**：基于已有 `SearchService` 包装 `memory_query` 工具

---

## 0. 现状与根因

### 0.1 现有记忆规则（已实现）

```
两层记忆结构：

层 1：文档记忆（Doc Memory）— 静态，人工维护
  读取路径（优先级从低到高）：
  ① ~/.config/hiq/hiq.md        ScopeUser（全局，跨项目）
  ② 祖先目录中的 hiq.md / AGENTS.md  ScopeAncestor
  ③ ./hiq.md / AGENTS.md / CLAUDE.md ScopeProject（提交共享）
  ④ ./hiq.local.md / AGENTS.local.md ScopeLocal（个人，git-ignored）
  支持 @path 导入语法（最多 5 层递归）
  全文注入系统提示词前缀，每次会话字节级稳定

层 2：自动记忆（Auto Memory）— 动态，工具驱动
  类型路由：
  - user / feedback → global memory（跨项目共享）
  - project / reference → project memory（项目独立）
  写入：remember 工具 → frontmatter .md 文件 + MEMORY.md 索引
  删除：forget 工具 → 归档到 .archive/（不永久删除）
  加载：MEMORY.md 索引（一行摘要）注入前缀，全文按需 read_file
  搜索：FTS5 SQLite（memory_fts.db），BM25 排名
```

### 0.2 当前已知问题（修订后）

| 问题 | 影响 | 状态 |
|------|------|------|
| ~~Memory 结构体无时间字段~~ | ~~覆盖即丢失~~ | ✅ Phase 1 已修复 |
| remember.Execute() 覆盖不触发 Supersede | 旧事实被静默覆盖 | 🔶 Phase 2 部分修复（方法存在，集成缺失） |
| 无遗忘/衰减机制 | 索引无限增长 | ❌ Phase 7 待实现 |
| 无召回机制 | 归档后无法检索 | ❌ Phase 7 待实现 |
| 无压缩机制 | 事实碎片化、冗余 | ❌ Phase 8 待实现 |
| 无用户画像结构 | 对话记忆无体系 | ❌ Phase 8 待实现 |
| FTS 无 status 过滤 | 搜索返回过时数据 | ❌ Phase 5 待实现 |
| 无 schema_version 检测 | 数据库升级无法迁移 | ❌ Phase 5 待实现 |

---

## 1. 设计目标

| # | 目标 | 验收标准 |
|---|------|---------|
| G1 | 记忆有创建/更新时间 | frontmatter 含 created_at / updated_at |
| G2 | 旧事实不丢失 | 覆盖时旧记录标记 superseded，保留历史 |
| G3 | 支持时间点回溯 | 能回答「3月住哪」 |
| G4 | 默认只返回当前有效 | 系统提示词只注入 status=active |
| G5 | 写入时冲突检测 | 新事实矛盾时自动作废旧事实 |
| G6 | 向后兼容 | 无时间字段的旧文件正常读取 |
| G7 | 记忆能够遗忘 | 长期未访问的事实自动降级为 dormant |
| G8 | 遗忘可以召回 | dormant 事实可通过工具或搜索重新激活 |
| G9 | 索引规模可控 | active 索引超阈值时触发压缩 |
| G10 | 用户画像结构化 | user 类事实按分类组织，支持 profile 视图 |
| G11 | TTL 自动过期 | 短期事实到期自动归档 |

---

## 2. 用户画像分类学

### 2.1 what to remember

```
user 类事实（存 global memory，跨项目共享）
├── identity          身份与背景（几乎不变）
│   ├── user-role          职业/角色
│   ├── user-expertise     技术专长领域
│   ├── user-experience    经验年限与深度
│   └── user-background    教育/行业背景
│
├── style             工作风格（变化很慢）
│   ├── user-comm-style    沟通风格（简洁/详细、结论前置）
│   ├── user-code-style    代码风格（命名/注释/函数粒度）
│   ├── user-review-style  审查习惯
│   └── user-output-format 输出格式偏好
│
├── belief            技术观念（变化慢）
│   ├── user-arch-style    架构哲学
│   ├── user-lang-prefs    语言偏好排序
│   ├── user-lib-prefs     框架/库好恶
│   └── user-antipatterns  明确禁止的模式
│
├── temporal          时效性信息（可变，需 valid_from/to）
│   ├── user-location      所在城市
│   ├── user-timezone      时区
│   ├── user-company       所在公司/团队
│   └── user-current-role  当前岗位
│
└── feedback          对模型行为的纠正
    ├── 模型做错了什么（已纠正的习惯）
    ├── 用户明确喜欢的模式
    └── 用户明确不喜欢的模式

project 类事实（存 project memory，项目独立）
├── project-goal       当前目标（建议加 TTL）
├── project-constraint 约束与限制
├── project-decision   已做的关键决策
└── project-milestone  重要里程碑
```

### 2.2 hiq.md 内容边界

**应该写入 hiq.md（永久真理，不会过时）：**
- 身份定位（工程师 / 架构师）
- 不变的工作原则（结论优先、不硬编码）
- 明确禁止列表（不用 Tailwind、不用 os.Exit）

**不应该写入 hiq.md（交给 remember 工具）：**
- 居住城市（时效性，用 valid_from/to）
- 当前项目目标（用 TTL 管理周期）
- 模型的历次纠错（用 type=feedback 记录）

---

## 3. 数据模型

### 3.1 Memory 结构体（完整版）

```go
type Memory struct {
    Name        string
    Title       string
    Description string
    Type        Type
    Body        string

    // 时间字段（Phase 1 ✅ 已实现，store.go L91-106）
    CreatedAt      time.Time `json:"created_at,omitempty"`
    UpdatedAt      time.Time `json:"updated_at,omitempty"`
    ValidFrom      string    `json:"valid_from,omitempty"`  // YYYY-MM-DD
    ValidTo        string    `json:"valid_to,omitempty"`    // 空=当前有效

    // 状态字段（Phase 1 ✅ 已实现，render()/loadMemory() 均已读写）
    Status         string    `json:"status,omitempty"`      // active/superseded/archived/dormant
    Supersedes     string    `json:"supersedes,omitempty"`
    SupersededBy   string    `json:"superseded_by,omitempty"`

    // 访问追踪（Phase 7 新增）
    LastAccessedAt time.Time `json:"last_accessed_at,omitempty"`
    AccessCount    int       `json:"access_count,omitempty"`

    // 生命周期（Phase 7 新增）
    TTL            string    `json:"ttl,omitempty"`         // YYYY-MM-DD，到期自动归档
    Importance     string    `json:"importance,omitempty"`  // high/medium/low

    // 分类（Phase 8 新增）
    Tags           []string  `json:"tags,omitempty"`
    Category       string    `json:"category,omitempty"`    // identity/style/belief/temporal/feedback
}
```

### 3.2 Status 状态机

```
         remember(同名)           forget
active ─────────────► superseded ──────────► .archive/
  │                   .archive/                  ▲
  │ forget                                       │
  └────────────────────────────► archived ────────
  │                              .archive/
  │ 未访问 > decay_days（importance 非 high）
  └────────────────────────────► dormant（原位保留，退出索引）
                                      │
                              memory_recall / FTS 命中并唤醒
                                      │
                                   active
```

### 3.3 Frontmatter 示例（完整字段）

```yaml
---
name: user-location
title: 用户居住城市
description: 用户当前居住的城市（时效性信息）
metadata:
  type: user
  category: temporal
  importance: medium
  tags: ["location", "lifestyle"]
  created_at: "2026-03-10T09:00:00Z"
  updated_at: "2026-06-01T08:00:00Z"
  last_accessed_at: "2026-06-20T14:30:00Z"
  access_count: 7
  valid_from: "2026-05-01"
  valid_to: ""
  ttl: ""
  status: active
  # supersedes 语义：本条记录替代了哪条旧记录的 name。
  # 同名覆盖时 supersedes 指向自身同名，表示"本记录是同名旧版本的新版本"。
  supersedes: "user-location"
  superseded_by: ""
---

用户自2026年5月起居住在上海。
```

---

## 4. 三温存储分层

```
┌──────────────────────────────────────────────────┐
│  HOT（每次会话自动加载）                             │
│  MEMORY.md 索引（一行摘要 × status=active）         │
│  建议上限：50 条 / ~3KB                             │
│  hiq.md 文档（永久真理，手动维护）               │
└──────────────────────────────────────────────────┘
                │ 超过阈值 → 衰减/压缩
                ▼
┌──────────────────────────────────────────────────┐
│  WARM（按需加载）                                   │
│  active .md 文件（read_file 读全文）                │
│  dormant .md 文件（退出索引，FTS 仍索引）             │
│  memory_query 关键词搜索可命中并唤醒                  │
└──────────────────────────────────────────────────┘
                │ 超过 cold_days 未访问
                ▼
┌──────────────────────────────────────────────────┐
│  COLD（归档，可检索）                               │
│  .archive/ 中的 superseded / archived 文件         │
│  memory_query(as_of=...) 时间点查询可命中            │
│  memory_recall(name) 可手动唤醒为 active            │
└──────────────────────────────────────────────────┘
```

---

## 5. 遗忘机制（四种）

### 5.1 手动遗忘（已有，待增强）
- 工具：`forget`，模型判断事实完全过时时调用
- 行为：归档到 `.archive/`（status=archived），MEMORY.md 移除
- 增强：归档前记录 access_count / last_accessed_at

### 5.2 自动衰减（Decay）— Phase 7 新增
```
衰减规则（可配置）：
  decay_days: 30   超过 N 天未访问 → dormant
  cold_days:  90   dormant 超过 N 天 → 建议归档

importance 修正：
  high   → 豁免，永不自动降级
  medium → 标准 decay_days
  low    → decay_days ÷ 2（更快衰减）

dormant 行为：
  - .md 文件原位保留（不移动）
  - 从 MEMORY.md 索引移除（Hot 层退出）
  - FTS 保持索引（Warm 层，可搜索）
  - status 更新为 "dormant"
```

### 5.3 TTL 过期 — Phase 7 新增
```
使用场景：
  "记住我本周目标是完成 Phase 1" → ttl: "2026-06-28"
  到期后自动归档，下次会话不再出现在索引中

配置：ttl_check_on_start = true（启动时扫描）
```

### 5.4 压缩（Compaction）— Phase 8 新增
```
触发条件：
  active 行数 > hot_limit（默认 50）
  或 memory_compact 工具手动触发

压缩三步：
  步骤 A — 降级低频：access_count=0 且 importance≠high → dormant
            按 access_count 升序处理，直到 active ≤ hot_limit × 0.8
  步骤 B — LLM 合并：同 category + 相似 tags → 合并为一条
            旧条目标记 superseded，链接到新合并条目
  步骤 C — 归档冷数据：dormant + last_accessed > cold_days → archived
```

---

## 6. 召回机制（五种）

| # | 召回类型 | 触发方式 | 数据来源 |
|---|---------|---------|---------|
| 1 | 索引召回 | 会话启动自动 | MEMORY.md（Hot） |
| 2 | FTS 语义召回 | memory_query(query=...) | active+dormant FTS |
| 3 | 时间点召回 | memory_query(as_of=...) | .archive/ Cold 层 |
| 4 | 冬眠唤醒 | memory_recall(name) | dormant → active |
| 5 | 链式召回 | ListSuperseded(name) | supersede 链 |

### 6.1 冬眠唤醒工具

```go
// memory_recall：将 dormant 重新激活为 active
{
  "name": "memory_recall",
  "description": "Reactivate a dormant memory fact back into the active index. Use when search results show a relevant dormant fact that should be in the hot layer again.",
  "properties": {
    "name": {"type": "string", "description": "Slug of the dormant memory to recall."}
  }
}
```

行为：
1. 读取 dormant .md 文件
2. 更新 status=active，last_accessed_at=now
3. 重新写入 MEMORY.md 索引行
4. 通过 QueueMemory 通知模型

---

## 7. 实施分阶段（全 9 个 Phase）

> Phase 1-6 来自 memory-dual-time.md，Phase 7-9 为新增。
> 状态标记：✅ 已完成 | 🔶 部分完成 | ❌ 未开始

### ✅ Phase 1：全量字段 + 时间戳 —— 已完成（store.go 884 行）

**实际已实现：**
- Memory 结构体已有全部 7 个 bitemporal 字段（L91-106）
- `render()` 已写入所有字段（L567-602，含向后兼容 omitempty）
- `loadMemory()` 已解析所有字段，CreatedAt 缺省回退 file mtime（L796-834）
- `Save()` 已保留 CreatedAt、设置 UpdatedAt、默认 status=active（L224-266）
- `Block()` 已注入当前日期（L134：`Today's date is %s`）
- `remember.Execute()` 已支持 valid_from/valid_to 参数（remember.go L55-56、L69-70）

**无需额外工作，直接进入 Phase 2。**

---

### 🔶 Phase 2：Supersede 机制 —— 部分完成

**已有：** `Supersede(name, validTo)`、`Get(name)`、`ListSuperseded(name)`、`archiveInDirWithStatus()` 均已实现（store.go）

**缺失（唯一剩余工作）：** `remember.Execute()` 写入前未调用 `Supersede()`

**实施说明：**

```go
// remember.go — Execute() 中，在 store.Save(m) 之前插入：
// ⚠️ 整个事务需在 indexMu 锁内执行，防止并发写相同记忆导致链断裂
mu := indexLockFor(dir)
mu.Lock()
defer mu.Unlock()

// 1. 检查是否存在同名 active 旧记录
if old, ok := t.store.Get(m.Name); ok && old.Status == "active" {
    // 2. 推导旧记录的 valid_to
    validTo := m.ValidFrom // 新记录 valid_from 即旧记录 valid_to
    if validTo == "" {
        validTo = time.Now().UTC().Format("2006-01-02")
    }
    // 3. 将旧记录标记为 superseded，移入 .archive/
    _ = t.store.Supersede(m.Name, validTo)
    m.Supersedes = m.Name // 同名覆盖：supersedes 指向自身旧版本
}
// 4. 保存新记录
path, err := t.store.Save(m)
```

**验收：**
- 同名两次 remember → 旧记录在 `.archive/`（status=superseded），新记录为 active
- `Supersedes` / `SupersededBy` 链正确
- `ListSuperseded("user-location")` 返回历史链
- 并发写同名记忆不会出现链断裂（加锁保护）

---

### ✅ Phase 3：Valid Time 时间线查询 —— 已完成（store.go L757-789）

**实际已实现：**
- `ListAsOf(t time.Time)` + `parseDate(s string)` 均已完整实现（store.go L757-789）
- `remember.Execute()` 已支持 valid_from/valid_to 参数（remember.go L55-56、L69-70、L92-93）

**无需额外工作。**

---

### ❌ Phase 4：写入时冲突检测（2 天）

**改动：** `conflict.go`（新）LLM 判断矛盾；`remember.Execute()` 编排冲突→Supersede→Save；`boot.go` 注入 LLM provider

**依赖：** Phase 2 补全后自然衔接（共用 Supersede() 调用链）

---

### ❌ Phase 5：FTS 检索过滤（1 天）

**改动：** FTS schema 加 `status`/`valid_from`/`valid_to` 列；`Search()` 默认过滤 active；`reconcile.go` 读新字段；`service.go` 加 `SearchAsOf`；`memory.go` Block 只注入 active

**⚠️ FTS5 迁移注意事项：** FTS5 **不支持** `ALTER TABLE`，加新列必须：

```go
// fts.go — 新增 ensureSchema()，在 OpenFTSStore 时调用
func (s *FTSStore) ensureSchema() error {
    var version int
    _ = s.db.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
    if version < 2 {
        // FTS5 不支持 ALTER TABLE，必须 DROP + recreate + 全量重建
        if _, err := s.db.Exec("DROP TABLE IF EXISTS memory_fts"); err != nil {
            return err
        }
        if _, err := s.db.Exec(ftsSchemaV2); err != nil { // 含 status/valid_from/valid_to 列
            return err
        }
        _, _ = s.db.Exec("CREATE TABLE IF NOT EXISTS schema_version (version INTEGER)")
        _, _ = s.db.Exec("INSERT OR REPLACE INTO schema_version VALUES (2)")
        return s.Reconcile(/* store */) // 全量重建索引
    }
    return nil
}
```

**FTS 结果携带 status：** `FTSResult` 新增 `Status` 字段，搜索结果标注 active/dormant/archived，模型自行决定是否引用。

---

### ❌ Phase 6：memory_query 工具（1 天）

**改动：** `query.go`（新）memory_query 工具；`boot.go` 注册；全面测试

**⚠️ 基于已有 SearchService 包装：** `service.go` 已有 `SearchService.Search()` 方法，Phase 6 **基于它包装**，不需要从头实现搜索逻辑。

```go
// query.go — 只需包装 SearchService
func (t queryTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
    // 解析 query + as_of 参数
    // 如有 as_of → s.service.SearchAsOf(query, asOfTime)
    // 否则    → s.service.Search(query)
    // 结果格式化返回，包含每条结果的 status 字段
}
```

---

### ❌ Phase 7：访问追踪 + 衰减 + TTL（2 天）✨ 新增

**目标：** 实现记忆的自然遗忘；长期未访问的事实退出 Hot 层，TTL 到期自动归档。

**改动文件：**

| 文件 | 改动 |
|------|------|
| `store.go` | Memory 加 LastAccessedAt/AccessCount/TTL/Importance/Tags/Category；Get() 更新访问统计；新增 Decay(cfg)/ExpireTTL()/ListDormant()/ListAll() |
| `reconcile.go` | Reconcile 时顺带调用 Decay() 和 ExpireTTL() |
| `remember.go` | Schema 加 ttl/importance/tags/category 参数 |
| `recall.go`（新） | memory_recall 工具：dormant → active |
| `boot.go` | 注册 memory_recall |
| `store_extra_test.go` | 衰减/TTL/访问统计测试 |

**关键逻辑：**

```go
type DecayConfig struct {
    DecayDays int // 默认 30
    ColdDays  int // 默认 90
    HotLimit  int // 默认 50
}

func (s Store) Decay(cfg DecayConfig) (dormantCount int, err error) {
    now := time.Now().UTC()
    for _, m := range s.ListAll() { // ListAll: active + dormant（不含 .archive/）
        if m.Status != "active" { continue }
        if m.Importance == "high" { continue } // 豁免
        threshold := cfg.DecayDays
        if m.Importance == "low" { threshold /= 2 }

        // LastAccessedAt.IsZero()（从未访问）时以 CreatedAt 为参考点，
        // 避免新写入但从未读取的记忆永不衰减。
        refTime := m.LastAccessedAt
        if refTime.IsZero() {
            refTime = m.CreatedAt
        }
        if now.Sub(refTime) > time.Duration(threshold)*24*time.Hour {
            m.Status = "dormant"
            m.UpdatedAt = now
            _ = s.saveInPlace(m) // 只更新文件，不移动
            _ = removeFromIndex(s.DirFor(m.Type), m.Name)
            dormantCount++
        }
    }
    return
}

func (s Store) ExpireTTL() error {
    today := time.Now().UTC().Format("2006-01-02")
    for _, m := range s.List() {
        if m.TTL != "" && m.TTL <= today {
            _ = s.archiveAsArchived(m)
        }
    }
    return nil
}
```

**List 方法分层：**
```go
List()         []Memory         // 只返回 active（Hot 层）
ListDormant()  []Memory         // 只返回 dormant（Warm 层）
ListAll()      []Memory         // active + dormant，遍历活跃目录非 .archive/ 文件（供 Decay 检查）
ListArchived() []ArchivedMemory // .archive/ 内容（Cold 层，已有）
```

**验收：**
- 31 天未访问的 medium 事实 → dormant，从 MEMORY.md 消失，FTS 仍命中
- TTL 到期事实 → 自动归档
- memory_recall("user-location") → dormant 变 active，重回索引
- importance=high 的事实不自动降级

---

### ❌ Phase 8：压缩 + 用户画像视图（2 天）✨ 新增

**目标：** active 超阈值时智能压缩；提供结构化用户画像。

**改动文件：**

| 文件 | 改动 |
|------|------|
| `compact.go`（新） | memory_compact 工具；三步压缩策略 |
| `profile.go`（新） | memory_profile 工具；按 category 输出画像 |
| `store.go` | 新增 ListByCategory(category string) |
| `boot.go` | 注册 memory_compact / memory_profile |
| `compact_test.go`（新） | 压缩测试 |

**memory_profile 输出示例：**
```
## User Profile (2026-06-24)

### Identity
- Role: Backend Engineer, Go / Distributed Systems
- Experience: 8+ years

### Style
- Communication: Concise, conclusion-first
- Code: Self-documenting names, minimal comments

### Technical Beliefs
- Architecture: High cohesion, low coupling, SRP
- Languages: Go > Python > TypeScript
- Banned: Tailwind CSS, os.Exit in libraries

### Temporal (time-sensitive)
- Location: Shanghai (since 2026-05-01)

### Feedback to Agent
- ✗ Don't use generic table format without being asked
- ✓ Prefer code-first explanations
```

**验收：**
- active 超 50 条 → compact 后降至 ≤ 40 条
- 相关事实正确合并，旧条目 status=superseded
- memory_profile 按 category 分组输出完整画像

---

### ❌ Phase 9：健康仪表盘 + 文档（1 天）✨ 新增

**改动文件：**

| 文件 | 改动 |
|------|------|
| `status.go`（新） | memory_status 工具 |
| `boot.go` | 注册 memory_status |
| `hiq.example.toml` | 新增 [memory] 配置区块 |
| `doc.go` | 更新包注释 |
| `CHANGELOG.md` | 记录变更 |

**memory_status 输出示例：**
```
## Memory System Status (2026-06-24)

### Global Memory
  Active:     23 facts  (Hot)
  Dormant:     7 facts  (Warm, searchable)
  Archived:   12 facts  (Cold, time-queryable)
  Superseded:  8 facts  (Cold, history chain)

### Health
  Hot layer: 23/50 (46%) — healthy
  ⚠️  Oldest active unaccessed: user-timezone (47 days, threshold: 30)
  ⚠️  TTL expiring soon: project-week-goal (expires 2026-06-28)

### Suggestions
  - Run memory_compact: 3 related style facts can be merged
  - Consider forget: user-old-company (superseded 90+ days ago)
```

---

## 8. 新增工具汇总

| 工具 | Phase | 功能 |
|------|-------|------|
| `remember` | Ph1/3/4/7 | 保存事实（扩展：valid_from/to/ttl/importance/tags/category） |
| `forget` | 已有 | 手动删除事实 |
| `memory_query` | Ph6 | 关键词+时间点查询（active+dormant+archived） |
| `memory_recall` | Ph7 | 将 dormant 事实唤醒回 active |
| `memory_compact` | Ph8 | 手动触发压缩（降级+LLM合并） |
| `memory_profile` | Ph8 | 输出结构化用户画像 |
| `memory_status` | Ph9 | 记忆系统健康状态报告 |

---

## 9. 配置项（hiq.toml）

```toml
[memory]
  decay_days         = 30    # 未访问 N 天后降为 dormant
  cold_days          = 90    # dormant N 天后建议归档
  hot_limit          = 50    # MEMORY.md 最大行数
  compact_threshold  = 0.9   # hot_limit × 此比例时提示压缩（0=禁用自动）
  compact_model      = ""    # 留空复用主模型，可指定轻量模型
  ttl_check_on_start = true  # 启动时自动归档过期事实
```

---

## 10. 文件变更总览

| 文件 | Ph1 | Ph2 | Ph3 | Ph4 | Ph5 | Ph6 | Ph7 | Ph8 | Ph9 |
|------|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| `store.go` | ✅ | ✅ | ✅ | | | | ❌ | ❌ | |
| `remember.go` | ✅ | | ✅ | ❌ | | | ❌ | | |
| `memory.go` | ✅ | | | | ❌ | | | | |
| `fts.go` | | | | | ❌ | | ❌ | | |
| `reconcile.go` | | | | | ❌ | | ❌ | | |
| `service.go` | | | | | ❌ | | | | |
| `conflict.go`（新） | | | | ❌ | | | | | |
| `query.go`（新） | | | | | | ❌ | | | |
| `recall.go`（新） | | | | | | | ❌ | | |
| `compact.go`（新） | | | | | | | | ❌ | |
| `profile.go`（新） | | | | | | | | ❌ | |
| `status.go`（新） | | | | | | | | | ❌ |
| `boot.go` | | | | ❌ | | ❌ | ❌ | ❌ | ❌ |
| `doc.go` | | | | | | ❌ | | | ❌ |
| 测试文件 | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | |

---

## 11. 风险与缓解

| 风险 | 影响 | 缓解 |
|------|------|------|
| 衰减误判（重要事实被降级） | 中 | importance=high 豁免；memory_recall 随时唤醒 |
| LLM 压缩合并出错 | 中 | 旧条目保持 superseded 可追溯；合并前可配置确认 |
| access_count 追踪开销 | 低 | 只在 Get() 时更新，写入合并到 Save() |
| .archive/ 目录膨胀 | 低 | memory_status 建议定期清理 |
| dormant FTS 索引持续增长 | 低 | cold_days 触发归档后移出 FTS；定期 Rebuild() |

---

## 12. 时间线（已更新至当前实现状态）

```
已完成（无需排期）：
  ✅ Phase 1  全量字段 + 序列化 + 向后兼容
  ✅ Phase 3  Valid Time / ListAsOf
  🔶 Phase 2  Supersede 方法已有，remember 集成缺失

剩余排期：
  Now    Phase 2 补全（~1 天）  ← 起点：remember.Execute() 加 Supersede 调用 + 加锁
  Week 1  Phase 4（冲突检测，2 天）
  Week 2  Phase 5（FTS schema 迁移 + status 过滤，1 天）
           Phase 6（memory_query 工具，基于已有 SearchService，1 天）
  Week 3  Phase 7（衰减 + TTL + memory_recall，2 天）
  Week 4  Phase 8（压缩 + 用户画像，2 天）
           Phase 9（仪表盘 + 文档，1 天）
```

总计剩余 **~10 个工作日**（Phase 1/3 已完成，节约 3 天）。

---

## 13. 不做的事

- **不做** 跨项目记忆关联
- **不做** 实时多设备同步
- **不做** 完整时序知识图谱（Graphiti 级别）
- **不做** Ebbinghaus 遗忘曲线（过度工程）
- **不做** 向量嵌入语义搜索（FTS5 BM25 足够）

---

## 14. Phase 0：引导文档设计（执行前的准备）

> 这一 Phase 不涉及代码，是在开始实施剩余 Phase 之前，先把「静态文档层」的结构定下来。

### 14.1 文档层与自动记忆层的职责边界

**判断标准：这条信息 5 年后还成立吗？**
- 是 → 文档层（hiq.md）
- 否/不确定 → 自动记忆层（remember 工具）

```
文档层（hiq.md）只负责：
  ✅ 身份定义（我是谁）
  ✅ 绝对不变的工作原则（永久真理）
  ✅ 红线禁止事项（如：不用 os.Exit、不硬编码）
  ✅ 模型行为准则（如何使用记忆系统）

  ❌ 不包括技术偏好（语言优先级等会随时间变化，用 remember type=user, category=belief 记录）
  ❌ 不包括架构偏好（同上，有时效性）

自动记忆层（remember 工具）负责：
  ✅ 技术偏好和架构观念（type=user, category=belief）
  ✅ 时效性事实（居住地、当前公司、当前项目目标）
  ✅ 对话中产生的纠错（type=feedback）
  ✅ 短期目标（带 TTL）
  ✅ 任何有可能随时间变化的信息
```

### 14.2 推荐的 `~/.config/hiq/hiq.md` 结构

```markdown
# 用户画像（User Profile）

> 本文件是 hiq 的全局用户文档，加载到每次会话的系统提示词前缀。
> 保持简洁（目标 2KB 以内）。时效性信息请用 remember 工具保存，不要写在这里。

## 身份

- **职业：** [后端工程师 / 架构师 / ...]
- **专长：** [Go、分布式系统、...]
- **当前主项目：** [项目名称]（时效性强的项目信息用 remember 记录）

## 工作原则（不变）

- 结论优先，解释随后
- 不硬编码，配置驱动
- 高内聚、低耦合、单一职责

## 代码风格

- 变量名自解释（Self-Documenting）
- 注释解释"为什么"而非"是什么"
- 错误必须有明确日志，禁止空 catch/except

## 明确禁止

- 不使用 Tailwind CSS
- 库代码不调用 os.Exit
- 不做全局状态的硬编码

> **技术偏好（语言优先级、架构哲学）不要写在本文件中。**
> 请用 `remember type=user, category=belief` 记录，这样可以追踪变化并在偏好改变时自动 supersede。

## 对 hiq 的行为要求

- 回答简洁，不要无意义铺垫
- 代码块优先，解释跟在代码后
- 不确定时主动澄清，而非假设
- 遇到涉及记忆的话题，主动用 memory_status 检查健康度

## 记忆系统使用规范（供模型读）

请参阅 @memory-rules.md
```

### 14.3 独立的 `memory-rules.md`（用 @import 引入）

将记忆行为规范单独存放，避免主画像文件膨胀：

```markdown
# 记忆系统规范（Memory Rules）

## 什么时候 remember

触发 remember 的信号：
- 用户说了关于自己的事实（"我最近搬到了..."、"我们团队用..."）
- 用户纠正了模型的行为（"不要这样...，改成..."）
- 用户表达了明确的偏好（"我更喜欢...而不是..."）
- 用户设定了目标（"这周要完成..."、"下个月要上线..."）
- 用户提到了重要的项目决策

## 字段填写规范

type 选择：
  user     → 关于用户本人的事实（偏好、背景、位置）
  feedback → 对模型行为的纠正或引导
  project  → 项目特定的目标、决策、约束
  reference → 外部资源指针（URL、文档、ticket）

category 选择（type=user 时必填）：
  identity  → 身份与背景（不变）
  style     → 工作风格（变化慢）
  belief    → 技术观念（变化慢）
  temporal  → 时效性事实（必须填 valid_from）
  feedback  → 对模型的纠正

importance 选择：
  high   → 核心身份信息、绝对禁止事项（不自动衰减）
  medium → 普通偏好（默认值）
  low    → 临时性、可有可无的细节

valid_from / valid_to（temporal 类必填）：
  - 格式：YYYY-MM-DD
  - "用户3月在北京" → valid_from: "2026-03-01"
  - 新事实替代旧事实时，旧事实的 valid_to 自动设为新事实 valid_from 的前一天

ttl（短期事实必填）：
  - "本周目标" → ttl: 本周日期
  - "这次会议的结论" → ttl: 会议当天 + 7 天

## 什么时候 forget

- 用户明确说某个记忆已经过时
- 发现两条功能相同的重复记忆
- 事实已经被新事实 supersede 且不再有历史价值

## 定期维护建议

每隔 2-4 周调用 memory_status，根据建议：
- 运行 memory_compact 合并相关事实
- 对 TTL 到期提醒进行处理
- 对长期 dormant 的事实决定是否 memory_recall 或彻底 forget
```

### 14.4 项目级 `./hiq.md` 结构

```markdown
# [项目名称] 记忆

## 项目简介（永久）
[一段话描述项目是什么]

## 架构约定（永久）
[核心的架构决策，如：依赖方向、包结构等]

## 编码规范（永久）
[项目特定的规范，如：错误处理格式、日志格式等]

## 构建与测试
[构建命令、测试命令]

> 注：时效性的目标、决策、里程碑请用 remember 工具（type=project）记录，
> 不要写在本文件中，因为本文件全文注入每次会话。
```

---

## 15. 模型行为引导设计

> 这是整个记忆系统能否真正工作的关键层，当前计划完全缺失。
> 记忆系统不会自动工作，必须通过系统提示词和工具描述引导模型行为。

### 15.1 remember 工具 Description 的完整引导文本（Phase 7 后）

```
Save a durable fact to memory so it survives across sessions.

WHEN TO USE:
  - User mentions a personal fact (location, job, preference, background)
  - User corrects your behavior ("don't do X, do Y instead")
  - User states a goal with a time horizon
  - A project decision is made that future sessions should know

HOW TO CHOOSE FIELDS:
  type:
    "user"      → facts about the user personally
    "feedback"  → corrections to your behavior
    "project"   → project-specific goals, constraints, decisions
    "reference" → external resource pointers (URLs, tickets)

  category (required when type=user):
    "identity"  → who the user is (role, background) — use importance=high
    "style"     → how they prefer to work (communication, code style)
    "belief"    → their technical philosophy and preferences
    "temporal"  → time-sensitive facts (location, job) — MUST set valid_from
    "feedback"  → corrections to agent behavior

  importance:
    "high"   → core identity, absolute prohibitions (never auto-decays)
    "medium" → general preferences (default)
    "low"    → transient details

  valid_from / valid_to (YYYY-MM-DD):
    Required for category=temporal. "User moved to Shanghai in May" →
    valid_from: "2026-05-01". Leave valid_to empty (= currently true).

  ttl (YYYY-MM-DD):
    For time-bounded facts: "this week's goal" → ttl: end of this week.

  tags: Free labels for grouping, e.g. ["location", "lifestyle"]

BEFORE SAVING:
  Check the loaded memory index for an existing entry with the same name.
  Reusing the name updates it (supersedes the old record, keeping history).
  Do NOT create near-duplicate entries — merge into the existing one.
```

### 15.2 系统提示词中需要新增的「记忆系统使用协议」

> 新增到 `memory.go` 的 `Block()` 方法输出，放在 Saved memories 区块之前。

```markdown
## Memory System Protocol

Current date: {CURRENT_DATE}

You maintain a structured memory system. At the start of each session:
1. The memory index above shows your active facts — treat them as current truth.
2. Facts marked as potentially stale should be verified before acting on them.
3. Use `memory_query` to search for relevant dormant facts before assuming ignorance.

During the session:
- When the user reveals personal facts, preferences, or corrections → call `remember`
- When a fact is superseded by new information → the system auto-handles it if you
  use the same `name`; or call `forget` if the old fact should not exist at all.
- When you need a fact that might be in dormant/archived memory → `memory_query`

Periodically (every ~10 turns or when relevant):
- If you notice the memory index growing large → suggest `memory_compact`
- If a TTL-expiring fact is spotted → prompt user to review it
```

### 15.3 各工具 Description 的行为引导汇总

| 工具 | 关键引导点 |
|------|----------|
| `remember` | WHEN（什么信号触发）+ HOW（字段选择规则）+ BEFORE（查重） |
| `forget` | 只在事实应该完全消失时用；更新优先用 remember 同名覆盖 |
| `memory_query` | 搜索前先看 MEMORY.md 索引；dormant 命中后建议用 memory_recall 唤醒 |
| `memory_recall` | 明确说明：将 dormant 拉回 active，适合「用户又提到了沉寂已久的话题」 |
| `memory_compact` | 建议在 hot_limit 的 80% 时主动调用；说明合并是可逆的（supersede 链保留历史） |
| `memory_profile` | 适合用户问「你了解我多少」或「回顾一下我的偏好」时调用 |
| `memory_status` | 适合会话开始或用户问「记忆健康吗」时调用 |

---

## 16. 会话生命周期设计

> 描述一次完整会话中，记忆系统在各个时间点的行为。

### 16.1 会话启动（Session Start）

```
启动时自动执行（代码层，无需模型干预）：
  1. Load() 发现并合并 hiq.md / AGENTS.md 层级文档
  2. store.Index() 读取 MEMORY.md，过滤 status=active
  3. Compose() 将文档 + 索引 + 当前日期注入系统提示词前缀（字节级稳定）
  4. ExpireTTL()：扫描 TTL 到期事实，自动归档（配置 ttl_check_on_start=true）
  5. Decay()：检查长期未访问事实，降级为 dormant

模型在首轮（如适用）主动执行：
  6. 读取系统提示词中的 memory_status 建议区（若有）
  7. 若有 TTL 即将到期的事实，在回复中提醒用户
  8. 若有 memory_status 提示的异常，择机告知用户
```

### 16.2 对话中（During Session）

```
触发 remember 的信号（模型监听）：
  - 用户陈述个人事实 → 判断 category + importance → 调用 remember
  - 用户纠正模型行为 → type=feedback → 调用 remember
  - 用户设定目标 → 判断是否有 TTL → 调用 remember
  - 用户确认/否认某个已有记忆 → 更新该记忆（同名 remember）

触发 memory_query 的信号：
  - 用户提到一个可能有相关记忆但索引里没有的话题
  - 用户问「你还记得...」、「之前说过...」

触发 memory_recall 的信号：
  - memory_query 返回了 dormant 事实且当前话题高度相关
  - 用户明确说「我又在... 了」（之前的 dormant 事实重新有效）

Get() 调用时自动更新：
  - LastAccessedAt = now
  - AccessCount += 1
  （这是 Warm 层事实不被误衰减的关键：访问即续命）
```

### 16.3 会话结束（Session End）

```
无显式「会话结束」事件，但 controller 在下列情况可触发清理：
  - 用户主动 /exit
  - 会话超时（headless 模式）

末轮可选行为（模型）：
  - 若本轮 remember 了多个事实，简要确认：「已记录 N 条新记忆」
  - 若 memory_status 建议压缩，提示用户下次可以运行 memory_compact
```

### 16.4 跨会话一致性保证

```
问题：会话 A 和会话 B 同时运行（桌面端多标签），两者都调用 remember 写同一条记忆。

当前缓解：
  indexMu（per-directory mutex）已序列化 MEMORY.md 的读-改-写（store.go L23-40）
  这保证了索引行的写入是原子的。

未解决的竞态：
  两个会话都读到 status=active 的旧记忆，同时调用 Save() 写新版本。
  → 后写的会覆盖先写的 .archive/ 条目（archive 文件名含时间戳，不会丢失）
  → 但两个会话都认为自己的 Save 成功了，SupersededBy 链可能有错

缓解方案（Phase 2 实施时已纳入）：
  Save() 内部的 supersede→archive→write 事务持有 indexMu（见 §7 Phase 2 代码）
```

---

## 17. 端到端集成场景

> 验证整个系统工作正确的关键场景，作为集成测试基础。

### 场景 A：居住地更新（时间事实替代）

```
[2026-03-10] 对话 1：
  用户："我3月初从北京搬家了，现在在上海。"
  模型：调用 remember(name="user-location", type="user", category="temporal",
                        valid_from="2026-03-01", body="用户自3月起居住上海",
                        importance="medium")
  系统：.archive/ 无旧文件，直接写入 user-location.md（status=active）

[2026-06-01] 对话 N：
  用户："我又搬回北京了。"
  模型：调用 remember(name="user-location", valid_from="2026-06-01", body="用户自6月起居住北京")
  系统：Phase 2 逻辑 — 旧 user-location.md（上海）移入 .archive/
        旧文件 valid_to 自动设为 "2026-05-31"，status=superseded
        新文件 user-location.md 写入（北京），status=active，supersedes=user-location

[回溯查询]：
  用户："我3月住哪里？"
  模型：memory_query(query="居住地", as_of="2026-03-15")
  系统：ListAsOf(2026-03-15) → 返回上海记录（valid_from=2026-03-01, valid_to=2026-05-31）
  模型："根据记忆，您3月住在上海。"
```

### 场景 B：事实衰减与召回

```
[2026-03-01] remember(name="user-old-hobby", body="用户喜欢弹钢琴",
                      importance="low")

[经过 15 天未访问（low → decay_days÷2=15天）]
  Decay() 检测到 → status 改为 dormant
  MEMORY.md 索引中移除该行

[2026-04-10] 用户："对了，我最近又开始弹钢琴了。"
  模型：memory_query(query="钢琴") → FTS 命中 dormant 的 user-old-hobby
  模型：调用 memory_recall(name="user-old-hobby")
  系统：status 改回 active，last_accessed_at=now，重新加入 MEMORY.md 索引
  模型："我记得您之前就喜欢弹钢琴，已经把这条记忆重新激活了。"
```

### 场景 C：画像压缩

```
[经过 3 个月积累]
  active facts: 58 条（超过 hot_limit=50）

[会话启动时 Reconcile 阶段]
  检测到 active > hot_limit × 0.9 = 45
  → 通过 QueueMemory 通知模型："⚠️ Memory hot layer at 116% capacity. Suggest running memory_compact."

[模型在首轮回复后]：
  调用 memory_compact
  步骤 A：access_count=0 且 importance≠high 的 8 条 → dormant
  步骤 B：发现 "user-comm-style" 和 "user-output-format" 同 category + tags 高度重叠
          → LLM 合并为 "user-comm-and-output-style"（新条目）
          → 旧两条 status=superseded
  结果：active 降至 48 条（< 50），返回 CompactResult
  模型告知用户："已压缩记忆：降级 8 条、合并 2 条，索引现在 48/50 条。"
```

### 场景 D：TTL 到期与续命

```
[周一] remember(name="week-goal-phase1", type="project",
                body="本周目标：完成 Phase 1 实现",
                ttl="2026-06-28",  # 本周日
                importance="medium")

[下周一启动 hiq]
  ExpireTTL() 检测到 week-goal-phase1 的 ttl="2026-06-28" <= today
  → 自动归档（status=archived，移入 .archive/）
  → 通过 QueueMemory 通知："TTL expired: week-goal-phase1 archived."

[模型首轮]：
  "上周目标「完成 Phase 1」已自动归档。
   本周是否需要设定新目标？（可以用 remember 记录，加上 ttl 避免积累）"
```

### 场景 E：首次使用——用户画像冷启动

```
[第一次运行 hiq]
  全局 memory 为空，MEMORY.md 不存在

[模型在首轮对话中]：
  检测到 MEMORY.md 为空或不存在
  → 主动说：
    "这是我们第一次对话。为了在未来的会话中更好地帮助您，
     我想记录一些基本信息。您愿意简单介绍一下自己吗？
     （比如：您的职业、当前项目、工作风格偏好）"

[用户回应后]：
  模型批量调用 remember：
    - user-role: identity, importance=high
    - user-current-project: project, importance=medium
    - user-comm-style: style, importance=medium
  调用 memory_profile 确认：
    "已建立初始用户画像，以下是当前记录的信息：..."
```

---

## 18. 开放设计问题（待决策）

> 这些问题在实施前需要明确决策，避免在 Phase 中途返工。

| # | 问题 | 选项 A | 选项 B | 建议 |
|---|------|--------|--------|------|
| Q1 | dormant 事实在 FTS 搜索时是否需要显式标注？ | 统一返回，让模型判断 | 在结果中标注"[dormant]" | **B**：模型能明确感知状态 |
| Q2 | memory_compact 合并时是否需要用户确认？ | 自动合并，通知结果 | 展示合并计划，等确认 | **B**：涉及删除操作应确认 |
| Q3 | cold_days 到期的 dormant 是否自动 archive？ | 自动归档 | 只建议，不自动 | **B**：归档是不可逆操作，建议优先 |
| Q4 | FTS 是否同时索引 .archive/ 中的 Cold 层事实？ | 是（全量） | 否（仅 active+dormant） | **A**：全量索引 + 结果携带 status 字段，模型自行判断是否引用 |
| Q5 | 冲突检测 LLM 调用是否应该可配置关闭？ | 强制开启 | 配置 conflict_detect=false | **B**：低频用户不需要 LLM 调用开销 |
| Q6 | memory_profile 输出应该存为文件还是只输出文本？ | 只输出文本 | 自动更新 `memory-profile-snapshot.md` | **A**：避免又多一种需要维护的文件 |
| Q7 | Tags 应该由模型自由填写还是有枚举约束？ | 自由文本 | 有推荐标签集 + 允许自定义 | **B**：有结构才能做过滤和合并 |
