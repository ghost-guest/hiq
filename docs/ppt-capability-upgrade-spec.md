# PPT 能力升级 Spec（ppt-auto 对齐与超越 ppt-master）

> **状态**：规划中（未开工；P0 项待排期）
> **基线**：skill v39（commit `96c02da`），本文所有"已有"均以该版本为准
> **范围**：图片/PDF→PPT 之外的四个提升方向——质量审查、PPTX 输入产品线、知识资产、创作辅助件
> **基础**：2026-08-15/16 ppt-vision 批次（见 CHANGELOG [Unreleased — ppt-vision]）+ 与 ppt-master v4.7.0 的实地对比
> **上游参考**：`Swarm-OS/ppt-master`（MIT，移植需保留 attribution）

---

## 一、现状基线（已有的，不重复做）

| 域 | 能力 | 状态 |
|---|---|---|
| 预分析 | 合并判定+四段+颜色并行（1 次往返）、路径路由、残留不变式、全降级可见 | ✅ |
| 生成 | 表格骨架（markdown→整页）、流程图骨架（DSL→分层布局）、裁剪嵌入、免 key 图搜（百度） | ✅ |
| 验收 | QA 对比参考（severity 门控/双轮硬顶/断点续跑）、修改模式 R1-R4 | ✅ |
| 转换 | svg_to_pptx（模板 OOXML 继承 + 7 件 finalizer） | ✅（同源且修复过上游 bug） |

**已知边界**（不追求修复，属接受项）：VLM 判定自然波动（±2 页 MAJOR 翻转，双轮硬顶兜底）；海报级流程图只画主干；插画/照片靠裁剪或图搜近似。

---

## 二、提升项总览（按优先级）

| # | 项 | 价值 | 工程量 | 优先级 |
|---|---|---|---|---|
| 1 | QA 无参考绝对审查（rubric 模式） | 补最大日常场景的质检空白 | 小 | **P0** |
| 2 | pptx_to_svg 反向解析 + Beautify 路线 | 解锁"PPTX 输入"产品线（读/美化旧 PPT） | 大 | **P0**（立项后分 Phase） |
| 3 | 知识资产移植（风格库/调色板/executor 文档/slice_images） | 零代码或近零代码的能力扩容 | 小 | **P1** |
| 4 | Phase C 复合布局模板（时间线+表/图+文同页） | 消灭当前唯一 MAJOR 类（p4 形式差异） | 中 | P1 |
| 5 | Playwright 渲染备选后端 | QA 渲染的 CJK 字体链稳健性 | 小 | P1（随 #1 做） |
| 6 | SVG 创作辅助件（预设几何/布尔/坐标计算） | 降模型手写 path 的错误率 | 中 | P2 |
| 7 | 公式 / 超链接 / 图表带数据 | 技术 deck 三件刚需 | 中 | P2 |
| 8 | 图搜增强（官方 key 层 / SearXNG 适配器） | 配额与质量升级，用户仍零注册 | 小 | P2 |
| 9 | 体验债（长任务进度反馈 / dream 泄漏） | UX 与产品决策 | 中/小 | P3（dream 待用户定方向） |

---

## 三、P0-1：QA 无参考绝对审查（rubric 模式）

**问题**：qa_compare 仅在存在参考（reference-style.json / pdf-pages）时运行。主题驱动的 deck——**大多数日常场景**——画完零审查。ppt-master 的 visual-review 阶段用 LLM rubric 逐页审（对齐/层次/留白/可读性），不需要参考。

**方案**：
- `qa_compare.py` 加 `--rubric`：无参考时对每页渲染图做**绝对标准** VLM 审查。rubric 固化在代码里（不靠模型自觉），要点：文字溢出/截断、明显重叠压字、对比度不足（深底深字）、对齐混乱、单页留白 >40%、字号层级缺失。判定仍三档 PASS/MINOR/MAJOR，仅 MAJOR 触发返工——与现有回路契约一致。
- SKILL.md Step 6.5 触发条件从"有参考"放宽为"**总是运行**"：有参考走对比模式，无参考走 rubric 模式。
- 渲染后端抽成可插拔（resvg 默认，`--renderer playwright` 备选——Chromium 字体回退链最稳，ppt-master 因 cairo CJK 豆腐块问题实测弃用过 cairo 系；我们的 resvg 在本机正常，换字体环境不保证）。

**不做**：不模仿其"子代理逐页 spawn"的审查编排（我们已有断点续跑 + 池并发，够用）。

**验收**：主题驱动 deck（无参考）跑 Step 6.5 出 qa-report；人为制造一页压字/溢出，rubric 判 MAJOR 触发修正。

## 四、P0-2：pptx_to_svg 移植与 Beautify 路线（PPTX 输入产品线）

**问题**：hiq 对 PPTX 输入只有 python-pptx 浅层遍历。ppt-master 有完整反向解析器（17 模块：图表/自定义几何/效果/表格/文本/预设形状/超链接/EMU 单位/主题色解析），是其 Beautify（保文字页序 1:1 重排版式）与 readPPTX 的地基。**这是 hiq 最大的产品空白**："我有份旧 PPT 帮我弄好看"是办公高频需求。

**分 Phase**：

- **Phase A：反向解析落地**。移植 `scripts/pptx_to_svg/`（MIT，保留版权声明）为 skill 脚本 `pptx_to_svg.py <input.pptx> <out_dir>`；验收金标准：华为那份 32 页 PDF 的**原 PPT 版**（或任意真实商务 PPT）逐页转 SVG，渲染后与 PowerPoint 截图人工比对结构完整（形状/文本/表格/图表骨架）。
- **Phase B：Beautify 路线**。新工作流：`输入 PPTX → pptx_to_svg 反解 → 逐页提取语义内容（文字/表格/图，锁 1:1 不许增删改）→ 套现有生成管线重排版式 → 导出`。关键纪律：**内容保真锁**——每页文字集合与原 PPT 严格一致，由脚本机械 diff 校验（复用 beautify_identity 思想：identity 校验而非靠模型自觉）。
- **Phase C：SKILL.md 路由矩阵**。引入 ppt-master 式路由表（生成 / 图片复刻 / PDF 复刻 / **美化** / 模板填充 五路线互斥 + 请求形态映射 + 禁止向用户弹路线菜单），替代现行"单流程 + 修改模式"的组织方式。Beautify 上线时一并切换。

**明确不做**：Create Template 工作区系统（可复用品牌模板库）——依赖重，待 Beautify 验证需求后再立项；native 增强路线（保页加配音/计时）。

**验收**：一份真实旧 PPT 经 Beautify 后文字零丢失（机械 diff 证明）、页数页序一致、版式明显改善（QA rubric + 用户评判）。

## 五、P1：知识资产移植（近零代码）

1. **风格库**：`ppt-master/references/visual-styles/`（19 种完整设计规格）+ `image-palettes/`（调色板库）→ 拷入 `references/`，SKILL.md Step 3 挂索引与选用规则（用户说"杂志风/水墨风"→ 对应 spec 文件，配色从 palette 取而非模型编）。
2. **executor 纪律文档**：chart/table/structured/visualization 分则 → 按需精选拷贝（与我们的骨架脚本冲突的段落改写为指向脚本）。
3. **`slice_images.py`**：拼图切分（--grid/--trim/--alpha 背景抠透，纯 PIL）。两个用途：多页拼图截图自动切页（扩展桌面拦截：一张附件=多页参考）；切插画素材。接入点：预分析前跑一次试探（网格检测），SKILL.md 补规则。
4. **License**：以上均为 MIT 资产，拷贝时在文件头保留上游版权与来源注释。

**验收**：`做成杂志风` 类请求命中 editorial 风格 spec；一张 2×3 拼图被切成 6 页参考并逐页复刻。

## 六、P1：Phase C 复合布局模板

**问题**：当前唯一 MAJOR 类（p4"图形时间线 vs 纯文字表"）。时间线/图 + 密集表/文字**同页**是常见版式，骨架脚本各自整页输出，无法组合。

**方案**：骨架脚本增加**区域输出**模式（`--region x,y,w,h` 只生成该矩形内的内容），SKILL.md 给出组合规则（如：上 60% 时间线 + 下 40% 表）；QA 对组合页的既有判断继续生效。取代目前"手工平移拼接"的做法（已在华为 deck 修复中证明易重叠）。

**验收**：p4 重建为"上图形时间线 + 下假设表"单页，QA 判 ≤MINOR。

## 七、P2 项（要点）

- **SVG 创作辅助件**：移植/改写 `preset_shape_svg`（PPT 170+ 预设几何 → SVG path）、`shape_boolean_svg`、`svg_position_calculator` 为 skill 脚本；SKILL.md 指引模型"复杂 path 用辅助件生成，禁手算"。
- **公式**：`latex_render` 改 matplotlib mathtext 后端（免装 LaTeX），输出 PNG 嵌入（同规则 11 内联）。
- **超链接 / 图表带数据**：参考其 native-hyperlinks / native-data-interface 文档，在 svg_to_pptx 侧做直通属性（`data-href` 标记 → DrawingML 关系）。
- **图搜增强**：Settings 可选 Pexels/Pixabay key 层（hiq 官方 key 内置或用户自配，检测到即优先，百度兜底不变）；`provider_searxng.py` 对接本地 SearXNG 聚合 Bing/Baidu（Swarm-OS 已有 searxng-standalone）。

## 八、P3：体验债

- **长任务进度反馈**：Step 6 逐页生成是分钟级流式补全，UI 无中间反馈（12 分钟静默的根源）。属 app UI 层工程，需产品排期，不在本 skill spec 内展开。
- **dream 提示词泄漏**（⏳ 待用户决策）：后台记忆代理任务书以 user 消息进入共享会话并显示于输入框。根因 `agent.go:721`。两个方向二选一后修复：**完全隐藏**（dream 轮次 UI 不可见）或**折叠显示**（显示为一行"系统任务"，可展开）。涉及核心会话管线，改前需补 agent 层测试。

---

## 九、工程约束（所有 Phase 通用）

| 约束 | 说明 |
|---|---|
| SkillVersion 纪律 | 改内嵌资产必升版本号，否则已安装 skill 不更新（v25 的教训） |
| bash 2 分钟超时 | 所有新脚本必须**分批或断点续跑**（v32 的教训）；长任务输出中间产物 |
| 多用户零注册 | 任何网络能力默认免 key；key 只能作为可选增强层 |
| 颜色权威链 | 上游转述 hex 不得覆盖 config；机械合并优先于模型自觉 |
| 上游 attribution | 移植 MIT 资产保留版权与来源声明 |
| 永不阻塞出片 | 任何增强失败一律降级 + 告警，不挡 PPTX 导出 |

## 十、验收与回归

每个 Phase 完成后：`go vet ./... && go build ./...`、desktop 全量测试零新增失败、脚本 `ast.parse`、真机 smoke；涉及生成的 Phase 用华为 deck 或新 PDF 做金标准回归（QA PASS 数不下降）。

## 开放问题（待用户决策）

1. dream 泄漏的呈现方向（隐藏 vs 折叠）——阻塞 P3 第二项；
2. Beautify Phase B 的内容锁严格度（逐字 1:1，还是允许等价改写）；
3. 图搜官方 key 采用"内置"还是"轻代理"形态（后者需服务端，涉及运营投入）。
