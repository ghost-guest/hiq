// skillTemplates — the ops browser tab's skill library (运维技能库): ready-made
// starting points for recurring ops workflows. Table-form templates parse
// losslessly in the structured editor; prose-form ones are conversational
// protocols (runAs:inline) the chat model follows turn by turn — the editor
// opens them in source mode. Users instantiate one, adapt selectors/values
// to their site, save, then invoke via /name or the panel's ▶.
//
// GUARD: skill-doc.test.ts asserts every table-form template parses with
// lossy=false and every prose template carries a valid name — a template
// that breaks the editor's parser fails CI, not the user.

export type SkillTemplateForm = "table" | "prose";

export interface SkillTemplate {
  id: string;
  form: SkillTemplateForm;
  titleKey: string;
  descKey: string;
  build: () => string;
}

export interface SkillTemplateGroup {
  id: string;
  labelKey: string;
  templates: SkillTemplate[];
}

// --- table-form templates --------------------------------------------------------

function blankSkillContent(): string {
  return [
    "---",
    "name: browser-my-skill",
    "description: 浏览器操作技能（新建，请完善描述）。",
    "runAs: subagent",
    "allowed-tools: browser_open, browser_navigate, browser_click, browser_type, browser_wait, browser_extract",
    "---",
    "",
    "# 我的浏览器技能",
    "",
    "## 何时使用",
    "",
    "（说明这个操作流程的适用场景与参数）",
    "",
    "## 步骤",
    "",
    "| # | 操作 | 目标 | 值 |",
    "|---|------|------|------|",
    "| 1 | navigate | `https://…` |  |",
    "| 2 | human |  | 请在浏览器中输入短信验证码并提交 |",
    "| 3 | wait | `networkidle` | 15s |",
    "",
    "## 注意事项",
    "",
    "- human 步骤会在运行时暂停，等人工完成或自动检测条件满足后继续",
    "",
    "## 验证",
    "",
    "（如何确认执行成功）",
    "",
  ].join("\n");
}

// loginKeepSkillContent — 登录与保活：打开站点、人工登录（自动检测登录跳转）、
// 验证登录态；长间隔任务配合会话栏「保持会话」。
function loginKeepSkillContent(): string {
  return [
    "---",
    "name: browser-login-keep",
    "description: 打开站点、人工完成登录并验证登录态；配合保持会话支撑长间隔任务。",
    "runAs: subagent",
    "executor: browser-flow",
    "allowed-tools: browser_open, browser_navigate, browser_wait, browser_extract",
    "params: 站点=https://example.com",
    "---",
    "",
    "# 登录与保活",
    "",
    "## 何时使用",
    "",
    "需要先建立登录态再做后续操作时。参数：{{站点}} 为目标网址；登录由人工完成，检测条件按实际站点改为登录后的地址片段。",
    "",
    "## 步骤",
    "",
    "| # | 操作 | 目标 | 值 |",
    "|---|------|------|------|",
    "| 1 | navigate | `{{站点}}` |  |",
    "| 2 | human | `url:/home` | 请在浏览器中完成登录（密码/短信验证码/扫码） |",
    "| 3 | wait | `networkidle` | 15s |",
    "| 4 | extract | `.user-name, .avatar` |  |",
    "",
    "## 注意事项",
    "",
    "- 第 2 步登录成功（地址栏出现 /home）会自动继续，也可点「已完成，继续」",
    "- 第 4 步选择器改成站点的用户名/头像元素，用于确认登录态",
    "- 两轮操作间隔长时，在面板会话栏打开「保持会话」；对话式调用则让 agent 用 browser_keepalive",
    "",
    "## 验证",
    "",
    "第 4 步提取到用户名/头像信息即登录成功。",
    "",
  ].join("\n");
}

// formSkillContent — 表单填报：ask 拿数据 → 填表 → 人工过验证 → 提交 →
// stable 等流式回执 → 提取。
function formSkillContent(): string {
  return [
    "---",
    "name: browser-form-submit",
    "description: 问用户拿单号、填表、人工过验证后提交并提取回执。",
    "runAs: subagent",
    "executor: browser-flow",
    "allowed-tools: browser_open, browser_navigate, browser_type, browser_click, browser_wait, browser_extract",
    "params: 站点=https://example.com/order",
    "---",
    "",
    "# 表单填报",
    "",
    "## 何时使用",
    "",
    "需要向网站提交一条表单时。参数：{{站点}} 为目标网址（可在参数默认值里改）。",
    "",
    "## 步骤",
    "",
    "| # | 操作 | 目标 | 值 |",
    "|---|------|------|------|",
    "| 1 | navigate | `{{站点}}` |  |",
    "| 2 | ask | 工单号 | 请输入要提交的工单号： |",
    "| 3 | type | `#order-no` | {{工单号}} |",
    "| 4 | click | `button.next` |  |",
    "| 5 | human | `visible:.captcha-done` | 请在浏览器中完成滑块验证 |",
    "| 6 | click | `button[type=submit]` |  |",
    "| 7 | wait | `stable:.receipt` | 60s |",
    "| 8 | extract | `.receipt` |  |",
    "",
    "## 注意事项",
    "",
    "- ask 的回复存为 {{工单号}}，第 3 步直接引用；试运行时在面板输入框里填",
    "- human 步骤暂停等人工完成滑块，检测条件满足后自动继续",
    "- 第 7 步 stable: 等回执区域内容稳定（流式输出完成）再提取",
    "",
    "## 验证",
    "",
    "第 8 步提取到回执编号即成功。",
    "",
  ].join("\n");
}

// streamQuerySkillContent — 流式问答：面向 AI/对话类站点。ask 拿问题 →
// 填入输入框发送 → stable: 等回答输出完成 → 提取回答。
function streamQuerySkillContent(): string {
  return [
    "---",
    "name: browser-stream-query",
    "description: 向 AI/对话站点提问，等流式回答输出完成后提取结果。",
    "runAs: subagent",
    "executor: browser-flow",
    "allowed-tools: browser_open, browser_navigate, browser_type, browser_click, browser_wait, browser_extract",
    "params: 站点=https://chat.example.com",
    "---",
    "",
    "# 流式问答",
    "",
    "## 何时使用",
    "",
    "需要用网页版 AI/对话站点完成一次提问并取回回答时。参数：{{站点}} 为对话站点网址。",
    "",
    "## 步骤",
    "",
    "| # | 操作 | 目标 | 值 |",
    "|---|------|------|------|",
    "| 1 | navigate | `{{站点}}` |  |",
    "| 2 | human | `url:/chat` | 请在浏览器中完成登录 |",
    "| 3 | ask | 问题 | 要向站点提什么问题？ |",
    "| 4 | type | `#prompt-textarea` | {{问题}} |",
    "| 5 | key | `#prompt-textarea` | enter |",
    "| 6 | wait | `stable:.answer-body` | 300s |",
    "| 7 | extract | `.answer-body` |  |",
    "",
    "## 注意事项",
    "",
    "- 输入框与回答区域的选择器按实际站点改；回答区也可用 main、[data-message] 之类",
    "- 第 6 步 stable: 判定流式输出停止变化 2 秒即完成，超时 300 秒可按需调大",
    "- 提问框支持 Ctrl+Enter 的站点，把第 5 步改为点击发送按钮",
    "",
    "## 验证",
    "",
    "第 7 步提取到非空回答即成功。",
    "",
  ].join("\n");
}

// dataExportSkillContent — 数据导出：ask 拿筛选条件 → 应用筛选 → 导出 →
// 等下载/渲染完成 → 提取结果。
function dataExportSkillContent(): string {
  return [
    "---",
    "name: browser-data-export",
    "description: 按条件筛选并导出报表/日志，等待完成后提取导出结果。",
    "runAs: subagent",
    "executor: browser-flow",
    "allowed-tools: browser_open, browser_navigate, browser_type, browser_click, browser_wait, browser_extract",
    "params: 站点=https://example.com/reports",
    "---",
    "",
    "# 数据导出",
    "",
    "## 何时使用",
    "",
    "需要从管理后台导出一段数据时。参数：{{站点}} 为报表页网址。",
    "",
    "## 步骤",
    "",
    "| # | 操作 | 目标 | 值 |",
    "|---|------|------|------|",
    "| 1 | navigate | `{{站点}}` |  |",
    "| 2 | human | `url:/reports` | 请在浏览器中完成登录 |",
    "| 3 | ask | 日期范围 | 导出哪个日期范围？（如 2026-09-01~2026-09-30） |",
    "| 4 | type | `#date-range` | {{日期范围}} |",
    "| 5 | click | `button.apply-filter` |  |",
    "| 6 | wait | `networkidle` | 20s |",
    "| 7 | click | `button.export` |  |",
    "| 8 | wait | `networkidle` | 60s |",
    "| 9 | extract | `.export-result, .toast` |  |",
    "",
    "## 注意事项",
    "",
    "- 筛选控件（日期、下拉）按实际站点改；多条件就多加几行 type/select",
    "- 导出是下载文件时，第 8 步等 networkidle 后查看下载记录即可",
    "- 大报表导出慢，第 8 步超时给足",
    "",
    "## 验证",
    "",
    "第 9 步提取到导出成功提示或下载完成即成功。",
    "",
  ].join("\n");
}

// --- prose-form templates --------------------------------------------------------

// siemWatchSkillContent — 安全日志值守（定时）：配合 schedule_create 定时触发。
// 与值守循环的关键差异是无人值守适配——定时触发时没人在场：登录态靠浏览器
// 持久化配置文件的 cookie 延续；掉线不等人，改成发邮件通知重新登录；分析
// 判定后发现问题发邮件告警；同轮最多一封防刷屏。
function siemWatchSkillContent(): string {
  return `---
name: browser-siem-watch
description: 安全平台定时巡检——定时读取安全态势感知平台日志并分析判断，发现问题发邮件告警；掉线发邮件通知重新登录，绝不人工等待。
runAs: inline
---

# 安全日志值守协议（定时巡检）

你是安全巡检编排者，本协议按定时任务逐次触发执行（也可手动运行一次）。浏览器操作交给 run_skill("browser-auto") 子代理；你负责：登录态判断、日志分析、告警决策、邮件通知。**无人值守是前提：任何环节都不要等待人工。**

## 参数（来自调用参数或技能参数默认值，缺了就用合理默认并继续）

- {{平台地址}}：安全态势感知平台网址（必填，缺失时发邮件说明配置不全并结束）
- {{日志路径}}：日志/告警页面路径（默认 /logs，按实际平台改）
- {{告警邮箱}}：问题通知收件人（必填）
- {{判定要点}}：关注什么（默认：高危告警、异地/非工作时间登录、短时间内批量失败、策略命中突增）

## 首次设置（人工，一次性）

在运维浏览器或对话里先完整登录一次平台（验证码/密码由人完成）——浏览器持久化配置文件会记住登录 cookie，后续定时巡检靠它免登录。设置定时：对对话说「每 30 分钟运行一次 browser-siem-watch」即可，agent 会用 schedule_create 建任务（支持 every 30m / cron / daily 09:00，任务跨重启持久化，结果存档可查，还可在任务上配置 im/email/notify 二次投递）。

## 每次巡检（定时触发或手动运行）

1. 派发 run_skill("browser-auto", arguments="browser_open 打开 {{平台地址}}{{日志路径}}，然后 browser_extract 提取页面全文原样带回；若被重定向到登录页，只回一句 REDIRECTED_LOGIN")。
2. 登录态判断：子代理回 REDIRECTED_LOGIN 或提取内容是登录表单 → 判定掉线。**不要等待任何人**：email_send 给 {{告警邮箱}}，标题「[SIEM巡检] 登录态失效，需要重新登录」，正文写明平台地址和「请在 hiq 里重新登录一次（运行 browser-login-keep 或对话里说重登）」；本轮结果记为「掉线，已通知」，结束。
3. 日志分析：逐条读提取内容，按 {{判定要点}} 判断。宁可保守——可疑但不确定的条目列为「待人工复核」，不触发告警邮件。
4. 发现问题 → email_send 给 {{告警邮箱}}：标题「[SIEM巡检] 发现 {{条数}} 项问题」，正文按条列出（时间、级别、内容、初步判断、建议动作）。**一轮巡检最多发一封告警邮件**（多条问题合并进同一封），避免刷屏。
5. 无问题 → 不发任何邮件，结果写一句「本轮巡检无异常（提取 N 条，复核 M 条）」。
6. 把本轮结果摘要作为最终回复返回——定时任务会把它存档，schedule_list 可查。

## 硬性约束

- 绝不代替用户输入密码、验证码；掉线的正确动作是邮件通知，不是等待。
- 无头触发（触发时没有打开的对话页）email_send 默认被拒绝：要么先在设置里为 email_send 加放行规则，要么保持一个 cowork 对话页开着（交互路径可弹审批）。
- 邮箱防刷屏：掉线通知只在登录态「由好变坏」的第一次发；告警邮件每轮至多一封。
- 结果永远返回摘要（哪怕是「配置不全」「掉线」），空结果会让定时任务记录变成黑洞。
`;
}

// pagePatrolSkillContent — 页面巡检（对话式）：逐轮读取页面关键指标并与
// 上一轮对比，变化即报；空闲期间保活；用户消息唤醒下一轮。
function pagePatrolSkillContent(): string {
  return `---
name: browser-page-patrol
description: 页面巡检循环——逐轮读取指定页面的关键指标并与上一轮对比，变化即报告；空闲保活，用户消息唤醒下一轮。
runAs: inline
---

# 页面巡检协议

你是页面巡检编排者。浏览器操作交给 run_skill("browser-auto") 子代理，你负责：维护上一轮快照、对比变化、向用户报告。从唤起起持续生效，直到用户说结束。

## 开局（只做一次）

1. 巡检目标来自调用参数（Arguments 行，格式：URL + 要关注的指标描述）；没有就问用户要。
2. 派发 run_skill("browser-auto", arguments="用 browser_open 打开 <URL>，browser_extract 提取页面全文，原样带回")。记住返回的 session_id。
3. 请用户在浏览器窗口完成登录（如需要），完成后在对话里回复；随后用子代理重提取一次，作为第 0 轮基线快照，向用户简报一次当前指标。

## 每轮巡检（用户消息唤醒或按用户要求的节奏）

1. 派发子代理：复用 session_id=<id>（明确「不要 browser_open」），重新提取页面全文带回。
2. 与上一轮快照对比：数值变化、状态词变化、新增/消失的关键内容。没有变化就一句话带过，有变化列出变化点（旧值 → 新值）。
3. 更新你记忆中的快照为本轮内容。
4. 若用户要求固定节奏（如每 30 分钟），说明：两轮之间需要用户发消息唤醒，或让用户在面板开启「保持会话」防掉线；同意保活则派发子代理调用 browser_keepalive(session_id=<id>, enabled=true, interval_sec=300, mode="ping")。
5. 结束回合等待用户下一条消息。

## 结束

用户说结束时：browser_keepalive(session_id=<id>, enabled=false)，总结本轮巡检发现的所有变化。

## 硬性约束

- 绝不代替用户输入密码、验证码、扫码。
- 子代理报 session 不存在：浏览器已被回收，回到开局第 2 步重开并提醒用户重新登录。
- 报告用中文、变化点列表化、无变化不啰嗦。
`;
}

// supervisorLoopSkillContent — 值守循环（对话式）：通用形态——打开站点等
// 人登录，之后逐轮接收对话指令、定位网页操作、等流式输出完成再回报，
// 保活询问，用户消息唤醒。
function supervisorLoopSkillContent(): string {
  return `---
name: browser-site-console
description: 网站值守循环——打开站点等用户人工登录，之后逐轮接收对话指令、定位网页元素操作、等流式输出完成再回报，支持会话保活与跨轮唤醒。
runAs: inline
---

# 网站值守循环协议

你是浏览器值守编排者。从唤起起本协议持续生效，直到用户明确说结束。浏览器操作一律交给 run_skill("browser-auto") 子代理执行；你负责：理解用户、拆解任务、派发子代理、判断结果、回复用户。

## 开局（只做一次）

1. 站点地址来自调用参数（Arguments 行）；没有就先问用户要。
2. 派发 run_skill("browser-auto", arguments="用 browser_open 打开 <站点URL> 后停在当前页，不要做其它操作，把返回的 session_id 原文带回来")。记住这个 session_id——它是整个值守期间的锚点。
3. 对用户说：「请在弹出的浏览器窗口里完成登录（密码 / 短信验证码 / 扫码都由您手动完成）。完成后直接在对话里回复我，例如『好了』或『已登录』。」然后结束本回合等待。
4. 用户回复任何表示完成的意思（好了 / 成了 / done / 登录完了 等变体，自行理解）后，派发子代理验证登录态：browser_wait(condition="url:<登录后路径>") 或提取页面特征确认；未成功就继续等，绝不代替用户输入凭据。

## 每轮循环（用户每条消息进来）

1. 理解指令：归类意图（查询 / 填写 / 点击 / 提取 / 组合）；找出用户指定的网页位置——给了准确 CSS 选择器就直接用；给的是描述（如「搜索框」「提交按钮」），让子代理先 browser_snapshot 再定位。
2. 派发子代理，arguments 必须包含：复用 session_id=<id>（明确写「不要 browser_open，不要新开会话」）、目标元素定位、要执行的操作。
3. 等待远端回复：远端常见流式输出。要求子代理在操作后先 browser_wait(condition="stable:<输出容器选择器>")（内容稳定 2 秒即完成）；不知道选择器时用 networkidle。完成后再 browser_extract 提取回复内容带回。
4. 判断与回报：自己读提取结果，思考后在对话里给出结论；用户要原文才贴原文，长内容给摘录 + 要点。
5. 保活询问（只在第一轮结束时问一次，记住答复）：「两次指令间隔如果较长，需要我保持网页登录会话吗？」同意 → 派发子代理调用 browser_keepalive(session_id=<id>, enabled=true, interval_sec=300, mode="ping")；拒绝 → 不保活。用户随时可改主意。
6. 结束回合，等待用户下一条消息——用户的任何新消息就是唤醒信号，回到第 1 步。

## 结束

用户表达「结束值守」类意思时：派发子代理 browser_keepalive(session_id=<id>, enabled=false) 停掉心跳，然后总结本次值守做过的事。浏览器会话会随空闲自动回收，无需强关。

## 硬性约束

- 绝不代替用户输入密码、验证码、扫码、人机验证——这些永远由用户在浏览器窗口人工完成。
- 子代理报 session 不存在：说明浏览器已被空闲回收，回到开局第 2 步重开，并提醒用户需重新登录。
- 回复用中文、结论先行、不啰嗦。
`;
}

// --- library ---------------------------------------------------------------------

// SKILL_TEMPLATE_GROUPS is the ops skill library the 新建技能 gallery renders.
// Locale keys are stored in full (brc.tpl*) so the panel renders them directly.
export const SKILL_TEMPLATE_GROUPS: SkillTemplateGroup[] = [
  {
    id: "quick",
    labelKey: "brc.tgQuick",
    templates: [
      { id: "blank", form: "table", titleKey: "brc.tplBlankTitle", descKey: "brc.tplBlankDesc", build: blankSkillContent },
    ],
  },
  {
    id: "common",
    labelKey: "brc.tgCommon",
    templates: [
      { id: "login-keep", form: "table", titleKey: "brc.tplLoginTitle", descKey: "brc.tplLoginDesc", build: loginKeepSkillContent },
      { id: "form-submit", form: "table", titleKey: "brc.tplFormTitle", descKey: "brc.tplFormDesc", build: formSkillContent },
      { id: "stream-query", form: "table", titleKey: "brc.tplStreamTitle", descKey: "brc.tplStreamDesc", build: streamQuerySkillContent },
      { id: "data-export", form: "table", titleKey: "brc.tplExportTitle", descKey: "brc.tplExportDesc", build: dataExportSkillContent },
    ],
  },
  {
    id: "loop",
    labelKey: "brc.tgLoop",
    templates: [
      { id: "siem-watch", form: "prose", titleKey: "brc.tplSiemTitle", descKey: "brc.tplSiemDesc", build: siemWatchSkillContent },
      { id: "page-patrol", form: "prose", titleKey: "brc.tplPatrolTitle", descKey: "brc.tplPatrolDesc", build: pagePatrolSkillContent },
      { id: "site-console", form: "prose", titleKey: "brc.tplLoopTitle", descKey: "brc.tplLoopDesc", build: supervisorLoopSkillContent },
    ],
  },
];
