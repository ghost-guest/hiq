package main

// hooks_settings_app.go exposes the hook configuration (settings.json) to the
// desktop frontend's Settings → Hooks tab. It supports BOTH scopes (global
// ~/.hiq/settings.json and project <root>/.hiq/settings.json), the
// project-trust gate (project hooks load only when trusted), and surfaces the
// file path + valid event list so the GUI can render a JSON editor.
//
// Ported from DeepSeek-Reasonix (desktop/hooks_settings_app.go), adapted to the
// hiq module path. The wire types (HookConfigView/HooksSettingsView) are
// flat (one entry per hook, carrying its event) so the frontend edits a single
// JSON document and groups by event itself.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zzycxz/hiq/internal/hook"
)

type HookConfigView struct {
	Event       string `json:"event"`
	Match       string `json:"match,omitempty"`
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
}

type HooksSettingsView struct {
	Scope       string           `json:"scope"`
	Path        string           `json:"path"`
	ProjectRoot string           `json:"projectRoot"`
	Trusted     bool             `json:"trusted"`
	Hooks       []HookConfigView `json:"hooks"`
	Events      []string         `json:"events"`
}

func (a *App) HooksSettings(scope string) HooksSettingsView {
	s, path, root := normalizeHooksScope(scope, a.activeHookProjectRoot())
	view := HooksSettingsView{
		Scope:       s,
		Path:        path,
		ProjectRoot: root,
		Trusted:     s == string(hook.ScopeGlobal) || hook.IsTrusted(root, ""),
		Hooks:       []HookConfigView{},
		Events:      hookEventNames(),
	}
	settings, err := readHooksSettingsFile(path)
	if err != nil || settings.Hooks == nil {
		return view
	}
	for _, event := range hook.Events {
		for _, cfg := range settings.Hooks[event] {
			if strings.TrimSpace(cfg.Command) == "" {
				continue
			}
			view.Hooks = append(view.Hooks, hookConfigView(event, cfg))
		}
	}
	return view
}

func (a *App) SaveHooksSettings(scope string, hooks []HookConfigView) error {
	return a.SaveHooksSettingsForRoot(scope, a.activeHookProjectRoot(), hooks)
}

func (a *App) SaveHooksSettingsForRoot(scope, projectRoot string, hooks []HookConfigView) error {
	s, path, _ := normalizeHooksScope(scope, projectRoot)
	settings := hook.Settings{Hooks: map[hook.Event][]hook.HookConfig{}}
	for _, h := range hooks {
		event := hook.Event(strings.TrimSpace(h.Event))
		if !validHookEvent(event) {
			return fmt.Errorf("unknown hook event %q", h.Event)
		}
		cmd := strings.TrimSpace(h.Command)
		if cmd == "" {
			continue
		}
		settings.Hooks[event] = append(settings.Hooks[event], hook.HookConfig{
			Match:       strings.TrimSpace(h.Match),
			Command:     cmd,
			Description: strings.TrimSpace(h.Description),
			Timeout:     h.Timeout,
			Cwd:         strings.TrimSpace(h.Cwd),
		})
	}
	if s == string(hook.ScopeProject) && strings.TrimSpace(path) == "" {
		return fmt.Errorf("no active project workspace")
	}
	if err := writeHooksSettingsFile(path, settings); err != nil {
		return err
	}
	// Reload so the change applies without a restart (mirrors the global-only
	// path's rebuild). rebuild re-runs boot.Build, which calls hook.Load.
	_ = a.rebuild()
	return nil
}

func (a *App) TrustProjectHooks() error {
	return a.TrustProjectHooksForRoot(a.activeHookProjectRoot())
}

func (a *App) TrustProjectHooksForRoot(root string) error {
	root = strings.TrimSpace(root)
	if strings.TrimSpace(root) == "" || root == "." {
		return fmt.Errorf("no active project workspace")
	}
	return hook.Trust(root, "")
}

// activeHookProjectRoot returns the active tab's workspace root when it is a
// project-scoped tab, else "". Project hooks are only editable when a project
// workspace is active.
func (a *App) activeHookProjectRoot() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if tab := a.activeTabLocked(); tab != nil && tab.Scope == "project" {
		return strings.TrimSpace(tab.WorkspaceRoot)
	}
	return ""
}

func normalizeHooksScope(scope, projectRoot string) (string, string, string) {
	if strings.EqualFold(strings.TrimSpace(scope), string(hook.ScopeProject)) {
		root := strings.TrimSpace(projectRoot)
		if root == "" {
			return string(hook.ScopeProject), "", ""
		}
		return string(hook.ScopeProject), hook.ProjectSettingsPath(root), root
	}
	return string(hook.ScopeGlobal), hook.GlobalSettingsPath(""), ""
}

func hookEventNames() []string {
	out := make([]string, 0, len(hook.Events))
	for _, event := range hook.Events {
		out = append(out, string(event))
	}
	return out
}

func validHookEvent(event hook.Event) bool {
	for _, e := range hook.Events {
		if event == e {
			return true
		}
	}
	return false
}

func hookConfigView(event hook.Event, cfg hook.HookConfig) HookConfigView {
	return HookConfigView{
		Event:       string(event),
		Match:       cfg.Match,
		Command:     cfg.Command,
		Description: cfg.Description,
		Timeout:     cfg.Timeout,
		Cwd:         cfg.Cwd,
	}
}

func readHooksSettingsFile(path string) (hook.Settings, error) {
	var settings hook.Settings
	body, err := os.ReadFile(path)
	if err != nil {
		return settings, err
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		return settings, err
	}
	if settings.Hooks == nil {
		settings.Hooks = map[hook.Event][]hook.HookConfig{}
	}
	return settings, nil
}

// writeHooksSettingsFile preserves any other top-level keys in settings.json
// (only the "hooks" key is rewritten), so a future schema additions on disk
// survive a hooks edit.
func writeHooksSettingsFile(path string, settings hook.Settings) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty hooks settings path")
	}
	raw := map[string]json.RawMessage{}
	if body, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(body, &raw); err != nil {
			return err
		}
	}
	hooksJSON, err := json.Marshal(settings.Hooks)
	if err != nil {
		return err
	}
	raw["hooks"] = hooksJSON
	body, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}
