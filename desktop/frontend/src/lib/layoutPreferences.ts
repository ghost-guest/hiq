import { activeStorageProfile, scopedStorageKey } from "./profileScopedStorage";

export type LayoutSizeKey =
  | "sidebarWidth"
  | "sidebarWidthGraphite"
  | "rightDockWidth"
  | "rightDockTreeWidth"
  | "rightDockPreviewWidth"
  | "workspaceFileTreePanelWidth"
  | "workspaceTreeWidth"
  | "composerHeight"
  | "drawerWidth"
  | "settingsDrawerWidth";

type LayoutPreferences = {
  sizes?: Partial<Record<LayoutSizeKey, number>>;
};

const STORAGE_KEY = "hiq.layoutPreferences.v1";

// Keys that belong to a PROFILE-SPECIFIC surface (the right dock / composer of
// dev vs cowork vs netdev): each profile gets its own bucket, seeded from the
// global (dev) values on first read. Chrome-level keys (sidebar, drawers) are
// shared surfaces and stay global.
const PROFILE_SCOPED_KEYS: Partial<Record<LayoutSizeKey, boolean>> = {
  rightDockWidth: true,
  rightDockTreeWidth: true,
  rightDockPreviewWidth: true,
  workspaceFileTreePanelWidth: true,
  workspaceTreeWidth: true,
  composerHeight: true,
};

function scopedPrefsKey(): string {
  return scopedStorageKey(STORAGE_KEY);
}

const LEGACY_SIZE_KEYS: Record<LayoutSizeKey, string[]> = {
  sidebarWidth: ["hiq.sidebar.width"],
  sidebarWidthGraphite: [],
  rightDockWidth: [],
  rightDockTreeWidth: [],
  rightDockPreviewWidth: [],
  workspaceFileTreePanelWidth: [],
  workspaceTreeWidth: ["hiq.workspaceTree.width"],
  composerHeight: ["hiq.composerHeight"],
  drawerWidth: ["hiq.drawer.width"],
  settingsDrawerWidth: ["hiq.settingsDrawer.width"],
};

type ClampSize = (value: number) => number;

function readPrefs(): LayoutPreferences {
  if (typeof window === "undefined") return {};
  const readBucket = (key: string): LayoutPreferences => {
    try {
      const raw = window.localStorage.getItem(key);
      if (!raw) return {};
      const parsed = JSON.parse(raw) as LayoutPreferences;
      return parsed && typeof parsed === "object" ? parsed : {};
    } catch {
      return {};
    }
  };
  const global = readPrefsGlobal();
  const scoped = readBucket(scopedPrefsKey());
  // Effective view: global sizes with the active profile's scoped sizes
  // layered on top (scoped buckets only carry the profile-specific keys, so
  // the first visit of a named profile inherits dev's dimensions).
  const sizes = { ...global.sizes };
  for (const k of Object.keys(scoped.sizes ?? {}) as LayoutSizeKey[]) {
    if (PROFILE_SCOPED_KEYS[k]) sizes[k] = scoped.sizes![k];
  }
  return { sizes };
}

function readPrefsGlobal(): LayoutPreferences {
  if (typeof window === "undefined") return {};
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as LayoutPreferences;
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch {
    return {};
  }
}

function writePrefs(prefs: LayoutPreferences): void {
  if (typeof window === "undefined") return;
  // Split the write: profile-specific keys go to the ACTIVE profile's bucket,
  // everything else to the global one. Scoped buckets keep working even if the
  // key catalog above changes (unknown scoped keys are preserved verbatim).
  const global = readPrefsGlobal();
  const globalSizes = { ...(global.sizes ?? {}) };
  const scopedRaw = (() => {
    try {
      const raw = window.localStorage.getItem(scopedPrefsKey());
      if (!raw) return {};
      const parsed = JSON.parse(raw) as LayoutPreferences;
      return parsed && typeof parsed === "object" ? parsed : {};
    } catch {
      return {};
    }
  })();
  const scopedSizes = { ...(scopedRaw.sizes ?? {}) };
  for (const [k, v] of Object.entries(prefs.sizes ?? {}) as [LayoutSizeKey, number][]) {
    if (PROFILE_SCOPED_KEYS[k]) {
      scopedSizes[k] = v;
    } else {
      globalSizes[k] = v;
    }
  }
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify({ sizes: globalSizes }));
    if (activeStorageProfile() !== "dev") {
      window.localStorage.setItem(scopedPrefsKey(), JSON.stringify({ sizes: scopedSizes }));
    }
  } catch {
    /* ignore storage failures */
  }
}

function readLegacySize(key: LayoutSizeKey): number | null {
  if (typeof window === "undefined") return null;
  for (const legacyKey of LEGACY_SIZE_KEYS[key]) {
    try {
      const raw = Number(window.localStorage.getItem(legacyKey));
      if (Number.isFinite(raw) && raw > 0) return raw;
    } catch {
      /* keep trying other keys */
    }
  }
  return null;
}

function normalizeSize(value: number, clamp?: ClampSize): number {
  const rounded = Math.round(value);
  return clamp ? clamp(rounded) : rounded;
}

export function loadLayoutSize(key: LayoutSizeKey, fallback: number, clamp?: ClampSize): number {
  const prefs = readPrefs();
  const saved = prefs.sizes?.[key];
  const value = Number.isFinite(saved) && saved! > 0 ? saved! : readLegacySize(key);
  return value === null ? normalizeSize(fallback, clamp) : normalizeSize(value, clamp);
}

export function loadOptionalLayoutSize(key: LayoutSizeKey, clamp?: ClampSize): number | null {
  const prefs = readPrefs();
  const saved = prefs.sizes?.[key];
  const value = Number.isFinite(saved) && saved! > 0 ? saved! : readLegacySize(key);
  return value === null ? null : normalizeSize(value, clamp);
}

export function saveLayoutSize(key: LayoutSizeKey, value: number, clamp?: ClampSize): void {
  const prefs = readPrefs();
  const sizes = { ...(prefs.sizes ?? {}), [key]: normalizeSize(value, clamp) };
  writePrefs({ ...prefs, sizes });
}

export function clearLayoutSize(key: LayoutSizeKey): void {
  const prefs = readPrefs();
  const sizes = { ...(prefs.sizes ?? {}) };
  delete sizes[key];
  writePrefs({ ...prefs, sizes });
}
