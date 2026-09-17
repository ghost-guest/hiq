// Package boot assembles a ready-to-drive control.Controller from configuration:
// it loads config, resolves the model(s), builds the tool registry (built-ins +
// plugins), wires the permission gate, and constructs the executor. It is the one
// place that turns "what the user configured" into "a Controller a frontend can
// drive", so every frontend — the terminal TUI, the HTTP/SSE server, the desktop
// webview — shares the exact
// same assembly instead of each re-deriving it. Frontends pass only a sink and a
// couple of run knobs; everything else comes from config.
package boot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zzycxz/fairpeer/internal/agent"
	"github.com/zzycxz/fairpeer/internal/apihelper"
	"github.com/zzycxz/fairpeer/internal/assets"
	"github.com/zzycxz/fairpeer/internal/codegraph"
	"github.com/zzycxz/fairpeer/internal/command"
	"github.com/zzycxz/fairpeer/internal/config"
	"github.com/zzycxz/fairpeer/internal/control"
	"github.com/zzycxz/fairpeer/internal/deferred"
	"github.com/zzycxz/fairpeer/internal/event"
	"github.com/zzycxz/fairpeer/internal/hook"
	"github.com/zzycxz/fairpeer/internal/installsource"
	"github.com/zzycxz/fairpeer/internal/instruction"
	"github.com/zzycxz/fairpeer/internal/jobs"
	"github.com/zzycxz/fairpeer/internal/lsp"
	"github.com/zzycxz/fairpeer/internal/memory"
	"github.com/zzycxz/fairpeer/internal/netclient"
	"github.com/zzycxz/fairpeer/internal/netdev"
	"github.com/zzycxz/fairpeer/internal/outputstyle"
	"github.com/zzycxz/fairpeer/internal/patrol"
	"github.com/zzycxz/fairpeer/internal/permission"
	"github.com/zzycxz/fairpeer/internal/plugin"
	"github.com/zzycxz/fairpeer/internal/projectkb"
	"github.com/zzycxz/fairpeer/internal/provider"
	"github.com/zzycxz/fairpeer/internal/rag"
	runtimepkg "github.com/zzycxz/fairpeer/internal/runtime"
	"github.com/zzycxz/fairpeer/internal/sandbox"
	"github.com/zzycxz/fairpeer/internal/secret"
	"github.com/zzycxz/fairpeer/internal/skill"
	"github.com/zzycxz/fairpeer/internal/tool"
	"github.com/zzycxz/fairpeer/internal/tool/builtin"
)

var (
	sandboxEnforceWarnOnce sync.Once
	powershellWarnOnce     sync.Once
	secretDegradedOnce     sync.Once
)

// warnDegradedSecretStore emits a one-time notice when the secret store falls
// back to machine-bound encryption (no OS keystore and no passphrase
// configured — typically headless Linux without a session keyring). The
// ciphertext is then recomputable by any local process, so the user should
// know and optionally set FAIRPEER_SECRET_PASSPHRASE(_FILE).
func warnDegradedSecretStore(stderr io.Writer, store *secret.Store) {
	backend, degraded := store.SecurityMode()
	if !degraded {
		return
	}
	secretDegradedOnce.Do(func() {
		fmt.Fprintf(stderr, "warning: secret store is using degraded machine-bound encryption (backend %q); "+
			"set FAIRPEER_SECRET_PASSPHRASE or run on a system with a keychain/secret service for user-bound encryption\n", backend)
	})
}

// ErrUnknownModel is returned by Build when the configured model can't be
// resolved to a provider — e.g. a default_model left over from a renamed or
// removed provider. Callers can detect it (errors.Is) to re-run setup.
var ErrUnknownModel = errors.New("unknown model")

// globalBudget is the process-level LLM request budget, set once per Build from
// [lll] config. NewProviderWithProxy wraps every provider with it so main-agent
// + subagent + background tasks share one per-API-key RPM quota. nil/zero-RPM
// disables limiting (RateLimitedProvider passes through unwrapped).
//
// Whether a provider is the "main" (high-priority) one is now passed explicitly
// to NewProviderWithProxy rather than via the old process-global
// buildingMainProvider flag, so concurrent boot.Build calls no longer race on
// which provider gets main-priority RPM slots.
var globalBudget *provider.RequestBudget

// GlobalBudget exposes the budget for the desktop layer (status display, cost
// estimates, and direct callers like RagAsk that talk HTTP outside the provider
// layer). May be nil when limiting is off.
func GlobalBudget() *provider.RequestBudget { return globalBudget }

// platformBudgetKey returns the budget bucket key for direct API
// calls (image/video tools, RAG embedding, the VLM fallback). Because
// BudgetKeyForConfig keys only on baseURL+apiKey (not name), this resolves to
// the SAME bucket the main conversation uses when it targets the platform
// endpoint with the platform API key — so all such calls share one per-minute quota,
// matching how the platform meters a single API key. The placeholder name is
// passed only to satisfy the call-site signature.
func platformBudgetKey() string {
	return provider.BudgetKeyForConfig("platform-direct", apihelper.BaseURL, os.Getenv("FAIRPEER_API_KEY"))
}

// ragBudgetKey returns the budget bucket key for RAG extraction (the
// platform extractor). It resolves the model with the SAME priority order initRAG
// uses (extract_model → fast_task_model → default_model) so the key matches the
// endpoint the extractor actually hits. If the model can't be resolved it falls
// back to the generic platform key so extraction is still gated under one bucket.
func ragBudgetKey(cfg *config.Config) string {
	if cfg != nil {
		ref := strings.TrimSpace(cfg.Cowork.ExtractModel)
		if ref == "" {
			ref = strings.TrimSpace(cfg.Agent.FastTaskModel)
		}
		if ref == "" {
			ref = strings.TrimSpace(cfg.DefaultModel)
		}
		if ref != "" {
			if e, ok := cfg.ResolveModel(ref); ok {
				return provider.BudgetKeyForConfig("rag-extract", e.BaseURL, e.APIKey())
			}
		}
	}
	return platformBudgetKey()
}

// RebindRAGBudget re-injects the current globalBudget into an extractor, so a
// runtime RPM change (settings rebuild) or the first boot.Build propagates to
// RAG extraction without an app restart. extractor may be nil or not implement
// rag.BudgetSetter (e.g. HE-based extraction), in which case nothing is rebound.
// Pass the loaded config so the RAG bucket key resolves to the extract model.
func RebindRAGBudget(extractor any, cfg *config.Config) {
	// Rebind the extractor if it supports it (platform extractor does; HE-based
	// extraction runs in a subprocess and is not gated here).
	if extractor == nil {
		return
	}
	if bs, ok := extractor.(rag.BudgetSetter); ok && globalBudget != nil {
		bs.SetBudget(globalBudget, ragBudgetKey(cfg))
	}
}

// RagAskBudgetKey returns the budget bucket key for a knowledge-base Q&A call,
// resolving from cfg the same model RagAsk uses (fast_task_model, then the
// default). Callers that talk /chat/completions directly (outside the provider
// layer) use this with GlobalBudget().Acquire so their calls share the
// per-minute quota of the model they target.
func RagAskBudgetKey(cfg *config.Config) string {
	if cfg != nil {
		ref := strings.TrimSpace(cfg.Agent.FastTaskModel)
		if ref == "" {
			ref = strings.TrimSpace(cfg.DefaultModel)
		}
		if ref != "" {
			if e, ok := cfg.ResolveModel(ref); ok {
				return provider.BudgetKeyForConfig("rag-ask", e.BaseURL, e.APIKey())
			}
		}
	}
	return platformBudgetKey()
}

// Options carries the per-run knobs a frontend chooses; everything else is read
// from configuration. Model "" falls back to the configured default_model;
// MaxSteps 0 uses the config/default. RequireKey forces the executor's API key to
// be present (run/serve pass true so a missing key fails fast; chat/desktop pass
// false so the UI is reachable before a key is set). Sink receives the agent's
// typed event stream.
type Options struct {
	Model      string
	MaxSteps   int
	RequireKey bool
	Sink       event.Sink
	// EffortOverride is a session-local reasoning effort override. Nil means use
	// the resolved provider config; a non-nil empty string means provider default.
	EffortOverride *string
	// Stderr is the writer for diagnostic warnings and plugin subprocess
	// stderr output. When nil, defaults to os.Stderr. Set to io.Discard
	// during model switch inside a bubbletea session to prevent any output
	// from corrupting the TUI's terminal raw mode.
	Stderr io.Writer
	// WorkspaceRoot is the project root directory for config, skills, memory,
	// commands, hooks, and tool confinement. When empty, the current working
	// directory is used (CLI default). Desktop tabs pass their project root here
	// so each tab loads its own config/skills/hooks without changing the process
	// cwd — enabling concurrent multi-project sessions.
	WorkspaceRoot string
	// ExtraPlugins are session-scoped MCP servers supplied by a host transport
	// (for example ACP session/new). They are connected eagerly for this
	// controller but are not persisted to fairpeer.toml.
	ExtraPlugins []plugin.Spec
	// SessionDir overrides where persisted chat transcripts are written. When
	// empty, the shared CLI/global session directory is used.
	SessionDir string
	// Host is an externally-owned plugin.Host the controller should adopt instead
	// of allocating its own. When set, Build does NOT create or Close the host —
	// the caller owns its lifecycle (e.g. desktop shares one host per workspace
	// root across tabs so a multi-tab project doesn't spawn N codegraph
	// processes). The host still receives this session's configured plugins via
	// Add, but Close leaves it untouched. Nil (the default) keeps the legacy
	// per-session host that Build creates and closes.
	Host *plugin.Host
	// Profile layers product-mode overrides on top of the resolved config: a
	// different model/effort, an appended/replaced system prompt, a skill
	// whitelist/blacklist, and a plugin whitelist. It is the mechanism behind
	// app.SwitchProfile("cowork") — the same rebuild flow as model switching,
	// generalized to a bundle. Nil means "dev" / unprofiled behaviour (Model,
	// prompt, skills, plugins all come from config unchanged). When both Model
	// and Profile.Model are set, Model wins (caller's explicit knob beats the
	// profile default); same for EffortOverride vs Profile.Effort.
	Profile *config.Profile
	// Present enables the presentation sidecar: the controller records the rich
	// view-only event stream (tool dispatches/results/progress, notice/phase/
	// compaction cards) to <session>.present.jsonl so a frontend can rebuild the
	// exact transcript after a tab switch/reload/crash. The sidecar never feeds
	// the LLM. Desktop frontends set this; the TUI/CLI leave it off (the terminal
	// scrollback IS the presentation layer, so a reload rebuilds nothing).
	Present bool
}

// Build loads config, resolves the model(s), and returns a Controller wrapping a
// single Agent. The returned controller owns plugin subprocesses; call Close
// (via Controller.Close) to release them.
// to release them.
func Build(ctx context.Context, opts Options) (*control.Controller, error) {
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	root := resolveWorkspaceRoot(opts.WorkspaceRoot)
	// One-time import of v1/v0.5 legacy config — runs before Load so the freshly
	// written config + ~/.env are picked up this same boot. CLI Run also calls this
	// before config-only commands; this call stays as the shared frontend fallback.
	migrated, migErr := config.MigrateLegacyIfNeeded()
	cfg, err := config.LoadForRoot(root)
	if err != nil {
		return nil, err
	}
	modelName := opts.Model
	// A profile can pin a model (e.g. cowork uses a cheaper office model). The
	// caller's explicit opts.Model wins over the profile default, so a manual
	// model pick inside a profile is respected.
	if modelName == "" && opts.Profile != nil && strings.TrimSpace(opts.Profile.Model) != "" {
		modelName = opts.Profile.Model
	}
	if modelName == "" {
		modelName = cfg.DefaultModel
	}
	entry, ok := cfg.ResolveModel(modelName)
	if !ok {
		// A stale explicit model (a persisted tab/session outliving its
		// provider — e.g. desktop-tabs.json after a config edit) must never
		// brick startup. Fall back through the same chain the desktop uses
		// (default_model, then any usable provider — keyless local endpoints
		// the user actually added, since this was an explicit request, not
		// onboarding; ambient injected presets stay out), and surface the
		// switch as a warning instead of an error. Only a request that names
		// default_model itself (nothing left to try) still errors.
		if fallback, _, fok := cfg.ResolveModelWithFallback(modelName); fok && fallback != modelName {
			slog.Warn("boot: unknown model, falling back", "requested", modelName, "fallback", fallback)
			modelName = fallback
			entry, ok = cfg.ResolveModel(modelName)
		}
	}
	if !ok {
		return nil, fmt.Errorf("%w %q (configured: %s); note: defining [[providers]] replaces the built-in presets, so add a [[providers]] entry for it or use a configured name, or run `fairpeer setup` to reconfigure", ErrUnknownModel, modelName, providerNames(cfg))
	}
	if opts.EffortOverride != nil {
		entry.Effort = *opts.EffortOverride
		if entry.Kind == "anthropic" && strings.TrimSpace(entry.Effort) != "" && strings.TrimSpace(entry.Thinking) == "" {
			entry.Thinking = "adaptive"
		}
	} else if opts.Profile != nil && strings.TrimSpace(opts.Profile.Effort) != "" {
		// No explicit caller effort: adopt the profile's effort as the base before
		// the normal anthropic thinking heuristic applies.
		entry.Effort = opts.Profile.Effort
		if entry.Kind == "anthropic" && strings.TrimSpace(entry.Effort) != "" && strings.TrimSpace(entry.Thinking) == "" {
			entry.Thinking = "adaptive"
		}
	}
	if opts.RequireKey {
		if err := cfg.Validate(modelName); err != nil {
			return nil, err
		}
	}

	// Serialize the frontend's sink once: background jobs (below) emit from their
	// own goroutines, which can overlap a running turn's emission, so every emitter
	// shares this synchronized sink. The job manager is session-scoped — its jobs
	// outlive a turn and are cancelled by Controller.Close.
	sink := event.Sync(opts.Sink)

	if migErr != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "config migration from ~/.fairpeer failed: " + migErr.Error()})
	} else if migrated != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: migrated.Notice()})
	}
	migrateLegacySessionSources(sink)

	// A resolvable model whose API key env is unset would otherwise build fine
	// (RequireKey is false so the UI stays reachable) and then fail silently on the
	// first request, showing as an empty/dead model. Surface the cause up front.
	if !opts.RequireKey && entry.APIKeyEnv != "" && entry.APIKey() == "" {
		sink.Emit(event.Event{Kind: event.Notice, Text: fmt.Sprintf("model %q is selected but its API key %s is not set — requests will fail until you set it", modelName, entry.APIKeyEnv)})
	}
	jm := jobs.NewManager(sink)

	// Push delivery for background results (internal/deferred): a finished job
	// hands its ACTUAL output to the session the moment it exists, instead of
	// waiting for the next turn to drain a one-line summary. The controller
	// installs itself as the deliverer right after it is built, below; until
	// then results only accumulate on disk (so nothing is lost if the session
	// never binds, and a later process flushes them).
	var deferredCoord *deferred.Coordinator
	// Set once the controller exists. Atomic because the completion hook runs on
	// background-job goroutines, concurrent with this assignment.
	var ctrlForJobs atomic.Pointer[control.Controller]
	// The coordinator is also the delivery path for the proactive patrol, so it
	// is built when EITHER feature is on. The job completion hook, however, is
	// installed only when [deferred] itself is on: turning on patrol must not
	// silently start pushing job results.
	if cfg.Deferred.EnabledEffective() || cfg.Patrol.EnabledEffective() {
		deferredCoord = deferred.NewCoordinator(deferred.Options{
			Policy: deferred.Policy{
				TriggerParentTurn: cfg.Deferred.TriggerParentTurnEffective(),
				NotifyOnFailure:   cfg.Deferred.NotifyOnFailureEffective(),
				NotifyOnSuccess:   cfg.Deferred.NotifyOnSuccessEffective(),
			},
			RetryInterval: time.Duration(cfg.Deferred.RetryIntervalSecondsEffective()) * time.Second,
			MaxAttempts:   cfg.Deferred.MaxAttemptsEffective(),
			BodyLimit:     cfg.Deferred.BodyLimitBytesEffective(),
			Logf: func(format string, args ...any) {
				slog.Debug("deferred: " + fmt.Sprintf(format, args...))
			},
		})
		if cfg.Deferred.EnabledEffective() {
			// The hook must not block (it runs on the job's goroutine): Enqueue
			// writes a small JSON table and spawns the delivery attempt.
			jm.SetCompletionHook(func(comp jobs.Completion) {
				c := ctrlForJobs.Load()
				if c == nil {
					return
				}
				sessionPath := c.SessionPath()
				if strings.TrimSpace(sessionPath) == "" {
					return
				}
				_ = deferredCoord.Enqueue(sessionPath, deferred.Task{
					ID:          "job:" + comp.ID,
					Source:      "job",
					Title:       deferredJobTitle(comp),
					Body:        comp.Output,
					Status:      deferredJobStatus(comp.Status),
					Meta:        deferred.Meta{Kind: comp.Kind, Label: comp.Label},
					SessionPath: sessionPath,
				})
			})
		}
		deferredCoord.Start()
	}

	// Proactive patrol ([patrol]): while the user is away, the bound session
	// periodically inspects its workspace and reports what changed — through the
	// same durable deferred path a finished job uses. The permission dial is
	// [patrol] mode (off by default; read-only even when on) plus allow_write;
	// see config.PatrolConfig. Inspectors are strictly read-only.
	//
	// The loop lives for the process, like the job manager: a rebuild swaps the
	// controller and the Targets closure simply resolves the new one.
	var patrolMgr *patrol.Manager
	if deferredCoord != nil && cfg.Patrol.EnabledEffective() {
		patrolMgr = patrol.New(patrol.Options{
			Mode:       patrol.ParseMode(cfg.Patrol.ModeEffective()),
			AllowWrite: cfg.Patrol.AllowWriteEffective(),
			Interval:   time.Duration(cfg.Patrol.IntervalSecondsEffective()) * time.Second,
			Inspectors: patrolInspectors(cfg.Patrol.Checks),
			BodyLimit:  cfg.Patrol.BodyLimitBytesEffective(),
			Targets: func() []patrol.Target {
				c := ctrlForJobs.Load()
				if c == nil {
					return nil
				}
				sessionPath, wsRoot := c.SessionPath(), c.WorkspaceRoot()
				if strings.TrimSpace(sessionPath) == "" || strings.TrimSpace(wsRoot) == "" {
					return nil
				}
				return []patrol.Target{{SessionPath: sessionPath, Root: wsRoot, Label: filepath.Base(wsRoot)}}
			},
			Deliverer: deferredCoord,
			Logf: func(format string, args ...any) {
				slog.Debug("patrol: " + fmt.Sprintf(format, args...))
			},
		})
		patrolMgr.Start()
	}

	// A settings change re-runs Build. If this Build fails after the background
	// services above have started, stop them here so a failed rebuild does not
	// leak a ticker; the success path swaps them into the package slots instead.
	var built bool
	defer func() {
		if built {
			return
		}
		if patrolMgr != nil {
			go patrolMgr.Close()
		}
		if deferredCoord != nil {
			go deferredCoord.Close()
		}
	}()

	proxySpec := cfg.NetworkProxySpec()
	if err := netclient.Validate(proxySpec); err != nil {
		return nil, err
	}
	// Publish the proxy spec so the VLM runner (which the cowork branch installs
	// below via SetProviderChatRunner) can build a proxy-aware provider. Read in
	// runProviderVLMChat; assigned here so every Build re-publishes the current
	// spec (a profile switch re-runs Build, re-resolving the proxy).
	proxySpecForVLM = proxySpec

	// Initialize the global LLM request budget from [llm] config. RPM=0 (the
	// default) disables limiting; NewProviderWithProxy then passes providers
	// through unwrapped, preserving backward compatibility.
	globalBudget = provider.NewRequestBudget(cfg.LLM.RPM, cfg.LLM.ReserveMain)
	httpClient, err := netclient.NewHTTPClient(proxySpec, netclient.TransportOptions{})
	if err != nil {
		return nil, err
	}
	// Inject the proxy-aware client into the shared apihelper. Without this,
	// the direct /chat/completions callers (scheduler time-parser, RAG ask,
	// RAG extractor) bypass the proxy and fail with EOF in environments that
	// require one. Done unconditionally so all callers share the same client.
	apihelper.SetClient(httpClient)
	apihelper.SetBaseDomain(cfg.Cowork.FastLLMBaseDomain)

	// The executor's provider is the main-agent provider — pass mainProvider=true
	// so NewProviderWithProxy marks it high-priority (always granted RPM slots;
	// reserve_main protects it from background tasks). Subagent/classifier/VLM
	// providers built elsewhere pass false (background priority, respect reserve).
	execProv, err := NewProviderWithProxy(entry, proxySpec, true)
	if err != nil {
		return nil, err
	}

	sysPrompt, err := cfg.ResolveSystemPrompt()
	if err != nil {
		return nil, err
	}
	// A profile can replace the resolved system prompt wholesale (file) or
	// append to it (addon). File takes precedence and reads the file the same way
	// cfg.ResolveSystemPrompt does, so a profile's system_prompt_file is an
	// override of agent.system_prompt_file, not a third prompt. The addon is
	// folded early so it precedes instruction/output-style/memory/skill appends —
	// a profile's mode bias should be part of the base, not the tail.
	if opts.Profile != nil {
		if pf := strings.TrimSpace(opts.Profile.SystemPromptFile); pf != "" {
			b, rerr := os.ReadFile(pf)
			if rerr != nil {
				return nil, fmt.Errorf("profile %q system_prompt_file: %w", opts.Profile.Name, rerr)
			}
			sysPrompt = strings.TrimSpace(string(b))
		}
		if addon := strings.TrimSpace(opts.Profile.SystemPromptAddon); addon != "" {
			// For the cowork/netdev profiles, drop capability-routing rows that
			// target disabled skills so the prompt doesn't instruct the model to
			// call a skill the user turned off (otherwise the model retries the
			// disabled skill instead of telling the user to re-enable it).
			// disabledNames is computed below for the skill store; we recompute
			// the effective set here (cheap, config-local) since the prompt is
			// assembled before that.
			switch config.ProfileNameKey(opts.Profile.Name) {
			case config.ProfileCowork:
				effective := cfg.DisabledSkillNames()
				effective = applyProfileToSkillDisabled(opts.Profile, effective)
				addon = config.CoworkPromptAddon(effective)
			case config.ProfileNetDev:
				effective := cfg.DisabledSkillNames()
				effective = applyProfileToSkillDisabled(opts.Profile, effective)
				addon = config.NetdevPromptAddon(effective)
			}
			if strings.TrimSpace(addon) != "" {
				sysPrompt += "\n\n" + addon
			}
		}
	}
	// Model-specific prompt addon (thinking encouragement, serial constraint, etc.)
	// Built once per Build — cache-stable for the session. Model switches go
	// through SetModelForTab's full boot.Build, so this addon is recomputed
	// with the new family; never add a model-swap path that skips the rebuild
	// (MODEL_ROUTING_SPEC invariant 3).
	if addon := instruction.ForModel(entry.Model); addon != "" {
		sysPrompt += "\n\n" + addon
	}
	// Inject the current date/time so the model doesn't fall back to its training
	// cutoff year when generating absolute timestamps (e.g. schedule_create with
	// "at 2025-..." instead of the real 2026). This is the root-cause fix for the
	// "AI created a task in the wrong year" bug. Built once per
	// Build (cache-stable for the session).
	now := time.Now()
	if strings.HasPrefix(strings.ToLower(cfg.Language), "zh") {
		weekdays := []string{"日", "一", "二", "三", "四", "五", "六"}
		sysPrompt += fmt.Sprintf("\n\n# 当前时间\n【重要】现在是 %s 周%s。在任何需要判断当前年份（例如搜索“最新”资讯）或生成具体日期/时间的任务中，都必须严格以此当前时间为准！",
			now.Format("2006-01-02 15:04"), weekdays[int(now.Weekday())])
	} else {
		sysPrompt += fmt.Sprintf("\n\n# Current Time\n[IMPORTANT] Current time: %s %s. In any task that requires judging the current year (e.g. searching for 'latest' news) or generating concrete dates/times, you MUST use this current time as the reference.",
			now.Format("2006-01-02 15:04 MST"), now.Weekday())
	}
	// Output style: fold the selected persona/tone block into the base prompt
	// before language/memory/skills append, so a "replace" style (keep-coding
	// false) still keeps those. Applied once, into the cache-stable prefix.
	// (some providers do not report cache tokens; the prefix stability still helps.)
	if st, ok := outputstyle.Resolve(cfg.Agent.OutputStyle, outputstyle.Dirs()); ok {
		sysPrompt = outputstyle.Apply(sysPrompt, st)
	}
	sysPrompt += "\n\n" + config.LanguagePolicy

	// Persistent memory (fairpeer.md / AGENTS.md hierarchy + portrait layer +
	// auto-memory index) folds into the system prompt exactly here, once: it
	// becomes part of the durable, cache-stable prefix every turn reuses, so
	// memory costs nothing per turn. Mid-session changes never touch this prefix
	// — they ride the controller's transient turn-injection and fold in on the
	// next session. Profile partitions both the portrait and the store by mode
	// (dev/cowork), so a mode switch rebuilds with a disjoint memory subtree.
	mem := memory.Load(memory.Options{
		CWD:             root,
		UserDir:         config.MemoryUserDir(),
		Profile:         profileName(opts.Profile),
		SkipProjectDocs: opts.Profile.SkipProjectInstructions(),
		InjectIndex:     cfg.MemoryInjectIndex(),
		IndexMaxChars:   cfg.MemoryIndexMaxChars(),
	})
	projectChecks := instruction.ExtractHostChecks(mem.Docs)
	sysPrompt = memory.Compose(sysPrompt, mem)

	// Project knowledge hub (项目知识地图): a compact pointer to the project's
	// modules, docs, saved memory and team progress, rendered from whatever the
	// hub last persisted. Folded into the same cache-stable prefix for the same
	// reason as memory — it is deterministic for a given kb.json, so it costs
	// nothing per turn; a mid-session kb_sync folds in on the NEXT session, and
	// the full map is always fetched on demand via kb_map / kb_search. Renders
	// to "" (leaving the prompt byte-identical) until the project first syncs.
	sysPrompt = projectkb.ComposeIndex(sysPrompt, config.MemoryUserDir(), root,
		profileName(opts.Profile), cfg.MemoryIndexMaxChars())

	// Skills: discover playbooks (built-in + project/custom/global) and fold their
	// one-liner index into the same cache-stable prefix — names + descriptions
	// only; bodies load on demand via run_skill or "/<name>". Bodies never enter
	// the prefix, so the index costs a fixed, small amount per turn.
	//
	// Profile skill filtering: a profile can disable additional skills and/or
	// expose a whitelist (only those skills stay callable). We resolve the merged
	// disabled set once here and pass it to BOTH stores so the live store and the
	// "all skills" index agree on what a profile hides.
	disabledNames := cfg.DisabledSkillNames()
	if opts.Profile != nil {
		disabledNames = applyProfileToSkillDisabled(opts.Profile, disabledNames)
	}
	// The LIVE store filters by disabledNames so disabled/profile-hidden skills
	// are uncallable. The ALL store deliberately does NOT filter — it lists every
	// discovered skill (including disabled) so the pinned index can tag them as
	// [关闭] for management (re-enable hints). Profile whitelist hiding for the
	// index is applied in the loop below via the Disabled flag.
	// skillStateDir is where cross-session skill usage lives (skill_usage.json),
	// the same area dream_state.json occupies (the parent of the session dir).
	// Derived from the tab's actual session dir (opts.SessionDir) so it follows
	// the workspace AND — once sessions are partitioned by profile — the profile:
	// dev and cowork keep separate usage stats, matching their disjoint skill sets.
	// Empty when no session dir resolvable → usage tracking is a no-op, safe.
	stateSessionDir := opts.SessionDir
	if stateSessionDir == "" {
		stateSessionDir = config.SessionDir()
	}
	skillStateDir := filepath.Dir(stateSessionDir)
	// Legacy fallback: before profile partition, skill_usage.json sat at the
	// user-config root (<userDir>/skill_usage.json). The profile tier moved it
	// under <profile>/sessions's parent, orphaning the old file. Recover it.
	skillLegacyPath := ""
	if udir := config.MemoryUserDir(); udir != "" {
		skillLegacyPath = filepath.Join(udir, "skill_usage.json")
	}
	// Release the embedded ppt-auto skill to ~/.fairpeer/skills/ppt-auto/ before
	// the skill store scans, so the just-released skill is discovered this run.
	// Best-effort: a failure is logged but never aborts startup, since the user
	// may already have a working skill from a prior release or a manual install.
	if err := assets.EnsurePPTAutoSkill(); err != nil {
		slog.Warn("assets: failed to release embedded ppt-auto skill", "err", err)
	}
	// Release the embedded helper scripts (pdf_to_page_images.py etc.) to
	// ~/.fairpeer/scripts/. Without this, a packaged binary running outside the
	// repo tree cannot find the renderer and the whole PDF→PPT visual path dies
	// with "pdf_to_page_images.py not found" (the exact failure seen on a real
	// 32-page PDF test: no page-N.json, no per-page redraw, tables lost).
	if err := assets.EnsureHelperScripts(); err != nil {
		slog.Warn("assets: failed to release embedded helper scripts", "err", err)
	}
	// Restore the user's ppt_mode choice into the just-released config.
	// EnsurePPTAutoSkill ships mode="fast"; without this, a SkillVersion bump
	// resets the user's "validate" choice to "fast" on every launch.
	if err := assets.SyncPPTMode(cfg.Cowork.PPTMode); err != nil {
		slog.Warn("assets: failed to sync ppt_mode", "err", err)
	}
	skillStore := skill.New(skill.Options{
		ProjectRoot:     root,
		CustomPaths:     cfg.SkillCustomPaths(),
		ExcludedPaths:   cfg.SkillExcludedPaths(),
		DisabledNames:   disabledNames,
		MaxDepth:        cfg.SkillMaxDepth(),
		Stderr:          opts.Stderr,
		StateDir:        skillStateDir,
		LegacyStatePath: skillLegacyPath,
	})
	skills := skillStore.List()
	allSkillStore := skill.New(skill.Options{ProjectRoot: root, CustomPaths: cfg.SkillCustomPaths(), ExcludedPaths: cfg.SkillExcludedPaths(), MaxDepth: cfg.SkillMaxDepth(), Stderr: io.Discard, StateDir: skillStateDir, LegacyStatePath: skillLegacyPath})
	allSkills := allSkillStore.List()
	// A profile whitelist can only be enforced once we know every skill name:
	// anything not whitelisted is disabled. We compute the effective disabled
	// status per skill here — config-disabled OR profile-additive-disabled OR
	// (whitelist active AND not whitelisted). The store already hid the first two
	// from the live Skills() list; this re-marks them for the index tags.
	whitelist := profileSkillWhitelist(opts.Profile)
	// Cold-skill detection: skills unused longer than the configured threshold
	// are tagged [休眠] in the index. Built-ins are exempt (they're cheap, and a
	// user shouldn't lose explore/research just because they haven't needed it).
	// Only user-authored (non-builtin) skills with no usage record are eligible,
	// so a freshly installed skill isn't retired before its first use.
	var coldNames []string
	if ut := allSkillStore.Usage(); ut != nil {
		threshold := time.Duration(cfg.Dream.SkillColdDaysEffective()) * 24 * time.Hour
		var known []string
		for _, s := range allSkills {
			if s.Scope == skill.ScopeBuiltin {
				continue // never retire built-ins for inactivity
			}
			known = append(known, s.Name)
		}
		coldNames = ut.ColdSkillNames(threshold, true, known)
	}
	coldSet := make(map[string]bool, len(coldNames))
	for _, n := range coldNames {
		coldSet[config.SkillNameKey(n)] = true
	}
	indexedSkills := make([]skill.Skill, 0, len(allSkills))
	// userDisabledSet is the config-only disabled set (what the user explicitly
	// turned off), WITHOUT the profile's whitelist hiding mixed in. We need it
	// separate from disabledNames (which applyProfileToSkillDisabled merged the
	// whitelist-hidden names into) so the index can distinguish "user turned this
	// off → keep in index as [关闭], hint at re-enabling" from "profile whitelist
	// hid this → omit from index entirely". Profile.DisabledSkills (additive) are
	// treated as user intent here: a profile that explicitly disables a skill is
	// closer to "off" than to "hidden by default".
	userDisabledSet := make(map[string]bool, len(cfg.DisabledSkillNames()))
	for _, n := range cfg.DisabledSkillNames() {
		userDisabledSet[config.SkillNameKey(n)] = true
	}
	if opts.Profile != nil {
		for _, n := range opts.Profile.DisabledSkills {
			userDisabledSet[config.SkillNameKey(n)] = true
		}
	}
	for _, s := range allSkills {
		userDisabled := userDisabledSet[config.SkillNameKey(s.Name)]
		// A profile whitelist (e.g. the dev profile) hides skills NOT named in it.
		// This is distinct from a user turning a skill off: profile hiding is
		// automatic and reversible by switching profile, so it must NOT pollute the
		// coding model's prompt with office-skill descriptions the user never opted
		// out of. We tag such skills ProfileHidden and skip them entirely below —
		// the model neither sees them nor suggests re-enabling. User-disabled skills
		// stay in the index with [关闭] so the model can hint at re-enabling.
		profileHidden := !userDisabled && whitelist != nil && !whitelist[config.SkillNameKey(s.Name)]
		s.Disabled = userDisabled
		s.ProfileHidden = profileHidden
		// Mark cold (long-unused) skills. A skill that's both disabled and cold
		// shows only [关闭] — user intent outranks auto-retirement in display.
		if !userDisabled && s.Scope != skill.ScopeBuiltin && coldSet[config.SkillNameKey(s.Name)] {
			s.Cold = true
		}
		// Profile-hidden skills are omitted from the pinned index (zero prompt
		// cost). User-disabled ones still enter it, tagged [关闭].
		if profileHidden {
			continue
		}
		indexedSkills = append(indexedSkills, s)
	}
	sysPrompt = skill.ApplyIndex(sysPrompt, indexedSkills)

	reg := tool.NewRegistry()
	// Compute write roots: workspace + user allow_write + ~/.fairpeer (for
	// built-in skill configs like ppt-auto/template_config.json, which skills
	// must update at runtime but live outside the workspace).
	writeRoots := cfg.WriteRootsForRoot(root)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		writeRoots = append(writeRoots, filepath.Join(home, ".fairpeer"))
	}
	// Spec shape follows the upstream Reasonix sandbox contract: the fail-closed
	// decision now lives in the sandbox itself (a restricted preset refuses to
	// run unconfined when no OS backend exists), so [sandbox] require_available
	// no longer changes the outcome — enforce refuses either way. strict_writes
	// maps onto the narrower MinimalWrites grant set.
	bashSpec := sandbox.Spec{Mode: cfg.BashMode(), WriteRoots: writeRoots, Network: cfg.Sandbox.Network}
	bashSpec.MinimalWrites = cfg.Sandbox.StrictWrites
	if bashSpec.Mode == "enforce" && !sandbox.Available() {
		sandboxEnforceWarnOnce.Do(func() {
			fmt.Fprintln(stderr, "warning: "+sandbox.UnavailableMessage())
		})
	}
	if sandbox.ResolveShell("", "", nil).Kind == sandbox.ShellPowerShell {
		powershellWarnOnce.Do(func() {
			fmt.Fprintln(stderr, "warning: bash not found on PATH; the shell tool will run commands under Windows PowerShell. Install Git for Windows or WSL to use bash.")
		})
	}
	searchSpec := builtin.ResolveSearch(cfg.Tools.Search.Engine, cfg.Tools.Search.RgPath, stderr)
	bashTimeout := time.Duration(cfg.BashTimeoutSeconds()) * time.Second
	addBuiltins(reg, cfg.Tools.Enabled, writeRoots, cfg.ReadRoots(), bashSpec, bashTimeout, searchSpec, stderr, root, proxySpec)

	// netdev-only tools: registered ONLY in the netdev profile branch — the
	// reverse half of the hard seal (dev/cowork registries never contain them;
	// NETDEV_SPEC §7.1). netdev_exec is read-only by construction: the driver
	// classifier refuses anything that is not a read command, and the profile's
	// tool_scope already removed bash/file-write tools.
	if config.ProfileNameKey(profileName(opts.Profile)) == config.ProfileNetDev {
		netdev.RegisterTools(reg, cfg)
		// Ops knowledge namespace (§7.1/§7.2): a rag_search/rag_import pair
		// PINNED to the "netdev:" collections (vendor docs, config backups).
		// The store enforces the boundary both ways — cowork rag_* cannot see
		// the namespace, and these tools cannot leave it. Gated by the same
		// knowledge-base master switch as cowork ([cowork] rag_enabled).
		if cfg.Cowork.RAGEnabledOrDefault() {
			for _, t := range builtin.NetDevRAGTools() {
				reg.Add(t)
			}
		}
	}

	// coWork-only capabilities: desktop automation, scheduled tasks, email,
	// RAG, PPT. These are office-specific and stay gated to the cowork profile
	// so the dev tool list stays focused on coding. (Update: they are hidden
	// from the main loop via reg.Hide, so registering them unconditionally
	// doesn't pollute the dev tool list, but allows subagents to work anywhere).
	profileKey := config.ProfileNameKey(profileName(opts.Profile))
	if profileKey == config.ProfileCowork || profileKey == config.ProfileNetDev { // Browser automation tools (cowork + netdev: the ops console's
		// browser tab shares this session infrastructure, and netdev skills
		// may drive browsers too). Hidden from the main loop's schema: the
		// model drives the browser through run_skill("browser-auto")
		// subagents, which reach these via FilterRegistry. This keeps 12
		// browser tool schemas out of every turn.
		builtin.SetConfiguredBrowserPath(cfg.Cowork.BrowserPath)
		for _, t := range builtin.BrowserTools() {
			reg.Add(t)
			reg.Hide(t.Name())
		}
		// Deterministic executor for `executor: browser-flow` skills: run_skill
		// routes those to the kernel step-table runner instead of an LLM
		// subagent — long per-site flows run verbatim, no per-step drift.
		// Callback-shaped so the skill package stays independent of the tool
		// layer (boot is the natural meeting point).
		skill.SetFlowRunner(func(ctx context.Context, sk skill.Skill, arguments string) (string, error) {
			return builtin.RunBrowserFlow(ctx, sk.Body, arguments)
		})
		// Browser launch options must be wired for netdev too — the ops
		// console's spawned sessions read the same global config (visible
		// window + persistent profile + proxy). The proxy URL is resolved
		// from the network spec; auto/env modes fall back to a probe via
		// ProxyURLFor (chromedp needs one concrete --proxy-server URL).
		builtin.SetBrowserLaunchOptions(cfg.Cowork.BrowserHeadless, cfg.Cowork.BrowserUserDataDir, resolveBrowserProxyURL(proxySpec))
	}
	if profileKey == config.ProfileCowork {
		// Desktop automation tools (screenshot, screen_click/type/scroll,
		// get_ui_tree). Windows-native (Win32 BitBlt/SendInput); on other
		// platforms ScreenTools returns nil so nothing registers and cowork
		// still works minus desktop control. Hidden from the main loop's schema:
		// the model drives desktop ops through run_skill("desktop-auto")
		// subagents, which reach these via FilterRegistry.
		for _, t := range builtin.ScreenTools() {
			reg.Add(t)
			reg.Hide(t.Name())
		}
		// Window-management tools (focus/maximize/restore/move/close). The agent
		// uses these to set up the workspace before perceiving/acting: bring the
		// target app to the foreground (so clicks/keys land in it, not whatever's
		// on top), maximize/position it (so the whole UI is visible and unoccluded).
		// Without these, the agent can't reliably control WHERE input goes — the
		// root cause of "I clicked but nothing happened / text went to the wrong
		// window". Windows-only; returns nil elsewhere.
		for _, t := range builtin.WindowTools() {
			reg.Add(t)
			reg.Hide(t.Name())
		}
		// Scheduled-task tools. The scheduler instance is injected by the
		// desktop app (see app.go) via builtin.SetScheduler; boot just
		// registers the tool surface here. When no scheduler is bound (CLI/TUI
		// cowork), the tools return a clear "offline" error.
		for _, t := range builtin.SchedulerTools() {
			reg.Add(t)
			reg.Hide(t.Name())
		}
		// Email tools (SMTP send + IMAP read/search). Config injected from
		// [cowork.smtp] and [cowork.imap]; when a side is unset, that side's
		// tool returns a config error (the other still works).
		//
		// Load encrypted secrets (cowork mail passwords, provider API keys) into
		// the process env before the tools can fire. The tools read passwords and
		// keys via os.Getenv; this is the bridge that makes the encrypted store
		// (secret.Default) usable from the CLI/TUI too, which doesn't run the
		// desktop startup migration. Explicit user/system env still wins.
		//
		// One-time upgrade sweep first: lift the legacy plaintext credentials
		// file (the pre-encryption KEY=value store) into the encrypted secret
		// store. Best-effort — on failure the plaintext file is kept and still
		// loaded by config.loadDotEnv, so nothing breaks; the next successful
		// run finishes the migration. (The desktop runs the same sweep at
		// startup; whichever gets there first wins, the other no-ops.)
		if p := config.UserCredentialsPath(); p != "" {
			if n, err := secret.Default().MigrateEnvFile(p); err != nil {
				fmt.Fprintf(stderr, "warning: credentials migration to the encrypted store failed (kept %s): %v\n", p, err)
			} else if n > 0 {
				fmt.Fprintf(stderr, "migrated %d credential(s) from %s into the encrypted secret store\n", n, p)
			}
		}
		if _, err := secret.Default().LoadIntoEnv(); err != nil {
			fmt.Fprintf(stderr, "warning: secret store load failed: %v\n", err)
		}
		warnDegradedSecretStore(stderr, secret.Default())
		builtin.SetEmailAccounts(cfg.Cowork.EmailAccounts)
		for _, t := range builtin.EmailTools() {
			reg.Add(t)
			reg.Hide(t.Name())
		}
		// Calendar tools. The store is injected by the desktop app
		// (calendar_app.go) via builtin.SetCalendarStore; boot registers the tool surface.
		for _, t := range builtin.CalendarTools() {
			reg.Add(t)
			reg.Hide(t.Name())
		}
		// IM push tool (im_send). The bot gateway is injected by the desktop app
		// (bot_gateway_app.go) via builtin.SetIMPusher; boot registers the tool
		// surface. Under the CLI/TUI or when the bot is off, the tool reports
		// offline — the agent then explains instead of reaching for a browser.
		//
		// Deliberately NOT hidden: unlike browser/calendar tools (heavy, driven via
		// run_skill subagents), im_send is a lightweight ~163-token tool users
		// invoke directly in both profiles ("给飞书发条消息"). Keeping it visible in
		// the main-loop schema means the model reaches for it instead of opening a
		// browser to log into web IM.
		for _, t := range builtin.IMTools() {
			reg.Add(t)
		}
		// RAG knowledge-base tools. The store is injected by the desktop app
		// (app.go) via builtin.SetRAGStore; boot registers the tool surface.
		// Gated by the rag_enabled master switch: when the user disabled the
		// knowledge base, the tools are not registered at all, so the agent
		// cannot call rag_search/import/... even proactively. Computed once and
		// reused by RAGContextFn below so auto-injection stays consistent.
		ragEnabled := cfg.Cowork.RAGEnabledOrDefault()
		if ragEnabled {
			for _, t := range builtin.RAGTools() {
				reg.Add(t)
				reg.Hide(t.Name())
			}
		}
		// VLM backend for screen_perceive (desktop automation).
		//
		// DEFAULT is now the provider multimodal channel, NOT
		// the legacy multimodal endpoint. The dedicated endpoint
		// intermittently returns HTTP 500 ("系统异常,请稍后重试") under load, which
		// blinds the CUA mid-task and sends it into a spiral of improvised
		// PowerShell/Python workarounds. The provider multimodal path is reached
		// via the standard /chat/completions endpoint with image_url parts — the
		// same stable path the screenshot hotkey uses — and was verified by the
		// tests/cua_vlm_test.go probe to localize labeled targets reliably.
		//
		// Resolve the vision model for CallVLM (screen_perceive). The image is
		// sent as a base64 data URL via the standard OpenAI multimodal chat
		// format (image_url content parts) to the user-configured vision model —
		// no degradation chain, no fallback. When no vision model is configured,
		// CallVLM returns a clear error guiding the user to Settings.
		vlmModel := strings.TrimSpace(cfg.Cowork.VLMModel)
		if vlmModel == "" {
			// Fall back to the screenshot-recognition model so the two
			// vision uses share one configured model. Both are empty by
			// default — the user must pick a vision-capable model in Settings.
			vlmModel = cfg.Cowork.ScreenshotVLMModel
		}
		builtin.SetVLMModel(vlmModel)
		// Voice input (mic button + audio attachment understanding): resolve
		// [cowork] voice_model. CallSTT reuses the same provider chat runner as
		// VLM (SetProviderChatRunner below), sending audio as an input_audio
		// content part — one unified interface for every audio-capable model.
		// When no voice model is configured, CallSTT returns a clear error and
		// the desktop mic button is disabled with a hint.
		builtin.SetVoiceModel(strings.TrimSpace(cfg.Cowork.VoiceModel))
		// Wire the provider-backed VLM runner so CallVLM can build a proxy-aware
		// one-shot client. Without this, callProviderVLM returns "provider VLM
		// bridge not initialized". cfg is captured by closure so a profile switch
		// (which rebuilds via a fresh Build) re-resolves models.
		builtin.SetProviderChatRunner(func(ctx context.Context, modelRef string, msgs []provider.Message) ([]provider.Message, error) {
			return runProviderVLMChat(ctx, cfg, modelRef, msgs)
		})
		// Browser launch options: set above in the shared cowork+netdev
		// section (the ops console reads the same globals).
		// Autonomous browsing (browser_auto): inject a runtime that launches the
		// shared browser, mirrors it to the in-app panel (if a desktop sink is
		// registered), and drives the Python browser-use sidecar. The sidecar
		// client + browser-launch are owned here; the desktop registers an
		// optional screencast sink so the panel can mirror the agent's browser.
		builtin.SetBrowserAutoRuntime(buildBrowserAutoRuntime(cfg, opts))
		// RAG embedding reranker: boot no longer force-disables it. The desktop
		// injects a Hyper-Extract embedder (HEClient) once the HE service starts;
		// headless/CLI runs leave it nil and rag_search falls back to the LLM
		// reranker below. boot.Build re-runs on profile switch — a hardcoded
		// SetRAGEmbedder(nil) here would wipe the desktop's injection each time.
		// LLM-driven semantic search (SPEC v2 §3.6): query expansion (synonyms /
		// English stems / cross-language) + rerank, reusing the user's already-
		// configured provider. Fully internalized — no Python, no embedding model,
		// no new deps. nil provider (e.g. headless run before keys are set) keeps
		// rag_search at plain FTS5; any call failure degrades the same way.
		if execProv != nil {
			builtin.SetRAGLLM(rag.NewLLMSemantic(execProv))
		}
		// Document tools (csv/json/md/txt read + write + convert). Text-based
		// formats only; binary Office handled elsewhere (ppt via WPS MCP).
		for _, t := range builtin.DocumentTools() {
			reg.Add(t)
			reg.Hide(t.Name())
		}
		// Expert-team tools. The orchestrator + store are injected by the
		// desktop app (app.go) via builtin.SetExpertOrchestrator/SetExpertStore;
		// boot registers the tool surface here. When unbound (CLI/TUI cowork),
		// the tools return a clear "offline" error.
		for _, t := range builtin.ExpertTools() {
			reg.Add(t)
			reg.Hide(t.Name())
		}
	}
	// Always construct a host, even with no plugins configured, so the controller's
	// host pointer is stable for the session and `/mcp add` can hot-add into it.
	// When the caller supplies an externally-owned host (desktop shares one per
	// workspace root across tabs), adopt it instead of allocating — and remember
	// not to Close it on controller teardown (the owner refcounts it).
	pluginHost := opts.Host
	hostOwned := pluginHost == nil
	if hostOwned {
		pluginHost = plugin.NewHost()
	}

	// Partition configured plugins by tier so eager/lazy/background can each
	// take the path that fits them. User entries default to background: the
	// session starts immediately while enabled MCP servers warm up.
	autoStartEntries := cfg.AutoStartPlugins()
	// PPT and other office-app operations are done the SAME way a human does
	// them: via the screen_* CUA tools (open the app, perceive the UI, click,
	// type) — NOT via COM automation. There are no ppt_* tools; the agent
	// drives WPS演示 like any other desktop window.
	// A profile plugin whitelist hides MCP servers not named in it. Empty list =
	// all plugins (dev default) — unless PluginAllowlist is set (netdev seal),
	// where empty means NONE: an MCP carrying write/exec tools would punch
	// through the profile's tool_scope seal because MCP tool names are outside
	// RemovePrefix's reach. We filter after AppendEnabled so built-in MCPs
	// (time, …) and session ExtraPlugins are also subject to the whitelist — a
	// coWork profile that lists only its office servers should not quietly expose
	// the dev codegraph server. Names match case-insensitively via PluginAllowedByProfile.
	if opts.Profile != nil && (len(opts.Profile.Plugins) > 0 || opts.Profile.PluginAllowlist) {
		kept := autoStartEntries[:0]
		for _, e := range autoStartEntries {
			if config.PluginAllowedByProfile(opts.Profile, e.Name) {
				kept = append(kept, e)
			}
		}
		autoStartEntries = kept
	}
	// NAMED plugin hiding (Profile.HiddenPlugins): unlike the whitelist above,
	// this removes only the servers the profile names — builtin profiles use it
	// to keep coding-domain MCPs (codegraph) out of office/netdev
	// modes without hiding the user-installed servers those modes exist for.
	if opts.Profile != nil {
		kept := autoStartEntries[:0]
		for _, e := range autoStartEntries {
			if !config.PluginHiddenByProfile(opts.Profile, e.Name) {
				kept = append(kept, e)
			}
		}
		autoStartEntries = kept
	}
	eagerEntries, lazyEntries, bgEntries := partitionByTier(autoStartEntries)

	// Auto-demote: any eager plugin that has been chronically slow (recent
	// samples repeatedly hit the blocking startup budget) drops to lazy
	// for this session. The user keeps eager intent, just doesn't pay for it
	// on a server that's been misbehaving. A notice surfaces the demotion.
	var demoteMessages []string
	budget := plugin.DefaultStartupBudget()
	kept := eagerEntries[:0]
	for _, e := range eagerEntries {
		rec := plugin.Recommend(e.Name, budget, 0)
		if rec.Demote {
			demoteMessages = append(demoteMessages, rec.Reason)
			lazyEntries = append(lazyEntries, e)
			continue
		}
		kept = append(kept, e)
	}
	eagerEntries = kept

	eagerSpecs := PluginSpecs(eagerEntries)
	lazySpecs := PluginSpecs(lazyEntries)
	bgSpecs := PluginSpecs(bgEntries)

	// CodeGraph is a built-in MCP server fetched on first use. When it resolves,
	// inject it as one more stdio plugin pinned to the project root (it is
	// cwd-aware); EnsureInit only creates .codegraph/ (fast, size-independent),
	// serve's daemon then indexes in the background, so startup never blocks even
	// on a large repo. When it is not yet installed, fetch it in the background
	// (one-time, ~45MB) if auto_install is on — startup still never blocks, the
	// tools come online next session — otherwise point the user at the explicit
	// install command. A failed init or fetch is a notice, not fatal.
	//
	// CodeGraph is fixed to background startup. Legacy tier values are ignored so
	// enabling it never blocks chat startup.
	//
	// Domain gate: this injection happens AFTER the autoStartEntries filters
	// above, so it must check them itself — otherwise the coding-domain server
	// leaks into office/netdev profiles even when they filter it by name (the
	// historical behavior the whitelist comment claimed to prevent but didn't).
	codegraphAllowed := config.PluginAllowedByProfile(opts.Profile, "codegraph") &&
		!config.PluginHiddenByProfile(opts.Profile, "codegraph")
	if cfg.Codegraph.Enabled && codegraphAllowed {
		// Honor a custom download mirror for air-gapped/intranet deployments
		// before any Resolve/Install attempt.
		codegraph.SetDownloadBase(cfg.Codegraph.DownloadURL)
		// An embedded build (portable/shipped exe) carries the runtime inside
		// the binary, so it can be laid down synchronously — a local unpack
		// with zero network, bounded below so a pathological filesystem can't
		// stall startup. That makes code intelligence live on the FIRST launch
		// instead of requiring a restart; a no-op once the cache exists.
		if codegraph.Embedded() {
			ictx, cancel := context.WithTimeout(ctx, 60*time.Second)
			if _, err := codegraph.Install(ictx, func(msg string) {
				sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: msg})
			}); err != nil {
				sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn,
					Text: "codegraph: embedded runtime install failed (" + err.Error() + ") — falling back to download/grep"})
			}
			cancel()
		}
		bin, ok := codegraph.Resolve(cfg.Codegraph.Path)
		switch {
		case ok && !codegraph.IndexableRoot(root):
			sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn,
				Text: "codegraph: project root is a filesystem root — skipped to avoid indexing the whole volume"})
		case ok:
			spec := plugin.Spec{
				Name:              "codegraph",
				StripRawPrefix:    "codegraph_",
				Command:           bin,
				Args:              []string{"serve", "--mcp"},
				Dir:               root,
				ReadOnlyToolNames: codegraph.ReadOnlyToolNames(),
				// Curate a compact surface: CodeGraph exposes 10 tools, but 3
				// cover 90% of use (context = "how does X work", search = find
				// symbol, callers = who calls this). The rest (callees/impact/
				// trace/files/explore/node/status) are available via run_skill
				// or can be unhidden by removing this whitelist. Saves ~1400
				// tokens of tool-schema in the system prompt.
				ExposeToolNames: map[string]bool{
					"codegraph_context": true,
					"codegraph_search":  true,
				},
				// The daemon walks and indexes the whole tree; below-normal
				// priority keeps it from starving the user's machine (#3797).
				LowPriority: true,
			}
			warm := codegraph.Initialized(root)
			if err := codegraph.EnsureInit(ctx, bin, root); err != nil {
				sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn,
					Text: "codegraph: init failed (" + err.Error() + ") — symbol-graph tools disabled this session"})
				break
			}
			bgNotice := func() {
				if !warm {
					sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
						Text: "codegraph: preparing code-intelligence tools in the background — tools will appear when ready"})
				}
			}
			bgSpecs = append(bgSpecs, spec)
			bgNotice()
		case cfg.Codegraph.AutoInstall:
			notify := func(msg string) { sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: msg}) }
			notify("codegraph: fetching code-intelligence runtime in the background (one-time) — symbol-graph tools available next session")
			codegraphClient, err := netclient.NewHTTPClient(proxySpec, netclient.TransportOptions{})
			if err != nil {
				notify("codegraph: install skipped (" + err.Error() + ")")
			} else {
				go func() {
					if _, err := codegraph.InstallWithClient(context.WithoutCancel(ctx), codegraphClient, nil); err != nil {
						notify("codegraph: install failed (" + err.Error() + ") — using grep/glob; retries next session. " +
							"If your network blocks GitHub, set [codegraph] download_url to a reachable mirror " +
							"or place the runtime in the codegraph cache manually.")
					} else {
						notify("codegraph: installed — symbol-graph tools available next session")
					}
				}()
			}
		default:
			sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
				Text: "codegraph: not installed — run `fairpeer codegraph install` to enable symbol-graph tools"})
		}
	}

	// Runtime: if uv is not found, auto-download it in the background so the
	// first Python-dependent feature (PPT, doc-convert, MCP, HE) doesn't fail.
	// This is non-blocking — features that need Python will fall back to PATH
	// lookup or error with a clear message until uv is ready.
	if _, ok := runtimepkg.ResolveUV(); !ok {
		go func() {
			bgCtx := context.Background()
			if err := runtimepkg.Install(bgCtx); err != nil {
				slog.Debug("runtime: uv auto-download failed (non-fatal)", "error", err)
			} else {
				slog.Info("runtime: uv downloaded successfully — Python features will use it")
			}
		}()
	}

	// ExtraPlugins (host-supplied session servers) are appended AFTER the
	// entry filters above, so re-apply the profile gates here — otherwise a
	// netdev allowlist could be bypassed by exactly the path meant to be
	// covered by it.
	for _, spec := range opts.ExtraPlugins {
		if opts.Profile != nil &&
			(!config.PluginAllowedByProfile(opts.Profile, spec.Name) || config.PluginHiddenByProfile(opts.Profile, spec.Name)) {
			continue
		}
		eagerSpecs = append(eagerSpecs, spec)
	}

	// Deduplicate specs across all tiers by name. This prevents duplicated startup
	// attempts when a built-in spec (like codegraph) overlaps with an entry loaded
	// from mcp.json or legacy claude_desktop_config.json.
	seenSpec := make(map[string]bool)
	dedupSpecs := func(specs []plugin.Spec) []plugin.Spec {
		var out []plugin.Spec
		for _, s := range specs {
			if seenSpec[s.Name] {
				continue
			}
			seenSpec[s.Name] = true
			out = append(out, s)
		}
		return out
	}
	eagerSpecs = dedupSpecs(eagerSpecs)
	lazySpecs = dedupSpecs(lazySpecs)
	bgSpecs = dedupSpecs(bgSpecs)

	// Apply caller-supplied stderr override to every spec across tiers.
	if opts.Stderr != nil {
		for i := range eagerSpecs {
			eagerSpecs[i].Stderr = opts.Stderr
		}
		for i := range lazySpecs {
			lazySpecs[i].Stderr = opts.Stderr
		}
		for i := range bgSpecs {
			bgSpecs[i].Stderr = opts.Stderr
		}
	}

	// Eager: block until handshake. Failures show up in /mcp.
	if len(eagerSpecs) > 0 {
		if hostOwned {
			// Legacy per-session path: StartAvailable allocates a fresh host and
			// hands its handshaked plugins back to this controller.
			host, ptools := plugin.StartAvailable(ctx, eagerSpecs)
			pluginHost = host
			for _, t := range ptools {
				reg.Add(t)
			}
			go host.StartPhaseB(ctx, sink)
			if text, ok := MCPStartupNotice(host.Failures()); ok {
				sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: text})
			}
		} else {
			// Shared-host path (desktop): add eager specs into the externally-owned
			// host so every tab sharing this workspace root shares one set of MCP
			// subprocesses. Concurrency mirrors StartAvailable's fan-out.
			ptools := plugin.StartAvailableInto(ctx, pluginHost, eagerSpecs)
			for _, t := range ptools {
				reg.Add(t)
			}
			go pluginHost.StartPhaseB(ctx, sink)
			if text, ok := MCPStartupNotice(pluginHost.Failures()); ok {
				sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: text})
			}
		}
	}

	// Lazy / background: register placeholder tools now; the real spawn waits
	// for either the first model call (lazy) or a goroutine kicked off here
	// (background). Both share the same pluginHost so /mcp status, hot-add,
	// and Close see one cohesive set of servers regardless of tier.
	registerDeferred := func(specs []plugin.Spec, kick bool) {
		for _, s := range specs {
			cs, _ := plugin.LoadCachedSchema(s.Name, plugin.SpecFingerprint(s))
			for _, t := range plugin.LazyToolset(s, cs, pluginHost, reg, ctx, kick) {
				reg.Add(t)
			}
		}
	}
	registerDeferred(lazySpecs, false)
	registerDeferred(bgSpecs, true)

	// Inject codegraph steering into the system prompt when symbol-graph tools
	// are available, so the model knows to prefer them for architecture / call-graph
	// questions over grep/read_file. Also register codegraph tool names in the
	// subagent allowed-tools list so explore/research/review can use them.
	if cfg.Codegraph.Enabled {
		prefix := plugin.ToolPrefix("codegraph")
		var cgTools []string
		for _, name := range reg.Names() {
			if strings.HasPrefix(name, prefix) {
				cgTools = append(cgTools, name)
			}
		}
		if len(cgTools) > 0 {
			sysPrompt += "\n\n" + codegraph.SteerText
			skill.SetExtraReadTools(cgTools)
		}
	}

	// Profile HiddenTools: hide tools the current profile doesn't need from the
	// main loop's schemas. Hidden tools stay callable by subagents (FilterRegistry
	// uses Get/Names, not Schemas), so office skills can still access coding tools
	// if needed — they're just not visible to the model in the main loop, saving
	// tool-schema tokens for irrelevant tools.
	if opts.Profile != nil {
		for _, name := range opts.Profile.HiddenTools {
			reg.Hide(name)
		}
	}

	// Profile ToolScope: the HARD seal. Unlike HiddenTools above, this removes
	// tools from the Registry entirely, so skill subagents (which share this
	// Registry through FilterRegistry) lose them too. This is what makes the
	// netdev diagnostic hand structurally read-only: a prompt-injected model
	// has no write path through any tool, in any subagent (NETDEV_SPEC §7.1).
	if opts.Profile.SealsExecutionTools() {
		for _, prefix := range netdevExcludedToolPrefixes {
			reg.RemovePrefix(prefix)
		}
	}

	for _, msg := range demoteMessages {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: msg})
	}

	// Close the plugin host only when Build owns it. A shared host (desktop's
	// per-workspace-root host) is refcounted by its owner; controller teardown
	// releases the owner's reference instead of tearing down subprocesses other
	// tabs still depend on.
	var cleanup func()
	if hostOwned {
		cleanup = pluginHost.Close
	}

	// LSP tools resolve their servers on PATH and spawn lazily on first query, so
	// registering them is cheap even when no server is installed (a query then
	// returns an install hint). The manager is session-scoped; chain its shutdown
	// into the controller's cleanup so servers stop with the session, not the turn.
	if cfg.LSP.Enabled {
		lspMgr := lsp.NewManager(root, LSPSpecs(cfg.LSP))
		for _, t := range lsp.Tools(lspMgr) {
			reg.Add(t)
		}
		// Wire edit_file/write_file post-edit diagnostics: after a successful
		// write, run the file's LSP diagnostics and append them to the tool
		// result. This closes the edit→diagnose→fix loop without the model
		// needing a separate lsp_diagnostics call. Errors are swallowed (no
		// server for this language, timeout, etc.) so a missing server never
		// blocks the edit.
		builtin.SetPostEditHook(func(ctx context.Context, path string) string {
			out, err := lspMgr.Diagnostics(ctx, path)
			if err != nil || strings.TrimSpace(out) == "" {
				return ""
			}
			return "LSP diagnostics for " + path + ":\n" + out
		})
		prev := cleanup
		cleanup = func() {
			if prev != nil {
				prev()
			}
			lspMgr.Close()
		}
	}

	maxSteps := cfg.Agent.MaxSteps
	if opts.MaxSteps > 0 {
		maxSteps = opts.MaxSteps
	}
	// Subagent transcripts follow the session partition (opts.SessionDir —
	// already profile-routed by the desktop), so netdev/cowork subagent runs
	// don't land in the shared default bucket. Callers without a session dir
	// (headless run) fall back to the default partition.
	subagentSessionDir := opts.SessionDir
	if strings.TrimSpace(subagentSessionDir) == "" {
		subagentSessionDir = config.SessionDir()
	}
	subagentStore := newSubagentStore(subagentSessionDir)

	// Permission policy gates every tool call. The headless gate (no Approver)
	// resolves "ask" to allow — preserving `fairpeer run` autonomy — while deny
	// rules hard-block in every mode. Interactive frontends (chat, desktop) swap
	// in an interactive gate later via Controller.EnableInteractiveApproval.
	// Sub-agents always run headless: they have no UI to answer a prompt, so they
	// inherit this same gate.
	policy := permission.New(cfg.Permissions.Mode, cfg.Permissions.Allow, cfg.Permissions.Ask, cfg.Permissions.Deny)
	// [netdev.guardrails].confirm_each_command: install Ask rules for the
	// netdev tool family so EVERY netdev_exec / netdev_netconf call pops an
	// approval card before it runs. Ask rules outrank both the readOnly
	// fallback and YOLO/full-access mode (permission.DecideSubject checks Ask
	// before Allow), so this guardrail survives "skip all approvals" — by
	// design: the network is the one place every command stays a control
	// point. Netdev tools only register in the netdev profile, so the rules
	// are inert elsewhere.
	if cfg.NetDev.Guardrails.ConfirmEachCommand {
		policy.Ask = append(policy.Ask,
			permission.Rule{Tool: "netdev_exec"},
			permission.Rule{Tool: "netdev_netconf"},
		)
	}
	riskOverrides := buildRiskOverrides(cfg.Plugins)
	// Sub-agent gate (upgrade spec 3-10): same policy as the main gate but
	// headless — the sub-agent runs unattended inside the main agent's turn.
	// Ask-level writers pass through to preserve autonomy (the main agent
	// already approved the task that spawned this sub-agent); external-risk
	// operations are denied because there is no human to confirm. When a
	// bridge approver is installed (interactive desktop), Ask-level calls
	// surface to the user through the main agent's approval card instead of
	// silently passing — closing the gap where a doc_write ran unapproved
	// inside a sub-agent while the same call in the main loop would prompt.
	headlessGate := permission.NewGate(policy, subagentApprover)
	headlessGate.RiskOverrides = riskOverrides

	// Hooks: load the global settings.json plus the project's (only when trusted —
	// project hooks run arbitrary shell commands, so cloning a repo must not
	// silently execute them). Non-blocking hook output is surfaced to the user as
	// a Notice through the shared sink. The runner fires PreToolUse/PostToolUse in
	// the agent loop and UserPromptSubmit/Stop at the controller's turn boundary.
	hooksTrusted := hook.IsTrusted(root, "")
	hookRunner := hook.NewRunner(
		hook.Load(hook.LoadOptions{ProjectRoot: root, Trusted: hooksTrusted}),
		root, nil,
		func(msg string) { sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: msg}) },
	)
	if hook.ProjectDefinesHooks(root) && !hooksTrusted {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
			Text: "this project defines hooks but they are not trusted — run /hooks trust to enable them"})
	}

	// Cold-start: fire the one-shot Startup hook now that hooks are loaded.
	// Unlike SessionStart (which is lazy — it waits for the first user turn),
	// Startup runs before any session is active, for one-time boot setup
	// (logging, workspace prep, notifications). boot.Build runs once per
	// process, so this fires exactly once.
	hookRunner.Startup(ctx)

	// The `task` tool spawns sub-agents that reuse the parent's provider and
	// tool registry. Wired here after the built-ins / plugins are loaded so
	// sub-agents inherit the full tool set (minus `task` itself, to keep
	// nesting out of the picture). It registers into the same reg the
	// executor uses, so the model surfaces it like any other tool.
	resolveSubagentProvider := func(modelRef, effort string) (provider.Provider, *provider.Pricing, int, error) {
		me := *entry
		if strings.TrimSpace(modelRef) != "" {
			resolved, ok := cfg.ResolveModel(modelRef)
			if !ok {
				return nil, nil, 0, fmt.Errorf("unknown model %q", modelRef)
			}
			me = *resolved
		}
		if strings.TrimSpace(effort) != "" {
			normalized, err := config.NormalizeEffort(&me, effort)
			if err != nil {
				return nil, nil, 0, err
			}
			me.Effort = normalized
			if me.Kind == "anthropic" && strings.TrimSpace(me.Effort) != "" && strings.TrimSpace(me.Thinking) == "" {
				me.Thinking = "adaptive"
			}
		}
		p, err := NewProviderWithProxy(&me, proxySpec, false)
		if err != nil {
			return nil, nil, 0, err
		}
		return p, me.Price, me.ContextWindow, nil
	}
	subagentIdentity := func(modelRef, effort string) (string, string) {
		return subagentEffectiveIdentity(cfg, modelName, entry, modelRef, effort)
	}
	taskModel := firstNonEmpty(cfg.Agent.SubagentModels["task"], cfg.Agent.SubagentModel)
	taskEffort := firstNonEmpty(cfg.Agent.SubagentEfforts["task"], cfg.Agent.SubagentEffort)
	// Sub-agent writer pre-edits feed the controller's checkpoint store through
	// this shared hook — boot creates it before the controller exists, both
	// spawn sites (task tool, run_skill runner) fire through it, and the
	// concrete snapshotter is installed after control.New (below). The post-edit
	// twin carries the post-edit hash recorder for the rewind safety
	// classification.
	subagentPreEdit := agent.NewSharedPreEditHook()
	subagentPostEdit := agent.NewSharedPostEditHook()
	taskTool := agent.NewTaskTool(execProv, entry.Price, reg, maxSteps,
		entry.ContextWindow, cfg.Agent.SoftCompactRatio, cfg.Agent.CompactRatio, cfg.Agent.CompactForceRatio,
		cfg.Agent.Temperature, config.ArchiveDir(), "", headlessGate,
		taskModel, taskEffort, resolveSubagentProvider).
		WithTranscripts(subagentStore, root, modelName, entry.Effort).
		WithTranscriptIdentityResolver(subagentIdentity).
		WithPreEditHook(subagentPreEdit.Fire).
		WithPostEditHook(subagentPostEdit.Fire)
	reg.Add(taskTool)

	// The `remember` tool lets the model persist durable facts to the project's
	// auto-memory store; `forget` prunes ones that turn out wrong. Saved facts no
	// longer load into the per-turn prefix (the portrait layer is the only thing
	// injected) — the model reaches saved facts on demand, so the index stays out
	// of every turn.
	reg.Add(memory.NewRememberTool(mem.Store))
	reg.Add(memory.NewForgetTool(mem.Store))
	reg.Add(memory.NewRecallTool(mem.Store))

	// Project knowledge hub tools (kb_sync / kb_map / kb_search / kb_history /
	// kb_rollback). The hub itself is injected by the desktop app via
	// builtin.SetKnowledgeHub; boot registers the surface so a headless run
	// still resolves the names (they report "not available" when unbound).
	// Unlike the rag tools these are NOT hidden: surveying a long-running
	// project is a first-class agent job, so the model is meant to reach for
	// them directly.
	for _, t := range builtin.KnowledgeTools() {
		reg.Add(t)
	}

	// The `ask` tool puts structured multiple-choice questions to the user. It
	// reaches them through the Asker on the call context, which interactive
	// frontends wire to the controller (EnableInteractiveApproval); a headless run
	// has none, so ask resolves to "decide for yourself".
	reg.Add(agent.NewAskTool())

	// Skill tools: run_skill / install_source plus the dedicated
	// subagent wrappers (explore / research). review and security-review are
	// available as run_skill targets. A subagent skill reuses the sub-agent
	// machinery via this runner — an isolated loop with the skill body
	// as system prompt, a tool set scoped to the skill's allowed-tools (minus the
	// task/skill meta-tools, to bar recursion), and an optional per-skill model.
	// Its tool activity nests under the invoking call, like `task`.
	skillRunner := func(sctx context.Context, sk skill.Skill, task string, runOpts skill.SubagentRunOptions) (string, error) {
		prov, price, ctxWin := execProv, entry.Price, entry.ContextWindow
		modelRef := subagentModelRef(cfg, sk)
		effortRef := subagentEffortRef(cfg, sk)
		if modelRef != "" || effortRef != "" {
			p, pr, cw, err := resolveSubagentProvider(modelRef, effortRef)
			if err != nil {
				return "", fmt.Errorf("subagent skill %q profile: %w", sk.Name, err)
			}
			prov, price, ctxWin = p, pr, cw
		}
		subReg := agent.FilterRegistry(reg, sk.AllowedTools, agent.SubagentMetaTools()...)
		continueFrom, forkFrom := strings.TrimSpace(runOpts.ContinueFrom), strings.TrimSpace(runOpts.ForkFrom)
		if continueFrom != "" && forkFrom != "" {
			return "", fmt.Errorf("continue_from and fork_from are mutually exclusive")
		}
		parentID, _, _, _ := agent.CallContext(sctx)
		parentSession := agent.ParentSession(sctx)
		var run *agent.SubagentRun
		if subagentStore == nil || parentSession == "" {
			// Headless runs (e.g. `fairpeer run`) have no persistent session to
			// own a transcript. Run the skill sub-agent ephemerally, as before
			// persisted transcripts existed, instead of failing. Continuation and
			// fork need a persisted owner, so they error here.
			if continueFrom != "" || forkFrom != "" {
				return "", fmt.Errorf("continue_from/fork_from require a persisted session; none is active in this run")
			}
			run = agent.EphemeralSubagentRun(sk.Body)
		} else {
			identityModel, identityEffort := subagentIdentity(modelRef, effortRef)
			spec := agent.SubagentSpec{
				Kind:             "skill",
				Name:             sk.Name,
				WorkspaceRoot:    root,
				ParentSession:    parentSession,
				ParentToolCallID: parentID,
				SystemPrompt:     sk.Body,
				Registry:         subReg,
				Model:            identityModel,
				Effort:           identityEffort,
			}
			var prepErr error
			switch {
			case continueFrom != "":
				run, prepErr = subagentStore.PrepareContinue(continueFrom, spec)
			case forkFrom != "":
				run, prepErr = subagentStore.PrepareFork(forkFrom, spec)
			default:
				run, prepErr = subagentStore.PrepareFresh(spec)
			}
			if prepErr != nil {
				return "", prepErr
			}
		}
		defer run.Release()
		// Step budget: a skill may declare its own cap (frontmatter max-steps:) for
		// heavy subagents (ppt-auto); otherwise default to half the main loop's
		// MaxSteps (floor 5), as before.
		steps := maxSteps
		if steps > 0 {
			if steps /= 2; steps < 5 {
				steps = 5
			}
		}
		if sk.MaxSteps > 0 {
			steps = sk.MaxSteps
		}
		answer, err := agent.RunSubAgentWithSession(sctx, prov, subReg, run.Session, task, agent.Options{
			MaxSteps:      steps,
			Temperature:   cfg.Agent.Temperature,
			Pricing:       price,
			Gate:          headlessGate,
			ContextWindow: ctxWin,
			ArchiveDir:    config.ArchiveDir(),
			PreEditHook:   subagentPreEdit.Fire,
			PostEditHook:  subagentPostEdit.Fire,
		}, agent.NestedSink(sctx, event.Discard))
		if err != nil {
			return "", errors.Join(err, subagentStore.SaveFailed(run))
		}
		if err := subagentStore.SaveCompleted(run); err != nil {
			return "", errors.Join(err, subagentStore.SaveFailed(run))
		}
		return agent.FormatSubagentResult(answer, run.Ref, false), nil
	}
	skillProfile := func(sk skill.Skill) *event.Profile {
		model, effort := subagentModelRef(cfg, sk), subagentEffortRef(cfg, sk)
		if model == "" && effort == "" {
			return nil
		}
		return &event.Profile{Model: model, Effort: effort}
	}
	reg.Add(skill.NewRunSkillToolWithIndex(skillStore, allSkillStore, skillRunner, skillProfile))
	reg.Add(installsource.NewTool(installsource.Options{
		ProjectRoot: root,
		HTTPClient:  httpClient,
		ConnectMCP: func(e config.PluginEntry) (installsource.MCPConnectResult, error) {
			exp := e.ExpandedPlugin()
			spec := plugin.Spec{
				Name:    exp.Name,
				Type:    exp.Type,
				Command: exp.Command,
				Args:    exp.Args,
				Env:     exp.Env,
				URL:     exp.URL,
				Headers: exp.Headers,
			}
			if opts.Stderr != nil {
				spec.Stderr = opts.Stderr
			}
			tools, err := pluginHost.Add(ctx, spec)
			if err != nil {
				return installsource.MCPConnectResult{}, err
			}
			reg.RemovePrefix(plugin.ToolPrefix(spec.Name))
			for _, t := range tools {
				reg.Add(t)
			}
			// Disconnect closes the server and drops its namespaced tools.
			// Used by the install_source rollback path when SaveTo fails.
			disconnect := func() {
				if prefix, ok := pluginHost.Remove(spec.Name); ok {
					reg.RemovePrefix(prefix)
				}
			}
			return installsource.MCPConnectResult{
				ToolCount:  len(tools),
				Disconnect: disconnect,
			}, nil
		},
		OnDisconnect: func(serverName string) bool {
			if prefix, ok := pluginHost.Remove(serverName); ok {
				reg.RemovePrefix(prefix)
				return true
			}
			return false
		},
	}))
	// skill_market: browse/search the skill marketplace and install from it
	// (SPEC v2 §3.4C). Shares the same SSRF-guarded httpClient as install_source.
	reg.Add(installsource.NewMarketToolFromOptions(installsource.Options{
		ProjectRoot: root,
		HTTPClient:  httpClient,
	}))
	for _, t := range skill.BuiltinSubagentTools(skillStore, skillRunner, skillProfile) {
		reg.Add(t)
	}

	// Register the post-distill skill-retirement hook. Two tiers of decay:
	//   soft (every Build) — skills past SkillColdDays are tagged [休眠] and
	//     demoted to the dormant tail of the index (still callable; a call wakes
	//     them by refreshing their last-used time).
	//   hard (after Distill) — skills past 2× the threshold are persisted to
	//     disabled_skills so they drop out of the registry entirely on the next
	//     Build (the model can no longer call them, but the file is kept and the
	//     user can re-enable via Settings → Skills). This mirrors memory's
	//     dormant→archive progression.
	// The hook closes over allSkillStore (for the usage tracker + known names)
	// and config.UserConfigPath (for persistence). Best-effort throughout.
	if cfg.Dream.Enabled {
		captureStore := allSkillStore
		captureColdDays := cfg.Dream.SkillColdDaysEffective()
		agent.RegisterDistillComplete(func() string {
			return retireColdSkills(captureStore, captureColdDays, config.UserConfigPath())
		})
	} else {
		agent.RegisterDistillComplete(nil) // dream disabled → no retirement
	}

	// Dream/distill provider: the background memory agents run on a cheaper
	// model so a periodic maintenance pass costs almost nothing. Resolution
	// order is [memory] provider/model (the dedicated memory channel the
	// settings panel exposes), then agent.fast_task_model (the older knob
	// [memory] falls back to), then the session's own provider — so an
	// unconfigured setup behaves exactly as before this section existed.
	dreamProv := execProv
	if ref := cfg.MemoryMaintenanceRef(); ref != "" {
		if fe, ok := cfg.ResolveModel(ref); ok {
			// [memory] effort rides the same per-provider plumbing as
			// [[providers]].effort — a cheap model usually wants a cheap effort.
			if eff := cfg.MemoryEffort(); eff != "" {
				fe.Effort = eff
			}
			if dp, err := NewProviderWithProxy(fe, proxySpec, false); err == nil {
				dreamProv = dp
			} else {
				fmt.Fprintf(stderr, "warning: memory maintenance model %q not built, dream falls back to main model: %v\n", ref, err)
			}
		} else {
			fmt.Fprintf(stderr, "warning: memory maintenance model %q unknown, dream falls back to main model\n", ref)
		}
	}

	execSess := agent.NewSession(sysPrompt)
	executor := agent.New(execProv, reg, execSess, agent.Options{
		MaxSteps:             maxSteps,
		Temperature:          cfg.Agent.Temperature,
		Pricing:              entry.Price,
		Gate:                 headlessGate,
		Hooks:                hookRunner,
		Jobs:                 jm,
		ProjectChecks:        projectChecks,
		ContextWindow:        entry.ContextWindow,
		SoftCompactRatio:     cfg.Agent.SoftCompactRatio,
		CompactRatio:         cfg.Agent.CompactRatio,
		CompactForceRatio:    cfg.Agent.CompactForceRatio,
		ContextBudgetPercent: cfg.Agent.ContextBudgetPercent,
		ArchiveDir:           config.ArchiveDir(),
		// The interactive loop (CLI/TUI/desktop/serve/bot all build through
		// here) keeps the visible-final guarantee: a turn that ends with no
		// readable answer is re-prompted instead of silently landing as an
		// empty bubble. Upstream Reasonix relaxes this for its interactive
		// harness; fairpeer deliberately stays stricter.
		RequireVisibleFinal: true,
	}, sink)

	// Custom slash commands (.fairpeer/commands + user dir). Best-effort: a malformed
	// file is skipped, and a load error never blocks the session.
	cmds, _ := command.Load(config.CommandDirsForRoot(root)...)

	// Expose the loaded slash commands (skills + custom commands) to the model via
	// the slash_command tool, so it can invoke a project playbook by name the way a
	// user types "/name". Skills are added first, then commands, so a command wins
	// a name clash — matching the prompt's command-over-skill precedence.
	var slashEntries []command.SlashEntry
	for _, sk := range skills {
		sk := sk
		slashEntries = append(slashEntries, command.SlashEntry{
			Name:        sk.Name,
			Description: sk.Description,
			Render:      func(args []string) string { return skill.Render(sk, strings.Join(args, " ")) },
		})
	}
	for _, cmd := range cmds {
		cmd := cmd
		slashEntries = append(slashEntries, command.SlashEntry{
			Name:        cmd.Name,
			Description: cmd.Description,
			ArgHint:     cmd.ArgHint,
			Render:      func(args []string) string { return cmd.Render(args) },
		})
	}
	reg.Add(command.NewSlashCommandTool(slashEntries))

	var runner agent.Runner = executor
	label := entry.Model
	var classifier *control.ProviderAutoPlanClassifier

	if control.NormalizeAutoPlan(cfg.Agent.AutoPlan) == control.AutoPlanOn && cfg.Agent.AutoPlanClassifier != "" {
		cm := cfg.Agent.AutoPlanClassifier
		ce, ok := cfg.ResolveModel(cm)
		if !ok {
			return nil, fmt.Errorf("auto_plan_classifier %q is not a configured provider", cm)
		}
		classifierProv, err := NewProviderWithProxy(ce, proxySpec, false)
		if err != nil {
			return nil, fmt.Errorf("auto_plan_classifier %q: %w", cm, err)
		}
		classifier = control.NewProviderAutoPlanClassifier(classifierProv)
	}

	sessionDir := opts.SessionDir
	if sessionDir == "" {
		// Derive from the resolved profile so CLI/serve --profile cowork lands its
		// transcript in the cowork partition instead of the dev default. Desktop
		// always passes opts.SessionDir explicitly, so this only affects callers
		// that omit it.
		sessionDir = config.SessionDirFor(profileName(opts.Profile))
	}

	ctrlOpts := control.Options{
		Runner:        runner,
		Executor:      executor,
		DreamProvider: dreamProv,
		Sink:          sink,
		Policy:        policy,
		Label:         label,
		SystemPrompt:  sysPrompt,
		SessionDir:    sessionDir,
		Host:          pluginHost,
		Commands:      cmds,
		Skills:        skills,
		AllSkills:     allSkills,
		SkillStore:    skillStore,
		AllSkillStore: allSkillStore,
		Hooks:         hookRunner,
		Memory:        mem,
		// Chain secret-env teardown into the controller's cleanup (audit A9:
		// bound the plaintext secret's lifetime in os.Environ()). In practice
		// this now no-ops: the config boot layer injects the store UNTRACKED
		// (see config.loadDotEnvForRoot) because provider keys must outlive any
		// single controller — the desktop scheduler, RAG extraction, and
		// settings indicators read them outside controller lifetimes, and
		// rebuild() closes the old controller after the new one is already
		// running. The unload stays wired for a future per-child cmd.Env
		// isolation architecture, where tracked injection becomes meaningful
		// again.
		Cleanup: func() {
			if cleanup != nil {
				cleanup()
			}
			secret.Default().UnloadFromEnv()
		},
		Jobs:          jm,
		Registry:      reg,
		PluginCtx:     ctx,
		WorkspaceRoot: root,
		Present:       opts.Present,
		AutoPlan:      cfg.Agent.AutoPlan,
		OnRemember: func(rule string) control.RememberResult {
			return rememberPermissionRule(root, rule)
		},
		// GoalJudge: wire the independent cold-read judge so goal-completion
		// claims ([goal:complete]) are verified against transcript evidence
		// instead of trusted on the model's say-so. Uses the main provider at
		// temperature 0; the controller caps the call at 60s and cancels on
		// turn Cancel. Disable by clearing the field after Build if needed.
		GoalJudge: func(ctx context.Context, prov provider.Provider, transcript []provider.Message, condition string) agent.GoalVerdict {
			return agent.GoalJudgeWithRetry(ctx, prov, transcript, condition, 0)
		},
		// RAGContextFn: auto-retrieve knowledge-base context for a user message
		// and inject it as a preamble. collection scopes the search to one
		// knowledge-base collection (the user's Composer dropdown selection);
		// when it's "" the controller skips the call entirely (opt-out). The
		// returned snippets may carry prompt-injection text from imported docs,
		// so they're wrapped so the model treats them as DATA, never commands.
		RAGContextFn: func(ctx context.Context, query, collection string) string {
			// Master switch: when the user disabled the knowledge base
			// ([cowork] rag_enabled = false), never inject — even if a
			// collection is selected. Read from the live config so toggling it
			// in settings (which triggers a rebuild) takes effect immediately.
			if !cfg.Cowork.RAGEnabledOrDefault() {
				return ""
			}
			if collection == "" {
				return "" // user opted out ("不使用")
			}
			return builtin.WrapUntrusted("rag", builtin.AutoSearch(ctx, query, collection))
		},
	}
	if classifier != nil {
		ctrlOpts.Classifier = classifier
	}
	ctrl := control.New(ctrlOpts)
	// Publish the controller to the background-job completion hook installed
	// above, then hand it the coordinator so finished results can be pushed into
	// this session (as their own turn or as next-turn context, per policy).
	ctrlForJobs.Store(ctrl)
	if deferredCoord != nil {
		ctrl.SetDeferred(deferredCoord)
	}
	// Route sub-agent writer pre-edits (task / run_skill) into the controller's
	// checkpoint store, so skill-driven file writes rewind like main-loop edits.
	subagentPreEdit.Set(ctrl.PreEditSnapshotter())
	subagentPostEdit.Set(ctrl.PostEditHasher())
	// Inject the per-tool risk-class overrides (SPEC v2 §3.2A) so the interactive
	// gate honors [[plugins]] risk config the same way the headless gate does.
	ctrl.SetRiskOverrides(riskOverrides)
	// Everything above is wired: retire the previous build's background services
	// and publish the new ones (see the swap helpers). Disabling a feature swaps
	// in nil, which is what stops the old ticker.
	built = true
	swapPatrolManager(patrolMgr)
	swapDeferredCoordinator(deferredCoord)
	return ctrl, nil
}

// --- process-lifetime background services -----------------------------------

// patrolMgrForApp and deferredCoordForApp hold the services boot builds. A
// settings change re-runs Build, so each new instance must stop the one it
// replaces: without the swap, every rebuild would leak a ticker, and a patrol
// the user just switched off would keep ticking forever.
var (
	patrolMgrForApp     atomic.Pointer[patrol.Manager]
	deferredCoordForApp atomic.Pointer[deferred.Coordinator]
)

// PatrolManager returns the live patrol manager, or nil when [patrol] is off.
// The desktop uses it for the "check now" action.
func PatrolManager() *patrol.Manager { return patrolMgrForApp.Load() }

// The retired services are closed on their own goroutines: Close waits for the
// in-flight tick, which may be sitting in a git probe, and a settings save must
// not block on that.
func swapPatrolManager(next *patrol.Manager) {
	if old := patrolMgrForApp.Swap(next); old != nil {
		go old.Close()
	}
}

func swapDeferredCoordinator(next *deferred.Coordinator) {
	if old := deferredCoordForApp.Swap(next); old != nil {
		go old.Close()
	}
}

// deferredJobTitle is the headline for a finished background job pushed into
// the session: what ran, and how it ended.
func deferredJobTitle(c jobs.Completion) string {
	name := fmt.Sprintf("background %s %s", c.Kind, c.ID)
	if c.Label != "" {
		name += fmt.Sprintf(" (%s)", c.Label)
	}
	switch c.Status {
	case jobs.Failed:
		if c.Err != nil {
			return fmt.Sprintf("%s failed: %v", name, c.Err)
		}
		return name + " failed"
	case jobs.Killed, jobs.Interrupted:
		return name + " was cancelled"
	default:
		return name + " finished"
	}
}

// deferredJobStatus maps a job's terminal status onto the deferred-task
// lifecycle. A job that stopped without finishing (killed, interrupted by a
// closing session) is an abort, not a failure.
func deferredJobStatus(st jobs.Status) deferred.Status {
	switch st {
	case jobs.Done:
		return deferred.Resolved
	case jobs.Failed:
		return deferred.Failed
	case jobs.Killed, jobs.Interrupted:
		return deferred.Aborted
	default:
		return deferred.Pending
	}
}

// patrolInspectors maps [patrol] checks onto inspectors. An empty list means
// every inspector. A list naming only checks we do not have falls back to the
// full set rather than silently patrolling nothing — a misconfiguration should
// degrade to the useful default, not to silence.
func patrolInspectors(checks []string) []patrol.Inspector {
	if len(checks) == 0 {
		return patrol.DefaultInspectors()
	}
	var out []patrol.Inspector
	for _, name := range checks {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "git":
			out = append(out, patrol.GitInspector{})
		case "markers", "todo", "todos", "hygiene":
			out = append(out, patrol.MarkerInspector{})
		}
	}
	if len(out) == 0 {
		return patrol.DefaultInspectors()
	}
	return out
}

func migrateLegacySessionSources(sink event.Sink) {
	dest := config.SessionDir()
	if strings.TrimSpace(dest) == "" {
		return
	}
	type legacySource struct {
		dir     string
		label   string
		migrate func(srcDir, globalDest string, projectDir func(string) string) (int, error)
	}
	var sources []legacySource
	if home, herr := os.UserHomeDir(); herr == nil {
		sources = append(sources, legacySource{
			dir:     filepath.Join(home, ".fairpeer", "sessions"),
			label:   "~/.fairpeer/sessions",
			migrate: agent.MigrateLegacySessions,
		})
	}
	// Back-fill v0.x sessions from the current user config session directory as
	// well. This covers users whose platform config root was redirected before the
	// Go rewrite; their event logs can already live where v2 stores sessions.
	sources = append(sources, legacySource{
		dir:     dest,
		label:   dest,
		migrate: agent.MigrateLegacySessionsFromConfigDir,
	})

	seen := map[string]bool{}
	for _, src := range sources {
		if strings.TrimSpace(src.dir) == "" {
			continue
		}
		key := filepath.Clean(src.dir)
		if seen[key] {
			continue
		}
		seen[key] = true
		if n, serr := src.migrate(src.dir, dest, config.ProjectSessionDir); serr == nil && n > 0 {
			sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: fmt.Sprintf("imported %d past session(s) from %s — resume them with --resume or the history panel", n, src.label)})
		}
	}
}

func rememberPermissionRule(workspaceRoot, rule string) control.RememberResult {
	path := rememberPermissionConfigPath(workspaceRoot)
	edit := config.LoadForEdit(path)
	result := control.RememberResult{Rule: strings.TrimSpace(rule), Path: path}
	if coveredBy := coveredPermissionRule(edit.Permissions.Allow, result.Rule); coveredBy != "" {
		result.CoveredBy = coveredBy
		return result
	}
	edit.Permissions.Allow = pruneCoveredPermissionRules(edit.Permissions.Allow, result.Rule)
	if err := edit.AddPermissionRule("allow", rule); err != nil {
		slog.Warn("persist permission rule", "rule", rule, "err", err)
		result.Err = err
		return result
	}
	if err := edit.SaveTo(path); err != nil {
		slog.Warn("save config after permission rule", "err", err)
		result.Err = err
		return result
	}
	result.Saved = true
	return result
}

func rememberPermissionConfigPath(workspaceRoot string) string {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot != "" {
		return filepath.Join(workspaceRoot, "fairpeer.toml")
	}
	path := config.SourcePath()
	if path == "" {
		path = "fairpeer.toml" // match Config.Save() fallback
	}
	return path
}

func coveredPermissionRule(rules []string, rule string) string {
	for _, existing := range rules {
		if permission.RuleCoversString(existing, rule) {
			return strings.TrimSpace(existing)
		}
	}
	return ""
}

func pruneCoveredPermissionRules(rules []string, rule string) []string {
	out := rules[:0]
	for _, existing := range rules {
		if strings.TrimSpace(existing) == "" || permission.RuleCoversString(rule, existing) {
			continue
		}
		out = append(out, existing)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func subagentModelRef(cfg *config.Config, sk skill.Skill) string {
	if cfg != nil {
		for _, key := range subagentModelKeys(sk.Name) {
			if m := strings.TrimSpace(cfg.Agent.SubagentModels[key]); m != "" {
				return m
			}
		}
	}
	if m := strings.TrimSpace(sk.Model); m != "" {
		return m
	}
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Agent.SubagentModel)
}

func subagentEffortRef(cfg *config.Config, sk skill.Skill) string {
	if cfg != nil {
		for _, key := range subagentModelKeys(sk.Name) {
			if e := strings.TrimSpace(cfg.Agent.SubagentEfforts[key]); e != "" {
				return e
			}
		}
	}
	if e := strings.TrimSpace(sk.Effort); e != "" {
		return e
	}
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Agent.SubagentEffort)
}

func subagentModelKeys(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	keys := []string{name}
	for _, alias := range []string{
		strings.ReplaceAll(name, "-", "_"),
		strings.ReplaceAll(name, "_", "-"),
	} {
		if alias == "" {
			continue
		}
		seen := false
		for _, key := range keys {
			if key == alias {
				seen = true
				break
			}
		}
		if !seen {
			keys = append(keys, alias)
		}
	}
	return keys
}

func resolveWorkspaceRoot(explicit string) string {
	if explicit != "" {
		return explicit
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if root, ok := nearestGitRoot(wd); ok {
		return root
	}
	return wd
}

func nearestGitRoot(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		dir = filepath.Clean(start)
	}
	for {
		if isGitMarker(filepath.Join(dir, ".git")) {
			return dir, true
		}
		next := filepath.Dir(dir)
		if next == dir {
			return "", false
		}
		dir = next
	}
}

func isGitMarker(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && (fi.IsDir() || fi.Mode().IsRegular())
}

func newSubagentStore(sessionDir string) *agent.SubagentStore {
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" {
		return nil
	}
	return agent.NewSubagentStore(filepath.Join(sessionDir, "subagents"))
}

func subagentEffectiveIdentity(cfg *config.Config, baseModelRef string, base *config.ProviderEntry, modelRef, effort string) (string, string) {
	var entry config.ProviderEntry
	if base != nil {
		entry = *base
	}
	ref := strings.TrimSpace(modelRef)
	if ref == "" {
		ref = strings.TrimSpace(baseModelRef)
	}
	if cfg != nil && ref != "" {
		if resolved, ok := cfg.ResolveModel(ref); ok {
			entry = *resolved
		} else if strings.TrimSpace(modelRef) != "" {
			entry.Model = ref
		}
	} else if strings.TrimSpace(modelRef) != "" {
		entry.Model = strings.TrimSpace(modelRef)
	}
	if rawEffort := strings.TrimSpace(effort); rawEffort != "" {
		if normalized, err := config.NormalizeEffort(&entry, rawEffort); err == nil {
			entry.Effort = normalized
		} else {
			entry.Effort = rawEffort
		}
	}
	modelID := strings.TrimSpace(entry.Name)
	model := strings.TrimSpace(entry.Model)
	if modelID != "" && model != "" {
		modelID += "/" + model
	} else if model != "" {
		modelID = model
	} else if modelID == "" {
		modelID = ref
	}
	return modelID, strings.TrimSpace(config.EffectiveEffort(&entry))
}

// runProviderVLMChat runs a one-shot multimodal chat through the provider layer,
// used as the VLM backend when [cowork] vlm_backend = "provider". It resolves the
// model ref to a configured provider entry, builds a proxy-aware provider client,
// streams the request, and returns a single assistant message with the aggregated
// text. The model must be vision-capable (provider.Vision); non-vision models
// will error at the provider when given image content, surfacing a clear message.
func runProviderVLMChat(ctx context.Context, cfg *config.Config, modelRef string, msgs []provider.Message) ([]provider.Message, error) {
	ref := strings.TrimSpace(modelRef)
	if ref == "" {
		return nil, fmt.Errorf("vlm_model is empty — set [cowork] vlm_model to a vision-capable model")
	}
	entry, ok := cfg.ResolveModel(ref)
	if !ok {
		return nil, fmt.Errorf("vlm_model %q is not a configured provider", ref)
	}
	prov, err := NewProviderWithProxy(entry, proxySpecForVLM, false)
	if err != nil {
		return nil, fmt.Errorf("build VLM provider %q: %w", ref, err)
	}
	// VLM output cap. Reasoning-model VLMs (e.g. mimo) burn tokens on hidden
	// reasoning BEFORE the visible content — at 1024 a color-JSON prompt could
	// return HTTP 200 with content=None (reasoning ate the whole budget), which
	// surfaced as colors silently vanishing from reference-style.json. The cap
	// is a ceiling, not a target — generation stops when done — so err generous:
	// 4096 for images/text (empirically the color task burns ~1024+ reasoning
	// tokens alone), 8192 for STT transcripts (audio input), which are longer.
	maxTokens := 4096
	for _, m := range msgs {
		if m.HasAudio() {
			maxTokens = 8192
			break
		}
	}
	ch, err := prov.Stream(ctx, provider.Request{Messages: msgs, MaxTokens: maxTokens})
	if err != nil {
		return nil, err
	}
	var sb strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			return nil, chunk.Err
		}
		if chunk.Type == provider.ChunkText {
			sb.WriteString(chunk.Text)
		}
	}
	return []provider.Message{{Role: provider.RoleAssistant, Content: sb.String()}}, nil
}

// proxySpecForVLM is set during Build so the VLM runner (defined above, which
// can't easily take params) shares the same proxy as the main provider. It is
// only read inside runProviderVLMChat, always after Build assigns it.
var proxySpecForVLM netclient.ProxySpec

// resolveBrowserProxyURL returns a single concrete proxy URL (e.g.
// "http://127.0.0.1:7890") for chromedp's --proxy-server flag, derived from the
// network spec. chromedp needs ONE URL applied to all requests (it can't take a
// per-request resolver), so we resolve the proxy that would be used for a
// generic HTTPS request. Returns "" (no proxy / direct) when the spec is "off"
// or no proxy applies — chromedp then uses the system default, matching the
// previous behaviour.
func resolveBrowserProxyURL(spec netclient.ProxySpec) string {
	mode := netclient.NormalizeMode(spec.Mode)
	if mode == netclient.ModeOff {
		return ""
	}
	pf, err := netclient.ProxyFunc(spec)
	if err != nil || pf == nil {
		return ""
	}
	// Probe with a generic https request — covers the common case (proxied HTTPS).
	// DirectHosts bypass is part of pf, so a direct host correctly resolves to "".
	req, err := http.NewRequest(http.MethodGet, "https://browser-proxy-probe.invalid/", nil)
	if err != nil {
		return ""
	}
	u, err := pf(req)
	if err != nil || u == nil {
		return ""
	}
	// Strip auth for the flag: chromedp/Chrome handles proxy auth via the URL's
	// userinfo, but embedding credentials in --proxy-server is fragile (some
	// builds reject it). Authed proxies are uncommon for local dev proxies; we
	// surface the scheme://host:port and let Chrome prompt or use a profile.
	return u.Scheme + "://" + u.Host
}

// NewProvider builds a provider.Provider from a configured entry. Exported so
// custom assemblers (e.g. the ACP per-session factory) can reuse it without
// going through the full Build.
func NewProvider(e *config.ProviderEntry) (provider.Provider, error) {
	return NewProviderWithProxy(e, netclient.ProxySpec{Mode: netclient.ModeAuto}, false)
}

// NewProviderWithProxy builds a provider.Provider with the configured ordinary
// network proxy settings, and wraps it with the global request-budget decorator
// (when [llm] rpm > 0) so it shares the per-API-key RPM quota.
//
// mainProvider marks the provider as the main-agent's (high-priority RPM slots,
// protected by reserve_main); background providers (subagents, classifiers,
// VLM, etc.) pass false. mainProvider is threaded explicitly so concurrent
// boot.Build calls don't race on a process-global "which provider is main" flag.
func NewProviderWithProxy(e *config.ProviderEntry, proxy netclient.ProxySpec, mainProvider bool) (provider.Provider, error) {
	// Reject reasoning_protocol typos up front (anthropic ignores the knob, so
	// the openai wire layer alone would not catch it there). Surface the valid
	// values instead of silently degrading to auto.
	if e != nil && !config.ReasoningProtocolValid(e.ReasoningProtocol) {
		return nil, fmt.Errorf("provider %q: unknown reasoning_protocol %q (valid: auto|openai|minimax|none)", e.Name, e.ReasoningProtocol)
	}
	p, err := provider.New(e.Kind, provider.Config{
		Name:    e.Name,
		BaseURL: e.BaseURL,
		Model:   e.Model,
		APIKey:  e.APIKey(),
		// Pass the key's env var so auth failures can name where to fix it, plus
		// provider-kind-specific knobs. EffectiveEffort applies a configured
		// default_effort when the user has not explicitly selected /effort.
		Extra: map[string]any{
			"api_key_env":        e.APIKeyEnv,
			"thinking":           e.Thinking,
			"effort":             config.EffectiveEffort(e),
			"reasoning_protocol": config.ReasoningProtocolForEntry(e),
			// Declared effort vocabulary (provider TOML supported_efforts /
			// default_effort) reaches the adapters' DeclaredReasoning so an
			// endpoint with non-standard levels can be described exactly.
			"supported_efforts": e.SupportedEfforts,
			"default_effort":    e.DefaultEffort,
			"proxy_spec":        proxy,
			"vision":            e.Vision,
			"vision_detail":     e.VisionDetail,
		},
	})
	if err != nil {
		return nil, err
	}
	if globalBudget != nil && globalBudget.Status("").RPM > 0 {
		key := provider.BudgetKeyForConfig(e.Name, e.BaseURL, e.APIKey())
		return provider.NewRateLimitedProvider(p, globalBudget, key, mainProvider), nil
	}
	return p, nil
}

// addBuiltins adds enabled built-in tools to reg. An empty list means all of
// them. writeRoots confines the file-writing built-ins to the workspace: after
// the (unconfined) defaults are added, each enabled writer is replaced by an
// instance bound to writeRoots (preserving registry order).
// When workDir is non-empty, tools resolve relative paths against it instead of
// the process cwd, enabling concurrent multi-project sessions.
func addBuiltins(reg *tool.Registry, enabled, writeRoots, readRoots []string, bashSpec sandbox.Spec, bashTimeout time.Duration, searchSpec builtin.SearchSpec, stderr io.Writer, workDir string, proxySpec netclient.ProxySpec) {
	// If a workspace directory is set, use workspace-bound tools that resolve
	// paths relative to that directory. Otherwise fall back to the process-cwd
	// compile-time builtins.
	if workDir != "" {
		ws := builtin.Workspace{Dir: workDir, WriteRoots: writeRoots, Bash: bashSpec, BashTimeout: bashTimeout, Search: searchSpec, ProxySpec: proxySpec}
		for _, t := range ws.Tools(enabled...) {
			reg.Add(t)
		}
		return
	}

	if len(enabled) == 0 {
		for _, t := range tool.Builtins() {
			reg.Add(t)
		}
	} else {
		for _, name := range enabled {
			if t, ok := tool.LookupBuiltin(name); ok {
				reg.Add(t)
			} else {
				fmt.Fprintf(stderr, "warning: unknown built-in tool %q\n", name)
			}
		}
	}
	// Replace the unconfined defaults with confined instances (registry order is
	// preserved on replace): file-writers bound to the workspace, bash to the OS
	// sandbox, web_fetch to the proxy. read_file/grep are confined ONLY when
	// [sandbox] read_roots is set (opt-in read isolation). Only replace tools
	// actually enabled/present.
	confined := append(builtin.ConfineWriters(writeRoots), builtin.ConfineBash(bashSpec, bashTimeout), builtin.ConfineSearch(searchSpec), builtin.ConfineWebFetch(proxySpec))
	confined = append(confined, builtin.ConfineReaders(readRoots)...)
	for _, t := range confined {
		if _, ok := reg.Get(t.Name()); ok {
			reg.Add(t)
		}
	}
}

// partitionByTier splits configured plugin entries into the three startup
// buckets — eager (block boot until ready), lazy (placeholder until first
// model use), background (placeholder + start spawn now). Entries with an
// empty tier land in background; unrecognised non-empty tiers land in lazy so a
// typo never triggers unexpected background work.
func partitionByTier(entries []config.PluginEntry) (eager, lazy, bg []config.PluginEntry) {
	for _, e := range entries {
		switch e.ResolvedTier() {
		case "eager":
			eager = append(eager, e)
		case "background":
			bg = append(bg, e)
		default:
			lazy = append(lazy, e)
		}
	}
	return eager, lazy, bg
}

// pluginSpecNames extracts the Name from each plugin.Spec. Used to mark
// session-scoped extra plugins as "reserved" so built-in entries don't
// duplicate them.
func pluginSpecNames(specs []plugin.Spec) []string {
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	return names
}

// applyProfileToSkillDisabled merges a profile's skill overrides into the
// config-wide disabled list, returning a new slice. Used so the live skill
// store and the "all skills" index agree on what a profile hides.
//
// Two mechanisms:
//  1. Profile.DisabledSkills — additive: these names join the disabled set.
//  2. Profile.EnabledSkills — whitelist: when non-empty, any builtin skill not
//     in it is disabled. The builtin skill names are a fixed list (see
//     builtinBuiltinSkillNames below), so we can enumerate the "rest" without
//     needing the discovered skill set. User-authored skills are deliberately
//     unaffected — they're opt-in via file placement, and a user who drops a
//     custom skill into the tree expects it to work regardless of profile.
//
// The result preserves first spelling and dedupes by SkillNameKey.
// netdevExcludedToolPrefixes lists the tool-name prefixes removed from the
// Registry when a profile sets tool_scope = "netdev-only": every process-exec
// and file-write surface. Prefix matching (Registry.RemovePrefix) also covers
// helper variants (e.g. "bash_output", "kill_shell" under "bash"/"kill_").
// Read-only tools (read_file, grep, glob, web_fetch, rag_search, todo…) stay:
// the netdev diagnostic hand reads broadly and writes nothing.
var netdevExcludedToolPrefixes = []string{
	"bash",          // process execution (and bash_output)
	"kill_shell",    // process management
	"edit_file",     // file writes
	"write_file",    // file writes
	"multi_edit",    // file writes
	"apply_patch",   // file writes
	"notebook_edit", // file writes
	"delete_range",  // file deletion
	"move_file",     // file moves
}

// legacySkillRenames maps retired skill names to their successors, applied to
// user config (EnabledSkills/DisabledSkills) and skill-disable lists so a
// whitelist written before a rename keeps working instead of silently losing
// its grip on the renamed skill.
var legacySkillRenames = map[string]string{
	"computer-auto": "desktop-auto",   // renamed 2026-08: GUI-only scope
	"rag-auto":      "knowledge-auto", // renamed 2026-08: name said the tech (RAG), not the job (knowledge base)
}

// normalizeSkillName applies legacySkillRenames (case-insensitive) and returns
// the empty string for unrenamed names.
func normalizeSkillName(name string) string {
	if v, ok := legacySkillRenames[strings.ToLower(strings.TrimSpace(name))]; ok {
		return v
	}
	return ""
}

func applyProfileToSkillDisabled(p *config.Profile, configDisabled []string) []string {
	if p == nil {
		return configDisabled
	}
	seen := make(map[string]bool, len(configDisabled)+len(p.DisabledSkills)+len(builtinBuiltinSkillNames))
	out := make([]string, 0, len(configDisabled)+len(p.DisabledSkills)+len(builtinBuiltinSkillNames))
	add := func(name string) {
		name = strings.TrimSpace(name)
		if !config.IsValidSkillName(name) {
			return
		}
		if renamed := normalizeSkillName(name); renamed != "" {
			name = renamed
		}
		key := config.SkillNameKey(name)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, name)
	}
	for _, name := range configDisabled {
		add(name)
	}
	for _, name := range p.DisabledSkills {
		add(name)
	}
	// Whitelist enforcement: disable every builtin skill NOT in the whitelist.
	// This makes dev mode actually hide office skills (browser-auto etc.) from
	// run_skill too, not just from the index — otherwise the model could still
	// invoke a "hidden" skill by name.
	if len(p.EnabledSkills) > 0 {
		allowed := make(map[string]bool, len(p.EnabledSkills))
		for _, n := range p.EnabledSkills {
			if renamed := normalizeSkillName(n); renamed != "" {
				n = renamed
			}
			allowed[config.SkillNameKey(n)] = true
		}
		for _, name := range builtinBuiltinSkillNames {
			if !allowed[config.SkillNameKey(name)] {
				add(name)
			}
		}
	}
	return out
}

// builtinBuiltinSkillNames is the fixed list of shipped skill names: the 15
// code builtins (internal/skill/builtins.go) plus ppt-auto (embedded file
// skill, released to ~/.fairpeer/skills — listed so profile whitelists govern
// it too). Used by applyProfileToSkillDisabled to enumerate which shipped
// skills a whitelist hides — the skill store hasn't been built yet at the
// point that function runs, so we can't ask it for the list. Keep in sync
// with builtinSkills(); drift lets a skill escape every whitelist (it stays
// enabled in dev/cowork/netdev regardless of the profile's domain).
var builtinBuiltinSkillNames = []string{
	"init", "install-capability", "test",
	"explore", "research", "review", "security-review",
	"browser-auto", "desktop-auto", "ppt-auto",
	"email-auto", "knowledge-auto", "schedule-auto",
	"document-auto", "expert-auto",
	"netdev-help",
	"netdev-playbook",
	"netdev-diag-ospf", "netdev-diag-bgp", "netdev-diag-interface",
}

// profileSkillWhitelist returns the profile's EnabledSkills as a SkillNameKey set,
// or nil if the profile defines no whitelist (meaning "all skills allowed").
func profileSkillWhitelist(p *config.Profile) map[string]bool {
	if p == nil || len(p.EnabledSkills) == 0 {
		return nil
	}
	out := make(map[string]bool, len(p.EnabledSkills))
	for _, n := range p.EnabledSkills {
		if k := config.SkillNameKey(n); k != "" {
			out[k] = true
		}
	}
	return out
}

// profileName returns the active product-mode name ("dev"|"cowork") for memory
// partitioning, defaulting to "dev" when no profile is set. A nil profile means
// the unprofiled floor — identical to dev — so callers that never set one keep
// their existing memory path rather than landing in a dangling partition.
func profileName(p *config.Profile) string {
	if p == nil {
		return config.ProfileDev
	}
	return strings.TrimSpace(p.Name)
}

// PluginSpecs maps configured plugin entries to plugin.Spec, expanding ${VAR}
// references. Exported so custom assemblers can connect the config's plugins
// alongside their own (e.g. ACP's per-session MCP servers).
func PluginSpecs(entries []config.PluginEntry) []plugin.Spec {
	specs := make([]plugin.Spec, len(entries))
	for i, e := range entries {
		e = e.ExpandedPlugin() // resolve ${VAR} / ${VAR:-default} from the environment
		specs[i] = plugin.Spec{
			Name:        e.Name,
			Type:        e.Type,
			Command:     e.Command,
			Args:        e.Args,
			Env:         e.Env,
			URL:         e.URL,
			Headers:     e.Headers,
			CallTimeout: parseDuration(e.CallTimeout),
			ServerRisk:  e.Risk,
		}
	}
	return specs
}

// parseDuration parses a Go duration string, returning 0 for empty/invalid.
func parseDuration(s string) time.Duration {
	d, _ := time.ParseDuration(strings.TrimSpace(s))
	return d
}

// buildRiskOverrides turns each [[plugins]] risk="…" declaration into a per-
// server prefix entry on the permission Gate's RiskOverrides map (SPEC v2
// §3.2A). A server configured risk="read" yields key "mcp__<server>__" →
// RiskRead, so every tool on that server skips the external-risk default.
// Empty/unknown risk strings are skipped (they default to external via
// permission.Classify's MCP rule — no map entry needed). Returns nil when no
// server declares a non-default risk, keeping the Gate's override map empty.
func buildRiskOverrides(entries []config.PluginEntry) map[string]permission.RiskClass {
	var overrides map[string]permission.RiskClass
	for _, e := range entries {
		raw := strings.TrimSpace(e.Risk)
		if raw == "" || strings.EqualFold(raw, "external") {
			continue // default; no override needed
		}
		cls := permission.ParseRiskClass(raw)
		if cls == permission.RiskExternal {
			continue // ParseRiskClass's safe-default also lands here; skip.
		}
		if overrides == nil {
			overrides = make(map[string]permission.RiskClass)
		}
		overrides["mcp__"+e.Name+"__"] = cls
	}
	return overrides
}

// MCPStartupNotice formats the warning shown when configured MCP servers failed
// to connect, naming the first few; ok is false when none failed.
func MCPStartupNotice(failures []plugin.Failure) (text string, ok bool) {
	if len(failures) == 0 {
		return "", false
	}
	names := make([]string, 0, min(len(failures), 3))
	for i, f := range failures {
		if i >= 3 {
			break
		}
		names = append(names, f.Name)
	}
	more := ""
	if len(failures) > len(names) {
		more = fmt.Sprintf(" (+%d more)", len(failures)-len(names))
	}
	return fmt.Sprintf("%d MCP server(s) failed to start: %s%s — run /mcp for details",
		len(failures), strings.Join(names, ", "), more), true
}

// LSPSpecs returns the language → server map: the built-in defaults overlaid with
// any user overrides. A user entry may set only the fields it wants to change;
// empty fields keep the default for that language.
func LSPSpecs(cfg config.LSPConfig) map[string]lsp.ServerSpec {
	specs := lsp.DefaultSpecs()
	for lang, s := range cfg.Servers {
		spec := specs[lang]
		if s.Command != "" {
			spec.Command = s.Command
		}
		if s.Args != nil {
			spec.Args = s.Args
		}
		if s.Env != nil {
			spec.Env = s.Env
		}
		if s.LanguageID != "" {
			spec.LanguageID = s.LanguageID
		}
		if s.Extensions != nil {
			spec.Extensions = s.Extensions
		}
		if s.InstallHint != "" {
			spec.InstallHint = s.InstallHint
		}
		if spec.LanguageID == "" {
			spec.LanguageID = lang
		}
		specs[lang] = spec
	}
	return specs
}

func providerNames(cfg *config.Config) string {
	names := make([]string, len(cfg.Providers))
	for i, p := range cfg.Providers {
		names[i] = p.Name
	}
	return strings.Join(names, "/")
}

// retireColdSkills is the post-distill hard-decay step: skills unused for more
// than 2× the cold threshold are persisted to disabled_skills, dropping them
// from the registry on the next Build (the soft [休眠] tag is the first tier at
// 1×; this is the second). Built-ins are exempt. Returns a human-readable
// summary of what was retired, or "" when nothing changed. Best-effort: any
// error (missing store, unreadable config) returns "" without panicking —
// retirement is cleanup, never a primary outcome.
func retireColdSkills(store *skill.Store, coldDays int, configPath string) string {
	if store == nil || coldDays <= 0 {
		return ""
	}
	ut := store.Usage()
	if ut == nil {
		return "" // tracking disabled (no StateDir)
	}
	// 2× threshold for hard retirement: soft decay ([休眠]) kicks in at 1×, hard
	// (disabled) at 2× — giving a long grace window before a skill is truly
	// benched. The user can always re-enable from Settings → Skills.
	hardThreshold := time.Duration(coldDays*2) * 24 * time.Hour
	var known []string
	for _, s := range store.List() {
		if s.Scope == skill.ScopeBuiltin {
			continue
		}
		known = append(known, s.Name)
	}
	cold := ut.ColdSkillNames(hardThreshold, true, known)
	if len(cold) == 0 {
		return ""
	}
	cfg := config.LoadForEdit(configPath)
	var retired []string
	for _, name := range cold {
		// SetSkillEnabled(name, false) is idempotent — a skill already disabled
		// stays disabled; we just ensure the threshold-eligible ones are in the
		// disabled set so the next Build drops them from the live registry.
		if err := cfg.SetSkillEnabled(name, false); err == nil {
			retired = append(retired, name)
		}
	}
	if len(retired) == 0 {
		return ""
	}
	if err := cfg.SaveTo(configPath); err != nil {
		return "retired " + strings.Join(retired, ", ") + " (persist failed: " + err.Error() + ")"
	}
	return fmt.Sprintf("retired %d dormant skill(s): %s", len(retired), strings.Join(retired, ", "))
}
