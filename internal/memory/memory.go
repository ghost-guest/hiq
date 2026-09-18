package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Set is everything memory loaded for one session: the hierarchical docs, the
// profile-layer portrait, and a handle to the auto-memory store. It is assembled
// once at boot and folded into the system prompt by Compose. CWD, UserDir and
// ProfileName are retained so the controller can re-discover (reload) without
// re-deriving discovery context — losing ProfileName on reload would drop the
// mode partition and let dev/cowork memories leak together.
type Set struct {
	Docs        []Source // hiq.md / AGENTS.md, ascending precedence
	Store       Store    // auto-memory store (may be a zero/disabled Store)
	Index       string   // MEMORY.md contents at load time
	PromptIndex string   // compact L1/L2 fact index injected with the portrait
	Profile     string   // rendered portrait text (global + active mode), injected each turn
	ProfileName string   // raw active profile ("dev"|"cowork"), kept for reload
	CWD         string   // project working dir used for discovery
	UserDir     string   // user config root (may be "")
}

// Options configures discovery. CWD defaults to "." and UserDir is the user
// config root (config.MemoryUserDir()); a "" UserDir disables user-global docs
// and the auto-memory store. Profile is the active product mode ("dev"|"cowork")
// and partitions both the portrait layer and the auto-memory store by mode.
type Options struct {
	CWD     string
	UserDir string
	Profile string

	// SkipProjectDocs drops the workspace hierarchy (ancestor + project +
	// project-local AGENTS.md/hiq.md) and keeps only user-global memory.
	// Used by profiles whose subject is not the workspace (netdev: the session
	// operates on network devices, so a cloned repo's instruction files must
	// not steer the session). See NETDEV_SPEC §7.3.
	SkipProjectDocs bool

	// InjectIndex controls whether the compact L1/L2 fact index is folded into
	// the system prompt ([memory] inject_index, default on). It is the
	// "knowledge base index" half of memory: the model sees that a fact exists
	// and fetches its body with `recall`, instead of every fact riding every
	// turn. Turning it off keeps the prompt smaller for users who never ask the
	// model to recall anything.
	InjectIndex bool
	// IndexMaxChars caps the injected index (0 = DefaultPromptIndexMaxChars).
	IndexMaxChars int
}

// Load discovers all memory for a session: the hierarchical docs, the
// profile-layer portrait, and the auto-memory index. It is best-effort and
// never errors — missing files just mean less memory — so boot can call it
// unconditionally.
func Load(opts Options) *Set {
	cwd := opts.CWD
	if cwd == "" {
		cwd = "."
	}
	p := NormalizeProfile(opts.Profile)
	store := StoreFor(opts.UserDir, cwd, p)
	docs := discoverDocs(cwd, opts.UserDir)
	if opts.SkipProjectDocs {
		docs = userDocsOnly(docs)
	}
	promptIndex := ""
	if opts.InjectIndex {
		max := opts.IndexMaxChars
		if max <= 0 {
			max = DefaultPromptIndexMaxChars
		}
		promptIndex = store.PromptIndex(max)
	}
	return &Set{
		Docs:        docs,
		Store:       store,
		Index:       store.Index(),
		PromptIndex: promptIndex,
		Profile:     discoverProfile(opts.UserDir, p),
		ProfileName: p,
		CWD:         cwd,
		UserDir:     opts.UserDir,
	}
}

// userDocsOnly filters a discoverDocs result down to the user-global sources,
// dropping everything discovered from the workspace (ancestors, project root,
// and project-local overrides).
func userDocsOnly(docs []Source) []Source {
	out := make([]Source, 0, len(docs))
	for _, d := range docs {
		if d.Scope == ScopeUser {
			out = append(out, d)
		}
	}
	return out
}

// DocPath returns the doc-memory file a given scope writes to. To avoid splitting
// a project's memory across conventions, it prefers a file that already exists
// (hiq.md / AGENTS.md / CLAUDE.md, in that order); when none exists it
// creates the universal default (AGENTS.md / AGENTS.local.md). ScopeUser →
// <userDir>, ScopeLocal → <cwd> with the *.local.md names, anything else → <cwd>.
// Returns "" for ScopeUser when no user dir is configured.
func (s *Set) DocPath(scope Scope) string {
	dir := s.CWD
	names, def := docNames, defaultDocName
	switch scope {
	case ScopeUser:
		if s.UserDir == "" {
			return ""
		}
		dir = s.UserDir
	case ScopeLocal:
		names, def = localNames, defaultLocalName
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			return p // append to the doc already in use
		}
	}
	return filepath.Join(dir, def)
}

// Empty reports whether the set carries nothing to inject, so Compose can leave
// the base prompt byte-for-byte untouched (and the cache prefix maximal) when
// there is no memory at all. The portrait counts — a user with only a profile
// portrait (and no docs) still gets it in the prefix.
func (s *Set) Empty() bool {
	return s == nil || (len(s.Docs) == 0 && strings.TrimSpace(s.Index) == "" &&
		strings.TrimSpace(s.Profile) == "" && strings.TrimSpace(s.PromptIndex) == "")
}

// docScopes are the scopes the panel can target for a quick-add or a new doc.
// Ordered broad → specific for display.
var docScopes = []Scope{ScopeUser, ScopeProject, ScopeLocal}

// allowedDocPaths is the closed set of files WriteDoc / AppendDoc may touch: the
// canonical file for each writable scope, the current mode's portrait file, and
// every doc already discovered this session (so an ancestor or AGENTS.md the
// user is already editing stays editable). Keyed by absolute path. This bounds
// frontend-driven writes to real memory files rather than arbitrary paths.
func (s *Set) allowedDocPaths() map[string]bool {
	allow := map[string]bool{}
	for _, sc := range docScopes {
		if p := s.DocPath(sc); p != "" {
			allow[absOf(p)] = true
		}
	}
	// The active mode's portrait file is user-editable from the workspace's
	// preference panel (cowork edits cowork.md, dev edits dev.md). The shared
	// user.md / memory.md stay dream-maintained for now, per the design choice
	// to expose only the mode file to direct user input.
	if p := s.ProfilePath(); p != "" {
		allow[absOf(p)] = true
	}
	for _, d := range s.Docs {
		allow[absOf(d.Path)] = true
	}
	return allow
}

// ProfilePath returns the absolute path of the active mode's portrait file
// (<userDir>/profile/<mode>.md), or "" when there is no user config dir. This is
// the file the workspace preference panel reads and writes; it is also added to
// the WriteDoc whitelist so SaveDoc accepts it. The shared portrait files
// (user.md, memory.md) are intentionally not exposed here — only the mode file
// is user-editable for now.
func (s *Set) ProfilePath() string {
	if s.UserDir == "" || s.ProfileName == "" {
		return ""
	}
	return filepath.Join(s.UserDir, profileDir, NormalizeProfile(s.ProfileName)+".md")
}

// ProfileContent returns the current body of the active mode's portrait file,
// or "" when the file does not exist yet (a fresh install, or a mode the user
// has never written to). It is the read side the preference panel pairs with
// WriteDoc.
func (s *Set) ProfileContent() string {
	p := s.ProfilePath()
	if p == "" {
		return ""
	}
	return readProfileFile(p)
}

// WriteDoc overwrites a doc-memory file with body, after checking path is a
// recognized memory file (see allowedDocPaths). It is the save side of the
// desktop panel's in-place editor. The write lands on disk immediately but does
// NOT mutate the cache-stable system prefix — the edit folds into the prefix on
// the next session; to make it apply this session, the controller separately
// queues a turn-tail note. Returns the path written.
func (s *Set) WriteDoc(path, body string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("memory unavailable")
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("no path given")
	}
	if !s.allowedDocPaths()[absOf(path)] {
		return "", fmt.Errorf("refusing to write %q: not a recognized memory file", path)
	}
	return path, writeDocFile(path, body)
}

// Block renders the memory as a single Markdown section, or "" when empty. It is
// deterministic given the same files, which is what keeps it a stable cache
// prefix across sessions that don't change their memory.
//
// Design (v0.5): the portrait, the L1/L2 fact INDEX, and the doc hierarchy are
// injected. The index is one line per saved fact (level · name · tags · hook) —
// enough for the model to know a fact exists and ask `recall` for the body, which
// is what keeps the block small while still making memory findable. L3 working
// memory and the "how to use remember/forget" operating instructions stay out.
func (s *Set) Block() string {
	profile := strings.TrimSpace(s.Profile)
	if s.Empty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("# 记忆\n\n")
	if profile != "" {
		b.WriteString(profile)
		b.WriteString("\n")
	}
	// The fact index rides right after the portrait: identification is
	// deterministic and cheap, and the bodies stay on disk until `recall` asks
	// for one. Only L1/L2 appear — see Store.PromptIndex.
	if idx := strings.TrimSpace(s.PromptIndex); idx != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("## 已保存的记忆（索引；用 `recall` 读取正文）\n\n")
		b.WriteString(idx)
		b.WriteString("\n")
	}
	for _, d := range s.Docs {
		fmt.Fprintf(&b, "\n## %s (%s)\n\n%s\n", d.Path, d.Scope, strings.TrimSpace(d.Body))
	}
	return b.String()
}

// Compose folds the memory block onto the base system prompt and returns the
// durable cached-prefix string. Base stays first (it is the most stable text, so
// it remains a valid cache prefix even when memory changes between sessions);
// memory follows. With no memory, base is returned unchanged.
func Compose(base string, s *Set) string {
	block := s.Block()
	if block == "" {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return block
	}
	return strings.TrimRight(base, "\n") + "\n\n" + block
}

// profileDir is the portrait-layer root under the user config dir. It holds
// hand-maintained (and dream-maintained) markdown files that distil who the
// user is, the world they work in, and how this mode should behave — the small,
// always-injected core of memory, as opposed to the un-injected scattered
// archive.
//
// The split mirrors the Hermes design (USER.md / MEMORY.md) crossed with our
// mode partition: facts are bucketed by BOTH nature (who vs what) and mode
// (shared vs dev/cowork), so a fast-changing objective fact (a deadline) never
// has to share a file with a slow-changing identity fact (the user's role).
const profileDir = "profile"

// profileMaxChars caps the injected portrait so a runaway dream (or a user
// pasting a huge file) can't bloat the cache-stable system-prompt prefix. The
// portrait is meant to be tight prose (the dream prompt targets ≤500/≤300
// chars); this is the hard backstop when that soft target is exceeded. Mirrors
// skill.IndexMaxChars. Truncation leaves a visible marker so the model knows
// the portrait was clipped and can read the full file itself if it needs to.
const profileMaxChars = 2000

// DefaultPromptIndexMaxChars caps the injected L1/L2 fact index. It is a hard
// backstop, not a target: a user with hundreds of saved facts still gets a
// bounded, cache-stable prefix, with a truncation marker telling the model to
// search instead of assuming the list is complete. Sized so a typical index
// (a few dozen facts) fits whole while staying well under the portrait budget.
const DefaultPromptIndexMaxChars = 1200

// globalProfileFiles are the mode-agnostic portrait files, injected under every
// profile. user.md is the stable identity/preferences ("who you are", changes
// slowly); memory.md is the objective world state (environment, tooling,
// cross-project experience, deadlines) that dream updates regularly. Keeping
// them apart means a frequent memory.md rewrite can't destabilise user.md.
var globalProfileFiles = []string{"user.md", "memory.md"}

// discoverProfile reads the portrait-layer markdown for the active mode and
// returns it rendered for injection, or "" when nothing is authored yet. It
// loads the global files (user.md + memory.md — shared across modes) and the
// current mode's file (dev.md/cowork.md — mode-specific preferences + the
// mode's current tasks). All are plain user-editable markdown; we take the body
// verbatim, no restructuring, so what the user/dream writes is exactly what the
// model sees. Missing files are silently skipped — a fresh install has no
// portrait, and that's fine.
//
// After the portrait files comes the mode's selected preference preset (see
// presets.go), rendered under its own heading: the preset is the user's
// explicit current choice from the workspace panel, deliberately distinct from
// the dream-maintained portrait, so both inject together.
//
// The combined result is hard-capped at profileMaxChars: if a portrait file has
// grown beyond the budget, injection is truncated to the cap with a trailing
// marker rather than bloating every turn. The full file is still on disk for
// the model to read_file on demand.
func discoverProfile(userDir, profile string) string {
	if userDir == "" {
		return ""
	}
	root := filepath.Join(userDir, profileDir)
	var b strings.Builder
	for _, name := range globalProfileFiles {
		if body := readProfileFile(filepath.Join(root, name)); body != "" {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(body)
		}
	}
	if mode := NormalizeProfile(profile); mode != "" {
		if body := readProfileFile(filepath.Join(root, mode+".md")); body != "" {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(body)
		}
	}
	// The selected preset rides last: it is the freshest, most deliberate
	// guidance (just switched from the panel), so it closes the portrait and
	// stays inside the shared profileMaxChars budget below.
	if p := LoadPresets(userDir, profile).ActivePreset(); p != nil && p.Content != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "# 当前使用的偏好模板：%s\n\n%s", p.Name, p.Content)
	}
	out := strings.TrimSpace(b.String())
	if r := []rune(out); len(r) > profileMaxChars {
		out = string(r[:profileMaxChars]) + "\n\n… (portrait truncated to fit the injection budget; read the full file directly for the rest)"
	}
	return out
}

// readProfileFile reads a portrait markdown file and returns its trimmed body,
// or "" if missing/unreadable. A leading H1 ("# ...") is kept — it doubles as a
// section heading in the injected block (e.g. "# 关于用户").
func readProfileFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
