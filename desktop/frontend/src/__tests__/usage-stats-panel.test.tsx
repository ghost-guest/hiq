// @vitest-environment jsdom
// usage-stats-panel.test.tsx — 用量统计面板冒烟测试。
//
// 面板只经 bridge 的 app.UsageStats（Go 侧的 App.UsageStats 聚合）取数，所以这
// 里用 vi.mock 换掉 app，覆盖：总计卡片（token/轮次/请求/活跃天数/缓存命中率/
// 最常用模型）、热力图与模型占比是否渲染、切换时间范围与统计来源是否重新取数、
// 以及全 0 数据的空态。断言落在类名与数值上，避免与中英文文案耦合过深。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { LocaleProvider } from "../lib/i18n";
import type { UsageStatsRange, UsageStatsRequest } from "../lib/types";

const host = vi.hoisted(() => ({
  calls: [] as UsageStatsRequest[],
  payload: null as UsageStatsRange | null,
}));

vi.mock("../lib/bridge", async (importOriginal) => {
  const orig = await importOriginal<typeof import("../lib/bridge")>();
  return {
    ...orig,
    app: {
      UsageStats: async (req: UsageStatsRequest) => {
        host.calls.push(req);
        return host.payload!;
      },
    },
  };
});

// jsdom has no ResizeObserver; the heatmap and the fitting card values observe
// their containers. A no-op implementation is enough — the layout maths falls
// back to the element's (zero) client width.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = ResizeObserverStub;

const { UsageStatsPanel } = await import("../components/UsageStatsPanel");

function day(offset: number): string {
  const d = new Date();
  d.setDate(d.getDate() + offset);
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const dayOfMonth = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${dayOfMonth}`;
}

const richStats: UsageStatsRange = {
  from: "2026-09-11",
  to: "2026-09-17",
  tokens: 1500000,
  requests: 42,
  turns: 17,
  cacheHit: 900000,
  cacheMiss: 100000,
  activeDays: 5,
  topModel: "deepseek/deepseek-v4.1-flash",
  topProvider: "deepseek",
  daily: [
    {
      day: day(-1),
      total: 800000,
      byModel: { "deepseek/deepseek-v4.1-flash": 800000 },
      byProvider: { deepseek: 800000 },
      requests: 20,
      turns: 8,
      cacheHit: 700000,
      cacheMiss: 100000,
    },
    {
      day: day(0),
      total: 700000,
      byModel: { "deepseek/deepseek-v4.1-flash": 700000 },
      byProvider: { deepseek: 700000 },
      requests: 22,
      turns: 9,
      cacheHit: 200000,
      cacheMiss: 0,
    },
  ],
  models: [{ model: "deepseek/deepseek-v4.1-flash", provider: "deepseek", tokens: 1500000, percent: 100 }],
  providers: [{ provider: "deepseek", tokens: 1500000, percent: 100 }],
};

const emptyStats: UsageStatsRange = {
  from: "",
  to: "",
  tokens: 0,
  requests: 0,
  turns: 0,
  cacheHit: 0,
  cacheMiss: 0,
  activeDays: 0,
  topModel: "",
  topProvider: "",
  daily: [],
  models: [],
  providers: [],
};

function mount() {
  return render(
    <LocaleProvider>
      <UsageStatsPanel />
    </LocaleProvider>,
  );
}

beforeEach(() => {
  host.calls.length = 0;
  host.payload = null;
});

// This suite has no vitest globals, so testing-library never registers its
// automatic cleanup: without this,每个 it 的渲染都会留在 document.body 里，于是
// 第二个测试的 getByText 会同时匹配到前面测试留下的同名按钮。
afterEach(cleanup);

describe("UsageStatsPanel（用量统计）", () => {
  it("renders the totals, cache hit rate and top model from the aggregate", async () => {
    host.payload = richStats;
    const { container } = mount();

    await waitFor(() => expect(container.querySelector(".usage-stats__cards")).toBeTruthy());
    const cards = container.querySelectorAll(".usage-stats__card");
    expect(cards.length).toBe(6);

    const text = container.querySelector(".usage-stats__cards")?.textContent ?? "";
    expect(text).toContain("1,500,000"); // token total, exact digits
    expect(text).toContain("17"); // completed turns
    expect(text).toContain("42"); // provider requests
    expect(text).toContain("90.0%"); // 900k hit / (900k + 100k) miss, one decimal
    expect(text).toContain("deepseek/deepseek-v4.1-flash");
  });

  it("draws the heatmap and the per-model split", async () => {
    host.payload = richStats;
    const { container } = mount();

    await waitFor(() => expect(container.querySelector(".usage-stats__heatmap")).toBeTruthy());
    expect(container.querySelector(".usage-stats__heatmap")?.querySelectorAll("rect").length ?? 0).toBeGreaterThan(0);
    const modelList = container.querySelector(".usage-stats__model-list");
    expect(modelList?.textContent ?? "").toContain("deepseek/deepseek-v4.1-flash");
  });

  it("asks the backend for the range default and re-queries when a preset is picked", async () => {
    host.payload = richStats;
    const { container } = mount();
    await waitFor(() => expect(host.calls.length).toBeGreaterThan(0));

    // The panel opens on the 30-day preset.
    expect(host.calls.some((c) => c.range === "30")).toBe(true);

    fireEvent.click(screen.getByText("Last 7 days"));
    await waitFor(() => expect(host.calls.some((c) => c.range === "7")).toBe(true));

    // Sanity: the newest request carries the freshly picked range.
    expect(host.calls[host.calls.length - 1]?.range).toBe("7");
    expect(container.querySelector(".usage-stats__cards")).toBeTruthy();
  });

  it("re-queries with the chosen source label when the source filter changes", async () => {
    host.payload = richStats;
    mount();
    await waitFor(() => expect(host.calls.length).toBeGreaterThan(0));
    expect(host.calls[0]?.source).toBe("all");

    fireEvent.click(screen.getByText("CLI"));
    await waitFor(() => expect(host.calls.some((c) => c.source === "cli")).toBe(true));
  });

  it("shows the empty state for an all-zero range and still draws a blank heatmap", async () => {
    host.payload = emptyStats;
    const { container } = mount();

    await waitFor(() => expect(container.querySelector(".usage-stats__empty")).toBeTruthy());
    // The heatmap is a fixed 40-week window, so it renders (all-zero cells)
    // even with no data; the per-model donut and the trend legend do not.
    expect(container.querySelector(".usage-stats__heatmap")).toBeTruthy();
    expect(container.querySelector(".usage-stats__model-list")).toBeNull();
    // Cache hit rate has no meaning without input tokens.
    expect(container.querySelector(".usage-stats__cards")?.textContent ?? "").toContain("—");
  });
});
