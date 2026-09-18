// CutoverView — 割接模式前端（NETDEV_SPEC_V2 §7.2）：整份 runbook 的总倒计
// 时 + 步骤 + 语义验证门 + 回退决策点。按 §10 契约不开第四个工作台——它
// 是「对话」主区里的任务过程视图（Esc 返回对话），入口在「提案」页签。
// 决策永远是人按的：hold 状态出现 [继续] 与 [回退] 并排，回退走提案回滚
// 全链路（首败冻结 + 备份恢复），结束后可导出前后基线对比报告。
import { useCallback, useEffect, useMemo, useState } from "react";
import { app } from "../../lib/bridge";
import { t as tt } from "../../lib/i18n";
import type { NetDevCutoverRun, NetDevCutoverStep, NetDevProposal } from "../../lib/types";

const STEP_MARK: Record<string, string> = {
  pending: "⬜",
  running: "…",
  gating: "⏳",
  done: "✅",
  approved: "✅",
  failed: "❌",
  "rolled-back": "↩️",
  skipped: "⏭",
};

const STATUS_LABEL: Record<string, string> = {
  running: "ndv.cut.stRunning",
  hold: "ndv.cut.stHold",
  done: "ndv.cut.stDone",
  failed: "ndv.cut.stFailed",
  aborted: "ndv.cut.stAborted",
};

function fmtCountdown(ms: number): string {
  if (ms <= 0) return tt("ndv.cut.exhausted");
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${String(sec).padStart(2, "0")}` : `${m}:${String(sec).padStart(2, "0")}`;
}

export function CutoverView({
  runId,
  proposals,
  devices,
  onClose,
}: {
  runId: string; // "" → creation form; otherwise the run to watch
  proposals: NetDevProposal[];
  devices: { name: string; vendor: string }[];
  onClose: () => void;
}) {
  // The creation form "graduates" into watching the run it started without a
  // parent re-render: watchId tracks the effective run.
  const [watchId, setWatchId] = useState(runId);
  useEffect(() => setWatchId(runId), [runId]);
  const [run, setRun] = useState<NetDevCutoverRun | null>(null);
  const [busy, setBusy] = useState("");
  const [err, setErr] = useState("");
  const [now, setNow] = useState(Date.now());

  const reload = useCallback(async () => {
    if (!watchId) return;
    try {
      setRun(await app.NetDevCutoverGet(watchId));
      setErr("");
    } catch (e) {
      setErr(String(e));
    }
  }, [watchId]);

  useEffect(() => {
    void reload();
  }, [reload]);

  // 活跃割接 2s 轮询 + 秒级倒计时；已结束 15s 兜底刷新。
  useEffect(() => {
    const tick = setInterval(() => setNow(Date.now()), 1000);
    const poll = setInterval(() => void reload(), run?.status === "running" || run?.status === "hold" ? 2000 : 15000);
    return () => {
      clearInterval(tick);
      clearInterval(poll);
    };
  }, [reload, run?.status]);

  const countdown = useMemo(() => (run ? fmtCountdown(new Date(run.deadline).getTime() - now) : ""), [run, now]);

  const act = async (label: string, fn: () => Promise<unknown>) => {
    setBusy(label);
    try {
      await fn();
      await reload();
    } catch (e) {
      setErr(String(e));
    } finally {
      setBusy("");
    }
  };

  if (!watchId) {
    return <CutoverCreate proposals={proposals} devices={devices} onCreated={(id) => setWatchId(id)} />;
  }
  if (!run) {
    return <div className="ndv__card"><div className="ndv__hint">{err || tt("ndv.loading")}</div></div>;
  }

  const hold = run.status === "hold";
  return (
    <div className="ndv-cutover">
      <div className="ndv-cutover__head">
        <span className="ndv-cutover__name">🌗 {run.name}</span>
        <span className={`ndv__badge ${run.status === "running" ? "" : run.status === "hold" ? "ndv__badge--warn" : ""}`}>
          {tt(STATUS_LABEL[run.status] as never) ?? run.status}
        </span>
        <span className={`ndv-cutover__count ${new Date(run.deadline).getTime() - now < 5 * 60_000 ? "ndv-cutover__count--low" : ""}`}>
          {"⏱ "}{tt("ndv.cut.countdown")} {countdown}
        </span>
        <span className="terminal-panel__spacer" />
        {run.report && (
          <span className="btn btn--secondary btn--small" role="button" onClick={() => void act("report", () => app.NetDevCutoverReport(run.id))}>
            {tt("ndv.cut.exportReport")}
          </span>
        )}
        {(run.status === "running" || run.status === "hold") && (
          <span className="btn btn--secondary btn--small" role="button" title={tt("ndv.cut.enterBoard")}
            onClick={() => { window.dispatchEvent(new CustomEvent("hiq:netdev-open-screen", { detail: { screen: "cutover" } })); }}>
            {tt("ndv.cut.enterBoard")}
          </span>
        )}
        <span className="btn btn--secondary btn--small" role="button" onClick={onClose}>{tt("ndv.cut.back")}</span>
      </div>

      {err && <div className="ndv__hint">{err}</div>}

      {hold && (
        <div className="ndv-cutover__hold">
          <div className="ndv-cutover__hold-note">{run.hold_note || tt("ndv.cut.holdDefault")}</div>
          <div className="ndv-cutover__hold-actions">
            <span className="btn btn--primary btn--small" role="button" onClick={() => void act("continue", () => app.NetDevCutoverContinue(run.id))}>
              {busy === "continue" ? tt("ndv.cut.continuing") : tt("ndv.cut.continue")}
            </span>
            <span
              className="btn btn--danger btn--small"
              role="button"
              title={tt("ndv.cut.rollbackTip")}
              onClick={() => void act("rollback", () => app.NetDevCutoverRollback(run.id))}
            >
              {busy === "rollback" ? tt("ndv.cut.rollingBack") : tt("ndv.cut.rollback")}
            </span>
          </div>
          <div className="ndv__hint">{tt("ndv.cut.rollbackHint")}</div>
        </div>
      )}
      {run.status === "running" && (
        <div className="ndv-cutover__hold ndv-cutover__hold--running">
          <span className="ndv__hint">{tt("ndv.cut.runningHint")}</span>
          <span className="btn btn--secondary btn--small" role="button" onClick={() => void act("abort", () => app.NetDevCutoverAbort(run.id))}>
            {busy === "abort" ? tt("ndv.cut.aborting") : tt("ndv.jobs.abort")}
          </span>
        </div>
      )}

      <div className="ndv-cutover__steps">
        {(run.steps ?? []).map((s, i) => (
          <CutoverStepRow key={i} step={s} active={run.status === "running" && i === run.cursor} />
        ))}
      </div>

      {run.report && (
        <div className="ndv-cutover__report">
          <div className="ndv__card-title">{tt("ndv.cut.reportTitle")}</div>
          <pre className="ndv-cutover__report-body">{run.report}</pre>
        </div>
      )}
    </div>
  );
}

function CutoverStepRow({ step, active }: { step: NetDevCutoverStep; active: boolean }) {
  return (
    <div className={`ndv-cutover__step${active ? " ndv-cutover__step--active" : ""}`}>
      <span className="ndv-cutover__step-mark">{STEP_MARK[step.status] ?? "⬜"}</span>
      <div className="ndv-cutover__step-body">
        <div className="ndv-cutover__step-title">
          {step.label}
          {step.proposal_id && <span className="ndv__meta"> · {tt("ndv.cut.proposalRef", { id: step.proposal_id })}</span>}
          {step.est_sec ? <span className="ndv__meta"> · {tt("ndv.cut.estSec", { n: step.est_sec })}</span> : null}
          {step.decision_point && <span className="ndv__meta"> · {"⚑ "}{tt("ndv.cut.decisionPoint")}{step.impact ? `（${step.impact}）` : ""}</span>}
        </div>
        {(step.device || step.command) && (
          <div className="ndv__meta ndv__audit-cmd">{step.device} ❯ {step.command}</div>
        )}
        {step.gate && (
          <div className="ndv__meta">
            {"⏳ "}{tt("ndv.cut.gate", { dev: step.gate.device, cmd: step.gate.command, expect: step.gate.expect, n: step.gate.sustain_sec ?? 30 })}
          </div>
        )}
        {step.error && <div className="ndv__hint ndv__hint--err">{step.error}</div>}
      </div>
    </div>
  );
}

// ── creation form ────────────────────────────────────────────────────────────

interface DraftStep {
  label: string;
  proposalId: string;
  gateCmd: string;
  gateExpect: string;
  gateSustain: string;
  decision: boolean;
  impact: string;
}

function CutoverCreate({
  proposals,
  devices,
  onCreated,
}: {
  proposals: NetDevProposal[];
  devices: { name: string; vendor: string }[];
  onCreated: (id: string) => void;
}) {
  const approved = proposals.filter((p) => p.status === "approved");
  const [name, setName] = useState("");
  const [minutes, setMinutes] = useState("120");
  const [sel, setSel] = useState<Record<string, DraftStep>>({});
  const [checkCmd, setCheckCmd] = useState<DeviceCommand>({ device: "", command: "", expect: "", sustain: "30" });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const toggle = (p: NetDevProposal) => {
    setSel((prev) => {
      const next = { ...prev };
      if (next[p.id]) delete next[p.id];
      else next[p.id] = { label: p.intent.slice(0, 24) || p.id, proposalId: p.id, gateCmd: "", gateExpect: "", gateSustain: "30", decision: true, impact: "" };
      return next;
    });
  };

  const start = async () => {
    setBusy(true);
    setErr("");
    try {
      const steps: NetDevCutoverStep[] = [];
      for (const d of Object.values(sel)) {
        const p = proposals.find((x) => x.id === d.proposalId);
        const gateDevice = p?.steps[0]?.device ?? devices[0]?.name ?? "";
        steps.push({
          label: d.label,
          proposal_id: d.proposalId,
          est_sec: 60,
          decision_point: d.decision,
          impact: d.impact || undefined,
          status: "",
          gate:
            d.gateCmd && d.gateExpect
              ? { device: gateDevice, command: d.gateCmd, expect: d.gateExpect, sustain_sec: Number(d.gateSustain) || 30 }
              : undefined,
        });
      }
      if (checkCmd.device && checkCmd.command && checkCmd.expect) {
        steps.push({
          label: tt("ndv.cut.verifyLabel"),
          device: checkCmd.device,
          command: checkCmd.command,
          est_sec: Number(checkCmd.sustain) || 30,
          status: "",
          gate: { device: checkCmd.device, command: checkCmd.command, expect: checkCmd.expect, sustain_sec: Number(checkCmd.sustain) || 30 },
        });
      }
      const created = await app.NetDevCutoverStart({
        id: "",
        name,
        deadline: new Date(Date.now() + (Number(minutes) || 120) * 60_000).toISOString(),
        steps,
        status: "",
        cursor: 0,
        created_at: "",
      });
      onCreated(created.id);
    } catch (e) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="ndv-cutover ndv-cutover--create">
      <div className="ndv__card">
        <div className="ndv__card-title">{tt("ndv.cut.createTitle")}</div>
        <div className="ndv__hint">
          {tt("ndv.cut.createHint1")}
          {tt("ndv.cut.createHint2")}<b>{tt("ndv.cut.approvedOnly1")}</b>{tt("ndv.cut.approvedOnly2")}
        </div>
        <div style={{ display: "flex", gap: 8, marginTop: 8 }}>
          <input className="mem-input" style={{ flex: 2 }} placeholder={tt("ndv.cut.phName")} value={name} onChange={(e) => setName(e.target.value)} />
          <input className="mem-input" style={{ width: 120 }} placeholder={tt("ndv.cut.phWindow")} value={minutes} onChange={(e) => setMinutes(e.target.value)} />
        </div>

        <div className="ndv__group-label" style={{ marginTop: 10 }}>{tt("ndv.cut.approvedProposals")}</div>
        {approved.length === 0 && <div className="ndv__hint">{tt("ndv.cut.noApproved")}</div>}
        {approved.map((p) => {
          const d = sel[p.id];
          return (
            <div key={p.id} className={`ndv-cutover__pick${d ? " ndv-cutover__pick--on" : ""}`} role="button" onClick={() => toggle(p)}>
              <div className="ndv-cutover__pick-head">
                <span>{d ? "☑" : "☐"} {p.id}</span>
                <span className="ndv__meta">{p.intent.slice(0, 60)}</span>
              </div>
              {d && (
                <div className="ndv-cutover__pick-form" onClick={(e) => e.stopPropagation()}>
                  <input className="mem-input" placeholder={tt("ndv.cut.phStepLabel")} value={d.label} onChange={(e) => setSel(prev => ({ ...prev, [p.id]: { ...d, label: e.target.value } }))} />
                  <input className="mem-input" placeholder={tt("ndv.cut.phImpact")} value={d.impact} onChange={(e) => setSel(prev => ({ ...prev, [p.id]: { ...d, impact: e.target.value } }))} />
                  <div style={{ display: "flex", gap: 6 }}>
                    <input className="mem-input" placeholder={tt("ndv.cut.phGateCmd")} value={d.gateCmd} onChange={(e) => setSel(prev => ({ ...prev, [p.id]: { ...d, gateCmd: e.target.value } }))} />
                    <input className="mem-input" style={{ width: 140 }} placeholder={tt("ndv.cut.phGateExpect")} value={d.gateExpect} onChange={(e) => setSel(prev => ({ ...prev, [p.id]: { ...d, gateExpect: e.target.value } }))} />
                    <input className="mem-input" style={{ width: 80 }} placeholder={tt("ndv.cut.phSustain")} value={d.gateSustain} onChange={(e) => setSel(prev => ({ ...prev, [p.id]: { ...d, gateSustain: e.target.value } }))} />
                  </div>
                  <label className="ndv__meta">
                    <input type="checkbox" checked={d.decision} onChange={(e) => setSel(prev => ({ ...prev, [p.id]: { ...d, decision: e.target.checked } }))} />
                    {tt("ndv.cut.stopAtDecision")}
                  </label>
                </div>
              )}
            </div>
          );
        })}

        <div className="ndv__group-label" style={{ marginTop: 10 }}>{tt("ndv.cut.verifySection")}</div>
        <div style={{ display: "flex", gap: 6 }}>
          <select className="mem-input" value={checkCmd.device} onChange={(e) => setCheckCmd({ ...checkCmd, device: e.target.value })}>
            <option value="">{tt("ndv.cut.pickDevice")}</option>
            {devices.map((d) => (
              <option key={d.name} value={d.name}>{d.name}</option>
            ))}
          </select>
          <input className="mem-input" style={{ flex: 1 }} placeholder={tt("ndv.cut.phVerifyCmd")} value={checkCmd.command} onChange={(e) => setCheckCmd({ ...checkCmd, command: e.target.value })} />
          <input className="mem-input" style={{ width: 140 }} placeholder={tt("ndv.cut.phExpect")} value={checkCmd.expect} onChange={(e) => setCheckCmd({ ...checkCmd, expect: e.target.value })} />
          <input className="mem-input" style={{ width: 80 }} placeholder={tt("ndv.cut.phSustain")} value={checkCmd.sustain} onChange={(e) => setCheckCmd({ ...checkCmd, sustain: e.target.value })} />
        </div>

        {err && <div className="ndv__hint ndv__hint--err">{err}</div>}
        <div style={{ marginTop: 10 }}>
          <span
            className="btn btn--primary btn--small"
            role="button"
            style={busy || !name || Object.keys(sel).length === 0 ? { opacity: 0.5 } : undefined}
            onClick={() => { if (!busy && name && Object.keys(sel).length > 0) void start(); }}
          >
            {busy ? tt("ndv.cut.starting") : tt("ndv.cut.startBtn")}
          </span>
        </div>
      </div>
    </div>
  );
}

interface DeviceCommand {
  device: string;
  command: string;
  expect: string;
  sustain: string;
}
