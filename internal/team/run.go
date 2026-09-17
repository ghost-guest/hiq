package team

import (
	"fmt"
	"strings"
)

// DefaultMemberMaxSteps bounds one member run's tool-call rounds. A member does
// a bounded slice of work (one card), not the whole project, so its budget is
// deliberately smaller than an unbounded parent turn.
const DefaultMemberMaxSteps = 24

// DefaultMemberSystemPrompt frames a member that has no explicit SystemPrompt.
// Like the kernel's sub-agent prompt it must be self-contained: the member never
// sees the leader's conversation, only this prompt plus the task brief.
const DefaultMemberSystemPrompt = `你是多智能体团队中的一名团员（执行者）。
你只负责完成分配给你的这一个任务；不要替其他成员或团长做决定。
需要澄清时，明确写出你的问题并停下，不要猜测。
把你最终的产出作为回答正文给出（而不是过程叙述）——团长只会看到这段正文。`

// LeaderPrompt is the fallback identity for a 团长 with no explicit
// SystemPrompt (used when the leader itself is asked to run a card).
const LeaderPrompt = `你是这个团队的团长（负责人）。
你的职责是分析目标、把目标拆解成可验收的任务、按能力把任务派给合适的团员、并跟踪进展；你不包办所有执行。
回答要给出结论与下一步，而不是罗列过程。`

// MemberRunPrompt builds the ISOLATED system prompt for running one member on
// one task.
//
// Layout: identity (persona) → team alignment (the L0 blackboard digest) → task
// frame (acceptance criteria + self-check). The digest is the mechanism that
// keeps a member with its own independent context from drifting off the team
// direction.
//
// It is injected ONLY here, into the member's own sub-session. It must never be
// folded into the main conversation: the main session's system prompt is under a
// strict byte-stability invariant (the prompt-cache prefix guard).
func MemberRunPrompt(t Team, m Member, tk Task) string {
	var b strings.Builder

	b.WriteString(memberIdentity(m))
	b.WriteString("\n\n")

	if digest := strings.TrimSpace(BlackboardDigest(t, 0)); digest != "" {
		b.WriteString("## 团队上下文（共享黑板，只读）\n")
		b.WriteString("以下是你所在团队的目标 / 约束 / 已定决策 / 已产出 / 未决问题。执行时必须遵守，不要偏离团队方向：\n")
		b.WriteString(digest)
		b.WriteString("\n")
	}

	b.WriteString("## 你被分配的任务\n")
	b.WriteString("任务：")
	b.WriteString(firstNonEmpty(tk.Title, "（未命名任务）"))
	b.WriteString("\n")
	if d := strings.TrimSpace(tk.Desc); d != "" {
		b.WriteString("说明：")
		b.WriteString(d)
		b.WriteString("\n")
	}
	if len(tk.RequiredSkills) > 0 {
		b.WriteString("所需能力：")
		b.WriteString(strings.Join(trimAll(tk.RequiredSkills), "、"))
		b.WriteString("\n")
	}
	if len(tk.Acceptance) > 0 {
		b.WriteString("验收标准（全部满足才算完成）：\n")
		for _, c := range tk.Acceptance {
			if s := strings.TrimSpace(c.Text); s != "" {
				b.WriteString("- ")
				b.WriteString(s)
				b.WriteString("\n")
			}
		}
	}
	b.WriteString("\n完成后按验收标准逐条自检，并在正文中给出你的产出；如果产出的是文件，写出文件的完整路径。\n")
	b.WriteString(SharedNotesInstruction)
	return b.String()
}

// SharedNotesInstruction teaches a member how to contribute to the team's shared
// context (P4 共享上下文). It is deliberately part of the run prompt rather than a
// tool: publishing a note needs no write access to the team store, works on
// every provider, and costs the member nothing but a paragraph.
const SharedNotesInstruction = `
## 向团队共享上下文投稿（可选，但强烈建议）
如果你在干活时发现了**别的成员需要知道**的信息——关键结论、踩到的坑、接口/字段约定、
没做完的遗留点——请在正文最后另起一节，标题就写【共享笔记】，每条一行：

【共享笔记】
- 结论/坑/约定，一条一句话
- 需要别人接手的事也写在这里

没有要补充的就整节省略；不要为了凑数而写，也不要写只有你自己才看得懂的流水账。
这一节会被系统摘出来放进团队共享上下文，**你后面接手的成员都会读到它**。
注意：这一节必须是正文的最后一部分。`

// TaskBrief is the user message handed to a member's sub-session. The system
// prompt already carries the identity and blackboard, so this stays a focused
// restatement of the deliverable plus the team goal for orientation.
func TaskBrief(t Team, tk Task) string {
	var b strings.Builder
	b.WriteString(firstNonEmpty(tk.Title, "（未命名任务）"))
	if d := strings.TrimSpace(tk.Desc); d != "" {
		b.WriteString("\n\n")
		b.WriteString(d)
	}
	if len(tk.Acceptance) > 0 {
		b.WriteString("\n\n验收标准：\n")
		for _, c := range tk.Acceptance {
			if s := strings.TrimSpace(c.Text); s != "" {
				b.WriteString("- ")
				b.WriteString(s)
				b.WriteString("\n")
			}
		}
	}
	if g := firstNonEmpty(t.Context.Goal, t.Goal); strings.TrimSpace(g) != "" {
		b.WriteString("\n（团队总目标：")
		b.WriteString(g)
		b.WriteString("）")
	}
	return b.String()
}

// memberIdentity resolves the member's persona prompt: its own SystemPrompt when
// set, otherwise one composed from Name + Role + Skills.
func memberIdentity(m Member) string {
	if s := strings.TrimSpace(m.SystemPrompt); s != "" {
		return s
	}
	if m.IsLeader {
		return LeaderPrompt
	}
	name := strings.TrimSpace(m.Name)
	if name == "" {
		name = "团员"
	}
	role := strings.TrimSpace(m.Role)
	if role == "" {
		role = "完成分配到的任务"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("你是团队成员「%s」，职责：%s。\n", name, role))
	if skills := trimAll(m.Skills); len(skills) > 0 {
		b.WriteString("你的专长：")
		b.WriteString(strings.Join(skills, "、"))
		b.WriteString("。\n")
	}
	b.WriteString(DefaultMemberSystemPrompt)
	return b.String()
}

// ResultArtifact turns a finished task into a blackboard artifact entry, so the
// next member sees what was already produced (the shared-context payoff). The
// one-line Summary is the gist of the deliverable, so a later member can judge
// relevance without opening the file.
func ResultArtifact(tk Task, summary string) Artifact {
	title := strings.TrimSpace(tk.Title)
	if title == "" {
		title = "任务产出"
	}
	return Artifact{
		ID:      newID("art"),
		Title:   title,
		TaskID:  tk.ID,
		Kind:    "deliverable",
		Path:    firstLinePath(summary),
		Summary: artifactSummary(summary),
	}
}

// artifactSummary reduces a deliverable body to one useful line: the first
// prose line that is neither the path nor a heading, so the 已产出 index reads
// like a table of contents rather than a list of filenames.
func artifactSummary(body string) string {
	path := firstLinePath(body)
	var fallback string
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(strings.Trim(line, "`*#>-\" "))
		if t == "" || len([]rune(t)) > 200 {
			continue
		}
		if t == path || strings.HasPrefix(t, "#") {
			continue
		}
		if fallback == "" {
			fallback = t
		}
		// Prefer a sentence-like line over a bare token.
		if strings.ContainsAny(t, "。．.！!？?：:，,") || len([]rune(t)) >= 12 {
			return clipTeam(t, 160)
		}
	}
	return clipTeam(fallback, 160)
}

// clipTeam truncates s to max runes, appending an ellipsis when it cut.
func clipTeam(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

// firstLinePath extracts a likely file path from a result body, so the artifact
// index can point at the produced file. A line with a path separator wins; a bare
// filename is the fallback. Display convenience only — "" when nothing fits.
func firstLinePath(s string) string {
	var extHit string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.Trim(line, "`*#>-\" "))
		if line == "" || len(line) > 200 {
			continue
		}
		if strings.ContainsAny(line, "/\\") {
			return line
		}
		if extHit == "" && looksLikeFilename(line) {
			extHit = line
		}
	}
	return extHit
}

// looksLikeFilename reports whether a single token reads as "name.ext" with a
// short alphanumeric extension (so prose and Chinese sentences are rejected).
func looksLikeFilename(s string) bool {
	if strings.Contains(s, " ") {
		return false
	}
	i := strings.LastIndexByte(s, '.')
	if i <= 0 || i >= len(s)-1 {
		return false
	}
	ext := s[i+1:]
	if len(ext) > 5 {
		return false
	}
	for _, r := range ext {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// trimAll trims each entry and drops the empties.
func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
