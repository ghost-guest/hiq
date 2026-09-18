import { Calendar, ChevronDown, ChevronRight, FileText, Search, Trash2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { app } from "../lib/bridge";
import { useT } from "../lib/i18n";
import type { Translator } from "../lib/i18n";
import type {
  DreamRunView,
  DreamStatusView,
  MemoryFact,
  MemoryMigrationReport,
  MemoryPromotionInput,
  MemoryPromotionResult,
  MemorySettings,
  MemorySettingsInput,
  MemoryView,
  ProviderView,
} from "../lib/types";
import { ResizableDrawer } from "./ResizableDrawer";
import { Tooltip } from "./Tooltip";
import { ModalCloseButton } from "./ModalCloseButton";

type LinkInfo = {
  name: string;
  exists: boolean;
};

// displayTitle returns the label for a fact card. v0.4: title/description are
// gone from the store, so prefer the body's first non-empty line (matching the
// MEMORY.md index label the backend derives), then fall back to a de-kebabed
// name.
function displayTitle(fact: MemoryFact): string {
  if (fact.body) {
    for (const ln of fact.body.split(/\r?\n/)) {
      const t = ln.trim();
      if (t) return t;
    }
  }
  return fact.title || fact.name.replaceAll("-", " ");
}

// memoryTypeLabel maps the raw English type (user/feedback/project/reference)
// to a localized label so the filter buttons and fact cards don't show raw
// enum values to end users.
function memoryTypeLabel(type: string, t: Translator): string {
  switch (type) {
    case "user": return t("memory.typeUser");
    case "feedback": return t("memory.typeFeedback");
    case "project": return t("memory.typeProject");
    case "reference": return t("memory.typeReference");
    default: return t("memory.typeOther");
  }
}

// memoryLevelLabel maps a fact's level to a localized label. An absent level
// means L1: memory.LevelOf infers one for records written before the hierarchy
// existed (legacy `project: true` → L2, everything else → L1), so the panel
// shows the same answer the injected index does.
function memoryLevelLabel(level: string | undefined, t: Translator): string {
  switch ((level || "l1").toLowerCase()) {
    case "l2": return t("memory.levelL2");
    case "l3": return t("memory.levelL3");
    default: return t("memory.levelL1");
  }
}

// memoryLevelShort is the compact chip form ("L1"/"L2"/"L3") shown on a fact row,
// where the full label would crowd out the title.
function memoryLevelShort(level: string | undefined): string {
  switch ((level || "l1").toLowerCase()) {
    case "l2": return "L2";
    case "l3": return "L3";
    default: return "L1";
  }
}

// MemoryLevelFilter is the L1/L2/L3 scope filter shared by the drawer and the
// settings page. Each chip carries its count, so a level with no facts reads as
// empty instead of just filtering everything away.
function MemoryLevelFilter({
  counts,
  value,
  onChange,
}: {
  counts: Record<string, number>;
  value: string;
  onChange: (v: string) => void;
}) {
  const t = useT();
  const chips: Array<[string, string]> = [
    ["all", t("memory.levelAll")],
    ["l1", t("memory.levelL1")],
    ["l2", t("memory.levelL2")],
    ["l3", t("memory.levelL3")],
  ];
  return (
    <div className="mem-filter mem-levels" role="tablist" aria-label={t("memory.levelFilter")}>
      {chips.map(([id, label]) => (
        <button
          key={id}
          className={"mem-filter__item" + (value === id ? " mem-filter__item--on" : "")}
          onClick={() => onChange(id)}
          type="button"
          title={id === "all" ? t("memory.levelAllHint") : memoryLevelHint(id, t)}
        >
          {label}
          {id !== "all" && counts[id] ? <small className="mem-filter__n">{counts[id]}</small> : null}
        </button>
      ))}
    </div>
  );
}

// memoryLevelHint explains what a level is for, shown as the chip's tooltip.
function memoryLevelHint(level: string, t: Translator): string {
  switch (level) {
    case "l1": return t("memory.levelL1Hint");
    case "l2": return t("memory.levelL2Hint");
    case "l3": return t("memory.levelL3Hint");
    default: return "";
  }
}

// memoryFactHaystack is the searchable text of a fact: title, slug, type, body
// and the v0.5 metadata (level, tags) — so "l2" or a tag finds the fact the way
// the model's `recall` tool would.
function memoryFactHaystack(fact: MemoryFact): string {
  return [displayTitle(fact), fact.name, fact.description, fact.type, fact.body, memoryLevelShort(fact.level), (fact.tags ?? []).join(" ")]
    .join(" ")
    .toLowerCase();
}

// memoryFactTags returns the fact's tags, tolerating older payloads without them.
function memoryFactTags(fact: MemoryFact): string[] {
  return Array.isArray(fact.tags) ? fact.tags : [];
}

function uniqueLinks(body: string, names: Set<string>): LinkInfo[] {
  const links: LinkInfo[] = [];
  const seen = new Set<string>();
  const re = /\[\[([^\]]+)\]\]/g;
  let match: RegExpExecArray | null;
  while ((match = re.exec(body)) !== null) {
    const name = match[1].trim();
    if (!name || seen.has(name)) continue;
    seen.add(name);
    links.push({ name, exists: names.has(name) });
  }
  return links;
}

function memoryScopeLabel(scope: string, t: ReturnType<typeof useT>): string {
  switch (scope) {
    case "project":
      return t("memory.scope.project");
    case "user":
      return t("memory.scope.user");
    case "local":
      return t("memory.scope.local");
    case "ancestor":
      return t("memory.scope.ancestor");
    default:
      return scope;
  }
}

function memoryDocTitle(scope: string, t: ReturnType<typeof useT>): string {
  switch (scope) {
    case "project":
      return t("memory.doc.projectTitle");
    case "user":
      return t("memory.doc.userTitle");
    case "local":
      return t("memory.doc.localTitle");
    case "ancestor":
      return t("memory.doc.ancestorTitle");
    default:
      return t("memory.doc.customTitle");
  }
}

function memoryDocHint(scope: string, t: ReturnType<typeof useT>): string {
  switch (scope) {
    case "project":
      return t("memory.doc.projectHint");
    case "user":
      return t("memory.doc.userHint");
    case "local":
      return t("memory.doc.localHint");
    case "ancestor":
      return t("memory.doc.ancestorHint");
    default:
      return t("memory.doc.customHint");
  }
}

function memoryDocPreview(body: string): string {
  const lines = body.split(/\r?\n/);
  const preview = lines.slice(0, 6).join("\n");
  return lines.length > 6 ? `${preview}\n...` : preview;
}

function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message;
  return String(err || "Unknown error");
}

// --- timeline helpers (bitemporal surface, v0.3.0) ---

// dateFromISO parses a YYYY-MM-DD or RFC3339 string into a Date (or null). Used
// for both validFrom/validTo (date-only) and createdAt/updatedAt (full ISO).
function dateFromISO(s?: string): Date | null {
  if (!s) return null;
  const d = new Date(s);
  return isNaN(d.getTime()) ? null : d;
}

// isExpired reports whether a fact's validity window has closed: a non-empty
// validTo in the past means the fact stopped being true on that date.
function isExpired(f: MemoryFact, now = Date.now()): boolean {
  if (!f.validTo) return false;
  const d = dateFromISO(f.validTo);
  return d !== null && d.getTime() < now;
}

// timelineGroupLabel buckets a fact by its validFrom date into a human-readable
// heading: "Today" / "Yesterday" / locale date, or the localized "No date" when
// the fact is timeless (no validFrom and no createdAt). Mirrors HistoryPanel's
// dayLabel but operates on a YYYY-MM-DD/ISO string rather than a timestamp.
function timelineGroupLabel(f: MemoryFact, t: Translator): string {
  const d = dateFromISO(f.validFrom) ?? dateFromISO(f.createdAt);
  if (!d) return t("memory.timelineUndated");
  const startOfDay = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round(
    (startOfDay(new Date()) - startOfDay(d)) / 86_400_000,
  );
  if (days <= 0) return t("history.today");
  if (days === 1) return t("history.yesterday");
  return d.toLocaleDateString();
}

// groupTimelineFacts buckets facts newest-first by their timeline label. Facts
// already arrive sorted newest-first from the backend (Store.ListTimeline), so
// this is a stable run-length grouping that preserves order within each bucket.
function groupTimelineFacts(
  facts: MemoryFact[],
  t: Translator,
): { label: string; items: MemoryFact[] }[] {
  const groups: { label: string; items: MemoryFact[] }[] = [];
  for (const f of facts) {
    const label = timelineGroupLabel(f, t);
    const last = groups[groups.length - 1];
    if (last && last.label === label) last.items.push(f);
    else groups.push({ label, items: [f] });
  }
  return groups;
}

// validAtPoint checks whether a fact was true on a given YYYY-MM-DD day, using
// the same inclusive [validFrom, validTo] semantics as the backend's timeFilter:
// a timeless record (no window) is always valid; validTo == day is in range.
function validAtPoint(f: MemoryFact, dayISO: string): boolean {
  const day = dateFromISO(dayISO);
  if (!day) return true;
  const from = dateFromISO(f.validFrom);
  const to = dateFromISO(f.validTo);
  if (from && day < from) return false;
  if (to && day > to) return false;
  return true;
}

// MemoryPanel is the desktop memory manager: a right-side drawer over the loaded
// hiq.md hierarchy and saved auto-memories. Unlike Claude Code's /memory
// (which shells out to $EDITOR) it edits docs in place, and unlike Codex (no UI
// at all) it shows the saved facts. Docs are editable; facts are read-only
// (the model owns them via the `remember` tool). Quick-add mirrors the "#"
// shortcut with an explicit scope selector.
export function MemoryPanel({
  view,
  onClose,
  onRemember,
  onForget,
  onSaveDoc,
}: {
  view: MemoryView | null;
  onClose: () => void;
  onRemember: (scope: string, note: string) => Promise<void> | void;
  onForget: (name: string) => Promise<void> | void;
  onSaveDoc: (path: string, body: string) => Promise<void> | void;
}) {
  const t = useT();
  const [note, setNote] = useState("");
  const [scope, setScope] = useState("");
  const [editingPath, setEditingPath] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);

  const [highlight, setHighlight] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [typeFilter, setTypeFilter] = useState("all");
  const [levelFilter, setLevelFilter] = useState("all");
  const [expanded, setExpanded] = useState<string | null>(null);
  const [confirmForget, setConfirmForget] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const factRefs = useRef<Record<string, HTMLElement | null>>({});

  // Filter input — a single substring search across docs and facts. The
  // substring is case-insensitive and matches anywhere in the body or the
  // path; an empty string shows everything. The filter is purely frontend
  // (no kernel round-trip) so it's instant and reversible.
  const [filter, setFilter] = useState("");

  const facts = view?.facts ?? [];
  const factNames = useMemo(() => new Set(facts.map((f) => f.name)), [facts]);
  const factTypes = useMemo(
    () => Array.from(new Set(facts.map((f) => f.type).filter(Boolean))).sort(),
    [facts],
  );
  const normalizedQuery = query.trim().toLowerCase();
  const normalizedFilter = filter.trim().toLowerCase();
  const levelCounts = useMemo(() => {
    const out: Record<string, number> = {};
    for (const f of facts) {
      const l = memoryLevelShort(f.level).toLowerCase();
      out[l] = (out[l] ?? 0) + 1;
    }
    return out;
  }, [facts]);
  const filteredFacts = useMemo(
    () =>
      facts.filter((f) => {
        if (typeFilter !== "all" && f.type !== typeFilter) return false;
        if (levelFilter !== "all" && memoryLevelShort(f.level).toLowerCase() !== levelFilter) return false;
        if (normalizedFilter) {
          const hay = [f.name, f.description, f.body].join(" ").toLowerCase();
          if (!hay.includes(normalizedFilter)) return false;
        }
        if (!normalizedQuery) return true;
        return memoryFactHaystack(f).includes(normalizedQuery);
      }),
    [facts, normalizedQuery, normalizedFilter, typeFilter, levelFilter],
  );

  const scrollToFact = (name: string) => {
    const el = factRefs.current[name];
    if (!el) return;
    el.scrollIntoView({ block: "center", behavior: "auto" });
    setHighlight(name);
    window.setTimeout(() => setHighlight((h) => (h === name ? null : h)), 1200);
  };

  // Clear active filters when the target is hidden, else the [[link]] is a silent no-op.
  const jumpTo = (name: string) => {
    if (!factNames.has(name)) return;
    const visible = filteredFacts.some((f) => f.name === name);
    setExpanded(name);
    setConfirmForget(null);
    if (!visible) {
      setQuery("");
      setTypeFilter("all");
      setLevelFilter("all");
      window.setTimeout(() => scrollToFact(name), 0);
      return;
    }
    scrollToFact(name);
  };

  // renderWithLinks turns [[name]] tokens into in-panel jumps; a token with no
  // matching saved memory renders as a flagged dead link.
  const renderWithLinks = (text: string): ReactNode[] => {
    const out: ReactNode[] = [];
    const re = /\[\[([^\]]+)\]\]/g;
    let last = 0;
    let k = 0;
    let m: RegExpExecArray | null;
    while ((m = re.exec(text)) !== null) {
      if (m.index > last) out.push(text.slice(last, m.index));
      const target = m[1].trim();
      out.push(
        factNames.has(target) ? (
          <button key={k++} type="button" className="mem-link" onClick={() => jumpTo(target)}>
            {target}
          </button>
        ) : (
          <Tooltip key={k++} label={t("memory.deadLink", { name: target })}>
            <span className="mem-link mem-link--dead">{target}</span>
          </Tooltip>
        ),
      );
      last = re.lastIndex;
    }
    if (last < text.length) out.push(text.slice(last));
    return out;
  };

  const forgetFact = async (name: string) => {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      await onForget(name);
      if (expanded === name) setExpanded(null);
      setConfirmForget(null);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const filteredDocs = useMemo(() => {
    if (!view) return [];
    const q = filter.trim().toLowerCase();
    if (!q) return view.docs;
    return view.docs.filter((d) => d.body.toLowerCase().includes(q) || d.path.toLowerCase().includes(q));
  }, [view, filter]);

  const scopes = view?.scopes ?? [];
  // Default the scope selector to "project" when present, else the first option.
  const activeScope =
    scope || scopes.find((s) => s.scope === "project")?.scope || scopes[0]?.scope || "project";

  const submitNote = async () => {
    const trimmed = note.trim();
    if (!trimmed || busy) return;
    setBusy(true);
    setError(null);
    try {
      await onRemember(activeScope, trimmed);
      setNote("");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const startEdit = (path: string, body: string) => {
    setEditingPath(path);
    setDraft(body);
  };

  const saveEdit = async () => {
    if (editingPath === null || busy) return;
    setBusy(true);
    setError(null);
    try {
      await onSaveDoc(editingPath, draft);
      setEditingPath(null);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <ResizableDrawer onClose={onClose}>
        <header className="drawer__head">
          <div>
            <div className="drawer__title">{t("memory.title")}</div>
            {view?.available && (
              <div className="drawer__summary">
                {t("memory.summary", { facts: facts.length, docs: view.docs.length })}
              </div>
            )}
          </div>
          <ModalCloseButton label={t("common.close")} onClick={onClose} />
        </header>

        {!view?.available ? (
          <div className="empty">{t("memory.unavailable")}</div>
        ) : (
          <div className="drawer__body">
            {/* Saved auto-memories — the model owns these via remember/forget;
                the panel can delete one and follow [[name]] cross-links. */}
            <section className="mem-section">
              <div className="mem-section__row">
                <div>
                  <div className="mem-section__title">{t("memory.savedMemories")}</div>
                  <div className="mem-note">{t("memory.fallibleNote")}</div>
                </div>
                <span className="mem-count">{facts.length}</span>
              </div>
              <div className="mem-toolbar">
                <label className="mem-search">
                  <Search size={14} />
                  <input
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    placeholder={t("memory.searchPlaceholder")}
                  />
                </label>
                <div className="mem-filter" role="tablist" aria-label={t("memory.typeFilter")}>
                  <button
                    className={`mem-filter__item${typeFilter === "all" ? " mem-filter__item--on" : ""}`}
                    onClick={() => { setTypeFilter("all"); setLevelFilter("all"); }}
                    type="button"
                  >
                    {t("memory.allTypes")}
                  </button>
                  {factTypes.map((type) => (
                    <button
                      className={`mem-filter__item${typeFilter === type ? " mem-filter__item--on" : ""}`}
                      onClick={() => setTypeFilter(type)}
                      type="button"
                      key={type}
                    >
                      {memoryTypeLabel(type, t)}
                    </button>
                  ))}
                </div>
                <MemoryLevelFilter counts={levelCounts} value={levelFilter} onChange={setLevelFilter} />
              </div>
              {error && <div className="mem-error" role="alert">{error}</div>}
              {facts.length === 0 ? (
                <div className="mem-empty">{t("memory.noFacts")}</div>
              ) : filteredFacts.length === 0 ? (
                <div className="mem-empty">
                  {t("memory.noMatches")}
                  <button
                    className="mem-empty__action"
                    onClick={() => {
                      setQuery("");
                      setTypeFilter("all");
                      setLevelFilter("all");
                    }}
                    type="button"
                  >
                    {t("memory.clearFilters")}
                  </button>
                </div>
              ) : (
                <div className="mem-facts">
                  {filteredFacts.map((f) => {
                    const isOpen = expanded === f.name;
                    const links = uniqueLinks(f.body, factNames);
                    const missing = links.filter((link) => !link.exists);
                    return (
                      <article
                        className={`mem-fact${highlight === f.name ? " mem-fact--hl" : ""}`}
                        data-mem-type={f.type || "other"}
                        key={f.name}
                        ref={(el) => {
                          factRefs.current[f.name] = el;
                        }}
                      >
                        <button
                          className="mem-fact__summary"
                          onClick={() => {
                            setExpanded(isOpen ? null : f.name);
                            setConfirmForget(null);
                          }}
                          type="button"
                        >
                          {isOpen ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
                          <span className="mem-fact__main">
                            <span className="mem-fact__title">{displayTitle(f)}</span>
                            <span className="mem-fact__meta">
                              <span
                                className="mem-fact__level"
                                data-mem-level={memoryLevelShort(f.level).toLowerCase()}
                                title={memoryLevelLabel(f.level, t)}
                              >
                                {memoryLevelShort(f.level)}
                              </span>
                              {f.type && <span className="mem-fact__type" data-mem-type={f.type}>{memoryTypeLabel(f.type, t)}</span>}
                              <span className="mem-fact__slug">{f.name}</span>
                              {memoryFactTags(f).map((tag) => (
                                <span
                                  key={tag}
                                  className="mem-tag"
                                  role="button"
                                  tabIndex={-1}
                                  title={t("memory.filterByTag", { tag })}
                                  onClick={(e) => {
                                    e.stopPropagation();
                                    setQuery(tag);
                                  }}
                                >
                                  #{tag}
                                </span>
                              ))}
                            </span>
                            <span className="mem-fact__desc">{f.description}</span>
                          </span>
                        </button>
                        {links.length > 0 && (
                          <div className="mem-fact__links" aria-label={t("memory.links")}>
                            {links.map((link) =>
                              link.exists ? (
                                <button
                                  className="mem-link-chip"
                                  key={link.name}
                                  onClick={() => jumpTo(link.name)}
                                  type="button"
                                >
                                  [[{link.name}]]
                                </button>
                              ) : (
                                <Tooltip key={link.name} label={t("memory.deadLink", { name: link.name })}>
                                  <span className="mem-link-chip mem-link-chip--dead">[[{link.name}]]</span>
                                </Tooltip>
                              ),
                            )}
                          </div>
                        )}
                        {isOpen && (
                          <div className="mem-fact__detail">
                            {f.body ? (
                              <div className="mem-fact__body">{renderWithLinks(f.body)}</div>
                            ) : (
                              <div className="mem-empty">{t("memory.noBody")}</div>
                            )}
                            {missing.length > 0 && (
                              <div className="mem-deadline">
                                {t("memory.missingLinks", { n: missing.length })}
                              </div>
                            )}
                            <div className="mem-fact__actions">
                              <span className="mem-hint mem-hint--inline">
                                {t("memory.appliesNow")}
                              </span>
                              {confirmForget === f.name ? (
                                <div className="mem-confirm">
                                  <button
                                    className="btn btn--small"
                                    onClick={() => setConfirmForget(null)}
                                    disabled={busy}
                                    type="button"
                                  >
                                    {t("common.cancel")}
                                  </button>
                                  <button
                                    className="btn btn--small mem-danger"
                                    onClick={() => void forgetFact(f.name)}
                                    disabled={busy}
                                    type="button"
                                  >
                                    {t("memory.confirmForget")}
                                  </button>
                                </div>
                              ) : (
                                <button
                                  className="btn btn--small mem-fact__forget"
                                  onClick={() => setConfirmForget(f.name)}
                                  disabled={busy}
                                  type="button"
                                >
                                  <Trash2 size={13} />
                                  {t("memory.forget")}
                                </button>
                              )}
                            </div>
                          </div>
                        )}
                      </article>
                    );
                  })}
                </div>
              )}
              {view.storeDir && (
                <div className="mem-hint">{t("memory.storedUnder", { dir: view.storeDir })}</div>
              )}
            </section>

            {/* Quick-add: scope selector + note, mirroring the "#" shortcut. */}
            <section className="mem-section">
              <div className="mem-section__title">{t("memory.quickAdd")}</div>
              <div className="mem-add">
                <Tooltip label={t("memory.whereToSave")}>
                  <select
                    className="mem-select"
                    value={activeScope}
                    onChange={(e) => setScope(e.target.value)}
                  >
                    {scopes.map((s) => (
                      <option key={s.scope} value={s.scope}>
                        {s.scope}
                      </option>
                    ))}
                  </select>
                </Tooltip>
                <input
                  className="mem-input"
                  placeholder={t("memory.notePlaceholder")}
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") void submitNote();
                  }}
                />
                <button
                  className="btn btn--primary btn--small"
                  onClick={() => void submitNote()}
                  disabled={busy || !note.trim()}
                >
                  {t("memory.remember")}
                </button>
              </div>
              <div className="mem-hint">
                {scopes.find((s) => s.scope === activeScope)?.path}
              </div>
            </section>

            {/* Doc files — editable in place. */}
            <section className="mem-section">
              <div className="mem-section__title">{t("memory.instructionFiles")}</div>
              <input
                className="mem-input mem-filter"
                placeholder={t("memory.filterPlaceholder")}
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                spellCheck={false}
                aria-label={t("memory.filterPlaceholder")}
              />
              {filteredDocs.length === 0 && (
                <div className="mem-empty">{filter ? t("memory.noFilterMatch") : t("memory.noDocs")}</div>
              )}
              {filteredDocs.map((d) => {
                const editing = editingPath === d.path;
                return (
                  <div className="mem-doc" key={d.path}>
                    <div className="mem-doc__head">
                      <span className="mem-doc__icon"><FileText size={15} /></span>
                      <span className="mem-doc__info">
                        <span className="mem-doc__name">{memoryDocTitle(d.scope, t)}</span>
                        <span className="mem-doc__path">{d.path}</span>
                      </span>
                      <span className={`mem-doc__tag badge--${d.scope}`}>{memoryScopeLabel(d.scope, t)}</span>
                      {!editing && (
                        <button
                          className="btn btn--small"
                          onClick={() => startEdit(d.path, d.body)}
                        >
                          {t("common.edit")}
                        </button>
                      )}
                    </div>
                    {editing ? (
                      <div className="mem-doc__edit">
                        <textarea
                          className="mem-textarea"
                          value={draft}
                          onChange={(e) => setDraft(e.target.value)}
                          spellCheck={false}
                        />
                        <div className="mem-doc__actions">
                          <button
                            className="btn btn--small"
                            onClick={() => setEditingPath(null)}
                            disabled={busy}
                          >
                            {t("common.cancel")}
                          </button>
                          <button
                            className="btn btn--primary btn--small"
                            onClick={() => void saveEdit()}
                            disabled={busy}
                          >
                            {t("common.save")}
                          </button>
                        </div>
                      </div>
                    ) : (
                      <pre className="mem-doc__body">{d.body}</pre>
                    )}
                  </div>
                );
              })}
            </section>



            {/* Saved auto-memories — read-only; the model owns these. */}
            <section className="mem-section">
              <div className="mem-section__title">{t("memory.savedMemories")}</div>
              {filteredFacts.length === 0 ? (
                <div className="mem-empty">{filter ? t("memory.noFilterMatch") : t("memory.noFacts")}</div>
              ) : (
                filteredFacts.map((f) => (
                  <div className="mem-fact" key={f.name} title={f.body}>
                    <span className={`badge badge--${f.type}`}>{f.type}</span>
                    <div className="mem-fact__text">
                      <div className="mem-fact__name">{f.name}</div>
                      <div className="mem-fact__desc">{f.description}</div>
                    </div>
                  </div>
                ))
              )}
              {view.storeDir && (
                <div className="mem-hint" title={view.storeDir}>
                  {t("memory.storedUnder", { dir: view.storeDir })}
                </div>
              )}
            </section>
          </div>
        )}
    </ResizableDrawer>
  );
}

// MemorySettingsPage is a self-contained memory management page embedded inside
// the settings centre. It loads its own data and handles all memory operations.
export function MemorySettingsPage() {
	const t = useT();
	const [view, setView] = useState<MemoryView | null>(null);
	const [note, setNote] = useState("");
	const [scope, setScope] = useState("");
	const [editingPath, setEditingPath] = useState<string | null>(null);
	const [draft, setDraft] = useState("");
	const [busy, setBusy] = useState(false);
	const [highlight, setHighlight] = useState<string | null>(null);
	const [query, setQuery] = useState("");
	const [typeFilter, setTypeFilter] = useState("all");
	const [levelFilter, setLevelFilter] = useState("all");
	const [expanded, setExpanded] = useState<string | null>(null);
	const [expandedDoc, setExpandedDoc] = useState<string | null>(null);
	const [confirmForget, setConfirmForget] = useState<string | null>(null);
	const [error, setError] = useState<string | null>(null);
	const [tab, setTab] = useState<"memories" | "timeline" | "docs">("memories");
	const [showAdd, setShowAdd] = useState(false);
	const [historyView, setHistoryView] = useState<MemoryView | null>(null);
	const [historyLoading, setHistoryLoading] = useState(false);
	const [asOfDay, setAsOfDay] = useState("");
	const factRefs = useRef<Record<string, HTMLElement | null>>({});

	const reload = useCallback(async () => {
		setView(await app.Memory().catch(() => null));
	}, []);
	useEffect(() => { void reload(); }, [reload]);

	// loadHistory fetches the full bitemporal surface (active + superseded) for
	// the timeline view. It's lazy — only called when the user opens the
	// timeline tab — so the normal memories view stays a single cheap Memory()
	// round-trip. It clears on reload so stale superseded records don't linger.
	const loadHistory = useCallback(async () => {
		setHistoryLoading(true);
		try {
			setHistoryView(await app.MemoryHistory().catch(() => null));
		} finally {
			setHistoryLoading(false);
		}
	}, []);
	// (Re)load history when entering the timeline tab; reload also clears it so
	// the next visit re-fetches fresh data after a remember/forget mutation.
	useEffect(() => {
		if (tab === "timeline") void loadHistory();
		else setHistoryView(null);
	}, [tab, loadHistory]);

	const facts = view?.facts ?? [];
	const factNames = useMemo(() => new Set(facts.map((f) => f.name)), [facts]);
	const factTypes = useMemo(
		() => Array.from(new Set(facts.map((f) => f.type).filter(Boolean))).sort(),
		[facts],
	);
	const levelCounts = useMemo(() => {
		const out: Record<string, number> = {};
		for (const f of facts) {
			const l = memoryLevelShort(f.level).toLowerCase();
			out[l] = (out[l] ?? 0) + 1;
		}
		return out;
	}, [facts]);
	const normalizedQuery = query.trim().toLowerCase();
	const filteredFacts = useMemo(
		() =>
			facts.filter((f) => {
				if (typeFilter !== "all" && f.type !== typeFilter) return false;
				if (levelFilter !== "all" && memoryLevelShort(f.level).toLowerCase() !== levelFilter) return false;
				if (!normalizedQuery) return true;
				return memoryFactHaystack(f).includes(normalizedQuery);
			}),
		[facts, normalizedQuery, typeFilter, levelFilter],
	);

	// historyFacts is the timeline view's data: the full bitemporal surface,
	// optionally narrowed by a point-in-time (asOfDay) query. When asOfDay is
	// set we keep only facts that were true on that day, mirroring the backend
	// ListAsOf semantics — this is the demo of the bitemporal "what did we
	// know on date X" capability. Without asOfDay we show the whole version
	// chain so the user can see expired/superseded records in context.
	const historyFacts = useMemo(() => {
		const all = historyView?.facts ?? [];
		if (!asOfDay) return all;
		return all.filter((f) => validAtPoint(f, asOfDay));
	}, [historyView, asOfDay]);
	const timelineGroups = useMemo(() => groupTimelineFacts(historyFacts, t), [historyFacts, t]);
	const expiredCount = useMemo(
		() => historyFacts.filter((f) => isExpired(f)).length,
		[historyFacts],
	);
	const supersededCount = useMemo(
		() => historyFacts.filter((f) => f.status === "superseded").length,
		[historyFacts],
	);

	const scrollToFact = useCallback((name: string) => {
		const el = factRefs.current[name];
		if (!el) return;
		el.scrollIntoView({ block: "center", behavior: "auto" });
		setHighlight(name);
		window.setTimeout(() => setHighlight((h) => (h === name ? null : h)), 1200);
	}, []);

	const jumpTo = useCallback((name: string) => {
		if (!factNames.has(name)) return;
		const visible = filteredFacts.some((f) => f.name === name);
		setExpanded(name);
		setConfirmForget(null);
		if (!visible) {
			setQuery("");
			setTypeFilter("all");
			setLevelFilter("all");
			window.setTimeout(() => scrollToFact(name), 0);
			return;
		}
		scrollToFact(name);
	}, [factNames, filteredFacts, scrollToFact]);

	const renderWithLinks = useCallback((text: string): ReactNode[] => {
		const out: ReactNode[] = [];
		const re = /\[\[([^\]]+)\]\]/g;
		let last = 0;
		let k = 0;
		let m: RegExpExecArray | null;
		while ((m = re.exec(text)) !== null) {
			if (m.index > last) out.push(text.slice(last, m.index));
			const target = m[1].trim();
			out.push(
				factNames.has(target) ? (
					<button key={k++} type="button" className="mem-link" onClick={() => jumpTo(target)}>
						{target}
					</button>
				) : (
					<Tooltip key={k++} label={t("memory.deadLink", { name: target })}>
						<span className="mem-link mem-link--dead">{target}</span>
					</Tooltip>
				),
			);
			last = re.lastIndex;
		}
		if (last < text.length) out.push(text.slice(last));
		return out;
	}, [factNames, jumpTo, t]);

	const forgetFact = useCallback(async (name: string) => {
		if (busy) return;
		setBusy(true);
		setError(null);
		try {
			await app.Forget(name);
			await reload();
			if (expanded === name) setExpanded(null);
			setConfirmForget(null);
		} catch (err) {
			setError(errorMessage(err));
		} finally {
			setBusy(false);
		}
	}, [busy, expanded, reload]);

	// promoteFact/rejectFact handle auto-captured "pending" memories from the
	// timeline view. Promote flips pending→active (enters the prompt); reject
	// deletes it. Both refresh the history view so the card disappears/moves.
	const promoteFact = useCallback(async (name: string) => {
		if (busy) return;
		setBusy(true);
		setError(null);
		try {
			await app.PromoteMemory(name);
			await loadHistory();
		} catch (err) {
			setError(errorMessage(err));
		} finally {
			setBusy(false);
		}
	}, [busy, loadHistory]);

	const rejectFact = useCallback(async (name: string) => {
		if (busy) return;
		setBusy(true);
		setError(null);
		try {
			await app.RejectMemory(name);
			if (expanded === name) setExpanded(null);
			await loadHistory();
		} catch (err) {
			setError(errorMessage(err));
		} finally {
			setBusy(false);
		}
	}, [busy, expanded, loadHistory]);

	const scopes = view?.scopes ?? [];
	const activeScope =
		scope || scopes.find((s) => s.scope === "project")?.scope || scopes[0]?.scope || "project";

	const submitNote = useCallback(async () => {
		const trimmed = note.trim();
		if (!trimmed || busy) return;
		setBusy(true);
		setError(null);
		try {
			await app.Remember(activeScope, trimmed);
			await reload();
			setNote("");
			setShowAdd(false);
		} catch (err) {
			setError(errorMessage(err));
		} finally {
			setBusy(false);
		}
	}, [note, busy, activeScope, reload]);

	const startEdit = useCallback((path: string, body: string) => {
		setEditingPath(path);
		setDraft(body);
	}, []);

	const saveEdit = useCallback(async () => {
		if (editingPath === null || busy) return;
		setBusy(true);
		setError(null);
		try {
			await app.SaveDoc(editingPath, draft);
			await reload();
			setEditingPath(null);
		} catch (err) {
			setError(errorMessage(err));
		} finally {
			setBusy(false);
		}
	}, [editingPath, busy, draft, reload]);

	if (!view?.available) {
		return <div className="empty">{t("memory.unavailable")}</div>;
	}

	return (
		<>
			<MemoryStorageSection storeDir={view.storeDir} />
			<MemoryPromotionSection facts={facts} />
			<SelfEvolutionSection />
			<div className="settings-subtabs" role="tablist" aria-label={t("settings.tab.memory")}>
				<button
					className={"settings-subtab" + (tab === "memories" ? " settings-subtab--active" : "")}
					role="tab"
					aria-selected={tab === "memories"}
					type="button"
					onClick={() => setTab("memories")}
				>
					<span>{t("memory.memoryEntries")}</span>
					<small>{facts.length}</small>
				</button>
					<button
						className={"settings-subtab" + (tab === "timeline" ? " settings-subtab--active" : "")}
						role="tab"
						aria-selected={tab === "timeline"}
						type="button"
						onClick={() => setTab("timeline")}
					>
						<span>{t("memory.timeline")}</span>
						{historyFacts.length > 0 && <small>{historyFacts.length}</small>}
					</button>
				<button
					className={"settings-subtab" + (tab === "docs" ? " settings-subtab--active" : "")}
					role="tab"
					aria-selected={tab === "docs"}
					type="button"
					onClick={() => setTab("docs")}
				>
					<span>{t("memory.instructionFiles")}</span>
					<small>{view.docs.length}</small>
				</button>
			</div>

			{tab === "memories" && <section className="mem-section">
				<div className="mem-section__head">
					<div>
						<div className="mem-section__title">{t("memory.memoryEntries")}</div>
						<div className="mem-note">{t("memory.fallibleNote")}</div>
					</div>
					<div className="mem-section__actions">
						<span className="mem-count">{facts.length}</span>
						<button
							className="btn btn--small"
							type="button"
							disabled={busy}
							onClick={() => setShowAdd((v) => !v)}
						>
							{showAdd ? t("common.collapse") : t("memory.addMemory")}
						</button>
					</div>
				</div>
				{showAdd && (
					<div className="mem-add-card">
						<div className="mem-add-card__head">
							<div>
								<strong>{t("memory.addMemory")}</strong>
								<span>{t("memory.addMemoryHint")}</span>
							</div>
						</div>
						<div className="mem-add mem-add--stacked">
							<div style={{ display: "flex", gap: "8px", alignItems: "center" }}>
								<Tooltip label={t("memory.whereToSave")}>
									<select
										className="mem-select"
										value={activeScope}
										onChange={(e) => setScope(e.target.value)}
									>
										{scopes.map((s) => (
											<option key={s.scope} value={s.scope}>
												{memoryScopeLabel(s.scope, t)}
											</option>
										))}
									</select>
								</Tooltip>
								<span className="mem-hint mem-hint--inline">
									{scopes.find((s) => s.scope === activeScope)?.path}
								</span>
							</div>
							<textarea
								className="mem-input"
								style={{ width: "100%", minHeight: "60px", resize: "vertical" }}
								placeholder={t("memory.notePlaceholder")}
								value={note}
								onChange={(e) => setNote(e.target.value)}
								onKeyDown={(e) => {
									if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) void submitNote();
								}}
							/>
							<div style={{ display: "flex", justifyContent: "flex-end" }}>
								<button
									className="btn btn--primary btn--small"
									onClick={() => void submitNote()}
									disabled={busy || !note.trim()}
								>
									{t("memory.remember")}
								</button>
							</div>
						</div>
					</div>
					)}
					<div className="mem-toolbar">
					<label className="mem-search">
						<Search size={14} />
						<input
							value={query}
							onChange={(e) => setQuery(e.target.value)}
							placeholder={t("memory.searchPlaceholder")}
						/>
					</label>
					<div className="mem-filter" role="tablist" aria-label={t("memory.typeFilter")}>
						<button
							className={"mem-filter__item" + (typeFilter === "all" ? " mem-filter__item--on" : "")}
							onClick={() => { setTypeFilter("all"); setLevelFilter("all"); }}
							type="button"
						>
							{t("memory.allTypes")}
						</button>
								{factTypes.map((type) => (
									<button
										className={"mem-filter__item" + (typeFilter === type ? " mem-filter__item--on" : "")}
										onClick={() => setTypeFilter(type)}
										type="button"
										key={type}
									>
										{memoryTypeLabel(type, t)}
									</button>
								))}
					</div>
					<MemoryLevelFilter counts={levelCounts} value={levelFilter} onChange={setLevelFilter} />
				</div>
				{error && <div className="mem-error" role="alert">{error}</div>}
				{facts.length === 0 ? (
					<div className="mem-empty">{t("memory.noFacts")}</div>
				) : filteredFacts.length === 0 ? (
					<div className="mem-empty">
						{t("memory.noMatches")}
						<button
							className="mem-empty__action"
							onClick={() => {
								setQuery("");
								setTypeFilter("all");
								setLevelFilter("all");
							}}
							type="button"
						>
							{t("memory.clearFilters")}
						</button>
					</div>
				) : (
					<div className="mem-facts">
						{filteredFacts.map((f) => {
							const isOpen = expanded === f.name;
							const links = uniqueLinks(f.body, factNames);
							const missing = links.filter((link) => !link.exists);
							return (
								<article
									className={"mem-fact" + (highlight === f.name ? " mem-fact--hl" : "")}
									data-mem-type={f.type || "other"}
									key={f.name}
									ref={(el) => {
										factRefs.current[f.name] = el;
									}}
								>
									<button
										className="mem-fact__summary"
										onClick={() => {
											setExpanded(isOpen ? null : f.name);
											setConfirmForget(null);
										}}
										type="button"
									>
										{isOpen ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
										<span className="mem-fact__main">
											<span className="mem-fact__title">{displayTitle(f)}</span>
											<span className="mem-fact__meta">
												<span
													className="mem-fact__level"
													data-mem-level={memoryLevelShort(f.level).toLowerCase()}
													title={memoryLevelLabel(f.level, t)}
												>
													{memoryLevelShort(f.level)}
												</span>
												{f.type && <span className="mem-fact__type" data-mem-type={f.type}>{memoryTypeLabel(f.type, t)}</span>}
												<span className="mem-fact__slug">{f.name}</span>
												{memoryFactTags(f).map((tag) => (
													<span
														key={tag}
														className="mem-tag"
														role="button"
														tabIndex={-1}
														title={t("memory.filterByTag", { tag })}
														onClick={(e) => {
															e.stopPropagation();
															setQuery(tag);
														}}
													>
														#{tag}
													</span>
												))}
											</span>
											<span className="mem-fact__desc">{f.description}</span>
										</span>
									</button>
									{links.length > 0 && (
										<div className="mem-fact__links" aria-label={t("memory.links")}>
											{links.map((link) =>
												link.exists ? (
													<button
														className="mem-link-chip"
														key={link.name}
														onClick={() => jumpTo(link.name)}
														type="button"
													>
														[[{link.name}]]
													</button>
												) : (
													<Tooltip key={link.name} label={t("memory.deadLink", { name: link.name })}>
														<span className="mem-link-chip mem-link-chip--dead">[[{link.name}]]</span>
													</Tooltip>
												),
											)}
										</div>
									)}
									{isOpen && (
										<div className="mem-fact__detail">
											{f.body ? (
												<div className="mem-fact__body">{renderWithLinks(f.body)}</div>
											) : (
												<div className="mem-empty">{t("memory.noBody")}</div>
											)}
											{missing.length > 0 && (
												<div className="mem-deadline">
													{t("memory.missingLinks", { n: missing.length })}
												</div>
											)}
											<div className="mem-fact__actions">
												<span className="mem-hint mem-hint--inline">
													{t("memory.appliesNow")}
												</span>
												{confirmForget === f.name ? (
													<div className="mem-confirm">
														<button
															className="btn btn--small"
															onClick={() => setConfirmForget(null)}
															disabled={busy}
															type="button"
														>
															{t("common.cancel")}
														</button>
														<button
															className="btn btn--small mem-danger"
															onClick={() => void forgetFact(f.name)}
															disabled={busy}
															type="button"
														>
															{t("memory.confirmForget")}
														</button>
													</div>
												) : (
													<button
														className="btn btn--small mem-fact__forget"
														onClick={() => setConfirmForget(f.name)}
														disabled={busy}
														type="button"
													>
														<Trash2 size={13} />
														{t("memory.forget")}
													</button>
												)}
											</div>
										</div>
									)}
								</article>
							);
						})}
					</div>
				)}
				{view.storeDir && (
					<div className="mem-hint">{t("memory.storedUnder", { dir: view.storeDir })}</div>
				)}
			</section>}

			{tab === "timeline" && <section className="mem-section">
				<div className="mem-section__head">
					<div>
						<div className="mem-section__title">{t("memory.timeline")}</div>
						<div className="mem-note">{t("memory.timelineHint")}</div>
					</div>
					{(expiredCount > 0 || supersededCount > 0) && (
						<div className="mem-timeline__legend">
							{supersededCount > 0 && (
								<span className="mem-timeline__legend-item mem-timeline__legend-item--superseded">
									{t("memory.legendSuperseded", { n: supersededCount })}
								</span>
							)}
							{expiredCount > 0 && (
								<span className="mem-timeline__legend-item mem-timeline__legend-item--expired">
									{t("memory.legendExpired", { n: expiredCount })}
								</span>
							)}
						</div>
					)}
				</div>

				<div className="mem-timeline__asof">
					<label className="mem-search mem-timeline__asof-field">
						<Calendar size={14} />
						<input
							type="date"
							value={asOfDay}
							max={new Date().toISOString().slice(0, 10)}
							onChange={(e) => setAsOfDay(e.target.value)}
						/>
					</label>
					{asOfDay && (
						<button
							className="mem-filter__item mem-filter__item--on"
							type="button"
							onClick={() => setAsOfDay("")}
						>
							{t("memory.asOfClear")}
						</button>
					)}
					<span className="mem-hint mem-hint--inline">
						{asOfDay
							? t("memory.asOfActive", { day: asOfDay })
							: t("memory.asOfIdle")}
					</span>
				</div>

				{historyLoading ? (
					<div className="mem-empty">{t("common.loading")}</div>
				) : historyFacts.length === 0 ? (
					<div className="mem-empty">
						{asOfDay ? t("memory.asOfEmpty", { day: asOfDay }) : t("memory.noHistory")}
					</div>
				) : (
					<div className="mem-timeline">
						{timelineGroups.map((group) => (
							<div className="mem-timeline__group" key={group.label}>
								<div className="hist-group__title mem-section__title">
									{group.label}
									<span className="hist-group__count">{group.items.length}</span>
								</div>
								<div className="mem-facts">
									{group.items.map((f) => (
										<MemoryTimelineCard
											key={f.name}
											fact={f}
											allNames={factNames}
											expanded={expanded === f.name}
											highlighted={highlight === f.name}
											asOfDay={asOfDay}
											onToggle={() => {
												setExpanded(expanded === f.name ? null : f.name);
												setConfirmForget(null);
											}}
											jumpTo={jumpTo}
											renderWithLinks={renderWithLinks}
											onPromote={promoteFact}
											onReject={rejectFact}
											busy={busy}
											t={t}
										/>
									))}
								</div>
							</div>
						))}
					</div>
				)}
			</section>}

			{tab === "docs" && <section className="mem-section">
				<div className="mem-section__head">
					<div>
						<div className="mem-section__title">{t("memory.instructionFiles")}</div>
						<div className="mem-note">{t("memory.instructionFilesHint")}</div>
					</div>
					<span className="mem-count">{view.docs.length}</span>
				</div>
				{view.docs.length === 0 && (
					<div className="mem-empty">{t("memory.noDocs")}</div>
				)}
				{view.docs.map((d) => {
					const editing = editingPath === d.path;
					const open = expandedDoc === d.path || editing;
					return (
						<div className="mem-doc" key={d.path}>
							<div className="mem-doc__head">
								<div className="mem-doc__identity">
									<span className="mem-doc__icon"><FileText size={15} /></span>
									<div>
										<strong>{memoryDocTitle(d.scope, t)}</strong>
										<span className="mem-doc__path">{d.path}</span>
										<small>{memoryDocHint(d.scope, t)}</small>
									</div>
								</div>
								<div className="mem-doc__head-actions">
									<span className={"mem-doc__tag badge--" + d.scope}>{memoryScopeLabel(d.scope, t)}</span>
									{!editing && (
										<button
											className="btn btn--small"
											type="button"
											onClick={() => setExpandedDoc(open ? null : d.path)}
										>
											{open ? t("common.collapse") : t("memory.expandDoc")}
										</button>
									)}
									{!editing && (
									<button
										className="btn btn--small"
										onClick={() => startEdit(d.path, d.body)}
									>
										{t("common.edit")}
									</button>
									)}
								</div>
							</div>
							{editing ? (
								<div className="mem-doc__edit">
									<textarea
										className="mem-textarea"
										value={draft}
										onChange={(e) => setDraft(e.target.value)}
										spellCheck={false}
									/>
									<div className="mem-doc__actions">
										<button
											className="btn btn--small"
											onClick={() => setEditingPath(null)}
											disabled={busy}
										>
											{t("common.cancel")}
										</button>
										<button
											className="btn btn--primary btn--small"
											onClick={() => void saveEdit()}
											disabled={busy}
										>
											{t("common.save")}
										</button>
									</div>
								</div>
							) : (
								<pre className={"mem-doc__body" + (!open ? " mem-doc__body--preview" : "")}>
									{open ? d.body : memoryDocPreview(d.body)}
								</pre>
							)}
						</div>
					);
				})}
			</section>}
		</>
	);
}

// MemoryTimelineCard renders one fact inside the timeline view. It mirrors the
// memories-tab card structure but adds bitemporal badges: an "expired" state
// (validTo in the past), a "superseded by X" link, and a validity window line.
// When an asOfDay query is active, the card also shows whether the fact was
// true on that day, making the bitemporal query legible at a glance.
function MemoryTimelineCard({
	fact: f,
	allNames,
	expanded,
	highlighted,
	asOfDay,
	onToggle,
	jumpTo,
	renderWithLinks,
	onPromote,
	onReject,
	busy,
	t,
}: {
	fact: MemoryFact;
	allNames: Set<string>;
	expanded: boolean;
	highlighted: boolean;
	asOfDay: string;
	onToggle: () => void;
	jumpTo: (name: string) => void;
	renderWithLinks: (text: string) => ReactNode[];
	onPromote?: (name: string) => void;
	onReject?: (name: string) => void;
	busy: boolean;
	t: Translator;
}) {
	const links = uniqueLinks(f.body, allNames);
	const missing = links.filter((l) => !l.exists);
	const expired = isExpired(f);
	const isSuperseded = f.status === "superseded";
	const dormant = f.status === "dormant";
	const isPending = f.status === "pending";
	const hasWindow = Boolean(f.validFrom || f.validTo);
	const inRange = asOfDay ? validAtPoint(f, asOfDay) : null;

	// The card dimming class: superseded records are de-emphasized (greyed) so
	// the current truth stands out, but remain readable for history context.
	const cardClass =
		"mem-fact mem-fact--timeline" +
		(highlighted ? " mem-fact--hl" : "") +
		(isSuperseded ? " mem-fact--superseded" : "") +
		(expired && !isSuperseded ? " mem-fact--expired" : "") +
		(dormant ? " mem-fact--dormant" : "") +
		(isPending ? " mem-fact--pending" : "") +
		(asOfDay && inRange === false ? " mem-fact--out-of-range" : "");

	return (
		<article className={cardClass} data-mem-type={f.type || "other"}>
			<button className="mem-fact__summary" onClick={onToggle} type="button">
				{expanded ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
				<span className="mem-fact__main">
					<span className="mem-fact__title">{displayTitle(f)}</span>
					<span className="mem-fact__meta">
						{f.type && (
							<span className="mem-fact__type" data-mem-type={f.type}>{memoryTypeLabel(f.type, t)}</span>
						)}
						<span className="mem-fact__slug">{f.name}</span>
						{isSuperseded && (
							<span className="mem-fact__badge mem-fact__badge--superseded">
								{t("memory.badgeSuperseded")}
							</span>
						)}
						{expired && !isSuperseded && (
							<span className="mem-fact__badge mem-fact__badge--expired">
								{t("memory.badgeExpired")}
							</span>
						)}
						{dormant && (
							<span className="mem-fact__badge mem-fact__badge--dormant">
								{t("memory.badgeDormant")}
							</span>
						)}
						{isPending && (
							<span className="mem-fact__badge mem-fact__badge--pending">
								{t("memory.badgePending")}
							</span>
						)}
						{asOfDay && (
							<span className={"mem-fact__badge mem-fact__badge--" + (inRange ? "valid" : "muted")}>
								{inRange ? t("memory.badgeValidAt") : t("memory.badgeNotAt")}
							</span>
						)}
					</span>
					<span className="mem-fact__desc">{f.description}</span>
					{hasWindow && (
						<span className="mem-fact__validity">
							<Calendar size={11} />
							{f.validFrom && <span>{t("memory.validFromLabel")}: {f.validFrom}</span>}
							{f.validTo && <span>{t("memory.validToLabel")}: {f.validTo}</span>}
						</span>
					)}
				</span>
			</button>
			{isSuperseded && f.supersededBy && (
				<div className="mem-fact__superseded">
					{t("memory.supersededBy", { name: f.supersededBy })}
				</div>
			)}
			{links.length > 0 && (
				<div className="mem-fact__links" aria-label={t("memory.links")}>
					{links.map((link) =>
						link.exists ? (
							<button
								className="mem-link-chip"
								key={link.name}
								onClick={() => jumpTo(link.name)}
								type="button"
							>
								[[{link.name}]]
							</button>
						) : (
							<Tooltip key={link.name} label={t("memory.deadLink", { name: link.name })}>
								<span className="mem-link-chip mem-link-chip--dead">[[{link.name}]]</span>
							</Tooltip>
						),
					)}
				</div>
			)}
			{expanded && (
				<div className="mem-fact__detail">
					{f.body ? (
						<div className="mem-fact__body">{renderWithLinks(f.body)}</div>
					) : (
						<div className="mem-empty">{t("memory.noBody")}</div>
					)}
					{missing.length > 0 && (
						<div className="mem-deadline">
							{t("memory.missingLinks", { n: missing.length })}
						</div>
					)}
					{isPending && (onPromote || onReject) && (
						<div className="mem-fact__actions mem-fact__actions--pending">
							<span className="mem-hint mem-hint--inline">
								{t("memory.pendingHint")}
							</span>
							<div className="mem-confirm">
								{onReject && (
									<button
										className="btn btn--small"
										onClick={() => onReject(f.name)}
										disabled={busy}
										type="button"
									>
										{t("memory.reject")}
									</button>
								)}
								{onPromote && (
									<button
										className="btn btn--primary btn--small"
										onClick={() => onPromote(f.name)}
										disabled={busy}
										type="button"
									>
										{t("memory.promote")}
									</button>
								)}
							</div>
						</div>
					)}
				</div>
			)}
		</article>
	);
}

// SelfEvolutionSection is the Dream/Distill configuration + status block at the
// top of the Memory settings page. Dream consolidates session knowledge into
// project memory; Distill extracts repeated workflows into skills. Both run in
// the background on a cadence; here the user can toggle them, set the cadence,
// run them on demand, and see when they last ran.
// MemoryStorageSection is the [memory] section's editor: WHERE the memory data
// tree lives and WHICH model maintains it in the background.
//
// Both are USER-GLOBAL settings (the backend pins them out of any project
// config, so a cloned repo cannot redirect someone's memory or their API spend),
// which is why they are edited here rather than per project. Saving a root does
// NOT move data — the migrate action copies the existing tree to the new root,
// and the root itself takes effect on the next session, because the data root is
// part of the boot snapshot the cache-stable prompt prefix is built from.
function MemoryStorageSection({ storeDir }: { storeDir?: string }) {
  const t = useT();
  const [settings, setSettings] = useState<MemorySettings | null>(null);
  const [providers, setProviders] = useState<ProviderView[]>([]);
  const [root, setRoot] = useState("");
  const [provider, setProvider] = useState("");
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  const [injectIndex, setInjectIndex] = useState(true);
  const [indexMaxChars, setIndexMaxChars] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [report, setReport] = useState<MemoryMigrationReport | null>(null);
  const [confirmMigrate, setConfirmMigrate] = useState(false);

  const adopt = useCallback((s: MemorySettings) => {
    setSettings(s);
    setRoot(s.configuredRoot ?? "");
    setProvider(s.provider ?? "");
    setModel(s.model ?? "");
    setEffort(s.effort ?? "");
    setInjectIndex(s.injectIndex);
    setIndexMaxChars(s.indexMaxChars);
  }, []);

  const reload = useCallback(async () => {
    const view = await app.Memory().catch(() => null);
    if (view?.settings) adopt(view.settings);
  }, [adopt]);

  useEffect(() => { void reload(); }, [reload]);
  // Providers come from the settings payload so the maintenance model is picked
  // from the models the user actually configured, not typed by hand.
  useEffect(() => {
    void app.Settings()
      .then((s) => setProviders(s.providers ?? []))
      .catch(() => setProviders([]));
  }, []);

  const modelOptions = useMemo(() => {
    const list = providers.find((p) => p.name === provider)?.models ?? [];
    // Keep an unknown/legacy value selectable instead of silently dropping it.
    return model && !list.includes(model) ? [model, ...list] : list;
  }, [providers, provider, model]);

  const effortOptions = useMemo(() => {
    const sup = providers.find((p) => p.name === provider)?.supportedEfforts ?? [];
    return sup.length > 0 ? sup : ["low", "medium", "high", "max"];
  }, [providers, provider]);

  const save = async () => {
    if (busy) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const input: MemorySettingsInput = { root, provider, model, effort, injectIndex, indexMaxChars };
      adopt(await app.SaveMemorySettings(input));
      setNotice(t("memory.saved"));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const migrate = async () => {
    if (busy) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    setReport(null);
    try {
      const rep = await app.MigrateMemoryRoot(root);
      setReport(rep);
      setConfirmMigrate(false);
    } catch (e) {
      setError(errorMessage(e));
      setConfirmMigrate(false);
    } finally {
      setBusy(false);
    }
  };

  if (!settings) {
    return (
      <section className="mem-section">
        <div className="mem-section__title">{t("memory.storageTitle")}</div>
        <div className="mem-empty">{t("settings.loading")}</div>
      </section>
    );
  }

  const dirty =
    root.trim() !== (settings.configuredRoot ?? "") ||
    provider.trim() !== (settings.provider ?? "") ||
    model.trim() !== (settings.model ?? "") ||
    effort.trim() !== (settings.effort ?? "") ||
    injectIndex !== settings.injectIndex ||
    indexMaxChars !== settings.indexMaxChars;

  return (
    <section className="mem-section mem-storage">
      <div className="mem-section__head">
        <div>
          <div className="mem-section__title">{t("memory.storageTitle")}</div>
          <div className="mem-note">{t("memory.storageHint")}</div>
        </div>
      </div>

      {error && <div className="mem-error" role="alert">{error}</div>}
      {notice && <div className="mem-notice">{notice}</div>}

      <div className="mem-field">
        <label className="mem-field__label" htmlFor="mem-root">{t("memory.rootLabel")}</label>
        <div className="mem-field__row">
          <input
            id="mem-root"
            className="mem-input"
            value={root}
            spellCheck={false}
            placeholder={settings.defaultRoot}
            onChange={(e) => setRoot(e.target.value)}
            disabled={settings.rootFromEnv}
          />
          {settings.configuredRoot && (
            <button type="button" className="btn btn--small" onClick={() => setRoot("")} disabled={busy}>
              {t("memory.rootReset")}
            </button>
          )}
          <button type="button" className="btn btn--primary btn--small" onClick={() => void save()} disabled={busy || !dirty}>
            {t("common.save")}
          </button>
        </div>
        <div className="mem-hint">
          {t("memory.rootEffective", { dir: settings.root })}
        </div>
        {settings.rootFromEnv && (
          <div className="mem-warn">{t("memory.rootFromEnv", { env: "HIQ_MEMORY_ROOT" })}</div>
        )}
        {!settings.configuredRoot && !settings.rootFromEnv && (
          <div className="mem-hint">{t("memory.rootDefault", { dir: settings.defaultRoot })}</div>
        )}
      </div>

      {/* The three level buckets, so "where does an L2 fact go?" is answerable
          without reading the config file. */}
      <div className="mem-buckets">
        <div className="mem-bucket">
          <span className="mem-bucket__tag" data-mem-level="l1">L1</span>
          <span className="mem-bucket__label">{t("memory.levelL1")}</span>
          <code className="mem-bucket__path" title={settings.globalDir}>{settings.globalDir}</code>
        </div>
        <div className="mem-bucket">
          <span className="mem-bucket__tag" data-mem-level="l2">L2</span>
          <span className="mem-bucket__label">{t("memory.levelL2")}</span>
          <code className="mem-bucket__path" title={storeDir ?? ""}>{storeDir || "—"}</code>
        </div>
        <div className="mem-bucket">
          <span className="mem-bucket__tag" data-mem-level="l3">L3</span>
          <span className="mem-bucket__label">{t("memory.levelL3")}</span>
          <code className="mem-bucket__path" title={settings.sessionDir}>{settings.sessionDir}</code>
        </div>
      </div>

      {/* Migration: copy-only, destination wins, safe to re-run. */}
      <div className="mem-field">
        <div className="mem-field__row">
          {confirmMigrate ? (
            <>
              <span className="mem-hint mem-hint--inline">{t("memory.migrateConfirm", { dir: root })}</span>
              <button type="button" className="btn btn--primary btn--small" onClick={() => void migrate()} disabled={busy}>
                {t("memory.migrateDo")}
              </button>
              <button type="button" className="btn btn--small" onClick={() => setConfirmMigrate(false)} disabled={busy}>
                {t("common.cancel")}
              </button>
            </>
          ) : (
            <>
              <button
                type="button"
                className="btn btn--small"
                onClick={() => setConfirmMigrate(true)}
                disabled={busy || !dirty || !root.trim()}
                title={t("memory.migrateHint")}
              >
                {t("memory.migrate")}
              </button>
              <span className="mem-hint mem-hint--inline">{t("memory.migrateHint")}</span>
            </>
          )}
        </div>
        {report && (
          <div className="mem-hint">
            {t("memory.migrateDone", { files: report.files, bytes: report.bytes, to: report.to })}
            {report.skipped.length > 0 && (
              <ul className="mem-report">
                {report.skipped.map((s) => <li key={s}>{s}</li>)}
              </ul>
            )}
          </div>
        )}
      </div>

      {/* Maintenance model: the cheap model the background Dream/Distill pass
          runs on, so a periodic consolidation never spends main-model tokens. */}
      <div className="mem-field mem-field--split">
        <div className="mem-field__label">{t("memory.maintenanceTitle")}</div>
        <div className="mem-field__row">
          <Tooltip label={t("memory.maintenanceProviderHint")}>
            <select className="mem-select" value={provider} onChange={(e) => setProvider(e.target.value)} disabled={busy}>
              <option value="">{t("memory.maintenanceInherit")}</option>
              {providers.map((p) => (
                <option key={p.name} value={p.name}>{p.name}</option>
              ))}
            </select>
          </Tooltip>
          <Tooltip label={t("memory.maintenanceModelHint")}>
            <select className="mem-select" value={model} onChange={(e) => setModel(e.target.value)} disabled={busy}>
              <option value="">{t("memory.maintenanceModelDefault")}</option>
              {modelOptions.map((m) => (
                <option key={m} value={m}>{m}</option>
              ))}
            </select>
          </Tooltip>
          <Tooltip label={t("memory.maintenanceEffortHint")}>
            <select className="mem-select mem-select--narrow" value={effort} onChange={(e) => setEffort(e.target.value)} disabled={busy}>
              <option value="">{t("memory.maintenanceEffortDefault")}</option>
              {effortOptions.map((e) => (
                <option key={e} value={e}>{e}</option>
              ))}
            </select>
          </Tooltip>
          <button type="button" className="btn btn--primary btn--small" onClick={() => void save()} disabled={busy || !dirty}>
            {t("common.save")}
          </button>
        </div>
        <div className="mem-hint">
          {settings.refFromMemory
            ? t("memory.maintenancePinned", { ref: settings.ref })
            : settings.ref
              ? t("memory.maintenanceInherited", { ref: settings.ref })
              : t("memory.maintenanceNone")}
        </div>
      </div>

      {/* The injected fact index — the "knowledge base index" that lets the model
          see WHAT memory exists while the bodies stay on disk for `recall`. */}
      <div className="mem-field mem-field--split">
        <div className="mem-field__label">{t("memory.indexTitle")}</div>
        <div className="mem-field__row">
          <label className="cap-switch" aria-label={t("memory.indexTitle")}>
            <input
              type="checkbox"
              checked={injectIndex}
              disabled={busy}
              onChange={(e) => setInjectIndex(e.target.checked)}
            />
            <span className="cap-switch__track" />
          </label>
          <span className="mem-hint mem-hint--inline">{injectIndex ? t("memory.indexOn") : t("memory.indexOff")}</span>
          <input
            className="mem-input mem-input--narrow"
            type="number"
            min={0}
            inputMode="numeric"
            value={indexMaxChars}
            disabled={busy || !injectIndex}
            onChange={(e) => setIndexMaxChars(Math.max(0, Math.floor(Number(e.target.value.replace(/[^\d]/g, "")) || 0)))}
            aria-label={t("memory.indexMaxChars")}
          />
          <span className="mem-hint mem-hint--inline">{t("memory.indexMaxChars")}</span>
          <button type="button" className="btn btn--primary btn--small" onClick={() => void save()} disabled={busy || !dirty}>
            {t("common.save")}
          </button>
        </div>
        <div className="mem-hint">{t("memory.indexHint")}</div>
      </div>
    </section>
  );
}

// selectPromotionSources mirrors memory.selectPromotionSources: the same filter
// the backend (and `recall`) applies, so the count shown before promoting is the
// count that will actually be folded in.
function selectPromotionSources(facts: MemoryFact[], level: string, tag: string, query: string): MemoryFact[] {
  const lv = level.trim().toLowerCase();
  const tg = tag.trim().toLowerCase();
  const q = query.trim().toLowerCase();
  if (!lv && !tg && !q) return [];
  return facts.filter((f) => {
    if (lv && memoryLevelShort(f.level).toLowerCase() !== lv) return false;
    if (tg && !memoryFactTags(f).some((x) => x.toLowerCase() === tg)) return false;
    if (q && !memoryFactHaystack(f).includes(q)) return false;
    return true;
  });
}

// MemoryPromotionSection turns accumulated memory into a reusable artifact — the
// self-evolution half of the memory layer.
//
// What it writes is DATA: a SKILL.md the existing skill loader indexes and
// run_skill runs, the source memories as references, and a plugin manifest whose
// capabilities use the kernel's own extensioncontract key format. Nothing here
// reaches into the agent kernel, so promoting can never break an agent update —
// which is exactly why it is safe to expose as a button.
function MemoryPromotionSection({ facts }: { facts: MemoryFact[] }) {
  const t = useT();
  const [kind, setKind] = useState<"skill" | "plugin">("plugin");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [notes, setNotes] = useState("");
  const [level, setLevel] = useState("");
  const [tag, setTag] = useState("");
  const [query, setQuery] = useState("");
  const [version, setVersion] = useState("");
  const [overwrite, setOverwrite] = useState(false);
  const [install, setInstall] = useState(true);
  const [scope, setScope] = useState<"global" | "project">("global");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<MemoryPromotionResult | null>(null);

  const selected = useMemo(() => selectPromotionSources(facts, level, tag, query), [facts, level, tag, query]);
  const canRun = selected.length > 0 && name.trim() !== "" && !busy;

  const run = async () => {
    if (!canRun) return;
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      const input: MemoryPromotionInput = {
        kind,
        name: name.trim(),
        description: description.trim(),
        level: level.trim(),
        tag: tag.trim(),
        query: query.trim(),
        notes: notes.trim(),
        version: version.trim(),
        overwrite,
        install,
        scope,
      };
      setResult(await app.PromoteMemoryArtifact(input));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="mem-section mem-promote">
      <div className="mem-section__head">
        <div>
          <div className="mem-section__title">{t("memory.promoteTitle")}</div>
          <div className="mem-note">{t("memory.promoteHint")}</div>
        </div>
      </div>

      {error && <div className="mem-error" role="alert">{error}</div>}

      <div className="mem-field">
        <div className="mem-field__label">{t("memory.promoteSelect")}</div>
        <div className="mem-field__row">
          <MemoryLevelFilter
            counts={{}}
            value={level || "all"}
            onChange={(v) => setLevel(v === "all" ? "" : v)}
          />
          <input
            className="mem-input mem-input--narrow"
            value={tag}
            onChange={(e) => setTag(e.target.value)}
            placeholder={t("memory.promoteTagPlaceholder")}
            aria-label={t("memory.promoteTag")}
          />
          <input
            className="mem-input"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("memory.promoteQueryPlaceholder")}
            aria-label={t("memory.promoteQuery")}
          />
        </div>
        <div className={selected.length > 0 ? "mem-hint" : "mem-warn"}>
          {selected.length > 0
            ? t("memory.promoteSelected", { n: selected.length })
            : t("memory.promoteNoSelection")}
        </div>
      </div>

      <div className="mem-field">
        <div className="mem-field__row">
          <Tooltip label={t("memory.promoteKindHint")}>
            <select className="mem-select" value={kind} onChange={(e) => setKind(e.target.value as "skill" | "plugin")} disabled={busy}>
              <option value="plugin">{t("memory.promoteKindPlugin")}</option>
              <option value="skill">{t("memory.promoteKindSkill")}</option>
            </select>
          </Tooltip>
          <input
            className="mem-input"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t("memory.promoteNamePlaceholder")}
            aria-label={t("memory.promoteName")}
            spellCheck={false}
          />
          <input
            className="mem-input mem-input--narrow"
            value={version}
            onChange={(e) => setVersion(e.target.value)}
            placeholder="1.0.0"
            aria-label={t("memory.promoteVersion")}
            spellCheck={false}
          />
        </div>
        <input
          className="mem-input"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder={t("memory.promoteDescPlaceholder")}
          aria-label={t("memory.promoteDesc")}
        />
        <textarea
          className="mem-input"
          style={{ minHeight: "52px", resize: "vertical" }}
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
          placeholder={t("memory.promoteNotesPlaceholder")}
          aria-label={t("memory.promoteNotes")}
        />
      </div>

      <div className="mem-field mem-field--split">
        <div className="mem-field__row">
          <label className="mem-check">
            <input type="checkbox" checked={install} disabled={busy} onChange={(e) => setInstall(e.target.checked)} />
            <span>{t("memory.promoteInstall")}</span>
          </label>
          <Tooltip label={t("memory.promoteScopeHint")}>
            <select
              className="mem-select"
              value={scope}
              disabled={busy || !install}
              onChange={(e) => setScope(e.target.value as "global" | "project")}
            >
              <option value="global">{t("memory.promoteScopeGlobal")}</option>
              <option value="project">{t("memory.promoteScopeProject")}</option>
            </select>
          </Tooltip>
          <label className="mem-check">
            <input type="checkbox" checked={overwrite} disabled={busy} onChange={(e) => setOverwrite(e.target.checked)} />
            <span>{t("memory.promoteOverwrite")}</span>
          </label>
          <button type="button" className="btn btn--primary btn--small" onClick={() => void run()} disabled={!canRun}>
            {busy ? t("memory.promoteRunning") : t("memory.promoteRun")}
          </button>
        </div>
        <div className="mem-hint">{t("memory.promoteInstallHint")}</div>
      </div>

      {result && (
        <div className="mem-notice">
          <div>{t("memory.promoteDone", { n: result.sources.length, dir: result.dir })}</div>
          <ul className="mem-report">
            <li>{result.skill}</li>
            {result.manifest && <li>{result.manifest}</li>}
            {result.readme && <li>{result.readme}</li>}
          </ul>
          {result.installed && <div>{t("memory.promoteInstalled", { path: result.installed })}</div>}
        </div>
      )}
    </section>
  );
}

function SelfEvolutionSection() {
	const t = useT();
	const [status, setStatus] = useState<DreamStatusView | null>(null);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string | null>(null);
	// Interval drafts are edited locally then saved on blur / button.
	const [dreamDraft, setDreamDraft] = useState("");
	const [distillDraft, setDistillDraft] = useState("");

	const reload = useCallback(async () => {
		const s = await app.DreamStatus().catch(() => null);
		setStatus(s);
		if (s) {
			setDreamDraft(String(s.dreamInterval));
			setDistillDraft(String(s.distillInterval));
		}
	}, []);
	useEffect(() => { void reload(); }, [reload]);

	const apply = async (fn: () => Promise<unknown>) => {
		setBusy(true);
		setError(null);
		try {
			await fn();
			await reload();
		} catch (e) {
			setError(String((e as Error)?.message ?? e));
		} finally {
			setBusy(false);
		}
	};

	// Optimistic toggle: flip immediately, roll back on failure (mirrors
	// the provider section's pattern so the switch never visibly snaps back).
	const toggleEnabled = async (enabled: boolean) => {
		const prev = status;
		if (prev) setStatus({ ...prev, enabled });
		setBusy(true);
		setError(null);
		try {
			await app.SetDreamEnabled(enabled);
			await reload();
		} catch (e) {
			setStatus(prev);
			setError(String((e as Error)?.message ?? e));
		} finally {
			setBusy(false);
		}
	};

	const saveIntervals = async () => {
		const d = Math.max(1, Math.floor(Number(dreamDraft) || 0));
		const di = Math.max(1, Math.floor(Number(distillDraft) || 0));
		setDreamDraft(String(d));
		setDistillDraft(String(di));
		if (status && d === status.dreamInterval && di === status.distillInterval) return;
		await apply(() => app.SetDreamIntervals(d, di));
	};

	const trigger = async (kind: "dream" | "distill") => {
		await apply(() => (kind === "dream" ? app.TriggerDream() : app.TriggerDistill()));
	};

	const disabled = !status?.enabled;
	const fmtAgo = (run?: DreamRunView): string => {
		if (!run) return t("dream.neverRun");
		const ms = Date.now() - new Date(run.startedAt).getTime();
		if (!Number.isFinite(ms) || ms < 0) return run.startedAt;
		const days = Math.floor(ms / 86_400_000);
		const hours = Math.floor(ms / 3_600_000);
		if (days >= 1) return t("dream.daysAgo", { n: days });
		if (hours >= 1) return t("dream.hoursAgo", { n: hours });
		return t("dream.justNow");
	};

	if (!status) {
		return (
			<section className="mem-section">
				<div className="mem-section__title">{t("dream.title")}</div>
				<div className="mem-empty">{t("settings.loading")}</div>
			</section>
		);
	}

	return (
		<section className="mem-section dream-section">
			<div className="mem-section__head">
				<div>
					<div className="mem-section__title">{t("dream.title")}</div>
					<div className="mem-note">{t("dream.subtitle")}</div>
				</div>
				<div className="dream-master">
					<span>{status.enabled ? t("settings.toggleOn") : t("settings.toggleOff")}</span>
					<label className="cap-switch" aria-label={t("dream.title")}>
						<input
							type="checkbox"
							checked={status.enabled}
							disabled={busy}
							onChange={(e) => void toggleEnabled(e.target.checked)}
						/>
						<span className="cap-switch__track" />
					</label>
				</div>
			</div>

			{error && <div className="mem-error" role="alert">{error}</div>}

			<div className="dream-grid">
				{/* Dream: memory consolidation */}
				<div className={`dream-card${disabled ? " dream-card--disabled" : ""}`}>
					<div className="dream-card__head">
						<strong>{t("dream.dreamName")}</strong>
						<span className="dream-card__status">
							{status.dreamInFlight ? t("dream.running") : t("dream.lastRun", { when: fmtAgo(status.lastDream) })}
						</span>
					</div>
					<div className="mem-note">{t("dream.dreamDesc")}</div>
					<div className="dream-card__controls">
						<label className="dream-interval">
							<span>{t("dream.interval")}</span>
							<input
								className="mem-input dream-interval__input"
								type="number"
								min={1}
								value={dreamDraft}
								disabled={busy || disabled}
								inputMode="numeric"
								onChange={(e) => setDreamDraft(e.target.value.replace(/[^\d]/g, ""))}
								onBlur={() => void saveIntervals()}
								onKeyDown={(e) => { if (e.key === "Enter") e.currentTarget.blur(); }}
							/>
							<small>{t("dream.days")}</small>
						</label>
						<button
							type="button"
							className="btn btn--secondary btn--small"
							disabled={busy || disabled || status.dreamInFlight}
							onClick={() => void trigger("dream")}
						>
							{status.dreamInFlight ? t("dream.running") : t("dream.runNow")}
						</button>
					</div>
				</div>

				{/* Distill: workflow extraction */}
				<div className={`dream-card${disabled ? " dream-card--disabled" : ""}`}>
					<div className="dream-card__head">
						<strong>{t("dream.distillName")}</strong>
						<span className="dream-card__status">
							{status.distillInFlight ? t("dream.running") : t("dream.lastRun", { when: fmtAgo(status.lastDistill) })}
						</span>
					</div>
					<div className="mem-note">{t("dream.distillDesc")}</div>
					<div className="dream-card__controls">
						<label className="dream-interval">
							<span>{t("dream.interval")}</span>
							<input
								className="mem-input dream-interval__input"
								type="number"
								min={1}
								value={distillDraft}
								disabled={busy || disabled}
								inputMode="numeric"
								onChange={(e) => setDistillDraft(e.target.value.replace(/[^\d]/g, ""))}
								onBlur={() => void saveIntervals()}
								onKeyDown={(e) => { if (e.key === "Enter") e.currentTarget.blur(); }}
							/>
							<small>{t("dream.days")}</small>
						</label>
						<button
							type="button"
							className="btn btn--secondary btn--small"
							disabled={busy || disabled || status.distillInFlight}
							onClick={() => void trigger("distill")}
						>
							{status.distillInFlight ? t("dream.running") : t("dream.runNow")}
						</button>
					</div>
				</div>
			</div>

			{status.history.length > 0 && (
				<div className="dream-history">
					<div className="dream-history__title">{t("dream.history")}</div>
					{status.history.slice(0, 6).map((r, i) => (
						<div key={i} className={`dream-history__row dream-history__row--${r.status}`}>
							<span className="dream-history__kind">{r.kind === "distill" ? t("dream.distillName") : t("dream.dreamName")}</span>
							<span className="dream-history__when">{fmtAgo(r)}</span>
							<span className="dream-history__tag">{r.trigger === "manual" ? t("dream.manual") : t("dream.auto")}</span>
							<span className="dream-history__state">{r.status === "ok" ? t("dream.ok") : r.status === "timeout" ? t("dream.timeout") : t("dream.failed")}</span>
							{r.error && <span className="dream-history__err" title={r.error}>{r.error}</span>}
						</div>
					))}
				</div>
			)}
		</section>
	);
}
