package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyUserDirCarriesKeysAndSettings(t *testing.T) {
	isolateUserConfig(t)
	old := legacyUserDir()
	if old == "" {
		t.Fatal("legacyUserDir() must resolve under an isolated HOME/AppData")
	}
	writeFile(t, filepath.Join(old, "config.toml"), "default_model = \"m\"\n")
	writeFile(t, filepath.Join(old, "secrets.enc.json"), `{"k":"v"}`)
	writeFile(t, filepath.Join(old, "memory", "2026-09-18.md"), "- note\n")

	moved, err := MigrateLegacyUserDir()
	if err != nil {
		t.Fatalf("MigrateLegacyUserDir: %v", err)
	}
	if !moved {
		t.Fatal("MigrateLegacyUserDir reported no copy")
	}
	dst := userDir()
	for _, rel := range []string{"config.toml", "secrets.enc.json", filepath.Join("memory", "2026-09-18.md")} {
		got, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Fatalf("read migrated %s: %v", rel, err)
		}
		want, _ := os.ReadFile(filepath.Join(old, rel))
		if string(got) != string(want) {
			t.Fatalf("%s not carried over intact: %q != %q", rel, got, want)
		}
	}
	// Non-destructive: the pre-rebrand dir must survive so a rollback still works.
	if _, err := os.Stat(filepath.Join(old, "config.toml")); err != nil {
		t.Fatalf("legacy dir must be left untouched: %v", err)
	}
}

func TestMigrateLegacyUserDirIsOneShot(t *testing.T) {
	isolateUserConfig(t)
	old := legacyUserDir()
	writeFile(t, filepath.Join(old, "config.toml"), "legacy = true\n")
	// hiq dir already exists (e.g. a previous run) => never resurrect the old tree.
	writeFile(t, filepath.Join(userDir(), "config.toml"), "current = true\n")

	moved, err := MigrateLegacyUserDir()
	if err != nil {
		t.Fatalf("MigrateLegacyUserDir: %v", err)
	}
	if moved {
		t.Fatal("migration must not run once the hiq dir exists")
	}
	body, err := os.ReadFile(filepath.Join(userDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "current = true\n" {
		t.Fatalf("existing hiq config was clobbered: %q", body)
	}
}

func TestMigrateLegacyUserDirNoOpWithoutLegacyDir(t *testing.T) {
	isolateUserConfig(t)
	moved, err := MigrateLegacyUserDir()
	if err != nil {
		t.Fatalf("MigrateLegacyUserDir: %v", err)
	}
	if moved {
		t.Fatal("nothing to migrate, yet it reported a copy")
	}
	if _, err := os.Stat(userDir()); !os.IsNotExist(err) {
		t.Fatalf("migration must not create the hiq dir when there is nothing to copy (err=%v)", err)
	}
}

// MigrateLegacyIfNeeded must perform the dir hop before it looks for a legacy
// config, so a pre-rebrand install is already the current config by the time the
// v1/v0.x import decides whether there is anything left to do.
func TestMigrateLegacyIfNeededPerformsDirHop(t *testing.T) {
	isolateUserConfig(t)
	writeFile(t, filepath.Join(legacyUserDir(), "config.toml"), "default_model = \"m\"\n")

	res, err := MigrateLegacyIfNeeded()
	if err != nil {
		t.Fatalf("MigrateLegacyIfNeeded: %v", err)
	}
	if res != nil {
		t.Fatalf("the dir hop already delivered the config; the import should be a no-op, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(userDir(), "config.toml")); err != nil {
		t.Fatalf("config.toml must exist in the hiq dir after the hop: %v", err)
	}
}

// The GUI shell opens app.log in the config dir during startup, before boot runs
// the migration, so the hiq dir routinely exists on a genuine first run. The
// decision must therefore key on config.toml, not on the directory — keying on
// the directory silently stranded the key store on the first real run.
func TestMigrateLegacyUserDirRunsWhenOnlyTheShellHasWritten(t *testing.T) {
	isolateUserConfig(t)
	old := legacyUserDir()
	writeFile(t, filepath.Join(old, "config.toml"), "default_model = \"m\"\n")
	writeFile(t, filepath.Join(old, "secrets.enc.json"), `{"k":"v"}`)
	// Simulate the shell's own early write into the hiq dir.
	writeFile(t, filepath.Join(userDir(), "app.log"), "shell started\n")

	moved, err := MigrateLegacyUserDir()
	if err != nil {
		t.Fatalf("MigrateLegacyUserDir: %v", err)
	}
	if !moved {
		t.Fatal("the shell's own app.log must not be mistaken for a completed migration")
	}
	for _, f := range []string{"config.toml", "secrets.enc.json"} {
		if _, err := os.Stat(filepath.Join(userDir(), f)); err != nil {
			t.Fatalf("%s must be carried over: %v", f, err)
		}
	}
	// Destination-wins: the shell's log survives the copy untouched.
	body, err := os.ReadFile(filepath.Join(userDir(), "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "shell started\n" {
		t.Fatalf("existing app.log was clobbered: %q", body)
	}
}

// The marker keeps the hop one-shot even when the old tree carries no
// config.toml, where the config.toml check alone would copy on every boot.
func TestMigrateLegacyUserDirMarkerStopsRepeatCopies(t *testing.T) {
	isolateUserConfig(t)
	writeFile(t, filepath.Join(legacyUserDir(), "sessions", "s.json"), "{}\n")

	moved, err := MigrateLegacyUserDir()
	if err != nil || !moved {
		t.Fatalf("first run should copy (moved=%v err=%v)", moved, err)
	}
	if _, err := os.Stat(filepath.Join(userDir(), migratedMarkName)); err != nil {
		t.Fatalf("marker must be written: %v", err)
	}
	moved, err = MigrateLegacyUserDir()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if moved {
		t.Fatal("marker must make the hop one-shot")
	}
}

func TestLegacyPeerDirnameDistinctFromCurrent(t *testing.T) {
	if legacyPeerDirname == userDirname {
		t.Fatal("the legacy dir name must differ from the current one, or the hop is meaningless")
	}
}
