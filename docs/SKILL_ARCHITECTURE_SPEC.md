# 技能体系架构 Spec（命名 / 职责边界 / 内置-外置）

> **状态**（2026-08 第二轮修理后）：**待办清单 25 项全部完成 ✅**。ppt-auto 嵌入资产 SkillVersion → 45（骨架生成器 + check_svg 章节页豁免 + 前轮的 preflight/批量检查/QA 采样/图标占位符）。大项落地记录：item 10 骨架生成器（7 种页面类型，pages.json 一次生成，validate 全量模式 7/7 通过）；item 8 派生可编辑副本（后端 DeriveEditableSkill + 能力面板按钮）；item 19 桌面启动后台预装 Python 依赖（marker 守卫）；item 20 PDF 参考预分析后台补齐（同步 6 页后 goroutine 跑技能自带 analyze_pdf_pages.py --max 999）。追加（同日）：codegraph 改 opt-in 默认关闭，与 context7 政策统一——含默认值/首跑特例删除/测试语义反转/渲染注释
> **范围**：run_skill 技能全景盘点、computer-auto→desktop-auto 改名决策、职责不清问题清单、内置-外置判据与专项技能调用控制原则、ppt-auto 性能解剖与调用侧提示词问题
> **基础**：基于 2026-08 的技能体系评审讨论（改名动机 → 全量职责审查 → 外置化设计分析 → ppt-auto 性能专项）
> **不属本 spec**：单个技能的生成质量/美学调优（如 ppt-auto 的版式美学——那是各技能自己的 spec，如 ppt-vision-enhancement-spec）；本 spec 管体系层——命名、路由、职责边界、性能结构

---

## 一、结论摘要

1. **computer-auto 已改名 desktop-auto**（v 本分支已实施）：名字收窄为"桌面 GUI 自动化"，并在技能体、cowork 路由表、中英文界面描述三处写入**代码优先原则**——凡能用代码直接调的电脑操作（文件/进程/系统信息/设置）绝不模拟鼠标键盘。
2. **技能职责审查发现 6 个问题**（Part 四），最重的两个：explore 的描述公然抢 review/security-review 的活；ppt-auto 与 document-auto 都声称管 .pptx 却没划界。
3. **内置 vs 外置的判据**：按"是否携带资产（脚本/模板/参考资料）"和"是否为 Go 工具族的路由外壳"两个问题决定，而不是按名字里的 `-auto` 后缀整体搬。ppt-auto 外置是因为它是 40MB 资产包；-auto 家族其余 7 个是提示词外壳，留代码内。
4. **外置化能力架构上已存在**：同名文件技能可遮蔽内置（project → custom → global → builtin 垫底）。想给用户"可定制的数字员工"，补一个"派生可编辑副本"GUI 动作即可，不需要整体外置。
5. **ppt-auto 慢的大头不是脚本，是模型逐页手写 SVG 的 token 量**（占 6-7 成）。预置只能救边缘（首启依赖、图标索引、预分析全量化）；真正的提速靠**页面骨架生成器**把"手写 5K token/页"变成"参数 300 token/页"。调用侧另有四处提示词问题，含一个幽灵工作流（icons README 引用不存在的脚本）。详见 Part 六。
6. **专项技能调用控制三原则**（5.6）：父级整任务委派（已达标）；技能侧模型回合最小化——机械循环下沉单次大调用，browser-auto 是标杆；harness 侧回合预算显式化（per-skill `max-steps:`，现在没有）。全技能审计（5.7）：ppt-auto 是最大违反者（Part 六治理中），desktop-auto 的"每步必复验"可改为检查点复验，其余全部合规。
7. **技能三分域已实施**：编码（dev）/ 办公（cowork）/ 运维（netdev）由各 profile `enabled_skills` 白名单强制；编码域的 MCP（codegraph server、context7）与 lsp 工具经新增的 `hidden_plugins` / HiddenTools 机制挡在办公/运维模式外（并修复 codegraph 注入绕过过滤的历史 bug）；research 归纯编码域。见 Part 二"三分域归属"。

---

## 二、技能全景（run_skill 可调用面）

run_skill 看到的是两个来源的并集：

### 来源一：代码内置（`internal/skill/builtins.go` 的 `builtinSkills()`，15 个）

| 技能 | RunAs | 职责（摘要） |
|---|---|---|
| netdev-help | inline | 运维速查卡：权威参考源列表（华为 Info-Finder / Cisco / ZTE / RFC / NVD）与溯源规则。纯参考，无工具 |
| init | inline | 分析代码库，生成/刷新 AGENTS.md 项目记忆 |
| explore | subagent | 隔离子代理大范围只读探查代码库，返回单一蒸馏结论 |
| research | subagent | web_fetch + 读代码结合的技术调研 |
| install-capability | inline | 安装/卸载 MCP 服务器与技能（URL / GitHub / 本地路径 / .mcp.json / 包名） |
| review | subagent | 审查当前分支 diff：正确性 / 安全 / 缺测试，给 file:line |
| security-review | subagent | 安全视角审查分支 diff：注入 / 越权 / 密钥 / 路径穿越 |
| test | inline | 跑测试套件 → 诊断 → 修复 → 重跑至绿 |
| browser-auto | subagent | 网页任务（导航 / 点击 / 输入 / 抓取 / 截图） |
| desktop-auto | subagent | 桌面 GUI 自动化（WPS / Excel / 系统对话框），GUI-only + 代码优先 |
| email-auto | subagent | SMTP/IMAP 收发读搜邮件 |
| rag-auto | subagent | 本地知识库（FTS5 + 实体）检索 / 导入 / 管理 |
| schedule-auto | subagent | 定时任务增删改查 |
| document-auto | subagent | Office 文档读写 / 填充 / 转换（docx / xlsx / pptx / pdf / csv / md） |
| expert-auto | subagent | 多专家团队评审提案或文档内容 |

### 来源二：文件技能（磁盘上的 SKILL.md 目录树）

- **ppt-auto**（唯一随包发布的）：嵌入二进制（`internal/assets/pptauto`，约 40MB：Python 管线 + 版式模板 + 17 套配色 + 20 余种视觉风格 + 上万图标），首启由 `EnsurePPTAutoSkill` 释放到 `~/.hiq/skills/ppt-auto/`，靠 `.embedded-version`（当前 "43"）判断刷新。
- 用户经 install-capability 自装的（本机当前没有）。

### 呈现机制

- **技能索引**：系统提示里每行 `- name [🧬 subagent] — description`，上限 4000 字符超出截断（`internal/skill/index.go`）。所有启用技能都进索引，两个 profile 共见。
- **cowork 路由表**：`internal/config/profile.go` 的 `coworkDefaultPromptAddon` 编译进二进制，8 行表把任务类型映射到 `run_skill("xxx", task)`；另有两句"不必委派"原则（web 查询用 web_fetch/web_search；系统操作直接跑代码）。

### 三分域归属（已实施 ✅ 2026-08）

技能按使用域分三类，由各 profile 的 `enabled_skills` 白名单强制（`internal/config/profile.go`）：

| 域 | 技能 | profile |
|---|---|---|
| **编码** | init、explore、**research**、review、security-review、test | dev |
| **办公** | browser-auto、desktop-auto、ppt-auto、email-auto、rag-auto、schedule-auto、document-auto、expert-auto | cowork |
| **运维** | **编码全集继承**（init/explore/research/review/security-review/test）+ **netdev-help**（用户方向 2026-08-20：运维先把编码内容全部拿过来；白名单管可见性、tool_scope 封印管行为——test/init 等需 shell/写的技能在封印下降级为只读分析） | netdev |
| **通用** | install-capability（装技能/MCP，三模式都留） | — |

编码域的 MCP / 工具同样不进其他模式（Profile 新增 `hidden_plugins` 按名隐藏机制）：

| 能力 | 类型 | 域 | 门控 |
|---|---|---|---|
| codegraph server + codegraph_* 工具 | 内置 MCP | 编码 | **opt-in 默认关**（同 context7）；开启后仅 dev，cowork/netdev 经 HiddenPlugins 隐藏 |
| context7（库文档 MCP） | 内置 MCP | 编码 | 手动开启后仅 dev 生效；cowork/netdev 隐藏 |
| **一切外部/自装 MCP** | MCP | — | netdev 升级为 **`plugin_allowlist` 严格白名单**（2026-08-20，NETDEV_SPEC §7.4）：空列表全隐藏，用户必须在 `[[profiles]] name="netdev"` 点名；热连接路径（connectMCPSpec 汇合点）与 boot 启动过滤（含 ExtraPlugins）双侧强制 |
| lsp_* 工具 | 内置工具 | 编码 | cowork 原有 HiddenTools；netdev 补 HiddenTools |
| web_search / web_fetch | 内置工具 | 通用 | 三模式可见（netdev seal 保留只读 web） |

**codegraph 分发可靠性（2026-08 事故修复）**：用户反馈"很多用户下载不下来导致报错"。根因：镜像链只有 ghproxy.net 一家 + GitHub 直连（公共 ghproxy 类镜像轮换性死亡，中国网络直连 GitHub 不稳）；降级机制本身健全（grep 兜底 + notice 不炸 + 下会话重试），但失败通知无出路。已修三处：①镜像链 1→4，**首位为自建 Gitee 发布仓**（gitee.com/zzycxz/codegraph/releases/download——Gitee 国内稳定可达且 URL 自有可控；注意 Gitee 的 GitHub 自动镜像不带 release 附件，资产需按版本手动上传；仓库建好前 404 快速失败自动跳下一源），后接 ghfast.top / gh-proxy.com / ghproxy.net 公共代理逐源尝试，SHA256 校验保证被篡改镜像装不进错文件；②失败通知附行动指引（[codegraph] download_url 配可达镜像 / 手动放缓存）；③research 提示词补"codegraph 不可用时 grep 兜底"（explore 原有，review/security-review 本就带 or grep）。**结构性方案（已实施 ✅ 同日升级）**：按平台随包 embed runtime——`//go:build codegraph_embed` 的 go:embed（internal/codegraph/embedded.go）+ release.yml 构建前按 matrix 平台抓取资产暂存 assets/codegraph_runtime.bin（gitignore），发布构建三平台命令全部加 tag。**发布版首跑零网络，"下载不下来"这一失败类别整体消失**；嵌入式字节同样过编译期 SHA256 表（管线抓错平台/版本会被兜到镜像链而非解出垃圾）。开发构建不打 tag，回落到镜像链（ghfast/gh-proxy/ghproxy + GitHub，均校验和保护）。**Gitee 自建镜像方案彻底放弃（两轮否决）**：第一轮因"每版手动传资产麻烦 + 不信任小站分发"否决；第二轮尝试自动化（Actions 拉取校验后经 API 传 Gitee）仍因维护成本否决——根本原因是 **embed 落地后 Gitee 已无服务对象**：发布版用户零网络安装，下载链只剩开发构建兜底，公共镜像即可，不值得为它维护令牌/工作流/资产同步。镜像链终态（第三轮收敛，**公共镜像全部移除**）：自定义 download_url（内网自建）→ GitHub 直连，仅此两源。理由：①SHA256 只保护内容完整性，不消除对陌生端点的元数据暴露与可用性依赖（ghfast/gh-proxy/ghproxy 匿名运营、域名易主）；②embed 已让发布版零网络，兜底链只服务开发构建，开发者可达 GitHub，公共镜像无服务对象。**遗留的深层缺口（明确知情并接受）**：校验表锚定"我们选了什么"而非"上游可信"——若 colbymchenry/codegraph 上游被攻破且新版被照常 bump 采纳，毒会平等穿透镜像/直连/embed。缓解：Version bump 是人工动作即人工审查闸口，升级上游版本时需人工核对该 release 的来源与内容。参照：Reasonix 的更新子系统解决"升级"不解决"首装"，首装根治只在分发层（embed 即分发层根治）。

**双分发模式（2026-08 确认）**：Windows 同发 NSIS 安装器 + 免安装便携 exe（同一 `-tags codegraph_embed` 构建产物，均内嵌 runtime；便携版此前被 CI 打包步骤丢弃，已改为双产物 `-installer.exe`/`-portable.exe`）。**manifest 钉死 installer**：Windows 更新器对下载物直接当 NSIS 安装器启动（updater.go applyWindows），latest.json 的平台键必须确定性指向安装器——cmd/sign 的 matchPlatform 跳过 `-portable.`（与 .deb 跳过同款先例），否则 map 覆盖顺序会随机让便携版抢到更新通道。

**第三方 SaaS 集成的形态决策（2026-08）**：figma / github / linear 一类外部服务集成**保持 MCP 可选自装形态，不内置、不预装**——第三方 SaaS + 各自鉴权 + 按用户可选 + 独立演进，正是 MCP 的适用面（套 5.2 判据三条全中）。bridge.ts 里的三条 figma/github/linear 是前端 dev mock（撑 MCP 面板三种状态展示，不进产物），保留。接入优先级建议：github（编码域 PR/issue 工作流，增益最大；bash 裸用 gh CLI 是凑合兜底）> linear（办公域项目管理，看用户画像）> figma（前端设计还原，截图 + VLM 已有粗路径兜底）。想锁域时往对应 profile 的 hidden_plugins 名单加行即可。

实施要点：

- **修历史颠倒**：netdev-help 原先不进任何白名单，在 netdev 运维模式可见纯属 boot 名单漂移的侥幸——现显式进 netdev 白名单。
- **research 归纯编码域**（二次修正）：曾同时挂在编码+运维两行；其工具面（codegraph/grep/read_file）与输出纪律（file:line 引用）全是代码向。运维查厂商文档走主循环 web_fetch 直连 + netdev-help 源清单，不需要代码调研子代理。**（2026-08-20 更新：按用户方向「运维继承编码全集」，research 连同整个编码技能集回到 netdev 白名单——知识可见性放开，行为仍由封印约束。）**
- **cowork 从"全量暴露"改为办公白名单**：编码技能和 netdev-help 不再出现在办公索引；与其 HiddenTools（藏编码工具 schema）的既有意图对齐。
- **dev 补 explore**：原白名单漏了它（同样靠漂移漏网可见）。
- **HiddenPlugins 新机制**：`Plugins` 白名单会隐藏一切未点名者（含用户自装 MCP），不能用于 cowork/netdev；新增 `hidden_plugins` 只隐藏**点名**的服务器——内置 profile 用它挡 codegraph/context7，用户为办公装的飞书/日历 MCP 不受影响，toml 可覆盖。
- **修 codegraph 注入绕过**：codegraph server 原先在插件过滤之后直接 append 进 bgSpecs，Plugins 白名单根本拦不住（注释声称能拦，实际从未生效）；注入点现已自查 allowed + hidden。
- **白名单只枚举出厂技能**（boot.go `builtinBuiltinSkillNames`，16 项 = 15 代码内置 + ppt-auto），用户自装文件技能不受影响、各模式照常可见。
- 前端能力面板按后端 `active` 标记分组展示，白名单改对后 GUI 自动跟随，无需前端改动。

---

## 三、已实施：computer-auto → desktop-auto ✅

### 动机

"computer-auto"名字承诺了整台电脑，实际只有"看屏幕 + 控制鼠标键盘"。对电脑的直接操作（查系统信息、管文件 / 进程 / 服务 / 设置）显然直接代码调用最好——准、快、可靠。名字与职责不符会误导路由。

### 三处行为性修改（不只是换名字）

1. **技能体收窄**（`internal/skill/builtins.go` `builtinDesktopAutoBody`）：开头新增 Scope 段——子代理发现任务不需要 GUI 时必须停下，让父级改用直接代码，不模拟按键。描述改为 "Desktop GUI automation ONLY … never simulate a GUI for what code can do"。
2. **cowork 路由表**（`internal/config/profile.go`）：该行改为 desktop-auto，并在 web 查询原则后新增对应桌面原则——无界面任务直接跑代码（Windows 上 PowerShell），只有真正需要看见并点击图形应用才委派。
3. **中英文界面描述**（`desktop/frontend/src/locales/{zh,en}.ts`）：`caps.skillDesc.desktop-auto` 明确写出"无需界面的电脑操作不归它管——直接用代码调用更准更快"。

### 同步的引用点

boot.go 两处注释与 `builtinBuiltinSkillNames` 名单、`internal/tool/tool.go` 与 `internal/tool/builtin/image_understand.go` 注释、前端 `CapabilitiesPanel.tsx` 的 OFFICIAL/OFFICE 技能集合、6 个 docs 文件（含 HIQ_FEATURES.md ASCII 表格对齐修补）。

### 迁移注意

用户配置 `[profile] enabled_skills` 白名单里若写过 `computer-auto`，旧名字失效（表现为该技能不再被强制启用；默认 cowork 全启用故一般无感）。如需兜底可在启动时加一条旧名 → 新名迁移。

---

## 四、职责不清问题清单（待办）

### 4.1 explore 描述抢 review/security-review 的活 ✅（已修复）

- **现象**：explore 描述结尾 "Also covers code review: ask it to 'review the current branch diff for correctness/security'…"，与 review、security-review 两个专职技能正面冲突。
- **影响**：模型路由摇摆；review 系技能被绕过。
- **修法**：删掉 explore 描述里那句，探索归探索、评审归评审。

### 4.2 ppt-auto 与 document-auto 都声称管 .pptx ✅（已修复）

- **现象**：document-auto 描述明确列 PowerPoint 读写 / 填充 / 转换；ppt-auto 负责从主题生成。两边描述都没划界。
- **影响**："做个 PPT"可能路由到 document-auto（填模板），质量远差于 ppt-auto（SVG 管线）。
- **修法**：明确分界——**从零生成 → ppt-auto；读取 / 填充 / 转换已有文件 → document-auto**。两边描述各加一句互相指路；cowork 路由表已隐含此分法，技能自身描述需对齐。

### 4.3 boot.go `builtinBuiltinSkillNames` 名单漂移 ✅（已修复）

- **现象**：名单含 `ppt-auto`（非代码内置，但按名禁用文件技能也生效，歪打正着可用），**漏 `explore` 与 `netdev-help`**。
- **影响**：配置 EnabledSkills 白名单时这两个拦不住——白名单形同虚设的口子；注释自称 "drift only cosmetic" 名不副实。
- **修复**（2026-08，随三分域划分一起）：名单补齐 `explore`、`netdev-help`（16 项），注释改述漂移的真实后果（"drift lets a skill escape every whitelist"）；配合 dev 补 explore、netdev 收编 netdev-help、cowork 建办公白名单——见 Part 二"三分域归属"。

### 4.4 screen 工具注释过时 ✅（已修复）

- **现象**：boot.go 注册 ScreenTools 处注释说经 `run_skill("desktop-auto")` **或 `run_skill("ppt-auto")`** 子代理可达；但 ppt-auto 的 allowed-tools 已无任何 screen_* 工具（只有 bash / 读写文件 / web）。
- **修复**（2026-08 复检轮）：删去 "or run_skill(\"ppt-auto\")" 半句。

### 4.5 netdev-help 描述中英混排 ✅（已修复）

- **现象**：索引里唯一中文描述（其余全英文），模型读到的索引中英混杂。
- **修法**：描述改英文（正文可保留中文参考源名）；或接受现状（影响轻微）。

### 4.6 install-capability 术语不统一 ✅（已修复）

- **现象**：名字叫 capability，GUI 叫"技能与 MCP 插件管理器"，描述里 MCP servers and skills 并称。
- **修法**：保持名字（改名成本高），描述开头补一句 "capability = skill 或 MCP server" 的自解释。

> expert-auto 与 review 都含"评审"但对象不同（文档内容 vs 代码 diff），描述已写清，不动。

### 4.7 复检记录（2026-08 第二轮）

全面复检通过项：改名无残留（仅存 219 行的改名理由注释）；netdev 提示词已正确引用 netdev-help（`profile_netdev_addon.go` 第 32 行"full quick-reference lives in the netdev-help skill"）；HIQ_FEATURES / GUIDE 无"全量技能"类过时表述；cowork 路由表八行全部命中办公白名单，无路由到已禁技能。

本轮顺手修复：4.4 注释半句；`PluginAllowedByProfile` 注释称"大小写不敏感"但实现是精确匹配——改为 `EqualFold`，与 `PluginHiddenByProfile` 行为一致。

复检新增待办：23-25（见待办清单）。

---

## 五、体系设计判据：内置-外置 split 与调用控制

### 5.1 ppt-auto 为什么外置

代码内置技能的形态 = 名字 + 描述 + 提示词字符串 + 工具白名单，一个 Go 结构体。ppt-auto 是 **40MB、上万文件的资产树**，提示词里引用文件路径（`<skill_dir>/setup_python.sh`、`references/animations.md`），运行时磁盘上必须存在——Go 字符串装不下。

其"外置"实为**混合形态**：资产 embed 进二进制，首启释放到 `~/.hiq/skills/ppt-auto/`。语义细节：

- **自愈**：目录被删，下次启动 `EnsurePPTAutoSkill` 重新走释放（`readVersion` 失败即重写）。
- **版本覆盖**：SkillVersion bump 时，与嵌入内容不同的文件会被覆盖——**用户对释放副本的修改会被新版本冲掉**（内容相同时跳过写入，避免每次 bump 重写上万个图标文件拖慢启动）。

### 5.2 两个判据问题

1. **这个技能除提示词外还有没有资产（脚本 / 模板 / 参考资料）？** 有 → 只能外置（embed + release）。
2. **它是不是某个 Go 工具族的路由外壳？** -auto 家族的 AllowedTools 全部指向 boot.go 注册并隐藏的 Go 工具（`screen_perceive` / `email_send` / `rag_search` / `doc_read`…），提示词与 Go 工具注册是一份契约的两个面；路由表又编译在二进制里。是 → 留代码内，让契约与路由同生共死。

按此判据：**ppt-auto 外置（唯一资产型）；browser/desktop/email/rag/schedule/document/expert-auto 留内置（提示词外壳，零资产）；init/explore/research/review/security-review/test/install-capability/netdev-help 留内置（引擎功能）。**

### 5.3 关键机制：遮蔽（已存在的 override 通道）

`Store.List()`（`internal/skill/skill.go`）合并顺序：**project → custom → global → builtin 垫底，同名文件技能直接遮蔽内置**。用户今天往 `~/.hiq/skills/browser-auto/SKILL.md` 放一份同名技能即可覆盖内置的提示词 / 描述 / 工具表。内置 = 默认值，文件 = 用户覆盖，优先级设计现成。

### 5.4 整体外置 -auto 家族的代价（不采纳）

1. **版本同步 ×7**：每个外置技能一套 SkillVersion 机制；ppt-auto 一个已手动 bump 到 43。
2. **契约漂移**：文件里的 allowed-tools 与新版二进制的 Go 工具名脱节，报错发生在用户运行时，远离变更点。
3. **核心路由可被删坏**：路由表编译死、引用 `run_skill("email-auto")`；外置后用户在 GUI 删除即断核心流程（ppt-auto 可容忍是因为它是内容生成器且删后重启自愈）。
4. **零体积收益**：Go 工具本来就在二进制里，提示词才几 KB。

### 5.5 决策

- **维持按资产判据 split**：提示词外壳留代码，资产包走 embed + release。
- **补"派生可编辑副本"**（新功能，小改动）：能力面板上加动作——把某内置技能提示词落成 `~/.hiq/skills/<同名>/SKILL.md`，遮蔽即生效；内置默认不动，删副本即还原。这让"数字员工可定制"对用户可见，而不必外置任何内置技能。
- **释放机制泛化时机**：出现第二个资产型技能（如报表 / 海报生成）时，再把 `EnsurePPTAutoSkill` 单技能硬编码泛化为"嵌入技能清单（名字 → 版本 → 目录）"表驱动。为 1 个技能做泛化是提前抽象。

### 5.6 专项技能调用控制三原则

专项（-auto 类）技能的成本结构 = 模型回合数 × 单回合上下文。设计纪律：

1. **一次进入，整任务委派**（父级侧）：委派完整子任务，不微委派——cowork 路由提示词已明文（"Do NOT micro-delegate"）。✅ 达标。
2. **模型回合最小化**（技能侧）：机械循环必须下沉到单次大调用（脚本 / sidecar），模型只做判断与内容生成。**browser-auto 是体系标杆**（browser_auto 一次调用自主跑完多步网页任务，手动工具仅作 sidecar 失败回退）；email / rag / schedule / document / expert 天然合规（工具粒度 = 任务粒度，一次读写即完整工作单元）。**违反者见 5.7 审计表。**
3. **回合预算显式化**（harness 侧）✅ 已实施：SKILL.md frontmatter 新增 `max-steps:`（Skill.MaxSteps 字段，连字符/下划线两种拼法都收），boot 的 skillRunner 优先用它，未设维持"主循环一半（下限 5）"的现状。ppt-auto 已设 80（一份 10 页 deck 约 40-50 回合的预算上限，防无界跑）。

### 5.7 调用控制审计（2026-08，全技能）

| 技能 | 模式 | 判定 |
|---|---|---|
| browser-auto | browser_auto 单次自主调用（sidecar） | ✅ 标杆 |
| email-auto / rag-auto / schedule-auto / expert-auto | 工具粒度 = 任务粒度 | ✅ 天然合规 |
| document-auto | doc_read → doc_write 各一次；模板填充单调用 | ✅ 合规 |
| test | 有界重试（同失败 2 次即停） | ✅ 合规 |
| **ppt-auto** | 模型当渲染引擎：逐页手写 SVG + 每页 fix/check 往返 + QA 回路 | ❌ 最大违反者（Part 六 5 刀治理，items 10-14） |
| **desktop-auto** | 原"每步必复验"已改**检查点复验** ✅：连续输入序列只在组末/状态应改变处（打开对话框、菜单、提交、切页）及最后一动作后复验；菜单保持逐步（瞬态元素） | ✅ 已修复 |

harness 级观察项（**不采纳**）：complete_step 每步一次额外回合，ppt-auto 一份 deck 多付 8-10 回合。允许批量 complete 可省，但削弱防幻觉的证据链——风险大于收益，维持现状，待骨架生成器落地后自然减码。

---

## 六、ppt-auto 提速与调用侧优化（待办）

### 6.1 慢在哪：10 页典型 deck（路线 A）的耗时解剖

| 环节 | 机制 | 量级 |
|---|---|---|
| **逐页手写 SVG**（核心大头） | 模型在 effort:high 下一页一页 `write_file` 整页 1280×720 SVG（每页 4-8K token），每页再跟 fix_svg + check_svg 两次 bash 往返 → **每页 3 个 agent turn** | 10 页 ≈ 30 个回合，且上下文随页数累积，后面的页越来越贵（近似二次增长）。**占总时长 6-7 成** |
| QA 回路 | 每页渲染 PNG + VLM 判定（qa_compare 已 3 并发），最多 2 轮 + 修图往返 | 20 次 VLM × 10-30s，并发后仍 1-3 分钟 |
| PDF 参考补齐 | analyze_pdf_pages 每批 8 页 VLM（防 2 分钟 bash 超时），可续传 | 30 页 PDF 要 4 批，期间技能干等 |
| 机械步骤 + Python 冷启动 | Steps 0/3/4 各占一个回合；每页 2 次脚本冷启动（实测 `import pptx` 1.4s） | 每项几百毫秒到几秒，×20 次后可感 |

### 6.2 预置能救什么（结论：只有边缘）

- **首启依赖预装**：setup_python 挪到应用安装/首次启动后台跑——只救第一次使用的悬崖。
- **图标名索引**：11600 个图标目前没有名字检索入口（见 6.4-3）。
- **桌面端预分析全量化**：参考 PDF 预分析从"前 6 页"（提交路径限速）扩成后台全量——消除技能内分批补齐的等待。
- **救不了的**：核心 token 产量（模型逐页产 SVG）——预置无从下手，只能靠 6.3-1 的骨架生成器绕开。

### 6.3 流程优化（按收益排序）

1. **页面骨架生成器家族（最大的一刀）**：`build_table_skeleton.py` / `build_flow_skeleton.py` 已验证模式——模型给紧凑参数，脚本机械产 SVG。推广到封面/目录/三卡/两栏/章节页/结尾/图表页（`templates/charts/charts_index.json` 已存在），模型从"写 5K token 的 SVG"变成"写 300 token 的 page-spec JSON"。Step 5 的 design_spec 本就是结构化的，顺势直接喂生成器；模型保留 edit_file 做周边微调（同表格页现行规则）。
2. **preflight 合并**：Step 0（ls 模板 → extract_template_colors → merge_vlm_style）+ Step 3（读 config）+ Step 4（init）共 5-6 个回合，合成一个脚本一次往返、输出合并 JSON。
3. **批量 fix+check**：一个 bash 调用在单解释器里循环（或多进程）处理全部页面，省 2×N 次往返与冷启动。
4. **QA 分级挂 mode**：现在 `fast/validate` 只控制 check_svg 严格度，QA 回路照跑全量。fast 模式应跳过或抽样 QA（封面 + 随机 2 页）。
5. **教模型用图标占位符**：`svg_finalize/embed_icons.py` 早已支持 `<use data-icon="tabler-outline/home">` 语法，但 SKILL.md 正文一字未提——图标库基本白放，模型在用 SVG path 手画图标浪费 token。正文补占位符用法 + 跑 embed_icons 的时机。

### 6.4 调用侧提示词问题（四处）

1. **SKILL.md frontmatter description 过时过窄**（路由模型唯一可见的）："使用PPT模板生成演示文稿"——没模板是常态；漏了 Beautify 美化、PPTX 反解、修改模式（R1-R4）、PDF 参考重绘四条路线；没和 document-auto 划界（4.2 的分界：从零生成→ppt-auto，读/填/转已有→document-auto）。
2. **cowork 路由表 ppt-auto 行**：中文混在英文表里、暴露实现细节（"使用SVG路径"）、没告诉模型"任务参数带已有 project_dir 时走修改模式不重跑全流程"。
3. **icons README 幽灵工作流**：`templates/icons/README.md` 描述的 `icon_sync.py`、`finalize_svg.py embed-icons`、"Strategist 选型时拷贝"全是 ppt-master 移植残留，**脚本不存在**，模型照做必报错。修法二选一：按 README 补 icon_sync.py，或把 README 改成实际的 data-icon 占位符 + `svg_finalize/embed_icons.py` 流程（与 6.3-5 联动）。
4. **SkillSelectModal 描述**（前端）："基于选中知识生成演示文稿"是知识库入口专用尚可；"Word 文档撰写"低估 document-auto 实际职责（读/填/转换）。

---

## 七、待办清单

| # | 事项 | 位置 | 状态 |
|---|---|---|---|
| 1 | computer-auto → desktop-auto 改名 + 代码优先原则 | builtins.go / profile.go / locales / boot.go / docs | ✅ 已实施（本分支） |
| 2 | explore 描述删"also covers code review"句 | `internal/skill/builtins.go` | ✅ 已实施 |
| 3 | ppt-auto / document-auto 划 .pptx 边界（两边描述互指） | builtins.go + ppt-auto SKILL.md | ✅ 已实施 |
| 4 | `builtinBuiltinSkillNames` 补 explore、netdev-help | `internal/boot/boot.go` | ✅ 已实施（随三分域划分，见 Part 二） |
| 5 | 删 screen 工具注释中 "or run_skill(\"ppt-auto\")" | `internal/boot/boot.go` | ✅ 已实施（复检轮） |
| 6 | netdev-help 描述改英文 | `internal/skill/builtins.go` | ✅ 已实施 |
| 7 | install-capability 描述自解释 capability | `internal/skill/builtins.go` | ✅ 已实施 |
| 8 | "派生可编辑副本"GUI 动作（遮蔽通道产品化） | 前端 CapabilitiesPanel + 后端落盘 | ✅ 已实施 |
| 9 | 旧名 computer-auto 白名单迁移兜底 | 启动逻辑 | ✅ 已实施 |
| 10 | ppt-auto 页面骨架生成器家族（封面/目录/三卡/两栏/章节/结尾/图表） | `scripts/build_*_skeleton.py` + SKILL.md Step 5/6 | ✅ 已实施 |
| 11 | ppt-auto preflight 合并（Step 0/3/4 一脚本一回合并输出 JSON） | `scripts/preflight.py` + SKILL.md | ✅ 已实施 |
| 12 | ppt-auto 批量 fix+check（单解释器处理全部页） | SKILL.md Step 6 | ✅ 已实施 |
| 13 | QA 分级挂 mode（fast 跳过/抽样） | `qa_compare.py` + SKILL.md Step 6.5 | ✅ 已实施 |
| 14 | SKILL.md 补 data-icon 占位符用法 + 图标名索引 | SKILL.md Step 6 + icons 目录 | ✅ 已实施 |
| 15 | icons README 幽灵工作流修复（icon_sync 不存在） | `templates/icons/README.md` | ✅ 已实施 |
| 16 | ppt-auto description 重写（五路线全覆盖 + 与 document-auto 划界） | ppt-auto SKILL.md frontmatter | ✅ 已实施 |
| 17 | cowork 路由表 ppt-auto 行改写（去实现细节 + 修改模式提示） | `internal/config/profile.go` | ✅ 已实施 |
| 18 | SkillSelectModal 描述修正 | 前端 SkillSelectModal.tsx | ✅ 已实施 |
| 19 | 首启 Python 依赖预装到应用安装/首次启动后台 | desktop 启动逻辑 | ✅ 已实施 |
| 20 | 桌面端 PDF 参考预分析全量化（现仅前 6 页） | desktop pdf_pages_vision.go | ✅ 已实施 |
| 21 | desktop-auto"每步必复验"改检查点复验（连续输入组末/状态变更处复验；菜单保持逐步） | `internal/skill/builtins.go` desktop-auto body | ✅ 已实施 |
| 22 | per-skill 回合预算：SKILL.md frontmatter `max-steps:` + boot 读取（未设维持减半现状） | `internal/skill/skill.go` + `internal/boot/boot.go` | ✅ 已实施 |
| 23 | HiddenPlugins / codegraph 域门控的测试覆盖（PluginHiddenByProfile 单测 + boot 级"cowork 不加载 codegraph"断言） | `internal/config/profile_test.go` + `internal/boot/boot_test.go` | ✅ 已实施 |
| 24 | render.go 示例配置补 `hidden_plugins` 渲染（新 toml 字段可发现性） | `internal/config/render.go` | ✅ 已实施 |
| 25 | CapabilitiesPanel 补"运维"域分组（现仅 office/其余二分；netdev 模式下 active 的只有 netdev-help，无域标签） | 前端 CapabilitiesPanel.tsx | ✅ 已实施 |
