// Command dashshot visually verifies the 大屏家族 (DASHBOARD spec v2.0) in
// the frontend dev shell: boots the netdev mock profile straight into each
// dash screen via the ?bench=dash&screen= deep links and screenshots them.
// The dev-mock bridge feeds every screen, so a blank/error shot means a real
// wiring bug — this is the visual counterpart of dash-boards.test.tsx.
//
// Usage: go run ./cmd/dashshot -url http://127.0.0.1:5173 -out gui-test-screenshots
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/zzycxz/hiq/internal/browserlaunch"
)

var screens = []struct{ name, screen string }{
	{"dash1-overview", "overview"},
	{"dash2-chain", "chain"},
	{"dash3-cutover", "cutover"},
	{"dash4-discovery", "discovery"},
	{"dash5-exposure", "exposure"},
}

func main() {
	url := flag.String("url", "http://127.0.0.1:5173", "frontend dev server URL")
	out := flag.String("out", "gui-test-screenshots", "screenshot output dir")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal("mkdir: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	handle, err := browserlaunch.Launch(ctx, browserlaunch.LaunchOptions{
		Headless:  true,
		ExtraArgs: []string{"--proxy-server=direct://", "--proxy-bypass-list=*"},
	})
	if err != nil {
		fatal("launch browser: %v (is Chrome/Edge installed?)", err)
	}
	defer handle.Close()
	fmt.Println("browser:", handle.BrowserName, handle.CDPURL)

	actx, acancel := chromedp.NewRemoteAllocator(ctx, handle.WSURL)
	defer acancel()
	bctx, bcancel := chromedp.NewContext(actx)
	defer bcancel()

	for _, s := range screens {
		full := *url + "/?profile=netdev&bench=dash&screen=" + s.screen
		var buf []byte
		if err := chromedp.Run(bctx,
			chromedp.Navigate(full),
			chromedp.WaitReady("body", chromedp.ByQuery),
			chromedp.Sleep(2500*time.Millisecond), // React boot + mock bridges
			chromedp.FullScreenshot(&buf, 90),
		); err != nil {
			fatal("%s: %v", s.name, err)
		}
		path := filepath.Join(*out, s.name+".png")
		if err := os.WriteFile(path, buf, 0o644); err != nil {
			fatal("write %s: %v", path, err)
		}
		fmt.Println("shot:", path, len(buf), "bytes")
	}
	fmt.Println("done")
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "dashshot: "+f+"\n", a...)
	os.Exit(1)
}
