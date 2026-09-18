package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConvertMDToGongwenDocx reads the Huawei ADS markdown written by GLM-5.2,
// converts each line to 公文-format DocSections, and writes a real .docx via
// writeDOCX. Skipped unless HIQ_GEN_SAMPLE=1. This is the reliable path:
// the model authors the CONTENT (markdown, no JSON-escaping risk), and a
// deterministic Go converter owns the FORMAT (the 公文 styles), which is the
// clean split the docxwrite.go header describes.
//
//	HIQ_GEN_SAMPLE=1 go test ./internal/tool/builtin/ -run TestConvertMDToGongwenDocx -v
func TestConvertMDToGongwenDocx(t *testing.T) {
	if os.Getenv("HIQ_GEN_SAMPLE") != "1" {
		t.Skip("set HIQ_GEN_SAMPLE=1 to run the markdown→docx conversion")
	}
	wd, _ := os.Getwd()
	// The markdown lives at <repo>/output/华为智驾_内容.md; tests run from
	// internal/tool/builtin, so walk up three dirs to the repo root.
	repo := wd
	for i := 0; i < 3; i++ {
		repo = filepath.Dir(repo)
	}
	src := filepath.Join(repo, "output", "华为智驾_内容.md")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read markdown %s: %v", src, err)
	}
	home, _ := os.UserHomeDir()
	out := filepath.Join(home, "Desktop", "华为智驾技术调研_5000字.docx")

	sections := mdToGongwenSections(string(raw))
	if err := writeDOCX(DocInput{Path: out, Title: "", Sections: sections}); err != nil {
		t.Fatalf("writeDOCX: %v", err)
	}
	t.Logf("converted %d sections → %s", len(sections), out)
}

// mdToGongwenSections converts the GLM-authored markdown into 公文 DocSections:
//
//	# 大标题        → paragraph SimSun 44 bold center
//	## 一、xxx       → heading L1 SimHei 32 (non-bold)
//	## （一）xxx     → heading L2 KaiTi 32 (treated as level-2 prefix)
//	**xxx** at line  → paragraph bold lead-in (kept as plain bold paragraph)
//	| a | b |        → table (header_bg #D9D9D9)
//	plain paragraph  → paragraph FangSong 32 indent 2
//
// It deliberately keeps the conversion simple and robust: every non-empty,
// non-heading, non-table line becomes a FangSong body paragraph.
func mdToGongwenSections(md string) []DocSection {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var out []DocSection
	inTable := false
	var headers []string
	var rows [][]string

	flushTable := func() {
		if len(headers) > 0 {
			out = append(out, DocSection{
				Type:    "table",
				Headers: headers,
				Rows:    rows,
				Style:   DocStyle{HeaderBg: "#D9D9D9", Align: "center"},
			})
		}
		headers, rows = nil, nil
		inTable = false
	}

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			if inTable {
				flushTable()
			}
			continue
		case strings.HasPrefix(line, "# "):
			if inTable {
				flushTable()
			}
			title := strings.TrimPrefix(line, "# ")
			// The H1 is the document title: 2号 SimSun bold center.
			out = append(out, DocSection{
				Type:  "paragraph",
				Text:  title,
				Style: DocStyle{Font: "SimSun", Size: 44, Bold: true, Align: "center", LineSpacing: 1.5},
			})
		case strings.HasPrefix(line, "## "):
			if inTable {
				flushTable()
			}
			heading := strings.TrimPrefix(line, "## ")
			// Level-1 headings start with 一、二、 (SimHei); "引言" and other
			// non-numbered sections also render as level-1 黑体. The model's
			// markdown only uses ## so we treat all as level 1 per 公文 first
			// layer; finer levels would need ### which the source lacks.
			out = append(out, DocSection{
				Type:  "heading",
				Level: 1,
				Text:  heading,
				Style: DocStyle{Font: "SimHei", Size: 32, Bold: false, LineSpacing: 1.5},
			})
		case strings.HasPrefix(line, "|"):
			// table row; skip the separator row (|---|---|).
			if strings.HasPrefix(strings.ReplaceAll(line, " ", ""), "|-") || strings.Contains(line, "---") {
				inTable = true
				continue
			}
			cells := splitTableRow(line)
			if !inTable {
				inTable = true
				headers = cells
			} else {
				rows = append(rows, cells)
			}
		default:
			if inTable {
				flushTable()
			}
			// Body paragraph. The model uses **xxx** bold lead-ins; we keep
			// them as plain text (公文 body is uniformly FangSong, no inline
			// bold), stripping the ** markers.
			text := strings.ReplaceAll(line, "**", "")
			out = append(out, DocSection{
				Type:  "paragraph",
				Text:  text,
				Style: DocStyle{Font: "FangSong", Size: 32, Indent: 2, LineSpacing: 1.5},
			})
		}
	}
	flushTable()
	return out
}

// splitTableRow parses "| a | b | c |" into ["a","b","c"].
func splitTableRow(line string) []string {
	s := strings.Trim(line, " ")
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	parts := strings.Split(s, "|")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}
