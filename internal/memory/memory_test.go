package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestComposeEmptyIsIdentity is the cache-first invariant: with no memory at
// all, Compose must return the base prompt byte-for-byte, so the cached system
// prefix is exactly what it was before memory existed.
func TestComposeEmptyIsIdentity(t *testing.T) {
	base := "You are a helpful coding agent.\nBe concise."
	got := Compose(base, &Set{})
	if got != base {
		t.Fatalf("empty memory changed the prompt:\n base=%q\n got =%q", base, got)
	}
	// A nil-ish set (no docs, blank index) must also be identity.
	if got := Compose(base, &Set{Index: "   \n"}); got != base {
		t.Fatalf("blank index changed the prompt: got %q", got)
	}
}

// TestComposeAppendsAfterBase verifies memory folds in *after* the base prompt,
// so the base stays a valid cache prefix even as memory changes between sessions.
func TestComposeAppendsAfterBase(t *testing.T) {
	base := "BASE PROMPT"
	set := &Set{Docs: []Source{{Path: "/p/hiq.md", Scope: ScopeProject, Body: "Use tabs."}}}
	got := Compose(base, set)
	if !strings.HasPrefix(got, base) {
		t.Fatalf("base is not the prefix of the composed prompt:\n%q", got)
	}
	if !strings.Contains(got, "Use tabs.") {
		t.Fatalf("doc body not folded into prompt:\n%q", got)
	}
}

// TestBlockInjectsUserProfile confirms the "always present" user profile: when
// TestBlockInjectsUserProfile ensures the portrait layer (profile/user.md +
// memory.md + profile/<mode>.md) folds into the system prompt so the model
// knows the user without a tool call. The portrait is plain user/dream-authored
// markdown; what is written is exactly what the model sees.
func TestBlockInjectsUserProfile(t *testing.T) {
	user := t.TempDir()
	mustMkdir(t, filepath.Join(user, "profile"))
	mustWrite(t, filepath.Join(user, "profile", "user.md"), "# 关于用户\n张三，后端工程师。")
	mustWrite(t, filepath.Join(user, "profile", "memory.md"), "# 客观事实\n项目在 C:\\swarm。")
	mustWrite(t, filepath.Join(user, "profile", "dev.md"), "偏好 Go。")

	set := Load(Options{CWD: user, UserDir: user, Profile: "dev"})
	got := Compose("BASE", set)

	if !strings.Contains(got, "张三，后端工程师。") {
		t.Errorf("user portrait not injected:\n%s", got)
	}
	if !strings.Contains(got, "项目在 C:\\swarm。") {
		t.Errorf("global memory not injected:\n%s", got)
	}
	if !strings.Contains(got, "偏好 Go。") {
		t.Errorf("mode portrait not injected:\n%s", got)
	}
}

// TestBlockOmitsProfileWhenNone ensures an empty portrait adds nothing — the
// cache-stable prefix stays maximal when there's nothing to say.
func TestBlockOmitsProfileWhenNoUserFacts(t *testing.T) {
	user := t.TempDir()
	set := Load(Options{CWD: user, UserDir: user, Profile: "dev"})
	got := Compose("BASE", set)
	if got != "BASE" {
		t.Errorf("empty portrait should leave base untouched, got:\n%s", got)
	}
}

// TestProfilePartition ensures dev/cowork portraits are isolated: loading under
// dev injects only the global files (user.md + memory.md) + dev, not cowork.
func TestProfilePartition(t *testing.T) {
	user := t.TempDir()
	mustMkdir(t, filepath.Join(user, "profile"))
	mustWrite(t, filepath.Join(user, "profile", "user.md"), "SHARED-USER")
	mustWrite(t, filepath.Join(user, "profile", "memory.md"), "SHARED-MEMORY")
	mustWrite(t, filepath.Join(user, "profile", "dev.md"), "DEV-ONLY")
	mustWrite(t, filepath.Join(user, "profile", "cowork.md"), "COWORK-ONLY")

	dev := Load(Options{CWD: user, UserDir: user, Profile: "dev"})
	devBlock := dev.Block()
	for _, want := range []string{"SHARED-USER", "SHARED-MEMORY", "DEV-ONLY"} {
		if !strings.Contains(devBlock, want) {
			t.Errorf("dev should see %s:\n%s", want, devBlock)
		}
	}
	if strings.Contains(devBlock, "COWORK-ONLY") {
		t.Errorf("dev must not see cowork portrait:\n%s", devBlock)
	}

	cow := Load(Options{CWD: user, UserDir: user, Profile: "cowork"})
	cowBlock := cow.Block()
	if strings.Contains(cowBlock, "DEV-ONLY") {
		t.Errorf("cowork must not see dev portrait:\n%s", cowBlock)
	}
	if !strings.Contains(cowBlock, "COWORK-ONLY") {
		t.Errorf("cowork should see cowork portrait:\n%s", cowBlock)
	}
}

// TestDiscoverPrecedenceOrder checks user → ancestor → project → local ordering,
// which puts the most specific guidance last.
func TestDiscoverPrecedenceOrder(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "userconfig")
	proj := filepath.Join(root, "proj")
	mustMkdir(t, user)
	mustMkdir(t, proj)
	// Make proj a git root so discovery stops there.
	mustMkdir(t, filepath.Join(proj, ".git"))

	mustWrite(t, filepath.Join(user, "hiq.md"), "USER LEVEL")
	mustWrite(t, filepath.Join(proj, "hiq.md"), "PROJECT LEVEL")
	mustWrite(t, filepath.Join(proj, "hiq.local.md"), "LOCAL LEVEL")

	set := Load(Options{CWD: proj, UserDir: user})
	if len(set.Docs) != 3 {
		t.Fatalf("want 3 docs, got %d: %+v", len(set.Docs), set.Docs)
	}
	wantScopes := []Scope{ScopeUser, ScopeProject, ScopeLocal}
	for i, s := range wantScopes {
		if set.Docs[i].Scope != s {
			t.Fatalf("doc %d: want scope %q, got %q", i, s, set.Docs[i].Scope)
		}
	}
	// In the composed block, local must appear after project must appear after user.
	block := set.Block()
	iu, ip, il := strings.Index(block, "USER LEVEL"), strings.Index(block, "PROJECT LEVEL"), strings.Index(block, "LOCAL LEVEL")
	if !(iu >= 0 && iu < ip && ip < il) {
		t.Fatalf("precedence order wrong in block: user=%d project=%d local=%d\n%s", iu, ip, il, block)
	}
}

// TestImportResolution checks "@path" inlining, including a relative import.
func TestImportResolution(t *testing.T) {
	proj := t.TempDir()
	mustMkdir(t, filepath.Join(proj, ".git"))
	mustWrite(t, filepath.Join(proj, "shared.md"), "SHARED CONTENT")
	mustWrite(t, filepath.Join(proj, "hiq.md"), "Top line\n@shared.md\nBottom line")

	set := Load(Options{CWD: proj})
	if len(set.Docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(set.Docs))
	}
	body := set.Docs[0].Body
	if !strings.Contains(body, "SHARED CONTENT") {
		t.Fatalf("import not inlined: %q", body)
	}
	if strings.Contains(body, "@shared.md") {
		t.Fatalf("import directive left in body: %q", body)
	}
}

// TestImportCycleDoesNotHang verifies cycle detection terminates.
func TestImportCycleDoesNotHang(t *testing.T) {
	proj := t.TempDir()
	mustMkdir(t, filepath.Join(proj, ".git"))
	mustWrite(t, filepath.Join(proj, "a.md"), "A\n@b.md")
	mustWrite(t, filepath.Join(proj, "b.md"), "B\n@a.md")
	mustWrite(t, filepath.Join(proj, "hiq.md"), "@a.md")

	set := Load(Options{CWD: proj}) // must return, not loop forever
	body := set.Docs[0].Body
	if !strings.Contains(body, "A") || !strings.Contains(body, "B") {
		t.Fatalf("cycle import dropped content: %q", body)
	}
}

// TestImportTargetClassification guards the "@mention vs @import" heuristic.
func TestImportTargetClassification(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"@docs/setup.md", true},
		{"@./notes.txt", true},
		{"@/abs/path.md", true},
		{"@mention", false},      // prose-y, no separator/dot
		{"@", false},             // bare
		{"@a/b and more", false}, // not the only token
		{"plain text", false},
	}
	for _, c := range cases {
		if _, got := importTarget(c.line); got != c.want {
			t.Errorf("importTarget(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestImportDiamondAndCycle(t *testing.T) {
	proj := t.TempDir()
	mustMkdir(t, filepath.Join(proj, ".git"))

	mustWrite(t, filepath.Join(proj, "shared.md"), "SHARED CONTENT")
	mustWrite(t, filepath.Join(proj, "a.md"), "A\n@shared.md")
	mustWrite(t, filepath.Join(proj, "b.md"), "B\n@shared.md")
	mustWrite(t, filepath.Join(proj, "hiq.md"), "@a.md\n@b.md")

	set := Load(Options{CWD: proj})
	if len(set.Docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(set.Docs))
	}
	body := set.Docs[0].Body

	count := strings.Count(body, "SHARED CONTENT")
	if count != 2 {
		t.Errorf("expected 'SHARED CONTENT' to appear twice, got %d times. Body:\n%s", count, body)
	}
	if strings.Contains(body, "skipped: import cycle") {
		t.Errorf("body contains incorrect import cycle message:\n%s", body)
	}

	projCycle := t.TempDir()
	mustMkdir(t, filepath.Join(projCycle, ".git"))
	mustWrite(t, filepath.Join(projCycle, "cycle1.md"), "CYCLE1\n@cycle2.md")
	mustWrite(t, filepath.Join(projCycle, "cycle2.md"), "CYCLE2\n@cycle1.md")
	mustWrite(t, filepath.Join(projCycle, "hiq.md"), "@cycle1.md")

	setCycle := Load(Options{CWD: projCycle})
	if len(setCycle.Docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(setCycle.Docs))
	}
	bodyCycle := setCycle.Docs[0].Body
	if !strings.Contains(bodyCycle, "skipped: import cycle") {
		t.Errorf("expected import cycle to be detected and reported. Body:\n%s", bodyCycle)
	}
}
