package netdev

// logsource.go is the structured log-read layer (B-batch log sources): the
// agent never free-hands shell to read logs — it names a LOG SOURCE
// (file:/journal:/docker:) and this layer composes ONE plain, metachar-free,
// classifier-passing command that runs through the sealed Manager.Exec path
// (guardrails → classifier → session → redact → audit → live). Client-side
// since/grep filtering keeps the seal airtight: a server-side `grep 'x|y'`
// would need shell metacharacters, which Exec structurally refuses.

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev/driver"
)

// logReadDefaults bound every log fetch. tailN is the returned line budget;
// fetchN is what may be pulled before client-side filtering (grep/since drop
// lines, so the fetch is wider than the return, but never unbounded).
const (
	logTailDefault = 100
	logTailMax     = 1000
	logFetchMax    = 2000
)

// logSinceRe accepts only journalctl-safe --since values: ISO dates/datetimes
// and relative offsets. No quotes, no metacharacters — the value is spliced
// verbatim into one command line.
var logSinceRe = regexp.MustCompile(`^(?:\d{4}-\d{2}-\d{2}(?: \d{2}:\d{2}(?::\d{2})?)?|-\d+[smhd]|yesterday|today)$`)

// logUnitRe constrains systemd unit / docker container names: one plain token.
var logUnitRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@:-]*$`)

// LogAllowedRoots returns the device's log whitelist: /var/log (the classic
// system-log root, always allowed) plus the device's log_paths entries.
func LogAllowedRoots(d config.NetDevDevice) []string {
	roots := []string{"/var/log"}
	for _, p := range d.LogPaths {
		if p = strings.TrimSpace(p); p != "" {
			roots = append(roots, p)
		}
	}
	return roots
}

// logPathAllowed reports whether p is lexically contained in one of the roots.
// Uses path (slash-based), not path/filepath: these are REMOTE Linux paths and
// must not be normalized by the local platform's separator rules.
func logPathAllowed(p string, roots []string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	clean := path.Clean(p)
	for _, r := range roots {
		cr := path.Clean(strings.TrimRight(r, "/"))
		if cr != "" && (clean == cr || strings.HasPrefix(clean, cr+"/")) {
			return true
		}
	}
	return false
}

// composeLogCommand builds the ONE plain command that fetches a log source.
// Every component is validated first, so the composed line contains no
// newlines, quotes, or shell metacharacters — it classifies as read and passes
// the shell-metachar guard unchanged.
func composeLogCommand(d config.NetDevDevice, source string, fetchN int, since string) (string, error) {
	if fetchN <= 0 {
		fetchN = logTailDefault
	}
	if fetchN > logFetchMax {
		fetchN = logFetchMax
	}
	kind, rest, ok := strings.Cut(source, ":")
	if !ok || rest == "" {
		return "", fmt.Errorf("source must be file:<abs path> | journal:<unit> | docker:<container>, got %q", source)
	}
	switch kind {
	case "file":
		rest = strings.TrimSpace(rest)
		if !logPathAllowed(rest, LogAllowedRoots(d)) {
			return "", fmt.Errorf("path %q is outside the device's log whitelist (/var/log or the device's log_paths) — add it to log_paths in 运维设置", rest)
		}
		return fmt.Sprintf("tail -n %d %s", fetchN, path.Clean(rest)), nil
	case "journal":
		if !logUnitRe.MatchString(rest) {
			return "", fmt.Errorf("invalid systemd unit %q (single plain token only)", rest)
		}
		var sb strings.Builder
		sb.WriteString("journalctl -u ")
		sb.WriteString(rest)
		if since != "" {
			if !logSinceRe.MatchString(since) {
				return "", fmt.Errorf("since must be an ISO date[-time] or a relative offset like -1h (no quotes/metacharacters), got %q", since)
			}
			sb.WriteString(" --since ")
			sb.WriteString(since)
		}
		sb.WriteString(fmt.Sprintf(" -n %d --no-pager -q", fetchN))
		return sb.String(), nil
	case "docker":
		if !logUnitRe.MatchString(rest) {
			return "", fmt.Errorf("invalid container name %q (single plain token only)", rest)
		}
		return fmt.Sprintf("docker logs --tail %d %s", fetchN, rest), nil
	case "winevt":
		// Windows 事件日志（NETDEV_SPEC_V2 §3.1）：channel 单 token；PowerShell
		// cmdlet 大小写不敏感，get- 前缀命中 windows 读表。
		if !logUnitRe.MatchString(rest) {
			return "", fmt.Errorf("invalid event channel %q (single plain token like Security)", rest)
		}
		return fmt.Sprintf("get-winevent -logname %s -maxevents %d", rest, fetchN), nil
	default:
		return "", fmt.Errorf("unknown log source kind %q (file|journal|docker|winevt)", kind)
	}
}

// LogRead fetches one log source on one configured device. The composed
// command goes through the SAME sealed Exec path as netdev_exec (budget,
// classifier, redaction, audit, live events all apply); since/grep filtering
// happens client-side on the fetched lines.
func (m *Manager) LogRead(ctx context.Context, deviceName, source string, tailN int, since, grep string) ExecResult {
	device, ok := m.cfg.NetDevDeviceByName(deviceName)
	if !ok {
		return ExecResult{Device: deviceName, Command: "log " + source, Refused: true,
			Refusal: fmt.Sprintf("device %q is not in the user-global netdev inventory (add it in 运维设置; the agent cannot add devices itself)", deviceName)}
	}
	if tailN <= 0 {
		tailN = logTailDefault
	}
	if tailN > logTailMax {
		tailN = logTailMax
	}
	// kind targets answer their own source forms through the API clients —
	// no SSH session exists for them (NETDEV_SPEC_V2 §3.1). linux hosts keep
	// the composed-command path below unchanged.
	if device.Kind == "k8s" {
		if rest, ok := strings.CutPrefix(source, "k8s:"); ok && rest != "" {
			ns, pod := rest, ""
			if i := strings.Index(rest, "/"); i >= 0 {
				ns, pod = rest[:i], rest[i+1:]
			} else {
				ns = "" // context default namespace
				pod = rest
			}
			out, err := m.KubeGet(ctx, deviceName, "podlog", ns, pod, tailN)
			if err != nil {
				return ExecResult{Device: deviceName, Command: "log " + source, Refused: true, Class: "guardrail", Refusal: err.Error()}
			}
			return ExecResult{Device: deviceName, Command: "log " + source, Class: "read", Output: out}
		}
		return ExecResult{Device: deviceName, Command: "log " + source, Refused: true, Class: "guardrail",
			Refusal: "kind=k8s targets read logs as k8s:<namespace>/<pod> (or k8s:<pod> for the context default)"}
	}
	if device.Kind == "docker" {
		if container, ok := strings.CutPrefix(source, "docker:"); ok && container != "" {
			out, err := m.DockerGet(ctx, deviceName, "logs", container, tailN)
			if err != nil {
				return ExecResult{Device: deviceName, Command: "log " + source, Refused: true, Class: "guardrail", Refusal: err.Error()}
			}
			return ExecResult{Device: deviceName, Command: "log " + source, Class: "read", Output: out}
		}
		return ExecResult{Device: deviceName, Command: "log " + source, Refused: true, Class: "guardrail",
			Refusal: "kind=docker targets read logs as docker:<container> (via the read-only Engine API)"}
	}
	fetchN := tailN
	if grep != "" || since != "" {
		// Widen the fetch so filtering has material to keep tailN lines; still
		// bounded by logFetchMax.
		fetchN = tailN * 3
		if fetchN > logFetchMax {
			fetchN = logFetchMax
		}
	}
	cmd, err := composeLogCommand(device, source, fetchN, since)
	if err != nil {
		return ExecResult{Device: deviceName, Command: "log " + source, Refused: true, Class: "guardrail", Refusal: err.Error()}
	}
	res := m.Exec(ctx, deviceName, cmd)
	if res.Refused || res.IsError {
		return res
	}
	lines := filterLogLines(strings.Split(res.Output, "\n"), since, grep, tailN)
	filtered := strings.Join(lines, "\n")
	if filtered != res.Output {
		filtered += fmt.Sprintf("\n\n[log] fetched %d lines, kept %d after since/grep filtering (tail %d)", len(strings.Split(res.Output, "\n")), len(lines), tailN)
	}
	res.Output = filtered
	return res
}

// filterLogLines applies the client-side since/grep filters and returns at
// most tailN final lines. Grep accepts a regular expression; an invalid one
// falls back to literal substring match.
func filterLogLines(lines []string, since, grep string, tailN int) []string {
	var re *regexp.Regexp
	if grep != "" {
		re, _ = regexp.Compile(grep)
	}
	now := time.Now()
	var cutoff time.Time
	if since != "" && logSinceRe.MatchString(since) {
		if strings.HasPrefix(since, "-") {
			// relative: -<n><unit>
			unit := since[len(since)-1]
			var n int
			fmt.Sscanf(since[:len(since)-1], "-%d", &n)
			switch unit {
			case 's':
				cutoff = now.Add(-time.Duration(n) * time.Second)
			case 'm':
				cutoff = now.Add(-time.Duration(n) * time.Minute)
			case 'h':
				cutoff = now.Add(-time.Duration(n) * time.Hour)
			case 'd':
				cutoff = now.AddDate(0, 0, -n)
			}
		} else {
			for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
				if t, err := time.ParseInLocation(layout, since, now.Location()); err == nil {
					cutoff = t
					break
				}
			}
		}
	}
	kept := make([]string, 0, len(lines))
	for _, ln := range lines {
		if re != nil {
			if !re.MatchString(ln) {
				continue
			}
		} else if grep != "" {
			if !strings.Contains(ln, grep) {
				continue
			}
		}
		if !cutoff.IsZero() {
			ts := extractLogTimestamp(ln)
			if !ts.IsZero() && ts.Before(cutoff) {
				continue
			}
		}
		kept = append(kept, ln)
	}
	if len(kept) > tailN {
		kept = kept[len(kept)-tailN:]
	}
	return kept
}

// extractLogTimestamp pulls a leading timestamp out of a common log line
// (syslog `Aug 27 10:00:00`, ISO `2026-08-27T10:00:00` / `2026-08-27 10:00:00`).
// Zero time = no timestamp seen; such lines always survive the since filter.
func extractLogTimestamp(ln string) time.Time {
	ln = strings.TrimSpace(ln)
	fields := strings.Fields(ln)
	if len(fields) >= 3 {
		// syslog style: "Aug 27 10:00:00 host proc..." (also journalctl's
		// short output). Year is not in the line — assume the current year.
		if t, err := time.ParseInLocation("Jan 2 15:04:05", fields[0]+" "+fields[1]+" "+fields[2], time.Local); err == nil {
			return t.AddDate(time.Now().Year()-t.Year(), 0, 0)
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04:05.000", "2006-01-02 15:04:05.000"} {
		if len(ln) >= len(layout) {
			if t, err := time.ParseInLocation(layout, ln[:len(layout)], time.Local); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

// logPathReadOverride is the classifier's log-whitelist bypass: on shell
// drivers, a tail/head/grep/wc command whose SINGLE path argument sits inside
// the device's log roots (always including /var/log) is read, even though the
// built-in tables only know the /var/log prefixes. Runs after the metachar
// guard, so the command is already one plain metachar-free line.
var logReadVerbs = map[string]bool{"tail": true, "head": true, "grep": true, "wc": true}

func logPathReadOverride(d config.NetDevDevice, drv driver.Driver, command string) (driver.Class, bool) {
	if !driver.IsShellMetacharDriver(drv.Key()) {
		return driver.Unknown, false
	}
	fields := strings.Fields(command)
	if len(fields) < 2 || !logReadVerbs[fields[0]] {
		return driver.Unknown, false
	}
	roots := LogAllowedRoots(d)
	// The path must be the FINAL token — the actual grammar of
	// `tail -n 100 PATH` and `grep [-flags] PATTERN PATH`. Between the verb
	// and it: flags (leading -), grep's single pattern word, and numeric
	// arguments (-n's count) for the tail/head/wc family.
	if last := fields[len(fields)-1]; !strings.HasPrefix(last, "/") || !logPathAllowed(last, roots) {
		return driver.Unknown, false
	}
	extra := 0
	for _, f := range fields[1 : len(fields)-1] {
		switch {
		case strings.HasPrefix(f, "-"):
			// flag
		case isAllDigits(f):
			// -n's numeric argument
		default:
			extra++ // a bare word: at most one, and only as grep's pattern
		}
	}
	if extra > 0 && (fields[0] != "grep" || extra > 1) {
		return driver.Unknown, false
	}
	return driver.Read, true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
