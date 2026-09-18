# dev 与 cowork 完整工具对比

> 基于当前代码（v0.4.0），所有工具名从源码逐行核实。
> 日期：2026-07-05

---

## 设计原则

dev（编码）和 cowork（办公）**共用同一个工具注册池**——所有工具在 `boot.go` 中一次性注册。区别只在于 `Profile.HiddenTools` 控制主循环可见性（`Registry.Schemas()` 排除 hidden）。

- **主循环可见**：模型在 `Schemas()` 里看到的工具，能直接调用
- **隐藏（子代理可达）**：通过 `run_skill` → 子代理 → `FilterRegistry`（`Get`/`Names`）仍可使用

工具过多会稀释弱模型注意力 + 每个工具 schema ~200 token 进 prompt。精简到 21（dev）/ 14（cowork）后省 ~1500-3000 token。

---

## 一、共有工具（14 个）

两个模式主循环都可见的 universal 脊柱：

| 工具 | 说明 | 文件 |
|------|------|------|
| `bash` | 执行 shell 命令（含后台、超时、OS 沙箱、交互检测） | builtin/bash.go |
| `read_file` | 读文件内容、目录列表、递归 walk | builtin/readfile.go |
| `write_file` | 写文件（整文件覆写） | builtin/writefile.go |
| `edit_file` | 编辑文件（old→new 替换，含 replace_all + fuzzy 匹配） | builtin/editfile.go |
| `grep` | ripgrep 搜索文件内容 | builtin/grep.go |
| `glob` | `**/*.go` 递归文件模式匹配 | builtin/glob.go |
| `web_fetch` | 抓取网页内容 | builtin/webfetch.go |
| `web_search` | Brave→Exa→Linkup→AnySearch 链式搜索 | builtin/websearch.go |
| `todo_write` | 两级任务列表（phase + sub-step） | builtin/todo.go |
| `complete_step` | 带证据签收步骤完成 | builtin/completestep.go |
| `ask` | 交互询问（多选问题） | agent/ask.go |
| `task` | 通用子代理（含 continue_from/fork_from） | agent/task.go |
| `run_skill` | 技能门户（调用 inline/subagent skill） | skill/tools.go |
| `background_job` | 后台作业管理（action: output/kill/wait） | builtin/bgjobs.go |

---

## 二、dev 独有工具（7 个，cowork 隐藏）

| 工具 | 说明 | 文件 |
|------|------|------|
| `lsp_lookup` | 符号查询（action: definition / hover / implementation） | lsp/tool.go |
| `lsp_references` | 查找符号所有引用 | lsp/tool.go |
| `lsp_workspace_symbol` | 全局符号搜索（按名称） | lsp/tool.go |
| `codegraph_context` | 代码图谱：入口点 + 相关符号 + 关键代码 | codegraph（MCP 插件） |
| `codegraph_search` | 代码图谱：按名称搜索符号 | codegraph（MCP 插件） |
| `multi_edit` | 同文件多组编辑（一次调用多组 old→new） | builtin/multiedit.go |
| `research` | 代码探索子代理（只读调查 + 可选 web） | skill/tools.go |

---

## 三、cowork 独有工具（51 个，dev 隐藏）

这些工具在 dev 和 cowork 模式下都通过 `reg.Hide` 隐藏——它们不从主循环的 `Schemas()` 出现，但通过 `run_skill` → 子代理 → `FilterRegistry` 可达。各办公 skill 的 AllowedTools 白名单指定子代理能用哪些。

### 浏览器自动化（12 个）

| 工具 | 说明 |
|------|------|
| `browser_open` | 打开浏览器 |
| `browser_attach` | 附加到已运行的浏览器 |
| `browser_navigate` | 导航到 URL |
| `browser_click` | 点击元素 |
| `browser_type` | 输入文本 |
| `browser_scroll` | 滚动页面 |
| `browser_extract` | 提取页面内容 |
| `browser_screenshot` | 截图 |
| `browser_evaluate` | 执行 JS |
| `browser_select_option` | 选择下拉选项 |
| `browser_set_path` | 设置下载路径 |
| `browser_wait` | 等待条件 |

### 屏幕感知与操作（8 个）

| 工具 | 说明 |
|------|------|
| `screen_perceive` | 屏幕感知（VLM 识别元素） |
| `screenshot` | 截屏 |
| `screen_click` | 点击坐标 |
| `screen_type` | 输入文本 |
| `screen_scroll` | 滚动 |
| `screen_key` | 按键 |
| `get_ui_tree` | 获取 UI 树（Accessibility） |
| `image_understand` | 图片理解（VLM） |

### 窗口管理（5 个）

| 工具 | 说明 |
|------|------|
| `window_focus` | 聚焦窗口 |
| `window_maximize` | 最大化 |
| `window_restore` | 还原 |
| `window_move` | 移动 |
| `window_close` | 关闭 |

### 邮件（3 个）

| 工具 | 说明 |
|------|------|
| `email_send` | 发送邮件 |
| `email_read` | 读取邮件 |
| `email_search` | 搜索邮件 |

### 知识库 RAG（6 个）

| 工具 | 说明 |
|------|------|
| `rag_import` | 导入文档（FTS5 + 实体抽取） |
| `rag_search` | 搜索知识库 |
| `rag_graph` | 图谱查询 |
| `rag_mindmap` | 思维导图 |
| `rag_list` | 列出集合 |
| `rag_delete` | 删除集合 |

### 日程任务（5 个）

| 工具 | 说明 |
|------|------|
| `schedule_create` | 创建定时任务 |
| `schedule_list` | 列出任务 |
| `schedule_update` | 更新任务 |
| `schedule_delete` | 删除任务 |
| `schedule_history` | 执行历史 |
| `schedule_run_now` | 立即执行 |

### 文档处理（7 个）

| 工具 | 说明 |
|------|------|
| `doc_read` | 读取文档 |
| `doc_write` | 写入文档 |
| `csv_read` | 读取 CSV |
| `csv_write` | 写入 CSV |
| `xlsx_read` | 读取 Excel |
| `xlsx_write` | 写入 Excel |
| `doc_convert` | 格式转换 |

### PPT（1 个）

| 工具 | 说明 |
|------|------|
| `ppt` (skill) | 渲染 PPT（SVG/模板填充） |

### 专家团队（2 个）

| 工具 | 说明 |
|------|------|
| `expert_team_run` | 运行多专家评审 |
| `expert_team_list` | 列出专家团队 |

---

## 四、总览矩阵

| | 主循环可见 | 隐藏（子代理可达） | 合计注册 |
|---|:---:|:---:|:---:|
| **dev（编码）** | 21（14 共有 + 7 编码） | 51 办公工具 | 72 |
| **cowork（办公）** | 14（14 共有） | 7 编码 + 51 办公工具 | 72 |

两个模式注册的工具池**完全相同**（72 个），区别只在 `Profile.HiddenTools` 控制哪些进主循环 schemas：

- **dev**：隐藏 51 个办公工具，主循环看到 21 个编码工具
- **cowork**：隐藏 7 个编码工具，主循环看到 14 个通用工具

隐藏的工具仍可通过 `run_skill` → 子代理 → `FilterRegistry` 调用。

---

## 五、办公技能与工具映射

办公技能通过 `run_skill` 调用，每个 skill 的子代理有独立的 `AllowedTools` 白名单：

| 技能 | AllowedTools |
|------|-------------|
| `browser-auto` | browser_*（12 个）+ web_search + web_fetch + read_file + write_file |
| `desktop-auto` | screen_* + screenshot + get_ui_tree + image_understand + window_*（5 个）+ read_file + write_file |
| `ppt-auto` | screen_* + screenshot + get_ui_tree + image_understand + window_* + read_file + write_file |
| `email-auto` | email_send + email_read + email_search + read_file |
| `rag-auto` | rag_import + rag_search + rag_list + rag_delete + read_file |
| `schedule-auto` | schedule_create + schedule_list + schedule_delete + schedule_update |
| `document-auto` | doc_read + doc_write + csv_read + csv_write + xlsx_read + xlsx_write + doc_convert + read_file + write_file |
| `expert-auto` | expert_team_run + expert_team_list |
| `research` | read_file + grep + bash + codegraph_*（子代理可见）+ web_fetch |

---

## 六、配置参考

### Profile.HiddenTools（config/profile.go）

```go
// cowork profile
HiddenTools: []string{
    "lsp_lookup", "lsp_references", "lsp_workspace_symbol",
    "codegraph_context", "codegraph_search",
    "multi_edit",
    "research",
},
```

dev profile 的 HiddenTools 为空（所有编码工具可见）。办公工具（browser/screen/window/email/rag/schedule/document/ppt/expert）在 boot.go 中无条件 `reg.Hide`，两个模式都隐藏。

### 用户自定义

用户可以在 `hiq.toml` 里覆盖内置 profile：

```toml
[[profiles]]
name = "cowork"
hidden_tools = ["lsp_lookup", "codegraph_context", "codegraph_search"]
# 自定义隐藏哪些工具（空 = 使用内置默认）
```
