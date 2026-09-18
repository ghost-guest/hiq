// runReadout.ts — pure formatting for the composer's run-status strip.
//
// The strip used to render only while a turn was in flight, and the tok/s +
// cache readouts inside it only once the turn's first `usage` event landed.
// The agent emits Usage *after* a model response has fully streamed
// (internal/agent/agent.go), so both gaps hid the numbers exactly when a user
// wanted to read them: while idle, and throughout the thinking phase of the
// next turn. Formatting lives here so the live/last-turn decision stays
// unit-testable instead of being buried in a render closure.

// Token counts under 1000 stay exact; above that we render one decimal of
// thousands and drop a trailing ".0" (12_000 -> "12k", 3_400 -> "3.4k").
export function fmtTokens(n: number): string {
  if (n >= 1000) return (n / 1000).toFixed(1).replace(/\.0$/, "") + "k";
  return String(n);
}

// A tokens-per-second average needs a real sample to mean anything. Gating on
// elapsed time alone hid the speed for every fast turn (a 2s turn with a
// hundred output tokens is a perfectly good sample), while gating on tokens
// alone lets a 200 ms turn divide by ~0. So both must clear their floor.
export const MIN_SPEED_SAMPLE_MS = 1000;
export const MIN_SPEED_SAMPLE_TOKENS = 20;

// Any object carrying cache-token counts: a live WireUsage event, or the
// session telemetry that ships with ContextInfo (restored with the tab, so it
// outlives the process that produced it).
export interface CacheTelemetry {
  cacheHitTokens?: number;
  cacheMissTokens?: number;
  sessionCacheHitTokens?: number;
  sessionCacheMissTokens?: number;
}

// Session-cumulative prompt-cache hit-rate as a 0..1 fraction (Σhit/Σ(hit+miss)).
// The session fields are steadier than a single turn's pair, so they win when
// present; a provider that reports neither yields null and the caller hides the
// readout rather than claiming a misleading 0%.
export function sessionCacheHitRate(usage?: CacheTelemetry | null): number | null {
  if (!usage) return null;
  const sessHit = usage.sessionCacheHitTokens ?? 0;
  const sessMiss = usage.sessionCacheMissTokens ?? 0;
  const turnHit = usage.cacheHitTokens ?? 0;
  const turnMiss = usage.cacheMissTokens ?? 0;
  const session = sessHit + sessMiss;
  const denom = session > 0 ? session : turnHit + turnMiss;
  if (denom <= 0) return null;
  const rate = (session > 0 ? sessHit : turnHit) / denom;
  return rate > 0 ? rate : null;
}

// tokenReadoutParts renders "↓ 3k tokens" plus the turn-average speed once the
// sample qualifies. A rate is never abbreviated ("1234 tok/s", not "1.2k") —
// thousands separators would be noise next to a number this short. An empty
// array means "nothing to show yet".
export function tokenReadoutParts(tokens: number, elapsedMs: number, tokensLabel: string): string[] {
  if (!(tokens > 0)) return [];
  const parts = [`↓ ${fmtTokens(tokens)} ${tokensLabel}`];
  if (tokens >= MIN_SPEED_SAMPLE_TOKENS && elapsedMs >= MIN_SPEED_SAMPLE_MS) {
    parts.push(`${Math.round(tokens / (elapsedMs / 1000))} tok/s`);
  }
  return parts;
}

// cacheReadoutPart renders "缓存 78%", or "" when the rate is unknown.
export function cacheReadoutPart(hitRate: number | null, cacheLabel: string): string {
  if (hitRate === null) return "";
  return `${cacheLabel} ${Math.round(hitRate * 100)}%`;
}

// joinReadout folds the non-empty fragments into one " · "-separated line.
export function joinReadout(parts: string[]): string {
  return parts.filter(Boolean).join(" · ");
}
