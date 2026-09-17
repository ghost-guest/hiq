import { useCallback, useEffect, useRef, useState } from "react";
import { Brain, Check, ChevronsUpDown } from "lucide-react";
import { asArray } from "../lib/array";
import { app } from "../lib/bridge";
import { useT } from "../lib/i18n";
import type { ModelInfo } from "../lib/types";
import { ANCHORED_POPOVER_CLOSE_MS, AnchoredPopover } from "./AnchoredPopover";


// ── ModelSwitcher (cascade) ─────────────────────────────────────────────
export function ModelSwitcher({ label, tabId, onPick }: { label: string; tabId?: string; onPick: (name: string) => void }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [closing, setClosing] = useState(false);
  const [models, setModels] = useState<ModelInfo[]>([]);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const closeTimerRef = useRef<number | null>(null);
  const triggerWidth = triggerRef.current?.getBoundingClientRect().width;

  const clearCloseTimer = useCallback(() => {
    if (closeTimerRef.current === null) return;
    window.clearTimeout(closeTimerRef.current);
    closeTimerRef.current = null;
  }, []);

  const openMenu = useCallback(() => {
    clearCloseTimer();
    setClosing(false);
    setOpen(true);
  }, [clearCloseTimer]);

  const closeMenu = useCallback((afterClose?: () => void) => {
    clearCloseTimer();
    setClosing(true);
    window.requestAnimationFrame(() => setOpen(false));
    const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    closeTimerRef.current = window.setTimeout(() => {
      closeTimerRef.current = null;
      setClosing(false);
      afterClose?.();
    }, reduceMotion ? 0 : ANCHORED_POPOVER_CLOSE_MS);
  }, [clearCloseTimer]);

  useEffect(() => {
    if (open) {
      (tabId ? app.ModelsForTab(tabId) : app.Models())
        .then((next) => setModels(asArray(next)))
        .catch(() => {});
    }
  }, [open, tabId]);

  useEffect(() => () => clearCloseTimer(), [clearCloseTimer]);

  const pick = (name: string) => {
    closeMenu(() => onPick(name));
  };

  // Group models by provider
  const grouped = new Map<string, ModelInfo[]>();
  for (const m of models) {
    const cat = m.provider || "other";
    if (!grouped.has(cat)) grouped.set(cat, []);
    grouped.get(cat)!.push(m);
  }

  const [hoverCat, setHoverCat] = useState<string | null>(null);
  const hoverTimerRef = useRef<number | null>(null);

  const enterCat = useCallback((catId: string) => {
    if (hoverTimerRef.current !== null) {
      window.clearTimeout(hoverTimerRef.current);
      hoverTimerRef.current = null;
    }
    setHoverCat(catId);
  }, []);

  const leaveCat = useCallback(() => {
    hoverTimerRef.current = window.setTimeout(() => {
      setHoverCat(null);
      hoverTimerRef.current = null;
    }, 150);
  }, []);

  useEffect(() => () => {
    if (hoverTimerRef.current !== null) window.clearTimeout(hoverTimerRef.current);
  }, []);

  // Only show categories that have models, derived from available providers
  const visibleCats = Array.from(grouped.keys()).map(provider => ({
    id: provider,
    label: providerLabel(provider)
  })).sort((a, b) => a.label.localeCompare(b.label));

  // Flat list when a single provider (a one-group cascade is pointless) or few
  // models overall; otherwise cascade submenus per provider. The flat list must
  // include EVERY provider's models — filtering to the first group here once
  // hid all other channels' models behind an alphabetically-lucky provider.
  const flatMode = visibleCats.length <= 1 || models.length <= 5;

  return (
    <div className="modelsw">
      <button
        ref={triggerRef}
        type="button"
        className="modelsw__trigger"
        aria-expanded={open && !closing}
        onClick={() => (open || closing ? closeMenu() : openMenu())}
      >
        <Brain size={13} className="modelsw__kind" />
        <span className="modelsw__label">{label}</span>
        <ChevronsUpDown size={11} />
      </button>
      <AnchoredPopover
        open={open}
        closing={closing}
        anchorRef={triggerRef}
        onClose={() => closeMenu()}
        className="modelsw__menu modelsw__menu--portal"
        style={{ minWidth: triggerWidth ? Math.max(triggerWidth, 180) : undefined, maxWidth: 400 }}
      >
        <div role="listbox">
          {models.length === 0 && <div className="modelsw__empty">{t("status.noModels")}</div>}

          {flatMode
            ? /* Flat list across ALL providers (rows carry a provider sub-label) */
              models.map((m) => <ModelRow key={m.ref} m={m} onPick={pick} />)
            : /* Multiple categories: cascade submenus */
              visibleCats.map((cat) => {
                const items = grouped.get(cat.id) ?? [];
                const isHovered = hoverCat === cat.id;
                return (
                  <div 
                    key={cat.id} 
                    className="modelsw__cascade" 
                    role="group" 
                    aria-label={cat.label}
                    onMouseEnter={() => enterCat(cat.id)}
                    onMouseLeave={leaveCat}
                  >
                    <div className="modelsw__cascade-head">
                      <span className="modelsw__cascade-label">{cat.label}</span>
                      <span className="modelsw__cascade-count">{items.length}</span>
                      <svg className="modelsw__cascade-arrow" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M6 4l4 4-4 4" /></svg>
                    </div>
                    {isHovered && (
                      <div className="modelsw__cascade-sub" role="listbox">
                        {items.map((m) => (
                          <ModelRow key={m.ref} m={m} onPick={pick} />
                        ))}
                      </div>
                    )}
                  </div>
                );
              })
          }
        </div>
      </AnchoredPopover>
    </div>
  );
}

// ── Single model row ────────────────────────────────────────────────────
function ModelRow({ m, onPick }: { m: ModelInfo; onPick: (ref: string) => void }) {
  return (
    <button
      type="button"
      role="option"
      aria-selected={m.current}
      className={`modelsw__item ${m.current ? "modelsw__item--current" : ""}`}
      onClick={() => onPick(m.ref)}
    >
      <span className="modelsw__copy">
        <span className="modelsw__model" title={m.model}>{m.model}</span>
        <span className="modelsw__provider" title={providerLabel(m.provider)}>{providerLabel(m.provider)}</span>
      </span>
      {m.current && <Check size={13} className="modelsw__check" />}
    </button>
  );
}

function providerLabel(provider: string): string {
  switch (provider) {
    case "openai":        return "OpenAI";
    case "minimax":       return "MiniMax";
    case "zai":           return "Z.ai";
    case "deepseek":      return "DeepSeek";
    case "moonshot":      return "Moonshot";
    default:              return provider;
  }
}
