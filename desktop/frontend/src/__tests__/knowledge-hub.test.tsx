// @vitest-environment jsdom
// knowledge-hub.test.tsx — 项目知识中枢面板冒烟测试。
// 面板经 bridge 的浏览器 dev mock 取数（无 window.go → 自动走 mock），因此这里
// 覆盖：标题与四源开关、状态元信息、地图正文加载、搜索框存在、以及「同步」后
// 修订历史多出一条。断言尽量落在 container 的类名/结构上，避免中英文文案耦合。
import { describe, expect, it } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { KnowledgeHub } from "../components/cowork/KnowledgeHub";
import { LocaleProvider } from "../lib/i18n";

function mount() {
  return render(
    <LocaleProvider>
      <KnowledgeHub />
    </LocaleProvider>,
  );
}

describe("KnowledgeHub（项目知识中枢）", () => {
  it("renders title, the four source switches and the status meta", async () => {
    const { container } = mount();
    await waitFor(() => expect(container.querySelector(".kb__title")).toBeTruthy());
    expect(screen.getByText("Project Knowledge Hub")).toBeTruthy();

    // Four source chips, each with a stable kind order (code/doc/memory/team).
    // Scoped to .kb__sources — the header also carries a watch-status chip.
    await waitFor(() => expect(container.querySelectorAll(".kb__sources .kb-chip").length).toBe(4));
    const labels = [...container.querySelectorAll(".kb__sources .kb-chip")].map((el) => el.textContent ?? "");
    expect(labels.join(" ")).toMatch(/代码|Code/);
    expect(labels.join(" ")).toMatch(/团队|Team/);

    // Meta reports totals (mock: 42+18+9+6 = 75 entries).
    expect(container.querySelector(".kb__meta")?.textContent ?? "").toMatch(/75/);
  });

  it("loads the project map body and offers search", async () => {
    const { container } = mount();
    await waitFor(() => {
      const body = container.querySelector(".kb__map-body");
      expect(body?.textContent ?? "").toContain("项目地图");
    });
    expect(container.querySelector(".kb__search-input")).toBeTruthy();
  });

  it("sync appends a revision row to the history", async () => {
    const { container } = mount();
    await waitFor(() => expect(container.querySelector(".kb__sync")).toBeTruthy());
    expect(container.querySelectorAll(".kb-rev").length).toBe(0);

    fireEvent.click(container.querySelector(".kb__sync") as HTMLElement);

    await waitFor(() => expect(container.querySelectorAll(".kb-rev").length).toBeGreaterThan(0));
  });

  it("toggling a source chip flips its enabled class", async () => {
    const { container } = mount();
    const chipSel = ".kb__sources .kb-chip";
    await waitFor(() => expect(container.querySelectorAll(chipSel).length).toBe(4));
    const chip = container.querySelector(chipSel) as HTMLElement;
    const wasOn = chip.classList.contains("kb-chip--on");
    fireEvent.click(chip);
    await waitFor(() => {
      const now = (container.querySelector(chipSel) as HTMLElement).classList.contains("kb-chip--on");
      expect(now).toBe(!wasOn);
    });
  });

  it("watch toggle (变更即同步) reflects and flips the watching state", async () => {
    const { container } = mount();
    const btn = await waitFor(() => {
      const b = container.querySelector(".kb__watch") as HTMLElement | null;
      expect(b).toBeTruthy();
      return b as HTMLElement;
    });
    // Mock starts watching=true → the toggle carries the active modifier.
    expect(btn.classList.contains("kb__watch--on")).toBe(true);
    fireEvent.click(btn);
    await waitFor(() => {
      expect((container.querySelector(".kb__watch") as HTMLElement).classList.contains("kb__watch--on")).toBe(false);
    });
  });

  it("renders the incremental-summary panel", async () => {
    const { container } = mount();
    await waitFor(() => expect(container.querySelector(".kb__summary")).toBeTruthy());
    // The panel always renders a body (bullets or the "no changes" note).
    expect(container.querySelector(".kb__summary-body")?.textContent ?? "").not.toBe("");
  });
});
