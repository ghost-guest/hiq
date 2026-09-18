# Pane System Spec — 双轴面板体系(右栏产物轴 + 底部工具轴)

> 状态:P1/P2 已上线;P3 伴随窗口层已上线(2026-08-19)——`OpenURLInManagedBrowser`(browserlaunch.OpenTab + CDP /json/new)打通预览页卡→可控浏览器;**P3.5(Windows 窗口内嵌 SetParent)与 macOS/Wayland 伴随窗口位置跟随仍为后续原生增强**,当前"伴随"= 独立浏览器窗口承接 URL,不做位置同步。
> 关联:docs/ui-redesign-spec.md(版式总纲,本 spec 是其右栏/底部章节的继任设计)
> 设计参考:ZCode 的面板模型(正交双轴 / 上下文驱动 / 产物优先),经用户确认

## 0. 目标与非目标

**目标**

1. 右栏升级为**页卡式产物轴**:文件 / 改动 / 预览(iframe→后续 attach 真浏览器),页卡可保活、可记住状态;
2. 底部升级为**工具轴**:终端页卡(已上线)后续并列"副会话"页卡,实现双输入框并行工作;
3. **上下文驱动**:面板因内容而出现(dev server 起来→预览自动开),不空转、不打扰;
4. Windows / macOS / Ubuntu 三平台:前两期纯前端零差异,原生差异隔离在 P3 能力探测层。

**非目标**

- 不内置任何浏览器内核(CEF 类 +150MB 方案明确否决);
- 不做页卡拖拽排序、拖出独立窗口(P4 再评估);
- 不改变现有聊天主区与侧栏结构。

## 1. 设计语言(ZCode 三原则)

1. **正交双轴**:右栏只放"看的东西"(产物),底部只放"干活的东西"(工具);两轴独立开合,互不挤占;
2. **上下文驱动**:面板默认不存在;有内容可看才出现;用户手动关闭后本轮不再自动弹(尊重操作);
3. **产物优先**:页卡命名显示"东西本身"而非工具名(预览·web-app:5173,不是"浏览器")。

## 2. 现状盘点(改造基线)

| 已有 | 状态 | 复用 |
|---|---|---|
| RightDockMode = "context"\|"files"\|"changed" | App 级状态 | 扩展 "preview" |
| WorkspacePanel 内部 files/changed 双视图 | showViewTabs=false 隐藏 | 页卡切换由 App 级 DockTabs 接管,mode 切换时 remount(initialViewMode 仅挂载时生效) |
| 右栏拖宽分隔条 / 最大化 | 本轮已修复 | 直接复用 |
| 终端面板(底部,页卡+增+删) | 已上线 | 工具轴第一块积木 |
| savedWorkspacePanelWidth | 已有 | 补"记住最后激活页卡" |

## 3. 架构

### 3.1 页卡注册表(v2 演进方向, P1 先用枚举)

P1 用 `RightDockMode` 枚举 + App 级 `DockTabs` 组件(轻量,三页卡不值得抽象)。
P2 起若页卡 ≥4(副会话/浏览器)再抽 `{id, icon, title, component, capability}` 注册表,接口预留:
页卡组件约定 `{ keepAlive?: boolean }`,保活实现复用 cowork 面板的 display:none 挂载模式。

### 3.2 预览页卡(P1)

- 结构:迷你工具条(地址只读显示 / 刷新 / 在系统浏览器打开)+ `<iframe>`;空态显示"等待本地服务"+ 手动地址输入;
- URL 来源(优先级):①工具输出自动探测 ②手动输入(localStorage 记忆) ③上一轮记忆;
- 探测规则:监听聊天事件流中 bash 工具的 output/result,正则 `(localhost|127\.0\.0\.1):(端口)` 取最新命中;同一 URL 变化才重新触发自动打开;
- 安全:仅允许 http(s) URL;iframe 原生遵守 X-Frame-Options,被拒站点显示"该站点禁止嵌入,已用系统浏览器打开"降级。

### 3.3 自动触发器(P1 核心)

| 事件 | 行为 | 防扰 |
|---|---|---|
| 探测到新 dev server URL | 右栏未开→自动开并切预览页卡;已开→预览页卡标签亮提示点 | 用户本轮手动关过右栏→本轮不再自动开 |
| turn_done 且有文件改动 | "改动"页卡亮提示点 | 用户切到改动页卡即清除 |
| 手动关闭 | 无 | — |

### 3.4 状态记忆

- `hiq.rightDockMode`:最后激活页卡(files/changed/preview,context 不记忆);
- `hiq.previewUrl`:手动输入过的地址。

### 3.5 副会话页卡(P2, 底部工具轴)

- 位置:终端面板页卡条的并列页卡(终端 | 副会话),复用同一 tab strip 语法;
- 语义:**并行会话**(用户确认方案 A)——精简 Transcript + 迷你 Composer,模型/审批模式跟随主会话,顶部下拉选择副会话;
- 主会话运行长任务时,副会话可独立对话;steer 语义不占页卡。

### 3.6 浏览器页卡(P3, 原生)

- 能力分级:探测→ Windows/X11 尝试窗口嵌入(SetParent/XReparentWindow);macOS/Wayland 降级**伴随窗口**(置顶独立窗停靠于右栏矩形旁,跟随移动);
- 上线形态:**替换预览页卡的内核**(同一页卡,iframe→真浏览器,用户无感),不新增页卡位;
- 依赖:internal/browserlaunch、desktop/managed_browser.go、attach 同步节流、进程回收。

## 4. 分期

- **P1(本次)**:DockTabs + 预览页卡(iframe)+ URL 探测/手动输入 + 自动触发器 + 提示点 + 页卡记忆;
- **P2**:副会话页卡(底部轴);
- **P3**:attach 能力分级 + Windows 嵌入 → 伴随窗口降级;预览页卡内核替换;
- **P4(可选)**:页卡注册表抽象、拖出独立窗口、页卡排序。

## 5. 功能保全矩阵(红线:零丢失)

| 现有功能 | 去向 |
|---|---|
| 文件树/文件预览(WorkspacePanel files) | 文件页卡,原样 |
| 改动列表/diff(WorkspacePanel changed) | 改动页卡,原样 |
| 右栏拖宽/最大化/收起 | 原样(本轮已修) |
| workspacePreviewActive 媒体预览宽度 | 原样(预览页卡不占用该宽度逻辑) |
| 终端面板 | 工具轴,原样 |
| 改动按钮(原 topicbar→右键菜单) | 触发器改为自动 + 改动页卡,右键菜单入口保留 |

## 6. 后端边界

P1/P2 **零后端改动**(URL 探测在前端事件流内完成)。
P3 涉及 Go(browserlaunch 能力探测/窗口同步/进程管理),另行评审,不阻塞 P1/P2。

## 7. 验收

- [ ] 右栏三页卡可切换,文件/改动功能与改前一致(diff 可看、可加聊);
- [ ] AI 跑 `python -m http.server 8000` 类命令后,右栏自动开并显示预览;
- [ ] 手动关右栏后,同 URL 不再自动开;新 URL 重新触发;
- [ ] 改动发生时改动页卡有点,切过去清除;
- [ ] 重启后最后页卡/手动地址恢复;
- [ ] dev 构建绿;tsc/三 CSS 守卫过;
- [ ] 预览 iframe 在 WebView2/WKWebView/WebKitGTK 行为一致(手动过三平台或 CI 截图)。

## 8. 后续扩展:浏览器产物轴(已落地部分)

产物轴家族的两个后续成员(2026-08-29),沿用 §1 三原则与 §3 的右栏骨架:

- **办公 dock「浏览器」镜像页**:agent/控制台驱动的浏览器画面实时镜像。数据通道为内核 `browser:mirror` 事件(browser.go 每次动作后截图 + 会话起止状态;browser_auto 边车截图帧同通道),面板读共享 store,挂载即订阅、卸载不丢流;活动开始自动开 dock(anti-nag 同 §3.3)。
- **运维 dock「浏览器」页卡(ZCode 胶囊风子页卡 交互/录制)**:产物轴与技能系统的合流——手动驱动浏览器(11 原语直调既有 agent 工具),录制手动操作经三道去噪 + AI 理解生成四段式 SKILL.md(步骤表=工作流,skill ⊃ 工作流),结构化⇄源码编辑器(防丢失护栏)人审后落 `~/.hiq/skills/`。详见 CHANGELOG [Unreleased]「运维浏览器控制台」条。
