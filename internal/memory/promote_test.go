package memory

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/fairpeer/internal/extensioncontract"
	"github.com/zzycxz/fairpeer/internal/skill"
)

// seedPromotionStore saves a few facts across levels and tags, the material a
// promotion distills.
func seedPromotionStore(t *testing.T) (Store, string) {
	t.Helper()
	root := t.TempDir()
	store := StoreFor(root, filepath.Join(root, "proj"), "dev")
	if store.Dir == "" {
		t.Fatal("store disabled")
	}
	save := func(name, body string, lvl Level, tags string) {
		t.Helper()
		if _, err := store.Save(Memory{
			Name:  name,
			Body:  body,
			Level: lvl,
			Tags:  ParseTags(tags),
		}); err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
	}
	save("prefers-tabs", "用户偏好用 tab 缩进。", LevelGlobal, "style,go")
	save("pg-upsert", "本项目写库一律用 INSERT ... ON CONFLICT，避免先查后写。", LevelProject, "postgres,db")
	save("pg-migration", "迁移脚本放在 db/migrations，命名 0001_init.sql。", LevelProject, "postgres,migration")
	save("scratch-note", "当前任务临时记录，不该进索引。", LevelSession, "tmp")
	return store, root
}

// TestPromotePluginBuildsLoadableArtifact is the end-to-end property that
// matters: a promotion produces an artifact the EXISTING skill loader indexes
// and runs, plus a manifest the kernel's capability contract accepts — with no
// kernel change on either side.
func TestPromotePluginBuildsLoadableArtifact(t *testing.T) {
	store, root := seedPromotionStore(t)

	got, err := PromoteMemory(store, PromoteRequest{
		Kind:        PromotePlugin,
		Name:        "postgres-writes",
		Description: "写库统一用 upsert，迁移脚本放 db/migrations",
		Tag:         "postgres",
		Notes:       "新表先写迁移，再改代码。",
		Version:     "1.2.0",
	})
	if err != nil {
		t.Fatalf("PromoteMemory: %v", err)
	}
	if got.Dir != filepath.Join(root, "plugins", "postgres-writes") {
		t.Fatalf("plugin dir = %q, want under the memory data root", got.Dir)
	}
	for _, p := range []string{got.Skill, got.Manifest, got.Readme} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected artifact file %s: %v", p, err)
		}
	}
	// Only the tagged (L2) facts are folded in; the L1 and L3 facts are not.
	var names []string
	for _, s := range got.Sources {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "pg-migration,pg-upsert" {
		t.Fatalf("sources = %v, want the two postgres-tagged facts in deterministic order", names)
	}
	if len(got.Refs) != 2 {
		t.Fatalf("references = %d, want one per source memory", len(got.Refs))
	}

	// Manifest: parseable, capability key valid per extensioncontract.
	raw, err := os.ReadFile(got.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	var man struct {
		SchemaVersion int                            `json:"schemaVersion"`
		ID            string                         `json:"id"`
		Version       string                         `json:"version"`
		Capabilities  []extensioncontract.Capability `json:"capabilities"`
		Sources       []PromotedSource               `json:"sources"`
		Entrypoints   map[string]string              `json:"entrypoints"`
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if man.SchemaVersion != 1 || man.ID != "memory-postgres-writes" || man.Version != "1.2.0" {
		t.Fatalf("unexpected manifest identity: %+v", man)
	}
	if man.Entrypoints["skill"] != "SKILL.md" {
		t.Fatalf("manifest must point at the skill entrypoint, got %+v", man.Entrypoints)
	}
	if len(man.Sources) != 2 {
		t.Fatalf("manifest sources = %d, want 2", len(man.Sources))
	}
	for _, c := range man.Capabilities {
		if err := c.Validate(); err != nil {
			t.Fatalf("manifest capability must satisfy the kernel contract: %v", err)
		}
		if c.Key.Kind != "skill" || c.Key.Namespace != "memory" {
			t.Fatalf("unexpected capability key %+v", c.Key)
		}
	}

	// The decisive assertion: the untouched skill loader discovers the promoted
	// playbook (so it enters the pinned index and run_skill can invoke it).
	st := skill.New(skill.Options{
		HomeDir:         filepath.Join(root, "home"),
		CustomPaths:     []string{filepath.Join(root, "plugins")},
		DisableBuiltins: true,
		Stderr:          io.Discard,
	})
	sk, ok := st.Read("postgres-writes")
	if !ok {
		t.Fatalf("skill loader did not pick up the promoted playbook; roots=%v", st.Roots())
	}
	if !strings.Contains(sk.Description, "upsert") {
		t.Fatalf("skill description = %q, want the promoted description", sk.Description)
	}
	if !strings.Contains(sk.Body, "pg-upsert") {
		t.Fatalf("promoted body must list its source memories, got:\n%s", sk.Body)
	}
	if !strings.Contains(sk.Body, "使用要点") || !strings.Contains(sk.Body, "新表先写迁移") {
		t.Fatalf("promoted body must carry the human notes, got:\n%s", sk.Body)
	}
	// references/*.md are appended by the loader: the raw material rides along.
	if !strings.Contains(sk.Body, "INSERT ... ON CONFLICT") {
		t.Fatalf("references were not folded into the skill body, got:\n%s", sk.Body)
	}
}

// TestPromoteSkillKindSkipsManifest keeps the simple path simple: a skill
// promotion is just the playbook.
func TestPromoteSkillKindSkipsManifest(t *testing.T) {
	store, root := seedPromotionStore(t)
	got, err := PromoteMemory(store, PromoteRequest{Kind: PromoteSkill, Name: "tabs", Memories: []string{"prefers-tabs"}})
	if err != nil {
		t.Fatalf("PromoteMemory: %v", err)
	}
	if got.Manifest != "" || got.Readme != "" {
		t.Fatalf("skill promotion must not emit a plugin manifest: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "tabs", "SKILL.md")); err != nil {
		t.Fatalf("skill promotion must land under skills/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "tabs", "plugin.json")); err == nil {
		t.Fatal("skill promotion must not write plugin.json")
	}
	// A description was derived from the memory when none was given.
	if !strings.Contains(got.Sources[0].Hook, "tab") {
		t.Fatalf("source hook should carry the memory's first line, got %q", got.Sources[0].Hook)
	}
}

// TestPromoteSelectionGuards covers the error paths that keep a promotion from
// folding unrelated facts together or silently clobbering an artifact.
func TestPromoteSelectionGuards(t *testing.T) {
	store, _ := seedPromotionStore(t)

	if _, err := PromoteMemory(store, PromoteRequest{Name: "x"}); err == nil {
		t.Fatal("an empty selection must be rejected")
	}
	if _, err := PromoteMemory(store, PromoteRequest{Name: "x", Memories: []string{"nope"}}); err == nil {
		t.Fatal("an unknown memory name must be rejected")
	}
	// An unrecognized level string normalizes to L1 rather than failing, so a
	// sloppy tool argument never hides a fact in a narrower bucket.
	norm, err := PromoteMemory(store, PromoteRequest{Kind: PromoteSkill, Name: "l9-normalizes", Level: "l9"})
	if err != nil {
		t.Fatalf("an unknown level must normalize to L1, not fail: %v", err)
	}
	if len(norm.Sources) != 1 || norm.Sources[0].Level != string(LevelGlobal) {
		t.Fatalf("l9 should resolve to the L1 facts, got %+v", norm.Sources)
	}
	if _, err := PromoteMemory(store, PromoteRequest{Name: "x", Query: "zzz-no-such-thing"}); err == nil {
		t.Fatal("a query with no matches must be rejected")
	}
	if _, err := PromoteMemory(store, PromoteRequest{Kind: "bogus", Name: "x", Level: "l1"}); err == nil {
		t.Fatal("an unknown kind must be rejected")
	}
	if _, err := PromoteMemory(store, PromoteRequest{Name: "bad name!", Level: "l1"}); err == nil {
		t.Fatal("an unusable artifact name must be rejected")
	}

	// Overwrite guard: the first promotion wins unless overwrite is requested.
	req := PromoteRequest{Kind: PromoteSkill, Name: "keep", Level: "l1"}
	if _, err := PromoteMemory(store, req); err != nil {
		t.Fatalf("first promotion: %v", err)
	}
	if _, err := PromoteMemory(store, req); err == nil {
		t.Fatal("re-promoting an existing artifact without overwrite must be refused")
	}
	req.Overwrite = true
	if _, err := PromoteMemory(store, req); err != nil {
		t.Fatalf("overwrite promotion: %v", err)
	}
}

// TestPromoteExcludesSessionWorkingMemory pins the L3 rule: working memory for
// one task is never promoted into a durable artifact by default, so a level
// filter is required and L3 facts only join when explicitly named.
func TestPromoteExcludesSessionWorkingMemory(t *testing.T) {
	store, _ := seedPromotionStore(t)
	got, err := PromoteMemory(store, PromoteRequest{Kind: PromoteSkill, Name: "all-global", Level: "l1"})
	if err != nil {
		t.Fatalf("PromoteMemory: %v", err)
	}
	for _, s := range got.Sources {
		if s.Level == string(LevelSession) {
			t.Fatalf("L1 filter must not pull in L3 working memory: %+v", got.Sources)
		}
	}
	if len(got.Sources) != 1 || got.Sources[0].Name != "prefers-tabs" {
		t.Fatalf("L1 selection = %+v, want only prefers-tabs", got.Sources)
	}
	// Explicitly naming an L3 fact is allowed — the user asked for it.
	explicit, err := PromoteMemory(store, PromoteRequest{Kind: PromoteSkill, Name: "scratch", Memories: []string{"scratch-note"}})
	if err != nil {
		t.Fatalf("explicit L3 promotion: %v", err)
	}
	if explicit.Sources[0].Level != string(LevelSession) {
		t.Fatalf("explicit L3 source lost its level: %+v", explicit.Sources)
	}
}

// TestStoreDataRootFollowsRelocation proves promoted artifacts follow a
// relocated memory root — the reason the root is derived rather than hardcoded.
func TestStoreDataRootFollowsRelocation(t *testing.T) {
	store, root := seedPromotionStore(t)
	if got := store.DataRoot(); got != root {
		t.Fatalf("DataRoot() = %q, want %q", got, root)
	}
	if got := (Store{}).DataRoot(); got != "" {
		t.Fatalf("a disabled store has no data root, got %q", got)
	}
}
