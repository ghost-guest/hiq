package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/linkpeersignal"
	"github.com/zzycxz/hiq/internal/mobilebridge"
	"github.com/zzycxz/hiq/internal/scheduler"
	"github.com/zzycxz/hiq/internal/secret"
)

// desktop/mobilebridge_app.go wires the internal/mobilebridge Bridge into the
// hiq desktop App. It provides:
//   - a secret.Store-backed KeyStore (encrypted at rest) for S's long-term
//     Ed25519 key + paired peer pubs
//   - an execAdapter exposing App's existing tab methods as CommandExecutor
//   - Wails-bound methods the frontend calls (pair / status / confirm / unpair)
//
// MINIMAL-INVASION design: app.go grows one field (mobilebridge atomic.Pointer)
// and one startup line; tabs.go grows one forward line in tabEventSink.Emit.
// Everything else lives in this file.

// secretKeyStore adapts the encrypted secret.Store (DPAPI on Windows, AES-GCM
// elsewhere) to mobilebridge.KeyStore, so the long-term Ed25519 private key and
// paired peer pubs are encrypted at rest (HIQ_SPEC §6). It deliberately
// uses its own store file — NOT secret.Default() — because the shared store's
// LoadIntoEnv exports every entry into the process env, which must never happen
// to key material.
type secretKeyStore struct {
	store *secret.Store
}

// mobilebridgeStorePath is the encrypted keystore location, beside the other
// hiq secrets in the user config dir.
func mobilebridgeStorePath() string {
	return filepath.Join(desktopConfigDir(), "mobilebridge.enc.json")
}

func newSecretKeyStore() (*secretKeyStore, error) {
	if desktopConfigDir() == "" {
		return nil, errors.New("user config dir unavailable")
	}
	ks := &secretKeyStore{store: secret.New(mobilebridgeStorePath())}
	ks.migrateLegacyKeyFileAt(legacyMobilebridgeKeysPath())
	return ks, nil
}

func (s *secretKeyStore) Get(key string) ([]byte, error) {
	v, ok, err := s.store.Get(key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, mobilebridge.ErrNotFound
	}
	return []byte(v), nil
}

func (s *secretKeyStore) Set(key string, val []byte) error {
	return s.store.Set(key, string(val))
}

func (s *secretKeyStore) Delete(key string) error {
	return s.store.Delete(key)
}

// legacyMobilebridgeKeysPath is the M1 plaintext keystore (~/.hiq/
// mobilebridge_keys.json, base64 values). Kept only for the one-time migration
// below.
func legacyMobilebridgeKeysPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".hiq", "mobilebridge_keys.json")
}

// migrateLegacyKeyFileAt lifts the M1 plaintext keystore into the encrypted
// store once, then removes the plaintext file. The long-term key is verified to
// decrypt before the file is dropped — a lost key means a new device identity
// and re-pairing every phone, so only a verified migration deletes. On any
// failure the legacy file is kept; whatever made it into the store still works.
func (s *secretKeyStore) migrateLegacyKeyFileAt(p string) {
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return // no legacy file — nothing to migrate
	}
	var legacy map[string]string
	if err := json.Unmarshal(b, &legacy); err != nil {
		slog.Warn("mobilebridge: legacy keystore unreadable; keeping file", "path", p, "err", err)
		return
	}
	if len(legacy) == 0 {
		_ = os.Remove(p)
		return
	}
	var anyKey string
	for k, v := range legacy {
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			slog.Warn("mobilebridge: legacy keystore entry undecodable; keeping file", "path", p, "key", k, "err", err)
			return
		}
		if _, ok, _ := s.store.Get(k); !ok {
			if err := s.store.Set(k, string(raw)); err != nil {
				slog.Warn("mobilebridge: legacy keystore migration failed; keeping file", "path", p, "err", err)
				return
			}
		}
		anyKey = k
	}
	// Verify a migrated entry survives a full encrypt→decrypt round trip before
	// dropping the plaintext original.
	if v, ok, err := s.store.Get(anyKey); err == nil && ok && len(v) > 0 {
		_ = os.Remove(p)
		slog.Info("mobilebridge: migrated legacy keystore into encrypted store", "from", p)
	}
}

// loadOrCreateDeviceKey returns S's long-term Ed25519 keypair, generating +
// persisting a fresh one on first run. The deviceId is derived from the pub
// (PROTOCOL §2.2), so a regenerated key means a new identity + re-pair.
func loadOrCreateDeviceKey(store mobilebridge.KeyStore) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	if b, err := store.Get("mobilebridge.device.priv"); err == nil && len(b) == ed25519.PrivateKeySize {
		priv := ed25519.PrivateKey(b)
		pub, ok := priv.Public().(ed25519.PublicKey)
		if !ok {
			return nil, nil, fmt.Errorf("invalid stored private key")
		}
		return priv, pub, nil
	}
	pub, priv, err := mobilebridge.GenerateLongTerm()
	if err != nil {
		return nil, nil, err
	}
	if err := store.Set("mobilebridge.device.priv", priv); err != nil {
		return nil, nil, err
	}
	return priv, pub, nil
}

// execAdapter exposes App's existing tab methods as mobilebridge.CommandExecutor.
// Each dispatch runs in its own goroutine so the bridge's DataChannel read loop
// is never blocked by a long Controller operation.
type execAdapter struct {
	app     *App
	bridge  atomic.Pointer[mobilebridge.Bridge] // Pause/Resume 回显发射用（ensureMobileBridge 注入）
	fdMu    sync.Mutex
	fdFiles map[string]*os.File // file_drop 接收中的文件（name → handle）
}

// resolveMobileTab maps linkpeer's tab alias ("default"/"") to a real hiq
// tab UUID (the active tab). All mobilebridge commands route through this so
// the phone transparently joins the desktop's active session without knowing
// hiq's UUID-style tab IDs.
func (a *App) resolveMobileTab(tab string) string {
	if tab == "default" || tab == "" {
		return a.ActiveTabID()
	}
	return tab
}

func (e *execAdapter) Submit(tab, input, _ string) error {
	tab = e.app.resolveMobileTab(tab)
	slog.Info("mobilebridge: submit", "resolvedTab", tab, "inputLen", len(input))
	if tab == "" {
		slog.Warn("mobilebridge: submit dropped — no active tab")
		return errors.New("tab_not_found") // S1: 让 router 回传错误给 C
	}
	go e.app.SubmitToTab(tab, input)
	return nil
}

func (e *execAdapter) Cancel(tab string) error {
	if tab = e.app.resolveMobileTab(tab); tab != "" {
		go e.app.CancelTab(tab)
	}
	return nil
}

// emitState 向订阅了该 tab 的手机端发一条状态回执（AUDIT P1-10：本地
// 乐观显示 → 服务端真值）。ForwardEvent 兼入 ring，重连 resync 可补。
func (e *execAdapter) emitState(tab, kind string) {
	if tab == "" {
		return
	}
	if mb := e.bridge.Load(); mb != nil {
		wire, _ := json.Marshal(map[string]any{"kind": kind})
		go mb.ForwardEvent(tab, wire)
	}
}

func (e *execAdapter) Steer(tab, text string) error {
	if tab = e.app.resolveMobileTab(tab); tab != "" {
		go e.app.SteerForTab(tab, text)
	}
	return nil
}

func (e *execAdapter) Pause(tab string) error {
	if tab = e.app.resolveMobileTab(tab); tab != "" {
		go e.app.PauseTab(tab)
		e.emitState(tab, "paused") // P1-10: 手机端暂停态由服务端真值驱动
	}
	return nil
}

func (e *execAdapter) Resume(tab string) error {
	if tab = e.app.resolveMobileTab(tab); tab != "" {
		go e.app.ResumeTurnTab(tab)
		e.emitState(tab, "resumed")
	}
	return nil
}

func (e *execAdapter) Approve(tab, id string, allow, session, persist bool) error {
	if tab = e.app.resolveMobileTab(tab); tab != "" {
		go e.app.ApproveTab(tab, id, allow, session, persist)
	}
	return nil
}
func (e *execAdapter) Answer(string, string, []string) error { return nil } // TODO: wire to AskRequest
func (e *execAdapter) SetPlan(_ string, on bool) error       { go e.app.SetPlanMode(on); return nil }
func (e *execAdapter) SetModel(tab, model string) error {
	if tab = e.app.resolveMobileTab(tab); tab != "" {
		go e.app.SetModelForTab(tab, model)
	}
	return nil
}
func (e *execAdapter) ListSessions() ([]mobilebridge.SessionInfo, error) {
	return e.app.SessionListForMobile(), nil
}

// ModelsForMobile 返回激活 tab 可用的模型列表（list_models 回复）。
func (e *execAdapter) ListModels() ([]mobilebridge.ModelInfo, error) {
	return e.app.ModelsForMobile(), nil
}

// ListTemplates 返回办公模板目录（list_templates 回复，UX M5 W14）。
// 源 = scheduler.BuiltinTemplates（桌面定时任务同一份目录，单一真源）。
func (e *execAdapter) ListTemplates() ([]mobilebridge.TemplateInfo, error) {
	out := make([]mobilebridge.TemplateInfo, 0, len(scheduler.BuiltinTemplates))
	for _, t := range scheduler.BuiltinTemplates {
		out = append(out, mobilebridge.TemplateInfo{
			ID: t.ID, Name: t.Name, Category: t.Category, Desc: t.Desc,
		})
	}
	return out, nil
}

// NewTab 在桌面创建一个新会话 tab，返回它的 ID（new_tab 命令）。
func (e *execAdapter) NewTab(workspaceRoot, profile string) (string, error) {
	scope := "global"
	if workspaceRoot != "" {
		scope = "project"
	}
	tab, err := e.app.EnsureBlankTab(scope, workspaceRoot, profile)
	if err != nil {
		return "", err
	}
	return tab.ID, nil
}

func (e *execAdapter) RenameSession(tab, title string) error {
	if t := e.app.resolveMobileTab(tab); t != "" {
		go e.app.RenameTabForMobile(t, title)
	}
	return nil
}

func (e *execAdapter) DeleteSession(tab string) error {
	if t := e.app.resolveMobileTab(tab); t != "" {
		go e.app.CloseTab(t)
	}
	return nil
}

// OfficeRun 触发办公生成（M5）。简化版：把模板 + 参数拼成指令 submit 到激活
// tab，hiq 的 Controller（cowork profile）调用办公工具链生成文档。
func (e *execAdapter) OfficeRun(tab, template string, args map[string]string) error {
	tab = e.app.resolveMobileTab(tab)
	if tab == "" {
		return nil
	}
	input := "生成「" + template + "」"
	if len(args) > 0 {
		var parts []string
		for k, v := range args {
			parts = append(parts, k+": "+v)
		}
		input += "（" + strings.Join(parts, "；") + "）"
	}
	go e.app.SubmitToTab(tab, input)
	return nil
}

// —— 文件投递（file_drop，§4）—— 落地到 configDir/hiq/incoming/。

func (e *execAdapter) FileStart(_ string, name string, size int64) error {
	// 安全：清理文件名，防路径遍历 + 限制大小 + 白名单扩展名
	clean := filepath.Base(name) // 去掉所有 ../ 和路径分隔符
	if clean == "." || clean == "/" || clean == "" {
		return errors.New("invalid filename")
	}
	const maxFileSize = 50 * 1024 * 1024 // 50MB 上限
	if size > maxFileSize {
		return errors.New("file too large (max 50MB)")
	}
	// 扩展名白名单（§11.2③ 安全沙箱）。UX_ONBOARDING W12：补代码类扩展名
	//（linkpeer 定位开发者伴侣，传 .go/.py/.ts 是最主场景，原名单全挡）。
	ext := strings.ToLower(filepath.Ext(clean))
	allowed := map[string]bool{
		".txt": true, ".md": true, ".pdf": true, ".doc": true, ".docx": true,
		".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
		".csv": true, ".json": true, ".png": true, ".jpg": true, ".jpeg": true,
		".gif": true, ".zip": true, ".mp3": true, ".mp4": true,
		// 代码/配置（W12 默认集扩充）
		".go": true, ".py": true, ".ts": true, ".tsx": true, ".js": true,
		".jsx": true, ".rs": true, ".java": true, ".kt": true, ".c": true,
		".h": true, ".cpp": true, ".cs": true, ".rb": true, ".php": true,
		".sh": true, ".ps1": true, ".bat": true, ".sql": true,
		".yaml": true, ".yml": true, ".toml": true, ".xml": true, ".html": true,
		".css": true, ".dart": true, ".swift": true, ".vue": true, ".svelte": true,
		".proto": true, ".lock": true, ".ini": true, ".conf": true, ".env": true,
	}
	if !allowed[ext] {
		return errors.New("file type not allowed: " + ext)
	}
	dir := filepath.Join(fileDropDir(), "incoming")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, clean))
	if err != nil {
		return err
	}
	e.fdMu.Lock()
	// 关闭并清理上一个未完成的文件（同时只允许一个文件投递）
	for _, old := range e.fdFiles {
		_ = old.Close()
	}
	e.fdFiles = map[string]*os.File{clean: f}
	e.fdMu.Unlock()
	slog.Info("mobilebridge: file_start", "name", clean, "size", size)
	return nil
}

func (e *execAdapter) FileChunk(_ string, _ int, b64 string) error {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return err
	}
	e.fdMu.Lock()
	defer e.fdMu.Unlock()
	for _, f := range e.fdFiles {
		_, _ = f.Write(data)
	}
	return nil
}

func (e *execAdapter) FileEnd(_ string, name string) error {
	e.fdMu.Lock()
	f := e.fdFiles[name]
	delete(e.fdFiles, name)
	e.fdMu.Unlock()
	if f != nil {
		_ = f.Close()
		slog.Info("mobilebridge: file_end", "name", name)
	}
	return nil
}

func fileDropDir() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "hiq")
}

// LoadSession 返回某个 tab 的对话历史（load_session 命令，§4.2）。
func (e *execAdapter) LoadSession(tab string) ([]map[string]any, error) {
	tab = e.app.resolveMobileTab(tab)
	if tab == "" {
		return nil, nil
	}
	msgs := e.app.HistoryForTab(tab)
	out := make([]map[string]any, len(msgs))
	for i, m := range msgs {
		out[i] = map[string]any{
			"role": m.Role, "content": m.Content, "reasoning": m.Reasoning,
		}
	}
	return out, nil
}

// ensureMobileBridge loads the device key + creates the Bridge. Called once
// from App.startup. Safe to call when disabled — the bridge simply won't be
// stored, and every MobileBridge* binding no-ops.
func (a *App) ensureMobileBridge(ctx context.Context) {
	store, err := newSecretKeyStore()
	if err != nil {
		slog.Warn("mobilebridge: key store unavailable, bridge disabled", "err", err)
		return // no key store → mobile bridge stays off
	}
	priv, pub, err := loadOrCreateDeviceKey(store)
	if err != nil {
		slog.Warn("mobilebridge: device key load failed, bridge disabled", "err", err)
		return
	}
	cfg := mobilebridge.DefaultConfig()
	var pairAddr string // 用户钉死的配对网卡（[mobilebridge] pair_address）
	// Resolution order: LINKPEER_SIGNAL env > [mobilebridge] signal_url > default.
	// The env var wins so ad-hoc `LINKPEER_SIGNAL=... wails dev` still works;
	// the TOML section is the persistent, restart-surviving way to point hiq
	// at your linkpeer-signal K during normal use.
	signalConfigured := false
	if s := os.Getenv("LINKPEER_SIGNAL"); s != "" {
		cfg.SignalURL = s
		signalConfigured = true
	} else if c, cerr := config.Load(); cerr == nil {
		if c.MobileBridge.SignalURL != "" {
			cfg.SignalURL = c.MobileBridge.SignalURL
			signalConfigured = true
		}
		if len(c.MobileBridge.STUNServers) > 0 {
			cfg.STUNServers = append(cfg.STUNServers, c.MobileBridge.STUNServers...)
		}
		if c.MobileBridge.LogLevel != "" {
			cfg.LogLevel = c.MobileBridge.LogLevel
		}
		cfg.AutoConfirm = c.MobileBridge.AutoConfirm
		pairAddr = c.MobileBridge.PairAddress
		cfg.UDPKnock = c.MobileBridge.UDPKnock
		cfg.KnockServer = c.MobileBridge.KnockServer
		// 公网跳板：云 K 第二长连 + TURN 中转兜底（跨网时手机经云候选
		// 打洞/中继；同网仍走局域网直连，零云）。
		cfg.CloudSignalURL = c.MobileBridge.CloudSignalURL
		cfg.TURNEnabled = c.MobileBridge.TURNEnabled
		cfg.TURNServers = c.MobileBridge.TURNServers
		cfg.TURNUser = c.MobileBridge.TURNUser
		cfg.TURNPass = c.MobileBridge.TURNPass
	}
	// 零配置上手（UX_ONBOARDING W1）：没配 signal_url 时进程内起嵌入式 K
	//（0.0.0.0:8080，手机经 LAN 地址直达），装完即用；8080 被占（外部 K
	// 已在跑）不随机换端口——会毁掉手机已保存的 relay 候选——降级提示。
	if !signalConfigured {
		if ln, lerr := net.Listen("tcp", "0.0.0.0:8080"); lerr == nil {
			ksrv := linkpeersignal.NewServer(linkpeersignal.DefaultConfig(), linkpeersignal.NewAudit("error"))
			hsrv := &http.Server{Handler: ksrv.Routes()}
			go func() {
				<-ctx.Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = hsrv.Shutdown(shutdownCtx)
			}()
			go func() {
				if serr := hsrv.Serve(ln); serr != nil && serr != http.ErrServerClosed {
					slog.Warn("mobilebridge: embedded K exited", "err", serr)
				}
			}()
			cfg.SignalURL = "http://127.0.0.1:8080"
			mobilebridgeEmbedded = true
			slog.Info("mobilebridge: embedded K listening on 0.0.0.0:8080 (zero-config LAN mode)")
		} else {
			slog.Warn("mobilebridge: :8080 occupied — embedded K disabled; "+
				"set [mobilebridge] signal_url to your external K", "err", lerr)
		}
	}
	// W4：knock_server 智能默认（云 K 同机 coturn）
	cfg = mobilebridge.ApplyKnockDefault(cfg)
	slog.Info("mobilebridge: starting bridge",
		"signal_url", cfg.SignalURL, "stun", cfg.STUNServers, "log_level", cfg.LogLevel,
		"cloud_signal_url", cfg.CloudSignalURL, "turn_enabled", cfg.TURNEnabled,
		"udp_knock", cfg.UDPKnock, "knock_server", cfg.KnockServer)
	adapter := &execAdapter{app: a, fdFiles: map[string]*os.File{}}
	bridge := mobilebridge.NewBridge(cfg, priv, pub, store, adapter, mobilebridge.NewAudit(cfg.LogLevel))
	adapter.bridge.Store(bridge)
	// 注入 tab 别名解析：linkpeer 发 "default"/""，hiq 需要 UUID tab id。
	// 映射到当前激活 tab，手机就能接入桌面正在用的会话。
	bridge.SetResolveTab(func(tab string) string {
		if tab == "default" || tab == "" {
			if id := a.ActiveTabID(); id != "" {
				return id
			}
		}
		return tab
	})
	a.mobilebridge.Store(bridge)
	if pairAddr != "" {
		bridge.SetPairAddress(pairAddr)
	}
	go bridge.Start(ctx)
}

// --- Wails-bound methods (frontend calls these) ---

// MobileBridgeStartPairing 返回 JSON {code, qrURL}（避免 Wails 多返回值折叠成单 string 的歧义）。
func (a *App) MobileBridgeStartPairing() (string, error) {
	mb := a.mobilebridge.Load()
	if mb == nil {
		return "", fmt.Errorf("mobile bridge not initialized")
	}
	code, qrURL, err := mb.StartPairing()
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(map[string]string{"code": code, "qrURL": qrURL})
	return string(b), nil
}

// MobileBridgeStatus reports state for the settings panel.
func (a *App) MobileBridgeStatus() map[string]any {
	mb := a.mobilebridge.Load()
	if mb == nil {
		return map[string]any{"enabled": false, "connected": false}
	}
	return map[string]any{
		"enabled":        true,
		"connected":      mb.SignalConnected(),
		"signal_url":     mb.SignalURL(),
		"embedded":       mobilebridgeEmbedded,
		"pending":        mb.PendingPairings(),
		"udp_knock":      mb.KnockEnabled(),
		"knock_server":   mb.KnockServer(),
		"cloud_relay":    mb.CloudRelayURL(),
		"cloud_connected": mb.CloudConnected(),
	}
}

func (a *App) MobileBridgeConfirm(pairID string) error {
	mb := a.mobilebridge.Load()
	if mb == nil {
		return fmt.Errorf("mobile bridge not initialized")
	}
	return mb.ConfirmPairing(pairID)
}

func (a *App) MobileBridgeReject(pairID string) {
	mb := a.mobilebridge.Load()
	if mb == nil {
		return
	}
	mb.RejectPairing(pairID)
}

func (a *App) MobileBridgeUnpair(devC string) error {
	mb := a.mobilebridge.Load()
	if mb == nil {
		return fmt.Errorf("mobile bridge not initialized")
	}
	mb.Unpair(devC)
	return nil
}

// MobileBridgeListPairNics 返回 JSON：{nics:[{ip,name,label,isDefault}], pinned}。
// 设置面板「配对网卡」下拉框数据源。
func (a *App) MobileBridgeListPairNics() (string, error) {
	mb := a.mobilebridge.Load()
	if mb == nil {
		return "", fmt.Errorf("mobile bridge not initialized")
	}
	pinned := ""
	if c, err := config.Load(); err == nil {
		pinned = c.MobileBridge.PairAddress
	}
	b, _ := json.Marshal(map[string]any{"nics": mb.ListPairNics(), "pinned": pinned})
	return string(b), nil
}

// MobileBridgeSetPairNic 钉死/恢复配对网卡（"" = 自动），并持久化到
// 用户 config.toml 的 [mobilebridge] pair_address。
func (a *App) MobileBridgeSetPairNic(ip string) error {
	mb := a.mobilebridge.Load()
	if mb == nil {
		return fmt.Errorf("mobile bridge not initialized")
	}
	c, err := config.LoadForEditStrict(config.UserConfigPath())
	if err != nil {
		return fmt.Errorf("load user config: %w", err)
	}
	c.MobileBridge.PairAddress = ip
	if err := c.WriteFile(config.UserConfigPath()); err != nil {
		return fmt.Errorf("save pair_address: %w", err)
	}
	mb.SetPairAddress(ip)
	return nil
}

// MobileBridgeSetKnock 配置 UDP 单包敲门（开关 + 远程 STUN 服务器），
// 持久化到 [mobilebridge] udp_knock / knock_server，并即时生效于 Bridge。
func (a *App) MobileBridgeSetKnock(enabled bool, server string) error {
	mb := a.mobilebridge.Load()
	if mb == nil {
		return fmt.Errorf("mobile bridge not initialized")
	}
	c, err := config.LoadForEditStrict(config.UserConfigPath())
	if err != nil {
		return fmt.Errorf("load user config: %w", err)
	}
	c.MobileBridge.UDPKnock = enabled
	c.MobileBridge.KnockServer = server
	if err := c.WriteFile(config.UserConfigPath()); err != nil {
		return fmt.Errorf("save udp_knock: %w", err)
	}
	mb.SetKnock(enabled, server)
	return nil
}

// MobileBridgeSetCloudRelay 配置公网跳板（开关 + 云 K 地址），持久化到
// [mobilebridge] cloud_signal_url，并即时生效于 Bridge（开/关云 K 第二长连、
// 二维码云候选与 turn= 字段）。enabled=false 时清空地址。
func (a *App) MobileBridgeSetCloudRelay(enabled bool, url string) error {
	mb := a.mobilebridge.Load()
	if mb == nil {
		return fmt.Errorf("mobile bridge not initialized")
	}
	c, err := config.LoadForEditStrict(config.UserConfigPath())
	if err != nil {
		return fmt.Errorf("load user config: %w", err)
	}
	if enabled {
		c.MobileBridge.CloudSignalURL = url
	} else {
		c.MobileBridge.CloudSignalURL = ""
	}
	if err := c.WriteFile(config.UserConfigPath()); err != nil {
		return fmt.Errorf("save cloud_signal_url: %w", err)
	}
	mb.SetCloudRelay(c.MobileBridge.CloudSignalURL)
	return nil
}

// MobileBridgeSetKMode 切换信令模式（UX_ONBOARDING W2，重启生效）：
//   - "embedded"：清空 signal_url → 下次启动进程内起嵌入式 K（零配置默认）
//   - "external"：写入手填的外部 K 地址（独立 K / debug-server）
//   - "cloud"：signal_url = cloud_signal_url（纯跨网，无本地 K）
// 返回最终模式供面板回显。当前运行中的 Bridge 不热切（信令重建复杂，
// 重启 hiq 生效——面板有提示文案）。
func (a *App) MobileBridgeSetKMode(mode, externalURL string) (string, error) {
	switch mode {
	case "embedded", "external", "cloud":
	default:
		return "", fmt.Errorf("mode must be embedded|external|cloud")
	}
	c, err := config.LoadForEditStrict(config.UserConfigPath())
	if err != nil {
		return "", fmt.Errorf("load user config: %w", err)
	}
	switch mode {
	case "embedded":
		c.MobileBridge.SignalURL = ""
	case "external":
		if strings.TrimSpace(externalURL) == "" {
			return "", fmt.Errorf("external 模式需要 K 地址")
		}
		c.MobileBridge.SignalURL = strings.TrimSpace(externalURL)
	case "cloud":
		if c.MobileBridge.CloudSignalURL == "" {
			return "", fmt.Errorf("cloud 模式需要先配置公网跳板地址")
		}
		c.MobileBridge.SignalURL = c.MobileBridge.CloudSignalURL
	}
	if err := c.WriteFile(config.UserConfigPath()); err != nil {
		return "", fmt.Errorf("save k_mode: %w", err)
	}
	return mode, nil
}

// MobileBridgeParseTurnCred 粘贴 turn-cred.sh 输出一键解析（UX_ONBOARDING
// W3）：从任意粘贴文本提取 user:pass@host[:port]，回填 turn 四项并持久化。
// 返回 JSON {host,port,user} 供面板回显；解析不到返回 error。
func (a *App) MobileBridgeParseTurnCred(paste string) (string, error) {
	user, pass, host, port, ok := mobilebridge.ParseTurnCred(paste)
	if !ok {
		return "", fmt.Errorf("未找到凭据（需含 user:pass@host[:port]）")
	}
	c, err := config.LoadForEditStrict(config.UserConfigPath())
	if err != nil {
		return "", fmt.Errorf("load user config: %w", err)
	}
	c.MobileBridge.TURNEnabled = true
	c.MobileBridge.TURNServers = []string{
		fmt.Sprintf("turn:%s:%d?transport=udp", host, port),
		fmt.Sprintf("turn:%s:%d?transport=tcp", host, port),
	}
	c.MobileBridge.TURNUser = user
	c.MobileBridge.TURNPass = pass
	if err := c.WriteFile(config.UserConfigPath()); err != nil {
		return "", fmt.Errorf("save turn creds: %w", err)
	}
	// 即时生效：重开云跳板链路让 turnQRParam 带新凭据（二维码即刻可用）
	if mb := a.mobilebridge.Load(); mb != nil && c.MobileBridge.CloudSignalURL != "" {
		mb.SetCloudRelay(c.MobileBridge.CloudSignalURL)
	}
	b, _ := json.Marshal(map[string]any{"host": host, "port": port, "user": user})
	return string(b), nil
}

// keep sync/atomic referenced (the field lives in app.go but the import is
// grouped here for the adapter's awareness; app.go itself also imports it).
var _ atomic.Pointer[mobilebridge.Bridge]

// mobilebridgeEmbedded 记录本次启动是否用内嵌 K（状态面板显示模式用）。
var mobilebridgeEmbedded bool

// ensure App has the field — this is a compile-time check; the actual field
// declaration is in app.go (added by the desktop-integration edit).
// (If you see a compile error here, app.go's mobilebridge field is missing.)
var _ = context.Background
