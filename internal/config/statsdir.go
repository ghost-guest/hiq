package config

import "path/filepath"

// StatsDir is where usage statistics are persisted (one .jsonl per day, e.g.
// stats/2026-08-02.jsonl). Kept beside sessions/archive under the hiq user
// config directory so usage records survive app updates. Empty if the user
// config dir can't be resolved, in which case usage accounting is skipped.
//
// Adapted from DeepSeek-Reasonix: upstream anchors this under its state root
// (REASONIX_STATE_HOME / reasonixHomeDir); hiq has no separate state-home
// concept, so it reuses the same userDir() root as SessionDir/ArchiveDir.
func StatsDir() string {
	dir := userDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "stats")
}
