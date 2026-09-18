package portable

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SkillSource is one skill to bundle under the given name.
type SkillSource struct {
	// Name is the directory name the skill will land in on import — normally
	// the skill's canonical name. It must be a single path element.
	Name string
	// Dir is the skill's source: either its directory (canonical
	// <dir>/SKILL.md layout) or a single .md file (the flat <name>.md layout).
	// A flat file is bundled as <name>/SKILL.md so both layouts import the same.
	Dir string
}

// DocSource is one memory document to bundle.
type DocSource struct {
	// Name is the file's base name inside the bundle (e.g. hiq.md).
	Name string
	// Path is the absolute source file.
	Path string
}

// ExportOptions configures an export.
type ExportOptions struct {
	// Skills are the skill directories to include (kind skill / skills).
	Skills []SkillSource
	// Docs are the memory documents to include (kind memory).
	Docs []DocSource
	// Name and Description describe the bundle in a file picker.
	Name        string
	Description string
	// Scrub, when non-nil, rewrites each text entry before it is written. It is
	// how the PII pass hooks in: a bundle is meant to leave the machine it was
	// made on, so it is the last place a secret should be caught.
	Scrub func(string) string
	// Now overrides the clock (tests).
	Now func() time.Time
}

// ExportSkills writes a bundle holding the given skill directories.
func ExportSkills(w io.Writer, opts ExportOptions) (*Manifest, error) {
	if len(opts.Skills) == 0 {
		return nil, fmt.Errorf("%w: no skills to export", ErrUnsupported)
	}
	kind := KindSkills
	if len(opts.Skills) == 1 {
		kind = KindSkill
	}
	return write(w, kind, opts)
}

// ExportMemory writes a bundle holding the given memory documents.
func ExportMemory(w io.Writer, opts ExportOptions) (*Manifest, error) {
	if len(opts.Docs) == 0 {
		return nil, fmt.Errorf("%w: no documents to export", ErrUnsupported)
	}
	return write(w, KindMemory, opts)
}

// write builds the manifest, then streams the zip. Nothing is written to w
// until every file has been read and validated, so a rejected export does not
// leave a half-written archive behind.
func write(w io.Writer, kind Kind, opts ExportOptions) (*Manifest, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	type blob struct {
		data []byte
	}
	files := map[string]blob{}
	var order []string
	var items []Item
	var skipped []string

	add := func(zipPath string, data []byte) {
		files[zipPath] = blob{data: data}
		order = append(order, zipPath)
	}

	collect := func(dir, prefix string) ([]string, error) {
		var paths []string
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, relErr := filepath.Rel(dir, p)
			if relErr != nil {
				return relErr
			}
			if rel == "." {
				return nil
			}
			name := d.Name()
			zipRel := path.Join(prefix, filepath.ToSlash(rel))
			if d.IsDir() {
				if isDeniedDir(name) {
					skipped = append(skipped, zipRel+" (denied directory)")
					return fs.SkipDir
				}
				return nil
			}
			// A non-directory entry that is not a regular file is a symlink,
			// device or socket. WalkDir does not follow symlinks, so this is
			// where a linked credential file gets refused rather than copied.
			if d.Type()&os.ModeSymlink != 0 {
				skipped = append(skipped, zipRel+" (symbolic link)")
				return nil
			}
			if !d.Type().IsRegular() {
				skipped = append(skipped, zipRel+" (not a regular file)")
				return nil
			}
			if isDeniedName(name) {
				skipped = append(skipped, zipRel+" (denied file)")
				return nil
			}
			if !extAllowed(name) {
				skipped = append(skipped, zipRel+" (extension not allowed)")
				return nil
			}
			data, readErr := os.ReadFile(p)
			if readErr != nil {
				return readErr
			}
			if int64(len(data)) > MaxFileBytes {
				return fmt.Errorf("%w: %s is %d bytes (per-file limit %d)",
					ErrTooLarge, zipRel, len(data), MaxFileBytes)
			}
			data = maybeScrub(zipRel, data, opts.Scrub)
			add(zipRel, data)
			paths = append(paths, zipRel)
			return nil
		})
		return paths, err
	}

	collectFile := func(srcFile, name string) ([]string, error) {
		data, err := os.ReadFile(srcFile)
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > MaxFileBytes {
			return nil, fmt.Errorf("%w: %s is %d bytes (per-file limit %d)",
				ErrTooLarge, srcFile, len(data), MaxFileBytes)
		}
		zipPath := path.Join(name, skillFileName)
		add(zipPath, maybeScrub(zipPath, data, opts.Scrub))
		return []string{zipPath}, nil
	}

	for _, s := range opts.Skills {
		name, err := cleanItemName(s.Name)
		if err != nil {
			return nil, err
		}
		// A flat <name>.md source is normalised into the canonical layout, so an
		// imported skill always has the shape the loader prefers.
		if st, statErr := os.Stat(s.Dir); statErr == nil && !st.IsDir() {
			paths, err := collectFile(s.Dir, name)
			if err != nil {
				return nil, err
			}
			items = append(items, Item{Name: name, Description: describeSkill(s.Dir), Paths: paths})
			continue
		}
		if !isDir(s.Dir) {
			return nil, fmt.Errorf("%w: skill %q has no directory at %s", ErrUnsupported, name, s.Dir)
		}
		paths, err := collect(s.Dir, name)
		if err != nil {
			return nil, err
		}
		if len(paths) == 0 {
			skipped = append(skipped, name+" (no bundleable files)")
			continue
		}
		items = append(items, Item{Name: name, Description: describeSkill(s.Dir), Paths: paths})
	}

	for _, d := range opts.Docs {
		name := filepath.Base(strings.TrimSpace(d.Name))
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = filepath.Base(d.Path)
		}
		if isDeniedName(name) {
			return nil, fmt.Errorf("%w: %q must never travel in a bundle", ErrUnsafe, name)
		}
		if !extAllowed(name) {
			return nil, fmt.Errorf("%w: %q is not a bundleable document type", ErrUnsupported, name)
		}
		data, err := os.ReadFile(d.Path)
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > MaxFileBytes {
			return nil, fmt.Errorf("%w: %s is %d bytes (per-file limit %d)",
				ErrTooLarge, name, len(data), MaxFileBytes)
		}
		data = maybeScrub(name, data, opts.Scrub)
		add(name, data)
		items = append(items, Item{Name: name, Description: firstHeading(data), Paths: []string{name}})
	}

	sort.Strings(order)
	if len(order) == 0 {
		return nil, fmt.Errorf("%w: nothing to export", ErrUnsupported)
	}

	manifest := &Manifest{
		Kind:          kind,
		SchemaVersion: SchemaVersion,
		App:           AppName,
		ExportedAt:    now().UTC(),
		Name:          strings.TrimSpace(opts.Name),
		Description:   strings.TrimSpace(opts.Description),
		Items:         items,
		Skipped:       skipped,
	}
	for _, p := range order {
		sum := sha256.Sum256(files[p].data)
		manifest.Entries = append(manifest.Entries, Entry{
			Path:   p,
			Size:   int64(len(files[p].data)),
			SHA256: hex.EncodeToString(sum[:]),
		})
	}
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	if manifest.Description == "" {
		manifest.Description = describeKind(kind, len(items))
	}

	zw := zip.NewWriter(w)
	// The manifest goes first so a reader can stream the zip and learn what it
	// holds before reaching the payload.
	if err := writeEntry(zw, ManifestName, mustJSON(manifest), manifest.ExportedAt); err != nil {
		return nil, err
	}
	for _, p := range order {
		if err := writeEntry(zw, p, files[p].data, manifest.ExportedAt); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return manifest, nil
}

// writeEntry adds one file to the zip with a fixed, portable header.
func writeEntry(zw *zip.Writer, name string, data []byte, mod time.Time) error {
	hdr := &zip.FileHeader{
		Name:   name,
		Method: zip.Deflate,
		// A fixed timestamp keeps two exports of unchanged input byte-identical,
		// which makes the archive diffable and testable.
		Modified: mod,
	}
	// 0644: a bundle's contents are data, never something the OS should
	// execute on extraction.
	hdr.SetMode(0o644)
	f, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return err
}

// maybeScrub runs the optional text hook. Binary assets pass through untouched:
// running a regex pass over a PNG would corrupt it.
func maybeScrub(zipPath string, data []byte, scrub func(string) string) []byte {
	if scrub == nil || scrubModeFor(zipPath) != scrubText {
		return data
	}
	out := scrub(string(data))
	if out == string(data) {
		return data
	}
	return []byte(out)
}

// cleanItemName validates a caller-supplied item name.
func cleanItemName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", fmt.Errorf("%w: empty item name", ErrUnsafe)
	}
	if n != filepath.Base(n) || strings.ContainsAny(n, `/\`) || n == "." || n == ".." {
		return "", fmt.Errorf("%w: item name %q is not a single path element", ErrUnsafe, name)
	}
	if isDeniedName(n) {
		return "", fmt.Errorf("%w: item name %q is reserved", ErrUnsafe, name)
	}
	return n, nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// describeSkill pulls the `description:` line out of a skill's Markdown so a
// bundle can preview what it carries without opening every file. The argument
// is either the skill directory or the flat <name>.md file itself.
func describeSkill(p string) string {
	candidates := []string{p}
	if isDir(p) {
		candidates = []string{filepath.Join(p, skillFileName), filepath.Join(p, "README.md")}
	}
	for _, name := range candidates {
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		if d := frontmatterDescription(string(data)); d != "" {
			return d
		}
		if t := firstHeading(data); t != "" {
			return t
		}
	}
	return ""
}

// frontmatterDescription reads a `description:` value from a leading YAML
// frontmatter block, tolerating quoted values. It intentionally does not parse
// TOML/JSON — those are reported by firstHeading instead.
func frontmatterDescription(body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for _, line := range lines[1:] {
		t := strings.TrimSpace(line)
		if t == "---" {
			break
		}
		if !strings.HasPrefix(t, "description:") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(t, "description:"))
		v = strings.Trim(v, `"'`)
		if len([]rune(v)) > 160 {
			v = string([]rune(v)[:159]) + "…"
		}
		return v
	}
	return ""
}

// firstHeading returns a document's first Markdown heading, used as the item
// description for memory bundles.
func firstHeading(data []byte) string {
	body := string(data)
	if d := frontmatterDescription(body); d != "" {
		return d
	}
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			title := strings.TrimSpace(strings.TrimLeft(t, "#"))
			if title != "" {
				if len([]rune(title)) > 160 {
					title = string([]rune(title)[:159]) + "…"
				}
				return title
			}
		}
	}
	return ""
}

func describeKind(k Kind, n int) string {
	switch k {
	case KindSkill, KindSkills:
		return fmt.Sprintf("hiq 技能包（%d 个技能）", n)
	case KindMemory:
		return fmt.Sprintf("hiq 记忆包（%d 份文档）", n)
	default:
		return "hiq 便携包"
	}
}

func mustJSON(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Manifest is a plain struct of strings and ints; marshalling cannot
		// fail. A panic here would be a programming error, not user input.
		panic(fmt.Sprintf("portable: marshal manifest: %v", err))
	}
	return append(b, '\n')
}
