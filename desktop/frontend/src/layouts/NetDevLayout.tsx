import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { AlertTriangle, Activity, ClipboardCheck, FileText, HeartPulse, History, LayoutDashboard, MousePointerClick, Network, PanelLeft, ScanSearch, ScrollText, Server, ShieldCheck, SlidersHorizontal } from "lucide-react";
import { t as tt } from "../lib/i18n";
import { app, onNetdevHealth, onNetdevLive } from "../lib/bridge";
import { ProfileSegmented } from "../components/AppChrome";
import { useConfirm } from "../lib/confirm";
import { getActiveProject, restoreActiveProject, setActiveProject, subscribeActiveProject, type NetDevProjectScope } from "../lib/netdevProjectStore";
import { ProposalActions } from "../components/netdev/ProposalCenter";
import { LiveOpsPanel } from "../components/netdev/LiveOpsPanel";
import { LogPanel } from "../components/netdev/LogPanel";
import { LogWorkbench } from "../components/netdev/LogWorkbench";
import { SecWorkbench } from "../components/netdev/SecWorkbench";
import { BrowserWorkbench } from "../components/netdev/BrowserWorkbench";
import { CutoverView } from "../components/netdev/CutoverView";
import { TemplateCard } from "../components/netdev/TemplateCard";
import { SrvConfCard } from "../components/netdev/SrvConfCard";
import { HealthPanel } from "../components/netdev/HealthPanel";
import { BrowserConsolePanel } from "../components/netdev/BrowserConsolePanel";
import { ImportWizardCard } from "../components/netdev/ImportWizardCard";
import { JobsPanel } from "../components/netdev/JobsPanel";
import { StateHistoryPanel } from "../components/netdev/StateHistoryPanel";
import { AlertSetupWizard } from "../components/netdev/AlertSetupWizard";
import { TopoIcon, topoRoleKey } from "../components/netdev/TopoIcon";
import { DockTabs, useDockTabState } from "../components/DockTabs";
import type { NetDevSettingsView, NetDevDeviceHealth, NetDevFinding, NetDevAggregatedFinding, NetDevProposal, NetDevAuditEntryView, NetDevTopologyGraph, NetDevBackupVersion, NetDevCutoverRun, NetDevDiscoveredHost, NetDevTopoImportPreview, NetDevAttackPathReport, NetDevDiscoverPlan, NetDevDiscoveryRunState, NetDevOverviewSnapshot, NetDevTopoReconcile } from "../lib/types";
import { useT } from "../lib/i18n";
import logoSymbol from "../assets/logo-symbol.png";
import { Markdown } from "../components/Markdown";
import { UnifiedDiff } from "../components/editors/UnifiedDiff";
import DashShell, { type DashScreen } from "../components/netdev/DashShell";
import OverviewPanel, { type OverviewJump } from "../components/netdev/OverviewPanel";
import "../styles/netdev.css";

// NetDevLayout is 运维页面的 shell (NETDEV_SPEC §10.1). The FRAME follows
// the coding view exactly: a full-height left rail (spans the chrome row) with
// the shared sidebar__brandrow at the top, the global AppChrome over the main
// area (title bar in its center slot + the standard right control cluster),
// and a tabbed right dock. Styling is scoped to .app--netdev / .ndv-*.

// NetdevTitleBar rides the global chrome's CENTER slot in netdev mode. To keep
// the header identical to the coding view's, it LEADS with the same topicbar
// (session title · rename · workspace subtitle, supplied by App) and appends
// 运维专属 chips after it — title on the left, mode bits on the right.
export function NetdevTitleBar({ leading, onOpenSettings }: { leading?: ReactNode; onOpenSettings?: (tab: string) => void }) {
  const [, setName] = useState("");
  const [projects, setProjects] = useState<{ name: string; groups: string[]; note?: string }[]>([]);
  const [menuOpen, setMenuOpen] = useState(false);
  const [active, setActive] = useState<{ name: string; groups: string[] } | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    app.NetDevSettings()
      .then(s => {
        setName(s?.networkName?.trim() || tt("ndv.tbar.defaultNet"));
        setProjects((s?.projects ?? []).map((p: any) => ({ ...p, groups: p.groups ?? [] })));
      })
      .catch(e => setErr(String(e)));
    const sync = () => setActive(getActiveProject());
    sync();
    return subscribeActiveProject(sync);
  }, []);
  const confirm = useConfirm();
  const stop = useCallback(async () => {
    const ok = await confirm({
      title: "SYSTEM HALT",
      message: tt("ndv.tbar.estopMsg"),
      danger: true,
      confirmLabel: tt("ndv.tbar.estopBtn"),
    });
    if (!ok) return;
    try {
      await app.NetDevEmergencyStop();
      setErr("[SYS] STOP SIGNAL SENT");
    } catch (e) {
      setErr(String(e));
    }
  }, [confirm]);
  return (
    <div className="ndv__titlebar">
      {leading}
      {projects.length > 0 && (
        <span
          className="ndv__project"
          role="button"
          title={active ? tt("ndv.tbar.projectTip", { name: active.name, groups: active.groups.join("、") }) : tt("ndv.tbar.allTip")}
          onClick={() => setMenuOpen(o => !o)}
        >{tt("ndv.tbar.project", { name: active ? active.name : tt("ndv.tbar.all") })} ▾</span>
      )}
      {menuOpen && (
        <span className="ndv__project-menu" role="menu">
          <span role="menuitem" onClick={() => { setActiveProject(null); setMenuOpen(false); }}>{tt("ndv.tbar.allDevices")}</span>
          {projects.map(p => (
            <span key={p.name} role="menuitem" title={p.note || tt("ndv.tbar.groups", { groups: p.groups.join("、") })}
              onClick={() => { setActiveProject({ name: p.name, groups: p.groups }); setMenuOpen(false); }}>
              {p.name}{active?.name === p.name ? " ✓" : ""}
            </span>
          ))}
          <span role="menuitem" onClick={() => { setMenuOpen(false); onOpenSettings?.("netdev"); }}>{tt("ndv.tbar.manageProjects")}</span>
        </span>
      )}

      {err && <span className="ndv__stat" style={{ color: "var(--err)" }}>{err}</span>}
      <span className="ndv__stop" role="button" onClick={() => void stop()}>{"⏹ "}{tt("ndv.tbar.estop")}</span>
    </div>
  );
}

// Sparkline — 24h 迷你趋势图（§5.3）：时序面数据的设备卡呈现。无数据不渲染
// （渐进披露），有则一条 60x16 的折线。
function Sparkline({ points, bad }: { points: { t: number; v: number }[]; bad?: boolean }) {
  if (points.length < 2) return null;
  const vs = points.map(p => p.v);
  const min = Math.min(...vs), max = Math.max(...vs);
  const span = max - min || 1;
  const w = 60, h = 16;
  const d = points.map((p, i) => {
    const x = (i / (points.length - 1)) * w;
    const y = h - ((p.v - min) / span) * h;
    return `${i === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`;
  }).join(" ");
  return (
    <svg width={w} height={h} style={{ verticalAlign: "middle", marginLeft: 6 }} aria-hidden>
      <path d={d} fill="none" stroke={bad ? "var(--danger)" : "var(--ok)"} strokeWidth="1.3" />
    </svg>
  );
}

// Severity colors ride the THEME TOKENS (accent/warn/danger) — never literal
// hexes, so every theme reads consistently (user direction: 配色跟随全局).
const SEV_COLOR: Record<string, string> = { info: "var(--accent)", warning: "var(--warn)", critical: "var(--danger)" };

// Audit table helpers — same class vocabulary as the live panel.
const AUDIT_CLASS_KEYS: Record<string, string> = {
  read: "ndv.cls.read", write: "ndv.cls.write", dangerous: "ndv.cls.dangerous", unknown: "ndv.cls.unknown",
  guardrail: "ndv.cls.guardrail", assess: "ndv.cls.assess", proposal: "ndv.acls.proposal",
  "proposal-write": "ndv.acls.proposalWrite", "proposal-rollback": "ndv.acls.proposalRb",
  job: "ndv.acls.job", cutover: "ndv.acls.cutover", oob: "ndv.acls.oob",
};

function auditClassLabel(cls: string): string {
  const k = AUDIT_CLASS_KEYS[cls];
  return k ? tt(k as never) : cls;
}
function classColorForAudit(cls: string): string {
  if (cls === "read" || cls === "proposal") return "var(--accent)";
  if (cls === "guardrail") return "var(--danger)";
  return "var(--warn)";
}

const QUICK_BATTERY: Record<string, string[]> = {
  huawei: ["display version", "display cpu-usage", "display interface brief"],
  cisco: ["show version", "show processes cpu", "show interfaces status"],
  zte: ["show version", "show processor cpu", "show interface brief"],
  vmware: ["esxcli system version get", "esxcli network nic list", "esxcfg-vswitch -l", "vim-cmd vmsvc/getallvms"],
  linux: ["ip addr", "ss -tlnp", "systemctl --failed", "df -h", "cat /proc/net/dev"],
  windows: ["Get-NetAdapter", "Get-NetTCPConnection", "Get-Service", "systeminfo"],
};

// Web 服务器只读预设（NETDEV_SPEC_V2 §2.5 v1）：nginx/apache 的配置自检与
// 模块/虚拟机清单——web kind 的先头部队，全部在既有读表白名单内。
const WEB_QUICK = ["nginx -t", "nginx -T", "apache2ctl -M", "apache2ctl -S"];

// kind=docker / kind=k8s 设备卡快捷（NETDEV_SPEC_V2 §2.2/§2.3 + §10.5 矩阵）。
const DOCKER_QUICK: { what: string; label: string }[] = [
  { what: "ping", label: "ndv.dq.ping" },
  { what: "ps", label: "ndv.dq.ps" },
  { what: "images", label: "ndv.dq.images" },
  { what: "info", label: "ndv.dq.info" },
];
const K8S_QUICK: { what: string; label: string }[] = [
  { what: "version", label: "ndv.kq.version" },
  { what: "nodes", label: "ndv.kq.nodes" },
  { what: "pods", label: "Pods" },
  { what: "deployments", label: "Deployments" },
  { what: "events", label: "ndv.kq.events" },
];
const FW_QUICK: { what: string; label: string }[] = [
  { what: "status", label: "ndv.fq.status" },
  { what: "resource", label: "ndv.fq.resource" },
  { what: "interfaces", label: "ndv.fq.interfaces" },
  { what: "conns", label: "ndv.fq.conns" },
  { what: "policies", label: "ndv.fq.policies" },
  { what: "routes", label: "ndv.fq.routes" },
];

// SNMP quick queries for the device card (vendor=snmp): label → (oid, mode).
const SNMP_QUICK: { label: string; oid: string; mode: string }[] = [
  { label: "ndv.sq.sysDesc", oid: "1.3.6.1.2.1.1.1.0", mode: "get" },
  { label: "ndv.sq.uptime", oid: "1.3.6.1.2.1.1.3.0", mode: "get" },
  { label: "ndv.sq.ifStatus", oid: "1.3.6.1.2.1.2.2.1.8", mode: "walk" },
  { label: "ndv.sq.ifTraffic", oid: "1.3.6.1.2.1.31.1.1.1.6", mode: "walk" },
];

// Read-only "grab the running config" command per vendor — the first step of
// any 配置 task (backup / diff / proposal drafting).
const CFG_CMD: Record<string, string> = {
  huawei: "display current-configuration",
  cisco: "show running-config",
  zte: "show running-config",
  vmware: "esxcli system version get",
};

// BMC quick queries for the device card (vendor=redfish): label → resource.
const REDFISH_QUICK: { label: string; path: string }[] = [
  { label: "ndv.rq.managers", path: "/redfish/v1/Managers" },
  { label: "ndv.rq.systems", path: "/redfish/v1/Systems" },
  { label: "ndv.rq.chassis", path: "/redfish/v1/Chassis" },
  { label: "ndv.rq.sel", path: "/redfish/v1/Managers/1/LogServices" },
];

type QuickResult = { command: string; output: string; isError: boolean; refused?: string; refusedUnknown?: boolean };
// ?bench=<workbench> deep-links the main-area workbench (mirror of ?dock=).
function benchParam(): "logs" | "sec" | "dash" | "browser" | null {
  try {
    const v = new URLSearchParams(window.location.search).get("bench");
    return v === "logs" || v === "sec" || v === "dash" ? v : null;
  } catch { return null; }
}
// ?screen=<dash-screen> + ?finding=<id> deep-link the dash shell (§4.4 入口 4).
function dashScreenParam(): DashScreen | null {
  try {
    const v = new URLSearchParams(window.location.search).get("screen");
    return (["overview", "chain", "cutover", "discovery", "exposure"] as const).includes(v as DashScreen) ? v as DashScreen : null;
  } catch { return null; }
}

type DockTab = "overview" | "live" | "devices" | "context" | "topology" | "findings" | "proposals" | "audit" | "state" | "logs" | "health" | "jobs" | "browser";

// C2.1 结果卡（completion-spec §3.1）：全网动作的成功/警告回执——中性样式，
// 可关闭，可一键跳转对应页签。setErr 只留给真实错误。
type OpsResult = {
  title: string;
  tone: "ok" | "warn";
  rows: { label: string; text: string }[];
  warn?: string;
  jump?: { label: string; tab: DockTab };
  at: number;
};
// Fresh installs open a curated set — 操作实况 leads (supervision first), the
// rest join via the "+" dropdown or the bottom-nav entries on demand. Stored
// state always wins after the user customizes.
const DOCK_TAB_DEFAULT_OPEN: readonly DockTab[] = ["overview", "live", "health", "findings"];
// DASHBOARD spec §4.2：总览是默认落地页签；老安装的存量页签集里没有它，
// 一次性补种（同 live-seeded 先例——种完之后用户的增删仍是权威）。
const NETDEV_DOCK_TABS_OVW_SEEDED = "hiq.netdevDockTabs.ovw-seeded";
const NETDEV_DOCK_TABS_KEY = "hiq.netdevDockTabs";
const NETDEV_DOCK_TABS_SEEDED = "hiq.netdevDockTabs.seeded";
const NETDEV_DOCK_TABS_LIVE_SEEDED = "hiq.netdevDockTabs.live-seeded";

// 深链过滤匹配器（大屏/总览 onJump 的 filter 语义）：
//   severity:<sev> | id:<id> | device:<name> | assess | baseline | syslog
// 未识别的串退化为标题/来源子串匹配——宁可多匹配也不空列表。
export function findingMatchesJump(f: NetDevFinding, filter: string): boolean {
  const q = (filter ?? "").trim();
  if (!q) return true;
  const src = (f as { source?: string }).source ?? "";
  if (q.startsWith("severity:")) return f.severity === q.slice(9);
  if (q.startsWith("id:")) return f.id === q.slice(3);
  if (q.startsWith("device:")) return (f.devices ?? []).includes(q.slice(7));
  if (q === "assess") return src.startsWith("assess") || /弱口令|weak/i.test(f.title);
  if (q === "baseline") return f.title.startsWith("基线");
  if (q === "syslog") return src.startsWith("syslog");
  return f.title.includes(q) || src.includes(q) || (f.detail ?? "").includes(q);
}

export function proposalMatchesJump(p: NetDevProposal, filter: string): boolean {
  const q = (filter ?? "").trim();
  if (!q) return true;
  if (q.startsWith("id:")) return p.id === q.slice(3);
  if (q.startsWith("device:")) return (p.steps ?? []).some(st => st.device === q.slice(7));
  return p.id.includes(q) || (p.intent ?? "").includes(q);
}

// ?dock=<tab> (browser dev mock only — same affordance as bridge.ts's
// ?profile=) boots the dock with a given tab focused (e.g. ?dock=live,
// ?dock=audit) so panels are screenshot-testable without driving the tab
// strip first. ?live=1 stays as a ?dock=live alias. The open-tabs correction
// effect OPENs this tab instead of correcting away from it.
const DOCK_PARAM_KEYS: readonly string[] = ["overview", "live", "devices", "context", "topology", "findings", "proposals", "audit", "state", "logs", "health", "jobs", "browser"];
function dockParam(): DockTab | null {
  try {
    if (typeof window !== "undefined" && !window.runtime) {
      const q = new URLSearchParams(window.location.search);
      const v = q.get("dock") ?? (q.get("live") === "1" ? "live" : "");
      if (DOCK_PARAM_KEYS.includes(v)) {
        // 退役页签归一化（context/jobs 已并入 devices/live）
        if (v === "context") return "devices";
        if (v === "jobs") return "live";
        return v as DockTab;
      }
    }
  } catch { /* not a browser */ }
  return null;
}

export function NetDevLayout({
  mainNode,
  footerNode,
  bannersNode,
  terminalNode,
  sessionsNode,
  searchNode,
  projectTreeNode,
  onOpenSettings,
  onInsertComposer,
  onToggleSidebar,
  onSwitchMode,

  sidebarToggleTitle,
  sidebarCollapsed = false,
  sidebarWidth,
  sidebarMinWidth,
  sidebarMaxWidth,
  onSidebarResizeStart,
  onSidebarResizeKey,
  onSidebarResetWidth,
  dockOpen = true,
  dockOnClose,
  onDockOpen,
  dockWidth,
  dockMinWidth,
  dockMaxAriaWidth,
  onDockResizeStart,
  onDockResizeKey,
  onDockResetWidth,
}: {
  mainNode: ReactNode;
  footerNode: ReactNode;
  // Global banners (startup error / update notice) — rendered at the top of
  // 运维 main so they stay visible here too (the coding chat pane that
  // used to host them is display:none under .app--netdev).
  bannersNode?: ReactNode;
  // Terminal console (Ctrl+`) — App builds the panel node once and lends it to
  // the mode layout, so the chrome's terminal toggle works in 运维 mode too.
  terminalNode?: ReactNode;
  sessionsNode: ReactNode;
  // Sidebar search + project tree — the same nodes the coding/office sidebars
  // render, so 运维左侧导航 carries the full grammar (search → 最近会话 →
  // 项目工作区) on top of its device list.
  searchNode?: ReactNode;
  projectTreeNode?: ReactNode;
  onOpenSettings: (tab: string) => void;
  // Fills the chat composer with a starter prompt (the AI-配置 entry point on
  // the device card / topology nodes). Optional: card hides the button if absent.
  onInsertComposer?: (text: string) => void;
  // New diagnostic session — mirrors the coding view's brand-row ghost button.
  onNewSession?: () => void;
  // Sidebar collapse + resize — same affordances as the coding sidebar.
  onToggleSidebar?: () => void;
  onSwitchMode?: (mode: "dev" | "cowork" | "netdev") => void;

  onPickProject?: (root: string) => void;
  onAddProject?: () => void;
  sidebarToggleTitle?: string;
  sidebarCollapsed?: boolean;
  sidebarWidth?: number;
  sidebarMinWidth?: number;
  sidebarMaxWidth?: number;
  onSidebarResizeStart?: (event: ReactPointerEvent<HTMLButtonElement>) => void;
  onSidebarResizeKey?: (event: ReactKeyboardEvent<HTMLButtonElement>) => void;
  onSidebarResetWidth?: () => void;
  // Right dock open/close + width resize — the coding workbench-dock's exact
  // affordances (chrome workspace toggle, drag resizer, drag-to-edge close).
  // dockOnClose also fires when the last strip tab is closed.
  dockOpen?: boolean;
  dockOnClose?: () => void;
  // Re-opens a closed dock (the bottom-left 设备清单 nav entry) — App owns the
  // open state.
  onDockOpen?: () => void;
  dockWidth?: number;
  dockMinWidth?: number;
  dockMaxAriaWidth?: number;
  onDockResizeStart?: (event: ReactPointerEvent<HTMLButtonElement>) => void;
  onDockResizeKey?: (event: ReactKeyboardEvent<HTMLButtonElement>) => void;
  onDockResetWidth?: () => void;
}) {
  const t = useT();
  const [settings, setSettings] = useState<NetDevSettingsView | null>(null);
  const [findings, setFindings] = useState<NetDevFinding[]>([]);
  const [proposals, setProposals] = useState<NetDevProposal[]>([]);
  const [audit, setAudit] = useState<NetDevAuditEntryView[]>([]);
  // 健康快照联动：设备页卡/拓扑/健康 tab 共用一份 live 数据；
  // hotTabs 是右栏 tab 的活动点信号（实况有流、健康有变更、发现有新条）。
  const [healthMap, setHealthMap] = useState<Record<string, NetDevDeviceHealth>>({});

  const healthDownCount = Object.values(healthMap).filter(h => !h.reachable || (h.interfaces ?? []).some(i => i.adminUp && !i.operUp)).length;
  const [selected, setSelected] = useState<string>("");
  const [quick, setQuick] = useState<Record<string, QuickResult>>({});
  const [topo, setTopo] = useState<NetDevTopologyGraph | null>(null);
  const [topoBusy, setTopoBusy] = useState(false);
  const [topoNotice, setTopoNotice] = useState("");
  const [inspBusy, setInspBusy] = useState(false);
  // ?dock=<tab> boots the dock with a given tab focused (see dockParam at
  // module scope); the open-tabs correction effect OPENs that tab instead of
  // correcting away from it.
  // §4.10：/ 快捷键聚焦当前页签首个搜索框。
  const dockBodyRef = useRef<HTMLDivElement>(null);
  const [tab, setTab] = useState<DockTab>(() => dockParam() ?? "overview");
  const [hotTabs, setHotTabs] = useState<Partial<Record<DockTab, boolean>>>({});
  const [alertWizardOpen, setAlertWizardOpen] = useState(false);
  const markHot = useCallback((k: DockTab) => setHotTabs(prev => ({ ...prev, [k]: true })), []);
  // Visiting a tab clears its activity dot.
  useEffect(() => { setHotTabs(prev => (prev[tab] ? { ...prev, [tab]: false } : prev)); }, [tab]);
  useEffect(() => {
    let alive = true;
    app.NetDevHealthSnapshot().then(snap => {
      if (!alive) return;
      const m: Record<string, NetDevDeviceHealth> = {};
      for (const d of snap?.devices ?? []) m[d.device] = d;
      setHealthMap(m);
    }).catch(() => {});
    const off1 = onNetdevHealth(h => {
      setHealthMap(prev => ({ ...prev, [h.device]: h }));
      markHot("health");
    });
    const off2 = onNetdevLive(() => markHot("live"));
    return () => { alive = false; off1(); off2(); };
  }, [markHot]);
  // Browser-style dock tabs (coding workbench-dock pattern): only OPEN tabs
  // show, each closable, "+" re-opens from the catalog, order persists.
  // Seed the curated default ONCE (localStorage flag); afterwards the user's
  // own open/close set is authoritative. A second one-time flag back-fills
  // the 实况 tab for installs that seeded BEFORE it existed — the stored set
  // wins afterwards, so closing it again sticks.
  const [openTabs, setOpenTabs] = useDockTabState(NETDEV_DOCK_TABS_KEY, (() => {
    try {
      if (!localStorage.getItem(NETDEV_DOCK_TABS_SEEDED)) {
        localStorage.setItem(NETDEV_DOCK_TABS_SEEDED, "1");
        localStorage.setItem(NETDEV_DOCK_TABS_KEY, JSON.stringify(DOCK_TAB_DEFAULT_OPEN));
      } else if (!localStorage.getItem(NETDEV_DOCK_TABS_LIVE_SEEDED)) {
        localStorage.setItem(NETDEV_DOCK_TABS_LIVE_SEEDED, "1");
        const stored: string[] = JSON.parse(localStorage.getItem(NETDEV_DOCK_TABS_KEY) || "[]");
        if (Array.isArray(stored) && !stored.includes("live")) {
          localStorage.setItem(NETDEV_DOCK_TABS_KEY, JSON.stringify(["live", ...stored]));
        }
      }
      if (localStorage.getItem(NETDEV_DOCK_TABS_SEEDED) && !localStorage.getItem(NETDEV_DOCK_TABS_OVW_SEEDED)) {
        localStorage.setItem(NETDEV_DOCK_TABS_OVW_SEEDED, "1");
        const stored2: string[] = JSON.parse(localStorage.getItem(NETDEV_DOCK_TABS_KEY) || "[]");
        if (Array.isArray(stored2) && !stored2.includes("overview") && stored2.length) {
          localStorage.setItem(NETDEV_DOCK_TABS_KEY, JSON.stringify(["overview", ...stored2]));
        }
      }
    } catch { /* storage unavailable */ }
    return DOCK_TAB_DEFAULT_OPEN;
  })());
  const [err, setErr] = useState(""); // shown in the global title bar

  // Main-area workbenches (NETDEV_SPEC_V2 §10.2): 对话 is the default view and
  // the return anchor; the 日志 workbench opens on demand and STAYS MOUNTED
  // once opened (closing = switching back to chat, state survives — §10.2).
  // The switch bar renders only while a workbench is the ACTIVE view (bench
  // !== "chat"), so the chat view stays pixel-equal to v1.1 even after a
  // workbench was opened (§10.8: 纯对话零新增 chrome) — dock/sidebar jumps
  // that deep-link into a bench must not grow permanent chrome on the chat.
  const [bench, setBench] = useState<"chat" | "logs" | "sec" | "dash" | "browser">(() => (benchParam() === "sec" ? "sec" : benchParam() === "dash" ? "dash" : benchParam() === "browser" ? "browser" : benchParam() ? "logs" : "chat"));
  const [logsBenchEverOpened, setLogsBenchEverOpened] = useState(() => benchParam() === "logs");
  const [secBenchEverOpened, setSecBenchEverOpened] = useState(() => benchParam() === "sec");
  const [dashBenchEverOpened, setDashBenchEverOpened] = useState(() => benchParam() === "dash");
  const [browserBenchEverOpened, setBrowserBenchEverOpened] = useState(() => benchParam() === "browser");
  const openBrowserBench = useCallback(() => {
    setBench("browser");
    setBrowserBenchEverOpened(true);
  }, []);
  // 大屏（DASHBOARD spec §4.1）：初始屏/深链 finding；manualSignal 驱动
  // Alt+1..5、命令面板、hiq://finding 深链的后到切换。
  const [dashScreen, setDashScreen] = useState<DashScreen | null>(() => dashScreenParam());
  const [dashFinding, setDashFinding] = useState<string>("");
  const [dashManual, setDashManual] = useState<{ screen: DashScreen; finding?: string } | null>(null);
  // 深链过滤（DASHBOARD spec §12 偏差 1 的兑现）：大屏/总览跳转携带的
  // filter（severity:critical / id:x / device:x / assess / baseline）落到
  // 目标面板，可一键清除。仅 findings/proposals 有外部过滤入口。
  const [dashJumpFilter, setDashJumpFilter] = useState<{ tab: string; filter: string } | null>(null);
  // 页签充实（同域补内容）：audit 画像 / topology 对账 / devices 统计行。
  const [auditProfile, setAuditProfile] = useState<NetDevOverviewSnapshot | null>(null);
  const [topoRec, setTopoRec] = useState<NetDevTopoReconcile | null>(null);
  const [devStats, setDevStats] = useState<{ roles: Record<string, number>; polled: number; managed: number; lastBackupAt: string } | null>(null);
  const openDockTabFnRef = useRef<((k: DockTab) => void) | null>(null);
  // 割接视图（§7.2）：对话主区里的任务过程视图（不开第四个工作台）。
  // null = 关闭；"" = 创建表单；其余 = 正在查看的 run id。
  const [cutoverId, setCutoverId] = useState<string | null>(null);
  const [cutovers, setCutovers] = useState<NetDevCutoverRun[]>([]);
  // 状态历史页签徽标（可回退事件数）：轻量独立轮询——面板自身 2.5s 刷新
  // 详情，这里只喂页签角标（5s 足够，出错静默为 0）。
  const [stateRestorable, setStateRestorable] = useState(0);
  useEffect(() => {
    let alive = true;
    const load = () => {
      app.NetDevStateEvents()
        .then((evs) => { if (alive) setStateRestorable((evs ?? []).filter((e) => e.canRestore).length); })
        .catch(() => { /* backend away → keep the last count */ });
    };
    load();
    const t = setInterval(load, 5000);
    return () => { alive = false; clearInterval(t); };
  }, []);





  const openLogsBench = useCallback(() => {
    setLogsBenchEverOpened(true);
    setBench("logs");
  }, []);

  const openSecBench = useCallback(() => {
    setSecBenchEverOpened(true);
    setBench("sec");
  }, []);

  const openDashBench = useCallback((screen?: DashScreen, finding?: string) => {
    setDashBenchEverOpened(true);
    if (screen) {
      setDashManual({ screen, finding });
      setDashScreen(screen);
      if (finding) setDashFinding(finding);
    }
    setBench("dash");
  }, []);

  // 命令面板入口（§10.7 第 5 层）：palette 在 App 层，经自定义事件抵达——
  // 与 ?bench= 深链同效，不做状态提升。
  useEffect(() => {
    const onBench = (e: Event) => {
      const d = (e as CustomEvent<string>).detail;
      if (d === "logs") openLogsBench();
      if (d === "sec") openSecBench();
      if (d === "dash") openDashBench();
      if (d === "browser") openBrowserBench();
    };
    const onOpenScreen = (e: Event) => {
      const d = (e as CustomEvent<{ screen?: DashScreen; finding?: string; tab?: DockTab; filter?: string }>).detail;
      if (d?.screen) {
        openDashBench(d.screen, d.finding);
        return;
      }
      // 页签型路由（hiq://proposal/<id>）：提案中心定位 + 深链过滤。
      if (d?.tab) {
        const tabKey = (d.tab === "jobs" ? "live" : d.tab) as DockTab;
        setDashJumpFilter(d.filter ? { tab: tabKey, filter: d.filter } : null);
        onDockOpen?.();
        openDockTabFnRef.current?.(tabKey);
      }
    };
    window.addEventListener("hiq:netdev-bench", onBench);
    window.addEventListener("hiq:netdev-open-screen", onOpenScreen);
    return () => {
      window.removeEventListener("hiq:netdev-bench", onBench);
      window.removeEventListener("hiq:netdev-open-screen", onOpenScreen);
    };
  }, [openLogsBench, openSecBench, openDashBench, openBrowserBench]);

  // §4.12 深链路由：notify 推送消息里的 hiq://finding/<id> 链接在
  // webview 里没有协议处理器——这里拦截点击，落调查链屏并高亮（把 v1 的
  // 断头路接通；协议级系统注册留待 Wails 侧一次性接线）。
  useEffect(() => {
    const onClick = (e: MouseEvent) => {
      const a = (e.target as HTMLElement | null)?.closest?.("a");
      const href = a?.getAttribute("href") ?? "";
      const m = href.match(/^hiq:\/\/finding\/([\w-]+)/);
      if (!m) return;
      e.preventDefault();
      openDashBench("chain", m[1]);
    };
    document.addEventListener("click", onClick);
    return () => document.removeEventListener("click", onClick);
  }, [openDashBench]);

  // §4.3 统一设备聚焦：五屏里任何设备点击走同一条路（开设备页签），
  // select 变体再把设备名填进搜索框定位到该设备（结果归位）。
  useEffect(() => {
    const onFocus = (e: Event) => {
      const d = (e as CustomEvent<string>).detail;
      if (!d) return;
      onDockOpen?.();
      openDockTabFnRef.current?.("devices");
    };
    const onSelect = (e: Event) => {
      const d = (e as CustomEvent<string>).detail;
      if (!d) return;
      setLocateTarget(d);
    };
    window.addEventListener("hiq:netdev-device-focus", onFocus);
    window.addEventListener("hiq:netdev-device-select", onSelect);
    return () => {
      window.removeEventListener("hiq:netdev-device-focus", onFocus);
      window.removeEventListener("hiq:netdev-device-select", onSelect);
    };
  }, [onDockOpen]);

  const reload = useCallback(async () => {
    try {
      const [s, f, p, a, cs] = await Promise.all([
        app.NetDevSettings(),
        app.NetDevFindings(),
        app.NetDevProposals(),
        app.NetDevAuditTail(200),
        app.NetDevCutovers().catch(() => [] as NetDevCutoverRun[]),
      ]);
      setSettings(s);
      setFindings(f ?? []);
      setProposals(p ?? []);
      setAudit(a ?? []);
      setCutovers(cs ?? []);
      setErr("");
      setReloadTick(t => t + 1);
    } catch (e) {
      setErr(String(e));
    }
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // §4.10 快捷键最小集：Esc 层级退出（原有）+ r 刷新当前页签 + /
      // 聚焦当前面板的搜索框。r 和 / 仅在无输入焦点时生效——不然会把
      // 用户正在敲的命令字符吞掉。
      if (e.key !== "Escape") {
        const inField = e.target instanceof HTMLElement && (
          e.target.tagName === "INPUT" || e.target.tagName === "TEXTAREA" || e.target.isContentEditable
        );
        if (!inField && (e.key === "r" || e.key === "R") && !e.ctrlKey && !e.metaKey && !e.altKey) {
          e.preventDefault();
          void reload();
          return;
        }
        // §4.4 入口 5：o = 大屏 toggle（总览落点）；Alt+1..5 五屏直切。
        if (!inField && (e.key === "o" || e.key === "O") && !e.ctrlKey && !e.metaKey && !e.altKey) {
          e.preventDefault();
          if (bench === "dash") setBench("chat");
          else openDashBench(dashScreen ?? "overview");
          return;
        }
        if (e.altKey && !e.ctrlKey && !e.metaKey && /^[1-5]$/.test(e.key)) {
          e.preventDefault();
          const sc: DashScreen = (["overview", "chain", "cutover", "discovery", "exposure"] as const)[Number(e.key) - 1];
          openDashBench(sc);
          return;
        }
        if (!inField && e.key === "/") {
          const box = dockBodyRef.current?.querySelector<HTMLInputElement>(
            'input[type="text"], input:not([type])'
          );
          if (box) {
            e.preventDefault();
            box.focus();
          }
        }
        return;
      }
      if (bench !== "chat") {
        setBench("chat");
        return;
      }
      if (cutoverId !== null) setCutoverId(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [bench, cutoverId, reload, openDashBench, dashScreen]);

  useEffect(() => {
    void reload();
    const t = setInterval(() => void reload(), 30_000);
    return () => clearInterval(t);
  }, [reload]);

  // 页签充实数据：进哪个页签拉哪份（NetDevOverview 有 30s 缓存，重复调用便宜）。
  useEffect(() => {
    if (tab !== "audit" && tab !== "devices") return;
    let alive = true;
    app.NetDevOverview(false).then(sp => { if (alive && sp) setAuditProfile(sp); }).catch(() => {});
    return () => { alive = false; };
  }, [tab]);

  useEffect(() => {
    if (tab !== "topology") return;
    let alive = true;
    app.NetDevTopoReconcile().then(r => { if (alive && r) setTopoRec(r); }).catch(() => {});
    return () => { alive = false; };
  }, [tab]);

  useEffect(() => {
    if (tab !== "devices") return;
    let alive = true;
    Promise.all([
      app.NetDevOverview(false).then(sp => sp ? { roles: sp.stats.device_by_role ?? {}, polled: sp.health.polled, managed: sp.coverage.managed } : null),
      app.NetDevBackups("").then(vs => {
        const latest: Record<string, string> = {};
        for (const v of vs ?? []) { if (!latest[v.device] || v.at > latest[v.device]) latest[v.device] = v.at; }
        const times = Object.values(latest).sort();
        return times.length ? times[times.length - 1] : "";
      }).catch(() => ""),
    ]).then(([ov, lastBackup]) => {
      if (alive && ov) setDevStats({ roles: ov.roles, polled: ov.polled, managed: ov.managed, lastBackupAt: lastBackup });
    }).catch(() => {});
    return () => { alive = false; };
  }, [tab]);

  // C3.10 快捷键最小集：r 刷新（输入焦点时不抢键）、/ 聚焦页内首个搜索框。
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable)) return;
      if (e.ctrlKey || e.metaKey || e.altKey) return;
      if (e.key === "r") { void reload(); }
      else if (e.key === "/") {
        const el = document.querySelector<HTMLElement>(".ndv__dock-body:not([style*='none']) input.mem-input");
        if (el) { e.preventDefault(); el.focus(); }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [reload]);

  // 聚合视图随 reload 刷新（告警队列，§4.10）——reload 闭包内拉取，避免声明序依赖。
  useEffect(() => {
    app.NetDevAggregatedFindings().then(list => setAggs(list ?? [])).catch(() => {});
  }, [findings]);

  const [reloadTick, setReloadTick] = useState(0);

  const runQuick = useCallback(async (device: string, command: string) => {
    try {
      const r = await app.NetDevQuickExec(device, command);
      setQuick(q => ({ ...q, [command]: {
        command,
        output: r.refused ? (r.refusal ?? tt("ndv.logp.refused")) : r.output,
        isError: !!r.refused || r.is_error,
        refused: r.refused ? "1" : undefined,
        refusedUnknown: !!r.refused && r.class === "unknown",
      } }));
    } catch (e) {
      setQuick(q => ({ ...q, [command]: { command, output: String(e), isError: true } }));
    }
  }, []);

  // SNMP panel: quick allowlisted queries land in the same result area.
  const runSnmp = useCallback(async (device: string, label: string, oid: string, mode: string) => {
    try {
      const out = await app.NetDevSnmpQuery(device, oid, mode);
      setQuick(q => ({ ...q, [label]: { command: `SNMP ${mode} ${oid}`, output: out, isError: false } }));
    } catch (e) {
      setQuick(q => ({ ...q, [label]: { command: `SNMP ${mode} ${oid}`, output: String(e), isError: true } }));
    }
  }, []);

  // BMC panel: quick Redfish GETs land in the same result area as CLI runs.
  const runRedfish = useCallback(async (device: string, label: string, path: string) => {
    try {
      const out = await app.NetDevRedfishQuery(device, path);
      setQuick(q => ({ ...q, [label]: { command: `GET ${path}`, output: out, isError: false } }));
    } catch (e) {
      setQuick(q => ({ ...q, [label]: { command: `GET ${path}`, output: String(e), isError: true } }));
    }
  }, []);

  // One-click knowledge growth: an unknown-but-plausibly-read command refused
  // by the classifier gets a chip that teaches the read table — no TOML
  // editing, no model self-declaration (B-1: only the user extends the table).
  const teachRead = useCallback(async (vendor: string, command: string, device: string) => {
    try {
      await app.NetDevAddExtraRead(vendor, command);
      await runQuick(device, command);
    } catch (e) {
      setErr(String(e));
    }
  }, [runQuick]);

  const genTopology = useCallback(async () => {
    setTopoBusy(true);
    try {
      const g = await app.NetDevTopologySnapshot();
      if (g) {
        setTopo({ ...g, nodes: g.nodes ?? [], edges: g.edges ?? [] });
        setTopoSource("measured");
      }
    } catch (e) {
      setErr(String(e));
    } finally {
      setTopoBusy(false);
    }
  }, []);

  // Topology node names come from LLDP sysnames, which need not equal the
  // configured device names — match by name first, then by management IP.
  const devicesRef = useRef<NetDevSettingsView["devices"]>([]);
  devicesRef.current = settings?.devices ?? [];
  const pickFromTopo = useCallback((name: string) => {
    const node = topo?.nodes.find(v => v.name === name);
    const hit = devicesRef.current.find(d => d.name === name || (node?.device_ip && d.address === node.device_ip));
    if (hit) {
      setSelected(hit.name);
      setTab("devices");
      setOpenTabs((prev) => (prev.includes("devices") ? prev : [...prev, "devices"]));
      setTopoNotice("");
      return;
    }
    setTopoNotice(tt("ndv.topo.unknownNode", { name }));
  }, [topo]);

  // Rendering doctrine for the map: click-to-render, program-computed, IP
  // plan first. Entering the 拓扑 tab loads the LOCAL view instantly (pure
  // computation over the inventory — zero device sessions, zero model calls);
  // the measured LLDP/CDP sweep only runs on the explicit 校准 click.
  const [topoSource, setTopoSource] = useState<"plan" | "measured" | "design">("plan");
  const loadPlan = useCallback(async () => {
    try {
      const g = await app.NetDevTopologyPlan();
      if (g) {
        setTopo(g);
        setTopoSource("plan");
      }
    } catch (e) {
      setErr(String(e));
    }
  }, []);
  const loadDesign = useCallback(async () => {
    try {
      const d = await app.NetDevTopologyDesign();
      if (d && d.graph?.nodes?.length) {
        setTopo({ ...d.graph, nodes: d.graph.nodes ?? [], edges: d.graph.edges ?? [] });
        setTopoSource("design");
      }
    } catch (e) {
      setErr(String(e));
    }
  }, []);
  // T2a: 设计稿导入 — file text → Preview (no side effects) → human confirms
  // → Apply writes the design source. The staged preview keeps the graph
  // until Apply consumes it.
  const [topoPreview, setTopoPreview] = useState<NetDevTopoImportPreview | null>(null);
  const [topoImportName, setTopoImportName] = useState("");
  const importTopoFile = useCallback(async (file: File) => {
    setTopoImportName(file.name);
    try {
      let pv: NetDevTopoImportPreview | null = null;
      if (/\.vsdxx?$/i.test(file.name) || file.name.toLowerCase().endsWith(".vsdx")) {
        // Binary OOXML package: bytes → base64 → Go-side unzip.
        const buf = new Uint8Array(await file.arrayBuffer());
        let bin = "";
        for (let i = 0; i < buf.length; i += 0x8000) {
          bin += String.fromCharCode(...buf.subarray(i, i + 0x8000));
        }
        pv = await app.NetDevImportVsdxPreview(btoa(bin));
      } else {
        pv = await app.NetDevImportTopoPreview(await file.text());
      }
      if (!pv) throw new Error("parse returned nothing");
      setTopoPreview(pv);
    } catch (e) {
      setErr(String(e));
      setTopoPreview(null);
    }
  }, []);
  // F5: attack-path SIMULATION — pure data-plane (zero connections), the
  // result card is clearly watermarked 推演.
  const [attackReport, setAttackReport] = useState<NetDevAttackPathReport | null>(null);
  const [attackBusy, setAttackBusy] = useState(false);
  const runAttackPaths = useCallback(async () => {
    setAttackBusy(true);
    try {
      setAttackReport(await app.NetDevAttackPaths());
    } catch (e) {
      setErr(String(e));
    } finally {
      setAttackBusy(false);
    }
  }, []);

  // T2 entry A: chat attachment chips dispatch parsed drawio previews here —
  // same preview card as the topology tab's import button, no new surface.
  useEffect(() => {
    const onTopoImport = (e: Event) => {
      const d = (e as CustomEvent<{ name: string; preview: NetDevTopoImportPreview }>).detail;
      if (!d?.preview) return;
      setTopoImportName(d.name || "attachment.drawio");
      setTopoPreview(d.preview);
      setTab("topology");
      setOpenTabs(prev => (prev.includes("topology") ? prev : [...prev, "topology"]));
    };
    window.addEventListener("hiq:netdev-topo-import", onTopoImport);
    return () => window.removeEventListener("hiq:netdev-topo-import", onTopoImport);
  }, []);

  const applyTopoImport = useCallback(async () => {
    if (!topoPreview) return;
    try {
      await app.NetDevImportTopoApply(topoImportName || "design.drawio", {
        ...topoPreview.graph,
        nodes: topoPreview.graph.nodes ?? [],
        edges: topoPreview.graph.edges ?? [],
      });
      setTopoPreview(null);
      await loadDesign();
    } catch (e) {
      setErr(String(e));
    }
  }, [topoPreview, topoImportName, loadDesign]);
  const topoAutoFired = useRef(false);
  useEffect(() => {
    if (tab === "topology" && !topoAutoFired.current) {
      topoAutoFired.current = true;
      void loadPlan();
    }
  }, [tab, loadPlan]);

  const runInspection = useCallback(async () => {
    setInspBusy(true);
    try {
      const f = await app.NetDevRunInspection();
      if (f) setErr(`[SYS] INSPECTION COMPLETE: ${f.title}`);
      await reload();
    } catch (e) {
      setErr(String(e));
    } finally {
      setInspBusy(false);
    }
  }, [reload]);

  // Security posture: sealed config reads + local rule battery → Findings.
  const [baseBusy, setBaseBusy] = useState(false);
  const runBaseline = useCallback(async () => {
    setBaseBusy(true);
    try {
      const f = await app.NetDevRunBaseline();
      if (f) setErr(`[SYS] BASELINE COMPLETE: ${f.title}`);
      await reload();
    } catch (e) {
      setErr(String(e));
    } finally {
      setBaseBusy(false);
    }
  }, [reload]);

  // 巡检家族（NETDEV_SPEC_V2 §10.6）：一个家四件事——网络巡检 / 主机体检 /
  // 基线核查 / 弱口令核查（弱口令后端早已就绪，这里补 UI 接线）。
  const [inspMenuOpen, setInspMenuOpen] = useState(false);
  const [triageBusy, setTriageBusy] = useState(false);
  const [weakBusy, setWeakBusy] = useState(false);
  const [triageOneBusy, setTriageOneBusy] = useState("");
  // §6.2/§10.5 文件下载对话框：设备卡「文件」打开的瞬时对话框（无常驻面板）。
  const [filePicker, setFilePicker] = useState("");
  const [filePath, setFilePath] = useState("/var/log");
  const [fileListing, setFileListing] = useState<string[] | null>(null);
  const [fileBusy, setFileBusy] = useState("");
  const [fileNote, setFileNote] = useState("");
  const [cardSeries, setCardSeries] = useState<Record<string, { t: number; v: number }[]>>({});

  // 设备卡打开时拉 24h 时序（有数据才显示 sparkline，§10.5 渐进披露）。
  useEffect(() => {
    if (!selected) { setCardSeries({}); return; }
    let alive = true;
    app.NetDevSeries(selected, 24).then(m => {
      if (!alive) return;
      const out: Record<string, { t: number; v: number }[]> = {};
      for (const [metric, pts] of Object.entries(m ?? {})) out[metric] = (pts ?? []).map(p => ({ t: p.t, v: p.v }));
      setCardSeries(out);
    }).catch(() => {});
    return () => { alive = false; };
  }, [selected, reloadTick]);
  const [locateTarget, setLocateTarget] = useState("");
  const [expBusy, setExpBusy] = useState(false);
  // C4.1 发现（密封 TCP 探测的 GUI 触发）与 C4.6 回放查看。
  const [discoverOpen, setDiscoverOpen] = useState(false);
  const [discoverCidr, setDiscoverCidr] = useState("");
  const [discoverBusy, setDiscoverBusy] = useState(false);
  // F1 待确认区: leads loaded on the devices tab; names editable per row
  // before promotion (the human names their assets, never the scanner).
  const [discovered, setDiscovered] = useState<NetDevDiscoveredHost[]>([]);
  const [discSel, setDiscSel] = useState<Set<string>>(new Set());
  const [discNames, setDiscNames] = useState<Record<string, string>>({});
  const [discBusy, setDiscBusy] = useState(false);
  // 自定义端口列表（F1 §4.2.5）：空 = 后端默认 22/23/161/443/830。
  const [discoverPorts, setDiscoverPorts] = useState("");
  // F4 多层发现：vantage 选择 + 预检计划卡（确认后才探测）。
  const [discVantage, setDiscVantage] = useState("");
  const [discPlan, setDiscPlan] = useState<NetDevDiscoverPlan | null>(null);
  const [discPlanSel, setDiscPlanSel] = useState<Set<string>>(new Set());
  const [discPlanBusy, setDiscPlanBusy] = useState(false);
  // F4 断点续扫：暂停中的运行（打开弹框时加载，提供「继续」入口）。
  const [discPausedRun, setDiscPausedRun] = useState<NetDevDiscoveryRunState | null>(null);
  const [recOpen, setRecOpen] = useState<string | null>(null); // device name
  const [recList, setRecList] = useState<{ device: string; path: string; at: string; bytes: number }[]>([]);
  const [recText, setRecText] = useState("");
  const [recPath, setRecPath] = useState("");
  const [handoffMd, setHandoffMd] = useState("");
  const [importPreview, setImportPreview] = useState<import("../lib/types").NetDevImportPreview | null>(null);
  const [aggView, setAggView] = useState(true); // 聚合视图开关（§4.10）
  // 清空发现的二次确认臂：第一次点击进入武装态（按钮变成确认文案），再点执行。
  const [findingsClearArm, setFindingsClearArm] = useState(false);
  const [aggs, setAggs] = useState<NetDevAggregatedFinding[]>([]);
  const [handoffBusy, setHandoffBusy] = useState(false);
  const [locateBusy, setLocateBusy] = useState(false);
  // C2.1 结果卡：全网动作的成功回执走这里（中性/成功样式），setErr 只留给
  // 真实错误——成功结果不再进红色 banner（completion-spec §3.1）。
  const [opsResult, setOpsResult] = useState<OpsResult | null>(null);

  // 「这个 IP 接在哪」——清单内 ARP 扇出定位（§4.11）。
  const runLocate = useCallback(async () => {
    const t = locateTarget.trim();
    if (!t) return;
    setLocateBusy(true);
    try {
      const r = await app.NetDevLocate(t);
      setOpsResult({
        title: tt("ndv.res.locate", { ip: t }),
        tone: r.hits.length > 0 ? "ok" : "warn",
        rows: [
          { label: tt("ndv.res.covered"), text: tt("ndv.res.coveredText", { a: r.covered_devices, b: r.total_devices }) },
          { label: tt("ndv.res.hits"), text: r.hits.length > 0 ? r.hits.map(h => `${h.device}${h.interface ? ":" + h.interface : ""}`).join("、") : tt("ndv.res.none") },
        ],
        warn: r.budget_stopped ? tt("ndv.res.budgetStopped") : undefined,
        at: Date.now(),
      });
    } catch (e) {
      setErr(String(e));
    } finally {
      setLocateBusy(false);
    }
  }, [locateTarget]);

  // 期望状态对比（§5.4）：清单声明 vs 健康采集——「掉电恢复后缺的 13 台是谁」。
  const runExpected = useCallback(async () => {
    setExpBusy(true);
    try {
      const v = await app.NetDevExpectedState();
      const miss = (v.missing ?? []).map(m => m.device);
      setOpsResult({
        title: tt("ndv.res.expected"),
        tone: miss.length > 0 ? "warn" : "ok",
        rows: [
          { label: tt("ndv.res.probe"), text: tt("ndv.res.probeText", { a: v.total, b: v.reachable }) },
          { label: tt("ndv.res.missing"), text: miss.length > 0 ? miss.join("、") : tt("ndv.res.allPresent") },
        ],
        warn: (v.noProbe ?? []).length ? tt("ndv.res.noProbe", { n: v.noProbe.length }) : undefined,
        jump: miss.length > 0 ? { label: tt("ndv.res.viewHealth"), tab: "health" } : undefined,
        at: Date.now(),
      });
    } catch (e) {
      setErr(String(e));
    } finally {
      setExpBusy(false);
    }
  }, []);

  // F1 待确认区: load on devices-tab visits and after scans/promotions.
  const loadDiscovered = useCallback(async () => {
    setDiscBusy(true);
    try {
      const list = await app.NetDevDiscoveredHosts();
      setDiscovered(list ?? []);
    } catch {
      setDiscovered([]);
    } finally {
      setDiscBusy(false);
    }
  }, []);
  useEffect(() => { if (tab === "devices") void loadDiscovered(); }, [tab, loadDiscovered]);

  // F4: 预检（零探测流量）→ 计划卡（勾选确认）→ 从 vantage 隧道探测。
  const runPrecheck = useCallback(async () => {
    if (!discVantage) return;
    setDiscPlanBusy(true);
    try {
      const plan = await app.NetDevDiscoverPrecheck(discVantage);
      setDiscPlan(plan);
      setDiscPlanSel(new Set((plan?.steps ?? []).filter(s => s.default_on).map(s => s.cidr)));
    } catch (e) {
      setErr(String(e));
      setDiscPlan(null);
    } finally {
      setDiscPlanBusy(false);
    }
  }, [discVantage]);
  const resumeLayerDiscover = useCallback(async () => {
    setDiscoverBusy(true);
    try {
      const hosts = await app.NetDevDiscoverResume();
      const alive = hosts ?? [];
      setDiscoverOpen(false);
      setDiscPausedRun(null);
      setOpsResult({
        title: tt("ndv.res.discoverResume", { id: "" }),
        tone: alive.length > 0 ? "ok" : "warn",
        rows: [
          { label: tt("ndv.res.result"), text: alive.length > 0 ? tt("ndv.res.alive", { n: alive.length }) : tt("ndv.res.noAlive") },
          ...alive.slice(0, 8).map(h => ({ label: h.ip, text: (h.open ?? []).map(o => `${o.port}${o.banner ? ` (${o.banner.slice(0, 30)})` : ""}`).join(" ") || "?" })),
        ],
        warn: tt("ndv.res.discoverWarn") + " " + tt("ndv.disc.zoneSaved"),
        at: Date.now(),
      });
      void loadDiscovered();
    } catch (e) {
      setErr(String(e));
    } finally {
      setDiscoverBusy(false);
    }
  }, [loadDiscovered]);

  const runLayerDiscover = useCallback(async () => {
    const cidrs = Array.from(discPlanSel);
    if (cidrs.length === 0) return;
    setDiscoverBusy(true);
    try {
      const hosts = await app.NetDevDiscoverLayer(discVantage, cidrs, parsePortList(discoverPorts));
      const alive = hosts ?? [];
      setDiscoverOpen(false);
      setDiscPlan(null);
      setOpsResult({
        title: tt("ndv.res.discoverLayer", { v: discVantage, n: cidrs.length }),
        tone: alive.length > 0 ? "ok" : "warn",
        rows: [
          { label: tt("ndv.res.result"), text: alive.length > 0 ? tt("ndv.res.alive", { n: alive.length }) : tt("ndv.res.noAlive") },
          ...alive.slice(0, 8).map(h => ({ label: h.ip, text: (h.open ?? []).map(o => `${o.port}${o.banner ? ` (${o.banner.slice(0, 30)})` : ""}`).join(" ") || "?" })),
        ],
        warn: tt("ndv.res.discoverWarn") + " " + tt("ndv.disc.zoneSaved"),
        at: Date.now(),
      });
      void loadDiscovered();
    } catch (e) {
      setErr(String(e));
    } finally {
      setDiscoverBusy(false);
    }
  }, [discVantage, discPlanSel, discoverPorts, loadDiscovered]);

  // vendor_hint → driver key for promotion prefill; anything else lands as
  // "" (the device form's vendor picker stays the source of truth).
  const VENDOR_DRIVER: Record<string, string> = { huawei: "huawei-vrp", cisco: "cisco-ios", zte: "zte-zxr10" };
  const promoteDiscovered = useCallback(async () => {
    const rows = discovered.filter(h => discSel.has(h.ip));
    if (rows.length === 0) return;
    try {
      await app.NetDevPromoteHosts(rows.map(h => ({
        ip: h.ip,
        name: (discNames[h.ip] ?? h.hostname ?? h.ip).trim() || h.ip,
        vendor: VENDOR_DRIVER[h.vendor_hint ?? ""] ?? "",
        role: h.role_hint ?? "",
      })));
      setDiscSel(new Set());
      await loadDiscovered();
      await reload();
    } catch (e) { setErr(String(e)); }
  }, [discovered, discSel, discNames, loadDiscovered, reload]);
  const dismissDiscovered = useCallback(async (ip: string) => {
    try {
      await app.NetDevDeleteDiscoveredHost(ip);
      await loadDiscovered();
    } catch (e) { setErr(String(e)); }
  }, [loadDiscovered]);

  // C4.1：密封 TCP 发现——与 agent netdev_discover 同路径/同白名单/同审计。
  const runDiscover = useCallback(async () => {
    const c = discoverCidr.trim();
    if (!c) return;
    setDiscoverBusy(true);
    try {
      const hosts = await app.NetDevDiscover(c, "", parsePortList(discoverPorts));
      const alive = hosts ?? [];
      setDiscoverOpen(false);
      setOpsResult({
        title: tt("ndv.res.discover", { cidr: c }),
        tone: alive.length > 0 ? "ok" : "warn",
        rows: [
          { label: tt("ndv.res.result"), text: alive.length > 0 ? tt("ndv.res.alive", { n: alive.length }) : tt("ndv.res.noAlive") },
          ...alive.slice(0, 8).map(h => ({ label: h.ip, text: (h.open ?? []).map(o => `${o.port}${o.banner ? ` (${o.banner.slice(0, 30)})` : ""}`).join(" ") || "?" })),
        ],
        warn: tt("ndv.res.discoverWarn") + " " + tt("ndv.disc.zoneSaved"),
        at: Date.now(),
      });
      await reload();
      void loadDiscovered();
    } catch (e) {
      setErr(String(e));
    } finally {
      setDiscoverBusy(false);
    }
  }, [discoverCidr, discoverPorts, reload, loadDiscovered]);

  // C4.6 #5：终端会话回放查看（录制文件落盘时已脱敏）。
  const openRecordings = useCallback(async (device: string) => {
    setRecOpen(device); setRecText(""); setRecPath("");
    try {
      const all = await app.NetDevHumanTTYRecordings();
      setRecList((all ?? []).filter(r => !device || r.device === device).slice(0, 20));
    } catch (e) { setErr(String(e)); setRecList([]); }
  }, []);
  const readRecording = useCallback(async (path: string) => {
    setRecPath(path);
    try { setRecText(await app.NetDevHumanTTYRecordingRead(path)); } catch (e) { setRecText(String(e)); }
  }, []);

  // kind=docker / kind=k8s 的设备卡快捷：结果落进与 CLI 快捷同一个展示区。
  const runApiQuick = useCallback(async (label: string, run: () => Promise<string>) => {
    try {
      const out = await run();
      setQuick(q => ({ ...q, [label]: { command: label, output: out, isError: false } }));
    } catch (e) {
      setQuick(q => ({ ...q, [label]: { command: label, output: String(e), isError: true } }));
    }
  }, []);

  // 设备卡的「一键体检」：单台跑电池，摘要直接回报，异常进「发现」。
  const runTriageOne = useCallback(async (device: string) => {
    setTriageOneBusy(device);
    try {
      const rep = await app.NetDevTriageRun(device);
      const anoms = rep.anomalies ?? [];
      setOpsResult({
        title: tt("ndv.res.triage", { dev: device }),
        tone: anoms.length > 0 ? "warn" : "ok",
        rows: [
          { label: tt("ndv.res.summary"), text: rep.summary },
          ...(anoms.length > 0 ? [{ label: tt("ndv.res.anomalies"), text: anoms.join("；") }] : []),
        ],
        jump: anoms.length > 0 ? { label: tt("ndv.res.viewFindings"), tab: "findings" } : undefined,
        at: Date.now(),
      });
      await reload();
    } catch (e) {
      setErr(String(e));
    } finally {
      setTriageOneBusy("");
    }
  }, [reload]);

  const runTriageAll = useCallback(async () => {
    const hosts = (settings?.devices ?? []).filter(d => d.vendor === "linux" || d.vendor === "windows");
    if (hosts.length === 0) { setErr(tt("ndv.res.noHosts")); return; }
    setTriageBusy(true);
    let anomalies = 0;
    try {
      for (const h of hosts) {
        const rep = await app.NetDevTriageRun(h.name);
        anomalies += (rep.anomalies ?? []).length;
      }
      setOpsResult({
        title: tt("ndv.res.triageAll"),
        tone: anomalies > 0 ? "warn" : "ok",
        rows: [{ label: tt("ndv.res.result"), text: tt("ndv.res.triageAllText", { a: hosts.length, b: anomalies }) }],
        jump: anomalies > 0 ? { label: tt("ndv.res.viewFindings"), tab: "findings" } : undefined,
        at: Date.now(),
      });
      await reload();
    } catch (e) {
      setErr(String(e));
    } finally {
      setTriageBusy(false);
    }
  }, [settings, reload]);

  const runWeakCredAll = useCallback(async (tier: "basic" | "strong" = "basic") => {
    const targets = (settings?.devices ?? []).filter(d => d.vendor !== "snmp");
    if (targets.length === 0) { setErr(tt("ndv.res.noTargets")); return; }
    if (tier === "strong" && !(settings?.weakCredDict ?? "").trim()) { setErr(tt("ndv.res.needDict")); return; }
    setWeakBusy(true);
    const weak: string[] = [];
    let failed = 0;
    try {
      for (const d of targets) {
        try {
          const r = await app.NetDevWeakCredCheck(d.name, tier);
          if (r.weak) weak.push(d.name);
        } catch {
          failed++; // 无凭证/不可达的设备计入未完成，不中断全队
        }
      }
      setOpsResult({
        title: tt("ndv.res.weakDone"),
        tone: weak.length > 0 ? "warn" : "ok",
        rows: [
          { label: tt("ndv.res.hits"), text: tt("ndv.res.weakText", { a: weak.length, b: targets.length, list: weak.length ? "：" + weak.join("、") : "" }) },
          ...(failed ? [{ label: tt("ndv.res.unfinished"), text: tt("ndv.res.unfinishedText", { n: failed }) }] : []),
        ],
        jump: weak.length > 0 ? { label: tt("ndv.res.viewFindings"), tab: "findings" } : undefined,
        at: Date.now(),
      });
      await reload();
    } finally {
      setWeakBusy(false);
    }
  }, [settings, reload]);


  // Active project scope (site switcher in the title bar): null = 全部.
  const [project, setProject] = useState<NetDevProjectScope>(getActiveProject());
  useEffect(() => subscribeActiveProject(() => setProject(getActiveProject())), []);
  // 启动恢复：定义（settings.projects）到位后按名字找回上次的选择；会话内
  // 已手选则不打扰；项目已删除则清掉残留。
  const projectRestoredRef = useRef(false);
  const settingsProjects = settings?.projects ?? [];
  useEffect(() => {
    if (projectRestoredRef.current || settingsProjects.length === 0) return;
    projectRestoredRef.current = true;
    restoreActiveProject(settingsProjects);
  }, [settingsProjects]);

  const allDevices = settings?.devices ?? [];
  const inScope = useCallback((group: string | undefined) => {
    if (!project || project.groups.length === 0) return true;
    return project.groups.includes(group?.trim() || tt("ndv.misc.ungrouped"));
  }, [project]);
  const devices = allDevices.filter(d => inScope(d.group));
  const scopedDeviceNames = new Set(devices.map(d => d.name));
  const selectedDevice = devices.find(d => d.name === selected);
  // 项目过滤走 Finding 上的快照标签（后端 save 时固化，不随改组漂移）——不再
  // 按设备隶属实时推导。未打标（未分组/未知来源/旧数据）在所有项目视图可见，
  // 盲区规则见后端 finding.go 的 Project 注释。
  const scopedFindings = findings.filter(f => !project || !f.project || f.project === project.name);
  // 深链过滤层：scopedFindings 之上再套大屏带来的 filter（可清除）。
  const jumpFilteredFindings = (() => {
    if (!dashJumpFilter || dashJumpFilter.tab !== "findings") return scopedFindings;
    return scopedFindings.filter(f => findingMatchesJump(f, dashJumpFilter.filter));
  })();
  const scopedProposals = proposals.filter(p => !project || (p.steps ?? []).some(s => scopedDeviceNames.has(s.device)));
  // §4.1 列表治理：状态筛选 + 加载更多（替代原 slice(0,10) 硬顶）。
  const PROPOSAL_PAGE_SIZE = 10;
  const PROPOSAL_FILTERS = [
    { key: "pending", label: tt("ndv.pf.pending"), match: (s: string) => s === "draft" || s === "approved" || s === "partial" },
    { key: "all", label: tt("ndv.pf.all"), match: (_: string) => true },
    { key: "draft", label: tt("ndv.pf.draft"), match: (s: string) => s === "draft" },
    { key: "approved", label: tt("ndv.pf.approved"), match: (s: string) => s === "approved" },
    { key: "rejected", label: tt("ndv.pf.rejected"), match: (s: string) => s === "rejected" },
    { key: "closed", label: tt("ndv.pf.closed"), match: (s: string) => s === "done" || s === "closed" || s === "failed" || s === "watching" },
  ] as const;
  const [proposalFilter, setProposalFilter] = useState<string>("pending");
  const [proposalPage, setProposalPage] = useState(1);
  const filteredProposals = scopedProposals.filter(p =>
    (PROPOSAL_FILTERS.find(f => f.key === proposalFilter) ?? PROPOSAL_FILTERS[1]).match(p.status ?? "") &&
    (!dashJumpFilter || dashJumpFilter.tab !== "proposals" || proposalMatchesJump(p, dashJumpFilter.filter)));
  const lastInspection = scopedFindings.find(f => f.title.startsWith(tt("ndv.misc.inspPrefix")));
  const pendingCount = scopedProposals.filter(p => p.status === "draft" || p.status === "approved" || p.status === "partial").length;

  const groups = new Map<string, { name: string; address: string; vendor: string; kind: string }[]>();
  for (const d of devices) {
    const g = d.group?.trim() || tt("ndv.misc.ungrouped");
    if (!groups.has(g)) groups.set(g, []);
    groups.get(g)!.push({ name: d.name, address: d.address, vendor: d.vendor, kind: d.kind ?? "" });
  }

  const today = new Date().toISOString().slice(5, 10);
  const todayAudit = audit.filter(a => (a.time ?? "").slice(5, 10) === today);
  const readCount = todayAudit.filter(a => a.class === "read").length;
  const writeCount = todayAudit.filter(a => a.class === "write" || a.class === "proposal-write").length;

  // Topology filtered to the active project: managed nodes must be in scope;
  // unmanaged neighbors survive only while an in-scope edge touches them.
  const scopedTopo = useMemo(() => {
    if (!project || !topo) return topo;
    const nameIn = (n: string) => scopedDeviceNames.has(n);
    const nodes = topo.nodes ?? [];
    const edges = (topo.edges ?? []).filter(e => nameIn(e.local_device) || nameIn(e.remote_device));
    const touched = new Set<string>(edges.flatMap(e => [e.local_device, e.remote_device]));
    return { ...topo, nodes: nodes.filter(n => nameIn(n.name) || (!n.managed && touched.has(n.name))), edges };
  }, [project, topo, scopedDeviceNames]);

  const findingsCountRef = useRef(0);
  useEffect(() => {
    if (scopedFindings.length > findingsCountRef.current && findingsCountRef.current > 0) markHot("findings");
    findingsCountRef.current = scopedFindings.length;
  }, [scopedFindings, markHot]);
  const liveHot = !!hotTabs.live;
  const healthHot = !!hotTabs.health;
  const findingsHot = !!hotTabs.findings;
  // 大屏/总览的风险色点（§4.4 活动信号）：出现未闭环 critical 即亮。
  const openCritical = scopedFindings.filter(f => f.severity === "critical" && f.status !== "resolved").length;
  const riskHot = openCritical > 0;
  const riskDotColor = openCritical > 0 ? "#e5484d" : (scopedFindings.filter(f => f.severity === "warning" && f.status !== "resolved").length > 0 ? "#f5a524" : "transparent");
  // Tab order = the operator's workflow (正在发生 → 什么状态 → 需要我决策
  // → 留档备查); the "+" catalog renders the same grouping as headers.
  const TABS: { key: DockTab; label: string; badge?: number; dot?: boolean; group: string; icon: React.ReactNode }[] = [
    { key: "overview", label: tt("ndv.tab.overview"), group: tt("ndv.tabgrp.now"), dot: riskHot, icon: <LayoutDashboard size={13} /> },
    { key: "live", label: tt("ndv.tab.live"), group: tt("ndv.tabgrp.now"), dot: liveHot, icon: <Activity size={13} /> },
    { key: "logs", label: tt("ndv.tab.logs"), group: tt("ndv.tabgrp.now"), icon: <FileText size={13} /> },
    { key: "health", label: tt("ndv.tab.health"), group: tt("ndv.tabgrp.state"), dot: healthHot, badge: healthDownCount || undefined, icon: <HeartPulse size={13} /> },
    { key: "devices", label: tt("ndv.tab.devices"), group: tt("ndv.tabgrp.state"), badge: devices.length || undefined, icon: <Server size={13} /> },
    { key: "topology", label: tt("ndv.tab.topology"), group: tt("ndv.tabgrp.state"), icon: <Network size={13} /> },
    { key: "browser", label: tt("ndv.tab.browser"), group: tt("ndv.tabgrp.state"), icon: <MousePointerClick size={13} /> },
    { key: "findings", label: tt("ndv.tab.findings"), group: tt("ndv.tabgrp.decide"), dot: findingsHot, badge: scopedFindings.length || undefined, icon: <AlertTriangle size={13} /> },
    { key: "proposals", label: tt("ndv.tab.proposals"), group: tt("ndv.tabgrp.decide"), badge: pendingCount || undefined, icon: <ClipboardCheck size={13} /> },
    { key: "audit", label: tt("ndv.tab.audit"), group: tt("ndv.tabgrp.archive"), icon: <ScrollText size={13} /> },
    { key: "state", label: tt("ndv.tab.state"), group: tt("ndv.tabgrp.archive"), badge: stateRestorable || undefined, icon: <History size={13} /> },
  ];

  // Active tab closed (or restored state desyncs) → fall back to the last
  // open one — the coding dock's dockTabs effect. The ?dock= deep link is the
  // one initializer allowed to OPEN a not-yet-open tab instead of correcting
  // away from it (dev-mock smoke entry).
  useEffect(() => {
    if (!openTabs.includes(tab)) {
      if (tab === dockParam()) {
        setOpenTabs((prev) => (prev.includes(tab) ? prev : [...prev, tab]));
        return;
      }
      setTab(openTabs[openTabs.length - 1] ?? "overview");
    }
  }, [openTabs, tab]);

  const closeDockTabFn = (key: DockTab) => {
    setOpenTabs((prev) => {
      const next = prev.filter((m) => m !== key);
      if (next.length === 0) {
        // Closing the last tab closes the whole dock (coding-dock behavior).
        dockOnClose?.();
        return prev;
      }
      setTab((active) => (active === key ? next[next.length - 1] : active));
      return next;
    });
  };

  // Strip click / "+" catalog pick: ensure the tab is open and focus it.
  const openDockTabFn = (key: DockTab) => {
    setOpenTabs((prev) => (prev.includes(key) ? prev : [...prev, key]));
    setTab(key);
  };
  openDockTabFnRef.current = openDockTabFn;

  return (
    <div className="ndv">
      {/* Rail width resizer — same class/handlers as the coding sidebar's
          (the .app--netdev rule hides the app-level copy; this one re-shows). */}
      {!sidebarCollapsed && onSidebarResizeStart && (
        <button
          className="sidebar-resizer"
          type="button"
          role="separator"
          aria-orientation="vertical"
          aria-label={tt("ndv.misc.dragSide")}
          aria-valuemin={sidebarMinWidth}
          aria-valuemax={sidebarMaxWidth}
          aria-valuenow={sidebarWidth}
          onPointerDown={onSidebarResizeStart}
          onKeyDown={onSidebarResizeKey}
          onDoubleClick={onSidebarResetWidth}
        />
      )}
      <div className="ndv__rail">
        {/* Brand row — identical classes/markup to the coding view's
            sidebar__brandrow: logo + Hiq + new-session ghost button +
            collapse toggle. Spans the chrome row (top-left of the window). */}
        <div className="sidebar__brandrow" title={tt("ndv.tbar.mode")}>
          {/* Logo retired; collapse toggle leads, mode name follows (2026-08-19). */}
          {onToggleSidebar && (
            <button
              type="button"
              className="app-chrome__panel-toggle app-chrome__panel-toggle--left sidebar__brand-toggle sidebar__brand-toggle--lead"
              onClick={onToggleSidebar}
              aria-label={sidebarToggleTitle}
              aria-pressed={!sidebarCollapsed}
            >
              <div className="brand-toggle-group">
                <img src={logoSymbol} alt="" className="brand-toggle-logo" draggable={false} />
                <PanelLeft size={16} className="brand-toggle-icon" />
              </div>
            </button>
          )}
          <ProfileSegmented
            profile="netdev"
            onSwitchProfile={onSwitchMode || (() => {})}
            t={t}
          />

        </div>
        {/* Scroll lives BELOW the pinned brand row (the coding sidebar's
            structure: root never scrolls, brand row stays put). */}
        <div className="ndv__rail-scroll">
          {searchNode}
          {sessionsNode}
          {/* 项目工作区 — the same ProjectTree node the coding/office sidebars
              render (profile="netdev" partition: 运维 profile has its own project index,
              empty until the user adds projects). Same left-nav grammar as the
              coding view: search → 最近会话 → 项目工作区. */}
          {projectTreeNode && (
            <section className="sidebar__section sidebar__section--projects" style={{ marginBottom: '8px', minHeight: 0, display: 'flex', flexDirection: 'column' }}>
              {projectTreeNode}
            </section>
          )}
        {/* 运维专属导航 — the pinned bottom-left group, mirroring the
            coding/office sidebars' bottom nav. §4.5 扩到 8 项；设备清单在
            右侧 dock，巡检是直接动作，偏好开设置，其余直达各视图。 */}
        </div>
        <section className="cowork-sidebar__group" style={{ marginBottom: '0px', marginTop: 'auto' }}>
          {/* §4.5 八项目录：看状态（大屏/设备/拓扑）→ 做动作（巡检）→ 处队列
              （安全/提案）→ 查档案（审计）→ 调配置（偏好）。徽章三型：
              计数 > 风险色点 > hot dot，视觉重量递减防告警疲劳。 */}
          <button
            className={`cowork-sidebar__item ${bench === "dash" ? "cowork-sidebar__item--active" : ""}`}
            onClick={() => openDashBench()}
            title={tt("ndv.dash.chipTip")}
          >
            <LayoutDashboard size={14} />
            <span>{tt("ndv.dash.title")}</span>
            <i className="ndv-bench__riskdot" style={{ background: riskDotColor }} />
          </button>
          <button
            className={`cowork-sidebar__item ${dockOpen && tab === "devices" ? "cowork-sidebar__item--active" : ""}`}
            onClick={() => {
              onDockOpen?.();
              openDockTabFn("devices");
            }}
          >
            <Server size={14} />
            <span>{tt("ndv.dev.titlePlain")}</span>
          </button>
          <button
            className={`cowork-sidebar__item ${dockOpen && tab === "topology" ? "cowork-sidebar__item--active" : ""}`}
            onClick={() => { onDockOpen?.(); openDockTabFn("topology"); }}
          >
            <Network size={14} />
            <span>{tt("ndv.tab.topology")}</span>
          </button>
          <div style={{ position: "relative" }}>
            <button
              className="cowork-sidebar__item"
              onClick={() => setInspMenuOpen(o => !o)}
              title={tt("ndv.insp.tip")}
            >
              <ScanSearch size={14} />
              <span>{inspBusy || triageBusy ? tt("ndv.insp.triaging") : weakBusy ? tt("ndv.insp.checking") : baseBusy ? tt("ndv.insp.baselining") : tt("ndv.insp.runNow")}</span>
            </button>
            {inspMenuOpen && (
              <span className="ndv__project-menu" role="menu" style={{ position: "absolute", left: "calc(100% + 6px)", bottom: 0, minWidth: 200, zIndex: 40 }}>
                <span role="menuitem" onClick={() => { setInspMenuOpen(false); void runInspection(); }}>{tt("ndv.insp.m1")}</span>
                <span role="menuitem" onClick={() => { setInspMenuOpen(false); void runTriageAll(); }}>{tt("ndv.insp.m2")}</span>
                <span role="menuitem" onClick={() => { setInspMenuOpen(false); void runBaseline(); }}>{tt("ndv.insp.m3")}</span>
                <span role="menuitem" onClick={() => { setInspMenuOpen(false); void runWeakCredAll("basic"); }}>{tt("ndv.insp.m4")}</span>
                <span role="menuitem" title={tt("ndv.insp.m5Tip")} onClick={() => { setInspMenuOpen(false); void runWeakCredAll("strong"); }}>{tt("ndv.insp.m5")}</span>
              </span>
            )}
          </div>
          <button
            className={`cowork-sidebar__item ${bench === "sec" ? "cowork-sidebar__item--active" : ""}`}
            onClick={() => openSecBench()}
            title={tt("ndv.nav.secTip")}
          >
            <ShieldCheck size={14} />
            <span>{tt("ndv.nav.sec")}</span>
            {findingsHot && <i className="ndv-nav__hotdot" />}
          </button>
          <button
            className={`cowork-sidebar__item ${dockOpen && tab === "proposals" ? "cowork-sidebar__item--active" : ""}`}
            onClick={() => { onDockOpen?.(); openDockTabFn("proposals"); }}
          >
            <ClipboardCheck size={14} />
            <span>{tt("ndv.nav.proposals")}</span>
            {pendingCount > 0 && <i className="ndv-nav__count">{pendingCount}</i>}
          </button>
          <button
            className={`cowork-sidebar__item ${dockOpen && tab === "audit" ? "cowork-sidebar__item--active" : ""}`}
            onClick={() => { onDockOpen?.(); openDockTabFn("audit"); }}
          >
            <ScrollText size={14} />
            <span>{tt("ndv.tbar.audit", { n: todayAudit.length })}</span>
          </button>
          <button
            className="cowork-sidebar__item"
            onClick={() => onOpenSettings("netdev")}
          >
            <SlidersHorizontal size={14} />
            <span>{tt("ndv.tbar.prefs")}</span>
          </button>
        </section>
      </div>

      <div className="ndv__main">
        {bannersNode}
        {/* §10.2「只开对话时切换条完全不渲染」：bar 只在非对话工作台为当前
            视图时出现，回到对话即隐去——对话主区无论开过什么都与 v1.1 像素
            等价；工作台本体保持挂载，重进现场不丢。 */}
        {bench !== "chat" && (
          <div className="ndv-bench__bar" role="tablist" aria-label={tt("ndv.bench.aria")}>
            {/* bar 只在非对话视图渲染，故「对话」chip 永非当前项，仅作返回入口 */}
            <span role="tab" aria-selected={false} className="ndv-bench__chip" onClick={() => setBench("chat")}>{tt("ndv.bench.chat")}</span>
            <span role="tab" aria-selected={bench === "logs"} className={`ndv-bench__chip${bench === "logs" ? " ndv-bench__chip--on" : ""}`} onClick={openLogsBench}>{tt("ndv.bench.logs")}</span>
            <span role="tab" aria-selected={bench === "sec"} className={`ndv-bench__chip${bench === "sec" ? " ndv-bench__chip--on" : ""}`} onClick={openSecBench}>{tt("ndv.bench.sec")}</span>
            <span role="tab" aria-selected={bench === "dash"} className={`ndv-bench__chip${bench === "dash" ? " ndv-bench__chip--on" : ""}`} onClick={() => openDashBench()} title={tt("ndv.dash.chipTip")}>
              {tt("ndv.bench.dash")}
              <i className="ndv-bench__riskdot" style={{ background: riskDotColor }} />
            </span>
            <span role="tab" aria-selected={bench === "browser"} className={`ndv-bench__chip${bench === "browser" ? " ndv-bench__chip--on" : ""}`} onClick={openBrowserBench}>{tt("ndv.bench.browser")}</span>
            <span className="ndv-bench__hint"><kbd>Esc</kbd> {tt("ndv.bench.back")}</span>
          </div>
        )}
        <div className="ndv__chat" style={bench !== "chat" ? { display: "none" } : undefined}>
          {cutoverId !== null ? (
            <CutoverView
              runId={cutoverId}
              proposals={scopedProposals}
              devices={(settings?.devices ?? []).map(d => ({ name: d.name, vendor: d.vendor }))}
              onClose={() => setCutoverId(null)}
            />
          ) : (
            mainNode
          )}
        </div>
        {logsBenchEverOpened && <LogWorkbench devices={settings?.devices ?? []} onInsertComposer={onInsertComposer} hidden={bench !== "logs"} />}
        {secBenchEverOpened && <SecWorkbench devices={settings?.devices ?? []} hidden={bench !== "sec"} />}
        {browserBenchEverOpened && <BrowserWorkbench hidden={bench !== "browser"} onClose={() => setBench("chat")} />}
        {dashBenchEverOpened && (
          <div className="ndv__dashwrap" style={bench !== "dash" ? { display: "none" } : undefined}>
            <DashShell
              initialScreen={dashScreen ?? undefined}
              initialFinding={dashFinding || undefined}
              manualSignal={dashManual}
              rightRailCollapsed={!dockOpen}
              onToggleRightRail={() => dockOnClose?.()}
              onClose={() => setBench("chat")}
              onJump={(j: OverviewJump) => {
                const key = j.tab as DockTab | "chat" | "sec";
                if (key === "chat") { setBench("chat"); return; }
                if (key === "sec") { openSecBench(); return; }
                const target = key === "jobs" ? "live" : key;
                setDashJumpFilter(j.filter ? { tab: target, filter: j.filter } : null);
                onDockOpen?.();
                openDockTabFn(target);
              }}
              onFocusDevice={(d: string) => {
                onDockOpen?.();
                openDockTabFn("devices");
                window.dispatchEvent(new CustomEvent("hiq:netdev-device-select", { detail: d }));
              }}
            />
          </div>
        )}
        <div style={bench !== "chat" ? { display: "none" } : { display: "contents" }}>{footerNode}</div>

        {/* C4.1 发现弹框：CIDR 须在探测白名单内（后端拨号前校验）。 */}
        {discoverOpen && (
          <div role="dialog" aria-modal="true"
            style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,.45)", display: "flex", alignItems: "center", justifyContent: "center", zIndex: 60 }}
            onClick={e => { if (e.target === e.currentTarget) setDiscoverOpen(false); }}>
            <div style={{ background: "var(--bg-elevated, #23272e)", borderRadius: 8, padding: 16, minWidth: 460, maxWidth: 560 }}>
              <div className="set-label" style={{ marginBottom: 8 }}>{tt("ndv.disc.title")}</div>
              <div className="mem-hint" style={{ marginBottom: 8 }}>
                {tt("ndv.disc.note")}
              </div>
              <input className="mem-input" style={{ width: "100%" }} autoFocus placeholder="10.30.2.0/24" value={discoverCidr}
                onChange={e => setDiscoverCidr(e.target.value)} onKeyDown={e => { if (e.key === "Enter") void runDiscover(); }} />
              <div style={{ display: "flex", justifyContent: "flex-end", marginTop: 6 }}>
                <span className="btn btn--secondary btn--small" role="button" onClick={() => { setDiscoverOpen(false); openDashBench("discovery"); }}>{tt("ndv.disc.boardBtn")}</span>
              </div>
              <input className="mem-input" style={{ width: "100%", marginTop: 6, fontSize: 12 }} placeholder={tt("ndv.disc.portsPh")}
                title={tt("ndv.disc.portsTip")} value={discoverPorts}
                onChange={e => setDiscoverPorts(e.target.value)} onKeyDown={e => { if (e.key === "Enter") void runDiscover(); }} />
              {discPausedRun && (
                <div style={{ marginTop: 8, border: "1px dashed var(--border-soft)", borderRadius: 6, padding: 8, display: "flex", gap: 8, alignItems: "center" }}>
                  <span className="ndv__meta" style={{ flex: 1 }}>
                    {tt("ndv.disc.resumeNote", { v: discPausedRun.vantage, n: (discPausedRun.cidrs ?? []).length - (discPausedRun.done_cidrs ?? []).length, found: discPausedRun.found_so_far })}
                  </span>
                  <span className="btn btn--primary btn--small" role="button" onClick={() => void resumeLayerDiscover()}>{tt("ndv.disc.resumeBtn")}</span>
                </div>
              )}
              <div style={{ marginTop: 8, display: "flex", gap: 6, alignItems: "center" }}>
                <span className="ndv__meta" style={{ flexShrink: 0 }}>{tt("ndv.disc.vantage")}</span>
                <select className="mem-input" style={{ flex: 1, fontSize: 12, padding: "2px 6px" }} value={discVantage}
                  onChange={e => { setDiscVantage(e.target.value); setDiscPlan(null); }}>
                  <option value="">{tt("ndv.disc.vantageDirect")}</option>
                  {(settings?.devices ?? []).filter(d => !d.kind).map(d => (
                    <option key={d.name} value={d.name}>{d.name} · {d.vendor}</option>
                  ))}
                </select>
                {discVantage && (
                  <span className="btn btn--secondary btn--small" role="button" style={{ flexShrink: 0 }}
                    onClick={() => void runPrecheck()}>{discPlanBusy ? tt("ndv.disc.precheckBusy") : tt("ndv.disc.precheck")}</span>
                )}
              </div>
              {discPlan && (
                <div style={{ marginTop: 8, border: "1px dashed var(--border-soft)", borderRadius: 6, padding: 8, maxHeight: 220, overflowY: "auto" }}>
                  <div className="ndv__meta">{tt("ndv.disc.planTitle", { v: discVantage, arp: discPlan.arp_known })}</div>
                  {(discPlan.steps ?? []).map(s => (
                    <div key={s.cidr} className="ndv__device" style={{ cursor: "default", alignItems: "center", gap: 6 }}>
                      <input type="checkbox" aria-label={s.cidr}
                        checked={discPlanSel.has(s.cidr)}
                        onChange={e => setDiscPlanSel(prev => { const n = new Set(prev); if (e.target.checked) n.add(s.cidr); else n.delete(s.cidr); return n; })} />
                      <span className="ndv__device-addr">{s.cidr}</span>
                      <span className="ndv__meta" style={{ marginLeft: "auto", fontSize: 10 }}>
                        {s.class} · {s.hosts}{s.class === "large" ? tt("ndv.disc.planLarge") : ""}
                      </span>
                    </div>
                  ))}
                  {(discPlan.warnings ?? []).slice(0, 3).map((w, i) => (
                    <div key={i} className="ndv__meta" style={{ color: "var(--warn)", fontSize: 11 }}>⚠ {w}</div>
                  ))}
                </div>
              )}
              <div style={{ marginTop: 10, display: "flex", gap: 8, justifyContent: "flex-end" }}>
                <span className="btn btn--secondary btn--small" role="button" onClick={() => { setDiscoverOpen(false); setDiscPlan(null); }}>{tt("common.cancel")}</span>
                {discPlan ? (
                  <>
                    {discoverBusy && (
                      <span className="btn btn--secondary btn--small" role="button" onClick={() => { void app.NetDevDiscoverPause(); }}>{tt("ndv.disc.pauseBtn")}</span>
                    )}
                    <span className="btn btn--primary btn--small" role="button" onClick={() => void runLayerDiscover()}>
                      {discoverBusy ? tt("ndv.disc.busy") : tt("ndv.disc.startLayer", { n: discPlanSel.size })}
                    </span>
                  </>
                ) : (
                  <span className="btn btn--primary btn--small" role="button" onClick={() => void runDiscover()}>{discoverBusy ? tt("ndv.disc.busy") : tt("ndv.disc.start")}</span>
                )}
              </div>
            </div>
          </div>
        )}

        {/* C4.6 #5 终端回放：录制列表（已脱敏）→ 只读查看。 */}
        {recOpen !== null && (
          <div role="dialog" aria-modal="true"
            style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,.45)", display: "flex", alignItems: "center", justifyContent: "center", zIndex: 60 }}
            onClick={e => { if (e.target === e.currentTarget) setRecOpen(null); }}>
            <div style={{ background: "var(--bg-elevated, #23272e)", borderRadius: 8, padding: 16, minWidth: 560, maxWidth: 720, maxHeight: "80vh", overflowY: "auto" }}>
              <div className="set-label" style={{ marginBottom: 8 }}>{tt("ndv.rec.title")}{recOpen ? ` · ${recOpen}` : ""}</div>
              {recList.length === 0 && <div className="mem-hint">{tt("ndv.rec.empty")}</div>}
              {recList.map(r => (
                <div key={r.path} className="ndv__device" role="button" style={{ cursor: "pointer" }}
                  onClick={() => void readRecording(r.path)}>
                  <span className="ndv__device-name">{r.at}</span>
                  <span className="ndv__device-addr">{r.bytes} B</span>
                  {recPath === r.path && <span className="ndv__meta" style={{ marginLeft: "auto" }}>{tt("ndv.rec.viewing")}</span>}
                </div>
              ))}
              {recText && <pre className="ndv__pre" style={{ marginTop: 8, maxHeight: 300, overflow: "auto", fontSize: 11 }}>{recText}</pre>}
              <div style={{ marginTop: 10, display: "flex", justifyContent: "flex-end" }}>
                <span className="btn btn--secondary btn--small" role="button" onClick={() => setRecOpen(null)}>{tt("ndv.rec.close")}</span>
              </div>
            </div>
          </div>
        )}

        {terminalNode}
      </div>

      {/* Right dock width resizer — same class/handlers as the coding
          workbench-dock's (the .app--netdev rule hides the app-level copy;
          this one re-shows). */}
      {dockOpen && onDockResizeStart && (
        <button
          className="workspace-panel-resizer"
          type="button"
          role="separator"
          aria-orientation="vertical"
          aria-label={tt("ndv.misc.dragDock")}
          aria-valuemin={dockMinWidth}
          aria-valuemax={dockMaxAriaWidth}
          aria-valuenow={dockWidth}
          onPointerDown={onDockResizeStart}
          onKeyDown={onDockResizeKey}
          onDoubleClick={onDockResetWidth}
        />
      )}
      {dockOpen && (
      <div className="ndv__dock">
        {/* The coding workbench-dock's tab strip, verbatim (DockTabs): only
            open tabs show, each closable, "+" offers the catalog. */}
        <DockTabs
          tabs={TABS}
          openTabs={openTabs}
          active={tab}
          onSelect={openDockTabFn}
          onClose={closeDockTabFn}
          listLabel={tt("ndv.dock.list")}
          closeLabel={tt("ndv.dock.close")}
          addLabel={tt("ndv.dock.add")}
        />
        <div className="ndv__dock-body" ref={dockBodyRef}>
        {err && <div className="banner banner--error" style={{ marginBottom: 8 }}>{err}</div>}
        {opsResult && (
          <div className="ndv__card" style={{
            marginBottom: 8, padding: "8px 12px",
            borderLeft: `3px solid ${opsResult.tone === "ok" ? "var(--ok, #46a758)" : "var(--warn, #f5a524)"}`,
          }}>
            <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
              <span style={{ fontSize: 13, fontWeight: 700 }}>{opsResult.tone === "ok" ? "✓" : "⚠"} {opsResult.title}</span>
              {opsResult.jump && (
                <span className="btn btn--secondary btn--small" role="button"
                  onClick={() => { openDockTabFn(opsResult.jump!.tab); setOpsResult(null); }}>{opsResult.jump.label}</span>
              )}
              <span style={{ marginLeft: "auto", opacity: 0.4, cursor: "pointer" }} role="button"
                onClick={() => setOpsResult(null)}>×</span>
            </div>
            <div style={{ display: "flex", flexDirection: "column", gap: 2, marginTop: 4, fontSize: 11.5 }}>
              {opsResult.rows.map((r, i) => (
                <div key={i}><span style={{ opacity: 0.55, minWidth: 48, display: "inline-block" }}>{r.label}</span>{r.text}</div>
              ))}
              {opsResult.warn && <div style={{ color: "var(--warn, #f5a524)" }}>{opsResult.warn}</div>}
            </div>
          </div>
        )}

        {tab === "audit" && auditProfile && (
          <div className="ndv__card" style={{ marginBottom: 8 }}>
            <div className="ndv__card-title">{tt("ndv.auditp.title")}</div>
            <div className="ndv-ovw__statgrid">
              <span role="button" onClick={() => void reload()}>
                {tt("ndv.ovw.audit24h", { r: auditProfile.audit.read_24h, w: auditProfile.audit.write_24h, g: auditProfile.audit.guardrail_24h })}
              </span>
              <span>
                {tt("ndv.auditp.chain")}{" "}
                <b style={{ color: auditProfile.audit.chain_ok ? "var(--ok, #46a758)" : "var(--warn, #f5a524)" }}>
                  {auditProfile.audit.chain_ok ? "✓" : "⚠"} {auditProfile.audit.chain_total}
                </b>
              </span>
              <span title={tt("ndv.auditp.mixTip")}>
                {tt("ndv.auditp.mix")}{" "}
                <b>{Object.entries(auditProfile.stats.cmd_mix ?? {}).sort((a, b) => b[1] - a[1]).slice(0, 4).map(([k, v]) => `${k} ${v}`).join(" · ") || "—"}</b>
                <span className="dim"> · 30d/{auditProfile.stats.audit_entries}</span>
              </span>
              <span className="dim">{auditProfile.audit.last_entry_at ? tt("ndv.auditp.last", { at: auditProfile.audit.last_entry_at }) : ""}</span>
            </div>
          </div>
        )}

        {tab === "audit" && (
          <div style={{ marginBottom: 8 }}>
            <span className="btn btn--secondary btn--small" role="button" onClick={() => void (async () => {
              setHandoffBusy(true);
              try { setHandoffMd(await app.NetDevHandoffReport()); } catch (e) { setErr(String(e)); } finally { setHandoffBusy(false); }
            })()}>{handoffBusy ? tt("ndv.exporting") : tt("ndv.rep.handoff")}</span>
            <span className="btn btn--secondary btn--small" role="button" style={{ marginLeft: 6 }} onClick={() => void (async () => { try { setHandoffMd(await app.NetDevWeeklyReport()); } catch (e) { setErr(String(e)); } })()}>{tt("ndv.rep.weekly")}</span>
            <span className="btn btn--secondary btn--small" role="button" style={{ marginLeft: 6 }} onClick={() => void (async () => { try { setHandoffMd(await app.NetDevCredentialInventory()); } catch (e) { setErr(String(e)); } })()}>{tt("ndv.rep.creds")}</span>
            <span className="btn btn--secondary btn--small" role="button" style={{ marginLeft: 6 }} onClick={() => void (async () => {
              try {
                const p = await app.NetDevExportState();
                setErr(tt("ndv.rep.exported", { path: p }));
              } catch (e) { setErr(String(e)); }
            })()}>{tt("ndv.rep.exportBtn")}</span>
            {/* 迁移导入向导（§5.6/裁决#11）：选文件→待确认区人工核对→合并。凭证不迁。 */}
            <span className="btn btn--secondary btn--small" role="button" style={{ marginLeft: 6 }} onClick={() => {
              const el = document.createElement("input");
              el.type = "file"; el.accept = ".json";
              el.onchange = () => {
                const f = el.files?.[0];
                if (!f) return;
                void (async () => {
                  try {
                    const text = await f.text();
                    const path = await app.NetDevImportStageFile(text);
                    const pv = await app.NetDevImportPreview(path);
                    setImportPreview(pv);
                  } catch (e) { setErr(String(e)); }
                })();
              };
              el.click();
            }}>{tt("ndv.rep.importBtn")}</span>
            {handoffMd && <div className="ndv__card" style={{ marginTop: 8, maxHeight: 300, overflow: "auto" }}><Markdown text={handoffMd} /></div>}
            {importPreview && (
              <ImportWizardCard
                pv={importPreview}
                onClose={() => setImportPreview(null)}
                onApplied={(n) => {
                  setImportPreview(null);
                  setErr(tt("ndv.rep.imported", { n }));
                  void reload();
                }}
              />
            )}
          </div>
        )}

        {tab === "overview" && (
          <OverviewPanel
            compact
            actions={
              <span
                className="btn btn--secondary btn--small"
                role="button"
                title={tt("ndv.dash.chipTip")}
                onClick={() => openDashBench("overview")}
              >{tt("ndv.ovw.bigView")}</span>
            }
            onJump={(j) => {
              const key = j.tab as DockTab | "sec";
              if (key === "sec") { openSecBench(); return; }
              const target = key === "jobs" ? "live" : key;
              setDashJumpFilter(j.filter ? { tab: target, filter: j.filter } : null);
              openDockTabFn(target);
            }}
            onFocusDevice={(d) => window.dispatchEvent(new CustomEvent("hiq:netdev-device-focus", { detail: d }))}
          />
        )}

        {tab === "live" && (
          <>
            <LiveOpsPanel />
            <div style={{ marginTop: 8 }}>
              <JobsPanel />
            </div>
          </>
        )}

        {tab === "logs" && <LogPanel devices={settings?.devices ?? []} dbSources={settings?.dbSources ?? []} onInsertComposer={onInsertComposer} onOpenWorkbench={openLogsBench} onOpenSettings={onOpenSettings} />}

        {tab === "health" && <HealthPanel onOpenSettings={onOpenSettings} />}
        {tab === "browser" && <BrowserConsolePanel onInsertComposer={onInsertComposer} />}

        {tab === "devices" && (
          <div className="ndv__card">
            <div className="ndv__card-title">{tt("ndv.dev.title")}{project ? <span style={{ fontWeight: 400, fontSize: 11 }}> · {project.name}</span> : ""}（{devices.length}）</div>
            <div style={{ display: "flex", gap: 6, marginBottom: 8 }}>
              <input className="mem-input" style={{ flex: 1, minWidth: 0 }} value={locateTarget}
                onChange={e => setLocateTarget(e.target.value)} onKeyDown={e => { if (e.key === "Enter") void runLocate(); }}
                placeholder={tt("ndv.dev.phLocate")} />
              <span className="btn btn--secondary btn--small" role="button" onClick={() => void runLocate()}>{locateBusy ? tt("ndv.dev.locating") : tt("ndv.dev.locate")}</span>
              <span className="btn btn--secondary btn--small" role="button" title={tt("ndv.dev.expectedTip")} onClick={() => void runExpected()}>{expBusy ? tt("ndv.comparing") : tt("ndv.dev.expected")}</span>
              <span className="btn btn--secondary btn--small" role="button" title={tt("ndv.disc.btnTip")} onClick={() => {
                  setDiscoverCidr(""); setDiscoverOpen(true);
                  void app.NetDevDiscoveryRunState().then(r => setDiscPausedRun(r?.status === "paused" ? r : null)).catch(() => setDiscPausedRun(null));
                }}>{tt("ndv.disc.btn")}</span>
            </div>
            {lastInspection && (
              <div className="ndv__meta">{tt("ndv.dev.lastInsp", { title: lastInspection.title, at: String(lastInspection.created_at ?? "").slice(5, 16).replace("T", " ") })}</div>
            )}
            {devStats && (
              <div className="ndv__meta" style={{ display: "flex", gap: 12, flexWrap: "wrap" }}>
                <span title={tt("ndv.dev.rolesTip")}>
                  {tt("ndv.ovw.roles")} <b>{Object.entries(devStats.roles ?? {}).map(([k, v]) => `${tt(`ndv.topo.role.${k || "unknown"}` as never)} ${v}`).join(" · ") || "—"}</b>
                </span>
                <span>{tt("ndv.ovw.reachable", { ok: devStats.polled, total: devStats.managed })}</span>
                {devStats.lastBackupAt && <span className="dim">{tt("ndv.dev.lastBackup", { at: devStats.lastBackupAt })}</span>}
              </div>
            )}
            {devices.length === 0 && (
              allDevices.length === 0
                ? <GettingStarted onOpenSettings={onOpenSettings} />
                : (
                  <div className="ndv__empty">
                    <div className="ndv__empty-title">{tt("ndv.dev.emptyTitle")}</div>
                    <div className="ndv__empty-desc">tt("ndv.dev.emptyDesc")</div>
                    <span className="btn btn--primary btn--small" role="button" onClick={() => onOpenSettings("netdev")}>{tt("ndv.dev.openSettings")}</span>
                  </div>
                )
            )}
            {[...groups.entries()].map(([g, list]) => (
              <div key={g} className="ndv__group">
                <div className="ndv__group-row"><span>▸ {g}</span><span>{list.length}</span></div>
                {list.map(d => (
                  <div
                    key={d.name}
                    className={`ndv__device ndv__device--click${selected === d.name ? " ndv__device--sel" : ""}`}
                    role="button"
                    onClick={() => { setSelected(selected === d.name ? "" : d.name); setQuick({}); }}
                  >{healthMap[d.name] && (
                    <span className={`ndv__dot ${!healthMap[d.name].reachable ? "ndv__dot--down" : (healthMap[d.name].interfaces ?? []).some(i => i.adminUp && !i.operUp) ? "ndv__dot--warn" : "ndv__dot--ok"}`} title={healthMap[d.name].reachable ? tt("ndv.dev.pollUp") : tt("ndv.dev.pollDown")} />
                  )}<span className="ndv__device-name">{d.name}{d.kind ? <span style={{ opacity: 0.65, fontSize: 10, marginLeft: 4 }}>·{d.kind}</span> : ""}</span><span className="ndv__device-addr">{d.address}</span></div>
                ))}
              </div>
            ))}
            {(settings?.hops?.length ?? 0) > 0 && (
              <>
                <div className="ndv__section-row"><div className="ndv__section">{tt("ndv.dev.bastion")}</div><span className="ndv__section-meta">{settings?.hops.length}{tt("ndv.dev.hopsUnit")}</span></div>
                {(settings?.hops ?? []).map(h => (
                  <div key={h.name} className="ndv__device"><span className="ndv__device-name">{h.name}</span><span className="ndv__device-addr">{h.host}</span></div>
                ))}
              </>
            )}
            {selected && <BackupTimeline device={selected} />}
          </div>
        )}

        {tab === "devices" && (
          <div className="ndv__card" style={{ marginTop: 10 }}>
            <div className="ndv__card-title">{tt("ndv.disc.zoneTitle")}（{discovered.length}）</div>
            <div className="ndv__meta" style={{ marginBottom: 6 }}>{tt("ndv.disc.zoneNote")}</div>
            {discBusy && <div className="ndv__meta">{tt("ndv.disc.zoneBusy")}</div>}
            {!discBusy && discovered.length === 0 && <div className="ndv__meta">{tt("ndv.disc.zoneEmpty")}</div>}
            {discovered.map(h => (
              <div key={h.ip} className="ndv__device" style={{ cursor: "default", alignItems: "center", gap: 6 }}>
                <input type="checkbox" aria-label={h.ip}
                  checked={discSel.has(h.ip)}
                  onChange={e => setDiscSel(prev => { const n = new Set(prev); if (e.target.checked) n.add(h.ip); else n.delete(h.ip); return n; })} />
                <span className="ndv__device-addr" style={{ minWidth: 88 }}>{h.ip}</span>
                <input className="mem-input" style={{ width: 96, fontSize: 11, padding: "1px 6px" }}
                  value={discNames[h.ip] ?? h.hostname ?? h.ip}
                  title={tt("ndv.disc.nameTip")}
                  onChange={e => setDiscNames(prev => ({ ...prev, [h.ip]: e.target.value }))} />
                <span className="ndv__meta" style={{ marginLeft: "auto", display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
                  {h.vendor_hint && <span style={{ fontSize: 10, border: "1px solid var(--border-soft)", borderRadius: 8, padding: "0 6px", opacity: 0.8 }}>{h.vendor_hint}</span>}
                  <span title={(h.sources ?? []).join(",")}>{(h.ports ?? []).map(p => p.port).join(" ") || "?"}</span>
                  <span style={{ opacity: 0.5, cursor: "pointer" }} role="button" title={tt("ndv.disc.dismiss")}
                    onClick={() => { void dismissDiscovered(h.ip); }}>×</span>
                </span>
              </div>
            ))}
            {discovered.length > 0 && (
              <div style={{ display: "flex", gap: 8, marginTop: 8, alignItems: "center" }}>
                <span className="btn btn--secondary btn--small" role="button"
                  onClick={() => setDiscSel(discovered.every(h => discSel.has(h.ip)) ? new Set<string>() : new Set(discovered.map(h => h.ip)))}>
                  {discovered.every(h => discSel.has(h.ip)) ? tt("ndv.disc.clearSel") : tt("ndv.disc.selectAll")}
                </span>
                <span className="btn btn--primary btn--small" role="button" style={{ marginLeft: "auto" }}
                  title={tt("ndv.disc.promoteTip")}
                  onClick={() => void promoteDiscovered()}>{tt("ndv.disc.promote", { n: discSel.size })}</span>
              </div>
            )}
          </div>
        )}

        {/* 设备 360（原 context 页签并入）：列表点选就地展开，再点收起。 */}
        {tab === "devices" && selectedDevice && (
            <div className="ndv__card">
              <div className="ndv__card-title">{selectedDevice.name}
                {(cardSeries["if_down"] ?? []).length > 1 && <Sparkline points={cardSeries["if_down"]} bad />}
                {(cardSeries["reachable"] ?? []).length > 1 && <Sparkline points={cardSeries["reachable"]} />}
                <span className="ndv__card-sub">· {selectedDevice.vendor}/{selectedDevice.os} · {selectedDevice.address}{(selectedDevice.via ?? []).length ? tt("ndv.dev.viaList", { list: (selectedDevice.via ?? []).join("→") }) : ""}</span></div>
              <div className="ndv__group-label">{tt("ndv.dev.quick")}</div>
              {selectedDevice.vendor === "redfish" ? (
                <div className="ndv__quick-cmds">
                  {REDFISH_QUICK.map(q => (
                    <span key={q.label} className="btn btn--secondary btn--small" role="button" title={q.path} onClick={() => void runRedfish(selectedDevice.name, q.label, q.path)}>{tt(q.label as never)}</span>
                  ))}
                </div>
              ) : selectedDevice.vendor === "snmp" ? (
                <div className="ndv__quick-cmds">
                  {SNMP_QUICK.map(q => (
                    <span key={q.label} className="btn btn--secondary btn--small" role="button" title={`${q.mode} ${q.oid}`} onClick={() => void runSnmp(selectedDevice.name, q.label, q.oid, q.mode)}>{tt(q.label as never)}</span>
                  ))}
                </div>
              ) : (
                <div className="ndv__quick-cmds">
                  {(QUICK_BATTERY[selectedDevice.vendor] ?? ["display version"]).map(cmd => (
                    <span key={cmd} className="btn btn--secondary btn--small" role="button" onClick={() => void runQuick(selectedDevice.name, cmd)}>{cmd}</span>
                  ))}
                </div>
              )}
              {/* 带外启动器（§6.3）：只启动本地浏览器/RDP 客户端，点击入审计。 */}
              {(selectedDevice.vendor === "windows" || selectedDevice.vendor === "vmware" || selectedDevice.vendor === "redfish" || (selectedDevice.oobUrl ?? "") !== "") && (
                <>
                  <div className="ndv__group-label">{tt("ndv.dev.oob")}</div>
                  <div className="ndv__quick-cmds">
                    <span
                      className="btn btn--secondary btn--small"
                      role="button"
                      title={tt("ndv.dev.oobTip")}
                      onClick={() => void app.NetDevOOBLaunch(selectedDevice.name).then(what => setErr(tt("ndv.dev.oobLaunched", { what }))).catch(e => setErr(String(e)))}
                    >
                      {selectedDevice.vendor === "windows" ? tt("ndv.dev.oobRdp")
                        : selectedDevice.vendor === "vmware" ? "ESXi Web UI"
                        : selectedDevice.vendor === "redfish" ? tt("ndv.dev.oobBmc")
                        : tt("ndv.dev.oobWeb")}
                    </span>
                  </div>
                </>
              )}
              {(selectedDevice.vendor === "linux" || selectedDevice.vendor === "windows") && (
                <>
                  <div className="ndv__group-label">{tt("ndv.dev.triageWeb")}</div>
                  <div className="ndv__quick-cmds">
                    <span
                      className="btn btn--primary btn--small"
                      role="button"
                      title={tt("ndv.dev.triageTip")}
                      onClick={() => void runTriageOne(selectedDevice.name)}
                    >{triageOneBusy === selectedDevice.name ? tt("ndv.insp.triaging") : tt("ndv.dev.triage")}</span>
                    <span
                      className="btn btn--secondary btn--small"
                      role="button"
                      title={tt("ndv.tty.launchTip")}
                      onClick={() => {
                        // §10.5：设备卡「终端」→ 主区终端面板设备页签。App 层监听
                        // 该事件并打开面板；PTY 生命周期由 DeviceTerminal 管理。
                        window.dispatchEvent(new CustomEvent("hiq:netdev-terminal", { detail: { device: selectedDevice.name } }));
                      }}
                    >{"⌨ "}{tt("ndv.dev.terminal")}</span>
                    {(selectedDevice.protocols ?? []).includes("netconf") && (
                      <>
                        <span className="btn btn--secondary btn--small" role="button" title={tt("ndv.dev.netconfIfTip")}
                          onClick={() => void runApiQuick("netconf:interfaces", () => app.NetDevNetconfQuery(selectedDevice.name, "interfaces"))}>{tt("ndv.dev.netconfIf")}</span>
                        <span className="btn btn--secondary btn--small" role="button" title={tt("ndv.dev.netconfCfgTip")}
                          onClick={() => void runApiQuick("netconf:running", () => app.NetDevNetconfQuery(selectedDevice.name, "running"))}>{tt("ndv.dev.netconfCfg")}</span>
                      </>
                    )}
                    <span
                      className="btn btn--secondary btn--small"
                      role="button"
                      title={tt("ndv.rec.btnTip")}
                      onClick={() => void openRecordings(selectedDevice.name)}
                    >{"↺ "}{tt("ndv.rec.btn")}</span>
                    {selectedDevice.vendor === "linux" && (
                      <span
                        className="btn btn--secondary btn--small"
                        role="button"
                        title={tt("ndv.dev.fileTip")}
                        onClick={() => setFilePicker(filePicker === selectedDevice.name ? "" : selectedDevice.name)}
                      >{"📁 "}{tt("ndv.dev.file")}</span>
                    )}
                    {selectedDevice.vendor === "linux" && WEB_QUICK.map(cmd => (
                      <span key={cmd} className="btn btn--secondary btn--small" role="button" onClick={() => void runQuick(selectedDevice.name, cmd)}>{cmd}</span>
                    ))}
                  </div>
                  {filePicker === selectedDevice.name && (
                    <div className="ndv__card" style={{ marginTop: 6 }}>
                      <div className="ndv__card-title">
                        {"📁 "}{tt("ndv.sftp.title")} — {selectedDevice.name}
                        <span className="btn btn--secondary btn--small" role="button" style={{ marginLeft: "auto" }} onClick={() => { setFilePicker(""); setFileListing(null); setFileNote(""); }}>{tt("ndv.rec.close")}</span>
                      </div>
                      <div className="ndv__hint">{tt("ndv.sftp.hint")}</div>
                      <div style={{ display: "flex", gap: 6, marginTop: 6 }}>
                        <input
                          className="mem-input"
                          value={filePath}
                          onChange={(e) => setFilePath(e.target.value)}
                          placeholder={tt("ndv.sftp.phDir")}
                          spellCheck={false}
                          style={{ flex: 1 }}
                        />
                        <span className="btn btn--secondary btn--small" role="button" onClick={() => void (async () => {
                          setFileBusy("browse");
                          try {
                            setFileListing(await app.NetDevSFTPBrowse(selectedDevice.name, filePath));
                            setFileNote("");
                          } catch (e) { setFileNote(String(e)); }
                          finally { setFileBusy(""); }
                        })()}>{fileBusy === "browse" ? tt("ndv.sftp.browsing") : tt("ndv.sftp.browse")}</span>
                        <span className="btn btn--primary btn--small" role="button" onClick={() => void (async () => {
                          setFileBusy("download");
                          try {
                            const saved = await app.NetDevSFTPDownload(selectedDevice.name, filePath);
                            setFileNote(saved ? tt("ndv.exportedTo", { path: saved }) : tt("ndv.misc.cancelled"));
                          } catch (e) { setFileNote(String(e)); }
                          finally { setFileBusy(""); }
                        })()}>{fileBusy === "download" ? tt("ndv.sftp.downloading") : tt("ndv.sftp.download")}</span>
                      </div>
                      {fileListing && fileListing.length > 0 && (
                        <div className="ndv__audit-scroll" style={{ maxHeight: 140, marginTop: 6 }}>
                          {fileListing.map(f => (
                            <div key={f} className="ndv__audit-row" role="button" style={{ cursor: "pointer" }} onClick={() => setFilePath(f)}>
                              <span className="ndv__audit-cmd">{f}</span>
                            </div>
                          ))}
                        </div>
                      )}
                      {fileNote && <div className="ndv__hint" style={{ marginTop: 6 }}>{fileNote}</div>}
                    </div>
                  )}
                  {/* 配置文件管理（§7.3）：变更区——快照/两版本 diff/环境 Drift；修改走 file-upload 提案。 */}
                  {selectedDevice.vendor === "linux" && (
                    <>
                      <div className="ndv__group-label" style={{ marginTop: 10 }}>{tt("ndv.dev.srvconf")}</div>
                      <SrvConfCard device={selectedDevice} peers={(settings?.devices ?? []).filter(d => (d.configPaths ?? []).length > 0)} />
                    </>
                  )}
                </>
              )}
              {(selectedDevice.kind ?? "") === "docker" && (
                <>
                  <div className="ndv__group-label">{tt("ndv.dev.dockerGroup")}</div>
                  <div className="ndv__quick-cmds">
                    {DOCKER_QUICK.map(q => (
                      <span key={q.what} className="btn btn--secondary btn--small" role="button"
                        onClick={() => void runApiQuick(q.label, () => app.NetDevDockerGet(selectedDevice.name, q.what, "", 100))}>{tt(q.label as never)}</span>
                    ))}
                  </div>
                </>
              )}
              {(selectedDevice.kind ?? "") === "k8s" && (
                <>
                  <div className="ndv__group-label">{tt("ndv.dev.k8sGroup")}</div>
                  <div className="ndv__quick-cmds">
                    {K8S_QUICK.map(q => (
                      <span key={q.what} className="btn btn--secondary btn--small" role="button"
                        onClick={() => void runApiQuick(q.label, () => app.NetDevK8sGet(selectedDevice.name, q.what, "", "", 100))}>{tt(q.label as never)}</span>
                    ))}
                  </div>
                </>
              )}
              {(selectedDevice.kind ?? "") === "firewall" && (
                <>
                  <div className="ndv__group-label">{tt("ndv.dev.fwGroup")}</div>
                  <div className="ndv__quick-cmds">
                    {FW_QUICK.map(q => (
                      <span key={q.what} className="btn btn--secondary btn--small" role="button"
                        onClick={() => void runApiQuick(q.label, () => app.NetDevFirewallGet(selectedDevice.name, q.what))}>{tt(q.label as never)}</span>
                    ))}
                  </div>
                </>
              )}
              <div className="ndv__group-label">{tt("ndv.dev.presets")}</div>
              {(settings?.presets ?? []).filter(p => (p.vendors ?? []).length === 0 || (p.vendors ?? []).includes(selectedDevice.vendor)).length > 0 && (
                <div className="ndv__quick-cmds">
                  {(settings?.presets ?? []).filter(p => (p.vendors ?? []).length === 0 || (p.vendors ?? []).includes(selectedDevice.vendor)).map(p => (
                    <span
                      key={p.name}
                      className="btn btn--secondary btn--small"
                      role="button"
                      title={tt("ndv.dev.presetTip", { cmds: p.commands.join("；") })}
                      onClick={() => { for (const c of p.commands) void runQuick(selectedDevice.name, c); }}
                    >▶ {p.name}</span>
                  ))}
                </div>
              )}
              <div className="ndv__group-label">{tt("ndv.dev.cfgBackup")}</div>
              <div className="ndv__quick-cmds">
                <span
                  className="btn btn--secondary btn--small"
                  role="button"
                  onClick={() => void runQuick(selectedDevice.name, CFG_CMD[selectedDevice.vendor] ?? "display current-configuration")}
                >{tt("ndv.dev.grabCfg")}</span>
                {onInsertComposer && (
                  <span
                    className="btn btn--primary btn--small"
                    role="button"
                    onClick={() => onInsertComposer(tt("ndv.dev.aiChangePrefix", { name: selectedDevice.name, addr: selectedDevice.address }) + tt("ndv.dev.aiChangeTail"))}
                  >{tt("ndv.dev.aiChange")}</span>
                )}
              </div>
              <BackupHistory device={selectedDevice.name} />
              {Object.values(quick).map(r => (
                <div key={r.command} className="ndv__quick-result">
                  <div className="ndv__quick-cmd" style={r.isError ? { color: "#ff8787" } : undefined}>{r.command}</div>
                  {r.refusedUnknown && selectedDevice && (
                    <div style={{ marginBottom: 4 }}>
                      <span
                        className="btn btn--secondary btn--small"
                        role="button"
                        title={tt("ndv.dev.allowCmdTip")}
                        onClick={() => void teachRead(selectedDevice.vendor, r.command, selectedDevice.name)}
                      >{tt("ndv.dev.allowCmd")}</span>
                    </div>
                  )}
                  <pre className="ndv__pre">{r.output || tt("ndv.misc.noOutput")}</pre>
                </div>
              ))}
            </div>
        )}

        {/* 场景入口 + 早报（原 context 页签的空态内容）→ 总览落地页：
            总览=态势 + 从这里发起动作，逻辑同域。 */}
        {tab === "overview" && devices.length > 0 && (
          <div style={{ display: "flex", flexDirection: "column", gap: 12, marginTop: 8 }}>
            <ScenarioHub
              onOpenAlertWizard={() => setAlertWizardOpen(true)}
              onDiagnose={() => { onInsertComposer?.(tt("ndv.sc.diagPrompt")); openDockTabFn("live"); }}
              onInspect={() => { void runInspection(); openDockTabFn("findings"); }}
              onLogs={() => openDockTabFn("logs")}
              onBaseline={() => { void runBaseline(); openDockTabFn("findings"); }}
              onGolden={() => openDockTabFn("devices")}
              onLocate={() => openDockTabFn("devices")}
              onTriage={() => { onInsertComposer?.(tt("ndv.sc.triagePrompt")); openDockTabFn("findings"); }}
              onWeak={() => { onInsertComposer?.(tt("ndv.sc.weakPrompt")); openDockTabFn("findings"); }}
              onCVE={() => { openSecBench(); window.dispatchEvent(new Event("hiq:netdev-cve")); }}
              onSecWizard={() => { openSecBench(); }}
              onCorrelate={() => { setLogsBenchEverOpened(true); setBench("logs"); }}
              onReports={() => openDockTabFn("audit")}
              onK8sGuide={() => onOpenSettings("netdev")}
            />
            {alertWizardOpen && settings && (
              <AlertSetupWizard
                settings={settings}
                onClose={() => setAlertWizardOpen(false)}
                onSaved={() => void reload()}
                onOpenSettings={onOpenSettings}
                onFinish={() => openDockTabFn("health")}
              />
            )}
            <DailyBriefing />
          </div>
        )}

        {tab === "topology" && topoRec && (
          <div className="ndv__card" style={{ marginBottom: 8 }}>
            <div className="ndv__card-title">{tt("ndv.topo.recTitle")}</div>
            <div className="ndv-ovw__statgrid">
              <span>{tt("ndv.discboard.triDesign", { n: topoRec.tri.design })} · {tt("ndv.discboard.triPlan", { n: topoRec.tri.plan })} · {tt("ndv.discboard.triMatch", { n: topoRec.tri.matched })}</span>
              <span className={topoRec.tri.only_design > 0 ? "ndv-ovw__warm" : "dim"}>
                {tt("ndv.discboard.triDrift", { d: topoRec.tri.only_design, p: topoRec.tri.only_plan })}
              </span>
              <span title={tt("ndv.topo.recPlatTip")}>
                {tt("ndv.topo.recPlat")}{" "}
                <b>{Object.entries(topoRec.platforms ?? {}).sort((a, b) => b[1] - a[1]).map(([k, v]) => `${k} ${v}`).join(" · ") || "—"}</b>
              </span>
            </div>
          </div>
        )}

        {tab === "topology" && (
          <div className="ndv__card">
            <div className="ndv__card-title">
              {tt("ndv.tab.topology")} <span style={{ fontWeight: 400, fontSize: 11, color: topoSource === "measured" ? "var(--ok)" : topoSource === "design" ? "var(--warn)" : "var(--accent)" }}>
                {topoSource === "measured" ? tt("ndv.topo.measured") : topoSource === "design" ? tt("ndv.topo.designTag") : tt("ndv.topo.inferred")}
              </span>
            </div>
            <div className="ndv__panel-actions">
              <span className="btn btn--secondary btn--small" role="button" onClick={() => void loadPlan()}>
                {tt("ndv.topo.refreshPlan")}
              </span>
              <span className="btn btn--secondary btn--small" role="button" onClick={() => void genTopology()}>
                {topoBusy ? tt("ndv.topo.collecting") : tt("ndv.topo.calibrate")}
              </span>
              <span className="btn btn--secondary btn--small" role="button" title={tt("ndv.topo.importTip")}
                onClick={() => {
                  const inp = document.createElement("input");
                  inp.type = "file";
                  inp.accept = ".drawio,.xml";
                  inp.onchange = () => { const f = inp.files?.[0]; if (f) void importTopoFile(f); };
                  inp.click();
                }}>{tt("ndv.topo.importBtn")}</span>
              <span className="btn btn--secondary btn--small" role="button" title={tt("ndv.topo.designLoadTip")}
                onClick={() => void loadDesign()}>{tt("ndv.topo.designLoad")}</span>
              <span className="btn btn--secondary btn--small" role="button" title={tt("ndv.topo.attackTip")}
                onClick={() => void runAttackPaths()}>{attackBusy ? tt("ndv.topo.attackBusy") : tt("ndv.topo.attackBtn")}</span>
            </div>
            {attackReport && (
              <div className="ndv__card" style={{ marginBottom: 8, border: "1px dashed var(--border-soft)" }}>
                <div className="ndv__card-title" style={{ fontSize: 12 }}>
                  {tt("ndv.topo.attackTitle")}
                  <span className="btn btn--secondary btn--small" role="button" style={{ marginLeft: "auto", fontSize: 10.5 }}
                    title={tt("ndv.topo.attackZoom")}
                    onClick={() => openDashBench("exposure")}>{tt("ndv.topo.attackZoom")}</span>
                </div>
                {attackReport.paths.length === 0 ? (
                  <div className="ndv__meta">{tt("ndv.topo.attackEmpty")}</div>
                ) : (
                  <>
                    <div className="ndv__meta">{tt("ndv.topo.attackStats", {
                      exp: attackReport.exposure_points.length, n: attackReport.paths.length,
                      edges: attackReport.edges, src: (attackReport.edge_sources ?? []).join("+") || "-",
                    })}</div>
                    {attackReport.paths.slice(0, 6).map((p, i) => (
                      <div key={i} className="ndv__meta" style={{ fontSize: 11 }}>
                        {p.exposure_device} → {p.steps.map(s => s.to).join(" → ")}
                        {" "}({p.hops} · {p.end_role || "?"}{p.end_managed ? "" : ` · ${tt("ndv.topo.attackUnmanaged")}`} · {p.score})
                      </div>
                    ))}
                    {(attackReport.cut_suggestions ?? []).length > 0 && (
                      <div className="ndv__meta" style={{ marginTop: 4 }}>{tt("ndv.topo.attackCuts")}{
                        attackReport.cut_suggestions.map(c => `${c.from}—${c.to}(${c.paths_removed})`).join("；")
                      }</div>
                    )}
                  </>
                )}
                <div style={{ display: "flex", justifyContent: "flex-end" }}>
                  <span className="btn btn--secondary btn--small" role="button" onClick={() => setAttackReport(null)}>{tt("common.close")}</span>
                </div>
              </div>
            )}
            {topoNotice && (
              <div className="ndv__hint ndv__hint--flush" style={{ marginBottom: 8, color: "var(--accent)" }}>{topoNotice}</div>
            )}
            {topoPreview && (
              <div className="ndv__card" style={{ marginBottom: 8, border: "1px dashed var(--border-soft)" }}>
                <div className="ndv__card-title" style={{ fontSize: 12 }}>{tt("ndv.topo.previewTitle", { file: topoImportName })}</div>
                <div className="ndv__meta">
                  {tt("ndv.topo.previewStats", {
                    total: topoPreview.stats.total, managed: topoPreview.stats.managed,
                    fresh: topoPreview.stats.new_nodes, unknown: topoPreview.stats.unresolved_role,
                  })}
                  {topoPreview.stats.pages > 1 ? ` · ${tt("ndv.topo.previewPage", { name: topoPreview.stats.used_page, n: topoPreview.stats.pages })}` : ""}
                </div>
                {(topoPreview.warnings ?? []).slice(0, 4).map((w, i) => (
                  <div key={i} className="ndv__meta" style={{ color: "var(--warn)", fontSize: 11 }}>⚠ {w}</div>
                ))}
                <TopologyMap graph={{ ...topoPreview.graph, nodes: topoPreview.graph.nodes ?? [], edges: topoPreview.graph.edges ?? [] }} selected={selected} selectedAddr={selectedDevice?.address} onPick={pickFromTopo} health={healthMap} />
                <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
                  <span className="btn btn--secondary btn--small" role="button" onClick={() => setTopoPreview(null)}>{tt("common.cancel")}</span>
                  <span className="btn btn--primary btn--small" role="button" onClick={() => void applyTopoImport()}>{tt("ndv.topo.apply")}</span>
                </div>
              </div>
            )}
            {topo && <TopologyMap graph={scopedTopo ?? topo} selected={selected} selectedAddr={selectedDevice?.address} onPick={pickFromTopo} health={healthMap} />}
            {!topo && !topoBusy && (
              <div className="ndv__hint ndv__hint--flush">
                {tt("ndv.topo.engineNote")}
              </div>
            )}
          </div>
        )}

        {tab === "findings" && (
          <div className="ndv__card">
            <div className="ndv__card-title">
              {tt("ndv.fnd.title", { n: jumpFilteredFindings.length })}{scopedFindings.length !== jumpFilteredFindings.length && <span style={{ fontWeight: 400, fontSize: 11 }}> / {scopedFindings.length}</span>}{project && <span style={{ fontWeight: 400, fontSize: 11 }}> · {project.name}</span>}
              {dashJumpFilter?.tab === "findings" && (
                <span className="ndv-jumpfilter" role="button" title={tt("ndv.jumpfilter.clear")}
                  onClick={() => setDashJumpFilter(null)}>
                  {tt("ndv.jumpfilter.label")}: {dashJumpFilter.filter} ×
                </span>
              )}
            </div>
            {project && <div className="ndv__hint ndv__hint--flush">{tt("ndv.fnd.projScopeHint", { name: project.name })}</div>}
            <div className="ndv__panel-actions">
              <span
                className={`btn btn--small ${aggView ? "btn--primary" : "btn--secondary"}`}
                role="button"
                title={tt("ndv.fnd.aggTip")}
                onClick={() => setAggView(v => !v)}
              >{aggView ? tt("ndv.fnd.aggView") : tt("ndv.fnd.flatView")}</span>
              <span
                className="btn btn--secondary btn--small"
                role="button"
                title={tt("ndv.fnd.baselineTip")}
                onClick={() => void runBaseline()}
              >{baseBusy ? tt("ndv.insp.checking") : tt("ndv.fnd.baselineBtn")}</span>
              <span
                className="btn btn--secondary btn--small"
                role="button"
                title={tt("ndv.fnd.cveTip")}
                onClick={() => void (async () => {
                  setBaseBusy(true);
                  try {
                    const f = await app.NetDevCVESweep();
                    if (f) setErr(`[SYS] CVE SWEEP: ${f.title}`);
                    await reload();
                  } catch (e) { setErr(String(e)); } finally { setBaseBusy(false); }
                })()}
              >{tt("ndv.fnd.cveBtn")}</span>
              {scopedFindings.length > 0 && (
                <span
                  className={`btn btn--small ${findingsClearArm ? "btn--primary" : "btn--secondary"}`}
                  role="button"
                  title={tt("ndv.fnd.clearTip")}
                  onClick={() => {
                    if (!findingsClearArm) { setFindingsClearArm(true); return; }
                    setFindingsClearArm(false);
                    void (async () => {
                      try {
                        const n = await app.NetDevFindingsClear();
                        setErr(`[SYS] ${tt("ndv.fnd.clearDone", { n })}`);
                        await reload();
                      } catch (e) { setErr(String(e)); }
                    })();
                  }}
                  onBlur={() => setFindingsClearArm(false)}
                >{findingsClearArm ? tt("ndv.fnd.clearConfirm", { n: scopedFindings.length }) : tt("ndv.fnd.clearBtn")}</span>
              )}
            </div>
            {jumpFilteredFindings.length === 0 && <div className="ndv__hint ndv__hint--flush">{project ? tt("ndv.fnd.emptyProj", { name: project.name }) : tt("ndv.fnd.empty")}</div>}
            {aggView && aggs.length > 0 ? aggs.map(a => <AggRow key={a.key} a={a} onChanged={() => void reload()} />) : jumpFilteredFindings.slice(0, 20).map(f => <FindingRow key={f.id} f={f} onResolved={() => void reload()} />)}
          </div>
        )}

        {tab === "proposals" && (
          <>
          <div className="ndv__card">
            <div className="ndv__card-title">{tt("ndv.pt.title", { a: filteredProposals.length, b: scopedProposals.length })}{project && <span style={{ fontWeight: 400, fontSize: 11 }}> · {project.name}</span>}
              {dashJumpFilter?.tab === "proposals" && (
                <span className="ndv-jumpfilter" role="button" title={tt("ndv.jumpfilter.clear")}
                  onClick={() => setDashJumpFilter(null)}>
                  {tt("ndv.jumpfilter.label")}: {dashJumpFilter.filter} ×
                </span>
              )}
            </div>
            {/* §4.1 列表治理：状态筛选 chips + 分页——去掉 slice(0,10) 硬顶。 */}
            <div className="ndv__quick-cmds" style={{ marginBottom: 6 }}>
              {PROPOSAL_FILTERS.map(f => (
                <span
                  key={f.key}
                  className={`btn btn--small ${proposalFilter === f.key ? "btn--primary" : "btn--secondary"}`}
                  role="button"
                  onClick={() => { setProposalFilter(f.key); setProposalPage(1); }}
                >{f.label}</span>
              ))}
            </div>
            {filteredProposals.length === 0 && <div className="ndv__hint ndv__hint--flush">{project ? tt("ndv.pt.emptyProj", { name: project.name }) : tt("ndv.pt.empty")}</div>}
            {filteredProposals.slice(0, proposalPage * PROPOSAL_PAGE_SIZE).map(p => <ProposalRow key={p.id} p={p} onDone={() => void reload()} />)}
            {filteredProposals.length > proposalPage * PROPOSAL_PAGE_SIZE && (
              <div className="ndv__quick-cmds" style={{ marginTop: 4 }}>
                <span className="btn btn--secondary btn--small" role="button" onClick={() => setProposalPage(v => v + 1)}>
                  {tt("ndv.pt.more", { n: filteredProposals.length - proposalPage * PROPOSAL_PAGE_SIZE })}
                </span>
              </div>
            )}
            <div className="ndv__hint ndv__hint--flush">{tt("ndv.pt.humanGate")}</div>
          </div>
          <div className="ndv__card">
            <div className="ndv__card-title">
              {"🌗 "}{tt("ndv.pt.cutover", { n: cutovers.length })}{cutovers.some(c => c.status === "running" || c.status === "hold") && <span className="ndv__warn"> · {tt("ndv.jobs.inProgress").replace("· ", "")}</span>}
              {cutovers.some(c => c.status === "running" || c.status === "hold") && (
                <span
                  className="btn btn--secondary btn--small"
                  role="button"
                  style={{ marginLeft: "auto", marginRight: 6 }}
                  title={tt("ndv.cut.enterBoard")}
                  onClick={() => openDashBench("cutover")}
                >{tt("ndv.cut.enterBoard")}</span>
              )}
              <span
                className="btn btn--secondary btn--small"
                role="button"
                style={{ marginLeft: cutovers.some(c => c.status === "running" || c.status === "hold") ? 0 : "auto" }}
                title={tt("ndv.pt.cutoverTip")}
                onClick={() => { setCutoverId(""); setBench("chat"); }}
              >{tt("ndv.pt.startCutover")}</span>
            </div>
            {cutovers.length === 0 && <div className="ndv__hint ndv__hint--flush">{tt("ndv.pt.cutoverHint")}</div>}
            {cutovers.slice(0, 5).map(c => (
              <div key={c.id} className="ndv__device" style={{ gap: 8, alignItems: "center" }}>
                <span className={`ndv__dot ${c.status === "running" ? "ndv__dot--warn" : c.status === "done" ? "ndv__dot--ok" : c.status === "hold" ? "ndv__dot--down" : ""}`} style={{ background: c.status === "hold" ? "var(--warn)" : undefined }} />
                <span className="ndv__device-name" role="button" onClick={() => { setCutoverId(c.id); setBench("chat"); }}>{c.name}</span>
                <span className="ndv__device-addr">
                  {c.status === "running" || c.status === "hold"
                    ? tt("ndv.pt.countdownMin", { n: Math.max(0, Math.round((new Date(c.deadline).getTime() - Date.now()) / 60000)) })
                    : c.status}
                  {(c.steps ?? []).length > 0 ? tt("ndv.pt.nSteps", { n: (c.steps ?? []).length }) : ""}
                </span>
              </div>
            ))}
          </div>
          <TemplateCard devices={devices} onDrafted={() => void reload()} />
          </>
        )}

        {tab === "audit" && (
          <div className="ndv__card">
            <div className="ndv__card-title">{tt("ndv.aud.title", { n: audit.length })}<AuditChainBadge /></div>
            
            <div className="ndv__audit-stats">
              <span className="ndv__bottom-item"><span className="ndv__ok">{tt("ndv.aud.todayRead")}</span> {readCount}</span>
              <span className="ndv__bottom-item"><span className="ndv__ok">{tt("ndv.aud.directWrites")}</span> <span className="ndv__zero">{writeCount}</span></span>
              <span className="ndv__bottom-item">
                {tt("ndv.aud.proposals")} {pendingCount > 0 ? <span className="ndv__warn">{tt("ndv.aud.pending", { n: pendingCount })}</span> : <span className="ndv__zero">0</span>}
              </span>
            </div>

            <div className="ndv__audit-scroll">
              <div className="ndv__audit-table">
                <div className="ndv__audit-row ndv__audit-row--head">
                  <span>{tt("ndv.aud.colTime")}</span><span>{tt("ndv.aud.colDevice")}</span><span>{tt("ndv.aud.colCommand")}</span><span>{tt("ndv.aud.colClass")}</span><span>{tt("ndv.aud.colStatus")}</span>
                </div>
                {audit.length === 0 ? (
                  <div className="ndv__audit-empty">
                    {tt("ndv.aud.empty1")}<br />
                    {tt("ndv.aud.empty2")}
                  </div>
                ) : audit.slice(0, 100).map((a, i) => (
                  <div key={`${a.time}-${i}`} className="ndv__audit-row" title={a.error || a.command}>
                    <span className="ndv__audit-time">{String(a.time ?? "").slice(11, 19) || String(a.time ?? "").slice(5, 16)}</span>
                    <span className="ndv__audit-dev">{a.device}</span>
                    <span className="ndv__audit-cmd">{a.command}</span>
                    <span style={{ color: classColorForAudit(a.class) }}>{auditClassLabel(a.class)}</span>
                    <span style={{ color: a.status === "ok" ? "var(--ok)" : a.status === "refused" ? "var(--danger)" : "var(--warn)" }}>{a.status}</span>
                  </div>
                ))}
              </div>
            </div>
            <div className="ndv__hint ndv__hint--flush" style={{ marginTop: 8 }}>{tt("ndv.aud.footer")}</div>
          </div>
        )}
        {tab === "state" && <StateHistoryPanel />}
        </div>
      </div>
      )}
    </div>
  );
}

// BackupTimeline: the selected device's config version vault — list, one-click
// backup now, and a two-pick diff. Restore stays proposal-shaped on purpose:
// "从此版本恢复" hands the version to the agent as DRAFT context (the human
// approves the actual change in the 提案 pipeline).
function BackupTimeline({ device }: { device: string }) {
  const [versions, setVersions] = useState<{ id: string; at: string; bytes: number; lines: number }[] | null>(null);
  const [pick, setPick] = useState<string[]>([]);
  const [diff, setDiff] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [golden, setGolden] = useState<{ set: boolean; at: string; lines: number } | null>(null);
  const [driftNote, setDriftNote] = useState("");
  const reload = useCallback(async () => {
    try {
      setVersions(await app.NetDevBackups(device));
      setGolden(await app.NetDevGoldenInfo(device));
      setErr("");
    } catch (e) {
      setErr(String(e));
    }
  }, [device]);
  useEffect(() => { setVersions(null); setPick([]); setDiff(""); setDriftNote(""); void reload(); }, [reload]);
  const togglePick = (id: string) => {
    setDiff("");
    setPick(prev => prev.includes(id) ? prev.filter(x => x !== id) : [...prev, id].slice(-2));
  };
  const showDiff = async () => {
    if (pick.length !== 2) return;
    setBusy(true);
    try {
      setDiff(await app.NetDevBackupDiff(device, pick[0], pick[1]));
    } catch (e) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="ndv__section-row" style={{ flexDirection: "column", alignItems: "stretch", gap: 6 }}>
      <div className="ndv__section-row">
        <div className="ndv__section">{tt("ndv.bkt.title", { dev: device })}</div>
        <span className="btn btn--secondary btn--small ndv__section-btn" role="button"
          onClick={() => { setBusy(true); void app.NetDevRunBackup(device).then(() => reload()).finally(() => setBusy(false)); }}>
          {busy ? tt("ndv.bkt.busy") : tt("ndv.bkt.backupNow")}
        </span>
        {golden?.set && (
          <span className="btn btn--secondary btn--small ndv__section-btn" role="button"
            title={tt("ndv.bkt.driftTip")}
            onClick={() => { setBusy(true); setDriftNote(""); void app.NetDevGoldenCheck(device).then(r => setDriftNote(r)).catch(e => setErr(String(e))).finally(() => setBusy(false)); }}>
            {busy ? tt("ndv.comparing") : tt("ndv.bkt.checkDrift")}
          </span>
        )}
      </div>
      {golden?.set && (
        <div className="ndv__meta">{tt("ndv.bkt.golden", { at: String(golden.at).slice(0, 16).replace("T", " "), n: golden.lines })}</div>
      )}
      {golden && !golden.set && (
        <div className="ndv__meta" style={{ color: "var(--warn, #f5a524)" }}>
          {tt("ndv.bkt.noGolden")}
        </div>
      )}
      {driftNote && <div className="ndv__meta" style={{ whiteSpace: "pre-wrap" }}>{driftNote}</div>}
      {err && <div className="ndv__hint">{err}</div>}
      {(versions ?? []).length === 0 && <div className="ndv__hint">{tt("ndv.bkt.noVersions")}</div>}
      {(versions ?? []).slice(0, 10).map(v => (
        <div key={v.id} className="ndv__device" role="button" onClick={() => togglePick(v.id)}
          style={{ cursor: "pointer", outline: pick.includes(v.id) ? "1px solid var(--accent)" : "none" }}>
          <span className="ndv__device-addr">{String(v.at ?? "").slice(5, 16).replace("T", " ")}</span>
          <span className="ndv__device-addr">{tt("ndv.bkt.linesBytes", { a: v.lines, b: v.bytes })}</span>
          {pick.includes(v.id) && <span className="ndv__meta" style={{ marginLeft: "auto" }}>{tt("ndv.bkt.picked", { n: pick.indexOf(v.id) + 1 })}</span>}
          <span className="btn btn--secondary btn--small" role="button"
            title={tt("ndv.bkt.setGoldenTip")}
            onClick={() => { setBusy(true); void app.NetDevSetGoldenFromBackup(device, v.id).then(() => reload()).catch(e => setErr(String(e))).finally(() => setBusy(false)); }}>
            {tt("ndv.bkt.setGolden")}
          </span>
        </div>
      ))}
      {pick.length === 2 && (
        <>
          <span className="btn btn--secondary btn--small" role="button" onClick={() => void showDiff()}>{busy ? tt("ndv.comparing") : tt("ndv.bkt.diffTwo")}</span>
          {diff && <pre className="ndv__diff ndv__pre" style={{ maxHeight: 220 }}>{diff}</pre>}
        </>
      )}
    </div>
  );
}

// AuditChainBadge: hash-chain integrity verdict (B-batch) — one query on
// mount; green = chain intact, red = tampering suspected (firstBroken says where).
function AuditChainBadge() {
  const [st, setSt] = useState<{ total: number; chained: number; ok: boolean; firstBroken?: string } | null>(null);
  useEffect(() => {
    app.NetDevAuditVerify().then(setSt).catch(() => setSt(null));
  }, []);
  if (!st) return null;
  if (st.chained === 0) return <span style={{ marginLeft: 8, fontSize: 11, fontWeight: 400, opacity: 0.7 }}>{tt("ndv.chain.none")}</span>;
  return (
    <span style={{ marginLeft: 8, fontSize: 11, fontWeight: 400, color: st.ok ? "var(--ok)" : "var(--danger)" }} title={st.firstBroken ?? ""}>
      {st.ok ? tt("ndv.chain.ok", { n: st.chained }) : tt("ndv.chain.broken", { at: st.firstBroken ?? "" })}
    </span>
  );
}

// ScenarioHub — 场景引导中心：每个场景一句话说明 + 一个动作，动作要么打开
// 向导（告警接入）、要么填好提示词并打开对应页卡（诊断/巡检/基线）。
function ScenarioHub({ onOpenAlertWizard, onDiagnose, onInspect, onLogs, onBaseline, onGolden,
  onLocate, onTriage, onWeak, onCVE, onSecWizard, onCorrelate, onReports, onK8sGuide }: {
  onOpenAlertWizard: () => void;
  onDiagnose: () => void;
  onInspect: () => void;
  onLogs: () => void;
  onBaseline: () => void;
  onGolden: () => void;
  onLocate: () => void;
  onTriage: () => void;
  onWeak: () => void;
  onCVE: () => void;
  onSecWizard: () => void;
  onCorrelate: () => void;
  onReports: () => void;
  onK8sGuide: () => void;
}) {
  const cards: { icon: string; title: string; desc: string; action: string; primary?: boolean; run: () => void }[] = [
    { icon: "⏰", title: tt("ndv.sc.a1t"), desc: tt("ndv.sc.a1d"), action: tt("ndv.sc.a1a"), primary: true, run: onOpenAlertWizard },
    { icon: "🔍", title: tt("ndv.sc.a2t"), desc: tt("ndv.sc.a2d"), action: tt("ndv.sc.a2a"), run: onDiagnose },
    { icon: "🧪", title: tt("ndv.sc.a3t"), desc: tt("ndv.sc.a3d"), action: tt("ndv.insp.runNow"), run: onInspect },
    { icon: "📜", title: tt("ndv.sc.a4t"), desc: tt("ndv.sc.a4d"), action: tt("ndv.sc.a4a"), run: onLogs },
    { icon: "🛡", title: tt("ndv.sc.a5t"), desc: tt("ndv.sc.a5d"), action: tt("ndv.sc.a5a"), run: onBaseline },
    { icon: "📐", title: tt("ndv.sc.a6t"), desc: tt("ndv.sc.a6d"), action: tt("ndv.sc.a6a"), run: onGolden },
    { icon: "📍", title: tt("ndv.sc.b1t"), desc: tt("ndv.sc.b1d"), action: tt("ndv.sc.b1a"), run: onLocate },
    { icon: "🩺", title: tt("ndv.sc.b2t"), desc: tt("ndv.sc.b2d"), action: tt("ndv.sc.b2a"), run: onTriage },
    { icon: "🔑", title: tt("ndv.sc.b3t"), desc: tt("ndv.sc.b3d"), action: tt("ndv.sc.b3a"), run: onWeak },
    { icon: "🧫", title: tt("ndv.sc.b4t"), desc: tt("ndv.sc.b4d"), action: tt("ndv.sc.b4a"), run: onCVE },
    { icon: "🚨", title: tt("ndv.sc.b5t"), desc: tt("ndv.sc.b5d"), action: tt("ndv.sc.b5a"), run: onSecWizard },
    { icon: "🔗", title: tt("ndv.sc.b6t"), desc: tt("ndv.sc.b6d"), action: tt("ndv.sc.b6a"), run: onCorrelate },
    { icon: "📋", title: tt("ndv.sc.b7t"), desc: tt("ndv.sc.b7d"), action: tt("ndv.sc.b7a"), run: onReports },
    { icon: "🐳", title: tt("ndv.sc.b8t"), desc: tt("ndv.sc.b8d"), action: tt("ndv.sc.b8a"), run: onK8sGuide },
  ];
  return (
    <div className="ndv__card">
      <div className="ndv__card-title">{tt("ndv.sc.title")}</div>
      <div className="mem-hint" style={{ marginBottom: 8 }}>{tt("ndv.sc.hint")}</div>
      <div style={{ display: "grid", gridTemplateColumns: "repeat(3, 1fr)", gap: 8 }}>
        {cards.map(c => (
          <div key={c.title} style={{ border: "1px solid var(--border)", borderRadius: "var(--radius-sm)", padding: 8, display: "flex", flexDirection: "column", gap: 4 }}>
            <div style={{ fontSize: 12.5, fontWeight: 600 }}>{c.icon} {c.title}</div>
            <div style={{ fontSize: 11, opacity: 0.75, flex: 1 }}>{c.desc}</div>
            <span className={`btn btn--small ${c.primary ? "btn--primary" : "btn--secondary"}`} role="button" style={{ alignSelf: "flex-start" }} onClick={c.run}>{c.action}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

// GettingStarted: the zero-device onboarding — the answer to "从哪里开始".
function GettingStarted({ onOpenSettings }: { onOpenSettings: (tab: string) => void }) {
  const steps: { text: string; action?: { label: string; run: () => void } }[] = [
    { text: tt("ndv.gs.s1"), action: { label: tt("ndv.dev.openSettings"), run: () => onOpenSettings("netdev") } },
    { text: tt("ndv.gs.s2") },
    { text: tt("ndv.gs.s3") },
  ];
  return (
    <div className="ndv__card ndv__gs">
      <div className="ndv__card-title">{tt("ndv.gs.title")}</div>
      {steps.map((s, i) => (
        <div key={i} className="ndv__step">
          <span className="ndv__step-n">{i + 1}</span>
          <div className="ndv__step-body">
            {s.text}
            {s.action && (
              <span className="btn btn--primary btn--small ndv__step-btn" role="button" onClick={s.action.run}>{s.action.label}</span>
            )}
          </div>
        </div>
      ))}
      <div className="ndv__hint" style={{ padding: 0, marginTop: 4 }}>
        {tt("ndv.gs.footer")}
      </div>
    </div>
  );
}

// DailyBriefing: objective 24h data in, model-judged report out — the
// content comes from one designed prompt on a headless netdev controller,
// never a hardcoded template (user direction). Falls back to a nudge when
// there is nothing to summarize yet.
function DailyBriefing() {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const run = useCallback(async () => {
    setBusy(true);
    setErr("");
    try {
      setText(await app.NetDevDailyBriefing());
    } catch (e) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  }, []);
  return (
    <div className="ndv__card">
      <div className="ndv__card-title">{tt("ndv.br.title")}</div>
      <div style={{ display: "flex", gap: 6, marginBottom: text || err ? 8 : 0 }}>
        <span className="btn btn--secondary btn--small" role="button" onClick={() => void run()}>
          {busy ? tt("ndv.br.busy") : tt("ndv.br.generate")}
        </span>
      </div>
      {err && <div className="ndv__meta" style={{ color: "var(--err)" }}>{err}</div>}
      {text && (
        <div className="ndv__md">
          <Markdown text={text} />
        </div>
      )}
    </div>
  );
}

// BackupHistory: the device card's version vault — newest versions first,
// pick any two to see the redacted unified diff (变更↔故障关联的入口).
function BackupHistory({ device }: { device: string }) {
  const [versions, setVersions] = useState<NetDevBackupVersion[]>([]);
  const [pick, setPick] = useState<string[]>([]);
  const [diff, setDiff] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    try {
      setVersions(await app.NetDevBackups(device));
      setPick([]);
      setDiff("");
      setErr("");
    } catch (e) {
      setErr(String(e));
    }
  }, [device]);
  useEffect(() => { void load(); }, [load]);
  const runBackup = useCallback(async () => {
    setBusy(true);
    try {
      const vers = await app.NetDevRunBackup(device);
      if (vers.length === 0) setErr("[SYS] BACKUP FAILED: ACCESS DENIED OR DEVICE ERROR");
      await load();
    } catch (e) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  }, [device, load]);
  const toggle = (id: string) => {
    setPick(p => p.includes(id) ? p.filter(x => x !== id) : [...p, id].slice(-2));
    setDiff("");
  };
  const showDiff = async () => {
    if (pick.length !== 2) return;
    try {
      // pick[0] is newer, pick[1] older → diff(old → new)
      setDiff(await app.NetDevBackupDiff(device, pick[1], pick[0]));
    } catch (e) {
      setErr(String(e));
    }
  };
  return (
    <div style={{ marginTop: 8 }}>
      <div style={{ display: "flex", gap: 6, alignItems: "center", marginBottom: 4 }}>
        <span
          className="btn btn--secondary btn--small"
          role="button"
          title={tt("ndv.bkc.backupTip")}
          onClick={() => void runBackup()}
        >{busy ? tt("ndv.bkc.backing") : tt("ndv.bkc.backupBtn")}</span>
        <span className="ndv__meta" style={{ marginBottom: 0 }}>{tt("ndv.bkc.versionsHint", { n: (versions ?? []).length })}</span>
      </div>
      {err && <div className="ndv__meta" style={{ color: "var(--err)" }}>{err}</div>}
      {(versions ?? []).slice(0, 8).map(v => (
        <div key={v.id} className="ndv__backup-row" role="button" onClick={() => toggle(v.id)}>
          <span style={{ opacity: pick.includes(v.id) ? 1 : 0.45 }}>{pick.includes(v.id) ? "☑" : "☐"}</span>
          <span>{v.at}</span>
          <span style={{ opacity: 0.6 }}>{tt("ndv.bkc.nLines", { n: v.lines })}</span>
        </div>
      ))}
      {pick.length === 2 && (
        <div style={{ margin: "4px 0" }}>
          <span className="btn btn--secondary btn--small" role="button" onClick={() => void showDiff()}>{tt("ndv.bkc.diffSel")}</span>
        </div>
      )}
      {diff && (
        <UnifiedDiff value={diff} maxHeight={360} showToggle={false} />
      )}
    </div>
  );
}

// FindingRow: one diagnosis conclusion with its expandable evidence chain
// (device ▸ command ▸ output) — the review surface, ported from the settings
// FindingCenter so operations never leave 运维页面.
const SEV_LABEL_KEYS: Record<string, string> = { info: "ndv.sev.info", warning: "ndv.sev.warning", critical: "ndv.sev.critical" };
// AggRow — 聚合队列行（§4.10 同类聚合）：「link-flap ×17（3 台）」一条队列项
// 展开看成员；ack/误报操作直达成员 Finding。
function AggRow({ a, onChanged }: { a: NetDevAggregatedFinding; onChanged?: () => void }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="ndv__finding" style={{ "--sev": SEV_COLOR[a.severity] ?? SEV_COLOR.info } as React.CSSProperties}>
      <div className="ndv__finding-title" role="button" onClick={() => setOpen(!open)}>
        <span style={{ color: SEV_COLOR[a.severity] ?? SEV_COLOR.info, marginRight: 6 }}>{tt((SEV_LABEL_KEYS[a.severity] ?? "ndv.sev.info") as never)}</span>
        {a.title} <span style={{ fontWeight: 700 }}>×{a.count}</span>
        <span style={{ fontWeight: 400, opacity: 0.7 }}>{tt("ndv.agg.meta", { n: a.devices?.length ?? 0, open: a.open })}{open ? " ▲" : " ▼"}</span>
        {a.suppressed >= 2 && <span className="ndv__badge ndv__badge--warn" style={{ marginLeft: 6 }} title={tt("ndv.agg.suppressedTip", { n: a.suppressed })}>{tt("ndv.agg.suppressed")}</span>}
      </div>
      {open && (
        <div style={{ marginTop: 4, display: "flex", flexDirection: "column", gap: 4 }}>
          {(a.members ?? []).map(f => (
            <FindingRow key={f.id} f={f} onResolved={onChanged} />
          ))}
        </div>
      )}
    </div>
  );
}

function FindingRow({ f, onResolved }: { f: NetDevFinding; onResolved?: () => void }) {
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  return (
    <div className="ndv__finding" style={{ "--sev": SEV_COLOR[f.severity] ?? SEV_COLOR.info, opacity: f.status === "resolved" ? 0.5 : 1 } as React.CSSProperties}>
      <div className="ndv__finding-title" role="button" onClick={() => setOpen(!open)}>
        <span style={{ color: SEV_COLOR[f.severity] ?? SEV_COLOR.info, marginRight: 6 }}>{tt((SEV_LABEL_KEYS[f.severity] ?? "ndv.sev.info") as never)}</span>
        {f.title} <span style={{ fontWeight: 400, opacity: 0.7 }}>{tt("ndv.fnd.evidence", { n: f.evidence?.length ?? 0 })} {open ? "▲" : "▼"}</span>
        {f.status === "active" && <span className="ndv__badge ndv__badge--warn" style={{ marginLeft: 6 }}>{tt("ndv.fnd.alerting")}</span>}
        {f.status === "resolved" && <span style={{ marginLeft: 6, fontSize: 10, color: "var(--ok)", border: "1px solid var(--ok)", borderRadius: "var(--radius-pill)", padding: "0 6px" }}>{tt("ndv.fnd.recovered")}</span>}
      </div>
      <div style={{ display: "flex", gap: 6, alignSelf: "flex-end", marginBottom: 4 }}>
        {f.status === "active" && (
          <span className="btn btn--secondary btn--small" role="button" title={tt("ndv.fnd.ackTip")}
            onClick={() => { void app.NetDevAckFinding(f.id).then(() => onResolved?.()); }}>{tt("ndv.fnd.ack")}</span>
        )}
        {(f.status === "active" || f.status === "ack") && (
          <span className="btn btn--secondary btn--small" role="button" title={tt("ndv.fnd.fpTip")}
            onClick={() => { void app.NetDevFalsePositiveFinding(f.id).then(() => onResolved?.()); }}>{tt("ndv.fnd.fp")}</span>
        )}
        <span className="btn btn--secondary btn--small" role="button" title={tt("ndv.fnd.caseTip")}
          onClick={() => {
            window.dispatchEvent(new CustomEvent("hiq:netdev-case", { detail: { title: f.title, device: (f.devices ?? [])[0] ?? "", text: `${f.severity}｜${f.title}｜${(f.detail ?? "").slice(0, 120)}`, ref: f.id } }));
            window.dispatchEvent(new CustomEvent("hiq:netdev-bench", { detail: "sec" }));
          }}>{tt("ndv.fnd.createCase")}</span>
        {f.status === "active" && (
          <span className="btn btn--secondary btn--small" role="button"
            onClick={() => { void app.NetDevResolveFinding(f.id).then(() => onResolved?.()); }}>{tt("ndv.fnd.resolve")}</span>
        )}
        <span className="btn btn--secondary btn--small" role="button" title={tt("ndv.fnd.chainTip")}
          onClick={() => { window.dispatchEvent(new CustomEvent("hiq:netdev-open-screen", { detail: { screen: "chain", finding: f.id } })); }}>{tt("ndv.fnd.chainBtn")}</span>
        <span className="btn btn--secondary btn--small" role="button" title={tt("ndv.fnd.copyLinkTip")}
          onClick={() => {
            void navigator.clipboard?.writeText(`hiq://finding/${f.id}`).then(() => {
              setCopied(true);
              window.setTimeout(() => setCopied(false), 1500);
            });
          }}>{copied ? "✓" : tt("ndv.fnd.copyLink")}</span>
        <span className="btn btn--secondary btn--small" role="button" title={tt("ndv.fnd.dismissTip")}
          onClick={() => { void app.NetDevFindingDismiss(f.id).then(() => onResolved?.()); }}>{tt("ndv.fnd.dismissBtn")}</span>
      </div>
      <div className="ndv__meta">{(f.devices ?? []).join("、")}{f.suggestion ? "" : ""}</div>
      {f.suggestion && !open && <div className="ndv__finding-suggestion">{tt("ndv.fnd.suggest", { s: f.suggestion })}</div>}
      {open && (
        <div style={{ marginTop: 4 }}>
          {f.detail && <div className="ndv__meta">{f.detail}</div>}
          {(f.evidence ?? []).map((e, i) => (
            <div key={i} style={{ marginBottom: 6, marginLeft: 8 }}>
              <div style={{ opacity: 0.8, fontSize: 11.5 }}>{e.device} ▸ <code>{e.command}</code></div>
              <pre className="ndv__pre" style={{ maxHeight: 140 }}>{e.output}</pre>
            </div>
          ))}
          {f.suggestion && <div className="ndv__finding-suggestion">{tt("ndv.fnd.suggestAI", { s: f.suggestion })}</div>}
          <div className="ndv__meta">{String(f.created_at ?? "").slice(0, 19).replace("T", " ")}</div>
        </div>
      )}
    </div>
  );
}

// ProposalRow: one queue row, expandable to steps (commands + rollback plan)
// with the human actions inline — approve / execute / rollback happen where
// the user is looking, not in a settings round-trip.
function ProposalRow({ p, onDone }: { p: NetDevProposal; onDone: () => void }) {
  const [open, setOpen] = useState(false);
  const sev = p.status === "partial" || p.status === "failed" ? SEV_COLOR.critical : SEV_COLOR.warning;
  return (
    <div className="ndv__finding" style={{ "--sev": sev } as React.CSSProperties}>
      <div className="ndv__finding-title" role="button" onClick={() => setOpen(!open)}>
        {p.id} · {p.status} {open ? "▲" : "▼"}
      </div>
      <div className="ndv__meta ndv__ellipsis">{p.intent}</div>
      {p.status === "rejected" && p.reject_reason && (
        <div className="ndv__meta" style={{ color: "var(--danger)" }}>{tt("ndv.pt.rejectReason", { s: p.reject_reason ?? "" })}</div>
      )}
      <ProposalActions p={p} onDone={onDone} />
      {open && (
        <div style={{ marginTop: 4 }}>
          {(p.steps ?? []).map((s, i) => (
            <div key={i} className="ndv__step-detail">
              <div>{s.device} — {s.applied ? tt("ndv.pt.applied") : s.error ? "❌ " + s.error : tt("ndv.pt.notApplied")}</div>
              <div className="ndv__meta">{tt("ndv.pt.changeLine", { list: (s.commands ?? []).join("；") })}</div>
              <div className="ndv__meta">{tt("ndv.pt.rollbackLine", { list: (s.rollback ?? []).join("；") })}</div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// parsePortList turns the discovery dialog's free-text port list ("22, 161，
// 830") into numbers; empty/unparsable entries drop out and an empty result
// means "backend defaults" (22/23/161/443/830).
function parsePortList(s: string): number[] {
  return s.split(/[,，\s]+/).map(p => parseInt(p, 10)).filter(n => Number.isInteger(n) && n > 0 && n < 65536);
}

// TopologyMap: tiered layout — connection degree infers 核心/汇聚/接入 bands
// (real ops shape), unmanaged neighbors sink to the bottom in grey. Ports ride
// the link as a hover tooltip; clicking a managed node selects the device
// (onPick); `selected` highlights the picked node.
function TopologyMap({ graph, selected, selectedAddr, onPick, health }: { graph: NetDevTopologyGraph; selected?: string; selectedAddr?: string; onPick?: (name: string) => void; health?: Record<string, NetDevDeviceHealth> }) {
  const W = 520, H_MAX = 360;
  // Null-hardening (2026-08-19 exe crash: "e.nodes is not iterable"): the
  // measured snapshot can arrive with null nodes/edges when no devices are
  // configured — normalize once, never iterate raw.
  const nodes = graph?.nodes ?? [];
  const edges = graph?.edges ?? [];
  // Degree per node (managed nodes only drive tiering).
  const degree = new Map<string, number>();
  for (const n of nodes) degree.set(n.name, 0);
  for (const e of edges) {
    if (e.local_device !== e.remote_device) {
      degree.set(e.local_device, (degree.get(e.local_device) ?? 0) + 1);
      degree.set(e.remote_device, (degree.get(e.remote_device) ?? 0) + 1);
    }
  }
  const managed = nodes.filter(v => v.managed);
  // Max degree for the fallback tiering.
  const maxDeg = Math.max(1, ...managed.map(v => degree.get(v.name) ?? 0));
  // Tier: the backend's local inference wins when present (IP-plan view);
  // otherwise fall back to the degree heuristic (measured view).
  const tierOfNode = (v: { name: string; managed: boolean; tier?: number }): number => {
    if (v.tier !== undefined && v.tier >= 0) return v.tier;
    if (!v.managed) return 3;
    const d = degree.get(v.name) ?? 0;
    if (d >= maxDeg * 0.6) return 0;
    if (d >= maxDeg * 0.3) return 1;
    return 2;
  };
  // Unmanaged neighbors get their own bottom band (tier 3).
  const bands: string[][] = [[], [], [], []];
  for (const v of nodes) bands[tierOfNode(v)].push(v.name);
  const pos = new Map<string, { x: number; y: number }>();
  const bandY = [46, 148, 250, 322];
  bands.forEach((band, bi) => {
    const n = band.length;
    band.forEach((name, i) => {
      pos.set(name, { x: n === 1 ? W / 2 : 36 + ((W - 72) * i) / Math.max(n - 1, 1), y: bandY[bi] });
    });
  });


  // Height ends at the last USED band — unused bottom bands must not inflate
  // the viewBox (the SVG scales to dock width, so excess height magnifies the
  // empty tail below the graph).
  const lastUsedBand = bands.reduce((acc, b, i) => (b.length > 0 ? i : acc), -1);
  const H = lastUsedBand >= 0 ? Math.min(H_MAX, Math.max(150, bandY[lastUsedBand] + 34)) : 110;
  const node = (name: string) => nodes.find(v => v.name === name);
  const TIER_LABEL = [tt("ndv.topo.tier1"), tt("ndv.topo.tier2"), tt("ndv.topo.tier3"), tt("ndv.topo.tier4")];
  return (
    <div>
      <svg viewBox={`0 0 ${W} ${H}`} style={{ width: "100%", marginTop: 8, display: "block" }}>
        {bandY.map((y, i) =>
          bands[i].length > 0 ? (
            <text key={i} x={10} y={y - 20} fontSize={10} fill="var(--fg-faint)" opacity={0.8}>
              {TIER_LABEL[i]}
            </text>
          ) : null,
        )}
        {edges.map((e, i) => {
          const pa = pos.get(e.local_device), pb = pos.get(e.remote_device);
          if (!pa || !pb) return null;
          return (
            <line
              key={i}
              x1={pa.x} y1={pa.y} x2={pb.x} y2={pb.y}
              stroke="var(--fg-faint)"
              strokeWidth={1.2}
              opacity={0.65}
            >
              <title>{`${e.local_device}:${e.local_port} ⇄ ${e.remote_device}${e.remote_port ? ":" + e.remote_port : ""} (${e.source})${e.platform ? " · " + e.platform : ""}`}</title>
            </line>
          );
        })}
        {nodes.map(v => {
          const p = pos.get(v.name);
          if (!p) return null;
          const tier = tierOfNode(v);
          const nv = node(v.name);
          const sel = !!v.managed && (!!selected && (v.name === selected || nv?.device_ip === selectedAddr));
          const roleStroke = v.managed ? "var(--fg)" : "var(--fg-faint)";
          return (
            <g
              key={v.name}
              style={{ cursor: v.managed && onPick ? "pointer" : "default" }}
              onClick={() => v.managed && onPick?.(v.name)}
            >
              <rect
                x={p.x - 36} y={p.y - 17} width={72} height={34} rx={6}
                fill={sel
                  ? "var(--accent-soft)"
                  : v.managed ? "color-mix(in srgb, var(--fg) 5%, var(--bg-elev))" : "transparent"}
                stroke={(() => {
                  const hv = v.managed ? health?.[v.name] : undefined;
                  if (hv && !hv.reachable) return "var(--danger)";
                  if (hv && (hv.interfaces ?? []).some(i => i.adminUp && !i.operUp)) return "var(--warn)";
                  return sel
                    ? "var(--accent)"
                    : v.managed ? (tier === 0 ? "var(--accent)" : "var(--fg-faint)") : "var(--border)";
                })()}
                strokeWidth={sel ? 1.6 : v.managed && tier === 0 ? 1.5 : 1}
                strokeDasharray={v.managed ? undefined : "3 3"}
              />
              <TopoIcon role={v.role ?? ""} x={p.x - 8} y={p.y - 15} size={16} color={roleStroke} />
              <text x={p.x} y={p.y + 12} textAnchor="middle" fontSize={9} fill={v.managed ? "var(--fg)" : "var(--fg-faint)"}>
                {v.name.length > 8 ? v.name.slice(0, 8) + "…" : v.name}
              </text>
              <title>{`${v.name}${nv?.device_ip ? " · " + nv.device_ip : ""}${v.subnet ? " · " + v.subnet : ""}${v.role ? " · " + tt(topoRoleKey(v.role) as never) + " (" + (v.role_source ?? "none") + ")" : ""}${v.managed ? tt("ndv.topo.managed") : tt("ndv.topo.unmanaged")}${health?.[v.name] ? (health[v.name].reachable ? tt("ndv.topo.up") : tt("ndv.topo.down")) : ""}${v.tier !== undefined && v.tier >= 0 ? tt("ndv.topo.tierNote") : tt("ndv.topo.degreeNote", { n: degree.get(v.name) ?? 0 })}${v.managed && onPick ? tt("ndv.topo.clickDevice") : ""}`}</title>
            </g>
          );
        })}
      </svg>
      <div style={{ opacity: 0.6, fontSize: 11 }}>
        {tt("ndv.topo.legend", { m: nodes.filter(v => v.managed).length, u: nodes.filter(v => !v.managed).length, e: edges.length, at: graph.at })}
      </div>
    </div>
  );
}
