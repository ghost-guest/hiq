// @vitest-environment jsdom
// model-switcher.test.tsx — 输入框模型选择器回归测试。
// 历史 bug：flatMode 条件写成了 models.length <= 5，而平铺分支只渲染
// `visibleCats[0]`（按字母序排第一的渠道）——多渠道但模型总数 ≤5 时，
// 其余渠道的模型被整组吞掉（用户看到"装了两个渠道却只有一个模型"）。
// 本测试锁定：来自多个渠道的模型必须全部渲染，行内带渠道标注。
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@testing-library/react";
import { ModelSwitcher } from "../components/ModelSwitcher";
import { LocaleProvider } from "../lib/i18n";

const multiProviderModels = vi.hoisted(() => [
  { ref: "yyqwen/Qwen3.6-35B-A3B-FP8", provider: "yyqwen", model: "Qwen3.6-35B-A3B-FP8", current: true },
  { ref: "woaichifan/deepseek-v4.1-flash", provider: "woaichifan", model: "deepseek-v4.1-flash", current: false },
]);

vi.mock("../lib/bridge", async (importOriginal) => {
  const orig = await importOriginal<typeof import("../lib/bridge")>();
  return {
    ...orig,
    app: {
      Models: async () => multiProviderModels,
      ModelsForTab: async () => multiProviderModels,
    },
  };
});

function mount() {
  return render(
    <LocaleProvider>
      <ModelSwitcher label="Qwen3.6-35B-A3B-FP8" onPick={() => {}} />
    </LocaleProvider>,
  );
}

describe("ModelSwitcher（模型选择器）", () => {
  it("shows every provider's models when the flat list spans multiple channels", async () => {
    const { container } = mount();
    fireEvent.click(container.querySelector(".modelsw__trigger") as HTMLElement);

    // The menu renders into a portal (document.body), so query the body.
    await waitFor(() => expect(document.body.querySelectorAll(".modelsw__item").length).toBe(2));
    const text = [...document.body.querySelectorAll(".modelsw__item")].map((el) => el.textContent ?? "").join("\n");
    expect(text).toContain("Qwen3.6-35B-A3B-FP8");
    expect(text).toContain("deepseek-v4.1-flash");
    // Each row carries its channel attribution.
    expect(document.body.querySelectorAll(".modelsw__provider").length).toBe(2);
    expect(document.body.querySelector(".modelsw__empty")).toBeNull();
  });
});
