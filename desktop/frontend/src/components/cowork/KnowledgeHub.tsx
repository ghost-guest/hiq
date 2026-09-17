// KnowledgeHub is the 项目知识中枢 (project knowledge hub) panel — the desktop
// surface for internal/projectkb. It unifies four sources (code / docs / memory
// / team) into a self-maintaining project map (map.md) plus a revision history,
// so a long-running effort stays oriented: what the project is made of, what is
// in flight, and what changed since the last sync.
//
// The compact index of this map is injected into the main session's system
// prompt (like memory), so the agent "knows" the project map without the user
// pasting anything; the full map and search are pulled on demand from here or
// via the kb_map / kb_search tools.
//
// Subscribes to "kb:changed" so a sync triggered anywhere (the boot-time first
// sync, a tool call) re-renders this panel without a manual refresh.

import { useCallback, useEffect, useMemo, useState } from "react";
import { Ban, Eye, EyeOff, FolderOpen, GitCompare, GitBranch, History, Network, RefreshCw, RotateCcw, Search } from "lucide-react";

import { app, onKBChanged } from "../../lib/bridge";
import type { KBView, KBSearchHitView, KBRevisionView } from "../../lib/types";
import { useT } from "../../lib/i18n";
import { useToast } from "../../lib/toast";
import { useConfirm } from "../../lib/confirm";

// fmtTime: the backend already formats timestamps ("2006-01-02 15:04", local),
// so this only supplies a dash for the unset case rather than re-parsing them.
function fmtTime(at: string): string {
  return at && at.trim() ? at : "—";
}

export function KnowledgeHub() {
  const t = useT();
  const { showToast } = useToast();
  const confirm = useConfirm();

  const [view, setView] = useState<KBView | null>(null);
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [note, setNote] = useState("");

  const [query, setQuery] = useState("");
  const [hits, setHits] = useState<KBSearchHitView[] | null>(null);

  const [mapKind, setMapKind] = useState("");
  const [mapText, setMapText] = useState("");
  const [mapLoading, setMapLoading] = useState(false);

  const [revisions, setRevisions] = useState<KBRevisionView[]>([]);
  const [summary, setSummary] = useState("");
  const [summaryLoading, setSummaryLoading] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const v = await app.KnowledgeStatus();
      setView(v);
    } catch {
      setView(null);
    } finally {
      setLoading(false);
    }
  }, []);

  // loadSummary pulls the diff-based incremental summary: entries added /
  // changed / removed by the latest sync versus the one before it. It is cheap
  // (bounded bullets) and survives no-op syncs, so we refresh it on every change.
  const loadSummary = useCallback(async (revision = "") => {
    setSummaryLoading(true);
    try {
      setSummary(await app.KnowledgeSummary(revision));
    } catch {
      setSummary("");
    } finally {
      setSummaryLoading(false);
    }
  }, []);

  const refreshHistory = useCallback(async () => {
    try {
      setRevisions(await app.KnowledgeHistory(30));
    } catch {
      setRevisions([]);
    }
  }, []);

  const loadMap = useCallback(async (kind: string) => {
    setMapKind(kind);
    setMapLoading(true);
    try {
      setMapText(await app.KnowledgeMap(kind));
    } catch (e) {
      setMapText(String((e as Error)?.message ?? e));
    } finally {
      setMapLoading(false);
    }
  }, []);

  // Initial load: status + history + the full map + the incremental summary.
  useEffect(() => {
    void refresh();
    void refreshHistory();
    void loadMap("");
    void loadSummary();
  }, [refresh, refreshHistory, loadMap, loadSummary]);

  // kb:changed → adopt the pushed view; refresh history + summary too (a sync
  // appends a revision and rewrites the last-change summary).
  useEffect(() => {
    return onKBChanged((v) => {
      if (v && typeof v.total === "number") setView(v as KBView);
      else void refresh();
      void refreshHistory();
      void loadSummary();
    });
  }, [refresh, refreshHistory, loadSummary]);

  const toggleWatch = useCallback(async () => {
    try {
      const v = await app.KnowledgeWatch(!(view?.watching ?? false));
      setView(v);
    } catch (e) {
      showToast(String((e as Error)?.message ?? e), "error");
    }
  }, [view, showToast]);

  const doSync = useCallback(async () => {
    setSyncing(true);
    try {
      const v = await app.KnowledgeSync(note.trim());
      setView(v);
      setNote("");
      await refreshHistory();
      await loadMap(mapKind);
      await loadSummary();
      showToast(t("kb.synced"), "info");
    } catch (e) {
      showToast(String((e as Error)?.message ?? e), "error");
    } finally {
      setSyncing(false);
    }
  }, [note, mapKind, loadMap, loadSummary, refreshHistory, showToast, t]);

  const toggleSource = useCallback(async (kind: string, enabled: boolean) => {
    try {
      const v = await app.KnowledgeSetSource(kind, enabled);
      setView(v);
    } catch (e) {
      showToast(String((e as Error)?.message ?? e), "error");
    }
  }, [showToast]);

  const doSearch = useCallback(async () => {
    try {
      const limit = 30;
      setHits(await app.KnowledgeSearch(query.trim(), mapKind, limit));
    } catch (e) {
      showToast(String((e as Error)?.message ?? e), "error");
    }
  }, [query, mapKind, showToast]);

  const doRollback = useCallback(async (r: KBRevisionView) => {
    if (!(await confirm({ title: t("kb.rollback"), message: `${fmtTime(r.at)} — ${t("kb.rollbackConfirm")}`, danger: true }))) return;
    try {
      const v = await app.KnowledgeRollback(r.id);
      setView(v);
      await refreshHistory();
      await loadMap(mapKind);
      await loadSummary();
      showToast(t("kb.rolledBack"), "info");
    } catch (e) {
      showToast(String((e as Error)?.message ?? e), "error");
    }
  }, [confirm, mapKind, loadMap, loadSummary, refreshHistory, showToast, t]);

  const copyRef = useCallback((ref: string) => {
    const cp = typeof navigator !== "undefined" ? navigator.clipboard : undefined;
    if (!cp) return;
    void cp.writeText(ref).then(
      () => showToast(t("msg.copied"), "info"),
      () => {},
    );
  }, [showToast, t]);

  const sources = useMemo(() => view?.sources ?? [], [view]);
  const isEmpty = !!view && view.total === 0 && revisions.length === 0;

  return (
    <div className="kb">
      <header className="kb__head">
        <div className="kb__head-left">
          <Network size={14} />
          <span className="kb__title">{t("kb.title")}</span>
          {view && (
            <span className="kb__meta">
              {t("kb.nodes", { n: view.total })} · {t("kb.revisions", { n: view.revisions })}
              {view.updated ? ` · ${t("kb.updated", { at: view.updated })}` : ""}
            </span>
          )}
          {view && (
            <span
              className={`kb-chip ${view.watching ? "kb-chip--on" : "kb-chip--off"}`}
              title={view.watching ? t("kb.watching") : t("kb.watchOff")}
            >
              {view.watching ? <Eye size={10} /> : <EyeOff size={10} />}
              <span>{view.watching ? t("kb.watching") : t("kb.watchOff")}</span>
              {view.watchSyncs > 0 && <span className="kb-chip__n">{view.watchSyncs}</span>}
            </span>
          )}
        </div>
        <div className="kb__head-right">
          <input
            className="kb__note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder={t("kb.syncNotePlaceholder")}
          />
          <button
            className={`btn btn--ghost kb__watch${view?.watching ? " kb__watch--on" : ""}`}
            onClick={() => void toggleWatch()}
            title={view?.watching ? t("kb.watchOff") : t("kb.watch")}
          >
            {view?.watching ? <Eye size={12} /> : <EyeOff size={12} />}
            <span>{t("kb.watch")}</span>
          </button>
          <button className="btn btn--ghost kb__sync" onClick={() => void doSync()} disabled={syncing}>
            <RefreshCw size={12} className={syncing ? "kb__spin" : undefined} />
            <span>{syncing ? t("kb.syncing") : t("kb.sync")}</span>
          </button>
          <button
            className="btn btn--ghost"
            onClick={() => void app.KnowledgeOpenDir().catch(() => {})}
            title={t("kb.openDir")}
          >
            <FolderOpen size={13} />
          </button>
        </div>
      </header>

      {loading ? (
        <div className="kb__empty">{t("kb.loading")}</div>
      ) : isEmpty ? (
        <div className="kb__empty">
          <Network size={22} />
          <p>{t("kb.empty")}</p>
          <button className="btn" onClick={() => void doSync()} disabled={syncing}>
            <RefreshCw size={12} /> {syncing ? t("kb.syncing") : t("kb.sync")}
          </button>
        </div>
      ) : (
        <>
          <div className="kb__sources">
            <span className="kb__sources-label">{t("kb.sources")}</span>
            {sources.map((s) => (
              <button
                key={s.kind}
                className={`kb-chip ${s.enabled ? "kb-chip--on" : "kb-chip--off"}`}
                onClick={() => void toggleSource(s.kind, !s.enabled)}
                title={`${s.note} · ${s.enabled ? t("kb.sourceOn") : t("kb.sourceOff")}`}
              >
                {s.enabled ? <Network size={10} /> : <Ban size={10} />}
                <span>{s.label}</span>
                <span className="kb-chip__n">{s.count}</span>
              </button>
            ))}
          </div>

          <div className="kb__search">
            <Search size={13} className="kb__search-icon" />
            <input
              className="kb__search-input"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") void doSearch(); }}
              placeholder={t("kb.searchPlaceholder")}
            />
            <button className="btn btn--ghost" onClick={() => void doSearch()}>{t("kb.search")}</button>
          </div>

          {hits !== null && (
            <ul className="kb__hits">
              {hits.length === 0 ? (
                <li className="kb__hit kb__hit--empty">{t("kb.searchEmpty")}</li>
              ) : (
                hits.map((h) => (
                  <li key={h.id} className="kb__hit" onClick={() => copyRef(h.ref)} title={h.ref}>
                    <span className="kb__hit-kind">{h.label}</span>
                    <span className="kb__hit-title">{h.title}</span>
                    <span className="kb__hit-ref">{h.ref}</span>
                    {h.summary && <p className="kb__hit-summary">{h.summary}</p>}
                  </li>
                ))
              )}
            </ul>
          )}

          <div className="kb__summary">
            <div className="kb__panel-title">
              <GitCompare size={12} /> {t("kb.summary")}
              <span className="kb__summary-at">
                {view?.lastChangeAt ? `${t("kb.lastChange")} · ${view.lastChangeAt}` : t("kb.lastChangeNone")}
              </span>
              <button
                className="btn btn--ghost kb__summary-refresh"
                onClick={() => void loadSummary()}
                disabled={summaryLoading}
                title={t("kb.summary")}
              >
                <RefreshCw size={10} className={summaryLoading ? "kb__spin" : undefined} />
              </button>
            </div>
            <p className="kb__summary-hint">{t("kb.summaryHint")}</p>
            <pre className="kb__summary-body">
              {summaryLoading && !summary ? t("kb.loading") : (summary || t("kb.summaryEmpty"))}
            </pre>
          </div>

          <div className="kb__body">
            <div className="kb__map">
              <div className="kb__map-head">
                <span className="kb__panel-title"><Network size={12} /> {t("kb.map")}</span>
                <div className="kb__map-tabs">
                  <button
                    className={`kb-tab ${mapKind === "" ? "kb-tab--active" : ""}`}
                    onClick={() => void loadMap("")}
                  >
                    {t("kb.mapAll")}
                  </button>
                  {sources.filter((s) => s.enabled).map((s) => (
                    <button
                      key={s.kind}
                      className={`kb-tab ${mapKind === s.kind ? "kb-tab--active" : ""}`}
                      onClick={() => void loadMap(s.kind)}
                    >
                      {s.label}
                    </button>
                  ))}
                </div>
              </div>
              <pre className="kb__map-body">{mapLoading ? t("kb.loading") : mapText}</pre>
            </div>

            <div className="kb__history">
              <div className="kb__panel-title"><History size={12} /> {t("kb.history")}</div>
              {revisions.length === 0 ? (
                <p className="kb__history-empty">{t("kb.historyEmpty")}</p>
              ) : (
                <ul className="kb-revs">
                  {revisions.map((r) => (
                    <li key={r.id} className="kb-rev">
                      <div className="kb-rev__top">
                        <GitBranch size={11} />
                        <span className="kb-rev__time">{fmtTime(r.at)}</span>
                        <span className="kb-rev__nodes">{t("kb.historyNodes", { n: r.nodes })}</span>
                      </div>
                      <div className="kb-rev__foot">
                        <span className="kb-rev__note">{r.note || r.trigger}</span>
                        <button className="kb-rev__btn" title={t("kb.rollback")} onClick={() => void doRollback(r)}>
                          <RotateCcw size={11} />
                        </button>
                      </div>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        </>
      )}
    </div>
  );
}
