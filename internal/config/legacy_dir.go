package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// legacyPeerDirname is the user-dir name this project used before the hiq
// rebrand. It is referenced from exactly one place — the one-time user-dir
// migration below — and nowhere else; every live path uses hiq. The spelling
// deliberately stays as-is, because that is the directory the pre-rebrand build
// actually created on disk: renaming the lookup would turn the copy below into a
// no-op and strand the user's API keys, which are DPAPI-sealed (machine-bound)
// and therefore expensive to re-enter by hand.
const legacyPeerDirname = "fairpeer"

// legacyUserDir returns the pre-rebrand user config dir
// (%AppData%/fairpeer on Windows, ~/.config/fairpeer on Unix), or "" when the
// platform config dir can't be resolved.
func legacyUserDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, legacyPeerDirname)
}

// migrateLegacyUserDir copies a pre-rebrand user directory into the hiq one,
// once, the first time hiq boots. This is what carries config.toml, the
// encrypted key store, provider entries, the memory root and every other setting
// across the fairpeer -> hiq rename.
//
// It is deliberately conservative:
//
//   - non-destructive: the old directory is left untouched, so rolling back to
//     the pre-rebrand build still finds its data;
//   - one-shot: it no-ops once hiq is actually configured (config.toml present)
//     or a previous migration left its marker, so a later deliberate reset is
//     never silently undone by resurrecting the old tree;
//   - destination-wins: copyTree never overwrites a file that already exists,
//     so the handful of files the shell writes before boot survive.
//
// Returns true only when a copy actually happened.
func MigrateLegacyUserDir() (bool, error) {
	old := legacyUserDir()
	if old == "" {
		return false, nil
	}
	dst := userDir()
	if dst == "" || strings.EqualFold(filepath.Clean(old), filepath.Clean(dst)) {
		return false, nil
	}
	// The hiq dir existing proves nothing. The GUI shell opens its app.log in
	// the config dir during startup, *before* boot's migration runs, so by the
	// time we get here a fresh hiq dir is already populated with that log (and
	// whichever lite state files the shell touched) — a directory-existence
	// check would wrongly report "already migrated" and strand the user's keys.
	//
	// config.toml is the real signal: it is hiq's own settings file, written
	// only once hiq has actually taken over. A marker is written on success as
	// well, so a run that copies a keyless old tree does not re-copy on every
	// later boot.
	if markerExists(dst) {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(dst, "config.toml")); err == nil {
		return false, nil // hiq is already configured => nothing left to hand over
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if _, err := os.Stat(old); err != nil {
		return false, nil // no pre-rebrand dir to copy
	}
	if _, _, err := copyTree(old, dst); err != nil {
		return false, fmt.Errorf("migrate legacy user dir %s -> %s: %w", old, dst, err)
	}
	// Best effort: a failed marker write must not fail the boot, it only costs
	// one redundant (idempotent) copy next launch.
	_ = os.WriteFile(filepath.Join(dst, migratedMarkName), []byte(old+"\n"), 0o644)
	return true, nil
}

// migratedMarkName records that the pre-rebrand tree was already handed over.
// Deliberately inside the hiq dir: wiping that dir is the user asking for a
// clean slate, and the marker goes with it.
const migratedMarkName = ".migrated-from-fairpeer"

func markerExists(dst string) bool {
	_, err := os.Stat(filepath.Join(dst, migratedMarkName))
	return err == nil
}
