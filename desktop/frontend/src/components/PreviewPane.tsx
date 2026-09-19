// PreviewPane — the right dock's "预览" tab (pane-system spec §3.2).
// Renders a local dev-server page in an iframe: zero native code, identical on
// WebView2 / WKWebView / WebKitGTK. The URL is normally auto-detected from the
// agent's tool output (App.tsx) and may also be entered manually; back/forward
// navigate the pane-local history; the managed-browser button hands the URL to
// the attachable Chrome (companion tier, §3.6).
import { useEffect, useState } from "react";
import { ArrowLeft, ArrowRight, ExternalLink, Globe, Loader2, MonitorPlay, RefreshCw } from "lucide-react";
import { app } from "../lib/bridge";
import { useT } from "../lib/i18n";

// looksLikeFilePath reports whether the input is a filesystem path (Windows
// drive-letter or POSIX absolute) rather than a URL — those go through the
// backend's loopback preview server, because an iframe cannot navigate file://
// and prefixing `http://` onto `D:\...` (the old behavior) is a dead address.
function looksLikeFilePath(raw: string): boolean {
  const t = raw.trim();
  if (!t || /^(https?:|about:|data:|file:)/i.test(t)) return false;
  return /^[A-Za-z]:[\\/]/.test(t) || t.startsWith("/");
}

// commitInput resolves any address the pane accepts — http(s) URL, bare host,
// or a local file path — into an iframe-navigable URL.
async function resolveInput(raw: string): Promise<string | null> {
  const trimmed = raw.trim();
  if (!trimmed) return null;
  if (/^https?:\/\//i.test(trimmed)) return trimmed;
  if (looksLikeFilePath(trimmed)) {
    try {
      return await app.PreviewLocalFile(trimmed);
    } catch {
      return null; // keep the pane on its current content; the reason is a path problem, not a URL
    }
  }
  return `http://${trimmed}`;
}

// Navigation history, cached in-module so it survives the pane unmounting when
// the dock closes (same pattern as TerminalPanel's terminal cache).
let historyStack: string[] = [];
let historyIndex = -1;

function pushHistory(url: string) {
  if (historyStack[historyIndex] === url) return;
  historyStack = [...historyStack.slice(0, historyIndex + 1), url];
  if (historyStack.length > 30) historyStack = historyStack.slice(historyStack.length - 30);
  historyIndex = historyStack.length - 1;
}

export function PreviewPane({ url, onUrlCommit }: { url: string; onUrlCommit?: (url: string) => void }) {
  const t = useT();
  // `current` is the pane-local URL: it follows the App-detected url but can
  // also move within the history stack without touching App state.
  const [current, setCurrent] = useState(() => {
    if (url) pushHistory(url);
    return url;
  });
  const [draft, setDraft] = useState(url);
  const [nonce, setNonce] = useState(0);
  const [managedBusy, setManagedBusy] = useState(false);

  useEffect(() => {
    if (url && url !== current) {
      pushHistory(url);
      setCurrent(url);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [url]);

  useEffect(() => {
    setDraft(current);
  }, [current]);

  const commit = (raw: string) => {
    void resolveInput(raw).then((next) => {
      if (!next) return;
      pushHistory(next);
      setCurrent(next);
      setNonce((v) => v + 1);
      onUrlCommit?.(next);
    });
  };

  const step = (delta: -1 | 1) => {
    const nextIndex = historyIndex + delta;
    if (nextIndex < 0 || nextIndex >= historyStack.length) return;
    historyIndex = nextIndex;
    setCurrent(historyStack[historyIndex]);
    setNonce((v) => v + 1);
  };

  if (!current) {
    return (
      <div className="preview-pane preview-pane--empty">
        <Globe size={22} />
        <div className="preview-pane__hint">{t("preview.emptyHint")}</div>
        <form
          className="preview-pane__form"
          onSubmit={(e) => {
            e.preventDefault();
            commit(draft);
          }}
        >
          <input
            className="preview-pane__input"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder={t("preview.addressPlaceholder")}
            spellCheck={false}
            autoComplete="off"
            aria-label={t("preview.addressPlaceholder")}
          />
          <button type="submit" className="btn btn--small">{t("preview.manualOpen")}</button>
        </form>
      </div>
    );
  }

  return (
    <div className="preview-pane">
      <div className="preview-pane__toolbar">
        <button
          type="button"
          className="preview-pane__btn"
          onClick={() => step(-1)}
          disabled={historyIndex <= 0}
          aria-label={t("preview.back")}
          title={t("preview.back")}
        >
          <ArrowLeft size={12} />
        </button>
        <button
          type="button"
          className="preview-pane__btn"
          onClick={() => step(1)}
          disabled={historyIndex >= historyStack.length - 1}
          aria-label={t("preview.forward")}
          title={t("preview.forward")}
        >
          <ArrowRight size={12} />
        </button>
        <button
          type="button"
          className="preview-pane__btn"
          onClick={() => setNonce((v) => v + 1)}
          aria-label={t("preview.refresh")}
          title={t("preview.refresh")}
        >
          <RefreshCw size={12} />
        </button>
        <form
          className="preview-pane__addressbar"
          onSubmit={(e) => {
            e.preventDefault();
            commit(draft);
          }}
        >
          <input
            className="preview-pane__address"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            spellCheck={false}
            autoComplete="off"
            aria-label={t("preview.addressPlaceholder")}
          />
        </form>
        <button
          type="button"
          className="preview-pane__btn preview-pane__btn--managed"
          onClick={() => {
            if (managedBusy) return;
            setManagedBusy(true);
            void app.OpenURLInManagedBrowser(current)
              .catch(() => { /* surfaced by the settings panel's browser state */ })
              .finally(() => setManagedBusy(false));
          }}
          disabled={managedBusy}
          aria-label={t("preview.openManaged")}
          title={t("preview.openManaged")}
        >
          {managedBusy ? <Loader2 size={12} className="composer-phase__spin" /> : <MonitorPlay size={12} />}
        </button>
        <button
          type="button"
          className="preview-pane__btn"
          onClick={() => window.open(current, "_blank", "noopener")}
          aria-label={t("preview.openExternal")}
          title={t("preview.openExternal")}
        >
          <ExternalLink size={12} />
        </button>
      </div>
      <div className="preview-pane__viewport">
        <iframe
          key={nonce}
          className="preview-pane__frame"
          src={current}
          title={t("preview.tabTitle")}
        />
      </div>
    </div>
  );
}
