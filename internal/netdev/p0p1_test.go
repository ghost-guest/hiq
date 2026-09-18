package netdev

import (
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

func TestComposeLogFollowCommand(t *testing.T) {
	d := config.NetDevDevice{Name: "srv1", Vendor: "linux", LogPaths: []string{"/opt/app/logs"}}
	ok := []struct{ source, want string }{
		{"file:/var/log/nginx/error.log", "tail -F -n 0 /var/log/nginx/error.log"},
		{"file:/opt/app/logs/app.log", "tail -F -n 0 /opt/app/logs/app.log"},
		{"journal:nginx", "journalctl -f -u nginx -n 0 --no-pager -q"},
		{"docker:web", "docker logs -f --tail 0 web"},
	}
	for _, c := range ok {
		got, err := composeLogFollowCommand(d, c.source)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q err %v", c.source, got, err)
		}
	}
	bad := []string{"file:/etc/shadow", "file:/opt/other/x.log", "journal:nginx; reboot", "docker:web;ls", "file:", "x:y"}
	for _, s := range bad {
		if _, err := composeLogFollowCommand(d, s); err == nil {
			t.Errorf("%q should be refused", s)
		}
	}
}

func TestDBQueryAllowed(t *testing.T) {
	allow := []string{
		"SHOW PROCESSLIST",
		"SHOW ENGINE INNODB STATUS",
		"SELECT * FROM information_schema.processlist",
	}
	ok := []string{
		"SHOW PROCESSLIST",
		"show  processlist ;", // normalized
		"SELECT * FROM information_schema.processlist",
		"select * from information_schema.processlist", // case-insensitive? NO — see below
	}
	bad := []string{
		"",
		"SHOW PROCESSLIST; SELECT 1",  // multi-statement
		"SHOW PROCESSLIST -- comment", // comment
		"SHOW GRANTS",                 // not in list
		"SELECT * FROM information_schema.tables", // prefix of nothing allowed
		"DROP TABLE users",                        // obviously
		"SELECT * FROM information_schema.processlist; DELETE FROM users",
	}
	for _, q := range ok {
		if !dbQueryAllowed(q, allow) {
			t.Errorf("%q should be allowed", q)
		}
	}
	for _, q := range bad {
		if dbQueryAllowed(q, allow) {
			t.Errorf("%q should be refused", q)
		}
	}
}

func TestRedisQueryAllowed(t *testing.T) {
	ok := []string{"slowlog get", "slowlog get 10", "info", "info memory", "dbsize", "client list", "latency history", "config get maxmemory"}
	bad := []string{
		"config set maxmemory 100mb", // write
		"flushdb", "flushall", "shutdown", "keys *",
		"slowlog reset", // write-ish
		"eval return 1 0",
		"get somekey", // data access, not diagnostics
	}
	for _, q := range ok {
		if !redisQueryAllowed(q) {
			t.Errorf("%q should be allowed", q)
		}
	}
	for _, q := range bad {
		if redisQueryAllowed(q) {
			t.Errorf("%q should be refused", q)
		}
	}
}
