package installsource

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/zzycxz/hiq/internal/netclient"
)

// ssrfGuardClient wraps base so every fetch refuses to connect to private,
// link-local, CGNAT, or unspecified addresses — the SSRF surface a prompt-
// injected install source would aim at (cloud metadata at 169.254.169.254,
// RFC1918 internal services). Loopback is allowed: the agent can already reach
// localhost via bash, and the install tests serve over 127.0.0.1. The check
// runs at dial time on the resolved IP and then dials that vetted IP, so a
// public host that DNS-rebinds to an internal address is caught too.
//
// This mirrors web_fetch's guard (internal/tool/builtin/webfetch.go); the
// install_source tool fetches the same kind of untrusted URLs and must not be
// the one un-guarded path. Kept in sync by hand — both block the same set.
func ssrfGuardClient(base *http.Client) *http.Client {
	guarded := *base // copy Timeout etc.
	if t, ok := base.Transport.(*http.Transport); ok && t != nil {
		ct := t.Clone()
		inner := ct.DialContext
		if inner == nil {
			inner = (&net.Dialer{}).DialContext
		}
		ct.DialContext = ssrfDial(inner)
		guarded.Transport = ct
	} else {
		// Non-*http.Transport (or nil Transport): build a fresh guarded transport.
		// The real paths — boot's netclient and the tests' httptest client — are
		// always *http.Transport, so this branch only covers a bare &http.Client{}.
		guarded.Transport = &http.Transport{DialContext: ssrfDial((&net.Dialer{}).DialContext)}
	}
	return &guarded
}

func ssrfDial(inner func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if blockedFetchIP(ip.IP) {
				return nil, fmt.Errorf("refusing to fetch internal address %s (resolves to %s)", host, ip.IP)
			}
		}
		// Dial the vetted IP, not the hostname, so the connection can't re-resolve
		// to a different (internal) address (DNS rebinding).
		return inner(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
}

// blockedFetchIP reports whether ip is an address install_source must not reach.
// Loopback is intentionally allowed (see ssrfGuardClient).
func blockedFetchIP(ip net.IP) bool {
	return netclient.BlockedFetchIP(ip)
}
