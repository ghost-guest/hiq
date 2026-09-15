// Command fairpeer-desktop is the Wails shell around the fairpeer kernel: a native
// window hosting a webview frontend, with the Go-side control.Controller bound
// directly to the UI (no HTTP hop — bindings in, runtime events out). It lives in
// a nested module (fairpeer/desktop) so the CGO/WebKit desktop build never touches
// the CLI's CGO_ENABLED=0 single-static-binary guarantee, while still importing
// the same internal/* kernel.
package main

import (
	"embed"
	"os"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	// Blank imports wire compile-time built-ins into their registries, exactly as
	// cmd/fairpeer does — boot.Build resolves providers/tools from these registries.
	_ "github.com/zzycxz/fairpeer/internal/provider/anthropic"
	_ "github.com/zzycxz/fairpeer/internal/provider/openai"
	"github.com/zzycxz/fairpeer/internal/sandbox"
	_ "github.com/zzycxz/fairpeer/internal/tool/builtin"
)

// assets embeds the built frontend. `all:` so dotfiles (e.g. the dist .gitkeep
// that keeps this directive compilable before the first `pnpm build`) are
// included. A real run requires `pnpm build` (or `wails build`) to populate dist.
//
//go:embed all:frontend/dist
var assets embed.FS

// version is injected at build time via `wails build -ldflags "-X main.version=..."`,
// mirroring cmd/fairpeer/main.go. The auto-updater reads it (App.Version) to compare
// against the published manifest; an un-injected dev build stays "dev" and never
// prompts to update.
var version = "dev"

// channel selects which updater pointer this build polls, injected via
// `-X main.channel=canary`. Default "stable" tracks the public release; "canary"
// tracks the opt-in pre-release line and never crosses over to stable.
var channel = "stable"

const disableWebview2GPUEnv = "FAIRPEER_DESKTOP_DISABLE_WEBVIEW2_GPU"

func windowsWebview2GPUDisabled() bool {
	if raw, ok := os.LookupEnv(disableWebview2GPUEnv); ok {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off", "":
			return false
		}
	}
	return channel == "canary"
}

// runWindowsSandboxHelperIfRequested handles the private re-entry used by the
// native Windows command sandbox, reporting whether this process was that
// helper (in which case the caller must exit with the returned code).
func runWindowsSandboxHelperIfRequested(argv []string) (int, bool) {
	if len(argv) > 1 && argv[1] == sandbox.WindowsHelperCommand {
		return sandbox.RunWindowsSandboxHelper(argv[2:], os.Stdin, os.Stdout, os.Stderr), true
	}
	return 0, false
}

func main() {
	// Windows command-sandbox helper dispatch. An enforced bash launch re-enters
	// this same executable with a private argv marker; dispatch before any
	// application initialization so an enforced launch can never fall through
	// into an unconfined GUI process.
	if code, ok := runWindowsSandboxHelperIfRequested(os.Args); ok {
		os.Exit(code)
	}
	sandbox.RegisterHelperDispatch()
	app := NewApp()

	// Restore saved window size, or fall back to the default.
	width, height := 1240, 720
	if saved, ok := loadWindowState(); ok {
		if saved.Width > 0 {
			width = saved.Width
		}
		if saved.Height > 0 {
			height = saved.Height
		}
	}

	// fairpeer:// 深链冷启动（应用未开、协议拉起本进程）：扫 argv 暂存路由，
	// 前端 boot 后经 NetDevConsumeDeepLink 取走。协议注册随启动（幂等）。
	registerFairpeerProtocol()
	if raw := deepLinkArg(os.Args); raw != "" {
		stashPendingDeepLink(raw)
	}

	err := wails.Run(&options.App{
		Title:     "fairpeer",
		Width:     width,
		Height:    height,
		MinWidth:  760,
		MinHeight: 480,
		Frameless: true,
		// Match the light UI shell so the initial webview background doesn't flash
		// dark before CSS loads — particularly visible on WebKitGTK.
		BackgroundColour:   &options.RGBA{R: 244, G: 243, B: 239, A: 255},
		AssetServer:        &assetserver.Options{Assets: assets, Middleware: app.workspaceMediaMiddleware()},
		OnStartup:          app.startup,
		OnDomReady:         app.domReady,
		OnBeforeClose:      app.beforeClose,
		OnShutdown:         app.shutdown,
		Bind:               []any{app},
		SingleInstanceLock: singleInstanceLock(app),

		// Start hidden — domReady positions and shows the window after restoring
		// geometry, so the user never sees the default size/position flash.
		StartHidden: true,

		// Native application menu (File > Settings, Edit, Window).
		Menu: app.createAppMenu(),

		// Native OS file drops: the webview withholds dropped files' paths from the
		// HTML drop event, so the frontend (composer) reads them via runtime.OnFileDrop
		// against the --wails-drop-target element instead.
		DragAndDrop: &options.DragAndDrop{EnableFileDrop: true},

		// Browser-native context menu in production (2026-08-19): without this,
		// Wails disables right-click entirely in packaged builds — users lost
		// copy/paste/select-all in every input and message text. Custom menus
		// (tree/sessions/dock) call preventDefault and keep overriding it.
		EnableDefaultContextMenu: true,

		// --- per-platform adaptation (see desktop/README.md for the rationale) ---
		Mac: &mac.Options{
			// Inset traffic-lights over a frameless-feeling header; the frontend
			// leaves a drag region at the top (CSS --wails-draggable).
			TitleBar: mac.TitleBarHiddenInset(),
			// Follow the OS appearance so the title bar matches light/dark system
			// preference instead of being locked to dark.
			Appearance: mac.DefaultAppearance,
		},
		Windows: &windows.Options{
			// Follow the OS theme so the title bar matches light/dark system
			// preference instead of being locked to dark.
			Theme:                windows.SystemDefault,
			WebviewGpuIsDisabled: windowsWebview2GPUDisabled(),
		},
		Linux: &linux.Options{
			ProgramName: "fairpeer",
			// WebKitGTK GPU compositing is inconsistent across distros/drivers and
			// is the one real cross-platform rough edge for a Go+webview stack:
			// "always" can yield blank or flickering webviews on some setups, so
			// we let the webview decide on demand. Users still hitting artifacts
			// can fall back to WEBKIT_DISABLE_COMPOSITING_MODE=1 (see README).
			WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand,
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
