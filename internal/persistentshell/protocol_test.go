package persistentshell

import (
	"strings"
	"testing"
)

func TestExtractOutputIgnoresEchoedScript(t *testing.T) {
	start := "REASONIX_START_abc"
	end := "REASONIX_END_abc:"
	raw := posixCommandScript("pwd", start, end) +
		start + "\n" +
		"/tmp/work\n" +
		end + "0\n"
	body, code, ok := extractOutput(raw, start, end)
	if !ok {
		t.Fatal("expected completed extraction")
	}
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if body != "/tmp/work" {
		t.Fatalf("body=%q", body)
	}
}

// The echoed wrapper source contains both markers. Completion must require the
// status digits that only the executed printf can produce.
func TestEchoedScriptAloneIsNotCompletion(t *testing.T) {
	start := "REASONIX_START_abc"
	end := "REASONIX_END_abc:"
	if _, _, ok := extractOutput(posixCommandScript("pwd", start, end), start, end); ok {
		t.Fatal("echoed wrapper source must not complete a command")
	}
	if readyLine(posixSetupScript()) {
		t.Fatal("echoed setup source must not report readiness")
	}
}

func TestExtractOutputCRLF(t *testing.T) {
	start := "REASONIX_START_x"
	end := "REASONIX_END_x:"
	raw := start + "\r\nhello\r\n" + end + "7\r\n"
	body, code, ok := extractOutput(raw, start, end)
	if !ok || code != 7 || body != "hello" {
		t.Fatalf("body=%q code=%d ok=%v", body, code, ok)
	}
}

// A command whose output has no trailing newline leaves the status marker
// mid-line. Requiring a line start there hung every such command until its
// deadline (printf without \n, echo -n, cat of a file with no final newline).
func TestExtractOutputWithoutTrailingNewline(t *testing.T) {
	start := "REASONIX_START_y"
	end := "REASONIX_END_y:"
	raw := start + "\nhi" + end + "0\n"
	body, code, ok := extractOutput(raw, start, end)
	if !ok || code != 0 || body != "hi" {
		t.Fatalf("body=%q code=%d ok=%v", body, code, ok)
	}
}

func TestReadyLine(t *testing.T) {
	if !readyLine("noise\n" + readyToken + "\nmore") {
		t.Fatal("ready token not detected")
	}
	if readyLine("printf '%s\\n' '" + readyToken + "'\n") {
		t.Fatal("quoted token in echoed source must not count as ready")
	}
}

func TestPosixQuote(t *testing.T) {
	if got := posixQuote("it's"); got != `'it'\''s'` {
		t.Fatalf("got %q", got)
	}
}

// A multi-line command has to reach the shell as one physical input line, or an
// interactive shell prints PS2 and the wrapper's own source leaks into output.
func TestAnsiCQuoteKeepsOnePhysicalLine(t *testing.T) {
	script := posixCommandScript("cat <<'EOF'\nline\nEOF", "S", "E:")
	if strings.Count(script, "\n") != 1 || !strings.HasSuffix(script, "\n") {
		t.Fatalf("wrapper must be one line, got %q", script)
	}
	if got := ansiCQuote("a'b\nc\\d\te"); got != `$'a\'b\nc\\d\te'` {
		t.Fatalf("quote=%q", got)
	}
	if got := ansiCQuote("\x01"); got != `$'\1'` {
		t.Fatalf("control quote=%q", got)
	}
}

// The command's stdin is /dev/null, matching one-shot execution: a command that
// prompts fails immediately instead of blocking the session shell.
func TestCommandScriptClosesStdin(t *testing.T) {
	if !strings.Contains(posixCommandScript("read x", "S", "E:"), "</dev/null") {
		t.Fatal("wrapper must detach stdin")
	}
}

// The line discipline can emit \r\r\n under output pressure. Mapping every \r
// to \n injected blank lines into model-visible output.
func TestNormalizePTYCollapsesCarriageReturnRuns(t *testing.T) {
	cases := map[string]string{
		"a\r\nb":     "a\nb",
		"a\r\r\nb":   "a\nb",
		"a\r\r\r\nb": "a\nb",
		"a\rb":       "a\nb",
		"plain":      "plain",
	}
	for in, want := range cases {
		if got := normalizePTY(in); got != want {
			t.Fatalf("normalizePTY(%q)=%q want %q", in, got, want)
		}
	}
}
