package netdev

// dbquery_more.go — four more database engines (netdev_db_query, 常用库扩展):
// mongodb / mssql / clickhouse / elasticsearch. The seal shape per engine:
//   - mongodb:    canonical-JSON command allowlist (admin RunCommand, diag set)
//   - mssql:      exact-statement allowlist over sys.dm_* views (database/sql)
//   - clickhouse: exact-statement allowlist, executed over the HTTP GET
//                 interface — zero driver, GET-only fits the seal verbatim
//   - elasticsearch: exact-PATH allowlist (GET endpoints only)
// Every engine rides the same guardrail/audit/live/turn-budget path as the
// original three (DBQuery dispatches here).

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"

	"github.com/zzycxz/hiq/internal/config"
)

// ── MongoDB ──────────────────────────────────────────────────────────────────

// mongoCmdAllowed: the query must be a single-key (or few-key) JSON command
// that canonicalizes to EXACTLY one allowlist entry (both sides parsed then
// re-marshaled — whitespace/key order differences don't matter).
func mongoCmdAllowed(query string, allowlist []string) bool {
	q, ok := canonicalJSON(query)
	if !ok || len(q) == 0 {
		return false
	}
	for _, a := range allowlist {
		if ca, ok := canonicalJSON(a); ok && ca == q {
			return true
		}
	}
	return false
}

// canonicalJSON parses and re-marshals compact; returns ok=false on any parse
// error, non-object input, or dangerous-looking operators ($where etc. — the
// diag set never needs them).
func canonicalJSON(s string) (string, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &m); err != nil || len(m) == 0 {
		return "", false
	}
	for k, v := range m {
		if strings.HasPrefix(k, "$") {
			return "", false
		}
		if s2, ok := v.(string); ok && (strings.Contains(s2, "$") || strings.Contains(s2, ";")) {
			return "", false
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (m *Manager) mongoQuery(ctx context.Context, src config.NetDevDBSource, query string) (string, error) {
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
	uri := fmt.Sprintf("mongodb://%s:%d/?connectTimeoutMS=5000&serverSelectionTimeoutMS=5000", src.Host, dbPort(src, 27017))
	if src.Username != "" {
		uri = fmt.Sprintf("mongodb://%s:%s@%s:%d/?connectTimeoutMS=5000&serverSelectionTimeoutMS=5000&authSource=admin",
			url.QueryEscape(src.Username), url.QueryEscape(pass), src.Host, dbPort(src, 27017))
	}
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return "", err
	}
	defer client.Disconnect(context.Background())
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return "", err
	}
	var cmd bson.D
	raw, _ := canonicalJSON(query)
	if err := bson.UnmarshalExtJSON([]byte(raw), true, &cmd); err != nil {
		return "", fmt.Errorf("mongo command parse: %v", err)
	}
	var out bson.M
	dbName := src.Database
	if dbName == "" {
		dbName = "admin"
	}
	if err := client.Database(dbName).RunCommand(ctx, cmd).Decode(&out); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", out), nil
	}
	return string(b), nil
}

// ── SQL Server ───────────────────────────────────────────────────────────────

// dbMSSQLQuery reuses dbSQLQuery's capped-row rendering; only the driver/DSN
// differ. Allowlist examples: SELECT * FROM sys.dm_exec_requests, sys.databases…
func dbMSSQLDSN(src config.NetDevDBSource, password string) string {
	db := src.Database
	if db == "" {
		db = "master"
	}
	return fmt.Sprintf("sqlserver://%s:%s@%s:%d?database=%s&connection+timeout=5",
		url.QueryEscape(src.Username), url.QueryEscape(password), src.Host, dbPort(src, 1433), url.QueryEscape(db))
}

// ── ClickHouse / Elasticsearch (HTTP GET legs) ───────────────────────────────

var dbHTTPClient = &http.Client{Timeout: dbQueryTimeout}

// dbSecretPass fetches the source's password (empty ok when none configured).
func dbSecretPass(src config.NetDevDBSource) (string, error) {
	if src.PasswordEnv == "" {
		return "", nil
	}
	v, ok, err := secretGetter(SecretKindPassword, src.PasswordEnv)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("secret %s not set — add the password in 运维设置", src.PasswordEnv)
	}
	return v, nil
}

// dbHostPort renders the dial target: a Host that already carries a port
// wins (test servers / explicit ports); otherwise the default port applies.
func dbHostPort(src config.NetDevDBSource, def int) string {
	if strings.Contains(src.Host, ":") {
		return src.Host
	}
	return fmt.Sprintf("%s:%d", src.Host, dbPort(src, def))
}

// clickhouseQuery runs ONE allowlisted statement over the HTTP interface
// (GET /?query=…) — the URL-encoding makes injection structurally impossible,
// and the single-statement rule (no ';') was already enforced by the seal.
func clickhouseQuery(ctx context.Context, src config.NetDevDBSource, query string) (string, error) {
	pass, err := dbSecretPass(src)
	if err != nil {
		return "", err
	}
	v := url.Values{}
	v.Set("query", query)
	if src.Database != "" {
		v.Set("database", src.Database)
	}
	endpoint := fmt.Sprintf("http://%s/?%s", dbHostPort(src, 8123), v.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	if src.Username != "" {
		req.SetBasicAuth(src.Username, pass)
	}
	res, err := dbHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, dbQueryCellCap*dbQueryRowCap))
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("clickhouse HTTP %d: %s", res.StatusCode, truncateStr(string(body), 200))
	}
	out := string(body)
	if out == "" {
		return "(no rows)", nil
	}
	return out, nil
}

// esPathAllowed: exact or entry-prefix match on the normalized path, plus the
// tame-path guard. Entries look like /_cluster/health, /_cat/indices.
func esPathAllowed(path string, allowlist []string) bool {
	p := strings.TrimSpace(path)
	if !strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.ContainsAny(p, " `\"<>") {
		return false
	}
	if u, err := url.Parse(p); err != nil || u.Path == "" {
		return false
	}
	for _, a := range allowlist {
		na := strings.TrimSuffix(strings.TrimSpace(a), "/")
		if na == "" {
			continue
		}
		if p == na || strings.HasPrefix(p, na+"/") || strings.HasPrefix(p, na+"?") {
			return true
		}
	}
	return false
}

// esQuery GETs one allowlisted endpoint (basic auth; TLS when the port or
// scheme hints https — self-signed tolerated, same call as the firewall leg).
func esQuery(ctx context.Context, src config.NetDevDBSource, path string) (string, error) {
	pass, err := dbSecretPass(src)
	if err != nil {
		return "", err
	}
	scheme := "http"
	if dbPort(src, 9200) == 443 || strings.HasPrefix(src.Host, "https://") {
		scheme = "https"
	}
	src.Host = strings.TrimPrefix(strings.TrimPrefix(src.Host, "https://"), "http://")
	endpoint := fmt.Sprintf("%s://%s%s", scheme, dbHostPort(src, 9200), path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	if src.Username != "" {
		req.SetBasicAuth(src.Username, pass)
	}
	client := dbHTTPClient
	if scheme == "https" {
		client = &http.Client{Timeout: dbQueryTimeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec — self-signed mgmt certs
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, dbQueryCellCap*dbQueryRowCap))
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("elasticsearch HTTP %d: %s", res.StatusCode, truncateStr(string(body), 200))
	}
	return string(body), nil
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// dbMoreQuery is the new-engine dispatch body (called from DBQuery; mssql
// rides the generic dbSQLQuery path with its own DSN branch).
func dbMoreQuery(ctx context.Context, src config.NetDevDBSource, query string, m *Manager) (string, error) {
	switch src.Type {
	case "mongodb":
		return m.mongoQuery(ctx, src, query)
	case "clickhouse":
		return clickhouseQuery(ctx, src, query)
	case "elasticsearch":
		return esQuery(ctx, src, query)
	}
	return "", fmt.Errorf("unknown db type %q", src.Type)
}
