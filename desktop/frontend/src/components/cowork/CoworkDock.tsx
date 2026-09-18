// CoworkDock is the cowork-mode right-side panel.
//
// Reverse-engineered 1:1 from the 7/15 release bundle. Two modes, both using
// the coding-mode workbench-dock tab strip classes (workbench-dock__tools /
// __tabs / __tab) so styling reads as the same control as the coding-mode right
// dock:
//   - mode="default" (Kp): 3 tabs — 今日 / 邮件 / 文件.
//   - mode="rag"     (Gp): 4 tabs — 集合 / 实体 / 文件 / 提取.
//
// In RAG mode, the body is replaced by EntityDetail / DocPreview when an entity
// or document is opened; rag:entity-click (emitted by the graph canvas) and
// rag:progress / rag:changed (emitted by the backend) are subscribed so the
// dock stays live.
//
// All backend methods are wrapped with .catch fallbacks since the backend may
// not implement every one yet.

import { useCallback, useEffect, useState } from "react";
import {
  Activity,
  CalendarClock,
  CalendarDays,
  ChevronDown,
  ChevronRight,
  CornerDownRight,
  FileText,
  Folder,
  FolderPlus,
  Mail,
  Coffee,
  MessageSquare,
  MonitorPlay,
  PartyPopper,

  RefreshCw,
  Search,
  Trash2,
  X,
  Zap,
  Bot,
  Sparkles,
} from "lucide-react";

import { app, onRagChanged, onRagProgress } from "../../lib/bridge";
import { subscribeBrowserMirrorFocus } from "../../lib/browserMirror";
import { useToast } from "../../lib/toast";
import { CustomSelect } from "./CustomSelect";
import { ContextPanel } from "../ContextPanel";
import { DockTabs, useDockTabState } from "../DockTabs";
import { BrowserMirrorPanel } from "./BrowserMirrorPanel";

// realApp mirrors bridge.ts's private helper: returns the Wails binding only
// when window.go.main.App is present (i.e. we are inside the desktop shell).
function realApp(): unknown | undefined {
  return typeof window !== "undefined"
    ? (window as unknown as { go?: { main?: { App?: unknown } } }).go?.main?.App
    : undefined;
}
import { useT } from "../../lib/i18n";
import type {
  CalendarEventView,
  ContextInfo,
  MailProbeResult,
  RagCollectionView,
  RagNodeView,
  TaskView,
  BotDockStatusView,
} from "../../lib/types";
import { WorkspacePanel } from "../WorkspacePanel";
import { EntityDetail } from "./EntityDetail";
import { DocPreview } from "./DocPreview";
import { TemplateSelect } from "./TemplateSelect";
import { RagNode } from "./RagNode";
import { ImportModal } from "./ImportModal";
import { ConfirmModal } from "../ConfirmModal";
import { useConfirm } from "../../lib/confirm";

// PALETTE (Bp) is the fallback color list for calendar events without an
// explicit color. The original bundle uses a small CSS-var palette; index 0 is
// the default returned by eventColor.
const PALETTE: string[] = [
  "var(--accent, #58a6ff)",
  "var(--ok, #3fb950)",
  "var(--warning, #d29922)",
  "var(--danger, #f85149)",
  "var(--info, #58a6ff)",
];

// InboxItem is the trimmed envelope returned by app.InboxPreview. types.ts
// declares preview as required, but the backend may omit it on some mails, so
// we mirror the runtime shape with an optional preview here.
interface InboxItem {
  from: string;
  to: string;
  date: string;
  subject: string;
  preview?: string;
}

// Window.runtime type for the wails EventsOn binding.
interface WailsRuntimeLike {
  EventsOn(name: string, cb: (...args: unknown[]) => void): () => void;
}

// ===========================================================================
// helpers
// ===========================================================================

// eventColor ($p) returns the event's own color or the first palette entry.
function eventColor(e: CalendarEventView): string {
  return e.color || PALETTE[0];
}

// formatEventTime (Hp) renders a Date as "HH:MM" (24h, locale-stripped), or ""
// when the input is not a valid date.
function formatEventTime(value: string): string {
  const d = new Date(value);
  if (isNaN(d.getTime())) return "";
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
}

// formatDateTime (Up) renders a Date as "M/D HH:MM" within the current year,
// or "YYYY/M/D HH:MM" otherwise. "" on parse failure.
function formatDateTime(value: string): string {
  const d = new Date(value);
  if (isNaN(d.getTime())) return "";
  const now = new Date();
  const sameYear = d.getFullYear() === now.getFullYear();
  const base = `${d.getMonth() + 1}/${d.getDate()} ${formatEventTime(value)}`;
  return sameYear ? base : `${d.getFullYear()}/${base}`;
}

// ===========================================================================
// CoworkDock (qp) — top-level switch between default and RAG modes
// ===========================================================================

export interface CoworkDockProps {
  cwd?: string;
  maximized: boolean;
  onClose: () => void;
  onToggleMaximized: () => void;
  mode?: "default" | "rag";
  onEntityClick?: (name: string) => void;
  onFileClick?: (path: string) => void;
  // Context overview tab data — forwarded to DefaultDock's "概览" (Overview)
  // tab, which renders the slim ContextPanel (stats strip + turn facts).
  // busy disables the compact button while the active tab is streaming.
  contextInfo?: ContextInfo;
  sessionTokens?: number;
  activeTabId?: string;
  dockRefreshKey?: number;
  busy?: boolean;
}

export function CoworkDock({
  cwd,
  maximized,
  onClose,
  onToggleMaximized,
  mode = "default",
  onEntityClick,
  onFileClick,
  contextInfo,
  sessionTokens,
  activeTabId,
  dockRefreshKey,
  busy,
}: CoworkDockProps) {
  return mode === "rag" ? (
    <RagDock onClose={onClose} onEntityClick={onEntityClick} onFileClick={onFileClick} />
  ) : (
    <DefaultDock
      cwd={cwd}
      maximized={maximized}
      onClose={onClose}
      onToggleMaximized={onToggleMaximized}
      contextInfo={contextInfo}
      sessionTokens={sessionTokens}
      activeTabId={activeTabId}
      refreshKey={dockRefreshKey}
      busy={busy}
    />
  );
}

// loadDockTabState/useDockTabState live in DockTabs.tsx (shared with the
// netdev dock).

// ===========================================================================
// DefaultDock (Kp) — 今日 / 邮件 / 文件 / 概览
// ===========================================================================

type DefaultTab = "today" | "mail" | "files" | "overview" | "browser";
// "browser" (the agent-browser mirror) is deliberately NOT in the default
// open set: per the pane-system's context-driven principle the tab appears
// when browsing activity starts (or via the "+" menu), not by default.
const DEFAULT_TAB_CATALOG: readonly DefaultTab[] = ["today", "mail", "files", "overview"];
const COWORK_DOCK_TABS_KEY = "hiq.coworkDockTabs";

function DefaultDock({
  cwd,
  maximized,
  onClose,
  onToggleMaximized,
  contextInfo,
  sessionTokens,
  activeTabId,
  refreshKey,
  busy,
}: {
  cwd?: string;
  maximized: boolean;
  onClose: () => void;
  onToggleMaximized: () => void;
  contextInfo?: ContextInfo;
  sessionTokens?: number;
  activeTabId?: string;
  refreshKey?: number;
  busy?: boolean;
}) {
  const t = useT();
  const [tab, setTab] = useState<DefaultTab>("today");
  const [openTabs, setOpenTabs] = useDockTabState(COWORK_DOCK_TABS_KEY, DEFAULT_TAB_CATALOG);

  // Active tab closed (or restored state desyncs) → fall back to the last
  // open one, exactly like the coding dock's dockTabs effect.
  useEffect(() => {
    if (!openTabs.includes(tab)) {
      setTab(openTabs[openTabs.length - 1] ?? "today");
    }
  }, [openTabs, tab]);

  const closeTab = (key: DefaultTab) => {
    setOpenTabs((prev) => {
      const next = prev.filter((m) => m !== key);
      if (next.length === 0) {
        // Closing the last tab closes the whole dock (coding-dock behavior).
        onClose();
        return prev;
      }
      setTab((active) => (active === key ? next[next.length - 1] : active));
      return next;
    });
  };

  const openTab = (key: DefaultTab) => {
    setOpenTabs((prev) => (prev.includes(key) ? prev : [...prev, key]));
    setTab(key);
  };

  // Mirror-tab focus requests come from App (browser activity onset): ensure
  // the tab exists and switch to it. Setters are stable, so an empty dep list
  // keeps one subscription per dock mount.
  useEffect(
    () =>
      subscribeBrowserMirrorFocus(() => {
        setOpenTabs((prev) => (prev.includes("browser") ? prev : [...prev, "browser"]));
        setTab("browser");
      }),
    [],
  );

  const TAB_DEFS: { key: DefaultTab; label: string; icon: React.ReactNode }[] = [
    { key: "today", label: t("coworkDock.today"), icon: <CalendarDays size={13} /> },
    { key: "mail", label: t("coworkDock.mail"), icon: <Mail size={13} /> },
    { key: "files", label: t("coworkDock.files"), icon: <FileText size={13} /> },
    { key: "overview", label: t("coworkDock.overview"), icon: <Activity size={13} /> },
    { key: "browser", label: t("coworkDock.browser"), icon: <MonitorPlay size={13} /> },
  ];

  return (
    <aside className="cowork-dock" aria-label={t("coworkDock.label")}>
      <DockTabs
        tabs={TAB_DEFS}
        openTabs={openTabs}
        active={tab}
        onSelect={openTab}
        onClose={closeTab}
        listLabel={t("coworkDock.label")}
        closeLabel={t("dock.closeTab")}
        addLabel={t("dock.addTab")}
      />

      <div className="cowork-dock__body">
        {tab === "today" && <TodayView />}
        {tab === "mail" && <MailView />}
        {tab === "files" &&
          (cwd ? (
            <WorkspacePanel
              open
              cwd={cwd}
              maximized={maximized}
              onClose={onClose}
              onToggleMaximized={onToggleMaximized}
              showViewTabs={false}
              initialViewMode="files"
              onAddToChat={(text: string) => window.dispatchEvent(new CustomEvent("cowork:insert-text", { detail: text }))}
            />
          ) : (
            <div className="cowork-dock__empty-state">
              <FileText size={22} />
              <p>{t("coworkDock.noWorkspace")}</p>
              <p className="cowork-dock__empty-hint">
                {t("coworkDock.noWorkspaceHint")}
              </p>
            </div>
          ))}
        {tab === "overview" && (
          <ContextPanel
            tabId={activeTabId}
            context={contextInfo}
            sessionTokens={sessionTokens}
            refreshKey={refreshKey}
            busy={busy}
          />
        )}
        {tab === "browser" && <BrowserMirrorPanel />}
      </div>
    </aside>
  );
}

// ===========================================================================
// TodayView (Wp) — 今日日程 / 邮箱 / 自动化
// ===========================================================================

function TodayView() {
  const t = useT();
  const [events, setEvents] = useState<CalendarEventView[] | null>(null);
  const [tasks, setTasks] = useState<TaskView[] | null>(null);
  const [probe, setProbe] = useState<MailProbeResult | null>(null);
  const [inbox, setInbox] = useState<InboxItem[] | null>(null);
  const [holidays, setHolidays] = useState<CalendarEventView[]>([]);
  const [botStatus, setBotStatus] = useState<BotDockStatusView | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(() => {
    setLoading(true);
    const now = new Date();
    const since = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(
      now.getDate(),
    ).padStart(2, "0")}`;
    const next = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1);
    const before = `${next.getFullYear()}-${String(next.getMonth() + 1).padStart(2, "0")}-${String(
      next.getDate(),
    ).padStart(2, "0")}`;

    // 1. 优先快加载：日程、任务、节假日、bot状态（本地数据，几乎 0 延迟）
    Promise.all([
      (app as unknown as { ListCalendarEvents: (s: string, b: string) => Promise<CalendarEventView[]> })
        .ListCalendarEvents(since, before)
        .catch(() => [] as CalendarEventView[]),
      (app as unknown as { ListScheduledTasks: () => Promise<TaskView[]> })
        .ListScheduledTasks()
        .catch(() => [] as TaskView[]),
      (app as unknown as { GetChineseHolidays?: (year: number) => Promise<CalendarEventView[]> })
        .GetChineseHolidays?.(now.getFullYear())
        .catch(() => [] as CalendarEventView[]) ?? Promise.resolve([] as CalendarEventView[]),
      (app as unknown as { BotDockStatus?: () => Promise<BotDockStatusView> })
        .BotDockStatus?.()
        .catch(() => null as BotDockStatusView | null) ?? Promise.resolve(null as BotDockStatusView | null),
    ]).then(([evs, tks, hols, bs]) => {
      setEvents(evs);
      setTasks(tks);
      setHolidays(hols);
      setBotStatus(bs);
    });

    // 2. 异步慢加载：邮件探针与 50 封邮件列表（远程请求）
    Promise.all([
      (app as unknown as { ProbeMailAccount: (name: string) => Promise<MailProbeResult> })
        .ProbeMailAccount("")
        .catch(() => ({ ok: false, status: "error", message: t("cowork.wailsError") } as MailProbeResult)),
      (app as unknown as { InboxPreview?: (mailbox: string, n: number) => Promise<InboxItem[]> })
        .InboxPreview?.("INBOX", 10)
        .catch(() => [] as InboxItem[]) ?? Promise.resolve([] as InboxItem[]),
    ]).then(([mb, inb]) => {
      setProbe(mb);
      setInbox(inb);
      setLoading(false); // 慢查询完成后关闭 loading 状态
    });
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  // 移除整页阻塞 loading，改为局部静默刷新，防止切换页卡卡顿

  const now = new Date();
  // todayItems merges today's calendar events AND today's enabled scheduled
  // tasks into a single unified list, so the user sees "everything happening
  // today" in one place instead of two disconnected sections. Each item carries
  // a `kind` so we can visually distinguish events (📅) from auto-tasks (⚡).
  // Events use .start ("YYYY-MM-DDTHH:MM"), tasks use .nextRun ("YYYY-MM-DD HH:MM",
  // space-separated) — both parse fine via new Date().
  type TodayItem = {
    id: string;
    title: string;
    time: Date;
    allDay: boolean;
    location: string;
    color: string;
    outputMode: string;
    kind: "event" | "task";
  };
  const isToday = (d: Date) =>
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  const todayItems: TodayItem[] = [
    ...(events ?? [])
      .filter((e) => isToday(new Date(e.start)))
      .map((e) => ({
        id: e.id,
        title: e.title,
        time: new Date(e.start),
        allDay: e.allDay,
        location: e.location,
        color: eventColor(e),
        outputMode: e.outputMode,
        kind: "event" as const,
      })),
    ...(tasks ?? [])
      .filter((tk) => tk.enabled && tk.nextRun && isToday(new Date(tk.nextRun)))
      .map((tk) => ({
        id: tk.id,
        title: tk.name,
        time: new Date(tk.nextRun),
        allDay: false,
        location: tk.location ?? "",
        color: tk.color || "#8b949e",
        outputMode: tk.outputMode,
        kind: "task" as const,
      })),
  ].sort((a, b) => a.time.getTime() - b.time.getTime());

  // Holiday hint: is today a holiday? If not, find the next upcoming one so
  // the user sees "距 XX节还有 N 天" in the briefing.
  const todayStr = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
  const todayHoliday = holidays.find((h) => {
    const hs = new Date(h.start);
    const he = new Date(h.end);
    return (now >= hs && now < he) || h.start === todayStr;
  });
  const nextHoliday = !todayHoliday
    ? holidays
        .filter((h) => new Date(h.start) > now)
        .sort((a, b) => a.start.localeCompare(b.start))[0]
    : undefined;
  const daysToNext = nextHoliday
    ? Math.ceil((new Date(nextHoliday.start).getTime() - Date.now()) / 86400000)
    : 0;

  const mailOk = probe?.status === "ok";
  const unreadCount = inbox?.length ?? 0;

  return (
    <div className="cowork-today" style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div style={{ flex: 1, overflowY: "auto", paddingBottom: "16px" }}>
        {/* 1. 顶部简报卡片 */}
        <div className="cowork-today__briefing">
          <div className="cowork-today__briefing-head" style={{ display: "flex", alignItems: "center", justifyContent: "flex-start" }}>
            <Sparkles size={14} style={{ color: "var(--accent)", marginRight: "4px" }} />
            <span style={{ fontWeight: 600 }}>{t("cowork.todaySchedule")}</span>
          </div>
          <div className="cowork-today__briefing-body" style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
            <div>1. <CalendarClock size={13} style={{ color: "var(--fg-faint)", margin: "0 2px", verticalAlign: "middle" }} />{t("cowork.todayItems", { count: String(todayItems.length) })}</div>
            <div>2. <Mail size={13} style={{ color: "var(--fg-faint)", margin: "0 2px", verticalAlign: "middle" }} />{t("cowork.unreadMails", { count: String(unreadCount) })}</div>
            <div>3. <Coffee size={13} style={{ margin: "0 2px", verticalAlign: "middle" }} /> {t("cowork.noUrgenda")}</div>
            <div>4. <Bot size={13} style={{ margin: "0 2px", verticalAlign: "middle" }} /> {t("cowork.clickForSummary")}</div>
            {todayHoliday && (
              <div style={{ color: "var(--err)", fontWeight: 600 }}><PartyPopper size={13} style={{ margin: "0 2px", verticalAlign: "middle" }} /> {t("cowork.holidayGreeting", { name: todayHoliday.title })}</div>
            )}
            {!todayHoliday && nextHoliday && daysToNext <= 14 && (
              <div style={{ color: "var(--warn)" }}><CalendarDays size={13} style={{ margin: "0 2px", verticalAlign: "middle" }} /> {t("cowork.daysToHoliday", { name: nextHoliday.title, count: String(daysToNext) })}</div>
            )}
          </div>
          <div className="cowork-today__briefing-actions">
            <button 
              className="rag-toolbar__btn" 
              style={{ background: "var(--accent)", color: "var(--accent-fg)", border: "none" }}
              onClick={() => {
                const prompt = `请生成今日行政决策早报。在早报开头，请务必直接列出以下现状信息：\n1. 今日安排核心日程 ${todayItems.length} 项。\n2. 待处理未读件 ${unreadCount} 封。\n3. 当前时段的紧迫议程。\n4. 昨日邮件的重要内容。\n\n接下来，请调用邮箱等工具分析上述内容，并向我简炼总结：今日还需要做的事情，以及昨天已经进行或遗留的重要事项。`;
                window.dispatchEvent(new CustomEvent("cowork:insert-text", { detail: prompt }));
              }}
            >
              <Sparkles size={13} /> {t("cowork.genDeepGuide")}
            </button>
          </div>
        </div>

      <section className="cowork-today__section" style={{ marginTop: "20px", paddingBottom: "12px" }}>
        <h4 className="cowork-today__heading">
          <CalendarClock size={13} />
          {t("coworkDock.todayTodo")}
          <span className="cowork-today__heading-count">{todayItems.length}</span>
        </h4>
        {todayItems.length === 0 ? (
          <div className="cowork-today__empty">{t("coworkDock.noTodo")}</div>
        ) : (
          <ul className="cowork-today__list">
            {todayItems.map((it) => {
              // Past items (time already passed) render dimmed so the user can
              // tell at a glance what's left today vs. what's behind them.
              const past = it.time.getTime() < Date.now();
              return (
                <li
                  key={`${it.kind}-${it.id}`}
                  className="cowork-today__row"
                  style={{ opacity: past ? 0.55 : 1 }}
                >
                  <span className="cowork-today__time">{it.allDay ? t("cal.allDay") : formatEventTime(it.time.toISOString())}</span>
                  <span className="cowork-today__dot" style={{ background: it.color }} />
                  <span className="cowork-today__text" title={it.title + (it.location ? ` @ ${it.location}` : "")}>
                    {it.title}
                  </span>
                  {/* kind badge: 📅 event vs ⚡ task, so the two underlying systems
                      are still distinguishable inside the unified list. */}
                  <span className="cowork-today__kind" title={it.kind === "event" ? t("cowork.calendarEvent") : t("cowork.scheduledTask")}>
                    {it.kind === "event" ? <CalendarDays size={12} /> : <Zap size={12} />}
                  </span>
                  {/* output-mode hint when the item pushes to IM/email */}
                  {it.outputMode === "im" && <span className="cowork-today__out" title={t("cowork.pushToIM")}><MessageSquare size={12} /></span>}
                  {it.outputMode === "email" && <span className="cowork-today__out" title={t("cowork.pushToEmail")}><Mail size={12} /></span>}
                </li>
              );
            })}
          </ul>
        )}
      </section>

      </div> {/* End of scrollable area */}

      {/* 4. 底部的统合状态控制卡片 (固定在底部) */}
      <div style={{ 
        margin: "0 16px 16px", 
        background: "var(--bg-elev)", 
        border: "1px solid var(--border-soft)", 
        borderRadius: "12px", 
        boxShadow: "0 2px 8px rgba(0,0,0,0.02)",
        overflow: "hidden",
        flexShrink: 0
      }}>
        {/* 上半区：状态列表 (纵向堆叠，解决横向空间不足) */}
        <div style={{ display: "flex", flexDirection: "column" }}>
          {/* 邮箱行 */}
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", padding: "10px 16px", borderBottom: "1px solid var(--border-soft)" }}>
            <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
              <Mail size={14} style={{ color: "var(--fg-dim)" }} />
              <span style={{ fontSize: "13px", fontWeight: 500 }}>{t("cowork.mailbox")}</span>
              <span className={"mail-status-dot mail-status-dot--" + (loading ? "warning" : (mailOk ? "ok" : "error"))} style={{ marginLeft: "4px" }} />
              <span style={{ fontSize: "12px", color: "var(--fg-dim)", maxWidth: "80px", whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>
                {loading ? t("cowork.syncing") : (mailOk ? t("cowork.connected") : (probe?.message))}
              </span>
            </div>
            <div>
              {unreadCount > 0 ? (
                <span style={{ color: "var(--danger)", fontWeight: 600, fontSize: "12px" }}>{t("cowork.nUnread", { count: String(unreadCount) })}</span>
              ) : (
                <span style={{ color: "var(--fg-faint)", fontSize: "12px" }}>{t("cowork.zeroUnread")}</span>
              )}
            </div>
          </div>

          {/* IM bot 行 — real status from BotDockStatus (not hardcoded). Click
              the row to jump to Settings → Bots for connection details. */}
          <div
            style={{ display: "flex", alignItems: "center", justifyContent: "space-between", padding: "10px 16px", cursor: "pointer" }}
            onClick={() => window.dispatchEvent(new CustomEvent("app:open-settings-tab", { detail: "bots" }))}
            title={t("cowork.imBotDetail")}
          >
            <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
              <Bot size={14} style={{ color: "var(--fg-dim)" }} />
              <span style={{ fontSize: "13px", fontWeight: 500 }}>IM bot</span>
              <span className={"mail-status-dot mail-status-dot--" + (loading ? "warning" : (botStatus?.online ? "ok" : "idle"))} style={{ marginLeft: "4px" }} />
              <span style={{ fontSize: "12px", color: "var(--fg-dim)" }}>
                {loading ? t("cowork.syncing") : (botStatus?.online
                  ? (botStatus.platforms.length > 0 ? t("cowork.onlinePlatforms", { platforms: botStatus.platforms.join(", ") }) : t("cowork.online"))
                  : t("cowork.notStarted"))}
              </span>
            </div>
            <div>
              <span style={{ color: "var(--fg-faint)", fontSize: "12px" }}>
                {botStatus?.online && botStatus.recentCount > 0
                  ? t("cowork.nRecentConvs", { count: String(botStatus.recentCount) })
                  : t("cowork.noMessages")}
                <span style={{ marginLeft: "4px", opacity: 0.6 }}>›</span>
              </span>
            </div>
          </div>
        </div>

        {/* 下半区：刷新操作条 */}
        <div 
          className="cowork-today__refresh-btn"
          style={{ 
            display: "flex", 
            justifyContent: "center", 
            alignItems: "center", 
            gap: "6px",
            padding: "8px", 
            background: "rgba(0,0,0,0.02)", 
            borderTop: "1px solid var(--border-soft)",
            color: "var(--fg-dim)",
            fontSize: "12px",
            cursor: "pointer",
            transition: "all 0.2s"
          }}
          onClick={refresh}
        >
          <RefreshCw size={12} className={loading ? "spin" : ""} />
          {t("cowork.refreshStatus")}
        </div>
      </div>
    </div>
  );
}

// ===========================================================================
// MailView (Vp) — 邮件列表 + probe 状态
// ===========================================================================

function MailView() {
  const t = useT();
  const [probe, setProbe] = useState<MailProbeResult | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [openKey, setOpenKey] = useState<string | null>(null);
  // folder: "inbox" (unread INBOX) or "sent" (Sent folder). Drives which mailbox
  // InboxPreview reads and the tab label.
  const [folder, setFolder] = useState<"inbox" | "sent">("inbox");

  // Cache both folders so switching inbox/sent is instant (no re-fetch).
  const [inboxData, setInboxData] = useState<InboxItem[]>([]);
  const [sentData, setSentData] = useState<InboxItem[]>([]);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      // Fetch both folders at once (probe + inbox 30 + sent 10). This runs on
      // mount and on explicit refresh-button click — NOT on folder switch, so
      // switching tabs is instant.
      const [mb, inb, sent] = await Promise.all([
        (app as unknown as { ProbeMailAccount: (name: string) => Promise<MailProbeResult> })
          .ProbeMailAccount("")
          .catch(() => ({ ok: false, status: "error", message: t("cowork.wailsError") } as MailProbeResult)),
        (app as unknown as { InboxPreview?: (mailbox: string, n: number) => Promise<InboxItem[]> })
          .InboxPreview?.("INBOX", 30) ??
          Promise.resolve([] as InboxItem[]),
        (app as unknown as { InboxPreview?: (mailbox: string, n: number) => Promise<InboxItem[]> })
          .InboxPreview?.("Sent", 10) ??
          Promise.resolve([] as InboxItem[]),
      ]);
      setProbe(mb);
      setInboxData(inb);
      setSentData(sent);
    } catch (e) {
      setError(String(e instanceof Error ? e.message : e));
    } finally {
      setLoading(false);
    }
  }, []);

  // Load once on mount only (not on folder switch).
  useEffect(() => {
    refresh();
  }, [refresh]);

  // The displayed list comes from the cached folder data — instant switch.
  const inbox = folder === "sent" ? sentData : inboxData;

  const mailOk = probe?.status === "ok";
  const mailUnconfigured = probe?.status === "unconfigured" || !probe;

  return (
    <div className="cowork-mailtab">
      <div className="cowork-mailtab__head">
        <div className="cowork-mailtab__status">
          <span
            className={"mail-status-dot mail-status-dot--" + (mailOk ? "ok" : mailUnconfigured ? "idle" : "error")}
          />
          <span>
            {mailUnconfigured
              ? t("coworkDock.mailUnconfigured")
              : mailOk
                ? t("coworkDock.mailConnected")
                : probe?.message || t("coworkDock.mailError")}
          </span>
        </div>
        <button
          className="cowork-mailtab__refresh"
          onClick={() => refresh()}
          title={t("common.refresh")}
        >
          <RefreshCw size={13} className={loading ? "spin" : ""} />
        </button>
      </div>

      {/* Inbox / Sent folder switch. Only show when mail is configured. */}
      {!mailUnconfigured && !error && (
        <div className="cowork-mailtab__folders">
          <button
            className={"cowork-mailtab__folder" + (folder === "inbox" ? " cowork-mailtab__folder--active" : "")}
            onClick={() => setFolder("inbox")}
          >
            {t("coworkDock.mailInbox")}
          </button>
          <button
            className={"cowork-mailtab__folder" + (folder === "sent" ? " cowork-mailtab__folder--active" : "")}
            onClick={() => setFolder("sent")}
          >
            {t("coworkDock.mailSent")}
          </button>
        </div>
      )}

      {loading ? (
        <div className="cowork-dock__loading">…</div>
      ) : error ? (
        <div className="cowork-today__empty">{error}</div>
      ) : mailUnconfigured ? (
        <div className="cowork-dock__empty-state">
          <Mail size={22} />
          <p>{t("coworkDock.mailUnconfigured")}</p>
          <p className="cowork-dock__empty-hint">
            {t("coworkDock.mailConfigureHint")}
          </p>
        </div>
      ) : mailOk ? (
        inbox && inbox.length !== 0 ? (
          <ul className="cowork-mailtab__list">
            {inbox.map((m, i) => {
              const key = `${m.date}-${i}`;
              const open = openKey === key;
              return (
                <li
                  key={key}
                  className={"cowork-mailtab__item" + (open ? " cowork-mailtab__item--open" : "")}
                  onClick={() => setOpenKey(open ? null : key)}
                >
                  <div className="cowork-mailtab__item-head">
                    {/* Inbox shows sender (from); Sent shows recipient (to). */}
                    <span className="cowork-mailtab__from" title={folder === "sent" ? m.to : m.from}>
                      {folder === "sent" ? (m.to) : m.from}
                    </span>
                    <span className="cowork-mailtab__date">{formatDateTime(m.date)}</span>
                  </div>
                  <div className="cowork-mailtab__subject" title={m.subject}>
                    {m.subject}
                  </div>
                  {open && m.preview && <div className="cowork-mailtab__preview">{m.preview}</div>}
                </li>
              );
            })}
          </ul>
        ) : (
          <div className="cowork-today__empty">{t("coworkDock.noUnreadMail")}</div>
        )
      ) : (
        <div className="cowork-today__empty">{probe?.message || t("coworkDock.mailError")}</div>
      )}
    </div>
  );
}

// 递归过滤 RagNodeView 树结构，若子树有匹配项则保留并展示父级文件夹
function filterRagTree(nodes: RagNodeView[], q: string): RagNodeView[] {
  if (!q || !q.trim()) return nodes;
  const lower = q.trim().toLowerCase();
  const res: RagNodeView[] = [];
  for (const node of nodes) {
    const selfMatch = node.label.toLowerCase().includes(lower) || (node.path && node.path.toLowerCase().includes(lower));
    const kidMatch = node.children ? filterRagTree(node.children, q) : undefined;
    if (selfMatch || (kidMatch && kidMatch.length > 0)) {
      res.push({
        ...node,
        children: kidMatch && kidMatch.length > 0 ? kidMatch : node.children,
      });
    }
  }
  return res;
}

// ===========================================================================
// RagDock (Gp) — 分类 / 文件（精简为 2 tab）
// ===========================================================================

type RagTab = "collections" | "files" | "extract";
const RAG_TAB_CATALOG: readonly RagTab[] = ["collections", "files", "extract"];
const COWORK_RAG_DOCK_TABS_KEY = "hiq.coworkRagDockTabs";

function RagDock({
  onClose,
  onEntityClick,
  onFileClick,
}: {
  // Closing the last strip tab closes the whole dock (coding-dock behavior).
  onClose: () => void;
  onEntityClick?: (name: string) => void;
  onFileClick?: (path: string) => void;
}) {
  const [tab, setTab] = useState<RagTab>("collections");
  const [openTabs, setOpenTabs] = useDockTabState(COWORK_RAG_DOCK_TABS_KEY, RAG_TAB_CATALOG);

  useEffect(() => {
    if (!openTabs.includes(tab)) {
      setTab(openTabs[openTabs.length - 1] ?? "collections");
    }
  }, [openTabs, tab]);

  const closeTab = (key: RagTab) => {
    setOpenTabs((prev) => {
      const next = prev.filter((m) => m !== key);
      if (next.length === 0) {
        onClose();
        return prev;
      }
      setTab((active) => (active === key ? next[next.length - 1] : active));
      return next;
    });
  };

  const openTab = (key: RagTab) => {
    setOpenTabs((prev) => (prev.includes(key) ? prev : [...prev, key]));
    setTab(key);
  };
  const [collections, setCollections] = useState<RagCollectionView[]>([]);
  const [activeCollection, setActiveCollection] = useState("");
  // activeCollections: null = "all selected", string[] = explicit subset.
  const [, setActiveCollections] = useState<string[] | null>(null);
  const [tree, setTree] = useState<RagNodeView[]>([]);
  const [entityName, setEntityName] = useState<string | null>(null);
  const [entityCollection, setEntityCollection] = useState<string | null>(null);
  const [docPath, setDocPath] = useState<string | null>(null);
  const [docCollection, setDocCollection] = useState("");
  const [showCreateModal, setShowCreateModal] = useState(false);
  const [newCollectionName, setNewCollectionName] = useState("");
  const [newCollectionParent, setNewCollectionParent] = useState("");
  const t = useT();
  const { showToast } = useToast();
  const confirm = useConfirm();
  const [expandedCats, setExpandedCats] = useState<Set<string>>(new Set());
  const [catSearch, setCatSearch] = useState("");
  const [fileSearch, setFileSearch] = useState("");
  const [showImportModal, setShowImportModal] = useState(false);
  const [importTargetCol, setImportTargetCol] = useState("");
  const [allExpanded, setAllExpanded] = useState(true);
  // Pending delete-collection confirmation (null = closed). Replaces the native
  // window.confirm() with an app-styled modal.
  const [deleteTarget, setDeleteTarget] = useState<{ name: string; path: string } | null>(null);

  // 当在分类查询框中打字时，自动将含匹配分支的父目录加至展开集合，让层级在视觉上一目了然
  useEffect(() => {
    if (!catSearch.trim()) return;
    const q = catSearch.trim().toLowerCase();
    setExpandedCats((prev) => {
      const next = new Set(prev);
      for (const c of collections) {
        if (c.name.toLowerCase().includes(q) || (c.path && c.path.toLowerCase().includes(q))) {
          if (c.parent) next.add(c.parent);
        }
      }
      return next;
    });
  }, [catSearch, collections]);

  // Auto-expand root nodes only upon initial tree load, respecting user's toggle state thereafter.
  useEffect(() => {
    if (collections.length === 0) return;
    setExpandedCats((prev) => {
      if (prev.size > 0) return prev; // Do not override user's manual collapse/expand actions!
      const next = new Set<string>();
      const allPaths = new Set(collections.map((c) => c.path));
      for (const c of collections) {
        if (collections.some((child) => child.parent === c.path)) {
          next.add(c.path);
        }
        if (c.parent && !allPaths.has(c.parent)) {
          next.add(c.parent);
        }
      }
      return next;
    });
  }, [collections]);

  const toggleCat = (path: string) => {
    setExpandedCats((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  };

  // Import files directly into a specific collection from the dock via modern modal selection.
  const handleImportToCollection = (collectionName: string) => {
    setImportTargetCol(collectionName);
    setShowImportModal(true);
  };

  const refreshCollections = useCallback(() => {
    (app as unknown as { ListRagCollections: () => Promise<RagCollectionView[]> })
      .ListRagCollections()
      .then(setCollections)
      .catch(() => {});
  }, []);

  // Quick extract a single collection (incremental) without switching to the extract tab.
  const handleQuickExtract = async (collectionName: string) => {
    try {
      await app.RagStartExtract(collectionName, "general/graph", "incremental");
      showToast(t("cowork.extracting", { name: collectionName }), "info");
    } catch (e) {
      const msg = String(e);
      if (msg.includes("已提取完成")) {
        showToast(t("cowork.extractDone", { name: collectionName }), "info");
      } else {
        showToast(`${t("cowork.extractFailed")}: ${msg}`, "error");
      }
    }
  };

  // Drag-and-drop import into the dock — drops onto the collections area.
  const [dragOver, setDragOver] = useState(false);
  const handleDrop = async (e: React.DragEvent) => {
    e.preventDefault();
    setDragOver(false);
    // Wails delivers drops to the window-level onFilesDropped handler in RagPanel.
    // Here we just provide the visual drag highlight; the actual import is handled
    // by the existing bridge. The activeCollection at drop time determines target.
  };

  // Initial load: collections + session-scoped collections.
  useEffect(() => {
    (app as unknown as { ListRagCollections: () => Promise<RagCollectionView[]> })
      .ListRagCollections()
      .then(setCollections)
      .catch(() => {});
    (app as unknown as { GetSessionCollections: () => Promise<string[]> })
      .GetSessionCollections()
      .then((e) => {
        setActiveCollections(e.length === 0 ? null : e);
      })
      .catch(() => {});
  }, []);

  const refreshTree = useCallback(() => {
    (app as unknown as { ListRagTree: (c: string) => Promise<RagNodeView[]> })
      .ListRagTree(activeCollection)
      .then(setTree)
      .catch(() => {});
  }, [activeCollection]);

  // Re-fetch tree when active collection changes.
  useEffect(() => {
    (app as unknown as { ListRagTree: (c: string) => Promise<RagNodeView[]> })
      .ListRagTree(activeCollection)
      .then(setTree)
      .catch(() => {});
  }, [activeCollection]);

  // rag:progress + rag:changed subscriptions.
  useEffect(() => {
    const offProgress =
      realApp() &&
      typeof window !== "undefined" &&
      (window as unknown as { runtime?: WailsRuntimeLike }).runtime
        ? (window as unknown as { runtime: WailsRuntimeLike }).runtime.EventsOn("rag:progress", (...args) => {
            const payload = (args?.[0] ?? {}) as Record<string, unknown>;
            // The bundle destructures these but only uses them to trigger a
            // tree refresh, so we just call refreshTree() here.
            void payload;
            refreshTree();
          })
        : () => {};
    const offChanged = onRagChanged(() => {
      refreshTree();
      (app as unknown as { ListRagCollections: () => Promise<RagCollectionView[]> })
        .ListRagCollections()
        .then(setCollections)
        .catch(() => {});
    });
    return () => {
      offProgress();
      offChanged();
    };
  }, [refreshTree]);

  // rag:entity-click → open entity detail (graph click → dock).
  useEffect(() => {
    const handler = (e: Event) => {
      const detail = (e as CustomEvent<{ name?: string; collection?: string }>).detail;
      if (detail?.name) {
        setEntityName(detail.name);
        if (detail.collection) setEntityCollection(detail.collection);
        setDocPath(null);
      }
    };
    window.addEventListener("rag:entity-click", handler);
    return () => window.removeEventListener("rag:entity-click", handler);
  }, []);

  // --- sub-view: entity detail --------------------------------------------
  if (entityName) {
    return (
      <aside className="cowork-dock">
        <EntityDetail
          collection={entityCollection || activeCollection}
          entityName={entityName}
          onBack={() => setEntityName(null)}
          onHighlightInGraph={(name) => {
            setEntityName(name);
            onEntityClick?.(name);
          }}
          onNavigatePeer={(name) => setEntityName(name)}
        />
      </aside>
    );
  }

  // --- sub-view: doc preview ----------------------------------------------
  if (docPath) {
    return (
      <aside className="cowork-dock">
        <DocPreview collection={docCollection} docPath={docPath} onBack={() => setDocPath(null)} />
      </aside>
    );
  }

  // --- main tabbed body (2 tabs: 分类 + 文件) -----------------------------
  return (
    <aside className="cowork-dock" aria-label={t("cowork.knowledgeNav")}>
      <DockTabs
        tabs={[
          { key: "collections", label: t("cowork.categories"), icon: <Folder size={13} /> },
          { key: "files", label: t("cowork.files"), icon: <FileText size={13} /> },
          { key: "extract", label: t("cowork.deepExtract"), icon: <Zap size={13} /> },
        ]}
        openTabs={openTabs}
        active={tab}
        onSelect={openTab}
        onClose={closeTab}
        listLabel={t("cowork.knowledgeNav")}
        closeLabel={t("dock.closeTab")}
        addLabel={t("dock.addTab")}
      />

      <div className="cowork-dock__body">
        {/* === 分类 tab === */}
        {tab === "collections" && (
          <div
            className={`rag-dock__collections ${dragOver ? "rag-dock__collections--drag" : ""}`}
            onDragOver={(e) => { e.preventDefault(); setDragOver(true); }}
            onDragLeave={() => setDragOver(false)}
            onDrop={(e) => void handleDrop(e)}
          >
            <div className="rag-dock__collection-header">
              <span className="rag-dock__collection-title">{t("cowork.activeCollection")}</span>
              <div className="rag-dock__collection-actions">
                <button
                  className="rag-dock__collection-action"
                  onClick={() => {
                    setActiveCollections(null);
                    setActiveCollection("");
                    (app as unknown as { SetSessionCollections: (c: string[]) => Promise<void> })
                      .SetSessionCollections([])
                      .catch(() => {});
                  }}
                >
                  {t("cowork.selectAll")}
                </button>
                <button
                  className="rag-dock__collection-action"
                  title={t("cowork.newCategory")}
                  onClick={() => setShowCreateModal(true)}
                >
                  +
                </button>
              </div>
            </div>

            {/* 实时分类检索筛选框 (Live Category Filter Bar) - 像素级对齐 .mem-search 规范，置于所有树条目最上方 */}
            <div style={{ padding: "6px 8px 6px", borderBottom: "1px solid var(--border-soft)" }}>
              <label className="mem-search" style={{ height: 28, borderRadius: 6, background: "var(--bg-elev)" }}>
                <Search size={13} style={{ color: "var(--fg-dim)", flex: "0 0 auto" }} />
                <input
                  value={catSearch}
                  onChange={(e) => setCatSearch(e.target.value)}
                  placeholder={t("cowork.searchCategories")}
                  style={{ fontSize: "11.5px" }}
                />
                {catSearch && (
                  <button
                    type="button"
                    onClick={() => setCatSearch("")}
                    style={{ border: "none", background: "transparent", cursor: "pointer", color: "var(--fg-dim)", padding: 0, display: "inline-flex" }}
                  >
                    <X size={12} />
                  </button>
                )}
              </label>
            </div>

            {/* "全部" — search across all collections */}
            <div
              className={`rag-dock__collection-row ${activeCollection === "" ? "rag-dock__collection-row--active" : ""}`}
              onClick={() => {
                setActiveCollection("");
                setActiveCollections(null);
                window.dispatchEvent(new CustomEvent("rag:collection-selected", { detail: { collection: "" } }));
                (app as unknown as { SetSessionCollections: (c: string[]) => Promise<void> })
                  .SetSessionCollections([])
                  .catch(() => {});
              }}
              style={{ cursor: "pointer", paddingLeft: "6px", gap: "4px" }}
            >
              <button
                className="rag-dock__collection-chevron"
                onClick={(e) => { e.stopPropagation(); setAllExpanded(!allExpanded); }}
                style={{ border: "none", background: "transparent", cursor: "pointer", color: "var(--fg-faint)", display: "flex", alignItems: "center", justifyContent: "center", width: "12px", padding: 0 }}
              >
                {allExpanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
              </button>
              <Folder size={13} className="rag-dock__collection-icon" style={{ color: "var(--accent)" }} />
              <span className="rag-dock__collection-label" style={{ fontWeight: 600 }}>{t("cowork.all")}</span>
              <span className="rag-dock__collection-count">
                {t("cowork.nDocs", { count: String(collections.reduce((s, c) => s + c.documents, 0)) })}
              </span>
            </div>

            {/* Build tree: root collections + their children */}
            {allExpanded && (() => {
              const qCat = catSearch.trim().toLowerCase();
              const filteredCols = !qCat ? collections : collections.filter((c) => {
                if (c.name.toLowerCase().includes(qCat) || (c.path && c.path.toLowerCase().includes(qCat))) return true;
                if (collections.some((child) => (child.name.toLowerCase().includes(qCat) || (child.path && child.path.toLowerCase().includes(qCat))) && (child.path.startsWith(c.path + "/") || child.parent === c.path))) {
                  return true;
                }
                return false;
              });

              if (filteredCols.length === 0 && qCat) {
                return (
                  <div style={{ padding: "24px 12px", textAlign: "center", color: "var(--fg-faint)", fontSize: "12px" }}>
                    {t("cowork.noMatchCategory", { query: catSearch })}
                  </div>
                );
              }

              // Build a true tree from paths
              interface TreeNode {
                name: string;
                path: string;
                documents: number;
                entities: number;
                children: Map<string, TreeNode>;
                isVirtual: boolean;
              }
              const tree = new Map<string, TreeNode>();
              
              filteredCols.forEach(c => {
                const parts = (c.path || c.name).split("/");
                let currPath = "";
                let currLevel = tree;
                parts.forEach((p, i) => {
                  currPath = currPath ? currPath + "/" + p : p;
                  if (!currLevel.has(p)) {
                    currLevel.set(p, {
                      name: p,
                      path: currPath,
                      documents: 0,
                      entities: 0,
                      children: new Map(),
                      isVirtual: true
                    });
                  }
                  const node = currLevel.get(p)!;
                  if (i === parts.length - 1) {
                    node.isVirtual = false;
                    node.documents = c.documents || 0;
                    node.entities = c.entities || 0;
                  }
                  currLevel = node.children;
                });
              });

              // Recursive render function
              const renderNode = (node: TreeNode, depth: number) => {
                const hasKids = node.children.size > 0;
                const expanded = expandedCats.has(node.path);
                const isActive = activeCollection === node.path;
                
                return (
                  <div key={node.path}>
                    <div
                      className={`rag-dock__collection-row ${isActive ? "rag-dock__collection-row--active" : ""}`}
                      onClick={() => {
                        if (hasKids && node.isVirtual) {
                           toggleCat(node.path);
                           return;
                        }
                        setActiveCollection(node.path);
                        window.dispatchEvent(new CustomEvent("rag:collection-selected", { detail: { collection: node.path } }));
                      }}
                      style={{ cursor: "pointer", paddingLeft: `${22 + depth * 14}px`, gap: "4px" }}
                      title={t("cowork.nDocsEntities", { docs: String(node.documents), entities: String(node.entities) })}
                    >
                      {hasKids ? (
                        <button
                          className="rag-dock__collection-chevron"
                          onClick={(e) => { e.stopPropagation(); toggleCat(node.path); }}
                          style={{ border: "none", background: "transparent", cursor: "pointer", color: "var(--fg-faint)", display: "flex", alignItems: "center", justifyContent: "center", width: "12px", padding: 0 }}
                        >
                          {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                        </button>
                      ) : (
                        depth > 0 ? (
                          <span style={{ width: "12px", display: "flex", justifyContent: "center", alignItems: "center" }}>
                            <CornerDownRight size={10} style={{ color: "var(--fg-faint)", opacity: 0.6, marginTop: "-2px" }} />
                          </span>
                        ) : (
                          <span style={{ width: "12px" }} />
                        )
                      )}
                      <Folder size={13} className="rag-dock__collection-icon" style={{ opacity: node.isVirtual ? 0.6 : 1, color: depth > 0 ? "var(--fg-dim)" : "var(--fg)" }} />
                      <span className="rag-dock__collection-label" style={{ fontWeight: depth === 0 ? 500 : 400, color: depth > 0 ? "var(--fg-dim)" : "var(--fg)" }}>{node.name}</span>
                      <span className="rag-dock__collection-count">{node.documents > 0 ? node.documents : ""}</span>
                      
                      {!node.isVirtual && (
                        <>
                          <button className="rag-dock__collection-action-btn" title={t("cowork.import")} onClick={(e) => { e.stopPropagation(); void handleImportToCollection(node.path); }}>
                            <FolderPlus size={12} />
                          </button>
                          <button className="rag-dock__collection-action-btn" title={t("cowork.extract")} onClick={(e) => { e.stopPropagation(); void handleQuickExtract(node.path); }}>
                            <Zap size={12} />
                          </button>
                          <button className="rag-dock__collection-delete" title={t("cowork.delete")} onClick={(e) => {
                            e.stopPropagation();
                            setDeleteTarget({ name: node.name, path: node.path });
                          }}>
                            <Trash2 size={12} />
                          </button>
                        </>
                      )}
                    </div>
                    {expanded && hasKids && Array.from(node.children.values()).map(child => renderNode(child, depth + 1))}
                  </div>
                );
              };

              return Array.from(tree.values()).map(root => renderNode(root, 0));
            })()}

            {collections.length === 0 && (
              <div className="cowork-dock__empty-state">
                <Folder size={22} />
                <p>{t("cowork.noCollections")}</p>
              </div>
            )}
          </div>
        )}

        {/* === 提取 tab === */}
        {tab === "extract" && (
          <TemplateSelect
            collection={activeCollection}
            collections={collections}
            onCollectionChange={(name) => {
              setActiveCollection(name);
              window.dispatchEvent(new CustomEvent("rag:collection-selected", { detail: { collection: name } }));
            }}
            onBack={() => setTab("files")}
          />
        )}

        {/* === 文件 tab === */}
        {tab === "files" && (
          <div className="rag-dock__files">
            {/* 简洁干练的分类切换级联菜单 (Select Dropdown) */}
            <div style={{ padding: "8px 12px", borderBottom: "1px solid var(--border-soft)" }}>
              <CustomSelect
                value={activeCollection}
                onChange={(val) => {
                  setActiveCollection(val);
                  window.dispatchEvent(new CustomEvent("rag:collection-selected", { detail: { collection: val } }));
                }}
                icon={<Folder size={14} style={{ color: "var(--accent)" }} />}
                options={[
                  {
                    value: "",
                    label: t("cowork.allDocs", { count: String(collections.reduce((acc, c) => acc + (c.documents || 0), 0)) }),
                    icon: <Folder size={13} style={{ color: "var(--accent)" }} />,
                  },
                  ...collections.map((c) => ({
                    value: c.path || c.name,
                    label: c.name,
                    subtitle: c.documents > 0 ? t("cowork.nArticles", { count: String(c.documents) }) : undefined,
                    indent: !!c.parent,
                    icon: <Folder size={13} />,
                  })),
                ]}
              />
            </div>

            {/* 实时文件检索过滤框 (Live File Search Bar) - 像素级对齐 .mem-search 规范 */}
            <div style={{ padding: "6px 8px 6px", borderBottom: "1px solid var(--border-soft)" }}>
              <label className="mem-search" style={{ height: 28, borderRadius: 6, background: "var(--bg-elev)" }}>
                <Search size={13} style={{ color: "var(--fg-dim)", flex: "0 0 auto" }} />
                <input
                  value={fileSearch}
                  onChange={(e) => setFileSearch(e.target.value)}
                  placeholder={t("cowork.searchFiles")}
                  style={{ fontSize: "11.5px" }}
                />
                {fileSearch && (
                  <button
                    type="button"
                    onClick={() => setFileSearch("")}
                    style={{ border: "none", background: "transparent", cursor: "pointer", color: "var(--fg-dim)", padding: 0, display: "inline-flex" }}
                  >
                    <X size={12} />
                  </button>
                )}
              </label>
            </div>

            {/* 当处于搜索过滤下，若未命中任何项则给出优雅空状态 */}
            {(() => {
              const displayTree = filterRagTree(tree, fileSearch);
              if (displayTree.length === 0 && tree.length > 0 && fileSearch.trim()) {
                return (
                  <div style={{ padding: "24px 12px", textAlign: "center", color: "var(--fg-faint)", fontSize: "12px" }}>
                    {t("cowork.noMatchFile", { query: fileSearch })}
                  </div>
                );
              }
              return null;
            })()}

            {/* 文件列表 */}
            {tree.length === 0 ? (
              activeCollection ? (
                <div className="cowork-dock__empty-state">
                  <FileText size={22} />
                  <p>{t("cowork.noFilesInCategory")}</p>
                  <button
                    className="btn btn--small btn--primary"
                    onClick={() => void handleImportToCollection(activeCollection)}
                  >
                    <FolderPlus size={14} />
                    <span>{t("cowork.importFiles")}</span>
                  </button>
                </div>
              ) : (
                <div className="cowork-dock__empty-state">
                  <FileText size={22} />
                  <p>{t("cowork.noFiles")}</p>
                </div>
              )
            ) : (
              <div className="rag-dock__file-tree">
                    {filterRagTree(tree, fileSearch).map((node) => (
                      <RagNode
                        key={node.key}
                        node={node}
                        depth={0}
                        onStartExtract={(n) => {
                          if (n.path) {
                            (app as unknown as { RagStartExtract: (c: string, t: string, m: string) => Promise<void> })
                              .RagStartExtract(activeCollection, n.path, "incremental")
                              .then(() => refreshTree())
                              .catch(() => refreshTree());
                          }
                        }}
                        onCancel={(n) => {
                          if (n.jobId) {
                            (app as unknown as { RagCancelExtract: (j: string) => Promise<void> })
                              .RagCancelExtract(n.jobId)
                              .then(() => refreshTree())
                              .catch(() => refreshTree());
                          }
                        }}
                        onRemove={(n) => {
                          if (!n.path) return;
                          void confirm({ title: t("cowork.deleteExtracted"), message: t("cowork.deleteExtractedMsg", { path: n.path }) }).then((ok) => {
                            if (!ok) return;
                            (app as unknown as { RagRemovePath: (c: string, p: string) => Promise<void> })
                              .RagRemovePath(activeCollection, n.path)
                              .then(() => refreshTree())
                              .catch(() => refreshTree());
                          });
                        }}
                        onFileClick={(n) => {
                          setDocCollection(n.collection);
                          setDocPath(n.path);
                          setEntityName(null);
                          onFileClick?.(n.path);
                        }}
                        selectedPath={docPath ?? undefined}
                      />
                    ))}
                  </div>
                )}
          </div>
        )}
      </div>

      {/* 新建分类弹窗 */}
      {showCreateModal && (
        <div className="rag-create-overlay" onClick={() => setShowCreateModal(false)}>
          <div className="rag-create-modal" onClick={(e) => e.stopPropagation()}>
            <div className="rag-create-modal__head">
              <h3 className="rag-create-modal__title">{t("cowork.newCategoryTitle")}</h3>
              <button className="rag-create-modal__close" onClick={() => setShowCreateModal(false)}>✕</button>
            </div>
            <div className="rag-create-modal__body">
              <div className="rag-create-modal__section">
                <label className="rag-create-modal__label">{t("cowork.selectTemplate")}</label>
                <div className="rag-create-modal__templates">
                  {["work", "study", "personal", "project"].map((tpl) => (
                    <button
                      key={tpl}
                      className={`rag-create-modal__template ${newCollectionParent === tpl ? "rag-create-modal__template--selected" : ""}`}
                      onClick={() => setNewCollectionParent(tpl)}
                    >
                      <Folder size={13} /> {tpl}
                    </button>
                  ))}
                  <button
                    className={`rag-create-modal__template ${newCollectionParent === "" ? "rag-create-modal__template--selected" : ""}`}
                    onClick={() => setNewCollectionParent("")}
                  >
                    {t("cowork.custom")}
                  </button>
                </div>
              </div>
              {newCollectionParent && (
                <div className="rag-create-modal__section">
                  <label className="rag-create-modal__label">{t("cowork.parentCategory")}</label>
                  <div className="rag-create-modal__parent">{newCollectionParent}/</div>
                </div>
              )}
              <div className="rag-create-modal__section">
                <label className="rag-create-modal__label">{t("cowork.categoryName")}</label>
                <input
                  className="rag-create-modal__input"
                  placeholder={newCollectionParent ? "e.g. Leadership" : "e.g. Work or Work/Leadership"}
                  value={newCollectionName}
                  onChange={(e) => setNewCollectionName(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      const name = newCollectionName.trim();
                      if (name) {
                        const full = newCollectionParent ? `${newCollectionParent}/${name}` : name;
                        (app as unknown as { RagCreateCollection: (n: string) => Promise<void> })
                          .RagCreateCollection(full)
                          .then(() => {
                            setShowCreateModal(false);
                            setNewCollectionName("");
                            setNewCollectionParent("");
                            (app as unknown as { ListRagCollections: () => Promise<RagCollectionView[]> })
                              .ListRagCollections()
                              .then(setCollections)
                              .catch(() => {});
                          })
                          .catch(() => {});
                      }
                    }
                  }}
                  autoFocus
                />
              </div>
              <div className="rag-create-modal__preview">
                {newCollectionParent && newCollectionName
                  ? `${newCollectionParent}/${newCollectionName}`
                  : newCollectionName}
              </div>
            </div>
            <div className="rag-create-modal__foot">
              <button className="btn btn--small" onClick={() => setShowCreateModal(false)}>{t("entityEdit.cancel")}</button>
              <button
                className="btn btn--primary btn--small"
                disabled={!newCollectionName.trim()}
                onClick={() => {
                  const name = newCollectionName.trim();
                  if (!name) return;
                  const full = newCollectionParent ? `${newCollectionParent}/${name}` : name;
                  (app as unknown as { RagCreateCollection: (n: string) => Promise<void> })
                    .RagCreateCollection(full)
                    .then(() => {
                      setShowCreateModal(false);
                      setNewCollectionName("");
                      setNewCollectionParent("");
                      (app as unknown as { ListRagCollections: () => Promise<RagCollectionView[]> })
                        .ListRagCollections()
                        .then(setCollections)
                        .catch(() => {});
                    })
                    .catch(() => {});
                }}
              >
                {t("cowork.create")}
              </button>
            </div>
          </div>
        </div>
      )}

      <ImportModal
        isOpen={showImportModal}
        onClose={() => setShowImportModal(false)}
        collections={collections}
        defaultCollection={importTargetCol}
        onSuccess={() => {
          refreshCollections();
        }}
      />
      {deleteTarget && (
        <ConfirmModal
          title={t("cowork.deleteCategory", { name: deleteTarget.name })}
          message={t("cowork.deleteCategoryMsg")}
          onConfirm={() => {
            const path = deleteTarget.path;
            (app as unknown as { RagDeleteCollection: (n: string) => Promise<void> })
              .RagDeleteCollection(path)
              .then(() => refreshCollections())
              .catch(() => {});
          }}
          onClose={() => setDeleteTarget(null)}
        />
      )}
    </aside>
  );
}

// onRagProgress is imported above for API parity with bridge.ts. The dock
// subscribes via window.runtime.EventsOn directly (matching the bundle), but
// we keep the symbol referenced so tree-shaking does not drop the import and
// future migrations to the bridge helper are a one-line swap.
void onRagProgress;
