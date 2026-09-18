# STT 技术验证（Spike）

> 目的：在**不改 hiq 任何代码**的前提下，把“语音输入”功能的 4 个技术风险点跑通，用实测数据指导后续集成方案。验证文件保留，后续集成可参考。

## 风险点

1. **STT 调用链路**：阶跃 / 智谱 / OpenAI 的 `/audio/transcriptions` 能否用同一套 multipart POST 调通？智谱 `/api/paas/v4` 路径拼接对不对？
2. **音频格式兼容性（最关键）**：浏览器 `MediaRecorder` 实际产出什么格式？阶跃/智谱能不能直接吃？要不要转码？
3. **麦克风权限**：`getUserMedia` 能否拿到麦克风？
4. **智谱 ≤30s 限制**：真实短句场景影响多大？

---

## 目录结构

```
spike/stt/
├── README.md          本文件（含结论矩阵）
├── api/               后端 API 验证（独立 Go module，不依赖 hiq）
│   ├── go.mod
│   └── main.go        gen / test / matrix / providers 子命令
├── mic/
│   └── index.html     前端麦克风验证（纯静态单文件，浏览器双击打开）
└── samples/           测试音频（gen 生成 + 你放真实录音）
```

---

## 怎么跑

### 后端（spike/stt/api/）

```bash
cd spike/stt/api

# 0. 看看配了哪些厂商、key 是否就绪
go run . providers

# 1. 生成测试 wav（蜂鸣音，验链路用）
go run . gen                       # 默认 ../samples/beep.wav，3s

# 2. 设置需要验证的厂商 key（按需）
export STEPFUN_API_KEY=sk-...
export ZHIPU_API_KEY=...
export OPENAI_API_KEY=sk-...       # 国内通常需代理

# 3. 单家单文件测试
go run . test -provider stepfun -file ../samples/beep.wav
go run . test -provider zhipu   -file ../samples/beep.wav

# 4. 跑格式 × 厂商 矩阵（多个文件）
go run . matrix -files ../samples/beep.wav,../samples/voice.webm,../samples/voice.mp3
```

### 前端（spike/stt/mic/）

直接用浏览器打开 `mic/index.html`：
1. 点“探测支持的 MIME” → 记录 `MediaRecorder.isTypeSupported()` 结果
2. 点“开始录音” → 授权麦克风 → 说话 → “停止”
3. 页面会打印 Blob 的真实 `type`（**这就是前端实际录出的格式**）
4. 点“下载录音” → 存成文件，丢进 `samples/` 再用后端 `test`/`matrix` 转写

> 在 Wails/WebView2 里跑这个页能进一步验证宿主环境的麦克风权限，但 spike 阶段先用普通浏览器验证逻辑即可。

---

## ✅ 结论矩阵

### 0. 端点探活（probe，已实测，无需 API key）

`go run . probe` 对每个 STT 端点发带假 key 的请求，看返回 401（端点在）还是 404（不在）。

| 厂商 | 模式 | 端点 | HTTP | 结论 |
|---|---|---|---|---|
| 阶跃 stepfun | multipart | `https://api.stepfun.com/v1/audio/transcriptions` | **401** | ✅ 端点存在（"Incorrect API key"）|
| 智谱 zhipu | multipart | `https://open.bigmodel.cn/api/paas/v4/audio/transcriptions` | **401** | ✅ 端点存在（"令牌已过期"——**确认 `/paas/v4` 路径正确**）|
| 硅基流动 siliconflow | multipart | `https://api.siliconflow.cn/v1/audio/transcriptions` | **401** | ✅ 端点存在（"Invalid token"）|
| OpenRouter | multipart | `https://openrouter.ai/api/v1/audio/transcriptions` | **401** | ✅ 端点存在（"Missing Authentication header"）|
| 小米 MiMo | chat | `https://api.xiaomimimo.com/v1/chat/completions` | **401** | ✅ 端点存在（"Invalid API Key"，chat 模式也通）|
| OpenAI | multipart | `https://api.openai.com/v1/audio/transcriptions` | **401** | ✅ 端点存在（连 fake key 都回显了）|

**结论：6 家端点路径全部正确、全部可达、错误响应都是结构化 JSON。** 之前担心的智谱 `/paas/v4` 前缀、OpenRouter `/api/v1` 双层路径、MiMo 走 chat 端点，全部实测通过。OpenAI 在本环境也能直连（无需代理）。

### 1. 全景厂家分类（hiq 支持的 15 家独立厂商）

| 分类 | 厂商 | STT | 本 spike 覆盖 |
|---|---|---|---|
| OpenAI 兼容 multipart | OpenAI、阶跃、智谱、硅基流动、OpenRouter | ✅ | ✅ 已 probe 通过 |
| 半兼容（chat+input_audio）| 小米 MiMo | ✅ | ✅ 已 probe 通过 |
| 私有协议 | 阿里百炼、火山豆包、讯飞、百度、腾讯 | ✅（各自协议）| ❌ 需独立适配器（WS/签名/云产品鉴权），超出本 spike |
| 无 STT | DeepSeek、Moonshot、MiniMax、Anthropic | ❌ | N/A |

私有协议 5 家若要支持，需单独写适配器：
- **阿里百炼**：DashScope 协议（`run-task`/`finish-task` 事件流），Bearer。文档 https://help.aliyun.com/zh/model-studio/non-realtime-speech-recognition-user-guide
- **火山豆包**：`openspeech.bytedance.com` 私有协议，Token/Signature。文档 https://docs.volcengine.com/docs/6561
- **讯飞**：WebSocket + HMAC-SHA256 签名。文档 https://www.xfyun.cn/doc/asr/voicedictation/API.html
- **百度**：云产品，access_token 鉴权。文档 https://cloud.baidu.com/doc/SPEECH/index.html
- **腾讯云**：云产品，TC3-HMAC 签名。文档 https://cloud.tencent.com/document/product/1093

### 2. 厂商 × 音频格式（真实识别，待 API key）

文档声明的格式支持（爬虫验证）：

| 厂商 | wav | mp3 | webm/opus | m4a | 备注 |
|---|---|---|---|---|---|
| 阶跃 stepaudio-2.5-asr | ✅ | ✅ | ❌ | ✅(ogg) | 文档：mp3/pcm/ogg/wav |
| 智谱 glm-asr-2512 | ✅ | ✅ | ❌ | ❌ | 仅 wav/mp3，**单次 ≤30s** |
| 硅基流动 SenseVoiceSmall | _待测_ | _待测_ | _待测_ | _待测_ | 文档未明确，≤1h/50MB |
| OpenRouter (whisper-1) | _待测_ | _待测_ | _待测_ | _待测_ | 走 OpenAI whisper |
| 小米 MiMo-v2.5-asr | ✅ | ✅ | ❌ | _待测_ | 仅 mp3/wav（文档）|
| OpenAI whisper-1 | ✅ | ✅ | **✅** | ✅ | 唯一明确支持 webm |

实测（待 key，`go run . matrix -files ...`）：

| 格式 \ 厂商 | 阶跃 | 智谱 | 硅基流动 | OpenRouter | MiMo | OpenAI |
|---|---|---|---|---|---|---|
| wav | _待填_ | _待填_ | _待填_ | _待填_ | _待填_ | _待填_ |
| webm/opus | _待填_ | _待填_ | _待填_ | _待填_ | _待填_ | _待填_ |
| mp3 | _待填_ | _待填_ | _待填_ | _待填_ | _待填_ | _待填_ |

### 3. 浏览器实际录出格式（待 mic/index.html 实测）

| 环境 | MediaRecorder 默认 type | 备注 |
|---|---|---|
| Chrome (Windows) | _待填_ | 预期 `audio/webm;codecs=opus` |
| Edge (WebView2) | _待填_ | hiq Wails 用的内核 |
| Safari | _待填_ | 预期 `audio/mp4` |

### 4. 四个问题的回答

1. **一套代码能否调通 6 家？**
   ✅ 端点层面已验证（probe 全 401）。真实识别待 key——**multipart 一套覆盖 5 家，chat 单独函数覆盖 MiMo，共两套**。
2. **浏览器录出的 webm 阶跃/智谱能不能直接吃？**
   _待填（文档说不支持 webm，待 mic 录音 + 真实 key 交叉验证）_
3. **麦克风权限能否拿到？**
   _待填（mic/index.html 实测）_
4. **智谱 ≤30s 限制影响？**
   _待填（需真实 key + 35s 录音）_

---

## 给后续集成方案的数据支撑

跑完上面后，能据此决定：
- 若 webm 直通三家都 OK → 集成时前端直接 MediaRecorder，后端一套 multipart，最省事
- 若 webm 只 OpenAI OK → 要么前端转 wav（Web Audio API 采 PCM），要么默认厂商只能 OpenAI
- 若需要国内免代理（阶跃/智谱）又不支持 webm → 前端必须做 PCM/wav 转码，集成方案要把这部分算进去

**基于这些事实再出集成方案，就不再是基于假设。**
