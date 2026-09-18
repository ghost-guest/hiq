package main

// screenshot_solve.go contains the cross-platform screenshot solving logic:
// capture → VLM solve (with optional web search) → IM push + toast.
// This file has no build tag — it compiles on all platforms.

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image/png"
	"log/slog"
	"os"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/boot"
	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/netclient"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/tool/builtin"
)

const defaultSolvePrompt = "请从屏幕截图中找到用户当前遇到的问题或题目，然后逐步推理并给出答案。完成后自行验证答案是否正确，如不确定请联网搜索核实。最终给出：1）识别到的题目 2）答案 3）解题过程 4）验证结果。"
const defaultSolveSystemPrompt = "你是一个能看图的解题助手。首先从截图中准确识别出用户正在处理的题目或问题，然后逐步推理。遇到不确定的信息，主动使用联网搜索核实。给出答案后，用逆向推理或代入法验证答案的正确性。如果发现错误，自行纠正后再给出最终答案。"

func (a *App) triggerScreenshotSolve() {
	a.emitScreenshotNotice("正在解题中（可能联网搜索，请稍候）…", "")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		img, err := builtin.CaptureFullScreen()
		if err != nil || img == nil {
			a.emitScreenshotNotice("截图失败: "+fmt.Sprint(err), "")
			return
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			a.emitScreenshotNotice("截图编码失败", "")
			return
		}
		dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
		cfg, err := config.Load()
		if err != nil {
			a.emitScreenshotNotice("配置读取失败", "")
			return
		}
		model := cfg.Cowork.ScreenshotVLMModel
		if model == "" {
			a.emitScreenshotNotice("未配置图片识别模型：请在设置中选择一个支持视觉的模型", "")
			return
		}
		entry, err := resolveModelEntry(model)
		if err != nil {
			a.emitScreenshotNotice("模型未找到: "+err.Error(), "")
			return
		}
		prov, err := boot.NewProviderWithProxy(entry, netclient.ProxySpec{Mode: netclient.ModeAuto}, false)
		if err != nil {
			a.emitScreenshotNotice("模型初始化失败: "+err.Error(), "")
			return
		}
		prompt := cfg.Cowork.ScreenshotPrompt
		if strings.TrimSpace(prompt) == "" {
			prompt = defaultSolvePrompt
		}
		result, err := solveScreenshot(ctx, prov, entry, dataURL, prompt)
		if err != nil {
			a.emitScreenshotNotice("解题失败: "+err.Error(), "")
			return
		}
		a.emitScreenshotNotice(result, "")
		if gw := a.botGW.Load(); gw != nil {
			if dest := a.screenshotPushDest(); dest != "" {
				pushCtx, pushCancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := gw.Push(pushCtx, dest, "🧮 截图解题结果：\n\n"+result); err != nil {
					slog.Warn("screenshot: IM push failed", "dest", dest, "err", err)
				}
				pushCancel()
			}
		}
	}()
}

func solveScreenshot(ctx context.Context, prov provider.Provider, entry *config.ProviderEntry, imageDataURL string, prompt string) (string, error) {
	// Turn input carrier: text plus one image reference. MessageFromInput (used
	// by both the sub-agent and the one-shot path) flattens this into the
	// provider-visible Message contract.
	content := []provider.ContentPart{
		{Type: "text", Text: prompt},
		{Type: "image_url", ImageURL: &provider.ImageURL{URL: imageDataURL}},
	}
	if !webSearchKeyConfigured() {
		return solveOneShot(ctx, prov, content)
	}
	reg := (&desktopExpertRunner{}).webSearchRegistry()
	if reg == nil {
		return solveOneShot(ctx, prov, content)
	}
	sess := agent.NewSession(defaultSolveSystemPrompt)
	opts := agent.Options{MaxSteps: 8, ContextWindow: entry.ContextWindow, RequireVisibleFinal: true}
	sink := event.FuncSink(func(e event.Event) { _ = e })
	sub := agent.New(prov, reg, sess, opts, sink)
	runErr := sub.Run(ctx, content)
	answer := lastAssistantText(sess)
	if runErr != nil {
		if isMaxStepsPaused(runErr) && answer != "" {
			return answer, nil
		}
		return solveOneShot(ctx, prov, content)
	}
	if answer == "" {
		return solveOneShot(ctx, prov, content)
	}
	return answer, nil
}

func solveOneShot(ctx context.Context, prov provider.Provider, content any) (string, error) {
	req := provider.Request{Messages: []provider.Message{provider.MessageFromInput(content)}}
	ch, err := prov.Stream(ctx, req)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for chunk := range ch {
		if chunk.Type == provider.ChunkText {
			b.WriteString(chunk.Text)
		}
		if chunk.Err != nil {
			return b.String(), chunk.Err
		}
	}
	return strings.TrimSpace(b.String()), nil
}

func webSearchKeyConfigured() bool {
	return os.Getenv("BRAVE_API_KEY") != "" || os.Getenv("BRAVE_SEARCH_API_KEY") != "" || os.Getenv("EXA_API_KEY") != "" || os.Getenv("LINKUP_API_KEY") != "" || os.Getenv("ANYSEARCH_API_KEY") != ""
}

func resolveModelEntry(modelRef string) (*config.ProviderEntry, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	entry, ok := cfg.ResolveModel(modelRef)
	if !ok {
		return nil, fmt.Errorf("VLM model %q not found in config", modelRef)
	}
	return entry, nil
}

func (a *App) emitScreenshotNotice(message, detail string) {
	if a.ctx == nil {
		return
	}
	wailsruntime.EventsEmit(a.ctx, "screenshot:notice", map[string]string{"message": message, "detail": detail})
}

func parseHotkey(s string) (mod, vk int, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, fmt.Errorf("empty hotkey")
	}
	parts := strings.Split(s, "+")
	vk = 0
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch strings.ToLower(p) {
		case "ctrl", "control":
			mod |= 0x0002
		case "shift":
			mod |= 0x0004
		case "alt":
			mod |= 0x0001
		case "win", "super", "meta":
			mod |= 0x0008
		default:
			if vk != 0 {
				return 0, 0, fmt.Errorf("multiple keys in hotkey %q", s)
			}
			vk = keyToVK(p)
			if vk == 0 {
				return 0, 0, fmt.Errorf("unknown key %q in hotkey", p)
			}
		}
	}
	if vk == 0 {
		return 0, 0, fmt.Errorf("no key in hotkey %q", s)
	}
	return mod, vk, nil
}

func keyToVK(key string) int {
	if len(key) == 1 {
		c := key[0]
		if c >= 'A' && c <= 'Z' {
			return int(c)
		}
		if c >= 'a' && c <= 'z' {
			return int(c - 32)
		}
		if c >= '0' && c <= '9' {
			return int(c)
		}
	}
	switch strings.ToUpper(key) {
	case "F1":
		return 0x70
	case "F2":
		return 0x71
	case "F3":
		return 0x72
	case "F4":
		return 0x73
	case "F5":
		return 0x74
	case "F6":
		return 0x75
	case "F7":
		return 0x76
	case "F8":
		return 0x77
	case "F9":
		return 0x78
	case "F10":
		return 0x79
	case "F11":
		return 0x7A
	case "F12":
		return 0x7B
	case "SPACE":
		return 0x20
	case "ENTER":
		return 0x0D
	case "TAB":
		return 0x09
	}
	return 0
}
