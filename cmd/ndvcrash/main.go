// Command ndvcrash reproduces the packaged-exe crash in the dev shell: with
// mocks returning null (exactly what Go nil slices serialize to), click 运维
// and capture the browser console/page errors with UNMINIFIED stacks.
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/zzycxz/hiq/internal/browserlaunch"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:5173", "dev server")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	handle, err := browserlaunch.Launch(ctx, browserlaunch.LaunchOptions{Headless: true, ExtraArgs: []string{"--proxy-server=direct://", "--proxy-bypass-list=*"}})
	if err != nil {
		die("launch: %v", err)
	}
	defer handle.Close()
	actx, _ := chromedp.NewRemoteAllocator(ctx, handle.WSURL)
	bctx, bcancel := chromedp.NewContext(actx)
	defer bcancel()

	chromedp.ListenTarget(bctx, func(ev interface{}) {
		switch e := ev.(type) {
		case *runtime.EventExceptionThrown:
			d := e.ExceptionDetails
			if d != nil {
				loc := ""
				if d.URL != "" {
					loc = fmt.Sprintf(" @ %s:%d:%d", d.URL, d.LineNumber, d.ColumnNumber)
				}
				fmt.Printf("EXCEPTION: %s%s\n", d.Text, loc)
				if d.Exception != nil && d.Exception.Description != "" {
					fmt.Println(d.Exception.Description)
				}
			}
		}
	})

	if err := chromedp.Run(bctx,
		chromedp.Navigate(*url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(2500*time.Millisecond),
		chromedp.Click(`//button[contains(., "运维")]`, chromedp.BySearch),
		chromedp.Sleep(4000*time.Millisecond),
	); err != nil {
		die("run: %v", err)
	}
	fmt.Println("--- probe done")
}

func die(f string, a ...any) {
	fmt.Printf("ndvcrash: "+f+"\n", a...)
}
