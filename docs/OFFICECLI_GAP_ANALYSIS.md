# Hiq vs OfficeCLI — Office 能力差距分析

> 基于 5 个并行调研任务的汇总结果，排除 PPT 相关能力。

---

## 一、当前 Hiq Office 能力总览

| 工具 | 格式 | 创建 | 读取 | 修改 | 样式 | 多Sheet | 图表 | 图片 |
|------|------|------|------|------|------|---------|------|------|
| `doc_read` | .docx | - | 纯文本 | - | ❌ | N/A | N/A | ❌ |
| `doc_write` | .docx | ✅ | - | 追加 | 基础 | N/A | N/A | PNG/JPG/GIF |
| `xlsx_read` | .xlsx | - | 单元格值 | - | ❌ | ✅ | ❌ | ❌ |
| `xlsx_write` | .xlsx | ✅ | - | ❌ | ✅ | ✅ | 4种 | ❌ |
| `csv_read/write` | .csv | ✅ | ✅ | 追加 | ❌ | ❌ | ❌ | ❌ |
| `doc_convert` | md/html | ✅ | 源读取 | - | 最小 | ❌ | ❌ | ❌ |
| `mindmap_create` | .md/.html | ✅ | - | - | ❌ | ❌ | ❌ | ❌ |

---

## 二、缺失能力清单（按优先级排序）

### 🔴 P0 — 核心办公场景必备（不实现则无法完成常见任务）

| # | 能力 | 说明 | OfficeCLI 实现 |
|---|------|------|----------------|
| 1 | **Word 表格填写** | 读取现有模板 → 定位表格 → 填写单元格 → 保留格式 | `set '/body/tbl[1]/tr[2]/tc[1]' --prop text="内容"` |
| 2 | **Word 读取增强** | 读取表格结构、标题层级、页数、字数、样式 | `view outline/stats/issues/text/annotated` |
| 3 | **Word 页眉页脚** | 添加页码、页眉文字、"第X页共Y页" | `add / --type footer --prop text="Page "` + field |
| 4 | **Word 查找替换** | 批量替换文本（如合同中的甲方/乙方名称） | `set / --find draft --replace final` |
| 5 | **Excel 读取增强** | 读取公式、样式、合并单元格、数据验证 | `get /Sheet1/A1 --json` 返回 formula/type/style |

### 🟠 P1 — 重要办公场景（显著提升用户体验）

| # | 能力 | 说明 | OfficeCLI 实现 |
|---|------|------|----------------|
| 6 | **Word 修改现有文档** | 不只是追加，能修改/删除/移动特定段落 | `set/remove/move/swap` |
| 7 | **Word 内容控件(SDT)** | 表单字段：文本框、下拉选择、日期、复选框 | `add --type sdt --prop type=dropdown` |
| 8 | **Word 样式系统** | 创建/修改/应用段落样式和字符样式 | `add /styles --type style` |
| 9 | **Word 分节符** | 页面方向、分栏、页边距按节设置 | `add --type section --prop columns=2` |
| 10 | **Word 字段(Fields)** | 日期、页码、交叉引用、条件判断等28种字段 | `add --type field --prop fieldType=date` |
| 11 | **Word 目录(TOC)** | 自动生成目录，按标题层级索引 | `add --type toc --prop levels=1-3` |
| 12 | **Excel 数据验证** | 下拉列表、数值范围限制、输入提示 | `add --type validation --prop type=list` |
| 13 | **Excel 透视表** | 多维数据分析、汇总、分组 | `add --type pivottable` |
| 14 | **Excel 排序筛选** | 按列排序、自动筛选 | `set --prop sort="C desc"` / `autoFilter` |
| 15 | **Excel 条件格式增强** | 数据条、色阶、图标集、Top N、公式规则 | `add --type conditionalformatting` |

### 🟡 P2 — 专业场景（高级用户/特定行业需要）

| # | 能力 | 说明 | OfficeCLI 实现 |
|---|------|------|----------------|
| 16 | **Word 修订追踪** | 跟踪更改、接受/拒绝修订 | `set --prop revision.type=ins --prop revision.author=Alice` |
| 17 | **Word 脚注尾注** | 学术论文、法律文档常用 | `add /body/p[3] --type footnote` |
| 18 | **Word 书签+交叉引用** | 文档内部跳转和引用 | `add --type bookmark` + `fieldType=ref` |
| 19 | **Word 公式(Equation)** | LaTeX 转 Word 公式 | `add --type equation --prop formula=...` |
| 20 | **Word 文本框** | 浮动文本框、图文混排 | `add --type textbox` |
| 21 | **Word 图表** | 在 Word 中嵌入图表 | `add --type chart --prop chartType=column` |
| 22 | **Word 水印** | 背景文字/图片水印 | `behindText=true` |
| 23 | **Word 图表(Mermaid)** | 流程图、时序图转 Word 形状 | `add --type diagram --prop mermaid=...` |
| 24 | **Excel 图表增强** | 30+图表类型、组合图、瀑布图、直方图 | column/bar/line/pie/scatter/radar/combo/waterfall/funnel... |
| 25 | **Excel 迷你图** | 单元格内小型图表 | `add --type sparkline` |
| 26 | **Excel 切片器** | 透视表交互筛选 | `add --type slicer` |
| 27 | **Excel 形状** | 绘图对象、流程图元素 | `add --type shape` |
| 28 | **Excel 图片** | 嵌入图片(含SVG) | `add --type image` |
| 29 | **Excel 工作簿保护** | 结构保护、密码保护 | `workbook.lockStructure=true` |
| 30 | **Excel 打印设置** | 打印区域、标题行重复、页边距 | `printArea`, `printTitleRows` |

### 🟢 P3 — 增强功能（锦上添花）

| # | 能力 | 说明 | OfficeCLI 实现 |
|---|------|------|----------------|
| 31 | **文档对比** | 两个文档的结构/文本差异 | `dump` → diff |
| 32 | **文档校验** | OpenXML schema 验证 | `validate` |
| 33 | **HTML/PDF 导出** | 文档转 HTML 截图或 PDF | `view html/screenshot/pdf` |
| 34 | **批量操作** | 原子性多操作事务 | `batch` |
| 35 | **Raw XML** | 底层 XML 直接操作 | `raw` / `raw-set` |
| 36 | **Excel 命名范围** | 公式中使用有意义的名称 | `add --type namedrange` |
| 37 | **Excel CSV 导入** | 直接从 CSV 创建工作表 | `import` |
| 38 | **Excel OLE 嵌入** | 嵌入外部对象 | `add --type ole` |
| 39 | **Word 修订追踪增强** | 按作者/类型筛选接受/拒绝 | `set '/revision[@author=Alice]' --prop revision.action=accept` |
| 40 | **Excel 高级图表** | 瀑布图、漏斗图、帕累托图、树状图、旭日图 | waterfall/funnel/pareto/treemap/sunburst |

---

## 三、实施建议

### 第一阶段：核心补齐（P0，约 1-2 周）

```
1. Word 表格填写     — 扩展 doc_write 支持 source + table_fill
2. Word 读取增强     — 扩展 doc_read 返回表格结构/标题/统计
3. Word 页眉页脚     — doc_write 新增 header/footer section 类型
4. Word 查找替换     — doc_write 新增 find_replace section 类型
5. Excel 读取增强    — 扩展 xlsx_read 返回公式/样式元数据
```

### 第二阶段：能力扩展（P1，约 2-3 周）

```
6.  Word 修改现有文档 — set/remove/move/swap 操作
7.  Word 内容控件    — SDT 表单字段
8.  Word 样式系统    — 创建/修改/应用样式
9.  Word 分节符      — 页面布局按节控制
10. Word 字段        — 28种字段类型
11. Word 目录        — 自动生成 TOC
12. Excel 数据验证   — 下拉列表/数值限制
13. Excel 透视表     — 多维数据分析
14. Excel 排序筛选   — 排序/自动筛选
15. Excel 条件格式增强 — 数据条/色阶/图标集
```

### 第三阶段：专业能力（P2，约 3-4 周）

按需实施，根据用户反馈优先级调整。

---

## 四、架构决策建议

### 方案 A：纯 Go 内化（当前路线）
- **优点**：无外部依赖，跨平台，打包简单
- **缺点**：需要自己实现大量 OOXML 细节，工作量大
- **适合**：P0 的 5 项核心能力

### 方案 B：集成 OfficeCLI 作为后端
- **优点**：500+ 能力开箱即用，经过充分测试
- **缺点**：需要安装 officecli 二进制，增加分发复杂度
- **适合**：P1/P2 的专业能力

### 方案 C：混合方案（推荐）
- **P0** 用纯 Go 内化（表格填写、读取增强、页眉页脚、查找替换、Excel 读取）
- **P1/P2** 通过 `doc_exec` 工具调用 officecli（可选依赖）
- **用户感知**：基础能力零依赖，高级能力安装 officecli 后解锁

---

## 五、能力对比总表

| 类别 | OfficeCLI 能力数 | Hiq 现有 | 缺失 | 覆盖率 |
|------|-----------------|--------------|------|--------|
| Word 文本操作 | ~30 | 5 | 25 | 17% |
| Word 表格 | ~25 | 3 | 22 | 12% |
| Word 样式/格式 | ~40 | 6 | 34 | 15% |
| Word 结构元素 | ~30 (字段/书签/目录/脚注/页眉页脚/分节) | 1 (TOC) | 29 | 3% |
| Word 表单/控件 | ~10 (SDT) | 0 | 10 | 0% |
| Word 修订/批注 | ~15 | 0 | 15 | 0% |
| Word 图片/图表/图形 | ~20 | 1 (图片) | 19 | 5% |
| Word 检查/查询 | ~15 (view/get/query/validate) | 1 (纯文本) | 14 | 7% |
| Excel 单元格 | ~20 | 4 | 16 | 20% |
| Excel 行列/Sheet | ~25 | 3 | 22 | 12% |
| Excel 图表 | ~30 | 4 | 26 | 13% |
| Excel 数据分析 | ~20 (透视表/验证/筛选/排序/条件格式) | 2 | 18 | 10% |
| Excel 高级对象 | ~15 (形状/图片/迷你图/切片器/OLE) | 0 | 15 | 0% |
| **合计** | **~305** | **~30** | **~275** | **~10%** |

---

## 六、结论

Hiq 当前 Office 能力覆盖率约 **10%**，主要集中在基础创建和简单读取。与 OfficeCLI 的 305 项能力相比，缺失约 275 项。

**最紧迫的 5 项能力**（P0）解决了 80% 的常见办公场景：
1. Word 表格填写 — 申报书、合同、报告模板
2. Word 读取增强 — 理解文档结构
3. Word 页眉页脚 — 正式文档必备
4. Word 查找替换 — 批量文本处理
5. Excel 读取增强 — 理解数据结构

建议采用**混合方案**：P0 纯 Go 内化，P1/P2 通过可选的 officecli 集成提供。
