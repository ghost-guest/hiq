// run-readout.test.ts — the composer's param-row readout (token speed, cache
// hit-rate) must stay readable in the two moments a strip-based readout cannot
// cover: after a turn ends, and through the thinking phase of the next turn
// (whose usage event only lands once that response has fully streamed). The
// formatting is pure, so the fallback rules are asserted here instead of
// through the live DOM.
import { describe, expect, it } from "vitest";
import { cacheReadoutPart, fmtTokens, joinReadout, MIN_SPEED_SAMPLE_MS, MIN_SPEED_SAMPLE_TOKENS, runReadoutText, sessionCacheHitRate, speedReadoutPart } from "../lib/runReadout";
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

describe("runReadoutText / joinReadout", () => {
  it("puts the throughput beside the cache rate", () => {
    expect(runReadoutText(3600, 12000, "tokens", "缓存 78%")).toBe("300 tok/s · 缓存 78%");
  });

  it("keeps the count while the rate is not yet meaningful", () => {
    expect(runReadoutText(40, 400, "tokens", "缓存 78%")).toBe("↓ 40 tokens · 缓存 78%");
  });

  it("drops the cache part for a provider that never reports it", () => {
    expect(runReadoutText(3600, 12000, "tokens", "")).toBe("300 tok/s");
  });

  it("is empty when there is neither a turn nor telemetry — the caller renders nothing", () => {
    expect(runReadoutText(0, 0, "tokens", "")).toBe("");
  });

  it("joins only the non-empty fragments", () => {
    expect(joinReadout(["300 tok/s", "缓存 78%", ""])).toBe("300 tok/s · 缓存 78%");
    expect(joinReadout(["", ""])).toBe("");
  });
});
