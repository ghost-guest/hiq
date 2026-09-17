package team

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// MemberSpec is the JSON shape an LLM returns when drafting a member from a
// natural-language instruction ("帮我加一个擅长后端的团员"). It is deliberately
// a flat, forgiving struct so a slightly-off model reply still parses.
type MemberSpec struct {
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Skills       []string `json:"skills"`
	Model        string   `json:"model"`
	Effort       string   `json:"effort"`
	SystemPrompt string   `json:"system_prompt"`
	Avatar       string   `json:"avatar"`
	IsLeader     bool     `json:"is_leader"`
}

// TeamSpec is the JSON shape an LLM returns when drafting a whole team from a
// natural-language goal ("帮我组一个团队做 X").
type TeamSpec struct {
	Name        string `json:"name"`
	Goal        string `json:"goal"`
	Constraints string `json:"constraints"`
	// LeaderIndex points at the member that should be the 团长 (0-based).
	LeaderIndex int          `json:"leader_index"`
	Members     []MemberSpec `json:"members"`
}

// MemberDraftSystemPrompt is the system prompt for drafting ONE member. It
// pins the output to a single JSON object (no prose, no code fence) so the
// desktop layer can parse it deterministically.
func MemberDraftSystemPrompt() string {
	return `你是 multi-agent 团队配置助手。用户会用自然语言描述他想要的一个"团员"（子智能体）。
你的任务：把它翻译成一个团员配置，并只输出一个 JSON 对象，不要输出任何解释、前缀或 Markdown 代码围栏。

JSON 字段（全部可选，但要尽量填全）：
{
  "name":        "团员名（2-6 字，体现职责，不要用"助手""智能体"这类空泛词）",
  "role":        "一句话职责，例如 后端实现 / 接口测试 / 竞品调研",
  "skills":      ["3-6 个能力标签，用于按能力自动派活，例如 backend、go、sql"],
  "model":       "可选：指定的 模型引用（provider/model）。不确定就留空字符串",
  "effort":      "可选：推理档位 low|medium|high，不确定就留空",
  "system_prompt": "给这个团员的人设与工作纪律（中文，明确身份"你就是X"、职责边界、输出要求）",
  "avatar":      "一个 emoji 头像",
  "is_leader":   false
}

纪律：
- skills 用短标签，中英文都行，但同一团队内风格要统一。
- system_prompt 要具体：说明它负责什么、不负责什么、产出什么格式。
- 不要臆造用户没提到的技术栈；用户没说的能力就不要写进 skills。
- 只输出 JSON。`
}

// MemberDraftUserPrompt builds the user message: the team roster (so the draft
// fits the existing team and doesn't duplicate a name/skill) + the raw
// instruction.
func MemberDraftUserPrompt(t Team, instruction string) string {
	var b strings.Builder
	b.WriteString("【团队】")
	b.WriteString(t.Name)
	if g := firstNonEmpty(t.Context.Goal, t.Goal); g != "" {
		b.WriteString("\n【团队目标】")
		b.WriteString(g)
	}
	if c := t.Context.Constraints; c != "" {
		b.WriteString("\n【约束】")
		b.WriteString(c)
	}
	if len(t.Members) > 0 {
		b.WriteString("\n【现有成员】")
		for _, m := range t.Members {
			b.WriteString("\n- ")
			b.WriteString(m.Name)
			if m.Role != "" {
				b.WriteString("（")
				b.WriteString(m.Role)
				b.WriteString("）")
			}
			if len(m.Skills) > 0 {
				b.WriteString(" 能力: ")
				b.WriteString(strings.Join(m.Skills, ", "))
			}
			if m.IsLeader {
				b.WriteString(" [团长]")
			}
		}
		b.WriteString("\n（新团员应与现有成员互补，不要重复同样的职责与能力。）")
	}
	b.WriteString("\n\n【用户要求】")
	b.WriteString(strings.TrimSpace(instruction))
	return b.String()
}

// TeamDraftSystemPrompt is the system prompt for drafting a whole team.
func TeamDraftSystemPrompt() string {
	return `你是 multi-agent 团队配置助手。用户会用自然语言描述一个项目目标，你要给出一个"团长 + 团员"团队配置。
只输出一个 JSON 对象，不要解释、不要 Markdown 代码围栏。

{
  "name": "团队名（4-8 字）",
  "goal": "一句话项目目标",
  "constraints": "关键约束/交付要求（可空）",
  "leader_index": 0,
  "members": [
    {"name":"团长名","role":"统筹与拆解任务","skills":["planning","architecture"],"system_prompt":"你就是团长……","avatar":"🧭","is_leader":true},
    {"name":"团员名","role":"……","skills":["..."],"system_prompt":"你就是……","avatar":"🛠","is_leader":false}
  ]
}

纪律：
- 第一位必须是团长（is_leader=true，leader_index=0），它的职责是分析、拆解、分派、跟踪进度，而不是亲自做所有事。
- 2-5 个团员，能力互补、边界清晰（例如 实现 / 测试 / 调研 / 文档）。
- 每个 system_prompt 用"你就是X"框定身份，说明职责、不负责什么、产出格式。
- 不要臆造用户没提的技术栈。
- 只输出 JSON。`
}

// ParseMemberDraft extracts a MemberSpec from a raw model reply (tolerating
// code fences and surrounding prose) and converts it to a Member.
func ParseMemberDraft(raw string) (Member, error) {
	obj, err := extractJSONObject(raw)
	if err != nil {
		return Member{}, err
	}
	var spec MemberSpec
	if err := json.Unmarshal([]byte(obj), &spec); err != nil {
		return Member{}, fmt.Errorf("team: parse member draft: %w", err)
	}
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		return Member{}, errors.New("team: draft is missing a name")
	}
	return spec.Member(), nil
}

// ParseTeamDraft extracts a TeamSpec from a raw model reply and converts it to
// a Team (with the leader flagged).
func ParseTeamDraft(raw string) (Team, error) {
	obj, err := extractJSONObject(raw)
	if err != nil {
		return Team{}, err
	}
	var spec TeamSpec
	if err := json.Unmarshal([]byte(obj), &spec); err != nil {
		return Team{}, fmt.Errorf("team: parse team draft: %w", err)
	}
	if strings.TrimSpace(spec.Name) == "" {
		return Team{}, errors.New("team: draft is missing a name")
	}
	if len(spec.Members) == 0 {
		return Team{}, errors.New("team: draft has no members")
	}
	t := Team{
		Name: spec.Name,
		Goal: strings.TrimSpace(spec.Goal),
		Context: TeamContext{
			Goal:        strings.TrimSpace(spec.Goal),
			Constraints: strings.TrimSpace(spec.Constraints),
		},
	}
	for i, ms := range spec.Members {
		m := ms.Member()
		m.IsLeader = ms.IsLeader || i == spec.LeaderIndex
		t.Members = append(t.Members, m)
	}
	// Guarantee exactly one leader: if the model marked none (and the index is
	// out of range) promote the first member, matching the "有且仅有一个团长"
	// invariant.
	if _, ok := t.Leader(); !ok {
		t.Members[0].IsLeader = true
	}
	t.normalize()
	return t, nil
}

// Member converts a draft spec into a Member.
func (s MemberSpec) Member() Member {
	return Member{
		Name:         strings.TrimSpace(s.Name),
		Role:         strings.TrimSpace(s.Role),
		Model:        strings.TrimSpace(s.Model),
		Effort:       strings.TrimSpace(s.Effort),
		Skills:       NormalizeSkills(s.Skills),
		SystemPrompt: strings.TrimSpace(s.SystemPrompt),
		Avatar:       strings.TrimSpace(s.Avatar),
		IsLeader:     s.IsLeader,
	}
}

// IdentityPrompt returns the member's system prompt, composing a sensible one
// from Name/Role/Skills when the member has none. This is what the execution
// layer frames the member's isolated sub-session with.
func (m Member) IdentityPrompt() string {
	if p := strings.TrimSpace(m.SystemPrompt); p != "" {
		return p
	}
	var b strings.Builder
	b.WriteString("你就是「")
	b.WriteString(m.Name)
	b.WriteString("」。")
	if m.Role != "" {
		b.WriteString("你在团队中的职责是：")
		b.WriteString(m.Role)
		b.WriteString("。")
	}
	if len(m.Skills) > 0 {
		b.WriteString("你的能力：")
		b.WriteString(strings.Join(m.Skills, "、"))
		b.WriteString("。")
	}
	b.WriteString("只负责职责范围内的事，产出要具体、可直接交付；对职责外的问题，说明并交回团长。")
	return b.String()
}

// BlackboardDigest renders a compact, index-style projection of the team
// blackboard for injection into a member's run (R6). It is intentionally an
// INDEX (goal + constraints + decision/artifact titles + open questions), not
// full text, so a long-lived team can't blow the member's context window — the
// same discipline the memory subsystem's PromptIndex uses.
//
// maxChars <= 0 uses DefaultDigestMaxChars. The result is truncated at a line
// boundary so it never cuts mid-token.
func BlackboardDigest(t Team, maxChars int) string {
	if maxChars <= 0 {
		maxChars = DefaultDigestMaxChars
	}
	var b strings.Builder
	if g := firstNonEmpty(t.Context.Goal, t.Goal); g != "" {
		b.WriteString("【团队目标】")
		b.WriteString(g)
		b.WriteString("\n")
	}
	if c := strings.TrimSpace(t.Context.Constraints); c != "" {
		b.WriteString("【约束】")
		b.WriteString(c)
		b.WriteString("\n")
	}
	if len(t.Context.Decisions) > 0 {
		b.WriteString("【已定决策】\n")
		for _, d := range t.Context.Decisions {
			b.WriteString("- ")
			b.WriteString(d.Text)
			b.WriteString("\n")
		}
	}
	if len(t.Context.Artifacts) > 0 {
		b.WriteString("【已产出】\n")
		for _, a := range t.Context.Artifacts {
			b.WriteString("- ")
			b.WriteString(a.Title)
			if a.Path != "" {
				b.WriteString("（")
				b.WriteString(a.Path)
				b.WriteString("）")
			}
			if s := strings.TrimSpace(a.Summary); s != "" {
				b.WriteString(" — ")
				b.WriteString(s)
			}
			b.WriteString("\n")
		}
	}
	// The shared scratchpad (P4 共享上下文): the most recent member-contributed
	// notes, newest last so the freshest knowledge sits next to the task frame.
	if len(t.Context.Notes) > 0 {
		notes := t.Context.Notes
		if len(notes) > notesDigestMax {
			notes = notes[len(notes)-notesDigestMax:]
		}
		b.WriteString("【共享笔记（其他成员留下）】\n")
		for _, n := range notes {
			b.WriteString("- ")
			if who := t.ResolveNoteAuthor(n.Author); who != "" {
				b.WriteString("[" + who + "] ")
			}
			b.WriteString(n.Text)
			b.WriteString("\n")
		}
	}
	if len(t.Context.OpenQuestions) > 0 {
		b.WriteString("【未决问题】\n")
		for _, q := range t.Context.OpenQuestions {
			b.WriteString("- ")
			b.WriteString(q)
			b.WriteString("\n")
		}
	}
	return truncateAtLine(b.String(), maxChars)
}

// DefaultDigestMaxChars bounds the blackboard digest injected per member run.
const DefaultDigestMaxChars = 1200

// truncateAtLine cuts s to at most max bytes, preferring the last newline so
// the digest never ends mid-line.
func truncateAtLine(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, "\n") + "\n…"
}

// extractJSONObject returns the first balanced top-level {...} object in raw,
// tolerating ```json fences and surrounding prose. Quoted strings and escapes
// are respected so a brace inside a string doesn't end the object early.
func extractJSONObject(raw string) (string, error) {
	return extractJSONValue(raw, '{')
}

// extractJSONValue returns the first balanced top-level value opened by `open`
// ('{' or '['), tolerating ```json fences and surrounding prose. Quoted strings
// and escapes are respected so a bracket inside a string can't close it early.
func extractJSONValue(raw string, open byte) (string, error) {
	closeCh := byte('}')
	if open == '[' {
		closeCh = ']'
	}
	s := strings.TrimSpace(raw)
	// Strip a fenced block if present.
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		// Drop an optional language tag on the same line.
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			tag := strings.TrimSpace(rest[:nl])
			if tag == "" || (!strings.ContainsRune(tag, '{') && !strings.ContainsRune(tag, '[')) {
				rest = rest[nl+1:]
			}
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			rest = rest[:j]
		}
		if strings.ContainsRune(rest, rune(open)) {
			s = rest
		}
	}
	start := strings.IndexByte(s, open)
	if start < 0 {
		return "", errors.New("team: no JSON value found in reply")
	}
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case open:
			depth++
		case closeCh:
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", errors.New("team: unterminated JSON value in reply")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
