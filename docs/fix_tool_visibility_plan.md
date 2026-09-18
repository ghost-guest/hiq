# Hiq Coding 工具可见性修复方案

> 问题：hiq 在切换界面时"吞掉"进行中的工具过程，用户看不到实时输出。
> 设计原则：**全量流式显示，完成后折叠** — 运行中的 tool 始终展开显示实时输出；完成后自动折叠为可展开的摘要卡片。

---

## 1. 问题校验

以下 6 个问题均通过源码逐行校验确认。

### 1.1 `case "history"` 全量替换 items

**文件:** `desktop/frontend/src/lib/useController.ts` 第 524-527 行

```typescript
case "history": {
  const { items, seq } = historyMessagesToItems(a.messages, "h", s.seq);
  return { ...s, items, seq };
}
```

`items` 被整体替换为 history 重建的结果。在 history 请求发出到返回之间到达的流式事件（`ToolProgress`、`text_delta` 等）产生的 item 被静默丢弃。

### 1.2 `loadSessionDataForTab` 的 reset 产生空帧

**文件:** `desktop/frontend/src/lib/useController.ts` 第 635-655 行

```typescript
if (reset) dispatchTo(tabId, { type: "reset" });  // 清空 items
// ... 中间有若干 dispatch ...
if (messages.length) dispatchTo(tabId, { type: "history", messages });
```

`reset` 和 `history` 之间存在一个渲染帧，此时 items 为空。如果 `messages` 也为空（后端尚未持久化），tab 完全空白。

### 1.3 30s 看门狗触发全量重载

**文件:** `desktop/frontend/src/lib/useController.ts` 第 758-774 行

```typescript
if (since >= 30_000) {
  void reconcileTabRuntime(activeTabId);
  return;
}
```

长时间编译/测试若 30 秒无 token 输出，触发 `reconcileTabRuntime()` → `loadSessionDataForTab(tabId, true)` → reset + history 全量重载。进行中的流式输出被覆盖。

### 1.4 `reconcileTabRuntime` 的 dispatch 顺序

**文件:** `desktop/frontend/src/lib/useController.ts` 第 679-703 行

```typescript
dispatchTo(tabId, { type: "backend_status", running: Boolean(tab.running) }); // 先标记 stopped
if (needsInitialLoad || missedTurnDone || isExpertTab) {
  await loadSessionDataForTab(tabId, missedTurnDone); // 再全量重载
}
```

`backend_status` 先将 running 工具标记为 stopped，然后 history 重载覆盖一切——stopped 标记被浪费。

### 1.5 minimal 模式过滤 bash 和 readOnly

**文件:** `desktop/frontend/src/components/Transcript.tsx` 第 937 行

```typescript
if (mode === "minimal") return (!it.readOnly && it.name !== "bash") || Boolean(it.attachments);
```

在默认的 `minimal` 模式下，`bash` 和所有 `readOnly` 工具（`read_file`、`grep`、`glob`、`ls`、`web_fetch`）被直接过滤，不渲染。

### 1.6 Shell 卡片完成后自动折叠

**文件:** `desktop/frontend/src/components/ToolCard.tsx` 第 122-126 行

```typescript
useEffect(() => {
  if (!item.isShell || userToggledRef.current) return;
  const should = item.status === "running";
  if (should !== open) setOpen(should);
}, [item.isShell, item.status, open]);
```

`status` 从 `"running"` 变为 `"done"` 时，`should` 变 `false`，卡片自动折叠。用户未手动操作过时，长命令的输出在完成后立刻隐藏。

---

## 2. 修复方案

### 改动 1：默认 displayMode 改为 "standard"

| 项目 | 内容 |
|------|------|
| 文件 | `desktop/frontend/src/lib/displayMode.ts` |
| 行 | 第 10 行 |
| 改动量 | 1 行 |
| 风险 | 低 |

**当前代码：**
```typescript
return "minimal";
```

**改为：**
```typescript
return "standard";
```

`standard` 模式下 `stepGroups` 不生效（`Transcript.tsx` 第 357 行 `if (displayMode === "standard") return null`），所有 tool item 都会被渲染，包括 bash 和 readOnly。用户仍可手动切回 minimal。

---

### 改动 2：history 合并策略 — 保留 running 状态的 item

| 项目 | 内容 |
|------|------|
| 文件 | `desktop/frontend/src/lib/useController.ts` |
| 位置 | `case "history"` reducer（第 524-527 行） |
| 改动量 | ~30 行（含新辅助函数） |
| 风险 | 中 |

**目标：** history 重建不覆盖仍在运行的工具，而是将 running item 保留在正确位置。

**改为：**
```typescript
case "history": {
  const { items: histItems, seq } = historyMessagesToItems(a.messages, "h", s.seq);
  const runningItems = s.items.filter(
    (it) => it.kind === "tool" && it.status === "running"
  );
  if (runningItems.length === 0) {
    return { ...s, items: histItems, seq };
  }
  const merged = mergeRunningIntoHistory(histItems, runningItems);
  return { ...s, items: merged, seq };
}
```

**新增辅助函数 `mergeRunningIntoHistory`：**

```typescript
function mergeRunningIntoHistory(
  histItems: Item[],
  runningItems: Item[]
): Item[] {
  // running items 按 parentId 分组，找到它们在 history 中的锚点
  const runningById = new Map(runningItems.map((it) => [it.id, it]));
  const result: Item[] = [];
  let inserted = new Set<string>();

  for (const it of histItems) {
    // 如果 history 中有一个同 id 的 tool item 且当前有 running 版本，
    // 说明 history 已经有终态了（turn 已结束），用 history 版本
    if (it.kind === "tool" && runningById.has(it.id) && it.status !== "running") {
      result.push(it);
      inserted.add(it.id);
      continue;
    }
    result.push(it);
  }

  // 仍在 running 但 history 中没有对应项的（尚未持久化）
  for (const it of runningItems) {
    if (!inserted.has(it.id)) {
      // 插入到其 parent assistant 之后
      const parentIdx = result.findIndex(
        (r) => r.kind === "assistant" && r.id === it.parentId
      );
      if (parentIdx >= 0) {
        // 找到 parent 后面最后一个同 parent 的 tool，插在后面
        let insertIdx = parentIdx + 1;
        while (
          insertIdx < result.length &&
          result[insertIdx].kind === "tool" &&
          (result[insertIdx] as any).parentId === it.parentId
        ) {
          insertIdx++;
        }
        result.splice(insertIdx, 0, it);
      } else {
        result.push(it);
      }
    }
  }

  return result;
}
```

**边界情况处理：**

| 场景 | 行为 |
|------|------|
| history 中已有同 id 且 status=done | 用 history 版本（turn 已结束） |
| history 中无对应项 | running item 保留，插入到 parent assistant 之后 |
| running item 的 parent 也不在 history 中 | 追加到末尾 |

---

### 改动 3：看门狗不触发全量重载

| 项目 | 内容 |
|------|------|
| 文件 | `desktop/frontend/src/lib/useController.ts` |
| 位置 | stale-stream watchdog（第 758-774 行） |
| 改动量 | ~5 行 |
| 风险 | 低 |

**方案 A（推荐）：标记 stale，不重载**

```typescript
if (since >= 30_000) {
  dispatchTo(activeTabId, { type: "stale", stale: true });
  return;
}
```

UI 层可显示一个 "连接可能断开" 的提示条，附带手动刷新按钮。不自动重载，避免吞掉进行中的输出。

**方案 B（保守）：提高阈值到 120s**

```typescript
if (since >= 120_000) {
  void reconcileTabRuntime(activeTabId);
  return;
}
```

保留自动重载但大幅提高门槛，给长编译/长测试留够时间。

**推荐方案 A** — 自动重载本身就是问题根源，标记 stale + 手动刷新更安全。

---

### 改动 4：minimal 模式保留 bash 输出

| 项目 | 内容 |
|------|------|
| 文件 | `desktop/frontend/src/components/Transcript.tsx` |
| 位置 | 第 937 行 |
| 改动量 | 1 行 |
| 风险 | 低 |

**当前：**
```typescript
if (mode === "minimal") return (!it.readOnly && it.name !== "bash") || Boolean(it.attachments);
```

**改为：**
```typescript
if (mode === "minimal") return !it.readOnly || Boolean(it.attachments);
```

去掉 `it.name !== "bash"` 条件，minimal 模式下 bash 工具也会渲染。readOnly 工具仍被过滤（保留 minimal 的简洁感），但 bash 是 coding 的核心输出，不应隐藏。

---

### 改动 5：Shell 卡片不自动折叠

| 项目 | 内容 |
|------|------|
| 文件 | `desktop/frontend/src/components/ToolCard.tsx` |
| 位置 | 第 122-126 行 |
| 改动量 | ~3 行 |
| 风险 | 低 |

**当前：**
```typescript
useEffect(() => {
  if (!item.isShell || userToggledRef.current) return;
  const should = item.status === "running";
  if (should !== open) setOpen(should);
}, [item.isShell, item.status, open]);
```

**改为：**
```typescript
useEffect(() => {
  if (!item.isShell || userToggledRef.current) return;
  // running 时展开，done 后保持当前状态（不自动折叠）
  if (item.status === "running" && !open) setOpen(true);
}, [item.isShell, item.status, open]);
```

移除 `should !== open` 的双向同步，改为单向：running 时展开，done 后不操作。用户可手动折叠。

---

## 3. 改动汇总

| # | 文件 | 改动 | 行数 | 风险 |
|---|------|------|------|------|
| 1 | `displayMode.ts` | 默认 "standard" | 1 | 低 |
| 2 | `useController.ts` | history merge running items | ~30 | 中 |
| 3 | `useController.ts` | watchdog 标记 stale | ~5 | 低 |
| 4 | `Transcript.tsx` | minimal 保留 bash | 1 | 低 |
| 5 | `ToolCard.tsx` | shell 不自动折叠 | ~3 | 低 |
| | **合计** | | **~40 行** | |

---

## 4. 验证计划

### 手动测试

1. **Tab 切换不丢过程**
   - 发起一个长时间 bash 命令（如 `sleep 10 && echo done`）
   - 在命令执行期间切换到另一个 tab，再切回来
   - 预期：bash 输出仍可见，status 为 running

2. **全量流式显示**
   - 发起一个多步 coding 任务（edit + bash + grep）
   - 预期：每一步的实时输出都展开显示

3. **完成后折叠**
   - 等待上述任务完成
   - 预期：bash 保持展开（改动 5），readOnly 被折叠，edit 保持展开

4. **minimal 模式**
   - 手动切到 minimal 模式
   - 预期：bash 可见，readOnly 被折叠，edit 可见

5. **30s 无输出场景**
   - 发起 `sleep 60` 命令
   - 等待 30s
   - 预期：不触发重载，UI 可能显示 stale 提示

### 自动化测试

- `useController` 的 history reducer：验证 running items 不被覆盖
- `mergeRunningIntoHistory`：验证各种边界情况（parent 匹配、history 已有终态等）

---

## 5. 后续可选优化（P2/P3，不在本次范围）

| 优先级 | 改动 | 说明 |
|--------|------|------|
| P2 | 合入 PI 的 `edits[]` 多处编辑 | 减少 LLM 往返 |
| P2 | 加 file-mutation-queue 防竞态 | 并行写同一文件安全 |
| P2 | 截断安全：token limit 拒绝执行 tool call | 防止半截参数 |
| P3 | Session 树结构 + branch 摘要 | 更强的会话管理 |
| P3 | 项目信任系统 | 项目级资源加载授权 |
