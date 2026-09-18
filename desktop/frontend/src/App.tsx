import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties, KeyboardEvent, PointerEvent as ReactPointerEvent } from "react";
import { ShellExpandProvider, useShellExpand } from "./lib/shellExpand";
import {
  Activity,
  Globe,
  SquarePen,
  FileText,
  GitBranch,
  History,
  MessageSquare,
  Settings as SettingsIcon,
  Pencil,
  PanelLeft,
  Plus,
  Repeat2,
  X,
  Trash2,
  Brain,
  Cpu,
  Palette,
  SlidersHorizontal,
  SquareKanban,
  Network,
  BookOpen,
} from "lucide-react";
import { useToast } from "./lib/toast";
import { useConfirm } from "./lib/confirm";
import { asArray } from "./lib/array";
import { clearLegacyLangPref, normalizeLangPref, readLegacyLangPref, useI18n, useT, type Translator } from "./lib/i18n";
import { useController, type Item, type LiveStream } from "./lib/useController";
import { app, onEvent, onProjectTreeChanged, onSchedulerNotice,
  onRemoteStatus, onBrowserMirror,
} from "./lib/bridge";
import { browserMirrorSnapshot, pushBrowserMirrorFrame, requestBrowserMirrorFocus } from "./lib/browserMirror";
import { loadWallpaper, subscribeWallpaper } from "./lib/wallpaper";
import { onHiqDeepLink, onProfileChanged } from "./lib/bridge";
import { CoWorkLayout } from "./layouts/CoWorkLayout";
import { NetDevLayout, NetdevTitleBar } from "./layouts/NetDevLayout";
import { PreferencePanel } from "./components/cowork/PreferencePanel";
import { Transcript } from "./components/Transcript";
import { ExpertSessionView } from "./components/cowork/ExpertSessionView";
import { Composer } from "./components/Composer";
import { TodoPanel } from "./components/TodoPanel";
import { ApprovalModal } from "./components/ApprovalModal";
import { AskCard } from "./components/AskCard";
import { ClearContextCard } from "./components/ClearContextCard";
import { SidebarFooter } from "./components/SidebarFooter";
import { TerminalPanel, loadTerminalOpen, saveTerminalOpen } from "./components/TerminalPanel";
import logoSymbol from "./assets/logo-symbol.png";
import { ContextMenu, type ContextMenuPoint } from "./components/ContextMenu";
import { LoopPanel } from "./components/loop/LoopPanel";
import { TeamBoard } from "./components/cowork/TeamBoard";
import { KnowledgeHub } from "./components/cowork/KnowledgeHub";
import { RagPanel } from "./components/cowork/RagPanel";
import { ProfileSegmented } from "./components/AppChrome";
import { SideSessionPane } from "./components/SideSessionPane";
import { HistoryPanel } from "./components/HistoryPanel";
import { SidebarSessions } from "./components/SidebarSessions";
import { CommandPalette, type PaletteItem } from "./components/CommandPalette";
import { AppBackdrop } from "./components/AppBackdrop";
import { AgentDashboard } from "./components/AgentDashboard";
import { BranchTree } from "./components/BranchTree";
import { SettingsPanel, SETTINGS_TABS } from "./components/SettingsPanel";
import { ShortcutsCheatsheet } from "./components/ShortcutsCheatsheet";
import { useGlobalShortcut } from "./lib/keyboardShortcuts";
import { UpdateBanner } from "./components/UpdateBanner";
import { ContextPanel } from "./components/ContextPanel";
import { WorkspacePanel } from "./components/WorkspacePanel";
import { PreviewPane } from "./components/PreviewPane";
import { Tooltip } from "./components/Tooltip";
import { StartupSplash, shouldShowStartupSplash } from "./components/StartupSplash";
import { OnboardingOverlay } from "./components/OnboardingOverlay";
import { RemoteConnectWizard } from "./components/RemoteConnectWizard";
import { AttachmentViewer } from "./components/AttachmentViewer";
import { AppChrome } from "./components/AppChrome";
import { ProjectTree } from "./components/ProjectTree";
import { PaneErrorBoundary } from "./components/ErrorBoundary";
import { parseTodos } from "./lib/tools";
import { shouldShowTodoPanel } from "./lib/todoVisibility";
import {
  modeFromAxes,
  normalizeMode,
  normalizeToolApprovalMode,
  type BotConnectionView,
  type BudgetStatusView,
  type BotSettingsView,
  type CollaborationMode,
  type ComposerInsertRequest,
  type Mode,
  type SessionMeta,
  type SettingsTab,
  type SettingsView,
  type TabMeta,
  type ToolApprovalMode,
  type CapabilitiesView,
} from "./lib/types";
import { Box, Server } from "lucide-react";
import {
  controllerCollaborationMode,
  displayedCollaborationMode,
  keepGoalDraftMode,
  metaSyncedCollaborationMode,
  tabListCollaborationMode,
} from "./lib/goalDraftMode";
import {
  restorableToolApprovalMode,
  toggleYoloToolApprovalMode,
  type RestorableToolApprovalMode,
} from "./lib/toolApprovalMode";
import { loadLayoutSize, saveLayoutSize } from "./lib/layoutPreferences";
import { hydrateDisplayMode } from "./lib/displayMode";
import { getScopedItem, setActiveStorageProfile, setScopedItem } from "./lib/profileScopedStorage";
import { blobToBase64, renderSessionHtml, renderSessionImageBlob, renderSessionPdfBlob } from "./lib/sessionExport";
import { sessionActivityTime } from "./lib/session";
import {
  applyTheme,
  clearLegacyThemePreference,
  getTheme,
  getThemeStyle,
  isThemeStyle,
  normalizeThemePreference,
  normalizeThemeStyleForTheme,
  readLegacyThemePreference,
  type Theme,
} from "./lib/theme";
import { applyTextSize, DEFAULT_TEXT_SIZE, getTextSize, nextTextSize } from "./lib/textSize";
import { useWindowStatePersistence } from "./lib/windowState";
import { availableWorkspacePanelWidth, resolveWorkspacePanelWidth, workspacePanelAriaMinWidth } from "./lib/workspaceLayout";

const SIDEBAR_COLLAPSED_KEY = "hiq.sidebar.collapsed";
// netdev 模式的建议 chips（completion-spec §3.3）：覆盖高频 + 低发现度能力
// （discover/netconf/locate/assess/netconf 厂商命令习惯）的用户语言问法。

const SIDEBAR_DEFAULT_WIDTH = 220;
const SIDEBAR_MIN_WIDTH = 200;
const SIDEBAR_MAX_WIDTH = 480;
const SIDEBAR_VIEWPORT_RATIO = 0.14;
const CHAT_MIN_WIDTH = 400;
const CHAT_COMFORT_MIN_WIDTH = 560;
const WORKSPACE_RESIZER_WIDTH = 8;

function isThemeMode(value: string): value is Theme {
  return value === "auto" || value === "light" || value === "dark";
}
const RIGHT_DOCK_TREE_DEFAULT_WIDTH = 260;
const RIGHT_DOCK_TREE_MIN_WIDTH = 260;
// 运维 dock 的下限：设备卡/发现卡的内容（命令按钮、证据链）与审计表
// 五列在更窄的栏里会折行成灾——编码 dock 的 260 下限对它远远不够；
// 380 起步让终端尾随/命令 chips 不再挤（2026-08-27 舒展化调整）。
const NETDEV_DOCK_MIN_WIDTH = 280;
const RIGHT_DOCK_TREE_MAX_WIDTH = 1200;
const RIGHT_DOCK_PREVIEW_DEFAULT_WIDTH = 660;
const RIGHT_DOCK_PREVIEW_MIN_WIDTH = 420;
const RIGHT_DOCK_MIN_RENDER_WIDTH = 240;
// Drag-to-edge hide thresholds: while dragging a resizer, releasing past these
// collapses the sidebar / closes the dock instead of pinning at min width
// (VS Code-style edge-hide). Sidebar uses viewport-x; dock uses raw dragged width.
const SIDEBAR_COLLAPSE_THRESHOLD = 96;
const DOCK_CLOSE_THRESHOLD = 180;
const RIGHT_DOCK_MAX_WIDTH = 3840;

type RightDockMode = "turns" | "files" | "changed" | "preview" | "session";

const RIGHT_DOCK_MODE_KEY = "hiq.rightDockMode";
const PREVIEW_URL_KEY = "hiq.previewUrl";
const DOCK_TABS_KEY = "hiq.dockTabs";

// All tabs the dock's "+" menu can open, in canonical order. Persisted dock
// tab lists are filtered against this, so entries saved before the 2026-08-27
// rename (the old "context" overview tab) are dropped on load.
const DOCK_TAB_CATALOG: RightDockMode[] = ["turns", "files", "changed", "preview", "session"];

const DEFAULT_DOCK_TABS: RightDockMode[] = ["files", "changed", "preview"];

function loadDockTabs(): RightDockMode[] {
  try {
    const raw = getScopedItem(DOCK_TABS_KEY, true);
    if (raw) {
      const list = (JSON.parse(raw) as string[]).filter(
        (v): v is RightDockMode => DOCK_TAB_CATALOG.includes(v as RightDockMode),
      );
      if (list.length > 0) return list;
    }
  } catch { /* storage unavailable */ }
  return [...DEFAULT_DOCK_TABS];
}

function loadRightDockMode(): RightDockMode {
  try {
    const v = getScopedItem(RIGHT_DOCK_MODE_KEY, true);
    if (v === "turns" || v === "files" || v === "changed" || v === "preview" || v === "session") return v;
  } catch { /* storage unavailable */ }
  return "files";
}

function loadPreviewUrl(): string {
  try {
    return getScopedItem(PREVIEW_URL_KEY, true) || "";
  } catch {
    return "";
  }
}
type HistoryScopeFilter = { scope: "global" | "project"; workspaceRoot: string };
type DesktopPlatform = "darwin" | "windows" | "linux";
type HistoryViewState =
  | { kind: "history"; source: "scope"; filter: HistoryScopeFilter; sessions: SessionMeta[] }
  | { kind: "history"; source: "all"; sessions: SessionMeta[] }
  | { kind: "trash"; sessions: SessionMeta[] };
type SidebarImPlatform = "feishu" | "lark" | "weixin" | "telegram";
type SidebarImStatus = "connected" | "disabled" | "pending" | "error" | "disconnected";
type SidebarImConnection = {
  id: string;
  platform: SidebarImPlatform;
  title: string;
  platformLabel: string;
  subtitle: string;
  status: SidebarImStatus;
  statusLabel: string;
  remoteId: string;
  chatType: string;
  chatId: string;
  sessionId: string;
  scope: "global" | "project";
  workspaceRoot: string;
};
type SidebarImTopicSource = {
  platform: SidebarImPlatform;
  label: string;
  title: string;
  remoteId: string;
  connectionId: string;
};
type SidebarImConnectionDetailProps = {
  connection: SidebarImConnection;
  sessions: SessionMeta[];
  allConnections: SidebarImConnection[];
  onClose: () => void;
  onOpenSession: () => void;
  onOpenSessionPath: (path: string) => void;
  onOpenSettings: () => void;
  onSelectConnection: (id: string) => void;
};

function isSidebarImConnection(connection: BotConnectionView): boolean {
  return connection.provider === "feishu" || connection.provider === "weixin" || connection.provider === "telegram";
}

function sidebarImPlatform(connection: BotConnectionView): SidebarImPlatform {
  if (connection.provider === "telegram") return "telegram";
  if (connection.provider === "weixin") return "weixin";
  return connection.domain === "lark" ? "lark" : "feishu";
}

function sidebarImPlatformLabel(platform: SidebarImPlatform, translate: Translator): string {
  if (platform === "telegram") return translate("settings.botTelegram");
  if (platform === "lark") return "Lark";
  if (platform === "weixin") return translate("settings.botWeixin");
  if (platform === "feishu") return translate("settings.botFeishu");
  // Unknown/future platform: verbatim, never relabeled as Feishu.
  return platform || translate("settings.botChannelUnknown");
}

function botMappingScope(mapping: BotConnectionView["sessionMappings"][number] | null | undefined, connectionWorkspaceRoot: string): "global" | "project" {
  if (mapping?.scope === "project") return "project";
  if ((mapping?.workspaceRoot ?? "").trim()) return "project";
  return connectionWorkspaceRoot.trim() ? "project" : "global";
}

function botMappingWorkspaceRoot(
  mapping: BotConnectionView["sessionMappings"][number] | null | undefined,
  connectionWorkspaceRoot: string,
): string {
  const workspaceRoot = (mapping?.workspaceRoot ?? "").trim() || connectionWorkspaceRoot.trim();
  return botMappingScope(mapping, connectionWorkspaceRoot) === "project" ? workspaceRoot : "";
}

function compactRemoteId(value: string): string {
  const trimmed = value.trim();
  if (trimmed.length <= 28) return trimmed;
  return `${trimmed.slice(0, 12)}…${trimmed.slice(-8)}`;
}

function sidebarImStatus(connection: BotConnectionView, botEnabled: boolean): SidebarImStatus {
  if (!botEnabled || !connection.enabled) return "disabled";
  if (connection.status === "connected") return "connected";
  if (connection.status === "pending") return "pending";
  if (connection.status === "error") return "error";
  return "disconnected";
}

function sidebarImStatusLabel(status: SidebarImStatus, translate: Translator): string {
  switch (status) {
    case "connected":
      return translate("sidebar.imConnected");
    case "disabled":
      return translate("sidebar.imDisabled");
    case "pending":
      return translate("sidebar.imPending");
    case "error":
      return translate("sidebar.imError");
    default:
      return translate("sidebar.imDisconnected");
  }
}

function sidebarImConnectionsFromBot(bot: BotSettingsView | null | undefined, translate: Translator): SidebarImConnection[] {
  if (!bot?.connections?.length) return [];
  // One sidebar entry per (bot connection × conversation). chatType+chatId are
  // part of the key so the same person's DM and their conversations in different
  // groups each get their own row — without them, a user in 3 groups would
  // collapse into one entry and those sessions would merge. Dedupe by the full
  // scope key. A bot with no conversations yet still gets a placeholder entry.
  const out: SidebarImConnection[] = [];
  for (const connection of bot.connections) {
    if (!isSidebarImConnection(connection)) continue;
    const platform = sidebarImPlatform(connection);
    const platformLabel = sidebarImPlatformLabel(platform, translate);
    const status = sidebarImStatus(connection, bot.enabled);
    const statusLabel = sidebarImStatusLabel(status, translate);
    const title = connection.label.trim() || platformLabel;
    const mappings = asArray(connection.sessionMappings).filter((m) => m.remoteId.trim() || m.sessionId.trim());
    const seenScope = new Set<string>();
    const pushEntry = (remoteId: string, chatType: string, chatId: string, sessionId: string, mapping: BotConnectionView["sessionMappings"][number] | null) => {
      out.push({
        id: connection.id + ":" + (scopeKey(remoteId, chatType, chatId) || "__"),
        platform,
        title,
        platformLabel,
        subtitle: conversationSubtitle(remoteId, chatType, chatId, platformLabel, connection.model, statusLabel, translate),
        status,
        statusLabel,
        remoteId,
        chatType,
        chatId,
        sessionId,
        scope: botMappingScope(mapping, connection.workspaceRoot),
        workspaceRoot: botMappingWorkspaceRoot(mapping, connection.workspaceRoot),
      });
    };
    if (mappings.length === 0) {
      pushEntry("", "", "", "", null);
      continue;
    }
    for (const mapping of mappings) {
      const remoteId = mapping.remoteId.trim();
      const chatType = (mapping.chatType || "").trim();
      const chatId = (mapping.chatId || "").trim();
      const key = scopeKey(remoteId, chatType, chatId);
      if (key && seenScope.has(key)) continue;
      if (key) seenScope.add(key);
      pushEntry(remoteId, chatType, chatId, mapping.sessionId.trim(), mapping);
    }
  }
  return out;
}

// scopeKey is the stable identity of one IM conversation: chatType+chatId+
// remoteId. It mirrors the backend's per-conversation dedup so a person's DM and
// their group conversations are distinct. Empty for placeholder entries.
function scopeKey(remoteId: string, chatType: string, chatId: string): string {
  return `${chatType}|${chatId}|${remoteId}`;
}

// conversationSubtitle builds the secondary line for a contact row: a translated
// chat-type label (DM / Group / …), the contact id, then model + status.
function conversationSubtitle(
  remoteId: string,
  chatType: string,
  chatId: string,
  platformLabel: string,
  model: string,
  statusLabel: string,
  translate: Translator,
): string {
  const typeLabel = chatTypeLabel(chatType, translate);
  const who = remoteId ? compactRemoteId(remoteId) : platformLabel;
  const where = chatId ? compactRemoteId(chatId) : "";
  return [typeLabel, where ? `${who} @ ${where}` : who, (model || "").trim(), statusLabel].filter(Boolean).join(" · ");
}

// chatTypeLabel maps a backend ChatType to a short localized label for display.
function chatTypeLabel(chatType: string, translate: Translator): string {
  switch ((chatType || "").trim()) {
    case "group":
    case "guild":
      return translate("botDetail.chatTypeGroup");
    case "thread":
      return translate("botDetail.chatTypeThread");
    case "direct":
      return translate("botDetail.chatTypeDirect");
    case "dm":
      return translate("botDetail.chatTypeDM");
    default:
      return chatType.trim() ? chatType.trim() : translate("botDetail.chatTypeDM");
  }
}

function mappedSessionTarget(sessionId: string): { kind: "path" | "topic"; value: string } | null {
  const trimmed = sessionId.trim();
  if (!trimmed) return null;
  const lower = trimmed.toLowerCase();
  if (lower.startsWith("path:")) {
    const value = trimmed.slice(5).trim();
    return value ? { kind: "path", value } : null;
  }
  if (lower.startsWith("topic:")) {
    const value = trimmed.slice(6).trim();
    return value ? { kind: "topic", value } : null;
  }
  if (trimmed.endsWith(".jsonl") || trimmed.includes("/") || trimmed.includes("\\") || trimmed.startsWith("~")) {
    return { kind: "path", value: trimmed };
  }
  return { kind: "topic", value: trimmed };
}

function sidebarImTopicSourcesFromBot(bot: BotSettingsView | null | undefined, translate: Translator): Record<string, SidebarImTopicSource> {
  if (!bot?.connections?.length) return {};
  const sources: Record<string, SidebarImTopicSource> = {};
  for (const connection of bot.connections) {
    if (!isSidebarImConnection(connection)) continue;
    const platform = sidebarImPlatform(connection);
    const label = sidebarImPlatformLabel(platform, translate);
    const title = connection.label.trim() || label;
    for (const mapping of asArray(connection.sessionMappings)) {
      const scope = botMappingScope(mapping, connection.workspaceRoot);
      if (scope !== "global") continue;
      const target = mappedSessionTarget(mapping.sessionId);
      if (!target || target.kind !== "topic") continue;
      if (sources[target.value]) continue;
      sources[target.value] = {
        platform,
        label,
        title,
        remoteId: mapping.remoteId.trim(),
        connectionId: connection.id,
      };
    }
  }
  return sources;
}

function sidebarImScopeLabel(connection: SidebarImConnection, translate: Translator): string {
  if (connection.scope === "project") return translate("botDetail.scopeProject", { name: connection.workspaceRoot || "Project" });
  return translate("botDetail.scopeGlobal");
}

function sidebarImSessionLabel(connection: SidebarImConnection, translate: Translator): string {
  const target = mappedSessionTarget(connection.sessionId);
  if (!target) return translate("botDetail.noSession");
  if (target.kind === "path") return target.value.split(/[\\/]/).pop() || target.value;
  return target.value;
}

function SidebarImConnectionDetail({ connection, sessions, allConnections, onClose, onOpenSession, onOpenSessionPath, onOpenSettings, onSelectConnection }: SidebarImConnectionDetailProps) {
  const translate = useT();
  const target = mappedSessionTarget(connection.sessionId);
  return (
    <div className="bot-detail">
      <section className="bot-detail__summary">
        <div className="bot-detail__summary-main">
          <span>{translate("botDetail.subtitle")}</span>
          <h2>{connection.title}</h2>
          <div className="bot-detail__chips">
            <span>{connection.platformLabel}</span>
            <span>{connection.statusLabel}</span>
            <span>{sidebarImScopeLabel(connection, translate)}</span>
          </div>
        </div>
        <div className="bot-detail__summary-actions">
          <button type="button" className="btn btn--primary btn--small bot-detail__primary" disabled={!target} title={target ? undefined : translate("botDetail.openDisabled")} onClick={onOpenSession}>
            <MessageSquare size={14} />
            {translate("botDetail.openSession")}
          </button>
          <button type="button" className="btn btn--secondary btn--small" onClick={onOpenSettings}>
            <SettingsIcon size={14} />
            {translate("botDetail.manage")}
          </button>
          <button type="button" className="btn btn--secondary btn--small" onClick={onClose}>
            {translate("botDetail.close")}
          </button>
        </div>
      </section>

      {allConnections.length > 1 && (
        <section className="bot-detail__panel" aria-label={translate("botDetail.contacts")}>
          <div className="bot-detail__section-head">
            <span>{translate("botDetail.contacts")}</span>
            <strong>{allConnections.length}</strong>
          </div>
          <div className="bot-detail__contacts">
            {allConnections.map((c) => (
              <button
                key={c.id}
                type="button"
                className={"bot-detail__contact-row" + (c.id === connection.id ? " is-active" : "")}
                onClick={() => onSelectConnection(c.id)}
                title={c.subtitle}
              >
                <span className="bot-detail__contact-platform">{c.platformLabel}</span>
                <span className="bot-detail__contact-title">{c.title}</span>
                <span className="bot-detail__contact-type">{chatTypeLabel(c.chatType, translate)}</span>
                <span className="bot-detail__contact-remote">{c.remoteId ? compactRemoteId(c.remoteId) : translate("botDetail.noContact")}</span>
              </button>
            ))}
          </div>
        </section>
      )}

      <section className="bot-detail__panel bot-detail__panel--facts" aria-label={translate("botDetail.summary")}>
        <div className="bot-detail__section-head">
          <span>{translate("botDetail.summary")}</span>
        </div>
        <div className="bot-detail__facts">
          <div>
            <span>{translate("botDetail.remoteId")}</span>
            <code>{connection.remoteId || "—"}</code>
          </div>
          <div>
            <span>{translate("botDetail.localTopic")}</span>
            <strong>{sidebarImSessionLabel(connection, translate)}</strong>
          </div>
          <div>
            <span>{translate("botDetail.scope")}</span>
            <strong>{sidebarImScopeLabel(connection, translate)}</strong>
          </div>
        </div>
      </section>

      <section className="bot-detail__panel" aria-label={translate("botDetail.sessions")}>
        <div className="bot-detail__section-head">
          <span>{translate("botDetail.sessions")}</span>
        </div>
        {sessions.length === 0 ? (
          <div className="bot-detail__empty">{translate("botDetail.noSessions")}</div>
        ) : (
          <div className="bot-detail__sessions">
            {sessions.map((s) => (
              <div key={s.path} className="bot-detail__session-row">
                <div className="bot-detail__session-main">
                  <div className="bot-detail__session-time">
                    {new Date(s.lastActivityAt).toLocaleString()}
                    <span className="bot-detail__session-turns"> · {s.turns} {translate("botDetail.turns")}</span>
                  </div>
                  <div className="bot-detail__session-preview">{(s.title || s.preview || "").trim() || translate("botDetail.noPreview")}</div>
                </div>
                <button type="button" className="btn btn--secondary btn--small" onClick={() => onOpenSessionPath(s.path)}>
                  {translate("botDetail.continue")}
                </button>
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}

// Joint width constraint (ui-redesign §4-B7): the sidebar may never push the
// chat pane below CHAT_MIN_WIDTH, dock or no dock — the two panels trade width
// against each other with the conversation always protected. The right-dock
// side of the constraint lives in lib/workspaceLayout.availableWorkspacePanelWidth.
function clampSidebarWidth(width: number, dockWidth = 0, chatFloor = CHAT_MIN_WIDTH): number {
  const viewportMax = Math.max(SIDEBAR_MIN_WIDTH, window.innerWidth - dockWidth - chatFloor);
  return Math.min(SIDEBAR_MAX_WIDTH, viewportMax, Math.max(SIDEBAR_MIN_WIDTH, Math.round(width)));
}

function clampRightDockPreviewWidth(width: number): number {
  return Math.min(RIGHT_DOCK_MAX_WIDTH, Math.max(RIGHT_DOCK_PREVIEW_MIN_WIDTH, Math.round(width)));
}

function clampRightDockTreeWidth(width: number): number {
  return Math.min(RIGHT_DOCK_TREE_MAX_WIDTH, Math.max(RIGHT_DOCK_TREE_MIN_WIDTH, Math.round(width)));
}

function defaultSidebarWidth(): number {
  if (typeof window !== "undefined") {
    return clampSidebarWidth(window.innerWidth * SIDEBAR_VIEWPORT_RATIO);
  }
  return SIDEBAR_DEFAULT_WIDTH;
}

function defaultRightDockTreeWidth(): number {
  return RIGHT_DOCK_TREE_DEFAULT_WIDTH;
}

function loadSidebarCollapsed(): boolean {
  if (typeof window === "undefined") return false;
  try {
    return window.localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === "1";
  } catch {
    return false;
  }
}

function saveSidebarCollapsed(collapsed: boolean): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(SIDEBAR_COLLAPSED_KEY, collapsed ? "1" : "0");
  } catch {
    /* ignore storage failures */
  }
}

function loadSidebarWidth(): number {
  return loadLayoutSize("sidebarWidthGraphite", defaultSidebarWidth(), clampSidebarWidth);
}

function saveSidebarWidth(width: number): void {
  saveLayoutSize("sidebarWidthGraphite", width, clampSidebarWidth);
}

function normalizeDesktopPlatform(value: string): DesktopPlatform {
  if (value === "darwin" || value === "windows") return value;
  return "linux";
}

function browserPlatformOverride(): DesktopPlatform | null {
  if (typeof window === "undefined" || window.runtime) return null;
  const value = new URLSearchParams(window.location.search).get("platform");
  if (value === "darwin" || value === "windows" || value === "linux") return value;
  return null;
}

function detectBrowserPlatform(): DesktopPlatform {
  const override = browserPlatformOverride();
  if (override) return override;
  if (typeof navigator === "undefined") return "linux";
  const marker = `${navigator.platform} ${navigator.userAgent}`;
  if (/Win/i.test(marker)) return "windows";
  if (/Mac/i.test(marker)) return "darwin";
  return "linux";
}

function loadRightDockTreeWidth(): number {
  return loadLayoutSize("rightDockTreeWidth", defaultRightDockTreeWidth(), clampRightDockTreeWidth);
}

function saveRightDockTreeWidth(width: number): void {
  saveLayoutSize("rightDockTreeWidth", width, clampRightDockTreeWidth);
}

function defaultRightDockPreviewWidth(): number {
  if (typeof window !== "undefined") {
    // 根据屏幕宽度动态分配：让它占据视口的近 50%，但保证有一个保底体验，且上限为 1200
    const half = window.innerWidth * 0.5;
    return Math.max(RIGHT_DOCK_PREVIEW_DEFAULT_WIDTH, Math.min(1200, half));
  }
  return RIGHT_DOCK_PREVIEW_DEFAULT_WIDTH;
}

function loadRightDockPreviewWidth(): number {
  return loadLayoutSize("rightDockPreviewWidth", defaultRightDockPreviewWidth(), clampRightDockPreviewWidth);
}

function saveRightDockPreviewWidth(width: number): void {
  saveLayoutSize("rightDockPreviewWidth", width, clampRightDockPreviewWidth);
}

function tabWorkspaceTitle(tab?: TabMeta): string {
  if (!tab) return "Project";
  return tab.workspaceName || tab.workspaceRoot || "Project";
}

function topicTitle(tab?: TabMeta): string {
  if (!tab) return "Untitled";
  const workspaceTitle = tabWorkspaceTitle(tab);
  const topic = tab.topicTitle || "Untitled";
  return topic === workspaceTitle ? workspaceTitle : `${workspaceTitle} / ${topic}`;
}

function topicDisplayTitle(tab?: TabMeta): string {
  if (!tab) return "Untitled";
  return tab.topicTitle || "Untitled";
}

function sessionsForScope(sessions: SessionMeta[], filter: HistoryScopeFilter): SessionMeta[] {
  if (filter.scope === "project") {
    return sessions.filter((session) => session.scope === "project" && session.workspaceRoot === filter.workspaceRoot);
  }
  return sessions.filter((session) => (session.scope || "global") === "global");
}

function workspaceDisplayName(path?: string): string {
  if (!path) return "";
  const parts = path.split(/[/\\]/).filter(Boolean);
  return parts.length > 0 ? parts[parts.length - 1] : path;
}

function materializeLiveItems(items: Item[], live?: LiveStream): Item[] {
  if (!live) return items;
  return items.map((item) => {
    if (item.kind !== "assistant" || item.id !== live.id) return item;
    return { ...item, text: live.text, reasoning: live.reasoning, streaming: true };
  });
}

function fence(label: string, value: string): string {
  if (!value.trim()) return "";
  const fenceToken = value.includes("```") ? "````" : "```";
  return `${label}\n${fenceToken}\n${value.trim()}\n${fenceToken}`;
}

function sessionItemsToMarkdown(title: string, items: Item[], live?: LiveStream): string {
  const lines: string[] = [`# ${title.trim() || "hiq session"}`, ""];
  for (const item of materializeLiveItems(items, live)) {
    switch (item.kind) {
      case "user":
        lines.push("## User", "", item.text.trim(), "");
        break;
      case "assistant":
        lines.push("## Assistant");
        if (item.reasoning.trim()) {
          lines.push("", "### Reasoning", "", item.reasoning.trim());
        }
        if (item.text.trim()) {
          lines.push("", item.text.trim());
        }
        lines.push("");
        break;
      case "tool":
        lines.push(`### Tool: ${item.name}`);
        if (item.args.trim()) lines.push("", fence("Args", item.args));
        if (item.output?.trim()) lines.push("", fence("Output", item.output));
        if (item.error?.trim()) lines.push("", fence("Error", item.error));
        lines.push("");
        break;
      case "phase":
        lines.push(`### Phase`, "", item.text.trim(), "");
        break;
      case "notice":
        lines.push(`### ${item.level === "warn" ? "Warning" : "Notice"}`, "", item.text.trim(), "");
        break;
      case "compaction":
        lines.push("### Context Compaction", "");
        if (item.pending) {
          lines.push("Compaction pending.");
        } else {
          lines.push(`Messages: ${item.messages}`);
          if (item.trigger) lines.push(`Trigger: ${item.trigger}`);
          if (item.summary.trim()) lines.push("", item.summary.trim());
        }
        lines.push("");
        break;
    }
  }
  return lines.join("\n").replace(/\n{3,}/g, "\n\n").trimEnd() + "\n";
}

function sessionItemsToJson(title: string, items: Item[], live?: LiveStream): string {
  return JSON.stringify(
    {
      title,
      exportedAt: new Date().toISOString(),
      items: materializeLiveItems(items, live),
    },
    null,
    2,
  );
}

function safeFilename(name: string): string {
  const cleaned = name.trim().replace(/[\\/:*?"<>|]+/g, "-").replace(/\s+/g, " ").slice(0, 80);
  return cleaned || "hiq-session";
}

export default function App() {
  // profileRef mirrors the active tab's product profile ("dev"|"cowork"). It is
  // kept in sync by the coworkActive effect below and read via the getter passed
  // to useController so the controller's OpenProjectTab/OpenGlobalTab calls scope
  // the topic to the active profile without stale-closure issues.
  const profileRef = useRef<"dev" | "cowork" | "netdev">("dev");
  const getProfile = useCallback(() => profileRef.current, []);
  const {
    state,
    activeTabId,
    send,
    runShell,
    steer,
    notice,
    cancel,
    pauseToggle,
    approve,
    answerQuestion,
    setCollaborationMode: setControllerCollaborationMode,
    setToolApprovalMode: setControllerToolApprovalMode,
    setGoal: setControllerGoal,
    clearGoal: clearControllerGoal,
    setRagScope: setControllerRagScope,
    clearSession,
    listSessions,
    listTrashedSessions,
    resumeSession,
    previewSession,
    deleteSession,
    restoreSession,
    purgeTrashedSession,
    renameSession,
    refreshMeta,
    pickWorkspace,
    switchWorkspace,
    rewind,
    setModel,
    setEffort,
    switchTab,
    openProjectTab,
    openGlobalTab,
    closeTab,
    reorderTabs,
    syncActiveTab,
    ensureBlankTab,
  } = useController(getProfile);
  const { locale, setPref: setLocalePref } = useI18n();
  const t = useT();
  const [modesByTab, setModesByTab] = useState<Record<string, Mode>>({});
  const [collaborationModesByTab, setCollaborationModesByTab] = useState<Record<string, CollaborationMode>>({});
  const [toolApprovalModesByTab, setToolApprovalModesByTab] = useState<Record<string, ToolApprovalMode>>({});
  const yoloRestoreToolApprovalModesRef = useRef<Record<string, RestorableToolApprovalMode>>({});
  const [goalsByTab, setGoalsByTab] = useState<Record<string, string>>({});
  const [goalDraftModesByTab, setGoalDraftModesByTab] = useState<Record<string, boolean>>({});
  const [tabMetas, setTabMetas] = useState<TabMeta[]>([]);
  const [tabOrderIds, setTabOrderIds] = useState<string[]>([]);
  const [tabRevealSignal, setTabRevealSignal] = useState(0);
  // coworkActive tracks whether the ACTIVE tab is in the coWork product profile.
  // Driven by app.Profile() on active-tab change + the "profile:changed" event
  // emitted after a SwitchProfile rebuild. When true the standard three-pane body
  // is hidden (via the app--cowork class) and a CoWorkLayout is rendered instead.
  const [coworkActive, setCoworkActive] = useState(false);
  // netdevActive mirrors coworkActive for 运维 profile: when true the
  // standard body is hidden (app--netdev) and the NetDevLayout shell renders.
  // Purely additive — dev/cowork behavior is byte-identical.
  const [netdevActive, setNetdevActive] = useState(false);
  // hiq:// 深链统一入口（DASHBOARD spec §4.12）：冷路径（启动 argv →
  // NetDevConsumeDeepLink）与热路径（第二实例 → hiq:deep-link 事件）都
  // 汇到这里——不在 netdev profile 时先切过去，落地后按 kind 分发。
  const [pendingDeepLink, setPendingDeepLink] = useState<{ kind: string; id: string } | null>(null);
  const deepLinkDispatchRef = useRef(false);
  // Skip the startup splash in the browser dev mock (no Wails runtime to wait
  // for) — only real desktop builds show it. Eliminates the 1.4–6s "stuck"
  // splash devs see when iterating in a browser.
  const [startupSplashVisible, setStartupSplashVisible] = useState<boolean>(() =>
    typeof window !== "undefined" && window.runtime ? shouldShowStartupSplash() : false,
  );
  // null until the mount probe resolves; true shows the overlay. Probed once —
  // clearing the key mid-session is the Settings panel's job, not the gate's.
  const [needsOnboarding, setNeedsOnboarding] = useState<boolean | null>(null);
  const [remoteWizardOpen, setRemoteWizardOpen] = useState(false);
  const [newTaskMenuPoint, setNewTaskMenuPoint] = useState<ContextMenuPoint | null>(null);
  // ?settings=<tab> (browser dev mock only — same affordance family as
  // bridge.ts's ?profile= and NetDevLayout's ?dock=) boots with that settings
  // tab open, so settings GUI smoke tests don't depend on clicking the gear.
  const initialSettingsTarget = (): SettingsTab | null => {
    try {
      if (typeof window !== "undefined" && !window.runtime) {
        const v = new URLSearchParams(window.location.search).get("settings");
        if (v && (SETTINGS_TABS as string[]).includes(v)) return v as SettingsTab;
      }
    } catch { /* not a browser */ }
    return null;
  };
  const [settingsTarget, setSettingsTarget] = useState<SettingsTab | null>(initialSettingsTarget);
  const [settingsPayload, setSettingsPayload] = useState<string | null>(null);
  const [startupUpdateChecksEnabled, setStartupUpdateChecksEnabled] = useState<boolean | null>(null);
  const [histView, setHistView] = useState<HistoryViewState | null>(null);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [shortcutsOpen, setShortcutsOpen] = useState(false);
  const [preferenceOpen, setPreferenceOpen] = useState(false);
  // 循环工程 panel (docs/loop-engineering-spec.md): sidebar entry under 编码偏好.
  const [loopOpen, setLoopOpen] = useState(false);
  // 团队 panel (multi-agent: 团长 + 团员 + 看板). Rendered from the coding
  // profile's sidebar too, so "团队开发项目" is reachable without a profile
  // switch; it renders the SAME TeamBoard the 办公 profile hosts.
  const [teamOpen, setTeamOpen] = useState(false);
  // 项目知识中枢 panel (code/docs/memory/team → one project map). Reachable
  // from the coding profile so "看清项目全貌与进度" needs no profile switch.
  const [kbOpen, setKbOpen] = useState(false);
  // 知识库 (RAG document/entity graph) panel. Previously cowork-only; the
  // coding profile now hosts the SAME RagPanel so it isn't lockable-in-office.
  const [ragOpen, setRagOpen] = useState(false);
  const [paletteSessions, setPaletteSessions] = useState<SessionMeta[]>([]);
  const [paletteCapabilities, setPaletteCapabilities] = useState<CapabilitiesView | null>(null);
  const { showToast } = useToast();
  const confirm = useConfirm();
  const [sidebarImConnections, setSidebarImConnections] = useState<SidebarImConnection[]>([]);
  const [imTopicSources, setImTopicSources] = useState<Record<string, SidebarImTopicSource>>({});
  const [sidebarImDetailConnectionId, setSidebarImDetailConnectionId] = useState("");
  const [sidebarImSessions, setSidebarImSessions] = useState<SessionMeta[]>([]);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(loadSidebarCollapsed);
  const [sidebarWidth, setSidebarWidth] = useState(loadSidebarWidth);
  const [sidebarResizing, setSidebarResizing] = useState(false);
  const [viewportWidth, setViewportWidth] = useState(() => (typeof window === "undefined" ? 1440 : window.innerWidth));
  // Default closed so the chat pane gets the full center column on launch.
  // Opened on demand via the AppChrome PanelRight toggle. No persistence —
  // resets to this calm default each launch.
  const [workspacePanelOpen, setWorkspacePanelOpen] = useState(false);
  // Cowork dock has its OWN open state, separate from the coding-mode
  // workspacePanelOpen. Rationale: the cowork overview dock (今日/邮件/文件)
  // is the centerpiece of office mode — it should default open and NOT be
  // closed when the user shrinks the coding-mode workspace panel. Sharing one
  // state leaked coding-mode preferences into cowork (a user who collapsed the
  // dev dock would arrive at cowork with no right column at all).
  const [coworkDockOpen, setCoworkDockOpen] = useState(true);
  // Netdev dock follows the same pattern (own open state, defaults open like
  // the mode's intrinsic right column, driven by the chrome workspace toggle
  // and the shared resizer's drag-to-edge close).
  const [netdevDockOpen, setNetdevDockOpen] = useState(true);
  const [rightDockTreeWidth, setRightDockTreeWidth] = useState(loadRightDockTreeWidth);
  const [rightDockPreviewWidth, setRightDockPreviewWidth] = useState(loadRightDockPreviewWidth);
  const [workspacePreviewActive, setWorkspacePreviewActive] = useState(false);
  // Custom wallpaper. This state exists only so the decorative layer mounts and
  // unmounts; the actual CSS values are written by lib/wallpaper.ts, which keeps
  // the settings preview and the live backdrop reading the same tokens.
  const [wallpaperActive, setWallpaperActive] = useState(false);
  useEffect(() => {
    let cancelled = false;
    void loadWallpaper().then((view) => {
      if (!cancelled) setWallpaperActive(Boolean(view?.active));
    });
    // The settings panel changes the wallpaper in a different subtree, so it
    // announces the change rather than lifting state through every parent.
    const unsubscribe = subscribeWallpaper((view) => setWallpaperActive(Boolean(view?.active)));
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, []);

  // Bump dockRefreshKey after each turn so WorkspacePanel/ContextPanel re-fetch
  // workspace changes, git history, and session metadata after AI tool writes.
  useEffect(() => {
    const unsub = onEvent((e) => {
      if (e.kind === "turn_done") {
        setDockRefreshKey((v) => v + 1);
        // Changed-tab activity dot (pane-system §3.3): lit after each turn,
        // cleared when the user visits the 改动 tab.
        setChangedDirty(true);
      }
    });
    return unsub;
  }, []);

  const [workspacePanelResizing, setWorkspacePanelResizing] = useState(false);
  const [workspacePanelMaximized, setWorkspacePanelMaximized] = useState(false);
  const [rightDockMode, setRightDockMode] = useState<RightDockMode>(loadRightDockMode);
  // Browser-style dock tabs (user request 2026-08-19): the strip shows only
  // OPEN tabs; each is closable, "+" offers the catalog in a dropdown, order
  // persists. Programmatic opens (auto-preview, tree menu) auto-add the tab.
  const [dockTabs, setDockTabs] = useState<RightDockMode[]>(loadDockTabs);
  const [dockAddMenuPoint, setDockAddMenuPoint] = useState<{ left: number; top: number } | null>(null);
  // closeWorkspacePanel is defined below (after its dependencies); the tab
  // closer needs it, hence this forward bridge.
  const closeWorkspacePanelRef = useRef<() => void>(() => {});
  useEffect(() => {
    try {
      setScopedItem(DOCK_TABS_KEY, JSON.stringify(dockTabs));
    } catch { /* storage unavailable */ }
  }, [dockTabs]);
  const ensureDockTab = useCallback((mode: RightDockMode) => {
    setDockTabs((prev) => (prev.includes(mode) ? prev : [...prev, mode]));
  }, []);
  // If the active tab was closed (or restored state desyncs), fall back.
  useEffect(() => {
    if (!dockTabs.includes(rightDockMode)) {
      setRightDockMode(dockTabs[dockTabs.length - 1] ?? "files");
    }
  }, [dockTabs, rightDockMode]);
  const closeDockTab = useCallback((mode: RightDockMode) => {
    setDockTabs((prev) => {
      const next = prev.filter((m) => m !== mode);
      if (next.length === 0) {
        closeWorkspacePanelRef.current();
        return prev;
      }
      setRightDockMode((active) => (active === mode ? next[next.length - 1] : active));
      return next;
    });
  }, []);
  // Pane-system P1 (docs/pane-system-spec.md §3.2/§3.3): auto-detected preview
  // URL, manual-close suppression, and the changed-tab activity dot.
  const [previewUrl, setPreviewUrl] = useState(loadPreviewUrl);
  const [changedDirty, setChangedDirty] = useState(false);
  const previewSuppressedRef = useRef("");

  // Auto-detect the newest localhost URL in tool output; on a NEW url, switch
  // to the preview tab and open the dock — unless the user manually closed it
  // on this same url (pane-system §3.3 anti-nag guard). Dev-profile only: the
  // preview dock belongs to the coding view, and a netdev/cowork transcript
  // mentioning localhost must not mutate the hidden dev dock's state.
  useEffect(() => {
    if (coworkActive || netdevActive) return;
    // state.items can be null transiently while useController hydrates a
    // restored tab — reading .length then crashes the restored view (one-off
    // startup React error observed 2026-08-29 E2E).
    if (!state.items) return;
    const re = /\b(?:https?:\/\/)?(?:localhost|127\.0\.0\.1)(?::\d{2,5})\b[^\s"'<>)]*/i;
    for (let i = state.items.length - 1; i >= 0; i--) {
      const it = state.items[i];
      if (it.kind !== "tool") continue;
      const m = re.exec(it.output ?? "");
      if (!m) continue;
      const raw = m[0];
      const url = /^https?:\/\//i.test(raw) ? raw : `http://${raw}`;
      if (url === previewUrl) return;
      setPreviewUrl(url);
      if (previewSuppressedRef.current !== url) {
        ensureDockTab("preview");
        setRightDockMode("preview");
        setWorkspacePanelOpen(true);
      }
      return;
    }
  }, [state.items, previewUrl, coworkActive, netdevActive]);

  // Remember the last active dock tab.
  useEffect(() => {
    try {
      setScopedItem(RIGHT_DOCK_MODE_KEY, rightDockMode);
    } catch { /* storage unavailable */ }
  }, [rightDockMode]);

  // Browser mirror (pane-system §3.6 companion): forward every kernel frame
  // into the module store (always-on — the stream survives dock/tab closes),
  // and on activity onset auto-open the cowork dock on the 浏览器 tab. Browser
  // tools are cowork-profile only, so other profiles just record frames.
  // Anti-nag mirrors the preview dock: closing the dock mid-run suppresses
  // reopening until the next run/session start.
  const browserMirrorSuppressedRef = useRef(false);
  useEffect(
    () =>
      onBrowserMirror((frame) => {
        if (frame.kind === "status" && frame.phase === "start") {
          browserMirrorSuppressedRef.current = false;
        }
        const startedActivity = pushBrowserMirrorFrame(frame);
        if (!startedActivity || !coworkActive || browserMirrorSuppressedRef.current) return;
        requestBrowserMirrorFocus();
        setCoworkDockOpen(true);
      }),
    [coworkActive],
  );

  const commitPreviewUrl = useCallback((url: string) => {
    setPreviewUrl(url);
    try {
      setScopedItem(PREVIEW_URL_KEY, url);
    } catch { /* storage unavailable */ }
  }, []);

  // Visiting the 改动 tab clears its activity dot.
  useEffect(() => {
    if (rightDockMode === "changed") setChangedDirty(false);
  }, [rightDockMode]);
  const [dockRefreshKey, setDockRefreshKey] = useState(0);
  const [projectRevision, setProjectRevision] = useState(0);

  const [composerInsertRequest, setComposerInsertRequest] = useState<ComposerInsertRequest | null>(null);
  const [transientOverlayDismissSignal, setTransientOverlayDismissSignal] = useState(0);
  const [desktopPlatform, setDesktopPlatform] = useState<DesktopPlatform>(detectBrowserPlatform);
  const [expandThinking, setExpandThinking] = useState(false);
  const [renamingTopicId, setRenamingTopicId] = useState<string | null>(null);
  const [topicTitleDraft, setTopicTitleDraft] = useState("");
  const [sidebarTogglePressed, setSidebarTogglePressed] = useState(false);
  const [workspaceTogglePressed, setWorkspaceTogglePressed] = useState(false);
  const [clearContextPending, setClearContextPending] = useState(false);
  const topicRenameSkipCommitRef = useRef(false);
  const topicRenameCommitHandledRef = useRef(false);
  const appRef = useRef<HTMLDivElement>(null);
  const sidebarTogglePressTimerRef = useRef<number | null>(null);
  const workspaceTogglePressTimerRef = useRef<number | null>(null);
  const sidebarImRefreshRef = useRef({ last: 0, inFlight: false });

  // Persist window geometry across launches.
  useWindowStatePersistence();

  useEffect(() => {
  }, []);

  const closeTransientOverlays = useCallback(() => {
    setTransientOverlayDismissSignal((signal) => signal + 1);
  }, []);

  // Toggleable terminal console under the composer (ZCode-style Ctrl+`).
  const [terminalOpen, setTerminalOpen] = useState(loadTerminalOpen);
  // Bottom axis's side session (pane-system §3.5): which open tab the mini
  // composer talks to; null → auto-pick the first non-active session tab.
  const [sideSessionTabId, setSideSessionTabId] = useState<string | null>(null);
  // 人工终端设备页签（NETDEV_SPEC_V2 §10.5）：设备卡「终端」经
  // "hiq:netdev-terminal" 事件抵达——面板打开并新增该设备的页签。
  const [openDeviceTerm, setOpenDeviceTerm] = useState<{ device: string; seq: number }>({ device: "", seq: 0 });
  useEffect(() => {
    const onOpenDevice = (e: Event) => {
      const d = (e as CustomEvent<{ device: string }>).detail;
      if (!d?.device) return;
      setTerminalOpen((v) => {
        if (!v) saveTerminalOpen(true);
        return true;
      });
      setOpenDeviceTerm((prev) => ({ device: d.device, seq: prev.seq + 1 }));
    };
    window.addEventListener("hiq:netdev-terminal", onOpenDevice);
    return () => window.removeEventListener("hiq:netdev-terminal", onOpenDevice);
  }, []);
  const toggleTerminal = useCallback(() => {
    setTerminalOpen((v) => {
      saveTerminalOpen(!v);
      return !v;
    });
  }, []);
  // Loop Engineering emergency stop (spec §2.4): Ctrl+Shift+\ kills the loop
  // from anywhere, mirroring the netdev e-stop precedent.
  useEffect(() => {
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.ctrlKey && event.shiftKey && event.code === "Backslash") {
        event.preventDefault();
        void app.LoopStop("emergency-hotkey");
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  useEffect(() => {
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.ctrlKey && event.code === "Backquote") {
        event.preventDefault();
        setTerminalOpen((v) => {
          saveTerminalOpen(!v);
          return !v;
        });
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const reloadSidebarImConnections = useCallback(async () => {
    const settings = await app.Settings();
    setSidebarImConnections(sidebarImConnectionsFromBot(settings.bot, t));
    setImTopicSources(sidebarImTopicSourcesFromBot(settings.bot, t));
  }, [t]);

  const quietlyRefreshSidebarImConnections = useCallback(() => {
    const now = Date.now();
    if (sidebarImRefreshRef.current.inFlight || now - sidebarImRefreshRef.current.last < 1000) return;
    sidebarImRefreshRef.current.inFlight = true;
    sidebarImRefreshRef.current.last = now;
    void reloadSidebarImConnections()
      .catch((e) => console.warn("bot sidebar refresh failed", e))
      .finally(() => {
        sidebarImRefreshRef.current.inFlight = false;
      });
  }, [reloadSidebarImConnections]);

  const openBotSettings = useCallback(() => {
    closeTransientOverlays();
    setSidebarImDetailConnectionId("");
    setSettingsTarget("bots");
  }, [closeTransientOverlays]);

  const pulseSidebarToggle = useCallback(() => {
    if (typeof window === "undefined") return;
    if (sidebarTogglePressTimerRef.current !== null) {
      window.clearTimeout(sidebarTogglePressTimerRef.current);
    }
    setSidebarTogglePressed(true);
    sidebarTogglePressTimerRef.current = window.setTimeout(() => {
      sidebarTogglePressTimerRef.current = null;
      setSidebarTogglePressed(false);
    }, 260);
  }, []);

  const pulseWorkspaceToggle = useCallback(() => {
    if (typeof window === "undefined") return;
    if (workspaceTogglePressTimerRef.current !== null) {
      window.clearTimeout(workspaceTogglePressTimerRef.current);
    }
    setWorkspaceTogglePressed(true);
    workspaceTogglePressTimerRef.current = window.setTimeout(() => {
      workspaceTogglePressTimerRef.current = null;
      setWorkspaceTogglePressed(false);
    }, 260);
  }, []);

  const anchorAppScrollToChat = useCallback(() => {
    if (typeof window === "undefined") return;
    const el = appRef.current;
    if (!el) return;
    const pin = () => {
      el.scrollLeft = 0;
    };
    pin();
    window.requestAnimationFrame(pin);
    window.setTimeout(pin, 300);
  }, []);

  useEffect(() => {
    return () => {
      if (sidebarTogglePressTimerRef.current !== null) {
        window.clearTimeout(sidebarTogglePressTimerRef.current);
      }
      if (workspaceTogglePressTimerRef.current !== null) {
        window.clearTimeout(workspaceTogglePressTimerRef.current);
      }
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    const override = browserPlatformOverride();
    if (override) {
      setDesktopPlatform(override);
      return () => {
        cancelled = true;
      };
    }
    void app.Platform()
      .then((value) => {
        if (!cancelled) setDesktopPlatform(normalizeDesktopPlatform(value));
      })
      .catch((e) => {
        console.warn("platform probe failed", e);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const applyDesktopPreferences = useCallback(
    (settings: Pick<SettingsView, "desktopTheme" | "desktopThemeStyle" | "desktopLanguage" | "checkUpdates">) => {
      const nextTheme = normalizeThemePreference(settings.desktopTheme);
      const nextStyle = normalizeThemeStyleForTheme(settings.desktopThemeStyle, nextTheme);
      applyTheme(nextTheme, nextStyle, { persist: false });
      setLocalePref(normalizeLangPref(settings.desktopLanguage));
      setStartupUpdateChecksEnabled(settings.checkUpdates !== false);
    },
    [setLocalePref],
  );

  useEffect(() => {
    let cancelled = false;
    const syncDesktopPreferences = async () => {
      const legacyLanguage = readLegacyLangPref();
      const legacyTheme = readLegacyThemePreference();
      if (legacyLanguage || legacyTheme.hasValue) {
        await app.MigrateDesktopPreferences(legacyLanguage, legacyTheme.theme, legacyTheme.style);
        clearLegacyLangPref();
        clearLegacyThemePreference();
      }
      const settings = await app.Settings();
      if (cancelled) return;
      applyDesktopPreferences(settings);
      setExpandThinking(settings.expandThinking);
      hydrateDisplayMode(settings.displayMode);
      setSidebarImConnections(sidebarImConnectionsFromBot(settings.bot, t));
      setImTopicSources(sidebarImTopicSourcesFromBot(settings.bot, t));
    };
    void syncDesktopPreferences().catch((e) => {
      console.warn("desktop preferences sync failed", e);
      setStartupUpdateChecksEnabled(true);
    });
    return () => {
      cancelled = true;
    };
  }, [applyDesktopPreferences, t]);

  useEffect(() => {
    setSidebarImDetailConnectionId((current) => {
      if (!current) return "";
      return sidebarImConnections.some((connection) => connection.id === current) ? current : "";
    });
  }, [sidebarImConnections]);

  // Open settings when the native menu item (CmdOrCtrl+,) is activated (Wails
  // backend event), OR when another frontend component (e.g. the dock's IM bot
  // row) dispatches a DOM CustomEvent to open a specific settings tab.
  useEffect(() => {
    // Backend event (keyboard shortcut) — always opens "general".
    if (typeof window !== "undefined" && window.runtime) {
      window.runtime.EventsOn("app:open-settings", () => {
        closeTransientOverlays();
        setSettingsTarget("general");
      });
    }
    // Frontend DOM event (e.g. dock → settings → bots). detail carries the tab.
    const validTabs = ["general", "models", "bots", "cowork", "mcp", "skills", "memory", "permissions", "sandbox", "network", "hooks", "appearance", "updates", "mobile", "netdev"];
    const handler = (e: Event) => {
      closeTransientOverlays();
      const tab = (e as CustomEvent<string>).detail;
      setSettingsTarget((validTabs.includes(tab) ? tab : "general") as SettingsTab);
    };
    window.addEventListener("app:open-settings-tab", handler as EventListener);
    return () => window.removeEventListener("app:open-settings-tab", handler as EventListener);
  }, [closeTransientOverlays]);

  // Screenshot hotkey recognition results — surface as a toast so the user sees
  // the VLM output even when Hiq isn't focused.
  useEffect(() => {
    if (typeof window === "undefined" || !window.runtime) return;
    return window.runtime.EventsOn("screenshot:notice", (...data: unknown[]) => {
      const e = (data?.[0] ?? {}) as { message?: string };
      if (e.message) showToast(e.message, "info");
    });
  }, [showToast]);

  // Emergency-stop hotkey confirmation — the global Ctrl+Shift+Pause fired and
  // cancelled the in-flight turn. Surface a prominent (error-level = red) toast
  // so the user gets immediate visible confirmation the stop landed, even when
  // Hiq is in the background (the event is emitted from the backend).
  useEffect(() => {
    if (typeof window === "undefined" || !window.runtime) return;
    return window.runtime.EventsOn("estop:fired", (...data: unknown[]) => {
      const e = (data?.[0] ?? {}) as { message?: string };
      showToast(e.message ?? "已紧急停止 AI 操作", "error");
    });
  }, [showToast]);

  // Global scheduler notice: when a scheduled task with output_mode="notify"
  // fires, the backend emits "scheduler:notice". This listener is registered at
  // the app root (not inside CalendarTaskPanel) so the toast
  // surfaces regardless of which panel the user is currently viewing —
  // previously the notice was only visible while the automation/calendar panel
  // was mounted, and switching to experts/rag/chat silently swallowed it.
  useEffect(() => {
    return onSchedulerNotice((e) => {
      showToast(`${e.name}: ${(e.result || "").slice(0, 100)}`, "info");
    });
  }, [showToast]);

  // PPT reference pre-analysis degraded: the desktop layer analyzed (or failed
  // to analyze) a reference image/PDF BEFORE the message reached the model
  // (mayPreparePPTReference), but something dropped the reference — VLM not
  // configured, VLM timeout, oversized image, PDF render failure, page-cap
  // truncation, or a pasted path that doesn't exist. All of these used to be
  // silent: the user got a deck that ignored their reference with no idea why.
  // The warn toast restores the user's chance to fix it (configure VLM, shrink
  // the image, upload instead of pasting a path, …).
  useEffect(() => {
    if (typeof window === "undefined" || !window.runtime) return;
    return window.runtime.EventsOn("ppt:reference-warning", (...data: unknown[]) => {
      const e = (data?.[0] ?? {}) as { hint?: string };
      if (e.hint) showToast(e.hint, "warn");
    });
  }, [showToast]);

  // PPT reference verdict: the pre-analysis finished and judged the reference
  // (visually designed → redraw similar; plain text → harvest words as
  // material). Surfaced as a short info toast so the user can catch a misroute
  // ("it judged plain text but I wanted the layout copied") while it's still
  // cheap to correct — human confirmation beats the code guessing intent.
  useEffect(() => {
    if (typeof window === "undefined" || !window.runtime) return;
    return window.runtime.EventsOn("ppt:reference-analyzed", (...data: unknown[]) => {
      const e = (data?.[0] ?? {}) as { is_visual?: boolean };
      showToast(
        e.is_visual ? "已识别参考图样式，将按参考图生成 PPT" : "参考图判断为纯文字，将提取文字内容作为素材",
        "info",
      );
    });
  }, [showToast]);

  useEffect(() => {
    if (typeof window === "undefined") return;
    const onResize = () => setViewportWidth(window.innerWidth);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);
  // Per-tab pending plan revision text. Keyed by tab id so a revision drafted
  // in one tab is never sent to another (previously a single global value that
  // could leak across tabs when the active tab changed).
  const [pendingPlanRevisionsByTab, setPendingPlanRevisionsByTab] = useState<Record<string, string>>({});
  const [footerHeight, setFooterHeight] = useState(0);
  const footerHeightRef = useRef(0);
  const footerRef = useRef<HTMLElement>(null);
  const runningRef = useRef(state.running);
  // Retry-last-failed-turn (upgrade spec 1-7): the NoticeCard's 重试 button
  // emits a DOM event; we answer it by resending the newest user prompt that
  // precedes a retryable failure notice. A ref mirrors state so the listener
  // always sees the current items without re-subscribing.
  const itemsRef = useRef(state.items);
  itemsRef.current = state.items;
  // Assigned after handleSend is declared below (it is defined later in the
  // component); the listener reads through the ref so ordering doesn't matter.
  const retrySendRef = useRef<((text: string) => void) | null>(null);
  useEffect(() => {
    const onRetry = () => {
      const items = itemsRef.current;
      for (let i = items.length - 1; i >= 0; i--) {
        const it = items[i];
        if (it.kind === "notice" && it.retryable) continue;
        if (it.kind === "user" && it.text.trim()) {
          retrySendRef.current?.(it.text);
          return;
        }
        if (it.kind === "assistant" || it.kind === "turn_summary") continue;
        return; // a tool/notice boundary without a trailing user message — nothing safe to resend
      }
    };
    window.addEventListener("hiq:retry-turn", onRetry);
    return () => window.removeEventListener("hiq:retry-turn", onRetry);
  }, []);
  const rightDockDetailActive = rightDockMode !== "turns" && workspacePreviewActive;
  const preferredWorkspacePanelWidth = rightDockDetailActive ? rightDockPreviewWidth : rightDockTreeWidth;
  const workspacePanelMinWidth = rightDockDetailActive ? RIGHT_DOCK_PREVIEW_MIN_WIDTH : RIGHT_DOCK_TREE_MIN_WIDTH;
  
  const activeDockOpen = coworkActive ? coworkDockOpen : netdevActive ? netdevDockOpen : workspacePanelOpen;
  const chatReservedWidth = coworkActive ? 0 : (workspacePanelOpen && !workspacePanelMaximized ? CHAT_COMFORT_MIN_WIDTH : CHAT_MIN_WIDTH);
  
  const workspacePanelAvailableWidth = availableWorkspacePanelWidth({
    viewportWidth,
    sidebarCollapsed,
    sidebarWidth,
    chatMinWidth: chatReservedWidth,
    resizerWidth: WORKSPACE_RESIZER_WIDTH,
  });

  const resolvedWorkspacePanelWidth = resolveWorkspacePanelWidth({
    open: activeDockOpen,
    maximized: workspacePanelMaximized,
    preferredWidth: preferredWorkspacePanelWidth,
    minWidth: workspacePanelMinWidth,
    availableWidth: workspacePanelAvailableWidth,
  });

  const workspacePanelRenderable =
    workspacePanelOpen && (workspacePanelMaximized || resolvedWorkspacePanelWidth >= RIGHT_DOCK_MIN_RENDER_WIDTH);
  const workspacePanelGridOpen = workspacePanelRenderable && !workspacePanelMaximized;
  const workspacePanelRenderWidth = workspacePanelMaximized ? preferredWorkspacePanelWidth : resolvedWorkspacePanelWidth;
  // The cowork dock reuses the same width resolution (so resize/maximize behave
  // consistently) but pairs it with the cowork-specific open state. So a coding-
  // mode panel close doesn't hide the cowork overview.
  const coworkDockRenderable =
    coworkDockOpen && (workspacePanelMaximized || resolvedWorkspacePanelWidth >= RIGHT_DOCK_MIN_RENDER_WIDTH);
  const activeTab = useMemo(
    () => tabMetas.find((tab) => tab.id === activeTabId) ?? tabMetas.find((tab) => tab.active),
    [activeTabId, tabMetas],
  );
  const sidebarImDetailConnection = useMemo(
    () => sidebarImConnections.find((connection) => connection.id === sidebarImDetailConnectionId) ?? null,
    [sidebarImConnections, sidebarImDetailConnectionId],
  );

  // Load this contact's bot sessions (transcripts whose .meta was stamped with
  // platform/remoteID/mode="bot" by OnTurnFinished) so the sidebar detail can
  // list every conversation the contact had with the bot, newest first.
  useEffect(() => {
    const conn = sidebarImDetailConnection;
    if (!conn) { setSidebarImSessions([]); return; }
    let cancelled = false;
    app.ListSessions().then((all) => {
      if (cancelled) return;
      setSidebarImSessions(
        all.filter(
          (s) =>
            s.mode === "bot" &&
            s.platform === conn.platform &&
            s.remoteId === conn.remoteId &&
            (s.chatType || "") === conn.chatType &&
            (s.chatId || "") === conn.chatId,
        ),
      );
    }).catch(() => { if (!cancelled) setSidebarImSessions([]); });
    return () => { cancelled = true; };
  }, [sidebarImDetailConnection]);

  // Sidebar "Recent" sessions (ui-redesign §4-B4): loaded proactively, refreshed
  // on tab switch and after resume/rename/delete so ages stay fresh.
  const [sidebarSessions, setSidebarSessions] = useState<SessionMeta[]>([]);
  const refreshSidebarSessions = useCallback(() => {
    listSessions().then((all) => setSidebarSessions(all)).catch(() => { /* offline: keep last list */ });
  }, [listSessions]);
  useEffect(() => {
    refreshSidebarSessions();
  }, [refreshSidebarSessions, activeTab]);
  // Also refresh sidebar sessions when the profile changes (dev/cowork/netdev).
  // profileRef.current is updated synchronously but listSessions reads it at
  // call-time, so a manual trigger keyed on the profile flags is needed.
  useEffect(() => {
    refreshSidebarSessions();
  }, [coworkActive, netdevActive, refreshSidebarSessions]);
  // Project tree topic IDs for deduplication: sessions already visible in the
  // tree are hidden from the "Recent" list to avoid showing the same entry twice.
  const [projectTreeTopicIds, setProjectTreeTopicIds] = useState<Set<string>>(new Set());
  const startupSplashHold = state.meta?.ready !== true && !state.meta?.startupErr;
  const legacyMode = activeTabId ? modesByTab[activeTabId] ?? "normal" : "normal";
  const goal = activeTabId ? goalsByTab[activeTabId] ?? state.meta?.goal ?? activeTab?.goal ?? "" : "";
  const goalDraftMode = activeTabId ? Boolean(goalDraftModesByTab[activeTabId]) : false;
  const collaborationMode = activeTabId
    ? displayedCollaborationMode({
        goalDraftMode,
        localMode: collaborationModesByTab[activeTabId],
        metaGoal: state.meta?.goal,
        tabMode: activeTab?.collaborationMode,
        goal,
        legacyMode,
      })
    : "normal";
  const toolApprovalMode = activeTabId
    ? toolApprovalModesByTab[activeTabId] ?? normalizeToolApprovalMode(state.meta?.toolApprovalMode ?? activeTab?.toolApprovalMode, legacyMode, state.meta?.autoApproveTools ?? state.meta?.bypass)
    : "ask";
  // Knowledge-base auto-injection scope, per-tab ("" = 不使用 / off, default).
  // Sourced directly from backend meta since the selection is persisted there;
  // refreshMetaForTab re-syncs after a change.
  const ragScope = state.meta?.ragScope ?? "";
  const controllerReady = state.meta?.ready === true;
  const setMode = useCallback(
    (next: Mode | ((prev: Mode) => Mode)) => {
      if (!activeTabId) return;
      setModesByTab((current) => {
        const prev = current[activeTabId] ?? "normal";
        const value = typeof next === "function" ? next(prev) : next;
        if (value === prev) return current;
        return { ...current, [activeTabId]: value };
      });
    },
    [activeTabId],
  );
  const setGoalDraftModeForTab = useCallback((tabId: string, enabled: boolean) => {
    setGoalDraftModesByTab((current) => {
      if (Boolean(current[tabId]) === enabled) return current;
      if (enabled) return { ...current, [tabId]: true };
      const next = { ...current };
      delete next[tabId];
      return next;
    });
  }, []);
  const topicbarEditing = Boolean(activeTab?.topicId && activeTab.topicId === renamingTopicId);
  const visibleTabId = activeTabId;
  const visibleTabs = useMemo(() => {
    const byId = new Map(tabMetas.map((tab) => [tab.id, tab]));
    const ordered = tabOrderIds.map((id) => byId.get(id)).filter((tab): tab is TabMeta => Boolean(tab));
    const missing = tabMetas.filter((tab) => !tabOrderIds.includes(tab.id));
    return [...ordered, ...missing].map((tab) => ({
      ...tab,
      running: tab.id === visibleTabId ? tab.running || state.running : tab.running,
      mode: modesByTab[tab.id] ?? normalizeMode(tab.mode),
      collaborationMode: tabListCollaborationMode({
        goalDraftMode: Boolean(goalDraftModesByTab[tab.id]),
        localMode: collaborationModesByTab[tab.id],
        tabMode: tab.collaborationMode,
        tabGoal: goalsByTab[tab.id] ?? tab.goal,
        legacyMode: normalizeMode(tab.mode),
      }),
      toolApprovalMode: toolApprovalModesByTab[tab.id] ?? normalizeToolApprovalMode(tab.toolApprovalMode, normalizeMode(tab.mode), tab.toolApprovalMode === "yolo"),
      goal: goalsByTab[tab.id] ?? tab.goal ?? "",
      active: tab.id === visibleTabId,
    }));
  }, [collaborationModesByTab, goalDraftModesByTab, goalsByTab, modesByTab, state.running, tabMetas, tabOrderIds, toolApprovalModesByTab, visibleTabId]);

  useEffect(() => {
    const ids = tabMetas.map((tab) => tab.id);
    setTabOrderIds((current) => {
      const next = current.filter((id) => ids.includes(id));
      for (const id of ids) {
        if (!next.includes(id)) next.push(id);
      }
      return next.join("\u0000") === current.join("\u0000") ? current : next;
    });
  }, [tabMetas]);

  useEffect(() => {
    const ids = new Set(tabMetas.map((tab) => tab.id));
    for (const id of Object.keys(yoloRestoreToolApprovalModesRef.current)) {
      if (!ids.has(id)) delete yoloRestoreToolApprovalModesRef.current[id];
    }
    setGoalDraftModesByTab((current) => {
      let changed = false;
      const next: Record<string, boolean> = {};
      for (const tab of tabMetas) {
        if (keepGoalDraftMode(Boolean(current[tab.id]), tab.goal)) {
          next[tab.id] = true;
        } else if (current[tab.id]) {
          changed = true;
        }
      }
      for (const id of Object.keys(current)) {
        if (!ids.has(id)) changed = true;
      }
      return changed ? next : current;
    });
    setModesByTab((current) => {
      let changed = false;
      const next: Record<string, Mode> = {};
      for (const tab of tabMetas) {
        const mode = normalizeMode(tab.mode);
        next[tab.id] = mode;
        if (current[tab.id] !== mode) changed = true;
      }
      for (const id of Object.keys(current)) {
        if (!ids.has(id)) changed = true;
      }
      return changed ? next : current;
    });
    setCollaborationModesByTab((current) => {
      let changed = false;
      const next: Record<string, CollaborationMode> = {};
      for (const tab of tabMetas) {
        const value = tabListCollaborationMode({
          goalDraftMode: keepGoalDraftMode(Boolean(goalDraftModesByTab[tab.id]), tab.goal),
          tabMode: tab.collaborationMode,
          tabGoal: tab.goal,
          legacyMode: normalizeMode(tab.mode),
        });
        next[tab.id] = value;
        if (current[tab.id] !== value) changed = true;
      }
      for (const id of Object.keys(current)) {
        if (!ids.has(id)) changed = true;
      }
      return changed ? next : current;
    });
    setToolApprovalModesByTab((current) => {
      let changed = false;
      const next: Record<string, ToolApprovalMode> = {};
      for (const tab of tabMetas) {
        const value = normalizeToolApprovalMode(tab.toolApprovalMode, normalizeMode(tab.mode));
        next[tab.id] = value;
        if (current[tab.id] !== value) changed = true;
      }
      for (const id of Object.keys(current)) {
        if (!ids.has(id)) changed = true;
      }
      return changed ? next : current;
    });
    setGoalsByTab((current) => {
      let changed = false;
      const next: Record<string, string> = {};
      for (const tab of tabMetas) {
        const value = tab.goal ?? "";
        next[tab.id] = value;
        if (current[tab.id] !== value) changed = true;
      }
      for (const id of Object.keys(current)) {
        if (!ids.has(id)) changed = true;
      }
      return changed ? next : current;
    });
  }, [goalDraftModesByTab, tabMetas]);

  useEffect(() => {
    if (!renamingTopicId || activeTab?.topicId === renamingTopicId) return;
    topicRenameSkipCommitRef.current = false;
    topicRenameCommitHandledRef.current = false;
    setRenamingTopicId(null);
    setTopicTitleDraft("");
  }, [activeTab?.topicId, renamingTopicId]);

  useEffect(() => {
    if (!activeTabId || !state.meta) return;
    const nextGoal = state.meta.goalStatus === "running" ? state.meta.goal ?? "" : "";
    if (nextGoal) setGoalDraftModeForTab(activeTabId, false);
    setGoalsByTab((current) => (current[activeTabId] === nextGoal ? current : { ...current, [activeTabId]: nextGoal }));
    setCollaborationModesByTab((current) => {
      const nextMode = metaSyncedCollaborationMode({ nextGoal, goalDraftMode, legacyMode });
      return current[activeTabId] === nextMode ? current : { ...current, [activeTabId]: nextMode };
    });
  }, [activeTabId, goalDraftMode, legacyMode, setGoalDraftModeForTab, state.meta]);

  useEffect(() => {
    void app.SetTrayLocale(locale).catch(() => {});
  }, [locale]);

  const applyCollaborationMode = useCallback(
    (m: CollaborationMode) => {
      if (!activeTabId) return;
      if (m === "goal") {
        setGoalDraftModeForTab(activeTabId, true);
        setCollaborationModesByTab((current) => (current[activeTabId] === "goal" ? current : { ...current, [activeTabId]: "goal" }));
        setMode(modeFromAxes(false, toolApprovalMode === "yolo"));
        void setControllerCollaborationMode("normal");
        return;
      }
      setGoalDraftModeForTab(activeTabId, false);
      setCollaborationModesByTab((current) => (current[activeTabId] === m ? current : { ...current, [activeTabId]: m }));
      if (m === "normal" || m === "plan") {
        setGoalsByTab((current) => (current[activeTabId] ? { ...current, [activeTabId]: "" } : current));
      }
      setMode(modeFromAxes(m === "plan", toolApprovalMode === "yolo"));
      void setControllerCollaborationMode(m);
    },
    [activeTabId, setControllerCollaborationMode, setGoalDraftModeForTab, setMode, toolApprovalMode],
  );
  const applyToolApprovalMode = useCallback(
    (m: ToolApprovalMode) => {
      if (!activeTabId) return;
      if (m === "yolo") {
        if (toolApprovalMode !== "yolo") {
          yoloRestoreToolApprovalModesRef.current[activeTabId] = restorableToolApprovalMode(toolApprovalMode);
        }
      } else {
        yoloRestoreToolApprovalModesRef.current[activeTabId] = restorableToolApprovalMode(m);
      }
      setToolApprovalModesByTab((current) => (current[activeTabId] === m ? current : { ...current, [activeTabId]: m }));
      setMode(modeFromAxes(collaborationMode === "plan", m === "yolo"));
      void setControllerToolApprovalMode(m);
    },
    [activeTabId, collaborationMode, setControllerToolApprovalMode, setMode, toolApprovalMode],
  );
  const toggleYoloApprovalMode = useCallback(() => {
    if (!activeTabId) return;
    const next = toggleYoloToolApprovalMode(
      toolApprovalMode,
      yoloRestoreToolApprovalModesRef.current[activeTabId],
    );
    if (next.restore) {
      yoloRestoreToolApprovalModesRef.current[activeTabId] = next.restore;
    }
    applyToolApprovalMode(next.mode);
  }, [activeTabId, applyToolApprovalMode, toolApprovalMode]);
  const applyGoal = useCallback(
    (nextGoal: string) => {
      if (!activeTabId) return;
      const trimmed = nextGoal.trim();
      setGoalDraftModeForTab(activeTabId, false);
      setGoalsByTab((current) => (current[activeTabId] === trimmed ? current : { ...current, [activeTabId]: trimmed }));
      setCollaborationModesByTab((current) => {
        const nextMode = trimmed ? "goal" : "normal";
        return current[activeTabId] === nextMode ? current : { ...current, [activeTabId]: nextMode };
      });
      setMode(modeFromAxes(false, toolApprovalMode === "yolo"));
      void (trimmed ? setControllerGoal(trimmed) : clearControllerGoal());
    },
    [activeTabId, clearControllerGoal, setControllerGoal, setGoalDraftModeForTab, setMode, toolApprovalMode],
  );
  // Shift+Tab toggles only the collaboration axis; Ctrl/Cmd+Y toggles YOLO on the
  // tool-permission axis while preserving the Ask/Auto base mode.
  const cycleMode = useCallback(() => {
    applyCollaborationMode(collaborationMode === "plan" ? "normal" : "plan");
  }, [applyCollaborationMode, collaborationMode]);

  // Switching models rebuilds the controller, which starts in normal mode — so
  // re-apply the current mode, or the pill would say plan/YOLO while the fresh
  // controller silently uses normal gating.
  const switchModel = useCallback(
    async (name: string) => {
      await setModel(name);
      await setControllerCollaborationMode(controllerCollaborationMode({ collaborationMode, goal }));
      await setControllerToolApprovalMode(toolApprovalMode);
      if (goal.trim()) await setControllerGoal(goal);
    },
    [collaborationMode, goal, setControllerCollaborationMode, setControllerGoal, setControllerToolApprovalMode, setModel, toolApprovalMode],
  );

  // switchProfile rebuilds the active tab's controller with a product profile
  // bundle (dev/cowork). The Go side carries conversation history across the
  // rebuild (see app.SwitchProfileForTab); we optimistically flip coworkActive so
  // the layout swaps without waiting for the profile:changed event, and revert on
  // error. The composer/mode/goal re-application is unnecessary here — the rebuild
  // preserves them via applyTabModeToController on the Go side.
  // switchProfile implements VIEW-SWITCH semantics: toggling dev/cowork does NOT
  // convert the current tab to the other profile. Instead it finds an existing
  // tab of the target profile (preferring the same scope/workspaceRoot) and
  // activates it, or creates a fresh blank tab in the target profile if none
  // exists. The current tab stays as-is in its own profile — so a dev tab is
  // never lost or relabeled when you peek at cowork, and switching back is just
  // clicking the dev tab again. The sidebar follows the active tab's profile
  // automatically (see the active-tab-sync effect below).
  const switchProfile = useCallback(
    async (name: string) => {
      const rawProfile = name.toLowerCase();
      const targetProfile: "dev" | "cowork" | "netdev" = rawProfile === "cowork" || rawProfile === "netdev" ? rawProfile : "dev";
      // Close any open modal/overlay (History panel, transient popovers) — their
      // cached content belongs to the outgoing profile's view and would be stale.
      closeTransientOverlays();
      setHistView(null);
      // Strict profile isolation (08-21): a mode switch NEVER carries the
      // workspace root across profiles — landing picks an existing tab of the
      // target profile, else its home project 工作台 (empty root home-fills).
      const anyMatch = tabMetas.find(
        (t) => (t.profile ?? "dev").toLowerCase() === targetProfile,
      );
      try {
        if (anyMatch) {
          // Activate the existing target-profile tab. Tab activation drives
          // coworkActive/profileRef/sidebar via the active-tab-sync effect, so
          // no manual layout flip is needed.
          await switchTab(anyMatch.id);
        } else {
          // No tab of the target profile exists yet — create a blank one. The
          // explicit profile arg ensures it boots in the target mode regardless
          // of the current tab's profile.
          await ensureBlankTab("project", "", targetProfile);
        }
        // Optimistically flip the layout so the view swaps immediately, without
        // waiting for the backend's profile:changed event to round-trip. The
        // active-tab-sync effect re-confirms (or corrects) coworkActive/
        // netdevActive once the new tab's profile field arrives; this just
        // removes the dead time. In the browser dev mock (no Go backend) this
        // flip is what makes the CoWorkLayout/NetDevLayout actually render,
        // since the event never fires. All three profiles flip here — folding
        // netdev to dev would scope any interim topic creation to the wrong
        // session partition.
        setCoworkActive(targetProfile === "cowork");
        setNetdevActive(targetProfile === "netdev");
        profileRef.current = targetProfile;
        setProjectRevision((v) => v + 1);
      } catch (err) {
        console.error("[switchProfile] failed:", err);
        if (String(err).includes("finish or cancel the current turn") || String(err).includes("turn")) {
          notice("请先停止当前运行的任务（如分析截图等），再切换模式！", "warn");
        } else {
          notice("切换模式失败: " + String(err), "warn");
        }
      }
    },
    [activeTab, tabMetas, switchTab, ensureBlankTab, closeTransientOverlays, notice],
  );

  // hiq:// 深链落地（DASHBOARD spec §4.12）：冷路径（启动 argv →
  // NetDevConsumeDeepLink）与热路径（第二实例 → hiq:deep-link 事件）汇到
  // 这里——不在 netdev profile 先切过去，到位后按 kind 分发（导航型 only，
  // Go 侧解析器已 fail-closed）。
  useEffect(() => {
    const dispatch = (r: { kind: string; id: string }) => {
      if (r.kind === "finding") {
        window.dispatchEvent(new CustomEvent("hiq:netdev-open-screen", { detail: { screen: "chain", finding: r.id } }));
      } else if (r.kind === "case") {
        window.dispatchEvent(new CustomEvent("hiq:netdev-open-screen", { detail: { screen: "chain" } }));
      } else if (r.kind === "cutover") {
        window.dispatchEvent(new CustomEvent("hiq:netdev-open-screen", { detail: { screen: "cutover" } }));
      } else if (r.kind === "screen") {
        window.dispatchEvent(new CustomEvent("hiq:netdev-open-screen", { detail: { screen: r.id } }));
      } else if (r.kind === "proposal") {
        window.dispatchEvent(new CustomEvent("hiq:netdev-open-screen", { detail: { tab: "proposals", filter: `id:${r.id}` } }));
      }
    };
    const offLive = onHiqDeepLink(r => setPendingDeepLink(r));
    if (!deepLinkDispatchRef.current) {
      deepLinkDispatchRef.current = true;
      app.NetDevConsumeDeepLink().then(r => { if (r) setPendingDeepLink(r); }).catch(() => {});
    }
    if (!pendingDeepLink || !netdevActive) return offLive;
    dispatch(pendingDeepLink);
    setPendingDeepLink(null);
    return offLive;
  }, [pendingDeepLink, netdevActive]);

  // 深链需要 netdev 面：不在就先切（切完上面的 effect 自然落地）。
  useEffect(() => {
    if (pendingDeepLink && !netdevActive) void switchProfile("netdev");
  }, [pendingDeepLink, netdevActive, switchProfile]);

  // Startup and workspace/model rebuilds create a fresh controller in normal
  // mode. Re-apply the UI mode once the controller is ready, including the case
  // where the user picked YOLO while boot was still loading and the legacy
  // SetBypass binding was a harmless no-op.
  useEffect(() => {
    if (!controllerReady) return;
    void setControllerCollaborationMode(controllerCollaborationMode({ collaborationMode, goal }));
    void setControllerToolApprovalMode(toolApprovalMode);
    if (goal.trim()) void setControllerGoal(goal);
  }, [collaborationMode, controllerReady, goal, setControllerCollaborationMode, setControllerGoal, setControllerToolApprovalMode, toolApprovalMode]);

  // The live task list pinned above the composer comes from the most recent
  // successful top-level todo_write result; failed or still-running attempts do
  // not advance the canonical panel state. It stays visible through the final
  // all-completed update, and can be dismissed by the user (the ✕). A dismissal
  // is keyed to that list's id, so a fresh accepted todo_write brings the panel
  // back.
  const todoEntry = useMemo(() => {
    for (let i = state.items.length - 1; i >= 0; i--) {
      const it = state.items[i];
      if (it.kind === "tool" && it.name === "todo_write" && !it.parentId && it.status === "done" && !it.error) {
        return { item: it, index: i };
      }
    }
    return null;
  }, [state.items]);
  const todoItem = todoEntry?.item ?? null;
  const todos = useMemo(() => (todoItem ? parseTodos(todoItem.args) : []), [todoItem]);
  const [dismissedTodo, setDismissedTodo] = useState<string | null>(null);
  const showTodos = shouldShowTodoPanel(todoItem?.id, dismissedTodo, todos);

  const sessionTitle = topicTitle(activeTab);
  const sessionHasContent = state.items.length > 0 || Boolean(state.live?.text || state.live?.reasoning);
  const getSessionMarkdown = useCallback(
    () => sessionItemsToMarkdown(sessionTitle, state.items, state.live),
    [sessionTitle, state.items, state.live],
  );
  const getSessionJson = useCallback(
    () => sessionItemsToJson(sessionTitle, state.items, state.live),
    [sessionTitle, state.items, state.live],
  );

  const exportSession = useCallback(
    async (format: "markdown" | "json" | "pdf" | "image" | "html") => {
      const base = safeFilename(sessionTitle);
      try {
        if (format === "json") {
          const path = await app.PickExportFile(`${base}.json`, "application/json");
          if (path) await app.SaveExportFile(path, getSessionJson(), false);
        } else if (format === "pdf") {
          const path = await app.PickExportFile(`${base}.pdf`, "application/pdf");
          if (!path) return;
          const blob = await renderSessionPdfBlob(getSessionMarkdown(), sessionTitle);
          await app.SaveExportFile(path, await blobToBase64(blob), true);
        } else if (format === "html") {
          const path = await app.PickExportFile(`${base}.html`, "text/html");
          if (!path) return;
          const html = await renderSessionHtml(getSessionMarkdown(), sessionTitle);
          await app.SaveExportFile(path, html, false);
        } else if (format === "image") {
          const path = await app.PickExportFile(`${base}.png`, "image/png");
          if (!path) return;
          const blob = await renderSessionImageBlob(getSessionMarkdown());
          await app.SaveExportFile(path, await blobToBase64(blob), true);
        } else {
          const path = await app.PickExportFile(`${base}.md`, "text/markdown");
          if (path) await app.SaveExportFile(path, getSessionMarkdown(), false);
        }
      } catch (err) {
        console.error("Failed to export session", err);
      }
    },
    [getSessionJson, getSessionMarkdown, sessionTitle],
  );

  // Drain the active tab's pending plan revision once it stops running. Keyed
  // per-tab so a revision drafted in tab A can't leak into tab B's send.
  useEffect(() => {
    if (!activeTabId || state.running) return;
    const text = pendingPlanRevisionsByTab[activeTabId];
    if (!text) return;
    setPendingPlanRevisionsByTab((current) => {
      if (!(activeTabId in current)) return current;
      const next = { ...current };
      delete next[activeTabId];
      return next;
    });
    send(text);
  }, [activeTabId, pendingPlanRevisionsByTab, send, state.running]);

  useEffect(() => {
    setClearContextPending(false);
  }, [activeTabId]);

  const cancelClearContext = useCallback(() => {
    setClearContextPending(false);
  }, []);

  const confirmClearContext = useCallback(async () => {
    setClearContextPending(false);
    try {
      await clearSession();
      notice(t("clearContext.done"));
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      notice(msg || t("clearContext.failed"), "warn");
    }
  }, [clearSession, notice, t]);

  // Keep runningRef in sync so handleSend sees the latest running value
  // even inside a stale closure.
  useEffect(() => {
    runningRef.current = state.running;
  }, [state.running]);

  // handleSend intercepts slash commands that need a desktop-native action before
  // they reach the backend: "/model <ref>" rebuilds on that model, "/memory"
  // opens Settings, and "/clear" shows an in-app confirmation card. Everything else — skills (/init, …),
  // custom commands, bare /model and the other read-only management verbs
  // (/skill, /hooks, /mcp) — goes straight to Submit, which the controller
  // resolves (a turn, or a listing Notice).
  const handleSend = useCallback(
    async (displayText: string, submitText = displayText) => {
      const trimmed = displayText.trim();
      // "!<cmd>" runs a shell command directly, bypassing the model.
      if (trimmed.startsWith("!")) {
        const cmd = trimmed.slice(1).trim();
        if (!cmd) {
          notice("usage: !<command>  (e.g. !ls -la)");
          return;
        }
        runShell(cmd);
        return;
      }
      const model = /^\/model\s+(\S+)$/.exec(trimmed);
      if (model) {
        void switchModel(model[1]);
        return;
      }
      if (trimmed === "/memory") {
        closeTransientOverlays();
        setSettingsTarget("memory");
        return;
      }
      if (trimmed === "/clear") {
        setClearContextPending(true);
        return;
      }
      const goalCommand = /^\/goal(?:\s+(.*))?$/.exec(trimmed);
      if (goalCommand) {
        const arg = (goalCommand[1] ?? "").trim();
        if (arg && !["status", "clear", "off", "stop", "done"].includes(arg.toLowerCase())) {
          applyGoal(arg);
        } else if (["clear", "off", "stop", "done"].includes(arg.toLowerCase())) {
          applyGoal("");
        }
        send(trimmed, submitText.trim());
        return;
      }
      if (collaborationMode === "goal" && !goal.trim()) {
        applyGoal(trimmed);
        send(trimmed, `/goal ${submitText.trim()}`);
        return;
      }
      const theme = /^\/theme(?:\s+(\S+))?$/.exec(trimmed);
      if (theme) {
        const arg = theme[1]?.toLowerCase();
        if (!arg) {
          const cur = getTheme();
          notice(t("settings.themeCurrent", { theme: cur, style: getThemeStyle(cur) }));
          return;
        }
        if (isThemeMode(arg)) {
          const next = arg;
          const style = getThemeStyle(next);
          await app.SetDesktopAppearance(next, style);
          applyTheme(next, style);
          notice(t("settings.themeChanged", { theme: next, style }));
          return;
        }
        if (isThemeStyle(arg)) {
          const cur = getTheme();
          await app.SetDesktopAppearance(cur, arg);
          applyTheme(cur, arg);
          notice(t("settings.themeChanged", { theme: cur, style: arg }));
          return;
        }
        notice(t("settings.themeUnknown", { name: arg }), "warn");
        return;
      }
      // Busy: queue as an independent NEXT turn (spec 2-4) — steering would
      // demote a fresh request to "guidance for the current task". Explicit
      // mid-turn guidance stays available via the Steer command.
      if (runningRef.current) { void app.FollowUpForTab(activeTabId ?? "", submitText.trim()); return; }
      await setControllerCollaborationMode(controllerCollaborationMode({ collaborationMode, goal }));
      await setControllerToolApprovalMode(toolApprovalMode);
      if (goal.trim()) await setControllerGoal(goal);
      // netdev guardrail: each user ask buys a fresh per-turn command budget
      // ([netdev.guardrails] turn_command_budget — a true per-ask control).
      if (netdevActive) void app.NetDevTurnBegin().catch(() => { /* budget reset is best-effort */ });
      send(trimmed, submitText.trim());
    },
    [applyGoal, closeTransientOverlays, collaborationMode, goal, send, runShell, notice, setControllerCollaborationMode, setControllerGoal, setControllerToolApprovalMode, steer, switchModel, t, toolApprovalMode],
  );
  retrySendRef.current = handleSend;

  const refreshTabMetas = useCallback(async (): Promise<TabMeta[]> => {
    const tabs = asArray(await app.ListTabs().catch(() => [] as TabMeta[]));

    setTabMetas(tabs);
    return tabs;
  }, []);

  const blankSessionTarget = useCallback(() => {
    // Global scope retired (2026-08-21): new sessions land on the active tab's
    // project; with no active project the backend routes to the profile home
    // project 工作台 (EnsureBlankTab normalizes empty roots).
    const activeWorkspaceRoot = activeTab?.workspaceRoot || "";
    return { scope: "project", workspaceRoot: activeWorkspaceRoot };
  }, [activeTab?.workspaceRoot]);

  const openBlankSession = useCallback(async (scope: string, workspaceRoot: string) => {
    window.dispatchEvent(new CustomEvent("cowork:reset-panel"));
    await ensureBlankTab(scope, scope === "project" ? workspaceRoot : "", profileRef.current);
    setProjectRevision((value) => value + 1);
    await refreshTabMetas();
    setTabRevealSignal((signal) => signal + 1);
  }, [ensureBlankTab, refreshTabMetas]);

  useEffect(() => {
    const offRemote = onRemoteStatus(() => { void refreshTabMetas(); });
    return () => offRemote();
  }, [refreshTabMetas]);

  useEffect(() => {
    void refreshTabMetas();
    const id = window.setInterval(() => void refreshTabMetas(), 2000);
    return () => window.clearInterval(id);
  }, [refreshTabMetas]);

  // Steer / follow-up queue strip (upgrade spec 2-3/2-4): polled while a turn
  // runs so the strip reflects both backend queues without new event kinds.
  const [queuedMsgs, setQueuedMsgs] = useState<{ steer?: string[]; followUp?: string[] }>({});
  const queuedActiveRef = useRef(false);
  queuedActiveRef.current = state.running;
  useEffect(() => {
    let cancelled = false;
    const poll = () => {
      app.QueuedMessages()
        .then((q) => { if (!cancelled) setQueuedMsgs(q ?? {}); })
        .catch(() => {});
    };
    poll();
    const id = window.setInterval(poll, 1500);
    return () => { cancelled = true; window.clearInterval(id); };
  }, []);
  useEffect(() => {
    if (!state.running) setQueuedMsgs({});
  }, [state.running]);
  const queueFollowUp = useCallback((text: string) => {
    void app.FollowUpForTab(activeTabId ?? "", text);
  }, [activeTabId]);

  // Agents overview (upgrade spec 2-7): Ctrl/Cmd+I opens the multi-tab
  // dashboard; metas are already polled every 2s by refreshTabMetas.
  const [agentsOpen, setAgentsOpen] = useState(false);
  useGlobalShortcut("agents.dashboard", () => setAgentsOpen((v) => !v));

  // Session-branch tree navigator (upgrade spec 4-3): Ctrl/Cmd+Shift+B.
  // After a switch the backend has swapped the session in place — pull the
  // fresh transcript via syncActiveTab (reset reload).
  const [branchesOpen, setBranchesOpen] = useState(false);
  useGlobalShortcut("branches.show", () => setBranchesOpen((v) => !v));

  // Main-provider rate-limit window for the usage chip (upgrade spec 0-8).
  // Polled: the budget drains as requests flow, not on events. rpm=0 (limiting
  // disabled) leaves the indicator hidden — fetch is cheap either way.
  const [budget, setBudget] = useState<BudgetStatusView | undefined>(undefined);
  useEffect(() => {
    let cancelled = false;
    const poll = () => {
      app.BudgetStatus()
        .then((b) => { if (!cancelled) setBudget(b); })
        .catch(() => {});
    };
    poll();
    const id = window.setInterval(poll, 5000);
    return () => { cancelled = true; window.clearInterval(id); };
  }, []);

  useEffect(() => {
    return onProjectTreeChanged(() => {
      setProjectRevision((value) => value + 1);
      void refreshTabMetas();
    });
  }, [refreshTabMetas]);

  // Refresh tab metas + sync active tab when cowork:reset-panel fires —
  // ExpertPanel dispatches it after RunExpertTeam (which opens an expert-session
  // tab and sets it active on the backend). Without this, the frontend's
  // activeTabId lags behind the backend, so the new expert tab appears in the
  // TabBar but isn't selected — the user still sees the old tab.
  // Non-destructive sync: this event also fires when SWITCHING chats
  // (handleOpenTopic), where the tab that is active at dispatch time may be
  // mid-stream — resetting it there would swallow its in-flight turn.
  useEffect(() => {
    const handler = () => {
      void refreshTabMetas();
      void syncActiveTab(false);
    };
    window.addEventListener("cowork:reset-panel", handler);
    return () => window.removeEventListener("cowork:reset-panel", handler);
  }, [refreshTabMetas, syncActiveTab]);

  // Sync coworkActive from the active tab's profile. Refetch on active-tab change
  // (each tab remembers its own profile) and on the profile:changed event (fired
  // after a SwitchProfile rebuild). The TabMeta.profile field is the per-tab
  // source of truth from refreshTabMetas; app.Profile() is the live backend read.
  useEffect(() => {
    const active = tabMetas.find((m) => m.id === activeTabId);
    if (active?.profile) {
      const p = active.profile.toLowerCase();
      const isCowork = p === "cowork";
      setCoworkActive(isCowork);
      setNetdevActive(p === "netdev");
      // Keep netdev as its own partition key: folding it to "dev" here would
      // file netdev topics into the dev session partition (P1 wiring bug).
      profileRef.current = p === "cowork" || p === "netdev" ? p : "dev";
      return;
    }
    // No tab meta yet (startup) — ask the backend directly.
    let cancelled = false;
    app
      .Profile()
      .then((name) => {
        const p = (name ?? "").toLowerCase();
        const isCowork = p === "cowork";
        if (!cancelled) {
          setCoworkActive(isCowork);
          setNetdevActive(p === "netdev");
          profileRef.current = p === "cowork" || p === "netdev" ? p : "dev";
        }
      })
      .catch(() => {
        /* dev mock / backend not ready */
      });
    return () => {
      cancelled = true;
    };
  }, [activeTabId, tabMetas]);

  useEffect(() => {
    return onProfileChanged((e) => {
      const isActive = e.tabId === activeTabId || !e.tabId;
      const p = e.profile.toLowerCase();
      const next: "dev" | "cowork" | "netdev" = p === "cowork" || p === "netdev" ? p : "dev";
      // Only adopt the change if it concerns the active tab — a background tab's
      // profile switch should not flip the visible layout.
      if (isActive) {
        setCoworkActive(next === "cowork");
        setNetdevActive(next === "netdev");
      }
      // Keep the profile getter (handed to useController) live so subsequent
      // OpenProjectTab/OpenGlobalTab calls scope to the new profile. All three
      // profiles map here — folding netdev to dev would file new topics into
      // the dev session partition (the P1 wiring bug the active-tab-sync
      // effect guards against; this event path must not reintroduce it).
      profileRef.current = next;
      // A profile switch rebuilds the active tab's controller, so the backend
      // re-emits agent:ready. The ready handler reloads with reset=false, which
      // leaves the previous profile's messages on screen when the new history is
      // empty. Explicitly reset+reload here so the chat list reflects the new
      // (possibly empty) session. Only the active tab's switch matters.
      if (isActive) void syncActiveTab(true);
      // Close any open History panel — its cached session list belongs to the
      // previous profile and would show the wrong conversations until reopened.
      setHistView(null);
      // Profiles are independent surfaces (user direction 2026-08-19): the
      // Loop modal and the coding preference panel belong to the coding
      // profile and must not linger into cowork/netdev.
      setLoopOpen(false);
      setPreferenceOpen(false);
      setTeamOpen(false);
      setKbOpen(false);
      setRagOpen(false);
    });
  }, [activeTabId, syncActiveTab]);

  // Profile-scoped dock layout: register the active profile with the storage
  // helper and swap the per-profile dock arrangement (widths/tabs/mode/preview
  // URL) at every mode boundary, so no profile inherits another's dock state.
  // Maximized is transient and resets at the boundary.
  useEffect(() => {
    setActiveStorageProfile(coworkActive ? "cowork" : netdevActive ? "netdev" : "dev");
    setRightDockTreeWidth(loadRightDockTreeWidth());
    setRightDockPreviewWidth(loadRightDockPreviewWidth());
    setDockTabs(loadDockTabs());
    setRightDockMode(loadRightDockMode());
    setPreviewUrl(loadPreviewUrl());
    setWorkspacePanelMaximized(false);
  }, [coworkActive, netdevActive]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const needs = await app.NeedsOnboarding();
        if (!cancelled) setNeedsOnboarding(needs);
      } catch {
        // Bridge unavailable (browser dev seam) — skip the gate; a real key
        // failure still surfaces via the topbar startupError banner.
        if (!cancelled) setNeedsOnboarding(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    const el = footerRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    let frame = 0;
    const update = () => {
      if (frame) window.cancelAnimationFrame(frame);
      frame = window.requestAnimationFrame(() => {
        frame = 0;
        const next = Math.round(el.getBoundingClientRect().height);
        if (Math.abs(footerHeightRef.current - next) < 2) return;
        footerHeightRef.current = next;
        setFooterHeight(next);
      });
    };
    update();
    const observer = new ResizeObserver(update);
    observer.observe(el);
    return () => {
      if (frame) window.cancelAnimationFrame(frame);
      observer.disconnect();
    };
  }, []);

  const toggleSidebar = useCallback(() => {
    closeTransientOverlays();
    pulseSidebarToggle();
    anchorAppScrollToChat();
    const nextCollapsed = !sidebarCollapsed;
    setSidebarCollapsed(nextCollapsed);
    saveSidebarCollapsed(nextCollapsed);
  }, [anchorAppScrollToChat, closeTransientOverlays, pulseSidebarToggle, sidebarCollapsed]);

  // Width the right dock currently reserves (0 when closed/maximized) — the
  // sidebar clamp subtracts it so the chat pane never drops below CHAT_MIN_WIDTH.
  const dockReserveWidth = useCallback(
    () => (workspacePanelOpen && !workspacePanelMaximized ? workspacePanelRenderWidth + WORKSPACE_RESIZER_WIDTH : 0),
    [workspacePanelOpen, workspacePanelMaximized, workspacePanelRenderWidth],
  );

  const setExpandedSidebarWidth = useCallback((width: number) => {
    closeTransientOverlays();
    const next = clampSidebarWidth(width, dockReserveWidth());
    setSidebarWidth(next);
    saveSidebarWidth(next);
  }, [closeTransientOverlays, dockReserveWidth]);

  const startSidebarResize = useCallback(
    (event: ReactPointerEvent<HTMLButtonElement>) => {
      if (sidebarCollapsed) return;
      event.preventDefault();
      closeTransientOverlays();
      setSidebarResizing(true);
      let nextWidth = sidebarWidth;
      let lastClientX = sidebarWidth;
      const dockWidth = dockReserveWidth();
      const onMove = (moveEvent: PointerEvent) => {
        lastClientX = moveEvent.clientX;
        nextWidth = clampSidebarWidth(moveEvent.clientX, dockWidth);
        setSidebarWidth(nextWidth);
      };
      const onDone = () => {
        // Drag-to-edge hide: releasing with the pointer near the left viewport
        // edge collapses the sidebar instead of pinning it at min width.
        if (lastClientX < SIDEBAR_COLLAPSE_THRESHOLD) {
          setSidebarCollapsed(true);
          saveSidebarCollapsed(true);
        } else {
          setSidebarWidth(nextWidth);
          saveSidebarWidth(nextWidth);
        }
        setSidebarResizing(false);
        window.removeEventListener("pointermove", onMove);
        window.removeEventListener("pointerup", onDone);
        window.removeEventListener("pointercancel", onDone);
        if (event.currentTarget && event.currentTarget.hasPointerCapture(event.pointerId)) {
          event.currentTarget.releasePointerCapture(event.pointerId);
        }
        document.body.style.cursor = "";
        document.body.style.userSelect = "";
      };
      document.body.style.cursor = "col-resize";
      document.body.style.userSelect = "none";
      event.currentTarget.setPointerCapture(event.pointerId);
      window.addEventListener("pointermove", onMove);
      window.addEventListener("pointerup", onDone);
      window.addEventListener("pointercancel", onDone);
    },
    [closeTransientOverlays, dockReserveWidth, sidebarCollapsed, sidebarWidth],
  );

  const resizeSidebarWithKeyboard = useCallback(
    (event: KeyboardEvent<HTMLButtonElement>) => {
      if (sidebarCollapsed) return;
      if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
        event.preventDefault();
        setExpandedSidebarWidth(sidebarWidth + (event.key === "ArrowRight" ? 16 : -16));
      } else if (event.key === "Home") {
        event.preventDefault();
        setExpandedSidebarWidth(SIDEBAR_MIN_WIDTH);
      } else if (event.key === "End") {
        event.preventDefault();
        setExpandedSidebarWidth(SIDEBAR_MAX_WIDTH);
      }
    },
    [setExpandedSidebarWidth, sidebarCollapsed, sidebarWidth],
  );

  const setSavedWorkspacePanelWidth = useCallback(
    (width: number) => {
      closeTransientOverlays();
      if (rightDockDetailActive) {
        const next = clampRightDockPreviewWidth(width);
        setRightDockPreviewWidth(next);
        saveRightDockPreviewWidth(next);
        return;
      }
      const next = clampRightDockTreeWidth(width);
      setRightDockTreeWidth(next);
      saveRightDockTreeWidth(next);
    },
    [closeTransientOverlays, rightDockDetailActive],
  );

  const ensureWorkspacePanelWidth = useCallback(
    (width: number) => {
      closeTransientOverlays();
      const next = clampRightDockPreviewWidth(width);
      setRightDockPreviewWidth(next);
      saveRightDockPreviewWidth(next);
    },
    [closeTransientOverlays],
  );

  const startWorkspacePanelResize = useCallback(
    (event: ReactPointerEvent<HTMLButtonElement>) => {
      // Cowork mode tracks its own dock open state (coworkDockOpen) separately
      // from the coding-mode workspacePanelOpen — accept either so the resizer
      // works in both layouts (previously cowork's resizer was dead because
      // workspacePanelOpen is always false in cowork). Netdev mirrors cowork
      // (netdevDockOpen).
      const dockOpen = coworkActive ? coworkDockOpen : netdevActive ? netdevDockOpen : workspacePanelOpen;
      if (!dockOpen) return;
      event.preventDefault();
      closeTransientOverlays();
      setWorkspacePanelResizing(true);
      const startX = event.clientX;
      const startDockWidth = workspacePanelRenderWidth;
      let nextDockWidth = startDockWidth;
      let rawDockWidth = startDockWidth;
      const onMove = (moveEvent: PointerEvent) => {
        const delta = moveEvent.clientX - startX;
        rawDockWidth = startDockWidth - delta;
        nextDockWidth = rightDockDetailActive
          ? clampRightDockPreviewWidth(rawDockWidth)
          : clampRightDockTreeWidth(rawDockWidth);
        if (rightDockDetailActive) {
          setRightDockPreviewWidth(nextDockWidth);
        } else {
          setRightDockTreeWidth(nextDockWidth);
        }
      };
      const onDone = () => {
        // Drag-to-edge hide: releasing with the dock dragged below the close
        // threshold closes it instead of pinning at min width.
        if (rawDockWidth < DOCK_CLOSE_THRESHOLD) {
          if (coworkActive) {
            setCoworkDockOpen(false);
          } else if (netdevActive) {
            setNetdevDockOpen(false);
          } else {
            setWorkspacePanelMaximized(false);
            setWorkspacePanelOpen(false);
          }
        } else {
          setSavedWorkspacePanelWidth(nextDockWidth);
        }
        setWorkspacePanelResizing(false);
        window.removeEventListener("pointermove", onMove);
        window.removeEventListener("pointerup", onDone);
        window.removeEventListener("pointercancel", onDone);
        if (event.currentTarget && event.currentTarget.hasPointerCapture(event.pointerId)) {
          event.currentTarget.releasePointerCapture(event.pointerId);
        }
        document.body.style.cursor = "";
        document.body.style.userSelect = "";
      };
      document.body.style.cursor = "col-resize";
      document.body.style.userSelect = "none";
      event.currentTarget.setPointerCapture(event.pointerId);
      window.addEventListener("pointermove", onMove);
      window.addEventListener("pointerup", onDone);
      window.addEventListener("pointercancel", onDone);
    },
    [closeTransientOverlays, coworkActive, coworkDockOpen, netdevActive, netdevDockOpen, rightDockDetailActive, setSavedWorkspacePanelWidth, workspacePanelOpen, workspacePanelRenderWidth],
  );

  const resizeWorkspacePanelWithKeyboard = useCallback(
    (event: KeyboardEvent<HTMLButtonElement>) => {
      if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
        event.preventDefault();
        setSavedWorkspacePanelWidth(workspacePanelRenderWidth + (event.key === "ArrowLeft" ? 16 : -16));
      } else if (event.key === "Home") {
        event.preventDefault();
        setSavedWorkspacePanelWidth(rightDockDetailActive ? RIGHT_DOCK_PREVIEW_MIN_WIDTH : RIGHT_DOCK_TREE_MIN_WIDTH);
      } else if (event.key === "End") {
        event.preventDefault();
        setSavedWorkspacePanelWidth(rightDockDetailActive ? RIGHT_DOCK_MAX_WIDTH : RIGHT_DOCK_TREE_MAX_WIDTH);
      }
    },
    [rightDockDetailActive, setSavedWorkspacePanelWidth, workspacePanelRenderWidth],
  );

  const openWorkspacePanel = useCallback(
    (mode: RightDockMode = rightDockMode) => {
      closeTransientOverlays();
      if (mode !== rightDockMode) {
        setWorkspacePreviewActive(false);
      }
      setRightDockMode(mode);
      // Keep the docked views docked; the rendered dock width is clamped to
      // the viewport so opening it reflows instead of forcing maximize.
      setWorkspacePanelMaximized(false);
      if (workspacePanelOpen && !workspacePanelMaximized) {
        return;
      }
      setWorkspacePanelOpen(true);
    },
    [closeTransientOverlays, rightDockMode, workspacePanelMaximized, workspacePanelOpen],
  );

  const closeWorkspacePanel = useCallback(() => {
    closeTransientOverlays();
    // Close only the ACTIVE surface's dock — the three profiles are
    // independent, so closing the ops dock must not also close the office
    // dock (or vice versa) the user may have open in the other view.
    if (netdevActive) {
      setNetdevDockOpen(false);
      return;
    }
    if (coworkActive) {
      // Mirror anti-nag (§3.3 pattern): a manual close mid-browsing-run
      // suppresses the dock's auto-reopen until the next run/session starts.
      if (browserMirrorSnapshot().running) {
        browserMirrorSuppressedRef.current = true;
      }
      setCoworkDockOpen(false);
      return;
    }
    if (!workspacePanelOpen) {
      return;
    }
    // Pane-system §3.3: a manual close suppresses auto-reopening for THIS url —
    // a NEW dev-server url still triggers the preview tab.
    previewSuppressedRef.current = previewUrl;
    setWorkspacePanelMaximized(false);
    setWorkspacePanelOpen(false);
  }, [closeTransientOverlays, coworkActive, netdevActive, previewUrl, workspacePanelOpen]);
  closeWorkspacePanelRef.current = closeWorkspacePanel;

  const toggleWorkspacePanel = useCallback(() => {
    pulseWorkspaceToggle();
    // Cowork and netdev each track their own dock open state, not the shared
    // coding-mode workspacePanelOpen — see coworkDockOpen comment above.
    if (coworkActive) {
      setCoworkDockOpen((open) => !open);
      return;
    }
    if (netdevActive) {
      setNetdevDockOpen((open) => !open);
      return;
    }
    if (workspacePanelRenderable) {
      closeWorkspacePanel();
      return;
    }
    openWorkspacePanel("files");
  }, [closeWorkspacePanel, coworkActive, netdevActive, openWorkspacePanel, pulseWorkspaceToggle, workspacePanelRenderable]);

  const openRightDockMode = useCallback(
    (mode: RightDockMode) => {
      ensureDockTab(mode);
      openWorkspacePanel(mode);
    },
    [ensureDockTab, openWorkspacePanel],
  );

  const handleWorkspacePreviewModeChange = useCallback(
    (active: boolean) => {
      if (workspacePreviewActive === active) return;
      closeTransientOverlays();
      setWorkspacePreviewActive(active);
    },
    [closeTransientOverlays, workspacePreviewActive],
  );

  const layoutStyle = useMemo(
    () =>
      ({
        "--sidebar-expanded-width": `${sidebarWidth}px`,
        "--workspace-width": `${Math.max(workspacePanelRenderWidth, netdevActive ? NETDEV_DOCK_MIN_WIDTH : 0)}px`,
        "--workspace-resizer-width": `${WORKSPACE_RESIZER_WIDTH}px`,
      }) as CSSProperties,
    [sidebarWidth, workspacePanelRenderWidth, netdevActive],
  );

  const addWorkspaceTextToComposer = useCallback((text: string) => {
    setComposerInsertRequest({ id: Date.now(), text });
  }, []);

  useEffect(() => {
    const handleCoworkInsert = (e: Event) => {
      const text = (e as CustomEvent<string>).detail;
      if (text) addWorkspaceTextToComposer(text);
    };
    window.addEventListener("cowork:insert-text", handleCoworkInsert as EventListener);
    return () => window.removeEventListener("cowork:insert-text", handleCoworkInsert as EventListener);
  }, [addWorkspaceTextToComposer]);

  const handleTabChange = useCallback(async (id: string) => {
    closeTransientOverlays();
    await switchTab(id);
    await refreshTabMetas();
    setTabRevealSignal((signal) => signal + 1);
  }, [closeTransientOverlays, refreshTabMetas, switchTab]);

  const handleTabClose = useCallback(async (id: string) => {
    closeTransientOverlays();
    // Purge all per-tab mode maps so a closed tab leaves no residue. The
    // tabMetas sync effect (App.tsx ~L1031) would eventually reconcile, but
    // deleting up-front avoids stale state window during async refresh.
    const dropKey = <T extends Record<string, unknown>>(current: T): T => {
      if (!(id in current)) return current;
      const next = { ...current };
      delete next[id];
      return next;
    };
    setModesByTab(dropKey);
    setCollaborationModesByTab(dropKey);
    setToolApprovalModesByTab(dropKey);
    setGoalsByTab(dropKey);
    setGoalDraftModesByTab(dropKey);
    setPendingPlanRevisionsByTab(dropKey);
    delete yoloRestoreToolApprovalModesRef.current[id];
    setTabMetas((current) => {
      if (current.length <= 1) return current;
      const closingIndex = current.findIndex((tab) => tab.id === id);
      if (closingIndex < 0) return current;
      const closingTab = current[closingIndex];
      const remaining = current.filter((tab) => tab.id !== id);
      if (!closingTab.active && closingTab.id !== activeTabId) return remaining;
      const nextIndex = Math.min(closingIndex, remaining.length - 1);
      const nextActiveId = remaining[nextIndex]?.id;
      return remaining.map((tab) => ({ ...tab, active: tab.id === nextActiveId }));
    });
    await closeTab(id);
    await refreshTabMetas();
    setTabRevealSignal((signal) => signal + 1);
  }, [activeTabId, closeTab, closeTransientOverlays, refreshTabMetas, yoloRestoreToolApprovalModesRef]);

  const handleTabsClose = useCallback(async (ids: string[], nextActiveTabId?: string) => {
    closeTransientOverlays();
    const currentIds = tabMetas.map((tab) => tab.id);
    const targets = ids.filter((id, index) => currentIds.includes(id) && ids.indexOf(id) === index);
    if (targets.length === 0) return;
    for (const id of targets) {
      await closeTab(id);
    }
    if (nextActiveTabId && currentIds.includes(nextActiveTabId)) {
      await switchTab(nextActiveTabId);
    }
    await refreshTabMetas();
    setTabRevealSignal((signal) => signal + 1);
  }, [closeTab, closeTransientOverlays, refreshTabMetas, switchTab, tabMetas]);

  const handleTabsReorder = useCallback(async (ids: string[]) => {
    setTabOrderIds(ids);
    setTabMetas((current) => {
      const byId = new Map(current.map((tab) => [tab.id, tab]));
      const ordered = ids.map((id) => byId.get(id)).filter((tab): tab is TabMeta => Boolean(tab));
      return ordered.length === current.length ? ordered : current;
    });
    await reorderTabs(ids);
    await refreshTabMetas();
    setTabRevealSignal((signal) => signal + 1);
  }, [refreshTabMetas, reorderTabs]);

  const handleNewTab = useCallback(async () => {
    closeTransientOverlays();
    setSidebarImDetailConnectionId("");
    const target = blankSessionTarget();
    await openBlankSession(target.scope, target.workspaceRoot);
  }, [blankSessionTarget, closeTransientOverlays, openBlankSession]);

  const handleMessageAction = useCallback(async (turn: number, scope: string) => {
    await rewind(turn, scope);
    if (scope === "fork") {
      await refreshTabMetas();
      setProjectRevision((value) => value + 1);
      setTabRevealSignal((signal) => signal + 1);
      return;
    }
    if (scope === "code" || scope === "both") {
      setDockRefreshKey((value) => value + 1);
      setProjectRevision((value) => value + 1);
    }
  }, [refreshTabMetas, rewind]);

  // runExpertFromSession triggers a new collaboration run on the team whose
  // expert-session tab is active. RunExpertTeam opens/activates the expert tab
  // and streams live; the result appends to the same session (multi-turn).
  // Search-cost confirmation is tracked per-team (a Set in a ref) so the user
  // is warned ONCE per team per session — not nagged on every follow-up.
  const searchCostConfirmedRef = useRef<Set<string>>(new Set());
  const runExpertFromSession = useCallback(async (teamId: string, task: string, mode: string, rounds: number) => {
    const teams = await app.ListExpertTeams().catch(() => []);
    const team = teams.find((tm) => tm.id === teamId);
    if (team?.allowSearch && !searchCostConfirmedRef.current.has(teamId)) {
      if (!(await confirm({ title: t("cowork.expertSearchBadge"), message: t("cowork.expertSearchCostConfirm"), danger: false }))) {
        throw new Error("cancelled"); // ExpertSessionView resets liveRunning on reject
      }
      searchCostConfirmedRef.current.add(teamId);
    }
    await app.RunExpertTeam(teamId, task, mode, rounds);
  }, [t]);

  const handleOpenTopic = useCallback(async (
    scope: string,
    workspaceRoot: string,
    topicId: string,
    remote?: { kind: string; target: string; user?: string; tls?: boolean } | null,
    sessionPath?: string,
    title?: string,
  ) => {
    window.dispatchEvent(new CustomEvent("cowork:reset-panel"));
    closeTransientOverlays();
    setSidebarImDetailConnectionId("");
    try {
      if (remote) {
        await app.OpenRemoteTopicTab(remote.kind, remote.target, remote.user ?? "", Boolean(remote.tls), workspaceRoot, topicId, title ?? "", sessionPath ?? "");
      } else if (scope === "global") {
        await openGlobalTab(topicId);
      } else {
        await openProjectTab(workspaceRoot, topicId);
      }
    } catch (err) {
      // A cross-profile topicId (stale sidebar briefly showing the previous
      // profile's tree right after a switch) is rejected by the backend with a
      // "does not belong to the X profile" error. Refresh the sidebar so the
      // stale entry is gone, and surface a localized notice instead of the raw
      // English backend message.
      const msg = String(err);
      if (msg.includes("does not belong")) {
        setProjectRevision((v) => v + 1);
        await refreshTabMetas();
        showToast(t("cowork.topicNotInProfile"), "warn");
        return;
      }
      throw err;
    }
    await refreshTabMetas();
    setTabRevealSignal((signal) => signal + 1);
  }, [closeTransientOverlays, openGlobalTab, openProjectTab, refreshTabMetas, showToast, t]);

  const openSidebarImConnectionSession = useCallback(async (connection: SidebarImConnection) => {
    const target = mappedSessionTarget(connection.sessionId);
    if (!target) {
      showToast(t("sidebar.imWaiting", { name: connection.title }));
      return;
    }
    setSidebarImDetailConnectionId("");
    try {
      if (target.kind === "path") {
        const tab = await ensureBlankTab(connection.scope, connection.scope === "project" ? connection.workspaceRoot : "", profileRef.current);
        await resumeSession(target.value, tab.id);
      } else if (connection.scope === "project") {
        await openProjectTab(connection.workspaceRoot, target.value);
      } else {
        await openGlobalTab(target.value);
      }
      await refreshTabMetas();
      setProjectRevision((value) => value + 1);
      setTabRevealSignal((signal) => signal + 1);
    } catch (err) {
      console.warn("bot sidebar open failed", err);
      showToast(t("sidebar.imOpenFailed", { name: connection.title }));
    }
  }, [ensureBlankTab, openGlobalTab, openProjectTab, refreshTabMetas, resumeSession, showToast, t]);

  const openSidebarImSessionPath = useCallback(async (path: string) => {
    const conn = sidebarImDetailConnection;
    if (!conn) return;
    setSidebarImDetailConnectionId("");
    try {
      const tab = await ensureBlankTab(conn.scope, conn.scope === "project" ? conn.workspaceRoot : "", profileRef.current);
      await resumeSession(path, tab.id);
      await refreshTabMetas();
      setProjectRevision((value) => value + 1);
      setTabRevealSignal((signal) => signal + 1);
    } catch (err) {
      console.warn("bot sidebar open path failed", err);
      showToast(t("sidebar.imOpenFailed", { name: conn.title }));
    }
  }, [sidebarImDetailConnection, ensureBlankTab, refreshTabMetas, resumeSession, showToast, t]);

  const showSidebarImDetail = useCallback(() => {
    closeTransientOverlays();
    if (sidebarImConnections.length === 0) {
      openBotSettings();
      return;
    }
    quietlyRefreshSidebarImConnections();
    setSidebarImDetailConnectionId(sidebarImConnections[0].id);
  }, [closeTransientOverlays, openBotSettings, quietlyRefreshSidebarImConnections, sidebarImConnections]);

  // History drawer: project menus can open a scoped saved-session list. Idle row
  // clicks resume; running row clicks only preview through PreviewSession.
  const openProjectHistory = useCallback(async (scope: "global" | "project", workspaceRoot: string) => {
    closeTransientOverlays();
    const filter = { scope, workspaceRoot };
    setHistView({ kind: "history", source: "scope", filter, sessions: sessionsForScope(await listSessions(), filter) });
  }, [closeTransientOverlays, listSessions]);
  const openAllHistory = useCallback(async () => {
    closeTransientOverlays();
    setHistView({ kind: "history", source: "all", sessions: await listSessions() });
  }, [closeTransientOverlays, listSessions]);
  const openTrash = useCallback(async () => {
    closeTransientOverlays();
    setHistView({ kind: "trash", sessions: await listTrashedSessions() });
  }, [closeTransientOverlays, listTrashedSessions]);
  const closeHistory = useCallback(() => {
    closeTransientOverlays();
    setHistView(null);
  }, [closeTransientOverlays]);

  const onResumeSession = useCallback(
    async (session: SessionMeta) => {
      window.dispatchEvent(new CustomEvent("cowork:reset-panel"));
      if (state.running) return;
      const scope = session.scope || (session.workspaceRoot ? "project" : "global");
      try {
        let targetTab: TabMeta;
        if (scope === "project" && session.workspaceRoot && session.topicId) {
          targetTab = await openProjectTab(session.workspaceRoot, session.topicId);
        } else if (scope === "global" && session.topicId) {
          targetTab = await openGlobalTab(session.topicId);
        } else {
          throw new Error(scope === "global" && !session.topicId
            ? t("history.failedOpenSession")
            : (session.topicId ? "Missing workspaceRoot" : t("history.failedOpenSession")));
        }
        setHistView(null);
        await resumeSession(session.path, targetTab.id);
        await refreshTabMetas();
        setTabRevealSignal((signal) => signal + 1);
      } catch (err: any) {
        setHistView(null);
        if (scope === "project" && session.workspaceRoot) {
          const name = workspaceDisplayName(session.workspaceRoot);
          showToast(t("history.failedOpenProject", { name, path: session.workspaceRoot }));
        } else {
          showToast(err?.message || String(err));
        }
      }
    },
    [openGlobalTab, openProjectTab, refreshTabMetas, state.running, resumeSession, t, showToast],
  );

  // Command palette: ⌘K / Ctrl+K opens a fuzzy navigator over commands and
  // recent sessions. Sessions are snapshotted on open so the list is stable
  // while the palette is up.
  const openPalette = useCallback(async () => {
    closeTransientOverlays();
    setPaletteOpen(true);
    setPaletteSessions(await listSessions().catch(() => []));
    setPaletteCapabilities(await app.Capabilities().catch(() => null));
  }, [closeTransientOverlays, listSessions]);
  // Global keyboard shortcuts — all wired through the centralized
  // keyboardShortcuts registry so they're self-documenting (Shift+? cheatsheet)
  // and share platform-aware matching, editable-target filtering, and a single
  // capture-phase listener per action. Replaces the old hand-rolled
  // ShellHotkeys / TextSizeHotkeys / CommandPalette Ctrl+K listeners.
  useGlobalShortcut("commandPalette.open", () => { if (!paletteOpen) void openPalette(); }, [paletteOpen, openPalette]);
  useGlobalShortcut("shortcuts.show", () => setShortcutsOpen(true));
  useGlobalShortcut("settings.open", () => setSettingsTarget("general"));
  useGlobalShortcut("app.newSession", () => { void handleNewTab(); });
  const shellExpand = useShellExpand();
  useGlobalShortcut("shell.toggle", () => shellExpand?.toggleLast(), [shellExpand]);
  useGlobalShortcut("textSize.increase", () => applyTextSize(nextTextSize(getTextSize(), 1)));
  useGlobalShortcut("textSize.decrease", () => applyTextSize(nextTextSize(getTextSize(), -1)));
  useGlobalShortcut("textSize.reset", () => applyTextSize(DEFAULT_TEXT_SIZE));
  // Pause/Resume the in-flight turn (graceful — finishes current step, freezes
  // with state preserved). Only fires while a turn is running; pauseToggle is a
  // no-op otherwise. Bound after pauseToggle is defined so the latest callback
  // is used.
  useGlobalShortcut("turn.pauseToggle", () => pauseToggle(), [pauseToggle]);
  const paletteItems = useMemo<PaletteItem[]>(() => {
    const cmds: PaletteItem[] = [
      { id: "cmd-new", group: t("palette.group.commands"), title: t("palette.cmd.newSession"), icon: <SquarePen size={15} />, compact: true, keywords: ["new", "新建"], run: () => void handleNewTab() },
      { id: "cmd-remote-connect", group: t("palette.group.commands"), title: t("remote.trigger"), icon: <Server size={15} />, compact: true, keywords: ["remote", "ssh", "wsl", "docker", "远程"], run: () => setRemoteWizardOpen(true) },
      ...(netdevActive ? [{ id: "cmd-netdev-logs", group: t("palette.group.commands"), title: "日志工作台（多源合并时间线）", icon: <FileText size={15} />, compact: true, keywords: ["logs", "日志", "时间线", "ioc"], run: () => { window.dispatchEvent(new CustomEvent("hiq:netdev-bench", { detail: "logs" })); } }] : []),
      ...(netdevActive ? ([
        { id: "cmd-ndv-dash-overview", title: "大屏·总览（值守态势）", keywords: ["dash", "overview", "大屏", "总览"] },
        { id: "cmd-ndv-dash-chain", title: "大屏·调查链（证据链路）", keywords: ["dash", "chain", "调查链", "证据"] },
        { id: "cmd-ndv-dash-cutover", title: "大屏·割接（变更窗口）", keywords: ["dash", "cutover", "割接", "变更"] },
        { id: "cmd-ndv-dash-discovery", title: "大屏·发现（资产纳管）", keywords: ["dash", "discovery", "发现", "纳管"] },
        { id: "cmd-ndv-dash-exposure", title: "大屏·暴露面（推演）", keywords: ["dash", "exposure", "暴露面", "推演"] },
      ] as const).map(x => ({ id: x.id, group: t("palette.group.commands"), title: x.title, icon: <FileText size={15} />, compact: true, keywords: [...x.keywords], run: () => { window.dispatchEvent(new CustomEvent("hiq:netdev-open-screen", { detail: { screen: x.id.replace("cmd-ndv-dash-", "") } })); } })) : []),
      { id: "cmd-history", group: t("palette.group.commands"), title: t("palette.cmd.history"), icon: <History size={15} />, compact: true, keywords: ["history", "历史"], run: () => void openAllHistory() },
      { id: "cmd-trash", group: t("palette.group.commands"), title: t("palette.cmd.trash"), icon: <Trash2 size={15} />, compact: true, keywords: ["trash", "回收站"], run: () => void openTrash() },
      { id: "cmd-settings", group: t("palette.group.commands"), title: t("palette.cmd.settings"), icon: <SettingsIcon size={15} />, compact: true, keywords: ["settings", "设置"], run: () => setSettingsTarget("general") },
      { id: "cmd-appearance", group: t("palette.group.commands"), title: t("palette.cmd.appearance"), icon: <Palette size={15} />, compact: true, keywords: ["theme", "appearance", "外观", "主题"], run: () => setSettingsTarget("appearance") },
      { id: "cmd-memory", group: t("palette.group.commands"), title: t("palette.cmd.memory"), icon: <Brain size={15} />, compact: true, keywords: ["memory", "记忆"], run: () => setSettingsTarget("memory") },
      { id: "cmd-models", group: t("palette.group.commands"), title: t("palette.cmd.models"), icon: <Cpu size={15} />, compact: true, keywords: ["model", "模型"], run: () => setSettingsTarget("models") },
    ];
    const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
    const dayLabel = (ms: number) => {
      const days = Math.round((startOfDay(new Date()) - startOfDay(new Date(ms))) / 86_400_000);
      if (days <= 0) return t("history.today");
      if (days === 1) return t("history.yesterday");
      return new Date(ms).toLocaleDateString();
    };
    const sessionItems: PaletteItem[] = paletteSessions.slice(0, 12).map((s) => ({
      id: `sess-${s.path}`,
      group: t("palette.group.sessions"),
      title: s.title?.trim() || s.preview || t("history.emptySession"),
      hint: s.workspaceRoot || undefined,
      meta: dayLabel(sessionActivityTime(s)),
      badge: t(s.turns === 1 ? "history.turnOne" : "history.turnOther", { n: s.turns }),
      run: () => void onResumeSession(s),
    }));

    const skillItems: PaletteItem[] = (paletteCapabilities?.skills || []).map((sk) => ({
      id: `skill-${sk.name}`,
      group: t("settings.tab.skills"),
      title: sk.name,
      hint: sk.description,
      icon: <Box size={15} />,
      keywords: [sk.name, sk.runAs, sk.scope],
      run: () => {
        setSettingsTarget("skills");
        setSettingsPayload(sk.name);
      },
    }));

    const mcpItems: PaletteItem[] = (paletteCapabilities?.servers || []).map((srv) => ({
      id: `mcp-${srv.name}`,
      group: t("settings.tab.mcp"),
      title: srv.name,
      hint: srv.command,
      icon: <Server size={15} />,
      keywords: [srv.name, srv.transport],
      run: () => {
        setSettingsTarget("mcp");
        setSettingsPayload(srv.name);
      },
    }));

    return [...cmds, ...skillItems, ...mcpItems, ...sessionItems];
  }, [t, paletteSessions, paletteCapabilities, handleNewTab, openAllHistory, openTrash, onResumeSession]);
  // Delete / rename act on disk, then re-fetch so the panel reflects the change.
  const onDeleteSession = useCallback(
    async (path: string) => {
      if (state.running) return;
      await deleteSession(path);
      const sessions = await listSessions();
      setHistView((cur) =>
        cur === null
          ? null
          : cur.kind === "history"
            ? { ...cur, sessions: cur.source === "scope" ? sessionsForScope(sessions, cur.filter) : sessions }
            : cur,
      );
    },
    [state.running, deleteSession, listSessions],
  );
  const onRenameSession = useCallback(
    async (path: string, title: string) => {
      if (state.running) return;
      await renameSession(path, title);
      const sessions = await listSessions();
      setHistView((cur) =>
        cur === null
          ? null
          : cur.kind === "history"
            ? { ...cur, sessions: cur.source === "scope" ? sessionsForScope(sessions, cur.filter) : sessions }
            : cur,
      );
    },
    [state.running, renameSession, listSessions],
  );
  const onRestoreTrashedSession = useCallback(
    async (path: string) => {
      await restoreSession(path);
      const trashed = await listTrashedSessions();
      setHistView((cur) => (cur === null ? null : { kind: "trash", sessions: trashed }));
    },
    [restoreSession, listTrashedSessions],
  );
  const onPurgeTrashedSession = useCallback(
    async (path: string) => {
      await purgeTrashedSession(path);
      const trashed = await listTrashedSessions();
      setHistView((cur) => (cur === null ? null : { kind: "trash", sessions: trashed }));
    },
    [purgeTrashedSession, listTrashedSessions],
  );
  const onPurgeAllTrashedSessions = useCallback(
    async (paths: string[]) => {
      const uniquePaths = Array.from(new Set(paths));
      for (const path of uniquePaths) {
        await purgeTrashedSession(path);
      }
      const trashed = await listTrashedSessions();
      setHistView((cur) => (cur === null ? null : { kind: "trash", sessions: trashed }));
    },
    [purgeTrashedSession, listTrashedSessions],
  );

  // Workspace: open the folder chooser and switch projects. The hook resets the
  // transcript and refreshes meta on a pick. A cancel is a no-op.

  // Same-profile open: the office/netdev pill lists only that profile's own
  // projects (pillProfile catalog above); picking one opens it IN PLACE —
  // openBlankSession files the topic under the active tab's profile. No
  // cross-profile funnel (08-21 strict isolation rule).
  const openProfileProject = useCallback((root: string) => {
    void openBlankSession("project", root);
  }, [openBlankSession]);


  const switchFolder = useCallback(async (path?: string) => {
    let picked = path === undefined ? await pickWorkspace() : await switchWorkspace(path);
    if (picked && /^\\wsl($|\.localhost)\\/i.test(picked)) {
      // WSL UNC path: the agent would run Windows-side against a slow 9P
      // share. Offer the remote connection instead (parity with ZCode's
      // wsl-unc prompt).
      const rest = picked.replace(/^\\wsl(\.localhost)?\\/i, "");
      const [distro, ...segs] = rest.split("\\");
      const linuxRoot = "/" + segs.filter(Boolean).join("/");
      const useRemote = window.confirm(
        `${t("remote.uncPromptTitle")}
${t("remote.uncPromptBody", { path: picked })}

[1] ${t("remote.uncPromptOpen")}
[2] ${t("remote.uncPromptContinue")}`
      );
      if (useRemote && distro && linuxRoot !== "/") {
        await app.OpenRemoteTab("wsl", distro, "", linuxRoot, "").catch(() => null);
        setProjectRevision((value) => value + 1);
        await refreshTabMetas();
        return "";
      }
    }
    if (picked) {
      setProjectRevision((value) => value + 1);
      await refreshTabMetas();
    }
    return picked;
  }, [pickWorkspace, switchWorkspace, refreshTabMetas]);

  const refreshProjectsAndTabs = useCallback(async () => {
    setProjectRevision((value) => value + 1);
    const tabs = await refreshTabMetas();
    if (activeTabId && !tabs.some((tab) => tab.id === activeTabId)) {
      // The active tab vanished (topic deleted / closed server-side); the
      // backend promoted another tab. Reconcile it — the promoted tab may be
      // mid-stream with valid in-memory state that a reset would swallow.
      await syncActiveTab(false);
    }
  }, [activeTabId, refreshTabMetas, syncActiveTab]);

  const renameTopic = useCallback(async (topicId: string, title: string) => {
    const nextTitle = title.trim();
    if (!topicId || !nextTitle) return;
    await app.RenameTopic(topicId, nextTitle);
    await refreshProjectsAndTabs();
  }, [refreshProjectsAndTabs]);

  const startActiveTopicRename = useCallback(() => {
    if (!activeTab?.topicId) return;
    topicRenameSkipCommitRef.current = false;
    topicRenameCommitHandledRef.current = false;
    setRenamingTopicId(activeTab.topicId);
    setTopicTitleDraft(activeTab.topicTitle || "");
  }, [activeTab?.topicId, activeTab?.topicTitle]);

  const cancelActiveTopicRename = useCallback(() => {
    topicRenameSkipCommitRef.current = true;
    topicRenameCommitHandledRef.current = true;
    setRenamingTopicId(null);
    setTopicTitleDraft("");
  }, []);

  const commitActiveTopicRename = useCallback(async () => {
    if (topicRenameSkipCommitRef.current) {
      topicRenameSkipCommitRef.current = false;
      topicRenameCommitHandledRef.current = false;
      setRenamingTopicId(null);
      return;
    }
    if (topicRenameCommitHandledRef.current) return;
    topicRenameCommitHandledRef.current = true;
    const topicId = renamingTopicId;
    setRenamingTopicId(null);
    if (!topicId) return;
    const nextTitle = topicTitleDraft.trim();
    if (!nextTitle) return;
    try {
      await renameTopic(topicId, nextTitle);
    } catch {
      /* keep the app usable if a stale topic cannot be renamed */
    }
  }, [renameTopic, renamingTopicId, topicTitleDraft]);

  const sidebarExpandBlocked = false;
  const sidebarToggleTitle = sidebarCollapsed
      ? t("sidebar.expand")
      : t("sidebar.collapse");

  const browserPreviewChrome = typeof window !== "undefined" && !window.runtime;
  const workspacePanelResetWidth = rightDockDetailActive
    ? RIGHT_DOCK_PREVIEW_DEFAULT_WIDTH
    : defaultRightDockTreeWidth();
  const workspacePanelResizeMinWidth = workspacePanelAriaMinWidth(workspacePanelMinWidth, workspacePanelRenderWidth);
  const workspacePanelMaxWidth = rightDockDetailActive ? RIGHT_DOCK_MAX_WIDTH : RIGHT_DOCK_TREE_MAX_WIDTH;
  const topicbarTitle = sidebarImDetailConnection ? t("botDetail.title", { name: sidebarImDetailConnection.title }) : topicDisplayTitle(activeTab);
  const topicbarWorkspaceLabel = sidebarImDetailConnection ? t("botDetail.subtitle") : activeTab ? tabWorkspaceTitle(activeTab) : "";
  const topicbarWorkspacePath = activeTab?.scope === "project" ? activeTab.workspaceRoot || state.meta?.cwd : "";
  const topicbarImSource = activeTab?.topicId ? imTopicSources[activeTab.topicId] : undefined;
  const topicbarImSourceLabel = sidebarImDetailConnection
    ? sidebarImDetailConnection.platformLabel
    : topicbarImSource ? t("msg.fromIm", { source: topicbarImSource.label }) : "";
  const topicbarImSourcePlatform = sidebarImDetailConnection?.platform ?? topicbarImSource?.platform;
  const topicbarSubtitleVisible = Boolean(topicbarWorkspaceLabel || topicbarImSourceLabel || state.meta?.label);
  const topicbarSubtitleTitle = sidebarImDetailConnection
    ? [topicbarWorkspaceLabel, topicbarImSourceLabel, sidebarImScopeLabel(sidebarImDetailConnection, t)].filter(Boolean).join(" · ")
    : [topicbarWorkspacePath || topicbarWorkspaceLabel, topicbarImSourceLabel].filter(Boolean).join(" · ");
  const sidebarImConnectedCount = sidebarImConnections.filter((connection) => connection.status === "connected").length;
  const sidebarImOnline = sidebarImConnectedCount > 0;

  // (sessionActions block removed 2026-08-18: Copy / Export relocated to the
  // project-tree topic context menu; exportSession/getSessionMarkdown below
  // remain the implementation and are wired through ProjectTree props.)

  const headerNode = !preferenceOpen && !teamOpen && !kbOpen && !ragOpen && (
    <header className="topicbar">
      <div className="topicbar__identity">
        <div className="topicbar__title-row">
          {topicbarEditing ? (
            <div className="topicbar__title-edit">
              <input
                autoFocus
                className="topicbar__title-input"
                value={topicTitleDraft}
                onChange={(event) => setTopicTitleDraft(event.target.value)}
                onKeyDown={(event: KeyboardEvent<HTMLInputElement>) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    void commitActiveTopicRename();
                  }
                  if (event.key === "Escape") {
                    event.preventDefault();
                    cancelActiveTopicRename();
                  }
                }}
                onBlur={() => void commitActiveTopicRename()}
              />
            </div>
          ) : (
            <h1 title={sidebarImDetailConnection ? topicbarTitle : topicTitle(activeTab)}>{topicbarTitle}</h1>
          )}
          <Tooltip label={t("topicBar.renameSession")}>
            <button
              className="topicbar__icon-btn"
              type="button"
              disabled={Boolean(sidebarImDetailConnection) || !activeTab?.topicId || topicbarEditing}
              onClick={startActiveTopicRename}
              aria-label={t("topicBar.renameSession")}
            >
              <Pencil size={14} />
            </button>
          </Tooltip>
        </div>
        {topicbarSubtitleVisible && (
          <div className="topicbar__subtitle" title={topicbarSubtitleTitle}>
            {topicbarWorkspaceLabel && <span>{topicbarWorkspaceLabel}</span>}
            {/* Model label dropped from the header (2026-08-18): it already
                lives on the composer's model selector. */}
            {topicbarImSourcePlatform && (
              <span className={`topicbar__source-chip topicbar__source-chip--${topicbarImSourcePlatform}`}>
                {topicbarImSourceLabel}
              </span>
            )}
          </div>
        )}
      </div>
      <div className="topicbar__spacer" />
      {/* All topic actions relocated (2026-08-19): copy/export/改动 live in the
          project-tree context menu; the palette button moved to the chrome's
          right icon cluster next to the terminal/workspace toggles. The
          topicbar is now a pure title/info row. */}
    </header>
  );

  const mainNode = (
    <main className="main">
      {/* Coding-profile preference surface: rendered only in dev so the dev
          sidebar's button can never bleed the panel into cowork/netdev shells
          (mainNode is embedded in all three layouts). */}
      {preferenceOpen && !coworkActive && !netdevActive && (
        <PreferencePanel
          mode="dev"
          title={t("preference.title") || "编码偏好"}
          onClose={() => setPreferenceOpen(false)}
        />
      )}
      {/* 团队 surface for the coding profile: the shared TeamBoard, given the
          whole main area. The chat Transcript below is skipped while it's open
          (see the leading ternary guard) so the kanban never shares flex space
          with the conversation. */}
      {teamOpen && !coworkActive && !netdevActive && (
        <div style={{ display: "flex", flexDirection: "column", flex: 1, minHeight: 0, height: "100%" }}>
          <TeamBoard />
        </div>
      )}
      {/* 项目知识中枢 surface for the coding profile: the same KnowledgeHub the
          办公 profile hosts. Full-area, and the transcript is skipped below. */}
      {kbOpen && !coworkActive && !netdevActive && (
        <div style={{ display: "flex", flexDirection: "column", flex: 1, minHeight: 0, height: "100%" }}>
          <KnowledgeHub />
        </div>
      )}
      {/* 知识库 (RAG graph) surface for the coding profile — previously cowork-
          only. Same full-area treatment. */}
      {ragOpen && !coworkActive && !netdevActive && (
        <div style={{ display: "flex", flexDirection: "column", flex: 1, minHeight: 0, height: "100%" }}>
          <RagPanel />
        </div>
      )}
      {(teamOpen || kbOpen || ragOpen) && !coworkActive && !netdevActive ? null : sidebarImDetailConnection ? (
        <SidebarImConnectionDetail
          connection={sidebarImDetailConnection}
          sessions={sidebarImSessions}
          allConnections={sidebarImConnections}
          onClose={() => setSidebarImDetailConnectionId("")}
          onOpenSettings={openBotSettings}
          onOpenSession={() => void openSidebarImConnectionSession(sidebarImDetailConnection)}
          onOpenSessionPath={(path) => void openSidebarImSessionPath(path)}
          onSelectConnection={(id) => setSidebarImDetailConnectionId(id)}
        />
      ) : state.meta?.ready === false && !state.meta?.startupErr ? (
        <div className="loading-screen">
          <div className="loading-screen__spinner" />
          <span className="loading-screen__text">{t("common.loading")}</span>
        </div>
      ) : state.meta?.expertSession ? (
        <ExpertSessionView
          key={state.meta.expertSession.teamId}
          items={state.items}
          teamName={state.meta.expertSession.teamName}
          teamId={state.meta.expertSession.teamId}
          running={state.running}
          onSend={(task, mode, rounds) => runExpertFromSession(state.meta!.expertSession!.teamId, task, mode, rounds)}
        />
      ) : (
        <Transcript
          items={state.items}
          live={state.live}
          footerHeight={footerHeight}
          onPrompt={send}
          onRemoteConnect={() => setRemoteWizardOpen(true)}
          onRewind={handleMessageAction}
          checkpoints={state.checkpoints}
          actionPending={state.messageAction != null}
          rewindDisabled={state.running || state.messageAction != null || state.approval != null || state.ask != null || clearContextPending}
          defaultExpandThinking={expandThinking}
          profile={coworkActive ? "cowork" : netdevActive ? "netdev" : "dev"}
          onInsert={addWorkspaceTextToComposer}
          modelLabel={state.meta?.label}
        />
      )}
    </main>
  );

  // Expert-session tabs render their own composer inside ExpertSessionView, so
  // the normal chat Composer (footerNode) is suppressed for them to avoid a
  // double-composer.
  const footerNode = !sidebarImDetailConnection && !state.meta?.expertSession ? (
    <footer className="footer" ref={footerRef}>
      {showTodos && <TodoPanel todos={todos} onDismiss={() => setDismissedTodo(todoItem!.id)} />}
      {state.approval && (
        <ApprovalModal
          approval={state.approval}
          onAnswer={(allow, session, persist) => {
            // Approving an exit_plan_mode plan leaves plan mode; sync the
            // tab-local indicator and persisted safe mode immediately.
            if (state.approval!.tool === "exit_plan_mode" && allow) applyCollaborationMode("normal");
            approve(state.approval!.id, allow, session, persist);
          }}
          onRevisePlan={(text) => {
            if (activeTabId) {
              setPendingPlanRevisionsByTab((current) => ({ ...current, [activeTabId]: text }));
            }
            approve(state.approval!.id, false, false, false);
          }}
          onExitPlan={() => {
            applyCollaborationMode("normal");
            approve(state.approval!.id, false, false, false);
          }}
        />
      )}
      {state.ask && (
        <AskCard
          ask={state.ask}
          onAnswer={answerQuestion}
          onDismiss={() => answerQuestion(state.ask!.id, [])}
        />
      )}
      {clearContextPending && (
        <ClearContextCard
          onCancel={cancelClearContext}
          onConfirm={() => {
            void confirmClearContext();
          }}
        />
      )}
      <Composer
        key={`composer-${coworkActive ? "cowork" : netdevActive ? "netdev" : "dev"}`}
        running={state.running}
        paused={state.paused}
        collaborationMode={collaborationMode}
        showKnowledge={coworkActive}
        placeholderOverride={netdevActive ? "描述故障现象或要查的状态，如「core-sw-1 的 OSPF 邻居一直 down」…" : undefined}
        toolApprovalMode={toolApprovalMode}
        goal={goal}
        cwd={state.meta?.cwd}
        modelLabel={state.meta?.label ?? t("status.connecting")}
        tabId={activeTabId}
        effort={state.effort}
        contextInfo={state.context}
        usage={state.usage}
        budget={budget}
        jobs={state.jobs}
        runningPhase={(() => {
          if (!state.running) return undefined;
          // Compaction in flight outranks the last tool phase (spec 2-5) —
          // "compacting…" answers "what is it doing" better than a stale
          // tool name while the summarizer runs.
          for (let i = state.items.length - 1; i >= 0; i--) {
            const it = state.items[i];
            if (it.kind === "compaction" && it.pending) return t("compaction.working");
            if (it.kind === "phase") return it.text;
          }
          return undefined;
        })()}
        thinkingTitle={(() => {
          // First bold line of the streaming reasoning, codex-style (spec
          // 2-2): the model usually opens with "Examining the config…" — that
          // is the status the user wants, not a spinner word.
          if (!state.running || !state.live) return undefined;
          const m = /\*\*([^*]+)\*\*/.exec(state.live.reasoning);
          return m ? m[1].slice(0, 80) : undefined;
        })()}
        queued={{ steer: queuedMsgs.steer ?? [], followUp: queuedMsgs.followUp ?? [] }}
        onQueueFollowUp={queueFollowUp}
        onSend={handleSend}
        onCancel={cancel}
        onPauseToggle={pauseToggle}
        onCycleMode={cycleMode}
        onSetCollaborationMode={applyCollaborationMode}
        onSetToolApprovalMode={applyToolApprovalMode}
        onToggleYoloApprovalMode={toggleYoloApprovalMode}
        onClearGoal={() => applyGoal("")}
        onSwitchModel={switchModel}
        onSetEffort={setEffort}
        ragScope={ragScope}
        onPickRagScope={setControllerRagScope}
        insertRequest={composerInsertRequest}
        onInsertComplete={() => setComposerInsertRequest(null)}
        disabled={state.meta?.ready === false || state.messageAction != null || state.approval != null || state.ask != null || clearContextPending}
        decisionPending={state.messageAction != null || state.approval != null || state.ask != null || clearContextPending}
        ready={state.meta?.ready === true}
        turnStartAt={state.turnStartAt}
        turnTokens={state.turnTokens}
        turnUsage={state.usage}
        retry={state.retry}
        transientDismissSignal={transientOverlayDismissSignal}
      />

    </footer>
  ) : null;

  // Filter out sessions already visible in the project tree to avoid duplicates.
  const dedupedSidebarSessions = sidebarSessions.filter(
    (s) => !s.topicId || !projectTreeTopicIds.has(s.topicId),
  );

  // Terminal console (Ctrl+`): built once and lent to whichever surface owns
  // the chat body — the coding-mode chat pane or 运维 shell — so the
  // chrome's terminal toggle works in every mode and the panel never mounts
  // twice.
  const terminalNode = terminalOpen && (
    <TerminalPanel
      onClose={toggleTerminal}
      cwd={state.meta?.cwd}
      openDevice={openDeviceTerm}
      sessionPane={
        <SideSessionPane
          tabs={tabMetas}
          sessions={dedupedSidebarSessions}
          activeMainTabId={activeTabId ?? undefined}
          selectedId={sideSessionTabId}
          onSelect={setSideSessionTabId}
        />
      }
    />
  );

  // Global banners (startup error / update notice): built once and lent to
  // whichever surface owns the main area — the coding chat pane, the office
  // main, or 运维 shell. They used to live only inside the chat pane,
  // which display:none hides in cowork/netdev, so the notices were invisible
  // there; each mode now renders them at the top of its main column.
  const bannersNode = (
    <>
      {state.meta?.startupErr && (
        <div className="banner banner--error">{t("topbar.startupError", { msg: state.meta.startupErr })}</div>
      )}
      <UpdateBanner enabled={startupUpdateChecksEnabled === true} />
    </>
  );

  // Sidebar search sits ABOVE the Recent sessions section and drives the
  // project tree (hoisted out of ProjectTree, ui-redesign §4-B4 follow-up).
  const [treeQuery, setTreeQuery] = useState("");
  const sidebarSearchRef = useRef<HTMLLabelElement | null>(null);
  // Geek touch: "/" anywhere (outside editable fields) focuses the sidebar
  // search — mirrors terminal UIs' filter affordance.
  useEffect(() => {
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "/" || event.ctrlKey || event.metaKey || event.altKey) return;
      const el = event.target as HTMLElement | null;
      if (el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable)) return;
      event.preventDefault();
      sidebarSearchRef.current?.querySelector("input")?.focus();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const sidebarSearchNode = (
    <>
      <button
        type="button"
        className="sidebar-new-session-bar-btn"
        onClick={(event) => {
          // ZCode-style new-task flow: offer local vs remote from the primary
          // entry instead of only opening a blank local session.
          const rect = event.currentTarget.getBoundingClientRect();
          setNewTaskMenuPoint({ left: rect.left, top: rect.bottom + 6 });
        }}
        aria-label={t("topbar.newTask" as never)}
        title={t("topbar.newTask" as never)}
      >
        <Plus size={14} />
        <span>{t("topbar.newTask" as never)}</span>
      </button>
      <ContextMenu
        open={newTaskMenuPoint !== null}
        point={newTaskMenuPoint}
        onClose={() => setNewTaskMenuPoint(null)}
        ariaLabel={t("topbar.newTask" as never)}
        items={[
          {
            key: "new-session",
            icon: <Plus size={14} />,
            label: t("topbar.newSession"),
            title: "Ctrl+N",
            onSelect: () => { setNewTaskMenuPoint(null); void handleNewTab(); },
          },
          {
            key: "remote-connect",
            icon: <Server size={14} />,
            label: t("remote.trigger"),
            onSelect: () => { setNewTaskMenuPoint(null); setRemoteWizardOpen(true); },
          },
        ]}
      />
      <label className="project-tree__search sidebar-search" ref={sidebarSearchRef}>
        <span className="sidebar-search__prompt" aria-hidden="true">❯</span>
        <input
          value={treeQuery}
          onChange={(e) => setTreeQuery(e.target.value)}
          placeholder={t("projectTree.searchPlaceholder")}
        />
        <kbd className="sidebar-search__kbd" aria-hidden="true">/</kbd>
      </label>
    </>
  );

  const projectTreeNode = (
    <ProjectTree
      activeScope={activeTab?.scope}
      activeWorkspaceRoot={activeTab?.workspaceRoot}
      activeTopicId={activeTab?.topicId}
      profile={netdevActive ? "netdev" : coworkActive ? "cowork" : "dev"}
      query={treeQuery}
      onQueryChange={setTreeQuery}
      hideSearch
      imTopicSources={imTopicSources}
      onOpenTopic={handleOpenTopic}
      onOpenRemote={() => setRemoteWizardOpen(true)}
      onOpenExpertSession={async (teamId, teamName) => {
        try {
          const meta = await app.OpenExpertSessionTab(teamId, teamName);
          // Switch to task center so the main area shows the expert tab's
          // ExpertSessionView (not the sidebar ExpertPanel).
          window.dispatchEvent(new CustomEvent("cowork:reset-panel"));
          await refreshTabMetas();
          await syncActiveTab(false);
          if (meta?.id) {
            setTimeout(() => { void refreshTabMetas(); void syncActiveTab(false); }, 1000);
          }
        } catch { /* ignore */ }
      }}
      onOpenProjectHistory={openProjectHistory}
      onCreateTopic={(scope, workspaceRoot) => openBlankSession(scope, scope === "project" ? workspaceRoot : "")}
      onTopicsChanged={refreshProjectsAndTabs}
      onRenameTopic={renameTopic}
      onCopyActiveSession={() => {
        void navigator.clipboard
          .writeText(getSessionMarkdown())
          .then(() => showToast(t("msg.copied"), "info"))
          .catch(() => { /* clipboard denied */ });
      }}
      onExportActiveSession={
        sessionHasContent ? (format) => void exportSession(format) : undefined
      }
      // The changed-files dock is a coding-view surface; the node is shared
      // with the cowork/netdev rails, where opening the hidden dev dock would
      // only mutate invisible state.
      onOpenChangedDock={() => { if (!coworkActive && !netdevActive) openRightDockMode("changed"); }}
      onCopyTopicPath={async (topicId) => {
        try {
          const sessions = await app.ListSessions();
          const hit = sessions.find((s) => s.topicId === topicId);
          if (!hit?.path) {
            showToast(t("projectTree.pathMissing"), "warn");
            return;
          }
          await navigator.clipboard.writeText(hit.path);
          showToast(t("msg.copied"), "info");
        } catch {
          /* clipboard unavailable */
        }
      }}
      refreshSignal={projectRevision}
      onAddProject={async () => {
        await switchFolder();
      }}
      onTopicIdsChanged={setProjectTreeTopicIds}
    />
  );

  const sidebarSessionsNode = (
    <SidebarSessions
      sessions={dedupedSidebarSessions}
      onResume={(session) => {
        void onResumeSession(session);
        window.setTimeout(refreshSidebarSessions, 500);
      }}
      onRename={(path, title) => {
        void onRenameSession(path, title);
        window.setTimeout(refreshSidebarSessions, 300);
      }}
      onDelete={(path) => {
        void onDeleteSession(path);
        window.setTimeout(refreshSidebarSessions, 300);
      }}
    />
  );

  return (
    <ShellExpandProvider>
    <div
      ref={appRef}
      className={[
        "app",
        `app--${desktopPlatform}`,
        browserPreviewChrome ? "app--browser-preview" : "",
        coworkActive ? "app--cowork" : "",
        netdevActive ? "app--netdev" : "",
        sidebarCollapsed ? "app--sidebar-collapsed" : "",
      ]
        .filter(Boolean)
        .join(" ")}
      style={layoutStyle}
    >
      {/* Decorative wallpaper layer. Rendered first (and fixed beneath) so it
          paints under .layout, which turns translucent while it is active. */}
      <AppBackdrop active={wallpaperActive} />
      <div
        className={[
          "layout",
          sidebarCollapsed ? "layout--sidebar-collapsed" : "",
          sidebarResizing ? "layout--resizing layout--sidebar-resizing" : "",
          workspacePanelGridOpen ? "layout--workspace-open" : "",
          workspacePanelOpen && workspacePanelMaximized ? "layout--workspace-maximized" : "",
          workspacePanelResizing ? "layout--resizing layout--workspace-resizing" : "",
        ]
          .filter(Boolean)
          .join(" ")}
      >
        {coworkActive && (
          <CoWorkLayout
          onPickProject={openProfileProject}
          onAddProject={() => { void switchFolder(); }}
          onSwitchMode={(mode) => { void switchProfile(mode).catch(() => { /* revert handled in switchProfile */ }); }}
            mainNode={mainNode}
            footerNode={footerNode}
            bannersNode={bannersNode}
            terminalNode={terminalNode}
            projectTreeNode={projectTreeNode}
            sessionsNode={sidebarSessionsNode}
            searchNode={sidebarSearchNode}
            rightDockOpen={coworkDockRenderable}
            sidebarCollapsed={sidebarCollapsed}
            onNewSession={() => void handleNewTab()}
            onToggleSidebar={toggleSidebar}
            sidebarToggleTitle={sidebarToggleTitle}
            dockCwd={state.meta?.cwd}
            dockMaximized={workspacePanelMaximized}
            dockOnClose={() => closeWorkspacePanel()}
            dockOnToggleMaximized={() => {
              closeTransientOverlays();
              setWorkspacePanelMaximized((value) => !value);
            }}
            dockWidth={workspacePanelRenderWidth}
            dockMinWidth={workspacePanelResizeMinWidth}
            dockMaxAriaWidth={Math.max(workspacePanelMaxWidth, workspacePanelRenderWidth)}
            onDockResizeStart={startWorkspacePanelResize}
            onDockResizeKey={resizeWorkspacePanelWithKeyboard}
            onDockResetWidth={() => setSavedWorkspacePanelWidth(workspacePanelResetWidth)}
            sidebarWidth={sidebarWidth}
            sidebarMinWidth={SIDEBAR_MIN_WIDTH}
            sidebarMaxWidth={SIDEBAR_MAX_WIDTH}
            onSidebarResizeStart={startSidebarResize}
            onSidebarResizeKey={resizeSidebarWithKeyboard}
            onSidebarResetWidth={() => setExpandedSidebarWidth(defaultSidebarWidth())}
            contextInfo={state.context}
            sessionTokens={state.sessionTokens}
            activeTabId={activeTabId}
            dockRefreshKey={dockRefreshKey}
            dockBusy={state.running}
          />
        )}
        {netdevActive && (
          <NetDevLayout
          onPickProject={openProfileProject}
          onAddProject={() => { void switchFolder(); }}
          onSwitchMode={(mode) => { void switchProfile(mode).catch(() => { /* revert handled in switchProfile */ }); }}
            mainNode={mainNode}
            footerNode={footerNode}
            bannersNode={bannersNode}
            terminalNode={terminalNode}
            sessionsNode={sidebarSessionsNode}
            searchNode={sidebarSearchNode}
            projectTreeNode={projectTreeNode}
            onOpenSettings={(t) => { setSettingsTarget(t as never); setSettingsPayload(null); }}
            onInsertComposer={addWorkspaceTextToComposer}
            onNewSession={() => void handleNewTab()}
            onToggleSidebar={toggleSidebar}
            sidebarToggleTitle={sidebarToggleTitle}
            sidebarCollapsed={sidebarCollapsed}
            sidebarWidth={sidebarWidth}
            sidebarMinWidth={SIDEBAR_MIN_WIDTH}
            sidebarMaxWidth={SIDEBAR_MAX_WIDTH}
            onSidebarResizeStart={startSidebarResize}
            onSidebarResizeKey={resizeSidebarWithKeyboard}
            onSidebarResetWidth={() => setExpandedSidebarWidth(defaultSidebarWidth())}
            dockOpen={netdevDockOpen}
            dockOnClose={() => closeWorkspacePanel()}
            onDockOpen={() => setNetdevDockOpen(true)}
            dockWidth={Math.max(workspacePanelRenderWidth, NETDEV_DOCK_MIN_WIDTH)}
            dockMinWidth={Math.max(workspacePanelResizeMinWidth, NETDEV_DOCK_MIN_WIDTH)}
            dockMaxAriaWidth={Math.max(workspacePanelMaxWidth, workspacePanelRenderWidth)}
            onDockResizeStart={startWorkspacePanelResize}
            onDockResizeKey={resizeWorkspacePanelWithKeyboard}
            onDockResetWidth={() => setSavedWorkspacePanelWidth(workspacePanelResetWidth)}
          />
        )}
        <AppChrome
          platform={desktopPlatform}
          browserPreviewChrome={browserPreviewChrome}
          tabs={visibleTabs}
          activeTabId={visibleTabId}
          revealActiveSignal={tabRevealSignal}
          commandCompact={workspacePanelGridOpen}
          sidebarTogglePressed={sidebarTogglePressed}
          sidebarExpandBlocked={sidebarExpandBlocked}
          sidebarCollapsed={sidebarCollapsed}
          sidebarToggleTitle={sidebarToggleTitle}
          workspacePanelMaximized={workspacePanelMaximized}
          workspacePanelRenderable={coworkActive ? coworkDockRenderable : netdevActive ? netdevDockOpen : workspacePanelRenderable}
          workspaceTogglePressed={workspaceTogglePressed}
          workspacePanelLabel={(coworkActive ? coworkDockRenderable : netdevActive ? netdevDockOpen : workspacePanelRenderable) ? t("rightDock.collapse") : t("rightDock.expand")}
          onToggleSidebar={toggleSidebar}
          onToggleWorkspacePanel={toggleWorkspacePanel}
          onTabChange={(id) => void handleTabChange(id)}
          onTabClose={(id) => void handleTabClose(id)}
          onTabsClose={(ids, nextActiveTabId) => void handleTabsClose(ids, nextActiveTabId)}
          onTabsReorder={(ids) => void handleTabsReorder(ids)}
          onNewTab={() => void handleNewTab()}
          center={netdevActive
            ? <NetdevTitleBar leading={headerNode} onOpenSettings={(t) => { setSettingsTarget(t as never); setSettingsPayload(null); }} />
            : headerNode}
          onOpenPalette={() => void openPalette()}
          terminalOpen={terminalOpen}
          onToggleTerminal={toggleTerminal}
          profile={netdevActive ? "netdev" : coworkActive ? "cowork" : "dev"}
          onSwitchProfile={(name) => void switchProfile(name).catch(() => { /* revert handled in switchProfile */ })}
        />

          {/* No inline paddingBottom on the sidebar: the stylesheet's
              calc(12px + --bottom-bar-height) reserve must survive — an inline
              8px override let the sidebar-only bottom bar cover the pinned
              编码偏好 button (2026-08-19). */}
          <aside
            className={`sidebar${sidebarCollapsed ? " sidebar--collapsed" : ""}`}
            aria-label={t("sidebar.navigation")}
          >
          <PaneErrorBoundary label="侧栏">
          <div className="sidebar__brandrow">
            {/* Logo retired (2026-08-19): the collapse toggle takes the leftmost
                slot, the mode name follows it; new-session stays right. */}
            <button
              type="button"
              className="app-chrome__panel-toggle app-chrome__panel-toggle--left sidebar__brand-toggle sidebar__brand-toggle--lead"
              onClick={toggleSidebar}
              aria-label={sidebarToggleTitle}
              aria-pressed={!sidebarCollapsed}
            >
              <div className="brand-toggle-group">
                <img src={logoSymbol} alt="" className="brand-toggle-logo" draggable={false} />
                <PanelLeft size={16} className="brand-toggle-icon" />
              </div>
            </button>
            <ProfileSegmented
              profile={netdevActive ? "netdev" : coworkActive ? "cowork" : "dev"}
              onSwitchProfile={(mode) => { void switchProfile(mode).catch(() => {}); }}
              t={t}
            />

          </div>

          {/* Search renders ONLY here in dev mode — cowork/netdev render the
              same node inside their layouts, and the "/" hotkey ref must land
              on the VISIBLE instance (a hidden duplicate would steal focus). */}
          {!coworkActive && !netdevActive && sidebarSearchNode}

          {/* Same single-mount rule for the session list and project tree: the
              hidden sidebar's copies would double-fetch ListProjectTree /
              ListSessions and double-write topic-seen state while the
              cowork/netdev rails render their own visible copies. */}
          {!coworkActive && !netdevActive && sidebarSessionsNode}

          <section className="sidebar__section sidebar__section--projects">
            {/* No "文件" toggle: this section IS the project workspace — the
                tree is always present (user decision 2026-08-18). */}
            {!coworkActive && !netdevActive && projectTreeNode}
          </section>

          {/* Coding-profile bottom group: both surfaces are dev-only (loop is
              backend-refused elsewhere), so skip the buttons entirely instead
              of leaving dead (CSS-hidden) state toggles mounted. */}
          {!coworkActive && !netdevActive && (
            <section className="cowork-sidebar__group" style={{ marginBottom: '0px', marginTop: 'auto' }}>
              <button
                className={`cowork-sidebar__item ${preferenceOpen ? "cowork-sidebar__item--active" : ""}`}
                onClick={() => {
                  closeTransientOverlays();
                  setLoopOpen(false);
                  setTeamOpen(false);
                  setKbOpen(false);
                  setRagOpen(false);
                  setPreferenceOpen(true);
                }}
              >
                <SlidersHorizontal size={14} />
                <span>{t("preference.title") || "编码偏好"}</span>
              </button>
              {/* 项目知识中枢 (project map: code/docs/memory/team → one map +
                  revisions). Mirrors the 办公 profile's entry to the same panel. */}
              <button
                className={`cowork-sidebar__item ${kbOpen ? "cowork-sidebar__item--active" : ""}`}
                onClick={() => {
                  closeTransientOverlays();
                  setPreferenceOpen(false);
                  setLoopOpen(false);
                  setTeamOpen(false);
                  setRagOpen(false);
                  setKbOpen(true);
                }}
              >
                <Network size={14} />
                <span>{t("cowork.knowledgeHub")}</span>
              </button>
              {/* 知识库 (RAG document/entity graph) — same RagPanel the 办公
                  profile hosts, so it's no longer lockable inside office mode. */}
              <button
                className={`cowork-sidebar__item ${ragOpen ? "cowork-sidebar__item--active" : ""}`}
                onClick={() => {
                  closeTransientOverlays();
                  setPreferenceOpen(false);
                  setLoopOpen(false);
                  setTeamOpen(false);
                  setKbOpen(false);
                  setRagOpen(true);
                }}
              >
                <BookOpen size={14} />
                <span>{t("cowork.knowledgeBase") || "知识库"}</span>
              </button>
              {/* 团队 (multi-agent: 团长 + 团员 + 看板). The coding profile's own
                  entry to the SAME TeamBoard the 办公 profile hosts, so "团队开发
                  项目" is one click away without a profile switch. */}
              <button
                className={`cowork-sidebar__item ${teamOpen ? "cowork-sidebar__item--active" : ""}`}
                onClick={() => {
                  closeTransientOverlays();
                  setPreferenceOpen(false);
                  setLoopOpen(false);
                  setKbOpen(false);
                  setRagOpen(false);
                  setTeamOpen(true);
                }}
              >
                <SquareKanban size={14} />
                <span>{t("team.title")}</span>
              </button>
              <button
                className={`cowork-sidebar__item ${loopOpen ? "cowork-sidebar__item--active" : ""}`}
                onClick={() => {
                  closeTransientOverlays();
                  setPreferenceOpen(false);
                  setTeamOpen(false);
                  setKbOpen(false);
                  setRagOpen(false);
                  setLoopOpen(true);
                }}
              >
                <Repeat2 size={14} />
                <span>{t("loop.title")}</span>
              </button>
            </section>
          )}

        </PaneErrorBoundary>
</aside>
        <button
          className="sidebar-resizer"
          type="button"
          role="separator"
          aria-orientation="vertical"
          aria-label={t("sidebar.resize")}
          aria-valuemin={SIDEBAR_MIN_WIDTH}
          aria-valuemax={SIDEBAR_MAX_WIDTH}
          aria-valuenow={sidebarWidth}
          onPointerDown={startSidebarResize}
          onKeyDown={resizeSidebarWithKeyboard}
          onDoubleClick={() => setExpandedSidebarWidth(defaultSidebarWidth())}
        />

        <PaneErrorBoundary label="对话区">
          <section className="chat-pane">
          {/* Dev-profile topicbar moved into the top chrome (AppChrome center
              slot). The coding chat body below is DEV-ONLY: cowork/netdev
              render mainNode/footerNode/terminal/banners inside their own
              shells, so this pane (hidden via .app--cowork/.app--netdev
              anyway) stays unmounted there — no duplicate mounts. */}
          {!coworkActive && !netdevActive && (
            <>
              {bannersNode}
              {mainNode}
              {!preferenceOpen && !teamOpen && !kbOpen && !ragOpen && footerNode}
              {terminalNode}
            </>
          )}
        </section>
        </PaneErrorBoundary>

        {workspacePanelGridOpen && (
          <button
            className="workspace-panel-resizer"
            type="button"
            role="separator"
            aria-orientation="vertical"
            aria-label={t("rightDock.resize")}
            aria-valuemin={workspacePanelResizeMinWidth}
            aria-valuemax={Math.max(workspacePanelMaxWidth, workspacePanelRenderWidth)}
            aria-valuenow={workspacePanelRenderWidth}
            onPointerDown={startWorkspacePanelResize}
            onKeyDown={resizeWorkspacePanelWithKeyboard}
            onDoubleClick={() => setSavedWorkspacePanelWidth(workspacePanelResetWidth)}
          />
        )}

        {workspacePanelRenderable && (
          <aside
            className={[
              "workbench-dock",
              `workbench-dock--${rightDockMode}`,
            ].join(" ")}
            aria-label={t("rightDock.workbench")}
          >
            <div className="workbench-dock__tools">
              {/* Browser-style tab management (2026-08-19): only OPEN tabs show,
                  each closable; "+" offers the full catalog in a dropdown. */}
              <div className="workbench-dock__tabs" role="tablist" aria-label={t("rightDock.views")}>
                {dockTabs
                  .map((mode) => (
                    <div
                      key={mode}
                      className={`workbench-dock__tabwrap${rightDockMode === mode ? " workbench-dock__tabwrap--active" : ""}`}
                    >
                      <button
                        type="button"
                        role="tab"
                        aria-selected={rightDockMode === mode}
                        className={`workbench-dock__tab${rightDockMode === mode ? " workbench-dock__tab--active" : ""}`}
                        onClick={() => openRightDockMode(mode)}
                      >
                        {mode === "turns" ? <Activity size={13} />
                          : mode === "files" ? <FileText size={13} />
                          : mode === "changed" ? <GitBranch size={13} />
                          : mode === "preview" ? <Globe size={13} />
                          : <MessageSquare size={13} />}
                        <span className="workbench-dock__tab-label">
                          {mode === "turns" ? t("rightDock.turns")
                            : mode === "files" ? t("workspace.filesTab")
                            : mode === "changed" ? t("workspace.changedTab")
                            : mode === "preview" ? t("preview.tabTitle")
                            : t("sideSession.tabTitle")}
                        </span>
                        {mode === "changed" && changedDirty && rightDockMode !== "changed" && (
                          <span className="workbench-dock__tab-dot" aria-hidden="true" />
                        )}
                      </button>
                      <button
                        type="button"
                        className="workbench-dock__tab-close"
                        onClick={() => closeDockTab(mode)}
                        aria-label={t("dock.closeTab")}
                        title={t("dock.closeTab")}
                      >
                        <X size={15} />
                      </button>
                    </div>
                  ))}
              </div>
              <button
                type="button"
                className="workbench-dock__tab-add"
                onClick={(e) => {
                  const r = (e.currentTarget as HTMLButtonElement).getBoundingClientRect();
                  // Pre-clamp so the first paint is already on-screen — the
                  // menu's own flip logic otherwise corrects one frame late
                  // and the dropdown visibly jumps in from the right.
                  const menuWidth = 176;
                  const left = Math.max(8, Math.min(r.left, window.innerWidth - menuWidth - 8));
                  setDockAddMenuPoint({ left, top: r.bottom + 4 });
                }}
                aria-label={t("dock.addTab")}
                title={t("dock.addTab")}
              >
                <Plus size={13} />
              </button>
              <ContextMenu
                open={dockAddMenuPoint !== null}
                point={dockAddMenuPoint}
                minWidth={160}
                ariaLabel={t("dock.addTab")}
                onClose={() => setDockAddMenuPoint(null)}
                items={DOCK_TAB_CATALOG
                  .filter((mode) => !dockTabs.includes(mode))
                  .map((mode) => ({
                    key: mode,
                    icon:
                      mode === "turns" ? <Activity size={13} />
                      : mode === "files" ? <FileText size={13} />
                      : mode === "changed" ? <GitBranch size={13} />
                      : mode === "preview" ? <Globe size={13} />
                      : <MessageSquare size={13} />,
                    label:
                      mode === "turns" ? t("rightDock.turns")
                      : mode === "files" ? t("workspace.filesTab")
                      : mode === "changed" ? t("workspace.changedTab")
                      : mode === "preview" ? t("preview.tabTitle")
                      : t("sideSession.tabTitle"),
                    onSelect: () => {
                      setDockAddMenuPoint(null);
                      openRightDockMode(mode);
                    },
                  }))}
              />
            </div>
            <div className="workbench-dock__body">
              {rightDockMode === "turns" ? (
                <ContextPanel
                  tabId={activeTabId}
                  context={state.context}
                  sessionTokens={state.sessionTokens}
                  refreshKey={dockRefreshKey}
                  busy={state.running}
                />
              ) : rightDockMode === "preview" ? (
                <PreviewPane url={previewUrl} onUrlCommit={commitPreviewUrl} />
              ) : rightDockMode === "session" ? (
                <SideSessionPane
                  tabs={tabMetas}
                  sessions={dedupedSidebarSessions}
                  activeMainTabId={activeTabId ?? undefined}
                  selectedId={sideSessionTabId}
                  onSelect={setSideSessionTabId}
                />
              ) : (
                <WorkspacePanel
                  open={workspacePanelRenderable}
                  cwd={state.meta?.cwd}
                  tabId={activeTabId ?? undefined}
                  branchSwitch={
                    activeTab?.remote
                      ? { enabled: false, blockedReason: t("workspace.branchSwitchRemote") }
                      : state.running
                        ? { enabled: true, blockedReason: t("workspace.branchSwitchBlockedRun") }
                        : state.approval
                          ? { enabled: true, blockedReason: t("workspace.branchSwitchBlockedApproval") }
                          : { enabled: true }
                  }
                  maximized={workspacePanelMaximized}
                  panelWidth={workspacePanelRenderWidth}
                  onClose={closeWorkspacePanel}
                  onToggleMaximized={() => {
                    closeTransientOverlays();
                    setWorkspacePanelMaximized((value) => !value);
                  }}
                  onPreviewModeChange={handleWorkspacePreviewModeChange}
                  onAddToChat={addWorkspaceTextToComposer}
                  onRequestPanelWidth={ensureWorkspacePanelWidth}
                  refreshKey={dockRefreshKey}
                  initialViewMode={rightDockMode === "changed" ? "changed" : "files"}
                  showViewTabs={false}
                />
              )}
            </div>
          </aside>
        )}
      </div>

      <div className="global-bottom-bar">
        <SidebarFooter
          imConnectionCount={sidebarImConnections.length}
          imOnline={sidebarImOnline}
          tooltipDisabled={false}
          onOpenIm={() => void showSidebarImDetail()}
          onOpenHistory={() => void openAllHistory()}
          onOpenTrash={() => void openTrash()}
          onOpenSettings={() => {
            closeTransientOverlays();
            setSettingsTarget("general");
          }}
        />
      </div>

      {/* Loop is a coding-profile facility (raw-shell sensor/verify/rollback);
          the profile-switch effect closes it, and this render gate keeps it
          from ever mounting over the cowork/netdev shells. */}
      {loopOpen && !coworkActive && !netdevActive && (
        <div className="management-modal-backdrop" onClick={() => setLoopOpen(false)}>
          <div className="management-modal loop-modal" onClick={(e) => e.stopPropagation()}>
            <LoopPanel onClose={() => setLoopOpen(false)} tabs={tabMetas} activeTabId={activeTabId ?? undefined} />
          </div>
        </div>
      )}

      {histView !== null && (
        <HistoryPanel
          kind={histView.kind}
          sessions={histView.sessions}
          running={state.running}
          onResume={onResumeSession}
          onPreview={previewSession}
          onDelete={onDeleteSession}
          onRename={onRenameSession}
          onRestore={onRestoreTrashedSession}
          onPurge={onPurgeTrashedSession}
          onPurgeAll={onPurgeAllTrashedSessions}
          onClose={closeHistory}
        />
      )}

      {settingsTarget !== null && (
        <SettingsPanel
          initialTab={settingsTarget}
          initialPayload={settingsPayload ?? undefined}
          onClose={() => {
            setSettingsTarget(null);
            setSettingsPayload(null);
          }}
          onChanged={() => {
            void refreshMeta();
            void reloadSidebarImConnections().catch((e) => console.warn("bot sidebar refresh failed", e));
            void app.Settings()
              .then(applyDesktopPreferences)
              .catch((e) => console.warn("desktop preferences refresh failed", e));
          }}
        />
      )}



      <BranchTree


        open={branchesOpen}


        onClose={() => setBranchesOpen(false)}


        onSwitched={() => { void syncActiveTab(); }}


      />

      <AgentDashboard

        open={agentsOpen}

        onClose={() => setAgentsOpen(false)}

        tabs={tabMetas}

        activeTabId={activeTabId}

        onSwitch={(id: string) => { void switchTab(id); }}

        onStop={(id: string) => { void app.CancelTab(id); }}

      />
      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        items={paletteItems}
        placeholder={t("palette.placeholder")}
        emptyText={t("palette.empty")}
      />

      <ShortcutsCheatsheet
        open={shortcutsOpen}
        platform={desktopPlatform}
        onClose={() => setShortcutsOpen(false)}
        t={t}
      />

      {startupSplashVisible && (
        <StartupSplash hold={startupSplashHold} onDone={() => setStartupSplashVisible(false)} />
      )}

      {needsOnboarding && <OnboardingOverlay onComplete={() => setNeedsOnboarding(false)} />}
      {remoteWizardOpen && <RemoteConnectWizard onClose={() => setRemoteWizardOpen(false)} />}

      <AttachmentViewer />
    </div>
    </ShellExpandProvider>
  );
}
