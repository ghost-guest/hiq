package main

import (
	"os"

	"github.com/wailsapp/wails/v2/pkg/options"
)

func singleInstanceLock(app *App) *options.SingleInstanceLock {
	// Allow contributors to run a dev build alongside the installed app.
	// Set HIQ_DEV=1 to skip the single-instance lock.
	if os.Getenv("HIQ_DEV") != "" {
		return nil
	}
	return &options.SingleInstanceLock{
		UniqueId: singleInstanceID,
		OnSecondInstanceLaunch: func(data options.SecondInstanceData) {
			// hiq:// 热路径：IM/邮件里点链接唤起的第二实例——解析
			// args 里的 URL，推给前端落对应屏，再唤窗到前。
			if raw := deepLinkArg(data.Args); raw != "" {
				app.emitDeepLink(raw)
			}
			app.secondInstanceLaunch()
		},
	}
}
