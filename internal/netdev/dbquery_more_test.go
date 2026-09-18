package netdev

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// The four new engines' seals: allowlist shapes + the HTTP legs over fake
// servers. mongo/mssql wire protocols can't be faked here — their allowlist
// gates are pure functions and still covered.
func TestMongoCmdAllowedCanonical(t *testing.T) {
	al := []string{`{"serverStatus": 1}`, `{"dbStats":1}`}
	if !mongoCmdAllowed(`{"serverStatus":1}`, al) {
		t.Fatal("whitespace/order differences must canonicalize to a match")
	}
	if !mongoCmdAllowed(`{ "dbStats" : 1 }`, al) {
		t.Fatal("canonical JSON must ignore formatting")
	}
	if mongoCmdAllowed(`{"serverStatus":1, "extra":2}`, al) {
		t.Fatal("extra keys must NOT match")
	}
	if mongoCmdAllowed(`{"$where": "x"}`, al) {
		t.Fatal("$-operators must be refused structurally")
	}
	if mongoCmdAllowed(`dropDatabase`, al) {
		t.Fatal("non-JSON input must be refused")
	}
}

func TestESPathAllowed(t *testing.T) {
	al := []string{"/_cluster/health", "/_cat/indices"}
	for _, ok := range []string{"/_cluster/health", "/_cat/indices?v", "/_cat/indices/graylog"} {
		if !esPathAllowed(ok, al) {
			t.Fatalf("%q should be allowed", ok)
		}
	}
	for _, bad := range []string{"/_search", "/../etc", "_cluster/health", "/_cluster/health?x=../..", "/_cat/indices?x=`id`"} {
		if esPathAllowed(bad, al) {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

func TestClickHouseAndESHTTPLegs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/" && r.URL.Query().Get("query") == "SHOW PROCESSLIST" {
			_, _ = w.Write([]byte("1\tquery\n"))
			return
		}
		if r.URL.Path == "/_cluster/health" {
			_, _ = w.Write([]byte(`{"status":"green"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	cfg := &config.Config{}
	cfg.NetDev.DBSources = []config.NetDevDBSource{
		{Name: "ch1", Type: "clickhouse", Host: addr, Allowlist: []string{"SHOW PROCESSLIST"}},
		{Name: "es1", Type: "elasticsearch", Host: addr, Allowlist: []string{"/_cluster/health"}},
	}
	m := NewManager(cfg)

	out, err := m.DBQuery(context.Background(), "ch1", "SHOW PROCESSLIST")
	if err != nil || !strings.Contains(out, "query") {
		t.Fatalf("clickhouse: %v %q", err, out)
	}
	out, err = m.DBQuery(context.Background(), "ch1", "DROP TABLE x")
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("clickhouse non-allowlisted must refuse: %v", err)
	}
	out, err = m.DBQuery(context.Background(), "es1", "/_cluster/health")
	if err != nil || !strings.Contains(out, "green") {
		t.Fatalf("es: %v %q", err, out)
	}
	if _, err = m.DBQuery(context.Background(), "es1", "/_search?q=*"); err == nil {
		t.Fatal("es endpoint outside allowlist must refuse")
	}
}
