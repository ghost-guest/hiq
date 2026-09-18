// netdevProjectStore — the active 运维项目 (site scope) shared between the
// NetdevTitleBar's switcher and the NetDevLayout's filtered views. The two
// mount separately (chrome center slot vs. layout body), so a tiny
// subscribe-store replaces prop drilling through App.
// Project DEFINITIONS live in the backend config ([netdev] projects via the
// settings bridge); only the ACTIVE SELECTION is session state, persisted
// here by name and restored (validated against the definitions) on boot.
export type NetDevProjectScope = { name: string; groups: string[] } | null; // null = 全部

const LS_KEY = "hiq.netdev.activeProject";

let current: NetDevProjectScope = null;
const listeners = new Set<() => void>();

export function getActiveProject(): NetDevProjectScope {
  return current;
}

export function setActiveProject(p: NetDevProjectScope) {
  current = p && p.name ? p : null;
  try {
    localStorage.setItem(LS_KEY, current ? current.name : "");
  } catch { /* private mode / test env — selection just won't persist */
  }
  listeners.forEach(fn => fn());
}

// restoreActiveProject applies the persisted selection once the project
// definitions are known: matched by name, dropped when the project no longer
// exists. In-session picks win — restore never clobbers a live choice.
export function restoreActiveProject(available: { name: string; groups: string[] }[]) {
  if (current || available.length === 0) return;
  let name = "";
  try {
    name = localStorage.getItem(LS_KEY) ?? "";
  } catch {
    return;
  }
  const hit = available.find(p => p.name === name);
  if (hit) {
    current = { name: hit.name, groups: hit.groups };
    listeners.forEach(fn => fn());
  } else if (name) {
    try { localStorage.removeItem(LS_KEY); } catch { /* best-effort */ }
  }
}

export function subscribeActiveProject(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}
