package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/fairpeer/internal/frontmatter"
)

// indexMu serializes MEMORY.md read-modify-write per directory. The store is a
// value type copied freely (Store{Dir:...}), so a per-instance mutex would
// split across copies and fail to serialize. A package-level map keyed by the
// canonical directory path makes every Save/Delete touching the same index file
// atomic, regardless of which Store value initiated it. Without this, concurrent
// remember calls (desktop panel edit + model tool call) race and one update
// clobbers the other's index line.
var indexMu = struct {
	sync.Mutex
	m map[string]*sync.Mutex
}{m: map[string]*sync.Mutex{}}

func indexLockFor(dir string) *sync.Mutex {
	indexMu.Lock()
	defer indexMu.Unlock()
	mu, ok := indexMu.m[dir]
	if !ok {
		mu = &sync.Mutex{}
		indexMu.m[dir] = mu
	}
	return mu
}

// Store is the auto-memory archive: a directory of one-fact-per-file Markdown
// notes with frontmatter, plus a MEMORY.md index of one line per fact. The
// model maintains it through the `remember`/`forget` tools. The index is NOT
// injected into the per-turn prompt (the portrait layer is) — it exists so the
// memory panel can list saved facts and the model can reach them on demand. The
// whole thing is plain files the user can edit by hand.
//
// v0.4: this is the slimmed store. The bitemporal model, decay/TTL, supersede
// chaining, FTS index, archive/restore, and conflict detection were removed —
// saved facts are no longer injected per turn, so the machinery that governed
// injection (status/importance/decay/compact) had no remaining job. Same-name
// save overwrites; history is the user's VCS.
type Store struct {
	Dir        string // .../fairpeer/projects/<slug>/<profile>/memory (L2 project facts)
	GlobalDir  string // .../fairpeer/memory/<profile> (L1 shared facts for this mode)
	SessionDir string // .../fairpeer/projects/<slug>/<profile>/memory/session (L3 working memory)
}

// Type classifies a memory, mirroring the auto-memory taxonomy. It is kept as a
// lightweight tag for the memory panel; it no longer drives storage routing
// (profile partitioning does that now) or injection.
type Type string

const (
	TypeUser      Type = "user"      // who the user is: role, preferences, expertise
	TypeFeedback  Type = "feedback"  // guidance on how to work
	TypeProject   Type = "project"   // ongoing work / goals / constraints
	TypeReference Type = "reference" // pointers to external resources (URLs, tickets)
)

// validTypes is the closed set the `remember` tool accepts; anything else
// normalises to TypeProject.
var validTypes = map[Type]bool{TypeUser: true, TypeFeedback: true, TypeProject: true, TypeReference: true}

// NormalizeType coerces an arbitrary string to a known Type, defaulting to
// TypeProject so a sloppy tool argument never blocks a save.
func NormalizeType(s string) Type {
	t := Type(strings.ToLower(strings.TrimSpace(s)))
	if validTypes[t] {
		return t
	}
	return TypeProject
}

// validProfiles is the closed set of product-mode partitions memory stores
// under. "dev" is the unprofiled floor (the coding mode a config with no
// [[profiles]] is always in), so an empty/unknown profile normalises to it —
// callers that never set a profile keep their existing memory path.
var validProfiles = map[string]bool{"dev": true, "cowork": true}

// NormalizeProfile coerces an arbitrary string to a known profile partition,
// defaulting to "dev" so a sloppy or empty caller never lands memories in a
// dangling directory. This is for *path* derivation; use NormalizeProfileScope
// (remember.go) when saving, where unknowns default to "global" instead.
func NormalizeProfile(s string) string {
	p := strings.ToLower(strings.TrimSpace(s))
	if validProfiles[p] {
		return p
	}
	return "dev"
}

// Memory is one stored fact.
//
// v0.4: the structure is deliberately small. The 13 bitemporal/lifecycle/
// classification fields that existed to govern per-turn injection (status,
// importance, valid_from/to, ttl, category, tags, supersede*, access tracking)
// were removed because saved facts are no longer injected — the portrait layer
// is. Name + Body + Type carry the fact; Profile records which mode it belongs
// to; CreatedAt supports a light-weight ordering in the panel.
type Memory struct {
	Name      string    `json:"name,omitempty"`       // kebab-case slug; also the file stem (<name>.md)
	Body      string    `json:"body,omitempty"`       // the fact itself (Markdown)
	Type      Type      `json:"type,omitempty"`       // user/feedback/project/reference (panel tag)
	Profile   string    `json:"profile,omitempty"`    // "global" | "dev" | "cowork" | "project"
	Level     Level     `json:"level,omitempty"`      // l1 global | l2 project | l3 session (see level.go)
	Tags      []string  `json:"tags,omitempty"`       // retrieval keywords (see level.go)
	CreatedAt time.Time `json:"created_at,omitempty"` // first write (immutable)
}

// StoreFor resolves the auto-memory directories for a project working dir under
// the user config root, partitioned by profile so dev/cowork memories never
// leak across modes. profile is the active product mode ("dev" | "cowork" | "");
// "" is normalised to "dev" (the unprofiled floor). The layout:
//
//	GlobalDir = <userDir>/memory/<profile>   shared facts for this mode
//	Dir       = <userDir>/projects/<slug>/<profile>/memory   project-scoped facts
//
// so switching profile points every Store method at a disjoint subtree —
// remember/forget/List all follow without per-call plumbing. A "" userDir
// (config dir unresolvable) yields a zero Store, which all methods treat as a
// disabled no-op.
func StoreFor(userDir, cwd, profile string) Store {
	if userDir == "" {
		return Store{}
	}
	p := NormalizeProfile(profile)
	projectDir := filepath.Join(userDir, "projects", slugify(absOf(cwd)), p, "memory")
	return Store{
		Dir:        projectDir,
		GlobalDir:  filepath.Join(userDir, "memory", p),
		SessionDir: filepath.Join(projectDir, sessionSubdir),
	}
}

// sessionSubdir is the L3 bucket inside a project's memory dir. Keeping it as a
// SUBdirectory (rather than a sibling) means List's non-recursive scan of the
// project dir never picks up working memory, so a stale L3 note can never be
// mistaken for a durable project fact.
const sessionSubdir = "session"

// DataRoot recovers the user data root the store lives under (…/fairpeer) from
// its own layout: GlobalDir is <root>/memory/<profile>. Promoted artifacts
// (skills/, plugins/) are written relative to it, so relocating the memory root
// carries them along instead of leaving them on the old drive. Empty when the
// store is disabled.
func (s Store) DataRoot() string {
	// GlobalDir is the most reliable anchor: it is always present in a live
	// store and exactly two levels below the root.
	if s.GlobalDir != "" {
		return filepath.Dir(filepath.Dir(s.GlobalDir))
	}
	if s.Dir != "" {
		return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(s.Dir))))
	}
	return ""
}

// DirForLevel returns the directory a memory of the given level belongs in:
// L1 → the shared profile bucket, L2 → the project bucket, L3 → the project's
// session bucket. Missing directories degrade toward the project dir so a save
// always has a concrete target.
func (s Store) DirForLevel(level Level) string {
	switch level {
	case LevelSession:
		if s.SessionDir != "" {
			return s.SessionDir
		}
		return s.Dir
	case LevelProject:
		return s.Dir
	default:
		if s.GlobalDir != "" {
			return s.GlobalDir
		}
		return s.Dir
	}
}

// DirFor returns the directory a memory of the given profile should be stored
// in. Kept for the pre-level call sites and for reading legacy trees: a
// "project" profile is L2, everything else is L1.
func (s Store) DirFor(profile string) string {
	if strings.EqualFold(strings.TrimSpace(profile), "project") {
		return s.DirForLevel(LevelProject)
	}
	if s.GlobalDir == "" {
		return s.Dir
	}
	return s.GlobalDir
}

// dirs returns the directories to read from, in order: GlobalDir (L1) first, then
// Dir (L2), then SessionDir (L3). This is the read-side union that lets
// List/Index/recall see every level of the active project.
func (s Store) dirs() []string {
	out := make([]string, 0, 3)
	seen := map[string]bool{}
	for _, d := range []string{s.GlobalDir, s.Dir, s.SessionDir} {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// indexFile is the human-readable index of saved memories.
const indexFile = "MEMORY.md"

// slugify turns an absolute project path into a single filesystem-safe segment,
// matching the auto-memory convention (path separators → '-'), e.g.
// "/Users/me/proj" → "-Users-me-proj".
func slugify(absPath string) string {
	r := strings.NewReplacer(string(os.PathSeparator), "-", "/", "-", "\\", "-", ":", "-")
	return r.Replace(absPath)
}

// indexLineRe matches the file stem in a Markdown link line, e.g.
// `- [Title](prefers-tabs.md) — hook` captures "prefers-tabs".
var indexLineRe = regexp.MustCompile(`\]\(([^)]+)\.md\)`)

// Index returns the MEMORY.md contents (the per-line index of saved memories),
// or "" if there are none yet. GlobalDir entries come first, then Dir entries,
// each group sorted alphabetically by name; a name present in both resolves to
// its global entry (global is the broader truth).
func (s Store) Index() string {
	type entry struct{ line string }
	dirs := s.dirs()
	groups := make([]map[string]entry, len(dirs))
	for i := range groups {
		groups[i] = map[string]entry{}
	}
	seen := map[string]int{}
	for gi, dir := range dirs {
		b, err := os.ReadFile(filepath.Join(dir, indexFile))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if mt := indexLineRe.FindStringSubmatch(line); mt != nil {
				name := mt[1]
				if _, exists := seen[name]; exists {
					continue
				}
				seen[name] = gi
				groups[gi][name] = entry{line: strings.TrimRight(line, "\r")}
			}
		}
	}
	total := 0
	for _, g := range groups {
		total += len(g)
	}
	if total == 0 {
		return ""
	}
	var b strings.Builder
	for gi := range groups {
		names := make([]string, 0, len(groups[gi]))
		for n := range groups[gi] {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			b.WriteString(groups[gi][n].line)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// Path returns the absolute file path a memory with the given name lives at.
// It checks GlobalDir first, then Dir; not found returns the GlobalDir default
// (or Dir when GlobalDir is empty) so a save always has a concrete target.
func (s Store) Path(name string) string {
	stem := slug(name) + ".md"
	for _, dir := range s.dirs() {
		if dir == "" {
			continue
		}
		p, err := safeJoin(dir, stem)
		if err != nil {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, dir := range s.dirs() {
		if dir != "" {
			p, _ := safeJoin(dir, stem)
			return p
		}
	}
	return ""
}

// Save writes (or overwrites) a memory file and refreshes its MEMORY.md index
// line. It is the single mutation entry point — the `remember` tool and the
// desktop editor both go through here so the index never drifts from the files.
// Same-name overwrite is a plain overwrite (no archive/supersede); prior
// versions live in the user's VCS if tracked. Returns the path written.
func (s Store) Save(m Memory) (string, error) {
	level := LevelOf(m)
	m.Level = level
	m.Tags = normalizeTags(m.Tags)
	dir := s.DirForLevel(level)
	if dir == "" {
		return "", fmt.Errorf("memory store unavailable (no user config dir)")
	}
	name := slug(m.Name)
	if name == "" {
		return "", fmt.Errorf("memory needs a name")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path, err := safeJoin(dir, name+".md")
	if err != nil {
		return "", err
	}
	// Preserve CreatedAt across an overwrite (it's immutable); otherwise stamp now.
	if existing, ok := loadMemory(path); ok && !existing.CreatedAt.IsZero() {
		m.CreatedAt = existing.CreatedAt
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if err := os.WriteFile(path, []byte(render(m, name)), 0o644); err != nil {
		return "", err
	}
	if err := reindexIn(dir, name, m); err != nil {
		return path, err
	}
	// Drop any stale copy of the same name from the *other* directory, so a fact
	// doesn't appear twice after a profile/routing change. Best-effort.
	for _, other := range s.dirs() {
		if other == dir || other == "" {
			continue
		}
		_ = removeActiveMemoryInDir(other, name)
		flushIndexExcept(other, name)
	}
	return path, nil
}

// Delete removes a memory file and its MEMORY.md line — the model's `forget`
// path and the user's way to prune a stale fact. A missing file is not an error;
// the goal state (gone) holds either way. v0.4: this is a hard delete (the old
// .archive/ traceability layer was removed with the bitemporal machinery).
func (s Store) Delete(name string) error {
	if s.Dir == "" && s.GlobalDir == "" {
		return fmt.Errorf("memory store unavailable (no user config dir)")
	}
	name = slug(name)
	if name == "" {
		return fmt.Errorf("memory needs a name")
	}
	for _, dir := range s.dirs() {
		if dir == "" {
			continue
		}
		_ = removeActiveMemoryInDir(dir, name)
		flushIndexExcept(dir, name)
	}
	return nil
}

// List returns all saved memories across the active mode's directories (global +
// project), sorted alphabetically by name. It is what the memory panel and the
// model's on-demand lookups read. Only .md files in the directory roots are
// scanned (no recursion, no .archive — that layer is gone).
func (s Store) List() []Memory {
	if s.Dir == "" && s.GlobalDir == "" {
		return nil
	}
	byName := map[string]Memory{}
	order := []string{}
	for _, dir := range s.dirs() {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			// Skip the index file itself — it lives in the same directory but is
			// not a fact. (.archive/ subdirs are skipped by the IsDir check.)
			if e.Name() == indexFile {
				continue
			}
			path := filepath.Join(dir, e.Name())
			m, ok := loadMemory(path)
			if !ok {
				continue
			}
			n := slug(m.Name)
			if _, exists := byName[n]; !exists {
				order = append(order, n)
			}
			byName[n] = m // first-seen (global) wins on collision
		}
	}
	sort.Strings(order)
	out := make([]Memory, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out
}

// --- index (MEMORY.md) helpers ---

// reindexIn rewrites dir's MEMORY.md so it carries an up-to-date line for name.
// Other lines are preserved verbatim so a user's hand-edits survive a save.
func reindexIn(dir, name string, m Memory) error {
	mu := indexLockFor(dir)
	mu.Lock()
	defer mu.Unlock()
	lines := readIndexLines(dir)
	lines[name] = indexLine(name, m)
	return flushIndexIn(dir, lines)
}

// indexLine renders the single MEMORY.md row for a memory: a Markdown link to
// the file plus a one-line hook derived from the body's first line.
func indexLine(name string, m Memory) string {
	title := displayTitle(firstLine(m.Body), name)
	hook := oneLine(firstLine(m.Body))
	if hook == "" {
		hook = oneLine(m.Body)
	}
	return fmt.Sprintf("- [%s](%s.md) — %s", title, name, hook)
}

// readIndexLines loads dir's MEMORY.md into a name→line map, skipping blanks and
// the header. Missing file → empty map.
func readIndexLines(dir string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(filepath.Join(dir, indexFile))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		if mt := indexLineRe.FindStringSubmatch(line); mt != nil {
			out[mt[1]] = line
		}
	}
	return out
}

// flushIndexIn writes the name→line map back to dir's MEMORY.md, sorted by name,
// with the standard header. Caller holds indexLockFor(dir).
func flushIndexIn(dir string, lines map[string]string) error {
	names := make([]string, 0, len(lines))
	for n := range lines {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# Memory\n\n")
	for _, n := range names {
		b.WriteString(lines[n])
		b.WriteString("\n")
	}
	return os.WriteFile(filepath.Join(dir, indexFile), []byte(b.String()), 0o644)
}

// flushIndexExcept rewrites dir's MEMORY.md without the given name's line.
func flushIndexExcept(dir, name string) {
	mu := indexLockFor(dir)
	mu.Lock()
	defer mu.Unlock()
	lines := readIndexLines(dir)
	delete(lines, name)
	_ = flushIndexIn(dir, lines)
}

// removeActiveMemoryInDir deletes <dir>/<name>.md if it exists. Returns an error
// only if the file was present but could not be removed; a missing file is nil
// (idempotent).
func removeActiveMemoryInDir(dir, name string) error {
	path := filepath.Join(dir, name+".md")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return os.Remove(path)
}

// --- serialization ---

// render writes a Memory as frontmatter + body. The frontmatter carries only
// the small set of fields that survive the v0.4 slim-down; everything else is
// derivable or gone.
func render(m Memory, name string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", name)
	if m.Type != "" {
		fmt.Fprintf(&b, "type: %s\n", m.Type)
	}
	if m.Profile != "" {
		fmt.Fprintf(&b, "profile: %s\n", m.Profile)
	}
	if lvl := LevelOf(m); lvl != "" {
		fmt.Fprintf(&b, "level: %s\n", lvl)
	}
	if len(m.Tags) > 0 {
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(normalizeTags(m.Tags), ", "))
	}
	if !m.CreatedAt.IsZero() {
		fmt.Fprintf(&b, "created_at: %q\n", m.CreatedAt.UTC().Format(time.RFC3339))
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(m.Body))
	b.WriteString("\n")
	return b.String()
}

// loadMemory parses a memory file. Returns ok=false on missing/unreadable file
// or a frontmatter parse failure. Missing created_at falls back to file mtime.
func loadMemory(path string) (Memory, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Memory{}, false
	}
	fm, body := frontmatter.Split(string(b))
	name := strings.ToLower(strings.TrimSpace(fm["name"]))
	if name == "" {
		// Fall back to the file stem when frontmatter omits name (legacy files).
		name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	m := Memory{
		Name:    name,
		Body:    strings.TrimSpace(body),
		Type:    Type(strings.ToLower(strings.TrimSpace(fm["type"]))),
		Profile: strings.ToLower(strings.TrimSpace(fm["profile"])),
		Level:   Level(strings.ToLower(strings.TrimSpace(fm["level"]))),
		Tags:    ParseTags(fm["tags"]),
	}
	// A record predating the level hierarchy carries no `level`; infer it from the
	// legacy `project` profile so an existing memory tree keeps its reach.
	if !validLevels[m.Level] {
		m.Level = LevelOf(Memory{Profile: m.Profile})
	}
	if raw := strings.TrimSpace(fm["created_at"]); raw != "" {
		// Tolerate quoted and unquoted RFC3339 / date-only values.
		raw = strings.Trim(raw, "\"'")
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			m.CreatedAt = t
		} else if t, err := time.Parse("2006-01-02", raw); err == nil {
			m.CreatedAt = t
		}
	}
	if m.CreatedAt.IsZero() {
		if info, err := os.Stat(path); err == nil {
			m.CreatedAt = info.ModTime()
		}
	}
	return m, true
}

// --- small text helpers ---

// safeJoin guards against path escape: it refuses to join a name that climbs
// out of base. Returns the joined path or an error.
//
// Uses filepath.Rel + filepath.IsLocal (the same robust check checkpoint's
// safePath uses) instead of a fragile prefix check. The old HasPrefix form was
// bypassable by a sibling directory sharing base's name prefix
// (base=".../memory", name="../memoryevil/x" → joined ".../memoryevil/x"
// passes the prefix check and escapes). See security audit finding A10.
// (In practice slug() neutralizes separators before safeJoin is reached, but
// defense in depth — don't rely on a single layer.)
func safeJoin(base, name string) (string, error) {
	// Reject any path with ".." components before filepath.Clean normalizes
	// separators — defense in depth against both forward and backslash escapes.
	if strings.Contains(name, "..") {
		return "", fmt.Errorf("path escapes memory dir: %q", name)
	}
	cleanBase := filepath.Clean(base)
	p := filepath.Join(cleanBase, name)
	rel, err := filepath.Rel(cleanBase, p)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("path escapes memory dir: %q", name)
	}
	return p, nil
}

// slugRe collapses any run of non-[a-z0-9] to a single hyphen.
var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slug turns an arbitrary name into a kebab-case file stem.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// oneLine collapses a string to a single trimmed line with ALL whitespace runs
// (spaces, tabs, newlines) folded to single spaces. Used both for index hooks
// (keeps MEMORY.md tidy) and by AppendDoc (so a multi-line "#" note can't
// corrupt the single-line bullet format). A newline does NOT truncate — the
// whole input is preserved, just flattened to one line.
func oneLine(s string) string {
	var b strings.Builder
	inSpace := false
	for _, r := range strings.TrimSpace(s) {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// displayTitle returns a human-readable label for a memory: the fallback if
// given, otherwise the name with hyphens turned to spaces.
func displayTitle(fallback, name string) string {
	if t := strings.TrimSpace(fallback); t != "" {
		return t
	}
	return strings.ReplaceAll(name, "-", " ")
}

// promptIndexLine renders one row of the injected memory index: level, name,
// one-line hook, and the retrieval tags. Tags come last so a truncated index
// still reads well.
func promptIndexLine(m Memory, level Level) string {
	hook := oneLine(firstLine(m.Body))
	if hook == "" {
		hook = oneLine(m.Body)
	}
	if len(hook) > 120 {
		hook = string([]rune(hook)[:120]) + "…"
	}
	line := fmt.Sprintf("- [%s] %s — %s", level.Label(), m.Name, hook)
	if len(m.Tags) > 0 {
		line += "  #" + strings.Join(m.Tags, " #")
	}
	return line
}

// PromptIndex renders the compact memory map that rides the cache-stable system
// prompt: one line per injected (L1/L2) fact — level, name, tags and a one-line
// hook — so the model knows what memory exists without loading any of it, and can
// pull a specific fact with `recall`. This is the "knowledge base index" side of
// memory: identification is free, content is fetched on demand.
//
// L3 is deliberately excluded — it is working memory for the current task and
// would be stale by the next session.
//
// The output is deterministic for the same files (sorted, no timestamps), which
// is what keeps it a valid cache prefix: it changes only when memory changes, and
// such a change rides the turn tail and folds into the prefix next session.
// maxChars > 0 caps the block; truncation happens at a line boundary and leaves a
// visible marker so the model knows to search instead of assuming nothing else
// exists.
func (s Store) PromptIndex(maxChars int) string {
	if s.Dir == "" && s.GlobalDir == "" {
		return ""
	}
	type row struct {
		name string
		line string
	}
	var rows []row
	seen := map[string]bool{}
	for _, dir := range s.dirs() {
		if dir == "" || dir == s.SessionDir {
			continue // L3 never enters the prompt
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == indexFile {
				continue
			}
			m, ok := loadMemory(filepath.Join(dir, e.Name()))
			if !ok {
				continue
			}
			lvl := LevelOf(m)
			if !lvl.Injected() {
				continue
			}
			n := slug(m.Name)
			if n == "" || seen[n] {
				continue // broadest scope wins on a name collision
			}
			seen[n] = true
			rows = append(rows, row{name: n, line: promptIndexLine(m, lvl)})
		}
	}
	if len(rows) == 0 {
		return ""
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, r.line)
	}
	out := strings.Join(lines, "\n")
	if maxChars > 0 {
		if r := []rune(out); len(r) > maxChars {
			cut := string(r[:maxChars])
			if i := strings.LastIndex(cut, "\n"); i > 0 {
				cut = cut[:i]
			}
			out = cut + "\n… (memory index truncated; call `recall` to search the rest)"
		}
	}
	return out
}
