// @vitest-environment jsdom
// usage-chip.test.tsx — UsageChip 的「窗口未知」分支守护测试。
//
// 背景（2026-09-20 用户实测）：渠道没配 context_window 时后端报告 window=0，
// 旧的 `if (!context.window) return null` 会让整块读数消失——输入框右下角只
// 剩 "26 tok/s · 缓存 96%" 孤零零挂着，用户无法分辨这是配置没填还是功能坏了
// （实测就是 octpus / 小米财财 两个渠道没填，而 yyqwen 填了 131072 所以正常）。
// 现在改为保留槽位：显示会话累计 + 「窗口未配置」，没有窗口就不画进度条。
//
// 断言落在类名与数值上，不依赖中英文文案（文案会随语言变）。
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render } from "@testing-library/react";
import { UsageChip } from "../components/composer/UsageChip";
import { LocaleProvider } from "../lib/i18n";
import type { ContextInfo } from "../lib/types";

afterEach(cleanup);

function ctx(over: Partial<ContextInfo>): ContextInfo {
  return { used: 0, window: 0, sessionTokens: 0, ...over };
}

function renderChip(context?: ContextInfo, readout?: string) {
  return render(
    <LocaleProvider>
      <UsageChip context={context} readout={readout} />
    </LocaleProvider>,
  );
}

describe("UsageChip — window unset", () => {
  it("keeps the slot instead of vanishing, and drops the fill bar", () => {
    const { container } = renderChip(ctx({ used: 32000, sessionTokens: 32000, window: 0 }), "3 tok/s");
    const chip = container.querySelector(".composer-usage");
    expect(chip).not.toBeNull();
    expect(chip?.classList.contains("composer-usage--nowindow")).toBe(true);
    // No bar without a window — a 0-width bar would read as "0% full".
    expect(container.querySelector(".composer-usage__bar")).toBeNull();
    // Session total and the throughput readout both survive.
    expect(chip?.textContent).toContain("32k");
    expect(chip?.textContent).toContain("3 tok/s");
  });

  it("renders the fill bar and the used/window pair once a window is known", () => {
    const { container } = renderChip(ctx({ used: 32000, sessionTokens: 32000, window: 131072 }), "3 tok/s");
    const chip = container.querySelector(".composer-usage");
    expect(chip?.classList.contains("composer-usage--nowindow")).toBe(false);
    expect(container.querySelector(".composer-usage__bar")).not.toBeNull();
    expect(chip?.textContent).toContain("32k · 32k/131k");
  });

  it("still shows the throughput readout when there is no context info at all", () => {
    const { container } = renderChip(undefined, "3 tok/s");
    const chip = container.querySelector(".composer-usage");
    expect(chip?.textContent).toBe("3 tok/s");
    expect(container.querySelector(".composer-usage__bar")).toBeNull();
  });
});
