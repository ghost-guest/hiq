// Package ppttemplate loads PPT generation templates — JSON specs that pair a
// master .pptx file (optional) with theme colors/fonts and pre-defined slide
// layouts. The CUA reads these so most slide content can be placed by KNOWN
// coordinates instead of VLM-discovered ones: the single biggest speedup for
// visible PPT generation (a screen_perceive round is ~8s; a coordinate-driven
// add is instant).
//
// Templates live in a fixed directory (<user-config>/hiq/ppt-templates/)
// so the user just drops JSON files there; the desktop settings page lists them
// and lets the user pick the active one. No upload UI, no DB — files on disk.
package ppttemplate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zzycxz/hiq/internal/assets"
)

// Template is one PPT template spec, parsed from a JSON file in the templates
// dir. The active template drives PPT generation: its master_file (if set) is
// opened in WPS instead of a blank deck, and its layouts give ready-made
// coordinates so the CUA clicks/types at known positions.
type Template struct {
	// ID is the template's unique id. Defaults to the JSON filename stem.
	ID string `json:"id"`
	// Name is the human label shown in the settings dropdown.
	Name string `json:"name"`
	// MasterFile is an optional .pptx the deck opens from (inherits its cover,
	// theme, fonts). Empty = use a WPS default blank deck. Absolute path.
	MasterFile string `json:"master_file,omitempty"`
	// Theme holds colors/fonts applied to added text. All optional.
	Theme Theme `json:"theme,omitempty"`
	// Layouts maps a layout name (cover/content/section/...) to coordinates for
	// its elements. The CUA uses these directly instead of perceiving each slide.
	Layouts map[string]Layout `json:"layouts,omitempty"`
	// DefaultLayout is used when a slide doesn't specify one (e.g. body slides).
	DefaultLayout string `json:"default_layout,omitempty"`
	// PageRoles describes what each slide in the master_file IS, so the renderer
	// can use them correctly: duplicate the "content" page as a background for
	// new body slides, keep the "cover"/"toc"/"closing" pages as-is or fill their
	// designated regions. Keys are roles (cover/toc/content/closing/...), values
	// carry the 1-based slide index in the master + the content-fill region for
	// that role (normalized 0-100, like Layouts). When set, renders build body
	// slides by COPYING the content page (preserving its background) rather than
	// appending blank slides — the correct way to use a designed template.
	PageRoles map[string]PageRole `json:"page_roles,omitempty"`
}

// PageRole describes one slide in the master template by its purpose.
type PageRole struct {
	// Index is the 1-based slide number in the master_file this role refers to.
	// e.g. content role on slide 3 of a 4-page cover/toc/content/closing template.
	Index int `json:"index"`
	// FillRegion gives the normalized 0-100 area (on the 960x540 canvas) where
	// new content text should be placed when this page is used. For "content"
	// pages that get duplicated + filled, this is where body text lands (relative
	// to the page). Omit for pages used as-is (cover/closing with fixed text).
	FillRegion *Layout `json:"fill_region,omitempty"`
}

// Theme is the color/font spec applied to text added by the CUA.
type Theme struct {
	PrimaryColor    string `json:"primary_color,omitempty"` // "RRGGBB" hex, no #
	AccentColor     string `json:"accent_color,omitempty"`
	BackgroundColor string `json:"background_color,omitempty"`
	FontTitle       string `json:"font_title,omitempty"` // e.g. "微软雅黑"
	FontBody        string `json:"font_body,omitempty"`
	FontSizeTitle   int    `json:"font_size_title,omitempty"`
	FontSizeBody    int    `json:"font_size_body,omitempty"`
}

// Layout is the coordinate spec for one slide type. Coordinates are NORMALIZED
// 0-100 on a 960x540 canvas (same space screen_perceive / ppt text use), so the
// agent reasons "title near the top" not "at 36 points".
type Layout struct {
	TitleX, TitleY, TitleW, TitleH float64 // custom marshal; see below
	BodyX, BodyY, BodyW, BodyH     float64
}

// Marshal/Unmarshal: Layout uses flat fields (title_x/title_y/title_w/title_h/
// body_x/body_y/body_w/body_h) to match the JSON shape in the plan and keep the
// file readable. We implement them explicitly rather than tag each field.

// layoutJSON is the on-disk shape (flat fields), decoupled from Layout's grouped
// struct above. Conversion happens in marshal/unmarshal.
type layoutJSON struct {
	TitleX float64 `json:"title_x,omitempty"`
	TitleY float64 `json:"title_y,omitempty"`
	TitleW float64 `json:"title_w,omitempty"`
	TitleH float64 `json:"title_h,omitempty"`
	BodyX  float64 `json:"body_x,omitempty"`
	BodyY  float64 `json:"body_y,omitempty"`
	BodyW  float64 `json:"body_w,omitempty"`
	BodyH  float64 `json:"body_h,omitempty"`
}

// MarshalJSON flattens Layout into the on-disk shape.
func (l Layout) MarshalJSON() ([]byte, error) {
	return json.Marshal(layoutJSON(l))
}

// UnmarshalJSON accepts the flat on-disk shape.
func (l *Layout) UnmarshalJSON(b []byte) error {
	var j layoutJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	l.TitleX, l.TitleY, l.TitleW, l.TitleH = j.TitleX, j.TitleY, j.TitleW, j.TitleH
	l.BodyX, l.BodyY, l.BodyW, l.BodyH = j.BodyX, j.BodyY, j.BodyW, j.BodyH
	return nil
}

// View is a trimmed template for the settings dropdown (id + name only). The
// full template is loaded on demand when generating.
type View struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ToView makes a dropdown entry from a template.
func (t Template) ToView() View {
	name := t.Name
	if name == "" {
		name = t.ID
	}
	return View{ID: t.ID, Name: name}
}

// DefaultDir returns the templates directory (<user-config>/hiq/ppt-templates),
// creating it and seeding an example template on first use. Returns "" if the
// user config dir is unavailable.
func DefaultDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return ""
	}
	dir := filepath.Join(base, "hiq", "ppt-templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return dir // return the path even on error; LoadDir will report it
	}
	// Seed an example so the user has something to copy/modify. Idempotent — we
	// don't overwrite an existing example.
	example := filepath.Join(dir, "example.json")
	if _, err := os.Stat(example); os.IsNotExist(err) {
		_ = os.WriteFile(example, []byte(exampleTemplate), 0o644)
	}
	return dir
}

// SkillTemplatesDir returns the PPT skill's templates directory if it exists.
// This allows the settings page to also show templates bundled with the skill.
//
// Lookup order: the embedded-skill release dir (~/.hiq/skills/ppt-auto/
// templates, where EnsurePPTAutoSkill writes it) first, then the legacy exe-
// sibling and user-config locations as fallbacks for older installs.
func SkillTemplatesDir() string {
	// Released embedded skill (the canonical location going forward).
	if dir := assets.PPTAutoTemplatesDir(); dir != "" {
		return dir
	}
	// Legacy: exe-sibling .hiq layout (pre-embed releases).
	exePath, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exePath)
		skillDir := filepath.Join(exeDir, ".hiq", "skills", "ppt-auto", "templates")
		if _, err := os.Stat(skillDir); err == nil {
			return skillDir
		}
	}
	// Legacy: user-config layout.
	base, err := os.UserConfigDir()
	if err == nil {
		skillDir := filepath.Join(base, "hiq", "skills", "ppt-auto", "templates")
		if _, err := os.Stat(skillDir); err == nil {
			return skillDir
		}
	}
	return ""
}

// LoadDir scans `dir` for *.json templates AND *.pptx files, returning them
// sorted by name. .json files are parsed as full template specs; .pptx files
// that don't have a matching .json are returned as lightweight synthetic
// templates (ID = filename stem, MasterFile = absolute path) so the user can
// simply drop a .pptx into the directory and use it without writing JSON.
// A malformed .json file is skipped (not fatal) so one bad template doesn't
// break the whole list.
func LoadDir(dir string) ([]Template, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []Template{}, err
	}

	// Collect .json specs first (canonical).
	var out []Template
	jsonIDs := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		t, err := loadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // skip malformed; the user can fix it
		}
		out = append(out, t)
		jsonIDs[t.ID] = true
	}

	// Add .pptx files that don't already have a .json spec.
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".pptx" {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".pptx")
		if jsonIDs[id] {
			continue // .json spec takes precedence
		}
		out = append(out, Template{
			ID:         id,
			Name:       id,
			MasterFile: filepath.Join(dir, e.Name()),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// loadFile parses one template JSON file. The id defaults to the filename stem
// when missing, so users only need to name the file.
func loadFile(path string) (Template, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Template{}, err
	}
	var t Template
	if err := json.Unmarshal(b, &t); err != nil {
		return Template{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if t.ID == "" {
		t.ID = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	if t.Name == "" {
		t.Name = t.ID
	}
	return t, nil
}

// LoadActive returns the template with the given id from dir, or nil if the id
// is empty / not found. Empty id means "no active template" → the CUA falls
// back to a default blank deck; not finding a configured id is an error so the
// user knows the setting points at something deleted.
//
// LoadActive supports both .json template specs AND direct .pptx files. If `id`
// matches a .json file, that spec is loaded. If it matches a .pptx file (by
// filename stem), a synthetic template is returned with the .pptx as its
// MasterFile — so users can simply drop a .pptx into the templates dir and
// select it without writing any JSON.
func LoadActive(dir, id string) (*Template, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}

	// 1. Try .json specs first (the canonical path).
	all, err := LoadDir(dir)
	if err == nil {
		for i := range all {
			if all[i].ID == id {
				return &all[i], nil
			}
		}
	}

	// 2. Fallback: check for a .pptx file with matching stem.
	pptxPath := filepath.Join(dir, id+".pptx")
	if _, statErr := os.Stat(pptxPath); statErr == nil {
		return &Template{
			ID:         id,
			Name:       id,
			MasterFile: pptxPath,
		}, nil
	}

	return nil, fmt.Errorf("PPT 模板 %q 未找到 — 请在设置里重新选择，或在 %s 添加该模板", id, dir)
}

// Views is a convenience: the dropdown list for a directory.
func Views(dir string) []View {
	ts, _ := LoadDir(dir)
	vs := make([]View, 0, len(ts))
	for _, t := range ts {
		vs = append(vs, t.ToView())
	}
	return vs
}

// exampleTemplate is the seed file written on first run. It demonstrates all
// fields and documents the coordinate space, so a non-technical user can copy +
// edit it without reading the struct definition.
const exampleTemplate = `{
  "id": "example",
  "name": "示例模板（可复制修改）",
  "master_file": "",
  "theme": {
    "primary_color": "F28B50",
    "accent_color": "1E1E1E",
    "background_color": "121212",
    "font_title": "Inter",
    "font_body": "Inter",
    "font_size_title": 36,
    "font_size_body": 20
  },
  "layouts": {
    "cover":   { "title_x": 10, "title_y": 35, "title_w": 80, "title_h": 20 },
    "content": { "title_x": 8,  "title_y": 8,  "title_w": 84, "title_h": 12,
                 "body_x":  8,  "body_y":  25, "body_w":  84, "body_h": 65 },
    "section": { "title_x": 20, "title_y": 40, "title_w": 60, "title_h": 15 }
  },
  "default_layout": "content"
}
`
