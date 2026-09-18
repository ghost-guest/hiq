package netdev

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// kind=k8s seal over a fake API server: whitelist paths answer, everything
// else 404; namespace allowlist and name syntax are refused client-side.
func TestKubeGetWhitelistAndNamespaceGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/version":
			_, _ = w.Write([]byte(`{"major":"1","minor":"30"}`))
		case r.URL.Path == "/api/v1/namespaces/prod/pods":
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"web-1","managedFields":[{"x":1}]}}]}`))
		case r.URL.Path == "/api/v1/namespaces/prod/pods/web-1/log":
			_, _ = w.Write([]byte("2026-08-27T10:00:00Z line1\nline2\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	kubeconfig := "apiVersion: v1\nkind: Config\ncurrent-context: prod\n" +
		"clusters:\n- name: c1\n  cluster:\n    server: " + srv.URL + "\n" +
		"contexts:\n- name: prod\n  context:\n    cluster: c1\n    user: u1\n    namespace: prod\n" +
		"users:\n- name: u1\n  user:\n    token: t0k\n"
	old := secretGetter
	secretGetter = func(kind, name string) (string, bool, error) {
		if kind == "kubeconfig" && name == "KUBE_TEST" {
			return kubeconfig, true, nil
		}
		return "", false, nil
	}
	defer func() { secretGetter = old }()

	cfg := &config.Config{}
	cfg.NetDev.Devices = []config.NetDevDevice{{
		Name: "k8s-test", Kind: "k8s",
		K8s: &config.NetDevK8sConfig{KubeconfigEnv: "KUBE_TEST", Namespaces: []string{"prod"}},
	}}
	m := NewManager(cfg)

	out, err := m.KubeGet(context.Background(), "k8s-test", "version", "", "", 0)
	if err != nil || !strings.Contains(out, `"major"`) {
		t.Fatalf("version: %v %s", err, out)
	}
	out, err = m.KubeGet(context.Background(), "k8s-test", "pods", "prod", "", 0)
	if err != nil {
		t.Fatalf("pods: %v", err)
	}
	if strings.Contains(out, "managedFields") {
		t.Fatalf("managedFields must be pruned: %s", out)
	}
	if !strings.Contains(out, "web-1") {
		t.Fatalf("pod missing from list: %s", out)
	}
	if _, err := m.KubeGet(context.Background(), "k8s-test", "podlog", "prod", "web-1", 10); err != nil {
		t.Fatalf("podlog: %v", err)
	}
	if _, err := m.KubeGet(context.Background(), "k8s-test", "pods", "kube-system", "", 0); err == nil {
		t.Fatal("namespace outside the allowlist must be refused")
	}
	if _, err := m.KubeGet(context.Background(), "k8s-test", "pod", "prod", "../../etc", 0); err == nil {
		t.Fatal("path-injection pod name must be refused")
	}
	if _, err := m.KubeGet(context.Background(), "k8s-test", "delete", "", "", 0); err == nil {
		t.Fatal("unknown verb must be refused")
	}
	if _, err := m.KubeGet(context.Background(), "no-such-device", "version", "", "", 0); err == nil {
		t.Fatal("unknown device must be refused")
	}
}

// kind=docker seal over a fake engine (tcp:// leg; npipe/unix share the same
// path-whitelist code).
func TestDockerGetWhitelistAndIDGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_ping":
			_, _ = w.Write([]byte("OK"))
		case r.URL.Path == "/containers/json":
			_, _ = w.Write([]byte(`[{"Id":"abc123","Names":["/web"],"State":"running"}]`))
		case r.URL.Path == "/containers/abc123/logs":
			_, _ = w.Write([]byte("log line\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := &config.Config{}
	cfg.NetDev.Devices = []config.NetDevDevice{
		{Name: "dock-test", Kind: "docker", Docker: &config.NetDevDockerConfig{Socket: "tcp://" + strings.TrimPrefix(srv.URL, "http://")}},
		{Name: "plain-host", Vendor: "linux"},
	}
	m := NewManager(cfg)

	out, err := m.DockerGet(context.Background(), "dock-test", "ps", "", 0)
	if err != nil || !strings.Contains(out, "abc123") {
		t.Fatalf("ps: %v %s", err, out)
	}
	if _, err := m.DockerGet(context.Background(), "dock-test", "logs", "abc123", 10); err != nil {
		t.Fatalf("logs: %v", err)
	}
	if _, err := m.DockerGet(context.Background(), "dock-test", "inspect", "../etc/passwd", 0); err == nil {
		t.Fatal("path-injection container id must be refused")
	}
	if _, err := m.DockerGet(context.Background(), "dock-test", "rm", "", 0); err == nil {
		t.Fatal("non-whitelisted verb must be refused")
	}
	if _, err := m.DockerGet(context.Background(), "plain-host", "ps", "", 0); err == nil {
		t.Fatal("non kind=docker device must be refused")
	}
}

// LogRead routes kind targets to their API clients — no SSH session involved
// (NETDEV_SPEC_V2 §3.1: k8s:/docker: sources ride the read-only APIs).
func TestLogReadRoutesKindTargetsToAPIs(t *testing.T) {
	kubeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/namespaces/prod/pods/web-1/log" {
			_, _ = w.Write([]byte("2026-08-27T10:00:00Z kube-line\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer kubeSrv.Close()
	dockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/containers/web/logs" {
			_, _ = w.Write([]byte("2026-08-27T10:00:01Z docker-line\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer dockSrv.Close()

	old := secretGetter
	secretGetter = func(kind, name string) (string, bool, error) {
		if kind == "kubeconfig" && name == "KUBE_LOG" {
			return "apiVersion: v1\nkind: Config\ncurrent-context: prod\n" +
				"clusters:\n- name: c1\n  cluster:\n    server: " + kubeSrv.URL + "\n" +
				"contexts:\n- name: prod\n  context:\n    cluster: c1\n    user: u1\n    namespace: prod\n" +
				"users:\n- name: u1\n  user:\n    token: t\n", true, nil
		}
		return "", false, nil
	}
	defer func() { secretGetter = old }()

	cfg := &config.Config{}
	cfg.NetDev.Devices = []config.NetDevDevice{
		{Name: "k8s-log", Kind: "k8s", K8s: &config.NetDevK8sConfig{KubeconfigEnv: "KUBE_LOG"}},
		{Name: "dock-log", Kind: "docker", Docker: &config.NetDevDockerConfig{Socket: "tcp://" + strings.TrimPrefix(dockSrv.URL, "http://")}},
	}
	m := NewManager(cfg)

	r := m.LogRead(context.Background(), "k8s-log", "k8s:prod/web-1", 50, "", "")
	if r.Refused || !strings.Contains(r.Output, "kube-line") {
		t.Fatalf("k8s log routing: refused=%v out=%q refusal=%q", r.Refused, r.Output, r.Refusal)
	}
	r = m.LogRead(context.Background(), "dock-log", "docker:web", 50, "", "")
	if r.Refused || !strings.Contains(r.Output, "docker-line") {
		t.Fatalf("docker log routing: refused=%v out=%q refusal=%q", r.Refused, r.Output, r.Refusal)
	}
	// Wrong source form for a kind target refuses client-side.
	if r = m.LogRead(context.Background(), "k8s-log", "file:/var/log/syslog", 50, "", ""); !r.Refused {
		t.Fatal("file: source on a kind=k8s target must be refused")
	}
}

// kind=firewall seal over a fake FortiOS: whitelist paths answer with the
// monitor envelope, everything else 404; non-whitelisted verbs refuse
// client-side; token auth rides the Bearer header.
func TestFirewallGetWhitelistAndAuth(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fw-t0k" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"http_status":401}`))
			return
		}
		switch r.URL.Path {
		case "/api/v2/monitor/system/status":
			_, _ = w.Write([]byte(`{"http_status":0,"results":{"hostname":"fgt-01","version":"7.4.5"}}`))
		case "/api/v2/monitor/firewall/conn":
			_, _ = w.Write([]byte(`{"http_status":0,"results":[{"src":"10.1.0.5","dst":"8.8.8.8"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	old := secretGetter
	secretGetter = func(kind, name string) (string, bool, error) {
		if kind == "api-token" && name == "FW_TOKEN" {
			return "fw-t0k", true, nil
		}
		return "", false, nil
	}
	defer func() { secretGetter = old }()

	cfg := &config.Config{}
	addr := strings.TrimPrefix(srv.URL, "https://")
	cfg.NetDev.Devices = []config.NetDevDevice{
		{Name: "fgw-1", Kind: "firewall", Address: addr, Fw: &config.NetDevFirewallConfig{ApiTokenEnv: "FW_TOKEN"}},
	}
	m := NewManager(cfg)

	out, err := m.FirewallGet(context.Background(), "fgw-1", "status")
	if err != nil || !strings.Contains(out, "fgt-01") {
		t.Fatalf("status: %v %s", err, out)
	}
	out, err = m.FirewallGet(context.Background(), "fgw-1", "conns")
	if err != nil || !strings.Contains(out, "8.8.8.8") {
		t.Fatalf("conns: %v %s", err, out)
	}
	if _, err := m.FirewallGet(context.Background(), "fgw-1", "delete-policy"); err == nil {
		t.Fatal("non-whitelisted verb must be refused")
	}
}
