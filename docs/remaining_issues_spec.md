# hiq 剩余问题规格说明书

> 审计日期: 2026-08-10 | 来源: 5 路深度扫描 + 逐项代码校验 | 共 31 项确认问题（13 HIGH + 13 MEDIUM + 5 LOW）

---

## 分级标准

| 级别 | 定义 |
|------|------|
| **HIGH** | 可导致崩溃/数据丢失/安全漏洞，或严重影响核心功能正确性 |
| **MEDIUM** | 影响可用性/模型行为质量/跨平台兼容，但不构成致命风险 |
| **LOW** | 代码质量/一致性/文档完善度问题，不影响核心功能 |

---

# HIGH — 12 项

## H-1. readFileEncoded 无大小限制（OOM 崩溃）

**文件**: `internal/tool/builtin/encoding_helpers.go:23`
**影响**: 所有写入路径（writeFile/editFile/deleteRange/applyPatch）写入前调用 `readFileEncoded` 读取旧文件以检测编码。对大文件（如 2GB 日志）执行 `os.ReadFile` 会一次性加载全部内容到内存，导致进程 OOM 崩溃。`maxWriteBytes`（5 MiB）仅限制新内容写入，不限制旧文件读取。

**方案**:
```go
// encoding_helpers.go
const maxReadBytes int64 = 10 << 20 // 10 MiB

func readFileEncoded(path string) (content string, enc fileenc.Kind, err error) {
    info, err := os.Stat(path)
    if err != nil {
        return "", 0, err // os.IsNotExist 由调用方处理（首次写入场景）
    }
    if info.Size() > maxReadBytes {
        return "", 0, fmt.Errorf("file too large for encoding detection (%d bytes, limit %d)", info.Size(), maxReadBytes)
    }
    b, err := os.ReadFile(path)
    if err != nil {
        return "", 0, err
    }
    enc, _ = fileenc.Detect(b)
    return string(fileenc.Decode(b, enc)), enc, nil
}
```

**验收标准**:
- [ ] `os.Stat` 检查文件大小，超过 10 MiB 直接返回错误
- [ ] 返回类型与签名一致：`(string, fileenc.Kind, error)`
- [ ] 错误信息包含实际大小和限制值
- [ ] 调用方（writeFile/editFile/applyPatch）能正确处理 `os.IsNotExist`

---

## H-2. MCP 工具结果无大小限制（恶意服务器 OOM）

**文件**: `internal/plugin/plugin.go:1058-1080`
**影响**: `parseToolResult` 用 `strings.Builder` 拼接 MCP 服务器返回的文本内容，无上限。恶意/异常的 MCP 服务器可返回多 GB 文本，耗尽进程内存。

**方案**:
```go
const maxMCPResultBytes = 1 << 20 // 1 MiB

func parseToolResult(res json.RawMessage) (string, error) {
    // ...existing parsing...
    truncated := false
    var sb strings.Builder
    for _, c := range r.Content {
        if truncated {
            break // 跳过剩余所有 chunk
        }
        switch c.Type {
        case "text":
            if sb.Len()+len(c.Text) > maxMCPResultBytes {
                sb.WriteString("\n... (truncated: MCP result exceeded 1 MiB limit)")
                truncated = true
                break
            }
            sb.WriteString(c.Text)
        // ...other cases...
        }
    }
    return sb.String(), nil
}
```

> **注意**: Go 中 `break` 在 `switch` 内仅中断 `switch`，不中断外层 `for`。必须用 `truncated` 标志位控制循环退出。

**验收标准**:
- [ ] `maxMCPResultBytes = 1 << 20`（1 MiB）常量定义
- [ ] 超限后截断并附加截断提示
- [ ] 用 `truncated` 标志位跳出 for 循环（非 break）
- [ ] 截断后不再追加任何内容

---

## H-3. 前台 bash 输出无内存限制

**文件**: `internal/agent/agent.go:1632`
**影响**: 前台 bash 执行用 `new(bytes.Buffer)` 缓冲全部输出，无上限。`yes`、`dd if=/dev/zero`、`cat /dev/urandom` 等命令可耗尽内存。后台任务已用 `newCappedBuffer(256 KiB)` 限制（`hook.go:324`），但前台没有。

**方案**:
```go
// agent.go — bash 执行处
// 方案 A：将 cappedBuffer 从 hook 包导出
// hook/hook.go — 将 cappedBuffer 改为 CappedBuffer，添加 NewCappedBuffer 构造函数
type CappedBuffer struct {
    buf   bytes.Buffer
    cap   int
    full  bool
}
func NewCappedBuffer(cap int) *CappedBuffer { return &CappedBuffer{cap: cap} }
// ...Write/String 方法不变

// agent.go 中使用:
out := hook.NewCappedBuffer(256 << 10) // 256 KiB, same as background jobs
cmd.Stdout = out
cmd.Stderr = out

// 方案 B（更简单）：在 agent.go 中直接实现一个 local cappedWriter
type cappedWriter struct {
    buf  bytes.Buffer
    cap  int
    full bool
}
func (w *cappedWriter) Write(p []byte) (int, error) {
    if w.full { return len(p), nil }
    if w.buf.Len()+len(p) > w.cap {
        w.full = true
        w.buf.WriteString("\n... (output truncated)")
        return len(p), nil
    }
    return w.buf.Write(p)
}
func (w *cappedWriter) String() string { return w.buf.String() }
```

> **注意**: `cappedBuffer` 是 `hook.go` 中的 unexported struct，不存在 `newCappedBuffer` 公共函数。需选择导出或本地实现。

**验收标准**:
- [ ] 前台 bash 输出限制 256 KiB（与后台一致）
- [ ] 超限后输出截断提示
- [ ] 正常命令输出不受影响
- [ ] 超限后输出截断提示
- [ ] 正常命令输出不受影响

---

## H-4. MCP 工具描述 prompt injection

**文件**: `internal/plugin/plugin.go:935`
**影响**: MCP 服务器的工具描述 `tdef.Description` 直接注入系统提示（`Schemas()` 输出），无任何清理。恶意 MCP 服务器可在描述中嵌入指令（如 "IGNORE ALL PREVIOUS INSTRUCTIONS"），操纵模型行为。

**方案**:
```go
// plugin.go — 注册工具时清理描述
func sanitizeToolDescription(desc string) string {
    // 1. 截断过长描述
    const maxDescLen = 2000
    if len(desc) > maxDescLen {
        desc = desc[:maxDescLen] + "... (truncated)"
    }
    // 2. 标记来源
    return "[MCP tool] " + desc
}

// 在 registerTool 处:
t.desc = sanitizeToolDescription(tdef.Description)
```

**验收标准**:
- [ ] 描述超过 2000 字符自动截断
- [ ] 所有 MCP 工具描述添加 `[MCP tool]` 前缀标记来源
- [ ] 日志中记录完整原始描述（便于调试）

---

## H-5. writeFileEncoded 非原子写入（崩溃时文件损坏）

**文件**: `internal/tool/builtin/encoding_helpers.go:39`
**影响**: `writeFileEncoded` 直接调用 `os.WriteFile`（truncate-and-write）。进程在写入中途崩溃会留下截断/损坏的文件。其他工具（docx/xlsx）已用 `atomicWrite`（temp+rename）保护。

**方案**:
```go
func writeFileEncoded(path string, content string, enc fileenc.Kind) error {
    if n := len(content); n > maxWriteBytes {
        return fmt.Errorf("content is %d bytes (limit %d); ...", n, maxWriteBytes)
    }
    return atomicWrite(path, fileenc.Encode(content, enc)) // 替换 os.WriteFile
}
```

> **注意**: `writeFileEncoded` 第三参数是 `fileenc.Kind`（枚举 int），不是 string。`fileenc.Encode` 直接返回 `[]byte`，无 error 返回值。

**验收标准**:
- [ ] 使用 `atomicWrite` 替代 `os.WriteFile`（已有 `atomic.go` 中的实现）
- [ ] 崩溃时不会留下半写文件
- [ ] 参数类型 `fileenc.Kind` 不变
- [ ] 编码逻辑 `fileenc.Encode(content, enc)` 不变

---

## H-6. Session 文件无完整性校验

**文件**: `internal/agent/save.go:132-163`
**影响**: `LoadSession` 直接 `dec.Decode(&m)` 加载 JSONL，无 HMAC/签名验证。本地攻击者或恶意插件可篡改会话文件，注入伪造消息/工具调用。

**方案**:
```go
// save.go — Session 方法，非全局函数
// Load 已返回 (*Session, error)，Save 是 (*Session) 的方法

// 加载时校验 HMAC
func Load(path string) (*Session, error) {
    data, err := os.ReadFile(path)
    if err != nil { return nil, err }

    // 校验签名（如果存在）
    sigPath := path + ".sig"
    if sigData, err := os.ReadFile(sigPath); err == nil {
        if !verifyHMAC(data, sigData) {
            return nil, fmt.Errorf("session file integrity check failed: %s", path)
        }
    }
    // ...existing decode logic...
}

// 保存时追加签名（Save 已用 temp+rename 原子写入，此处只需在写入后追加 .sig）
func (s *Session) Save(path string) error {
    // ...existing atomic write logic (temp + ReplaceFile)...
    if err := fileutil.ReplaceFile(tmpPath, path); err != nil {
        return err
    }
    // 写入 HMAC 签名
    data, _ := os.ReadFile(path)
    sig := computeHMAC(data)
    _ = os.WriteFile(path+".sig", sig, 0o644)
    // ...existing cachePreviewInMeta...
    return nil
}
```

> **注意**: `Save` 是 `*Session` 的方法（非全局函数），且已实现原子写入（temp+rename via `fileutil.ReplaceFile`）。方案仅需在原子写入成功后追加 `.sig` 文件。

**验收标准**:
- [ ] 保持 `*Session` 方法签名不变
- [ ] 利用已有的 temp+rename 原子写入
- [ ] 原子写入成功后生成 `.sig` 文件（HMAC-SHA256）
- [ ] 加载时校验签名，不匹配则报错
- [ ] 无签名文件时向后兼容（不阻塞加载）

---

## H-7. browser_click.target 无类型定义

**文件**: `internal/tool/builtin/browser.go:1058`
**影响**: `target` 参数可以是字符串（snapshot ref 或 CSS selector）或坐标对象 `{x, y}`，但 schema 中无 `type` 字段。部分 LLM provider 的 JSON Schema 验证器无法正确处理，可能导致参数传递错误。

**方案**:
```go
"target": map[string]any{
    "description": `A snapshot ref ("e5"), a CSS selector ("button.submit"), or {"x":320,"y":240}.`,
    "oneOf": []map[string]any{
        {"type": "string"},
        {"type": "object", "properties": map[string]any{
            "x": map[string]any{"type": "number"},
            "y": map[string]any{"type": "number"},
        }, "required": []string{"x", "y"}},
    },
},
```

**验收标准**:
- [ ] 使用 `oneOf` 声明 string 或 `{x, y}` object
- [ ] 坐标 object 声明 `x`/`y` 为 number 且 required
- [ ] 描述保持清晰

---

## H-8. schedule_update 缺少参数描述（6/7 字段）

**文件**: `internal/tool/builtin/schedule.go:208-213`
**影响**: `name`、`expression`、`prompt`、`enabled`、`output_mode`、`output_dest` 六个参数无 description。模型无法理解每个参数的含义和格式，必须从 `schedule_create` 的描述推断。

**方案**:
```go
"name":        map[string]any{"type": "string", "description": "New display name (omit to keep current)"},
"expression":  map[string]any{"type": "string", "description": "New cron expression M H DoM Mon DoW (omit to keep current)"},
"prompt":      map[string]any{"type": "string", "description": "New prompt template (omit to keep current)"},
"enabled":     map[string]any{"type": "boolean", "description": "true to enable, false to disable"},
"output_mode": map[string]any{"type": "string", "description": "im, email, notify, or file"},
"output_dest": map[string]any{"type": "string", "description": "Target address/path (omit to clear)"},
```

**验收标准**:
- [ ] 6 个字段全部添加 description
- [ ] 描述与 `schedule_create` 对应字段语义一致
- [ ] 标注 "omit to keep current" 区分更新语义

---

## H-9. 系统提示扁平无分节

**文件**: `internal/agent/config.go:1399-1418`
**影响**: `DefaultSystemPrompt` 是单段纯文本，无 markdown 标题分节。模型难以定位具体规则，遵循率低于结构化提示。boot.go 追加的 7+ 块也是 `\n\n` 拼接，整体无统一结构。

**方案**: 重构 `DefaultSystemPrompt` 为分节格式：
```go
const DefaultSystemPrompt = `# Identity
You are hiq, a coding agent focused on executing code tasks.
When asked about your identity, always say you are hiq. Never mention
Claude, Anthropic, GPT, Qwen, DeepSeek, or any underlying model name.

# Principles
- Understand the request before acting.
- Verify with tools instead of guessing.
- Keep changes minimal and correct.
- Briefly summarize what you did.

# Tools
Use the provided tools to read and write files and run shell commands.
Prefer grep over bash grep for searching code.
Use edit_file for targeted changes; use write_file only for new files or full rewrites.

# Safety
- Do not run destructive commands (rm -rf, sudo) without explicit user request.
- Do not read or modify files outside the working directory.
- Do not fabricate file contents — always read before editing.

# Plan Mode
In plan mode the harness blocks writer tools.
Explore with read-only tools, then write your plan as your reply.
...
```

**验收标准**:
- [ ] 至少 5 个 `#` 分节（Identity/Principles/Tools/Safety/Plan Mode）
- [ ] 工具使用指导包含具体工具名称和选择标准
- [ ] 安全规则明确列出禁止行为
- [ ] boot.go 追加块也使用 `#` 标题保持一致

---

## H-10. 无工具使用指导

**文件**: `internal/agent/config.go:1400`
**影响**: 系统提示仅说 "Use the provided tools to read and write files and run shell commands"，未命名任何具体工具或选择标准。模型可能用 `bash grep` 替代 `grep` 工具，用 `write_file` 替代 `edit_file`。

**方案**: 在 H-9 重构的 `# Tools` 节中添加：
```
- read_file: Read file contents. Always read before editing.
- edit_file: Targeted find-and-replace. Use for modifying existing files.
- write_file: Full file write. Use only for new files or complete rewrites.
- multi_edit: Atomic batch of edits. Use when making 3+ changes to one file.
- apply_patch: Multi-file patch. Use when changing 3+ files together.
- grep: Search code by regex. Preferred over bash grep.
- glob: Find files by pattern. Preferred over bash find.
- bash: Run shell commands. Use for builds, tests, git, installations.
```

**验收标准**:
- [ ] 列出所有核心工具及一句话用途
- [ ] 包含 "prefer X over Y" 选择指导
- [ ] 与 H-9 的分节结构合并

---

## H-11. 无反幻觉指令（dev 模式）

**文件**: `internal/agent/config.go:1404`
**影响**: dev 配置仅有 "verify with tools instead of guessing" 一句。模型可能编造文件内容、函数签名、路径。cowork 配置有专门的反幻觉节，但 dev 没有。

**方案**: 在系统提示中添加 `# Anti-Hallucination` 节：
```
# Anti-Hallucination
- NEVER fabricate file contents. Always use read_file before editing.
- NEVER guess file paths. Use glob or grep to find them.
- NEVER invent function signatures, class names, or API endpoints. Read the source.
- NEVER claim success without evidence. Show the tool output that confirms it.
- If unsure about a library API, read the source or documentation — do not guess.
```

**验收标准**:
- [ ] 包含 5+ 条明确的 "NEVER" 规则
- [ ] 覆盖文件内容、路径、API 三种常见幻觉场景
- [ ] 与 H-9 的分节结构合并

---

## H-12. Cold 标签未渲染（功能 Bug）

**文件**: `internal/skill/index.go:48-66` + `internal/agent/boot.go:489`
**影响**: `boot.go:489` 设置 `s.Cold = true` 标记长期未使用的技能，但 `indexLine()` 函数（line 48-66）只检查 `sk.RunAs` 和 `sk.Disabled`，从不检查 `sk.Cold`。休眠技能在索引中无标记，模型不知道它们是低优先级的。

**方案**:
```go
// index.go:53-59 — 添加 Cold 检查
if sk.RunAs == RunSubagent {
    tag = " [🧬 subagent]"
}
if sk.Disabled {
    tag += " [关闭]"
}
if sk.Cold {
    tag += " [休眠]"  // 添加此行
}
```

**验收标准**:
- [ ] Cold 技能显示 `[休眠]` 标签
- [ ] 标签与 `[关闭]` 可共存（disabled + cold）
- [ ] 测试确认索引输出包含 `[休眠]`

---

## H-13. Checkpoint 恢复丢失可执行权限（数据完整性）

**文件**: `internal/checkpoint/checkpoint.go:283`
**影响**: `RestoreCode` 回滚文件时硬编码 `os.WriteFile(abs, ..., 0o644)`。如果原文件是 `0755`（如部署脚本、构建脚本），回滚后可执行权限被默默降级为 `0644`。后续执行该脚本会因权限不足而失败，且 `git diff` 不显示权限变化（除非 `git diff --stat` 捕获 mode 变化），极难排查。

**方案**:
```go
// checkpoint.go:283 — 恢复时保留原始文件权限
var perm os.FileMode = 0o644
if info, statErr := os.Stat(abs); statErr == nil {
    perm = info.Mode().Perm() // 保留现有权限
} else if snap.Perm != nil {
    perm = *snap.Perm // 使用快照中保存的权限
}
if wErr := os.WriteFile(abs, fileenc.Encode(*snap.Content, enc), perm); wErr != nil {
    err = wErr
    continue
}
```

> 需要在 `Checkpoint` 结构体中添加 `Perm *os.FileMode` 字段，在 `onPreEdit` 快照时保存原始权限。

**验收标准**:
- [ ] 快照时保存原始文件权限（`Checkpoint.Perm` 字段）
- [ ] 恢复时使用快照中保存的权限（而非硬编码 0o644）
- [ ] 回滚 0755 脚本后权限仍为 0755
- [ ] 回滚 0644 源码后权限仍为 0644

---

# MEDIUM — 13 项

## M-1. 文件锁无重试（Windows sharing violation）

**文件**: `internal/tool/builtin/writefile.go`、`movefile.go`、`editfile.go`
**影响**: Windows 上 `os.WriteFile`/`os.Rename` 遇到 `ERROR_SHARING_VIOLATION`（错误 32）直接失败，无重试。当文件被其他进程（编辑器、杀毒、云同步）锁定时，写入操作一次失败即终止。

**方案**:
```go
// encoding_helpers.go — 添加重试包装
func writeFileWithRetry(path string, data []byte, perm os.FileMode) error {
    var err error
    for attempt := 0; attempt < 3; attempt++ {
        err = os.WriteFile(path, data, perm)
        if err == nil {
            return nil
        }
        if !isSharingViolation(err) {
            return err
        }
        time.Sleep(time.Duration(100*(attempt+1)) * time.Millisecond)
    }
    return fmt.Errorf("file locked after 3 retries: %w", err)
}

func isSharingViolation(err error) bool {
    // Windows ERROR_SHARING_VIOLATION = 32
    return strings.Contains(err.Error(), "sharing violation") ||
           strings.Contains(err.Error(), "being used by another process")
}
```

**验收标准**:
- [ ] 重试 3 次，间隔 100/200/300ms 指数退避
- [ ] 仅对 sharing violation 重试，其他错误直接返回
- [ ] 所有写入路径（writeFile/editFile/moveFile/applyPatch）统一使用

---

## M-2. 无长路径支持（Windows 260 字符限制）

**文件**: `internal/tool/builtin/confine.go`、`writefile.go`、`readfile.go`、`movefile.go`
**影响**: Windows 默认 MAX_PATH=260 字符。hiq 无 `\\?\` 前缀支持，深层嵌套目录的文件操作会失败。

**方案**:
```go
// path_helpers.go — 新文件
func extendLengthPath(path string) string {
    if runtime.GOOS != "windows" {
        return path
    }
    // 已有前缀则跳过
    if strings.HasPrefix(path, `\\?\`) {
        return path
    }
    // 关键：先将 / 转为 \，否则 \\?\ 前缀会关闭路径清洗，Windows API 直接拒绝
    path = filepath.FromSlash(path)
    path = filepath.Clean(path)
    // UNC 路径
    if strings.HasPrefix(path, `\\`) {
        return `\\?\UNC\` + path[2:]
    }
    // 普通绝对路径
    if filepath.IsAbs(path) {
        return `\\?\` + path
    }
    return path
}
```

> **注意**: `\\?\` 前缀会关闭 Windows 的自动路径清洗，路径中绝对不能出现正斜杠 `/`。必须先 `filepath.FromSlash` + `filepath.Clean` 再拼前缀。

**验收标准**:
- [ ] 先 `filepath.FromSlash` 再 `filepath.Clean`，最后拼 `\\?\`
- [ ] `realPath()` 和 `resolveIn()` 输出调用 `extendLengthPath`
- [ ] 普通路径和 UNC 路径均正确处理
- [ ] 非 Windows 平台无副作用

---

## M-3. 8 个 browser 工具缺 session_id 描述

**文件**: `internal/tool/builtin/browser.go` — 8 处
**影响**: `browserClick`、`browserType`、`browserScroll`、`browserExtract`、`browserScreenshot`、`browserEvaluate`、`browserSelectOption`、`browserUploadFile` 的 `session_id` 参数仅有 `{"type":"string"}` 无 description。模型需要猜测其用途。

**方案**: 为每个工具的 `session_id` 添加统一描述：
```go
"session_id": map[string]any{"type": "string", "description": "Browser session id from browser_open"},
```

**验收标准**:
- [ ] 8 个工具全部添加 description
- [ ] 描述内容与 `browserNavigate` 的 `session_id` 一致

---

## M-4. delete_symbol.kind 应使用 enum

**文件**: `internal/tool/builtin/delete_symbol.go:48`
**影响**: `kind` 参数的有效值（func/method/type/interface/const/var）仅在 description 中文字列出，未用 `enum` 约束。模型可能传入无效值（如 "function" 而非 "func"），导致运行时错误。

**方案**:
```go
"kind": map[string]any{
    "type": "string",
    "enum": []string{"func", "method", "type", "interface", "const", "var"},
    "description": "Optional kind filter",
},
```

**验收标准**:
- [ ] 使用 `enum` 约束 6 个有效值
- [ ] 无效值在 schema 验证阶段被拒绝

---

## M-5. browser_scroll.amount 缺 minimum

**文件**: `internal/tool/builtin/browser.go:1516`
**影响**: `amount` 为 integer 但无 `minimum` 约束。负值语义错误（已有 `direction` 参数控制方向）。

**方案**:
```go
"amount": map[string]any{"type": "integer", "minimum": 1, "description": "Pixels to scroll (default 600)"},
```

**验收标准**:
- [ ] 添加 `"minimum": 1`
- [ ] 负值在 schema 验证阶段被拒绝

---

## M-6. web_fetch 1 MiB 限制未文档化

**文件**: `internal/tool/builtin/webfetch.go:31` + `:37`
**影响**: `webFetchMaxRead = 1 << 20` 在代码中定义，但工具描述未提及。模型获取大页面时不知道内容会被截断。

**方案**: 在描述末尾添加：
```
Note: body is capped at 1 MiB; larger pages are truncated.
```

**验收标准**:
- [ ] 描述中包含 1 MiB 限制说明
- [ ] 截断行为对模型透明

---

## M-7. SerialAddon 死代码

**文件**: `internal/instruction/instruction.go:39`
**影响**: `SerialAddon` 是一个 `const string`，定义后从未被引用。`ForModel()`（line 32）仅调用 `FamilyAddon()`，从不调用 `SerialAddon`。它是死代码。

**方案**: 删除 `instruction.go:39` 的 `const SerialAddon` 定义。

**验收标准**:
- [ ] 删除 `internal/instruction/instruction.go:39` 的 `const SerialAddon = ...` 定义
- [ ] 确认代码库中无其他引用（grep 验证）
- [ ] 编译通过

---

## M-8. 日期注入硬编码中文

**文件**: `internal/agent/boot.go:345-347`
**影响**: 日期时间块 `# 当前时间\n【重要】现在是...` 始终为中文，无视用户语言设置。与 `LanguagePolicy`（line 1355）"reply in the same language the user uses" 矛盾。

**方案**:
```go
// boot.go — 根据语言设置选择日期格式
if lang == "zh" {
    sysPrompt += fmt.Sprintf("\n\n# 当前时间\n【重要】现在是 %s 周%s。...", now.Format("2006-01-02 15:04"), weekdaysZh[now.Weekday()])
} else {
    sysPrompt += fmt.Sprintf("\n\n# Current Time\n[IMPORTANT] Current time: %s. ...", now.Format("2006-01-02 15:04 MST"))
}
```

**验收标准**:
- [ ] 中文用户显示中文日期块
- [ ] 非中文用户显示英文日期块
- [ ] 语言判断与 `LanguagePolicy` 逻辑一致

---

## M-9. 无错误恢复指导

**文件**: `internal/agent/config.go`（系统提示）
**影响**: 系统提示未告诉模型工具调用失败时如何处理。模型可能重复相同失败操作，或在错误后直接放弃。

**方案**: 在系统提示中添加 `# Error Recovery` 节：
```
# Error Recovery
- If a tool call fails, read the error message carefully before retrying.
- Do not retry the exact same call — change your approach.
- For file-not-found errors, use glob or grep to locate the correct path.
- For permission errors, check if the file is locked by another process.
- If stuck after 2-3 attempts, explain the problem to the user and ask for help.
```

**验收标准**:
- [ ] 包含 "do not retry the exact same call" 规则
- [ ] 列出常见错误类型的恢复策略
- [ ] 与 H-9 的分节结构合并

---

## M-10. 无编码风格指导

**文件**: `internal/agent/config.go`（系统提示）
**影响**: 系统提示无任何编码风格指引。模型可能生成与项目风格不一致的代码（命名、注释、错误处理）。

**方案**: 在系统提示中添加 `# Coding Style` 节：
```
# Coding Style
- Match the existing code style in the project you're working in.
- Follow the language's standard conventions (go fmt, prettier, black, etc.).
- Add comments only when the intent is non-obvious.
- Handle errors explicitly — do not silently ignore them.
- Write tests for new functionality when the project has existing tests.
```

**验收标准**:
- [ ] 包含 "match existing style" 核心原则
- [ ] 涵盖命名/格式/注释/错误处理/测试
- [ ] 与 H-9 的分节结构合并

---

## M-11. browser_wait ReadOnly=false 但无副作用

**文件**: `internal/tool/builtin/browser.go:2252`
**影响**: `browser_wait` 的 `ReadOnly()` 返回 `false`，但它仅阻塞等待（无磁盘写入、无网络请求、无 DOM 变更）。这阻止了它与其他只读工具在批量中并行执行。

**方案**:
```go
func (browserWait) ReadOnly() bool { return true } // wait is side-effect-free
```

**验收标准**:
- [ ] `ReadOnly()` 改为返回 `true`
- [ ] 可与其他只读浏览器工具并行执行

---

## M-12. wait 工具命名过于泛化

**文件**: `internal/tool/builtin/bgjobs.go:131`
**影响**: 工具名为 `wait`，在多工具上下文中含义模糊。对比 `bash_output`、`bash_kill` 有 `bash_` 前缀。

**方案**: 重命名为 `wait_job` 或保持 `wait` 但在描述中强调 "Wait for a background job to finish"。（注：重命名可能影响已有会话的工具引用，需评估兼容性。）

**验收标准**:
- [ ] 若重命名，旧会话中的工具引用仍能正确解析
- [ ] 描述明确说明是等待后台任务

---

## M-13. ls schema 无 required 数组

**文件**: `internal/tool/builtin/ls.go:27`
**影响**: schema 无 `required` 字段。虽然所有参数确实是可选的（默认 "."），但缺少显式 `"required": []` 可能让某些 JSON Schema 消费者产生歧义。

**方案**:
```go
Schema: map[string]any{
    "type":     "object",
    "properties": map[string]any{...},
    "required": []string{}, // 显式空数组
},
```

**验收标准**:
- [ ] 添加 `"required": []`
- [ ] 行为不变

---

# LOW — 5 项

## L-1. 无上下文管理指导

**文件**: `internal/agent/config.go`（系统提示）
**影响**: 模型不知道上下文窗口限制、消息压缩机制、早期消息可能被摘要。可能引用已被压缩掉的内容。

**方案**: 在系统提示中添加：
```
# Context Management
- Earlier messages may be summarized to stay within the context window.
- Rely on file contents and todo_write state rather than remembering what was discussed earlier.
- If you need to reference something from earlier, re-read the relevant file.
```

**验收标准**:
- [ ] 提及消息可能被摘要
- [ ] 指导模型使用文件/todo 作为持久状态

---

## L-2. 安全指令极少（dev 模式）

**文件**: `internal/agent/config.go`（系统提示）
**影响**: dev 配置无任何安全相关指令（不提 rm -rf、sudo、.env、credentials）。完全依赖沙箱和权限层。

**方案**: 在 H-9 重构的 `# Safety` 节中添加（已包含在 H-9 方案中）：
```
- Do not run destructive commands (rm -rf, sudo) without explicit user request.
- Do not read or modify .env, credentials, or SSH keys unless explicitly asked.
- Do not make network requests to unexpected endpoints.
```

**验收标准**:
- [ ] 至少列出 3 类禁止行为
- [ ] 与 H-9 合并，不重复

---

## L-3. Checkpoint 无清理机制

**文件**: `internal/checkpoint/checkpoint.go`
**影响**: checkpoint 文件只增不减，无 TTL、数量限制或清理函数。长期运行的会话会持续占用磁盘空间。

**方案**:
```go
// checkpoint.go — 添加清理方法
const maxCheckpointsPerSession = 50

func (s *Store) Prune() {
    if s.dir == "" { return } // 纯内存模式无需清理
    entries, err := os.ReadDir(s.dir)
    if err != nil || len(entries) <= maxCheckpointsPerSession {
        return
    }
    // checkpoint 文件名为 turn-%d.json，按修改时间排序删除最旧的
    type fileInfo struct {
        path    string
        modTime time.Time
    }
    var files []fileInfo
    for _, e := range entries {
        if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
            continue
        }
        info, _ := e.Info()
        files = append(files, fileInfo{
            path:    filepath.Join(s.dir, e.Name()),
            modTime: info.ModTime(),
        })
    }
    sort.Slice(files, func(i, j int) bool {
        return files[i].modTime.Before(files[j].modTime)
    })
    for _, f := range files[:len(files)-maxCheckpointsPerSession] {
        os.Remove(f.path)
    }
}
```

> **注意**: checkpoint 文件存储在 `s.dir`（`<session>.ckpt/`）下，文件名为 `turn-%d.json`，不是 `*.ckpt`。

**验收标准**:
- [ ] 读取 `s.dir` 下的 `turn-*.json` 文件
- [ ] 单会话 checkpoint 上限 50 个
- [ ] 超限时按修改时间删除最旧的
- [ ] 在会话加载时调用 Prune

---

## L-4. CJK token 估算偏高

**文件**: `internal/agent/compact.go:400`
**影响**: `tokPerChar` 对 CJK 字符设为 1.5 token/字符。实际中文约 0.7-1.0 token/字符（取决于 tokenizer）。这会导致压缩器过早触发摘要，丢弃本可保留的上下文。

**方案**: 将 CJK `tokPerChar` 从 1.5 调整为 1.0。

**验收标准**:
- [ ] `tokPerChar` CJK 值改为 1.0
- [ ] 压缩触发时机更接近实际 token 消耗

---

## L-5. apply_patch 未调用 ValidateSyntax + runPostEditHook

**文件**: `internal/tool/builtin/apply_patch.go`
**影响**: `apply_patch` 执行文件修改后未运行 `validation.ValidateSyntax`（语法检查）和 `runPostEditHook`（LSP 诊断）。对比 `editfile.go` 和 `multiedit.go` 会运行这些钩子。

**方案**:
```go
// apply_patch.go — Phase 2 写入成功后
for _, ch := range changes {
    if ch.Kind == diff.Modify || ch.Kind == diff.Add {
        if diags := runPostEditHook(ch.Path, string(ch.New)); len(diags) > 0 {
            // 附加诊断信息到结果
        }
    }
}
```

**验收标准**:
- [ ] 每个修改/新增文件运行 `runPostEditHook`
- [ ] LSP 诊断信息附加到工具结果
- [ ] `ValidateSyntax` 在写入前运行（与 edit_file 一致）

---

## L-6. glob/grep 结果上限未文档化

**文件**: `internal/tool/builtin/glob.go:38`（`globMaxResults = 200`）、`internal/tool/builtin/grep.go:25`（`grepMaxMatches = 200`）
**影响**: 两个工具都有 200 结果上限，但描述中未提及。模型不知道结果可能被截断。

**方案**: 在两个工具的描述中添加：
```
Note: results are capped at 200 entries; use more specific patterns if you need to narrow down.
```

**验收标准**:
- [ ] glob 描述提及 200 上限
- [ ] grep 描述提及 200 上限

---

## L-7. notebookedit 别名容忍未文档化

**文件**: `internal/tool/builtin/notebookedit.go:143-159`
**影响**: 工具接受 `content`/`source`/`new_string` 作为 `new_source` 的别名，但描述中未说明。模型用别名时能工作但不知道为什么。

**方案**: 在描述中添加：
```
Accepts "content", "source", or "new_string" as aliases for "new_source".
```

**验收标准**:
- [ ] 描述提及别名
- [ ] 别名行为不变

---

# 汇总

| 级别 | 数量 | 关键主题 |
|------|------|---------|
| **HIGH** | 13 | OOM 防护×3、prompt injection、原子写入、session 完整性、schema 质量×3、系统提示结构×3、Cold bug、checkpoint 权限丢失 |
| **MEDIUM** | 13 | Windows 兼容×2、browser 描述×2、schema 约束×3、死代码、语言混杂、错误恢复、编码风格、ReadOnly、命名 |
| **LOW** | 5 | 上下文管理、安全指令、checkpoint 清理、CJK 估算、validate/hook 缺失、文档完善×2 |

**建议实施顺序**: H-1→H-2→H-3（OOM 三连）→ H-5（原子写入）→ H-13（checkpoint 权限）→ H-9+H-10+H-11（系统提示重构三合一）→ H-12（Cold bug）→ H-7+H-8（schema）→ M-1+M-2（Windows）→ 其余按需
