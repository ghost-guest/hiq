# CUA（Computer-Use Agent）Demo 与验证清单

本文件帮你验证 hiq 的通用电脑操作能力（CUA）是否跑通。
CUA 的本质：你下**任意**指令，hiq 自主地 **看屏幕 → 决策 → 执行 → 验证**，循环直到完成。

> 前提：本仓库的 `bin/hiq.exe` 必须是用最新代码构建的（构建标签 `dev-cua`）。
> 确认：`./bin/hiq.exe version`  应显示 `dev-cua`。

---

## 0. 一次性配置（可选但推荐）

编辑 `hiq.toml`（没有就 `./bin/hiq.exe setup` 生成），加上：

```toml
[network]
# 国内访问外网（GitHub 等）需要代理时才配
proxy_mode = "custom"
proxy_url = "http://127.0.0.1:7890"   # 换成你自己的代理端口

[cowork]
browser_headless = false                                              # 有头，更像真人
browser_user_data_dir = "C:/Users/<你的用户名>/.hiq/chrome-profile"  # 持久登录态（路径换成你的）
vlm_backend = "openai-compatible"     # 视觉模型后端（用OpenAI-compatible；或 "provider" + vlm_model）
```

环境变量（`.env` 或系统环境）：
```
OPENAI_API_KEY=你的key      # OpenAI-compatible/VLM 后端用
```

---

## 1. 最小验证：确认工具就位（30 秒）

```bash
cd <项目路径>\hiq
./bin/hiq.exe chat --profile cowork
```

进入后输入：
```
列出你所有能用的工具，特别是 screen_ 和 browser_ 开头的
```

**预期**：模型列出 `screen_perceive`、`screen_click`、`screen_type`、`browser_open`、`browser_snapshot`、`browser_attach` 等。
- ✅ 列出来了 → cowork 模式生效，工具已注册。
- ❌ 只看到 grep/read_file 这类 → `--profile cowork` 没生效，检查命令和配置。

---

## 2. Demo A：桌面操作（记事本写一句话并存盘）

**目的**：验证"看屏幕→操作任意 App"闭环，不依赖浏览器。

```bash
./bin/hiq.exe run --profile cowork --max-steps 50 "打开记事本（notepad），输入'CUA测试成功'，然后保存到桌面叫 cua-test.txt"
```

**预期行为**（你会看到屏幕上自动发生）：
1. 启动 notepad（开始菜单或直接 notepad 命令）
2. screen_perceive 看到编辑区 → screen_type 输入文字
3. Ctrl+S → 在保存对话框输入文件名 → 确认

**检查表**：
- [ ] hiq 自己调用了 screen_perceive / screen_click / screen_type（在终端能看到工具调用）
- [ ] 记事本里真的出现了文字
- [ ] 桌面真的生成了 cua-test.txt，内容正确
- [ ] 全程你只下了那一句话指令，没手动干预

---

## 3. Demo B：浏览器操作（GitHub 搜索）

**目的**：验证浏览器精确通道 + 代理修复。

```bash
./bin/hiq.exe run --profile cowork --max-steps 50 "用浏览器打开 github.com，搜索 reasonix，告诉我第一个结果的仓库名和 star 数"
```

**预期**：
1. browser_open（或 browser_attach）开浏览器
2. browser_snapshot 取页面 → 找到搜索框 ref → browser_type 输入
3. 回车 → 再次 snapshot → 读取第一个结果文字

**检查表**：
- [ ] 浏览器能正常打开 GitHub（不报 context canceled）→ 代理/headless 修复生效
- [ ] 用 browser_snapshot + ref 操作，而不是瞎猜坐标
- [ ] 返回的结果正确（esengine/DeepSeek-Reasonix，23.7k 左右 star）

**如果报 EOF / 连不上**：检查 `[network] proxy` 配置和 `OPENAI_API_KEY`。

---

## 4. Demo C：接管你已开的浏览器（attach 模式）

**目的**：验证 browser_attach——操控你已经登录的浏览器。

第 1 步，先**完全退出**所有 Chrome，然后带调试端口启动：
```powershell
& "C:\Program Files\Google\Chrome\Application\chrome.exe" `
    --remote-debugging-port=9222 `
    --user-data-dir="C:\Users\<你的用户名>\AppData\Local\Google\Chrome\User Data"
```
（用日常 profile，保留你的 GitHub 登录态）

第 2 步：
```bash
./bin/hiq.exe chat --profile cowork
```
输入：
```
我已经在 9222 端口开好了 Chrome（已登录 GitHub），用它打开我的 GitHub 仓库列表页，告诉我有哪些仓库
```

**检查表**：
- [ ] 模型调用了 browser_attach {"cdp_url":"http://127.0.0.1:9222"}
- [ ] 操作发生你那个已开的 Chrome 窗口里（不是新开的）
- [ ] 登录态保留（不用重新登录）
- [ ] 任务结束后 Chrome 还开着（attach 不杀进程）

---

## 5. Demo D：视觉通道（反爬/特殊页面）

**目的**：验证截图→VLM→坐标点击的降级路径。

```bash
./bin/hiq.exe chat --profile cowork
```
输入：
```
用截图的方式看着屏幕操作：打开浏览器访问 https://www.baidu.com ，看着搜索框的位置点击并输入"今天天气"，然后点搜索
```
（"用截图的方式"这句话会引导模型走视觉通道而非 DOM）

**检查表**：
- [ ] 模型调用 browser_screenshot + image_understand 得到坐标
- [ ] 用 browser_click {target:{x,y}} 按坐标点击
- [ ] 搜索结果页正常出现

---

## 排错速查

| 现象 | 原因 | 处理 |
|---|---|---|
| 模型说"我没有 screen_ 工具" | 没进 cowork | 命令加 `--profile cowork` |
| browser_navigate: context canceled | 跑的是旧二进制 / 没代理 | 重新 `make`；配 `[network] proxy` |
| image_understand: EOF | OpenAI-compatible直连没走代理 | 已修复，确认跑的是新二进制 + 配了代理 |
| 模型不截图就开始瞎点 | 提示词没生效 | 确认 `dev-cua` 版本；明确说"先截图看清楚再操作" |
| 操作卡住反复点同一处 | 死循环（护栏应触发） | 已加护栏；若仍循环，Ctrl+C 停止并反馈 |

---

## 反馈

跑完任意一个 Demo，把**终端里模型调用的工具序列**和**实际结果**告诉我，我据此继续调优（比如提示词措辞、感知精度、护栏阈值）。
