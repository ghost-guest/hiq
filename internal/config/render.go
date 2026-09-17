package config

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type RenderScope string

const (
	RenderScopeFull    RenderScope = "full"
	RenderScopeUser    RenderScope = "user"
	RenderScopeProject RenderScope = "project"
)

// RenderTOML renders the config as annotated TOML in the `fairpeer setup` house style:
// comments preserved, system_prompt as a multi-line string, helpful hints. The
// output round-trips back through Load (see render_test.go).
func RenderTOML(c *Config) string {
	return RenderTOMLForScope(c, RenderScopeFull)
}

// RenderTOMLForScope renders an annotated TOML file for a specific persistence
// target. User configs can carry desktop and account-level preferences; project
// fairpeer.toml stays focused on project behavior and intentionally excludes
// desktop-only preferences.
func RenderTOMLForScope(c *Config, scope RenderScope) string {
	if c == nil {
		c = Default()
	}
	switch scope {
	case RenderScopeUser, RenderScopeProject:
	default:
		scope = RenderScopeFull
	}
	defaults := Default()
	var b strings.Builder

	b.WriteString("# fairpeer configuration.\n")
	b.WriteString("# Resolution order: flag > ./fairpeer.toml > ~/.config/fairpeer/config.toml > built-in defaults.\n")
	b.WriteString("# Secrets come from the environment via api_key_env; never put keys here.\n\n")

	fmt.Fprintf(&b, "config_version = %d   # schema marker for diagnostics; old versions may ignore it\n", configVersion(c))
	fmt.Fprintf(&b, "default_model = %q\n", c.DefaultModel)
	if c.Language != "" {
		fmt.Fprintf(&b, "language      = %q   # ui/model language; empty = auto-detect from $LANG / $FAIRPEER_LANG\n", c.Language)
	} else {
		b.WriteString("# language      = \"zh\"   # ui/model language; empty = auto-detect from $LANG / $FAIRPEER_LANG\n")
	}
	b.WriteString("\n")

	if shouldRenderUI(c, defaults, scope) {
		b.WriteString("[ui]\n")
		fmt.Fprintf(&b, "theme = %q   # auto|dark|light; CLI colors only; FAIRPEER_THEME can override per run\n", c.UITheme())
		if style := c.UIThemeStyle(); style != "" {
			fmt.Fprintf(&b, "theme_style = %q   # CLI accent palette; FAIRPEER_THEME_STYLE can override per run\n", style)
		} else {
			b.WriteString("# theme_style = \"slate\"   # slate|graphite|aurora|midnight|sandstone|porcelain|linen|glacier\n")
		}
		if layout := c.UIShortcutLayout(); layout != "classic" {
			fmt.Fprintf(&b, "shortcut_layout = %q   # classic|desktop; compatibility setting; Shift+Tab toggles Plan, Ctrl+Y toggles YOLO\n", layout)
		} else {
			b.WriteString("# shortcut_layout = \"desktop\"   # classic|desktop; compatibility setting; Shift+Tab toggles Plan, Ctrl+Y toggles YOLO\n")
		}
		if strings.TrimSpace(c.UI.CloseBehavior) != "" && scope == RenderScopeProject {
			fmt.Fprintf(&b, "close_behavior = %q   # legacy desktop close behavior; prefer [desktop].close_behavior in user config\n", c.DesktopCloseBehavior())
		}
		if c.UI.ShowReasoning {
			b.WriteString("show_reasoning = true   # CLI: show thinking text by default; false = collapsed (toggle with Ctrl+O)\n")
		} else {
			b.WriteString("# show_reasoning = true   # CLI: show thinking text by default; false = collapsed (toggle with Ctrl+O)\n")
		}
		b.WriteString("\n")
	}

	if scope != RenderScopeProject {
		b.WriteString("[desktop]\n")
		if lang := c.DesktopLanguage(); lang != "" {
			fmt.Fprintf(&b, "language = %q   # desktop UI language; empty/auto = browser/OS auto-detect\n", lang)
		} else {
			b.WriteString("# language = \"zh\"   # desktop UI language; empty/auto = browser/OS auto-detect\n")
		}
		fmt.Fprintf(&b, "theme = %q   # desktop only: auto|dark|light\n", c.DesktopTheme())
		if style := c.DesktopThemeStyle(); style != "" {
			fmt.Fprintf(&b, "theme_style = %q   # desktop accent palette\n", style)
		} else {
			b.WriteString("# theme_style = \"slate\"   # slate|graphite|aurora|midnight|sandstone|porcelain|linen|glacier\n")
		}
		fmt.Fprintf(&b, "close_behavior = %q   # desktop: quit|background when the window close button is clicked\n", c.DesktopCloseBehavior())
		fmt.Fprintf(&b, "check_updates = %v   # desktop: check for new versions on startup\n", c.DesktopCheckUpdates())
		fmt.Fprintf(&b, "telemetry = %v   # desktop: anonymous launch ping (install id + version + OS); never content\n", c.DesktopTelemetry())
		fmt.Fprintf(&b, "metrics = %v   # desktop: opt-in aggregate agent metrics (anonymous signal/bucket counts); never content\n", c.DesktopMetrics())
		if len(c.Desktop.ProviderAccess) > 0 {
			fmt.Fprintf(&b, "provider_access = %s   # desktop settings: providers shown on Settings > Model > Access\n", renderStringArray(c.Desktop.ProviderAccess))
		}
		fmt.Fprintf(&b, "expand_thinking = %v   # desktop: show reasoning text expanded by default; false = collapsed\n", c.Desktop.ExpandThinking)
		b.WriteString("\n")

		b.WriteString("[notifications]\n")
		fmt.Fprintf(&b, "enabled = %v   # system notifications for CLI chat/run; default off\n", c.Notifications.Enabled)
		fmt.Fprintf(&b, "turn_done = %v   # notify when a turn finishes\n", c.Notifications.TurnDone)
		fmt.Fprintf(&b, "approval_request = %v   # notify when a tool approval is waiting\n", c.Notifications.ApprovalRequest)
		fmt.Fprintf(&b, "ask_request = %v   # notify when a question is waiting\n", c.Notifications.AskRequest)
		b.WriteString("\n")
	}

	if shouldRenderNetwork(c, defaults, scope) {
		b.WriteString("[network]\n")
		fmt.Fprintf(&b, "proxy_mode = %q   # auto|env|custom|off; auto currently uses env proxy\n", c.NetworkProxyMode())
		if c.Network.ProxyURL != "" {
			fmt.Fprintf(&b, "proxy_url  = %q   # custom override, e.g. socks5://127.0.0.1:7890\n", c.Network.ProxyURL)
		} else {
			b.WriteString("# proxy_url  = \"socks5://127.0.0.1:7890\"   # optional custom override\n")
		}
		if c.Network.NoProxy != "" {
			fmt.Fprintf(&b, "no_proxy   = %q   # honored for proxy_mode = \"custom\"\n", c.Network.NoProxy)
		} else {
			b.WriteString("# no_proxy   = \"localhost,127.0.0.1,.local\"   # honored for proxy_mode = \"custom\"\n")
		}
		b.WriteString("\n[network.proxy]\n")
		proxyType := c.Network.Proxy.Type
		if proxyType == "" {
			proxyType = "socks5"
		}
		fmt.Fprintf(&b, "type = %q   # http|https|socks5|socks5h\n", proxyType)
		if c.Network.Proxy.Server != "" {
			fmt.Fprintf(&b, "server = %q\n", c.Network.Proxy.Server)
		} else {
			b.WriteString("# server = \"127.0.0.1\"\n")
		}
		if c.Network.Proxy.Port > 0 {
			fmt.Fprintf(&b, "port = %d\n", c.Network.Proxy.Port)
		} else {
			b.WriteString("# port = 7890\n")
		}
		if c.Network.Proxy.Username != "" {
			fmt.Fprintf(&b, "username = %q\n", c.Network.Proxy.Username)
		} else {
			b.WriteString("# username = \"\"\n")
		}
		if c.Network.Proxy.Password != "" {
			fmt.Fprintf(&b, "password = %q   # supports ${VAR} expansion\n", c.Network.Proxy.Password)
		} else {
			b.WriteString("# password = \"${FAIRPEER_PROXY_PASSWORD}\"   # optional; supports ${VAR} expansion\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("[agent]\n")
	if shouldRenderSystemPrompt(c, defaults, scope) {
		b.WriteString("system_prompt = \"\"\"\n")
		b.WriteString(c.Agent.SystemPrompt)
		b.WriteString("\"\"\"\n")
	} else {
		b.WriteString("# system_prompt = \"\"\"...\"\"\"   # omit to use the built-in prompt for this version\n")
	}
	if c.Agent.SystemPromptFile != "" {
		fmt.Fprintf(&b, "system_prompt_file = %q\n", c.Agent.SystemPromptFile)
	} else {
		b.WriteString("# system_prompt_file = \"prompts/system.md\"   # overrides system_prompt when set\n")
	}
	fmt.Fprintf(&b, "max_steps         = %d   # executor tool-call rounds; 0 = no limit\n", c.Agent.MaxSteps)
	fmt.Fprintf(&b, "planner_max_steps = %d   # planner read-only tool-call rounds; 0 = no limit\n", c.Agent.PlannerMaxSteps)
	fmt.Fprintf(&b, "temperature       = %s\n", formatFloat(c.Agent.Temperature))
	autoPlan := c.Agent.AutoPlan
	switch strings.ToLower(strings.TrimSpace(autoPlan)) {
	case "on", "ask":
		autoPlan = "on"
	default:
		autoPlan = "off"
	}
	fmt.Fprintf(&b, "auto_plan   = %q   # off|on; off keeps plan mode manual\n", autoPlan)
	if c.Agent.AutoPlanClassifier != "" {
		fmt.Fprintf(&b, "auto_plan_classifier = %q   # optional provider/model for borderline auto-plan decisions\n", c.Agent.AutoPlanClassifier)
	} else {
		b.WriteString("# auto_plan_classifier = \"<provider>/<model>\"   # optional; only used for borderline tasks\n")
	}
	fmt.Fprintf(&b, "soft_compact_ratio  = %s   # notice only; keeps cache-first prefix intact\n", formatFloat(c.Agent.SoftCompactRatio))
	fmt.Fprintf(&b, "compact_ratio       = %s   # try compacting when prompt reaches this fraction\n", formatFloat(c.Agent.CompactRatio))
	fmt.Fprintf(&b, "compact_force_ratio = %s   # force compacting at this high-water mark\n", formatFloat(c.Agent.CompactForceRatio))
	if c.Agent.FastTaskModel != "" {
		fmt.Fprintf(&b, "fast_task_model = %q   # lightweight model for background tasks (dream/distill/rag-extract)\n", c.Agent.FastTaskModel)
	} else {
		b.WriteString("# fast_task_model = \"<provider>/<model>\"   # lightweight model for background tasks\n")
	}
	if c.Agent.PlannerModel != "" {
		fmt.Fprintf(&b, "planner_model = %q   # low-frequency planner (two-model collaboration)\n", c.Agent.PlannerModel)
	} else {
		b.WriteString("# planner_model = \"<provider>/<model>\"   # optional: enable two-model collaboration\n")
	}
	if c.Agent.SubagentModel != "" {
		fmt.Fprintf(&b, "subagent_model = %q   # default model for runAs=subagent skills\n", c.Agent.SubagentModel)
	} else {
		b.WriteString("# subagent_model = \"<provider>/<model>\"   # optional default for runAs=subagent skills\n")
	}
	if len(c.Agent.SubagentModels) > 0 {
		fmt.Fprintf(&b, "subagent_models = %s   # per-skill overrides\n", renderStringMap(c.Agent.SubagentModels))
	} else {
		b.WriteString("# subagent_models = { review = \"<provider>/<model>\", security_review = \"<provider>/<model>\" }   # per-skill overrides\n")
	}
	if c.Agent.SubagentEffort != "" {
		fmt.Fprintf(&b, "subagent_effort = %q   # default effort for subagent entry points\n", c.Agent.SubagentEffort)
	} else {
		b.WriteString("# subagent_effort = \"high\"   # optional default effort for subagents\n")
	}
	if len(c.Agent.SubagentEfforts) > 0 {
		fmt.Fprintf(&b, "subagent_efforts = %s   # per-tool/skill effort overrides\n", renderStringMap(c.Agent.SubagentEfforts))
	} else {
		b.WriteString("# subagent_efforts = { review = \"max\", task = \"high\" }   # per-tool/skill effort overrides\n")
	}
	if c.Agent.OutputStyle != "" {
		fmt.Fprintf(&b, "output_style = %q   # persona/tone folded into the prompt\n", c.Agent.OutputStyle)
	} else {
		b.WriteString("# output_style = \"explanatory\"   # explanatory | learning | concise | custom; empty = default\n")
	}
	b.WriteString("\n")

	b.WriteString("[llm]\n")
	fmt.Fprintf(&b, "rpm = %d   # max requests/minute per API key (0 = unlimited)\n", c.LLM.RPM)
	b.WriteString("\n")

	if shouldRenderProviders(c, defaults, scope) {
		for _, p := range c.Providers {
			// Ambient injected presets are runtime-only: writing them out would
			// turn "nothing selected yet" into a permanent [[providers]] entry
			// the user never wrote (and flip them out of ambient semantics).
			// They render once the user adds them via provider_access.
			if c.AmbientLocalPreset(p.Name) {
				continue
			}
			b.WriteString("[[providers]]\n")
			fmt.Fprintf(&b, "name        = %q\n", p.Name)
			fmt.Fprintf(&b, "kind        = %q\n", p.Kind)
			fmt.Fprintf(&b, "base_url    = %q\n", p.BaseURL)
			if len(p.Models) > 0 {
				fmt.Fprintf(&b, "models      = %s\n", renderStringArray(p.Models))
				if p.Default != "" {
					fmt.Fprintf(&b, "default     = %q\n", p.Default)
				}
			} else if p.Model != "" {
				fmt.Fprintf(&b, "model       = %q\n", p.Model)
			}
			if p.ModelsURL != "" {
				fmt.Fprintf(&b, "models_url  = %q   # auto-fetch models from this URL on startup\n", p.ModelsURL)
			}
			fmt.Fprintf(&b, "api_key_env = %q\n", p.APIKeyEnv)
			if p.ContextWindow > 0 {
				fmt.Fprintf(&b, "context_window = %d   # tokens; compaction triggers near this limit\n", p.ContextWindow)
			}
			if p.Price != nil {
				fmt.Fprintf(&b, "price       = { cache_hit = %v, cache_write = %v, input = %v, output = %v, currency = %q }   # per 1M tokens (cache_write 0 → defaults to 1.25× input)\n",
					p.Price.CacheHit, p.Price.CacheWrite, p.Price.Input, p.Price.Output, p.Price.Symbol())
			}
			if p.Thinking != "" {
				fmt.Fprintf(&b, "thinking    = %q\n", p.Thinking)
			}
			if p.Effort != "" {
				fmt.Fprintf(&b, "effort      = %q\n", p.Effort)
			}
			if p.ReasoningProtocol != "" {
				fmt.Fprintf(&b, "reasoning_protocol = %q   # auto|openai|none; overrides model/endpoint reasoning detection\n", p.ReasoningProtocol)
			}
			if len(p.SupportedEfforts) > 0 {
				fmt.Fprintf(&b, "supported_efforts = %s   # custom /effort levels exposed by this provider; overrides the built-in Kind/BaseURL default\n", renderStringArray(p.SupportedEfforts))
			}
			if p.DefaultEffort != "" {
				fmt.Fprintf(&b, "default_effort    = %q   # used when /effort is auto or unset; must be one of supported_efforts\n", p.DefaultEffort)
			}
			if p.NoProxy {
				b.WriteString("no_proxy    = true   # reach this base_url directly, never via the proxy\n")
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("[tools]\n")
	if len(c.Tools.Enabled) == 0 {
		b.WriteString("enabled = []   # empty = all built-in tools\n")
	} else {
		b.WriteString("enabled = [")
		for i, t := range c.Tools.Enabled {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", t)
		}
		b.WriteString("]\n")
	}
	fmt.Fprintf(&b, "bash_timeout_seconds = %d   # foreground safety cap; set 0 for no tool-local cap\n\n", c.BashTimeoutSeconds())

	b.WriteString("[codegraph]\n")
	fmt.Fprintf(&b, "enabled      = %v   # built-in MCP server; opt-in — enable here or in Settings\n", c.Codegraph.Enabled)
	fmt.Fprintf(&b, "auto_install = %v   # fetch the runtime when CodeGraph is enabled but missing\n", c.Codegraph.AutoInstall)
	if c.Codegraph.Path != "" {
		fmt.Fprintf(&b, "path         = %q   # optional launcher override\n", c.Codegraph.Path)
	} else {
		b.WriteString("# path       = \"\"   # empty = cache, then PATH, then a bundle beside fairpeer\n")
	}
	b.WriteString("\n")

	// [dream] toggles the background self-evolution agents. Without this section
	// in the rendered file, SetDreamEnabled/Intervals writes were silently dropped
	// (LoadForRoot fell back to defaults), so the master switch never persisted.
	b.WriteString("[dream]\n")
	fmt.Fprintf(&b, "enabled          = %v   # 后台自进化：Dream 记忆整合 + Distill 工作流提炼\n", c.Dream.Enabled)
	fmt.Fprintf(&b, "dream_interval   = %d   # Dream 运行周期（天）；0 = 默认 %d\n", c.Dream.DreamIntervalDays(), DefaultDreamInterval)
	fmt.Fprintf(&b, "distill_interval = %d   # Distill 运行周期（天）；0 = 默认 %d\n", c.Dream.DistillIntervalDays(), DefaultDistillInterval)
	b.WriteString("\n")

	// [memory] relocated the user DATA tree and names the cheap model the
	// background memory agents run on. User-global (pinned by LoadForRoot), so
	// it is rendered only in the user/full scope.
	if scope != RenderScopeProject {
		b.WriteString("[memory]\n")
		if c.Memory.Root != "" {
			fmt.Fprintf(&b, "root = %q   # 记忆数据根目录（profile/memory/projects 全跟随）；留空 = 系统用户配置目录\n", c.Memory.Root)
		} else {
			b.WriteString("# root     = \"D:/fairpeer-data\"   # 记忆数据根目录（profile/memory/projects 全跟随）；留空 = 系统用户配置目录\n")
		}
		if c.Memory.Provider != "" || c.Memory.Model != "" {
			fmt.Fprintf(&b, "provider = %q   # 记忆维护（Dream/Distill）使用的渠道\n", c.Memory.Provider)
			fmt.Fprintf(&b, "model    = %q   # 记忆维护使用的模型（用便宜模型，不烧主模型 token）\n", c.Memory.Model)
		} else {
			b.WriteString("# provider = \"yyqwen\"   # 记忆维护使用的渠道；留空 = agent.fast_task_model → 主模型\n")
			b.WriteString("# model    = \"Qwen3.6-35B-A3B-FP8\"   # 记忆维护使用的模型\n")
		}
		if c.Memory.Effort != "" {
			fmt.Fprintf(&b, "effort   = %q   # 记忆维护推理强度 low|medium|high|max；留空 = 渠道默认\n", c.Memory.Effort)
		}
		if c.Memory.InjectIndex != nil && !*c.Memory.InjectIndex {
			b.WriteString("inject_index = false   # 不把 L1/L2 记忆索引注入系统提示词（默认 true）\n")
		}
		if c.Memory.IndexMaxChars > 0 {
			fmt.Fprintf(&b, "index_max_chars = %d   # 注入索引的字符上限；0 = 默认 1200\n", c.Memory.IndexMaxChars)
		}
		b.WriteString("\n")
	}

	// [cowork] section: PPT, mail (SMTP/IMAP/email_accounts), screenshot, RAG.
	b.WriteString("[cowork]\n")
	if c.Cowork.PPTActiveTemplate != "" {
		fmt.Fprintf(&b, "ppt_active_template = %q   # id of the PPT template to build decks from\n", c.Cowork.PPTActiveTemplate)
	} else {
		b.WriteString("# ppt_active_template = \"\"   # id of a template in ppt-templates/; empty = build from a blank deck\n")
	}
	if c.Cowork.PPTMode != "" {
		fmt.Fprintf(&b, "ppt_mode = %q   # fast (one-shot) or validate (generate + check + rework)\n", c.Cowork.PPTMode)
	} else {
		b.WriteString("# ppt_mode = \"fast\"   # fast (one-shot) or validate (generate + check + rework)\n")
	}
	if c.Cowork.BrowserPath != "" {
		fmt.Fprintf(&b, "browser_path = %q   # Chromium-based browser exe; empty = auto-detect\n", c.Cowork.BrowserPath)
	}
	if c.Cowork.EmbeddingModel != "" {
		fmt.Fprintf(&b, "embedding_model = %q   # enables semantic RAG reranking; empty = FTS5-only\n", c.Cowork.EmbeddingModel)
	}
	if c.Cowork.ScreenshotEnabled {
		fmt.Fprintf(&b, "screenshot_enabled = %v\n", c.Cowork.ScreenshotEnabled)
	}
	if c.Cowork.ScreenshotHotkey != "" {
		fmt.Fprintf(&b, "screenshot_hotkey = %q\n", c.Cowork.ScreenshotHotkey)
	}
	if c.Cowork.ScreenshotVLMModel != "" {
		fmt.Fprintf(&b, "screenshot_vlm_model = %q\n", c.Cowork.ScreenshotVLMModel)
	}
	if c.Cowork.VoiceModel != "" {
		fmt.Fprintf(&b, "voice_model = %q   # STT model for voice input; provider/model ref, OpenAI-compatible /audio/transcriptions\n", c.Cowork.VoiceModel)
	}
	if c.Cowork.ScreenshotPrompt != "" {
		fmt.Fprintf(&b, "screenshot_prompt = %q\n", c.Cowork.ScreenshotPrompt)
	}
	if c.Cowork.EStopHotkey != "" {
		fmt.Fprintf(&b, "estop_hotkey = %q   # emergency-stop hotkey; \"off\" disables\n", c.Cowork.EStopHotkey)
	}
	if c.Cowork.FastLLMBaseDomain != "" {
		fmt.Fprintf(&b, "fast_llm_base_domain = %q   # overrides the base URL for direct /chat/completions calls (scheduler/RAG); empty = built-in default\n", c.Cowork.FastLLMBaseDomain)
	}

	// Mail config. When EmailAccounts is set, it's the source of truth and we
	// render it as [[cowork.email_accounts]] array-of-tables. The legacy single
	// [cowork.smtp]/[cowork.imap] pair is rendered only when there are no
	// named accounts (single-account backward-compat). This is the fix for the
	// long-standing bug where the cowork renderer omitted mail entirely, so
	// SetCoWorkSettings wrote config that config.Load then couldn't read back —
	// every mailbox "save" silently vanished.
	if len(c.Cowork.EmailAccounts) > 0 {
		for _, a := range c.Cowork.EmailAccounts {
			b.WriteString("\n[[cowork.email_accounts]]\n")
			fmt.Fprintf(&b, "name = %q   # stable handle tools/scheduler address\n", a.Name)
			fmt.Fprintf(&b, "default = %v\n", a.Default)
			b.WriteString("[cowork.email_accounts.smtp]\n")
			renderSMTPFields(&b, a.SMTP)
			b.WriteString("[cowork.email_accounts.imap]\n")
			renderIMAPFields(&b, a.IMAP)
		}
	} else {
		// Legacy single-pair form.
		b.WriteString("\n[cowork.smtp]\n")
		renderSMTPFields(&b, c.Cowork.SMTP)
		b.WriteString("[cowork.imap]\n")
		renderIMAPFields(&b, c.Cowork.IMAP)
	}
	b.WriteString("\n")

	renderLSPConfig(&b, c.LSP)

	b.WriteString("[skills]\n")
	if len(c.Skills.Paths) > 0 {
		fmt.Fprintf(&b, "paths = %s   # extra custom skill roots\n", renderStringArray(c.Skills.Paths))
	} else {
		b.WriteString("# paths = [\"~/my-skills\", \"../shared/skills\"]   # extra custom skill roots\n")
	}
	if len(c.Skills.ExcludedPaths) > 0 {
		fmt.Fprintf(&b, "excluded_paths = %s   # skill roots hidden from discovery\n", renderStringArray(c.Skills.ExcludedPaths))
	} else {
		b.WriteString("# excluded_paths = [\"~/.agents/skills\"]   # hide convention roots without deleting folders\n")
	}
	if c.Skills.MaxDepth != 0 {
		fmt.Fprintf(&b, "max_depth = %d   # nested scan depth; default 3, set 1 for legacy root-only discovery\n", c.SkillMaxDepth())
	} else {
		b.WriteString("# max_depth = 3   # nested scan depth; set 1 for legacy root-only discovery\n")
	}
	if disabled := c.DisabledSkillNames(); len(disabled) > 0 {
		fmt.Fprintf(&b, "disabled_skills = %s   # hidden from the prompt, slash invocation, and skill tools\n\n", renderStringArray(disabled))
	} else {
		b.WriteString("# disabled_skills = [\"review\"]   # hide noisy or unwanted skills\n\n")
	}

	b.WriteString("[permissions]\n")
	b.WriteString("# Per-call gating. mode = writer fallback when no rule matches: ask|allow|deny.\n")
	b.WriteString("# Readers always default to allow. Precedence: deny > ask > allow > fallback.\n")
	b.WriteString("# Rules are \"Tool\" or \"Tool(specifier)\"; e.g. Bash(go test:*), Edit(src/**).\n")
	mode := c.Permissions.Mode
	if mode == "" {
		mode = "ask"
	}
	fmt.Fprintf(&b, "mode  = %q\n", mode)
	b.WriteString(renderRuleList("deny", c.Permissions.Deny, `["Bash(rm -rf*)", "Bash(git push*)"]   # hard-blocked in every mode`))
	b.WriteString(renderRuleList("allow", c.Permissions.Allow, `["Bash(go test:*)", "Bash(git status:*)"]   # never prompted`))
	b.WriteString(renderRuleList("ask", c.Permissions.Ask, `["Edit(src/**)"]   # force a prompt even if otherwise allowed`))
	b.WriteString("\n")

	b.WriteString("[sandbox]\n")
	b.WriteString("# Confine tool blast radius. File-writers (write_file/edit_file/multi_edit)\n")
	b.WriteString("# may only write under workspace_root (empty = current dir) + allow_write.\n")
	b.WriteString("# bash = \"enforce\" (default) jails each command in an OS sandbox (macOS now;\n")
	b.WriteString("# graceful fallback elsewhere); \"off\" disables it. network allows egress.\n")
	if c.Sandbox.WorkspaceRoot != "" {
		fmt.Fprintf(&b, "workspace_root = %q\n", c.Sandbox.WorkspaceRoot)
	} else {
		b.WriteString("# workspace_root = \"\"            # default: current working directory\n")
	}
	if len(c.Sandbox.AllowWrite) > 0 {
		fmt.Fprintf(&b, "allow_write = %s\n", renderStringArray(c.Sandbox.AllowWrite))
	} else {
		b.WriteString("# allow_write = [\"/tmp\"]          # extra dirs writers may also modify\n")
	}
	fmt.Fprintf(&b, "bash    = %q\n", c.BashMode())
	fmt.Fprintf(&b, "network = %v\n", c.Sandbox.Network)
	b.WriteString("\n")

	b.WriteString("[statusline]\n")
	b.WriteString("# A custom status line: a command whose first stdout line replaces the built-in\n")
	b.WriteString("# data row. It receives {\"model\",\"contextUsed\",\"contextWindow\",\"cwd\"} as JSON on stdin.\n")
	if c.Statusline.Command != "" {
		fmt.Fprintf(&b, "command = %q\n", c.Statusline.Command)
	} else {
		b.WriteString("# command = \"my-statusline.sh\"\n")
	}
	b.WriteString("\n")

	if shouldRenderDeferred(c, defaults, scope) {
		renderDeferredConfig(&b, c.Deferred)
	}
	if shouldRenderPatrol(c, defaults, scope) {
		renderPatrolConfig(&b, c.Patrol)
	}

	if shouldRenderBot(c, defaults, scope) {
		b.WriteString("# Bot gateway: multi-channel IM bot for QQ, Feishu/Lark, and WeChat.\n")
		b.WriteString("[bot]\n")
		fmt.Fprintf(&b, "enabled = %v\n", c.Bot.Enabled)
		if c.Bot.Model != "" {
			fmt.Fprintf(&b, "model = %q\n", c.Bot.Model)
		} else {
			b.WriteString("# model = \"\"   # empty = default_model\n")
		}
		fmt.Fprintf(&b, "max_steps = %d\n", c.Bot.MaxSteps)
		fmt.Fprintf(&b, "debounce_ms = %d\n", c.Bot.DebounceMs)
		b.WriteString("\n[bot.allowlist]\n")
		fmt.Fprintf(&b, "enabled = %v\n", c.Bot.Allowlist.Enabled)
		fmt.Fprintf(&b, "allow_all = %v\n", c.Bot.Allowlist.AllowAll)
		fmt.Fprintf(&b, "qq_users = %s\n", renderStringArray(c.Bot.Allowlist.QQUsers))
		fmt.Fprintf(&b, "feishu_users = %s\n", renderStringArray(c.Bot.Allowlist.FeishuUsers))
		fmt.Fprintf(&b, "weixin_users = %s\n", renderStringArray(c.Bot.Allowlist.WeixinUsers))
		fmt.Fprintf(&b, "qq_groups = %s\n", renderStringArray(c.Bot.Allowlist.QQGroups))
		fmt.Fprintf(&b, "feishu_groups = %s\n", renderStringArray(c.Bot.Allowlist.FeishuGroups))
		fmt.Fprintf(&b, "weixin_groups = %s\n", renderStringArray(c.Bot.Allowlist.WeixinGroups))
		b.WriteString("\n[bot.qq]\n")
		fmt.Fprintf(&b, "enabled = %v\n", c.Bot.QQ.Enabled)
		fmt.Fprintf(&b, "app_id = %q\n", c.Bot.QQ.AppID)
		fmt.Fprintf(&b, "app_secret_env = %q\n", c.Bot.QQ.AppSecretEnv)
		b.WriteString("\n[bot.feishu]\n")
		fmt.Fprintf(&b, "enabled = %v\n", c.Bot.Feishu.Enabled)
		fmt.Fprintf(&b, "app_id = %q\n", c.Bot.Feishu.AppID)
		fmt.Fprintf(&b, "domain = %q\n", c.Bot.Feishu.Domain)
		fmt.Fprintf(&b, "app_secret_env = %q\n", c.Bot.Feishu.AppSecretEnv)
		fmt.Fprintf(&b, "verification_token = %q\n", c.Bot.Feishu.VerificationToken)
		fmt.Fprintf(&b, "mode = %q\n", c.Bot.Feishu.Mode)
		fmt.Fprintf(&b, "webhook_port = %d\n", c.Bot.Feishu.WebhookPort)
		fmt.Fprintf(&b, "require_mention = %v\n", c.Bot.Feishu.RequireMention)
		b.WriteString("\n[bot.weixin]\n")
		fmt.Fprintf(&b, "enabled = %v\n", c.Bot.Weixin.Enabled)
		fmt.Fprintf(&b, "account_id = %q\n", c.Bot.Weixin.AccountID)
		fmt.Fprintf(&b, "token_env = %q\n", c.Bot.Weixin.TokenEnv)
		fmt.Fprintf(&b, "api_base = %q\n", c.Bot.Weixin.APIBase)
		for _, conn := range c.Bot.Connections {
			b.WriteString("\n[[bot.connections]]\n")
			fmt.Fprintf(&b, "id = %q\n", conn.ID)
			fmt.Fprintf(&b, "provider = %q\n", conn.Provider)
			fmt.Fprintf(&b, "domain = %q\n", conn.Domain)
			fmt.Fprintf(&b, "label = %q\n", conn.Label)
			fmt.Fprintf(&b, "enabled = %v\n", conn.Enabled)
			fmt.Fprintf(&b, "status = %q\n", conn.Status)
			if conn.Model != "" {
				fmt.Fprintf(&b, "model = %q\n", conn.Model)
			}
			if conn.WorkspaceRoot != "" {
				fmt.Fprintf(&b, "workspace_root = %q\n", conn.WorkspaceRoot)
			}
			if conn.LastError != "" {
				fmt.Fprintf(&b, "last_error = %q\n", conn.LastError)
			}
			if conn.CreatedAt != "" {
				fmt.Fprintf(&b, "created_at = %q\n", conn.CreatedAt)
			}
			if conn.UpdatedAt != "" {
				fmt.Fprintf(&b, "updated_at = %q\n", conn.UpdatedAt)
			}
			if parts := renderBotCredential(conn.Credential); parts != "" {
				fmt.Fprintf(&b, "credential = %s\n", parts)
			}
			if len(conn.SessionMappings) > 0 {
				fmt.Fprintf(&b, "session_mappings = %s\n", renderBotSessionMappings(conn.SessionMappings))
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("# External MCP servers. type: \"stdio\" (default, a subprocess) | \"http\" | \"sse\".\n")
	b.WriteString("# ${VAR} / ${VAR:-default} are expanded from the environment in command/args/env/url/headers.\n")
	if len(c.Plugins) == 0 {
		b.WriteString("# [[plugins]]\n")
		b.WriteString("# name    = \"example\"\n")
		b.WriteString("# command = \"fairpeer-plugin-example\"\n")
		b.WriteString("# [[plugins]]                                  # a remote server over Streamable HTTP\n")
		b.WriteString("# name    = \"stripe\"\n")
		b.WriteString("# type    = \"http\"\n")
		b.WriteString("# url     = \"https://mcp.stripe.com\"\n")
		b.WriteString("# headers = { Authorization = \"Bearer ${STRIPE_KEY}\" }\n")
	} else {
		for _, pl := range c.Plugins {
			b.WriteString("\n[[plugins]]\n")
			fmt.Fprintf(&b, "name    = %q\n", pl.Name)
			if pl.Type != "" {
				fmt.Fprintf(&b, "type    = %q\n", pl.Type)
			}
			if pl.Command != "" {
				fmt.Fprintf(&b, "command = %q\n", pl.Command)
			}
			if len(pl.Args) > 0 {
				fmt.Fprintf(&b, "args    = %s\n", renderStringArray(pl.Args))
			}
			if pl.URL != "" {
				fmt.Fprintf(&b, "url     = %q\n", pl.URL)
			}
			if len(pl.Headers) > 0 {
				fmt.Fprintf(&b, "headers = %s\n", renderStringMap(pl.Headers))
			}
			if len(pl.Env) > 0 {
				fmt.Fprintf(&b, "env     = %s\n", renderStringMap(pl.Env))
			}
			if pl.AutoStart != nil {
				fmt.Fprintf(&b, "auto_start = %v\n", *pl.AutoStart)
			}
		}
	}

	return b.String()
}

func configVersion(c *Config) int {
	if c != nil && c.ConfigVersion > 0 {
		return c.ConfigVersion
	}
	return Default().ConfigVersion
}

func shouldRenderUI(c, defaults *Config, scope RenderScope) bool {
	if scope != RenderScopeProject {
		return true
	}
	return !reflect.DeepEqual(c.UI, defaults.UI)
}

func shouldRenderNetwork(c, defaults *Config, scope RenderScope) bool {
	if scope != RenderScopeProject {
		return true
	}
	return !reflect.DeepEqual(c.Network, defaults.Network)
}

func shouldRenderProviders(c, defaults *Config, scope RenderScope) bool {
	if scope != RenderScopeProject {
		return true
	}
	return !reflect.DeepEqual(c.Providers, defaults.Providers)
}

func shouldRenderBot(c, defaults *Config, scope RenderScope) bool {
	if scope != RenderScopeProject {
		return true
	}
	return !reflect.DeepEqual(c.Bot, defaults.Bot)
}

func shouldRenderSystemPrompt(c, defaults *Config, scope RenderScope) bool {
	if scope == RenderScopeFull {
		return true
	}
	return strings.TrimSpace(c.Agent.SystemPrompt) != "" && c.Agent.SystemPrompt != defaults.Agent.SystemPrompt
}

// shouldRenderDeferred and shouldRenderPatrol follow the same rule as the other
// gated sections: always emit for a user/full render (where the settings panel
// saves, and where a missing section would silently drop the user's choice),
// but only emit into a project file when it actually differs from the defaults.
// These sections ARE round-tripped through RenderTOMLForScope, so forgetting to
// render one erases it on the next save — they are not optional decoration.
func shouldRenderDeferred(c, defaults *Config, scope RenderScope) bool {
	if scope != RenderScopeProject {
		return true
	}
	return !reflect.DeepEqual(c.Deferred, defaults.Deferred)
}

func shouldRenderPatrol(c, defaults *Config, scope RenderScope) bool {
	if scope != RenderScopeProject {
		return true
	}
	return !reflect.DeepEqual(c.Patrol, defaults.Patrol)
}

// writeOptBool writes "key = <value>" when the pointer is set, and the
// commented default when it is nil, so an unset knob stays visible and
// documented without pinning the value.
func writeOptBool(b *strings.Builder, key string, v *bool, def bool) {
	if v != nil {
		fmt.Fprintf(b, "%s = %v\n", key, *v)
		return
	}
	fmt.Fprintf(b, "# %s = %v\n", key, def)
}

// renderDeferredConfig emits [deferred]: pushing a finished background job's
// actual result into the session. See config.DeferredConfig and internal/deferred.
func renderDeferredConfig(b *strings.Builder, cfg DeferredConfig) {
	b.WriteString("[deferred]\n")
	b.WriteString("# Push a finished background job's ACTUAL result into its session the moment it\n")
	b.WriteString("# exists, instead of waiting for the next turn to drain a one-line note. The\n")
	b.WriteString("# result is always carried into the next turn's context; these knobs decide\n")
	b.WriteString("# whether it may also start its own turn (costs tokens) and whether it raises\n")
	b.WriteString("# an immediate notice.\n")
	writeOptBool(b, "enabled", cfg.Enabled, true)
	writeOptBool(b, "trigger_parent_turn", cfg.TriggerParentTurn, false)
	writeOptBool(b, "notify_on_failure", cfg.NotifyOnFailure, true)
	writeOptBool(b, "notify_on_success", cfg.NotifyOnSuccess, false)
	if cfg.RetryIntervalSec > 0 {
		fmt.Fprintf(b, "retry_interval_sec = %d\n", cfg.RetryIntervalSec)
	} else {
		fmt.Fprintf(b, "# retry_interval_sec = %d   # how often an undelivered result is retried\n", DefaultDeferredRetryIntervalSec)
	}
	if cfg.MaxAttempts > 0 {
		fmt.Fprintf(b, "max_attempts = %d\n", cfg.MaxAttempts)
	} else {
		fmt.Fprintf(b, "# max_attempts = %d   # give up (and suppress) after this many tries\n", DefaultDeferredMaxAttempts)
	}
	if cfg.BodyLimitBytes > 0 {
		fmt.Fprintf(b, "body_limit_bytes = %d\n", cfg.BodyLimitBytes)
	} else {
		fmt.Fprintf(b, "# body_limit_bytes = %d\n", DefaultDeferredBodyLimitBytes)
	}
	b.WriteString("\n")
}

// renderPatrolConfig emits [patrol]: the proactive inspection heartbeat. The
// permission dial is mode x allow_write; both default to the conservative
// choice (off, and report-only even when on). See config.PatrolConfig.
func renderPatrolConfig(b *strings.Builder, cfg PatrolConfig) {
	b.WriteString("[patrol]\n")
	b.WriteString("# Proactive inspection heartbeat: while you are away, the session periodically\n")
	b.WriteString("# inspects its workspace (git state, unresolved TODO/FIXME markers) and reports\n")
	b.WriteString("# what changed, through the same path a finished background job uses.\n")
	b.WriteString("#\n")
	b.WriteString("# Permission dial (the normal tool-permission gate still applies on top):\n")
	b.WriteString("#   mode = off        never inspects (default)\n")
	b.WriteString("#   mode = readonly   inspects and reports; never writes, never spends a turn\n")
	b.WriteString("#   mode = assist     a fresh warn/high signal may start its own turn\n")
	b.WriteString("#   allow_write       only meaningful under assist: may that turn modify files?\n")
	writeOptBool(b, "enabled", cfg.Enabled, false)
	fmt.Fprintf(b, "mode = %q\n", cfg.ModeEffective())
	writeOptBool(b, "allow_write", cfg.AllowWrite, false)
	if cfg.IntervalSec > 0 {
		fmt.Fprintf(b, "interval_sec = %d\n", cfg.IntervalSec)
	} else {
		fmt.Fprintf(b, "# interval_sec = %d   # 15 minutes\n", DefaultPatrolIntervalSec)
	}
	if len(cfg.Checks) > 0 {
		fmt.Fprintf(b, "checks = %s\n", renderStringArray(cfg.Checks))
	} else {
		b.WriteString("# checks = [\"git\", \"markers\"]   # available: git, markers\n")
	}
	if cfg.BodyLimitBytes > 0 {
		fmt.Fprintf(b, "body_limit_bytes = %d\n", cfg.BodyLimitBytes)
	} else {
		fmt.Fprintf(b, "# body_limit_bytes = %d\n", DefaultPatrolBodyLimitBytes)
	}
	b.WriteString("\n")
}

func renderLSPConfig(b *strings.Builder, cfg LSPConfig) {
	b.WriteString("[lsp]\n")
	fmt.Fprintf(b, "enabled = %v   # language server tools; servers launch lazily when used\n", cfg.Enabled)
	if len(cfg.Servers) == 0 {
		b.WriteString("# [lsp.servers.go]\n")
		b.WriteString("# command = \"gopls\"\n")
		b.WriteString("# args = []\n")
		b.WriteString("# extensions = [\".go\"]\n\n")
		return
	}
	b.WriteString("\n")

	langs := make([]string, 0, len(cfg.Servers))
	for lang := range cfg.Servers {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	for _, lang := range langs {
		srv := cfg.Servers[lang]
		fmt.Fprintf(b, "[lsp.servers.%s]\n", renderTOMLKeyPart(lang))
		if srv.Command != "" {
			fmt.Fprintf(b, "command = %q\n", srv.Command)
		}
		if len(srv.Args) > 0 {
			fmt.Fprintf(b, "args = %s\n", renderStringArray(srv.Args))
		}
		if len(srv.Env) > 0 {
			fmt.Fprintf(b, "env = %s\n", renderStringMap(srv.Env))
		}
		if srv.LanguageID != "" {
			fmt.Fprintf(b, "language_id = %q\n", srv.LanguageID)
		}
		if len(srv.Extensions) > 0 {
			fmt.Fprintf(b, "extensions = %s\n", renderStringArray(srv.Extensions))
		}
		if srv.InstallHint != "" {
			fmt.Fprintf(b, "install_hint = %q\n", srv.InstallHint)
		}
		b.WriteString("\n")
	}
}

func renderTOMLKeyPart(key string) string {
	if isBareTOMLKey(key) {
		return key
	}
	return strconv.Quote(key)
}

func isBareTOMLKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// renderStringArray renders a []string as a TOML inline array.
func renderStringArray(ss []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, s := range ss {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", s)
	}
	b.WriteByte(']')
	return b.String()
}

// renderSMTPFields writes the SMTP config fields into a TOML table that the
// caller has already opened (e.g. "[cowork.smtp]" or
// "[cowork.email_accounts.smtp]"). password_env holds the env var NAME only —
// the secret itself lives in the encrypted store, never in config.toml.
func renderSMTPFields(b *strings.Builder, s SMTPConfig) {
	if s.Host != "" {
		fmt.Fprintf(b, "host = %q\n", s.Host)
	} else {
		b.WriteString("# host = \"smtp.example.com\"\n")
	}
	if s.Port != 0 {
		fmt.Fprintf(b, "port = %d   # 465 implicit TLS, 587 STARTTLS, 25 plain\n", s.Port)
	} else {
		b.WriteString("# port = 465   # 465 implicit TLS, 587 STARTTLS, 25 plain\n")
	}
	if s.From != "" {
		fmt.Fprintf(b, "from = %q   # sender address\n", s.From)
	} else {
		b.WriteString("# from = \"you@example.com\"   # sender address\n")
	}
	if s.Username != "" {
		fmt.Fprintf(b, "username = %q\n", s.Username)
	} else {
		b.WriteString("# username = \"you@example.com\"\n")
	}
	if s.PasswordEnv != "" {
		fmt.Fprintf(b, "password_env = %q   # env var name holding the password (not the password itself)\n", s.PasswordEnv)
	} else {
		b.WriteString("# password_env = \"SMTP_PASS\"   # env var name holding the password (not the password itself)\n")
	}
	if s.EncryptionMode != "" {
		fmt.Fprintf(b, "encryption_mode = %q   # tls | starttls | none\n", s.EncryptionMode)
	} else if s.UseTLS {
		b.WriteString("use_tls = true   # deprecated: prefer encryption_mode\n")
	} else {
		b.WriteString("# use_tls = true   # enable implicit SSL (required by most 465 ports)\n")
	}
}

// renderIMAPFields writes the IMAP config fields into an already-opened TOML
// table. Like SMTP, only the password_env name is persisted.
func renderIMAPFields(b *strings.Builder, m IMAPConfig) {
	if m.Host != "" {
		fmt.Fprintf(b, "host = %q\n", m.Host)
	} else {
		b.WriteString("# host = \"imap.example.com\"\n")
	}
	if m.Port != 0 {
		fmt.Fprintf(b, "port = %d   # 993 implicit TLS, 143 STARTTLS/plain\n", m.Port)
	} else {
		b.WriteString("# port = 993   # 993 implicit TLS, 143 STARTTLS/plain\n")
	}
	if m.Username != "" {
		fmt.Fprintf(b, "username = %q\n", m.Username)
	} else {
		b.WriteString("# username = \"you@example.com\"\n")
	}
	if m.PasswordEnv != "" {
		fmt.Fprintf(b, "password_env = %q   # env var name holding the password\n", m.PasswordEnv)
	} else {
		b.WriteString("# password_env = \"IMAP_PASS\"\n")
	}
	if m.SkipTLSVerify {
		fmt.Fprintf(b, "skip_tls_verify = %v   # skip cert verification (self-signed/corporate CAs)\n", m.SkipTLSVerify)
	} else {
		b.WriteString("# skip_tls_verify = false   # skip cert verification (self-signed/corporate CAs)\n")
	}
}

// renderStringMap renders a map[string]string as a TOML inline table with keys
// in sorted order so output is deterministic (round-trips cleanly).
func renderStringMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("{ ")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s = %q", k, m[k])
	}
	b.WriteString(" }")
	return b.String()
}

func renderBotCredential(cred BotConnectionCredential) string {
	parts := make(map[string]string)
	if cred.AppID != "" {
		parts["app_id"] = cred.AppID
	}
	if cred.AppSecretEnv != "" {
		parts["app_secret_env"] = cred.AppSecretEnv
	}
	if cred.AccountID != "" {
		parts["account_id"] = cred.AccountID
	}
	if cred.TokenEnv != "" {
		parts["token_env"] = cred.TokenEnv
	}
	if len(parts) == 0 {
		return ""
	}
	return renderStringMap(parts)
}

func renderBotSessionMappings(mappings []BotConnectionSessionMapping) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, mapping := range mappings {
		if i > 0 {
			b.WriteString(", ")
		}
		parts := map[string]string{
			"remote_id":  mapping.RemoteID,
			"session_id": mapping.SessionID,
		}
		if mapping.Scope != "" {
			parts["scope"] = mapping.Scope
		}
		if mapping.WorkspaceRoot != "" {
			parts["workspace_root"] = mapping.WorkspaceRoot
		}
		if mapping.UpdatedAt != "" {
			parts["updated_at"] = mapping.UpdatedAt
		}
		b.WriteString(renderStringMap(parts))
	}
	b.WriteByte(']')
	return b.String()
}

// renderRuleList emits a permission rule list. A populated list renders as an
// active TOML array; an empty one renders as a commented example so `fairpeer setup`
// scaffolds discoverable guidance without imposing surprising rules.
func renderRuleList(key string, rules []string, example string) string {
	if len(rules) == 0 {
		return fmt.Sprintf("# %s = %s\n", key, example)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s = [", key)
	for i, r := range rules {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", r)
	}
	b.WriteString("]\n")
	return b.String()
}

// formatFloat ensures a float renders with a decimal point so TOML types it as a
// float, not an integer (e.g. 0 -> "0.0").
func formatFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}
