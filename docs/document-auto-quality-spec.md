# document-auto 质量提升 Spec

> **状态**：Phase 0-6 全部已实施 ✅（v3 Phase 3 暂不做）
> **范围**：分两块——(A) 修现有 document-auto 的保底缺失与业务 bug（已完成）；(B) 新增模板填充能力 `doc_template`、doc_read/doc_write 功能增强、DocStyle/XLSXStyle 扩展（整合自 v3 方案 cosmic-coalescing-moonbeam.md）
> **审查基础**：3 轮代码审查（工具链一致性 / docxwrite 业务 bug / xlsx 与读取逻辑）+ OfficeCLI 对比 + 技术细节验证

---

## 一、目标与整体路线

这份 spec 把两件事整合成一份完整路线：

1. **修现有问题（Phase 0-3，已完成）** — 让现有 doc_read/doc_write/xlsx_*/doc_convert 达到"不损坏用户文件、不产出坏结果、不丢失信息、LLM 能发现能力"。
2. **加新功能（Phase 4-6，待实施）** — 在修好的地基上，新增 `doc_template` 模板填充工具（办公场景核心刚需）、doc_read 多 mode、DocStyle/XLSXStyle 格式扩展、单位转换。整合自 v3 方案，吸收了之前审查发现的矛盾（中文占位符正则、mergeRuns vs SplitRuns、source confine 等）。

### 路线图

```
Phase 0-3（已完成 ✅）→ 修保底 + 修 bug + 修一致性
Phase 4   （待实施）→ docxsafety.go：模板专用防护（解压炸弹/文件锁/DocError）
Phase 5   （待实施）→ doc_template 模板填充工具（核心新功能）
Phase 6   （待实施）→ doc_read 多 mode + DocStyle/XLSXStyle 扩展 + 单位转换
```

### 非目标（仍然不做）

| 不做项 | 理由 |
|---|---|
| `doc_read(mode:html)` 渲染 | 办公场景用户用 Word 自己看 |
| issue 检测 / view issues | Word 自带检查，用户人工 review |
| `{{#each}}` 块循环 | Phase 5 先做简单 find_replace/table_fill，循环按需 |
| OfficeCLI 的递归深度/正则超时/DOM上限/SSRF | 本地单用户无此威胁，Go 语言级已免疫 XXE/ReDoS |
| **v3 Phase 3 全部**（模板 TOC / 书签+交叉引用 / 样式管理 / 修订模式 Tracked Changes / 文本框 / 超链接 / 批量原子事务） | 用户明确暂不做。注意：这里说的"模板 TOC"是 doc_template 场景下在模板里生成/更新目录域，与 Phase 1.6 已完成的"doc_write 新建文档 TOC + settings.xml updateFields"不同 |

---

## 二、问题清单总览

共 **27 个修复项**，分 4 个 Phase。按"损坏风险 → 数据正确性 → 信息完整性 → 一致性"排序。

| Phase | 主题 | 项数 | 工期估计 |
|---|---|---|---|
| Phase 0 | 保底修复（防损坏/防崩溃） | 4 | 2-3 天 |
| Phase 1 | 修业务 bug（防产出坏结果） | 8 | 3-4 天 |
| Phase 2 | 修读取逻辑（防信息丢失） | 5 | 2-3 天 |
| Phase 3 | 修一致性（LLM 能发现能力） | 6 | 1 天 |

---

## Phase 0：保底修复（防损坏用户文件 / 防崩溃）

### P0.1 统一原子写入工具 `atomicWrite`

**问题**：5 处写入都是截断式覆盖：

| # | 位置 | 现状 | 后果 |
|---|---|---|---|
| D1 | `writeDOCXFull`（docxwrite.go:138） | `os.Create` 截断后流式写 zip | 失败即损坏原 docx |
| D2 | `XLSXWriteRows`（officedoc.go:85） | `excelize.SaveAs` 直接覆盖 | 失败损坏原 xlsx |
| D3 | `XLSXWriteStructured`（xlsxwrite_structured.go:155） | 同上 | 同上 |
| D4 | `writeFileEncoded`（encoding_helpers.go:23） | `os.WriteFile` 截断 | 影响所有文本写入工具 |
| D8 | `writeDOCXAppend`（docxwrite.go:358） | rename 前先 `os.Remove` | 极小窗口进程被杀丢文件 |

**修法**：新增 `internal/tool/builtin/atomic.go`，参照 OfficeCLI `AtomicPackageWriter`（AtomicPackageWriter.cs:31-82）的临时文件 + rename 模式：

```go
// atomicWrite 写入临时文件 + 原子 rename，失败时原文件保持完整。
// 调用方必须先关闭对 path 持有的任何句柄（Windows 下 rename 到被占用的
// 目标会失败 —— 见 docxwrite.go:197-198 的注释）。
func atomicWrite(path string, write func(*os.File) error) (err error) {
    if e := os.MkdirAll(filepath.Dir(path), 0o755); e != nil {
        return e
    }
    tmp, e := os.CreateTemp(filepath.Dir(path), ".fp-tmp-*")
    if e != nil { return e }
    tmpName := tmp.Name()
    defer func() { if err != nil { _ = os.Remove(tmpName) } }()
    if err = write(tmp); err != nil { tmp.Close(); return err }
    if err = tmp.Sync(); err != nil { tmp.Close(); return err }  // fsync 落盘
    if err = tmp.Close(); err != nil { return err }
    return os.Rename(tmpName, path)  // 原子覆盖（Win/POSIX 均支持覆盖已存在文件）
}
```

**改造清单**：

1. **`writeDOCXFull`** — 把 `os.Create` + `zip.NewWriter` 流程包进 atomicWrite 的回调。
2. **`XLSXWriteRows`** — excelize 有 `f.Write(io.Writer)` 方法（file.go:111），可直接 `f.Write(tmpFile)`：
   ```go
   return atomicWrite(path, func(tmp *os.File) error {
       return f.Write(tmp)
   })
   ```
3. **`XLSXWriteStructured`** — 同上，用 `f.Write(tmp)`。
4. **`writeFileEncoded`** — atomicWrite 包裹 `Write`。
5. **`writeDOCXAppend`** — 删掉 docxwrite.go:358 的 `os.Remove(in.Path)`，直接 `os.Rename(tmpName, in.Path)`（此路径在 buildTemp 返回前已关闭原 zip reader，见 docxwrite.go:197-198，无句柄占用问题）。

**验收**：写一个测试——用 `write` 回调里主动 panic，断言原文件字节完整不变。

---

### P0.2 工具执行加 panic recover

**问题**：`agent.go:1623` 的 `t.Execute` 无 recover。单工具 panic（excelize 内部 nil panic、xml 解析 panic）会冒泡到 `controller.go:483`，**终结整个 turn 而非只失败这一个工具**。LLM 收到 "internal error: ..." 而非可重试的工具错误。

**修法**：在 `executeOne`（agent.go:1525）的 `t.Execute` 调用外包 recover，参照 OfficeCLI `SafeRun`（CommandBuilder.cs:658-699）：

```go
result, err := func() (res string, e error) {
    defer func() {
        if r := recover(); r != nil {
            e = fmt.Errorf("tool panic (recovered): %v\n%s", r, debug.Stack())
        }
    }()
    return t.Execute(cctx, json.RawMessage(call.Arguments))
}()
```

**验收**：写一个会 panic 的测试工具，注册并调用，断言整个 turn 不挂、LLM 收到 "tool panic (recovered)" 错误。

---

### P0.3 补 confine 安全漏洞

**问题 A**：`mindmap_create`（mindmap.go:172）是写入工具（`ReadOnly() bool { return false }`）却**完全无 confine**，且不在 `ConfineWriters()`（confine.go:43-57）列表里——能写任意路径（如 `~/.ssh/authorized_keys`）。真实越权写入漏洞。

**修法**：
1. `mindmapCreate` 结构体从 `struct{}` 改为 `struct{ roots []string }`（与 docWrite 同模式，document.go:176）
2. `Execute`（mindmap.go:203）开头加 `if err := confine(m.roots, abs); err != nil { return "", err }`
3. `ConfineWriters()`（confine.go:43）列表加 `mindmapCreate{roots: rs}`

**问题 B**：`image_path`（docxwrite.go:44）无 confine，agent 可读任意路径文件嵌入 docx（信息泄露——把 `~/.ssh/id_rsa` 改名 .png 嵌入）。

**修法**：`docWrite.Execute`（document.go:227 附近，`writeDOCX` 调用前）遍历 sections 的 image_path，逐个调 `confineRead(w.roots, image_path)`。

**验收**：测试 mindmap_create 写沙箱外路径返回错误；测试 image_path 指向沙箱外返回错误。

---

### P0.4 残留临时文件清理

**问题**：无清理机制，崩溃后 `.docx-append-*`/`.fp-tmp-*` 永久残留污染用户目录。

**修法**：`atomic.go` 加 `cleanupStaleTemps()`，在 `DocumentTools()`（document.go:29）注册时调用一次。参照 OfficeCLI 的 Batch 孤儿清理（CommandBuilder.Batch.cs:450-482 的 age gate）——只清理超过 1 小时的（防止误删正在写的）：

```go
// cleanupStaleTemps 清理上次崩溃残留的 .fp-tmp-* 临时文件。
// 只清理 mtime > 1h 的，保守不碰正在写的文件。
func cleanupStaleTemps() {
    // 遍历用户工作目录，清理 mtime > 1h 的 .fp-tmp-* 文件
}
```

**注意**：工作目录从哪获取需确认（可能从 sandbox 配置或固定相对路径）。保守起见，Phase 0 先只清理 `.fp-tmp-*` 前缀，不碰旧版本前缀 `.docx-append-*`。

**验收**：创建一个 2 小时前的假临时文件，调用清理函数，断言被删；创建 5 分钟前的，断言不被删。

---

## Phase 1：修业务 bug（防产出坏结果）

> 审查发现 8 个"静默产出坏数据"的 bug，用户完全不知道结果错了。P1.1 最高频。

### P1.1 xlsx 数字类型推断 ❗最高频

**问题**：`XLSXWriteRows`（officedoc.go:80）把所有值当 string 写 → excelize `SetCellValue` 对 string 走 `SetCellStr`，cell 标记 `t="s"`（shared string）→ `=SUM()` 忽略文本数字 → **报表数据全错**。LLM 用 rows 模式写报表必中招。`TestXLSXWriteReadRoundtrip` 只测字符串往返，没测 SUM，所以 bug 漏网。

**修法**：写入前探测数字，参照 OfficeCLI `ExcelDataFormatter` 的类型推断：

```go
for ci, val := range row {
    cell, _ := excelize.CoordinatesToCellName(ci+1, ri+1)
    if n, err := strconv.ParseFloat(val, 64); err == nil {
        f.SetCellValue(sheet, cell, n)  // 数字 → 数字 cell
    } else {
        f.SetCellValue(sheet, cell, val) // 文本
    }
}
```

**注意前导零场景**：工号 "001"、邮政编码 "010000" 用 `strconv.ParseFloat` 会丢前导零（转成 1.0/10000）。办公场景前导零 ID 常见，**需要保留为文本**。修法：检测前导零（`len(val) > 1 && val[0] == '0'`）则跳过数字转换。

```go
isNumeric := !strings.HasPrefix(val, "0") || val == "0"  // 0 本身转数字，前导零保留文本
if isNumeric {
    if n, err := strconv.ParseFloat(val, 64); err == nil {
        f.SetCellValue(sheet, cell, n)
        continue
    }
}
f.SetCellValue(sheet, cell, val)
```

**验收**：写一个含数字和前导零 ID 的 xlsx，用 excelize 读回断言数字列 cell 类型是数字（`GetCellType` 返回非 `CellTypeString`/`CellTypeSharedString`），断言 `=SUM(A1:A3)` 结果正确；断言 ID 列保持文本。

---

### P1.2 条件格式 criteria 映射 + between 拆分

**问题**：
- **A2**：`XLSXCondFmt.Criteria` 注释（xlsxwrite_structured.go:77）声明 `greater_than`/`less_than`/`equal`/`between`（下划线），但 excelize 的 `criteriaType` map（styles.go:1303-1318）key 是**带空格形式** `greater than`/`less than`/`equal to`/`between`。LLM 按文档传 `greater_than` → `SetConditionalFormat`（styles.go:2869）查不到 key → `ErrParameterInvalid`。
- **A3**：`between` 的 `"min,max"` 没拆分，整串塞进 `Value` 字段（xlsxwrite_structured.go:313），但 excelize 的 `drawCondFmtCellIs`（styles.go:3465-3467）只读 `MinValue`/`MaxValue` → between 规则永远为空，Excel 打开要么忽略要么弹修复提示。

**修法**：`addConditionalFormat`（xlsxwrite_structured.go:295）加 criteria 映射和 between 拆分：

```go
criteriaMap := map[string]string{
    "greater_than": "greater than",
    "less_than":    "less than",
    "equal":        "equal to",
    "between":      "between",
}
criteria, ok := criteriaMap[cf.Criteria]
if !ok {
    return fmt.Errorf("unsupported criteria %q (use greater_than/less_than/equal/between)", cf.Criteria)
}
opts := excelize.ConditionalFormatOptions{Type: "cell", Criteria: criteria, Format: &styleID}
if cf.Criteria == "between" {
    parts := strings.SplitN(cf.Value, ",", 2)
    if len(parts) != 2 {
        return fmt.Errorf("between criteria needs 'min,max' value, got %q", cf.Value)
    }
    opts.MinValue = strings.TrimSpace(parts[0])
    opts.MaxValue = strings.TrimSpace(parts[1])
} else {
    opts.Value = cf.Value
}
```

**验收**：测试 greater_than 和 between 各生成一个条件格式，用 excelize 读回验证规则正确（Formula 字段非空）。

---

### P1.3 图片扩展名校验

**问题**：`addImageContentTypes`（docxwrite.go:449-455）的 switch 无 default 分支，未知扩展名（.bmp/.tiff/.webp）静默 fallback 到初始值 `image/png` → 产出的 docx ContentTypes 声明与实际文件不符 → Word 打开报"文件已损坏"。

**修法**：
1. `addImageContentTypes` 加 default 分支报错（明确告诉 LLM 怎么修）
2. 更好的做法是在 `writeDOCX` 收集图片阶段（docxwrite.go:97-118）就拦截非法扩展名，fail fast

```go
// addImageContentTypes 的 switch 加 default：
default:
    return fmt.Errorf("unsupported image format .%s: only PNG/JPG/GIF supported (convert your image first)", ext)
```

**验收**：传 `.bmp` 路径，断言返回明确错误而非静默产出。

---

### P1.4 文本 `\n`→`<w:br/>`、`\t`→`<w:tab/>`

**问题**：`runXML`（docxwrite.go:715-721）把整个 text 塞进单个 `<w:t xml:space="preserve">`。OOXML 中 `<w:t>` 内的字面 `\n`/`\t` **不被 Word 渲染**（当空白忽略）。用户传 `"第一行\n第二行"` → Word 显示成一行。影响所有段落、表格 cell、列表 item。无任何测试覆盖。

**修法**：`runXML` 改为按 `\n`/`\t` 分段，参照 OfficeCLI `NormalizeNewlineChars`（WordHandler.Helpers.FindReplace.cs）的换行处理思路：

```go
func runXML(text string, st DocStyle) string {
    rPr := runPropsXML(st)
    var b strings.Builder
    lines := strings.Split(text, "\n")
    for i, line := range lines {
        if i > 0 { b.WriteString("<w:br/>") }
        tabs := strings.Split(line, "\t")
        for j, seg := range tabs {
            if j > 0 { b.WriteString("<w:tab/>") }
            var esc strings.Builder
            xml.Escape(&esc, []byte(seg))
            b.WriteString(`<w:t xml:space="preserve">`)
            b.WriteString(esc.String())
            b.WriteString(`</w:t>`)
        }
    }
    return fmt.Sprintf(`<w:r>%s%s</w:r>`, rPr, b.String())
}
```

**验收**：传含 `\n` 和 `\t` 的文本生成 docx，解压断言 `document.xml` 含 `<w:br/>` 和 `<w:tab/>`。

---

### P1.5 有序列表独立编号

**问题**：所有有序列表共享 `numId=1`（docxwrite.go:651-653）。OOXML 语义：同一个 numId 实例被多个段落引用时编号**连续递增不自动重置**。所以两个有序列表，第二个从 4 开始而非 1。`TestWriteDOCXRoundtrip` 只有一个有序列表，没覆盖。

**现状**（docxwrite.go:889-895）：`numberingXML` 是 const 硬编码，定义了 abstractNumId=0（bullet）/1（decimal），以及 `<w:num w:numId="1">→abstractNumId=1`、`<w:num w:numId="0">→abstractNumId=0`。`renderList`（docxwrite.go:650）所有有序列表都用 numId=1。

**修法**（最小改动，复用 abstractNum，OOXML 标准做法）：
- `numberingXML` 从 const 改为函数，接收有序列表计数 `orderedListCount int`
- 每个有序列表分配独立 numId（从 2 起递增：numId=2,3,...），都绑定 abstractNumId=1（复用 decimal 定义），但加 `<w:startOverride w:val="1"/>` 重置计数
- 无序列表继续用 numId=0（bullet 样式相同，共享无害）
- `renderList` 改签名：`renderList(items []string, ordered bool, listIdx int) string`，有序列表 numId = `listIdx + 2`
- `buildSectionsXML`（docxwrite.go:501）遍历 sections 时，给每个有序列表递增 `listIdx`，并把总数传给 numbering 生成函数

**生成 XML 示例**（2 个有序列表）：
```xml
<w:num w:numId="2"><w:abstractNumId w:val="1"/><w:lvlOverride w:ilvl="0"><w:startOverride w:val="1"/></w:lvlOverride></w:num>
<w:num w:numId="3"><w:abstractNumId w:val="1"/><w:lvlOverride w:ilvl="0"><w:startOverride w:val="1"/></w:lvlOverride></w:num>
```

参照 OfficeCLI 的 numbering 生成（每列表独立 num 实例 + startOverride）。

**验收**：文档含两个有序列表，解压断言各自 numId 独立（2 和 3），打开后第二个从 1 开始。

---

### P1.6 TOC 加 settings.xml + updateFields

**问题**：`renderTOC`（docxwrite.go:789-810）生成真域代码（`<w:fldChar>` + `TOC \o "1-N" \h \z \u`），结构正确可更新。但缺 `word/settings.xml` 的 `<w:updateFields w:val="true"/>` → Word 打开时**不自动重算域**，TOC 显示占位文字 "[Table of Contents - Update field to populate]"，用户必须手动 F9。完全无测试。

**修法**：`writeDOCXFull`（docxwrite.go:134）检测是否含 TOC section，是则在 parts 列表新增 `word/settings.xml`：

```xml
<w:settings xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:updateFields w:val="true"/>
</w:settings>
```

并在 `[Content_Types].xml` 加 `settings` part 的 Override 声明、`word/_rels/document.xml.rels` 加关系（参照 docxwrite.go:157-164 的 parts 注册模式）。

**验收**：含 TOC 的 docx，解压断言 `word/settings.xml` 存在且含 `<w:updateFields w:val="true"/>`。

---

### P1.7 合并单元格样式应用到整个区域

**问题**：`writeSheet`（xlsxwrite_structured.go:164-235）顺序是先写所有 Cells（含 style，:176-214），再做 MergeCell（:216-225）。excelize 的 `MergeCell` 合并后只保留左上角 cell 的值与样式；但 Excel 渲染合并区域需要**区域内每个 cell 都带边框样式**才能画出完整边框。当前只在左上角画了边框，导致合并单元格的下/右边框缺失。`TestXLSXWriteStructuredMergeAndColWidth` 只验证左上角值和列宽，没验证边框。

**现状**（xlsxwrite_structured.go:216-225）：合并用 `strings.SplitN(m.Range, ":", 2)` 拿到 `parts[0]`（左上 cell）和 `parts[1]`（右下 cell）。

**修法**：调整顺序——先 MergeCell，再把左上角的 styleID 重新 apply 到整个范围。不需要新函数，直接复用 `parts[0]`/`parts[1]`：

```go
// 第一遍：先写值（不设样式或设但会被覆盖也无所谓）
// 第二遍：合并
for _, m := range sh.Merges {
    r := strings.TrimSpace(m.Range)
    parts := strings.SplitN(r, ":", 2)
    topLeft, bottomRight := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
    if err := f.MergeCell(sheet, topLeft, bottomRight); err != nil {
        return fmt.Errorf("merge %s: %w", r, err)
    }
    // 合并后，把左上角的 styleID apply 到整个范围（边框/底纹才能画全）
    if styleID := cellStyleID(topLeft); styleID > 0 {  // cellStyleID 查记录的左上角 styleID
        f.SetCellStyle(sheet, topLeft, bottomRight, styleID)
    }
}
```

需要在写 cell 阶段记录每个 cell 的 styleID（`map[cellRef]int`），合并后查左上角的 styleID 重新 apply 到整个范围。

**验收**：测试合并区域，用 excelize 读回断言范围内每个 cell 都有边框样式（`GetCellStyle` 返回相同 styleID）。

---

### P1.8 append 到外部 docx 的 numbering fallback

**问题**：`writeDOCXAppend`（docxwrite.go:212-222）只对 `styles.xml` 有 fallback（原文件缺失则用 `defaultStylesXML()`），`numbering.xml` 没有。append 含列表的 section 到外部 docx（无 `numbering.xml`，或 numId 0/1 不匹配）→ 列表编号失效（显示无项目符号，或 Word 报"列表不存在"）。

**修法**：append 流程增加检测——原 docx 是否有 `word/numbering.xml`：
- 若无 → 生成默认 numbering.xml 补入（参照 styles.xml 的 fallback 模式，docxwrite.go:212-222）
- 若有 → 合并本工具的 numbering 定义到原 numbering.xml（避免 numId 冲突）

**验收**：append 含有序列表的 section 到一个无 numbering.xml 的外部 docx，断言列表编号正常显示。

---

## Phase 2：修读取逻辑（防信息丢失）

> 审查发现 5 类读取问题，LLM 看到的内容严重失真或不完整。

### P2.1 readDOCX 读全部文本 part

**问题**：`readDOCX`（officedoc.go:104-126）只读 `word/document.xml`，其他 part 一律 `continue`（:111-113）。页眉/页脚/脚注/尾注/批注里的文本 **LLM 完全看不到**。合同/论文的关键信息（如页眉的合同编号、脚注的法律引用）常在这些 part。

**修法**：遍历所有 `word/*.xml`，参照 OfficeCLI 读多 part 的方式，加前缀标签区分来源：

- `word/header*.xml` → `[页眉] ...`
- `word/footer*.xml` → `[页脚] ...`
- `word/footnotes.xml` → `[脚注 N] ...`
- `word/endnotes.xml` → `[尾注 N] ...`
- `word/comments.xml` → `[批注 N] ...`

**注意**：注释 part（comments）的关系需读 `.rels`（r:id 映射到 word/comments.xml），不是固定文件名。Phase 2 先按固定文件名尝试，复杂映射留后续。

**验收**：构造含页眉脚注的 docx，读取断言这些文本出现且带标签。

---

### P2.2 readDOCX 保留表格结构

**问题**：`parseDOCXText`（officedoc.go:130-165）只识别 `p`/`t`/`tab`/`br`，**不识别 `tbl`/`tr`/`tc`**。表格里每个 cell 内含一个 `w:p`，遇到就 `b.WriteByte('\n')`（:159）→ 每个 cell 的文字单独成行，**行列结构完全丢失**。LLM 看到 "指标\nQ1\nQ2\n营收\n1.2亿" 无法区分这是表格还是竖排文字。`TestWriteDOCXRoundtrip` 只 `Contains` 检查，不验证结构。

**修法**：`parseDOCXText` 增加 tbl/tr/tc 状态跟踪：
- 遇到 `tbl` StartElement → 进入表格模式（设 `inTable=true`）
- 遇到 `tr` StartElement → 换行
- 遇到 `tc` StartElement → 若非行首 cell 则前缀 `| `
- `t` 文本在表格模式下，收集到当前 cell
- 遇到 `tbl` EndElement → 退出表格模式，空行分隔

输出示例：
```
| 指标 | Q1 | Q2 |
| 营收 | 1.2亿 | 1.8亿 |
```

**验收**：构造含表格的 docx，读取断言输出含 `|` 分隔和正确行列数。

---

### P2.3 readPPTX 读备注

**问题**：`readPPTX`（officedoc.go:171-206）只扫 `ppt/slides/slide*.xml`（:179），不读 `ppt/notesSlides/notesSlide*.xml` → **演讲者备注丢失**。备注对理解 PPT 内容经常很关键（演讲要点）。

**修法**：每个 slide 读完后，查找对应 `ppt/notesSlides/notesSlide{N}.xml`（N 与 slide 编号对应），有则用 `parseSlideText` 提取文本，追加 `[备注] ...` 段。

**验收**：构造含备注的 pptx，读取断言备注文本出现。

---

### P2.4 readXLSX 公式读回

**问题**：`readXLSX`（officedoc.go:48）用 `f.GetRows(sheet)`，默认 `RawCellValue=false`，公式 cell 返回 `<v>` 缓存值。但 hiq 自己 `SetCellFormula` 写的公式**无缓存值**（excelize 不计算）→ 读回空字符串 → LLM 以为没写成功。`TestXLSXWriteStructuredFormulaAndStyle` 用 `GetCellFormula` 单独读公式绕过了这个问题，但真实 `doc_read` 路径走的是 `GetRows`。

**修法**：改用遍历 cell 而非 `GetRows`，对每个 cell 先查 `f.GetCellFormula`：
- 有公式 → 返回 `=<formula>` 字符串（参照 OfficeCLI 的 formulas mode）
- 无公式 → 返回值（缓存值或字面值）

**实现思路**：
```go
rows, _ := f.GetRows(sheet)  // 仍用 GetRows 拿文本值
// 再遍历补公式：对每个 cell 查 GetCellFormula
for ri, row := range rows {
    for ci := range row {
        cell, _ := excelize.CoordinatesToCellName(ci+1, ri+1)
        if formula, _ := f.GetCellFormula(sheet, cell); formula != "" {
            rows[ri][ci] = "=" + formula
        }
    }
}
```

**验收**：写一个含公式的 xlsx，读取断言返回 `=SUM(...)` 字符串而非空。

---

### P2.5 readXLSX trailing 空 cell 补齐 + 日期格式化

**问题**：
- **B5**：`GetRows`（excelize rows.go:237-252）截断 trailing 空 cell → 各行列数不一致，LLM 按固定列数解析会错位。
- **B6**：无样式的日期 cell（`c.S == 0`）读出 Excel 序列号 "45678" 而非 "2024-12-01"。`normalizeNumber`（officedoc.go:89）只处理 ".0" 后缀，不碰序列号。

**修法**：
- **B5**：读所有行后，找最大列数，短行用空字符串补齐。
- **B6**：检测 cell 的 numFmt 是否日期格式（用 `f.GetCellStyle` 拿 styleID，再查 numFmt 字符串是否含 `y`/`d`/`m`/`h` 等日期标记），是则用 `excelize.ExcelDateToTime` 转成时间再格式化。

**验收**：含 trailing 空 cell 的表格，断言各行等长；含日期的表格断言返回日期字符串。

---

## Phase 3：修一致性（LLM 能发现并正确使用能力）

### P3.1 xlsx_write schema 补全 structured 能力

**问题**：`xlsxWrite.Schema`（document.go:381）只说 `"content":{"description":"array of arrays of strings (rows)"}`，完全隐瞒 structured/charts/cond_fmt 能力。而 `docWrite.Schema`（document.go:189）说全了。LLM 选 xlsx_write（名字最贴切）发现不了多 sheet/图表/条件格式等核心能力。

**修法**：`xlsxWrite.Schema` 的 content description 改为和 docWrite 一致：

```
"content":{"description":"array of arrays of strings (rows → Sheet1), OR a structured object {sheets:[{name,cells:[{ref,value,number,formula,format,style}],merges,col_widths,cond_fmt}],charts:[...]}. Use structured form for multi-sheet/styling/charts/conditional formatting."}
```

**验收**：人工检查 schema 输出，确认和 docWrite 的 xlsx 描述对齐。

---

### P3.2 html→md 做真 markdown 转换

**问题**：`stripHTMLText`（document.go:547-561）只剥 `<>` 标签产出纯文本，但 schema 声称 html→md（document.go:395, 404）。LLM 调 html→md 期望拿到 markdown 源码，实际拿到剥了标签的纯文本，下游 markdown 渲染断裂（`<h1>` 变纯文本而非 `#`，`<strong>` 丢失加粗）。

**修法**：新增 `htmlToMarkdown` 函数，html→md 路径用新函数，html→text 仍用 strip：

- `<h1>` → `# `、`<h2>` → `## `...`<h6>` → `###### `（块级，前后空行）
- `<strong>`/`<b>` → `**...**`
- `<em>`/`<i>` → `*...*`
- `<code>` → `` `...` ``
- `<pre>` → ```` ```...``` ````
- `<ul><li>` → `- ...`
- `<ol><li>` → `1. ...`（编号自增）
- `<a href="X">T</a>` → `[T](X)`
- 其他标签 strip

**验收**：html 含 h1/strong/ul，转 md 断言含 `#`/`**`/`- `。

---

### P3.3 docx append 返回值区分 wrote/appended

**问题**：`docWrite.Execute`（document.go:230）硬编码 `"wrote %s (%d sections)"`，append 模式也返回 "wrote"，LLM 不知道是新建还是追加。文本分支（:317-320）会根据 `p.Append` 切换 "wrote"/"appended"，docx 分支不会。

**修法**：docx 分支也根据 `p.Append` 切换：

```go
mode := "wrote"
if p.Append { mode = "appended" }
return fmt.Sprintf("%s %s (%d sections)", mode, abs, len(sections)), nil
```

**验收**：append=true 调用，断言返回含 "appended"。

---

### P3.4 GIF 限制提示

**问题**：skill body（builtins.go:306）说支持 GIF，但隐瞒动画不保留、部分 Word 版本兼容性差的限制。

**修法**：`builtinDocumentAutoBody`（builtins.go:306）补一句：

> GIF is supported but only the first frame renders (no animation); prefer PNG for reliability.

---

### P3.5 skill body 整体更新

**问题**：`builtinDocumentAutoBody`（builtins.go:299-315）现 17 行，缺少单位提示、xlsx_write structured 说明、读取限制说明。

**修法**：扩充到 40-50 行，补充：
- ⚠️ **单位提示**：Word 的 `size` 是 half-points（24=12pt），Excel 的 `size` 是 points（12=12pt）—— 同名字段不同单位，极易混淆
- 明确 `xlsx_write` 支持 structured form（和 `doc_write` 一样，schema 已对齐 P3.1）
- 读取限制：`doc_read` 读 docx 会包含页眉脚注但文本框可能丢失；xlsx 公式读回为 `=<formula>`
- 更新 PNG/JPG/GIF 限制说明（GIF 只首帧）

---

### P3.6 配套测试补全

审查发现大量 bug 漏网就是因为测试没覆盖关键场景。每个修复点都配测试，重点补：

| 修复点 | 必须新增的测试 |
|---|---|
| P0.1 | `TestAtomicWritePreservesOriginalOnPanic` — 写入 panic 后原文件完整 |
| P1.1 | `TestXLSXWriteRowsNumericCells` — 数字写入后 SUM 正确，前导零 ID 保持文本 |
| P1.2 | `TestXLSXCondFmtCellType` — greater_than/between 真生成规则 |
| P1.4 | `TestWriteDOCXNewlineTab` — `\n`→`<w:br/>`、`\t`→`<w:tab/>` |
| P1.5 | `TestWriteDOCXMultipleOrderedListIndependentNumbering` |
| P1.6 | `TestWriteDOCXTOCAutoUpdate` — settings.xml 存在 |
| P1.7 | `TestXLSXMergeCellStyleAppliedToRange` |
| P2.1 | `TestReadDOCXIncludesHeaderFooterFootnote` |
| P2.2 | `TestReadDOCXTableStructure` — `|` 分隔 |
| P2.4 | `TestReadXLSXFormula` — 读回 `=SUM(...)` |

---

## 三、实施顺序

```
Phase 0（保底，2-3 天）→ 不损坏用户文件
  P0.1 atomicWrite（统一 5 处）
  P0.2 panic recover
  P0.3 confine 补全（mindmap + image_path）
  P0.4 临时文件清理

Phase 1（修 bug，3-4 天）→ 不产出坏结果
  P1.1 xlsx 数字类型 ❗先做（最高频）
  P1.2 条件格式 criteria + between
  P1.3 图片扩展名校验
  P1.4 \n\t 处理
  P1.5 列表独立编号
  P1.6 TOC updateFields
  P1.7 合并单元格样式
  P1.8 append numbering fallback

Phase 2（修读取，2-3 天）→ 不丢信息
  P2.1 readDOCX 全 part
  P2.2 readDOCX 表格结构
  P2.3 readPPTX 备注
  P2.4 readXLSX 公式
  P2.5 readXLSX trailing/日期

Phase 3（修一致性，1 天）→ LLM 能发现能力
  P3.1 xlsx_write schema
  P3.2 html→md 真转换
  P3.3 append 返回值
  P3.4 GIF 提示
  P3.5 skill body 更新
  P3.6 配套测试补全
```

---

## 四、验收标准

每个修复点必须满足：

1. **配一个测试**（审查发现大量 bug 漏网就是因为测试没覆盖场景）
2. **回归测试通过**：不破坏现有 `docxwrite_test.go`/`xlsxwrite_*_test.go`/`officedoc_test.go` 的通过用例
3. **参照 OfficeCLI 的测试模式**——它对每个格式边界、每个 issue 子类型都有专门 fixture

**整体验收命令**：
```bash
cd C:\Users\13852\Desktop\Swarm-OS\hiq
go test ./internal/tool/builtin/... -run "XLSX|DOCX|Read|Write|Atomic|Confine" -v
go vet ./internal/tool/builtin/...
```

---

## 五、与 OfficeCLI 的对照（每个修法的参照来源）

| 修复项 | 参照 OfficeCLI 实现 |
|---|---|
| atomicWrite（P0.1） | `AtomicPackageWriter.cs:31-82`（临时文件 + File.Replace） |
| panic recover（P0.2） | `SafeRun`（CommandBuilder.cs:658-699） |
| 临时文件清理（P0.4） | `BatchExecutor` 的 age gate（CommandBuilder.Batch.cs:450-482） |
| xlsx 数字推断（P1.1） | `ExcelDataFormatter`（类型推断） |
| 条件格式 criteria（P1.2） | excelize 内部映射（styles.go:1303-1318）—— 我们做 snake_case→空格的适配层 |
| `\n`→`<w:br/>`（P1.4） | `MaterializeSoftBreakChars`/`NormalizeNewlineChars`（WordHandler.Helpers.FindReplace.cs） |
| 有序列表编号（P1.5） | 每列表独立 abstractNum + num + startOverride |
| TOC updateFields（P1.6） | settings.xml 的 `<w:updateFields/>` 标准做法 |
| readDOCX 多 part（P2.1） | OfficeCLI 读全部文本 part 的模式 |
| readDOCX 表格（P2.2） | 表格 cell 用分隔符保留结构 |
| readXLSX 公式（P2.4） | `formulas` mode（返回 `=<formula>`） |
| html→md（P3.2） | 真 markdown 反向转换（h1→#、strong→**、li→-） |

---

## 六、风险与缓解

| 风险 | 缓解 |
|---|---|
| atomicWrite 改造引入回归（写入路径全变） | 每个改造点配测试，先跑全量回归 |
| Windows rename 覆盖行为 | Go 1.5+ 用 `MoveFileEx(MOVEFILE_REPLACE_EXISTING)`，已验证支持覆盖已存在文件；但调用方须先关闭目标句柄（见 P0.1 注意） |
| readDOCX 多 part 改动影响现有 roundtrip 测试 | 新逻辑加前缀标签，正文输出不变 |
| xlsx 数字推断误判（如 ID 编号 "001" 转 1） | P1.1 已加前导零检测：`len(val) > 1 && val[0] == '0'` 保留为文本 |
| panic recover 吞掉真正该崩的 bug | recover 后记录完整 stack 到日志，不只是返回错误 |
| readDOCX 读 header/footer 可能读到模板占位符 | 加前缀标签让 LLM 可区分，Phase 2 接受这个限制 |

---

# Phase 4-6：新增功能（待实施）

> 以下三阶段整合自 v3 方案 `cosmic-coalescing-moonbeam.md`，吸收了之前审查发现的 13 处矛盾/漏洞。建立在 Phase 0 的 atomicWrite/panic recover/confine 地基之上。

## Phase 4：docxsafety.go — 模板专用防护（2-3 天）

> Phase 0 的 `atomic.go` 解决了"写入不损坏文件"。Phase 4 解决"**读取模板**不损坏进程"——doc_template 要读用户提供的 .docx 模板，模板可能损坏、超大、被 Office 锁定。

### P4.1 解压炸弹检测（三件套，办公场景够用）

**问题**：doc_template 的 `source` 读取用户提供的 .docx。恶意/损坏模板（小 zip 解出几 GB）会让 hiq OOM。OfficeCLI 用六件套，我们做三件套：

```go
// guardDecompressionBomb 检查 zip 包是否是解压炸弹。
// 三件套：总解压量 2GB / 条目数 10 万 / 压缩比 1000×。
// 参照 OfficeCLI DocumentLimits.cs（MaxUncompressedBytes/MaxZipEntries/MaxCompressionRatio）。
func guardDecompressionBomb(zipPath string) error {
    // 遍历 zip central directory（不解压），累加 UncompressedSize64
    // 超 2GB → ErrDecompBomb
    // 条目数超 10 万 → ErrDecompBomb
    // 压缩比（总量/压缩量）超 1000× → ErrDecompBomb
}
```

**不做**：递归深度限制（Go xml 解析器自身稳健）、正则超时（Go RE2 无回溯）、DOM 元素上限（办公文档达不到）、SSRF（本地无网络模板）。

### P4.2 文件锁检测

**问题**：用户在 Word 里打开着模板，doc_template 读取/复制可能失败或读到半成品。

**修法**：`checkFileLocked` 尝试排他打开，失败返回友好错误：
```go
func checkFileLocked(path string) error {
    f, err := os.OpenFile(path, os.O_RDWR, 0o644)
    if err != nil {
        return DocError{Code: ErrFileLocked, Message: "...", Suggestion: "请先关闭 Word/Excel 中打开的此文件"}
    }
    f.Close()
    return nil
}
```

### P4.3 结构化错误码 DocError

**问题**：现有 `fmt.Errorf` 对 LLM 不友好。doc_template 的错误（索引越界、占位符未找到、单元格溢出）需要告诉 LLM "怎么修"。

```go
type DocError struct {
    Code       string `json:"code"`
    Message    string `json:"message"`
    Suggestion string `json:"suggestion,omitempty"` // 告诉 LLM 怎么修
}

const (
    ErrFileNotFound   = "file_not_found"
    ErrFileLocked     = "file_locked"
    ErrCorruptFile    = "corrupt_file"
    ErrDecompBomb     = "decompression_bomb"
    ErrCellOverflow   = "cell_overflow"          // Excel 32767 上限
    ErrTableIndexOOB  = "table_index_out_of_bounds"
    ErrRowIndexOOB    = "row_index_out_of_bounds"
    ErrColIndexOOB    = "col_index_out_of_bounds"
    ErrMergedCell     = "merged_cell_continuation" // v3 审查新增：填到 vMerge continue 格子
    ErrPlaceholderNF  = "placeholder_not_found"
    ErrInvalidArg     = "invalid_argument"          // v3 审查新增：source==path 等
)
```

### P4.4 其他安全辅助

- `stripBOM(data []byte) []byte` — 剥离 UTF-8/16 BOM（模板可能带 BOM）
- `xmlEscape(s string) string` — 统一 XML 转义（复用现有 `xml.Escape`）
- `ensureDir(path)` — 复用现有 `os.MkdirAll`，统一入口
- `checkCellValue(value, ref)` — Excel 单元格 32767 字符上限校验

**验收**：测试恶意 zip（构造超高压缩比）被拒；测试锁定的文件返回 ErrFileLocked。

---

## Phase 5：doc_template — 模板填充工具（核心，1.5 周）

> 办公场景最高频需求：用户有合同/公文/报表模板，让 AI 填名字、日期、金额、表格。这是 v3 方案的核心，也是 OfficeCLI 的核心能力。

### P5.1 工具注册与参数

新增 `doc_template` 工具，注册到 `DocumentTools()` 和 document-auto 的 AllowedTools。

```go
type docTemplate struct{ roots []string }

func (docTemplate) Name() string { return "doc_template" }
```

**参数**（无互斥规则，自由组合）：

| 参数 | 必填 | 说明 |
|---|---|---|
| `source` | ✅ | 只读模板路径（confine 校验 + ≠ path 校验） |
| `path` | ✅ | 输出路径（confine 校验） |
| `find_replace` | ❌ | 简单 key-value 替换 |
| `table_fill` | ❌ | 逐格填写表格 |
| `header` | ❌ | `{"text": "...", "align": "center"}` |
| `footer` | ❌ | 同上，支持 `{PAGE}` |

**v3 审查的关键修订**（已吸收）：
- `source` 必须 confine（v3 漏写）
- `source == path` 禁止（返回 ErrInvalidArg）
- `header.text`（不是 v3 含糊的 `"default"`）
- 占位符正则支持中文（见 P5.2）

### P5.2 find_replace（查找替换，含跨 run）

**占位符正则**（采用 OfficeCLI 成熟实现，支持中文/点路径/下标）：
```go
// \w 在 Go regexp 默认匹配 Unicode（含中文），支持 {{甲方}}/{{a.b}}/{{items[0]}}
var placeholderRe = regexp.MustCompile(`\{\{\s*([\w.\-\[\] ]+?)\s*\}\}`)
```

**输入格式**：
```json
[
  {"find": "{{甲方}}", "replace": "张三"},
  {"find": "{{日期}}", "replace": "2026年8月7日"}
]
```

**跨 run 处理**（采用 SplitRuns，不用矛盾的 mergeRuns）：
- 占位符可能跨多个 `<w:r>`（Word 把文本切分到不同 run）
- 拆分首尾 run 使占位符落到独立 run，替换后**继承第一个 run 的 `<w:rPr>` 格式**
- 参照 OfficeCLI `WordHandler.Helpers.FindReplace.cs` 的 `BuildRunTexts`/`SplitRunAtOffset`

**借鉴 OfficeCLI 的两条防 bug 规则**：
- 替换值不再回灌正则（防 `{{name}}` 值里含 `{{other}}` 被二次替换）
- 占位符未找到 → 返回 warnings 不阻断（`checkPlaceholders`）

### P5.3 table_fill（表格逐格填写，含 grid 模型）

**输入格式**：
```json
[
  {"table": 0, "row": 2, "col": 1, "value": "项目A"},
  {"table": 0, "row": 2, "col": 2, "value": "100万", "style": {"bold": true, "color": "#FF0000"}}
]
```

**grid 模型**（处理合并单元格）：
- 构建 `tableGrid`：遍历所有 `<w:tbl>`，处理 `gridSpan`（水平合并）+ `vMerge`（垂直合并）
- `cells[row][col]` 映射到实际 `<w:tc>` 节点

**v3 审查新增的明确语义**：
- 填到 **vMerge continue 格子**（虚拟格子，无独立 `<w:tc>`）→ 返回 `ErrMergedCell`："cell at [r,c] is a vMerge continuation; target the merge-start cell instead"
- `table/row/col` 索引越界 → 返回 `ErrTableIndexOOB`/`ErrRowIndexOOB`/`ErrColIndexOOB`

**style 应用**（参照 docxwrite 的 `runPropsXML`，复用现有逻辑）：
- 不传 style → 保留单元格原格式
- 传 style → 替换 `<w:rPr>`，保留未指定的属性

### P5.4 header/footer（页眉页脚）

**v3 审查的关键修订**：必须读 `.rels` 才能定位。

OOXML 里 `<w:headerReference w:r:id="rId7"/>` 只存 rId，真实文件（`word/header1.xml`）映射在 `word/_rels/document.xml.rels`。**流程**：
1. 读 `document.xml` 找 `<w:sectPr>` 里的 `<w:headerReference>`/`<w:footerReference>`
2. 读 `word/_rels/document.xml.rels` 把 rId 解析成实际文件名
3. 修改对应 header/footer 文件

Phase 5 先做**替换所有 section 的 default header**（简单场景）；per-section header 留后续。

### P5.5 best-effort 模式 + warnings 返回

部分填写失败时不阻断，返回 warnings：
```json
{
  "wrote": "合同-张三.docx",
  "warnings": [
    {"code": "placeholder_not_found", "message": "{{乙方}} not found in document"},
    {"code": "merged_cell_continuation", "message": "table 0 [3,2] is vMerge continuation, skipped"}
  ]
}
```

LLM 看 warnings 自己决定是否修正。

**验收**：用真实合同模板测试 find_replace + table_fill 端到端；测试合并单元格定位；测试占位符跨 run。

---

## Phase 6：功能增强（1 周）

> v3 的 doc_read 多 mode + DocStyle/XLSXStyle 扩展 + 单位转换。

### P6.1 doc_read 多 mode

新增 `mode` 参数：

| mode | 返回值 | 用途 |
|---|---|---|
| `text`（默认） | 纯文本（现有，Phase 2 已增强） | 快速查看 |
| `structured` | JSON：段落/标题/表格结构 + 合并信息 + 单元格样式 | 理解模板结构，为 table_fill 做准备 |
| `tables` | JSON：只提取表格数据 | 与 table_fill 输入格式对齐 |
| `metadata` | JSON：作者/标题/时间 | 文档属性 |

`mode:"tables"` 输出直接可喂给 table_fill，形成"读结构→填数据"闭环。

### P6.2 DocStyle 格式扩展（P0 属性）

现有 DocStyle（docxwrite.go:54）扩展，**v3 审查确认的受影响调用点**：
- `runPropsXML`（docxwrite.go:814）— 读 Size，需加类型分支
- `pPrXML`（docxwrite.go:619）— 间接受影响
- 新增的 `applyCellStyle`（doc_template）— 读 Size

**新增 P0 属性**：

| 属性 | JSON tag | 说明 |
|---|---|---|
| `Align` 扩展 | `align` | 新增 `"justify"` 两端对齐 |
| `SpaceBefore` | `space_before` | 段前间距 `"12pt"` |
| `SpaceAfter` | `space_after` | 段后间距 |
| `LeftIndent` | `left_indent` | 左缩进 `"2cm"` |
| `RightIndent` | `right_indent` | 右缩进 |
| `Underline` | `underline` | `true` 或 `"single"/"double"` |
| `LineSpacingRule` | `line_spacing_rule` | `"auto"/"exact"/"atLeast"` |

### P6.3 XLSXStyle 格式扩展（P0 属性）

现有 XLSXStyle（xlsxwrite_structured.go:38）扩展：

| 属性 | 说明 |
|---|---|
| `AlignV` | 垂直对齐 `"top"/"center"/"bottom"` |
| `Strike` | 删除线 |
| `Underline` | `true` 或 `"single"/"double"` |
| `BorderLeft/Right/Top/Bottom` | object，独立边框控制 `{style:"thin", color:"999999"}` |
| `BorderColor` | 统一边框颜色 |

### P6.4 单位转换

新增 `docxstyle.go`：

```go
// parseSize 统一处理 Size 字段（向后兼容）：
// int 24 → half-points 24（旧用法）
// string "12pt" → half-points 24
func parseSize(v any) (int, error)

// parseSpacing 支持 pt/cm/in/twips → half-points（段间距）
func parseSpacing(value string) (int, error)
// parseIndent 支持 pt/cm/in/char → twips（缩进）
func parseIndent(value string) (int, error)
```

**单位转换参照**（OfficeCLI Units.cs）：
- 1 pt = 20 twips = 2 half-points = 12700 EMU
- 1 cm = 567 twips
- 1 in = 1440 twips

**skill body 显著提示单位差异**（Phase 3 已加，Phase 6 保持）：Word size=half-points vs Excel size=points。

**验收**：测试 `"12pt"`→24、`"2cm"`→1134 twips；测试 DocStyle 新属性在 doc_write 和 doc_template 都生效。

---

## Phase 4-6 实施顺序

```
Phase 4（防护，2-3 天）→ 模板读取安全
  P4.1 guardDecompressionBomb（三件套）
  P4.2 checkFileLocked
  P4.3 DocError 错误码
  P4.4 stripBOM/xmlEscape/checkCellValue

Phase 5（核心功能，1.5 周）→ doc_template
  P5.1 工具注册 + source/path confine
  P5.2 find_replace（增强正则 + SplitRuns）
  P5.3 table_fill（grid 模型 + vMerge 报错）
  P5.4 header/footer（读 .rels）
  P5.5 best-effort warnings

Phase 6（增强，1 周）→ 多 mode + 样式扩展
  P6.1 doc_read mode:structured/tables/metadata
  P6.2 DocStyle P0 扩展
  P6.3 XLSXStyle P0 扩展
  P6.4 单位转换 parseSize/parseSpacing/parseIndent
```

## Phase 4-6 与 v3 方案的修订对照

v3 方案（cosmic-coalescing-moonbeam.md）有 13 处矛盾/漏洞，本 spec 已全部吸收修订：

| v3 原文 | 问题 | 本 spec 修订 |
|---|---|---|
| source 无 confine | 信息泄露 | P5.1 source 必须 confine |
| source==path 未禁止 | 覆盖模板 | P5.1 返回 ErrInvalidArg |
| 占位符"仅字母数字下划线" | 与 `{{甲方}}` 示例矛盾 | P5.2 改用 Unicode 正则 |
| Phase 1.6 mergeRuns vs P1 风险表 SplitRuns | 两个相反操作 | P5.2 统一用 SplitRuns |
| header 的 "default" 字段 | 语义不明 | P5.4 改为 `text` |
| header/footer 不读 .rels | 无法定位 | P5.4 必须读 .rels |
| vMerge continue 行为未定义 | LLM 不知所措 | P5.3 返回 ErrMergedCell |
| DocStyle.Size 改 any 未列调用点 | 易漏改 | P6.2 列出 runPropsXML/pPrXML |
| Size 单位 docx vs xlsx 混淆 | LLM 必混 | P6.4 + skill body 显著提示 |
| SharedStrings 警告无实施条目 | 风险识别了没做 | P6.1 xlsx_read 加警告 |
| cols/range 优先级未定义 | LLM 困惑 | P6.1 明确取交集 |
| withFileLock 锁粒度未定义 | 竞态 | P4.2 明确锁 path |
| 存在性检查顺序 | 晦涩错误 | P4 流程：os.Stat → 炸弹检测 → 文件锁 |
