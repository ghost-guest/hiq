# Word 表格填写能力增强方案

## 1. 背景与目标

### 当前问题

Hiq 的 `doc_write` 工具只能**创建新文档**，无法处理以下场景：
- 给定一个申报书模板，填写其中的表格
- 修改现有合同/协议中的特定字段
- 保留模板原有的格式、边框、合并单元格等

### 典型用例

```
用户：帮我填写这个申报书（附 MB_7人工智能应用实践案例征集申报书.docx）
期望：读取模板 → 识别表格结构 → 根据用户提供的信息填写 → 输出填写后的文档
```

### 设计约束

1. **不增加新工具** — 扩展 `doc_write` 的能力
2. **工具对 LLM 隐藏** — 所有 document-auto 工具通过 skill 暴露
3. **保持简洁** — LLM 只需关心"填什么"，不需要理解 OOXML 细节

---

## 2. 方案设计

### 2.1 核心思路：扩展 `doc_write` 支持两种模式

| 模式 | 触发条件 | 行为 |
|------|----------|------|
| **创建模式** | `file_path` 不存在 或 无 `source` 参数 | 当前行为：创建新文档 |
| **修改模式** | `source` 参数指向已有 `.docx` | 复制源文件 → 按 `sections` 修改 → 输出 |

### 2.2 新增参数

```go
// DocWriteInput 扩展
type DocWriteInput struct {
    // ... 现有字段 ...

    // Source 是可选的源模板文件路径。
    // 设置后，doc_write 会复制该文件并在此基础上修改，
    // 而不是从零创建新文档。保留源文件的所有格式、
    // 表格、图片、样式等。
    Source string `json:"source,omitempty"`

    // FillMode 是表格填写模式。
    // "auto"（默认）：自动识别表格并填写
    // "manual"：按精确路径填写（高级用法）
    FillMode string `json:"fill_mode,omitempty"`
}
```

### 2.3 表格填写语法

在 `sections` 中新增 `type: "table_fill"` 类型：

```json
{
  "file_path": "output.docx",
  "source": "template.docx",
  "sections": [
    {
      "type": "table_fill",
      "table": 1,
      "fills": [
        { "row": 2, "col": 1, "text": "张三" },
        { "row": 2, "col": 2, "text": "人工智能学院" },
        { "row": 3, "col": 1, "text": "项目名称" },
        { "row": 3, "col": 2, "text": "智能办公助手" }
      ]
    }
  ]
}
```

### 2.4 高级填写语法

支持更精确的定位方式：

```json
{
  "type": "table_fill",
  "table": 1,
  "fills": [
    // 方式1：按行列索引（1-based）
    { "row": 2, "col": 1, "text": "张三" },

    // 方式2：按单元格内容匹配（模糊匹配）
    { "find": "项目名称", "replace_with": "智能办公助手" },

    // 方式3：按 XPath 路径（高级）
    { "path": "/body/tbl[1]/tr[2]/tc[1]", "text": "张三" },

    // 方式4：填写并设置格式
    {
      "row": 1, "col": 1,
      "text": "标题",
      "bold": true,
      "shd": "4472C4",
      "color": "FFFFFF"
    }
  ]
}
```

---

## 3. 实现细节

### 3.1 复制源文件

```go
func copySourceFile(source, dest string) error {
    src, err := os.Open(source)
    if err != nil {
        return fmt.Errorf("打开源文件失败: %w", err)
    }
    defer src.Close()

    dst, err := os.Create(dest)
    if err != nil {
        return fmt.Errorf("创建目标文件失败: %w", err)
    }
    defer dst.Close()

    _, err = io.Copy(dst, src)
    return err
}
```

### 3.2 读取表格结构

```go
func getTableStructure(doc *goquery.Document, tableIndex int) (rows, cols int, err error) {
    // 选择第 N 个表格
    tbl := doc.Find("w\\:tbl").Eq(tableIndex)
    if tbl.Length() == 0 {
        return 0, 0, fmt.Errorf("表格 %d 不存在", tableIndex)
    }

    // 计算行数
    rows = tbl.Find("w\\:tr").Length()

    // 计算列数（取第一行的单元格数）
    cols = tbl.Find("w\\:tr").First().Find("w\\:tc").Length()

    return rows, cols, nil
}
```

### 3.3 填写单元格

```go
func fillCell(doc *goquery.Document, tableIndex, rowIndex, colIndex int, text string) error {
    // 定位单元格
    cell := doc.Find("w\\:tbl").Eq(tableIndex).
        Find("w\\:tr").Eq(rowIndex).
        Find("w\\:tc").Eq(colIndex)

    if cell.Length() == 0 {
        return fmt.Errorf("单元格 [%d,%d,%d] 不存在", tableIndex, rowIndex, colIndex)
    }

    // 清空现有内容并写入新文本
    cell.Find("w\\:p").First().Find("w\\:r").Remove()
    cell.Find("w\\:p").First().AppendHtml(
        fmt.Sprintf("<w:r><w:t>%s</w:t></w:r>", escapeXML(text)),
    )

    return nil
}
```

### 3.4 模糊匹配填写

```go
func fillByMatch(doc *goquery.Document, tableIndex int, find, replaceWith string) error {
    tbl := doc.Find("w\\:tbl").Eq(tableIndex)
    if tbl.Length() == 0 {
        return fmt.Errorf("表格 %d 不存在", tableIndex)
    }

    // 遍历所有单元格
    filled := 0
    tbl.Find("w\\:tc").Each(func(i int, cell *goquery.Selection) {
        text := cell.Text()
        if strings.Contains(text, find) {
            // 找到匹配的单元格，清空并填写
            cell.Find("w\\:p").First().Find("w\\:r").Remove()
            cell.Find("w\\:p").First().AppendHtml(
                fmt.Sprintf("<w:r><w:t>%s</w:t></w:r>", escapeXML(replaceWith)),
            )
            filled++
        }
    })

    if filled == 0 {
        return fmt.Errorf("未找到包含 %q 的单元格", find)
    }
    return nil
}
```

---

## 4. LLM 使用指南

### 4.1 基本流程

```
1. 读取模板文档结构
   ↓
2. 识别需要填写的表格和单元格
   ↓
3. 根据用户提供的信息构建 fills 数组
   ↓
4. 调用 doc_write 填写
```

### 4.2 示例对话

```
用户：帮我填写这个申报书，项目名称是"智能办公助手"，负责人是"张三"

LLM 思考：
1. 先读取模板结构，找到表格
2. 识别"项目名称"和"负责人"在哪个单元格
3. 构建 fills 数组
4. 调用 doc_write 填写

LLM 调用：
{
  "file_path": "申报书_已填写.docx",
  "source": "MB_7人工智能应用实践案例征集申报书.docx",
  "sections": [
    {
      "type": "table_fill",
      "table": 1,
      "fills": [
        { "find": "项目名称", "replace_with": "智能办公助手" },
        { "find": "负责人", "replace_with": "张三" }
      ]
    }
  ]
}
```

### 4.3 注意事项

1. **行号从 1 开始**（不是 0）
2. **模糊匹配优先** — 不需要知道精确位置
3. **保留格式** — 修改模式下，源文件的所有格式都会保留
4. **多次填写** — 一个 `sections` 数组可以包含多个 `table_fill`

---

## 5. 与 OfficeCLI 的对比

| 特性 | Hiq 方案 | OfficeCLI |
|------|--------------|-----------|
| 新增工具 | 否（扩展现有） | 是（需要集成） |
| 依赖 | 纯 Go（goquery） | 需要安装 officecli |
| 表格填写 | ✅ | ✅ |
| 格式保留 | ✅ | ✅ |
| 合并单元格 | ⚠️ 部分支持 | ✅ 完整支持 |
| 内容控件 | ❌ | ✅ |
| 学习成本 | 低 | 中 |

---

## 6. 实施计划

### Phase 1：基础填写（1-2天）

- [ ] 扩展 `DocWriteInput` 添加 `Source` 字段
- [ ] 实现文件复制逻辑
- [ ] 实现按行列索引填写
- [ ] 更新 skill 文档

### Phase 2：智能填写（2-3天）

- [ ] 实现模糊匹配填写（`find`/`replace_with`）
- [ ] 实现格式设置（bold/color/shd）
- [ ] 添加错误处理和友好提示

### Phase 3：高级功能（可选）

- [ ] 支持内容控件（sdt）填写
- [ ] 支持合并单元格感知
- [ ] 支持图片插入

---

## 7. 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| 复杂表格结构解析失败 | 无法填写 | 降级为按文本匹配 |
| 源文件格式损坏 | 输出异常 | 添加文件校验 |
| 大文件处理慢 | 用户体验差 | 异步处理 + 进度提示 |
| goquery 不支持某些 OOXML 元素 | 功能缺失 | 使用原生 XML 解析兜底 |

---

## 8. 总结

本方案通过**扩展现有 `doc_write` 工具**实现 Word 表格填写能力，无需新增工具。核心设计：

1. **`source` 参数** — 指定源模板文件
2. **`table_fill` 类型** — 声明式表格填写
3. **模糊匹配** — LLM 不需要知道精确位置
4. **格式保留** — 修改模式下保留源文件所有格式

这使得 Hiq 能够处理"给模板填写表格"这一常见办公场景，如申报书、合同、报告等。
