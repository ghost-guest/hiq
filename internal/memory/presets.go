package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProfilePreset is one named preference template the user can write once and
// switch between ("减少AI味", "严格Excel匹配", …). Exactly one preset (the file's
// Active id) is injected per turn as a clearly labelled section after the
// portrait files, so the model sees it as the user's explicit current choice —
// distinct from the dream-maintained portrait that accumulates over time.
type ProfilePreset struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	// Builtin marks the factory-seeded presets so the panel can offer a
	// "restore defaults" action and tag them in the list. It is a provenance
	// tag, not a lock: builtin presets stay fully editable and deletable.
	Builtin bool `json:"builtin"`
}

// PresetFile is the on-disk shape of <mode>-presets.json: the selectable items
// plus the id of the one currently in use ("" = none selected → nothing extra
// injected beyond the portrait files).
type PresetFile struct {
	Active string          `json:"active"`
	Items  []ProfilePreset `json:"items"`
}

// preset guards against a runaway list bloating the file (and the panel):
// the UI is a simple picker, not a snippet library.
const (
	presetMaxItems   = 50
	presetMaxContent = 4000 // runes per item; injection is further capped by profileMaxChars
)

// presetsPath returns <userDir>/profile/<mode>-presets.json, or "" when there
// is no user config dir. Keyed by mode so cowork and dev presets never mix —
// the same code path serves both, dev just hasn't been wired to a panel yet.
func presetsPath(userDir, profile string) string {
	if userDir == "" {
		return ""
	}
	return filepath.Join(userDir, profileDir, NormalizeProfile(profile)+"-presets.json")
}

// defaultPresets seeds a fresh install (no presets file on disk yet): three
// factory presets per mode. Defaults live only in memory — the file is written
// on the user's first save, so an untouched install has no side effect on disk
// and future default-content changes still reach existing users who never
// saved. The frontend mirrors these lists (lib/builtinPresets.ts) for the
// browser dev mock and the panel's "restore built-ins" action — keep ids and
// content in sync.
func defaultPresets(profile string) PresetFile {
	var items []ProfilePreset
	switch NormalizeProfile(profile) {
	case "cowork":
		items = []ProfilePreset{
			{
				ID:      "reduce-ai",
				Name:    "减少AI味",
				Builtin: true,
				Content: "行文自然口语化，像人写的。禁用“首先/其次/最后/总之”式模板结构与空洞排比；少用“赋能、抓手、闭环”这类词；句长要有变化，直接给结论，不堆砌形容词，少用感叹号。",
			},
			{
				ID:      "strict-excel",
				Name:    "严格Excel匹配",
				Builtin: true,
				Content: "处理 Excel 时严格忠于原表：只改动明确要求的单元格，不擅自增删行列、不改格式与公式；输出中引用的数值、表头必须与原表逐字一致；拿不准就先问，不要猜。",
			},
			{
				ID:      "concise-summary",
				Name:    "少描述只给总结",
				Builtin: true,
				Content: "回复尽量短：直接给结果和关键结论，省略过程描述与步骤解释；优先用列表/表格呈现，能一句话说清的不展开。",
			},
		}
	case "dev":
		items = []ProfilePreset{
			{
				ID:      "minimal-diff",
				Name:    "最小改动",
				Builtin: true,
				Content: "只改任务要求的代码，不顺手重构、不改无关的格式与命名；改动范围之外的代码保持原样；能用小改动解决就不用大方案。",
			},
			{
				ID:      "match-style",
				Name:    "贴合现有风格",
				Builtin: true,
				Content: "新代码向周围代码看齐：命名、注释密度、错误处理、导入分组都模仿同文件/同包的既有写法，不引入新依赖和新风格，除非我明确要求。",
			},
			{
				ID:      "universal-code",
				Name:    "普遍适用",
				Builtin: true,
				Content: "写的代码要普遍适用，不针对当前一个用例写死：优先标准库和惯用写法，不依赖特定机器或环境；不硬编码绝对路径、密钥、账号；公共逻辑抽成可复用函数；处理常见边界（空输入、异常路径、超大输入）；注意跨平台差异（路径分隔符、编码、大小写）。",
			},
			{
				ID:      "explain-more",
				Name:    "多解释",
				Builtin: true,
				Content: "每次改动都说明：改了什么、为什么这么改、有什么影响；关键路径给出简要讲解，帮我理解这套代码库，不要只给结论。",
			},
		}
	}
	if len(items) == 0 {
		return PresetFile{}
	}
	// Nothing is active by default: a fresh user's system prompt stays
	// byte-for-byte untouched (maximal cache prefix) and model behaviour never
	// changes until the user explicitly picks a preset in the panel.
	return PresetFile{Items: items}
}

// LoadPresets reads the mode's presets file. A missing or corrupt file yields
// the factory defaults (best-effort like the rest of memory: unreadable state
// degrades to defaults, never to an error the panel can't handle).
func LoadPresets(userDir, profile string) PresetFile {
	p := presetsPath(userDir, profile)
	if p == "" {
		return PresetFile{}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return defaultPresets(profile)
	}
	var f PresetFile
	if err := json.Unmarshal(b, &f); err != nil {
		return defaultPresets(profile)
	}
	return normalizePresets(f)
}

// SavePresets normalizes and writes the mode's presets file. Normalization is
// defensive: trims names, fills duplicate/empty ids, drops blank items, clamps
// counts/lengths, and clears an Active id that no longer resolves — so the
// frontend can submit optimistic local state without pre-sanitizing it.
func SavePresets(userDir, profile string, f PresetFile) (string, error) {
	p := presetsPath(userDir, profile)
	if p == "" {
		return "", fmt.Errorf("no user config dir for presets")
	}
	f = normalizePresets(f)
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	if dir := filepath.Dir(p); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	if err := writeMemoryFile(p, string(append(b, '\n'))); err != nil {
		return "", err
	}
	return p, nil
}

// normalizePresets makes a submitted PresetFile well-formed: items trimmed and
// capped, ids unique and non-empty, Active either "" or an existing id.
func normalizePresets(f PresetFile) PresetFile {
	out := PresetFile{}
	seen := map[string]bool{}
	next := 1
	for _, it := range f.Items {
		it.Name = strings.TrimSpace(it.Name)
		it.Content = strings.TrimSpace(it.Content)
		if it.Name == "" && it.Content == "" {
			continue // a fully blank row carries nothing worth keeping
		}
		if r := []rune(it.Content); len(r) > presetMaxContent {
			it.Content = string(r[:presetMaxContent])
		}
		it.ID = strings.TrimSpace(it.ID)
		if it.ID == "" || seen[it.ID] {
			// Generate a unique id. Loop until free: with the item cap at 50 this
			// terminates immediately in practice.
			for {
				candidate := fmt.Sprintf("preset-%d", next)
				next++
				if !seen[candidate] {
					it.ID = candidate
					break
				}
			}
		}
		seen[it.ID] = true
		out.Items = append(out.Items, it)
		if len(out.Items) >= presetMaxItems {
			break
		}
	}
	out.Active = strings.TrimSpace(f.Active)
	activeOK := false
	for _, it := range out.Items {
		if it.ID == out.Active {
			activeOK = true
			break
		}
	}
	if !activeOK {
		out.Active = "" // dangling selection → treat as "none in use"
	}
	return out
}

// ActivePreset resolves the file's Active id to the preset it points at (nil
// when none is in use — by choice or because the id dangles). discoverProfile
// uses this to append the user's current explicit choice to the portrait.
func (f PresetFile) ActivePreset() *ProfilePreset {
	for i := range f.Items {
		if f.Items[i].ID == f.Active {
			return &f.Items[i]
		}
	}
	return nil
}

// PresetsPath returns the absolute path of the active mode's presets file, or
// "" when there is no user config dir. Read side for the panel that wants to
// show where preferences live (mirrors Set.ProfilePath).
func (s *Set) PresetsPath() string {
	if s == nil || s.UserDir == "" {
		return ""
	}
	return presetsPath(s.UserDir, s.ProfileName)
}
