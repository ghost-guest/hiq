package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/rag"
)

// TestRAGSearchSurfacesProvenance confirms the structured (entity/relation)
// layer of rag_search now cites WHERE each fact came from — the source file +
// chunk — instead of hiding the Sources the DB already stores. This is what
// makes the knowledge graph quotable by the agent (the highest-ROI gap).
func TestRAGSearchSurfacesProvenance(t *testing.T) {
	s := newRAGTestStore(t)
	// Seed: 张三 --[负责]--> TestProject, extracted from /docs/spec.md chunk 3.
	src := rag.Source{Path: "/docs/spec.md", Chunk: 3}
	mustUpsertRAG(t, s, "docs",
		rag.Entity{NameRaw: "张三", Type: "person", Description: "技术总监"},
		rag.Relation{Source: "张三", Target: "TestProject", Type: "负责"},
		src)

	prev := globalRAGStore
	SetRAGStore(s)
	defer SetRAGStore(prev)

	args := json.RawMessage(mustMarshalJSON(t, map[string]any{
		"query":      "张三",
		"collection": "docs",
		"top_k":      5,
	}))
	out, err := ragSearch{}.Execute(context.TODO(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// The unwrapped content should cite the source. wrapUntrusted adds a fence;
	// check the inner string.
	body := out
	if !strings.Contains(body, "spec.md#3") {
		t.Errorf("entity output missing provenance citation 'spec.md#3'; got:\n%s", body)
	}
	if !strings.Contains(body, "来源") {
		t.Errorf("output missing the '来源' label; got:\n%s", body)
	}
	if !strings.Contains(body, "溯源文件") {
		t.Errorf("output missing the provenance-file summary; got:\n%s", body)
	}
}

// TestRAGSearchExpandsTopicMembers confirms the cog_rag-style topic expansion:
// when a hit is a topic/event entity, its members (entities linked by
// member_of/part_of) are surfaced as a "成员：" line — "by point to face"
// retrieval over the binary-relation graph (no schema change needed).
func TestRAGSearchExpandsTopicMembers(t *testing.T) {
	s := newRAGTestStore(t)
	src := rag.Source{Path: "/docs/team.md", Chunk: 0}
	// Topic hub + two members linked via member_of.
	mustUpsertRAG(t, s, "docs", rag.Entity{NameRaw: "项目X团队", Type: "topic", Description: "核心团队"}, rag.Relation{}, src)
	s.UpsertEntity("docs", rag.Entity{NameRaw: "张三", Type: "person"}, src)
	s.UpsertEntity("docs", rag.Entity{NameRaw: "李四", Type: "person"}, src)
	s.UpsertRelation("docs", rag.Relation{Source: "张三", Target: "项目X团队", Type: "member_of"}, src)
	s.UpsertRelation("docs", rag.Relation{Source: "李四", Target: "项目X团队", Type: "member_of"}, src)

	prev := globalRAGStore
	SetRAGStore(s)
	defer SetRAGStore(prev)

	args := json.RawMessage(mustMarshalJSON(t, map[string]any{
		"query":      "项目X团队",
		"collection": "docs",
		"top_k":      5,
	}))
	out, err := ragSearch{}.Execute(context.TODO(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "成员：") {
		t.Errorf("topic hit should expand members; got:\n%s", out)
	}
	if !strings.Contains(out, "张三") || !strings.Contains(out, "李四") {
		t.Errorf("members 张三/李四 missing from expansion; got:\n%s", out)
	}
}

// mustUpsertRAG seeds one entity + one relation into a collection from a source.
func mustUpsertRAG(t *testing.T, s *rag.Store, collection string, e rag.Entity, r rag.Relation, src rag.Source) {
	t.Helper()
	if err := s.UpsertEntity(collection, e, src); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertRelation(collection, r, src); err != nil {
		t.Fatal(err)
	}
}
