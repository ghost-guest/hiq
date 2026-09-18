import { useCallback, useEffect, useState } from "react";
import { app } from "../../lib/bridge";
import { useI18n } from "../../lib/i18n";
import type { NetDevCutoverBoard } from "../../lib/types";

// CutoverBoardView — 割接屏（DASHBOARD spec §4.7）。三行结构：步骤流水线
// （状态机映射）/ 受影响设备进度+回退点 / 预算与命令流。进行时优先
// 连续性：本组件自己不设失焦暂停（兜底轮询由 DashShell 的 cutover 例外
// 条款处理）。无进行中变更时显示最近一次的终态复盘 + 发起入口。

const STEP_ICON: Record<string, string> = {
  done: "✓", approved: "✓", "rolled-back": "↩", skipped: "–",
  running: "⏳", pending: "·", failed: "✗",
};

interface Props {
  onJump?: (j: { tab: string; filter?: string }) => void;
  onFocusDevice?: (device: string) => void;
}

export default function CutoverBoardView({ onJump, onFocusDevice }: Props) {
  const { t } = useI18n();
  const [b, setB] = useState<NetDevCutoverBoard | null>(null);
  const [tick, setTick] = useState(0); // 倒计时走字：加载后经过的秒数（仅显示，无请求）

  const load = useCallback(() => {
    app.NetDevCutoverBoard("").then(x => { if (x) setB(x); }).catch(() => {});
  }, []);
  useEffect(() => { load(); }, [load]);
  useEffect(() => {
    const on = (e: Event) => {
      const screens = (e as CustomEvent<{ screens?: string[] }>).detail?.screens ?? [];
      if (screens.includes("cutover") || screens.includes("overview")) load();
    };
    window.addEventListener("hiq:netdev-dash", on);
    return () => window.removeEventListener("hiq:netdev-dash", on);
  }, [load]);
  // 窗口倒计时秒级走字（进行时例外：不失焦暂停，只做显示，无请求）。
  useEffect(() => { setTick(0); }, [b?.id]);
  useEffect(() => {
    if (!b || (b.status !== "running" && b.status !== "hold")) return;
    const tm = setInterval(() => setTick(x => x + 1), 1000);
    return () => clearInterval(tm);
  }, [b]);

  if (!b) return <div className="ndv__card" style={{ padding: 16 }}>{t("ndv.cut.loading")}</div>;
  if (!b.found) {
    return (
      <div className="ndv__card ndv-cut__empty" style={{ padding: 24 }}>
        <div>{t("ndv.cut.none")}</div>
        <span className="btn btn--secondary btn--small" role="button" onClick={() => onJump?.({ tab: "proposals" })}>{t("ndv.cut.fromProposals")}</span>
      </div>
    );
  }
  const remain = Math.max(0, b.remaining_sec - tick);
  const mm = String(Math.floor(remain / 60)).padStart(2, "0");
  const ss = String(remain % 60).padStart(2, "0");

  return (
    <div className="ndv-cut">
      <div className="ndv-cut__head ndv__card">
        <div className="ndv-cut__title">
          <b>{b.id}</b> {b.name}
          <span className={`ndv-cut__st ndv-cut__st--${b.status}`}>{t(`ndv.cut.st.${b.status}` as never)}</span>
        </div>
        {b.deadline && (
          <span className="ndv-cut__clock" data-over={remain <= 0}>
            {t("ndv.cut.window")} {b.deadline} · {remain > 0 ? `${mm}:${ss}` : t("ndv.cut.over")}
          </span>
        )}
        <span>{t("ndv.cut.frozen", { n: b.frozen })}</span>
        <span className={b.rollback_ready ? "ndv-cut__rb" : "dim"}>
          {b.rollback_ready ? `✓ ${t("ndv.cut.rbReady")}（${b.rollback_note}）` : t("ndv.cut.rbNone")}
        </span>
      </div>

      <div className="ndv-cut__pipeline">
        {b.steps.map((s, i) => (
          <div key={i} className={`ndv-cut__step ndv-cut__step--${s.status}`} role="button"
            onClick={() => s.device && onFocusDevice?.(s.device)}>
            <div className="ndv-cut__stephead">
              <span>{STEP_ICON[s.status] ?? "·"}</span>
              <span className="ndv-cut__steplabel">{s.label}</span>
              {s.gate && <span className="ndv-cut__gate" title={t("ndv.cut.gateTip")}>gate</span>}
              {s.decision_point && <span className="ndv-cut__dp" title={t("ndv.cut.dpTip")}>⏸</span>}
            </div>
            <div className="ndv-cut__stepmeta">
              {[s.device, s.proposal_id, s.status === "running" ? t("ndv.cut.doing") : t(`ndv.cut.step.${s.status}` as never)].filter(Boolean).join(" · ")}
            </div>
            {i < b.steps.length - 1 && <span className="ndv-cut__arrow">→</span>}
          </div>
        ))}
      </div>

      <div className="ndv-cut__mid">
        <div className="ndv__card ndv-cut__devs">
          <div className="ndv__card-title">{t("ndv.cut.devices", { n: b.devices.length })}</div>
          {b.devices.map(d => (
            <div key={d.device} className="ndv-cut__dev" role="button" onClick={() => onFocusDevice?.(d.device)}>
              <span>{d.device}</span>
              <span className={`ndv-cut__devst ndv-cut__devst--${d.status}`}>{t(`ndv.cut.dev.${d.status}` as never)}</span>
              <span className={d.rollback_ready ? "ndv-cut__rb" : "dim"} title={t("ndv.cut.rbPoint")}>{d.rollback_ready ? "↩" : "—"}</span>
            </div>
          ))}
        </div>
        <div className="ndv__card ndv-cut__jobs">
          <div className="ndv__card-title">{t("ndv.cut.jobs")}</div>
          {b.jobs.length === 0 && <div className="dim" style={{ fontSize: 11.5 }}>{t("ndv.cut.noJobs")}</div>}
          {b.jobs.map(j => (
            <div key={j.id} className="ndv-cut__job" role="button" onClick={() => onJump?.({ tab: "jobs", filter: `id:${j.id}` })}>
              <span>{j.name}</span>
              <span className={`ndv-cut__devst ndv-cut__devst--${j.status}`}>{t(`ndv.cut.job.${j.status}` as never)}</span>
              <span className="dim">{t("ndv.cut.budget", { c: j.commands, m: j.max_commands || 200, w: Math.round(j.active_ms / 1000), mw: j.max_wall_sec || 1800 })}</span>
            </div>
          ))}
          {b.report && <div className="dim" style={{ marginTop: 6, fontSize: 11 }}>{t("ndv.cut.hasReport")}</div>}
        </div>
      </div>

      <div className="ndv__card ndv-cut__audit">
        <div className="ndv__card-title">{t("ndv.cut.stream")}</div>
        {b.audit.length === 0 && <div className="dim" style={{ fontSize: 11.5 }}>{t("ndv.cut.noStream")}</div>}
        {b.audit.slice().reverse().map((a, i) => (
          <div key={i} className={`ndv-cut__auline${a.status !== "ok" ? " ndv-cut__auline--bad" : ""}`}>
            <span className="dim">{a.time}</span>
            <span>{a.device}</span>
            <span className="ndv-cut__aucmd">{a.command}</span>
            <span className={a.status === "ok" ? "dim" : ""}>{a.status}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
