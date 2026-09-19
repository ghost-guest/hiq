// useController is the frontend's state machine over the agent's event stream. It
// maintains per-tab state so background tabs preserve their streaming output, tool
// states, and approvals when the user switches away and back. The active tab's state
// is what components render.

import { useCallback, useEffect, useRef, useState } from "react";
import { asArray } from "./array";
import { app, onEvent, onReady } from "./bridge";
import { createRafBatch } from "./rafBatch";
import { t } from "./i18n";
import { blendTokenScale, countCjk, estimateTokens } from "./runReadout";
import type {
  CheckpointMeta,
  CollaborationMode,
  ContextInfo,
  EffortInfo,
  HistoryMessage,
  JobView,
  MemoryView,
  Meta,
  PresentRecord,
  QuestionAnswer,
  SessionMeta,
  TabMeta,
  ToolApprovalMode,
  WireApproval,
  WireAsk,
  WireAttachment,
  WireCollab,
  WireEvent,
  WireFileDiff,
  WireUsage,
} from "./types";

export type ToolStatus = "running" | "done" | "error" | "stopped";

// TurnSummaryFile is one file's change within a turn's end-of-turn summary
// card (upgrade spec 1-4): the diff section text plus its tallies.
export interface TurnSummaryFile {
  path: string;
  added: number;
  removed: number;
  diff: string;
}

export type LiveStream = { id: string; text: string; reasoning: string };
export type MessageActionScope = "fork" | "summ-from" | "summ-upto" | "conversation" | "code" | "both";
export type MessageActionState = { turn: number; scope: MessageActionScope };

export type Item =
  | { kind: "user"; id: string; text: string; failed?: boolean }
  | { kind: "assistant"; id: string; text: string; reasoning: string; streaming: boolean }
  | { kind: "phase"; id: string; text: string }
  | { kind: "notice"; id: string; level: "info" | "warn"; text: string; retryable?: boolean; repeat?: number }
  | { kind: "turn_summary"; id: string; files: TurnSummaryFile[] }
  | {
      kind: "compaction";
      id: string;
      pending: boolean;
      trigger: string;
      messages: number;
      summary: string;
      archive: string;
    }
  | { kind: "expert_collab"; id: string; collab: WireCollab }
  | {
      kind: "tool";
      id: string;
      name: string;
      args: string;
      readOnly: boolean;
      status: ToolStatus;
      output?: string;
      error?: string;
      truncated?: boolean;
      durationMs?: number;
      isShell?: boolean; // true for !-prefix shell commands (controls default expand)
      parentId?: string; // a sub-agent call nests under the `task` call with this id
      profile?: { model?: string; effort?: string }; // subagent model/effort from tool event
      attachments?: WireAttachment[]; // files the tool produced (e.g. generated images)
      fileDiff?: WireFileDiff; // server-side preview (live dispatch or replayed sidecar)
      argsDiff?: string; // live partial patch while the model streams apply_patch args (3-3)
    };

interface State {
  items: Item[];
  running: boolean;
  turnActive: boolean;
  // paused is true while the in-flight turn is frozen on a graceful pause
  // (between steps, awaiting ResumeTurn). The run loop preserves full state,
  // so the UI keeps rendering the running turn — just gated. Distinct from
  // running=false (turn done): paused means "running, but held".
  paused: boolean;
  approval?: WireApproval;
  ask?: WireAsk;
  usage?: WireUsage;
  context: ContextInfo;
  meta?: Meta;
  effort?: EffortInfo;
  jobs: JobView[];
  checkpoints: CheckpointMeta[];
  messageAction?: MessageActionState;
  currentAssistant?: string;
  live?: LiveStream;
  pendingUser?: string;
  discardTurn?: boolean;
  turnStartAt: number;
  turnTokens: number;
  turnTotalTokens: number;
  // Characters streamed during this turn (assistant text + reasoning + the
  // patch previews a tool call streams) and how many of those were CJK. A
  // provider reports real token counts only when a response finishes, so an
  // in-flight tok/s has to be estimated from these instead — see lib/runReadout.
  turnChars: number;
  turnCjkChars: number;
  // Chars→tokens correction for that estimate, relearned from every response
  // that reports an exact count (1 = the built-in defaults).
  tokenScale: number;
  sessionTokens: number;
  retry?: { attempt: number; max: number; afterMs?: number };
  turnItemStart: number; // items index where the current turn began (1-4 summary scoping)
  seq: number;
}

export const initialState: State = {
  items: [],
  running: false,
  turnActive: false,
  paused: false,
  context: { used: 0, window: 0, sessionTokens: 0 },
  jobs: [],
  checkpoints: [],
  turnStartAt: 0,
  turnTokens: 0,
  turnTotalTokens: 0,
  turnChars: 0,
  turnCjkChars: 0,
  tokenScale: 1,
  sessionTokens: 0,
  seq: 0,
  turnItemStart: 0,
};

// isShellTool decides whether a tool card should render in the "live shell"
// style (10-line preview + show-all + Ctrl+B). The backend already streams
// bash stdout chunk-by-chunk via tool_progress; this gate just decides whether
// the card *presents* it as a shell panel. We match on tool name so agent-
// initiated bash calls (whose IDs are provider-generated like "call_xxx") are
// treated the same as user "!" shell commands (whose IDs carry a "shell-"
// prefix). Keeping the prefix check preserves backward compat for the user-
// initiated path.
function isShellTool(name: string, id: string): boolean {
  return name === "bash" || id.startsWith("shell-");
}

function usageTotalTokens(usage?: WireUsage): number {
  if (!usage) return 0;
  if (usage.totalTokens > 0) return usage.totalTokens;
  const promptTokens = usage.promptTokens || usage.cacheHitTokens + usage.cacheMissTokens;
  return Math.max(0, promptTokens + usage.completionTokens);
}

function sameMeta(a?: Meta, b?: Meta): boolean {
  if (a === b) return true;
  if (!a || !b) return false;
  return (
    a.label === b.label &&
    a.ready === b.ready &&
    a.startupErr === b.startupErr &&
    a.eventChannel === b.eventChannel &&
    a.cwd === b.cwd &&
    a.autoApproveTools === b.autoApproveTools &&
    a.bypass === b.bypass &&
    a.toolApprovalMode === b.toolApprovalMode &&
    a.ragScope === b.ragScope &&
    a.goal === b.goal &&
    a.goalStatus === b.goalStatus &&
    // expertSession drives the main-area branch (ExpertSessionView vs
    // Transcript). Without comparing it, a meta refresh that only changes the
    // expert team (e.g. on tab switch / team rename) is silently dropped, and
    // the main area can render the wrong view or wrong team name.
    a.expertSession?.teamId === b.expertSession?.teamId &&
    a.expertSession?.teamName === b.expertSession?.teamName
  );
}

type Action =
  | { type: "event"; e: WireEvent }
  | { type: "user"; text: string; seq: number }
  | { type: "unsend" }
  | { type: "send_failed"; error: string }
  | { type: "backend_status"; running: boolean }
  | { type: "meta"; meta: Meta }
  | { type: "context"; context: ContextInfo }
  | { type: "effort"; effort: EffortInfo }
  | { type: "jobs"; jobs: JobView[] }
  | { type: "checkpoints"; checkpoints: CheckpointMeta[] }
  | { type: "message_action_start"; action: MessageActionState }
  | { type: "message_action_done" }
  | { type: "history"; messages: HistoryMessage[] }
  | { type: "present"; records: PresentRecord[] }
  | { type: "local_notice"; level: "info" | "warn"; text: string }
  | { type: "clearApproval" }
  | { type: "clearAsk" }
  | { type: "reset" };

// ---- reducer helpers (unchanged logic) ----

// parseCollabRecord decodes a persisted expert-collab tool message's content
// (a CollabRecord JSON) into the WireCollab the transcript renders. Returns null
// for any input that isn't a valid collab record (wrong/missing marker, bad
// JSON) so the caller can fall back to a plain tool card.
export function parseCollabRecord(content: string): WireCollab | null {
  try {
    const r = JSON.parse(content);
    if (!r || r.__type !== "__expert_collab__") return null;
    return {
      runId: r.runId ?? "",
      teamId: r.teamId ?? "",
      teamName: r.teamName ?? "",
      task: r.task ?? "",
      mode: r.mode ?? "",
      rounds: Array.isArray(r.rounds) ? r.rounds : [],
      synthesis: r.synthesis ?? "",
      createdAt: r.createdAt ?? 0,
    };
  } catch {
    return null;
  }
}

export function historyMessagesToItems(messages: HistoryMessage[], idPrefix: string, startSeq = 0): { items: Item[]; seq: number } {
  const resultByID = new Map<string, HistoryMessage>();
  for (const m of messages) {
    if (m.role === "tool" && m.toolCallId && !resultByID.has(m.toolCallId)) {
      resultByID.set(m.toolCallId, m);
    }
  }

  const items: Item[] = [];
  let seq = startSeq;
  const consumedToolIDs = new Set<string>();
  for (const m of messages) {
    if (m.role === "system") continue;
    if (m.role === "phase") {
      if (m.content.trim() !== "") {
        items.push({ kind: "phase", id: `${idPrefix}${seq}`, text: m.content });
        seq++;
      }
      continue;
    }
    if (m.role === "notice") {
      if (m.content.trim() !== "") {
        items.push({ kind: "notice", id: `${idPrefix}${seq}`, level: m.level === "warn" ? "warn" : "info", text: m.content });
        seq++;
      }
      continue;
    }
    if (m.role === "compaction") {
      items.push({
        kind: "compaction",
        id: `${idPrefix}${seq}`,
        pending: Boolean(m.pending),
        trigger: m.trigger ?? "",
        messages: m.messages ?? 0,
        summary: m.summary ?? "",
        archive: m.archive ?? "",
      });
      seq++;
      continue;
    }
    if (m.role === "user") {
      if (m.content.trim() === "") continue;
      items.push({ kind: "user", id: `${idPrefix}${seq}`, text: m.content });
      seq++;
      continue;
    }
    if (m.role === "assistant") {
      const hasText = m.content.trim() !== "" || (m.reasoning ?? "").trim() !== "";
      if (hasText) {
        items.push({ kind: "assistant", id: `${idPrefix}${seq}`, text: m.content, reasoning: m.reasoning ?? "", streaming: false });
        seq++;
      }
      for (const tc of m.toolCalls ?? []) {
        const result = resultByID.get(tc.id);
        if (tc.id) consumedToolIDs.add(tc.id);
        const output = result?.content ?? "";
        const error = output.startsWith("[error") || output.startsWith("Error:") ? output : undefined;
        const toolID = tc.id || `${idPrefix}tool${seq}`;
        items.push({
          kind: "tool",
          id: toolID,
          name: tc.name,
          args: tc.arguments ?? "",
          readOnly: false,
          status: error ? "error" : "done",
          output,
          error,
          isShell: isShellTool(tc.name, toolID),
        });
        // Sub-agent transcript (task/run_skill/explore/...): the backend loads the
        // persisted sub-agent session (subagents/sa_xxx.jsonl) and attaches it as
        // tc.subagent. Recurse into it and emit each sub-step as a tool item with
        // parentId == this call's id, so Transcript.tsx's subcallsByParent nests
        // them exactly as the live stream does (which carries parentId on the
        // forwarded ToolDispatch/ToolResult events — see task.go subSinkFor).
        // Without this, a tab switch makes every sub-agent's internal steps
        // vanish, leaving only the parent card's final-answer text.
        if (tc.subagent && tc.subagent.length > 0) {
          // idPrefix "" (not `${toolID}/`): the sub-agent's tool ids are its own
          // real ids; we namespacing them ourselves below so they match the live
          // stream's format (task.go subSinkFor rewrites child ids to
          // `parentID/childID`). Matching the live stream's id format is what lets
          // the present sidecar's tool overlays (keyed by namespaced id) land on
          // these rebuilt items and restore their rich fields (durationMs etc).
          const { items: subItems, seq: subSeq } = historyMessagesToItems(tc.subagent, "s", seq + 1);
          for (const si of subItems) {
            // Only tool items carry parentId — assistant/phase/notice inside a
            // sub-agent aren't nested-renderable by ToolCard, and the live stream
            // (subSinkFor) only forwards ToolDispatch/ToolResult anyway.
            if (si.kind === "tool") {
              // Namespace the id: parentID/childID. Matches subSinkFor's rewrite
              // (task.go:520, `parentID + "/" + childID`) so the present sidecar's
              // tool overlays — keyed by that same namespaced id — land on these
              // rebuilt items and restore their rich fields (durationMs etc).
              items.push({ ...si, id: `${toolID}/${si.id}`, parentId: toolID });
            } else {
              items.push(si);
            }
          }
          seq = subSeq;
        } else {
          seq++;
        }
      }
      continue;
    }
  if (m.role === "tool") {
    if (m.toolCallId && consumedToolIDs.has(m.toolCallId)) continue;
    // A persisted expert-team collaboration is stored as a tool message whose
    // content is a CollabRecord JSON (marker __type === "__expert_collab__").
    // Render it as a folded card instead of a raw tool dump.
    if (m.toolName === "expert_team_collab") {
      const parsed = parseCollabRecord(m.content);
      if (parsed) {
        items.push({ kind: "expert_collab", id: m.toolCallId || `${idPrefix}ec${seq}`, collab: parsed });
        seq++;
        continue;
      }
      // Fall through: corrupt/unparseable record renders as a plain tool card.
    }
    const output = m.content;
    const error = output.startsWith("[error") || output.startsWith("Error:") ? output : undefined;
    items.push({
      kind: "tool",
      id: m.toolCallId || `${idPrefix}tool${seq}`,
      name: m.toolName || "tool",
      args: "",
      readOnly: false,
      status: error ? "error" : "done",
      output,
      error,
      isShell: isShellTool(m.toolName || "tool", m.toolCallId || ""),
    });
      seq++;
      continue;
    }
  }
  return { items, seq };
}

// presentRecordsToOverlays splits a presentation sidecar's records into two
// parts the "present" reducer can apply on top of history-built items:
//
//   - overlays: per-tool rich fields (readOnly, durationMs, truncated, attachments,
//     profile, parentId, output, error) keyed by tool id. tool_dispatch and
//     tool_result for the same id are merged; tool_progress chunks accumulate into
//     output (matching the live stream's incremental append).
//   - cards: standalone items history can't reconstruct — notice, phase,
//     compaction, expert_collab. These are appended to the transcript.
//
// user / assistant / reasoning / message records are ignored here: history
// already produced those from provider.Message, and re-emitting them would
// duplicate the transcript. Only the presentation-only information survives.
//
// startSeq seeds the id namespace for cards and id-less tools so the caller can
// keep its seq counter monotonic.
function presentRecordsToOverlays(
  records: PresentRecord[],
  startSeq: number,
): { overlays: Map<string, ToolOverlay>; cards: Item[] } {
  const overlays = new Map<string, ToolOverlay>();
  const cards: Item[] = [];
  let seq = startSeq;

  const overlayFor = (id: string): ToolOverlay => {
    let ov = overlays.get(id);
    if (!ov) {
      ov = { id };
      overlays.set(id, ov);
    }
    return ov;
  };

  for (const r of records) {
    switch (r.kind) {
      case "tool_dispatch": {
        if (!r.tool?.id) break;
        const ov = overlayFor(r.tool.id);
        if (r.tool.name !== undefined) ov.name = r.tool.name;
        if (r.tool.args !== undefined) ov.args = r.tool.args;
        if (r.tool.readOnly !== undefined) ov.readOnly = r.tool.readOnly;
        if (r.tool.parentId !== undefined) ov.parentId = r.tool.parentId;
        if (r.tool.profile) ov.profile = r.tool.profile;
        if (r.tool.fileDiff) ov.fileDiff = r.tool.fileDiff;
        break;
      }
      case "tool_progress": {
        if (!r.tool?.id) break;
        const ov = overlayFor(r.tool.id);
        // Live stream appends progress chunks (useController.ts tool_progress
        // case does `it.output + chunk`); mirror that so reload reconstructs the
        // same accumulated output when there was no tool_result yet.
        ov.output = (ov.output ?? "") + (r.tool.output ?? "");
        break;
      }
      case "tool_result": {
        if (!r.tool?.id) break;
        const ov = overlayFor(r.tool.id);
        // A result's output is authoritative (full, final) — overwrite any
        // accumulated progress chunks.
        if (r.tool.output !== undefined) ov.output = r.tool.output;
        if (r.tool.err !== undefined && r.tool.err !== "") ov.error = r.tool.err;
        if (r.tool.durationMs !== undefined) ov.durationMs = r.tool.durationMs;
        if (r.tool.truncated !== undefined) ov.truncated = r.tool.truncated;
        if (r.tool.attachments !== undefined) ov.attachments = r.tool.attachments as WireAttachment[];
        if (r.tool.readOnly !== undefined) ov.readOnly = r.tool.readOnly;
        if (r.tool.parentId !== undefined) ov.parentId = r.tool.parentId;
        if (r.tool.name !== undefined) ov.name = r.tool.name;
        break;
      }
      case "notice": {
        if (r.text && r.text.trim()) {
          cards.push({ kind: "notice", id: `pn${seq}`, level: r.level === "warn" ? "warn" : "info", text: r.text });
          seq++;
        }
        break;
      }
      case "phase": {
        if (r.text && r.text.trim()) {
          cards.push({ kind: "phase", id: `pp${seq}`, text: r.text });
          seq++;
        }
        break;
      }
      case "compaction_started": {
        cards.push({ kind: "compaction", id: `pc${seq}`, pending: true, trigger: r.compaction?.trigger ?? "", messages: 0, summary: "", archive: "" });
        seq++;
        break;
      }
      case "compaction_done": {
        // An aborted compaction (empty summary) resolves the pending placeholder;
        // a real one fills it. We don't try to match the pending card here — the
        // sidecar is replayed wholesale, so we just emit the filled card. If a
        // pending one preceded it, the transcript shows both briefly; acceptable
        // for a rare, informational card.
        if (r.compaction && r.compaction.summary) {
          cards.push({
            kind: "compaction",
            id: `pc${seq}`,
            pending: false,
            trigger: r.compaction.trigger ?? "",
            messages: r.compaction.messages ?? 0,
            summary: r.compaction.summary,
            archive: r.compaction.archive ?? "",
          });
          seq++;
        }
        break;
      }
      case "expert_collab": {
        // expert_collab is ALREADY persisted as a tool message in session.jsonl
        // (content = CollabRecord JSON with __type marker) and rebuilt by
        // historyMessagesToItems → parseCollabRecord. Emitting it again from the
        // sidecar would duplicate the card. Skip — history owns this card.
        break;
      }
      default:
        // turn_started / reasoning / text / message / usage / retrying / steer /
        // paused / resumed — not recoverable as standalone items (they'd duplicate
        // history or are transient indicators). Skip.
        break;
    }
  }
  return { overlays, cards };
}

// ToolOverlay is the per-tool rich-field patch the present reducer applies to a
// history-built tool item. Every field is optional; only the ones the sidecar
// recorded are set.
interface ToolOverlay {
  id?: string;
  name?: string;
  args?: string;
  readOnly?: boolean;
  durationMs?: number;
  truncated?: boolean;
  parentId?: string;
  profile?: { model?: string; effort?: string };
  attachments?: WireAttachment[];
  output?: string;
  error?: string;
  fileDiff?: WireFileDiff;
}

// splitDiffSections splits a (possibly multi-file) unified diff string into
// per-file sections — everything from one '--- a/path' header to the next —
// with +/- tallies counted per section. Bare-hunk input (no headers) comes
// back as a single anonymous section.
function splitDiffSections(text: string): TurnSummaryFile[] {
  const out: TurnSummaryFile[] = [];
  let cur: TurnSummaryFile | null = null;
  for (const line of text.split("\n")) {
    if (line.startsWith("--- a/")) {
      cur = { path: line.slice(6).trim(), added: 0, removed: 0, diff: "" };
      out.push(cur);
      cur.diff = line + "\n";
      continue;
    }
    if (!cur) {
      if (line.startsWith("@@")) {
        cur = { path: "", added: 0, removed: 0, diff: "" };
        out.push(cur);
      } else continue;
    }
    cur.diff += line + "\n";
    if (line.startsWith("+")) cur.added++;
    else if (line.startsWith("-")) cur.removed++;
  }
  return out;
}

// turnSummaryCard folds the turn's writer fileDiffs (from dispatches at or
// after startIdx) into one end-of-turn review card; null when the turn made
// no previewable file changes. Same path later in the turn wins (the final
// state is what a review wants).
function turnSummaryCard(items: Item[], startIdx: number, seq: number): Item | null {
  const files = new Map<string, TurnSummaryFile>();
  for (let i = Math.max(0, startIdx); i < items.length; i++) {
    const it = items[i];
    if (it.kind !== "tool" || !it.fileDiff?.diff) continue;
    for (const f of splitDiffSections(it.fileDiff.diff)) files.set(f.path, f);
  }
  if (files.size === 0) return null;
  return { kind: "turn_summary", id: `ts${seq}`, files: [...files.values()] };
}

function ensureAssistant(s: State): { items: Item[]; id: string; seq: number } {  if (s.currentAssistant) {
    const exists = s.items.some((it) => it.id === s.currentAssistant && it.kind === "assistant");
    if (exists) return { items: s.items, id: s.currentAssistant, seq: s.seq };
  }
  const id = `a${s.seq}`;
  const item: Item = { kind: "assistant", id, text: "", reasoning: "", streaming: true };
  return { items: [...s.items, item], id, seq: s.seq + 1 };
}

function flushPendingUser(s: State): State {
  if (s.pendingUser === undefined) return s;
  const lastItem = s.items[s.items.length - 1];
  if (lastItem?.kind === "user" && lastItem.text === s.pendingUser) {
    return { ...s, pendingUser: undefined };
  }
  return {
    ...s,
    seq: s.seq + 1,
    items: [...s.items, { kind: "user", id: `u${s.seq}`, text: s.pendingUser }],
    pendingUser: undefined,
  };
}

function applyEvent(s: State, e: WireEvent): State {
  if (s.discardTurn) {
    if (e.kind === "turn_done") return { ...s, discardTurn: false, running: false, turnActive: false, currentAssistant: undefined, live: undefined };
    return s;
  }
  if (s.pendingUser !== undefined && e.kind !== "turn_done") {
    s = flushPendingUser(s);
  }
  if (e.kind === "retrying") {
    return { ...s, retry: { attempt: e.retryAttempt ?? 0, max: e.retryMax ?? 0, afterMs: e.retryAfterMs } };
  }
  if (s.retry) s = { ...s, retry: undefined };
  switch (e.kind) {
    case "turn_started": {
      // Flush the user message and pre-create an empty assistant bubble
      // immediately so the user sees their message + a blinking cursor the
      // instant the backend acknowledges the turn — no dead gap waiting for
      // the first text/reasoning token.
      let cur: State = s;
      if (cur.pendingUser !== undefined) cur = flushPendingUser(cur);
      const { items, id, seq } = ensureAssistant(cur);
      return { ...cur, items, currentAssistant: id, seq, live: { id, text: "", reasoning: "" }, running: true, turnActive: true, turnStartAt: Date.now(), turnTokens: 0, turnTotalTokens: 0, turnChars: 0, turnCjkChars: 0, turnItemStart: items.length };
    }
    case "text":
    case "reasoning": {
      const { items, id, seq } = ensureAssistant(s);
      const delta = e.text ?? e.reasoning ?? "";
      const base = s.live?.id === id ? s.live : { id, text: "", reasoning: "" };
      const live = e.kind === "text" ? { ...base, text: base.text + delta } : { ...base, reasoning: base.reasoning + delta };
      // Count what actually came down the wire so the param row can show a
      // live tok/s while the turn is still running.
      return { ...s, items, live, currentAssistant: id, seq, turnChars: s.turnChars + delta.length, turnCjkChars: s.turnCjkChars + countCjk(delta) };
    }
    case "message": {
      const { items, id, seq } = ensureAssistant(s);
      const next = items.map((it) =>
        it.kind === "assistant" && it.id === id
          ? { ...it, text: e.text ?? s.live?.text ?? it.text, reasoning: e.reasoning ?? s.live?.reasoning ?? it.reasoning, streaming: false }
          : it,
      );
      return { ...s, items: next, live: undefined, currentAssistant: undefined, seq };
    }
    case "tool_dispatch": {
      const t = e.tool;
      if (!t) return s;
      const id = t.id || `tool${s.seq}`;
      const idx = s.items.findIndex((it) => it.kind === "tool" && it.id === id);
      if (idx >= 0) {
        const next = [...s.items];
        const it = next[idx];
        if (it.kind === "tool") next[idx] = { ...it, name: t.name, args: t.args ? t.args : it.args, readOnly: t.readOnly, profile: t.profile ?? it.profile, fileDiff: t.fileDiff ?? it.fileDiff, argsDiff: t.args ? undefined : it.argsDiff };
        return { ...s, items: next };
      }
      return { ...s, seq: s.seq + 1, items: [...s.items, { kind: "tool", id, name: t.name, args: t.args ?? "", readOnly: t.readOnly, status: "running", isShell: isShellTool(t.name, id), parentId: t.parentId, profile: t.profile, fileDiff: t.fileDiff }] };
    }
    case "tool_result": {
      const t = e.tool;
      if (!t) return s;
      const next = [...s.items];
      let idx = t.id ? next.findIndex((it) => it.kind === "tool" && it.id === t.id) : -1;
      if (idx < 0) {
        for (let i = next.length - 1; i >= 0; i--) {
          const it = next[i];
          if (it.kind === "tool" && it.status === "running") { idx = i; break; }
        }
      }
      if (idx >= 0) {
        const it = next[idx];
        if (it.kind === "tool") next[idx] = { ...it, status: t.err ? "error" : "done", output: t.output, error: t.err, truncated: t.truncated, durationMs: t.durationMs, attachments: t.attachments, argsDiff: undefined };
      }
      return { ...s, items: next };
    }
    case "tool_args_delta": {
      // Live patch preview (spec 3-3): attach the partial patch to the
      // running tool card created by the Partial dispatch.
      const t = e.tool;
      if (!t) return s;
      const next = [...s.items];
      let idx = t.id ? next.findIndex((it) => it.kind === "tool" && it.id === t.id) : -1;
      if (idx < 0) {
        for (let i = next.length - 1; i >= 0; i--) {
          const it = next[i];
          if (it.kind === "tool" && it.status === "running") { idx = i; break; }
        }
      }
      if (idx >= 0) {
        const it = next[idx];
        if (it.kind === "tool") next[idx] = { ...it, argsDiff: e.text ?? "", name: t.name || it.name };
      }
      // A patch preview is streamed file content, i.e. the same output tokens
      // the provider will count when this round finishes — without it the live
      // estimate reads far too low on any turn that writes files.
      const patch = e.text ?? "";
      return { ...s, items: next, turnChars: s.turnChars + patch.length, turnCjkChars: s.turnCjkChars + countCjk(patch) };
    }
    case "tool_progress": {
      const t = e.tool;
      if (!t?.id) return s;
      const idx = s.items.findIndex((it) => it.kind === "tool" && it.id === t.id);
      if (idx < 0) return s;
      const next = [...s.items];
      const it = next[idx];
      if (it.kind === "tool") next[idx] = { ...it, output: (it.output ?? "") + (t.output ?? "") };
      return { ...s, items: next };
    }
    case "usage": {
      const used = e.usage && s.context.window ? e.usage.promptTokens : s.context.used;
      const turnTokens = s.turnTokens + (e.usage?.completionTokens ?? 0);
      const usageTokens = usageTotalTokens(e.usage);
      const turnTotalTokens = s.turnTotalTokens + usageTokens;
      const sessionTokens = s.sessionTokens + usageTokens;
      // The first exact count of a turn is also the calibration sample for the
      // live estimator: characters streamed so far vs the tokens they cost.
      // Later rounds are skipped — by then `turnChars` spans every round while
      // `completionTokens` covers only the round that just finished.
      let tokenScale = s.tokenScale;
      if (s.turnTokens === 0 && s.turnChars > 0) {
        const estimated = estimateTokens(s.turnCjkChars, s.turnChars - s.turnCjkChars);
        const actual = e.usage?.completionTokens ?? 0;
        if (estimated > 0 && actual > 0) tokenScale = blendTokenScale(s.tokenScale, actual / estimated);
      }
      return { ...s, usage: e.usage, context: { ...s.context, used, sessionTokens }, turnTokens, turnTotalTokens, sessionTokens, tokenScale };
    }
    case "notice":
      return { ...s, running: s.turnActive ? s.running : false, seq: s.seq + 1, items: [...s.items, { kind: "notice", id: `n${s.seq}`, level: e.level ?? "info", text: e.text ?? "" }] };
    case "phase":
      return { ...s, seq: s.seq + 1, items: [...s.items, { kind: "phase", id: `p${s.seq}`, text: e.text ?? "" }] };
    case "compaction_started":
      return { ...s, seq: s.seq + 1, items: [...s.items, { kind: "compaction", id: `c${s.seq}`, pending: true, trigger: e.compaction?.trigger ?? "", messages: 0, summary: "", archive: "" }] };
    case "compaction_done": {
      const c = e.compaction;
      const idx = [...s.items].reverse().findIndex((it) => it.kind === "compaction" && it.pending);
      const at = idx < 0 ? -1 : s.items.length - 1 - idx;
      if (!c?.summary) {
        const items = at < 0 ? s.items : s.items.filter((_, i) => i !== at);
        return { ...s, running: s.turnActive ? s.running : false, items };
      }
      const filled: Item = { kind: "compaction", id: at < 0 ? `c${s.seq}` : (s.items[at] as Extract<Item, { kind: "compaction" }>).id, pending: false, trigger: c.trigger ?? "", messages: c.messages ?? 0, summary: c.summary, archive: c.archive ?? "" };
      const items = at < 0 ? [...s.items, filled] : s.items.map((it, i) => (i === at ? filled : it));
      return { ...s, running: s.turnActive ? s.running : false, seq: s.seq + 1, items };
    }
    case "steer":
      return { ...s, seq: s.seq + 1, items: [...s.items, { kind: "notice", id: `s${s.seq}`, level: "info", text: `↪ ${e.text ?? ""}` }] };
    case "paused":
      // The agent finished its current step and is now frozen. running stays
      // true (the turn is still in flight, just held) so the status bar keeps
      // showing; paused flips the button to Resume.
      return { ...s, paused: true, seq: s.seq + 1, items: [...s.items, { kind: "notice", id: `p${s.seq}`, level: "info", text: e.text ?? "已暂停" }] };
    case "resumed":
      return { ...s, paused: false };
    case "approval_request": return { ...s, approval: e.approval };
    case "ask_request": return { ...s, ask: e.ask };
    case "turn_done": {
      if (s.pendingUser !== undefined) s = flushPendingUser(s);
      const finalized = s.items.map((it) => {
        if (it.kind === "assistant" && s.live && it.id === s.live.id) return { ...it, text: s.live.text, reasoning: s.live.reasoning, streaming: false };
        if (it.kind === "assistant" && it.streaming) return { ...it, streaming: false };
        if (it.kind === "tool" && it.status === "running") return { ...it, status: "stopped" as const };
        return it;
      });
      // Turn change summary (upgrade spec 1-4): fold this turn's writer diffs
      // into one reviewable card ("Edited N files +X −Y"). Built from the
      // server-side fileDiff on dispatches since the turn started; subagent
      // edits nest in (their cards carry parentId but their diffs count).
      const summaryCard = e.err ? null : turnSummaryCard(finalized, s.turnItemStart, s.seq);
      const items: Item[] = e.err
        ? [...finalized, { kind: "notice", id: `e${s.seq}`, level: "warn", text: e.err, retryable: true }]
        : summaryCard
          ? [...finalized, summaryCard]
          : finalized;
      return { ...s, items, live: undefined, running: false, turnActive: false, paused: false, currentAssistant: undefined, approval: undefined, ask: undefined, seq: s.seq + 1 };
    }
    case "expert_collab": {
      // A finished expert-team collaboration is appended as a folded card. The
      // full record (per-round answers + synthesis) rides in the event, so no
      // fetch is needed; the model's context layer (Go side) projects it down to
      // a synthesis-only view before the next turn.
      if (!e.collab) return s;
      return { ...s, seq: s.seq + 1, items: [...s.items, { kind: "expert_collab", id: `ec${s.seq}`, collab: e.collab }] };
    }
    default: return s;
  }
}

// mergeRunningIntoHistory re-attaches in-flight items (tools still "running",
// plus the live streaming assistant bubble) to a freshly rebuilt history.
// Without this, a history reload issued mid-run (watchdog reconcile, tab
// switch) silently discards the live tool card and the current step's
// streamed-so-far text.
//
// Anchor strategy: stream-time tool ids are `a${seq}`/`call_xxx` while history
// rebuilds assistants as `h${seq}`, so we CANNOT match by parentId (the ids are
// disjoint namespaces). Instead:
//  1. If history already has the same tool id at a terminal state, the turn has
//     finished and been persisted — keep the history (final) version.
//  2. Otherwise append the survivors at the END of history. They all belong to
//     the current, still-unpersisted step, which is chronologically after every
//     persisted message — inserting "after the last assistant" instead would
//     misplace them before tool results that were persisted after that
//     assistant (mid-batch snapshot).
export function mergeRunningIntoHistory(histItems: Item[], runningItems: Item[]): Item[] {
  const terminal = new Set(
    histItems
      .filter((it) => it.kind === "tool")
      .map((it) => it.id),
  );
  const survivors = runningItems.filter((it) => !terminal.has(it.id));
  if (survivors.length === 0) return histItems;
  return [...histItems, ...survivors];
}

export function reducer(s: State, a: Action): State {
  switch (a.type) {
    case "user": {
      const seq = a.seq !== undefined ? a.seq : s.seq;
      return {
        ...s,
        seq: seq + 1,
        items: [...s.items, { kind: "user", id: `u${seq}`, text: a.text }],
        running: true,
        turnStartAt: Date.now(),
        turnTokens: 0,
        turnTotalTokens: 0,
        turnChars: 0,
        turnCjkChars: 0,
        pendingUser: a.text,
        discardTurn: false,
      };
    }
    case "unsend": return { ...s, pendingUser: undefined, discardTurn: true, running: false, live: undefined };
    case "send_failed": {
      if (s.pendingUser === undefined) return s;
      let idx = -1;
      for (let i = s.items.length - 1; i >= 0; i--) {
        const it = s.items[i];
        if (it.kind === "user" && it.text === s.pendingUser) { idx = i; break; }
      }
      const items = idx >= 0 ? s.items.map((it, i) => (i === idx ? { ...it, failed: true } : it)) : s.items;
      const notice: Item = { kind: "notice", id: `n${s.seq}`, level: "warn", text: a.error };
      return { ...s, pendingUser: undefined, running: false, turnActive: false, live: undefined, seq: s.seq + 1, items: [...items, notice] };
    }
    case "backend_status": {
      if (a.running === s.running) return s;
      if (a.running) return { ...s, running: true, turnActive: true, turnStartAt: s.turnStartAt || Date.now() };
      const finalized = s.items.map((it) => {
        if (it.kind === "assistant" && s.live && it.id === s.live.id) return { ...it, text: s.live.text, reasoning: s.live.reasoning, streaming: false };
        if (it.kind === "assistant" && it.streaming) return { ...it, streaming: false };
        if (it.kind === "tool" && it.status === "running") return { ...it, status: "stopped" as const };
        return it;
      });
      return { ...s, items: finalized, running: false, turnActive: false, live: undefined, currentAssistant: undefined, approval: undefined, ask: undefined };
    }
    case "meta": return sameMeta(s.meta, a.meta) ? s : { ...s, meta: a.meta };
    case "context": {
      if (!a.context) return s;
      const sessionTokens = typeof a.context.sessionTokens === "number"
        ? Math.max(0, a.context.sessionTokens)
        : s.sessionTokens;
      return { ...s, context: a.context, sessionTokens };
    }
    case "effort": return { ...s, effort: a.effort };
    case "jobs": return { ...s, jobs: a.jobs };
    case "checkpoints": return { ...s, checkpoints: a.checkpoints };
    case "message_action_start": return { ...s, messageAction: a.action };
    case "message_action_done": return { ...s, messageAction: undefined };
    case "history": {
      const { items: histItems, seq } = historyMessagesToItems(a.messages, "h", s.seq);
      // Preserve in-flight items across a history reload: running tools AND the
      // live streaming assistant bubble. historyMessagesToItems rebuilds items
      // from persisted messages, but the current LLM step's text/reasoning is
      // only persisted when the step completes — a rebuild issued mid-step
      // would orphan `live` (no item matches live.id anymore) and the next
      // token would restart from an empty bubble, swallowing everything
      // streamed so far.
      const liveId = s.live?.id;
      const runningItems = s.items.filter(
        (it) => (it.kind === "tool" && it.status === "running") || (it.kind === "assistant" && it.id === liveId),
      );
      if (runningItems.length === 0) {
        return { ...s, items: histItems, seq };
      }
      return { ...s, items: mergeRunningIntoHistory(histItems, runningItems), seq };
    }
    case "local_notice": return { ...s, running: false, turnActive: false, seq: s.seq + 1, items: [...s.items, { kind: "notice", id: `n${s.seq}`, level: a.level, text: a.text }] };
    case "present": {
      // Merge the presentation sidecar's rich fields onto the history-built items.
      // historyMessagesToItems produced tool items with readOnly=false, no
      // durationMs/truncated/attachments/profile/parentId, and dropped notice/
      // phase/compaction cards entirely (provider.Message doesn't carry them).
      // The sidecar has all of that; we project its records into:
      //   (a) rich-field overlays keyed by tool id — applied to existing tool items,
      //   (b) standalone cards (notice/phase/compaction/expert_collab) — inserted.
      // user/assistant text from the sidecar is NOT re-emitted: history already has
      // those, and duplicating them would double the transcript. Only the fields
      // history CAN'T reconstruct are recovered here.
      const { overlays, cards } = presentRecordsToOverlays(a.records, s.seq);
      if (overlays.size === 0 && cards.length === 0) {
        return s; // nothing to merge (e.g. sidecar only had text/reasoning)
      }
      let seq = s.seq;
      const items: Item[] = [];
      for (const it of s.items) {
        if (it.kind === "tool") {
          const ov = overlays.get(it.id);
          if (ov) {
            overlays.delete(it.id);
            items.push({
              ...it,
              ...(ov.readOnly !== undefined ? { readOnly: ov.readOnly } : {}),
              ...(ov.durationMs !== undefined ? { durationMs: ov.durationMs } : {}),
              ...(ov.truncated !== undefined ? { truncated: ov.truncated } : {}),
              ...(ov.profile !== undefined ? { profile: ov.profile } : {}),
              ...(ov.parentId !== undefined ? { parentId: ov.parentId } : {}),
              ...(ov.attachments !== undefined ? { attachments: ov.attachments } : {}),
              ...(ov.fileDiff !== undefined ? { fileDiff: ov.fileDiff } : {}),
              ...(ov.output !== undefined ? { output: ov.output } : {}),
              ...(ov.error !== undefined ? { error: ov.error } : {}),
            });
            continue;
          }
        }
        items.push(it);
      }
      // Remaining overlays (tools in the sidecar but not in history — e.g. a tool
      // whose result wasn't persisted) become standalone tool items at the end.
      for (const ov of overlays.values()) {
        items.push({
          kind: "tool",
          id: ov.id ?? `p${seq}`,
          name: ov.name ?? "tool",
          args: ov.args ?? "",
          readOnly: ov.readOnly ?? false,
          status: ov.error ? "error" : "done",
          ...(ov.output !== undefined ? { output: ov.output } : {}),
          ...(ov.error !== undefined ? { error: ov.error } : {}),
          ...(ov.durationMs !== undefined ? { durationMs: ov.durationMs } : {}),
          ...(ov.truncated !== undefined ? { truncated: ov.truncated } : {}),
          ...(ov.profile !== undefined ? { profile: ov.profile } : {}),
          ...(ov.parentId !== undefined ? { parentId: ov.parentId } : {}),
          ...(ov.attachments !== undefined ? { attachments: ov.attachments } : {}),
        });
        seq++;
      }
      // Standalone cards (notice/phase/compaction/expert_collab) — these have no
      // counterpart in history, so append them. Ordering across turns is imperfect
      // (we don't interleave with history items by turn), but these cards are rare
      // and informational; appending them is strictly better than dropping them.
      for (const card of cards) {
        items.push({ ...card, id: card.id || `p${seq}` });
        seq++;
      }
      return { ...s, items, seq };
    }
    case "clearApproval": return { ...s, approval: undefined };
    case "clearAsk": return { ...s, ask: undefined };
    case "reset": return { ...initialState, meta: s.meta, context: { ...s.context, used: 0, sessionTokens: 0 }, effort: s.effort, jobs: s.jobs };
    case "event": return applyEvent(s, a.e);
    default: return s;
  }
}

// ---- per-tab state map ----

type TabStates = Map<string, State>;

function getOrCreateState(states: TabStates, tabId: string): State {
  if (!states.has(tabId)) states.set(tabId, { ...initialState });
  return states.get(tabId)!;
}

function messageActionBusyText(scope: MessageActionScope): string {
  switch (scope) {
    case "fork":
      return t("rewind.busyFork");
    case "summ-from":
      return t("rewind.busySummFrom");
    case "summ-upto":
      return t("rewind.busySummUpto");
    case "conversation":
      return t("rewind.busyConversation");
    case "code":
      return t("rewind.busyCode");
    default:
      return t("rewind.busyBoth");
  }
}

function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message;
  if (typeof err === "string") return err;
  return String(err || "");
}

async function refreshMetaForTab(tabId: string, dispatchTo: (tabId: string, action: Action) => void): Promise<void> {
  try {
    dispatchTo(tabId, { type: "meta", meta: await app.MetaForTab(tabId) });
    dispatchTo(tabId, { type: "context", context: await app.ContextUsageForTab(tabId) });
    dispatchTo(tabId, { type: "effort", effort: await app.EffortForTab(tabId) });
  } catch {
    /* ignore */
  }
}

export function useController(getProfile?: () => string) {
  const statesRef = useRef<TabStates>(new Map());
  // Per-tab last-token timestamp for the stale-stream watchdog. A single global
  // value would cross-talk between tabs: tokens from a background tab's stream
  // would keep the ACTIVE tab's watchdog fed (a dead stream never reconciles),
  // and vice versa.
  const lastTokenAt = useRef(new Map<string, number>());
  const [activeTabId, setActiveTabId] = useState<string | undefined>();
  const activeTabIdRef = useRef<string | undefined>(undefined);
  // Read the active product profile ("dev"|"cowork") via the getter supplied by
  // App — it mirrors the active tab's profile and is passed to backend calls
  // (OpenProjectTab/OpenGlobalTab) that scope topics to a profile.
  const profile = () => {
    try { return getProfile?.() ?? ""; } catch { return ""; }
  };
  // A render-triggering counter so that mutations to a non-active tab's state still
  // cause a re-render when that tab becomes active.
  const [, setVersion] = useState(0);
  const bump = useCallback(() => setVersion((v) => v + 1), []);

  // The active tab's current state, with a stable identity for cancel().
  const activeState = activeTabId ? getOrCreateState(statesRef.current, activeTabId) : initialState;
  const stateRef = useRef(activeState);
  activeTabIdRef.current = activeTabId;
  stateRef.current = activeState;

  // Dispatch to a specific tab's state. If the tab doesn't have state yet, it's
  // created. Bumps the version so React re-renders when it becomes active.
  const dispatchTo = useCallback((tabId: string, action: Action) => {
    const states = statesRef.current;
    const prev = getOrCreateState(states, tabId);
    const next = reducer(prev, action);
    if (prev !== next) {
      states.set(tabId, next);
      bump();
    }
  }, [bump]);

  const checkpointRefreshSeq = useRef(new Map<string, number>());
  const sessionLoadSeq = useRef(new Map<string, number>());
  const bumpSessionLoadSeq = useCallback((tabId: string): number => {
    const seq = (sessionLoadSeq.current.get(tabId) ?? 0) + 1;
    sessionLoadSeq.current.set(tabId, seq);
    return seq;
  }, []);
  const sessionLoadCurrent = useCallback((tabId: string, seq: number): boolean => {
    return sessionLoadSeq.current.get(tabId) === seq;
  }, []);
  const bumpCheckpointRefreshSeq = useCallback((tabId: string): number => {
    const seq = (checkpointRefreshSeq.current.get(tabId) ?? 0) + 1;
    checkpointRefreshSeq.current.set(tabId, seq);
    return seq;
  }, []);
  const refreshCheckpoints = useCallback(async (tabId: string) => {
    const seq = bumpCheckpointRefreshSeq(tabId);
    const checkpoints = await app.CheckpointsForTab(tabId).catch(() => undefined);
    if (checkpointRefreshSeq.current.get(tabId) !== seq || checkpoints === undefined) return;
    dispatchTo(tabId, { type: "checkpoints", checkpoints: asArray(checkpoints) });
  }, [bumpCheckpointRefreshSeq, dispatchTo]);

  const loadSessionDataForTab = useCallback(async (tabId: string, reset = false) => {
    const seq = bumpSessionLoadSeq(tabId);
    const safe = <T,>(p: Promise<T>): Promise<T | undefined> => p.catch(() => undefined);
    const [meta, context, effort, jobs, checkpoints, history, present] = await Promise.all([
      safe(app.MetaForTab(tabId)),
      safe(app.ContextUsageForTab(tabId)),
      safe(app.EffortForTab(tabId)),
      safe(app.JobsForTab(tabId)),
      safe(app.CheckpointsForTab(tabId)),
      safe(app.HistoryForTab(tabId)),
      safe(app.PresentForTab(tabId)),
    ]);
    if (!sessionLoadCurrent(tabId, seq)) return;
    if (reset) dispatchTo(tabId, { type: "reset" });
    if (meta) dispatchTo(tabId, { type: "meta", meta });
    if (context) dispatchTo(tabId, { type: "context", context });
    if (effort) dispatchTo(tabId, { type: "effort", effort });
    if (jobs) dispatchTo(tabId, { type: "jobs", jobs: asArray(jobs) });
    if (checkpoints) dispatchTo(tabId, { type: "checkpoints", checkpoints: asArray(checkpoints) });
    const messages = asArray<HistoryMessage>(history);
    if (messages.length) dispatchTo(tabId, { type: "history", messages });
    // The presentation sidecar carries the rich view-only fields provider.Message
    // can't (readOnly, durationMs, truncated, attachments, parentId, notice/phase/
    // compaction cards). Dispatch it AFTER history so the reducer can merge the
    // rich fields onto the history-built tool items by id, and insert any cards
    // history couldn't reconstruct.
    if (present && Array.isArray(present.records) && present.records.length) {
      dispatchTo(tabId, { type: "present", records: present.records });
    }
  }, [bumpSessionLoadSeq, dispatchTo, sessionLoadCurrent]);

  const activeTabFromBackend = useCallback(async (): Promise<TabMeta | undefined> => {
    const tabs = asArray(await app.ListTabs().catch(() => [] as TabMeta[]));
    return tabs.find((tab) => tab.active) ?? tabs[0];
  }, []);

  const waitForTabReady = useCallback(async (tabId: string): Promise<void> => {
    for (let attempt = 0; attempt < 60; attempt += 1) {
      const tabs = asArray(await app.ListTabs().catch(() => [] as TabMeta[]));
      const tab = tabs.find((candidate) => candidate.id === tabId);
      if (!tab || tab.ready || tab.startupErr) return;
      await new Promise((resolve) => window.setTimeout(resolve, 100));
    }
  }, []);

  const reconcileTabRuntime = useCallback(async (tabId: string) => {
    const tabs = asArray(await app.ListTabs().catch(() => [] as TabMeta[]));
    const tab = tabs.find((candidate) => candidate.id === tabId);
    if (!tab) return;
    const local = statesRef.current.get(tabId);
    const needsInitialLoad = !local?.meta;
    const missedTurnDone = Boolean(local?.running && !tab.running);
    // Expert-session tabs must always re-fetch meta on activation: the
    // expertSession field (which selects ExpertSessionView vs Transcript and
    // supplies the teamId) is populated from the backend's tab flags. A cached
    // meta from before the controller was ready, or a meta whose expertSession
    // was dropped by a stale sameMeta guard, would render the wrong view.
    const isExpertTab = Boolean(tab.expertSession);
    dispatchTo(tabId, { type: "backend_status", running: Boolean(tab.running) });
    if (needsInitialLoad || missedTurnDone || isExpertTab) {
      await loadSessionDataForTab(tabId, missedTurnDone);
      return;
    }
    const [jobs, effort] = await Promise.all([
      app.JobsForTab(tabId).catch(() => undefined),
      app.EffortForTab(tabId).catch(() => undefined),
    ]);
    if (jobs) dispatchTo(tabId, { type: "jobs", jobs: asArray(jobs) });
    if (effort) dispatchTo(tabId, { type: "effort", effort });
  }, [dispatchTo, loadSessionDataForTab]);

  // reset=true forces a clean rebuild from backend data — only for callers that
  // know the controller was replaced behind the frontend's back (profile or
  // workspace switch, fork). reset=false reconciles instead of reloading: a tab
  // whose in-memory state already exists keeps it, because the global event
  // subscription kept that state current while the tab was backgrounded.
  // Rebuilding from history mid-run would swallow the in-flight turn — the
  // current LLM step's streaming text only persists when the step completes.
  const syncActiveTabFromBackend = useCallback(async (reset = false): Promise<string | undefined> => {
    const active = await activeTabFromBackend();
    if (!active) return undefined;
    setActiveTabId(active.id);
    if (reset) {
      await loadSessionDataForTab(active.id, true);
    } else {
      await reconcileTabRuntime(active.id);
    }
    return active.id;
  }, [activeTabFromBackend, loadSessionDataForTab, reconcileTabRuntime]);

  useEffect(() => {
    // Stream events (token deltas AND per-chunk tool output) are coalesced
    // into one dispatch per animation frame. Tool events used to dispatch
    // immediately: every tool_progress chunk copied the whole items array and
    // re-rendered the transcript, so a tool emitting hundreds of chunks
    // froze the UI exactly like a token-per-render storm would.
    const streamBatch = createRafBatch<{ tabId: string; e: WireEvent }>((batch) => {
      for (const { tabId, e } of batch) dispatchTo(tabId, { type: "event", e });
    });
    const batchedKinds = new Set([
      "text",
      "reasoning",
      "tool_dispatch",
      "tool_result",
      "tool_progress",
      "tool_args_delta",
    ]);
    const off = onEvent((e) => {
      const targetTabId = e.tabId || activeTabIdRef.current;
      if (!targetTabId) return;
      if (e.kind === "turn_started" || e.kind === "text" || e.kind === "reasoning") {
        lastTokenAt.current.set(targetTabId, Date.now());
      }
      if (batchedKinds.has(e.kind)) {
        streamBatch.push({ tabId: targetTabId, e });
      } else {
        streamBatch.drain();
        dispatchTo(targetTabId, { type: "event", e });
      }
      if (e.kind === "turn_done") {
        void refreshMetaForTab(targetTabId, dispatchTo);
        app
          .ContextUsageForTab(targetTabId)
          .then((context) => dispatchTo(targetTabId, { type: "context", context }))
          .catch(() => {});
        app.EffortForTab(targetTabId).then((effort) => dispatchTo(targetTabId, { type: "effort", effort })).catch(() => {});
        void refreshCheckpoints(targetTabId);
      }
      if (e.kind === "turn_done" || e.kind === "notice") {
        app.JobsForTab(targetTabId).then((jobs) => dispatchTo(targetTabId, { type: "jobs", jobs: asArray(jobs) })).catch(() => {});
      }
    });

    const offReady = onReady((readyTabId) => {
      // Prefer the tab the event names — a background tab finishing its build
      // must not trigger a reload of the ACTIVE (possibly streaming) tab.
      const target = readyTabId || activeTabIdRef.current;
      if (target) {
        void loadSessionDataForTab(target);
        return;
      }
      void syncActiveTabFromBackend();
    });

    void syncActiveTabFromBackend();
    // The event subscription is live now, so ask the backend to re-emit any
    // approval/ask prompt that was already blocking a tab before this load —
    // otherwise a session left mid-confirmation shows "waiting" with no modal
    // and no way to stop (#3844).
    void app.ReplayPendingPrompts().catch(() => {});

    return () => { streamBatch.drain(); off(); offReady(); };
  }, [dispatchTo, loadSessionDataForTab, refreshCheckpoints, syncActiveTabFromBackend]);

  // Stale-stream watchdog: if the frontend thinks the agent is running but
  // no token events have arrived for 120 seconds, reconcile with the backend.
  // This catches the case where the Wails event channel silently drops the
  // turn_done event after a model-service interruption (#3746). The threshold
  // is deliberately generous: a reconcile triggers a full reset+history reload
  // that discards in-flight tool output, so long compiles/tests/generations
  // (which may legitimately pause text streaming) must not trip it. This only
  // fires while the assistant is actively streaming text (s.live set); a pure
  // tool run has no live stream and is out of scope here.
  useEffect(() => {
    if (!activeTabId) return;
    const s = statesRef.current.get(activeTabId);
    if (!s?.running || !s.live) return;
    const lastToken = lastTokenAt.current.get(activeTabId) ?? 0;
    const since = Date.now() - lastToken;
    if (since >= 120_000) {
      void reconcileTabRuntime(activeTabId);
      return;
    }
    const timer = window.setTimeout(() => {
      const cur = statesRef.current.get(activeTabId);
      if (cur?.running && cur.live && Date.now() - (lastTokenAt.current.get(activeTabId) ?? 0) >= 120_000) {
        void reconcileTabRuntime(activeTabId);
      }
    }, 120_000 - since);
    return () => window.clearTimeout(timer);
  }, [activeTabId, reconcileTabRuntime, activeState.running, activeState.live]);

  const send = useCallback((displayText: string, submitText = displayText) => {
    const submitForTab = (tabId: string) => {
      const seq = getOrCreateState(statesRef.current, tabId).seq;
      dispatchTo(tabId, { type: "user", text: displayText, seq });
      const display = displayText.trim();
      const submit = submitText.trim();
      (display !== submit ? app.SubmitDisplayToTab(tabId, display, submit) : app.SubmitToTab(tabId, submit)).catch((error) => {
        dispatchTo(tabId, { type: "send_failed", error: `Send failed: ${error instanceof Error ? error.message : String(error)}` });
      });
    };
    const tabId = activeTabIdRef.current ?? activeTabId;
    if (tabId) {
      submitForTab(tabId);
      return;
    }
    void activeTabFromBackend().then((active) => {
      if (!active?.id) return;
      setActiveTabId(active.id);
      submitForTab(active.id);
    });
  }, [activeTabFromBackend, activeTabId, dispatchTo]);

  const runShell = useCallback((command: string) => {
    if (!activeTabId) return;
    dispatchTo(activeTabId, { type: "user", text: `!${command}`, seq: getOrCreateState(statesRef.current, activeTabId).seq });
    app.RunShellForTab(activeTabId, command).catch(() => {});
  }, [activeTabId, dispatchTo]);

  const steer = useCallback((text: string) => {
    if (!activeTabId) return;
    // No optimistic user bubble: rewind/fork map turns by counting user items,
    // and a steer is not a backend turn — the Steer event's ↪ notice is the
    // visible confirmation (#3660).
    app.SteerForTab(activeTabId, text).catch(() => {});
  }, [activeTabId]);

  const notice = useCallback((text: string, level: "info" | "warn" = "info") => {
    if (!activeTabId) return;
    dispatchTo(activeTabId, { type: "local_notice", level, text });
  }, [activeTabId, dispatchTo]);

  const cancel = useCallback((): string | undefined => {
    const cur = stateRef.current;
    const tabId = activeTabId;
    if (cur.running && cur.pendingUser !== undefined) {
      const text = cur.pendingUser;
      if (tabId) {
        dispatchTo(tabId, { type: "unsend" });
        app.CancelTab(tabId).catch(() => {});
      }
      return text;
    }
    if (tabId) app.CancelTab(tabId).catch(() => {});
    return undefined;
  }, [activeTabId, dispatchTo]);

  // pauseToggle flips between Pause and ResumeTurn based on current state. When
  // the turn is running and not yet paused, it requests a pause (the agent
  // finishes its current step, then freezes). When paused, it resumes. No-op
  // when nothing is running. Reads stateRef so the callback identity is stable
  // across renders (the Composer memoizes on it).
  const pauseToggle = useCallback((): void => {
    const cur = stateRef.current;
    const tabId = activeTabId;
    if (!tabId || !cur.running) return;
    if (cur.paused) {
      app.ResumeTurnTab(tabId).catch(() => {});
    } else {
      app.PauseTab(tabId).catch(() => {});
    }
  }, [activeTabId]);

  const approve = useCallback((id: string, allow: boolean, session: boolean, persist: boolean) => {
    if (!activeTabId) return;
    dispatchTo(activeTabId, { type: "clearApproval" });
    app.ApproveTab(activeTabId, id, allow, session, persist).catch(() => {});
  }, [activeTabId, dispatchTo]);

  const answerQuestion = useCallback((id: string, answers: QuestionAnswer[]) => {
    if (!activeTabId) return;
    dispatchTo(activeTabId, { type: "clearAsk" });
    app.AnswerQuestionForTab(activeTabId, id, answers).catch(() => {});
  }, [activeTabId, dispatchTo]);

  const setCollaborationMode = useCallback(async (mode: CollaborationMode): Promise<void> => {
    if (!activeTabId) return;
    await app.SetCollaborationModeForTab(activeTabId, mode).catch(() => {});
    await refreshMetaForTab(activeTabId, dispatchTo);
  }, [activeTabId, dispatchTo]);

  const setToolApprovalMode = useCallback(async (mode: ToolApprovalMode): Promise<void> => {
    if (!activeTabId) return;
    await app.SetToolApprovalModeForTab(activeTabId, mode).catch(() => {});
    if (mode === "auto" || mode === "yolo") dispatchTo(activeTabId, { type: "clearApproval" });
    await refreshMetaForTab(activeTabId, dispatchTo);
  }, [activeTabId, dispatchTo]);

  const setGoal = useCallback(async (goal: string): Promise<void> => {
    if (!activeTabId) return;
    await app.SetGoalForTab(activeTabId, goal).catch(() => {});
    await refreshMetaForTab(activeTabId, dispatchTo);
  }, [activeTabId, dispatchTo]);

  // setRagScope picks the knowledge-base collection for auto-injection (""
  // = 不使用 / off, the default). Per-tab, persisted on the backend.
  const setRagScope = useCallback(async (scope: string): Promise<void> => {
    if (!activeTabId) return;
    await app.SetRagScopeForTab(activeTabId, scope).catch(() => {});
    await refreshMetaForTab(activeTabId, dispatchTo);
  }, [activeTabId, dispatchTo]);

  const clearGoal = useCallback(async (): Promise<void> => {
    if (!activeTabId) return;
    await app.ClearGoalForTab(activeTabId).catch(() => {});
    await refreshMetaForTab(activeTabId, dispatchTo);
  }, [activeTabId, dispatchTo]);

  const newSession = useCallback(async () => {
    const tabId = activeTabId;
    if (tabId) bumpCheckpointRefreshSeq(tabId);
    try {
      await app.NewSession();
    } catch {
      return; // backend refused (workspace starting / failed) — keep the transcript
    }
    if (tabId) dispatchTo(tabId, { type: "reset" });
  }, [activeTabId, bumpCheckpointRefreshSeq, dispatchTo]);

  const clearSession = useCallback(async () => {
    const tabId = activeTabId;
    if (tabId) bumpCheckpointRefreshSeq(tabId);
    try {
      await app.ClearSession();
    } catch {
      return;
    }
    if (tabId) dispatchTo(tabId, { type: "reset" });
  }, [activeTabId, bumpCheckpointRefreshSeq, dispatchTo]);

  // Session lists are profile-scoped: the sidebar/history/palette surfaces of
  // dev/cowork/netdev are independent, so a mode never lists another mode's
  // conversations. Falls back to the unscoped list when the backend is older.
  const listSessions = useCallback(async (): Promise<SessionMeta[]> => {
    try {
      return asArray<SessionMeta>(await app.ListSessionsForProfile(profile()));
    } catch {
      return asArray<SessionMeta>(await app.ListSessions().catch(() => []));
    }
  }, [getProfile]);
  const listTrashedSessions = useCallback(async (): Promise<SessionMeta[]> => {
    try {
      return asArray<SessionMeta>(await app.ListTrashedSessionsForProfile(profile()));
    } catch {
      return asArray<SessionMeta>(await app.ListTrashedSessions().catch(() => []));
    }
  }, [getProfile]);
  const resumeSession = useCallback(async (path: string, tabId?: string) => {
    const targetTabId = tabId || activeTabId;
    if (!targetTabId) return;
    if (tabId) await waitForTabReady(tabId);
    const messages = asArray(
      await (tabId ? app.ResumeSessionForTab(tabId, path) : app.ResumeSession(path)).catch(() => [] as HistoryMessage[]),
    );
    if (messages.length === 0) return;
    dispatchTo(targetTabId, { type: "reset" });
    dispatchTo(targetTabId, { type: "history", messages });
    // Rich view-only fields (durations, cards, attachments) live in the present
    // stream, not provider.Message — fetch it so a resumed transcript keeps
    // them instead of degrading to plain text.
    void app.PresentForTab(targetTabId)
      .then((payload) => {
        if (payload && Array.isArray(payload.records) && payload.records.length) {
          dispatchTo(targetTabId, { type: "present", records: payload.records });
        }
      })
      .catch(() => {});
    app.ContextUsageForTab(targetTabId).then((context) => dispatchTo(targetTabId, { type: "context", context })).catch(() => {});
    void refreshCheckpoints(targetTabId);
  }, [activeTabId, dispatchTo, refreshCheckpoints, waitForTabReady]);

  const previewSession = useCallback(async (path: string): Promise<HistoryMessage[]> => asArray<HistoryMessage>(await app.PreviewSession(path).catch(() => [])), []);
  const deleteSession = useCallback((path: string) => app.DeleteSession(path).catch(() => {}), []);
  const restoreSession = useCallback((path: string) => app.RestoreSession(path).catch(() => {}), []);
  const purgeTrashedSession = useCallback((path: string) => app.PurgeTrashedSession(path).catch(() => {}), []);
  const renameSession = useCallback((path: string, title: string) => app.RenameSession(path, title).catch(() => {}), []);

  const refreshMeta = useCallback(async () => {
    if (!activeTabId) return;
    try {
      dispatchTo(activeTabId, { type: "meta", meta: await app.MetaForTab(activeTabId) });
      dispatchTo(activeTabId, { type: "context", context: await app.ContextUsageForTab(activeTabId) });
      dispatchTo(activeTabId, { type: "effort", effort: await app.EffortForTab(activeTabId) });
    } catch { /* ignore */ }
  }, [activeTabId, dispatchTo]);

  const refreshWorkspaceState = useCallback(async (path: string): Promise<string> => {
    if (path) await syncActiveTabFromBackend(true);
    return path;
  }, [syncActiveTabFromBackend]);

  const pickWorkspace = useCallback(async (): Promise<string> => {
    const path = await app.PickWorkspace().catch(() => "");
    return refreshWorkspaceState(path);
  }, [refreshWorkspaceState]);
  const switchWorkspace = useCallback(async (path: string): Promise<string> => {
    const next = await app.SwitchWorkspace(path).catch(() => "");
    return refreshWorkspaceState(next);
  }, [refreshWorkspaceState]);

  const setModel = useCallback(async (name: string) => {
    if (!activeTabId) return;
    try {
      await app.SetModelForTab(activeTabId, name);
    } catch (err) {
      dispatchTo(activeTabId, { type: "local_notice", level: "warn", text: t("status.modelSwitchFailed", { err: errorMessage(err) }) });
      return;
    }
    try {
      dispatchTo(activeTabId, { type: "meta", meta: await app.MetaForTab(activeTabId) });
      dispatchTo(activeTabId, { type: "context", context: await app.ContextUsageForTab(activeTabId) });
      dispatchTo(activeTabId, { type: "effort", effort: await app.EffortForTab(activeTabId) });
    } catch { /* ignore */ }
  }, [activeTabId, dispatchTo]);

  const setEffort = useCallback(async (level: string) => {
    if (!activeTabId) return;
    await app.SetEffortForTab(activeTabId, level).catch(() => {});
    try {
      dispatchTo(activeTabId, { type: "meta", meta: await app.MetaForTab(activeTabId) });
      dispatchTo(activeTabId, { type: "context", context: await app.ContextUsageForTab(activeTabId) });
      dispatchTo(activeTabId, { type: "effort", effort: await app.EffortForTab(activeTabId) });
    } catch { /* ignore */ }
  }, [activeTabId, dispatchTo]);

  const fetchMemory = useCallback((): Promise<MemoryView> =>
    app.Memory().catch(() => ({ docs: [], facts: [], scopes: [], storeDir: "", available: false })), []);
  const remember = useCallback(async (scope: string, note: string) => { await app.Remember(scope, note).catch(() => {}); }, []);
  const forget = useCallback(async (name: string) => { await app.Forget(name).catch(() => {}); }, []);
  const saveDoc = useCallback(async (path: string, body: string) => { await app.SaveDoc(path, body).catch(() => {}); }, []);

  const rewind = useCallback(async (turn: number, scope: string) => {
    const sourceTabId = activeTabId;
    if (!sourceTabId) return;
    const actionScope = (["fork", "summ-from", "summ-upto", "conversation", "code", "both"].includes(scope) ? scope : "both") as MessageActionScope;
    dispatchTo(sourceTabId, { type: "message_action_start", action: { turn, scope: actionScope } });
    dispatchTo(sourceTabId, { type: "local_notice", level: "info", text: messageActionBusyText(actionScope) });
    try {
      if (actionScope === "fork") {
        const tab = await app.Fork(turn);
        if (tab?.id) {
          setActiveTabId(tab.id);
          // The fork's controller builds in a background goroutine: an immediate
          // load reads empty history, and the ready-event fallback can still
          // target the source tab, leaving the fork blank (#3742).
          await waitForTabReady(tab.id);
          await loadSessionDataForTab(tab.id, true);
        } else {
          await syncActiveTabFromBackend(true);
        }
        return;
      }

      if (actionScope === "summ-from") await app.SummarizeFrom(turn);
      else if (actionScope === "summ-upto") await app.SummarizeUpTo(turn);
      else await app.Rewind(turn, actionScope);

      const messages = asArray(await app.HistoryForTab(sourceTabId).catch(() => [] as HistoryMessage[]));
      dispatchTo(sourceTabId, { type: "reset" });
      if (messages.length) dispatchTo(sourceTabId, { type: "history", messages });
      dispatchTo(sourceTabId, { type: "context", context: await app.ContextUsageForTab(sourceTabId) });
      dispatchTo(sourceTabId, { type: "checkpoints", checkpoints: asArray(await app.CheckpointsForTab(sourceTabId)) });
    } catch {
      /* The controller emits a warning notice with the specific failure reason. */
    } finally {
      dispatchTo(sourceTabId, { type: "message_action_done" });
    }
  }, [activeTabId, dispatchTo, loadSessionDataForTab, syncActiveTabFromBackend, waitForTabReady]);

  // deleteExpertCollab removes the Nth expert-collab message from the active
  // tab's session and re-renders from the refreshed history. Mirrors rewind's
  // call-then-history-dispatch flow but without the checkpoint/notice ceremony.
  const deleteExpertCollab = useCallback(async (ordinal: number) => {
    const sourceTabId = activeTabId;
    if (!sourceTabId) return;
    try {
      const messages = asArray(await app.DeleteExpertCollab(sourceTabId, ordinal));
      dispatchTo(sourceTabId, { type: "reset" });
      if (messages.length) dispatchTo(sourceTabId, { type: "history", messages });
    } catch { /* the card stays; a failure here is rare and silent */ }
  }, [activeTabId, dispatchTo]);

  // Tab management: switch preserves per-tab state; open creates it. Opening a
  // topic whose tab already exists (backend reuses it) is also a switch: the
  // global event subscription kept that tab's state current while it was
  // backgrounded, so reconcile — never rebuild from history, which would
  // swallow an in-flight turn's streaming bubbles and tool cards.
  const switchTab = useCallback(async (tabId: string) => {
    setActiveTabId(tabId);
    try {
      await app.SetActiveTab(tabId);
      await reconcileTabRuntime(tabId);
    } catch { /* ignore */ }
  }, [reconcileTabRuntime]);

  const openProjectTab = useCallback(async (workspaceRoot: string, topicId: string): Promise<TabMeta> => {
    const meta = await app.OpenProjectTab3(workspaceRoot, topicId, profile());
    setActiveTabId(meta.id);
    await reconcileTabRuntime(meta.id);
    return meta;
  }, [getProfile, reconcileTabRuntime]);

  const openGlobalTab = useCallback(async (topicId: string): Promise<TabMeta> => {
    const meta = await app.OpenGlobalTab(topicId, profile());
    setActiveTabId(meta.id);
    await reconcileTabRuntime(meta.id);
    return meta;
  }, [getProfile, reconcileTabRuntime]);

  // Ensure a blank tab exists for the given scope — reuses an existing one
  // or creates a new tab, then loads its session data.
  const ensureBlankTab = useCallback(async (scope: string, workspaceRoot: string, profile?: string): Promise<TabMeta> => {
    const meta = await app.EnsureBlankTab(scope, workspaceRoot, profile ?? getProfile?.() ?? "");
    setActiveTabId(meta.id);
    await reconcileTabRuntime(meta.id);
    return meta;
  }, [getProfile, reconcileTabRuntime]);

  const closeTab = useCallback(async (tabId: string) => {
    try {
      await app.CloseTab(tabId);
      statesRef.current.delete(tabId);
      bump();
      // Closing a tab promotes another (possibly streaming, backgrounded) tab
      // to active — reconcile it, don't reset-and-reload.
      if (tabId === activeTabId) await syncActiveTabFromBackend();
    } catch { /* ignore */ }
  }, [activeTabId, bump, syncActiveTabFromBackend]);

  const reorderTabs = useCallback(async (tabIds: string[]) => {
    try {
      await app.ReorderTabs(tabIds);
    } catch { /* ignore */ }
  }, []);

  return {
    state: activeState,
    activeTabId,
    send, runShell, steer, notice, cancel, pauseToggle, approve, answerQuestion, setCollaborationMode, setToolApprovalMode, setGoal, clearGoal, setRagScope,
    newSession, clearSession, listSessions, listTrashedSessions, resumeSession, previewSession, deleteSession, restoreSession, purgeTrashedSession, renameSession,
    refreshMeta, pickWorkspace, switchWorkspace, rewind, deleteExpertCollab, setModel, setEffort,
    fetchMemory, remember, forget, saveDoc,
    switchTab, openProjectTab, openGlobalTab, ensureBlankTab, closeTab, reorderTabs,
    syncActiveTab: syncActiveTabFromBackend,
  };
}
