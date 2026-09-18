package team

import (
	"sort"
	"strings"

	"github.com/zzycxz/hiq/internal/taskmonitor"
)

// AssignCandidate is a scored member for a task, surfaced so the UI can show
// the leader WHY it picked someone (and offer alternatives).
type AssignCandidate struct {
	MemberID string `json:"member_id"`
	Name     string `json:"name"`
	Score    int    `json:"score"`
	// Matched lists the required skills this member covers.
	Matched []string `json:"matched"`
	// Missing lists the required skills this member lacks.
	Missing []string `json:"missing"`
	// Load is the member's open (non-terminal, assigned) task count.
	Load int `json:"load"`
	// IsLeader marks the routing owner (kept out of the top slot unless it is
	// the only/best match, so the leader orchestrates instead of executing).
	IsLeader bool `json:"is_leader"`
}

// SuggestAssignee picks the best member for a task via capability routing
// (CrewAI-style hierarchical matching):
//
//  1. skill coverage first — how many of the task's RequiredSkills the member has;
//  2. then lower load — spread work instead of piling it on one member;
//  3. then non-leader — let the leader orchestrate rather than execute;
//  4. then name — deterministic.
//
// Returns false when the team has no members.
func SuggestAssignee(t Team, task Task) (Member, bool) {
	cands := RankCandidates(t, task)
	if len(cands) == 0 {
		return Member{}, false
	}
	m, ok := t.Member(cands[0].MemberID)
	if !ok {
		return Member{}, false
	}
	return m, true
}

// RankCandidates scores every member against a task and returns them best-first.
func RankCandidates(t Team, task Task) []AssignCandidate {
	if len(t.Members) == 0 {
		return nil
	}
	load := openLoad(t)
	out := make([]AssignCandidate, 0, len(t.Members))
	for _, m := range t.Members {
		matched, missing := skillDiff(task.RequiredSkills, m.Skills)
		out = append(out, AssignCandidate{
			MemberID: m.ID,
			Name:     m.Name,
			Score:    len(matched),
			Matched:  matched,
			Missing:  missing,
			Load:     load[m.ID],
			IsLeader: m.IsLeader,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Load != b.Load {
			return a.Load < b.Load
		}
		if a.IsLeader != b.IsLeader {
			return !a.IsLeader
		}
		return a.Name < b.Name
	})
	return out
}

// openLoad counts each member's assigned, non-terminal tasks.
func openLoad(t Team) map[string]int {
	load := make(map[string]int, len(t.Members))
	for _, tk := range t.Tasks {
		if tk.AssigneeID == "" {
			continue
		}
		if tk.Status.Terminal() {
			continue
		}
		load[tk.AssigneeID]++
	}
	return load
}

// skillDiff splits required skills into the ones a member has and the ones it
// lacks. Matching is case- and whitespace-insensitive and de-duplicated.
func skillDiff(required, have []string) (matched []string, missing []string) {
	if len(required) == 0 {
		return nil, nil
	}
	owned := make(map[string]bool, len(have))
	for _, s := range have {
		if k := normalizeSkill(s); k != "" {
			owned[k] = true
		}
	}
	seen := make(map[string]bool, len(required))
	for _, s := range required {
		k := normalizeSkill(s)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		if owned[k] {
			matched = append(matched, strings.TrimSpace(s))
		} else {
			missing = append(missing, strings.TrimSpace(s))
		}
	}
	return matched, missing
}

// normalizeSkill canonicalises a capability tag for comparison.
func normalizeSkill(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// NormalizeSkills trims and de-duplicates a skill list, preserving order.
func NormalizeSkills(skills []string) []string {
	out := make([]string, 0, len(skills))
	seen := make(map[string]bool, len(skills))
	for _, s := range skills {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		k := normalizeSkill(s)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

// TerminalStates returns the states a card can be moved to that end its life.
func TerminalStates() []taskmonitor.TaskState {
	return []taskmonitor.TaskState{
		taskmonitor.TaskStateSucceeded,
		taskmonitor.TaskStateFailed,
		taskmonitor.TaskStateCancelled,
	}
}
