package builtin

// Interactive in-app browser panel (the "embedded browser" tier of the
// cowork dock, modeled after snow-app's webview panel but implemented over
// CDP because Wails has no <webview> equivalent): the driven browser is
// mirrored LIVE in the panel via Page.startScreencast, and the panel
// forwards pointer/keyboard input back through Input.dispatch*, so the
// user browses the very page instance the agent drives — one target, two
// drivers. This is the live tier on top of the post-action mirror ("frame"
// kind), which stays for CLI-less sinks and history.
//
// Flow control: the desktop flips panel visibility (SetPanelStreaming) when
// the dock's browser tab mounts/unmounts; streaming stops entirely while
// hidden so a hidden panel costs no screencast frames. Frames are emitted
// through the same browserPanelSink as mirror frames (Kind "live"), throttled
// to ~10 fps; every received frame is acknowledged promptly or CDP stalls
// the stream after the first frame.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/input"
	cdprotopage "github.com/chromedp/cdproto/page"
	cdptarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// panelStreamState is the per-session live-stream bookkeeping. Lives on
// browserSession via an atomic pointer so session constructors stay untouched.
type panelStreamState struct {
	mu       sync.Mutex
	active   bool            // screencast currently running on a tab
	tabCtx   context.Context // chromedp context the listener is attached to
	targetID cdptarget.ID    // tab this stream follows
	lastEmit atomic.Int64    // unix nanos of the last emitted frame (throttle)
	visible  atomic.Bool     // panel visibility (desktop flow control)

	// events feeds the stream's OWN goroutine (writeLoop). CDP events are
	// queued here and handled off the listener; see panelStreamEvent.
	events chan panelStreamEvent
	// writerOnce guards the single writeLoop goroutine per session.
	writerOnce sync.Once
	// lastURL/lastTitle cache the address-bar values so image frames don't
	// each cost a CDP round trip. Written only by writeLoop (single goroutine).
	lastURL   string
	lastTitle string
}

// panelStreamQueue bounds the event queue. Screencast is capped at ~20 fps
// (EveryNthFrame(3)) while the writer does one CDP round trip per drained
// batch; the buffer only has to absorb bursts, because writeLoop drains the
// whole queue at once. Overflow still drops the OLDEST event rather than
// blocking — a stalled listener is fatal, a skipped frame is not.
const panelStreamQueue = 32

// panelStreamEvent is one queued CDP event for the stream goroutine.
//
// WHY THIS TYPE EXISTS: a chromedp listener runs INLINE on the target's
// message-queue goroutine (chromedp/target.go run() → runListeners →
// listener.fn(ev)), and that same goroutine is what reads command RESPONSES
// back. So a listener that calls chromedp.Run — as this handler used to do for
// Page.screencastFrameAck and the title/URL probe — waits for a response that
// only the goroutine it is blocking could deliver. The session deadlocks.
//
// That was a real, user-visible bug: the first screencast frame stalled the
// message loop, the mandatory ack never landed, no image ever reached the panel
// (blank viewport), and the very next CDP call — the browser_open navigate —
// timed out after 60s while the page itself loaded fine in Chrome. Listeners
// must therefore only ENQUEUE; every blocking call happens in writeLoop.
type panelStreamEvent struct {
	tabCtx    context.Context // tab the event came from; acks go back on it
	sessionID int64           // screencast frame id (non-zero ⇒ needs an ack)
	data      string          // jpeg base64 payload ("" = no image)
	url       string          // FrameNavigated url ("" = none)
	probe     bool            // LoadEventFired: re-read title+url via CDP
}

// panelLiveFrameInterval is the minimum gap between emitted frames (~10 fps
// cap). Screencast may deliver faster; the throttle drops intermediates while
// still acknowledging them.
const panelLiveFrameInterval = 100 * time.Millisecond

// panelStreamDefaultVisible seeds newly opened sessions: the desktop turns
// streaming off for its hidden dock tabs; a session opened before that call
// starts streaming (the panel then shows the page the moment it's opened).
var panelStreamDefaultVisible atomic.Bool

func init() { panelStreamDefaultVisible.Store(true) }

// panelStream returns (creating lazily) the session's stream state.
func panelStream(s *browserSession) *panelStreamState {
	st := s.panelStream.Load()
	if st == nil {
		st = &panelStreamState{events: make(chan panelStreamEvent, panelStreamQueue)}
		if !s.panelStream.CompareAndSwap(nil, st) {
			st = s.panelStream.Load()
		}
	}
	return st
}

// enqueue hands an event to the stream goroutine without ever blocking. It is
// called from the chromedp listener, i.e. while chromedp holds the target's
// listenersMu — blocking here would wedge every later CDP call on the session.
// When the queue is full the OLDEST event is dropped: an unacked screencast
// frame is merely skipped by Chrome, while a stalled listener is fatal.
func (st *panelStreamState) enqueue(ev panelStreamEvent) {
	if st.events == nil {
		return
	}
	select {
	case st.events <- ev:
		return
	default:
	}
	select {
	case <-st.events:
	default:
	}
	select {
	case st.events <- ev:
	default:
	}
}

// writeLoop is the session's single stream goroutine: it acks screencast frames
// and emits panel frames. All blocking CDP calls live here, never in a listener.
// It exits with the session.
//
// It drains the WHOLE queue per wake-up and acknowledges every frame in the
// batch, then renders only the newest one. Acking all of them matters: Chrome
// stops pushing frames until the last one is acknowledged, so a batch where only
// the newest frame was acked (the old drop-oldest behaviour) could leave the
// stream waiting forever on a frame nobody acknowledged — the panel image
// freezes even though the page is alive. Acks are cheap and must not be skipped;
// only the JPEG decode/emit is throttled.
func (st *panelStreamState) writeLoop(s *browserSession) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case ev := <-st.events:
			batch := []panelStreamEvent{ev}
		drain:
			for len(batch) < panelStreamQueue {
				select {
				case more := <-st.events:
					batch = append(batch, more)
				default:
					break drain
				}
			}
			st.deliver(s, batch)
		}
	}
}

// deliver acks every screencast frame in the batch (mandatory — Chrome stops the
// stream on the first unacked frame) and emits the newest frame it carries.
func (st *panelStreamState) deliver(s *browserSession, batch []panelStreamEvent) {
	newest := batch[len(batch)-1]
	for _, ev := range batch {
		if ev.sessionID != 0 {
			ackCtx, cancel := context.WithTimeout(ev.tabCtx, 5*time.Second)
			_ = cdprotopage.ScreencastFrameAck(ev.sessionID).Do(ackCtx)
			cancel()
		}
		if ev.url != "" {
			st.lastURL = ev.url
		}
	}
	if newest.probe {
		if title, url := panelTitleAndURL(newest.tabCtx); url != "" {
			st.lastURL, st.lastTitle = url, title
		}
		// A finished load must produce a picture DETERMINISTICALLY. Screencast is
		// the motion channel and Chrome only pushes frames when it composites:
		// a page that renders once and then sits still (every login form, every
		// error page) can composite fewer frames than everyNthFrame asks for, so
		// nothing arrives and the dock shows the URL over an empty viewport —
		// the blank-panel bug all over again. Capture explicitly instead.
		if st.visible.Load() {
			if img, ok := capturePanelJPEG(newest.tabCtx); ok {
				st.lastEmit.Store(time.Now().UnixNano())
				EmitBrowserPanel(BrowserPanelFrame{
					Kind:      "live",
					Source:    "tool",
					Image:     img,
					URL:       st.lastURL,
					Title:     st.lastTitle,
					TabID:     string(sessionTargetID(s)),
					SessionID: s.id,
					Text:      "loaded",
				})
				return
			}
		}
	}
	if newest.data == "" && !newest.probe && newest.url == "" {
		return
	}
	if !st.visible.Load() {
		return // panel hidden: ack only, nothing to show
	}
	if newest.data != "" {
		// Throttle image frames; the acks above already keep CDP flowing.
		now := time.Now().UnixNano()
		if prev := st.lastEmit.Load(); prev != 0 && now-prev < int64(panelLiveFrameInterval) {
			return
		}
		st.lastEmit.Store(now)
	}
	EmitBrowserPanel(BrowserPanelFrame{
		Kind:      "live",
		Source:    "tool",
		Image:     panelJPEGDataURL(newest.data),
		URL:       st.lastURL,
		Title:     st.lastTitle,
		TabID:     string(sessionTargetID(s)),
		SessionID: s.id,
		Text:      panelEventText(newest),
	})
}

// panelJPEGDataURL wraps a raw screencast payload, empty in ⇒ empty out.
func panelJPEGDataURL(data string) string {
	if data == "" {
		return ""
	}
	return "data:image/jpeg;base64," + data
}

// panelEventText labels the frame for the panel's status line.
func panelEventText(ev panelStreamEvent) string {
	switch {
	case ev.probe:
		return "loaded"
	case ev.url != "":
		return "navigating"
	default:
		return ""
	}
}

// SetPanelStreaming flips live-panel streaming for every live session. The
// desktop calls it when the dock's browser tab mounts (true) or unmounts
// (false). Sessions opened later follow panelStreamDefaultVisible.
//
// When the panel becomes visible on a session whose screencast is ALREADY up,
// startPanelStream is a no-op (idempotent by design) and Chrome only pushes
// frames on repaint — so a page that finished loading while the dock was closed
// (the common case: the agent opens a URL, the user then clicks the browser tab)
// would leave the viewport blank until the page happened to repaint. We
// therefore push one on-demand frame on every hide→show transition.
func SetPanelStreaming(on bool) {
	panelStreamDefaultVisible.Store(on)
	browserMu.Lock()
	sessions := make([]*browserSession, 0, len(browserSessions))
	for _, s := range browserSessions {
		sessions = append(sessions, s)
	}
	browserMu.Unlock()
	for _, s := range sessions {
		st := panelStream(s)
		st.visible.Store(on)
		if !on {
			stopPanelStream(s)
			continue
		}
		st.mu.Lock()
		alreadyStreaming := st.active
		st.mu.Unlock()
		startPanelStream(s)
		if alreadyStreaming {
			go refreshPanelFrame(s)
		}
	}
}

// capturePanelJPEG grabs one JPEG frame of the tab as a data URL. Every path
// that needs a picture the screencast cannot be trusted to deliver (load
// finished, panel just became visible, stream just started) goes through here:
// screencast only fires when Chrome composites, so a page that renders once and
// then sits still can leave the viewport empty forever.
func capturePanelJPEG(tabCtx context.Context) (string, bool) {
	if tabCtx == nil || tabCtx.Err() != nil {
		return "", false
	}
	sctx, cancel := context.WithTimeout(tabCtx, 10*time.Second)
	defer cancel()
	var data []byte
	// Page.captureScreenshot returns the bytes (its Do signature differs from
	// chromedp.Action), so wrap it.
	err := chromedp.Run(sctx, chromedp.ActionFunc(func(cctx context.Context) error {
		b, e := cdprotopage.CaptureScreenshot().
			WithFormat(cdprotopage.CaptureScreenshotFormatJpeg).
			WithQuality(60).
			Do(cctx)
		data = b
		return e
	}))
	if err != nil || len(data) == 0 {
		return "", false
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data), true
}

// refreshPanelFrame pushes one on-demand screenshot for a session whose
// screencast is already running but whose frames the panel may have missed
// (hidden while the page loaded, or a static page that never repaints). Runs off
// the caller's goroutine: it is a plain CDP call plus an emit.
func refreshPanelFrame(s *browserSession) {
	if browserPanelSink == nil || s.ctx == nil || s.ctx.Err() != nil {
		return
	}
	st := panelStream(s)
	if !st.visible.Load() {
		return // panel hidden again before we got here
	}
	img, ok := capturePanelJPEG(s.ctx)
	if !ok {
		return // best-effort: the live stream, if any, still covers the panel
	}
	title, url := panelTitleAndURL(s.ctx)
	EmitBrowserPanel(BrowserPanelFrame{
		Kind:      "live",
		Source:    "tool",
		Image:     img,
		URL:       url,
		Title:     title,
		TabID:     string(sessionTargetID(s)),
		SessionID: s.id,
		Text:      "refresh",
	})
	st.lastEmit.Store(time.Now().UnixNano())
}

// startPanelStream begins screencasting the session's current tab. Safe to
// call repeatedly (idempotent while the stream is up); also called after tab
// switches (panelRestart) to follow the new target.
func startPanelStream(s *browserSession) {
	if browserPanelSink == nil || s.ctx.Err() != nil {
		return
	}
	st := panelStream(s)
	st.visible.Store(panelStreamDefaultVisible.Load())
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.active {
		return
	}
	tabCtx := s.ctx
	// Event listener bound to THIS tab's context; stale events from a tab the
	// session switched away from are filtered inside the handler.
	chromedp.ListenTarget(tabCtx, panelEventHandler(s, st, tabCtx))
	sctx, cancel := context.WithTimeout(tabCtx, 5*time.Second)
	defer cancel()
	err := chromedp.Run(sctx, cdprotopage.StartScreencast().
		WithFormat(cdprotopage.ScreencastFormatJpeg).
		WithQuality(60).
		WithMaxWidth(1280).
		WithMaxHeight(800).
		// EveryNthFrame(1) asked Chrome to JPEG-encode EVERY composited frame
		// (up to 60/s) for a page whose background animates, which saturated the
		// browser process: CDP round trips stretched to ~50ms each, so a click
		// felt like molasses and typing lagged behind the keyboard. The panel
		// only renders ~10 fps anyway (panelLiveFrameInterval), so feed it ~20.
		WithEveryNthFrame(3))
	if err != nil {
		return
	}
	st.active = true
	st.tabCtx = tabCtx
	st.targetID = sessionTargetID(s)
	st.writerOnce.Do(func() { go st.writeLoop(s) })
	// The first frame must not depend on the page repainting (see capturePanelJPEG).
	go refreshPanelFrame(s)
}

// stopPanelStream halts the session's screencast (panel hidden / shutdown).
func stopPanelStream(s *browserSession) {
	st := panelStream(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.stopLocked()
}

func (st *panelStreamState) stopLocked() {
	if !st.active {
		return
	}
	st.active = false
	ctx := st.tabCtx
	if ctx == nil || ctx.Err() != nil {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = chromedp.Run(sctx, cdprotopage.StopScreencast())
}

// panelRestart re-targets the stream after a tab switch: tear down the old
// tab's screencast, start one on the new tab. Called (best-effort) from
// switchSessionTab.
func panelRestart(s *browserSession) {
	if browserPanelSink == nil {
		return
	}
	st := panelStream(s)
	if !st.visible.Load() {
		return
	}
	st.mu.Lock()
	st.stopLocked()
	st.mu.Unlock()
	startPanelStream(s)
}

// panelTabIsCurrent reports whether the event arrived from the tab the session
// currently drives. Reads s.ctx under tabMu (switchSessionTab writes it there).
func panelTabIsCurrent(s *browserSession, tabCtx context.Context) bool {
	s.tabMu.Lock()
	cur := s.ctx
	s.tabMu.Unlock()
	return cur == tabCtx
}

// panelEventHandler builds the per-tab CDP event handler. It ONLY enqueues:
// this runs on chromedp's message-loop goroutine with the target's listenersMu
// held, so any blocking CDP call here deadlocks the session (see
// panelStreamEvent). Acks, URL probes and sink emits happen in writeLoop.
func panelEventHandler(s *browserSession, st *panelStreamState, tabCtx context.Context) func(interface{}) {
	return func(ev interface{}) {
		switch e := ev.(type) {
		case *cdprotopage.EventScreencastFrame:
			if !panelTabIsCurrent(s, tabCtx) {
				return // stale tab — its screencast is stopped by panelRestart
			}
			st.enqueue(panelStreamEvent{tabCtx: tabCtx, sessionID: e.SessionID, data: e.Data})
		case *cdprotopage.EventFrameNavigated:
			if e.Frame == nil || e.Frame.ParentID != "" {
				return // iframe — only the main frame drives the address bar
			}
			if !panelTabIsCurrent(s, tabCtx) {
				return
			}
			st.enqueue(panelStreamEvent{tabCtx: tabCtx, url: e.Frame.URL})
		case *cdprotopage.EventLoadEventFired:
			if !panelTabIsCurrent(s, tabCtx) {
				return
			}
			st.lastEmit.Store(0) // let the next frame through immediately
			st.enqueue(panelStreamEvent{tabCtx: tabCtx, probe: true})
		}
	}
}

// panelTitleAndURL reads the tab's title and location. Called ONLY from
// writeLoop — a chromedp.Run from a listener would deadlock the session.
func panelTitleAndURL(tabCtx context.Context) (title, url string) {
	sctx, cancel := context.WithTimeout(tabCtx, 3*time.Second)
	defer cancel()
	var t, u string
	if err := chromedp.Run(sctx, chromedp.Title(&t), chromedp.Location(&u)); err != nil {
		return "", ""
	}
	return t, u
}

// --- panel input + navigation (desktop App method surface) -------------------
//
// The desktop panel forwards user input here. Coordinates arrive normalized
// (0..1) relative to the streamed frame image; they are mapped to CSS-page
// coordinates via Page.getLayoutMetrics at dispatch time, which absorbs DPR
// and screencast downscaling.

// PanelInputEvent is one input action from the interactive panel.
type PanelInputEvent struct {
	Type       string  `json:"type"` // click|down|up|move|wheel|key|text
	X          float64 `json:"x,omitempty"`
	Y          float64 `json:"y,omitempty"`
	DeltaX     float64 `json:"deltaX,omitempty"`
	DeltaY     float64 `json:"deltaY,omitempty"`
	Button     string  `json:"button,omitempty"` // left|middle|right
	ClickCount int     `json:"clickCount,omitempty"`
	// Buttons is the DOM PointerEvent.buttons bitmask (left=1, right=2,
	// middle=4) that the shell reports with a move. Without it a drag is
	// indistinguishable from a hover, so selecting text / dragging a slider in
	// the panel did nothing.
	Buttons   int64  `json:"buttons,omitempty"`
	Key       string `json:"key,omitempty"` // named key: Enter|Backspace|Escape|...
	Text      string `json:"text,omitempty"`
	Modifiers int64  `json:"modifiers,omitempty"`
}

// PanelDispatchInput applies one panel input event to the session's current
// tab. Input never routes through runBrowserAction's post-action mirror —
// the live stream already shows the result.
func PanelDispatchInput(id string, ev PanelInputEvent) error {
	s, err := getBrowserSession(id)
	if err != nil {
		slog.Warn("panel: input for unknown session", "session", id, "type", ev.Type, "err", err)
		return err
	}
	// Hover moves arrive ~16/s; log only the discrete actions so a user report of
	// "the panel ignores my clicks / my typing" can be answered from app.log
	// alone. Keyboard events carry no coordinates, so log what actually matters
	// for them (a keystroke that never arrives is a different bug from one that
	// arrives and is dropped by the page).
	if ev.Type != "move" {
		attrs := []any{"session", id, "type", ev.Type}
		switch ev.Type {
		case "text":
			txt := ev.Text
			if len(txt) > 16 {
				txt = txt[:16] + "…"
			}
			attrs = append(attrs, "text", txt)
		case "key":
			attrs = append(attrs, "key", ev.Key, "modifiers", ev.Modifiers)
		default:
			attrs = append(attrs, "x", ev.X, "y", ev.Y, "button", ev.Button, "clickCount", ev.ClickCount)
		}
		slog.Info("panel: input", attrs...)
	}
	if s.ctx.Err() != nil {
		return fmt.Errorf("browser session closed: %w", s.ctx.Err())
	}
	ctx, cancel := context.WithTimeout(s.ctx, browserActionTimeout)
	defer cancel()
	switch ev.Type {
	case "down", "up", "click", "move":
		return panelMouse(ctx, s, ev)
	case "wheel":
		var x, y float64
		if err := chromedp.Run(ctx, panelCssPoint(s, ev.X, ev.Y, &x, &y)); err != nil {
			return err
		}
		return chromedp.Run(ctx, panelMouseWheel(x, y, ev.DeltaX, ev.DeltaY, ev.Modifiers))
	case "key":
		return panelKey(ctx, ev)
	case "text":
		n := len([]rune(ev.Text))
		switch {
		case n == 0:
			return nil
		case n == 1:
			// One character = one physical key press. Emitting a real key event
			// (instead of Input.insertText) is what makes a page that validates
			// on keydown — login forms do — see the input at all.
			return panelTypeChar(ctx, ev.Text)
		default:
			// A multi-character string is an IME commit (拼音 → 汉字); it has no
			// key code, so insertText is the correct primitive for it.
			return chromedp.Run(ctx, input.InsertText(ev.Text))
		}
	default:
		return fmt.Errorf("unknown panel input type %q", ev.Type)
	}
}

// panelMouse dispatches pressed/released/moved; a click is pressed+released
// with the same click count at the same point.
func panelMouse(ctx context.Context, s *browserSession, ev PanelInputEvent) error {
	var x, y float64
	if err := chromedp.Run(ctx, panelCssPoint(s, ev.X, ev.Y, &x, &y)); err != nil {
		return err
	}
	btn := panelMouseButton(ev.Button)
	cc := ev.ClickCount
	if cc < 1 {
		cc = 1
	}
	switch ev.Type {
	case "down":
		// buttons carries the state DURING the press (bit set): Chromium reads it
		// to start a drag/selection, and a press claiming "no button down" makes
		// the page ignore it.
		return chromedp.Run(ctx, panelMouseEvent("mousePressed", x, y, btn, cc, ev.Modifiers, panelButtonMask(btn), 0, 0))
	case "up":
		return chromedp.Run(ctx, panelMouseEvent("mouseReleased", x, y, btn, cc, ev.Modifiers, 0, 0, 0))
	case "click":
		if err := chromedp.Run(ctx, panelMouseEvent("mousePressed", x, y, btn, cc, ev.Modifiers, panelButtonMask(btn), 0, 0)); err != nil {
			return err
		}
		return chromedp.Run(ctx, panelMouseEvent("mouseReleased", x, y, btn, cc, ev.Modifiers, 0, 0, 0))
	default: // move (hover, or drag while a button is held)
		return chromedp.Run(ctx, panelMouseEvent("mouseMoved", x, y, input.None, 0, ev.Modifiers, ev.Buttons, 0, 0))
	}
}

// panelButtonMask maps a mouse button to the CDP `buttons` bitmask
// (Left=1, Right=2, Middle=4).
func panelButtonMask(b input.MouseButton) int64 {
	switch b {
	case input.Right:
		return 2
	case input.Middle:
		return 4
	case input.Left:
		return 1
	default:
		return 0
	}
}

// panelCssPoint maps a frame-normalized point to CSS page coordinates using
// the live layout metrics (CSS visual viewport, DPR-independent).
func panelCssPoint(s *browserSession, nx, ny float64, x, y *float64) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		_, _, _, _, cssVis, _, err := cdprotopage.GetLayoutMetrics().Do(ctx)
		if err != nil {
			return err
		}
		if cssVis == nil {
			return errors.New("panel: layout metrics unavailable")
		}
		*x = panelClamp01(nx) * cssVis.ClientWidth
		*y = panelClamp01(ny) * cssVis.ClientHeight
		return nil
	})
}

func panelClamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func panelMouseButton(b string) input.MouseButton {
	switch strings.ToLower(b) {
	case "middle":
		return input.Middle
	case "right":
		return input.Right
	default:
		return input.Left
	}
}

// panelMouseEvent wraps Input.dispatchMouseEvent builder chaining.
func panelMouseEvent(typ string, x, y float64, btn input.MouseButton, clickCount int, modifiers, buttons int64, dx, dy float64) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		p := input.DispatchMouseEvent(input.MouseType(typ), x, y).
			WithButton(btn).
			WithClickCount(int64(clickCount)).
			WithButtons(buttons).
			WithDeltaX(dx).
			WithDeltaY(dy)
		if modifiers != 0 {
			p = p.WithModifiers(input.Modifier(modifiers))
		}
		return p.Do(ctx)
	})
}

func panelMouseWheel(x, y, dx, dy float64, modifiers int64) chromedp.Action {
	return panelMouseEvent("mouseWheel", x, y, input.None, 0, modifiers, 0, dx, dy)
}

// panelNamedKeys maps panel key names to CDP key params (key, code,
// windowsVirtualKeyCode) — the keys a keyboard-driven browser needs.
var panelNamedKeys = map[string]struct {
	Key  string
	Code string
	VK   int64
}{
	"Enter":      {"Enter", "Enter", 13},
	"Backspace":  {"Backspace", "Backspace", 8},
	"Delete":     {"Delete", "Delete", 46},
	"Tab":        {"Tab", "Tab", 9},
	"Escape":     {"Escape", "Escape", 27},
	"ArrowUp":    {"ArrowUp", "ArrowUp", 38},
	"ArrowDown":  {"ArrowDown", "ArrowDown", 40},
	"ArrowLeft":  {"ArrowLeft", "ArrowLeft", 37},
	"ArrowRight": {"ArrowRight", "ArrowRight", 39},
	"Home":       {"Home", "Home", 36},
	"End":        {"End", "End", 35},
	"PageUp":     {"PageUp", "PageUp", 33},
	"PageDown":   {"PageDown", "PageDown", 34},
	"F5":         {"F5", "F5", 116},
}

func panelKey(ctx context.Context, ev PanelInputEvent) error {
	k, ok := panelNamedKeys[ev.Key]
	if !ok {
		// A single character (letters, digits, symbols) is still a key press:
		// with a modifier that is Ctrl+A / Ctrl+C / Ctrl+V and friends, which
		// must reach the page as KEY events — Ctrl+V in particular only pastes
		// if the page sees the shortcut. Multi-character text is an IME commit.
		if runes := []rune(ev.Key); len(runes) == 1 {
			return panelTypeCharMod(ctx, ev.Key, ev.Modifiers)
		}
		if ev.Text != "" {
			return chromedp.Run(ctx, input.InsertText(ev.Text))
		}
		return fmt.Errorf("unsupported key %q", ev.Key)
	}
	dispatch := func(typ input.KeyType) chromedp.Action {
		return chromedp.ActionFunc(func(ctx context.Context) error {
			p := input.DispatchKeyEvent(typ).
				WithKey(k.Key).
				WithCode(k.Code).
				WithWindowsVirtualKeyCode(k.VK)
			if ev.Modifiers != 0 {
				p = p.WithModifiers(input.Modifier(ev.Modifiers))
			}
			return p.Do(ctx)
		})
	}
	if err := chromedp.Run(ctx, dispatch(input.KeyRawDown)); err != nil {
		return err
	}
	return chromedp.Run(ctx, dispatch(input.KeyUp))
}

// panelTypeChar types one printable character the way a physical keyboard does:
// keyDown carrying the text, then keyUp.
//
// WHY NOT Input.insertText: insertText is the IME primitive — it inserts the
// text and fires NO key events at all. A page that reacts to keydown/keypress
// (a login form's live validation, a search box's Enter handler, a masked
// password field) therefore never sees the character, and worst case swallows
// it. Only multi-character IME commits should use insertText.
func panelTypeChar(ctx context.Context, ch string) error {
	return panelTypeCharMod(ctx, ch, 0)
}

func panelTypeCharMod(ctx context.Context, ch string, modifiers int64) error {
	key, code, vk, shift := panelCharKey(ch)
	if shift {
		modifiers |= 8 // shift
	}
	press := func(typ input.KeyType, withText bool) chromedp.Action {
		return chromedp.ActionFunc(func(ctx context.Context) error {
			p := input.DispatchKeyEvent(typ).
				WithKey(key).
				WithCode(code).
				WithWindowsVirtualKeyCode(vk)
			if withText {
				p = p.WithText(ch).WithUnmodifiedText(ch)
			}
			if modifiers != 0 {
				p = p.WithModifiers(input.Modifier(modifiers))
			}
			return p.Do(ctx)
		})
	}
	if err := chromedp.Run(ctx, press(input.KeyDown, true)); err != nil {
		return err
	}
	return chromedp.Run(ctx, press(input.KeyUp, false))
}

// panelCharKey is the US-layout definition for a printable ASCII character
// (key, code, windows virtual key code, needs-shift), mirroring the shape of
// Puppeteer's USKeyboardLayout. Pages that read event.keyCode/which — still
// common in enterprise web apps — only behave correctly when it is set.
func panelCharKey(ch string) (key, code string, vk int64, shift bool) {
	r := []rune(ch)
	if len(r) != 1 {
		return ch, "", 0, false
	}
	c := r[0]
	switch {
	case c >= 'a' && c <= 'z':
		up := c - 32
		return string(c), "Key" + string(up), int64(up), false
	case c >= 'A' && c <= 'Z':
		return string(c), "Key" + string(c), int64(c), true
	case c >= '0' && c <= '9':
		return string(c), "Digit" + string(c), int64(c), false
	}
	if p, ok := panelPunctKeys[c]; ok {
		return p.key, p.code, p.vk, p.shift
	}
	// Non-ASCII (e.g. a CJK character typed directly): no meaningful key code
	// exists on a US layout, so send the character with the text field only.
	return ch, "", 0, false
}

var panelPunctKeys = map[rune]struct {
	key   string
	code  string
	vk    int64
	shift bool
}{
	' ':  {" ", "Space", 32, false},
	'`':  {"`", "Backquote", 192, false},
	'~':  {"~", "Backquote", 192, true},
	'-':  {"-", "Minus", 189, false},
	'_':  {"_", "Minus", 189, true},
	'=':  {"=", "Equal", 187, false},
	'+':  {"+", "Equal", 187, true},
	'[':  {"[", "BracketLeft", 219, false},
	'{':  {"{", "BracketLeft", 219, true},
	']':  {"]", "BracketRight", 221, false},
	'}':  {"}", "BracketRight", 221, true},
	'\\': {"\\", "Backslash", 220, false},
	'|':  {"|", "Backslash", 220, true},
	';':  {";", "Semicolon", 186, false},
	':':  {":", "Semicolon", 186, true},
	'\'': {"'", "Quote", 222, false},
	'"':  {"\"", "Quote", 222, true},
	',':  {",", "Comma", 188, false},
	'<':  {"<", "Comma", 188, true},
	'.':  {".", "Period", 190, false},
	'>':  {">", "Period", 190, true},
	'/':  {"/", "Slash", 191, false},
	'?':  {"?", "Slash", 191, true},
	'!':  {"!", "Digit1", 49, true},
	'@':  {"@", "Digit2", 50, true},
	'#':  {"#", "Digit3", 51, true},
	'$':  {"$", "Digit4", 52, true},
	'%':  {"%", "Digit5", 53, true},
	'^':  {"^", "Digit6", 54, true},
	'&':  {"&", "Digit7", 55, true},
	'*':  {"*", "Digit8", 56, true},
	'(':  {"(", "Digit9", 57, true},
	')':  {")", "Digit0", 48, true},
}

// --- navigation + tabs for the panel toolbar ---------------------------------

// PanelNavigate drives the session's current tab to url (address bar).
func PanelNavigate(id, url string) error {
	s, err := getBrowserSession(id)
	if err != nil {
		return err
	}
	if url == "" {
		return errors.New("empty url")
	}
	if !strings.Contains(url, "://") && !strings.HasPrefix(url, "about:") {
		url = "https://" + url // bare host convenience, like a real address bar
	}
	ctx, cancel := context.WithTimeout(s.ctx, browserActionTimeout)
	defer cancel()
	return chromedp.Run(ctx, chromedp.Navigate(url))
}

// PanelBack / PanelForward / PanelReload drive the toolbar buttons.
func PanelBack(id string) error {
	return panelSimpleAction(id, chromedp.NavigateBack())
}

func PanelForward(id string) error {
	return panelSimpleAction(id, chromedp.NavigateForward())
}

func PanelReload(id string) error {
	return panelSimpleAction(id, chromedp.Reload())
}

func panelSimpleAction(id string, action chromedp.Action) error {
	s, err := getBrowserSession(id)
	if err != nil {
		return err
	}
	if s.ctx.Err() != nil {
		return fmt.Errorf("browser session closed: %w", s.ctx.Err())
	}
	ctx, cancel := context.WithTimeout(s.ctx, browserActionTimeout)
	defer cancel()
	return chromedp.Run(ctx, action)
}

// PanelTab is one entry of the panel's tab strip.
type PanelTab struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// PanelTabs lists the session's page targets for the tab strip.
func PanelTabs(id string) ([]PanelTab, error) {
	s, err := getBrowserSession(id)
	if err != nil {
		return nil, err
	}
	infos, err := pageTargetInfos(s)
	if err != nil {
		return nil, err
	}
	active := sessionTargetID(s)
	out := make([]PanelTab, 0, len(infos))
	for _, t := range infos {
		out = append(out, PanelTab{
			ID:     string(t.TargetID),
			Title:  t.Title,
			URL:    t.URL,
			Active: cdptarget.ID(t.TargetID) == active,
		})
	}
	return out, nil
}

// PanelSwitchTab moves the session (and the live stream) to the given target.
func PanelSwitchTab(id, tabID string) error {
	s, err := getBrowserSession(id)
	if err != nil {
		return err
	}
	if err := switchSessionTab(s, cdptarget.ID(tabID)); err != nil {
		return err
	}
	panelRestart(s)
	return nil
}
