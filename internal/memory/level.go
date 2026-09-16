package memory

import "strings"

// Level is the scope hierarchy of a stored memory. It answers "how far does this
// fact reach?" — the question the old single `project` boolean could only answer
// with two values, and which the panel and the retrieval layer both need.
//
//	L1 — global   : who the user is and how they like to work. Cross-project.
//	L2 — project  : this project's conventions, decisions and constraints.
//	L3 — session  : working memory for the current task. Never injected; it is
//	                retrieved on demand and is expected to go stale.
//
// Level is orthogonal to Profile: Level decides WHICH tree (global vs project vs
// session), Profile decides WHICH mode partition (dev / cowork / shared) inside
// that tree. A fact can therefore be "L1, dev-only" or "L2, shared".
type Level string

const (
	LevelGlobal  Level = "l1" // 全局：身份与偏好，跨项目，始终可见
	LevelProject Level = "l2" // 项目：约定与决策，仅该项目可见
	LevelSession Level = "l3" // 会话/任务：工作记忆，不注入，按需召回
)

// Levels lists the hierarchy broad → specific, for display and iteration.
var Levels = []Level{LevelGlobal, LevelProject, LevelSession}

// validLevels is the closed set NormalizeLevel accepts.
var validLevels = map[Level]bool{LevelGlobal: true, LevelProject: true, LevelSession: true}

// NormalizeLevel coerces an arbitrary string to a known Level, defaulting to L1
// (the broadest scope) so a sloppy tool argument never hides a fact in a narrower
// bucket than the caller intended. It accepts the canonical "l1"/"l2"/"l3", the
// bare digits, and the descriptive names the model may reach for.
func NormalizeLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "l1", "1", "global", "user":
		return LevelGlobal
	case "l2", "2", "project":
		return LevelProject
	case "l3", "3", "session", "task", "local":
		return LevelSession
	}
	return LevelGlobal
}

// ParseLevelArg is NormalizeLevel but reports whether the caller actually named
// a level. Tool handlers use it to distinguish "no level given" (derive one from
// the legacy project/profile arguments) from "explicitly L1".
func ParseLevelArg(s string) (Level, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return LevelGlobal, false
	}
	for _, l := range Levels {
		if s == string(l) {
			return l, true
		}
	}
	if s == "1" || s == "global" || s == "user" {
		return LevelGlobal, true
	}
	if s == "2" || s == "project" {
		return LevelProject, true
	}
	if s == "3" || s == "session" || s == "task" || s == "local" {
		return LevelSession, true
	}
	return NormalizeLevel(s), true
}

// LevelOf returns a memory's level, inferring one for records written before the
// hierarchy existed: a fact saved with the legacy `project: true` flag was
// project-scoped, everything else was shared. This is what makes old memory trees
// load correctly after the upgrade instead of collapsing into L1.
func LevelOf(m Memory) Level {
	if l := Level(strings.ToLower(strings.TrimSpace(string(m.Level)))); validLevels[l] {
		return l
	}
	if strings.EqualFold(strings.TrimSpace(m.Profile), "project") {
		return LevelProject
	}
	return LevelGlobal
}

// Label is the human-facing name of a level, used by the panel and by the
// injected index so the model can tell scopes apart at a glance.
func (l Level) Label() string {
	switch l {
	case LevelProject:
		return "L2/project"
	case LevelSession:
		return "L3/session"
	default:
		return "L1/global"
	}
}

// Injected reports whether a level's facts are surfaced in the always-on memory
// index. L3 is working memory for one task: it would be stale by the time the
// next session read it, so it stays out of the prompt and is retrieved only when
// the model (or the user) asks for it.
func (l Level) Injected() bool { return l != LevelSession }

// normalizeTags cleans a tag list: trimmed, lower-cased, de-duplicated, and
// capped so one save can't flood the index with keywords. Order is preserved so
// the author's first tag stays first.
func normalizeTags(in []string) []string {
	const maxTags = 8
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) == maxTags {
			break
		}
	}
	return out
}

// ParseTags splits a comma/space separated tag string (the form the `remember`
// tool and the panel accept) into a normalized tag list.
func ParseTags(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return normalizeTags(strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	}))
}
