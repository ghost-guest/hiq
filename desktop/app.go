package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/zzycxz/fairpeer/internal/agent"
	"github.com/zzycxz/fairpeer/internal/boot"
	"github.com/zzycxz/fairpeer/internal/bot"
	"github.com/zzycxz/fairpeer/internal/browserlaunch"
	"github.com/zzycxz/fairpeer/internal/browseruse"
	calendarpkg "github.com/zzycxz/fairpeer/internal/calendar"
	"github.com/zzycxz/fairpeer/internal/config"
	"github.com/zzycxz/fairpeer/internal/control"
	"github.com/zzycxz/fairpeer/internal/event"
	expertspkg "github.com/zzycxz/fairpeer/internal/experts"
	"github.com/zzycxz/fairpeer/internal/fileref"
	fileenc "github.com/zzycxz/fairpeer/internal/fileutil/encoding"
	"github.com/zzycxz/fairpeer/internal/i18n"
	"github.com/zzycxz/fairpeer/internal/mcpdiag"
	"github.com/zzycxz/fairpeer/internal/mcpregistry"
	"github.com/zzycxz/fairpeer/internal/memory"
	"github.com/zzycxz/fairpeer/internal/mobilebridge"
	"github.com/zzycxz/fairpeer/internal/permission"
	"github.com/zzycxz/fairpeer/internal/plugin"
	"github.com/zzycxz/fairpeer/internal/present"
	"github.com/zzycxz/fairpeer/internal/provider"
	ragpkg "github.com/zzycxz/fairpeer/internal/rag"
	schedulerpkg "github.com/zzycxz/fairpeer/internal/scheduler"
	"github.com/zzycxz/fairpeer/internal/skill"
	teampkg "github.com/zzycxz/fairpeer/internal/team"
	"github.com/zzycxz/fairpeer/internal/tool/builtin"
)

// eventChannel is the Wails runtime event name the frontend subscribes to for the
// agent's typed event stream. One channel carries every event kind; the payload's
// `kind` field discriminates — the desktop analogue of the serve transport's SSE
// `data:` frames.
const eventChannel = "agent:event"

// singleInstanceID is used by Wails to route a second desktop launch back to the
// running instance. Keep it stable across releases so launcher/Dock/taskbar
// reopen behavior remains predictable on every platform.
const singleInstanceID = "com.fairpeer.desktop"

// App is the Wails-bound application object: the desktop frontend's command
// surface. Its exported methods (Submit/Cancel/Approve/…) are generated into JS
// bindings. The app manages multiple WorkspaceTabs — each with its own controller
// scoped to a project workspace — and routes commands to the active tab. Events
// flow the other way: each tab's controller emits to a tabEventSink that
// forwards events tagged with tabId to the webview via runtime.EventsEmit.
type App struct {
	ctx context.Context

	// mu protects the tab map, tabOrder, activeTabID, and per-tab fields that are read
	// from bound methods. All bound methods that touch a controller use activeCtrl().
	mu          sync.RWMutex
	tabs        map[string]*WorkspaceTab
	tabOrder    []string
	activeTabID string
	readyHook   func()

	// metrics holds the opt-in aggregate agent-metrics aggregator (non-nil only
	// when desktop.metrics is enabled in config). Swapped live by
	// SetDesktopMetrics so the toggle takes effect without a restart.
	metrics atomic.Pointer[metricsAggregator]

	// loopRunState is the active Loop Engineering run (nil when idle); see
	// loop_engine.go. Guarded by mu.
	loopRunState *loopRun

	// mobilebridge is the linkpeer P2P bridge (nil until ensureMobileBridge
	// completes during startup). Read atomically from tabEventSink.Emit's hot path.
	mobilebridge atomic.Pointer[mobilebridge.Bridge]
	botBridge    atomic.Pointer[botBridgeHub]
	// remoteManager owns the live remote-host links (one per WSL distro/user
	// in P1). Lazily initialized on the first remote tab build.
	remoteManager *remoteHostManager

	// managedBrowser is the user-facing 可控浏览器 launched by
	// StartManagedBrowser (nil until started). Kept so a later Start can see
	// we already own the process; we never Close it — the window stays open
	// until the user closes it. Guarded by mu.
	managedBrowser *browserlaunch.Handle

	forceQuit           atomic.Bool
	backgroundMaximised atomic.Bool
	trayReady           bool
	tray                *desktopTray

	mediaTokens *mediaTokenStore
	botInstalls map[string]*botInstallSession
	botGW       atomic.Pointer[bot.BotGateway] // nil when bot is disabled or not started; atomic for lock-free reads from Push/hotkey
	hotkeyMgr   *hotkeyManager                 // screenshot hotkey manager; nil when feature off/stopped
	estopMgr    *estopManager                  // emergency-stop hotkey manager; nil when feature off/stopped
	// knownRemoteIDs 记录已回写过 SessionMappings 的远端 ID（"provider:remoteID"），
	// 避免同一用户每发一条消息就 read-modify-write 整个 config 文件。goroutine 启
	// 动时从已有 SessionMappings 预热，之后每条消息 LoadOrStore 命中即跳过。
	knownRemoteIDs sync.Map

	// sharedHosts shares one plugin.Host per workspace root across desktop tabs
	// so opening N tabs on the same project spawns MCP subprocesses (CodeGraph,
	// etc.) once, not N times. See desktop/shared_host.go.
	sharedHosts   map[string]*sharedPluginHost
	sharedHostsMu sync.Mutex

	// scheduler is the app-level scheduled-task engine (coWork). Created once at
	// startup; bound to the active cowork controller via schedulerRunner so
	// scheduled prompts fire into whichever tab is active. Persists tasks to
	// ~/.config/fairpeer/scheduled_tasks.json across restarts.
	scheduler *schedulerpkg.Scheduler
	// calendarStore is the cowork calendar store (SQLite). Created once at
	// startup; persists to ~/.config/fairpeer/calendar.db.
	calendarStore  *calendarpkg.Store
	calendarRemind *calendarpkg.ReminderEngine

	// ragStore is the cowork knowledge-base store (FTS5 + structured entities).
	// ragPipeline is the background deep-extraction engine (chunks → LLM →
	// entity/relation graph). Both persist to the user config dir.
	ragStore    *ragpkg.Store
	ragPipeline *ragpkg.Pipeline
	ragSession  *ragpkg.SessionRAGContext
	// ragExtractor holds the configured extraction model (legacyExtractor, or
	// nil when no extract model is set). Kept on the App so boot.RebindRAGBudget
	// can re-inject the global RPM budget after each boot.Build — without it,
	// a runtime RPM change (settings rebuild) wouldn't reach RAG extraction
	// until an app restart, and extraction would stay on a stale/disabled budget.
	ragExtractor ragpkg.Extractor
	// heService manages the Hyper-Extract Python server lifecycle.
	heService *HEService
	// buService manages the browser-use Python sidecar (autonomous browsing).
	buService *BrowserUseService
	// expertStore + expertOrchestrator power the 专家团 (expert-team) panel:
	// multi-model collaboration with persistent team rosters.
	expertStore        *expertspkg.Store
	expertOrchestrator *expertspkg.Orchestrator
	// teamStore powers the 团队 (team) panel: a persistent project with a 团长
	// (leader) + 团员 (members) and a kanban board. Persists under the memory
	// data root so team projects follow the user's configured data location.
	// Created at startup by initTeams; requireTeamStore lazily re-creates it.
	teamStore *teampkg.Store
	// expertRuns tracks in-flight expert-team runs keyed by teamID, so a panel
	// remounted after the CoWorkLayout was torn down (tab/profile switch) can
	// query whether a run is still going and re-subscribe to its stream. The
	// backend goroutine runs independent of the frontend, so this survives
	// unmounting. Guarded by expertRunsMu.
	expertRuns   map[string]*expertRunState
	expertRunsMu sync.Mutex
	// screenshotHwnd is the hidden message-only window receiving WM_HOTKEY for
	// the global screenshot hotkey. 0 when the feature is off.
	screenshotHwnd uintptr
	// estopHwnd is the hidden message-only window receiving WM_HOTKEY for the
	// global emergency-stop hotkey. 0 when the feature is off.
	estopHwnd uintptr
}

// mediaTokenEntry holds metadata for a workspace media file served via temporary URL.
type mediaTokenEntry struct {
	absPath   string
	filename  string
	mime      string
	kind      string
	size      int64
	modTime   time.Time
	createdAt time.Time
	expiresAt time.Time
}

// mediaTokenStore manages temporary tokens that grant access to workspace files
// through the AssetServer middleware. Tokens expire after a fixed TTL and are
// capped at a maximum count; creating a new token evicts the oldest entry when
// the store is full.
type mediaTokenStore struct {
	mu    sync.Mutex
	byTok map[string]*mediaTokenEntry
	order []string // oldest first
	maxN  int
	ttl   time.Duration
}

const mediaTokenMax = 256

func newMediaTokenStore() *mediaTokenStore {
	return &mediaTokenStore{
		byTok: map[string]*mediaTokenEntry{},
		maxN:  mediaTokenMax,
		ttl:   10 * time.Minute,
	}
}

func (s *mediaTokenStore) cleanupLocked() {
	now := time.Now()
	for len(s.order) > 0 {
		tok := s.order[0]
		e := s.byTok[tok]
		if e == nil {
			s.order = s.order[1:]
			continue
		}
		if !now.Before(e.expiresAt) {
			delete(s.byTok, tok)
			s.order = s.order[1:]
			continue
		}
		break
	}
	for len(s.order) > s.maxN {
		oldest := s.order[0]
		delete(s.byTok, oldest)
		s.order = s.order[1:]
	}
}

func (s *mediaTokenStore) create(absPath, filename, mime, kind string, size int64, modTime time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cleanupLocked()

	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		panic("crypto/rand.Read failed: " + err.Error())
	}
	token := hex.EncodeToString(tok)

	now := time.Now()
	s.byTok[token] = &mediaTokenEntry{
		absPath:   absPath,
		filename:  filename,
		mime:      mime,
		kind:      kind,
		size:      size,
		modTime:   modTime,
		createdAt: now,
		expiresAt: now.Add(s.ttl),
	}
	s.order = append(s.order, token)

	// Trim oldest if the new token pushed us over the limit.
	for len(s.order) > s.maxN {
		oldest := s.order[0]
		delete(s.byTok, oldest)
		s.order = s.order[1:]
	}

	return token
}

func (s *mediaTokenStore) get(token string) *mediaTokenEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.byTok[token]
	if e == nil {
		return nil
	}
	if time.Now().After(e.expiresAt) {
		delete(s.byTok, token)
		return nil
	}
	return e
}

func (a *App) ensureMediaTokenStore() *mediaTokenStore {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mediaTokens == nil {
		a.mediaTokens = newMediaTokenStore()
	}
	return a.mediaTokens
}

// workspaceMediaMiddleware returns an HTTP middleware that intercepts
// /__fairpeer_workspace_media/{token}/{filename} requests and serves the
// corresponding workspace file. All other paths pass through to the Wails
// default asset handler unchanged.
func (a *App) workspaceMediaMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			prefix := "/__fairpeer_workspace_media/"
			if !strings.HasPrefix(r.URL.Path, prefix) {
				next.ServeHTTP(w, r)
				return
			}

			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}

			rest := strings.TrimPrefix(r.URL.Path, prefix)
			parts := strings.SplitN(rest, "/", 2)
			if len(parts) == 0 || parts[0] == "" {
				http.NotFound(w, r)
				return
			}
			token := parts[0]

			entry := a.ensureMediaTokenStore().get(token)
			if entry == nil {
				http.NotFound(w, r)
				return
			}

			f, err := os.Open(entry.absPath)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer f.Close()

			w.Header().Set("Content-Type", entry.mime)
			w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": entry.filename}))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "private, max-age=600")
			http.ServeContent(w, r, entry.filename, entry.modTime, f)
		})
	}
}

// NewApp constructs the bound object. Tabs are restored in startup from the
// last session's desktop-tabs.json.
func NewApp() *App {
	return &App{tabs: map[string]*WorkspaceTab{}, mediaTokens: newMediaTokenStore(), botInstalls: map[string]*botInstallSession{}, expertRuns: map[string]*expertRunState{}}
}

func (a *App) bootContext() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// Platform exposes the native OS to the frontend so chrome/layout affordances can
// stay platform-scoped instead of relying on browser user-agent guesses.
func (a *App) Platform() string {
	return goruntime.GOOS
}

// startup runs once the webview process is up, before the frontend can issue any
// bound call. It captures the Wails context (needed for EventsEmit), then kicks
// off the initialization in a background goroutine so the webview loads immediately.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// browser console record stream: forward kernel recorder events to the
	// ops panel's live step feed ("browser:record").
	a.initBrowserConsoleForwarders()
	// bot desktop bridge: observe every tab's approval/ask/turn-done events and
	// forward to subscribed IM chats. Sync init — only loads the watcher list.
	a.botBridge.Store(newBotBridge(a))
	// netdev schedulers (inspection / backup / briefing push): start at app
	// launch, not on first settings-page open — a user who never opens 设置
	// still gets the sweeps. Each loop re-reads config; off = idle 1-min polls.
	startInspectionScheduler(a)
	startBackupScheduler(a)
	startBriefingScheduler(a)
	// linkpeer mobile bridge: loads device key, connects to signaling K.
	// Runs async — never blocks GUI startup. No-op if the key store can't init.
	go a.ensureMobileBridge(ctx)
	// ppt-auto: preinstall the skill's Python deps (pip install) in the
	// background so the first PPT generation doesn't pay the setup cliff
	// mid-task. Marker-guarded; failure retries next launch. Never blocks.
	go ensurePPTAutoDeps()
	// Route slog to a file in the config dir so diagnostic logs (mail save,
	// probe, panic traces) are visible in a packaged GUI build, where stdout/
	// stderr are not attached. Truncates on each launch to avoid unbounded
	// growth; rotation isn't needed for short debug sessions.
	if cfgDir := desktopConfigDir(); cfgDir != "" {
		if err := os.MkdirAll(cfgDir, 0o755); err == nil {
			if f, err := os.OpenFile(filepath.Join(cfgDir, "app.log"),
				os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
				slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))
			}
		}
	}
	// Relocate legacy un-profiled session/topic data into the "dev" partition
	// BEFORE tabs are restored, so the sidebar lists sessions at their new
	// paths. Idempotent (guarded by a marker) and best-effort: failures are
	// logged inside, never crashing startup.
	migrateToProfilePartition()
	// Prune ghost projects left by the migration's workspace union: a project
	// copied into BOTH profile files that has no topics/sessions in one of them
	// should not appear in that profile's sidebar. Runs every startup (idempotent)
	// so already-migrated disks are healed too.
	pruneGhostProjects()
	installSystemQuitHook()
	a.startTray()

	// Initialize OS-level notifications so scheduled-task reminders (and other
	// notify-mode output) surface as Windows toast — visible even when the window
	// is minimized to tray / in the background, and persisted to Action Center.
	// Best-effort: a failure only logs and continues (the in-app toast path is
	// the fallback). Registering a response callback brings the window to front
	// when the user clicks the toast.
	if err := runtime.InitializeNotifications(ctx); err != nil {
		slog.Warn("notifications: init failed; reminders will only show in-app", "err", err)
	} else {
		runtime.OnNotificationResponse(ctx, func(_ runtime.NotificationResult) {
			go a.showMainWindow()
		})
	}

	// 启动内嵌 bot gateway
	if cfg, err := config.Load(); err == nil && cfg.Bot.Enabled {
		a.startBotGateway(cfg)
	}

	// One-time upgrade sweep before any tab boots: lift the legacy plaintext
	// credentials file (API keys, bot tokens) into the encrypted secret store.
	// Best-effort — on failure the plaintext file keeps loading via
	// config.loadDotEnv until a later startup succeeds.
	migrateCredentialsFile()

	go a.restoreOrBuildTabs()
	go a.sendStartupPing()
	// Load coWork secrets (SMTP/IMAP passwords) from the fairpeer-managed .env
	// into the process environment BEFORE initScheduler/initRAG, so coWork tools
	// find them via os.Getenv without the user setting system env vars manually.
	loadCoworkEnvAtStartup()
	a.initScheduler()
	a.initCalendar()
	a.initRAG()
	a.initExperts()
	a.initTeams()
	a.StartScreenshotHotkey()
	// Start the emergency-stop hotkey AFTER the screenshot hotkey so both
	// global combos are registered before the app reports ready. E-stop is the
	// safety baseline for screen_* tools; registering it unconditionally (rather
	// than only when a turn is running) keeps the kill switch always-available.
	a.StartEStopHotkey()
	// Initialize the provider-template registry (cache → models.dev remote).
	// Non-blocking: the embed snapshot is used until the cache/remote data loads.
	go a.initRegistry()
}

// initRAG opens the coWork knowledge-base store (FTS5 + structured entities)
// and binds it to the rag_* tools. It also constructs the deep-extraction
// pipeline (chunks → LLM → entity/relation graph) from the loaded config — the
// pipeline's worker goroutines start here and run for the app's lifetime.
// Safe to call once at startup; a failure to open logs a warning but doesn't
// block boot (rag_* tools will report "offline").
func (a *App) initRAG() {
	dbPath := filepath.Join(desktopConfigDir(), "rag.db")
	store, err := ragpkg.Open(dbPath)
	if err != nil {
		slog.Warn("rag: open failed", "err", err)
		return
	}
	a.ragStore = store
	a.ragSession = ragpkg.NewSessionRAGContext()
	builtin.SetRAGStore(store)
	// Bridge the desktop UI's "activate collection" control to the agent's
	// rag_search calls: when the user narrows the session to one collection,
	// LLM-driven rag_search (with no explicit collection arg) auto-scopes to it.
	builtin.SetRAGSessionResolver(func() []string {
		if a.ragSession == nil {
			return nil
		}
		return a.ragSession.GetActiveCollections()
	})

	// Build the extraction pipeline from [cowork] extract_* settings (with
	// conservative defaults: 1 concurrent chunk, 3s between chunks). When no
	// extract model is configured the pipeline uses a noop extractor — FTS5
	// still works, but "deep extract" is a no-op until the user sets a model.
	cfg := ragpkg.DefaultPipelineConfig()
	var extractor ragpkg.Extractor
	if c, err := config.Load(); err == nil {
		if c.Cowork.ExtractInterval != "" {
			if d, err := time.ParseDuration(c.Cowork.ExtractInterval); err == nil && d > 0 {
				cfg.Interval = d
			}
		}
		if c.Cowork.ExtractConcurrency > 0 {
			cfg.Concurrency = c.Cowork.ExtractConcurrency
		}
		// Resolve which model the RAG extractor uses, in priority order:
		//   1. [cowork] extract_model — explicit override (rarely needed);
		//   2. [agent] fast_task_model — the "迅捷任务模型" from Settings → Model,
		//      same one dream/distill use (designed for fast background work);
		//   3. default_model — the main chat model.
		// All three go through config.ResolveModel, which yields a ProviderEntry
		// with the right base_url + api_key_env + bare model name — so the
		// extractor always hits the correct endpoint with the correct key, no
		// hardcoding. This mirrors exactly how the main agent and subagents
		// resolve their models.
		modelRef := strings.TrimSpace(c.Cowork.ExtractModel)
		if modelRef == "" {
			modelRef = strings.TrimSpace(c.Agent.FastTaskModel)
		}
		if modelRef == "" {
			modelRef = strings.TrimSpace(c.DefaultModel)
		}
		if modelRef != "" {
			extCfg := ragpkg.LLMExtractorConfig{TwoStage: true}
			if e, ok := c.ResolveModel(modelRef); ok {
				// ResolveModel gives us the concrete provider (base_url, api_key_env)
				// + bare model name. This is the same resolution the main agent uses.
				extCfg.BaseURL = e.BaseURL
				extCfg.APIKey = e.APIKeyEnv
				extCfg.Model = e.Model
				slog.Info("rag: extraction model resolved", "ref", modelRef, "model", e.Model, "provider", e.Name)
			} else {
				// Fallback: send the ref as-is to the default provider endpoint.
				extCfg.Model = modelRef
				slog.Warn("rag: could not resolve model ref, using as-is", "ref", modelRef)
			}
			extractor = ragpkg.NewLLMExtractor(extCfg)
		}
	}
	if extractor == nil {
		slog.Warn("rag: extract model not configured — deep extraction disabled (FTS5 still works)")
	} else {
		// Stash the extractor so the global RPM budget can be wired into it once
		// boot.Build has initialized globalBudget (it runs later, in
		// restoreOrBuildTabs). boot.RebindRAGBudget — called from the first
		// successful Build and again on every settings rebuild — injects the
		// shared budget so RAG extraction draws from the same per-minute quota
		// as the main conversation, instead of a separate local budget that
		// could exceed the configured RPM. Extraction runs at background
		// priority (reserve_main protects interactive requests).
		a.ragExtractor = extractor
	}
	a.ragPipeline = ragpkg.NewPipeline(store, extractor, cfg, func(ev ragpkg.ProgressEvent) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "rag:progress", ev)
		}
	})
	a.ragPipeline.SetLogger(func(format string, args ...any) {
		slog.Debug("rag: "+format, args...)
	})
	a.ragPipeline.Start()
	// Rehydrate any extraction jobs interrupted by a prior shutdown. Pending/
	// extracting jobs are re-enqueued from their FTS5 chunk text so restarts no
	// longer silently drop in-flight work.
	if n := a.ragPipeline.Resume(); n > 0 {
		slog.Info("rag: resumed interrupted extraction", "chunks", n)
	}

	// Start Hyper-Extract Python server (optional — failure is non-fatal).
	// The port defaults to 18900 but can be overridden via [cowork] he_port.
	scriptPath := FindScript()
	if scriptPath != "" {
		hePort := 0
		if c, err := config.Load(); err == nil && c.Cowork.HEPort > 0 {
			hePort = c.Cowork.HEPort
		}
		a.heService = NewHEService("", scriptPath, hePort)
		if err := a.heService.Start(); err != nil {
			slog.Warn("Hyper-Extract service not started", "err", err)
			a.heService = nil
		} else {
			// HE is ready — wire its embedder into RAG so rag_search reranks FTS5
			// hits by vector cosine (hybrid pipeline: LLM expands for recall, HE
			// precision-ranks). The LLM reranker stays as the fallback when HE is
			// absent (headless/CLI) or the embed call fails (Store.Rerank degrades
			// to plain FTS5 order on any embed error).
			builtin.SetRAGEmbedder(a.heService.Client())
		}
	} else {
		slog.Info("Hyper-Extract script not found — HE extraction unavailable")
	}

	// Start the browser-use autonomous-browsing sidecar (optional — failure is
	// non-fatal). Disabled by config ([cowork] browser_use_enabled=false) or if
	// the script is absent. The sidecar is lazy in effect: even when started,
	// it only does work when browser_auto posts a /run.
	if cfgCowork, cerr := config.Load(); cerr == nil && cfgCowork.Cowork.BrowserUseEnabled {
		buScript := FindBrowserUseScript()
		if buScript != "" {
			a.buService = NewBrowserUseService(cfgCowork.Cowork.BrowserUsePython, buScript, cfgCowork.Cowork.BrowserUsePort)
			if err := a.buService.Start(); err != nil {
				slog.Warn("browser-use sidecar not started", "err", err)
				a.buService = nil
			}
		} else {
			slog.Info("browser-use script not found — autonomous browsing unavailable (browseruse_server.py)")
		}
	}

	// Wire the browser_auto runtime hooks into boot. The client provider is
	// resolved lazily (returns nil until the sidecar is ready), and the panel
	// sink attaches a screencast to the in-app browser view. These are set once
	// at startup; boot.Build's injected runtime reads them at tool-exec time.
	boot.SetBrowserUseClientProvider(func() *browseruse.Client {
		if a.buService == nil || !a.buService.IsReady() {
			return nil
		}
		return a.buService.Client()
	})

	// Browser mirror panel (pane-system §3.6): forward every lifecycle/frame
	// emission from the browser tools + browser_auto to the frontend dock.
	// EventsEmit is goroutine-safe; a.ctx is nil only before startup, and no
	// browser tool can run before then (controllers don't exist yet).
	builtin.SetBrowserPanelSink(func(f builtin.BrowserPanelFrame) {
		if a.ctx == nil {
			return
		}
		runtime.EventsEmit(a.ctx, "browser:mirror", f)
	})

	// Try to wire the global RPM budget into the extractor now. restoreOrBuildTabs
	// runs concurrently (started earlier as a goroutine), so globalBudget may or
	// may not be ready yet; RebindRAGBudget is nil-safe and idempotent. If the
	// first Build hasn't finished, buildTabController rebinds again once it does.
	// This call covers the race where Build already completed before initRAG set
	// up a.ragExtractor (otherwise the extractor would never get the budget).
	rebindCfg, _ := config.Load()
	boot.RebindRAGBudget(a.ragExtractor, rebindCfg)
}

// initScheduler creates the app-level scheduled-task engine, loads persisted
// tasks, binds the runner bridge to the active cowork controller, and starts
// firing. The scheduler lives for the app's lifetime; a scheduled prompt runs in
// whichever tab is active (cowork profile). Safe to call once at startup.
func (a *App) initScheduler() {
	storePath := filepath.Join(desktopConfigDir(), "scheduled_tasks.json")
	a.scheduler = schedulerpkg.New(storePath)
	a.scheduler.SetLogger(func(format string, args ...any) {
		slog.Debug("scheduler: "+format, args...)
	})
	if err := a.scheduler.Load(); err != nil {
		slog.Warn("scheduler: load failed", "err", err)
	}
	a.scheduler.SetRunner(schedulerRunner{app: a})
	a.scheduler.SetIMPusher(schedulerIMPusher{app: a})
	a.scheduler.SetEmailSender(schedulerEmailSender{})
	a.scheduler.SetNotifier(schedulerNotifier{app: a})
	a.scheduler.Start()
	builtin.SetScheduler(a.scheduler)
	builtin.SetAuthNotifier(authNotifier{app: a})
	a.scheduler.SetAccountProber(imapProber{})

	// Surface any one-shot reminders that were due while the app was down. Load
	// captured them; we drain now (after the notifier + OS-toast init are bound)
	// and fire a catch-up notification for each, so a reminder the user set isn't
	// silently lost just because fairpeer wasn't running at the fire instant.
	// Delayed briefly so the OS notification registration (InitializeNotifications
	// in startup) has settled before we send.
	if missed := a.scheduler.DrainMissedReminders(); len(missed) > 0 {
		go func() {
			time.Sleep(2 * time.Second)
			for _, m := range missed {
				(schedulerNotifier{app: a}).Notify("⏰ 错过的提醒："+m.Name, m.Body)
			}
		}()
	}
}

// schedulerIMPusher implements scheduler.IMPusher by routing through the bot
// gateway. The gateway is bound lazily (a.botGW is set when the bot starts,
// which may be after the scheduler init), so we read it at push time. When the
// bot isn't running, Push returns nil (best-effort — a scheduled task shouldn't
// fail because IM is offline).
// authNotifier implements builtin.AuthNotifier by emitting a Wails event.
type authNotifier struct{ app *App }

func (n authNotifier) NotifyAuthExpired(account string) {
	if n.app.ctx != nil {
		runtime.EventsEmit(n.app.ctx, "auth:expired", account)
	}
}

// imapProber implements scheduler.AccountProber by delegating to the real
// IMAP probe (builtin.ProbeAccountIMAP), which resolves the named account to
// its configured IMAP credentials and verifies connect+login without fetching
// mail. Returns nil for send-only accounts (no IMAP host) so they don't block.
type imapProber struct{}

func (imapProber) Probe(account string) error {
	return builtin.ProbeAccountIMAP(account)
}

// ExpertSessionMeta holds metadata for an expert collaboration session.
type ExpertSessionMeta struct {
	TeamID   string `json:"teamId"`
	TeamName string `json:"teamName"`
}

type schedulerIMPusher struct{ app *App }

func (p schedulerIMPusher) Push(ctx context.Context, dest, text string) error {
	gw := p.app.botGW.Load()
	if gw == nil {
		// Signal "offline" so the scheduler records a skipped delivery with a
		// clear reason, rather than silently reporting success. The user then
		// sees "bot 未启动" and knows to start the bot.
		return schedulerpkg.ErrIMOffline
	}
	return gw.Push(ctx, dest, text)
}

// schedulerRunner implements scheduler.Runner by running a prompt in a tab of
// the task's profile. A scheduled task fires headlessly into the agent loop;
// the result text (or error) is returned to the scheduler, which stores it on
// the task for schedule_list inspection. The profiles are independent
// surfaces: the interactive path only uses a tab whose profile MATCHES the
// task's (a cowork prompt must not execute inside a netdev-sealed or dev tab),
// preferring the active tab, then any ready tab of that profile.
type schedulerRunner struct{ app *App }

// scheduledTargetTabLocked returns the tab a scheduled task of the given profile
// should run in: the active tab when it belongs to the task's profile, else any
// ready tab of that profile. The profiles are independent surfaces — a cowork
// task must never execute inside a dev or netdev (sealed) tab, and vice versa.
// Callers must hold a.mu (a read lock is enough).
func scheduledTargetTabLocked(a *App, profile string) *WorkspaceTab {
	want := normalizeProfileName(profile)
	if tab := a.tabs[a.activeTabID]; tab != nil && tab.Ctrl != nil && tab.Ready && normalizeProfileName(tab.profile) == want {
		return tab
	}
	for _, t := range a.tabs {
		if t.Ctrl != nil && t.Ready && normalizeProfileName(t.profile) == want {
			return t
		}
	}
	return nil
}

func (r schedulerRunner) Run(ctx context.Context, profile, prompt string) (string, error) {
	r.app.mu.RLock()
	tab := scheduledTargetTabLocked(r.app, profile)
	r.app.mu.RUnlock()
	if tab != nil {
		return runScheduledPrompt(ctx, tab.Ctrl, prompt)
	}
	// No usable tab of the task's profile — previously this returned an error
	// and the scheduled task silently failed (scheduler.runOne records the
	// error but does not retry, so the prompt was lost). The scheduler package
	// comment explicitly promises tasks "must fire even when no chat tab is
	// open"; build a temporary headless controller in the task's profile so the
	// task still runs. See audit finding C7.
	//
	// Headless mode is STRICTER than interactive: boot installs a headless
	// permission gate (deny-by-default for unconfigured writers), so email_send
	// and other irreversible ops fail closed (no UI to ask) rather than silently
	// proceeding. The A1/A2/A3 policy hardening we just landed applies here too.
	return r.app.runHeadlessScheduled(ctx, profile, prompt)
}

// runScheduledPrompt runs a prompt on the given controller and returns a short
// summary of the result (the last assistant message text), shared by the
// active-tab and headless paths.
func runScheduledPrompt(ctx context.Context, ctrl tabSession, prompt string) (string, error) {
	if err := ctrl.Run(ctx, prompt); err != nil {
		return "", err
	}
	hist := ctrl.History()
	if len(hist) == 0 {
		return "ran (no output)", nil
	}
	last := hist[len(hist)-1]
	if txt := assistantText(last); txt != "" {
		return txt, nil
	}
	return "ran", nil
}

// runHeadlessScheduled builds a throwaway headless cowork controller, runs the
// prompt, then tears the controller down (releasing the shared plugin host).
// WorkspaceRoot is the cowork home project 工作台 since scheduled tasks are not
// bound to a specific project. See C7.
func (a *App) runHeadlessScheduled(ctx context.Context, profileName, prompt string) (string, error) {
	root := ensureProfileHomeRoot(config.ProfileCowork)
	cfg, err := config.LoadForRoot(root)
	if err != nil {
		return "", fmt.Errorf("scheduled task: load config: %w", err)
	}
	// Resolve the product profile (default cowork for scheduled tasks). Empty
	// or "dev" yields a nil profile → unprofiled coding behaviour; any other
	// name resolves against config + builtins, mirroring tabs.go.
	var profile *config.Profile
	name := strings.TrimSpace(profileName)
	if name == "" {
		name = config.ProfileCowork
	}
	if !strings.EqualFold(name, config.ProfileDev) {
		if p, perr := cfg.ResolveProfile(name); perr == nil {
			profile = p
		}
	}
	sharedHost := a.acquireSharedHost(root)
	ctrl, err := boot.Build(a.bootContext(), boot.Options{
		Model:         "", // config default_model
		RequireKey:    false,
		WorkspaceRoot: root,
		Host:          sharedHost,
		Profile:       profile,
	})
	if err != nil {
		a.releaseSharedHost(root) // Build failed: drop the acquire
		return "", fmt.Errorf("scheduled task: build headless controller: %w", err)
	}
	// Headless gate is the boot default (no EnableInteractiveApproval), so writes
	// and irreversible ops resolve via config policy — fail-closed for anything
	// not explicitly allowed. Do NOT enable plan/goal (no UI to exit them).
	defer func() {
		ctrl.Close()
		a.releaseSharedHost(root)
	}()
	return runScheduledPrompt(ctx, ctrl, prompt)
}

// assistantText extracts the text content of an assistant message (empty for
// non-assistant or tool-call-only messages). Used by the scheduler to
// summarize a fired prompt's result.
func assistantText(m provider.Message) string {
	if m.Role != provider.RoleAssistant {
		return ""
	}
	return strings.TrimSpace(m.Content)
}

func (a *App) beforeClose(ctx context.Context) bool {
	if a.forceQuit.Swap(false) || consumeSystemQuitRequested() {
		return false
	}
	cfg, _, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		cfg = config.LoadForEdit(config.UserConfigPath())
	}
	if cfg.DesktopCloseBehavior() == "background" {
		a.backgroundMaximised.Store(runtime.WindowIsMaximised(ctx))
		a.saveWindowStateSync()
		a.snapshotAllTabs()
		hideForBackground(ctx)
		return true
	}
	return false
}

func (a *App) showMainWindow() {
	if a.ctx != nil {
		showFromBackground(a.ctx, a.backgroundMaximised.Swap(false))
	}
}

func (a *App) secondInstanceLaunch() {
	a.showMainWindow()
}

func (a *App) quitApp() {
	if a.ctx == nil {
		return
	}
	a.forceQuit.Store(true)
	runtime.Quit(a.ctx)
}

func hideForBackground(ctx context.Context) {
	if backgroundCloseUsesApplicationHide(goruntime.GOOS) {
		runtime.Hide(ctx)
		return
	}
	runtime.WindowHide(ctx)
}

func showFromBackground(ctx context.Context, wasMaximised bool) {
	if backgroundCloseUsesApplicationHide(goruntime.GOOS) {
		runtime.Show(ctx)
	}
	plan := backgroundRestorePlanFor(goruntime.GOOS, wasMaximised)
	if plan.maximiseBeforeShow {
		runtime.WindowMaximise(ctx)
	}
	runtime.WindowShow(ctx)
	if plan.unminimiseAfterShow {
		runtime.WindowUnminimise(ctx)
	}
}

func backgroundCloseUsesApplicationHide(goos string) bool {
	return goos == "darwin"
}

type backgroundRestorePlan struct {
	maximiseBeforeShow  bool
	unminimiseAfterShow bool
}

func backgroundRestorePlanFor(goos string, wasMaximised bool) backgroundRestorePlan {
	if backgroundRestoreShouldMaximise(goos, wasMaximised) {
		return backgroundRestorePlan{maximiseBeforeShow: true}
	}
	return backgroundRestorePlan{unminimiseAfterShow: true}
}

func backgroundRestoreShouldMaximise(goos string, wasMaximised bool) bool {
	return wasMaximised && !backgroundCloseUsesApplicationHide(goos)
}

// restoreOrBuildTabs restores the tabs from the last session, or creates a
// default Global tab on first launch.
func (a *App) restoreOrBuildTabs() {
	ctx := a.ctx
	ensureWorkspace()

	// Load i18n from the first available config.
	// Prefer DesktopLanguage (desktop UI setting) over Language (CLI setting),
	// so the user's language choice in desktop settings takes effect.
	if cfg, err := config.Load(); err == nil {
		lang := cfg.DesktopLanguage()
		if lang == "" {
			lang = cfg.Language
		}
		i18n.DetectLanguage(lang)
	}

	// Global scope retired (2026-08-21): fold every profile's remaining global
	// topics into its home project 工作台 before restoring tabs, so restored
	// global entries reopen as home-project tabs.
	for _, key := range []string{config.ProfileDev, config.ProfileCowork, config.ProfileNetDev} {
		migrateGlobalIntoHome(key)
	}
	// Strict profile isolation: drop project entries a foreign profile
	// acquired before the guard existed (mode switching once carried the
	// coding workspace into the ops index).
	pruneForeignProjects()
	// Retire the pre-profile shared workspace list into dev — per-profile
	// project generation reads nothing shared afterwards.
	migrateLegacyWorkspacesIntoDevOnce()

	f := loadTabsFile()
	if len(f.Tabs) > 0 {
		toBuild := make([]*WorkspaceTab, 0, len(f.Tabs))
		for _, entry := range f.Tabs {
			a.mu.Lock()
			id := a.restoredTabIDLocked(entry.ID)
			a.mu.Unlock()

			var tab *WorkspaceTab
			if entry.Remote != nil {
				// Remote-workspace tab: root and session path are remote-side
				// strings; the controller attaches over the host link at build.
				tab = a.createTabEntryWithID("project", entry.WorkspaceRoot, entry.Profile, entry.TopicID, id)
				tab.Remote = entry.Remote
			} else if entry.IsExpertSession {
				// Expert-session tabs are their own scope; restore verbatim.
				tab = a.createTabEntryWithID("expert", "", entry.Profile, "", id)
			} else if entry.Scope == "project" && !rootOwnedByOtherProfile(entry.WorkspaceRoot, entry.Profile) {
				tab = a.createTabEntryWithID(entry.Scope, entry.WorkspaceRoot, entry.Profile, entry.TopicID, id)
			} else if entry.Scope == "project" {
				// Foreign-root tab (pre-isolation carry-over): re-land on this
				// tab's profile home as a blank tab; the root stays with its
				// owning profile.
				tab = a.createTabEntryWithID("project", ensureProfileHomeRoot(entry.Profile), entry.Profile, "", id)
			} else {
				// Retired global tabs reopen on the profile's home project;
				// the home root resolves to the former global session partition.
				tab = a.createTabEntryWithID("project", ensureProfileHomeRoot(entry.Profile), entry.Profile, entry.TopicID, id)
			}
			tab.model = entry.Model
			tab.effort = cloneStringPtr(entry.Effort)
			tab.mode = persistedTabMode(entry.Mode)
			tab.goal = strings.TrimSpace(entry.Goal)
			tab.toolApprovalMode = normalizeToolApprovalMode(entry.ToolApprovalMode)
			if tab.toolApprovalMode == control.ToolApprovalAsk && tabModeHasAutoApproveTools(entry.Mode) {
				tab.toolApprovalMode = control.ToolApprovalYolo
			}
			tab.ragScope = strings.TrimSpace(entry.RagScope)
			tab.profile = strings.TrimSpace(entry.Profile)
			tab.SessionPath = strings.TrimSpace(entry.SessionPath)
			tab.IsExpertSession = entry.IsExpertSession
			tab.ExpertTeamID = strings.TrimSpace(entry.ExpertTeamID)
			tab.ExpertTeamName = strings.TrimSpace(entry.ExpertTeamName)
			tab.sink = &tabEventSink{tabID: tab.ID, app: a, ctx: ctx}
			a.mu.Lock()
			a.tabs[tab.ID] = tab
			a.tabOrder = append(a.tabOrder, tab.ID)
			a.mu.Unlock()
			toBuild = append(toBuild, tab)
		}
		a.mu.Lock()
		if _, ok := a.tabs[f.ActiveTab]; ok {
			a.activeTabID = f.ActiveTab
		} else {
			ordered := a.orderedTabIDsLocked()
			if len(ordered) > 0 {
				a.activeTabID = ordered[0]
			}
		}
		a.mu.Unlock()
		for _, tab := range toBuild {
			a.startTabControllerBuild(tab)
		}
		return
	}

	// First launch: open the dev home project 工作台 (the global scope's
	// replacement) so there is always a concrete landing workspace.
	tab := a.createTabEntry("project", ensureProfileHomeRoot(""), "", "")
	tab.sink = &tabEventSink{tabID: tab.ID, app: a, ctx: ctx}
	tab.TopicTitle = homeProjectTitle
	addProjectTitled(tab.WorkspaceRoot, "")
	a.mu.Lock()
	a.tabs[tab.ID] = tab
	a.tabOrder = append(a.tabOrder, tab.ID)
	a.activeTabID = tab.ID
	a.mu.Unlock()
	a.startTabControllerBuild(tab)
}

func (a *App) createTabEntry(scope, workspaceRoot, profile, topicID string) *WorkspaceTab {
	return a.createTabEntryWithID(scope, workspaceRoot, profile, topicID, newTabID())
}

func (a *App) createTabEntryWithID(scope, workspaceRoot, profile, topicID, id string) *WorkspaceTab {
	return &WorkspaceTab{
		ID:               id,
		Scope:            scope,
		WorkspaceRoot:    workspaceRoot,
		TopicID:          topicID,
		TopicTitle:       topicTitleForTab(scope, workspaceRoot, topicID),
		profile:          normalizeProfileName(profile),
		mode:             "normal",
		toolApprovalMode: control.ToolApprovalAsk,
		disabledMCP:      map[string]ServerView{},
	}
}

func (a *App) snapshotAllTabs() {
	a.mu.RLock()
	tabs := make([]*WorkspaceTab, 0, len(a.tabs))
	for _, t := range a.tabs {
		tabs = append(tabs, t)
	}
	a.mu.RUnlock()
	for _, t := range tabs {
		if t.Ctrl != nil {
			_ = t.Ctrl.Snapshot()
		}
	}
}

// shutdown snapshots all tabs, saves the final window geometry, and closes tabs.
func (a *App) shutdown(context.Context) {
	a.stopBotGateway()
	a.stopTray()
	// Tear down remote host processes (WSL/Docker/SSH/Server links).
	if m := a.remoteManager; m != nil {
		m.closeAll()
	}
	// Stop the screenshot hotkey loop so its goroutine exits cleanly instead of
	// leaking (it now uses non-blocking PeekMessage + stopCh, so this returns
	// within one tick).
	a.StopScreenshotHotkey()
	// Stop the emergency-stop hotkey loop likewise.
	a.StopEStopHotkey()
	// Stop the RAG extraction pipeline so its worker goroutines exit cleanly
	// (pending chunks are durable — Resume() rehydrates them on next launch).
	if a.ragPipeline != nil {
		a.ragPipeline.Stop()
	}
	// Kill the Hyper-Extract Python subprocess so it doesn't leak as an orphan
	// when the app exits (Windows especially leaves it running otherwise).
	if a.heService != nil {
		a.heService.Stop()
	}
	// Stop the browser-use sidecar so it doesn't leak as an orphan on exit.
	if a.buService != nil {
		a.buService.Stop()
	}
	// Save window geometry synchronously from Go so it's persisted even if the
	// frontend's beforeunload promise hasn't resolved yet.
	a.saveWindowStateSync()

	a.mu.RLock()
	tabs := make([]*WorkspaceTab, 0, len(a.tabs))
	for _, t := range a.tabs {
		tabs = append(tabs, t)
	}
	a.mu.RUnlock()
	for _, t := range tabs {
		if t.Ctrl != nil {
			// Cancel a still-running turn and let its goroutine finish before
			// the closing snapshot — otherwise Close's autosave wait eats its
			// full 5s timeout per tab (the ticker never exits uncancelled) and
			// any step completing after Snapshot is lost.
			if t.Ctrl.Running() {
				t.Ctrl.Cancel()
				t.Ctrl.WaitTurn(3 * time.Second)
			}
			_ = t.Ctrl.Snapshot()
			t.Ctrl.Close()
			a.releaseSharedHost(t.WorkspaceRoot)
		}
	}
}

// domReady is called (via OnDomReady) after the webview finishes loading its DOM
// but before the window is shown (StartHidden). It restores the saved window
// position and size, then calls WindowShow so the user never sees the default
// size/position flash.
func (a *App) domReady(_ context.Context) {
	state, ok := loadWindowState()
	if ok {
		// Validate saved position against current screens. Wails v2 doesn't
		// expose per-screen origin (x,y offsets) so we can only do a basic
		// sanity check: ensure the window origin falls within a generous
		// estimate of the screen area. If the user unplugged an external
		// display, negative or out-of-bounds coordinates are caught here.
		valid := state.X >= 0 && state.Y >= 0
		if valid {
			screens, err := runtime.ScreenGetAll(a.ctx)
			if err == nil && len(screens) > 0 {
				maxW, maxH := 0, 0
				for _, sc := range screens {
					if sc.Size.Width > maxW {
						maxW = sc.Size.Width
					}
					if sc.Size.Height > maxH {
						maxH = sc.Size.Height
					}
				}
				if state.X > maxW*2 || state.Y > maxH*2 {
					valid = false
				}
			}
		}
		if valid {
			runtime.WindowSetPosition(a.ctx, state.X, state.Y)
		} else {
			runtime.WindowCenter(a.ctx)
		}
	} else {
		runtime.WindowCenter(a.ctx)
	}

	if ok && state.Maximised {
		runtime.WindowMaximise(a.ctx)
	}

	runtime.WindowShow(a.ctx)
}

// --- bound command surface (frontend → controller) ---
// Each method guards on a nil controller so a pre-startup or failed-build call is
// a no-op, never a panic.

// Submit runs raw user input as a turn; slash commands and @-references are
// resolved by the controller. Output arrives asynchronously on eventChannel.
func (a *App) Submit(input string) {
	a.SubmitToTab("", input)
}

func (a *App) SubmitToTab(tabID, input string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "/effort" || strings.HasPrefix(trimmed, "/effort ") {
		a.runEffortCommandForTab(tabID, trimmed)
		return
	}
	ctrl := a.ctrlByTabID(tabID)
	if ctrl == nil {
		// Controller 可能还没构建（重启后异步构建未完成/失败）。同步重建一次。
		// mobilebridge 的 submit 在独立 goroutine 里，阻塞在此可接受。
		slog.Info("SubmitToTab: controller missing, rebuilding", "tabID", tabID)
		if tab := a.tabByID(tabID); tab != nil {
			a.buildTabController(tab)
			ctrl = a.ctrlByTabID(tabID)
		}
	}
	if ctrl == nil {
		slog.Warn("SubmitToTab: controller still nil after rebuild — message dropped",
			"tabID", tabID, "activeTab", a.ActiveTabID())
		return
	}
	// If the user attached a reference image/PDF with PPT-making intent, run the
	// VLM gatekeeper NOW (synchronously) so reference-style.json / page-N.json is
	// ready by the time the controller dispatches to ppt-auto (which reads them in
	// Image→PPT gatekeeper. Called from BOTH submit entry points (see
	// mayPreparePPTReference) — the frontend routes here via SubmitToTab OR
	// SubmitDisplayToTab (the latter when display≠submit, i.e. exactly when an
	// attachment @ref is present). Best-effort; never blocks the message. The
	// returned input may carry a system note (PDF per-page reference ready) —
	// dispatched to the model but kept out of the display copy.
	input = a.mayPreparePPTReference(input)
	ctrl.SubmitDisplay(input, input)
	slog.Info("SubmitToTab: dispatched to controller", "tabID", tabID, "inputLen", len(input))
}

// mayPreparePPTReference is the image→PPT gatekeeper. Called from BOTH submit
// entry points: SubmitToTab (display==submit) and SubmitDisplayToTab (display≠
// submit, which is EXACTLY when there's an attachment @ref — the case we must
// catch). It detects a PPT reference — an uploaded attachment token OR an
// absolute local path pasted as text — runs PreparePPTReference synchronously
// (VLM classify → vision flow), and writes reference-style.json / page-N.json
// before the message reaches the model. See ppt-vision-enhancement-spec §六.五.
// Best-effort: any failure logs, emits a ppt:reference-warning so the user KNOWS
// the reference was dropped (silent degradation used to ship decks that ignored
// the reference with no explanation), and degrades to normal topic-driven flow.
//
// Returns the input to dispatch: when a VISUAL PDF reference was analyzed it is
// annotated with a system note (model-facing only — call sites keep the display
// copy clean) steering the main model to hand the per-page reference to ppt-auto
// instead of improvising its own outline from raw text extraction, which
// flattens tables — the exact failure seen on a real 32-page PDF test where the
// deck came out table-less because the model never touched pdf-pages.
func (a *App) mayPreparePPTReference(input string) string {
	if !hasPPTIntent(strings.ToLower(input)) {
		return input
	}
	// Reference form 1: an uploaded attachment token. Form 2: an absolute local
	// path typed/pasted into the message (e.g. C:\Users\me\Desktop\shot.png) —
	// both route to the same PreparePPTReference.
	refPath, refMatched := pptReferenceAttachment(input)
	missingPath := ""
	if !refMatched {
		if lp, miss, ok := localPathReference(input); ok {
			refPath, refMatched = lp, true
		} else {
			missingPath = miss
		}
	}
	head := input
	if len(head) > 120 {
		head = head[:120] + "..."
	}
	pptVisionDebugLog("trigger check: matched=%v refPath=%q missing=%q inputHead=%q", refMatched, refPath, missingPath, head)
	if !refMatched {
		if missingPath != "" {
			runtime.EventsEmit(a.ctx, "ppt:reference-warning", map[string]string{
				"reason": "path_not_found",
				"hint":   fmt.Sprintf("引用的参考文件不存在：%s — 请检查路径，或直接把图片拖入对话上传。", missingPath),
			})
		}
		// PPT task WITHOUT a reference: leftovers from a previous task would
		// silently hijack this run (pdf-pages/page-N.json even overrides the
		// deck's page count per SKILL.md Step 3). Enforce the invariant:
		// those files exist ⟺ the current task provided a reference.
		clearStaleReferenceFiles()
		return input
	}
	if err := a.withActiveWorkspaceDo(func() error {
		res, perr := a.PreparePPTReference(refPath)
		vErr := ""
		pages, total := 0, 0
		if res != nil {
			vErr = res.VisionError
			pages = res.PDFPages
			total = res.PDFTotalPages
		}
		pptVisionDebugLog("PreparePPTReference: err=%v isVisual=%v needsVLM=%v visionErr=%q pdfPages=%d/%d",
			perr, res != nil && res.IsVisual, res != nil && res.NeedsVLMConfig, vErr, pages, total)
		switch {
		case perr == nil && res != nil && res.NeedsVLMConfig:
			runtime.EventsEmit(a.ctx, "ppt:reference-warning", map[string]string{
				"reason": "no_vlm_model",
				"hint":   "未配置视觉模型(VLM)，无法分析参考图样式，已用普通流程生成。请在设置里配置 VLM。",
			})
			slog.Warn("mayPreparePPTReference: VLM not configured — reference image not analyzed, degrading to normal flow")
		case perr != nil:
			// The classify call itself failed (timeout / provider error) — the
			// whole pre-analysis is skipped, so the user must hear about it.
			runtime.EventsEmit(a.ctx, "ppt:reference-warning", map[string]string{
				"reason": "classify_failed",
				"hint":   fmt.Sprintf("参考图分析失败：%v — 已按普通流程生成，参考图样式未被应用。", perr),
			})
			slog.Warn("mayPreparePPTReference: classify failed (degrading to normal flow)", "err", perr)
		}
		if perr == nil && res != nil {
			if vErr != "" {
				runtime.EventsEmit(a.ctx, "ppt:reference-warning", map[string]string{
					"reason": "vision_error",
					"hint":   fmt.Sprintf("参考图视觉分析部分失败：%s — 已按普通流程降级生成。", vErr),
				})
			}
			if total > pages && pages > 0 {
				runtime.EventsEmit(a.ctx, "ppt:reference-warning", map[string]string{
					"reason": "pdf_truncated",
					"hint":   fmt.Sprintf("PDF 共 %d 页，仅分析了前 %d 页，其余页面将按普通方式生成。", total, pages),
				})
			}
			// Surface the verdict so the user can catch a misroute ("it judged
			// plain text but I wanted the layout") while it's still cheap to
			// correct — human confirmation beats the code guessing intent.
			runtime.EventsEmit(a.ctx, "ppt:reference-analyzed", map[string]any{
				"verdict":   res.Verdict,
				"is_visual": res.IsVisual,
				"reason":    res.Reason,
				"pdf_pages": pages,
			})
		}
		// Steer the main model: the per-page visual reference is on disk, so
		// relay it to ppt-auto rather than re-deriving an outline from text
		// extraction (which flattens tables — the 32-page PDF failure mode).
		if pages > 0 {
			input += "\n\n[system] 此 PDF 已完成逐页视觉分析，结果在 ~/.fairpeer/pdf-pages/page-N.json（每页含内容/布局/表格行列/风格描述）。该 PDF 的规范绝对路径记录在 ~/.fairpeer/reference-style.json 的 source_path 字段——生成 PPT 时请把该路径附在任务参数中，并指示 ppt-auto 读取 pdf-pages 逐页重绘、用其 scripts/analyze_pdf_pages.py 分批补齐未分析的页（每次至多 8 页、被超时打断后重跑即可续传，直到 remaining=0）、总页数以 PDF 总页数为准。不要自行提取 PDF 文字编写大纲——纯文字提取会丢失表格结构。"
		}
		return nil
	}); err != nil {
		pptVisionDebugLog("withActiveWorkspaceDo err: %v", err)
		slog.Warn("mayPreparePPTReference: PreparePPTReference failed (degrading to normal flow)", "err", err)
	}
	return input
}

// SessionListForMobile returns all tabs as SessionInfo for linkpeer's session
// drawer (the list_sessions command reply).
func (a *App) SessionListForMobile() []mobilebridge.SessionInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]mobilebridge.SessionInfo, 0, len(a.tabOrder))
	for _, id := range a.tabOrder {
		if t := a.tabs[id]; t != nil {
			title := t.TopicTitle
			if strings.TrimSpace(title) == "" {
				title = t.Label
			}
			if strings.TrimSpace(title) == "" {
				title = "会话"
			}
			out = append(out, mobilebridge.SessionInfo{Path: id, Title: title})
		}
	}
	return out
}

// ModelsForMobile returns the model list for linkpeer's model switcher
// (the list_models command reply).
func (a *App) ModelsForMobile() []mobilebridge.ModelInfo {
	ms := a.Models()
	out := make([]mobilebridge.ModelInfo, 0, len(ms))
	for _, m := range ms {
		out = append(out, mobilebridge.ModelInfo{ID: m.Ref, Label: m.Model})
	}
	return out
}

// RenameTabForMobile 改 tab 的显示标题（移动端 rename_session）。
func (a *App) RenameTabForMobile(tabID, title string) {
	a.mu.Lock()
	if t := a.tabs[tabID]; t != nil {
		t.TopicTitle = title
		a.saveTabsLocked()
	}
	a.mu.Unlock()
	a.notifyMobileSessionList()
}

// notifyMobileSessionList 通知所有在线 linkpeer：会话列表变了，刷新（方案B）。
func (a *App) notifyMobileSessionList() {
	if mb := a.mobilebridge.Load(); mb != nil {
		mb.NotifySessionListChanged()
	}
}

// RunShell executes a shell command directly (bypassing the model) and streams
// output as events on eventChannel.
func (a *App) RunShell(command string) {
	a.RunShellForTab("", command)
}

func (a *App) RunShellForTab(tabID, command string) {
	if ctrl := a.ctrlByTabID(tabID); ctrl != nil {
		ctrl.RunShell(command)
	}
}

// SubmitDisplay runs input as a turn while recording a shorter UI-only display
// string for the saved desktop transcript. The model still receives input.
func (a *App) SubmitDisplay(display, input string) {
	a.SubmitDisplayToTab("", display, input)
}

func (a *App) SubmitDisplayToTab(tabID, display, input string) {
	// Image→PPT gatekeeper — MUST run here too. The frontend routes to
	// SubmitDisplayToTab (not SubmitToTab) exactly when display≠submit, i.e. when
	// an attachment @ref is present — which is precisely the case we must catch.
	// Without this, image references bypassed the gatekeeper entirely (the bug
	// that caused the first test runs to hallucinate). See mayPreparePPTReference.
	// Returns the input to dispatch — possibly annotated with a system note
	// (PDF per-page reference ready); `display` stays untouched.
	input = a.mayPreparePPTReference(input)
	ctrl := a.ctrlByTabID(tabID)
	if ctrl == nil {
		return
	}
	ctrl.SubmitDisplay(display, input)
}

func (a *App) bindControllerDisplayRecorder(ctrl *control.Controller) {
	if ctrl == nil {
		return
	}
	ctrl.SetDisplayRecorder(func(content, display string) {
		dir := ctrl.SessionDir()
		if dir == "" {
			dir = config.SessionDir()
		}
		_ = recordSessionDisplay(dir, ctrl.SessionPath(), content, display)
	})
}

// bindControllerContextFilter wires the expert-collab context projection onto a
// freshly built controller, so a full-fidelity expert-collab message in the
// transcript is shown to the model as its synthesis-only summary (keeping the
// multi-expert transcript from bloating the context window). Called at every
// controller build alongside bindControllerDisplayRecorder.
func (a *App) bindControllerContextFilter(ctrl *control.Controller) {
	if ctrl == nil {
		return
	}
	ctrl.SetContextFilter(expertspkg.CollabContextMessages)
}

// Cancel aborts the in-flight turn.
func (a *App) Cancel() {
	a.CancelTab("")
}

func (a *App) CancelTab(tabID string) {
	if ctrl := a.ctrlByTabID(tabID); ctrl != nil {
		ctrl.Cancel()
	}
}

// Steer sends mid-turn guidance to the agent without interrupting the in-flight request.
func (a *App) Steer(text string) {
	a.SteerForTab("", text)
}

// SteerForTab sends mid-turn guidance to a specific tab's agent.
func (a *App) SteerForTab(tabID, text string) {
	if ctrl := a.ctrlByTabID(tabID); ctrl != nil {
		ctrl.Steer(text)
	}
}

// FollowUp queues a prompt to run as an independent next turn once the active
// tab's current turn finishes (upgrade spec 2-4); when idle it runs now.
func (a *App) FollowUp(text string) {
	a.FollowUpForTab("", text)
}

// FollowUpForTab queues a follow-up prompt on a specific tab.
func (a *App) FollowUpForTab(tabID, text string) {
	if ctrl := a.ctrlByTabID(tabID); ctrl != nil {
		ctrl.FollowUp(text)
	}
}

// QueuedMessagesView is the wire form of the live steer/follow-up queues.
type QueuedMessagesView struct {
	Steer    []string `json:"steer,omitempty"`
	FollowUp []string `json:"followUp,omitempty"`
}

// QueuedMessages reports the active tab's queued mid-turn guidance and
// follow-up prompts for the composer queue strip (upgrade spec 2-3/2-4).
func (a *App) QueuedMessages() QueuedMessagesView {
	a.mu.RLock()
	tab := a.tabByIDLocked("")
	a.mu.RUnlock()
	if tab == nil || tab.Ctrl == nil {
		return QueuedMessagesView{}
	}
	s, f := tab.Ctrl.QueuedMessages()
	return QueuedMessagesView{Steer: s, FollowUp: f}
}

// Pause requests a graceful pause of the active tab's in-flight turn. The agent
// finishes its current step, then freezes with full state preserved. Contrast
// Cancel, which aborts and discards partial work. No-op when nothing is running.
func (a *App) Pause() {
	a.PauseTab("")
}

// PauseTab requests a graceful pause on a specific tab.
func (a *App) PauseTab(tabID string) {
	if ctrl := a.ctrlByTabID(tabID); ctrl != nil {
		ctrl.Pause()
	}
}

// ResumeTurn unblocks a paused turn on the active tab. No-op when not paused.
func (a *App) ResumeTurn() {
	a.ResumeTurnTab("")
}

// ResumeTurnTab unblocks a paused turn on a specific tab.
func (a *App) ResumeTurnTab(tabID string) {
	if ctrl := a.ctrlByTabID(tabID); ctrl != nil {
		ctrl.ResumeTurn()
	}
}

// PausedTab reports whether the given tab's turn is frozen on a graceful pause.
func (a *App) PausedTab(tabID string) bool {
	ctrl := a.ctrlByTabID(tabID)
	if ctrl == nil {
		return false
	}
	return ctrl.Paused()
}

// Approve answers a pending approval_request by ID: allow runs the call, session
// also remembers the grant for the rest of the session.
func (a *App) Approve(id string, allow, session, persist bool) {
	ctrl := a.ctrlByTabID("")
	if ctrl != nil {
		ctrl.Approve(id, allow, session, persist)
	}
}

// ApproveTab is like Approve but scoped to a specific tab.
func (a *App) ApproveTab(tabID, id string, allow, session, persist bool) {
	ctrl := a.ctrlByTabID(tabID)
	if ctrl != nil {
		ctrl.Approve(id, allow, session, persist)
	}
}

// ReplayPendingPrompts asks every tab's controller to re-emit any approval/ask
// prompt that is currently blocking its run loop. The frontend calls this once
// its event subscription is live (on load/reconnect) so a session that was
// already awaiting confirmation rebuilds its modal instead of showing a
// "waiting" status with no way to answer — and no way to stop.
func (a *App) ReplayPendingPrompts() {
	a.mu.RLock()
	tabs := make([]*WorkspaceTab, 0, len(a.tabs))
	for _, t := range a.tabs {
		tabs = append(tabs, t)
	}
	a.mu.RUnlock()
	for _, t := range tabs {
		if t.Ctrl != nil {
			t.Ctrl.ReplayPendingPrompts()
		}
	}
}

// SetPlanMode toggles the read-only plan axis while preserving the current
// tool-auto-approval axis.
func (a *App) SetPlanMode(on bool) {
	a.setPlanModeForTab("", on)
}

func (a *App) setPlanModeForTab(tabID string, on bool) {
	current := a.currentModeForTab(tabID)
	a.SetModeForTab(tabID, tabModeFromAxes(on, tabModeHasAutoApproveTools(current)))
}

// SetMode applies a composer gating mode ("plan" | "yolo" | "plan-yolo" |
// anything else =
// normal) in one call, so a turn submitted right after the switch can't race a
// half-applied plan/tool-auto-approval pair.
func (a *App) SetMode(mode string) {
	a.SetModeForTab("", mode)
}

func (a *App) SetModeForTab(tabID, mode string) {
	normalized := normalizeTabMode(mode)
	a.mu.Lock()
	tab := a.tabByIDLocked(tabID)
	if tab == nil {
		a.mu.Unlock()
		return
	}
	tab.mode = normalized
	tab.toolApprovalMode = normalizeToolApprovalMode(tab.toolApprovalMode)
	if tabModeHasAutoApproveTools(normalized) {
		tab.toolApprovalMode = control.ToolApprovalYolo
	} else if tab.toolApprovalMode == control.ToolApprovalYolo {
		tab.toolApprovalMode = control.ToolApprovalAsk
	}
	ctrl := tab.Ctrl
	approvalMode := tab.toolApprovalMode
	tabIDForSave := tab.ID
	a.mu.Unlock()
	applyTabModeToController(ctrl, normalized)
	applyTabToolApprovalModeToController(ctrl, approvalMode)
	a.mu.Lock()
	if a.tabs[tabIDForSave] == tab {
		a.saveTabsLocked()
	}
	a.mu.Unlock()
}

func applyTabModeToController(ctrl tabSession, mode string) {
	if ctrl == nil {
		return
	}
	switch normalizeTabMode(mode) {
	case "plan":
		ctrl.SetMode(true, false)
	case "yolo":
		ctrl.SetMode(false, true)
	case "plan-yolo":
		ctrl.SetMode(true, true)
	default:
		ctrl.SetMode(false, false)
	}
}

func applyTabToolApprovalModeToController(ctrl tabSession, mode string) {
	if ctrl == nil {
		return
	}
	ctrl.SetToolApprovalMode(normalizeToolApprovalMode(mode))
}

// applyTabRagScopeToController pushes the tab's knowledge-base auto-injection
// scope onto a (re)built controller. Mirrors applyTabToolApprovalModeToController.
func applyTabRagScopeToController(ctrl tabSession, scope string) {
	if ctrl == nil {
		return
	}
	ctrl.SetRAGScope(scope)
}

func (a *App) currentModeForTab(tabID string) string {
	a.mu.RLock()
	tab := a.tabByIDLocked(tabID)
	mode := "normal"
	if tab != nil {
		mode = currentTabMode(tab)
	}
	a.mu.RUnlock()
	return mode
}

func normalizeCollaborationMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "plan":
		return "plan"
	case "goal":
		return "goal"
	default:
		return "normal"
	}
}

func (a *App) SetCollaborationMode(mode string) {
	a.SetCollaborationModeForTab("", mode)
}

func (a *App) SetCollaborationModeForTab(tabID, mode string) {
	mode = normalizeCollaborationMode(mode)
	a.mu.Lock()
	tab := a.tabByIDLocked(tabID)
	if tab == nil {
		a.mu.Unlock()
		return
	}
	approvalMode := currentTabToolApprovalMode(tab)
	switch mode {
	case "plan":
		tab.mode = tabModeFromAxes(true, approvalMode == control.ToolApprovalYolo)
		tab.goal = ""
	case "goal":
		tab.mode = tabModeFromAxes(false, approvalMode == control.ToolApprovalYolo)
	default:
		tab.mode = tabModeFromAxes(false, approvalMode == control.ToolApprovalYolo)
		tab.goal = ""
	}
	ctrl := tab.Ctrl
	goal := tab.goal
	plan := tabModeHasPlan(tab.mode)
	tabIDForSave := tab.ID
	a.mu.Unlock()
	if ctrl != nil {
		ctrl.SetPlanMode(plan)
		ctrl.SetGoal(goal)
	}
	a.mu.Lock()
	if a.tabs[tabIDForSave] == tab {
		a.saveTabsLocked()
	}
	a.mu.Unlock()
}

// QuestionAnswer is the frontend's reply to one question in an ask_request.
type QuestionAnswer struct {
	QuestionID string   `json:"questionId"`
	Selected   []string `json:"selected"`
}

// AnswerQuestion resolves a pending ask_request (the `ask` tool) by ID with the
// user's selections per question.
func (a *App) AnswerQuestion(id string, answers []QuestionAnswer) {
	a.AnswerQuestionForTab("", id, answers)
}

func (a *App) AnswerQuestionForTab(tabID, id string, answers []QuestionAnswer) {
	ctrl := a.ctrlByTabID(tabID)
	if ctrl == nil {
		return
	}
	out := make([]event.AskAnswer, len(answers))
	for i, an := range answers {
		out[i] = event.AskAnswer{QuestionID: an.QuestionID, Selected: an.Selected}
	}
	ctrl.AnswerQuestion(id, out)
}

// Compact runs one compaction pass on demand.
// Compact runs a plain compaction pass (the "compact now" button). Focus-guided
// compaction goes through Submit("/compact <focus>") instead.
func (a *App) Compact() error {
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return nil
	}
	return ctrl.Compact(a.ctx, "")
}

// workspaceNotReadyErr names why a session action arrived before the tab's
// controller existed: still starting, or failed to start. Silently returning
// nil here swallowed the click with no feedback (#3938).
func workspaceNotReadyErr(tab *WorkspaceTab) error {
	if tab != nil && strings.TrimSpace(tab.StartupErr) != "" {
		return fmt.Errorf("workspace failed to start: %s", tab.StartupErr)
	}
	return fmt.Errorf("workspace is still starting")
}

// NewSession snapshots the current conversation and rotates to a fresh one.
func (a *App) NewSession() error {
	a.mu.RLock()
	tab := a.activeTabLocked()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return workspaceNotReadyErr(tab)
	}
	// Tab is already blank — just persist and skip the new-session dance.
	if !ctrl.Running() && !messagesHaveConversationContent(ctrl.History()) {
		a.persistTabSessionPath(tab, ctrl.SessionPath())
		return nil
	}

	if err := ctrl.NewSession(); err != nil {
		return err
	}
	a.persistTabSessionPath(tab, ctrl.SessionPath())
	return nil
}

func messagesHaveConversationContent(messages []provider.Message) bool {
	for _, msg := range messages {
		if msg.Role != provider.RoleSystem {
			return true
		}
	}
	return false
}

// ClearSession discards the current conversation and rotates to a fresh unsaved one.
func (a *App) ClearSession() error {
	a.mu.RLock()
	tab := a.activeTabLocked()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return workspaceNotReadyErr(tab)
	}
	if err := ctrl.ClearSession(); err != nil {
		return err
	}
	a.persistTabSessionPath(tab, ctrl.SessionPath())
	return nil
}

// CheckpointMeta summarises one rewind point (a user turn) for the desktop.
type CheckpointMeta struct {
	Turn            int      `json:"turn"`
	Prompt          string   `json:"prompt"`
	Files           []string `json:"files"` // paths changed during the turn
	Time            int64    `json:"time"`  // unix milliseconds
	CanCode         bool     `json:"canCode"`
	CanConversation bool     `json:"canConversation"`
	// Reapply marks the synthetic reverse checkpoint the latest code rewind
	// left behind: rewinding code to it replays (reapplies) that rewind.
	Reapply bool `json:"reapply,omitempty"`
}

// Checkpoints lists the session's rewind points, oldest first, for the rewind UI.
func (a *App) Checkpoints() []CheckpointMeta {
	return a.CheckpointsForTab("")
}

// SessionSearchHit is one session whose transcript matched a full-text query
// (upgrade spec 4-5), with short excerpts around the matches.
type SessionSearchHit struct {
	Path          string   `json:"path"`
	Excerpts      []string `json:"excerpts"`
	Title         string   `json:"title,omitempty"`
	TopicID       string   `json:"topicId,omitempty"`
	Scope         string   `json:"scope,omitempty"`
	WorkspaceRoot string   `json:"workspaceRoot,omitempty"`
	Profile       string   `json:"profile,omitempty"`
}

// OpenInEditorAt opens a file (optionally at a line) in the user's editor via
// the vscode:// URL scheme — VS Code / Cursor / VSCodium register it. Falls
// back to the OS default handler for the path when the scheme is absent.
func (a *App) OpenInEditorAt(path string, line int) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("path is required")
	}
	if line <= 0 {
		line = 1
	}
	url := fmt.Sprintf("vscode://file/%s:%d", strings.ReplaceAll(filepath.ToSlash(path), " ", "%20"), line)
	return openWorkspacePath(url)
}

// BranchView is one session-branch node for the tree navigator (upgrade spec 4-3).
type BranchView struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	ParentID  string `json:"parentId,omitempty"`
	Path      string `json:"path"`
	Preview   string `json:"preview,omitempty"`
	Turns     int    `json:"turns,omitempty"`
	UpdatedAt int64  `json:"updatedAt"`
	Current   bool   `json:"current,omitempty"`
}

// BranchesForTab lists the tab's session branches for the tree view.
func (a *App) BranchesForTab(tabID string) []BranchView {
	ctrl := a.ctrlByTabID(tabID)
	if ctrl == nil {
		return []BranchView{}
	}
	infos, err := ctrl.Branches()
	if err != nil {
		return []BranchView{}
	}
	current := agent.BranchID(ctrl.SessionPath())
	out := make([]BranchView, 0, len(infos))
	for _, b := range infos {
		out = append(out, BranchView{
			ID: b.ID, Name: b.Name, ParentID: b.ParentID, Path: b.Path,
			Preview: b.Preview, Turns: b.Turns, UpdatedAt: b.ModTime.UnixMilli(),
			Current: b.ID == current,
		})
	}
	return out
}

// SwitchBranchForTab navigates the tab to another branch; the frontend
// refreshes the transcript afterwards (syncActiveTab).
func (a *App) SwitchBranchForTab(tabID, ref string) error {
	ctrl := a.ctrlByTabID(tabID)
	if ctrl == nil {
		return fmt.Errorf("no active controller")
	}
	_, err := ctrl.SwitchBranch(ref)
	return err
}

// TurnFactsView mirrors tabs.turnFact on the wire.
type TurnFactsView struct {
	Seq              int    `json:"seq"`
	DurationMs       int64  `json:"durationMs"`
	Retries          int    `json:"retries"`
	ToolCalls        int    `json:"toolCalls"`
	ToolErrors       int    `json:"toolErrors"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	CacheHitTokens   int    `json:"cacheHitTokens"`
	CacheMissTokens  int    `json:"cacheMissTokens"`
	Err              string `json:"err,omitempty"`
}

// TurnFactsForTab returns the active tab's per-turn lifecycle records
// (upgrade spec 4-4), oldest first, last 50 turns.
func (a *App) TurnFactsForTab(tabID string) []TurnFactsView {
	a.mu.RLock()
	var tab *WorkspaceTab
	if t, ok := a.tabs[tabID]; ok {
		tab = t
	} else {
		tab = a.activeTabLocked()
	}
	a.mu.RUnlock()
	if tab == nil {
		return []TurnFactsView{}
	}
	facts := tab.TurnFacts()
	out := make([]TurnFactsView, 0, len(facts))
	for _, f := range facts {
		out = append(out, TurnFactsView{
			Seq: f.Seq, DurationMs: f.DurationMs, Retries: f.Retries,
			ToolCalls: f.ToolCalls, ToolErrors: f.ToolErrors,
			PromptTokens: f.PromptTokens, CompletionTokens: f.CompletionTokens,
			CacheHitTokens: f.CacheHitTokens, CacheMissTokens: f.CacheMissTokens, Err: f.Err,
		})
	}
	return out
}

// SearchSessionText full-text searches the active tab's session directory.
func (a *App) SearchSessionText(query string) []SessionSearchHit {
	a.mu.RLock()
	var ctrl tabSession
	if tab := a.tabByIDLocked(""); tab != nil {
		ctrl = tab.Ctrl
	}
	a.mu.RUnlock()
	if ctrl == nil {
		return []SessionSearchHit{}
	}
	hits := ctrl.SearchSessionText(query)
	out := make([]SessionSearchHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, SessionSearchHit{
			Path: h.Path, Excerpts: h.Excerpts, Title: h.Title, TopicID: h.TopicID,
			Scope: h.Scope, WorkspaceRoot: h.WorkspaceRoot, Profile: h.Profile,
		})
	}
	return out
}

// CheckpointFileChange is one file's previewed rewind change (spec 3-6).
type CheckpointFileChange struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // create | modify | delete
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Diff    string `json:"diff"`
}

// CheckpointDiffForTab previews what a code-scope rewind of `turn` would
// restore, per file — the rewind menu renders it instead of a bare count.
func (a *App) CheckpointDiffForTab(tabID string, turn int) []CheckpointFileChange {
	a.mu.RLock()
	var ctrl tabSession
	if tab := a.tabByIDLocked(tabID); tab != nil {
		ctrl = tab.Ctrl
	}
	a.mu.RUnlock()
	if ctrl == nil {
		return []CheckpointFileChange{}
	}
	changes := ctrl.CheckpointDiff(turn)
	out := make([]CheckpointFileChange, 0, len(changes))
	for _, ch := range changes {
		if ch.Binary {
			out = append(out, CheckpointFileChange{Path: ch.Path, Kind: "binary"})
			continue
		}
		out = append(out, CheckpointFileChange{Path: ch.Path, Kind: string(ch.Kind), Added: ch.Added, Removed: ch.Removed, Diff: ch.Diff})
	}
	return out
}

func (a *App) CheckpointsForTab(tabID string) []CheckpointMeta {
	a.mu.RLock()
	var ctrl tabSession
	if tab := a.tabByIDLocked(tabID); tab != nil {
		ctrl = tab.Ctrl
	}
	a.mu.RUnlock()
	if ctrl == nil {
		return []CheckpointMeta{}
	}
	metas := ctrl.Checkpoints()
	out := make([]CheckpointMeta, 0, len(metas))
	for _, m := range metas {
		out = append(out, CheckpointMeta{
			Turn:            m.Turn,
			Prompt:          m.Prompt,
			Files:           m.Paths,
			Time:            m.Time.UnixMilli(),
			CanCode:         len(m.Paths) > 0,
			CanConversation: ctrl.CheckpointHasBoundary(m.Turn),
		})
	}
	// RestoreCode(turn) reverts every file touched in this turn or any later one, so
	// a turn can rewind code even when it changed no files itself — as long as a
	// later turn did. Propagate CanCode backwards over the oldest-first list.
	hasCodeAfter := false
	for i := len(out) - 1; i >= 0; i-- {
		if len(out[i].Files) > 0 {
			hasCodeAfter = true
		}
		out[i].CanCode = hasCodeAfter
	}
	// The newest checkpoint, when it is a rewind-keep (the reverse side of the
	// latest code rewind), is the reapply target.
	if n := len(out); n > 0 && strings.HasPrefix(out[n-1].Prompt, "rewind-keep") {
		out[n-1].Reapply = true
	}
	return out
}

// RewindPreviewFileView is one file's safety verdict for a code rewind.
type RewindPreviewFileView struct {
	Path  string `json:"path"`
	Class string `json:"class"` // safe | unsafe | ignored
}

// RewindPreviewForTab classifies every file a code-scope rewind of `turn`
// would restore (ZCode-style buckets: safe = unchanged since the agent's last
// write; unsafe = externally modified since; ignored = no hash provenance).
func (a *App) RewindPreviewForTab(tabID string, turn int) []RewindPreviewFileView {
	a.mu.RLock()
	var ctrl tabSession
	if tab := a.tabByIDLocked(tabID); tab != nil {
		ctrl = tab.Ctrl
	}
	a.mu.RUnlock()
	if ctrl == nil {
		return []RewindPreviewFileView{}
	}
	classes := ctrl.RewindPreview(turn)
	out := make([]RewindPreviewFileView, 0, len(classes))
	for _, c := range classes {
		out = append(out, RewindPreviewFileView{Path: c.Path, Class: c.Class})
	}
	return out
}

// Rewind restores the session to the start of turn. scope is "code",
// "conversation", or "both" (anything else is treated as "both"). The frontend
// re-reads History after this resolves.
func (a *App) Rewind(turn int, scope string) error {
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return nil
	}
	s := control.RewindBoth
	switch scope {
	case "code":
		s = control.RewindCode
	case "conversation":
		s = control.RewindConversation
	}
	return ctrl.Rewind(turn, s)
}

// Fork branches the conversation at the start of turn into a new session tab
// (preserving the current tab), keeping code intact, and switches to the new tab.
func (a *App) Fork(turn int) (TabMeta, error) {
	a.mu.RLock()
	sourceTab := a.activeTabLocked()
	ctrl := a.activeCtrlLocked()
	if sourceTab == nil || ctrl == nil {
		a.mu.RUnlock()
		return TabMeta{}, nil
	}
	scope := sourceTab.Scope
	workspaceRoot := sourceTab.WorkspaceRoot
	sourceTitle := sourceTab.TopicTitle
	model := sourceTab.model
	effort := cloneStringPtr(sourceTab.effort)
	mode := currentTabMode(sourceTab)
	profileKey := normalizeProfileName(sourceTab.profile)
	disabledMCP := cloneServerViewMap(sourceTab.disabledMCP)
	mcpOrder := append([]string(nil), sourceTab.mcpOrder...)
	a.mu.RUnlock()

	newPath, err := ctrl.ForkSession(turn, "")
	if err != nil {
		return TabMeta{}, err
	}
	topicID := newTopicID()
	topicTitle := forkTopicTitle(sourceTitle)
	titleRoot := workspaceRoot
	if scope == "global" {
		titleRoot = ""
	}
	if err := setTopicTitle(titleRoot, topicID, topicTitle); err != nil {
		return TabMeta{}, err
	}
	m, _ := agent.EnsureBranchMeta(newPath)
	m.Scope = scope
	m.WorkspaceRoot = workspaceRoot
	m.TopicID = topicID
	m.TopicTitle = topicTitle
	m.Profile = profileKey
	if err := agent.SaveBranchMeta(newPath, m); err != nil {
		return TabMeta{}, err
	}

	a.mu.Lock()
	tabID := a.newUniqueTabIDLocked()
	tab := &WorkspaceTab{
		ID:            tabID,
		Scope:         scope,
		WorkspaceRoot: workspaceRoot,
		TopicID:       topicID,
		TopicTitle:    topicTitle,
		SessionPath:   newPath,
		model:         model,
		effort:        effort,
		mode:          mode,
		profile:       profileKey,
		disabledMCP:   disabledMCP,
		mcpOrder:      mcpOrder,
	}
	tab.sink = &tabEventSink{tabID: tabID, app: a}
	a.tabs[tabID] = tab
	a.tabOrder = append(a.tabOrder, tabID)
	a.activeTabID = tabID
	a.saveTabsLocked()
	meta := a.tabMeta(tab, true)
	a.mu.Unlock()

	a.emitProjectTreeChanged()
	a.startTabControllerBuild(tab)
	return meta, nil
}

// SummarizeFrom / SummarizeUpTo compress the conversation from / up to the start
// of turn into one summary (Claude Code's "summarize from/up to here"), keeping
// code intact. The frontend re-reads History after this resolves.
func (a *App) SummarizeFrom(turn int) error {
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return nil
	}
	return ctrl.SummarizeFrom(a.ctx, turn)
}

func (a *App) SummarizeUpTo(turn int) error {
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return nil
	}
	return ctrl.SummarizeUpTo(a.ctx, turn)
}

// SessionMeta summarises one saved session for the history panel.
type SessionMeta struct {
	Path           string `json:"path"`
	Preview        string `json:"preview"`         // first user message
	Title          string `json:"title,omitempty"` // user-chosen name, when set (overrides preview)
	Turns          int    `json:"turns"`
	CreatedAt      int64  `json:"createdAt"`      // unix milliseconds
	LastActivityAt int64  `json:"lastActivityAt"` // unix milliseconds
	ModTime        int64  `json:"modTime"`        // compatibility alias for lastActivityAt
	DeletedAt      int64  `json:"deletedAt,omitempty"`
	Current        bool   `json:"current"`
	Open           bool   `json:"open"`
	Scope          string `json:"scope,omitempty"`
	WorkspaceRoot  string `json:"workspaceRoot,omitempty"`
	TopicID        string `json:"topicId,omitempty"`
	TopicTitle     string `json:"topicTitle,omitempty"`
	Profile        string `json:"profile,omitempty"`
	// IsExpert is true for expert-team collaboration sessions (scope="expert").
	// The frontend uses it to group expert sessions separately in the panel.
	IsExpert bool `json:"isExpert,omitempty"`
	// ExpertTeamID identifies the expert team for an expert session; empty for
	// normal sessions. Pairs with IsExpert so the panel can group by team.
	ExpertTeamID string `json:"expertTeamId,omitempty"`
	// Platform/RemoteID/ChatType/Mode are non-empty only for IM bot sessions.
	// The sidebar IM-contact detail uses them to list a contact's bot sessions.
	Platform string `json:"platform,omitempty"`
	RemoteID string `json:"remoteId,omitempty"`
	ChatType string `json:"chatType,omitempty"`
	Mode     string `json:"mode,omitempty"`
}

type WorkspaceMeta struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

func controllerSessionDir(ctrl tabSession) string {
	if ctrl != nil {
		if dir := ctrl.SessionDir(); dir != "" {
			return dir
		}
	}
	return desktopSessionDir("")
}

func tabSessionDir(tab *WorkspaceTab) string {
	if tab != nil {
		// The controller's session dir was fixed at boot.Build time from the
		// tab's profile, so it is already profile-correct — prefer it.
		if tab.Ctrl != nil {
			if dir := tab.Ctrl.SessionDir(); dir != "" {
				return dir
			}
		}
		// Controller not built yet (tab is being constructed): derive from the
		// workspace root + the tab's profile so even pre-build paths partition.
		if tab.WorkspaceRoot != "" {
			return desktopSessionDirFor(tab.WorkspaceRoot, tab.profile)
		}
		return desktopSessionDirFor("", tab.profile)
	}
	return desktopSessionDir("")
}

func (a *App) activeSessionDir() string {
	a.mu.RLock()
	tab := a.activeTabLocked()
	dir := tabSessionDir(tab)
	a.mu.RUnlock()
	return dir
}

// ListSessions returns the saved sessions newest-first for the history panel,
// marking the one the current conversation is writing to and attaching any
// user-chosen titles.
//
// It scans every known session directory (global + every project + every tab's
// controller dir), not just the active tab's, so a session created in one
// project's tab still shows up in the panel while another tab is active.
// Directories returned by knownSessionDirs may overlap (the same dir added via
// different paths), so entries are deduped by session file path.
func (a *App) ListSessions() []SessionMeta {
	return a.listSessions("")
}

// ListSessionsForProfile returns saved sessions newest-first, restricted to the
// given profile's partition ("dev"/"cowork"/"netdev"). The three workspaces are
// independent surfaces: the ops sidebar must not list coding or office
// conversations, and vice versa, so the frontend session lists pass the active
// tab's profile here instead of relying on ListSessions' cross-profile union.
func (a *App) ListSessionsForProfile(profile string) []SessionMeta {
	return a.listSessions(normalizeProfileName(profile))
}

// namedPartitionDirs returns the exact session directories that belong to the
// NAMED (non-dev) profile partitions, global and per-project, across every
// profile's projects index. Computed from config.SessionDirFor/
// ProjectSessionDirFor rather than path-pattern matching so a workspace
// literally named "cowork" is not misclassified.
func (a *App) namedPartitionDirs() map[string]string {
	dirs := map[string]string{}
	roots := []string{
		profileHomeRoot(config.ProfileDev),
		profileHomeRoot(config.ProfileCowork),
		profileHomeRoot(config.ProfileNetDev),
	}
	for _, key := range []string{config.ProfileDev, config.ProfileCowork, config.ProfileNetDev} {
		for _, p := range loadProjectsFile(key).Projects {
			if root := strings.TrimSpace(p.Root); root != "" {
				roots = append(roots, root)
			}
		}
	}
	for _, key := range []string{config.ProfileCowork, config.ProfileNetDev} {
		if d := config.SessionDirFor(key); d != "" {
			dirs[d] = key
		}
		for _, root := range roots {
			if d := config.ProjectSessionDirFor(root, key); d != "" {
				dirs[d] = key
			}
		}
	}
	return dirs
}

// classifySessionProfile attributes a session listed from dir to a profile
// key. The directory partition is the reliable signal (named profiles live in
// their own dirs); the session's .meta Profile stamp breaks ties for legacy
// unpartitioned dirs, which otherwise count as dev.
func classifySessionProfile(dir, metaProfile string, namedDirs map[string]string) string {
	if key, ok := namedDirs[dir]; ok && key != "" {
		return key
	}
	if p := config.ProfileNameKey(metaProfile); p != "" {
		return p
	}
	return config.ProfileDev
}

func (a *App) listSessions(onlyProfile string) []SessionMeta {
	out := []SessionMeta{}
	seen := map[string]bool{} // dedupe by session file path; same dir can be listed more than once
	namedDirs := map[string]string{}
	if onlyProfile != "" {
		namedDirs = a.namedPartitionDirs()
	}
	// The "current"/"open" markers are per-directory: a session is only current
	// relative to the dir its tab writes to, so resolve them once per dir.
	activeDir := a.activeSessionDir()
	activePath := a.activeSessionPath(activeDir)
	for _, dir := range a.knownSessionDirs() {
		infos, err := agent.ListSessions(dir)
		if err != nil {
			continue
		}
		titles := loadSessionTitles(dir)
		open := a.openSessionPaths(dir)
		for _, s := range infos {
			if seen[s.Path] {
				continue
			}
			seen[s.Path] = true
			if onlyProfile != "" && classifySessionProfile(dir, s.Profile, namedDirs) != onlyProfile {
				continue
			}
			// Skip sessions with no completed turns — these are brand-new
			// tabs that haven't exchanged any messages yet.  They should not
			// appear in the "Recent Sessions" sidebar until at least one
			// conversation has taken place.
			if s.Turns == 0 {
				continue
			}
			_, isOpen := open[s.Path]
			meta := sessionMetaFromInfo(s, titles[filepath.Base(s.Path)], s.Path == activePath, isOpen, 0)
			out = append(out, meta)
		}
	}
	// Newest activity first. knownSessionDirs already orders dirs somewhat, but
	// a flat sort gives a stable recency order across all dirs.
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastActivityAt == out[j].LastActivityAt {
			return out[i].Path < out[j].Path
		}
		return out[i].LastActivityAt > out[j].LastActivityAt
	})
	return out
}

// ListTrashedSessions returns sessions that were moved to the local trash,
// newest-deleted first. These can be previewed, restored, or permanently purged.
func (a *App) ListTrashedSessions() []SessionMeta {
	return a.listTrashedSessions("")
}

// ListTrashedSessionsForProfile is the profile-scoped trash list, matching
// ListSessionsForProfile's partition attribution.
func (a *App) ListTrashedSessionsForProfile(profile string) []SessionMeta {
	return a.listTrashedSessions(normalizeProfileName(profile))
}

func (a *App) listTrashedSessions(onlyProfile string) []SessionMeta {
	out := []SessionMeta{}
	namedDirs := map[string]string{}
	if onlyProfile != "" {
		namedDirs = a.namedPartitionDirs()
	}
	for _, dir := range a.knownSessionDirs() {
		paths, err := listTrashedSessionFiles(dir)
		if err != nil {
			continue
		}
		titles := loadSessionTitles(dir)
		for _, path := range paths {
			infos, err := agent.ListSessions(filepath.Dir(path))
			if err != nil || len(infos) == 0 {
				continue
			}
			if onlyProfile != "" && classifySessionProfile(dir, infos[0].Profile, namedDirs) != onlyProfile {
				continue
			}
			deletedAt := trashedSessionDeletedAt(path)
			out = append(out, sessionMetaFromInfo(infos[0], titles[filepath.Base(path)], false, false, deletedAt))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeletedAt == out[j].DeletedAt {
			return out[i].LastActivityAt > out[j].LastActivityAt
		}
		return out[i].DeletedAt > out[j].DeletedAt
	})
	return out
}

func (a *App) trashedSessionDir(path string) (string, error) {
	for _, dir := range a.knownSessionDirs() {
		if _, _, _, err := validateTrashedSessionPath(dir, path); err == nil {
			return dir, nil
		}
	}
	return "", fmt.Errorf("trashed session path outside known session dirs: %s", path)
}

func (a *App) sessionDirForPath(path string) (string, string, error) {
	for _, dir := range a.knownSessionDirs() {
		sessionPath, _, err := validateSessionPath(dir, path)
		if err == nil {
			return dir, sessionPath, nil
		}
	}
	return "", "", fmt.Errorf("session path outside known session dirs: %s", path)
}

func sessionMetaFromInfo(s agent.SessionInfo, title string, current, open bool, deletedAt int64) SessionMeta {
	return SessionMeta{
		Path:           s.Path,
		Preview:        s.Preview,
		Title:          title,
		Turns:          s.Turns,
		CreatedAt:      s.CreatedAt.UnixMilli(),
		LastActivityAt: s.LastActivityAt.UnixMilli(),
		ModTime:        s.LastActivityAt.UnixMilli(),
		DeletedAt:      deletedAt,
		Current:        current,
		Open:           open,
		Scope:          s.Scope,
		WorkspaceRoot:  s.WorkspaceRoot,
		TopicID:        s.TopicID,
		TopicTitle:     s.TopicTitle,
		Profile:        s.Profile,
		IsExpert:       s.Scope == "expert",
		ExpertTeamID:   s.ExpertTeamID,
		Platform:       s.Platform,
		RemoteID:       s.RemoteID,
		ChatType:       s.ChatType,
		Mode:           s.Mode,
	}
}

// DeleteSession moves a saved session to the local trash. It refuses any open
// session because tab auto-save would recreate or append to the file later.
func (a *App) DeleteSession(path string) error {
	dir := a.activeSessionDir()
	sessionPath, key, err := validateSessionPath(dir, path)
	if err != nil {
		return err
	}
	if _, ok := a.openSessionPaths(dir)[sessionPath]; ok {
		return errActiveSession
	}
	if err := trashSessionArtifacts(dir, sessionPath, key); err != nil {
		return err
	}
	a.emitProjectTreeChanged()
	return nil
}

func (a *App) openSessionPaths(dir string) map[string]struct{} {
	a.mu.RLock()
	paths := make([]string, 0, len(a.tabs))
	for _, tab := range a.tabs {
		if tab != nil {
			paths = append(paths, tab.currentSessionPath())
		}
	}
	a.mu.RUnlock()

	out := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		currentPath, _, err := validateSessionPath(dir, path)
		if err == nil {
			out[currentPath] = struct{}{}
		}
	}
	return out
}

func (a *App) activeSessionPath(dir string) string {
	a.mu.RLock()
	var path string
	if tab := a.tabs[a.activeTabID]; tab != nil {
		path = tab.currentSessionPath()
	}
	a.mu.RUnlock()
	currentPath, _, err := validateSessionPath(dir, path)
	if err != nil {
		return ""
	}
	return currentPath
}

// RestoreSession moves a trashed session back into the saved-session list.
func (a *App) RestoreSession(path string) error {
	dir, err := a.trashedSessionDir(path)
	if err != nil {
		return err
	}
	_, key, _, err := validateTrashedSessionPath(dir, path)
	if err != nil {
		return err
	}
	if err := restoreTrashedSessionFile(dir, path); err != nil {
		return err
	}
	// Folder layout (2026-08-21): restore lands at <dir>/<stem>/<key>.
	if err := restoreSessionTopicIndex(dir, filepath.Join(dir, strings.TrimSuffix(key, ".jsonl"), key), a.activeProfileKey()); err != nil {
		return err
	}
	a.emitProjectTreeChanged()
	return nil
}

// PurgeTrashedSession permanently removes a trashed session and its title/display
// sidecars.
func (a *App) PurgeTrashedSession(path string) error {
	dir, err := a.trashedSessionDir(path)
	if err != nil {
		return err
	}
	return purgeTrashedSessionFile(dir, path)
}

// RenameSession sets a custom display name for a session (empty clears it back to
// the preview). It only affects the history panel; the file on disk is unchanged.
func (a *App) RenameSession(path, title string) error {
	return setSessionTitle(a.activeSessionDir(), path, title)
}

// ResumeSession snapshots the current conversation, then loads the session at
// path and continues it on the active tab. The model and working folder are
// unchanged; only the transcript is swapped. Returns the resumed messages for
// the frontend to render.
func (a *App) ResumeSession(path string) ([]HistoryMessage, error) {
	return a.ResumeSessionForTab("", path)
}

// ResumeSessionForTab is the tab-scoped form of ResumeSession. History rows
// carry scope/workspace/topic metadata, so callers that opened or selected a
// matching tab should resume on that exact controller instead of whichever tab is
// active by the time the async call reaches the backend.
func (a *App) ResumeSessionForTab(tabID, path string) ([]HistoryMessage, error) {
	tab := a.tabByID(tabID)
	if tab == nil {
		return []HistoryMessage{}, fmt.Errorf("tab is not ready")
	}
	// Snapshot ctrl under RLock to avoid TOCTOU.
	a.mu.RLock()
	ctrl := tab.Ctrl
	a.mu.RUnlock()
	if ctrl == nil {
		return []HistoryMessage{}, fmt.Errorf("tab is not ready")
	}
	// Refuse while a turn is running: Resume hot-swaps the executor's session
	// object, so a mid-flight turn would append its remaining output to the
	// RESUMED session (cross-conversation pollution) while the 30s autosave
	// starts writing the old turn's partial state into the new file. The
	// frontend's resume entry already guards the ACTIVE tab; this covers
	// background tabs and non-desktop callers. Mirrors SetModelForTab.
	if ctrl.Running() {
		return []HistoryMessage{}, fmt.Errorf("finish or cancel the current turn before resuming another session")
	}
	sessionPath, _, err := validateSessionPath(controllerSessionDir(ctrl), path)
	if err != nil {
		return nil, err
	}
	loaded, err := agent.LoadSession(sessionPath)
	if err != nil {
		return nil, err
	}
	_ = ctrl.Snapshot() // persist the current session before switching away
	ctrl.Resume(loaded, sessionPath)
	a.rememberTabSessionPath(tab, sessionPath)
	return a.HistoryForTab(tabID), nil
}

// PreviewSession reads a saved session for display only. It does not snapshot or
// swap the active controller, so the history drawer can call it while a turn runs.
func (a *App) PreviewSession(path string) ([]HistoryMessage, error) {
	sessionDir, sessionPath, err := a.sessionDirForPath(path)
	if err != nil {
		return nil, err
	}
	return previewSessionMessages(sessionDir, sessionPath)
}

// PickWorkspace opens a folder chooser and, on a pick, opens a new project tab
// scoped to that folder. Returns the chosen path ("" if cancelled).
func (a *App) PickWorkspace() (string, error) {
	if a.ctx == nil {
		return "", nil
	}
	cur, _ := os.Getwd()
	a.mu.RLock()
	if tab := a.activeTabLocked(); tab != nil && tab.WorkspaceRoot != "" {
		cur = tab.WorkspaceRoot
	}
	a.mu.RUnlock()
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "选择工作文件夹",
		DefaultDirectory: dialogDefaultDirectory(cur),
	})
	if err != nil || dir == "" {
		return "", err
	}
	return a.SwitchWorkspace(dir)
}

// PickImportFolder opens a directory dialog WITHOUT switching workspace.
// Used by the RAG import flow so importing documents into a knowledge-base
// collection doesn't pollute the project tree with a new workspace entry.
func (a *App) PickImportFolder() (string, error) {
	if a.ctx == nil {
		return "", nil
	}
	cur, _ := os.Getwd()
	a.mu.RLock()
	if tab := a.activeTabLocked(); tab != nil && tab.WorkspaceRoot != "" {
		cur = tab.WorkspaceRoot
	}
	a.mu.RUnlock()
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "选择要导入的文件夹",
		DefaultDirectory: dialogDefaultDirectory(cur),
	})
	if err != nil || dir == "" {
		return "", err
	}
	return dir, nil
}

// PickImportFiles opens a multi-file dialog WITHOUT switching workspace.
// Supports selecting individual files (not just folders) for import.
func (a *App) PickImportFiles() ([]string, error) {
	if a.ctx == nil {
		return nil, nil
	}
	cur, _ := os.Getwd()
	a.mu.RLock()
	if tab := a.activeTabLocked(); tab != nil && tab.WorkspaceRoot != "" {
		cur = tab.WorkspaceRoot
	}
	a.mu.RUnlock()
	selection, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "选择要导入的文件",
		DefaultDirectory: dialogDefaultDirectory(cur),
		Filters: []runtime.FileFilter{
			{DisplayName: "文档文件", Pattern: "*.pdf;*.docx;*.xlsx;*.pptx;*.xls;*.epub;*.txt;*.md;*.csv;*.tsv;*.json;*.html;*.htm;*.py;*.go;*.js;*.ts;*.yaml;*.yml"},
			{DisplayName: "所有文件", Pattern: "*"},
		},
	})
	if err != nil || len(selection) == 0 {
		return nil, err
	}
	return selection, nil
}

func dialogDefaultDirectory(preferred string) string {
	if dir := nearestExistingDirectory(preferred); dir != "" {
		return dir
	}
	if cwd, err := os.Getwd(); err == nil {
		if dir := nearestExistingDirectory(cwd); dir != "" {
			return dir
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if dir := nearestExistingDirectory(home); dir != "" {
			return dir
		}
	}
	return ""
}

func nearestExistingDirectory(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	for {
		info, err := os.Stat(path)
		if err == nil {
			if info.IsDir() {
				return path
			}
			path = filepath.Dir(path)
			continue
		}
		parent := filepath.Dir(path)
		if parent == path {
			return ""
		}
		path = parent
	}
}

func (a *App) ListWorkspaces() []WorkspaceMeta {
	profileKey := a.activeProfileKey()
	activeRoot := ""
	a.mu.RLock()
	if tab := a.activeTabLocked(); tab != nil && tab.WorkspaceRoot != "" {
		activeRoot = normalizeProjectRoot(tab.WorkspaceRoot)
	}
	a.mu.RUnlock()
	openRoots := map[string]bool{}
	if activeRoot != "" {
		openRoots[activeRoot] = true
	}
	a.mu.RLock()
	for _, tab := range a.tabs {
		if tab != nil && tab.WorkspaceRoot != "" {
			if profileKey == "" || tab.profile == "" || config.ProfileNameKey(tab.profile) == profileKey {
				openRoots[normalizeProjectRoot(tab.WorkspaceRoot)] = true
			}
		}
	}
	a.mu.RUnlock()

	projects := loadProjectsFile(profileKey).Projects
	out := make([]WorkspaceMeta, 0, len(projects))
	for _, project := range projects {
		if len(project.Topics) == 0 && len(loadTopicTitles(project.Root)) == 0 && !openRoots[normalizeProjectRoot(project.Root)] {
			continue
		}
		out = append(out, WorkspaceMeta{
			Path:    project.Root,
			Name:    projectDisplayName(project),
			Current: activeRoot != "" && project.Root == activeRoot,
		})
	}
	return out
}

func (a *App) RemoveWorkspace(dir string) error {
	if dir == "" {
		return fmt.Errorf("workspace path is required")
	}
	profileKey := a.activeProfileKey()
	dir = normalizeProjectRoot(dir)
	forgetWorkspace(dir)
	if err := removeProject(dir, profileKey); err != nil {
		return err
	}
	// If the removed workspace was the active one, clear the pointer
	// so we don't leave a stale reference to a deleted project.
	if loadWorkspace() == dir {
		if remaining := loadProjectsFile(profileKey); len(remaining.Projects) > 0 {
			// Fall back to the first remaining project
			saveWorkspace(remaining.Projects[0].Root)
		} else {
			// No projects left; clear the active pointer entirely
			clearWorkspace()
		}
	}
	a.emitProjectTreeChanged()
	return nil
}

// migrateLegacyWorkspacesIntoDevOnce drains the pre-profile
// desktop-workspaces.json list into the DEV index and deletes the file.
// That list predates profiles (it only ever recorded coding-era roots), and
// under strict three-profile isolation it must never feed a non-dev profile's
// project generation — formerly ListWorkspaces injected it into whatever
// profile asked first, which is how a coding workspace appeared in a fresh
// ops interface. Startup-only; the file's deletion makes it self-retiring.
func migrateLegacyWorkspacesIntoDevOnce() {
	legacy := loadWorkspaces()
	if len(legacy) == 0 {
		return
	}
	f := loadProjectsFile(config.ProfileDev)
	seen := make(map[string]bool, len(f.Projects)+len(legacy))
	for _, p := range f.Projects {
		seen[p.Root] = true
	}
	changed := false
	for _, path := range legacy {
		root := normalizeProjectRoot(path)
		if root == "" || seen[root] {
			continue
		}
		f.Projects = append(f.Projects, desktopProject{Root: root})
		seen[root] = true
		changed = true
	}
	if changed {
		_ = saveProjectsFile(f, config.ProfileDev)
	}
	_ = os.Remove(workspaceListPath())
}

func workspaceName(path string) string {
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return path
	}
	return name
}

func (a *App) SwitchWorkspace(dir string) (string, error) {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = home
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	saveWorkspace(dir)

	// Open a registered topic so the new workspace appears in the project tree
	// immediately instead of only existing as an in-memory tab. New workspaces
	// open in the active tab's profile (dev by default).
	profile := a.activeProfileKey()
	// Strict profile isolation: refuse opening a workspace that belongs to
	// another profile's index.
	if rootOwnedByOtherProfile(dir, profile) {
		return "", fmt.Errorf("项目 %s 属于其他模式，三模式项目互相隔离", dir)
	}
	topic, err := a.CreateTopic("project", dir, profile, "")
	if err != nil {
		return "", err
	}
	meta, err := a.OpenProjectTab(dir, topic.ID, profile)
	if err != nil {
		return "", err
	}
	return meta.WorkspaceRoot, nil
}

// HistoryMessage is one prior turn, for the frontend to repopulate its transcript
// after a reload.
type HistoryMessage struct {
	Role        string            `json:"role"`
	Content     string            `json:"content"`
	Reasoning   string            `json:"reasoning,omitempty"`
	Level       string            `json:"level,omitempty"`
	ToolCalls   []HistoryToolCall `json:"toolCalls,omitempty"`
	ToolCallID  string            `json:"toolCallId,omitempty"`
	ToolName    string            `json:"toolName,omitempty"`
	Pending     bool              `json:"pending,omitempty"`
	Trigger     string            `json:"trigger,omitempty"`
	Messages    int               `json:"messages,omitempty"`
	Summary     string            `json:"summary,omitempty"`
	Archive     string            `json:"archive,omitempty"`
	Subagent    []HistoryMessage  `json:"subagent,omitempty"`    // nested transcript for a task/run_skill/... call (loaded from subagents/sa_xxx.jsonl)
	SubagentRef string            `json:"subagentRef,omitempty"` // the sa_... ref identifying this nested transcript (for "Subagent reference: sa_...")
}

type HistoryToolCall struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Arguments   string           `json:"arguments"`
	Subagent    []HistoryMessage `json:"subagent,omitempty"`
	SubagentRef string           `json:"subagentRef,omitempty"`
}

// PresentForTab returns the presentation event records for the tab's session
// (the rich view-only stream: tool dispatches with readOnly/profile/parentId,
// results with durationMs/truncated/attachments, notice/phase/compaction
// cards). Prefers the controller's in-memory recorder — the on-disk sidecar is
// only flushed by snapshot() (every 30s mid-turn), so a disk read during an
// active run would drop the newest cards exactly when a reload happens. Falls
// back to the sidecar only when no recorder exists (a session created before
// Present was enabled, or a CLI-started one); an empty result means callers
// degrade to the provider.Message-only history. The rewrite version the
// records are aligned to is returned alongside so the frontend can detect
// staleness after a compaction that hasn't re-flushed.
func (a *App) PresentForTab(tabID string) PresentPayload {
	ctrl := a.ctrlByTabID(tabID)
	if ctrl == nil {
		return PresentPayload{}
	}
	if records, ver, ok := ctrl.PresentRecords(); ok {
		return PresentPayload{Records: records, RewriteVersion: ver}
	}
	path := present.PresentPath(ctrl.SessionPath())
	records, ver, err := present.Load(path)
	if err != nil {
		return PresentPayload{}
	}
	return PresentPayload{Records: records, RewriteVersion: ver}
}

// PresentPayload is the wire shape of PresentForTab's return.
type PresentPayload struct {
	Records        []present.Record `json:"records"`
	RewriteVersion int              `json:"rewriteVersion,omitempty"`
}

func (a *App) HistoryForTab(tabID string) []HistoryMessage {
	ctrl := a.ctrlByTabID(tabID)
	if ctrl == nil {
		return []HistoryMessage{}
	}
	msgs := ctrl.History()
	resolve := sessionDisplayResolver(controllerSessionDir(ctrl), ctrl.SessionPath())
	out := historyMessages(msgs, resolve)
	// Attach nested sub-agent transcripts to the task/run_skill/... tool calls that
	// spawned them. Sub-agent transcripts are persisted independently
	// (subagents/sa_xxx.jsonl) and keyed by ParentToolCallID == the parent's
	// ToolCall.ID, but were never loaded on history rebuild — so a tab switch or
	// reload made every sub-agent's internal steps vanish, leaving only the final
	// answer. This injects them back so the rebuilt transcript nests exactly like
	// the live stream (which uses parentId-based nesting in Transcript.tsx).
	attachSubagentTranscripts(out, controllerSessionDir(ctrl), ctrl.SessionPath(), resolve)
	return out
}

func historyMessages(msgs []provider.Message, resolveUserContent func(string) string) []HistoryMessage {
	out := make([]HistoryMessage, 0, len(msgs))
	for _, m := range msgs {
		content := provider.ContentString(m.Content)
		if m.Role == provider.RoleUser {
			content = resolveUserContent(content)
			if control.IsSyntheticUserMessage(content) {
				continue
			}
		}
		reasoning := ""
		if m.Role == provider.RoleAssistant {
			reasoning = m.ReasoningContent
		}
		hm := HistoryMessage{Role: string(m.Role), Content: content, Reasoning: reasoning}
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0 {
			hm.ToolCalls = make([]HistoryToolCall, len(m.ToolCalls))
			for i, tc := range m.ToolCalls {
				hm.ToolCalls[i] = HistoryToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
			}
		}
		if m.Role == provider.RoleTool {
			hm.ToolCallID = m.ToolCallID
			hm.ToolName = m.Name
		}
		out = append(out, hm)
	}
	return out
}

// attachSubagentTranscripts loads persisted sub-agent transcripts (subagents/
// sa_xxx.jsonl) for the parent session and inlines them under the tool call
// that spawned them. It mutates hm in place: for each assistant tool call whose
// name is a sub-agent meta-tool (task/run_skill/explore/...), if a matching
// SubagentArtifact exists (Meta.ParentToolCallID == tc.ID), its transcript is
// loaded via LoadSession, mapped with historyMessages, and attached as
// hm.ToolCalls[i].Subagent (+ SubagentRef). The frontend then nests these
// exactly as it does the live stream (parentId-based, ToolCard.tsx).
//
// Cost: one ListSubagentsByParent per call (O(n) over the subagents dir) + one
// transcript load per matching tool call that is actually present in this page.
// Sub-agents still running (no .jsonl yet) or crashed (only .meta.json) are
// silently skipped — the parent card keeps its final-result text either way.
// maxSubagentDepth caps recursive sub-agent nesting. A sub-agent may itself
// spawn sub-agents (e.g. research → research); we load transcripts down to this
// depth so nested delegation renders, with a guard against pathological or
// cyclic chains. 4 covers realistic delegation trees.
const maxSubagentDepth = 4

func attachSubagentTranscripts(hm []HistoryMessage, sessionDir, sessionPath string, resolveUserContent func(string) string) {
	attachSubagentTranscriptsDepth(hm, sessionDir, sessionPath, resolveUserContent, 0)
}

// attachSubagentTranscriptsDepth is the recursive worker. depth tracks how many
// sub-agent layers we've descended; at maxSubagentDepth we stop descending
// (deeper transcripts are omitted, not malformed — the parent card still shows).
func attachSubagentTranscriptsDepth(hm []HistoryMessage, sessionDir, sessionPath string, resolveUserContent func(string) string, depth int) {
	subagentTools := agent.SubagentMetaTools()
	isSubagent := make(map[string]bool, len(subagentTools))
	for _, n := range subagentTools {
		isSubagent[n] = true
	}
	// Quick reject: is there any sub-agent tool call at all?
	hasSubagentCall := false
	for i := range hm {
		for _, tc := range hm[i].ToolCalls {
			if isSubagent[tc.Name] {
				hasSubagentCall = true
				break
			}
		}
		if hasSubagentCall {
			break
		}
	}
	if !hasSubagentCall {
		return
	}
	parentSession := agent.BranchID(sessionPath)
	artifacts, err := agent.ListSubagentsByParent(sessionDir, parentSession)
	if err != nil || len(artifacts) == 0 {
		return
	}
	// Index by ParentToolCallID for O(1) lookup per tool call.
	byToolCall := make(map[string]agent.SubagentArtifact, len(artifacts))
	for _, art := range artifacts {
		if id := strings.TrimSpace(art.Meta.ParentToolCallID); id != "" {
			byToolCall[id] = art
		}
	}
	for i := range hm {
		for j, tc := range hm[i].ToolCalls {
			if !isSubagent[tc.Name] {
				continue
			}
			art, ok := byToolCall[tc.ID]
			if !ok {
				continue
			}
			sess, lerr := agent.LoadSession(art.SessionPath)
			if lerr != nil || sess == nil {
				continue // running sub-agent (no .jsonl yet) or crash — skip
			}
			sub := historyMessages(sess.Snapshot(), resolveUserContent)
			hm[i].ToolCalls[j].Subagent = sub
			hm[i].ToolCalls[j].SubagentRef = art.Ref
			// Descend: a sub-agent's own tool calls may have spawned grandchild
			// agents (their transcripts sit in the same subagents/ dir, keyed by
			// this sub-agent's session id). Recurse with the sub-agent's session
			// path so ListSubagentsByParent resolves to its children, until the
			// depth cap is reached.
			if depth+1 < maxSubagentDepth {
				attachSubagentTranscriptsDepth(sub, sessionDir, art.SessionPath, resolveUserContent, depth+1)
			}
		}
	}
}

func previewSessionMessages(sessionDir, path string) ([]HistoryMessage, error) {
	sessionPath, _, err := validateSessionPath(sessionDir, path)
	if err != nil {
		return nil, err
	}
	if out, ok, err := previewEventSessionMessages(sessionPath); ok || err != nil {
		return out, err
	}
	loaded, err := agent.LoadSession(sessionPath)
	if err != nil {
		return nil, err
	}
	return historyMessages(loaded.Snapshot(), sessionDisplayResolver(sessionDir, sessionPath)), nil
}

type previewEventRecord struct {
	Kind             string             `json:"kind"`
	Type             string             `json:"type"`
	Role             string             `json:"role"`
	Text             string             `json:"text"`
	Content          string             `json:"content"`
	Reasoning        string             `json:"reasoning"`
	ReasoningContent string             `json:"reasoningContent"`
	Level            string             `json:"level"`
	ToolCalls        []previewToolCall  `json:"toolCalls"`
	CallID           string             `json:"callId"`
	ToolCallID       string             `json:"toolCallId"`
	ToolName         string             `json:"toolName"`
	Name             string             `json:"name"`
	Output           string             `json:"output"`
	Compaction       *previewCompaction `json:"compaction"`
	Trigger          string             `json:"trigger"`
	Messages         int                `json:"messages"`
	Summary          string             `json:"summary"`
	Archive          string             `json:"archive"`
}

type previewToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Function  struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type previewCompaction struct {
	Trigger  string `json:"trigger"`
	Messages int    `json:"messages"`
	Summary  string `json:"summary"`
	Archive  string `json:"archive"`
}

func previewEventSessionMessages(path string) ([]HistoryMessage, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	out := []HistoryMessage{}
	toolName := map[string]string{}
	sawEvent := false
	for {
		var rec previewEventRecord
		if err := dec.Decode(&rec); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if sawEvent {
				return out, true, nil
			}
			return nil, false, nil
		}
		eventName := strings.TrimSpace(rec.Kind)
		if eventName == "" {
			eventName = strings.TrimSpace(rec.Type)
		}
		if eventName == "" {
			continue
		}
		sawEvent = true
		switch eventName {
		case "user.message":
			if rec.Text != "" {
				out = append(out, HistoryMessage{Role: "user", Content: rec.Text})
			}
		case "model.final":
			hm := HistoryMessage{Role: "assistant", Content: rec.Content, Reasoning: firstNonEmpty(rec.Reasoning, rec.ReasoningContent)}
			for _, tc := range rec.ToolCalls {
				id := tc.ID
				name := firstNonEmpty(tc.Name, tc.Function.Name)
				args := firstNonEmpty(tc.Arguments, tc.Function.Arguments)
				hm.ToolCalls = append(hm.ToolCalls, HistoryToolCall{ID: id, Name: name, Arguments: args})
				if id != "" {
					toolName[id] = name
				}
			}
			out = append(out, hm)
		case "tool.result":
			callID := firstNonEmpty(rec.CallID, rec.ToolCallID)
			out = append(out, HistoryMessage{
				Role:       "tool",
				ToolCallID: callID,
				ToolName:   firstNonEmpty(rec.ToolName, rec.Name, toolName[callID]),
				Content:    firstNonEmpty(rec.Output, rec.Content),
			})
		case "phase":
			out = append(out, HistoryMessage{Role: "phase", Content: firstNonEmpty(rec.Text, rec.Content)})
		case "notice":
			level := rec.Level
			if level != "warn" {
				level = "info"
			}
			out = append(out, HistoryMessage{Role: "notice", Level: level, Content: firstNonEmpty(rec.Text, rec.Content)})
		case "compaction_started":
			c := rec.compactionPayload()
			out = append(out, HistoryMessage{Role: "compaction", Pending: true, Trigger: c.Trigger})
		case "compaction_done":
			c := rec.compactionPayload()
			out = append(out, HistoryMessage{
				Role:     "compaction",
				Trigger:  c.Trigger,
				Messages: c.Messages,
				Summary:  c.Summary,
				Archive:  c.Archive,
			})
		}
	}
	return out, sawEvent, nil
}

func (r previewEventRecord) compactionPayload() previewCompaction {
	if r.Compaction != nil {
		return *r.Compaction
	}
	return previewCompaction{Trigger: r.Trigger, Messages: r.Messages, Summary: r.Summary, Archive: r.Archive}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// ContextInfo is the prompt-vs-window gauge payload plus session totals. Used
// and Window both zero means no context-window data yet. The Session* fields
// are session-cumulative telemetry so the composer usage chip's hover detail
// and the right-dock turns tab can show aggregate cache hit-rate, token
// splits, and active-turn time without extra polling. SessionElapsedMs is the
// sum of turn durations (in-flight turn included), not wall-clock session age.
type ContextInfo struct {
	Used          int     `json:"used"`
	Window        int     `json:"window"`
	SessionTokens int     `json:"sessionTokens"`
	CompactRatio  float64 `json:"compactRatio,omitempty"`

	SessionPromptTokens     int     `json:"sessionPromptTokens,omitempty"`
	SessionCompletionTokens int     `json:"sessionCompletionTokens,omitempty"`
	SessionReasoningTokens  int     `json:"sessionReasoningTokens,omitempty"`
	SessionCacheHitTokens   int     `json:"sessionCacheHitTokens,omitempty"`
	SessionCacheMissTokens  int     `json:"sessionCacheMissTokens,omitempty"`
	SessionCacheWriteTokens int     `json:"sessionCacheWriteTokens,omitempty"`
	RequestCount            int     `json:"requestCount,omitempty"`
	SessionCost             float64 `json:"sessionCost,omitempty"`
	SessionCostCurrency     string  `json:"sessionCostCurrency,omitempty"`
	SessionElapsedMs        int64   `json:"sessionElapsedMs,omitempty"`
}

// ContextUsage returns the latest context-window gauge numbers.
func (a *App) ContextUsage() ContextInfo {
	return a.ContextUsageForTab("")
}

// BudgetStatus reports the main provider's rate-limit window (rpm used /
// remaining + seconds until reset) for the composer status row. Zeroed (RPM=0)
// when limiting is disabled — the frontend hides the indicator then.
func (a *App) BudgetStatus() provider.BudgetStatus {
	return boot.GlobalBudget().MainStatus()
}

func (a *App) ContextUsageForTab(tabID string) ContextInfo {
	a.mu.RLock()
	tab := a.tabByIDLocked(tabID)
	var ctrl tabSession
	if tab != nil {
		ctrl = tab.Ctrl
	}
	a.mu.RUnlock()

	info := ContextInfo{}
	if tab != nil {
		telemetry := tab.telemetrySnapshot().Usage
		info.SessionTokens = telemetry.TotalTokens
		info.SessionPromptTokens = telemetry.PromptTokens
		info.SessionCompletionTokens = telemetry.CompletionTokens
		info.SessionReasoningTokens = telemetry.ReasoningTokens
		info.SessionCacheHitTokens = telemetry.CacheHitTokens
		info.SessionCacheMissTokens = telemetry.CacheMissTokens
		info.SessionCacheWriteTokens = telemetry.CacheWriteTokens
		if telemetry.Cost > 0 {
			info.SessionCost = math.Round(telemetry.Cost*10000) / 10000
			info.SessionCostCurrency = telemetry.Currency
		}
		info.RequestCount = telemetry.RequestCount
		info.SessionElapsedMs = telemetry.ElapsedMs
	}
	if ctrl == nil {
		return info
	}
	used, window := ctrl.ContextSnapshot()
	info.Used = used
	info.Window = window
	info.CompactRatio = ctrl.CompactRatio()
	return info
}

// JobView is one running background job (bash/task started with
// run_in_background) for the status-bar indicator.
type JobView struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Status    string `json:"status"`
	StartedAt int64  `json:"startedAt"`
}

// Jobs returns the still-running background jobs for the status bar. It refreshes
// on demand (mount, turn end, and on each notice the frontend receives).
func (a *App) Jobs() []JobView {
	out := []JobView{}
	ctrl := a.ctrlByTabID("")
	return a.jobsForCtrl(ctrl, out)
}

func (a *App) JobsForTab(tabID string) []JobView {
	out := []JobView{}
	ctrl := a.ctrlByTabID(tabID)
	return a.jobsForCtrl(ctrl, out)
}

func (a *App) jobsForCtrl(ctrl tabSession, out []JobView) []JobView {
	if ctrl == nil {
		return out
	}
	for _, v := range ctrl.Jobs() {
		out = append(out, JobView{ID: v.ID, Kind: v.Kind, Label: v.Label, Status: v.Status, StartedAt: v.StartedAt})
	}
	return out
}

// Meta describes the session for the frontend's header and status line.
type Meta struct {
	Label            string `json:"label"`
	Ready            bool   `json:"ready"`
	StartupErr       string `json:"startupErr,omitempty"`
	EventChannel     string `json:"eventChannel"`
	Cwd              string `json:"cwd"`
	AutoApproveTools bool   `json:"autoApproveTools"`
	Bypass           bool   `json:"bypass"` // legacy JSON key for YOLO/full-access tool auto-approval
	ToolApprovalMode string `json:"toolApprovalMode"`
	RagScope         string `json:"ragScope,omitempty"`
	Goal             string `json:"goal,omitempty"`
	GoalStatus       string `json:"goalStatus,omitempty"`
	// ExpertSession is set when this tab is an expert-team collaboration session.
	ExpertSession *ExpertSessionMeta `json:"expertSession,omitempty"`
}

// Meta reports the model label, readiness, any startup error, the working
// directory (for the status line), and the runtime event channel the frontend
// subscribes to.
func (a *App) Meta() Meta {
	return a.MetaForTab("")
}

func (a *App) MetaForTab(tabID string) Meta {
	// Snapshot ctrl pointer + scalar fields under a single RLock to avoid torn
	// reads. Controller methods are called AFTER releasing the lock to avoid
	// deadlock (controller internals may also acquire a.mu).
	a.mu.RLock()
	tab := a.tabByIDLocked(tabID)
	if tab == nil {
		a.mu.RUnlock()
		return Meta{EventChannel: eventChannel}
	}
	ctrl := tab.Ctrl
	label := tab.Label
	ready := tab.Ready
	startupErr := tab.StartupErr
	cwd := tab.WorkspaceRoot
	goal := tab.goal
	toolApprovalMode := tab.toolApprovalMode
	ragScope := tab.ragScope
	isExpert := tab.IsExpertSession
	expertTeamID := tab.ExpertTeamID
	expertTeamName := tab.ExpertTeamName
	a.mu.RUnlock()

	// Resolve the team's current name (it may have been renamed since the tab
	// was created/restored). Falls back to the persisted name if the team was
	// deleted or the store is offline.
	if isExpert && expertTeamID != "" {
		if fresh := a.teamDisplayName(expertTeamID); fresh != expertTeamID {
			expertTeamName = fresh
		}
	}

	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	autoApproveTools := ctrl != nil && ctrl.AutoApproveTools()
	if ctrl != nil {
		toolApprovalMode = ctrl.ToolApprovalMode()
		goal = ctrl.Goal()
	}
	var goalStatus string
	if ctrl != nil {
		goalStatus = ctrl.GoalStatus()
	} else if strings.TrimSpace(goal) != "" {
		goalStatus = control.GoalStatusRunning
	} else {
		goalStatus = control.GoalStatusStopped
	}
	if toolApprovalMode == "" {
		toolApprovalMode = control.ToolApprovalAsk
	}
	var expertSession *ExpertSessionMeta
	if isExpert {
		expertSession = &ExpertSessionMeta{TeamID: expertTeamID, TeamName: expertTeamName}
	}
	return Meta{
		Label:            label,
		Ready:            ready,
		StartupErr:       startupErr,
		EventChannel:     eventChannel,
		Cwd:              cwd,
		AutoApproveTools: autoApproveTools,
		Bypass:           autoApproveTools,
		ToolApprovalMode: toolApprovalMode,
		RagScope:         ragScope,
		Goal:             goal,
		GoalStatus:       goalStatus,
		ExpertSession:    expertSession,
	}
}

func (a *App) SetGoal(goal string) {
	a.SetGoalForTab("", goal)
}

func (a *App) SetGoalForTab(tabID, goal string) {
	goal = strings.TrimSpace(goal)
	a.mu.Lock()
	tab := a.tabByIDLocked(tabID)
	if tab == nil {
		a.mu.Unlock()
		return
	}
	tab.goal = goal
	if goal != "" {
		tab.mode = tabModeFromAxes(false, currentTabToolApprovalMode(tab) == control.ToolApprovalYolo)
	}
	ctrl := tab.Ctrl
	plan := tabModeHasPlan(tab.mode)
	tabIDForSave := tab.ID
	a.mu.Unlock()
	if ctrl != nil {
		ctrl.SetPlanMode(plan)
		ctrl.SetGoal(goal)
	}
	a.mu.Lock()
	if a.tabs[tabIDForSave] == tab {
		a.saveTabsLocked()
	}
	a.mu.Unlock()
}

func (a *App) ClearGoal() {
	a.SetGoal("")
}

func (a *App) ClearGoalForTab(tabID string) {
	a.SetGoalForTab(tabID, "")
}

// SetAutoApproveTools toggles YOLO/full-access tool auto-approval:
// approval-gated tool calls run without asking, while ask questions and plan
// approvals still wait for the user. Runtime-only — not written to config.
func (a *App) SetAutoApproveTools(on bool) {
	if on {
		a.SetToolApprovalModeForTab("", control.ToolApprovalYolo)
		return
	}
	a.SetToolApprovalModeForTab("", control.ToolApprovalAsk)
}

// SetBypass is the legacy Wails binding for SetAutoApproveTools.
func (a *App) SetBypass(on bool) {
	a.SetAutoApproveTools(on)
}

func (a *App) SetToolApprovalMode(mode string) {
	a.SetToolApprovalModeForTab("", mode)
}

func (a *App) SetToolApprovalModeForTab(tabID, mode string) {
	mode = normalizeToolApprovalMode(mode)
	a.mu.Lock()
	tab := a.tabByIDLocked(tabID)
	if tab == nil {
		a.mu.Unlock()
		return
	}
	tab.toolApprovalMode = mode
	tab.mode = tabModeFromAxes(tabModeHasPlan(currentTabMode(tab)), mode == control.ToolApprovalYolo)
	ctrl := tab.Ctrl
	tabIDForSave := tab.ID
	a.mu.Unlock()
	applyTabToolApprovalModeToController(ctrl, mode)
	a.mu.Lock()
	if a.tabs[tabIDForSave] == tab {
		a.saveTabsLocked()
	}
	a.mu.Unlock()
}

// SetRagScope sets the knowledge-base auto-injection scope for the active tab
// ("" = 不使用, the default). Wired from the Composer "知识库" dropdown.
func (a *App) SetRagScope(scope string) {
	a.SetRagScopeForTab("", scope)
}

// SetRagScopeForTab sets the knowledge-base auto-injection scope for a tab,
// applies it to the live controller, and persists the selection. Mirrors
// SetToolApprovalModeForTab.
func (a *App) SetRagScopeForTab(tabID, scope string) {
	scope = strings.TrimSpace(scope)
	a.mu.Lock()
	tab := a.tabByIDLocked(tabID)
	if tab == nil {
		a.mu.Unlock()
		return
	}
	tab.ragScope = scope
	ctrl := tab.Ctrl
	tabIDForSave := tab.ID
	a.mu.Unlock()
	applyTabRagScopeToController(ctrl, scope)
	a.mu.Lock()
	if a.tabs[tabIDForSave] == tab {
		a.saveTabsLocked()
	}
	a.mu.Unlock()
}

// CommandInfo describes one available slash command for the composer's "/" menu.
type CommandInfo struct {
	Name        string `json:"name"` // without the leading slash
	Description string `json:"description"`
	Hint        string `json:"hint,omitempty"` // argument hint, if any
	Kind        string `json:"kind"`           // "builtin" | "custom" | "mcp"
}

// Commands lists the slash commands available this session — built-in actions,
// custom commands (.fairpeer/commands), and MCP prompts — for the composer's "/"
// autocomplete menu.
func (a *App) Commands() []CommandInfo {
	out := []CommandInfo{
		{Name: "new", Description: i18n.M.CmdNew, Kind: "builtin"},
		{Name: "clear", Description: i18n.M.CmdClear, Kind: "builtin"},
		{Name: "compact", Description: i18n.M.CmdCompact, Kind: "builtin"},
		{Name: "model", Description: i18n.M.CmdModel, Kind: "builtin"},
		{Name: "provider", Description: i18n.M.CmdProvider, Kind: "builtin"},
		{Name: "effort", Description: i18n.M.CmdEffort, Kind: "builtin"},
		{Name: "memory", Description: i18n.M.CmdMemory, Kind: "builtin"},
		{Name: "goal", Description: i18n.M.CmdGoal, Kind: "builtin"},
		{Name: "remember", Description: i18n.M.CmdRemember, Kind: "builtin"},
		{Name: "mcp", Description: i18n.M.CmdMcp, Kind: "builtin"},
		{Name: "hooks", Description: i18n.M.CmdHooks, Kind: "builtin"},
		{Name: "theme", Description: i18n.M.CmdTheme, Kind: "builtin"},
		{Name: "skill", Description: i18n.M.CmdSkill, Kind: "builtin"},
	}
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return out
	}
	// Skills are invocable as /<name> (the model runs inline ones; subagent ones
	// run isolated). Listing them here is what surfaces /init, /explore, … in the
	// composer's slash menu; selecting one submits "/<name>", which the controller
	// resolves via RunSkill.
	for _, s := range ctrl.Skills() {
		out = append(out, CommandInfo{Name: s.Name, Description: s.Description, Kind: "skill"})
	}
	for _, c := range ctrl.Commands() {
		out = append(out, CommandInfo{Name: c.Name, Description: c.Description, Hint: c.ArgHint, Kind: "custom"})
	}
	if h := ctrl.Host(); h != nil {
		for _, p := range h.Prompts() {
			out = append(out, CommandInfo{Name: p.Name, Description: p.Description, Kind: "mcp"})
		}
	}
	return out
}

// SlashArgItem is one sub-command / argument suggestion for the composer's slash
// menu (the part after the command word). Mirrors the CLI's arg completion via
// the shared control.SlashArgItems, so desktop and CLI offer the same hints.
type SlashArgItem struct {
	Label   string `json:"label"`
	Insert  string `json:"insert"`
	Hint    string `json:"hint"`
	Descend bool   `json:"descend"`
}

// SlashArgsResult carries the suggestions plus the byte offset in the input where
// the current token begins, so the composer replaces just that token.
type SlashArgsResult struct {
	Items []SlashArgItem `json:"items"`
	From  int            `json:"from"`
}

// SlashArgs completes the arguments of a management slash command (/mcp, /model,
// /skill, /hooks) for the composer — the same logic the chat TUI uses. Empty
// Items means the input has no structured arguments to complete.
func (a *App) SlashArgs(input string) SlashArgsResult {
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	model := ""
	if tab := a.activeTabLocked(); tab != nil {
		model = tab.model
	}
	a.mu.RUnlock()
	if ctrl == nil {
		return SlashArgsResult{Items: []SlashArgItem{}}
	}
	data := control.ArgData{
		Skills:          ctrl.Skills(),
		DisabledSkills:  ctrl.DisabledSkills(),
		ConfiguredMCP:   ctrl.ConfiguredMCPNames(),
		DisconnectedMCP: ctrl.DisconnectedMCPNames(),
		CurrentModel:    model,
	}
	seen := map[string]bool{}
	for _, m := range a.Models() {
		data.ModelRefs = append(data.ModelRefs, m.Ref)
		if m.Provider != "" && !seen[m.Provider] {
			seen[m.Provider] = true
			data.ProviderNames = append(data.ProviderNames, m.Provider)
		}
		if m.Current {
			data.CurrentProvider = m.Provider
		}
	}
	if h := ctrl.Host(); h != nil {
		data.ServerNames = h.ServerNames()
	}
	items, from := control.SlashArgItems(input, data)
	// Non-nil so it serializes as a JSON array, never null — the frontend filters
	// over it directly.
	out := SlashArgsResult{Items: []SlashArgItem{}, From: from}
	for _, it := range items {
		out.Items = append(out.Items, SlashArgItem{Label: it.Label, Insert: it.Insert, Hint: it.Hint, Descend: it.Descend})
	}
	return out
}

// CapabilitiesView is the MCP & Skills drawer's data: connected/failed MCP
// servers and the discoverable skills, the GUI counterpart to `/mcp` + `/skill`.
type CapabilitiesView struct {
	Servers    []ServerView    `json:"servers"`
	Skills     []SkillView     `json:"skills"`
	SkillRoots []SkillRootView `json:"skillRoots"`
}

// ServerView is one MCP server for the drawer. Status is "connected" (with
// tool/prompt/resource counts), "deferred" (lazy/on-demand startup enabled),
// "failed" (with the connection error), "initializing" (background startup in
// progress), or "disabled".
type ServerView struct {
	Name           string     `json:"name"`
	Transport      string     `json:"transport"`
	Status         string     `json:"status"`
	BuiltIn        bool       `json:"builtIn,omitempty"`
	Configured     bool       `json:"configured,omitempty"`
	AutoStart      bool       `json:"autoStart"`
	Tier           string     `json:"tier,omitempty"`
	Command        string     `json:"command,omitempty"`
	Args           []string   `json:"args,omitempty"`
	URL            string     `json:"url,omitempty"`
	EnvKeys        []string   `json:"envKeys,omitempty"`
	Tools          int        `json:"tools"`
	Prompts        int        `json:"prompts"`
	Resources      int        `json:"resources"`
	Error          string     `json:"error,omitempty"`
	ToolList       []ToolView `json:"toolList,omitempty"`
	AuthStatus     string     `json:"authStatus,omitempty"`
	AuthURL        string     `json:"authUrl,omitempty"`
	AuthConfigured bool       `json:"authConfigured,omitempty"`
	// ProfileHidden: the ACTIVE tab's profile gates this server out (netdev
	// allowlist / named HiddenPlugins). The settings page shows it as hidden in
	// this mode instead of a misleading initializing/deferred status.
	ProfileHidden bool `json:"profileHidden,omitempty"`
}

type ToolView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SkillView is one discoverable skill for the drawer.
type SkillView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`
	RunAs       string `json:"runAs"`
	Enabled     bool   `json:"enabled"`
	// Active reports whether the skill is in effect under the current product
	// profile's skill set: true when it is surfaced to the model (in the pinned
	// index / callable), false when the active profile's whitelist hides it. This
	// is distinct from Enabled (a user toggle): a profile-hidden skill can be
	// Enabled=true yet Active=false. The skills page uses this to separate "in
	// effect for this mode" from "available after switching mode".
	Active bool `json:"active"`
	// InstalledFrom is the marketplace source URL when the skill was installed
	// via install_source (empty for builtins and manually created skills).
	InstalledFrom string `json:"installedFrom,omitempty"`
}

type SkillRootSkillView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`
	RunAs       string `json:"runAs"`
}

// SkillRootView is one skill discovery root for the drawer's Sources section.
type SkillRootView struct {
	Dir        string               `json:"dir"`
	Scope      string               `json:"scope"`
	Priority   int                  `json:"priority"`
	Status     string               `json:"status"`
	Configured bool                 `json:"configured"`
	Removable  bool                 `json:"removable"`
	Skills     int                  `json:"skills"`
	SkillItems []SkillRootSkillView `json:"skillItems,omitempty"`
	Warning    string               `json:"warning,omitempty"`
}

// capabilitiesProfile resolves the active tab's profile for per-mode MCP
// visibility annotation. Returns nil for dev (the superset — nothing hidden)
// and when the profile can't be resolved (annotation is best-effort; the boot
// and hot-connect gates are the enforcement, not this view).
func (a *App) capabilitiesProfile(loadedCfg *config.Config, profileName string) *config.Profile {
	if profileName == "" || strings.EqualFold(strings.TrimSpace(profileName), config.ProfileDev) {
		return nil
	}
	cfg := loadedCfg
	if cfg == nil {
		if c, err := config.Load(); err == nil {
			cfg = c
		}
	}
	if cfg == nil {
		return nil
	}
	p, err := cfg.ResolveProfile(profileName)
	if err != nil {
		return nil
	}
	return p
}

// Capabilities projects the session's MCP servers (connected + failed) and skills
// for the MCP & Skills drawer. Non-nil slices so the frontend can map over them.
func (a *App) Capabilities() CapabilitiesView {
	out := CapabilitiesView{Servers: []ServerView{}, Skills: []SkillView{}, SkillRoots: []SkillRootView{}}
	// Snapshot all tab fields we need under the read lock, THEN release. Reading
	// tab.disabledMCP (a map) outside the lock races with RemoveMCPServer's
	// delete() under a write lock — that is a concurrent map read/write crash.
	var ctrl tabSession
	disabled := map[string]ServerView{}
	var order []string
	var workspaceRoot string
	var profileName string
	a.mu.RLock()
	if tab := a.activeTabLocked(); tab != nil {
		ctrl = tab.Ctrl
		workspaceRoot = tab.WorkspaceRoot
		profileName = tab.profile
		for name, s := range tab.disabledMCP {
			disabled[name] = s
		}
		order = append([]string(nil), tab.mcpOrder...)
	}
	a.mu.RUnlock()
	if ctrl == nil {
		return out
	}
	seen := map[string]bool{}
	connected := map[string]bool{}
	retainedDisabled := map[string]ServerView{}
	var loadedCfg *config.Config
	configured := map[string]config.PluginEntry{}
	var configuredEntries []config.PluginEntry
	if cfg, err := config.LoadForRoot(workspaceRoot); err == nil {
		loadedCfg = cfg
		configuredEntries = append(configuredEntries, cfg.Plugins...)
		for _, p := range configuredEntries {
			configured[p.Name] = p
		}
	}
	if h := ctrl.Host(); h != nil {
		for _, s := range h.Servers() {
			if seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			connected[s.Name] = true
			view := ServerView{
				Name: s.Name, Transport: s.Transport, Status: "connected",
				BuiltIn: s.Name == "codegraph",
				Tools:   s.Tools, Prompts: s.Prompts, Resources: s.Resources,
				ToolList: pluginToolsToView(s.ToolList),
			}
			if p, ok := configured[s.Name]; ok {
				view = withPluginConfig(view, p)
			} else if s.Name == "codegraph" && loadedCfg != nil {
				view = withCodegraphConfig(view, loadedCfg.Codegraph)
			}
			out.Servers = append(out.Servers, view)
		}
		for _, f := range h.Failures() {
			if seen[f.Name] {
				continue
			}
			seen[f.Name] = true
			view := ServerView{
				Name: f.Name, Transport: f.Transport, Status: "failed", BuiltIn: f.Name == "codegraph", Error: f.Error,
			}
			if p, ok := configured[f.Name]; ok {
				view = withPluginConfig(view, p)
			} else if f.Name == "codegraph" && loadedCfg != nil {
				view = withCodegraphConfig(view, loadedCfg.Codegraph)
			}
			out.Servers = append(out.Servers, view)
		}
	}
	// Configured servers that are neither connected nor failed are either lazy
	// (deferred), background/eager (initializing), or toggled off this session.
	if len(configuredEntries) > 0 || loadedCfg != nil {
		for _, p := range configuredEntries {
			if seen[p.Name] {
				continue
			}
			if s, ok := disabled[p.Name]; ok {
				s.Status = "disabled"
				s = withPluginConfig(s, p)
				s.Error = ""
				out.Servers = append(out.Servers, s)
				retainedDisabled[p.Name] = s
				seen[p.Name] = true
				delete(disabled, p.Name)
				continue
			}
			status := "disabled"
			if p.ShouldAutoStart() {
				switch p.ResolvedTier() {
				case "background", "eager":
					status = "initializing"
				default:
					status = "deferred"
				}
			}
			out.Servers = append(out.Servers, withPluginConfig(ServerView{Name: p.Name, Status: status}, p))
			seen[p.Name] = true
		}
		if loadedCfg != nil && !seen["codegraph"] {
			status := "disabled"
			if loadedCfg.Codegraph.Enabled {
				status = "initializing"
			}
			if s, ok := disabled["codegraph"]; ok {
				s.Status = "disabled"
				s.Transport = "stdio"
				s.BuiltIn = true
				s = withCodegraphConfig(s, loadedCfg.Codegraph)
				s.Error = ""
				out.Servers = append(out.Servers, s)
				retainedDisabled["codegraph"] = s
				delete(disabled, "codegraph")
			} else {
				out.Servers = append(out.Servers, withCodegraphConfig(ServerView{Name: "codegraph", Status: status}, loadedCfg.Codegraph))
			}
			seen["codegraph"] = true
		}
	}
	out.Servers = orderServerViews(out.Servers, order)

	// Annotate per-profile visibility (mirrors the skills tab's Active flag):
	// a server gated out by the ACTIVE tab's profile shows as hidden in this
	// mode. dev is the superset and skips annotation.
	if prof := a.capabilitiesProfile(loadedCfg, profileName); prof != nil {
		for i := range out.Servers {
			if !config.PluginAllowedByProfile(prof, out.Servers[i].Name) || config.PluginHiddenByProfile(prof, out.Servers[i].Name) {
				out.Servers[i].ProfileHidden = true
			}
		}
	}

	a.mu.Lock()
	if tab := a.activeTabLocked(); tab != nil {
		for name := range connected {
			delete(retainedDisabled, name)
		}
		tab.disabledMCP = retainedDisabled
		tab.mcpOrder = mergeServerOrder(tab.mcpOrder, out.Servers)
	}
	a.mu.Unlock()

	// Resolve the active profile's skill whitelist (if any) so each SkillView can
	// report whether it is in effect for the current mode. Mirrors boot.go's
	// profileSkillWhitelist: an empty/non-existent EnabledSkills list means "all
	// skills active"; a non-empty list is a whitelist and anything outside it is
	// hidden by the profile (Active=false). Must stay in lockstep with the index
	// logic in internal/boot/boot.go.
	var profileWhitelist map[string]bool
	if cfg, err := config.Load(); err == nil {
		if prof, perr := cfg.ResolveProfile(strings.TrimSpace(profileName)); perr == nil && len(prof.EnabledSkills) > 0 {
			profileWhitelist = make(map[string]bool, len(prof.EnabledSkills))
			for _, n := range prof.EnabledSkills {
				profileWhitelist[config.SkillNameKey(n)] = true
			}
		}
	}
	installedSkillSources, _ := a.SkillMarketInstalledNames()
	for _, s := range ctrl.AllSkills() {
		enabled := ctrl.SkillEnabled(s.Name)
		// A profile whitelist hides skills not named in it. User-disabled skills
		// are Enabled=false already; profile-hidden ones are Enabled=true but
		// Active=false.
		active := enabled
		if active && profileWhitelist != nil && !profileWhitelist[config.SkillNameKey(s.Name)] {
			active = false
		}
		out.Skills = append(out.Skills, SkillView{
			Name: s.Name, Description: s.Description,
			Scope: string(s.Scope), RunAs: string(s.RunAs),
			Enabled: enabled, Active: active,
			InstalledFrom: installedSkillSources[s.Name],
		})
	}
	out.SkillRoots = skillRootsView()
	return out
}

// DreamRunView is one Dream/Distill run record for the settings panel.
type DreamRunView struct {
	Kind      string `json:"kind"`
	Trigger   string `json:"trigger"`
	StartedAt string `json:"startedAt"`
	Duration  string `json:"duration,omitempty"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

// DreamStatusView is the self-evolution panel's data: the live config (master
// switch + cadence), the most recent run of each kind, and whether one is in
// flight. The "last run" times come from the on-disk dream_state.json so manual
// and automatic runs are both reflected.
type DreamStatusView struct {
	Enabled         bool           `json:"enabled"`
	DreamInterval   int            `json:"dreamInterval"`
	DistillInterval int            `json:"distillInterval"`
	DreamInFlight   bool           `json:"dreamInFlight"`
	DistillInFlight bool           `json:"distillInFlight"`
	LastDream       *DreamRunView  `json:"lastDream,omitempty"`
	LastDistill     *DreamRunView  `json:"lastDistill,omitempty"`
	History         []DreamRunView `json:"history"`
}

// DreamStatus returns the self-evolution status for the active session's panel.
func (a *App) DreamStatus() DreamStatusView {
	view := DreamStatusView{History: []DreamRunView{}}
	if cfg, err := config.Load(); err == nil {
		view.Enabled = cfg.Dream.Enabled
		view.DreamInterval = cfg.Dream.DreamIntervalDays()
		view.DistillInterval = cfg.Dream.DistillIntervalDays()
	} else {
		d := config.Default().Dream
		view.Enabled, view.DreamInterval, view.DistillInterval = d.Enabled, d.DreamIntervalDays(), d.DistillIntervalDays()
	}
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	view.DreamInFlight = agent.DreamInFlight(agent.KindDream)
	view.DistillInFlight = agent.DreamInFlight(agent.KindDistill)
	if ctrl == nil {
		return view
	}
	if r, ok := ctrl.LastDreamRun(agent.KindDream); ok {
		view.LastDream = dreamRunView(r)
	}
	if r, ok := ctrl.LastDreamRun(agent.KindDistill); ok {
		view.LastDistill = dreamRunView(r)
	}
	for _, r := range agent.DreamHistory(ctrl.SessionDir(), agent.KindDream) {
		view.History = append(view.History, *dreamRunView(r))
	}
	for _, r := range agent.DreamHistory(ctrl.SessionDir(), agent.KindDistill) {
		view.History = append(view.History, *dreamRunView(r))
	}
	return view
}

func dreamRunView(r agent.DreamRun) *DreamRunView {
	return &DreamRunView{
		Kind:      string(r.Kind),
		Trigger:   string(r.Trigger),
		StartedAt: r.StartedAt.UTC().Format(time.RFC3339),
		Duration:  r.Duration,
		Status:    r.Status,
		Error:     r.Error,
	}
}

// SetDreamEnabled toggles the self-evolution master switch in the config file.
func (a *App) SetDreamEnabled(enabled bool) error {
	return a.applyConfigChange(func(cfg *config.Config) error {
		cfg.SetDreamEnabled(enabled)
		return nil
	})
}

// SetDreamIntervals sets the Dream and Distill cadence (days) in the config.
func (a *App) SetDreamIntervals(dreamDays, distillDays int) error {
	return a.applyConfigChange(func(cfg *config.Config) error {
		return cfg.SetDreamIntervals(dreamDays, distillDays)
	})
}

// TriggerDream runs a Dream consolidation pass now (blocking) and returns the
// outcome. The frontend uses this for the "run now" button.
func (a *App) TriggerDream() (DreamRunView, error) {
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return DreamRunView{}, fmt.Errorf("no active session")
	}
	r, ran := ctrl.TriggerDream(context.Background())
	if !ran {
		return DreamRunView{}, fmt.Errorf("dream did not run: %s", r.Error)
	}
	return *dreamRunView(r), nil
}

// TriggerDistill runs a Distill workflow-extraction pass now (blocking).
func (a *App) TriggerDistill() (DreamRunView, error) {
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return DreamRunView{}, fmt.Errorf("no active session")
	}
	r, ran := ctrl.TriggerDistill(context.Background())
	if !ran {
		return DreamRunView{}, fmt.Errorf("distill did not run: %s", r.Error)
	}
	return *dreamRunView(r), nil
}

func withPluginConfig(v ServerView, p config.PluginEntry) ServerView {
	tt := p.Type
	if tt == "" {
		tt = "stdio"
	}
	v.Transport = tt
	v.Configured = true
	v.AutoStart = p.ShouldAutoStart()
	v.Tier = p.ResolvedTier()
	v.Command = p.Command
	v.Args = append([]string(nil), p.Args...)
	v.URL = p.URL
	v.AuthConfigured = mcpdiag.HasAuthConfig(p.Headers, p.Env, p.URL)
	if len(p.Env) > 0 {
		v.EnvKeys = make([]string, 0, len(p.Env))
		for k := range p.Env {
			v.EnvKeys = append(v.EnvKeys, k)
		}
		sort.Strings(v.EnvKeys)
	}
	auth := mcpdiag.DiagnoseAuth(v.Transport, v.Status, v.Error, v.URL, v.AuthConfigured)
	v.AuthStatus = auth.Status
	v.AuthURL = auth.URL
	return v
}

func withCodegraphConfig(v ServerView, c config.CodegraphConfig) ServerView {
	v.Name = "codegraph"
	v.Transport = "stdio"
	v.BuiltIn = true
	v.Configured = true
	v.AutoStart = c.ShouldAutoStart()
	v.Tier = c.ResolvedTier()
	v.AuthStatus = mcpdiag.AuthNone
	return v
}

func skillRootsView() []SkillRootView {
	cwd, _ := os.Getwd()
	cfg, _ := config.Load()
	userCfg := config.LoadForEdit(config.UserConfigPath())
	var custom []string
	var excluded []string
	maxDepth := 3
	if cfg != nil {
		custom = cfg.SkillCustomPaths()
		excluded = cfg.SkillExcludedPaths()
		maxDepth = cfg.SkillMaxDepth()
	}
	st := skill.New(skill.Options{ProjectRoot: cwd, CustomPaths: custom, ExcludedPaths: excluded, MaxDepth: maxDepth, DisableBuiltins: true, Stderr: io.Discard})
	counts := map[string]int{}
	skillItems := map[string][]SkillRootSkillView{}
	roots := st.Roots()
	for _, sk := range st.List() {
		root := skillDisplayRoot(sk, roots)
		counts[root]++
		skillItems[root] = append(skillItems[root], SkillRootSkillView{
			Name:        sk.Name,
			Description: sk.Description,
			Scope:       string(sk.Scope),
			RunAs:       string(sk.RunAs),
		})
	}
	for root := range skillItems {
		sort.Slice(skillItems[root], func(i, j int) bool {
			return skillItems[root][i].Name < skillItems[root][j].Name
		})
	}
	userConfigured := map[string]bool{}
	if userCfg != nil {
		for _, p := range userCfg.Skills.Paths {
			userConfigured[config.CanonicalSkillPath(p)] = true
		}
	}
	out := []SkillRootView{}
	for _, r := range roots {
		dir := config.CanonicalSkillPath(r.Dir)
		view := SkillRootView{
			Dir:        r.Dir,
			Scope:      string(r.Scope),
			Priority:   r.Priority + 1,
			Status:     string(r.Status),
			Configured: r.Scope == skill.ScopeCustom && userConfigured[dir],
			Removable:  true,
			Skills:     counts[dir],
			SkillItems: skillItems[dir],
		}
		out = append(out, view)
	}
	if userCfg != nil {
		for _, p := range userCfg.Skills.Paths {
			if rootActive(out, p) {
				continue
			}
			out = append(out, SkillRootView{
				Dir:        p,
				Scope:      string(skill.ScopeCustom),
				Status:     "inactive",
				Configured: true,
				Removable:  true,
				Warning:    "configured in user config but not active in this workspace; project [skills].paths may override it",
			})
		}
	}
	return out
}

func rootActive(roots []SkillRootView, path string) bool {
	want := config.CanonicalSkillPath(path)
	for _, r := range roots {
		if r.Scope == string(skill.ScopeCustom) && config.CanonicalSkillPath(r.Dir) == want {
			return true
		}
	}
	return false
}

// PickSkillFolder opens a directory picker for adding custom skill roots. It only
// returns a path; AddSkillPath performs normalization and writes config.
func (a *App) PickSkillFolder() (string, error) {
	if a.ctx == nil {
		return "", nil
	}
	cur, _ := os.Getwd()
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "Choose skills folder",
		DefaultDirectory: dialogDefaultDirectory(cur),
	})
	if err != nil || dir == "" {
		return "", err
	}
	return normalizeSkillPath(dir), nil
}

// PickDirectory opens a system directory picker and returns the chosen path.
// Used by settings fields that would otherwise be "fill-in-the-blank" path
// inputs (sandbox workspace_root, bot workspace_root, allow_write paths): a
// folder picker is far less error-prone than hand-typing an absolute path.
// Returns ("", nil) if the user cancels. title localizes the dialog caption.
func (a *App) PickDirectory(title string) (string, error) {
	if a.ctx == nil {
		return "", nil
	}
	cur, _ := os.Getwd()
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            title,
		DefaultDirectory: dialogDefaultDirectory(cur),
	})
	if err != nil || dir == "" {
		return "", err
	}
	return dir, nil
}

// AddSkillPath adds a custom skill root to the user config and rebuilds the
// controller so the skills index and slash menu reflect it immediately.
func (a *App) AddSkillPath(path string) error {
	path = normalizeSkillPath(path)
	workspaceRoot := a.activeWorkspaceRoot()
	return a.applyConfigChange(func(c *config.Config) error {
		if isConventionSkillRoot(path, workspaceRoot) {
			return c.RestoreSkillPath(path)
		}
		return c.AddSkillPath(path)
	})
}

// RemoveSkillPath removes a skill source from the user config and rebuilds. For
// convention roots, it records a pseudo-delete in excluded_paths.
func (a *App) RemoveSkillPath(path string) error {
	path = normalizeSkillPath(path)
	return a.applyConfigChange(func(c *config.Config) error {
		removed, err := c.RemoveSkillPath(path)
		if err != nil || removed {
			return err
		}
		return c.ExcludeSkillPath(path)
	})
}

// RefreshSkills rebuilds the controller without changing config, reloading skill
// discovery, the system prompt index, and slash completions.
func (a *App) RefreshSkills() error {
	return a.rebuild()
}

// SetSkillEnabled persists a skill toggle and rebuilds the controller so the
// prompt index, slash menu, and skill tools reflect it immediately.
func (a *App) SetSkillEnabled(name string, enabled bool) error {
	return a.applyConfigChange(func(c *config.Config) error {
		return c.SetSkillEnabled(name, enabled)
	})
}

// DeriveEditableSkill writes an editable file copy of a BUILT-IN skill to
// ~/.fairpeer/skills/<name>/SKILL.md. File skills shadow built-ins by name
// (store precedence: project → custom → global → builtin), so the copy takes
// effect on the rebuild below and the user can edit the prompt in place;
// deleting the file restores the built-in. Returns the written path.
func (a *App) DeriveEditableSkill(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("skill name is required")
	}
	cwd, _ := os.Getwd()
	st := skill.New(skill.Options{ProjectRoot: cwd, MaxDepth: 3, Stderr: io.Discard})
	sk, ok := st.Read(name)
	if !ok {
		return "", fmt.Errorf("skill %q not found", name)
	}
	if sk.Scope != skill.ScopeBuiltin {
		return "", fmt.Errorf("%q is already a file skill — edit %s directly", name, sk.Path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".fairpeer", "skills", name)
	target := filepath.Join(dir, skill.SkillFile)
	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("an editable copy already exists: %s — edit it directly", target)
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", sk.Name)
	if d := strings.TrimSpace(sk.Description); d != "" {
		fmt.Fprintf(&b, "description: %s\n", strings.ReplaceAll(d, "\n", " "))
	}
	fmt.Fprintf(&b, "runAs: %s\n", sk.RunAs)
	if sk.Model != "" {
		fmt.Fprintf(&b, "model: %s\n", sk.Model)
	}
	if sk.Effort != "" {
		fmt.Fprintf(&b, "effort: %s\n", sk.Effort)
	}
	if sk.MaxSteps > 0 {
		fmt.Fprintf(&b, "max-steps: %d\n", sk.MaxSteps)
	}
	if len(sk.AllowedTools) > 0 {
		fmt.Fprintf(&b, "allowed-tools: %s\n", strings.Join(sk.AllowedTools, ", "))
	}
	b.WriteString("---\n\n")
	b.WriteString(sk.Body)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	if err := a.rebuild(); err != nil {
		return target, fmt.Errorf("editable copy written but controller rebuild failed: %w", err)
	}
	return target, nil
}

func normalizeSkillPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				path = home
			} else {
				path = filepath.Join(home, path[2:])
			}
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	info, err := os.Stat(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if info.Mode().IsRegular() {
		if filepath.Base(path) == skill.SkillFile {
			return filepath.Clean(filepath.Dir(filepath.Dir(path)))
		}
		return filepath.Clean(filepath.Dir(path))
	}
	if info.IsDir() {
		if _, err := os.Stat(filepath.Join(path, skill.SkillFile)); err == nil {
			return filepath.Clean(filepath.Dir(path))
		}
	}
	return filepath.Clean(path)
}

func isConventionSkillRoot(path, workspaceRoot string) bool {
	want := config.CanonicalSkillPath(path)
	if want == "" {
		return false
	}
	bases := []string{workspaceRoot}
	if home, err := os.UserHomeDir(); err == nil {
		bases = append(bases, home)
	}
	for _, base := range bases {
		base = strings.TrimSpace(base)
		if base == "" {
			continue
		}
		for _, dir := range config.ConventionDirs {
			if want == config.CanonicalSkillPath(filepath.Join(base, dir, skill.SkillsDirname)) {
				return true
			}
		}
	}
	return false
}

func skillRootPath(path string) string {
	if filepath.Base(path) == skill.SkillFile {
		return filepath.Dir(path)
	}
	return path
}

func skillDisplayRoot(sk skill.Skill, roots []skill.Root) string {
	cleanPath := filepath.Clean(sk.Path)
	for _, r := range roots {
		if r.Scope != sk.Scope {
			continue
		}
		cleanRoot := filepath.Clean(r.Dir)
		prefix := cleanRoot + string(filepath.Separator)
		if cleanPath == cleanRoot || strings.HasPrefix(cleanPath, prefix) {
			return config.CanonicalSkillPath(r.Dir)
		}
	}
	return config.CanonicalSkillPath(filepath.Dir(skillRootPath(sk.Path)))
}

// MCPServerInput is the drawer's "add server" form. Transport is "stdio" (Command
// + Args + Env) or "http"/"sse" (URL). Mirrors config.PluginEntry's writable shape.
type MCPServerInput struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	URL       string            `json:"url"`
	Env       map[string]string `json:"env"`
}

// MCPRegistryEntryView is one official MCP Registry server in the shape the
// desktop marketplace consumes: enough to render a card and, when Installable,
// assemble an MCPServerInput without a second round-trip.
type MCPRegistryEntryView struct {
	Name              string   `json:"name"`
	SuggestedName     string   `json:"suggestedName"`
	Title             string   `json:"title,omitempty"`
	Description       string   `json:"description,omitempty"`
	Version           string   `json:"version,omitempty"`
	RepositoryURL     string   `json:"repositoryUrl,omitempty"`
	Installable       bool     `json:"installable"`
	UnavailableReason string   `json:"unavailableReason,omitempty"`
	Transport         string   `json:"transport,omitempty"`
	Command           string   `json:"command,omitempty"`
	Args              []string `json:"args"`
	URL               string   `json:"url,omitempty"`
}

// MCPRegistryView is the marketplace query result. Cached/Warning surface a
// registry outage gracefully: browsing keeps working from the on-disk cache,
// but the UI must say so.
type MCPRegistryView struct {
	Servers []MCPRegistryEntryView `json:"servers"`
	Cached  bool                   `json:"cached"`
	Warning string                 `json:"warning,omitempty"`
}

// MCPRegistrySearch queries the official MCP Registry
// (registry.modelcontextprotocol.io). Only the settings marketplace calls it —
// boot and tool discovery never touch the network. A per-query cache keeps the
// page useful during a registry outage without treating cached entries as
// install metadata.
func (a *App) MCPRegistrySearch(query string) (MCPRegistryView, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := mcpregistry.New(mcpRegistryCachePath()).Search(ctx, query, 50)
	if err != nil {
		return MCPRegistryView{Servers: []MCPRegistryEntryView{}}, err
	}
	view := MCPRegistryView{
		Servers: make([]MCPRegistryEntryView, 0, len(result.Entries)),
		Cached:  result.Cached,
		Warning: result.Warning,
	}
	for _, entry := range result.Entries {
		view.Servers = append(view.Servers, mcpRegistryEntryView(entry))
	}
	return view, nil
}

// MCPRegistryResolve re-fetches one Registry entry immediately before the
// marketplace installs it. Offline cache stays useful for browsing, but it is
// never accepted as installation metadata — a cached package may have been
// removed or changed since it was stored.
func (a *App) MCPRegistryResolve(registryName string) (MCPRegistryEntryView, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	entry, _, err := mcpregistry.New(mcpRegistryCachePath()).Resolve(ctx, registryName)
	if err != nil {
		return MCPRegistryEntryView{}, err
	}
	return mcpRegistryEntryView(entry), nil
}

func mcpRegistryCachePath() string {
	if cacheDir := config.CacheDir(); cacheDir != "" {
		return filepath.Join(cacheDir, "mcp-registry.json")
	}
	return ""
}

func mcpRegistryEntryView(entry mcpregistry.Entry) MCPRegistryEntryView {
	return MCPRegistryEntryView{
		Name:              entry.Name,
		SuggestedName:     entry.SuggestedName,
		Title:             entry.Title,
		Description:       entry.Description,
		Version:           entry.Version,
		RepositoryURL:     entry.RepositoryURL,
		Installable:       entry.Installable,
		UnavailableReason: entry.UnavailableReason,
		Transport:         entry.Transport,
		Command:           entry.Command,
		Args:              append(make([]string, 0, len(entry.Args)), entry.Args...),
		URL:               entry.URL,
	}
}

// AddMCPServer connects a server live and persists it to config (Customize → MCP →
// Add). Returns the number of tools it exposed.
func (a *App) AddMCPServer(in MCPServerInput) (int, error) {
	ctrl := a.activeCtrl()
	if ctrl == nil {
		return 0, fmt.Errorf("no active session")
	}
	entry := config.PluginEntry{
		Name:    in.Name,
		Type:    normalizeMCPTransport(in.Transport),
		Command: in.Command,
		Args:    in.Args,
		URL:     in.URL,
		Env:     in.Env,
	}
	entry, _ = config.NormalizePluginCommandLine(entry)
	if err := a.saveDesktopMCPServer(entry); err != nil {
		return 0, err
	}
	return ctrl.ConnectMCPServer(entry)
}

// UpdateMCPServer edits a persisted external MCP server. The name is the stable
// identity; callers must remove + add if they want to rename a server.
func (a *App) UpdateMCPServer(name string, in MCPServerInput) error {
	if name == "codegraph" {
		return fmt.Errorf("codegraph is built in; configure it with [codegraph]")
	}
	ctrl := a.activeCtrl()
	if ctrl == nil {
		return fmt.Errorf("no active session")
	}
	if strings.TrimSpace(in.Name) != "" && strings.TrimSpace(in.Name) != name {
		return fmt.Errorf("renaming MCP servers is not supported; remove and add a new server")
	}
	updated, found, err := a.desktopMCPServerForEdit(name)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no configured MCP server named %q", name)
	}
	updated.Type = normalizeMCPTransport(in.Transport)
	updated.Command = strings.TrimSpace(in.Command)
	updated.Args = append([]string(nil), in.Args...)
	updated.URL = strings.TrimSpace(in.URL)
	updated.Tier = ""
	if in.Env != nil {
		updated.Env = in.Env
	}
	updated, _ = config.NormalizePluginCommandLine(updated)
	if updated.Type == "stdio" {
		updated.URL = ""
	} else {
		updated.Command = ""
		updated.Args = nil
	}
	if err := a.saveDesktopMCPServer(updated); err != nil {
		return err
	}

	a.mu.RLock()
	tab := a.activeTabLocked()
	sessionDisabled := false
	if tab != nil {
		_, sessionDisabled = tab.disabledMCP[name]
	}
	a.mu.RUnlock()
	wasConnected := mcpConnected(ctrl, name)
	wasFailed := mcpFailed(ctrl, name)
	if wasConnected {
		ctrl.DisconnectMCPServer(name)
	}
	if !sessionDisabled && (wasConnected || wasFailed || updated.ResolvedTier() != "lazy") {
		if _, err := ctrl.ConnectMCPServer(updated); err != nil {
			recordMCPFailure(ctrl, updated, err)
			return nil
		}
	}
	return nil
}

// RemoveMCPServer disconnects a live server and drops it from config (the row's ✕).
func (a *App) RemoveMCPServer(name string) error {
	if name == "codegraph" {
		return fmt.Errorf("codegraph is built in; it cannot be removed")
	}
	// Use activeCtrl (self-locking, returns a snapshot) instead of activeTab +
	// tab.Ctrl: the latter reads tab.Ctrl outside the lock, racing with rebuild()
	// which sets tab.Ctrl = nil — a nil deref panic.
	ctrl := a.activeCtrl()
	if ctrl == nil {
		return fmt.Errorf("no active session")
	}
	disconnected := ctrl.DisconnectMCPServer(name)
	removed, err := a.removeDesktopMCPServer(name)
	if err != nil {
		return err
	}
	if disconnected || removed {
		a.mu.Lock()
		if tab := a.activeTabLocked(); tab != nil {
			delete(tab.disabledMCP, name)
			tab.mcpOrder = removeServerOrder(tab.mcpOrder, name)
		}
		a.mu.Unlock()
		return nil
	}
	return fmt.Errorf("no MCP server named %q", name)
}

// ReconnectMCPServer disconnects the server if it is already connected (to force
// a fresh handshake and tool re-registration), then reconnects.  Failures are
// recorded on the Host so the UI can render them.
func (a *App) ReconnectMCPServer(name string) error {
	// Snapshot ctrl + workspaceRoot under the lock; accessing tab.Ctrl outside
	// the lock races rebuild() → nil panic (TOCTOU, same class as RemoveMCPServer).
	a.mu.RLock()
	tab := a.activeTabLocked()
	var ctrl tabSession
	root := ""
	if tab != nil {
		ctrl = tab.Ctrl
		root = tab.WorkspaceRoot
	}
	a.mu.RUnlock()
	if ctrl == nil {
		return fmt.Errorf("no active session")
	}
	if mcpConnected(ctrl, name) {
		ctrl.DisconnectMCPServer(name)
	}
	_, err := a.connectConfiguredMCPServer(ctrl, root, name)
	if err != nil {
		recordMCPFailure(ctrl, config.PluginEntry{Name: name}, err)
		return err
	}
	a.mu.Lock()
	if tab := a.activeTabLocked(); tab != nil {
		delete(tab.disabledMCP, name)
	}
	a.mu.Unlock()
	return nil
}

// ClearMCPServerAuthentication removes local auth-like config for one MCP and
// clears the current session's cached connection failure. It does not remove the
// server itself or try to sign the user out of the third-party browser session.
func (a *App) ClearMCPServerAuthentication(name string) error {
	if name == "codegraph" {
		return fmt.Errorf("codegraph is built in; it has no stored MCP authentication")
	}
	ctrl := a.activeCtrl()
	if ctrl == nil {
		return fmt.Errorf("no active session")
	}
	if _, _, _, err := config.ClearPluginAuthenticationInSource(name); err != nil {
		return err
	}
	ctrl.DisconnectMCPServer(name)
	if h := ctrl.Host(); h != nil {
		h.ClearFailure(name)
	}
	return nil
}

// SetMCPServerEnabled is the connector toggle: on reconnects a configured server
// for this session, off disconnects it (config untouched either way — like Claude
// Code's per-conversation enable/disable, it resets on the next session start).
func (a *App) SetMCPServerEnabled(name string, enabled bool) error {
	ctrl, root := a.activeCtrlAndRoot()
	if ctrl == nil {
		return fmt.Errorf("no active session")
	}
	tab := a.activeTab()
	if tab == nil {
		return fmt.Errorf("no active session")
	}
	if name == "codegraph" {
		return a.setCodegraphEnabled(enabled)
	}
	if _, _, err := a.desktopMCPServerForEdit(name); err != nil {
		return err
	}
	if enabled {
		_, err := a.connectConfiguredMCPServer(ctrl, root, name)
		if err == nil {
			a.mu.Lock()
			delete(tab.disabledMCP, name)
			a.mu.Unlock()
		}
		return err
	}
	if s, ok := findMCPServerView(ctrl, name); ok {
		s.Status = "disabled"
		s.Error = ""
		a.mu.Lock()
		if tab.disabledMCP == nil {
			tab.disabledMCP = map[string]ServerView{}
		}
		tab.disabledMCP[name] = s
		tab.mcpOrder = mergeServerOrder(tab.mcpOrder, []ServerView{s})
		a.mu.Unlock()
	}
	ctrl.DisconnectMCPServer(name)
	return nil
}

// connectConfiguredMCPServer connects a configured MCP server using an
// already-snapshotted controller + workspace root, so callers avoid holding a
// *WorkspaceTab (and its TOCTOU-prone tab.Ctrl) across the call.
func (a *App) connectConfiguredMCPServer(ctrl tabSession, root, name string) (int, error) {
	if ctrl == nil {
		return 0, fmt.Errorf("no active session")
	}
	cfg, err := config.LoadForRoot(root)
	if err != nil {
		return 0, err
	}
	for _, p := range cfg.Plugins {
		if p.Name == name {
			return ctrl.ConnectMCPServer(p)
		}
	}
	if name == "codegraph" {
		return ctrl.ConnectCodegraphMCPServer(cfg)
	}
	return 0, fmt.Errorf("no configured MCP server named %q", name)
}

// SetMCPServerTier is kept for old desktop bindings. New config writes drop the
// retired tier field; for CodeGraph this now means "enable and start in the
// background".
func (a *App) SetMCPServerTier(name, tier string) error {
	if name == "codegraph" {
		return a.setCodegraphTier(tier)
	}
	tier = normalizeMCPTier(tier)
	updated, found, err := a.desktopMCPServerForEdit(name)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no configured MCP server named %q", name)
	}
	updated.Tier = tier
	if !updated.ShouldAutoStart() {
		on := true
		updated.AutoStart = &on
	}
	if err := a.saveDesktopMCPServer(updated); err != nil {
		return err
	}
	ctrl := a.activeCtrl()
	if tier != "lazy" && ctrl != nil && !mcpConnected(ctrl, name) {
		if _, err := ctrl.ConnectMCPServer(updated); err != nil {
			recordMCPFailure(ctrl, updated, err)
			return nil
		}
		tab := a.activeTab()
		if tab != nil {
			a.mu.Lock()
			delete(tab.disabledMCP, name)
			a.mu.Unlock()
		}
	}
	return nil
}

func (a *App) setCodegraphEnabled(enabled bool) error {
	cfg, path, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		return err
	}
	ctrl, _ := a.activeCtrlAndRoot()
	if ctrl == nil {
		return fmt.Errorf("no active session")
	}
	tab := a.activeTab()
	if tab == nil {
		return fmt.Errorf("no active session")
	}
	cfg.Codegraph.Enabled = enabled
	if err := cfg.SaveTo(path); err != nil {
		return err
	}
	if err := a.syncProjectCodegraphOverride(cfg.Codegraph); err != nil {
		return err
	}
	if enabled {
		a.mu.Lock()
		delete(tab.disabledMCP, "codegraph")
		a.mu.Unlock()
		if !mcpConnected(ctrl, "codegraph") {
			if _, err := ctrl.ConnectCodegraphMCPServer(cfg); err != nil {
				recordCodegraphFailure(ctrl, cfg.Codegraph, err)
				return nil
			}
		}
		return nil
	}
	if h := ctrl.Host(); h != nil {
		h.ClearFailure("codegraph")
	}
	ctrl.DisconnectMCPServer("codegraph")
	s := withCodegraphConfig(ServerView{Name: "codegraph", Status: "disabled"}, cfg.Codegraph)
	a.mu.Lock()
	if tab.disabledMCP == nil {
		tab.disabledMCP = map[string]ServerView{}
	}
	tab.disabledMCP["codegraph"] = s
	tab.mcpOrder = mergeServerOrder(tab.mcpOrder, []ServerView{s})
	a.mu.Unlock()
	return nil
}

func (a *App) setCodegraphTier(_ string) error {
	cfg, path, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		return err
	}
	cfg.Codegraph.Enabled = true
	cfg.Codegraph.Tier = ""
	if err := cfg.SaveTo(path); err != nil {
		return err
	}
	if err := a.syncProjectCodegraphOverride(cfg.Codegraph); err != nil {
		return err
	}
	ctrl := a.activeCtrl()
	if ctrl == nil {
		return nil
	}
	tab := a.activeTab()
	if tab != nil {
		a.mu.Lock()
		delete(tab.disabledMCP, "codegraph")
		a.mu.Unlock()
	}
	if !mcpConnected(ctrl, "codegraph") {
		if _, err := ctrl.ConnectCodegraphMCPServer(cfg); err != nil {
			recordCodegraphFailure(ctrl, cfg.Codegraph, err)
			return nil
		}
	}
	return nil
}

func (a *App) desktopMCPServerForEdit(name string) (config.PluginEntry, bool, error) {
	cfg, _, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		return config.PluginEntry{}, false, err
	}
	if p, ok := findPluginEntry(cfg.Plugins, name); ok {
		return p, true, nil
	}
	if merged, err := config.LoadForRoot(a.activeWorkspaceRoot()); err == nil {
		if p, ok := findPluginEntry(merged.Plugins, name); ok {
			return p, true, nil
		}
	}
	return config.PluginEntry{}, false, nil
}

func (a *App) saveDesktopMCPServer(entry config.PluginEntry) error {
	cfg, path, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		return err
	}
	if err := cfg.UpsertPlugin(entry); err != nil {
		return err
	}
	if err := cfg.SaveTo(path); err != nil {
		return err
	}
	_, err = a.removeProjectMCPOverride(entry.Name)
	return err
}

func (a *App) removeDesktopMCPServer(name string) (bool, error) {
	removed := false
	cfg, path, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		return false, err
	}
	if cfg.RemovePlugin(name) {
		removed = true
		if err := cfg.SaveTo(path); err != nil {
			return false, err
		}
	}
	projectRemoved, err := a.removeProjectMCPOverride(name)
	if err != nil {
		return removed, err
	}
	return removed || projectRemoved, nil
}

func (a *App) removeProjectMCPOverride(name string) (bool, error) {
	path := projectConfigPathForRoot(a.activeWorkspaceRoot())
	userPath := config.UserConfigPath()
	if path == "" || sameConfigPath(path, userPath) {
		return false, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	cfg, err := config.LoadForEditStrict(path)
	if err != nil {
		return false, err
	}
	if !cfg.RemovePlugin(name) {
		return false, nil
	}
	if err := cfg.SaveTo(path); err != nil {
		return false, err
	}
	return true, nil
}

func (a *App) syncProjectCodegraphOverride(c config.CodegraphConfig) error {
	path := projectConfigPathForRoot(a.activeWorkspaceRoot())
	userPath := config.UserConfigPath()
	if path == "" || sameConfigPath(path, userPath) {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cfg, err := config.LoadForEditStrict(path)
	if err != nil {
		return err
	}
	cfg.Codegraph = c
	return cfg.SaveTo(path)
}

func findPluginEntry(entries []config.PluginEntry, name string) (config.PluginEntry, bool) {
	for _, p := range entries {
		if p.Name == name {
			return p, true
		}
	}
	return config.PluginEntry{}, false
}

func normalizeMCPTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "eager":
		return "eager"
	case "background":
		return "background"
	case "":
		return "background"
	default:
		return "lazy"
	}
}

func normalizeMCPTransport(transport string) string {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "http", "streamable-http":
		return "http"
	case "sse":
		return "sse"
	default:
		return "stdio"
	}
}

func mcpConnected(ctrl tabSession, name string) bool {
	if ctrl == nil || ctrl.Host() == nil {
		return false
	}
	for _, s := range ctrl.Host().Servers() {
		if s.Name == name {
			return true
		}
	}
	return false
}

func mcpFailed(ctrl tabSession, name string) bool {
	if ctrl == nil || ctrl.Host() == nil {
		return false
	}
	for _, f := range ctrl.Host().Failures() {
		if f.Name == name {
			return true
		}
	}
	return false
}

func recordMCPFailure(ctrl tabSession, e config.PluginEntry, err error) {
	if ctrl == nil || ctrl.Host() == nil || err == nil {
		return
	}
	exp := e.ExpandedPlugin()
	ctrl.Host().RecordFailure(plugin.Spec{
		Name:    exp.Name,
		Type:    exp.Type,
		Command: exp.Command,
		Args:    exp.Args,
		Env:     exp.Env,
		URL:     exp.URL,
		Headers: exp.Headers,
	}, err)
}

func recordCodegraphFailure(ctrl tabSession, c config.CodegraphConfig, err error) {
	if ctrl == nil || ctrl.Host() == nil || err == nil {
		return
	}
	cmd := strings.TrimSpace(c.Path)
	if cmd == "" {
		cmd = "codegraph"
	}
	ctrl.Host().RecordFailure(plugin.Spec{
		Name:    "codegraph",
		Type:    "stdio",
		Command: cmd,
		Args:    []string{"serve", "--mcp"},
	}, err)
}

func findMCPServerView(ctrl tabSession, name string) (ServerView, bool) {
	if ctrl == nil || ctrl.Host() == nil {
		return ServerView{}, false
	}
	for _, s := range ctrl.Host().Servers() {
		if s.Name == name {
			return ServerView{
				Name: s.Name, Transport: s.Transport, Status: "connected",
				Tools: s.Tools, Prompts: s.Prompts, Resources: s.Resources,
				ToolList: pluginToolsToView(s.ToolList),
			}, true
		}
	}
	for _, f := range ctrl.Host().Failures() {
		if f.Name == name {
			return ServerView{Name: f.Name, Transport: f.Transport, Status: "failed", Error: f.Error}, true
		}
	}
	return ServerView{}, false
}

func pluginToolsToView(tools []plugin.ToolInfo) []ToolView {
	if len(tools) == 0 {
		return nil
	}
	out := make([]ToolView, 0, len(tools))
	for _, t := range tools {
		out = append(out, ToolView{Name: t.Name, Description: t.Description})
	}
	return out
}

func orderServerViews(servers []ServerView, order []string) []ServerView {
	pos := make(map[string]int, len(order))
	for i, name := range order {
		pos[name] = i
	}
	sort.SliceStable(servers, func(i, j int) bool {
		pi, iok := pos[servers[i].Name]
		pj, jok := pos[servers[j].Name]
		switch {
		case iok && jok:
			return pi < pj
		case iok:
			return true
		case jok:
			return false
		default:
			return false
		}
	})
	return servers
}

func mergeServerOrder(order []string, servers []ServerView) []string {
	seen := make(map[string]bool, len(order)+len(servers))
	next := make([]string, 0, len(order)+len(servers))
	for _, name := range order {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		next = append(next, name)
	}
	for _, s := range servers {
		if s.Name == "" || seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		next = append(next, s.Name)
	}
	return next
}

func removeServerOrder(order []string, name string) []string {
	if name == "" || len(order) == 0 {
		return order
	}
	next := order[:0]
	for _, n := range order {
		if n != name {
			next = append(next, n)
		}
	}
	return next
}

// ModelInfo is one (provider, model) the bottom switcher can pick. Ref ("provider/
// model") is what SetModel takes; Provider/Model are for display.
type ModelInfo struct {
	Ref      string `json:"ref"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Current  bool   `json:"current"`
}

type EffortInfo struct {
	Supported bool     `json:"supported"`
	Current   string   `json:"current"`
	Default   string   `json:"default"`
	Levels    []string `json:"levels"`
}

// Models flattens the configured providers into their (provider, model) pairs —
// the switcher's options — marking the active one. A vendor with a `models` list
// yields one entry per model, all sharing the same endpoint/key. Unconfigured
// providers are skipped. Result is non-nil: the frontend reads .length, so a nil
// slice (JSON null) would crash the switcher on an empty list.
func (a *App) Models() []ModelInfo {
	return a.ModelsForTab("")
}

func (a *App) ModelsForTab(tabID string) []ModelInfo {
	a.mu.RLock()
	curModel := ""
	workspaceRoot := ""
	if tab := a.tabByIDLocked(tabID); tab != nil {
		curModel = tab.model
		workspaceRoot = tab.WorkspaceRoot
	}
	a.mu.RUnlock()
	cfg, err := config.LoadForRoot(workspaceRoot)
	if err != nil {
		return []ModelInfo{}
	}
	if entry, ok := cfg.ResolveModel(curModel); ok {
		curModel = entry.Name + "/" + entry.Model
		// normalize aliases to main provider so they highlight correctly in UI
		if entry.Name == "legacy-alias1" || entry.Name == "legacy-alias2" {
			curModel = "legacy/" + entry.Model
		}
	}
	access := providerAccessSet(cfg.Desktop.ProviderAccess)
	out := []ModelInfo{}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if !modelProviderAccessAllowed(access, p.Name) || !p.Configured() {
			continue
		}
		if p.Name == "custom" || p.Name == "anthropic" {
			continue
		}
		for _, m := range p.ChatModelList() {
			ref := p.Name + "/" + m
			out = append(out, ModelInfo{Ref: ref, Provider: p.Name, Model: m, Current: ref == curModel})
		}
	}
	return out
}

func modelProviderAccessAllowed(access map[string]bool, name string) bool {
	if len(access) == 0 {
		return true
	}
	return access[strings.TrimSpace(name)]
}

// SetModel switches the active model and carries the current conversation into the
// new model's session, so the chat continues seamlessly and subsequent turns use
// the new model. No-op if name is already active or the controller is down.
func (a *App) SetModel(name string) error {
	return a.SetModelForTab("", name)
}

func (a *App) SetModelForTab(tabID, name string) error {
	if a.ctx == nil || name == "" {
		return nil
	}
	tab := a.tabByID(tabID)
	if tab == nil {
		return nil
	}
	// Snapshot old controller + scalar fields under RLock to avoid TOCTOU.
	a.mu.RLock()
	oldCtrl := tab.Ctrl
	oldModel := tab.model
	oldEffort := tab.effort
	root := tab.WorkspaceRoot
	sink := tab.sink
	a.mu.RUnlock()

	if name == oldModel {
		return nil
	}
	if oldCtrl != nil && oldCtrl.Running() {
		return fmt.Errorf("finish or cancel the current turn before changing model")
	}
	cfg, err := config.LoadForRoot(root)
	if err != nil {
		return err
	}
	entry, ok := cfg.ResolveModel(name)
	if !ok {
		return fmt.Errorf("unknown model %q", name)
	}
	if !modelProviderAccessAllowed(providerAccessSet(cfg.Desktop.ProviderAccess), entry.Name) {
		return fmt.Errorf("model %q is not available because provider %q is not added", name, entry.Name)
	}
	name = entry.Name + "/" + entry.Model
	effortOverride := cloneStringPtr(oldEffort)
	if effortOverride != nil {
		normalized, err := config.NormalizeEffort(entry, config.EffortDisplay(&config.ProviderEntry{Effort: *effortOverride}))
		if err != nil {
			effortOverride = nil
		} else {
			effortOverride = &normalized
		}
	}

	var carried []provider.Message
	prevPath := ""
	// Acquire the shared host BEFORE closing the old controller so the refcount
	// never hits zero mid-switch (which would tear down subprocesses the new
	// controller is about to reuse). The matching release runs after the old
	// controller's Close, keeping the net reference count unchanged.
	sharedHost := a.acquireSharedHost(root)
	if oldCtrl != nil {
		prevPath = oldCtrl.SessionPath()
		_ = oldCtrl.Snapshot()
		carried = oldCtrl.History()
	}

	newCtrl, err := boot.Build(a.bootContext(), boot.Options{
		Model:          name,
		RequireKey:     false,
		Sink:           sink,
		WorkspaceRoot:  root,
		SessionDir:     tabSessionDir(tab),
		EffortOverride: cloneStringPtr(effortOverride),
		Host:           sharedHost,
		Present:        true,
	})
	if err != nil {
		a.releaseSharedHost(root) // Build failed: drop our acquire
		return err
	}
	a.bindControllerDisplayRecorder(newCtrl)
	a.bindControllerContextFilter(newCtrl)
	a.mu.Lock()
	if tab.Ctrl == oldCtrl {
		tab.Ctrl = newCtrl
		if oldCtrl != nil {
			oldCtrl.Close()
			a.releaseSharedHost(root) // drop the old controller's reference
		}
	} else {
		// Another goroutine swapped this tab's controller between our RUnlock and
		// Lock (e.g. a concurrent rebuild from a config change), so the
		// concurrently-installed controller supersedes our snapshot. Rather than
		// overwrite it (and leak the concurrent controller's host ref), roll back
		// this attempt: discard newCtrl and release the host ref we acquired for
		// it. Surface a clear error so the user knows the switch didn't take and
		// can retry, rather than silently no-op'ing.
		a.mu.Unlock()
		newCtrl.Close()
		a.releaseSharedHost(root)
		return errors.New("model switch aborted: this tab was concurrently rebuilt (e.g. by a config change); please retry")
	}
	tab.model = name
	tab.effort = cloneStringPtr(effortOverride)
	tab.Label = newCtrl.Label()
	a.saveTabsLocked()
	a.mu.Unlock()
	newCtrl.EnableInteractiveApproval()
	applyTabModeToController(newCtrl, tab.mode)
	applyTabToolApprovalModeToController(newCtrl, tab.toolApprovalMode)
	applyTabRagScopeToController(newCtrl, tab.ragScope)
	newCtrl.SetGoal(tab.goal)

	path := agent.ContinueSessionPath(prevPath, newCtrl.SessionDir(), newCtrl.Label())
	if len(carried) > 0 {
		newCtrl.Resume(&agent.Session{Messages: carried}, path)
	} else if path != "" {
		newCtrl.SetSessionPath(path)
	}
	a.persistTabSessionPath(tab, path)
	return nil
}

// ProfileInfo describes one selectable product profile for the frontend picker.
type ProfileInfo struct {
	Name          string `json:"name"`
	DisplayName   string `json:"displayName"`
	WorkspaceType string `json:"workspaceType,omitempty"`
}

// Profile returns the active profile name for the current tab ("dev" | "cowork").
// Empty/unprofiled resolves to "dev" so the frontend always sees a concrete mode.
func (a *App) Profile() string {
	return a.ProfileForTab("")
}

// ProfileForTab returns the active profile name for the given tab (current tab
// when tabID is ""). Empty resolves to "dev".
func (a *App) ProfileForTab(tabID string) string {
	tab := a.tabByID(tabID)
	if tab == nil {
		return config.ProfileDev
	}
	name := strings.TrimSpace(tab.profile)
	if name == "" {
		return config.ProfileDev
	}
	return strings.ToLower(name)
}

// Profiles lists every profile the user can switch to (builtins + configured),
// for the profile picker. Names are lowercased to match resolution.
func (a *App) Profiles() []ProfileInfo {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		cfg = config.Default()
	}
	seen := map[string]bool{}
	var out []ProfileInfo
	// Builtins first so dev is always available even with an empty config.
	for _, b := range config.DefaultProfiles() {
		k := strings.ToLower(b.Name)
		if !seen[k] {
			seen[k] = true
			out = append(out, ProfileInfo{Name: k, DisplayName: b.DisplayName, WorkspaceType: b.WorkspaceType})
		}
	}
	for _, p := range cfg.Profiles {
		k := strings.ToLower(strings.TrimSpace(p.Name))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		disp := strings.TrimSpace(p.DisplayName)
		if disp == "" {
			disp = k
		}
		out = append(out, ProfileInfo{Name: k, DisplayName: disp, WorkspaceType: p.WorkspaceType})
	}
	return out
}

// SwitchProfile switches the current tab to a product profile (dev/cowork) and
// rebuilds its controller so the new model/prompt/skill/plugin bundle takes
// effect immediately, carrying the conversation history across. Equivalent to
// SetModel but for a whole profile bundle instead of a single model. No-op when
// the profile is already active; refuses while a turn is running.
func (a *App) SwitchProfile(name string) error {
	return a.SwitchProfileForTab("", name)
}

// SwitchProfileForTab switches a specific tab (current when tabID is "") to the
// named profile. The rebuild flow mirrors SetModelForTab: acquire the shared MCP
// host so subprocesses survive, snapshot history, close the old controller,
// boot.Build with the profile, rebind, and Resume the history onto the fresh
// session. Emits "profile:changed" so the frontend swaps layouts.
func (a *App) SwitchProfileForTab(tabID, name string) error {
	if a.ctx == nil {
		return nil
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = config.ProfileDev
	}
	tab := a.tabByID(tabID)
	if tab == nil {
		return nil
	}
	// Expert-session tabs are always cowork and cannot be switched to dev —
	// switching would rebuild the controller and lose the IsExpertSession flag.
	if tab.IsExpertSession {
		return fmt.Errorf("cannot switch profile of an expert-session tab")
	}
	// Snapshot old controller + scalar fields under RLock to avoid TOCTOU.
	a.mu.RLock()
	oldCtrl := tab.Ctrl
	oldProfile := tab.profile
	oldModel := tab.model
	oldEffort := tab.effort
	root := tab.WorkspaceRoot
	sink := tab.sink
	a.mu.RUnlock()

	current := strings.ToLower(strings.TrimSpace(oldProfile))
	if current == "" {
		current = config.ProfileDev
	}
	if name == current {
		return nil
	}
	if oldCtrl != nil && oldCtrl.Running() {
		return fmt.Errorf("finish or cancel the current turn before switching profile")
	}
	cfg, err := config.LoadForRoot(root)
	if err != nil {
		return err
	}
	prof, err := cfg.ResolveProfile(name)
	if err != nil {
		return err
	}

	// Resolve the effective model: keep the tab's current model unless the profile
	// pins one. We pass BOTH to boot.Build — Profile for the bundle, Model as the
	// explicit winner. This lets a user pick a model inside cowork and keep it on
	// switch-back, while a profile that pins a model still overrides the default.
	modelName := strings.TrimSpace(oldModel)
	if modelName == "" {
		if strings.TrimSpace(prof.Model) != "" {
			modelName = prof.Model
		} else {
			modelName = cfg.DefaultModel
		}
	}
	// Normalize effort against the resolved model, mirroring SetModelForTab. A
	// profile switch can change the model (via prof.Model), so an effort level
	// valid for the old model (e.g. "high" on a reasoning-capable model) may be unsupported on
	// the new one — drop it then rather than passing an invalid level that boot
	// would silently keep.
	effortOverride := cloneStringPtr(oldEffort)
	if entry, ok := cfg.ResolveModel(modelName); ok && effortOverride != nil {
		if normalized, err := config.NormalizeEffort(entry, config.EffortDisplay(&config.ProviderEntry{Effort: *effortOverride})); err != nil {
			effortOverride = nil
		} else {
			effortOverride = &normalized
		}
	}

	// Acquire the shared host BEFORE closing the old controller so the refcount
	// never hits zero mid-switch (subprocess teardown). Same invariant as
	// SetModelForTab — the matching release runs after the old Close.
	sharedHost := a.acquireSharedHost(root)
	if oldCtrl != nil {
		_ = oldCtrl.Snapshot() // flush the in-memory turn buffer to the old file
		// Hard profile isolation: the old conversation is NOT carried across —
		// it stays on disk under the old profile (see comment further down).
	}

	// We CANNOT use tabSessionDir(tab) here because it prefers
	// tab.Ctrl.SessionDir(), and tab.Ctrl is still the OLD controller until the
	// swap below — so it would resolve the OLD profile's dir and break hard
	// isolation. Pass the new profile's dir explicitly. tab.profile itself is
	// flipped inside a.mu below (not here) so concurrent readers never see a
	// half-switched tab.
	newSessionDir := desktopSessionDirFor(root, name)

	newCtrl, err := boot.Build(a.bootContext(), boot.Options{
		Model:          modelName,
		RequireKey:     false,
		Sink:           sink,
		WorkspaceRoot:  root,
		SessionDir:     newSessionDir,
		EffortOverride: effortOverride, // already cloned+normalized above; no double-clone
		Host:           sharedHost,
		Profile:        prof,
		Present:        true,
	})
	if err != nil {
		// No state was mutated yet (tab.profile not flipped until the Lock below),
		// so there's nothing to roll back — just release the acquired host.
		a.releaseSharedHost(root)
		return err
	}
	a.bindControllerDisplayRecorder(newCtrl)
	a.bindControllerContextFilter(newCtrl)
	a.mu.Lock()
	if tab.Ctrl == oldCtrl {
		tab.Ctrl = newCtrl
		if oldCtrl != nil {
			oldCtrl.Close()
			a.releaseSharedHost(root)
		}
	} else {
		tab.Ctrl = newCtrl
	}
	tab.profile = name
	tab.effort = cloneStringPtr(effortOverride) // persist the normalized effort
	tab.Label = newCtrl.Label()
	// Clear the goal on a profile switch (hard isolation): the goal belongs to
	// the previous profile's conversation. The old goal remains in the old
	// profile's session on disk and is restored when switching back; the new
	// profile's fresh session starts goal-less.
	tab.goal = ""
	a.saveTabsLocked()
	// Snapshot the post-switch scalar fields under the lock so the unlocked
	// persist/index calls below don't race with a concurrent tabMeta/update*.
	snapScope := tab.Scope
	snapRoot := tab.WorkspaceRoot
	snapTopicID := strings.TrimSpace(tab.TopicID)
	snapTopicTitle := tab.TopicTitle
	snapMode := tab.mode
	snapToolApprovalMode := tab.toolApprovalMode
	snapRagScope := tab.ragScope
	a.mu.Unlock()
	newCtrl.EnableInteractiveApproval()
	applyTabModeToController(newCtrl, snapMode)
	applyTabToolApprovalModeToController(newCtrl, snapToolApprovalMode)
	applyTabRagScopeToController(newCtrl, snapRagScope)
	// Hard isolation extends to the goal: a dev goal (e.g. "refactor auth.go")
	// must NOT carry into the cowork controller, because Compose would inject it
	// as an <active-goal> block on every cowork turn. The goal lives with the
	// profile's conversation, so a fresh session starts goal-less. The old
	// profile's goal stays recorded on disk in its own session and is restored
	// when the user switches back (buildTabController re-reads it).
	newCtrl.SetGoal("")

	// Hard isolation: a profile switch starts a FRESH session in the new
	// profile's directory. The old conversation stays on disk under the old
	// profile, fully preserved — switching back reveals it again. We never carry
	// history across (that would leak dev context into cowork or vice versa) and
	// never reuse the old session path (ContinueSessionPath would return it).
	path := agent.NewSessionPath(newCtrl.SessionDir(), newCtrl.Label())
	if path != "" {
		newCtrl.SetSessionPath(path)
	}
	a.persistTabSessionPath(tab, path)
	// Index the (existing) topic under the NEW profile so it shows up in the
	// sidebar immediately, rather than waiting for the next restart's
	// buildTabController → ensureTopicIndexed pass. Best-effort: a failure just
	// means the topic appears after the first turn or restart.
	if snapTopicID != "" {
		if err := ensureTopicIndexed(snapScope, snapRoot, name, snapTopicID, snapTopicTitle, loadTopicTitleSource(topicTitleRoot(snapScope, snapRoot), name, snapTopicID)); err == nil {
			a.emitProjectTreeChanged()
		}
	}

	// Tell the frontend this tab's profile changed so it swaps the layout. The
	// payload carries the tab id + normalized profile name. Emit agent:ready too
	// so the frontend reloads (now-empty) history for the fresh session.
	a.emitReady(a.ctx, tab.ID)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "profile:changed", map[string]string{
			"tabId":   tab.ID,
			"profile": name,
		})
	}
	return nil
}

func (a *App) Effort() EffortInfo {
	return a.EffortForTab("")
}

func (a *App) EffortForTab(tabID string) EffortInfo {
	entry, err := a.currentProviderEntryForTab(tabID)
	if err != nil {
		return EffortInfo{Current: "auto", Levels: []string{}}
	}
	cap := config.EffortCapabilityForEntry(entry)
	if !cap.Supported {
		return EffortInfo{Supported: false, Current: "auto", Default: cap.Default, Levels: []string{}}
	}
	levels := cap.Levels
	if levels == nil {
		levels = []string{}
	}
	return EffortInfo{Supported: true, Current: config.EffortDisplay(entry), Default: cap.Default, Levels: levels}
}

func (a *App) SetEffort(level string) error {
	return a.SetEffortForTab("", level)
}

func (a *App) SetEffortForTab(tabID, level string) error {
	tab := a.tabByID(tabID)
	if tab == nil {
		if strings.TrimSpace(tabID) == "" {
			entry, err := a.currentProviderEntryForTab("")
			if err != nil {
				return err
			}
			effort, err := config.NormalizeEffort(entry, level)
			if err != nil {
				return err
			}
			return a.applyProviderEffortConfig(entry, effort)
		}
		return fmt.Errorf("tab %q not found", tabID)
	}
	// Snapshot old controller + scalar fields under RLock to avoid TOCTOU.
	a.mu.RLock()
	oldCtrl := tab.Ctrl
	oldModel := tab.model
	root := tab.WorkspaceRoot
	sink := tab.sink
	a.mu.RUnlock()

	if oldCtrl != nil && oldCtrl.Running() {
		return fmt.Errorf("finish or cancel the current turn before changing effort")
	}
	entry, err := a.currentProviderEntryForTab(tabID)
	if err != nil {
		return err
	}
	effort, err := config.NormalizeEffort(entry, level)
	if err != nil {
		return err
	}
	var carried []provider.Message
	prevPath := ""
	// Acquire before release so the shared host survives the controller swap.
	sharedHost := a.acquireSharedHost(root)
	if oldCtrl != nil {
		prevPath = oldCtrl.SessionPath()
		_ = oldCtrl.Snapshot()
		carried = oldCtrl.History()
	}
	newCtrl, err := boot.Build(a.bootContext(), boot.Options{
		Model:          oldModel,
		RequireKey:     false,
		Sink:           sink,
		WorkspaceRoot:  root,
		SessionDir:     tabSessionDir(tab),
		EffortOverride: &effort,
		Host:           sharedHost,
		Present:        true,
	})
	if err != nil {
		a.releaseSharedHost(root) // Build failed: drop our acquire
		return err
	}
	a.bindControllerDisplayRecorder(newCtrl)
	a.bindControllerContextFilter(newCtrl)
	a.mu.Lock()
	if tab.Ctrl == oldCtrl {
		tab.Ctrl = newCtrl
		if oldCtrl != nil {
			oldCtrl.Close()
			a.releaseSharedHost(root)
		}
	} else {
		tab.Ctrl = newCtrl
	}
	tab.effort = &effort
	tab.Label = newCtrl.Label()
	tab.StartupErr = ""
	tab.Ready = true
	a.saveTabsLocked()
	a.mu.Unlock()
	newCtrl.EnableInteractiveApproval()
	applyTabModeToController(newCtrl, tab.mode)
	applyTabToolApprovalModeToController(newCtrl, tab.toolApprovalMode)
	applyTabRagScopeToController(newCtrl, tab.ragScope)
	newCtrl.SetGoal(tab.goal)
	path := agent.ContinueSessionPath(prevPath, newCtrl.SessionDir(), newCtrl.Label())
	if len(carried) > 0 {
		newCtrl.Resume(&agent.Session{Messages: carried}, path)
	} else if path != "" {
		newCtrl.SetSessionPath(path)
	}
	a.persistTabSessionPath(tab, path)
	return nil
}

func (a *App) applyProviderEffortConfig(entry *config.ProviderEntry, effort string) error {
	return a.applyConfigChange(func(cfg *config.Config) error {
		if _, ok := cfg.Provider(entry.Name); !ok {
			if err := cfg.UpsertProvider(*entry); err != nil {
				return err
			}
		}
		if entry.Kind == "anthropic" && effort != "" && entry.Thinking == "" {
			if err := cfg.SetProviderThinking(entry.Name, "adaptive"); err != nil {
				return err
			}
		}
		for _, name := range providerEffortTargetNames(cfg, entry) {
			if err := cfg.SetProviderEffort(name, effort); err != nil {
				return err
			}
		}
		return nil
	})
}

func providerEffortTargetNames(cfg *config.Config, entry *config.ProviderEntry) []string {
	if cfg == nil || entry == nil {
		return nil
	}
	out := []string{entry.Name}
	seen := map[string]bool{entry.Name: true}
	kind := officialProviderKindFromEntry(*entry)
	if kind == "" {
		return out
	}
	var family []string
	switch kind {
	// No special family grouping needed for standard providers
	}
	for _, name := range family {
		if seen[name] {
			continue
		}
		p, ok := cfg.Provider(name)
		if !ok || officialProviderKindFromEntry(*p) != kind {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// DirEntry is one entry in the "@" file-reference menu.
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
}

// FilePreview is a bounded, read-only file payload for the workspace side panel.
type FilePreview struct {
	Path      string `json:"path"`
	Body      string `json:"body"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
	Kind      string `json:"kind,omitempty"`
	Mime      string `json:"mime,omitempty"`
	URL       string `json:"url,omitempty"`
	Err       string `json:"err,omitempty"`
}

type WorkspaceChangeView struct {
	Path         string   `json:"path"`
	OldPath      string   `json:"oldPath,omitempty"`
	Sources      []string `json:"sources"`
	GitStatus    string   `json:"gitStatus,omitempty"`
	Turns        []int    `json:"turns,omitempty"`
	LatestPrompt string   `json:"latestPrompt,omitempty"`
	LatestTime   int64    `json:"latestTime,omitempty"`
}

type WorkspaceChangesView struct {
	Files        []WorkspaceChangeView `json:"files"`
	GitAvailable bool                  `json:"gitAvailable"`
	GitErr       string                `json:"gitErr,omitempty"`
	GitBranch    string                `json:"gitBranch,omitempty"`
	// Added/Removed are tracked-file line deltas (git diff --numstat HEAD) and
	// Untracked the untracked-file count — the changed-tab "+N -N ?N" summary.
	// Omitted when git or the numstat probe fails; gitAvailable stays true then.
	Added     int `json:"added,omitempty"`
	Removed   int `json:"removed,omitempty"`
	Untracked int `json:"untracked,omitempty"`
}

// workspaceNoiseNames are local cache/vendor entries hidden from the file tree
// and "@" menu regardless of where they appear.
var workspaceNoiseNames = map[string]bool{
	".codex":       true,
	".codegraph":   true,
	".DS_Store":    true,
	".git":         true,
	".npm":         true,
	".pnpm-store":  true,
	"node_modules": true,
	"Thumbs.db":    true,
}

var workspaceNoiseDirs = map[string]bool{
	"bin":                      true,
	"desktop/build":            true,
	"desktop/frontend/dist":    true,
	"desktop/frontend/wailsjs": true,
	"dist":                     true,
	"npm/.stage":               true,
	"site/.astro":              true,
	"site/dist":                true,
	"stage":                    true,
	"tmp":                      true,
}

const filePreviewLimit = 256 * 1024
const fileRefSearchLimit = 20

// Office-document preview extraction bounds: the whole pipeline (markitdown
// subprocess or Word/WPS COM automation) must finish within the timeout, and
// oversized files skip extraction entirely (the raw binary read still runs).
const officeDocPreviewTimeout = 30 * time.Second
const officeDocPreviewMaxBytes = 100 * 1024 * 1024
const officeDocPreviewRunes = 40000

var previewMediaMIMEs = map[string]string{
	".bmp":  "image/bmp",
	".gif":  "image/gif",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".pdf":  "application/pdf",
	".png":  "image/png",
	".svg":  "image/svg+xml",
	".webp": "image/webp",
	// Audio/video stream through the media-token URL; Chromium's native
	// decoders handle what they can (mp3/wav/ogg/m4a/flac/aac, mp4/webm;
	// mov/mkv depend on the codecs inside the container).
	".aac":  "audio/aac",
	".flac": "audio/flac",
	".m4a":  "audio/mp4",
	".mp3":  "audio/mpeg",
	".oga":  "audio/ogg",
	".ogg":  "audio/ogg",
	".opus": "audio/ogg",
	".wav":  "audio/wav",
	".m4v":  "video/mp4",
	".mkv":  "video/x-matroska",
	".mov":  "video/quicktime",
	".mp4":  "video/mp4",
	".webm": "video/webm",
	// HTML previews render in a sandboxed iframe. No charset in the header so
	// the browser honors the file's own <meta charset> (GBK pages included).
	".htm":  "text/html",
	".html": "text/html",
}

func trimUTF8PartialSuffix(data []byte) []byte {
	if utf8.Valid(data) {
		return data
	}
	for i := len(data) - 1; i >= 0 && len(data)-i <= utf8.UTFMax; i-- {
		if !utf8.RuneStart(data[i]) {
			continue
		}
		if !utf8.Valid(data[:i]) || utf8.FullRune(data[i:]) {
			return data
		}
		return data[:i]
	}
	return data
}

func previewMediaKind(path string) (kind string, mime string) {
	mime = previewMediaMIMEs[strings.ToLower(filepath.Ext(path))]
	if mime == "" {
		return "", ""
	}
	if strings.HasPrefix(mime, "image/") {
		return "image", mime
	}
	switch mime {
	case "application/pdf":
		return "pdf", mime
	case "text/html":
		return "html", mime
	}
	if strings.HasPrefix(mime, "audio/") {
		return "audio", mime
	}
	if strings.HasPrefix(mime, "video/") {
		return "video", mime
	}
	return "", ""
}

func workspaceEntryRel(rel, name string) string {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	if rel == "" || rel == "." {
		return name
	}
	return rel + "/" + name
}

func skipWorkspaceEntry(rel, name string, isDir bool) bool {
	if workspaceNoiseNames[name] {
		return true
	}
	return isDir && workspaceNoiseDirs[workspaceEntryRel(rel, name)]
}

func (a *App) activeWorkspaceBase() (string, error) {
	root := a.activeWorkspaceRoot()
	if strings.TrimSpace(root) == "" || root == "." {
		return os.Getwd()
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return filepath.Clean(root), nil
}

func (a *App) workspacePath(rel string) (string, bool, error) {
	base, err := a.activeWorkspaceBase()
	if err != nil {
		return "", false, err
	}
	return workspacePathForBase(base, rel)
}

func workspacePathForBase(base, rel string) (string, bool, error) {
	base = filepath.Clean(base)
	if rel == "" {
		return "", false, os.ErrInvalid
	}
	path := rel
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, rel)
	}
	path = filepath.Clean(path)
	r, err := filepath.Rel(base, path)
	if err != nil {
		return "", false, err
	}
	if r == ".." || strings.HasPrefix(r, ".."+string(os.PathSeparator)) {
		return "", false, os.ErrPermission
	}
	return path, true, nil
}

// ListDir lists one directory level (directories first, then files, each
// alphabetical) for the "@" file-reference menu. rel resolves against the active
// tab workspace. The menu navigates one level at a time, never recursively —
// bounded for huge trees.
func (a *App) ListDir(rel string) []DirEntry {
	if rs := a.activeRemoteSession(); rs != nil {
		return a.remoteListDir(rs, rel)
	}
	base, err := a.activeWorkspaceBase()
	if err != nil {
		return []DirEntry{}
	}
	dir := base
	if rel != "" {
		path, ok, err := workspacePathForBase(base, rel)
		if err != nil || !ok {
			return []DirEntry{}
		}
		dir = path
	}
	es, err := os.ReadDir(dir)
	if err != nil {
		return []DirEntry{}
	}
	dirs, files := []DirEntry{}, []DirEntry{}
	for _, e := range es {
		name := e.Name()
		if skipWorkspaceEntry(rel, name, e.IsDir()) {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, DirEntry{Name: name, IsDir: true})
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, DirEntry{Name: name, IsDir: false})
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name) })
	return append(dirs, files...)
}

// SearchFileRefs finds workspace files by basename for bare "@token" completion.
func (a *App) SearchFileRefs(query string) []DirEntry {
	if rs := a.activeRemoteSession(); rs != nil {
		return a.remoteSearchFileRefs(rs, query)
	}
	base, err := a.activeWorkspaceBase()
	if err != nil {
		return nil
	}
	results := fileref.Search(base, query, fileRefSearchLimit)
	out := make([]DirEntry, 0, len(results))
	for _, r := range results {
		out = append(out, DirEntry{Name: r.Path, IsDir: r.IsDir})
	}
	return out
}

// ReadFile returns a small text preview for a file under the current workspace.
func (a *App) ReadFile(rel string) FilePreview {
	if rs := a.activeRemoteSession(); rs != nil {
		return a.remoteReadFile(rs, rel)
	}
	out := FilePreview{Path: rel}
	path, ok, err := a.workspacePath(rel)
	if err != nil || !ok {
		out.Err = "invalid path"
		return out
	}
	info, err := os.Stat(path)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	if info.IsDir() {
		out.Err = "path is a directory"
		return out
	}
	if !info.Mode().IsRegular() {
		out.Err = "path is not a regular file"
		return out
	}
	out.Size = info.Size()
	if kind, mime := previewMediaKind(path); kind != "" {
		token := a.ensureMediaTokenStore().create(path, info.Name(), mime, kind, info.Size(), info.ModTime())
		out.Kind = kind
		out.Mime = mime
		out.URL = "/__fairpeer_workspace_media/" + token + "/" + url.PathEscape(info.Name())
		return out
	}
	// Rich documents (docx/doc/xlsx/xls/pptx/ppt/epub/msg): extract text via
	// the RAG document pipeline (markitdown → Go parsers → Word/WPS COM for
	// legacy .doc) so chat/workspace previews show the document's content
	// instead of a "binary file" hint. PDF is excluded — it previews natively
	// via the media URL above.
	if ragpkg.IsOfficeDoc(path) && info.Size() <= officeDocPreviewMaxBytes {
		if text, err := ragpkg.ReadDocumentForPreview(path, officeDocPreviewTimeout); err == nil && strings.TrimSpace(text) != "" {
			runes := []rune(text)
			if len(runes) > officeDocPreviewRunes {
				out.Body = string(runes[:officeDocPreviewRunes])
				out.Truncated = true
			} else {
				out.Body = text
			}
			return out
		}
		// No parser available (markitdown missing, no Word/WPS, conversion
		// failed): fall through to the raw read — the Binary flag drives the
		// frontend's "open with system app" fallback.
	}
	f, err := os.Open(path)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	defer f.Close()

	buf := make([]byte, filePreviewLimit+1)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		out.Err = err.Error()
		return out
	}
	data := buf[:n]
	if len(data) > filePreviewLimit {
		data = data[:filePreviewLimit]
		out.Truncated = true
	}

	// Check for BOM first (just the first 2-3 bytes — always complete
	// even at a truncation boundary). BOM-prefixed files skip the NUL
	// check since UTF-16 normally contains 0x00 for ASCII characters.
	bomKind := fileenc.DetectQuick(data)
	if bomKind != fileenc.UTF8 {
		enc, _ := fileenc.Detect(data)
		if enc == fileenc.LossyUTF8 {
			out.Binary = true
			return out
		}
		decoded := fileenc.Decode(data, enc)
		out.Body = string(decoded)
		return out
	}

	// No BOM — NUL in raw bytes is a binary signal.
	if bytes.Contains(data, []byte{0}) {
		out.Binary = true
		return out
	}

	// Trim any partial multi-byte rune at the truncation boundary BEFORE
	// encoding detection. Without this, a large UTF-8 file truncated
	// mid-character would fail utf8.Valid and be misdetected as GB18030
	// or LossyUTF8, producing mojibake or a false binary classification.
	if out.Truncated {
		data = trimUTF8PartialSuffix(data)
	}
	enc, _ := fileenc.Detect(data)
	if enc == fileenc.LossyUTF8 {
		out.Binary = true
		return out
	}
	out.Body = string(fileenc.Decode(data, enc))
	return out
}

// OpenWorkspacePath opens a file or folder from the workspace in the OS default app.
func (a *App) OpenWorkspacePath(rel string) error {
	path, ok, err := a.workspacePath(rel)
	if err != nil || !ok {
		return os.ErrInvalid
	}
	return openWorkspacePath(path)
}

// RevealWorkspacePath shows a workspace file in the native file manager.
func (a *App) RevealWorkspacePath(rel string) error {
	if rs := a.activeRemoteSession(); rs != nil {
		return a.remoteReveal(rs, rel)
	}
	path, ok, err := a.workspacePath(rel)
	if err != nil || !ok {
		return os.ErrInvalid
	}
	return revealPath(path)
}

// RevealPath shows an arbitrary absolute path in the native file manager.
func (a *App) RevealPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return os.ErrInvalid
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return revealPath(path)
}

func revealPath(path string) error {
	switch goruntime.GOOS {
	case "darwin":
		return exec.Command("open", "-R", path).Start()
	case "windows":
		return exec.Command("explorer", "/select,", path).Start()
	default:
		dir := path
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			dir = filepath.Dir(path)
		}
		return exec.Command("xdg-open", dir).Start()
	}
}

func (a *App) noticeForTab(tabID, text string) {
	tab := a.tabByID(tabID)
	if tab != nil && tab.sink != nil {
		tab.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: text})
	}
}

func (a *App) runEffortCommandForTab(tabID, input string) {
	entry, err := a.currentProviderEntryForTab(tabID)
	if err != nil {
		a.noticeForTab(tabID, "effort: "+err.Error())
		return
	}
	cap := config.EffortCapabilityForEntry(entry)
	if !cap.Supported {
		a.noticeForTab(tabID, fmt.Sprintf("effort is not configurable for %s", entry.Name))
		return
	}
	args := strings.Fields(input)
	if len(args) < 2 {
		a.noticeForTab(tabID, fmt.Sprintf("effort for %s: %s (default: %s; options: %s)", entry.Name, config.EffortDisplay(entry), cap.Default, strings.Join(cap.Levels, "|")))
		return
	}
	if len(args) > 2 {
		a.noticeForTab(tabID, "usage: /effort "+strings.Join(cap.Levels, "|"))
		return
	}
	effort, err := config.NormalizeEffort(entry, args[1])
	if err != nil {
		a.noticeForTab(tabID, err.Error())
		return
	}
	if err := a.SetEffortForTab(tabID, args[1]); err != nil {
		a.noticeForTab(tabID, "effort: "+err.Error())
		return
	}
	display := effort
	if display == "" {
		display = "auto"
	}
	a.noticeForTab(tabID, fmt.Sprintf("effort for %s set to %s", entry.Name, display))
}

func (a *App) currentProviderEntryForTab(tabID string) (*config.ProviderEntry, error) {
	a.mu.RLock()
	ref := ""
	workspaceRoot := ""
	effortOverride := (*string)(nil)
	if tab := a.tabByIDLocked(tabID); tab != nil {
		ref = tab.model
		workspaceRoot = tab.WorkspaceRoot
		effortOverride = cloneStringPtr(tab.effort)
	}
	a.mu.RUnlock()
	cfg, err := config.LoadForRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(ref) == "" {
		ref = cfg.DefaultModel
	}
	resolved, _, ok := cfg.ResolveModelWithFallback(ref)
	if !ok {
		return nil, fmt.Errorf("unknown model %q", ref)
	}
	entry, ok := cfg.ResolveModel(resolved)
	if !ok {
		return nil, fmt.Errorf("unknown model %q", resolved)
	}
	if effortOverride != nil {
		entry.Effort = *effortOverride
	}
	return entry, nil
}

func (a *App) withActiveWorkspace(fn func() (string, error)) (string, error) {
	var result string
	err := a.withActiveWorkspaceDo(func() error {
		var err error
		result, err = fn()
		return err
	})
	return result, err
}

func (a *App) withActiveWorkspaceDo(fn func() error) error {
	root := a.activeWorkspaceRoot()
	if root != "" && root != "." {
		prev, err := os.Getwd()
		if err != nil {
			return err
		}
		if err := os.Chdir(root); err != nil {
			return err
		}
		defer func() { _ = os.Chdir(prev) }()
	}
	return fn()
}

// SavePastedImage stores a browser clipboard image data URL under the active
// tab's workspace .fairpeer/attachments and returns the relative @-reference path.
func (a *App) SavePastedImage(dataURL string) (string, error) {
	return a.withActiveWorkspace(func() (string, error) {
		return control.SaveImageDataURL(dataURL)
	})
}

// SaveClipboardImage reads the native OS clipboard image under the active tab's
// workspace .fairpeer/attachments and returns the relative @-reference path.
func (a *App) SaveClipboardImage() (string, error) {
	return a.withActiveWorkspace(control.SaveClipboardImage)
}

// SavePastedFile stores a dropped non-image file (the browser exposes its bytes
// as a data URL but not a real path) under the active tab's workspace
// .fairpeer/attachments and returns the relative @-reference path.
func (a *App) SavePastedFile(name, dataURL string) (string, error) {
	return a.withActiveWorkspace(func() (string, error) {
		return control.SaveAttachmentDataURL(name, dataURL)
	})
}

// PickExportFile opens the native save dialog and returns the selected path. It
// returns "" when the user cancels.
func (a *App) PickExportFile(defaultFilename, mimeType string) (string, error) {
	if a.ctx == nil {
		return "", nil
	}
	defaultFilename = safeExportFilename(defaultFilename)
	ext := strings.ToLower(filepath.Ext(defaultFilename))
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:                "Export session",
		DefaultDirectory:     dialogDefaultDirectory(a.activeWorkspaceRoot()),
		DefaultFilename:      defaultFilename,
		CanCreateDirectories: true,
		Filters:              exportFileFilters(mimeType, ext),
	})
	if err != nil || path == "" {
		return "", err
	}
	if ext != "" && filepath.Ext(path) == "" {
		path += ext
	}
	return path, nil
}

// SaveExportFile writes an exported session payload to a path previously picked
// by PickExportFile. An empty path is treated as a cancelled export.
func (a *App) SaveExportFile(path, payload string, base64Encoded bool) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	var data []byte
	var err error
	if base64Encoded {
		data, err = base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return fmt.Errorf("decode export payload: %w", err)
		}
	} else {
		data = []byte(payload)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	return nil
}

func safeExportFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "fairpeer-session.md"
	}
	return filepath.Base(name)
}

func exportFileFilters(mimeType, ext string) []runtime.FileFilter {
	switch mimeType {
	case "text/markdown":
		return []runtime.FileFilter{{DisplayName: "Markdown (*.md)", Pattern: "*.md"}}
	case "application/json":
		return []runtime.FileFilter{{DisplayName: "JSON (*.json)", Pattern: "*.json"}}
	case "application/pdf":
		return []runtime.FileFilter{{DisplayName: "PDF (*.pdf)", Pattern: "*.pdf"}}
	case "image/png":
		return []runtime.FileFilter{{DisplayName: "PNG image (*.png)", Pattern: "*.png"}}
	}
	if ext != "" {
		return []runtime.FileFilter{{DisplayName: strings.ToUpper(strings.TrimPrefix(ext, ".")) + " files (*" + ext + ")", Pattern: "*" + ext}}
	}
	return []runtime.FileFilter{{DisplayName: "All files (*.*)", Pattern: "*.*"}}
}

// AttachmentDataURL returns a safe data URL for a stored image attachment.
func (a *App) AttachmentDataURL(path string) (string, error) {
	return a.withActiveWorkspace(func() (string, error) {
		return control.ImageDataURL(path)
	})
}

// DroppedItem is one OS-dropped file resolved into a composer context entry: an
// in-tree file becomes a workspace @reference (read in place, no copy), while an
// image or out-of-tree file is copied into .fairpeer/attachments.
type DroppedItem struct {
	Kind       string `json:"kind"` // "workspace" | "attachment"
	Path       string `json:"path"`
	IsDir      bool   `json:"isDir,omitempty"`
	PreviewURL string `json:"previewUrl,omitempty"`
}

// AttachDropped turns an absolute path from the native file-drop bridge into a
// composer context entry. Images are stored as attachments so the chip shows a
// thumbnail; other in-workspace files are referenced relatively (no copy); files
// outside the workspace are copied into .fairpeer/attachments.
func (a *App) AttachDropped(path string) (DroppedItem, error) {
	var item DroppedItem
	err := a.withActiveWorkspaceDo(func() error {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if isImageExt(path) {
			if rel, err := control.SaveImageFile(path); err == nil {
				preview, _ := control.ImageDataURL(rel)
				item = DroppedItem{Kind: "attachment", Path: rel, PreviewURL: preview}
				return nil
			}
		}
		if rel, ok := workspaceRelativeIn(path, a.activeWorkspaceRoot()); ok {
			item = DroppedItem{Kind: "workspace", Path: rel, IsDir: info.IsDir()}
			return nil
		}
		if info.IsDir() {
			return fmt.Errorf("can only attach files from outside the workspace")
		}
		rel, err := control.SaveAttachmentFile(path)
		if err != nil {
			return err
		}
		item = DroppedItem{Kind: "attachment", Path: rel}
		return nil
	})
	if err != nil {
		return DroppedItem{}, err
	}
	return item, nil
}

func isImageExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

func workspaceRelativeIn(path, workspaceRoot string) (string, bool) {
	root := workspaceRoot
	if !filepath.IsAbs(root) {
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", false
		}
		root = abs
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// --- memory panel (frontend ⇄ controller) ---

// MemoryDoc is one loaded doc-memory file for the panel: path, scope, and body.
type MemoryDoc struct {
	Path  string `json:"path"`
	Scope string `json:"scope"`
	Body  string `json:"body"`
}

// MemoryFact is one saved auto-memory, surfaced read-only in the panel.
// The bitemporal fields (ValidFrom/ValidTo/Status/SupersededBy/CreatedAt)
// are populated from the store's Memory struct so the timeline view can show
// when a fact became true, whether it has expired, and what superseded it.
type MemoryFact struct {
	Name         string   `json:"name"`
	Title        string   `json:"title,omitempty"`
	Description  string   `json:"description"`
	Type         string   `json:"type"`
	Body         string   `json:"body"`
	Level        string   `json:"level,omitempty"` // l1 global | l2 project | l3 session
	ValidFrom    string   `json:"validFrom,omitempty"`
	ValidTo      string   `json:"validTo,omitempty"`
	Status       string   `json:"status,omitempty"`
	Category     string   `json:"category,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	SupersededBy string   `json:"supersededBy,omitempty"`
	CreatedAt    string   `json:"createdAt,omitempty"`
	UpdatedAt    string   `json:"updatedAt,omitempty"`
}

// MemoryScope is one writable quick-add target (scope id + the file it writes to).
type MemoryScope struct {
	Scope string `json:"scope"`
	Path  string `json:"path"`
}

// MemoryView is the whole memory panel payload: hierarchical docs, saved facts,
// the writable scopes for the quick-add selector, and where the memory tree lives.
type MemoryView struct {
	Docs      []MemoryDoc   `json:"docs"`
	Facts     []MemoryFact  `json:"facts"`
	Scopes    []MemoryScope `json:"scopes"`
	StoreDir  string        `json:"storeDir"`
	Available bool          `json:"available"`

	// Storage + maintenance settings, so the panel can show and edit them
	// without a second round-trip (see MemorySettings).
	Settings MemorySettings `json:"settings"`
}

// MemorySettings is the [memory] section as the panel needs it: the effective
// data root (and where it came from), and the model the background memory agents
// run on.
type MemorySettings struct {
	// Root is the effective data-tree root — where profile/, memory/ and
	// projects/ actually live right now.
	Root string `json:"root"`
	// DefaultRoot is what the root would be with no override, so the panel can
	// offer "reset to default".
	DefaultRoot string `json:"defaultRoot"`
	// ConfiguredRoot is the raw [memory] root value ("" = default in use).
	ConfiguredRoot string `json:"configuredRoot"`
	// RootFromEnv is true when $FAIRPEER_MEMORY_ROOT overrode the config, in
	// which case the panel must say so instead of pretending the field is empty.
	RootFromEnv bool `json:"rootFromEnv"`
	// StoreDir / GlobalDir / SessionDir are the resolved fact buckets, shown as
	// the L2 / L1 / L3 destinations.
	GlobalDir  string `json:"globalDir"`
	SessionDir string `json:"sessionDir"`

	// Maintenance model for Dream/Distill. Provider/Model are the raw [memory]
	// values; Ref is the resolved reference actually in use (which falls back to
	// agent.fast_task_model), and RefFromMemory distinguishes "pinned here" from
	// "inherited".
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Effort        string `json:"effort"`
	Ref           string `json:"ref"`
	RefFromMemory bool   `json:"refFromMemory"`

	// InjectIndex / IndexMaxChars describe the injected L1/L2 fact index.
	InjectIndex   bool `json:"injectIndex"`
	IndexMaxChars int  `json:"indexMaxChars"`
}

// MemorySettingsInput is the WRITABLE form of the [memory] section — one struct
// instead of a long positional argument list, so adding a knob later cannot
// silently shift an argument at the call site (the frontend is TypeScript, where
// a 5th parameter would compile fine and be ignored).
//
// IndexMaxChars <= 0 means "use the built-in default" rather than "no index":
// the cap is a safety backstop, not a switch. InjectIndex false is the switch.
type MemorySettingsInput struct {
	Root          string `json:"root"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Effort        string `json:"effort"`
	InjectIndex   bool   `json:"injectIndex"`
	IndexMaxChars int    `json:"indexMaxChars"`
}

// writableScopes are the quick-add targets the panel offers, broad → specific.
var writableScopes = []memory.Scope{memory.ScopeUser, memory.ScopeProject, memory.ScopeLocal}

// Memory returns the loaded memory for the panel: the fairpeer.md hierarchy, the
// saved auto-memories, and the writable scopes. Read-only; mutations go through
// Remember / SaveDoc.
func (a *App) Memory() MemoryView {
	// Always return non-nil slices: a nil Go slice marshals to JSON `null`, which
	// would crash the panel's `view.facts.length` / `.map`.
	view := MemoryView{Docs: []MemoryDoc{}, Facts: []MemoryFact{}, Scopes: []MemoryScope{}}
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return view
	}
	set := ctrl.Memory()
	if set == nil {
		return view
	}
	view.StoreDir = set.Store.Dir
	view.Available = true
	view.Settings = memorySettings(set.Store)
	for _, d := range set.Docs {
		view.Docs = append(view.Docs, MemoryDoc{Path: d.Path, Scope: string(d.Scope), Body: d.Body})
	}
	for _, f := range set.Store.List() {
		view.Facts = append(view.Facts, memoryFactView(f))
	}
	for _, sc := range writableScopes {
		if p := set.DocPath(sc); p != "" { // user scope yields "" when no config dir
			view.Scopes = append(view.Scopes, MemoryScope{Scope: string(sc), Path: p})
		}
	}
	return view
}

// memoryFactView maps a memory.Memory onto the panel's MemoryFact DTO.
// memory.Memory was slimmed to {Name,Body,Type,Profile,CreatedAt}; the legacy
// bitemporal fields (Title/Description/ValidFrom/ValidTo/Status/Category/Tags/
// SupersededBy/UpdatedAt) are no longer on the store struct, so they stay at
// their zero value here. The frontend tolerates empty fields (omitempty on the
// JSON side). CreatedAt is still carried so the timeline can sort entries.
func memoryFactView(f memory.Memory) MemoryFact {
	out := MemoryFact{
		Name:  f.Name,
		Type:  string(f.Type),
		Body:  f.Body,
		Level: string(memory.LevelOf(f)),
		Tags:  f.Tags,
	}
	if !f.CreatedAt.IsZero() {
		out.CreatedAt = f.CreatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// memorySettings resolves the [memory] section for the panel: the effective data
// root (and its provenance) plus the maintenance model actually in use. It reads
// the live config so a change made elsewhere is reflected without a restart.
func memorySettings(store memory.Store) MemorySettings {
	out := configMemorySettings()
	out.GlobalDir = store.GlobalDir
	out.SessionDir = store.SessionDir
	return out
}

// configMemorySettings resolves the live [memory] section with no store in hand —
// used after a save, when the panel needs the refreshed values but the caller has
// no session context.
func configMemorySettings() MemorySettings {
	out := MemorySettings{
		Root:        config.MemoryRoot(),
		DefaultRoot: config.DefaultMemoryRoot(),
	}
	out.ConfiguredRoot = config.ConfiguredMemoryRoot()
	out.RootFromEnv = config.MemoryRootFromEnv()

	if cfg, err := config.Load(); err == nil {
		out.Provider = cfg.Memory.Provider
		out.Model = cfg.Memory.Model
		out.Effort = cfg.MemoryEffort()
		out.Ref = cfg.MemoryMaintenanceRef()
		out.RefFromMemory = strings.TrimSpace(cfg.Memory.Provider) != "" || strings.TrimSpace(cfg.Memory.Model) != ""
		out.InjectIndex = cfg.MemoryInjectIndex()
		out.IndexMaxChars = cfg.MemoryIndexMaxChars()
		if out.IndexMaxChars <= 0 {
			out.IndexMaxChars = memory.DefaultPromptIndexMaxChars
		}
	}
	return out
}

// MemoryHistory returns the full bitemporal surface — active, dormant, and
// superseded records (the latter pulled from .archive/) — for the timeline
// view. Unlike Memory() this includes docs/scopes metadata as well so the
// panel can render a self-contained history tab without a second round-trip.
// Read-only; never mutates state.
func (a *App) MemoryHistory() MemoryView {
	view := MemoryView{Docs: []MemoryDoc{}, Facts: []MemoryFact{}, Scopes: []MemoryScope{}}
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return view
	}
	set := ctrl.Memory()
	if set == nil {
		return view
	}
	view.StoreDir = set.Store.Dir
	view.Available = true
	view.Settings = memorySettings(set.Store)
	for _, f := range set.Store.List() {
		view.Facts = append(view.Facts, memoryFactView(f))
	}
	return view
}

// SaveMemorySettings persists the [memory] section: where the memory data tree
// lives and which model maintains it. Saving a root does NOT move any data — the
// panel offers MigrateMemoryRoot for that — so a user can point at a new drive
// and decide afterwards whether to copy. It takes effect on the NEXT session,
// like every other memory edit: the data root is part of the boot snapshot that
// the cache-stable prefix is built from, so rewriting it mid-session would break
// the prefix for no benefit.
func (a *App) SaveMemorySettings(in MemorySettingsInput) (MemorySettings, error) {
	cfg, path, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		return MemorySettings{}, err
	}
	raw := strings.TrimSpace(in.Root)
	if raw != "" {
		probe := config.Default()
		probe.Memory = config.MemoryConfig{Root: raw}
		resolved := probe.MemoryRootPath()
		if resolved == "" {
			return MemorySettings{}, fmt.Errorf("memory root %q could not be resolved", raw)
		}
		if err := config.MemoryRootError(resolved); err != nil {
			return MemorySettings{}, err
		}
	}
	cfg.Memory.Root = raw
	cfg.Memory.Provider = strings.TrimSpace(in.Provider)
	cfg.Memory.Model = strings.TrimSpace(in.Model)
	cfg.Memory.Effort = strings.ToLower(strings.TrimSpace(in.Effort))
	// The fact index is the "knowledge base index" half of memory: it is what
	// lets the model know a fact exists without every body riding every turn.
	// Stored explicitly so the panel's toggle round-trips (a nil pointer would
	// otherwise be written back as "unset" and silently re-enable it).
	inject := in.InjectIndex
	cfg.Memory.InjectIndex = &inject
	if in.IndexMaxChars > 0 {
		cfg.Memory.IndexMaxChars = in.IndexMaxChars
	} else {
		cfg.Memory.IndexMaxChars = 0
	}
	if err := cfg.SaveTo(path); err != nil {
		return MemorySettings{}, err
	}
	return configMemorySettings(), nil
}

// MigrateMemoryRoot copies the existing memory data — the portrait layer, the
// saved facts at every level, and per-project sessions/state — from the
// currently effective root to `to`.
//
// It COPIES, never moves: the source tree is left intact so an interrupted run,
// a wrong destination or a change of mind costs nothing, and the user deletes the
// old tree once they have verified the new one. Files already present at the
// destination win, which makes the operation safe to re-run.
func (a *App) MigrateMemoryRoot(to string) (config.MemoryMigrationReport, error) {
	raw := strings.TrimSpace(to)
	if raw == "" {
		return config.MemoryMigrationReport{}, fmt.Errorf("no destination given")
	}
	probe := config.Default()
	probe.Memory = config.MemoryConfig{Root: raw}
	dest := probe.MemoryRootPath()
	if dest == "" {
		return config.MemoryMigrationReport{}, fmt.Errorf("destination %q could not be resolved", raw)
	}
	if err := config.MemoryRootError(dest); err != nil {
		return config.MemoryMigrationReport{}, err
	}
	from := config.MemoryRoot()
	if from == "" {
		return config.MemoryMigrationReport{}, fmt.Errorf("the current memory root could not be resolved")
	}
	if from == dest {
		return config.MemoryMigrationReport{}, fmt.Errorf("the destination is already the active memory root")
	}
	return config.MigrateMemoryTree(from, dest)
}

// MemoryPromotionInput is the panel's "promote memory into a skill/plugin"
// request. Selection mirrors the `recall` tool (explicit names, or a
// level/tag/query filter) so the panel and the model address memory the same way.
type MemoryPromotionInput struct {
	Kind        string   `json:"kind"` // "skill" | "plugin"
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Memories    []string `json:"memories,omitempty"`
	Level       string   `json:"level,omitempty"`
	Tag         string   `json:"tag,omitempty"`
	Query       string   `json:"query,omitempty"`
	Notes       string   `json:"notes,omitempty"`
	Version     string   `json:"version,omitempty"`
	Overwrite   bool     `json:"overwrite,omitempty"`
	// Install also copies the generated playbook into a skill root the agent
	// already scans, so the promoted capability is live in the next session with
	// no kernel change. Scope picks which root: "project" (the workspace's
	// .fairpeer/skills) or anything else for the global one.
	Install bool   `json:"install,omitempty"`
	Scope   string `json:"scope,omitempty"`
}

// MemoryPromotionResult is what the promotion produced. Paths, not content: the
// panel links to the artifact and the user's editor does the rest.
type MemoryPromotionResult struct {
	Kind      string                  `json:"kind"`
	Name      string                  `json:"name"`
	Dir       string                  `json:"dir"`
	Skill     string                  `json:"skill"`
	Manifest  string                  `json:"manifest,omitempty"`
	Readme    string                  `json:"readme,omitempty"`
	Refs      int                     `json:"refs"`
	Sources   []memory.PromotedSource `json:"sources"`
	Version   string                  `json:"version"`
	Installed string                  `json:"installed,omitempty"`
}

// PromoteMemoryArtifact turns saved memories into a reusable artifact — the step that
// makes memory self-evolving instead of merely accumulating.
//
// The artifact is DATA in the documented layout memory.PromoteMemory emits: a
// SKILL.md the existing skill loader indexes and `run_skill` runs, source
// memories under references/, and (for the plugin kind) a manifest whose
// capabilities use the kernel's extensioncontract key format. Nothing here
// touches the agent kernel, so a promotion can never break an agent iteration —
// which is the property that makes this safe to do from the UI.
func (a *App) PromoteMemoryArtifact(input MemoryPromotionInput) (MemoryPromotionResult, error) {
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return MemoryPromotionResult{}, fmt.Errorf("no active session to read memory from")
	}
	set := ctrl.Memory()
	if set == nil {
		return MemoryPromotionResult{}, fmt.Errorf("memory is unavailable in this session")
	}
	got, err := memory.PromoteMemory(set.Store, memory.PromoteRequest{
		Kind:        memory.PromoteKind(strings.TrimSpace(input.Kind)),
		Name:        input.Name,
		Description: input.Description,
		Memories:    input.Memories,
		Level:       input.Level,
		Tag:         input.Tag,
		Query:       input.Query,
		Notes:       input.Notes,
		Version:     input.Version,
		Overwrite:   input.Overwrite,
	})
	if err != nil {
		return MemoryPromotionResult{}, err
	}
	out := MemoryPromotionResult{
		Kind:     string(got.Kind),
		Name:     got.Name,
		Dir:      got.Dir,
		Skill:    got.Skill,
		Manifest: got.Manifest,
		Readme:   got.Readme,
		Refs:     len(got.Refs),
		Sources:  got.Sources,
		Version:  got.Version,
	}
	if !input.Install {
		return out, nil
	}
	installed, err := installPromotedSkill(got, input.Scope)
	if err != nil {
		// The artifact exists on disk either way; report the install failure with
		// the paths so the user can wire it up by hand instead of losing the work.
		return out, fmt.Errorf("promoted to %s, but installing the skill failed: %w", got.Dir, err)
	}
	out.Installed = installed
	return out, nil
}

// installPromotedSkill copies a promoted playbook into a skill root so the agent
// picks it up through its normal discovery (name + description enter the pinned
// skills index; the body loads on invoke).
//
// Re-installing refreshes the file IN PLACE, but only when the existing skill of
// that name actually lives in the target root — a same-named skill from another
// scope (project overriding global, or a hand-written one) is never clobbered.
func installPromotedSkill(got memory.Promotion, scopeName string) (string, error) {
	content, err := os.ReadFile(got.Skill)
	if err != nil {
		return "", err
	}
	cwd, _ := os.Getwd()
	cfg, _ := config.Load()
	var custom, excluded []string
	maxDepth := 3
	if cfg != nil {
		custom = cfg.SkillCustomPaths()
		excluded = cfg.SkillExcludedPaths()
		maxDepth = cfg.SkillMaxDepth()
	}
	scope := skill.ScopeGlobal
	if strings.EqualFold(strings.TrimSpace(scopeName), "project") {
		scope = skill.ScopeProject
	}
	st := skill.New(skill.Options{
		ProjectRoot:     cwd,
		CustomPaths:     custom,
		ExcludedPaths:   excluded,
		MaxDepth:        maxDepth,
		DisableBuiltins: true,
		Stderr:          io.Discard,
	})
	if existing, ok := st.Read(got.Name); ok {
		home, _ := os.UserHomeDir()
		target := skillRootFor(scope, cwd, home)
		if target == "" || !strings.HasPrefix(config.CanonicalSkillPath(existing.Path), config.CanonicalSkillPath(target)) {
			return "", fmt.Errorf("a skill named %q already exists outside the %s skill root (%s); rename the artifact or install it manually", got.Name, scope, existing.Path)
		}
		if filepath.Base(existing.Path) != skill.SkillFile {
			return "", fmt.Errorf("skill %q is a flat %s file; install the promoted copy manually from %s", got.Name, filepath.Base(existing.Path), got.Skill)
		}
		if err := os.WriteFile(existing.Path, content, 0o644); err != nil {
			return "", err
		}
		return existing.Path, nil
	}
	return st.CreateWithContent(got.Name, scope, string(content))
}

// skillRootFor is the directory a scope's promoted skills land in — the same
// convention the skill loader discovers (.fairpeer/skills under the workspace or
// the home dir).
func skillRootFor(scope skill.Scope, cwd, home string) string {
	if scope == skill.ScopeProject {
		if cwd == "" {
			return ""
		}
		return filepath.Join(cwd, ".fairpeer", skill.SkillsDirname)
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".fairpeer", skill.SkillsDirname)
}

// Remember quick-adds a one-line note to the doc-memory file for scope — the
// panel's explicit "remember" action, equivalent to typing "/remember <note>".
// An unknown scope falls back to project. Returns the file written.
func (a *App) Remember(scope, note string) (string, error) {
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return "", nil
	}
	return ctrl.QuickAdd(parseScope(scope), note)
}

// Forget deletes a saved auto-memory by name — the panel's delete action for a
// fact the model owns. A no-op when no controller is attached.
func (a *App) Forget(name string) error {
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return nil
	}
	return ctrl.ForgetMemory(name)
}

// PromoteMemory confirms an auto-captured "pending" memory. The pending-capture
// pipeline was removed when memory.Memory was slimmed, so there are no pending
// records to promote anymore — this is now a no-op that returns false so the
// frontend's confirm button stays harmless. The signature is kept so the panel
// call site doesn't need a frontend change.
func (a *App) PromoteMemory(name string) (bool, error) {
	return false, nil
}

// RejectMemory dismisses an auto-captured "pending" memory. As with PromoteMemory,
// the pending-capture pipeline no longer exists, so this is a no-op returning
// false. Signature retained for the frontend's ignore button.
func (a *App) RejectMemory(name string) (bool, error) {
	return false, nil
}

// SaveDoc overwrites a memory doc with the panel editor's contents. The controller
// validates path against the recognized memory files. Returns the file written.
func (a *App) SaveDoc(path, body string) (string, error) {
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return "", nil
	}
	return ctrl.SaveDoc(path, body)
}

// ProfileView is the payload the workspace preference panel reads: the path of
// the active mode's portrait file and its current contents. Under cowork this
// is cowork.md; under dev it is dev.md. user.md / memory.md are intentionally
// not exposed — only the mode file is user-editable for now.
type ProfileView struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// PortraitProfile returns the active mode's portrait for the workspace
// preference panel. Read-only; saves go through SaveDoc (the profile path is
// whitelisted in memory.allowedDocPaths, so a normal SaveDoc call accepts it).
// Named PortraitProfile to avoid clashing with Profile() (the mode switch).
func (a *App) PortraitProfile() ProfileView {
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return ProfileView{}
	}
	pv := ctrl.Profile()
	return ProfileView{Path: pv.Path, Content: pv.Content}
}

// ProfilePresetsPayload is the preset-panel's read/write payload: the mode's
// preference presets plus the id in use and the file path (read-only display).
// Items reuse memory.ProfilePreset directly (same precedent as the PPT template
// views). On save the frontend submits Active+Items; Path is ignored.
type ProfilePresetsPayload struct {
	Active string                 `json:"active"`
	Items  []memory.ProfilePreset `json:"items"`
	Path   string                 `json:"path"`
}

// ProfilePresets returns the active mode's preference presets for the cowork
// preference panel. A fresh install gets the factory defaults (in memory only).
func (a *App) ProfilePresets() ProfilePresetsPayload {
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return ProfilePresetsPayload{}
	}
	v := ctrl.ProfilePresets()
	return ProfilePresetsPayload{Active: v.Active, Items: v.Items, Path: v.Path}
}

// SetProfilePresets saves the whole preset list and the active selection in one
// call (the panel edits locally and commits atomically). Returns the file written.
func (a *App) SetProfilePresets(p ProfilePresetsPayload) (string, error) {
	a.mu.RLock()
	ctrl := a.activeCtrlLocked()
	a.mu.RUnlock()
	if ctrl == nil {
		return "", nil
	}
	return ctrl.SaveProfilePresets(memory.PresetFile{Active: p.Active, Items: p.Items})
}

// parseScope maps a frontend scope id to a memory.Scope, defaulting to project.
func parseScope(s string) memory.Scope {
	switch memory.Scope(s) {
	case memory.ScopeUser:
		return memory.ScopeUser
	case memory.ScopeLocal:
		return memory.ScopeLocal
	default:
		return memory.ScopeProject
	}
}

// onboardingKeyEnv is a legacy fallback env name; NeedsOnboarding now checks
// whether ANY configured provider has a resolvable key.
const onboardingKeyEnv = "FAIRPEER_API_KEY"

// probeVendorKey validates an API key by hitting the vendor's /models endpoint.
// A lightweight connectivity + auth check used during onboarding. The baseURL
// is passed by the caller (the selected vendor's endpoint) — it no longer
// assumes a single built-in provider.
func probeVendorKey(ctx context.Context, baseURL, apiKey string) error {
	// Try the common OpenAI-compatible model-list shapes. Most vendors answer
	// at least one of these; a 200 means the key is valid for the endpoint.
	candidates := []string{
		strings.TrimRight(baseURL, "/") + "/models",
		strings.TrimRight(baseURL, "/") + "/v1/models",
	}
	var lastErr error
	for _, u := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("network: %w", err)
			continue
		}
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			return nil // key works
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("invalid API key")
		case http.StatusNotFound:
			// Wrong path shape; try the next candidate.
			lastErr = fmt.Errorf("unexpected status %d", resp.StatusCode)
			continue
		default:
			lastErr = fmt.Errorf("unexpected status %d", resp.StatusCode)
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("could not validate key against %s", baseURL)
}

// NativeConfirmRequest is the payload for ConfirmAction — a native OS confirmation
// dialog that replaces web-style confirm() for destructive or important actions.
type NativeConfirmRequest struct {
	Title        string `json:"title"`
	Message      string `json:"message"`
	Detail       string `json:"detail"`
	ConfirmLabel string `json:"confirmLabel"`
	CancelLabel  string `json:"cancelLabel"`
	Destructive  bool   `json:"destructive"`
}

// ConfirmAction shows a native confirmation dialog and returns true when the user
// clicks the confirm button. For destructive actions the dialog type is Warning so
// the platform can apply its danger styling (red tint on macOS, etc.).
func (a *App) ConfirmAction(req NativeConfirmRequest) (bool, error) {
	if a.ctx == nil {
		return false, nil
	}
	dialogType := runtime.QuestionDialog
	if req.Destructive {
		dialogType = runtime.WarningDialog
	}
	confirm := req.ConfirmLabel
	if confirm == "" {
		confirm = "OK"
	}
	cancel := req.CancelLabel
	if cancel == "" {
		cancel = "Cancel"
	}
	title := req.Title
	if title == "" {
		title = req.Message
	}
	body := req.Message
	if req.Detail != "" {
		if body != "" {
			body += "\n\n" + req.Detail
		} else {
			body = req.Detail
		}
	}
	defaultBtn := confirm
	if req.Destructive {
		// On destructive actions, make cancel the default so Enter / Space
		// does NOT accidentally confirm. ESC always maps to CancelButton.
		defaultBtn = cancel
	}
	result, err := runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:          dialogType,
		Title:         title,
		Message:       body,
		Buttons:       []string{confirm, cancel},
		DefaultButton: defaultBtn,
		CancelButton:  cancel,
	})
	if err != nil {
		return false, err
	}
	return result == confirm, nil
}

// NeedsOnboarding reports whether the user has at least one provider with a
// resolvable API key. FairPeer ships no built-in provider, so the first run
// shows the onboarding wizard until the user configures one.
func (a *App) NeedsOnboarding() bool {
	cfg, err := config.Load()
	if err != nil {
		// Can't load config — show onboarding so the user can set things up.
		return true
	}
	access := providerAccessSet(cfg.Desktop.ProviderAccess)
	for _, p := range cfg.Providers {
		if p.APIKeyEnv == "" {
			// Keyless local provider (Ollama / llama.cpp): counts as configured
			// only when the user added it via the wizard/settings — the built-in
			// presets alone never finish onboarding.
			if access[p.Name] {
				return false
			}
			continue
		}
		if strings.TrimSpace(os.Getenv(p.APIKeyEnv)) != "" {
			return false // at least one provider has a key
		}
	}
	// Legacy fallback: a bare FAIRPEER_API_KEY without a provider entry.
	return strings.TrimSpace(os.Getenv(onboardingKeyEnv)) == ""
}

// ProbeVendorKey validates an API key against a vendor endpoint during the
// onboarding wizard (step 2). baseURL is the vendor's API root (e.g.
// "https://api.deepseek.com"). Does NOT persist anything — call SetupProvider
// to commit after the user picks a model.
func (a *App) ProbeVendorKey(baseURL, apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return fmt.Errorf("key is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		return fmt.Errorf("base URL is required")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	return probeVendorKey(ctx, baseURL, apiKey)
}

// SetupProvider commits a vendor selection from the onboarding wizard. It
// writes the API key, saves the provider config (with the chosen default model),
// sets it as the global default, and rebuilds so it takes effect immediately.
// template is the vendor preset; apiKey is the user's key; defaultModel is the
// template is the vendor preset; apiKey is the user's key; defaultModel is the
// vendor-relative model name picked in step 3 (e.g. "deepseek-v4-pro").
// visionModel, fastModel and voiceModel are similarly vendor-relative names for
// those roles. voiceModel picks the speech-to-text model (empty/"none" = skip).
func (a *App) SetupProvider(template ProviderTemplate, apiKey string, defaultModel string, visionModel string, fastModel string, voiceModel string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" && template.APIKeyEnv != "" {
		// Keyless local presets (empty APIKeyEnv) skip this check — the wizard
		// never collects a key for them.
		return fmt.Errorf("key is required")
	}
	defaultModel = strings.TrimSpace(defaultModel)
	if defaultModel == "" {
		defaultModel = template.DefaultModel
	}
	visionModel = strings.TrimSpace(visionModel)
	fastModel = strings.TrimSpace(fastModel)

	// 1. Write the key to .env so FetchModels / SaveProvider can resolve it.
	// Keyless local templates carry no env var — nothing to persist.
	if template.APIKeyEnv != "" {
		if err := upsertCredential(template.APIKeyEnv, apiKey); err != nil {
			return fmt.Errorf("save key: %w", err)
		}
	}

	// 2. Build the model list (unique names among the picked models + template recommendations)
	modelsMap := make(map[string]bool)
	for _, m := range template.Models {
		modelsMap[m] = true
	}
	modelsMap[defaultModel] = true
	if visionModel != "" && visionModel != "none" {
		modelsMap[visionModel] = true
	}
	if fastModel != "" && fastModel != "follow" {
		modelsMap[fastModel] = true
	}
	var models []string
	for m := range modelsMap {
		if m != "" {
			models = append(models, m)
		}
	}
	sort.Strings(models)

	ref := template.Name + "/" + defaultModel

	// 3. Save the provider entry.
	pv := ProviderView{
		Name:          template.Name,
		Kind:          template.Kind,
		BaseURL:       template.BaseURL,
		APIKeyEnv:     template.APIKeyEnv,
		ContextWindow: template.ContextWindow,
		Default:       defaultModel,
		Models:        models,
	}
	if err := a.SaveProvider(pv); err != nil {
		return fmt.Errorf("save provider: %w", err)
	}

	// 4. Update the config for Vision and Fast models.
	_ = a.applyConfigChange(func(c *config.Config) error {
		// Vision follows the registry's per-provider capability (sourced from
		// models.dev), or is forced on when the user explicitly picked a vision
		// model. A bare `true` here marks text-only providers (deepseek-chat, o1,
		// …) vision-capable, which later silently drops images the model rejects.
		vision := template.Vision
		if visionModel != "" && visionModel != "none" {
			vision = true
		}
		for i := range c.Providers {
			if c.Providers[i].Name == template.Name {
				c.Providers[i].Vision = vision
				break
			}
		}
		if visionModel != "" && visionModel != "none" {
			c.Cowork.ScreenshotVLMModel = template.Name + "/" + visionModel
		}
		voiceModel = strings.TrimSpace(voiceModel)
		if voiceModel != "" && voiceModel != "none" {
			c.Cowork.VoiceModel = template.Name + "/" + voiceModel
		}
		if fastModel == "follow" || fastModel == "" {
			c.Agent.FastTaskModel = "" // follows default
		} else {
			c.Agent.FastTaskModel = template.Name + "/" + fastModel
		}
		return nil
	})

	// 5. Set as global default so the agent starts using it immediately.
	if err := a.SetDefaultModel(ref); err != nil {
		a.mu.Lock()
		if tab := a.activeTabLocked(); tab != nil {
			tab.StartupErr = "provider configured but could not set as default: " + err.Error()
		}
		a.mu.Unlock()
	}
	return nil
}

// ConnectKey validates apiKey against the provider endpoint, persists it to the
// global credentials file, and rebuilds the controller so the new key takes effect.
//
// Deprecated: kept for backward compatibility with older frontends. New code
// should use ProbeVendorKey + SetupProvider (the multi-vendor onboarding flow).
func (a *App) ConnectKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return fmt.Errorf("key is required")
	}
	// Legacy single-provider path: validate against the first configured
	// provider's endpoint, then store under FAIRPEER_API_KEY.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	var baseURL string
	for _, p := range cfg.Providers {
		if p.APIKeyEnv == onboardingKeyEnv || p.APIKey() != "" {
			baseURL = p.BaseURL
			break
		}
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	if baseURL != "" {
		if err := probeVendorKey(ctx, baseURL, apiKey); err != nil {
			return fmt.Errorf("validate: %w", err)
		}
	}
	if err := upsertCredential(onboardingKeyEnv, apiKey); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	if err := a.rebuild(); err != nil {
		a.mu.Lock()
		if tab := a.activeTabLocked(); tab != nil {
			tab.StartupErr = err.Error()
		}
		a.mu.Unlock()
	}
	return nil
}

// gateApproverForSubagents adapts the controller to boot's sub-agent bridge
// (spec 3-10). Defined here to avoid the boot→control import cycle.
func gateApproverForSubagents(ctrl *control.Controller) permission.Approver {
	return ctrl.GateApprover()
}

// ImportMCPServersJSON parses a standard `mcpServers` JSON block (Claude
// Desktop / Cursor / Codex paste format) and imports every entry. Returns the
// count imported; entries with invalid shape are skipped silently (the caller
// shows the count; users can fix leftovers manually). Existing servers with
// the same name are updated in place.
func (a *App) ImportMCPServersJSON(jsonText string) (int, error) {
	var block struct {
		Servers map[string]struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(jsonText), &block); err != nil {
		return 0, fmt.Errorf("invalid JSON: %w", err)
	}
	if len(block.Servers) == 0 {
		return 0, fmt.Errorf("no mcpServers entries found")
	}
	ctrl := a.activeCtrl()
	if ctrl == nil {
		return 0, fmt.Errorf("no active session")
	}
	count := 0
	for name, s := range block.Servers {
		transport := strings.ToLower(strings.TrimSpace(s.Type))
		if transport == "" {
			if s.URL != "" {
				transport = "http"
			} else {
				transport = "stdio"
			}
		}
		// Map "streamable-http"/"streamable_http" → "http" (fairpeer's canonical).
		if transport == "streamable-http" || transport == "streamable_http" {
			transport = "http"
		}
		entry := config.PluginEntry{
			Name:    name,
			Type:    transport,
			Command: s.Command,
			Args:    s.Args,
			Env:     s.Env,
			URL:     s.URL,
			Headers: s.Headers,
		}
		entry, _ = config.NormalizePluginCommandLine(entry)
		if err := a.saveDesktopMCPServer(entry); err != nil {
			continue // skip failures; count only successes
		}
		_, _ = ctrl.ConnectMCPServer(entry)
		count++
	}
	return count, nil
}
