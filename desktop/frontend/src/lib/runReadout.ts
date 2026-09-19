// runReadout.ts — pure formatting for the composer's token-throughput readout.
//
// The figures live in the composer param row, next to the context/usage chip:
// "169 tok/s · 缓存 80%". They used to sit in the run-status strip, which only
// renders while a turn is in flight — and the agent emits Usage *after* a
// response has fully streamed (internal/agent/agent.go), so the numbers were
// hidden exactly when a user wanted to read them: while idle, and throughout
// the thinking phase of the next turn. Keeping them in the param row makes them
// permanently readable without a second status bar. Formatting lives here so
// the live/last-turn rules stay unit-testable rather than buried in a render
// closure.

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

// A rate is never abbreviated ("1234 tok/s", not "1.2k") — thousands separators
// would be noise next to a number this short. When the sample is too small to
// divide by, the raw output count stands in so the slot never sits empty on a
// turn that did produce output.
export function speedReadoutPart(tokens: number, elapsedMs: number, tokensLabel: string): string {
  if (!(tokens > 0)) return "";
  if (tokens >= MIN_SPEED_SAMPLE_TOKENS && elapsedMs >= MIN_SPEED_SAMPLE_MS) {
    return `${Math.round(tokens / (elapsedMs / 1000))} tok/s`;
  }
  return `↓ ${fmtTokens(tokens)} ${tokensLabel}`;
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

// resolveRunReadout composes the param-row readout: the live estimate wins
// while a turn streams, the last exact figure is the fallback (idle, or the
// first second of a turn before there is anything to divide), and the session
// cache hit-rate rides along. Returns "" when there is nothing to show at all.
export function resolveRunReadout(livePart: string, exactPart: string, cachePart: string): string {
  return joinReadout([livePart || exactPart, cachePart]);
}

// ── live (in-flight) throughput ─────────────────────────────────────────────
//
// A provider reports real token counts only when a response finishes, so during
// a turn the only continuous signal is the streamed text itself. The figures
// below turn that text into a tokens/s reading: a script-aware character rate
// (CJK and Latin tokenize at very different densities) times a correction
// learned from the last exact count the provider did give us.

// Characters per token, before correction. Chinese runs ~1.5–1.7 chars/token on
// Qwen/DeepSeek-class tokenizers; English prose and code run ~4.
export const CJK_CHARS_PER_TOKEN = 1.6;
export const LATIN_CHARS_PER_TOKEN = 4;

// Anything outside this band means the sample is nonsense (a response that
// streamed no text at all, or a provider reporting counts for something else),
// so the learned correction is pinned rather than allowed to run away.
export const MIN_TOKEN_SCALE = 0.2;
export const MAX_TOKEN_SCALE = 5;

const CJK_RE = /[\u3000-\u303f\u3040-\u30ff\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff\uff00-\uffef]/gu;

// countCjk counts the CJK code points in a streamed delta — punctuation, kana,
// ideographs, compatibility forms and fullwidth forms. Everything else (ASCII,
// accents, emoji) is treated as "other" for the density split.
export function countCjk(text: string): number {
  if (!text) return 0;
  const m = text.match(CJK_RE);
  return m ? m.length : 0;
}

// estimateTokens converts streamed character counts into an approximate token
// count. `scale` is the learned correction (1 = the defaults above).
export function estimateTokens(cjkChars: number, otherChars: number, scale = 1): number {
  if (!(cjkChars > 0) && !(otherChars > 0)) return 0;
  const base = cjkChars / CJK_CHARS_PER_TOKEN + otherChars / LATIN_CHARS_PER_TOKEN;
  return base * (scale > 0 ? scale : 1);
}

// blendTokenScale folds one completed response's true ratio into the running
// correction. A single response is a poor sample (its character mix is one
// point), so it is clamped and blended rather than adopted.
export function blendTokenScale(current: number, sample: number, weight = 0.5): number {
  if (!Number.isFinite(sample) || sample <= 0) return current;
  const clamped = Math.min(MAX_TOKEN_SCALE, Math.max(MIN_TOKEN_SCALE, sample));
  const next = current * (1 - weight) + clamped * weight;
  return Math.min(MAX_TOKEN_SCALE, Math.max(MIN_TOKEN_SCALE, next));
}

// liveSpeedPart renders "≈ 62 tok/s" while a turn streams. The "≈" is not
// decoration: the number is derived from characters, and only the provider's
// own count (once the turn ends) is exact. Returns "" until the sample is worth
// dividing by, so the caller falls back to the last exact figure instead of
// flashing a bogus 0.
export function liveSpeedPart(estTokens: number, elapsedMs: number): string {
  if (!(estTokens >= MIN_SPEED_SAMPLE_TOKENS) || elapsedMs < MIN_SPEED_SAMPLE_MS) return "";
  return `≈ ${Math.round(estTokens / (elapsedMs / 1000))} tok/s`;
}
