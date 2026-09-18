package agent

import (
	"fmt"
	"strings"

	"github.com/zzycxz/hiq/internal/provider"
)

// Pruning is the free half of context maintenance: stale tool results are
// re-derivable (files can be re-read, commands re-run), so eliding them needs
// no summarizer call and never drops a message — tool_call/result pairing and
// assistant content (including signed reasoning) are untouched by construction.
const (
	prunedMarker  = "[elided tool result — "
	minPruneBytes = 1024

	// SoftTrim constants: used by SoftTrimLargeResults for graduated pruning.
	// Outputs larger than SoftTrimThreshold in the prune zone are partially
	// trimmed (keep head+tail) before being candidates for full elision.
	SoftTrimThreshold = 4096
	SoftTrimKeepHead  = 1536
	SoftTrimKeepTail  = 1536
)

// PruneStats reports one prune pass.
type PruneStats struct {
	Results    int
	SavedChars int
	Archive    string
}

// PruneStaleToolResults elides tool-result content older than the protected
// recent tail, archiving the originals first. Idempotent; a no-op when
// compaction is disabled (no context window).
func (a *Agent) PruneStaleToolResults() (PruneStats, error) {
	var st PruneStats
	if a.contextWindow <= 0 {
		return st, nil
	}
	msgs := a.session.Messages
	head, start, ok := a.planCompaction(msgs, 1)
	if !ok {
		return st, nil
	}
	var idx []int
	for i := head; i < start; i++ {
		m := msgs[i]
		if m.Role != provider.RoleTool || provider.ContentLen(m.Content) < minPruneBytes || strings.HasPrefix(provider.ContentString(m.Content), prunedMarker) {
			continue
		}
		idx = append(idx, i)
	}
	if len(idx) == 0 {
		return st, nil
	}
	if a.archiveDir != "" {
		originals := make([]provider.Message, 0, len(idx))
		for _, i := range idx {
			originals = append(originals, msgs[i])
		}
		path, err := archiveMessages(a.archiveDir, originals)
		if err != nil {
			return st, fmt.Errorf("archive: %w", err)
		}
		st.Archive = path
	}
	next := append([]provider.Message(nil), msgs...)
	for _, i := range idx {
		m := next[i]
		placeholder := fmt.Sprintf("%s%s, %d bytes dropped to save context; re-run the tool if the data is needed again]", prunedMarker, m.Name, provider.ContentLen(m.Content))
		st.SavedChars += provider.ContentLen(m.Content) - len(placeholder)
		m.Content = placeholder
		next[i] = m
		st.Results++
	}
	a.session.Replace(next)
	a.session.IncrementRewrite()
	return st, nil
}

// SoftTrimLargeResults partially trims tool results in the prune zone that are
// larger than SoftTrimThreshold, keeping head and tail. This is a graduated
// step between "keep everything" and "full elision" — it preserves the most
// useful parts (commands/setup at top, results/errors at bottom) while saving
// context. Call this BEFORE PruneStaleToolResults for a two-pass approach:
// soft trim first, then hard prune whatever is still too large.
func (a *Agent) SoftTrimLargeResults() (PruneStats, error) {
	var st PruneStats
	if a.contextWindow <= 0 {
		return st, nil
	}
	msgs := a.session.Messages
	head, start, ok := a.planCompaction(msgs, 1)
	if !ok {
		return st, nil
	}
	next := append([]provider.Message(nil), msgs...)
	changed := false
	for i := head; i < start; i++ {
		m := msgs[i]
		if m.Role != provider.RoleTool {
			continue
		}
		content := provider.ContentString(m.Content)
		if len(content) <= SoftTrimThreshold {
			continue
		}
		if strings.HasPrefix(content, prunedMarker) || strings.Contains(content, "[... trimmed") {
			continue
		}
		trimmed := softTrimOutput(content)
		if len(trimmed) < len(content) {
			st.SavedChars += len(content) - len(trimmed)
			m.Content = trimmed
			next[i] = m
			st.Results++
			changed = true
		}
	}
	if changed {
		a.session.Replace(next)
		a.session.IncrementRewrite()
	}
	return st, nil
}

// softTrimOutput keeps the head and tail of a large output, replacing the
// middle with a marker.
func softTrimOutput(content string) string {
	if len(content) <= SoftTrimThreshold {
		return content
	}
	head := snapToRuneBoundary(content, 0, SoftTrimKeepHead)
	tail := snapToRuneBoundary(content, len(content)-SoftTrimKeepTail, len(content))
	if len(head)+len(tail) >= len(content) {
		return content
	}
	marker := fmt.Sprintf("\n\n[... trimmed — kept first and last 1.5K of %d chars ...]\n\n", len(content))
	return head + marker + tail
}
