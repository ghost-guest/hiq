package boot

// browserauto_runtime.go builds the browser_auto runtime injected into the
// builtin tool. It wires together:
//
//   - internal/browserlaunch (launch a system Chrome/Edge with a CDP endpoint)
//   - a desktop-registered screencast sink (so the in-app panel mirrors the
//     agent's browser), optional
//   - the browser-use sidecar client (desktop-owned, resolved lazily so the
//     runtime can be built before the sidecar process is up)
//
// The runtime is rebuilt on each boot.Build (per profile switch) because it
// captures the current cfg (model/proxy/limits). The sidecar client and panel
// sink are process-global, set by the desktop, so they survive rebuilds.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/browserlaunch"
	"github.com/zzycxz/hiq/internal/browseruse"
	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/tool/builtin"
)

// --- desktop-registered process-global hooks --------------------------------

// browserUseClientProvider returns the live sidecar client (or nil if the
// sidecar isn't running). Set by the desktop once the service is up, so the
// runtime (built at boot.Build time, before the sidecar exists) can resolve
// the client lazily at tool-execution time.
var browserUseClientProvider func() *browseruse.Client

// SetBrowserUseClientProvider injects the sidecar-client resolver. Called by
// the desktop app once its BrowserUseService is created.
func SetBrowserUseClientProvider(fn func() *browseruse.Client) {
	browserUseClientProvider = fn
}

// buildBrowserAutoRuntime constructs the runtime closure the browser_auto tool
// calls. It resolves the sidecar client + browser-launch at call time (not
// build time) so a runtime that was built before the sidecar came up still
// works once the sidecar is ready.
func buildBrowserAutoRuntime(cfg *config.Config, opts Options) builtin.BrowserAutoRuntime {
	return builtin.BrowserAutoRuntime{
		Available: func() (bool, string) {
			if browserUseClientProvider == nil {
				return false, "autonomous-browsing sidecar is not wired up (desktop build only)"
			}
			if browserUseClientProvider() == nil {
				return false, "autonomous-browsing sidecar is not running (check that browser-use + a provider client are installed)"
			}
			return true, ""
		},
		Run: func(ctx context.Context, req builtin.BrowserAutoRunRequest) ([]builtin.BrowserAutoStep, string, error) {
			return runBrowserAuto(ctx, cfg, req)
		},
	}
}

// runBrowserAuto performs the full autonomous-browse sequence:
//  1. Launch a system browser with a CDP endpoint.
//  2. (Optional) attach it to the in-app panel via the desktop sink.
//  3. POST /run to the sidecar with the goal + cdp_url + model config.
//  4. Stream the SSE events into builtin.BrowserAutoStep records.
//  5. Close the browser (and detach the panel) on completion/cancel/error.
func runBrowserAuto(ctx context.Context, cfg *config.Config, req builtin.BrowserAutoRunRequest) ([]builtin.BrowserAutoStep, string, error) {
	if browserUseClientProvider == nil {
		return nil, "", fmt.Errorf("autonomous-browsing sidecar is not wired up")
	}
	client := browserUseClientProvider()
	if client == nil {
		return nil, "", fmt.Errorf("autonomous-browsing sidecar is not running")
	}

	// Resolve the LLM provider entry so we can hand the sidecar the bare model
	// name + base_url + api-key-env it needs. hiq uses "provider/model"
	// refs (and may point at a custom gateway or proxy), but browser-use's
	// ChatOpenAI/ChatAnthropic expect a bare model name and (for OpenAI-compatible
	// gateways) an explicit base_url. Without this translation the sidecar would
	// hit api.openai.com with a model name it doesn't understand.
	modelRef := cfg.Cowork.BrowserUseModel
	if modelRef == "" {
		modelRef = cfg.Cowork.VLMModel
	}
	var modelName, baseURL, providerKind, apiKeyEnv string
	if entry, ok := cfg.ResolveModel(modelRef); ok {
		modelName = entry.Model
		baseURL = entry.BaseURL
		providerKind = entry.Kind
		apiKeyEnv = entry.APIKeyEnv
	}
	// Fallbacks when the ref didn't resolve: pass the raw ref as the model and
	// let the sidecar's client default to OPENAI_API_KEY / api.openai.com. This
	// is the common dev path where the user set a model directly.
	if modelName == "" {
		modelName = modelRef
	}

	maxSteps := cfg.Cowork.BrowserUseMaxSteps
	if req.MaxSteps > 0 {
		maxSteps = req.MaxSteps
	}

	// Resolve the browser to drive. Two modes:
	//
	//   Attach (cfg.Cowork.BrowserAttachURL set): connect to the browser the
	//   user already has open — normally the managed browser started from
	//   Settings → 办公, which keeps its window and login state across tasks.
	//   We never launch or close it here; the sidecar's browser.close() only
	//   disconnects. An unreachable endpoint is a hard error (not a silent
	//   fallback) so a forgotten managed browser surfaces immediately with
	//   guidance instead of surprising the user with a fresh temp browser.
	//
	//   Launch (default): spawn a system browser with a CDP endpoint. A fresh
	//   temp profile per run keeps sessions isolated unless the user configured
	//   a persistent BrowserUserDataDir (to keep login state). The proxy routes
	//   the browser through the user's network config. Closed when the run ends.
	var cdpURL string
	var browserLabel string
	if attach := strings.TrimSpace(cfg.Cowork.BrowserAttachURL); attach != "" {
		probeCtx, cancelProbe := context.WithTimeout(ctx, 5*time.Second)
		info, err := browserlaunch.ProbeAttach(probeCtx, attach)
		cancelProbe()
		if err != nil {
			return nil, "", fmt.Errorf(`attach browser: %w — 可控浏览器未运行：在 设置→办公 点击"启动可控浏览器"，或清空 CDP 地址回到自动新开浏览器`, err)
		}
		cdpURL = info.WSURL
		browserLabel = info.BrowserName
		slog.Info("browser_auto: attached to running browser",
			"cdp", info.CDPURL, "browser", info.BrowserName)
	} else {
		handle, err := browserlaunch.Launch(ctx, browserlaunch.LaunchOptions{
			ExecutablePath: cfg.Cowork.BrowserPath,
			Headless:       cfg.Cowork.BrowserHeadless,
			UserDataDir:    cfg.Cowork.BrowserUserDataDir,
			Proxy:          resolveBrowserProxyURL(cfg.NetworkProxySpec()),
			StartURL:       req.URL,
		})
		if err != nil {
			return nil, "", fmt.Errorf("launch browser: %w", err)
		}
		defer handle.Close()
		cdpURL = handle.WSURL
		browserLabel = handle.BrowserName
		slog.Info("browser_auto: launched shared browser",
			"name", handle.BrowserName, "cdp", handle.CDPURL, "ws", handle.WSURL)
	}

	// Mirror panel: mark the run live before the first step, so the panel can
	// auto-open before any screenshot arrives (a long first page load would
	// otherwise leave it blank). runEnded keeps the end emission on ALL exit
	// paths (success, error, sidecar failure) without duplicating it below.
	builtin.EmitBrowserPanel(builtin.BrowserPanelFrame{
		Kind: "status", Source: "auto", Phase: "start", Text: browserLabel,
	})
	runEnded := false
	emitRunEnd := func(text string) {
		if runEnded {
			return
		}
		runEnded = true
		builtin.EmitBrowserPanel(builtin.BrowserPanelFrame{
			Kind: "status", Source: "auto", Phase: "end", Text: text,
		})
	}

	// Drive the sidecar. The cdp_url we hand it points at the browser we just
	// launched, so there is exactly one shared instance. The model/base_url/key
	// come from the resolved provider entry above. The proxy applies to the
	// sidecar's LLM client (the browser got its own --proxy-server at launch) so
	// LLM calls reach the gateway through the user's network config too.
	llmProxy := resolveBrowserProxyURL(cfg.NetworkProxySpec())
	stream, err := client.RunStream(ctx, browseruse.RunRequest{
		Goal:         req.Goal,
		URL:          req.URL,
		CDPURL:       cdpURL,
		MaxSteps:     maxSteps,
		Model:        modelName,
		ProviderKind: providerKind,
		BaseURL:      baseURL,
		APIKeyEnv:    apiKeyEnv,
		Proxy:        llmProxy,
	})
	if err != nil {
		emitRunEnd("error: sidecar /run: " + err.Error())
		return nil, "", fmt.Errorf("sidecar /run: %w", err)
	}

	// If the turn is cancelled (user clicked Stop, or the parent turn ended),
	// tell the sidecar to /stop so the agent loop breaks at its next step hook.
	// Without this the Python loop keeps running (billing LLM tokens, holding the
	// single-run lock) until the deferred handle.Close() kills the browser out
	// from under it — an orphaned run that also starves later calls with HTTP 409.
	// We use a detached context for the Stop call so it can complete even after
	// the turn ctx is gone.
	stopCtx, stopCancel := context.WithCancel(context.Background())
	defer stopCancel()
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Stop(stopCtx)
		case <-stopCtx.Done():
		}
	}()

	var steps []builtin.BrowserAutoStep
	var summary string
	for ev := range stream {
		// Screenshots never enter the tool output (context bloat); the mirror
		// panel is their consumer. Forward every frame as-is, plus a text line
		// for actions so the panel can show what the agent is doing between
		// frames without a second event channel.
		switch ev.Type {
		case browseruse.EventScreenshot:
			if ev.Image != "" {
				builtin.EmitBrowserPanel(builtin.BrowserPanelFrame{
					Kind: "frame", Source: "auto", Text: lastAutoAction(steps), Image: ev.Image,
				})
			}
		case browseruse.EventAction:
			builtin.EmitBrowserPanel(builtin.BrowserPanelFrame{
				Kind: "status", Source: "auto", Phase: "step", Text: ev.Text,
			})
		}
		step := builtin.BrowserAutoStep{
			Type: string(ev.Type),
			Step: ev.Step,
			Text: ev.Text,
			Done: ev.Done,
		}
		steps = append(steps, step)
		if ev.Done {
			switch ev.Type {
			case "done":
				summary = ev.Text
				emitRunEnd(ev.Text)
			case "error":
				emitRunEnd("error: " + ev.Text)
				// Return the steps taken + a clear error; the tool wraps it.
				return steps, "", fmt.Errorf("agent run failed: %s", ev.Text)
			}
		}
	}
	emitRunEnd(summary)
	return steps, summary, nil
}

// lastAutoAction returns the most recent action text for a screenshot frame,
// so the mirror panel can caption each image with what produced it.
func lastAutoAction(steps []builtin.BrowserAutoStep) string {
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Type == "action" {
			return strings.TrimSpace(steps[i].Text)
		}
	}
	return ""
}
