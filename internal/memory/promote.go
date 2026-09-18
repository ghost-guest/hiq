package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zzycxz/fairpeer/internal/extensioncontract"
)

// Promotion turns accumulated memory into something the agent can USE — the
// step that makes memory self-evolving rather than merely accumulating.
//
// The design constraint is that a promotion must never touch the agent kernel.
// Everything it writes is DATA in a documented on-disk layout, consumed through
// channels the agent already has:
//
//   - the SKILL.md is a plain skill playbook, so the existing skill loader
//     indexes it (name + description in the cache-stable prompt index) and
//     `run_skill` executes it. No new tool, no prompt edit, no rebuild.
//   - the plugin manifest declares capabilities in the kernel's own
//     extensioncontract key format (namespace/kind/id + version), so a future
//     capability resolver can load the package without a format migration.
//   - the source memories are snapshotted under references/, which the skill
//     loader already appends to the body — the distilled playbook carries its
//     raw material, and re-promoting after new memories arrive refreshes it.
//
// Because it is all data, an agent iteration can ignore promotions entirely, and
// a promotion can never break an agent build. That is the "plugin, not a patch"
// property the memory layer needs.

// PromoteKind selects the artifact a promotion produces. A plugin is a superset:
// it contains the skill plus a manifest and provenance, so it can be versioned,
// listed and uninstalled as a unit.
type PromoteKind string

const (
	PromoteSkill  PromoteKind = "skill"  // SKILL.md (+ references/) only
	PromotePlugin PromoteKind = "plugin" // SKILL.md + references/ + plugin.json + README.md
)

// PromoteRequest selects the source memories and describes the artifact.
//
// Selection is either explicit (`Memories` names) or filtered (`Level`/`Tag`/
// `Query`), exactly matching the `recall` tool's vocabulary — so "promote
// everything tagged postgres at L2" is the same query the model would run.
type PromoteRequest struct {
	Kind        PromoteKind
	Name        string // artifact id (slug); derived from Description when empty
	Description string // one-liner for the skill index; derived when empty
	Memories    []string
	Level       string
	Tag         string
	Query       string
	Version     string
	// Notes is optional extra guidance appended to the playbook — the "and now
	// do it this way" a human adds on top of what the memories say.
	Notes string `json:"notes,omitempty"`
	// Root is where the package directory is created. Empty uses the store's own
	// data root (…/fairpeer/plugins/<name>), so a relocated memory root carries
	// promoted artifacts with it.
	Root string
	// Overwrite re-promotes an existing artifact in place — the normal path when
	// new memories have accumulated and the playbook should be refreshed.
	Overwrite bool
}

// PromotedSource is one memory that contributed to an artifact, recorded so the
// artifact can be traced back to (and refreshed from) its sources.
type PromotedSource struct {
	Name  string   `json:"name"`
	Level string   `json:"level"`
	Tags  []string `json:"tags,omitempty"`
	Hook  string   `json:"hook,omitempty"`
}

// Promotion is what a promotion produced, as paths rather than content, so the
// panel can show and open the result.
type Promotion struct {
	Kind     PromoteKind      `json:"kind"`
	Name     string           `json:"name"`
	Dir      string           `json:"dir"`
	Skill    string           `json:"skill"`
	Manifest string           `json:"manifest,omitempty"`
	Readme   string           `json:"readme,omitempty"`
	Refs     []string         `json:"refs,omitempty"`
	Sources  []PromotedSource `json:"sources"`
	Version  string           `json:"version"`
}

// Promotion limits. A promotion is a distilled playbook with its material
// attached, not a memory dump: past these bounds the artifact would cost more
// context than the memories it replaces.
const (
	maxPromoteRefs      = 50
	maxPromoteRefBytes  = 64 << 10
	maxPromoteSources   = 100
	defaultPromoteVer   = "1.0.0"
	promoteManifestName = "plugin.json"
)

// PromoteMemory folds the selected memories into a skill/plugin package.
func PromoteMemory(store Store, req PromoteRequest) (Promotion, error) {
	kind := req.Kind
	if kind == "" {
		kind = PromotePlugin
	}
	if kind != PromoteSkill && kind != PromotePlugin {
		return Promotion{}, fmt.Errorf("unknown promotion kind %q (use %q or %q)", req.Kind, PromoteSkill, PromotePlugin)
	}
	sources, err := selectPromotionSources(store, req)
	if err != nil {
		return Promotion{}, err
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		version = defaultPromoteVer
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = slugify(req.Description)
	}
	if name == "" {
		name = slugify(firstLine(sources[0].body))
	}
	if !validPromoteName(name) {
		return Promotion{}, fmt.Errorf("promote: %q is not a usable artifact name — use letters, digits, '_', '-' or '.'", name)
	}

	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		desc = fmt.Sprintf("Distilled from %d saved memor%s: %s", len(sources), pluralMem(len(sources)), sources[0].hook)
	}
	desc = oneLine(desc)

	root := strings.TrimSpace(req.Root)
	if root == "" {
		root = store.DataRoot()
	}
	if root == "" {
		return Promotion{}, fmt.Errorf("promote: no destination root (the memory store is disabled)")
	}
	bucket := "skills"
	if kind == PromotePlugin {
		bucket = "plugins"
	}
	dir := filepath.Join(root, bucket, name)
	if !req.Overwrite {
		if _, err := os.Stat(dir); err == nil {
			return Promotion{}, fmt.Errorf("promote: %s already exists at %s — re-run with overwrite to refresh it", kind, dir)
		}
	}

	out := Promotion{
		Kind:    kind,
		Name:    name,
		Dir:     dir,
		Version: version,
		Sources: promotedSourceList(sources),
		Refs:    []string{},
		Skill:   filepath.Join(dir, "SKILL.md"),
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Promotion{}, err
	}
	skill := renderPromotedSkill(name, desc, version, out.Sources, strings.TrimSpace(req.Notes))
	if err := writeMemoryFile(out.Skill, skill); err != nil {
		return Promotion{}, err
	}

	// references/ carries the raw material. The skill loader appends sibling
	// references/*.md to the body, so invoking the skill brings the memories
	// that produced it — no recall round-trip, no drift between the two.
	refDir := filepath.Join(dir, "references")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		return Promotion{}, err
	}
	var budget = maxPromoteRefBytes
	for i, src := range sources {
		if i >= maxPromoteRefs || budget <= 0 {
			break
		}
		body := fmt.Sprintf("# %s (%s)\n\n%s\n", src.name, LevelOf(src.memory).Label(), strings.TrimSpace(src.body))
		if len(body) > budget {
			body = body[:budget] + "\n\n…（超出长度上限，已截断；用 `recall " + src.name + "` 读取全文）\n"
		}
		budget -= len(body)
		file := filepath.Join(refDir, src.name+".md")
		if err := writeMemoryFile(file, body); err != nil {
			return Promotion{}, err
		}
		out.Refs = append(out.Refs, file)
	}

	if kind == PromoteSkill {
		return out, nil
	}

	// Plugin: a manifest in the kernel's own capability vocabulary plus a README
	// explaining what the package is. The manifest is validated against
	// extensioncontract so a promotion cannot emit a capability key the kernel
	// would reject later.
	manifest := pluginManifest{
		SchemaVersion: 1,
		Kind:          "memory-plugin",
		ID:            "memory-" + name,
		Name:          name,
		Version:       version,
		Description:   desc,
		GeneratedBy:   "fairpeer/memory",
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Sources:       out.Sources,
		Entrypoints:   map[string]string{"skill": "SKILL.md", "readme": "README.md"},
		Capabilities: []extensioncontract.Capability{{
			Key:     extensioncontract.CapabilityKey{Namespace: "memory", Kind: "skill", ID: name},
			Version: version,
		}},
	}
	for _, c := range manifest.Capabilities {
		if err := c.Validate(); err != nil {
			return Promotion{}, fmt.Errorf("promote: generated manifest is invalid: %w", err)
		}
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Promotion{}, err
	}
	out.Manifest = filepath.Join(dir, promoteManifestName)
	if err := writeMemoryFile(out.Manifest, string(append(raw, '\n'))); err != nil {
		return Promotion{}, err
	}
	out.Readme = filepath.Join(dir, "README.md")
	if err := writeMemoryFile(out.Readme, renderPromotedReadme(name, desc, version, out.Sources)); err != nil {
		return Promotion{}, err
	}
	return out, nil
}

// pluginManifest is the on-disk package descriptor. It is deliberately a small,
// versioned, data-only contract: no executable entrypoint, so loading a memory
// plugin can never introduce code into the agent.
type pluginManifest struct {
	SchemaVersion int                            `json:"schemaVersion"`
	Kind          string                         `json:"kind"`
	ID            string                         `json:"id"`
	Name          string                         `json:"name"`
	Version       string                         `json:"version"`
	Description   string                         `json:"description"`
	GeneratedBy   string                         `json:"generatedBy"`
	GeneratedAt   string                         `json:"generatedAt"`
	Sources       []PromotedSource               `json:"sources"`
	Entrypoints   map[string]string              `json:"entrypoints"`
	Capabilities  []extensioncontract.Capability `json:"capabilities"`
}

// promotionSource is a selected memory plus the derived hook the renderers show.
type promotionSource struct {
	name   string
	body   string
	hook   string
	memory Memory
}

// selectPromotionSources resolves a request's selection to concrete memories.
// An empty selection is an error: promoting "everything" by accident would fold
// unrelated facts into one artifact.
func selectPromotionSources(store Store, req PromoteRequest) ([]promotionSource, error) {
	facts := store.List()
	if len(facts) == 0 {
		return nil, fmt.Errorf("promote: no saved memories to promote")
	}
	byName := make(map[string]Memory, len(facts))
	for _, m := range facts {
		byName[strings.ToLower(m.Name)] = m
	}

	var picked []Memory
	if len(req.Memories) > 0 {
		for _, want := range req.Memories {
			want = strings.TrimSpace(want)
			if want == "" {
				continue
			}
			m, ok := byName[strings.ToLower(want)]
			if !ok {
				return nil, fmt.Errorf("promote: no saved memory named %q", want)
			}
			picked = append(picked, m)
		}
	} else {
		levelFilter, hasLevel := ParseLevelArg(req.Level)
		tag := strings.ToLower(strings.TrimSpace(req.Tag))
		query := strings.ToLower(strings.TrimSpace(req.Query))
		if !hasLevel && tag == "" && query == "" {
			return nil, fmt.Errorf("promote: select at least one memory (names, or a level/tag/query filter)")
		}
		for _, m := range facts {
			if hasLevel && LevelOf(m) != levelFilter {
				continue
			}
			if tag != "" && !hasTag(m, tag) {
				continue
			}
			if query != "" && !matchesQuery(m, query) {
				continue
			}
			picked = append(picked, m)
		}
	}
	if len(picked) == 0 {
		return nil, fmt.Errorf("promote: the selection matched no saved memory")
	}
	// Deterministic order (level broad → specific, then name) keeps a re-promote
	// byte-identical for the same inputs, so the artifact is diffable.
	rank := map[Level]int{LevelGlobal: 0, LevelProject: 1, LevelSession: 2}
	sort.SliceStable(picked, func(i, j int) bool {
		li, lj := rank[LevelOf(picked[i])], rank[LevelOf(picked[j])]
		if li != lj {
			return li < lj
		}
		return picked[i].Name < picked[j].Name
	})
	if len(picked) > maxPromoteSources {
		picked = picked[:maxPromoteSources]
	}
	out := make([]promotionSource, 0, len(picked))
	for _, m := range picked {
		hook := oneLine(firstLine(m.Body))
		if hook == "" {
			hook = oneLine(m.Body)
		}
		out = append(out, promotionSource{name: m.Name, body: m.Body, hook: hook, memory: m})
	}
	return out, nil
}

// promotedSourceList maps sources onto the serializable provenance records.
func promotedSourceList(sources []promotionSource) []PromotedSource {
	out := make([]PromotedSource, 0, len(sources))
	for _, s := range sources {
		out = append(out, PromotedSource{
			Name:  s.name,
			Level: string(LevelOf(s.memory)),
			Tags:  s.memory.Tags,
			Hook:  s.hook,
		})
	}
	return out
}

// renderPromotedSkill writes the playbook. Frontmatter carries exactly the keys
// the skill loader reads (`name`, `description`), so the artifact is indexed and
// invokable the moment it lands in a skill root.
func renderPromotedSkill(name, desc, version string, sources []PromotedSource, notes string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", name)
	fmt.Fprintf(&b, "description: %s\n", yamlScalar(desc))
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n\n", strings.ReplaceAll(name, "-", " "))
	fmt.Fprintf(&b, "%s\n\n", desc)
	b.WriteString("This playbook was distilled from saved memory by fairpeer's promotion step, so the ")
	b.WriteString("lesson survives without every session having to re-read the source facts.\n")
	if notes != "" {
		fmt.Fprintf(&b, "\n## 使用要点\n\n%s\n", notes)
	}
	b.WriteString("\n## 何时使用\n\n")
	fmt.Fprintf(&b, "- 任务与下述记忆主题相关时（共 %d 条来源记忆）。\n", len(sources))
	b.WriteString("- 需要复用已经验证过的做法、约定或结论，而不是重新推导。\n")
	b.WriteString("\n## 来源记忆\n\n")
	for _, s := range sources {
		line := fmt.Sprintf("- `%s` — %s", s.Name, s.Hook)
		if s.Level != "" {
			line += fmt.Sprintf("（%s", strings.ToUpper(s.Level))
			if len(s.Tags) > 0 {
				line += " · " + strings.Join(s.Tags, ", ")
			}
			line += "）"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n## 说明\n\n")
	b.WriteString("本技能 `references/` 下的文件是来源记忆的快照正文（运行时自动附加）。")
	b.WriteString("记忆更新后重新执行一次「提升」即可刷新本技能，来源记忆本身不会被改动。\n")
	fmt.Fprintf(&b, "\n<!-- promoted by fairpeer memory v%s -->\n", version)
	return b.String()
}

// renderPromotedReadme explains the package to a human reading the directory.
func renderPromotedReadme(name, desc, version string, sources []PromotedSource) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n\n", name, desc)
	fmt.Fprintf(&b, "- 版本：%s\n- 类型：memory-plugin（纯数据，不含可执行代码）\n", version)
	fmt.Fprintf(&b, "- 能力声明：`memory/skill/%s`\n", name)
	fmt.Fprintf(&b, "- 来源记忆：%d 条\n\n", len(sources))
	b.WriteString("## 文件\n\n")
	b.WriteString("- `SKILL.md` — 可执行剧本（技能加载器读取 name/description 进索引）\n")
	b.WriteString("- `plugin.json` — 插件清单（能力、版本、来源，使用内核 extensioncontract 键格式）\n")
	b.WriteString("- `references/` — 来源记忆正文快照\n\n")
	b.WriteString("## 如何集成\n\n")
	b.WriteString("1. 最简单：把 `SKILL.md` 所在目录复制/软链到技能根目录（如 `~/.fairpeer/skills/<name>/`），")
	b.WriteString("技能索引会自动收录，agent 通过 `run_skill` 调用——无需改内核。\n")
	b.WriteString("2. 插件方式：把本目录加入 `[skills] paths`，或在能力解析器接入后按 manifest 注册。\n\n")
	b.WriteString("## 卸载\n\n直接删除本目录即可；来源记忆不受影响。\n")
	return b.String()
}

// validPromoteName keeps artifact names to the same charset as skill names, so a
// promoted artifact can always be installed as a skill.
func validPromoteName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// yamlScalar quotes a frontmatter value when it contains characters that would
// otherwise change the document's shape (colons, leading quotes/braces).
func yamlScalar(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, `:#{}[]"'`) || strings.HasPrefix(s, "-") || strings.HasPrefix(s, " ") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
