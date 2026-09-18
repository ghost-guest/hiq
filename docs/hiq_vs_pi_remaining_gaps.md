# Hiq vs PI — 剩余差距与改进方案

> 接续 `fix_tool_visibility_plan.md`，汇总前端可见性修复之后仍存在的 coding 能力差距。
> 日期：2026-08-10 | 更新：2026-08-10（P1 已完成，方案代码已校正）

---

## 一、已完成项（无需再动）

| # | 能力 | 说明 |
|---|------|------|
| A | 吞过程修复（5 项） | displayMode 改 standard、history merge running items、watchdog 120s、bash 不折叠、shell 不自动折叠 |
| B | Compaction 文件追踪 | `ExtractFileOps()` 程序化提取 read/modified 文件，不依赖 LLM |
| C | 并行执行上限 | `maxParallel = 8`，只读并行，写工具串行 |
| D | Session fork/branch | `Fork(turn)` 支持从任意 turn 分叉 |
| E | 热切换模型 | `SetModelForTab` 带对话历史重建 |
| F | multi_edit 工具 | 独立工具，原子批量编辑 |
| G | apply_patch 工具 | 多文件补丁，两阶段提交 |

---

## 二、差距总览

| # | 差距 | hiq 现状 | PI 做法 | 影响 | 优先级 |
|---|------|--------------|---------|------|--------|
| 1 | edit_file 不返回 diff | 只返回 `"edited %s"` 一句话 | 返回 `{diff, patch, firstChangedLine}` | LLM 看不到自己改了什么，可能重复修改 | **P1** |
| 2 | 截断安全缺失 | `finish_reason=length` 只警告，tool call 照常执行 | 拒绝执行所有截断的 tool call | 半截 JSON 参数可能写坏文件 | **P1** |
| 3 | apply_patch 无预览 | 执行后才返回摘要（`"A path"`, `"M path"`） | 执行前 show diff，用户可审批 | plan 模式无法审查补丁内容 | **P2** |
| 4 | file mutation queue | 全部 writer 串行（粗粒度） | 按 canonicalPath 加锁，不同文件可并行 | 同 turn 多文件编辑不必要的等待 | **P3** |
| 5 | tool call 无集中校验 | 各工具自行校验参数 | — | 不统一，容易遗漏 | **P3** |

---

## 三、详细方案

### P1-1：edit_file 返回 diff ✅ 已完成

**文件:** `internal/tool/builtin/editfile.go`

**现状：**
```go
// editfile.go:114
return fmt.Sprintf("edited %s", p.Path), nil
```

LLM 只知道"改了某个文件"，不知道具体改了什么。可能导致：
- 重复修改同一处
- 改错但不知道
- 无法验证自己的修改

**PI 的做法：**
```typescript
// edit.ts:115-123
return { diff, patch, firstChangedLine }
```

返回完整的 unified diff + 标准 patch + 首次变更行号。

**实际改法：**

`internal/diff/diff.go` 的 `Build` 签名为：
```go
func Build(path, oldText, newText string, kind Kind) Change
```
4 个参数（path、oldText 全文、newText 全文、kind），内部分行。返回 `Change` 结构体，其中 `.Diff` 字段已是 unified diff 文本（3 行上下文，硬编码）。`FormatUnified` 不存在——diff 包只导出 `Build` 一个函数。

实际插入位置：`editfile.go` 写盘成功后，复用已有的 `content`（原文件，`:77` 读入）和 `updated`（改后内容，`:101` 生成）：

```go
ch := diff.Build(p.Path, content, updated, diff.Modify)
if ch.Diff != "" {
    result = result + "\n" + ch.Diff
}
```

`ch.Diff` 为空时安全跳过（二进制文件或无实际变更）。

**改动量：** ~5 行（实际比预估更小）
**风险：** 低 — 只改返回值，不影响编辑逻辑

---

### P1-2：截断安全 ✅ 已完成

**文件:** `internal/agent/agent.go`

**现状：**

`agent.go:793-795` 的 `finishReasonMessage` 检测到 `finish_reason == "length"` 时只发一个 Notice 警告：

```go
if finishReason == "length" {
    a.sink.Emit(event.Event{Kind: event.Notice, Text: "response truncated: hit max output tokens"})
}
```

但 tool call 仍然进入 `executeBatch`（`:849`）执行。如果 LLM 在输出 token 耗尽时恰好在写一个 JSON 参数，截断的 JSON 会导致：
- 工具收到不完整参数
- `edit_file` 的 `old_string` 被截断，匹配失败或匹配到错误位置
- `write_file` 的 `content` 被截断，写入不完整文件

**PI 的做法：**

```typescript
// agent-loop.ts:381-406
if (stopReason === "length") {
    for (const call of toolCalls) {
        failToolCall(call.id, "response truncated: tool call skipped for safety");
    }
    continue; // skip executeBatch
}
```

**实际改法：**

不需要改 `stream()` 签名——`FinishReason` 已在 `stream()` 的第 5 个返回值 `*provider.Usage` 的 `usage.FinishReason` 字段中。拦截点在 `:793`（Notice 发出后）和 `:849`（executeBatch 之前）之间的干净空隙：

```go
// agent.go — Run() 主循环，:793 之后、:849 之前
if (usage.FinishReason == "length" || usage.FinishReason == "repetition_truncation") && len(toolCalls) > 0 {
    for _, call := range toolCalls {
        a.sink.Emit(event.Event{Kind: event.ToolDispatch, Tool: event.Tool{ID: call.ID, Name: call.Name}})
        a.sink.Emit(event.Event{
            Kind: event.ToolResult,
            Tool: event.Tool{ID: call.ID, Name: call.Name, Err: fmt.Errorf("tool call skipped: response was truncated (hit max output tokens)")},
        })
        sess.Add(provider.Message{Role: provider.ToolRole, ToolCallID: call.ID, Content: "tool call skipped: response was truncated"})
    }
    continue // 跳过 executeBatch，进入下一轮让 LLM 重试
}
```

关键细节：
- 只拦截 `length` 和 `repetition_truncation`，不拦截 `content_filter`（内容过滤不是截断，参数完整）
- 先发 `ToolDispatch` 再发 `ToolResult`，保证前端卡片显示完整（直接跳过会让卡片缺 dispatch）
- 复用 `executeBatch` 的事件模式（`Err` 字段而非 `IsError`）
- `content_filter` 的 tool call 不受影响，照常执行

**改动量：** ~15 行
**风险：** 低 — 只在异常路径生效，正常流不受影响

---

### P2：apply_patch 预览

**文件:** `internal/tool/builtin/apply_patch.go`

**现状：**

`apply_patch` 执行后返回摘要：
```go
return "A path/to/new.go\nM path/to/existing.go\nD path/to/old.go", nil
```

没有执行前预览。`agent.go:1322-1325` 的 `executeBatch` 已经对 `edit_file`/`write_file`/`multi_edit` 调用 `PreviewChange` 生成 `FileDiff` 附加到 `ToolDispatch` 事件，但 `apply_patch` 没有实现 `tool.Previewer` 接口。

**PI 的做法：**

PI 没有 `apply_patch`，但其 `edit` 工具返回 `{diff, patch}` 给模型。

**实际改法：**

`tool.Previewer` 接口（`tool.go:41-43`）签名为：
```go
type Previewer interface {
    Preview(args json.RawMessage) (diff.Change, error)
}
```
注意：没有 `ctx` 参数，返回 `diff.Change` 不是 `string`。

`apply_patch` 的入参只有 `{PatchText string}`（`:297-299`），没有 `applyPatchParams`/`Operations`/`Action` 等结构体。hunk 列表是内部 `parsePatch()`（`:72`）从自由文本解析出来的 `[]patchHunk`，字段是 `typ`/`path`/`movePath`/`contents`/`chunks`。

实现步骤：
1. 从 `PatchText` 调用 `parsePatch()` 解析出 `[]patchHunk`
2. 对每个 `update` 类型 hunk：读原文件 → 内存中 apply hunks → `diff.Build(path, old, new, diff.Modify)` 生成 `Change`
3. 对 `add`/`delete` 类型：构造空 `Change` 或用 `diff.Build` 的 `diff.Add`/`diff.Delete` kind
4. 合并多个 `Change` 的 `.Diff` 字段返回一个总的 `Change`

`executeBatch` 中的 `PreviewChange` 分支已处理 `Previewer` 接口——只要实现了就会自动生效，`Change.Diff` 会附加到 `ToolDispatch.FileDiff`。

**改动量：** ~40 行
**风险：** 低 — 只影响 UI 预览，不影响执行逻辑

---

### P3-1：file mutation queue

**文件:** `internal/agent/agent.go` 的 `executeBatch` / `partitionToolCalls`

**现状：**

`partitionToolCalls`（`agent.go:1388-1404`）+ `parallelisable`（`:1406-1412`）将 tool calls 分为两类：
- 只读工具（`ReadOnly() == true`）的连续序列 → 并行执行（信号量 `maxParallel=8`，`:1415`）
- 写工具 → 串行执行，每个写工具独占一个 batch

注意：`parallelisable` 还额外排除了 `complete_step` 和 `todo_write`（`:1407`，它们虽是只读但需读 turn 的 evidence ledger），改造时这两者要保留串行。

这意味着一个 turn 里同时 `edit_file("a.go")` 和 `edit_file("b.go")` 会串行执行，即使它们操作不同文件。

**PI 的做法：**

```typescript
// file-mutation-queue.ts
withFileMutationQueue(env, canonicalPath, async () => {
    // 同一 canonicalPath 串行，不同路径可并行
})
```

**改法：**

在 `executeBatch` 中引入按路径的互斥锁：

```go
type fileMutex struct {
    mu sync.Map // canonicalPath -> *sync.Mutex
}

func (fm *fileMutex) Lock(path string) *sync.Mutex {
    canonical := filepath.Clean(path)
    actual, _ := fm.mu.LoadOrStore(canonical, &sync.Mutex{})
    m := actual.(*sync.Mutex)
    m.Lock()
    return m
}
```

`partitionToolCalls` 改为：
- 只读工具：并行（不变）
- `complete_step`/`todo_write`：保留串行（不变）
- 其他写工具：按 canonicalPath 分组，同路径串行，不同路径并行

**改动量：** ~30 行
**风险：** 中 — 改变了并发模型，需要充分测试

---

### P3-2：tool call 集中校验

**文件:** `internal/agent/agent.go` 或新增 `internal/tool/validate.go`

**现状：**

各工具在自己的 `Execute` 方法中自行校验参数：
- `editfile.go:66-71` 检查 `path` 和 `old_string` 必填
- `bash.go:132-134` 检查 `command` 必填
- 没有统一的 JSON Schema 校验层

**PI 的做法：**

PI 在 tool 定义层声明 JSON Schema，agent loop 在 dispatch 前用 schema 校验参数。

**改法：**

`tool.ValidateArgs` 全仓库不存在，需要新建。`tool.Schema()` 返回的是 `json.RawMessage`（JSON Schema 格式），校验层需要基于此做 required/类型校验。

方案 A（最小改动）：在 `executeOne` 中加一层基础校验——只检查 required 字段是否存在：

```go
// internal/tool/validate.go（新文件）
func ValidateArgs(schema, args json.RawMessage) error {
    // 解析 schema 中的 required 数组
    // 检查 args 中对应字段是否存在
    // 不做深层类型校验（留给各工具自行处理）
}
```

方案 B（更完整）：引入 `jsonschema` 库做完整校验，但会增加依赖。

注意：这不是"~20 行"——需要实现 required 字段提取 + JSON 字段存在性检查，预估 ~50-60 行。

**改动量：** ~50-60 行（方案 A）/ ~80 行（方案 B，含依赖）
**风险：** 低 — 只在执行前加校验，不影响已有逻辑

---

## 四、hiq 独有优势（PI 没有的，保持）

这些能力 hiq 有而 PI 没有，是差异化优势，**不需要改，只需确保不退化**：

| # | 能力 | hiq 实现 | 说明 |
|---|------|--------------|------|
| 1 | Checkpoint 快照 + Rewind | `internal/checkpoint/` — 每轮自动快照修改的文件，支持代码/对话回滚/fork | PI 完全没有 |
| 2 | 4 级权限模型 | RiskRead < RiskWriteLocal < RiskExec < RiskExternal，YOLO/Auto/Ask 三模式 | PI 只有 hook 级 block |
| 3 | Plan 模式 HardDeny | writer 工具不可绕过，即使 `"*": "allow"` | PI 无 |
| 4 | MCP 客户端 | 完整 JSON-RPC 2.0，stdio/HTTP transport，热插拔 | PI 无 |
| 5 | RPM 限流 + 优先级 | `RequestBudget` 按 API key 限 RPM，主 agent 优先 | PI 无 |
| 6 | 中流重连 | 断流后自动重放（未产出 token 时），最多 3 次 | PI 无 |
| 7 | Retry 10 次 + 指数退避 | 408/429/5xx/529 自动重试，尊重 Retry-After | PI 有 retry 但策略较简单 |
| 8 | VLM 桌面视觉 | UIA + VLM 融合感知，屏幕截图→标注→语义选择 | PI 无桌面自动化 |
| 9 | 14 个内置 Skill | explore/review/security-review/browser-auto/desktop-auto/email-auto 等 | PI 内置少 |
| 10 | 证据链 Todo | `complete_step` 需要证据才能标记完成，防幻觉 | PI 是纯状态列表 |
| 11 | 风暴检测 + 重复守卫 | 检测重复失败/重复成功，自动重定向 LLM | PI 无 |
| 12 | 系统 prompt 9 层架构 | base + profile + model-family + time + output-style + language + memory + skill-index + codegraph | PI 较简单 |
| 13 | RAG 知识库注入 | `<untrusted_content>` 标签自动注入检索结果 | PI 无 |
| 14 | Thinking 语言控制 | reasoning language 独立于回答语言，per-turn 注入 | PI 无 |
| 15 | Max 模式（并行推理） | N 个候选并行生成，judge 选最优 | PI 无 |
| 16 | Windows 通知 | BurntToast 长时通知 + "知道了" 按钮 | PI TUI 无桌面通知 |
| 17 | 编码感知 | GBK/UTF-16/BOM 检测和保留 | PI 仅 UTF-8 |
| 18 | Session trash + 恢复 | 删除的 session 移到 `.trash/`，可恢复 | PI 无 |
| 19 | 12 种 Hook 事件 | PreToolUse/PostToolUse/UserPromptSubmit/Stop/PostLLMCall 等 | PI 有更细粒度但 hiq 已覆盖核心场景 |

---

## 五、PI 独有优势（hiq 没有的，可选借鉴）

这些能力 PI 有而 hiq 没有，**非紧急但值得长期关注**：

| # | 能力 | PI 实现 | 是否值得引入 |
|---|------|---------|-------------|
| 1 | 38 个内置 provider | `packages/ai/src/providers/all.ts` | ⚠️ hiq 的 openai kind 已覆盖多数场景，但缺少 Kimi Coding、小米 Token Plan 等中国 vendor 预设 |
| 2 | 项目信任系统 | `trust-manager.ts` — 项目级 `.pi/` 资源加载需授权 | ⚠️ 安全加分，但 hiq 的 hook trust 已覆盖部分场景 |
| 3 | Session append-only tree | JSONL tree + branch summary + compaction boundary | ⚠️ 更强的会话管理，但 hiq 的扁平 session + checkpoint 已够用 |
| 4 | Hook 更细粒度 | 20+ 事件含 `before_provider_payload`、`tool_result` patch | ⚠️ 可选扩展，当前 12 种已覆盖核心场景 |
| 5 | Kitty 图片协议 TUI | 终端内直接渲染图片 | ❌ hiq 是桌面 GUI，不需要 |
| 6 | 多实例 server supervisor | `ServerSupervisor` 管理多 agent 实例 | ❌ hiq 是单实例桌面应用 |
| 7 | Observability (OTEL) | 结构化生命周期事件，可转 OTEL spans | ⚠️ 长期有价值，短期非必须 |

---

## 六、实施路线图

### 第一阶段：P1（安全 + 质量）✅ 已完成

| 序号 | 改动 | 文件 | 行数 | 目标 | 状态 |
|------|------|------|------|------|------|
| 1 | edit_file 返回 diff | `editfile.go` | ~5 | LLM 可见自己的修改 | ✅ |
| 2 | 截断安全 | `agent.go` | ~15 | 防止半截参数执行 | ✅ |

### 第二阶段：P2（plan 模式体验，~40 行）

| 序号 | 改动 | 文件 | 行数 | 目标 |
|------|------|------|------|------|
| 3 | apply_patch 预览 | `apply_patch.go` | ~40 | plan 模式可审查补丁 |

### 第三阶段：P3（并发优化，~80-100 行）

| 序号 | 改动 | 文件 | 行数 | 目标 |
|------|------|------|------|------|
| 4 | file mutation queue | `agent.go` | ~30 | 不同文件可并行写 |
| 5 | tool call 集中校验 | `internal/tool/validate.go`（新文件） | ~50-60 | 统一参数校验 |

**总改动量：~120-140 行，P1 已完成，P2/P3 待实施。**

---

## 七、方案校正记录

本文档初版方案有 5 处代码示例与实际源码不符，已全部修正：

| # | 问题 | 初版错误 | 实际情况 |
|---|------|---------|---------|
| 1 | `diff.Build` 签名 | 写成 2 参数 `Build([]string, []string)` | 实际 4 参数 `Build(path, oldText, newText string, kind Kind)`，内部分行 |
| 2 | `FormatUnified` | 编造的函数名 | 不存在。`Build` 返回的 `Change.Diff` 已含 unified diff 文本（3 行上下文） |
| 3 | 截断标志来源 | 说"需改 `stream()` 签名加 `truncated bool`" | `FinishReason` 已在 `stream()` 第 5 个返回值 `*provider.Usage` 的 `usage.FinishReason` 中，无需改签名 |
| 4 | `Previewer` 接口 | 写成 `Preview(ctx, json.RawMessage) (string, error)` | 实际 `Preview(json.RawMessage) (diff.Change, error)`，无 ctx，返回 Change 不是 string |
| 5 | `applyPatchParams` | 编造的结构体 | 不存在。入参只有 `{PatchText string}`，hunk 由内部 `parsePatch()` 从自由文本解析 |
| 6 | `ValidateArgs` | 编造的函数 | 不存在，需新建。实际工作量 ~50-60 行（非预估的 ~20 行） |

结论：问题诊断准确，但修复代码需要按实际 API 重写。好消息是实际 API 让修复比文档写的更简单（`diff.Build` 一步到位、截断拦截不用改签名）。

---

## 八、相关文档

| 文档 | 内容 |
|------|------|
| `docs/fix_tool_visibility_plan.md` | 已完成的 5 项前端可见性修复方案 |
| `docs/COWORK_HARNESS_SECURITY_PLAN.md` | cowork 模式安全方案 |
| `internal/diff/diff.go` | Myers diff 算法实现（edit_file diff 可复用） |
| `internal/checkpoint/checkpoint.go` | Checkpoint 快照系统（hiq 独有优势） |
