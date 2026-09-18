package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/zzycxz/hiq/internal/agent"
	"github.com/zzycxz/hiq/internal/config"
)

// profilePartitionMarker is written into <userDir>/sessions/dev once the
// Phase I relocation has run, making migrateToProfilePartition a no-op on
// every subsequent launch.
const profilePartitionMarker = ".profile-partitioned"

const (
	topicTitlesBaseName       = "topic_titles.json"
	topicTitleSourcesBasename = "topic_title_sources.json"
	topicCreatedAtsBaseName   = "topic_created_at.json"
	desktopProjectsFileLegacy = "projects.json"
)

func projectsFilePath(profile string) string {
	return filepath.Join(desktopConfigDir(), profile, "projects.json")
}

// migrateToProfilePartition is a one-time, idempotent migration that relocates
// pre-partition (un-profiled) session/topic data into the per-profile
// partitions (dev/cowork). It runs at desktop startup before tabs are restored.
//
// It is safe to call every launch: the .profile-partitioned marker in the dev
// sessions dir makes it a no-op once the merge-aware pass has run. All
// operations are best-effort and log failures without aborting — a partial
// state (e.g. some sessions moved, some sidecars left) is still a correct one
// because every step is independently idempotent and merge-aware: re-running
// on a half-partitioned disk (where another code path already created some
// -dev files / sessions/dev/ dirs) MERGES rather than skips, so no real data
// is ever orphaned.
//
// Companion files for a session <id>.jsonl are: <id>.meta, <id>.ckpt,
// <id>.telemetry.json. Sessions are routed to sessions/<profile>/ by reading
// their .meta Profile field (empty/unknown defaults to dev, which is correct
// for legacy). Migration NEVER deletes user data: moving = os.Rename, merging
// a sidecar JSON = read-modify-write the destination.
func migrateToProfilePartition() {
	userDir := config.MemoryUserDir()
	if strings.TrimSpace(userDir) == "" {
		return
	}
	devSessionsDir := config.SessionDirFor(config.ProfileDev)
	if strings.TrimSpace(devSessionsDir) == "" {
		return
	}

	// 1. Marker check — already partitioned, nothing to do.
	if _, err := os.Stat(filepath.Join(devSessionsDir, profilePartitionMarker)); err == nil {
		return
	}

	if err := os.MkdirAll(devSessionsDir, 0o755); err != nil {
		slog.Warn("profile migration: create dev sessions dir", "dir", devSessionsDir, "err", err)
		// Without the dest dir we can't relocate anything; bail without the
		// marker so the next launch retries.
		return
	}

	// 2. Move global sessions: every top-level entry under <userDir>/sessions/
	// that is not itself a profile subdir (dev/cowork) goes into
	// sessions/<profile>/, routed per-session by its .meta Profile field.
	migrateGlobalSessionsByProfile(userDir)

	// 3. Move project sessions: <userDir>/projects/<slug>/sessions/<file> ->
	// <userDir>/projects/<slug>/<profile>/sessions/<file>, routed per-session.
	migrateProjectSessionsByProfile(userDir)

	// 4. Merge topic sidecars: un-suffixed desktop-topic-titles.json
	// (-title-sources, -created-at) are MERGED into their -dev variants, in the
	// global sidecar dir and in each known workspace's .hiq/ dir. Merge
	// (not skip) so a pre-existing -dev.json never orphans the legacy keys.
	migrateTopicSidecarsByMerge()

	// 5. Merge the projects index: legacy desktop-projects.json is MERGED into
	// desktop-projects-dev.json (union GlobalTopics, union Projects). The
	// existing -cowork.json is left untouched. Merge (not skip) so ~80 legacy
	// topics are never lost when a 1-entry -dev.json already exists.
	migrateProjectsIndexByMerge()

	// 6. Backfill BranchMeta.Profile: every .meta sidecar under the dev AND
	// cowork session dirs whose Profile is empty gets stamped to "dev". An
	// existing non-empty Profile (e.g. "cowork") is preserved.
	backfillBranchMetaProfile(userDir, devSessionsDir)

	// 7. Rewrite desktop-tabs.json SessionPaths: persisted absolute paths now
	// point at OLD locations. Best-effort relocate each tab's SessionPath to
	// the new profile-partitioned location by basename match.
	rewriteTabSessionPaths(userDir)

	// 8. Stamp the marker so this never runs the expensive steps again.
	if err := os.WriteFile(filepath.Join(devSessionsDir, profilePartitionMarker), []byte{}, 0o644); err != nil {
		slog.Warn("profile migration: write marker", "err", err)
		return
	}
	slog.Info("profile migration: relocated legacy data into per-profile partitions")
}

// profileForSession determines the profile partition a session belongs in by
// reading its sibling .meta. Profile=="cowork"/"netdev" routes to that
// partition; anything else (empty/unknown/legacy) routes to dev. sessionPath
// is the path to the .jsonl. A missing or unreadable .meta defaults to dev
// (correct for legacy data).
func profileForSession(sessionPath string) string {
	m, ok, err := agent.LoadBranchMeta(sessionPath)
	if err != nil || !ok {
		return config.ProfileDev
	}
	switch strings.ToLower(strings.TrimSpace(m.Profile)) {
	case config.ProfileCowork:
		return config.ProfileCowork
	case config.ProfileNetDev:
		return config.ProfileNetDev
	default:
		return config.ProfileDev
	}
}

// sessionCompanions returns the paths of every file/dir that travels with a
// session .jsonl: the .meta sidecar, the .ckpt file/dir, and the telemetry
// sidecar. sessionPath is the path to the .jsonl. The .jsonl itself is moved
// by the caller; this returns only the companions.
func sessionCompanions(sessionPath string) []string {
	stem := strings.TrimSuffix(sessionPath, ".jsonl")
	return []string{
		sessionPath + ".meta",
		stem + ".ckpt",
		sessionPath + ".telemetry.json",
	}
}

// moveSessionWithCompanions moves a session .jsonl plus its sibling .meta,
// .ckpt, and .telemetry.json from srcDir into dstDir, preserving basenames. A
// destination that already exists is left in place (idempotent); the source is
// then removed only if it still exists and the rename would clobber — handled
// by movePathIfExistsExistingOK below. Per-file failures are logged, not fatal.
func moveSessionWithCompanions(sessionName, srcDir, dstDir string) {
	srcSession := filepath.Join(srcDir, sessionName)
	dstSession := filepath.Join(dstDir, sessionName)
	movePathIfExistsExistingOK(srcSession, dstSession)
	for _, c := range sessionCompanions(srcSession) {
		movePathIfExistsExistingOK(c, filepath.Join(dstDir, filepath.Base(c)))
	}
}

// movePathIfExistsExistingOK moves src to dst if src exists. If dst already
// exists, the move is skipped (idempotent re-run / merge into a pre-existing
// partitioned dir) rather than clobbering. A missing src is a quiet no-op;
// other errors are logged.
func movePathIfExistsExistingOK(src, dst string) {
	if _, err := os.Stat(src); err != nil {
		return // nothing at src — quiet no-op.
	}
	if _, err := os.Stat(dst); err == nil {
		return // destination already present (merge/idempotent) — never overwrite.
	} else if !os.IsNotExist(err) {
		slog.Warn("profile migration: stat dest", "dst", dst, "err", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		slog.Warn("profile migration: mkdir dest dir", "dir", filepath.Dir(dst), "err", err)
		return
	}
	if err := os.Rename(src, dst); err != nil {
		slog.Warn("profile migration: move", "from", src, "to", dst, "err", err)
	}
}

// migrateGlobalSessionsByProfile moves each top-level entry under
// <userDir>/sessions (except the dev/cowork profile subdirs) into the profile
// partition indicated by its .meta. This covers .jsonl session files (routed
// per-session into dev/ or cowork/) and every non-session sibling (.meta,
// .trash, subagents/, .legacy-imported markers, .titles.json, etc.) which goes
// to dev (the legacy default). When a profile sessions dir already exists
// (half-partitioned disk), flat session FILES are merged into it individually
// rather than skipping the whole pass.
func migrateGlobalSessionsByProfile(userDir string) {
	sessionsDir := filepath.Join(userDir, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("profile migration: read sessions dir", "dir", sessionsDir, "err", err)
		}
		return
	}
	devDir := config.SessionDirFor(config.ProfileDev)
	coworkDir := config.SessionDirFor(config.ProfileCowork)
	netdevDir := config.SessionDirFor(config.ProfileNetDev)
	// Two passes so a session's .meta companion is consumed together with its
	// .jsonl (and routed by that .meta's Profile) rather than being orphaned to
	// dev if dir iteration happened to visit the .meta first. Pass 1: .jsonl
	// files (which pull their .meta/.ckpt/.telemetry companions along by
	// basename). Pass 2: any remaining top-level entries.
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		srcSession := filepath.Join(sessionsDir, name)
		profile := profileForSession(srcSession)
		dstDir := devDir
		switch profile {
		case config.ProfileCowork:
			dstDir = coworkDir
		case config.ProfileNetDev:
			dstDir = netdevDir
		}
		moveSessionWithCompanions(name, sessionsDir, dstDir)
	}
	for _, e := range entries {
		name := e.Name()
		// Leave the profile subdirs the partitioned layout created in place.
		if e.IsDir() && (name == config.ProfileDev || name == config.ProfileCowork || name == config.ProfileNetDev) {
			continue
		}
		// .jsonl files were handled in pass 1; skip them here.
		if !e.IsDir() && strings.HasSuffix(name, ".jsonl") {
			continue
		}
		// Every remaining top-level entry (orphan .meta/.ckpt/.telemetry
		// companions, .trash, subagents/, markers, etc.) belongs to the legacy
		// dev partition.
		movePathIfExistsExistingOK(filepath.Join(sessionsDir, name), filepath.Join(devDir, name))
	}
}

// migrateProjectSessionsByProfile relocates each project's un-profiled
// sessions into the partitioned per-profile location. For every
// <userDir>/projects/<slug>/ with a flat sessions/ dir, each session FILE
// inside is routed by its .meta into <slug>/<profile>/sessions/ (default dev).
// When <slug>/dev/sessions/ (or cowork) already exists (half-partitioned
// disk), files are merged in individually rather than skipping the project.
func migrateProjectSessionsByProfile(userDir string) {
	projectsDir := filepath.Join(userDir, "projects")
	slugs, err := os.ReadDir(projectsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("profile migration: read projects dir", "dir", projectsDir, "err", err)
		}
		return
	}
	for _, slug := range slugs {
		if !slug.IsDir() {
			continue
		}
		slugDir := filepath.Join(projectsDir, slug.Name())
		flatSessions := filepath.Join(slugDir, "sessions")
		entries, err := os.ReadDir(flatSessions)
		if err != nil {
			continue // no legacy sessions dir here (or unreadable) — nothing to do.
		}
		// Two passes: .jsonl first (pulling companions by basename so a
		// session's .meta is routed with it), then any remaining entries.
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				continue
			}
			if !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			srcSession := filepath.Join(flatSessions, name)
			profile := profileForSession(srcSession)
			dstDir := filepath.Join(slugDir, profile, "sessions")
			moveSessionWithCompanions(name, flatSessions, dstDir)
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() && strings.HasSuffix(name, ".jsonl") {
				continue // handled in pass 1.
			}
			// Non-.jsonl siblings (orphan .meta/.ckpt/.telemetry, subagents/,
			// etc.) -> dev (legacy default).
			movePathIfExistsExistingOK(
				filepath.Join(flatSessions, name),
				filepath.Join(slugDir, config.ProfileDev, "sessions", name),
			)
		}
	}
}

// migrateTopicSidecarsByMerge merges the legacy un-suffixed topic sidecars
// (desktop-topic-titles, -title-sources, -created-at) into their -dev variants.
// Global sidecars live under <configDir>/global/; project sidecars live under
// <workspaceRoot>/.hiq/. When the -dev variant already exists, the legacy
// keys are MERGED into it (legacy keys win on conflict, since they are the
// source of truth for unmigrated data) rather than skipping — this is the fix
// for BUG M1 where ~87 titles were orphaned by a 1-key -dev.json.
func migrateTopicSidecarsByMerge() {
	sidecarBases := []string{
		topicTitlesBaseName,
		topicTitleSourcesBasename,
		topicCreatedAtsBaseName,
	}

	// Global sidecars live under <configDir>/global/.
	mergeTopicSidecarsInDir(filepath.Join(desktopConfigDir(), "global"), sidecarBases)

	// Project sidecars live under <workspaceRoot>/.hiq/. Gather the known
	// workspace roots from every index so we cover both code paths.
	for _, root := range knownWorkspaceRoots() {
		mergeTopicSidecarsInDir(filepath.Join(root, ".hiq"), sidecarBases)
	}
}

// mergeTopicSidecarsInDir merges each un-suffixed <base>.json into <base>-dev.json
// inside dir. The titles and title-sources sidecars are string->string maps;
// created-at is string->int64. Missing legacy file or missing dir is a quiet
// no-op. After a successful merge the legacy file is removed so the next run
// is a no-op; a failed merge leaves both files untouched.
func mergeTopicSidecarsInDir(dir string, bases []string) {
	for _, base := range bases {
		legacy := filepath.Join(dir, base+".json")
		dev := filepath.Join(dir, base+"-"+config.ProfileDev+".json")
		if _, err := os.Stat(legacy); err != nil {
			continue // nothing to merge.
		}
		switch base {
		case topicCreatedAtsBaseName:
			if merged, ok := mergeInt64Sidecar(legacy, dev); ok {
				writeInt64Sidecar(dev, merged)
				removeMergedLegacy(legacy)
			}
		default: // titles + title-sources are string->string maps.
			if merged, ok := mergeStringSidecar(legacy, dev); ok {
				writeStringSidecar(dev, merged)
				removeMergedLegacy(legacy)
			}
		}
	}
}

// mergeStringSidecar reads legacy (string->string) and dev (string->string,
// may be absent) and returns the union, with legacy keys winning on conflict.
// ok=false means the merge could not be performed (read/parse failure).
func mergeStringSidecar(legacy, dev string) (map[string]string, bool) {
	legacyMap := map[string]string{}
	if b, err := os.ReadFile(legacy); err == nil {
		if err := json.Unmarshal(b, &legacyMap); err != nil {
			slog.Warn("profile migration: parse legacy string sidecar", "path", legacy, "err", err)
			return nil, false
		}
	}
	devMap := map[string]string{}
	if b, err := os.ReadFile(dev); err == nil {
		if err := json.Unmarshal(b, &devMap); err != nil {
			slog.Warn("profile migration: parse dev string sidecar", "path", dev, "err", err)
			return nil, false
		}
	}
	// Legacy wins on conflict (it is the source of truth for unmigrated data).
	for k, v := range legacyMap {
		devMap[k] = v
	}
	return devMap, true
}

// mergeInt64Sidecar reads legacy (string->int64) and dev (string->int64, may
// be absent) and returns the union, legacy winning on conflict. ok=false means
// the merge could not be performed.
func mergeInt64Sidecar(legacy, dev string) (map[string]int64, bool) {
	legacyMap := map[string]int64{}
	if b, err := os.ReadFile(legacy); err == nil {
		if err := json.Unmarshal(b, &legacyMap); err != nil {
			slog.Warn("profile migration: parse legacy int64 sidecar", "path", legacy, "err", err)
			return nil, false
		}
	}
	devMap := map[string]int64{}
	if b, err := os.ReadFile(dev); err == nil {
		if err := json.Unmarshal(b, &devMap); err != nil {
			slog.Warn("profile migration: parse dev int64 sidecar", "path", dev, "err", err)
			return nil, false
		}
	}
	for k, v := range legacyMap {
		devMap[k] = v
	}
	return devMap, true
}

func writeStringSidecar(path string, m map[string]string) {
	writeJSONSidecar(path, m)
}

func writeInt64Sidecar(path string, m map[string]int64) {
	writeJSONSidecar(path, m)
}

// writeJSONSidecar marshals v and atomically writes it to path, creating the
// parent dir. Failures are logged (best-effort).
func writeJSONSidecar(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		slog.Warn("profile migration: marshal sidecar", "path", path, "err", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("profile migration: mkdir sidecar dir", "dir", filepath.Dir(path), "err", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		slog.Warn("profile migration: write sidecar tmp", "path", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		slog.Warn("profile migration: rename sidecar", "from", tmp, "to", path, "err", err)
	}
}

// removeMergedLegacy deletes the legacy sidecar once it has been merged into
// the -dev variant, so a re-run is a no-op. A failure is non-fatal (the merge
// already succeeded; the next run just re-merges harmlessly).
func removeMergedLegacy(legacy string) {
	if err := os.Remove(legacy); err != nil && !os.IsNotExist(err) {
		slog.Warn("profile migration: remove merged legacy sidecar", "path", legacy, "err", err)
	}
}

// migrateProjectsIndexByMerge MERGES the legacy desktop-projects.json index
// into desktop-projects-dev.json: union GlobalTopics, union Projects (topics
// merged per project root), preserving SidebarOrder/Title/Color from whichever
// side has them. The existing -cowork.json (if any) is left untouched — it
// already holds cowork-profile topics from the other code path, and we cannot
// cheaply tell per-topic profile without scanning every session. The key
// invariant: the ~80 topics that live ONLY in legacy desktop-projects.json are
// never lost when a 1-entry -dev.json already exists. If -cowork.json does not
// yet exist it is seeded with workspace metadata only (topics cleared).
func migrateProjectsIndexByMerge() {
	cfgDir := desktopConfigDir()
	legacy := filepath.Join(cfgDir, desktopProjectsFileLegacy)
	coworkFile := projectsFilePath(config.ProfileCowork)

	// Load the legacy index (absent => empty, merge is then a no-op).
	var legacyFile desktopProjectFile
	if b, err := os.ReadFile(legacy); err == nil {
		if json.Unmarshal(b, &legacyFile) == nil {
			legacyFile = normalizeProjectsFile(legacyFile)
		}
	}

	// Load the existing dev index (may be absent on a pristine-legacy disk).
	devFileLoaded := loadProjectsFile(config.ProfileDev)

	// Merge: union GlobalTopics + SidebarOrder + workspace metadata (root/title/
	// color). BUT clear per-project Topics — pre-partition topic lists mixed dev
	// and cowork IDs together, and the sessions have already been routed to the
	// right profile dir by the session-move step above. Leaving the mixed Topics
	// here would let cowork topics leak into the dev sidebar. Instead, projects
	// enter the dev index topic-less; buildTabController's ensureTopicIndexed
	// re-populates each project's topics from the sessions that actually live in
	// the dev partition. ListProjectTree/ListWorkspaces skip topic-less projects
	// with no open tab, so a cowork-only workspace won't surface in dev.
	mergedProjects := make([]desktopProject, 0, len(legacyFile.Projects)+len(devFileLoaded.Projects))
	seenRoots := map[string]bool{}
	for _, p := range append(append([]desktopProject{}, legacyFile.Projects...), devFileLoaded.Projects...) {
		root := normalizeProjectRoot(p.Root)
		if root == "" || seenRoots[root] {
			continue
		}
		seenRoots[root] = true
		mergedProjects = append(mergedProjects, desktopProject{
			Root:   p.Root,
			Title:  firstNonEmpty(p.Title, ""),
			Color:  p.Color,
			Topics: []string{}, // re-indexed by ensureTopicIndexed per profile
		})
	}
	merged := desktopProjectFile{
		GlobalTitle:  firstNonEmpty(legacyFile.GlobalTitle, devFileLoaded.GlobalTitle),
		GlobalColor:  firstNonEmpty(legacyFile.GlobalColor, devFileLoaded.GlobalColor),
		GlobalTopics: uniqueStrings(append(append([]string{}, legacyFile.GlobalTopics...), devFileLoaded.GlobalTopics...)),
		SidebarOrder: uniqueStrings(append(append([]string{}, legacyFile.SidebarOrder...), devFileLoaded.SidebarOrder...)),
		Projects:     mergedProjects,
	}
	if err := saveProjectsFile(merged, config.ProfileDev); err != nil {
		slog.Warn("profile migration: merge projects index into dev", "err", err)
	}

	// Remove the legacy file now that it has been folded into -dev.json so a
	// re-run is a no-op. Best-effort.
	if _, err := os.Stat(legacy); err == nil {
		if err := os.Remove(legacy); err != nil && !os.IsNotExist(err) {
			slog.Warn("profile migration: remove legacy projects index", "path", legacy, "err", err)
		}
	}

	// Seed the cowork index with workspace metadata only (topics cleared, empty
	// global topics). Like the dev merge above, topics are re-indexed per profile
	// by ensureTopicIndexed from the sessions that actually live in each profile's
	// dir, so cowork shows only cowork conversations. Never overwrite an existing
	// -cowork.json.
	if _, err := os.Stat(coworkFile); err == nil {
		return // cowork index already exists — leave it.
	}
	coworkExisting := loadProjectsFile(config.ProfileCowork)
	coworkSeed := desktopProjectFile{
		GlobalTitle:  firstNonEmpty(legacyFile.GlobalTitle, coworkExisting.GlobalTitle),
		GlobalColor:  firstNonEmpty(legacyFile.GlobalColor, coworkExisting.GlobalColor),
		GlobalTopics: nil,
		Projects:     cloneProjectsWithoutTopics(coworkExisting.Projects),
	}
	if err := saveProjectsFile(coworkSeed, config.ProfileCowork); err != nil {
		slog.Warn("profile migration: seed cowork projects index", "err", err)
	}
}

// cloneProjectsWithoutTopics returns a copy of projects carrying only the
// workspace metadata (Root/Title/Color); topic membership starts empty for the
// cowork profile so dev and cowork keep independent topic lists.
func cloneProjectsWithoutTopics(projects []desktopProject) []desktopProject {
	out := make([]desktopProject, 0, len(projects))
	for _, p := range projects {
		out = append(out, desktopProject{Root: p.Root, Title: p.Title, Color: p.Color})
	}
	return out
}

// knownWorkspaceRoots returns the set of workspace roots recorded in the
// projects indexes (legacy, dev, and cowork), deduped. All three are consulted
// so roots are captured regardless of which code path recorded them.
func knownWorkspaceRoots() []string {
	seen := map[string]bool{}
	var roots []string
	add := func(root string) {
		root = normalizeProjectRoot(root)
		if root == "" || seen[root] {
			return
		}
		seen[root] = true
		roots = append(roots, root)
	}
	// Legacy un-profiled index (read from disk directly).
	if b, err := os.ReadFile(filepath.Join(desktopConfigDir(), desktopProjectsFileLegacy)); err == nil {
		var f desktopProjectFile
		if json.Unmarshal(b, &f) == nil {
			for _, p := range f.Projects {
				add(p.Root)
			}
		}
	}
	// Partitioned indexes.
	for _, profileKey := range []string{config.ProfileDev, config.ProfileCowork, config.ProfileNetDev} {
		f := loadProjectsFile(profileKey)
		for _, p := range f.Projects {
			add(p.Root)
		}
	}
	return roots
}

// backfillBranchMetaProfile stamps the owning profile key on every .meta
// sidecar under the global dev/cowork/netdev session dirs and each project's
// per-profile session dirs whose Profile field is EMPTY. An existing non-empty
// Profile (e.g. "cowork") is PRESERVED — this is the fix for BUG M2 where all
// sessions were blanket-stamped to dev. Best-effort: a failed load/save for
// one sidecar is logged and skipped.
func backfillBranchMetaProfile(userDir, devSessionsDir string) {
	dirs := []struct {
		dir     string
		profile string
	}{
		{devSessionsDir, config.ProfileDev},
		{config.SessionDirFor(config.ProfileCowork), config.ProfileCowork},
		{config.SessionDirFor(config.ProfileNetDev), config.ProfileNetDev},
	}
	// Each project's per-profile session dir:
	// <userDir>/projects/<slug>/<profile>/sessions.
	projectsDir := filepath.Join(userDir, "projects")
	if slugs, err := os.ReadDir(projectsDir); err == nil {
		for _, slug := range slugs {
			if !slug.IsDir() {
				continue
			}
			for _, profileKey := range []string{config.ProfileDev, config.ProfileCowork, config.ProfileNetDev} {
				dirs = append(dirs, struct {
					dir     string
					profile string
				}{filepath.Join(projectsDir, slug.Name(), profileKey, "sessions"), profileKey})
			}
		}
	}
	for _, d := range dirs {
		backfillBranchMetaProfileInDir(d.dir, d.profile)
	}
}

// backfillBranchMetaProfileInDir globs *.meta under dir and stamps Profile on
// any whose field is EMPTY (legacy sidecars predate the field), preserving
// UpdatedAt via SaveBranchMetaPreserveUpdated. Non-empty profiles are untouched.
func backfillBranchMetaProfileInDir(dir, profileKey string) {
	metas, err := filepath.Glob(filepath.Join(dir, "*.meta"))
	if err != nil || len(metas) == 0 {
		return
	}
	for _, metaPath := range metas {
		// LoadBranchMeta takes the *session* path (it appends ".meta"); strip it.
		sessionPath := strings.TrimSuffix(metaPath, ".meta")
		m, ok, err := agent.LoadBranchMeta(sessionPath)
		if err != nil || !ok {
			continue
		}
		if strings.TrimSpace(m.Profile) != "" {
			continue // preserve existing (e.g. cowork) — never overwrite.
		}
		m.Profile = profileKey
		if err := agent.SaveBranchMetaPreserveUpdated(sessionPath, m); err != nil {
			slog.Warn("profile migration: backfill branch meta profile", "path", metaPath, "err", err)
		}
	}
}

// rewriteTabSessionPaths rewrites persisted absolute SessionPaths in
// desktop-tabs.json that no longer exist on disk (because the session was just
// relocated into a profile partition) to their new location, found by basename
// match across the profile-partitioned session dirs. Best-effort: a tab whose
// path can't be relocated is left alone (the topicID-fallback recovery path in
// tab restore still works for non-empty topicIDs). The file is only rewritten
// if at least one path actually changed.
func rewriteTabSessionPaths(userDir string) {
	tabsPath := filepath.Join(desktopConfigDir(), tabsFileName)
	b, err := os.ReadFile(tabsPath)
	if err != nil {
		return // no tabs file yet — nothing to rewrite.
	}
	var f desktopTabsFile
	if err := json.Unmarshal(b, &f); err != nil {
		slog.Warn("profile migration: parse tabs file", "path", tabsPath, "err", err)
		return
	}

	// Gather every candidate destination dir: global dev/cowork/netdev
	// sessions and each project's per-profile sessions dir.
	candidateDirs := []string{
		config.SessionDirFor(config.ProfileDev),
		config.SessionDirFor(config.ProfileCowork),
		config.SessionDirFor(config.ProfileNetDev),
	}
	projectsDir := filepath.Join(userDir, "projects")
	if slugs, err := os.ReadDir(projectsDir); err == nil {
		for _, slug := range slugs {
			if !slug.IsDir() {
				continue
			}
			for _, profileKey := range []string{config.ProfileDev, config.ProfileCowork, config.ProfileNetDev} {
				candidateDirs = append(candidateDirs, filepath.Join(projectsDir, slug.Name(), profileKey, "sessions"))
			}
		}
	}

	changed := false
	for i := range f.Tabs {
		sp := strings.TrimSpace(f.Tabs[i].SessionPath)
		if sp == "" {
			continue
		}
		// Path still valid at its recorded location — nothing to do.
		if _, err := os.Stat(sp); err == nil {
			continue
		}
		base := filepath.Base(sp)
		if base == "" || base == "." || base == string(os.PathSeparator) {
			continue
		}
		// Find the relocated file by basename match in any candidate dir.
		newPath := ""
		for _, dir := range candidateDirs {
			cand := filepath.Join(dir, base)
			if _, err := os.Stat(cand); err == nil {
				newPath = cand
				break
			}
		}
		if newPath != "" {
			f.Tabs[i].SessionPath = newPath
			changed = true
		}
	}
	if !changed {
		return
	}

	// Atomic write back, matching saveTabsLocked's tmp+rename style.
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		slog.Warn("profile migration: marshal tabs file", "err", err)
		return
	}
	tmp := tabsPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		slog.Warn("profile migration: write tabs tmp", "path", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, tabsPath); err != nil {
		slog.Warn("profile migration: rename tabs file", "from", tmp, "to", tabsPath, "err", err)
	}
}

// pruneGhostProjects removes workspace entries that have no conversations in a
// profile — the cross-profile contamination left over by the migration's union
// of the legacy projects index (which copied every workspace root into BOTH
// desktop-projects-dev.json and -cowork.json). A project is a "ghost" in a
// profile when it has BOTH no indexed topics AND no session files in that
// profile's session directory. Such a ghost should not appear in that profile's
// sidebar/workspace switcher.
//
// This runs on EVERY startup (not gated by the migration marker) so it also
// heals disks that were already migrated before the union contamination was
// noticed. Idempotent: once pruned, a project only re-enters a profile's index
// when the user actually opens it there (addProject/CreateTopic), which creates
// a real topic/session — so it survives the next prune. Best-effort: errors are
// logged and swallowed.
func pruneGhostProjects() {
	// Clean up orphan .meta sidecar files (no corresponding .jsonl). These
	// accumulate when a tab is created (writes .meta via saveTabSessionMeta)
	// but the user never sends a message (Session.Save never fires, so .jsonl
	// is never created). They don't appear in ListSessions (which only reads
	// .jsonl), but they clutter the disk and confuse manual inspection.
	pruneOrphanMetaFiles()

	for _, profileKey := range []string{config.ProfileDev, config.ProfileCowork, config.ProfileNetDev} {
		f := loadProjectsFile(profileKey)
		if len(f.Projects) == 0 {
			continue
		}
		pruned := make([]desktopProject, 0, len(f.Projects))
		changed := false
		for _, p := range f.Projects {
			root := normalizeProjectRoot(p.Root)
			if root == "" {
				pruned = append(pruned, p)
				continue
			}
			// Keep if it has indexed topics in this profile.
			if len(p.Topics) > 0 {
				pruned = append(pruned, p)
				continue
			}
			// Keep if there is at least one session file under this profile's
			// project session dir (a fresh tab whose topic isn't indexed yet).
			if projectHasSessionsInProfile(root, profileKey) {
				pruned = append(pruned, p)
				continue
			}
			// Ghost: no topics, no sessions in this profile — it was copied here
			// by the migration union but has no real conversations here. Drop it.
			changed = true
			slog.Info("profile prune: dropping ghost project from profile", "root", root, "profile", profileKey)
		}
		if changed {
			f.Projects = pruned
			if err := saveProjectsFile(f, profileKey); err != nil {
				slog.Warn("profile prune: save projects file", "profile", profileKey, "err", err)
			}
		}
	}
}

// projectHasSessionsInProfile reports whether the project's session directory
// under the given profile contains at least one .jsonl transcript.
func projectHasSessionsInProfile(workspaceRoot, profileKey string) bool {
	dir := config.ProjectSessionDirFor(workspaceRoot, profileKey)
	if dir == "" {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			return true
		}
	}
	return false
}

// pruneOrphanMetaFiles removes .jsonl.meta sidecar files that have no
// corresponding .jsonl session file. These orphans accumulate when a tab is
// created (saveTabSessionMeta writes the .meta) but the user never sends a
// message (Session.Save never creates the .jsonl). Safe to delete — they carry
// no data, just an empty sidecar.
func pruneOrphanMetaFiles() {
	base := desktopConfigDir()
	if base == "" {
		return
	}
	// Walk all session directories under the config dir.
	filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		// Only look at .meta files (sidecars for .jsonl sessions).
		if !strings.HasSuffix(path, ".jsonl.meta") {
			return nil
		}
		// The corresponding .jsonl is the path without the ".meta" suffix.
		jsonlPath := strings.TrimSuffix(path, ".meta")
		if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
			// Orphan: .meta exists but .jsonl doesn't. Remove it.
			os.Remove(path)
		}
		return nil
	})
}
