package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/rag"
)

// mustImportRAGDoc writes a doc to temp and imports it into the store.
func mustImportRAGDoc(t *testing.T, s *rag.Store, collection, name, text string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(collection, path, nil); err != nil {
		t.Fatalf("import %s into %s: %v", name, collection, err)
	}
}

// TestNetDevRAGNamespaceIsolation pins NETDEV_SPEC §7.2 in both directions:
//   - cowork rag_search/list never see the "netdev:" namespace (store-level
//     empty-scope exclusion + tool-level refusal of an explicit netdev scope);
//   - the netdev tools are PINNED to the namespace — an ops session never
//     reads office/coding knowledge, and cannot widen its scope by argument.
func TestNetDevRAGNamespaceIsolation(t *testing.T) {
	s := newRAGTestStore(t)
	prev := globalRAGStore
	SetRAGStore(s)
	defer SetRAGStore(prev)

	mustImportRAGDoc(t, s, "office", "meeting.md", "-office-secret- quarterly planning, personnel changes")
	mustImportRAGDoc(t, s, rag.NetDevSubCollection("vrp8"), "vrp8-cmds.md", "-ospf-neighbor-marker- display ospf peer command reference")
	mustImportRAGDoc(t, s, rag.NetDevSubCollection("backups"), "core-sw-1.cfg", "hostname core-sw-1 -backup-marker- ospf 100")

	ctx := context.TODO()

	// 1) Cowork empty scope (all collections): office hit visible, netdev rows absent.
	out, err := ragSearch{}.Execute(ctx, mustMarshalJSON(t, map[string]any{"query": "secret"}))
	if err != nil {
		t.Fatalf("cowork rag_search all-scope: %v", err)
	}
	if !strings.Contains(out, "office-secret") {
		t.Fatalf("cowork search lost its own collection:\n%s", out)
	}
	// The netdev docs' shared token "marker" must yield NO hits on this surface.
	out, err = ragSearch{}.Execute(ctx, mustMarshalJSON(t, map[string]any{"query": "marker"}))
	if err != nil {
		t.Fatalf("cowork rag_search all-scope leak probe: %v", err)
	}
	if strings.Contains(out, "ospf-neighbor-marker") || strings.Contains(out, "backup-marker") {
		t.Fatalf("cowork empty-scope search leaked netdev namespace rows:\n%s", out)
	}

	// 2) Cowork explicitly naming the namespace: refused.
	if _, err := (ragSearch{}).Execute(ctx, mustMarshalJSON(t, map[string]any{"query": "ospf", "collection": "netdev:vrp8"})); err == nil {
		t.Fatal("cowork rag_search must refuse an explicit netdev: collection")
	}
	// 2b) The list surface hides namespace rows entirely.
	out, err = ragList{}.Execute(ctx, mustMarshalJSON(t, map[string]any{}))
	if err != nil {
		t.Fatalf("rag_list: %v", err)
	}
	if strings.Contains(out, "netdev:") {
		t.Fatalf("rag_list leaked netdev namespace rows:\n%s", out)
	}
	if !strings.Contains(out, "office") {
		t.Fatalf("rag_list dropped the office collection:\n%s", out)
	}

	// 3) Netdev whole-namespace search: sees both netdev docs, never the office one.
	out, err = netdevRAGSearch{}.Execute(ctx, mustMarshalJSON(t, map[string]any{"query": "marker"}))
	if err != nil {
		t.Fatalf("netdev_rag_search: %v", err)
	}
	if !strings.Contains(out, "ospf-neighbor-marker") || !strings.Contains(out, "backup-marker") {
		t.Fatalf("netdev namespace search missing its own docs:\n%s", out)
	}
	if strings.Contains(out, "office-secret") {
		t.Fatalf("netdev namespace search leaked the office collection:\n%s", out)
	}

	// 4) Sub-collection narrowing works inside the namespace.
	out, err = netdevRAGSearch{}.Execute(ctx, mustMarshalJSON(t, map[string]any{"query": "ospf", "sub_collection": "vrp8"}))
	if err != nil {
		t.Fatalf("netdev_rag_search sub-collection: %v", err)
	}
	if !strings.Contains(out, "ospf-neighbor-marker") {
		t.Fatalf("netdev sub-collection search missing its doc:\n%s", out)
	}
	// 4b) A sneaky sub_collection trying to escape the namespace lands inside it
	// (netdev:office), finding nothing.
	out, err = netdevRAGSearch{}.Execute(ctx, mustMarshalJSON(t, map[string]any{"query": "marker", "sub_collection": "office"}))
	if err != nil {
		t.Fatalf("netdev_rag_search escaped-scope attempt errored: %v", err)
	}
	if strings.Contains(out, "office-secret") {
		t.Fatalf("netdev sub_collection escaped the namespace:\n%s", out)
	}

	// 5) Netdev import stays inside the namespace.
	tmp := filepath.Join(t.TempDir(), "newdoc.md")
	if err := os.WriteFile(tmp, []byte("-escape-attempt- payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = netdevRAGImport{}.Execute(ctx, mustMarshalJSON(t, map[string]any{"path": tmp, "sub_collection": "office"}))
	if err != nil {
		t.Fatalf("netdev_rag_import: %v", err)
	}
	if !strings.Contains(out, "netdev:office") {
		t.Fatalf("netdev_rag_import wrote outside the namespace: %s", out)
	}
	// …and the cowork surface still cannot see that import.
	out, err = ragList{}.Execute(ctx, mustMarshalJSON(t, map[string]any{}))
	if err != nil {
		t.Fatalf("rag_list after netdev import: %v", err)
	}
	if strings.Contains(out, "netdev:office") {
		t.Fatalf("netdev import leaked into the cowork collection list:\n%s", out)
	}
}
