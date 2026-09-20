// browserMirror — module store for the cowork dock's 浏览器 mirror tab.
//
// Frames arrive through the App-level onBrowserMirror subscription (always
// mounted), not inside the panel, so nothing is missed while the tab/dock is
// closed; the panel and the dock's tab auto-switch both read this snapshot.
// The pure transition (applyBrowserMirrorFrame) is separated from the live
// store so it can be unit-tested without a DOM.
import type { BrowserMirrorFrame } from "./types";

export interface MirrorSessionFrame {
  image: string;
  url: string;
  title?: string; // page title (live frames)
  tabId?: string; // page target the latest live frame came from
  at: number; // unix millis of the latest frame
}

export interface BrowserMirrorState {
  image: string; // latest screenshot (data URL); "" before the first frame
  url: string; // latest page URL (tool source)
  source: "tool" | "auto" | "";
  running: boolean; // between status start and end
  lastText: string; // browser name / latest action / run summary
  seq: number; // increments on every store change
  picked: import("./types").BrowserPickDescriptor | null; // latest picked element (panel chip)
  // Per-session latest frames (kernel tags frames with session_id): the ops
  // viewer lists the console session plus agent-driven sessions from here.
  // Capped by recency; aggregate fields above stay the historical behavior.
  sessions: Record<string, MirrorSessionFrame>;
}

const MIRROR_SESSION_CAP = 8;

const initialBrowserMirrorState: BrowserMirrorState = {
  image: "",
  url: "",
  source: "",
  running: false,
  lastText: "",
  seq: 0,
  picked: null,
  sessions: {},
};

// rememberSessionFrame keeps the latest frame per session id, capping the map
// by recency (oldest entry drops once over the cap).
function rememberSessionFrame(
  sessions: Record<string, MirrorSessionFrame>,
  id: string,
  patch: Partial<MirrorSessionFrame>,
): Record<string, MirrorSessionFrame> {
  if (!id) return sessions;
  const prev = sessions[id];
  const next: Record<string, MirrorSessionFrame> = {
    ...sessions,
    [id]: { image: prev?.image ?? "", url: prev?.url ?? "", at: Date.now(), ...patch },
  };
  const ids = Object.keys(next).sort((a, b) => next[a].at - next[b].at);
  while (ids.length > MIRROR_SESSION_CAP) {
    delete next[ids.shift() as string];
  }
  return next;
}

// applyBrowserMirrorFrame is the pure state transition. startedActivity
// reports "onset of a new browsing activity" — the signal App uses to
// auto-open the dock: a run/session start, or the first frame after the app
// (re)started into an already-open browser session whose start was never seen.
export function applyBrowserMirrorFrame(
  state: BrowserMirrorState,
  frame: BrowserMirrorFrame,
): { state: BrowserMirrorState; startedActivity: boolean } {
  if (frame.kind === "live") {
    // Live screencast frame: same visual fields as "frame", plus the page
    // title and the target tab. These arrive at ~10 fps — bump the seq so
    // useSyncExternalStore re-renders the viewport image.
    return {
      state: {
        ...state,
        image: frame.image ?? state.image,
        url: frame.url || state.url,
        lastText: state.lastText,
        seq: state.seq + 1,
        sessions: frame.session_id
          ? rememberSessionFrame(state.sessions, frame.session_id, {
              image: frame.image ?? state.sessions[frame.session_id]?.image ?? "",
              url: frame.url || state.sessions[frame.session_id]?.url || "",
              title: frame.title ?? state.sessions[frame.session_id]?.title,
              tabId: frame.tab_id ?? state.sessions[frame.session_id]?.tabId,
            })
          : state.sessions,
      },
      startedActivity: false,
    };
  }
  if (frame.kind === "picked") {
    let desc: import("./types").BrowserPickDescriptor | null = null;
    try {
      desc = frame.text ? (JSON.parse(frame.text) as import("./types").BrowserPickDescriptor) : null;
    } catch {
      desc = null;
    }
    return { state: { ...state, picked: desc, seq: state.seq + 1 }, startedActivity: false };
  }
  if (frame.kind === "frame") {
    return {
      state: {
        ...state,
        image: frame.image ?? "",
        url: frame.url || state.url,
        source: frame.source,
        lastText: frame.text || state.lastText,
        seq: state.seq + 1,
        sessions: frame.session_id
          ? rememberSessionFrame(state.sessions, frame.session_id, {
              image: frame.image ?? "",
              url: frame.url || state.sessions[frame.session_id]?.url || "",
              title: frame.title ?? state.sessions[frame.session_id]?.title,
              tabId: frame.tab_id ?? state.sessions[frame.session_id]?.tabId,
            })
          : state.sessions,
      },
      startedActivity: !state.running,
    };
  }
  switch (frame.phase) {
    case "start":
      return {
        state: {
          ...state,
          source: frame.source,
          running: true,
          lastText: frame.text ?? "",
          seq: state.seq + 1,
          sessions: frame.session_id
            ? rememberSessionFrame(state.sessions, frame.session_id, {})
            : state.sessions,
        },
        startedActivity: true,
      };
    case "step":
      // browser_auto action captions — text only, run state untouched.
      return {
        state: { ...state, source: frame.source, lastText: frame.text ?? "", seq: state.seq + 1 },
        startedActivity: false,
      };
    case "end":
      return {
        state: { ...state, running: false, lastText: frame.text || state.lastText, seq: state.seq + 1 },
        startedActivity: false,
      };
    default:
      return { state, startedActivity: false };
  }
}

// --- live module store --------------------------------------------------------

let browserMirrorState: BrowserMirrorState = initialBrowserMirrorState;
const browserMirrorListeners = new Set<() => void>();

export function browserMirrorSnapshot(): BrowserMirrorState {
  return browserMirrorState;
}

export function subscribeBrowserMirror(fn: () => void): () => void {
  browserMirrorListeners.add(fn);
  return () => {
    browserMirrorListeners.delete(fn);
  };
}

// pushBrowserMirrorFrame applies a frame and notifies subscribers; returns
// whether this frame marked new browsing activity (see applyBrowserMirrorFrame).
export function pushBrowserMirrorFrame(frame: BrowserMirrorFrame): boolean {
  const result = applyBrowserMirrorFrame(browserMirrorState, frame);
  browserMirrorState = result.state;
  for (const listener of browserMirrorListeners) listener();
  return result.startedActivity;
}

// --- focus requests ------------------------------------------------------------
// App auto-opens the cowork dock, but the dock's active tab is internal to
// CoworkDock — a focus request lets it switch to the mirror tab itself.

const browserMirrorFocusListeners = new Set<() => void>();

export function requestBrowserMirrorFocus(): void {
  for (const listener of browserMirrorFocusListeners) listener();
}

export function subscribeBrowserMirrorFocus(fn: () => void): () => void {
  browserMirrorFocusListeners.add(fn);
  return () => {
    browserMirrorFocusListeners.delete(fn);
  };
}
