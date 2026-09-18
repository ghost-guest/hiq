package team

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Shared-context checkpoints (open-vetta 移植 P1-4 步骤 B).
//
// Layering, from freshest to oldest:
//
//	hot window  TeamContext.Notes (MaxSharedNotes, newest kept) → rendered in
//	            the digest, capped at notesDigestMax lines.
//	checkpoints TeamContext.Checkpoints → a compact summary of everything that
//	            has aged out, rendered identically for every member.
//	archive     <teamID>.notes.jsonl → the raw text, pageable on demand.
//
// The point of a checkpoint is bounded visibility: a member joining a
// long-running team sees a summary of what it missed instead of either a wall
// of notes or nothing at all. Consistency is structural — every member renders
// the same checkpoints from the same team record — rather than something the
// members have to agree on.
//
// Counterpart in open-vetta: packages/agent-team/shared-context.ts (a shared
// summary checkpoint produced when the public context exceeds its budget).
// Independent Go implementation of the same mechanism.

const (
	// CheckpointTriggerNotes is the hot-window size that makes a checkpoint
	// worthwhile. Below it, the digest already shows nearly everything there is.
	CheckpointTriggerNotes = 40
	// CheckpointFoldCount is how many of the oldest in-window notes one pass
	// folds into a checkpoint.
	CheckpointFoldCount = 20
	// MaxCheckpoints bounds the retained checkpoint list. Older checkpoints are
	// themselves superseded by newer ones covering a wider range.
	MaxCheckpoints = 12
	// MaxCheckpointChars bounds one checkpoint's summary, so a runaway model
	// cannot inflate every later member's digest.
	MaxCheckpointChars = 700
	// checkpointsDigestMax caps how many checkpoints the digest renders (the
	// newest ones, which cover the most recent knowledge).
	checkpointsDigestMax = 4
)

// PendingCheckpointNotes returns the oldest notes that should be folded into a
// checkpoint, or nil when the hot window is small enough that a summary would
// cost more than it saves.
//
// The caller decides WHEN to run this (async, off the member's critical path),
// and is responsible for turning the result into a stored Checkpoint via
// ApplyCheckpoint.
func PendingCheckpointNotes(t Team) []Note {
	notes := t.Context.Notes
	if len(notes) < CheckpointTriggerNotes {
		return nil
	}
	// Notes are ordered oldest-first in storage; fold from the front so the
	// checkpoint covers the earliest, least-refresh knowledge.
	n := CheckpointFoldCount
	if n > len(notes) {
		n = len(notes)
	}
	out := make([]Note, n)
	copy(out, notes[:n])
	return out
}

// ApplyCheckpoint records a checkpoint covering notes, trims the in-window
// notes it covers, and bumps the blackboard version.
//
// It is deliberately tolerant: an empty summary is replaced by the degraded
// index so a checkpoint always says something useful.
func ApplyCheckpoint(t *Team, notes []Note, summary string, degraded bool) {
	if len(notes) == 0 {
		return
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = FallbackCheckpointSummary(notes)
		degraded = true
	}
	summary = clipTeam(summary, MaxCheckpointChars)
	upTo := notes[0].At
	for _, n := range notes {
		if n.At.After(upTo) {
			upTo = n.At
		}
	}
	next := Checkpoint{
		ID:       newID("ckpt"),
		UpTo:     upTo,
		Count:    len(notes),
		Summary:  summary,
		Degraded: degraded,
		At:       time.Now().UTC(),
	}
	t.Context.Checkpoints = append(t.Context.Checkpoints, next)
	if len(t.Context.Checkpoints) > MaxCheckpoints {
		t.Context.Checkpoints = t.Context.Checkpoints[len(t.Context.Checkpoints)-MaxCheckpoints:]
	}
	// Drop exactly the covered notes (by ID) rather than blindly slicing the
	// front: a concurrent append must survive.
	covered := make(map[string]struct{}, len(notes))
	for _, n := range notes {
		if n.ID != "" {
			covered[n.ID] = struct{}{}
		}
	}
	kept := t.Context.Notes[:0]
	for _, n := range t.Context.Notes {
		if _, ok := covered[n.ID]; ok && n.ID != "" {
			continue
		}
		kept = append(kept, n)
	}
	t.Context.Notes = kept
	t.Context.Version++
}

// FallbackCheckpointSummary builds an index-style summary from note bodies
// without any model. It is the guaranteed non-blocking path: a checkpoint must
// never fail because the LLM call did, so this runs whenever synthesis fails.
func FallbackCheckpointSummary(notes []Note) string {
	var b strings.Builder
	b.WriteString("（自动摘要不可用，以下为条目索引）")
	for _, n := range notes {
		line := firstSentence(n.Text)
		if line == "" {
			continue
		}
		b.WriteString("\n- ")
		if n.Author != "" {
			b.WriteString("[" + n.Author + "] ")
		}
		b.WriteString(line)
	}
	return strings.TrimSpace(b.String())
}

// firstSentence clips a note to its first sentence-ish fragment, so an index
// entry stays one line even when the note itself was long.
func firstSentence(s string) string {
	t := strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if t == "" {
		return ""
	}
	for i, r := range t {
		if r == '。' || r == '！' || r == '？' || r == '.' || r == '!' || r == '?' {
			// range yields the rune's START byte; include its full width so a
			// multi-byte terminator is never sliced in half.
			return clipTeam(t[:i+utf8.RuneLen(r)], 80)
		}
	}
	return clipTeam(t, 80)
}

// CheckpointPrompt builds the leader-side synthesis request. The system prompt
// pins the output to the summary text only (no preamble, no Markdown fence) so
// the reply can be stored verbatim.
//
// The note author is rendered as the member's display name when resolvable, so
// the summary reads as team history rather than a list of opaque IDs.
func CheckpointPrompt(t Team, notes []Note) (system, user string) {
	system = `你在为一个多智能体团队维护"共享笔记"的滚动摘要。
这些笔记是各成员干活时留下的结论、踩到的坑、接口约定与遗留点，比当前可见的窗口更早。
请把它们压缩成一段紧凑的中文摘要，供后续所有成员在开工前阅读。

要求：
- 只保留后续成员真正需要的信息：已确认的结论、约定、坑、仍未解决的遗留点。
- 合并重复项；丢弃只有原作者才看得懂的流水账。
- 按主题组织，不要逐条复述，不要输出"笔记1/笔记2"这类编号。
- 不要编造原文里没有的信息；不确定的就不要写。
- 直接输出摘要正文，不要任何前缀、标题或 Markdown 代码围栏。
- 控制在 500 字以内。`
	var b strings.Builder
	if g := firstNonEmpty(t.Context.Goal, t.Goal); g != "" {
		b.WriteString("【团队目标】")
		b.WriteString(g)
		b.WriteString("\n\n")
	}
	b.WriteString("【需要压缩的笔记】\n")
	for _, n := range notes {
		b.WriteString("- ")
		if who := t.ResolveNoteAuthor(n.Author); who != "" {
			b.WriteString("[" + who + "] ")
		}
		b.WriteString(n.Text)
		b.WriteString("\n")
	}
	user = b.String()
	return system, user
}

// CheckpointsDigest renders the checkpoint block for a member digest. Every
// member renders the same text (it is derived from the team record alone), so
// the team cannot end up with members holding differing versions of its past.
func CheckpointsDigest(t Team) string {
	cps := t.Context.Checkpoints
	if len(cps) == 0 {
		return ""
	}
	if len(cps) > checkpointsDigestMax {
		cps = cps[len(cps)-checkpointsDigestMax:]
	}
	var b strings.Builder
	b.WriteString("【早期共享笔记摘要（更早的笔记已归档）】\n")
	for _, c := range cps {
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(c.Summary))
		if c.Degraded {
			b.WriteString("（索引式摘要）")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ArchiveHint is the pointer line the digest appends so a member knows older
// notes exist and how to reach them. archivedTotal is the archive's note count;
// zero (or a count that adds nothing beyond the window) renders nothing.
func ArchiveHint(t Team, archivedTotal int) string {
	older := archivedTotal - len(t.Context.Notes)
	if older <= 0 {
		return ""
	}
	return fmt.Sprintf("（更早的 %d 条笔记已归档，可用 team_read_shared_history 分页查阅原文）\n", older)
}
