package portable

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// ImportOptions configures an import.
type ImportOptions struct {
	// DestDir is the directory the bundle's items are placed under — the
	// skills root for a skill bundle, the memory root for a memory bundle.
	DestDir string
	// Overwrite allows replacing an existing item of the same name. The default
	// (false) keeps both: the incoming item is renamed with a -2/-3 suffix.
	// Keeping both is the default because an import is normally "add what I
	// have on the other machine", and silently replacing a skill the user has
	// since edited is data loss.
	Overwrite bool
	// Scrub, when non-nil, rewrites each text entry on the way in. A bundle can
	// come from anywhere, so the same PII pass that protects the export runs
	// here too.
	Scrub func(string) string
	// Now overrides the clock (tests).
	Now func() time.Time
}

// ImportedItem reports one item that landed on disk.
type ImportedItem struct {
	// Name is the name recorded in the bundle.
	Name string
	// FinalName is the name actually used — equal to Name unless a conflict
	// forced a suffix.
	FinalName string
	// Renamed is FinalName != Name.
	Renamed bool
	// Description is the bundle's one-liner for the item.
	Description string
	// Files are the paths written, relative to DestDir.
	Files []string
}

// ImportResult summarises what an import did, so the UI can report it.
type ImportResult struct {
	Kind    Kind
	Name    string
	Items   []ImportedItem
	Skipped []string
	Bytes   int64
	// DestDir is where the items were written.
	DestDir string
}

// Import reads a bundle and writes its items under opts.DestDir.
//
// The order of operations matters: the manifest is read and validated, then the
// destination name is resolved, then bytes are written with O_EXCL so nothing
// is ever clobbered by accident. A failure part-way removes what it wrote.
func Import(r io.ReaderAt, size int64, opts ImportOptions) (*ImportResult, error) {
	dest := strings.TrimSpace(opts.DestDir)
	if dest == "" {
		return nil, fmt.Errorf("%w: no destination directory", ErrUnsupported)
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}

	manifest, index, err := readManifest(zr)
	if err != nil {
		return nil, err
	}
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}

	res := &ImportResult{Kind: manifest.Kind, Name: manifest.Name, DestDir: dest}
	// Anything in the zip the manifest does not list is not written; it is
	// reported so a hand-edited archive is visible rather than mysterious.
	// The comparison is against the manifest, not against the zip's own index —
	// the index holds every entry, so it can never show what was left out.
	listed := make(map[string]bool, len(manifest.Entries))
	for _, e := range manifest.Entries {
		listed[e.Path] = true
	}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		clean := path.Clean(f.Name)
		if clean == ManifestName || listed[clean] {
			continue
		}
		res.Skipped = append(res.Skipped, clean+" (not listed in the manifest)")
	}

	// Group entries by their item (the first path segment for a skill, the file
	// name for a doc) so a rename moves the whole item at once.
	grouped := map[string][]Entry{}
	var groupOrder []string
	for _, e := range manifest.Entries {
		top := topSegment(e.Path, manifest.Kind)
		if _, ok := grouped[top]; !ok {
			groupOrder = append(groupOrder, top)
		}
		grouped[top] = append(grouped[top], e)
	}

	descOf := map[string]string{}
	for _, it := range manifest.Items {
		descOf[it.Name] = it.Description
	}

	var written []string
	cleanup := func() {
		// Best-effort rollback: remove the files this import created, then any
		// directory it left empty. Never remove dest itself.
		for i := len(written) - 1; i >= 0; i-- {
			_ = os.Remove(written[i])
			pruneEmptyDirs(filepath.Dir(written[i]), dest)
		}
	}

	for _, top := range groupOrder {
		entries := grouped[top]
		finalName, renamed, err := resolveUniqueName(dest, top, manifest.Kind, opts.Overwrite)
		if err != nil {
			cleanup()
			return nil, err
		}
		item := ImportedItem{
			Name:        top,
			FinalName:   finalName,
			Renamed:     renamed,
			Description: descOf[top],
		}
		for _, e := range entries {
			// Re-derive the path under the (possibly renamed) item.
			sub := strings.TrimPrefix(e.Path, top)
			sub = strings.TrimPrefix(sub, "/")
			finalRel := finalName
			if sub != "" {
				finalRel = path.Join(finalName, sub)
			}

			data, err := readVerified(index[e.Path], e, opts.Scrub)
			if err != nil {
				cleanup()
				return nil, err
			}

			target := filepath.Join(dest, filepath.FromSlash(finalRel))
			if err := containedIn(dest, target); err != nil {
				cleanup()
				return nil, err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				cleanup()
				return nil, err
			}
			// The default path uses O_EXCL — the no-clobber guarantee: if a file
			// appeared between the name resolution above and here, this fails
			// instead of overwriting. An explicit Overwrite replaces in place.
			flags := os.O_WRONLY | os.O_CREATE
			if opts.Overwrite {
				flags |= os.O_TRUNC
			} else {
				flags |= os.O_EXCL
			}
			fh, err := os.OpenFile(target, flags, 0o644)
			if err != nil {
				if os.IsExist(err) && !opts.Overwrite {
					cleanup()
					return nil, fmt.Errorf("%w: %s appeared while importing; re-run to pick a free name",
						ErrUnsafe, finalRel)
				}
				cleanup()
				return nil, err
			}
			if _, err := fh.Write(data); err != nil {
				_ = fh.Close()
				cleanup()
				return nil, err
			}
			if err := fh.Close(); err != nil {
				cleanup()
				return nil, err
			}
			written = append(written, target)
			res.Bytes += int64(len(data))
			item.Files = append(item.Files, finalRel)
		}
		// A skill's `name:` frontmatter overrides its directory name, so a
		// renamed skill would otherwise still load under the OLD name and
		// collide with the very skill it was renamed to avoid. Patching keeps
		// the folder and the identifier in step.
		if renamed && manifest.Kind != KindMemory {
			if _, err := patchSkillName(filepath.Join(dest, finalName, skillFileName), finalName); err != nil {
				cleanup()
				return nil, err
			}
		}
		res.Items = append(res.Items, item)
	}
	return res, nil
}

// readManifest locates and parses the manifest, and returns an index of the
// zip's file entries by cleaned name.
func readManifest(zr *zip.Reader) (*Manifest, map[string]*zip.File, error) {
	index := make(map[string]*zip.File, len(zr.File))
	var mf *zip.File
	for _, f := range zr.File {
		clean := path.Clean(f.Name)
		if clean == ManifestName {
			if mf != nil {
				return nil, nil, fmt.Errorf("%w: bundle has two manifests", ErrCorrupt)
			}
			mf = f
			continue
		}
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		if !f.Mode().IsRegular() {
			// A zip can record a symlink or a device node. Refusing the whole
			// bundle is right here: extraction semantics for those differ by
			// platform and a bundle has no legitimate need for one.
			return nil, nil, fmt.Errorf("%w: %q is not a regular file", ErrUnsafe, f.Name)
		}
		index[clean] = f
	}
	if mf == nil {
		return nil, nil, fmt.Errorf("%w: no %s in the archive", ErrUnsupported, ManifestName)
	}
	if mf.UncompressedSize64 > 1<<20 {
		return nil, nil, fmt.Errorf("%w: manifest is implausibly large", ErrCorrupt)
	}
	rc, err := mf.Open()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, fmt.Errorf("%w: manifest is not valid JSON: %v", ErrCorrupt, err)
	}
	return &m, index, nil
}

// readVerified reads one entry and checks it against the manifest: the declared
// size must match, the decompressed bytes must match the declared size, and the
// digest must match. Together those reject a truncated download and a zip whose
// payload was swapped after export.
func readVerified(f *zip.File, e Entry, scrub func(string) string) ([]byte, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: %q is recorded but missing from the archive", ErrCorrupt, e.Path)
	}
	if int64(f.UncompressedSize64) != e.Size {
		return nil, fmt.Errorf("%w: %q declares %d bytes, manifest says %d",
			ErrCorrupt, e.Path, f.UncompressedSize64, e.Size)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	defer rc.Close()
	// One byte past the declared size is enough to detect a lying header
	// without ever materialising a decompression bomb.
	data, err := io.ReadAll(io.LimitReader(rc, e.Size+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if int64(len(data)) != e.Size {
		return nil, fmt.Errorf("%w: %q decompressed to %d bytes, expected %d",
			ErrCorrupt, e.Path, len(data), e.Size)
	}
	if e.SHA256 != "" {
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, e.SHA256) {
			return nil, fmt.Errorf("%w: %q does not match its recorded digest", ErrCorrupt, e.Path)
		}
	}
	base := path.Base(e.Path)
	if isDeniedName(base) {
		return nil, fmt.Errorf("%w: %q must never be imported from a bundle", ErrUnsafe, e.Path)
	}
	if scrub != nil && scrubModeFor(base) == scrubText {
		if out := scrub(string(data)); out != string(data) {
			data = []byte(out)
		}
	}
	return data, nil
}

// patchSkillName rewrites a skill file's `name:` frontmatter to newName.
//
// It reports false when there is nothing to do: a skill with no `name:` line is
// identified by its directory alone, so the rename already took effect and
// inventing a frontmatter block would be a change the author did not ask for.
func patchSkillName(file, newName string) (bool, error) {
	if !validSkillName(newName) {
		return false, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return false, nil
	}
	patched := false
	for i := 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "---" {
			break
		}
		if !strings.HasPrefix(t, "name:") {
			continue
		}
		cur := strings.Trim(strings.TrimSpace(strings.TrimPrefix(t, "name:")), `"'`)
		if cur == newName {
			return false, nil
		}
		lines[i] = "name: " + newName
		patched = true
		break
	}
	if !patched {
		return false, nil
	}
	st, err := os.Stat(file)
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(file, []byte(strings.Join(lines, "\n")), st.Mode().Perm())
}

// validSkillName mirrors internal/skill.IsValidName: the identifier travels into
// frontmatter, so it must not carry characters the loader would reject.
func validSkillName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// topSegment returns the manifest path segment that identifies an item.
func topSegment(p string, kind Kind) string {
	if kind == KindMemory {
		return path.Base(p)
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

// resolveUniqueName picks the name an item lands under, appending -2, -3, … when
// the name is taken. A hash tail is the last resort, so the loop is bounded.
func resolveUniqueName(dest, name string, kind Kind, overwrite bool) (string, bool, error) {
	if _, err := cleanItemName(name); err != nil && kind != KindMemory {
		return "", false, err
	}
	exists := func(n string) bool {
		_, err := os.Lstat(filepath.Join(dest, n))
		return err == nil
	}
	if overwrite || !exists(name) {
		return name, false, nil
	}
	for i := 2; i <= 99; i++ {
		cand := fmt.Sprintf("%s-%d", name, i)
		if !exists(cand) {
			return cand, true, nil
		}
	}
	// Saturated the readable suffixes: fall back to a content-free nonce so the
	// import still succeeds rather than looping.
	cand := fmt.Sprintf("%s-%s", name, time.Now().UTC().Format("20060102150405"))
	if exists(cand) {
		return "", false, fmt.Errorf("%w: cannot find a free name for %q", ErrUnsafe, name)
	}
	return cand, true, nil
}

// containedIn verifies target stays inside base after path resolution. Clean
// paths can still escape once joined (a junction, a symlinked parent), so this
// is checked on the final absolute pair rather than on the textual path alone.
func containedIn(base, target string) error {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return fmt.Errorf("%w: %q is not inside the destination", ErrUnsafe, target)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%w: %q escapes the destination", ErrUnsafe, target)
	}
	// A symlinked parent inside the destination would let the write land
	// elsewhere. Walk the existing prefix and refuse any link.
	for dir := filepath.Dir(absTarget); len(dir) >= len(absBase); dir = filepath.Dir(dir) {
		st, err := os.Lstat(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if dir == absBase {
					break
				}
				continue
			}
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %q is reached through a symbolic link", ErrUnsafe, target)
		}
		if dir == absBase {
			break
		}
	}
	return nil
}

// pruneEmptyDirs removes empty directories from dir upwards, stopping at base.
func pruneEmptyDirs(dir, base string) {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return
	}
	for {
		absDir, err := filepath.Abs(dir)
		if err != nil || absDir == absBase || len(absDir) < len(absBase) {
			return
		}
		if err := os.Remove(absDir); err != nil {
			return
		}
		dir = filepath.Dir(absDir)
	}
}

// Inspect reads a bundle's manifest without writing anything, so a UI can show
// what an import would do before the user commits.
func Inspect(r io.ReaderAt, size int64) (*Manifest, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	m, _, err := readManifest(zr)
	if err != nil {
		return nil, err
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// Bytes is a convenience for callers that already hold a bundle in memory.
func Bytes(b []byte) io.ReaderAt { return bytes.NewReader(b) }
