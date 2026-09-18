# PPT 视觉增强 Spec（图片 / PDF → PPT）

> **状态**：Phase 1-6 全部已实施 ✅（端到端链路接通；merge/字号/触发均测试通过）
> **范围**：让 hiq 能"给一张 PPT 图片（或扫描 PDF）→ 用 VLM 理解 → ppt-auto 生成一页类似的"
> **基础**：基于多轮讨论 + ppt-master 调研 + ppt-auto config 体系深调
> **不属本 spec**：PPT 文件（.pptx）仿写/扩写 —— 那走结构化解析（readPPTX 增强），不靠 VLM，另立 spec

---

## 一、目标与场景

### 核心场景（本 spec 聚焦）
**用户给一页 PPT 图片**（截图/参考图）→ hiq 用 VLM 理解它怎么做的（文字/布局/密度/颜色/字号比例）→ **ppt-auto 生成一页类似的**（参考生成，非像素复刻）。

### 扩展场景（核心通后接上）
**扫描/图片式 PDF** → 切成每页图 → 每页走核心场景的 VLM 理解 → ppt-auto 逐页重画。

### 非目标
- **像素级复刻**（要 exact 坐标/hex/字号 px）—— VLM 做不到，本 spec 不追求，只做"类似的"
- **PPT 文件仿写** —— 靠 readPPTX 结构化解析（另立 spec）
- **图标/插画/照片的精确还原** —— 自绘做不到，接受限制（见 §九）

---

## 二、核心原则：VLM 怎么用（一路确立，不可违背）

| 原则 | 说明 |
|---|---|
| **不用 image_understand** | 它只服务主会话看用户上传图（见 image_understand 定位 spec）。PPT 场景不调它 |
| **desktop 层直接调 `builtin.CallVLM`** | 仿 `ppt_template_vision.go` 模式：desktop App 方法调 CallVLM，结果落盘 JSON 给 ppt-auto 读 |
| **有结构 → 解析，无结构 → VLM** | 图片/扫描 PDF 无结构 → VLM；PPT 文件/文字 PDF 有结构 → 解析 |
| **PNG 无损，不 JPEG 强转** | 和 image_understand 保格式修复一致：PNG 直传 data URL，不二次缩放/不转 JPEG |
| **背景/模板固定** | 用用户选的 `default.pptx`，VLM 不管背景（见 §四） |

---

## 三、VLM 从参考图提取什么（4 样）

参考图（一页 PPT 图片）经 VLM，提取 4 样信息，写进 `~/.hiq/reference-style.json`：

| # | 提取项 | 用途 | 精度 |
|---|---|---|---|
| 1 | **文字内容** | 逐字转录（标题/正文/要点/标签） | 精确（verbatim） |
| 2 | **布局 + 密度** | 版式类型（封面/内容/对比/表格/图表…）+ 内容疏密（多少元素、密还是疏） | VLM 判断（**核心**） |
| 3 | **颜色** | 主色/强调色（背景色不提取——用模板的） | 读取（已有能力） |
| 4 | **字号相对比例** | 标题/正文/注释 谁大谁小、大致几倍关系 | 喂字号自适应，**不取 exact px** |

**VLM 不提取**（用户明确否决）：
- ❌ 画布比例（模板定 16:9）
- ❌ 背景（用 default.pptx）
- ❌ exact 字号 px（VLM 看不到像素，靠自适应）
- ❌ 字体 family（默认用模板字体）

---

## 四、不动项（固定，VLM 不碰）

| 项 | 值 | 来源 |
|---|---|---|
| **背景/模板** | `default.pptx`（用户选的） | 除非用户明确指定换模板，否则一律读默认模板 |
| **画布** | 16:9（1280×720 px，模板定） | ppt-auto 现状，不改 |
| **字体** | 模板字体（`fonts.family`） | 不靠 VLM 提取 |

---

## 五、字号自适应（"有判断，不瞎猜"）

字号既不能 VLM 瞎给 px（VLM 看不到像素），也不能纯硬编码。判断逻辑（有依据）：

```
字号 ramp = 模板基线 (标题26 / 卡片标题18 / 正文14)
           × 密度调节（VLM LAYOUT 段给的密度：密 → 按比例缩，疏 → 放大）
           × 保持相对比例（标题 > 卡片标题 > 正文，比例来自参考图 FORMAT 段）
```

**依据**：
- 模板基线（数值锚点，来自 config）
- VLM 给的密度（定性，可靠：偏密/偏疏）
- VLM 给的相对比例（定性：标题≈正文×2）

**不依据**：VLM 直接吐 exact px（瞎猜，禁止）。

落地：一个 `fontsize_autofit(density, ratio, baseline)` 函数，输入定性判断 + 基线，输出数值 ramp。

---

## 六、工程缺口（调研发现，必须补）

调研 ppt-auto config 体系发现三个真问题：

### 缺口 1：没有 merge 步骤（最关键）
`ppt-template-style.json`（VLM 提取的颜色）**从不被代码合并进 `template_config.json`**——全靠 SKILL.md 用自然语告诉 LLM "你读两份、颜色优先级最高"，让 LLM 自觉。grep 全仓确认无 merge 脚本。

**后果**：即使 VLM 把颜色/布局提取得再准，也没代码把它写进 config 覆盖默认。"VLM 覆盖 config" 现在是假的。

**修法**：写 merge 脚本/步骤，把 VLM 提取的颜色 + 布局判断机械写进 config（或 ppt-auto 读的中间文件），不靠 LLM 自觉。

### 缺口 2：config 字段部分硬编码
- 字号 `rules.font_hierarchy` 是字符串（"标题26/卡片18/正文14"），不是数值字段 → `check_svg.py:178` 的 26px 是魔法数
- 画布 1280×720 在 `check_svg.py:168-176` 硬编码，不读 canvas 字段
- 14 种布局散在 `layout_templates.md`（纯文本），不进 config，代码不消费

**修法**（本 spec 范围内最小集）：
- 字号：拆成结构化字段供自适应函数用（画布/布局结构化留后续）

### 缺口 3：VLM 4 段 prompt 现状只面向 PDF
`pdfPageVLMPrompt`（pdf_pages_vision.go）输出自由文本到 `~/.hiq/pdf-pages/page-N.json`，不进 config。颜色 VLM（`ppt_template_vision`）只提 6 字段颜色。

**修法**：图片路线复用 4 段 prompt 的文字/布局/格式/设计提取，但落地成结构化（`reference-style.json`）+ merge 进 config。

---

## 六.五、触发条件与分流（VLM 把关）

**触发**：前端发 PPT 制作请求，带参考文件（图/PDF）+ 意图（"图片变 PPT" / "PDF 变 PPT"）。

**分流由 VLM 判断输入的视觉性**（不靠扩展名/文件大小等写死规则）——一切都让 VLM 看：

```
参考文件（图/PDF）+ 意图
        ↓
VLM 预判断（轻量）：这输入是纯文字，还是有样式/布局？
        ├─ 纯文字（无样式/布局，只有文字）
        │     → 抽文字作材料（图: VLM OCR / PDF: doc_read）
        │     → 普通 ppt-auto（主题驱动，文字作素材）
        └─ 有样式/布局（标题样式/配色/多栏/图表/装饰）
              → 视觉流程：
                  ├─ 图  → AnalyzeReferenceImage → reference-style.json
                  └─ PDF → AnalyzePDFPages       → page-N.json
                  → ppt-auto 视觉生成（参考布局/颜色/字号，§三/§五）
```

**关键**："是否走视觉流程"由 VLM 看一眼输入决定（纯文字 vs 有样式），不靠写死规则。这样：
- 纯文字的图/PDF → 不浪费跑完整视觉流程，只抽文字作素材
- 有样式布局的 → 走视觉流程，把布局/颜色/字号也复刻

VLM 预判断是一次**轻量调用**（只问 A/B：纯文字 vs 有样式），先于重的 4 段描述。判断为"有样式"时才继续 AnalyzeReferenceImage/AnalyzePDFPages 的完整 4 段提取。

## 七、实施方案（分阶段，按优先级）

### Phase 1：merge 步骤（地基，最关键）
**做什么**：让 VLM 提取的信息真正写进 config / 驱动生成，不靠 LLM 自觉。
- 写 merge 逻辑：`reference-style.json`（图片）的颜色 + 布局判断 → 写进 ppt-auto 可消费的结构化输入
- 复用/扩展现有 `ppt-template-style.json` merge 机制（现状没有，要建）
**为何最先**：没有它，Phase 2 提取再多信息也"读取了用不上"。

### Phase 2：图片 → VLM → PPT 核心
**做什么**：单页参考图 → VLM 提取 4 样 → ppt-auto 生成一页。
- 新 `desktop/reference_image_vision.go`：`AnalyzeReferenceImage(imgPath)` → 读图 → PNG 无损 → `CallVLM`(4段prompt) → 写 `~/.hiq/reference-style.json`
- 复用 `pdfPageVLMPrompt`（pdf_pages_vision.go 已有的 4 段常量）
- ppt-auto SKILL.md 加步骤：读 `reference-style.json`，指导大纲/SVG 设计
**依赖**：Phase 1（提取了要能用）

### Phase 3：字号自适应函数
**做什么**：`fontsize_autofit(density, ratio, baseline)` → 字号 ramp。
- 输入：VLM 的密度判断 + 相对比例 + 模板基线
- 输出：标题/卡片标题/正文 字号数值
- 接入 ppt-auto 生成（覆盖 config 的硬编码字号）
**为何靠后**：前两阶段通了再加精度。

### Phase 4：PDF → 图片 前置
**做什么**：扫描 PDF → 切图 → 每页走 Phase 2 的 VLM 理解 → ppt-auto 逐页画。
- 接已写的 `pdf_to_page_images.py`（PDF→每页 PNG）+ `desktop/pdf_pages_vision.go`（每页 VLM 识别）
- 这两个文件已写好（编译通过），等 Phase 2 的 VLM 提取逻辑成熟后接上
**为何最后**：核心链（图片→PPT）通后，PDF 只是加"PDF→图"前置。

---

## 八、已有代码（写好的，待接入）

| 文件 | 干什么 | 状态 |
|---|---|---|
| `pdf_to_page_images.py`（根目录） | PDF → 每页 PNG（复用 fitz，只渲染不 OCR） | ✅ 已写，Python 语法验证通过 |
| `desktop/pdf_pages_vision.go` | 调切割脚本 + 逐页 CallVLM（含 `pdfPageVLMPrompt` 4段常量）+ 写 page-N.json | ✅ 已写，desktop 模块 go vet 通过 |
| `desktop/ppt_template_vision.go` | 模板配色 VLM（`builtin.CallVLM`，写 `ppt-template-style.json`） | ✅ 已有（hiq 原生），Phase 2 复用其模式 |

**关键复用**：`pdfPageVLMPrompt`（4 段：CONTENT/LAYOUT/FORMAT/DESIGN）是图片版和 PDF 版共用的 VLM 提取逻辑，Phase 2 直接用。

---

## 九、生成图形的限制（接受，不追求）

ppt-auto 生成的图形受"调色板 + 元素类型白名单（rect/text/line/circle/path/polygon）"约束，靠 LLM 读布局参考自由发挥。参考图图形的复刻能力：

| 图形类型 | 复刻可行性 |
|---|---|
| 几何形状（卡片/色块/装饰） | ✅ 近似可行（VLM 描述 + LLM 用 6 种元素重绘） |
| 图表骨架（柱/折线/饼） | ⚠️ 可行但粗糙（数据靠 CONTENT 转录，样式细节丢） |
| 图标 | ❌ 无"参考图图标→图标库检索"映射，无法对齐 |
| 插画/照片 | ❌ 自绘不出来，只能嵌真实图或靠模板背景透出 |

本 spec 接受这个限制——目标是"类似的"（参考生成），不是"一模一样"（复刻）。

---

## 十、验收

每个 Phase 完成后：
1. **配测试**（照 document-auto-quality-spec 惯例）
2. **回归**：不破坏现有 ppt-auto 流程（无参考图时退回原有主题驱动生成）
3. **端到端**：给一张 PPT 截图 → VLM 理解 → 生成一页类似的，人工评判相似度

**整体验收命令**：
```bash
cd C:\Users\13852\Desktop\Swarm-OS\hiq
go vet ./desktop/...  # desktop 模块（含新 vision 文件）
go test ./internal/tool/builtin/...  # 不破坏 builtin
python -c "import ast; ast.parse(open('pdf_to_page_images.py').read())"  # 脚本语法
```

---

## 十一、与 image_understand 的关系（边界清晰）

- `image_understand`：只主会话看用户上传图（定位不变，见其能力边界文档）
- 本 spec 的 VLM 调用：走 desktop 层 `builtin.CallVLM`（仿 `ppt_template_vision`），**不经过 image_understand**
- 两者共用底层 `CallVLM` + 全局 `vlm_model`，但调用方/时机/模式不同（见前期 image_understand 定位讨论）

---

## 决策记录（为什么这么定）

| 决策 | 理由 |
|---|---|
| 先图片后 PDF | 图片→PPT 是核心链，PDF 只是加"PDF→图"前置；核心不通预处理无意义 |
| 不用 image_understand | 它会抢占专门 VLM 封装路径（前期的 desktop-auto 教训）；PPT 场景走 desktop 直接调 CallVLM |
| 背景/画布固定 | 用户选 default.pptx，VLM 不瞎提取；简化范围 |
| 字号自适应不取 exact px | VLM 看不到像素，瞎猜不可靠；用密度+比例+基线有判断 |
| merge 步骤优先 | 调研发现"提取了不 merge"是真瓶颈，不补这个，扩再多提取项也没用 |
| 参考生成非复刻 | 像素复刻是开放难题（VLM 无 grounding），参考生成落在可行区间且满足"画个类似的" |
