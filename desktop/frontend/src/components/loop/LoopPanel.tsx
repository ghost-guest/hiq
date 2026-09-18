// LoopPanel — 循环工程 main-area panel (docs/loop-engineering-spec.md §2.2).
// Three right-side states share one column: config (idle), live status
// (running), and the morning report (terminal). The left column holds the
// preset catalog and the serial task queue (chain-started as runs finish).
import { useCallback, useEffect, useRef, useState } from "react";
import {
  BookOpen, Eraser, FlaskConical, Gauge, OctagonX, Package, Pause, Play,
  Radar, Repeat2, Trash2,
} from "lucide-react";
import { app, onLoopStatus } from "../../lib/bridge";
import { LOOP_PRESETS, presetToConfig } from "../../lib/loopPresets";
import { useT } from "../../lib/i18n";
import type { LoopConfig, LoopRunStatus, TabMeta } from "../../lib/types";

const QUEUE_KEY = "hiq.loopQueue";

const PRESET_ICONS: Record<string, typeof Radar> = {
  flask: FlaskConical,
  broom: Eraser,
  gauge: Gauge,
  package: Package,
  book: BookOpen,
  radar: Radar,
};

function loadQueue(): LoopConfig[] {
  try {
    const raw = localStorage.getItem(QUEUE_KEY);
    if (raw) return JSON.parse(raw) as LoopConfig[];
  } catch { /* storage unavailable */ }
  return [];
}

function blankConfig(): LoopConfig {
  return {
    id: `custom-${Date.now()}`,
    name: "",
    goal: "",
    sensorCommand: "",
    verifyCommand: "",
    exploratory: false,
    autonomy: "L2",
    maxRounds: 15,
    maxTokens: 600_000,
    intervalSeconds: 0,
    commandAllowlist: [],
    startAt: "",
    endTime: "",
  };
}

export function LoopPanel({
  onClose,
  tabs,
  activeTabId,
}: {
  onClose: () => void;
  // Target selection (2026-08-19 accuracy fix): the loop acts on ONE project
  // session — the picker lists open project tabs, defaulting to the active.
  tabs: TabMeta[];
  activeTabId?: string;
}) {
  // Loop is coding-profile only (its sensor/verify/rollback run raw shell —
  // see LoopStart's backend refusal), so only dev-profile project tabs are
  // valid targets.
  const projectTabs = tabs.filter(
    (tab) => tab.tabType !== "file" && tab.scope === "project" && ((tab.profile ?? "dev").toLowerCase() === "dev"),
  );
  const [targetTabId, setTargetTabId] = useState<string>(
    () => (activeTabId && projectTabs.some((tab) => tab.id === activeTabId) ? activeTabId : projectTabs[0]?.id ?? ""),
  );
  const targetTab = projectTabs.find((tab) => tab.id === targetTabId);
  const t = useT();
  const [status, setStatus] = useState<LoopRunStatus | null>(null);
  const [draft, setDraft] = useState<LoopConfig>(() => presetToConfig(LOOP_PRESETS[0]));
  const [queue, setQueue] = useState<LoopConfig[]>(loadQueue);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const chainingRef = useRef(false);

  useEffect(() => {
    try { localStorage.setItem(QUEUE_KEY, JSON.stringify(queue)); } catch { /* ignore */ }
  }, [queue]);

  useEffect(() => {
    void app.LoopStatus().then(setStatus).catch(() => setStatus(null));
    return onLoopStatus((s) => setStatus(s));
  }, []);

  const running = status !== null && (status.state === "running" || status.state === "stopping");

  // Queue chaining: when no run is active and the queue has entries, start the
  // next one (spec §2.2 任务队列 — 睡前挂多个任务).
  useEffect(() => {
    if (running || chainingRef.current || queue.length === 0) return;
    chainingRef.current = true;
    const next = queue[0];
    void app.LoopStart(targetTabId, next)
      .then(() => setQueue((prev) => prev.slice(1)))
      .catch((e: unknown) => setError(String(e)))
      .finally(() => { chainingRef.current = false; });
  }, [running, queue, targetTabId]);

  const start = useCallback((cfg: LoopConfig) => {
    setError("");
    setBusy(true);
    void app.LoopStart(targetTabId, cfg)
      .catch((e: unknown) => setError(String(e)))
      .finally(() => setBusy(false));
  }, [targetTabId]);

  const stop = useCallback(() => {
    void app.LoopStop(t("loop.stopReason"));
  }, [t]);

  const report = !running && status?.report ? status.report : null;

  return (
    <div className="loop-panel">
      <div className="loop-panel__head">
        <Repeat2 size={16} />
        <span className="loop-panel__title">{t("loop.title")}</span>
        <span className="loop-panel__spacer" />
        <button type="button" className="loop-panel__estop" onClick={stop} disabled={!running}>
          <OctagonX size={13} />
          <span>{t("loop.estop")}</span>
        </button>
        <button type="button" className="loop-panel__close" onClick={onClose} aria-label={t("common.close")}>✕</button>
      </div>

      <div className="loop-panel__cols">
        {/* ── left: presets + queue ─────────────────────────────────────── */}
        <aside className="loop-panel__side">
          <div className="loop-panel__side-title">{t("loop.presets")}</div>
          <div className="loop-panel__presets">
            {LOOP_PRESETS.map((preset) => {
              const Icon = PRESET_ICONS[preset.icon] ?? Radar;
              return (
                <button
                  key={preset.id}
                  type="button"
                  className={`loop-preset${draft.name === preset.config.name ? " loop-preset--active" : ""}`}
                  onClick={() => setDraft(presetToConfig(preset))}
                >
                  <Icon size={14} />
                  <span className="loop-preset__name">{preset.labelZh}</span>
                  <span className="loop-preset__desc">{preset.descZh}</span>
                </button>
              );
            })}
          </div>
          <div className="loop-panel__side-title">{t("loop.queue")}</div>
          <div className="loop-panel__queue">
            {queue.length === 0 && <div className="loop-panel__queue-empty">{t("loop.queueEmpty")}</div>}
            {queue.map((cfg, i) => (
              <div key={cfg.id} className="loop-queue__item">
                <span className="loop-queue__idx">{i + 1}</span>
                <span className="loop-queue__name">{cfg.name || t("loop.untitled")}</span>
                <button type="button" onClick={() => setQueue((prev) => prev.filter((c) => c.id !== cfg.id))} aria-label={t("common.delete")}>
                  <Trash2 size={12} />
                </button>
              </div>
            ))}
          </div>
        </aside>

        {/* ── right: config / status / report ───────────────────────────── */}
        <section className="loop-panel__main">
          {error && <div className="loop-panel__error">{error}</div>}

          {report && (
            <div className="loop-report">
              <div className="loop-report__head">☽ {t("loop.report.title")}</div>
              <div className="loop-report__headline">{report.headline}</div>
              <div className="loop-report__stats">
                <span>{t("loop.report.rounds")}: {report.roundsRun}</span>
                <span>{t("loop.report.passed")}: {report.passed}</span>
                <span>{t("loop.report.rolled")}: {report.rolledBack}</span>
                <span>{t("loop.report.files")}: {report.changedFiles}</span>
              </div>
              {status?.stopNote && <div className="loop-report__note">{status.stopNote}</div>}
              {report.suggestion && <div className="loop-report__note">{report.suggestion}</div>}
            </div>
          )}

          {running && status && (
            <div className="loop-live">
              <div className="loop-live__topline">
                <span className="loop-live__round">{t("loop.round", { n: String(status.round), max: String(status.config.maxRounds) })}</span>
                {status.config.maxTokens > 0 && (
                  <span className="loop-live__tokens">_tokens: {status.tokensUsed ?? 0}/{status.config.maxTokens}_</span>
                )}
                <span className={`loop-live__state loop-live__state--${status.state}`}>{t(`loop.state.${status.state}`)}</span>
                {status.state === "running" && (
                  <button type="button" className="btn btn--small" onClick={stop}>
                    <Pause size={11} /> {t("loop.stop")}
                  </button>
                )}
              </div>
              <div className="loop-live__meta">
                {status.config.name} · {status.config.autonomy}
                {status.workspaceRoot ? ` · ${status.workspaceRoot}` : ""}
              </div>
              <div className="loop-timeline">
                {status.timeline.map((rec) => (
                  <div key={rec.round} className={`loop-timeline__row loop-timeline__row--${rec.verify}`}>
                    <span className="loop-timeline__round">{rec.round}</span>
                    <span className="loop-timeline__mark">
                      {rec.verify === "pass" ? "✓" : rec.verify === "fail-rolled-back" ? "↺" : "–"}
                    </span>
                    <span className="loop-timeline__note">
                      {rec.changed && rec.changed.length > 0 ? rec.changed.join(", ").slice(0, 80) : (rec.note ?? "")}
                    </span>
                    <span className="loop-timeline__dur">{(rec.durationMs / 1000).toFixed(0)}s</span>
                  </div>
                ))}
              </div>
            </div>
          )}

          {!running && (
            <div className="loop-config">
              <label className="loop-field">
                <span>{t("loop.cfg.target")}</span>
                <select
                  className="loop-target"
                  value={targetTabId}
                  onChange={(e) => setTargetTabId(e.target.value)}
                >
                  {projectTabs.length === 0 && <option value="">{t("loop.cfg.noProject")}</option>}
                  {projectTabs.map((tab) => (
                    <option key={tab.id} value={tab.id}>
                      {tab.workspaceName || tab.label} · {tab.topicTitle || tab.label}
                    </option>
                  ))}
                </select>
                {targetTab && <div className="loop-autonomy__hint">{targetTab.workspaceRoot}</div>}
              </label>
              <label className="loop-field">
                <span>{t("loop.cfg.name")}</span>
                <input value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} placeholder={t("loop.cfg.namePh")} />
              </label>
              <label className="loop-field loop-field--wide">
                <span>{t("loop.cfg.goal")}</span>
                <textarea rows={4} value={draft.goal} onChange={(e) => setDraft({ ...draft, goal: e.target.value })} placeholder={t("loop.cfg.goalPh")} />
              </label>
              <div className="loop-field-row">
                <label className="loop-field">
                  <span>{t("loop.cfg.sensor")}</span>
                  <input className="loop-input--mono" value={draft.sensorCommand} onChange={(e) => setDraft({ ...draft, sensorCommand: e.target.value })} placeholder="npm run lint" />
                </label>
                <label className="loop-field">
                  <span>{t("loop.cfg.verify")}</span>
                  <input className="loop-input--mono" value={draft.verifyCommand} onChange={(e) => setDraft({ ...draft, verifyCommand: e.target.value })} placeholder="npm test" />
                </label>
              </div>
              <div className="loop-field-row">
                <label className="loop-field loop-field--inline">
                  <input type="checkbox" checked={draft.exploratory} onChange={(e) => setDraft({ ...draft, exploratory: e.target.checked })} />
                  <span>{t("loop.cfg.exploratory")}</span>
                </label>
                <label className="loop-field">
                  <span>{t("loop.cfg.rounds")}</span>
                  <input type="number" min={1} max={200} value={draft.maxRounds} onChange={(e) => setDraft({ ...draft, maxRounds: Number(e.target.value) || 1 })} />
                </label>
                <label className="loop-field">
                  <span>{t("loop.cfg.maxTokens")}</span>
                  <input className="loop-input--mono" type="number" min={0} step={50000} value={draft.maxTokens} onChange={(e) => setDraft({ ...draft, maxTokens: Number(e.target.value) || 0 })} placeholder="600000" />
                </label>
                <label className="loop-field">
                  <span>{t("loop.cfg.interval")}(s)</span>
                  <input type="number" min={0} max={3600} value={draft.intervalSeconds} onChange={(e) => setDraft({ ...draft, intervalSeconds: Number(e.target.value) || 0 })} />
                </label>
                <label className="loop-field">
                  <span>{t("loop.cfg.endTime")}</span>
                  <input className="loop-input--mono" type="text" value={draft.endTime ?? ""} onChange={(e) => setDraft({ ...draft, endTime: e.target.value })} placeholder="07:00" />
                </label>
              </div>
              <div className="loop-field">
                <span>{t("loop.cfg.autonomy")}</span>
                <div className="loop-autonomy" role="radiogroup">
                  {(["L1", "L2", "L3"] as const).map((level) => (
                    <button
                      key={level}
                      type="button"
                      role="radio"
                      aria-checked={draft.autonomy === level}
                      className={`loop-autonomy__seg${draft.autonomy === level ? " loop-autonomy__seg--active" : ""}`}
                      onClick={() => setDraft({ ...draft, autonomy: level })}
                      title={t(`loop.autonomy.${level}.desc`)}
                    >
                      {t(`loop.autonomy.${level}`)}
                    </button>
                  ))}
                </div>
                <div className="loop-autonomy__hint">{t(`loop.autonomy.${draft.autonomy}.desc`)}</div>
              </div>
              <div className="loop-config__actions">
                <button type="button" className="btn btn--primary" disabled={busy} onClick={() => start(draft)}>
                  <Play size={12} /> {t("loop.start")}
                </button>
                <button type="button" className="btn" onClick={() => start({ ...draft, maxRounds: 1 })} title={t("loop.tryOnce.tip")}>
                  {t("loop.tryOnce")}
                </button>
                <button type="button" className="btn" onClick={() => setQueue((prev) => [...prev, draft])}>
                  {t("loop.enqueue")}
                </button>
                <button type="button" className="btn" onClick={() => setDraft(blankConfig())}>
                  {t("loop.blank")}
                </button>
              </div>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}
