package builtin

// Phase-3 panel surface: downloads mirror, in-page find, and login-state
// (cookie store) export/import.
//
// Downloads ride the existing startDownloadsHandler capture — this file adds
// panel mirror frames (kind "download") and a PanelDownloads reader.
//
// Find-in-page is an injected highlighter: CDP has no find API, but a walker
// that wraps text-node matches in <mark data-hiq-find> is self-contained and
// page-agnostic (cleared before each new search / navigation).
//
// Login state: export the session's cookie store to a JSON file and import
// it back — the legitimate equivalent of snow-app's "import login state",
// which reads other browsers' DPAPI-encrypted cookie stores. That path is
// deliberately NOT implemented here: Chrome 127+ app-bound encryption broke
// it, and shipping DPAPI cookie-decryption in a distributed executable is a
// Defender-detection pattern. hiq's export/import covers "move my login
// between sessions/machines" without touching third-party browsers.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	cdpnetwork "github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// PanelDownloadPayload is the JSON carried in download mirror frames.
type PanelDownloadPayload struct {
	GUID  string `json:"guid"`
	Name  string `json:"name"`
	URL   string `json:"url,omitempty"`
	State string `json:"state"` // inProgress | completed | canceled
	Path  string `json:"path,omitempty"`
}

// PanelDownloads mirrors a session's download records for the panel.
func PanelDownloads(id string) ([]PanelDownloadPayload, error) {
	s, err := getBrowserSession(id)
	if err != nil {
		return nil, err
	}
	s.downloadMu.Lock()
	defer s.downloadMu.Unlock()
	out := make([]PanelDownloadPayload, 0, len(s.downloadRecords))
	for _, r := range s.downloadRecords {
		out = append(out, PanelDownloadPayload{
			GUID: r.GUID, Name: r.SuggestedName, URL: r.URL, State: r.State, Path: r.FilePath,
		})
	}
	return out, nil
}

// panelEmitDownload pushes one download mirror frame (best-effort).
func panelEmitDownload(s *browserSession, p PanelDownloadPayload) {
	if browserPanelSink == nil {
		return
	}
	b, _ := json.Marshal(p)
	EmitBrowserPanel(BrowserPanelFrame{
		Kind:      "download",
		Source:    "tool",
		Text:      string(b),
		SessionID: s.id,
	})
}

// panelStartDownloadsHandler wraps the existing session handler: identical
// capture, plus mirror frames. Called INSTEAD of startDownloadsHandler when
// built with the panel (single handler — no double capture).
func panelStartDownloadsHandler(s *browserSession) {
	if err := initBrowserDownloadDir(); err != nil {
		fmt.Printf("[browser] downloads handler disabled: %v\n", err)
		return
	}
	go func() {
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
		}
		chromedp.ListenBrowser(s.ctx, func(ev interface{}) {
			switch e := ev.(type) {
			case *cdpbrowser.EventDownloadWillBegin:
				s.downloadMu.Lock()
				s.downloadRecords = append(s.downloadRecords, downloadRecord{
					GUID:          e.GUID,
					URL:           e.URL,
					SuggestedName: e.SuggestedFilename,
					State:         "inProgress",
				})
				s.downloadMu.Unlock()
				panelEmitDownload(s, PanelDownloadPayload{
					GUID: e.GUID, Name: e.SuggestedFilename, URL: e.URL, State: "inProgress",
				})
			case *cdpbrowser.EventDownloadProgress:
				var state string
				switch e.State {
				case cdpbrowser.DownloadProgressStateCompleted:
					state = "completed"
				case cdpbrowser.DownloadProgressStateCanceled:
					state = "canceled"
				default:
					return
				}
				s.downloadMu.Lock()
				var name, url, path string
				for i := range s.downloadRecords {
					if s.downloadRecords[i].GUID == e.GUID {
						s.downloadRecords[i].State = state
						if e.FilePath != "" {
							s.downloadRecords[i].FilePath = e.FilePath
						}
						name = s.downloadRecords[i].SuggestedName
						url = s.downloadRecords[i].URL
						path = s.downloadRecords[i].FilePath
						break
					}
				}
				s.downloadMu.Unlock()
				panelEmitDownload(s, PanelDownloadPayload{GUID: e.GUID, Name: name, URL: url, State: state, Path: path})
			}
		})
	}()
}

// --- find in page --------------------------------------------------------------

// panelFindJS wraps matches of query in highlight marks; returns match count.
// query/limit are interpolated as JSON strings (quote-safe by construction).
func panelFindJS(query string) string {
	return `(() => {
window.__hiqFindClear && window.__hiqFindClear();
const q = ` + jsQuote(query) + `;
if (!q) return 0;
const marks = [];
const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT, {
  acceptNode: (n) => n.nodeValue && n.nodeValue.toLowerCase().includes(q.toLowerCase())
      && n.parentElement && !['SCRIPT','STYLE','MARK','NOSCRIPT'].includes(n.parentElement.tagName)
      ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT
});
const nodes = [];
let n; while ((n = walker.nextNode())) nodes.push(n);
for (const node of nodes) {
  const text = node.nodeValue;
  const lower = text.toLowerCase();
  let idx = lower.indexOf(q.toLowerCase());
  if (idx < 0) continue;
  const frag = document.createDocumentFragment();
  let pos = 0;
  while (idx >= 0) {
    frag.appendChild(document.createTextNode(text.slice(pos, idx)));
    const m = document.createElement('mark');
    m.setAttribute('data-hiq-find', '1');
    m.style.background = '#ffe066'; m.style.color = '#111';
    m.textContent = text.slice(idx, idx + q.length);
    frag.appendChild(m);
    marks.push(m);
    pos = idx + q.length;
    idx = lower.indexOf(q.toLowerCase(), pos);
  }
  frag.appendChild(document.createTextNode(text.slice(pos)));
  node.parentNode.replaceChild(frag, node);
}
window.__hiqFindMarks = marks;
window.__hiqFindClear = () => {
  for (const m of (window.__hiqFindMarks || [])) {
    const p = m.parentNode; if (!p) continue;
    p.replaceChild(document.createTextNode(m.textContent), m); p.normalize();
  }
  window.__hiqFindMarks = []; window.__hiqFindClear = undefined;
};
if (marks.length) marks[0].scrollIntoView({ block: 'center' });
return marks.length;
})()`
}

const panelFindClearJS = `(() => { window.__hiqFindClear && window.__hiqFindClear(); return 0; })()`

// jsQuote encodes v as a JSON string literal (quote-safe JS interpolation).
// (jsonString already exists for notebook templates — different return type.)
func jsQuote(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// PanelFindInPage highlights query occurrences; returns the match count.
func PanelFindInPage(id, query string) (int, error) {
	s, err := getBrowserSession(id)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	var count int
	if err := chromedp.Run(ctx, chromedp.Evaluate(panelFindJS(query), &count)); err != nil {
		return 0, err
	}
	return count, nil
}

// PanelFindClear removes the highlight.
func PanelFindClear(id string) error {
	s, err := getBrowserSession(id)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	return chromedp.Run(ctx, chromedp.Evaluate(panelFindClearJS, nil))
}

// --- login-state export / import -------------------------------------------------

// stateFile is the on-disk format of an exported cookie store.
type stateFile struct {
	Version int                  `json:"version"` // 1
	SavedAt int64                `json:"saved_at_ms"`
	URL     string               `json:"url,omitempty"`
	Cookies []*cdpnetwork.Cookie `json:"cookies"`
}

// PanelExportState writes the session's cookie store to path.
func PanelExportState(id, path string) error {
	if path == "" {
		return errors.New("empty path")
	}
	s, err := getBrowserSession(id)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	var cookies []*cdpnetwork.Cookie
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		cookies, err = cdpnetwork.GetCookies().Do(ctx)
		return err
	})); err != nil {
		return fmt.Errorf("read cookies: %w", err)
	}
	var url string
	_ = chromedp.Run(ctx, chromedp.Location(&url))
	b, err := json.MarshalIndent(stateFile{Version: 1, SavedAt: time.Now().UnixMilli(), URL: url, Cookies: cookies}, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the file carries live session credentials.
	return os.WriteFile(path, b, 0o600)
}

// PanelImportState loads a state file written by PanelExportState and sets
// its cookies on the session. Existing cookies with the same name+domain+path
// are overwritten (Chrome semantics).
func PanelImportState(id, path string) (int, error) {
	if path == "" {
		return 0, errors.New("empty path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var f stateFile
	if err := json.Unmarshal(b, &f); err != nil {
		return 0, fmt.Errorf("parse state file: %w", err)
	}
	if len(f.Cookies) == 0 {
		return 0, errors.New("state file has no cookies")
	}
	s, err := getBrowserSession(id)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	params := make([]*cdpnetwork.CookieParam, 0, len(f.Cookies))
	for _, c := range f.Cookies {
		params = append(params, &cdpnetwork.CookieParam{
			Name: c.Name, Value: c.Value,
			Domain: c.Domain, Path: c.Path,
			Secure: c.Secure, HTTPOnly: c.HTTPOnly,
			SameSite: c.SameSite,
		})
	}
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpnetwork.SetCookies(params).Do(ctx)
	})); err != nil {
		return 0, err
	}
	return len(params), nil
}
