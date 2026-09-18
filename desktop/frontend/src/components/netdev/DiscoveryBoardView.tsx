import { useCallback, useEffect, useState } from "react";
import { app } from "../../lib/bridge";
import { useI18n } from "../../lib/i18n";
import type { NetDevDiscoveryBoard } from "../../lib/types";

// DiscoveryBoardView — 发现屏（DASHBOARD spec §4.8）。漏斗（哪一步丢了
// 多少线索）+ 层级账本（vantage 出发的深度）+ 图实一致性（设计↔IP 规划
// 对账）+ 端口突变卡（R2 journal）。全部读存量，零探测。

interface Props {
  onJump?: (j: { tab: string; filter?: string }) => void;
  onFocusDevice?: (device: string) => void;
}

const RISK_PORTS = new Set([23, 445, 3389, 5900, 21, 69, 161]);

export default function DiscoveryBoardView({ onJump, onFocusDevice }: Props) {
  const { t } = useI18n();
  const [b, setB] = useState<NetDevDiscoveryBoard | null>(null);

  const load = useCallback(() => {
    app.NetDevDiscoveryBoard().then(x => { if (x) setB(x); }).catch(() => {});
  }, []);
  useEffect(() => { load(); }, [load]);
  useEffect(() => {
    const on = (e: Event) => {
      const screens = (e as CustomEvent<{ screens?: string[] }>).detail?.screens ?? [];
      if (screens.includes("discovery") || screens.includes("overview")) load();
    };
    window.addEventListener("hiq:netdev-dash", on);
    return () => window.removeEventListener("hiq:netdev-dash", on);
  }, [load]);

  if (!b) return <div className="ndv__card" style={{ padding: 16 }}>{t("ndv.discboard.loading")}</div>;
  const max = Math.max(1, ...b.funnel.map(f => f.count));
  const triTotal = Math.max(1, b.tri_source.design + b.tri_source.only_plan);

  return (
    <div className="ndv-dsb">
      <div className="ndv-dsb__kpis">
        <span className="ndv-dsb__kpi" role="button" onClick={() => onJump?.({ tab: "devices", filter: "pending" })}>
          <b>{b.pending}</b>{t("ndv.dash.funnel.pending")}
        </span>
        <span className="ndv-dsb__kpi"><b>{b.promoted}</b>{t("ndv.dash.funnel.promoted")}</span>
        <span className="ndv-dsb__kpi" role="button" onClick={() => onJump?.({ tab: "devices" })}>
          <b>{b.managed}</b>{t("ndv.dash.funnel.managed")}
        </span>
        <span className="ndv-dsb__kpi"><b>{b.subnets_done}/{b.subnets_total}</b>{t("ndv.discboard.subnets")}</span>
        <span className="ndv-dsb__kpi" title={t("ndv.discboard.hopsTip")}>
          <b>{b.layer_depth}/{b.max_hops}</b>{t("ndv.discboard.depth")}
        </span>
        {b.run_status && (
          <span className="ndv-dsb__kpi dim">
            {t("ndv.discboard.run", { v: b.run_vantage || "—", s: t(`ndv.discboard.runst.${b.run_status}` as never), at: b.run_updated_at ?? "—" })}
          </span>
        )}
      </div>

      <div className="ndv-dsb__grid">
        <div className="ndv__card">
          <div className="ndv__card-title">{t("ndv.discboard.funnel")}</div>
          {b.funnel.map(f => (
            <div key={f.key} className="ndv-dsb__frow" role="button" onClick={() => onJump?.({ tab: "devices", filter: f.key })}>
              <span className="ndv-dsb__flabel">{t(`ndv.dash.funnel.${f.key}` as never)}</span>
              <span className="ndv-dsb__fbar"><span style={{ width: `${(f.count / max) * 100}%` }} /></span>
              <span className="ndv-dsb__fcount">{f.count}</span>
            </div>
          ))}
          <div className="dim" style={{ fontSize: 11, marginTop: 6 }}>{t("ndv.discboard.funnelNote")}</div>
        </div>

        <div className="ndv__card">
          <div className="ndv__card-title">{t("ndv.discboard.layers")}</div>
          {b.layers.length === 0 && <div className="dim" style={{ fontSize: 11.5 }}>{t("ndv.discboard.noLayers")}</div>}
          {b.layers.map((l, i) => (
            <div key={i} className="ndv-dsb__lrow" style={{ paddingLeft: 8 + l.layer * 18 }} role="button" onClick={() => onFocusDevice?.(`L${l.layer}`)}>
              <span className="ndv-dsb__ltag">L{l.layer}</span>
              <span>{l.note}</span>
            </div>
          ))}
          <div className="dim" style={{ fontSize: 11, marginTop: 6 }}>{t("ndv.discboard.layersNote")}</div>
        </div>
      </div>

      <div className="ndv-dsb__grid">
        <div className="ndv__card">
          <div className="ndv__card-title">{t("ndv.discboard.tri")}</div>
          <div className="ndv-dsb__tri">
            <span>{t("ndv.discboard.triDesign", { n: b.tri_source.design })}</span>
            <span>{t("ndv.discboard.triPlan", { n: b.tri_source.plan })}</span>
            <span>{t("ndv.discboard.triMatch", { n: b.tri_source.matched })}</span>
          </div>
          <div className="ndv-dsb__tribar" title={t("ndv.discboard.triTip")}>
            <span style={{ width: `${(b.tri_source.only_design / triTotal) * 100}%` }} className="ndv-dsb__tribar--design" />
            <span style={{ width: `${(b.tri_source.matched / triTotal) * 100}%` }} className="ndv-dsb__tribar--match" />
            <span style={{ width: `${(b.tri_source.only_plan / triTotal) * 100}%` }} className="ndv-dsb__tribar--plan" />
          </div>
          <div className="dim" style={{ fontSize: 11 }}>
            {t("ndv.discboard.triDrift", { d: b.tri_source.only_design, p: b.tri_source.only_plan })}
            <span role="button" style={{ textDecoration: "underline" }} onClick={() => onJump?.({ tab: "topology" })}>{t("ndv.discboard.openTopo")}</span>
          </div>
        </div>

        <div className="ndv__card">
          <div className="ndv__card-title">{t("ndv.discboard.ports")}</div>
          {b.port_events.length === 0 && <div className="dim" style={{ fontSize: 11.5 }}>{t("ndv.discboard.noPorts")}</div>}
          {b.port_events.slice().reverse().slice(0, 10).map((e, i) => (
            <div key={i} className={`ndv-dsb__pev${RISK_PORTS.has(e.port) ? " ndv-dsb__pev--risk" : ""}`}>
              <span className="dim">{e.at}</span>
              <span>{e.ip}</span>
              <span className="ndv-dsb__pport">{e.port}</span>
              <span className={e.kind === "newly-opened" ? "ndv-dsb__popen" : "ndv-dsb__pclose"}>
                {t(`ndv.discboard.pev.${e.kind}` as never)}
              </span>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
