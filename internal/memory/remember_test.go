package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestRememberToolSaves drives the tool the way the agent does — raw JSON args —
// and verifies the fact lands in the store and the index.
func TestRememberToolSaves(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	tl := NewRememberTool(store)

	if tl.Name() != "remember" || tl.ReadOnly() {
		t.Fatalf("unexpected tool identity: name=%q readonly=%v", tl.Name(), tl.ReadOnly())
	}
	if !json.Valid(tl.Schema()) {
		t.Fatal("remember schema is not valid JSON")
	}

	args := []byte(`{"name":"likes-go","body":"Default to Go for backend work."}`)
	out, err := tl.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "Saved L1/global memory") {
		t.Fatalf("a level-less save must land at L1/global; got %q", out)
	}

	list := store.List()
	if len(list) != 1 || list[0].Name != "likes-go" {
		t.Fatalf("memory not saved correctly: %+v", list)
	}
	if LevelOf(list[0]) != LevelGlobal {
		t.Fatalf("expected the saved fact to be L1, got %q", list[0].Level)
	}
	if !strings.Contains(list[0].Body, "Default to Go") {
		t.Fatalf("body not persisted: %q", list[0].Body)
	}
}

// TestRememberToolValidates rejects calls missing required fields rather than
// writing an empty memory.
func TestRememberToolValidates(t *testing.T) {
	tl := NewRememberTool(Store{Dir: t.TempDir()})
	if _, err := tl.Execute(context.Background(), []byte(`{"name":"x"}`)); err == nil {
		t.Fatal("expected error when body is missing")
	}
	if _, err := tl.Execute(context.Background(), []byte(`{"body":""}`)); err == nil {
		t.Fatal("expected error when body is empty")
	}
}

// TestRememberToolQueuesNote verifies a save injects a turn-tail note so the
// fact applies this session, not only the next.
func TestRememberToolQueuesNote(t *testing.T) {
	q := &fakeQueue{}
	ctx := WithQueue(context.Background(), q)
	tl := NewRememberTool(Store{Dir: t.TempDir()})
	if _, err := tl.Execute(ctx, []byte(`{"name":"uses-rmb","body":"Balance is reported in RMB."}`)); err != nil {
		t.Fatal(err)
	}
	if len(q.notes) != 1 || !strings.Contains(q.notes[0], "uses-rmb") {
		t.Fatalf("expected one queued note naming the saved memory, got %v", q.notes)
	}
}
