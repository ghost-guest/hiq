package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	skillassets "github.com/zzycxz/hiq/internal/assets"
	"github.com/zzycxz/hiq/internal/browserlaunch"
	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/ppttemplate"
	"github.com/zzycxz/hiq/internal/secret"
	"github.com/zzycxz/hiq/internal/tool/builtin"
)

// CoWorkSettingsView is the settings-panel view of the coWork profile config.
// It mirrors config.CoworkConfig but presents secrets (SMTP/IMAP passwords) as
// plain values the user types — they're stored in a hiq-managed .env file
// (loaded at startup), NOT in config.toml. The *_env fields in config point at
// env var names; this view resolves them to/from the .env so the user never
// touches environment variables manually.
type CoWorkSettingsView struct {
	BrowserPath    string `json:"browserPath"` // Chromium browser exe; "" = auto-detect
	EmbeddingModel string `json:"embeddingModel"`
	// BrowserAttachURL makes browser automation attach to an already-running
	// debug-enabled browser (the managed 可控浏览器) instead of launching a
	// fresh one per task. Empty = launch mode.
	BrowserAttachURL string `json:"browserAttachURL"`
	// RAGEnabled is the knowledge-base master switch. nil = enabled (default);
	// explicit false = fully disabled (no auto-injection, no rag_* tools, expert
	// teams skip KB context). Mirrors [cowork] rag_enabled. Distinct from
	// EmbeddingModel (which only toggles semantic reranking on top of FTS5).
	RAGEnabled *bool `json:"ragEnabled"`
	// PPTActiveTemplate is the id of the active PPT template (or "" for none).
	// PPTTemplates is the read-only list of available templates for the dropdown.
	// PPTTemplateDir is the absolute path to the templates dir, shown so the user
	// knows where to drop JSON files.
	PPTActiveTemplate string             `json:"pptActiveTemplate"`
	PPTTemplates      []ppttemplate.View `json:"pptTemplates"`
	PPTTemplateDir    string             `json:"pptTemplateDir"`
	// PPTMode controls the PPT generation quality mode:
	// "fast" = one-shot generation, no rework (default, reliable)
	// "validate" = generate + check + rework up to 3 rounds (higher quality)
	PPTMode string       `json:"pptMode"`
	SMTP    SMTPSettings `json:"smtp"`
	IMAP    IMAPSettings `json:"imap"`
	// SMTPPassword/IMAPPassword are WRITE-ONLY: the user types a new password
	// here when changing it. On load they are ALWAYS empty — the panel never
	// receives the stored secret. On save, a non-empty value updates the
	// encrypted store; an empty value leaves the existing password untouched
	// ("leave blank to keep"). The set/unset state is reported via the *Set
	// booleans below so the panel can mark the field without holding the value.
	SMTPPassword string `json:"smtpPassword"`
	IMAPPassword string `json:"imapPassword"`
	// SMTPPasswordSet/IMAPPasswordSet report whether an encrypted secret is
	// stored for the configured password_env. True = "已设置", false = "未设置".
	SMTPPasswordSet bool `json:"smtpPasswordSet"`
	IMAPPasswordSet bool `json:"imapPasswordSet"`
	// DetectedBrowser is read-only diagnostic: which browser auto-detection
	// found (e.g. "Chrome"), so the panel can show "auto-detected: Chrome" and
	// let the user skip manual entry. "" = nothing detected.
	DetectedBrowser string `json:"detectedBrowser"`
	// Screenshot fields: global-hotkey screenshot-to-VLM feature. Disabled by
	// default; the user opts in via the cowork settings tab.
	ScreenshotEnabled  bool   `json:"screenshotEnabled"`
	ScreenshotHotkey   string `json:"screenshotHotkey"`
	ScreenshotVLMModel string `json:"screenshotVlmModel"`
	ScreenshotPrompt   string `json:"screenshotPrompt"`
	// VoiceModel is the provider/model ref for speech-to-text (voice input +
	// audio attachment understanding), e.g. "mimo/mimo-v2.5-asr". Empty = voice
	// input disabled. Mirrors [cowork] voice_model.
	VoiceModel string `json:"voiceModel"`
	// EStopHotkey is the global emergency-stop combo for desktop automation.
	EStopHotkey string `json:"estopHotkey"`
	// EmailAccounts is the multi-mailbox list. When non-empty it is the source of
	// truth on save (full overwrite); the legacy SMTP/IMAP single-pair fields
	// above are kept as a backward-compat mirror of the Default account so older
	// frontends and the legacy probe path still work.
	EmailAccounts []EmailAccountView `json:"emailAccounts"`
	// AllowHeadlessEmail, when true, adds email_send to permissions.Allow so
	// scheduled tasks can send email in headless mode (no tab = no interactive
	// user to approve). Surfaced as a checkbox in the mail settings card.
	AllowHeadlessEmail bool `json:"allowHeadlessEmail"`
}

// EmailAccountView is one mailbox in the multi-account list. Passwords are
// write-only (Password holds a freshly-typed value on save; the stored secret
// is never echoed back). PasswordSet reports whether a secret is stored.
type EmailAccountView struct {
	Name        string       `json:"name"`    // stable handle tools/scheduler address
	Default     bool         `json:"default"` // exactly one should be true
	SMTP        SMTPSettings `json:"smtp"`
	IMAP        IMAPSettings `json:"imap"`
	Password    string       `json:"password"`    // SMTP/IMAP share one password (write-only)
	PasswordSet bool         `json:"passwordSet"` // reports stored secret presence
}

// SMTPSettings mirrors config.SMTPConfig minus the password (which lives in
// CoWorkSettingsView.SMTPPassword / the .env).
type SMTPSettings struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	From           string `json:"from"`
	Username       string `json:"username"`
	PasswordEnv    string `json:"passwordEnv"` // env var name holding the password
	UseTLS         bool   `json:"useTLS"`
	EncryptionMode string `json:"encryptionMode"` // "tls" (465) | "starttls" (587) | "none" (25)
}

// IMAPSettings mirrors config.IMAPConfig minus the password.
type IMAPSettings struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	PasswordEnv string `json:"passwordEnv"`
}

// coworkSettingsView builds the view from config + the .env file. Passwords are
// resolved from the .env so the panel shows their set/unset state (masked in UI).
func coworkSettingsView(c config.CoworkConfig) CoWorkSettingsView {
	env := loadCoworkEnv()
	// Load PPT templates from the skill's template directory, falling back to
	// the user-config ppt-templates dir when the skill dir doesn't exist.
	skillTplDir := ppttemplate.SkillTemplatesDir()
	if skillTplDir == "" {
		skillTplDir = ppttemplate.DefaultDir()
	}
	templates := scanPPTXTemplates(skillTplDir)

	// Resolve SMTP/IMAP from EmailAccounts (preferred) or legacy fields.
	smtp := c.SMTP
	imap := c.IMAP
	if a, ok := c.DefaultEmailAccount(); ok {
		smtp = a.SMTP
		imap = a.IMAP
	}

	v := CoWorkSettingsView{
		BrowserPath:        c.BrowserPath,
		BrowserAttachURL:   c.BrowserAttachURL,
		EmbeddingModel:     c.EmbeddingModel,
		RAGEnabled:         c.RAGEnabled,
		PPTActiveTemplate:  c.PPTActiveTemplate,
		PPTTemplates:       templates,
		PPTTemplateDir:     skillTplDir,
		PPTMode:            c.PPTMode,
		ScreenshotEnabled:  c.ScreenshotEnabled,
		ScreenshotHotkey:   c.ScreenshotHotkey,
		ScreenshotVLMModel: c.ScreenshotVLMModel,
		ScreenshotPrompt:   c.ScreenshotPrompt,
		VoiceModel:         c.VoiceModel,
		EStopHotkey:        c.EStopHotkey,
		SMTP: SMTPSettings{
			Host:           smtp.Host,
			Port:           smtp.Port,
			From:           smtp.From,
			Username:       smtp.Username,
			PasswordEnv:    smtp.PasswordEnv,
			UseTLS:         smtp.UseTLS,
			EncryptionMode: smtp.EncryptionMode,
		},
		IMAP: IMAPSettings{
			Host:        imap.Host,
			Port:        imap.Port,
			Username:    imap.Username,
			PasswordEnv: imap.PasswordEnv,
		},
	}
	if v.PPTMode == "" {
		v.PPTMode = "fast"
	}
	// Report set/unset WITHOUT ever returning the secret value. Check the
	// encrypted store first, then the legacy cowork.env as a pre-migration
	// fallback (so the panel still shows "已设置" right after upgrade, before
	// the first startup migration runs).
	if smtp.PasswordEnv != "" {
		v.SMTPPasswordSet = secretIsSet(smtp.PasswordEnv, env)
	}
	if imap.PasswordEnv != "" {
		v.IMAPPasswordSet = secretIsSet(imap.PasswordEnv, env)
	}
	// Project the multi-account list. Each account's password_env is derived
	// from its name so accounts don't collide in the secret store. PasswordSet
	// reflects whichever of SMTP/IMAP env has a stored secret.
	if len(c.EmailAccounts) > 0 {
		v.EmailAccounts = make([]EmailAccountView, 0, len(c.EmailAccounts))
		for _, a := range c.EmailAccounts {
			smtpEnv, imapEnv := accountPasswordEnvs(a.Name)
			asmtp := a.SMTP
			aimap := a.IMAP
			if asmtp.PasswordEnv == "" {
				asmtp.PasswordEnv = smtpEnv
			}
			if aimap.PasswordEnv == "" {
				aimap.PasswordEnv = imapEnv
			}
			pwdSet := secretIsSet(asmtp.PasswordEnv, env) || secretIsSet(aimap.PasswordEnv, env)
			v.EmailAccounts = append(v.EmailAccounts, EmailAccountView{
				Name:    a.Name,
				Default: a.Default,
				SMTP: SMTPSettings{
					Host:           asmtp.Host,
					Port:           asmtp.Port,
					From:           asmtp.From,
					Username:       asmtp.Username,
					PasswordEnv:    asmtp.PasswordEnv,
					UseTLS:         asmtp.UseTLS,
					EncryptionMode: asmtp.EncryptionMode,
				},
				IMAP: IMAPSettings{
					Host:        aimap.Host,
					Port:        aimap.Port,
					Username:    aimap.Username,
					PasswordEnv: aimap.PasswordEnv,
				},
				PasswordSet: pwdSet,
			})
		}
	}
	return v
}

// accountPasswordEnvs returns the stable SMTP/IMAP env-var names for a named
// account. The default account ("primary" or empty-named) reuses the legacy
// COWORK_SMTP_PASSWORD / COWORK_IMAP_PASSWORD names so existing secrets keep
// working; named accounts get COWORK_SMTP_PASSWORD_<name> to avoid collisions.
func accountPasswordEnvs(name string) (smtpEnv, imapEnv string) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "primary") {
		return "COWORK_SMTP_PASSWORD", "COWORK_IMAP_PASSWORD"
	}
	upper := strings.ToUpper(strings.ReplaceAll(name, " ", "_"))
	return "COWORK_SMTP_PASSWORD_" + upper, "COWORK_IMAP_PASSWORD_" + upper
}

// scanPPTXTemplates scans a directory for template files (.json specs and .pptx
// files) and returns them as template views for the settings dropdown.
func scanPPTXTemplates(dir string) []ppttemplate.View {
	if dir == "" {
		return []ppttemplate.View{}
	}
	return ppttemplate.Views(dir)
}

// updatePPTSkillConfig updates a single key in the PPT skill's template_config.json.
func (a *App) updatePPTSkillConfig(key, value string) error {
	// Prefer the released embedded skill's config (~/.hiq/skills/ppt-auto),
	// then fall back to the legacy exe-sibling layout for older installs.
	configPath := skillassets.PPTAutoConfigPath()
	if configPath == "" {
		exePath, _ := os.Executable()
		exeDir := filepath.Dir(exePath)
		configPath = filepath.Join(exeDir, ".hiq", "skills", "ppt-auto", "template_config.json")
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil // config not found, skip silently
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read ppt config: %w", err)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse ppt config: %w", err)
	}

	cfg[key] = value

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ppt config: %w", err)
	}

	return os.WriteFile(configPath, out, 0644)
}

// SetCoWorkSettings persists the coWork settings: non-secret fields to
// config.toml via the edit pipeline, secret fields (SMTP/IMAP passwords) to the
// hiq-managed .env. The password_env names are auto-assigned when empty so
// the user doesn't have to invent env var names.
func (a *App) SetCoWorkSettings(v CoWorkSettingsView) (err error) {
	// Recover from panic so a bug in the save path returns an error to the
	// frontend instead of crashing the Wails backend (which kills the UI).
	defer func() {
		if r := recover(); r != nil {
			slog.Error("SetCoWorkSettings panic", "panic", r)
			err = fmt.Errorf("保存设置时发生内部错误：%v", r)
		}
	}()
	// Diagnostic log (Debug level — not emitted by default). Intentionally
	// excludes the email address and password: those are personal data. Only
	// structural facts (account count, server host, port, whether a password is
	// set) are logged, enough to trace a save failure without leaking PII.
	slog.Debug("SetCoWorkSettings", "emailAccounts", len(v.EmailAccounts))
	for i, ac := range v.EmailAccounts {
		slog.Debug("SetCoWorkSettings account", "idx", i, "name", ac.Name, "default", ac.Default,
			"smtpHost", ac.SMTP.Host, "smtpPort", ac.SMTP.Port,
			"imapHost", ac.IMAP.Host, "imapPort", ac.IMAP.Port,
			"pwdSet", ac.PasswordSet, "encMode", ac.SMTP.EncryptionMode)
	}
	// Assign default password_env names when unset, so .env keys are stable.
	if v.SMTP.PasswordEnv == "" {
		v.SMTP.PasswordEnv = "COWORK_SMTP_PASSWORD"
	}
	if v.IMAP.PasswordEnv == "" {
		v.IMAP.PasswordEnv = "COWORK_IMAP_PASSWORD"
	}

	// Persist secrets to the encrypted store. A blank password means "leave
	// the existing one untouched" (standard password-field UX), so we only
	// write when the user typed something. The live process env is updated too
	// so the change takes effect without an app restart — the tools read via
	// os.Getenv(passwordEnv), which is the in-memory decrypted view.
	store := secret.Default()
	// Legacy single-pair password write (kept for backward compat with the
	// collapsed SMTPPassword/IMAPPassword fields).
	if pwd := strings.TrimSpace(v.SMTPPassword); pwd != "" {
		if err := store.Set(v.SMTP.PasswordEnv, pwd); err != nil {
			return fmt.Errorf("save smtp secret: %w", err)
		}
		os.Setenv(v.SMTP.PasswordEnv, pwd)
	}
	if pwd := strings.TrimSpace(v.IMAPPassword); pwd != "" {
		if err := store.Set(v.IMAP.PasswordEnv, pwd); err != nil {
			return fmt.Errorf("save imap secret: %w", err)
		}
		os.Setenv(v.IMAP.PasswordEnv, pwd)
	}
	// Multi-account passwords. Each account writes to its own env name so
	// accounts don't overwrite each other's secret.
	for i := range v.EmailAccounts {
		av := &v.EmailAccounts[i]
		pwd := strings.TrimSpace(av.Password)
		if pwd == "" {
			continue // leave existing untouched
		}
		smtpEnv, imapEnv := accountPasswordEnvs(av.Name)
		av.SMTP.PasswordEnv = smtpEnv
		av.IMAP.PasswordEnv = imapEnv
		if err := store.Set(smtpEnv, pwd); err != nil {
			return fmt.Errorf("save smtp secret for %q: %w", av.Name, err)
		}
		if err := store.Set(imapEnv, pwd); err != nil {
			return fmt.Errorf("save imap secret for %q: %w", av.Name, err)
		}
		os.Setenv(smtpEnv, pwd)
		os.Setenv(imapEnv, pwd)
	}

	// Persist non-secret config via the standard edit pipeline.
	if err := a.applyConfigOnly(func(c *config.Config) error {
		c.Cowork.BrowserPath = strings.TrimSpace(v.BrowserPath)
		c.Cowork.BrowserAttachURL = browserlaunch.NormalizeCDPEndpoint(strings.TrimSpace(v.BrowserAttachURL))
		c.Cowork.EmbeddingModel = strings.TrimSpace(v.EmbeddingModel)
		// Knowledge-base master switch. The front-end always sends an explicit
		// bool from its toggle, so we copy the pointer through; nil stays nil
		// only when the panel never rendered the field (older clients).
		c.Cowork.RAGEnabled = v.RAGEnabled
		c.Cowork.PPTActiveTemplate = strings.TrimSpace(v.PPTActiveTemplate)
		c.Cowork.PPTMode = strings.TrimSpace(v.PPTMode)
		// PPT template ↔ ppt-auto skill linkage: clearing the template disables
		// the ppt-auto skill, while setting a template enables it. This keeps the
		// Office UI toggle in sync with the Capabilities panel toggle.
		if strings.TrimSpace(v.PPTActiveTemplate) == "" {
			c.SetSkillEnabled("ppt-auto", false)
		} else {
			c.SetSkillEnabled("ppt-auto", true)
		}
		c.Cowork.ScreenshotEnabled = v.ScreenshotEnabled
		c.Cowork.ScreenshotHotkey = strings.TrimSpace(v.ScreenshotHotkey)
		c.Cowork.ScreenshotVLMModel = strings.TrimSpace(v.ScreenshotVLMModel)
		c.Cowork.ScreenshotPrompt = v.ScreenshotPrompt
		c.Cowork.VoiceModel = strings.TrimSpace(v.VoiceModel)
		c.Cowork.EStopHotkey = strings.TrimSpace(v.EStopHotkey)
		smtp := config.SMTPConfig{
			Host:           strings.TrimSpace(v.SMTP.Host),
			Port:           v.SMTP.Port,
			From:           strings.TrimSpace(v.SMTP.From),
			Username:       strings.TrimSpace(v.SMTP.Username),
			PasswordEnv:    v.SMTP.PasswordEnv,
			UseTLS:         v.SMTP.UseTLS,
			EncryptionMode: strings.TrimSpace(v.SMTP.EncryptionMode),
		}
		imap := config.IMAPConfig{
			Host:        strings.TrimSpace(v.IMAP.Host),
			Port:        v.IMAP.Port,
			Username:    strings.TrimSpace(v.IMAP.Username),
			PasswordEnv: v.IMAP.PasswordEnv,
		}
		c.Cowork.SMTP = smtp
		c.Cowork.IMAP = imap
		// Multi-account path: when the frontend submits a full account list,
		// it becomes the source of truth. We first clean up secrets for any
		// accounts that are being removed, then overwrite the slice. The legacy
		// single-pair fields above are mirrored onto the Default account by
		// normalizeEmailAccounts on the next Load.
		if len(v.EmailAccounts) > 0 {
			// Build the set of surviving account names; drop secrets for the
			// rest so we don't leak stale credentials in the encrypted store.
			surviving := make(map[string]bool, len(v.EmailAccounts))
			next := make([]config.EmailAccount, 0, len(v.EmailAccounts))
			for _, av := range v.EmailAccounts {
				// Skip empty accounts: a half-filled card (user clicked "new"
				// but didn't enter a host) must not be persisted — it would
				// create a ghost entry with only password_env, which then shows
				// as a second blank "primary" and can confuse DefaultEmailAccount.
				if strings.TrimSpace(av.SMTP.Host) == "" && strings.TrimSpace(av.IMAP.Host) == "" {
					continue
				}
				name := strings.TrimSpace(av.Name)
				if name == "" {
					name = "primary"
				}
				surviving[strings.ToLower(name)] = true
				smtpEnv, imapEnv := accountPasswordEnvs(name)
				next = append(next, config.EmailAccount{
					Name:    name,
					Default: av.Default,
					SMTP: config.SMTPConfig{
						Host:           strings.TrimSpace(av.SMTP.Host),
						Port:           av.SMTP.Port,
						From:           strings.TrimSpace(av.SMTP.From),
						Username:       strings.TrimSpace(av.SMTP.Username),
						PasswordEnv:    smtpEnv,
						UseTLS:         av.SMTP.UseTLS,
						EncryptionMode: strings.TrimSpace(av.SMTP.EncryptionMode),
					},
					IMAP: config.IMAPConfig{
						Host:        strings.TrimSpace(av.IMAP.Host),
						Port:        av.IMAP.Port,
						Username:    strings.TrimSpace(av.IMAP.Username),
						PasswordEnv: imapEnv,
					},
				})
			}
			// Ensure exactly one Default so EmailAccountByName("") resolves.
			hasDefault := false
			for i := range next {
				if next[i].Default {
					if hasDefault {
						next[i].Default = false
					}
					hasDefault = true
				}
			}
			if !hasDefault && len(next) > 0 {
				next[0].Default = true
			}
			// Clean secrets of removed accounts (fire-and-forget; failure is
			// non-fatal — a leftover secret is just unused, not a correctness
			// issue).
			for _, old := range c.Cowork.EmailAccounts {
				if !surviving[strings.ToLower(strings.TrimSpace(old.Name))] {
					smtpEnv, imapEnv := accountPasswordEnvs(old.Name)
					_ = store.Delete(smtpEnv)
					_ = store.Delete(imapEnv)
				}
			}
			c.Cowork.EmailAccounts = next
		} else if len(c.Cowork.EmailAccounts) > 0 {
			// Legacy single-pair edit on an existing multi-account config:
			// update the default account only (preserves other accounts).
			for i := range c.Cowork.EmailAccounts {
				if c.Cowork.EmailAccounts[i].Default {
					c.Cowork.EmailAccounts[i].SMTP = smtp
					c.Cowork.EmailAccounts[i].IMAP = imap
					break
				}
			}
		} else if smtp.Host != "" || imap.Host != "" {
			c.Cowork.EmailAccounts = []config.EmailAccount{{
				Name:    "primary",
				Default: true,
				SMTP:    smtp,
				IMAP:    imap,
			}}
		}
		// Toggle email_send in permissions.Allow based on the user's checkbox.
		// When ON, scheduled tasks can send email in headless mode (no tab open
		// = no interactive approver). When OFF, email_send falls back to the
		// default Ask rule, which headless denies. We add/remove only the bare
		// "email_send" tool name, leaving any subject-scoped rules untouched.
		c.Permissions.Allow = togglePermissionRule(c.Permissions.Allow, "email_send", v.AllowHeadlessEmail)
		return nil
	}); err != nil {
		return err
	}

	// Refresh the in-memory email accounts cache so the email tools see the
	// updated config without requiring a restart.
	if freshCfg, err := config.Load(); err == nil {
		builtin.SetEmailAccounts(freshCfg.Cowork.EmailAccounts)
		// Hot-reload the voice model so the mic button picks up the new STT
		// model without a restart (boot.Build would otherwise only re-inject
		// on profile switch).
		builtin.SetVoiceModel(freshCfg.Cowork.VoiceModel)
		slog.Info("SetCoWorkSettings done", "savedAccounts", len(freshCfg.Cowork.EmailAccounts))
	} else {
		slog.Warn("SetCoWorkSettings: reload config failed", "err", err)
	}

	// Also update template_config.json so the PPT skill reads the mode
	mode := strings.TrimSpace(v.PPTMode)
	if mode == "" {
		mode = "fast"
	}
	if err := a.updatePPTSkillConfig("mode", mode); err != nil {
		slog.Warn("SetCoWorkSettings: updatePPTSkillConfig failed", "err", err)
	}

	// Hot-reload the global hotkeys so a changed combo or a just-enabled toggle
	// takes effect immediately — without this, StartScreenshotHotkey() (called
	// once at startup) would keep polling with the OLD config (or not run at all
	// if the feature was off at boot), so the user would have to restart the app
	// to use the hotkey. Stop-then-Start is safe: Stop is a no-op when nothing
	// is running (and idempotent via stopOnce), and Start reads fresh config and
	// returns early when the feature is disabled.
	a.StopScreenshotHotkey()
	a.StartScreenshotHotkey()
	a.StopEStopHotkey()
	a.StartEStopHotkey()

	slog.Info("SetCoWorkSettings complete")
	return nil
}

// MailProbeResult is the outcome of a mailbox connection probe, returned to the
// settings panel so it can show a green/red status dot. The error is always nil
// (a connection failure is reported via Status="error" + Message, not a Go
// error) — this avoids wails surfacing it as a system error dialog. The panel
// triggers a probe after the user saves the mailbox config.
type MailProbeResult struct {
	OK      bool   `json:"ok"`
	Status  string `json:"status"`  // "ok" | "error" | "unconfigured"
	Message string `json:"message"` // human hint, e.g. "IMAP 登录失败：检查授权码"
}

// ProbeMailAccount tests a saved mailbox's IMAP login by actually connecting.
// It reloads config (so a just-saved mailbox is tested, not the stale boot-time
// snapshot) and reuses the same go-imap wiring the email tools use. Powers the
// green/red status dot on each mail card. An empty name probes the Default
// account (legacy single-pair path).
func (a *App) ProbeMailAccount(name string) (result MailProbeResult, err error) {
	// Recover from any panic so a bug in the probe path returns a friendly
	// error instead of crashing the Wails backend (which would kill the UI —
	// the "断了" symptom). The stack is logged for diagnosis.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("ProbeMailAccount panic", "name", name, "panic", r)
			result = MailProbeResult{Status: "error", Message: fmt.Sprintf("内部错误：%v", r)}
			err = nil
		}
	}()
	slog.Info("ProbeMailAccount", "name", name)
	cfg, err := config.Load()
	if err != nil {
		slog.Warn("ProbeMailAccount: config.Load failed", "err", err)
		return MailProbeResult{Status: "error", Message: "读取配置失败：" + err.Error()}, nil
	}
	// normalizeEmailAccounts folds the legacy [cowork.imap] single-pair into the
	// default account, so reading the default account covers both old/new configs.
	name = strings.TrimSpace(name)
	var (
		acct config.EmailAccount
		ok   bool
	)
	if name == "" {
		acct, ok = cfg.Cowork.DefaultEmailAccount()
	} else {
		acct, ok = cfg.Cowork.EmailAccountByName(name)
	}
	if !ok {
		slog.Warn("ProbeMailAccount: account not found", "name", name, "availableAccounts", len(cfg.Cowork.EmailAccounts))
		return MailProbeResult{Status: "unconfigured", Message: ""}, nil
	}
	if strings.TrimSpace(acct.IMAP.Host) == "" {
		slog.Warn("ProbeMailAccount: IMAP host empty", "name", name)
		return MailProbeResult{Status: "unconfigured", Message: ""}, nil
	}
	// Debug-level only; excludes the email address and password env name (PII).
	// host/port are server config, not personal data, so they're safe to log.
	slog.Debug("ProbeMailAccount connecting", "name", name, "host", acct.IMAP.Host, "port", acct.IMAP.Port)
	if err := builtin.ProbeIMAPConfig(acct.IMAP); err != nil {
		slog.Warn("ProbeMailAccount failed", "name", name, "err", err)
		return MailProbeResult{Status: "error", Message: err.Error()}, nil
	}
	return MailProbeResult{OK: true, Status: "ok", Message: "连接正常"}, nil
}

// InboxItem is one row in the cowork dock's "邮件" tab preview. A trimmed-down
// view of builtin.EmailMessage — the dock only needs envelope + preview, not
// attachments, to keep the JSON payload small for a sidebar list.
type InboxItem struct {
	From    string `json:"from"`
	To      string `json:"to"` // recipient(s); shown instead of From in the Sent view
	Subject string `json:"subject"`
	Date    string `json:"date"`
	Preview string `json:"preview"`
}

// InboxPreview reads the most recent messages (up to limit) from the default
// mailbox, for the cowork dock's "邮件" tab. mailbox is "INBOX" (unread only)
// or "Sent" (all sent). Like ProbeMailAccount, it reloads config so a just-saved
// mailbox works without restart. Returns an empty slice when unconfigured.
func (a *App) InboxPreview(mailbox string, limit int) ([]InboxItem, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("读取配置失败：%s", err.Error())
	}
	acct, ok := cfg.Cowork.DefaultEmailAccount()
	if !ok || strings.TrimSpace(acct.IMAP.Host) == "" {
		return []InboxItem{}, nil // unconfigured — dock shows "未配置邮箱"
	}
	mbox := strings.TrimSpace(mailbox)
	if mbox == "" {
		mbox = "INBOX"
	}
	// unreadOnly only makes sense for INBOX; sent folders show all.
	unreadOnly := mbox == "INBOX"
	msgs, err := builtin.ReadInboxFor(acct.IMAP, mbox, limit, unreadOnly)
	if err != nil {
		return nil, err
	}
	out := make([]InboxItem, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, InboxItem{
			From:    m.From,
			To:      m.To,
			Subject: m.Subject,
			Date:    m.Date,
			Preview: m.Preview,
		})
	}
	return out, nil
}

// CheckCoworkBrowser runs browser auto-detection and returns the display name
// ("Chrome"/"Edge"/…) or "" if none found. Powers the "detect" button in the
// panel so users see what auto-detect found before overriding.
func (a *App) CheckCoworkBrowser() string {
	return detectBrowserForSettings()
}

// OpenPPTTemplateDir opens the PPT templates folder in the OS file manager so the
// user can add/edit JSON templates directly. Powers the "打开模板目录" button. The
// dir is created/seeded first so it always exists when opened.
func (a *App) OpenPPTTemplateDir() error {
	dir := ppttemplate.SkillTemplatesDir()
	if dir == "" {
		dir = ppttemplate.DefaultDir()
	}
	if dir == "" {
		return fmt.Errorf("无法定位模板目录（用户配置目录不可用）")
	}
	return openInFileExplorer(dir)
}

// PickPPTTemplate opens a file picker for .pptx files and copies the selected
// file to ~/.hiq/ppt-template.pptx (a fixed path the ppt-auto skill reads).
// Returns the display name of the selected file, or "" if cancelled.
func (a *App) PickPPTTemplate() (string, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择 PPT 模板",
		Filters: []runtime.FileFilter{
			{DisplayName: "PowerPoint 模板 (*.pptx)", Pattern: "*.pptx"},
		},
	})
	if err != nil || path == "" {
		return "", nil
	}
	// Copy the selected file to the fixed location.
	home, _ := os.UserHomeDir()
	dest := filepath.Join(home, ".hiq", "ppt-template.pptx")
	srcData, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取模板文件失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, srcData, 0o644); err != nil {
		return "", fmt.Errorf("写入模板文件失败: %w", err)
	}
	// Kick off async vision-based color/style extraction. This writes
	// ~/.hiq/ppt-template-style.json which the ppt-auto skill reads
	// (Step 0) as the highest-priority color source. Best-effort: silently
	// degrades if no VLM configured or no full-screen bg image found.
	go a.analyzeTemplateStyleAsync(dest)
	// Return the filename for display.
	return filepath.Base(path), nil
}

// --- .env management --------------------------------------------------------

// coworkEnvPath is the hiq-managed .env holding coWork secrets (SMTP/IMAP
// passwords). Lives in the user config dir alongside config.toml. Loaded at
// startup into the process env so the tools see the passwords via os.Getenv.
func coworkEnvPath() string {
	dir := desktopConfigDir()
	return filepath.Join(dir, "cowork.env")
}

// loadCoworkEnv reads KEY=VALUE lines from the .env into a map. Missing file =
// empty map (not an error — first run has no secrets yet).
func loadCoworkEnv() map[string]string {
	out := map[string]string{}
	f, err := os.Open(coworkEnvPath())
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, "="); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			// Strip optional surrounding quotes.
			val = strings.Trim(val, "\"")
			out[key] = val
		}
	}
	return out
}

// loadCoworkEnvAtStartup is called once during desktop startup. It first lifts
// any plaintext secrets from the legacy cowork.env into the encrypted store
// (one-time migration), then — as a fallback for the rare case the store is
// unavailable — still loads whatever cowork.env remains into the process env.
// CoWork tools read passwords via os.Getenv, which the boot layer also feeds
// from the encrypted store via secret.LoadIntoEnv.
func loadCoworkEnvAtStartup() {
	migrateLegacyCoworkEnv()
	for key, val := range loadCoworkEnv() {
		// Don't override an existing env var (user/system env wins over file).
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
	// Restore email account passwords from the encrypted store into the process
	// env. Secrets are written to the store on save (SetCoWorkSettings) but only
	// mirrored to os.Environ for the live session — without this restore, a
	// restart would leave PasswordEnv unset, so IMAP/SMTP auth would fail until
	// the user re-saved the mailbox. We read every account's PasswordEnv from
	// config and pull each from the store.
	restoreEmailSecretsToEnv()
}

// restoreEmailSecretsToEnv loads the multi-account config and copies each
// account's stored secret (if any) from the encrypted store into the process
// environment, so tools reading os.Getenv(PasswordEnv) find the password right
// after startup. Skips env vars already set (user/system env wins). Best-effort:
// a missing secret (not yet saved) is silently skipped.
func restoreEmailSecretsToEnv() {
	cfg, err := config.Load()
	if err != nil {
		return // non-fatal — tools will report "not configured" clearly
	}
	store := secret.Default()
	restore := func(envName string) {
		envName = strings.TrimSpace(envName)
		if envName == "" || os.Getenv(envName) != "" {
			return
		}
		if val, ok, err := store.Get(envName); err == nil && ok && strings.TrimSpace(val) != "" {
			os.Setenv(envName, val)
		}
	}
	// Legacy single-pair.
	restore(cfg.Cowork.SMTP.PasswordEnv)
	restore(cfg.Cowork.IMAP.PasswordEnv)
	// Multi-account list — the source of truth when set.
	for _, a := range cfg.Cowork.EmailAccounts {
		restore(a.SMTP.PasswordEnv)
		restore(a.IMAP.PasswordEnv)
		// Also restore the derived env names (accountPasswordEnvs) in case the
		// config predates the per-account naming and PasswordEnv is still empty.
		smtpEnv, imapEnv := accountPasswordEnvs(a.Name)
		restore(smtpEnv)
		restore(imapEnv)
	}
}

// secretIsSet reports whether a secret for envName is stored — in the encrypted
// store (preferred) or, pre-migration, in the legacy cowork.env map. It never
// returns the value itself.
func secretIsSet(envName string, legacyEnv map[string]string) bool {
	if envName == "" {
		return false
	}
	if _, ok, _ := secret.Default().Get(envName); ok {
		return true
	}
	if legacyEnv != nil {
		if v, ok := legacyEnv[envName]; ok && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// migrateLegacyCoworkEnv moves plaintext secrets from the legacy cowork.env
// into the encrypted store, then removes the plaintext file. Called once at
// startup. No-op when cowork.env is absent. If the store rejects a secret
// (e.g. DPAPI unavailable), migration stops and the plaintext file is kept so
// the feature degrades gracefully — tools still find the password via the
// cowork.env fallback in loadCoworkEnvAtStartup.
func migrateLegacyCoworkEnv() {
	legacy := loadCoworkEnv()
	if len(legacy) == 0 {
		return
	}
	store := secret.Default()
	for key, val := range legacy {
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		// Don't clobber an already-stored secret (the user may have re-entered
		// it via the panel after upgrade).
		if _, ok, _ := store.Get(key); ok {
			continue
		}
		if err := store.Set(key, val); err != nil {
			return // store unavailable — keep the plaintext fallback
		}
	}
	// Every non-empty entry made it into the store; drop the plaintext residue.
	_ = os.Remove(coworkEnvPath())
}

// togglePermissionRule adds or removes a bare tool name (e.g. "email_send")
// from a permission rule list. It matches case-insensitively on the tool-name
// prefix only, so a subject-scoped rule like "email_send:example.com" is left
// untouched (we only manage the blanket rule). add=true appends if absent;
// add=false removes all bare-tool matches.
func togglePermissionRule(rules []string, tool string, add bool) []string {
	tool = strings.ToLower(strings.TrimSpace(tool))
	out := make([]string, 0, len(rules))
	present := false
	for _, r := range rules {
		name := strings.TrimSpace(r)
		if i := strings.IndexAny(name, "(:"); i >= 0 {
			name = name[:i]
		}
		if strings.EqualFold(strings.TrimSpace(name), tool) {
			present = true
			if !add {
				continue // drop it
			}
		}
		out = append(out, r)
	}
	if add && !present {
		out = append(out, tool)
	}
	return out
}
