package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/rag"
)

// TestRAGMindMapBuildsTree confirms rag_mindmap walks the entity graph outward
// from a root and compiles it into a mind-map Markdown file. We seed a store
// with a small graph (张三→TestProject→RAG模块), set it as the global RAG store,
// run the tool, and check the output reflects the relations.
func TestRAGMindMapBuildsTree(t *testing.T) {
	s := newRAGTestStore(t)
	// Seed: 张三 --[负责]--> TestProject; TestProject --[包含]--> RAG模块.
	src := rag.Source{Path: "/doc.md", Chunk: 0}
	if err := s.UpsertEntity("docs", rag.Entity{NameRaw: "张三", Type: "person", Description: "技术总监"}, src); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertEntity("docs", rag.Entity{NameRaw: "TestProject", Type: "project"}, src); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertEntity("docs", rag.Entity{NameRaw: "RAG模块", Type: "module"}, src); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertRelation("docs", rag.Relation{Source: "张三", Target: "TestProject", Type: "负责"}, src); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertRelation("docs", rag.Relation{Source: "TestProject", Target: "RAG模块", Type: "包含"}, src); err != nil {
		t.Fatal(err)
	}

	prev := globalRAGStore
	SetRAGStore(s)
	defer SetRAGStore(prev)

	dir := t.TempDir()
	out := filepath.Join(dir, "graph.md")
	args := mustMarshalJSON(t, map[string]any{
		"root":       "张三",
		"collection": "docs",
		"path":       out,
		"depth":      3,
	})
	msg, err := ragMindMap{}.Execute(context.TODO(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(msg, "wrote") {
		t.Fatalf("unexpected output: %s", msg)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	got := string(data)
	// Root = 张三 (H1), branch = [负责] testproject (H2), nested = [包含] rag模块 (H3).
	// Entity names are normalized to lowercase by the SIMPLE merge key, so the
	// display forms here are lowercased (the raw form is kept in the DB but
	// RelationsOf returns the normalized name).
	for _, want := range []string{"# 张三", "[负责] testproject", "[包含] rag模块"} {
		if !strings.Contains(got, want) {
			t.Errorf("mindmap missing %q\ngot:\n%s", want, got)
		}
	}
}

// TestRAGMindMapCycleGuard confirms a cycle (A→B→A) doesn't loop forever — the
// revisited node is rendered once with a "见上文" note and not recursed.
func TestRAGMindMapCycleGuard(t *testing.T) {
	s := newRAGTestStore(t)
	src := rag.Source{Path: "/c.md", Chunk: 0}
	s.UpsertEntity("docs", rag.Entity{NameRaw: "A"}, src)
	s.UpsertEntity("docs", rag.Entity{NameRaw: "B"}, src)
	s.UpsertRelation("docs", rag.Relation{Source: "A", Target: "B", Type: "knows"}, src)
	s.UpsertRelation("docs", rag.Relation{Source: "B", Target: "A", Type: "knows"}, src) // cycle back

	prev := globalRAGStore
	SetRAGStore(s)
	defer SetRAGStore(prev)

	dir := t.TempDir()
	out := filepath.Join(dir, "cycle.md")
	_, err := ragMindMap{}.Execute(context.TODO(), mustMarshalJSON(t, map[string]any{
		"root": "A", "collection": "docs", "path": out, "depth": 5,
	}))
	if err != nil {
		t.Fatalf("execute (should not loop): %v", err)
	}
	data, _ := os.ReadFile(out)
	if !strings.Contains(string(data), "见上文") {
		t.Error("cycle guard should emit a '见上文' note for the revisited node")
	}
}

// TestRAGMindMapNoEntities confirms the tool returns a clear message when the
// collection hasn't been extracted yet (graceful, not an error).
func TestRAGMindMapNoEntities(t *testing.T) {
	s := newRAGTestStore(t) // empty store
	prev := globalRAGStore
	SetRAGStore(s)
	defer SetRAGStore(prev)
	out, err := ragMindMap{}.Execute(context.TODO(), mustMarshalJSON(t, map[string]any{
		"root": "x", "collection": "docs", "path": "/tmp/m.md",
	}))
	if err != nil {
		t.Fatalf("expected graceful message, got error: %v", err)
	}
	if !strings.Contains(out, "no entities") {
		t.Errorf("output = %q, want 'no entities' message", out)
	}
}

// --- helpers ----------------------------------------------------------------

// newRAGTestStore opens a temp rag.Store for tool-level tests (the rag package
// has its own test store; here we just need a real one to seed + query).
func newRAGTestStore(t *testing.T) *rag.Store {
	t.Helper()
	s, err := rag.Open(t.TempDir() + "/rag.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// mustMarshalJSON is a tiny test helper (the package's existing mustJSON takes
// a string and returns []byte for patches — different signature).
func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
