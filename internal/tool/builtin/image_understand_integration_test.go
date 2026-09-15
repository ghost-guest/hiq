package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/zzycxz/fairpeer/internal/provider"
	"github.com/zzycxz/fairpeer/internal/tool"
)

// TestImageUnderstandIntegration verifies the four integration points that
// unit tests check in isolation but never prove connect end-to-end:
//
//  1. The tool is actually REGISTERED under the name "image_understand" — the
//     name 11 other files reference. If registration fails, every existing
//     reference (browser.go, skill/builtins.go's AllowedTools, etc.) dangles.
//  2. The tool surfaces in the registry's Schemas() — i.e. the main model will
//     actually SEE this tool and can decide to call it. A hidden or
//     un-registered tool is invisible to the model.
//  3. Workspace.Tools() binds workDir into the tool instance, so a relative
//     attachment path (".fairpeer/attachments/x.png") resolves under the
//     workspace rather than the process cwd.
//  4. The tool's JSON schema parses and advertises the prompt parameter the
//     agent must fill in — the entire design hinges on the agent authoring the
//     prompt, so the schema must make that obligation explicit.
//
// Together these prove the wiring the agent loop relies on, without needing a
// live LLM. The actual "model reads the <image path=...> reference and decides
// to call image_understand" behavior is a runtime property of the chosen model
// and is exercised manually (see the plan's verification section).
func TestImageUnderstandIntegration(t *testing.T) {
	// --- 1. Registered under the documented name ---
	regTool, ok := tool.LookupBuiltin("image_understand")
	if !ok {
		t.Fatal("image_understand is NOT registered — 11 files reference this name. " +
			"The whole feature is dead if the lookup fails.")
	}
	fmt.Printf("[1] registered: name=%q type=%T\n", regTool.Name(), regTool)

	// --- 2. Surfaces in registry Schemas() (visible to the model) ---
	registry := tool.NewRegistry()
	for _, tl := range tool.Builtins() {
		registry.Add(tl)
	}
	schemas := registry.Schemas()
	var seen *provider.ToolSchema
	for i, s := range schemas {
		if s.Name == "image_understand" {
			seen = &schemas[i]
			break
		}
	}
	if seen == nil {
		t.Fatal("image_understand not found in registry.Schemas() — the model would never see it. " +
			"Check it isn't being Hide()'d, or that builtins init() ran.")
	}
	fmt.Printf("[2] visible to model: name=%q description(first 80)=%q\n",
		seen.Name, truncN(seen.Description, 80))
	fmt.Printf("    parameters: %s\n", string(seen.Parameters))

	// --- 3. Workspace binds workDir ---
	ws := Workspace{Dir: "/tmp/fake-workspace"}
	wsTools := ws.Tools()
	var bound imageUnderstand
	found := false
	for _, tl := range wsTools {
		if iu, ok := tl.(imageUnderstand); ok && tl.Name() == "image_understand" {
			bound = iu
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Workspace.Tools() does not return image_understand — " +
			"desktop multi-tab mode would lose the tool entirely.")
	}
	if bound.workDir != "/tmp/fake-workspace" {
		t.Fatalf("workspace did not bind workDir: got %q, want /tmp/fake-workspace", bound.workDir)
	}
	fmt.Printf("[3] workspace-bound: workDir=%q (relative paths resolve here)\n", bound.workDir)

	// --- 4. Schema advertises the agent-authored prompt ---
	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(regTool.Schema(), &schema); err != nil {
		t.Fatalf("schema unmarshal: %v", err)
	}
	promptProp, hasPrompt := schema.Properties["prompt"]
	if !hasPrompt {
		t.Fatal("schema has no 'prompt' property — the agent gets no signal it must write one")
	}
	promptRequired := false
	for _, r := range schema.Required {
		if r == "prompt" {
			promptRequired = true
		}
	}
	if !promptRequired {
		t.Fatal("'prompt' is not in required[] — the agent may omit it, getting a generic description")
	}
	fmt.Printf("[4] prompt param: type=%q required=true description(first 80)=%q\n",
		promptProp.Type, truncN(promptProp.Description, 80))

	// Confirm the description tells the agent about the <image path=...> reference
	// convention — without this hint the agent won't know the path comes from
	// the message body.
	if !strings.Contains(regTool.Description(), "image") || !strings.Contains(regTool.Description(), "path") {
		t.Errorf("description should hint that the path comes from an <image path=...> reference; got: %s",
			regTool.Description())
	}
}

// TestImageUnderstandEndToEndWithStub proves the full Execute path with a stubbed
// VLM runner: the tool reads the file, builds a multimodal message, and returns
// the runner's text. This is as close to the real path as a test can get without
// network access; it mirrors TestCallVLMProviderPath's offline strategy.
func TestImageUnderstandEndToEndWithStub(t *testing.T) {
	const wantDesc = "the table shows: Q1=100, Q2=200"
	origModel := vlmModel
	origRunner := runProviderChat
	t.Cleanup(func() {
		vlmModel = origModel
		runProviderChat = origRunner
	})
	SetVLMModel("test-vlm")
	var gotPrompt string
	SetProviderChatRunner(func(ctx context.Context, modelRef string, msgs []provider.Message) ([]provider.Message, error) {
		gotPrompt = provider.ContentString(msgs[0].Content)
		// Verify the image part actually made it onto the wire.
		if !msgs[0].HasImages() {
			t.Error("VLM runner received no image part — image dropped before the call")
		}
		return []provider.Message{{Role: provider.RoleAssistant, Content: wantDesc}}, nil
	})

	dir := t.TempDir()
	pngPath := mustWriteRedPNG(t, dir, "table.png")
	args, _ := json.Marshal(map[string]string{
		"path":   pngPath,
		"prompt": "What does the table say? Output as markdown.",
	})
	got, err := imageUnderstand{}.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Result is now the VLM description prefixed with an objective header
	// ([image: name | WxH | mime | vision: model]). Verify both halves.
	if !strings.Contains(got, wantDesc) {
		t.Errorf("result missing VLM answer %q; got %q", wantDesc, got)
	}
	if !strings.Contains(got, "[image: table.png | 1x1 | image/png |") {
		t.Errorf("result missing objective header (name/dims/mime); got %q", got)
	}
	fmt.Printf("[e2e] VLM received prompt=%q → returned %q\n", truncN(gotPrompt, 60), got)
}

func truncN(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
