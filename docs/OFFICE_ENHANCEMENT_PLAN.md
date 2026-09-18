# Hiq Office 能力增强计划（修订版）

> **版本**: v2.0 | **日期**: 2026-08-04 | **状态**: 待实施
>
> **原则**: 扩展现有能力，不创建新工具，不引入外部依赖

---

## 一、核心原则

### 1.1 不做的事

- ❌ 不创建新的独立工具（如 doc_chart、doc_preview）
- ❌ 不引入 OfficeCLI 等外部依赖
- ❌ 不破坏现有架构
- ❌ 不创建重复功能

### 1.2 要做的事

- ✅ 扩展现有工具的能力
- ✅ 利用已有的 Python 脚本
- ✅ 遵循现有架构模式
- ✅ 保持零外部依赖

---

## 二、能力增强方案

### 2.1 Word 增强（扩展 doc_write）

**当前能力：**
- 段落、标题、列表、表格
- 基础样式（粗体、斜体、颜色）

**增强项：**

| 增强 | 实现方式 | 工作量 |
|------|----------|--------|
| **图片插入** | 扩展 DocSection 类型，支持 `type: "image"` | 0.5 天 |
| **目录生成** | 扩展 DocSection 类型，支持 `type: "toc"` | 0.5 天 |
| **页眉/页脚** | 扩展 DocInput 结构，支持 header/footer 字段 | 0.5 天 |
| **超链接** | 扩展文本样式，支持 `link` 属性 | 0.5 天 |

**实现方式：**
```go
// 扩展 DocSection 结构
type DocSection struct {
    Type     string      `json:"type"`     // heading|paragraph|list|table|image|toc
    Text     string      `json:"text"`
    Level    int         `json:"level"`
    // ... 现有字段 ...
    
    // 新增字段
    ImagePath string    `json:"image_path,omitempty"`  // 图片路径
    ImageAlt  string    `json:"image_alt,omitempty"`   // 图片描述
    TOCLevel  int       `json:"toc_level,omitempty"`   // 目录深度
    LinkURL   string    `json:"link_url,omitempty"`    // 超链接 URL
}
```

### 2.2 Excel 增强（扩展 doc_write）

**当前能力：**
- 单元格读写
- 基础公式
- 多 Sheet 支持

**增强项：**

| 增强 | 实现方式 | 工作量 |
|------|----------|--------|
| **条件格式** | 扩展 XLSXWorkbook 结构 | 1 天 |
| **排序** | 扩展 XLSXWorkbook 结构 | 0.5 天 |
| **自动筛选** | 扩展 XLSXWorkbook 结构 | 0.5 天 |
| **图表** | 扩展 XLSXWorkbook 结构 | 1 天 |

**实现方式：**
```go
// 扩展 XLSXWorkbook 结构
type XLSXWorkbook struct {
    Path   string       `json:"path"`
    Sheets []XLSXSheet  `json:"sheets"`
    
    // 新增字段
    Charts []XLSXChart  `json:"charts,omitempty"`  // 图表
}

type XLSXChart struct {
    Type      string   `json:"type"`      // bar|line|pie|scatter
    Title     string   `json:"title"`
    DataRange string   `json:"data_range"` // e.g., "Sheet1!A1:B10"
    Position  string   `json:"position"`   // e.g., "D2"
}
```

### 2.3 PPT 增强（扩展 PPT-auto 脚本）

**当前能力：**
- SVG 自由设计
- 模板填充
- 演讲者备注

**增强项：**

| 增强 | 实现方式 | 工作量 |
|------|----------|--------|
| **图表** | 扩展 `pptx_builder.py` | 1 天 |
| **动画** | 扩展 `animation_config.py` | 0.5 天 |
| **过渡效果** | 扩展 `pptx_builder.py` | 0.5 天 |
| **图片增强** | 扩展 `pptx_media.py` | 0.5 天 |

**实现方式：**
```python
# 扩展 pptx_builder.py
def add_chart(slide, chart_type, data, position):
    """添加图表到幻灯片"""
    # 使用 python-pptx 的图表 API
    
def add_animation(shape, animation_type, duration):
    """为形状添加动画"""
    # 使用 python-pptx 的动画 API
    
def add_transition(slide, transition_type, duration):
    """添加幻灯片过渡效果"""
    # 使用 python-pptx 的过渡 API
```

### 2.4 文档预览（扩展 doc_read）

**当前能力：**
- 读取文档内容
- 返回结构化文本

**增强项：**

| 增强 | 实现方式 | 工作量 |
|------|----------|--------|
| **大纲视图** | 扩展 doc_read，支持 format 参数 | 0.5 天 |
| **统计信息** | 扩展 doc_read，返回文档统计 | 0.5 天 |

**实现方式：**
```go
// 扩展 doc_read 的 Schema
func (docRead) Schema() json.RawMessage {
    return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Absolute path to the document"},
  "format":{"type":"string","enum":["text","outline","stats"],"description":"Output format (default: text)"}
},
"required":["path"]
}`)
}
```

---

## 三、实施计划

### 3.1 Phase 1: Word 增强（2 天）

| 任务 | 工作量 | 优先级 |
|------|--------|--------|
| 图片插入 | 0.5 天 | P0 |
| 目录生成 | 0.5 天 | P0 |
| 页眉/页脚 | 0.5 天 | P1 |
| 超链接 | 0.5 天 | P1 |

### 3.2 Phase 2: Excel 增强（2.5 天）

| 任务 | 工作量 | 优先级 |
|------|--------|--------|
| 图表 | 1 天 | P0 |
| 条件格式 | 1 天 | P1 |
| 排序 | 0.5 天 | P1 |
| 自动筛选 | 0.5 天 | P1 |

### 3.3 Phase 3: PPT 增强（2.5 天）

| 任务 | 工作量 | 优先级 |
|------|--------|--------|
| 图表 | 1 天 | P0 |
| 动画 | 0.5 天 | P1 |
| 过渡效果 | 0.5 天 | P1 |
| 图片增强 | 0.5 天 | P1 |

### 3.4 Phase 4: 文档预览（1 天）

| 任务 | 工作量 | 优先级 |
|------|--------|--------|
| 大纲视图 | 0.5 天 | P1 |
| 统计信息 | 0.5 天 | P2 |

**总计：8 天**

---

## 四、技术细节

### 4.1 Word 图片插入

```go
// internal/tool/builtin/docx_write.go

func writeDOCXImage(doc *docx.Document, section DocSection) error {
    // 读取图片文件
    imgData, err := os.ReadFile(section.ImagePath)
    if err != nil {
        return err
    }
    
    // 添加图片到文档
    img, err := doc.AddImage(imgData)
    if err != nil {
        return err
    }
    
    // 设置图片属性
    img.SetAltText(section.ImageAlt)
    
    return nil
}
```

### 4.2 Excel 图表

```go
// internal/tool/builtin/xlsx_write.go

func addChart(f *excelize.File, sheet string, chart XLSXChart) error {
    // 解析数据范围
    // 添加图表
    return f.AddChart(sheet, chart.Position, &excelize.Chart{
        Type: getChartType(chart.Type),
        Series: []excelize.ChartSeries{
            {
                Name:       chart.Title,
                Categories: chart.DataRange,
                Values:     chart.DataRange,
            },
        },
    })
}
```

### 4.3 PPT 图表

```python
# internal/assets/pptauto/scripts/svg_to_pptx/pptx_builder.py

def add_chart(slide, chart_type, data, position):
    """添加图表到幻灯片"""
    from pptx.chart.data import CategoryChartData
    
    chart_data = CategoryChartData()
    chart_data.categories = data['labels']
    chart_data.add_series('Series 1', data['values'])
    
    chart = slide.shapes.add_chart(
        get_chart_type(chart_type),
        position['x'], position['y'],
        position['width'], position['height'],
        chart_data
    ).chart
    
    return chart
```

---

## 五、验收标准

### 5.1 Word 增强

- [ ] 支持插入 PNG/JPG/GIF/SVG 图片
- [ ] 支持自动生成目录
- [ ] 支持页眉/页脚设置
- [ ] 支持超链接插入

### 5.2 Excel 增强

- [ ] 支持柱状图、折线图、饼图、散点图
- [ ] 支持条件格式（颜色规则、数据条）
- [ ] 支持多键排序
- [ ] 支持自动筛选

### 5.3 PPT 增强

- [ ] 支持柱状图、折线图、饼图、散点图
- [ ] 支持入场/退场/强调动画
- [ ] 支持淡入淡出、推入、擦除过渡
- [ ] 支持图片亮度/对比度/发光/阴影

### 5.4 文档预览

- [ ] 支持大纲视图（仅显示标题）
- [ ] 支持统计信息（字数、页数、行数）

---

## 六、总结

### 关键改进

1. **扩展而非新建** — 扩展现有工具，不创建新工具
2. **利用现有架构** — 使用已有的 Python 脚本和 Go 库
3. **零外部依赖** — 不引入 OfficeCLI 等外部工具
4. **渐进式增强** — 每个 Phase 独立可交付

### Hiq Office 能力提升

| 能力 | 优化前 | 优化后 |
|------|--------|--------|
| **Word 图片** | ⭐ | ⭐⭐⭐⭐ |
| **Word 目录** | ⭐ | ⭐⭐⭐⭐ |
| **Excel 图表** | ⭐ | ⭐⭐⭐⭐ |
| **Excel 条件格式** | ⭐ | ⭐⭐⭐⭐ |
| **PPT 图表** | ⭐ | ⭐⭐⭐⭐ |
| **PPT 动画** | ⭐⭐⭐ | ⭐⭐⭐⭐ |
| **文档预览** | ⭐⭐ | ⭐⭐⭐⭐ |

**总计：8 天，完全内化，零外部依赖！**
