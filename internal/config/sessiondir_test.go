package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSessionDirFollowsDataRoot pins the global session store's location rule:
// with no configured root it stays at the historical <userDir>/sessions (so an
// upgraded default install sees zero change), and once a data root is set the
// store moves under it instead of silently filling the system drive.
func TestSessionDirFollowsDataRoot(t *testing.T) {
	isolateUserConfig(t)

	wantDefault := filepath.Join(userDir(), "sessions")
	if got := SessionDir(); got != wantDefault {
		t.Fatalf("SessionDir() with no root = %q, want %q", got, wantDefault)
	}

	custom := t.TempDir()
	t.Setenv(MemoryRootEnv, custom)
	resetMemoryRootProbe()

	if got := SessionDir(); got != filepath.Join(custom, "sessions") {
		t.Fatalf("SessionDir() with root = %q, want %q", got, filepath.Join(custom, "sessions"))
	}
	// Profile partitions ride along: SessionDirFor builds on SessionDir.
	if got := SessionDirFor("cowork"); got != filepath.Join(custom, "sessions", "cowork") {
		t.Fatalf("SessionDirFor(cowork) with root = %q, want %q", got, filepath.Join(custom, "sessions", "cowork"))
	}
}

// TestMigrateMemoryTreeCarriesGlobalSessions guards the relocation contract for
// the global session store: without it, pointing the data root at another drive
// would strand every 工作台 conversation on the old one.
func TestMigrateMemoryTreeCarriesGlobalSessions(t *testing.T) {
	from := t.TempDir()
	to := t.TempDir()

	writeFile(t, filepath.Join(from, "sessions", "s1.jsonl"), `{"a":1}`)
	writeFile(t, filepath.Join(from, "sessions", "cowork", "s2.jsonl"), `{"a":2}`)

	rep, err := MigrateMemoryTree(from, to)
	if err != nil {
		t.Fatalf("MigrateMemoryTree: %v", err)
	}
	for _, rel := range []string{
		filepath.Join("sessions", "s1.jsonl"),
		filepath.Join("sessions", "cowork", "s2.jsonl"),
	} {
		if _, err := os.Stat(filepath.Join(to, rel)); err != nil {
			t.Errorf("expected %s at the destination: %v", rel, err)
		}
		// Copy, never move: the source stays until the user deletes it.
		if _, err := os.Stat(filepath.Join(from, rel)); err != nil {
			t.Errorf("source %s must survive the migration: %v", rel, err)
		}
	}
	if rep.Files < 2 {
		t.Errorf("report must account for the copied session files: %+v", rep)
	}
}
