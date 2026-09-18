package netdev

// dbtunnel.go — DB connections through the bastion chain (NETDEV_SPEC_V2 追加):
// when a [[netdev.db_sources]] entry declares Via, the query rides a LOCAL
// FORWARD — one ephemeral 127.0.0.1 listener pumped over the hop's SSH
// direct-tcpip channel (the same dialer discovery uses). Local forward keeps
// every engine stock: sql drivers, redis, mongo and the HTTP legs all just
// point at 127.0.0.1:<ephemeral>, zero per-driver dial plumbing.

import (
	"context"
	"fmt"
	"io"
	"net"

	"github.com/zzycxz/hiq/internal/config"
)

// dbDefaultPort per engine type (tunnel target side).
func dbDefaultPort(t string) int {
	switch t {
	case "mysql":
		return 3306
	case "postgres":
		return 5432
	case "redis":
		return 6379
	case "mongodb":
		return 27017
	case "mssql":
		return 1433
	case "clickhouse":
		return 8123
	case "elasticsearch":
		return 9200
	}
	return 3306
}

// dbTunnel rewrites src onto a local forward when Via is set; no Via = the
// source unchanged with a nop closer.
func (m *Manager) dbTunnel(ctx context.Context, src config.NetDevDBSource) (config.NetDevDBSource, func(), error) {
	nop := func() {}
	if len(src.Via) == 0 {
		return src, nop, nil
	}
	dialer, cleanup, err := m.dialerFor(ctx, src.Via[0])
	if err != nil {
		return src, cleanup, fmt.Errorf("db via %q: %w", src.Via[0], err)
	}
	target := dbHostPort(src, dbDefaultPort(src.Type))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cleanup()
		return src, nop, err
	}
	go func() {
		for {
			c1, err := ln.Accept()
			if err != nil {
				return
			}
			dctx, cancel := context.WithTimeout(ctx, dbQueryTimeout)
			c2, err := dialer.DialContext(dctx, "tcp", target)
			cancel()
			if err != nil {
				c1.Close()
				continue
			}
			go dbPump(c1, c2)
		}
	}()
	t := src
	t.Host = "127.0.0.1"
	t.Port = ln.Addr().(*net.TCPAddr).Port
	t.Via = nil
	return t, func() { _ = ln.Close(); cleanup() }, nil
}

// dbPump shuttles both directions and closes both ends when either stalls.
func dbPump(a, b net.Conn) {
	defer a.Close()
	defer b.Close()
	done := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(b, a)
		done <- struct{}{}
	}()
	_, _ = io.Copy(a, b)
	<-done
}
