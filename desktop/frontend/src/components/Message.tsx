import { memo, useEffect, useMemo, useState } from "react";
import { ChevronDown, ChevronRight, FileText, Folder, GitBranch, Image, MessageSquare, RotateCcw, ScrollText } from "lucide-react";
import { Markdown } from "./Markdown";
import { CopyButton } from "./CopyButton";
import { ProcessBrainIcon } from "./ProcessCard";
import { openAttachmentViewer } from "./AttachmentViewer";
import { parseAttachmentRefsForDisplay, sortDisplayAttachments } from "../lib/attachmentDisplay";
import { app } from "../lib/bridge";
import { UnifiedDiff } from "./editors/UnifiedDiff";
import { useT } from "../lib/i18n";
import type { Item, MessageActionScope } from "../lib/useController";
import type { CheckpointMeta } from "../lib/types";

type AssistantItem = Extract<Item, { kind: "assistant" }>;
export type TurnActionMenu = "summary" | "rewind";
type ImSourceMessage = {
  provider: string;
  label: string;
  sender: string;
  chat: string;
  text: string;
};

const IM_SOURCE_START = "[[hiq-im]]";
const IM_SOURCE_END = "[[/hiq-im]]";

function parseImSourceMessage(text: string): ImSourceMessage | null {
  // Display-only metadata: keep IM sender/chat details out of model prompts.
  if (!text.startsWith(IM_SOURCE_START)) return null;
  const end = text.indexOf(IM_SOURCE_END);
  if (end < 0) return null;
  const metaBlock = text.slice(IM_SOURCE_START.length, end).trim();
  const body = text.slice(end + IM_SOURCE_END.length).replace(/^\r?\n/, "");
  const meta: Record<string, string> = {};
  for (const line of metaBlock.split(/\r?\n/)) {
    const index = line.indexOf("=");
    if (index <= 0) continue;
    const key = line.slice(0, index).trim().toLowerCase();
    const value = line.slice(index + 1).trim();
    if (key) meta[key] = value;
  }
  return {
    provider: meta.provider || "",
    label: meta.label || "",
    sender: meta.sender || meta.senderid || "",
    chat: meta.chat || meta.chat_type || "",
    text: body,
  };
}

function imSourceLabel(source: ImSourceMessage, t: ReturnType<typeof useT>): string {
  if (source.label.trim()) return source.label.trim();
  const provider = source.provider.trim().toLowerCase();
  if (provider === "lark") return "Lark";
  if (provider === "weixin" || provider === "wechat") return t("settings.botWeixin");
  return t("settings.botFeishu");
}

// T2 entry A: a .drawio attachment chip carries a one-click "parse as
// topology" action — read via the same ReadFile bridge the viewer uses,
// parse through NetDevImportTopoPreview, then hand the preview to the
// netdev dock via a window event (no state lifting across panes).
async function parseTopoFromAttachment(path: string, name: string): Promise<void> {
  try {
    let pv: import("../lib/types").NetDevTopoImportPreview | null = null;
    if (name.toLowerCase().endsWith(".vsdx")) {
      // Binary OOXML package: bytes via the capped base64 bridge.
      const b64 = await app.NetDevAttachmentBytesBase64(path);
      if (b64) pv = await app.NetDevImportVsdxPreview(b64);
    } else {
      const preview = await app.ReadFile(path);
      if (preview?.binary || !preview?.body) return;
      pv = await app.NetDevImportTopoPreview(preview.body);
    }
    if (!pv) return;
    window.dispatchEvent(new CustomEvent("hiq:netdev-topo-import", { detail: { name, preview: pv } }));
  } catch {
    // attachments the bridge cannot read simply keep the viewer-only chip
  }
}

function attachmentIcon(kind: "image" | "file" | "folder") {
  if (kind === "image") return <Image size={15} />;
  if (kind === "folder") return <Folder size={15} />;
  return <FileText size={15} />;
}

export function UserMessage({
  text,
  failed,
  turn,
  anchorId,
}: {
  text: string;
  failed?: boolean;
  turn?: number;
  anchorId?: string;
}) {
  const t = useT();
  const imSource = parseImSourceMessage(text);
  const { text: displayText, attachments } = parseAttachmentRefsForDisplay(imSource?.text ?? text);
  const orderedAttachments = sortDisplayAttachments(attachments);
  const sourceLabel = imSource ? imSourceLabel(imSource, t) : "";
  const [imagePreviews, setImagePreviews] = useState<Record<string, string>>({});
  const imagePreviewKey = orderedAttachments
    .filter((attachment) => attachment.kind === "image" && attachment.source === "attachment")
    .map((attachment) => attachment.path)
    .join("\n");

  useEffect(() => {
    const paths = imagePreviewKey ? imagePreviewKey.split("\n") : [];
    if (paths.length === 0) return;
    let cancelled = false;
    for (const path of paths) {
      if (imagePreviews[path]) continue;
      app.AttachmentDataURL(path)
        .then((url) => {
          if (cancelled) return;
          setImagePreviews((prev) => (prev[path] ? prev : { ...prev, [path]: url }));
        })
        .catch(() => {});
    }
    return () => {
      cancelled = true;
    };
  }, [imagePreviewKey]);
  return (
    <div
      className={`msg msg--user${imSource ? " msg--im-source" : ""}${failed ? " msg--user-failed" : ""}`}
      id={anchorId}
      data-question-anchor={anchorId}
      data-turn={turn}
      data-im-source={imSource?.provider || undefined}
    >
      <div className="msg__body">
        {imSource ? (
          <div className="im-source-card">
            <div className="im-source-card__head">
              <MessageSquare size={14} />
              <span>{t("msg.fromIm", { source: sourceLabel })}</span>
            </div>
            {displayText && <div className="im-source-card__text">{displayText}</div>}
            {(imSource.sender || imSource.chat) && (
              <div className="im-source-card__meta">
                {imSource.sender && <span>{t("msg.imSender", { id: imSource.sender })}</span>}
                {imSource.chat && <span>{imSource.chat}</span>}
              </div>
            )}
          </div>
        ) : (
          displayText && <div className="msg__text">{displayText}</div>
        )}
        {failed && <div className="msg__send-failed">{t("msg.sendFailed")}</div>}
        {orderedAttachments.length > 0 && (
          <div className="msg-attachments" aria-label={t("msg.attachments")}>
            {orderedAttachments.map((attachment, index) => (
              <span key={`${attachment.path}:${index}`} style={{ position: "relative", display: "inline-block" }}>
              {((attachment.name ?? "").toLowerCase().endsWith(".drawio") || (attachment.name ?? "").toLowerCase().endsWith(".vsdx")) && (
                <span
                  role="button"
                  className="msg-attachment__topo"
                  title={t("msg.parseTopo")}
                  onClick={e => { e.stopPropagation(); void parseTopoFromAttachment(attachment.path, attachment.name); }}
                >↗</span>
              )}
              <button
                type="button"
                className={`msg-attachment msg-attachment--${attachment.kind}`}
                title={t("msg.previewAttachment", { name: attachment.name })}
                onClick={() =>
                  openAttachmentViewer({
                    path: attachment.path,
                    name: attachment.name,
                    kind: attachment.kind,
                    source: attachment.source,
                    // Every attachment in the message is a peer, so multiple
                    // images browse as one set via the lightbox ‹ › controls.
                    siblings: orderedAttachments.map((att) => ({
                      path: att.path,
                      name: att.name,
                      kind: att.kind,
                      source: att.source,
                    })),
                  })
                }
              >
                <span className={`msg-attachment__icon msg-attachment__icon--${attachment.kind}`} aria-hidden="true">
                  {attachment.kind === "image" && imagePreviews[attachment.path] ? <img src={imagePreviews[attachment.path]} alt="" draggable={false} /> : attachmentIcon(attachment.kind)}
                </span>
                <span className="msg-attachment__main">
                  <span className="msg-attachment__name">{attachment.name}</span>
                  <span className="msg-attachment__meta">
                    {attachment.kind === "folder"
                      ? t("msg.folderReference")
                      : `${attachment.ext || t("msg.fileAttachment")} · ${attachment.source === "workspace" ? t("msg.workspaceReference") : attachment.kind === "image" ? t("msg.imageAttachment") : t("msg.fileAttachment")}`}
                  </span>
                </span>
              </button>
              </span>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

export function TurnActions({
  text,
  turn,
  openMenu,
  onOpenMenu,
  onRewind,
  checkpoint,
  reapplyTurn,
  actionPending = false,
  rewindDisabled = false,
}: {
  text: string;
  turn?: number;
  openMenu?: TurnActionMenu | null;
  onOpenMenu?: (menu: TurnActionMenu | null) => void;
  onRewind?: (turn: number, scope: MessageActionScope) => void;
  checkpoint?: CheckpointMeta;
  reapplyTurn?: number;
  actionPending?: boolean;
  rewindDisabled?: boolean;
}) {
  const t = useT();
  const [confirmScope, setConfirmScope] = useState<MessageActionScope | null>(null);
  // Rewind diff preview (upgrade spec 3-6): fetched on demand from the
  // checkpoint store; null = closed.
  const [rewindPreview, setRewindPreview] = useState<{ path: string; kind: string; added: number; removed: number; diff: string }[] | null>(null);
  useEffect(() => { setRewindPreview(null); }, [openMenu]);
  // ZCode-style safety classification for the confirm step: fetched when the
  // user arms a code/both rewind; null = not loaded.
  const [rewindClasses, setRewindClasses] = useState<{ path: string; class: string }[] | null>(null);
  useEffect(() => {
    setRewindClasses(null);
    if (confirmScope !== "code" && confirmScope !== "both") return;
    app.RewindPreviewForTab("", turn as number)
      .then((files) => setRewindClasses(files ?? []))
      .catch(() => setRewindClasses([]));
  }, [confirmScope, turn]);
  const classCounts = useMemo(() => {
    const c = { safe: 0, unsafe: 0, ignored: 0 };
    for (const f of rewindClasses ?? []) c[f.class as "safe" | "unsafe" | "ignored"] = (c[f.class as "safe" | "unsafe" | "ignored"] ?? 0) + 1;
    return c;
  }, [rewindClasses]);
  // Reapply (the latest code rewind's reverse checkpoint): own armed state so
  // it rides the same onRewind("code") flow against the keep turn.
  const [reapplyArmed, setReapplyArmed] = useState(false);
  useEffect(() => { setReapplyArmed(false); }, [openMenu]);
  const canAct = onRewind != null && turn != null;
  const actionDisabledReason = (scope: string): string => {
    if (rewindDisabled || actionPending) return t("rewind.disabledRunning");
    if (!checkpoint) return t("rewind.disabledNoCheckpoint");
    if ((scope === "fork" || scope === "summ-from" || scope === "conversation") && !checkpoint.canConversation) {
      return t("rewind.disabledNoBoundary");
    }
    if (scope === "summ-upto") {
      if (!checkpoint.canConversation) return t("rewind.disabledNoBoundary");
      if ((turn ?? 0) <= 0) return t("rewind.disabledNoEarlier");
    }
    if (scope === "code" && !checkpoint.canCode) return t("rewind.disabledNoCode");
    if (scope === "both") {
      if (!checkpoint.canConversation) return t("rewind.disabledNoBoundary");
      if (!checkpoint.canCode) return t("rewind.disabledNoCode");
    }
    return "";
  };
  const actionLabel = (scope: MessageActionScope): string => {
    if (confirmScope !== scope) {
      switch (scope) {
        case "fork":
          return t("rewind.fork");
        case "summ-from":
          return t("rewind.summFrom");
        case "summ-upto":
          return t("rewind.summUpto");
        case "conversation":
          return t("rewind.conversation");
        case "code":
          return t("rewind.code");
        default:
          return t("rewind.both");
      }
    }
    switch (scope) {
      case "fork":
        return t("rewind.confirmFork");
      case "summ-from":
        return t("rewind.confirmSummFrom");
      case "summ-upto":
        return t("rewind.confirmSummUpto");
      case "conversation":
        return t("rewind.confirmConversation");
      case "code":
        return t("rewind.confirmCode");
      default:
        return t("rewind.confirmBoth");
    }
  };
  const actionMeta = (scope: MessageActionScope): string => {
    if ((scope === "code" || scope === "both") && checkpoint?.files?.length) {
      return t("rewind.filesChanged", { count: checkpoint.files.length });
    }
    return "";
  };
  const runAction = (scope: MessageActionScope) => {
    setConfirmScope(null);
    onOpenMenu?.(null);
    onRewind?.(turn as number, scope);
  };
  const selectRewind = (scope: MessageActionScope) => {
    if (actionDisabledReason(scope)) return;
    if (confirmScope !== scope) {
      setConfirmScope(scope);
      return;
    }
    runAction(scope);
  };
  const renderAction = (scope: MessageActionScope, danger = false) => {
    const disabledReason = actionDisabledReason(scope);
    const meta = actionMeta(scope);
    return (
      <button
        className={[
          "rewind__menu-item",
          danger ? "rewind__menu-danger" : "",
          confirmScope === scope ? "rewind__menu-confirm" : "",
        ].filter(Boolean).join(" ")}
        type="button"
        disabled={Boolean(disabledReason)}
        title={disabledReason || undefined}
        onClick={() => selectRewind(scope)}
      >
        <span>{actionLabel(scope)}</span>
        {meta && <span className="rewind__menu-meta">{meta}</span>}
      </button>
    );
  };
  const forkDisabledReason = canAct ? actionDisabledReason("fork") : "";
  const toggleMenu = (menu: TurnActionMenu) => {
    setConfirmScope(null);
    onOpenMenu?.(openMenu === menu ? null : menu);
  };

  return (
    <div className="turn-actions">
      <CopyButton text={text} label={t("msg.copy")} />
      {canAct && (
        <>
          <button
            className={`turn-actions__btn${confirmScope === "fork" ? " turn-actions__btn--confirm" : ""}`}
            type="button"
            disabled={Boolean(forkDisabledReason)}
            title={forkDisabledReason || undefined}
            onClick={() => selectRewind("fork")}
          >
            <GitBranch size={13} />
            <span>{actionLabel("fork")}</span>
          </button>
          <div className={`turn-actions__group${openMenu === "summary" ? " turn-actions__group--open" : ""}`}>
            <button
              className="turn-actions__btn"
              type="button"
              aria-haspopup="menu"
              aria-expanded={openMenu === "summary"}
              onClick={() => toggleMenu("summary")}
            >
              <ScrollText size={13} />
              <span>{t("turnActions.summary")}</span>
              <ChevronDown size={12} />
            </button>
            {openMenu === "summary" && (
              <div className="rewind__menu turn-actions__menu" role="menu">
                {rewindDisabled && <div className="rewind__menu-hint">{t("rewind.disabledRunning")}</div>}
                {!rewindDisabled && !checkpoint && <div className="rewind__menu-hint">{t("rewind.disabledNoCheckpoint")}</div>}
                {renderAction("summ-from")}
                {renderAction("summ-upto")}
              </div>
            )}
          </div>
          <div className={`turn-actions__group${openMenu === "rewind" ? " turn-actions__group--open" : ""}`}>
            <button
              className="turn-actions__btn"
              type="button"
              aria-haspopup="menu"
              aria-expanded={openMenu === "rewind"}
              onClick={() => toggleMenu("rewind")}
            >
              <RotateCcw size={13} />
              <span>{t("turnActions.rewind")}</span>
              <ChevronDown size={12} />
            </button>
            {openMenu === "rewind" && (
              <div className="rewind__menu turn-actions__menu" role="menu">
                {rewindDisabled && <div className="rewind__menu-hint">{t("rewind.disabledRunning")}</div>}
                {!rewindDisabled && !checkpoint && <div className="rewind__menu-hint">{t("rewind.disabledNoCheckpoint")}</div>}
                {renderAction("conversation")}
                {renderAction("code")}
                {renderAction("both", true)}
                {(confirmScope === "code" || confirmScope === "both") && rewindClasses && rewindClasses.length > 0 && (
                  <div className="rewind__preview">
                    <div className="rewind__preview-file" style={{ color: "var(--ok, #30a46c)" }}>{t("rewind.classSafe", { n: classCounts.safe })}</div>
                    {classCounts.unsafe > 0 && (
                      <div className="rewind__preview-file" style={{ color: "var(--danger, #e5484d)" }}>
                        {t("rewind.classUnsafe", { n: classCounts.unsafe })}
                        {(rewindClasses ?? []).filter(f => f.class === "unsafe").map(f => (
                          <div key={f.path} style={{ paddingLeft: 8 }} title={f.path}>· {f.path}</div>
                        ))}
                      </div>
                    )}
                    {classCounts.ignored > 0 && (
                      <div className="rewind__preview-file" style={{ opacity: 0.7 }}>
                        {t("rewind.classIgnored", { n: classCounts.ignored })}
                        {(rewindClasses ?? []).filter(f => f.class === "ignored").map(f => (
                          <div key={f.path} style={{ paddingLeft: 8 }} title={f.path}>· {f.path}</div>
                        ))}
                      </div>
                    )}
                  </div>
                )}
                {reapplyTurn != null && !rewindDisabled && !actionPending && (
                  <button
                    className={`rewind__menu-item${reapplyArmed ? " rewind__menu-confirm" : ""}`}
                    type="button"
                    title={t("rewind.reapplyTip")}
                    onClick={() => {
                      if (!reapplyArmed) { setReapplyArmed(true); return; }
                      setReapplyArmed(false);
                      onOpenMenu?.(null);
                      onRewind?.(reapplyTurn, "code");
                    }}
                  >
                    <span>{reapplyArmed ? t("rewind.reapplyConfirm") : t("rewind.reapply")}</span>
                  </button>
                )}
                {!!checkpoint?.files?.length && (
                  <button
                    className="rewind__menu-item"
                    type="button"
                    onClick={() => {
                      if (rewindPreview) { setRewindPreview(null); return; }
                      app.CheckpointDiffForTab("", turn as number)
                        .then((files) => setRewindPreview(files ?? []))
                        .catch(() => setRewindPreview([]));
                    }}
                  >
                    <span>{rewindPreview === null ? t("rewind.preview") : t("rewind.collapse")}</span>
                    <span className="rewind__menu-meta">{checkpoint.files.length} files</span>
                  </button>
                )}
                {rewindPreview && rewindPreview.length > 0 && (
                  <div className="rewind__preview">
                    {rewindPreview.map((f) => (
                      <div key={f.path} className="rewind__preview-file">
                        <div className="rewind__preview-path">
                          <span>{f.path}</span>
                          {f.kind !== "binary" && <span className="rewind__preview-stat">+{f.added} -{f.removed}</span>}
                        </div>
                        {f.diff && <UnifiedDiff value={f.diff} maxHeight={200} showToggle={false} />}
                      </div>
                    ))}
                  </div>
                )}
                {rewindPreview && rewindPreview.length === 0 && (
                  <div className="rewind__menu-hint">文件与当前状态一致</div>
                )}
              </div>
            )}
          </div>
        </>
      )}
    </div>
  );
}

export const AssistantMessage = memo(function AssistantMessage({
  item,
  defaultExpanded = false,
  expandWhileStreaming = true,
  modelLabel,
}: {
  item: AssistantItem;
  defaultExpanded?: boolean;
  /** false in compact/minimal: completed steps fold away, so auto-open + fold reads as flicker. */
  expandWhileStreaming?: boolean;
  /** the active model name (e.g. "deepseek/deepseek-chat"); falls back to the generic label. */
  modelLabel?: string;
}) {
  const t = useT();
  const [reasoningOpen, setReasoningOpen] = useState((expandWhileStreaming && item.streaming) || defaultExpanded);
  const hasText = item.streaming || item.text.trim() !== "";
  const processOnly = Boolean(item.reasoning) && !hasText;
  const processWithText = Boolean(item.reasoning) && hasText;
  const eyebrowText = modelLabel || t("msg.assistantEyebrow");
  return (
    <div className={`msg msg--assistant${processOnly ? " msg--process-only" : ""}${processWithText ? " msg--process-with-text" : ""}`}>
      {!processOnly && <div className="msg__eyebrow" aria-hidden="true">{eyebrowText}</div>}
      {item.reasoning && (
        <div className="reasoning">
          <button
            type="button"
            className="reasoning__head"
            data-running={item.streaming ? "" : undefined}
            onClick={() => setReasoningOpen((v) => !v)}
            aria-expanded={reasoningOpen}
          >
            <ProcessBrainIcon size={12} />
            <span>{t("msg.thinking")}</span>
            <span className="reasoning__meta">{item.streaming ? t("msg.thinkingRunning") : t("msg.thinkingDone")}</span>
            <ChevronRight className={`reasoning__chevron${reasoningOpen ? " reasoning__chevron--open" : ""}`} size={12} />
          </button>
          {reasoningOpen && (
            <div className="reasoning__body">
              <Markdown text={item.reasoning} />
            </div>
          )}
        </div>
      )}
      {hasText && (
        <div className="msg__body">
          <Markdown text={item.text} showCursor={item.streaming} />
        </div>
      )}
    </div>
  );
});
