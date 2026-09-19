// PreviewPane — the right dock's "预览" tab (pane-system spec §3.2).
// Renders a local dev-server page in an iframe: zero native code, identical on
// WebView2 / WKWebView / WebKitGTK. The URL is normally auto-detected from the
// agent's tool output (App.tsx) and may also be entered manually; back/forward
// navigate the pane-local history; the managed-browser button hands the URL to
// the attachable Chrome (companion tier, §3.6).
//
// Zoom-to-fit: generated pages are often authored at a fixed width (game
// canvases, landing pages) that overflows the narrow dock and demands
// horizontal scrolling. Loopback-served HTML (desktop/preview_server.go)
// injects a size-report script, so the pane learns the content box and scales
// the iframe — 适应宽度 / 适应窗口 / 实际大小, fit-width by default.
import { useCallback, useEffect, useState } from "react";
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

type ZoomMode = "fit-width" | "fit-page" | "actual";

// Navigation history + zoom preference, cached in-module so they survive the
// pane unmounting when the dock closes (same pattern as TerminalPanel's cache).
let historyStack: string[] = [];
let historyIndex = -1;
let zoomPref: ZoomMode = "fit-width";

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
  const [zoom, setZoom] = useState<ZoomMode>(zoomPref);
  // Content box reported by the injected fit script (loopback pages only).
  const [contentSize, setContentSize] = useState<{ w: number; h: number } | null>(null);
  // The viewport's visible box, measured so fit modes can compute a scale.
  const [viewportEl, setViewportEl] = useState<HTMLDivElement | null>(null);
  const [viewportBox, setViewportBox] = useState({ w: 0, h: 0 });
  const viewportRef = useCallback((el: HTMLDivElement | null) => setViewportEl(el), []);

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

  // New navigation: drop the previous page's reported size until the fresh
  // document reports its own.
  useEffect(() => {
    setContentSize(null);
  }, [current, nonce]);

  // Fit reports come from loopback-served HTML via parent.postMessage.
  useEffect(() => {
    const onMsg = (e: MessageEvent) => {
      const d = e.data as { __hiqPreviewSize?: boolean; w?: number; h?: number } | null;
      if (d && d.__hiqPreviewSize && typeof d.w === "number" && typeof d.h === "number" && d.w > 0 && d.h > 0) {
        setContentSize({ w: d.w, h: d.h });
      }
    };
    window.addEventListener("message", onMsg);
    return () => window.removeEventListener("message", onMsg);
  }, []);

  // Track the viewport's visible box for scale computation.
  useEffect(() => {
    if (!viewportEl) return;
    const update = () => setViewportBox({ w: viewportEl.clientWidth, h: viewportEl.clientHeight });
    update();
    const ro = new ResizeObserver(update);
    ro.observe(viewportEl);
    return () => ro.disconnect();
  }, [viewportEl]);

  const changeZoom = (mode: ZoomMode) => {
    zoomPref = mode;
    setZoom(mode);
  };

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

  // Scale the iframe only when the page reported a size and a fit mode is on.
  // Never upscale past 1× — small pages stay pixel-crisp and centered CSS
  // keeps working; fitting is about taming oversized content.
  let scale = 1;
  const hasReport = !!contentSize && contentSize.w > 0 && contentSize.h > 0 && viewportBox.w > 0;
  if (hasReport && zoom !== "actual") {
    const byW = viewportBox.w / contentSize!.w;
    scale = zoom === "fit-page" ? Math.min(byW, viewportBox.h / contentSize!.h) : byW;
    scale = Math.min(1, Math.max(0.05, scale));
  }
  const frameStyle: React.CSSProperties = hasReport
    ? {
        width: contentSize!.w,
        height: contentSize!.h,
        transform: scale < 1 ? `scale(${scale})` : undefined,
        transformOrigin: "0 0",
        flex: "0 0 auto",
      }
    : {};
  const wrapperStyle: React.CSSProperties = hasReport
    ? { width: Math.round(contentSize!.w * scale), height: Math.round(contentSize!.h * scale), overflow: "hidden", flex: "0 0 auto", margin: "0 auto" }
    : {};

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
        <select
          className="preview-pane__zoom"
          value={zoom}
          onChange={(e) => changeZoom(e.target.value as ZoomMode)}
          aria-label={t("preview.zoomTitle")}
          title={t("preview.zoomTitle")}
        >
          <option value="fit-width">{t("preview.fitWidth")}</option>
          <option value="fit-page">{t("preview.fitPage")}</option>
          <option value="actual">{t("preview.actualSize")}</option>
        </select>
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
      <div className="preview-pane__viewport" ref={viewportRef}>
        <div style={wrapperStyle}>
          <iframe
            key={nonce}
            className="preview-pane__frame"
            style={frameStyle}
            src={current}
            title={t("preview.tabTitle")}
          />
        </div>
      </div>
    </div>
  );
}
