package projectkb

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Layout constants under <root>/projectkb/<slug>/.
const (
	stateFile = "kb.json"
	mapFile   = "map.md"
	subdir    = "projectkb"
)

// stateVersion is the on-disk schema version. Bumped only on a breaking change;
// an unknown version is still readable (unknown fields are ignored).
const stateVersion = 1

// Hub is the project knowledge hub for one working directory. It owns the
// persisted project map and its revision history; Sync refreshes it from the
// enabled sources.
//
// A Hub is safe for concurrent use: Sync and the mutating setters serialise on
// an internal mutex, and every read accessor returns a copy.
type Hub struct {
	root    string // data root (the memory root)
	cwd     string // project working directory
	profile string // active product mode ("dev" | "cowork" | "")

	mu    sync.Mutex
	state State
}

// New opens (or initialises) the hub for a working directory. It reads any
// persisted map but performs no collection — call Sync for that. An empty root
// is an error; the caller decides whether the hub is available at all.
func New(root, cwd, profile string) (*Hub, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("projectkb: data root is required")
	}
	h := &Hub{
		root:    absOfPath(root),
		cwd:     absOfPath(cwd),
		profile: strings.TrimSpace(profile),
	}
	h.state = h.defaultState()
	if err := h.load(); err != nil {
		return nil, err
	}
	return h, nil
}

// defaultState is a fresh state with every source enabled.
func (h *Hub) defaultState() State {
	srcs := make([]Source, 0, len(Kinds))
	for _, k := range Kinds {
		srcs = append(srcs, Source{Kind: k, Enabled: true})
	}
	return State{Version: stateVersion, Sources: srcs}
}

// Dir is the hub's own directory. Exported so the UI can reveal it.
func (h *Hub) Dir() string {
	return filepath.Join(h.root, subdir, slugify(h.cwd))
}

// CWD is the working directory this hub describes. Exported so a caller can
// watch the same tree the map is built from.
func (h *Hub) CWD() string { return h.cwd }

func (h *Hub) statePath() string { return filepath.Join(h.Dir(), stateFile) }
func (h *Hub) mapPath() string   { return filepath.Join(h.Dir(), mapFile) }

// load reads the persisted state, if any. A missing file is not an error.
func (h *Hub) load() error {
	b, err := os.ReadFile(h.statePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("projectkb: parse %s: %w", h.statePath(), err)
	}
	h.state = h.mergeSources(st)
	return nil
}

// mergeSources reconciles a loaded state's source list with the kinds this
// build knows: unknown kinds are dropped and missing kinds default to enabled,
// so upgrading never silently disables (or resurrects) a source.
func (h *Hub) mergeSources(st State) State {
	have := make(map[Kind]Source, len(st.Sources))
	for _, s := range st.Sources {
		have[s.Kind] = s
	}
	out := make([]Source, 0, len(Kinds))
	for _, k := range Kinds {
		s, ok := have[k]
		if !ok {
			s = Source{Kind: k, Enabled: true}
		}
		s.Kind = k
		out = append(out, s)
	}
	st.Sources = out
	if st.Version == 0 {
		st.Version = stateVersion
	}
	return st
}

// State returns a copy of the persisted state.
func (h *Hub) State() State {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.copyState()
}

func (h *Hub) copyState() State {
	st := h.state
	st.Nodes = append([]Node(nil), h.state.Nodes...)
	st.Sources = append([]Source(nil), h.state.Sources...)
	return st
}

// Nodes returns a copy of the indexed nodes.
func (h *Hub) Nodes() []Node { return h.State().Nodes }

// Sources returns a copy of the source switches.
func (h *Hub) Sources() []Source { return h.State().Sources }

// Map returns the rendered map, preferring the on-disk artifact (so an
// out-of-band edit is honoured) and falling back to a fresh render.
func (h *Hub) Map() string {
	if b, err := os.ReadFile(h.mapPath()); err == nil && len(b) > 0 {
		return string(b)
	}
	return RenderMap(h.State())
}

// Index renders the compact prompt index from the persisted state, without
// collecting anything. Safe and fast to call at boot.
func (h *Hub) Index(maxChars int) string {
	return RenderIndex(h.State(), maxChars)
}

// SetSource toggles a source and persists the switch. The map is not rebuilt —
// the caller runs Sync for that (so a switch is cheap and can be batched).
func (h *Hub) SetSource(kind Kind, enabled bool) error {
	if !kind.Valid() {
		return fmt.Errorf("projectkb: unknown source %q", kind)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.state.Sources {
		if h.state.Sources[i].Kind == kind {
			h.state.Sources[i].Enabled = enabled
			if !enabled {
				h.state.Sources[i].Count = 0
				h.state.Sources[i].Note = "已关闭"
			} else {
				h.state.Sources[i].Note = ""
			}
		}
	}
	return h.persist()
}

// Sync collects every enabled source, diffs against the persisted node set and
// rewrites the map. When the map genuinely moved it also writes a revision
// snapshot, which is what makes the history (and Rollback) meaningful.
//
// The returned Report carries both the counters and the structured Diff (+ its
// rendered summary), so every caller — the panel, a tool, the watcher — can say
// WHAT changed, not merely that something did.
func (h *Hub) Sync(trigger string) (Report, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	prev := make(map[string]Node, len(h.state.Nodes))
	for _, n := range h.state.Nodes {
		prev[n.ID] = n
	}

	var nodes []Node
	var sources []Source
	for _, s := range h.state.Sources {
		next := s
		if !s.Enabled {
			next.Count = 0
			next.Note = "已关闭"
			sources = append(sources, next)
			continue
		}
		got := h.collect(s.Kind)
		next.Count = len(got)
		next.Note = ""
		sources = append(sources, next)
		nodes = append(nodes, got...)
	}
	sortNodes(nodes)

	diff := buildDiff(prev, nodes)
	rep := Report{
		At:    time.Now().UTC(),
		Total: len(nodes),
		Diff:  diff,
	}
	seen := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		seen[n.ID] = true
		old, ok := prev[n.ID]
		switch {
		case !ok:
			rep.Added++
		case old.FP != n.FP:
			rep.Changed++
		default:
			rep.Unchanged++
		}
	}
	for id := range prev {
		if !seen[id] {
			rep.Removed++
		}
	}

	digest := digestOf(nodes)
	rep.Digest = digest
	moved := digest != h.state.Digest
	if moved {
		rep.Summary = diff.Markdown(0)
	}

	h.state.Nodes = nodes
	h.state.Sources = sources
	h.state.Digest = digest
	h.state.CWD = h.cwd
	h.state.Profile = h.profile
	h.state.Updated = rep.At
	// Only a sync that actually moved the map updates the "last change" record,
	// so a no-op poll cannot erase what the reader still needs to see.
	if moved {
		d := diff
		h.state.LastDiff = &d
		h.state.LastChangeAt = rep.At
	}

	if err := h.persist(); err != nil {
		return rep, err
	}
	if moved {
		if id, err := h.writeRevision(trigger, "", diff); err == nil {
			rep.Revision = id
		}
	}
	return rep, nil
}

// collect dispatches to the collector for one kind.
func (h *Hub) collect(kind Kind) []Node {
	switch kind {
	case KindCode:
		return collectCode(h.cwd)
	case KindDoc:
		return collectDocs(h.cwd)
	case KindMemory:
		return collectMemories(h.root, h.cwd, h.profile)
	case KindTeam:
		return collectTeams(h.root)
	default:
		return nil
	}
}

// persist writes map.md then kb.json, both atomically. The map is written
// first so a crash between the two leaves a readable map and a stale state
// (which the next sync repairs) rather than the reverse.
func (h *Hub) persist() error {
	if err := os.MkdirAll(h.Dir(), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(h.mapPath(), []byte(RenderMap(h.state))); err != nil {
		return err
	}
	b, err := json.MarshalIndent(h.state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(h.statePath(), b)
}

// LoadIndex renders the compact project-map index for a working directory by
// reading only the persisted state. It is the boot-time entry point: it never
// scans the filesystem, so folding it into the system prompt costs nothing, and
// it returns "" when the hub has never synced.
func LoadIndex(root, cwd, profile string, maxChars int) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	h, err := New(root, cwd, profile)
	if err != nil {
		return ""
	}
	return h.Index(maxChars)
}

// ComposeIndex folds the project-map index onto a base system prompt, mirroring
// memory.Compose.
//
// Base stays first so it remains a valid cache prefix even when the map
// changes. With no persisted map (a project that has never synced) base is
// returned byte-for-byte unchanged — that is what keeps the injected prefix
// maximal for anyone not using the hub. The index is deterministic for a given
// kb.json, so within a session the prefix stays byte-stable: a mid-session
// kb_sync folds in on the NEXT session, exactly like memory, and the map body
// is always fetched on demand with kb_map / kb_search.
func ComposeIndex(base, root, cwd, profile string, maxChars int) string {
	block := LoadIndex(root, cwd, profile, maxChars)
	if strings.TrimSpace(block) == "" {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return block
	}
	return strings.TrimRight(base, "\n") + "\n\n" + block
}

// writeFileAtomic writes b to path via a temp file + rename.
func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
