// ProjectTree is the sidebar replacement for the flat recent-sessions list.
// It shows a tree of projects (each with expandable topics) plus a Global
// section. Clicking a topic opens its tab; "+" next to a project creates a
// new topic.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties, DragEvent as ReactDragEvent, KeyboardEvent as ReactKeyboardEvent, MouseEvent as ReactMouseEvent } from "react";
import { Archive, ClipboardCopy, Pencil, Plus, Folder, FolderPlus, Search, BriefcaseBusiness, Copy, FileDown, FileImage, FileJson, FileText, FolderOpen, GitBranch, XCircle, History, Check, ListCollapse, ListRestart, MessageSquare, Users, Loader2,
  Server,
} from "lucide-react";
import { asArray } from "../lib/array";
import { compactSessionAge } from "./SidebarSessions";
import { app } from "../lib/bridge";
import type { ProjectNode, ProjectTopicStatus } from "../lib/types";
import { useT, type DictKey, type Translator } from "../lib/i18n";
import { getScopedItem, setScopedItem } from "../lib/profileScopedStorage";
import { PROJECT_COLOR_OPTIONS, projectColorValue } from "../lib/projectColors";
import { ContextMenu, contextMenuPointFromEvent, type ContextMenuItem, type ContextMenuPoint } from "./ContextMenu";
import { Tooltip } from "./Tooltip";

interface ProjectTreeProps {
  activeScope?: string;
  activeWorkspaceRoot?: string;
  activeTopicId?: string;
  // Controlled search (ui-redesign §4-B4): the sidebar hoists the search box
  // above the "最近" sessions section and drives the tree through these. When
  // query is undefined the tree keeps its own internal state (old contract).
  query?: string;
  onQueryChange?: (q: string) => void;
  hideSearch?: boolean;
  // The active product profile ("dev"|"cowork"); scopes ListProjectTree/
  // CreateTopic/ReorderProjects to the profile so each profile shows its own
  // project/topic set.
  profile?: string;
  imTopicSources?: Record<string, ProjectTreeImTopicSource>;
  onOpenTopic: (
    scope: string,
    workspaceRoot: string,
    topicId: string,
    remote?: { kind: string; target: string; user?: string; tls?: boolean } | null,
    sessionPath?: string,
    title?: string,
  ) => Promise<void> | void;
  onOpenExpertSession?: (teamId: string, teamName: string) => Promise<void> | void;
  onOpenProjectHistory: (scope: "project", workspaceRoot: string) => Promise<void> | void;
  onAddProject: () => Promise<void>;
  onOpenRemote?: () => void;
  onCreateTopic?: (scope: string, workspaceRoot: string) => Promise<void> | void;
  onRenameTopic?: (topicId: string, title: string) => Promise<void> | void;
  // Session actions relocated from the topicbar into the topic context menu
  // (2026-08-18). Copy/export target the ACTIVE session's row (they operate on
  // the loaded transcript); the changed-dock toggle is global.
  onCopyActiveSession?: () => void;
  onExportActiveSession?: (format: "markdown" | "json" | "pdf" | "image" | "html") => void;
  onOpenChangedDock?: () => void;
  // Copies the topic's on-disk session (.jsonl) path to the clipboard — the
  // backend resolves topicId → path via ListSessions.
  onCopyTopicPath?: (topicId: string) => Promise<void> | void;
  onTopicsChanged?: () => Promise<void> | void;
  refreshSignal?: number;
  // Called when the tree data is loaded/refreshed, passing the set of topic IDs
  // currently visible in the project tree. Used to dedupe the "Recent" list.
  onTopicIdsChanged?: (topicIds: Set<string>) => void;
}

type ProjectTreeImTopicSource = {
  platform?: string;
  label: string;
  title?: string;
  remoteId?: string;
};

// remoteKindLabel shortens a remote connection kind for the project-row badge.
function remoteKindLabel(kind: string): string {
  switch ((kind || "").toLowerCase()) {
    case "wsl": return "WSL";
    case "docker": return "Docker";
    case "ssh": return "SSH";
    case "server": return "Srv";
    default: return kind;
  }
}

function projectNodeKey(node: ProjectNode, depth: number): string {
  return node.key || `${node.kind}-${node.root ?? ""}-${node.topicId ?? ""}-${depth}`;
}

function topicIsActive(node: ProjectNode, activeScope?: string, activeWorkspaceRoot?: string, activeTopicId?: string): boolean {
  if (node.kind !== "topic") return false;
  return activeTopicId === node.topicId && activeScope === "project" && activeWorkspaceRoot === node.root;
}

const TOPIC_SEEN_KEY = "hiq.topicSeen";

function topicMetaLine(node: ProjectNode, t: Translator): string {
  // Sidebar-parity meta (2026-08-18): compact age only — the turns badge was
  // noise; the Recent list next to the tree already shows this format.
  const activityAt = node.lastActivityAt || node.createdAt || 0;
  if (!activityAt) return t("projectTree.justNow");
  return compactSessionAge(activityAt);
}

const topicStatusLabels: Record<ProjectTopicStatus, DictKey> = {
  thinking: "projectTree.status.thinking",
  streaming: "projectTree.status.streaming",
  waiting_confirmation: "projectTree.status.waitingConfirmation",
  paused: "projectTree.status.paused",
  error: "projectTree.status.error",
};

function normalizeTopicStatus(status?: string): ProjectTopicStatus | "" {
  if (!status) return "";
  if (status === "thinking" || status === "streaming" || status === "waiting_confirmation" || status === "paused" || status === "error") {
    return status;
  }
  return "";
}

function topicStatus(node: ProjectNode): ProjectTopicStatus | "" {
  return normalizeTopicStatus(node.status) || (node.running ? "streaming" : "");
}

function topicStatusLabel(node: ProjectNode, t: Translator): string {
  const status = topicStatus(node);
  return status ? t(topicStatusLabels[status]) : "";
}


type ProjectDropPosition = "before" | "after";

function projectOrderKey(node: ProjectNode): string {
  if (node.kind === "project" && node.root) return node.root;
  return "";
}

function projectRoots(nodes: ProjectNode[]): string[] {
  return nodes
    .map(projectOrderKey)
    .filter((key) => key !== "");
}


function reorderedProjectRoots(nodes: ProjectNode[], draggedRoot: string, targetRoot: string, position: ProjectDropPosition): string[] {
  const roots = projectRoots(nodes);
  if (draggedRoot === targetRoot || !roots.includes(draggedRoot) || !roots.includes(targetRoot)) return roots;
  const next = roots.filter((root) => root !== draggedRoot);
  const targetIndex = next.indexOf(targetRoot);
  if (targetIndex < 0) return roots;
  next.splice(position === "before" ? targetIndex : targetIndex + 1, 0, draggedRoot);
  return next;
}

function applyProjectOrder(nodes: ProjectNode[], roots: string[]): ProjectNode[] {
  const projectEntries = nodes
    .map((node): [string, ProjectNode] => [projectOrderKey(node), node])
    .filter(([key]) => key !== "");
  const byRoot = new Map<string, ProjectNode>(projectEntries);
  const orderedProjects = roots.map((root) => byRoot.get(root)).filter((node): node is ProjectNode => Boolean(node));
  const orderedKeys = new Set(roots);
  const nonProjects = nodes.filter((node) => !orderedKeys.has(projectOrderKey(node)));
  return [...nonProjects, ...orderedProjects];
}

// Global rows use the same project tree recipe; the fallback supplies their non-workspace accent.
function projectAccentStyle(color?: string, fallbackValue?: string): CSSProperties | undefined {
  const value = projectColorValue(color) || fallbackValue;
  if (!value) return undefined;
  return { "--project-accent": value } as CSSProperties;
}

function colorMenuLabel(label: string, color?: string, active = false) {
  const value = projectColorValue(color);
  return (
    <span className="project-tree__color-option">
      <span
        className="project-tree__color-swatch"
        style={value ? ({ "--project-accent": value } as CSSProperties) : undefined}
        aria-hidden="true"
      />
      <span>{label}</span>
      {active && <Check className="project-tree__color-check" size={12} />}
    </span>
  );
}

function revealLabelKey(platform: string): "projectTree.revealInFinder" | "projectTree.revealInExplorer" | "projectTree.revealInFileManager" {
  if (platform === "darwin") return "projectTree.revealInFinder";
  if (platform === "windows") return "projectTree.revealInExplorer";
  return "projectTree.revealInFileManager";
}

function projectColorLabel(t: Translator, color?: string): string {
  switch (color) {
    case "red": return t("projectTree.colorRed");
    case "orange": return t("projectTree.colorOrange");
    case "amber": return t("projectTree.colorAmber");
    case "green": return t("projectTree.colorGreen");
    case "teal": return t("projectTree.colorTeal");
    case "blue": return t("projectTree.colorBlue");
    case "purple": return t("projectTree.colorPurple");
    case "pink": return t("projectTree.colorPink");
    default: return t("projectTree.colorDefault");
  }
}

export function ProjectTree({
  activeScope,
  activeWorkspaceRoot,
  activeTopicId,
  query: queryProp,
  onQueryChange,
  hideSearch = false,
  profile = "",
  imTopicSources = {},
  onOpenTopic,
  onOpenExpertSession,
  onOpenProjectHistory,
  onAddProject,
  onOpenRemote,
  onCreateTopic,
  onRenameTopic,
  onCopyActiveSession,
  onExportActiveSession,
  onOpenChangedDock,
  onCopyTopicPath,
  onTopicsChanged,
  refreshSignal,
  onTopicIdsChanged,
}: ProjectTreeProps) {
  const t = useT();
  const [tree, setTree] = useState<ProjectNode[]>([]);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [workspaceExpanded, setWorkspaceExpanded] = useState(() => {
    try {
      const saved = localStorage.getItem(`hiq.projectTree.workspaceExpanded.${profile}`);
      return saved ? saved === "true" : true;
    } catch {
      return true;
    }
  });
  const toggleWorkspace = useCallback(() => {
    setWorkspaceExpanded(v => {
      const next = !v;
      try {
        localStorage.setItem(`hiq.projectTree.workspaceExpanded.${profile}`, String(next));
      } catch {}
      return next;
    });
  }, [profile]);
  const [manuallyCollapsed, setManuallyCollapsed] = useState<Set<string>>(new Set());
  const [creatingProject, setCreatingProject] = useState<string | null>(null);
  // Unseen-activity markers (2026-08-18): a topic glows blue when it produced
  // output after we last opened it; opening clears it. Seeded once so existing
  // sessions do not all light up on the first run of this feature. Storage is
  // profile-scoped — topics belong to a mode, and the visible tree's markers
  // must not be cleared by a hidden instance of another profile.
  const [topicSeen, setTopicSeen] = useState<Record<string, number>>(() => {
    try { return JSON.parse(getScopedItem(TOPIC_SEEN_KEY) || "{}"); } catch { return {}; }
  });
  const seededSeen = useRef(false);
  useEffect(() => {
    if (seededSeen.current || tree.length === 0) return;
    seededSeen.current = true;
    if (getScopedItem(TOPIC_SEEN_KEY)) return;
    const stamp = Date.now();
    const next: Record<string, number> = {};
    const walkAll = (nodes: ProjectNode[]) => {
      for (const n of nodes) {
        if (n.topicId) next[n.topicId] = stamp;
        if (n.children) walkAll(asArray(n.children));
      }
    };
    walkAll(tree);
    setTopicSeen(next);
    try { setScopedItem(TOPIC_SEEN_KEY, JSON.stringify(next)); } catch { /* quota */ }
  }, [tree]);
  const markTopicSeen = useCallback((topicId: string) => {
    if (!topicId) return;
    setTopicSeen((cur) => {
      const now = Date.now();
      if ((cur[topicId] ?? 0) > now - 2000) return cur;
      const next = { ...cur, [topicId]: now };
      try { setScopedItem(TOPIC_SEEN_KEY, JSON.stringify(next)); } catch { /* quota */ }
      return next;
    });
  }, []);

  // Search is hoistable: the sidebar renders the search box itself (above the
  // Recent sessions section) and drives this via query/onQueryChange.
  const [innerQuery, setInnerQuery] = useState("");
  const query = queryProp ?? innerQuery;
  const setQuery = (next: string) => {
    setInnerQuery(next);
    onQueryChange?.(next);
  };
  const [editingTopic, setEditingTopic] = useState<string | null>(null);
  const [topicDraft, setTopicDraft] = useState("");
  const [menuTopic, setMenuTopic] = useState<string | null>(null);
  const [menuProject, setMenuProject] = useState<{ key: string; root: string; path: string; scope: "project"; label: string } | null>(null);
  const [menuPoint, setMenuPoint] = useState<ContextMenuPoint | null>(null);
  const [editingProject, setEditingProject] = useState<{ key: string; root: string } | null>(null);
  const [projectDraft, setProjectDraft] = useState("");
  const [addingProject, setAddingProject] = useState(false);
  const [confirmAction, setConfirmAction] = useState<{ topicId: string; action: "trash" } | null>(null);
  const [confirmRemoveProject, setConfirmRemoveProject] = useState<string | null>(null);
  const [dragProjectRoot, setDragProjectRoot] = useState<string | null>(null);
  const [dropProject, setDropProject] = useState<{ root: string; position: ProjectDropPosition } | null>(null);
  const [platform, setPlatform] = useState("");
  const creatingRef = useRef(false);
  const manuallyCollapsedRef = useRef(manuallyCollapsed);

  const closeMenu = useCallback(() => {
    setMenuTopic(null);
    setMenuProject(null);
    setMenuPoint(null);
    setConfirmAction(null);
    setConfirmRemoveProject(null);
  }, []);

  const updateManuallyCollapsed = useCallback((updater: (prev: Set<string>) => Set<string>) => {
    setManuallyCollapsed((prev) => {
      const next = updater(prev);
      manuallyCollapsedRef.current = next;
      return next;
    });
  }, []);

  const refresh = useCallback(async () => {
    try {
      const nodes = await app.ListProjectTree(profile);
      const list = asArray(nodes);

      setTree(list);
      setExpanded((prev) => {
        const next = new Set(prev);
        const collapsed = manuallyCollapsedRef.current;
        for (const collapsedKey of collapsed) next.delete(collapsedKey);
        for (const node of list) {
          if (node?.key && !collapsed.has(node.key)) next.add(node.key);
        }
        return next;
      });
      // Extract topic IDs for deduplication with the "Recent" list.
      if (onTopicIdsChanged) {
        const ids = new Set<string>();
        const walk = (nodes: ProjectNode[]) => {
          for (const n of nodes) {
            if (n.topicId) ids.add(n.topicId);
            if (n.children) walk(asArray(n.children));
          }
        };
        walk(list);
        onTopicIdsChanged(ids);
      }
    } catch {
      /* bridge unavailable */
    }
  }, [profile, t, onTopicIdsChanged]);

  useEffect(() => {
    manuallyCollapsedRef.current = manuallyCollapsed;
  }, [manuallyCollapsed]);

  useEffect(() => {
    void refresh();
  }, [refresh, refreshSignal]);

  useEffect(() => {
    let cancelled = false;
    void app.Platform().then((value) => {
      if (!cancelled) setPlatform(value);
    }).catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  const toggleExpand = (key: string) => {
    const willCollapse = expanded.has(key);
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
    updateManuallyCollapsed((prev) => {
      const next = new Set(prev);
      if (willCollapse) next.add(key);
      else next.delete(key);
      return next;
    });
  };

  const handleAddProject = async () => {
    if (addingProject) return;
    setAddingProject(true);
    try {
      await onAddProject();
      await refresh();
    } finally {
      setAddingProject(false);
    }
  };

  const handleCreateTopic = async (scope: string, workspaceRoot: string, key: string) => {
    if (creatingRef.current) return;
    creatingRef.current = true;
    setCreatingProject(key);
    setMenuProject(null);
    setMenuPoint(null);
    setExpanded((prev) => {
      const next = new Set(prev);
      next.add(key);
      return next;
    });
    updateManuallyCollapsed((prev) => {
      if (!prev.has(key)) return prev;
      const next = new Set(prev);
      next.delete(key);
      return next;
    });
    try {
      if (onCreateTopic) {
        await onCreateTopic(scope, workspaceRoot);
        await refresh();
        await onTopicsChanged?.();
        return;
      }
      const topic = await app.CreateTopic(scope, workspaceRoot, profile, "");
      await refresh();
      await onTopicsChanged?.();
      await onOpenTopic(scope, workspaceRoot, topic.id);
    } catch {
      /* ignore */
    } finally {
      creatingRef.current = false;
      setCreatingProject(null);
    }
  };

  const startRenameTopic = (node: ProjectNode, label: string) => {
    setMenuTopic(null);
    setMenuProject(null);
    setMenuPoint(null);
    setConfirmAction(null);
    setEditingTopic(node.topicId ?? null);
    setTopicDraft(label);
  };

  const startRenameProject = (key: string, root: string, label: string) => {
    setMenuProject(null);
    setMenuTopic(null);
    setMenuPoint(null);
    setConfirmRemoveProject(null);
    setEditingProject({ key, root });
    setProjectDraft(label);
  };

  const commitRenameTopic = async (topicId: string) => {
    const title = topicDraft.trim();
    setEditingTopic(null);
    if (!title) return;
    try {
      if (onRenameTopic) await onRenameTopic(topicId, title);
      else await app.RenameTopic(topicId, title);
      await refresh();
      if (!onRenameTopic) await onTopicsChanged?.();
    } catch {
      /* ignore */
    }
  };

  const commitRenameProject = async (root: string) => {
    const title = projectDraft.trim();
    setEditingProject(null);
    if (!title) return;
    try {
      await app.RenameProject(root, title);
      await refresh();
    } catch {
      /* ignore */
    }
  };

  const trashTopic = async (topicId: string) => {
    try {
      await app.TrashTopic(topicId);
      setMenuTopic(null);
      setMenuPoint(null);
      setConfirmAction(null);
      await refresh();
      await onTopicsChanged?.();
    } catch {
      /* ignore */
    }
  };

  // trashExpertSession moves a team's expert-session conversation history to
  // trash (mirrors trashTopic for normal sessions). The backend closes any open
  // expert tab for the team, cancels any in-flight run, and moves the .jsonl/
  // .meta/.ckpt files to local trash. The team definition itself is preserved.
  const trashExpertSession = async (teamId: string) => {
    try {
      await app.TrashExpertSession(teamId);
      setMenuTopic(null);
      setMenuPoint(null);
      setConfirmAction(null);
      await refresh();
      await onTopicsChanged?.();
    } catch {
      /* ignore */
    }
  };

  const copyProjectPath = async (path: string) => {
    if (!path) return;
    try {
      await navigator.clipboard?.writeText(path);
    } catch {
      /* ignore */
    }
  };

  const removeProject = async (path: string) => {
    if (!path) return;
    try {
      await app.RemoveWorkspace(path);
      setMenuProject(null);
      setMenuPoint(null);
      setConfirmRemoveProject(null);
      await refresh();
    } catch {
      /* ignore */
    }
  };

  const setProjectColor = async (path: string, color: string) => {
    try {
      await app.SetProjectColor(path, color);
      setMenuProject(null);
      setMenuPoint(null);
      await refresh();
      await onTopicsChanged?.();
    } catch {
      /* ignore */
    }
  };

  const visibleTree = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return tree;
    const matches = (node: ProjectNode) =>
      [node.label, node.root, node.topicId].some((value) => (value ?? "").toLowerCase().includes(q));
    const filterNode = (node: ProjectNode): ProjectNode | null => {
      const children = asArray(node.children)
        .map(filterNode)
        .filter((child): child is ProjectNode => child !== null);
      if (matches(node) || children.length > 0) return { ...node, children };
      return null;
    };
    return tree
      .map(filterNode)
      .filter((node): node is ProjectNode => node !== null);
  }, [query, tree]);

  const projectDragEnabled = query.trim() === "";

  const commitProjectReorder = useCallback(async (draggedRoot: string, targetRoot: string, position: ProjectDropPosition) => {
    const nextRoots = reorderedProjectRoots(tree, draggedRoot, targetRoot, position);
    const currentRoots = projectRoots(tree);
    if (nextRoots.join("\n") === currentRoots.join("\n")) return;
    setTree((current) => applyProjectOrder(current, nextRoots));
    try {
      await app.ReorderProjects(profile, nextRoots);
      await refresh();
      await onTopicsChanged?.();
    } catch {
      await refresh();
    }
  }, [onTopicsChanged, profile, refresh, tree]);

  const clearProjectDrag = useCallback(() => {
    setDragProjectRoot(null);
    setDropProject(null);
  }, []);

  useEffect(() => {
    if (!dragProjectRoot) return;
    window.addEventListener("dragend", clearProjectDrag);
    window.addEventListener("drop", clearProjectDrag);
    window.addEventListener("blur", clearProjectDrag);
    return () => {
      window.removeEventListener("dragend", clearProjectDrag);
      window.removeEventListener("drop", clearProjectDrag);
      window.removeEventListener("blur", clearProjectDrag);
    };
  }, [clearProjectDrag, dragProjectRoot]);

  const activeAncestorKeys = useMemo(() => {
    const walk = (nodes: ProjectNode[], ancestors: string[]): string[] | null => {
      for (const node of nodes) {
        if (!node) continue;
        if (topicIsActive(node, activeScope, activeWorkspaceRoot, activeTopicId)) return ancestors;
        const children = asArray(node.children);
        if (children.length > 0) {
          const next = walk(children, [...ancestors, projectNodeKey(node, ancestors.length)]);
          if (next) return next;
        }
      }
      return null;
    };
    return walk(tree, []) ?? [];
  }, [activeScope, activeTopicId, activeWorkspaceRoot, tree]);

  useEffect(() => {
    if (activeAncestorKeys.length === 0) return;
    setExpanded((prev) => {
      let changed = false;
      const next = new Set(prev);
      for (const key of activeAncestorKeys) {
        if (manuallyCollapsed.has(key) || next.has(key)) continue;
        next.add(key);
        changed = true;
      }
      return changed ? next : prev;
    });
  }, [activeAncestorKeys, manuallyCollapsed]);

  const renderNode = (node: ProjectNode | null | undefined, depth: number) => {
    if (!node) return null;
    const key = projectNodeKey(node, depth);
    const children = asArray(node.children);
    // Manual collapse is authoritative: even if some refresh/auto-expand path
    // re-adds the key to `expanded`, a folder the user collapsed stays closed.
    const isExpanded = query.trim() ? true : expanded.has(key) && !manuallyCollapsed.has(key);
    const hasChildren = children.length > 0;

    if (node.kind === "expert_topic" && node.expertTeamId) {
      const label = node.expertTeamName || node.label || node.expertTeamId;
      const meta = topicMetaLine(node, t);
      const status = topicStatus(node);
      const statusLabel = topicStatusLabel(node, t);
      const title = [label, statusLabel, meta].filter(Boolean).join(" · ");
      const teamId = node.expertTeamId;
      const expertMenuOpen = menuTopic === `expert:${teamId}`;
      const openExpertMenu = (event: ReactMouseEvent<HTMLElement> | ReactKeyboardEvent<HTMLElement>) => {
        event.preventDefault();
        event.stopPropagation();
        setMenuProject(null);
        setConfirmRemoveProject(null);
        setMenuPoint(contextMenuPointFromEvent(event));
        setMenuTopic(`expert:${teamId}`);
        setConfirmAction(null);
      };
      const expertMenuItems: ContextMenuItem[] = [
        {
          key: "trash",
          icon: <Archive size={13} />,
          label: confirmAction?.topicId === teamId && confirmAction.action === "trash" ? t("history.confirmMoveToTrash") : t("history.moveToTrash"),
          danger: true,
          onSelect: () => {
            if (confirmAction?.topicId === teamId && confirmAction.action === "trash") void trashExpertSession(teamId);
            else setConfirmAction({ topicId: teamId, action: "trash" });
          },
        },
      ];
      return (
        <div
          key={node.key}
          className={`project-tree__topic project-tree__topic--expert${expertMenuOpen ? " project-tree__topic--menu-open" : ""}`}
          title={title}
          onContextMenu={openExpertMenu}
          onClick={() => { if (onOpenExpertSession) void onOpenExpertSession(node.expertTeamId!, node.expertTeamName ?? label); }}
        >
          <span className="project-tree__topic-icon"><Users size={14} /></span>
          <span className="project-tree__topic-label">{label}</span>
          {node.open && <span className="project-tree__topic-open-dot" />}
          {status && <span className={`project-tree__topic-status project-tree__topic-status--${status}`} />}
          <ContextMenu
            open={expertMenuOpen}
            point={menuPoint}
            items={expertMenuItems}
            minWidth={178}
            ariaLabel={t("projectTree.topicActions")}
            onClose={closeMenu}
          />
        </div>
      );
    }

    if (node.kind === "topic") {
      const scope = "project";
      const scopeClass = " project-tree__topic--project";
      const accentStyle = projectAccentStyle(node.projectColor);
      const active = topicIsActive(node, activeScope, activeWorkspaceRoot, activeTopicId);
      const label = (node.label || node.topicId || "Untitled").replace(/^●\s*/, "");
      const meta = topicMetaLine(node, t);
      const status = topicStatus(node);
      const statusLabel = topicStatusLabel(node, t);
      const topicId = node.topicId ?? "";
      const imSource = topicId ? imTopicSources[topicId] : undefined;
      const imSourceLabel = imSource?.label || "";
      const imSourceTitle = imSourceLabel ? t("msg.fromIm", { source: imSourceLabel }) : "";
      const imSourcePlatform = (imSource?.platform || "im").replace(/[^a-z0-9_-]/gi, "").toLowerCase() || "im";
      const title = [label, imSourceTitle, statusLabel, meta].filter(Boolean).join(" · ");
      const topicMenuOpen = menuTopic === topicId;
      const openTopicMenu = (event: ReactMouseEvent<HTMLElement> | ReactKeyboardEvent<HTMLElement>) => {
        event.preventDefault();
        event.stopPropagation();
        setMenuProject(null);
        setConfirmRemoveProject(null);
        setMenuPoint(contextMenuPointFromEvent(event));
        setMenuTopic(topicId);
        setConfirmAction(null);
      };
      const topicMenuItems: ContextMenuItem[] = [
        ...(onOpenChangedDock
          ? [
              {
                key: "changed-dock",
                icon: <GitBranch size={13} />,
                label: t("workspace.changedTab"),
                onSelect: onOpenChangedDock,
              },
            ]
          : []),
        ...(active && onCopyActiveSession
          ? [
              {
                key: "copy-session",
                icon: <Copy size={13} />,
                label: t("topicBar.copyAll"),
                onSelect: onCopyActiveSession,
              },
            ]
          : []),
        ...(active && onExportActiveSession
          ? ([
              { type: "separator", key: "sep-export" },
              {
                key: "export-markdown",
                icon: <FileText size={13} />,
                label: t("topicBar.exportMarkdown"),
                onSelect: () => onExportActiveSession("markdown"),
              },
              {
                key: "export-json",
                icon: <FileJson size={13} />,
                label: t("topicBar.exportJson"),
                onSelect: () => onExportActiveSession("json"),
              },
              {
                key: "export-pdf",
                icon: <FileDown size={13} />,
                label: t("topicBar.exportPdf"),
                onSelect: () => onExportActiveSession("pdf"),
              },
              {
                key: "export-image",
                icon: <FileImage size={13} />,
                label: t("topicBar.exportImage"),
                onSelect: () => onExportActiveSession("image"),
              },
              {
                key: "export-html",
                icon: <FileImage size={13} />,
                label: t("topicBar.exportHtml"),
                onSelect: () => onExportActiveSession("html"),
              },
            ] as ContextMenuItem[])
          : []),
        { type: "separator", key: "sep-topic" },
        ...(onCopyTopicPath
          ? [
              {
                key: "copy-topic-path",
                icon: <ClipboardCopy size={13} />,
                label: t("projectTree.copyTopicPath"),
                onSelect: () => void onCopyTopicPath(topicId),
              },
            ]
          : []),
        {
          key: "rename",
          icon: <Pencil size={13} />,
          label: t("projectTree.renameTopic"),
          onSelect: () => startRenameTopic(node, label),
        },
        {
          key: "trash",
          icon: <Archive size={13} />,
          label: confirmAction?.topicId === topicId && confirmAction.action === "trash" ? t("history.confirmMoveToTrash") : t("history.moveToTrash"),
          danger: true,
          onSelect: () => {
            if (confirmAction?.topicId === topicId && confirmAction.action === "trash") void trashTopic(topicId);
            else setConfirmAction({ topicId, action: "trash" });
          },
        },
      ];
      if (editingTopic === topicId) {
        return (
          <div
            key={key}
            className={`project-tree__topic project-tree__topic--editing${active ? " project-tree__topic--active" : ""}${imSource ? " project-tree__topic--im-source" : ""}`}
            style={{ paddingLeft: 14 + depth * 16 }}
          >
            <input
              autoFocus
              className="project-tree__topic-input"
              value={topicDraft}
              onChange={(event) => setTopicDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") void commitRenameTopic(topicId);
                if (event.key === "Escape") setEditingTopic(null);
              }}
              onBlur={() => void commitRenameTopic(topicId)}
            />
          </div>
        );
      }
      return (
        <div
          key={key}
          className={`project-tree__topic${scopeClass}${active ? " project-tree__topic--active" : ""}${node.running ? " project-tree__topic--running" : ""}${status ? ` project-tree__topic--status-${status}` : ""}${topicMenuOpen ? " project-tree__topic--menu-open" : ""}${meta ? " project-tree__topic--has-meta" : ""}${imSource ? " project-tree__topic--im-source" : ""}`}
          style={accentStyle}
          onContextMenu={openTopicMenu}
        >
          <button
            type="button"
            className="project-tree__topic-main"
            title={title}
            style={{ paddingLeft: 14 + depth * 16 }}
            onClick={() => {
              markTopicSeen(topicId);
              onOpenTopic(scope, node.root ?? "", topicId);
            }}
            onKeyDown={(event) => {
              if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) {
                openTopicMenu(event);
              }
            }}
          >
            <span className="project-tree__topic-copy">
              <span className="project-tree__topic-heading">
                <span className="project-tree__topic-label">{label}</span>
                {imSource && (
                  <span
                    className={`project-tree__topic-im project-tree__topic-im--${imSourcePlatform}`}
                    title={imSourceTitle}
                    aria-label={imSourceTitle}
                  >
                    <MessageSquare size={11} />
                    <span>{imSourceLabel}</span>
                  </span>
                )}
                {status && status !== "error" ? (
                  <span
                    className={`project-tree__topic-status project-tree__topic-status--${status}`}
                    title={statusLabel}
                    aria-label={statusLabel}
                  >
                    <Loader2 size={11} className="project-tree__topic-spinner" />
                  </span>
                ) : statusLabel ? (
                  <span className={`project-tree__topic-status project-tree__topic-status--${status}`}>{statusLabel}</span>
                ) : (
                  (node.lastActivityAt || 0) > (topicSeen[topicId] ?? 0) && !active && (
                    <span className="project-tree__topic-unseen" aria-hidden="true" />
                  )
                )}
              </span>
              {meta && (
                <span className="project-tree__topic-meta">
                  <span className="project-tree__topic-meta-text">{meta}</span>
                </span>
              )}
            </span>
          </button>
          <ContextMenu
            open={topicMenuOpen}
            point={menuPoint}
            items={topicMenuItems}
            minWidth={178}
            ariaLabel={t("projectTree.topicActions")}
            onClose={closeMenu}
          />
        </div>
      );
    }

    const scope = "project";
    const scopeClass = " project-tree__folder--project";
    const accentStyle = projectAccentStyle(node.projectColor);
    const projectRoot = node.root ?? "";
    const projectDragKey = projectRoot;
    const projectPath = node.root ?? "";
    const colorTargetRoot = projectPath;
    const projectLabel = node.label || "Untitled";
    const projectActive = activeScope === "project" && activeWorkspaceRoot === node.root;
    const draggableProject = projectDragEnabled && depth === 0 && Boolean(projectDragKey) && editingProject?.key !== key;
    const projectDropPosition = dropProject?.root === projectDragKey ? dropProject.position : null;
    const handleProjectDragStart = (event: ReactDragEvent<HTMLElement>) => {
      if (!draggableProject) return;
      const target = event.target;
      if (target instanceof Element && target.closest(".project-tree__action-slot")) {
        event.preventDefault();
        return;
      }
      event.dataTransfer.effectAllowed = "move";
      event.dataTransfer.setData("text/plain", projectDragKey);
      setDragProjectRoot(projectDragKey);
      setDropProject(null);
    };
    const handleProjectDragOver = (event: ReactDragEvent<HTMLDivElement>) => {
      if (!draggableProject || !dragProjectRoot || dragProjectRoot === projectDragKey) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
      const rect = event.currentTarget.getBoundingClientRect();
      const position: ProjectDropPosition = event.clientY < rect.top + rect.height / 2 ? "before" : "after";
      setDropProject((current) => {
        if (current?.root === projectDragKey && current.position === position) return current;
        return { root: projectDragKey, position };
      });
    };
    const handleProjectDrop = (event: ReactDragEvent<HTMLDivElement>) => {
      if (!draggableProject) return;
      const draggedRoot = dragProjectRoot || event.dataTransfer.getData("text/plain");
      const position = dropProject?.root === projectDragKey ? dropProject.position : "after";
      event.preventDefault();
      clearProjectDrag();
      if (draggedRoot && draggedRoot !== projectDragKey) void commitProjectReorder(draggedRoot, projectDragKey, position);
    };
    const openProjectMenu = (event: ReactMouseEvent<HTMLElement> | ReactKeyboardEvent<HTMLElement>) => {
      event.preventDefault();
      event.stopPropagation();
      setMenuTopic(null);
      setConfirmAction(null);
      setMenuPoint(contextMenuPointFromEvent(event));
      setMenuProject({ key, root: projectRoot, path: projectPath, scope, label: projectLabel });
      setConfirmRemoveProject(null);
    };
    const projectMenuItems: ContextMenuItem[] = [
      {
        key: "new-session",
        icon: <Plus size={13} />,
        label: t("projectTree.newTopic"),
        onSelect: () => {
          void handleCreateTopic(scope, projectRoot, key);
        },
      },
      {
        key: "project-history",
        icon: <History size={13} />,
        label: t("projectTree.projectHistory"),
        onSelect: () => {
          closeMenu();
          void onOpenProjectHistory(scope, projectRoot);
        },
      },
      {
        key: "rename",
        icon: <Pencil size={13} />,
        label: t("projectTree.renameProject"),
        onSelect: () => startRenameProject(key, projectRoot, projectLabel),
      },
      { type: "separator" as const, key: "color-separator" },
      ...PROJECT_COLOR_OPTIONS.map((option): ContextMenuItem => ({
        key: `color-${option.key || "default"}`,
        label: colorMenuLabel(projectColorLabel(t, option.key), option.key, (node.projectColor || "") === option.key),
        onSelect: () => {
          void setProjectColor(colorTargetRoot, option.key);
        },
      })),
      { type: "separator" as const, key: "path-separator" },
      {
        key: "reveal",
        icon: <FolderOpen size={13} />,
        label: t(revealLabelKey(platform)),
        disabled: !projectPath,
        onSelect: () => {
          void app.RevealPath(projectPath);
          closeMenu();
        },
      },
      {
        key: "copy-path",
        icon: <Copy size={13} />,
        label: t("projectTree.copyPath"),
        disabled: !projectPath,
        onSelect: () => {
          void copyProjectPath(projectPath);
          closeMenu();
        },
      },
      ...(scope === "project"
        ? [
            { type: "separator" as const, key: "remove-separator" },
            {
              key: "remove",
              icon: <XCircle size={13} />,
              label: confirmRemoveProject === key ? t("projectTree.confirmRemoveProject") : t("projectTree.removeProject"),
              danger: true,
              onSelect: () => {
                if (confirmRemoveProject === key) void removeProject(projectPath);
                else setConfirmRemoveProject(key);
              },
            },
          ]
        : []),
    ];

    if (editingProject?.key === key) {
      return (
        <div key={key}>
          <div
            className={`project-tree__folder project-tree__folder--editing${projectActive ? " project-tree__folder--active" : ""}`}
            style={{ paddingLeft: 8 + depth * 16 }}
          >
            <input
              autoFocus
              className="project-tree__folder-input"
              value={projectDraft}
              onChange={(event) => setProjectDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") void commitRenameProject(projectRoot);
                if (event.key === "Escape") setEditingProject(null);
              }}
              onBlur={() => void commitRenameProject(projectRoot)}
            />
          </div>
          {hasChildren && (
            <div className={`project-tree__children${isExpanded ? " project-tree__children--expanded" : ""}`}>
              <div className="project-tree__children-inner">
                {children.map((child) => renderNode(child, depth + 1))}
              </div>
            </div>
          )}
        </div>
      );
    }

    return (
      <div key={key}>
        <div
          className={`project-tree__folder${scopeClass}${draggableProject ? " project-tree__folder--draggable" : ""}${projectActive ? " project-tree__folder--active" : ""}${menuProject?.key === key ? " project-tree__folder--menu-open" : ""}${dragProjectRoot === projectDragKey ? " project-tree__folder--dragging" : ""}${projectDropPosition ? ` project-tree__folder--drop-${projectDropPosition}` : ""}`}
          style={accentStyle}
          draggable={draggableProject}
          aria-grabbed={draggableProject ? dragProjectRoot === projectRoot : undefined}
          onDragStart={handleProjectDragStart}
          onDragOver={handleProjectDragOver}
          onDragLeave={(event) => {
            if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDropProject(null);
          }}
          onDrop={handleProjectDrop}
          onDragEnd={clearProjectDrag}
          onContextMenu={openProjectMenu}
        >
          <button
            type="button"
            className="project-tree__folder-main"
            style={{ paddingLeft: 8 + depth * 16 }}
            onClick={() => {
              if (hasChildren) toggleExpand(key);
            }}
            onKeyDown={(event) => {
              if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) {
                openProjectMenu(event);
              }
            }}
            aria-expanded={hasChildren ? isExpanded : undefined}
          >
            {/* Folder icon doubles as the expand state: open folder = expanded,
                closed folder = collapsed (replaces the old chevron+folder pair
                that ate horizontal space; user direction 2026-08-18). */}
            {isExpanded ? <FolderOpen size={14} className="project-tree__folder-icon" /> : <Folder size={14} className="project-tree__folder-icon" />}
            <span className="project-tree__folder-label">{projectLabel}</span>
            {node.remote && (
              <span
                className="project-tree__remote-badge"
                title={`${remoteKindLabel(node.remote.kind)} · ${node.remote.target}${node.remote.user ? " · " + node.remote.user : ""}`}
              >
                {remoteKindLabel(node.remote.kind)}
              </span>
            )}
          </button>
          <Tooltip label={t("projectTree.newTopicTooltip")} className="project-tree__action-slot">
            <button
              type="button"
              className={`project-tree__new-topic${creatingProject === key ? " project-tree__new-topic--active" : ""}`}
              disabled={creatingProject !== null}
              onClick={(e) => {
                e.stopPropagation();
                void handleCreateTopic(scope, projectRoot, key);
              }}
            >
              <Plus size={12} />
            </button>
          </Tooltip>
          <ContextMenu
            open={menuProject?.key === key}
            point={menuPoint}
            items={projectMenuItems}
            minWidth={212}
            ariaLabel={t("projectTree.projectActions")}
            onClose={closeMenu}
          />
        </div>
        {hasChildren && (
          <div className={`project-tree__children${isExpanded ? " project-tree__children--expanded" : ""}`}>
            <div className="project-tree__children-inner">
              {children.map((child) => renderNode(child, depth + 1))}
            </div>
          </div>
        )}
      </div>
    );
  };

  return (
    <div className="project-tree">
      {!hideSearch && (
        <label className="project-tree__search">
          <Search size={14} />
          <input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t("projectTree.searchPlaceholder")}
          />
        </label>
      )}
      <div className="project-tree__header">
        <span className="project-tree__header-title" role="button" aria-expanded={workspaceExpanded} onClick={toggleWorkspace}>
          <BriefcaseBusiness className="project-tree__header-icon" size={13} />
          {t("projectTree.workspaceTitle")}
        </span>
        <span className="project-tree__header-actions">
          <Tooltip label={!workspaceExpanded ? t("projectTree.restoreExpanded") : t("projectTree.collapseAll")} className="project-tree__action-slot project-tree__header-action-slot project-tree__action-slot--collapse">
            <button
              type="button"
              className={`project-tree__collapse-all${!workspaceExpanded ? " project-tree__collapse-all--restore" : ""}`}
              aria-label={!workspaceExpanded ? t("projectTree.restoreExpanded") : t("projectTree.collapseAll")}
              aria-pressed={!workspaceExpanded}
              onClick={() => setWorkspaceExpanded((v) => !v)}
            >
              {!workspaceExpanded ? <ListRestart size={14} /> : <ListCollapse size={14} />}
            </button>
          </Tooltip>
          <Tooltip label={t("projectTree.addProjectTooltip")} className="project-tree__action-slot project-tree__header-action-slot project-tree__action-slot--add">
            <button
              type="button"
              className="project-tree__add-project"
              aria-label={t("projectTree.addProjectTooltip")}
              disabled={addingProject}
              onClick={() => void handleAddProject()}
            >
              <FolderPlus size={14} />
            </button>
          </Tooltip>
          {onOpenRemote && (
            <Tooltip label={t("remote.trigger")} className="project-tree__action-slot project-tree__header-action-slot">
              <button
                type="button"
                className="project-tree__add-project"
                aria-label={t("remote.trigger")}
                onClick={onOpenRemote}
              >
                <Server size={14} />
              </button>
            </Tooltip>
          )}
        </span>
      </div>
      {workspaceExpanded && (
        <div className="project-tree__list">
          {visibleTree.length === 0 ? (
            query.trim() ? (
              <div className="project-tree__empty">{t("projectTree.emptyNoMatch")}</div>
            ) : (
              <div className="project-tree__empty-state">
                <div className="project-tree__empty project-tree__empty--subtle">{t("projectTree.emptyNoProjects")}</div>
                <button
                  type="button"
                  className="project-tree__empty-primary"
                  onClick={() => void handleAddProject()}
                  disabled={addingProject}
                >
                  <FolderPlus size={14} />
                  <span>{t("projectTree.addProjectTooltip")}</span>
                </button>
              </div>
            )
          ) : (
            visibleTree.map((node) => renderNode(node, 0))
          )}
        </div>
      )}
    </div>
  );
}
