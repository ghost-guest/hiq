// run-readout.test.ts — the composer's param-row readout (token speed, cache
// hit-rate) must stay readable in the two moments a strip-based readout cannot
// cover: after a turn ends, and through the thinking phase of the next turn
// (whose usage event only lands once that response has fully streamed). The
// formatting is pure, so the fallback rules are asserted here instead of
// through the live DOM.
import { describe, expect, it } from "vitest";
import { blendTokenScale, cacheReadoutPart, countCjk, estimateTokens, fmtTokens, joinReadout, liveSpeedPart, MIN_SPEED_SAMPLE_MS, MIN_SPEED_SAMPLE_TOKENS, resolveRunReadout, sessionCacheHitRate, speedReadoutPart } from "../lib/runReadout";
import type { WireUsage } from "../lib/types";

function usage(partial: Partial<WireUsage>): WireUsage {
  return {
    promptTokens: 0,
    completionTokens: 0,
    totalTokens: 0,
    cacheHitTokens: 0,
    cacheMissTokens: 0,
    sessionCacheHitTokens: 0,
    sessionCacheMissTokens: 0,
    ...partial,
  };
}

describe("fmtTokens", () => {
  it("keeps sub-thousand counts exact and drops a trailing .0", () => {
    expect(fmtTokens(0)).toBe("0");
    expect(fmtTokens(999)).toBe("999");
    expect(fmtTokens(3000)).toBe("3k");
    expect(fmtTokens(3400)).toBe("3.4k");
    expect(fmtTokens(12000)).toBe("12k");
  });
});

describe("sessionCacheHitRate", () => {
  it("prefers the session-cumulative pair over the single turn", () => {
    // The session aggregate (80%) must win over the turn pair (10%) — it is
    // the steadier number and matches what the usage chip reports.
    const rate = sessionCacheHitRate(usage({ sessionCacheHitTokens: 800, sessionCacheMissTokens: 200, cacheHitTokens: 10, cacheMissTokens: 90 }));
    expect(rate).toBeCloseTo(0.8, 6);
  });

  it("falls back to the turn pair when the session fields are absent", () => {
    expect(sessionCacheHitRate(usage({ cacheHitTokens: 30, cacheMissTokens: 70 }))).toBeCloseTo(0.3, 6);
  });

  it("returns null when the provider reports no cache tokens at all", () => {
    // A provider that omits the fields yields 0/0 — the caller hides the
    // readout rather than claiming a misleading 0%.
    expect(sessionCacheHitRate(usage({ promptTokens: 500 }))).toBeNull();
    expect(sessionCacheHitRate(undefined)).toBeNull();
    expect(sessionCacheHitRate(null)).toBeNull();
  });

  it("returns null for a genuine zero hit-rate", () => {
    expect(sessionCacheHitRate(usage({ cacheMissTokens: 40 }))).toBeNull();
  });

  it("reads the session telemetry that ships with ContextInfo", () => {
    // ContextInfo carries only the session-cumulative fields (and only once a
    // restored tab has telemetry) — the readout has nothing else to go on
    // after an app restart.
    expect(sessionCacheHitRate({ sessionCacheHitTokens: 900, sessionCacheMissTokens: 100 })).toBeCloseTo(0.9, 6);
    expect(sessionCacheHitRate({})).toBeNull();
  });
});

describe("speedReadoutPart", () => {
  it("shows nothing before the turn reports any output tokens", () => {
    expect(speedReadoutPart(0, 12000, "tokens")).toBe("");
  });

  it("falls back to the raw count when the turn was too short to sample", () => {
    expect(speedReadoutPart(120, MIN_SPEED_SAMPLE_MS - 1, "tokens")).toBe("↓ 120 tokens");
  });

  it("falls back to the raw count when too few tokens came out", () => {
    // A dozen tokens over 12 s is a real elapsed time but a meaningless rate.
    expect(speedReadoutPart(MIN_SPEED_SAMPLE_TOKENS - 1, 12000, "tokens")).toBe("↓ 19 tokens");
  });

  it("reports the turn-average speed once the sample qualifies", () => {
    expect(speedReadoutPart(3600, 12000, "tokens")).toBe("300 tok/s");
  });

  it("reports the speed for a fast turn instead of hiding it", () => {
    // The regression this guards: a 2 s turn used to clear no threshold at all,
    // so a fast model never showed a tok/s figure.
    expect(speedReadoutPart(106, 2000, "tokens")).toBe("53 tok/s");
  });

  it("never abbreviates the rate", () => {
    expect(speedReadoutPart(24000, 12000, "tokens")).toBe("2000 tok/s");
  });
});

describe("cacheReadoutPart", () => {
  it("renders a percentage or drops out entirely", () => {
    expect(cacheReadoutPart(0.784, "缓存")).toBe("缓存 78%");
    expect(cacheReadoutPart(null, "缓存")).toBe("");
  });
});

describe("countCjk", () => {
  it("counts ideographs, fullwidth punctuation and kana", () => {
    // 你 好 ， 世 界 — the comma is U+FF0C, i.e. the same bucket as the hanzi.
    expect(countCjk("你好，世界")).toBe(5);
    expect(countCjk("テスト")).toBe(3);
  });

  it("leaves latin, accents and emoji to the other bucket", () => {
    expect(countCjk("hello, world! naïve")).toBe(0);
    expect(countCjk("")).toBe(0);
  });

  it("mixes the two scripts in one delta", () => {
    expect(countCjk("foo 二分查找 bar")).toBe(4);
  });
});

describe("estimateTokens", () => {
  it("is zero when nothing has streamed", () => {
    expect(estimateTokens(0, 0)).toBe(0);
  });

  it("charges CJK by 1.6 chars/token and latin by 4", () => {
    expect(estimateTokens(160, 0)).toBeCloseTo(100, 6);
    expect(estimateTokens(0, 400)).toBeCloseTo(100, 6);
  });

  it("applies the learned correction, ignoring a nonsense one", () => {
    expect(estimateTokens(160, 0, 2)).toBeCloseTo(200, 6);
    expect(estimateTokens(160, 0, 0)).toBeCloseTo(100, 6);
    expect(estimateTokens(160, 0, Number.NaN)).toBeCloseTo(100, 6);
  });
});

describe("blendTokenScale", () => {
  it("keeps the current value when the sample is unusable", () => {
    expect(blendTokenScale(1.5, 0)).toBe(1.5);
    expect(blendTokenScale(1.5, Number.NaN)).toBe(1.5);
  });

  it("clamps a wild sample before blending", () => {
    // 50x is nonsense (50 chars per token); the band stops one bad response
    // from wrecking every later estimate.
    expect(blendTokenScale(1, 50)).toBe(3);
    expect(blendTokenScale(1, 0.001)).toBe(0.6);
  });

  it("moves halfway toward a sane sample", () => {
    expect(blendTokenScale(1, 2)).toBeCloseTo(1.5, 6);
  });
});

describe("liveSpeedPart", () => {
  it("stays hidden until the sample is worth dividing by", () => {
    expect(liveSpeedPart(MIN_SPEED_SAMPLE_TOKENS - 1, 5000)).toBe("");
    expect(liveSpeedPart(100, MIN_SPEED_SAMPLE_MS - 1)).toBe("");
  });

  it("marks the figure as approximate — only the provider's count is exact", () => {
    expect(liveSpeedPart(100, 2000)).toBe("≈ 50 tok/s");
  });
});

describe("resolveRunReadout / joinReadout", () => {
  it("prefers the live estimate while a turn streams", () => {
    expect(resolveRunReadout("≈ 62 tok/s", "300 tok/s", "缓存 78%")).toBe("≈ 62 tok/s · 缓存 78%");
  });

  it("falls back to the last exact figure before the live one has a sample", () => {
    expect(resolveRunReadout("", "300 tok/s", "缓存 78%")).toBe("300 tok/s · 缓存 78%");
  });

  it("still shows the cache rate when there is no throughput to pair it with", () => {
    expect(resolveRunReadout("", "", "缓存 78%")).toBe("缓存 78%");
  });

  it("is empty when there is neither a turn nor telemetry — the caller renders nothing", () => {
    expect(resolveRunReadout("", "", "")).toBe("");
  });

  it("joins only the non-empty fragments", () => {
    expect(joinReadout(["300 tok/s", "缓存 78%", ""])).toBe("300 tok/s · 缓存 78%");
    expect(joinReadout(["", ""])).toBe("");
  });
});