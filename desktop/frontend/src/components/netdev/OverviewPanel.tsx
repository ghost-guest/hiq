import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { ShieldAlert, Server, Activity, AlertTriangle, FileText, LayoutDashboard } from "lucide-react";
import { app } from "../../lib/bridge";
import { useI18n } from "../../lib/i18n";
import type { NetDevOverviewSnapshot } from "../../lib/types";

// OverviewPanel — 总览屏（DASHBOARD spec §4.2）。两档一套数据：dock 紧凑
// 单列（compact）与 bench 双列渲染同一组件（响应式断点 <520/≥800 由容器
// 宽度决定，这里用 prop 显式分档）。刷新纪律（§3.4/§8.4）：
//   dock 档零定时器 —— 仅挂载 force + hiq:netdev-dash 事件驱动；
//   bench 档由 DashShell 负责 60s 可见兜底，本组件不重复起 timer。
// 诚实分母（§6）：一切比率 x/y；CVE 无 feed / 基线从未跑 = 引导态不是 0。

export interface OverviewJump { tab: string; filter?: string }

interface Props {
  compact?: boolean;
  onJump?: (j: OverviewJump) => void;
  onFocusDevice?: (device: string) => void;
  /** DashShell 已持有快照时直接喂进来（壳与卡片不双拉）。 */
  snapshot?: NetDevOverviewSnapshot | null;
  actions?: ReactNode;
}

const RISK_COLOR: Record<string, string> = {
  safe: "#46a758", low: "#7bc86c", medium: "#f5a524", high: "#f76b15", critical: "#e5484d",
};

function Spark({ series, color = "#4f8ef7" }: { series: number[]; color?: string }) {
  const pts = (series ?? []).slice(-96);
  if (pts.length < 2) return <span style={{ opacity: 0.4, fontSize: 10 }}>—</span>;
  const min = Math.min(...pts), max = Math.max(...pts);
  const span = max - min || 1;
  const d = pts.map((v, i) => `${(i / (pts.length - 1)) * 100},${28 - ((v - min) / span) * 24}`).join(" ");
  return (
    <svg viewBox="0 0 100 30" preserveAspectRatio="none" style={{ width: 90, height: 26 }}>
      <polyline points={d} fill="none" stroke={color} strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

export default function OverviewPanel({ compact, actions, onJump, onFocusDevice, snapshot }: Props) {
  const { t } = useI18n();
  const [snap, setSnap] = useState<NetDevOverviewSnapshot | null>(snapshot ?? null);
  const [busy, setBusy] = useState(false);

  const load = useCallback((force: boolean) => {
    setBusy(true);
    app.NetDevOverview(force)
      .then(s => { if (s) setSnap(s); })
      .catch(() => {})
      .finally(() => setBusy(false));
  }, []);

  useEffect(() => { if (!snapshot) load(true); }, [snapshot, load]);
  useEffect(() => { if (snapshot) setSnap(snapshot); }, [snapshot]);
  // dock 档零定时器（§8.4）：事件驱动刷新，无 setInterval。
  useEffect(() => {
    const on = (e: Event) => {
      const screens = (e as CustomEvent<{ screens?: string[] }>).detail?.screens ?? [];
      if (!screens.length || screens.includes("overview")) load(false);
    };
    window.addEventListener("hiq:netdev-dash", on);
    return () => window.removeEventListener("hiq:netdev-dash", on);
  }, [load]);

  const fresh = useMemo(() => {
    if (!snap) return true;
    return Date.now() - snap.generated_at < snap.stale_after_sec * 1000;
  }, [snap]);

  if (!snap) {
    return <div className="ndv__card" style={{ padding: 16 }}>{busy ? t("ndv.ovw.loading") : t("ndv.ovw.empty")}</div>;
  }
  const jump = (tab: string, filter?: string) => onJump?.({ tab, filter });
  const r = snap.risk;
  const sev = (n: number) => n || undefined;

  const riskBar = (
    <div className="ndv__card" data-ovw="risk">
      <div style={{ display: "flex", gap: 16 }}>
        <div style={{ flex: 1 }}>
          <div className="ndv__card-title"><ShieldAlert size={14} />{t("ndv.ovw.risk")}</div>
          <div className="ndv-ovw__num">
            <span style={{ color: RISK_COLOR[r.risk_level] ?? "#888" }}>{r.weighted_score}</span>
            <span className={`ndv-ovw__lvl ndv-ovw__lvl--${r.risk_level}`} style={{ marginLeft: 10, fontSize: 13, verticalAlign: "middle" }}>{t(`ndv.ovw.lvl.${r.risk_level}` as never)}</span>
          </div>
        </div>
        <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: 6, alignItems: "flex-end", textAlign: "right" }}>
          <div className="ndv-ovw__line dim" style={{ display: "flex", gap: 6, flexWrap: "wrap", justifyContent: "flex-end" }}>
            <span role="button" style={{ cursor: "pointer", color: RISK_COLOR[r.risk_level] }} onClick={() => jump("findings", "severity:critical")}>critical {sev(r.critical) ?? 0}</span>
            <span>·</span>
            <span role="button" style={{ cursor: "pointer" }} onClick={() => jump("findings", "severity:warning")}>warning {r.warning}</span>
            <span>·</span>
            <span>{t("ndv.ovw.info")} {r.info}</span>
          </div>
          {(r.cve_needs_feed || r.weak_creds > 0) && (
            <div className="ndv-ovw__urgent" style={{ marginTop: 0, justifyContent: "flex-end", flexWrap: "wrap" }}>
              {r.cve_needs_feed && <span role="button" onClick={() => jump("sec")}>{t("ndv.ovw.cveFeed")}</span>}
              {r.weak_creds > 0 && <span role="button" onClick={() => jump("findings", "assess")}>{t("ndv.ovw.weak", { n: r.weak_creds })}</span>}
            </div>
          )}
        </div>
      </div>
    </div>
  );

  return (
    <div className={`ndv-ovw${compact ? " ndv-ovw--compact" : ""}`}>
      <div className="ndv-ovw__head" style={{ justifyContent: actions ? "space-between" : "flex-end", alignItems: "center", marginBottom: actions ? 6 : 0 }}>
        <span className="ndv-ovw__fresh" data-fresh={fresh}>
          {fresh ? "●" : "⚠"} {t("ndv.ovw.updated", { at: new Date(snap.generated_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) })}
        </span>
        {actions && <div>{actions}</div>}
      </div>

      {/* ⭐ 在途动作（sticky 横条） */}
      <div className="ndv__card ndv-ovw__inflight">
        <span role="button" onClick={() => jump("proposals")}>{t("ndv.ovw.pending", { n: snap.inflight.proposals_pending })}</span>
        <span role="button" onClick={() => jump("jobs")}>{t("ndv.ovw.jobsRun", { n: snap.inflight.jobs_running })}</span>
        {snap.inflight.jobs_paused > 0 && <span role="button" onClick={() => jump("jobs")}>⏸ {t("ndv.ovw.jobsPause", { n: snap.inflight.jobs_paused })}</span>}
        <span role="button" onClick={() => jump("chat")}>{t("ndv.ovw.cutover", { n: snap.inflight.cutovers_active })}</span>
        <span>{t("ndv.ovw.terminals", { n: snap.inflight.terminals_open })}</span>
      </div>

      <div className="ndv-ovw__grid">
        <div className="ndv__card" data-ovw="coverage">
          <div style={{ display: "flex", gap: 16, alignItems: "center" }}>
            <div style={{ flex: 1 }}>
              <div className="ndv__card-title"><Server size={14} />{t("ndv.ovw.coverage")}</div>
              <div className="ndv-ovw__num" role="button" onClick={() => jump("devices")}>{snap.coverage.managed}</div>
            </div>
            <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: 4, alignItems: "flex-end", textAlign: "right" }}>
              <div className="ndv-ovw__line">
                <span role="button" onClick={() => jump("devices", "pending")}>{t("ndv.ovw.discovered", { n: snap.coverage.discovered })}</span>
              </div>
              <div className="ndv-ovw__line dim">
                {t("ndv.ovw.unreachable", { n: snap.coverage.unreachable })} · {t("ndv.ovw.noSnmp", { n: snap.coverage.no_snmp })}
              </div>
            </div>
          </div>
        </div>

        <div className="ndv__card" data-ovw="health">
          <div style={{ display: "flex", gap: 16 }}>
            <div style={{ flex: 1 }}>
              <div className="ndv__card-title"><Activity size={14} />{t("ndv.ovw.health")}</div>
              <div className="ndv-ovw__num" role="button" onClick={() => jump("health")}>
                {snap.health.reachable}<span style={{ fontSize: 14, opacity: 0.6, fontWeight: 600 }}>/{snap.health.polled}</span>
              </div>
              <div className="ndv-ovw__line dim">{t("ndv.ovw.reachable", { ok: snap.health.reachable, total: snap.health.polled })}</div>
            </div>
            <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: 4, alignItems: "flex-end", textAlign: "right" }}>
              <div className="ndv-ovw__line dim">
                {t("ndv.ovw.flap", { n: snap.health.flap_alerts })} · {t("ndv.ovw.p90", { n: snap.health.p90_alerts })}
              </div>
              {snap.health.max_cpu_pct > 0 && (
                <div className="ndv-ovw__line">
                  <span className={snap.health.max_cpu_pct >= 80 ? "ndv-ovw__warm" : ""}>
                    {t("ndv.ovw.water", { c: snap.health.max_cpu_pct, m: snap.health.max_mem_pct })}
                    {snap.health.max_cpu_dev ? ` · ${snap.health.max_cpu_dev}` : ""}
                  </span>
                </div>
              )}
            </div>
          </div>
          {!compact && (
            <div className="ndv-ovw__sparks">
              {Object.entries(snap.health.uptime_spark ?? {}).slice(0, 6).map(([dev, s]) => (
                <span key={dev} className="ndv-ovw__sparkrow" role="button" title={dev} onClick={() => onFocusDevice?.(dev)}>
                  <span className="ndv-ovw__sparkdev">{dev}</span>
                  <Spark series={s} />
                </span>
              ))}
            </div>
          )}
        </div>
      </div>

      {riskBar}

      <div className={compact ? "ndv-ovw__col" : "ndv-ovw__grid"}>
        <div className="ndv__card" data-ovw="events">
          <div className="ndv__card-title"><AlertTriangle size={14} />{t("ndv.ovw.events")}</div>
          {(snap.events ?? []).length === 0 && <div className="dim" style={{ fontSize: 11.5 }}>{t("ndv.ovw.noEvents")}</div>}
          {(snap.events ?? []).slice(0, compact ? 5 : 10).map(e => (
            <div key={e.id} className="ndv-ovw__ev" role="button" onClick={() => jump("findings", `id:${e.id}`)}>
              <span className={`ndv-ovw__sev ndv-ovw__sev--${e.severity}`}>{e.severity}</span>
              <span className="ndv-ovw__evtitle">{e.title}</span>
              <span className="dim">{e.at}</span>
            </div>
          ))}
        </div>

        <div className="ndv__card" data-ovw="audit">
          <div style={{ display: "flex", gap: 16, alignItems: "center" }}>
            <div style={{ flex: 1 }}>
              <div className="ndv__card-title"><FileText size={14} />{t("ndv.ovw.audit")}</div>
              <div className="ndv-ovw__num">{snap.audit.chain_total}</div>
            </div>
            <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: 4, alignItems: "flex-end", textAlign: "right" }}>
              <div className="ndv-ovw__line">
                <span role="button" onClick={() => jump("audit")}>
                  {snap.audit.chain_ok ? "✓" : "⚠"} {t("ndv.ovw.chain")}
                </span>
              </div>
              <div className="ndv-ovw__line dim">
                {t("ndv.ovw.audit24h", { r: snap.audit.read_24h, w: snap.audit.write_24h, g: snap.audit.guardrail_24h })}
              </div>
            </div>
          </div>
        </div>
      </div>

      {/* 统计条（§7.1 L0）——分母可查：每格深链明细 */}
      <div className="ndv__card ndv-ovw__stats" data-ovw="stats">
        <div className="ndv__card-title"><LayoutDashboard size={14} />{t("ndv.ovw.stats")}</div>
        <div className="ndv-ovw__statgrid">
          <span role="button" onClick={() => jump("findings")}>
            {t("ndv.ovw.mttr")} <b>{snap.stats.mttr_hours != null ? `${snap.stats.mttr_hours.toFixed(1)}h` : t("ndv.ovw.noSample")}</b>
          </span>
          <span role="button" onClick={() => jump("findings", "baseline")}>
            {t("ndv.ovw.baseline")}{" "}
            <b>{snap.stats.baseline ? t("ndv.ovw.baselineVal", { h: snap.stats.baseline.hits, r: snap.stats.baseline.rules, d: snap.stats.baseline.checked, t: snap.stats.baseline.devices }) : t("ndv.ovw.baselineNever")}</b>
          </span>
          <span role="button" onClick={() => jump("jobs")}>
            {t("ndv.ovw.jobRate")} <b>{snap.stats.job_finished > 0 ? `${snap.stats.job_done}/${snap.stats.job_finished}` : "—"}</b>
          </span>
          <span role="button" onClick={() => jump("audit")}>
            {t("ndv.ovw.change24h")} <b>{snap.audit.write_24h}</b>
          </span>
          <span>
            {t("ndv.ovw.roles")}{" "}
            <b>{Object.entries(snap.stats.device_by_role ?? {}).map(([k, v]) => `${t(`ndv.topo.role.${k || "unknown"}` as never)} ${v}`).join(" · ") || "—"}</b>
          </span>
          <span role="button" onClick={() => jump("proposals")}>
            {t("ndv.ovw.proposals")} <b>{Object.entries(snap.stats.proposal_funnel ?? {}).map(([k, v]) => `${t(`ndv.dash.funnel.${k}` as never)} ${v}`).join(" · ") || "—"}</b>
          </span>
          {snap.stats.inspection_compliance?.enabled && (
            <span title={t("ndv.ovw.complianceTip")}>
              {t("ndv.ovw.compliance")}{" "}
              <b style={{ color: snap.stats.inspection_compliance.ok ? "var(--ok, #46a758)" : "var(--warn, #f5a524)" }}>
                {!snap.stats.inspection_compliance.last_run_at
                  ? t("ndv.ovw.complianceNever")
                  : snap.stats.inspection_compliance.ok
                    ? `${t("ndv.ovw.complianceOk")} ${snap.stats.inspection_compliance.last_run_at.slice(5, 16)}`
                    : `${t("ndv.ovw.complianceMiss")} ${snap.stats.inspection_compliance.last_run_at.slice(5, 16)}`}
              </b>
            </span>
          )}
          {snap.stats.cred_health && (
            <span title={t("ndv.ovw.credTip")}>
              {t("ndv.ovw.cred")}{" "}
              <b style={{ color: snap.stats.cred_health.stale ? "var(--warn, #f5a524)" : undefined }}>
                {t("ndv.ovw.credVal", { n: snap.stats.cred_health.count, d: snap.stats.cred_health.age_days })}
                {snap.stats.cred_health.stale ? ` · ${t("ndv.ovw.credStale")}` : ""}
              </b>
            </span>
          )}
          {(snap.stats.risk_trend?.length ?? 0) > 1 && (
            <span className="ndv-ovw__trend" title={t("ndv.ovw.trendTip")}>
              {t("ndv.ovw.trend")}
              <Spark series={(snap.stats.risk_trend ?? []).map(x => x.critical * 10 + x.warning * 3 + x.info * 0.5)} color="#e5484d" />
            </span>
          )}
        </div>
      </div>
    </div>
  );
}
