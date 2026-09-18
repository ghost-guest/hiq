package netdev

import (
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev/driver"
)

// ── redact: server-log / app / database patterns ─────────────────────────────

func TestRedactServerLogSecrets(t *testing.T) {
	cases := []struct{ name, in string }{
		{"env password", "DB_PASSWORD=hunter2 restart=always"},
		{"env export", "  export MYSQL_PWD=s3cret!"},
		{"env api key", "OPENAI_API_KEY=sk-abcdef0123456789abcdef"},
		{"yaml password", "database:\n  password: Sup3rPass!"},
		{"mysql conn string", `2026-08-27T10:00:00 app connect mysql://root:P@ssw0rd@10.0.0.50:3306/db failed`},
		{"postgres conn string", `conn=postgres://netops:pw123@db1.internal:5432/prod`},
		{"http basic auth", "curl fetched https://admin:admin123@internal.example.com/api"},
		{"authorization header", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.SflKxwRJSMeKKF2QT4"},
		{"bearer inline", "got bearer eyJhbGciOiJub25lIn0.eyJhIjoxfQ.sig12345678 from client"},
		{"jwt alone", "token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjMifQ.abcDEF123ghi"},
		{"openai key", `error calling api key "sk-proj-0123456789abcdefgh"`},
		{"aws key", "credentials AKIAIOSFODNN7EXAMPLE rejected"},
		{"github token", "using ghp_0123456789abcdefghijklmnopqrstuvwxyz"},
		{"slack token", "xoxb-1234567890-abcdef auth failed"},
		{"sql identified by", "CREATE USER 'app'@'%' IDENTIFIED BY 'S3cretPw';"},
		{"sql set password", "SET PASSWORD FOR 'app'@'%' = 'NewPass123';"},
		{"sql password literal", "WHERE password='plainPW' AND active=1"},
		{"redis auth", "1:M 10:00:00.000 # AUTH myRedisToken123"},
	}
	for _, c := range cases {
		out := Redact(c.in)
		if out == c.in {
			t.Errorf("%s: not redacted: %q", c.name, out)
		}
		if strings.Contains(out, "hunter2") || strings.Contains(out, "P@ssw0rd") ||
			strings.Contains(out, "Sup3rPass!") || strings.Contains(out, "admin123") ||
			strings.Contains(out, "S3cretPw") || strings.Contains(out, "plainPW") ||
			strings.Contains(out, "myRedisToken123") || strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
			t.Errorf("%s: secret survived: %q", c.name, out)
		}
		if !strings.Contains(out, "<redacted>") {
			t.Errorf("%s: no redacted token in %q", c.name, out)
		}
	}
}

// URL credentials keep scheme://user visible (the target stays readable).
func TestRedactConnStringKeepsTarget(t *testing.T) {
	out := Redact("mysql://root:P@ss@10.0.0.50:3306/db")
	// NOTE: `@` inside the password terminates the password group early by
	// design (URL syntax) — the tail after the last @ must survive.
	if !strings.Contains(out, "mysql://root:") {
		t.Errorf("scheme/user lost: %q", out)
	}
	if !strings.Contains(out, "10.0.0.50:3306/db") {
		t.Errorf("host lost: %q", out)
	}
}

// Normal log lines must pass through untouched (no over-redaction).
func TestRedactLeavesPlainLogLines(t *testing.T) {
	plain := strings.Join([]string{
		"2026-08-27 10:00:01 nginx-error: connect() failed (111: Connection refused) while connecting to upstream",
		"Aug 27 10:00:02 web01 systemd[1]: Started nginx.",
		"Query_time: 12.3 Lock_time: 0.001 Rows_sent: 1 Rows_examined: 999999",
		"auth.log: Accepted publickey for deploy from 10.0.0.9 port 51234 ssh2",
		"# Time: 2026-08-27T10:00:03.000000Z",
	}, "\n")
	if out := Redact(plain); out != plain {
		t.Errorf("plain log lines altered:\n%s", out)
	}
}

// ── logsource: command composition & path whitelist ──────────────────────────

func devWithLogPaths(paths ...string) config.NetDevDevice {
	return config.NetDevDevice{Name: "srv1", Vendor: "linux", OS: "ubuntu", Address: "10.0.0.5", LogPaths: paths}
}

func TestComposeLogCommand(t *testing.T) {
	d := devWithLogPaths("/opt/app/logs")
	cases := []struct {
		source, since string
		fetchN        int
		want          string
	}{
		{"file:/var/log/nginx/error.log", "", 100, "tail -n 100 /var/log/nginx/error.log"},
		{"file:/opt/app/logs/app.log", "", 50, "tail -n 50 /opt/app/logs/app.log"},
		{"journal:nginx", "", 100, "journalctl -u nginx -n 100 --no-pager -q"},
		{"journal:mysql", "-1h", 200, "journalctl -u mysql --since -1h -n 200 --no-pager -q"},
		{"docker:web", "", 100, "docker logs --tail 100 web"},
	}
	for _, c := range cases {
		got, err := composeLogCommand(d, c.source, c.fetchN, c.since)
		if err != nil {
			t.Fatalf("%s: %v", c.source, err)
		}
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.source, got, c.want)
		}
		if strings.ContainsAny(got, ";|&`$()<>\"'") {
			t.Errorf("%s: composed command has metacharacters: %q", c.source, got)
		}
	}
}

func TestComposeLogCommandRefusals(t *testing.T) {
	d := devWithLogPaths("/opt/app/logs")
	for _, bad := range []struct{ source, since string }{
		{"file:/etc/shadow", ""},                // outside whitelist
		{"file:/opt/other/x.log", ""},           // outside this device's roots
		{"file:/opt/app/logs/../../etc/x", ""},  // traversal
		{"journal:nginx; reboot", ""},           // unit with metachars
		{"docker:web; rm -rf /", ""},            // container with metachars
		{"journal:nginx", `--since "; reboot"`}, // since with quotes/metachars
		{"journal:nginx", "yesterday; ls"},      // since trailing junk
		{"whatever:x", ""},                      // unknown kind
		{"file:", ""},                           // empty
	} {
		if _, err := composeLogCommand(d, bad.source, 100, bad.since); err == nil {
			t.Errorf("expected refusal for %q since=%q", bad.source, bad.since)
		}
	}
}

func TestLogPathAllowed(t *testing.T) {
	roots := LogAllowedRoots(devWithLogPaths("/opt/app/logs"))
	ok := []string{"/var/log/syslog", "/var/log/nginx/error.log", "/opt/app/logs/app.log", "/opt/app/logs/sub/x.log"}
	bad := []string{"/etc/passwd", "/opt/app/logs2/x", "/opt/app", "var/log/x", "/opt/app/logs/../etc/x", "relative.log"}
	for _, p := range ok {
		if !logPathAllowed(p, roots) {
			t.Errorf("%s should be allowed", p)
		}
	}
	for _, p := range bad {
		if logPathAllowed(p, roots) {
			t.Errorf("%s should be refused", p)
		}
	}
}

func TestLogPathReadOverride(t *testing.T) {
	drv, _ := driver.For("linux", "")
	d := devWithLogPaths("/opt/app/logs", "/usr/local/tomcat/logs")
	ok := []string{
		"tail -n 100 /opt/app/logs/app.log",
		"tail -f /usr/local/tomcat/logs/catalina.out",
		"head -n 20 /var/log/nginx/access.log",
		"grep -i error /opt/app/logs/app.log",
		"wc -l /opt/app/logs/app.log",
	}
	for _, cmd := range ok {
		if c, yes := logPathReadOverride(d, drv, cmd); !yes || c != driver.Read {
			t.Errorf("%q should override to read", cmd)
		}
	}
	bad := []string{
		"cat /opt/app/logs/app.log",          // verb outside the log bypass
		"tail -n 100 /etc/shadow",            // outside roots
		"grep /var/log/x error",              // two path-shaped tokens
		"tail -n 100 /opt/app/logs/a /etc/b", // two paths, one not allowed
		"tail -n 100",                        // no path
		"systemctl restart nginx",            // write verb
	}
	for _, cmd := range bad {
		if _, yes := logPathReadOverride(d, drv, cmd); yes {
			t.Errorf("%q should not override", cmd)
		}
	}
	// Network-CLI drivers never get the shell bypass.
	hw, _ := driver.For("huawei", "")
	if _, yes := logPathReadOverride(d, hw, "tail -n 5 /var/log/x"); yes {
		t.Error("non-shell driver must not get the log-path override")
	}
}

func TestFilterLogLines(t *testing.T) {
	lines := []string{
		"2026-08-27T09:00:00 old line",
		"2026-08-27T10:00:00 error connecting to db",
		"2026-08-27T10:00:01 all good",
		"2026-08-27T10:00:02 error retrying",
		"no timestamp here",
	}
	// grep filter, regex.
	got := filterLogLines(lines, "", `error`, 10)
	if len(got) != 2 {
		t.Errorf("grep: got %v", got)
	}
	// tail clamp.
	got = filterLogLines(lines, "", "", 2)
	if len(got) != 2 || got[0] != "2026-08-27T10:00:02 error retrying" {
		t.Errorf("tail: got %v", got)
	}
	// invalid regex falls back to substring.
	got = filterLogLines(lines, "", "error (", 10)
	if len(got) != 0 {
		t.Errorf("invalid regex literal not present: got %v", got)
	}
	nginx := []string{"connect() failed (111: Connection refused) while connecting to upstream"}
	if got = filterLogLines(nginx, "", "failed (111", 10); len(got) != 1 {
		t.Errorf("literal fallback: got %v", got)
	}
}
