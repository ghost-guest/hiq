// wallpaper.ts — applies the persisted desktop wallpaper to the DOM.
//
// The Go side owns the image file and the persisted tuning (wallpaper_app.go);
// this module owns the single mapping from that state to the CSS custom
// properties documented in styles/wallpaper.css. Keeping the mapping in one
// place is what stops the settings preview and the live backdrop from drifting
// apart — both read the exact same tokens.
//
// Blur and scrim are applied purely as CSS values, so dragging a slider is
// instant: no image is re-encoded, re-uploaded, or re-decoded.

import { app } from "./bridge";
import type { WallpaperView } from "./types";

/** Blur is a gaussian sigma in px. Capped so the image stays recognisable. */
export const WALLPAPER_BLUR_MAX = 40;
/** Scrim opacity in percent. Capped below 100 so some image always survives. */
export const WALLPAPER_DIM_MAX = 85;
/**
 * Surface opacity used for the large layout surfaces while a wallpaper is
 * active. Deliberately fixed rather than user-tunable: it is the legibility
 * floor that keeps body text at WCAG AA contrast over a busy photo, while
 * still letting 18% of the image read through. The user's real control over
 * legibility is the scrim (dim) slider.
 */
export const WALLPAPER_SURFACE_PCT = 82;

export const WALLPAPER_FITS = ["cover", "contain", "tile"] as const;
export type WallpaperFit = (typeof WALLPAPER_FITS)[number];

// CSS values per fit mode. `tile` is not an object-fit value, so the image is
// painted as a repeating background instead — see wallpaper.css.
const FIT_SIZE: Record<WallpaperFit, string> = { cover: "cover", contain: "contain", tile: "auto" };
const FIT_REPEAT: Record<WallpaperFit, string> = { cover: "no-repeat", contain: "no-repeat", tile: "repeat" };

export function normalizeWallpaperFit(value: unknown): WallpaperFit {
  return value === "contain" || value === "tile" ? value : "cover";
}

export function clampWallpaperBlur(value: number): number {
  if (!Number.isFinite(value)) return 0;
  return Math.min(WALLPAPER_BLUR_MAX, Math.max(0, Math.round(value)));
}

export function clampWallpaperDim(value: number): number {
  if (!Number.isFinite(value)) return 0;
  return Math.min(WALLPAPER_DIM_MAX, Math.max(0, Math.round(value)));
}

/**
 * wallpaperLegibilityHint grades how readable the UI is at a given scrim
 * strength. The settings panel surfaces this so a user who drags the scrim to
 * zero is told why the interface just became hard to read, rather than having
 * to discover it in a screenshot.
 */
export function wallpaperLegibilityHint(dim: number): "ok" | "soft" | "risky" {
  if (dim >= 35) return "ok";
  if (dim >= 20) return "soft";
  return "risky";
}

/**
 * toAbsoluteURL resolves the wallpaper asset route against the current origin.
 *
 * The route is served by the Wails AssetServer, so the correct origin differs
 * between the shell and a browser dev session. Resolving to an absolute URL
 * here also removes any ambiguity about how a url() token inside a CSS custom
 * property gets resolved once it is substituted into a stylesheet rule.
 */
function toAbsoluteURL(url: string): string {
  if (typeof window === "undefined") return url;
  try {
    return new URL(url, window.location.href).href;
  } catch {
    return url;
  }
}

/**
 * applyWallpaper writes the wallpaper state to the document. Passing a view
 * with `active: false` (or null) restores the plain themed background.
 *
 * Every value is written as a custom property on the document element, which
 * is what makes the feature a pure add-on: with no wallpaper the properties
 * are removed and nothing in the stylesheet matches [data-wallpaper="on"].
 */
export function applyWallpaper(view: WallpaperView | null): void {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  const style = root.style;
  // `active` is the single gate for the whole feature: no view, no flag, or no
  // URL all mean "plain themed background".
  const active = Boolean(view && view.active && view.url);

  root.setAttribute("data-wallpaper", active ? "on" : "off");

  if (!active || !view) {
    root.setAttribute("data-wallpaper-blur", "0");
    style.removeProperty("--app-bg-image");
    style.removeProperty("--app-bg-blur-px");
    style.removeProperty("--app-bg-dim-pct");
    style.removeProperty("--app-bg-surface-pct");
    style.removeProperty("--app-bg-size");
    style.removeProperty("--app-bg-repeat");
    return;
  }

  const blur = clampWallpaperBlur(view.blur);
  const dim = clampWallpaperDim(view.dim);
  const fit = normalizeWallpaperFit(view.fit);

  // Consumed by the blur-skipping selector in wallpaper.css.
  root.setAttribute("data-wallpaper-blur", String(blur));

  style.setProperty("--app-bg-image", `url("${toAbsoluteURL(view.url)}")`);
  style.setProperty("--app-bg-blur-px", String(blur));
  style.setProperty("--app-bg-dim-pct", String(dim));
  style.setProperty("--app-bg-surface-pct", String(WALLPAPER_SURFACE_PCT));
  style.setProperty("--app-bg-size", FIT_SIZE[fit]);
  style.setProperty("--app-bg-repeat", FIT_REPEAT[fit]);
}

/**
 * loadWallpaper reads the persisted wallpaper and applies it. Failures are
 * swallowed: a broken wallpaper must never stop the app from starting — the
 * user simply keeps the plain background.
 */
export async function loadWallpaper(): Promise<WallpaperView | null> {
  try {
    const view = await app.WallpaperInfo();
    broadcastWallpaper(view);
    return view;
  } catch {
    broadcastWallpaper(null);
    return null;
  }
}

// The wallpaper is changed from the settings panel but painted by the app root
// (AppBackdrop), which are different subtrees. A window event keeps them in
// sync without threading state through every intermediate component — the same
// pattern the cowork "insert text" bridge already uses.
const WALLPAPER_EVENT = "hiq:wallpaper-changed";

/**
 * broadcastWallpaper applies a view and tells any mounted backdrop about it.
 * Use this instead of applyWallpaper whenever the change originates in the UI,
 * so the root layer re-renders.
 */
export function broadcastWallpaper(view: WallpaperView | null): void {
  applyWallpaper(view);
  if (typeof window === "undefined") return;
  window.dispatchEvent(new CustomEvent<WallpaperView | null>(WALLPAPER_EVENT, { detail: view }));
}

/** subscribeWallpaper listens for wallpaper changes; returns an unsubscribe. */
export function subscribeWallpaper(listener: (view: WallpaperView | null) => void): () => void {
  if (typeof window === "undefined") return () => {};
  const handler = (event: Event) => listener((event as CustomEvent<WallpaperView | null>).detail ?? null);
  window.addEventListener(WALLPAPER_EVENT, handler);
  return () => window.removeEventListener(WALLPAPER_EVENT, handler);
}
