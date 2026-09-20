// BrowserMirrorPanel — the cowork dock's 浏览器 tab: an INTERACTIVE mirror
// of the browser the agent drives (snow-app-style embedded feel, over CDP).
// Live screencast frames and lifecycle states flow kernel → Wails
// "browser:mirror" → the App-level subscription → the browserMirror module
// store, so this panel can be closed or remounted freely without losing the
// stream. The viewport forwards pointer/keyboard input back through
// BrowserPanelDispatch, so the user browses the very page instance the agent
// drives — one target, two drivers. Mounting = the dock tab is visible, so
// this panel also drives the screencast on/off flow control.
import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import {
  ArrowLeft,
  ArrowRight,
  Copy,
  Download,
  Globe,
  Loader2,
  LogIn,
  LogOut,
  MousePointer2,
  RefreshCw,
  Search,
  SendToBack,
  Star,
  X,
} from "lucide-react";
import type { BrowserBookmark } from "../../lib/types";
import {
  browserMirrorSnapshot,
  subscribeBrowserMirror,
  type MirrorSessionFrame,
} from "../../lib/browserMirror";
import { app } from "../../lib/bridge";
import { useT } from "../../lib/i18n";
import type { BrowserPanelInput, BrowserPanelTab } from "../../lib/types";

// latestSessionFrame picks the most recently updated session — the one the
// agent (or the user) is driving right now.
function latestSessionFrame(
  sessions: Record<string, MirrorSessionFrame>,
): { id: string; frame: MirrorSessionFrame } | null {
  let best: { id: string; frame: MirrorSessionFrame } | null = null;
  for (const [id, frame] of Object.entries(sessions)) {
    if (!best || frame.at > best.frame.at) best = { id, frame };
  }
  return best;
}

export function BrowserMirrorPanel() {
  const t = useT();
  const s = useSyncExternalStore(subscribeBrowserMirror, browserMirrorSnapshot);
  const live = latestSessionFrame(s.sessions);
  const sessionID = live?.id ?? "";
  const frame = live?.frame ?? null;

  // Toolbar + tab strip state.
  const [address, setAddress] = useState("");
  const [tabs, setTabs] = useState<BrowserPanelTab[]>([]);
  const [loading, setLoading] = useState(false);
  const [picking, setPicking] = useState(false);
  const [findOpen, setFindOpen] = useState(false);
  const [findQuery, setFindQuery] = useState("");
  const [findCount, setFindCount] = useState<number | null>(null);
  const [bookmarks, setBookmarks] = useState<BrowserBookmark[]>([]);
  const downloads = s.downloads;
  const viewportRef = useRef<HTMLDivElement>(null);
  const lastMoveRef = useRef(0);

  // Mounting = the dock tab is visible → stream on; unmount → stream off.
  useEffect(() => {
    void app?.BrowserPanelSetVisible(true);
    return () => {
      void app?.BrowserPanelSetVisible(false);
    };
  }, []);

  // Bookmarks load once per session; toggles refresh the list.
  useEffect(() => {
    app?.BrowserBookmarksList()
      .then(setBookmarks)
      .catch(() => {});
  }, [sessionID]);

  // Track the address bar from the stream; refresh the tab strip when the
  // session or its active tab changes.
  useEffect(() => {
    if (frame) setAddress(frame.url);
  }, [frame?.url]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!sessionID) return;
    let cancelled = false;
    app?.BrowserPanelTabs(sessionID)
      .then((ts) => {
        if (!cancelled) setTabs(ts ?? []);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [sessionID, frame?.tabId]); // eslint-disable-line react-hooks/exhaustive-deps

  const guard = useCallback(
    (fn: () => Promise<void>) => {
      if (!sessionID) return;
      setLoading(true);
      fn()
        .catch(() => {})
        .finally(() => setLoading(false));
    },
    [sessionID],
  );

  const navigate = useCallback(
    (url: string) => {
      const v = url.trim();
      if (!v) return;
      guard(() => app!.BrowserPanelNavigate(sessionID, v));
    },
    [guard, sessionID],
  );

  // Coordinate normalization: pointer position → 0..1 within the frame image.
  const norm = useCallback((e: { clientX: number; clientY: number }) => {
    const el = viewportRef.current?.querySelector("img");
    if (!el) return null;
    const r = el.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return null;
    return {
      x: Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)),
      y: Math.min(1, Math.max(0, (e.clientY - r.top) / r.height)),
    };
  }, []);

  const dispatch = useCallback(
    (ev: BrowserPanelInput) => {
      if (!sessionID || !app) return;
      void app.BrowserPanelDispatch(sessionID, ev).catch(() => {});
    },
    [sessionID],
  );

  // Element picker: arm/cancel on the session; the picked element arrives as
  // a "picked" frame through the store (chip below).
  const togglePick = useCallback(() => {
    if (!sessionID || !app) return;
    if (picking) {
      void app.BrowserPanelStopPick(sessionID).catch(() => {});
      setPicking(false);
    } else {
      void app.BrowserPanelStartPick(sessionID)
        .then(() => setPicking(true))
        .catch(() => {});
    }
  }, [picking, sessionID]);

  const bookmarked = !!frame?.url && bookmarks.some((b) => b.url === frame.url);

  const toggleBookmark = useCallback(() => {
    const url = frame?.url;
    if (!url || !app) return;
    void app
      .BrowserBookmarkToggle(url, frame?.title || url)
      .then(() => app.BrowserBookmarksList())
      .then(setBookmarks)
      .catch(() => {});
  }, [frame?.url, frame?.title]); // eslint-disable-line react-hooks/exhaustive-deps

  const runFind = useCallback(
    (q: string) => {
      setFindQuery(q);
      if (!q) {
        setFindCount(null);
        void app?.BrowserPanelFindClear(sessionID).catch(() => {});
        return;
      }
      void app?.BrowserPanelFindInPage(sessionID, q)
        .then((n) => setFindCount(n))
        .catch(() => {});
    },
    [sessionID],
  );

  if (!frame) {
    return (
      <div className="browser-mirror browser-mirror--empty">
        <Globe size={22} />
        <p>{t("browserMirror.emptyHint")}</p>
      </div>
    );
  }

  const btn = (ev: React.PointerEvent): "left" | "middle" | "right" =>
    ev.button === 1 ? "middle" : ev.button === 2 ? "right" : "left";

  return (
    <div className="browser-mirror browser-mirror--live">
      {/* Tab strip */}
      {tabs.length > 0 && (
        <div className="browser-mirror__tabs">
          {tabs.map((tab) => (
            <button
              key={tab.id}
              className={`browser-mirror__tab${tab.active ? " browser-mirror__tab--active" : ""}`}
              title={tab.url}
              onClick={() => guard(() => app!.BrowserPanelSwitchTab(sessionID, tab.id))}
            >
              <span className="browser-mirror__tab-title">{tab.title || tab.url}</span>
            </button>
          ))}
        </div>
      )}
      {/* Toolbar: back / forward / reload / address */}
      <div className="browser-mirror__toolbar">
        <button
          className="browser-mirror__tool"
          title={t("browserMirror.back")}
          disabled={loading}
          onClick={() => guard(() => app!.BrowserPanelBack(sessionID))}
        >
          <ArrowLeft size={15} />
        </button>
        <button
          className="browser-mirror__tool"
          title={t("browserMirror.forward")}
          disabled={loading}
          onClick={() => guard(() => app!.BrowserPanelForward(sessionID))}
        >
          <ArrowRight size={15} />
        </button>
        <button
          className="browser-mirror__tool"
          title={t("browserMirror.reload")}
          disabled={loading}
          onClick={() => guard(() => app!.BrowserPanelReload(sessionID))}
        >
          {loading ? <Loader2 size={15} className="composer-phase__spin" /> : <RefreshCw size={15} />}
        </button>
        <button
          className={`browser-mirror__tool${picking ? " browser-mirror__tool--active" : ""}`}
          title={t("browserMirror.pick")}
          disabled={loading}
          onClick={togglePick}
        >
          <MousePointer2 size={15} />
        </button>
        <input
          className="browser-mirror__address"
          value={address}
          spellCheck={false}
          onChange={(e) => setAddress(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              (e.target as HTMLInputElement).blur();
              navigate(address);
            }
          }}
          placeholder={t("browserMirror.addressHint")}
        />
        <button
          className={`browser-mirror__tool${bookmarked ? " browser-mirror__tool--active" : ""}`}
          title={t("browserMirror.bookmark")}
          onClick={toggleBookmark}
        >
          <Star size={15} />
        </button>
        <button
          className={`browser-mirror__tool${findOpen ? " browser-mirror__tool--active" : ""}`}
          title={t("browserMirror.find")}
          onClick={() => {
            setFindOpen((v) => !v);
            if (findOpen) runFind("");
          }}
        >
          <Search size={15} />
        </button>
        <span
          className={`browser-mirror__dot${loading ? " browser-mirror__dot--live" : ""}`}
          aria-hidden="true"
        />
      </div>
      {/* Bookmarks bar */}
      {bookmarks.length > 0 && (
        <div className="browser-mirror__bookmarks">
          {bookmarks.map((b) => (
            <button
              key={b.id}
              className="browser-mirror__bookmark"
              title={b.url}
              onClick={() => navigate(b.url)}
            >
              {b.title || b.url}
            </button>
          ))}
        </div>
      )}
      {/* Find bar */}
      {findOpen && (
        <div className="browser-mirror__findrow">
          <input
            className="browser-mirror__address"
            autoFocus
            value={findQuery}
            spellCheck={false}
            onChange={(e) => runFind(e.target.value)}
            placeholder={t("browserMirror.findPlaceholder")}
          />
          {findCount !== null && <span className="browser-mirror__findcount">{findCount}</span>}
          <button
            className="browser-mirror__tool"
            title={t("browserMirror.findClose")}
            onClick={() => {
              setFindOpen(false);
              runFind("");
            }}
          >
            <X size={14} />
          </button>
        </div>
      )}
      {/* Interactive viewport: pointer + wheel + keyboard forwarded via CDP */}
      <div
        ref={viewportRef}
        className="browser-mirror__viewport browser-mirror__viewport--live"
        tabIndex={0}
        onContextMenu={(e) => e.preventDefault()}
        onPointerDown={(e) => {
          const p = norm(e);
          if (!p) return;
          e.currentTarget.focus();
          dispatch({ type: "down", x: p.x, y: p.y, button: btn(e), clickCount: 1 });
        }}
        onPointerUp={(e) => {
          const p = norm(e);
          if (!p) return;
          dispatch({ type: "up", x: p.x, y: p.y, button: btn(e), clickCount: 1 });
        }}
        onDoubleClick={(e) => {
          const p = norm(e);
          if (!p) return;
          dispatch({ type: "click", x: p.x, y: p.y, clickCount: 2 });
        }}
        onPointerMove={(e) => {
          const now = Date.now();
          if (now - lastMoveRef.current < 60) return; // ~16 hover updates/s
          lastMoveRef.current = now;
          const p = norm(e);
          if (!p) return;
          dispatch({ type: "move", x: p.x, y: p.y });
        }}
        onWheel={(e) => {
          const p = norm(e);
          if (!p) return;
          e.preventDefault();
          dispatch({ type: "wheel", x: p.x, y: p.y, deltaX: e.deltaX, deltaY: e.deltaY });
        }}
        onKeyDown={(e) => {
          if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
            e.preventDefault();
            dispatch({ type: "text", text: e.key });
            return;
          }
          const named = [
            "Enter",
            "Backspace",
            "Delete",
            "Tab",
            "Escape",
            "ArrowUp",
            "ArrowDown",
            "ArrowLeft",
            "ArrowRight",
            "Home",
            "End",
            "PageUp",
            "PageDown",
          ];
          if (named.includes(e.key)) {
            e.preventDefault();
            const mods =
              (e.shiftKey ? 8 : 0) | (e.ctrlKey ? 2 : 0) | (e.altKey ? 1 : 0) | (e.metaKey ? 4 : 0);
            dispatch({ type: "key", key: e.key, modifiers: mods });
          }
        }}
      >
        <img className="browser-mirror__img" src={frame.image} alt={frame.title || ""} draggable={false} />
      </div>
      {/* Downloads + login-state row */}
      {(downloads.length > 0 || sessionID) && (
        <div className="browser-mirror__meta">
          {downloads.slice(0, 3).map((d) => (
            <span key={d.guid} className="browser-mirror__dl" title={d.path || d.url}>
              <Download size={13} />
              <span className="browser-mirror__dl-name">{d.name}</span>
              <span className={`browser-mirror__dl-state browser-mirror__dl-state--${d.state}`}>
                {t(`browserMirror.dl_${d.state}`)}
              </span>
            </span>
          ))}
          <span className="browser-mirror__meta-actions">
            <button
              className="browser-mirror__tool"
              title={t("browserMirror.exportState")}
              onClick={() => void app?.BrowserPanelExportState(sessionID).catch(() => {})}
            >
              <LogOut size={14} />
            </button>
            <button
              className="browser-mirror__tool"
              title={t("browserMirror.importState")}
              onClick={() =>
                void app
                  ?.BrowserPanelImportState(sessionID)
                  .then((n) => {
                    if (n > 0) void app?.BrowserPanelReload(sessionID).catch(() => {});
                  })
                  .catch(() => {})
              }
            >
              <LogIn size={14} />
            </button>
          </span>
        </div>
      )}
      {/* Picked element chip: insert a readable description into the chat
          input (existing cowork:insert-text channel) or copy the selector. */}
      {s.picked && (
        <div className="browser-mirror__chip">
          <span className="browser-mirror__chip-text" title={s.picked.selector}>
            <code>{s.picked.tag}</code>
            {s.picked.text ? ` “${s.picked.text.slice(0, 40)}”` : ""}
            {" · "}
            <span className="browser-mirror__chip-sel">{s.picked.selector}</span>
          </span>
          <button
            className="browser-mirror__tool"
            title={t("browserMirror.insertChat")}
            onClick={() => {
              const d = s.picked!;
              const desc = `${d.tag}${d.text ? ` “${d.text}”` : ""}（选择器 ${d.selector}）`;
              window.dispatchEvent(new CustomEvent("cowork:insert-text", { detail: desc }));
            }}
          >
            <SendToBack size={14} />
          </button>
          <button
            className="browser-mirror__tool"
            title={t("browserMirror.copySelector")}
            onClick={() => void navigator.clipboard.writeText(s.picked!.selector).catch(() => {})}
          >
            <Copy size={14} />
          </button>
        </div>
      )}
      {(frame.title || frame.url) && (
        <div className="browser-mirror__text" title={frame.url}>
          {frame.title || frame.url}
        </div>
      )}
    </div>
  );
}
