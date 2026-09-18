// @vitest-environment jsdom
// attachment-viewer.test.tsx — 全屏媒体查看器冒烟测试。
//
// 覆盖三件事：相邻切换（‹ › 按钮 + 左右方向键，含首尾环绕）、键盘缩放
// （+/-/0）与切换文件后复位视图、以及 Esc 关闭。图片解析统一走 mock 的
// AttachmentDataURL，断言落在类名/数值上，避免与中英文文案强耦合。
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { LocaleProvider } from "../lib/i18n";

vi.mock("../lib/bridge", async (importOriginal) => {
  const orig = await importOriginal<typeof import("../lib/bridge")>();
  return {
    ...orig,
    app: {
      AttachmentDataURL: async (p: string) => `data:image/png;base64,${encodeURIComponent(p)}`,
      ReadFile: async (p: string) => ({ kind: "image", url: `file:///${p}`, body: "", binary: false, err: "", truncated: false }),
      OpenWorkspacePath: async () => {},
      RevealWorkspacePath: async () => {},
    },
  };
});

// jsdom may not provide rAF; the viewer recentres through it on file/zoom change.
(globalThis as unknown as { requestAnimationFrame: (cb: FrameRequestCallback) => number }).requestAnimationFrame = (cb) =>
  setTimeout(() => cb(0), 0) as unknown as number;
(globalThis as unknown as { cancelAnimationFrame: (id: number) => void }).cancelAnimationFrame = (id) => clearTimeout(id as unknown as ReturnType<typeof setTimeout>);

const { AttachmentViewer, openAttachmentViewer, closeAttachmentViewer } = await import("../components/AttachmentViewer");

function img(name: string) {
  return { path: `.hiq/attachments/${name}`, name, kind: "image" as const, source: "attachment" as const };
}

function mount() {
  return render(
    <LocaleProvider>
      <AttachmentViewer />
    </LocaleProvider>,
  );
}

const name = (c: HTMLElement) => c.querySelector(".attachment-viewer__name")?.textContent ?? "";
const zoomText = (c: HTMLElement) => c.querySelector(".attachment-viewer__zoomval")?.textContent ?? "";

afterEach(() => {
  cleanup();
  closeAttachmentViewer();
});

describe("AttachmentViewer（媒体查看器）", () => {
  it("switches to the adjacent media via the ‹ › buttons, wrapping at the ends", async () => {
    const a = img("a.png");
    const b = img("b.png");
    const c = img("c.png");
    openAttachmentViewer({ ...a, siblings: [a, b, c] });
    const { container } = mount();

    await waitFor(() => expect(name(container)).toBe("a.png"));
    const navs = container.querySelectorAll(".attachment-viewer__nav");
    expect(navs.length).toBe(2); // prev + next

    fireEvent.click(container.querySelector(".attachment-viewer__nav--next")!);
    await waitFor(() => expect(name(container)).toBe("b.png"));

    fireEvent.click(container.querySelector(".attachment-viewer__nav--next")!);
    await waitFor(() => expect(name(container)).toBe("c.png"));

    // Forward from the last wraps to the first, and back from the first wraps
    // to the last — the gallery is a ring, not a dead end.
    fireEvent.click(container.querySelector(".attachment-viewer__nav--next")!);
    await waitFor(() => expect(name(container)).toBe("a.png"));
    fireEvent.click(container.querySelector(".attachment-viewer__nav--prev")!);
    await waitFor(() => expect(name(container)).toBe("c.png"));
  });

  it("switches with the Left/Right arrow keys", async () => {
    const a = img("a.png");
    const b = img("b.png");
    openAttachmentViewer({ ...a, siblings: [a, b] });
    const { container } = mount();
    await waitFor(() => expect(name(container)).toBe("a.png"));

    fireEvent.keyDown(window, { key: "ArrowRight" });
    await waitFor(() => expect(name(container)).toBe("b.png"));

    fireEvent.keyDown(window, { key: "ArrowLeft" });
    await waitFor(() => expect(name(container)).toBe("a.png"));
  });

  it("zooms the image with +/-/0 and reports the percentage", async () => {
    const a = img("a.png");
    openAttachmentViewer({ ...a, siblings: [a] });
    const { container } = mount();
    await waitFor(() => expect(container.querySelector(".attachment-viewer__stage")).toBeTruthy());

    expect(zoomText(container)).toBe("100%");
    fireEvent.keyDown(window, { key: "+" });
    await waitFor(() => expect(zoomText(container)).toBe("150%"));
    // A zoomed-in image becomes pannable (drag affordance turns on).
    expect(container.querySelector(".attachment-viewer__stage")?.className).toContain("--pannable");

    fireEvent.keyDown(window, { key: "-" });
    await waitFor(() => expect(zoomText(container)).toBe("100%"));
    expect(container.querySelector(".attachment-viewer__stage")?.className).not.toContain("--pannable");

    fireEvent.keyDown(window, { key: "+" });
    fireEvent.keyDown(window, { key: "+" });
    await waitFor(() => expect(zoomText(container)).toBe("200%"));
    fireEvent.keyDown(window, { key: "0" });
    await waitFor(() => expect(zoomText(container)).toBe("100%"));
  });

  it("resets zoom to 1:1 when moving to the next file", async () => {
    const a = img("a.png");
    const b = img("b.png");
    openAttachmentViewer({ ...a, siblings: [a, b] });
    const { container } = mount();
    await waitFor(() => expect(name(container)).toBe("a.png"));

    fireEvent.keyDown(window, { key: "+" });
    await waitFor(() => expect(zoomText(container)).toBe("150%"));

    fireEvent.keyDown(window, { key: "ArrowRight" });
    await waitFor(() => expect(name(container)).toBe("b.png"));
    await waitFor(() => expect(zoomText(container)).toBe("100%"));
  });

  it("closes on Escape", async () => {
    const a = img("a.png");
    openAttachmentViewer({ ...a, siblings: [a] });
    const { container } = mount();
    await waitFor(() => expect(container.querySelector(".attachment-viewer")).toBeTruthy());

    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(container.querySelector(".attachment-viewer")).toBeNull());
  });

  it("hides the nav controls for a lone file but keeps zoom", async () => {
    const a = img("a.png");
    openAttachmentViewer({ ...a, siblings: [a] }); // single-element set → no siblings
    const { container } = mount();
    await waitFor(() => expect(container.querySelector(".attachment-viewer__stage")).toBeTruthy());

    expect(container.querySelectorAll(".attachment-viewer__nav").length).toBe(0);
    expect(container.querySelector(".attachment-viewer__zoombar")).toBeTruthy();
  });
});
