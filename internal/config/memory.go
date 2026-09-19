package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// MemoryRootEnv overrides [memory] root for one process. It exists for
// portable/headless runs ("keep everything on this USB stick / this drive")
// and for tests, which must never write into a developer's real memory tree.
const MemoryRootEnv = "HIQ_MEMORY_ROOT"

// MemoryConfig is the user-facing [memory] section. It answers the two
// questions the memory panel and the background self-evolution agents both
// need answered: WHERE the memory data tree lives, and WHICH model maintains it.
//
// The section is USER-GLOBAL. LoadForRoot pins it back to the user config after
// the project merge (pinMemory) for the same reason [netdev] is pinned: a cloned
// repo's hiq.toml must never be able to redirect a user's memory — or their
// API spend — to a path (or a provider) the repo author chose.
type MemoryConfig struct {
	// Root relocates hiq's user MEMORY data tree away from the OS user
	// config dir: the portrait layer (profile/), the auto-memory store
	// (memory/, projects/<slug>/memory), project state
	// (projects/<slug>/{sessions,dream_state.json}) and the trust-domain ledger.
	// Empty keeps the default (%AppData%\hiq on Windows, ~/.config/hiq
	// elsewhere).
	//
	// config.toml, the credential store and the derived cache deliberately do
	// NOT follow: config.toml is the file that defines this field, so it cannot
	// live inside the tree it points at, and secrets/cache are not memory.
	Root string `toml:"root"`

	// Provider + Model select the lightweight model the background
	// self-evolution agents (Dream portrait consolidation, Distill workflow
	// extraction) run on, so a periodic maintenance pass never spends
	// main-model tokens. Either "provider/model", a bare provider name (its
	// default model), or a bare model name.
	//
	// Empty Provider/Model falls back to agent.fast_task_model, then to the
	// session's own provider — so an unconfigured setup behaves exactly as
	// before this section existed.
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	// Effort optionally pins the reasoning effort for those background runs
	// (low|medium|high|max); empty leaves the provider's default. A cheaper
	// model usually wants a cheaper effort.
	Effort string `toml:"effort"`

	// InjectIndex controls the compact L1/L2 fact index folded into the system
	// prompt — one line per saved fact (level · name · tags · hook), so the model
	// knows what memory exists and fetches bodies on demand. nil (unset) keeps it
	// ON: the index is what makes saved memory findable at all. Set false for the
	// smallest possible prompt.
	InjectIndex *bool `toml:"inject_index"`
	// IndexMaxChars caps that index (0 = built-in default, currently 1200
	// characters). Truncation is at a line boundary and leaves a visible marker.
	IndexMaxChars int `toml:"index_max_chars"`
}

// MemoryRootPath returns the configured data-tree root, or "" when the default
// (OS user config dir) applies. Relative paths resolve against the user config
// dir so a config copied between machines still lands somewhere sane.
func (c *Config) MemoryRootPath() string {
	if c == nil {
		return ""
	}
	return resolveMemoryRoot(strings.TrimSpace(c.Memory.Root))
}

// MemoryMaintenanceRef returns the model reference the background memory agents
// should run on: [memory] provider/model when set, else agent.fast_task_model,
// else "" (callers then keep the session's own provider). The shape matches
// Config.ResolveModel: "provider/model", "provider", or a bare model name.
func (c *Config) MemoryMaintenanceRef() string {
	if c == nil {
		return ""
	}
	if p := strings.TrimSpace(c.Memory.Provider); p != "" {
		if m := strings.TrimSpace(c.Memory.Model); m != "" {
			return p + "/" + m
		}
		return p
	}
	if m := strings.TrimSpace(c.Memory.Model); m != "" {
		return m
	}
	return strings.TrimSpace(c.Agent.FastTaskModel)
}

// MemoryEffort returns the pinned effort for background memory runs ("" = the
// provider's default).
func (c *Config) MemoryEffort() string {
	if c == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(c.Memory.Effort))
}

// MemoryInjectIndex reports whether the L1/L2 fact index rides the system prompt.
// Unset means on.
func (c *Config) MemoryInjectIndex() bool {
	if c == nil || c.Memory.InjectIndex == nil {
		return true
	}
	return *c.Memory.InjectIndex
}

// MemoryIndexMaxChars returns the injected index cap; 0 lets the memory package
// apply its default.
func (c *Config) MemoryIndexMaxChars() int {
	if c == nil || c.Memory.IndexMaxChars < 0 {
		return 0
	}
	return c.Memory.IndexMaxChars
}

// pinMemory restores [memory] from the USER config after the project merge, so
// a project hiq.toml cannot relocate the memory tree or redirect the
// maintenance model. Mirrors pinNetDev.
func pinMemory(cfg *Config) {
	uc := userConfigPath()
	if uc == "" {
		cfg.Memory = MemoryConfig{}
		return
	}
	var userCfg struct {
		Memory MemoryConfig `toml:"memory"`
	}
	if _, err := toml.DecodeFile(uc, &userCfg); err != nil {
		// Unreadable/absent user config: the merged-in project section is not
		// trustworthy either — fall back to empty (default root, no override).
		cfg.Memory = MemoryConfig{}
		return
	}
	cfg.Memory = userCfg.Memory
}

// memoryRootProbe memoizes the [memory] root scanned out of the user config,
// keyed by (path, mtime, size) so a mid-session edit is picked up on the next
// call without re-decoding the file on every path resolution.
var memoryRootProbe struct {
	sync.Mutex
	key string
	val string
}

// memoryRootOverride returns the configured memory-data root, or "" for the
// default. Precedence: $HIQ_MEMORY_ROOT > user config [memory] root.
//
// It reads the ONE key straight out of the user config instead of calling
// Load(), because Load() itself resolves paths through this function — calling
// it here would recurse. The probe is cheap and cached.
func memoryRootOverride() string {
	if v := strings.TrimSpace(os.Getenv(MemoryRootEnv)); v != "" {
		return resolveMemoryRoot(v)
	}
	uc := userConfigPath()
	if uc == "" {
		return ""
	}
	info, err := os.Stat(uc)
	if err != nil {
		return ""
	}
	key := fmt.Sprintf("%s|%d|%d", uc, info.ModTime().UnixNano(), info.Size())
	memoryRootProbe.Lock()
	defer memoryRootProbe.Unlock()
	if memoryRootProbe.key == key {
		return memoryRootProbe.val
	}
	var probe struct {
		Memory struct {
			Root string `toml:"root"`
		} `toml:"memory"`
	}
	val := ""
	if _, err := toml.DecodeFile(uc, &probe); err == nil {
		val = resolveMemoryRoot(strings.TrimSpace(probe.Memory.Root))
	}
	memoryRootProbe.key, memoryRootProbe.val = key, val
	return val
}

// resetMemoryRootProbe drops the memoized [memory] root. Tests call it after
// rewriting a user config so a cached value cannot leak between cases.
func resetMemoryRootProbe() {
	memoryRootProbe.Lock()
	memoryRootProbe.key, memoryRootProbe.val = "", ""
	memoryRootProbe.Unlock()
}

// resolveMemoryRoot normalizes a configured root: empty stays empty (default),
// a leading ~ expands to home, and a relative path resolves against the user
// config dir.
func resolveMemoryRoot(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") || strings.HasPrefix(raw, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			raw = filepath.Join(home, strings.TrimLeft(raw[1:], `/\`))
		}
	}
	if !filepath.IsAbs(raw) {
		if base := userDir(); base != "" {
			raw = filepath.Join(base, raw)
		}
	}
	if abs, err := filepath.Abs(raw); err == nil {
		raw = abs
	}
	return filepath.Clean(raw)
}

// MemoryRoot returns the effective root of hiq's user DATA tree — the
// parent of profile/, memory/ and projects/. It is [memory] root when
// configured, else the OS user config dir based default.
func MemoryRoot() string {
	if r := memoryRootOverride(); r != "" {
		return r
	}
	return userDir()
}

// DefaultMemoryRoot is the data root used when nothing is configured: the OS
// user config dir (…/hiq). The panel shows it as the "reset to default"
// target.
func DefaultMemoryRoot() string { return userDir() }

// MemoryRootFromEnv reports whether $HIQ_MEMORY_ROOT is overriding the
// configured root, so the UI can explain why editing the field has no effect.
func MemoryRootFromEnv() bool { return strings.TrimSpace(os.Getenv(MemoryRootEnv)) != "" }

// ConfiguredMemoryRoot returns the raw configured root — the env value when set,
// else the [memory] root written in the user config, else "". Unlike MemoryRoot
// it never falls back to the default and never normalizes for display purposes,
// so the panel can show the user exactly what they (or the environment) asked
// for.
func ConfiguredMemoryRoot() string {
	if v := strings.TrimSpace(os.Getenv(MemoryRootEnv)); v != "" {
		return v
	}
	uc := userConfigPath()
	if uc == "" {
		return ""
	}
	var probe struct {
		Memory struct {
			Root string `toml:"root"`
		} `toml:"memory"`
	}
	if _, err := toml.DecodeFile(uc, &probe); err != nil {
		return ""
	}
	return strings.TrimSpace(probe.Memory.Root)
}

// MemoryRootError reports why a configured root is unusable, or nil. A root is
// unusable when it exists as a non-directory, or when it cannot be created.
func MemoryRootError(root string) error {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	if info, err := os.Stat(root); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("memory root %q exists and is not a directory", root)
		}
		return nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("memory root %q is not creatable: %w", root, err)
	}
	return nil
}

// --- relocation ---

// memoryMigrateDirs are the top-level data SUBTREES that follow the memory root.
//
// "sessions" is the GLOBAL session store (config.SessionDir, <root>/sessions,
// with per-profile partitions): it resolves from the memory root too, so a
// relocation must carry it or the 工作台 history would be stranded on the old
// drive. Project-scoped sessions follow automatically, because they live under
// projects/<slug>/<profile>/sessions.
var memoryMigrateDirs = []string{"memory", "profile", "projects", "sessions", "trustdomain"}

// memoryMigrateFiles are the user-global memory/state FILES at the old root
// (the user-scope doc layer and the legacy cross-session skill usage stats).
var memoryMigrateFiles = []string{
	"hiq.md", "AGENTS.md", "CLAUDE.md",
	"hiq.local.md", "AGENTS.local.md", "CLAUDE.local.md",
	"skill_usage.json",
}

// MemoryMigrationReport summarizes a relocation so the UI can show the user
// exactly what moved and what was left alone.
type MemoryMigrationReport struct {
	From    string   `json:"from"`
	To      string   `json:"to"`
	Copied  []string `json:"copied"`  // top-level entries copied
	Skipped []string `json:"skipped"` // entries present at the source but not copied, with the reason
	Files   int      `json:"files"`   // regular files copied
	Bytes   int64    `json:"bytes"`   // bytes copied
}

// MigrateMemoryTree copies hiq's memory data from one root to another.
//
// It COPIES, never moves: the source tree stays intact so an interrupted run,
// a wrong target, or a change of mind loses nothing — the user deletes the old
// tree themselves once they have verified the new one. Existing files at the
// destination are not overwritten (the destination wins), which makes the
// operation idempotent and safe to re-run after adding new memories.
func MigrateMemoryTree(from, to string) (MemoryMigrationReport, error) {
	rep := MemoryMigrationReport{From: from, To: to, Copied: []string{}, Skipped: []string{}}
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if from == "" || to == "" {
		return rep, fmt.Errorf("memory migration needs both a source and a destination root")
	}
	fromAbs, err := filepath.Abs(from)
	if err != nil {
		return rep, err
	}
	toAbs, err := filepath.Abs(to)
	if err != nil {
		return rep, err
	}
	if fromAbs == toAbs {
		return rep, fmt.Errorf("memory migration source and destination are the same directory")
	}
	// Refuse to copy a tree into its own subtree (or vice versa): the walk
	// would either loop or produce a nested duplicate.
	if strings.HasPrefix(toAbs+string(os.PathSeparator), fromAbs+string(os.PathSeparator)) ||
		strings.HasPrefix(fromAbs+string(os.PathSeparator), toAbs+string(os.PathSeparator)) {
		return rep, fmt.Errorf("memory migration cannot nest source and destination")
	}
	if info, err := os.Stat(fromAbs); err != nil || !info.IsDir() {
		return rep, fmt.Errorf("memory migration source %q is not a directory", fromAbs)
	}
	if err := os.MkdirAll(toAbs, 0o755); err != nil {
		return rep, err
	}
	for _, name := range memoryMigrateDirs {
		src := filepath.Join(fromAbs, name)
		if info, err := os.Stat(src); err != nil || !info.IsDir() {
			continue
		}
		n, b, err := copyTree(src, filepath.Join(toAbs, name))
		if err != nil {
			return rep, err
		}
		rep.Copied = append(rep.Copied, name+"/")
		rep.Files += n
		rep.Bytes += b
	}
	for _, name := range memoryMigrateFiles {
		src := filepath.Join(fromAbs, name)
		info, err := os.Stat(src)
		if err != nil || info.IsDir() {
			continue
		}
		dst := filepath.Join(toAbs, name)
		if _, err := os.Stat(dst); err == nil {
			rep.Skipped = append(rep.Skipped, name+" (already exists at the destination)")
			continue
		}
		if err := copyFile(src, dst); err != nil {
			return rep, err
		}
		rep.Copied = append(rep.Copied, name)
		rep.Files++
		rep.Bytes += info.Size()
	}
	return rep, nil
}

// copyTree recursively copies src into dst, never overwriting an existing file.
// It returns the file count and byte total copied.
func copyTree(src, dst string) (int, int64, error) {
	var files int
	var bytes int64
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks/sockets: memory is plain files
		}
		if _, err := os.Stat(target); err == nil {
			return nil // destination wins
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := copyFile(path, target); err != nil {
			return err
		}
		files++
		bytes += info.Size()
		return nil
	})
	return files, bytes, err
}

// copyFile writes src to dst (creating parents), copying the mode.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
