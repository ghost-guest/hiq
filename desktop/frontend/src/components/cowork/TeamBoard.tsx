// TeamBoard is the coWork "团队" panel: a persistent multi-agent project with a
// 团长 (leader) + 团员 (members) and a kanban board.
//
// Layout: header (team picker + new-team + blackboard) → members rail on the
// left → kanban columns on the right. Cards are draggable between columns;
// Backlog is "queued + unassigned" and Ready auto-routes an unassigned card to
// the best-skilled member (server-side capability routing).
//
// Members can be created two ways: a manual form, or natural language
// ("帮我加一个擅长后端的团员") which asks the model for a draft the user reviews
// before anything is written.
//
// Subscribes to "team:changed" so the board reflects mutations made anywhere
// (including future leader-driven planning) without a manual refresh.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Plus, Trash2, Pencil, Users, LayoutDashboard, Sparkles, Wand2, Crown, ListChecks, X } from "lucide-react";

import { app, onTeamChanged } from "../../lib/bridge";
import type {
  TeamProjectView,
  TeamMemberView,
  TeamTaskView,
  TeamColumnView,
  TeamDraftInput,
} from "../../lib/types";
import { useT } from "../../lib/i18n";
import { useToast } from "../../lib/toast";
import { useConfirm } from "../../lib/confirm";

// Column order + the state each column maps to on drop (mirrors the Go board).
const COLUMN_ORDER = ["backlog", "ready", "doing", "blocked", "done", "failed", "cancelled"] as const;

// COLUMN_LABEL maps a column key to its translation key. The literal union (not
// a computed `team.col.${key}` template) keeps t()'s DictKey typing satisfied.
const COLUMN_LABEL: Record<string, "team.col.backlog" | "team.col.ready" | "team.col.doing" | "team.col.blocked" | "team.col.done" | "team.col.failed" | "team.col.cancelled"> = {
  backlog: "team.col.backlog",
  ready: "team.col.ready",
  doing: "team.col.doing",
  blocked: "team.col.blocked",
  done: "team.col.done",
  failed: "team.col.failed",
  cancelled: "team.col.cancelled",
};

function emptyMember(): TeamMemberView {
  return {
    id: "", name: "", role: "", model: "", effort: "", skills: [],
    tools: [], systemPrompt: "", isLeader: false, avatar: "",
  };
}

function emptyTask(): TeamTaskView {
  return {
    id: "", title: "", desc: "", assigneeId: "", assigneeName: "", requiredSkills: [],
    status: "queued", column: "backlog", deps: [], parentId: "", acceptance: [],
    deliverable: "", attempts: 0, evidence: [], progress: "", error: "", order: 0,
  };
}

// groupColumns projects a team's tasks onto the fixed kanban columns locally
// (each task already carries its server-derived `column`), so the board stays
// reactive to team:changed without a second round-trip.
function groupColumns(team: TeamProjectView): TeamColumnView[] {
  const byKey: Record<string, TeamColumnView> = {};
  const cols: TeamColumnView[] = COLUMN_ORDER.map((key) => {
    const c: TeamColumnView = { key, label: key, states: [], tasks: [] };
    byKey[key] = c;
    return c;
  });
  for (const task of team.tasks) {
    (byKey[task.column] ?? byKey.backlog).tasks.push(task);
  }
  return cols;
}

export function TeamBoard() {
  const t = useT();
  const { showToast } = useToast();
  const confirm = useConfirm();

  const [teams, setTeams] = useState<TeamProjectView[] | null>(null);
  const [activeId, setActiveId] = useState("");
  const [showMembers, setShowMembers] = useState(false);
  const [showBlackboard, setShowBlackboard] = useState(false);
  const [memberDraft, setMemberDraft] = useState<TeamMemberView | null>(null);
  const [taskDraft, setTaskDraft] = useState<TeamTaskView | null>(null);
  const [newTeam, setNewTeam] = useState(false);
  const [showPlan, setShowPlan] = useState(false);
  const [dragTaskId, setDragTaskId] = useState("");
  const [dragOverCol, setDragOverCol] = useState("");

  const team = useMemo(() => teams?.find((x) => x.id === activeId) ?? null, [teams, activeId]);

  const refresh = useCallback(async () => {
    try {
      const list = await app.ListTeamProjects();
      setTeams(list);
    } catch {
      setTeams([]);
    }
  }, []);

  useEffect(() => { void refresh(); }, [refresh]);

  // team:changed → patch the matching team in place (or drop a deleted one).
  useEffect(() => {
    return onTeamChanged((tv) => {
      if (!tv?.id) return;
      if (tv.deleted) {
        setTeams((prev) => (prev ?? []).filter((x) => x.id !== tv.id));
        return;
      }
      setTeams((prev) => {
        const list = prev ?? [];
        const full = tv as TeamProjectView;
        if (!list.some((x) => x.id === full.id)) return [...list, full];
        return list.map((x) => (x.id === full.id ? full : x));
      });
    });
  }, []);

  // Auto-select the first team once loaded.
  useEffect(() => {
    if (teams && teams.length > 0 && !teams.some((x) => x.id === activeId)) {
      setActiveId(teams[0].id);
    }
  }, [teams, activeId]);

  const applyTeam = useCallback((updated: TeamProjectView) => {
    setTeams((prev) => (prev ?? []).map((x) => (x.id === updated.id ? updated : x)));
  }, []);

  const onDropCard = useCallback(async (column: string) => {
    const taskId = dragTaskId;
    setDragTaskId("");
    setDragOverCol("");
    if (!team || !taskId) return;
    const before = team.tasks.find((x) => x.id === taskId)?.column;
    if (before === column) return;
    try {
      const updated = await app.MoveTeamTask(team.id, taskId, column);
      applyTeam(updated);
      const after = updated.tasks.find((x) => x.id === taskId)?.column;
      if (after !== column) showToast(t("team.moveFailed"), "warn");
    } catch (e) {
      showToast(String(e), "error");
    }
  }, [team, dragTaskId, applyTeam, showToast, t]);

  const removeTask = useCallback(async (task: TeamTaskView) => {
    if (!team) return;
    if (!(await confirm({ title: t("common.delete"), message: task.title, danger: true }))) return;
    try {
      applyTeam(await app.RemoveTeamTask(team.id, task.id));
    } catch (e) {
      showToast(String(e), "error");
    }
  }, [team, confirm, applyTeam, showToast, t]);

  const removeMember = useCallback(async (m: TeamMemberView) => {
    if (!team) return;
    if (!(await confirm({ title: t("team.removeMember"), message: `${m.name} — ${t("team.removeMemberConfirm")}`, danger: true }))) return;
    try {
      applyTeam(await app.RemoveTeamMember(team.id, m.id));
    } catch (e) {
      showToast(String(e), "error");
    }
  }, [team, confirm, applyTeam, showToast, t]);

  const deleteTeam = useCallback(async () => {
    if (!team) return;
    if (!(await confirm({ title: t("team.delete"), message: t("team.deleteConfirm"), danger: true }))) return;
    try {
      await app.DeleteTeamProject(team.id);
      setActiveId("");
      showToast(t("team.deleted"), "info");
    } catch (e) {
      showToast(String(e), "error");
    }
  }, [team, confirm, showToast, t]);

  if (teams === null) {
    return <div className="team">{t("common.loading")}</div>;
  }

  const columns = team ? groupColumns(team) : [];

  return (
    <div className="team">
      <header className="team__head">
        <div className="team__head-left">
          <LayoutDashboard size={15} />
          <span className="team__title">{t("team.title")}</span>
          {teams.length > 0 && (
            <select
              className="team__select"
              value={activeId}
              onChange={(e) => setActiveId(e.target.value)}
              aria-label={t("team.select")}
            >
              {teams.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
            </select>
          )}
        </div>
        <div className="team__head-right">
          {team && (
            <>
              <button className="btn btn--small" onClick={() => setShowMembers(true)}>
                <Users size={13} /> {t("team.members")} ({team.members.length})
              </button>
              <button className="btn btn--small" onClick={() => setShowBlackboard(true)}>
                <ListChecks size={13} /> {t("team.blackboard")}
              </button>
              <button
                className="btn btn--small"
                onClick={() => {
                  if (team.members.length === 0) { showToast(t("team.planNoMembers"), "warn"); return; }
                  setShowPlan(true);
                }}
              >
                <Sparkles size={13} /> {t("team.plan")}
              </button>
              <button className="btn btn--small" onClick={() => setTaskDraft(emptyTask())}>
                <Plus size={13} /> {t("team.addTask")}
              </button>
              <button className="btn btn--small btn--danger" onClick={() => void deleteTeam()} title={t("team.delete")}>
                <Trash2 size={13} />
              </button>
            </>
          )}
          <button className="btn btn--small btn--primary" onClick={() => setNewTeam(true)}>
            <Sparkles size={13} /> {t("team.new")}
          </button>
        </div>
      </header>

      {!team ? (
        <div className="team__empty">
          <Users size={28} />
          <p>{t("team.empty")}</p>
          <button className="btn btn--primary" onClick={() => setNewTeam(true)}>
            <Sparkles size={14} /> {t("team.newByNL")}
          </button>
        </div>
      ) : (
        <div className="team__body">
          <div className="team__board">
            {columns.map((col) => (
              <section
                key={col.key}
                className={`team-col${dragOverCol === col.key ? " team-col--over" : ""}`}
                onDragOver={(e) => { e.preventDefault(); setDragOverCol(col.key); }}
                onDragLeave={() => setDragOverCol((c) => (c === col.key ? "" : c))}
                onDrop={(e) => { e.preventDefault(); void onDropCard(col.key); }}
              >
                <div className="team-col__head">
                  <span className={`team-col__dot team-col__dot--${col.key}`} />
                  <span className="team-col__label">{t(COLUMN_LABEL[col.key] ?? "team.col.backlog")}</span>
                  <span className="team-col__count">{col.tasks.length}</span>
                </div>
                <div className="team-col__cards">
                  {col.tasks.map((task) => (
                    <TeamCard
                      key={task.id}
                      task={task}
                      team={team}
                      dragging={dragTaskId === task.id}
                      onDragStart={() => setDragTaskId(task.id)}
                      onDragEnd={() => { setDragTaskId(""); setDragOverCol(""); }}
                      onEdit={() => setTaskDraft(task)}
                      onRemove={() => void removeTask(task)}
                    />
                  ))}
                  {col.tasks.length === 0 && <div className="team-col__blank" />}
                </div>
              </section>
            ))}
          </div>
        </div>
      )}

      {showMembers && team && (
        <MembersModal
          team={team}
          onClose={() => setShowMembers(false)}
          onEdit={(m) => { setShowMembers(false); setMemberDraft(m); }}
          onAdd={() => { setShowMembers(false); setMemberDraft(emptyMember()); }}
          onRemove={removeMember}
        />
      )}

      {showBlackboard && team && (
        <BlackboardModal team={team} onClose={() => setShowBlackboard(false)} onUpdate={applyTeam} />
      )}

      {showPlan && team && (
        <PlanModal
          team={team}
          onClose={() => setShowPlan(false)}
          onPlanned={(updated) => { applyTeam(updated); setShowPlan(false); showToast(t("team.saved"), "info"); }}
        />
      )}

      {memberDraft && team && (
        <MemberEditor
          team={team}
          initial={memberDraft}
          onClose={() => setMemberDraft(null)}
          onSaved={(updated) => { applyTeam(updated); setMemberDraft(null); showToast(t("team.saved"), "info"); }}
        />
      )}

      {taskDraft && team && (
        <TaskEditor
          team={team}
          initial={taskDraft}
          onClose={() => setTaskDraft(null)}
          onSaved={(updated) => { applyTeam(updated); setTaskDraft(null); showToast(t("team.saved"), "info"); }}
        />
      )}

      {newTeam && (
        <NewTeamModal
          onClose={() => setNewTeam(false)}
          onCreated={(created) => {
            setTeams((prev) => [...(prev ?? []), created]);
            setActiveId(created.id);
            setNewTeam(false);
            showToast(t("team.saved"), "info");
          }}
        />
      )}
    </div>
  );
}

// --- card --------------------------------------------------------------------

function TeamCard({
  task, team, dragging, onDragStart, onDragEnd, onEdit, onRemove,
}: {
  task: TeamTaskView;
  team: TeamProjectView;
  dragging: boolean;
  onDragStart: () => void;
  onDragEnd: () => void;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const t = useT();
  const assignee = team.members.find((m) => m.id === task.assigneeId);
  return (
    <article
      className={`team-card${dragging ? " team-card--dragging" : ""}`}
      draggable
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
    >
      <div className="team-card__top">
        <span className="team-card__title">{task.title}</span>
        <span className="team-card__actions">
          <button className="team-card__btn" onClick={onEdit} title={t("team.editTask")}><Pencil size={12} /></button>
          <button className="team-card__btn team-card__btn--danger" onClick={onRemove} title={t("common.delete")}><Trash2 size={12} /></button>
        </span>
      </div>
      {task.desc && <p className="team-card__desc">{task.desc}</p>}
      {task.requiredSkills.length > 0 && (
        <div className="team-card__tags">
          {task.requiredSkills.map((s) => <span key={s} className="team-chip team-chip--skill">{s}</span>)}
        </div>
      )}
      {task.progress && <p className="team-card__progress">{t("team.progress")}: {task.progress}</p>}
      {task.error && <p className="team-card__error">{t("team.error")}: {task.error}</p>}
      <div className="team-card__foot">
        <span className={`team-card__who${assignee ? "" : " team-card__who--none"}`}>
          {assignee ? `${assignee.avatar || "👤"} ${assignee.name}` : t("team.task.unassigned")}
        </span>
        {task.deps.length > 0 && <span className="team-card__dep">⇢ {task.deps.length}</span>}
        {task.attempts > 0 && <span className="team-card__attempts">{t("team.attempts", { n: task.attempts })}</span>}
      </div>
    </article>
  );
}

// --- members -----------------------------------------------------------------

function MembersModal({
  team, onClose, onEdit, onAdd, onRemove,
}: {
  team: TeamProjectView;
  onClose: () => void;
  onEdit: (m: TeamMemberView) => void;
  onAdd: () => void;
  onRemove: (m: TeamMemberView) => void;
}) {
  const t = useT();
  return (
    <div className="cowork-taskform-overlay" onClick={onClose}>
      <div className="cowork-taskform" onClick={(e) => e.stopPropagation()}>
        <header className="cowork-taskform__head">
          <span className="team__title">{t("team.members")} · {t("team.memberCount", { n: team.members.length })}</span>
          <button className="cowork-task-card__btn" onClick={onClose}><X size={16} /></button>
        </header>
        <div className="cowork-taskform__body">
          <ul className="team-members">
            {team.members.map((m) => (
              <li key={m.id} className={`team-member${m.isLeader ? " team-member--leader" : ""}`}>
                <span className="team-member__avatar">{m.avatar || (m.isLeader ? "🧭" : "👤")}</span>
                <div className="team-member__meta">
                  <div className="team-member__name">
                    {m.name}
                    {m.isLeader && <span className="team-chip team-chip--leader"><Crown size={10} /> {t("team.leaderBadge")}</span>}
                  </div>
                  {m.role && <div className="team-member__role">{m.role}</div>}
                  {m.skills.length > 0 && (
                    <div className="team-member__skills">
                      {m.skills.map((s) => <span key={s} className="team-chip team-chip--skill">{s}</span>)}
                    </div>
                  )}
                </div>
                <div className="team-member__ops">
                  <button className="cowork-task-card__btn" onClick={() => onEdit(m)} title={t("team.editMember")}><Pencil size={13} /></button>
                  <button className="cowork-task-card__btn cowork-task-card__btn--danger" onClick={() => onRemove(m)} title={t("team.removeMember")}><Trash2 size={13} /></button>
                </div>
              </li>
            ))}
          </ul>
          <button className="cowork-taskform__quick" onClick={onAdd}>
            <Plus size={13} /> {t("team.addMember")}
          </button>
        </div>
      </div>
    </div>
  );
}

// MemberEditor creates/edits one member. Two paths: a manual form, and natural
// language drafting (the model proposes, the user reviews, then it is created).
function MemberEditor({
  team, initial, onClose, onSaved,
}: {
  team: TeamProjectView;
  initial: TeamMemberView;
  onClose: () => void;
  onSaved: (updated: TeamProjectView) => void;
}) {
  const t = useT();
  const { showToast } = useToast();
  const isNew = !initial.id;
  const [tab, setTab] = useState<"manual" | "nl">(isNew ? "nl" : "manual");
  const [form, setForm] = useState<TeamMemberView>({ ...emptyMember(), ...initial });
  const [skillsText, setSkillsText] = useState((initial.skills ?? []).join(", "));
  const [nlText, setNlText] = useState("");
  const [nlBusy, setNlBusy] = useState(false);
  const [draft, setDraft] = useState<TeamMemberView | null>(null);
  const [saving, setSaving] = useState(false);

  const set = <K extends keyof TeamMemberView>(k: K, v: TeamMemberView[K]) => setForm((f) => ({ ...f, [k]: v }));

  const toModel = (m: TeamMemberView): TeamMemberView => ({
    ...m,
    name: m.name.trim(),
    skills: skillsText.split(/[,，]/).map((s) => s.trim()).filter(Boolean),
  });
  const draftToModel = (m: TeamMemberView): TeamMemberView => ({
    ...m,
    skills: (m.skills ?? []).map((s) => s.trim()).filter(Boolean),
  });

  const save = async (m: TeamMemberView, useSkillsText: boolean) => {
    setSaving(true);
    try {
      const model = useSkillsText ? toModel(m) : draftToModel(m);
      if (!model.name) { showToast(t("team.member.name"), "warn"); setSaving(false); return; }
      const updated = isNew
        ? await app.AddTeamMember(team.id, model)
        : await app.UpdateTeamMember(team.id, model);
      onSaved(updated);
    } catch (e) {
      showToast(String(e), "error");
    } finally {
      setSaving(false);
    }
  };

  const generate = async () => {
    if (!nlText.trim()) return;
    setNlBusy(true);
    try {
      const input: TeamDraftInput = { teamId: team.id, instruction: nlText, model: "", adopt: false };
      setDraft(await app.DraftTeamMember(input));
    } catch (e) {
      showToast(t("team.nl.failed", { msg: String(e) }), "error");
    } finally {
      setNlBusy(false);
    }
  };

  const adoptDraft = async () => {
    if (!draft) return;
    await save(draft, false);
  };

  const editDraft = () => {
    if (!draft) return;
    setForm({ ...emptyMember(), ...draft });
    setSkillsText((draft.skills ?? []).join(", "));
    setDraft(null);
    setTab("manual");
  };

  return (
    <div className="cowork-taskform-overlay" onClick={onClose}>
      <div className="cowork-taskform" onClick={(e) => e.stopPropagation()}>
        <header className="cowork-taskform__head">
          <span className="team__title">{isNew ? t("team.addMember") : t("team.editMember")}</span>
          <button className="cowork-task-card__btn" onClick={onClose}><X size={16} /></button>
        </header>

        {isNew && (
          <div className="team-tabs">
            <button className={`team-tabs__btn${tab === "nl" ? " team-tabs__btn--active" : ""}`} onClick={() => setTab("nl")}>
              <Wand2 size={12} /> {t("team.nl.title")}
            </button>
            <button className={`team-tabs__btn${tab === "manual" ? " team-tabs__btn--active" : ""}`} onClick={() => setTab("manual")}>
              <Pencil size={12} /> {t("team.member.name")}
            </button>
          </div>
        )}

        {tab === "nl" && isNew ? (
          <div className="cowork-taskform__body">
            <p className="team-hint">{t("team.nl.hint")}</p>
            <textarea
              className="cowork-taskform__input team-textarea"
              rows={3}
              value={nlText}
              placeholder={t("team.nl.memberPlaceholder")}
              onChange={(e) => setNlText(e.target.value)}
            />
            <div className="team-row">
              <button className="btn btn--small btn--primary" onClick={() => void generate()} disabled={nlBusy || !nlText.trim()}>
                <Wand2 size={13} /> {nlBusy ? t("team.nl.drafting") : t("team.nl.draft")}
              </button>
            </div>
            {draft && (
              <div className="team-draft">
                <div className="team-draft__head">
                  <span className="team-draft__avatar">{draft.avatar || "👤"}</span>
                  <strong>{draft.name}</strong>
                  {draft.role && <span className="team-draft__role">{draft.role}</span>}
                </div>
                {draft.skills.length > 0 && (
                  <div className="team-draft__tags">
                    {draft.skills.map((s) => <span key={s} className="team-chip team-chip--skill">{s}</span>)}
                  </div>
                )}
                {draft.systemPrompt && <pre className="team-draft__prompt">{draft.systemPrompt}</pre>}
                <div className="team-row">
                  <button className="btn btn--small btn--primary" onClick={() => void adoptDraft()} disabled={saving}>
                    {t("team.nl.confirm")}
                  </button>
                  <button className="btn btn--small" onClick={editDraft} disabled={saving}>{t("common.edit")}</button>
                  <button className="btn btn--small" onClick={() => setDraft(null)} disabled={saving}>{t("team.nl.discard")}</button>
                </div>
              </div>
            )}
          </div>
        ) : (
          <div className="cowork-taskform__body">
            <label className="cowork-taskform__label">
              <span className="cowork-taskform__labeltext">{t("team.member.name")}</span>
              <input className="cowork-taskform__input" value={form.name} placeholder={t("team.member.namePlaceholder")}
                onChange={(e) => set("name", e.target.value)} />
            </label>
            <label className="cowork-taskform__label">
              <span className="cowork-taskform__labeltext">{t("team.member.role")}</span>
              <input className="cowork-taskform__input" value={form.role} placeholder={t("team.member.rolePlaceholder")}
                onChange={(e) => set("role", e.target.value)} />
            </label>
            <label className="cowork-taskform__label">
              <span className="cowork-taskform__labeltext">{t("team.member.skills")}</span>
              <input className="cowork-taskform__input" value={skillsText} placeholder={t("team.member.skillsPlaceholder")}
                onChange={(e) => setSkillsText(e.target.value)} />
            </label>
            <div className="team-row">
              <label className="cowork-taskform__label team-row__half">
                <span className="cowork-taskform__labeltext">{t("team.member.model")}</span>
                <input className="cowork-taskform__input" value={form.model} onChange={(e) => set("model", e.target.value)} />
              </label>
              <label className="cowork-taskform__label team-row__half">
                <span className="cowork-taskform__labeltext">{t("team.member.effort")}</span>
                <input className="cowork-taskform__input" value={form.effort} onChange={(e) => set("effort", e.target.value)} />
              </label>
              <label className="cowork-taskform__label team-row__half">
                <span className="cowork-taskform__labeltext">{t("team.member.avatar")}</span>
                <input className="cowork-taskform__input" value={form.avatar} onChange={(e) => set("avatar", e.target.value)} />
              </label>
            </div>
            <label className="cowork-taskform__label">
              <span className="cowork-taskform__labeltext">{t("team.member.systemPrompt")}</span>
              <textarea className="cowork-taskform__input team-textarea" rows={4} value={form.systemPrompt}
                onChange={(e) => set("systemPrompt", e.target.value)} />
            </label>
            <label className="team-check">
              <input type="checkbox" checked={form.isLeader} onChange={(e) => set("isLeader", e.target.checked)} />
              <span>{t("team.member.isLeader")}</span>
            </label>
          </div>
        )}

        <footer className="cowork-taskform__foot">
          <div className="cowork-taskform__foot-right">
            <button className="btn btn--small" onClick={onClose} disabled={saving}>{t("common.cancel")}</button>
            {!(tab === "nl" && isNew) && (
              <button className="btn btn--primary btn--small" onClick={() => void save(form, true)} disabled={saving || !form.name.trim()}>
                {t("common.save")}
              </button>
            )}
          </div>
        </footer>
      </div>
    </div>
  );
}

// --- task editor -------------------------------------------------------------

function TaskEditor({
  team, initial, onClose, onSaved,
}: {
  team: TeamProjectView;
  initial: TeamTaskView;
  onClose: () => void;
  onSaved: (updated: TeamProjectView) => void;
}) {
  const t = useT();
  const { showToast } = useToast();
  const isNew = !initial.id;
  const [form, setForm] = useState<TeamTaskView>({ ...emptyTask(), ...initial });
  const [skillsText, setSkillsText] = useState((initial.requiredSkills ?? []).join(", "));
  const [acceptText, setAcceptText] = useState((initial.acceptance ?? []).map((c) => c.text).join("\n"));
  const [suggestions, setSuggestions] = useState<import("../../lib/types").TeamCandidateView[] | null>(null);
  const [saving, setSaving] = useState(false);

  const set = <K extends keyof TeamTaskView>(k: K, v: TeamTaskView[K]) => setForm((f) => ({ ...f, [k]: v }));

  const suggest = async () => {
    if (!form.id) { showToast(t("team.suggestHint"), "info"); return; }
    try {
      setSuggestions(await app.SuggestTeamAssignee(team.id, form.id));
    } catch (e) {
      showToast(String(e), "error");
    }
  };

  const save = async () => {
    setSaving(true);
    try {
      const model: TeamTaskView = {
        ...form,
        title: form.title.trim(),
        requiredSkills: skillsText.split(/[,，]/).map((s) => s.trim()).filter(Boolean),
        acceptance: acceptText.split("\n").map((s) => s.trim()).filter(Boolean).map((text) => ({ text, done: false })),
      };
      if (!model.title) { showToast(t("team.task.title"), "warn"); setSaving(false); return; }
      const updated = isNew ? await app.AddTeamTask(team.id, model) : await app.UpdateTeamTask(team.id, model);
      onSaved(updated);
    } catch (e) {
      showToast(String(e), "error");
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="cowork-taskform-overlay" onClick={onClose}>
      <div className="cowork-taskform" onClick={(e) => e.stopPropagation()}>
        <header className="cowork-taskform__head">
          <span className="team__title">{isNew ? t("team.addTask") : t("team.editTask")}</span>
          <button className="cowork-task-card__btn" onClick={onClose}><X size={16} /></button>
        </header>
        <div className="cowork-taskform__body">
          <label className="cowork-taskform__label">
            <span className="cowork-taskform__labeltext">{t("team.task.title")}</span>
            <input className="cowork-taskform__input" value={form.title} placeholder={t("team.task.titlePlaceholder")}
              onChange={(e) => set("title", e.target.value)} />
          </label>
          <label className="cowork-taskform__label">
            <span className="cowork-taskform__labeltext">{t("team.task.desc")}</span>
            <textarea className="cowork-taskform__input team-textarea" rows={3} value={form.desc}
              onChange={(e) => set("desc", e.target.value)} />
          </label>
          <label className="cowork-taskform__label">
            <span className="cowork-taskform__labeltext">{t("team.task.assignee")}</span>
            <select className="cowork-taskform__input" value={form.assigneeId}
              onChange={(e) => set("assigneeId", e.target.value)}>
              <option value="">{t("team.task.unassigned")}</option>
              {team.members.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}
            </select>
          </label>
          <label className="cowork-taskform__label">
            <span className="cowork-taskform__labeltext">{t("team.task.requiredSkills")}</span>
            <input className="cowork-taskform__input" value={skillsText} placeholder={t("team.task.requiredSkillsPlaceholder")}
              onChange={(e) => setSkillsText(e.target.value)} />
          </label>
          <label className="cowork-taskform__label">
            <span className="cowork-taskform__labeltext">{t("team.task.acceptance")}</span>
            <textarea className="cowork-taskform__input team-textarea" rows={3} value={acceptText}
              placeholder={t("team.task.acceptancePlaceholder")} onChange={(e) => setAcceptText(e.target.value)} />
          </label>
          <label className="cowork-taskform__label">
            <span className="cowork-taskform__labeltext">{t("team.task.deliverable")}</span>
            <textarea className="cowork-taskform__input team-textarea" rows={2} value={form.deliverable}
              onChange={(e) => set("deliverable", e.target.value)} />
          </label>

          {!isNew && (
            <div className="team-suggest">
              <button className="btn btn--small" onClick={() => void suggest()}>
                <Sparkles size={12} /> {t("team.suggest")}
              </button>
              <span className="team-hint">{t("team.suggestHint")}</span>
              {suggestions && (
                <ul className="team-suggest__list">
                  {suggestions.map((c) => (
                    <li key={c.memberId}>
                      <button className="team-suggest__pick" onClick={() => set("assigneeId", c.memberId)}>
                        <span>{c.name}{c.isLeader ? ` · ${t("team.leaderBadge")}` : ""}</span>
                        <span className="team-suggest__stat">
                          {c.score > 0 ? c.matched.join(", ") : "—"} · {t("team.load", { n: c.load })}
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </div>
        <footer className="cowork-taskform__foot">
          <div className="cowork-taskform__foot-right">
            <button className="btn btn--small" onClick={onClose} disabled={saving}>{t("common.cancel")}</button>
            <button className="btn btn--primary btn--small" onClick={() => void save()} disabled={saving || !form.title.trim()}>
              {t("team.task.save")}
            </button>
          </div>
        </footer>
      </div>
    </div>
  );
}

// --- blackboard --------------------------------------------------------------

function BlackboardModal({
  team, onClose, onUpdate,
}: {
  team: TeamProjectView;
  onClose: () => void;
  onUpdate: (updated: TeamProjectView) => void;
}) {
  const t = useT();
  const { showToast } = useToast();
  const [goal, setGoal] = useState(team.context?.goal ?? team.goal);
  const [constraints, setConstraints] = useState(team.context?.constraints ?? "");
  const [decisions, setDecisions] = useState(team.context?.decisions ?? []);
  const [openQuestions, setOpenQuestions] = useState(team.context?.openQuestions ?? []);
  const [dText, setDText] = useState("");
  const [qText, setQText] = useState("");
  const [digest, setDigest] = useState("");
  const [saving, setSaving] = useState(false);

  const preview = async () => {
    try {
      setDigest(await app.TeamBlackboardDigest(team.id, 1200));
    } catch (e) {
      showToast(String(e), "error");
    }
  };

  const save = async () => {
    setSaving(true);
    try {
      const updated = await app.SetTeamContext(team.id, {
        goal,
        constraints,
        decisions,
        artifacts: team.context?.artifacts ?? [],
        openQuestions,
        version: team.context?.version ?? 0,
      });
      onUpdate(updated);
      showToast(t("team.saved"), "info");
      onClose();
    } catch (e) {
      showToast(String(e), "error");
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="cowork-taskform-overlay" onClick={onClose}>
      <div className="cowork-taskform team-modal--wide" onClick={(e) => e.stopPropagation()}>
        <header className="cowork-taskform__head">
          <span className="team__title">{t("team.blackboard")}</span>
          <button className="cowork-task-card__btn" onClick={onClose}><X size={16} /></button>
        </header>
        <div className="cowork-taskform__body">
          <p className="team-hint">{t("team.blackboard.hint")}</p>
          <label className="cowork-taskform__label">
            <span className="cowork-taskform__labeltext">{t("team.goal")}</span>
            <textarea className="cowork-taskform__input team-textarea" rows={2} value={goal} onChange={(e) => setGoal(e.target.value)} />
          </label>
          <label className="cowork-taskform__label">
            <span className="cowork-taskform__labeltext">{t("team.constraints")}</span>
            <textarea className="cowork-taskform__input team-textarea" rows={2} value={constraints} onChange={(e) => setConstraints(e.target.value)} />
          </label>

          <div className="team-list">
            <span className="cowork-taskform__labeltext">{t("team.decisions")}</span>
            <ul>
              {decisions.map((d) => (
                <li key={d.id}>
                  <span>{d.text}</span>
                  <button className="team-card__btn team-card__btn--danger"
                    onClick={() => setDecisions((xs) => xs.filter((x) => x.id !== d.id))}><X size={11} /></button>
                </li>
              ))}
            </ul>
            <InlineAdd value={dText} onChange={setDText} onAdd={() => {
              if (!dText.trim()) return;
              setDecisions((xs) => [...xs, { id: `d_${Date.now()}`, text: dText.trim(), by: "", at: new Date().toISOString() }]);
              setDText("");
            }} placeholder={t("team.itemPlaceholder")} addLabel={t("team.addItem")} />
          </div>

          <div className="team-list">
            <span className="cowork-taskform__labeltext">{t("team.openQuestions")}</span>
            <ul>
              {openQuestions.map((q, i) => (
                <li key={`${q}-${i}`}>
                  <span>{q}</span>
                  <button className="team-card__btn team-card__btn--danger"
                    onClick={() => setOpenQuestions((xs) => xs.filter((_, j) => j !== i))}><X size={11} /></button>
                </li>
              ))}
            </ul>
            <InlineAdd value={qText} onChange={setQText} onAdd={() => {
              if (!qText.trim()) return;
              setOpenQuestions((xs) => [...xs, qText.trim()]);
              setQText("");
            }} placeholder={t("team.itemPlaceholder")} addLabel={t("team.addItem")} />
          </div>

          <div className="team-list">
            <button className="btn btn--small" onClick={() => void preview()}>{t("team.digest")}</button>
            {digest && <pre className="team-draft__prompt">{digest}</pre>}
          </div>
        </div>
        <footer className="cowork-taskform__foot">
          <div className="cowork-taskform__foot-right">
            <button className="btn btn--small" onClick={onClose} disabled={saving}>{t("common.cancel")}</button>
            <button className="btn btn--primary btn--small" onClick={() => void save()} disabled={saving}>{t("common.save")}</button>
          </div>
        </footer>
      </div>
    </div>
  );
}

function InlineAdd({
  value, onChange, onAdd, placeholder, addLabel,
}: {
  value: string;
  onChange: (v: string) => void;
  onAdd: () => void;
  placeholder: string;
  addLabel: string;
}) {
  return (
    <div className="team-row">
      <input
        className="cowork-taskform__input"
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); onAdd(); } }}
      />
      <button className="btn btn--small" onClick={onAdd}>{addLabel}</button>
    </div>
  );
}

// --- leader planning ---------------------------------------------------------

// PlanModal runs the 团长's WBS step: decompose the goal into cards and route
// each one to the best-skilled member. It is an explicit user action (the board
// is not auto-planned), so a mis-aimed goal costs one click to redo.
function PlanModal({
  team, onClose, onPlanned,
}: {
  team: TeamProjectView;
  onClose: () => void;
  onPlanned: (updated: TeamProjectView) => void;
}) {
  const t = useT();
  const { showToast } = useToast();
  const [goal, setGoal] = useState(team.context?.goal || team.goal || "");
  const [busy, setBusy] = useState(false);

  const run = async () => {
    setBusy(true);
    try {
      onPlanned(await app.PlanTeamTasks(team.id, goal.trim(), ""));
    } catch (e) {
      showToast(t("team.planFailed", { msg: String(e) }), "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="cowork-taskform-overlay" onClick={onClose}>
      <div className="cowork-taskform" onClick={(e) => e.stopPropagation()}>
        <header className="cowork-taskform__head">
          <span className="team__title">{t("team.plan")}</span>
          <button className="cowork-task-card__btn" onClick={onClose}><X size={16} /></button>
        </header>
        <div className="cowork-taskform__body">
          <p className="team-hint">{t("team.planHint")}</p>
          <label className="cowork-taskform__label">
            <span className="cowork-taskform__labeltext">{t("team.goal")}</span>
            <textarea
              className="cowork-taskform__input team-textarea"
              rows={3}
              value={goal}
              placeholder={t("team.planGoalPlaceholder")}
              onChange={(e) => setGoal(e.target.value)}
            />
          </label>
          <ul className="team-suggest__list">
            {team.members.map((m) => (
              <li key={m.id} className="team-suggest__stat">
                {m.avatar || "👤"} {m.name} · {m.skills.length > 0 ? m.skills.join(", ") : "—"}
              </li>
            ))}
          </ul>
        </div>
        <footer className="cowork-taskform__foot">
          <div className="cowork-taskform__foot-right">
            <button className="btn btn--small" onClick={onClose} disabled={busy}>{t("common.cancel")}</button>
            <button className="btn btn--primary btn--small" onClick={() => void run()} disabled={busy}>
              {busy ? t("team.planning") : t("team.planRun")}
            </button>
          </div>
        </footer>
      </div>
    </div>
  );
}

// --- new team ----------------------------------------------------------------

// NewTeamModal offers two paths: describe the team in natural language (the
// model drafts a 团长 + 团员 roster), or fill the name/goal by hand.
function NewTeamModal({
  onClose, onCreated,
}: {
  onClose: () => void;
  onCreated: (created: TeamProjectView) => void;
}) {
  const t = useT();
  const { showToast } = useToast();
  const [mode, setMode] = useState<"nl" | "manual">("nl");
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [name, setName] = useState("");
  const [goal, setGoal] = useState("");
  const [draft, setDraft] = useState<TeamProjectView | null>(null);
  const [adopting, setAdopting] = useState(false);
  const inputRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => { inputRef.current?.focus(); }, []);

  const generate = async () => {
    if (!text.trim()) return;
    setBusy(true);
    try {
      const item: TeamDraftInput = { teamId: "", instruction: text, model: "", adopt: false };
      setDraft(await app.DraftTeamProject(item));
    } catch (e) {
      showToast(t("team.nl.failed", { msg: String(e) }), "error");
    } finally {
      setBusy(false);
    }
  };

  const adopt = async () => {
    if (!draft) return;
    setAdopting(true);
    try {
      const item: TeamDraftInput = { teamId: "", instruction: text, model: "", adopt: true };
      onCreated(await app.DraftTeamProject(item));
    } catch (e) {
      showToast(t("team.nl.failed", { msg: String(e) }), "error");
    } finally {
      setAdopting(false);
    }
  };

  const createManual = async () => {
    setAdopting(true);
    try {
      const created = await app.CreateTeamProject({
        id: "", name: name.trim(), goal: goal.trim(), members: [], tasks: [],
        context: { goal: goal.trim(), constraints: "", decisions: [], artifacts: [], openQuestions: [], version: 1 },
        policy: { maxRounds: 12, maxParallel: 3, autoAssign: true, autoReplan: false },
      });
      onCreated(created);
    } catch (e) {
      showToast(String(e), "error");
    } finally {
      setAdopting(false);
    }
  };

  return (
    <div className="cowork-taskform-overlay" onClick={onClose}>
      <div className="cowork-taskform team-modal--wide" onClick={(e) => e.stopPropagation()}>
        <header className="cowork-taskform__head">
          <span className="team__title">{t("team.new")}</span>
          <button className="cowork-task-card__btn" onClick={onClose}><X size={16} /></button>
        </header>

        <div className="team-tabs">
          <button className={`team-tabs__btn${mode === "nl" ? " team-tabs__btn--active" : ""}`} onClick={() => setMode("nl")}>
            <Wand2 size={12} /> {t("team.newByNL")}
          </button>
          <button className={`team-tabs__btn${mode === "manual" ? " team-tabs__btn--active" : ""}`} onClick={() => setMode("manual")}>
            <Pencil size={12} /> {t("team.name")}
          </button>
        </div>

        <div className="cowork-taskform__body">
          {mode === "nl" ? (
            <>
              <p className="team-hint">{t("team.nl.hint")}</p>
              <textarea
                ref={inputRef}
                className="cowork-taskform__input team-textarea"
                rows={3}
                value={text}
                placeholder={t("team.newTeamPlaceholder")}
                onChange={(e) => setText(e.target.value)}
              />
              <div className="team-row">
                <button className="btn btn--small btn--primary" onClick={() => void generate()} disabled={busy || !text.trim()}>
                  <Wand2 size={13} /> {busy ? t("team.nl.drafting") : t("team.nl.draft")}
                </button>
              </div>
              {draft && (
                <div className="team-draft">
                  <div className="team-draft__head">
                    <strong>{draft.name}</strong>
                    {draft.goal && <span className="team-draft__role">{draft.goal}</span>}
                  </div>
                  <ul className="team-members">
                    {draft.members.map((m) => (
                      <li key={m.id || m.name} className={`team-member${m.isLeader ? " team-member--leader" : ""}`}>
                        <span className="team-member__avatar">{m.avatar || (m.isLeader ? "🧭" : "👤")}</span>
                        <div className="team-member__meta">
                          <div className="team-member__name">
                            {m.name}
                            {m.isLeader && <span className="team-chip team-chip--leader"><Crown size={10} /> {t("team.leaderBadge")}</span>}
                          </div>
                          {m.role && <div className="team-member__role">{m.role}</div>}
                          {m.skills.length > 0 && (
                            <div className="team-member__skills">
                              {m.skills.map((s) => <span key={s} className="team-chip team-chip--skill">{s}</span>)}
                            </div>
                          )}
                        </div>
                      </li>
                    ))}
                  </ul>
                  <div className="team-row">
                    <button className="btn btn--small btn--primary" onClick={() => void adopt()} disabled={adopting}>
                      {t("team.nl.confirm")}
                    </button>
                    <button className="btn btn--small" onClick={() => setDraft(null)} disabled={adopting}>{t("team.nl.discard")}</button>
                  </div>
                </div>
              )}
            </>
          ) : (
            <>
              <label className="cowork-taskform__label">
                <span className="cowork-taskform__labeltext">{t("team.name")}</span>
                <input className="cowork-taskform__input" value={name} onChange={(e) => setName(e.target.value)} />
              </label>
              <label className="cowork-taskform__label">
                <span className="cowork-taskform__labeltext">{t("team.goal")}</span>
                <textarea className="cowork-taskform__input team-textarea" rows={3} value={goal} onChange={(e) => setGoal(e.target.value)} />
              </label>
            </>
          )}
        </div>

        <footer className="cowork-taskform__foot">
          <div className="cowork-taskform__foot-right">
            <button className="btn btn--small" onClick={onClose} disabled={adopting}>{t("common.cancel")}</button>
            {mode === "manual" && (
              <button className="btn btn--primary btn--small" onClick={() => void createManual()} disabled={adopting || !name.trim()}>
                {t("common.save")}
              </button>
            )}
          </div>
        </footer>
      </div>
    </div>
  );
}
