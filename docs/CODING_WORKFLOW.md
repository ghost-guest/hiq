# hiq 编码流程（v0.4 实际行为）

> 基于当前代码的实际执行路径，不是旧文档描述。
> 任务进来后，hiq 按复杂度自动分流到不同处理路径。

---

## 总览

三条路径，**compose 不是独立路径，是 Plan Mode 审批后的执行出口之一**：

```
用户输入
   │
   ▼
isLowRiskQuestion? ──── 是 ──→ 【路径 A】直接回答（不进任何模式）
   │ 否                         解释/说明/怎么看/查一下/why/how/run/show/what
   │
   ▼
auto_plan 开启? ──── 否 ──→ 用户手动决定：
   │ 是                         · /plan 或 Shift+Tab → 进入【路径 C】规划
   │                            · 直接发（不规划）→ 【路径 B】单次执行
   │
   ▼
autoPlanScore 打分 ── ≥2 分 ─→ 【路径 C】Plan Mode
   │ <2 分
   │                            ┌─────────────────────────────────────┐
   ▼                            │ 路径 C 内部分两个阶段：               │
直接发 ──→ 路径 B                │                                       │
                                │  ① 规划阶段（所有 C 任务都走）：       │
                                │     只读探索 → 出方案 → 用户审批       │
                                │                                       │
                                │  ② 执行阶段（审批后按 task 数量分流）：│
                                │     ├── <3 task → 单次执行（同 B）     │
                                │     └── ≥3 task → compose 循环        │
                                │         (Implement→Verify→Review)     │
                                └─────────────────────────────────────┘
```

**所以复杂任务是"先进 C 规划，审批后进 compose 执行"——是 C 的两个阶段，不是 C+D 走两遍。**

---

## 路径 A：直接回答（轻任务）

### 触发条件
输入以这些前缀开头（且不含复杂意图词 implement/重构/迁移 等）：
- 中文：解释、说明、怎么看、查一下、运行
- 英文：run、show、what、why、how

### 实际行为
1. 不进 plan mode，不规划
2. 模型直接回答或调只读工具（read_file/grep/codegraph/lsp）查证
3. bash 可用（写命令受 permission Gate 管，不受 plan mode 限制）
4. 完成后返回

### 典型场景
- "解释这段代码"
- "这个函数怎么用"
- "为什么这里报错"
- "查一下 X 在哪定义"
- "运行 go test 看看"

### 用户感受
快。一问一答，不绕弯。

---

## 路径 B：单次执行（中等任务）

### 触发条件
- 不是轻任务（不命中 isLowRiskQuestion）
- 且 auto_plan 关闭（默认）
- 或 auto_plan 开启但任务评分 <2
- 或 Plan Mode 审批后 task 数量 <3

### 实际行为
1. 模型收到任务，直接开始执行
2. 用 todo_write 建任务列表（可选，多步时建议）
3. 逐步执行：read_file → edit_file → bash（跑测试）
4. 每步用 complete_step 带证据签收（可选）
5. **edit/write 后自动跑 LSP 诊断**（A3 改进）——结果塞回 tool output，模型立即看到类型错误
6. 完成后总结

### 编码纪律（DefaultSystemPrompt 约束）
- **编辑前先读**——不许编辑没读过的文件
- **edit 优于 write**——targeted 替换，不整文件重写（除非新建文件）
- **改完跑测试**——编辑后跑项目测试/构建确认
- **工具分层**——read_file/grep/codegraph/lsp 优先于 bash cat/find
- **reversibility**——可逆改动自由做；不可逆/外向操作（force push/发邮件/删文件）先用 ask 确认

### 典型场景
- "修一下 login 函数的空指针"
- "给 Config 加个 timeout 字段"
- "把这个函数重命名"
- "补个单元测试"

### 用户感受
流畅。模型直接干活，edit 后能看到 LSP 诊断，不用反复提示"你没验证"。

---

## 路径 C：Plan Mode（规划任务）

### 触发条件
- 用户手动：`/plan` 或 Shift+Tab（TUI）/ 桌面 plan 按钮
- 或 auto_plan=on 且 autoPlanScore ≥2（多步/多文件/复杂意图词）

### Plan Mode 可用工具
| 工具 | 可用 | 说明 |
|------|------|------|
| read_file | ✅ | 读文件、目录列表 |
| grep | ✅ | ripgrep 搜索 |
| glob | ✅ | glob 模式匹配文件发现 |
| codegraph_* | ✅ | 符号图、调用链、影响分析 |
| lsp_definition/references/hover | ✅ | 跳转定义、查引用 |
| lsp_workspace_symbol | ✅ | 全局符号搜索 |
| lsp_implementation | ✅ | 接口实现定位 |
| web_fetch / web_search | ✅ | 联网查证 |
| **bash（只读命令）** | ✅ | git log / find / cat / wc 等（ReadOnlyCallChecker 参数级判定）|
| bash（写命令） | ❌ | rm / > / git commit / curl 被拦截 |
| edit_file / write_file | ❌ | HardDeny 数据层拦截（用户配置无法绕过）|
| task / run_skill | ❌ | HardDeny 数据层拦截 |
| todo_write / ask | ✅ | 任务管理、交互询问 |

**plan mode 只读保证**：双重机制——运行时 ReadOnlyCallChecker（bash 参数级判定）+ 数据层 HardDeny（edit_file/write_file/run_skill/task 即使用户配 `"*":"allow"` 也无法绕过）。

### 实际行为
1. 进入只读模式（写工具物理拦截，agent.go:1405）
2. 模型探索代码库（read_file/grep/codegraph/lsp/只读 bash）
3. 写出两层 markdown 计划：
   - 顶层：编号 phase（里程碑，2-6 个）
   - 缩进：可验证的子步骤
4. 停下，发审批请求（`exit_plan_mode`）
5. 用户审批：
   - **批准** → SetPlanMode(false) → seedPlanTodos（计划→todo 列表）→ 进入执行
   - **拒绝** → 留在 plan mode，修订计划

### 审批后的执行分流
- task 数量 <3 → **路径 B**（单次执行）
- task 数量 ≥3 → **C 的 compose 执行阶段**（见下节）

### 典型场景
- "帮我看看这块怎么改比较好"（探索+方案）
- "设计一个配置加载器"
- "调研用 LangGraph 还是 AutoGen"
- "重构整个认证模块"（≥3 步 → compose）

### 用户感受
安全。规划阶段绝对不改文件，方案审批后才动手。

---

## 路径 C 的执行阶段：compose 工作流（复杂任务，task ≥3）

> compose 不是独立路径，是 Plan Mode 审批后、task 数量 ≥3 时的执行出口。
> 审批前经历的"探索→方案→审批"和路径 C 完全一样，区别只在审批后怎么执行。

### 触发条件
Plan Mode 审批通过后，seedPlanTodos 解析出的 task 数量 ≥3（MinTasksForCompose）。
task <3 时走单次执行（同路径 B），不进 compose。

### 工作流（确定性 Go 编排器驱动）

```
审批通过（task ≥3）
    │
    ▼
┌─→ Phase 1: IMPLEMENT（执行计划）
│     模型按 plan-approved nudge 执行
│     todo_write 建任务列表，逐步 complete_step 带证据
│     edit/write 后自动 LSP 诊断
│     │
│     ▼
│   Phase 2: VERIFY（跑测试验证）
│     模型跑项目测试（go test / npm test / cargo test）
│     报 VERDICT: PASS 或 VERDICT: FAIL
│     纪律：无新鲜证据不声称完成（verifyDiscipline）
│     │
│     ▼
│   VERDICT?
│   ├── PASS ──→ Phase 3: REVIEW
│   │              │
│   │              ▼
│   │            模型自审 git diff（critical/important/minor）
│   │            报 VERDICT: CLEAN 或 VERDICT: ISSUES
│   │              │
│   │              ├── CLEAN ──→ 完成 ✅
│   │              │
│   │              └── ISSUES ──→ 模型在 turn 内修复
│   │                             │
│   │                             ▼
│   │                           跳过 IMPLEMENT（不覆盖修复）
│   │                           直接回 Phase 2 RE-VERIFY
│   │
│   ├── PLAN_INVALID ──→ 模型发现计划前提错误（非代码 bug）
│   │                      │
│   │                      ▼
│   │                    通知用户 + 审批（composeReplanTool，YOLO 不绕过）
│   │                      │
│   │                      ├── 批准 → 重新规划剩余部分（replan）→ 回 Phase 1
│   │                      │         （已完成 task 保留，只重规划剩余）
│   │                      │
│   │                      └── 拒绝 → 停止
│   │
│   └── FAIL ──→ 喂回失败信息 + 根因修复纪律（implementDiscipline）
│                 │
│                 ▼
│               回 Phase 1 RETRY（≤3 轮）
│
└─ attempt ≤3 且 replan ≤2（超过则放弃，返回"部分完成"）
```

### 关键机制

| 机制 | 说明 |
|------|------|
| **确定性编排** | Go 编排器固定阶段顺序（Implement→Verify→Review），不靠模型自己走 |
| **VERDICT 协议** | 模型报 `VERDICT: PASS/FAIL/CLEAN/ISSUES/PLAN_INVALID`，编排器据此流转（轻量，不依赖 provider schema）|
| **跨轮状态** | compose Runner 自管 failures/skipImplement（不依赖每 Run reset 的 evidence Ledger）|
| **重试上限** | MaxImplementAttempts=3 + MaxReplans=2，超过放弃（不算硬错误，工作部分落地）|
| **review-fix 保护** | review 发现 issues 修复后，跳过 implement 直接 re-verify（不覆盖修复）|
| **PLAN_INVALID 重新规划** | 模型发现计划前提错误时报 `VERDICT: PLAN_INVALID`，编排器暂停并通知用户审批，批准后基于已完成 task 重新规划剩余部分（composeReplanTool 不被 YOLO 绕过）|
| **审批窗口复用** | 在 approvedPlanAutoApproveTools 窗口内执行，写工具自动批准 |
| **全局 RPM** | 所有 LLM 调用共享 `[llm].rpm` 配额（默认 60），compose 的 implement/verify/review 也在配额内 |

### 编排纪律文本（注入 nudge）

**verifyDiscipline**（verify 阶段）：
- 跑项目真实测试命令，不靠读代码推断
- 捕获真实输出（exit code、测试计数、编译错误）
- 不知道测试命令就查（go.mod/package.json/Cargo.toml/Makefile）
- 静默或跳过不算 pass

**implementDiscipline**（retry 时）：
- 读真实错误再改，不从失败摘要猜原因
- 找根因，追踪到源码行
- 测试错了就改测试（但要明说）
- 不许禁用/跳过失败测试骗绿
- 同一症状修 2 次没成功就重新考虑方案

**reviewNudge**（review 阶段）：
- 检查刚才的改动（git diff 或重读文件）
- 分 critical/important/minor 三档
- critical 必须修复

### 典型场景
- "实现 issue #2395：新增配置项、自动判断复杂任务、补测试和文档"
- "加一个 TOML 配置加载器并写测试"
- "重构整个认证模块，迁移到新接口"
- "实现完整的用户注册流程"

### 用户感受
可靠。复杂任务不再"做完一半就停"或"声称做完没验证"——有验证环、重试、自审。任务面板能看到进度（compose: implement/verify/review 的 notice）。

---

## 通用机制（所有路径共享）

### edit/write 后 LSP 诊断回灌（A3）
edit_file/write_file 成功后，自动跑该文件的 LSP diagnostics，结果塞回 tool output：
```
edited /path/to/file.go
LSP diagnostics for /path/to/file.go:
  undefined: foo (line 42)
```
模型立即看到类型错误，不用等用户跑 typecheck。

### Goal Judge（A4）
goal 模式下，模型说 `[goal:complete]` 时，独立 LLM 冷读 transcript 验证：
- 不满足 → 注入 synthetic turn 继续
- temperature 0，60s 超时，turn ctx cancel 可中断

### ask 工具（交互询问）
真实选择留给用户时（用哪个库、scope 多大、模糊决策），模型调 ask 给 2-4 个选项。YOLO 模式不替用户回答 ask。

### todo_write + complete_step
- todo_write：两级任务列表（phase + sub-step），3 态（pending/in_progress/completed）
- complete_step：带证据签收（无证据拒绝），host 自动推进 todo
- finalReadinessCheck：todo 未完成时阻止"最终答案"

### permission Gate（权限）
- Allow/Ask/Deny 规则（glob 匹配）
- bash 参数级只读判定（isReadOnlyBashSubject）
- YOLO 模式自动批准工具（plan 审批除外）

### checkpoint rewind
- edit/write 前拍快照（PreEditHook）
- `/rewind` 撤销（RewindCode/RewindConversation/RewindBoth）

### codegraph（独家）
- tree-sitter + SQLite 符号图
- codegraph_search/callers/callees/impact/trace
- 后台增量索引，零 API 开销

---

## 各路径对比

| 维度 | 路径 A 直接回答 | 路径 B 单次执行 | 路径 C Plan Mode（规划阶段）| C 审批后 compose（执行阶段）|
|------|---------------|---------------|-----------------|---------------|
| **复杂度** | 轻任务 | 中等任务 | 规划任务 | 复杂任务 |
| **触发** | isLowRiskQuestion | 非 A + 不进 plan | 手动/auto-plan | 审批后 task≥3 |
| **规划** | 无 | 无（直接干）| 只读探索+方案+审批 | 复用 C 的审批 |
| **验证** | 无 | 改完跑测试（纪律）| N/A | Implement→Verify→retry |
| **重试** | 无 | 无 | 无 | ≤3 轮 + review-fix |
| **写工具** | 全开 | 全开 | 物理拦截 | 审批窗口自动批准 |
| **bash** | 全开 | 全开 | 只读命令放行 | 审批窗口全开 |
| **LSP 诊断** | ✅ | ✅ | N/A（不改文件）| ✅ |
| **典型耗时** | 秒级 | 分钟级 | 分钟级（探索+方案）| 较长（多轮验证）|

---

## 用户怎么控制

### 手动进 Plan Mode
- TUI：Shift+Tab
- 桌面：plan 按钮
- 命令：`/plan`（如果配置了）

### 开启 auto-plan
```toml
[agent]
auto_plan = "on"          # 启用自动规划判定
# auto_plan_classifier = "model-ref"  # 可选：边界情况用 LLM 二分类
```

### YOLO 模式（自动批准工具）
- Ctrl+Y（TUI）
- 桌面 YOLO 按钮
- 注意：plan 审批永远不绕过

### 中断
- Esc / Ctrl+C：取消当前 turn（ctx cancel，立即中断 LLM 调用）

---

## 配置参考

```toml
[agent]
auto_plan = "off"                    # 默认关；"on" 启用自动规划
fast_task_model = "qwen/qwen3-max"           # 后台 dream/distill 模型
max_steps = 0                        # 0=不限；每 turn 工具调用上限

[lsp]
enabled = true                       # 默认开；edit/write 后诊断回灌依赖此

[codegraph]
enabled = true                       # 默认开；符号图搜索
```
