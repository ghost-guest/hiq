package builtin

// Element picker for the interactive browser panel (snow-app parity, CDP
// flavor): the panel toggles picking; a JS overlay highlights the hovered
// element and its click (capture phase) reports a descriptor back through a
// Runtime.addBinding channel. The descriptor lands both in the session
// (agent-visible) and on the panel sink (kind "picked"), where the frontend
// turns it into a chat-input chip — "select in page, then ask about it".

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// PickDescriptor describes one picked element, compact enough for a chat chip
// and unambiguous enough for the agent to re-locate it via browser tools.
type PickDescriptor struct {
	Selector string  `json:"selector"` // CSS path (id-fast, nth-of-type fallback)
	Tag      string  `json:"tag"`
	ID       string  `json:"id,omitempty"`
	Classes  string  `json:"classes,omitempty"`
	Text     string  `json:"text,omitempty"` // trimmed inner text (≤120 chars)
	Href     string  `json:"href,omitempty"`
	Value    string  `json:"value,omitempty"` // input value when applicable
	X, Y     float64 `json:"x,omitempty"`     // viewport rect (CSS px)
	W, H     float64 `json:"w,omitempty"`
}

// pickState is the per-session picker bookkeeping (lazy, like panelStreamState).
// armed/listenCtx are guarded by mu; the handler reads them under the same
// lock (short hold, no CDP I/O), so cross-goroutine visibility is explicit.
type pickState struct {
	mu        sync.Mutex
	armed     bool            // picker script injected, awaiting a pick
	listenCtx context.Context // context the binding listener is attached to
	result    atomic.Pointer[PickDescriptor]
}

// panelPickJS is injected on arm. It highlights on mousemove, captures the
// click in the capture phase (before page handlers), and reports through the
// __hiqPickElement binding. Escape cancels. Idempotent while active.
const panelPickJS = `(() => {
if (window.__hiqPickActive) return "already";
window.__hiqPickActive = true;
const hl = document.createElement('div');
hl.style.cssText = 'position:fixed;pointer-events:none;z-index:2147483647;border:2px solid #4f8ef7;background:rgba(79,142,247,.15);border-radius:2px;transition:all .05s';
document.documentElement.appendChild(hl);
const cleanup = () => {
  window.removeEventListener('mousemove', onMove, true);
  window.removeEventListener('click', onClick, true);
  window.removeEventListener('keydown', onKey, true);
  hl.remove();
  window.__hiqPickActive = false;
  window.__hiqPickCleanup = undefined;
};
window.__hiqPickCleanup = cleanup;
const visibleText = (el) => (el.innerText || el.value || '').replace(/\s+/g, ' ').trim().slice(0, 120);
const cssPath = (el) => {
  if (el.id) return '#' + CSS.escape(el.id);
  const parts = [];
  for (let n = el; n && n.nodeType === 1 && parts.length < 5; n = n.parentElement) {
    let part = n.tagName.toLowerCase();
    if (n.id) { parts.unshift('#' + CSS.escape(n.id)); break; }
    const parent = n.parentElement;
    if (parent) {
      const same = Array.from(parent.children).filter(c => c.tagName === n.tagName);
      if (same.length > 1) part += ':nth-of-type(' + (same.indexOf(n) + 1) + ')';
    }
    parts.unshift(part);
  }
  return parts.join(' > ');
};
const describe = (el) => {
  const r = el.getBoundingClientRect();
  const d = {
    selector: cssPath(el),
    tag: el.tagName.toLowerCase(),
    id: el.id || undefined,
    classes: (el.className && typeof el.className === 'string') ? el.className.slice(0, 120) : undefined,
    text: visibleText(el),
    href: el.href || undefined,
    value: (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') ? String(el.value || '').slice(0, 120) : undefined,
    x: r.x, y: r.y, w: r.width, h: r.height
  };
  for (const k of Object.keys(d)) if (d[k] === undefined || d[k] === '') delete d[k];
  return d;
};
const target = (e) => {
  const el = document.elementFromPoint(e.clientX, e.clientY);
  return el && hl !== el ? el : null;
};
const onMove = (e) => {
  const el = target(e);
  if (!el) return;
  const r = el.getBoundingClientRect();
  hl.style.left = r.x + 'px'; hl.style.top = r.y + 'px';
  hl.style.width = r.width + 'px'; hl.style.height = r.height + 'px';
};
const onClick = (e) => {
  e.preventDefault(); e.stopPropagation();
  const el = target(e);
  cleanup();
  if (el) window.__hiqPickElement(JSON.stringify(describe(el)));
};
const onKey = (e) => { if (e.key === 'Escape') { e.preventDefault(); window.__hiqPickStopped = true; cleanup(); } };
window.addEventListener('mousemove', onMove, true);
window.addEventListener('click', onClick, true);
window.addEventListener('keydown', onKey, true);
return "started";
})()`

// panelPickStoppedJS cancels an active picker from the kernel side.
const panelPickStoppedJS = `(() => { if (window.__hiqPickCleanup) { window.__hiqPickCleanup(); return "stopped"; } return "idle"; })()`

// pickStateFor lazily creates the session's pick state and installs the
// binding-called listener once.
func pickStateFor(s *browserSession) *pickState {
	st := s.pickState.Load()
	if st == nil {
		st = &pickState{}
		if !s.pickState.CompareAndSwap(nil, st) {
			st = s.pickState.Load()
		}
	}
	return st
}

// PanelStartPick arms the element picker on the session's current tab.
func PanelStartPick(id string) error {
	s, err := getBrowserSession(id)
	if err != nil {
		return err
	}
	if s.ctx.Err() != nil {
		return fmt.Errorf("browser session closed: %w", s.ctx.Err())
	}
	st := pickStateFor(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	// Tab switches create a fresh chromedp context; the binding listener must
	// ride the CURRENT one (re-install after a switch, never duplicated).
	if st.listenCtx != s.ctx {
		chromedp.ListenTarget(s.ctx, pickHandler(s))
		st.listenCtx = s.ctx
	}
	if st.armed {
		return nil
	}
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		cdpruntime.AddBinding("__hiqPickElement"),
		chromedp.Evaluate(panelPickJS, nil),
	); err != nil {
		return fmt.Errorf("arm picker: %w", err)
	}
	st.armed = true
	st.result.Store(nil)
	return nil
}

// PanelStopPick cancels an armed picker (panel toggled off / user done).
func PanelStopPick(id string) error {
	s, err := getBrowserSession(id)
	if err != nil {
		return err
	}
	st := pickStateFor(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.armed {
		return nil
	}
	st.armed = false
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	_ = chromedp.Run(ctx, chromedp.Evaluate(panelPickStoppedJS, nil))
	return nil
}

// PanelTakePick returns and clears the latest picked element (if any).
func PanelTakePick(id string) (*PickDescriptor, error) {
	s, err := getBrowserSession(id)
	if err != nil {
		return nil, err
	}
	st := pickStateFor(s)
	return st.result.Swap(nil), nil
}

// pickHandler builds the binding-called handler: a picked element is stored
// on the session and pushed to the panel sink (kind "picked") where the
// frontend turns it into a chat-input chip.
func pickHandler(s *browserSession) func(interface{}) {
	return func(ev interface{}) {
		e, ok := ev.(*cdpruntime.EventBindingCalled)
		if !ok || e.Name != "__hiqPickElement" {
			return
		}
		st := pickStateFor(s)
		st.mu.Lock()
		armed := st.armed
		st.armed = false
		st.mu.Unlock()
		if !armed {
			return // stray call after a cancel/re-arm cycle
		}
		var d PickDescriptor
		if err := json.Unmarshal([]byte(e.Payload), &d); err != nil {
			return
		}
		st.result.Store(&d)
		EmitBrowserPanel(BrowserPanelFrame{
			Kind:      "picked",
			Source:    "tool",
			Text:      e.Payload,
			TabID:     string(sessionTargetID(s)),
			SessionID: s.id,
		})
	}
}
