// bridge is the single seam between the React app and the Go kernel. In the Wails
// shell it calls the bound App methods (window.go.main.App.*) and subscribes to
// the runtime event stream (window.runtime.EventsOn). In a plain browser (`pnpm
// dev` outside the shell) those globals are absent, so it falls back to a mock
// that streams a canned turn through the same contract — letting the whole UI be
// developed and laid out without rebuilding the Go side.

import type * as GeneratedApp from "../../wailsjs/go/main/App";

import { builtinPresetsFor } from "./builtinPresets";
import { t } from "./i18n";
import { modeWithAutoApproveTools, modeWithPlan, normalizeCollaborationMode, normalizeMode, normalizeToolApprovalMode, ProviderTemplate, RegistryStatus } from "./types";

import type {
  BotConnectionDiagnostic,
  BotInstallPollResult,
  BotInstallStartResult,
  BotSettingsView,
  CapabilitiesView,
  CheckpointMeta,
  RewindPreviewFileView,
  CoWorkSettingsView,
  CommandInfo,
  ContextInfo,
  DirEntry,
  DroppedItem,
  DreamRunView,
  DreamStatusView,
  EffortInfo,
  NetDevSettingsView,
  NetDevExecResult,
  NetDevLogFollowEvent,
  NetDevLogSearchResult,
  NetDevTriageReport,
  NetDevLocateResult,
  NetDevSeriesPoint,
  NetDevTimelineEvent,
  NetDevExpectedStateView,
  NetDevIncidentCase,
  NetDevAggregatedFinding,
  NetDevDeviceHealth,
  NetDevHealthSnapshot,
  NetDevSyslogStatusView,
  NetDevAuditChainStatus,
  NetDevGoldenInfo,
  NetDevMetricPoint,
  NetDevAuditEntryView,
  NetDevStateEventView,
  NetDevStateFileDiff,
  NetDevStateRestoreResultView,
  NetDevFinding,
  NetDevTopologyGraph,
  NetDevProposal,
  NetDevJob,
  NetDevCutoverRun,
  NetDevTemplate,
  NetDevTemplatePreviewDevice,
  NetDevSrvConfVersion,
  NetDevSrvConfDriftRow,
  NetDevSSHImportCandidate,
  FilePreview,
  HistoryMessage,
  JobView,
  MCPRegistryEntry,
  MCPRegistryView,
  MCPServerInput,
  MemoryView,
  MemorySettings,
  MemorySettingsInput,
  MemoryMigrationReport,
  MemoryPromotionInput,
  MemoryPromotionResult,
  Meta,
  MobileModelInfo,
  MobileSessionInfo,
  ModelInfo,
  ProfileView,
  ProfilePresetsPayload,
  NetworkView,
  PresentPayload,
  ProjectNode,
  ProfileInfo,
  ProviderView,
  QuestionAnswer,
  CollabEvent,
  RagCollectionView,
  RagETAView,
  RagImportResult,
  RagNodeView,
  RagProgressEvent,
  RagSearchHitView,
  GraphDataView,
  EntityDetailView,
  EntityPatch,
  DocPreviewView,
  RunRecordView,
  ServerView,
  SessionMeta,
  SettingsView,
  SchedulePreview,
  SkillRootView,
  SkillView,
  SlashArgsResult,
  TabMeta,
  CalendarEventView,
  CalendarEventInput,
  TaskInput,
  TaskView,
  TeamView,
  TemplateView,
  TopicMeta,
  UpdateInfo,
  UpdateProgress,
  WireEvent,
  WorkspaceChangesView,
  GitCommitView,
  GitCommitDetailView,
  WorkspaceView,
  HooksSettingsView,
  HookConfigView,
  MailProbeResult,
  LoopConfig,
  LoopRunStatus,
  ManagedBrowserStatus,
  InboxItem,
  RagExtractResultView,
  ExpertRunView,
  RecentChatView,
  BotDockStatusView,
  CatalogEntry,
  MarketSourceMeta,
  NetDevTopologyNode,
  NetDevBackupVersion,
  NetDevLiveEvent,
  NetDevLiveSnapshot,
  NetDevWeakCredResult,

  BudgetStatusView,
  WallpaperView,
  TeamProjectView,
  TeamBoardView,
  TeamMemberView,
  TeamTaskView,
  TeamColumnView,
  TeamPolicyView,
  TeamCandidateView,
  TeamContextView,
  TeamDraftInput,
} from "./types";


// AppBindings is derived from the Wails-generated Go → TS method signatures, so
// the compiler catches drift between the Go binding surface and the frontend mock.
// Run `wails generate module` after adding/renaming a bound method on App, then
// `pnpm typecheck` to verify the mock still satisfies the contract.
//
// Types for the new native-feel bindings — kept inline since they are
// bridge-specific and only used in AppBindings / the dev mock.

// Trust domain panel view (desktop/trustdomain_app.go mirrors this shape).
export interface TrustDomainMemberView {
  id: string; name: string; role: string; attestation?: string; admittedAt: number;
}
export interface TrustDomainTokenView {
  id: string; resource: string; ops: string[]; expires: number; parent?: string;
}
export interface TrustDomainView {
  enabled: boolean; joined: boolean; detail?: string;
  domain?: string; height: number; head?: string; paused: boolean;
  quorum?: string; me?: string;
  members?: TrustDomainMemberView[]; revoked?: string[]; tokens?: TrustDomainTokenView[];
  successionConfigured: boolean; successionAfterSec: number;
  successionMembers?: string[]; successionLastActive: number; successionDue: boolean;
}
interface NativeConfirmRequest {
  title: string;
  message: string;
  detail: string;
  confirmLabel: string;
  cancelLabel: string;
  destructive: boolean;
}

interface DesktopWindowState {
  width: number;
  height: number;
  x: number;
  y: number;
  maximised: boolean;
}

// AppBindings is the hand-written contract between the React app and the Go
// kernel. It uses local types (types.ts) so components don't import generated
// model classes. _CheckGeneratedBindings catches drift: when a Go method is
// added or renamed, the generated types shift, and a key present in GeneratedApp
// but missing from AppBindings causes a type error here. Fix: add the new method
// to AppBindings, then run `pnpm typecheck` to verify.
export interface AppBindings {
  Platform(): Promise<string>;
  Submit(input: string): Promise<void>;
  SubmitToTab(tabID: string, input: string): Promise<void>;
  SubmitDisplay(display: string, input: string): Promise<void>;
  SubmitDisplayToTab(tabID: string, display: string, input: string): Promise<void>;
  RunShell(command: string): Promise<void>;
  RunShellForTab(tabID: string, command: string): Promise<void>;
  Steer(text: string): Promise<void>;
  SteerForTab(tabID: string, text: string): Promise<void>;
  // PTY (spec 3-4): ConPTY-backed interactive terminal
  PTYCreate(cols: number, rows: number): Promise<number>;
  PTYCreateForTab(tabID: string, cols: number, rows: number): Promise<number>;
  ListWSLDistros(): Promise<Array<{ name: string; state: string; version: number; default: boolean }>>;
  ListDockerContainers(): Promise<Array<{ ID: string; Image: string; Names: string; State: string; Status: string }>>;
  SSHConnect(host: string, port: string, user: string, authMethod: string, keyPath: string): Promise<{ version: string; goos: string; arch: string; homeDir: string }>;
  SetSSHSecret(authField: "password" | "passphrase", host: string, port: string, user: string, value: string): Promise<void>;
  ServerConnect(address: string, token: string, useTLS: boolean): Promise<{ version: string; goos: string; arch: string; homeDir: string }>;
  ServerForget(address: string): Promise<void>;
  SSHInspectHost(host: string, port: string, user: string): Promise<{ fingerprint: string; trusted: boolean }>;
  SSHTrustHost(host: string, port: string): Promise<void>;
  OpenRemoteTopicTab(kind: string, target: string, user: string, tls: boolean, root: string, topicId: string, title: string, sessionPath: string): Promise<TabMeta | null>;
  RemoteConnectProbe(kind: string, target: string, user: string): Promise<{ version: string; goos: string; arch: string; homeDir: string }>;
  RemoteBrowseList(path: string): Promise<Array<{ name: string; dir: boolean }>>;
  RemoteWizardClose(): Promise<void>;
  OpenRemoteTab(kind: string, target: string, user: string, root: string, label: string): Promise<TabMeta | null>;
  PTYWrite(id: number, input: string): Promise<void>;
  PTYRead(id: number): Promise<[string, boolean]>;
  PTYResize(id: number, cols: number, rows: number): Promise<void>;
  PTYKill(id: number): Promise<void>;
  PTYAlive(id: number): Promise<boolean>;
  FollowUp(text: string): Promise<void>;
  FollowUpForTab(tabID: string, text: string): Promise<void>;
  QueuedMessages(): Promise<{ steer?: string[]; followUp?: string[] }>;
  Cancel(): Promise<void>;
  CancelTab(tabID: string): Promise<void>;
  Pause(): Promise<void>;
  PauseTab(tabID: string): Promise<void>;
  ResumeTurn(): Promise<void>;
  ResumeTurnTab(tabID: string): Promise<void>;
  PausedTab(tabID: string): Promise<boolean>;
  Approve(id: string, allow: boolean, session: boolean, persist: boolean): Promise<void>;
  ApproveTab(tabID: string, id: string, allow: boolean, session: boolean, persist: boolean): Promise<void>;
  AnswerQuestion(id: string, answers: QuestionAnswer[]): Promise<void>;
  AnswerQuestionForTab(tabID: string, id: string, answers: QuestionAnswer[]): Promise<void>;
  ReplayPendingPrompts(): Promise<void>;
  SetFastTaskModel(ref: string): Promise<void>;
  // FastLLMBaseDomain getters/setters retained for Wails binding parity
  // (_CheckGenToApp); not called by the frontend UI. Mock impls included below.
  GetFastLLMBaseDomain(): Promise<string>;
  SetFastLLMBaseDomain(domain: string): Promise<void>;
  // SetPlanMode/SetMode/SetGoal etc. (non-ForTab variants) are retained for
  // Wails binding parity (_CheckGenToApp) but not called by the frontend,
  // which uses the *ForTab variants exclusively. Their mock impls are omitted.
  SetPlanMode(on: boolean): Promise<void>;
  SetMode(mode: string): Promise<void>;
  SetModeForTab(tabID: string, mode: string): Promise<void>;
  SetAutoApproveTools(on: boolean): Promise<void>;
  SetCollaborationMode(mode: string): Promise<void>;
  SetCollaborationModeForTab(tabID: string, mode: string): Promise<void>;
  SetToolApprovalMode(mode: string): Promise<void>;
  SetToolApprovalModeForTab(tabID: string, mode: string): Promise<void>;
  SetRagScope(scope: string): Promise<void>;
  SetRagScopeForTab(tabID: string, scope: string): Promise<void>;
  SetGoal(goal: string): Promise<void>;
  SetGoalForTab(tabID: string, goal: string): Promise<void>;
  ClearGoal(): Promise<void>;
  ClearGoalForTab(tabID: string): Promise<void>;
  Compact(): Promise<void>;
  NewSession(): Promise<void>;
  ClearSession(): Promise<void>;
  HistoryForTab(tabID: string): Promise<HistoryMessage[]>;
  PresentForTab(tabID: string): Promise<PresentPayload>;
  Checkpoints(): Promise<CheckpointMeta[]>;
  CheckpointsForTab(tabID: string): Promise<CheckpointMeta[]>;
  SearchSessionText(query: string): Promise<{ path: string; excerpts: string[]; title?: string; topicId?: string; scope?: string; workspaceRoot?: string; profile?: string }[]>;
  TurnFactsForTab(tabID: string): Promise<{ seq: number; durationMs: number; retries: number; toolCalls: number; toolErrors: number; promptTokens: number; completionTokens: number; cacheHitTokens: number; cacheMissTokens: number; err?: string }[]>;
  BranchesForTab(tabID: string): Promise<{ id: string; name?: string; parentId?: string; path: string; preview?: string; turns?: number; updatedAt: number; current?: boolean }[]>;
  SwitchBranchForTab(tabID: string, ref: string): Promise<void>;
  CheckpointDiffForTab(tabID: string, turn: number): Promise<{ path: string; kind: string; added: number; removed: number; diff: string }[]>;
  RewindPreviewForTab(tabID: string, turn: number): Promise<RewindPreviewFileView[]>;
  Rewind(turn: number, scope: string): Promise<void>;
  Fork(turn: number): Promise<TabMeta>;
  SummarizeFrom(turn: number): Promise<void>;
  SummarizeUpTo(turn: number): Promise<void>;
  ListSessions(): Promise<SessionMeta[]>;
  // Profile-scoped variant ("dev"/"cowork"/"netdev"; empty = all profiles).
  ListSessionsForProfile(profile: string): Promise<SessionMeta[]>;
  ListTrashedSessions(): Promise<SessionMeta[]>;
  // Profile-scoped variant, matching ListSessionsForProfile.
  ListTrashedSessionsForProfile(profile: string): Promise<SessionMeta[]>;
  ResumeSession(path: string): Promise<HistoryMessage[]>;
  ResumeSessionForTab(tabID: string, path: string): Promise<HistoryMessage[]>;
  PreviewSession(path: string): Promise<HistoryMessage[]>;
  DeleteSession(path: string): Promise<void>;
  RestoreSession(path: string): Promise<void>;
  PurgeTrashedSession(path: string): Promise<void>;
  RenameSession(path: string, title: string): Promise<void>;
  ListWorkspaces(): Promise<WorkspaceView[]>;
  PickWorkspace(): Promise<string>;
  PickImportFolder(): Promise<string>;
  PickImportFiles(): Promise<string[]>;
  SwitchWorkspace(path: string): Promise<string>;
  RemoveWorkspace(path: string): Promise<void>;
  ContextUsage(): Promise<ContextInfo>;
  ContextUsageForTab(tabID: string): Promise<ContextInfo>;
  Jobs(): Promise<JobView[]>;
  JobsForTab(tabID: string): Promise<JobView[]>;
  Meta(): Promise<Meta>;
  MetaForTab(tabID: string): Promise<Meta>;
  Commands(): Promise<CommandInfo[]>;
  Capabilities(): Promise<CapabilitiesView>;
  ImportMCPServersJSON(jsonText: string): Promise<number>;
  AddMCPServer(input: MCPServerInput): Promise<number>;
  UpdateMCPServer(name: string, input: MCPServerInput): Promise<void>;
  RemoveMCPServer(name: string): Promise<void>;
  ReconnectMCPServer(name: string): Promise<void>;
  ClearMCPServerAuthentication(name: string): Promise<void>;
  PickSkillFolder(): Promise<string>;
  PickDirectory(title: string): Promise<string>;
  AddSkillPath(path: string): Promise<void>;
  RemoveSkillPath(path: string): Promise<void>;
  RefreshSkills(): Promise<void>;
  SetSkillEnabled(name: string, enabled: boolean): Promise<void>;
  DeriveEditableSkill(name: string): Promise<string>;
  SkillMarketBrowse(): Promise<CatalogEntry[]>;
  SkillMarketSources(): Promise<MarketSourceMeta[]>;
  SkillMarketSearch(query: string, source?: string): Promise<CatalogEntry[]>;
  SkillMarketInstall(installRef: string, name: string, scope: string, apply: boolean): Promise<string>;
  SkillMarketUninstall(name: string, scope: string): Promise<string>;
  SkillMarketInstalledNames(): Promise<Record<string, string>>;
  DreamStatus(): Promise<DreamStatusView>;
  SetDreamEnabled(enabled: boolean): Promise<void>;
  SetDreamIntervals(dreamDays: number, distillDays: number): Promise<void>;
  TriggerDream(): Promise<DreamRunView>;
  TriggerDistill(): Promise<DreamRunView>;
  SetMCPServerEnabled(name: string, enabled: boolean): Promise<void>;
  SetMCPServerTier(name: string, tier: string): Promise<void>;
  MCPRegistrySearch(query: string): Promise<MCPRegistryView>;
  MCPRegistryResolve(name: string): Promise<MCPRegistryEntry>;
  SlashArgs(input: string): Promise<SlashArgsResult>;
  ListDir(rel: string): Promise<DirEntry[]>;
  SearchFileRefs(query: string): Promise<DirEntry[]>;
  ReadFile(rel: string): Promise<FilePreview>;
  WorkspaceChanges(): Promise<WorkspaceChangesView>;
  GitBranches(): Promise<string[]>;
  GitCheckout(branch: string): Promise<void>;
  WorkspaceGitHistory(path: string): Promise<GitCommitView[]>;
  WorkspaceGitCommitDetail(hash: string, path: string): Promise<GitCommitDetailView>;
  OpenWorkspacePath(rel: string): Promise<void>;
  OpenInEditorAt(path: string, line: number): Promise<void>;
  RevealWorkspacePath(rel: string): Promise<void>;
  RevealPath(path: string): Promise<void>;
  SavePastedImage(dataUrl: string): Promise<string>;
  SaveClipboardImage(): Promise<string>;
  SavePastedFile(name: string, dataUrl: string): Promise<string>;
  PickExportFile(defaultFilename: string, mimeType: string): Promise<string>;
  SaveExportFile(path: string, payload: string, base64Encoded: boolean): Promise<void>;
  AttachDropped(path: string): Promise<DroppedItem>;
  AttachmentDataURL(path: string): Promise<string>;
  // Voice input (mic button). VoiceModelConfigured is a cheap poll (reads an
  // in-memory field) so the Composer can render the mic enabled/disabled each
  // render. TranscribeAudio sends a base64 wav data URL → returns transcript.
  VoiceModelConfigured(): Promise<boolean>;
  TranscribeAudio(audioDataURL: string, language: string): Promise<string>;
  // Mobile bridge (mobilebridge_app.go) — wailsjs generates these from the Go
  // methods; AppBindings must declare them so _CheckGenToApp stays satisfied.
  // NOTE: MobileBridgeStartPairing's Go signature returns (code, qrURL, err);
  // wailsjs collapses it to Promise<string>. Matching that here keeps tsc happy;
  // the runtime shape is a separate (pre-existing) concern for the mobile bridge.
  MobileBridgeConfirm(pairID: string): Promise<void>;
  MobileBridgeReject(pairID: string): Promise<void>;
  MobileBridgeStartPairing(): Promise<string>;
  MobileBridgeStatus(): Promise<Record<string, any>>;
  MobileBridgeUnpair(deviceCode: string): Promise<void>;
  // 配对网卡：枚举可进二维码的真实网卡（isDefault=默认路由出口），钉死选择。
  MobileBridgeListPairNics(): Promise<string>;
  MobileBridgeSetPairNic(ip: string): Promise<void>;
  // UDP 单包敲门（M3 NAT 穿透辅助）：开关 + 远程 STUN 服务器。
  MobileBridgeSetKnock(enabled: boolean, server: string): Promise<void>;
  // 公网跳板（跨网配对/信令）：开关 + 云 K 地址（enabled=false 关闭并清空）。
  MobileBridgeSetCloudRelay(enabled: boolean, url: string): Promise<void>;
  // 信令模式切换（embedded 零配置内嵌 K / external 外部 K / cloud 仅云），
  // 持久化后重启 fairpeer 生效，返回最终模式。
  MobileBridgeSetKMode(mode: string, externalURL: string): Promise<string>;
  // 粘贴 turn-cred.sh 输出一键解析回填 TURN 四项，返回 {host,port,user}。
  MobileBridgeParseTurnCred(paste: string): Promise<string>;
  // Mobile-facing readouts (mobilebridge_app.go + tabs.go). ModelsForMobile /
  // SessionListForMobile return the trimmed mobile payloads above, NOT the
  // app-wide ModelInfo/SessionMeta. ActiveTabID feeds the current tab id to the
  // mobile companion.
  ActiveTabID(): Promise<string>;
  ModelsForMobile(): Promise<MobileModelInfo[]>;
  SessionListForMobile(): Promise<MobileSessionInfo[]>;
  Models(): Promise<ModelInfo[]>;
  SetModel(name: string): Promise<void>;
  ModelsForTab(tabID: string): Promise<ModelInfo[]>;
  SetModelForTab(tabID: string, name: string): Promise<void>;
  // Product profile (dev | cowork). SwitchProfile rebuilds the tab's controller
  // with the profile's model/prompt/skill/plugin bundle (see app.SwitchProfileForTab).
  Profile(): Promise<string>;
  ProfileForTab(tabID: string): Promise<string>;
  Profiles(): Promise<ProfileInfo[]>;
  SwitchProfile(name: string): Promise<void>;
  SwitchProfileForTab(tabID: string, name: string): Promise<void>;
  Effort(): Promise<EffortInfo>;
  SetEffort(level: string): Promise<void>;
  EffortForTab(tabID: string): Promise<EffortInfo>;
  SetEffortForTab(tabID: string, level: string): Promise<void>;
  Memory(): Promise<MemoryView>;
  MemoryHistory(): Promise<MemoryView>;
  Remember(scope: string, note: string): Promise<string>;
  Forget(name: string): Promise<void>;
  PromoteMemory(name: string): Promise<boolean>;
  RejectMemory(name: string): Promise<boolean>;
  SaveMemorySettings(input: MemorySettingsInput): Promise<MemorySettings>;
  MigrateMemoryRoot(to: string): Promise<MemoryMigrationReport>;
  PromoteMemoryArtifact(input: MemoryPromotionInput): Promise<MemoryPromotionResult>;
  // 团队 (team): persistent leader + members project with a kanban board.
  // Distinct from the 专家团 team methods above (ListExpertTeams/…).
  ListTeamProjects(): Promise<TeamProjectView[]>;
  GetTeamProject(id: string): Promise<TeamProjectView>;
  CreateTeamProject(tv: TeamProjectView): Promise<TeamProjectView>;
  UpdateTeamProject(tv: TeamProjectView): Promise<TeamProjectView>;
  DeleteTeamProject(id: string): Promise<void>;
  AddTeamMember(teamId: string, mv: TeamMemberView): Promise<TeamProjectView>;
  UpdateTeamMember(teamId: string, mv: TeamMemberView): Promise<TeamProjectView>;
  RemoveTeamMember(teamId: string, memberId: string): Promise<TeamProjectView>;
  AddTeamTask(teamId: string, tv: TeamTaskView): Promise<TeamProjectView>;
  UpdateTeamTask(teamId: string, tv: TeamTaskView): Promise<TeamProjectView>;
  MoveTeamTask(teamId: string, taskId: string, column: string): Promise<TeamProjectView>;
  RemoveTeamTask(teamId: string, taskId: string): Promise<TeamProjectView>;
  TeamBoard(teamId: string): Promise<TeamBoardView>;
  SuggestTeamAssignee(teamId: string, taskId: string): Promise<TeamCandidateView[]>;
  SetTeamContext(teamId: string, ctx: TeamContextView): Promise<TeamProjectView>;
  TeamBlackboardDigest(teamId: string, maxChars: number): Promise<string>;
  // Natural-language creation: draft a member (needs teamId) or a whole team
  // (empty teamId). Adopt=false returns a preview for confirmation.
  DraftTeamMember(input: TeamDraftInput): Promise<TeamMemberView>;
  DraftTeamProject(input: TeamDraftInput): Promise<TeamProjectView>;
  // The 团长's planning step: decompose a goal into cards and route each to the
  // best-skilled member by capability matching.
  PlanTeamTasks(teamId: string, goal: string, model: string): Promise<TeamProjectView>;
  // Execute a card as its assignee's own isolated sub-session (P2). The call
  // returns once the card flips to running; progress arrives via team:changed.
  RunTeamTask(teamId: string, taskId: string): Promise<TeamProjectView>;
  CancelTeamTask(teamId: string, taskId: string): Promise<void>;
  // Task IDs with an in-flight run, so a remounted board restores its spinners.
  RunningTeamTasks(teamId: string): Promise<string[]>;
  SaveDoc(path: string, body: string): Promise<string>;
  PortraitProfile(): Promise<ProfileView>;
  ProfilePresets(): Promise<ProfilePresetsPayload>;
  SetProfilePresets(payload: ProfilePresetsPayload): Promise<string>;
  Settings(): Promise<SettingsView>;
  SetDefaultModel(ref: string): Promise<void>;
  SetSubagentModel(ref: string): Promise<void>;
  SetSubagentEffort(level: string): Promise<void>;
  SetAutoPlan(mode: string): Promise<void>;
  SaveProvider(p: ProviderView): Promise<void>;
  AddOfficialProviderAccess(kind: string, key: string): Promise<void>;
  FetchProviderModels(p: ProviderView): Promise<string[]>;
  DeleteProvider(name: string): Promise<void>;
  RemoveProviderAccess(name: string): Promise<void>;
  SetProviderKey(apiKeyEnv: string, value: string): Promise<void>;
  ClearProviderKey(apiKeyEnv: string): Promise<void>;
  SetPermissionMode(mode: string): Promise<void>;
  AddPermissionRule(list: string, rule: string): Promise<void>;
  RemovePermissionRule(list: string, rule: string): Promise<void>;
  SetSandbox(bash: string, network: boolean, workspaceRoot: string, allowWrite: string[]): Promise<void>;
  SetNetwork(n: NetworkView): Promise<void>;
  SetBotSettings(b: BotSettingsView): Promise<void>;
  // coWork profile settings (browser/PPT/email/RAG). Secrets go to a managed
  // .env via SetCoWorkSettings; CheckCoworkBrowser powers the panel's detect
  // button; OpenPPTTemplateDir opens the templates folder so the user can add
  // JSON templates.
  SetCoWorkSettings(v: CoWorkSettingsView): Promise<void>;
  // netdev（运维）settings: inventory persists to the USER config ([netdev] is
  // globally pinned); passwords go to the encrypted secret store under the
  // netdev namespace (blank password field = keep the stored secret).
  NetDevSettings(): Promise<NetDevSettingsView>;
  SetNetDevSettings(v: NetDevSettingsView): Promise<void>;
  NetDevDeleteSecret(kind: string, envName: string): Promise<void>;
  // First-device flow: connect → TOFU capture → CLI session verify. Returns
  // status ok | unknown-host-key (with fingerprint for the confirm dialog) |
  // auth-failed | error.
  NetDevTestConnection(device: string): Promise<{ device: string; status: string; detail?: string; host?: string; keyType?: string; fingerprint?: string }>;
  NetDevTrustHostKey(fingerprint: string): Promise<void>;
  // Proposal pipeline (human-only entry points; the agent can only draft).
  NetDevProposals(): Promise<NetDevProposal[]>;
  // One-click read-battery sweep; files a Finding with evidence.
  NetDevRunInspection(): Promise<NetDevFinding | null>;
  // Config-security baseline battery (sealed reads + local rules) → Findings.
  NetDevRunBaseline(): Promise<NetDevFinding | null>;
  // Configuration backup vault: snapshot (sealed read, redacted-only), list,
  // and unified diff between two versions.
  NetDevRunBackup(device: string): Promise<NetDevBackupVersion[]>;
  NetDevBackups(device: string): Promise<NetDevBackupVersion[]>;
  NetDevBackupDiff(device: string, idA: string, idB: string): Promise<string>;
  // Daily briefing: objective 24h data → designed prompt → headless netdev
  // controller synthesizes the report (content is model-judged, not templated).
  NetDevDailyBriefing(): Promise<string>;
  // One GET-only Redfish query for the device card's BMC panel.
  NetDevRedfishQuery(device: string, path: string): Promise<string>;
  // One allowlisted SNMP get/walk for the device card's metrics panel.
  NetDevSnmpQuery(device: string, oid: string, mode: string): Promise<string>;
  // Assessment-mode weak-credential check (engagement-envelope gated; every
  // attempt audited). basic tier only from the UI.
  NetDevWeakCredCheck(device: string, tier: string): Promise<NetDevWeakCredResult>;
  // 操作实况 mount-time snapshot; live updates arrive on the
  // "netdev:live" channel (see onNetdevLive).
  NetDevLiveSnapshot(): Promise<NetDevLiveSnapshot>;
  // Import a user-run nmap -oX dump: hosts + open ports → one Finding; hosts
  // outside the inventory are flagged 待确认 (nothing dials).
  NetDevImportNmap(xmlText: string): Promise<NetDevFinding | null>;
  NetDevFindings(): Promise<NetDevFinding[]>;
  // Findings queue hygiene: per-row dismiss and double-confirmed clear-all.
  NetDevFindingDismiss(id: string): Promise<void>;
  NetDevFindingsClear(): Promise<number>;
  // Emergency stop: close every device connection at once (audited).
  NetDevEmergencyStop(): Promise<number>;
  // Reset the per-turn command budget — called on every user submit in the
  // 运维 profile so turn_command_budget is a true per-ask control.
  NetDevTurnBegin(): Promise<void>;
  // Rate-limit budget window status for live UI display (rpm/used/remaining).
  BudgetStatus(): Promise<BudgetStatusView>;
  // One-click read-table growth from a refusal chip (user teaches, never the
  // model). Single-line commands only.
  NetDevAddExtraRead(vendor: string, command: string): Promise<void>;
  // UI quick-diagnose: one read-only command through the SAME sealed path.
  NetDevQuickExec(device: string, command: string): Promise<{ device: string; command: string; class: string; output: string; is_error: boolean; refused?: boolean; refusal?: string }>;
  NetDevTopologySnapshot(): Promise<NetDevTopologyGraph | null>;
  // LOCAL IP-plan view: pure computation over the inventory (zero device
  // sessions, zero model calls) — the 拓扑 tab's instant default.
  NetDevTopologyPlan(): Promise<NetDevTopologyGraph | null>;
  NetDevImportTopoPreview(xmlText: string): Promise<import("./types").NetDevTopoImportPreview | null>;
  NetDevImportVsdxPreview(b64: string): Promise<import("./types").NetDevTopoImportPreview | null>;
  NetDevAttachmentBytesBase64(rel: string): Promise<string>;
  NetDevImportTopoApply(sourceFile: string, graph: import("./types").NetDevTopologyGraph): Promise<void>;
  NetDevTopologyDesign(): Promise<import("./types").NetDevTopologyDesign | null>;
  NetDevAttackPaths(): Promise<import("./types").NetDevAttackPathReport | null>;
  // 大屏家族（DASHBOARD spec v2.0）：总览快照 + 四屏组装
  NetDevOverview(force: boolean): Promise<import("./types").NetDevOverviewSnapshot | null>;
  NetDevInvestigationChain(caseID: string, findingID: string, hours: number): Promise<import("./types").NetDevInvestigationChain | null>;
  NetDevCutoverBoard(id: string): Promise<import("./types").NetDevCutoverBoard | null>;
  NetDevDiscoveryBoard(): Promise<import("./types").NetDevDiscoveryBoard | null>;
  NetDevExposureBoard(): Promise<import("./types").NetDevExposureBoard | null>;
  // fairpeer:// 深链冷路径：boot 时一次性取走启动 argv 里的路由（null=普通启动）。
  NetDevConsumeDeepLink(): Promise<{ kind: string; id: string } | null>;
  // 页签充实：syslog 事件量（R3 journal）/ 拓扑对账（离线）
  NetDevSyslogCounts(limit: number): Promise<import("./types").NetDevSyslogCountRow[]>;
  NetDevTopoReconcile(): Promise<import("./types").NetDevTopoReconcile | null>;
  NetDevApproveProposal(id: string, confirm2: boolean): Promise<NetDevProposal>;
  NetDevRejectProposal(id: string, reason: string): Promise<NetDevProposal>;
  NetDevDeleteProposal(id: string): Promise<void>;
  NetDevExecuteProposal(id: string): Promise<NetDevProposal>;
  NetDevRollbackProposal(id: string): Promise<NetDevProposal>;
  NetDevAuditTail(n: number): Promise<NetDevAuditEntryView[]>;
  NetDevSSHImportCandidates(): Promise<NetDevSSHImportCandidate[]>;
  // P0/P1 运维感知侧: structured log read / streaming follow / read-only db
  // diagnostics / SNMP health snapshot. Follow chunks + health changes stream
  // on the "netdev:logfollow" / "netdev:health" channels.
  NetDevLogRead(device: string, source: string, tailN: number, since: string, grep: string): Promise<NetDevExecResult>;
  NetDevLogSearch(pattern: string, devices: string[], sources: string[], since: string): Promise<NetDevLogSearchResult>;
  NetDevTriageRun(device: string): Promise<NetDevTriageReport>;
  // kind=docker / kind=k8s read-only API targets (NETDEV_SPEC_V2 §2.2/§2.3).
  NetDevDockerGet(device: string, what: string, container: string, tailN: number): Promise<string>;
  NetDevK8sGet(device: string, what: string, namespace: string, name: string, tailN: number): Promise<string>;
  NetDevFirewallGet(device: string, what: string): Promise<string>;
  NetDevLocate(target: string): Promise<NetDevLocateResult>;
  NetDevSeries(device: string, hours: number): Promise<Record<string, NetDevSeriesPoint[]>>;
  NetDevHandoffReport(): Promise<string>;
  NetDevWeeklyReport(): Promise<string>;
  NetDevCredentialInventory(): Promise<string>;
  NetDevTrapStatus(): Promise<{ listening: boolean; port: number; buffered: number }>;
  NetDevExportState(): Promise<string>;
  NetDevImportStageFile(content: string): Promise<string>;
  NetDevImportPreview(path: string): Promise<import("./types").NetDevImportPreview>;
  NetDevImportApply(path: string, addNames: string[], takeOvernames: string[]): Promise<number>;
  NetDevTimeline(device: string, hours: number): Promise<NetDevTimelineEvent[]>;
  NetDevExpectedState(): Promise<NetDevExpectedStateView>;
  NetDevCases(): Promise<NetDevIncidentCase[]>;
  NetDevCaseSave(c: NetDevIncidentCase): Promise<NetDevIncidentCase>;
  NetDevCaseDelete(id: string): Promise<void>;
  NetDevCaseBundle(id: string): Promise<string>;
  NetDevAckFinding(id: string): Promise<void>;
  NetDevFalsePositiveFinding(id: string): Promise<void>;
  NetDevAggregatedFindings(): Promise<NetDevAggregatedFinding[]>;
  NetDevImportCVEs(feedJSON: string): Promise<number>;
  NetDevCVEMatches(): Promise<{ device: string; cve_id: string; desc: string; severity: string; product: string }[]>;
  NetDevCVESweep(): Promise<NetDevFinding | null>;
  // Human terminal (§6.1) + SFTP read-only download (§6.2)
  NetDevHumanTTYStart(device: string): Promise<{ device: string; connected: boolean; startedAt: string; bytes: number }>;
  // completion-spec additions
  NetDevDiscover(cidr: string, via: string, ports: number[]): Promise<import("./types").NetDevDiscoverHost[]>;
  NetDevDiscoveredHosts(): Promise<import("./types").NetDevDiscoveredHost[]>;
  NetDevPromoteHosts(entries: import("./types").NetDevPromoteForm[]): Promise<void>;
  NetDevDeleteDiscoveredHost(ip: string): Promise<void>;
  NetDevDiscoverPrecheck(vantage: string): Promise<import("./types").NetDevDiscoverPlan | null>;
  NetDevDiscoverLayer(vantage: string, cidrs: string[], ports: number[]): Promise<import("./types").NetDevDiscoverHost[]>;
  NetDevDiscoveryRunState(): Promise<import("./types").NetDevDiscoveryRunState | null>;
  NetDevDiscoverResume(): Promise<import("./types").NetDevDiscoverHost[]>;
  NetDevDiscoverPause(): Promise<boolean>;
  NetDevNetconfQuery(device: string, kind: string): Promise<string>;
  NetDevHumanTTYRecordings(): Promise<import("./types").NetDevHumanTTYRecording[]>;
  NetDevHumanTTYRecordingRead(path: string): Promise<string>;
  NetDevHumanTTYWrite(device: string, input: string): Promise<void>;
  NetDevHumanTTYStop(device: string): Promise<void>;
  NetDevHumanTTYStatus(): Promise<{ device: string; connected: boolean; startedAt: string; bytes: number }[]>;
  NetDevHumanTTYResize(device: string, cols: number, rows: number): Promise<void>;
  NetDevSFTPDownload(device: string, remotePath: string): Promise<string>;
  NetDevSFTPBrowse(device: string, dirPath: string): Promise<string[]>;
  // Job 引擎（R4，v1 C 批）——诊断 runbook 的多步骤 harness
  NetDevJobStart(def: NetDevJob): Promise<NetDevJob>;
  NetDevJobs(): Promise<NetDevJob[]>;
  NetDevJobGet(id: string): Promise<NetDevJob>;
  NetDevJobPause(id: string): Promise<void>;
  NetDevJobResume(id: string): Promise<NetDevJob>;
  NetDevJobAbort(id: string): Promise<NetDevJob>;
  // 割接模式（§7.2）——倒计时 runbook + 验证门 + 回退决策点
  NetDevCutoverStart(def: NetDevCutoverRun): Promise<NetDevCutoverRun>;
  NetDevCutovers(): Promise<NetDevCutoverRun[]>;
  NetDevCutoverGet(id: string): Promise<NetDevCutoverRun>;
  NetDevCutoverContinue(id: string): Promise<NetDevCutoverRun>;
  NetDevCutoverRollback(id: string): Promise<NetDevCutoverRun>;
  NetDevCutoverAbort(id: string): Promise<NetDevCutoverRun>;
  NetDevCutoverReport(id: string): Promise<string>;
  // 批量模板（§7.2）——渲染预览无副作用；apply 生成提案草稿
  NetDevTemplates(): Promise<NetDevTemplate[]>;
  NetDevTemplateSave(t: NetDevTemplate): Promise<NetDevTemplate>;
  NetDevTemplateDelete(id: string): Promise<void>;
  NetDevTemplateRender(id: string, vars: Record<string, string>): Promise<NetDevTemplatePreviewDevice[]>;
  NetDevTemplateApply(id: string, vars: Record<string, string>): Promise<NetDevProposal>;
  // 服务器配置文件管理（§7.3）
  NetDevSrvConfSnapshot(device: string, path: string): Promise<NetDevSrvConfVersion>;
  NetDevSrvConfVersions(device: string, path: string): Promise<NetDevSrvConfVersion[]>;
  NetDevSrvConfDiff(idA: string, idB: string): Promise<string>;
  NetDevSrvConfDrift(path: string, devices: string[]): Promise<NetDevSrvConfDriftRow[]>;
  // 带外启动器（§6.3）
  NetDevOOBLaunch(device: string): Promise<string>;
  NetDevLogFollowStart(device: string, source: string): Promise<void>;
  NetDevLogFollowStop(device: string): Promise<void>;
  NetDevDBQuery(source: string, query: string): Promise<string>;
  NetDevHealthSnapshot(): Promise<NetDevHealthSnapshot>;
  // Trust domain (docs/TRUSTDOMAIN_SPEC.md §15.3): read-mostly panel + brake.
  TrustDomainStatus(): Promise<TrustDomainView>;
  TrustDomainPause(reason: string): Promise<void>;
  TrustDomainResume(): Promise<void>;
  TrustDomainAnchor(): Promise<void>;
  // P2: passive syslog ring buffer + audit hash-chain verify + alert resolve.
  NetDevSyslogTail(device: string, tailN: number, grep: string): Promise<string[]>;
  NetDevSyslogStatus(): Promise<NetDevSyslogStatusView>;
  NetDevAuditVerify(): Promise<NetDevAuditChainStatus>;
  NetDevResolveFinding(id: string): Promise<void>;
  // Golden Config: baseline from a backup version + drift check.
  NetDevMetricHistory(device: string): Promise<NetDevMetricPoint[]>;
  NetDevSetGoldenFromBackup(device: string, versionID: string): Promise<void>;
  NetDevGoldenInfo(device: string): Promise<NetDevGoldenInfo>;
  NetDevGoldenCheck(device: string): Promise<string>;
  NetDevNotifyTest(): Promise<void>;
  // State history (三层回退之"状态回退"): pre-mutation snapshots of ops state
  // files; restore is local-records-only, devices are never touched.
  NetDevStateEvents(): Promise<NetDevStateEventView[]>;
  NetDevStateEventDiff(id: number): Promise<NetDevStateFileDiff[]>;
  NetDevStateRestore(id: number): Promise<NetDevStateRestoreResultView>;
  // ProbeMailAccount tests a saved mailbox's IMAP login by actually connecting.
  // An empty name probes the Default account; a non-empty name probes that
  // named account. Returns ok/error/unconfigured so the mail card can show a
  // green/red status dot after the user saves. Always resolves (a connection
  // failure comes back as status="error", not a rejection).
  ProbeMailAccount(name: string): Promise<MailProbeResult>;
  // InboxPreview reads the most recent messages (up to limit) from a mailbox
  // ("INBOX" unread-only, or "Sent" for sent mail), for the cowork dock's
  // "邮件" tab. Returns [] when no mailbox is configured or no mail.
  InboxPreview(mailbox: string, limit: number): Promise<InboxItem[]>;
  // Hooks settings (settings.json, global + project scopes). HooksSettings
  // returns the payload for the Hooks tab; Save/Trust write + gate project hooks.
  HooksSettings(scope: string): Promise<HooksSettingsView>;
  SaveHooksSettings(scope: string, hooks: HookConfigView[]): Promise<void>;
  SaveHooksSettingsForRoot(scope: string, projectRoot: string, hooks: HookConfigView[]): Promise<void>;
  TrustProjectHooks(): Promise<void>;
  TrustProjectHooksForRoot(projectRoot: string): Promise<void>;
  CheckCoworkBrowser(): Promise<string>;
  // 可控浏览器 (managed attachable browser): a persistent browser window with
  // a fixed CDP port (9222) and dedicated profile. Start launches it (or
  // reports it as already running); Check only probes. Pair with
  // browserAttachURL in the cowork settings so browser_auto attaches to it.
  StartManagedBrowser(): Promise<ManagedBrowserStatus>;
  CheckManagedBrowser(): Promise<ManagedBrowserStatus>;
  // OpenURLInManagedBrowser launches the managed browser if needed and opens
  // the URL as a new tab in it (preview pane's companion-window tier).
  OpenURLInManagedBrowser(url: string): Promise<ManagedBrowserStatus>;
  // 浏览器控制台 (ops browser console): manual primitives over the kernel's
  // console session slot, recording, and the record→SKILL.md generator.
  // Progress streams: "browser:record" (live events) / "browser:trial"
  // (per-step trial-run status).
  BrowserConsoleOpen(cdpURL: string, url: string): Promise<import("./types").BrowserConsoleState>;
  BrowserConsoleState(): Promise<import("./types").BrowserConsoleState>;
  BrowserConsoleTabs(): Promise<import("./types").BrowserConsoleTab[]>;
  BrowserConsoleHighlight(target: string, durationMs: number): Promise<void>;
  BrowserConsoleSwitchTab(index: number): Promise<string>;
  BrowserConsoleClose(): Promise<void>;
  BrowserConsoleNavigate(url: string): Promise<string>;
  BrowserConsoleBack(): Promise<string>;
  BrowserConsoleForward(): Promise<string>;
  BrowserConsoleExtractTable(selector: string): Promise<string>;
  BrowserConsoleElements(): Promise<import("./types").BrowserConsoleElement[]>;
  BrowserConsoleClick(target: string): Promise<string>;
  BrowserConsoleType(target: string, text: string): Promise<string>;
  BrowserConsoleKey(key: string): Promise<void>;
  BrowserConsoleScroll(direction: string, amount: number): Promise<string>;
  BrowserConsoleSelectOption(target: string, value: string): Promise<string>;
  BrowserConsoleUploadFile(target: string, files: string[]): Promise<string>;
  BrowserConsoleWait(condition: string, timeoutSec: number): Promise<string>;
  BrowserConsoleExtract(selector: string): Promise<string>;
  BrowserConsoleScreenshot(): Promise<string>;
  BrowserConsoleEvaluate(expression: string): Promise<string>;
  BrowserConsoleRecordStart(): Promise<void>;
  BrowserConsoleRecordStop(): Promise<import("./types").BrowserConsoleRecordEvent[]>;
  BrowserConsoleFilterTrace(events: import("./types").BrowserConsoleRecordEvent[]): Promise<import("./types").BrowserConsoleTraceFilter>;
  BrowserConsoleGenerateSkill(nameHint: string, events: import("./types").BrowserConsoleRecordEvent[]): Promise<import("./types").BrowserSkillDraft>;
  BrowserConsoleSaveSkill(content: string, overwrite: boolean): Promise<string>;
  BrowserConsoleListSkills(): Promise<import("./types").BrowserConsoleSkill[]>;
  BrowserConsoleReadSkill(name: string): Promise<string>;
  BrowserConsoleDeleteSkill(name: string): Promise<void>;
  BrowserConsoleSetKeepAlive(enabled: boolean, intervalSec: number, mode: string, url: string): Promise<import("./types").BrowserConsoleState>;
  BrowserConsoleTrialRun(steps: import("./types").BrowserConsoleStep[], params: Record<string, string>): Promise<void>;
  BrowserConsoleTrialResume(reply: string): Promise<void>;
  BrowserConsoleTrialAbort(): Promise<void>;
  // Loop Engineering (docs/loop-engineering-spec.md): start/stop/status of the
  // supervised agent loop. Round updates arrive on the "loop:round" event.
  LoopStart(tabID: string, config: LoopConfig): Promise<void>;
  LoopStop(reason: string): Promise<void>;
  LoopStatus(): Promise<LoopRunStatus | null>;
  OpenPPTTemplateDir(): Promise<void>;
  PickPPTTemplate(): Promise<string>;
  SetBotSecret(envName: string, value: string): Promise<void>;
  ClearBotSecret(envName: string): Promise<void>;
  StartBotConnectionInstall(provider: string, domain: string): Promise<BotInstallStartResult>;
  PollBotConnectionInstall(installID: string): Promise<BotInstallPollResult>;
  // CompleteTelegramBotConnection validates a pasted Bot Token via getMe and
  // saves the connection in one step (Telegram needs no OAuth/QR install flow).
  CompleteTelegramBotConnection(token: string): Promise<BotInstallPollResult>;
  DiagnoseBotConnection(id: string): Promise<BotConnectionDiagnostic>;
  TestBotConnection(id: string, target?: string): Promise<BotConnectionDiagnostic>;
  // ListRecentBotChats returns recently-seen IM chats for the task-form picker.
  ListRecentBotChats(): Promise<RecentChatView[]>;
  // BotDockStatus returns the lightweight bot status for the dock Today panel
  // (online, connected platforms, recent chat count). Replaces hardcoded text.
  BotDockStatus(): Promise<BotDockStatusView>;
  SetCloseBehavior(mode: string): Promise<void>;
  SetDisplayMode(mode: string): Promise<void>;
  SetDesktopLanguage(lang: string): Promise<void>;
  SetDesktopAppearance(theme: string, style: string): Promise<void>;
  // Custom desktop wallpaper (wallpaper_app.go). The chosen image is copied into
  // the app wallpaper directory and loaded through a local asset route, so it
  // survives the original file being moved. UI-only — never model-visible.
  WallpaperInfo(): Promise<WallpaperView>;
  PickWallpaper(): Promise<WallpaperView>;
  ClearWallpaper(): Promise<void>;
  SetWallpaperOptions(blur: number, dim: number, fit: string): Promise<void>;
  SetDesktopCheckUpdates(enabled: boolean): Promise<void>;
  SetDesktopTelemetry(enabled: boolean): Promise<void>;
  SetExpandThinking(on: boolean): Promise<void>;
  MigrateDesktopPreferences(language: string, theme: string, style: string): Promise<void>;
  SetAgentParams(temperature: number, maxSteps: number, plannerMaxSteps: number, systemPrompt: string): Promise<void>;
  SetRPM(rpm: number): Promise<void>;
  SetTrayLocale(locale: "en" | "zh"): Promise<void>;
  // SetBypass is the legacy Wails name for YOLO/full-access tool auto-approval
  // (ask questions and plan approvals still wait; deny rules still apply).
  // Runtime-only.
  SetBypass(on: boolean): Promise<void>;
  Version(): Promise<string>;
  CheckUpdate(): Promise<UpdateInfo | null>;
  ApplyUpdate(): Promise<void>;
  OpenDownloadPage(): Promise<void>;
  NeedsOnboarding(): Promise<boolean>;
  ConnectKey(apiKey: string): Promise<void>;
  // Multi-vendor onboarding (provider_templates.go + app.go).
  GetProviderTemplates(): Promise<ProviderTemplate[]>;
  ProbeVendorKey(baseURL: string, apiKey: string): Promise<void>;
  SetupProvider(template: ProviderTemplate, apiKey: string, defaultModel: string, visionModel: string, fastModel: string, voiceModel: string): Promise<void>;
  // Provider-template registry (registry.go).
  GetRegistryStatus(): Promise<RegistryStatus>;
  RefreshRegistry(): Promise<void>;
  // Crash overlay "Send report" (desktop/crash_app.go): scrubs user paths, attaches
  // version/os/arch, POSTs to the collection endpoint. Only ever sent on user click.
  ReportCrash(kind: string, detail: string): Promise<void>;
  ListTabs(): Promise<TabMeta[]>;
  // profile ("dev"|"cowork"|"" for default) scopes the topic/tab to a product
  // profile; it comes from the active tab's profile in the frontend.
  OpenProjectTab(workspaceRoot: string, topicID: string, profile: string): Promise<TabMeta>;
  OpenProjectTab3(workspaceRoot: string, topicID: string, profile: string): Promise<TabMeta>;
  OpenGlobalTab(topicID: string, profile: string): Promise<TabMeta>;
  EnsureBlankTab(scope: string, workspaceRoot: string, profile: string): Promise<TabMeta>;
  OpenExpertSessionTab(teamId: string, teamName: string): Promise<TabMeta>;
  SetActiveTab(tabID: string): Promise<void>;
  ReorderTabs(tabIDs: string[]): Promise<void>;
  CloseTab(tabID: string): Promise<void>;
  ListProjectTree(profile: string): Promise<ProjectNode[]>;
  RenameProject(workspaceRoot: string, title: string): Promise<void>;
  SetProjectColor(workspaceRoot: string, color: string): Promise<void>;
  ReorderProjects(profile: string, workspaceRoots: string[]): Promise<void>;
  CreateTopic(scope: string, workspaceRoot: string, profile: string, title: string): Promise<TopicMeta>;
  RenameTopic(topicID: string, title: string): Promise<void>;
  DeleteTopic(topicID: string): Promise<void>;
  TrashTopic(topicID: string): Promise<void>;
  TrashExpertSession(teamID: string): Promise<void>;
  // New native-feel bindings (added with the desktop native-feel plan).
  ConfirmAction(req: NativeConfirmRequest): Promise<boolean>;
  SaveWindowState(state: DesktopWindowState): Promise<void>;
  // --- Scheduled tasks (coWork automation panel) ---------------------------
  // Backed by desktop/scheduler_app.go. The UI re-lists on the
  // "scheduler:changed" event (onSchedulerChanged) so cards stay live without
  // each component polling. "scheduler:notice" (onSchedulerNotice) carries a
  // fired task's {name, result} for an in-app toast.
  ListScheduledTasks(): Promise<TaskView[]>;
  CreateScheduledTask(input: TaskInput): Promise<TaskView>;
  UpdateScheduledTask(input: TaskInput): Promise<TaskView>;
  DeleteScheduledTask(id: string): Promise<void>;
  PauseScheduledTask(id: string): Promise<void>;
  ResumeScheduledTask(id: string): Promise<void>;
  RunScheduledTaskNow(id: string): Promise<string>;
  ScheduledTaskHistory(taskID: string): Promise<RunRecordView[]>;
  ScheduledTaskTemplates(): Promise<TemplateView[]>;
  PreviewSchedule(text: string): Promise<SchedulePreview>;
  // SmartParseSchedule is the on-demand LLM time parser (迅捷任务模型), called
  // ONLY when the user clicks the "🔍 智能解析" button — never during typing.
  // It resolves phrases the regex can't ("下下周五下午3点") into a one-shot time.
  SmartParseSchedule(text: string): Promise<SchedulePreview>;
  // --- Calendar (coWork calendar panel) ------------------------------------
  // Backed by desktop/calendar_app.go. The UI re-lists on the
  // "calendar:changed" event (onCalendarChanged).
  ListCalendarEvents(since: string, before: string): Promise<CalendarEventView[]>;
  ListScheduledTasksAsEvents(since: string, before: string): Promise<CalendarEventView[]>;
  CreateCalendarEvent(input: CalendarEventInput): Promise<CalendarEventView>;
  UpdateCalendarEvent(input: CalendarEventInput): Promise<CalendarEventView>;
  DeleteCalendarEvent(id: string): Promise<void>;
  SearchCalendarEvents(q: string, limit: number): Promise<CalendarEventView[]>;
  ExportCalendarEvents(path: string): Promise<string>;
  ImportCalendarEvents(path: string): Promise<string>;
  // ExportCalendarDialog / ImportCalendarDialog open a native file dialog, then
  // export/import. Return "" when the user cancels the dialog.
  ExportCalendarDialog(): Promise<string>;
  ImportCalendarDialog(): Promise<string>;
  GetChineseHolidays(year: number): Promise<CalendarEventView[]>;
  // --- RAG knowledge base (coWork RAG panel) -------------------------------
  // Backed by desktop/rag_app.go. The panel re-fetches the tree on the
  // "rag:changed" event (onRagChanged) and updates per-node progress bars on
  // "rag:progress" (onRagProgress).
  ListRagCollections(): Promise<RagCollectionView[]>;
  ListRagTree(collection: string): Promise<RagNodeView[]>;
  RagImportPaths(collection: string, paths: string[]): Promise<RagImportResult>;
  RagStartExtract(collection: string, template: string, mode: string): Promise<void>;
  RagExtractResult(collection: string): Promise<RagExtractResultView>;
  RagCancelExtract(jobId: string): Promise<void>;
  RagRemovePath(collection: string, path: string): Promise<void>;
  RagClear(collection: string): Promise<void>;
  RagCleanCollection(collection: string): Promise<void>;
  RagSearch(collection: string, query: string, topK: number): Promise<RagSearchHitView>;
  RagSemanticSearch(collection: string, query: string, topK: number): Promise<RagSearchHitView>;
  RagEmbedEntities(collection: string): Promise<void>;
  RagDetectCommunities(collection: string): Promise<void>;
  RagSummarize(collection: string): Promise<{ summary: string; themes: string[] }>;
  RagAsk(collection: string, question: string): Promise<string>;
  RagPreviewETA(jobId: string): Promise<RagETAView>;
  RagListTemplates(): Promise<string[]>;
  HEHealth(): Promise<{ running: boolean; ready: boolean; port: number }>;
  RagListHETemplates(): Promise<Array<{ name: string; displayName: string; description: string; category: string; available: boolean; templateType: string; entityFields: Array<{ name: string; description: string }>; relationFields: Array<{ name: string; description: string }> }>>;
  // Graph / Entity detail / Edit / Merge / KnowledgeRef / Obsidian
  GetGraphData(collection: string): Promise<GraphDataView>;
  GetTopEntities(collection: string, limit: number): Promise<GraphDataView>;
  GetGraphDataPaged(collection: string, offset: number, limit: number, types: string[]): Promise<GraphDataView>;
  GetEntityDetail(collection: string, name: string): Promise<EntityDetailView>;
  UpdateEntity(collection: string, name: string, patch: EntityPatch): Promise<void>;
  MergeEntities(collection: string, keepName: string, mergeNames: string[]): Promise<void>;
  RagFindMergeCandidates(collection: string): Promise<Array<{ keepName: string; mergeName: string; keepRaw: string; mergeRaw: string; score: number }>>;
  GetDocumentPreview(collection: string, docPath: string): Promise<DocPreviewView>;
  WriteKnowledgeRef(collection: string, entityNames: string[], relationKeys: string[]): Promise<string>;
  RunSkillWithKnowledge(skillName: string, refPath: string): Promise<void>;
  ExportObsidian(collection: string, outputDir: string): Promise<void>;
  SetSessionCollections(collections: string[]): Promise<void>;
  GetSessionCollections(): Promise<string[]>;
  RagFeedText(collection: string, label: string, text: string): Promise<void>;
  RagBatchImport(collection: string, paths: string[]): Promise<RagImportResult>;
  RagBatchExtract(collection: string): Promise<void>;
  // --- Expert team (multi-model collaboration) -----------------------------
  // Backed by desktop/experts_app.go. The panel subscribes to "experts:collab"
  // (onExpertsCollab) for streamed expert outputs and "experts:changed"
  // (onExpertsChanged) for team-list refresh.
  ListExpertTeams(): Promise<TeamView[]>;
  CreateExpertTeam(team: TeamView): Promise<TeamView>;
  UpdateExpertTeam(team: TeamView): Promise<TeamView>;
  DeleteExpertTeam(id: string): Promise<void>;
  RunExpertTeam(teamId: string, task: string, mode: string, rounds: number): Promise<string>;
  GetActiveExpertRun(teamId: string): Promise<ExpertRunView>;
  DeleteExpertCollab(tabId: string, ordinal: number): Promise<HistoryMessage[]>;
  StartScreenshotHotkey(): Promise<void>;
  StopScreenshotHotkey(): Promise<void>;
  StartEStopHotkey(): Promise<void>;
  StopEStopHotkey(): Promise<void>;
  RagCreateCollection(name: string): Promise<void>;
  RagDeleteCollection(name: string): Promise<void>;
  RagRenameCollection(oldName: string, newName: string): Promise<void>;
  SetDesktopMetrics(enabled: boolean): Promise<void>;
  SetPlannerModel(model: string): Promise<void>;
  // PPT-reference VLM gate (desktop/classify_reference.go etc.). Currently
  // invoked Go-side / not yet called by the frontend UI; declared for Wails
  // binding parity (_CheckGenToApp) so the generated bindings stay in check.
  AnalyzePDFPages(pdfPath: string): Promise<number>;
  AnalyzeReferenceImage(imgPath: string): Promise<void>;
  ClassifyReferenceVisual(filePath: string): Promise<{ is_visual: boolean; verdict: string; reason: string }>;
  PreparePPTReference(filePath: string): Promise<{
    is_visual: boolean;
    verdict: string;
    reason: string;
    pdf_pages?: number;
    vision_error?: string;
    needs_vlm_config?: boolean;
  }>;
  RenameTabForMobile(tabID: string, title: string): Promise<void>;
}

// Compile-time drift check. Exclude<A, B> extracts keys in A that are missing
// from B. If that set is non-empty, AssertNever<non-never> fails with
// "Type 'X' does not satisfy the constraint 'never'".
// _CheckGenToApp errors mean a generated Go method has no TS counterpart.
// These compare method *names* only; full signature checking isn't possible here
// because local types (types.ts) use plain interfaces while generated types
// (models.ts) use classes with a convertValues prototype method. The structural
// mismatch would produce false positives. Method-arity and parameter-order drift
// are caught at the call sites by tsc when components invoke app.<method>(...).
type AssertNever<T extends never> = T;
export type _CheckGenToApp = AssertNever<Exclude<keyof typeof GeneratedApp, keyof AppBindings>>;

interface WailsRuntime {
  EventsOn(name: string, cb: (...data: unknown[]) => void): () => void;
  BrowserOpenURL(url: string): void;
  // Native OS file drop (desktop only); useDropTarget gates delivery to elements
  // carrying the --wails-drop-target CSS property. Absent in the browser dev mock.
  OnFileDrop?(cb: (x: number, y: number, paths: string[]) => void, useDropTarget: boolean): void;
  OnFileDropOff?(): void;
}

declare global {
  interface Window {
    runtime?: WailsRuntime;
    go?: { main?: { App?: AppBindings } };
  }
}

// Must match desktop/app.go's eventChannel constant.
const EVENT_CHANNEL = "agent:event";

// Resolve the Wails binding at CALL time, not module-load time: in dev the Wails
// runtime can inject window.go AFTER this module first evaluates, so snapshotting
// once would pin the browser mock for the whole session (and show fake data — the
// dev mock's model list leaking into the real app was exactly this bug).
function realApp(): AppBindings | undefined {
  return typeof window !== "undefined" ? window.go?.main?.App : undefined;
}

let mockSingleton: AppBindings | null = null;
function getMock(): AppBindings {
  if (!mockSingleton) mockSingleton = makeMockApp();
  return mockSingleton;
}

// onTeamChanged subscribes to 团队 mutations; the payload is the refreshed team
// (or {id, deleted:"1"} on removal). The panel uses it to re-render without a
// follow-up round-trip.
export function onTeamChanged(cb: (tv: Partial<TeamProjectView> & { id?: string; deleted?: string }) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("team:changed", (...data: unknown[]) => {
      cb((data?.[0] ?? {}) as Partial<TeamProjectView> & { id?: string; deleted?: string });
    });
  }
  mockTeamListeners.add(cb);
  return () => mockTeamListeners.delete(cb);
}

// subscribeMockTeamEvents fans a team mutation out to the mock listeners.
function emitMockTeam(tv: Partial<TeamProjectView> & { id?: string; deleted?: string }) {
  mockTeamListeners.forEach((l) => l(tv));
}

// onEvent subscribes to the agent's typed event stream; returns an unsubscribe.
// Loop status stream (real: wails "loop:round" event; mock: simulator).
export function onLoopStatus(cb: (s: import("./types").LoopRunStatus) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("loop:round", (payload: unknown) => cb(payload as import("./types").LoopRunStatus));
  }
  mockLoopListeners.add(cb);
  return () => mockLoopListeners.delete(cb);
}

// onRemoteStatus subscribes to remote-host connection changes.
export function onRemoteStatus(cb: (s: { kind: string; target: string; state: string }) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("remote:status", (payload: unknown) => cb(payload as { kind: string; target: string; state: string }));
  }
  return () => {};
}

export function onEvent(cb: (e: WireEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn(EVENT_CHANNEL, (payload) => cb(payload as WireEvent));
  }
  return mockSubscribe(cb);
}

// onUpdaterProgress subscribes to the auto-updater's progress events (a separate
// channel from the agent stream); returns an unsubscribe. Must match the event
// name emitted in desktop/updater_app.go.
export function onUpdaterProgress(cb: (p: UpdateProgress) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("updater:progress", (p) => cb(p as UpdateProgress));
  }
  updaterListeners.add(cb);
  return () => {
    updaterListeners.delete(cb);
  };
}

// onNetdevLive subscribes to the 操作实况 batch stream ("netdev:live" —
// desktop/netdev_app.go coalesces ~40ms batches of NetDevLiveEvent). In the
// browser dev mock a demo sequence plays so the panel is testable standalone.
const netdevLiveListeners = new Set<(events: NetDevLiveEvent[]) => void>();
export function onNetdevLive(cb: (events: NetDevLiveEvent[]) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("netdev:live", (batch) => cb(batch as NetDevLiveEvent[]));
  }
  netdevLiveListeners.add(cb);
  return () => {
    netdevLiveListeners.delete(cb);
  };
}

function emitNetdevLiveMock(events: NetDevLiveEvent[]) {
  for (const cb of netdevLiveListeners) cb(events);
}

// onNetdevHumanTTY subscribes to the human-terminal output stream
// ("netdev:humantty": {device, chunk} — desktop/netdev_app.go forwards the
// Go-side PTY tap). Device tabs filter by device name.
export interface NetDevHumanTTYChunk {
  device: string;
  chunk: string;
}

const netdevHumanTTYListeners = new Set<(ev: NetDevHumanTTYChunk) => void>();

export function onNetdevHumanTTY(cb: (ev: NetDevHumanTTYChunk) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("netdev:humantty", (ev) => cb(ev as NetDevHumanTTYChunk));
  }
  netdevHumanTTYListeners.add(cb);
  return () => {
    netdevHumanTTYListeners.delete(cb);
  };
}

export function emitNetdevHumanTTYMock(ev: NetDevHumanTTYChunk) {
  for (const cb of netdevHumanTTYListeners) cb(ev);
}

// onNetdevLogFollow subscribes to the streaming log-follow channel
// ("netdev:logfollow": chunks + the terminal done event with the stop reason).
export function onNetdevLogFollow(cb: (ev: NetDevLogFollowEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("netdev:logfollow", (ev) => cb(ev as NetDevLogFollowEvent));
  }
  netdevLogFollowListeners.add(cb);
  return () => {
    netdevLogFollowListeners.delete(cb);
  };
}

const netdevLogFollowListeners = new Set<(ev: NetDevLogFollowEvent) => void>();

export function emitNetdevLogFollowMock(ev: NetDevLogFollowEvent) {
  for (const cb of netdevLogFollowListeners) cb(ev);
}

// onBrowserMirror subscribes to the browser-mirror frame stream
// ("browser:mirror": lifecycle status + post-action screenshots from the
// browser tools and browser_auto — desktop/app.go forwards the kernel sink).
export function onBrowserMirror(cb: (f: import("./types").BrowserMirrorFrame) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("browser:mirror", (f) => cb(f as import("./types").BrowserMirrorFrame));
  }
  browserMirrorListeners.add(cb);
  return () => {
    browserMirrorListeners.delete(cb);
  };
}

const browserMirrorListeners = new Set<(f: import("./types").BrowserMirrorFrame) => void>();

export function emitBrowserMirrorMock(f: import("./types").BrowserMirrorFrame) {
  for (const cb of browserMirrorListeners) cb(f);
}

// onBrowserRecord subscribes to the browser console's live recording stream
// ("browser:record": one event per captured click/input/navigation).
export function onBrowserRecord(cb: (ev: import("./types").BrowserConsoleRecordEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("browser:record", (ev) => cb(ev as import("./types").BrowserConsoleRecordEvent));
  }
  browserRecordListeners.add(cb);
  return () => {
    browserRecordListeners.delete(cb);
  };
}

const browserRecordListeners = new Set<(ev: import("./types").BrowserConsoleRecordEvent) => void>();

export function emitBrowserRecordMock(ev: import("./types").BrowserConsoleRecordEvent) {
  for (const cb of browserRecordListeners) cb(ev);
}

// onBrowserTrial subscribes to the skill editor's trial-run progress
// ("browser:trial": per-step running/done/failed; index -1 is terminal).
export function onBrowserTrial(cb: (st: import("./types").BrowserConsoleTrialStatus) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("browser:trial", (st) => cb(st as import("./types").BrowserConsoleTrialStatus));
  }
  browserTrialListeners.add(cb);
  return () => {
    browserTrialListeners.delete(cb);
  };
}

const browserTrialListeners = new Set<(st: import("./types").BrowserConsoleTrialStatus) => void>();

export function emitBrowserTrialMock(st: import("./types").BrowserConsoleTrialStatus) {
  for (const cb of browserTrialListeners) cb(st);
}

// Mock human-breakpoint gate: the dev-mode TrialRun parks on a promise that
// Resume/Abort resolves, mirroring the Go-side consoleTrialGate channels. The
// resume verdict carries the ask step's reply text.
type MockTrialVerdict = { kind: "resume"; reply: string } | { kind: "abort" };

let mockTrialResolve: ((verdict: MockTrialVerdict) => void) | null = null;

function mockTrialGate(): Promise<MockTrialVerdict> {
  return new Promise((resolve) => {
    mockTrialResolve = resolve;
  });
}

function mockTrialSignal(verdict: MockTrialVerdict) {
  mockTrialResolve?.(verdict);
  mockTrialResolve = null;
}

// onNetdevHealth subscribes to health change events ("netdev:health": one
// device's reachability/interface state changed since the previous poll).
// fairpeer:// 深链热路径：第二实例唤起（IM/邮件里点链接）时 Go 侧 EventsEmit。
export function onFairpeerDeepLink(cb: (r: { kind: string; id: string }) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("fairpeer:deep-link", (r) => cb(r as { kind: string; id: string }));
  }
  return () => {};
}

export function onNetdevHealth(cb: (h: NetDevDeviceHealth) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("netdev:health", (h) => cb(h as NetDevDeviceHealth));
  }
  netdevHealthListeners.add(cb);
  return () => {
    netdevHealthListeners.delete(cb);
  };
}

const netdevHealthListeners = new Set<(h: NetDevDeviceHealth) => void>();

// playNetdevLiveMockDemo feeds a scripted command lifecycle + a guardrail
// refusal into the mock live stream so the 操作实况 panel demos standalone.
// Once per page load: the panel's effect can run several times (React
// StrictMode double-mount, dock remounts) and each NetDevLiveSnapshot call
// schedules a play — unguarded, the folds stack into visibly duplicated
// tail/chips/guardrail rows.
let netdevLiveDemoPlayed = false;
function playNetdevLiveMockDemo() {
  if (netdevLiveDemoPlayed) return;
  netdevLiveDemoPlayed = true;
  const t = () => Date.now();
  const dev = "core-sw-1";
  const script: [number, NetDevLiveEvent[]][] = [
    [0, [{ kind: "cmd_start", device: dev, command: "display ospf peer", class: "read", time: t() }]],
    [300, [{ kind: "cmd_output", device: dev, chunk: " Area 0.0.0.0 neighbors\n", time: t() }]],
    [600, [{ kind: "cmd_output", device: dev, chunk: " RouterID       State   DeadTime  Interface\n 10.0.0.2       Full    32s       GE0/0/1\n 10.0.0.3       Full    35s       GE0/0/2\n", time: t() }]],
    [900, [
      { kind: "cmd_output", device: dev, chunk: "<HUAWEI>", time: t() },
      { kind: "cmd_end", device: dev, command: "display ospf peer", class: "read", status: "ok", ms: 880, bytes: 168, time: t() },
    ]],
    [1400, [{ kind: "cmd_refused", device: dev, command: "save", class: "write", time: t(), reason: "写命令——运维会话结构性只读；变更走人工审批的提案。" }]],
  ];
  for (const [delay, events] of script) {
    window.setTimeout(() => emitNetdevLiveMock(events), delay);
  }
}

// onFilesDropped subscribes to native OS file drops landing on the composer (the
// --wails-drop-target element); the callback gets the dropped files' absolute
// paths. No-op in the browser dev mock, where the runtime is absent.
export function onFilesDropped(cb: (paths: string[]) => void): () => void {
  const rt = typeof window !== "undefined" ? window.runtime : undefined;
  if (!rt?.OnFileDrop) return () => {};

  // Wails' internal ResolveFilePaths throws when a non-file object (e.g. the
  // window icon) is dragged onto the webview. The error is uncaught and crashes
  // the app. Intercept it here so only real file drops reach the callback.
  const suppressNonFileDragError = (e: ErrorEvent) => {
    if (e.message?.includes("additional File object is not a file on the disk")) {
      e.preventDefault();
    }
  };
  const suppressNonFileDragRejection = (e: PromiseRejectionEvent) => {
    const msg = e.reason?.message ?? String(e.reason);
    if (msg.includes("additional File object is not a file on the disk")) {
      e.preventDefault();
    }
  };
  window.addEventListener("error", suppressNonFileDragError);
  window.addEventListener("unhandledrejection", suppressNonFileDragRejection);

  rt.OnFileDrop((_x, _y, paths) => {
    if (Array.isArray(paths) && paths.length > 0) cb(paths);
  }, true);
  return () => {
    rt.OnFileDropOff?.();
    window.removeEventListener("error", suppressNonFileDragError);
    window.removeEventListener("unhandledrejection", suppressNonFileDragRejection);
  };
}

// onReady subscribes to the agent:ready event fired when boot.Build completes.
// The callback receives the tab whose controller finished building (undefined
// for older backends that emit without a payload) — the frontend re-fetches
// that tab's Meta/Context/History when this lands.
export function onReady(cb: (tabId?: string) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("agent:ready", (...data: unknown[]) =>
      cb((data[0] as { tabId?: string } | undefined)?.tabId),
    );
  }
  // Mock fallback: fire once on subscribe (as before) AND on subsequent mock
  // agent:ready emissions (e.g. after a mock SwitchProfile), so the dev shell
  // reloads session data the same way the real app does.
  cb();
  const wrapped = (payload: unknown) => cb((payload as { tabId?: string } | undefined)?.tabId);
  (mockEventListeners["agent:ready"] ??= []).push(wrapped);
  return () => {
    const arr = mockEventListeners["agent:ready"] ?? [];
    mockEventListeners["agent:ready"] = arr.filter((x) => x !== wrapped);
  };
}

// Mock-mode event bus: when running without the Go backend (npm run dev),
// onProjectTreeChanged/onProfileChanged register here and the mock App methods
// emit through emitMockEvent. This lets the dev shell exercise profile-switch
// UI flows (sidebar refresh, message clear) that otherwise only fire via Wails.
const mockEventListeners: Record<string, Array<(payload: unknown) => void>> = {};
export function emitMockEvent(name: string, payload?: unknown): void {
  (mockEventListeners[name] ?? []).forEach((cb) => cb(payload));
}

export function onProjectTreeChanged(cb: () => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("project-tree:changed", () => cb());
  }
  // Mock fallback so the dev shell refreshes the sidebar on profile switch.
  (mockEventListeners["project-tree:changed"] ??= []).push(cb);
  return () => {
    const arr = mockEventListeners["project-tree:changed"] ?? [];
    mockEventListeners["project-tree:changed"] = arr.filter((x) => x !== cb);
  };
}

// onProfileChanged fires when a tab's product profile (dev/cowork) changes after
// a SwitchProfile rebuild. The payload carries {tabId, profile}; cb receives it
// so the layout can swap for the affected tab only.
export function onProfileChanged(cb: (e: { tabId: string; profile: string }) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("profile:changed", (...data: unknown[]) => {
      const e = (data?.[0] ?? {}) as { tabId?: string; profile?: string };
      cb({ tabId: e.tabId ?? "", profile: e.profile ?? "dev" });
    });
  }
  // Mock fallback so the dev shell clears messages + refreshes the sidebar on
  // profile switch (matching the real Wails event-driven flow).
  const wrapped = (payload: unknown) => {
    const e = (payload ?? {}) as { tabId?: string; profile?: string };
    cb({ tabId: e.tabId ?? "", profile: e.profile ?? "dev" });
  };
  (mockEventListeners["profile:changed"] ??= []).push(wrapped);
  return () => {
    const arr = mockEventListeners["profile:changed"] ?? [];
    mockEventListeners["profile:changed"] = arr.filter((x) => x !== wrapped);
  };
}

// onSchedulerChanged fires when the scheduled-task list mutates (create/update/
// delete/run). Payload-free — the automation panel re-lists on this event to
// keep cards live without polling.
export function onSchedulerChanged(cb: () => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("scheduler:changed", () => cb());
  }
  return () => {};
}

// onSchedulerNotice fires when a task with OutputMode="notify" runs (in-app
// desktop toast). Payload: {name, result}. The toast layer subscribes once at
// app root so notices surface even when the user isn't on the automation tab.
export function onSchedulerNotice(cb: (e: { name: string; result: string }) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("scheduler:notice", (...data: unknown[]) => {
      const e = (data?.[0] ?? {}) as { name?: string; result?: string };
      cb({ name: e.name ?? "", result: e.result ?? "" });
    });
  }
  return () => {};
}

// onCalendarChanged fires when the calendar event list mutates (create/update/
// delete). Payload-free — the calendar panel re-lists on this event.
export function onCalendarChanged(cb: () => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("calendar:changed", () => cb());
  }
  return () => {};
}

// onRagChanged fires when the RAG tree/collections mutate (import/remove/status
// change). Payload-free — the panel re-fetches the tree.
export function onRagChanged(cb: () => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("rag:changed", () => cb());
  }
  return () => {};
}

// onRagProgress fires on each chunk extraction completion. Payload is a
// RagProgressEvent; the panel updates the matching tree node's progress bar.
export function onRagProgress(cb: (e: RagProgressEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("rag:progress", (...data: unknown[]) => {
      const e = (data?.[0] ?? {}) as Partial<RagProgressEvent>;
      cb({
        jobId: e.jobId ?? "",
        collection: e.collection ?? "",
        path: e.path ?? "",
        status: e.status ?? "",
        doneChunks: e.doneChunks ?? 0,
        totalChunks: e.totalChunks ?? 0,
        avgLatencyMs: e.avgLatencyMs ?? 0,
        message: e.message ?? "",
      });
    });
  }
  return () => {};
}

// onRagRunSkill fires when the user selects a skill from the knowledge-ref panel.
// Payload: { skill, arguments, refPath }. The chat should invoke the skill.
export function onRagRunSkill(cb: (e: { skill: string; arguments: string; refPath: string }) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("rag:run-skill", (...data: unknown[]) => {
      const e = (data?.[0] ?? {}) as Record<string, unknown>;
      cb({
        skill: String(e.skill ?? ""),
        arguments: String(e.arguments ?? ""),
        refPath: String(e.refPath ?? ""),
      });
    });
  }
  return () => {};
}

// onExpertsCollab fires during an expert-team run (expert chunks, synthesis,
// completion). Payload is a CollabEvent; the panel appends text deltas.
export function onExpertsCollab(cb: (e: CollabEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("experts:collab", (...data: unknown[]) => {
      const e = (data?.[0] ?? {}) as Partial<CollabEvent>;
      cb({
        runId: e.runId ?? "",
        teamId: e.teamId ?? "",
        teamName: e.teamName ?? "",
        phase: e.phase ?? "",
        expertIdx: e.expertIdx ?? 0,
        expertName: e.expertName ?? "",
        round: e.round ?? 0,
        text: e.text ?? "",
        message: e.message ?? "",
        mode: e.mode ?? "",
      });
    });
  }
  return () => {};
}

// onExpertsChanged fires when the team list mutates. Payload-free — re-list.
export function onExpertsChanged(cb: () => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("experts:changed", () => cb());
  }
  return () => {};
}


// outside the shell), so a late-injected window.go is picked up transparently.
export const app: AppBindings = new Proxy({} as AppBindings, {
  get(_t, prop) {
    const target = realApp() ?? getMock();
    const v = (target as unknown as Record<string, unknown>)[String(prop)];
    return typeof v === "function" ? (v as (...a: unknown[]) => unknown).bind(target) : v;
  },
});

// openExternal opens a URL in the system browser (so links in rendered markdown
// don't navigate the webview away from the app). Falls back to window.open in the
// browser dev mock.
export function openExternal(url: string): void {
  if (typeof window !== "undefined" && window.runtime?.BrowserOpenURL) {
    window.runtime.BrowserOpenURL(url);
  } else if (typeof window !== "undefined") {
    window.open(url, "_blank", "noopener");
  }
}

// --- browser dev mock --------------------------------------------------------

const listeners = new Set<(e: WireEvent) => void>();
// mockTeamListeners backs onTeamChanged in the browser dev seam (the Wails
// runtime event is unavailable outside the shell).
const mockTeamListeners = new Set<(tv: Partial<TeamProjectView> & { id?: string; deleted?: string }) => void>();
let mockScopedTabId: string | undefined;

function mockSubscribe(cb: (e: WireEvent) => void): () => void {
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
  };
}

function emit(e: WireEvent) {
  const event = mockScopedTabId && !e.tabId ? { ...e, tabId: mockScopedTabId } : e;
  listeners.forEach((l) => l(event));
}

async function withMockTabScope<T>(tabId: string, fn: () => Promise<T>): Promise<T> {
  const previous = mockScopedTabId;
  mockScopedTabId = tabId || previous;
  try {
    return await fn();
  } finally {
    mockScopedTabId = previous;
  }
}

// Updater progress has its own listener set so the browser dev mock's ApplyUpdate
// can stream a fake download through onUpdaterProgress.
const updaterListeners = new Set<(p: UpdateProgress) => void>();

function emitUpdater(p: UpdateProgress) {
  updaterListeners.forEach((l) => l(p));
}

function delay(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

// ── Loop Engineering mock simulator ──────────────────────────────────────────
// mockLoopState holds the simulated run; mockLoopTick advances it one round per
// ~1.6s and notifies subscribers on a dedicated channel (polled via
// LoopStatus by the panel).

let mockLoopState: import("./types").LoopRunStatus | null = null;
const mockLoopListeners = new Set<(s: import("./types").LoopRunStatus) => void>();

function emitMockLoop() {
  if (mockLoopState) mockLoopListeners.forEach((l) => l(structuredClone(mockLoopState!)));
}

function mockLoopTick() {
  if (!mockLoopState || mockLoopState.state !== "running") return;
  const maxRounds = mockLoopState.config.maxRounds || 3;
  const round = mockLoopState.round + 1;
  const outcomes: Array<"pass" | "fail-rolled-back" | "skipped"> =
    mockLoopState.config.autonomy === "L1"
      ? ["skipped", "skipped", "skipped"]
      : ["fail-rolled-back", "pass", "pass"];
  const verify = outcomes[(round - 1) % outcomes.length];
  mockLoopState.round = round;
  mockLoopState.timeline.push({
    round,
    verify,
    changed: verify === "skipped" ? [] : ["src/utils/date.ts", "tests/date.spec.ts"],
    note: verify === "fail-rolled-back" ? "验证失败,已回滚(mock)" : verify === "pass" ? "修复通过(mock)" : "L1 只读轮次",
    durationMs: 3200,
  });
  const terminal =
    round >= Math.min(maxRounds, 3) ||
    (!mockLoopState.config.exploratory && verify === "pass") ||
    mockLoopState.timeline.filter((r) => r.verify === "fail-rolled-back").length >= 3;
  if (terminal) {
    const passed = mockLoopState.timeline.filter((r) => r.verify === "pass").length;
    const rolled = mockLoopState.timeline.filter((r) => r.verify === "fail-rolled-back").length;
    mockLoopState.state = "done";
    mockLoopState.stopNote = mockLoopState.config.exploratory ? "问题队列清空(mock)" : "验收通过(mock)";
    mockLoopState.endedAt = Date.now();
    mockLoopState.report = {
      roundsRun: round,
      passed,
      rolledBack: rolled,
      skipped: mockLoopState.timeline.filter((r) => r.verify === "skipped").length,
      changedFiles: 2,
      lastVerify: "通过",
      headline: `完成(mock):${round} 轮 · 通过 ${passed} · 回滚 ${rolled}`,
      suggestion: "这是浏览器 dev 模拟;真机由 Go 引擎驱动",
    };
    emitMockLoop();
    return;
  }
  emitMockLoop();
  setTimeout(mockLoopTick, 1600);
}

function baseName(path: string): string {
  return path.replace(/[/\\]+$/, "").split(/[/\\]/).filter(Boolean).pop() ?? path;
}

function browserPlatformOverride(): "darwin" | "windows" | "linux" | "" {
  if (typeof window === "undefined" || window.runtime) return "";
  const value = new URLSearchParams(window.location.search).get("platform");
  return value === "darwin" || value === "windows" || value === "linux" ? value : "";
}

function mockScenario(): "demo" | "fresh" | "running" {
  if (typeof window === "undefined") return "demo";
  const value = new URLSearchParams(window.location.search).get("mock")?.trim().toLowerCase();
  if (value === "fresh" || value === "empty" || value === "first-run") return "fresh";
  if (value === "running" || value === "busy" || value === "streaming") return "running";
  return "demo";
}

// mockInitialProfile: ?profile=netdev|cowork boots the dev mock straight into
// that mode (the active tab carries the profile) — GUI smoke tests for the
// mode layouts don't have to drive the header switcher first.
function mockInitialProfile(): "dev" | "cowork" | "netdev" {
  if (typeof window === "undefined") return "dev";
  const value = new URLSearchParams(window.location.search).get("profile")?.trim().toLowerCase();
  return value === "cowork" || value === "netdev" ? value : "dev";
}

// ── 团队 (team) mock store ────────────────────────────────────────────────────
// A small in-memory implementation so the kanban / roster / natural-language
// drafting flows are demoable in `pnpm dev` without the Go backend. It mirrors
// the server's derived-column rule (status + assignee + deps).

let mockTeams: TeamProjectView[] = [];
let mockTeamSeq = 0;
// Keys "teamId/taskId" with a simulated in-flight member run.
const mockRunningTasks = new Set<string>();

function mockTeamID(prefix: string): string {
  mockTeamSeq += 1;
  return `${prefix}_${Date.now()}_${mockTeamSeq}`;
}

function mockEmptyContext(goal = ""): TeamContextView {
  return { goal, constraints: "", decisions: [], artifacts: [], openQuestions: [], version: 1 };
}

function mockPolicy(): TeamPolicyView {
  return { maxRounds: 12, maxParallel: 3, autoAssign: true, autoReplan: false };
}

// emptyMockTask is the zero card the team mock mutates into real cards.
function emptyMockTask(): TeamTaskView {
  return {
    id: "", title: "", desc: "", assigneeId: "", assigneeName: "", requiredSkills: [],
    status: "queued", column: "backlog", deps: [], parentId: "", acceptance: [],
    deliverable: "", attempts: 0, evidence: [], progress: "", error: "", order: 0,
  };
}

function mockColumnOf(team: TeamProjectView, task: TeamTaskView): string {
  switch (task.status) {
    case "running": return "doing";
    case "waiting": return "blocked";
    case "succeeded": return "done";
    case "failed":
    case "stale": return "failed";
    case "cancelled": return "cancelled";
    default: {
      if (!task.assigneeId) return "backlog";
      const deps = task.deps ?? [];
      const ok = deps.every((d) => team.tasks.find((x) => x.id === d)?.status === "succeeded");
      return ok ? "ready" : "blocked";
    }
  }
}

function mockBoardOf(team: TeamProjectView): TeamBoardView {
  const order = ["backlog", "ready", "doing", "blocked", "done", "failed", "cancelled"];
  const labels: Record<string, string> = {
    backlog: "待分配", ready: "待开始", doing: "进行中", blocked: "阻塞",
    done: "已完成", failed: "失败", cancelled: "已取消",
  };
  const counts: Record<string, number> = {};
  const columns: TeamColumnView[] = order.map((key) => ({ key, label: labels[key], states: [], tasks: [] }));
  const byKey: Record<string, TeamColumnView> = {};
  columns.forEach((c) => { byKey[c.key] = c; });
  team.tasks.forEach((tk) => {
    const col = mockColumnOf(team, tk);
    const withCol: TeamTaskView = {
      ...tk,
      column: col,
      assigneeName: team.members.find((m) => m.id === tk.assigneeId)?.name ?? "",
    };
    (byKey[col] ?? byKey.backlog).tasks.push(withCol);
    counts[col] = (counts[col] ?? 0) + 1;
  });
  return { teamId: team.id, columns, counts, total: team.tasks.length };
}

function mockFindTeam(id: string): TeamProjectView | undefined {
  return mockTeams.find((x) => x.id === id);
}

// mockPatchTeam applies a mutator to one team, persists it and emits team:changed.
function mockPatchTeam(id: string, mut: (tm: TeamProjectView) => TeamProjectView): TeamProjectView {
  const idx = mockTeams.findIndex((x) => x.id === id);
  if (idx < 0) throw new Error("团队不存在");
  const next = mut({ ...mockTeams[idx] });
  next.updatedAt = new Date().toISOString();
  mockTeams = mockTeams.map((x, i) => (i === idx ? next : x));
  emitMockTeam(next);
  return { ...next };
}

// mockDraftMemberFrom stands in for the LLM draft so the confirm-then-create
// flow is exercisable offline: it derives a plausible name/skills from the text.
function mockDraftMemberFrom(instruction: string): TeamMemberView {
  const text = instruction.trim();
  const hints: Array<[string, string[]]> = [
    ["后端", ["backend", "go", "sql"]],
    ["前端", ["frontend", "react"]],
    ["测试", ["testing", "api"]],
    ["调研", ["research", "analysis"]],
    ["文档", ["docs", "writing"]],
    ["设计", ["design", "ux"]],
    ["运维", ["ops", "linux"]],
    ["数据", ["data", "sql"]],
  ];
  let skills = ["general"];
  let avatar = "🙂";
  for (const [hint, s] of hints) {
    if (text.includes(hint)) { skills = s; avatar = "🛠"; break; }
  }
  const name = text.replace(/^(帮我|请|麻烦)?(新增|加|添加|创建|要)?(一个|个)?/, "").slice(0, 8) || "新团员";
  return {
    id: "",
    name,
    role: text.slice(0, 40) || "承担指派的任务",
    model: "",
    effort: "",
    skills,
    tools: [],
    systemPrompt: `你就是「${name}」。${text}`,
    isLeader: false,
    avatar,
  };
}

function makeMockApp(): AppBindings {
  const scenario = mockScenario();
  const freshMock = scenario === "fresh";
  const runningMock = scenario === "running";
  let cancelled = false;
  let pendingAskPreview = false;
  let pendingApprovalPreview = false;
  // Global scope retired (08-21): the mock home project 工作台 replaces it.
  const mockHomeRoot = "~/Library/Application Support/fairpeer/home-dev";
  let cwd = freshMock ? mockHomeRoot : "~/projects/web-app"; // mutable so PickWorkspace is visible in dev
  let workspaces = freshMock ? [] : ["~/projects/web-app", "~/projects/api-server", "~/projects/docs", "~/projects/mobile"];
  let mockEffort = "auto";
  // In-memory RAG state for browser dev. Seeded with one file mid-extraction so
  // the panel shows a live progress bar + ETA outside the Wails shell.
  let mockRagDocs = 3;
  let mockRagEntities = 12;
  let mockRagTree: RagNodeView[] = freshMock ? [] : [
    {
      key: "/mock/doc.md", label: "会议纪要.md", kind: "file", path: "/mock/doc.md", relPath: "会议纪要.md",
      isDir: false, collection: "default", status: "extracting", hasFts5: true,
      jobId: "rag_job_mock_demo", doneChunks: 3, totalChunks: 10, entityCount: 0, errorMsg: "",
    },
  ];
  // simulateRagProgress advances a mock node's doneChunks every ~1.5s until done.
  // jobId is kept in the signature for parity with the real backend's events.
  const simulateRagProgress = (_jobId: string, node: RagNodeView) => {
    const h = setInterval(() => {
      if (node.doneChunks >= node.totalChunks) {
        node.status = "enriched"; node.entityCount = Math.floor(Math.random() * 8) + 2;
        mockRagEntities += node.entityCount;
        clearInterval(h);
        return;
      }
      node.doneChunks++;
    }, 1500);
  };
  // In-memory scheduled-task store for browser dev. Seeded with one sample so
  // the automation panel isn't empty outside the Wails shell.
  let mockSchedulerTasks: TaskView[] = freshMock ? [] : [
    {
      id: "sched_mock_demo",
      name: "日报提醒",
      expression: "daily 18:00 Mon-Fri",
      prompt: "请整理今日工作日报，按三段式汇总。",
      profile: "cowork",
      enabled: true,
      oneShot: false,
      lastRun: "2026-06-21 18:00",
      nextRun: "2026-06-22 18:00",
      runCount: 12,
      lastResult: "日报已生成",
      outputMode: "notify",
      outputDest: "",
      outputDir: "",
      humanSchedule: "工作日 18:00",
      source: "manual",
      calendarEventId: "",
      outputAccount: "",
      plain: false,
      lastDeliverErr: "",
      lastDeliverAt: "",
    },
  ];
  const cloneTask = (t: TaskView): TaskView => JSON.parse(JSON.stringify(t)) as TaskView;
  const day = 86_400_000;
  const t0 = Date.now();
  // Mutable so MCP add/remove/retry are observable in browser dev.
  let capServers: ServerView[] = [
    {
      name: "codegraph",
      transport: "stdio",
      status: "disabled",
      builtIn: true,
      configured: true,
      autoStart: false,
      tier: "background",
      tools: 0,
      prompts: 0,
      resources: 0,
      toolList: [
        { name: "search", description: "Search symbols, files, and text in the workspace." },
        { name: "context", description: "Fetch surrounding source context for a symbol or file." },
        { name: "trace", description: "Follow callers and callees across the code graph." },
        { name: "node", description: "Inspect a specific graph node." },
      ],
    },
  ];
  const capSkills: SkillView[] = [
    { name: "explore", description: "Investigate the codebase in an isolated subagent", scope: "builtin", runAs: "subagent", enabled: true },
    { name: "review", description: "Review the staged diff", scope: "project", runAs: "inline", enabled: false },
    { name: "init", description: "Scaffold a project memory doc (fairpeer.md) for this repo", scope: "builtin", runAs: "inline", enabled: true },
  ];
  let capSkillRoots: SkillRootView[] = [
    { dir: "~/projects/docs/.fairpeer/skills", scope: "project", priority: 1, status: "missing", configured: false, removable: true, skills: 0 },
    {
      dir: "~/my-skills",
      scope: "custom",
      priority: 5,
      status: "ok",
      configured: true,
      removable: true,
      skills: 1,
      skillItems: [{ name: "review", description: "Review the staged diff", scope: "custom", runAs: "inline" }],
    },
    {
      dir: "~/.fairpeer/skills",
      scope: "global",
      priority: 6,
      status: "ok",
      configured: false,
      removable: true,
      skills: 2,
      skillItems: [
        { name: "explore", description: "Investigate the codebase in an isolated subagent", scope: "global", runAs: "subagent" },
        { name: "init", description: "Scaffold a project memory doc (fairpeer.md) for this repo", scope: "global", runAs: "inline" },
      ],
    },
  ];
  const mockSwitchWorkspace = async (path: string) => {
    cwd = path || "~";
    workspaces = [cwd, ...workspaces.filter((p) => p !== cwd)].slice(0, 12);
    if (!mockProjectTree.some((node) => node.kind === "project" && node.root === cwd)) {
      mockProjectTree.unshift({
        key: `project_${cwd}`,
        kind: "project",
        label: baseName(cwd),
        root: cwd,
        children: [],
      });
    }
    return cwd;
  };
  // Mutable so delete/rename are observable in browser dev.
  const sessions: SessionMeta[] = [
    { path: "/mock/sessions/a.jsonl", preview: "fix the login bug in auth.go", turns: 12, createdAt: t0 - 2 * day, lastActivityAt: t0 - 3_600_000, modTime: t0 - 3_600_000, current: true, open: true },
    { path: "/mock/sessions/b.jsonl", preview: "refactor the payment module", turns: 5, createdAt: t0 - 3 * day, lastActivityAt: t0 - 6 * 3_600_000, modTime: t0 - 6 * 3_600_000, current: false, open: true },
    { path: "/mock/sessions/c.jsonl", preview: "write the README and badges", turns: 8, createdAt: t0 - 4 * day, lastActivityAt: t0 - day - 3_600_000, modTime: t0 - day - 3_600_000, current: false, open: false },
    { path: "/mock/sessions/d.jsonl", preview: "explain the plugin host design", turns: 3, createdAt: t0 - 5 * day, lastActivityAt: t0 - 4 * day, modTime: t0 - 4 * day, current: false, open: false },
  ];
  const trashedSessions: SessionMeta[] = [
    {
      path: "/mock/sessions/.trash/trash-dev-standard.jsonl",
      title: t("mock.trashDevStandardTitle"),
      preview: t("mock.trashDevStandardPreview"),
      turns: 4,
      createdAt: t0 - 8 * day,
      lastActivityAt: t0 - 7 * day,
      modTime: t0 - 7 * day,
      deletedAt: t0 - 20 * 60_000,
      current: false,
      open: false,
      scope: "project",
      workspaceRoot: "~/projects/web-app",
      topicId: "topic_dev_standard",
      topicTitle: t("mock.trashDevStandardTitle"),
    },
    {
      path: "/mock/sessions/.trash/trash-p3a-review.jsonl",
      title: t("mock.trashP3aTitle"),
      preview: t("mock.trashP3aPreview"),
      turns: 7,
      createdAt: t0 - 6 * day,
      lastActivityAt: t0 - 5 * day,
      modTime: t0 - 5 * day,
      deletedAt: t0 - 2 * 3_600_000,
      current: false,
      open: false,
      scope: "project",
      workspaceRoot: "~/projects/api-server",
      topicId: "topic_p3a_pd",
      topicTitle: t("mock.trashP3aTitle"),
    },
    {
      path: "/mock/sessions/.trash/trash-global-product.jsonl",
      title: t("mock.trashGlobalProductTitle"),
      preview: t("mock.trashGlobalProductPreview"),
      turns: 2,
      createdAt: t0 - 4 * day,
      lastActivityAt: t0 - 3 * day,
      modTime: t0 - 3 * day,
      deletedAt: t0 - day,
      current: false,
      open: false,
      scope: "global",
      topicId: "topic_product",
      topicTitle: t("mock.trashGlobalProductTitle"),
    },
  ];
  if (freshMock) {
    sessions.splice(0);
    trashedSessions.splice(0);
  }
  // Mutable dream/distill status so the Memory panel's self-evolution section is
  // interactive in browser dev mode (no backend).
  const dreamMock: DreamStatusView = {
    enabled: true,
    dreamInterval: 7,
    distillInterval: 30,
    dreamInFlight: false,
    distillInFlight: false,
    history: [],
  };
  // Mutable [memory] settings so the Memory panel's storage/maintenance section
  // is interactive in browser dev mode (no backend, no real config file).
  let mockMemorySettings: MemorySettings = {
    root: "~/.config/fairpeer",
    defaultRoot: "~/.config/fairpeer",
    configuredRoot: "",
    rootFromEnv: false,
    globalDir: "~/.config/fairpeer/memory/dev",
    sessionDir: "~/.config/fairpeer/projects/-mock/dev/memory/session",
    provider: "",
    model: "",
    effort: "",
    ref: "yyqwen/Qwen3.6-35B-A3B-FP8",
    refFromMemory: false,
    injectIndex: true,
    indexMaxChars: 1200,
  };
  // Mutable settings so the Settings panel's edits are observable in browser dev.
  // hookSettings holds the per-scope mock hooks payload (global + project).
  const hookEvents = ["Startup", "PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop", "PostLLMCall", "SessionStart", "SessionEnd", "SubagentStop", "Notification", "PreCompact"];
  const hookSettings: Record<string, HooksSettingsView> = {
    global: {
      scope: "global",
      path: "~/.fairpeer/settings.json",
      projectRoot: "",
      trusted: true,
      events: hookEvents,
      hooks: [],
    },
    project: {
      scope: "project",
      path: "./.fairpeer/settings.json",
      projectRoot: "/mock/project",
      trusted: false,
      events: hookEvents,
      hooks: [],
    },
  };
  // Wallpaper state for the browser dev mock. The real implementation lives in
  // desktop/wallpaper_app.go; here we keep just enough state for the appearance
  // settings to be exercised without the Wails shell (no native file picker).
  let mockWallpaper: WallpaperView = { active: false, url: "", name: "", blur: 14, dim: 40, fit: "cover" };

  const settings: SettingsView = {
    defaultModel: "",
    fastTaskModel: "",
    subagentModel: "",
    subagentEffort: "",
    autoPlan: "off",
    providers: [],
    officialProviders: [],
    permissions: { mode: "ask", allow: ["read_file"], ask: [], deny: ["Bash(rm:*)"] },
    sandbox: { bash: "enforce", network: true, workspaceRoot: "", allowWrite: [] },
    network: {
      proxyMode: "auto",
      proxyUrl: "",
      noProxy: "",
      proxy: { type: "socks5", server: "127.0.0.1", port: 7890, username: "", password: "" },
    },
    agent: { temperature: 0.2, maxSteps: 0, plannerMaxSteps: 0, systemPrompt: "You are fairpeer, a coding agent.", rpm: 60 },
    cowork: {
      browserPath: "",
      browserAttachURL: "",
      embeddingModel: "",
      ragEnabled: null,
      pptActiveTemplate: "",
      pptTemplates: [],
      pptTemplateDir: "",
      pptMode: "fast",
      smtp: { host: "", port: 0, from: "", username: "", passwordEnv: "COWORK_SMTP_PASSWORD", useTLS: false },
      imap: { host: "", port: 0, username: "", passwordEnv: "COWORK_IMAP_PASSWORD" },
      smtpPassword: "",
      imapPassword: "",
      smtpPasswordSet: false,
      imapPasswordSet: false,
      detectedBrowser: "",
      screenshotEnabled: false,
      screenshotHotkey: "Ctrl+Shift+Alt+W",
      screenshotPrompt: "",
      screenshotVlmModel: "",
      voiceModel: "",
      estopHotkey: "Ctrl+Shift+Pause",
      emailAccounts: [],
      allowHeadlessEmail: false,
    },
    bot: {
      enabled: !freshMock,
      model: "",
      maxSteps: 25,
      debounceMs: 1500,
      allowlist: {
        enabled: true,
        allowAll: false,
        mode: "open",
        qqUsers: [],
        feishuUsers: [],
        weixinUsers: [],
        telegramUsers: [],
        qqGroups: [],
        feishuGroups: [],
        weixinGroups: [],
        telegramGroups: [],
      },
      qq: { enabled: false, appId: "", appSecretEnv: "QQ_BOT_APP_SECRET", secretSet: false },
      feishu: {
        enabled: false,
        domain: "feishu",
        appId: "",
        appSecretEnv: "FEISHU_BOT_APP_SECRET",
        secretSet: false,
        verificationToken: "",
        mode: "webhook",
        webhookPort: 8080,
        requireMention: true,
      },
      weixin: {
        enabled: false,
        accountId: "default",
        tokenEnv: "WEIXIN_BOT_TOKEN",
        tokenSet: false,
        apiBase: "https://ilinkai.weixin.qq.com",
      },
      telegram: {
        enabled: false,
        tokenEnv: "TELEGRAM_BOT_TOKEN",
        tokenSet: false,
        apiBase: "",
      },
      connections: freshMock ? [] : [
        {
          id: "mock-lark-kun",
          provider: "feishu",
          domain: "lark",
          label: "kun",
          enabled: true,
          status: "connected",
          model: "",
          workspaceRoot: "",
          credential: {
            appId: "cli_mock_lark",
            appSecretEnv: "FEISHU_BOT_APP_SECRET",
            accountId: "",
            tokenEnv: "",
            secretSet: true,
          },
          sessionMappings: [
            {
              remoteId: "ou_3a2bdd60640aaa95518186677b1f6d8c",
              sessionId: "topic:topic_product",
              scope: "global",
              workspaceRoot: "",
              updatedAt: new Date(Date.now() - 4 * 60_000).toISOString(),
            },
          ],
          lastError: "",
          createdAt: new Date(Date.now() - 86_400_000).toISOString(),
          updatedAt: new Date(Date.now() - 4 * 60_000).toISOString(),
        },
        {
          id: "mock-weixin-kun",
          provider: "weixin",
          domain: "weixin",
          label: "kun",
          enabled: true,
          status: "connected",
          model: "",
          workspaceRoot: "",
          credential: {
            appId: "",
            appSecretEnv: "",
            accountId: "default",
            tokenEnv: "WEIXIN_BOT_TOKEN",
            secretSet: true,
          },
          sessionMappings: [
            {
              remoteId: "wxid_kun_auto",
              sessionId: "topic:topic_ai",
              scope: "global",
              workspaceRoot: "",
              updatedAt: new Date(Date.now() - 12 * 60_000).toISOString(),
            },
          ],
          lastError: "",
          createdAt: new Date(Date.now() - 86_400_000).toISOString(),
          updatedAt: new Date(Date.now() - 12 * 60_000).toISOString(),
        },
      ],
    },
    webSearch: {
      braveKeySet: false,
      exaKeySet: false,
      linkupKeySet: false,
      anysearchKeySet: false,
    },
    desktopLanguage: "",
    desktopTheme: "light",
    desktopThemeStyle: "graphite",
    closeBehavior: "background",
    displayMode: "minimal",
    checkUpdates: true,
    telemetry: true,
    expandThinking: false,
    configPath: "~/projects/docs/fairpeer.toml",
    providerKinds: ["openai"],
    autoApproveTools: false,
    bypass: false,
  };
  // providers default to empty (provider-agnostic)
  if (freshMock) {
    settings.configPath = "~/.config/fairpeer/config.toml";
  }
  const mockNow = Date.now();
  const mockProjectTree: ProjectNode[] = freshMock ? [] : [
    {
      key: "project_~/projects/web-app",
      kind: "project",
      label: t("mock.projectJoyquantDb"),
      root: "~/projects/web-app",
      projectColor: "blue",
      children: [
        { key: "topic_dev_standard", kind: "topic", label: `● ${t("mock.topicDevStandard")}`, root: "~/projects/web-app", topicId: "topic_dev_standard", projectColor: "blue", turns: 18, lastActivityAt: mockNow - 8 * 60_000, open: true, running: runningMock },
        { key: "topic_db_maint", kind: "topic", label: t("mock.topicDbMaint"), root: "~/projects/web-app", topicId: "topic_db_maint", projectColor: "blue", turns: 7, lastActivityAt: mockNow - 2 * 60 * 60_000 },
        { key: "topic_env", kind: "topic", label: t("mock.topicEnv"), root: "~/projects/web-app", topicId: "topic_env", projectColor: "blue", turns: 3, lastActivityAt: mockNow - 26 * 60 * 60_000 },
      ],
    },
    {
      key: "project_~/projects/api-server",
      kind: "project",
      label: t("mock.projectJoyquantSys"),
      root: "~/projects/api-server",
      projectColor: "purple",
      children: [
        { key: "topic_p3b_pd", kind: "topic", label: `● ${t("mock.topicP3b")}`, root: "~/projects/api-server", topicId: "topic_p3b_pd", projectColor: "purple", turns: 11, lastActivityAt: mockNow - 3 * 24 * 60 * 60_000, status: runningMock ? "streaming" : undefined },
        { key: "topic_p3a_pd", kind: "topic", label: t("mock.topicP3a"), root: "~/projects/api-server", topicId: "topic_p3a_pd", projectColor: "purple", turns: 9, lastActivityAt: mockNow - 4 * 24 * 60 * 60_000, status: runningMock ? "thinking" : undefined },
        { key: "topic_hotfix", kind: "topic", label: t("mock.topicHotfix"), root: "~/projects/api-server", topicId: "topic_hotfix", projectColor: "purple", turns: 4, lastActivityAt: mockNow - 5 * 24 * 60 * 60_000, status: runningMock ? "thinking" : undefined },
        { key: "topic_sys_coord", kind: "topic", label: t("mock.topicSysCoord"), root: "~/projects/api-server", topicId: "topic_sys_coord", projectColor: "purple", turns: 14, lastActivityAt: mockNow - 6 * 24 * 60 * 60_000, status: runningMock ? "waiting_confirmation" : undefined },
        { key: "topic_sys_standard", kind: "topic", label: t("mock.topicSysStandard"), root: "~/projects/api-server", topicId: "topic_sys_standard", projectColor: "purple", turns: 6, lastActivityAt: mockNow - 7 * 24 * 60 * 60_000, status: "paused" },
        { key: "topic_sys_exception", kind: "topic", label: t("mock.topicSysException"), root: "~/projects/api-server", topicId: "topic_sys_exception", projectColor: "purple", turns: 2, lastActivityAt: mockNow - 8 * 24 * 60 * 60_000, status: "error" },
      ],
    },
    {
      key: "project_home",
      kind: "project",
      label: "工作台",
      root: mockHomeRoot,
      children: [
        { key: "home_topic_product", kind: "topic", label: t("mock.topicProduct"), root: mockHomeRoot, topicId: "topic_product", turns: 5, lastActivityAt: mockNow - 8 * 24 * 60 * 60_000 },
        { key: "home_topic_ai", kind: "topic", label: t("mock.topicAi"), root: mockHomeRoot, topicId: "topic_ai", turns: 8, lastActivityAt: mockNow - 10 * 24 * 60 * 60_000 },
        { key: "home_topic_lab", kind: "topic", label: t("mock.topicLab"), root: mockHomeRoot, topicId: "topic_lab", turns: 2, lastActivityAt: mockNow - 12 * 24 * 60 * 60_000 },
      ],
    },
  ];
  const cloneProjectTree = () => JSON.parse(JSON.stringify(mockProjectTree)) as ProjectNode[];
  const projectChildren = (node: ProjectNode): ProjectNode[] => Array.isArray(node.children) ? node.children : [];
  const findMockTopic = (topicId: string): ProjectNode | null => {
    for (const parent of mockProjectTree) {
      const found = projectChildren(parent).find((child) => child.topicId === topicId);
      if (found) return found;
    }
    return null;
  };
  const deleteMockTopic = (topicId: string) => {
    for (const parent of mockProjectTree) {
      parent.children = projectChildren(parent).filter((child) => child.topicId !== topicId);
    }
  };
  const topicLabel = (topicId: string, fallback: string) => (findMockTopic(topicId)?.label || fallback).replace(/^●\s*/, "");
  const mockTopicStatus = (topicId: string) => findMockTopic(topicId)?.status ?? "";
  const mockTopicIsRunning = (topicId: string) => {
    const status = mockTopicStatus(topicId);
    return status === "streaming" || status === "thinking" || status === "waiting_confirmation";
  };
  const mockTopicRunsInScenario = (topicId: string) => runningMock && mockTopicIsRunning(topicId);
  const mockTopicHistory = (topicId: string): HistoryMessage[] => {
    switch (topicId) {
      case "topic_product":
        return [
          {
            role: "user",
            content: [
              "[[fairpeer-im]]",
              "provider=lark",
              "label=Feishu / Lark",
              "sender=ou_3a2bdd60640aaa95518186677b1f6d8c",
              "chat=p2p 会话",
              "[[/fairpeer-im]]",
              "你可以做什么",
            ].join("\n"),
          },
          {
            role: "assistant",
            content: "这是 Global 范围下的 IM 会话。我可以先处理不依赖项目文件的问答、计划和信息整理；需要进入项目时，再由桌面端显式绑定或迁移到项目话题。",
          },
        ];
      case "topic_ai":
        return [
          {
            role: "user",
            content: [
              "[[fairpeer-im]]",
              "provider=weixin",
              "label=微信",
              "sender=wxid_kun_auto",
              "chat=单聊",
              "[[/fairpeer-im]]",
              "帮我整理一下今天要做的事",
            ].join("\n"),
          },
          {
            role: "assistant",
            content: "可以。我会先在 Global 范围里整理任务清单；如果某条任务需要读取项目文件，再切到你授权的项目话题处理。",
          },
        ];
      case "topic_dev_standard":
        return [
          {
            role: "user",
            content: [
              "[[fairpeer-im]]",
              "provider=lark",
              "label=Feishu / Lark",
              "sender=ou_3a2bdd60640aaa95518186677b1f6d8c",
              "chat=p2p 会话",
              "[[/fairpeer-im]]",
              "你可以做什么",
            ].join("\n"),
          },
          {
            role: "assistant",
            content: "我可以在桌面端帮你处理代码编写、文件操作、项目分析和问题定位。来自 IM 的请求会进入同一条聊天时间线，桌面端继续承载模型调用、工具执行和上下文管理。",
          },
          {
            role: "user",
            content: "看一下这版设计稿和需求说明 @[设计稿.png](.fairpeer/attachments/mock-clipboard.png) @[需求说明.md](.fairpeer/attachments/mock-spec.md)",
          },
          {
            role: "assistant",
            content: "收到，设计稿如下图，我先对照需求说明核对一遍再给结论：\n\n![设计稿](.fairpeer/attachments/mock-clipboard.png)",
          },
        ];
      case "topic_p3b_pd":
        return [
          { role: "user", content: "把 p3b P&D 的范围和风险重新整理成可执行计划。" },
          { role: "phase", content: "分析需求范围" },
        ];
      case "topic_p3a_pd":
        return [
          { role: "user", content: "复盘 p3a 的技术方案，先不要写文件，先说明你的判断。" },
        ];
      case "topic_hotfix":
        return [
          { role: "user", content: "检查 post-p3-hotfix 的回归风险，重点看最近的 shell 输出和 git 改动。" },
          { role: "assistant", content: "", reasoning: "我先定位最近一次 hotfix 的上下文，然后用只读命令检查状态；左侧保持“思考中”，工具细节在这里展开。" },
        ];
      case "topic_sys_coord":
        return [
          { role: "user", content: "准备执行 api-server 的同步脚本，但需要我确认后再运行。" },
          { role: "assistant", content: "", reasoning: "这个动作会运行脚本并可能刷新本地缓存，所以需要先等用户确认。" },
        ];
      case "topic_sys_standard":
        return [
          { role: "user", content: "继续制定 SYS 项目开发规范，先停在当前检查点。" },
          { role: "assistant", content: "已暂停在规范整理阶段。当前保留了目录约定、分支策略和待确认的发布检查项；继续时可以从这里恢复。" },
          { role: "notice", level: "info", content: "会话已暂停：未继续执行命令，等待用户恢复或切换任务。" },
        ];
      case "topic_sys_exception":
        return [
          { role: "user", content: "演练异常处理流程，看看失败时界面怎么提示。" },
          { role: "assistant", content: "我尝试校验恢复脚本时遇到异常，已停止继续执行。" },
          { role: "notice", level: "warn", content: "运行异常：恢复脚本缺少必要环境变量 JOYQUANT_SYS_TOKEN。请补齐配置后重试。" },
        ];
      default:
        return [];
    }
  };
  const mockRuntimeInjected = new Set<string>();
  const queueMockTopicRuntime = (tab: TabMeta) => {
    if (!runningMock) return;
    const status = mockTopicStatus(tab.topicId);
    if (status !== "streaming" && status !== "thinking" && status !== "waiting_confirmation") return;
    const key = `${tab.id}:${tab.topicId}:${status}`;
    if (mockRuntimeInjected.has(key)) return;
    mockRuntimeInjected.add(key);
    window.setTimeout(() => {
      void withMockTabScope(tab.id, async () => {
        emitMockTurnStarted();
        await delay(120);
        if (tab.topicId === "topic_p3b_pd") {
          const text = "我会先把范围拆成三层：目标、依赖、风险。当前已经确认 p3b 的交付边界，接下来补充每个模块的验收口径...";
          for (const ch of text) {
            emit({ kind: "text", text: ch });
            await delay(5);
          }
          emitMockTurnDone();
          return;
        }
        if (tab.topicId === "topic_p3a_pd") {
          emit({ kind: "reasoning", text: "我正在对比 p3a 和 p3b 的差异：先看约束，再看变更风险，最后判断是否需要拆成独立任务。\n\n" });
          await delay(220);
          emit({ kind: "reasoning", text: "当前倾向：先保留 p3a 的兼容路径，不急于删除旧逻辑。" });
          emitMockTurnDone();
          return;
        }
        if (tab.topicId === "topic_hotfix") {
          const id = "mock-hotfix-shell";
          emit({ kind: "tool_dispatch", tool: { id, name: "bash", args: JSON.stringify({ command: "git status --short && npm test" }), readOnly: true } });
          await delay(180);
          emit({ kind: "tool_progress", tool: { id, name: "bash", readOnly: true, output: "$ git status --short\n M internal/sys/runner.go\n\n$ npm test\nrunning targeted regression tests...\n" } });
          emitMockTurnDone();
          return;
        }
        if (tab.topicId === "topic_sys_coord") {
          pendingApprovalPreview = true;
          emit({ kind: "reasoning", text: "我已经准备好执行同步脚本，但这个操作会影响本地 workspace，需要用户确认。" });
          await delay(160);
          emit({
            kind: "approval_request",
            approval: {
              id: "mock-sys-confirm",
              tool: "bash",
              subject: "npm run sync:api-server\n\n该命令会同步 SYS 项目配置并刷新本地缓存。",
            },
          });
        }
      });
    }, 180);
  };
  const setMockActiveTab = (tabId: string) => {
    mockTabs = mockTabs.map((tab) => ({ ...tab, active: tab.id === tabId }));
  };
  const currentMockTurnTabId = () => mockScopedTabId || mockTabs.find((tab) => tab.active)?.id;
  const setMockTabRunning = (tabId: string | undefined, running: boolean) => {
    if (!tabId) return;
    mockTabs = mockTabs.map((tab) => (tab.id === tabId ? { ...tab, running } : tab));
  };
  const emitMockTurnStarted = () => {
    setMockTabRunning(currentMockTurnTabId(), true);
    emit({ kind: "turn_started" });
  };
  const emitMockTurnDone = () => {
    setMockTabRunning(currentMockTurnTabId(), false);
    emit({ kind: "turn_done" });
  };
  let mockTabs: TabMeta[] = freshMock ? [
    {
      id: "tab_home",
      scope: "project",
      workspaceRoot: mockHomeRoot,
      workspaceName: "工作台",
      topicId: "",
      topicTitle: "工作台",
      label: "gpt-4o",
      ready: true,
      running: false,
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      active: true,
      cwd: mockHomeRoot,
    },
  ] : [
    {
      id: "tab_web_app",
      scope: "project",
      workspaceRoot: "~/projects/web-app",
      workspaceName: "web-app",
      topicId: "topic_dev_standard",
      topicTitle: t("mock.trashDevStandardTitle"),
      projectColor: "blue",
      label: "gpt-4o",
      ready: true,
      running: false,
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      active: true,
      cwd: "~/projects/web-app",
      profile: mockInitialProfile(),
    },
    {
      id: "tab_api_server",
      scope: "project",
      workspaceRoot: "~/projects/api-server",
      workspaceName: "api-server",
      topicId: "topic_p3b_pd",
      topicTitle: "p3b P&D",
      projectColor: "purple",
      label: "gpt-4o",
      ready: true,
      running: runningMock && mockTopicIsRunning("topic_p3b_pd"),
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      active: false,
      cwd: "~/projects/api-server",
    },
    {
      id: "tab_home",
      scope: "project",
      workspaceRoot: mockHomeRoot,
      workspaceName: "工作台",
      topicId: "topic_ai",
      topicTitle: t("mock.topicAi"),
      label: "",
      ready: true,
      running: false,
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      active: false,
      cwd: mockHomeRoot,
    },
  ];
  const mockModelCatalog = [
    { ref: "openai/gpt-4o", provider: "openai", model: "gpt-4o" },
  ];
  const defaultMockModelRef = mockModelCatalog[0].ref;
  const mockModelRef = (name: string): string => {
    const trimmed = name.trim();
    if (!trimmed) return defaultMockModelRef;
    const exact = mockModelCatalog.find((model) => model.ref === trimmed);
    if (exact) return exact.ref;
    const byModel = mockModelCatalog.find((model) => model.model === trimmed);
    return byModel?.ref ?? trimmed;
  };
  const mockModelLabel = (ref: string): string => mockModelCatalog.find((model) => model.ref === mockModelRef(ref))?.model ?? ref.split("/").pop() ?? ref;
  const mockTabModelRef = (tab?: TabMeta): string => mockModelRef(tab?.label ?? "");
  const setMockTabModel = (tabID: string | undefined, name: string) => {
    const ref = mockModelRef(name);
    const label = mockModelLabel(ref);
    let applied = false;
    mockTabs = mockTabs.map((tab) => {
      const match = tabID ? tab.id === tabID : tab.active;
      if (!match) return tab;
      applied = true;
      return { ...tab, label };
    });
    if (!applied && mockTabs.length > 0) {
      mockTabs = mockTabs.map((tab, index) => (index === 0 ? { ...tab, label } : tab));
    }
  };
  // Profile mock helpers: profile lives on TabMeta.profile (absent = dev). The
  // mock emits a profile:changed event so the dev-shell layout swap can be
  // exercised without the Go backend.
  const mockProfileOf = (tab?: TabMeta): string => (tab?.profile ?? "dev").toLowerCase() || "dev";
  const setMockTabProfile = (tabID: string | undefined, name: string) => {
    const profile = (name || "dev").toLowerCase();
    let affectedId: string | undefined;
    mockTabs = mockTabs.map((tab) => {
      const match = tabID ? tab.id === tabID : tab.active;
      if (!match) return tab;
      affectedId = tab.id;
      return { ...tab, profile };
    });
    if (affectedId) {
      // Emit through the mock event bus so the dev shell's profile:changed /
      // project-tree:changed handlers fire — mirroring the real Wails flow
      // (backend emits both after a SwitchProfileForTab rebuild).
      emitMockEvent("profile:changed", { tabId: affectedId, profile });
      emitMockEvent("project-tree:changed");
      emitMockEvent("agent:ready", { tabId: affectedId });
    }
  };
  // NetDev mock: an in-memory settings store so the browser dev shell can
  // exercise the netdev panel without the Go backend. Passwords/audit/ssh
  // imports are stubbed — the real implementations live in the Go bindings.
  // Two seeded devices mirror the topology mock (CORE-01/ACC-01) so the
  // click-a-node → device card flow is demoable in the browser dev shell.
  let mockBackups: { id: string; device: string; at: string; bytes: number; lines: number }[] = [];
  // Job 引擎 / 割接 mock state (R4) — in-memory, mock-run scoped.
  const mockNetDevJobs: NetDevJob[] = [];
  const mockNetDevCutovers: NetDevCutoverRun[] = [];
  const mockNetDevTemplates: NetDevTemplate[] = [];
  // 状态历史 mock（浏览器演示）：两条可回退事件 + 一条被活跃实体阻塞的。
  const mockStateEvents: NetDevStateEventView[] = [
    { id: 3, time: Date.now() - 240_000, kind: "approve", entity: "P-20260831-1", actor: "user", paths: ["netdev/proposals/P-20260831-1.json"], live: [], canRestore: true, canRedo: false },
    { id: 2, time: Date.now() - 900_000, kind: "propose", entity: "P-20260831-1", actor: "agent", paths: ["netdev/proposals/P-20260831-1.json"], live: [], canRestore: true, canRedo: false },
    { id: 1, time: Date.now() - 3_600_000, kind: "cutover-start", entity: "C-20260830-2", actor: "user", paths: ["netdev/cutovers/C-20260830-2.json"], live: [{ type: "cutover", id: "C-20260830-2", status: "running" }], canRestore: false, canRedo: false },
  ];

  let mockNetDev: any = {
    enabled: false,
    networkName: "我的网络",
    devices: null,
    hops: null, groups: null, auditRetention: "", scopes: null,
    guardConfirmEach: false, guardTurnBudget: 0, guardAllowedGroups: null,
    inspectionInterval: "", backupInterval: "", scheduledBaseline: false,
    extraRead: null, projects: null, presets: null,
  };
  return {
    async NetDevSettings() {
      return mockNetDev;
    },
    async NetDevLogRead(device: string, source: string, tailN: number) {
      const out: NetDevExecResult = {
        device, command: `tail -n ${tailN || 100} ${source}`, class: "read",
        output: [
          "2026-08-27 10:00:01 [error] upstream connect failed (111 refused)",
          "2026-08-27 10:00:02 [warn] retrying in 2s",
          "2026-08-27 10:00:04 [info] upstream recovered",
          "(browser dev mock: no device backend)",
        ].join("\n"), is_error: false,
      };
      return out;
    },
    async NetDevLogFollowStart(device: string, source: string) {
      let n = 0;
      const t = setInterval(() => {
        n++;
        emitNetdevLogFollowMock({ device, source, chunk: `${new Date().toISOString().slice(11, 19)} [info] mock follow line ${n}\n` });
        if (n >= 20) {
          clearInterval(t);
          emitNetdevLogFollowMock({ device, source, done: true, reason: "mock cap reached (20 lines)" });
        }
      }, 1000);
    },
    async NetDevLogFollowStop(_device: string) {},
    async NetDevSeries(_device: string, _hours: number): Promise<Record<string, NetDevSeriesPoint[]>> {
      return {};
    },
    async NetDevHandoffReport(): Promise<string> {
      return "# 值班交接报告（browser dev mock）";
    },
    async NetDevWeeklyReport(): Promise<string> {
      return "# 运维周报（browser dev mock）";
    },
    async NetDevCredentialInventory(): Promise<string> {
      return "# 凭证盘点（browser dev mock）";
    },
    async NetDevTrapStatus(): Promise<{ listening: boolean; port: number; buffered: number }> {
      return { listening: false, port: 0, buffered: 0 };
    },
    async NetDevImportStageFile(_content: string): Promise<string> { throw new Error("browser dev mock: no import backend"); },
    async NetDevImportPreview(_path: string): Promise<import("./types").NetDevImportPreview> { throw new Error("browser dev mock: no import backend"); },
    async NetDevImportApply(_path: string, _add: string[], _take: string[]): Promise<number> { throw new Error("browser dev mock: no import backend"); },
    async NetDevExportState(): Promise<string> {
      return "(browser dev mock)";
    },
    async NetDevTimeline(_device: string, _hours: number): Promise<NetDevTimelineEvent[]> {
      return [];
    },
    async NetDevExpectedState(): Promise<NetDevExpectedStateView> {
      return { total: 0, reachable: 0, missing: [], noProbe: [] };
    },
    async NetDevCases(): Promise<NetDevIncidentCase[]> {
      return [];
    },
    async NetDevCaseSave(c: NetDevIncidentCase): Promise<NetDevIncidentCase> {
      return { ...c, id: c.id || "C-mock", created_at: c.created_at || new Date().toISOString(), updated_at: new Date().toISOString() };
    },
    async NetDevCaseDelete(_id: string): Promise<void> {},
    async NetDevCaseBundle(_id: string): Promise<string> {
      return "(browser dev mock)";
    },
    async NetDevAckFinding(_id: string): Promise<void> {},
    async NetDevFalsePositiveFinding(_id: string): Promise<void> {},
    async NetDevAggregatedFindings(): Promise<NetDevAggregatedFinding[]> {
      return [];
    },
    async NetDevImportCVEs(_f: string): Promise<number> {
      return 0;
    },
    async NetDevCVEMatches(): Promise<{ device: string; cve_id: string; desc: string; severity: string; product: string }[]> {
      return [];
    },
    async NetDevCVESweep(): Promise<NetDevFinding | null> {
      return null;
    },
    async NetDevDiscover(_c: string, _v: string, _p: number[]): Promise<import("./types").NetDevDiscoverHost[]> { return []; },
    async NetDevDiscoveredHosts(): Promise<import("./types").NetDevDiscoveredHost[]> { return []; },
    async NetDevPromoteHosts(_e: import("./types").NetDevPromoteForm[]): Promise<void> { },
    async NetDevDeleteDiscoveredHost(_ip: string): Promise<void> { },
    async NetDevDiscoverPrecheck(_v: string): Promise<import("./types").NetDevDiscoverPlan | null> { return null; },
    async NetDevDiscoverLayer(_v: string, _c: string[], _p: number[]): Promise<import("./types").NetDevDiscoverHost[]> { return []; },
    async NetDevDiscoveryRunState(): Promise<import("./types").NetDevDiscoveryRunState | null> { return null; },
    async NetDevDiscoverResume(): Promise<import("./types").NetDevDiscoverHost[]> { return []; },
    async NetDevDiscoverPause(): Promise<boolean> { return false; },
    async NetDevNetconfQuery(_d: string, _k: string): Promise<string> { return "browser dev mock: no netconf backend"; },
    async NetDevHumanTTYRecordings(): Promise<import("./types").NetDevHumanTTYRecording[]> { return []; },
    async NetDevHumanTTYRecordingRead(_p: string): Promise<string> { return ""; },
    async NetDevHumanTTYStart(_d: string): Promise<{ device: string; connected: boolean; startedAt: string; bytes: number }> {
      // Browser dev: a short banner plays so the device tab renders standalone.
      setTimeout(() => emitNetdevHumanTTYMock({ device: _d, chunk: "\r\n[mock] 人工终端（浏览器开发模拟）——真实会话在桌面端\r\n" }), 100);
      return { device: _d, connected: true, startedAt: new Date().toISOString(), bytes: 0 };
    },
    async NetDevHumanTTYWrite(d: string, input: string): Promise<void> {
      emitNetdevHumanTTYMock({ device: d, chunk: input });
    },
    async NetDevHumanTTYStop(_d: string): Promise<void> {},
    async NetDevHumanTTYStatus(): Promise<{ device: string; connected: boolean; startedAt: string; bytes: number }[]> {
      return [];
    },
    async NetDevHumanTTYResize(_d: string, _c: number, _r: number): Promise<void> {},
    async NetDevSFTPDownload(_d: string, _p: string): Promise<string> {
      return "(browser dev mock)";
    },
    async NetDevSFTPBrowse(_d: string, _p: string): Promise<string[]> {
      return [];
    },
    async NetDevJobStart(def: NetDevJob): Promise<NetDevJob> {
      const j: NetDevJob = { ...def, id: "J-mock-1", status: "paused", pause_note: "browser dev mock", commands: def.steps.length, breakpoint_ok: 0, active_ms: 0, step_state: def.steps.map(() => ({ status: "pending" })) };
      mockNetDevJobs.length = 0;
      mockNetDevJobs.push(j);
      return j;
    },
    async NetDevJobs(): Promise<NetDevJob[]> {
      return mockNetDevJobs;
    },
    async NetDevJobGet(id: string): Promise<NetDevJob> {
      const j = mockNetDevJobs.find(x => x.id === id);
      if (!j) throw new Error("browser dev mock: no such job");
      return j;
    },
    async NetDevJobPause(_id: string): Promise<void> {},
    async NetDevJobResume(id: string): Promise<NetDevJob> {
      const j = mockNetDevJobs.find(x => x.id === id);
      if (!j) throw new Error("browser dev mock: no such job");
      j.status = "done";
      j.pause_note = "";
      return j;
    },
    async NetDevJobAbort(id: string): Promise<NetDevJob> {
      const j = mockNetDevJobs.find(x => x.id === id);
      if (!j) throw new Error("browser dev mock: no such job");
      j.status = "aborted";
      return j;
    },
    async NetDevCutoverStart(def: NetDevCutoverRun): Promise<NetDevCutoverRun> {
      const c: NetDevCutoverRun = { ...def, id: "C-mock-1", status: "hold", hold_note: "决策点（浏览器模拟）：变更已下发", cursor: 1, created_at: new Date().toISOString(), steps: def.steps.map(s => ({ ...s, status: s.decision_point ? "approved" : "done" })) };
      mockNetDevCutovers.length = 0;
      mockNetDevCutovers.push(c);
      return c;
    },
    async NetDevCutovers(): Promise<NetDevCutoverRun[]> {
      return mockNetDevCutovers;
    },
    async NetDevCutoverGet(id: string): Promise<NetDevCutoverRun> {
      const c = mockNetDevCutovers.find(x => x.id === id);
      if (!c) throw new Error("browser dev mock: no such cutover");
      return c;
    },
    async NetDevCutoverContinue(id: string): Promise<NetDevCutoverRun> {
      const c = mockNetDevCutovers.find(x => x.id === id);
      if (!c) throw new Error("browser dev mock: no such cutover");
      c.status = "done";
      c.hold_note = "";
      c.report = "# 割接对比报告（浏览器模拟）\n\n无变化\n";
      return c;
    },
    async NetDevCutoverRollback(id: string): Promise<NetDevCutoverRun> {
      const c = mockNetDevCutovers.find(x => x.id === id);
      if (!c) throw new Error("browser dev mock: no such cutover");
      c.status = "aborted";
      c.hold_note = "已按决策点回退（浏览器模拟）";
      return c;
    },
    async NetDevCutoverAbort(id: string): Promise<NetDevCutoverRun> {
      const c = mockNetDevCutovers.find(x => x.id === id);
      if (!c) throw new Error("browser dev mock: no such cutover");
      c.status = "aborted";
      return c;
    },
    async NetDevCutoverReport(_id: string): Promise<string> {
      return "cutover-mock.md";
    },
    async NetDevTemplates(): Promise<NetDevTemplate[]> {
      return mockNetDevTemplates;
    },
    async NetDevTemplateSave(t: NetDevTemplate): Promise<NetDevTemplate> {
      const saved: NetDevTemplate = { ...t, id: t.id || "T-mock-1", created_at: t.created_at || new Date().toISOString() };
      mockNetDevTemplates.length = 0;
      mockNetDevTemplates.push(saved);
      return saved;
    },
    async NetDevTemplateDelete(_id: string): Promise<void> {},
    async NetDevTemplateRender(id: string, vars: Record<string, string>): Promise<NetDevTemplatePreviewDevice[]> {
      const tpl = mockNetDevTemplates.find(t => t.id === id);
      return (tpl?.targets ?? []).map(dev => ({
        device: dev,
        available: true,
        steps: (tpl?.steps ?? []).map(s => ({
          commands: s.commands.map(c => c.replace(/\{\{(\w+)\}\}/g, (_m, k) => vars[k] ?? `{{${k}}}`)),
          rollback: s.rollback.map(c => c.replace(/\{\{(\w+)\}\}/g, (_m, k) => vars[k] ?? `{{${k}}}`)),
          classes: s.commands.map(() => "write"),
          dangerous: false,
        })),
      }));
    },
    async NetDevTemplateApply(_id: string, _vars: Record<string, string>): Promise<NetDevProposal> {
      throw new Error("browser dev mock: no proposal backend");
    },
    async NetDevSrvConfSnapshot(_d: string, _p: string): Promise<NetDevSrvConfVersion> {
      return { id: "sc@mock@hash@1", device: _d, path: _p, at: new Date().toISOString().slice(5, 16), bytes: 128, lines: 8 };
    },
    async NetDevSrvConfVersions(_d: string, _p: string): Promise<NetDevSrvConfVersion[]> {
      return [];
    },
    async NetDevSrvConfDiff(_a: string, _b: string): Promise<string> {
      return "";
    },
    async NetDevSrvConfDrift(_p: string, devices: string[]): Promise<NetDevSrvConfDriftRow[]> {
      return devices.map(d => ({ device: d, group: "", status: "same" }));
    },
    async NetDevOOBLaunch(_d: string): Promise<string> {
      return "(browser dev mock) 带外启动仅桌面端可用";
    },
    async NetDevLocate(_target: string): Promise<NetDevLocateResult> {
      return { target: _target, hits: [], searched: [], covered_devices: 0, total_devices: 0, budget_stopped: false, note: "browser dev mock" };
    },
    async NetDevFirewallGet(_device: string, what: string): Promise<string> {
      return what === "conns"
        ? '{"http_status":0,"results":[{"src":"10.1.0.5","dst":"8.8.8.8"}]}'
        : `(browser dev mock: no firewall for "${what}")`;
    },
    async NetDevDockerGet(_device: string, what: string, _container: string, _tailN: number): Promise<string> {
      return what === "ps"
        ? '[{"Id":"abc123","Names":["/web"],"State":"running"}]'
        : `(browser dev mock: no docker engine for "${what}")`;
    },
    async NetDevK8sGet(_device: string, what: string, _namespace: string, _name: string, _tailN: number): Promise<string> {
      return what === "pods"
        ? '{"items":[{"metadata":{"name":"web-1"}}]}'
        : `(browser dev mock: no cluster for "${what}")`;
    },
    async NetDevTriageRun(device: string): Promise<NetDevTriageReport> {
      return {
        device, vendor: "linux",
        sections: [
          { name: "失败登录", command: "lastb -n 100", ok: true, lines: ["btmp begins", "(browser dev mock)"] },
          { name: "磁盘水位", command: "df -h", ok: true, lines: ["/dev/sda1  91G  78G  8G  91% /"] },
        ],
        anomalies: ["磁盘使用率最高 91%（≥85% 水位）"],
        summary: "体检 2 项，1 项异常（browser dev mock）",
        created_at: new Date().toISOString(),
      };
    },
    async NetDevLogSearch(pattern: string, _devices: string[], _sources: string[], since: string): Promise<NetDevLogSearchResult> {
      return {
        pattern,
        hits: [{
          device: "mock-host", source: "file:/var/log/auth.log",
          line: `Aug 27 10:00:02 host sshd[2]: Failed password for root from 10.6.6.6 (${since || "no window"})`,
          context: ["Aug 27 10:00:01 host cron[1]: (root) CMD (/usr/bin/backup)"],
        }],
        devices_searched: ["mock-host"], skipped: [],
        covered_devices: 1, total_devices: 1, devices_with_hits: 1,
        budget_stopped: false, note: "browser dev mock: no device backend",
      };
    },
    async NetDevDBQuery(_source: string, query: string) {
      return `(browser dev mock: no db backend for "${query}")`;
    },
    async NetDevHealthSnapshot(): Promise<NetDevHealthSnapshot> {
      return { pollIntervalSeconds: 0, devices: [] };
    },
    async TrustDomainStatus(): Promise<TrustDomainView> {
      return { enabled: false, joined: false, height: 0, paused: false, successionConfigured: false, successionAfterSec: 0, successionLastActive: 0, successionDue: false };
    },
    async TrustDomainPause(_reason: string): Promise<void> {},
    async TrustDomainResume(): Promise<void> {},
    async TrustDomainAnchor(): Promise<void> {},
    async NetDevSyslogTail(_device: string, _tailN: number, _grep: string): Promise<string[]> {
      return ["(browser dev mock: syslog receiver off)"];
    },
    async NetDevSyslogStatus(): Promise<NetDevSyslogStatusView> {
      return { listening: false, port: 0, buffered: 0 };
    },
    async NetDevAuditVerify(): Promise<NetDevAuditChainStatus> {
      return { total: 0, chained: 0, ok: true };
    },
    async NetDevResolveFinding(_id: string): Promise<void> {},
    async NetDevSetGoldenFromBackup(_device: string, _versionID: string): Promise<void> {},
    async NetDevMetricHistory(_device: string): Promise<NetDevMetricPoint[]> {
      return [];
    },
    async NetDevGoldenInfo(_device: string): Promise<NetDevGoldenInfo> {
      return { set: false, at: "", lines: 0 };
    },
    async NetDevGoldenCheck(_device: string): Promise<string> {
      return "(browser dev mock: golden check off)";
    },
    async NetDevNotifyTest(): Promise<void> {},
    async SetNetDevSettings(v: NetDevSettingsView) {
      mockNetDev = { ...v, devices: [...v.devices], hops: [...v.hops], groups: [...v.groups], scopes: [...v.scopes], guardAllowedGroups: [...(v.guardAllowedGroups ?? [])], projects: v.projects ? [...v.projects] : mockNetDev.projects, presets: v.presets ? [...v.presets] : mockNetDev.presets };
    },
    async NetDevDeleteSecret(_kind: string, _envName: string) {},
    async NetDevTestConnection(device: string) {
      return { device, status: "error", detail: "browser dev mock: no device backend" };
    },
    async NetDevTrustHostKey(_fingerprint: string) {},
    async NetDevStateEvents(): Promise<NetDevStateEventView[]> { return mockStateEvents; },
    async NetDevStateEventDiff(_id: number): Promise<NetDevStateFileDiff[]> { return []; },
    async NetDevStateRestore(id: number): Promise<NetDevStateRestoreResultView> {
      const ev = mockStateEvents.find(e => e.id === id);
      if (ev && !ev.canRestore) throw new Error("mock: restore blocked (live entity)");
      return { written: ev ? ev.paths : [], deleted: [], reverseEventId: Date.now() };
    },
    async NetDevProposals() { return [] as NetDevProposal[]; },
    async NetDevRunInspection() { return null; },
    async NetDevRunBaseline() { return null; },
    async NetDevRunBackup(device: string) {
      mockBackups = [{ id: `${device}@${Date.now() - 3600_000}`, device, at: "08-19 09:00:00", bytes: 4210, lines: 180 },
        { id: `${device}@${Date.now() - 86_400_000}`, device, at: "08-18 09:00:00", bytes: 4180, lines: 178 }].concat(mockBackups);
      return mockBackups.filter(v => v.device === device);
    },
    async NetDevBackups(device: string) { return mockBackups.filter(v => !device || v.device === device); },
    async NetDevBackupDiff(device: string, _a: string, _b: string) {
      return `--- ${device} running-config\n+++ ${device} running-config\n@@ -12,4 +12,5 @@\n vlan 10\n- description office\n+ description office-floor2\n+ stp edged-port enable\n ntp-service unicast-server 10.0.0.253`;
    },
    async NetDevImportNmap(_xml: string) { return null; },
    async NetDevSnmpQuery(_device: string, _oid: string, _mode: string) {
      return "1.3.6.1.2.1.1.1.0 = Linux mock 6.1 (browser dev mock)\n1.3.6.1.2.1.1.3.0 = 3691200 (ticks)";
    },
    async NetDevWeakCredCheck(device: string, _tier: string): Promise<NetDevWeakCredResult> {
      return { device, tier: "basic", weak: false, attempts: 3, budget: 3, detail: "browser dev mock: no weak credential in 3 attempt(s) (budget 3)" };
    },
    async NetDevLiveSnapshot(): Promise<NetDevLiveSnapshot> {
      // Demo snapshot: one connected switch, one idle router; then play a
      // scripted command lifecycle so the panel is visible in the browser mock.
      queueMicrotask(() => playNetdevLiveMockDemo());
      return {
        devices: [
          { device: "core-sw-1", vendor: "huawei", os: "vrp8", group: "core", connected: true, vtyUse: 1, vtyCap: 2 },
          { device: "edge-r-2", vendor: "cisco", os: "ios", group: "edge", connected: false, vtyUse: 0, vtyCap: 2 },
        ],
        spent: 2,
        budget: 30,
      };
    },
    async NetDevRedfishQuery(_device: string, _path: string) {
      return JSON.stringify({ "@odata.type": "#Chassis.v1_20.Chassis", Id: "1", Name: "Mock Chassis", Thermal: { Fans: [{ Name: "Fan1", Reading: 2400, Status: { Health: "OK" } }] } }, null, 2);
    },
    async NetDevDailyBriefing() {
      return "**总体判断**：网络平稳，风险等级 **低**。\n\n**需要关注**\n1. ACC-01 上行口错包增长（依据：今日发现）\n2. 基线：2 台设备仍在用 SNMP v2c（依据：基线核查）\n\n**建议动作**\n- 只读核查：ACC-01 接口错包计数（可直接做）\n- 变更：SNMPv3 迁移（需起草提案）";
    },
    async NetDevFindings() { return [] as NetDevFinding[]; },
    async NetDevFindingDismiss(_id: string) { console.info("mock NetDevFindingDismiss", _id); },
    async NetDevFindingsClear() { return 0; },
    async NetDevEmergencyStop() { return 0; },
    async NetDevTurnBegin() {},
    async NetDevAddExtraRead(vendor: string, command: string) {
      mockNetDev = { ...mockNetDev, extraRead: { ...mockNetDev.extraRead, [vendor]: [...(mockNetDev.extraRead[vendor] ?? []), command] } };
    },
    async NetDevQuickExec(device: string, command: string) {
      // Dev-shell stand-in: verb-rooted reads pass, anything else is refused
      // as unknown so the one-click "teach the read table" chip is demoable.
      if (!/^(display|show)\s/.test(command.trim())) {
        return { device, command, class: "unknown", output: "", is_error: true, refused: true, refusal: "unknown command — conservatively refused（演示：点「允许此命令」加入读表后立即可用）" };
      }
      return { device, command, class: "read", output: `(browser dev mock: ${command} 输出示意)\n\n GigabitEthernet0/0/1  current state : UP\n Line protocol state : UP`, is_error: false };
    },
    async NetDevTopologySnapshot() {
      // Browser-dev stand-in shaped like a real small campus net (FW → 2 core →
      // 2 aggregation → 3 access + an unmanaged neighbor) so the tiered
      // TopologyMap is demoable without the Go backend.
      const N = (name: string, managed: boolean, ip?: string, role?: string, role_source?: string): NetDevTopologyNode => ({ name, managed, device_ip: ip, role, role_source });
      return {
        nodes: [
          N("FW-01", true, "10.0.0.1", "firewall", "label"),
          N("CORE-01", true, "10.0.0.2", "switch", "model"), N("CORE-02", true, "10.0.0.3", "switch", "model"),
          N("AGG-01", true, "10.0.1.1", "switch", "vendor"), N("AGG-02", true, "10.0.1.2", "switch", "vendor"),
          N("ACC-01", true, "10.0.2.1", "switch", "vendor"), N("ACC-02", true, "10.0.2.2", "switch", "vendor"), N("ACC-03", true, "10.0.2.3", "switch", "vendor"),
          N("SRV-ESXi", false, undefined, "server", "label"), N("IPSLA-P", false),
        ],
        edges: [
          { local_device: "FW-01", local_port: "GE0/0/1", remote_device: "CORE-01", remote_port: "GE1/0/1", source: "lldp" },
          { local_device: "FW-01", local_port: "GE0/0/2", remote_device: "CORE-02", remote_port: "GE1/0/1", source: "lldp" },
          { local_device: "CORE-01", local_port: "GE1/0/24", remote_device: "CORE-02", remote_port: "GE1/0/24", source: "lldp" },
          { local_device: "CORE-01", local_port: "GE1/0/2", remote_device: "AGG-01", remote_port: "GE0/0/1", source: "lldp" },
          { local_device: "CORE-02", local_port: "GE1/0/2", remote_device: "AGG-01", remote_port: "GE0/0/2", source: "lldp" },
          { local_device: "CORE-02", local_port: "GE1/0/3", remote_device: "AGG-02", remote_port: "GE0/0/1", source: "lldp" },
          { local_device: "AGG-01", local_port: "GE0/0/23", remote_device: "AGG-02", remote_port: "GE0/0/23", source: "lldp" },
          { local_device: "AGG-01", local_port: "GE0/0/3", remote_device: "ACC-01", remote_port: "GE0/0/1", source: "lldp" },
          { local_device: "AGG-01", local_port: "GE0/0/4", remote_device: "ACC-02", remote_port: "GE0/0/1", source: "lldp" },
          { local_device: "AGG-02", local_port: "GE0/0/3", remote_device: "ACC-03", remote_port: "GE0/0/1", source: "lldp" },
          { local_device: "AGG-01", local_port: "GE0/0/48", remote_device: "SRV-ESXi", source: "cdp" },
          { local_device: "ACC-03", local_port: "GE0/0/24", remote_device: "IPSLA-P", source: "cdp" },
        ],
        at: new Date().toISOString().slice(0, 19).replace("T", " "),
      };
    },
    async NetDevImportTopoPreview(_x: string): Promise<import("./types").NetDevTopoImportPreview | null> { return null; },
    async NetDevImportVsdxPreview(_b: string): Promise<import("./types").NetDevTopoImportPreview | null> { return null; },
    async NetDevAttachmentBytesBase64(_r: string): Promise<string> { return ""; },
    async NetDevImportTopoApply(_s: string, _g: import("./types").NetDevTopologyGraph): Promise<void> { },
    async NetDevTopologyDesign(): Promise<import("./types").NetDevTopologyDesign | null> { return null; },
    async NetDevAttackPaths(): Promise<import("./types").NetDevAttackPathReport | null> { return null; },
    async NetDevOverview(_force: boolean): Promise<import("./types").NetDevOverviewSnapshot | null> {
      return {
        generated_at: Date.now(), stale_after_sec: 300,
        coverage: { managed: 2, discovered: 3, unreachable: 0, no_snmp: 1 },
        health: { polled: 1, reachable: 1, last_poll_at: Date.now(), flap_alerts: 0, p90_alerts: 0, uptime_spark: {}, max_cpu_pct: 87, max_cpu_dev: "SW-03", max_mem_pct: 62 },
        risk: { critical: 0, warning: 1, info: 2, open_total: 3, weighted_score: 4, risk_level: "low", cve_matches: 0, cve_needs_feed: true, weak_creds: 0 },
        inflight: { proposals_pending: 1, proposals_watchable: 0, jobs_running: 0, jobs_paused: 0, cutovers_active: 0, terminals_open: 0 },
        events: [{ id: "F-demo", severity: "warning", title: "link-flap SW-03", source: "syslog:SW-03:link-flap", at: "08-29 10:00" }],
        audit: { chain_ok: true, chain_total: 12, last_entry_at: "08-29 10:05", read_24h: 20, write_24h: 2, guardrail_24h: 0 },
        stats: {
          cve_needs_feed: true, job_done: 3, job_finished: 4,
          cmd_mix: { read: 18, write: 2 }, audit_entries: 20,
          device_by_role: { switch: 1, router: 1 }, proposal_funnel: { draft: 1, done: 2 },
          risk_trend: [{ at: "2026-08-29T09:00:00", kind: "inspection", devices: 2, checked: 2, critical: 0, warning: 1, info: 2 }],
          inspection_compliance: { enabled: true, last_run_at: "2026-08-29T03:00:00", ok: true, title: "巡检 2 台设备，0 项异常" },
          cred_health: { count: 3, last_changed_at: "2026-05-12", age_days: 109, stale: true },
        },
        scenario_cutover_active: false, scenario_discovery_run: false,
      };
    },
    async NetDevInvestigationChain(_caseID: string, _findingID: string, _hours: number): Promise<import("./types").NetDevInvestigationChain | null> {
      return {
        has_case: true, case_id: "C-demo", case_title: "OSPF 邻居震荡·SW-03", counts: { event: 1, action: 1, evidence: 2, conclusion: 1, remediation: 1, verification: 1 },
        nodes: [
          { id: "event-0", kind: "event", label: "syslog:SW-03:ospf-adjacency", device: "SW-03", at: "08-29 10:00", ref_type: "finding", group: 2 },
          { id: "conclusion-1", kind: "conclusion", label: "OSPF 邻居 Down", device: "SW-03", status: "critical" },
          { id: "evidence-2", kind: "evidence", label: "display ospf peer", device: "SW-03" },
          { id: "action-3", kind: "action", label: "display logbuffer", device: "SW-03", ref_type: "audit" },
          { id: "remediation-4", kind: "remediation", label: "P-1 调整 hello 定时器", status: "done" },
          { id: "verification-5", kind: "verification", label: "执行完成", status: "done" },
        ],
        edges: [
          { from: "event-0", to: "conclusion-1", label: "触发" },
          { from: "evidence-2", to: "conclusion-1", label: "产生" },
          { from: "action-3", to: "evidence-2", label: "执行" },
          { from: "conclusion-1", to: "remediation-4", label: "处置" },
          { from: "remediation-4", to: "verification-5", label: "验证" },
        ],
        truncated: false,
      };
    },
    async NetDevCutoverBoard(_id: string): Promise<import("./types").NetDevCutoverBoard | null> {
      return {
        id: "CO-demo", name: "核心-SW 上联迁移", status: "running", deadline: "08-30 04:00", remaining_sec: 1800,
        frozen: 2, rollback_ready: true, rollback_note: "快照 2 台",
        steps: [
          { label: "预检", status: "done", device: "SW-01" },
          { label: "配置下发", status: "running", device: "SW-02", proposal_id: "P-9" },
          { label: "验证门", status: "pending", gate: true, device: "R-05" },
        ],
        devices: [
          { device: "SW-01", status: "done", rollback_ready: true },
          { device: "SW-02", status: "running", rollback_ready: true },
        ],
        jobs: [], audit: [{ time: "02:31:05", device: "SW-02", command: "shut x/0/1", status: "ok" }],
        has_active: true, found: true,
      };
    },
    async NetDevDiscoveryBoard(): Promise<import("./types").NetDevDiscoveryBoard | null> {
      return {
        leads: 37, fingerprinted: 24, pending: 12, promoted: 8, managed: 22,
        subnets_done: 6, subnets_total: 9, layer_depth: 2, max_hops: 2,
        run_status: "done", run_vantage: "NAS", run_updated_at: "08-29 09:05",
        funnel: [
          { key: "leads", count: 37 }, { key: "fingerprinted", count: 24 }, { key: "pending", count: 12 },
          { key: "promoted", count: 8 }, { key: "managed", count: 22 },
        ],
        layers: [{ layer: 0, label: "L0", note: "1 台" }, { layer: 1, label: "L1", note: "14 条线索" }],
        tri_source: { design: 41, plan: 38, matched: 36, only_design: 5, only_plan: 2 },
        port_events: [{ at: "08-29 09:04:11", ip: "10.1.1.5", port: 445, kind: "newly-opened" }],
      };
    },
    async NetDevConsumeDeepLink(): Promise<{ kind: string; id: string } | null> { return null; },
    async NetDevSyslogCounts(): Promise<import("./types").NetDevSyslogCountRow[]> {
      return [{ hour: "2026-08-31T10", device: "SW-03", class: "ospf-adjacency", n: 14 }, { hour: "2026-08-31T10", device: "SW-01", class: "link-flap", n: 3 }];
    },
    async NetDevTopoReconcile(): Promise<import("./types").NetDevTopoReconcile | null> {
      return { tri: { design: 41, plan: 38, matched: 36, only_design: 5, only_plan: 2 }, platforms: { lldp: 18, cdp: 4, design: 12 } };
    },
    async NetDevExposureBoard(): Promise<import("./types").NetDevExposureBoard | null> {
      return {
        simulated: true, generated_at: "08-29 10:00", critical: 1, warning: 2,
        paths: [], cut_suggestions: [], exposure_points: [],
        matrix: [{ device: "EDGE-1", critical: 1, warning: 0, info: 0, cve_critical: 0, cve_high: 0, managed: true }],
        cve_needs_feed: true, unmanaged_ends: 0, max_hops: 0,
      };
    },
    async NetDevTopologyPlan() {
      // Browser-dev stand-in for the LOCAL IP-plan view: managed devices only,
      // tiers/subnets inferred from the inventory, zero edges (links are never
      // invented from IPs — only the measured sweep draws them).
      const P = (name: string, ip: string, tier: number, subnet: string): NetDevTopologyNode => ({ name, managed: true, device_ip: ip, tier, subnet });
      return {
        nodes: [
          P("FW-01", "10.0.0.1", 0, "10.0.0.0/24"),
          P("CORE-01", "10.0.0.2", 0, "10.0.0.0/24"), P("CORE-02", "10.0.0.3", 0, "10.0.0.0/24"),
          P("AGG-01", "10.0.1.1", 1, "10.0.1.0/24"), P("AGG-02", "10.0.1.2", 1, "10.0.1.0/24"),
          P("ACC-01", "10.0.2.1", 2, "10.0.2.0/24"), P("ACC-02", "10.0.2.2", 2, "10.0.2.0/24"), P("ACC-03", "10.0.2.3", 2, "10.0.2.0/24"),
        ],
        edges: [],
        at: new Date().toISOString().slice(0, 19).replace("T", " "),
      };
    },
    async NetDevApproveProposal(_id: string, _confirm2: boolean) { throw new Error("browser dev mock: no proposal backend"); },
    async NetDevRejectProposal(_id: string, _reason: string) { throw new Error("browser dev mock: no proposal backend"); },
    async NetDevDeleteProposal(_id: string) { throw new Error("browser dev mock: no proposal backend"); },
    async NetDevExecuteProposal(_id: string) { throw new Error("browser dev mock: no proposal backend"); },
    async NetDevRollbackProposal(_id: string) { throw new Error("browser dev mock: no proposal backend"); },
    async NetDevAuditTail(_n: number) {
      return [] as NetDevAuditEntryView[];
    },
    async NetDevSSHImportCandidates() {
      return [] as NetDevSSHImportCandidate[];
    },
    async Platform() {
      const override = browserPlatformOverride();
      if (override) return override;
      // Mirror the OS the browser dev mock runs on.
      const ua = typeof navigator !== "undefined" ? navigator.userAgent : "";
      if (/Win/i.test(ua)) return "windows";
      if (/Mac/i.test(ua)) return "darwin";
      return "linux";
    },
        async Submit(input) {
          cancelled = false;
      emitMockTurnStarted();
      const trimmedInput = input.trim().toLowerCase();
      // Browser dev-shell demo: reply with a mermaid diagram so the rendering
      // pipeline (Markdown → MermaidViewer → SVG in the chat bubble) can be
      // verified without the Go backend. 流程 → decision flowchart, otherwise a
      // topology graph.
      if (trimmedInput.startsWith("mermaid") || trimmedInput.includes("画")) {
        let chart: string;
        let caption: string;
        if (trimmedInput.includes("流程")) {
          chart = [
            "flowchart TD",
            "  AL[端口 down 告警] --> Q1{链路灯亮?}",
            "  Q1 -- 不亮 --> HW[检查光模块/线缆并更换]",
            "  HW --> Q2{恢复?}",
            "  Q2 -- 否 --> RMA[报硬件更换]",
            "  Q1 -- 亮 --> Q3{对端 MAC 学习?}",
            "  Q3 -- 无 --> CFG[查端口配置 shutdown/VLAN]",
            "  Q3 -- 有 --> STP[查 STP 状态/环路]",
            "  Q2 -- 是 --> OK[业务恢复 关闭工单]",
            "  CFG --> OK",
            "  STP --> OK",
            "  RMA --> OK",
          ].join("\n");
          caption = "端口故障排查流程（mermaid flowchart 演示）";
        } else {
          chart = [
            "graph LR",
            "  CORE1[核心 SW1] --- AGG1[汇聚 SW1]",
            "  CORE1 --- AGG2[汇聚 SW2]",
            "  AGG1 --- ACC1[接入 SW1]",
            "  AGG1 --- ACC2[接入 SW2]",
            "  AGG2 --- ACC3[接入 SW3]",
            "  FW[防火墙] --- CORE1",
          ].join("\n");
          caption = "全网拓扑示意（mermaid 渲染演示）";
        }
        const reply = caption + "：\n\n```mermaid\n" + chart + "\n```\n";
        await delay(400);
        if (cancelled) return;
        emit({ kind: "message", text: reply });
        emitMockTurnDone();
        return;
      }
      const goalMatch = /^\/goal(?:\s+([\s\S]*))?$/.exec(input.trim());
      if (goalMatch) {
        const arg = (goalMatch[1] ?? "").trim();
        const lowered = arg.toLowerCase();
        const active = mockTabs.find((tab) => tab.active);
        if (!arg || lowered === "status") {
          emit({ kind: "notice", level: "info", text: active?.goal ? `goal: ${active.goal}` : "goal: none" });
          emitMockTurnDone();
          return;
        }
        if (["clear", "off", "stop", "done"].includes(lowered)) {
          mockTabs = mockTabs.map((tab) => (tab.active ? { ...tab, goal: "", goalStatus: "stopped", collaborationMode: "normal" } : tab));
          emit({ kind: "notice", level: "info", text: "goal cleared" });
          emitMockTurnDone();
          return;
        }
        mockTabs = mockTabs.map((tab) => (tab.active ? { ...tab, goal: arg, goalStatus: "running", collaborationMode: "goal" } : tab));
        emit({ kind: "notice", level: "info", text: `goal set: ${arg}` });
        await delay(350);
        if (cancelled) return;
        const reply = `Autonomous goal run started for: **${arg}**\n\nMock run completed.\n\n[goal:complete]`;
        emit({ kind: "message", text: reply });
        mockTabs = mockTabs.map((tab) => (tab.active ? { ...tab, goal: "", goalStatus: "complete", collaborationMode: "normal" } : tab));
        emit({ kind: "notice", level: "info", text: "goal complete" });
        emitMockTurnDone();
        return;
      }
      if (trimmedInput === "/approve-preview" || trimmedInput === "approve preview" || trimmedInput === "approve预览") {
        pendingApprovalPreview = true;
        await delay(250);
        if (cancelled) return;
        emit({
          kind: "approval_request",
          approval: {
            id: "mock-approval-preview",
            tool: "bash",
            subject: t("mock.approvalSubject"),
          },
        });
        return;
      }
      if (
        trimmedInput === "/plan-approve-preview" ||
        trimmedInput === "plan approve preview" ||
        trimmedInput === "plan approve预览"
      ) {
        pendingApprovalPreview = true;
        await delay(250);
        if (cancelled) return;
        emit({
          kind: "approval_request",
          approval: {
            id: "mock-plan-approval-preview",
            tool: "exit_plan_mode",
            subject: "",
          },
        });
        return;
      }
      if (trimmedInput === "/ask-preview" || trimmedInput === "ask preview" || trimmedInput === "ask预览") {
        pendingAskPreview = true;
        await delay(250);
        if (cancelled) return;
        emit({
          kind: "ask_request",
          ask: {
            id: "mock-ask-preview",
            questions: [
              {
                id: "q1",
                header: t("mock.askQ1Header"),
                prompt: t("mock.askQ1Prompt"),
                options: [
                  { label: t("mock.askQ1Opt1Label"), description: t("mock.askQ1Opt1Desc") },
                  { label: t("mock.askQ1Opt2Label"), description: t("mock.askQ1Opt2Desc") },
                  { label: t("mock.askQ1Opt3Label"), description: t("mock.askQ1Opt3Desc") },
                ],
              },
              {
                id: "q2",
                header: t("mock.askQ2Header"),
                prompt: t("mock.askQ2Prompt"),
                options: [
                  { label: t("mock.askQ2Opt1Label"), description: t("mock.askQ2Opt1Desc") },
                  { label: t("mock.askQ2Opt2Label"), description: t("mock.askQ2Opt2Desc") },
                  { label: t("mock.askQ2Opt3Label"), description: t("mock.askQ2Opt3Desc") },
                ],
              },
            ],
          },
        });
        return;
      }
      if (trimmedInput === "/todo-preview" || trimmedInput === "todo preview" || trimmedInput === "todo预览") {
        await delay(250);
        if (cancelled) return;
        emit({
          kind: "tool_dispatch",
          tool: {
            id: "mock-todo-preview",
            name: "todo_write",
            args: JSON.stringify({
              todos: [
                { content: t("mock.todo1"), status: "completed" },
                { content: t("mock.todo2"), activeForm: t("mock.todo2ActiveForm"), status: "in_progress" },
                { content: t("mock.todo3"), status: "pending" },
              ],
            }),
            readOnly: false,
          },
        });
        await delay(150);
        emit({
          kind: "tool_result",
          tool: {
            id: "mock-todo-preview",
            name: "todo_write",
            args: JSON.stringify({
              todos: [
                { content: t("mock.todo1"), status: "completed" },
                { content: t("mock.todo2"), activeForm: t("mock.todo2ActiveForm"), status: "in_progress" },
                { content: t("mock.todo3"), status: "pending" },
              ],
            }),
            output: "todo list updated",
            readOnly: false,
            durationMs: 150,
          },
        });
        emitMockTurnDone();
        return;
      }
      if (trimmedInput === "/process-preview" || trimmedInput === "process preview" || trimmedInput === "过程预览") {
        await delay(200);
        if (cancelled) return;
        emit({ kind: "phase", text: "Preparing context" });
        await delay(120);
        emit({ kind: "notice", level: "info", text: "Loaded project instructions from AGENTS.md." });
        await delay(120);
        emit({ kind: "notice", level: "warn", text: "Network access is enabled; external results may change over time." });
        await delay(120);
        emit({ kind: "compaction_started", compaction: { trigger: "manual" } });
        await delay(320);
        emit({
          kind: "compaction_done",
          compaction: {
            trigger: "manual",
            messages: 6,
            summary: "Preserved the active task, relevant files, and UI decisions while trimming earlier exploratory context.",
          },
        });
        emit({ kind: "message", text: "Process card preview complete." });
        emitMockTurnDone();
        return;
      }
      // Simulate the server's pre-first-token latency so the deferred user bubble
      // and the "un-send on Esc before any reply" path are observable in browser
      // dev. Bail if cancelled during the wait — nothing was streamed yet.
      await delay(700);
      if (cancelled) return;
      const reply =
        `You said: **${input}**\n\n` +
        "This is the browser dev mock — the real reply comes from the kernel " +
        "inside the Wails shell. Here's a fenced block to exercise the editor seam:\n\n" +
        "```go\nfunc main() {\n    println(\"hello from the mock\")\n}\n```\n";
      for (const ch of reply) {
        if (cancelled) break;
        emit({ kind: "text", text: ch });
        await delay(6);
      }
      emit({ kind: "message", text: reply });
      emit({
        kind: "tool_dispatch",
        tool: {
          id: "t1",
          name: "edit_file",
          args: '{"path":"main.go","old_string":"println(\\"hi\\")","new_string":"println(\\"hello\\")"}',
          readOnly: false,
        },
      });
      await delay(350);
      emit({
        kind: "tool_result",
        tool: { id: "t1", name: "edit_file", output: "edited main.go", readOnly: false, durationMs: 350 },
      });
      emit({
        kind: "usage",
        usage: {
          promptTokens: 1280,
          completionTokens: 64,
          totalTokens: 1344,
          cacheHitTokens: 0,
          cacheMissTokens: 0,
          sessionCacheHitTokens: 0,
          sessionCacheMissTokens: 0,
        },
      });
          emitMockTurnDone();
        },
        async SubmitToTab(_tabID, input) {
          await withMockTabScope(_tabID, () => this.Submit(input));
        },
        async SubmitDisplay(_display, input) {
          await this.Submit(input);
        },
        async SubmitDisplayToTab(_tabID, display, input) {
          await withMockTabScope(_tabID, () => this.SubmitDisplay(display, input));
        },
        async RunShell(command) {
          cancelled = false;
          emitMockTurnStarted();
          await delay(100);
          if (cancelled) return;
          const id = `shell-${command.slice(0, 32)}`;
          emit({ kind: "tool_dispatch", tool: { id, name: "bash", args: JSON.stringify({ command }), readOnly: false } });
          await delay(200);
          if (cancelled) return;
          emit({ kind: "tool_progress", tool: { id, name: "bash", output: `$ ${command}\n(mock output)\n`, readOnly: false } });
          await delay(100);
          if (cancelled) return;
          emit({ kind: "tool_result", tool: { id, name: "bash", output: `$ ${command}\n(mock output)\n`, readOnly: false, durationMs: 300 } });
          emitMockTurnDone();
        },
        async RunShellForTab(_tabID, command) {
          await withMockTabScope(_tabID, () => this.RunShell(command));
        },
        async Steer(_text) {
          // Mock: emit a steer event as confirmation in the transcript.
          emit({ kind: "steer", text: _text });
        },
        async SteerForTab(_tabID, _text) {
          await this.Steer(_text);
        },
        async FollowUp() {},
        async FollowUpForTab() {},
        async PTYCreate() { return 0; },
        async PTYCreateForTab() { return 0; },
        async ListWSLDistros() { return []; },
        async ListDockerContainers() { return []; },
        async SSHConnect() { throw new Error("ssh connect unavailable in browser mode"); },
        async SetSSHSecret() { /* browser mock: no secret store */ },
        async ServerConnect() { throw new Error("server connect unavailable in browser mode"); },
        async ServerForget() {},
        async SSHInspectHost() { throw new Error("ssh unavailable in browser mode"); },
        async SSHTrustHost() {},
        async OpenRemoteTopicTab() { return null; },
        async RemoteConnectProbe() { throw new Error("remote connect unavailable in browser mode"); },
        async RemoteBrowseList() { return []; },
        async RemoteWizardClose() {},
        async OpenRemoteTab() { return null; },
        async PTYWrite() {},
        async PTYRead() { return ["", false]; },
        async PTYResize() {},
        async PTYKill() {},
        async PTYAlive() { return false; },
        async QueuedMessages() {
          return { steer: [], followUp: [] };
        },
        async Cancel() {
          cancelled = true;
          emitMockTurnDone();
        },
        async CancelTab(_tabID) {
          await withMockTabScope(_tabID, () => this.Cancel());
        },
        async Pause() {
          // Mock: surface a pause notice so the UI flow is testable without a backend.
          emit({ kind: "notice", level: "info", text: "（预览）已暂停" });
        },
        async PauseTab(_tabID) {
          await withMockTabScope(_tabID, () => this.Pause());
        },
        async ResumeTurn() {
          emit({ kind: "notice", level: "info", text: "（预览）已恢复" });
        },
        async ResumeTurnTab(_tabID) {
          await withMockTabScope(_tabID, () => this.ResumeTurn());
        },
        async PausedTab(_tabID) {
          return false;
        },
        async Approve(_id, allow, session, persist) {
          if (!pendingApprovalPreview) return;
          pendingApprovalPreview = false;
          const suffix = persist ? "grant saved" : session ? "grant active this session" : "allowed once";
          emit({
            kind: "message",
            text: `approval preview answered: ${allow ? suffix : "denied"}`,
          });
          emitMockTurnDone();
        },
        async ApproveTab(_tabID, id, allow, session, persist) {
          await withMockTabScope(_tabID, () => this.Approve(id, allow, session, persist));
        },
        async AnswerQuestion(_id, answers) {
      if (!pendingAskPreview) return;
      pendingAskPreview = false;
      const summary = answers
        .map((answer) => `${answer.questionId}: ${(answer.selected ?? []).join(", ") || "(no answer)"}`)
        .join("\n");
      emit({ kind: "message", text: `ask preview answered:\n\n${summary}` });
          emitMockTurnDone();
        },
        async AnswerQuestionForTab(_tabID, id, answers) {
          await withMockTabScope(_tabID, () => this.AnswerQuestion(id, answers));
        },
        async ReplayPendingPrompts() {},
        async ConfirmAction(req) {
          void req;
          return false;
        },
        // SetPlanMode is retained for binding parity but unused by the frontend
        // (use SetModeForTab / SetCollaborationModeForTab instead). Stub so the
        // mock satisfies the interface contract.
        async SetPlanMode() {},
        async SetMode(mode) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetModeForTab(active.id, mode);
        },
        async SetModeForTab(tabID, mode) {
          const nextMode = normalizeMode(mode);
          mockTabs = mockTabs.map((tab) =>
            tab.id === tabID
              ? {
                  ...tab,
                  mode: nextMode,
                  collaborationMode: normalizeCollaborationMode(undefined, tab.goal, nextMode),
                  toolApprovalMode: normalizeToolApprovalMode(undefined, nextMode),
                }
              : tab,
          );
        },
        async SetCollaborationMode(mode) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetCollaborationModeForTab(active.id, mode);
        },
        async SetCollaborationModeForTab(tabID, mode) {
          const next = normalizeCollaborationMode(mode);
          mockTabs = mockTabs.map((tab) => {
            if (tab.id !== tabID) return tab;
            const toolMode = normalizeToolApprovalMode(tab.toolApprovalMode, normalizeMode(tab.mode));
            return {
              ...tab,
              collaborationMode: next,
              goal: next === "normal" || next === "plan" ? "" : tab.goal,
              mode: modeWithPlan(modeWithAutoApproveTools(normalizeMode(tab.mode), toolMode === "yolo"), next === "plan"),
            };
          });
        },
        async SetToolApprovalMode(mode) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetToolApprovalModeForTab(active.id, mode);
        },
        async SetToolApprovalModeForTab(tabID, mode) {
          const next = normalizeToolApprovalMode(mode);
          settings.autoApproveTools = next === "yolo";
          settings.bypass = next === "yolo";
          mockTabs = mockTabs.map((tab) =>
            tab.id === tabID
              ? {
                  ...tab,
                  toolApprovalMode: next,
                  mode: modeWithAutoApproveTools(normalizeMode(tab.mode), next === "yolo"),
                }
              : tab,
          );
        },
        async SetRagScope(scope) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetRagScopeForTab(active.id, scope);
        },
        async SetRagScopeForTab(tabID, scope) {
          mockTabs = mockTabs.map((tab) =>
            tab.id === tabID ? { ...tab, ragScope: scope.trim() } : tab,
          );
        },
        async SetGoal(goal) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetGoalForTab(active.id, goal);
        },
        async SetGoalForTab(tabID, goal) {
          const nextGoal = goal.trim();
          mockTabs = mockTabs.map((tab) =>
            tab.id === tabID
              ? {
                  ...tab,
                  goal: nextGoal,
                  goalStatus: nextGoal ? "running" : "stopped",
                  collaborationMode: nextGoal ? "goal" : "normal",
                  mode: modeWithPlan(normalizeMode(tab.mode), false),
                }
              : tab,
          );
        },
        async ClearGoal() {
          await this.SetGoal("");
        },
        async ClearGoalForTab(tabID) {
          await this.SetGoalForTab(tabID, "");
        },
        async Compact() {},
        async NewSession() {},
        async ClearSession() {},
    async Checkpoints() {
      return [
        { turn: 0, prompt: "你好呀", files: ["src/App.tsx"], time: Date.now() - 30_000, canCode: true, canConversation: true },
      ];
    },
    async CheckpointsForTab() {
      return this.Checkpoints();
    },
        async SearchSessionText() {
          return [];
        },
        async TurnFactsForTab() {
          return [];
        },
        async BranchesForTab() {
          return [];
        },
        async SwitchBranchForTab() {},
    async CheckpointDiffForTab() {
      return [];
    },
    async RewindPreviewForTab(): Promise<RewindPreviewFileView[]> {
      return [];
    },
    async Rewind() {},
    async Fork() {
      const active = mockTabs.find((tab) => tab.active) ?? mockTabs[0];
      const tab: TabMeta = {
        ...active,
        id: "tab_fork_" + Date.now(),
        topicId: "topic_fork_" + Date.now(),
        topicTitle: `${active.topicTitle || t("rewind.fork")} · fork`,
        active: true,
        running: false,
      };
      mockTabs = [...mockTabs.map((item) => ({ ...item, active: false })), tab];
      return { ...tab };
    },
    async SummarizeFrom() {},
    async SummarizeUpTo() {},
        async HistoryForTab(tabID?: string) {
          const tab = mockTabs.find((item) => item.id === tabID) ?? mockTabs.find((item) => item.active);
          if (tab?.topicId) {
            queueMockTopicRuntime(tab);
            return mockTopicHistory(tab.topicId);
          }
          return [];
        },
        async PresentForTab(_tabID?: string) {
          return { records: [], rewriteVersion: 0 };
        },
    async ListSessions() {
      return sessions.map((s) => ({ ...s }));
    },
    async ListSessionsForProfile(profile?: string) {
      const key = (profile ?? "").trim().toLowerCase();
      if (!key) return sessions.map((s) => ({ ...s }));
      return sessions.filter((s) => ((s.profile ?? "dev").toLowerCase() === key)).map((s) => ({ ...s }));
    },
    async ListTrashedSessions() {
      return trashedSessions.map((s) => ({ ...s }));
    },
    async ListTrashedSessionsForProfile(profile?: string) {
      const key = (profile ?? "").trim().toLowerCase();
      if (!key) return trashedSessions.map((s) => ({ ...s }));
      return trashedSessions.filter((s) => ((s.profile ?? "dev").toLowerCase() === key)).map((s) => ({ ...s }));
    },
    async ResumeSession(path: string) {
      sessions.forEach((s) => {
        s.current = s.path === path;
        s.open = s.open || s.path === path;
      });
      return [
        { role: "user", content: `(mock) resumed ${path}` },
        { role: "assistant", content: "This is a mock resumed transcript — the real one comes from the kernel." },
      ];
    },
    async ResumeSessionForTab(_tabID: string, path: string) {
      return this.ResumeSession(path);
    },
    async PreviewSession(path: string) {
      const s = sessions.find((x) => x.path === path) ?? trashedSessions.find((x) => x.path === path);
      return [
        { role: "user", content: s?.preview || `(mock) preview ${path}` },
        { role: "phase", content: "Preparing read-only preview" },
        {
          role: "assistant",
          content: "This is a read-only mock preview. The active conversation is unchanged.",
          reasoning: "Preview reads the saved session without resuming it.",
        },
        { role: "notice", level: "info", content: "Preview mode keeps the active conversation untouched." },
        { role: "compaction", content: "", trigger: "manual", messages: 3, summary: "Mock preview preserved the latest task, tool result, and answer summary." },
      ];
    },
    async DeleteSession(path: string) {
      const i = sessions.findIndex((s) => s.path === path);
      if (i >= 0) {
        const [s] = sessions.splice(i, 1);
        trashedSessions.unshift({
          ...s,
          current: false,
          open: false,
          path: s.path.replace("/mock/sessions/", "/mock/sessions/.trash/"),
          deletedAt: Date.now(),
        });
      }
    },
    async RestoreSession(path: string) {
      const i = trashedSessions.findIndex((s) => s.path === path);
      if (i >= 0) {
        const [s] = trashedSessions.splice(i, 1);
        sessions.unshift({
          ...s,
          path: s.path.replace("/mock/sessions/.trash/", "/mock/sessions/"),
          deletedAt: undefined,
        });
      }
    },
    async PurgeTrashedSession(path: string) {
      const i = trashedSessions.findIndex((s) => s.path === path);
      if (i >= 0) trashedSessions.splice(i, 1);
    },
    async RenameSession(path: string, title: string) {
      const s = sessions.find((x) => x.path === path);
      if (s) s.title = title.trim() || undefined;
    },
    async ListWorkspaces() {
      return mockProjectTree
        .filter((node) => node.kind === "project" && node.root)
        .map((node) => ({
          path: node.root!,
          name: node.label || baseName(node.root!),
          current: node.root === cwd,
        }));
    },
    async PickWorkspace() {
      // Browser dev has no native dialog; simulate picking a folder and re-root so
      // the topbar folder chip visibly changes.
      return mockSwitchWorkspace(cwd.endsWith("another-project") ? "~/projects/docs" : "~/projects/another-project");
    },
    async PickImportFolder() {
      return "~/Documents/my-import-folder";
    },
    async PickImportFiles() {
      return ["~/Documents/sample-report.pdf", "~/Documents/summary-data.xlsx"];
    },
    async SwitchWorkspace(path: string) {
      return mockSwitchWorkspace(path);
    },
    async RemoveWorkspace(path: string) {
      workspaces = workspaces.filter((p) => p !== path);
      const index = mockProjectTree.findIndex((node) => node.root === path);
      if (index >= 0) mockProjectTree.splice(index, 1);
    },
        async BudgetStatus() {
          return { rpm: 0, used: 0, remaining: 0, reserveMain: 0, windowSecs: 0 };
        },
        async ContextUsage() {
          return {
            used: 42124,
            window: 128000,
            sessionTokens: 34479,
            compactRatio: 0.8,
            sessionPromptTokens: 28120,
            sessionCompletionTokens: 6359,
            sessionReasoningTokens: 1840,
            sessionCacheHitTokens: 26480,
            sessionCacheMissTokens: 1640,
            sessionCacheWriteTokens: 2100,
            requestCount: 12,
            sessionElapsedMs: 33 * 60 * 1000,
          };
        },
        async ContextUsageForTab() {
          return this.ContextUsage();
        },
        async Jobs() {
          return []; // browser dev mock has no background jobs
        },
        async JobsForTab() {
          return this.Jobs();
        },
        async Meta() {
          const active = mockTabs.find((tab) => tab.active) ?? mockTabs[0];
          const toolApprovalMode = normalizeToolApprovalMode(active?.toolApprovalMode, active ? normalizeMode(active.mode) : "normal", settings.autoApproveTools);
          const autoApproveTools = toolApprovalMode === "yolo";
          return {
            label: active?.label ?? "",
            ready: active?.ready ?? true,
            eventChannel: EVENT_CHANNEL,
            cwd: active?.cwd || cwd,
            autoApproveTools,
            bypass: autoApproveTools,
            toolApprovalMode,
            goal: active?.goal ?? "",
            goalStatus: active?.goalStatus ?? (active?.goal ? "running" : "stopped"),
          };
        },
        async MetaForTab(tabID) {
          const tab = mockTabs.find((item) => item.id === tabID) ?? mockTabs.find((item) => item.active) ?? mockTabs[0];
          const toolApprovalMode = normalizeToolApprovalMode(tab?.toolApprovalMode, tab ? normalizeMode(tab.mode) : "normal", settings.autoApproveTools);
          const autoApproveTools = toolApprovalMode === "yolo";
          return {
            label: tab?.label ?? "",
            ready: tab?.ready ?? true,
            eventChannel: EVENT_CHANNEL,
            cwd: tab?.cwd || cwd,
            autoApproveTools,
            bypass: autoApproveTools,
            toolApprovalMode,
            goal: tab?.goal ?? "",
            goalStatus: tab?.goalStatus ?? (tab?.goal ? "running" : "stopped"),
          };
        },
    async Commands() {
      return [
        { name: "new", description: "start new session; save transcript", kind: "builtin" as const },
        { name: "clear", description: "discard current context", kind: "builtin" as const },
        { name: "compact", description: "Summarize older history to free up context", kind: "builtin" as const },
        { name: "model", description: "Switch model", kind: "builtin" as const },
        { name: "effort", description: "Set reasoning effort", kind: "builtin" as const },
        { name: "skill", description: "List skills", kind: "builtin" as const },
        { name: "explore", description: "Investigate the codebase in an isolated subagent", kind: "skill" as const },
        { name: "review", description: "Review the staged diff", hint: "[focus]", kind: "custom" as const },
      ];
    },
    async Capabilities() {
      return {
        servers: capServers.map((s) => ({ ...s })),
        skills: capSkills.map((s) => ({ ...s })),
        skillRoots: capSkillRoots.map((s) => ({ ...s })),
      };
    },
    async ImportMCPServersJSON(_jsonText: string) { return 0; },
    async AddMCPServer(input: MCPServerInput) {
      const tools = input.transport === "stdio" ? 3 : 5;
      capServers.push({
        name: input.name,
        transport: input.transport,
        status: "connected",
        configured: true,
        autoStart: true,
        tier: "background",
        command: input.command,
        args: input.args,
        url: input.url,
        tools,
        prompts: 0,
        resources: 0,
        toolList: Array.from({ length: tools }, (_, i) => ({
          name: `${input.name}_tool_${i + 1}`,
          description: `Mock tool ${i + 1} exposed by ${input.name}.`,
        })),
      });
      return tools;
    },
    async UpdateMCPServer(name: string, input: MCPServerInput) {
      capServers = capServers.map((s) => {
        if (s.name !== name) return s;
        const connected = s.status === "connected" || s.status === "failed" || s.tier !== "lazy";
        const nextStatus = s.status === "disabled" ? "disabled" : connected ? "connected" : "deferred";
        const nextTools = nextStatus === "connected" ? s.tools || (input.transport === "stdio" ? 3 : 5) : 0;
        return {
          ...s,
          transport: input.transport,
          status: nextStatus,
          command: input.transport === "stdio" ? input.command : "",
          args: input.transport === "stdio" ? input.args : [],
          url: input.transport === "stdio" ? "" : input.url,
          envKeys: input.env ? Object.keys(input.env).sort() : s.envKeys,
          tools: nextTools,
          error: undefined,
          authStatus: nextStatus !== "connected" && input.transport !== "stdio" ? "possible" : undefined,
          authUrl: nextStatus !== "connected" && input.transport !== "stdio" ? input.url : undefined,
        };
      });
    },
    async RemoveMCPServer(name: string) {
      capServers = capServers.filter((s) => s.name !== name);
    },
    async ReconnectMCPServer(name: string) {
      capServers = capServers.map((s) =>
        s.name === name
          ? { ...s, status: "initializing", error: undefined, authStatus: undefined, authUrl: undefined }
          : s,
      );
      await new Promise((r) => setTimeout(r, 400));
      capServers = capServers.map((s) =>
        s.name === name ? { ...s, status: "connected", tools: s.tools || 4 } : s,
      );
    },
    async ClearMCPServerAuthentication(name: string) {
      capServers = capServers.map((s) =>
        s.name === name
          ? {
              ...s,
              status: s.tier === "background" || s.tier === "eager" ? "initializing" : "deferred",
              tools: 0,
              error: undefined,
              authStatus: s.transport !== "stdio" ? "possible" : undefined,
              authUrl: s.transport !== "stdio" ? s.url : undefined,
              authConfigured: undefined,
            }
          : s,
      );
    },
    async PickSkillFolder() {
      return "~/my-skills";
    },
    async PickDirectory(_title: string) {
      return "~/selected-folder";
    },
    async AddSkillPath(path: string) {
      const dir = path.trim() || "~/my-skills";
      if (!capSkillRoots.some((r) => r.scope === "custom" && r.dir === dir)) {
        capSkillRoots.push({
          dir,
          scope: "custom",
          priority: capSkillRoots.length + 1,
          status: "ok",
          configured: true,
          removable: true,
          skills: 1,
          skillItems: [{ name: "local-dev", description: "Local custom development workflow", scope: "custom", runAs: "inline" }],
        });
      }
      if (!capSkills.some((s) => s.name === "local-dev")) {
        capSkills.push({ name: "local-dev", description: "Local custom development workflow", scope: "custom", runAs: "inline", enabled: true });
      }
    },
    async RemoveSkillPath(path: string) {
      capSkillRoots = capSkillRoots.filter((r) => r.dir !== path);
      if (!capSkillRoots.some((r) => r.scope === "custom")) {
        const idx = capSkills.findIndex((s) => s.name === "local-dev");
        if (idx >= 0) capSkills.splice(idx, 1);
      }
    },
    async RefreshSkills() {},
    async SetSkillEnabled(name: string, enabled: boolean) {
      const skill = capSkills.find((s) => s.name === name);
      if (skill) skill.enabled = enabled;
    },
    async DeriveEditableSkill(_name: string): Promise<string> {
      return "";
    },
    async SkillMarketBrowse(): Promise<CatalogEntry[]> {
      return [
        { source: "builtin", name: "pdf", slug: "pdf", description: "Create and analyze PDF documents", topics: ["office"], installs: 0, contentUrl: "", installRef: "" },
        { source: "builtin", name: "docx", slug: "docx", description: "Create and edit Word documents", topics: ["office"], installs: 0, contentUrl: "", installRef: "" },
        { source: "builtin", name: "skill-creator", slug: "skill-creator", description: "Create and edit skills", topics: ["meta"], installs: 0, contentUrl: "", installRef: "" },
      ];
    },
    async SkillMarketSources(): Promise<MarketSourceMeta[]> {
      return [
        { id: "builtin", name: "Curated", type: "builtin-catalog" },
        { id: "anthropics", name: "Anthropic Skills", type: "github-repo" },
        { id: "openai", name: "OpenAI Skills", type: "github-repo" },
        { id: "clawhub", name: "ClawHub Community", type: "clawhub-api" },
      ];
    },
    async SkillMarketSearch(_query: string, _source?: string): Promise<CatalogEntry[]> {
      return [
        { source: "clawhub", name: "code-review", slug: "code-review", description: "Code review skill", installs: 42, contentUrl: "https://example.com/SKILL.md", installRef: "https://example.com/SKILL.md" },
        { source: "builtin", name: "pdf", slug: "pdf", description: "PDF tools", installs: 0, contentUrl: "", installRef: "" },
      ];
    },
    async MCPRegistrySearch(_query: string): Promise<MCPRegistryView> {
      return {
        servers: [
          { name: "io.example/mock", suggestedName: "mock", title: "Mock Server", description: "Mock MCP Registry entry", installable: true, transport: "stdio", command: "npx", args: ["-y", "@example/mock-mcp"] },
        ],
        cached: false,
      };
    },
    async MCPRegistryResolve(name: string): Promise<MCPRegistryEntry> {
      return { name, suggestedName: name, installable: true, transport: "stdio", command: "npx", args: ["-y", "@example/mock-mcp"] };
    },
    async SkillMarketInstall(_installRef: string, _name: string, _scope: string, _apply: boolean): Promise<string> {
      return "Installed (dev mock)";
    },
    async SkillMarketUninstall(_name: string, _scope: string): Promise<string> {
      await delay(500);
      return "Uninstall successful";
    },
    async SkillMarketInstalledNames(): Promise<Record<string, string>> {
      await delay(100);
      return {};
    },
    async DreamStatus(): Promise<DreamStatusView> {
      return {
        enabled: dreamMock.enabled,
        dreamInterval: dreamMock.dreamInterval,
        distillInterval: dreamMock.distillInterval,
        dreamInFlight: dreamMock.dreamInFlight,
        distillInFlight: dreamMock.distillInFlight,
        lastDream: dreamMock.lastDream,
        lastDistill: dreamMock.lastDistill,
        history: dreamMock.history,
      };
    },
    async SetDreamEnabled(enabled: boolean) {
      dreamMock.enabled = enabled;
    },
    async SetDreamIntervals(dreamDays: number, distillDays: number) {
      dreamMock.dreamInterval = dreamDays;
      dreamMock.distillInterval = distillDays;
    },
    async TriggerDream(): Promise<DreamRunView> {
      const run: DreamRunView = {
        kind: "dream",
        trigger: "manual",
        startedAt: new Date().toISOString(),
        duration: "2s",
        status: "ok",
      };
      dreamMock.lastDream = run;
      dreamMock.history = [run, ...dreamMock.history].slice(0, 20);
      return run;
    },
    async TriggerDistill(): Promise<DreamRunView> {
      const run: DreamRunView = {
        kind: "distill",
        trigger: "manual",
        startedAt: new Date().toISOString(),
        duration: "3s",
        status: "ok",
      };
      dreamMock.lastDistill = run;
      dreamMock.history = [run, ...dreamMock.history].slice(0, 20);
      return run;
    },
    async SetMCPServerEnabled(name: string, enabled: boolean) {
      capServers = capServers.map((s) =>
        s.name === name
          ? {
              ...s,
              status: enabled ? "connected" : "disabled",
              autoStart: s.builtIn ? enabled : s.autoStart,
              tools: enabled ? s.tools || 4 : 0,
              error: undefined,
              authStatus: !enabled && s.transport !== "stdio" ? "possible" : undefined,
              authUrl: !enabled && s.transport !== "stdio" ? s.url : undefined,
            }
          : s,
      );
    },
    async SetMCPServerTier(name: string, tier: string) {
      capServers = capServers.map((s) => {
        if (s.name !== name) return s;
        if (tier === "lazy") return { ...s, tier, autoStart: true };
        const tools = s.tools || (s.transport === "stdio" ? 3 : 5);
        return { ...s, tier, autoStart: true, status: "connected", tools, error: undefined, authStatus: undefined, authUrl: undefined };
      });
    },
    async SlashArgs(input: string) {
      // Mirror a slice of the real arg hints so the menu is exercisable in browser dev.
      const from = input.lastIndexOf(" ") + 1;
      const cur = input.slice(from);
      const cmd = input.slice(0, input.indexOf(" ") < 0 ? input.length : input.indexOf(" "));
      const subs: Record<string, { label: string; insert: string; hint: string; descend?: boolean }[]> = {
        "/skill": [
          { label: "list", insert: "list", hint: "list skills" },
          { label: "show", insert: "show ", hint: "show a skill's body", descend: true },
          { label: "enable", insert: "enable ", hint: "enable a disabled skill", descend: true },
          { label: "disable", insert: "disable ", hint: "disable an enabled skill", descend: true },
          { label: "new", insert: "new ", hint: "scaffold a new skill" },
          { label: "paths", insert: "paths", hint: "show discovery paths" },
        ],
        "/hooks": [
          { label: "list", insert: "list", hint: "list active hooks" },
          { label: "trust", insert: "trust", hint: "trust this project's hooks" },
        ],
        "/model": [
          { label: "openai/gpt-4o", insert: "openai/gpt-4o", hint: "current" },
        ],
        "/effort": [
          { label: "auto", insert: "auto", hint: "use the model default" },
          { label: "low", insert: "low", hint: "lightweight reasoning" },
          { label: "medium", insert: "medium", hint: "balanced reasoning" },
          { label: "high", insert: "high", hint: "deeper reasoning" },
        ],
      };
      const items = (subs[cmd] ?? [])
        .filter((it) => it.label.toLowerCase().startsWith(cur.toLowerCase()))
        .map((it) => ({ label: it.label, insert: it.insert, hint: it.hint, descend: it.descend ?? false }));
      return { items, from };
    },
    async ListDir(rel: string) {
      // A tiny fake tree so the @ menu is navigable in browser dev.
      if (rel === "" || rel === "./") {
        return [
          { name: "internal", isDir: true },
          { name: "desktop", isDir: true },
          { name: "README.md", isDir: false },
          { name: "go.mod", isDir: false },
        ];
      }
      if (rel === "internal/") {
        return [
          { name: "control", isDir: true },
          { name: "boot", isDir: true },
          { name: "event.go", isDir: false },
        ];
      }
      return [{ name: "file.go", isDir: false }];
    },
    async SearchFileRefs(query: string) {
      const q = query.toLowerCase();
      return ["desktop/frontend/src/lib/bridge.ts", "frontend/wailsjs/runtime/runtime.js", "internal/control/refs.go"]
        .filter((path) => path.split("/").pop()?.toLowerCase().includes(q))
        .map((name) => ({ name, isDir: false }));
    },
    async ReadFile(rel: string) {
      const samples: Record<string, string> = {
        "README.md": "# fairpeer\n\nBrowser-dev workspace preview.\n\n- Chat in the center\n- Browse files on the right\n- Keep sessions on the left\n",
        "go.mod": "module fairpeer\n\ngo 1.23\n",
        "desktop/file.go": "package desktop\n\nfunc main() {\n\tprintln(\"workspace preview\")\n}\n",
        "internal/event.go": "package internal\n\n// mock file used by the browser dev seam\n",
      };
      return {
        path: rel,
        body: samples[rel] ?? `// ${rel}\n\nMock file body from browser dev.`,
        size: samples[rel]?.length ?? 42,
        truncated: false,
        binary: false,
      };
    },
    async WorkspaceChanges() {
      return {
        gitAvailable: true,
        gitBranch: "main",
        added: 142,
        removed: 37,
        untracked: 1,
        files: [
          {
            path: "desktop/frontend/src/components/WorkspacePanel.tsx",
            sources: ["session", "git"],
            gitStatus: "M",
            turns: [0, 2],
            latestPrompt: "Mock session edited the workspace panel.",
            latestTime: Date.now() - 60_000,
          },
          { path: "README.md", sources: ["git"], gitStatus: "??" },
          { path: "internal/control/controller.go", sources: ["session"], turns: [1], latestTime: Date.now() - 120_000 },
        ],
      };
    },
    async GitBranches() {
      return ["main", "dev", "feature/branch-switcher"];
    },
    async GitCheckout(_branch: string) {
      console.info("mock GitCheckout", _branch);
    },
    async WorkspaceGitHistory(path: string) {
      return [
        { hash: "abcdef123456", author: "Mock Author", date: new Date().toISOString(), message: "Mock commit message for " + path },
      ];
    },
    async WorkspaceGitCommitDetail(_hash: string, path: string) {
      if (path) {
        return { diff: "--- a/mock\n+++ b/mock\n@@ -1,1 +1,1 @@\n-mock\n+mock diff" };
      }
      return { files: ["mock_file_1.ts", "mock_file_2.ts"] };
    },
    async OpenWorkspacePath(rel: string) {
      console.info("mock OpenWorkspacePath", rel);
    },
        async OpenInEditorAt() {},
    async RevealWorkspacePath(rel: string) {
      console.info("mock RevealWorkspacePath", rel);
    },
    async RevealPath(path: string) {
      console.info("mock RevealPath", path);
    },
    async SavePastedImage(_dataUrl: string) {
      return ".fairpeer/attachments/mock.png";
    },
    async SaveClipboardImage() {
      return ".fairpeer/attachments/mock-clipboard.png";
    },
    async SavePastedFile(name: string, _dataUrl: string) {
      return `.fairpeer/attachments/mock-${name}`;
    },
    async PickExportFile(defaultFilename: string, _mimeType: string) {
      return defaultFilename;
    },
    async SaveExportFile(path: string, payload: string, base64Encoded: boolean) {
      const a = document.createElement("a");
      let url = "";
      if (base64Encoded) {
        url = `data:application/octet-stream;base64,${payload}`;
      } else {
        url = URL.createObjectURL(new Blob([payload], { type: "text/plain;charset=utf-8" }));
      }
      a.href = url;
      a.download = path;
      document.body.appendChild(a);
      a.click();
      a.remove();
      if (!base64Encoded) URL.revokeObjectURL(url);
    },
    async AttachDropped(path: string) {
      const name = path.split(/[/\\]/).filter(Boolean).pop() ?? path;
      return { kind: "attachment" as const, path: `.fairpeer/attachments/mock-${name}` };
    },
    async AttachmentDataURL(_path: string) {
      // 96×64 two-tone PNG so the attachment lightbox has something visible to
      // render in browser dev (the old 1×1 pixel was impossible to see).
      return "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAGAAAABACAIAAABqVuVZAAAAgklEQVR4nO3QQQ2AQBDAwDWCEjwgCpUo4X0Ojid9TFIBzcx5v9o0vx/EAwQIECBA4QABAgQIUDhAgAABAhQOECBAgACFAwQIECBA4QABAgQIULg5rkebAAECBAhQOECAAAECFA4QIECAAIUDBAgQIEDhAAECBAhQOECAAAECFA7QRwuH3IoOE9YxSgAAAABJRU5ErkJggg==";
    },
    async VoiceModelConfigured() {
      return true;
    },
    async TranscribeAudio(_audioDataURL: string, _language: string) {
      await delay(500);
      return "（语音转写结果 — 开发模式 mock）";
    },
    async MobileBridgeConfirm(_pairID: string) { /* mock: accept pairing is a no-op in the browser shell */ },
    async MobileBridgeReject(_pairID: string) { /* mock: reject pairing is a no-op */ },
    async MobileBridgeStartPairing() { return ""; },
    async MobileBridgeStatus() { return { paired: false, devices: [] }; },
    async MobileBridgeUnpair(_deviceCode: string) { /* mock: unpair is a no-op */ },
    async MobileBridgeListPairNics() { return JSON.stringify({ nics: [], pinned: "" }); },
    async MobileBridgeSetPairNic(_ip: string) { /* mock: pin nic is a no-op */ },
    async MobileBridgeSetCloudRelay(_enabled: boolean, _url: string) { /* mock: cloud relay is a no-op */ },
    async MobileBridgeSetKMode(mode: string, _externalURL: string) { return mode; },
    async MobileBridgeParseTurnCred(_paste: string) { return ""; },
    async MobileBridgeSetKnock(_enabled: boolean, _server: string) { /* mock: knock is a no-op */ },
    // Mobile-facing readouts — browser mock returns minimal data; the real
    // payloads come from mobilebridge_app.go in the Wails build.
    async ActiveTabID() { return mockTabs.find((t) => t.active)?.id ?? ""; },
    async ModelsForMobile() { return []; },
    async SessionListForMobile() { return []; },
        async Models() {
          const active = mockTabs.find((tab) => tab.active) ?? mockTabs[0];
          const current = mockTabModelRef(active);
          return mockModelCatalog.map((model) => ({ ...model, current: model.ref === current }));
        },
        async ModelsForTab(tabID) {
          const tab = mockTabs.find((item) => item.id === tabID) ?? mockTabs.find((item) => item.active) ?? mockTabs[0];
          const current = mockTabModelRef(tab);
          return mockModelCatalog.map((model) => ({ ...model, current: model.ref === current }));
        },
        async SetModel(name) {
          setMockTabModel(undefined, name);
        },
        async SetModelForTab(tabID, name) {
          setMockTabModel(tabID, name);
        },
        async Profile() {
          const active = mockTabs.find((tab) => tab.active) ?? mockTabs[0];
          return mockProfileOf(active);
        },
        async ProfileForTab(tabID) {
          const tab = mockTabs.find((item) => item.id === tabID) ?? mockTabs.find((item) => item.active) ?? mockTabs[0];
          return mockProfileOf(tab);
        },
        async Profiles() {
          return [
            { name: "dev", displayName: "编码", workspaceType: "code" },
            { name: "cowork", displayName: "办公", workspaceType: "document" },
          ];
        },
        async SwitchProfile(name) {
          setMockTabProfile(undefined, name);
        },
        async SwitchProfileForTab(tabID, name) {
          setMockTabProfile(tabID, name);
        },
        async Effort() {
          return { supported: true, current: mockEffort, default: "high", levels: ["auto", "high", "max"] };
        },
        async EffortForTab() {
          return this.Effort();
        },
        async SetEffort(level: string) {
          mockEffort = level || "auto";
        },
        async SetEffortForTab(_tabID, level) {
          await this.SetEffort(level);
        },
    async Memory() {
      return {
        available: true,
        storeDir: "~/.config/fairpeer/projects/-mock/memory",
        docs: [
          {
            path: "fairpeer.md",
            scope: "project",
            body: "# fairpeer project memory\n\nMock doc shown in the browser dev seam.\n\n## Notes\n\n- prefers concise replies",
          },
          {
            path: "~/.config/fairpeer/fairpeer.md",
            scope: "user",
            body: t("mock.memoryBody"),
          },
        ],
        facts: [
          {
            name: "prefers-tabs",
            description: "User prefers tabs",
            type: "user",
            body: "Indent with tabs.",
            status: "active",
            category: "style",
            tags: ["indent", "code-style"],
            validFrom: "2026-01-15",
            createdAt: "2026-01-15T09:30:00Z",
            updatedAt: "2026-01-15T09:30:00Z",
          },
          {
            name: "lives-in-shanghai",
            description: "User currently lives in Shanghai (moved from Beijing)",
            type: "user",
            body: "User moved to Shanghai in May 2026. Previously lived in Beijing.",
            status: "active",
            category: "temporal",
            tags: ["location"],
            validFrom: "2026-05-01",
            supersededBy: "",
            createdAt: "2026-05-01T08:00:00Z",
            updatedAt: "2026-05-01T08:00:00Z",
          },
        ],
        scopes: [
          { scope: "user", path: "~/.config/fairpeer/fairpeer.md" },
          { scope: "project", path: "fairpeer.md" },
          { scope: "local", path: "fairpeer.local.md" },
        ],
      };
    },
    async MemoryHistory() {
      // Dev-seam mock: shows the full version chain including a superseded
      // record (the Beijing address that the Shanghai move replaced).
      return {
        available: true,
        storeDir: "~/.config/fairpeer/projects/-mock/memory",
        docs: [],
        facts: [
          {
            name: "lives-in-shanghai",
            description: "User currently lives in Shanghai (moved from Beijing)",
            type: "user",
            body: "User moved to Shanghai in May 2026. Previously lived in Beijing.",
            status: "active",
            category: "temporal",
            validFrom: "2026-05-01",
            createdAt: "2026-05-01T08:00:00Z",
          },
          {
            name: "lives-in-beijing",
            description: "User lived in Beijing",
            type: "user",
            body: "User lives in Beijing.",
            status: "superseded",
            category: "temporal",
            validFrom: "2026-03-01",
            validTo: "2026-04-30",
            supersededBy: "lives-in-shanghai",
            createdAt: "2026-03-01T10:00:00Z",
          },
          {
            name: "prefers-tabs",
            description: "User prefers tabs",
            type: "user",
            body: "Indent with tabs.",
            status: "active",
            category: "style",
            validFrom: "2026-01-15",
            createdAt: "2026-01-15T09:30:00Z",
          },
        ],
        scopes: [],
        settings: mockMemorySettings,
      };
    },
    async Remember(scope: string, note: string) {
      emit({ kind: "notice", level: "info", text: `remembered → ${scope}` });
      return `${scope} fairpeer.md (mock): ${note}`;
    },
    async Forget(name: string) {
      emit({ kind: "notice", level: "info", text: `forgot → ${name}` });
    },
    async PromoteMemory(name: string) {
      emit({ kind: "notice", level: "info", text: `promoted → ${name}` });
      return true;
    },
    async RejectMemory(name: string) {
      emit({ kind: "notice", level: "info", text: `rejected → ${name}` });
      return true;
    },
    async SaveMemorySettings(input: MemorySettingsInput) {
      mockMemorySettings = {
        ...mockMemorySettings,
        configuredRoot: input.root,
        root: input.root || mockMemorySettings.defaultRoot,
        provider: input.provider,
        model: input.model,
        effort: input.effort,
        injectIndex: input.injectIndex,
        indexMaxChars: input.indexMaxChars > 0 ? input.indexMaxChars : 1200,
        ref: input.provider ? (input.model ? `${input.provider}/${input.model}` : input.provider) : input.model,
        refFromMemory: Boolean(input.provider || input.model),
      };
      emit({ kind: "notice", level: "info", text: `memory settings saved → ${mockMemorySettings.root}` });
      return mockMemorySettings;
    },
    async MigrateMemoryRoot(to: string) {
      emit({ kind: "notice", level: "info", text: `memory migrated → ${to}` });
      return { from: mockMemorySettings.root, to, copied: ["profile/", "memory/", "projects/"], skipped: [], files: 3, bytes: 1024 };
    },
    async PromoteMemoryArtifact(input: MemoryPromotionInput) {
      const dir = `${mockMemorySettings.root}/${input.kind === "plugin" ? "plugins" : "skills"}/${input.name}`;
      emit({ kind: "notice", level: "info", text: `promoted memory → ${dir}` });
      return {
        kind: input.kind,
        name: input.name,
        dir,
        skill: `${dir}/SKILL.md`,
        manifest: input.kind === "plugin" ? `${dir}/plugin.json` : undefined,
        readme: input.kind === "plugin" ? `${dir}/README.md` : undefined,
        refs: (input.memories ?? []).length,
        sources: (input.memories ?? []).map((name) => ({ name, level: "l2" })),
        version: input.version || "1.0.0",
        installed: input.install ? `${mockMemorySettings.root}/skills/${input.name}/SKILL.md` : undefined,
      };
    },
    async SaveDoc(path: string, _body: string) {
      emit({ kind: "notice", level: "info", text: `saved → ${path}` });
      return path;
    },
    async PortraitProfile() {
      return { path: "", content: "" };
    },
    // Mirrors memory.defaultPresets: the builtin list for the ACTIVE mock tab's
    // profile, nothing selected (a fresh user has no preset in use). The real
    // backend resolves the mode from the active tab's controller.
    async ProfilePresets(): Promise<ProfilePresetsPayload> {
      const mode = mockTabs.find(t => t.active)?.profile === "cowork" ? "cowork" : "dev";
      return {
        path: "",
        active: "",
        items: builtinPresetsFor(mode).map(i => ({ ...i })),
      };
    },
    async SetProfilePresets(p: ProfilePresetsPayload) {
      emit({ kind: "notice", level: "info", text: `presets saved → active=${p.active} (${p.items.length} items)` });
      return p.path;
    },
    // PPT-reference VLM gate (Go-side flow): browser-dev no-ops that keep the
    // mock shape-aligned with AppBindings.
    async AnalyzePDFPages(_pdfPath: string) {
      return 0;
    },
    async AnalyzeReferenceImage(_imgPath: string) {},
    async ClassifyReferenceVisual(_filePath: string) {
      return { is_visual: false, verdict: "A", reason: "browser-dev mock" };
    },
    async PreparePPTReference(_filePath: string) {
      return { is_visual: false, verdict: "A", reason: "browser-dev mock" };
    },
    async RenameTabForMobile(tabID: string, title: string) {
      emit({ kind: "notice", level: "info", text: `renamed ${tabID} → ${title}` });
    },
    async Settings() {
      return JSON.parse(JSON.stringify(settings)) as SettingsView;
    },
    async SetDefaultModel(ref: string) {
      settings.defaultModel = ref;
    },
    async SetFastTaskModel(ref: string) {
      settings.fastTaskModel = ref;
    },
    async GetFastLLMBaseDomain() {
      return "";
    },
    async SetFastLLMBaseDomain(_domain: string) {
      // mock no-op
    },
    async SetSubagentModel(ref: string) {
      settings.subagentModel = ref;
    },
    async SetSubagentEffort(level: string) {
      settings.subagentEffort = level;
    },
    async SetAutoPlan(mode: string) {
      settings.autoPlan = mode;
    },
    async SaveProvider(p: ProviderView) {
      p.added = true;
      const i = settings.providers.findIndex((x) => x.name === p.name);
      if (i >= 0) settings.providers[i] = p;
      else settings.providers.push(p);
    },
    async AddOfficialProviderAccess(kind: string, key: string) {
      // mock: no official provider templates bundled (provider-agnostic)
      const next: ProviderView = { name: kind || "custom", builtIn: false, added: true, kind: "openai", baseUrl: "", modelsUrl: "", models: [], default: "", apiKeyEnv: "", keySet: !!key.trim(), contextWindow: 0, reasoningProtocol: "", supportedEfforts: [], defaultEffort: "" };
      const i = settings.providers.findIndex((x) => x.name === next.name);
      if (i >= 0) settings.providers[i] = { ...settings.providers[i], ...next, keySet: next.keySet || settings.providers[i].keySet };
      else settings.providers.push(next);
    },
    async FetchProviderModels(p: ProviderView) {
      if (!p.baseUrl.trim()) throw new Error(t("settings.fetchModelsMissingBaseUrl"));
      if (!p.apiKeyEnv.trim()) throw new Error(t("settings.fetchModelsMissingKeyEnv"));
      await delay(350);
      return [];
    },
    async DeleteProvider(name: string) {
      settings.providers = settings.providers.filter((p) => p.name !== name);
    },
    async RemoveProviderAccess(name: string) {
      const p = settings.providers.find((x) => x.name === name);
      if (p?.builtIn) p.added = false;
      else settings.providers = settings.providers.filter((x) => x.name !== name);
    },
    async SetProviderKey(apiKeyEnv: string, _value: string) {
      settings.providers.forEach((p) => {
        if (p.apiKeyEnv === apiKeyEnv) p.keySet = true;
      });
    },
    async ClearProviderKey(apiKeyEnv: string) {
      settings.providers.forEach((p) => {
        if (p.apiKeyEnv === apiKeyEnv) p.keySet = false;
      });
    },
    async SetPermissionMode(mode: string) {
      settings.permissions.mode = mode;
    },
    async AddPermissionRule(list: string, rule: string) {
      const k = list as "allow" | "ask" | "deny";
      if (settings.permissions[k] && !settings.permissions[k].includes(rule)) settings.permissions[k].push(rule);
    },
    async RemovePermissionRule(list: string, rule: string) {
      const k = list as "allow" | "ask" | "deny";
      settings.permissions[k] = settings.permissions[k].filter((r) => r !== rule);
    },
        async SetSandbox(bash: string, network: boolean, workspaceRoot: string, allowWrite: string[]) {
          settings.sandbox = { bash, network, workspaceRoot, allowWrite };
        },
        async SetNetwork(n: NetworkView) {
          settings.network = n;
        },
        async SetBotSettings(b: BotSettingsView) {
          settings.bot = JSON.parse(JSON.stringify(b)) as BotSettingsView;
        },
        async SetBotSecret(envName: string, _value: string) {
          const name = envName.trim();
          if (settings.bot.qq.appSecretEnv === name) settings.bot.qq.secretSet = true;
          if (settings.bot.feishu.appSecretEnv === name) settings.bot.feishu.secretSet = true;
          if (settings.bot.weixin.tokenEnv === name) settings.bot.weixin.tokenSet = true;
          if (settings.bot.telegram.tokenEnv === name) settings.bot.telegram.tokenSet = true;
          settings.bot.connections = settings.bot.connections.map((connection) => ({
            ...connection,
            credential: connection.credential.appSecretEnv === name || connection.credential.tokenEnv === name
              ? { ...connection.credential, secretSet: true }
              : connection.credential,
          }));
        },
        async ClearBotSecret(envName: string) {
          const name = envName.trim();
          if (settings.bot.qq.appSecretEnv === name) settings.bot.qq.secretSet = false;
          if (settings.bot.feishu.appSecretEnv === name) settings.bot.feishu.secretSet = false;
          if (settings.bot.weixin.tokenEnv === name) settings.bot.weixin.tokenSet = false;
          if (settings.bot.telegram.tokenEnv === name) settings.bot.telegram.tokenSet = false;
          settings.bot.connections = settings.bot.connections.map((connection) => ({
            ...connection,
            credential: connection.credential.appSecretEnv === name || connection.credential.tokenEnv === name
              ? { ...connection.credential, secretSet: false }
              : connection.credential,
          }));
        },
        async StartBotConnectionInstall(provider: string, domain: string) {
          const normalizedProvider = provider === "weixin" ? "weixin" : "feishu";
          const normalizedDomain = normalizedProvider === "weixin" ? "weixin" : domain === "lark" ? "lark" : "feishu";
          return {
            ok: true,
            provider: normalizedProvider,
            domain: normalizedDomain,
            installId: `mock-${normalizedProvider}-${normalizedDomain}`,
            url: "https://example.com/fairpeer-bot-qr",
            deviceCode: "MOCKDEVICE",
            userCode: normalizedProvider === "weixin" ? "" : "MOCK-CODE",
            interval: 3,
            expireIn: 300,
            message: "",
          };
        },
        async PollBotConnectionInstall(installID: string) {
          const isWeixin = installID.includes("weixin");
          const domain = installID.includes("lark") ? "lark" : isWeixin ? "weixin" : "feishu";
          const provider = isWeixin ? "weixin" : "feishu";
          const connection = {
            id: `${provider}-${domain}`,
            provider,
            domain,
            label: domain === "lark" ? "Lark" : domain === "weixin" ? "微信" : "飞书",
            enabled: true,
            status: "connected",
            model: "",
            workspaceRoot: "",
            credential: {
              appId: provider === "feishu" ? "cli_mock" : "",
              appSecretEnv: provider === "feishu" ? (domain === "lark" ? "LARK_BOT_APP_SECRET" : "FEISHU_BOT_APP_SECRET") : "",
              accountId: provider === "weixin" ? "mock-account" : "",
              tokenEnv: provider === "weixin" ? "WEIXIN_BOT_TOKEN" : "",
              secretSet: true,
            },
            sessionMappings: [],
            lastError: "",
            createdAt: new Date().toISOString(),
            updatedAt: new Date().toISOString(),
          };
          settings.bot.connections = [...settings.bot.connections.filter((c) => c.id !== connection.id), connection];
          return { done: true, connection, status: "connected", message: "connected", error: "" };
        },
        async CompleteTelegramBotConnection(_token: string) {
          const connection = {
            id: "telegram-telegram",
            provider: "telegram",
            domain: "telegram",
            label: "Telegram @mockbot",
            enabled: true,
            status: "connected",
            model: "",
            workspaceRoot: "",
            credential: {
              appId: "",
              appSecretEnv: "",
              accountId: "",
              tokenEnv: "TELEGRAM_BOT_TOKEN",
              secretSet: true,
            },
            sessionMappings: [],
            lastError: "",
            createdAt: new Date().toISOString(),
            updatedAt: new Date().toISOString(),
          };
          settings.bot.connections = [...settings.bot.connections.filter((c) => c.id !== connection.id), connection];
          return { done: true, connection, status: "connected", message: "connected", error: "" };
        },
        async DiagnoseBotConnection(id: string) {
          const connection = settings.bot.connections.find((c) => c.id === id);
          return connection
            ? { id, label: connection.label, status: connection.enabled ? "ok" : "disabled", message: connection.enabled ? "连接配置已保存。" : "连接已保存但未启用。", messageId: "" }
            : { id, label: "", status: "missing", message: "未找到连接。", messageId: "" };
        },
        async TestBotConnection(id: string, target?: string) {
          const diag = await this.DiagnoseBotConnection(id);
          if (target?.trim()) return { ...diag, message: `Mock test sent to ${target.trim()}`, messageId: "mock-message-id" };
          return diag;
        },
        async ListRecentBotChats() {
          return [];
        },
        async BotDockStatus() {
          return { online: false, platforms: [], recentCount: 0 } as BotDockStatusView;
        },
        async SetCloseBehavior(mode: string) {
          settings.closeBehavior = mode === "quit" ? "quit" : "background";
        },
        async SetDisplayMode(mode: string) {
          settings.displayMode = mode;
        },
        async SetDesktopLanguage(lang: string) {
          settings.desktopLanguage = lang === "en" || lang === "zh" ? lang : "";
        },
        async SetDesktopAppearance(theme: string, style: string) {
          settings.desktopTheme = theme === "auto" || theme === "light" ? theme : "dark";
          settings.desktopThemeStyle = style;
        },
        async WallpaperInfo(): Promise<WallpaperView> {
          return { ...mockWallpaper };
        },
        async PickWallpaper(): Promise<WallpaperView> {
          // No native picker outside the shell: return the current state so
          // callers still receive a well-formed view.
          return { ...mockWallpaper };
        },
        async ClearWallpaper() {
          mockWallpaper = { active: false, url: "", name: "", blur: mockWallpaper.blur, dim: mockWallpaper.dim, fit: mockWallpaper.fit };
        },
        async SetWallpaperOptions(blur: number, dim: number, fit: string) {
          mockWallpaper = { ...mockWallpaper, blur, dim, fit };
        },
        async SetDesktopCheckUpdates(enabled: boolean) {
          settings.checkUpdates = enabled;
        },
        async SetDesktopTelemetry(enabled: boolean) {
          settings.telemetry = enabled;
        },
        async SetExpandThinking(on: boolean) {
          settings.expandThinking = on;
        },
        async MigrateDesktopPreferences(language: string, theme: string, style: string) {
          if (!settings.desktopLanguage) settings.desktopLanguage = language === "en" || language === "zh" ? language : "";
          if (!settings.desktopTheme && !settings.desktopThemeStyle) {
            settings.desktopTheme = theme === "auto" || theme === "light" ? theme : "dark";
            settings.desktopThemeStyle = style;
          }
        },
    async SetAgentParams(temperature: number, maxSteps: number, plannerMaxSteps: number, systemPrompt: string) {
      settings.agent = { temperature, maxSteps, plannerMaxSteps, systemPrompt, rpm: settings.agent?.rpm ?? 0 };
    },
    async SetRPM(rpm: number) {
      if (settings.agent) settings.agent.rpm = rpm;
    },
    async SetTrayLocale(_locale: "en" | "zh") {},
    async SetAutoApproveTools(on: boolean) {
      await this.SetToolApprovalMode(on ? "yolo" : "ask");
    },
    async SetBypass(on: boolean) {
      await this.SetAutoApproveTools(on);
    },
    async Version() {
      return "v1.0.0 (browser dev)";
    },
    async CheckUpdate() {
      // Keep the default browser preview focused on the primary product surface.
      // ApplyUpdate remains mocked for explicit updater-flow tests.
      return {
        available: false,
        current: "v1.0.0",
        latest: "v1.0.0",
        notes: "",
        canSelfUpdate: false,
        downloadUrl: "",
        assetSize: 0,
      };
    },
    async ApplyUpdate() {
      const total = 12_345_678;
      for (let r = 0; r <= total; r += 1_800_000) {
        emitUpdater({ phase: "downloading", received: Math.min(r, total), total });
        await delay(120);
      }
      emitUpdater({ phase: "verifying", received: total, total });
      await delay(500);
      emitUpdater({ phase: "applying", received: total, total });
      await delay(500);
      emitUpdater({ phase: "done", received: total, total });
      // The real shell relaunches here; the mock just stops.
    },
    async OpenDownloadPage() {
      if (typeof window !== "undefined") {
        window.open("https://github.com/zzycxz/fairpeer/releases/latest", "_blank", "noopener");
      }
    },
    // Dev seam: drives the overlay flow in the browser until ConnectKey sets the
    // key. Matches ConnectKey on apiKeyEnv so the two stay in sync.
    async NeedsOnboarding() {
      // Browser dev mock: skip the onboarding wizard — it exists to collect a
      // real API key, which the mock doesn't need. Avoids the slow multi-step
      // wizard (with its mock delays) devs hit on every fresh mock session.
      // To preview the wizard UI in-browser, temporarily return the providers
      // check: !settings.providers.some((p) => p.keySet).
      return false;
    },
    async SetupProvider(template: ProviderTemplate, apiKey: string, defaultModel: string, _visionModel: string, _fastModel: string, _voiceModel: string) {
      if (!apiKey.trim()) throw new Error("key is required");
      await delay(300);
      // Mock: add the provider to settings so NeedsOnboarding flips to false.
      settings.providers.push({
        name: template.name, builtIn: false, added: true, kind: template.kind,
        baseUrl: template.baseUrl, models: template.models, modelsUrl: "",
        default: defaultModel || template.defaultModel, apiKeyEnv: template.apiKeyEnv,
        keySet: true, contextWindow: template.contextWindow,
        reasoningProtocol: "", supportedEfforts: [], defaultEffort: "",
      });
      settings.defaultModel = template.name + "/" + (defaultModel || template.defaultModel);
    },
    async ConnectKey(apiKey: string) {
      if (!apiKey.trim()) throw new Error("key is required");
      // Mark the first provider as having a key set (mock behavior)
      const first = settings.providers[0];
      if (first) first.keySet = true;
      await delay(300);
    },
    async GetProviderTemplates() {
      // Minimal mock: return a couple of representative templates so the
      // wizard renders in-browser without the Go backend.
      await delay(100);
      return [
        { name: "deepseek", displayName: "DeepSeek", kind: "openai",
          baseUrl: "https://api.deepseek.com", apiKeyEnv: "DEEPSEEK_API_KEY",
          defaultModel: "deepseek-v4-pro", fastModel: "deepseek-v4-flash",
          visionModel: "deepseek-v4-pro", vision: true, contextWindow: 1000000,
          codingOnly: false, aggregator: false, category: "direct",
          docUrl: "https://platform.deepseek.com/api_keys",
          models: ["deepseek-v4-pro", "deepseek-v4-flash"],
          reasoningModels: ["deepseek-v4-pro"] },
        { name: "zhipu", displayName: "智谱 AI (GLM)", kind: "openai",
          baseUrl: "https://open.bigmodel.cn/api/paas/v4", apiKeyEnv: "ZHIPU_API_KEY",
          defaultModel: "glm-5.2", fastModel: "glm-4.7-flash",
          visionModel: "glm-5v-turbo", vision: true, contextWindow: 1000000,
          codingOnly: false, aggregator: false, category: "direct",
          docUrl: "https://open.bigmodel.cn/usercenter/apikeys",
          models: ["glm-5.2", "glm-5v-turbo", "glm-4.7-flash"] },
      ] as ProviderTemplate[];
    },
    async ProbeVendorKey(_baseURL: string, apiKey: string) {
      if (!apiKey.trim()) throw new Error("invalid API key");
      await delay(500);
    },
    async GetRegistryStatus() {
      await delay(50);
      return { updatedAt: "", source: "embed" } as RegistryStatus;
    },
    async RefreshRegistry() {
      await delay(500);
    },
    async ReportCrash() {
      await delay(300);
    },
    // Tab management mocks.
    async ListTabs() {
      return mockTabs.map((tab) => ({ ...tab }));
    },
    async OpenProjectTab(workspaceRoot: string, _topicID: string, profile?: string) {
      const existing = mockTabs.find((tab) => tab.scope === "project" && tab.workspaceRoot === workspaceRoot && tab.topicId === _topicID);
      if (existing) {
        const active = { ...existing, active: true, running: mockTopicRunsInScenario(_topicID) };
        mockTabs = mockTabs.map((tab) => (tab.id === existing.id ? active : { ...tab, active: false }));
        return { ...active };
      }
      const tab: TabMeta = {
        id: "tab_" + Date.now(),
        scope: "project",
        workspaceRoot,
        workspaceName: workspaceRoot.split("/").filter(Boolean).pop() ?? workspaceRoot,
        topicId: _topicID,
        topicTitle: topicLabel(_topicID, t("mock.newSession")),
        projectColor: mockProjectTree.find((node) => node.root === workspaceRoot)?.projectColor,
        label: "",
        ready: true,
        running: mockTopicRunsInScenario(_topicID),
        mode: "normal",
        collaborationMode: "normal",
        toolApprovalMode: "ask",
        active: true,
        cwd: workspaceRoot,
        profile: (profile || "dev").toLowerCase(),
      };
      mockTabs = [...mockTabs.map((item) => ({ ...item, active: false })), tab];
      return { ...tab };
    },
    async OpenProjectTab3(workspaceRoot: string, topicID: string, profile: string) {
      return this.OpenProjectTab(workspaceRoot, topicID, profile);
    },
    async OpenGlobalTab(_topicID: string, profile?: string) {
      // Global scope retired: legacy calls land on the profile home project.
      const homeProfile = profile || "dev";
      return this.OpenProjectTab(mockHomeRoot, _topicID, homeProfile);
    },
    async EnsureBlankTab(scope: string, workspaceRoot: string, profile: string) {
      const targetRoot = scope === "project" && workspaceRoot ? workspaceRoot : mockHomeRoot;
      const targetProfile = (profile || "dev").toLowerCase();
      const existing = mockTabs.find((tab) =>
        tab.scope === "project" &&
        (tab.profile ?? "dev").toLowerCase() === targetProfile &&
        tab.workspaceRoot === targetRoot &&
        !tab.running
      );
      if (existing) {
        setMockActiveTab(existing.id);
        return { ...existing, active: true };
      }
      const topic = await this.CreateTopic("project", targetRoot, targetProfile, "");
      return this.OpenProjectTab(targetRoot, topic.id, targetProfile);
    },
    async OpenExpertSessionTab(teamId: string, teamName: string) {
      const existing = mockTabs.find((t) => t.expertSession?.teamId === teamId);
      if (existing) { setMockActiveTab(existing.id); return { ...existing, active: true }; }
      const meta: TabMeta = {
        id: `expert_${Date.now()}`, tabType: "session", scope: "expert",
        workspaceRoot: "", workspaceName: "", topicId: "", topicTitle: teamName,
        label: "", ready: true, running: false, mode: "normal", active: true, cwd: "",
        profile: "cowork", expertSession: { teamId, teamName },
      };
      mockTabs.push(meta);
      setMockActiveTab(meta.id);
      return meta;
    },
    async SetActiveTab(_tabID: string) {
      setMockActiveTab(_tabID);
      const tab = mockTabs.find((item) => item.id === _tabID);
      if (tab) queueMockTopicRuntime(tab);
    },
    async ReorderTabs(_tabIDs: string[]) {
      const byId = new Map(mockTabs.map((tab) => [tab.id, tab]));
      const ordered = _tabIDs.map((id) => byId.get(id)).filter((tab): tab is TabMeta => Boolean(tab));
      if (ordered.length === mockTabs.length) mockTabs = ordered;
    },
    async CloseTab(_tabID: string) {
      if (mockTabs.length <= 1) return;
      const wasActive = mockTabs.some((tab) => tab.id === _tabID && tab.active);
      mockTabs = mockTabs.filter((tab) => tab.id !== _tabID);
      if (wasActive && mockTabs.length > 0 && !mockTabs.some((tab) => tab.active)) {
        mockTabs[mockTabs.length - 1] = { ...mockTabs[mockTabs.length - 1], active: true };
      }
    },
    async ListProjectTree(_profile?: string) {
      return cloneProjectTree();
    },
    async RenameProject(workspaceRoot: string, title: string) {
      const node = workspaceRoot
        ? mockProjectTree.find((item) => item.root === workspaceRoot)
        : undefined;
      if (node) node.label = title.trim() || node.label;
    },
    async SetProjectColor(workspaceRoot: string, color: string) {
      if (!workspaceRoot) return;
      const node = mockProjectTree.find((item) => item.root === workspaceRoot);
      if (!node) return;
      node.projectColor = color || undefined;
      for (const child of projectChildren(node)) child.projectColor = node.projectColor;
      mockTabs = mockTabs.map((tab) =>
        tab.workspaceRoot === workspaceRoot
          ? { ...tab, projectColor: node.projectColor }
          : tab,
      );
    },
    async ReorderProjects(_profile: string, workspaceRoots: string[]) {
      const projects = mockProjectTree.filter((node) => node.kind === "project");
      if (workspaceRoots.length !== projects.length) return;
      const byRoot = new Map(projects.map((node) => [node.root, node]));
      const seen = new Set<string>();
      const ordered: ProjectNode[] = [];
      for (const key of workspaceRoots) {
        if (seen.has(key)) return;
        const node = byRoot.get(key);
        if (!node) return;
        seen.add(key);
        ordered.push(node);
      }
      mockProjectTree.splice(0, mockProjectTree.length, ...ordered);
    },
    async CreateTopic(_scope: string, _workspaceRoot: string, _profile: string, title: string) {
      const now = Date.now();
      const id = "topic_" + now;
      const topicTitle = title.trim() || t("mock.newSession");
      const root = _scope === "global" || !_workspaceRoot ? mockHomeRoot : _workspaceRoot;
      const parent = mockProjectTree.find((node) => node.root === root)
        ?? (root === mockHomeRoot
          ? (mockProjectTree.push({ key: "project_home", kind: "project", label: "工作台", root: mockHomeRoot, children: [] }),
             mockProjectTree.find((node) => node.root === root))
          : undefined);
      if (parent) {
        parent.children = [{
          key: "topic_" + id,
          kind: "topic",
          label: topicTitle,
          root: parent.root,
          topicId: id,
          projectColor: parent.projectColor,
          createdAt: now,
        }, ...projectChildren(parent)];
      }
      return { id, title: topicTitle, createdAt: now };
    },
    async RenameTopic(topicID: string, title: string) {
      const topic = findMockTopic(topicID);
      const nextTitle = title.trim();
      if (!topic || !nextTitle) return;
      const activePrefix = topic.label?.startsWith("● ") ? "● " : "";
      topic.label = `${activePrefix}${nextTitle}`;
      mockTabs = mockTabs.map((tab) =>
        tab.topicId === topicID ? { ...tab, topicTitle: nextTitle } : tab,
      );
    },
    async DeleteTopic(topicID: string) {
      deleteMockTopic(topicID);
    },
    async TrashTopic(topicID: string) {
      deleteMockTopic(topicID);
    },
    async TrashExpertSession(_teamID: string) {
      // no-op in mock — no real session files to trash
    },
    async SaveWindowState(_state) {
      // no-op in browser dev — no real window geometry to persist
    },
    // --- Scheduled tasks mock (browser dev only) -----------------------------
    // A small in-memory store seeded with one sample task so the automation
    // panel looks alive outside the Wails shell. The real backend persists to
    // JSON; here we just keep the array.
    async ListScheduledTasks(): Promise<TaskView[]> {
      return mockSchedulerTasks.map(cloneTask);
    },
    async CreateScheduledTask(input: TaskInput): Promise<TaskView> {
      const view: TaskView = {
        id: `sched_mock_${Date.now()}`,
        name: input.name || "未命名任务",
        expression: input.expression,
        prompt: input.prompt,
        profile: "cowork",
        enabled: true,
        oneShot: input.expression.toLowerCase().startsWith("at "),
        lastRun: "",
        nextRun: input.expression.toLowerCase().startsWith("at ") ? input.expression.slice(3) : "明天 09:00",
        runCount: 0,
        lastResult: "",
        outputMode: input.outputMode ?? "",
        outputDest: input.outputDest ?? "",
        outputAccount: input.outputAccount ?? "",
        outputDir: input.outputDir ?? "",
        plain: input.plain ?? false,
        lastDeliverErr: "",
        lastDeliverAt: "",
        humanSchedule: input.expression,
        source: "manual",
        calendarEventId: "",
      };
      mockSchedulerTasks.unshift(view);
      return cloneTask(view);
    },
    async UpdateScheduledTask(input: TaskInput): Promise<TaskView> {
      const idx = mockSchedulerTasks.findIndex((t) => t.id === input.id);
      if (idx < 0) throw new Error("task not found");
      mockSchedulerTasks[idx] = {
        ...mockSchedulerTasks[idx],
        name: input.name,
        expression: input.expression,
        prompt: input.prompt,
        outputMode: input.outputMode ?? "",
        outputDest: input.outputDest ?? "",
        outputAccount: input.outputAccount ?? "",
        outputDir: input.outputDir ?? "",
        plain: input.plain ?? false,
        lastDeliverErr: "",
        lastDeliverAt: "",
        humanSchedule: input.expression,
      };
      return cloneTask(mockSchedulerTasks[idx]);
    },
    async DeleteScheduledTask(id: string): Promise<void> {
      mockSchedulerTasks = mockSchedulerTasks.filter((t) => t.id !== id);
    },
    async PauseScheduledTask(id: string): Promise<void> {
      const t = mockSchedulerTasks.find((t) => t.id === id);
      if (t) { t.enabled = false; t.nextRun = ""; }
    },
    async ResumeScheduledTask(id: string): Promise<void> {
      const t = mockSchedulerTasks.find((t) => t.id === id);
      if (t) { t.enabled = true; t.nextRun = "稍后"; }
    },
    async RunScheduledTaskNow(id: string): Promise<string> {
      const t = mockSchedulerTasks.find((t) => t.id === id);
      if (!t) throw new Error("task not found");
      t.runCount++;
      t.lastRun = new Date().toISOString().slice(0, 16).replace("T", " ");
      t.lastResult = "（mock）已运行";
      return t.lastResult;
    },
    async ScheduledTaskHistory(_taskID: string): Promise<RunRecordView[]> {
      return [
        { taskId: _taskID || "demo", name: "日报提醒", at: "2026-06-21 18:00", status: "ok", result: "日报已生成并发送", outputMode: "notify" },
      ];
    },
    async ScheduledTaskTemplates(): Promise<TemplateView[]> {
      return [
        { id: "daily_report_reminder", name: "日报提醒", category: "reminder", desc: "每个工作日下班前提醒整理日报", expression: "daily 18:00 Mon-Fri", prompt: "请整理今日工作日报，按三段式汇总。", outputMode: "notify", outputHint: "", oneShot: false },
        { id: "weekly_report_reminder", name: "周报提醒", category: "reminder", desc: "每周五提醒提交周报到邮箱", expression: "daily 17:00 Fri", prompt: "生成本周工作周报。", outputMode: "email", outputHint: "填写收件人邮箱", oneShot: false },
        { id: "meeting_reminder", name: "会议提醒", category: "reminder", desc: "一次性会议开始前提醒", expression: "at 2026-06-24 14:45", prompt: "15分钟后有会议，请准备材料。", outputMode: "notify", outputHint: "", oneShot: true },
        { id: "data_scrape", name: "定时数据抓取", category: "data", desc: "每天早上抓取数据存为 CSV", expression: "daily 09:00", prompt: "抓取昨日关键业务数据并保存为 CSV。", outputMode: "file", outputHint: "填写保存路径", oneShot: false },
        { id: "system_check", name: "系统巡检", category: "ops", desc: "每小时检查系统状态，异常告警", expression: "every 1h", prompt: "检查磁盘/内存/进程，异常时告警。", outputMode: "im", outputHint: "填写飞书会话标识", oneShot: false },
      ];
    },
    async PreviewSchedule(text: string): Promise<SchedulePreview> {
      const low = (text || "").trim().toLowerCase();
      if (!low) return { inputText: text, expression: "", absoluteTime: "", kind: "unknown", note: "输入时间或计划" };
      if (/^(后天|明天|大后天|下周|今天|周|星期)/.test(text) || /点|：|:/.test(text)) {
        return { inputText: text, expression: "at 2026-06-24 15:00", absoluteTime: "2026-06-24 15:00", kind: "oneshot", note: "一次性任务（mock 预览）" };
      }
      if (low.startsWith("at ") || low.startsWith("in ") || low.startsWith("daily") || low.startsWith("every") || low === "hourly") {
        return { inputText: text, expression: text, absoluteTime: "", kind: "recurring", note: "下次：稍后（mock）" };
      }
      return { inputText: text, expression: "", absoluteTime: "", kind: "unknown", note: "无法识别（mock）" };
    },
    async SmartParseSchedule(text: string): Promise<SchedulePreview> {
      // Mock: pretend the model resolved it to a near-future time.
      const now = new Date();
      now.setDate(now.getDate() + 7);
      const ts = `${now.getFullYear()}-${String(now.getMonth()+1).padStart(2,"0")}-${String(now.getDate()).padStart(2,"0")} 15:00`;
      return { inputText: text, expression: "at " + ts, absoluteTime: ts, kind: "oneshot", note: "一次性任务（智能解析 mock）" };
    },
    // --- Calendar mock (browser dev only) ------------------------------------
    async ListCalendarEvents(_since: string, _before: string): Promise<CalendarEventView[]> {
      const now = new Date();
      const y = now.getFullYear();
      const m = now.getMonth();
      const d = now.getDate();
      return [
        { id: "evt_mock_1", title: "周会", description: "讨论本周进展", location: "会议室A", start: `${y}-${String(m+1).padStart(2,"0")}-${String(d).padStart(2,"0")}T10:00`, end: `${y}-${String(m+1).padStart(2,"0")}-${String(d).padStart(2,"0")}T11:00`, allDay: false, timezone: "Asia/Shanghai", color: "#FF4444", status: "confirmed", source: "manual", recurrence: "FREQ=WEEKLY;BYDAY=MO", recurrenceEnd: "", reminders: [15], taskId: "", tags: ["工作", "例会"], createdAt: "2026-07-01 10:00", outputMode: "", outputDest: "", outputAccount: "" },
        { id: "evt_mock_2", title: "代码review", description: "", location: "线上", start: `${y}-${String(m+1).padStart(2,"0")}-${String(d).padStart(2,"0")}T14:00`, end: `${y}-${String(m+1).padStart(2,"0")}-${String(d).padStart(2,"0")}T15:00`, allDay: false, timezone: "Asia/Shanghai", color: "#4488FF", status: "confirmed", source: "manual", recurrence: "", recurrenceEnd: "", reminders: [5], taskId: "", tags: ["工作"], createdAt: "2026-07-01 10:00", outputMode: "", outputDest: "", outputAccount: "" },
      ];
    },
    async ListScheduledTasksAsEvents(_since: string, _before: string): Promise<CalendarEventView[]> {
      return [];
    },
    async CreateCalendarEvent(input: CalendarEventInput): Promise<CalendarEventView> {
      return { ...input, outputMode: input.outputMode ?? "", outputDest: input.outputDest ?? "", outputAccount: input.outputAccount ?? "", id: `evt_mock_${Date.now()}`, status: "confirmed", source: "manual", taskId: "", createdAt: new Date().toISOString().slice(0,16).replace("T"," ") };
    },
    async UpdateCalendarEvent(input: CalendarEventInput): Promise<CalendarEventView> {
      return { ...input, outputMode: input.outputMode ?? "", outputDest: input.outputDest ?? "", outputAccount: input.outputAccount ?? "", status: "confirmed", source: "manual", taskId: "", createdAt: "2026-07-01 10:00" };
    },
    async DeleteCalendarEvent(_id: string): Promise<void> {},
    async SearchCalendarEvents(_q: string, _limit: number): Promise<CalendarEventView[]> {
      return [];
    },
    async ExportCalendarEvents(_path: string): Promise<string> {
      return "exported 0 events (mock)";
    },
    async ImportCalendarEvents(_path: string): Promise<string> {
      return "imported 0 events (mock)";
    },
    async ExportCalendarDialog(): Promise<string> {
      return "exported 0 events (mock)";
    },
    async ImportCalendarDialog(): Promise<string> {
      return "imported 0 events (mock)";
    },
    async GetChineseHolidays(_year: number): Promise<CalendarEventView[]> {
      return [];
    },
    // --- RAG mock (browser dev only) ------------------------------------------
    // In-memory tree seeded with one sample collection + a file mid-extraction
    // so the panel shows a progress bar outside the Wails shell.
    async ListRagCollections(): Promise<RagCollectionView[]> {
      return [
        { id: "default", name: "default", path: "default", parent: "", documents: mockRagDocs, chunks: mockRagDocs * 4, entities: mockRagEntities },
      ];
    },
    async ListRagTree(_collection: string): Promise<RagNodeView[]> {
      return mockRagTree;
    },
    async RagImportPaths(_collection: string, paths: string[]): Promise<RagImportResult> {
      const jobIds: string[] = [];
      let files = 0;
      for (const p of paths) {
        files++;
        const jid = `rag_job_mock_${Date.now()}_${files}`;
        jobIds.push(jid);
        const node: RagNodeView = {
          key: p, label: p.split(/[\\/]/).pop() || p, kind: "file", path: p, relPath: p,
          isDir: false, collection: _collection || "default", status: "extracting",
          hasFts5: true, jobId: jid, doneChunks: 0, totalChunks: 8, entityCount: 0, errorMsg: "",
        };
        mockRagTree.push(node);
        // Simulate progress for browser dev.
        simulateRagProgress(jid, node);
      }
      mockRagDocs += files;
      return { jobIds, files, ftsChunks: files * 4, message: `mock：已导入 ${files} 个文件` };
    },
    async RagStartExtract(_collection: string, _template: string, _mode: string): Promise<void> {
      const node = mockRagTree.find((n) => n.path === _template);
      if (node) { node.status = "extracting"; node.doneChunks = 0; node.totalChunks = node.totalChunks || 8; simulateRagProgress(node.jobId, node); }
    },
    async RagExtractResult(_collection: string): Promise<RagExtractResultView> {
      return {
        entityCount: 5,
        relationCount: 3,
        topEntities: [
          { name: "mock_entity", nameRaw: "Mock Entity", type: "concept", description: "A mock entity", relationCount: 2 },
        ],
        topRelations: [
          { source: "mock_entity", target: "mock_entity2", type: "related", description: "mock relation" },
        ],
        jobCount: 1,
        doneCount: 1,
        hasData: true,
      };
    },
    async RagCancelExtract(jobId: string): Promise<void> {
      for (const n of mockRagTree) { if (n.jobId === jobId) { n.status = "cancelled"; } }
    },
    async RagRemovePath(_collection: string, path: string): Promise<void> {
      mockRagTree = mockRagTree.filter((n) => n.path !== path);
    },
    async RagClear(_collection: string): Promise<void> {
      mockRagTree = [];
      mockRagDocs = 0;
      mockRagEntities = 0;
    },
    async RagCleanCollection(_collection: string): Promise<void> {
      // mock: no-op
    },
    async RagSearch(_collection: string, query: string, _topK: number): Promise<RagSearchHitView> {
      return {
        entities: [{ name: query + "（示例实体）", type: "person", description: "mock 命中" }],
        relations: [],
        snippets: [{ collection: "default", path: "/mock/doc.md", chunk: 0, snippet: `…包含「${query}」的片段…`, score: 0.9 }],
      };
    },
    async RagSemanticSearch(_collection: string, query: string, _topK: number): Promise<RagSearchHitView> {
      return {
        entities: [{ name: query + "（语义匹配）", type: "concept", description: "mock 语义命中" }],
        relations: [],
        snippets: [],
      };
    },
    async RagEmbedEntities(_collection: string): Promise<void> {
      // mock: no-op
    },
    async RagDetectCommunities(_collection: string): Promise<void> {
      // mock: no-op
    },
    async RagSummarize(_collection: string): Promise<{ summary: string; themes: string[] }> {
      return { summary: "这是一份示例摘要，展示了知识库的主要内容。", themes: ["示例主题1", "示例主题2"] };
    },
    async RagAsk(_collection: string, _question: string): Promise<string> {
      return "这是来自知识库的示例回答。";
    },
    async RagPreviewETA(jobId: string): Promise<RagETAView> {
      const n = mockRagTree.find((x) => x.jobId === jobId);
      const remaining = n ? Math.max(0, n.totalChunks - n.doneChunks) : 0;
      return { jobId, doneChunks: n?.doneChunks ?? 0, totalChunks: n?.totalChunks ?? 0, avgLatencyMs: 2800, etaSeconds: remaining * 3 };
    },
    async RagListTemplates(): Promise<string[]> {
      return [".txt", ".md", ".csv", ".json", ".html", ".py", ".go", ".js", ".ts", ".yaml"];
    },
    async HEHealth(): Promise<{ running: boolean; ready: boolean; port: number }> {
      return { running: false, ready: false, port: 0 };
    },
    async RagListHETemplates() {
      return [] as Array<{ name: string; displayName: string; description: string; category: string; available: boolean; templateType: string; entityFields: Array<{ name: string; description: string }>; relationFields: Array<{ name: string; description: string }> }>;
    },
    async GetGraphData(_collection: string): Promise<GraphDataView> {
      return { nodes: [], edges: [] };
    },
    async GetTopEntities(_collection: string, _limit: number): Promise<GraphDataView> {
      return { nodes: [], edges: [] };
    },
    async GetGraphDataPaged(_collection: string, _offset: number, _limit: number, _types: string[]): Promise<GraphDataView> {
      return { nodes: [], edges: [] };
    },
    async GetEntityDetail(_collection: string, _name: string): Promise<EntityDetailView> {
      return { name: "", nameRaw: "", type: "", description: "", sources: [], relations: [], community: -1, relationCnt: 0 };
    },
    async UpdateEntity(_collection: string, _name: string, _patch: EntityPatch): Promise<void> {},
    async MergeEntities(_collection: string, _keepName: string, _mergeNames: string[]): Promise<void> {},
    async RagFindMergeCandidates(_collection: string): Promise<Array<{ keepName: string; mergeName: string; keepRaw: string; mergeRaw: string; score: number }>> {
      return [];
    },
    async GetDocumentPreview(_collection: string, _docPath: string): Promise<DocPreviewView> {
      return { path: "", content: "", chunks: [] };
    },
    async WriteKnowledgeRef(_collection: string, _entityNames: string[], _relationKeys: string[]): Promise<string> {
      return "/tmp/mock_knowledge_ref.md";
    },
    async RunSkillWithKnowledge(_skillName: string, _refPath: string): Promise<void> {},
    async ExportObsidian(_collection: string, _outputDir: string): Promise<void> {},
    async SetSessionCollections(_collections: string[]): Promise<void> {},
    async GetSessionCollections(): Promise<string[]> { return []; },
    async RagFeedText(_collection: string, _label: string, _text: string): Promise<void> {},
    async RagBatchImport(_collection: string, _paths: string[]): Promise<RagImportResult> {
      return { jobIds: [], files: 0, ftsChunks: 0, message: "mock" };
    },
    async RagBatchExtract(_collection: string): Promise<void> {},
    // --- Expert team mock (browser dev only) -------------------------------
    async ListExpertTeams(): Promise<TeamView[]> {
      return [
        { id: "t1", name: "方案评审团", defaultMode: "debate", defaultRounds: 2, allowSearch: false, experts: [
          { name: "批判者", model: "", perspective: "从风险角度批判性审视" },
          { name: "建设者", model: "", perspective: "从改进落地角度给建议" },
        ]},
      ];
    },
    async CreateExpertTeam(team: TeamView): Promise<TeamView> {
      return { ...team, id: `team_mock_${Date.now()}` };
    },
    async UpdateExpertTeam(team: TeamView): Promise<TeamView> { return team; },
    async DeleteExpertTeam(_id: string): Promise<void> {},
    async RunExpertTeam(_teamId: string, _task: string, _mode: string, _rounds: number): Promise<string> {
      const runId = `run_mock_${Date.now()}`;
      // In browser dev there's no runtime.EventsOn, so the mock can't stream
      // CollabEvents. Real runs stream via onExpertsCollab; here we just return
      // a runId so the panel's "start" handler doesn't crash.
      return runId;
    },
    async GetActiveExpertRun(_teamId: string): Promise<ExpertRunView> {
      // No in-flight run in the browser dev shell.
      return {};
    },
    async DeleteExpertCollab(_tabId: string, _ordinal: number): Promise<HistoryMessage[]> {
      return [];
    },
    async StartScreenshotHotkey() {},
    async StopScreenshotHotkey() {},
    async StartEStopHotkey() {},
    async StopEStopHotkey() {},
    async SetCoWorkSettings(v: any) { settings.cowork = { ...v, detectedBrowser: settings.cowork.detectedBrowser }; },
    async ProbeMailAccount(_name: string) { return { ok: true, status: "unconfigured", message: "" } as MailProbeResult; },
    async InboxPreview(_mailbox: string, _limit: number) { return [] as InboxItem[]; },
    async HooksSettings(scope: string) {
      const key = scope === "project" ? "project" : "global";
      return JSON.parse(JSON.stringify(hookSettings[key])) as HooksSettingsView;
    },
    async SaveHooksSettings(scope: string, hooks: HookConfigView[]) {
      const key = scope === "project" ? "project" : "global";
      hookSettings[key].hooks = JSON.parse(JSON.stringify(hooks)) as HookConfigView[];
    },
    async SaveHooksSettingsForRoot(scope: string, _projectRoot: string, hooks: HookConfigView[]) {
      const key = scope === "project" ? "project" : "global";
      hookSettings[key].hooks = JSON.parse(JSON.stringify(hooks)) as HookConfigView[];
    },
    async TrustProjectHooks() {
      hookSettings.project.trusted = true;
    },
    async TrustProjectHooksForRoot(projectRoot: string) {
      if (projectRoot && projectRoot === hookSettings.project.projectRoot) {
        hookSettings.project.trusted = true;
      }
    },
    async CheckCoworkBrowser() { return "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe"; },
    async StartManagedBrowser() {
      return { running: true, url: "http://127.0.0.1:9222", browser: "Chrome", profile: "~/Library/Application Support/fairpeer/browser-profile", alreadyRunning: false };
    },
    async CheckManagedBrowser() {
      return { running: false, url: "http://127.0.0.1:9222", browser: "", profile: "", alreadyRunning: false, detail: "browser dev mock" };
    },
    async OpenURLInManagedBrowser(_url: string) {
      await delay(300);
      return { running: true, url: "http://127.0.0.1:9222", browser: "Chrome (mock)", profile: "~/fairpeer/browser-profile", alreadyRunning: false };
    },
    // 浏览器控制台 mock：browser dev 模式下的桩——交互原语返回模拟输出，
    // 录制/生成给出一条示例轨迹转朴素草稿，试运行模拟三步进度。
    async BrowserConsoleOpen(_cdpURL: string, url: string) {
      await delay(400);
      return { open: true, session_id: "br_mock", browser: "Chrome (mock)", attached: false, url: url || "about:blank", keep_alive: false, keep_alive_mode: "", keep_alive_url: "", keep_alive_last: 0, keep_alive_err: "" };
    },
    async BrowserConsoleState() {
      return { open: false, session_id: "", browser: "", attached: false, url: "", keep_alive: false, keep_alive_mode: "", keep_alive_url: "", keep_alive_last: 0, keep_alive_err: "" };
    },
    async BrowserConsoleClose() {},
    async BrowserConsoleNavigate(url: string) { await delay(300); return `已导航到 ${url} (mock)`; },
    async BrowserConsoleHighlight(_t: string, _d: number) { await delay(120); },
    async BrowserConsoleTabs() {
      await delay(150);
      return [
        { index: 1, title: "工作台", url: "https://ops.local/home", current: false },
        { index: 2, title: "告警详情", url: "https://ops.local/alerts", current: true },
      ];
    },
    async BrowserConsoleSwitchTab(index: number) { await delay(250); return `已切换到页卡 ${index} (mock)`; },
    async BrowserConsoleBack() { await delay(200); return "后退一页 (mock)"; },
    async BrowserConsoleForward() { await delay(200); return "前进一页 (mock)"; },
    async BrowserConsoleExtractTable(selector: string) { await delay(300); return `| 列1 | 列2 |
|---|---|
| a | b | (mock, ${selector || "整页"})`; },
    async BrowserConsoleElements() {
      return [
        { ref: "e1", role: "textbox", name: "用户名", value: "" },
        { ref: "e2", role: "textbox", name: "密码", value: "" },
        { ref: "e3", role: "button", name: "登录" },
      ];
    },
    async BrowserConsoleClick(target: string) { await delay(200); return `已点击 ${target} (mock)`; },
    async BrowserConsoleType(target: string, text: string) { await delay(200); return `已在 ${target} 输入 ${text.length} 字符 (mock)`; },
    async BrowserConsoleKey(_key: string) { await delay(150); },
    async BrowserConsoleScroll(_direction: string, _amount: number) { await delay(150); return "已滚动 (mock)"; },
    async BrowserConsoleSelectOption(target: string, value: string) { return `已在 ${target} 选择 ${value} (mock)`; },
    async BrowserConsoleUploadFile(target: string, files: string[]) { return `已向 ${target} 上传 ${files.length} 个文件 (mock)`; },
    async BrowserConsoleWait(_condition: string, _timeoutSec: number) { await delay(300); return "waited (mock)"; },
    async BrowserConsoleExtract(selector: string) { return selector ? `提取 ${selector} 的内容 (mock)` : "提取整页内容 (mock)"; },
    async BrowserConsoleScreenshot() { await delay(300); return "data:image/png;base64,"; },
    async BrowserConsoleEvaluate(_expression: string) { return "undefined (mock)"; },
    async BrowserConsoleRecordStart() { await delay(200); },
    async BrowserConsoleRecordStop() {
      return [
        { type: "navigate", url: "https://ops.portal.local/", time: Date.now() },
        { type: "input", selector: "#user", role: "textbox", name: "用户名", value: "admin", time: Date.now() + 900 },
        { type: "click", selector: "button[type=submit]", role: "button", name: "登录", time: Date.now() + 1600 },
      ];
    },
    async BrowserConsoleFilterTrace(events) {
      return { kept: events.filter((e) => e.type !== "effect"), dropped: events.filter((e) => e.type === "effect") };
    },
    async BrowserConsoleGenerateSkill(nameHint: string, events) {
      const content = [
        "---",
        `name: ${nameHint || "mock-skill"}`,
        "description: 浏览器操作技能（mock）。",
        "runAs: subagent",
        "allowed-tools: browser_open, browser_navigate, browser_click, browser_type, browser_wait, browser_extract",
        "---",
        "",
        `# ${nameHint || "mock 技能"}`,
        "",
        "## 何时使用",
        "",
        "（mock 草稿）",
        "",
        "## 步骤",
        "",
        "| # | 操作 | 目标 | 值 |",
        "|---|------|------|------|",
        ...events.map((e, i) => `| ${i + 1} | ${e.type} | \`${e.selector ?? e.url ?? ""}\` | ${e.value ?? ""} |`),
        "",
        "## 注意事项",
        "",
        "- mock",
        "",
        "## 验证",
        "",
        "- mock",
        "",
      ].join("\n");
      return { name: nameHint || "mock-skill", content, fallback: true, detail: "browser dev mock" };
    },
    async BrowserConsoleSaveSkill(_content: string, _overwrite: boolean) { return "~/.fairpeer/skills/mock/SKILL.md (mock)"; },
    async BrowserConsoleListSkills() { return [{ name: "mock-skill", description: "浏览器操作技能（mock）", browser: true }]; },
    async BrowserConsoleReadSkill(_name: string) { return "---\nname: mock-skill\ndescription: mock\n---\n\n# mock\n"; },
    async BrowserConsoleDeleteSkill(_name: string) { await delay(200); },
    async BrowserConsoleSetKeepAlive(enabled: boolean, _intervalSec: number, mode: string, _url: string) {
      await delay(200);
      return {
        open: true, session_id: "br_mock", browser: "Chrome (mock)", attached: false, url: "about:blank",
        keep_alive: enabled, keep_alive_mode: enabled ? mode : "", keep_alive_url: _url,
        keep_alive_last: enabled ? Date.now() : 0, keep_alive_err: "",
      };
    },
    async BrowserConsoleTrialRun(steps, params) {
      const bindings: Record<string, string> = { ...(params ?? {}) };
      for (let i = 0; i < steps.length; i++) {
        if (steps[i].type === "human") {
          // Mock the human breakpoint: park on a promise the mock Resume/
          // Abort bindings release, so the editor's waiting banner is
          // demoable in browser dev mode.
          emitBrowserTrialMock({ index: i, status: "waiting", output: steps[i].text || "请完成人工操作 (mock)" });
          const verdict = await mockTrialGate();
          if (verdict.kind !== "resume") {
            emitBrowserTrialMock({ index: i, status: "failed", error: "人工中止 (mock)" });
            emitBrowserTrialMock({ index: -1, status: "failed", error: "人工中止 (mock)" });
            return;
          }
          emitBrowserTrialMock({ index: i, status: "done", output: "人工操作完成 (mock)" });
          continue;
        }
        if (steps[i].type === "ask") {
          emitBrowserTrialMock({ index: i, status: "waiting", output: steps[i].text || "请输入内容 (mock)", await_reply: true, bind: steps[i].target || "" });
          const verdict = await mockTrialGate();
          if (verdict.kind !== "resume") {
            emitBrowserTrialMock({ index: i, status: "failed", error: "人工中止 (mock)" });
            emitBrowserTrialMock({ index: -1, status: "failed", error: "人工中止 (mock)" });
            return;
          }
          const bind = (steps[i].target || "").trim();
          if (bind) bindings[bind] = verdict.reply;
          emitBrowserTrialMock({ index: i, status: "done", output: verdict.reply ? `收到回复：${verdict.reply}` : "（用户未输入，继续）" });
          continue;
        }
        emitBrowserTrialMock({ index: i, status: "running" });
        await delay(400);
        emitBrowserTrialMock({ index: i, status: "done", output: "(mock)" });
      }
      emitBrowserTrialMock({ index: -1, status: "done" });
    },
    async BrowserConsoleTrialResume(reply: string) {
      mockTrialSignal({ kind: "resume", reply: reply ?? "" });
    },
    async BrowserConsoleTrialAbort() {
      mockTrialSignal({ kind: "abort" });
    },
    // Loop Engineering mock: a fast 3-round simulation so the panel's config →
    // running → report flow is demoable without the Go backend.
    async LoopStart(tabID, config) {
      mockLoopState = {
        runId: `loop-mock-${Date.now()}`,
        config,
        workspaceRoot: "C:\dev\demo-project (mock)",
        tabLabel: `tab:${tabID || "active"}`,
        state: "running",
        round: 0,
        startedAt: Date.now(),
        tokensUsed: 0,
        timeline: [],
      };
      mockLoopTick();
    },
    async LoopStop(reason) {
      if (mockLoopState) {
        mockLoopState.state = "aborted";
        mockLoopState.stopNote = reason || "手动停止";
        mockLoopState.endedAt = Date.now();
        emitMockLoop();
      }
    },
    async LoopStatus() {
      return mockLoopState ? structuredClone(mockLoopState) : null;
    },
    async OpenPPTTemplateDir() {},
    async PickPPTTemplate() { return ""; },
    async RagCreateCollection(_name: string) {},
    async RagDeleteCollection(_name: string) {},
    async RagRenameCollection(_oldName: string, _newName: string) {},
    async SetDesktopMetrics(_enabled: boolean) {},
    async SetPlannerModel(_model: string) {},
    // --- 团队 (team) mock ---
    async ListTeamProjects() {
      return mockTeams.map((x) => ({ ...x }));
    },
    async GetTeamProject(id: string) {
      const tm = mockFindTeam(id);
      if (!tm) throw new Error("团队不存在");
      return { ...tm };
    },
    async CreateTeamProject(tv: TeamProjectView) {
      const now = new Date().toISOString();
      const team: TeamProjectView = {
        ...tv,
        id: tv.id || mockTeamID("team"),
        members: (tv.members ?? []).map((m) => ({ ...m, id: m.id || mockTeamID("mem") })),
        tasks: (tv.tasks ?? []).map((t) => ({ ...t, id: t.id || mockTeamID("task"), status: t.status || "queued" })),
        context: tv.context ?? mockEmptyContext(tv.goal),
        policy: tv.policy ?? mockPolicy(),
        createdAt: now,
        updatedAt: now,
      };
      mockTeams = [...mockTeams, team];
      emitMockTeam(team);
      return { ...team };
    },
    async UpdateTeamProject(tv: TeamProjectView) {
      return mockPatchTeam(tv.id, (tm) => ({ ...tm, ...tv }));
    },
    async DeleteTeamProject(id: string) {
      mockTeams = mockTeams.filter((x) => x.id !== id);
      emitMockTeam({ id, deleted: "1" });
    },
    async AddTeamMember(teamId: string, mv: TeamMemberView) {
      return mockPatchTeam(teamId, (tm) => {
        const members = mv.isLeader ? tm.members.map((m) => ({ ...m, isLeader: false })) : tm.members;
        return { ...tm, members: [...members, { ...mv, id: mv.id || mockTeamID("mem") }] };
      });
    },
    async UpdateTeamMember(teamId: string, mv: TeamMemberView) {
      return mockPatchTeam(teamId, (tm) => ({
        ...tm,
        members: tm.members.map((m) => {
          if (m.id !== mv.id) return mv.isLeader ? { ...m, isLeader: false } : m;
          return { ...mv };
        }),
      }));
    },
    async RemoveTeamMember(teamId: string, memberId: string) {
      return mockPatchTeam(teamId, (tm) => ({
        ...tm,
        members: tm.members.filter((m) => m.id !== memberId),
        tasks: tm.tasks.map((t) => (t.assigneeId === memberId ? { ...t, assigneeId: "" } : t)),
      }));
    },
    async AddTeamTask(teamId: string, tv: TeamTaskView) {
      return mockPatchTeam(teamId, (tm) => ({
        ...tm,
        tasks: [...tm.tasks, { ...tv, id: tv.id || mockTeamID("task"), status: tv.status || "queued" }],
      }));
    },
    async UpdateTeamTask(teamId: string, tv: TeamTaskView) {
      return mockPatchTeam(teamId, (tm) => ({
        ...tm,
        tasks: tm.tasks.map((t) => (t.id === tv.id ? { ...t, ...tv } : t)),
      }));
    },
    async MoveTeamTask(teamId: string, taskId: string, column: string) {
      const statusOf: Record<string, string> = {
        backlog: "queued", ready: "queued", doing: "running", blocked: "waiting",
        done: "succeeded", failed: "failed", cancelled: "cancelled",
      };
      return mockPatchTeam(teamId, (tm) => {
        const task = tm.tasks.find((t) => t.id === taskId);
        let assignee = task?.assigneeId ?? "";
        if (column === "backlog") assignee = "";
        if (column === "ready" && !assignee) {
          const best = tm.members.find((m) => !m.isLeader) ?? tm.members[0];
          assignee = best?.id ?? "";
        }
        return {
          ...tm,
          tasks: tm.tasks.map((t) =>
            t.id === taskId ? { ...t, status: statusOf[column] ?? t.status, assigneeId: assignee } : t,
          ),
        };
      });
    },
    async RemoveTeamTask(teamId: string, taskId: string) {
      return mockPatchTeam(teamId, (tm) => ({
        ...tm,
        tasks: tm.tasks.filter((t) => t.id !== taskId).map((t) => ({ ...t, deps: (t.deps ?? []).filter((d) => d !== taskId) })),
      }));
    },
    async TeamBoard(teamId: string) {
      const tm = mockFindTeam(teamId);
      if (!tm) throw new Error("团队不存在");
      return mockBoardOf(tm);
    },
    async SuggestTeamAssignee(teamId: string, taskId: string) {
      const tm = mockFindTeam(teamId);
      if (!tm) throw new Error("团队不存在");
      const task = tm.tasks.find((t) => t.id === taskId);
      return tm.members
        .map((m) => {
          const owned = m.skills.map((s) => s.toLowerCase());
          const matched = (task?.requiredSkills ?? []).filter((s) => owned.includes(s.toLowerCase()));
          return {
            memberId: m.id,
            name: m.name,
            score: matched.length,
            matched,
            missing: (task?.requiredSkills ?? []).filter((s) => !owned.includes(s.toLowerCase())),
            load: tm.tasks.filter((t) => t.assigneeId === m.id && !["succeeded", "failed", "cancelled", "stale"].includes(t.status)).length,
            isLeader: m.isLeader,
          };
        })
        .sort((a, b) => b.score - a.score || a.load - b.load || Number(a.isLeader) - Number(b.isLeader));
    },
    async SetTeamContext(teamId: string, ctx: TeamContextView) {
      return mockPatchTeam(teamId, (tm) => ({ ...tm, context: { ...ctx, version: (tm.context?.version ?? 0) + 1 } }));
    },
    async TeamBlackboardDigest(teamId: string) {
      const tm = mockFindTeam(teamId);
      if (!tm) return "";
      const parts = [`【团队目标】${tm.context?.goal || tm.goal}`];
      if (tm.context?.constraints) parts.push(`【约束】${tm.context.constraints}`);
      if (tm.members.length) parts.push(`【成员】${tm.members.map((m) => m.name).join("、")}`);
      return parts.join("\n");
    },
    async DraftTeamMember(input: TeamDraftInput) {
      const draft = mockDraftMemberFrom(input.instruction);
      if (!input.adopt) return draft;
      const tm = mockFindTeam(input.teamId);
      if (!tm) throw new Error("团队不存在");
      const created = { ...draft, id: mockTeamID("mem") };
      await this.AddTeamMember(input.teamId, created);
      return created;
    },
    async DraftTeamProject(input: TeamDraftInput) {
      const now = new Date().toISOString();
      const leader: TeamMemberView = {
        id: mockTeamID("mem"), name: "团长", role: "拆解目标、分派任务、跟踪进度",
        model: "", effort: "", skills: ["planning", "architecture"], tools: [],
        systemPrompt: "你就是团长。负责分析目标、拆解任务、按能力分派并跟踪进展，不亲自包办所有执行。",
        isLeader: true, avatar: "🧭",
      };
      const worker = { ...mockDraftMemberFrom(input.instruction), id: mockTeamID("mem") };
      const team: TeamProjectView = {
        id: mockTeamID("team"),
        name: input.instruction.trim().slice(0, 10) || "新团队",
        goal: input.instruction.trim(),
        members: [leader, worker],
        tasks: [],
        context: mockEmptyContext(input.instruction.trim()),
        policy: mockPolicy(),
        createdAt: now,
        updatedAt: now,
      };
      if (!input.adopt) return team;
      mockTeams = [...mockTeams, team];
      emitMockTeam(team);
      return { ...team };
    },
    async PlanTeamTasks(teamId: string, goal: string) {
      const tm = mockFindTeam(teamId);
      if (!tm) throw new Error("团队不存在");
      // Stand in for the leader's WBS: one card per member, routed by the
      // member's own top skill, plus a closing acceptance card.
      const blocks: TeamTaskView[] = tm.members.map((m, i) => ({
        ...emptyMockTask(),
        id: mockTeamID("task"),
        title: `${m.name}：完成${goal || tm.goal}`,
        desc: `由${m.name}负责的部分（${m.role || "按能力分派"}）`,
        requiredSkills: m.skills.slice(0, 1),
        assigneeId: m.isLeader ? "" : m.id,
        acceptance: [{ text: "产出可复核", done: false }],
        order: i,
      }));
      blocks.push({
        ...emptyMockTask(),
        id: mockTeamID("task"),
        title: `整体验收：${goal || tm.goal}`,
        desc: "端到端验收，确认目标达成",
        requiredSkills: [],
        acceptance: [{ text: "验收结论明确", done: false }],
        deps: blocks.map((b) => b.id),
        order: blocks.length,
      });
      return mockPatchTeam(teamId, (t) => ({
        ...t,
        context: { ...t.context, goal: goal || t.context.goal, version: (t.context?.version ?? 0) + 1 },
        tasks: [...t.tasks, ...blocks],
      }));
    },
    async RunTeamTask(teamId: string, taskId: string) {
      const tm = mockFindTeam(teamId);
      if (!tm) throw new Error("团队不存在");
      const updated = mockPatchTeam(teamId, (t) => ({
        ...t,
        tasks: t.tasks.map((tk) =>
          tk.id === taskId
            ? {
                ...tk,
                status: "running",
                progress: "已启动…",
                attempts: (tk.attempts ?? 0) + 1,
                error: "",
              }
            : tk,
        ),
      }));
      mockRunningTasks.add(`${teamId}/${taskId}`);
      // Land a simulated delivery shortly after so `pnpm dev` mirrors the real
      // start → progress → done flow (including the blackboard payoff).
      window.setTimeout(() => {
        if (!mockRunningTasks.has(`${teamId}/${taskId}`)) return;
        mockRunningTasks.delete(`${teamId}/${taskId}`);
        mockPatchTeam(teamId, (t) => ({
          ...t,
          tasks: t.tasks.map((tk) =>
            tk.id === taskId
              ? { ...tk, status: "succeeded", progress: "", error: "", deliverable: "（浏览器预览：模拟产出）" }
              : tk,
          ),
        }));
      }, 2500);
      return updated;
    },
    async CancelTeamTask(teamId: string, taskId: string) {
      mockRunningTasks.delete(`${teamId}/${taskId}`);
      mockPatchTeam(teamId, (t) => ({
        ...t,
        tasks: t.tasks.map((tk) =>
          tk.id === taskId ? { ...tk, status: "queued", progress: "", error: "" } : tk,
        ),
      }));
    },
    async RunningTeamTasks(teamId: string) {
      const prefix = `${teamId}/`;
      return [...mockRunningTasks]
        .filter((k) => k.startsWith(prefix))
        .map((k) => k.slice(prefix.length));
    },
  };
}
