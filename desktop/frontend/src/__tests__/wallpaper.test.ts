// wallpaper.test.ts — guards the state → CSS mapping for the custom wallpaper.
//
// lib/wallpaper.ts is the single place where persisted wallpaper state becomes
// CSS custom properties, and styles/wallpaper.css reads those properties by
// name. A typo on either side fails silently — the backdrop simply renders
// nothing — so the property names and units are asserted here rather than left
// to a visual check.
import { beforeEach, describe, expect, it } from "vitest";

// Minimal DOM stand-in. lib/wallpaper.ts touches only documentElement's
// setAttribute + style, and window.location for URL resolution, so pulling in a
// full jsdom environment would add cost without covering anything extra.
const vars = new Map<string, string>();
const attrs = new Map<string, string>();

(globalThis as unknown as { document: unknown }).document = {
  documentElement: {
    setAttribute: (key: string, value: string) => {
      attrs.set(key, value);
    },
    style: {
      setProperty: (key: string, value: string) => {
        vars.set(key, value);
      },
      removeProperty: (key: string) => {
        vars.delete(key);
      },
    },
  },
};
(globalThis as unknown as { window: unknown }).window = {
  location: { href: "http://localhost/" },
  addEventListener: () => {},
  removeEventListener: () => {},
  dispatchEvent: () => true,
};

// Imported dynamically so the stubs above are installed before the module (and
// the bridge it pulls in) evaluates.
const {
  WALLPAPER_BLUR_MAX,
  WALLPAPER_DIM_MAX,
  WALLPAPER_SURFACE_PCT,
  applyWallpaper,
  clampWallpaperBlur,
  clampWallpaperDim,
  normalizeWallpaperFit,
  wallpaperLegibilityHint,
} = await import("../lib/wallpaper");

import type { WallpaperView } from "../lib/types";

const HEARTH = {
  active: true,
  url: "/__fairpeer_wallpaper/wallpaper.png?v=1758000000",
  name: "wallpaper.png",
  blur: 18,
  dim: 45,
  fit: "cover",
} satisfies WallpaperView;

beforeEach(() => {
  vars.clear();
  attrs.clear();
});

describe("wallpaper tuning normalization", () => {
  it("clamps blur into range and rejects non-numbers", () => {
    expect(clampWallpaperBlur(-4)).toBe(0);
    expect(clampWallpaperBlur(WALLPAPER_BLUR_MAX + 100)).toBe(WALLPAPER_BLUR_MAX);
    expect(clampWallpaperBlur(18)).toBe(18);
    expect(clampWallpaperBlur(Number.NaN)).toBe(0);
  });

  it("clamps the scrim into range", () => {
    expect(clampWallpaperDim(-1)).toBe(0);
    expect(clampWallpaperDim(WALLPAPER_DIM_MAX + 50)).toBe(WALLPAPER_DIM_MAX);
    expect(clampWallpaperDim(45)).toBe(45);
  });

  it("normalizes fit, defaulting unknown values to cover", () => {
    expect(normalizeWallpaperFit("contain")).toBe("contain");
    expect(normalizeWallpaperFit("tile")).toBe("tile");
    expect(normalizeWallpaperFit("stretch")).toBe("cover");
    expect(normalizeWallpaperFit(undefined)).toBe("cover");
  });

  it("grades legibility so a low scrim is called out", () => {
    expect(wallpaperLegibilityHint(60)).toBe("ok");
    expect(wallpaperLegibilityHint(35)).toBe("ok");
    expect(wallpaperLegibilityHint(25)).toBe("soft");
    expect(wallpaperLegibilityHint(5)).toBe("risky");
  });
});

describe("applyWallpaper", () => {
  it("writes the tokens the stylesheet reads", () => {
    applyWallpaper(HEARTH);

    expect(attrs.get("data-wallpaper")).toBe("on");
    expect(attrs.get("data-wallpaper-blur")).toBe("18");
    // Resolved to an absolute URL: the property is substituted into a stylesheet
    // rule, where a relative url() would be ambiguous.
    expect(vars.get("--app-bg-image")).toBe('url("http://localhost/__fairpeer_wallpaper/wallpaper.png?v=1758000000")');
    expect(vars.get("--app-bg-blur-px")).toBe("18");
    expect(vars.get("--app-bg-dim-pct")).toBe("45");
    // The surface opacity is what keeps text legible over the photo.
    expect(vars.get("--app-bg-surface-pct")).toBe(String(WALLPAPER_SURFACE_PCT));
    expect(vars.get("--app-bg-size")).toBe("cover");
    expect(vars.get("--app-bg-repeat")).toBe("no-repeat");
  });

  it("maps tile to a repeating auto-sized background", () => {
    applyWallpaper({ ...HEARTH, fit: "tile" });
    expect(vars.get("--app-bg-size")).toBe("auto");
    expect(vars.get("--app-bg-repeat")).toBe("repeat");
  });

  it("clamps out-of-range tuning before writing it", () => {
    applyWallpaper({ ...HEARTH, blur: 900, dim: -20 });
    expect(vars.get("--app-bg-blur-px")).toBe(String(WALLPAPER_BLUR_MAX));
    expect(vars.get("--app-bg-dim-pct")).toBe("0");
  });

  it("restores the plain background when no wallpaper is active", () => {
    applyWallpaper(HEARTH);
    applyWallpaper(null);

    expect(attrs.get("data-wallpaper")).toBe("off");
    // Blur flag resets too, so the blur-skipping selector stays in sync.
    expect(attrs.get("data-wallpaper-blur")).toBe("0");
    // Every property is removed, leaving nothing for the stylesheet to match.
    expect(vars.size).toBe(0);
  });

  it("treats a view without a URL as inactive", () => {
    applyWallpaper({ ...HEARTH, active: true, url: "" });
    expect(attrs.get("data-wallpaper")).toBe("off");
    expect(vars.size).toBe(0);
  });
});
