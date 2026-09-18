// Package calendar provides a local SQLite-backed calendar for Hiq.
// Events are stored in a single user-scoped database (~/.hiq/calendar.db)
// and queried by time range for efficient month/week views. Recurring events
// store an RRULE and expand on read.
package calendar

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Event is the core calendar entity. Times are stored as UTC in SQLite and
// converted to the event's Timezone for display.
type Event struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Description   string    `json:"description"`
	Location      string    `json:"location"`
	StartTime     time.Time `json:"start_time"`
	EndTime       time.Time `json:"end_time"`
	AllDay        bool      `json:"all_day"`
	Timezone      string    `json:"timezone"`
	Color         string    `json:"color"`
	Status        string    `json:"status"` // confirmed / cancelled / tentative
	Source        string    `json:"source"` // manual / email / agent
	Recurrence    string    `json:"recurrence"`
	RecurrenceEnd time.Time `json:"recurrence_end"`
	Reminders     []int     `json:"reminders"` // minutes before
	TaskID        string    `json:"task_id"`
	Tags          []string  `json:"tags"`
	// OutputMode/OutputDest/OutputAccount route the reminder push beyond the
	// desktop toast: "im" pushes to OutputDest (platform:chatID), "email" sends
	// via the named OutputAccount ("" = default). Empty/"" = toast only (the
	// pre-existing behavior). Only events explicitly configured push — see the
	// reminder engine's fire() fan-out.
	OutputMode    string    `json:"output_mode,omitempty"`
	OutputDest    string    `json:"output_dest,omitempty"`
	OutputAccount string    `json:"output_account,omitempty"`
	RemindedAt    time.Time `json:"reminded_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Exception overrides one occurrence of a recurring event.
type Exception struct {
	ID           string    `json:"id"`
	EventID      string    `json:"event_id"`
	OriginalDate time.Time `json:"original_date"` // the date that was overridden
	NewStart     time.Time `json:"new_start"`     // zero = cancelled occurrence
	NewEnd       time.Time `json:"new_end"`
}

// Store manages calendar events in SQLite.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the calendar database at the given path.
// The parent directory is created if missing.
func Open(dbPath string) (*Store, error) {
	if err := mkdirAll(filepath.Dir(dbPath)); err != nil {
		return nil, fmt.Errorf("calendar: mkdir %s: %w", filepath.Dir(dbPath), err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("calendar: open %s: %w", dbPath, err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrate creates tables if they don't exist, and adds columns added in later
// versions via idempotent ALTER TABLE (SQLite has no IF NOT EXISTS for ADD
// COLUMN, so we check PRAGMA table_info first).
func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS events (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT DEFAULT '',
    location TEXT DEFAULT '',
    start_time DATETIME NOT NULL,
    end_time DATETIME NOT NULL,
    all_day INTEGER DEFAULT 0,
    timezone TEXT DEFAULT 'Asia/Shanghai',
    color TEXT DEFAULT '',
    status TEXT DEFAULT 'confirmed',
    source TEXT DEFAULT 'manual',
    recurrence TEXT DEFAULT '',
    recurrence_end DATETIME,
    reminders TEXT DEFAULT '[]',
    task_id TEXT DEFAULT '',
    tags TEXT DEFAULT '[]',
    output_mode TEXT DEFAULT '',
    output_dest TEXT DEFAULT '',
    output_account TEXT DEFAULT '',
    reminded_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_events_time ON events(start_time, end_time);
CREATE INDEX IF NOT EXISTS idx_events_task ON events(task_id);

CREATE TABLE IF NOT EXISTS event_exceptions (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL,
    original_date DATE NOT NULL,
    new_start DATETIME,
    new_end DATETIME,
    FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_exceptions_event ON event_exceptions(event_id);
`)
	if err != nil {
		return err
	}
	// Add output_mode/output_dest/output_account to databases created before
	// these columns existed. Idempotent: each call no-ops when the column is
	// already present. All three ship together, so add them in sequence.
	for _, c := range []struct{ name, decl string }{
		{"output_mode", "TEXT DEFAULT ''"},
		{"output_dest", "TEXT DEFAULT ''"},
		{"output_account", "TEXT DEFAULT ''"},
	} {
		if err := s.addColumnIfMissing("events", c.name, c.decl); err != nil {
			return err
		}
	}
	return nil
}

// addColumnIfMissing adds a column when it isn't already on the table. SQLite
// lacks "ALTER TABLE ... ADD COLUMN IF NOT EXISTS", so we probe table_info.
func (s *Store) addColumnIfMissing(table, column, decl string) error {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("calendar migrate: probe %s.%s: %w", table, column, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil // already present
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl))
	return err
}

// Create inserts a new event. ID is auto-generated if empty.
func (s *Store) Create(e *Event) error {
	if e.ID == "" {
		e.ID = genID()
	}
	now := time.Now().UTC()
	e.CreatedAt = now
	e.UpdatedAt = now
	if e.Timezone == "" {
		e.Timezone = "Asia/Shanghai"
	}
	if e.Status == "" {
		e.Status = "confirmed"
	}
	if e.Source == "" {
		e.Source = "manual"
	}

	remindersJSON, _ := json.Marshal(e.Reminders)
	tagsJSON, _ := json.Marshal(e.Tags)

	_, err := s.db.Exec(`INSERT INTO events
(id, title, description, location, start_time, end_time, all_day, timezone, color, status, source, recurrence, recurrence_end, reminders, task_id, tags, output_mode, output_dest, output_account, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Title, e.Description, e.Location,
		e.StartTime.UTC(), e.EndTime.UTC(), boolToInt(e.AllDay),
		e.Timezone, e.Color, e.Status, e.Source,
		e.Recurrence, nullTime(e.RecurrenceEnd),
		string(remindersJSON), e.TaskID, string(tagsJSON),
		e.OutputMode, e.OutputDest, e.OutputAccount,
		e.CreatedAt, e.UpdatedAt,
	)
	return err
}

// Get returns a single event by ID.
func (s *Store) Get(id string) (*Event, error) {
	row := s.db.QueryRow(`SELECT id, title, description, location, start_time, end_time, all_day, timezone, color, status, source, recurrence, recurrence_end, reminders, task_id, tags, output_mode, output_dest, output_account, reminded_at, created_at, updated_at FROM events WHERE id = ?`, id)
	return scanEvent(row)
}

// Update modifies an existing event. Only non-zero fields are updated.
func (s *Store) Update(e *Event) error {
	e.UpdatedAt = time.Now().UTC()
	remindersJSON, _ := json.Marshal(e.Reminders)
	tagsJSON, _ := json.Marshal(e.Tags)

	_, err := s.db.Exec(`UPDATE events SET
title=?, description=?, location=?, start_time=?, end_time=?, all_day=?, timezone=?, color=?, status=?, source=?, recurrence=?, recurrence_end=?, reminders=?, task_id=?, tags=?, output_mode=?, output_dest=?, output_account=?, updated_at=?
WHERE id=?`,
		e.Title, e.Description, e.Location,
		e.StartTime.UTC(), e.EndTime.UTC(), boolToInt(e.AllDay),
		e.Timezone, e.Color, e.Status, e.Source,
		e.Recurrence, nullTime(e.RecurrenceEnd),
		string(remindersJSON), e.TaskID, string(tagsJSON),
		e.OutputMode, e.OutputDest, e.OutputAccount,
		e.UpdatedAt, e.ID,
	)
	return err
}

// Delete removes an event and its exceptions.
func (s *Store) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM event_exceptions WHERE event_id = ?`, id)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM events WHERE id = ?`, id)
	return err
}

// List returns events whose time range overlaps [since, before).
// For recurring events, the original event is returned (caller must expand).
func (s *Store) List(since, before time.Time) ([]Event, error) {
	rows, err := s.db.Query(`SELECT id, title, description, location, start_time, end_time, all_day, timezone, color, status, source, recurrence, recurrence_end, reminders, task_id, tags, output_mode, output_dest, output_account, reminded_at, created_at, updated_at FROM events WHERE end_time > ? AND start_time < ? ORDER BY start_time`, since.UTC(), before.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// ListAll returns all events (for export).
func (s *Store) ListAll() ([]Event, error) {
	rows, err := s.db.Query(`SELECT id, title, description, location, start_time, end_time, all_day, timezone, color, status, source, recurrence, recurrence_end, reminders, task_id, tags, output_mode, output_dest, output_account, reminded_at, created_at, updated_at FROM events ORDER BY start_time`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// Search returns events whose title or description contains the query string.
func (s *Store) Search(q string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id, title, description, location, start_time, end_time, all_day, timezone, color, status, source, recurrence, recurrence_end, reminders, task_id, tags, output_mode, output_dest, output_account, reminded_at, created_at, updated_at FROM events WHERE title LIKE ? OR description LIKE ? ORDER BY start_time LIMIT ?`,
		"%"+q+"%", "%"+q+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// ListByTaskID returns events linked to a schedule task.
func (s *Store) ListByTaskID(taskID string) ([]Event, error) {
	rows, err := s.db.Query(`SELECT id, title, description, location, start_time, end_time, all_day, timezone, color, status, source, recurrence, recurrence_end, reminders, task_id, tags, output_mode, output_dest, output_account, reminded_at, created_at, updated_at FROM events WHERE task_id = ? ORDER BY start_time`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// --- Exceptions ---

// AddException adds a recurrence exception.
func (s *Store) AddException(ex *Exception) error {
	if ex.ID == "" {
		ex.ID = genID()
	}
	_, err := s.db.Exec(`INSERT INTO event_exceptions (id, event_id, original_date, new_start, new_end) VALUES (?, ?, ?, ?, ?)`,
		ex.ID, ex.EventID, ex.OriginalDate.Format("2006-01-02"), nullTime(ex.NewStart), nullTime(ex.NewEnd))
	return err
}

// GetExceptions returns all exceptions for an event.
func (s *Store) GetExceptions(eventID string) ([]Exception, error) {
	rows, err := s.db.Query(`SELECT id, event_id, original_date, new_start, new_end FROM event_exceptions WHERE event_id = ?`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Exception
	for rows.Next() {
		var ex Exception
		var origDate string
		var newStart, newEnd sql.NullTime
		if err := rows.Scan(&ex.ID, &ex.EventID, &origDate, &newStart, &newEnd); err != nil {
			return nil, err
		}
		ex.OriginalDate, _ = time.Parse("2006-01-02", origDate)
		if newStart.Valid {
			ex.NewStart = newStart.Time
		}
		if newEnd.Valid {
			ex.NewEnd = newEnd.Time
		}
		out = append(out, ex)
	}
	return out, rows.Err()
}

// DeleteException removes an exception.
func (s *Store) DeleteException(id string) error {
	_, err := s.db.Exec(`DELETE FROM event_exceptions WHERE id = ?`, id)
	return err
}

// --- Reminders ---

// DueReminders returns events that have reminders configured. The reminder
// engine expands recurring events and decides per-instance + per-offset whether
// to fire (see ReminderEngine.check), so this query is intentionally broad: it
// returns every non-cancelled event with a non-empty reminders list whose next
// occurrence could fall within the lookahead window. The start_time bounds are
// relaxed to include recurring events whose original start_time is in the past.
func (s *Store) DueReminders(now time.Time) ([]Event, error) {
	rows, err := s.db.Query(`SELECT id, title, description, location, start_time, end_time, all_day, timezone, color, status, source, recurrence, recurrence_end, reminders, task_id, tags, output_mode, output_dest, output_account, reminded_at, created_at, updated_at FROM events WHERE status != 'cancelled' AND reminders != '[]' AND reminders != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// MarkReminded records that the reminder due at remindAt has fired, so a later
// tick won't re-fire the same offset. remindAt is the absolute instant the
// reminder was scheduled for (instance start − offset), NOT time.Now — this
// lets the engine distinguish multiple tiers: a 60-min reminder sets
// reminded_at to (start−60m), and a 15-min reminder (start−15m, which is later)
// still fires because reminded_at (= start−60m) is before its remindAt.
func (s *Store) MarkReminded(id string, remindAt time.Time) error {
	_, err := s.db.Exec(`UPDATE events SET reminded_at = ? WHERE id = ?`, remindAt.UTC(), id)
	return err
}

// --- Helpers ---

func scanEvent(row *sql.Row) (*Event, error) {
	var e Event
	var allDay int
	var recEnd, remindedAt, createdAt, updatedAt sql.NullTime
	var remindersJSON, tagsJSON string
	err := row.Scan(&e.ID, &e.Title, &e.Description, &e.Location,
		&e.StartTime, &e.EndTime, &allDay, &e.Timezone, &e.Color,
		&e.Status, &e.Source, &e.Recurrence, &recEnd,
		&remindersJSON, &e.TaskID, &tagsJSON, &e.OutputMode, &e.OutputDest, &e.OutputAccount, &remindedAt, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	e.AllDay = allDay != 0
	if recEnd.Valid {
		e.RecurrenceEnd = recEnd.Time
	}
	if remindedAt.Valid {
		e.RemindedAt = remindedAt.Time
	}
	if createdAt.Valid {
		e.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		e.UpdatedAt = updatedAt.Time
	}
	_ = json.Unmarshal([]byte(remindersJSON), &e.Reminders)
	_ = json.Unmarshal([]byte(tagsJSON), &e.Tags)
	return &e, nil
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	var out []Event
	for rows.Next() {
		var e Event
		var allDay int
		var recEnd, remindedAt, createdAt, updatedAt sql.NullTime
		var remindersJSON, tagsJSON string
		if err := rows.Scan(&e.ID, &e.Title, &e.Description, &e.Location,
			&e.StartTime, &e.EndTime, &allDay, &e.Timezone, &e.Color,
			&e.Status, &e.Source, &e.Recurrence, &recEnd,
			&remindersJSON, &e.TaskID, &tagsJSON, &e.OutputMode, &e.OutputDest, &e.OutputAccount, &remindedAt, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		e.AllDay = allDay != 0
		if recEnd.Valid {
			e.RecurrenceEnd = recEnd.Time
		}
		if remindedAt.Valid {
			e.RemindedAt = remindedAt.Time
		}
		if createdAt.Valid {
			e.CreatedAt = createdAt.Time
		}
		if updatedAt.Valid {
			e.UpdatedAt = updatedAt.Time
		}
		_ = json.Unmarshal([]byte(remindersJSON), &e.Reminders)
		_ = json.Unmarshal([]byte(tagsJSON), &e.Tags)
		out = append(out, e)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func genID() string {
	return fmt.Sprintf("evt_%d", time.Now().UnixNano())
}

// mkdirAll creates a directory and all parents.
func mkdirAll(path string) error {
	return os.MkdirAll(path, 0o755)
}
