// UsageChip — compact context-window meter for the composer param row
// (ui-redesign spec §4-C3): a 46px fill bar plus inline token readings. The
// chip itself carries the numbers (session tokens · used/window); aria-label
// keeps the reading screen-reader friendly. Past it sits the `readout` string
// (token throughput + session cache hit-rate) supplied by Composer — a separate
// prop, because it describes the last completed turn rather than the context.
// The fill turns warn-coloured past 85% so compaction pressure is visible at a
// glance — and in that hot state a small inline "compact" button appears on the
// chip (the sole Compact entry point since the right-dock overview tab was
// slimmed down). Hovering (or keyboard focus) opens a detail panel: precise
// context fill, average prompt-cache hit-rate (Σhit/Σ(hit+miss) across the
// session), current-turn rate, and session-cumulative token splits + active-turn
// time. Data comes from App state — ContextInfo (extended with session
// telemetry) plus the latest-turn WireUsage.
import { Tooltip } from "../Tooltip";
import { useT } from "../../lib/i18n";
import { fmtDuration } from "../../lib/duration";
import type { BudgetStatusView, ContextInfo, WireUsage } from "../../lib/types";

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`;
  if (n >= 1_000) return `${Math.round(n / 1_000)}k`;
  return String(n);
}

function fmtFull(n: number): string {
  return n.toLocaleString("en-US");
}

function hitRate(hit: number, miss: number): number | null {
  const denom = hit + miss;
  if (denom <= 0) return null;
  return (hit / denom) * 100;
}

function rateTone(rate: number | null): string {
  if (rate === null) return "";
  if (rate >= 80) return "usage-pop__value--good";
  if (rate >= 50) return "usage-pop__value--notice";
  return "usage-pop__value--critical";
}

function fmtRate(rate: number | null): string {
  return rate === null ? "—" : `${rate.toFixed(1)}%`;
}

function Row({ label, value, tone }: { label: string; value: string; tone?: string }) {
  return (
    <div className="usage-pop__row">
      <span>{label}</span>
      <b className={tone || undefined}>{value}</b>
    </div>
  );
}

function fmtCost(cost: number, currency?: string): string {
  const sym = currency || "$";
  return `${sym}${cost < 0.01 ? cost.toFixed(4) : cost.toFixed(3)}`;
}

export function UsageChip({ context, usage, budget, readout, onCompact, disabled }: { context?: ContextInfo; usage?: WireUsage; budget?: BudgetStatusView; readout?: string; onCompact?: () => void; disabled?: boolean }) {
  const t = useT();
  if (!context || !context.window || context.window <= 0) {
    // No window known → no chip. The throughput readout beside it does not
    // depend on the window, so it still shows (without the hover panel).
    return readout ? <span className="composer-usage">{readout}</span> : null;
  }
  const pct = Math.min(100, Math.round((context.used / context.window) * 100));
  const hot = pct >= 85;
  const session = context.sessionTokens > 0 ? context.sessionTokens : context.used;

  // Aggregate cache rate prefers the telemetry totals (works for every
  // provider); the wire event's session fields are the live per-request
  // fallback while a turn is still streaming.
  const telHit = context.sessionCacheHitTokens ?? 0;
  const telMiss = context.sessionCacheMissTokens ?? 0;
  const wireHit = usage?.sessionCacheHitTokens ?? 0;
  const wireMiss = usage?.sessionCacheMissTokens ?? 0;
  const avgRate = telHit + telMiss > 0
    ? hitRate(telHit, telMiss)
    : hitRate(wireHit, wireMiss);
  const nowRate = usage ? hitRate(usage.cacheHitTokens, usage.cacheMissTokens) : null;
  const cacheWrite = context.sessionCacheWriteTokens ?? 0;
  const requests = context.requestCount ?? 0;

  const panel = (
    <div className="usage-pop">
      <div className="usage-pop__title">{t("composer.usageDetail.title")}</div>
      <div className="usage-pop__bar" aria-hidden="true">
        <i style={{ width: `${Math.min(100, (context.used / context.window) * 100)}%` }} />
      </div>
      <Row label={`${fmtFull(context.used)} / ${fmtFull(context.window)}`} value={`${((context.used / context.window) * 100).toFixed(1)}%`} />
      {!!context.compactRatio && context.compactRatio > 0 && context.compactRatio < 1 && (
        <Row label={t("composer.usageDetail.compactLine")} value={`${Math.round(context.compactRatio * 100)}%`} />
      )}
      <div className="usage-pop__sep" />
      <div className="usage-pop__row usage-pop__row--hero">
        <span>{t("composer.usageDetail.avgHitRate")}</span>
        <b className={rateTone(avgRate)}>{fmtRate(avgRate)}</b>
      </div>
      <Row label={t("composer.usageDetail.nowHitRate")} value={fmtRate(nowRate)} tone={rateTone(nowRate)} />
      <Row
        label={t("composer.usageDetail.cacheRW")}
        value={`${fmtTokens(telHit)} / ${fmtTokens(cacheWrite)}`}
      />
      <div className="usage-pop__sep" />
      <div className="usage-pop__title">{t("composer.usageDetail.session")}</div>
      {!!usage?.cost && usage.cost > 0 && (
        <Row label={t("composer.usageDetail.cost")} value={fmtCost(usage.cost, usage.currency)} />
      )}
      {!!context.sessionCost && context.sessionCost > 0 && (
        <Row
          label={t("composer.usageDetail.sessionCost")}
          value={fmtCost(context.sessionCost, context.sessionCostCurrency)}
        />
      )}
      {!!budget?.rpm && budget.rpm > 0 && (
        <Row
          label={t("composer.usageDetail.rpm")}
          value={`${budget.used}/${budget.rpm}${budget.windowSecs > 0 ? ` · ${Math.round(budget.windowSecs)}s` : ""}`}
        />
      )}
      <Row label={t("composer.usageDetail.requests")} value={requests > 0 ? String(requests) : "—"} />
      {!!context.sessionElapsedMs && context.sessionElapsedMs > 0 && (
        <Row label={t("composer.usageDetail.elapsed")} value={fmtDuration(context.sessionElapsedMs)} />
      )}
      <Row label={t("composer.usageDetail.input")} value={fmtFull(context.sessionPromptTokens ?? 0)} />
      <Row label={t("composer.usageDetail.output")} value={fmtFull(context.sessionCompletionTokens ?? 0)} />
      <Row label={t("composer.usageDetail.reasoning")} value={fmtFull(context.sessionReasoningTokens ?? 0)} />
    </div>
  );

  return (
    <Tooltip label={panel} bodyClassName="usage-pop-body" side="top">
      <div
        className={`composer-usage${hot ? " composer-usage--hot" : ""}`}
        aria-label={`${t("composer.contextUsage")} ${pct}% · ${t("composer.sessionTokens")} ${fmtTokens(session)}${readout ? ` · ${readout}` : ""}`}
      >
        <span className="composer-usage__bar" aria-hidden="true">
          <i style={{ width: `${pct}%` }} />
        </span>
        <span className="composer-usage__text">
          {fmtTokens(session)} · {fmtTokens(context.used)}/{fmtTokens(context.window)}
          {!!budget?.rpm && budget.rpm > 0 && ` · ${budget.used}/${budget.rpm} rpm`}
        </span>
        {/* Token throughput + session cache hit-rate, always on screen (hover
            the chip for the full breakdown). */}
        {!!readout && <span className="composer-usage__readout">{readout}</span>}
        {hot && onCompact && (
          <button
            type="button"
            className="composer-usage__compact"
            onClick={onCompact}
            disabled={disabled}
            title={t("composer.usageDetail.compactHint")}
            aria-label={t("composer.usageDetail.compactHint")}
          >
            {t("composer.usageDetail.compactLabel")}
          </button>
        )}
      </div>
    </Tooltip>
  );
}
