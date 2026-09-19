// turn-stream-chars.test.ts — the data path behind the live tok/s readout.
//
// A provider reports real token counts only when a response *finishes*, so an
// in-flight figure has to be estimated from the characters that actually
// streamed. These tests pin the reducer half of that: what gets counted, when
// the counter resets, and how the chars→tokens correction is learned (and why
// only the first exact count of a turn is allowed to move it).
import { describe, expect, it } from "vitest";
import { initialState, reducer } from "../lib/useController";
import type { WireEvent, WireUsage } from "../lib/types";

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

// A turn in flight: the user message plus the backend's acknowledgement.
function started() {
  let s = reducer(initialState, { type: "user", text: "写一段说明", seq: 0 });
  s = reducer(s, { type: "event", e: { kind: "turn_started" } as WireEvent });
  return s;
}

function stream(s: ReturnType<typeof started>, ...events: WireEvent[]) {
  for (const e of events) s = reducer(s, { type: "event", e });
  return s;
}

describe("streamed character accounting", () => {
  it("counts assistant text, split into CJK and the rest", () => {
    const latin = " is O(log n)";
    const s = stream(started(), { kind: "text", text: "二分查找" } as WireEvent, { kind: "text", text: latin } as WireEvent);
    expect(s.turnChars).toBe(4 + latin.length);
    expect(s.turnCjkChars).toBe(4);
  });

  it("counts reasoning too — it is output the provider bills for", () => {
    const s = stream(started(), { kind: "reasoning", reasoning: "先分析一下" } as WireEvent);
    expect(s.turnChars).toBe(5);
    expect(s.turnCjkChars).toBe(5);
  });

  it("counts a streamed patch preview, which is file content the model wrote", () => {
    const patch = "@@ -1 +1 @@\n-old\n+new\n";
    const s = stream(started(), { kind: "tool_args_delta", text: patch, tool: { id: "t1", name: "write" } } as WireEvent);
    expect(s.turnChars).toBe(patch.length);
    expect(s.turnCjkChars).toBe(0);
  });

  it("starts each turn from zero rather than carrying the last one's totals", () => {
    const after = stream(started(), { kind: "text", text: "第一轮的正文" } as WireEvent);
    const next = reducer(after, { type: "event", e: { kind: "turn_started" } as WireEvent });
    expect(next.turnChars).toBe(0);
    expect(next.turnCjkChars).toBe(0);
  });
});

describe("learned chars→tokens correction", () => {
  it("starts neutral and learns from the first exact count of a turn", () => {
    expect(initialState.tokenScale).toBe(1);
    // 160 CJK chars estimate to 100 tokens; the provider says 200 were really
    // produced, i.e. the default rate was half what it should be.
    const s = stream(started(), { kind: "text", text: "中".repeat(160) } as WireEvent, { kind: "usage", usage: usage({ completionTokens: 200 }) } as WireEvent);
    expect(s.tokenScale).toBeCloseTo(1.5, 6);
  });

  it("ignores later rounds of the same turn", () => {
    // By then `turnChars` spans every round while `completionTokens` covers
    // only the round that just finished — a ratio of the two is meaningless.
    let s = stream(started(), { kind: "text", text: "中".repeat(160) } as WireEvent, { kind: "usage", usage: usage({ completionTokens: 200 }) } as WireEvent);
    const learned = s.tokenScale;
    s = stream(s, { kind: "text", text: "中".repeat(10) } as WireEvent, { kind: "usage", usage: usage({ completionTokens: 10 }) } as WireEvent);
    expect(s.tokenScale).toBe(learned);
  });

  it("leaves the correction alone when the response streamed no text", () => {
    // A tool-only round has no visible text to calibrate against.
    const s = stream(started(), { kind: "usage", usage: usage({ completionTokens: 500 }) } as WireEvent);
    expect(s.tokenScale).toBe(1);
  });

  it("survives a nonsense sample instead of reflecting it", () => {
    const s = stream(started(), { kind: "text", text: "x".repeat(4) } as WireEvent, { kind: "usage", usage: usage({ completionTokens: 100000 }) } as WireEvent);
    expect(s.tokenScale).toBeLessThanOrEqual(5);
  });
});
