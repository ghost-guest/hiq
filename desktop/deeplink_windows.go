//go:build windows

package main

import (
	"log/slog"
	"os"

	"golang.org/x/sys/windows/registry"
)

// deeplink_windows.go — HKCU 协议注册（用户级，无需管理员）。best-effort：
// 失败只记日志，不影响启动。dev 构建跳过（HIQ_DEV=1 与单实例旁路同
// 开关——防 dev exe 抢注安装版的协议）。
func registerHiqProtocol() {
	if os.Getenv("HIQ_DEV") != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	root, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\hiq`, registry.SET_VALUE|registry.CREATE_SUB_KEY)
	if err != nil {
		slog.Warn("deeplink: protocol register failed", "err", err)
		return
	}
	defer root.Close()
	// "URL Protocol" 空串是 Windows 识别 URL scheme 类的标记。
	if err := root.SetStringValue("URL Protocol", ""); err != nil {
		slog.Warn("deeplink: protocol register failed", "err", err)
		return
	}
	cmdKey, _, err := registry.CreateKey(root, `shell\open\command`, registry.SET_VALUE)
	if err != nil {
		slog.Warn("deeplink: protocol register failed", "err", err)
		return
	}
	defer cmdKey.Close()
	if err := cmdKey.SetStringValue("", `"`+exe+`" --deep-link "%1"`); err != nil {
		slog.Warn("deeplink: protocol register failed", "err", err)
		return
	}
	iconKey, _, err := registry.CreateKey(root, `DefaultIcon`, registry.SET_VALUE)
	if err == nil {
		defer iconKey.Close()
		_ = iconKey.SetStringValue("", `"`+exe+`",0`)
	}
	slog.Info("deeplink: hiq:// protocol registered (HKCU)")
}
