package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/tool"
)

type fakeLedgerEditor struct {
	updates []string
	body    string
}

func (f *fakeLedgerEditor) UpdateLedger(section, content, mode string) (string, error) {
	f.updates = append(f.updates, section+"|"+mode+"|"+content)
	return "ok", nil
}

func (f *fakeLedgerEditor) LedgerSnapshot() string { return f.body }

func TestTaskLedgerToolBasics(t *testing.T) {
	p := taskLedger{}
	if p.Name() != "task_ledger" {
		t.Fatalf("name = %q", p.Name())
	}
	if p.ReadOnly() {
		t.Fatal("task_ledger must be a writer (it rewrites the session's pinned anchor)")
	}
	if p.Description() != (taskLedger{}).Description() {
		t.Fatal("description must be deterministic (cache-stable prompt)")
	}
	if !json.Valid(p.Schema()) {
		t.Fatal("schema must be valid JSON")
	}
}

func TestTaskLedgerWithoutEditor(t *testing.T) {
	p := taskLedger{}
	_, err := p.Execute(context.Background(), json.RawMessage(`{"action":"read"}`))
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("expected graceful unavailable error, got %v", err)
	}
}

func TestTaskLedgerReadAndUpdate(t *testing.T) {
	p := taskLedger{}
	fake := &fakeLedgerEditor{body: "## Goal\nship it"}
	ctx := tool.WithLedgerEditor(context.Background(), fake)

	out, err := p.Execute(ctx, json.RawMessage(`{"action":"read"}`))
	if err != nil || !strings.Contains(out, "ship it") {
		t.Fatalf("read: %q %v", out, err)
	}

	out, err = p.Execute(ctx, json.RawMessage(`{"action":"update","section":"progress","content":"did a thing","mode":"append"}`))
	if err != nil || out != "ok" {
		t.Fatalf("update: %q %v", out, err)
	}
	if len(fake.updates) != 1 || fake.updates[0] != "progress|append|did a thing" {
		t.Fatalf("editor calls = %v", fake.updates)
	}

	// Default mode is replace.
	if _, err := p.Execute(ctx, json.RawMessage(`{"action":"update","section":"goal","content":"x"}`)); err != nil {
		t.Fatalf("default-mode update: %v", err)
	}
	if len(fake.updates) != 2 || fake.updates[1] != "goal|replace|x" {
		t.Fatalf("default mode must be replace; got %v", fake.updates[1])
	}

	// Unknown action errors.
	if _, err := p.Execute(ctx, json.RawMessage(`{"action":"nuke"}`)); err == nil {
		t.Fatal("unknown action must error")
	}
}
