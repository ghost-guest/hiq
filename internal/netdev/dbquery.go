package netdev

// dbquery.go — read-only database diagnostics (netdev_db_query). The seal has
// two layers, by design: (1) the account itself must be least-privilege — the
// DB's own grants are the structural boundary no client can bypass; (2) the
// per-source allowlist is EXACT-STATEMENT-PREFIX, matched on a normalized
// single statement (no wildcards, no table patterns, no multi-statement, no
// comments). Anything not on the list refuses before a socket opens.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	"github.com/redis/go-redis/v9"

	"github.com/zzycxz/hiq/internal/config"
)

// dbQueryRowCap / dbQueryCellCap bound one result set.
const (
	dbQueryRowCap  = 50
	dbQueryCellCap = 4096
	dbQueryTimeout = 10 * time.Second
)

// dbNormalize collapses whitespace and strips one trailing semicolon — the
// form allowlist entries and queries are both compared in.
func dbNormalize(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	return strings.TrimSuffix(q, ";")
}

// dbQueryAllowed: single plain statement (no ';', no comment syntax) whose
// normalized form equals, or word-boundary-prefixes, a normalized allowlist
// entry.
func dbQueryAllowed(query string, allowlist []string) bool {
	n := dbNormalize(query)
	if n == "" || strings.Contains(n, ";") || strings.Contains(n, "--") || strings.Contains(n, "/*") {
		return false
	}
	n = strings.ToLower(n)
	for _, a := range allowlist {
		na := strings.ToLower(dbNormalize(a))
		if na == "" {
			continue
		}
		if n == na {
			return true
		}
		if strings.HasPrefix(n, na+" ") {
			return true
		}
	}
	return false
}

// redisCmdAllowed is the redis flavor of the same seal: exact command +
// argument shape. Only DIAGNOSTIC commands are accepted, hardcoded — redis has
// no query language to allowlist against.
var redisAllowed = []struct {
	cmd  string
	args int // exact arg count (-1 = any)
}{
	{"slowlog", -1}, // SLOWLOG GET [n]
	{"info", -1},    // INFO [section]
	{"dbsize", 0},
	{"client", -1},  // CLIENT LIST / CLIENT INFO
	{"latency", -1}, // LATENCY HISTORY/STATUS/DOCTOR
	{"config", -1},  // CONFIG GET (writes like CONFIG SET land in the write table below)
	{"lastsave", 0},
	{"time", 0},
}

var redisDenied = regexp.MustCompile(`(?i)^(config\s+(set|resetstat|rename)|flushdb|flushall|shutdown|bgsave|bgrewriteaof|swapdb|replicaof|slaveof|script|eval|migrate|restore|dump)\b`)

// DBQuery runs ONE allowlisted read-only statement against a configured
// [[netdev.db_sources]] entry. Output is JSON lines, redacted, row-capped.
func (m *Manager) DBQuery(ctx context.Context, sourceName, query string) (string, error) {
	src, ok := m.dbSourceByName(sourceName)
	if !ok {
		return "", fmt.Errorf("db_source %q is not configured (add it in 运维设置 — the agent cannot add sources)", sourceName)
	}
	label := "db " + src.Type + " " + dbNormalize(query)
	if src.Type == "redis" {
		if !redisQueryAllowed(query) {
			m.dbAuditRefuse(src, query, "not in the redis diagnostic command set")
			return "", fmt.Errorf("netdev_db_query: %q is not an allowlisted redis diagnostic command", dbNormalize(query))
		}
	} else if src.Type == "mongodb" {
		if !mongoCmdAllowed(query, src.Allowlist) {
			m.dbAuditRefuse(src, query, "not in the source's canonical-JSON command allowlist")
			return "", fmt.Errorf("netdev_db_query: %q is not in source %q's allowlist (canonical JSON commands like {\"serverStatus\":1})", strings.TrimSpace(query), src.Name)
		}
	} else if src.Type == "elasticsearch" {
		if !esPathAllowed(query, src.Allowlist) {
			m.dbAuditRefuse(src, query, "not in the source's endpoint-path allowlist")
			return "", fmt.Errorf("netdev_db_query: %q is not in source %q's allowlist (GET endpoint paths like /_cluster/health)", strings.TrimSpace(query), src.Name)
		}
	} else if !dbQueryAllowed(query, src.Allowlist) {
		m.dbAuditRefuse(src, query, "not in the source's exact-statement allowlist")
		return "", fmt.Errorf("netdev_db_query: %q is not in source %q's allowlist (exact statements only — adjust [[netdev.db_sources]] in 运维设置 if this is a legitimate read)", dbNormalize(query), src.Name)
	}

	if r, allow := m.guardrailCheck("(db:"+src.Name+")", label); !allow {
		return r.Refusal, nil
	}

	ctx, cancel := context.WithTimeout(ctx, dbQueryTimeout)
	defer cancel()

	// Via: 本地转发穿跳板链（dbtunnel.go）——生产库在堡垒后面的正解。
	if len(src.Via) > 0 {
		t, closer, terr := m.dbTunnel(ctx, src)
		if terr != nil {
			m.dbAudit(src, query, AuditFailure, terr)
			return "", fmt.Errorf("netdev_db_query: %w", terr)
		}
		defer closer()
		src = t
	}

	start := m.liveCmdStart("(db:"+src.Name+")", label, "read")
	status := AuditFailure
	defer func() { m.liveCmdEnd("(db:"+src.Name+")", label, "read", status, start, 0, "") }()

	var out string
	var err error
	switch src.Type {
	case "mysql", "postgres", "mssql":
		out, err = dbSQLQuery(ctx, src, dbNormalize(query))
	case "redis":
		out, err = m.redisQuery(ctx, src, dbNormalize(query))
	case "mongodb", "clickhouse", "elasticsearch":
		out, err = dbMoreQuery(ctx, src, dbNormalize(query), m)
	default:
		err = fmt.Errorf("unknown db type %q", src.Type)
	}
	if err != nil {
		m.dbAudit(src, query, AuditFailure, err)
		return "", fmt.Errorf("netdev_db_query: %w", err)
	}
	m.turnSpend()
	red, n := RedactCounted(out)
	m.dbAudit(src, query, AuditOK, nil)
	status = AuditOK
	if n > 0 {
		red += fmt.Sprintf("\n[安全提醒] %d 处敏感字段已脱敏。", n)
	}
	return red, nil
}

func (m *Manager) dbSourceByName(name string) (config.NetDevDBSource, bool) {
	for _, s := range m.cfg.NetDev.DBSources {
		if s.Name == name {
			return s, true
		}
	}
	return config.NetDevDBSource{}, false
}

func (m *Manager) dbAudit(src config.NetDevDBSource, query, st string, err error) {
	e := Audit{Device: "(db:" + src.Name + ")", Command: "db " + src.Type + " " + dbNormalize(query), Class: "read", Status: st}
	if err != nil {
		e.Error = err.Error()
	}
	_ = AppendAudit(e)
}

func (m *Manager) dbAuditRefuse(src config.NetDevDBSource, query, why string) {
	_ = AppendAudit(Audit{Device: "(db:" + src.Name + ")", Command: "db " + src.Type + " " + dbNormalize(query), Class: "guardrail", Status: AuditRefused})
	m.liveCmdRefused("(db:"+src.Name+")", "db "+dbNormalize(query), "guardrail", why)
}

func dbDSN(src config.NetDevDBSource, password string) string {
	switch src.Type {
	case "mysql":
		db := src.Database
		if db == "" {
			db = "information_schema"
		}
		return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?timeout=5s&readTimeout=8s&charset=utf8mb4", src.Username, password, src.Host, dbPort(src, 3306), db)
	case "mssql":
		db := src.Database
		if db == "" {
			db = "master"
		}
		return fmt.Sprintf("sqlserver://%s:%s@%s:%d?database=%s&connection+timeout=5",
			url.QueryEscape(src.Username), url.QueryEscape(password), src.Host, dbPort(src, 1433), url.QueryEscape(db))
	default: // postgres
		db := src.Database
		if db == "" {
			db = "postgres"
		}
		return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=prefer connect_timeout=5", src.Host, dbPort(src, 5432), src.Username, password, db)
	}
}

func dbPort(src config.NetDevDBSource, def int) int {
	if src.Port > 0 {
		return src.Port
	}
	return def
}

// dbSQLQuery executes the allowlisted statement and renders capped JSON rows.
func dbSQLQuery(ctx context.Context, src config.NetDevDBSource, query string) (string, error) {
	pass, ok, err := secretGetter(SecretKindPassword, src.PasswordEnv)
	if err != nil || !ok {
		return "", fmt.Errorf("secret %s not set — add the password in 运维设置 (secret store, never TOML)", src.PasswordEnv)
	}
	driverName := "pgx"
	if src.Type == "mysql" {
		driverName = "mysql"
	} else if src.Type == "mssql" {
		driverName = "sqlserver"
	}
	db, err := sql.Open(driverName, dbDSN(src, pass))
	if err != nil {
		return "", err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	var sb strings.Builder
	n := 0
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return "", err
		}
		if n++; n > dbQueryRowCap {
			sb.WriteString(fmt.Sprintf("…（已达单次上限 %d 行）\n", dbQueryRowCap))
			break
		}
		row := map[string]string{}
		for i, c := range cols {
			v := ""
			switch x := vals[i].(type) {
			case []byte:
				v = string(x)
			case nil:
				v = ""
			default:
				v = fmt.Sprintf("%v", x)
			}
			if len(v) > dbQueryCellCap {
				v = v[:dbQueryCellCap] + "…"
			}
			row[c] = v
		}
		b, _ := json.Marshal(row)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if sb.Len() == 0 {
		return "(no rows)", nil
	}
	return sb.String(), rows.Err()
}

// redisQueryAllowed: exact diagnostic-command shapes only.
func redisQueryAllowed(query string) bool {
	n := dbNormalize(query)
	if n == "" || redisDenied.MatchString(n) {
		return false
	}
	fields := strings.Fields(strings.ToLower(n))
	for _, a := range redisAllowed {
		if fields[0] != a.cmd {
			continue
		}
		rest := len(fields) - 1
		if a.args == -1 || rest == a.args {
			// SLOWLOG's single arg must be GET.
			if a.cmd == "slowlog" && fields[1] != "get" {
				return false
			}
			// CONFIG's second word must be GET.
			if a.cmd == "config" && (len(fields) < 2 || fields[1] != "get") {
				return false
			}
			// CLIENT's second word must be LIST/INFO.
			if a.cmd == "client" && (len(fields) < 2 || (fields[1] != "list" && fields[1] != "info")) {
				return false
			}
			// LATENCY's second word must be read-only.
			if a.cmd == "latency" && (len(fields) < 2 || (fields[1] != "history" && fields[1] != "status" && fields[1] != "doctor")) {
				return false
			}
			return true
		}
	}
	return false
}

// redisQuery runs one allowlisted diagnostic command and renders plain lines.
func (m *Manager) redisQuery(ctx context.Context, src config.NetDevDBSource, n string) (string, error) {
	pass := ""
	if src.PasswordEnv != "" {
		v, ok, err := secretGetter(SecretKindPassword, src.PasswordEnv)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("secret %s not set — add the password in 运维设置", src.PasswordEnv)
		}
		pass = v
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", src.Host, dbPort(src, 6379)),
		Username: src.Username,
		Password: pass,
		DB:       0,
	})
	defer rdb.Close()
	fields := strings.Fields(strings.ToLower(n))
	cmdName := fields[0]
	var out any
	var err error
	switch cmdName {
	case "slowlog":
		cmdArgs := []any{"SLOWLOG", "GET"}
		if len(fields) == 3 {
			cmdArgs = append(cmdArgs, fields[2])
		}
		out, err = rdb.Do(ctx, cmdArgs...).Result()
	case "dbsize":
		out, err = rdb.Do(ctx, "DBSIZE").Result()
	case "info":
		cmdArgs := []any{"INFO"}
		if len(fields) == 2 {
			cmdArgs = append(cmdArgs, strings.ToUpper(fields[1]))
		}
		out, err = rdb.Do(ctx, cmdArgs...).Result()
	default:
		upper := strings.Fields(strings.ToUpper(n))
		cmdArgs := make([]any, len(upper))
		for i, s := range upper {
			cmdArgs[i] = s
		}
		out, err = rdb.Do(ctx, cmdArgs...).Result()
	}
	if err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", out), nil
	}
	return string(b), nil
}
