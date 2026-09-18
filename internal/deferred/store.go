package deferred

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/filelock"
)

// SchemaVersion is the on-disk shape of a session's deferred-task file. Bump it
// only when a change cannot be read by the previous version.
const SchemaVersion = 1

const storeFileName = "tasks.json"

// DirFor derives a session's deferred-task directory from its transcript path,
// following the same convention as the checkpoint store (…/<id>.jsonl →
// …/<id>.deferred). Empty session path → empty (in-memory only).
func DirFor(sessionPath string) string {
	if strings.TrimSpace(sessionPath) == "" {
		return ""
	}
	return strings.TrimSuffix(sessionPath, ".jsonl") + ".deferred"
}

type storeFile struct {
	SchemaVersion int    `json:"schemaVersion"`
	Revision      int64  `json:"revision"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
	Tasks         []Task `json:"tasks"`
}

// Store is a session's durable deferred-task table. It is safe for concurrent
// use; every mutation rewrites the file atomically under a lock file so a
// second process (or a crash) can never observe a half-written table.
type Store struct {
	dir  string
	path string

	mu     sync.Mutex
	tasks  map[string]Task
	order  []string
	rev    int64
	logf   func(string, ...any)
	nowFn  func() time.Time
	locked bool
}

// OpenStore loads (or initializes) the deferred-task table for a session.
func OpenStore(sessionPath string, logf func(string, ...any)) (*Store, error) {
	dir := DirFor(sessionPath)
	if dir == "" {
		return nil, errors.New("deferred: session path is required")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &Store{
		dir:   dir,
		path:  filepath.Join(dir, storeFileName),
		tasks: map[string]Task{},
		logf:  logf,
		nowFn: time.Now,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("deferred: read %s: %w", s.path, err)
	}
	var f storeFile
	if err := json.Unmarshal(data, &f); err != nil {
		// Never destroy a table we cannot parse: set it aside and start clean
		// so the session keeps working, and leave the bytes for inspection.
		aside := fmt.Sprintf("%s.corrupt-%d", s.path, s.nowFn().Unix())
		if rerr := os.Rename(s.path, aside); rerr == nil {
			s.logf("deferred: %s is unreadable (%v); moved to %s", s.path, err, aside)
		} else {
			s.logf("deferred: %s is unreadable: %v", s.path, err)
		}
		return nil
	}
	s.rev = f.Revision
	for _, t := range f.Tasks {
		if t.ID == "" {
			continue
		}
		if _, dup := s.tasks[t.ID]; !dup {
			s.order = append(s.order, t.ID)
		}
		s.tasks[t.ID] = t
	}
	return nil
}

// saveLocked writes the table atomically. Callers hold s.mu.
func (s *Store) saveLocked() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("deferred: mkdir %s: %w", s.dir, err)
	}
	s.rev++
	items := make([]Task, 0, len(s.order))
	for _, id := range s.order {
		if t, ok := s.tasks[id]; ok {
			items = append(items, t)
		}
	}
	f := storeFile{SchemaVersion: SchemaVersion, Revision: s.rev, UpdatedAt: s.nowFn().UTC().Format(time.RFC3339Nano), Tasks: items}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// Serialize cross-process writers, then swap the file in one rename so a
	// reader never sees a partial table.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	unlock, err := filelock.Acquire(ctx, s.path+".lock")
	if err != nil {
		return fmt.Errorf("deferred: lock %s: %w", s.path, err)
	}
	defer unlock()

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("deferred: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("deferred: rename %s: %w", tmp, err)
	}
	return nil
}

// Path returns the table's file path ("" for an in-memory store).
func (s *Store) Path() string { return s.path }

// WithNow overrides the store's time source. The coordinator passes its own
// clock through so timestamps and pruning age agree with everything else the
// coordinator tracks (and so tests can drive time deterministically).
func (s *Store) WithNow(fn func() time.Time) *Store {
	if fn == nil {
		return s
	}
	s.mu.Lock()
	s.nowFn = fn
	s.mu.Unlock()
	return s
}

// Put inserts or replaces a task (same ID = same logical result, so a retry of
// the producer's bookkeeping must not duplicate the row).
func (s *Store) Put(t Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID == "" {
		return errors.New("deferred: task id is required")
	}
	now := s.nowFn()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	if _, dup := s.tasks[t.ID]; !dup {
		s.order = append(s.order, t.ID)
	} else if prev := s.tasks[t.ID]; prev.Delivered && !t.Delivered {
		// A producer re-reporting an already-delivered result must not resurrect
		// it: keep the delivery bookkeeping, refresh the payload only.
		t.Delivered = true
		t.DeliverySuppressed = prev.DeliverySuppressed
		t.SuppressReason = prev.SuppressReason
	}
	s.tasks[t.ID] = t
	return s.saveLocked()
}

// Query returns a copy of one task.
func (s *Store) Query(id string) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	return t, ok
}

// List returns every task, oldest first.
func (s *Store) List() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Task, 0, len(s.order))
	for _, id := range s.order {
		if t, ok := s.tasks[id]; ok {
			out = append(out, t)
		}
	}
	return out
}

// ListUndelivered returns tasks that still need delivery, oldest first: the
// crash-recovery queue.
func (s *Store) ListUndelivered() []Task {
	var out []Task
	for _, t := range s.List() {
		if t.Deliverable() {
			out = append(out, t)
		}
	}
	return out
}

func (s *Store) update(id string, fn func(*Task)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return fmt.Errorf("deferred: unknown task %q", id)
	}
	fn(&t)
	t.UpdatedAt = s.nowFn()
	s.tasks[id] = t
	return s.saveLocked()
}

// MarkDelivered records a successful hand-off. Idempotent.
func (s *Store) MarkDelivered(id string) error {
	return s.update(id, func(t *Task) { t.Delivered = true; t.Attempts = 0 })
}

// Suppress stops further delivery attempts (parent session gone, attempts
// exhausted, or the task belongs elsewhere).
func (s *Store) Suppress(id, reason string) error {
	return s.update(id, func(t *Task) {
		t.DeliverySuppressed = true
		t.SuppressReason = reason
	})
}

// BumpAttempts increments the attempt counter and returns the new value.
func (s *Store) BumpAttempts(id string) (int, error) {
	var n int
	err := s.update(id, func(t *Task) { t.Attempts++; n = t.Attempts })
	return n, err
}

// PruneDelivered drops delivered/suppressed rows older than age so a long-lived
// session's table does not grow without bound. Returns how many rows went.
func (s *Store) PruneDelivered(age time.Duration) (int, error) {
	if age <= 0 {
		return 0, nil
	}
	cutoff := s.nowFn().Add(-age)
	s.mu.Lock()
	removed := 0
	kept := s.order[:0:0]
	for _, id := range s.order {
		t, ok := s.tasks[id]
		if !ok {
			continue
		}
		if (t.Delivered || t.DeliverySuppressed) && t.UpdatedAt.Before(cutoff) {
			delete(s.tasks, id)
			removed++
			continue
		}
		kept = append(kept, id)
	}
	s.order = kept
	if removed == 0 {
		s.mu.Unlock()
		return 0, nil
	}
	err := s.saveLocked()
	s.mu.Unlock()
	return removed, err
}
