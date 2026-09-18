package memory

import (
	"os"

	"github.com/zzycxz/fairpeer/internal/pii"
)

// Shared-memory writes go through this one function so that the redaction rule
// cannot be forgotten at a new call site.
//
// Why at the write and not at the read: memory files are plain text in a
// directory the user is told to copy, back up and bundle. A secret that reaches
// disk is inherited by every later reader — a teammate, an export, a backup —
// and there is no second chance to take it back. Masking here costs one regex
// pass and makes "the model summarised a conversation about an integration" stop
// meaning "the key is now in a file".
//
// The masking is a backstop, not a guarantee: text written in prose is not
// detectable by pattern. See internal/pii for what is and is not covered.
func writeMemoryFile(path, text string) error {
	if scrubbed, found := pii.Scrub(text); len(found) > 0 {
		text = scrubbed
	}
	return os.WriteFile(path, []byte(text), 0o644)
}
