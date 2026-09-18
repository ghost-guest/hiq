// SidebarSessions — the "Recent" section above the project tree (ui-redesign
// spec §4-B4). Rows are compact: single-line title + relative age; hover swaps
// the age for a ⋮ menu with rename (inline input) and a two-step delete, the
// same interaction grammar as HistoryPanel. Clicking a row resumes the session.
// The list itself is a scrollable box holding up to 20 recent sessions
// (user direction 2026-08-18) — no separate "view all" entry; the full
// manager stays reachable from the sidebar footer's history button.
import { useState } from "react";
import { ChevronDown, MoreHorizontal, Pencil, Trash2 } from "lucide-react";
import { useT } from "../lib/i18n";
import { ContextMenu, contextMenuPointFromEvent, type ContextMenuItem, type ContextMenuPoint } from "./ContextMenu";
import type { SessionMeta } from "../lib/types";

const MAX_ROWS = 20;

// Compact relative age, OpenWorker-style: 刚刚 / 5m / 2h / 昨天 / 3d.
export function compactSessionAge(ts: number): string {
  const diff = Date.now() - ts;
  if (diff < 60_000) return "<1m";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h`;
  if (diff < 172_800_000) return "1d";
  return `${Math.floor(diff / 86_400_000)}d`;
}

export function SidebarSessions({
  sessions,
  onResume,
  onRename,
  onDelete,
}: {
  sessions: SessionMeta[];
  onResume: (session: SessionMeta) => void;
  onRename: (path: string, title: string) => void;
  onDelete: (path: string) => void;
}) {
  const t = useT();
  const [menuSession, setMenuSession] = useState<SessionMeta | null>(null);
  const [menuPoint, setMenuPoint] = useState<ContextMenuPoint | null>(null);
  const [confirmPath, setConfirmPath] = useState<string | null>(null);
  const [editing, setEditing] = useState<string | null>(null);
  const [open, setOpen] = useState(() => {
    try {
      const saved = localStorage.getItem("hiq.sidebarSessions.open");
      return saved ? saved === "true" : true;
    } catch {
      return true;
    }
  });

  const toggleOpen = () => {
    setOpen((prev) => {
      const next = !prev;
      try {
        localStorage.setItem("hiq.sidebarSessions.open", String(next));
      } catch {}
      return next;
    });
  };

  const [draft, setDraft] = useState("");

  const closeMenu = () => {
    setMenuSession(null);
    setMenuPoint(null);
    setConfirmPath(null);
  };

  const rows = [...sessions]
    .filter((s) => !s.deletedAt)
    .sort((a, b) => b.lastActivityAt - a.lastActivityAt)
    .slice(0, MAX_ROWS);

  const menuItems: ContextMenuItem[] = menuSession
    ? [
        {
          key: "rename",
          icon: <Pencil size={13} />,
          label: t("sidebar.sessionRename"),
          onSelect: () => {
            setEditing(menuSession.path);
            setDraft(menuSession.title || menuSession.preview || "");
            closeMenu();
          },
        },
        { type: "separator", key: "session-sep" },
        {
          key: "delete",
          icon: <Trash2 size={13} />,
          label:
            confirmPath === menuSession.path
              ? t("sidebar.sessionDeleteConfirm")
              : t("sidebar.sessionDelete"),
          danger: true,
          onSelect: () => {
            if (confirmPath === menuSession.path) {
              onDelete(menuSession.path);
              closeMenu();
            } else {
              setConfirmPath(menuSession.path);
            }
          },
        },
      ]
    : [];

  return (
    <section className="side-sessions" aria-label={t("sidebar.sessionsLabel")}>
      <button type="button" className="side-sessions__head" onClick={toggleOpen} aria-expanded={open}>
        <span className="side-sessions__label">{t("sidebar.sessionsLabel")}</span>
        <ChevronDown size={12} className={open ? undefined : "side-sessions__chev--closed"} />
      </button>
      {open && (
        <div className="side-sessions__scroll">
          {rows.length === 0 ? (
            <div className="side-sessions__empty">{t("sidebar.sessionsEmpty")}</div>
          ) : (
            rows.map((s) => {
          const title = s.title || s.preview || s.path;
          const active = s.current;
          if (editing === s.path) {
            return (
              <input
                key={s.path}
                className="side-sessions__rename"
                value={draft}
                autoFocus
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    const next = draft.trim();
                    if (next) onRename(s.path, next);
                    setEditing(null);
                  } else if (e.key === "Escape") {
                    setEditing(null);
                  }
                }}
                onBlur={() => setEditing(null)}
                aria-label={t("sidebar.sessionRename")}
              />
            );
          }
          return (
            <div
              key={s.path}
              className={`side-sessions__row${active ? " side-sessions__row--active" : ""}`}
              role="button"
              tabIndex={0}
              onClick={() => onResume(s)}
              onKeyDown={(e) => {
                if (e.key === "Enter") onResume(s);
              }}
              title={title}
            >
              <span className="side-sessions__title">{title}</span>
              <span className="side-sessions__age">{compactSessionAge(s.lastActivityAt)}</span>
              <button
                type="button"
                className="side-sessions__more"
                aria-label={t("sidebar.sessionMenu")}
                onClick={(e) => {
                  e.stopPropagation();
                  setMenuSession(s);
                  setConfirmPath(null);
                  setMenuPoint(contextMenuPointFromEvent(e));
                }}
              >
                <MoreHorizontal size={13} />
              </button>
            </div>
          );
            })
          )}
        </div>
      )}
      <ContextMenu open={menuPoint !== null} point={menuPoint} items={menuItems} onClose={closeMenu} />
    </section>
  );
}
