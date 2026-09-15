package persistentshell

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const (
	readyToken = "REASONIX_SHELL_READY"
	// markerOverlap is how much already-scanned output is re-examined with the
	// next PTY read so a marker split across two reads is still found. It must
	// exceed the longest marker plus its status digits and terminator.
	markerOverlap = 96
	// preStartCap bounds what is retained while waiting for the start marker.
	// Only echoed wrapper source and stray output from a previous command's
	// background child can appear there, and none of it is model-visible.
	preStartCap = 64 << 10
)

func newMarkerID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", b[:])
	}
	return hex.EncodeToString(b[:])
}

func posixQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ansiCQuote renders s as a POSIX $'...' literal with no raw control bytes, so
// a multi-line command still travels as ONE physical input line. An interactive
// shell echoes PS2 for an embedded newline before it runs the buffer, which
// would put prompt bytes and wrapper source into model-visible output — and a
// PTY line discipline is not a reliable carrier for arbitrary control bytes.
func ansiCQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	b.WriteString("$'")
	for i := range len(s) {
		c := s[i]
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`\'`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 || c == 0x7f {
				// Octal escapes are the portable $'...' form; \xHH is not
				// available in every POSIX shell this package can drive.
				b.WriteString(`\`)
				b.WriteString(strconv.FormatUint(uint64(c), 8))
				continue
			}
			b.WriteByte(c)
		}
	}
	b.WriteString("'")
	return b.String()
}

func posixSetupScript() string {
	return strings.Join([]string{
		// -onlcr stops the line discipline rewriting \n as \r\n and emitting a
		// stray extra \r under output pressure, which normalisation would turn
		// into a blank line. The sanitizer covers hosts that reject it.
		"stty -echo -onlcr 2>/dev/null || stty -echo 2>/dev/null || true",
		"unset PROMPT_COMMAND",
		"PS1=",
		"PS2=",
		"PS4=",
		"set +H 2>/dev/null || true",
		"printf '%s\\n' " + posixQuote(readyToken),
	}, "; ") + "\n"
}

// posixCommandScript wraps one command. Everything is one physical line, the
// status marker is printed by its own printf (so it is found even when the
// command's own output has no trailing newline), and stdin is /dev/null so a
// command that prompts fails the same way it did under one-shot execution
// instead of blocking the session shell until the deadline.
func posixCommandScript(command, start, end string) string {
	return "printf '%s\\n' " + posixQuote(start) +
		"; eval -- " + ansiCQuote(command) + " </dev/null" +
		"; __rx_status=$?" +
		"; printf '%s%s\\n' " + posixQuote(end) + ` "$__rx_status"` + "\n"
}

// normalizePTY collapses terminal line endings. A run of carriage returns
// before a newline is one line break: the line discipline can emit \r\r\n under
// output pressure, and mapping each \r to \n would inject blank lines into
// model-visible output. A standalone \r (progress bars) still becomes a break.
func normalizePTY(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\r' {
			b.WriteByte(s[i])
			continue
		}
		for i+1 < len(s) && s[i+1] == '\r' {
			i++
		}
		if i+1 < len(s) && s[i+1] == '\n' {
			continue // the \n itself is written on the next iteration
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// readyLine reports whether the shell printed the ready token. The echoed setup
// source contains the token too, so completion requires the token to end its
// own line; the echo continues with a quote character.
func readyLine(buf string) bool {
	return strings.Contains(normalizePTY(buf), readyToken+"\n")
}

// parseStatus reads the exit status that must follow an end marker. Digits
// terminated by a newline are required, which is what stops echoed wrapper
// source from fabricating a completion.
//
// pending distinguishes "the status line has not arrived yet" from "this marker
// is not a completion". Without it, a shell tracing its own input (set -x)
// parks on a marker whose trailer never becomes digits.
func parseStatus(after string) (status int, ok bool, pending bool) {
	line, _, complete := strings.Cut(after, "\n")
	if !complete {
		return 0, false, true
	}
	n, err := strconv.Atoi(strings.TrimSuffix(line, "\r"))
	if err != nil {
		return 0, false, false
	}
	return n, true, false
}

// extractOutput finds a completed command in text. The markers are matched as
// substrings rather than whole lines: a command whose output does not end in a
// newline leaves the status marker mid-line, and requiring a line start there
// is what used to hang such a command until its deadline.
func extractOutput(buf, start, end string) (body string, code int, ok bool) {
	text := normalizePTY(buf)
	endIdx := strings.LastIndex(text, end)
	if endIdx < 0 {
		return "", 0, false
	}
	status, ok, _ := parseStatus(text[endIdx+len(end):])
	if !ok {
		return "", 0, false
	}
	body = text[:endIdx]
	if startIdx := strings.LastIndex(body, start+"\n"); startIdx >= 0 {
		body = body[startIdx+len(start)+1:]
	}
	return strings.TrimSuffix(body, "\n"), status, true
}
