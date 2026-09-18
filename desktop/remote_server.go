package main

// remote_server.go — the Server transport: attaches to an already-running
// `hiq host --listen <addr> --token <t>` over TCP. The same NDJSON
// JSON-RPC protocol rides the socket after a one-line token handshake. The
// token lives in the desktop secret store keyed by address; RemoteRef persists
// only the address.

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/remotehost"
)

type serverTransport struct {
	token string
}

// serverProc adapts a net.Conn to remoteProcess.
type serverProc struct{ conn net.Conn }

func (p *serverProc) Kill() error { return p.conn.Close() }
func (p *serverProc) Wait() error {
	// Nothing to wait on for a socket; block until closed by read loop end.
	_ = p.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1)
	for {
		if _, err := p.conn.Read(buf); err != nil {
			return nil
		}
	}
}

// serverPinKey derives the TLS certificate-pin key for a server address.
func serverPinKey(addr string) string {
	return serverTokenKey(addr) + "_PIN"
}

// pinnedFingerprint reads the stored TLS pin ("" = not pinned yet).
func (t *serverTransport) pinnedFingerprint(addr string) string {
	if store := desktopSecretStore(); store != nil {
		if v, ok, _ := store.Get(serverPinKey(addr)); ok {
			return v
		}
	}
	return ""
}

// serverTokenKey derives the secret-store key for a server address.
func serverTokenKey(addr string) string {
	var b strings.Builder
	b.WriteString("HIQ_REMOTE_SERVER_")
	for _, r := range strings.ToUpper(strings.TrimSpace(addr)) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	b.WriteString("_TOKEN")
	return b.String()
}

// saveServerToken persists the Server token for reconnects.
func (m *remoteHostManager) saveServerToken(addr, token string) {
	key := remoteRefKey(RemoteRef{Kind: "server", Target: addr})
	m.mu.Lock()
	if m.serverTokens == nil {
		m.serverTokens = make(map[string]string)
	}
	m.serverTokens[key] = token
	m.mu.Unlock()
	if store := desktopSecretStore(); store != nil && token != "" {
		_ = store.Set(serverTokenKey(addr), token)
	}
}

func (m *remoteHostManager) loadServerToken(addr string) string {
	key := remoteRefKey(RemoteRef{Kind: "server", Target: addr})
	m.mu.Lock()
	token := m.serverTokens[key]
	m.mu.Unlock()
	if token != "" {
		return token
	}
	if store := desktopSecretStore(); store != nil {
		if v, ok, _ := store.Get(serverTokenKey(addr)); ok {
			return v
		}
	}
	return ""
}

// Dial connects to the running server and performs the token handshake. With
// ref.TLS the connection upgrades to TLS: the self-signed leaf certificate is
// fingerprint-pinned in the secret store on first connect and must match ever
// after (a regenerated server certificate is an identity change — clear the
// pin via ServerForget and re-confirm).
func (t *serverTransport) Dial(ctx context.Context, ref RemoteRef) (io.Reader, io.Writer, remoteProcess, error) {
	addr := strings.TrimSpace(ref.Target)
	if addr == "" {
		return nil, nil, nil, fmt.Errorf("server: address is required")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = addr + ":8787"
	}
	token := t.token
	if token == "" {
		return nil, nil, nil, fmt.Errorf("server: missing token")
	}
	var conn net.Conn
	if ref.TLS {
		pinned := t.pinnedFingerprint(addr)
		var presented string
		tlsConn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, &tls.Config{
			// Self-signed server cert: identity comes from the fingerprint pin,
			// not a CA chain.
			InsecureSkipVerify: true,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if cs.PeerCertificates == nil || len(cs.PeerCertificates) == 0 {
					return fmt.Errorf("server: no certificate presented")
				}
				presented = sha256FingerprintHex(cs.PeerCertificates[0].Raw)
				return nil
			},
		})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("server: tls: %w", err)
		}
		if pinned != "" && pinned != presented {
			tlsConn.Close()
			return nil, nil, nil, fmt.Errorf("server: certificate fingerprint changed (pinned %s, got %s) — server identity changed; run ServerForget and re-confirm", shortFP(pinned), shortFP(presented))
		}
		if pinned == "" && presented != "" {
			if store := desktopSecretStore(); store != nil {
				_ = store.Set(serverPinKey(addr), presented)
			}
		}
		conn = tlsConn
	} else {
		d := net.Dialer{Timeout: 15 * time.Second}
		var err error
		conn, err = d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("server: %w", err)
		}
	}
	auth, _ := json.Marshal(remotehost.AuthParams{Token: token})
	if _, err := conn.Write(append(auth, '\n')); err != nil {
		conn.Close()
		return nil, nil, nil, fmt.Errorf("server: handshake: %w", err)
	}
	// Probe with a real request before wiring the link, so a wrong token (the
	// server just closes) surfaces as a clean error here. The consumed reply is
	// ours; the link starts with a clean reader.
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		conn.Close()
		return nil, nil, nil, err
	}
	helloReq, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "host/hello", "params": map[string]any{}})
	if _, err := conn.Write(append(helloReq, '\n')); err != nil {
		conn.Close()
		return nil, nil, nil, fmt.Errorf("server: hello: %w", err)
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil || !strings.Contains(line, `"version"`) {
		conn.Close()
		return nil, nil, nil, fmt.Errorf("server: rejected handshake (wrong token or not a hiq host)")
	}
	_ = conn.SetReadDeadline(time.Time{})
	return br, conn, &serverProc{conn: conn}, nil
}


// shortFP renders a fingerprint prefix for error messages.
func shortFP(fp string) string {
	if len(fp) <= 12 {
		return fp
	}
	return fp[:12] + "…"
}
