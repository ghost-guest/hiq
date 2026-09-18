# ppt-auto 工作流修复与增强规格

> **日期**：2026-08-26
> **范围**：ppt-auto 主题生成 + image-to-PPT 两条工作流
> **状态**：已全部实施（批次一 2026-08-27：S-01~S-17、S-20~S-22、D-01、D-02、D-04 + M-6；批次二 2026-08-27：S-18、S-19、S-10 全部、D-03、D-05、D-06——见文末实施记录）
> **修订**：2026-08-26 核实修正（S-01/S-02/S-07/S-08/S-09/S-14/S-17/S-18/S-19/D-01/D-03/D-06/附录；编号为修订前旧编号）
> **修订 2**：2026-08-26 兼容性审查（两不变量：生成链路不被破坏、产出的 PPTX 在 PowerPoint 真实可用）——S-04/S-05 上线策略、S-06 去 id、S-10 fix 改写范围、S-13 pattern 兜底改安全底色、S-16 改强制降采样、S-19 exit code 冻结（编号为本版编号）
> **修订 3**：2026-08-27 沙箱模拟运行——新增 S-21（无模板+参考图颜色断链，P1）、S-22（icacls 解码崩溃，P2），修正 S-05 上线策略"无设置入口"表述（cowork_settings.go:515 存在入口），附模拟运行记录（第七节末）
> **修订 4**：2026-08-27 批次一实施——22 项落地 + 2 项实施中发现的新缺陷修复（fix_svg 贪心截断捷径、merge 误写 background_type），SkillVersion 45→46，见文末实施记录
> **修订 5**：2026-08-27 批次二实施——收尾 S-18/S-19/S-10 全部/D-03/D-05/D-06，SkillVersion 46→47；D-05 实施中修正了 prompt 示例与提取正则的形状不一致（对象 vs 裸数组）
> **修订 6**：2026-08-27 三项遗留决策全部裁定并落地（见文末「决策记录」），SkillVersion 47→48

---

## 一、P0 — 必须修复（阻塞核心功能）

### S-01: autofit_fontsize 输出接入骨架生成器

**问题**：`autofit_fontsize.py` 计算的字号（如 `{"title": 24, "card_title": 15, "body": 12}`）未被 `build_page_skeleton.py` 消费，骨架页始终用硬编码 26/18/14。

**规格**：

方案 A（推荐）：`build_page_skeleton.py` 增加 `--autofit` 参数

1. `build_page_skeleton.py` 新增 CLI 参数 `--autofit <path>`，读取 autofit 输出 JSON
2. `Page.__init__` 的默认字号逻辑改为：

```python
f = {"title": 26, "card_title": 18, "body": 14}  # 兜底
if autofit_data:
    f.update(autofit_data)
f.update(fonts_override or {})  # pages.json 的 fonts 仍为最高优先
```

3. SKILL.md Step 6 更新：有参考图时，骨架生成命令增加 `--autofit <path>`
4. **前置**：定义落盘路径。当前 `autofit_fontsize.py` 只往 stdout 打 JSON，没有任何脚本写文件。需要在 SKILL.md 中约定：Step 5 执行 autofit 后，LLM 将 stdout 重定向到 `{project}/output/autofit_result.json`，或由 `preflight.py` 封装一次落盘调用。

方案 B（备选）：SKILL.md 指示 LLM 将 autofit 结果写入 pages.json 的 `fonts` 字段。

**验收**：
- 有参考图 + autofit 输出 → 骨架 SVG 使用 autofit 字号
- 无参考图 → 骨架 SVG 使用默认 26/18/14
- pages.json 的 `fonts` 字段优先级最高（覆盖 autofit）

---

### S-02: 统一 rgba 规则

**问题**：`config.py` 的 `SVG_CONSTRAINTS['forbidden_patterns']` 禁止 `rgba()`，但 `template_config.json` 和 `build_page_skeleton.py` 都在用 rgba。此外 SKILL.md 规则 4 明确指示 LLM "用 rgba() 做半透明卡片背景"，矛盾比最初评估的还多一层。

**规格**：

方案 A（推荐）：从禁止列表移除 rgba

1. `scripts/config.py` 的 `SVG_CONSTRAINTS['forbidden_patterns']` 中删除 `r'rgba\s*\('`
2. 理由：PowerPoint 实际支持半透明填充（`<a:solidFill>` + `<a:alpha>`），rgba 不是真正的不兼容项；SKILL.md 已经指示 LLM 使用 rgba

方案 B（备选）：改用 hex + fill-opacity

1. `template_config.json` 的 `card_bg` 改为 `{"color": "#FFFFFF", "opacity": 0.75}`
2. `build_page_skeleton.py` 输出时用 `fill="#FFFFFF" fill-opacity="0.75"`
3. 工作量更大，需同步修改 SKILL.md 规则 4

**验收**：
- `config.py` 的禁止列表与 `template_config.json` 的实际用法不矛盾
- `check_svg.py`（或未来的统一检查器）不会误报 rgba 使用

---

## 二、P1 — 应该修复（影响质量与健壮性）

### S-03: check_svg.py 从 config 读取画布尺寸

**问题**：硬编码 1280×720，未来支持其他比例时全部失效。

**规格**：

1. `check_svg.py` 启动时从 `template_config.json` 读取：
```python
canvas_w = config.get("canvas", {}).get("width", 1280)
canvas_h = config.get("canvas", {}).get("height", 720)
```
2. 所有硬编码的 `1280` / `720` 替换为变量

**验收**：
- config 中 canvas 为 960×540 → check_svg 用 960×540 检查
- config 缺失 canvas 字段 → 兜底 1280×720

---

### S-04: 降低垂直覆盖检查阈值

**问题**：`min_zones_with_content: 4` 过严，合理布局被误判为 ERROR。

**规格**：

1. `template_config.json` 的 `vertical_coverage.min_zones_with_content` 从 `4` 改为 `3`
2. `check_svg.py` 中对应检查：不足阈值时输出 WARN 而非 ERROR（exit 0 而非 exit 2）
3. SKILL.md 中注明：垂直覆盖是建议而非强制，3/4 即可
4. **生效前提与上线策略**：垂直覆盖检查在 `mode == "validate"` 分支内，默认 `mode=fast` 时不执行（详见 S-05 上线策略第 1 点）。本条的 WARN/ERROR 分级与 S-05 的影子模式同批切换，避免单独收紧卡住生成循环

**验收**：
- 3/4 区域有内容 → PASS
- 2/4 区域有内容 → WARN（不阻塞流程）
- 1/4 或 0/4 → ERROR

---

### S-05: 文字宽度估算区分 CJK/Latin

**问题**：`len × 0.6` 对 CJK 偏小（漏报溢出），对 Latin 偏大（误报溢出）。

**规格**：

1. `check_svg.py` 新增函数（复用 `build_page_skeleton.py` 的 `cjk_w` 逻辑）：
```python
def estimate_text_width(text: str, font_size: float) -> float:
    return sum(1.0 if ord(c) > 0x2E80 else 0.55 for c in text) * font_size
```
2. 替换第 92 行的 `len(text_content) * font_size * 0.6`
3. 考虑提取为共享模块 `scripts/text_utils.py`，`build_page_skeleton.py` 和 `check_svg.py` 共用

**验收**：
- 纯中文 "测试文本"（4 字）× 14px → 56px（而非 33.6px）
- 纯英文 "Test"（4 字）× 14px → 30.8px（而非 33.6px）
- 混合 "Test测试" → 按字符分别计算

**上线策略（影子模式先行）**：

> **已裁定（2026-08-27，决策记录①）**：影子期取消，CJK 估算已直接转正为唯一溢出判据——骨架生成器与检查器共用同一套 `cjk_w` 公式，骨架页数学上不可能误报（沙箱 6 页实测零误报），观察轮失去必要性。以下原文保留作背景。

1. **前置事实**：溢出/重叠/覆盖等深度检查全部在 `check_svg.py` 的 `if mode == "validate"` 分支（line 436），而 `template_config.json` 默认 `"mode": "fast"`、`batch_check.py:46` 从 config 读取——**默认流水线不执行这些检查**。设置面板**有**切换入口（`desktop/cowork_settings.go:515` 经 `updatePPTSkillConfig("mode", ...)` 写入已安装 skill 副本的 config），但默认 fast、普通用户不会主动切换。因此本条（及 S-04）默认只影响 validate 模式；是否把溢出/重叠挪入 fast、或 batch_check 默认 validate，需先做决策，属本条前置
2. CJK 系数 0.6 → 1.0 是大幅收紧（30 字标题 ×26px：468px → 780px）。骨架页安全（`build_page_skeleton.py` 用同一套 `cjk_w` 预换行），但**手写 SVG 页可能集中报 ERROR**，直接以 ERROR 上线会卡死生成-返工循环
3. 分阶段上线：第一轮影子模式——新估算只记日志（如 `[shadow] would-error: ...`）不改退出码；跑一轮真实项目统计误报后，再与 S-04 的 WARN/ERROR 分级同批切换为强制

---

### S-06: check_svg.py 增加 forbidden_patterns/attributes 检查

**问题**：`config.py` 定义了 `@font-face`、`rgba()`、`<g opacity>`、事件属性等禁止模式，但 `check_svg.py` 只检查 `forbidden_elements`。

**规格**：

1. **前置**：在 `template_config.json` 的 `rules` 节点下新增两个字段。当前该文件只有 `forbidden_elements`（5 项：filter/feDropShadow/pattern/mask/foreignObject），没有 `forbidden_patterns` 和 `forbidden_attributes`。`check_svg.py` 的 `load_config()` 硬编码了相同的 5 项默认值（line 41-49），合并 `--config` 后覆盖。新增字段后 check_svg 自动读取：
```json
"rules": {
  "forbidden_elements": ["filter", "feDropShadow", "pattern", "mask", "foreignObject"],
  "forbidden_patterns": ["@font-face", "@import", "<g\\s+opacity", "on\\w+="],
  "forbidden_attributes": ["class", "onclick", "onload", "onmouseover"]
}
```
2. `check_svg.py` 从 `template_config.json` 读取新增字段：
```python
# forbidden_patterns
for pat in config.get("rules", {}).get("forbidden_patterns", []):
    if re.search(pat, svg_content):
        violations.append(f"forbidden pattern: {pat}")

# forbidden_attributes
for elem in root.iter():
    for attr in config.get("rules", {}).get("forbidden_attributes", []):
        if attr in elem.attrib:
            violations.append(f"forbidden attribute {attr} on <{elem.tag}>")
```
3. 违规统一为 WARN 级别（不阻塞，但记录）
4. **`id` 不加入 forbidden_attributes**：native 模式的动画编组按 svg_id 寻址（animations.json 的 `groups` 以元素 id 为键，`pptx_builder.py` `_build_sequence_targets` 按 id 分组），渐变 / `<use>` 引用也依赖 id。WARN 虽不阻塞，但返工循环中的 agent 会把 WARN 当待办"修"掉——删 id 即断动画/渐变。真正的兼容性问题是 `<style>` + id 的 CSS 选择器组合；若要管应检查 `<style>` 元素本身，而非警告 id 属性

**验收**：
- SVG 含 `class="title"` → WARN "forbidden attribute class"
- SVG 含 `id="chart1"`（无 `<style>`）→ 不告警（动画/渐变寻址依赖 id）
- SVG 含 `<g opacity="0.5">` → WARN "forbidden pattern"
- SVG 含 `<filter>` → ERROR（forbidden_elements 仍为 ERROR）

---

### S-07: fix_svg.py XML 修复策略优化

**问题**：解析失败时从末尾逐行删除，可能丢失大量有效内容。

**规格**：

1. 在暴力删除之前，增加针对性修复步骤：
   - `&`（非 `&amp;`/`&lt;`/`&gt;`/`&quot;`/`&apos;`）→ `&amp;`
   - 未闭合的 `<text>` / `<tspan>` → 追加闭合标签
   - 属性值中的未转义引号 → 转义
2. 注意：`<br>` → `<br/>` 的修复已存在于 `fix_svg.py:52-53`（无条件执行），不需要重复添加
3. 每步修复后尝试 `ET.fromstring()`，成功即返回
4. 所有针对性修复失败后，才使用暴力截断（保留现有逻辑作为兜底）
5. 暴力截断时，记录被删除的行数，输出 WARN

**验收**：
- SVG 含 `A&B` → 修复为 `A&amp;B`，不丢内容
- SVG 含未闭合 `<text>` → 追加 `</text>`，不丢内容
- 无法修复的 XML 错误 → 兜底截断 + WARN 日志

---

### S-08: drawingml_converter.py skew 变换警告

**问题**：`parse_transform()` 静默丢弃 `skewX`/`skewY`，SVG 渲染错误无提示。

**规格**：

1. `parse_transform()` 检测到 skew 变换时，通过 `trace_events` 机制记录警告：
```python
if "skewX" in transform_str or "skewY" in transform_str:
    trace_events.append({"type": "skew_dropped", "transform": transform_str})
```
2. 转换完成后，如果有 skew_dropped 事件，在输出摘要中打印 WARN
3. 不阻塞转换（skew 在 PPTX 中确实难以表达），但让用户知道

**验收**：
- SVG 含 `transform="skewX(20)"` → 转换完成 + WARN "skew transform dropped"
- 无 skew → 无额外输出

---

### S-09: embed_images.py 支持 xlink:href

**问题**：正则只匹配 `href="..."`，不匹配 `xlink:href="..."`。

**规格**：

1. `embed_images.py` 的 pattern 改为同时匹配两种写法：
```python
pattern = r'(?:xlink:)?href="(?!data:)([^"]+\.(png|jpg|jpeg|gif|webp|svg))"'
```
2. 替换时保持原始属性名（`href` 还是 `xlink:href`）
3. 同时解决 .svg 扩展名支持

**验收**：
- `<image xlink:href="photo.jpg"/>` → 内联为 base64
- `<image href="diagram.svg"/>` → 内联为 base64
- `<image href="data:image/png;base64,..."/>` → 跳过（已是内联）

---

### S-10: SVG 文件名约定统一

**问题**：各脚本匹配模式不一致，旧格式文件被遗漏。`batch_check.py` 和 `qa_compare.py` 只 glob `slide_*.svg`，`svg_to_pptx` 用 `*.svg` 全匹配。

**规格**：

1. 统一为两种格式并存：
   - 新格式：`slide_NN_type.svg`（如 `slide_01_cover.svg`）
   - 旧格式：`NN_type.svg`（如 `01_cover.svg`）
2. 各脚本的 glob 模式统一为：
```python
# 同时匹配两种格式
svgs = sorted(glob.glob("slide_*.svg")) + sorted(glob.glob("[0-9]*.svg"))
# 去重（如果 slide_01.svg 和 01.svg 同时存在）
```
3. 或更优：在 `project_utils.py` 中提供统一的 `find_page_svgs(directory)` 函数，所有脚本调用它
4. **fix_svg 原地改写范围不随枚举自动扩大**：`batch_check.py` 对每页先跑 `fix_svg`（原地改写），纳入旧格式文件意味着 S-13/S-14 的破坏性改写会作用到旧项目文件。`find_page_svgs` 先只接入只读路径（`check_svg.py` / `qa_compare.py` 的枚举）；batch_check 扩大 fix 范围前打印将改写的文件清单，或加 `--fix-legacy` 显式开关

**验收**：
- 目录含 `slide_01_cover.svg` → batch_check 能找到
- 目录含 `01_cover.svg` → batch_check 能找到
- 两种格式混用 → 不重复处理

---

## 三、P2 — 可以延后（不影响核心流程）

### S-11: merge_vlm_style.py 增加非 hex 防御

**问题**：`_apply_vlm_style()` 假设 `reference-style.json` 的颜色字段为 hex，但如果上游传入非 hex 值（如 hue name），merge 会静默写入无效值。

**背景**：颜色管道实际由 Go 侧 `referenceColorPrompt`（`desktop/reference_image_vision.go:65`）独立调用 VLM 并要求输出 hex JSON，`ANALYZER_PROMPT` 的 DESIGN 段不参与颜色提取（`parse_analysis()` 只解析 verdict 首行，docstring 明确 "Never touches reference-style.json"）。当前管道正常工作，此条为防御性加固。

**规格**：

1. `merge_vlm_style.py` 的 `_apply_vlm_style()` 增加 hex 校验：
```python
def _is_hex(s):
    return isinstance(s, str) and re.match(r'^#[0-9a-fA-F]{6}$', s)

bg = vlm_style.get("background")
if bg and not _is_hex(bg):
    print(f"[WARN] non-hex background ignored: {bg}")
    bg = None
```
2. 同理校验 `accent_colors` 列表中的每个值和 `text_color`
3. 非 hex 值跳过（不写入 config），打印 WARN

**验收**：
- hex 值 → 正常合并
- 非 hex 值（如 "深蓝色"）→ 跳过 + WARN，不崩溃

---

### S-12: crop_ref_region.py 清理死代码

**问题**：`--x`/`--y` 参数绑定到 `dest="unused"`，互相覆盖且无法读取。

**规格**：删除第 66 行 `ap.add_argument("--x", "--y", ...)` ，只保留 `--rx`/`--ry`/`--rw`/`--rh`。

**验收**：`--x` 参数不再存在，`--help` 中不显示。

---

### S-13: fix_svg.py 移除 pattern/mask 元素

**问题**：docstring 声称移除 filter/pattern/mask，实际只移除 filter。

**规格**：

1. `fix_svg.py` 增加对 `<pattern>` 和 `<mask>` 元素的移除：
```python
# 收集 pattern/mask 的 id，用于后续清理引用
pattern_ids = set(re.findall(r'<pattern[^>]*\bid=["\']([^"\']+)["\']', content, re.DOTALL))
mask_ids = set(re.findall(r'<mask[^>]*\bid=["\']([^"\']+)["\']', content, re.DOTALL))

# 移除元素（含自闭合和非自闭合）
content = re.sub(r'<pattern[^>]*/>', '', content)
content = re.sub(r'<pattern[^>]*>.*?</pattern>', '', content, flags=re.DOTALL)
content = re.sub(r'<mask[^>]*/>', '', content)
content = re.sub(r'<mask[^>]*>.*?</mask>', '', content, flags=re.DOTALL)
```
2. 清理引用（两种引用处理方式不同）：
   - `mask="url(#id)"`（id ∈ mask_ids）→ 直接移除该属性（元素只是不再被遮罩，渲染安全）
   - `fill="url(#id)"` / `stroke="url(#id)"`（id ∈ pattern_ids）→ **替换为安全底色，不能删除属性**——SVG 删除 fill 后默认填充为黑色，用过 pattern 的卡片会整体变黑块进 PPT。替换值取 `template_config.json` 的 `colors.card_bg`（读不到 config 时 `#FFFFFF`）
   - 不能无差别处理所有 `url(#...)`：gradient 用同样语法，必须先收集 id 集合再匹配
3. 更新 docstring 使其与实际行为一致

**验收**：
- SVG 含 `<pattern id="p1">` → 元素被移除，`fill="url(#p1)"` 被替换为 card_bg/白色，元素**不变成黑块**
- SVG 含 `<mask id="m1">` → 元素被移除，`mask="url(#m1)"` 属性被移除
- SVG 含 `<linearGradient id="g1">` → 不受影响
- docstring 准确描述实际行为

---

### S-14: fix_svg.py 中点号替换限定范围

**问题**：`content.replace("·", ".")` 过于激进，破坏排版字符。

**规格**：

1. 限定替换范围：只在 XML 标签外部替换（不在属性值和文本内容中替换）
2. 或更优：完全移除这个替换（如果 `·` 不是 SVG 解析问题的根源）
3. 如果确实需要保留，改为只替换出现在 `d="..."` 路径数据中的 `·`（某些 SVG 编辑器用 `·` 代替小数点）

**验收**：
- SVG 文本 "A·B·C" → 保持不变
- SVG 路径 `d="M0·5 100·3"` → 如果需要，替换为 `d="M0.5 100.3"`

---

### S-15: pptx_builder.py 模板 slide 清理增加错误处理

**问题**：`drop_rel(rId)` 被 `except Exception: pass` 吞掉，失败时留孤儿 part。

**规格**：

1. 捕获具体异常而非 `Exception`：
```python
try:
    prs.part.drop_rel(rId)
except KeyError:
    pass  # rel 不存在，可忽略
except Exception as e:
    print(f"[WARN] failed to drop rel {rId}: {e}")
```
2. 或：在清理前检查 rel 是否存在，不存在则跳过

**验收**：
- 清理成功 → 无输出
- 清理失败 → WARN 日志，不静默

---

### S-16: embed_images.py 增加大图体积防护

**问题**：大图 base64 嵌入后 SVG 体积暴增，拖慢渲染和 DrawingML 转换。已有 `--max-dimension` 降采样缓解，但无硬性上限。

**规格**：

1. 嵌入前检查原文件大小，超限**不跳过，强制降采样后嵌入**：
```python
MAX_EMBED_SIZE = 5 * 1024 * 1024  # 5MB
if file_size > MAX_EMBED_SIZE:
    print(f"[WARN] {path} is {file_size/1024/1024:.1f}MB, force-downscaling before embed")
    # 逐级减小 max_dimension（2560 → 1920 → 1280 → 1024）重新编码，
    # 直到编码后体积 < MAX_EMBED_SIZE，然后照常内联
```
2. **为什么不能"保留外部引用"**：native 模式下转换器经 `_resolve_external_image`（`drawingml_elements.py:35`）从磁盘解析外部图并打入 PPTX 媒体包，断链不明显；但 legacy 模式（`-s final`，beautify 流程使用）把 SVG 原样拷进包内，相对路径 `href` 在 PPTX 包里无法解析——现代 Office 打开即断图。且 native 模式还有 `_optimize_image_for_pptx` 二次优化，跳过内联对最终 PPTX 体积并无收益
3. 降采样到 1024px 仍超限的极端情况 → 才保留外部引用，WARN 明示"该图在 legacy 模式导出时会断链"
4. 输出中记录所有被降采样图片的前后体积

**验收**：
- 1MB PNG → 正常内联
- 10MB PNG → 降采样后内联 + WARN（默认不保留外部引用）
- 降采样到 1024px 仍超限 → 保留外部引用 + 明示断链风险的 WARN
- 输出日志列出处理明细

---

### S-17: Canvas 尺寸定义清理 fallback

**问题**：`CANVAS_FORMATS` 的权威定义在 `config.py`。`project_utils.py:21` 和 `pptx_dimensions.py:14` 都 `from config import CANVAS_FORMATS`，仅 ImportError 时才落回本地副本。文档最初误判为"两处各定义一份"。

**规格**：

1. 保持 `config.py` 为唯一权威来源
2. 清理 `project_utils.py` 和 `pptx_dimensions.py` 中的 fallback 副本，改为纯 import（移除 try/except ImportError 分支）
3. 注意：不能反向以 `pptx_dimensions.py` 为权威——它已经 `from project_utils import get_project_info`，会造成循环依赖
4. 当前 fallback 副本是不完整的子集：`project_utils.py` 只有 3 个格式（ppt169/ppt43/wechat），`pptx_dimensions.py` 只有 1 个格式（ppt169）。如果 config.py 不可达，这些 fallback 会导致功能静默降级

**验收**：
- `CANVAS_FORMATS` 只在 `config.py` 定义
- `project_utils.py` 和 `pptx_dimensions.py` 纯 import，无 fallback 副本

---

### S-18: 颜色格式归一化（靶点修正）

**问题**：颜色格式不一致问题的真实靶点不是 `check_svg.py`（该脚本没有颜色比较代码，docstring 列了"配色不一致"但从未实现），而是 `svg_quality_checker.py:814` 起的 spec_lock drift 检查——它只做 `upper()` 归一化，不认短 hex（`#FFF`）和命名颜色（`white`）。但该脚本不在 SKILL.md 工作流中。

**规格**：

1. 如果 `svg_quality_checker.py` 未来被纳入工作流，增加颜色归一化：
```python
def normalize_color(c: str) -> str:
    if c.startswith("#") and len(c) == 4:  # #FFF → #FFFFFF
        return "#" + c[1]*2 + c[2]*2 + c[3]*2
    NAMED = {"white": "#FFFFFF", "black": "#000000", "red": "#FF0000"}
    return NAMED.get(c.lower(), c.upper())
```
2. 当前 SKILL 工作流不使用该脚本，此条优先级取决于是否计划启用它
3. 或：在 `text_utils.py`（见 S-05）中提供共享的颜色工具函数，未来统一调用

**验收**：
- `#FFF` 和 `#FFFFFF` 被识别为同一颜色
- `white` 和 `#FFFFFF` 被识别为同一颜色
- 仅在 `svg_quality_checker.py` 纳入工作流后生效

---

### S-19: 错误输出格式统一

**问题**：各脚本输出格式不一（JSON/纯文本/前缀文本），Agent 解析困难。

**规格**：

1. 统一为 JSON 输出（成功和失败都用 JSON）：
```json
{"status": "ok", "message": "...", "data": {...}}
{"status": "error", "message": "...", "code": "SVG_INVALID"}
```
2. 人类可读的摘要输出到 stderr，JSON 输出到 stdout
3. 分批实施：先统一 `batch_check.py`、`fix_svg.py`、`check_svg.py`（它们是流水线核心）

**硬约束**：

1. **退出码语义冻结**：desktop 的 `internal/agent/op_gate.go:198-202` 显式特判 check_svg 等验证脚本的非零退出码（0=通过 / 2=错误）。输出改 JSON 的同时**退出码一个都不能变**——任何严重度的调整（如 S-04 的降级）必须保证"阻塞级问题仍 exit 2"，否则 op_gate 的验证信号失真
2. **SKILL.md 同批更新**：SKILL.md 多处教 agent 读文本输出（batch_check 的 "ERROR pages:" 摘要、qa-report 解读、check_svg 的 `[ERROR]` 列表）。改输出的改动必须同 PR 更新 SKILL.md 对应段落，否则 agent 读不懂新输出

**验收**：
- Agent 解析 stdout → 总是合法 JSON
- 人类看 stderr → 可读的 `[OK]`/`[ERROR]` 摘要
- 退出码语义与改前完全一致（op_gate 依赖不变）

---

### S-20: 图片优化参数统一

**问题**：`embed_images.py` 硬编码 quality=85，`pptx_builder.py` 可配置。

**规格**：

1. `embed_images.py` 增加 `--quality` 参数（默认 85）
2. SKILL.md 的 svg_to_pptx 步骤将 `image_quality` 传递给 embed_images
3. 或：embed_images 从 `template_config.json` 读取 `image_quality` 字段

**验收**：
- `--quality 90` → 嵌入的 JPEG 用 quality 90
- 不传参 → 默认 85（向后兼容）

---

### S-21: 无模板 + 参考图时颜色合并断链

**问题**：`merge_vlm_style.py` 只被 `preflight.py` 调用，且该调用被"ppt-template.pptx 存在"门控（`preflight.py:53-58`）。用户给了参考图但未选模板时（image-to-PPT 的标准场景），reference-style.json 的 hex 颜色**不会**被合并进 template_config.json——SKILL.md 的"颜色权威链"（config 的 colors 即参考图真实颜色）在该场景不成立，生成只会用默认 #4472C4 配色。desktop 侧无补偿路径（`updatePPTSkillConfig` 只写 mode 这类单键，不合并颜色）。

**模拟证据**：沙箱跑 preflight（reference-style.json 携带 #EDF8FC/#0078D4/#1A3C6E，无 ppt-template.pptx）→ steps 仅 project_init，config 仍 accent=#4472C4；放入模板后重跑 → merge 执行，颜色正确合并。

**规格**：

1. `preflight.py` 把 merge_vlm_style 移出模板门控：`ppt-template-style.json` 或 `reference-style.json` 任一存在即执行 merge（`extract_template_colors` 仍保留模板门控）
2. SKILL.md Step 0 的措辞同步：颜色合并不依赖模板存在

**验收**：
- 无模板 + reference-style.json（带 hex）→ preflight 后 config colors 为参考图颜色
- 无模板 + 无参考 → config 不变（merge 输出 "no VLM style files found"）
- 有模板 → 行为不变

---

### S-22: pptx_builder icacls 子进程输出解码崩溃（中文 Windows + Python UTF-8 模式）

**问题**：`_relax_output_permissions` 的 icacls 调用（`pptx_builder.py:220`）`text=True` 未指定 encoding/errors。中文 Windows 下 icacls 输出 GBK；当 Python 处于 UTF-8 模式（`sys.flags.utf8_mode=1`，如 PYTHONUTF8=1）时 subprocess 默认按 UTF-8 解码 → 读取线程抛 `UnicodeDecodeError`：icacls 结果丢失 + 每次导出打印吓人堆栈。exit code 不受影响（异常在线程内），PPTX 正常产出——属噪音与信息丢失，非阻塞。

**模拟证据**：沙箱两次导出均在 stderr 出现 `UnicodeDecodeError: 'utf-8' codec can't decode byte 0xd2/0xcb`；独立复现 `subprocess.run(['icacls',...], encoding='utf-8')` 同错。

**规格**：

1. icacls 调用加 `errors="replace"`（显式 `encoding="utf-8", errors="replace"` 或仅加 errors）
2. 同批检查 `probe_audio_duration` 的 ffprobe 调用（`pptx_narration.py:76`）——已指定 `encoding="utf-8"`，ffprobe 的 JSON 输出 ASCII 安全，可不改；如改则统一加 `errors="replace"`

**验收**：
- 中文 Windows + UTF-8 模式下导出 → stderr 无 Traceback
- icacls 失败时 WARN 文本可读（允许含替换字符）

---

## 四、设计增强（非 bug，提升能力）

### D-01: QA 覆盖所有页面

**现状**：单图参考时只对比第一页，后续页无视觉校验。

**规格**：

1. `qa_compare.py` 的 `reference_pages()` 逻辑修改：
   - 单图参考 → 第 1 页用参考图对比，第 2-N 页用 rubric 模式（绝对标准）
   - 多图参考 → 按顺序一一对应
2. SKILL.md Step 6.5 更新：明确说明所有页面都会经过 QA

**验收**：
- 5 页 deck + 1 张参考图 → 第 1 页 compare 模式，第 2-5 页 rubric 模式
- 5 页 deck + 5 张参考图 → 每页 compare 模式

---

### D-02: font_hierarchy 结构化

**现状**：`template_config.json` 的 `font_hierarchy` 是自然语言字符串，不被代码解析。

**规格**：

1. 在 `template_config.json` 中增加结构化字段：
```json
"font_sizes": {"title": 26, "card_title": 18, "body": 14}
```
2. `build_page_skeleton.py` 从 config 读取 `font_sizes` 作为默认值（替代硬编码）
3. 保留 `font_hierarchy` 字符串给 LLM 作参考（不删除）

**验收**：
- config 的 `font_sizes.title` 为 30 → 骨架默认字号 30
- config 缺失 `font_sizes` → 兜底 26/18/14

---

### D-03: page type 元数据嵌入 SVG

**现状**：check_svg.py 靠文件名猜页面类型，LLM 手写的 SVG 无法识别。

**规格**：

1. `build_page_skeleton.py` 在 SVG 中嵌入元数据：
```xml
<metadata>
  <ppt-auto page-type="cover" slide-number="1" layout="cards"/>
</metadata>
```
2. `check_svg.py` 优先从 `<metadata>` 读取 page-type，文件名检测作为兜底
3. SKILL.md 指示 LLM 手写 SVG 时也嵌入 metadata

**验收**：
- 骨架生成的 SVG → 含 `<metadata>` 标签
- LLM 手写的 SVG（有 metadata）→ check_svg 正确识别类型
- LLM 手写的 SVG（无 metadata）→ 降级到文件名检测

---

### D-04: 模板模式 QA 背景色保障

**现状**：模板模式 SVG 无背景，不同渲染器表现不同（白/黑/透明），QA 误报。

**规格**：

1. `qa_compare.py` 的 `_deck_background()` 增加 fallback 链：
   - 优先读 `template_config.json` 的 `colors.background`
   - 如果为空，读 `reference-style.json` 的顶层 `background` 字段（注意：不是 `colors.background`，该文件是扁平结构）
   - 如果仍为空，默认 `#FFFFFF`
2. `_flatten_alpha()` 始终用确定的背景色合成，不依赖渲染器默认行为

**验收**：
- config 有 background → 用它
- config 无 background + reference-style 有 → 用它
- 都没有 → 用白色

---

### D-05: image-to-ppt 结构化中间表示

**现状**：VLM 返回 4 段自由文本，LLM 靠理解描述生成 SVG，位置全靠猜。

**规格**：

扩展 `reference-style.json`，增加结构化区域信息：

```json
{
  "description": "VLM 的 4 段描述（保留作为 LLM 上下文）",
  "regions": [
    {"id": "r1", "type": "text", "bbox": [100, 50, 400, 80], "content": "标题", "font_size_est": 36},
    {"id": "r2", "type": "card", "bbox": [50, 150, 580, 400]},
    {"id": "r3", "type": "image", "bbox": [600, 100, 1200, 600]}
  ],
  "background": "#EDF8FC",
  "accent_colors": ["#1a3c6e", "#0078D4"]
}
```

**实现路径**（需要 Go 侧配合）：

1. 当前 Go 侧 `referenceColorPrompt` 只提取颜色，`ANALYZER_PROMPT` 输出的 LAYOUT 段是 markdown 文本，Go 侧原样存入 `reference-style.json` 的 `description` 字段，不做结构化解析
2. 要得到结构化 regions，需要以下之一：
   - **方案 A**：Go 侧增加第二次 VLM 调用（类似 referenceColorPrompt），专门要求输出 regions JSON
   - **方案 B**：扩展 ANALYZER_PROMPT 的 LAYOUT 段，要求 VLM 在 markdown 中嵌入 JSON 代码块，Go 侧用 regex 提取
   - **方案 C**：Python 侧增加 `parse_regions.py`，从 description 文本中用 LLM 提取 regions（第三次调用，成本最高）
3. 示例中的 `colors` 嵌套结构需改为扁平结构（与现有 `reference-style.json` 的 `background` / `accent_colors` 顶层字段一致）

**验收**：
- VLM 返回 regions → reference-style.json 含 bbox
- VLM 不返回 regions → 降级为纯文本描述（向后兼容）
- SKILL.md 指示 LLM 参考 regions 定位元素
- Go 侧改动范围明确（不超出 reference-style.json 写入逻辑）

---

### D-06: 骨架生成器支持复合布局

**现状**：7 种类型都是整页单一布局，复合布局靠 LLM 手写 SVG。

**规格**：

1. `build_page_skeleton.py` 增加 `--region x,y,w,h` 参数，只生成指定区域的内容
2. **复杂度说明**：骨架生成器全部使用全局常量绝对坐标（`CANVAS_W`、`MARGIN_X` 等），`--region` 需要整体坐标平移——不是加个参数就行，需要：
   - 将 region 的 (x, y) 作为偏移量加到所有元素坐标上
   - 将 region 的 (w, h) 替换 `CANVAS_W` / `CANVAS_H` 用于该区域内的布局计算
   - 处理 region 内的 margin 缩放（region 越小，margin 是否等比缩小？）
3. SKILL.md 给出组合规则：
   - 上 60% 时间线 + 下 40% 表格
   - 左 40% 大数字 + 右 60% 指标卡
4. LLM 在 pages.json 中指定 `"layout": "composite"`, `"regions": [...]`

**验收**：
- `--region 0,0,1280,432` → 只生成上半部分内容，坐标正确
- 两个 region 调用组合 → 不重叠、不留空隙
- region 内的元素不超出 region 边界

---

## 五、优先级总览

| 序号 | 编号 | 描述 | 优先级 | 类型 |
|------|------|------|--------|------|
| 1 | S-01 | autofit 接入骨架 | **P0** | Bug |
| 2 | S-02 | rgba 规则统一 | **P0** | Bug |
| 3 | S-03 | check_svg 读画布尺寸 | P1 | Bug |
| 4 | S-04 | 垂直覆盖阈值降低 | P1 | Bug |
| 5 | S-05 | 文字宽度 CJK/Latin | P1 | Bug |
| 6 | S-06 | check_svg 检查 patterns | P1 | Bug |
| 7 | S-07 | fix_svg 修复策略优化 | P1 | Bug |
| 8 | S-08 | skew 变换警告 | P1 | Bug |
| 9 | S-09 | embed_images xlink:href | P1 | Bug |
| 10 | S-10 | SVG 文件名统一 | P1 | 一致性 |
| 11 | S-11 | merge_vlm_style 非 hex 防御 | P2 | 加固 |
| 12 | S-12 | crop_ref_region 死代码 | P2 | Bug |
| 13 | S-13 | fix_svg 删 pattern/mask | P2 | Bug |
| 14 | S-14 | fix_svg 中点号限定 | P2 | Bug |
| 15 | S-15 | pptx_builder 清理错误处理 | P2 | Bug |
| 16 | S-16 | embed_images 体积防护 | P2 | Bug |
| 17 | S-17 | Canvas 尺寸清理 fallback | P2 | 一致性 |
| 18 | S-18 | 颜色格式归一化 | P2 | 一致性 |
| 19 | S-19 | 错误输出格式统一 | P2 | 一致性 |
| 20 | S-20 | 图片优化参数统一 | P2 | 一致性 |
| 21 | S-21 | 无模板+参考图颜色合并断链 | **P1** | Bug |
| 22 | S-22 | icacls 子进程解码崩溃 | P2 | Bug |
| 23 | D-01 | QA 覆盖所有页面 | P1 | 增强 |
| 24 | D-02 | font_hierarchy 结构化 | P2 | 增强 |
| 25 | D-03 | page type 元数据 | P2 | 增强 |
| 26 | D-04 | QA 背景色保障 | P2 | 增强 |
| 27 | D-05 | 结构化中间表示 | P2 | 增强 |
| 28 | D-06 | 复合布局支持 | P2 | 增强 |

---

## 六、附录：规则体系现状

ppt-auto 内部存在**三处独立的规则定义**，内容不一致，且消费关系混乱：

| # | 定义位置 | 内容 | 被谁消费 |
|---|---|---|---|
| 1 | `template_config.json` → `rules.forbidden_elements` | filter/feDropShadow/pattern/mask/foreignObject | `check_svg.py`（SKILL 工作流使用） |
| 2 | `scripts/config.py` → `SVG_CONSTRAINTS` | forbidden_elements + forbidden_patterns + forbidden_attributes（含 @font-face/rgba/@import/g opacity/事件属性等） | `Config.validate_svg_element()`（line 656）读取它，但全仓无代码调用该方法——**死代码** |
| 3 | `scripts/svg_quality_checker.py` → 硬编码 | mask/style/class/rgba/g opacity 等（与 config.py 高度重合但各自维护） | 自身内部使用（不在 SKILL 工作流中） |

**问题**：
1. 三处定义的禁止元素列表各不相同
2. `config.py` 的 `SVG_CONSTRAINTS` 是死代码——`Config.validate_svg_element()`（line 656）读取它，但全仓无代码调用该方法
3. `svg_quality_checker.py` 不从 config.py 导入任何东西，完全硬编码自己的检查列表（~20 项），与 config.py 高度重合但各自维护
4. `config.py` 禁止 `rgba()`，但 SKILL.md 规则 4 明确指示 LLM 使用 rgba，`template_config.json` 也定义了 rgba 值
5. `svg_quality_checker.py` 的检查范围是 `template_config.json` 的超集（多了 style/textPath/script/animate*/iframe/@font-face/rgba/事件属性/opacity 等），但不在 SKILL 工作流中——这些更严格的检查从未生效

**建议**：统一为一套规则，由 `template_config.json` 承载（因为它已经被 merge_vlm_style.py 动态修改），`check_svg.py` 读取并执行。删除 `config.py` 的 `SVG_CONSTRAINTS`（死代码）。如果未来启用 `svg_quality_checker.py`，让它也从 `template_config.json` 读取规则。此建议已纳入 S-02 和 S-06 的规格中。

**附带发现**：`svg_quality_checker.py` 是唯一从 `project_utils`（而非 `config.py`）导入 `CANVAS_FORMATS` 的消费者。如果 config.py 不可达，它拿到的是 project_utils 的 fallback 副本（只有 3 个格式），而非完整定义。

---

## 七、核对记录

> 本节记录规格条目的代码核对状态，不作为实施依据。

| 编号 | 核对结果 | 核对方式 |
|------|---------|---------|
| S-01 | ✅ autofit 只输出 stdout，preflight 不调用它；需先定义落盘路径 | agent 读 autofit_fontsize.py + preflight.py |
| S-02 | ✅ config.py 禁 rgba，SKILL.md 规则 4 指示用 rgba，template_config.json 定义 rgba——三层矛盾 | agent 读 config.py + SKILL.md + template_config.json |
| S-03 | ✅ check_svg.py 硬编码 1280×720，不读 config 的 canvas 字段 | agent 读 check_svg.py |
| S-04 | ✅ min_zones_with_content: 4 过严 | agent 读 template_config.json + check_svg.py |
| S-05 | ✅ 文字宽度用 len×0.6，不区分 CJK/Latin | agent 读 check_svg.py:92 |
| S-06 | ✅ check_svg 只检查 forbidden_elements；template_config.json 当前无 forbidden_patterns/attributes 字段，需先新增 | agent 读 check_svg.py + template_config.json |
| S-07 | ✅ `<br>` → `<br/>` 已存在于 fix_svg.py:52-53（规格已移除）；缺的是 & 转义和未闭合标签修复 | agent 读 fix_svg.py |
| S-08 | ✅ parse_transform() docstring 承认 "shear silently collapses"；trace_events 机制存在于 drawingml_context.py:62 | agent 读 drawingml_converter.py + drawingml_context.py |
| S-09 | ✅ 正则只匹配 href，不匹配 xlink:href | agent 读 embed_images.py |
| S-10 | ✅ batch_check 和 qa_compare 只 glob slide_*.svg，svg_to_pptx 用 *.svg | agent 读多个脚本 |
| S-11 | ✅ _apply_vlm_style() 期望 hex，无非 hex 防御 | agent 读 merge_vlm_style.py |
| S-12 | ✅ --x/--y 绑定到 dest="unused"，死代码 | agent 读 crop_ref_region.py:66 |
| S-13 | ✅ fix_svg 只删 filter，不删 pattern/mask；docstring 声称三者都删 | agent 读 fix_svg.py |
| S-14 | ✅ content.replace("·", ".") 全局替换 | agent 读 fix_svg.py |
| S-15 | ✅ drop_rel 被 except Exception: pass 吞掉 | agent 读 pptx_builder.py |
| S-16 | ✅ 无 base64 体积限制；已有 --max-dimension 降采样缓解 | agent 读 embed_images.py |
| S-17 | ✅ config.py 是权威；project_utils 和 pptx_dimensions 是 import+fallback；fallback 是不完整子集 | agent 读 config.py + project_utils.py + pptx_dimensions.py |
| S-18 | ✅ check_svg 无颜色比较代码（docstring 写了但没实现）；实际颜色检查在 svg_quality_checker.py:811-946，不在 SKILL 工作流 | agent 读 check_svg.py + svg_quality_checker.py + SKILL.md |
| S-19 | ✅ 各脚本输出格式不一 | agent 读多个脚本 |
| S-20 | ✅ embed_images 硬编码 quality=85 | agent 读 embed_images.py |
| S-21 | ✅ preflight 只在有模板时调 merge（preflight.py:53-58 门控）；沙箱实测无模板+参考图 config 未合并 | 沙箱模拟 + agent 读 preflight.py |
| S-22 | ✅ icacls 调用 text=True 无 encoding（pptx_builder.py:220），UTF-8 模式下读取线程解码崩溃（沙箱两次复现） | 沙箱模拟 + 独立复现 |
| D-01 | ✅ 单图参考时第 2-N 页完全不入 QA 配对 | agent 读 qa_compare.py |
| D-02 | ✅ font_hierarchy 是自然语言字符串 | agent 读 template_config.json |
| D-03 | ✅ check_svg 靠文件名猜 page type | agent 读 check_svg.py |
| D-04 | ✅ reference-style.json 的 background 是顶层字段，不是 colors.background | agent 读 reference_image_vision.go |
| D-05 | ✅ Go 侧原样存 description，regions 需 Go 侧改动或独立 VLM 调用 | agent 读 reference_image_vision.go + classify_reference.go |
| D-06 | ✅ 骨架用全局绝对坐标常量（CANVAS_W/MARGIN_X 等），--region 需整体坐标平移 | agent 读 build_page_skeleton.py |
| 附录 | ✅ 三处独立定义：template_config.json（活跃）/ config.py SVG_CONSTRAINTS（死代码）/ svg_quality_checker.py（独立硬编码） | agent grep + 读三个文件 |

### 兼容性核对（修订 2）

> 面向两不变量（生成链路不破坏、PPTX 在 PowerPoint 真实可用）的转换器行为核对，结论已写入对应规格。

| 项 | 结论 | 证据 |
|----|------|------|
| rgba → DrawingML alpha | ✅ native 转换器完整支持，S-02 解禁安全 | `drawingml_utils.py:335-390`（#RRGGBB/#RGB/rgb()/rgba() 解析）+ `drawingml_styles.py:19-22`（`<a:srgbClr>` + `<a:alpha>`） |
| 外部图片引用 | ✅ native 模式经 `_resolve_external_image` 从磁盘读取并打入媒体包；⚠️ legacy 模式原样拷贝 SVG，包内相对路径断链（S-16 据此改强制降采样） | `drawingml_elements.py:35-53, 2140-2170` |
| 转换默认模式 | ✅ svg_output 源走 native（`-s final` 才走 legacy） | `pptx_cli.py:102-105` |
| 退出码依赖 | ✅ desktop op_gate 按 exit code 特判验证脚本——输出格式可改、退出码不可改 | `internal/agent/op_gate.go:198-202` |
| 元素 id 依赖 | ✅ 动画编组按 svg_id 寻址、渐变/`<use>` 引用依赖 id——id 不应进 forbidden_attributes（S-06） | `pptx_builder.py` `_slide_config` / `_build_sequence_targets`（按 svg_id 分组） |
| `<metadata>` 容忍性 | ✅ 转换器对未知元素按 skip 处理，D-03 嵌入无害通过 | `drawingml_converter.py` 元素分派 |
| 深度检查默认关闭 | ⚠️ 溢出/重叠/覆盖检查全在 `mode=="validate"` 分支，config 默认 fast——S-04/S-05 生效前提（见 S-05 上线策略） | `check_svg.py:436`、`template_config.json:45`、`batch_check.py:46` |

### 模拟运行记录（2026-08-27）

> 沙箱实测：独立 USERPROFILE + skill 目录副本（不触碰真实 `~/.hiq` 与仓库文件）。场景：参考图（reference-style.json 带 hex 颜色）→ 6 页 deck（cover/toc/section/cards/columns/ending）→ autofit → build_page_skeleton → batch_check → qa_compare → svg_to_pptx，无模板与有模板各跑一轮。

| # | 发现 | 结论 |
|---|------|------|
| M-1 | 无模板+参考图：preflight 跳过 merge，config 保持默认 #4472C4；有模板时 merge 正常（参考色正确覆盖） | → 新增 **S-21**（P1） |
| M-2 | autofit 输出 24/15/12，骨架页实际用 26/18/14 | S-01 live 实锤 |
| M-3 | 含未转义 `&`/`<` 的 SVG：fix_svg 截断到只剩背景 rect（5 段文字全丢，输出仅 119 chars，无 WARN）；该空页在 fast 模式 batch_check **[OK] 通过、exit 0** → 会直接进最终 PPTX | S-07 + mode=fast 的组合故障链实锤；中文内容 "R&D"、"A<B" 是高频触发词 |
| M-4 | validate 模式对 x=1000 起、20 个中文字 ×20px（真实右缘 1400 > 1280）不报溢出——len×0.6 估宽仅 240px | S-05 漏报 live 实锤 |
| M-5 | 导出时 icacls 读取线程 UnicodeDecodeError（GBK 输出 × Python UTF-8 模式），exit 0、PPTX 正常 | → 新增 **S-22**（P2） |
| M-6 | qa_compare 在检查 VLM 可用性**之前**渲染全部页（无 key 时白渲染 6 页后才跳过） | 建议随 D-01 顺手把 VLM 可用性检查提到渲染循环之前 |
| M-7 | 模板场景全链路正常：extract→merge（参考色覆盖、模板字体 Calibri 提取）→骨架无背景 rect→check 通过→导出继承 Title Slide layout，6 页文字完整 | ✅ 有模板主链路健康 |
| M-8 | validate 模式对 3 列卡片布局报 3 条"对齐偏差"WARN（多栏布局固有噪音，exit 0 不阻塞） | 记录在案，可在 S-04/S-06 落地时一并评估豁免 |
| M-9（修正） | 前轮"desktop 无写 mode 入口"结论**有误**：`cowork_settings.go:515` 经 `updatePPTSkillConfig("mode",...)` 写已安装副本的 config——S-05 上线策略措辞已修正 | 复查 grep 修正 |

### 实施记录（2026-08-27 批次一）

**已实施 22 项**：S-01~S-17、S-20、S-21、S-22、D-01、D-02、D-04，外加 M-6（qa_compare 的 VLM 可用性检查移到渲染前）与 SKILL.md 同步（autofit 落盘约定 `{project}/output/autofit_result.json`、`--autofit` 用法、垂直覆盖 3/4 说明）。`release.go` SkillVersion 45→46 强制已安装副本刷新。

**延后**：S-18（svg_quality_checker 不在工作流，待处置决策）、S-19（JSON 输出需与 SKILL.md/op_gate 退出码语义同批，单独批次）、S-10 的 batch_check 部分（按规格 find_page_svgs 只接入 check/QA 只读路径）、D-03（metadata 嵌入）、D-05（需 Go 侧配合）、D-06（复合布局坐标平移）。

**S-05 按"影子模式"实施**：CJK 估算溢出以 `[shadow]` 前缀的 WARN 呈现（不阻塞、exit 0），旧 len×0.6 公式仍是 error 判定依据。观察一轮真实项目后切换强制 = 把 shadow WARN 升级为 error（一行改动），与 S-04 分级同批。

**实施中发现并修复的 2 个新缺陷**：

1. **fix_svg 的 `rfind("/>")` 贪心捷径是 M-3 大量丢内容的真正放大器**——它只会命中最后一个自闭合标签（通常是背景 rect），其后内容整体丢弃。已删除该捷径：兜底改为"从尾向头找最长可解析前缀，每次尝试前先补齐未闭合的 text/tspan 并转义裸 &"。M-3 用例实测：从静默丢 5 段文字 → 保住 4 段、只丢 1 行（`A<B` 的裸 `<` 无法安全自动修复）+ WARN 记录。
2. **S-21 修复引出的连锁缺陷**：merge 原把 reference-style.json 的 `background_type:"solid"` 一并写入 config，导致"无模板+参考图"场景被翻转成模板模式——骨架不再画背景、check_svg 报"禁止画全屏背景"、最终 PPTX 背景变白。已修复：`_apply_vlm_style` 新增 `allow_background_type` 参数，只有 ppt-template-style.json（真实 .pptx 模板的视觉提取）可写 background_type；参考图只贡献颜色值。

**沙箱回归记录**：T1 无模板颜色合并（bg=#EDF8FC/accent=#0078D4）✅ / T2 autofit 24/15/12 进入骨架 ✅ / T3 坏 SVG 保 4/5 段 ✅ / T4 pattern→card_bg 底色、mask 移除、gradient 保留 ✅ / T5 CJK 影子 WARN ✅ / T6 class+g-opacity 告警、id 不告警 ✅ / T8 非 hex 跳过+WARN ✅ / T9 `--x` 移除 ✅ / T10 960×540 画布边界生效 ✅ / T11 双命名枚举去重 ✅ / T12 垂直覆盖 2/4→WARN ✅ / M-6 无 key 不再白渲染 ✅ / S-16 11.3MB 图强制降采样嵌入（4.8MB）✅ / 无模板+有模板两轮全链路 PPTX 产出正常、导出零 Traceback ✅ / 仓库侧导入冒烟（svg_quality_checker/batch_check/pptx_dimensions/find_page_svgs）✅

### 实施记录（2026-08-27 批次二）

**收尾 6 项**：

1. **S-18 + S-05③**：新建 `scripts/text_utils.py` 共享模块（`cjk_char_units`/`estimate_text_width`/`normalize_color`），check_svg 与 build_page_skeleton 改为共用（消除两份 cjk 系数副本）；svg_quality_checker 的 spec_lock 漂移检查接入 `normalize_color`——短 hex（#FFF==#FFFFFF）、8 位 hex（丢 alpha）、命名色（white==#FFFFFF）两侧归一化，SVG 扫描正则同时捕获 hex 与命名色
2. **S-10 全部**：batch_check 增加 `--fix-legacy` 显式开关——默认旧格式 `NN_type.svg` 只在 JSON 的 `skipped_legacy` 里列出（不被 fix_svg 原地改写），传开关才纳入 fix+check
3. **S-19**：check_svg 拆出纯计算入口 `run_check()`（返回 dict 不打印）；CLI 与 batch_check 的输出契约为 **stdout=JSON / stderr=人类可读报告**，batch_check 输出单 JSON 对象（含 failed/skipped_legacy/results）。**退出码语义冻结未动**（0/2，op_gate 依赖）；fix_svg 返回结果 dict（含 truncated/dropped_lines），CLI 同样 JSON stdout。SKILL.md 对应段落已同步（"解析 JSON 而不是读文本"）
4. **D-03**：骨架输出在 `<svg>` 后首行嵌入 `<metadata><ppt-auto page-type="..." slide-number="..."/></metadata>`；check_svg 新增 `_read_page_type()` 优先读元数据、文件名猜测降为兜底；转换器 `_NON_VISUAL_TAGS` 已含 metadata（无需改动，已核实）；SKILL.md 指示手写 SVG 也嵌入
5. **D-06**：`--region x,y,w,h`（CLI 全局或 pages.json 每页 `"region"`）——整页内容经 `translate+scale` 线性变换映射进目标区域（生成器布局逻辑零改动，多块拼装天然不重叠不留缝），区域模式不画背景；SKILL.md 给出两块拼装的完整组合规则（拼装页跑 fast 检查，跨块深度检查按原始坐标会误报）
6. **D-05**：两侧 analyzer prompt（Go `pptRefAnalyzerPrompt` + Python `ANALYZER_PROMPT`，保持同步契约）的 LAYOUT 段新增可选 fenced json regions 块（0-1280×0-720 粗略 bbox，4-8 区域）；Go 侧 `extractRegionsJSON()` 从 description 提取写入 reference-style.json 顶层 `regions` 字段（`json.RawMessage`，VLM 不输出时整链降级为纯文本，向后兼容）；SKILL.md 指示 LLM 优先参考 bbox 定位

**批次二实施中修正的缺陷**：D-05 初版 prompt 示例给的是对象 `{"regions":[...]}` 而提取正则匹配裸数组 `[{...}]`——VLM 照示例输出必然提取失败。沙箱正则测试抓到后统一改为裸数组（两侧 prompt + 验证含 3 区域用例与纯文本降级用例）。

**批次二沙箱回归**：T13 S-19（check_svg stdout JSON+stderr 报告、fix_svg JSON 含 truncated、batch 单对象）✅ / T14 S-10（默认 skipped_legacy 列出、--fix-legacy 纳入 7 页）✅ / T15 D-03（骨架首行 metadata；同内容同文件名，cover 元数据 0 错 vs cards 元数据报内容不足——元数据优先级生效）✅ / T16 D-06（上下两块 transform 正确 0.6/0.4、双文件合法 XML）✅ / T17 S-18（normalize_color 5 用例）✅ / T18 D-05（正则提取 3 区域 + 纯文本降级 + Go 模式串一致性）✅ / 全链路两轮（无模板+有模板）batch ok、导出 exit 0 零 Traceback、PPTX 6 页内容/layout 正确 ✅

**遗留决策（非代码）**：① S-05 影子模式观察一轮后把 `[shadow]` WARN 升为 error（一行改动）；② 深度检查默认启用策略（fast/validate 决策，见 S-05 上线策略）；③ svg_quality_checker.py 的最终处置（纳入工作流或删除）——S-18 的归一化已就位，两条路都不阻塞。

### 决策记录（2026-08-27，三项全部裁定）

**决策 ① S-05 影子转正——CJK 估算成为唯一溢出判据**（不再等观察轮）

理由：骨架生成器用同一套 `cjk_w` 公式预换行，骨架页在数学上不可能被新公式误报（T21 实测 6 页零误报）；1.0em 系数对 CJK 是物理事实（全角字符即 1em 宽），旧的 0.6 不是"保守"而是错误。影子期本为防误报，而唯一的系统性误报源（骨架自身）已被同源公式排除。改动：`parse_text_elements` 的 `width` 直接取 `estimate_text_width`，删除 `width_cjk` 影子分支。

**决策 ② fast/validate 分层——硬底线三项进 fast**

fast 模式新增（始终执行）：内容硬底线（非稀疏页 <5 段文字、稀疏页 <3 → ERROR）、文字超出画布、文字压字。密度建议/垂直与空间覆盖/对齐/间距/多样性留在 validate。理由：M-3（空页静默出厂）与 M-4（CJK 溢出漏报）都发生在默认 fast 流水线里，这三项是纯数学毫秒级检查、针对的是真缺陷而非风格偏好——"快"不应等于"不设底线"。配套：`--region` 拼装块在 metadata 里带 `region="1"`，内容底线对其豁免（它是部分页）；SKILL.md 的 fast/validate 描述已同步。风险已验证：骨架 6 页 fast 零误报，双轮全链路 batch ok。

**决策 ③ 规则体系收敛——删 config.py 死代码，svg_quality_checker 定位为可选工具**

`SVG_CONSTRAINTS` + `validate_svg_element` + export 条目从 config.py 删除（全仓零消费者，grep 复核；留注释指路 template_config.json）。`svg_quality_checker.py` **保留**（2106 行自包含审计器，spec_lock 漂移检查独此一家，S-18 归一化已接入），定位为可选手动深度审计工具，已写入 SKILL.md 脚本一览（明确标注不在生成流水线内）。至此规则体系收敛为：`template_config.json` → `rules`（活跃，check_svg 执行）+ svg_quality_checker 自带审计规则（可选）两处，config.py 不再承载规则。

**决策批沙箱回归**：T19 M-4 溢出用例在 fast 报 ERROR ✅ / T20 稀疏页 fast 报"内容严重不足" ✅ / T21 骨架 6 页 fast 零误报（转正关键风险排除）✅ / T22 region 块带 `region="1"` 元数据且豁免生效 ✅ / T23 config.py 手术后导入链完整、SVG_CONSTRAINTS 已删、CANVAS_FORMATS 8 项无损 ✅ / 无模板+有模板双轮全链路 batch ok、导出 exit 0 零 Traceback、PPTX 正常 ✅

**至此全部 28 项规格 + 3 项决策落地完毕，无遗留代码项。**
