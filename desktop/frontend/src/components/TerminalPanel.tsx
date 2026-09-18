// TerminalPanel — toggleable command console under the composer (ZCode-style
// Ctrl+` terminal, user request 2026-08-18). v1 runs one-shot commands through
// the existing RunShell bridge (executes directly, bypassing the model) and
// mirrors the streamed bash tool events into a local history. Tabs follow the
// ZCode grammar: a tab strip with add ("+") and per-tab close ("×"); closing
// the last tab collapses the panel. A full interactive PTY (ConPTY + xterm.js)
// can later replace the transport without changing this component's contract.
//
// R4 §10.5: 设备页签 — device terminal tabs (DeviceTerminal/xterm) live in the
// SAME strip, visually distinct (route badge + REC dot), added via the
// `openDevice` prop (App routes the "hiq:netdev-terminal" event here).
import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { ChevronDown, Eraser, MessageSquare, Plus, Route, TerminalSquare, X } from "lucide-react";
import { app, onEvent } from "../lib/bridge";
import { useT } from "../lib/i18n";
import type { WireEvent, WireTool } from "../lib/types";
import { getScopedItem, setScopedItem } from "../lib/profileScopedStorage";
import { DeviceTerminal } from "./DeviceTerminal";

const TERMINAL_OPEN_KEY = "hiq.terminalOpen";
const MAX_LINES = 500;
const MAX_TERMINALS = 8;
const SESSION_TAB_ID = "__side_session__";

export function loadTerminalOpen(): boolean {
  try {
    return getScopedItem(TERMINAL_OPEN_KEY, true) === "1";
  } catch {
    return false;
  }
}

export function saveTerminalOpen(open: boolean): void {
  try {
    setScopedItem(TERMINAL_OPEN_KEY, open ? "1" : "0");
  } catch {
    /* storage unavailable */
  }
}

interface TermLine {
  id: number;
  text: string;
  kind: "cmd" | "out" | "err";
}

interface TermState {
  id: number;
  lines: TermLine[];
  running: boolean;
  // §10.5 设备页签: a device terminal tab renders DeviceTerminal instead of
  // the local console (the PTY lives Go-side; state survives remounts).
  device?: string;
}

let termSeq = 0;
let lineSeq = 0;

function newTerm(): TermState {
  return { id: ++termSeq, lines: [], running: false };
}

// In-memory cache: terminals survive the panel being collapsed (Ctrl+`) and
// reopened within the app session — closing the panel no longer wipes their
// histories. Deliberately not localStorage: output text can be large and
// stale across restarts.
let cachedTerms: TermState[] | null = null;
let cachedActiveId: number | typeof SESSION_TAB_ID | null = null;

export function TerminalPanel({
  onClose,
  cwd,
  sessionPane,
  openDevice,
}: {
  onClose: () => void;
  cwd?: string;
  // Bottom axis's second pane (副会话): rendered inside a pinned tab
  // (pane-system spec §3.5). Absent → terminal-only bar.
  sessionPane?: ReactNode;
  // 设备页签入口（§10.5）: App bumps `seq` per "hiq:netdev-terminal"
  // event; the panel opens (or reactivates) that device's tab.
  openDevice?: { device: string; seq: number };
}) {
  const t = useT();
  const [terms, setTerms] = useState<TermState[]>(() => cachedTerms ?? [newTerm()]);
  const [activeId, setActiveId] = useState<number | typeof SESSION_TAB_ID>(() => cachedActiveId ?? terms[0].id);
  useEffect(() => {
    cachedTerms = terms;
    cachedActiveId = activeId;
  }, [terms, activeId]);
  const [value, setValue] = useState("");
  const termsRef = useRef(terms);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    termsRef.current = terms;
  }, [terms]);

  useEffect(() => {
    inputRef.current?.focus();
  }, [activeId]);

  // 设备页签（§10.5）: open or reactivate the device's tab; the terminal
  // itself (PTY attach, xterm, recording) is DeviceTerminal's business.
  useEffect(() => {
    if (!openDevice || !openDevice.seq || !openDevice.device) return;
    setTerms((prev) => {
      const existing = prev.find((t) => t.device === openDevice.device);
      if (existing) {
        setActiveId(existing.id);
        return prev;
      }
      if (prev.length >= MAX_TERMINALS + 3) return prev; // device tabs get a little headroom
      const term = newTerm();
      term.device = openDevice.device;
      setActiveId(term.id);
      return [...prev, term];
    });
  }, [openDevice?.seq, openDevice?.device]);

  const active = terms.find((term) => term.id === activeId);
  const sessionTabActive = activeId === SESSION_TAB_ID;

  const patchTerm = (id: number, patch: (term: TermState) => TermState) => {
    setTerms((prev) => prev.map((term) => (term.id === id ? patch(term) : term)));
  };

  const pushLine = (id: number, text: string, kind: TermLine["kind"]) => {
    patchTerm(id, (term) => {
      const next = [...term.lines, { id: ++lineSeq, text, kind }];
      return { ...term, lines: next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next };
    });
  };

  // Mirror the bash tool events of OUR in-flight RunShell call into the
  // running terminal's history. RunShell is serialized, so at most one tab is
  // running at a time; events are routed by that flag, not tab identity.
  useEffect(() => {
    return onEvent((e: WireEvent) => {
      const tool: WireTool | undefined = e.tool;
      if (!tool || tool.name !== "bash") return;
      const runningTerm = termsRef.current.find((term) => term.running);
      if (!runningTerm) return;
      if (e.kind === "tool_progress") {
        const text = tool.output ?? "";
        if (text) pushLine(runningTerm.id, text, "out");
      } else if (e.kind === "tool_result") {
        const text = tool.err ?? tool.output ?? "";
        if (text) pushLine(runningTerm.id, text, tool.err ? "err" : "out");
        patchTerm(runningTerm.id, (term) => ({ ...term, running: false }));
      }
    });
  }, []);

  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [active?.lines, activeId]);

  const addTerminal = () => {
    if (terms.length >= MAX_TERMINALS) return;
    const term = newTerm();
    setTerms((prev) => [...prev, term]);
    setActiveId(term.id);
  };

  const closeTerminal = (id: number) => {
    setTerms((prev) => {
      const next = prev.filter((term) => term.id !== id);
      if (next.length === 0) {
        // Closing the last terminal falls back to the pinned session tab when
        // the bottom axis hosts one; otherwise the whole panel collapses.
        if (sessionPane) {
          setActiveId(SESSION_TAB_ID);
          return next;
        }
        onClose();
        return prev;
      }
      if (id === activeId) setActiveId(next[next.length - 1].id);
      return next;
    });
  };

  const submit = () => {
    const cmd = value.trim();
    if (!cmd || !active || active.running) return;
    if (cmd === "clear" || cmd === "cls") {
      patchTerm(active.id, (term) => ({ ...term, lines: [] }));
      setValue("");
      return;
    }
    pushLine(active.id, cmd, "cmd");
    patchTerm(active.id, (term) => ({ ...term, running: true }));
    const id = active.id;
    void app.RunShell(cmd).catch(() => {
      pushLine(id, t("terminal.failed"), "err");
      patchTerm(id, (term) => ({ ...term, running: false }));
    });
    setValue("");
  };

  if (!active && !sessionTabActive) return null;

  return (
    <section className="terminal-panel" aria-label={t("terminal.title")}>
      <div className="terminal-panel__head">
        <div className="terminal-panel__tabs" role="tablist">
          {terms.map((term, index) =>
            term.device ? (
              <div
                key={term.id}
                className={`terminal-panel__tab terminal-panel__tab--device${term.id === activeId ? " terminal-panel__tab--active" : ""}`}
              >
                <button
                  type="button"
                  role="tab"
                  aria-selected={term.id === activeId}
                  className="terminal-panel__tab-btn"
                  onClick={() => setActiveId(term.id)}
                  title={`人工终端 ${term.device}（§6.1 全程审计录制）`}
                >
                  <Route size={11} />
                  <span>{term.device}</span>
                  <span className="terminal-panel__tab-rec" aria-label="录制中" title="录制中（审计回放见「审计」页签）" />
                </button>
                <button
                  type="button"
                  className="terminal-panel__tab-close"
                  onClick={() => closeTerminal(term.id)}
                  aria-label={t("terminal.closeTab")}
                  title={t("terminal.closeTab")}
                >
                  <X size={10} />
                </button>
              </div>
            ) : (
              <div
                key={term.id}
                className={`terminal-panel__tab${term.id === activeId ? " terminal-panel__tab--active" : ""}`}
              >
                <button
                  type="button"
                  role="tab"
                  aria-selected={term.id === activeId}
                  className="terminal-panel__tab-btn"
                  onClick={() => setActiveId(term.id)}
                  title={t("terminal.title")}
                >
                  <TerminalSquare size={11} />
                  <span>{t("terminal.tabTitle", { n: String(index + 1) })}</span>
                  {term.running && <span className="terminal-panel__tab-run" aria-hidden="true" />}
                </button>
                <button
                  type="button"
                  className="terminal-panel__tab-close"
                  onClick={() => closeTerminal(term.id)}
                  aria-label={t("terminal.closeTab")}
                  title={t("terminal.closeTab")}
                >
                  <X size={10} />
                </button>
              </div>
            ),
          )}
          <button
            type="button"
            className="terminal-panel__tab-add"
            onClick={addTerminal}
            aria-label={t("terminal.newTab")}
            title={t("terminal.newTab")}
            disabled={terms.length >= MAX_TERMINALS}
          >
            <Plus size={12} />
          </button>
          {sessionPane && (
            <div className={`terminal-panel__tab${sessionTabActive ? " terminal-panel__tab--active" : ""}`}>
              <button
                type="button"
                role="tab"
                aria-selected={sessionTabActive}
                className="terminal-panel__tab-btn"
                onClick={() => setActiveId(SESSION_TAB_ID)}
                title={t("sideSession.tabTitle")}
              >
                <MessageSquare size={11} />
                <span>{t("sideSession.tabTitle")}</span>
              </button>
            </div>
          )}
        </div>
        {cwd && !sessionTabActive && !active?.device && <span className="terminal-panel__cwd" title={cwd}>{cwd}</span>}
        <span className="terminal-panel__spacer" />
        {!sessionTabActive && active && !active.device && (
          <button type="button" className="terminal-panel__btn" onClick={() => patchTerm(active.id, (term) => ({ ...term, lines: [] }))} aria-label={t("terminal.clear")} title={t("terminal.clear")}>
            <Eraser size={12} />
          </button>
        )}
        <button type="button" className="terminal-panel__btn" onClick={onClose} aria-label={t("terminal.close")} title={t("terminal.close")}>
          <ChevronDown size={13} />
        </button>
      </div>
      {sessionTabActive ? (
        <div className="terminal-panel__session">{sessionPane}</div>
      ) : active?.device ? (
        <div className="terminal-panel__device">
          <DeviceTerminal device={active.device} />
        </div>
      ) : (
        <>
      <div className="terminal-panel__out" ref={scrollRef}>
        {active && active.lines.length === 0 ? (
          <div className="terminal-panel__empty">{t("terminal.hint")}</div>
        ) : (
          active?.lines.map((line) => (
            <div key={line.id} className={`terminal-panel__line terminal-panel__line--${line.kind}`}>
              {line.kind === "cmd" ? `❯ ${line.text}` : line.text}
            </div>
          ))
        )}
      </div>
      <div className="terminal-panel__inputrow">
        <span className="terminal-panel__prompt" aria-hidden="true">❯</span>
        <input
          ref={inputRef}
          className="terminal-panel__input"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              submit();
            }
            if (e.key === "Escape") {
              e.preventDefault();
              onClose();
            }
          }}
          placeholder={active?.running ? t("terminal.running") : t("terminal.placeholder")}
          disabled={!active || active.running}
          spellCheck={false}
          autoComplete="off"
        />
      </div>
        </>
      )}
    </section>
  );
}
