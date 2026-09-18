# Hiq 动态模型注册表实施方案 (v1.1 修订版)

> **版本**: v1.1 | **日期**: 2026-08-04 | **状态**: 待实施
>
> **目标**: 将 Hiq 从硬编码的静态模型模板库平滑升级为动态模型注册表，实现自动获取、自动更新、零维护的多厂商模型管理，且**完全无缝兼容现有的配置持久化与前端 UI 体系**。

---

## 一、背景与需求

### 1.1 现状问题

Hiq 当前采用**硬编码方式**管理官方推荐的模型提供商模板（`desktop/provider_templates.go` 中的 `builtInTemplates`）：

**存在的问题：**

| 问题 | 影响 | 严重程度 |
|------|------|----------|
| **维护成本高** | 每次厂商发布新模型或更改 URL，都必须发版更新二进制文件 | 高 |
| **更新滞后** | 无法第一时间响应行业模型发布节奏 | 高 |
| **容易出错** | 手动填写 URL 容易出错（如智谱 Coding Plan URL 错误） | 中 |

### 1.2 行业参考与外部数据源

**models.dev 注册表：**
`models.dev` 是一个社区维护的模型注册表（或自建的云端 API），包含：
- 主流供应商的 API 端点、标识（如 Coding Plan）
- 模型分类及推荐映射（主力、视觉、迅捷任务模型）
- 额外元数据：成本估算（input/output token 价格）、上下文窗口

### 1.3 核心设计原则（修正版）

1. **绝对兼容**：注册表仅作为**“动态模板库”**替换原有的硬编码模板。用户配置持久化（`hiq.toml`）、连通性测试等现有核心架构**一字不改**。
2. **职责分离**：
   - **更新注册表**：从 `models.dev` 获取全球“已知模型库和推荐模板”。
   - **刷新模型 (Fetch)**：利用用户 API Key 从具体厂商的 `/v1/models` 获取“用户真实可用/私有微调”的模型。
3. **数据解耦**：移除业务层代码中的硬编码判断（如通过截取后缀判断 Coding Plan），全部转为通过 JSON 的 `Tags` 元数据驱动。

---

## 二、预期效果

### 2.1 用户体验提升

**改进前：**
新模型发布后，用户必须等待 Hiq 更新版本才能在添加向导中看到预设。

**改进后：**
后台静默更新模型库，用户打开“添加提供商”的 18 宫格向导时，即可直接看到并使用最新的模型推荐配置，无需等待版本更新。

### 2.2 维护成本降低

| 方面 | 改进前 | 改进后 | 提升 |
|------|--------|--------|------|
| **新增供应商/模型** | 改代码 + 重新编译发版 | 注册表服务端添加即可 | 100% |
| **URL 修正** | 需要发版更新 | 服务端热修复 | 100% |

---

## 三、技术方案

### 3.1 整体架构（四层兜底）

```text
┌─────────────────────────────────────────────────────────────┐
│                    Hiq 注册表获取流程                    │
├─────────────────────────────────────────────────────────────┤
│  1. 触发获取 (启动时异步 or 用户点击更新)                     │
│  2. 初始化模型模板注册表                                      │
│     ├─ 检查本地缓存 (~/.hiq/models-cache.json)          │
│     ├─ 缓存有效 (< 12小时) → 内存读取                         │
│     ├─ 缓存过期 → 从 models.dev 远程拉取并序列化              │
│     └─ 获取失败/无网络 → fallback 使用程序内置的 JSON 快照    │
│  3. 对接前端                                                  │
│     └─ `GetProviderTemplates()` 直接返回注册表的处理结果      │
└─────────────────────────────────────────────────────────────┘
```

### 3.2 目录结构

```text
hiq/
├── desktop/
│   ├── registry.go               # 注册表核心 (接管原 provider_templates.go)
│   ├── registry_fetcher.go       # 远程拉取与本地缓存逻辑
│   └── default_registry.json     # 内置快照数据 (//go:embed)
├── docs/
│   └── MODEL_REGISTRY_PLAN.md    # 本文档
```

*(注：放在 `desktop/` 是因为模板数据主要服务于前端添加向导，底层 `core/config` 无需感知。)*

---

## 四、核心模块设计（解决冗余与无序问题）

### 4.1 数据结构：复用并扩展 `ProviderTemplate`

为了不修改任何现有的前端映射逻辑，直接在现有的 `ProviderTemplate` 上进行字段扩充。

**特别注意：**
模型列表 `Models` 必须继续使用 `[]string` 以保证前端下拉框中的排序符合官方推荐顺序（绝对不可使用 Map 导致每次启动顺序随机）。

```go
// desktop/registry.go

type ProviderTemplate struct {
    Name          string   `json:"name"`
    Kind          string   `json:"kind"`
    BaseURL       string   `json:"baseUrl"`
    APIKeyEnv     string   `json:"apiKeyEnv"`
    ContextWindow int      `json:"contextWindow"`
    
    // 【核心能力1】：映射我们在前端的新 UI，提供三大场景的最佳官方推荐
    DefaultModel  string   `json:"defaultModel"` 
    VisionModel   string   `json:"visionModel"`  
    FastModel     string   `json:"fastModel"`    
    
    // 使用 Slice 保障前端模型选择下拉框的特定排序（如：核心模型在前，老模型在后）
    Models        []string `json:"models"` 

    // 【新增扩展能力】：替代此前的代码硬编码，使用标签驱动
    Tags          []string            `json:"tags"` // 例如: ["coding-plan", "aggregator"]
    
    // 【新增扩展能力】：供未来进行成本估算和智能路由使用的元数据
    Capabilities  map[string]ModelCap `json:"capabilities,omitempty"`
}

type ModelCap struct {
    Vision    bool    `json:"vision"`
    Reasoning bool    `json:"reasoning"`
    InputCost float64 `json:"input_cost"`   // 每百万 Token 成本
    OutputCost float64 `json:"output_cost"`
}
```

### 4.2 数据获取与容错设计

```go
// desktop/registry_fetcher.go

// FetchTemplates 带有极强的容错能力
func FetchTemplates(ctx context.Context, url string) ([]ProviderTemplate, error) {
    req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
    // 强制增加版本号，以防服务端数据结构突变
    req.URL.Path = "/api/v1/registry.json"
    
    // 发起请求并解析...
    
    // 【容错】：允许解析中忽略未知字段；如果某条供应商记录没有 Name 或 Models 长度为0，直接剔除，而不中断整个解析。
}
```

### 4.3 解耦业务逻辑

之前方案中的 `isCodingPlan` 字符串截取代码直接移除。
前端或后端需要判断时，改用 `Tags` 识别：
```go
func (p *ProviderTemplate) IsCodingPlan() bool {
    for _, tag := range p.Tags {
        if tag == "coding-plan" {
            return true
        }
    }
    return false
}
```

---

## 五、配置持久化设计（保持现有逻辑不变）

`hiq.toml` 中 `[[providers]]` 的逻辑**保持一字不改**：
* 注册表仅作为“待选商品库”。
* 当用户在 GUI 点击添加 `DeepSeek` 并保存后，系统执行现有的 `SaveProvider`。
* 供应商配置被完整克隆一份并写入 `hiq.toml`。
* 这种设计完美兼容已有的 `ProviderAccess`（管理卡片显示），以及用户的 `.env` 密钥存储逻辑。

而在全局系统配置中，仅增加注册表自身的行为控制：

```toml
[desktop]
# 注册表拉取地址（为空则强制仅使用内置默认 json）
registry_url = "https://models.dev"
# 更新频次
registry_ttl_hours = 12
```

---

## 六、GUI 对接设计（替代 CLI 方案）

将原计划中的 CLI 降级，全力保障 GUI 体验。

1. **设置面板入口**：
   在现有的 `SettingsPanel.tsx` -> "通用" 中，增加一个子模块【模型提供商支持库】。
   - 界面显示当前库的最后更新时间。
   - 提供一个【手动检查更新】按钮。

2. **向导数据喂入**：
   现有的 `GetProviderTemplates()` API 从底层读取内存中已初始化的 `ModelRegistry`，将合并、兜底后的 `[]ProviderTemplate` 无缝发送给 `VendorStep` 渲染。前端无需修改。

3. **双重拉取概念明确**：
   - 【全局】设置 -> 通用 -> **更新提供商支持库**（拉取 `models.dev`，获得最新的厂商和推荐名单）。
   - 【个体】具体提供商卡片 -> **刷新模型**（请求 `api.厂商.com/v1/models`，使用用户的 API Key 获取微调模型和测试连通性）。

---

## 七、实施计划 (共计 4 天)

| Phase | 耗时 | 文件 | 说明 |
|---|---|---|---|
| **Phase 1<br>注册表核心** | 1.5 天 | `desktop/registry.go`<br>`desktop/registry_fetcher.go`<br>`desktop/default_registry.json` | 实现带 4 层兜底的模型模板拉取机制；替换掉静态 `builtInTemplates`；引入 Tags 替代硬编码判断。 |
| **Phase 2<br>适配拓展** | 1 天 | `core/config/config.go`<br>`desktop/app.go` | 在应用启动时异步触发 Registry Fetch；扩展 `ProviderTemplate` 结构。 |
| **Phase 3<br>GUI 对接** | 1 天 | `SettingsPanel.tsx`<br>`desktop/settings_app.go` | 添加“手动更新注册表”的 API 与按钮 UI；显示最近同步时间戳。 |
| **Phase 4<br>测试与文档** | 0.5 天 | 单元测试 & 文档 | 验证断网情况下的内置快照兜底功能；测试 JSON 反序列化容错。 |

> 注：原方案中的所有 CLI 命令（P1 级）暂缓开发，转入 Backlog，优先保障核心桌面用户的图形交互体验。
