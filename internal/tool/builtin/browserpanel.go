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
	"errors"
	"fmt"
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
		st = &panelStreamState{}
		if !s.panelStream.CompareAndSwap(nil, st) {
			st = s.panelStream.Load()
		}
	}
	return st
}

// SetPanelStreaming flips live-panel streaming for every live session. The
// desktop calls it when the dock's browser tab mounts (true) or unmounts
// (false). Sessions opened later follow panelStreamDefaultVisible.
func SetPanelStreaming(on bool) {
	panelStreamDefaultVisible.Store(on)
	browserMu.Lock()
	sessions := make([]*browserSession, 0, len(browserSessions))
	for _, s := range browserSessions {
		sessions = append(sessions, s)
	}
	browserMu.Unlock()
	for _, s := range sessions {
		panelStream(s).visible.Store(on)
		if on {
			startPanelStream(s)
		} else {
			stopPanelStream(s)
		}
	}
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
		WithMaxWidth(1440).
		WithMaxHeight(900).
		WithEveryNthFrame(1))
	if err != nil {
		return
	}
	st.active = true
	st.tabCtx = tabCtx
	st.targetID = sessionTargetID(s)
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

// panelEventHandler builds the per-tab CDP event handler: screencast frames
// (throttled emit + mandatory ack) and page-load status frames.
func panelEventHandler(s *browserSession, st *panelStreamState, tabCtx context.Context) func(interface{}) {
	return func(ev interface{}) {
		switch e := ev.(type) {
		case *cdprotopage.EventScreencastFrame:
			if !panelTabIsCurrent(s, tabCtx) {
				return // stale tab — its screencast is stopped by panelRestart
			}
			if e.SessionID != 0 {
				ackCtx, cancel := context.WithTimeout(tabCtx, 2*time.Second)
				_ = cdprotopage.ScreencastFrameAck(e.SessionID).Do(ackCtx)
				cancel()
			}
			if e.Data == "" {
				return
			}
			// Throttle the sink emit; keep the ack flowing regardless.
			now := time.Now().UnixNano()
			if prev := st.lastEmit.Load(); prev != 0 && now-prev < int64(panelLiveFrameInterval) {
				return
			}
			st.lastEmit.Store(now)
			EmitBrowserPanel(BrowserPanelFrame{
				Kind:      "live",
				Source:    "tool",
				Image:     "data:image/jpeg;base64," + e.Data,
				URL:       panelCurrentURL(tabCtx),
				TabID:     string(st.targetID),
				SessionID: s.id,
			})
		case *cdprotopage.EventFrameNavigated:
			if e.Frame == nil || e.Frame.ParentID != "" {
				return // iframe — only the main frame drives the address bar
			}
			if !panelTabIsCurrent(s, tabCtx) {
				return
			}
			EmitBrowserPanel(BrowserPanelFrame{
				Kind:      "live",
				Source:    "tool",
				URL:       e.Frame.URL,
				TabID:     string(sessionTargetID(s)),
				SessionID: s.id,
				Text:      "navigating",
			})
		case *cdprotopage.EventLoadEventFired:
			if !panelTabIsCurrent(s, tabCtx) {
				return
			}
			st.lastEmit.Store(0) // let the next frame through immediately
			title, url := panelTitleAndURL(tabCtx)
			if url == "" {
				return
			}
			EmitBrowserPanel(BrowserPanelFrame{
				Kind:      "live",
				Source:    "tool",
				URL:       url,
				Title:     title,
				TabID:     string(sessionTargetID(s)),
				SessionID: s.id,
			})
		}
	}
}

// panelCurrentURL is a best-effort location probe for frame emits; failures
// are swallowed (the frame still carries the image).
func panelCurrentURL(tabCtx context.Context) string {
	_, url := panelTitleAndURL(tabCtx)
	return url
}

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
	Key        string  `json:"key,omitempty"` // named key: Enter|Backspace|Escape|...
	Text       string  `json:"text,omitempty"`
	Modifiers  int64   `json:"modifiers,omitempty"`
}

// PanelDispatchInput applies one panel input event to the session's current
// tab. Input never routes through runBrowserAction's post-action mirror —
// the live stream already shows the result.
func PanelDispatchInput(id string, ev PanelInputEvent) error {
	s, err := getBrowserSession(id)
	if err != nil {
		return err
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
		return chromedp.Run(ctx, input.InsertText(ev.Text))
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
		return chromedp.Run(ctx, panelMouseEvent("mousePressed", x, y, btn, cc, ev.Modifiers, 0, 0))
	case "up":
		return chromedp.Run(ctx, panelMouseEvent("mouseReleased", x, y, btn, cc, ev.Modifiers, 0, 0))
	case "click":
		if err := chromedp.Run(ctx, panelMouseEvent("mousePressed", x, y, btn, cc, ev.Modifiers, 0, 0)); err != nil {
			return err
		}
		return chromedp.Run(ctx, panelMouseEvent("mouseReleased", x, y, btn, cc, ev.Modifiers, 0, 0))
	default: // move (hover)
		return chromedp.Run(ctx, panelMouseEvent("mouseMoved", x, y, input.None, 0, ev.Modifiers, 0, 0))
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
func panelMouseEvent(typ string, x, y float64, btn input.MouseButton, clickCount int, modifiers int64, dx, dy float64) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		p := input.DispatchMouseEvent(input.MouseType(typ), x, y).
			WithButton(btn).
			WithClickCount(int64(clickCount)).
			WithDeltaX(dx).
			WithDeltaY(dy)
		if modifiers != 0 {
			p = p.WithModifiers(input.Modifier(modifiers))
		}
		return p.Do(ctx)
	})
}

func panelMouseWheel(x, y, dx, dy float64, modifiers int64) chromedp.Action {
	return panelMouseEvent("mouseWheel", x, y, input.None, 0, modifiers, dx, dy)
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
		// Fall back to raw text dispatch for printable keys the panel didn't
		// classify; empty text is a no-op.
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
