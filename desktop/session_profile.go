package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/zzycxz/hiq/internal/config"
)

// desktopSessionDirFor returns the session directory for a workspace root
// partitioned by profile. The default profile (empty / "dev") falls back to the
// un-profiled per-project session dir (backward compatible with
// desktopSessionDir). A named profile (e.g. "cowork") routes to
// <userDir>/projects/<slug>/<profileKey>/sessions so conversations stay in their
// own partition. When workspaceRoot is empty, the global profile session dir is
// used instead. Used by tabSessionDir so a tab's pre-build path already lands
// in the right profile partition.
func desktopSessionDirFor(workspaceRoot, profile string) string {
	root := strings.TrimSpace(workspaceRoot)
	key := config.ProfileNameKey(profile)
	if root != "" {
		// The profile home project (工作台 — the retired global scope's
		// replacement) reads/writes the former global partition so migrated
		// global sessions stay reachable with zero file moves.
		if homeKey, ok := profileKeyForHomeRoot(root); ok {
			if dir := config.SessionDirFor(homeKey); dir != "" {
				return dir
			}
		}
		if dir := config.ProjectSessionDirFor(root, key); dir != "" {
			return dir
		}
	}
	// GLOBAL sessions (empty root) have ONE fixed, profile-partitioned home:
	// <userDir>/sessions for dev, <userDir>/sessions/<key> for named profiles
	// (cowork/netdev). This deliberately bypasses desktopSessionDir — its empty
	// root fallback used to resolve the process CWD, scattering dev globals
	// into CWD-derived project dirs (2026-08-21 fix).
	if dir := config.SessionDirFor(key); dir != "" {
		return dir
	}
	return desktopSessionDir(root)
}

// homeProjectTitle is the display title of each profile's pinned landing
// project. The global scope was retired (2026-08-21): every session belongs
// to a project, and the profile home project is where "no project chosen"
// sessions (formerly global) live.
const homeProjectTitle = "工作台"

// profileHomeRoot returns the on-disk workspace root of a profile's home
// project (<configDir>/hiq/home-<profileKey>). It is a real directory so
// the home project behaves exactly like any user project.
func profileHomeRoot(profile string) string {
	key := config.ProfileNameKey(profile)
	if key == "" {
		key = config.ProfileDev
	}
	return filepath.Join(desktopConfigDir(), "home-" + key)
}

// ensureProfileHomeRoot mkdirs and returns the profile's home root.
func ensureProfileHomeRoot(profile string) string {
	root := profileHomeRoot(profile)
	_ = os.MkdirAll(root, 0o755)
	return root
}

// profileKeyForHomeRoot reports whether root is one of the three profile home
// roots, returning its profile key.
func profileKeyForHomeRoot(root string) (string, bool) {
	clean := filepath.Clean(strings.TrimSpace(root))
	if clean == "" || clean == "." {
		return "", false
	}
	for _, key := range []string{config.ProfileDev, config.ProfileCowork, config.ProfileNetDev} {
		if clean == filepath.Clean(profileHomeRoot(key)) {
			return key, true
		}
	}
	return "", false
}

// addProjectTitled registers a workspace in the profile's projects index,
// titling the profile home root 工作台 (plain workspaces keep their folder
// name — empty title means "derive from folder"). Refuses cross-profile
// registration (strict isolation — see rootOwnedByOtherProfile).
func addProjectTitled(root, profile string) {
	if rootOwnedByOtherProfile(root, profile) {
		return
	}
	title := ""
	if _, ok := profileKeyForHomeRoot(root); ok {
		title = homeProjectTitle
	}
	_ = addProject(root, title, profile)
}

var allProfileKeys = []string{config.ProfileDev, config.ProfileCowork, config.ProfileNetDev}

// projectRootOwners returns the profile keys whose projects index registers
// root with actual use (≥1 indexed topic, or the root is that profile's home).
// A root with no topics anywhere is unowned (fresh, free to register).
func projectRootOwners(root string) map[string]bool {
	root = normalizeProjectRoot(strings.TrimSpace(root))
	owners := map[string]bool{}
	if root == "" {
		return owners
	}
	for _, key := range allProfileKeys {
		for _, p := range loadProjectsFile(key).Projects {
			if normalizeProjectRoot(p.Root) != root {
				continue
			}
			if len(p.Topics) > 0 || normalizeProjectRoot(profileHomeRoot(key)) == root {
				owners[key] = true
			}
		}
	}
	return owners
}

// normalizedProfileKey maps "" / "default" / casing variants onto the
// canonical profile key ("" → dev, matching legacy un-profiled callers).
func normalizedProfileKey(profile string) string {
	key := config.ProfileNameKey(profile)
	if key == "" {
		key = config.ProfileDev
	}
	return key
}

// rootOwnedByOtherProfile reports whether a project root is claimed by a
// DIFFERENT profile. Isolation is generational, not defensive: every flow
// generates only its own profile's content (the mode switch never carries a
// root, the shared legacy workspace list is retired), so foreign roots can no
// longer be *produced*. This predicate is the invariant that asserts exactly
// that — the few checks that consult it fire only on pre-isolation leftovers
// (persisted tabs, polluted indexes) and normalize them to the profile home.
func rootOwnedByOtherProfile(root, profile string) bool {
	if key, isHome := profileKeyForHomeRoot(root); isHome {
		return key != normalizedProfileKey(profile)
	}
	owners := projectRootOwners(root)
	if len(owners) == 0 {
		return false
	}
	return !owners[normalizedProfileKey(profile)]
}

// projectClaimIsReal reports whether a profile's claim on a root reflects
// actual use: a topic with a real (non-default) title, or at least one file in
// the profile's session partition for that root. A bare entry (the blank
// topic the pre-guard mode-switch carry-over created) is not real use.
func projectClaimIsReal(root, profileKey string) bool {
	for _, title := range loadTopicTitles(root, profileKey) {
		if t := strings.TrimSpace(title); t != "" && t != defaultTopicTitle {
			return true
		}
	}
	if dir := config.ProjectSessionDirFor(root, profileKey); dir != "" {
		if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
			return true
		}
	}
	return false
}

// pruneForeignProjects repairs indexes polluted before the strict isolation
// guard existed (the mode switcher once carried the coding workspace root
// into the ops index, blank topic and all): when a root is claimed by several
// profiles, bare claims are dropped and only profiles with real use keep it.
// The profile home project is always kept. Startup-only, under the projects
// mutex, idempotent.
func pruneForeignProjects() {
	projectsMu.Lock()
	defer projectsMu.Unlock()
	for _, key := range allProfileKeys {
		home := normalizeProjectRoot(profileHomeRoot(key))
		f := loadProjectsFile(key)
		kept := f.Projects[:0]
		changed := false
		for _, p := range f.Projects {
			root := normalizeProjectRoot(p.Root)
			if root == home || !projectRootSharedWithRealUseElsewhere(root, key) {
				kept = append(kept, p)
				continue
			}
			changed = true
		}
		if changed {
			f.Projects = kept
			_ = saveProjectsFile(f, key)
		}
	}
}

// projectRootSharedWithRealUseElsewhere: this profile's claim is bare AND at
// least one other profile claims the same root with real use — so this entry
// is a foreign carry-over and should be pruned.
func projectRootSharedWithRealUseElsewhere(root, profileKey string) bool {
	if projectClaimIsReal(root, profileKey) {
		return false
	}
	for _, other := range allProfileKeys {
		if other == profileKey {
			continue
		}
		claimed := false
		for _, p := range loadProjectsFile(other).Projects {
			if normalizeProjectRoot(p.Root) == root {
				claimed = true
				break
			}
		}
		if claimed && projectClaimIsReal(root, other) {
			return true
		}
	}
	return false
}

// migrateGlobalIntoHome folds a profile's retired global topic section into
// its home project. Idempotent: once GlobalTopics is drained it only
// guarantees the home project entry exists. Titles/created sidecars are
// copied into the home root's .hiq; session files are NOT moved — the
// home project resolves to the former global session partition (see
// desktopSessionDirFor), so every migrated topic finds its transcripts.
func migrateGlobalIntoHome(profile string) {
	key := config.ProfileNameKey(profile)
	if key == "" {
		key = config.ProfileDev
	}
	home := ensureProfileHomeRoot(key)

	projectsMu.Lock()
	defer projectsMu.Unlock()
	f := loadProjectsFile(key)
	homeIdx := -1
	for i := range f.Projects {
		if normalizeProjectRoot(f.Projects[i].Root) == normalizeProjectRoot(home) {
			homeIdx = i
			break
		}
	}
	changed := false
	if homeIdx < 0 {
		f.Projects = append([]desktopProject{{Root: home, Title: homeProjectTitle}}, f.Projects...)
		homeIdx = 0
		changed = true
	}
	if len(f.GlobalTopics) > 0 {
		titles := loadTopicTitles("", key)
		sources := loadTopicTitleSources("", key)
		createds := loadTopicCreatedAts("", key)
		for _, id := range f.GlobalTopics {
			f.Projects[homeIdx].Topics = prependUniqueString(f.Projects[homeIdx].Topics, id)
			if t := strings.TrimSpace(titles[id]); t != "" {
				_ = setTopicTitleWithSource(home, id, t, sources[id])
			}
			if c := createds[id]; c > 0 {
				_ = setTopicCreatedAt(home, id, c)
			}
		}
		f.GlobalTopics = nil
		f.GlobalTitle = ""
		f.GlobalColor = ""
		changed = true
	}
	if changed {
		_ = saveProjectsFile(f, key)
	}
}

// activeProfileKey returns the profile key of the currently active tab, or the
// default ("dev") when no tab is active or the active tab has no profile. The
// returned value is normalized via config.ProfileNameKey so it is safe to use
// directly as a projects-file path segment.
func (a *App) activeProfileKey() string {
	a.mu.RLock()
	tab := a.activeTabLocked()
	a.mu.RUnlock()
	if tab == nil {
		return config.ProfileDev
	}
	key := config.ProfileNameKey(tab.profile)
	if key == "" {
		return config.ProfileDev
	}
	return key
}

// activeProfileKeyRaw returns the active tab's profile string verbatim (no
// normalization, no defaulting). Used where the caller needs to preserve an
// empty profile to indicate "inherit the default" rather than forcing dev —
// e.g. CreateTopic on a new workspace should match whatever profile the tab is
// actually in, including the un-profiled default.
func (a *App) activeProfileKeyRaw() string {
	a.mu.RLock()
	tab := a.activeTabLocked()
	a.mu.RUnlock()
	if tab == nil {
		return ""
	}
	return strings.TrimSpace(tab.profile)
}

// activeProfileKeyLocked is the lock-free variant of activeProfileKey for use
// when the caller already holds a.mu (Lock or RLock). Calling activeProfileKey()
// from within a held lock causes a deadlock (RLock blocks on held Lock).
func (a *App) activeProfileKeyLocked() string {
	tab := a.activeTabLocked()
	if tab == nil {
		return config.ProfileDev
	}
	key := config.ProfileNameKey(tab.profile)
	if key == "" {
		return config.ProfileDev
	}
	return key
}

// profileDisplayName maps a profile key to its Chinese product name for error
// messages shown to the user; unknown keys pass through as-is.
func profileDisplayName(profile string) string {
	switch config.ProfileNameKey(profile) {
	case config.ProfileCowork:
		return "办公"
	case config.ProfileNetDev:
		return "运维"
	default:
		return "编码"
	}
}
