package team

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/fairpeer/internal/taskmonitor"
)

// Store persists team projects to a single JSON file, mirroring
// experts.Store / scheduler.Store's pattern (temp file + rename so a crash
// mid-write can't corrupt the file).
//
// One file for all teams is deliberate: the whole set is small (teams + cards),
// and a single atomic rewrite keeps member/task/board mutations consistent
// without a second consistency mechanism.
type Store struct {
	path  string
	mu    sync.Mutex
	teams []Team
}

var (
	// ErrNotFound is returned by mutating operations on a missing team/member/task.
	ErrNotFound = errors.New("team: not found")
	// ErrInvalid is returned for a structurally invalid input.
	ErrInvalid = errors.New("team: invalid input")
)

// NewStore opens (or creates) the store at path, loading any persisted teams.
func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // first run
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, &s.teams); err != nil {
		return fmt.Errorf("team: parse %s: %w", s.path, err)
	}
	// Repair invariants on load so a hand-edited or partially-written file
	// (e.g. two leaders) can't wedge the UI.
	for i := range s.teams {
		s.teams[i].normalize()
	}
	return nil
}

// save writes the whole set atomically. Callers must NOT hold s.mu.
func (s *Store) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.teams, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns all teams (a copy).
func (s *Store) List() []Team {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Team, len(s.teams))
	copy(out, s.teams)
	return out
}

// Get returns one team by ID.
func (s *Store) Get(id string) (Team, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.teams {
		if t.ID == id {
			return t, true
		}
	}
	return Team{}, false
}

// Create adds a team, normalizing + validating it, and returns the stored copy.
func (s *Store) Create(t Team) (Team, error) {
	if strings.TrimSpace(t.Name) == "" {
		return Team{}, fmt.Errorf("%w: team name is required", ErrInvalid)
	}
	now := time.Now().UTC()
	if t.ID == "" {
		t.ID = newID("team")
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	assignMissingIDs(&t)
	t.normalize()
	s.mu.Lock()
	// Reject a duplicate ID rather than silently shadowing an existing team.
	for _, ex := range s.teams {
		if ex.ID == t.ID {
			s.mu.Unlock()
			return Team{}, fmt.Errorf("%w: team %q already exists", ErrInvalid, t.ID)
		}
	}
	s.teams = append(s.teams, t)
	err := s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return Team{}, err
	}
	return t, nil
}

// Update applies mut to a team's copy and persists. mut must not be nil. The
// result is re-normalized (so a mutation can't leave two leaders or an empty
// name).
func (s *Store) Update(id string, mut func(*Team)) (Team, error) {
	if mut == nil {
		return Team{}, fmt.Errorf("%w: nil mutator", ErrInvalid)
	}
	s.mu.Lock()
	idx := -1
	for i := range s.teams {
		if s.teams[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return Team{}, fmt.Errorf("%w: team %q", ErrNotFound, id)
	}
	mut(&s.teams[idx])
	if strings.TrimSpace(s.teams[idx].Name) == "" {
		s.mu.Unlock()
		return Team{}, fmt.Errorf("%w: team name is required", ErrInvalid)
	}
	s.teams[idx].UpdatedAt = time.Now().UTC()
	assignMissingIDs(&s.teams[idx])
	s.teams[idx].normalize()
	out := s.teams[idx]
	err := s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return Team{}, err
	}
	return out, nil
}

// Delete removes a team by ID.
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	for i, t := range s.teams {
		if t.ID == id {
			s.teams = append(s.teams[:i], s.teams[i+1:]...)
			_ = s.saveLocked()
			s.mu.Unlock()
			return true
		}
	}
	s.mu.Unlock()
	return false
}

// --- members ---

// AddMember appends a member to a team (ID assigned when empty). Setting
// IsLeader on the new member demotes any existing leader, preserving the
// single-leader invariant.
func (s *Store) AddMember(teamID string, m Member) (Team, error) {
	if strings.TrimSpace(m.Name) == "" {
		return Team{}, fmt.Errorf("%w: member name is required", ErrInvalid)
	}
	return s.Update(teamID, func(t *Team) {
		if m.ID == "" {
			m.ID = newID("mem")
		}
		m.CreatedAt = time.Now().UTC()
		if m.IsLeader {
			for i := range t.Members {
				t.Members[i].IsLeader = false
			}
		}
		t.Members = append(t.Members, m)
	})
}

// UpdateMember replaces a member's mutable fields by ID.
func (s *Store) UpdateMember(teamID string, m Member) (Team, error) {
	return s.Update(teamID, func(t *Team) {
		for i := range t.Members {
			if t.Members[i].ID != m.ID {
				continue
			}
			created := t.Members[i].CreatedAt
			if m.IsLeader {
				// Demote every other member first so we never end up with two.
				for j := range t.Members {
					if j != i {
						t.Members[j].IsLeader = false
					}
				}
			}
			if m.CreatedAt.IsZero() {
				m.CreatedAt = created
			}
			t.Members[i] = m
			return
		}
	})
}

// RemoveMember drops a member and unassigns any task owned by them (so the
// board never shows a card pointing at a vanished member).
func (s *Store) RemoveMember(teamID, memberID string) (Team, error) {
	return s.Update(teamID, func(t *Team) {
		out := t.Members[:0]
		for _, m := range t.Members {
			if m.ID != memberID {
				out = append(out, m)
			}
		}
		t.Members = out
		for i := range t.Tasks {
			if t.Tasks[i].AssigneeID == memberID {
				t.Tasks[i].AssigneeID = ""
			}
		}
	})
}

// --- tasks ---

// AddTask appends a kanban card. A zero Status defaults to queued so a card
// always lands in a real column.
func (s *Store) AddTask(teamID string, tk Task) (Team, error) {
	if strings.TrimSpace(tk.Title) == "" {
		return Team{}, fmt.Errorf("%w: task title is required", ErrInvalid)
	}
	return s.Update(teamID, func(t *Team) {
		now := time.Now().UTC()
		if tk.ID == "" {
			tk.ID = newID("task")
		}
		if tk.Status == "" {
			tk.Status = taskmonitor.TaskStateQueued
		}
		tk.TeamID = teamID
		if tk.CreatedAt.IsZero() {
			tk.CreatedAt = now
		}
		tk.UpdatedAt = now
		t.Tasks = append(t.Tasks, tk)
	})
}

// UpdateTask replaces a card's mutable fields by ID.
func (s *Store) UpdateTask(teamID string, tk Task) (Team, error) {
	return s.Update(teamID, func(t *Team) {
		for i := range t.Tasks {
			if t.Tasks[i].ID != tk.ID {
				continue
			}
			created := t.Tasks[i].CreatedAt
			tk.TeamID = teamID
			if tk.CreatedAt.IsZero() {
				tk.CreatedAt = created
			}
			if tk.Status == "" {
				tk.Status = t.Tasks[i].Status
			}
			tk.UpdatedAt = time.Now().UTC()
			t.Tasks[i] = tk
			return
		}
	})
}

// MoveTask transitions a card to a new state, rejecting an illegal transition
// (so a done card can't be silently dragged back to doing). It returns the
// refreshed team.
//
// This is the strict, state-level API. Column-level drops (which also need to
// set/clear the assignee, and must tolerate the derived Backlog/Ready columns)
// go through MoveTaskToColumn instead.
func (s *Store) MoveTask(teamID, taskID string, status taskmonitor.TaskState) (Team, error) {
	return s.Update(teamID, func(t *Team) {
		for i := range t.Tasks {
			cur := t.Tasks[i]
			if cur.ID != taskID {
				continue
			}
			if cur.Status != status && !cur.Status.ValidTransition(status) {
				// Illegal transition: leave the card untouched rather than
				// corrupting the lifecycle. The frontend refetches and snaps back.
				return
			}
			cur.Status = status
			if status == taskmonitor.TaskStateRunning {
				cur.Attempts++
				cur.Error = ""
			}
			if status == taskmonitor.TaskStateSucceeded {
				cur.Progress = ""
				cur.Error = ""
			}
			cur.UpdatedAt = time.Now().UTC()
			t.Tasks[i] = cur
			return
		}
	})
}

// RemoveTask deletes a card and drops it from every other card's Deps so no
// dangling dependency remains.
func (s *Store) RemoveTask(teamID, taskID string) (Team, error) {
	return s.Update(teamID, func(t *Team) {
		out := t.Tasks[:0]
		for _, tk := range t.Tasks {
			if tk.ID != taskID {
				out = append(out, tk)
			}
		}
		t.Tasks = out
		for i := range t.Tasks {
			deps := t.Tasks[i].Deps[:0]
			for _, d := range t.Tasks[i].Deps {
				if d != taskID {
					deps = append(deps, d)
				}
			}
			t.Tasks[i].Deps = deps
		}
	})
}

// --- context ---

// SetContext replaces the team blackboard and bumps its version.
func (s *Store) SetContext(teamID string, ctx TeamContext) (Team, error) {
	return s.Update(teamID, func(t *Team) {
		if ctx.Version <= t.Context.Version {
			ctx.Version = t.Context.Version + 1
		}
		t.Context = ctx
	})
}

// --- helpers ---

// assignMissingIDs gives IDs to any member/task that arrived without one.
func assignMissingIDs(t *Team) {
	for i := range t.Members {
		if t.Members[i].ID == "" {
			t.Members[i].ID = newID("mem")
		}
		if t.Members[i].CreatedAt.IsZero() {
			t.Members[i].CreatedAt = time.Now().UTC()
		}
	}
	for i := range t.Tasks {
		if t.Tasks[i].ID == "" {
			t.Tasks[i].ID = newID("task")
		}
		if t.Tasks[i].TeamID == "" {
			t.Tasks[i].TeamID = t.ID
		}
		if t.Tasks[i].Status == "" {
			t.Tasks[i].Status = taskmonitor.TaskStateQueued
		}
		if t.Tasks[i].CreatedAt.IsZero() {
			t.Tasks[i].CreatedAt = time.Now().UTC()
		}
		if t.Tasks[i].UpdatedAt.IsZero() {
			t.Tasks[i].UpdatedAt = t.Tasks[i].CreatedAt
		}
	}
}

// idSeq disambiguates IDs generated within the same nanosecond (common when a
// plan materialises several cards in one pass).
var (
	idMu  sync.Mutex
	idSeq uint64
)

func newID(prefix string) string {
	idMu.Lock()
	idSeq++
	seq := idSeq
	idMu.Unlock()
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixMilli(), seq)
}
