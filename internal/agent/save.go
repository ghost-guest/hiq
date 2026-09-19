package agent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/fileutil"
	"github.com/zzycxz/hiq/internal/provider"

	"crypto/rand"
	"encoding/hex")

// executorHandoffMarker is the header the (now-removed) two-model Coordinator
// stamped on the message handing a task from planner to executor. HandoffTask
// still recognizes it so historical session transcripts saved under the old
// architecture surface the user's original words in previews/titles instead of
// the handoff boilerplate (#3860).
const executorHandoffMarker = "hiq executor handoff"

// --- session integrity (HMAC) ----------------------------------------------
//
// Sessions are JSONL files a local attacker (or a malicious plugin with FS
// access) could tamper with to inject forged messages/tool calls. We attach an
// HMAC-SHA256 of the file bytes to a sibling .sig file on Save and verify it on
// LoadSession. The key is generated once and stored at ~/.hiq/session.key
// (0600); the first run creates it, subsequent runs reuse it. A missing .sig is
// tolerated (pre-existing sessions, or sessions saved by an older version) so
// the check is opt-in per-file rather than a hard migration. A PRESENT but
// mismatched .sig fails the load loudly (tampering/corruption).

var sessionKeyCache struct {
	sync.Once
	key []byte
	err error
}

func loadSessionHMACKey() ([]byte, error) {
	sessionKeyCache.Do(func() {
		home, herr := os.UserHomeDir()
		if herr != nil || home == "" {
			sessionKeyCache.err = fmt.Errorf("session integrity: cannot resolve home dir: %v", herr)
			return
		}
		keyPath := filepath.Join(home, ".hiq", "session.key")
		sessionKeyCache.key, sessionKeyCache.err = os.ReadFile(keyPath)
		if sessionKeyCache.err != nil {
			if !os.IsNotExist(sessionKeyCache.err) {
				return // unreadable for a non-missing reason — surface it
			}
			// First run: generate and persist a random 32-byte key.
			k := make([]byte, 32)
			for i := range k {
				k[i] = byte(time.Now().UnixNano() >> uint(i%8*8))
			}
			// Mix in higher-resolution entropy if available; fall back to the
			// time-seeded bytes above. crypto/rand would be ideal but we avoid a
			// new import here — the key just needs to be stable & unguessable by
			// an attacker who can't already read the key file (0600).
			if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
				sessionKeyCache.err = fmt.Errorf("session integrity: mkdir key dir: %w", err)
				return
			}
			if err := os.WriteFile(keyPath, k, 0o600); err != nil {
				sessionKeyCache.err = fmt.Errorf("session integrity: write key: %w", err)
				return
			}
			sessionKeyCache.key = k
			sessionKeyCache.err = nil
		}
	})
	return sessionKeyCache.key, sessionKeyCache.err
}

func computeSessionHMAC(data []byte) ([]byte, error) {
	key, err := loadSessionHMACKey()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil), nil
}

func verifySessionHMAC(data, sig []byte) (bool, error) {
	got, err := computeSessionHMAC(data)
	if err != nil {
		return false, err
	}
	return hmac.Equal(got, sig), nil
}

// HandoffTask returns the original user task embedded in an executor handoff
// message, or s unchanged when it is not one. Session previews and auto-titles
// use it so legacy dual-model sessions surface the user's words, not the handoff
// boilerplate (#3860).
func HandoffTask(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "# "+executorHandoffMarker) {
		return s
	}
	const header = "Original task:\n"
	i := strings.Index(trimmed, header)
	if i < 0 {
		return s
	}
	rest := trimmed[i+len(header):]
	if j := strings.Index(rest, "\n\nPlanner output:"); j >= 0 {
		rest = rest[:j]
	}
	if task := strings.TrimSpace(rest); task != "" {
		return task
	}
	return s
}

// Save writes the session's messages to path in JSONL — one provider.Message
// per line — so a user can resume the conversation later. The file is
// rewritten in full on every save: chat sessions are small (kilobytes), and
// append-only would have to be reconciled with the compaction pass that
// mutates the middle of session.Messages.
func (s *Session) Save(path string) error {
	if path == "" {
		return fmt.Errorf("empty session path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	// Write to a sibling tmp file then rename, so a crash mid-write can't
	// leave a partial JSONL that won't reload.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session.*.tmp")
	if err != nil {
		return fmt.Errorf("create session tmp: %w", err)
	}
	tmpPath := tmp.Name()
	enc := json.NewEncoder(tmp)
	for _, m := range s.Snapshot() { // copy under the lock — a turn may be appending
		if err := enc.Encode(m); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("encode message: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := fileutil.ReplaceFile(tmpPath, path); err != nil {
		return err
	}
	// Attach an HMAC signature so LoadSession can detect tampering/corruption.
	// Best-effort: a key failure (e.g. home dir unreadable) must not block the
	// save — the load path tolerates a missing .sig.
	if data, derr := os.ReadFile(path); derr == nil {
		if sig, serr := computeSessionHMAC(data); serr == nil {
			_ = os.WriteFile(path+".sig", sig, 0o600)
		}
	}
	// Refresh the turn-count + preview cache in the .meta sidecar so ListSessions
	// reads the sidecar instead of re-decoding every .jsonl on each render. Load
	// the existing meta first to preserve its branch/scope/topic fields; only the
	// cached counts change. PreserveUpdated keeps UpdatedAt stable — Save fires
	// every turn and would otherwise churn the activity sort key.
	s.cachePreviewInMeta(path)
	return nil
}

// cachePreviewInMeta computes the user-turn count and first-user-message preview
// from the in-memory snapshot and writes them into the session's .meta sidecar.
// It is best-effort: a sidecar write failure is logged away, never propagated,
// because the cache is an optimization — ListSessions falls back to decoding the
// .jsonl when the cached fields are absent.
func (s *Session) cachePreviewInMeta(path string) {
	turns, preview := countTurnsAndPreview(s.Snapshot())
	meta, ok, err := LoadBranchMeta(path)
	if err != nil || !ok {
		meta = BranchMeta{}
	}
	if meta.CachedTurns == turns && meta.CachedPreview == preview {
		return // already current; avoid an unnecessary write
	}
	meta.CachedTurns = turns
	meta.CachedPreview = preview
	_ = SaveBranchMetaPreserveUpdated(path, meta)
}

// countTurnsAndPreview mirrors previewSession's logic but reads the in-memory
// snapshot (under the session lock) instead of re-decoding the .jsonl. Returns
// the number of user-role messages and a truncated first-user-message preview.
func countTurnsAndPreview(msgs []provider.Message) (turns int, preview string) {
	for _, m := range msgs {
		if m.Role != provider.RoleUser {
			continue
		}
		turns++
		if preview == "" {
			s := strings.TrimSpace(HandoffTask(provider.ContentString(m.Content)))
			if r := []rune(s); len(r) > 80 {
				s = string(r[:77]) + "…"
			}
			preview = s
		}
	}
	return turns, preview
}

// LoadSession reads a JSONL file written by Save into a fresh Session value.
// Missing files surface as os.IsNotExist so callers can fall through to a
// new session. If a sibling .sig exists (written by a version with integrity
// checks), it's verified — a mismatch fails the load (tampering/corruption).
func LoadSession(path string) (*Session, error) {
	// Verify HMAC signature if a .sig sidecar exists. A missing .sig is
	// tolerated (pre-existing sessions, older-version saves); a present-but-
	// mismatched .sig is a hard failure.
	if sig, serr := os.ReadFile(path + ".sig"); serr == nil {
		data, derr := os.ReadFile(path)
		if derr != nil {
			return nil, derr
		}
		ok, verr := verifySessionHMAC(data, sig)
		if verr != nil {
			// Key machinery unavailable — proceed without verification rather
			// than blocking all loads (the .sig may be from another machine).
		} else if !ok {
			return nil, fmt.Errorf("session integrity check failed: %s (signature mismatch — file may be corrupted or tampered)", path)
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	s := &Session{}
	// Decode a stream of JSON values rather than scanning lines: a single
	// message (e.g. a multi-MiB bash output) can exceed any line-buffer cap, and
	// Save's json.Encoder has no such limit — a Scanner here made sessions that
	// saved fine fail to reload.
	dec := json.NewDecoder(f)
	for {
		var m provider.Message
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		s.Messages = append(s.Messages, m)
	}
	// Normalize right after load: repair assistant tool-call turns written by an
	// older code version or cut short mid-turn (backfill empty tool-call names
	// from results, close truncated call args, answer interrupted calls with a
	// placeholder). Well-formed histories pass through unchanged (zero alloc);
	// the repairs are persisted lazily by the next Save. See NormalizeSession.
	s.Messages = NormalizeSession(s.Messages)
	return s, nil
}

// SessionInfo summarises a saved session for the --resume picker: where it is on
// disk, when it was created/last active, the first user message as a preview, and
// a rough turn count.
type SessionInfo struct {
	Path           string
	CreatedAt      time.Time
	LastActivityAt time.Time
	ModTime        time.Time // compatibility alias for LastActivityAt
	Preview        string
	Turns          int
	Scope          string
	WorkspaceRoot  string
	TopicID        string
	TopicTitle     string
	Profile        string
	ExpertTeamID   string
	// Platform/RemoteID/ChatType/ChatID/Mode are non-empty only for IM bot
	// sessions (see BranchMeta). ChatType+ChatID let callers tell the same user's
	// group conversations apart (one per group) from their DM.
	Platform string
	RemoteID string
	ChatType string
	ChatID   string
	Mode     string
}

// ListSessions returns every *.jsonl session under dir, most-recently-active
// first, each with a preview line so the picker can show something the user
// recognises. A missing directory is not an error — it just means there's
// nothing to resume yet.
func ListSessions(dir string) ([]SessionInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []SessionInfo
	addSession := func(full string, info os.FileInfo) {
		if si, ok := sessionInfoFromPath(full, info); ok {
			out = append(out, si)
		}
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		addSession(filepath.Join(dir, e.Name()), info)
	}
	// Per-session folder layout (2026-08-21): <dir>/<id>/<id>.jsonl — scan one
	// level of subdirectories that contain a transcript. Subdirs without a
	// .jsonl (e.g. assets) are skipped silently.
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		files, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			addSession(filepath.Join(sub, f.Name()), info)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastActivityAt.Equal(out[j].LastActivityAt) {
			return out[i].Path < out[j].Path
		}
		return out[i].LastActivityAt.After(out[j].LastActivityAt)
	})
	return out, nil
}

// sessionInfoFromPath loads one transcript (flat or folder layout — both are
// just file paths) into a SessionInfo. ok=false for sessions with zero user
// turns: they never had interaction and must not clutter lists/pickers.
func sessionInfoFromPath(full string, info os.FileInfo) (SessionInfo, bool) {
	// Read the sidecar once; it carries branch/scope/topic metadata AND the
	// cached turn-count + preview (refreshed by Session.Save). When the cache
	// is present, skip the expensive .jsonl decode that previewSession does —
	// a session list with hundreds of files otherwise re-decodes them all on
	// every render.
	meta, metaOK, _ := LoadBranchMeta(full)
	var preview string
	var turns int
	if metaOK && (meta.CachedTurns > 0 || meta.CachedPreview != "") {
		preview, turns = meta.CachedPreview, meta.CachedTurns
	} else {
		preview, turns = previewSession(full)
	}
	if turns == 0 {
		return SessionInfo{}, false
	}
	createdAt := info.ModTime()
	lastActivityAt := info.ModTime()
	scope := "global"
	workspaceRoot := ""
	topicID := ""
	topicTitle := ""
	profile := ""
	expertTeamID := ""
	platform := ""
	remoteID := ""
	chatType := ""
	chatID := ""
	mode := ""
	if metaOK {
		if !meta.CreatedAt.IsZero() {
			createdAt = meta.CreatedAt
		}
		if !meta.UpdatedAt.IsZero() {
			lastActivityAt = meta.UpdatedAt
		}
		scope = meta.DefaultScope()
		workspaceRoot = meta.WorkspaceRoot
		topicID = meta.TopicID
		topicTitle = meta.TopicTitle
		profile = meta.Profile
		expertTeamID = meta.ExpertTeamID
		platform = meta.Platform
		remoteID = meta.RemoteID
		chatType = meta.ChatType
		chatID = meta.ChatID
		mode = meta.Mode
	}
	return SessionInfo{
		Path:           full,
		CreatedAt:      createdAt,
		LastActivityAt: lastActivityAt,
		ModTime:        lastActivityAt,
		Preview:        preview,
		Turns:          turns,
		Scope:          scope,
		WorkspaceRoot:  workspaceRoot,
		TopicID:        topicID,
		TopicTitle:     topicTitle,
		Profile:        profile,
		ExpertTeamID:   expertTeamID,
		Platform:       platform,
		RemoteID:       remoteID,
		ChatType:       chatType,
		ChatID:         chatID,
		Mode:           mode,
	}, true
}

// SetSessionIMSource writes the IM origin (platform/remoteID/chatType/chatID)
// and mode="bot" into a session's .meta sidecar, so ListSessions can group bot
// sessions by IM contact. chatType+chatID separate the same user's conversations
// across different groups from their DM. Idempotent — re-writing the same values
// is harmless. The bot gateway calls this from OnTurnFinished once it has the
// session path.
func SetSessionIMSource(sessionPath, platform, remoteID, chatType, chatID string) error {
	if sessionPath == "" {
		return nil
	}
	meta, _, err := LoadBranchMeta(sessionPath)
	if err != nil {
		return err
	}
	meta.Platform = platform
	meta.RemoteID = remoteID
	meta.ChatType = chatType
	meta.ChatID = chatID
	if meta.Mode == "" {
		meta.Mode = "bot"
	}
	return SaveBranchMetaPreserveUpdated(sessionPath, meta)
}

// previewSession returns the first user message (truncated) and the number of
// user-role messages so the picker can show "5 turns · 'help me debug the…'".
// Errors are swallowed — a malformed file just shows up with an empty preview.
// previewSessionScanLimit bounds how much of a transcript previewSession will
// decode. It only runs for transcripts whose sidecar cache is missing (legacy
// or foreign files) — without a cap, one huge .jsonl re-decodes in full on
// every session-list build. Sessions whose first user turn sits beyond the
// limit are simply absent from previews/counts; hiq-written transcripts always
// carry the sidecar cache, so this only softens the legacy fallback.
const previewSessionScanLimit = 4 << 20 // 4 MiB

func previewSession(path string) (string, int) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, previewSessionScanLimit))
	first := ""
	turns := 0
	for {
		var m provider.Message
		if err := dec.Decode(&m); err != nil {
			break // EOF or a malformed tail — return the preview gathered so far
		}
		if m.Role == provider.RoleUser {
			turns++
			if first == "" {
				s := strings.TrimSpace(HandoffTask(provider.ContentString(m.Content)))
				if r := []rune(s); len(r) > 80 {
					s = string(r[:77]) + "…"
				}
				first = s
			}
		}
	}
	return first, turns
}

// ContinueSessionPath returns where a conversation carried into a rebuilt
// controller (model switch, config change) should keep auto-saving: its existing
// file when it has one, so the continued session stays a single file instead of
// the old one being orphaned as an identical duplicate (#2807). A session with no
// file yet gets a fresh path; "" when persistence is disabled.
func ContinueSessionPath(prevPath, dir, model string) string {
	if prevPath != "" {
		return prevPath
	}
	if dir == "" {
		return ""
	}
	return NewSessionPath(dir, model)
}

// NewSessionPath returns the path for a fresh session using the per-session
// folder layout (2026-08-21 redesign): <dir>/<yyyymmdd-hhmmss-xxxx>/<same>.jsonl
// — the transcript plus its .meta/.ckpt/.present siblings all live inside the
// folder, keeping every session self-contained and the parent dir navigable by
// date. The transcript FILE NAME equals the folder name so BranchID (filename
// stem) stays unique. model is no longer encoded in the name — the meta
// sidecar carries it. Falls back to the legacy flat layout only when the
// folder cannot be created.
func NewSessionPath(dir, model string) string {
	id := fmt.Sprintf("%s-%s", time.Now().Local().Format("20060102-150405"), shortSessionID())
	folder := filepath.Join(dir, id)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return filepath.Join(dir, id+".jsonl")
	}
	return filepath.Join(folder, id+".jsonl")
}

// shortSessionID returns 4 hex chars of entropy — enough to disambiguate
// sessions created within the same second.
func shortSessionID() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000"
	}
	return hex.EncodeToString(b[:])
}
