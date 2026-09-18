package portable

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedNow keeps a bundle byte-reproducible across runs.
func fixedNow() time.Time { return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC) }

// writeSkill lays out a canonical skill directory and returns its path.
func writeSkill(t *testing.T, root, name string, extra map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: does " + name + " things\n---\n\n# " + name + "\n\nSteps.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	for rel, content := range extra {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return dir
}

func exportToBytes(t *testing.T, kind Kind, opts ExportOptions) []byte {
	t.Helper()
	var buf bytes.Buffer
	var (
		m   *Manifest
		err error
	)
	switch kind {
	case KindMemory:
		m, err = ExportMemory(&buf, opts)
	default:
		m, err = ExportSkills(&buf, opts)
	}
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if m == nil {
		t.Fatal("export returned a nil manifest")
	}
	return buf.Bytes()
}

func importDir(t *testing.T, bundle []byte, dest string, opts ImportOptions) (*ImportResult, error) {
	t.Helper()
	opts.DestDir = dest
	return Import(bytes.NewReader(bundle), int64(len(bundle)), opts)
}

// TestExportImportRoundTrip is the base contract: what goes in comes out, byte
// for byte, in the canonical layout.
func TestExportImportRoundTrip(t *testing.T) {
	src := t.TempDir()
	skillDir := writeSkill(t, src, "alpha", map[string]string{
		"assets/logo.png":        "\x89PNG fake",
		"references/notes.md":    "notes\n",
		"scripts/run.sh":         "echo hi\n",
		"config.toml":            "[should]\nnot = 'travel'\n",
		".env":                   "SECRET=1\n",
		"helpers/tool.exe":       "MZ binary",
		"secrets.prod.json":      `{"k":"v"}`,
		"nested/deeper/note.txt": "deep\n",
	})

	bundle := exportToBytes(t, KindSkill, ExportOptions{
		Skills: []SkillSource{{Name: "alpha", Dir: skillDir}},
		Name:   "alpha",
		Scrub:  nil,
		Now:    fixedNow,
	})

	// The manifest must not list anything that must never travel.
	m, err := Inspect(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if m.Kind != KindSkill || m.SchemaVersion != SchemaVersion || m.App != AppName {
		t.Fatalf("unexpected manifest header: %+v", m)
	}
	for _, e := range m.Entries {
		for _, bad := range []string{"config.toml", ".env", "secrets.prod.json", "tool.exe"} {
			if strings.Contains(e.Path, bad) {
				t.Errorf("%q must not be bundled", e.Path)
			}
		}
	}
	// ...and the omissions must be reported, not silent.
	joined := strings.Join(m.Skipped, "\n")
	for _, want := range []string{"config.toml", ".env", "tool.exe", "secrets.prod.json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("skipped list should mention %q, got:\n%s", want, joined)
		}
	}
	if m.Items[0].Description != "does alpha things" {
		t.Errorf("item description = %q", m.Items[0].Description)
	}

	dest := t.TempDir()
	res, err := importDir(t, bundle, dest, ImportOptions{Now: fixedNow})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].FinalName != "alpha" || res.Items[0].Renamed {
		t.Fatalf("unexpected import result: %+v", res.Items)
	}
	got, err := os.ReadFile(filepath.Join(dest, "alpha", "SKILL.md"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(got), "name: alpha") {
		t.Errorf("SKILL.md lost its frontmatter:\n%s", got)
	}
	for _, rel := range []string{"assets/logo.png", "references/notes.md", "scripts/run.sh", "nested/deeper/note.txt"} {
		if _, err := os.Stat(filepath.Join(dest, "alpha", filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s did not survive the round trip: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "alpha", "config.toml")); !os.IsNotExist(err) {
		t.Errorf("config.toml must not be extracted, stat err = %v", err)
	}
}

// A flat <name>.md skill is normalised into the canonical directory layout, so
// the imported shape does not depend on how the source happened to be stored.
func TestExportFlatSkillNormalisesLayout(t *testing.T) {
	src := t.TempDir()
	flat := filepath.Join(src, "flat.md")
	if err := os.WriteFile(flat, []byte("---\nname: flat\ndescription: a flat skill\n---\n\nBody\n"), 0o644); err != nil {
		t.Fatalf("write flat skill: %v", err)
	}
	bundle := exportToBytes(t, KindSkill, ExportOptions{
		Skills: []SkillSource{{Name: "flat", Dir: flat}},
		Now:    fixedNow,
	})
	dest := t.TempDir()
	if _, err := importDir(t, bundle, dest, ImportOptions{Now: fixedNow}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "flat", "SKILL.md")); err != nil {
		t.Fatalf("flat skill should land as <name>/SKILL.md: %v", err)
	}
}

// A second import of the same skill must keep both, and must rewrite the
// renamed copy's frontmatter name so the two do not collide inside fairpeer.
func TestImportRenamesOnConflictAndPatchesFrontmatter(t *testing.T) {
	src := t.TempDir()
	dir := writeSkill(t, src, "alpha", nil)
	bundle := exportToBytes(t, KindSkill, ExportOptions{
		Skills: []SkillSource{{Name: "alpha", Dir: dir}},
		Now:    fixedNow,
	})
	dest := t.TempDir()

	if _, err := importDir(t, bundle, dest, ImportOptions{Now: fixedNow}); err != nil {
		t.Fatalf("first import: %v", err)
	}
	res, err := importDir(t, bundle, dest, ImportOptions{Now: fixedNow})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].FinalName != "alpha-2" || !res.Items[0].Renamed {
		t.Fatalf("second import should land as alpha-2: %+v", res.Items)
	}
	body, err := os.ReadFile(filepath.Join(dest, "alpha-2", "SKILL.md"))
	if err != nil {
		t.Fatalf("read renamed skill: %v", err)
	}
	if !strings.Contains(string(body), "name: alpha-2") {
		t.Errorf("frontmatter name must follow the directory rename:\n%s", body)
	}
	if strings.Contains(string(body), "name: alpha\n") {
		t.Errorf("the old name survived the rename:\n%s", body)
	}
	// The original is untouched.
	orig, _ := os.ReadFile(filepath.Join(dest, "alpha", "SKILL.md"))
	if !strings.Contains(string(orig), "name: alpha") {
		t.Errorf("the existing skill was modified:\n%s", orig)
	}
}

// Overwrite replaces in place when the caller explicitly asks for it.
func TestImportOverwriteReplacesInPlace(t *testing.T) {
	src := t.TempDir()
	dir := writeSkill(t, src, "alpha", nil)
	bundle := exportToBytes(t, KindSkill, ExportOptions{Skills: []SkillSource{{Name: "alpha", Dir: dir}}, Now: fixedNow})
	dest := t.TempDir()
	if _, err := importDir(t, bundle, dest, ImportOptions{Now: fixedNow}); err != nil {
		t.Fatalf("first import: %v", err)
	}
	res, err := importDir(t, bundle, dest, ImportOptions{Overwrite: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("overwriting import: %v", err)
	}
	if res.Items[0].Renamed || res.Items[0].FinalName != "alpha" {
		t.Fatalf("overwrite should keep the name: %+v", res.Items[0])
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("overwrite left %d entries, want 1", len(entries))
	}
}

// buildZip writes a zip by hand so a test can express a bundle an export would
// never produce.
func buildZip(t *testing.T, m *Manifest, payload map[string][]byte, symlinks map[string]string) []byte {
	t.Helper()
	if m != nil {
		m.Entries = nil
		for p, data := range payload {
			sum := sha256.Sum256(data)
			m.Entries = append(m.Entries, Entry{Path: p, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
		}
		sortEntries(m.Entries)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if m != nil {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal manifest: %v", err)
		}
		w, _ := zw.Create(ManifestName)
		if _, err := w.Write(raw); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
	}
	for p, data := range payload {
		w, _ := zw.Create(p)
		if _, err := w.Write(data); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	for p, target := range symlinks {
		hdr := &zip.FileHeader{Name: p, Method: zip.Deflate}
		hdr.SetMode(os.ModeSymlink | 0o777)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("create symlink entry: %v", err)
		}
		if _, err := w.Write([]byte(target)); err != nil {
			t.Fatalf("write symlink entry: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func sortEntries(es []Entry) {
	for i := 1; i < len(es); i++ {
		for j := i; j > 0 && es[j].Path < es[j-1].Path; j-- {
			es[j], es[j-1] = es[j-1], es[j]
		}
	}
}

// TestImportRejectsPathEscape is the zip-slip contract. A relative path that
// climbs out of the destination is the classic archive attack, and the
// manifest is the natural place to try it.
func TestImportRejectsPathEscape(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"parent", "../evil.md"},
		{"nested parent", "alpha/../../evil.md"},
		{"absolute", "/etc/passwd"},
		{"drive letter", `C:/Windows/evil.md`},
		{"unc-ish", "//server/share/evil.md"},
		{"backslash", `alpha\evil.md`},
		{"too deep", strings.Repeat("a/", MaxPathDepth+1) + "evil.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manifest{Kind: KindSkill, SchemaVersion: SchemaVersion, App: AppName}
			bundle := buildZip(t, m, map[string][]byte{tc.path: []byte("pwned")}, nil)
			dest := t.TempDir()
			_, err := importDir(t, bundle, dest, ImportOptions{})
			if !errors.Is(err, ErrUnsafe) {
				t.Fatalf("path %q: err = %v, want ErrUnsafe", tc.path, err)
			}
			// Nothing may have been created outside the destination.
			parent, _ := os.ReadDir(filepath.Dir(dest))
			for _, e := range parent {
				if e.Name() == "evil.md" {
					t.Fatalf("path %q escaped into %s", tc.path, filepath.Dir(dest))
				}
			}
		})
	}
}

// A secret file must be refused even when it arrives from outside, because the
// export-side check only protects bundles this build produced.
func TestImportRejectsCredentialFiles(t *testing.T) {
	for _, p := range []string{"alpha/config.toml", "alpha/secrets.enc.json", "alpha/.env", "alpha/id_rsa"} {
		t.Run(p, func(t *testing.T) {
			m := &Manifest{Kind: KindSkill, SchemaVersion: SchemaVersion, App: AppName}
			bundle := buildZip(t, m, map[string][]byte{p: []byte("x")}, nil)
			_, err := importDir(t, bundle, t.TempDir(), ImportOptions{})
			if !errors.Is(err, ErrUnsafe) {
				t.Fatalf("%q: err = %v, want ErrUnsafe", p, err)
			}
		})
	}
}

// A symlink entry is refused outright: its extraction semantics differ by
// platform and it is the standard way to make an archive write outside itself.
func TestImportRejectsSymlinkEntries(t *testing.T) {
	m := &Manifest{Kind: KindSkill, SchemaVersion: SchemaVersion, App: AppName,
		Entries: []Entry{{Path: "alpha/SKILL.md", Size: 1, SHA256: "00"}}}
	bundle := buildZip(t, m, map[string][]byte{"alpha/SKILL.md": []byte("x")},
		map[string]string{"alpha/link": "/etc/passwd"})
	_, err := importDir(t, bundle, t.TempDir(), ImportOptions{})
	if !errors.Is(err, ErrUnsafe) {
		t.Fatalf("err = %v, want ErrUnsafe", err)
	}
}

// A payload that does not match the digest it was exported with is rejected —
// that is what catches a truncated download.
func TestImportDetectsTampering(t *testing.T) {
	src := t.TempDir()
	dir := writeSkill(t, src, "alpha", nil)
	bundle := exportToBytes(t, KindSkill, ExportOptions{Skills: []SkillSource{{Name: "alpha", Dir: dir}}, Now: fixedNow})

	// Rebuild the zip with the same manifest but different bytes for the body.
	m, err := Inspect(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	payload := map[string][]byte{}
	for _, f := range zr.File {
		if f.Name == ManifestName {
			continue
		}
		payload[f.Name] = []byte("tampered")
	}
	// Keep the original manifest digests: now the bytes disagree with them.
	tampered := buildZip(t, nil, payload, nil)
	tampered = injectManifest(t, tampered, m)

	_, err = importDir(t, tampered, t.TempDir(), ImportOptions{})
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

// injectManifest replaces a hand-built zip's manifest with a given one, so a
// test can hold the digests fixed while changing the payload.
func injectManifest(t *testing.T, bundle []byte, m *Manifest) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	payload := map[string][]byte{}
	for _, f := range zr.File {
		if f.Name == ManifestName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open entry: %v", err)
		}
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read entry: %v", err)
		}
		_ = rc.Close()
		payload[f.Name] = data
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(ManifestName)
	if _, err := w.Write(raw); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	for p, data := range payload {
		w, _ := zw.Create(p)
		if _, err := w.Write(data); err != nil {
			t.Fatalf("write payload: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

// A bundle from a newer build is refused rather than half-understood.
func TestImportRefusesUnknownOrNewerBundles(t *testing.T) {
	payload := map[string][]byte{"alpha/SKILL.md": []byte("x")}
	cases := []struct {
		name string
		m    *Manifest
	}{
		{"unknown kind", &Manifest{Kind: Kind("fairpeer.future"), SchemaVersion: 1, App: AppName}},
		{"newer schema", &Manifest{Kind: KindSkill, SchemaVersion: SchemaVersion + 1, App: AppName}},
		{"no schema", &Manifest{Kind: KindSkill, App: AppName}},
		{"foreign app", &Manifest{Kind: KindSkill, SchemaVersion: 1, App: "somethingelse"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle := buildZip(t, tc.m, payload, nil)
			_, err := importDir(t, bundle, t.TempDir(), ImportOptions{})
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("err = %v, want ErrUnsupported", err)
			}
		})
	}
	// A manifest that lists nothing is corrupt, not "an empty bundle".
	t.Run("no entries", func(t *testing.T) {
		bundle := buildZip(t, &Manifest{Kind: KindSkill, SchemaVersion: 1, App: AppName}, map[string][]byte{}, nil)
		_, err := importDir(t, bundle, t.TempDir(), ImportOptions{})
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("err = %v, want ErrCorrupt", err)
		}
	})
}

// A zip with no manifest cannot be imported: the manifest is what makes a
// bundle self-describing.
func TestImportRequiresManifest(t *testing.T) {
	bundle := buildZip(t, nil, map[string][]byte{"alpha/SKILL.md": []byte("x")}, nil)
	_, err := importDir(t, bundle, t.TempDir(), ImportOptions{})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// Payload the manifest does not list is never written, but it is reported so a
// hand-edited archive is visible rather than mysterious.
func TestImportIgnoresAndReportsUnlistedFiles(t *testing.T) {
	src := t.TempDir()
	dir := writeSkill(t, src, "alpha", nil)
	bundle := exportToBytes(t, KindSkill, ExportOptions{Skills: []SkillSource{{Name: "alpha", Dir: dir}}, Now: fixedNow})
	m, err := Inspect(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Re-export with one extra file that the manifest will not mention by
	// rebuilding the zip from the original payload plus an intruder.
	payload := map[string][]byte{}
	for _, f := range zr.File {
		if f.Name == ManifestName {
			continue
		}
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		payload[f.Name] = data
	}
	payload["alpha/intruder.md"] = []byte("not listed")
	withExtra := buildZip(t, m, payload, nil)
	// buildZip recomputes the manifest from the payload, so strip the intruder
	// back out of the manifest to model a hand-edited archive.
	m2 := *m
	m2.Entries = nil
	for _, e := range m.Entries {
		if e.Path == "alpha/intruder.md" {
			continue
		}
		m2.Entries = append(m2.Entries, e)
	}
	withExtra = injectManifest(t, withExtra, &m2)

	dest := t.TempDir()
	res, err := importDir(t, withExtra, dest, ImportOptions{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "alpha", "intruder.md")); !os.IsNotExist(err) {
		t.Errorf("an unlisted file must not be written, stat err = %v", err)
	}
	if !strings.Contains(strings.Join(res.Skipped, "\n"), "intruder.md") {
		t.Errorf("the unlisted file should be reported, got %v", res.Skipped)
	}
}

// A truncated bundle must be rejected by the reader, not unpacked partially.
func TestImportRejectsTruncatedArchive(t *testing.T) {
	src := t.TempDir()
	dir := writeSkill(t, src, "alpha", nil)
	bundle := exportToBytes(t, KindSkill, ExportOptions{Skills: []SkillSource{{Name: "alpha", Dir: dir}}, Now: fixedNow})
	cut := bundle[:len(bundle)/2]
	_, err := importDir(t, cut, t.TempDir(), ImportOptions{})
	if err == nil {
		t.Fatal("a truncated bundle should not import")
	}
}

// The scrub hook runs on export and on import, so a note written on one machine
// does not carry a key to another.
func TestScrubAppliesToTextEntriesOnly(t *testing.T) {
	src := t.TempDir()
	dir := writeSkill(t, src, "alpha", map[string]string{
		"references/notes.md": "token sk-live-abcdefghijklmnop here\n",
		"assets/blob.bin":     "sk-live-abcdefghijklmnop\n",
	})
	// assets/blob.bin is skipped by the extension whitelist, so use a .png to
	// prove binary content is untouched on the way through.
	if err := os.WriteFile(filepath.Join(dir, "assets", "logo.png"), []byte("sk-live-abcdefghijklmnop"), 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
	scrub := func(s string) string {
		return strings.ReplaceAll(s, "sk-live-abcdefghijklmnop", "[redacted]")
	}
	bundle := exportToBytes(t, KindSkill, ExportOptions{
		Skills: []SkillSource{{Name: "alpha", Dir: dir}},
		Scrub:  scrub,
		Now:    fixedNow,
	})
	dest := t.TempDir()
	if _, err := importDir(t, bundle, dest, ImportOptions{Now: fixedNow}); err != nil {
		t.Fatalf("import: %v", err)
	}
	notes, _ := os.ReadFile(filepath.Join(dest, "alpha", "references", "notes.md"))
	if strings.Contains(string(notes), "sk-live-") {
		t.Errorf("the text entry should have been scrubbed on export:\n%s", notes)
	}
	png, _ := os.ReadFile(filepath.Join(dest, "alpha", "assets", "logo.png"))
	if !strings.Contains(string(png), "sk-live-") {
		t.Errorf("a binary asset must pass through untouched, got %q", png)
	}
}

// A memory bundle carries documents and never a session transcript.
func TestExportMemoryCarriesDocsOnly(t *testing.T) {
	src := t.TempDir()
	doc := filepath.Join(src, "fairpeer.md")
	if err := os.WriteFile(doc, []byte("# Project memory\n\nFacts.\n"), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	bundle := exportToBytes(t, KindMemory, ExportOptions{
		Docs: []DocSource{{Name: "fairpeer.md", Path: doc}},
		Name: "memory",
		Now:  fixedNow,
	})
	m, err := Inspect(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if m.Kind != KindMemory {
		t.Fatalf("kind = %q", m.Kind)
	}
	if len(m.Entries) != 1 || m.Entries[0].Path != "fairpeer.md" {
		t.Fatalf("unexpected entries: %+v", m.Entries)
	}
	if m.Items[0].Description != "Project memory" {
		t.Errorf("description = %q, want the first heading", m.Items[0].Description)
	}
	dest := t.TempDir()
	if _, err := importDir(t, bundle, dest, ImportOptions{Now: fixedNow}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "fairpeer.md")); err != nil {
		t.Fatalf("doc did not land: %v", err)
	}
}

// Exporting a credentials file directly must fail loudly rather than ship it.
func TestExportRefusesCredentialDoc(t *testing.T) {
	src := t.TempDir()
	secret := filepath.Join(src, "secrets.enc.json")
	if err := os.WriteFile(secret, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	var buf bytes.Buffer
	if _, err := ExportMemory(&buf, ExportOptions{Docs: []DocSource{{Name: "secrets.enc.json", Path: secret}}, Now: fixedNow}); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("err = %v, want ErrUnsafe", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a refused export must write nothing, got %d bytes", buf.Len())
	}
}

// An item name that is not a single path element is refused: it would let a
// caller decide where inside the destination the item lands.
func TestExportRefusesUnsafeItemName(t *testing.T) {
	src := t.TempDir()
	dir := writeSkill(t, src, "alpha", nil)
	for _, bad := range []string{"../escape", "a/b", `a\b`, "", ".."} {
		var buf bytes.Buffer
		_, err := ExportSkills(&buf, ExportOptions{Skills: []SkillSource{{Name: bad, Dir: dir}}, Now: fixedNow})
		if !errors.Is(err, ErrUnsafe) {
			t.Errorf("name %q: err = %v, want ErrUnsafe", bad, err)
		}
	}
}

// A single oversized file is refused before anything is written.
func TestExportEnforcesSizeCeiling(t *testing.T) {
	src := t.TempDir()
	big := strings.Repeat("x", MaxFileBytes+1)
	dir := writeSkill(t, src, "big", map[string]string{"references/huge.md": big})
	var buf bytes.Buffer
	_, err := ExportSkills(&buf, ExportOptions{Skills: []SkillSource{{Name: "big", Dir: dir}}, Now: fixedNow})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a refused export must write nothing, got %d bytes", buf.Len())
	}
}

// A symlinked file inside a skill is skipped, not followed: following it would
// copy whatever it points at (a key, a socket, a network path).
func TestExportSkipsSymlinks(t *testing.T) {
	if testing.Short() {
		t.Skip("symlink creation is unavailable in some sandboxes")
	}
	src := t.TempDir()
	outside := filepath.Join(src, "outside-secret.md")
	if err := os.WriteFile(outside, []byte("do not ship me"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	dir := writeSkill(t, src, "alpha", map[string]string{"references/keep.md": "keep\n"})
	link := filepath.Join(dir, "references", "linked.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	bundle := exportToBytes(t, KindSkill, ExportOptions{Skills: []SkillSource{{Name: "alpha", Dir: dir}}, Now: fixedNow})
	m, err := Inspect(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	for _, e := range m.Entries {
		if strings.Contains(e.Path, "linked.md") {
			t.Fatalf("a symlink must not be bundled: %+v", e)
		}
	}
	if !strings.Contains(strings.Join(m.Skipped, "\n"), "linked.md") {
		t.Errorf("the skipped symlink should be reported, got %v", m.Skipped)
	}
}

// The export is reproducible: same inputs, same clock, same bytes.
func TestExportIsReproducible(t *testing.T) {
	src := t.TempDir()
	dir := writeSkill(t, src, "alpha", map[string]string{"references/n.md": "n\n"})
	first := exportToBytes(t, KindSkill, ExportOptions{Skills: []SkillSource{{Name: "alpha", Dir: dir}}, Now: fixedNow})
	second := exportToBytes(t, KindSkill, ExportOptions{Skills: []SkillSource{{Name: "alpha", Dir: dir}}, Now: fixedNow})
	if !bytes.Equal(first, second) {
		t.Fatal("two exports of identical input differ")
	}
}

// Exporting nothing is a caller error, not an empty bundle.
func TestExportRequiresInput(t *testing.T) {
	var buf bytes.Buffer
	if _, err := ExportSkills(&buf, ExportOptions{Now: fixedNow}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if _, err := ExportMemory(&buf, ExportOptions{Now: fixedNow}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// Inspect must not write anything, so a UI can preview before committing.
func TestInspectWritesNothing(t *testing.T) {
	src := t.TempDir()
	dir := writeSkill(t, src, "alpha", nil)
	bundle := exportToBytes(t, KindSkill, ExportOptions{Skills: []SkillSource{{Name: "alpha", Dir: dir}}, Now: fixedNow})
	dest := t.TempDir()
	if _, err := Inspect(bytes.NewReader(bundle), int64(len(bundle))); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("inspect wrote %d entries", len(entries))
	}
}
