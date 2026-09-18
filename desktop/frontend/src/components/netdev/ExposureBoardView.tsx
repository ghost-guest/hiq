import { useCallback, useEffect, useState } from "react";
import { app } from "../../lib/bridge";
import { useI18n } from "../../lib/i18n";
import type { NetDevExposureBoard } from "../../lib/types";

// ExposureBoardView — 暴露面屏（DASHBOARD spec §4.9）。事实（矩阵/CVE 融合）
// 与推演（BuildAttackPaths 路径/剪断建议）同屏分区、推演角标常驻（§1
// 不变量 5）。无 CVE feed = 引导态，不是 0。

interface Props {
  onJump?: (j: { tab: string; filter?: string }) => void;
  onFocusDevice?: (device: string) => void;
}

export default function ExposureBoardView({ onJump, onFocusDevice }: Props) {
  const { t } = useI18n();
  const [b, setB] = useState<NetDevExposureBoard | null>(null);

  const load = useCallback(() => {
    app.NetDevExposureBoard().then(x => { if (x) setB(x); }).catch(() => {});
  }, []);
  useEffect(() => { load(); }, [load]);
  useEffect(() => {
    const on = (e: Event) => {
      const screens = (e as CustomEvent<{ screens?: string[] }>).detail?.screens ?? [];
      if (screens.includes("exposure") || screens.includes("overview")) load();
    };
    window.addEventListener("hiq:netdev-dash", on);
    return () => window.removeEventListener("hiq:netdev-dash", on);
  }, [load]);

  if (!b) return <div className="ndv__card" style={{ padding: 16 }}>{t("ndv.exp.loading")}</div>;

  return (
    <div className="ndv-exp">
      <div className="ndv-exp__kpis">
        <span className="ndv-exp__kpi ndv-exp__kpi--crit"><b>{b.critical}</b>{t("ndv.exp.critical")}</span>
        <span className="ndv-exp__kpi"><b>{b.warning}</b>{t("ndv.exp.warning")}</span>
        <span className="ndv-exp__kpi">
          <b>{b.paths.length}</b>{t("ndv.exp.paths")}<em className="ndv-sim">{t("ndv.exp.simTag")}</em>
        </span>
        <span className="ndv-exp__kpi"><b>{b.max_hops}</b>{t("ndv.exp.maxHops")}</span>
        <span className="ndv-exp__kpi"><b>{b.unmanaged_ends}</b>{t("ndv.exp.unmanaged")}</span>
        {b.cve_needs_feed
          ? <span className="ndv-exp__kpi ndv-exp__kpi--guide" role="button" onClick={() => onJump?.({ tab: "sec" })}>{t("ndv.ovw.cveFeed")}</span>
          : <span className="ndv-exp__kpi"><b>{Object.values(b.cve_by_severity ?? {}).reduce((a, x) => a + x, 0)}</b>{t("ndv.exp.cve")}</span>}
      </div>

      <div className="ndv-exp__grid">
        <div className="ndv__card">
          <div className="ndv__card-title">{t("ndv.exp.matrix")}</div>
          {b.matrix.length === 0 && <div className="dim" style={{ fontSize: 11.5 }}>{t("ndv.exp.noMatrix")}</div>}
          <table className="ndv-exp__tbl">
            <thead>
              <tr><th>{t("ndv.exp.dev")}</th><th>crit</th><th>warn</th><th>info</th><th>CVE-C</th><th>CVE-H</th><th /></tr>
            </thead>
            <tbody>
              {b.matrix.map(r => (
                <tr key={r.device} role="button" onClick={() => onFocusDevice?.(r.device)}>
                  <td>{r.device}{!r.managed && <span className="ndv-exp__unmanaged" title={t("ndv.exp.unmanagedTip")}>?</span>}</td>
                  <td className={r.critical ? "ndv-exp__hot" : ""}>{r.critical || ""}</td>
                  <td className={r.warning ? "ndv-exp__warm" : ""}>{r.warning || ""}</td>
                  <td className="dim">{r.info || ""}</td>
                  <td className={r.cve_critical ? "ndv-exp__hot" : ""}>{r.cve_critical || ""}</td>
                  <td className={r.cve_high ? "ndv-exp__warm" : ""}>{r.cve_high || ""}</td>
                  <td role="button" className="dim" onClick={e => { e.stopPropagation(); onJump?.({ tab: "findings", filter: `device:${r.device}` }); }}>→</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        <div className="ndv__card">
          <div className="ndv__card-title">{t("ndv.exp.pathsTitle")}<em className="ndv-sim">{t("ndv.exp.simTag")}</em></div>
          {b.exposure_points.length === 0 && <div className="dim" style={{ fontSize: 11.5 }}>{t("ndv.exp.noPaths")}</div>}
          {b.paths.slice(0, 20).map((p, i) => (
            <div key={i} className="ndv-exp__path" role="button" onClick={() => onJump?.({ tab: "findings", filter: `id:${p.finding_id}` })}>
              <span className="ndv-exp__origin">{p.exposure_device}</span>
              <span className="ndv-exp__hops">
                {p.steps.map((s, j) => <span key={j} className="ndv-exp__hop">→ {s.to}</span>)}
              </span>
              <span className="ndv-exp__end" data-managed={p.end_managed}>{p.end_device}{!p.end_managed ? "?" : ""}</span>
              <span className="dim">{p.hops}h · {p.score}</span>
            </div>
          ))}
          {b.cut_suggestions.length > 0 && (
            <div className="ndv-exp__cuts">
              <div className="ndv__card-title" style={{ fontSize: 11.5 }}>{t("ndv.exp.cuts")}<em className="ndv-sim">{t("ndv.exp.simTag")}</em></div>
              {b.cut_suggestions.slice(0, 5).map((c, i) => (
                <div key={i} className="ndv-exp__cut">
                  <span className="ndv-exp__cutlink">{c.from} ⇢ {c.to}</span>
                  <span className="dim">{t("ndv.exp.cutRemoves", { n: c.paths_removed })}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
