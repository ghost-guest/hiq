# hiq CUA 测试序列（重新设计版）

三个**互相独立**的测试，由易到难。每个都是一条命令，可直接复制粘贴。
**核心目的**：验证 hiq 能不能自主地"看屏幕→操作→验证"。

> 前提：`bin/hiq.exe` 是 dev-cua 版本。先确认：`./bin/hiq.exe version`
> 全程在 Git Bash 或 PowerShell 里跑都行，命令是跨 shell 通用的。

---

## 测试 1（必做，最简单）：验证"看屏幕"闭环

**这一步不操作任何东西，只让 hiq 截图并描述。** 用来确认 screen_perceive / image_understand 链路通。

```bash
cd <项目路径>/hiq
./bin/hiq.exe run --profile cowork --max-steps 10 "截一张当前屏幕的图，然后告诉我屏幕上现在开着哪些窗口、每个窗口的标题是什么。不要点击或操作任何东西，只看。"
```

**判断标准**：
- ✅ **成功**：它准确说出你屏幕上当前开着的窗口（比如"有一个 VSCode 窗口、一个 Chrome、任务栏在底部"）
- ❌ **失败信号**：
  - 报 `EOF` / `image/text` 错误 → OpenAI-compatible VLM 没接通（检查 `OPENAI_API_KEY` 和代理配置）
  - 报 `UIA dump failed` 且没 fallback → 跑的是旧二进制，重新 `make`
  - 它乱编屏幕内容（说的和实际不符）→ VLM 调用没真正发生

**这一步通了，后面才有意义。不通的话，把报错完整贴给我。**

---

## 测试 2（记事本，但用更稳的姿势）：验证"操作桌面 App"

和上次不同，我**拆成两步**：先确认能开程序，再让它操作。任务也写得更明确。

### 第 2a 步：只开记事本（不输入不保存）

```bash
cd <项目路径>/hiq
./bin/hiq.exe run --profile cowork --max-steps 15 "用 bash 工具执行命令 'notepad.exe' 打开 Windows 记事本，然后截图确认它已经打开了。只需要打开它，不要输入任何文字。"
```

> 明确告诉它"用 bash 执行 notepad.exe"——避免它走 start 菜单那条容易失败的路径。

**判断标准**：
- ✅ 记事本窗口真的弹出来了，hiq 截图后说"已打开"
- ❌ 如果 bash 报 `notepad not found`，换成：`cmd.exe /c start notepad`

### 第 2b 步：在已开的记事本里输入（手动开好记事本，再跑这个）

先**你自己手动**打开一个记事本（开始菜单→记事本），点一下编辑区让它获得焦点。然后：

```bash
cd <项目路径>/hiq
./bin/hiq.exe run --profile cowork --max-steps 15 "屏幕上已经打开了一个记事本窗口并且编辑区有焦点。用 screen_type 工具输入文字 'Hello from CUA'，然后截图确认文字已经出现在记事本里。"
```

**判断标准**：
- ✅ 记事本里真的出现了 `Hello from CUA`
- ❌ 文字跑到别的窗口去了 → 焦点问题，screen_type 是往当前焦点输入的

---

## 测试 3（浏览器）：验证 GitHub 操作 + 代理修复

```bash
cd <项目路径>/hiq
./bin/hiq.exe run --profile cowork --max-steps 40 "用 browser_open 打开 https://github.com，等页面加载后用 browser_snapshot 看页面结构，找到搜索框，用 browser_type 输入 'reasonix' 并回车搜索，然后告诉我第一个搜索结果的仓库名和 star 数。每一步操作后都要重新 snapshot 确认。"
```

**判断标准**：
- ✅ 浏览器打开 GitHub **不报 context canceled** → 代理/headless 修复生效
- ✅ 用 snapshot + ref 操作，返回正确结果（`esengine/DeepSeek-Reasonix`，约 23.7k star）
- ❌ 如果 GitHub 打不开 / 一直转圈 → 代理没配好，在 `hiq.toml` 加：
  ```toml
  [network]
  proxy_mode = "custom"
  proxy_url = "http://127.0.0.1:7890"   # 你的代理端口
  ```

---

## 怎么把结果反馈给我

**每个测试，请贴这两样：**

1. **hiq 终端输出的最后 30 行左右**（包含它调用了哪些工具、有没有报错）
2. **一句话结论**：成功了还是失败了，实际现象是什么

格式示例：
```
测试1：✅ 成功
它说屏幕上有 VSCode、Chrome、Terminal 三个窗口，和实际一致。
终端最后几行：
[贴终端输出]

测试2a：❌ 失败
报错：[贴报错]
```

---

## 排错速查

| 现象 | 处理 |
|---|---|
| `hiq.exe: command not found` | 用 `./bin/hiq.exe`（带 `./`），或先 `cd` 到 hiq 目录 |
| 报 `OPENAI_API_KEY not set` | 设环境变量：`export OPENAI_API_KEY=你的key`（Git Bash） |
| screen_perceive 报 EOF | OpenAI-compatible API 连不上，检查网络/代理；确认跑的是 dev-cua 新二进制 |
| browser 报 context canceled | 代理没配，见测试3说明 |
| 模型说"我没有这个工具" | 命令漏了 `--profile cowork` |
| PowerShell 跑不了 exe | 在 Git Bash 里跑，或 exe 用完整路径 |
