package portable

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// Kind names a bundle's contents. The value travels in the manifest so an
// import can refuse a bundle it does not understand rather than guess.
type Kind string

const (
	// KindSkill is a bundle holding exactly one skill directory.
	KindSkill Kind = "fairpeer.skill"
	// KindSkills is a bundle holding one or more skill directories.
	KindSkills Kind = "fairpeer.skills"
	// KindMemory is a bundle holding memory documents (fairpeer.md / AGENTS.md
	// and friends), never sessions and never secrets.
	KindMemory Kind = "fairpeer.memory"
)

// Known reports whether a kind is one this build can import.
func (k Kind) Known() bool {
	switch k {
	case KindSkill, KindSkills, KindMemory:
		return true
	default:
		return false
	}
}

// Filesystem and format constants.
const (
	// ManifestName is the bundle's table of contents. It sits at the zip root,
	// so an import can read the manifest before extracting anything.
	ManifestName = "fairpeer-bundle.json"
	// AppName identifies the producer, so a bundle from a fork is recognisable.
	AppName = "fairpeer"
	// SchemaVersion is the manifest format this build writes and the newest it
	// accepts. An older bundle imports (fields are additive); a newer one is
	// refused, because it may rely on semantics this build does not implement.
	SchemaVersion = 1
	// Ext is the file extension a bundle is offered as.
	Ext = ".fairpeer.zip"
	// skillFileName is the canonical file inside a directory-layout skill. It is
	// duplicated from internal/skill rather than imported so this package stays
	// free of a domain dependency: a bundle is a file format, and reaching into
	// the skill loader to learn a filename would couple the two for no gain.
	skillFileName = "SKILL.md"
)

// Limits. These exist so an imported bundle cannot exhaust the disk or the
// import loop: the reference implementation has no ceiling at all, which turns
// "open this zip someone sent me" into an unbounded operation.
const (
	// MaxTotalBytes caps the summed size of all extracted files.
	MaxTotalBytes = 32 << 20 // 32 MiB
	// MaxFileBytes caps one file. Skills are text plus small assets; anything
	// larger is a mistake or an attack.
	MaxFileBytes = 8 << 20 // 8 MiB
	// MaxFiles caps how many entries a bundle may hold.
	MaxFiles = 512
	// MaxPathDepth caps how deep a bundled path may nest, so a pathological
	// archive cannot create a kilometre of directories.
	MaxPathDepth = 8
)

// Sentinel errors. Callers match these rather than string-compare messages.
var (
	// ErrUnsupported is returned for a bundle this build cannot read: an
	// unknown kind, a missing manifest, or a newer schema.
	ErrUnsupported = errors.New("portable: unsupported bundle")
	// ErrUnsafe is returned when a bundle tries to escape its destination or
	// carries something that must never travel in a bundle.
	ErrUnsafe = errors.New("portable: unsafe bundle")
	// ErrTooLarge is returned when a bundle exceeds a limit.
	ErrTooLarge = errors.New("portable: bundle exceeds size limit")
	// ErrCorrupt is returned when a bundle's bytes do not match its manifest.
	ErrCorrupt = errors.New("portable: bundle failed integrity check")
)

// Entry is one file recorded in the manifest.
type Entry struct {
	// Path is the file's location inside the zip, always slash-separated and
	// always relative. It is the key an import validates before extracting.
	Path string `json:"path"`
	// Size and SHA256 let an import detect a truncated download or a zip that
	// was edited after export.
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Item names one logical unit inside the bundle — a skill, or a memory doc —
// so an import can report "imported skill X" rather than listing loose files,
// and so a future per-item conflict policy has something to hang off.
type Item struct {
	// Name is the skill's directory name (or the doc's file name).
	Name string `json:"name"`
	// Description is the skill's one-liner / the doc's first heading, carried
	// for a preview before the user commits to importing.
	Description string `json:"description,omitempty"`
	// Paths are the manifest Entry paths belonging to this item.
	Paths []string `json:"paths"`
}

// Manifest is the bundle's table of contents.
type Manifest struct {
	Kind          Kind      `json:"kind"`
	SchemaVersion int       `json:"schemaVersion"`
	App           string    `json:"app"`
	ExportedAt    time.Time `json:"exportedAt"`
	// Name and Description describe the bundle as a whole, so a group export
	// reads as one thing in a file picker.
	Name        string  `json:"name,omitempty"`
	Description string  `json:"description,omitempty"`
	Items       []Item  `json:"items"`
	Entries     []Entry `json:"entries"`
	// Skipped lists paths the export deliberately left out (denied name, or an
	// extension outside the whitelist). Surfacing them beats a silent omission:
	// "your skill shipped without its .exe helper" should be visible.
	Skipped []string `json:"skipped,omitempty"`
	// Note is a human-readable line rendered by an import wizard.
	Note string `json:"note,omitempty"`
}

// TotalBytes is the summed size of every recorded entry.
func (m Manifest) TotalBytes() int64 {
	var n int64
	for _, e := range m.Entries {
		n += e.Size
	}
	return n
}

// validate checks the manifest's own consistency and the bundle's limits. It is
// called before any extraction, so a hostile manifest cannot reach the
// filesystem.
func (m Manifest) validate() error {
	if m.App != "" && m.App != AppName {
		return fmt.Errorf("%w: produced by %q", ErrUnsupported, m.App)
	}
	if m.SchemaVersion <= 0 {
		return fmt.Errorf("%w: missing schema version", ErrUnsupported)
	}
	if m.SchemaVersion > SchemaVersion {
		return fmt.Errorf("%w: schema %d is newer than this build understands (%d)",
			ErrUnsupported, m.SchemaVersion, SchemaVersion)
	}
	if !m.Kind.Known() {
		return fmt.Errorf("%w: unknown kind %q", ErrUnsupported, m.Kind)
	}
	if len(m.Entries) == 0 {
		return fmt.Errorf("%w: bundle records no files", ErrCorrupt)
	}
	if len(m.Entries) > MaxFiles {
		return fmt.Errorf("%w: %d files exceeds the %d limit", ErrTooLarge, len(m.Entries), MaxFiles)
	}
	var total int64
	seen := make(map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		if seen[e.Path] {
			return fmt.Errorf("%w: %q is listed twice", ErrCorrupt, e.Path)
		}
		seen[e.Path] = true
		if err := checkRelPath(e.Path); err != nil {
			return err
		}
		if isDeniedName(path.Base(e.Path)) {
			return fmt.Errorf("%w: %q must never travel in a bundle", ErrUnsafe, e.Path)
		}
		if e.Size < 0 || e.Size > MaxFileBytes {
			return fmt.Errorf("%w: %q is %d bytes (per-file limit %d)", ErrTooLarge, e.Path, e.Size, MaxFileBytes)
		}
		total += e.Size
	}
	if total > MaxTotalBytes {
		return fmt.Errorf("%w: %d bytes exceeds the %d limit", ErrTooLarge, total, MaxTotalBytes)
	}
	return nil
}

// checkRelPath rejects anything that could land outside the bundle root. It is
// the first of two gates; the second is the destination containment check in
// Import, because a path can be relative and still escape once joined.
func checkRelPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("%w: empty path", ErrUnsafe)
	}
	if strings.Contains(p, `\`) {
		return fmt.Errorf("%w: %q uses a backslash", ErrUnsafe, p)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("%w: %q is absolute", ErrUnsafe, p)
	}
	// A drive letter or UNC prefix would survive filepath.Join on Windows.
	if len(p) >= 2 && p[1] == ':' {
		return fmt.Errorf("%w: %q carries a drive letter", ErrUnsafe, p)
	}
	if p != path.Clean(p) {
		return fmt.Errorf("%w: %q is not in canonical form", ErrUnsafe, p)
	}
	depth := 0
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".":
			return fmt.Errorf("%w: %q has an empty segment", ErrUnsafe, p)
		case "..":
			return fmt.Errorf("%w: %q escapes the bundle root", ErrUnsafe, p)
		}
		depth++
	}
	if depth > MaxPathDepth {
		return fmt.Errorf("%w: %q nests deeper than %d levels", ErrUnsafe, p, MaxPathDepth)
	}
	return nil
}
