import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { app } from "../../lib/bridge";
import { useI18n } from "../../lib/i18n";
import type { NetDevAuditEntryView, NetDevOverviewSnapshot } from "../../lib/types";
import OverviewPanel, { OverviewJump } from "./OverviewPanel";
import ChainBoard from "./ChainBoard";
import CutoverBoardView from "./CutoverBoardView";
import DiscoveryBoardView from "./DiscoveryBoardView";
import ExposureBoardView from "./ExposureBoardView";

// DashShell — 大屏家族的壳（DASHBOARD spec §4.1/§4.11）。职责：五页签、
// 场景感知默认页、投影模式、底条审计 ticker、刷新纪律（§8.4：写侧事件
// 驱动 + 可见时 60s 兜底；割接屏例外不失焦暂停）。总览快照由壳持有并
// 喂给 OverviewPanel（壳与卡片不双拉）。

export type DashScreen = "overview" | "chain" | "cutover" | "discovery" | "exposure";
const SCREENS: DashScreen[] = ["overview", "chain", "cutover", "discovery", "exposure"];

interface Props {
  initialScreen?: DashScreen;
  initialFinding?: string;
  onClose: () => void;
  onToggleRightRail?: () => void;
  rightRailCollapsed?: boolean;
  onJump?: (j: OverviewJump) => void;
  onFocusDevice?: (device: string) => void;
  /** 手动切屏后置 true：场景感知默认页退位（会话内记住）。 */
  manualSignal?: { screen: DashScreen; finding?: string } | null;
}

export default function DashShell({ initialScreen, initialFinding, onClose, onToggleRightRail, rightRailCollapsed, onJump, onFocusDevice, manualSignal }: Props) {
  const { t } = useI18n();
  const [screen, setScreen] = useState<DashScreen>(initialScreen ?? "overview");
  const [finding, setFinding] = useState(initialFinding ?? "");
  const [snap, setSnap] = useState<NetDevOverviewSnapshot | null>(null);
  const [ticker, setTicker] = useState<NetDevAuditEntryView[]>([]);
  const [projection, setProjection] = useState(false);
  const [rotateSec, setRotateSec] = useState(30);
  const [paused, setPaused] = useState(false); // 兜底轮询手动暂停（⟳/∥）
  const manualRef = useRef(!!initialScreen);
  const hoverRef = useRef(false);
  const rootRef = useRef<HTMLDivElement | null>(null);

  // 场景感知默认页（§4.1）：未手动切过时按快照的 scenario 提示落位。
  const loadSnap = useCallback((force: boolean) => {
    app.NetDevOverview(force).then(s => {
      if (!s) return;
      setSnap(s);
      if (!manualRef.current) {
        if (s.scenario_cutover_active) setScreen("cutover");
        else if (s.scenario_discovery_run) setScreen("discovery");
        else setScreen("overview");
      }
    }).catch(() => {});
  }, []);
  const loadTicker = useCallback(() => {
    app.NetDevAuditTail(20).then(a => setTicker(a ?? [])).catch(() => {});
  }, []);

  useEffect(() => { loadSnap(true); loadTicker(); }, [loadSnap, loadTicker]);
  useEffect(() => { if (manualSignal) { manualRef.current = true; setScreen(manualSignal.screen); setFinding(manualSignal.finding ?? ""); } }, [manualSignal]);

  // 兜底轮询（§8.4）：仅可见时；割接屏例外（不失焦暂停——进行时优先连续性）。
  useEffect(() => {
    if (paused) return;
    let visible = typeof document === "undefined" || document.visibilityState === "visible";
    const onVis = () => { visible = document.visibilityState === "visible"; };
    document.addEventListener("visibilitychange", onVis);
    const tm = setInterval(() => {
      const cutoverException = screen === "cutover";
      if (visible || cutoverException) { loadSnap(false); loadTicker(); }
      if (visible && screen !== "overview" && screen !== "chain") { /* 屏自己按事件刷新 */ }
    }, 60_000);
    return () => { document.removeEventListener("visibilitychange", onVis); clearInterval(tm); };
  }, [paused, screen, loadSnap, loadTicker]);

  // 写侧事件（§3.4）：payload 只有屏枚举，不带数据。
  useEffect(() => {
    const on = () => {
      loadSnap(false);
      loadTicker();
    };
    window.addEventListener("hiq:netdev-dash", on);
    return () => window.removeEventListener("hiq:netdev-dash", on);
  }, [loadSnap, loadTicker]);

  // 投影模式（§4.11）：轮播 + 悬停暂停 + Esc 先退投影。
  useEffect(() => {
    if (!projection) return;
    const tm = setInterval(() => { if (!hoverRef.current && !paused) setScreen(s => SCREENS[(SCREENS.indexOf(s) + 1) % SCREENS.length]); }, rotateSec * 1000);
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { e.stopPropagation(); setProjection(false); }
      if (e.altKey && e.key.toLowerCase() === "p") { e.preventDefault(); setProjection(p => !p); }
    };
    window.addEventListener("keydown", onKey, true);
    return () => { clearInterval(tm); window.removeEventListener("keydown", onKey, true); };
  }, [projection, rotateSec, paused]);

  const screenBody = useMemo(() => {
    switch (screen) {
      case "chain": return <ChainBoard findingID={finding} onJump={onJump} onFocusDevice={onFocusDevice} />;
      case "cutover": return <CutoverBoardView onJump={onJump} onFocusDevice={onFocusDevice} />;
      case "discovery": return <DiscoveryBoardView onJump={onJump} onFocusDevice={onFocusDevice} />;
      case "exposure": return <ExposureBoardView onJump={onJump} onFocusDevice={onFocusDevice} />;
      default: return <OverviewPanel snapshot={snap} onJump={onJump} onFocusDevice={onFocusDevice} />;
    }
  }, [screen, finding, snap, onJump, onFocusDevice]);

  return (
    <div ref={rootRef} className={`ndv-dash${projection ? " ndv-dash--projection" : ""}`}
      onMouseEnter={() => { hoverRef.current = true; }}
      onMouseLeave={() => { hoverRef.current = false; }}>
      <div className="ndv-dash__bar" role="tablist" aria-label={t("ndv.dash.aria")}>
        <b className="ndv-dash__brand">{t("ndv.dash.title")}</b>
        {SCREENS.map((s, i) => (
          <span key={s} role="tab" aria-selected={screen === s}
            className={`ndv-dash__chip${screen === s ? " ndv-dash__chip--on" : ""}`}
            onClick={() => { manualRef.current = true; setScreen(s); }}>
            {t(`ndv.dash.screen.${s}`)}
            <kbd className="ndv-dash__alt">{i + 1}</kbd>
          </span>
        ))}
        <span className="ndv-dash__spacer" />
        <span className="ndv-dash__btn" role="button" title={t("ndv.dash.railTip")} onClick={() => onToggleRightRail?.()}>
          {rightRailCollapsed ? t("ndv.dash.railOpen") : t("ndv.dash.railClose")}
        </span>
        <span className="ndv-dash__btn" role="button" title={t("ndv.dash.pauseTip")} onClick={() => setPaused(p => !p)}>
          {paused ? t("ndv.dash.paused") : t("ndv.dash.auto")}
        </span>
        {projection && (
          <select className="ndv-dash__rot" value={rotateSec} onChange={e => setRotateSec(Number(e.target.value))} title={t("ndv.dash.rotTip")}>
            {[15, 30, 60].map(n => <option key={n} value={n}>{n}s</option>)}
          </select>
        )}
        <span className="ndv-dash__btn" role="button" title={t("ndv.dash.projTip")} onClick={() => setProjection(p => !p)}>
          {projection ? t("ndv.dash.projExit") : t("ndv.dash.proj")}
        </span>
        <span className="ndv-dash__btn ndv-dash__btn--close" role="button" onClick={onClose}>×</span>
      </div>

      <div className="ndv-dash__body" data-projection={projection}>
        {screenBody}
      </div>

      <div className="ndv-dash__ticker">
        <span className="ndv-dash__tickerlabel">{t("ndv.dash.ticker")}</span>
        <div className="ndv-dash__tickerrun">
          {(ticker || []).slice().reverse().map((a, i) => (
            <span key={i} className={`ndv-dash__tick${a.status !== "ok" ? " ndv-dash__tick--bad" : ""}`} role="button"
              onClick={() => onJump?.({ tab: "audit" })}>
              <span className="dim">{a.time}</span> {a.device} · {a.command}
            </span>
          ))}
        </div>
      </div>
    </div>
  );
}
