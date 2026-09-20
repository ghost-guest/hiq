package builtin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	cdprotopage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	cdptarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/zzycxz/hiq/internal/tool"
)

// Browser automation tools (Phase 1 of coWork). These drive a real Chromium via
// the Chrome DevTools Protocol (chromedp) — navigation, clicking, typing,
// scrolling, content extraction, screenshots, and JS evaluation. They are the
// "precise control" channel for office web tasks (research, form filling, data
// scraping) and complement the VLM screenshot channel.
//
// Session model: browser_open starts a Chromium subprocess and returns a session
// id; the other tools take that session id and reuse the same browser tab. A
// process-global pool holds live sessions so multiple agent sub-tasks can drive
// independent tabs without each spawning a browser. Sessions time out after
// browserIdleTimeout of inactivity and are closed on process exit.
//
// Build footprint: chromedp is pure Go (no CGO), so it does not compromise the
// CLI's single-static-binary guarantee. The browser subprocess is only spawned
// on first browser_open — dev mode never pays for it. These tools are
// profile-gated in boot.go (registered only when the cowork profile is active),
// so they don't appear in the dev tool list at all.

// BrowserTools returns the full set of browser automation tools, for
// registration when the cowork profile is active. Unlike the compile-time
// built-ins (which self-register via init() and are therefore in every tool
// list), browser tools are intentionally NOT in the global set — they are
// office-specific and should not appear in dev mode. boot.go calls this only
// under the cowork profile, so the dev tool list stays clean and the browser
// subprocess is never reachable from a coding session.
func BrowserTools() []tool.Tool {
	return []tool.Tool{
		browserOpen{},
		browserAttach{},
		browserNavigate{},
		browserCookies{},
		browserTabs{},
		browserSwitchTab{},
		browserHover{},
		browserBack{},
		browserForward{},
		browserClick{},
		browserType{},
		browserScroll{},
		browserExtract{},
		browserScreenshot{},
		browserEvaluate{},
		browserSnapshot{},
		browserSelectOption{},
		browserUploadFile{},
		browserSetPath{},
		browserWait{},
		browserKeepalive{},
		browserAuto{},
	}
}

// --- in-app browser mirror (pane-system §3.6 companion tier) -----------------
//
// The driven browser is a real OS window (spawned or attached), but the app
// mirrors it in the cowork dock: every session-lifecycle transition and every
// post-action screenshot is forwarded through a desktop-registered sink to the
// frontend panel. CLI runs register no sink and pay nothing beyond a nil check.

// BrowserPanelFrame is one update for the desktop's browser-mirror panel.
// Kind "frame" carries a screenshot (Image as data URL); kind "status" carries
// a lifecycle transition (Phase "start"|"end"). The frontend owns all labels —
// Text stays machine-ish (browser name, summary) rather than localized prose.
type BrowserPanelFrame struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`          // "tool" (chromedp tools) | "auto" (browser-use sidecar)
	Phase  string `json:"phase,omitempty"` // status only: "start" | "end"
	Text   string `json:"text,omitempty"`
	URL    string `json:"url,omitempty"`
	Title  string `json:"title,omitempty"` // page title (live frames, after load)
	Image  string `json:"image,omitempty"` // data URL (frame only)
	// TabID tags live frames with the page target they came from, so the
	// panel's tab strip can mark the active tab.
	TabID string `json:"tab_id,omitempty"`
	// SessionID tags every frame with the browser session that produced it,
	// so the frontend can keep per-session mirrors (the ops viewer shows the
	// console session AND agent-driven sessions side by side).
	SessionID string `json:"session_id,omitempty"`
}

var browserPanelSink func(BrowserPanelFrame)

// SetBrowserPanelSink registers the desktop's mirror forwarder (Wails event
// emitter). Set once at startup; nil (CLI) disables emission entirely.
func SetBrowserPanelSink(fn func(BrowserPanelFrame)) { browserPanelSink = fn }

// EmitBrowserPanel forwards a frame when a sink is registered.
func EmitBrowserPanel(f BrowserPanelFrame) {
	if browserPanelSink != nil {
		browserPanelSink(f)
	}
}

const (
	// browserIdleTimeout closes a browser session after this long without a tool
	// call, so a forgotten browser_open doesn't leak a Chromium process forever.
	browserIdleTimeout = 10 * time.Minute
	// browserActionTimeout caps any single CDP action, so a hung page can't block
	// the agent loop indefinitely.
	browserActionTimeout = 60 * time.Second
	// browserExtractMaxChars caps extracted text so a huge page doesn't blow the
	// model's context. The agent can narrow with a selector or paginate.
	browserExtractMaxChars = 200_000
	// browserEvaluateMaxChars caps the JSON returned by browser_evaluate. A
	// bare document.querySelectorAll(...) can return tens of thousands of
	// serialized nodes and blow the model's context or trip a 400 from the
	// upstream API before any meaningful truncation downstream. evaluate returns
	// structured JSON (not readable text like extract), so the cap is tighter;
	// if the script needs more, it should slice and return only what matters.
	browserEvaluateMaxChars = 100_000
)

// browserDownloadDir is where browser-triggered downloads land. Resolved once
// on first session start (see initBrowserDownloadDir). We pin a dedicated dir
// (not Chrome's default) so the agent has a stable path to point at, and so
// downloads don't clutter the user's ~/Downloads. Surfaced to the model via the
// downloadRecords summary ("download completed: <dir>/<file>").
var (
	browserDownloadDir     string
	browserDownloadDirOnce sync.Once
	browserDownloadDirErr  error
)

// initBrowserDownloadDir resolves a writable per-user downloads dir on first
// use. Falls back to a system temp dir if the user cache is unavailable, since
// downloads are best-effort — a missing dir must not block browser launch.
// Safe for concurrent callers (sync.Once); subsequent calls are no-ops.
func initBrowserDownloadDir() error {
	browserDownloadDirOnce.Do(func() {
		base, err := os.UserCacheDir()
		if err != nil || base == "" {
			base = os.TempDir()
		}
		dir := filepath.Join(base, "hiq", "browser-downloads")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			browserDownloadDirErr = fmt.Errorf("create download dir: %w", err)
			return
		}
		browserDownloadDir = dir
	})
	return browserDownloadDirErr
}

// --- session pool -----------------------------------------------------------

// browserSession is one live Chromium tab driven via CDP.
type browserSession struct {
	id          string
	allocCancel context.CancelFunc
	ctxCancel   context.CancelFunc
	ctx         context.Context // chromedp context tied to this tab
	lastUsed    atomic.Int64    // unix seconds; drives idle reaping
	browser     string          // display name of the driven browser ("Chrome"/"Edge"/…)
	// attached is true when this session attaches to an externally-launched
	// browser (browser_attach via a CDP debug port) rather than one we spawned.
	// Close then ONLY disconnects the CDP websocket — it must NOT kill the
	// browser process, which the user owns. Spawned sessions close the process.
	attached bool
	// refs holds the ref→node map from the most recent browser_snapshot. It's an
	// atomic pointer (not a plain field) because navigate clears it (sets nil)
	// while a concurrent click/type may read it — a plain field would data-race.
	// The refs map itself is never mutated in place, only wholesale-replaced,
	// which is exactly the atomic-store pattern. Cleared on navigate (refs expire
	// when the page changes). Action tools resolve a ref via resolveRefToObjectID.
	refs atomic.Pointer[snapshotRefs]
	// dialogMessages records JS dialog messages (alert/confirm) for the agent.
	dialogMu       sync.Mutex
	dialogMessages []string
	// downloadRecords captures files downloaded in this session so the agent
	// learns a download finished and where the file landed — without this the
	// click that triggers a download returns "clicked" with no hint a file is
	// now on disk. Populated by startDownloadsHandler.
	downloadMu      sync.Mutex
	downloadRecords []downloadRecord
	// stepTracker tracks action repetition and page stagnation for loop detection.
	stepTracker *browserStepTracker
	// tabMu serializes tab switches (auto-follow + browser_switch_tab). The
	// old tab's context is abandoned, not canceled — chromedp cancel would
	// CLOSE that target, and the old tab must stay open for switching back.
	tabMu sync.Mutex
	// panelStream carries the live-panel screencast bookkeeping (see
	// browserpanel.go). Atomic pointer: created lazily on first panel use,
	// never mutated after.
	panelStream atomic.Pointer[panelStreamState]
	// Session keep-alive (会话保活): armed from the ops console toggle or the
	// browser_keepalive tool. Each tick refreshes lastUsed (beats the idle
	// reaper) and, per mode, pings the site session from inside the page or
	// reloads it — long-interval ops tasks keep their login.
	keepMu       sync.Mutex
	keepOn       bool
	keepMode     string // ping|navigate|local
	keepURL      string
	keepInterval time.Duration
	keepLast     int64  // unix millis of the last successful refresh
	keepErr      string // last refresh failure ("" = ok)
	keepStop     chan struct{}
}

// downloadRecord is one completed (or failed) file download in a session.
type downloadRecord struct {
	GUID          string // CDP download guid (pairs willBegin ↔ progress)
	URL           string // page URL that triggered the download
	SuggestedName string // filename suggested by the server
	State         string // "inProgress", "completed", or "canceled"
}

// --- loop detection (Phase 2) -----------------------------------------------

// browserStepTracker tracks action repetition and page stagnation to detect
// behavioral loops. Modeled after browser-use's ActionLoopDetector.
type browserStepTracker struct { //nolint:unused
	mu                     sync.Mutex
	recentActionHashes     []string          // rolling window (max 20)
	recentPageFingerprints []pageFingerprint // last 5 page states //nolint:unused
	consecutiveStagnant    int               // steps with identical page fingerprint
	consecutiveFailures    int               // consecutive action failures
}

type pageFingerprint struct { //nolint:unused
	url          string
	elementCount int
	textHash     string // SHA-256[:16] of DOM text
}

const (
	loopWindowSize      = 20
	fingerprintWindow   = 5
	stagnantThreshold   = 5
	repeatThreshold5    = 5
	repeatThreshold8    = 8
	repeatThreshold12   = 12
	maxConsecutiveFails = 5
)

// typeRefJSBody is the element-typing logic shared by the ref and selector
// paths of browser_type. It assumes `this` is the target element and reads
// `text` + `clear` from the enclosing function args. Returns
// JSON.stringify({value, expectFormatChange}) so callers can detect value
// mismatch. Extracted as a const so both paths stay byte-identical — a
// previous split (ref hardened, selector using chromedp.SendKeys) caused
// React-controlled inputs to silently fail on the selector path.
const typeRefJSBody = `// contenteditable elements (rich-text editors, Quill, some comment boxes)
  // don't have a .value — setting it does nothing. Write to textContent and
  // dispatch an InputEvent so the editor's framework picks up the change.
  if (this.isContentEditable) {
    if (clear) { this.textContent = ''; }
    this.focus();
    if (clear || !this.textContent) {
      this.textContent = text;
    } else {
      this.textContent = this.textContent + text;
    }
    this.dispatchEvent(new InputEvent('input', {bubbles: true, data: text, inputType: 'insertText'}));
    return JSON.stringify({value: this.textContent, expectFormatChange: true});
  }
  // HTML5 inputs with specialized pickers + jQuery/Bootstrap datepickers.
  var tag = (this.tagName || '').toLowerCase();
  var type = (this.getAttribute && this.getAttribute('type')) || '';
  type = type.toLowerCase();
  var nativeSpecial = tag === 'input' && ['date','time','datetime-local','month','week','color','range'].indexOf(type) !== -1;
  var jQueryDate = false;
  if (tag === 'input' && (type === 'text' || type === '')) {
    var cls = ((this.getAttribute && this.getAttribute('class')) || '').toLowerCase();
    var dateClassIndicators = ['datepicker', 'daterangepicker', 'datetimepicker', 'bootstrap-datepicker'];
    for (var di = 0; di < dateClassIndicators.length; di++) {
      if (cls.indexOf(dateClassIndicators[di]) !== -1) { jQueryDate = true; break; }
    }
    if (!jQueryDate) {
      if (this.hasAttribute('data-datepicker') || this.hasAttribute('data-date-format') || this.hasAttribute('data-provide')) {
        jQueryDate = true;
      }
    }
  }
  var isSpecialInput = nativeSpecial || jQueryDate;
  if (clear || isSpecialInput) { this.value = ''; }
  this.focus();
  var setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value');
  if (!setter) { setter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value'); }
  if (setter && setter.set) { setter.set.call(this, text); }
  else { this.value = text; }
  this.dispatchEvent(new Event('input', {bubbles: true}));
  this.dispatchEvent(new Event('change', {bubbles: true}));
  return JSON.stringify({value: this.value, expectFormatChange: isSpecialInput});`

func newStepTracker() *browserStepTracker {
	return &browserStepTracker{}
}

// computeActionHash normalizes an action for similarity comparison.
func computeActionHash(name string, params string) string {
	h := sha256.Sum256([]byte(name + "|" + params))
	return hex.EncodeToString(h[:])[:12]
}

// recordAction records an action into the rolling window.
func (t *browserStepTracker) recordAction(name, params string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h := computeActionHash(name, params)
	t.recentActionHashes = append(t.recentActionHashes, h)
	if len(t.recentActionHashes) > loopWindowSize {
		t.recentActionHashes = t.recentActionHashes[len(t.recentActionHashes)-loopWindowSize:]
	}
}

// recordPageState records a page fingerprint and updates stagnation count.
func (t *browserStepTracker) recordPageState(url, domText string, elemCount int) { //nolint:unused
	t.mu.Lock()
	defer t.mu.Unlock()
	hash := sha256.Sum256([]byte(domText))
	fp := pageFingerprint{url: url, elementCount: elemCount, textHash: hex.EncodeToString(hash[:])[:16]}
	if len(t.recentPageFingerprints) > 0 && t.recentPageFingerprints[len(t.recentPageFingerprints)-1] == fp {
		t.consecutiveStagnant++
	} else {
		t.consecutiveStagnant = 0
	}
	t.recentPageFingerprints = append(t.recentPageFingerprints, fp)
	if len(t.recentPageFingerprints) > fingerprintWindow {
		t.recentPageFingerprints = t.recentPageFingerprints[len(t.recentPageFingerprints)-fingerprintWindow:]
	}
}

// recordFailure increments consecutive failures.
func (t *browserStepTracker) recordFailure() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.consecutiveFailures++
}

// resetFailures resets consecutive failure count on success.
func (t *browserStepTracker) resetFailures() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.consecutiveFailures = 0
}

// getNudgeMessage returns escalating warnings or empty string if no issues.
func (t *browserStepTracker) getNudgeMessage() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var msgs []string

	// Action repetition detection
	counts := map[string]int{}
	for _, h := range t.recentActionHashes {
		counts[h]++
	}
	maxRepeat := 0
	for _, c := range counts {
		if c > maxRepeat {
			maxRepeat = c
		}
	}
	if maxRepeat >= repeatThreshold12 {
		msgs = append(msgs, fmt.Sprintf("⚠️ You have repeated a similar action %d times. A different approach might work better.", maxRepeat))
	} else if maxRepeat >= repeatThreshold8 {
		msgs = append(msgs, fmt.Sprintf("⚠️ You have repeated a similar action %d times. Are you still making progress?", maxRepeat))
	} else if maxRepeat >= repeatThreshold5 {
		msgs = append(msgs, fmt.Sprintf("⚠️ You have repeated a similar action %d times. Consider a different approach.", maxRepeat))
	}

	// Page stagnation detection
	if t.consecutiveStagnant >= stagnantThreshold {
		msgs = append(msgs, fmt.Sprintf("⚠️ Page content unchanged across %d consecutive actions. Your actions may not be having the intended effect.", t.consecutiveStagnant))
	}

	// Consecutive failure warning
	if t.consecutiveFailures >= 3 && t.consecutiveFailures < maxConsecutiveFails {
		msgs = append(msgs, fmt.Sprintf("⚠️ Consecutive failures: %d/%d — reconsider your approach.", t.consecutiveFailures, maxConsecutiveFails))
	} else if t.consecutiveFailures >= maxConsecutiveFails {
		msgs = append(msgs, fmt.Sprintf("❌ Consecutive failures: %d/%d — provide your final answer.", t.consecutiveFailures, maxConsecutiveFails))
	}

	if len(msgs) == 0 {
		return ""
	}
	return strings.Join(msgs, "\n")
}

// --- session pool -----------------------------------------------------------

var (
	browserMu         sync.Mutex
	browserSessions   = map[string]*browserSession{}
	browserSeq        atomic.Int64
	browserReaperOnce sync.Once
)

// BrowserClearAllCookies clears cookies on EVERY live browser session (CDP
// Network.clearBrowserCookies is browser-wide, one call suffices, but sessions
// may be separate browser processes). Powers the settings card's 清除 Cookies
// button. Returns a human summary; error only when no session is live (the
// button then tells the user to start the managed browser first).
func BrowserClearAllCookies() (string, error) {
	browserMu.Lock()
	defer browserMu.Unlock()
	if len(browserSessions) == 0 {
		return "", fmt.Errorf("当前没有运行中的浏览器会话 — 先在设置里启动可控浏览器，或让智能体执行一次 browser_open")
	}
	cleared := 0
	for _, s := range browserSessions {
		if s.ctx == nil || s.ctx.Err() != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		if err := network.ClearBrowserCookies().Do(ctx); err == nil {
			cleared++
		}
		cancel()
	}
	if cleared == 0 {
		return "", fmt.Errorf("浏览器会话已断开，清除失败 — 请重新启动可控浏览器后重试")
	}
	return fmt.Sprintf("已清除 %d 个浏览器会话的全部 Cookies（所有网站退出登录）", cleared), nil
}

// browserPoolCtx is the parent allocator context for all browser sessions. It is
// created lazily on first browser_open and never cancelled (process-lifetime).
var browserPoolCtx context.Context

// browserLaunchOptions holds the launch-time knobs injected from config (and
// the resolved proxy). They shape the chromedp allocator: a persistent profile
// keeps login state across sessions; a non-headless browser behaves more like a
// human user and avoids headless rendering quirks on anti-bot sites; the proxy
// routes the browser through the same network path as the rest of hiq.
// Injected via SetBrowserLaunchOptions at boot so this file stays free of a
// config import cycle.
type browserLaunchOptions struct {
	headless    bool   // false = visible browser (more human-like)
	userDataDir string // persistent profile dir; "" = temp profile
	proxyServer string // e.g. "http://127.0.0.1:7890"; "" = no --proxy-server
	// persistCookies (nil = true) and openLinksIn ("current"|"new") come from
	// SetBrowserCookiePolicy; see there for semantics.
	persistCookies *bool
	openLinksIn    string
}

// globalBrowserLaunch is the resolved launch config. Set once at boot.
var globalBrowserLaunch = browserLaunchOptions{headless: false}

// SetBrowserLaunchOptions injects the browser launch config (headless toggle,
// persistent user-data-dir, proxy URL). boot.go calls this after resolving the
// cowork config and the proxy spec. Empty proxyServer means "no proxy" (the
// browser uses the system default), matching chromedp's behaviour.
func SetBrowserLaunchOptions(headless bool, userDataDir, proxyServer string) {
	globalBrowserLaunch = browserLaunchOptions{
		headless:    headless,
		userDataDir: strings.TrimSpace(userDataDir),
		proxyServer: strings.TrimSpace(proxyServer),
	}
}

// SetBrowserCookiePolicy injects the cookie/tab behavior toggles from the
// cowork settings (HanaAgent-style browser options):
//   - persistCookies: nil/true keeps the persistent user-data-dir (cookies and
//     logins survive restarts); false forces an ephemeral session — the
//     configured user-data-dir is ignored and every launch starts cookie-clean.
//     Consulted when the allocator is created (first browser_open), so a
//     mid-session toggle lands on the next app start.
//   - openLinksIn: "current" (default) navigates in the tab the session
//     already drives; "new" opens each browser_navigate in a fresh tab,
//     leaving the previous page open for back-and-forth.
func SetBrowserCookiePolicy(persistCookies *bool, openLinksIn string) {
	globalBrowserLaunch.persistCookies = persistCookies
	globalBrowserLaunch.openLinksIn = strings.TrimSpace(openLinksIn)
}

// cookiesPersisted reports whether the browser should keep cookies across
// launches (nil = default true, the historical behavior).
func (o browserLaunchOptions) cookiesPersisted() bool {
	return o.persistCookies == nil || *o.persistCookies
}

// ensureBrowserAllocator creates the shared chromedp allocator on first use. It
// auto-detects an installed Chromium-based browser (Chrome → Edge → Brave → …)
// so the agent works with zero config on most machines; CHROME_PATH still
// overrides. If nothing is found, the returned error is ErrNoBrowser with
// install guidance — callers surface that to the user instead of a low-level
// chromedp failure.
//
// Returns the allocator context, the detected browser's display name (for the
// "ready" message), and an error if no browser is available.
func ensureBrowserAllocator() (context.Context, string, error) {
	browserMu.Lock()
	defer browserMu.Unlock()
	if browserPoolCtx != nil {
		return browserPoolCtx, detectedBrowserName, nil
	}
	exePath, name, err := detectBrowserPath()
	if err != nil {
		return nil, "", err
	}
	// Use chromedp's DefaultExecAllocatorOptions as the base. These include
	// --headless, --no-sandbox, --disable-gpu, --disable-dev-shm-usage, and
	// many other stability flags that are required for chromedp to work reliably.
	// ExecPath must come first — DefaultExecAllocatorOptions uses the system
	// default Chrome, but we need the detected path.
	opts := []chromedp.ExecAllocatorOption{chromedp.ExecPath(exePath)}
	opts = append(opts, chromedp.DefaultExecAllocatorOptions[:]...)

	// Headless override: DefaultExecAllocatorOptions forces --headless=true, but
	// the user may have configured a HEADED browser ([cowork] browser_headless =
	// false, the default). A visible browser is what desktop users expect and
	// behaves closer to a human user (better against anti-bot challenges). We
	// honor the configured setting here — the later Flag() wins over the same
	// flag set by DefaultExecAllocatorOptions, since ExecAllocator stores flags
	// in a map (last write wins).
	//
	// On servers / CI without a display, the user sets browser_headless = true.
	if globalBrowserLaunch.headless {
		opts = append(opts, chromedp.Flag("headless", true))
	} else {
		opts = append(opts, chromedp.Flag("headless", false))
	}
	opts = append(opts,
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
	)
	// Persistent profile keeps cookies/login state across launches. Essential for
	// sites that require sign-in (GitHub, internal portals) and reduces CAPTCHA
	// friction on repeat visits. Empty = chromedp's default temp profile.
	// The 接受 Cookies toggle (persist=false) forces the temp profile even when
	// a user-data-dir is configured — "don't keep cookies" wins.
	if globalBrowserLaunch.userDataDir != "" && globalBrowserLaunch.cookiesPersisted() {
		opts = append(opts, chromedp.UserDataDir(globalBrowserLaunch.userDataDir))
	}
	// Route the browser through the same proxy as the rest of hiq. Without
	// this the browser ignores [network] proxy and goes direct, which fails on
	// sites (incl. GitHub) that are only reachable through a configured proxy.
	if globalBrowserLaunch.proxyServer != "" {
		opts = append(opts, chromedp.Flag("proxy-server", globalBrowserLaunch.proxyServer))
	}
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	// Keep cancel alive for the process lifetime; we never tear down the
	// allocator itself, only individual sessions.
	_ = cancel
	browserPoolCtx = allocCtx
	// Debug: print the launch config so desktop users can see which browser and
	// mode is actually used (the allocator is built lazily on first browser_open,
	// and the desktop app swallows stdout — this line still reaches logs when run
	// from a terminal).
	fmt.Printf("[browser] allocator created: exe=%s headless=%v userDataDir=%q proxy=%q\n",
		exePath, globalBrowserLaunch.headless, globalBrowserLaunch.userDataDir, globalBrowserLaunch.proxyServer)
	return allocCtx, name, nil
}

// startBrowserReaper launches a background goroutine (once) that periodically
// closes idle sessions. It's safe to call repeatedly.
func startBrowserReaper() {
	browserReaperOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				reapIdleBrowserSessions()
			}
		}()
	})
}

func reapIdleBrowserSessions() {
	now := time.Now().Unix()
	var stale []string
	browserMu.Lock()
	for id, s := range browserSessions {
		last := s.lastUsed.Load()
		if now-last > int64(browserIdleTimeout.Seconds()) {
			stale = append(stale, id)
		}
	}
	browserMu.Unlock()
	for _, id := range stale {
		closeBrowserSession(id)
	}
}

// closeBrowserSession tears down a session's tab and removes it from the pool.
// Missing id is a no-op.
func closeBrowserSession(id string) {
	browserMu.Lock()
	s, ok := browserSessions[id]
	if !ok {
		browserMu.Unlock()
		return
	}
	delete(browserSessions, id)
	browserMu.Unlock()
	EmitBrowserPanel(BrowserPanelFrame{Kind: "status", Source: "tool", Phase: "end", SessionID: s.id})
	// Disarm keep-alive before tearing the tab down so the loop observes the
	// stop signal deterministically (it also watches ctx.Done as a backstop).
	s.keepMu.Lock()
	stop := s.keepStop
	s.keepStop = nil
	s.keepOn = false
	s.keepMu.Unlock()
	if stop != nil {
		close(stop)
	}
	// Cancelling the tab context closes the CDP target; the allocator stays up.
	if s.ctxCancel != nil {
		s.ctxCancel()
	}
}

// getBrowserSession returns the session for id, refreshing its lastUsed. It
// returns an error if the session doesn't exist (closed/never opened/expired).
func getBrowserSession(id string) (*browserSession, error) {
	browserMu.Lock()
	s, ok := browserSessions[id]
	browserMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("browser session %q not found (open one with browser_open; idle sessions close after %s)", id, browserIdleTimeout)
	}
	s.lastUsed.Store(time.Now().Unix())
	return s, nil
}

// newBrowserSession creates and registers a fresh Chromium tab.
func newBrowserSession() (*browserSession, error) {
	allocCtx, browserName, err := ensureBrowserAllocator()
	if err != nil {
		// No browser found. If detection was cached but the browser was since
		// uninstalled, re-probe once before giving up — otherwise the user would
		// have to restart the app after installing a browser.
		if errors.Is(err, ErrNoBrowser) && detectedBrowserPath != "" {
			resetBrowserDetection()
			allocCtx, browserName, err = ensureBrowserAllocator()
		}
		if err != nil {
			return nil, err
		}
	}
	// Task context = one tab. Its cancellation closes just this target.
	ctx, cancel := chromedp.NewContext(allocCtx)
	// Force the target to actually connect within a timeout, so a missing or
	// broken browser binary fails here rather than on the first action.
	// NOTE: We must NOT use context.WithTimeout here — in chromedp v0.15.x,
	// canceling ANY derived context kills the Chrome process. Even an expired
	// timeout cancels the parent. So we use a goroutine with time.After instead.
	bootDone := make(chan error, 1)
	go func() {
		bootDone <- chromedp.Run(ctx)
	}()
	select {
	case err := <-bootDone:
		if err != nil {
			cancel()
			resetBrowserDetection()
			return nil, fmt.Errorf("launch %s (try setting CHROME_PATH to a working browser): %w", browserName, err)
		}
	case <-time.After(20 * time.Second):
		cancel()
		resetBrowserDetection()
		return nil, fmt.Errorf("launch %s: timed out after 20s (try setting CHROME_PATH to a working browser)", browserName)
	}
	id := fmt.Sprintf("br_%d", browserSeq.Add(1))
	s := &browserSession{
		id:          id,
		ctx:         ctx,
		ctxCancel:   cancel,
		browser:     browserName,
		stepTracker: newStepTracker(),
	}
	s.lastUsed.Store(time.Now().Unix())
	browserMu.Lock()
	browserSessions[id] = s
	browserMu.Unlock()
	startBrowserReaper()
	startPanelStream(s) // live panel: begin mirroring the initial tab
	// Phase 1: Start dialog auto-accept handler.
	startDialogHandler(s)
	// Downloads: pin a known download dir and capture completion events so the
	// agent learns a download finished (and where the file landed) instead of
	// silently waiting forever after a "click download link" step.
	startDownloadsHandler(s)
	// Phase 6: Start WebSocket keepalive.
	startSessionKeepalive(s)
	EmitBrowserPanel(BrowserPanelFrame{Kind: "status", Source: "tool", Phase: "start", Text: s.browser, SessionID: s.id})
	return s, nil
}

// newAttachedSession connects to an ALREADY-RUNNING browser via the Chrome
// DevTools Protocol, returning a session that drives one of its tabs. This is
// the path for "control the Chrome the user opened themselves": the user (or a
// start-script) launches Chrome with --remote-debugging-port=N, then the agent
// attaches here instead of spawning its own browser. The attached browser keeps
// its full state (cookies, login, extensions, profile) and stays open when the
// session closes — we only drop the CDP websocket.
//
// cdpURL is anything chromedp.NewRemoteAllocator accepts: "http://HOST:PORT",
// "ws://HOST:PORT", or the full ws://.../devtools/browser/<id> URL. chromedp
// auto-resolves it via /json/version. An empty/existing-tab selector picks the
// first available target.
func newAttachedSession(cdpURL string) (*browserSession, error) {
	cdpURL = strings.TrimSpace(cdpURL)
	if cdpURL == "" {
		return nil, errors.New("a CDP debug URL is required (e.g. http://127.0.0.1:9222)")
	}
	// RemoteAllocator connects to the running browser; no process is spawned.
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), cdpURL)
	ctx, cancel := chromedp.NewContext(allocCtx)
	// Force a real connection within a timeout so a wrong host/port fails here
	// with a clear message, not on the first action. The first chromedp.Run on a
	// remote context opens the browser-level websocket and a target.
	bootCtx, bootCancel := context.WithTimeout(ctx, 15*time.Second)
	defer bootCancel()
	if err := chromedp.Run(bootCtx); err != nil {
		cancel()
		allocCancel()
		return nil, fmt.Errorf("attach to %s: %w (is Chrome running with --remote-debugging-port and is the port correct?)", cdpURL, err)
	}
	// Identify which browser we attached to, for the "ready" message. Best-effort:
	// read navigator.userAgent; fall back to a generic label on any error.
	browserName := "Chrome (attached)"
	var ua string
	if err := runBrowserAction(bootCtx, &browserSession{ctx: ctx}, chromedp.Evaluate(`navigator.userAgent`, &ua)); err == nil {
		browserName = guessBrowserFromUA(ua) + " (attached)"
	}
	id := fmt.Sprintf("br_%d", browserSeq.Add(1))
	s := &browserSession{
		id:          id,
		ctx:         ctx,
		ctxCancel:   cancel,
		allocCancel: allocCancel, // dropping the session also frees the allocator
		browser:     browserName,
		attached:    true, // close only disconnects; we never kill the user's browser
	}
	s.lastUsed.Store(time.Now().Unix())
	browserMu.Lock()
	browserSessions[id] = s
	browserMu.Unlock()
	startBrowserReaper()
	startPanelStream(s) // live panel: begin mirroring the initial tab
	// Same background services as a spawned session: dialog auto-accept,
	// download capture, and websocket keepalive. Without these, an attached
	// session dies on the first alert() or on proxies that close idle websockets.
	startDialogHandler(s)
	startDownloadsHandler(s)
	startSessionKeepalive(s)
	EmitBrowserPanel(BrowserPanelFrame{Kind: "status", Source: "tool", Phase: "start", Text: s.browser, SessionID: s.id})
	return s, nil
}

// guessBrowserFromUA maps a navigator.userAgent string to a short display name,
// so the "attached" ready message names the real browser. Falls back to "Chrome"
// since only Chromium browsers speak CDP.
func guessBrowserFromUA(ua string) string {
	ua = strings.ToLower(ua)
	switch {
	case strings.Contains(ua, "edg/"):
		return "Edge"
	case strings.Contains(ua, "brave"):
		return "Brave"
	default:
		return "Chrome"
	}
}

// actionCtx derives the context for a CDP action that is cancelled when EITHER
// the turn is cancelled (turnCtx — i.e. the user clicked Stop) OR the session/
// tab dies (s.ctx) OR the per-action timeout elapses — WITHOUT ever cancelling
// s.ctx's chromedp allocator chain. In chromedp v0.15.x cancelling any context
// derived from the allocator kills the Chrome process, so the action ctx must
// be parented strictly under s.ctx (so only the stored s.ctxCancel/s.allocCancel
// can ever tear the tab/browser down) and turn-cancellation is bridged onto that
// child via a watcher goroutine: a Stop aborts only the in-flight CDP call, the
// browser/tab stays alive for reuse. Tab death (s.ctx cancelled) still cancels
// the action via normal parent→child propagation.
func actionCtx(turnCtx context.Context, s *browserSession) (context.Context, context.CancelFunc) {
	merged, mergeCancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	go func() {
		select {
		case <-turnCtx.Done():
			mergeCancel() // turn cancelled → abort only this CDP call, NOT the browser
		case <-done:
		}
	}()
	actx, timeoutCancel := context.WithTimeout(merged, browserActionTimeout)
	return actx, func() {
		timeoutCancel()
		mergeCancel()
		close(done)
	}
}

// runBrowserAction runs a chromedp action list against session s. ctx is the
// turn context: cancelling it (Stop) aborts the in-flight CDP call promptly
// instead of waiting out browserActionTimeout, while leaving the browser alive.
func runBrowserAction(ctx context.Context, s *browserSession, actions ...chromedp.Action) error {
	if s.ctx.Err() != nil {
		return fmt.Errorf("browser session context already canceled: %w", s.ctx.Err())
	}
	actx, cancel := actionCtx(ctx, s)
	defer cancel()
	err := chromedp.Run(actx, actions...)
	if err == nil {
		mirrorAfterAction(s)
	}
	return err
}

// mirrorAfterAction pushes a post-action viewport screenshot to the in-app
// mirror panel. Best-effort by design: a capture failure (downloads, PDF
// viewers, detached sessions) never fails the tool call, and with no sink
// registered (CLI) the whole hook is one map lookup. Sessions not in the
// registry are skipped — newAttachedSession probes with a throwaway session
// before registering, and that pre-registration screenshot is noise.
func mirrorAfterAction(s *browserSession) {
	if browserPanelSink == nil {
		return
	}
	browserMu.Lock()
	_, registered := browserSessions[s.id]
	browserMu.Unlock()
	if !registered {
		return
	}
	mctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	var buf []byte
	var url string
	if err := chromedp.Run(mctx,
		chromedp.CaptureScreenshot(&buf),
		chromedp.Location(&url),
	); err != nil {
		return
	}
	EmitBrowserPanel(BrowserPanelFrame{
		Kind:      "frame",
		Source:    "tool",
		URL:       url,
		Image:     "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf),
		SessionID: s.id,
	})
}

// waitForBodyContent blocks until the <body> has meaningful content (SPA apps
// render asynchronously, so right after Navigate the DOM can be near-empty).
// It replaces a blind time.Sleep: chromedp.Poll re-evaluates a JS predicate
// every pollInterval and returns the instant body length exceeds the threshold,
// so a fast page completes in tens of ms instead of always waiting the full
// cap. The cap is a worst-case backstop mirroring the old fixed sleeps.
func waitForBodyContent(ctx context.Context, s *browserSession, minLen int, cap time.Duration) {
	pollCtx, cancel := context.WithTimeout(ctx, cap)
	defer cancel()
	// Returns true (truthy) once non-whitespace body content exceeds minLen.
	const expr = `document.body && document.body.innerHTML.replace(/\s/g,'').length > %d`
	var ok bool
	_ = runBrowserAction(pollCtx, s, chromedp.Poll(
		fmt.Sprintf(expr, minLen),
		&ok,
		chromedp.WithPollingInterval(150*time.Millisecond),
	))
}

// --- retry helpers -----------------------------------------------------------

// retryConfig holds retry parameters for browser actions.
type retryConfig struct {
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
}

var defaultRetry = retryConfig{
	maxAttempts: 3,
	baseDelay:   500 * time.Millisecond,
	maxDelay:    3 * time.Second,
}

// runWithRetry wraps a browser action with exponential backoff retry.
// It retries on transient errors (context deadline, element not found,
// connection errors) but fails fast on permanent errors.
func runWithRetry(ctx context.Context, s *browserSession, cfg retryConfig, action func() error) error {
	var lastErr error
	for attempt := 0; attempt < cfg.maxAttempts; attempt++ {
		if err := action(); err != nil {
			lastErr = err
			if !isRetryable(err) {
				return err // permanent error, fail fast
			}
			if attempt < cfg.maxAttempts-1 {
				delay := cfg.baseDelay * time.Duration(1<<uint(attempt))
				if delay > cfg.maxDelay {
					delay = cfg.maxDelay
				}
				select {
				case <-time.After(delay):
					continue
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		} else {
			return nil
		}
	}
	return fmt.Errorf("after %d attempts: %w", cfg.maxAttempts, lastErr)
}

// isRetryable classifies errors as transient (worth retrying) or permanent.
func isRetryable(err error) bool {
	errStr := err.Error()
	retryablePatterns := []string{
		"context deadline exceeded",
		"element not found",
		"no node found",
		"node is not an HTMLElement",
		"connection refused",
		"connection reset",
		"i/o timeout",
		"target closed",
		"session not found",
	}
	for _, p := range retryablePatterns {
		if strings.Contains(errStr, p) {
			return true
		}
	}
	return false
}

// ensureSession returns a healthy session, detecting dead tabs early.
func ensureSession(id string) (*browserSession, error) {
	s, err := getBrowserSession(id)
	if err != nil {
		return nil, err
	}
	if s.ctx.Err() != nil {
		closeBrowserSession(id)
		return nil, fmt.Errorf("browser session %q died (tab closed or crashed); reopen with browser_open", id)
	}
	return s, nil
}

// --- shared arg helpers -----------------------------------------------------

// selectorFromArgs resolves a target spec into one of three forms:
//   - a snapshot ref (string like "e5", from browser_snapshot) → isRef=true
//   - a CSS/XPath selector string → selector set, both flags false
//   - a coordinate pair {x, y} → isCoord=true
//
// A plain string is classified by shape: a leading "e" followed by digits is a
// ref, otherwise a selector. This three-way form lets the model target elements
// by ref (most reliable, from the accessibility tree), selector (when known), or
// coordinate (from a VLM screenshot) — whichever it has.
func selectorFromArgs(raw json.RawMessage) (selector string, x, y float64, isCoord, isRef bool, err error) {
	// Try string first.
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		s = strings.TrimSpace(s)
		if looksLikeRef(s) {
			return s, 0, 0, false, true, nil
		}
		return s, 0, 0, false, false, nil
	}
	// Try {x, y}.
	var c struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	if json.Unmarshal(raw, &c) == nil && (c.X != 0 || c.Y != 0) {
		return "", c.X, c.Y, true, false, nil
	}
	return "", 0, 0, false, false, fmt.Errorf("expected a ref (e.g. \"e5\"), a selector string, or a {x, y} object")
}

// looksLikeRef reports whether s is a snapshot ref: "e" followed by one or more
// digits. Kept strict so CSS selectors starting with "e" (rare but possible,
// like "email-input") aren't misclassified — those don't match ^e\d+$.
func looksLikeRef(s string) bool {
	if len(s) < 2 || s[0] != 'e' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// --- tools ------------------------------------------------------------------

// browserOpen
type browserOpen struct{}

func (browserOpen) Name() string { return "browser_open" }

func (browserOpen) Description() string {
	return "Launch a browser tab (Chromium via Chrome DevTools Protocol) and return a session id used by the other browser_* tools. " +
		"Pass an optional url to navigate immediately. Auto-detects an installed Chromium-based browser (Chrome, then Edge, then Brave); set the CHROME_PATH env var to force a specific one. " +
		"Use for web research, form filling, scraping, and any task needing a real browser. " +
		"ROUTING: only open a browser when the user explicitly asks to operate one, or when a task needs real browser rendering (login, JS-heavy pages, clicks). " +
		"For plain information lookup use web_search; for reading a URL's content use web_fetch — both are cheaper and don't need a browser session. " +
		"The session stays open for 10 minutes of inactivity; reuse its id across calls."
}

func (browserOpen) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "url":{"type":"string","description":"Optional URL to navigate to on open (absolute http(s) or about:blank). Omit for a blank tab."}
},
"required":[]
}`)
}

func (browserOpen) ReadOnly() bool { return false } // spawns a process

func (browserOpen) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		URL string `json:"url"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
	}
	s, err := newBrowserSession()
	if err != nil {
		// When no browser is found, point the agent at the recovery flow: ask
		// the user for a browser path, then browser_set_path it. This is the
		// "guide the user to input a Chromium browser" requirement.
		if errors.Is(err, ErrNoBrowser) {
			return "", fmt.Errorf("%w\n\nTo fix: ask the user for the path to their Chrome or Edge executable (e.g. on Windows: \"C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe\" or \"C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe\"), then call browser_set_path with it and retry browser_open", err)
		}
		return "", err
	}
	if strings.TrimSpace(p.URL) != "" {
		p.URL = normalizeNavURL(p.URL)
		if err := runBrowserAction(ctx, s, chromedp.Navigate(p.URL)); err != nil {
			closeBrowserSession(s.id)
			return "", fmt.Errorf("navigate to %s: %w", p.URL, err)
		}
	}
	return fmt.Sprintf("browser session %q ready (driving %s)%s", s.id, s.browser, navSuffix(p.URL)), nil
}

func navSuffix(url string) string {
	if strings.TrimSpace(url) == "" {
		return " (blank tab)"
	}
	return fmt.Sprintf(" at %s", url)
}

// browserAttach connects to an already-running browser that the user launched
// with a CDP debug port (e.g. Chrome started with --remote-debugging-port=9222),
// and drives one of its tabs. This is how the agent controls "the browser the
// user already has open" — with full login state, cookies, extensions, and
// profile — rather than spawning a fresh one. The returned session_id works
// with all the other browser_* tools identically to one from browser_open.
//
// When to use attach vs open:
//   - browser_open: the agent needs a throwaway browser; state is lost on close.
//   - browser_attach: the user wants the agent to drive THEIR browser (already
//     logged into GitHub/their portal), or a browser they preconfigured. The
//     browser stays open and untouched when the session ends.
//
// The user must start their browser with a debug port first. On Windows:
//
//	chrome.exe --remote-debugging-port=9222
//
// Then call browser_attach with "http://127.0.0.1:9222" (or just the host:port).
type browserAttach struct{}

func (browserAttach) Name() string { return "browser_attach" }

func (browserAttach) Description() string {
	return "Attach to an ALREADY-RUNNING browser launched with a CDP debug port, instead of spawning a new one. Use when the user has a browser they want you to drive — one they're already logged into, or one they preconfigured. The browser keeps its full state (cookies/login/extensions) and stays open when the session ends; you only disconnect. Pass the CDP URL (e.g. \"http://127.0.0.1:9222\" or \"127.0.0.1:9222\"). The user must start their browser with --remote-debugging-port=PORT first. The returned session_id works with all browser_* tools just like browser_open's."
}

func (browserAttach) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "cdp_url":{"type":"string","description":"CDP debug URL of the running browser. Accepts http://HOST:PORT, ws://HOST:PORT, or bare HOST:PORT. The browser must have been started with --remote-debugging-port=PORT."}
},
"required":["cdp_url"]
}`)
}

func (browserAttach) ReadOnly() bool { return false } // opens a network connection

func (browserAttach) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		CDPURL string `json:"cdp_url"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
	}
	url := normalizeCDPURL(strings.TrimSpace(p.CDPURL))
	if url == "" {
		return "", errors.New("cdp_url is required (e.g. http://127.0.0.1:9222)")
	}
	s, err := newAttachedSession(url)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("attached to browser session %q (driving %s) — this browser stays open when you close the session", s.id, s.browser), nil
}

// normalizeCDPURL accepts the loose forms a user/model is likely to pass
// ("127.0.0.1:9222", "9222", "http://127.0.0.1:9222") and returns the form
// chromedp.NewRemoteAllocator expects. A bare port assumes localhost. Schemes
// other than ws/wss/http are left untouched (let chromedp validate them).
func normalizeCDPURL(s string) string {
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		return s // already has a scheme chromedp understands
	}
	// Bare port → localhost.
	if _, err := strconv.Atoi(s); err == nil {
		return "http://127.0.0.1:" + s
	}
	// host:port → assume http (chromedp upgrades to ws via /json/version).
	return "http://" + s
}

// browserNavigate
type browserNavigate struct{}

func (browserNavigate) Name() string { return "browser_navigate" }

func (browserNavigate) Description() string {
	return "Navigate an OPEN browser session to a URL. By default the current tab is reused; set new_tab=true (or the user's 打开网页时 setting) to open the URL in a fresh tab and drive it there, keeping the old page open. " +
		"Use ONLY when a browser session is already open or the user explicitly asks to operate a browser. " +
		"Do NOT use this to read a URL's content — use web_fetch for that. " +
		"Do NOT use this to search the internet — use web_search for that. " +
		"Navigation invalidates ALL element refs — you MUST call browser_snapshot before any subsequent click/type/select actions. " +
		"The result includes an auto page summary so you can verify the navigation succeeded."
}

func (browserNavigate) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Session id from browser_open"},
  "url":{"type":"string","description":"Absolute URL to navigate to"},
  "new_tab":{"type":"boolean","description":"Open the URL in a NEW tab instead of the current one. Optional; defaults to the user's configured 打开网页时 behavior."}
},
"required":["session_id","url"]
}`)
}

func (browserNavigate) ReadOnly() bool { return false }

// normalizeNavURL makes scheme-less input navigable: "example.com" →
// "https://example.com" (the sensible modern default). Scheme-ful URLs and
// scheme-less pseudo targets (about:, data:, file:, chrome:, …) pass through.
// Without this, CDP rejects bare domains with the raw English
// "Cannot navigate to invalid URL (-32000)".
func normalizeNavURL(u string) string {
	t := strings.TrimSpace(u)
	if t == "" {
		return u
	}
	low := strings.ToLower(t)
	if strings.Contains(low, "://") ||
		strings.HasPrefix(low, "about:") || strings.HasPrefix(low, "data:") ||
		strings.HasPrefix(low, "javascript:") || strings.HasPrefix(low, "file:") ||
		strings.HasPrefix(low, "chrome:") || strings.HasPrefix(low, "edge:") ||
		strings.HasPrefix(low, "devtools:") {
		return t
	}
	return "https://" + t
}

func (browserNavigate) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
		URL       string `json:"url"`
		NewTab    *bool  `json:"new_tab"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" || p.URL == "" {
		return "", errors.New("session_id and url are required")
	}
	p.URL = normalizeNavURL(p.URL)
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	// Tab policy: explicit new_tab arg wins; otherwise the settings default
	// ("打开网页时": current tab vs 新开标签页).
	newTab := globalBrowserLaunch.openLinksIn == "new"
	if p.NewTab != nil {
		newTab = *p.NewTab
	}
	if newTab {
		if err := openSessionTab(s, "about:blank"); err != nil {
			return "", wrapError(s, "navigate", p.URL, fmt.Errorf("open new tab: %w", err))
		}
	}
	if err := runBrowserAction(ctx, s, chromedp.Navigate(p.URL), chromedp.WaitReady("body")); err != nil {
		return "", wrapError(s, "navigate", p.URL, fmt.Errorf("navigate: %w", err))
	}
	// Phase 5: Check for empty DOM after navigation (SPA race condition).
	var bodyHTML string
	if err := runBrowserAction(ctx, s, chromedp.OuterHTML("body", &bodyHTML)); err == nil {
		if len(strings.TrimSpace(bodyHTML)) < 50 {
			// Wait for the SPA to populate <body>. Polling returns the moment
			// content appears (cap mirrors the old fixed 3s as a backstop).
			waitForBodyContent(ctx, s, 50, 3*time.Second)
			_ = runBrowserAction(ctx, s, chromedp.OuterHTML("body", &bodyHTML))
			if len(strings.TrimSpace(bodyHTML)) < 50 {
				// Still empty after the wait — reload and give it another chance.
				_ = runBrowserAction(ctx, s, chromedp.Reload(), chromedp.WaitReady("body"))
				waitForBodyContent(ctx, s, 50, 2*time.Second)
			}
		}
	}
	// Refs from any prior snapshot are now stale.
	s.refs.Store(nil)
	return wrapResult(s, "navigate", p.URL, fmt.Sprintf("navigated to %s", p.URL)), nil
}

// browserClick
type browserClick struct{}

func (browserClick) Name() string { return "browser_click" }

func (browserClick) Description() string {
	return "Click an element in an open browser session. The target is one of: (1) a snapshot ref like \"e5\" from browser_snapshot — PREFERRED, unambiguous and doesn't need guessing selectors; (2) a CSS selector string (e.g. \"button#submit\"); (3) a coordinate object {x, y} from a screenshot. Refs are the most reliable: take a snapshot, read the accessibility tree, and click by ref. Use coordinates only when the element has no selector or ref. The result includes a [page: ...] summary — read it to verify the click had the expected effect. If the page navigated unexpectedly, re-snapshot."
}

func (browserClick) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Browser session id from browser_open"},
  "target":{"description":"A snapshot ref (\"e5\"), a CSS selector (\"button.submit\"), or {\"x\":320,\"y\":240}. Prefer refs from browser_snapshot.","oneOf":[{"type":"string"},{"type":"object","properties":{"x":{"type":"number"},"y":{"type":"number"}},"required":["x","y"]}]}
},
"required":["session_id","target"]
}`)
}

func (browserClick) ReadOnly() bool { return false }

func (browserClick) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string          `json:"session_id"`
		Target    json.RawMessage `json:"target"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" {
		return "", errors.New("session_id is required")
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	sel, x, y, isCoord, isRef, err := selectorFromArgs(p.Target)
	if err != nil {
		return "", err
	}
	// Phase 4: Record URL before click for page change detection.
	// Also snapshot the open "page" targets so we can detect a target=_blank
	// click that opens a new tab — the URL won't change on the current tab,
	// so waitForPageLoad alone misses it. targetsFromContext returns the
	// page-type target IDs we were driving before the click.
	urlBefore := ""
	_ = runBrowserAction(ctx, s, chromedp.Location(&urlBefore))
	tabsBefore := pageTargetIDs(s)

	var baseResult string
	var actionLabel string
	if isRef {
		// file inputs must not be clicked — clicking one opens a native file
		// chooser dialog that blocks the browser process and deadlocks the
		// session (CDP can't dismiss it). Detect and refuse early so the agent
		// learns it needs a different approach instead of hanging.
		if isFileInputRef(ctx, s, sel) {
			return "", fmt.Errorf("ref %q is a file input (<input type=\"file\">); clicking it opens a native dialog that blocks the browser. File upload is not supported via click — use a dedicated upload mechanism if available, or skip this element", sel)
		}
		// Scroll into view so the click coordinates actually hit the element.
		// Without this, a button below the fold or in a scroll container is
		// clicked at the wrong viewport position.
		scrollRefIntoView(ctx, s, sel)
		// Check occlusion before clicking: if the element's center is covered by
		// another element (modal overlay, cookie banner, sticky header), a
		// coordinate-based click would hit the wrong target. We still proceed
		// with this.click() (JS dispatch bypasses the visual stack), but warn
		// the agent so it knows the click may not have reached the intended
		// handler — some frameworks check event.isTrusted or elementFromPoint.
		occluded := isRefOccluded(ctx, s, sel)
		// For checkbox/radio, capture pre-click state so we can verify the
		// toggle actually took effect after the click. Some frameworks
		// intercept clicks (animations, async validation) and the checked
		// state doesn't change even though click() returned. Reporting the
		// actual post-click state saves the agent a re-snapshot round-trip
		// and surfaces "I clicked but it didn't toggle" failures immediately.
		clickJS := `function() {
			var before = null;
			var isToggle = this.tagName === 'INPUT' && (this.type === 'checkbox' || this.type === 'radio');
			if (isToggle) { before = this.checked ? 'checked' : 'unchecked'; }
			this.click();
			var after = null;
			if (isToggle) { after = this.checked ? 'checked' : 'unchecked'; }
			if (isToggle) {
				return JSON.stringify({toggle: true, before: before, after: after, changed: before !== after});
			}
			return JSON.stringify({toggle: false, html: this.outerHTML.slice(0, 80)});
		}`
		// runWithRetry is safe here: it only retries on error (the action func
		// returns nil on success), so a click that landed is never re-clicked —
		// no risk of double-toggling a checkbox. The retry covers transient
		// failures (context deadline, element detached, connection hiccup).
		var clickResult string
		if err := runWithRetry(ctx, s, defaultRetry, func() error {
			var err error
			clickResult, err = callOnRef(ctx, s, sel, clickJS)
			return err
		}); err != nil {
			return "", wrapError(s, "click", sel, fmt.Errorf("click ref %q: %w", sel, err))
		}
		baseResult = formatClickResult(clickResult, sel)
		if occluded {
			baseResult += " ⚠️ Element appears occluded (covered by another element at its center). The JS click was dispatched but may be ignored by frameworks that verify pointer events. Verify the click took effect; if not, dismiss the overlay (modal/cookie banner) first."
		}
		actionLabel = sel
	} else if isCoord {
		if err := runWithRetry(ctx, s, defaultRetry, func() error {
			return runBrowserAction(ctx, s, chromedp.MouseClickXY(x, y))
		}); err != nil {
			return "", wrapError(s, "click", fmt.Sprintf("%.0f,%.0f", x, y), fmt.Errorf("click (%.0f, %.0f): %w", x, y, err))
		}
		baseResult = fmt.Sprintf("clicked (%.0f, %.0f)", x, y)
		actionLabel = fmt.Sprintf("%.0f,%.0f", x, y)
	} else {
		if err := runWithRetry(ctx, s, defaultRetry, func() error {
			return runBrowserAction(ctx, s, chromedp.Click(sel))
		}); err != nil {
			return "", wrapError(s, "click", sel, fmt.Errorf("click %q: %w", sel, err))
		}
		baseResult = fmt.Sprintf("clicked %q", sel)
		actionLabel = sel
	}
	// Phase 4: Detect unexpected page navigation after click.
	// Use waitForPageLoad to let the new page finish loading before checking.
	urlAfter, navigated := waitForPageLoad(s, urlBefore)
	if navigated {
		baseResult += fmt.Sprintf("\n⚠️ Page navigated: %s → %s. Run browser_snapshot to see the new page.", truncate(urlBefore, 50), truncate(urlAfter, 50))
		s.refs.Store(nil)
	} else if urlAfter != "" && urlBefore == urlAfter {
		// URL unchanged — check whether a new tab opened (target=_blank).
		// Switching the session's driving target requires rebuilding the
		// chromedp context off the allocator (sessions don't currently hold
		// the allocator ref in attach mode), which is a larger change. For
		// now, surface the new tab + its URL so the agent knows the click had
		// a side effect and can navigate to it explicitly if needed.
		if newTabs := newPageTargetsSince(s, tabsBefore); len(newTabs) > 0 {
			// Follow the tab the click opened — a human follows the popup;
			// staying on the opener tab made every later action land on the
			// wrong page. The old tab stays open (browser_switch_tab goes back).
			followed := newTabs[len(newTabs)-1]
			if swerr := switchSessionTab(s, followed.TargetID); swerr == nil {
				baseResult += fmt.Sprintf("\n🆕 Followed the new tab opened by the click: %s — the session now drives it (browser_switch_tab switches back)", truncate(firstNonEmptyStr(followed.Title, followed.URL), 80))
			} else {
				for _, t := range newTabs {
					baseResult += fmt.Sprintf("\n🔗 Click opened a new tab: %s — %s (follow failed: %v; use browser_switch_tab)", truncate(t.Title, 50), truncate(t.URL, 80), swerr)
				}
			}

		} else if isRef && refLooksLikeSubmit(ctx, s, sel) {
			// No navigation, no new tab, but the clicked element looks like a
			// form submit button (type=submit, or inside a <form>, or has a
			// submit-ish name/class). Most likely an SPA/AJAX submit whose
			// result hasn't rendered yet. Nudge the agent to wait for the
			// network to settle before reading the result — otherwise it sees
			// the pre-submit state and wrongly concludes the click did nothing.
			baseResult += "\n📝 URL unchanged after a submit-like click — if this is an SPA/AJAX form, run browser_wait(condition=\"networkidle\") before checking the result, since the validation response renders asynchronously."
		}
		// else: URL unchanged, no new tab, not a submit — a no-op click
		// (button disabled, JS handler swallowed it, etc.). Don't say anything
		// misleading.
	}
	return wrapResult(s, "click", actionLabel, baseResult), nil
}

// browserType
type browserType struct{}

func (browserType) Name() string { return "browser_type" }

func (browserType) Description() string {
	return "Type text into an input element in an OPEN browser session. " +
		"Do NOT use this to search the internet — typing into a page's search box drives that specific site only; use web_search for general information lookup. " +
		"Prefer passing a ref (from browser_snapshot) — it targets the exact element without guessing selectors. Set clear=true to empty the field first. Dispatches input + change events so React/Vue style frameworks register the value. The result includes a [page: ...] summary for verification."
}

func (browserType) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Browser session id from browser_open"},
  "ref":{"type":"string","description":"Snapshot ref of the input element (e.g. \"e5\"), from browser_snapshot. Preferred over selector."},
  "selector":{"type":"string","description":"CSS selector of the input element. Used when no ref is given. Omit both ref and selector to type into the currently-focused element."},
  "text":{"type":"string","description":"Text to type"},
  "clear":{"type":"boolean","description":"Clear the field before typing (default false)"}
},
"required":["session_id","text"]
}`)
}

func (browserType) ReadOnly() bool { return false }

func (browserType) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Ref       string `json:"ref"`
		Selector  string `json:"selector"`
		Text      string `json:"text"`
		Clear     bool   `json:"clear"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" {
		return "", errors.New("session_id is required")
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	ref := strings.TrimSpace(p.Ref)
	sel := strings.TrimSpace(p.Selector)

	// Ref path: resolve ref → DOM node, set value + dispatch input/change events.
	// The event dispatch is essential for React/Vue/Svelte which read from events,
	// not from the DOM value directly (a bare .value= wouldn't register). We use
	// the native value setter from the prototype so React-controlled inputs pick
	// up the change — React tracks value via its own setter, and the documented
	// workaround is to call the prototype's setter then dispatch input.
	if ref != "" {
		// Scroll into view first so the field is in the viewport (matters for
		// scroll-into-view autofocus and for any subsequent click on the field).
		scrollRefIntoView(ctx, s, ref)
		fn := "function(text, clear) {\n  " + typeRefJSBody + "\n}"
		result, err := callOnRef(ctx, s, ref, fn, p.Text, p.Clear)
		if err != nil {
			return "", wrapError(s, "type", ref, fmt.Errorf("type into ref %q: %w", ref, err))
		}
		// Parse the structured return to detect value mismatch. The JS returns
		// {value, expectFormatChange}; for fields that legitimately reformat
		// input (date pickers, contenteditable), a mismatch is expected and
		// NOT flagged — only "plain text field that silently changed my input"
		// is suspicious (auto-complete, masked input, framework override).
		msg := formatTypeResult(result, p.Text, ref)
		// Autocomplete/combobox fields need a brief delay for the JS-driven
		// suggestion dropdown to populate after the input event. Without this,
		// a follow-up click on a suggestion misses because the list hasn't
		// rendered yet. Mirror browser-use's 0.4s heuristic. Also surface a
		// behavioral hint so the agent knows to wait for suggestions rather
		// than pressing Enter (which may submit the form prematurely).
		if isAutocompleteRef(ctx, s, ref) {
			time.Sleep(400 * time.Millisecond)
			msg += "\n💡 Autocomplete field — wait for the suggestion dropdown, then click the correct suggestion. Do NOT press Enter (it may submit the form)."
		}
		return wrapResult(s, "type", ref, msg), nil
	}

	// Selector / focused-element path. Uses the SAME hardened JS as the ref path
	// (prototype setter for React, contenteditable branch, HTML5/jQuery special
	// inputs, structured return for value-mismatch detection) — only the element
	// lookup differs (querySelector / activeElement instead of ref→backendNodeId).
	// Previously this path used chromedp.SendKeys (CDP Input.insertText), which
	// React/Vue controlled inputs silently ignore. Keeping both paths on the
	// same JS eliminates the "ref works, selector doesn't" split.
	var locator string
	if sel != "" {
		// Validate the selector resolves to exactly one element; querySelector
		// returns the first match, which is what we want.
		locator = fmt.Sprintf("(document.querySelector(%q) || document.activeElement)", sel)
	} else {
		locator = "document.activeElement"
	}
	typeJS := fmt.Sprintf(`(function(text, clear){
		var el = %s;
		if (!el || el === document.body) { return JSON.stringify({value:'', error:'no focused element'}); }
		%s
	})(%q, %v)`,
		locator,
		// Inline the same body used by the ref path. Defined once as
		// typeRefJSBody so both paths stay byte-identical.
		typeRefJSBody,
		p.Text, p.Clear,
	)
	var rawResult any
	if err := runBrowserAction(ctx, s, chromedp.Evaluate(typeJS, &rawResult)); err != nil {
		return "", wrapError(s, "type", sel, fmt.Errorf("type: %w", err))
	}
	resultStr := fmt.Sprintf("%v", rawResult)
	msg := formatTypeResult(resultStr, p.Text, sel)
	if isAutocompleteSelector(ctx, s, sel) {
		time.Sleep(400 * time.Millisecond)
		msg += "\n💡 Autocomplete field — wait for the suggestion dropdown, then click the correct suggestion. Do NOT press Enter (it may submit the form)."
	}
	return wrapResult(s, "type", sel, msg), nil
}

func fieldSuffix(sel string, cleared bool) string { //nolint:unused
	var parts []string
	if sel != "" {
		parts = append(parts, fmt.Sprintf(" into %q", sel))
	}
	if cleared {
		parts = append(parts, " (cleared first)")
	}
	return strings.Join(parts, "")
}

// formatTypeResult parses the structured return from browser_type's ref-path
// JS and builds the human-readable result line, including a value-mismatch
// warning when the field's actual value differs from what was typed.
//
// The JS returns JSON.stringify({value, expectFormatChange}). callOnRef wraps
// that as a JSON string (so res.Value is "\"{\\\"value\\\":...}\"" — a quoted
// string containing JSON). We unwrap the outer quotes, then parse the inner
// object.
//
// Mismatch handling:
//   - expectFormatChange=true (date pickers, contenteditable): the field is
//     EXPECTED to reformat the input (e.g. "2026-7-5" → "2026-07-05"), so a
//     mismatch is not flagged — it would cry wolf on every date input.
//   - expectFormatChange=false (plain text/number/email): a mismatch means
//     the page silently altered the input (auto-format phone numbers, mask
//     credit cards, framework value transforms). Surface it so the agent
//     doesn't assume its text landed verbatim and submit wrong data.
//
// Falls back gracefully if the result isn't the expected JSON shape (older
// callers, edge cases) — just shows the raw value without mismatch analysis.
func formatTypeResult(rawResult, typedText, ref string) string {
	// callOnRef returns the raw JSON value. For a JS string return, that's a
	// quoted JSON string; unwrap one layer of quotes first.
	inner := unwrapJSONString(rawResult)
	var parsed struct {
		Value              string `json:"value"`
		ExpectFormatChange bool   `json:"expectFormatChange"`
	}
	if err := json.Unmarshal([]byte(inner), &parsed); err != nil {
		// Not the structured JSON we expected — show the raw value.
		return fmt.Sprintf("typed %d chars into ref %q (value now: %s)", len(typedText), ref, truncate(inner, 60))
	}
	msg := fmt.Sprintf("typed %d chars into ref %q (value now: %s)", len(typedText), ref, truncate(parsed.Value, 60))
	// Value mismatch check — only for plain fields where reformatting is NOT
	// expected. Trimming handles trivial whitespace differences (some inputs
	// add trailing space); a real reformat will differ by more than whitespace.
	if !parsed.ExpectFormatChange && strings.TrimSpace(parsed.Value) != strings.TrimSpace(typedText) {
		msg += fmt.Sprintf("\n⚠️ Note: the field's actual value %q differs from the typed text %q — the page may have reformatted, autocompleted, or masked the input. Verify before submitting.", truncate(parsed.Value, 40), truncate(typedText, 40))
	}
	return msg
}

// isRefOccluded reports whether the element pointed to by ref is visually
// covered at its center by a different element. Uses getBoundingClientRect +
// document.elementsFromPoint: if the topmost element at the center isn't the
// ref element itself (or a descendant), something is on top of it (modal,
// sticky header, cookie banner). Best-effort: any probe failure returns false
// (no warning) so we never block a click on an unprobeable element.
func isRefOccluded(ctx context.Context, s *browserSession, ref string) bool {
	js := `function() {
		var rect = this.getBoundingClientRect();
		if (!rect || rect.width === 0 || rect.height === 0) return 'false';
		var cx = rect.left + rect.width / 2;
		var cy = rect.top + rect.height / 2;
		var stack = document.elementsFromPoint(cx, cy);
		if (!stack || stack.length === 0) return 'false';
		// The topmost element at the center — if it's not this element or a
		// descendant of this element, something is covering it.
		var top = stack[0];
		if (top === this || this.contains(top)) return 'false';
		return 'true';
	}`
	result, err := callOnRef(ctx, s, ref, js)
	return err == nil && strings.TrimSpace(result) == "true"
}

// formatClickResult parses the structured return from browser_click's ref-path
// JS and builds the result line. For checkbox/radio it reports the actual
// toggle outcome (before → after), including a warning when the click didn't
// change the state (framework intercepted it). For other elements it shows a
// snippet of the clicked element's HTML for verification.
//
// The JS returns JSON.stringify({toggle, before, after, changed, html}).
// callOnRef wraps it as a quoted JSON string; we unwrap one layer then parse.
// Falls back to a plain "clicked ref" message if parsing fails.
func formatClickResult(rawResult, ref string) string {
	inner := unwrapJSONString(rawResult)
	var parsed struct {
		Toggle  bool   `json:"toggle"`
		Before  string `json:"before"`
		After   string `json:"after"`
		Changed bool   `json:"changed"`
		HTML    string `json:"html"`
	}
	if err := json.Unmarshal([]byte(inner), &parsed); err != nil {
		return fmt.Sprintf("clicked ref %q", ref)
	}
	if !parsed.Toggle {
		return fmt.Sprintf("clicked ref %q (%s)", ref, truncate(parsed.HTML, 60))
	}
	msg := fmt.Sprintf("clicked ref %q — %s → %s", ref, parsed.Before, parsed.After)
	if !parsed.Changed {
		// The click ran but checked state didn't change. Common causes: the
		// element is disabled, a framework intercepted the click (animation,
		// async validation), or it's a custom toggle that needs a real pointer
		// event. Surface it so the agent doesn't assume the toggle succeeded.
		msg += " ⚠️ State did NOT change after click — the element may be disabled, or a framework intercepted the click. Verify before relying on the toggle."
	}
	return msg
}

// isFileInputRef reports whether the ref points at an <input type="file">.
// Used by browser_click to refuse the click before it opens a native file
// chooser — that dialog blocks the browser process and deadlocks the session
// (CDP has no way to dismiss it). Best-effort: on any probe failure, returns
// false so the click proceeds (the click may still be the right action for a
// non-file element that the probe couldn't read).
func isFileInputRef(ctx context.Context, s *browserSession, ref string) bool {
	result, err := callOnRef(ctx, s, ref, "function() { var t=(this.tagName||'').toLowerCase(); var ty=(this.getAttribute&&this.getAttribute('type'))||''; return t==='input' && ty.toLowerCase()==='file' ? 'true' : 'false'; }")
	return err == nil && strings.TrimSpace(result) == "true"
}

// refLooksLikeSubmit reports whether the ref points at an element that looks
// like a form submit trigger — used to nudge the agent toward waiting for an
// async submit response when the URL didn't change. Considers: type=submit on
// a button/input, an element nested inside a <form>, or a button whose
// accessible name/text mentions submit/登录/提交/保存/确认. Best-effort: any
// probe failure returns false (no nudge), which is always the safe default.
func refLooksLikeSubmit(ctx context.Context, s *browserSession, ref string) bool {
	result, err := callOnRef(ctx, s, ref, `function() {
		var tag = (this.tagName || '').toLowerCase();
		var type = (this.getAttribute && this.getAttribute('type')) || '';
		type = type.toLowerCase();
		if (tag === 'button' && type === 'submit') return 'true';
		if (tag === 'input' && type === 'submit') return 'true';
		// Element lives inside a <form> → likely a submit trigger when clicked.
		if (this.closest && this.closest('form')) return 'true';
		// Submit-ish visible label.
		var label = (this.textContent || this.value || '').trim().slice(0, 20).toLowerCase();
		var keywords = ['submit','sign in','log in','login','register','save','confirm','next','继续','登录','提交','保存','确认','下一步','注册'];
		for (var i = 0; i < keywords.length; i++) {
			if (label.indexOf(keywords[i]) !== -1) return 'true';
		}
		return 'false';
	}`)
	return err == nil && strings.TrimSpace(result) == "true"
}

// isAutocompleteRef reports whether the ref points at a field that spawns a
// JS-driven suggestion dropdown after input (role=combobox, aria-autocomplete,
// or has a datalist). Used by browser_type to wait briefly so the dropdown can
// render before the agent tries to click a suggestion. Same best-effort policy
// as isFileInputRef: probe failure → false (no wait).
func isAutocompleteRef(ctx context.Context, s *browserSession, ref string) bool {
	result, err := callOnRef(ctx, s, ref, "function() { if(this.getAttribute('role')==='combobox')return'true'; var ac=this.getAttribute('aria-autocomplete'); if(ac&&ac!=='none')return'true'; if(this.list)return'true'; return'false'; }")
	return err == nil && strings.TrimSpace(result) == "true"
}

// isAutocompleteSelector is the selector-path counterpart of isAutocompleteRef.
// Runs the same attribute check via querySelector so the selector path of
// browser_type gets the same autocomplete delay + behavioral hint. sel may be
// empty (focused-element path) — returns false then since we can't probe.
func isAutocompleteSelector(ctx context.Context, s *browserSession, sel string) bool {
	if strings.TrimSpace(sel) == "" {
		return false
	}
	var result string
	checkJS := fmt.Sprintf(`(function(){var el=document.querySelector(%q);if(!el)return'false';if(el.getAttribute('role')==='combobox')return'true';var ac=el.getAttribute('aria-autocomplete');if(ac&&ac!=='none')return'true';if(el.list)return'true';return'false';})()`, sel)
	if err := runBrowserAction(ctx, s, chromedp.Evaluate(checkJS, &result)); err != nil {
		return false
	}
	return strings.TrimSpace(result) == "true"
}

// browserScroll
type browserScroll struct{}

func (browserScroll) Name() string { return "browser_scroll" }

func (browserScroll) Description() string {
	return "Scroll the page in an open browser session. Direction is up/down/left/right; amount is in pixels (default 600). Use to reach content below the fold before extracting or screenshotting."
}

func (browserScroll) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Browser session id from browser_open"},
  "direction":{"type":"string","enum":["up","down","left","right"],"description":"Scroll direction"},
  "amount":{"type":"integer","minimum":1,"description":"Pixels to scroll (default 600)"}
},
"required":["session_id","direction"]
}`)
}

func (browserScroll) ReadOnly() bool { return false }

func (browserScroll) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Direction string `json:"direction"`
		Amount    int    `json:"amount"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" {
		return "", errors.New("session_id is required")
	}
	dir := strings.ToLower(strings.TrimSpace(p.Direction))
	amt := p.Amount
	if amt == 0 {
		amt = 600
	}
	var expr string
	switch dir {
	case "down":
		expr = fmt.Sprintf("window.scrollBy(0, %d)", amt)
	case "up":
		expr = fmt.Sprintf("window.scrollBy(0, -%d)", amt)
	case "right":
		expr = fmt.Sprintf("window.scrollBy(%d, 0)", amt)
	case "left":
		expr = fmt.Sprintf("window.scrollBy(-%d, 0)", amt)
	default:
		return "", fmt.Errorf("direction must be up/down/left/right, got %q", p.Direction)
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	if err := runBrowserAction(ctx, s, chromedp.Evaluate(expr, nil)); err != nil {
		return "", wrapError(s, "scroll", dir, fmt.Errorf("scroll: %w", err))
	}
	return wrapResult(s, "scroll", dir, fmt.Sprintf("scrolled %s %dpx", dir, amt)), nil
}

// browserTabs lists the browser's open tabs — the multi-tab companion to the
// click auto-follow: when a click opens a new tab the session follows it, and
// this tool shows where you are and what else is open.
type browserTabs struct{}

func (browserTabs) Name() string { return "browser_tabs" }

func (browserTabs) Description() string {
	return "List the open tabs of the session's browser with index, title and URL, marking the tab the session currently drives. Clicks that open a new tab (target=_blank) are auto-followed — use this to orient after one, and browser_switch_tab to move between tabs."
}

func (browserTabs) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Browser session id from browser_open"}
},
"required":["session_id"]
}`)
}

func (browserTabs) ReadOnly() bool { return true }

func (browserTabs) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	tabs, err := pageTargetInfos(s)
	if err != nil {
		return "", fmt.Errorf("list tabs: %w", err)
	}
	cur := sessionTargetID(s)
	var b strings.Builder
	for i, t := range tabs {
		mark := "      "
		if t.TargetID == cur {
			mark = "[当前] "
		}
		fmt.Fprintf(&b, "%s%d. %s — %s\n", mark, i+1, truncate(firstNonEmptyStr(t.Title, "(untitled)"), 50), truncate(t.URL, 90))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// browserSwitchTab moves the session onto another open tab (1-based index
// from browser_tabs). The current tab stays open — switch back anytime.
type browserSwitchTab struct{}

func (browserSwitchTab) Name() string { return "browser_switch_tab" }

func (browserSwitchTab) Description() string {
	return "Switch the session to another open tab by its 1-based index from browser_tabs (or an explicit target id). The current tab stays open — you can switch back. Use after a click auto-followed a new tab, or to move between pages of a multi-tab workflow."
}

func (browserSwitchTab) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Browser session id from browser_open"},
  "index":{"type":"integer","description":"1-based tab index from browser_tabs"},
  "target":{"type":"string","description":"Explicit target id (alternative to index)"}
},
"required":["session_id"]
}`)
}

func (browserSwitchTab) ReadOnly() bool { return false }

// browserCookies — inspect and manage the session browser's cookies (CDP
// Network domain). Powers "记住登录态 / 清 Cookies" style requests without
// dropping to JS, and mirrors the desktop cookies management card.
type browserCookies struct{}

func (browserCookies) Name() string { return "browser_cookies" }

func (browserCookies) ReadOnly() bool { return false }

func (browserCookies) Description() string {
	return "Manage the session browser's cookies. op=\"get\" lists the cookies the current page can see (name/domain/value truncated); op=\"clear\" wipes ALL cookies (logouts every site — ask before doing it); op=\"set\" writes one cookie (name+value required; domain/path default to the current page's). Use when debugging login state, carrying an auth cookie over, or when the user asks to clear cookies."
}

func (browserCookies) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Session id from browser_open"},
  "op":{"type":"string","enum":["get","set","clear"],"description":"get = list cookies; set = write one cookie; clear = remove ALL cookies"},
  "name":{"type":"string","description":"Cookie name (set)"},
  "value":{"type":"string","description":"Cookie value (set)"},
  "domain":{"type":"string","description":"Cookie domain (optional; defaults to current page host)"},
  "path":{"type":"string","description":"Cookie path (optional; defaults to \"/\")"}
},
"required":["session_id","op"]
}`)
}

func (browserCookies) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Op        string `json:"op"`
		Name      string `json:"name"`
		Value     string `json:"value"`
		Domain    string `json:"domain"`
		Path      string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" || p.Op == "" {
		return "", errors.New("session_id and op are required")
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	switch p.Op {
	case "get":
		getCtx, getCancel := context.WithTimeout(s.ctx, 10*time.Second)
		defer getCancel()
		cookies, err := network.GetCookies().Do(getCtx)
		if err != nil {
			return "", wrapError(s, "cookies.get", "", err)
		}
		return wrapResult(s, "cookies.get", "", formatCookies(cookies)), nil
	case "clear":
		clearCtx, clearCancel := context.WithTimeout(s.ctx, 10*time.Second)
		defer clearCancel()
		if err := network.ClearBrowserCookies().Do(clearCtx); err != nil {
			return "", wrapError(s, "cookies.clear", "", err)
		}
		return wrapResult(s, "cookies.clear", "", "all cookies cleared (every site is logged out in this browser)"), nil
	case "set":
		if p.Name == "" {
			return "", errors.New("name is required for op=set")
		}
		if p.Domain == "" {
			var cur string
			_ = chromedp.Run(s.ctx, chromedp.Location(&cur))
			if u, err := url.Parse(cur); err == nil && u.Hostname() != "" {
				p.Domain = u.Hostname()
			}
		}
		if p.Path == "" {
			p.Path = "/"
		}
		setCtx, setCancel := context.WithTimeout(s.ctx, 10*time.Second)
		defer setCancel()
		params := network.SetCookie(p.Name, p.Value).WithDomain(p.Domain).WithPath(p.Path)
		if err := params.Do(setCtx); err != nil {
			return "", wrapError(s, "cookies.set", p.Name, err)
		}
		return wrapResult(s, "cookies.set", p.Name, fmt.Sprintf("cookie %s set for %s", p.Name, p.Domain)), nil
	default:
		return "", fmt.Errorf("unknown op %q (get|set|clear)", p.Op)
	}
}

func formatCookies(cookies []*network.Cookie) string {
	if len(cookies) == 0 {
		return "no cookies visible from the current page"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d cookies:\n", len(cookies))
	for _, c := range cookies {
		val := c.Value
		if len(val) > 40 {
			val = val[:37] + "..."
		}
		fmt.Fprintf(&b, "- %s=%s (domain %s, path %s)\n", c.Name, val, c.Domain, c.Path)
	}
	return b.String()
}

func (browserSwitchTab) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Index     int    `json:"index"`
		Target    string `json:"target"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" {
		return "", errors.New("session_id is required")
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	tabs, err := pageTargetInfos(s)
	if err != nil {
		return "", fmt.Errorf("list tabs: %w", err)
	}
	var pick *cdptarget.Info
	switch {
	case p.Target != "":
		for _, t := range tabs {
			if string(t.TargetID) == p.Target {
				pick = t
				break
			}
		}
	case p.Index >= 1 && p.Index <= len(tabs):
		pick = tabs[p.Index-1]
	default:
		return "", fmt.Errorf("index %d out of range — browser_tabs lists 1..%d", p.Index, len(tabs))
	}
	if pick == nil {
		return "", fmt.Errorf("tab %q not found — run browser_tabs for the current list", firstNonEmptyStr(p.Target, fmt.Sprintf("%d", p.Index)))
	}
	if pick.TargetID == sessionTargetID(s) {
		return "already on tab: " + truncate(firstNonEmptyStr(pick.Title, pick.URL), 80), nil
	}
	if err := switchSessionTab(s, pick.TargetID); err != nil {
		return "", err
	}
	return "switched to tab: " + truncate(firstNonEmptyStr(pick.Title, pick.URL), 80), nil
}

// pageTargetInfos lists the browser's page-type targets in stable order.
func pageTargetInfos(s *browserSession) ([]*cdptarget.Info, error) {
	infos, err := chromedp.Targets(s.ctx)
	if err != nil {
		return nil, err
	}
	out := []*cdptarget.Info{}
	for _, t := range infos {
		if t.Type == "page" {
			out = append(out, t)
		}
	}
	return out, nil
}

// browserHover — menu-open hover via a REAL mousemove to the element's
// center: synthetic JS mouseover does NOT trigger CSS :hover (pseudo-classes
// only apply to trusted pointer input), so coordinate dispatch is required
// for the hover-menus pattern (hover → submenu → click).
type browserHover struct{}

func (browserHover) Name() string { return "browser_hover" }

func (browserHover) Description() string {
	return "Hover the pointer over an element (a real mousemove to its center — triggers CSS :hover, unlike synthetic events). Use BEFORE clicking items in hover-opened menus/dropdowns: hover the menu entry first, then click the revealed item."
}

func (browserHover) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Browser session id from browser_open"},
  "target":{"type":"string","description":"Snapshot ref (e.g. e5) or CSS selector of the element to hover"}
},
"required":["session_id","target"]
}`)
}

func (browserHover) ReadOnly() bool { return false }

func (browserHover) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string          `json:"session_id"`
		Target    json.RawMessage `json:"target"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" {
		return "", errors.New("session_id is required")
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	x, y, err := hoverPointForTarget(ctx, s, p.Target)
	if err != nil {
		return "", wrapError(s, "hover", string(p.Target), err)
	}
	if err := runBrowserAction(ctx, s, chromedp.MouseEvent(input.MouseMoved, x, y)); err != nil {
		return "", wrapError(s, "hover", fmt.Sprintf("%.0f,%.0f", x, y), fmt.Errorf("hover: %w", err))
	}
	return fmt.Sprintf("hovered (%.0f, %.0f) — CSS :hover applied; menus depending on it should now be open", x, y), nil
}

// hoverPointForTarget resolves a ref/selector target to viewport coordinates.
func hoverPointForTarget(ctx context.Context, s *browserSession, raw json.RawMessage) (float64, float64, error) {
	sel, _, _, _, isRef, err := selectorFromArgs(raw)
	if err != nil {
		return 0, 0, err
	}
	if isRef {
		out, cerr := callOnRef(ctx, s, sel, `JSON.stringify((function(){var r=this.getBoundingClientRect();return {x:r.left+r.width/2,y:r.top+r.height/2};})())`)
		if cerr != nil {
			return 0, 0, fmt.Errorf("hover ref %q: %w", sel, cerr)
		}
		return parseXYJSON(out)
	}
	expr := fmt.Sprintf(`(function(){var el=document.querySelector(%q);if(!el)return "";var r=el.getBoundingClientRect();return JSON.stringify({x:r.left+r.width/2,y:r.top+r.height/2});})()`, sel)
	var out string
	if rerr := runBrowserAction(ctx, s, chromedp.Evaluate(expr, &out)); rerr != nil {
		return 0, 0, fmt.Errorf("hover %q: %w", sel, rerr)
	}
	return parseXYJSON(out)
}

func parseXYJSON(out string) (float64, float64, error) {
	var pt struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &pt); jerr != nil || (pt.X == 0 && pt.Y == 0) {
		return 0, 0, fmt.Errorf("element not found or has no box")
	}
	return pt.X, pt.Y, nil
}

// browserBack / browserForward — history navigation, the primitives the
// step vocabulary was missing: replaying a "look at A, peek at B, back to A"
// flow should ride the history stack (state preserved) rather than
// re-navigating by URL (full reload).
type browserBack struct{}

func (browserBack) Name() string { return "browser_back" }

func (browserBack) Description() string {
	return "Go back one entry in the browser session's history (the browser's back button). Prefer over re-navigating by URL when returning to the previous page — it's faster and preserves page state. No previous entry: the page stays unchanged (not an error)."
}

func (browserBack) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Browser session id from browser_open"}
},
"required":["session_id"]
}`)
}

func (browserBack) ReadOnly() bool { return false }

func (browserBack) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return execHistoryNav(ctx, args, chromedp.NavigateBack(), "back")
}

type browserForward struct{}

func (browserForward) Name() string { return "browser_forward" }

func (browserForward) Description() string {
	return "Go forward one entry in the browser session's history (the browser's forward button); the counterpart of browser_back after navigating back. No forward entry: the page stays unchanged (not an error)."
}

func (browserForward) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Browser session id from browser_open"}
},
"required":["session_id"]
}`)
}

func (browserForward) ReadOnly() bool { return false }

func (browserForward) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return execHistoryNav(ctx, args, chromedp.NavigateForward(), "forward")
}

// execHistoryNav is the shared back/forward body: run the history action,
// then report where the session landed.
func execHistoryNav(ctx context.Context, args json.RawMessage, act chromedp.Action, dir string) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" {
		return "", errors.New("session_id is required")
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	if err := runBrowserAction(ctx, s, act, chromedp.WaitReady("body")); err != nil {
		return "", wrapError(s, "history-"+dir, "", fmt.Errorf("%s: %w", dir, err))
	}
	var url string
	_ = runBrowserAction(ctx, s, chromedp.Location(&url))
	out := fmt.Sprintf("went %s → %s", dir, url)
	if summary := autoPageSummary(s); summary != "" {
		out += "\n" + summary
	}
	return out, nil
}

// browserExtract
type browserExtract struct{}

func (browserExtract) Name() string { return "browser_extract" }

func (browserExtract) Description() string {
	return "Extract text content from an OPEN browser session (the page currently loaded in the browser). " +
		"Use this when the agent is already operating the browser and needs the visible text of the page or a part of it. " +
		"Do NOT use this to read a URL you haven't navigated to — use web_fetch for arbitrary URLs. " +
		"With no selector, returns the visible text of the whole page; with a selector, returns that element's text. " +
		"format=\"table\" renders every <table> under the selector (or the page) as markdown tables — use it for log grids, " +
		"result tables and anything with rows/columns so structure survives extraction. Output is capped at 200k chars — narrow with a selector or scroll+extract in chunks for long pages."
}

func (browserExtract) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Browser session id from browser_open"},
  "selector":{"type":"string","description":"Optional CSS selector to extract from a specific element. Omit for the whole page body."},
  "format":{"type":"string","enum":["text","table"],"description":"text (default) = visible text; table = every <table> under the selector rendered as markdown (rows/columns preserved)"}
},
"required":["session_id"]
}`)
}

func (browserExtract) ReadOnly() bool { return true }

// extractTablesJS renders the tables under one root as markdown. The selector
// is injected as a JSON string (SEL placeholder) — quote-safe by construction.
const extractTablesJS = `(function(){
  var root;
  try { root = SEL ? (document.querySelector(SEL) || document) : document; } catch (e) { root = document; }
  var tables = root.querySelectorAll('table');
  if (!tables.length) { return '(no <table> found under ' + (SEL || 'page') + ')'; }
  var out = [];
  for (var t = 0; t < tables.length && t < 10; t++) {
    var rows = tables[t].rows;
    if (!rows || !rows.length) { continue; }
    var lines = [];
    var headerDone = false;
    for (var i = 0; i < rows.length && lines.length < 200; i++) {
      var cells = [];
      for (var c = 0; c < rows[i].cells.length; c++) {
        var v = String(rows[i].cells[c].innerText || '').replace(/\s+/g, ' ').replace(/\|/g, '\\|').trim();
        cells.push(v.slice(0, 200));
      }
      if (!cells.length) { continue; }
      lines.push('| ' + cells.join(' | ') + ' |');
      if (!headerDone) {
        lines.push('|' + cells.map(function () { return '---'; }).join('|') + '|');
        headerDone = true;
      }
    }
    if (lines.length) {
      out.push('### table ' + (t + 1) + ' (' + rows.length + ' rows)\n\n' + lines.join('\n'));
    }
  }
  return out.length ? out.join('\n\n') : '(tables found but empty)';
})()`

func (browserExtract) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Selector  string `json:"selector"`
		Format    string `json:"format"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" {
		return "", errors.New("session_id is required")
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	sel := strings.TrimSpace(p.Selector)
	var text string
	if strings.EqualFold(strings.TrimSpace(p.Format), "table") {
		// Table mode: structure-preserving markdown for log grids / result
		// tables — plain-text extraction flattens columns into noise.
		expr := strings.Replace(extractTablesJS, "SEL", fmt.Sprintf("%q", sel), 2)
		if err := runBrowserAction(ctx, s, chromedp.Evaluate(expr, &text)); err != nil {
			return "", fmt.Errorf("extract tables %q: %w", sel, err)
		}
	} else if sel != "" {
		if err := runBrowserAction(ctx, s, chromedp.Text(sel, &text, chromedp.NodeVisible)); err != nil {
			return "", fmt.Errorf("extract %q: %w", sel, err)
		}
	} else {
		if err := runBrowserAction(ctx, s, chromedp.OuterHTML("body", &text)); err != nil {
			return "", fmt.Errorf("extract body: %w", err)
		}
		// OuterHTML still has tags; reduce to text for the model. Keep it simple —
		// the full HTML reducer lives in web_fetch; here we want quick readable text.
		text = htmlToText(text)
	}
	if len(text) > browserExtractMaxChars {
		text = text[:browserExtractMaxChars] + fmt.Sprintf("\n\n[...truncated, %d more chars]", len(text)-browserExtractMaxChars)
	}
	// Wrap in <untrusted_content>: the DOM came from an arbitrary web page the
	// agent navigated to, so it may contain prompt-injection attempts ("ignore
	// previous instructions…"). The tag + system prompt tell the model to treat
	// this as data, not commands. See internal/tool/builtin/untrusted.go.
	return WrapUntrusted("browser", strings.TrimSpace(text)), nil
}

// browserScreenshot
type browserScreenshot struct{}

func (browserScreenshot) Name() string { return "browser_screenshot" }

func (browserScreenshot) Description() string {
	return "Capture a screenshot of an open browser session as a PNG and return its file path plus a base64 thumbnail. Pass to image_understand for visual analysis, or use the path as an attachment. full_page=true captures the whole scrollable page."
}

func (browserScreenshot) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Browser session id from browser_open"},
  "full_page":{"type":"boolean","description":"Capture the entire scrollable page, not just the viewport (default false)"}
},
"required":["session_id"]
}`)
}

func (browserScreenshot) ReadOnly() bool { return true }

func (browserScreenshot) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
		FullPage  bool   `json:"full_page"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" {
		return "", errors.New("session_id is required")
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	var buf []byte
	if p.FullPage {
		// FullScreenshot captures the entire scrollable page at a JPEG quality
		// (it returns JPEG, not PNG — quality 80 keeps it small for VLM input).
		if err := runBrowserAction(ctx, s, chromedp.FullScreenshot(&buf, 80)); err != nil {
			return "", fmt.Errorf("screenshot: %w", err)
		}
	} else {
		// CaptureScreenshot grabs the current viewport as PNG.
		if err := runBrowserAction(ctx, s, chromedp.CaptureScreenshot(&buf)); err != nil {
			return "", fmt.Errorf("screenshot: %w", err)
		}
	}
	// Persist to the attachments dir so it doubles as a file attachment. The
	// extension reflects the actual format: FullScreenshot is JPEG, the viewport
	// capture is PNG — image_understand handles both.
	dir := browserAttachmentsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create attachments dir: %w", err)
	}
	ext := ".png"
	if p.FullPage {
		ext = ".jpg"
	}
	name := fmt.Sprintf("browser-%s-%d%s", p.SessionID, time.Now().Unix(), ext)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return "", fmt.Errorf("write screenshot: %w", err)
	}
	thumb := base64.StdEncoding.EncodeToString(buf)
	if len(thumb) > 4096 {
		thumb = thumb[:4096] + "…"
	}
	return fmt.Sprintf("screenshot saved: %s\nbase64 (first 4k): %s", path, thumb), nil
}

// browserEvaluate
type browserEvaluate struct{}

func (browserEvaluate) Name() string { return "browser_evaluate" }

func (browserEvaluate) Description() string {
	return "Evaluate a JavaScript expression in an OPEN browser session and return the result as JSON. " +
		"Use for custom DOM queries on the current page, triggering handlers, or reading computed state the other tools can't reach. " +
		"Do NOT use this to fetch arbitrary URLs (use web_fetch) or to search the internet (use web_search) — evaluate runs only against the page already loaded in the browser. " +
		"Avoid for tasks a dedicated tool covers (click/type/extract). " +
		"The returned JSON is capped at ~100KB — for large results (e.g. querySelectorAll over a big DOM), slice or map to the fields you need inside the expression rather than returning everything."
}

func (browserEvaluate) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Browser session id from browser_open"},
  "expression":{"type":"string","description":"JavaScript expression to evaluate. Must return a JSON-serializable value."}
},
"required":["session_id","expression"]
}`)
}

func (browserEvaluate) ReadOnly() bool { return false } // JS can mutate the page

func (browserEvaluate) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID  string `json:"session_id"`
		Expression string `json:"expression"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" || strings.TrimSpace(p.Expression) == "" {
		return "", errors.New("session_id and expression are required")
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	var result any
	// AwaitPromise lets async expressions resolve; ReturnByValue marshals the
	// result into Go rather than returning a remote object handle.
	if err := runBrowserAction(ctx, s, chromedp.Evaluate(p.Expression, &result, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true).WithReturnByValue(true)
	})); err != nil {
		return "", fmt.Errorf("evaluate: %w", err)
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", result), nil
	}
	// Cap the serialized result. A querySelectorAll over a large DOM, or any
	// script returning a big structure, can produce payloads that blow the
	// model's context or get rejected by the upstream API with HTTP 400 before
	// anything else can intervene. Tell the model how much was elided so it
	// knows to narrow the query (slice, map to ids, etc.).
	if len(out) > browserEvaluateMaxChars {
		kept := browserEvaluateMaxChars
		// Avoid cutting in the middle of a multibyte UTF-8 sequence: back up to
		// the last valid boundary so the truncated payload stays valid UTF-8.
		for kept > 0 && (out[kept]&0xC0) == 0x80 {
			kept--
		}
		return string(out[:kept]) + fmt.Sprintf("\n\n[...truncated, %d more bytes — narrow the expression or slice the result]", len(out)-kept), nil
	}
	return string(out), nil
}

// browserSelectOption picks an option in a <select> dropdown. Office forms are
// full of these (department, date, type, status), and setting .value directly on
// a <select> + dispatching change is the reliable cross-framework way (React's
// onChange listens for the event). The select element is addressed by ref
// (preferred, from browser_snapshot) or CSS selector.
type browserSelectOption struct{}

func (browserSelectOption) Name() string { return "browser_select_option" }

func (browserSelectOption) Description() string {
	return "Select an option in a <select> dropdown. Pass the select element's ref (from browser_snapshot) and either the option's value attribute or its visible label. Handles native selects by setting .value and dispatching the change event so React/Vue form handlers register the selection. Use for office form dropdowns (department, date, type, status, etc.)."
}

func (browserSelectOption) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Browser session id from browser_open"},
  "ref":{"type":"string","description":"Snapshot ref of the <select> element (e.g. \"e9\"). Preferred over selector."},
  "selector":{"type":"string","description":"CSS selector of the <select> element. Used when no ref is given."},
  "value":{"type":"string","description":"The option's value attribute. Preferred over label (matches exactly)."},
  "label":{"type":"string","description":"The option's visible text. Used when value is unknown; matched by visible label."}
},
"required":["session_id"]
}`)
}

func (browserSelectOption) ReadOnly() bool { return false }

func (browserSelectOption) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Ref       string `json:"ref"`
		Selector  string `json:"selector"`
		Value     string `json:"value"`
		Label     string `json:"label"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" {
		return "", errors.New("session_id is required")
	}
	if strings.TrimSpace(p.Value) == "" && strings.TrimSpace(p.Label) == "" {
		return "", errors.New("either value or label is required")
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	ref := strings.TrimSpace(p.Ref)
	sel := strings.TrimSpace(p.Selector)
	if ref == "" && sel == "" {
		return "", errors.New("either ref or selector is required to identify the <select> element")
	}

	// The JS: set .value (by value attr, or fall back to matching by visible
	// label), then dispatch change so framework handlers fire. Returns the
	// selected option's label + value for confirmation.
	js := `function(value, label) {
  var select = this;
  if (select.tagName !== 'SELECT') {
    return 'error: element is ' + select.tagName + ', not SELECT (wrong ref/selector?)';
  }
  var matched = null;
  if (value) {
    for (var i = 0; i < select.options.length; i++) {
      if (select.options[i].value === value) { matched = select.options[i]; break; }
    }
  }
  if (!matched && label) {
    for (var j = 0; j < select.options.length; j++) {
      if (select.options[j].textContent.trim() === label) { matched = select.options[j]; break; }
    }
  }
  if (!matched) {
    var avail = [];
    for (var k = 0; k < select.options.length; k++) {
      avail.push(select.options[k].value + '=' + select.options[k].textContent.trim());
    }
    return 'error: no matching option. Available: ' + avail.join(', ');
  }
  select.value = matched.value;
  select.dispatchEvent(new Event('input', {bubbles: true}));
  select.dispatchEvent(new Event('change', {bubbles: true}));
  return 'selected: ' + matched.textContent.trim() + ' (value=' + matched.value + ')';
}`

	if ref != "" {
		result, err := callOnRef(ctx, s, ref, js, p.Value, p.Label)
		if err != nil {
			return "", wrapError(s, "select", ref, fmt.Errorf("select option on ref %q: %w", ref, err))
		}
		return wrapResult(s, "select", ref, unwrapJSONString(result)), nil
	}
	// Selector path: run the same logic, but locate the element via querySelector
	// inside the JS. We pass sel/value/label as a JSON array the IIFE unpacks.
	var result any
	actx, cancel := actionCtx(ctx, s)
	defer cancel()
	body := js
	body = strings.TrimSpace(body)
	body = strings.TrimPrefix(body, "function(value, label) {")
	body = strings.TrimSuffix(body, "}")
	body = strings.ReplaceAll(body, "this", "el")
	expr := fmt.Sprintf(`(function(){var el=document.querySelector(%q);if(!el){return 'error: selector matched nothing'};%s})()`, sel, body)
	if err := chromedp.Run(actx, chromedp.Evaluate(expr, &result)); err != nil {
		return "", wrapError(s, "select", sel, fmt.Errorf("select option on %q: %w", sel, err))
	}
	out, _ := json.Marshal(result)
	return wrapResult(s, "select", sel, unwrapJSONString(string(out))), nil
}

// unwrapJSONString turns a JSON-encoded string result (like "\"selected: ...\"")
// into the plain string for a clean tool message. Non-string JSON is returned as-is.
func unwrapJSONString(s string) string {
	var str string
	if json.Unmarshal([]byte(s), &str) == nil {
		return str
	}
	return s
}

// browserUploadFile uploads one or more local files into an <input type="file">
// element on the page. This is the ONLY way to fill file inputs — browser_click
// refuses to click them (clicking opens a native OS file-chooser dialog that
// blocks the browser process and can't be dismissed via CDP).
//
// The path must be readable by the BROWSER process, not just hiq. For a
// locally-launched browser that's the same machine, so any absolute path the
// user can read works. For an attached remote browser, the path must be valid
// on the remote host.
//
// Uses CDP DOM.setFileInputFiles with the ref's backendNodeId, the same
// mechanism browser-use and Playwright use. Dispatches input + change events
// afterward so React/Vue form handlers register the upload.
type browserUploadFile struct{}

func (browserUploadFile) Name() string { return "browser_upload_file" }

func (browserUploadFile) Description() string {
	return "Upload one or more local files into an <input type=\"file\"> element. Pass the file input's ref (from browser_snapshot) and the absolute path(s) to the file(s). This is the ONLY way to fill file inputs — do NOT use browser_click on them (it will refuse). The path must be readable by the browser process. Multiple files are supported when the input has the 'multiple' attribute. Dispatches input + change events so React/Vue form handlers register the upload."
}

func (browserUploadFile) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Browser session id from browser_open"},
  "ref":{"type":"string","description":"Snapshot ref of the <input type=\"file\"> element (e.g. \"e7\"), from browser_snapshot."},
  "files":{"type":"array","items":{"type":"string"},"description":"Absolute file path(s) to upload. The browser process must be able to read them."},
  "selector":{"type":"string","description":"CSS selector of the file input. Used when no ref is given; ref is preferred."}
},
"required":["session_id","files"]
}`)
}

func (browserUploadFile) ReadOnly() bool { return false }

func (browserUploadFile) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string   `json:"session_id"`
		Ref       string   `json:"ref"`
		Selector  string   `json:"selector"`
		Files     []string `json:"files"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" {
		return "", errors.New("session_id is required")
	}
	if len(p.Files) == 0 {
		return "", errors.New("at least one file path is required")
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	ref := strings.TrimSpace(p.Ref)
	sel := strings.TrimSpace(p.Selector)
	if ref == "" && sel == "" {
		return "", errors.New("either ref or selector is required to identify the file input element")
	}

	// Validate the files exist locally first — gives a clear error before
	// hitting CDP, which would just fail with an opaque "set files" error.
	// NOTE: this checks the hiq process's view; for an attached remote
	// browser, the path also needs to exist on the remote host, which we can't
	// verify here. The check is best-effort: if it passes locally but the
	// browser is remote and the path is wrong, setFileInputFiles will error.
	for _, f := range p.Files {
		f = strings.TrimSpace(f)
		if f == "" {
			return "", errors.New("file path is empty")
		}
		if info, err := os.Stat(f); err != nil {
			return "", fmt.Errorf("file not accessible: %s: %w", f, err)
		} else if info.IsDir() {
			return "", fmt.Errorf("path is a directory, not a file: %s", f)
		}
	}

	// Resolve the file input's backendNodeId via ref (preferred) or selector.
	// We reuse resolveRefToObjectID's backendNodeId path but need the raw ID
	// for setFileInputFiles, so reach into the ref map directly for refs.
	scrollRefIntoView(ctx, s, ref) // best-effort; harmless if ref is empty
	var backendID cdp.BackendNodeID
	if ref != "" {
		refsPtr := s.refs.Load()
		if refsPtr == nil {
			return "", fmt.Errorf("no snapshot taken for session %q — call browser_snapshot first to get refs", s.id)
		}
		info, ok := (*refsPtr)[ref]
		if !ok {
			return "", fmt.Errorf("ref %q not found in the last snapshot (refs expire when the page changes; re-run browser_snapshot)", ref)
		}
		if info.backendID == 0 {
			return "", fmt.Errorf("ref %q has no DOM node (virtual node); target a concrete <input type=\"file\"> element", ref)
		}
		backendID = info.backendID
	}

	actx, cancel := actionCtx(ctx, s)
	defer cancel()
	// setFileInputFiles expects the files to be reachable from the browser's
	// perspective. Local browser = same filesystem; remote = remote path.
	if backendID != 0 {
		if err := chromedp.Run(actx, chromedp.ActionFunc(func(ctx context.Context) error {
			return dom.SetFileInputFiles(p.Files).WithBackendNodeID(backendID).Do(ctx)
		})); err != nil {
			return "", wrapError(s, "upload", ref, fmt.Errorf("set files on ref %q: %w", ref, err))
		}
	} else {
		// Selector path: resolve selector → remote objectId → backendNodeId,
		// then setFileInputFiles. Done in one ActionFunc so the DOM/runtime
		// sessions stay consistent. This mirrors how the ref path resolves.
		var selBackendID cdp.BackendNodeID
		if err := chromedp.Run(actx, chromedp.ActionFunc(func(ctx context.Context) error {
			// Evaluate selector to a remote object, then describeNode to get
			// its backendNodeId. describeNode is more reliable than requestNode
			// because it returns the backendNodeId directly.
			expr := fmt.Sprintf("(function(){var el=document.querySelector(%q);return el;})()", sel)
			res, ex, err := runtime.Evaluate(expr).Do(ctx)
			if err != nil {
				return err
			}
			if ex != nil {
				return fmt.Errorf("selector %q: %s", sel, ex.Text)
			}
			if res == nil || res.ObjectID == "" {
				return fmt.Errorf("no element matches selector %q", sel)
			}
			objID := res.ObjectID
			defer func() { _ = runtime.ReleaseObject(objID).Do(ctx) }()
			node, err := dom.DescribeNode().WithObjectID(objID).Do(ctx)
			if err != nil {
				return err
			}
			if node == nil || node.BackendNodeID == 0 {
				return fmt.Errorf("selector %q resolved to no backend node", sel)
			}
			selBackendID = node.BackendNodeID
			return nil
		})); err != nil {
			return "", wrapError(s, "upload", sel, fmt.Errorf("resolve file input selector %q: %w", sel, err))
		}
		if err := chromedp.Run(actx, chromedp.ActionFunc(func(ctx context.Context) error {
			return dom.SetFileInputFiles(p.Files).WithBackendNodeID(selBackendID).Do(ctx)
		})); err != nil {
			return "", wrapError(s, "upload", sel, fmt.Errorf("set files on selector %q: %w", sel, err))
		}
	}

	// Dispatch input + change events so React/Vue onChange handlers fire.
	// setFileInputFiles updates the internal file list but doesn't trigger
	// events by itself — frameworks listening for 'change' won't see the
	// upload otherwise.
	eventJS := "function() { this.dispatchEvent(new Event('input', {bubbles: true})); this.dispatchEvent(new Event('change', {bubbles: true})); return this.files ? this.files.length + ' file(s) set' : 'no files'; }"
	var fileCount string
	if ref != "" {
		fileCount, _ = callOnRef(ctx, s, ref, eventJS)
	} else if sel != "" {
		_ = runBrowserAction(ctx, s, chromedp.Evaluate(fmt.Sprintf(`(function(){var el=document.querySelector(%q);if(!el)return'no element';el.dispatchEvent(new Event('input',{bubbles:true}));el.dispatchEvent(new Event('change',{bubbles:true}));return el.files?el.files.length+' file(s) set':'no files';})()`, sel), &fileCount))
	}
	if fileCount == "" {
		fileCount = "events dispatched"
	}

	label := ref
	if label == "" {
		label = sel
	}
	return wrapResult(s, "upload", label, fmt.Sprintf("uploaded %d file(s) into %q (%s)", len(p.Files), label, fileCount)), nil
}

// browserSetPath lets the agent persist a user-supplied browser path when
// browser_open failed with ErrNoBrowser. The flow: browser_open fails → the
// agent asks the user for their Chrome/Edge exe path → calls browser_set_path
// to validate + persist it to config → retries browser_open. This is the
// "guide the user to input a Chromium browser path" requirement: one-shot,
// remembered across restarts.
type browserSetPath struct{}

func (browserSetPath) Name() string { return "browser_set_path" }

func (browserSetPath) Description() string {
	return "Persist the path to a Chromium-based browser (Chrome/Edge/Brave/Chromium) so browser_* tools can find it. Use after browser_open reports no browser found: ask the user for their browser's exe path (e.g. \"C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe\"), then call this to validate and save it. The path is written to the user config ([cowork] browser_path) and takes effect on the next browser_open — no restart needed. Pass an empty path to clear the override and revert to auto-detection."
}

func (browserSetPath) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Absolute path to a Chromium-based browser executable (.exe on Windows). Pass \"\" to clear a previously-set override."}
},
"required":["path"]
}`)
}

func (browserSetPath) ReadOnly() bool { return false }

func (browserSetPath) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	path := strings.TrimSpace(p.Path)

	// Empty path = clear override.
	if path == "" {
		SetConfiguredBrowserPath("")
		resetBrowserDetection()
		if err := persistBrowserPath(""); err != nil {
			return "", fmt.Errorf("clear browser_path: %w", err)
		}
		return "browser_path cleared — auto-detection restored (Chrome/Edge/Brave)", nil
	}

	// Validate the path exists before persisting — a typo would otherwise make
	// every future browser_open fail with a confusing "path does not exist".
	if verified, ok := verifyBrowserExe(path); !ok {
		return "", fmt.Errorf("path %q does not exist or is not executable; ask the user for the correct browser path", path)
	} else {
		path = verified
	}

	SetConfiguredBrowserPath(path)
	resetBrowserDetection()
	if err := persistBrowserPath(path); err != nil {
		return "", fmt.Errorf("save browser_path: %w", err)
	}
	return fmt.Sprintf("browser_path saved: %s (%s) — retry browser_open", path, browserDisplayName(path)), nil
}

// persistBrowserPath writes the [cowork] browser_path value into the user's
// config TOML. It reads the existing file (if any), updates just that field,
// and writes back atomically — preserving all other settings. This is a minimal
// targeted edit rather than a full re-render, so unrelated config is untouched.
func persistBrowserPath(path string) error {
	cfgPath := browserConfigFilePath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return err
	}
	existing, _ := os.ReadFile(cfgPath)
	updated := upsertCoworkBrowserPath(string(existing), path)
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(updated), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, cfgPath)
}

// browserConfigFilePath returns the user config TOML path, mirroring
// config.UserConfigPath() without the import cycle (this package is in
// internal/tool/builtin; config is a sibling). We resolve the same XDG/home
// location directly.
func browserConfigFilePath() string {
	// Respect XDG_CONFIG_HOME if set, else ~/.config/hiq/config.toml — same
	// logic as config.userConfigPath.
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "hiq", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "hiq", "config.toml")
}

// upsertCoworkBrowserPath inserts or replaces the browser_path line under the
// [cowork] section of a TOML string. If the section or file is missing, it
// appends a fresh [cowork] section. Existing [cowork] keys other than
// browser_path are preserved.
func upsertCoworkBrowserPath(toml, path string) string {
	lines := strings.Split(toml, "\n")
	// Escape backslashes for TOML string value (Windows paths).
	escaped := strings.ReplaceAll(path, "\\", "\\\\")

	sectionIdx := -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "[cowork]" {
			sectionIdx = i
			break
		}
	}

	if sectionIdx == -1 {
		// No [cowork] section — append one. Avoid a leading blank line when the
		// file is empty/new.
		var b strings.Builder
		if strings.TrimSpace(toml) != "" {
			b.WriteString(toml)
			if !strings.HasSuffix(toml, "\n") {
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		b.WriteString("[cowork]\n")
		if path != "" {
			b.WriteString("browser_path = \"" + escaped + "\"\n")
		}
		return b.String()
	}

	// Section exists: find the existing browser_path line within it (before the
	// next section header) and replace, or insert after the section header.
	nextSection := len(lines)
	for i := sectionIdx + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") && strings.HasSuffix(strings.TrimSpace(lines[i]), "]") {
			nextSection = i
			break
		}
	}
	for i := sectionIdx + 1; i < nextSection; i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "browser_path") {
			if path == "" {
				// Remove the line entirely.
				lines = append(lines[:i], lines[i+1:]...)
			} else {
				lines[i] = "browser_path = \"" + escaped + "\""
			}
			return strings.Join(lines, "\n")
		}
	}
	// No existing browser_path in section — insert one after the header.
	insert := "browser_path = \"" + escaped + "\""
	if path == "" {
		return strings.Join(lines, "\n") // nothing to add when clearing an absent key
	}
	result := append([]string{}, lines[:sectionIdx+1]...)
	result = append(result, insert)
	result = append(result, lines[sectionIdx+1:]...)
	return strings.Join(result, "\n")
}

// browserWait waits for a page condition before proceeding. Essential for SPAs
// and dynamic pages where content loads asynchronously after navigation or clicks.
type browserWait struct{}

func (browserWait) Name() string { return "browser_wait" }

func (browserWait) Description() string {
	return "Wait for a condition before proceeding. Use after navigate or click to ensure the page is ready. Conditions: 'load' (page fully loaded), 'networkidle' (no network requests for 500ms), 'visible:<selector>' (element appears), 'hidden:<selector>' (element disappears), 'title:<text>' (page title contains text), 'url:<text>' (page URL contains text, e.g. post-login redirect), 'stable:<selector>' (element's content stops changing for 2s — detects the end of streaming/AI responses). Essential for SPAs and dynamic pages."
}

func (browserWait) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Session id from browser_open"},
  "condition":{"type":"string","description":"Wait condition: 'load', 'networkidle', 'visible:<selector>', 'hidden:<selector>', 'title:<text>', 'url:<text>', 'stable:<selector>'"},
  "timeout":{"type":"integer","description":"Timeout in seconds (default 15)"}
},
"required":["session_id","condition"]
}`)
}

func (browserWait) ReadOnly() bool { return false } // wait blocks execution, not read-only

func (browserWait) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Condition string `json:"condition"`
		Timeout   int    `json:"timeout"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" {
		return "", errors.New("session_id is required")
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}

	timeout := 15 * time.Second
	if p.Timeout > 0 {
		timeout = time.Duration(p.Timeout) * time.Second
	}

	cond := strings.TrimSpace(p.Condition)
	switch {
	case cond == "load":
		// Wait for document.readyState === "complete".
		err = runBrowserAction(ctx, s, chromedp.Evaluate(
			`new Promise(resolve => {
				if (document.readyState === 'complete') return resolve(true);
				const check = () => { if (document.readyState === 'complete') resolve(true); };
				document.addEventListener('readystatechange', check);
				setTimeout(() => { document.removeEventListener('readystatechange', check); resolve(false); }, `+fmt.Sprintf("%d", int(timeout.Milliseconds()))+`);
			})`, nil))

	case cond == "networkidle":
		// Wait until no new resource requests for 500ms.
		err = runBrowserAction(ctx, s, chromedp.Evaluate(
			`new Promise(resolve => {
				let lastCount = performance.getEntriesByType('resource').length;
				let lastChange = Date.now();
				const poll = setInterval(() => {
					const cur = performance.getEntriesByType('resource').length;
					if (cur !== lastCount) { lastCount = cur; lastChange = Date.now(); }
					if (Date.now() - lastChange > 500) { clearInterval(poll); resolve(true); }
					if (Date.now() - lastChange > `+fmt.Sprintf("%d", int(timeout.Milliseconds()))+`) { clearInterval(poll); resolve(false); }
				}, 100);
			})`, nil))

	case strings.HasPrefix(cond, "visible:"):
		sel := strings.TrimSpace(strings.TrimPrefix(cond, "visible:"))
		actx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		err = chromedp.Run(actx, chromedp.WaitVisible(sel))

	case strings.HasPrefix(cond, "hidden:"):
		sel := strings.TrimSpace(strings.TrimPrefix(cond, "hidden:"))
		actx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		err = chromedp.Run(actx, chromedp.WaitNotVisible(sel))

	case strings.HasPrefix(cond, "title:"):
		titleText := strings.TrimSpace(strings.TrimPrefix(cond, "title:"))
		err = runBrowserAction(ctx, s, chromedp.Evaluate(
			`new Promise(resolve => {
				const check = () => { if (document.title.includes(`+fmt.Sprintf("%q", titleText)+`)) resolve(true); };
				if (document.title.includes(`+fmt.Sprintf("%q", titleText)+`)) return resolve(true);
				const poll = setInterval(() => { check(); }, 200);
				setTimeout(() => { clearInterval(poll); resolve(false); }, `+fmt.Sprintf("%d", int(timeout.Milliseconds()))+`);
			})`, nil))

	case strings.HasPrefix(cond, "url:"):
		// Poll location.href for a substring — the post-login/redirect signal
		// human-breakpoint skills wait on. A page object isn't required, so
		// poll via the executor's target without runBrowserAction's frame push.
		urlText := strings.TrimSpace(strings.TrimPrefix(cond, "url:"))
		actx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		err = chromedp.Run(actx, chromedp.Evaluate(
			`new Promise(resolve => {
				const needle = `+fmt.Sprintf("%q", urlText)+`;
				const check = () => location.href.includes(needle);
				if (check()) return resolve(true);
				const poll = setInterval(() => { if (check()) { clearInterval(poll); resolve(true); } }, 200);
				setTimeout(() => { clearInterval(poll); resolve(false); }, `+fmt.Sprintf("%d", int(timeout.Milliseconds()))+`);
			})`, nil))

	case strings.HasPrefix(cond, "stable:"):
		// Streaming-completion detector: watch the element's content signature
		// (text length + child count) and resolve once it stops changing for
		// 2s — the classic "AI/streaming response finished rendering" signal.
		stableSel := strings.TrimSpace(strings.TrimPrefix(cond, "stable:"))
		err = runBrowserAction(ctx, s, chromedp.Evaluate(
			`new Promise(resolve => {
				const sel = `+fmt.Sprintf("%q", stableSel)+`;
				const sig = () => {
					const el = document.querySelector(sel);
					if (!el) return 'none';
					return el.textContent.length + ':' + el.children.length;
				};
				let last = sig(), lastAt = Date.now();
				const poll = setInterval(() => {
					const cur = sig();
					if (cur !== last) { last = cur; lastAt = Date.now(); }
					if (Date.now() - lastAt >= 2000) { clearInterval(poll); resolve(true); }
				}, 300);
				setTimeout(() => { clearInterval(poll); resolve(false); }, `+fmt.Sprintf("%d", int(timeout.Milliseconds()))+`);
			})`, nil))

	default:
		return "", fmt.Errorf("unknown condition %q; use 'load', 'networkidle', 'visible:<sel>', 'hidden:<sel>', 'title:<text>', 'url:<text>', or 'stable:<sel>'", cond)
	}

	if err != nil {
		return "", fmt.Errorf("wait %q: %w", cond, err)
	}
	return fmt.Sprintf("waited for %q", cond), nil
}

// drainDialogMessages returns any JS dialog messages captured since the last
// call and clears the buffer. Used by autoPageSummary so the agent sees that a
// dialog appeared and what it said — otherwise an auto-dismissed "密码错误"
// alert is invisible to the model and it can't react. Returns "" when there
// are none, so callers can append unconditionally.
func (s *browserSession) drainDialogMessages() string {
	s.dialogMu.Lock()
	defer s.dialogMu.Unlock()
	if len(s.dialogMessages) == 0 {
		return ""
	}
	joined := strings.Join(s.dialogMessages, "; ")
	s.dialogMessages = s.dialogMessages[:0]
	return joined
}

// drainDownloadRecords returns a one-line summary of files that reached a
// terminal state (completed/canceled) since the last call and drops those from
// the buffer; in-progress downloads are kept so the next call can still report
// them once they finish. Surfaced via autoPageSummary so a "click download
// link" step reports "download completed: report.pdf" instead of an opaque
// "clicked". Returns "" when nothing finished.
func (s *browserSession) drainDownloadRecords() string {
	s.downloadMu.Lock()
	defer s.downloadMu.Unlock()
	if len(s.downloadRecords) == 0 {
		return ""
	}
	var parts []string
	kept := s.downloadRecords[:0]
	for _, d := range s.downloadRecords {
		if d.State == "inProgress" {
			kept = append(kept, d)
			continue
		}
		name := d.SuggestedName
		if name == "" {
			name = "(unknown filename)"
		}
		parts = append(parts, fmt.Sprintf("%s %s in %s", d.State, name, browserDownloadDir))
	}
	s.downloadRecords = kept
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}

// --- Phase 1: dialog auto-accept --------------------------------------------

// startDialogHandler launches a background goroutine that listens for
// Page.javascriptDialogOpening events and auto-accepts alert/confirm/beforeunload
// dialogs so they never block the agent. Modeled after browser-use's PopupsWatchdog.
func startDialogHandler(s *browserSession) {
	go func() {
		// Use chromedp.ListenTarget to catch dialog events on this tab.
		chromedp.ListenTarget(s.ctx, func(ev interface{}) {
			switch e := ev.(type) {
			case *cdprotopage.EventJavascriptDialogOpening:
				msg := fmt.Sprintf("[%s] %s", e.Type, e.Message)
				s.dialogMu.Lock()
				s.dialogMessages = append(s.dialogMessages, msg)
				s.dialogMu.Unlock()
				// Auto-accept alert, confirm, beforeunload; cancel prompt (can't provide input).
				shouldAccept := e.Type != cdprotopage.DialogTypePrompt
				// Best-effort dialog handling — if the session is closing, this will fail silently.
				actx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
				defer cancel()
				_ = chromedp.Run(actx, chromedp.ActionFunc(func(ctx context.Context) error {
					return cdprotopage.HandleJavaScriptDialog(shouldAccept).Do(ctx)
				}))
			}
		})
		// Block until session context is cancelled.
		<-s.ctx.Done()
	}()
}

// --- Downloads capture ------------------------------------------------------

// startDownloadsHandler pins a known download directory on the browser, then
// listens for Browser.downloadWillBegin / downloadProgress events. Captures
// completed/failed downloads into the session so a "click download link" step
// surfaces "download completed: foo.pdf" to the agent instead of an opaque
// "clicked". Best-effort: any CDP error is logged and swallowed — download
// capture must never block the session from booting.
//
// CDP download events are browser-level (not per-target), so we use
// chromedp.ListenBrowser. The behavior is set via Browser.setDownloadBehavior,
// which must be called after the browser websocket is up — we defer it onto a
// one-shot chromedp.Run.
func startDownloadsHandler(s *browserSession) {
	if err := initBrowserDownloadDir(); err != nil {
		// No dir → can't pin behavior. Skip capture; downloads still land in
		// Chrome's default dir, the agent just won't get a completion signal.
		fmt.Printf("[browser] downloads handler disabled: %v\n", err)
		return
	}
	go func() {
		// Pin the download dir + enable event flow. Wrapped in a Run so it
		// executes on the browser-level websocket; allowFailure guards against
		// the session closing mid-boot.
		setupCtx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		err := chromedp.Run(setupCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			return cdpbrowser.SetDownloadBehavior(cdpbrowser.SetDownloadBehaviorBehaviorAllow).
				WithDownloadPath(browserDownloadDir).
				WithEventsEnabled(true).
				Do(ctx)
		}))
		cancel()
		if err != nil {
			fmt.Printf("[browser] set download behavior failed: %v (downloads will not be tracked)\n", err)
			// Continue anyway — listen registration is independent and cheap.
		}
		chromedp.ListenBrowser(s.ctx, func(ev interface{}) {
			switch e := ev.(type) {
			case *cdpbrowser.EventDownloadWillBegin:
				// Register the download under its guid so the progress event
				// can pair back to name+url. SuggestedFilename is the only
				// source for the human-readable name.
				s.downloadMu.Lock()
				s.downloadRecords = append(s.downloadRecords, downloadRecord{
					GUID:          e.GUID,
					URL:           e.URL,
					SuggestedName: e.SuggestedFilename,
					State:         "inProgress",
				})
				s.downloadMu.Unlock()
			case *cdpbrowser.EventDownloadProgress:
				// Map CDP state → our terminal state. InProgress updates are
				// dropped (too noisy); only terminal states matter for the LLM.
				var state string
				switch e.State {
				case cdpbrowser.DownloadProgressStateCompleted:
					state = "completed"
				case cdpbrowser.DownloadProgressStateCanceled:
					state = "canceled"
				default:
					return // inProgress — wait for terminal state
				}
				s.downloadMu.Lock()
				// Pair by guid: mark the matching record terminal. guid comes
				// from willBegin, so a progress without a prior willBegin is a
				// race we just drop (no name to report anyway).
				for i := range s.downloadRecords {
					if s.downloadRecords[i].GUID == e.GUID {
						s.downloadRecords[i].State = state
						break
					}
				}
				s.downloadMu.Unlock()
			}
		})
		<-s.ctx.Done()
	}()
}

// --- Phase 6: WebSocket keepalive -------------------------------------------

// startSessionKeepalive sends periodic CDP pings to prevent intermediate proxies
// from closing idle WebSocket connections during long tasks.
func startSessionKeepalive(s *browserSession) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				if s.ctx.Err() != nil {
					return
				}
				actx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
				var ua string
				_ = chromedp.Run(actx, chromedp.Evaluate(`navigator.userAgent`, &ua))
				cancel()
			}
		}
	}()
}

// --- session keep-alive (会话保活) ---------------------------------------------------
//
// Long-interval tasks (log in now, next instruction hours later) die twice
// without help: the idle reaper closes the session after browserIdleTimeout,
// and the site's own session cookie/contract expires with no traffic. Arming
// keep-alive beats both — every tick refreshes lastUsed, then per mode:
//
//   - ping:     same-origin credentials fetch INSIDE the page (slides the
//     site's cookie/server session without disturbing the page)
//   - navigate: reload the keep URL (or current page) — the heavy option for
//     sites that only renew sessions on full page loads
//   - local:    reaper-side only, for pages where any traffic is unwanted
//
// Armed from the ops console's toggle (ConsoleSetKeepAlive) and from the
// agent side via the browser_keepalive tool — the chat-driven 值守循环
// honors the user's keep-alive choice without leaving the conversation.

// kaPingJS is evaluated in the page each ping tick. The fetch is fire-and-
// forget with its outcome parked on window.__fpKA — chromedp.Evaluate can't
// await promises portably, so the NEXT tick reads the previous result.
const kaPingJS = `(function(){
  try {
    var target = (window.__fpKAURL || location.href);
    window.__fpKA = 'pending';
    fetch(target, {credentials: 'include', cache: 'no-store'})
      .then(function(r){ window.__fpKA = 'http ' + r.status; })
      .catch(function(e){ window.__fpKA = 'err ' + (e && e.message ? e.message : 'fetch failed'); });
  } catch (e) { window.__fpKA = 'err ' + (e && e.message ? e.message : 'setup failed'); }
  return window.__fpKA;
})()`

// armBrowserKeepAlive arms/disarms one session's keep-alive loop. intervalSec
// clamps to [60,3600] (default 300); mode "" → "ping". Re-arming while armed
// swaps settings in place — the loop reads them fresh each tick.
func armBrowserKeepAlive(s *browserSession, enabled bool, intervalSec int, mode, url string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", "ping", "navigate", "local":
	default:
		return fmt.Errorf("unknown keep-alive mode %q (ping|navigate|local)", mode)
	}
	if mode == "" {
		mode = "ping"
	}
	s.keepMu.Lock()
	defer s.keepMu.Unlock()
	if !enabled {
		s.keepOn = false
		s.keepMode = ""
		s.keepURL = ""
		stop := s.keepStop
		s.keepStop = nil
		if stop != nil {
			close(stop)
		}
		return nil
	}
	if intervalSec <= 0 {
		intervalSec = 300
	}
	if intervalSec < 60 {
		intervalSec = 60
	}
	if intervalSec > 3600 {
		intervalSec = 3600
	}
	s.keepOn = true
	s.keepMode = mode
	s.keepURL = strings.TrimSpace(url)
	s.keepInterval = time.Duration(intervalSec) * time.Second
	s.keepErr = ""
	if s.keepStop == nil {
		s.keepStop = make(chan struct{})
		go sessionKeepaliveLoop(s, s.keepStop)
	}
	return nil
}

// sessionKeepaliveLoop refreshes until disarmed (stop closed) or the session
// dies (ctx cancelled). First tick fires ~immediately so the panel shows a
// fresh last-refresh at once.
func sessionKeepaliveLoop(s *browserSession, stop chan struct{}) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
		s.keepMu.Lock()
		stopped := s.keepStop == nil || s.keepStop != stop
		mode, url := s.keepMode, s.keepURL
		s.keepMu.Unlock()
		if stopped {
			return
		}
		kaErr := sessionKeepaliveTick(s, mode, url)
		s.keepMu.Lock()
		if kaErr != nil {
			s.keepErr = kaErr.Error()
		} else {
			s.keepErr = ""
			s.keepLast = time.Now().UnixMilli()
		}
		s.keepMu.Unlock()
		ticker.Reset(s.currentKeepInterval())
	}
}

// currentKeepInterval reads the armed interval (300s floor for safety).
func (s *browserSession) currentKeepInterval() time.Duration {
	s.keepMu.Lock()
	defer s.keepMu.Unlock()
	if s.keepInterval <= 0 {
		return 300 * time.Second
	}
	return s.keepInterval
}

// sessionKeepaliveTick performs one refresh cycle. The reaper-side lastUsed
// refresh happens even when the site-side half fails — a degraded keep-alive
// still preserves the hiq session.
func sessionKeepaliveTick(s *browserSession, mode, url string) error {
	if s.ctx.Err() != nil {
		return errors.New("session closed")
	}
	// Always beat the idle reaper for this tick.
	s.lastUsed.Store(time.Now().Unix())
	switch mode {
	case "local":
		return nil
	case "navigate":
		target := url
		if target == "" {
			ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
			_ = chromedp.Run(ctx, chromedp.Location(&target))
			cancel()
		}
		if target == "" {
			return errors.New("keep-alive target URL unresolved")
		}
		raw, _ := json.Marshal(map[string]any{"session_id": s.id, "url": target})
		ctx, cancel := context.WithTimeout(s.ctx, browserActionTimeout)
		defer cancel()
		if _, nerr := (browserNavigate{}).Execute(ctx, raw); nerr != nil {
			return fmt.Errorf("reload page: %w", nerr)
		}
		return nil
	default: // ping
		ctx, cancel := context.WithTimeout(s.ctx, browserActionTimeout)
		defer cancel()
		var prev string
		// Read the previous tick's fetch outcome for reporting, then fire the
		// next heartbeat (result lands next tick via window.__fpKA).
		_ = chromedp.Run(ctx,
			chromedp.Evaluate(`window.__fpKAURL = `+fmt.Sprintf("%q", url)+`; void 0;`, nil),
			chromedp.Evaluate(`(window.__fpKA === undefined) ? "first" : String(window.__fpKA)`, &prev),
			chromedp.Evaluate(kaPingJS, nil),
		)
		if strings.HasPrefix(prev, "err") {
			return fmt.Errorf("page heartbeat: %s", prev)
		}
		if prev == "http 401" || prev == "http 403" {
			// The heartbeat reached the site but was rejected — the site-side
			// session is likely gone; surface it instead of pretending alive.
			return fmt.Errorf("page heartbeat got %s — site session may have expired", prev)
		}
		return nil
	}
}

// browserKeepalive arms or disarms a session's keep-alive — the chat-driven
// flow's answer to "下一轮可能几个小时后才来，别让会话掉".
type browserKeepalive struct{}

func (browserKeepalive) Name() string { return "browser_keepalive" }

func (browserKeepalive) Description() string {
	return "Arm or disarm keep-alive for a browser session, for long-idle workflows (log in now, next instruction much later). While armed, the session survives idle reaping and the site's session is refreshed: mode 'ping' sends a same-origin credentials fetch inside the page (default, does not disturb the page), 'navigate' reloads the keep URL (or current page), 'local' only prevents local idle reaping. interval_sec clamps to 60..3600 (default 300)."
}

func (browserKeepalive) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "session_id":{"type":"string","description":"Session id from browser_open"},
  "enabled":{"type":"boolean","description":"true to arm, false to disarm"},
  "interval_sec":{"type":"integer","description":"Refresh interval in seconds (60..3600, default 300)"},
  "mode":{"type":"string","enum":["ping","navigate","local"],"description":"ping = in-page heartbeat fetch (default); navigate = periodic reload; local = only prevent idle reaping"},
  "url":{"type":"string","description":"navigate mode target (empty = current page)"}
},
"required":["session_id","enabled"]
}`)
}

func (browserKeepalive) ReadOnly() bool { return false } // navigate mode reloads the page

func (browserKeepalive) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SessionID   string `json:"session_id"`
		Enabled     bool   `json:"enabled"`
		IntervalSec int    `json:"interval_sec"`
		Mode        string `json:"mode"`
		URL         string `json:"url"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SessionID == "" {
		return "", errors.New("session_id is required")
	}
	s, err := ensureSession(p.SessionID)
	if err != nil {
		return "", err
	}
	if err := armBrowserKeepAlive(s, p.Enabled, p.IntervalSec, p.Mode, p.URL); err != nil {
		return "", err
	}
	s.keepMu.Lock()
	on, mode, last, kaErr := s.keepOn, s.keepMode, s.keepLast, s.keepErr
	s.keepMu.Unlock()
	if !on {
		return "keep-alive off", nil
	}
	status := fmt.Sprintf("keep-alive on (mode=%s, interval=%s)", mode, s.currentKeepInterval())
	if kaErr != "" {
		status += "; last refresh error: " + kaErr
	} else if last > 0 {
		status += fmt.Sprintf("; last refresh %d ms ago", time.Now().UnixMilli()-last)
	}
	return status, nil
}

// --- Phase 3: auto page summary ---------------------------------------------

// detectCaptchaChallenge runs a lightweight DOM probe looking for well-known
// captcha / bot-challenge signatures (reCAPTCHA, hCaptcha, Cloudflare Turnstile
// iframe sources, plus a few high-confidence text markers). Returns a short
// hint string when a challenge is detected, "" otherwise.
//
// Intentionally narrow: we only flag what we're confident about, so the agent
// doesn't cry wolf on pages that merely mention "verification". On a hit we
// surface 🔒 in the page summary so the agent can fall back to a VLM read of
// the screenshot or ask the user for help — we do NOT attempt to solve the
// challenge here.
//
// Best-effort: any CDP error is swallowed (the probe must never block the
// action that triggered it).
func detectCaptchaChallenge(s *browserSession) string {
	if s.ctx.Err() != nil {
		return ""
	}
	// One round-trip: look for captcha iframe srcs + a couple of high-signal
	// markers. Returning a compact string keeps the probe cheap to parse.
	expr := `(function(){
		var iframes = document.querySelectorAll('iframe[src]');
		for (var i = 0; i < iframes.length; i++) {
			var src = iframes[i].src.toLowerCase();
			if (src.indexOf('recaptcha') !== -1) return 'recaptcha';
			if (src.indexOf('hcaptcha.com') !== -1) return 'hcaptcha';
			if (src.indexOf('challenges.cloudflare.com') !== -1) return 'turnstile';
			if (src.indexOf('geetest') !== -1) return 'geetest';
		}
		// reCAPTCHA v3 / enterprise can render without a visible iframe —
		// detect via the injected script tag instead.
		var scripts = document.querySelectorAll('script[src]');
		for (var j = 0; j < scripts.length; j++) {
			var s = scripts[j].src.toLowerCase();
			if (s.indexOf('recaptcha/api') !== -1 || s.indexOf('recaptcha/enterprise') !== -1) return 'recaptcha';
			if (s.indexOf('hcaptcha.com/1/api') !== -1) return 'hcaptcha';
		}
		// High-confidence text markers (page-visible copy, not just any node).
		// Matching textContent of a small banner element avoids false positives
		// from incidental mentions in body copy.
		var t = (document.title || '').toLowerCase();
		if (t.indexOf('attention required') !== -1 || t.indexOf('just a moment') !== -1) return 'cloudflare-challenge';
		return '';
	})()`
	probeCtx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
	defer cancel()
	var result string
	if err := chromedp.Run(probeCtx, chromedp.Evaluate(expr, &result)); err != nil {
		return ""
	}
	switch result {
	case "recaptcha":
		return "🔒 reCAPTCHA challenge detected — fall back to browser_screenshot + image_understand, or ask the user to solve it; do not retry blindly"
	case "hcaptcha":
		return "🔒 hCaptcha challenge detected — fall back to browser_screenshot + image_understand, or ask the user to solve it; do not retry blindly"
	case "turnstile":
		return "🔒 Cloudflare Turnstile challenge detected — it usually auto-solves in a few seconds; wait (browser_wait) before retrying, or fall back to a screenshot read"
	case "geetest":
		return "🔒 GeeTest slider/click captcha detected — needs a human or a solver service; fall back to browser_screenshot + image_understand to assess, or ask the user"
	case "cloudflare-challenge":
		return "🔒 Cloudflare bot-challenge page detected — wait a few seconds (browser_wait) for it to clear, then re-check before retrying"
	default:
		return ""
	}
}

// autoPageSummary returns a lightweight page state string without a full
// snapshot. Attached to action tool results so the agent always knows the
// current page without an explicit browser_snapshot call. Modeled after
// Playwright MCP's --snapshot-mode=full auto-injection.
func autoPageSummary(s *browserSession) string {
	if s.ctx.Err() != nil {
		return "[page: session closed]"
	}
	var url, title string
	var elemCount int
	actx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	_ = chromedp.Run(actx,
		chromedp.Location(&url),
		chromedp.Title(&title),
		chromedp.Evaluate(`document.querySelectorAll('*').length`, &elemCount),
	)
	if title == "" {
		title = "(untitled)"
	}
	summary := fmt.Sprintf("[page: %s | %d elements | %s]", truncate(title, 40), elemCount, truncate(url, 60))
	// Surface any JS dialogs auto-dismissed since the last action, so the
	// agent learns the page pushed a message (e.g. "登录失败") even though
	// the dialog itself never blocked the CDP action.
	if dialogs := s.drainDialogMessages(); dialogs != "" {
		summary += " ⚠️ auto-dismissed dialogs: " + truncate(dialogs, 200)
	}
	// Surface downloads that finished since the last action. Without this a
	// click that triggers a download returns "clicked" with no hint a file
	// landed — the agent can't tell the click had a side effect.
	if downloads := s.drainDownloadRecords(); downloads != "" {
		summary += " 📥 downloads: " + truncate(downloads, 200)
	}
	// Captcha / bot-challenge probe. A hit means the agent is likely stuck and
	// should fall back to a screenshot read (VLM) or ask the user — never retry
	// blindly. Kept narrow to avoid false positives on pages that merely talk
	// about verification.
	if captcha := detectCaptchaChallenge(s); captcha != "" {
		summary += " " + captcha
	}
	return summary
}

// wrapResult appends auto-page-summary and loop detection nudges to an action result.
func wrapResult(s *browserSession, actionName, actionParams, baseResult string) string {
	s.stepTracker.recordAction(actionName, actionParams)
	s.stepTracker.resetFailures()
	summary := autoPageSummary(s)
	nudge := s.stepTracker.getNudgeMessage()
	if nudge != "" {
		// Include a screenshot so the LLM can see what's actually on screen.
		if thumb := captureThumbnailBase64(s); thumb != "" {
			return baseResult + "\n" + summary + "\n" + nudge + "\n[screenshot:image/jpeg;base64," + thumb + "]"
		}
		return baseResult + "\n" + summary + "\n" + nudge
	}
	return baseResult + "\n" + summary
}

// wrapError appends loop detection nudges to an error result and tracks failures.
func wrapError(s *browserSession, actionName, actionParams string, err error) error {
	s.stepTracker.recordAction(actionName, actionParams)
	s.stepTracker.recordFailure()
	nudge := s.stepTracker.getNudgeMessage()
	if nudge != "" {
		return fmt.Errorf("%w\n%s", err, nudge)
	}
	return err
}

// --- post-click stability helpers -------------------------------------------

// pageTargetIDs returns the set of page-type target IDs currently open in the
// session's browser. Used by browser_click to detect a target=_blank click:
// snapshot IDs before the click, compare after, any new page target is a tab
// the click opened. Returns nil on any CDP error (treated as "no baseline").
//
// The timeout is parented under s.ctx and cancelled locally — chromedp v0.15.x
// only kills Chrome when the allocator context itself is cancelled, and a
// child timeout cancel does not propagate up to it (same pattern as
// waitForPageLoad / autoPageSummary elsewhere in this file).
func pageTargetIDs(s *browserSession) map[cdptarget.ID]bool {
	listCtx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	infos, err := chromedp.Targets(listCtx)
	if err != nil {
		return nil
	}
	ids := make(map[cdptarget.ID]bool, len(infos))
	for _, info := range infos {
		if info != nil && info.Type == "page" {
			ids[info.TargetID] = true
		}
	}
	return ids
}

// newPageTargetsSince returns the page targets that exist now but weren't in
// before — i.e. tabs opened since the baseline snapshot. Each carries its URL
// and Title so the agent gets enough context to decide whether to navigate to
// it. Returns nil if no new tabs or the lookup failed.
func newPageTargetsSince(s *browserSession, before map[cdptarget.ID]bool) []*cdptarget.Info {
	if before == nil {
		return nil
	}
	listCtx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	infos, err := chromedp.Targets(listCtx)
	if err != nil {
		return nil
	}
	var newTabs []*cdptarget.Info
	for _, info := range infos {
		if info == nil || info.Type != "page" {
			continue
		}
		if !before[info.TargetID] {
			newTabs = append(newTabs, info)
		}
	}
	return newTabs
}

// switchSessionTab re-points a session onto an existing tab. Attaching a
// sibling chromedp context via WithTargetID needs only the Browser the
// session already carries (the old "needs the allocator" concern was
// overcautious). The previous tab stays open — its context is abandoned,
// since canceling a chromedp tab context would close the target.
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// openSessionTab creates a brand-new tab in the session's browser and re-points
// the session onto it. Uses Target.createTarget over the browser-level
// connection (switchSessionTab then attaches to the new target with its proven
// WithTargetID path). The previously driven tab stays open.
func openSessionTab(s *browserSession, url string) error {
	c := chromedp.FromContext(s.ctx)
	if c == nil || c.Browser == nil {
		return errors.New("session browser not connected")
	}
	if url == "" {
		url = "about:blank"
	}
	var created struct {
		TargetID cdptarget.ID `json:"targetId"`
	}
	createCtx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	err := c.Browser.Execute(createCtx, "Target.createTarget",
		map[string]any{"url": url}, &created)
	if err != nil {
		return fmt.Errorf("create tab: %w", err)
	}
	if created.TargetID == "" {
		return errors.New("Target.createTarget returned no targetId")
	}
	return switchSessionTab(s, created.TargetID)
}

func switchSessionTab(s *browserSession, id cdptarget.ID) error {
	s.tabMu.Lock()
	defer s.tabMu.Unlock()
	c := chromedp.FromContext(s.ctx)
	if c == nil || c.Browser == nil {
		return errors.New("session browser not connected")
	}
	newCtx, cancel := chromedp.NewContext(s.ctx, chromedp.WithTargetID(id))
	boot := make(chan error, 1)
	go func() { boot <- chromedp.Run(newCtx) }()
	select {
	case err := <-boot:
		if err != nil {
			cancel()
			return fmt.Errorf("attach tab %s: %w", id, err)
		}
	case <-time.After(15 * time.Second):
		cancel()
		return fmt.Errorf("attach tab %s: timed out", id)
	}
	s.ctx = newCtx
	s.ctxCancel = cancel
	s.refs.Store(nil) // refs belonged to the abandoned page
	panelRestart(s)   // live panel stream follows the new tab
	return nil
}

// sessionTargetID reports the target (tab) the session currently drives.
func sessionTargetID(s *browserSession) cdptarget.ID {
	if c := chromedp.FromContext(s.ctx); c != nil && c.Target != nil {
		return c.Target.TargetID
	}
	return ""
}

// waitForPageLoad waits for a page to finish loading after a click that
// triggered navigation. Returns the final URL and true if navigation occurred.
func waitForPageLoad(s *browserSession, urlBefore string) (urlAfter string, navigated bool) {
	// Wait for the new page to be ready (up to 8s).
	waitCtx, cancel := context.WithTimeout(s.ctx, 8*time.Second)
	defer cancel()
	_ = chromedp.Run(waitCtx, chromedp.WaitReady("body"))
	// Check if the URL actually changed.
	_ = runBrowserAction(s.ctx, s, chromedp.Location(&urlAfter))
	if urlBefore != "" && urlAfter != "" && urlBefore != urlAfter {
		return urlAfter, true
	}
	return urlAfter, false
}

// captureThumbnailBase64 takes a JPEG screenshot and returns a base64 string
// suitable for embedding in tool results. Returns "" on failure.
func captureThumbnailBase64(s *browserSession) string {
	if s.ctx.Err() != nil {
		return ""
	}
	var buf []byte
	capCtx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	if err := chromedp.Run(capCtx, chromedp.CaptureScreenshot(&buf)); err != nil {
		return ""
	}
	// Compress to JPEG quality 30 to keep output small (~5-10KB).
	var jpegBuf bytes.Buffer
	img, _, err := image.Decode(bytes.NewReader(buf))
	if err != nil {
		// Fallback: just base64 the raw PNG (larger but always works).
		return base64.StdEncoding.EncodeToString(buf)
	}
	if err := jpeg.Encode(&jpegBuf, img, &jpeg.Options{Quality: 30}); err != nil {
		return base64.StdEncoding.EncodeToString(buf)
	}
	return base64.StdEncoding.EncodeToString(jpegBuf.Bytes())
}

// --- helpers ----------------------------------------------------------------

func browserAttachmentsDir() string {
	// Mirror web_fetch / image_understand's attachment convention so screenshots
	// are discovered by the same attachment UI.
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, ".hiq", "attachments")
	}
	return filepath.Join(os.TempDir(), "hiq-browser")
}
