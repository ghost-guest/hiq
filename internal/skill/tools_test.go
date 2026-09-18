package skill

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/event"
)

func TestRunSkillInline(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".hiq/skills/note.md", "---\ndescription: take a note\n---\nDo the thing.")
	tl := NewRunSkillTool(New(Options{HomeDir: home, DisableBuiltins: true}), nil)

	out, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"note","arguments":"with args"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.HasPrefix(out, "<skill-pin name=\"note\">") || !strings.HasSuffix(out, "</skill-pin>") {
		t.Errorf("inline skill should be skill-pin wrapped:\n%s", out)
	}
	if !strings.Contains(out, "Do the thing.") || !strings.Contains(out, "Arguments: with args") {
		t.Errorf("body/args missing:\n%s", out)
	}
}

func TestRunSkillUnknown(t *testing.T) {
	tl := NewRunSkillTool(New(Options{HomeDir: t.TempDir(), DisableBuiltins: true}), nil)
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"nope"}`)); err == nil {
		t.Error("unknown skill should error")
	}
}

func TestRunSkillSubagentNeedsRunner(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".hiq/skills/dig.md", "---\ndescription: dig\nrunAs: subagent\n---\nbody")
	tl := NewRunSkillTool(New(Options{HomeDir: home, DisableBuiltins: true}), nil) // nil runner
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"dig","arguments":"go"}`)); err == nil {
		t.Error("subagent skill with no runner should error, not silently inline")
	}
}

func TestRunSkillSubagentRuns(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".hiq/skills/dig.md", "---\ndescription: dig\nrunAs: subagent\n---\nbody")
	var gotTask string
	runner := func(_ context.Context, sk Skill, task string, _ SubagentRunOptions) (string, error) {
		gotTask = task
		return "answer from " + sk.Name, nil
	}
	tl := NewRunSkillTool(New(Options{HomeDir: home, DisableBuiltins: true}), runner)
	out, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"dig","arguments":"find X"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gotTask != "find X" {
		t.Errorf("runner got task %q", gotTask)
	}
	if out != "answer from dig" {
		t.Errorf("runner output not returned: %q", out)
	}
}

func TestRunSkillSubagentResolvesProfile(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".hiq/skills/deep.md", "---\ndescription: deep\nrunAs: subagent\nmodel: test-provider\neffort: max\n---\nbody")
	tl := NewRunSkillTool(New(Options{HomeDir: home, DisableBuiltins: true}), nil)

	pr, ok := tl.(interface {
		ResolveProfile(json.RawMessage) *event.Profile
	})
	if !ok {
		t.Fatal("run_skill should expose ResolveProfile")
	}
	got := pr.ResolveProfile(json.RawMessage(`{"name":"deep","arguments":"x"}`))
	if got == nil || got.Model != "test-provider" || got.Effort != "max" {
		t.Fatalf("profile = %+v, want test-provider/max", got)
	}
}

func TestRunSkillSubagentRequiresArgs(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".hiq/skills/dig.md", "---\ndescription: dig\nrunAs: subagent\n---\nbody")
	runner := func(_ context.Context, _ Skill, _ string, _ SubagentRunOptions) (string, error) {
		return "x", nil
	}
	tl := NewRunSkillTool(New(Options{HomeDir: home, DisableBuiltins: true}), runner)
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"dig"}`)); err == nil {
		t.Error("subagent skill should require arguments")
	}
}

func TestCleanSkillName(t *testing.T) {
	cases := map[string]string{
		"explore":              "explore",
		"explore [🧬 subagent]": "explore",
		"[🧬 subagent] explore": "explore",
		" review ":             "review",
		"[only a tag]":         "",
		"":                     "",
	}
	for in, want := range cases {
		if got := cleanSkillName(in); got != want {
			t.Errorf("cleanSkillName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuiltinSubagentToolsRunner(t *testing.T) {
	var ran string
	runner := func(_ context.Context, sk Skill, task string, _ SubagentRunOptions) (string, error) {
		ran = sk.Name + ":" + task
		return "ok", nil
	}
	tools := BuiltinSubagentTools(New(Options{HomeDir: t.TempDir()}), runner)
	var explore interface {
		Name() string
		Execute(context.Context, json.RawMessage) (string, error)
	}
	for _, tl := range tools {
		if tl.Name() == "explore" {
			explore = tl
		}
	}
	if explore == nil {
		t.Fatal("explore wrapper tool not built")
	}
	if _, err := explore.Execute(context.Background(), json.RawMessage(`{"task":"map the loop"}`)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if ran != "explore:map the loop" {
		t.Errorf("runner not invoked correctly: %q", ran)
	}
}

func TestBuiltinSubagentToolsPassContinuationOptions(t *testing.T) {
	var got SubagentRunOptions
	runner := func(_ context.Context, _ Skill, _ string, opts SubagentRunOptions) (string, error) {
		got = opts
		return "ok", nil
	}
	tools := BuiltinSubagentTools(New(Options{HomeDir: t.TempDir()}), runner)
	var review interface {
		Name() string
		Execute(context.Context, json.RawMessage) (string, error)
	}
	for _, tl := range tools {
		if tl.Name() == "review" {
			review = tl
			break
		}
	}
	if review == nil {
		t.Fatal("review wrapper tool not built")
	}
	if _, err := review.Execute(context.Background(), json.RawMessage(`{"task":"again","continue_from":"sa_prev"}`)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.ContinueFrom != "sa_prev" || got.ForkFrom != "" {
		t.Fatalf("continuation opts = %+v, want continue_from sa_prev", got)
	}
}

func TestBuiltinSubagentToolResolvesProfile(t *testing.T) {
	store := New(Options{HomeDir: t.TempDir()})
	tools := BuiltinSubagentTools(store, nil, func(sk Skill) *event.Profile {
		return &event.Profile{Model: sk.Name + "-model", Effort: "max"}
	})
	var review interface {
		ResolveProfile(json.RawMessage) *event.Profile
	}
	for _, tl := range tools {
		if tl.Name() == "review" {
			review = tl.(interface {
				ResolveProfile(json.RawMessage) *event.Profile
			})
			break
		}
	}
	if review == nil {
		t.Fatal("review tool not found")
	}
	got := review.ResolveProfile(json.RawMessage(`{"task":"general"}`))
	if got == nil || got.Model != "review-model" || got.Effort != "max" {
		t.Fatalf("profile = %+v, want review-model/max", got)
	}
}

func TestInstallSkill(t *testing.T) {
	home := t.TempDir()
	st := New(Options{HomeDir: home, DisableBuiltins: true})
	tl := NewInstallSkillTool(st, nil)

	out, err := tl.Execute(context.Background(), json.RawMessage(
		`{"name":"deploy","description":"ship it","body":"steps","runAs":"subagent","model":"test-provider","effort":"max","allowedTools":["bash","read_file"]}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Errorf("expected ok result, got %s", out)
	}
	var res struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result JSON: %v", err)
	}
	wantPath := filepath.Join(home, ".hiq", "skills", "deploy", SkillFile)
	if res.Path != wantPath {
		t.Fatalf("install_skill should report canonical path %s, got %s", wantPath, res.Path)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("install_skill should write canonical SKILL.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".hiq", "skills", "deploy.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install_skill should not write legacy flat deploy.md, stat err=%v", err)
	}
	// Round-trips through the store with the frontmatter we wrote.
	sk, ok := st.Read("deploy")
	if !ok {
		t.Fatal("installed skill not readable")
	}
	if sk.RunAs != RunSubagent || sk.Model != "test-provider" || sk.Effort != "max" || len(sk.AllowedTools) != 2 {
		t.Errorf("frontmatter not round-tripped: runAs=%s model=%q effort=%q tools=%v", sk.RunAs, sk.Model, sk.Effort, sk.AllowedTools)
	}
	// Refuses overwrite.
	if _, err := tl.Execute(context.Background(), json.RawMessage(
		`{"name":"deploy","description":"again","body":"x"}`)); err == nil {
		t.Error("install_skill should refuse to overwrite")
	}
	// Requires description.
	if _, err := tl.Execute(context.Background(), json.RawMessage(
		`{"name":"x","description":"","body":"b"}`)); err == nil {
		t.Error("install_skill should require a description")
	}
}

func TestReadSkillLoadsInlineAndIsReadOnly(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".hiq/skills/note.md", "---\ndescription: take a note\n---\nDo the thing.")
	tl := NewReadSkillTool(New(Options{HomeDir: home, DisableBuiltins: true}))

	if !tl.ReadOnly() {
		t.Fatal("read_skill must be ReadOnly so it works in plan mode")
	}
	out, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"note","arguments":"with args"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "Do the thing.") || !strings.Contains(out, "Arguments: with args") {
		t.Errorf("inline body/args missing:\n%s", out)
	}
}

func TestReadSkillRejectsSubagent(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".hiq/skills/dig.md", "---\ndescription: dig\nrunAs: subagent\n---\nbody")
	tl := NewReadSkillTool(New(Options{HomeDir: home, DisableBuiltins: true}))

	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"dig"}`)); err == nil || !strings.Contains(err.Error(), "run_skill") {
		t.Fatalf("read_skill on a subagent skill should point to run_skill, got %v", err)
	}
}

// TestRunSkillDisabledHint verifies that run_skill distinguishes "disabled"
// from "unknown" when an allStore is wired: a skill the user turned off should
// return a clear "disabled, enable it in Settings" error instead of the vague
// "unknown skill", so the model tells the user rather than retrying.
func TestRunSkillDisabledHint(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".hiq/skills/ppt-auto.md", "---\ndescription: ppt\n---\nbody")

	// live store filters ppt-auto out (user disabled it); all store keeps it.
	live := New(Options{HomeDir: home, DisableBuiltins: true, DisabledNames: []string{"ppt-auto"}})
	all := New(Options{HomeDir: home, DisableBuiltins: true})

	tl := NewRunSkillToolWithIndex(live, all, nil)
	_, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"ppt-auto"}`))
	if err == nil {
		t.Fatal("disabled skill should still error (it's filtered from the live store)")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled skill should report a 'disabled' error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Settings") {
		t.Fatalf("disabled error should guide the user to Settings, got: %v", err)
	}
}

// TestRunSkillDisabledHintWithoutAllStore verifies that without an allStore,
// a disabled skill falls back to the legacy "unknown skill" wording (so the
// feature degrades gracefully when the host doesn't wire the index store).
func TestRunSkillDisabledHintWithoutAllStore(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".hiq/skills/ppt-auto.md", "---\ndescription: ppt\n---\nbody")
	live := New(Options{HomeDir: home, DisableBuiltins: true, DisabledNames: []string{"ppt-auto"}})

	tl := NewRunSkillTool(live, nil) // no allStore
	_, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"ppt-auto"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Fatalf("without allStore a disabled skill should fall back to 'unknown skill', got %v", err)
	}
}

func TestRunSkillExecutorRouting(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".hiq/skills/browser-site-a.md",
		"---\ndescription: site A flow\nrunAs: subagent\nexecutor: browser-flow\n---\n\n## 步骤\n\n| # | 操作 | 目标 | 值 |\n|---|---|---|---|\n| 1 | navigate | `https://a/` |  |\n")

	// No runner wired → clear error (browser tools are profile-gated).
	tl := NewRunSkillTool(New(Options{HomeDir: home, DisableBuiltins: true}), nil)
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"browser-site-a"}`)); err == nil ||
		!strings.Contains(err.Error(), "flow runner") {
		t.Errorf("nil flow runner must error clearly, got %v", err)
	}

	// Wired → routed to the deterministic runner, NOT the subagent runner.
	prev := flowRunner
	defer SetFlowRunner(prev)
	var gotBody, gotArgs string
	calledSubagent := false
	SetFlowRunner(func(_ context.Context, sk Skill, arguments string) (string, error) {
		gotBody, gotArgs = sk.Body, arguments
		return "flow done", nil
	})
	tl2 := NewRunSkillTool(New(Options{HomeDir: home, DisableBuiltins: true}),
		func(_ context.Context, _ Skill, _ string, _ SubagentRunOptions) (string, error) {
			calledSubagent = true
			return "subagent ran", nil
		})
	out, err := tl2.Execute(context.Background(), json.RawMessage(`{"name":"browser-site-a","arguments":"工单号=A1"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out != "flow done" {
		t.Errorf("executor skill must return the flow result, got %q", out)
	}
	if calledSubagent {
		t.Error("executor-routed skill must bypass the subagent runner")
	}
	if !strings.Contains(gotBody, "https://a/") || gotArgs != "工单号=A1" {
		t.Errorf("flow runner must receive body+arguments, got args %q", gotArgs)
	}
}
