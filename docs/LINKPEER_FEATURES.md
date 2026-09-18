# linkpeer 功能规格（FRD）

> 状态：v1 草案
> 日期：2026-08-11
> 范围：定义 linkpeer **做什么功能、做到什么程度、数据怎么存、页面怎么转**。是产品维度的完整定义。
> 相关：架构见 [`MOBILE_CLIENT_PLAN.md`](./MOBILE_CLIENT_PLAN.md)；连接协议见 [`LINKPEER_PROTOCOL.md`](./LINKPEER_PROTOCOL.md)。

---

## 1. 功能边界（做什么 / 不做什么）

### 1.1 linkpeer 做

- 实时镜像桌面 hiq 的对话事件流。
- 远程下达命令（追问 / 批准 / 暂停 / 取消 / 切计划模式 / 触发办公）。
- 触发桌面端办公自动化（Word/Excel/PPT/文档转换）并预览结果。
- 文件投递（手机→桌面工作区）。
- 多桌面端管理。
- 历史会话本地缓存浏览（断网可看）。

### 1.2 linkpeer 不做（明确边界）

- ❌ **不跑模型** —— 无 LLM 推理，所有 AI 算力在桌面。
- ❌ **不存 API Key** —— Provider 密钥只在桌面 hiq。
- ❌ **不存原始文件** —— 桌面文件不复制到手机；手机只有指令 + 结果预览。
- ❌ **不脱离桌面独立工作** —— 无连接时只能看本地缓存历史，不能新对话。
- ❌ **不承担 IM 功能** —— 不是聊天工具，不互通第三方。

这条边界是 linkpeer 的安全承诺基础（§1.1 of PROTOCOL）。

---

## 2. 功能清单（按优先级分级）

### P0（MVP，M4 必须有）

| 功能 | 描述 |
|---|---|
| **扫码配对** | 扫 hiq 桌面二维码完成绑定，指纹本地校验 |
| **实时对话镜像** | wireEvent 流渲染（文本/思考/工具卡/diff/usage） |
| **流式输入** | 输入框发指令（submit），流式看回包 |
| **审批交互** | 高危工具调用弹底部 sheet，允许/拒绝 |
| **会话控制** | 暂停 / 取消 / 切计划模式 |
| **历史会话浏览** | 侧抽屉列出最近会话，点开看缓存内容 |
| **连接状态** | 顶栏显示连接质量（直连/中继/失败）+ 重连 |
| **多桌面切换** | 顶部下拉切当前桌面（单活动连接） |
| **设置** | 解绑设备、只读开关、深色模式 |

### P1（M5 + M6 前后）

| 功能 | 描述 |
|---|---|
| **办公触发** | 选 Word/Excel/PPT 模板，填参数，点执行，看结果 |
| **文档转换** | 触发桌面 PDF/Markdown/HTML 等转换 |
| **文件投递** | 手机选照片/文档投到桌面当前工作区 |
| **本地缓存** | 历史会话落 sqflite，断网可回看 |
| **通知** | 审批请求 / turn 完成 / 错误，前台推送 + 系统通知 |
| **追问编辑** | 长按消息：复制 / 重新生成 / 基于该消息追问 |
| **剪贴板同步** | 手机复制 → 桌面剪贴板（单向，需桌面授权） |

### P2（后期，不阻塞上架）

| 功能 | 描述 |
|---|---|
| **多桌面并发镜像** | 同时连多台桌面分别看 |
| **Widget / 快捷指令** | iOS Widget / Android Quick Tile 发常用指令 |
| **语音输入** | 语音转文字后 submit |
| **Wear OS / Apple Watch** | 轻量审批通知 |
| **会话分享** | 导出某段对话为图/文分享 |

---

## 3. 办公能力清单（P1，对接桌面 hiq 现有能力）

linkpeer 是触发入口，**实际能力由桌面 hiq 的 office 工具提供**。linkpeer 列出可用模板，桌面执行，结果回传预览。

| 类别 | linkpeer 侧 | 桌面侧（已有/对接） |
|---|---|---|
| **Word** | 模板列表 + 参数表单 + 触发 | `internal/tool/builtin/docxtemplate*`（模板填充、表填充、页眉页脚、查找替换） |
| **Excel** | 触发数据处理任务 | `internal/tool/builtin/*excel*`（excellize 读写） |
| **PPT** | 模板选择 + 触发 | `internal/tool/builtin/ppttemplate` + `ppttemplate_vision` |
| **文档读取** | 选文件查看提取内容 | `internal/tool/builtin/docxread`、`officedoc`、`pdf` |
| **文档转换** | 触发转换 | `docconv`、`officedoc` 转换链路 |
| **截图解题** | 拍照/截图投递 → 看解题 | 桌面 `screenshot_solve` 能力 |

linkpeer 不内置这些能力，而是通过 `office_run` 命令（PROTOCOL §4.2）调用桌面，桌面用 wireEvent 回流过程 + 结果。

---

## 4. 文件投递（P1）

### 4.1 方向

**仅手机 → 桌面**（单向）。桌面 → 手机不放（桌面文件不该在手机留副本）。

### 4.2 协议

手机选文件 → 分块加密（PROTOCOL §6 帧格式）→ 桌面收到写入当前工作区指定路径 → 回执。

```
C → S: {"t":"file_start","id":"...","name":"photo.jpg","size":N,"mime":"image/jpeg","toDir":"<workspace>"}
C → S: {"t":"file_chunk","id":"...","seq":0,...base64...}  // 走加密帧
...
C → S: {"t":"file_end","id":"...","sha256":"..."}
S → C: {"t":"file_ack","id":"...","path":"<桌面落地绝对路径>","ok":true}
```

### 4.3 限制

- 单文件上限 50MB（移动流量友好）。
- 桌面端可关掉投递权限（默认开，可禁）。
- 投递目标路径由桌面确认（默认工作区 `incoming/`，避免覆盖工程文件）。

---

## 5. 通知（P1）

### 5.1 触发场景

| 事件 | 通知方式 |
|---|---|
| 审批请求（`ApprovalRequest`） | 前台：底部 sheet；后台：系统通知（点击进 App） |
| Turn 完成（`TurnDone`，且用户切走） | 系统通知 |
| 错误 / 连接断 | 系统通知 + 顶栏红条 |

### 5.2 平台差异

- **Android**：前台服务维持，通知通道（channel）分「审批/完成/错误」三档，用户可分级静音。
- **iOS**：MVP 仅前台（切后台暂停）。后台推送（APNs 静默唤醒）作为后期增强，需信令服务代发推送 token（仅唤醒信号，不含内容）。

### 5.3 推送内容零知识

任何系统通知**不含业务明文**——只显示「桌面 hiq 请求审批」「任务完成」等模板文案。业务详情必须用户点开 App、走 P2P 拉。

---

## 6. 数据模型（linkpeer 本地）

### 6.1 存储选型

- `flutter_secure_storage`：Ed25519 私钥、session token（敏感）。
- `sqflite`：结构化数据（设备、会话缓存、设置）。
- 文件系统：仅缓存目录（预览图等临时文件）。

### 6.2 sqflite schema

```sql
-- 已配对桌面
CREATE TABLE devices (
  dev_id      TEXT PRIMARY KEY,    -- base32(SHA256(pubS)[:10])
  label       TEXT,                 -- 用户起名"公司电脑"
  pub_s       TEXT,                 -- Ed25519 公钥 base64
  fp_s        TEXT,                 -- 指纹（便于 UI 显示）
  paired_at   INTEGER,
  last_connected_at INTEGER,
  last_state  TEXT                  -- "online"|"offline"|"unknown"
);

-- 会话缓存（每会话存最近 N 条事件）
CREATE TABLE session_cache (
  dev_id      TEXT,
  session_path TEXT,                -- 桌面端 .jsonl 路径，作为会话标识
  title       TEXT,
  last_event_seq INTEGER,           -- 已缓存到桌面第几条
  last_ts     INTEGER,
  events_json TEXT,                 -- JSON 数组：[{kind,text,...}, ...] 最近 N 条
  PRIMARY KEY (dev_id, session_path)
);

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT
);
```

### 6.3 缓存策略

| 项 | 值 | 说明 |
|---|---|---|
| 每会话缓存条数 | 最近 200 条事件 | 控制单会话体积 |
| 缓存总配额 | 100MB | 超限按 LRU 清最旧会话 |
| 增量同步键 | `last_event_seq` | 重连后只拉 seq 之后的 |
| 全量刷新 | 用户下拉刷新触发 | 重新拉完整会话 |

### 6.4 冲突处理

linkpeer 不编辑桌面数据，**只读镜像**，无写冲突。唯一"写"是上行命令——桌面 Controller 自己串行排队处理。

---

## 7. 页面清单与流转

### 7.1 页面树

```
App 启动
 ├─ 首次（无 device）→ 引导页 → 扫码配对页 → 主界面
 └─ 已配对 → 主界面
      ├─ 对话 Tab（默认）
      │    ├─ 侧抽屉：会话列表
      │    ├─ 顶栏：桌面切换 / 模型 / 菜单
      │    ├─ 消息流
      │    ├─ 底部输入区
      │    └─ 弹层：审批 sheet / 文件选择器 / 长按菜单
      ├─ 办公 Tab
      │    ├─ 模板网格
      │    ├─ 参数表单（选模板后）
      │    └─ 结果预览
      ├─ 我的 Tab
      │    ├─ 桌面列表 + 连接状态
      │    ├─ 设置（只读/批准/投递权限）
      │    ├─ 诊断（连接自检）
      │    └─ 关于
      └─ 全局：连接质量条 / 错误条 / 通知
```

### 7.2 每页状态

| 页面 | 空 | 加载 | 错误 |
|---|---|---|---|
| 对话 | "新对话"引导 + 快捷指令建议 | 连接中骨架屏 | 连接失败 → 诊断入口 |
| 会话列表 | "暂无会话" | 拉取中 | 拉取失败 → 重试按钮 |
| 办公 | "桌面端未启用办公" | 执行中进度 | 执行失败 → 详情 |
| 审批 sheet | — | — | 桌面已取消该请求 |
| 诊断 | — | 各项检测中 | 红绿项 + 建议 |

### 7.3 关键流转

**配对**：引导 → 扫码 → 指纹确认 → 成功 → 主界面（对话 Tab）。
**审批**：消息流中插 approval 卡 → 点开 sheet → 允许/拒绝 → 卡片状态更新。
**重连**：连接断 → 顶栏"重连中" → 自动 backoff → 恢复或失败提示 → 手动重试 / 诊断。

---

## 8. 权限管理

### 8.1 移动端系统权限

| 权限 | 用途 | 何时申请 |
|---|---|---|
| 相机 | 扫码配对 | 配对页 |
| 网络 | P2P + 信令 | 启动 |
| 前台服务（Android） | 保活 DataChannel | 连接时 |
| 通知 | 审批/完成提醒 | 首次审批前 |
| 文件/照片（P1） | 投递到桌面 | 投递时 |

遵循「用到才申请」，拒绝不崩溃（降级提示）。

### 8.2 桌面端授权（S 对 C 的权限）

每台 C 在 S 侧可独立配置（PROTOCOL §6 权限分级）：

| 权限 | 说明 | 默认 |
|---|---|---|
| 只读 | 只看不动 | 关 |
| 操作需批准 | linkpeer 提交也要桌面批准 | 关 |
| 文件投递 | 允许手机投文件到桌面 | 开 |
| 高危工具 | 允许移动端触发 shell/写文件类工具 | 关 |
| 办公触发 | 允许触发办公任务 | 开 |

---

## 9. 国际化（i18n）

- 复用 hiq 的 i18n 思路（`desktop/frontend/src/locales/en.ts`、`zh.ts`）。
- linkpeer 首发中英双语，按系统语言自动切。
- 协议消息（wireEvent / cmd.*）**不含本地化文案**——文案由各端自己渲染时查表。桌面 hiq 的 Notice 文本可能是中文（来自桌面用户语言），linkpeer 原样显示（不翻译用户内容）。

---

## 10. 无障碍

- 所有交互元素加 `Semantics` 标注。
- 支持系统字体缩放（与 hiq `--font-scale` 思路一致）。
- 对比度满足 WCAG AA。
- 审批 sheet 大按钮，触控目标 ≥48dp。

---

## 11. 与桌面 hiq 的功能对齐

linkpeer 的能力边界 = hiq 桌面已有能力的一个子集映射。每条 linkpeer 功能都对应桌面端既有方法（无新业务逻辑）：

| linkpeer 动作 | 桌面方法 | 状态 |
|---|---|---|
| submit | `App.SubmitToTab` | 已有 |
| cancel | `App.CancelTab` | 已有 |
| approve | `App.ApproveTab` | 已有 |
| pause/resume | `App.PauseTab` / `ResumeTurnTab` | 已有 |
| set_plan | `App.SetPlanMode` / `setPlanModeForTab` | 已有 |
| list_sessions | `loadSessionTitles` / `desktopSessionDir` | 已有 |
| load_session | 读 JSONL | 已有 |
| new_tab / switch_tab | `createTabEntry*` / tab 管理 | 已有 |
| office_run | 办公工具调用 | 已有（工具） |
| file_drop | 写文件到工作区 | **新增（简单）** |

唯一新增的桌面侧业务点是「file_drop 写入」，逻辑简单（写文件 + sha256 校验），不算新能力。

---

## 12. v1.1 补充：linkpeer 功能完整性审视

复审发现 linkpeer 作为产品还缺这些（按 MVP/P1/P2 分）。已在 §2 清单基础上补充。

### 12.1 MVP 应补（P0 追加）

| 功能 | 说明 | 协议补充 |
|---|---|---|
| 新建对话 | `new_tab` 已有协议，补 UI（"+"按钮、选 workspace） | — |
| 切换模型/effort | 桌面顶栏有，移动端下拉切换 | `set_model` 命令（映射 `App.SwitchModel`） |
| 自动重连 | App 启动自动连上次桌面 | — |

### 12.2 P1 应补（体验关键）

| 功能 | 说明 | 协议补充 |
|---|---|---|
| 会话重命名 | 改桌面 session title | `rename_session`（映射 `saveSessionTitles`） |
| 会话删除/归档 | 桌面有 `.trash`，移动端触发 | `delete_session` |
| 历史搜索 | 本地 sqflite 缓存内全文搜（断网可用） | — |
| 附件作 prompt | 选照片/文件作为对话 context（非投递，是 prompt 内容） | `submit` 加 `attachments` 字段 |
| App 锁 | 生物识别 + 隐私屏幕（SPEC §11.7） | — |
| 通知点击跳转 | 点审批通知跳到对应对话 + tab | — |
| 切换 expert | hiq 有 experts 系统，移动端触发 | `run_expert` 命令 |
| 诊断报告导出 | 脱敏诊断包（连接质量/错误日志）发运维 | — |
| 离线命令队列 | SPEC §11.5 | — |
| 增量同步 | SPEC §11.2（resync） | `resync` 命令 |

### 12.3 P2（锦上添花）

| 功能 | 说明 |
|---|---|
| 快捷指令模板 | 保存常用 prompt 复用（本地） |
| 平板双栏适配 | iPad/Android Tablet 手机+会话列表双栏 |
| 动效 | 消息进入动画、tab 切换过渡 |
| 多语言扩展 | 日韩等（中英 MVP） |
| 隐私屏幕 | 切后台模糊（与 App 锁联动） |
| 截图保护 | 敏感会话禁截屏（Android `FLAG_SECURE`） |
| 勿扰模式 | 通知静音时段 |
| 锁屏通知隐私 | 锁屏只显"有新消息"不显内容 |

### 12.4 不做的（明确排除）

| 功能 | 不做理由 |
|---|---|
| 独立新对话（脱离桌面） | linkpeer 不跑模型，无桌面无意义 |
| 文件存储/管理 | 桌面文件不该在手机留副本（安全边界） |
| IM/社交 | 不是聊天工具 |
| 语音/视频通话 | 用 WebRTC 但只走 DataChannel，不做媒体 |
| 账号系统 | 扫码配对已够，账号是过度设计 |

### 12.5 与桌面对齐性再核对

§11 表基础上，P1 新增命令需对齐桌面：
- `rename_session` → `saveSessionTitles`（已有）
- `delete_session` → 桌面 `sessionTrashPath`（已有 `.trash` 机制）
- `set_model` → `App.SwitchModel`（已有）
- `run_expert` → experts 系统（已有）
- `submit` 的 `attachments` → 桌面需支持（**新增**：把附件写入临时区作为 prompt context）

仍只需 1 个桌面侧新增点（attachments 处理），其余全复用。
