package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zzycxz/hiq/internal/rag"
	"github.com/zzycxz/hiq/internal/tool"
)

// NetDev knowledge-namespace tools (NETDEV_SPEC §7.1/§7.2). The ops tool set
// includes a rag_search scoped to the "netdev:" collection namespace (vendor
// docs, config backups). The boundary is bidirectional:
//
//   - these tools are PINNED to the namespace — the agent cannot widen the
//     scope, so an ops session never reads the office knowledge base;
//   - the cowork rag_* tools refuse/namespace-filter the "netdev:" prefix and
//     the store excludes the namespace from empty-scope searches, so
//     dev/cowork sessions never read ops knowledge.
//
// Import writes only to hiq's local knowledge store (never to a device),
// which is inside the netdev seal's intent — the seal removes network/exec
// write paths, not hiq's own local state (findings/proposals write
// locally the same way).
func NetDevRAGTools() []tool.Tool {
	return []tool.Tool{netdevRAGSearch{}, netdevRAGImport{}}
}

// --- netdev rag_search --------------------------------------------------------

type netdevRAGSearch struct{}

func (netdevRAGSearch) Name() string { return "netdev_rag_search" }

func (netdevRAGSearch) Description() string {
	return "Search the ops knowledge base (vendor docs, config backups — the netdev namespace) for text matching a query. Returns FTS5 snippets with source citations, plus structured entity hits when the collection has been deep-extracted. Scope is PINNED to the netdev namespace: sub_collection narrows within it (e.g. \"vrp8\" → netdev:vrp8); office/coding knowledge bases are not visible here, and this namespace is not visible to them."
}

func (netdevRAGSearch) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "query":{"type":"string","description":"Search terms (a command name, an alarm keyword, a config pattern)"},
  "sub_collection":{"type":"string","description":"Narrow to one netdev sub-collection (empty = the whole netdev namespace)"},
  "top_k":{"type":"integer","description":"Max results per layer (default 5)"}
},
"required":["query"]
}`)
}

func (netdevRAGSearch) ReadOnly() bool { return true }

func (netdevRAGSearch) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query         string `json:"query"`
		SubCollection string `json:"sub_collection"`
		TopK          int    `json:"top_k"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Query) == "" {
		return "", errors.New("query is required")
	}
	if p.TopK <= 0 {
		p.TopK = 5
	}
	s, err := requireRAG()
	if err != nil {
		return "", err
	}
	// Pin the scope to the netdev namespace — regardless of what the caller
	// passes, the effective collection can never leave it.
	collection := rag.NetDevSubCollection(p.SubCollection)
	return runRAGSearch(ctx, s, p.Query, collection, p.TopK)
}

// --- netdev rag_import --------------------------------------------------------

type netdevRAGImport struct{}

func (netdevRAGImport) Name() string { return "netdev_rag_import" }

func (netdevRAGImport) Description() string {
	return "Import a local document (vendor command reference, config export, backup) into the ops knowledge base so netdev_rag_search can cite it. Text-like formats (txt, md, code, csv, json, html); binary Office/PDF formats must be converted to text first. Re-importing the same path replaces it. Writes ONLY to the netdev: namespace of hiq's local knowledge store — never to any device."
}

func (netdevRAGImport) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Absolute path to the document file to import"},
  "sub_collection":{"type":"string","description":"Netdev sub-collection (e.g. \"vrp8\", \"backups\"; default = the namespace root)"}
},
"required":["path"]
}`)
}

func (netdevRAGImport) ReadOnly() bool { return false }

func (netdevRAGImport) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path          string `json:"path"`
		SubCollection string `json:"sub_collection"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return "", errors.New("path is required")
	}
	abs, err := filepath.Abs(p.Path)
	if err != nil {
		return "", err
	}
	s, err := requireRAG()
	if err != nil {
		return "", err
	}
	collection := rag.NetDevSubCollection(p.SubCollection)
	chunks, err := s.Import(collection, abs, nil)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("imported %s into ops collection %q (%d chunks)", abs, collection, chunks), nil
}
