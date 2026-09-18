// Package checkpoint is hiq's snapshot-based edit safety net. Before a writer
// tool changes a file, the agent records the file's pre-edit content here, keyed
// to the current user turn; a frontend can then rewind the workspace (and, via the
// controller, the conversation) to an earlier turn.
//
// It is deliberately git-free (like Claude Code's rewind): snapshots live beside
// the session, never touch the user's git, and work in a non-git directory. Only
// edit-tool changes are tracked — bash side effects are not (a shell command's
// targets can't be known in advance), which is why the capture hook only fires for
// tools that can Preview their change.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/diff"
	fileenc "github.com/zzycxz/hiq/internal/fileutil/encoding"
)

// FileSnap is one file's state at the moment it was first touched in a turn.
// Content == nil means the file did not exist then, so a restore deletes it.
// Perm captures the file's permission bits (e.g. 0755 for scripts) so a restore
// preserves executability instead of forcing everything to 0644.
type FileSnap struct {
	Path     string        `json:"path"`
	Content  *string       `json:"content"`
	Encoding *fileenc.Kind `json:"encoding,omitempty"`
	Perm     *uint32       `json:"perm,omitempty"`
	// Hash is the sha256 of the snapshotted (pre-edit) decoded text — the
	// rewind preview uses it to detect edits made outside this agent.
	Hash string `json:"hash,omitempty"`
	// PostHash is the sha256 of the file's decoded text right after the writer
	// tool that touched it completed (updated on every edit within the turn),
	// so the preview can tell "agent wrote this last" from "changed since".
	PostHash string `json:"postHash,omitempty"`
}

// Checkpoint anchors the pre-edit state of every distinct file touched during one
// user turn. MsgIndex is len(Session.Messages) at the turn's start — the
// conversation-rewind boundary — persisted so a resumed session can rewind the
// conversation and fork, not just the code.
type Checkpoint struct {
	Turn     int        `json:"turn"`
	Time     time.Time  `json:"time"`
	Prompt   string     `json:"prompt"`
	MsgIndex int        `json:"msgIndex"`
	Files    []FileSnap `json:"files"`
}

// Meta is the picker-facing summary of a checkpoint (no file contents).
type Meta struct {
	Turn   int
	Time   time.Time
	Prompt string
	Paths  []string
}

// Store holds a session's checkpoints in memory and, when dir is set, persists one
// JSON file per turn under it (cheap delete, corruption-isolated). All methods are
// safe for concurrent use — the agent snapshots from tool goroutines.
type Store struct {
	dir  string // <session>.ckpt/, or "" for in-memory only
	root string // workspace root, for restore path-escape guards
	max  int    // retention cap; oldest finalized checkpoints are pruned past it

	mu   sync.Mutex
	done []*Checkpoint   // finalized turns
	cur  *Checkpoint     // the active turn's checkpoint
	seen map[string]bool // paths already snapshotted this turn (dedup)
}

// maxCheckpointsPerSession caps how many checkpoint files accumulate per
// session. Without pruning, a long session grows turn-*.json files unboundedly,
// wasting disk. 50 keeps ample undo history while bounding footprint.
const maxCheckpointsPerSession = 50

// New returns a store for the given checkpoint dir and workspace root, loading any
// checkpoints already persisted under dir. A "" dir disables persistence (the
// store still works in memory for the session).
func New(dir, root string) *Store {
	return NewWithLimit(dir, root, maxCheckpointsPerSession)
}

// NewWithLimit is New with a custom retention cap. Event-oriented consumers
// (e.g. the netdev state history, one checkpoint per state transition rather
// than per conversation turn) fire far more often than a chat session and pass
// a larger cap. max < 1 is clamped to 1.
func NewWithLimit(dir, root string, max int) *Store {
	if max < 1 {
		max = 1
	}
	s := &Store{dir: dir, root: root, max: max, seen: map[string]bool{}}
	if dir != "" {
		s.load()
		s.prune()
	}
	return s
}

// prune deletes the oldest checkpoint files (and their in-memory entries) once
// the count exceeds the retention cap. Runs at load and after every Begin; safe
// for a pure in-memory store (dir == "") — it's a no-op there.
func (s *Store) prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
}

// pruneLocked is prune without taking the lock; caller holds s.mu.
func (s *Store) pruneLocked() {
	if len(s.done) <= s.max {
		return
	}
	excess := len(s.done) - s.max
	// done is append-ordered by turn (ascending), so the first `excess` entries
	// are the oldest. Delete their files and slice them off.
	for i := 0; i < excess; i++ {
		if s.dir != "" {
			_ = os.Remove(filepath.Join(s.dir, fmt.Sprintf("turn-%d.json", s.done[i].Turn)))
		}
	}
	s.done = append([]*Checkpoint{}, s.done[excess:]...)
}

func (s *Store) load() {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var c Checkpoint
		if json.Unmarshal(b, &c) == nil {
			s.done = append(s.done, &c)
		}
	}
	sort.Slice(s.done, func(i, j int) bool { return s.done[i].Turn < s.done[j].Turn })
}

// Begin opens a checkpoint for a new user turn, finalizing the previous one. The
// prompt labels it in the picker; msgIndex is the conversation-rewind boundary.
func (s *Store) Begin(turn int, prompt string, msgIndex int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		s.done = append(s.done, s.cur)
	}
	s.cur = &Checkpoint{Turn: turn, Time: time.Now(), Prompt: prompt, MsgIndex: msgIndex}
	s.seen = map[string]bool{}
	s.persist(s.cur)
	s.pruneLocked()
}

// Finalize closes the active checkpoint so it reports its Paths through List and
// participates in restore bookkeeping as a completed entry. Conversation-turn
// consumers leave the turn open until the next Begin (an in-progress turn's
// files must not propagate CanCode); event-oriented consumers (netdev state
// history) finalize immediately after each event so the newest entry is fully
// visible. A no-op when no checkpoint is open.
func (s *Store) Finalize() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return
	}
	s.done = append(s.done, s.cur)
	s.cur = nil
	s.seen = map[string]bool{}
	s.pruneLocked()
}

// Bounds returns turn → MsgIndex over all checkpoints (persisted + current), so
// the controller can rebuild its conversation-rewind boundaries after loading a
// resumed session's checkpoints from disk.
func (s *Store) Bounds() map[int]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := make(map[int]int, len(s.done)+1)
	for _, c := range s.done {
		m[c.Turn] = c.MsgIndex
	}
	if s.cur != nil {
		m[s.cur.Turn] = s.cur.MsgIndex
	}
	return m
}

// Snapshot records the pre-edit state of the file a writer is about to change.
// Only the first touch of a path in the current turn is kept (that is its
// turn-start content). A no-op before the first Begin.
func (s *Store) Snapshot(ch diff.Change) {
	if ch.Path == "" {
		return
	}
	var enc *fileenc.Kind
	if ch.Kind != diff.Create {
		enc = s.detectEncoding(ch.Path)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil || s.seen[ch.Path] {
		return
	}
	s.seen[ch.Path] = true
	var content *string
	if ch.Kind != diff.Create { // create == file didn't exist → leave nil (restore deletes)
		old := ch.OldText
		content = &old
	}
	snap := FileSnap{Path: ch.Path, Content: content, Encoding: enc, Perm: s.detectPerm(ch.Path)}
	if content != nil {
		snap.Hash = hashString(*content)
	}
	s.cur.Files = append(s.cur.Files, snap)
	s.persist(s.cur)
}

func hashString(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// hashFile returns the sha256 of the file's decoded text at abs ("" when the
// file can't be read).
func hashFile(abs string) string {
	b, err := os.ReadFile(abs)
	if err != nil {
		return ""
	}
	enc, _ := fileenc.Detect(b)
	return hashString(string(fileenc.Decode(b, enc)))
}

// NotePostEdit records the file's on-disk content hash after the writer tool
// that touched it completed, on the current turn's snapshot for that path
// (no-op when the path wasn't snapshotted this turn). The rewind preview
// compares it against the current hash to classify a rewind as safe (nothing
// external since the agent's last write) or unsafe.
func (s *Store) NotePostEdit(path string) {
	if path == "" {
		return
	}
	abs, err := safePath(s.root, path)
	if err != nil {
		return
	}
	h := hashFile(abs)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return
	}
	for i := range s.cur.Files {
		if s.cur.Files[i].Path == path {
			s.cur.Files[i].PostHash = h
		}
	}
	s.persist(s.cur)
}

// CurrentHash returns the file's decoded-text hash now ("" when absent or
// unreadable) — the "now" side of the rewind safety classification.
func (s *Store) CurrentHash(path string) string {
	abs, err := safePath(s.root, path)
	if err != nil {
		return ""
	}
	if _, serr := os.Stat(abs); serr != nil {
		return ""
	}
	return hashFile(abs)
}

// SuffixPaths lists every distinct path touched from fromTurn onward — exactly
// the set a code rewind of fromTurn would restore.
func (s *Store) SuffixPaths(fromTurn int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, c := range s.all() {
		if c.Turn < fromTurn {
			continue
		}
		for _, f := range c.Files {
			if !seen[f.Path] {
				seen[f.Path] = true
				out = append(out, f.Path)
			}
		}
	}
	return out
}

// SuffixFileInfo is what the rewind preview needs for one suffix path.
type SuffixFileInfo struct {
	Path     string
	PreHash  string // hash of the earliest snapshot's content
	PostHash string // latest post-edit hash recorded across the suffix
}

// SuffixInfo summarizes the suffix of fromTurn per path: the earliest snapshot
// (what a rewind restores to) and the most recent post-edit hash (the last
// state the agent itself wrote).
func (s *Store) SuffixInfo(fromTurn int) []SuffixFileInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := map[string]*SuffixFileInfo{}
	var order []string
	for _, c := range s.all() {
		if c.Turn < fromTurn {
			continue
		}
		for _, f := range c.Files {
			e, ok := info[f.Path]
			if !ok {
				e = &SuffixFileInfo{Path: f.Path, PreHash: f.Hash}
				info[f.Path] = e
				order = append(order, f.Path)
			}
			if f.PostHash != "" {
				e.PostHash = f.PostHash
			}
		}
	}
	out := make([]SuffixFileInfo, 0, len(order))
	for _, p := range order {
		out = append(out, *info[p])
	}
	return out
}

// KeepCurrent snapshots the CURRENT on-disk content of paths as a finalized
// synthetic checkpoint — the reverse side of a rewind. Restoring back to that
// checkpoint replays (reapplies) the rewound edits; a path absent now is kept
// as a create marker so the replay deletes it again. msgIndex is the
// conversation boundary to record for the synthetic turn. Returns the new
// turn, or -1 when there was nothing to keep.
func (s *Store) KeepCurrent(prompt string, msgIndex int, paths []string) int {
	if len(paths) == 0 {
		return -1
	}
	s.mu.Lock()
	turn := 0
	for _, c := range s.all() {
		if c.Turn >= turn {
			turn = c.Turn + 1
		}
	}
	s.mu.Unlock()

	s.Begin(turn, prompt, msgIndex)
	for _, p := range paths {
		abs, err := safePath(s.root, p)
		if err != nil {
			continue
		}
		data, rerr := os.ReadFile(abs)
		if rerr != nil {
			s.Snapshot(diff.Change{Path: p, Kind: diff.Create})
			continue
		}
		enc, _ := fileenc.Detect(data)
		s.Snapshot(diff.Change{Path: p, Kind: diff.Modify, OldText: string(fileenc.Decode(data, enc))})
	}
	s.Finalize()
	// The kept state IS the agent's last known write for these paths — mark it
	// so a preview of the replay itself classifies as safe.
	s.mu.Lock()
	if n := len(s.done); n > 0 && s.done[n-1].Turn == turn {
		for i := range s.done[n-1].Files {
			s.done[n-1].Files[i].PostHash = s.done[n-1].Files[i].Hash
		}
		s.persist(s.done[n-1])
	}
	s.mu.Unlock()
	return turn
}

func (s *Store) detectEncoding(p string) *fileenc.Kind {
	abs, err := safePath(s.root, p)
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil
	}
	enc, _ := fileenc.Detect(b)
	return &enc
}

// restorePerm resolves the permission bits to use when restoring a file: the
// snapshotted perm if present (preserving executability), else the current
// file's perm (a rollback of a non-exec edit), else the 0644 default. abs is
// the safePath-resolved target — snapshot paths may be root-relative.
func restorePerm(snap FileSnap, abs string) os.FileMode {
	if snap.Perm != nil && *snap.Perm != 0 {
		return os.FileMode(*snap.Perm)
	}
	if info, err := os.Stat(abs); err == nil {
		return info.Mode().Perm()
	}
	return 0o644
}

// detectPerm returns the file's permission bits (e.g. 0755 for an executable
// script) so RestoreCode can preserve them. Returns nil for unreadable/missing
// files (restore then falls back to 0644).
func (s *Store) detectPerm(p string) *uint32 {
	abs, err := safePath(s.root, p)
	if err != nil {
		return nil
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil
	}
	perm := uint32(info.Mode().Perm())
	return &perm
}

func (s *Store) persist(c *Checkpoint) {
	if s.dir == "" {
		return
	}
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		slog.Warn("checkpoint: create dir failed", "dir", s.dir, "err", err)
		return
	}
	if err := os.WriteFile(filepath.Join(s.dir, fmt.Sprintf("turn-%d.json", c.Turn)), b, 0o644); err != nil {
		slog.Warn("checkpoint: persist failed", "turn", c.Turn, "err", err)
	}
}

// NextTurn returns the turn number a new checkpoint should take: one past the
// highest existing turn (0 when empty), so a resumed session keeps numbering
// without colliding with checkpoints loaded from disk.
func (s *Store) NextTurn() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := 0
	for _, c := range s.done {
		if c.Turn >= next {
			next = c.Turn + 1
		}
	}
	if s.cur != nil && s.cur.Turn >= next {
		next = s.cur.Turn + 1
	}
	return next
}

// List returns every checkpoint's metadata, oldest turn first.
func (s *Store) List() []Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meta, 0, len(s.done)+1)
	for _, c := range s.all() {
		paths := make([]string, len(c.Files))
		for i, f := range c.Files {
			paths[i] = f.Path
		}
		// The current in-progress turn's files haven't been committed yet —
		// they must not participate in CanCode propagation.
		if c == s.cur {
			paths = nil
		}
		out = append(out, Meta{Turn: c.Turn, Time: c.Time, Prompt: c.Prompt, Paths: paths})
	}
	return out
}

// all returns done + cur in turn order. Caller holds the lock.
func (s *Store) all() []*Checkpoint {
	cps := append([]*Checkpoint(nil), s.done...)
	if s.cur != nil {
		cps = append(cps, s.cur)
	}
	sort.Slice(cps, func(i, j int) bool { return cps[i].Turn < cps[j].Turn })
	return cps
}

// RestoreCode reverts the workspace to its state at the start of turn `fromTurn`:
// for every file touched in turn fromTurn or later, it writes back that file's
// earliest recorded content (or deletes it when the earliest snapshot was nil).
// Returns the paths written and deleted.
func (s *Store) RestoreCode(fromTurn int) (written, deleted []string, err error) {
	s.mu.Lock()
	// earliest snapshot per path across checkpoints >= fromTurn (turn order → first wins).
	earliest := map[string]FileSnap{}
	order := []string{}
	for _, c := range s.all() {
		if c.Turn < fromTurn {
			continue
		}
		for _, f := range c.Files {
			if _, ok := earliest[f.Path]; ok {
				continue
			}
			earliest[f.Path] = f
			order = append(order, f.Path)
		}
	}
	root := s.root
	s.mu.Unlock()

	for _, p := range order {
		abs, gerr := safePath(root, p)
		if gerr != nil {
			err = gerr
			continue
		}
		snap := earliest[p]
		if snap.Content == nil {
			if rmErr := os.Remove(abs); rmErr == nil {
				deleted = append(deleted, p)
			} else if !os.IsNotExist(rmErr) {
				err = rmErr
			}
			continue
		}
		if mkErr := os.MkdirAll(filepath.Dir(abs), 0o755); mkErr != nil {
			err = mkErr
			continue
		}
		enc := fileenc.UTF8
		if snap.Encoding != nil {
			enc = *snap.Encoding
		} else if current := detectCurrentEncoding(abs); current != nil {
			enc = *current
		}
		if wErr := os.WriteFile(abs, fileenc.Encode(*snap.Content, enc), restorePerm(snap, abs)); wErr != nil {
			err = wErr
			continue
		}
		written = append(written, p)
	}
	return written, deleted, err
}

func detectCurrentEncoding(path string) *fileenc.Kind {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	enc, _ := fileenc.Detect(b)
	return &enc
}

// safePath resolves p against root and rejects anything escaping it — restore
// must never write outside the workspace, even if a snapshot path is hostile or
// the project moved since it was taken. Uses filepath.IsLocal (Go 1.20+) for
// robust rejection of "..", UNC paths, and other platform-specific escape vectors.
func safePath(root, p string) (string, error) {
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, p)
	}
	abs = filepath.Clean(abs)
	if root != "" {
		r := filepath.Clean(root)
		rel, err := filepath.Rel(r, abs)
		if err != nil || !filepath.IsLocal(rel) {
			return "", fmt.Errorf("checkpoint path %q escapes workspace %q", p, root)
		}
	}
	return abs, nil
}

// DiffForTurn (upgrade spec 3-6) renders what a code-scope rewind of `turn`
// would restore: one change per snapshotted file, old side = current on-disk
// content, new side = the snapshotted pre-edit state — so the preview reads as
// "these are the changes the rewind will revert". A create snapshot (nil
// Content) previews as a delete of the current file; a file removed since the
// snapshot previews as a recreate.
func (s *Store) DiffForTurn(turn int) []diff.Change {
	s.mu.Lock()
	var files []FileSnap
	for _, c := range s.all() {
		if c.Turn == turn {
			files = append(files, c.Files...)
		}
	}
	s.mu.Unlock()

	out := make([]diff.Change, 0, len(files))
	for _, f := range files {
		snap := ""
		if f.Content != nil {
			snap = *f.Content
		}
		// Resolve through safePath: snapshot paths may be root-relative, and the
		// process CWD is not necessarily the root (multi-root desktop tabs, the
		// netdev state store rooted at the user config dir).
		abs, perr := safePath(s.root, f.Path)
		if perr != nil {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			// Gone since the snapshot — the rewind would bring it back.
			out = append(out, diff.Build(f.Path, "", snap, diff.Create))
			continue
		}
		enc, _ := fileenc.Detect(data)
		cur := string(fileenc.Decode(data, enc))
		if f.Content == nil {
			out = append(out, diff.Build(f.Path, cur, "", diff.Delete))
			continue
		}
		out = append(out, diff.Build(f.Path, cur, snap, diff.Modify))
	}
	return out
}
