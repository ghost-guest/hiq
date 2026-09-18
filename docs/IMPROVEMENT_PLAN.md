# Hiq 全面提升计划

> **版本**: v1.0 | **日期**: 2026-08-04 | **状态**: 待实施
>
> **目标**: 将 Hiq 打造为最强的通用多厂商 AI 编程助手 + 办公自动化平台

---

## 一、提升总览

### 1.1 提升维度

| 维度 | 当前状态 | 目标状态 | 工作量 |
|------|----------|----------|--------|
| **Word/Excel** | ❌ 无能力 | ✅ 完整编辑（集成 OfficeCLI） | 3 天 |
| **PPT 增强** | ⭐⭐⭐ 基础 | ⭐⭐⭐⭐⭐ 完整 | 5 天 |
| **Skill 市场** | ❌ 无市场 | ✅ 完整生态 | 5 天 |
| **错误恢复** | ⭐⭐⭐ 简单重试 | ⭐⭐⭐⭐⭐ 智能恢复 | 4 天 |
| **编辑前验证** | ❌ 无验证 | ✅ 语法/AST/类型检查 | 3 天 |
| **向量搜索** | ❌ 无 | ✅ 语义检索 | 4 天 |
| **Hook 系统** | ⭐⭐⭐ 基础 | ⭐⭐⭐⭐ 优化 | 3 天 |
| **模型管理** | ⭐⭐⭐⭐ 静态配置 | ⭐⭐⭐⭐⭐ 动态获取 | 3 天 |

**总计：30 天（约 6 周）**

---

## 二、详细计划

### 2.1 Phase 1: Office 能力提升（8 天）

#### 2.1.1 集成 OfficeCLI（3 天）

**目标：** 快速获得 Word/Excel 编辑能力

**实施方案：**

```toml
# hiq.toml
[tools.officecli]
enabled = true
# 自动检测路径，或手动指定
# path = "/usr/local/bin/officecli"
```

**工具映射：**

| Hiq 工具 | OfficeCLI 命令 | 说明 |
|---------------|----------------|------|
| `word_create` | `officecli create report.docx` | 创建 Word 文档 |
| `word_read` | `officecli view report.docx text` | 读取 Word 内容 |
| `word_edit` | `officecli set report.docx /body/p[1] --prop text="..."` | 修改 Word 内容 |
| `word_preview` | `officecli view report.docx html` | HTML 预览 |
| `excel_create` | `officecli create data.xlsx` | 创建 Excel 表格 |
| `excel_read` | `officecli view data.xlsx text` | 读取 Excel 数据 |
| `excel_edit` | `officecli set data.xlsx /Sheet1/A1 --prop value="..."` | 修改 Excel 数据 |
| `excel_preview` | `officecli view data.xlsx html` | HTML 预览 |
| `ppt_preview` | `officecli view slides.pptx html` | PPT 预览 |
| `ppt_validate` | `officecli validate slides.pptx` | PPT 验证 |

**文件结构：**
```
internal/tool/builtin/
├── word.go          # Word 工具（调用 OfficeCLI）
├── excel.go         # Excel 工具（调用 OfficeCLI）
└── office_preview.go # Office 预览工具
```

**验收标准：**
- [ ] OfficeCLI 自动检测和安装
- [ ] Word 创建/读取/编辑/预览
- [ ] Excel 创建/读取/编辑/预览
- [ ] PPT 预览和验证
- [ ] 错误处理和降级

#### 2.1.2 PPT 增强（5 天）

**目标：** 增强 PPT 功能完整性

**增强项：**

| 增强项 | 说明 | 工作量 | 优先级 |
|--------|------|--------|--------|
| **图表支持** | 柱状图、折线图、饼图、散点图 | 2 天 | P1 |
| **动画支持** | 入场/退场/强调动画 | 1 天 | P2 |
| **过渡效果** | 淡入淡出、推入、擦除等 | 0.5 天 | P2 |
| **实时预览** | watch 模式，修改即时刷新 | 1 天 | P1 |
| **模板库扩展** | 更多专业模板 | 0.5 天 | P3 |

**图表支持实现：**

```python
# scripts/chart_generator.py

def generate_chart(chart_type, data, options=None):
    """生成图表并插入 PPT"""
    if chart_type == "bar":
        return generate_bar_chart(data, options)
    elif chart_type == "line":
        return generate_line_chart(data, options)
    elif chart_type == "pie":
        return generate_pie_chart(data, options)
    elif chart_type == "scatter":
        return generate_scatter_chart(data, options)
```

**动画支持实现：**

```python
# scripts/animation_generator.py

def add_animation(slide, shape, animation_type, duration=0.5):
    """为形状添加动画"""
    if animation_type == "fade_in":
        return add_fade_in(slide, shape, duration)
    elif animation_type == "fly_in":
        return add_fly_in(slide, shape, duration)
    elif animation_type == "zoom_in":
        return add_zoom_in(slide, shape, duration)
```

**验收标准：**
- [ ] 支持 4 种图表类型
- [ ] 支持 5 种动画效果
- [ ] 支持 4 种过渡效果
- [ ] 实时预览可用
- [ ] 模板库扩展到 10+

---

### 2.2 Phase 2: 生态建设（10 天）

#### 2.2.1 Skill 市场（5 天）

**目标：** 建立 Skill 发现、分享、管理生态

**架构设计：**

```
┌─────────────────────────────────────────────────────────┐
│                    Skill 市场                            │
├─────────────────────────────────────────────────────────┤
│  注册表 API (https://skills.hiq.dev/api)           │
│  ├─ GET /skills — 列出所有 Skill                        │
│  ├─ GET /skills/{id} — 获取 Skill 详情                  │
│  ├─ POST /skills — 发布新 Skill                         │
│  └─ PUT /skills/{id} — 更新 Skill                      │
├─────────────────────────────────────────────────────────┤
│  Skill 包格式                                           │
│  ├─ SKILL.md — Skill 定义                               │
│  ├─ manifest.json — 元数据                              │
│  └─ scripts/ — 脚本文件                                 │
├─────────────────────────────────────────────────────────┤
│  安全机制                                               │
│  ├─ 代码签名                                            │
│  ├─ 沙箱执行                                            │
│  └─ 权限声明                                            │
└─────────────────────────────────────────────────────────┘
```

**manifest.json 格式：**
```json
{
  "name": "code-review",
  "version": "1.0.0",
  "description": "Automated code review",
  "author": "hiq-team",
  "license": "MIT",
  "permissions": {
    "tools": ["read_file", "grep", "bash"],
    "network": false
  }
}
```

**CLI 命令：**
```bash
hiq skill search "code review"
hiq skill install code-review
hiq skill publish ./my-skill
hiq skill list
hiq skill update code-review
```

**验收标准：**
- [ ] 注册表 API 可用
- [ ] CLI 命令可用
- [ ] 签名验证通过
- [ ] 沙箱执行安全

#### 2.2.2 智能错误恢复（4 天）

**目标：** 实现循环检测和智能重试

**6 层重试架构：**

| 层级 | 机制 | 超时 | 重试次数 |
|------|------|------|----------|
| L1 | AI SDK 内部重试 | 5s | 2 |
| L2 | LLM 持久重试 | 5min | 10 |
| L3 | 会话级重试策略 | 30s | 5 |
| L4 | HTTP 客户端重试 | 200ms | 2 |
| L5 | 检查点写入重试 | 10s | 3 |
| L6 | 预填充拒绝重试 | 即时 | 1 |

**循环检测：**

| 检测器 | 触发条件 | 恢复动作 |
|--------|----------|----------|
| N-gram 重复 | 连续重复块 | 注入恢复提示 |
| 文本循环 | 3 次相同输出 | 注入强提示 |
| Try-best 循环 | 4+ 次相同操作 | 暂停 + 提示 |
| Doom 循环 | 3 次相同工具调用 | 权限询问 |

**文件结构：**
```
internal/recovery/
├── retry.go          # 重试策略
├── detector.go       # 循环检测器
├── classifier.go     # 错误分类器
└── strategies.go     # 恢复策略
```

**验收标准：**
- [ ] 6 层重试机制可用
- [ ] 4 种循环检测器可用
- [ ] 错误分类准确率 > 90%
- [ ] 恢复成功率 > 95%

#### 2.2.3 编辑前验证（1 天）

**目标：** 在写入前验证语法和类型

**验证层级：**

| 层级 | 验证内容 | 语言支持 |
|------|----------|----------|
| L1 | 语法检查 | Go, TypeScript, Python, JSON, YAML |
| L2 | AST 验证 | Go, TypeScript |
| L3 | 类型检查 | Go (go vet), TypeScript (tsc) |

**验证流程：**
```
编辑请求
  ↓
L1: 语法检查（<100ms）
  ↓ 通过
L2: AST 验证（<500ms）
  ↓ 通过
L3: 类型检查（<2s）
  ↓ 通过
写入文件
```

**验收标准：**
- [ ] 语法检查支持 5+ 语言
- [ ] AST 验证支持 Go/TypeScript
- [ ] 验证失败提供清晰错误信息
- [ ] 验证不影响编辑性能

---

### 2.3 Phase 3: 智能增强（7 天）

#### 2.3.1 向量搜索（4 天）

**目标：** 提升 RAG 检索精度

**混合检索架构：**
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

**验收标准：**
- [ ] 向量搜索支持 100K+ 文档
- [ ] 查询延迟 < 200ms
- [ ] 检索精度比纯 FTS5 提升 30%+

#### 2.3.2 Hook 系统优化（3 天）

**目标：** 提升现有 Hook 系统效果

**优化方向：**
1. **性能优化** — 减少 Hook 执行延迟
2. **错误处理** — Hook 失败时的降级策略
3. **配置简化** — 更易用的配置格式
4. **文档完善** — 更好的使用文档

**验收标准：**
- [ ] Hook 执行延迟 < 10ms
- [ ] Hook 失败有降级处理
- [ ] 配置格式简洁易用
- [ ] 文档完善

---

## 三、技术决策

| 决策项 | 方案 | 理由 |
|--------|------|------|
| **Word/Excel** | 集成 OfficeCLI | 快速获得完整能力，无需重复开发 |
| **PPT** | 增强原生能力 | 保持设计质量优势 |
| **Skill 市场** | 自建注册表 | 可控，中国厂商全 |
| **错误恢复** | 6 层重试 + 循环检测 | 参考 MiMo-Code 最佳实践 |
| **编辑验证** | 分层验证 | 渐进式，不影响性能 |
| **向量搜索** | SQLite + 向量扩展 | 无外部依赖，部署简单 |
| **Hook 优化** | 性能优化 + 配置简化 | 提升现有系统效果 |

---

## 四、实施计划

### 4.1 Phase 1: Office 能力提升（第 1-2 周）

| 任务 | 工作量 | 优先级 | 依赖 |
|------|--------|--------|------|
| 集成 OfficeCLI | 3 天 | P0 | 无 |
| PPT 图表支持 | 2 天 | P1 | 无 |
| PPT 动画支持 | 1 天 | P2 | 无 |
| PPT 过渡效果 | 0.5 天 | P2 | 无 |
| PPT 实时预览 | 1 天 | P1 | 无 |
| PPT 模板库扩展 | 0.5 天 | P3 | 无 |

### 4.2 Phase 2: 生态建设（第 3-4 周）

| 任务 | 工作量 | 优先级 | 依赖 |
|------|--------|--------|------|
| Skill 市场基础 | 5 天 | P0 | 无 |
| 智能错误恢复 | 4 天 | P1 | 无 |
| 编辑前验证 | 1 天 | P1 | 无 |

### 4.3 Phase 3: 智能增强（第 5-6 周）

| 任务 | 工作量 | 优先级 | 依赖 |
|------|--------|--------|------|
| 向量搜索 | 4 天 | P2 | 无 |
| Hook 系统优化 | 3 天 | P2 | 无 |

**总计：30 天（约 6 周）**

---

## 五、验收标准

### 5.1 功能验收

- [ ] Word 创建/读取/编辑/预览
- [ ] Excel 创建/读取/编辑/预览
- [ ] PPT 图表支持（4 种类型）
- [ ] PPT 动画支持（5 种效果）
- [ ] Skill 市场可用（search/install/publish）
- [ ] 6 层重试机制可用
- [ ] 4 种循环检测器可用
- [ ] 语法检查支持 5+ 语言
- [ ] 向量搜索支持 100K+ 文档

### 5.2 性能验收

- [ ] Word/Excel 操作延迟 < 500ms
- [ ] PPT 图表生成延迟 < 2s
- [ ] 向量查询延迟 < 200ms
- [ ] 编辑验证延迟 < 100ms
- [ ] 错误恢复成功率 > 95%
- [ ] Hook 执行延迟 < 10ms

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
| **Hook 系统复杂度** | 用户学习成本高 | 内置常用 Hook |

---

## 七、后续演进

### 7.1 短期（v2.1）

- **Skill 市场增强** — 评分、评论、自动更新
- **PPT 增强** — 3D 模型、视频嵌入
- **Word/Excel 增强** — 更多格式支持

### 7.2 中期（v2.2）

- **智能路由** — 根据任务类型自动选择模型
- **成本优化** — 自动选择性价比最高的模型
- **性能监控** — 监控各 provider 的响应时间和成功率

### 7.3 长期（v3.0）

- **多模态支持** — 图片、音频、视频理解
- **自主学习** — 从用户反馈中学习优化
- **分布式执行** — 跨设备任务编排

---

## 八、总结

### Hiq 提升后的定位

| 能力 | 评级 | 说明 |
|------|------|------|
| **AI 编程助手** | ⭐⭐⭐⭐⭐ | 多厂商支持 + 智能错误恢复 |
| **PPT 设计** | ⭐⭐⭐⭐⭐ | SVG 自由设计 + VLM + 图表动画 |
| **Word/Excel** | ⭐⭐⭐⭐ | 通过 OfficeCLI 获得完整能力 |
| **邮件/日历** | ⭐⭐⭐⭐⭐ | 独特优势 |
| **Skill 生态** | ⭐⭐⭐⭐ | 市场 + 版本管理 + 安全 |
| **RAG 检索** | ⭐⭐⭐⭐ | 向量搜索 + 全文搜索 |

### 核心竞争力

1. **最强 PPT 设计** — SVG 自由设计 + VLM 集成
2. **最全 Office 能力** — Word/Excel/PPT 完整支持
3. **最智能错误恢复** — 6 层重试 + 循环检测
4. **最丰富 Skill 生态** — 市场 + 版本管理 + 安全
5. **最精准 RAG 检索** — 向量搜索 + 全文搜索

---

**Hiq — 最强的通用多厂商 AI 编程助手 + 办公自动化平台！**
