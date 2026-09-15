package permission

import (
	"strings"

	"github.com/zzycxz/fairpeer/internal/shellsafe"
)

// isReadOnlyBashSubject returns true when a bash command is a statically
// proven read-only operation, so the auto-approve path can run it without
// prompting the user. The subject is the JSON arg value extracted by
// Subject() — for bash it is the raw command string.
//
// Classification is delegated to internal/shellsafe, the single source of
// truth shared with the workspace-writer and delivery paths. It parses the
// command with a real shell parser (mvdan.cc/sh) instead of matching first
// words, so pipelines, redirections, command substitutions, `env VAR=x cmd`
// prefixes and subcommand-specific writes (`git branch -D`, `cargo check`,
// `date -s`) all fail closed instead of being misread as readers.
func isReadOnlyBashSubject(subject string) bool {
	return shellsafe.ClassifyBash(subject).IsPermissionReader()
}

// containsShellSyntax reports whether a command uses shell operators or
// substitution — chaining/redirection/expansion can smuggle a write past a
// read-only base-word check, so any such command is treated as not read-only.
func containsShellSyntax(cmd string) bool {
	return shellsafe.ContainsShellSyntax(cmd)
}

// dangerousBashPatterns are glob-like patterns that match destructive
// commands. Used only for a UI warning — the deny list is the actual
// enforcement mechanism.
var dangerousBashPatterns = []struct {
	pattern string
	label   string
}{
	{"rm -rf*", "recursive delete"},
	{"rm -r *", "recursive delete"},
	{"rm -fr*", "recursive delete"},
	{"git push*--force*", "force push"},
	{"git push*-f*", "force push"},
	{"git reset --hard*", "hard reset"},
	{"git clean -f*", "force clean"},
	{"chmod 777*", "world-writable"},
	{"chmod -R 777*", "world-writable recursive"},
	{"chown *", "ownership change"},
	{"sudo *", "superuser"},
	{"mkfs*", "filesystem format"},
	{"dd if=*", "raw device write"},
	{"fdisk*", "partition table"},
	{"> /dev/*", "device overwrite"},
}

// BashDangerWarning returns a short label if subject matches a known
// dangerous pattern, or "" when the command looks safe. This is a visual
// hint only — the Policy rules are the authority.
func BashDangerWarning(subject string) string {
	s := strings.TrimSpace(subject)
	for _, d := range dangerousBashPatterns {
		if matchGlob(d.pattern, s) {
			return d.label
		}
	}
	return ""
}
