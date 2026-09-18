package mobilebridge

import (
	"log/slog"
)

// Audit logs ONLY metadata about mobile-side activity: devIds (truncated),
// command types, connection lifecycle, errors. It NEVER logs command inputs,
// conversation text, file contents, or any business data — that would defeat
// the privacy promise. Retention is local (HIQ_SPEC §11.2④).
type Audit struct{ log *slog.Logger }

func NewAudit(level string) *Audit {
	// 用 hiq 全局 slog（startup 时 route 到 app.log）。原来用 os.Stdout，
	// 但 wails GUI 应用没有控制台，stdout 不可见，dc_open/cmd/conn_open 等
	// 关键日志全丢了。level 跟随全局配置（[mobilebridge] log_level=debug 时
	// 整个 app.log 已是 debug 级）。
	_ = level
	return &Audit{log: slog.Default()}
}

// truncDev shortens a devId so logs can't reconstruct the full identifier.
func truncDev(dev string) string {
	if len(dev) <= 7 {
		return dev
	}
	return dev[:4] + "..." + dev[len(dev)-3:]
}

func (a *Audit) PairStart(devS string)                    { a.log.Info("pair_start", "devS", truncDev(devS)) }
func (a *Audit) PairConfirmed(devC, devS string)          { a.log.Info("pair_confirmed", "devC", truncDev(devC), "devS", truncDev(devS)) }
func (a *Audit) Unpaired(dev string)                      { a.log.Info("unpaired", "dev", truncDev(dev)) }
func (a *Audit) ConnOpen(dev, iceMode string)             { a.log.Info("conn_open", "dev", truncDev(dev), "ice", iceMode) }
func (a *Audit) ConnClose(dev string)                     { a.log.Info("conn_close", "dev", truncDev(dev)) }
func (a *Audit) Cmd(dev, cmd, tab string, ok bool)        { a.log.Info("cmd", "dev", truncDev(dev), "cmd", cmd, "tab", tab, "ok", ok) }
func (a *Audit) Denied(dev, cmd, reason string)           { a.log.Warn("denied", "dev", truncDev(dev), "cmd", cmd, "reason", reason) }
func (a *Audit) Error(evt, dev string, err error)         { a.log.Error("error", "evt", evt, "dev", truncDev(dev), "err", err.Error()) }

// Info 是通用日志（联调诊断用：信令消息、WebRTC 状态等）。生产日志优先用上面
// 的具体方法（语义清晰）；联调临时诊断用 Info 灵活传 key/value。
func (a *Audit) Info(evt string, args ...any) { a.log.Info(evt, args...) }
