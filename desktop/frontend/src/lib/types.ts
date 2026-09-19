// Wire contract — mirrors desktop/wire.go (itself mirroring internal/serve/wire.go).
// One event channel carries every kind; `kind` discriminates the payload.

export type EventKind =
  | "turn_started"
  | "reasoning"
  | "text"
  | "message"
  | "tool_dispatch"
  | "tool_result"
  | "tool_args_delta"
  | "tool_progress"
  | "usage"
  | "notice"
  | "phase"
  | "approval_request"
  | "ask_request"
  | "turn_done"
  | "compaction_started"
  | "compaction_done"
  | "retrying"
  | "steer"
  | "paused"
  | "resumed"
  | "expert_collab";

export interface WireCompaction {
  trigger?: string; // "auto" | "manual"
  messages?: number; // done: how many messages were folded into the summary
  summary?: string; // done: the briefing (empty on an aborted pass)
  archive?: string; // done: archive path, if any
}

// WireCollab is the payload of an "expert_collab" event: a finished expert-team
// collaboration appended to the transcript. Carries the full record (per-round
// expert answers + synthesis) so the frontend renders the folded card without an
// extra fetch. Mirrors desktop/wire.go wireCollab.
export interface WireCollabAnswer {
  expertName: string;
  text: string;
}
export interface WireCollab {
  runId: string;
  teamId: string;
  teamName: string;
  task: string;
  mode: string; // "parallel" | "debate" | "pipeline"
  rounds: WireCollabAnswer[][];
  synthesis: string;
  createdAt: number; // unix ms
}

export interface WireProfile {
  model?: string;
  effort?: string;
}

export interface WireTool {
  id?: string;
  name: string;
  args?: string;
  output?: string;
  err?: string;
  readOnly: boolean;
  truncated?: boolean;
  durationMs?: number;
  partial?: boolean; // an early dispatch (name only) — a full one with args follows
  parentId?: string; // set on a sub-agent's calls — the parent `task` call's id
  profile?: WireProfile; // subagent model/effort resolved for this call
  attachments?: WireAttachment[]; // files the tool produced (e.g. generated images)
  fileDiff?: WireFileDiff; // server-side preview on writer dispatches (authoritative — covers apply_patch)
}

export interface WireFileDiff {
  diff: string;
  added: number;
  removed: number;
}

export interface WireAttachment {
  path: string; // repo-relative, under .hiq/attachments/
  kind: string; // "image"
}

export interface WireUsage {
  promptTokens: number;
  completionTokens: number;
  totalTokens: number;
  cacheHitTokens: number;
  cacheMissTokens: number;
  cacheWriteTokens?: number; // cache-creation writes (Anthropic); billed above input
  reasoningTokens?: number;
  // Session-cumulative cache tokens — the status bar shows the aggregate
  // hit-rate (Σhit/Σ(hit+miss)), steadier than the single-turn cacheHitTokens.
  // Some providers currently do not report these fields, so they remain 0.
  sessionCacheHitTokens: number;
  sessionCacheMissTokens: number;
  cost?: number; // this turn's cost per the model's pricing table (omitted when unknown)
  currency?: string;
  costUsd?: number;
}

export interface WireApproval {
  id: string;
  tool: string;
  subject: string;
  args?: string; // raw JSON of the call being approved (bash command, target path…)
  changes?: WireFileChange[]; // previewed per-file diffs for writer tools
}

export interface WireFileChange {
  path: string;
  kind: "create" | "modify" | "delete" | string;
  added: number;
  removed: number;
  diff: string;
}

export interface WireAskOption {
  label: string;
  description?: string;
}

export interface WireAskQuestion {
  id: string;
  header?: string;
  prompt: string;
  options: WireAskOption[];
  multi?: boolean;
}

export interface WireAsk {
  id: string;
  questions: WireAskQuestion[];
}

// QuestionAnswer is the reply for one question, sent back via AnswerQuestion.
export interface QuestionAnswer {
  questionId: string;
  selected: string[];
}

export interface WireEvent {
  kind: EventKind;
  text?: string;
  reasoning?: string;
  level?: "info" | "warn";
  tool?: WireTool;
  usage?: WireUsage;
  approval?: WireApproval;
  ask?: WireAsk;
  compaction?: WireCompaction;
  collab?: WireCollab;
  err?: string;
  retryAttempt?: number;
  retryMax?: number;
  retryAfterMs?: number; // backoff before the retry attempt (0/undefined = immediate)
  // Tab routing: set by the Go-side tabEventSink so multi-tab frontends
  // route each event to the correct per-tab reducer.
  tabId?: string;
  sessionHitTokens?: number;
  sessionMissTokens?: number;
}

// Tab management types (desktop/tabs.go).
export interface TabMeta {
  id: string;
  remote?: { kind: string; target: string; user?: string; label?: string } | null;
  remoteState?: string;
  tabType?: "session" | "file";
  scope: string;
  workspaceRoot: string;
  workspaceName: string;
  topicId: string;
  topicTitle: string;
  filePath?: string;
  projectColor?: string;
  label: string;
  ready: boolean;
  running: boolean;
  mode: Mode;
  collaborationMode?: CollaborationMode;
  toolApprovalMode?: ToolApprovalMode;
  // Knowledge-base collection to auto-inject ("ragScope"); "" = 不使用 (default).
  ragScope?: string;
  goal?: string;
  goalStatus?: GoalStatus;
  startupErr?: string;
  active: boolean;
  cwd: string;
  // Product profile ("dev" | "cowork"); absent = dev. Drives layout selection.
  profile?: string;
  // Set when this tab is an independent expert-team collaboration session.
  // The frontend renders ExpertSessionView (group-chat) instead of the normal
  // transcript when present.
  expertSession?: ExpertSessionMeta;
}

// ExpertSessionMeta carries the team context for an expert-session tab.
export interface ExpertSessionMeta {
  teamId: string;
  teamName: string;
}

export interface ProjectNode {
  key: string;
  kind: "project" | "topic" | "global_folder" | "global_topic" | "expert_folder" | "expert_topic";
  label: string;
  root?: string;
  topicId?: string;
  remote?: { kind: string; target: string; user?: string; label?: string; keyPath?: string; tls?: boolean } | null;
  sessionPath?: string;
  projectColor?: string;
  turns?: number;
  createdAt?: number;
  lastActivityAt?: number;
  open?: boolean;
  running?: boolean;
  status?: ProjectTopicStatus;
  children?: ProjectNode[];
  expertTeamId?: string;
  expertTeamName?: string;
}

export type ProjectTopicStatus = "thinking" | "streaming" | "waiting_confirmation" | "paused" | "error";

export interface TopicMeta {
  id: string;
  title: string;
  createdAt: number;
}

// Bound-method payloads (desktop/app.go).
export interface HistoryMessage {
  role: string;
  content: string;
  reasoning?: string;
  level?: "info" | "warn";
  toolCalls?: HistoryToolCall[];
  toolCallId?: string;
  toolName?: string;
  pending?: boolean;
  trigger?: string;
  messages?: number;
  summary?: string;
  archive?: string;
}

export interface HistoryToolCall {
  id: string;
  name: string;
  arguments: string;
  subagent?: HistoryMessage[];
  subagentRef?: string;
}

// PresentPayload is the rich view-only event stream persisted alongside the
// session (the <session>.present.jsonl sidecar). It carries what
// provider.Message cannot: tool dispatches with readOnly/profile/parentId,
// results with durationMs/truncated/attachments, notice/phase/compaction cards,
// streamed tool-progress chunks. The frontend rebuilds the exact transcript a
// user saw from these records after a reload, instead of the degraded
// message-only history. rewriteVersion lets a stale sidecar (saved before a
// compaction that hasn't re-flushed) be detected.
export interface PresentPayload {
  records: PresentRecord[];
  rewriteVersion?: number;
}

export type PresentKind =
  | "turn_started"
  | "reasoning"
  | "text"
  | "message"
  | "tool_dispatch"
  | "tool_result"
  | "tool_progress"
  | "usage"
  | "notice"
  | "phase"
  | "compaction_started"
  | "compaction_done"
  | "retrying"
  | "steer"
  | "paused"
  | "resumed"
  | "expert_collab";

export interface PresentTool {
  id?: string;
  name?: string;
  args?: string;
  output?: string;
  err?: string;
  readOnly?: boolean;
  truncated?: boolean;
  durationMs?: number;
  parentId?: string;
  attachments?: { path?: string; kind?: string }[];
  profile?: { model?: string; effort?: string };
  fileDiff?: WireFileDiff; // server-side preview persisted for replay
}

export interface PresentCompaction {
  trigger?: string;
  messages?: number;
  summary?: string;
  archive?: string;
}

export interface PresentRetry {
  attempt: number;
  max?: number;
}

export interface PresentRecord {
  kind: PresentKind;
  text?: string;
  reasoning?: string;
  level?: string;
  tool?: PresentTool;
  usage?: { inputTokens?: number; outputTokens?: number; totalTokens?: number; cacheRead?: number };
  compaction?: PresentCompaction;
  collab?: unknown;
  retry?: PresentRetry;
}

// CheckpointMeta is one rewind point (a user turn) for the rewind UI.
export interface CheckpointMeta {
  turn: number;
  prompt: string;
  files: string[];
  time: number; // unix ms
  canCode?: boolean;
  canConversation?: boolean;
  // Marks the synthetic reverse checkpoint the latest code rewind left
  // behind; rewinding code to it replays (reapplies) that rewind.
  reapply?: boolean;
}

// One file's ZCode-style safety verdict for a code rewind.
export interface RewindPreviewFileView {
  path: string;
  class: string; // safe | unsafe | ignored
}

// SessionMeta is one saved session for the history panel.
export interface SessionMeta {
  path: string;
  preview: string;
  title?: string; // user-chosen name; falls back to preview when empty
  turns: number;
  createdAt: number; // unix milliseconds
  lastActivityAt: number; // unix milliseconds
  modTime: number; // compatibility alias for lastActivityAt
  deletedAt?: number; // unix milliseconds, present for trashed sessions
  current: boolean;
  open: boolean;
  scope?: string;       // "project" | "global" | "expert"; empty for legacy → treated as "global"
  workspaceRoot?: string;
  topicId?: string;
  topicTitle?: string;
  profile?: string;
  isExpert?: boolean; // true = expert-team collaboration session (scope="expert")
  expertTeamId?: string; // identifies the expert team for expert sessions
  // IM bot session origin (empty for desktop-tab sessions). The sidebar IM-
  // contact detail groups bot sessions by platform + chatType + chatId + remoteId
  // so the same person's conversations in different groups stay separate.
  platform?: string;
  remoteId?: string;
  chatType?: string;
  chatId?: string;
  mode?: string; // "bot" for IM bot sessions
}

// SessionReference is a session selected via @ past:chats for context injection.
export interface SessionReference {
  path: string;
  title: string;
  preview?: string;
  turns?: number;
  createdAt?: number;
  lastActivityAt?: number;
}

export interface WorkspaceView {
  path: string;
  name: string;
  current: boolean;
}

export interface ContextInfo {
  used: number;
  window: number;
  sessionTokens: number;
  compactRatio?: number;
  // Session-cumulative telemetry for the usage chip's hover detail and the
  // right-dock turns tab (zero/absent when the backend hasn't aggregated any
  // turn yet). sessionElapsedMs is the sum of turn durations, not wall-clock
  // session age.
  sessionPromptTokens?: number;
  sessionCompletionTokens?: number;
  sessionReasoningTokens?: number;
  sessionCacheHitTokens?: number;
  sessionCacheMissTokens?: number;
  sessionCacheWriteTokens?: number;
  sessionCost?: number;
  sessionCostCurrency?: string;
  requestCount?: number;
  sessionElapsedMs?: number;
}

export interface Meta {
  label: string;
  ready: boolean;
  startupErr?: string;
  eventChannel: string;
  cwd: string;
  autoApproveTools?: boolean;
  bypass?: boolean; // legacy JSON key for YOLO/full-access tool auto-approval
  toolApprovalMode?: ToolApprovalMode;
  // Knowledge-base collection to auto-inject ("ragScope"); "" = 不使用 (default).
  ragScope?: string;
  goal?: string;
  goalStatus?: GoalStatus;
  expertSession?: ExpertSessionMeta;
}

export type CollaborationMode = "normal" | "plan" | "goal";
export type ToolApprovalMode = "ask" | "auto" | "yolo";
export type GoalStatus = "running" | "complete" | "blocked" | "stopped";

export function normalizeCollaborationMode(mode?: string, goal?: string, legacyMode?: Mode): CollaborationMode {
  if (mode === "plan" || mode === "goal" || mode === "normal") return mode;
  if (legacyMode && modeHasPlan(legacyMode)) return "plan";
  if ((goal ?? "").trim()) return "goal";
  return "normal";
}

export function normalizeToolApprovalMode(mode?: string, legacyMode?: Mode, legacyAutoApproveTools?: boolean): ToolApprovalMode {
  if (mode === "auto" || mode === "yolo" || mode === "ask") return mode;
  if (legacyAutoApproveTools || (legacyMode && modeHasAutoApproveTools(legacyMode))) return "yolo";
  return "ask";
}

// Mode is the compatibility string for two independent composer axes:
// plan (read-only/user-plan gate) and yolo/full access (tool auto-approval).
export type Mode = "normal" | "plan" | "yolo" | "plan-yolo";

export function normalizeMode(mode?: string): Mode {
  if (mode === "plan" || mode === "yolo" || mode === "plan-yolo" || mode === "yolo-plan") {
    return mode === "yolo-plan" ? "plan-yolo" : mode;
  }
  return "normal";
}

export function modeHasPlan(mode: Mode): boolean {
  return mode === "plan" || mode === "plan-yolo";
}

export function modeHasAutoApproveTools(mode: Mode): boolean {
  return mode === "yolo" || mode === "plan-yolo";
}

export function modeFromAxes(plan: boolean, autoApproveTools: boolean): Mode {
  if (plan && autoApproveTools) return "plan-yolo";
  if (plan) return "plan";
  if (autoApproveTools) return "yolo";
  return "normal";
}

export function modeWithPlan(mode: Mode, plan: boolean): Mode {
  return modeFromAxes(plan, modeHasAutoApproveTools(mode));
}

export function modeWithAutoApproveTools(mode: Mode, autoApproveTools: boolean): Mode {
  return modeFromAxes(modeHasPlan(mode), autoApproveTools);
}

export interface CommandInfo {
  name: string; // without the leading slash
  description: string;
  hint?: string;
  kind: "builtin" | "custom" | "mcp" | "skill";
}

export interface DirEntry {
  name: string;
  isDir: boolean;
}

export interface DroppedItem {
  kind: "workspace" | "attachment";
  path: string;
  isDir?: boolean;
  previewUrl?: string;
}

export interface FilePreview {
  path: string;
  body: string;
  size: number;
  truncated: boolean;
  binary: boolean;
  /** Media previews stream through an expiring token URL served by the kernel. */
  kind?: "image" | "pdf" | "audio" | "video" | "html";
  mime?: string;
  url?: string;
  err?: string;
}

export interface WorkspaceChangeView {
  path: string;
  oldPath?: string;
  sources: string[];
  gitStatus?: string;
  turns?: number[];
  latestPrompt?: string;
  latestTime?: number;
}

export interface WorkspaceChangesView {
  files: WorkspaceChangeView[];
  gitAvailable: boolean;
  gitErr?: string;
  gitBranch?: string;
  // Tracked-file line deltas (git diff --numstat HEAD) plus untracked count —
  // the changed-tab summary "+N -N ?N". Zero/absent when git or numstat fails.
  added?: number;
  removed?: number;
  untracked?: number;
}

export interface GitCommitView {
  hash: string;
  author: string;
  date: string;
  message: string;
}

export interface GitCommitDetailView {
  diff?: string;
  files?: string[];
}

export interface ComposerInsertRequest {
  id: number;
  text: string;
}

// MCP & Skills drawer (desktop/app.go Capabilities) — the GUI counterpart to
// /mcp + /skill: connected/failed servers and discoverable skills.
export interface ServerView {
  name: string;
  transport: string;
  status: "connected" | "deferred" | "failed" | "initializing" | "disabled";
  builtIn?: boolean;
  configured?: boolean;
  autoStart: boolean;
  tier?: "lazy" | "background" | "eager" | string;
  command?: string;
  args?: string[];
  url?: string;
  envKeys?: string[];
  tools: number;
  prompts: number;
  resources: number;
  error?: string;
  toolList?: MCPToolView[];
  authStatus?: "none" | "possible" | "required" | string;
  authUrl?: string;
  authConfigured?: boolean;
  /** Hidden by the ACTIVE tab's profile gates (netdev allowlist / named hidden). */
  profileHidden?: boolean;
}
export interface MCPToolView {
  name: string;
  description: string;
}
export interface SkillView {
  name: string;
  description: string;
  scope: string;
  runAs: string;
  enabled: boolean;
  /** In effect under the current product profile. A profile whitelist can hide a
   * skill the user left enabled — enabled=true, active=false. */
  active?: boolean;
  /**
   * InstalledFrom is the marketplace source URL when the skill was installed
   * via install_source (empty for builtins and manually created skills).
   */
  installedFrom?: string;
}

/** A skill catalog entry from the marketplace (browse/search results). */
export interface CatalogEntry {
  source: string;
  name: string;
  slug: string;
  author?: string;
  description: string;
  topics?: string[];
  installs: number;
  contentUrl: string;
  installRef: string;
}

/** A marketplace source's metadata (for the source list UI). */
export interface MarketSourceMeta {
  id: string;
  name: string;
  type: string;
}
export interface SkillRootSkillView {
  name: string;
  description: string;
  scope: string;
  runAs: string;
}
export interface SkillRootView {
  dir: string;
  scope: string;
  priority: number;
  status: string;
  configured: boolean;
  removable: boolean;
  skills: number;
  skillItems?: SkillRootSkillView[];
  warning?: string;
}
export interface CapabilitiesView {
  servers: ServerView[];
  skills: SkillView[];
  skillRoots: SkillRootView[];
}
export interface MCPServerInput {
  name: string;
  transport: string; // stdio | http | sse
  command: string;
  args: string[];
  url: string;
  env?: Record<string, string> | null;
}

// MCPRegistryEntry mirrors desktop.MCPRegistryEntryView — one official MCP
// Registry server, enough to render a card and assemble an MCPServerInput.
export interface MCPRegistryEntry {
  name: string;
  suggestedName: string;
  title?: string;
  description?: string;
  version?: string;
  repositoryUrl?: string;
  installable: boolean;
  unavailableReason?: string;
  transport?: string;
  command?: string;
  args: string[];
  url?: string;
}

// MCPRegistryView mirrors desktop.MCPRegistryView. cached/warning surface a
// registry outage so the UI can keep browsing from the on-disk cache.
export interface MCPRegistryView {
  servers: MCPRegistryEntry[];
  cached: boolean;
  warning?: string;
}

export interface ModelInfo {
  ref: string; // "provider/model" — pass to SetModel
  provider: string;
  model: string;
  current: boolean;
}

// Mobile bridge payloads (mobilebridge_app.go) — trimmed subsets mirroring the
// mobilebridge.ModelInfo / mobilebridge.SessionInfo classes generated in
// wailsjs/go/models.ts. Deliberately separate from ModelInfo/SessionMeta above:
// the mobile payload only carries id/label (models) and path/title (sessions).
export interface MobileModelInfo {
  id: string;
  label: string;
}
export interface MobileSessionInfo {
  path: string;
  title: string;
}

export interface EffortInfo {
  supported: boolean;
  current: string; // "auto" | "low" | "medium" | "high" | "xhigh" | "max"
  default: string;
  levels: string[];
}

// Product profile entry returned by Profiles() — drives the profile picker in
// the chrome. WorkspaceType is a frontend hint ("code" | "document") that
// selects the layout; the backend ignores it.
export interface ProfileInfo {
  name: string; // "dev" | "cowork" | …
  displayName: string;
  workspaceType?: string;
}

// --- Scheduled tasks (coWork automation panel) ------------------------------
// Mirror desktop/scheduler_app.go view structs. Time fields are pre-formatted
// "YYYY-MM-DD HH:MM" strings (empty when absent) so the UI renders directly
// without a date library.

// One task row in the automation panel. humanSchedule is a friendly Chinese
// rendering of expression (e.g. "工作日 09:00"); the UI may show both.
export interface TaskView {
  id: string;
  name: string;
  expression: string;
  prompt: string;
  profile: string;
  enabled: boolean;
  oneShot: boolean;
  lastRun: string;
  nextRun: string;
  runCount: number;
  lastResult: string;
  outputMode: string; // "" | "im" | "email" | "notify" | "file"
  outputDest: string;
  outputAccount: string; // named mailbox for "email" mode; "" = default
  outputDir: string;
  color?: string;
  location?: string;
  plain: boolean;        // 纯提醒：到点直接弹原文，不调 AI
  // Last delivery outcome. lastDeliverErr is empty when the last run delivered
  // successfully (or had no delivery configured); non-empty means the agent ran
  // but IM/email/file push failed — surfaced in the card so the user notices.
  lastDeliverErr: string;
  lastDeliverAt: string;
  humanSchedule: string;
  source: string;        // "manual" | "calendar"
  calendarEventId: string;
}

// Create/update payload from the UI. Empty id on create.
export interface TaskInput {
  id: string;
  name: string;
  expression: string;
  prompt: string;
  outputMode?: string;
  outputDest?: string;
  outputAccount?: string;
  outputDir?: string;
  color?: string;
  location?: string;
  plain?: boolean;       // 纯提醒：到点直接弹原文，不调 AI
}

// One run-history record (newest first when listed).
export interface RunRecordView {
  taskId: string;
  name: string;
  at: string;
  status: string; // "ok" | "error" | "skipped"
  result: string;
  outputMode: string;
}

// Predefined recipe in the "模板" menu.
export interface TemplateView {
  id: string;
  name: string;
  category: string; // "reminder" | "data" | "ops"
  desc: string;
  expression: string;
  prompt: string;
  outputMode: string;
  outputHint: string;
  oneShot: boolean;
}

// Live preview of an expression input. Kind is "oneshot" | "recurring" |
// "unknown"; absoluteTime is set for one-shot (the resolved instant) and empty
// for recurring (which has no single fire time).
export interface SchedulePreview {
  inputText: string;
  expression: string;
  absoluteTime: string;
  kind: string;
  note: string;
}

// --- Calendar (coWork calendar panel) ----------------------------------------
// Mirror desktop/calendar_app.go view structs.

export interface CalendarEventView {
  id: string;
  title: string;
  description: string;
  location: string;
  start: string;    // "2006-01-02T15:04"
  end: string;
  allDay: boolean;
  timezone: string;
  color: string;
  status: string;   // confirmed / cancelled / tentative
  source: string;   // manual / email / agent
  recurrence: string;
  recurrenceEnd: string;
  reminders: number[];
  taskId: string;
  tags: string[];
  // Reminder push routing (mirrors Go). Empty outputMode = toast only.
  outputMode: string;
  outputDest: string;
  outputAccount: string;
  createdAt: string;
}

export interface CalendarEventInput {
  id: string;
  title: string;
  description: string;
  location: string;
  start: string;
  end: string;
  allDay: boolean;
  timezone: string;
  color: string;
  recurrence: string;
  recurrenceEnd: string;
  reminders: number[];
  tags: string[];
  outputMode?: string;
  outputDest?: string;
  outputAccount?: string;
}

// --- RAG knowledge base (coWork RAG panel) ----------------------------------
// Mirror desktop/rag_app.go view structs. The tree is folder/file recursive;
// file nodes carry FTS5 + extraction status + progress.

// One node in the RAG file/folder tree.
export interface RagNodeView {
  key: string;
  label: string;
  kind: string; // "folder" | "file"
  path: string;
  relPath: string;
  isDir: boolean;
  collection: string;
  status: string; // "indexed" | "extracting" | "enriched" | "error" | "cancelled"
  hasFts5: boolean;
  jobId: string;
  doneChunks: number;
  totalChunks: number;
  entityCount: number;
  errorMsg: string;
  children?: RagNodeView[];
}

// One collection summary (for the dropdown).
export interface RagCollectionView {
  id: string;       // = path (full path, e.g. "工作/领导材料")
  name: string;     // display name (last path segment)
  path: string;     // full path
  parent: string;   // parent path (empty for root)
  documents: number;
  chunks: number;
  entities: number;
}

// Import result (immediate feedback: FTS5 ready, extraction queued).
export interface RagImportResult {
  jobIds: string[];
  files: number;
  ftsChunks: number;
  message: string;
}

// Extraction result summary.
export interface RagExtractResultView {
  entityCount: number;
  relationCount: number;
  topEntities: RagEntityBrief[];
  topRelations: RagRelationBrief[];
  jobCount: number;
  doneCount: number;
  hasData: boolean;
}

export interface RagEntityBrief {
  name: string;
  nameRaw: string;
  type: string;
  description: string;
  relationCount: number;
}

export interface RagRelationBrief {
  source: string;
  target: string;
  type: string;
  description: string;
}

// Combined search hits (entities/relations + FTS5 snippets).
export interface RagSearchHitView {
  entities: RagEntityView[];
  relations: RagRelView[];
  snippets: RagSnippetView[];
}
export interface RagEntityView {
  name: string;
  type: string;
  description: string;
}
export interface RagRelView {
  source: string;
  target: string;
  type: string;
  description: string;
}
export interface RagSnippetView {
  collection: string;
  path: string;
  chunk: number;
  snippet: string;
  score: number;
}

// On-demand ETA probe (for hover tooltip).
export interface RagETAView {
  jobId: string;
  doneChunks: number;
  totalChunks: number;
  avgLatencyMs: number;
  etaSeconds: number;
}

// Progress event payload from the pipeline (rag:progress).
export interface RagProgressEvent {
  jobId: string;
  collection: string;
  path: string;
  status: string;
  doneChunks: number;
  totalChunks: number;
  avgLatencyMs: number;
  message: string;
}

// --- Graph visualization types (mirrors desktop/rag_app.go) ------------------

export interface GraphNodeView {
  id: string;
  label: string;
  type: string;
  description: string;
  sources: Array<{ path: string; chunk: number }>;
  relationCnt: number;
  collection: string;
  community: number;
}

export interface GraphEdgeView {
  source: string;
  target: string;
  type: string;
  description: string;
  weight: number;
  strength: number;
}

export interface GraphDataView {
  nodes: GraphNodeView[];
  edges: GraphEdgeView[];
}

export interface EntityRelationView {
  direction: string; // "out" | "in"
  peer: string;
  type: string;
  description: string;
  weight: number;
  strength: number;
}

export interface EntityDetailView {
  name: string;
  nameRaw: string;
  type: string;
  description: string;
  sources: Array<{ path: string; chunk: number }>;
  relations: EntityRelationView[];
  community: number;
  relationCnt: number;
}

export interface EntityPatch {
  nameRaw: string;
  type: string;
  description: string;
}

export interface ChunkHighlight {
  index: number;
  start: number;
  end: number;
  content: string;
}

export interface DocPreviewView {
  path: string;
  content: string;
  chunks: ChunkHighlight[];
}

// --- Expert team (multi-model collaboration) --------------------------------
// Mirror desktop/experts_app.go view structs.

export interface ExpertView {
  name: string;
  model: string;       // "provider/model" ref, "" = use default
  perspective: string; // role instruction
}

export interface TeamView {
  id: string;
  name: string;
  experts: ExpertView[];
  defaultMode: string;     // "parallel" | "debate" | "pipeline"
  defaultRounds: number;   // debate rounds
  allowSearch: boolean;    // true = each expert runs a web_search mini-agent (slower, accurate for real-time data)
}

// CollabEvent is one streamed event during an expert-team run.
export interface CollabEvent {
  runId: string;
  teamId: string;
  teamName: string;
  phase: string; // "expert_start" | "expert_chunk" | "expert_done" | "synthesis" | "run_done" | "error"
  expertIdx: number;
  expertName: string;
  round: number;
  text: string;   // expert_chunk: delta; synthesis: delta
  message: string;
  mode: string;
}

// ExpertRunView is the queryable status of an in-flight expert-team run, used
// by ExpertPanel/ExpertSessionView to recover after the CoWorkLayout is torn
// down (tab/profile switch) mid-run. Mirrors desktop/expert_runs.go ExpertRunView.
export interface ExpertRunView {
  runId?: string;
  teamId?: string;
  teamName?: string;
  task?: string;
  mode?: string;
  status?: string;  // "" | "running" | "done" | "error"
  startedAt?: number;  // unix ms
  err?: string;
  // Cached live-stream messages accumulated on the backend while the tab was
  // hidden. When the user switches back, the frontend restores these so the
  // experts' progress that happened during the switch isn't lost.
  messages?: StreamMessageWire[];
}

// StreamMessageWire mirrors the Go StreamMessageWire type — the JSON form of
// one expert's (or synthesis's) accumulated text in the live stream.
export interface StreamMessageWire {
  kind: string;       // "expert" | "synthesis"
  expertName: string;
  round: number;
  text: string;
  streaming: boolean;
}

// Slash sub-command / argument completion (desktop/app.go SlashArgs). Mirrors the
// CLI's arg hints so the composer can suggest e.g. /skill → list/show/new/paths.
export interface SlashArgItem {
  label: string;
  insert: string; // token to place at the current position
  hint: string;
  descend: boolean; // re-open the menu one level deeper after accepting
}
export interface SlashArgsResult {
  items: SlashArgItem[];
  from: number; // byte offset where the current token begins
}

// Memory panel payloads (desktop/app.go MemoryView).
export interface MemoryDoc {
  path: string;
  scope: string; // "user" | "ancestor" | "project" | "local"
  body: string;
}

export interface MemoryFact {
  name: string;
  body: string;
  type: string; // "user" | "feedback" | "project" | "reference" (panel tag)
  // How far the fact reaches: "l1" global (user identity/preferences),
  // "l2" project (this project's conventions/decisions), "l3" session (working
  // memory for the current task — never injected into the prompt).
  level?: string;
  // v0.4: title/description were removed from the store struct; the index label
  // is now derived from the body's first line. Kept optional here so older
  // payloads and the timeline UI don't type-error — they just read as empty.
  title?: string;
  description?: string;
  // Bitemporal fields (v0.3.0). No longer populated by the slimmed store (the
  // bitemporal model was removed in v0.4); retained optional so the timeline
  // panel compiles. They will be undefined on new facts.
  validFrom?: string;
  validTo?: string;
  status?: string;
  category?: string;
  tags?: string[];
  supersededBy?: string;
  createdAt?: string; // RFC3339, system write time
  updatedAt?: string;
}

export interface MemoryScope {
  scope: string; // "user" | "project" | "local"
  path: string;
}

// ProfileView is the active mode's portrait (cowork.md under cowork, dev.md
// under dev). The workspace preference panel reads it on open and writes it
// back via SaveDoc (the path is whitelisted in the backend).
export interface ProfileView {
  path: string;
  content: string;
}

// ProfilePreset is one named preference template the cowork preference panel
// manages ("减少AI味", "严格Excel匹配", …). The backend injects the active
// preset's content into every turn, after the portrait files.
export interface ProfilePreset {
  id: string;
  name: string;
  content: string;
  builtin: boolean; // factory-seeded (still fully editable/deletable)
}

// ProfilePresetsPayload is the read/write payload of the preset list: which id
// is in use ("" = none) plus the items. Path is read-only display (the JSON
// file on disk); on save the backend ignores it.
export interface ProfilePresetsPayload {
  active: string;
  items: ProfilePreset[];
  path: string;
}

// MemorySettings is the [memory] section as the panel needs it (desktop/app.go
// MemorySettings): where the memory data tree actually lives, and which model
// maintains it in the background.
export interface MemorySettings {
  root: string; // effective data root (profile/, memory/, projects/ live here)
  defaultRoot: string; // what the root would be with no override
  configuredRoot: string; // raw [memory] root ("" = default in use)
  rootFromEnv: boolean; // $HIQ_MEMORY_ROOT is overriding the config
  globalDir: string; // L1 fact bucket
  sessionDir: string; // L3 fact bucket
  provider: string;
  model: string;
  effort: string;
  ref: string; // resolved maintenance model reference actually in use
  refFromMemory: boolean; // true = pinned in [memory], false = inherited
  injectIndex: boolean;
  indexMaxChars: number;
}

// MemorySettingsInput is the writable form of [memory] (desktop/app.go
// MemorySettingsInput). IndexMaxChars <= 0 means "built-in default", not "off":
// use injectIndex=false to switch the injected index off.
export interface MemorySettingsInput {
  root: string;
  provider: string;
  model: string;
  effort: string;
  injectIndex: boolean;
  indexMaxChars: number;
}

export interface MemoryView {
  docs: MemoryDoc[];
  facts: MemoryFact[];
  scopes: MemoryScope[];
  storeDir: string;
  available: boolean;
  settings?: MemorySettings;
}

// MemoryMigrationReport is the result of copying the memory tree to a new root
// (desktop/app.go MigrateMemoryRoot). Copy-only: the source tree is left intact.
export interface MemoryMigrationReport {
  from: string;
  to: string;
  copied: string[];
  skipped: string[];
  files: number;
  bytes: number;
}

// DataDirSaveResult is SaveDataDir's outcome (desktop/app.go SaveDataDir): the
// refreshed [memory] view plus the migration report when a copy was requested.
export interface DataDirSaveResult {
  settings: MemorySettings;
  migration?: MemoryMigrationReport;
}

// MemoryPromotionInput selects saved memories and describes the artifact to
// build from them (desktop/app.go PromoteMemoryArtifact). Selection mirrors the
// `recall` tool: explicit names, or a level/tag/query filter.
export interface MemoryPromotionInput {
  kind: "skill" | "plugin";
  name: string;
  description: string;
  memories?: string[];
  level?: string;
  tag?: string;
  query?: string;
  notes?: string;
  version?: string;
  overwrite?: boolean;
  // install also copies the generated playbook into a skill root the agent
  // already scans, making the promoted capability live without a kernel change.
  install?: boolean;
  scope?: "global" | "project";
}

export interface PromotedSource {
  name: string;
  level: string;
  tags?: string[];
  hook?: string;
}

export interface MemoryPromotionResult {
  kind: string;
  name: string;
  dir: string;
  skill: string;
  manifest?: string;
  readme?: string;
  refs: number;
  sources: PromotedSource[];
  version: string;
  installed?: string;
}

// Dream / Distill self-evolution payloads (desktop/app.go DreamStatusView).
export interface DreamRunView {
  kind: string; // "dream" | "distill"
  trigger: string; // "auto" | "manual"
  startedAt: string; // RFC3339
  duration?: string;
  status: string; // "ok" | "error" | "timeout"
  error?: string;
}

export interface DreamStatusView {
  enabled: boolean;
  dreamInterval: number;
  distillInterval: number;
  dreamInFlight: boolean;
  distillInFlight: boolean;
  lastDream?: DreamRunView;
  lastDistill?: DreamRunView;
  history: DreamRunView[];
}

// SettingsTab is the top-level navigation item in the Settings Centre modal.
export type SettingsTab = "general" | "models" | "providers" | "bots" | "cowork" | "preference" | "mcp" | "skills" | "memory" | "permissions" | "sandbox" | "network" | "hooks" | "appearance" | "updates" | "mobile" | "netdev" | "trustdomain";

// Settings panel payloads (desktop/settings_app.go).
export interface ProviderView {
  name: string;
  builtIn: boolean;
  added: boolean;
  kind: string;
  baseUrl: string;
  models: string[];
  modelsUrl: string; // optional override for model discovery; empty derives from baseUrl
  default: string;
  apiKeyEnv: string;
  keySet: boolean; // the env var currently resolves to a value
  contextWindow: number;
  reasoningProtocol: string; // auto|openai|minimax|none; empty = auto (endpoint auto-detect)
  supportedEfforts: string[]; // custom /effort levels; empty = use built-in Kind/BaseURL default
  defaultEffort: string; // /effort level when user picks "auto" or unset; "" = supportedEfforts[0]
  // models.dev-flagged reasoning-capable models among `models` — display-only
  // badge in pickers (MODEL_ROUTING_SPEC §5); the behaviour layer never reads it.
  reasoningModels?: string[];
}

// ProviderTemplate is a built-in vendor preset for the onboarding wizard and
// the "add provider" picker. Mirrors desktop/provider_templates.go ProviderTemplate.
export interface ProviderTemplate {
  name: string;
  displayName: string;
  kind: string; // "openai" | "anthropic"
  baseUrl: string;
  apiKeyEnv: string;
  defaultModel: string; // recommended default (vendor-relative, no provider prefix)
  fastModel: string; // recommended fast model
  visionModel: string; // recommended vision model ("" = same as default)
  vision: boolean; // provider supports image input
  contextWindow: number;
  local: boolean; // keyless local endpoint (Ollama, llama.cpp) — no API key step, models fetched live
  codingOnly: boolean; // consumes Coding Plan subscription quota
  aggregator: boolean; // model-aggregation platform
  category: string; // "direct" | "aggregator" | "local"
  docUrl: string; // where to get an API key
  models: string[]; // preset model list (fallback when probe fails)
  reasoningModels?: string[]; // models.dev-flagged reasoning-capable model IDs (display-only)
}

// RegistryStatus reports the provider-template registry freshness for Settings.
export interface RegistryStatus {
  updatedAt: string; // RFC3339; "" = never (using embed snapshot)
  source: string; // "cache" | "embed"
}

// JobView is one running background job (desktop/app.go Jobs) for the status bar.
export interface JobView {
  id: string;
  kind: string; // "bash" | "task"
  label: string;
  status: string; // "running"
  startedAt: number; // unix milliseconds
}

export interface PermissionsView {
  mode: string; // "ask" | "allow" | "deny"
  allow: string[];
  ask: string[];
  deny: string[];
}

export interface SandboxView {
  bash: string; // "enforce" | "off"
  network: boolean;
  workspaceRoot: string;
  allowWrite: string[];
}

export interface NetworkProxyView {
  type: string;
  server: string;
  port: number;
  username: string;
  password: string;
}

export interface NetworkView {
  proxyMode: string; // "auto" | "custom" | "off" (backend may still return legacy "env")
  proxyUrl: string;
  noProxy: string;
  proxy: NetworkProxyView;
}

export interface AgentView {
  temperature: number;
  maxSteps: number;
  /** @deprecated retained for Wails binding parity; the backend ignores it. */
  plannerMaxSteps?: number;
  systemPrompt: string;
  rpm: number; // max requests/minute; 0 = unlimited
}

export interface BotAllowlistView {
  enabled: boolean;
  allowAll: boolean;
  mode: string; // "open" | "review"
  qqUsers: string[];
  feishuUsers: string[];
  weixinUsers: string[];
  telegramUsers: string[];
  qqGroups: string[];
  feishuGroups: string[];
  weixinGroups: string[];
  telegramGroups: string[];
}

export interface QQBotView {
  enabled: boolean;
  appId: string;
  appSecretEnv: string;
  secretSet: boolean;
}

export interface FeishuBotView {
  enabled: boolean;
  domain: string;
  appId: string;
  appSecretEnv: string;
  secretSet: boolean;
  verificationToken: string;
  mode: string;
  webhookPort: number;
  requireMention: boolean;
}

export interface WeixinBotView {
  enabled: boolean;
  accountId: string;
  tokenEnv: string;
  tokenSet: boolean;
  apiBase: string;
}

export interface TelegramBotView {
  enabled: boolean;
  tokenEnv: string;
  tokenSet: boolean;
  apiBase: string;
}

export interface BotConnectionCredentialView {
  appId: string;
  appSecretEnv: string;
  accountId: string;
  tokenEnv: string;
  secretSet: boolean;
}

export interface BotConnectionSessionMappingView {
  remoteId: string;
  // chatType/chatId are populated by the backend once a conversation happens
  // (they identify which group/DM the mapping is for). Optional because the
  // settings form constructs these literals before any conversation exists.
  chatType?: string;
  chatId?: string;
  sessionId: string;
  scope: "global" | "project" | string;
  workspaceRoot: string;
  updatedAt: string;
}

export interface BotConnectionView {
  id: string;
  provider: "qq" | "feishu" | "weixin" | "telegram" | string;
  domain: "qq" | "feishu" | "lark" | "weixin" | "telegram" | string;
  label: string;
  enabled: boolean;
  status: "disconnected" | "pending" | "connected" | "error" | string;
  model: string;
  workspaceRoot: string;
  credential: BotConnectionCredentialView;
  sessionMappings: BotConnectionSessionMappingView[];
  lastError: string;
  createdAt: string;
  updatedAt: string;
}

// HookConfigView mirrors the Go HookConfigView (one hook, flat — carries its
// event). command is required; match is a regex for PreToolUse/PostToolUse only.
export interface HookConfigView {
  event: string;
  match?: string;
  command: string;
  description?: string;
  timeout?: number;
  cwd?: string;
}

// HooksSettingsView mirrors the Go HooksSettingsView — the hooks panel payload.
// scope is "global" | "project"; path is the settings.json being edited;
// projectRoot/trusted apply to the project scope (project hooks load only when
// trusted); events is the valid event list (drives the JSON editor validation).
export interface HooksSettingsView {
  scope: string;
  path: string;
  projectRoot: string;
  trusted: boolean;
  hooks: HookConfigView[];
  events: string[];
}

export interface BotSettingsView {
  enabled: boolean;
  model: string;
  maxSteps: number;
  debounceMs: number;
  allowlist: BotAllowlistView;
  qq: QQBotView;
  feishu: FeishuBotView;
  weixin: WeixinBotView;
  telegram: TelegramBotView;
  connections: BotConnectionView[];
}

// CoWorkSettingsView mirrors the Go CoWorkSettingsView. Secrets (SMTP/IMAP
// passwords) are presented as plain fields here; they're persisted to a
// hiq-managed .env (not config.toml). detectedBrowser is a read-only
// diagnostic from CheckCoworkBrowser. pptTemplates/pptActiveTemplate drive the
// PPT-template dropdown; pptTemplateDir is where the user drops JSON templates.
export interface CoWorkSettingsView {
  browserPath: string;
  // browserAttachURL makes browser automation attach to an already-running
  // debug-enabled browser (the managed 可控浏览器 on port 9222) instead of
  // launching a fresh one per task. Empty = launch mode.
  browserAttachURL: string;
  embeddingModel: string;
  // Knowledge-base master switch. null = unset (default → enabled); true =
  // enabled; false = fully disabled. Mirrors [cowork] rag_enabled. Distinct
  // from embeddingModel (which only toggles semantic reranking on top of FTS5).
  ragEnabled: boolean | null;
  // PPT template selection. pptTemplates lists available templates (id+name)
  // read from the templates dir; pptActiveTemplate is the selected id ("=" none).
  pptActiveTemplate: string;
  pptTemplates: PPTTemplateView[];
  pptTemplateDir: string;
  pptMode: string;
  smtp: SMTPSettings;
  imap: IMAPSettings;
  smtpPassword: string;
  imapPassword: string;
  // True when an encrypted secret is stored for the password_env — the panel
  // shows "已设置" without ever holding the value (the field above is write-only).
  smtpPasswordSet: boolean;
  imapPasswordSet: boolean;
  detectedBrowser: string;
  // Screenshot hotkey → VLM feature (off by default; user opts in).
  screenshotEnabled: boolean;
  screenshotHotkey: string;
  screenshotVlmModel: string;
  screenshotPrompt: string;
  /** STT model ref (provider/model) for voice input; "" = disabled. */
  voiceModel: string;
  // Emergency-stop hotkey for desktop automation (always on by default; set
  // "off" to disable). Cancels the in-flight turn globally.
  estopHotkey: string;
  // Multi-mailbox list. When non-empty it's the source of truth on save (full
  // overwrite); the legacy smtp/imap single-pair fields mirror the Default
  // account for backward compat.
  emailAccounts: EmailAccountView[];
  // When true, email_send is added to permissions.Allow so scheduled tasks can
  // send email in headless mode (no interactive user to approve).
  allowHeadlessEmail: boolean;
}

// EmailAccountView is one mailbox in the multi-account list. password is
// write-only (a freshly-typed value on save; the stored secret is never echoed
// back). passwordSet reports whether a secret is stored.
export interface EmailAccountView {
  name: string;
  default: boolean;
  smtp: SMTPSettings;
  imap: IMAPSettings;
  password: string;
  passwordSet: boolean;
}

// PPTTemplateView is a trimmed PPT template (id + name) for the settings
// dropdown. The full template (master file + theme + layout coordinates) is
// loaded by the backend and injected into the ppt-auto skill context.
export interface PPTTemplateView {
  id: string;
  name: string;
}

export interface SMTPSettings {
  host: string;
  port: number;
  from: string;
  username: string;
  passwordEnv: string;
  useTLS: boolean;
  encryptionMode?: string; // "tls" | "starttls" | "none"; empty → migrate from useTLS
}

export interface IMAPSettings {
  host: string;
  port: number;
  username: string;
  passwordEnv: string;
}

export interface WebSearchView {
  braveKeySet: boolean;
  exaKeySet: boolean;
  linkupKeySet: boolean;
  anysearchKeySet: boolean;
}

// PatrolView is the proactive-inspection heartbeat (internal/patrol): the
// permission dial the user controls plus live status. `running`/`passes`/`fired`
// are runtime-only and reset when the app restarts.
export interface PatrolView {
  enabled: boolean;
  /** "off" | "readonly" | "assist" */
  mode: string;
  /** Only meaningful under assist: may an assist turn modify files? */
  allowWrite: boolean;
  intervalSec: number;
  /** Inspector names in use: "git", "markers". */
  checks: string[];
  running: boolean;
  passes: number;
  fired: number;
}

export interface BotInstallStartResult {
  ok: boolean;
  provider: string;
  domain: string;
  installId: string;
  url: string;
  deviceCode: string;
  userCode: string;
  interval: number;
  expireIn: number;
  message: string;
}

export interface BotInstallPollResult {
  done: boolean;
  connection: BotConnectionView;
  status: string;
  message: string;
  error: string;
}

// RecentChatView is one recently-seen IM chat, offered as a push destination in
// the task form's IM-target picker. userName is the best available display name
// (private chat = user name; group may be empty when the platform doesn't
// expose group names).
export interface RecentChatView {
  platform: string;
  chatType: string;
  chatId: string;
  userName: string;
  lastSeen: number;
}

// BotDockStatusView is the lightweight bot status shown in the dock Today panel:
// online (gateway running), connected platforms, and recent chat count.
export interface BotDockStatusView {
  online: boolean;
  platforms: string[];
  recentCount: number;
}

// WallpaperView is the desktop custom-background contract (wallpaper_app.go).
// `url` is empty when no wallpaper is active; when present it always carries a
// version query so replacing an image is never served from a stale cache.
export interface WallpaperView {
  active: boolean;
  url: string;
  name: string;
  blur: number;
  dim: number;
  fit: string;
}

export interface BotConnectionDiagnostic {
  id: string;
  label: string;
  status: string;
  message: string;
  messageId: string;
}

// MailProbeResult is the outcome of a mailbox IMAP connection probe, returned
// by ProbeMailAccount so the mail card can show a green/red status dot after
// the user saves the mailbox config. status: "ok" | "error" | "unconfigured".
export interface MailProbeResult {
  ok: boolean;
  status: string;
  message: string;
}

// ManagedBrowserStatus reports the state of the 可控浏览器 — the persistent
// attachable browser window (fixed CDP port 9222 + dedicated profile) that
// browser automation can attach to via CoWorkSettingsView.browserAttachURL.
export interface ManagedBrowserStatus {
  running: boolean;
  // Attach URL to save into browserAttachURL (e.g. "http://127.0.0.1:9222").
  url: string;
  browser: string;
  profile: string;
  alreadyRunning: boolean;
  detail?: string;
}

// InboxItem is one row in the cowork dock's "邮件" tab: a trimmed-down mail
// envelope (from/subject/date + a short body preview). Returned by
// InboxPreview; an empty array means either no mailbox configured or no unread
// mail — the dock distinguishes via ProbeMailAccount's status.
export interface InboxItem {
  from: string;
  subject: string;
  date: string;
  preview: string;
}

export interface SettingsView {
  defaultModel: string;
  fastTaskModel: string;
  subagentModel: string;
  subagentEffort: string;
  autoPlan: string;
  providers: ProviderView[];
  officialProviders: ProviderView[];
  permissions: PermissionsView;
  sandbox: SandboxView;
  network: NetworkView;
  agent: AgentView;
  bot: BotSettingsView;
  cowork: CoWorkSettingsView;
  webSearch: WebSearchView;
  patrol: PatrolView;
  desktopLanguage: string; // "" | "en" | "zh"; empty = auto
  desktopTheme: string; // "auto" | "dark" | "light"
  desktopThemeStyle: string;
  closeBehavior: string; // "background" | "quit"
  displayMode: string;   // "standard" | "compact" | "minimal"
  checkUpdates: boolean; // check for new versions on startup
  telemetry: boolean; // anonymous launch ping (install id + version + OS)
  expandThinking: boolean; // show reasoning text expanded by default
  configPath: string;
  providerKinds: string[]; // provider implementations the kernel registered (for the kind picker)
  autoApproveTools: boolean;
  bypass: boolean; // legacy JSON key for live YOLO/full-access tool auto-approval
  secretStore?: SecretStoreStatus; // at-rest encryption backend of the credential store
}

// Mirrors desktop.SecretStoreView (secret.Store.SecurityMode). backend is one
// of dpapi/keychain/secret-service/passphrase/machine, or "unavailable" when an
// existing store's KEK cannot be reached (keystore locked/reset). degraded
// marks the machine-bound fallback whose key any local process can recompute.
export interface SecretStoreStatus {
  backend: string;
  degraded: boolean;
}

// Auto-updater payloads (desktop/updater.go). UpdateInfo drives the update banner;
// UpdateProgress streams on the "updater:progress" event during ApplyUpdate.
export interface UpdateInfo {
  available: boolean;
  current: string;
  latest: string;
  notes: string;
  canSelfUpdate: boolean; // win/linux true; macOS false (no cert → manual download)
  downloadUrl: string; // human-facing releases page (macOS path / fallback link)
  assetSize: number; // running platform's artifact size, for the progress bar
  err?: string; // set when the check itself failed (both endpoints down)
}

export interface UpdateProgress {
  phase: "downloading" | "verifying" | "applying" | "done" | "error";
  received: number;
  total: number;
  err?: string;
}

// BudgetStatusView describes the rate-limit budget window for live UI display
// (rpm/used/remaining). Mirrors desktop.BudgetStatusView on the Go side.
export interface BudgetStatusView {
  rpm: number;
  used: number;
  remaining: number;
  reserveMain: number;
  windowSecs: number;
}

// MergeCandidate represents one entity in the graph deduplication merge flow:
// the candidate to merge away (name/raw/score), the keep target (keepName/keepRaw),
// and the proposed merged result (mergeName/mergeRaw) for review before applying.
export interface MergeCandidate {
  name: string;
  raw?: string;
  score?: number;
  keepName?: string;
  keepRaw?: string;
  mergeName?: string;
  mergeRaw?: string;
}

// ── netdev（运维）───────────────────────────────────────────────────────────
// NetDevSettingsView mirrors the Go NetDevSettingsView: the pinned USER-config
// inventory plus write-only password fields (blank = keep the stored secret).
export interface NetDevDeviceView {
  name: string;
  vendor: string;
  os: string;
  model: string;
  address: string;
  port: number;
  via: string[];
  group: string;
  username: string;
  passwordEnv: string;
  passwordSet: boolean;
  identityFile: string;
  // Data-plane discriminator (NETDEV_SPEC_V2 §2.1): ""(按厂商) | docker | k8s.
  kind?: string;
  dockerSocket?: string;
  k8sKubeconfigEnv?: string;
  k8sKubeconfigSet?: boolean;
  k8sKubeconfig?: string;
  k8sContext?: string;
  k8sNamespaces?: string[];
  fwApiTokenEnv?: string;
  fwApiTokenSet?: boolean;
  fwApiToken?: string;
  encoding: string;
  // Extra log-directory whitelist roots (outside /var/log) for log-source reads.
  logPaths: string[];
  // Server config-file whitelist roots (§7.3): snapshot/diff/drift + restore-verify.
  configPaths?: string[];
  // 带外启动器 deep link (§6.3): ESXi/堡垒/BMC Web UI.
  oobUrl?: string;
  // Dial-priority order (ssh, netconf).
  protocols?: string[];
  // SNMP collector credentials (community write-only, blank = keep).
  snmpVersion?: string;
  snmpCommunityEnv?: string;
  snmpCommunitySet?: boolean;
  snmpCommunity?: string;
  password?: string;
}

export interface NetDevHopView {
  name: string;
  host: string;
  port: number;
  user: string;
  passwordEnv: string;
  passwordSet: boolean;
  proxyJump: string;
  password?: string;
}

export interface NetDevSettingsView {
  enabled: boolean;
  networkName: string;
  devices: NetDevDeviceView[];
  hops: NetDevHopView[];
  groups: string[];
  auditRetention: string;
  scopes: string[];
  // [netdev.guardrails] — per-ask / per-tool-call controls.
  guardConfirmEach: boolean;
  guardTurnBudget: number;
  guardAllowedGroups: string[];
  // Read-table extensions (vendor → commands) — the user-owned knowledge
  // growth path for the classifier.
  extraRead: Record<string, string[]>;
  // Site-level scopes for the title-bar project switcher.
  projects: NetDevProjectView[];
  // Scheduled sweeps ("1h"/"24h"; empty = off).
  inspectionInterval: string;
  scheduledBaseline: boolean;
  backupInterval: string;
  // Named diagnostic batteries for the device card.
  presets: NetDevPresetView[];
  // Read-only database diagnostic endpoints (netdev_db_query).
  dbSources: NetDevDBSourceView[];
  // SNMP health sweep cadence (seconds; 0 = off).
  pollIntervalSeconds: number;
  // Health threshold rules → auto-Findings.
  alertRules: NetDevAlertRuleView[];
  // Passive syslog UDP receiver port (0 = off).
  syslogPort: number;
  // P3 gap closure: previously TOML-only fields.
  defaultMode: string;
  maxSessionsPerDevice: number;
  discoveryRate: number;
  discoveryMode: string;
  probeFallback: string;
  groupDefs: NetDevGroupDefView[];
  // 通知出口（§5.2）：webhook / SMTP / IM 直推，任选组合。
  notifyWebhook: string;
  notifyFormat: string;      // generic | feishu | dingtalk | wecom
  notifyMinSeverity: string; // info | warning | critical
  notifyBotDest: string;     // e.g. feishu:oc_xxx；空=关
  notifySMTPHost: string;
  notifySMTPPort: number;
  notifySMTPUser: string;
  notifySMTPFrom: string;
  notifySMTPTo: string[];
  notifySMTPPassSet: boolean;
  notifySMTPPassword?: string; // write-only
  // Daily briefing push time (local HH:MM; empty = off).
  briefingPushTime: string;
  weakCredDict: string;
}

// One group's policy + maintenance window.
export interface NetDevGroupDefView {
  name: string;
  policy: string;       // "" | read-only | proposal | proposal+confirm2
  changeWindow: string; // e.g. "tue,thu 22:00-24:00"; "" = any time
}

// One [[netdev.alert_rules]] entry.
export interface NetDevAlertRuleView {
  name: string;
  metric: string; // reachable | if_down_count | uptime_reset
  op: string;     // >= | <= | ==
  value: number;
  severity: string; // info | warning | critical
  enabled: boolean;
}

// Passive syslog receiver state.
export interface NetDevSyslogStatusView {
  listening: boolean;
  port: number;
  buffered: number;
}

// One health-poll rollup (metrics.db history, newest first).
export interface NetDevMetricPoint {
  time: string;
  up: boolean;
  us: number;
  iu: number;
  id: number;
  cu?: number;
  me?: number;
  io?: number;
  oo?: number;
}

// Golden Config baseline state (the 备份时间线 header).
export interface NetDevGoldenInfo {
  set: boolean;
  at: string;
  lines: number;
}

// Audit hash-chain verification verdict (the 审计 tab badge).
export interface NetDevAuditChainStatus {
  total: number;
  chained: number;
  ok: boolean;
  firstBroken?: string;
}

// ── 状态历史（三层回退之"状态回退"）──────────────────────────────────────────
// One timeline entry: a pre-mutation snapshot event over the ops state files
// (proposals/jobs/cutovers/templates/findings/inventory TOML…).
export interface NetDevStateEventView {
  id: number;
  time: number; // unix ms
  kind: string; // approve | execute | job-start | cutover-hold | settings-save | restore-keep | …
  entity?: string;
  actor: string; // user | agent | im | system
  paths: string[];
  live: NetDevStateLiveEntity[]; // non-empty = restoring would fight a live entity
  canRestore: boolean;
  canRedo: boolean;
}

// An entity the candidate suffix touches that is currently mid-flight.
export interface NetDevStateLiveEntity {
  type: string; // proposal | job | cutover
  id: string;
  status: string;
}

// One file's previewed restore change (unified diff).
export interface NetDevStateFileDiff {
  path: string;
  kind: string; // create | modify | delete | binary
  added: number;
  removed: number;
  diff: string;
}

// What a restore wrote back/deleted + the reverse event enabling redo.
export interface NetDevStateRestoreResultView {
  written: string[];
  deleted: string[];
  reverseEventId: number;
}

// One [[netdev.db_sources]] entry; password is write-only (blank = keep).
export interface NetDevDBSourceView {
  name: string;
  type: string; // mysql | postgres | redis | mongodb | mssql | clickhouse | elasticsearch
  host: string;
  port: number;
  username: string;
  passwordEnv: string;
  passwordSet: boolean;
  database: string;
  allowlist: string[];
  password?: string;
  via?: string[];
}

// One sealed log read (netdev_log_read / App.NetDevLogRead).
export interface NetDevExecResult {
  device: string;
  command: string;
  class: string;
  output: string;
  is_error: boolean;
  refused?: boolean;
  refusal?: string;
}

// One streaming follow event ("netdev:logfollow" channel).
export interface NetDevLogFollowEvent {
  device: string;
  source: string;
  chunk?: string;
  done?: boolean;
  reason?: string;
}

// Cross-device fan-out search (netdev_log_search / App.NetDevLogSearch) — the
// IOC sweep. Coverage is explicit: a budget-stopped sweep never implies the
// uncovered devices are clean.
export interface NetDevLogSearchHit {
  device: string;
  source: string;
  line: string;
  context?: string[];
}

export interface NetDevLogSearchResult {
  pattern: string;
  hits: NetDevLogSearchHit[];
  devices_searched: string[];
  skipped: string[];
  covered_devices: number;
  total_devices: number;
  devices_with_hits: number;
  budget_stopped: boolean;
  note?: string;
}

// 告警队列聚合视图（App.NetDevAggregatedFindings / §4.10）。
export interface NetDevAggregatedFinding {
  key: string;
  count: number;
  open: number;
  severity: string;
  devices: string[];
  title: string;
  newest: string;
  members?: NetDevFinding[];
  suppressed: number;
}

// 案例（安全工作台 / §4.6）。
export interface NetDevCaseIOC {
  value: string;
  type: string;
  note?: string;
  added_at: string;
}

export interface NetDevCaseEntry {
  time: string;
  kind: string; // finding | log | audit | triage | note
  device?: string;
  text: string;
  ref?: string;
}

export interface NetDevIncidentCase {
  id: string;
  title: string;
  status: string;
  devices?: string[];
  entries?: NetDevCaseEntry[];
  iocs?: NetDevCaseIOC[];
  created_at: string;
  updated_at: string;
}

// 时间关联层（App.NetDevTimeline / §5.4）：变更/发现/事件统一事件流。
export interface NetDevTimelineEvent {
  time: string;
  kind: string; // change | finding | event
  device: string;
  title: string;
  detail?: string;
}

// 期望状态对比（App.NetDevExpectedState / §5.4）。
export interface NetDevExpectedStateView {
  total: number;
  reachable: number;
  missing: NetDevDeviceHealth[];
  noProbe: string[];
}

// Timeline point (App.NetDevSeries, NETDEV_SPEC_V2 §5.3).
export interface NetDevSeriesPoint {
  t: number; d: string; m: string; v: number;
}

// IP/MAC locate fan-out (netdev_locate / App.NetDevLocate, NETDEV_SPEC_V2 §4.11).
export interface NetDevLocateHit {
  device: string;
  interface?: string;
  line: string;
}

export interface NetDevLocateResult {
  target: string;
  hits: NetDevLocateHit[];
  searched: string[];
  skipped?: string[];
  covered_devices: number;
  total_devices: number;
  budget_stopped: boolean;
  note?: string;
}

// One-click host triage battery (netdev_triage / App.NetDevTriageRun).
export interface NetDevTriageSection {
  name: string;
  command: string;
  ok: boolean;
  refused?: string;
  lines?: string[];
}

export interface NetDevTriageReport {
  device: string;
  vendor: string;
  sections: NetDevTriageSection[];
  anomalies?: string[];
  summary: string;
  created_at: string;
}

// ── SNMP 健康快照 ("netdev:health" changes) ─────────────────────────────────
export interface NetDevIfHealth {
  name: string;
  adminUp: boolean;
  operUp: boolean;
}

export interface NetDevDeviceHealth {
  device: string;
  time: string;
  reachable: boolean;
  uptimeSec: number;
  interfaces: NetDevIfHealth[];
  lastError?: string;
  cpuPct?: number;
  memPct?: number;
  inOct?: number;
  outOct?: number;
}

export interface NetDevHealthSnapshot {
  pollIntervalSeconds: number;
  devices: NetDevDeviceHealth[];
}

export interface NetDevProjectView {
  name: string;
  groups: string[];
  note: string;
}

export interface NetDevPresetView {
  name: string;
  commands: string[];
  vendors: string[];
}

export interface NetDevBackupVersion {
  id: string;
  device: string;
  at: string;
  bytes: number;
  lines: number;
}

export interface NetDevAuditEntryView {
  time: string;
  device: string;
  command: string;
  class: string;
  status: string;
  error?: string;
}

// ── 操作实况 (live ops panel) ────────────────────────────────────────────────

export interface NetDevLiveEvent {
  kind: "conn" | "cmd_start" | "cmd_output" | "cmd_end" | "cmd_refused" | "turn";
  device: string;
  time: number; // unix millis
  state?: string; // conn: connected | connecting | reconnecting | stopped | idle-closed
  vtyUse?: number;
  vtyCap?: number;
  command?: string;
  class?: string; // read | write | dangerous | unknown | guardrail
  chunk?: string; // cmd_output: cleaned + redacted incremental text
  status?: string; // cmd_end: ok | device-error | failure
  ms?: number;
  bytes?: number;
  reason?: string;
}

// ── 浏览器镜像 (browser mirror panel) ────────────────────────────────────────

// One update from the kernel's browser-panel sink ("browser:mirror" Wails
// event; desktop/app.go forwards internal/tool/builtin.BrowserPanelFrame).
export interface BrowserMirrorFrame {
  kind: "frame" | "status";
  source: "tool" | "auto"; // chromedp tools | browser-use sidecar
  phase?: "start" | "end" | "step"; // status only
  text?: string;
  url?: string;
  image?: string; // data URL (frame only)
  session_id?: string; // the browser session that produced this frame
}

// ── 浏览器控制台 (ops browser console) ───────────────────────────────────────

export interface BrowserConsoleState {
  open: boolean;
  session_id: string;
  browser: string;
  attached: boolean;
  url: string;
  // 会话保活：长间隔任务下既防内核空闲回收、也用页面心跳/定时刷新
  // 滑动站点会话有效期。last = 最近一次成功刷新（unix 毫秒）。
  keep_alive: boolean;
  keep_alive_mode: string; // ping|navigate|local
  keep_alive_url: string;
  keep_alive_last: number;
  keep_alive_err: string;
}

export interface BrowserConsoleTab {
  index: number; // 1-based
  title: string;
  url: string;
  current: boolean;
}

export interface BrowserConsoleElement {
  ref: string;
  role: string;
  name: string;
  value?: string;
}

export interface BrowserConsoleRecordEvent {
  type: string; // click|input|change|submit|navigate|effect
  selector?: string;
  role?: string;
  name?: string;
  value?: string;
  url?: string;
  time: number;
  effective?: boolean;
  password?: boolean;
}

export interface BrowserConsoleTraceFilter {
  kept: BrowserConsoleRecordEvent[];
  dropped: BrowserConsoleRecordEvent[];
}

export interface BrowserSkillDraft {
  name: string;
  content: string;
  fallback: boolean;
  detail?: string;
}

export interface BrowserConsoleSkill {
  name: string;
  description: string;
  browser: boolean;
}

// Editor step vocabulary (values carry {{参数}} refs; the trial runner
// substitutes them at run time, ask replies included). "human" is the 人工断点：
// 重放时在此暂停，等人在浏览器里完成短信验证码/登录/扫码等操作，人工确认继续或
// 满足自动检测条件后放行。"ask" 是运行时询问：暂停等用户在面板里输入回复，
// 回复绑定到参数名（目标列），后续步骤用 {{参数名}} 引用。
export type BrowserConsoleStepType =
  | "navigate"
  | "back"
  | "forward"
  | "click"
  | "hover"
  | "type"
  | "key"
  | "scroll"
  | "select"
  | "upload"
  | "wait"
  | "extract"
  | "screenshot"
  | "evaluate"
  | "human"
  | "ask";

export interface BrowserConsoleStep {
  type: BrowserConsoleStepType;
  target?: string;
  url?: string;
  text?: string;
  value?: string;
  direction?: string;
  amount?: number;
  condition?: string;
  timeout_sec?: number;
  files?: string[];
  expression?: string;
  label?: string;
}

export interface BrowserConsoleTrialStatus {
  index: number; // -1 = run terminal event
  status: "running" | "waiting" | "done" | "failed";
  output?: string;
  error?: string;
  // ask 步骤的等待：横幅带输入框，回复经 TrialResume(reply) 送回并绑定到 bind 参数。
  await_reply?: boolean;
  bind?: string;
}

export interface NetDevLiveDeviceState {
  device: string;
  vendor: string;
  os?: string;
  group?: string;
  connected: boolean;
  vtyUse: number;
  vtyCap: number;
}

export interface NetDevLiveSnapshot {
  devices: NetDevLiveDeviceState[];
  spent: number; // commands spent this turn
  budget: number; // turn_command_budget (0 = unlimited)
}

// Assessment-mode weak-credential check result (netdev_assess /
// NetDevWeakCredCheck; NETDEV_SPEC §6.2).
export interface NetDevWeakCredResult {
  device: string;
  tier: string; // basic | dictionary
  weak: boolean;
  attempts: number;
  budget: number;
  detail?: string;
}

export interface NetDevSSHImportCandidate {
  alias: string;
  host: string;
  user: string;
  port: number;
}

export interface NetDevProposalStep {
  device: string;
  /** 判别联合（§7.1）: "" | cli | k8s-apply | sql-migration | file-upload | cert-replace */
  type?: string;
  commands: string[];
  rollback?: string[];
  yaml?: string;
  up_sql?: string;
  down_sql?: string;
  local_path?: string;
  remote_path?: string;
  key_local_path?: string;
  key_remote_path?: string;
  checksum?: string;
  reload_cmd?: string;
  backup?: string;
  applied: boolean;
  error?: string;
  dangerous?: boolean;
}

// Mirrors the Go Proposal JSON (lowercase tags).
export interface NetDevProposal {
  id: string;
  intent: string;
  status: string;
  steps: NetDevProposalStep[];
  created_at: string;
  approved_at?: string;
  executed_at?: string;
  approver?: string;
  confirm2?: boolean;
  note?: string;
  rejected_at?: string;
  reject_reason?: string;
  watch_until?: string;
  watch_note?: string;
}

// ── Job 引擎（R4，v1 C 批）─────────────────────────────────────────────────

export interface NetDevJobStep {
  name: string;
  device: string;
  command: string;
  expect?: string;
  timeout_sec?: number;
  retries?: number;
  on_fail?: string; // pause | abort | continue
  pause_before?: boolean;
}

export interface NetDevJobStepState {
  status: string; // pending | running | ok | failed | skipped
  attempts?: number;
  output?: string;
  error?: string;
  started_at?: string;
  ended_at?: string;
}

export interface NetDevJob {
  id: string;
  name: string;
  steps: NetDevJobStep[];
  step_state: NetDevJobStepState[];
  status: string; // running | paused | done | failed | aborted
  budget?: { max_wall_sec?: number; max_commands?: number; fail_streak?: number };
  created_at: string;
  started_at?: string;
  ended_at?: string;
  active_ms: number;
  commands: number;
  cursor: number;
  breakpoint_ok: number;
  pause_note?: string;
}

// ── 割接模式（§7.2）────────────────────────────────────────────────────────

export interface NetDevCutoverGate {
  device: string;
  command: string;
  expect: string;
  sustain_sec?: number;
  timeout_sec?: number;
}

export interface NetDevCutoverStep {
  label: string;
  est_sec?: number;
  proposal_id?: string;
  device?: string;
  command?: string;
  gate?: NetDevCutoverGate;
  decision_point?: boolean;
  impact?: string;
  status: string; // pending | running | gating | done | failed | approved | rolled-back | skipped
  started_at?: string;
  ended_at?: string;
  output?: string;
  error?: string;
}

export interface NetDevCutoverRun {
  id: string;
  name: string;
  deadline: string;
  steps: NetDevCutoverStep[];
  status: string; // running | hold | done | failed | aborted
  hold_note?: string;
  cursor: number;
  pre_snapshot?: Record<string, string>;
  post_snapshot?: Record<string, string>;
  report?: string;
  created_at: string;
  started_at?: string;
  ended_at?: string;
}

// ── 批量模板（§7.2）────────────────────────────────────────────────────────

export interface NetDevTemplateStep {
  commands: string[]; // {{var}} placeholders
  rollback: string[];
}

export interface NetDevTemplate {
  id: string;
  name: string;
  intent: string;
  vars: string[];
  steps: NetDevTemplateStep[];
  targets: string[];
  created_at: string;
}

export interface NetDevTemplatePreviewStep {
  commands: string[];
  rollback: string[];
  classes: string[]; // per rendered command: read|write|dangerous|unknown
  dangerous: boolean;
}

export interface NetDevTemplatePreviewDevice {
  device: string;
  available: boolean;
  reason?: string;
  steps: NetDevTemplatePreviewStep[];
}

// ── 服务器配置文件管理（§7.3）───────────────────────────────────────────────

export interface NetDevSrvConfVersion {
  id: string;
  device: string;
  path: string;
  at: string;
  bytes: number;
  lines: number;
}

export interface NetDevSrvConfDriftRow {
  device: string;
  group: string;
  status: string; // same | drift | error | absent
  diff?: string;
  error?: string;
}

// Mirrors the Go Finding JSON (lowercase tags).
export interface NetDevFindingEvidence {
  device: string;
  command: string;
  output: string;
}

export interface NetDevFinding {
  id: string;
  title: string;
  severity: "info" | "warning" | "critical";
  devices: string[];
  detail: string;
  evidence: NetDevFindingEvidence[];
  suggestion?: string;
  created_at: string;
  // Auto-origin marker (alert:/syslog:) — such findings auto-resolve; the
  // 发现 card offers a manual resolve button while status === "active".
  source?: string;
  status?: string; // "" | active | resolved
  resolvedAt?: string;
  // Site-scope snapshot stamped at save time (backend finding.go); "" = 未分组
  // — visible in EVERY project view so unknown-source alerts are never hidden.
  project?: string;
}

export interface NetDevTopologyNode {
  name: string;
  managed: boolean;
  device_ip?: string;
  // IP-plan view only: the /24 the address lives in and the locally inferred
  // band (0 core / 1 agg / 2 access / 3 unmanaged; -1 = not inferred).
  subnet?: string;
  tier?: number;
  // Device-class icon key (router/switch/firewall/ips/vpn/bastion/server/ap/
  // cloud; ""/absent = unknown) — orthogonal to tier/health/managed.
  // role_source records the inference provenance (config|kind|group|model|
  // vendor|label|none) for the tooltip confidence hint.
  role?: string;
  role_source?: string;
}

export interface NetDevTopologyGraph {
  nodes: NetDevTopologyNode[];
  edges: { local_device: string; local_port: string; remote_device: string; remote_port?: string; remote_ip?: string; source: string; platform?: string }[];
  at: string;
}

// T2a: drawio design import — preview (no side effects) + the persisted
// design snapshot (topology's third source: plan | design | snapshot).
export interface NetDevTopoImportStats {
  total: number;
  managed: number;
  new_nodes: number;
  unresolved_role: number;
  pages: number;
  used_page: string;
}

export interface NetDevTopoImportPreview {
  graph: NetDevTopologyGraph;
  stats: NetDevTopoImportStats;
  warnings: string[];
}

export interface NetDevTopologyDesign {
  imported_at: string;
  source_file: string;
  graph: NetDevTopologyGraph;
}

// F5: attack-path SIMULATION — pure data-plane, zero connections. Every
// field is 推演 (simulated), never measured.
export interface NetDevAttackPathStep { from: string; to: string; via: string; }
export interface NetDevAttackPath {
  exposure_device: string; reason: string; finding_id?: string;
  steps: NetDevAttackPathStep[]; end_device: string; end_role?: string;
  end_managed: boolean; hops: number; score: number;
}
export interface NetDevExposurePoint { device: string; finding_id?: string; reason: string; paths: number; }
export interface NetDevCutSuggestion { from: string; to: string; via: string; paths_removed: number; }
export interface NetDevAttackPathReport {
  generated_at: string; simulated: boolean; edge_sources: string[];
  nodes: number; edges: number;
  exposure_points: NetDevExposurePoint[];
  paths: NetDevAttackPath[];
  cut_suggestions: NetDevCutSuggestion[];
}

// ── 大屏家族（docs/NETDEV_DASHBOARD_SPEC.md v2.0）────────────────────────────

export interface NetDevOvCoverage { managed: number; discovered: number; unreachable: number; no_snmp: number; }
export interface NetDevOvHealth {
  polled: number; reachable: number; last_poll_at: number;
  flap_alerts: number; p90_alerts: number; uptime_spark?: Record<string, number[]>;
  max_cpu_pct: number; max_cpu_dev?: string; max_mem_pct: number;
}
export interface NetDevOvRisk {
  critical: number; warning: number; info: number; open_total: number;
  weighted_score: number; risk_level: "safe" | "low" | "medium" | "high" | "critical" | string;
  cve_matches: number; cve_needs_feed: boolean; weak_creds: number;
}
export interface NetDevOvInflight {
  proposals_pending: number; proposals_watchable: number; jobs_running: number;
  jobs_paused: number; cutovers_active: number; terminals_open: number;
}
export interface NetDevOvEvent { id: string; severity: string; title: string; source: string; at: string; }
export interface NetDevOvAudit {
  chain_ok: boolean; chain_total: number; last_entry_at?: string;
  read_24h: number; write_24h: number; guardrail_24h: number;
}
export interface NetDevBaselineAgg { devices: number; checked: number; rules: number; hits: number; at: string; }
export interface NetDevInspectionRow {
  at: string; kind: string; devices: number; checked: number;
  critical: number; warning: number; info: number; baseline_hits?: number;
  if_brief?: Record<string, { up: number; down: number }>;
}
export interface NetDevOvStats {
  mttr_hours?: number;
  baseline?: NetDevBaselineAgg | null;
  cve_by_severity?: Record<string, number>;
  cve_needs_feed: boolean;
  job_done: number; job_finished: number;
  cmd_mix: Record<string, number>; audit_entries: number;
  device_by_role: Record<string, number>;
  proposal_funnel: Record<string, number>;
  risk_trend?: NetDevInspectionRow[];
  inspection_compliance?: { enabled: boolean; last_run_at?: string; ok: boolean; title?: string; note?: string } | null;
  cred_health?: { count: number; last_changed_at?: string; age_days: number; stale: boolean } | null;
}
export interface NetDevOverviewSnapshot {
  generated_at: number; stale_after_sec: number;
  coverage: NetDevOvCoverage; health: NetDevOvHealth; risk: NetDevOvRisk;
  inflight: NetDevOvInflight; events: NetDevOvEvent[]; audit: NetDevOvAudit; stats: NetDevOvStats;
  scenario_cutover_active: boolean; scenario_discovery_run: boolean;
}

export type NetDevChainKind = "event" | "action" | "evidence" | "conclusion" | "remediation" | "verification";
export interface NetDevChainNode {
  id: string; kind: NetDevChainKind; label: string; device?: string; at?: string;
  ref_type?: string; ref_id?: string; status?: string; group?: number;
}
export interface NetDevChainEdge { from: string; to: string; label: string; }
export interface NetDevInvestigationChain {
  case_id?: string; case_title?: string; has_case: boolean; finding_id?: string;
  counts: Record<string, number>;
  nodes: NetDevChainNode[]; edges: NetDevChainEdge[];
  timeline?: NetDevTimelineEvent[]; truncated: boolean;
}

export interface NetDevCutoverBoardStep {
  label: string; status: string; device?: string; proposal_id?: string;
  gate?: boolean; decision_point?: boolean; est_sec?: number; started_at?: string; ended_at?: string;
}
export interface NetDevCutoverBoardDevice { device: string; status: string; rollback_ready: boolean; }
export interface NetDevCutoverBoardJob {
  id: string; name: string; status: string; active_ms: number; commands: number;
  max_wall_sec?: number; max_commands?: number;
}
export interface NetDevCutoverBoardAudit { time: string; device: string; command: string; status: string; }
export interface NetDevCutoverBoard {
  id: string; name: string; status: string; deadline?: string; remaining_sec: number;
  frozen: number; steps: NetDevCutoverBoardStep[]; devices: NetDevCutoverBoardDevice[];
  rollback_ready: boolean; rollback_note?: string;
  jobs: NetDevCutoverBoardJob[]; audit: NetDevCutoverBoardAudit[];
  report?: string; has_active: boolean; found: boolean;
}

export interface NetDevDiscoveryFunnelStep { key: string; count: number; }
export interface NetDevDiscoveryLayerRow { layer: number; label: string; note?: string; }
export interface NetDevTriSource { design: number; plan: number; matched: number; only_design: number; only_plan: number; }
export interface NetDevPortEvent { at: string; ip: string; port: number; kind: string; }
export interface NetDevDiscoveryBoard {
  leads: number; fingerprinted: number; pending: number; promoted: number; managed: number;
  subnets_done: number; subnets_total: number; layer_depth: number; max_hops: number;
  run_status?: string; run_vantage?: string; run_updated_at?: string;
  funnel: NetDevDiscoveryFunnelStep[]; layers: NetDevDiscoveryLayerRow[];
  tri_source: NetDevTriSource; port_events: NetDevPortEvent[];
}

export interface NetDevExposureMatrixRow {
  device: string; critical: number; warning: number; info: number;
  cve_critical: number; cve_high: number; managed: boolean;
}
export interface NetDevSyslogCountRow { hour: string; device: string; class: string; n: number; }
export interface NetDevTopoReconcile {
  tri: { design: number; plan: number; matched: number; only_design: number; only_plan: number };
  platforms: Record<string, number>;
}
export interface NetDevExposureBoard {
  simulated: boolean; generated_at: string; critical: number; warning: number;
  paths: NetDevAttackPath[]; cut_suggestions: NetDevCutSuggestion[];
  exposure_points: NetDevExposurePoint[]; matrix: NetDevExposureMatrixRow[];
  cve_by_severity?: Record<string, number>; cve_needs_feed: boolean;
  unmanaged_ends: number; max_hops: number;
}

// ── Loop Engineering (docs/loop-engineering-spec.md §4) ──────────────────────

export interface LoopConfig {
  id: string;
  name: string;
  goal: string;
  sensorCommand: string;
  verifyCommand: string;
  exploratory: boolean;
  autonomy: "L1" | "L2" | "L3";
  maxRounds: number;
  maxTokens: number;
  intervalSeconds: number;
  commandAllowlist: string[];
  startAt?: string;
  endTime?: string;
}

export interface LoopRoundRecord {
  round: number;
  problem?: string;
  changed?: string[];
  verify: "pass" | "fail-rolled-back" | "skipped";
  note?: string;
  durationMs: number;
}

export interface LoopReport {
  roundsRun: number;
  passed: number;
  rolledBack: number;
  skipped: number;
  changedFiles: number;
  lastVerify: string;
  headline: string;
  suggestion?: string;
}

export interface LoopRunStatus {
  runId: string;
  config: LoopConfig;
  workspaceRoot: string;
  tabLabel: string;
  state: "running" | "stopping" | "paused" | "done" | "aborted" | "failed";
  round: number;
  startedAt: number;
  endedAt?: number;
  stopNote?: string;
  tokensUsed: number;
  timeline: LoopRoundRecord[];
  report?: LoopReport;
}

// ── 迁移导入向导（§5.6 / completion-spec §6 #11）────────────────────────────

export interface NetDevImportDevice {
  name: string;
  vendor: string;
  kind: string;
  address: string;
  group: string;
  via: string[];
}

export interface NetDevImportDeviceConflict {
  imported: NetDevImportDevice;
  local: NetDevImportDevice;
}

export interface NetDevImportDBSource {
  name: string;
  type: string;
  host: string;
  port: number;
  via: string[];
}

export interface NetDevImportPreview {
  new_devices: NetDevImportDevice[];
  conflict_devices: NetDevImportDeviceConflict[];
  db_new: NetDevImportDBSource[];
  db_overlap: string[];
  findings_seen: number;
  exported_at: string;
  source: string;
}

// TCP discovery (App.NetDevDiscover / netdev_discover, completion-spec §5.1).
export interface NetDevDiscoverHost {
  ip: string;
  open: { port: number; banner?: string }[];
}

// 待确认区 (F1): one unmanaged asset lead. An asset LEAD, never a finding
// and never an inventory device — promotion is a human act.
export interface NetDevBannerInfo {
  kind?: string;
  product?: string;
  version?: string;
  vendor_hint?: string;
  role_hint?: string;
}

export interface NetDevDiscoveredHost {
  ip: string;
  hostname?: string;
  vendor_hint?: string;
  role_hint?: string;
  first_seen: string;
  last_seen: string;
  sources: string[];
  ports: { port: number; banner?: string; parsed?: NetDevBannerInfo; at: string }[];
}

export interface NetDevPromoteForm {
  ip: string;
  name: string;
  vendor: string;
  role: string;
}

// F4 multi-layer discovery: vantage tables → confirm-first plan.
export interface NetDevPlanStep { cidr: string; class: string; hosts: number; default_on: boolean; }
export interface NetDevDiscoverPlan {
  vantage: string;
  steps: NetDevPlanStep[];
  arp_known: number;
  warnings: string[];
}

// F4 run state: checkpoint progress of one layered scan (paused runs offer
// 继续上次发现 in the dialog).
export interface NetDevDiscoveryRunState {
  id: string; vantage: string; ports?: number[];
  cidrs: string[]; done_cidrs: string[];
  status: "running" | "paused" | "done";
  started_at: string; updated_at: string; found_so_far: number;
}

// Human-terminal recordings (completion-spec §6 #5 回放查看): files were
// ANSI-stripped and redacted at capture time.
export interface NetDevHumanTTYRecording {
  device: string;
  path: string;
  at: string;
  bytes: number;
}

// --- 团队 (team) --------------------------------------------------------------
// Multi-agent collaboration: a 团长 (leader) plans + assigns work, 团员
// (members) each own an independent context, and a kanban board tracks every
// card. Mirrors desktop/team_app.go. Distinct from TeamView above, which is the
// 专家团 (one-shot multi-model collaboration) roster.

export interface TeamMemberView {
  id: string;
  name: string;
  role: string;
  model: string;
  effort: string;
  skills: string[];
  tools: string[];
  systemPrompt: string;
  isLeader: boolean;
  avatar: string;
}

export interface TeamCriterionView {
  text: string;
  done: boolean;
}

export interface TeamTaskView {
  id: string;
  title: string;
  desc: string;
  assigneeId: string;
  assigneeName: string;
  requiredSkills: string[];
  status: string;
  // column is derived server-side from status + assignee + deps.
  column: string;
  deps: string[];
  parentId: string;
  acceptance: TeamCriterionView[];
  deliverable: string;
  attempts: number;
  evidence: string[];
  progress: string;
  error: string;
  // HITL gate (P4 人在回路): "" | "before" | "after"; approvalState is the live
  // status ("", "pending", "approved", "rejected") the 待确认 column keys off.
  approval: string;
  approvalState: string;
  approvalNote: string;
  order: number;
}

export interface TeamDecisionView {
  id: string;
  text: string;
  by: string;
  at?: string;
}

// TeamNoteView is one shared-context entry (P4 共享上下文) posted by a member
// from its run output, or by the user from the panel.
export interface TeamNoteView {
  id: string;
  author: string;
  authorName: string;
  taskId: string;
  text: string;
  at?: string;
}

export interface TeamArtifactView {
  id: string;
  title: string;
  path: string;
  taskId: string;
  kind: string;
  // summary is a one-line gist of the deliverable, so a later member sees what
  // was produced without opening the file.
  summary: string;
}

export interface TeamContextView {
  goal: string;
  constraints: string;
  decisions: TeamDecisionView[];
  artifacts: TeamArtifactView[];
  openQuestions: string[];
  notes: TeamNoteView[];
  // checkpoints are rolling summaries of notes that have aged out of the hot
  // window (older members' knowledge, compressed). Optional so an older backend
  // payload still satisfies this type.
  checkpoints?: TeamCheckpointView[];
  version: number;
}

export interface TeamCheckpointView {
  id: string;
  summary: string;
  count: number;
  // degraded marks an index-style fallback: the summariser (leader model) failed
  // or was unavailable, so this is a list of first lines rather than a synthesis.
  degraded: boolean;
  upTo?: string;
  at?: string;
}

export interface TeamPolicyView {
  maxRounds: number;
  maxParallel: number;
  autoAssign: boolean;
  autoReplan: boolean;
}

export interface TeamProjectView {
  id: string;
  name: string;
  goal: string;
  members: TeamMemberView[];
  tasks: TeamTaskView[];
  context: TeamContextView;
  policy: TeamPolicyView;
  createdAt?: string;
  updatedAt?: string;
}

export interface TeamColumnView {
  key: string;
  label: string;
  states: string[];
  tasks: TeamTaskView[];
}

export interface TeamBoardView {
  teamId: string;
  columns: TeamColumnView[];
  counts: Record<string, number>;
  total: number;
}

export interface TeamCandidateView {
  memberId: string;
  name: string;
  score: number;
  matched: string[];
  missing: string[];
  load: number;
  isLeader: boolean;
}

export interface TeamDraftInput {
  teamId: string;
  instruction: string;
  model: string;
  adopt: boolean;
}

// --- 项目知识中枢 (project knowledge hub) -------------------------------
// desktop/projectkb_app.go mirrors these shapes. The hub unifies four sources
// (code / docs / memory / team) into a self-maintaining project map with a
// revision history, so long-running work stays oriented.
export interface KBSourceView {
  kind: string;
  label: string;
  enabled: boolean;
  count: number;
  note: string;
}

export interface KBView {
  dir: string;
  cwd: string;
  total: number;
  counts: Record<string, number>;
  sources: KBSourceView[];
  updated: string;
  digest: string;
  revisions: number;
  // watching / watchSyncs report the 变更即同步 watcher; lastChange is the
  // one-line gist of the most recent real change (survives a no-op sync).
  watching: boolean;
  watchSyncs: number;
  lastChange?: string;
  lastChangeAt?: string;
  note?: string;
}

export interface KBSearchHitView {
  id: string;
  kind: string;
  label: string;
  title: string;
  ref: string;
  summary: string;
  status: string;
  score: number;
}

export interface KBRevisionView {
  id: string;
  at: string;
  trigger: string;
  note: string;
  nodes: number;
}

// ---------------------------------------------------------------------------
// Usage statistics (settings → models → usage statistics subtab)
// ---------------------------------------------------------------------------

// UsageStatsRequest selects the aggregation range and optional entry-point
// filter for the usage statistics panel. Range is "7" | "14" | "30" | "90" |
// "custom"; custom requires from/to as "2006-01-02" (inclusive, local dates).
// Source "" or "all" aggregates every entry point; "desktop" | "cli" | "serve"
// | "bot" | "remote" filters to that source's records.
export interface UsageStatsRequest {
  range: string;
  from?: string;
  to?: string;
  source?: string;
}

// DailyTokenUsage is one day's token total, per-model split and turn count in
// the daily trend series.
export interface DailyTokenUsage {
  day: string; // "2006-01-02"
  total: number;
  byModel: Record<string, number>; // model ref -> tokens
  byProvider: Record<string, number>; // provider name -> tokens
  requests: number; // API calls that day
  turns: number;
  cacheHit: number; // cached input tokens that day
  cacheMiss: number; // uncached input tokens that day
}

// ModelTokenUsage is one model's aggregate within the range.
export interface ModelTokenUsage {
  model: string; // canonical "provider/model"
  provider: string;
  tokens: number;
  percent: number; // 0..100
}

// ProviderTokenUsage is one provider's aggregate within the range.
export interface ProviderTokenUsage {
  provider: string;
  tokens: number;
  percent: number;
}

// UsageStatsRange is the full aggregate the settings panel renders.
export interface UsageStatsRange {
  from: string;
  to: string;
  tokens: number;
  requests: number; // API calls
  turns: number; // completed turns
  cacheHit: number;
  cacheMiss: number;
  activeDays: number;
  topModel: string;
  topProvider: string;
  // Output-ceiling observability. truncated counts replies the model stopped for
  // hitting the output limit (stop reason "length"); ceilingHit / gatewayCut
  // split them into "the limit we sent was too low" and "something in between
  // cut the reply". A truncated call with no recorded limit counts only in
  // truncated, so ceilingHit + gatewayCut may be less than truncated.
  truncated: number;
  ceilingHit: number;
  gatewayCut: number;
  daily: DailyTokenUsage[];
  models: ModelTokenUsage[];
  providers: ProviderTokenUsage[];
}
