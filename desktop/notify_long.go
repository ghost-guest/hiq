package main

// notify_long.go sends long-duration (25s) Windows toast notifications for
// scheduled-task reminders, instead of the ~7s default that Wails'
// runtime.SendNotification uses. The user asked for reminders that stay on
// screen long enough to actually read (and persist to Action Center), not a
// 7-second flash that's easy to miss.
//
// We bypass Wails' SendNotification (which hardcodes Duration=Short and
// exposes no override) and call go-toast directly. This reuses the AUMID/COM
// registration that Wails' InitializeNotifications already performed at startup
// (it calls toast.SetAppData with AppID = exe base name), so we must match that
// same AppID here. On non-Windows this is a no-op (go-toast only renders on
// Windows).

import (
	"os"
	"path/filepath"

	toast "git.sr.ht/~jackmordaunt/go-toast/v2"
)

// notifyLongDurationToast fires a 25-second ("long") toast with an explicit
// "知道了" dismiss button. It is best-effort: errors are swallowed by the caller.
// AppID matches Wails' InitializeNotifications (filepath.Base of the executable)
// so it shares the same registry/COM registration and the toast attributes
// correctly to hiq.
//
// The dismiss Action uses Foreground activation so clicking it both closes the
// toast AND routes through Wails' OnNotificationResponse (registered in startup
// → showMainWindow), bringing the window forward. Windows always shows a hover-X
// too, but the labeled button is far more discoverable for "I've seen it, close".
func notifyLongDurationToast(title, body string) {
	appID := appExeBaseName()
	n := toast.Notification{
		AppID:               appID,
		Title:               title,
		Body:                body,
		Duration:            toast.Long, // ~25s on screen (vs ~7s Short), then Action Center
		ActivationType:      toast.Foreground,
		ActivationArguments: "default",
		Actions: []toast.Action{
			{
				Type:      toast.Foreground,
				Content:   "知道了",
				Arguments: "dismiss",
			},
		},
	}
	_ = n.Push()
}

// appExeBaseName returns the running executable's base name (e.g. "hiq.exe"),
// matching what Wails' InitializeNotifications uses as the toast AppID. Falls
// back to "hiq.exe" if the path can't be resolved (keeps the toast working
// rather than failing with an empty AppID, which Windows would suppress).
func appExeBaseName() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Base(exe)
	}
	return "hiq.exe"
}
