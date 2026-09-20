package builtin

// browserpanel_live_test.go guards the live-panel streaming path against the
// deadlock that shipped in v0.1.18 and made the in-app browser useless:
//
//	A chromedp listener runs INLINE on the target's message-queue goroutine,
//	with the target's listenersMu held (chromedp/target.go run() → runListeners
//	→ listener.fn(ev)). The panel handler used to call chromedp.Run from there
//	— Page.screencastFrameAck on every frame, plus a title/URL probe. Those
//	calls wait for a response that only the goroutine they are blocking could
//	deliver, so the FIRST screencast frame wedged the session forever:
//	  - the mandatory ack never landed → Chrome stopped streaming;
//	  - no image ever reached the panel → the dock showed a blank viewport;
//	  - every later CDP call blocked on listenersMu → browser_open's own
//	    navigate timed out after 60s (browserActionTimeout) while the page
//	    loaded fine in Chrome, and the user was told it failed.
//
// The fix queues events (panelStreamState.enqueue) and does all blocking work in
// writeLoop. These tests pin both halves: the queue must never block, and a real
// session must stay responsive AND actually stream frames.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestPanelStreamEnqueueNeverBlocks is the cheap invariant: enqueue is called
// with chromedp's listenersMu held, so it must return immediately even when the
// writer is far behind, and it must keep the NEWEST event (an unacked frame is
// merely skipped by Chrome; a stalled listener is fatal).
func TestPanelStreamEnqueueNeverBlocks(t *testing.T) {
	st := &panelStreamState{events: make(chan panelStreamEvent, 2)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 5000; i++ {
			st.enqueue(panelStreamEvent{sessionID: int64(i), data: fmt.Sprintf("frame-%d", i)})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue blocked — a full queue must drop, never wait (this runs on chromedp's listener goroutine)")
	}
	if got := len(st.events); got != 2 {
		t.Fatalf("queue length = %d, want the 2-slot buffer full", got)
	}
	newest := <-st.events
	if newest.data != "frame-4998" {
		t.Fatalf("oldest event survived (data=%q) — drop-oldest must keep the newest frames", newest.data)
	}
}

// TestPanelTypingReachesFocusedInput pins the panel's KEYBOARD contract, which
// no earlier test covered — and that is exactly how v0.1.21 shipped able to show
// a login page, accept clicks, and swallow every keystroke: the panel's own
// `text` path used Input.insertText, which is the IME primitive and fires NO
// key events, so a form validating on keydown (login pages do) never saw the
// character. The shell-side cause was a bare <div> holding focus (a Chinese IME
// turns those keystrokes into key="Process" and drops them); here we pin what
// the kernel must do once a stroke arrives:
//
//   - a single character is typed as a KEY (keydown with text + keyup), so the
//     page's own keydown listeners fire;
//   - a multi-character string (IME commit) inserts as text;
//   - named keys and Ctrl-shortcuts are delivered as key events, never rejected
//     with "unsupported key" (which surfaced as a red banner in the panel).
func TestPanelTypingReachesFocusedInput(t *testing.T) {
	if _, _, err := detectBrowserPath(); err != nil {
		t.Skipf("no Chromium-based browser available: %v", err)
	}
	liveHome(t)
	ReleaseBrowserPool()
	defer ReleaseBrowserPool()
	resetBrowserDetection()
	defer resetBrowserDetection()

	const pg = `<!doctype html><html><head><title>TYPING</title>
<style>html,body{margin:0;height:100%}
input{display:block;width:60%;font-size:28px;margin:8% auto;padding:8px}
</style></head><body>
<input id="u" placeholder="user" autocomplete="off">
<input id="p" placeholder="pass" autocomplete="off">
<script>
window.__keys=[];
document.addEventListener('keydown',function(e){window.__keys.push(e.key)},true);
window.__vals=function(){return document.getElementById('u').value+'|'+document.getElementById('p').value};
window.__active=function(){return document.activeElement?(document.activeElement.id||document.activeElement.tagName):'none'};
window.__keysLog=function(){return window.__keys.join(',')};
window.__center=function(id){var r=document.getElementById(id).getBoundingClientRect();
 var vv=window.visualViewport;
 return {x:(r.left+r.width/2)/vv.width, y:(r.top+r.height/2)/vv.height};};
</script></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, pg)
	}))
	defer srv.Close()

	SetBrowserPanelSink(func(BrowserPanelFrame) {})
	defer SetBrowserPanelSink(nil)
	SetBrowserLaunchOptions(false, "", "")
	SetBrowserSurface(true) // panel mode: headless behind the dock
	SetBrowserCookiePolicy(nil, "")

	if _, err := (browserOpen{}).Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`)); err != nil {
		t.Fatalf("browser_open failed: %v", err)
	}
	id := sessionIDOf(t)
	defer closeBrowserSession(id)
	s, err := getBrowserSession(id)
	if err != nil {
		t.Fatalf("session %s not registered: %v", id, err)
	}

	evalStr := func(js string) string {
		pctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		defer cancel()
		var out string
		if err := chromedp.Run(pctx, chromedp.Evaluate(js, &out)); err != nil {
			t.Fatalf("eval %s: %v", js, err)
		}
		return out
	}
	click := func(elemID string) {
		pctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		var c struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		}
		if err := chromedp.Run(pctx, chromedp.Evaluate(fmt.Sprintf(`window.__center(%q)`, elemID), &c)); err != nil {
			cancel()
			t.Fatalf("center(%s): %v", elemID, err)
		}
		cancel()
		if err := PanelDispatchInput(id, PanelInputEvent{Type: "down", X: c.X, Y: c.Y, Button: "left", ClickCount: 1}); err != nil {
			t.Fatalf("click %s down: %v", elemID, err)
		}
		time.Sleep(40 * time.Millisecond)
		if err := PanelDispatchInput(id, PanelInputEvent{Type: "up", X: c.X, Y: c.Y, Button: "left", ClickCount: 1}); err != nil {
			t.Fatalf("click %s up: %v", elemID, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	typ := func(ev PanelInputEvent) {
		t.Helper()
		if err := PanelDispatchInput(id, ev); err != nil {
			t.Fatalf("dispatch %+v: %v", ev, err)
		}
	}

	// The user clicks the username field, then types. Both must land.
	click("u")
	if got := evalStr(`window.__active()`); got != "u" {
		t.Fatalf("after clicking #u activeElement = %q, want %q — the page is not receiving focus from the panel", got, "u")
	}
	for _, ch := range []string{"a", "B", "7", "@"} {
		typ(PanelInputEvent{Type: "text", Text: ch})
	}
	if got := evalStr(`window.__vals()`); got != "aB7@|" {
		t.Errorf("#u value = %q, want %q — typed characters are not reaching the focused field", got, "aB7@|")
	}
	if got := evalStr(`window.__keysLog()`); !strings.Contains(got, "a") {
		t.Errorf("page keydown log = %q — single characters must arrive as KEY events (keydown), "+
			"not as bare Input.insertText: a form that validates on keydown silently ignores the typing", got)
	}

	// Then the password field, and an IME commit (multi-character string).
	click("p")
	if got := evalStr(`window.__active()`); got != "p" {
		t.Fatalf("after clicking #p activeElement = %q, want %q", got, "p")
	}
	typ(PanelInputEvent{Type: "text", Text: "用户名"})
	if got := evalStr(`window.__vals()`); got != "aB7@|用户名" {
		t.Errorf("IME commit did not land: %q, want %q", got, "aB7@|用户名")
	}

	// Ctrl+A / Backspace / Enter must be delivered as keys, not rejected.
	for _, k := range []PanelInputEvent{
		{Type: "key", Key: "a", Modifiers: 2},
		{Type: "key", Key: "Backspace"},
		{Type: "key", Key: "Enter"},
	} {
		typ(k)
	}
	keys := evalStr(`window.__keysLog()`)
	for _, want := range []string{"Backspace", "Enter"} {
		if !strings.Contains(keys, want) {
			t.Errorf("page never saw %s (keydown log %q)", want, keys)
		}
	}
}

// TestPanelStreamKeepsSessionResponsive drives the real browser_open path with
// the panel sink attached (so the screencast is running) and asserts the two
// properties the deadlock broke: the navigate returns promptly, and image
// frames reach the sink. Skips when no Chromium browser is installed.
func TestPanelStreamKeepsSessionResponsive(t *testing.T) {
	if _, _, err := detectBrowserPath(); err != nil {
		t.Skipf("no Chromium-based browser available: %v", err)
	}
	// Keep everything (config dir → panel profile, downloads) inside a temp HOME
	// so the test never touches the developer's real profile.
	liveHome(t)
	// A headless Chrome exits when its last target closes, so any allocator left
	// over from an earlier live test points at a dead process; rebuild it.
	ReleaseBrowserPool()
	defer ReleaseBrowserPool()
	resetBrowserDetection()
	defer resetBrowserDetection()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>PANEL-PROBE</title></head>
<body style="background:#204;color:#fff;font-size:64px">HELLO-PANEL</body></html>`)
	}))
	defer srv.Close()

	var mu sync.Mutex
	var imageFrames int
	var liveURL string
	SetBrowserPanelSink(func(f BrowserPanelFrame) {
		if f.Kind != "live" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if f.Image != "" {
			imageFrames++
		}
		if f.URL != "" {
			liveURL = f.URL
		}
	})
	defer SetBrowserPanelSink(nil)
	SetBrowserLaunchOptions(false, "", "")
	SetBrowserSurface(true) // panel mode: headless behind the dock
	SetBrowserCookiePolicy(nil, "")

	start := time.Now()
	out, err := browserOpen{}.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("browser_open failed after %s: %v", elapsed, err)
	}
	t.Logf("browser_open ok in %s: %s", elapsed.Truncate(time.Millisecond), out)
	if elapsed > 30*time.Second {
		t.Fatalf("navigate took %s — the panel stream is blocking the session again "+
			"(browserActionTimeout is 60s; the page loads but every CDP call hangs)", elapsed)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := imageFrames
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	mu.Lock()
	n, url := imageFrames, liveURL
	mu.Unlock()
	if n == 0 {
		t.Fatal("no screencast image frame reached the panel sink — the dock would show a blank viewport")
	}
	t.Logf("received %d live image frames (address bar last saw %q)", n, url)

	// The session must still answer CDP calls AFTER streaming started.
	id := sessionIDOf(t)
	defer closeBrowserSession(id) // release the temp profile before TempDir cleanup
	s, gerr := getBrowserSession(id)
	if gerr != nil {
		t.Fatalf("session %s not registered: %v", id, gerr)
	}
	pctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	var loc string
	if err := chromedp.Run(pctx, chromedp.Location(&loc)); err != nil {
		t.Fatalf("session wedged after streaming started: %v", err)
	}
	// Chrome normalises a bare origin to "…/"; compare ignoring that.
	if strings.TrimSuffix(loc, "/") != strings.TrimSuffix(srv.URL, "/") {
		t.Fatalf("location = %q, want %q", loc, srv.URL)
	}

	// Tab switching shares the same hazard: switchSessionTab used to hold tabMu
	// across its attach + panelRestart CDP calls while the panel listener also
	// takes tabMu, so clicking the panel's tab strip could wedge the session.
	tabStart := time.Now()
	nav := browserNavigate{}
	if _, err := nav.Execute(context.Background(), json.RawMessage(
		`{"session_id":"`+id+`","url":"about:blank","new_tab":true}`)); err != nil {
		t.Fatalf("new_tab navigate failed: %v", err)
	}
	if el := time.Since(tabStart); el > 30*time.Second {
		t.Fatalf("tab switch took %s — tabMu is being held across a CDP call again", el)
	}
	if tabs, err := PanelTabs(id); err != nil || len(tabs) < 2 {
		t.Fatalf("expected ≥2 tabs after a new_tab navigate, got %d (err=%v)", len(tabs), err)
	}
}

func sessionIDOf(t *testing.T) string {
	t.Helper()
	browserMu.Lock()
	defer browserMu.Unlock()
	id := ""
	for k := range browserSessions {
		id = k
	}
	return id
}

// sharedLiveHome is the temp HOME shared by EVERY live-browser test in this
// file. The chromedp allocator (browserPoolCtx) is a process-wide singleton that
// is created on first use and never rebuilt, and Chrome exits the moment its
// --user-data-dir disappears. A per-test temp HOME therefore broke the SECOND
// live test with "chrome failed to start": test one deleted the profile out from
// under the still-running browser. One stable profile keeps the allocator's
// browser alive across tests; TestMain removes the directory at the very end.
var (
	sharedLiveHome     string
	sharedLiveHomeOnce sync.Once
)

// liveHome points the process at the shared temp HOME and returns it. Callers
// must not delete it.
func liveHome(t *testing.T) string {
	t.Helper()
	sharedLiveHomeOnce.Do(func() {
		if dir, err := os.MkdirTemp("", "hiq-panel-live"); err == nil {
			sharedLiveHome = dir
		}
	})
	if sharedLiveHome == "" {
		t.Fatal("could not create a temp HOME for the live browser tests")
	}
	t.Setenv("HOME", sharedLiveHome)
	t.Setenv("USERPROFILE", sharedLiveHome)
	t.Setenv("XDG_CONFIG_HOME", sharedLiveHome)
	t.Setenv("AppData", sharedLiveHome)
	return sharedLiveHome
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedLiveHome != "" {
		_ = os.RemoveAll(sharedLiveHome)
	}
	os.Exit(code)
}

// TestPanelClickHitsNormalizedTarget pins the panel's coordinate contract: a
// user click is forwarded as 0..1 within the streamed frame, so the shell maps
// it back with the live CSS visual viewport. A drift there does NOT fail loudly
// — every tap just lands somewhere else, which is exactly the "I can see the
// page but cannot press 接受" report. Four quadrant targets plus a bottom banner
// (the cookie-consent shape) make any scaling/offset error visible.
func TestPanelClickHitsNormalizedTarget(t *testing.T) {
	if _, _, err := detectBrowserPath(); err != nil {
		t.Skipf("no Chromium-based browser available: %v", err)
	}
	liveHome(t)
	ReleaseBrowserPool()
	defer ReleaseBrowserPool()
	resetBrowserDetection()
	defer resetBrowserDetection()

	const pg = `<!doctype html><html><head><title>ACCURACY</title>
<style>html,body{margin:0;height:100%;overflow:hidden}
div.q{position:absolute;width:50%;height:50%;font-size:28px}
#a{left:0;top:0}#b{right:0;top:0}#c{left:0;bottom:0}#d{right:0;bottom:0}
#bar{position:absolute;left:0;bottom:0;width:100%;height:12%;z-index:9}
</style></head><body>
<div class="q" id="a" data-h="A">A</div><div class="q" id="b" data-h="B">B</div>
<div class="q" id="c" data-h="C">C</div><div class="q" id="d" data-h="D">D</div>
<div id="bar" data-h="BAR">ACCEPT ALL</div>
<script>
var hits=[];
document.addEventListener('click',function(e){
  var el=e.target.closest('[data-h]');
  hits.push(el?el.getAttribute('data-h'):'none');
});
window.__last=function(){return hits.length?hits[hits.length-1]:''};
</script></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, pg)
	}))
	defer srv.Close()

	SetBrowserPanelSink(func(BrowserPanelFrame) {})
	defer SetBrowserPanelSink(nil)
	SetBrowserLaunchOptions(false, "", "")
	SetBrowserSurface(true)
	SetBrowserCookiePolicy(nil, "")

	if _, err := (browserOpen{}).Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`)); err != nil {
		t.Fatalf("browser_open failed: %v", err)
	}
	id := sessionIDOf(t)
	if id == "" {
		t.Fatal("no browser session registered after browser_open")
	}
	defer closeBrowserSession(id)
	s, err := getBrowserSession(id)
	if err != nil {
		t.Fatalf("session %s not registered: %v", id, err)
	}
	last := func() string {
		pctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		defer cancel()
		var out string
		_ = chromedp.Run(pctx, chromedp.Evaluate(`window.__last()`, &out))
		return out
	}

	for _, tc := range []struct {
		name string
		x, y float64
		want string
	}{
		{"top-left", 0.25, 0.25, "A"},
		{"top-right", 0.75, 0.25, "B"},
		{"bottom-left", 0.25, 0.75, "C"},
		{"bottom-right", 0.75, 0.75, "D"},
		{"bottom-banner", 0.5, 0.95, "BAR"},
	} {
		if err := PanelDispatchInput(id, PanelInputEvent{Type: "down", X: tc.x, Y: tc.y, Button: "left", ClickCount: 1}); err != nil {
			t.Fatalf("%s: down: %v", tc.name, err)
		}
		time.Sleep(80 * time.Millisecond)
		if err := PanelDispatchInput(id, PanelInputEvent{Type: "up", X: tc.x, Y: tc.y, Button: "left", ClickCount: 1}); err != nil {
			t.Fatalf("%s: up: %v", tc.name, err)
		}
		time.Sleep(250 * time.Millisecond)
		if got := last(); got != tc.want {
			t.Errorf("click at (%.2f,%.2f) hit %q, want %q — panel coordinates are drifting; "+
				"check panelCssPoint's layout metrics and the frame aspect ratio", tc.x, tc.y, got, tc.want)
		}
	}
}

// TestPanelPushesFrameWhenDockOpensAfterNavigate pins the SECOND blank-panel
// bug, which survived the deadlock fix: the agent opens a URL while the dock's
// browser tab is closed, so the session is created with streaming hidden
// (visible=false → every frame is acked and dropped). The user only clicks the
// browser tab afterwards — and startPanelStream is idempotent, so nothing
// restarted the stream. Chrome's screencast only pushes frames on REPAINT, so a
// page that had already finished loading stayed invisible: the dock showed the
// right URL with an empty viewport. SetPanelStreaming(true) must therefore push
// one on-demand frame for sessions whose screencast is already up.
func TestPanelPushesFrameWhenDockOpensAfterNavigate(t *testing.T) {
	if _, _, err := detectBrowserPath(); err != nil {
		t.Skipf("no Chromium-based browser available: %v", err)
	}
	liveHome(t)
	ReleaseBrowserPool()
	defer ReleaseBrowserPool()
	resetBrowserDetection()
	defer resetBrowserDetection()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>STATIC-PANEL</title></head>
<body style="background:#123;color:#fff;font-size:64px">STATIC-PANEL-BODY</body></html>`)
	}))
	defer srv.Close()

	var mu sync.Mutex
	var frames int
	var lastText string
	SetBrowserPanelSink(func(f BrowserPanelFrame) {
		if f.Kind != "live" || f.Image == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		frames++
		lastText = f.Text
	})
	defer SetBrowserPanelSink(nil)
	SetBrowserLaunchOptions(false, "", "")
	SetBrowserSurface(true) // panel mode: headless behind the dock
	SetBrowserCookiePolicy(nil, "")

	// Dock closed: a session opened now starts streaming-aware but hidden.
	SetPanelStreaming(false)
	defer SetPanelStreaming(false)

	if _, err := (browserOpen{}).Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`)); err != nil {
		t.Fatalf("browser_open failed: %v", err)
	}
	id := sessionIDOf(t)
	if id == "" {
		t.Fatal("no browser session registered after browser_open")
	}
	defer closeBrowserSession(id)
	s, err := getBrowserSession(id)
	if err != nil {
		t.Fatalf("session %s not registered: %v", id, err)
	}
	st := panelStream(s)
	st.mu.Lock()
	active := st.active
	st.mu.Unlock()
	if !active {
		t.Skip("screencast did not start in this environment (no browser/display)")
	}

	// While the dock is closed nothing may reach the panel.
	time.Sleep(1500 * time.Millisecond)
	mu.Lock()
	before := frames
	mu.Unlock()
	if before != 0 {
		t.Fatalf("%d frames reached a HIDDEN panel — streaming must stay gated", before)
	}

	// The user finally opens the dock's browser tab.
	SetPanelStreaming(true)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := frames
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	mu.Lock()
	n, text := frames, lastText
	mu.Unlock()
	if n == 0 {
		t.Fatal("opening the dock produced no frame for an already-loaded session — " +
			"the panel would show the URL with a blank viewport forever (Chrome only " +
			"streams on repaint)")
	}
	t.Logf("panel received %d frame(s) right after the dock opened (source=%q)", n, text)
}
