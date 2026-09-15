package main

// tree_turns_test.go — helper for topic-visibility tests under the "brand-new
// topics stay invisible until their first completed turn" tree rule: seed one
// session carrying a single user turn for a topic so the project tree counts
// it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zzycxz/fairpeer/internal/agent"
	"github.com/zzycxz/fairpeer/internal/provider"
)

// seedTopicTurn writes one session file with a single user message for the
// topic under root, stamped via the .meta sidecar the way Session.Save does,
// so agent.ListSessions reports Turns=1 and the project tree shows the topic.
func seedTopicTurn(t *testing.T, scope, root, topicID, profile string) {
	t.Helper()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "seeded-" + topicID + "-" + time.Now().Format("150405.000000000")
	path := filepath.Join(dir, name+".jsonl")
	msg := provider.Message{Role: provider.RoleUser, Content: "seed turn"}
	line, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := agent.BranchMeta{
		ID:           name,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		Scope:        scope,
		WorkspaceRoot: root,
		TopicID:      topicID,
		Profile:      profile,
		CachedTurns:  1,
		CachedPreview: "seed turn",
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".meta", metaBytes, 0o644); err != nil {
		t.Fatal(err)
	}
}
