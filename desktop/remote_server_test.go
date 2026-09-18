package main

// TestServerTransportE2E attaches to a live `hiq host --listen` over TCP
// through the real serverTransport (token handshake + protocol). Opt-in via
// FP_SERVER_ADDR / FP_SERVER_TOKEN:
//
//	../scratch/hiq-host-e2e.exe host --listen 127.0.0.1:18787 --token tk123 &
//	FP_SERVER_ADDR=127.0.0.1:18787 FP_SERVER_TOKEN=tk123 \
//	  go test -run TestServerTransportE2E -count=1
//
// A wrong-token variant is asserted inline when the env points at the same host.
import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/remotehost"
)

func TestServerTransportE2E(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("FP_SERVER_ADDR"))
	token := strings.TrimSpace(os.Getenv("FP_SERVER_TOKEN"))
	if addr == "" || token == "" {
		t.Skip("FP_SERVER_ADDR / FP_SERVER_TOKEN not set")
	}

	ref := RemoteRef{Kind: "server", Target: addr}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Wrong token must fail cleanly.
	bad := &serverTransport{token: "wrong-token"}
	if _, _, _, err := bad.Dial(ctx, ref); err == nil {
		t.Fatal("dial with wrong token should fail")
	}

	tr := &serverTransport{token: token}
	stdin, stdout, proc, err := tr.Dial(ctx, ref)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	link := newRemoteHostLink(ctx, stdin, stdout, proc)
	defer link.close()

	var hello remotehost.HelloResult
	if err := link.call(ctx, "host/hello", map[string]any{}, &hello); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if hello.Version == "" {
		t.Fatal("hello missing version")
	}

	// Session survives a reconnect (shared registry across connections).
	var newRes remotehost.SessionNewResult
	if err := link.call(ctx, "session/new", remotehost.SessionNewParams{SessionID: "persist", Cwd: hello.Home}, &newRes); err != nil {
		t.Fatalf("session/new: %v", err)
	}
	link.close()

	stdin2, stdout2, proc2, err := tr.Dial(ctx, ref)
	if err != nil {
		t.Fatalf("re-Dial: %v", err)
	}
	link2 := newRemoteHostLink(ctx, stdin2, stdout2, proc2)
	defer link2.close()
	var state remotehost.SessionStateResult
	if err := link2.call(ctx, "session/state", remotehost.SessionRef{SessionID: "persist"}, &state); err != nil {
		t.Fatalf("session/state after reconnect: %v (sessions should be shared)", err)
	}
	if state.SessionPath != newRes.SessionPath {
		t.Fatalf("sessionPath after reconnect = %s, want %s", state.SessionPath, newRes.SessionPath)
	}
}

// TestServerTransportTLSE2E attaches to a TLS-enabled host with certificate
// pinning. FP_SERVER_TLS_ADDR / FP_SERVER_TLS_TOKEN gate it:
//
//	../scratch/hiq-host-e2e.exe host --listen 127.0.0.1:18788 --token tk --tls &
//	FP_SERVER_TLS_ADDR=127.0.0.1:18788 FP_SERVER_TLS_TOKEN=tk \
//	  go test -run TestServerTransportTLSE2E -count=1
func TestServerTransportTLSE2E(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("FP_SERVER_TLS_ADDR"))
	token := strings.TrimSpace(os.Getenv("FP_SERVER_TLS_TOKEN"))
	if addr == "" || token == "" {
		t.Skip("FP_SERVER_TLS_ADDR / FP_SERVER_TLS_TOKEN not set")
	}
	// Tests share one desktop config dir: isolate the pin store.
	isolateDesktopUserDirs(t)

	ref := RemoteRef{Kind: "server", Target: addr, TLS: true}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// First dial pins the certificate, second verifies it.
	tr := &serverTransport{token: token}
	stdin, stdout, proc, err := tr.Dial(ctx, ref)
	if err != nil {
		t.Fatalf("first TLS dial: %v", err)
	}
	link := newRemoteHostLink(ctx, stdin, stdout, proc)
	var hello remotehost.HelloResult
	if err := link.call(ctx, "host/hello", map[string]any{}, &hello); err != nil {
		t.Fatalf("hello over TLS: %v", err)
	}
	link.close()

	// A wrong pin must be rejected: overwrite the pin, dial again.
	if store := desktopSecretStore(); store != nil {
		_ = store.Set(serverPinKey(addr), "bogus-pin")
	}
	if _, _, _, err := (&serverTransport{token: token}).Dial(ctx, ref); err == nil {
		t.Fatal("dial with wrong certificate pin should fail")
	}
}
