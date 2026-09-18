---
name: ppt-auto
description: 演示文稿专项技能：从主题/参考图/参考 PDF 生成 PPT、美化旧 PPT（保内容重排版式）、反解读取 .pptx、或修改已有项目（传 project_dir 走局部修复）。输出可编辑 .pptx。从零生成或美化 PPT 用本技能；仅读取/转换已有文档归 document-auto。
runAs: subagent
effort: high
max-steps: 80
allowed-tools: bash, read_file, write_file, edit_file, grep, todo_write, complete_step, web_search, web_fetch
---

# PPT 生成 Skill

根据用户主题生成专业演示文稿，输出 `.pptx` 文件。默认无动画、无过渡（用户明确要求时才加，参数见 `references/animations.md`）。

## 前置条件

需要 **Python 3.10+**。首次使用安装依赖：
- macOS/Linux：`bash <skill_dir>/setup_python.sh`
- Windows：`<skill_dir>\setup_python.bat`

依赖纯 Python（python-pptx/Pillow/lxml），**不需要 Office**。

## todo 管理规则（严格遵守，否则任务无法完成）

1. Step 0 开始前，用 `todo_write` 创建所有步骤
2. **每完成一步，必须使用 `complete_step` 提交证据**（系统会自动将该步标记为 completed 并将下一步置为 in_progress，**不要手动用 `todo_write` 去改状态**）
3. 某步失败无法完成时，也使用 `complete_step` 提交结果并在证据里说明原因
4. **最终答案前，确认所有步骤都已通过 `complete_step` 完成**——否则系统会判定任务未完成并阻止输出

### complete_step 证据格式（严格遵守，否则报错）

`evidence` 必须是**数组**，每项含 `kind`（verification/diff/files/manual）+ `summary`，以及可选的 `command`/`paths`。

**关键规则**：
1. `command` 字段必须和你实际跑的 bash 命令**完全一致**（系统会拿它去匹配 bash 执行记录）。不一致会报 "no matching successful bash receipt"。如果拿不准命令原文，用 `kind: "manual"` 代替。
2. `kind: "files"` 的 `paths` 只能引用**你自己用 write_file/edit_file 直接写过的文件**。脚本生成的文件（如 `exports/*.pptx`、`backup/`、`template_config.json` 被 extract_template_colors.py 改写）**不算你写的**——用 `kind: "verification"` + `command`（引用生成它的脚本命令）代替。

正确示例：
```json
{
  "step": "Step 0: 提取模板配色",
  "result": "已提取模板配色并更新 template_config.json",
  "evidence": [
    {"kind": "verification", "summary": "extract_template_colors 成功，background=#EDF8FC", "command": "python \"C:\\Users\\13852\\.hiq\\skills\\ppt-auto\\scripts\\extract_template_colors.py\" \"C:\\Users\\13852\\.hiq\\ppt-template.pptx\" \"C:\\Users\\13852\\.hiq\\skills\\ppt-auto\\template_config.json\""}
  ]
}
```

转换 PPTX 步骤（脚本生成的文件用 verification，不用 files）：
```json
{
  "step": "Step 7: 转换 PPTX",
  "result": "PPTX 已生成",
  "evidence": [
    {"kind": "verification", "summary": "svg_to_pptx 转换成功，57个元素", "command": "python \"C:\\Users\\13852\\.hiq\\skills\\ppt-auto\\scripts\\svg_to_pptx.py\" \"<project_dir>\""}
  ]
}
```

write_file 写的文件用 files：
```json
{
  "step": "Step 5: 写大纲",
  "result": "大纲已写入 design_spec.md",
  "evidence": [
    {"kind": "files", "summary": "design_spec.md 已创建", "paths": ["<project_dir>/design_spec.md"]}
  ]
}
```

拿不准时用 manual（不匹配命令也不匹配文件）：
```json
{"kind": "manual", "summary": "检查通过，无报错"}
```

## 输入与输出

- **输入**：主题（如"企业数字化转型"），可选源文档 + 页数/风格要求
- **输出**：`<project_dir>/exports/*.pptx`

## 路径占位符

- `<skill_dir>`：本 SKILL.md 所在目录
- `<project_dir>`：Step 4 init 创建的工作目录（init 前不存在）

> Windows 上 `python3` 若不存在，换成 `python`。

## 生成路线

默认走**路线 A（SVG 路线）**，除非用户要求"直接模板填充"才走路线 B。

---

## 路线 A：SVG 生成（8 步）

### Step 0: preflight（模板配色 + 视觉合并 + 配置 + 项目初始化，一次完成）

```bash
python3 <skill_dir>/scripts/preflight.py <project_name>
```

一个脚本做完原来五步：检查 `~/.hiq/ppt-template.pptx` 是否存在 → 有则提取配色（extract_template_colors）→ 合并视觉配色（merge_vlm_style，把 `ppt-template-style.json` / `reference-style.json` 的颜色机械写进 config，reference 优先 > 模板视觉 > extract 基线）→ 初始化项目（project_manager init）→ 打印**合并 JSON 摘要**。

输出的 JSON 就是你需要的全部配置（**不需要再 read_file template_config.json，也不需要单独跑 Step 3/Step 4**）：

- `has_template`：有无模板。**有模板 → SVG 不画任何全屏背景**（模板的背景/装饰/logo 由 PPTX 继承自动透出）；无模板 → SVG 自己画背景色
- `colors`：最终生效配色（background/accent/text/card_bg 等）——后续写大纲、生成 SVG 时**只能用这些颜色，禁止凭主题名推断品牌色**
- `fonts.family`：模板字体 + 跨平台降级链，SVG 文字用此字体
- `mode`：fast / validate（设置面板控制）
- `reference_style` / `pdf_pages`：参考物存在性（决定 Step 3 读什么）
- `project_dir`：创建好的项目目录（`<project_dir>/svg_output/` 等）

无项目名时 `preflight.py` 也可不带参数运行（只做配色与配置，不 init）。

### Step 1: 提取源文档（如有）

```bash
python3 <skill_dir>/scripts/extract_content.py <file.pdf> source_content.json
```
支持 PDF/DOCX/XLSX/CSV/TXT/MD。init 后挪到 `<project_dir>/sources/`。

### Step 2: 联网搜索（如需要）

```
web_search(query="<主题相关关键词>")
```

### Step 3: 读参考物（配置已由 Step 0 preflight 给出）

配色/字号/布局的唯一事实源是 `template_config.json`，但**其关键内容（colors/fonts/mode）已由 Step 0 preflight 的 JSON 摘要给出**——正常流程直接用那份摘要，只有跳过了 preflight 或需要布局规则细节时才 `read_file <skill_dir>/template_config.json`。同样记住：**只能用已读到的颜色，禁止凭主题名推断品牌色**。

**参考图（若有）**：若 `~/.hiq/reference-style.json` 存在（用户给参考图时由 desktop 的 `AnalyzeReferenceImage` 生成），读它的 `description`——VLM 对参考图的 4 段描述：

```bash
read_file ~/.hiq/reference-style.json
```

- **CONTENT**：参考图文字内容（若用户要"照这个内容"，以此为准）
- **LAYOUT**：版式 + 内容密度（选 `references/layout_templates.md` 对应布局，密度指导元素数）。LAYOUT 段末尾可能带一个 ```json regions``` 块（`[{"type","bbox":[x,y,w,h],"content"}]`，0-1280×0-720 粗略像素估算）——定位元素时优先参考 bbox，没有就按文字描述估
- **FORMAT**：字号相对比例（标题/正文 谁大谁小，喂字号选择）
- **DESIGN**：颜色/风格（辅助；精确颜色仍以 `template_config.json` 为准）

写大纲（Step 5）和 SVG（Step 6）时参照它——目标是"画一页类似的"，不是像素复刻。

**参考 PDF（若有，多页）**：若任务参数带 PDF 参考路径（或 `~/.hiq/pdf-pages/` 已有分析），这是**多页参考**——每个 `page-N.json` 对应一页，含 4 段 `description`（CONTENT 含**表格的完整行列内容**——照它画表，不要丢）：

**第一步：补齐分析**。桌面预分析只处理前 6 页（提交路径限速）；任务带 PDF 路径时必须先补齐全部页（幂等——已分析的页自动跳过）：

```bash
python3 <skill_dir>/scripts/analyze_pdf_pages.py "<PDF路径>"
# 输出 {"total": N, "analyzed": X, "skipped_existing": Y, "failed": Z, "note": "remaining_unanalyzed=R ..."}
```

**分批与续传**：每次调用至多分析 8 个缺失页（防 2 分钟 bash 超时）；`remaining_unanalyzed > 0` 或被超时打断时**原命令重跑即可续传**（已分析页自动跳过），循环直到 remaining=0。路径给错也不必全盘搜索——脚本会自动回退到 `reference-style.json` 的 `source_path`。

**第二步：逐页重绘**：
- 每页对应生成**一张 slide**（page-1.json → slide 1…），**总页数 = PDF 总页数**（补齐后 = page-N.json 数量，覆盖 `default_prompt.pages`）
- 每页按各自 `description` 的 CONTENT/LAYOUT/FORMAT/DESIGN 画；**description 里有表格（行列数+单元格文字）就用 SVG 表格画出来**——纯文字罗列会丢表格，这是最常见的失真
- **不要**自行用 python 提取 PDF 文字来编大纲——文字提取丢表格结构，以 page-N.json 为准

```bash
ls ~/.hiq/pdf-pages/page-*.json 2>/dev/null   # 看有几页
read_file ~/.hiq/pdf-pages/page-1.json         # 逐页读其 description
```

单图参考（`reference-style.json`）和多页 PDF 参考（`page-N.json`）一般不同时存在——前者画一张，后者画多张。若两者都有，以 PDF 多页为准。

**字号自适应（有参考图时）**：参考图能看出"密度"和"标题/正文比例"，但 VLM 看不到 exact px——不要让它瞎给字号数值。改为从 reference-style 的 LAYOUT（密度）+ FORMAT（比例）推断 density（high/medium/low）和 title/body ratio，套模板基线机械算：

```bash
python3 <skill_dir>/scripts/autofit_fontsize.py --density <high|medium|low> --ratio <标题是正文的几倍> > <project_dir>/output/autofit_result.json
# 输出 {"title": N, "card_title": N, "body": N} 落盘；骨架生成用 --autofit 传入，手写页直接采用这套字号
```

依据：模板基线（config）× 密度系数（密→缩小，疏→放大，±15%）× 比例保持（标题>卡片标题>正文）。这是"有判断、不瞎猜"——不靠 VLM 给 px，靠它的定性判断（密度+比例）机械算。无参考图时退回 config 默认字号。字号优先级（低→高）：config `font_sizes` < autofit 结果 < pages.json 每页 `fonts`。

**风格库（用户提到风格词时必查）**：`<skill_dir>/references/visual-styles/` 有 19 种成体系的设计规格（swiss-minimal 瑞士极简 / editorial 杂志 / ink-wash 水墨 / dark-tech / glassmorphism / data-journalism 等，目录含 `_index.md` 总览）。用户说"做成XX风"时：

```bash
read_file <skill_dir>/references/visual-styles/<风格名>.md
```

按该 spec 的形状语言/排版纪律/留白规则/配色运用方式画页。**配色 hex 仍从 config/palette 取**（`<skill_dir>/references/image-palettes/` 有 15 套成套调色板可参考，含 `_index.md`）——风格 spec 管怎么用色，不管具体色值。

### 颜色规则（唯一来源）

| 层级 | 来源 | 说明 |
|------|------|------|
| 基线 | `template_config.json` | 始终有效，提供全部颜色字段 |
| 视觉提取（最高） | 由 Step 0 `merge_vlm_style.py` 机械合并进 `template_config.json` | 来源：`ppt-template-style.json`（选模板）+ `reference-style.json`（参考图，若有）；reference 优先 |
| 用户输入 | 自然语言（"用绿色主色"） | 只覆盖明确提及的字段 |

**⚠️ 严禁凭空捏造颜色。严禁凭主题名推断品牌色（如"中国移动"≠ 自己编蓝色）。配色只能从已读的 config 取。**

**⚠️ 有参考图时的颜色权威链**：`~/.hiq/reference-style.json` 带颜色字段时，桌面预分析已把参考图真实配色（hex）机械合并进 `template_config.json`——config 的 colors 即参考图的真实颜色。**任务参数里转述的颜色描述**（如"主色调为深蓝色(#1a3c6e)"）是**上游模型看图后的转述，不是用户原话**，hex 常有偏差（实测把 #0078D4 亮蓝转述成 #1a3c6e 暗藏青），**不得作为用户输入覆盖 config**。仅当消息中明确出现"用户要求/用户指定"字样时才按用户输入处理。

### Step 4: 初始化项目（已由 Step 0 preflight 完成）

preflight 已创建目录结构（`project_dir` 见其 JSON 输出）。只有跳过了 preflight 才需要手动：

```bash
python3 <skill_dir>/scripts/project_manager.py init <project_name> --format ppt169
```

init 创建目录结构：

| 目录 | 用途 |
|------|------|
| `svg_output/` | 逐页 SVG（`slide_01.svg`…） |
| `notes/` | 演讲者备注（用户要求时才写） |
| `exports/` | 最终 PPTX |
| `images/` | 图片资源 |

### Step 5: 写大纲 + design_spec + pages.json

规划每页：页面类型（封面/目录/章节/内容/结尾）、标题、要点（3-6 条）、布局选型（参考 `references/layout_templates.md`）。

**内容必须来自用户主题和素材，不要套固定结构。**

**⚠️ 配色方案必须直接引用 Step 0 读到的 config colors 值，不得自创。**

把大纲写入 `<project_dir>/design_spec.md`（完整性校验必需）。**同时（关键提速）**：凡页面属于骨架类型——`cover`（封面）/`toc`（目录）/`section`（章节过渡）/`cards`（2-4 卡片）/`columns`（两栏对比）/`bullets`（要点列表）/`ending`（结尾）——把该页写成 pages.json 里的一条紧凑 spec（见 Step 6），不要手写这些页的 SVG；表格页/流程页走规则 12/13 的骨架脚本；只有骨架覆盖不了的定制版式才手写 SVG。

### Step 6: 逐页生成 SVG

**首选：骨架生成器（pages.json → 一次生成全部骨架页）**。Step 5 已把 cover/toc/section/cards/columns/bullets/ending 类页面写成 pages.json——一条 spec 约 300 token，替代手写 5K token 的整页 SVG：

```json
{"pages": [
  {"type": "cover", "title": "企业数字化转型", "subtitle": "从线上化到数据驱动", "footer": "2026-08"},
  {"type": "toc", "title": "目录", "items": ["背景", "架构", "场景", "收益"]},
  {"type": "cards", "title": "整体架构", "lead": "一套底座三层能力",
   "items": [{"icon": "tabler-outline/server", "head": "基础设施", "lines": ["混合云", "统一运维"]}]}
]}
```

```bash
python3 <skill_dir>/scripts/build_page_skeleton.py <project_dir>/pages.json --project <project_dir> [--autofit <project_dir>/output/autofit_result.json]
```

有参考图时传 `--autofit`（Step 3 落盘的字号结果）；字号链：config `font_sizes` < `--autofit` < pages.json 每页 `fonts`。

**复合布局（D-06）**：一页要拼两种骨架（如上 60% 时间线 + 下 40% 卡片）时，每块单独生成再拼装——骨架内容会经线性变换缩放进目标区域，各块天然不重叠：

```bash
# 块 A：上半部（0,0,1280,432）
python3 <skill_dir>/scripts/build_page_skeleton.py <project_dir>/pages_a.json --project <project_dir> --region 0,0,1280,432
# 块 B：下半部（0,432,1280,288），或每页 spec 里写 "region": [0,432,1280,288]
```

拼装：新建整页 SVG（含全屏背景 rect，规则 1），把各块文件里 `<g transform=...>...</g>` 的内容原样并入；各块先单独 check 再拼，拼好的整页跑 fast 模式 check（深度检查按原始坐标算，跨块会误报）。

一次调用生成所有骨架页（输出 `slide_NN_<type>.svg`，保持页序）。生成器读 template_config.json 自动执行配色/半透明卡片/背景规则/字体链，图标用规则 14 的占位符，文字自动换行——**你不需要管坐标**。JSON 摘要会报 `lines_dropped`（spec 太长装不下，删行后重新生成）和稀疏页警告（封面/结尾给足 title+subtitle+footer 三条文字）。生成后可用 edit_file 微调个别页，再跑批量检查。

**兜底：手写 SVG**（骨架覆盖不了的定制版式）。每页写入 `<project_dir>/svg_output/slide_NN.svg`…。

每页 SVG 必须：

0. **嵌入页型元数据**（check_svg 优先读它判断封面/章节/结尾豁免，文件名只是兜底）：
   ```xml
   <metadata><ppt-auto page-type="cards" slide-number="4"/></metadata>
   ```

1. **背景**（取决于 Step 0 是否有模板）：
   - **有模板**（`~/.hiq/ppt-template.pptx` 存在）：**不要画任何全屏背景**（不要 rect、不要 image）。模板的背景/渐变/装饰/logo 会通过 PPTX layout 继承自动透出。SVG 只画内容（卡片、文字、图标）。画全屏 rect 会盖住模板背景！check_svg.py 会报错。
   - **无模板**：每页第一个元素是全屏 `<rect width="1280" height="720" fill="#背景色"/>`（`colors.background`）。
2. viewBox 固定 `"0 0 1280 720"`
3. 配色严格读 config，不得凭空捏造
4. **卡片用半透明背景**（`rgba()` 格式），让模板背景透出来——**不要用纯色不透明填充**（如 `#F5F5F5`），那样会像色块贴上去。深浅自动适配：浅色背景用 `rgba(255,255,255,0.7~0.85)` 或主题色的 `rgba(R,G,B,0.08~0.12)`；深色背景用 `rgba(255,255,255,0.08~0.15)`。卡片可加细描边（`stroke` 主题色 + `stroke-width="1"`）增强层次感。`colors.card_bg` 已给出适配当前背景的半透明值，直接用。
5. 强调色只用于关键数据/按钮/警告（面积 ≤5%）
6. 文字与背景高对比度，避免压字/溢出
7. 同类元素对齐、间距均匀、同层级字号一致（标题 26px/卡片 18px/正文 14px）
8. 禁止 filter/pattern/mask/foreignObject
9. 纯 SVG 代码，不要 markdown 代码块
10. 封面只叠加标题副标题；结尾页只加致谢文字
11. **照片/截图/logo 等画不出来的区域不得静默省略**：从参考页渲染图裁剪后以 `<image>` 嵌入——
    ```bash
    # 按 page-N.json LAYOUT 段的 position/share 裁剪，输出到项目 images/
    python3 <skill_dir>/scripts/crop_ref_region.py ~/.hiq/pdf-pages/page-N.png --pos <位置> --share <占比> --out <project_dir>/images/pNN_desc.png
    ```
    SVG 中 `<image href="../images/pNN_desc.png" x=".." y=".." width=".." height=".."/>`（坐标按 LAYOUT 估算、宽高比用脚本输出的 aspect）；该页生成后必须跑 `python3 <skill_dir>/scripts/svg_finalize/embed_images.py <该页svg>` 把图片内联成 data URI（否则 check/QA/转换看不到图）
12. **表格页机械生成（禁止手写表格坐标）**：page-N.json 的 CONTENT 段含 markdown 表（`|` 分隔）时，用脚本生成整页（模型可再用 edit_file 补充周边元素，但表格本体不手画）：
    ```bash
    python3 <skill_dir>/scripts/build_table_skeleton.py ~/.hiq/pdf-pages/page-N.json --title "<页标题>" --lead "<可选导语>" --out <project_dir>/svg_output/slide_NN.svg
    ```
    返回 `overflow: true` 时用 `--rows A-B` 拆成连续两页（后续页码顺延）；生成后照常跑 fix_svg + check_svg
13. **流程图/时间线页机械生成（禁止手画连线坐标）**：参考页是流程图（节点+连线+判断分支）时，把 CONTENT 的流程步骤整理成 DSL（节点 `[流程]` `{判断?}` `(起止)`，`[A] -> [B] |标签|` 定义连线），然后：
    ```bash
    python3 <skill_dir>/scripts/build_flow_skeleton.py <flow.dsl> --title "<标题>" --out <project_dir>/svg_output/slide_NN.svg
    ```
    时间线页：`--timeline --from-table <两列表.md>`（`日期 | 任务`）。层级>6 自动横向布局，回路画虚线箭头。海报级多子流程只画主干链，子流程细节以文字块补充（一页塞不下是物理事实）。生成后照常 fix_svg + check_svg
14. **图标用占位符，禁止手画 icon path**：`<skill_dir>/templates/icons/` 有 11600+ 现成图标（5 个库，每个 deck 选一个风格库，simple-icons 是品牌 logo 库可并用）。SVG 里写占位符引用，不要自己拼 `<path d="...">`：
    ```svg
    <use data-icon="tabler-outline/server" x="960" y="200" width="48" height="48" fill="#0076A8"/>
    ```
    库选型见 `templates/icons/README.md`；描边类库（tabler-outline）可加 `stroke-width="1.5"`（细）或 `"3"`（粗）。页面生成后跑 `python3 <skill_dir>/scripts/svg_finalize/embed_icons.py <该页svg>` 把占位符替换为真实图标（可与 embed_images 同批处理）
15. **网络配图搜索（主题驱动的 deck 需要照片/氛围图时）**：确需照片时用免 key 图搜（百度图源，无需任何注册）：
    ```bash
    python3 <skill_dir>/scripts/image_search.py --query "<关键词，如：数据中心 机房>" --out <project_dir>/images/hero.png --aspect landscape
    ```
    嵌入方式同规则 11（`<image href="../images/...">` + embed_images 内联）。**版权提示**：网络图仅供用户内部参考用途，商用需用户自行确认来源许可。
16. **拼图切分**：一张图里含**多页/多元素**（用户贴的拼图截图、多页幻灯片合图、插画素材表）先用脚本切分，不要当单页处理：
    ```bash
    python3 <skill_dir>/scripts/slice_images.py <拼图.png> --grid 2x3 -o <project_dir>/images/
    ```
    切出的页图可作为逐页参考（同 pdf-pages 用法）；切出的素材元素按普通图片嵌入（规则 11）。`--trim` 紧裁内容、`--alpha` 抠透背景

**修复 + 检查：批量跑，不逐页**。写完所有页面（或一批）后跑**一次**——单解释器循环处理全部页，替代每页两条命令两个回合：

```bash
python3 <skill_dir>/scripts/batch_check.py <project_dir>
```

检查模式由 `template_config.json` 的 `mode` 字段控制（设置面板的"快速/校验模式"可切换）：
- `fast`：基础检查（XML 格式、背景规则、禁止元素/模式）+ **核心内容检查**（内容硬底线：非封面/章节/结尾页 <5 段文字即 ERROR；文字超出画布；文字压字）——快而兜底，空页/溢出页不会再静默出厂
- `validate`：额外检查密度建议/垂直与空间覆盖/对齐/间距——全面。WARN 是建议性的，不阻止流程

batch 的 **stdout 是一个 JSON 对象**（`failed` 列出 ERROR 页，`skipped_legacy` 列出未处理的旧格式页；exit code 2 = 有 ERROR）——解析 JSON 而不是读文本；人类可读的逐页报告走 stderr。**只修 `failed` 列出的页**，改完对该页单独复检：

```bash
python3 <skill_dir>/scripts/fix_svg.py <project_dir>/svg_output/slide_05.svg <project_dir>/svg_output/slide_05.svg
python3 <skill_dir>/scripts/check_svg.py <project_dir>/svg_output/slide_05.svg --config <skill_dir>/template_config.json
```

> **演讲者备注**：用户明确要求时才做——在 `notes/slide_NN.md` 写每页 2-5 句备注，svg_to_pptx 会自动读取嵌入。默认不生成。

### Step 6.5: 视觉 QA 回路（**必须列入 todo**；无参考时在本步说明跳过）

**本步骤必须出现在 Step 0 创建的 todo 列表中**——无参考也执行（走 rubric 模式），不许在创建 todo 时省略。

模式自动选择：`~/.hiq/reference-style.json`（单图参考）或 `~/.hiq/pdf-pages/page-1.json`（PDF 参考）存在 → **对比模式**（与参考图并排判定保真度）；两者都没有（纯主题驱动）→ **rubric 模式**（无参考绝对标准审查：文字溢出/重叠压字/对比度/对齐/留白/字号层级，仅 MAJOR 触发返工）。

把生成的 SVG 渲染成图，与参考图并排送 VLM 对比（用的是 hiq 配置的视觉模型，无需额外参数）：

```bash
python3 <skill_dir>/scripts/qa_compare.py <project_dir> --round 1
```

**fast 模式加 `--sample 3`**（封面 + 2 个等距页）——QA 的 VLM 调用是主要耗时，快速模式没必要全量；`validate` 模式才全页：

```bash
python3 <skill_dir>/scripts/qa_compare.py <project_dir> --round 1 --sample 3   # mode=fast 时
```

**可续传**：页数多时单次会被 2 分钟 bash 超时打断（报告记为 `in_progress`）——**原命令重跑即从断点继续**（已完成页自动跳过），直到输出带 `done: true` 的完整报告。

输出 JSON（同时写入 `<project_dir>/qa-report.json`）：`pages[].verdict`（PASS/MINOR/MAJOR）+ `issues` + `stop`。按以下规则执行，**不要自行发明额外轮次**：

- `stop: true` → 本步结束，进入 Step 7（无论是否还有 MAJOR——脚本已判定返工到顶或无进展，继续改是空转）
- 有 MAJOR 页且 `stop: false` → 按 `issues` 提示修复对应页 SVG（改完必须重跑该页的 fix_svg + check_svg），再以 `--round 2` 重跑本命令
- **最多 2 轮**（round > 2 时脚本强制 stop）；第 2 轮与第 1 轮 issues 完全相同 → 脚本判 no_progress 自动 stop
- **MINOR 一律忽略**，不触发返工——目标是"类似的"，不是像素复刻

complete_step 证据：引用**最后一次** qa_compare 运行（`kind: verification` + `command`）。

### Step 7: 转换 PPTX

```bash
python3 <skill_dir>/scripts/svg_to_pptx.py <project_dir>
```

转换器会**自动检测** `~/.hiq/ppt-template.pptx`：有模板时打开模板、清空已有 slides、用模板的 layout 添加新 slide（模板的背景/master/装饰通过继承保留），把每页 SVG 转成原生 DrawingML 形状/文字（**可编辑**）叠加在模板背景上。无模板时用空白 Presentation。无需手动传参。

若 `notes/` 目录有备注文件（用户要求时才生成），转换器会自动读取嵌入。

纯 Python（python-pptx），**不需要 Office**。默认无动画无过渡。

> 如需动画/过渡/旁白，参数见 `references/animations.md`。

---

**生成完成后**：把 `<project_dir>/exports/*.pptx` 交付给用户。如用户要求修改，用 edit_file 改对应页 SVG → 重跑 Step 7。

> 导出 PDF 需额外安装 PowerPoint/WPS，用户明确要求时才做：`python3 <skill_dir>/scripts/export_pdf.py <input.pptx> <output.pdf>`

---

## 修改模式（已有项目的局部修复——不重跑全流程）

**触发识别**：任务参数指向一个**已存在**的 `<project_dir>`（含 `svg_output/`），要求修复 QA 问题、修改部分页面或重新导出——而非从主题/参考新生成。此时**不走** Step 0-6，按本模式执行；todo 按下面 4 步创建。

### R1: 读 QA 报告定位问题页

```bash
read_file <project_dir>/qa-report.json
```

取所有 `verdict == "MAJOR"` 的页（无报告时按任务参数指定的页）。issues 字段就是每页的具体问题清单。

### R2: 逐页修复

对每个 MAJOR 页：

1. 读该页参考描述：PDF 参考 → `~/.hiq/pdf-pages/page-N.json` 的 description；单图参考 → `~/.hiq/reference-style.json`
2. `edit_file` 修改 `<project_dir>/svg_output/slide_NN.svg`——**只改 issues 指出的问题项**（补缺失的内容块/表格行列、修正结构、恢复被截断的文字），不要重画整页。缺失的截图/logo/照片按 Step 6 规则 11 裁剪参考页嵌入；表格类内容缺失/截断按 Step 6 规则 12 用 build_table_skeleton.py 整页重建
3. 改完跑 `fix_svg.py` + `check_svg.py`（同 Step 6 规则，ERROR 必须修）

### R3: QA 复检（round 2，可续传）

```bash
python3 <skill_dir>/scripts/qa_compare.py <project_dir> --round 2
```

被超时打断就**原命令重跑续传**直到 `done: true`。结束条件（机械判定，不要自行加轮次）：`stop_reason` 为 `no_major`（修好了）、`no_progress`（两轮 issues 相同，改不动了）或到 2 轮上限——后两种情况**接受现状**，在最终结果里如实列出未修复项。

### R4: 重新导出

```bash
python3 <skill_dir>/scripts/svg_to_pptx.py <project_dir>
```

交付新的 `exports/*.pptx`，并列出：修复了哪些页、仍遗留哪些 MAJOR 及原因。

---

## PPTX 输入（反解任意 .pptx）

用户给出 **PPTX 文件**（要求读取内容/分析结构/基于它修改或美化）时，先反解为逐页 SVG，不要用 python-pptx 手工遍历：

```bash
python3 <skill_dir>/scripts/pptx_reverse.py <input.pptx> -o <work_dir>
# 输出 slides=N placeholders=M + svg-flat/（自包含逐页视觉）+ svg/（母版/版式分层）+ assets/（媒体）+ conversion-report.json
```

- `svg-flat/slide_NN.svg`：每页完整视觉重建（含模板继承的背景/装饰），可渲染预览、可逐形状改写后走 svg_to_pptx 重新导出
- `placeholders=M > 0` 表示有不支持的源构造被降级为占位框——在结果里如实告知用户
- 文本内容在形状级 `<metadata data-pptx-part="txbody">`（base64 OOXML）与可见 `<text>` 中；改文字优先改可见层并同步
- 完整"保内容重排版式"（Beautify）路线为后续 Phase，当前先用于读取/分析/局部修改

---

## Beautify 路线（旧 PPT 保内容重排版式）

**触发识别**：用户给出**已有 PPTX** 并要求美化/重新设计/变好看/换风格——且**不新增删改内容**。与"PPTX 反解读取"的区别：Beautify 要产出全新排版的 PPTX；与"内容修改"的区别：文字一比一保留。用户既要改内容又要美化时，先问清以哪个为准。

**铁律——内容锁**：每页文字（含页码、脚注）**一个都不能丢**，由 `beautify_lock.py` 机械校验，不靠自觉。页数、页序 1:1。

### B1: 反解原稿

```bash
python3 <skill_dir>/scripts/pptx_reverse.py <原.pptx> -o <work_dir>
```

### B2: 建立内容锁

```bash
python3 <skill_dir>/scripts/beautify_lock.py extract <work_dir>/svg-flat --out <project_dir>/content_lock.json
```

### B3: 逐页重排（Step 5-6 流程）

对每页：读 `svg-flat/slide_NN.svg` 提取该页**全部文字**（可见 `<text>`，含隐藏副本过滤后的）与图片数——这些文字**逐字**进入新页；图片可引用 `<work_dir>/assets/` 里反解出的原图（`<image href>` + embed_images 内联）。版式/风格/配色重新设计（可用风格库，规则同 Step 3/6）。**禁止**：增删文字、并页拆页、改页序。

### B4: 内容锁校验（⛔ 必须通过）

```bash
python3 <skill_dir>/scripts/beautify_lock.py check <project_dir>/svg_output --lock <project_dir>/content_lock.json
```

exit 2 或 `ok: false` → 按 `missing` 清单补回丢失文字后重查，**不过不导出**。`added` 只是提示（新增标签等需用户可解释）。

### B5: QA（Step 6.5 rubric 模式——无参考，绝对标准）

### B6: 导出（Step 7）+ 交付时说明：内容零丢失（附 B4 结果）、页数页序一致、版式改动点

---

## 路线 B：模板填充（不经 SVG）

用户明确要求"直接模板填充"时用 `template_fill_pptx.py`。与路线 A 二选一，不混用。详细参数见脚本 `--help`。

---

## 用户输入覆盖

| 用户说 | 覆盖 |
|--------|------|
| "用XX模板" | template |
| "做10页" | pages |
| "深色风格" / "用绿色主色" | style / colors |
| "快速模式" / "校验模式" | mode = fast / validate |
| "要动画" | 参考 references/animations.md |

> 垂直覆盖为建议项而非强制：3/4 区域有内容即通过；2/4 仅 WARN 不阻塞流程；≤1/4 才是 ERROR。

---

## 脚本一览

| 脚本 | 依赖 |
|------|------|
| preflight.py | 纯 Python（编排 extract/merge/init 并输出合并 JSON 摘要） |
| batch_check.py | 纯 Python（单解释器批量 fix + check 全部页） |
| build_page_skeleton.py | 纯 Python（pages.json → 骨架页 SVG，7 种类型） |
| extract_content.py | 纯 Python |
| qa_compare.py | 纯 Python（需 cairosvg；VLM 访问读 hiq 配置） |
| svg_quality_checker.py | 纯 Python（**可选**手动深度审计：spec_lock 漂移/字号层级/逐元素检查；不在生成流水线内，需要逐项审计报告时手动跑） |
| analyze_pdf_pages.py | 纯 Python（需 PyMuPDF；VLM 访问读 hiq 配置） |
| crop_ref_region.py | 纯 Python（需 Pillow） |
| build_table_skeleton.py | 纯 Python |
| build_flow_skeleton.py | 纯 Python（流程 DSL / 时间线表） |
| image_search.py | 纯 Python（需 Pillow；百度图源免 key） |
| pptx_reverse.py | 纯 Python（vendor 自 ppt-master，MIT） |
| slice_images.py | 纯 Python（需 Pillow；vendor 自 ppt-master，MIT） |
| beautify_lock.py | 纯 Python |
| extract_template_colors.py | 纯 Python（需 Pillow） |
| project_manager.py init | 纯 Python |
| fix_svg.py | 纯 Python |
| check_svg.py | 纯 Python |
| svg_to_pptx.py | python-pptx（纯 Python） |
| template_fill_pptx.py | python-pptx（纯 Python） |
| export_pdf.py | **需 PowerPoint/WPS** |

## 资源

| 资源 | 用途 |
|------|------|
| templates/charts/ | 图表/流程图（含选型规则 charts_index.json） |
| templates/icons/ | 5 个图标库 |
| references/layout_templates.md | 布局坐标规格 |
| references/animations.md | 动画/过渡/旁白参数 |
| references/error_handling.md | 排错参考 |
