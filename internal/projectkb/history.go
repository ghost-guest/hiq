package projectkb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Revision history bounds. A snapshot is small (a node index + the rendered
// map), so keeping a couple of months of automatic snapshots is cheap.
const (
	revDirName   = "revisions"
	revIndexFile = "index.json"
	maxRevisions = 60
)

// snapshot is one revision's payload: everything needed to restore the map,
// plus the incremental summary of what this snapshot introduced.
type snapshot struct {
	Revision Revision `json:"revision"`
	Nodes    []Node   `json:"nodes"`
	Map      string   `json:"map"`
	// Summary is the rendered 增量摘要 of the change this snapshot captured.
	// It lives in the snapshot (not in the index) so the revision index stays
	// small while a reader can still ask "what changed in THIS one?".
	Summary string `json:"summary,omitempty"`
	Diff    Diff   `json:"diff,omitempty"`
}

func (h *Hub) revDir() string { return filepath.Join(h.Dir(), revDirName) }

// History returns the revision list, newest first. An unreadable or missing
// index is an empty history rather than an error — history is a convenience.
func (h *Hub) History() []Revision {
	b, err := os.ReadFile(filepath.Join(h.revDir(), revIndexFile))
	if err != nil || len(b) == 0 {
		return nil
	}
	var out []Revision
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// RevisionSummary returns the 增量摘要 recorded with one revision, so the panel
// can show what a snapshot changed without loading its whole payload.
func (h *Hub) RevisionSummary(revisionID string) (string, error) {
	snap, err := h.loadSnapshot(revisionID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(snap.Summary) != "" {
		return snap.Summary, nil
	}
	return snap.Diff.Markdown(0), nil
}

// writeRevision snapshots the current map and prunes the oldest snapshots past
// the cap. Callers must hold h.mu (Sync does).
func (h *Hub) writeRevision(trigger, note string, diff Diff) (string, error) {
	st := h.state
	when := st.Updated
	if when.IsZero() {
		when = time.Now().UTC()
	}
	rev := Revision{
		ID:      revID(when, st.Digest),
		At:      when,
		Trigger: trigger,
		Note:    note,
		Nodes:   len(st.Nodes),
		Digest:  st.Digest,
		Counts:  Counts(st.Nodes),
		Added:   len(diff.Added),
		Changed: len(diff.Changed),
		Removed: len(diff.Removed),
	}
	snap := snapshot{
		Revision: rev,
		Nodes:    st.Nodes,
		Map:      RenderMap(st),
		Summary:  diff.Markdown(0),
		Diff:     diff,
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(h.revDir(), 0o755); err != nil {
		return "", err
	}
	if err := writeFileAtomic(filepath.Join(h.revDir(), rev.ID+".json"), b); err != nil {
		return "", err
	}

	list := append([]Revision{rev}, h.History()...)
	if len(list) > maxRevisions {
		for _, old := range list[maxRevisions:] {
			_ = os.Remove(filepath.Join(h.revDir(), old.ID+".json"))
		}
		list = list[:maxRevisions]
	}
	idx, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(filepath.Join(h.revDir(), revIndexFile), idx); err != nil {
		return "", err
	}
	return rev.ID, nil
}

// Rollback restores the node set (and therefore the map) from a revision and
// persists it. It is the "一键回滚" half of the Wiki-Mode design: an automated
// sync that produced a bad map is one call away from being undone.
//
// The rollback itself is recorded as a fresh revision, so undoing a rollback is
// just another rollback rather than a lost state.
func (h *Hub) Rollback(revisionID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	snap, err := h.loadSnapshot(revisionID)
	if err != nil {
		return err
	}
	// Diff the outgoing state against the restored one FIRST, so the rollback's
	// own revision records what it undid (a rollback that can't say what it
	// changed is just as opaque as the sync it reverts).
	prev := make(map[string]Node, len(h.state.Nodes))
	for _, n := range h.state.Nodes {
		prev[n.ID] = n
	}
	diff := buildDiff(prev, snap.Nodes)

	h.state.Nodes = snap.Nodes
	h.state.Digest = digestOf(snap.Nodes)
	h.state.Updated = time.Now().UTC()
	h.state.CWD = h.cwd
	h.state.Profile = h.profile
	if !diff.IsEmpty() {
		d := diff
		h.state.LastDiff = &d
		h.state.LastChangeAt = h.state.Updated
	}
	// Recompute per-source counts so the panel agrees with the restored map.
	counts := Counts(snap.Nodes)
	for i := range h.state.Sources {
		if h.state.Sources[i].Enabled {
			h.state.Sources[i].Count = counts[string(h.state.Sources[i].Kind)]
		}
	}
	if err := h.persist(); err != nil {
		return err
	}
	_, err = h.writeRevision("rollback", "回滚到 "+revisionID, diff)
	return err
}

// loadSnapshot reads one revision payload, rejecting an unsafe ID.
func (h *Hub) loadSnapshot(revisionID string) (snapshot, error) {
	if !safeRevisionID(revisionID) {
		return snapshot{}, fmt.Errorf("projectkb: invalid revision id %q", revisionID)
	}
	b, err := os.ReadFile(filepath.Join(h.revDir(), revisionID+".json"))
	if err != nil {
		return snapshot{}, fmt.Errorf("projectkb: revision %q not found", revisionID)
	}
	var snap snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return snapshot{}, fmt.Errorf("projectkb: parse revision %q: %w", revisionID, err)
	}
	return snap, nil
}

// revID builds a sortable, collision-resistant revision ID.
func revID(t time.Time, digest string) string {
	return t.UTC().Format("20060102-150405") + "-" + shortDigest(digest)
}

// safeRevisionID bounds an ID to the characters revID emits, so a crafted ID
// can never escape the revisions directory.
func safeRevisionID(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}
