import { memo, useEffect, useRef, useState } from "react";
import { ChevronRight, Loader2, XCircle , ExternalLink } from "lucide-react";
import { CodeViewer } from "./CodeViewer";
import { DiffView } from "./DiffView";
import { UnifiedDiff } from "./editors/UnifiedDiff";
import { openAttachmentViewer } from "./AttachmentViewer";
import { useT } from "../lib/i18n";
import { diffsFor, subjectOf, summarize } from "../lib/tools";
import { toolCardSpec } from "../lib/toolCards";
import { useShellExpand } from "../lib/shellExpand";
import { app } from "../lib/bridge";
import type { Item } from "../lib/useController";
import { getInitialOpenState, shouldKeepMounted } from "./toolcardLogic";

type ToolItem = Extract<Item, { kind: "tool" }>;

function baseName(path: string): string {
  const parts = path.split(/[/\\]/).filter(Boolean);
  return parts.length > 0 ? parts[parts.length - 1] : path;
}

const SUBAGENT_TOOLS = new Set(["task", "run_skill", "explore", "research"]);

/** Lines shown by default in a shell output block: head + tail around the
 *  "… +N lines" marker. Errors live at the END of a log, but build steps
 *  (compile progress, test names) live in the middle — 20+20 keeps the
 *  working context visible without dumping the entire log. */
/** Codex-style output display (spec 1-5 refinement): outputs under
 *  SHELL_FULL_THRESHOLD lines render in full — no fold, no "show more"
 *  button, the card IS the output. Only truly massive logs (verbose build
 *  flags, huge test suites) fold to head+tail. */
const SHELL_FULL_THRESHOLD = 500;
const SHELL_HEAD_LINES = 50;
const SHELL_TAIL_LINES = 50;

function pretty(json: string): string {
  try {
    return JSON.stringify(JSON.parse(json), null, 2);
  } catch {
    return json;
  }
}

function formatToolDuration(ms?: number): string {
  if (typeof ms !== "number" || !Number.isFinite(ms) || ms < 0) return "";
  return `${Math.round(ms)} ms`;
}

// ToolAttachments renders image files a tool produced (e.g. image_generate
// pictures saved under .hiq/attachments/) directly under the tool card.
// Paths can't be loaded by a bare <img> in the webview, so fetch a data URL via
// the kernel — the same bridge UserMessage uses for pasted-image previews.
// Clicking opens the shared lightbox for the full-size view.
function ToolAttachments({ paths }: { paths: string[] }) {
  const [previews, setPreviews] = useState<Record<string, string>>({});
  const key = paths.join("\n");
  useEffect(() => {
    const list = key ? key.split("\n") : [];
    if (list.length === 0) return;
    let cancelled = false;
    for (const p of list) {
      if (previews[p]) continue;
      app.AttachmentDataURL(p)
        .then((url) => { if (!cancelled) setPreviews((prev) => (prev[p] ? prev : { ...prev, [p]: url })); })
        .catch(() => {});
    }
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  return (
    <div className="tool__attachments">
      {paths.map((p) => previews[p] ? (
        <button
          type="button"
          key={p}
          className="tool__attachment-open"
          onClick={() =>
            openAttachmentViewer({
              path: p,
              name: baseName(p),
              kind: "image",
              source: "attachment",
              // A tool result's images browse as one set in the lightbox.
              siblings: paths.map((sibling) => ({ path: sibling, name: baseName(sibling), kind: "image", source: "attachment", previewUrl: previews[sibling] })),
            })
          }
        >
          <img src={previews[p]} alt={baseName(p)} loading="lazy" draggable={false} />
        </button>
      ) : null)}
    </div>
  );
}

/** Splits long output into head+tail around a "… +N lines" marker line; short
 *  output passes through unchanged. hasMore drives the show-all toggle. */
function splitPreview(text: string, head: number, tail: number, fullThreshold = Infinity): { preview: string; total: number; hasMore: boolean } {
  const lines = text.split("\n");
  const total = lines.length;
  if (total <= fullThreshold) return { preview: text, total, hasMore: false };
  if (total <= head + tail + 1) return { preview: text, total, hasMore: false };
  const hidden = total - head - tail;
  const marker = `… +${hidden} lines`;
  return { preview: [...lines.slice(0, head), marker, ...lines.slice(-tail)].join("\n"), total, hasMore: true };
}

// ToolCard renders one tool call. `subcalls` are sub-agent calls nested under a
// `task` card (their ParentID points at this call); they render inline, live, so
// the sub-agent's work is visible as it happens.
export const ToolCard = memo(function ToolCard({ item, subcalls }: { item: ToolItem; subcalls?: ToolItem[] }) {
  const t = useT();
  // Per-tool spec (registry, upgrade spec 1-1): a registered body replaces the
  // generic args/output body; forceOpen/noQuiet tune the shared shell.
  const spec = toolCardSpec(item.name);
  const customBody = spec?.body?.(item);
  // Server-side diff is authoritative (covers apply_patch and encoding-aware
  // previews); the args-derived diffsFor pairs stay as the fallback for
  // replayed sessions whose sidecar predates the field.
  const serverDiff = item.fileDiff?.diff ? item.fileDiff : undefined;
  const diffs = serverDiff ? [] : diffsFor(item.name, item.args);
  const subject = subjectOf(item.name, item.args);
  // First path-ish arg for the "open in editor" affordance (spec 5-4).
  const editPath = (() => {
    try {
      const a = JSON.parse(item.args || "{}");
      const p = typeof a.path === "string" ? a.path : typeof a.file_path === "string" ? a.file_path : "";
      return p && !p.includes("\n") ? p : "";
    } catch {
      return "";
    }
  })();
  const nested = subcalls ?? [];
  const hasNested = nested.length > 0;
  const profileText =
    SUBAGENT_TOOLS.has(item.name) && item.profile
      ? [item.profile.model, item.profile.effort ? `effort ${item.profile.effort}` : ""].filter(Boolean).join(" · ")
      : "";
  // Header meta line: line tallies for writers (server preview first), output
  // shape for read tools (N matches / N lines), once the call has settled.
  const statText = serverDiff
    ? `+${serverDiff.added} -${serverDiff.removed}`
    : item.status === "running"
      ? ""
      : summarize(item.name, item.args, item.output, item.error);

  // A task's summary is its step count; everything else derives from the result.
  const summary =
    item.status === "running"
      ? ""
      : hasNested
        ? t(nested.length === 1 ? "tool.stepOne" : "tool.stepOther", { n: nested.length })
        : "";

  // edit diffs are the point of the card, so they're shown inline; everything
  // else folds its args/output away by default.  Open while running so the
  // user sees progress; closed by default once settled (registry forceOpen
  // tools — netdev evidence — stay open).
  const hasDiffBody = Boolean(serverDiff || item.argsDiff) || diffs.length > 0;
  const hasCustomBody = customBody !== undefined && customBody !== null;
  const hasArgsOrOutput = !hasDiffBody && !hasCustomBody && (!!item.args || !!item.output);

  // Shell output: head+tail preview + "show all" toggle.
  const shellOutput = item.isShell && item.output ? item.output : null;
  const shellPreview = shellOutput ? splitPreview(shellOutput, SHELL_HEAD_LINES, SHELL_TAIL_LINES, SHELL_FULL_THRESHOLD) : null;
  const hasAttachments = Boolean(item.attachments && item.attachments.some((a) => a.kind === "image"));
  const hasBody = Boolean(summary || hasDiffBody || hasCustomBody || hasNested || shellPreview || (!shellPreview && hasArgsOrOutput) || item.error || hasAttachments);
  // Open while running so the user sees live progress; closed once settled.
  // Shell cards (incl. agent-initiated bash) follow the same rule so streamed
  // stdout stays visible during a long command and auto-collapses on finish.
  const [open, setOpen] = useState(getInitialOpenState(item.status, hasNested, item.isShell ?? false, Boolean(item.output)) || (spec?.forceOpen ?? false));
  const [hasBeenOpened, setHasBeenOpened] = useState(open);
  const [showAll, setShowAll] = useState(false);
  
  useEffect(() => {
    if (open && !hasBeenOpened) {
      setHasBeenOpened(true);
    }
  }, [open, hasBeenOpened]);
  // Track whether the user has manually toggled this card, so the auto-open /
  // auto-close effect below doesn't fight a deliberate interaction.
  const userToggledRef = useRef(false);
  const shellBodyRef = useRef<HTMLDivElement | null>(null);

  // Register this shell card's toggle with the global ShellExpand context so
  // Ctrl/Cmd+B can expand/collapse the most recent shell output.
  const shellExpand = useShellExpand();
  useEffect(() => {
    if (!item.isShell || !shellExpand) return;
    return shellExpand.register(item.id, () => {
      userToggledRef.current = true;
      setOpen((v) => !v);
    });
  }, [item.isShell, item.id, shellExpand]);

  // Auto-open shell cards while running so streamed chunks are visible. Once
  // running, we leave the card alone — do NOT auto-collapse on completion, so a
  // long command's output stays readable without the user having to re-expand.
  // We only respect a manual toggle (Ctrl/Cmd+B or the header) thereafter.
  useEffect(() => {
    if (!item.isShell || userToggledRef.current) return;
    if (item.status === "running" && !open) setOpen(true);
  }, [item.isShell, item.status, open]);

  // Keep the shell output pinned to the bottom as new chunks stream in, so the
  // latest lines (where errors appear) are always in view while running.
  useEffect(() => {
    if (!item.isShell || !open || item.status !== "running") return;
    const el = shellBodyRef.current?.querySelector("pre.code") as HTMLElement | null;
    if (el) el.scrollTop = el.scrollHeight;
  }, [item.isShell, open, item.status, item.output]);

  // Read-only "research" calls (read/grep/web_fetch) are hidden after
  // completion so they don't clutter the transcript. During execution they still
  // render so the user sees progress. Registry noQuiet tools (netdev evidence)
  // keep full contrast.
  const quiet =
    item.readOnly && !hasNested && !spec?.noQuiet && item.status !== "error" && item.status !== "stopped";

  const duration = item.status === "running" ? "" : formatToolDuration(item.durationMs);

  return (
    <div className={`tool${quiet ? " tool--quiet" : ""}`}>
      <button
        type="button"
        className="tool__head"
        data-running={item.status === "running" ? "" : undefined}
        onClick={() => {
          if (!hasBody) return;
          userToggledRef.current = true;
          setOpen((v) => !v);
        }}
        aria-expanded={hasBody ? open : undefined}
      >
        {item.status === "running" && <Loader2 size={13} className="tool__status tool__status--running" />}
        {item.status === "error" && <XCircle size={13} className="tool__status tool__status--error" />}
        <span className="tool__label-group">
          <span className="tool__name">{item.name}</span>
          {subject && <span className="tool__subject">{subject}</span>}
        </span>
        {statText && <span className="tool__stat">{statText}</span>}
        {editPath && (
          <button
            type="button"
            className="tool__editor-open"
            title={`${editPath} — 在编辑器中打开`}
            onClick={(e) => { e.stopPropagation(); void app.OpenInEditorAt(editPath, 0).catch(() => {}); }}
          >
            <ExternalLink size={11} />
          </button>
        )}
        {profileText && <span className="tool__profile">{profileText}</span>}
        {duration && <span className="tool__duration">{duration}</span>}
        {hasBody && (
          <span className={`tool__chevron${open ? " tool__chevron--open" : ""}`}>
            <ChevronRight size={12} />
          </span>
        )}
      </button>

      <div className={`tool__body-wrapper ${open ? "open" : "closed"}`}>
        {shouldKeepMounted(hasBeenOpened, open) && (
          <div className="tool__body">
            {summary && <div className="tool__summary">{summary}</div>}

        {hasCustomBody && customBody}

        {serverDiff && (
          <UnifiedDiff value={serverDiff.diff} maxHeight={480} />
        )}

        {item.argsDiff && (
          <div className="tool__argsdiff">
            <div className="tool__argsdiff-head">
              <Loader2 size={11} className="tool__status--running" />
              <span>补丁生成中——实时预览</span>
            </div>
            <UnifiedDiff value={item.argsDiff} maxHeight={280} showToggle={false} />
          </div>
        )}

        {diffs.map((d, i) => (
          <div key={i}>
            {d.label && <div className="tool__difflabel">{d.label}</div>}
            <DiffView original={d.original} modified={d.modified} language={d.lang} maxHeight={260} />
          </div>
        ))}

        {hasNested && (
          <div className="tool__nested">
            {nested.map((c) => (
              <ToolCard key={c.id} item={c} />
            ))}
          </div>
        )}

        {shellPreview && (
          <div ref={shellBodyRef}>
            <CodeViewer value={showAll ? shellOutput! : shellPreview.preview} maxHeight={showAll ? 800 : 600} />
            {shellPreview.hasMore && !showAll && (
              <button className="tool__showall" onClick={() => setShowAll(true)}>
                {t("tool.showAllLines", { n: shellPreview.total })}
              </button>
            )}
            {item.truncated && <div className="tool__note">{t("tool.truncated")}</div>}
          </div>
        )}

        {!shellPreview && hasArgsOrOutput && (
          <>
            {item.args && <CodeViewer value={pretty(item.args)} language="json" maxHeight={180} />}
            {item.output && (
              <>
                <CodeViewer value={item.output} maxHeight={600} />
                {item.truncated && <div className="tool__note">{t("tool.truncated")}</div>}
              </>
            )}
          </>
        )}

          {item.error && <div className="tool__err">{item.error}</div>}
          </div>
        )}
      </div>

      {item.attachments && item.attachments.filter((a) => a.kind === "image").length > 0 && (
        <ToolAttachments paths={item.attachments!.filter((a) => a.kind === "image").map((a) => a.path)} />
      )}
    </div>
  );
});
