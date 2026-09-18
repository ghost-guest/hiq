//go:build !windows

package main

// deeplink_other.go — 非 Windows 的协议注册占位（打包版的 plist/protocol
// 声明随各平台打包方案落地；当前只在 Windows 实现，其余平台静默跳过）。
func registerHiqProtocol() {}
