package netdev

import (
	"regexp"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// matchLogLines is the pure half of the fan-out search: pattern matching over
// fetched lines with bounded ±context. The sealed-exec half lives in
// Manager.LogSearch and is covered by the existing logsource/session tests.
func TestMatchLogLinesRegexAndLiteral(t *testing.T) {
	lines := []string{
		"Aug 27 10:00:01 host cron[1]: (root) CMD (/usr/bin/backup)",
		"Aug 27 10:00:02 host sshd[2]: Failed password for root from 10.6.6.6",
		"Aug 27 10:00:03 host systemd[3]: Started nginx.",
		"Aug 27 10:00:04 host sshd[4]: Failed password for admin from 10.6.6.6",
	}

	// Regex path: both failed-login lines match, each with ±2 context.
	re := regexp.MustCompile(`Failed password`)
	hits := matchLogLines("vm-1", "file:/var/log/auth.log", lines, `Failed password`, re)
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	if hits[0].Device != "vm-1" || hits[0].Source != "file:/var/log/auth.log" {
		t.Fatalf("hit carries device/source: %+v", hits[0])
	}
	if len(hits[0].Context) != 4 { // lines 0..3 (hit at index 1, ±2 clamped to start)
		t.Fatalf("expected 4 context lines, got %d: %v", len(hits[0].Context), hits[0].Context)
	}

	// Literal fallback path: invalid regex → substring match.
	hits = matchLogLines("vm-1", "file:/var/log/syslog", lines, "10.6.6.6", nil)
	if len(hits) != 2 {
		t.Fatalf("literal fallback expected 2 hits, got %d", len(hits))
	}
	if hits[0].Context[0] != lines[0] {
		t.Fatalf("context keeps original line order, got %q", hits[0].Context[0])
	}

	// No match → no hits, no panic.
	if hits := matchLogLines("vm-1", "file:/var/log/syslog", lines, "nothing-matches", regexp.MustCompile(`nothing-matches`)); len(hits) != 0 {
		t.Fatalf("expected 0 hits, got %d", len(hits))
	}
}

// The hit cap must bound the report even against a pattern that matches
// everything.
func TestMatchLogLinesHitCap(t *testing.T) {
	lines := make([]string, logSearchMaxHits+20)
	for i := range lines {
		lines[i] = "Aug 27 10:00:00 host app: boom"
	}
	hits := matchLogLines("vm-1", "file:/var/log/syslog", lines, "boom", regexp.MustCompile(`boom`))
	if len(hits) > logSearchMaxHits {
		t.Fatalf("hit cap violated: %d hits", len(hits))
	}
}

// Default sources must be plain whitelisted file paths — they ride the same
// per-device log_path whitelist as any file: read.
func TestLogSearchDefaultSourcesShape(t *testing.T) {
	for _, src := range logSearchDefaultSources {
		if len(src) < 5 || src[:5] != "file:" {
			t.Fatalf("default source %q must be a file: source", src)
		}
		if !logPathAllowed(src[5:], []string{"/var/log"}) {
			t.Fatalf("default source %q must sit under /var/log", src)
		}
	}
}

// winevt: sources compose a plain, metachar-free command (the seal's
// precondition) with the channel as a single token.
func TestComposeWinevtCommand(t *testing.T) {
	cmd, err := composeLogCommand(config.NetDevDevice{Vendor: "windows"}, "winevt:Security", 200, "")
	if err != nil || cmd != "get-winevent -logname Security -maxevents 200" {
		t.Fatalf("winevt compose: %v %q", err, cmd)
	}
	for _, bad := range []string{"winevt:Security -m", "winevt:a;b", "winevt:", "winevt:../x"} {
		if _, err := composeLogCommand(config.NetDevDevice{Vendor: "windows"}, bad, 100, ""); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}
