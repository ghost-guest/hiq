package instruction

import (
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/memory"
)

func TestForModel_QwenFamilyAddon(t *testing.T) {
	addon := ForModel("qwen/qwen3-max")
	// qwen models should get the family addon. Thinking behavior is now declared
	// per-provider via ReasoningProtocol rather than injected as a prompt addon,
	// so there is no thinking-capability addon assertion here.
	if !strings.Contains(addon, "tool call") {
		t.Fatalf("expected QwenAddon (tool call) for qwen3-max, got %q", addon)
	}
}

func TestForModel_UnknownModel(t *testing.T) {
	addon := ForModel("unknown/model")
	if addon != "" {
		t.Fatalf("expected empty for unknown model, got %q", addon)
	}
}

func TestForModel_AutoRouter(t *testing.T) {
	addon := ForModel("test-provider/auto-router")
	if addon != "" {
		t.Fatalf("expected empty for auto-router, got %q", addon)
	}
}

func TestExtractHostChecksFromStructuredSection(t *testing.T) {
	docs := []memory.Source{{
		Path:  "AGENTS.md",
		Scope: memory.ScopeProject,
		Body: strings.Join([]string{
			"# Project rules",
			"## hiq host checks",
			"- verify: go test ./internal/...",
			"* verify: git diff --check",
			"- verify: go test ./internal/...",
			"- note: ignored",
			"## Other",
			"- verify: ignored after section",
		}, "\n"),
	}}

	checks := ExtractHostChecks(docs)
	if len(checks) != 2 {
		t.Fatalf("checks len = %d, want 2: %#v", len(checks), checks)
	}
	if checks[0].Command != "go test ./internal/..." || checks[0].SourcePath != "AGENTS.md" || checks[0].Line != 3 {
		t.Fatalf("first check = %#v", checks[0])
	}
	if checks[1].Command != "git diff --check" || checks[1].SourcePath != "AGENTS.md" || checks[1].Line != 4 {
		t.Fatalf("second check = %#v", checks[1])
	}
}

func TestExtractHostChecksIgnoresOrdinaryGuidance(t *testing.T) {
	docs := []memory.Source{{
		Path: "hiq.md",
		Body: "Always run go test before committing.\n\n- verify: go test ./...",
	}}

	if checks := ExtractHostChecks(docs); len(checks) != 0 {
		t.Fatalf("ordinary guidance should not create hard checks: %#v", checks)
	}
}

func TestExtractHostChecksIsCaseInsensitive(t *testing.T) {
	docs := []memory.Source{{
		Path: "hiq.md",
		Body: "## hiq HOST checks\n- verify: go test ./...",
	}}

	checks := ExtractHostChecks(docs)
	if len(checks) != 1 || checks[0].Command != "go test ./..." {
		t.Fatalf("case-insensitive heading not extracted: %#v", checks)
	}
}
