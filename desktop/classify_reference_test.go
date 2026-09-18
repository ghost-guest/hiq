package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestPPTReferenceAttachment verifies the SubmitToTab trigger condition: an
// attachment is only routed to PreparePPTReference when BOTH (a) it's an
// image/PDF attachment token AND (b) the message shows PPT intent. This guards
// against two failure modes: burning a VLM call on every pasted screenshot
// (no PPT intent), and missing a real reference (intent present, attachment
// missed by the parser).
func TestPPTReferenceAttachment(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantPath string
		wantOK   bool
	}{
		{"image + ppt intent", "照这张图做个PPT @.hiq/attachments/shot.png", ".hiq/attachments/shot.png", true},
		{"pdf + ppt intent (演示文稿)", "把这个PDF转成演示文稿 @.hiq/attachments/doc.pdf", ".hiq/attachments/doc.pdf", true},
		{"slide keyword (english)", "make slides @.hiq/attachments/x.jpg", ".hiq/attachments/x.jpg", true},
		{"幻灯 keyword", "做几页幻灯 @.hiq/attachments/a.webp", ".hiq/attachments/a.webp", true},
		{"ppt intent but no attachment", "做个PPT关于AI发展", "", false},
		{"attachment but NO ppt intent → must skip (avoid VLM burn)", "看这张图里写了啥 @.hiq/attachments/shot.png", "", false},
		{"attachment but non-image/pdf ext → skip", "做个PPT @.hiq/attachments/notes.txt", "", false},
		{"multiple attachments, pick the image one", "做PPT @.hiq/attachments/a.txt 和 @.hiq/attachments/b.png", ".hiq/attachments/b.png", true},
		{"chinese comma delimiter after token", "做PPT@.hiq/attachments/x.png，谢谢", ".hiq/attachments/x.png", true},
		{"token at end of input (no trailing space)", "做个PPT 照这张 @.hiq/attachments/end.png", ".hiq/attachments/end.png", true},
		{"empty input", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := pptReferenceAttachment(c.input)
			if got != c.wantPath || ok != c.wantOK {
				t.Errorf("pptReferenceAttachment(%q) = (%q, %v), want (%q, %v)",
					c.input, got, ok, c.wantPath, c.wantOK)
			}
		})
	}
}

// TestHasPPTIntent sanity-checks the intent keyword net — broad enough to catch
// real PPT requests, doesn't match ordinary text.
func TestHasPPTIntent(t *testing.T) {
	yes := []string{"做个ppt", "PPT", "幻灯片", "演示文稿", "make slides", "slide deck", "PPTX"}
	for _, s := range yes {
		if !hasPPTIntent(lowerForTest(s)) {
			t.Errorf("hasPPTIntent(%q) = false, want true", s)
		}
	}
	no := []string{"写个文档", "翻译这段", "总结一下", "看这张图", "做个表格"}
	for _, s := range no {
		if hasPPTIntent(lowerForTest(s)) {
			t.Errorf("hasPPTIntent(%q) = true, want false", s)
		}
	}
}

// lowerForTest mirrors what pptReferenceAttachment does (strings.ToLower on the
// whole input) so the test exercises the same normalization.
func lowerForTest(s string) string {
	// strings.ToLower is what the real code uses; replicate without re-importing
	// strings here to keep the test focused (hasPPTIntent takes a pre-lowered str).
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// TestLocalPathReference verifies the second reference form: an ABSOLUTE local
// path pasted as text (e.g. 把 C:\Users\me\Desktop\shot.png 转成PPT). Guards three
// failure modes: existing paths must route (previously this form bypassed the
// gate entirely), non-existent paths must surface as `missing` (so the caller
// warns instead of silently dropping the reference), and URLs / relative paths
// must NOT match (false positives would burn VLM calls on ordinary messages).
func TestLocalPathReference(t *testing.T) {
	tmp := t.TempDir()
	existing := tmp + string(filepath.Separator) + "shot.png"
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	pdf := tmp + string(filepath.Separator) + "deck.pdf"
	if err := os.WriteFile(pdf, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name        string
		input       string
		wantFound   string
		wantMissing string
		wantOK      bool
	}{
		{"existing png path", "把 " + existing + " 转成PPT", existing, "", true},
		{"existing pdf path, quoted", `转成幻灯 "` + pdf + `" 谢谢`, pdf, "", true},
		{"missing path → missing, not ok", "把 " + tmp + string(filepath.Separator) + "nope.png 做成PPT", "", tmp + string(filepath.Separator) + "nope.png", false},
		{"URL with image ext → skip (no :// tokens)", "看这个 https://example.com/a.png 做PPT", "", "", false},
		{"relative path → skip", "把 shot.png 做成PPT", "", "", false},
		{"bare extension word → skip", "做个 shots.png 主题的PPT", "", "", false},
		{"no path at all", "做个PPT关于AI", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, miss, ok := localPathReference(c.input)
			if got != c.wantFound || miss != c.wantMissing || ok != c.wantOK {
				t.Errorf("localPathReference(%q) = (%q, %q, %v), want (%q, %q, %v)",
					c.input, got, miss, ok, c.wantFound, c.wantMissing, c.wantOK)
			}
		})
	}
}

// TestClearStaleReferenceFilesIn verifies the existence invariant's core: both
// leftovers from a previous task (reference-style.json + pdf-pages/) are
// removed, and clearing an already-clean home is a harmless no-op.
func TestClearStaleReferenceFilesIn(t *testing.T) {
	home := t.TempDir()
	fp := filepath.Join(home, ".hiq", "reference-style.json")
	pd := filepath.Join(home, ".hiq", "pdf-pages", "page-1.json")
	if err := os.MkdirAll(filepath.Dir(pd), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fp, pd} {
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	clearStaleReferenceFilesIn(home)
	for _, p := range []string{fp, pd} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists after clear (err=%v)", p, err)
		}
	}
	// Idempotent: a second clear on the clean home must not error or panic.
	clearStaleReferenceFilesIn(home)
}

// TestWriteReferenceStyleSourcePath verifies the writers record source_path —
// the absolute path qa_compare.py reads to find the reference image for the
// post-generation QA loop. Without it the QA script can only guess where the
// original attachment lives (relative paths resolve against whatever directory
// the skill happens to run in).
func TestWriteReferenceStyleSourcePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("HOME", home)        // and on unix

	src := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeReferenceStyle(src, "## 1 CONTENT\nx", ""); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".hiq", "reference-style.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Image      string `json:"image"`
		SourcePath string `json:"source_path"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Image != "shot.png" {
		t.Errorf("Image = %q, want shot.png", got.Image)
	}
	if !filepath.IsAbs(got.SourcePath) {
		t.Errorf("SourcePath = %q, want an absolute path", got.SourcePath)
	}
	if got.SourcePath != src {
		t.Errorf("SourcePath = %q, want %q", got.SourcePath, src)
	}
}

// TestParseRefAnalysis verifies the merged analyzer's verdict parser: the first
// non-empty line decides PLAIN/VISUAL, the rest becomes Body. Guards the two
// failure modes of a one-call gate: an ambiguous answer must default to the
// CHEAP path (PLAIN, never burn the vision pipeline on uncertainty), and the
// verdict line must not leak into the description/transcription consumed by
// ppt-auto.
func TestParseRefAnalysis(t *testing.T) {
	cases := []struct {
		name       string
		resp       string
		wantVisual bool
		wantVerdic string
		wantBody   string
	}{
		{
			name:       "visual + 4 sections",
			resp:       "VISUAL\n## 1 CONTENT\n标题\n## 2 LAYOUT\ntop-center title",
			wantVisual: true,
			wantVerdic: "VISUAL",
			wantBody:   "## 1 CONTENT\n标题\n## 2 LAYOUT\ntop-center title",
		},
		{
			name:       "plain + transcription",
			resp:       "PLAIN\n第一行文字\n第二行文字",
			wantVisual: false,
			wantVerdic: "PLAIN",
			wantBody:   "第一行文字\n第二行文字",
		},
		{
			name:       "code-fenced verdict",
			resp:       "`VISUAL`\n## 1 CONTENT\nx",
			wantVisual: true,
			wantVerdic: "VISUAL",
			wantBody:   "## 1 CONTENT\nx",
		},
		{
			name:       "VERDICT: prefix + reason tail",
			resp:       "VISUAL - has a large blue title and cards\n## 1 CONTENT\nx",
			wantVisual: true,
			wantVerdic: "VISUAL",
			wantBody:   "## 1 CONTENT\nx",
		},
		{
			name:       "A/B habit answers still work",
			resp:       "B\n## 1 CONTENT\nx",
			wantVisual: true,
			wantVerdic: "VISUAL",
			wantBody:   "## 1 CONTENT\nx",
		},
		{
			name:       "leading blank lines skipped",
			resp:       "\n\nPLAIN\nwords here",
			wantVisual: false,
			wantVerdic: "PLAIN",
			wantBody:   "words here",
		},
		{
			name:       "ambiguous prose → conservative PLAIN?",
			resp:       "这张图片看起来是视觉设计的",
			wantVisual: false,
			wantVerdic: "PLAIN?",
			wantBody:   "这张图片看起来是视觉设计的",
		},
		{
			name:       "empty response",
			resp:       "",
			wantVisual: false,
			wantVerdic: "PLAIN?",
			wantBody:   "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseRefAnalysis(c.resp)
			if got.IsVisual != c.wantVisual || got.Verdict != c.wantVerdic || got.Body != c.wantBody {
				t.Errorf("parseRefAnalysis(%q) = (%v, %q, %q), want (%v, %q, %q)",
					c.resp, got.IsVisual, got.Verdict, got.Body, c.wantVisual, c.wantVerdic, c.wantBody)
			}
		})
	}
}
