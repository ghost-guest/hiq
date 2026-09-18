package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/tool"
)

// TestLatestCompactionSummary_FindsAndStrips verifies the helper locates the
// most recent summary message, strips the tag wrapper + preamble, and returns
// the body text + its index.
func TestLatestCompactionSummary_FindsAndStrips(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleUser, Content: summaryTagOpen + "\nSummary of earlier conversation (older messages were compacted to save context):\n## Goal\nDo X\n\n## Pending & next step\n- step 1\n" + summaryTagClose},
		{Role: provider.RoleUser, Content: "next"},
	}
	text, idx := latestCompactionSummary(msgs)
	if idx != 2 {
		t.Errorf("idx = %d, want 2", idx)
	}
	want := "## Goal\nDo X\n\n## Pending & next step\n- step 1"
	if text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
}

func TestLatestCompactionSummary_NoneReturnsMinusOne(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "task"},
	}
	text, idx := latestCompactionSummary(msgs)
	if idx != -1 || text != "" {
		t.Errorf("expected (\"\", -1), got (%q, %d)", text, idx)
	}
}

func TestLatestCompactionSummary_NewestWhenMultiple(t *testing.T) {
	// Two summaries; the helper must return the newest (highest index).
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: summaryTagOpen + "\nSummary of earlier conversation (older messages were compacted to save context):\nOLD\n" + summaryTagClose},
		{Role: provider.RoleUser, Content: summaryTagOpen + "\nSummary of earlier conversation (older messages were compacted to save context):\nNEW\n" + summaryTagClose},
	}
	text, idx := latestCompactionSummary(msgs)
	if idx != 1 {
		t.Errorf("idx = %d, want 1 (newest)", idx)
	}
	if text != "NEW" {
		t.Errorf("text = %q, want NEW", text)
	}
}

// TestCompact_FirstTimeUsesFreshPrompt: no prior summary exists, so the
// summarizer must receive the fresh summarySystemPrompt (not the update one)
// and no <previous-summary> wrapper.
func TestCompact_FirstTimeUsesFreshPrompt(t *testing.T) {
	prov := &fakeProvider{reply: "- fresh summary"}
	big := strings.Repeat("detail ", 80)
	sess := &Session{Messages: []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "task " + big},
		{Role: provider.RoleAssistant, Content: big},
		{Role: provider.RoleUser, Content: "next"},
		{Role: provider.RoleAssistant, Content: "ok"},
	}}
	a := New(prov, tool.NewRegistry(), sess, Options{RecentKeep: 2, ArchiveDir: t.TempDir()}, event.Discard)

	if err := a.compact(context.Background(), "manual", "", true); err != nil {
		t.Fatalf("compact: %v", err)
	}

	if len(prov.got) < 2 {
		t.Fatalf("provider got %d messages, want >= 2", len(prov.got))
	}
	sysMsg := provider.ContentString(prov.got[0].Content)
	if !strings.Contains(sysMsg, "You are compacting the earlier part") {
		t.Errorf("first-time compact should use fresh prompt; sys = %q", firstLine(sysMsg))
	}
	if strings.Contains(sysMsg, "previous-summary") {
		t.Errorf("first-time compact must not use update prompt")
	}
	userMsg := provider.ContentString(prov.got[1].Content)
	if strings.Contains(userMsg, "<previous-summary>") {
		t.Errorf("first-time compact must not inject <previous-summary>")
	}
}

// TestCompact_SecondTimeUsesUpdatePrompt: with a prior summary in the pinned
// prefix, the summarizer must receive updateSummarySystemPrompt and the
// previous summary wrapped in <previous-summary>.
func TestCompact_SecondTimeUsesUpdatePrompt(t *testing.T) {
	prov := &fakeProvider{reply: "- updated summary"}
	big := strings.Repeat("detail ", 80)
	prevSummaryBody := "## Goal\nOriginal goal\n\n## Pending & next step\n- step 1"
	sess := &Session{Messages: []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "task"}, // pinned first turn
		// Prior compaction summary sitting in the pinned prefix.
		{Role: provider.RoleUser, Content: summaryTagOpen + "\nSummary of earlier conversation (older messages were compacted to save context):\n" + prevSummaryBody + "\n" + summaryTagClose},
		// New work since the last compaction.
		{Role: provider.RoleAssistant, Content: big},
		{Role: provider.RoleTool, ToolCallID: "1", Name: "bash", Content: big},
		{Role: provider.RoleUser, Content: "next"},
		{Role: provider.RoleAssistant, Content: "ok"},
	}}
	a := New(prov, tool.NewRegistry(), sess, Options{RecentKeep: 2, ArchiveDir: t.TempDir()}, event.Discard)

	if err := a.compact(context.Background(), "manual", "", true); err != nil {
		t.Fatalf("compact: %v", err)
	}

	if len(prov.got) < 2 {
		t.Fatalf("provider got %d messages, want >= 2", len(prov.got))
	}
	sysMsg := provider.ContentString(prov.got[0].Content)
	if !strings.Contains(sysMsg, "updating an existing conversation summary") {
		t.Errorf("second compact should use update prompt; sys = %q", firstLine(sysMsg))
	}
	userMsg := provider.ContentString(prov.got[1].Content)
	if !strings.Contains(userMsg, "<previous-summary>") {
		t.Errorf("second compact must inject <previous-summary> wrapper")
	}
	if !strings.Contains(userMsg, prevSummaryBody) {
		t.Errorf("second compact must include previous summary body in <previous-summary>")
	}
}

// TestCompact_SecondTimeReplacesOldSummary: the prior summary message must be
// REMOVED from the session (not coexist with the new one). After the second
// compact, exactly ONE summary message should remain.
func TestCompact_SecondTimeReplacesOldSummary(t *testing.T) {
	prov := &fakeProvider{reply: "- updated summary"}
	big := strings.Repeat("detail ", 80)
	prevSummaryBody := "## Goal\nOriginal goal"
	sess := &Session{Messages: []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleUser, Content: summaryTagOpen + "\nSummary of earlier conversation (older messages were compacted to save context):\n" + prevSummaryBody + "\n" + summaryTagClose},
		{Role: provider.RoleAssistant, Content: big},
		{Role: provider.RoleTool, ToolCallID: "1", Name: "bash", Content: big},
		{Role: provider.RoleUser, Content: "next"},
		{Role: provider.RoleAssistant, Content: "ok"},
	}}
	a := New(prov, tool.NewRegistry(), sess, Options{RecentKeep: 2, ArchiveDir: t.TempDir()}, event.Discard)

	if err := a.compact(context.Background(), "manual", "", true); err != nil {
		t.Fatalf("compact: %v", err)
	}

	// Exactly one summary message in the final session.
	summaryCount := 0
	var onlySummary string
	for _, m := range sess.Snapshot() {
		if isCompactionSummary(m) {
			summaryCount++
			onlySummary = provider.ContentString(m.Content)
		}
	}
	if summaryCount != 1 {
		t.Fatalf("expected exactly 1 summary after incremental compact, got %d: %+v", summaryCount, sess.Snapshot())
	}
	// The new summary body is present, the old one's marker is gone.
	if !strings.Contains(onlySummary, "updated summary") {
		t.Errorf("new summary not in session: %q", onlySummary)
	}
}

// TestCompact_UpdatePromptStructureMatches: the update prompt must reference
// the same section headings as summarySystemPrompt so the summarizer's output
// stays compatible with the layout the agent expects.
func TestCompact_UpdatePromptStructureMatches(t *testing.T) {
	for _, heading := range []string{
		"## Standing facts & constraints",
		"## Goal",
		"## Decisions & rationale",
		"## Files & code",
		"## Commands & outcomes",
		"## Errors & fixes",
		"## Pending & next step",
	} {
		if !strings.Contains(updateSummarySystemPrompt, heading) {
			t.Errorf("updateSummarySystemPrompt missing heading %q", heading)
		}
		if !strings.Contains(summarySystemPrompt, heading) {
			t.Errorf("summarySystemPrompt missing heading %q (sanity)", heading)
		}
	}
}
