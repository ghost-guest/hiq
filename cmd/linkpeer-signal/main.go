// linkpeer-signal is the cloud signaling server (K) for linkpeer × hiq.
// It is a stateless router: pair matching, public-key exchange, and SDP/ICE
// forwarding. Deployment via docker-compose (signal + coturn + caddy), see
// docs/LINKPEER_PROTOCOL.md §10.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zzycxz/hiq/internal/linkpeersignal"
)

func main() {
	cfgPath := flag.String("config", "", "path to signal.toml (empty = defaults)")
	flag.Parse()

	cfg, err := linkpeersignal.LoadConfig(*cfgPath)
	if err != nil {
		os.Stderr.WriteString("config load failed: " + err.Error() + "\n")
		os.Exit(1)
	}
	audit := linkpeersignal.NewAudit(cfg.Log.Level)
	srv := linkpeersignal.NewServer(cfg, audit)

	httpSrv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Periodic sweeps: expired pairs, idle WS peers, stale rate-limit buckets.
	// 30s cadence is cheap (O(n) over small in-memory maps).
	stopSweep := make(chan struct{})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				srv.Sweep()
			case <-stopSweep:
				return
			}
		}
	}()

	go func() {
		audit.Info("listening", "addr", cfg.Server.Listen)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			audit.Warn("listen failed", "err", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM (SIGNAL_SPEC §13.2). Existing WS
	// links get a brief window to close cleanly; clients reconnect with their
	// existing backoff+jitter.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	audit.Info("shutting down")
	srv.BroadcastShutdown(5) // 通知所有 WS 客户端立即重连（§13.2）
	close(stopSweep)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}
