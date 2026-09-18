package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The redaction funnel (see scrub.go). A memory file is plain text in a
// directory the user is told to copy, so a secret that lands there is inherited
// by every later reader — a teammate, a backup, an exported bundle.

// TestSaveMasksSecretsInBodyAndIndex is the primary contract: a key the model
// wrote into a fact never reaches the file or the index that points at it.
func TestSaveMasksSecretsInBodyAndIndex(t *testing.T) {
	const secret = "sk-abcdefghijklmnopqrstuvwx"
	dir := t.TempDir()
	s := Store{Dir: dir}

	path, err := s.Save(Memory{
		Name: "Integration note",
		Type: TypeProject,
		Body: "The staging key is " + secret + " and it expires in March.",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(body), secret) {
		t.Fatalf("the key reached disk:\n%s", body)
	}
	if !strings.Contains(string(body), "<redacted:api-key>") {
		t.Errorf("the key should be replaced by a mask:\n%s", body)
	}

	// The index line carries a hook derived from the body, so it is a second
	// place the same secret would otherwise appear.
	idx := s.Index()
	if strings.Contains(idx, secret) {
		t.Fatalf("the key reached the index:\n%s", idx)
	}
	if !strings.Contains(idx, "<redacted:api-key>") {
		t.Errorf("the index hook should carry the mask:\n%s", idx)
	}
	// The mask must not break the index row's link syntax.
	if !strings.Contains(idx, ".md)") {
		t.Errorf("index row is no longer parseable:\n%s", idx)
	}
	// ...and the store must still read its own file back.
	list := s.List()
	if len(list) != 1 || !strings.Contains(list[0].Body, "<redacted:api-key>") {
		t.Fatalf("round trip through the masked file failed: %+v", list)
	}
}

// TestAppendDocMasksSecrets covers the "#" quick-add path, which writes a plain
// bullet rather than a rendered fact file.
func TestAppendDocMasksSecrets(t *testing.T) {
	const secret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	dir := t.TempDir()
	path := filepath.Join(dir, "hiq.md")
	if err := AppendDoc(path, "prod token is "+secret); err != nil {
		t.Fatalf("append: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(body), secret) {
		t.Fatalf("the token reached disk:\n%s", body)
	}
	if !strings.Contains(string(body), "## Notes") {
		t.Errorf("the quick-add section was lost:\n%s", body)
	}
}

// TestWriteDocFileMasksSecrets covers the panel's in-place editor, the write
// path a user hits when they paste a note by hand.
func TestWriteDocFileMasksSecrets(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	path := filepath.Join(t.TempDir(), "hiq.md")
	if err := writeDocFile(path, "# Notes\n\nAWS key "+secret+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), secret) {
		t.Fatalf("the key reached disk:\n%s", body)
	}
}

// TestOrdinaryNotesSurviveUnchanged is the false-positive guard: masking must
// not mangle ordinary project notes, and must not add a trailing newline
// difference that would make every save look like a change.
func TestOrdinaryNotesSurviveUnchanged(t *testing.T) {
	dir := t.TempDir()
	s := Store{Dir: dir}
	body := "Build with `pnpm build` (not npm).\nOrder id 6217001234567890 is a hash, not a card.\nPort 8080."
	if _, err := s.Save(Memory{Name: "build", Type: TypeProject, Body: body}); err != nil {
		t.Fatalf("save: %v", err)
	}
	list := s.List()
	if len(list) != 1 {
		t.Fatalf("want 1 memory, got %d", len(list))
	}
	if !strings.Contains(list[0].Body, "6217001234567890") {
		t.Errorf("an order number was mangled:\n%s", list[0].Body)
	}
	if strings.Contains(list[0].Body, "<redacted") {
		t.Errorf("ordinary text was masked:\n%s", list[0].Body)
	}
}

// TestEveryMemoryWriteGoesThroughTheFunnel is the drift guard. The redaction
// rule only holds while there is exactly one write path; a new os.WriteFile in
// this package would quietly reopen the hole, so the invariant is asserted
// against the source rather than trusted.
func TestEveryMemoryWriteGoesThroughTheFunnel(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == "scrub.go" {
			continue // the funnel itself
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(src), "os.WriteFile") {
			t.Errorf("%s writes directly to disk; route it through writeMemoryFile so the "+
				"PII pass cannot be bypassed", name)
		}
	}
}
