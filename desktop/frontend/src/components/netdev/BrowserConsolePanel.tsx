// BrowserConsolePanel — 运维 dock 的「浏览器」 tab: a three-sub-tab console
// for manually driving the console browser session, recording runs into
// skills, and managing/editing/running those skills. Sub-tab pills follow the
// ZCode right-dock tab language (transparent inactive / soft-pill active);
// the driven browser is the external window, the collapsible preview reads
// the shared mirror stream.
import { useCallback, useEffect, useRef, useState, useSyncExternalStore, type Dispatch, type SetStateAction } from "react";
import {
  BookMarked,
  ChevronDown,
  Download,
  Monitor,
  CircleStop,
  Globe,
  HeartPulse,
  Loader2,
  MousePointerClick,
  Plus,
  RefreshCw,
  Sparkles,
  Square,
  Trash2,
  Upload,
} from "lucide-react";
import { app, onBrowserRecord } from "../../lib/bridge";
import { useConfirm } from "../../lib/confirm";
import { useT, type Translator } from "../../lib/i18n";
import type {
  BrowserConsoleElement,
  BrowserConsoleStep,
  BrowserConsoleTab,
  BrowserConsoleRecordEvent,
  BrowserConsoleSkill,
  BrowserConsoleState,
  BrowserSkillDraft,
} from "../../lib/types";
import { summarizeRecordEvent } from "../../lib/recordTrace";
import { browserMirrorSnapshot, subscribeBrowserMirror } from "../../lib/browserMirror";
import { SKILL_TEMPLATE_GROUPS } from "../../lib/skillTemplates";
import { BrowserSkillEditor } from "./BrowserSkillEditor";

type SubTab = "interact" | "record" | "skills";

const LOG_CAP = 50;

// useMirror subscribes to the shared browser mirror store; the sidebar
// preview prefers the CONSOLE session's own frame (per-session buckets)
// over the any-session aggregate.
function useMirror() {
  return useSyncExternalStore(subscribeBrowserMirror, browserMirrorSnapshot);
}
const LIVE_CAP = 200;

export function BrowserConsolePanel({ onInsertComposer }: { onInsertComposer?: (text: string) => void }) {
  const t = useT();
  const [subTab, setSubTab] = useState<SubTab>("interact");

  // --- shared session state ---
  const [state, setState] = useState<BrowserConsoleState | null>(null);
  const [busy, setBusy] = useState<string>("");
  const [error, setError] = useState("");
  const [log, setLog] = useState<string[]>([]);
  // 浏览器观察窗 (center viewer): tabs + big mirror bound together — the
  // sidebar keeps only a launcher. Polling gives near-live view of manual
  // changes in the controlled browser (frames otherwise push after actions).
  // 结构化动作历史：面板每个操作记一步，「记录为技能」一键转 SKILL.md。
  const [history, setHistory] = useState<BrowserConsoleStep[]>([]);
  // Inline preview (sidebar): invisible until the console session has a
  // frame, then unfolds once — the launcher beside it opens the big bench.
  const [previewOpen, setPreviewOpen] = useState(false);
  const mirror = useMirror();
  const recordStep = useCallback((step: BrowserConsoleStep) => {
    setHistory((prev) => [...prev.slice(-199), step]);
  }, []);

  const appendLog = useCallback((line: string) => {
    setLog((prev) => [...prev.slice(-(LOG_CAP - 1)), line]);
  }, []);

  const [tabs, setTabs] = useState<BrowserConsoleTab[]>([]);

  const refreshState = useCallback(async () => {
    try {
      const [st, tb] = await Promise.all([
        app.BrowserConsoleState(),
        app.BrowserConsoleTabs().catch(() => [] as BrowserConsoleTab[]),
      ]);
      setState(st);
      setTabs(st.open ? tb : []);
    } catch {
      setState({
        open: false, session_id: "", browser: "", attached: false, url: "",
        keep_alive: false, keep_alive_mode: "", keep_alive_url: "", keep_alive_last: 0, keep_alive_err: "",
      });
    }
  }, []);

  useEffect(() => {
    void refreshState();
  }, [refreshState]);
  const previewImg = (state?.session_id && mirror.sessions[state.session_id]?.image) || mirror.image || "";
  const hadFrameRef = useRef(false);
  if (previewImg) hadFrameRef.current = true;
  useEffect(() => {
    if (previewImg && !previewOpen) setPreviewOpen(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [Boolean(previewImg)]);

  // The center workbench broadcasts tab switches — refresh state + elements
  // so the dock stays in sync (refs die with the old page).
  useEffect(() => {
    const onConsoleChanged = () => {
      setTarget("");
      void refreshState();
      void app.BrowserConsoleElements().then(setElements).catch(() => undefined);
    };
    window.addEventListener("hiq:browser-console-changed", onConsoleChanged);
    return () => window.removeEventListener("hiq:browser-console-changed", onConsoleChanged);
  }, [refreshState]);

  // While keep-alive is armed, poll the session state so the 上次刷新 clock
  // in the status line advances without user interaction.
  useEffect(() => {
    if (!state?.keep_alive) return;
    const timer = window.setInterval(() => void refreshState(), 30_000);
    return () => window.clearInterval(timer);
  }, [state?.keep_alive, refreshState]);

  const runAction = useCallback(
    async (id: string, fn: () => Promise<unknown>, successText?: string) => {
      setError("");
      setBusy(id);
      try {
        const out = await fn();
        if (successText || out) appendLog(successText || String(out));
        await refreshState();
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        setError(msg);
        appendLog(`✕ ${msg}`);
      } finally {
        setBusy("");
      }
    },
    [appendLog, refreshState],
  );

  // --- interact sub-tab ---
  const [url, setUrl] = useState("");
  const [elements, setElements] = useState<BrowserConsoleElement[]>([]);
  const [target, setTarget] = useState("");
  const [text, setText] = useState("");
  const [submitEnter, setSubmitEnter] = useState(false);
  const [extractResult, setExtractResult] = useState("");

  // --- record sub-tab ---
  const [recording, setRecording] = useState(false);
  const [live, setLive] = useState<BrowserConsoleRecordEvent[]>([]);
  const [dropped, setDropped] = useState<BrowserConsoleRecordEvent[]>([]);
  const [showDropped, setShowDropped] = useState(false);
  const [generating, setGenerating] = useState(false);
  const [skills, setSkills] = useState<BrowserConsoleSkill[]>([]);

  useEffect(() => onBrowserRecord((ev) => {
    setLive((prev) => (ev.type === "effect" ? prev : [...prev.slice(-(LIVE_CAP - 1)), ev]));
  }), []);

  const refreshSkills = useCallback(async () => {
    try {
      setSkills(await app.BrowserConsoleListSkills());
    } catch { /* listed empty on error */ }
  }, []);

  useEffect(() => {
    void refreshSkills();
  }, [refreshSkills]);

  // Re-list skills each time the tab is entered (external edits / deletions
  // from outside the panel stay visible).
  useEffect(() => {
    if (subTab === "skills") void refreshSkills();
  }, [subTab, refreshSkills]);

  const startRecord = () =>
    void runAction("record", async () => {
      setLive([]);
      setDropped([]);
      await app.BrowserConsoleRecordStart();
      setRecording(true);
      appendLog(t("ndv.brc.recStart"));
    });

  const stopRecord = () =>
    void (async () => {
      setBusy("record");
      try {
        const events = await app.BrowserConsoleRecordStop();
        setRecording(false);
        const filtered = await app.BrowserConsoleFilterTrace(events);
        setDropped(filtered.dropped);
        setLive(filtered.kept);
        // Raw trace (kept + dropped) travels to the editor as the 原始对照
        // so the review can verify nothing meaningful was filtered away.
        setRawTrace(events);
        appendLog(t("ndv.brc.recEnd", { kept: filtered.kept.length, dropped: filtered.dropped.length }));
        await generateSkill(filtered.kept);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
        setRecording(false);
      } finally {
        setBusy("");
      }
    })();

  const generateSkill = async (events: BrowserConsoleRecordEvent[]) => {
    if (events.length === 0) {
      setError(t("brc.emptyTrace"));
      return;
    }
    setGenerating(true);
    try {
      const draft: BrowserSkillDraft = await app.BrowserConsoleGenerateSkill("", events);
      setDraft(draft);
      if (draft.fallback && draft.detail) appendLog(`⚠ ${draft.detail}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setGenerating(false);
    }
  };

  const [draft, setDraft] = useState<BrowserSkillDraft | null>(null);
  const [rawTrace, setRawTrace] = useState<BrowserConsoleRecordEvent[]>([]);
  // Run-after-open: the skills tab's 试运行 opens the editor and immediately
  // starts the trial (the waiting-on-human banner lives in the editor).
  const [autoRun, setAutoRun] = useState(false);
  const openEditor = useCallback((d: BrowserSkillDraft, run = false) => {
    setAutoRun(run);
    setDraft(d);
  }, []);

  // --- render ---
  if (draft) {
    return (
      <BrowserSkillEditor
        initialContent={draft.content}
        trace={rawTrace}
        autoRun={autoRun}
        onCancel={() => {
          setDraft(null);
          setAutoRun(false);
        }}
        onSaved={() => {
          setDraft(null);
          setAutoRun(false);
          void refreshSkills();
        }}
      />
    );
  }

  return (
    <div className="ndv__card ndv-brc" style={{ display: "flex", flexDirection: "column", minHeight: 0, flex: 1 }}>
      {/* sub-tab pills (ZCode style) + session dot */}
      <div className="ndv-brc__subtabs">
        <div className="ndv-brc__subtab-group">
          <button
            type="button"
            className={`ndv-brc__subtab${subTab === "interact" ? " ndv-brc__subtab--active" : ""}`}
            onClick={() => setSubTab("interact")}
          >
            <MousePointerClick size={12} />
            <span>{t("brc.subInteract")}</span>
          </button>
          <button
            type="button"
            className={`ndv-brc__subtab${subTab === "record" ? " ndv-brc__subtab--active" : ""}`}
            onClick={() => setSubTab("record")}
          >
            <CircleStop size={12} />
            <span>{t("brc.subRecord")}</span>
          </button>
          <button
            type="button"
            className={`ndv-brc__subtab${subTab === "skills" ? " ndv-brc__subtab--active" : ""}`}
            onClick={() => setSubTab("skills")}
          >
            <BookMarked size={12} />
            <span>{t("brc.subSkills")}</span>
          </button>
        </div>
        <span className={`ndv-brc__dot${state?.open ? " ndv-brc__dot--on" : ""}`} title={state?.browser ?? ""} />
      </div>

      {/* session bar */}
      <div className="ndv-brc__session">
        <span className="ndv-brc__session-label">
          {state?.open ? `${state.browser}${state.attached ? " · attach" : ""}` : t("brc.sessionClosed")}
        </span>
        {state?.open ? (
          <>
            <KeepAliveToggle state={state} busy={busy} runAction={runAction} t={t} />
            <button type="button" className="btn btn--secondary btn--small" onClick={() => void runAction("close", () => app.BrowserConsoleClose(), t("brc.closed"))}>
              {t("brc.closeBrowser")}
            </button>
          </>
        ) : (
          <button type="button" className="btn btn--primary btn--small" onClick={() => void runAction("open", async () => { await app.BrowserConsoleOpen("", ""); }, t("brc.opened"))}>
            {busy === "open" ? <Loader2 size={12} className="composer-phase__spin" /> : t("brc.openBrowser")}
          </button>
        )}
      </div>
      {state?.open && state.keep_alive && <KeepAliveStatus state={state} runAction={runAction} t={t} />}

      {error && <div className="banner banner--error ndv-brc__error">{error}</div>}

      {/* inline mirror (console session) — appears with the first frame */}
      {state?.open && previewImg && (
        <>
          <button type="button" className="ndv-brc__preview-toggle" onClick={() => setPreviewOpen((v) => !v)}>
            <ChevronDown size={12} className={previewOpen ? "" : "ndv-brc__chev-closed"} />
            <span>{t("brc.preview")}</span>
          </button>
          {previewOpen && (
            <div className="ndv-brc__preview">
              <img className="ndv-brc__preview-img" src={previewImg} alt="" />
            </div>
          )}
        </>
      )}

      {/* bench launcher — the tabs/mirror/elements live in the center
          浏览器工作台 (same mechanism as logs/sec/dash); this opens it. */}
      {state?.open && (
        <button
          type="button"
          className="ndv-brc__view-launch"
          onClick={() => window.dispatchEvent(new CustomEvent("hiq:netdev-bench", { detail: "browser" }))}
          title={t("brc.viewerHint")}
        >
          <Monitor size={12} />
          <span className="ndv-brc__view-launch-title">{t("brc.viewerTitle")}</span>
          <span className="ndv-brc__view-launch-n">{tabs.length}</span>
        </button>
      )}



      {subTab === "interact" ? (
        <InteractSub
          {...{ url, setUrl, elements, setElements, target, setTarget, text, setText, submitEnter, setSubmitEnter, extractResult, setExtractResult, busy, runAction, t, onInsertComposer, onAction: recordStep }}
        />
      ) : subTab === "record" ? (
        <RecordSub
          {...{
            recording, live, dropped, showDropped, setShowDropped, generating, t,
            busy, startRecord, stopRecord, setDraft,
          }}
        />
      ) : (
        <SkillsSub {...{ skills, t, busy, runAction, refreshSkills, openEditor }} />
      )}

      {/* op log (动作记录) — every panel action, plus 记录为技能 turning the
          structured history into a SKILL.md draft for the skills tab editor. */}
      {(log.length > 0 || history.length > 0) && (
        <div className="ndv-brc__log">
          <div className="ndv-brc__log-head">
            <span>{t("brc.opLog")}</span>
            {history.length > 0 && (
              <>
                <span className="ndv-brc__elements-count">{history.length}</span>
                <button
                  type="button"
                  className="btn btn--secondary btn--small"
                  title={t("brc.logToSkillHint")}
                  onClick={() =>
                    openEditor({ name: "", content: historySkillContent(history), fallback: false }, false)
                  }
                >
                  <Sparkles size={11} />
                  <span>{t("brc.logToSkill")}</span>
                </button>
                <button
                  type="button"
                  className="btn btn--secondary btn--small ndv-brc__skill-del"
                  title={t("brc.logClear")}
                  onClick={() => setHistory([])}
                >
                  <Trash2 size={11} />
                </button>
              </>
            )}
          </div>
          {log.slice(-8).map((line, i) => (
            <div key={i} className="ndv-brc__log-line">{line}</div>
          ))}
        </div>
      )}
    </div>
  );
}

// useMirror subscribes to the shared browser mirror store (no local state).
// formatElapsed renders the recording clock as m:ss.
function formatElapsed(seconds: number): string {
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

// ElementPicker — the 元素 section: a bounded, filterable list of the page's
// interactive elements. Fetching can return ~200 rows; without a filter and
// display cap the raw dump drowned the panel (and pre-scroll-fix, pushed it
// over the card). Show matched rows up to a cap, always report the count.
const ELEMENT_CAP = 50;

function ElementPicker(props: {
  elements: BrowserConsoleElement[];
  target: string;
  onPick: (ref: string) => void;
  onRefresh: () => void;
  busy: string;
  t: Translator;
}) {
  const { elements, target, onPick, onRefresh, busy, t } = props;
  const [filter, setFilter] = useState("");
  // Hover flashes a short outline on the page element; click lights it
  // longer. Best-effort — a stale ref just doesn't paint.
  const hoverTimerRef = useRef<number>(0);
  const flash = (ref: string, ms: number) => {
    void app.BrowserConsoleHighlight(ref, ms).catch(() => undefined);
  };
  const q = filter.trim().toLowerCase();
  const matched = q
    ? elements.filter((el) =>
        `${el.role} ${el.name} ${el.ref} ${el.value ?? ""}`.toLowerCase().includes(q))
    : elements;
  const shown = matched.slice(0, ELEMENT_CAP);
  return (
    <>
      <div className="ndv-brc__section-head">
        <span>{t("brc.elements")}</span>
        <span className="ndv-brc__elements-count">
          {q ? `${shown.length}/${matched.length}` : elements.length}
        </span>
        <div className="ndv-brc__elements-tools">
          <input
            className="mem-input ndv-brc__elements-filter"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder={t("brc.filterElements")}
            spellCheck={false}
          />
          <button
            type="button"
            className="btn btn--secondary btn--small"
            aria-label={t("brc.refreshElements")}
            title={t("brc.refreshElements")}
            onClick={onRefresh}
            disabled={busy === "elements"}
          >
            <RefreshCw size={11} />
          </button>
        </div>
      </div>
      <div className="ndv-brc__elements">
        {elements.length === 0 && <div className="ndv-brc__hint">{t("brc.elementsEmpty")}</div>}
        {shown.map((el) => (
          <button
            type="button"
            key={el.ref}
            className={`ndv-brc__element${target === el.ref ? " ndv-brc__element--sel" : ""}`}
            onMouseEnter={() => {
              window.clearTimeout(hoverTimerRef.current);
              hoverTimerRef.current = window.setTimeout(() => flash(el.ref, 500), 150);
            }}
            onMouseLeave={() => window.clearTimeout(hoverTimerRef.current)}
            onClick={() => {
              flash(el.ref, 1500);
              onPick(el.ref);
            }}
            title={`${el.role} ${el.name}`}
          >
            <span className="ndv-brc__element-role">{el.role}</span>
            <span className="ndv-brc__element-name">{el.name || el.ref}</span>
            {el.value ? <span className="ndv-brc__element-value">{el.value}</span> : null}
          </button>
        ))}
        {matched.length > shown.length && (
          <div className="ndv-brc__hint">{t("brc.elementsCapped", { n: matched.length - shown.length })}</div>
        )}
      </div>
    </>
  );
}

// --- interact sub-tab -----------------------------------------------------------------

function InteractSub(props: {
  url: string;
  setUrl: (v: string) => void;
  elements: BrowserConsoleElement[];
  setElements: (v: BrowserConsoleElement[]) => void;
  target: string;
  setTarget: (v: string) => void;
  text: string;
  setText: (v: string) => void;
  submitEnter: boolean;
  setSubmitEnter: (v: boolean) => void;
  extractResult: string;
  setExtractResult: (v: string) => void;
  busy: string;
  runAction: (id: string, fn: () => Promise<unknown>, successText?: string) => Promise<void>;
  onInsertComposer?: (text: string) => void;
  onAction: (step: BrowserConsoleStep) => void;
  t: Translator;
}) {
  const { url, setUrl, elements, setElements, target, setTarget, text, setText, submitEnter, setSubmitEnter, extractResult, setExtractResult, busy, runAction, onInsertComposer, onAction, t } = props;
  return (
    <div className="ndv-brc__body">
      <form
        className="ndv-brc__urlbar"
        onSubmit={(e) => {
          e.preventDefault();
          if (url) { onAction({ type: "navigate", url }); void runAction("nav", () => app.BrowserConsoleNavigate(url)); }
        }}
      >
        <input
          className="mem-input ndv-brc__url"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          placeholder="https://…"
          spellCheck={false}
        />
        <button
          type="submit"
          className="btn btn--secondary btn--small"
          disabled={!url || busy === "nav"}
          aria-label={t("brc.go")}
          title={t("brc.go")}
        >
          {busy === "nav" ? <Loader2 size={12} className="composer-phase__spin" /> : <Globe size={12} />}
        </button>
      </form>

      <div className="ndv-brc__section">
        <ElementPicker
          elements={elements}
          target={target}
          onPick={setTarget}
          onRefresh={() => {
            setTarget(""); // refs are snapshot-transient: a fresh list invalidates the old pick
            void runAction("elements", async () => {
              setElements(await app.BrowserConsoleElements());
            });
          }}
          busy={busy}
          t={t}
        />
      </div>

      <div className="ndv-brc__section">
        <div className="ndv-brc__field">
          <span className="ndv-brc__field-label">{t("brc.target")}</span>
          <input className="mem-input" value={target} onChange={(e) => setTarget(e.target.value)} placeholder="e12 / CSS" spellCheck={false} />
        </div>
        <div className="ndv-brc__field">
          <span className="ndv-brc__field-label">{t("brc.text")}</span>
          <input className="mem-input" value={text} onChange={(e) => setText(e.target.value)} placeholder={t("brc.textPlaceholder")} />
        </div>
        <div className="ndv-brc__actions">
          <label className="ndv-brc__check">
            <input type="checkbox" checked={submitEnter} onChange={(e) => setSubmitEnter(e.target.checked)} />
            <span>↵</span>
          </label>
          <button
            type="button"
            className="btn btn--secondary btn--small"
            disabled={!target || !text || busy === "type"}
            onClick={() => {
              onAction({ type: "type", target, text });
              if (submitEnter) onAction({ type: "key", value: "enter" });
              void runAction("type", async () => {
                const out = await app.BrowserConsoleType(target, text);
                if (submitEnter) await app.BrowserConsoleKey("enter");
                return out;
              });
            }}
          >
            {t("brc.typeBtn")}
          </button>
          <button
            type="button"
            className="btn btn--secondary btn--small"
            disabled={!target || busy === "click"}
            onClick={() => {
              onAction({ type: "click", target });
              void runAction("click", () => app.BrowserConsoleClick(target));
            }}
          >
            {t("brc.clickBtn")}
          </button>
        </div>
      </div>

      <div className="ndv-brc__section">
        <ExtractBox {...{ extractResult, setExtractResult, target, busy, runAction, onInsertComposer, onAction, t }} />
      </div>
    </div>
  );
}

function ExtractBox(props: {
  extractResult: string;
  setExtractResult: (v: string) => void;
  target: string;
  busy: string;
  runAction: (id: string, fn: () => Promise<unknown>, successText?: string) => Promise<void>;
  onInsertComposer?: (text: string) => void;
  onAction: (step: BrowserConsoleStep) => void;
  t: Translator;
}) {
  const { extractResult, setExtractResult, target, busy, runAction, onInsertComposer, onAction, t } = props;
  const [selector, setSelector] = useState("");
  return (
    <>
      <div className="ndv-brc__section-head">
        <span>{t("brc.extract")}</span>
        <div className="ndv-brc__section-tools">
          {extractResult && (
            <>
              <button type="button" className="btn btn--secondary btn--small" onClick={() => void navigator.clipboard?.writeText(extractResult)}>
                {t("brc.copy")}
              </button>
              <button
                type="button"
                className="btn btn--secondary btn--small"
                disabled={!onInsertComposer}
                onClick={() => {
                  // Bounded excerpt into the chat composer — the netdev
                  // "交给 AI" convention (LogPanel's sendToAI pattern).
                  onInsertComposer?.(extractResult.slice(0, 1200));
                }}
                title={t("brc.sendToAIHint")}
              >
                <Upload size={11} />
              </button>
            </>
          )}
        </div>
      </div>
      <div className="ndv-brc__extract-form">
        <input
          className="mem-input"
          value={selector}
          onChange={(e) => setSelector(e.target.value)}
          placeholder={t("brc.extractPlaceholder")}
          spellCheck={false}
        />
        <button
          type="button"
          className="btn btn--secondary btn--small"
          disabled={busy === "extract"}
          onClick={() => {
            onAction({ type: "extract", target: selector || target });
            void runAction("extract", async () => {
              setExtractResult(await app.BrowserConsoleExtract(selector || target));
            });
          }}
        >
          {busy === "extract" ? <Loader2 size={12} className="composer-phase__spin" /> : t("brc.extractBtn")}
        </button>
      </div>
      {extractResult && <pre className="ndv-brc__extract-result">{extractResult.slice(0, 4000)}</pre>}
    </>
  );
}

// --- keep-alive (会话保活) ------------------------------------------------------------

// formatKaTime renders the last-refresh stamp as HH:MM:SS local time.
function formatKaTime(unixMillis: number): string {
  if (!unixMillis) return "–";
  const d = new Date(unixMillis);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}:${String(d.getSeconds()).padStart(2, "0")}`;
}

const KA_MODES = ["ping", "navigate", "local"] as const;
const KA_INTERVALS = [1, 5, 15, 30, 60] as const; // minutes

// KeepAliveToggle is the session-bar switch. Arming uses the current status
// line settings (mode/interval/URL persist there while armed).
function KeepAliveToggle(props: {
  state: BrowserConsoleState;
  busy: string;
  runAction: (id: string, fn: () => Promise<unknown>, successText?: string) => Promise<void>;
  t: Translator;
}) {
  const { state, busy, runAction, t } = props;
  if (!state.keep_alive) {
    return (
      <button
        type="button"
        className="btn btn--secondary btn--small"
        title={t("brc.kaHint")}
        onClick={() => void runAction("ka", () => app.BrowserConsoleSetKeepAlive(true, 300, "ping", ""), t("brc.kaOn"))}
      >
        {busy === "ka" ? <Loader2 size={12} className="composer-phase__spin" /> : <HeartPulse size={12} />}
        <span>{t("brc.keepAlive")}</span>
      </button>
    );
  }
  return (
    <button
      type="button"
      className="btn btn--secondary btn--small ndv-brc__ka-btn--on"
      title={t("brc.kaStopHint")}
      onClick={() => void runAction("ka", () => app.BrowserConsoleSetKeepAlive(false, 0, "", ""), t("brc.kaOff"))}
    >
      <HeartPulse size={12} className="ndv-brc__ka-pulse" />
      <span>{t("brc.kaStop")}</span>
    </button>
  );
}

// KeepAliveStatus is the armed status line: mode/interval/URL selectors that
// re-arm in place (the kernel swaps settings without dropping the loop), the
// last-refresh clock, and any heartbeat error.
function KeepAliveStatus(props: {
  state: BrowserConsoleState;
  runAction: (id: string, fn: () => Promise<unknown>, successText?: string) => Promise<void>;
  t: Translator;
}) {
  const { state, runAction, t } = props;
  const [mode, setMode] = useState<string>(state.keep_alive_mode || "ping");
  const [minutes, setMinutes] = useState<number>(5);
  const [url, setUrl] = useState(state.keep_alive_url || "");
  const rearm = (m: string, min: number, u: string) =>
    void runAction("ka", () => app.BrowserConsoleSetKeepAlive(true, min * 60, m, u), t("brc.kaOn"));
  return (
    <div className="ndv-brc__ka">
      <span className="ndv-brc__ka-clock">
        <HeartPulse size={11} className="ndv-brc__ka-pulse" />
        {t("brc.kaActive")} · {t("brc.kaLast")} {formatKaTime(state.keep_alive_last)}
      </span>
      <select className="mem-select ndv-brc__ka-select" value={mode} onChange={(e) => { setMode(e.target.value); rearm(e.target.value, minutes, url); }}>
        {KA_MODES.map((m) => (
          <option key={m} value={m}>{t(`brc.kaMode_${m}` as never)}</option>
        ))}
      </select>
      <select className="mem-select ndv-brc__ka-select" value={minutes} onChange={(e) => { const v = parseInt(e.target.value, 10); setMinutes(v); rearm(mode, v, url); }}>
        {KA_INTERVALS.map((m) => (
          <option key={m} value={m}>{t("brc.kaEvery", { n: m })}</option>
        ))}
      </select>
      {mode === "navigate" && (
        <input
          className="mem-input ndv-brc__ka-url"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          onBlur={() => rearm(mode, minutes, url)}
          placeholder={t("brc.kaUrlPlaceholder")}
          spellCheck={false}
        />
      )}
      {state.keep_alive_err && <span className="ndv-brc__ka-err" title={state.keep_alive_err}>{state.keep_alive_err}</span>}
    </div>
  );
}

// historySkillContent turns the panel's structured action history into a
// SKILL.md draft — targets ride verbatim (refs are transient, the editor's
// review pass swaps them for CSS/text anchors).
function historySkillContent(steps: BrowserConsoleStep[]): string {
  const rows = steps
    .map((st, i) => {
      const target = st.type === "navigate" ? (st.url ?? "") : st.target ?? "";
      const value = st.type === "type" ? (st.text ?? "") : st.value ?? "";
      return `| ${i + 1} | ${st.type} | \`${target}\` | ${value} |`;
    })
    .join("\n");
  return [
    "---",
    "name: browser-actions",
    "description: 由运维面板动作记录生成，请完善描述与选择器。",
    "runAs: subagent",
    "executor: browser-flow",
    "allowed-tools: browser_open, browser_navigate, browser_click, browser_type, browser_wait, browser_extract",
    "---",
    "",
    "# 面板动作技能",
    "",
    "## 何时使用",
    "",
    "（说明适用场景；把会变的值改成 {{参数名}}）",
    "",
    "## 步骤",
    "",
    "| # | 操作 | 目标 | 值 |",
    "|---|------|------|------|",
    rows,
    "",
    "## 注意事项",
    "",
    "- 由面板动作记录生成；快照编号（eN）是瞬时值，改用 CSS 选择器或 text= 可见文字锚",
    "",
    "## 验证",
    "",
    "（如何确认执行成功）",
    "",
  ].join("\n");
}

// --- skills sub-tab --------------------------------------------------------------------

function SkillsSub(props: {
  skills: BrowserConsoleSkill[];
  t: Translator;
  busy: string;
  runAction: (id: string, fn: () => Promise<unknown>, successText?: string) => Promise<void>;
  refreshSkills: () => Promise<void>;
  openEditor: (draft: BrowserSkillDraft, autoRun: boolean) => void;
}) {
  const { skills, t, busy, runAction, refreshSkills, openEditor } = props;
  const confirm = useConfirm();
  const [editError, setEditError] = useState("");
  // Template gallery popover: 新建技能 opens the picker instead of jumping
  // straight into a blank table — real workflows differ, the descriptions
  // say which shape fits.
  const [galleryOpen, setGalleryOpen] = useState(false);
  const importInputRef = useRef<HTMLInputElement | null>(null);
  const galleryRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!galleryOpen) return;
    const onDown = (e: MouseEvent) => {
      if (galleryRef.current && !galleryRef.current.contains(e.target as Node)) setGalleryOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [galleryOpen]);

  const openSkill = async (name: string, autoRun: boolean) => {
    setEditError("");
    try {
      const content = await app.BrowserConsoleReadSkill(name);
      openEditor({ name, content, fallback: false }, autoRun);
    } catch (err) {
      setEditError(err instanceof Error ? err.message : String(err));
    }
  };

  const deleteSkill = async (name: string) => {
    const ok = await confirm({
      title: t("brc.deleteSkillTitle"),
      message: t("brc.deleteSkillConfirm", { name }),
      danger: true,
      confirmLabel: t("brc.deleteSkillConfirmBtn"),
    });
    if (!ok) return;
    await runAction("del-skill", async () => {
      await app.BrowserConsoleDeleteSkill(name);
      await refreshSkills();
      return t("brc.skillDeleted", { name });
    });
  };

  return (
    <div className="ndv-brc__body">
      <div className="ndv-brc__record-bar">
        <span className="ndv-brc__rec-label">{t("brc.skillsHint")}</span>
        <button
          type="button"
          className="btn btn--secondary btn--small"
          title={t("brc.importSkillHint")}
          onClick={() => importInputRef.current?.click()}
        >
          <Upload size={11} />
          <span>{t("brc.importSkill")}</span>
        </button>
        <div className="ndv-brc__gallery-wrap" ref={galleryRef}>
          <button
            type="button"
            className="btn btn--primary btn--small"
            onClick={() => setGalleryOpen((v) => !v)}
            disabled={galleryOpen}
          >
            <Plus size={11} />
            <span>{t("brc.newSkill")}</span>
            <ChevronDown size={11} />
          </button>
          {galleryOpen && (
            <div className="ndv-brc__gallery">
              {SKILL_TEMPLATE_GROUPS.map((group) => (
                <div key={group.id} className="ndv-brc__gallery-group">
                  <div className="ndv-brc__gallery-cat">{t(group.labelKey as never)}</div>
                  {group.templates.map((tpl) => (
                    <button
                      key={tpl.id}
                      type="button"
                      className="ndv-brc__gallery-item"
                      onClick={() => {
                        setGalleryOpen(false);
                        openEditor({ name: "", content: tpl.build(), fallback: false }, false);
                      }}
                    >
                      <span className="ndv-brc__gallery-title">
                        {t(tpl.titleKey as never)}
                        <span className="ndv-brc__gallery-form">{tpl.form === "prose" ? t("brc.tplFormProse") : t("brc.tplFormTable")}</span>
                      </span>
                      <span className="ndv-brc__gallery-desc">{t(tpl.descKey as never)}</span>
                    </button>
                  ))}
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
      <input
        ref={importInputRef}
        type="file"
        accept=".md,.markdown,text/markdown"
        style={{ display: "none" }}
        onChange={(e) => {
          const file = e.target.files?.[0];
          e.target.value = "";
          if (!file) return;
          void file.text().then((content) => {
            openEditor({ name: "", content, fallback: false }, false);
          }).catch(() => undefined);
        }}
      />
      {editError && <div className="banner banner--error ndv-brc__error">{editError}</div>}

      <div className="ndv-brc__section">
        <div className="ndv-brc__section-head">
          <span>{t("brc.savedSkills")}</span>
          <span className="ndv-brc__hint-inline">/{t("brc.slashHint")}</span>
        </div>
        {skills.length === 0 && (
          <div className="ndv-brc__hint">
            {t("brc.noSkills")}
            <div className="ndv-brc__hint-sub">{t("brc.noSkillsHint")}</div>
          </div>
        )}
        {skills.map((s) => (
          <div key={s.name} className="ndv-brc__skill-row">
            <span className="ndv-brc__skill-name">{s.name}</span>
            <span className="ndv-brc__skill-desc">{s.description}</span>
            <button
              type="button"
              className="btn btn--secondary btn--small"
              title={t("brc.runSkill")}
              disabled={busy === "run-skill"}
              onClick={() => void openSkill(s.name, true)}
            >
              ▶
            </button>
            <button
              type="button"
              className="btn btn--secondary btn--small"
              title={t("brc.exportSkill")}
              onClick={() =>
                void (async () => {
                  try {
                    const content = await app.BrowserConsoleReadSkill(s.name);
                    const blob = new Blob([content], { type: "text/markdown;charset=utf-8" });
                    const a = document.createElement("a");
                    a.href = URL.createObjectURL(blob);
                    a.download = `${s.name}.md`;
                    a.click();
                    URL.revokeObjectURL(a.href);
                  } catch (err) {
                    setEditError(err instanceof Error ? err.message : String(err));
                  }
                })()
              }
            >
              <Download size={11} />
            </button>
            <button
              type="button"
              className="btn btn--secondary btn--small"
              title={t("brc.editSkill")}
              onClick={() => void openSkill(s.name, false)}
            >
              ✎
            </button>
            <button
              type="button"
              className="btn btn--secondary btn--small ndv-brc__skill-del"
              title={t("brc.deleteSkill")}
              onClick={() => void deleteSkill(s.name)}
            >
              <Trash2 size={11} />
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}

// --- record sub-tab --------------------------------------------------------------------

function RecordSub(props: {
  recording: boolean;
  live: BrowserConsoleRecordEvent[];
  dropped: BrowserConsoleRecordEvent[];
  showDropped: boolean;
  setShowDropped: Dispatch<SetStateAction<boolean>>;
  generating: boolean;
  t: Translator;
  busy: string;
  startRecord: () => void;
  stopRecord: () => void;
  setDraft: (d: BrowserSkillDraft | null) => void;
}) {
  const { recording, live, dropped, showDropped, setShowDropped, generating, t, busy, startRecord, stopRecord, setDraft } = props;
  const [skillName, setSkillName] = useState("");
  const [regenError, setRegenError] = useState("");
  // Elapsed recording clock (mm:ss), ticking only while recording.
  const [elapsed, setElapsed] = useState(0);
  const recordStartRef = useRef<number>(0);
  useEffect(() => {
    if (!recording) return;
    recordStartRef.current = Date.now();
    setElapsed(0);
    const timer = window.setInterval(() => {
      setElapsed(Math.floor((Date.now() - recordStartRef.current) / 1000));
    }, 1000);
    return () => window.clearInterval(timer);
  }, [recording]);
  return (
    <div className="ndv-brc__body">
      <div className="ndv-brc__record-bar">
        {recording ? (
          <>
            <span className="ndv-brc__rec-dot" aria-hidden="true" />
            <span className="ndv-brc__rec-count">{formatElapsed(elapsed)}</span>
            <span className="ndv-brc__rec-label">{t("brc.recording")} · {live.length}</span>
            <button type="button" className="btn btn--danger btn--small" onClick={stopRecord} disabled={busy === "record"}>
              {busy === "record" ? <Loader2 size={12} className="composer-phase__spin" /> : <Square size={11} />}
              <span>{t("brc.stopRecord")}</span>
            </button>
          </>
        ) : (
          <>
            <span className="ndv-brc__rec-label">{t("brc.recordHint")}</span>
            <button
              type="button"
              className="btn btn--primary btn--small"
              onClick={startRecord}
              disabled={busy === "record"}
            >
              <CircleStop size={11} />
              <span>{t("brc.startRecord")}</span>
            </button>
          </>
        )}
      </div>

      <div className="ndv-brc__steps">
        {live.length === 0 && <div className="ndv-brc__hint">{t("brc.recordEmpty")}</div>}
        {live.map((ev, i) => (
          <div key={i} className="ndv-brc__step-row">
            <span className="ndv-brc__step-n">{i + 1}</span>
            <span className="ndv-brc__step-text">{summarizeRecordEvent(ev)}</span>
          </div>
        ))}
        {recording && live.length > 0 && (
          <div className="ndv-brc__hint">{t("brc.recordTail")}</div>
        )}
      </div>

      {dropped.length > 0 && (
        <div className="ndv-brc__dropped">
          <button type="button" className="ndv-brc__dropped-toggle" onClick={() => setShowDropped((v) => !v)}>
            <ChevronDown size={11} className={showDropped ? "" : "ndv-brc__chev-closed"} />
            <span>{t("brc.filtered", { n: dropped.length })}</span>
          </button>
          {showDropped && (
            <div className="ndv-brc__dropped-list">
              {dropped.map((ev, i) => (
                <div key={i} className="ndv-brc__step-row ndv-brc__step-row--dim">
                  <span className="ndv-brc__step-text">{summarizeRecordEvent(ev)}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {generating && (
        <div className="ndv-brc__generating">
          <Sparkles size={12} className="composer-phase__spin" />
          <span>{t("brc.generating")}</span>
        </div>
      )}

      {!recording && live.length > 0 && !generating && (
        <div className="ndv-brc__regen">
          <input
            className="mem-input"
            value={skillName}
            onChange={(e) => setSkillName(e.target.value)}
            placeholder={t("brc.skillNamePlaceholder")}
          />
          <button
            type="button"
            className="btn btn--secondary btn--small"
            onClick={() =>
              void (async () => {
                setRegenError("");
                try {
                  setDraft(await app.BrowserConsoleGenerateSkill(skillName, live));
                } catch (err) {
                  setRegenError(err instanceof Error ? err.message : String(err));
                }
              })()
            }
          >
            {t("brc.regen")}
          </button>
        </div>
      )}
      {regenError && <div className="banner banner--error ndv-brc__error">{regenError}</div>}
    </div>
  );
}
