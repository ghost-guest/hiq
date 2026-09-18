# Hiq 跨平台兼容性规格说明书

> **版本**: v1.0 | **日期**: 2026-08-05 | **状态**: 待评审
>
> **目标**: 实现 Hiq 所有功能在 Windows/macOS/Linux 三个平台的完整可用性

---

## 一、背景与动机

### 1.1 现状分析

Hiq 当前的核心功能（代码编辑、Office 自动化、记忆系统、任务编排）在所有平台都可用，但系统集成层存在严重的平台依赖问题：

| 功能类别 | Windows | macOS | Linux | 问题严重程度 |
|----------|---------|-------|-------|-------------|
| **核心功能** | ✅ 完整 | ✅ 完整 | ✅ 完整 | 无 |
| **桌面自动化** | ✅ 完整 | ❌ 不可用 | ❌ 不可用 | 严重 |
| **热键系统** | ✅ 可靠 | ⚠️ 需授权 | ⚠️ 误触发 | 中等 |
| **系统托盘** | ✅ 正常 | ❌ 禁用 | ⚠️ 需 KDE | 中等 |
| **进程管理** | ⚠️ 不完整 | ✅ 正常 | ✅ 正常 | 低 |
| **沙箱** | ❌ 无 | ✅ Seatbelt | ⚠️ bubblewrap | 中等 |

### 1.2 问题根因

1. **过度依赖 Windows API** — 桌面自动化完全使用 Win32 API（UIA、SendInput、BitBlt）
2. **缺乏抽象层** — 平台特定代码直接暴露给上层，无法复用
3. **测试覆盖不足** — 跨平台测试主要依赖手动验证
4. **文档缺失** — 未明确说明各平台的功能限制

### 1.3 设计目标

1. **功能完整性** — 所有功能在所有平台可用
2. **原生体验** — 每个平台使用原生 API，不依赖模拟层
3. **优雅降级** — 不可用时提供替代方案或明确提示
4. **最小依赖** — 减少外部依赖，保持单一二进制分发

---

## 二、架构设计

### 2.1 分层架构

```
┌─────────────────────────────────────────────────────────────────┐
│                        用户层                                    │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐          │
│  │ CLI / TUI    │  │ Desktop App  │  │ HTTP/SSE     │          │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘          │
├─────────┼─────────────────┼─────────────────┼───────────────────┤
│         │                 │                 │                   │
│         ▼                 ▼                 ▼                   │
│  ┌─────────────────────────────────────────────────────────────┐│
│  │                    公共接口层                                ││
│  │  ScreenAPI  │  InputAPI  │  WindowAPI  │  TrayAPI  │ ...    ││
│  └─────────────────────────┬───────────────────────────────────┘│
├─────────────────────────────┼───────────────────────────────────┤
│                             │                                   │
│  ┌──────────────────────────▼──────────────────────────────────┐│
│  │                    平台适配层                                ││
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐         ││
│  │  │ Windows     │  │ macOS       │  │ Linux       │         ││
│  │  │ Win32 API   │  │ AppleScript │  │ xdotool     │         ││
│  │  │ UIA         │  │ Accessibility│ │ xdg-utils   │         ││
│  │  └─────────────┘  └─────────────┘  └─────────────┘         ││
│  └─────────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────────┘
```

### 2.2 接口定义

#### 2.2.1 屏幕操作接口

```go
// internal/platform/screen.go

package platform

import "image"

// ScreenAPI 屏幕操作接口
type ScreenAPI interface {
    // CaptureFullScreen 截取全屏
    CaptureFullScreen() (image.Image, error)
    
    // CaptureRegion 截取指定区域
    CaptureRegion(x, y, width, height int) (image.Image, error)
    
    // FindElement 查找 UI 元素
    FindElement(query string) (Element, error)
    
    // GetUIATree 获取 UI 元素树（仅 Windows）
    GetUIATree() (UITree, error)
}

// Element UI 元素
type Element struct {
    ID       string
    Name     string
    Type     string
    Bounds   image.Rectangle
    Value    string
    Children []Element
}

// UITree UI 元素树
type UITree struct {
    Root     Element
    Metadata map[string]interface{}
}
```

#### 2.2.2 输入操作接口

```go
// internal/platform/input.go

package platform

// InputAPI 输入操作接口
type InputAPI interface {
    // Click 点击指定坐标
    Click(x, y int, button MouseButton) error
    
    // DoubleClick 双击指定坐标
    DoubleClick(x, y int) error
    
    // TypeText 输入文本
    TypeText(text string) error
    
    // PressKey 按下按键
    PressKey(keys ...string) error
    
    // Scroll 滚动
    Scroll(x, y int, direction ScrollDirection, amount int) error
    
    // Drag 拖拽
    Drag(x1, y1, x2, y2 int) error
}

// MouseButton 鼠标按钮
type MouseButton string

const (
    MouseButtonLeft   MouseButton = "left"
    MouseButtonRight  MouseButton = "right"
    MouseButtonMiddle MouseButton = "middle"
)

// ScrollDirection 滚动方向
type ScrollDirection string

const (
    ScrollUp   ScrollDirection = "up"
    ScrollDown ScrollDirection = "down"
)
```

#### 2.2.3 窗口管理接口

```go
// internal/platform/window.go

package platform

// WindowAPI 窗口管理接口
type WindowAPI interface {
    // ListWindows 列出所有窗口
    ListWindows() ([]WindowInfo, error)
    
    // Focus 聚焦窗口
    Focus(windowID string) error
    
    // Maximize 最大化窗口
    Maximize(windowID string) error
    
    // Minimize 最小化窗口
    Minimize(windowID string) error
    
    // Restore 恢复窗口
    Restore(windowID string) error
    
    // Close 关闭窗口
    Close(windowID string) error
    
    // Move 移动窗口
    Move(windowID string, x, y, width, height int) error
}

// WindowInfo 窗口信息
type WindowInfo struct {
    ID       string
    Title    string
    PID      int
    Bounds   image.Rectangle
    IsMinimized bool
    IsMaximized bool
}
```

#### 2.2.4 热键管理接口

```go
// internal/platform/hotkey.go

package platform

// HotkeyAPI 热键管理接口
type HotkeyAPI interface {
    // Register 注册全局热键
    Register(hotkey Hotkey, handler func()) error
    
    // Unregister 注销热键
    Unregister(hotkey Hotkey) error
    
    // IsRegistered 检查热键是否已注册
    IsRegistered(hotkey Hotkey) bool
}

// Hotkey 热键定义
type Hotkey struct {
    Modifiers []string // ctrl, alt, shift, cmd/meta
    Key       string   // w, p, f1, etc.
}
```

#### 2.2.5 系统托盘接口

```go
// internal/platform/tray.go

package platform

// TrayAPI 系统托盘接口
type TrayAPI interface {
    // Create 创建托盘图标
    Create(icon []byte, tooltip string) error
    
    // AddMenuItem 添加菜单项
    AddMenuItem(label string, handler func()) error
    
    // AddSeparator 添加分隔线
    AddSeparator()
    
    // UpdateTooltip 更新提示文本
    UpdateTooltip(tooltip string) error
    
    // UpdateIcon 更新图标
    UpdateIcon(icon []byte) error
    
    // Destroy 销毁托盘
    Destroy() error
}
```

#### 2.2.6 浏览器检测接口

```go
// internal/platform/browser.go

package platform

// BrowserAPI 浏览器检测接口
type BrowserAPI interface {
    // Detect 检测浏览器路径
    Detect(name string) (string, error)
    
    // ListAvailable 列出可用浏览器
    ListAvailable() ([]BrowserInfo, error)
}

// BrowserInfo 浏览器信息
type BrowserInfo struct {
    Name    string // chrome, edge, firefox, etc.
    Path    string
    Version string
}
```

#### 2.2.7 进程管理接口

```go
// internal/platform/process.go

package platform

// ProcessAPI 进程管理接口
type ProcessAPI interface {
    // KillProcessTree 杀死进程树
    KillProcessTree(pid int) error
    
    // GetProcessTree 获取进程树
    GetProcessTree(pid int) ([]ProcessInfo, error)
    
    // SetPriority 设置进程优先级
    SetPriority(pid int, priority ProcessPriority) error
}

// ProcessInfo 进程信息
type ProcessInfo struct {
    PID     int
    PPID    int
    Name    string
    Command string
}

// ProcessPriority 进程优先级
type ProcessPriority string

const (
    PriorityLow    ProcessPriority = "low"
    PriorityNormal ProcessPriority = "normal"
    PriorityHigh   ProcessPriority = "high"
)
```

---

## 三、平台实现

### 3.1 Windows 实现

#### 3.1.1 屏幕操作

```go
// internal/platform/windows/screen.go

package windows

import (
    "image"
    "syscall"
    "unsafe"
)

var (
    user32  = syscall.NewLazyDLL("user32.dll")
    gdi32   = syscall.NewLazyDLL("gdi32.dll")
    
    procGetDesktopWindow          = user32.NewProc("GetDesktopWindow")
    procGetWindowDC               = user32.NewProc("GetWindowDC")
    procCreateCompatibleDC        = gdi32.NewProc("CreateCompatibleDC")
    procCreateCompatibleBitmap    = gdi32.NewProc("CreateCompatibleBitmap")
    procBitBlt                    = gdi32.NewProc("BitBlt")
    procGetDIBits                 = gdi32.NewProc("GetDIBits")
    procDeleteDC                  = gdi32.NewProc("DeleteDC")
    procDeleteObject              = gdi32.NewProc("DeleteObject")
)

type WindowsScreenAPI struct{}

func (s *WindowsScreenAPI) CaptureFullScreen() (image.Image, error) {
    // 使用 Win32 BitBlt API 截取全屏
    // ...
}

func (s *WindowsScreenAPI) CaptureRegion(x, y, width, height int) (image.Image, error) {
    // 使用 Win32 BitBlt API 截取指定区域
    // ...
}

func (s *WindowsScreenAPI) FindElement(query string) (Element, error) {
    // 使用 UIA API 查找元素
    // ...
}

func (s *WindowsScreenAPI) GetUIATree() (UITree, error) {
    // 使用 UIA API 获取元素树
    // ...
}
```

#### 3.1.2 输入操作

```go
// internal/platform/windows/input.go

package windows

import (
    "syscall"
    "unsafe"
)

var (
    procSendInput = user32.NewProc("SendInput")
)

type WindowsInputAPI struct{}

func (i *WindowsInputAPI) Click(x, y int, button MouseButton) error {
    // 使用 Win32 SendInput API 模拟点击
    // ...
}

func (i *WindowsInputAPI) TypeText(text string) error {
    // 使用 Win32 SendInput API 模拟键盘输入
    // ...
}

func (i *WindowsInputAPI) PressKey(keys ...string) error {
    // 使用 Win32 SendInput API 模拟按键
    // ...
}
```

#### 3.1.3 热键管理

```go
// internal/platform/windows/hotkey.go

package windows

import (
    "syscall"
    "unsafe"
)

var (
    procRegisterHotKey   = user32.NewProc("RegisterHotKey")
    procUnregisterHotKey = user32.NewProc("UnregisterHotKey")
)

type WindowsHotkeyAPI struct {
    registered map[Hotkey]int
    handlers   map[int]func()
    nextID     int
}

func (h *WindowsHotkeyAPI) Register(hotkey Hotkey, handler func()) error {
    // 使用 Win32 RegisterHotKey 注册全局热键
    // ...
}

func (h *WindowsHotkeyAPI) Unregister(hotkey Hotkey) error {
    // 使用 Win32 UnregisterHotKey 注销热键
    // ...
}
```

### 3.2 macOS 实现

#### 3.2.1 屏幕操作

```go
// internal/platform/darwin/screen.go

package darwin

import (
    "image"
    "os/exec"
)

type DarwinScreenAPI struct{}

func (s *DarwinScreenAPI) CaptureFullScreen() (image.Image, error) {
    // 使用 screencapture 命令截取全屏
    cmd := exec.Command("screencapture", "-x", "/tmp/screenshot.png")
    // ...
}

func (s *DarwinScreenAPI) CaptureRegion(x, y, width, height int) (image.Image, error) {
    // 使用 screencapture 命令截取指定区域
    cmd := exec.Command("screencapture", "-x", "-R", 
        fmt.Sprintf("%d,%d,%d,%d", x, y, width, height), "/tmp/screenshot.png")
    // ...
}

func (s *DarwinScreenAPI) FindElement(query string) (Element, error) {
    // 使用 AppleScript 查找元素
    // ...
}
```

#### 3.2.2 输入操作

```go
// internal/platform/darwin/input.go

package darwin

import (
    "os/exec"
)

type DarwinInputAPI struct{}

func (i *DarwinInputAPI) Click(x, y int, button MouseButton) error {
    // 使用 AppleScript 模拟点击
    script := fmt.Sprintf(`
        tell application "System Events"
            click at {%d, %d}
        end tell
    `, x, y)
    cmd := exec.Command("osascript", "-e", script)
    // ...
}

func (i *DarwinInputAPI) TypeText(text string) error {
    // 使用 AppleScript 模拟键盘输入
    script := fmt.Sprintf(`
        tell application "System Events"
            keystroke "%s"
        end tell
    `, text)
    cmd := exec.Command("osascript", "-e", script)
    // ...
}
```

#### 3.2.3 热键管理

```go
// internal/platform/darwin/hotkey.go

package darwin

import (
    "os/exec"
)

type DarwinHotkeyAPI struct {
    registered map[Hotkey]bool
}

func (h *DarwinHotkeyAPI) Register(hotkey Hotkey, handler func()) error {
    // 使用 Carbon EventHotKeyRegister 注册热键
    // 需要 Accessibility 权限
    // ...
}
```

### 3.3 Linux 实现

#### 3.3.1 屏幕操作

```go
// internal/platform/linux/screen.go

package linux

import (
    "image"
    "os/exec"
)

type LinuxScreenAPI struct{}

func (s *LinuxScreenAPI) CaptureFullScreen() (image.Image, error) {
    // 使用 xwd 或 import 命令截取全屏
    cmd := exec.Command("import", "-window", "root", "/tmp/screenshot.png")
    // ...
}

func (s *LinuxScreenAPI) CaptureRegion(x, y, width, height int) (image.Image, error) {
    // 使用 import 命令截取指定区域
    cmd := exec.Command("import", "-window", "root",
        "-crop", fmt.Sprintf("%dx%d+%d+%d", width, height, x, y),
        "/tmp/screenshot.png")
    // ...
}

func (s *LinuxScreenAPI) FindElement(query string) (Element, error) {
    // 使用 xdotool 或 AT-SPI 查找元素
    // ...
}
```

#### 3.3.2 输入操作

```go
// internal/platform/linux/input.go

package linux

import (
    "os/exec"
)

type LinuxInputAPI struct{}

func (i *LinuxInputAPI) Click(x, y int, button MouseButton) error {
    // 使用 xdotool 模拟点击
    cmd := exec.Command("xdotool", "mousemove", fmt.Sprintf("%d", x), 
        fmt.Sprintf("%d", y), "click", "1")
    // ...
}

func (i *LinuxInputAPI) TypeText(text string) error {
    // 使用 xdotool 模拟键盘输入
    cmd := exec.Command("xdotool", "type", text)
    // ...
}

func (i *LinuxInputAPI) PressKey(keys ...string) error {
    // 使用 xdotool 模拟按键
    args := append([]string{"key"}, keys...)
    cmd := exec.Command("xdotool", args...)
    // ...
}
```

#### 3.3.3 热键管理

```go
// internal/platform/linux/hotkey.go

package linux

import (
    "os/exec"
)

type LinuxHotkeyAPI struct {
    registered map[Hotkey]bool
}

func (h *LinuxHotkeyAPI) Register(hotkey Hotkey, handler func()) error {
    // 使用 XGrabKey 注册热键
    // 或使用 xdotool 监听按键
    // ...
}
```

---

## 四、配置管理

### 4.1 平台检测

```go
// internal/platform/detect.go

package platform

import "runtime"

// CurrentPlatform 返回当前平台
func CurrentPlatform() Platform {
    switch runtime.GOOS {
    case "windows":
        return PlatformWindows
    case "darwin":
        return PlatformMacOS
    case "linux":
        return PlatformLinux
    default:
        return PlatformUnknown
    }
}

// Platform 平台类型
type Platform string

const (
    PlatformWindows Platform = "windows"
    PlatformMacOS   Platform = "darwin"
    PlatformLinux   Platform = "linux"
    PlatformUnknown Platform = "unknown"
)
```

### 4.2 平台适配器注册

```go
// internal/platform/registry.go

package platform

import "sync"

// Registry 平台适配器注册表
type Registry struct {
    mu       sync.RWMutex
    adapters map[Platform]Adapter
}

// Adapter 平台适配器
type Adapter struct {
    Screen  ScreenAPI
    Input   InputAPI
    Window  WindowAPI
    Hotkey  HotkeyAPI
    Tray    TrayAPI
    Browser BrowserAPI
    Process ProcessAPI
}

// GetAdapter 获取当前平台的适配器
func (r *Registry) GetAdapter() (Adapter, error) {
    platform := CurrentPlatform()
    r.mu.RLock()
    defer r.mu.RUnlock()
    
    adapter, ok := r.adapters[platform]
    if !ok {
        return Adapter{}, fmt.Errorf("unsupported platform: %s", platform)
    }
    return adapter, nil
}
```

---

## 五、配置文件

### 5.1 平台特定配置

```toml
# hiq.toml

[platform]
# 平台特定配置
[platform.windows]
# Windows 特定设置
sandbox = false  # Windows 暂不支持沙箱
tray_enabled = true

[platform.macos]
# macOS 特定设置
sandbox = true  # 使用 Seatbelt
tray_enabled = false  # 暂不支持
accessibility_prompt = true  # 提示用户授权

[platform.linux]
# Linux 特定设置
sandbox = true  # 使用 bubblewrap
tray_enabled = true
tray_fallback = "notification"  # 托盘不可用时使用通知
```

---

## 六、实施计划

### 6.1 Phase 1: 核心跨平台（2 周）

| 任务 | 工作量 | 优先级 | 说明 |
|------|--------|--------|------|
| 公共接口定义 | 1 天 | P0 | 定义所有平台 API 接口 |
| Windows 适配器 | 2 天 | P0 | 实现 Windows 平台适配器 |
| macOS 适配器 | 2 天 | P0 | 实现 macOS 平台适配器 |
| Linux 适配器 | 2 天 | P0 | 实现 Linux 平台适配器 |
| 热键系统统一 | 2 天 | P0 | 统一热键注册 API |
| 集成测试 | 2 天 | P0 | 跨平台功能验证 |

### 6.2 Phase 2: 系统集成（1 周）

| 任务 | 工作量 | 优先级 | 说明 |
|------|--------|--------|------|
| 跨平台托盘 | 2 天 | P1 | 使用 systray 库 |
| 浏览器检测 | 1 天 | P1 | 平台特定路径 |
| 进程管理统一 | 1 天 | P1 | 统一进程树管理 |
| Python 脚本跨平台 | 1 天 | P1 | 替换平台特定脚本 |

### 6.3 Phase 3: 安全和优化（1 周）

| 任务 | 工作量 | 优先级 | 说明 |
|------|--------|--------|------|
| Windows 沙箱 | 2 天 | P2 | AppContainer 实现 |
| macOS 权限引导 | 1 天 | P2 | Accessibility 权限引导 |
| Linux 兼容性 | 1 天 | P2 | 桌面环境适配 |
| 文档更新 | 1 天 | P2 | 跨平台使用指南 |

**总计：4 周**

---

## 七、验收标准

### 7.1 功能验收

| 功能 | Windows | macOS | Linux |
|------|---------|-------|-------|
| **屏幕截图** | ✅ | ✅ | ✅ |
| **鼠标点击** | ✅ | ✅ | ✅ |
| **键盘输入** | ✅ | ✅ | ✅ |
| **窗口管理** | ✅ | ✅ | ✅ |
| **热键注册** | ✅ | ✅ | ✅ |
| **紧急停止** | ✅ | ✅ | ✅ |
| **系统托盘** | ✅ | ✅ | ✅ |
| **浏览器检测** | ✅ | ✅ | ✅ |
| **自动更新** | ✅ | ✅ | ✅ |
| **沙箱** | ✅ | ✅ | ✅ |

### 7.2 性能验收

| 指标 | Windows | macOS | Linux |
|------|---------|-------|-------|
| **截图延迟** | < 100ms | < 200ms | < 200ms |
| **热键响应** | < 50ms | < 100ms | < 100ms |
| **托盘启动** | < 1s | < 1s | < 1s |

### 7.3 兼容性验收

| 平台 | 版本要求 | 测试环境 |
|------|----------|----------|
| **Windows** | Windows 10+ | Windows 10, Windows 11 |
| **macOS** | macOS 12+ | macOS 12, macOS 13, macOS 14 |
| **Linux** | Ubuntu 22.04+ | Ubuntu 22.04, Ubuntu 24.04, Fedora 38 |

---

## 八、风险与应对

| 风险 | 影响 | 应对措施 |
|------|------|----------|
| **macOS 权限限制** | 热键/自动化需授权 | 添加权限引导，提供替代方案 |
| **Linux 桌面多样性** | 托盘/热键兼容性 | 检测桌面环境，适配主流 DE |
| **Windows 沙箱复杂度** | 实现难度高 | 先实现基础功能，逐步完善 |
| **测试覆盖** | 跨平台测试困难 | 使用 CI/CD 多平台构建 |
| **依赖管理** | 平台特定依赖 | 最小化依赖，使用标准库 |

---

## 九、后续演进

### 9.1 短期（v1.1）

- **移动端支持** — Android/iOS 基础功能
- **远程桌面** — 跨设备协作
- **云同步** — 配置和记忆同步

### 9.2 中期（v1.2）

- **容器化** — Docker 支持
- **WebAssembly** — 浏览器端运行
- **嵌入式** — IoT 设备支持

### 9.3 长期（v2.0）

- **统一体验** — 所有平台功能完全一致
- **原生性能** — 每个平台使用最优实现
- **零配置** — 自动检测和适配

---

**Hiq 跨平台兼容性规格说明书 — 让所有功能在所有平台完整可用！**
