import { useCallback, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { ChevronLeft, ChevronRight, FileText, Folder, FolderOpen, FolderSearch, Image as ImageIcon, Loader2, X, ZoomIn, ZoomOut } from "lucide-react";
import { Markdown } from "./Markdown";
import { app } from "../lib/bridge";
import { useT } from "../lib/i18n";
import { useToast } from "../lib/toast";
import type { FilePreview } from "../lib/types";

// AttachmentViewer is the lightbox behind every chat attachment. Message chips,
// tool-card images, markdown images and composer chips all call
// openAttachmentViewer(); the layer mounted once in App renders the modal, so
// callers don't each need their own overlay plumbing.
//
// Images resolve through AttachmentDataURL (data URL) or ReadFile (media token
// URL for workspace files); other files preview as text or PDF when the kernel
// can serve one, and everything always exposes "open with system app" /
// "show in file manager" via the existing Go bindings.
//
// Navigation: when the opener knows the file has siblings in the same
// conversation (a message with several pictures, a tool result with many
// images), it hands over the full `siblings` list and the lightbox gains a
// ‹ / › affordance — plus Left/Right keys and drag-to-pan / +/-/0 zoom for
// images — so a burst of generated images can be browsed without closing.

export interface ViewerTarget {
  path: string;
  name: string;
  kind: "image" | "file" | "folder";
  source: "attachment" | "workspace";
  /** Already-known data URL (composer chips keep their preview in memory). */
  previewUrl?: string;
  /** Peer media in the same conversation; enables ‹ › switching when >1. */
  siblings?: ViewerTarget[];
}

let current: ViewerTarget | null = null;
const listeners = new Set<() => void>();

// normalizePeers dedupes siblings by path and strips their own sibling lists,
// so the stored target never nests the peer set more than one level deep — a
// peer opened via ‹ › carries the same flat list and reopens the same way.
function normalizePeers(peers: ViewerTarget[]): ViewerTarget[] {
  const seen = new Set<string>();
  const out: ViewerTarget[] = [];
  for (const p of peers) {
    if (!p || seen.has(p.path)) continue;
    seen.add(p.path);
    out.push({ path: p.path, name: p.name, kind: p.kind, source: p.source, previewUrl: p.previewUrl });
  }
  return out;
}

export function openAttachmentViewer(target: ViewerTarget): void {
  let peers = target.siblings && target.siblings.length > 0 ? normalizePeers(target.siblings) : [];
  // Guarantee the opened file is in the set so index maths is total even when
  // the caller's sibling list was trimmed.
  if (peers.length > 0 && !peers.some((p) => p.path === target.path)) {
    peers = [{ path: target.path, name: target.name, kind: target.kind, source: target.source, previewUrl: target.previewUrl }, ...peers];
  }
  current = { ...target, siblings: peers.length > 1 ? peers : undefined };
  listeners.forEach((l) => l());
}

export function closeAttachmentViewer(): void {
  current = null;
  listeners.forEach((l) => l());
}

function useViewerTarget(): ViewerTarget | null {
  const [target, setTarget] = useState(current);
  useEffect(() => {
    const listener = () => setTarget(current);
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }, []);
  return target;
}

type ImageState =
  | { status: "loading" }
  | { status: "ready"; url: string }
  | { status: "error" };

const MIN_ZOOM = 1;
const MAX_ZOOM = 4;
const ZOOM_STEP = 0.5;

function clampZoom(z: number): number {
  return Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, Math.round(z * 100) / 100));
}

// ImageBody resolves the full-size image and turns the stage into a pannable
// viewport: at zoom > 1 the picture overflows the stage (overflow: auto) and
// pointer drag scrolls it, exactly the gesture users expect from a map-like
// canvas. Zoom itself is owned by the parent so the keyboard handler and the
// on-screen buttons drive one source of truth.
function ImageBody({
  target,
  zoom,
  onMeta,
}: {
  target: ViewerTarget;
  zoom: number;
  onMeta?: (w: number, h: number) => void;
}) {
  const [state, setState] = useState<ImageState>({ status: "loading" });
  const [panning, setPanning] = useState(false);
  const stageRef = useRef<HTMLDivElement | null>(null);
  const panRef = useRef<{ x: number; y: number; left: number; top: number; active: boolean } | null>(null);
  const key = `${target.source}\n${target.path}\n${target.previewUrl ?? ""}`;

  useEffect(() => {
    let cancelled = false;
    setState({ status: "loading" });
    (async () => {
      let url = target.previewUrl ?? "";
      if (!url && target.source === "attachment") {
        try {
          url = await app.AttachmentDataURL(target.path);
        } catch {
          url = "";
        }
      }
      if (!url) {
        try {
          const preview = await app.ReadFile(target.path);
          if (preview.kind === "image" && preview.url) url = preview.url;
        } catch {
          url = "";
        }
      }
      if (!cancelled) setState(url ? { status: "ready", url } : { status: "error" });
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  // Recentre whenever the shown file changes, and when zoom returns to 1: a
  // fresh image always opens centred instead of inheriting the last pan.
  const recentre = useCallback(() => {
    const el = stageRef.current;
    if (!el) return;
    el.scrollLeft = (el.scrollWidth - el.clientWidth) / 2;
    el.scrollTop = (el.scrollHeight - el.clientHeight) / 2;
  }, []);

  useEffect(() => {
    const id = requestAnimationFrame(recentre);
    return () => cancelAnimationFrame(id);
  }, [key, zoom === MIN_ZOOM, recentre]);

  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    const el = stageRef.current;
    if (!el || zoom <= MIN_ZOOM) return;
    panRef.current = { x: e.clientX, y: e.clientY, left: el.scrollLeft, top: el.scrollTop, active: true };
    el.setPointerCapture?.(e.pointerId);
    setPanning(true);
  };
  const onPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    const el = stageRef.current;
    const p = panRef.current;
    if (!el || !p || !p.active) return;
    el.scrollLeft = p.left - (e.clientX - p.x);
    el.scrollTop = p.top - (e.clientY - p.y);
  };
  const endPan = (e: React.PointerEvent<HTMLDivElement>) => {
    const el = stageRef.current;
    const p = panRef.current;
    if (el && p?.active) {
      try {
        el.releasePointerCapture?.(e.pointerId);
      } catch {
        /* pointer already released */
      }
    }
    panRef.current = null;
    setPanning(false);
  };

  if (state.status === "loading") {
    return <ViewerLoading />;
  }
  if (state.status === "error") {
    return <ViewerHint icon={<ImageIcon size={22} />} textKey="viewer.loadFailed" actions={<ViewerActions target={target} />} />;
  }
  const pannable = zoom > MIN_ZOOM;
  return (
    <div
      ref={stageRef}
      className={`attachment-viewer__stage${pannable ? " attachment-viewer__stage--pannable" : ""}${panning ? " attachment-viewer__stage--panning" : ""}`}
      onClick={(e) => e.stopPropagation()}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endPan}
      onPointerCancel={endPan}
    >
      <img
        className="attachment-viewer__img"
        src={state.url}
        alt={target.name}
        draggable={false}
        style={{ maxWidth: `${zoom * 100}%`, maxHeight: `${zoom * 100}%` }}
        onLoad={(e) => {
          onMeta?.(e.currentTarget.naturalWidth, e.currentTarget.naturalHeight);
          recentre();
        }}
      />
    </div>
  );
}

type FileState =
  | { status: "loading" }
  | { status: "ready"; preview: FilePreview }
  | { status: "error" };

// FileBody previews non-image attachments: PDFs stream through ReadFile's media
// token URL in an iframe, text-ish files render their (possibly truncated)
// body, and anything binary falls back to a hint card — the header's open /
// reveal actions cover those.
function FileBody({ target }: { target: ViewerTarget }) {
  const t = useT();
  const [state, setState] = useState<FileState>({ status: "loading" });

  useEffect(() => {
    let cancelled = false;
    setState({ status: "loading" });
    app.ReadFile(target.path)
      .then((preview) => {
        if (!cancelled) setState(preview.err ? { status: "error" } : { status: "ready", preview });
      })
      .catch(() => {
        if (!cancelled) setState({ status: "error" });
      });
    return () => {
      cancelled = true;
    };
  }, [target.path]);

  if (state.status === "loading") {
    return <ViewerLoading text={t("viewer.parsingDoc")} />;
  }
  if (state.status === "error") {
    return <ViewerHint icon={<FileText size={22} />} textKey="viewer.loadFailed" actions={<ViewerActions target={target} />} />;
  }
  const preview = state.preview;
  if (preview.kind === "pdf" && preview.url) {
    return (
      <div className="attachment-viewer__stage" onClick={(e) => e.stopPropagation()}>
        <iframe className="attachment-viewer__pdf" src={preview.url} title={target.name} />
      </div>
    );
  }
  if (preview.kind === "audio" && preview.url) {
    return (
      <div className="attachment-viewer__stage" onClick={(e) => e.stopPropagation()}>
        <audio className="attachment-viewer__audio" controls src={preview.url} preload="metadata" />
      </div>
    );
  }
  if (preview.kind === "video" && preview.url) {
    return (
      <div className="attachment-viewer__stage" onClick={(e) => e.stopPropagation()}>
        <video className="attachment-viewer__video" controls src={preview.url} preload="metadata" />
      </div>
    );
  }
  if (preview.kind === "html" && preview.url) {
    // Sandbox with no permissions: scripts, forms and same-origin access are
    // all blocked, so a workspace HTML file can't touch the app.
    return (
      <div className="attachment-viewer__stage" onClick={(e) => e.stopPropagation()}>
        <iframe className="attachment-viewer__html" src={preview.url} title={target.name} sandbox="" />
      </div>
    );
  }
  if (!preview.binary && preview.body) {
    const note = preview.truncated ? <div className="attachment-viewer__truncated">{t("viewer.truncated")}</div> : null;
    if (/\.(md|markdown)$/i.test(target.path)) {
      return (
        <div className="attachment-viewer__stage attachment-viewer__stage--text" onClick={(e) => e.stopPropagation()}>
          {note}
          <div className="attachment-viewer__md">
            <Markdown text={preview.body} />
          </div>
        </div>
      );
    }
    return (
      <div className="attachment-viewer__stage attachment-viewer__stage--text" onClick={(e) => e.stopPropagation()}>
        {note}
        <pre className="attachment-viewer__pre">{preview.body}</pre>
      </div>
    );
  }
  return <ViewerHint icon={<FileText size={22} />} textKey="viewer.binaryFile" actions={<ViewerActions target={target} />} />;
}

function ViewerLoading({ text }: { text?: string }) {
  return (
    <div className="attachment-viewer__state">
      <Loader2 size={22} className="attachment-viewer__spin" />
      {text ? <p className="attachment-viewer__state-note">{text}</p> : null}
    </div>
  );
}

// ViewerActions renders the prominent open/reveal buttons used inside hint
// cards (the header keeps its compact icon buttons). Both bind to the same Go
// handlers; failures surface as a toast.
function ViewerActions({ target }: { target: ViewerTarget }) {
  const t = useT();
  const { showToast } = useToast();
  const run = (fn: () => Promise<void>) => {
    fn().catch(() => showToast(t("viewer.openFailed"), "error"));
  };
  return (
    <div className="attachment-viewer__actions">
      <button
        type="button"
        className="attachment-viewer__action btn btn--secondary"
        onClick={() => run(() => app.OpenWorkspacePath(target.path))}
      >
        <FolderOpen size={14} />
        {target.kind === "folder" ? t("viewer.openFolder") : t("viewer.open")}
      </button>
      <button
        type="button"
        className="attachment-viewer__action btn btn--secondary"
        onClick={() => run(() => app.RevealWorkspacePath(target.path))}
      >
        <FolderSearch size={14} />
        {t("workspace.revealInFileManager")}
      </button>
    </div>
  );
}

function ViewerHint({ icon, textKey, actions }: { icon: ReactNode; textKey: "viewer.loadFailed" | "viewer.binaryFile" | "viewer.folderHint"; actions?: ReactNode }) {
  const t = useT();
  return (
    <div className="attachment-viewer__hint">
      <span className="attachment-viewer__hint-icon">{icon}</span>
      <p>{t(textKey)}</p>
      {actions}
    </div>
  );
}

export function AttachmentViewer() {
  const target = useViewerTarget();
  const t = useT();
  const { showToast } = useToast();
  const [meta, setMeta] = useState("");
  const [zoom, setZoom] = useState(MIN_ZOOM);

  const peers = target?.siblings ?? [];
  const index = target ? peers.findIndex((p) => p.path === target.path) : -1;
  const hasPeers = peers.length > 1 && index >= 0;

  const zoomIn = useCallback(() => setZoom((z) => clampZoom(z + ZOOM_STEP)), []);
  const zoomOut = useCallback(() => setZoom((z) => clampZoom(z - ZOOM_STEP)), []);
  const resetZoom = useCallback(() => setZoom(MIN_ZOOM), []);

  const go = useCallback(
    (delta: number) => {
      if (!hasPeers) return;
      const next = peers[(index + delta + peers.length) % peers.length];
      // Re-hand the flat peer list so ‹ › keeps working after the switch; the
      // normalized entries carry no siblings of their own.
      if (next) openAttachmentViewer({ ...next, siblings: peers });
    },
    [hasPeers, peers, index],
  );

  // A fresh file always opens at 1:1 with a clean pan.
  useEffect(() => {
    setZoom(MIN_ZOOM);
    setMeta("");
  }, [target?.path]);

  useEffect(() => {
    if (!target) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        closeAttachmentViewer();
        return;
      }
      if (hasPeers && e.key === "ArrowLeft") {
        e.preventDefault();
        go(-1);
        return;
      }
      if (hasPeers && e.key === "ArrowRight") {
        e.preventDefault();
        go(1);
        return;
      }
      if (target.kind === "image") {
        if (e.key === "+" || e.key === "=") {
          e.preventDefault();
          zoomIn();
        } else if (e.key === "-" || e.key === "_") {
          e.preventDefault();
          zoomOut();
        } else if (e.key === "0") {
          e.preventDefault();
          resetZoom();
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [target, hasPeers, go, zoomIn, zoomOut, resetZoom]);

  if (!target) return null;

  const openWith = async () => {
    try {
      await app.OpenWorkspacePath(target.path);
    } catch {
      showToast(t("viewer.openFailed"), "error");
    }
  };
  const reveal = async () => {
    try {
      await app.RevealWorkspacePath(target.path);
    } catch {
      showToast(t("viewer.openFailed"), "error");
    }
  };

  return (
    <div className="attachment-viewer" role="dialog" aria-modal="true" aria-label={target.name} onClick={closeAttachmentViewer}>
      <div className="attachment-viewer__header" onClick={(e) => e.stopPropagation()}>
        <span className={`attachment-viewer__kind attachment-viewer__kind--${target.kind}`}>
          {target.kind === "image" ? <ImageIcon size={14} /> : target.kind === "folder" ? <Folder size={14} /> : <FileText size={14} />}
        </span>
        <span className="attachment-viewer__title">
          <span className="attachment-viewer__name">{target.name}</span>
          <span className="attachment-viewer__path">
            {target.path}
            {meta ? ` · ${meta}` : ""}
            {hasPeers ? ` · ${index + 1}/${peers.length}` : ""}
          </span>
        </span>
        <button type="button" className="attachment-viewer__btn" onClick={openWith} title={t("viewer.open")} aria-label={t("viewer.open")}>
          <FolderOpen size={15} />
        </button>
        <button type="button" className="attachment-viewer__btn" onClick={reveal} title={t("workspace.revealInFileManager")} aria-label={t("workspace.revealInFileManager")}>
          <FolderSearch size={15} />
        </button>
        <button type="button" className="attachment-viewer__btn attachment-viewer__btn--close" onClick={closeAttachmentViewer} title={t("viewer.close")} aria-label={t("viewer.close")}>
          <X size={16} />
        </button>
      </div>
      {target.kind === "image" && (
        <div className="attachment-viewer__zoombar" onClick={(e) => e.stopPropagation()}>
          <button type="button" className="attachment-viewer__zoombtn" onClick={zoomOut} title={t("viewer.zoomOut")} aria-label={t("viewer.zoomOut")}>
            <ZoomOut size={14} />
          </button>
          <span className="attachment-viewer__zoomval">{Math.round(zoom * 100)}%</span>
          <button type="button" className="attachment-viewer__zoombtn" onClick={zoomIn} title={t("viewer.zoomIn")} aria-label={t("viewer.zoomIn")}>
            <ZoomIn size={14} />
          </button>
          {zoom !== MIN_ZOOM && (
            <button type="button" className="attachment-viewer__zoombtn attachment-viewer__zoombtn--reset" onClick={resetZoom} title={t("viewer.resetZoom")} aria-label={t("viewer.resetZoom")}>
              1:1
            </button>
          )}
        </div>
      )}
      <div className="attachment-viewer__body">
        {hasPeers && (
          <button
            type="button"
            className="attachment-viewer__nav attachment-viewer__nav--prev"
            onClick={(e) => {
              e.stopPropagation();
              go(-1);
            }}
            title={t("viewer.prev")}
            aria-label={t("viewer.prev")}
          >
            <ChevronLeft size={22} />
          </button>
        )}
        {target.kind === "image" && <ImageBody target={target} zoom={zoom} onMeta={(w, h) => setMeta(`${w}×${h}`)} />}
        {target.kind === "file" && <FileBody target={target} />}
        {target.kind === "folder" && <ViewerHint icon={<Folder size={22} />} textKey="viewer.folderHint" actions={<ViewerActions target={target} />} />}
        {hasPeers && (
          <button
            type="button"
            className="attachment-viewer__nav attachment-viewer__nav--next"
            onClick={(e) => {
              e.stopPropagation();
              go(1);
            }}
            title={t("viewer.next")}
            aria-label={t("viewer.next")}
          >
            <ChevronRight size={22} />
          </button>
        )}
      </div>
    </div>
  );
}
