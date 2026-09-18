import { useCallback, useEffect, useRef, useState } from "react";
import logo from "../assets/logo.png";
import { useT, type Translator } from "../lib/i18n";
import { app } from "../lib/bridge";
import type { ProviderTemplate } from "../lib/types";
import { Loader2, RefreshCw } from "lucide-react";

// Three-step first-run wizard: pick vendor → paste key → pick default model.
// Replaces the old single-key-input overlay (which assumed a built-in provider
// that no longer exists after WP-3.0).
type Step = "vendor" | "key" | "model";

export function OnboardingOverlay({ onComplete }: { onComplete: () => void }) {
  const t = useT();
  const [step, setStep] = useState<Step>("vendor");
  const [templates, setTemplates] = useState<ProviderTemplate[]>([]);
  const [syncing, setSyncing] = useState(false);
  const [selected, setSelected] = useState<ProviderTemplate | null>(null);
  // The API key is entered in step 2 and consumed in step 3 (SetupProvider).
  // Lifted here so both steps share it without a global.
  const [apiKey, setApiKey] = useState("");
  const [loadErr, setLoadErr] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    app.GetProviderTemplates()
      .then((ts) => { if (!cancelled) setTemplates(ts); })
      .catch((e) => { if (!cancelled) setLoadErr(String(e)); });
    return () => { cancelled = true; };
  }, []);

  return (
    <div className="onboarding">
      <div className="onboarding__card onboarding__card--wide">
        <img src={logo} className="onboarding__logo" alt="Hiq" draggable={false} />
        <div className="onboarding__title">{t("onboarding.title")}</div>
        <div className="onboarding__dots" aria-hidden="true">
          {(["vendor", "key", "model"] as const).map((s) => (
            <span key={s} className={`onboarding__dot${step === s ? " onboarding__dot--on" : ""}`} />
          ))}
        </div>

        {loadErr && <div className="onboarding__error" role="alert">{loadErr}</div>}

        {step === "vendor" && (
          <VendorStep
            direct={templates.filter((t) => t.category === "direct")}
            aggregators={templates.filter((t) => t.category === "aggregator")}
            locals={templates.filter((t) => t.category === "local")}
            onPick={(tpl) => {
              setSelected(tpl);
              if (tpl.local) {
                // Keyless local endpoint (Ollama / llama.cpp) — no key step.
                setApiKey("");
                setStep("model");
              } else {
                setStep("key");
              }
            }}
            syncing={syncing}
            onSync={async () => {
              setSyncing(true);
              try {
                await app.RefreshRegistry();
                const fresh = await app.GetProviderTemplates();
                setTemplates(fresh);
              } catch (e) { console.error(e); }
              setSyncing(false);
            }}
            t={t}
          />
        )}

        {step === "key" && selected && (
          <KeyStep
            template={selected}
            apiKey={apiKey}
            onApiKeyChange={setApiKey}
            onBack={() => setStep("vendor")}
            onConnected={() => setStep("model")}
            t={t}
          />
        )}

        {step === "model" && selected && (
          <ModelStep
            template={selected}
            apiKey={apiKey}
            onDone={onComplete}
            t={t}
          />
        )}

        <button type="button" className="onboarding__skip" onClick={onComplete}>
          {t("onboarding.skip")}
        </button>
      </div>
    </div>
  );
}

// ── Step 1: vendor grid ───────────────────────────────────────────────────
// ── Step 1: provider card wall ────────────────────────────────────────────
// Two-column brand cards grouped 直连 / Coding Plan / 本地 (ui-redesign §4-E2).
// A click selects; the footer Next confirms — same contract as before, so the
// Settings "add provider" panel (which reuses these steps) gets the wall too.
// Brand marks are vendored from lobe-icons (MIT; see assets/providers/ATTRIBUTUTION.md);
// vendors without an upstream icon fall back to the monogram tile.
import anthropicSvg from "../assets/providers/anthropic.svg";
import openaiSvg from "../assets/providers/openai.svg";
import deepseekSvg from "../assets/providers/deepseek.svg";
import qwenSvg from "../assets/providers/qwen.svg";
import zhipuSvg from "../assets/providers/zhipu.svg";
import minimaxSvg from "../assets/providers/minimax.svg";
import moonshotSvg from "../assets/providers/moonshot.svg";
import doubaoSvg from "../assets/providers/doubao.svg";
import xaiSvg from "../assets/providers/xai.svg";
import ollamaSvg from "../assets/providers/ollama.svg";
import openrouterSvg from "../assets/providers/openrouter.svg";
import siliconcloudSvg from "../assets/providers/siliconcloud.svg";
import stepfunSvg from "../assets/providers/stepfun.svg";
import sparkSvg from "../assets/providers/spark.svg";
import wenxinSvg from "../assets/providers/wenxin.svg";
import hunyuanSvg from "../assets/providers/hunyuan.svg";

const PROVIDER_LOGOS: Record<string, string> = {
  anthropic: anthropicSvg,
  openai: openaiSvg,
  deepseek: deepseekSvg,
  qwen: qwenSvg,
  bailian: qwenSvg,
  "bailian-token": qwenSvg,
  "bailian-coding": qwenSvg,
  "qwen-coding": qwenSvg,
  zhipu: zhipuSvg,
  "zhipu-coding": zhipuSvg,
  minimax: minimaxSvg,
  moonshot: moonshotSvg,
  volcengine: doubaoSvg,
  "volcengine-coding": doubaoSvg,
  xai: xaiSvg,
  ollama: ollamaSvg,
  openrouter: openrouterSvg,
  siliconflow: siliconcloudSvg,
  stepfun: stepfunSvg,
  "stepfun-coding": stepfunSvg,
  xfyun: sparkSvg,
  "xfyun-coding": sparkSvg,
  "baidu-coding": wenxinSvg,
  "tencent-coding": hunyuanSvg,
};

function wallTileLetter(tpl: ProviderTemplate): string {
  const source = tpl.name || tpl.displayName || "?";
  const ascii = source.match(/[a-zA-Z]/);
  return (ascii ? ascii[0] : source[0] || "?").toUpperCase();
}

function wallShortUrl(url: string): string {
  return url.replace(/^https?:\/\//, "").replace(/\/.*$/, "");
}

export function VendorStep({ direct, aggregators, locals, onPick, syncing, onSync, t }: {
  direct: ProviderTemplate[];
  aggregators: ProviderTemplate[];
  locals: ProviderTemplate[];
  onPick: (tpl: ProviderTemplate) => void;
  syncing: boolean;
  onSync: () => void;
  t: Translator;
}) {
  const all = [...direct, ...aggregators, ...locals];
  const [value, setValue] = useState("");
  const selected = all.find((x) => x.name === value) ?? null;

  const groups = ([
    { key: "direct", label: t("onboarding.categoryDirect"), list: direct },
    { key: "aggregator", label: t("onboarding.categoryAggregator"), list: aggregators },
    { key: "local", label: t("onboarding.categoryLocal"), list: locals },
  ] as const).filter((g) => g.list.length > 0);

  return (
    <div className="onboarding__step">
      <div className="onboarding__wall-head">
        <div className="onboarding__tag">{t("onboarding.step1Hint")}</div>
        <button
          type="button"
          className="btn btn--small onboarding__sync"
          onClick={onSync}
          disabled={syncing}
        >
          {syncing ? <Loader2 size={12} className="onboarding__spinner" /> : <RefreshCw size={12} />}
          {t("settings.registryCheckUpdate") || "同步模型"}
        </button>
      </div>

      <div className="onboarding__wall" role="listbox" aria-label={t("onboarding.selectPlaceholder")}>
        {groups.map((group) => (
          <div key={group.key} className="onboarding__wall-group">
            <div className="onboarding__wall-group-label">{group.label}</div>
            <div className="onboarding__wall-grid">
              {group.list.map((tpl) => {
                const isSel = value === tpl.name;
                return (
                  <button
                    key={tpl.name}
                    type="button"
                    role="option"
                    aria-selected={isSel}
                    className={[
                      "onboarding__vcard",
                      isSel ? "onboarding__vcard--selected" : "",
                      tpl.local ? "onboarding__vcard--local" : "",
                    ].filter(Boolean).join(" ")}
                    onClick={() => setValue(tpl.name)}
                  >
                    <span className="onboarding__vcard-tile" aria-hidden="true">
                      {PROVIDER_LOGOS[tpl.name]
                        ? <img src={PROVIDER_LOGOS[tpl.name]} alt="" className="onboarding__vcard-logo" draggable={false} />
                        : wallTileLetter(tpl)}
                    </span>
                    <span className="onboarding__vcard-copy">
                      <span className="onboarding__vcard-name">{tpl.displayName}</span>
                      <span className="onboarding__vcard-status">
                        {tpl.local ? t("onboarding.noKeyNeeded") : tpl.codingOnly ? t("onboarding.badgeCoding") : wallShortUrl(tpl.baseUrl)}
                      </span>
                    </span>
                    <span className="onboarding__vcard-go" aria-hidden="true">›</span>
                  </button>
                );
              })}
            </div>
          </div>
        ))}
      </div>

      {selected && (
        <div className="onboarding__vendor-info">
          <div className="onboarding__endpoint">{selected.baseUrl}</div>
          {selected.local && <span className="onboarding__vendor-badge">{t("onboarding.badgeLocal")}</span>}
          {selected.vision && <span className="onboarding__vendor-badge">{t("onboarding.badgeVision")}</span>}
          {selected.codingOnly && <span className="onboarding__vendor-badge">{t("onboarding.badgeCoding")}</span>}
        </div>
      )}

      <div className="onboarding__actions">
        <button
          type="button"
          className="onboarding__submit"
          onClick={() => selected && onPick(selected)}
          disabled={!selected}
        >
          {t("onboarding.next")}
        </button>
      </div>
    </div>
  );
}

// ── Step 2: paste API key ─────────────────────────────────────────────────
export function KeyStep({ template, apiKey, onApiKeyChange, onBack, onConnected, t }: {
  template: ProviderTemplate;
  apiKey: string;
  onApiKeyChange: (v: string) => void;
  onBack: () => void;
  onConnected: () => void;
  t: Translator;
}) {
  const [state, setState] = useState<"idle" | "validating" | "error" | "verified">("idle");
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => { inputRef.current?.focus(); }, []);

  const submit = useCallback(async () => {
    const key = apiKey.trim();
    if (!key) {
      setError(t("onboarding.error.empty"));
      setState("error");
      return;
    }
    setState("validating");
    setError(null);
    try {
      await app.ProbeVendorKey(template.baseUrl, key);
      // Brief inline "verified" confirmation before advancing (ui-redesign §4-E2).
      setState("verified");
      window.setTimeout(onConnected, 650);
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      if (/invalid|401|403|unauthorized/i.test(msg)) {
        setError(t("onboarding.error.invalid"));
      } else if (/network|unreachable|timeout|dial/i.test(msg)) {
        setError(t("onboarding.error.network"));
      } else {
        setError(msg || t("onboarding.error.unknown"));
      }
      setState("error");
      inputRef.current?.focus();
      inputRef.current?.select();
    }
  }, [apiKey, template, onConnected, t]);

  return (
    <div className="onboarding__step">
      <div className="onboarding__vendor-picked">{template.displayName}</div>
      <div className="onboarding__endpoint">{template.baseUrl}</div>

      <label className="onboarding__label" htmlFor="onboarding-key">
        {t("onboarding.inputLabel")}
      </label>
      <input
        id="onboarding-key"
        ref={inputRef}
        className="onboarding__input"
        type="password"
        autoComplete="off"
        spellCheck={false}
        placeholder={t("onboarding.inputPlaceholder")}
        value={apiKey}
        onChange={(e) => { onApiKeyChange(e.target.value); if (state === "error") setState("idle"); }}
        onKeyDown={(e) => { if (e.key === "Enter" && state !== "validating" && state !== "verified") { e.preventDefault(); void submit(); } }}
        disabled={state === "validating" || state === "verified"}
      />

      {state === "verified" && (
        <div className="onboarding__verified" role="status">✓ {t("onboarding.verified")}</div>
      )}
      {state === "error" && error && (
        <div className="onboarding__error" role="alert">{error}</div>
      )}

      <div className="onboarding__actions">
        <button type="button" className="onboarding__back" onClick={onBack} disabled={state === "validating" || state === "verified"}>
          ← {t("onboarding.back")}
        </button>
        <button type="button" className="onboarding__submit" onClick={() => void submit()} disabled={state === "validating" || state === "verified"}>
          {state === "validating" ? (<><span className="onboarding__spinner" />{t("onboarding.validating")}</>) : state === "verified" ? t("onboarding.verified") : t("onboarding.connect")}
        </button>
      </div>

      {template.docUrl && (
        <a className="onboarding__doclink" href={template.docUrl} target="_blank" rel="noreferrer">
          {t("onboarding.getKey")} →
        </a>
      )}
    </div>
  );
}

// ── Step 3: pick default model ────────────────────────────────────────────
// Shows the template's preset model list (with role badges). Real-time model
// discovery happens later in Settings; here we use the curated preset so the
// user gets a working default in one shot. SetupProvider persists key + provider
// + default in one atomic call.
export function ModelStep({ template, apiKey, onDone, t }: {
  template: ProviderTemplate;
  apiKey: string;
  onDone: () => void;
  t: Translator;
}) {
  const [defaultPick, setDefaultPick] = useState<string>(template.defaultModel);
  const [visionPick, setVisionPick] = useState<string>(template.visionModel || template.defaultModel);
  const [fastPick, setFastPick] = useState<string>(template.fastModel || "follow");
  const [voicePick, setVoicePick] = useState<string>("none");
  const [state, setState] = useState<"ready" | "saving" | "error">("ready");
  const [error, setError] = useState<string | null>(null);

  // Local templates (Ollama, llama.cpp): list the models actually installed on
  // the running server via GET /v1/models — no key required. Falls back to the
  // preset list when the server is unreachable.
  const [fetchedModels, setFetchedModels] = useState<string[] | null>(null);
  const [fetchFailed, setFetchFailed] = useState(false);
  useEffect(() => {
    if (!template.local) return;
    let cancelled = false;
    app.FetchProviderModels({
      name: template.name,
      builtIn: false,
      added: false,
      kind: template.kind,
      baseUrl: template.baseUrl,
      models: [],
      modelsUrl: "",
      default: "",
      apiKeyEnv: template.apiKeyEnv,
      keySet: false,
      contextWindow: 0,
      reasoningProtocol: "",
      supportedEfforts: [],
      defaultEffort: "",
    })
      .then((ms) => { if (!cancelled && ms.length > 0) setFetchedModels(ms); })
      .catch(() => { if (!cancelled) setFetchFailed(true); });
    return () => { cancelled = true; };
  }, [template]);

  const models = fetchedModels ?? (template.models.length > 0 ? template.models : [template.defaultModel]);
  // models.dev-flagged reasoning models get a "推理" suffix in option labels.
  const reasoningSet = template.reasoningModels && template.reasoningModels.length > 0
    ? new Set(template.reasoningModels)
    : undefined;
  const modelLabel = (m: string) => (reasoningSet?.has(m) ? `${m} · ${t("settings.reasoningBadge")}` : m);

  // Once the live list lands, retarget picks that no longer exist in it.
  useEffect(() => {
    if (!fetchedModels || fetchedModels.length === 0) return;
    if (!fetchedModels.includes(defaultPick)) setDefaultPick(fetchedModels[0]);
    if (visionPick !== "none" && !fetchedModels.includes(visionPick)) setVisionPick("none");
    if (fastPick !== "follow" && !fetchedModels.includes(fastPick)) setFastPick("follow");
  }, [fetchedModels, defaultPick, visionPick, fastPick]);

  const finish = useCallback(async () => {
    setState("saving");
    setError(null);
    try {
      await app.SetupProvider(template, apiKey, defaultPick, visionPick, fastPick, voicePick);
      onDone();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setState("error");
    }
  }, [template, apiKey, defaultPick, visionPick, fastPick, voicePick, onDone]);

  return (
    <div className="onboarding__step">
      <div className="onboarding__tag">{t("onboarding.step3Hint")}</div>
      {template.local && fetchFailed && (
        <div className="onboarding__endpoint" role="status" style={{ marginTop: "0.5rem" }}>
          {t("onboarding.localFetchFailed")}
        </div>
      )}
      <div className="onboarding__model-selectors" style={{ display: "flex", flexDirection: "column", gap: "1rem", margin: "1.5rem 0" }}>
        <div>
          <label className="set-label" style={{ display: "block", marginBottom: "0.25rem" }}>{t("settings.defaultModel")}</label>
          <select className="mem-select" style={{ width: "100%" }} value={defaultPick} onChange={(e) => setDefaultPick(e.target.value)}>
            {models.map((m) => (
              <option key={m} value={m}>{modelLabel(m)}</option>
            ))}
          </select>
        </div>
        
        <div>
          <label className="set-label" style={{ display: "block", marginBottom: "0.25rem" }}>{t("settings.screenshotVlmLabel") || "图片识别模型"}</label>
          <select className="mem-select" style={{ width: "100%" }} value={visionPick} onChange={(e) => setVisionPick(e.target.value)}>
            <option value="none">{t("settings.screenshotVlmNone") || "未配置"}</option>
            {models.map((m) => (
              <option key={m} value={m}>{modelLabel(m)}</option>
            ))}
          </select>
        </div>

        <div>
          <label className="set-label" style={{ display: "block", marginBottom: "0.25rem" }}>{t("settings.voiceModelLabel") || "语音识别模型"}</label>
          <select className="mem-select" style={{ width: "100%" }} value={voicePick} onChange={(e) => setVoicePick(e.target.value)}>
            <option value="none">{t("settings.voiceModelNone") || "未配置"}</option>
            {models.map((m) => (
              <option key={m} value={m}>{modelLabel(m)}</option>
            ))}
          </select>
        </div>
        
        <div>
          <label className="set-label" style={{ display: "block", marginBottom: "0.25rem" }}>{t("settings.fastTaskModel") || "迅捷任务模型"}</label>
          <select className="mem-select" style={{ width: "100%" }} value={fastPick} onChange={(e) => setFastPick(e.target.value)}>
            <option value="follow">{t("settings.fastTaskNone") || "跟随默认"}</option>
            {models.map((m) => (
              <option key={m} value={m}>{modelLabel(m)}</option>
            ))}
          </select>
        </div>
      </div>

      {state === "error" && error && <div className="onboarding__error" role="alert">{error}</div>}

      <div className="onboarding__actions">
        <button type="button" className="onboarding__submit" onClick={() => void finish()} disabled={state === "saving"}>
          {state === "saving" ? (<><span className="onboarding__spinner" />{t("onboarding.saving")}</>) : t("onboarding.finish")}
        </button>
      </div>
    </div>
  );
}
