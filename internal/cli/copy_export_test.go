package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/control"
	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/provider"
)

// newTestChatTUIWithMessages builds a chatTUI wired to a controller whose
// session already holds msgs, for /copy and /export tests. The system prompt is
// included to prove it is excluded from exports.
func newTestChatTUIWithMessages(t *testing.T, workspaceRoot string, msgs ...provider.Message) chatTUI {
	t.Helper()
	sess := agent.NewSession("system prompt should not export")
	for _, msg := range msgs {
		sess.Add(msg)
	}
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{Executor: exec, WorkspaceRoot: workspaceRoot})
	return newChatTUI(ctrl, "", make(chan event.Event, 4), 80)
}

func sampleExportMessages() []provider.Message {
	return []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
		{Role: provider.RoleAssistant, Content: "hi there"},
		{Role: provider.RoleUser, Content: "Referenced context:\n\nfile=foo.go\n\nfix the bug"},
		{Role: provider.RoleAssistant, Content: "will do"},
	}
}

func TestCopyAssistantPartsSkipsPlaceholders(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "q"},
		{Role: provider.RoleAssistant, Content: "..."}, // placeholder skipped
		{Role: provider.RoleAssistant, Content: ""},    // empty skipped
		{Role: provider.RoleAssistant, Content: "real answer"},
	}
	parts := copyAssistantParts(msgs)
	if len(parts) != 1 || parts[0] != "real answer" {
		t.Fatalf("copyAssistantParts = %v, want [real answer]", parts)
	}
}

func TestCopyAssistantPartsStartsAfterLastUser(t *testing.T) {
	// A user message after earlier assistant turns resets the window: only the
	// assistant messages after the LAST user message are candidates.
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "first"},
		{Role: provider.RoleAssistant, Content: "old answer"},
		{Role: provider.RoleUser, Content: "second"},
		{Role: provider.RoleAssistant, Content: "new answer"},
	}
	parts := copyAssistantParts(msgs)
	if len(parts) != 1 || parts[0] != "new answer" {
		t.Fatalf("copyAssistantParts = %v, want [new answer]", parts)
	}
}

// TestCopyCommandByIndex verifies "/copy 1" copies the most recent assistant
// response directly, without opening the picker.
func TestCopyCommandByIndex(t *testing.T) {
	m := newTestChatTUIWithMessages(t, t.TempDir(), sampleExportMessages()...)
	cmd := m.runCopyCommand("/copy 1")
	if cmd == nil {
		t.Fatal("/copy 1 should return a clipboard command")
	}
}

// TestCopyCommandEmptyShowsNotice verifies a session with no assistant response
// surfaces the empty notice rather than a clipboard command.
func TestCopyCommandEmptyShowsNotice(t *testing.T) {
	m := newTestChatTUIWithMessages(t, t.TempDir(),
		provider.Message{Role: provider.RoleUser, Content: "q"},
	)
	if cmd := m.runCopyCommand("/copy 1"); cmd != nil {
		t.Fatalf("/copy 1 with no assistant reply should return nil, got %v", cmd)
	}
}

// TestCopyPickerOpensFromBareCopy verifies a bare "/copy" opens the picker when
// there is at least one assistant response.
func TestCopyPickerOpensFromBareCopy(t *testing.T) {
	m := newTestChatTUIWithMessages(t, t.TempDir(), sampleExportMessages()...)
	m.runCopyCommand("/copy")
	if m.copyPick == nil {
		t.Fatal("bare /copy should open the picker")
	}
	if len(m.copyPick.parts) != 1 {
		t.Fatalf("picker parts = %v, want 1 (most recent assistant reply)", m.copyPick.parts)
	}
	// Newest-first: index 0 is the most recent ("will do").
	if m.copyPick.parts[0] != "will do" {
		t.Fatalf("picker[0] = %q, want \"will do\"", m.copyPick.parts[0])
	}
}

// TestCopyPickerNavigatesAndCopies drives the picker via key messages.
func TestCopyPickerNavigatesAndCopies(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "q"},
		{Role: provider.RoleAssistant, Content: "first answer"},
		{Role: provider.RoleAssistant, Content: "second answer"},
	}
	m := newTestChatTUIWithMessages(t, t.TempDir(), msgs...)
	m.openCopyPicker()
	if m.copyPick == nil {
		t.Fatal("picker should open")
	}
	// Down to the older answer, then Enter copies it.
	mdl, _ := m.handleCopyPickerKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mdl.(chatTUI)
	if m.copyPick == nil || m.copyPick.sel != 1 {
		t.Fatalf("down should move selection to index 1, got sel=%d", selOf(m))
	}
	mdl, enterCmd := m.handleCopyPickerKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mdl.(chatTUI)
	if m.copyPick != nil {
		t.Error("Enter should close the picker")
	}
	if enterCmd == nil {
		t.Error("Enter should return a clipboard command")
	}
}

func selOf(m chatTUI) int {
	if m.copyPick == nil {
		return -1
	}
	return m.copyPick.sel
}

// TestExportWritesMarkdownAndStripsReferencedContext verifies /export writes a
// file whose content is the clean transcript: system prompt excluded, the
// "Referenced context" wrapper stripped, user and assistant turns present.
func TestExportWritesMarkdownAndStripsReferencedContext(t *testing.T) {
	ws := t.TempDir()
	m := newTestChatTUIWithMessages(t, ws, sampleExportMessages()...)
	m.runExportCommand("/export")

	// Locate the written file (timestamped name) in the workspace root.
	entries, err := os.ReadDir(ws)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "session-") && strings.HasSuffix(e.Name(), ".md") {
			path = filepath.Join(ws, e.Name())
			break
		}
	}
	if path == "" {
		t.Fatalf("no session-*.md written to %s: %v", ws, entries)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)

	checks := map[string]bool{
		"system prompt excluded": !strings.Contains(got, "system prompt should not export"),
		"has title":              strings.HasPrefix(got, "# hiq session"),
		"user turn present":      strings.Contains(got, "## User"),
		"assistant turn present": strings.Contains(got, "## Assistant") && strings.Contains(got, "hi there"),
		// The wrapper header must be stripped; the user's actual text remains.
		"ref-context stripped": !strings.Contains(got, "Referenced context:"),
		"real user text kept":  strings.Contains(got, "fix the bug"),
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s failed; body:\n%s", name, got)
		}
	}
}

// TestExportEmptySessionWritesNothing verifies an empty (or system-only) session
// produces no file rather than an empty transcript.
func TestExportEmptySessionWritesNothing(t *testing.T) {
	ws := t.TempDir()
	// No messages at all.
	m := newTestChatTUIWithMessages(t, ws)
	m.runExportCommand("/export")
	if entries, _ := os.ReadDir(ws); len(entries) != 0 {
		t.Fatalf("empty session should write no file, got %v", entries)
	}
}

// TestFirstLine truncates long single-line content for the picker preview.
func TestFirstLine(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := firstLine(long)
	if len([]rune(got)) > 80 {
		t.Fatalf("firstLine should cap at 80 runes, got %d", len([]rune(got)))
	}
	if firstLine("\n\n  real  ") != "real" {
		t.Errorf("firstLine should skip leading blank lines")
	}
}
